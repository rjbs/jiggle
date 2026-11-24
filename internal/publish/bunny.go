package publish

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Bunny publishes a site to a Bunny storage zone, and purges its CDN.
//
// The site is published by copying it into a storage zone, through Bunny's
// storage API, and a pull zone serves it.
//
// What to copy isn't found by comparing files with the storage zone, which
// would mean listing a directory per photo.  Instead, the build's manifest
// (see the site package's Writer) says what each path in the site holds,
// and this keeps a record of what each path held when it was last
// uploaded.  A path whose entries differ is uploaded; a path in the record
// and not the manifest is deleted.  The record is updated as each upload or
// deletion succeeds, so an interrupted sync picks up where it stopped.
// Anything put in the zone by other means is left alone.
//
// Bunny serves bunnycdn_errors/404.html for a missing path, so the site's
// 404.html is uploaded there too.
//
// Pages go live only after what they refer to: every other file
// (stylesheets, scripts, renditions, data) is uploaded first, and the HTML
// only once all of those have succeeded.  Otherwise a page could go live
// showing a new photo whose renditions aren't there yet.  Likewise, every
// deletion waits until every upload has succeeded, so the live site never
// links to something already gone.  If any upload fails, nothing is
// deleted.
//
// Then the CDN's cache is purged of every URL changed or deleted, on each
// of the pull zone's hostnames (or PurgeHosts, if given).  That's required,
// not just tidy: a rendition's URL stays the same when it's remade (say,
// rotated), and a photo made private must stop being served from the
// cache, not just from storage.
//
// Bunny limits purging: exact URLs to a burst of 120, then 5 a second, and
// prefixes to a burst of 20, then one every two seconds.  A template change
// rewrites every page, which would be tens of thousands of exact purges, so
// changes are grouped by their top-level directory.  A directory with more
// than PrefixOver changed files is purged by prefix, as one request.  Other
// changes are purged by exact URL; a directory's index.html is served at
// three URLs (dir/, dir, and dir/index.html), and all three are purged.
// Renditions are published apart from pages (under /img/), so a change to
// every page purges no renditions.  When Bunny says to slow down, purging
// waits and tries again.
//
// With PurgeAll, the whole pull zone is purged instead, which is one
// request.
//
// This is Perl jiggle's Jiggle::Sync::Bunny, and reads and writes the same
// record, so either can carry on from the other.
type Bunny struct {
	// SiteDir is the built site.  Manifest is the record the build left
	// of what it made, and Record is this type's own, of what it
	// uploaded.
	SiteDir  string
	Manifest string
	Record   string

	// Zone is the storage zone, Password its password, and StorageURL
	// the storage API of its region.
	Zone       string
	Password   string
	StorageURL string

	// APIKey is the account's, for purging, and PullZoneID the pull
	// zone that serves the storage zone.
	APIKey     string
	PullZoneID string
	APIURL     string

	// PurgeHosts are the hostnames whose URLs are purged.  Bunny
	// refuses (with a 404) to purge a URL on a hostname that isn't the
	// pull zone's, so if there are none here, they are asked of the
	// pull zone itself.  Its record also holds certificate keys, so
	// it's never logged.
	PurgeHosts []string

	// Concurrency is how many requests are under way at once, 8 if
	// zero.  A request that fails is made Attempts times in all, 3 if
	// zero, waiting RetryDelay, then twice that, and so on, between
	// them: 2 seconds if zero.
	Concurrency int
	Attempts    int
	RetryDelay  time.Duration

	// PrefixOver is how many changed files a top-level directory may
	// have before it's purged by prefix: 10 if zero.
	PrefixOver int

	// RateLimitWait is how long to wait when Bunny answers a purge
	// with 429 and no Retry-After, 2 seconds if zero, and
	// RateLimitTries how many times to try before giving up, 60 if
	// zero.
	RateLimitWait  time.Duration
	RateLimitTries int

	Log  func(string)
	HTTP *http.Client

	mu     sync.Mutex
	record map[string]entry
}

// An entry is what a manifest or a record says of one path: the SHA-1 of
// content that was written there, or the source and key of a link.
type entry struct {
	SHA1 string `json:"sha1,omitempty"`
	Link string `json:"link,omitempty"`
	Key  string `json:"key,omitempty"`
}

type entriesFile struct {
	Version int              `json:"version"`
	Files   map[string]entry `json:"files"`
}

// SyncOptions are how a sync is to be done.
type SyncOptions struct {
	// DryRun returns what would be done, having done nothing.
	DryRun bool

	// PurgeAll purges the whole pull zone, even if nothing else needed
	// doing.
	PurgeAll bool
}

// SyncResult is what a sync did: the paths it uploaded and deleted, and how
// many purge requests it made, or that it purged everything.
type SyncResult struct {
	Uploaded  []string
	Deleted   []string
	Purged    int
	PurgedAll bool
}

func (b *Bunny) defaults() {
	if b.StorageURL == "" {
		b.StorageURL = "https://storage.bunnycdn.com"
	}
	if b.APIURL == "" {
		b.APIURL = "https://api.bunny.net"
	}
	if b.Concurrency == 0 {
		b.Concurrency = 8
	}
	if b.Attempts == 0 {
		b.Attempts = 3
	}
	if b.RetryDelay == 0 {
		b.RetryDelay = 2 * time.Second
	}
	if b.PrefixOver == 0 {
		b.PrefixOver = 10
	}
	if b.RateLimitWait == 0 {
		b.RateLimitWait = 2 * time.Second
	}
	if b.RateLimitTries == 0 {
		b.RateLimitTries = 60
	}
	if b.Log == nil {
		b.Log = func(string) {}
	}
	if b.HTTP == nil {
		b.HTTP = &http.Client{Timeout: 10 * time.Minute}
	}
}

func loadEntries(path string) (map[string]entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file entriesFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if file.Files == nil {
		file.Files = map[string]entry{}
	}
	return file.Files, nil
}

// saveRecord writes the record, whole, and then puts it in place, so that
// there is never half a record: it is the only account there is of what's
// in the storage zone.
func (b *Bunny) saveRecord() error {
	data, err := json.Marshal(entriesFile{Version: 1, Files: b.record})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(b.Record), 0o755); err != nil {
		return err
	}
	partial := b.Record + ".partial"
	if err := os.WriteFile(partial, data, 0o644); err != nil {
		return err
	}
	return os.Rename(partial, b.Record)
}

// Remote paths that get a copy of some other file in the site.
var copyOf = map[string]string{"bunnycdn_errors/404.html": "404.html"}

func (b *Bunny) source(rel string) string {
	if of, ok := copyOf[rel]; ok {
		rel = of
	}
	return filepath.Join(b.SiteDir, filepath.FromSlash(rel))
}

func isPage(rel string) bool { return strings.HasSuffix(rel, ".html") }

// A plan is what a sync has to do.
type plan struct {
	// upload is the paths that need uploading, in the order they'll be
	// uploaded: pages last.  delete is the paths that need deleting,
	// sorted.
	upload, delete []string

	// want is what the manifest says of every path.
	want map[string]entry
}

// plan works out what needs doing, and loads the record.
//
// A site with no manifest can't be synced: nothing says what it holds, and
// to take that for a site that holds nothing would be to delete everything
// that was ever uploaded.  No record is another matter: it means nothing
// has been uploaded yet.
func (b *Bunny) plan() (*plan, error) {
	want, err := loadEntries(b.Manifest)
	if err != nil {
		return nil, fmt.Errorf("reading the build's manifest: %w", err)
	}
	have, err := loadEntries(b.Record)
	if errors.Is(err, os.ErrNotExist) {
		b.Log("there's no record of earlier uploads at " + b.Record + ", so everything will be uploaded")
		have, err = map[string]entry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the record of uploads: %w", err)
	}
	b.record = have

	for copied, of := range copyOf {
		if e, ok := want[of]; ok {
			want[copied] = e
		}
	}

	p := &plan{want: want}
	var pages []string
	for _, rel := range sortedKeys(want) {
		if had, ok := have[rel]; ok && had == want[rel] {
			continue
		}
		if isPage(rel) {
			pages = append(pages, rel)
		} else {
			p.upload = append(p.upload, rel)
		}
	}
	p.upload = append(p.upload, pages...)
	for _, rel := range sortedKeys(have) {
		if _, ok := want[rel]; !ok {
			p.delete = append(p.delete, rel)
		}
	}
	return p, nil
}

func sortedKeys(m map[string]entry) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Sync does the work described above.  If any request fails, it returns an
// error, and a result all the same: what it did get done, which it has
// recorded.
func (b *Bunny) Sync(ctx context.Context, opts SyncOptions) (*SyncResult, error) {
	b.defaults()
	p, err := b.plan()
	if err != nil {
		return nil, err
	}
	result := &SyncResult{Uploaded: p.upload, Deleted: p.delete}
	if opts.DryRun {
		return result, nil
	}
	if len(p.upload) == 0 && len(p.delete) == 0 {
		if opts.PurgeAll {
			if err := b.purgeAll(ctx); err != nil {
				return result, err
			}
			result.PurgedAll = true
		}
		return result, nil
	}

	saved := 0
	recorded := func(rel string, e *entry) {
		b.mu.Lock()
		defer b.mu.Unlock()
		if e != nil {
			b.record[rel] = *e
		} else {
			delete(b.record, rel)
		}
		if saved++; saved%200 == 0 {
			if err := b.saveRecord(); err != nil {
				b.Log("saving the record of uploads: " + err.Error())
			}
		}
	}
	uploadOne := func(ctx context.Context, rel string) error {
		if err := b.upload(ctx, rel); err != nil {
			return err
		}
		e := p.want[rel]
		recorded(rel, &e)
		return nil
	}

	var files, pages []string
	for _, rel := range p.upload {
		if isPage(rel) {
			pages = append(pages, rel)
		} else {
			files = append(files, rel)
		}
	}

	failed := map[string]bool{}
	fail := func(paths []string) {
		for _, rel := range paths {
			failed[rel] = true
		}
	}
	fail(b.each(ctx, "uploading files", files, uploadOne))
	filesFailed := len(failed) > 0
	tried := files
	if !filesFailed {
		fail(b.each(ctx, "uploading pages", pages, uploadOne))
		tried = p.upload
	}
	uploadFailed := len(failed) > 0

	if !uploadFailed {
		fail(b.each(ctx, "deleting", p.delete, func(ctx context.Context, rel string) error {
			if err := b.delete(ctx, rel); err != nil {
				return err
			}
			recorded(rel, nil)
			return nil
		}))
	}

	if err := b.saveRecord(); err != nil {
		return result, fmt.Errorf("saving the record of uploads: %w", err)
	}

	// Whatever did get uploaded or deleted is purged even if something
	// failed, so the cache doesn't go on serving what was replaced or
	// removed until the next sync.  That goes for a sync that was
	// stopped, too: the record says those paths are done, so no later
	// sync would purge them.
	result.Uploaded, result.Deleted = without(tried, failed), nil
	if !uploadFailed {
		result.Deleted = without(p.delete, failed)
	}
	stopped := ctx.Err()
	if stopped != nil {
		b.Log("stopped; purging what was changed before stopping")
		ctx = context.WithoutCancel(ctx)
	}
	if opts.PurgeAll {
		err = b.purgeAll(ctx)
		result.PurgedAll = err == nil
	} else {
		result.Purged, err = b.purge(ctx, append(append([]string{}, result.Uploaded...), result.Deleted...))
	}

	if stopped != nil {
		failure := fmt.Errorf("stopped with %d upload(s) and deletion(s) not done; sync again to finish", len(failed))
		if err != nil {
			failure = fmt.Errorf("%w\nand then: %w", failure, err)
		}
		return result, failure
	}
	if len(failed) > 0 {
		so := ""
		switch {
		case filesFailed:
			so = ", so no pages were uploaded and nothing was deleted"
		case uploadFailed:
			so = ", so nothing was deleted"
		}
		names := make([]string, 0, len(failed))
		for rel := range failed {
			names = append(names, rel)
		}
		sort.Strings(names)
		failure := fmt.Errorf("%d request(s) failed%s; sync again to retry:\n  %s", len(failed), so, strings.Join(names, "\n  "))
		if err != nil {
			failure = fmt.Errorf("%w\nand then: %w", failure, err)
		}
		return result, failure
	}
	return result, err
}

func without(paths []string, failed map[string]bool) []string {
	out := []string{}
	for _, rel := range paths {
		if !failed[rel] {
			out = append(out, rel)
		}
	}
	return out
}

// each runs do for every path, some at once, trying again what fails.  It
// returns the paths that failed every attempt, sorted.  If ctx is done, the
// paths not yet begun fail without being tried.
func (b *Bunny) each(ctx context.Context, label string, paths []string, do func(context.Context, string) error) []string {
	if len(paths) == 0 {
		return nil
	}
	progress := newProgress(label, len(paths), b.Log)

	var mu sync.Mutex
	var failed []string
	var wg sync.WaitGroup
	next := make(chan string)
	for range min(b.Concurrency, len(paths)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for rel := range next {
				if err := b.retrying(ctx, func() error { return do(ctx, rel) }); err != nil {
					// When the sync is being stopped, everything
					// left fails, and there's no use in saying
					// so of each.
					if ctx.Err() == nil {
						b.Log(label + " " + rel + ": " + err.Error())
					}
					mu.Lock()
					failed = append(failed, rel)
					mu.Unlock()
				}
				progress.tick()
			}
		}()
	}
	for _, rel := range paths {
		next <- rel
	}
	close(next)
	wg.Wait()

	progress.done()
	sort.Strings(failed)
	return failed
}

func (b *Bunny) retrying(ctx context.Context, do func() error) error {
	var err error
	for attempt := 1; ; attempt++ {
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = do(); err == nil || attempt >= b.Attempts {
			return err
		}
		if !sleep(ctx, b.RetryDelay*time.Duration(attempt)) {
			return err
		}
	}
}

// sleep waits, and returns false if ctx was done first.
func sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// escape encodes one step of a path for a URL, leaving only letters,
// digits, and "-._~" as they are, which is what Perl jiggle did, and what
// Bunny then decodes.
func escape(step string) string {
	var out strings.Builder
	for i := 0; i < len(step); i++ {
		c := step[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', strings.IndexByte("-._~", c) >= 0:
			out.WriteByte(c)
		default:
			fmt.Fprintf(&out, "%%%02X", c)
		}
	}
	return out.String()
}

func (b *Bunny) storageURL(rel string) string {
	steps := []string{escape(b.Zone)}
	for _, step := range strings.Split(rel, "/") {
		steps = append(steps, escape(step))
	}
	return strings.TrimRight(b.StorageURL, "/") + "/" + strings.Join(steps, "/")
}

// send makes a request with an access key, and returns its response if it
// was answered at all, with the body read and closed.
func (b *Bunny) send(req *http.Request, key string) (*http.Response, error) {
	req.Header.Set("AccessKey", key)
	res, err := b.HTTP.Do(req)
	if err != nil {
		// The error names the URL, and a purge's URL has nothing
		// secret in it, but there's no need for either.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return nil, err
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	return res, nil
}

func ok(res *http.Response) bool { return res.StatusCode >= 200 && res.StatusCode < 300 }

func (b *Bunny) upload(ctx context.Context, rel string) error {
	file, err := os.Open(b.source(rel))
	if err != nil {
		return err
	}
	defer file.Close()

	// The checksum makes Bunny refuse an upload that arrives damaged.
	digest := sha256.New()
	size, err := io.Copy(digest, file)
	if err != nil {
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "PUT", b.storageURL(rel), file)
	if err != nil {
		return err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Checksum", strings.ToUpper(hex.EncodeToString(digest.Sum(nil))))
	res, err := b.send(req, b.Password)
	if err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	if !ok(res) {
		return fmt.Errorf("upload: %s", res.Status)
	}
	return nil
}

func (b *Bunny) delete(ctx context.Context, rel string) error {
	req, err := http.NewRequestWithContext(ctx, "DELETE", b.storageURL(rel), nil)
	if err != nil {
		return err
	}
	res, err := b.send(req, b.Password)
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	// Already gone is as good as deleted.
	if !ok(res) && res.StatusCode != http.StatusNotFound {
		return fmt.Errorf("delete: %s", res.Status)
	}
	return nil
}

// purgeHosts returns the hostnames whose URLs are to be purged.
func (b *Bunny) purgeHosts(ctx context.Context) ([]string, error) {
	if len(b.PurgeHosts) > 0 {
		return b.PurgeHosts, nil
	}
	hosts, err := func() ([]string, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", b.APIURL+"/pullzone/"+url.PathEscape(b.PullZoneID), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("AccessKey", b.APIKey)
		res, err := b.HTTP.Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		if !ok(res) {
			return nil, errors.New(res.Status)
		}
		var zone struct {
			Hostnames []struct{ Value string }
		}
		if err := json.NewDecoder(res.Body).Decode(&zone); err != nil {
			return nil, err
		}
		var hosts []string
		for _, h := range zone.Hostnames {
			hosts = append(hosts, h.Value)
		}
		sort.Strings(hosts)
		return hosts, nil
	}()
	if err != nil {
		return nil, fmt.Errorf("can't get the pull zone's hostnames: %w", err)
	}
	if len(hosts) == 0 {
		return nil, errors.New("the pull zone has no hostnames")
	}
	b.PurgeHosts = hosts
	return hosts, nil
}

var indexRE = regexp.MustCompile(`\A(?:(.*)/)?index\.html\z`)

// purgePaths returns the purges needed after the given paths changed: the
// paths to purge exactly, and the prefixes to purge, each beginning with a
// slash.  See Bunny for how they're chosen.
func purgePaths(paths []string, prefixOver int) (exact, prefix []string) {
	in := map[string][]string{}
	for _, rel := range paths {
		top, _, found := strings.Cut(rel, "/")
		if !found {
			top = ""
		}
		in[top] = append(in[top], rel)
	}
	tops := make([]string, 0, len(in))
	for top := range in {
		tops = append(tops, top)
	}
	sort.Strings(tops)

	for _, top := range tops {
		rels := in[top]
		if top != "" && len(rels) > prefixOver {
			prefix = append(prefix, "/"+top+"/")

			// The directory without its slash is a URL of its own,
			// and outside the prefix.
			for _, rel := range rels {
				if rel == top+"/index.html" {
					exact = append(exact, "/"+top)
				}
			}
			continue
		}
		for _, rel := range rels {
			exact = append(exact, "/"+rel)
			if m := indexRE.FindStringSubmatch(rel); m != nil {
				if strings.Contains(rel, "/") {
					exact = append(exact, "/"+m[1]+"/", "/"+m[1])
				} else {
					exact = append(exact, "/")
				}
			}
		}
	}
	return exact, prefix
}

func (b *Bunny) purgeAll(ctx context.Context) error {
	if err := b.purgeRequest(ctx, b.APIURL+"/pullzone/"+url.PathEscape(b.PullZoneID)+"/purgeCache"); err != nil {
		return err
	}
	b.Log("purged the whole pull zone")
	return nil
}

// purgeRequest posts a purge request, waiting and trying again for as long
// as Bunny says it's being asked too often.
func (b *Bunny) purgeRequest(ctx context.Context, api string) error {
	for tries := 1; ; tries++ {
		req, err := http.NewRequestWithContext(ctx, "POST", api, nil)
		if err != nil {
			return err
		}
		res, err := b.send(req, b.APIKey)
		if err != nil {
			return fmt.Errorf("purge: %w", err)
		}
		if ok(res) {
			return nil
		}
		if res.StatusCode == http.StatusTooManyRequests && tries < b.RateLimitTries {
			wait := b.RateLimitWait
			if seconds, err := strconv.ParseFloat(res.Header.Get("Retry-After"), 64); err == nil && seconds >= 0 {
				wait = time.Duration(seconds * float64(time.Second))
			}
			if !sleep(ctx, wait) {
				return ctx.Err()
			}
			continue
		}
		hint := ""
		if res.StatusCode == http.StatusNotFound {
			hint = " (is the URL's hostname one of the pull zone's?)"
		}
		return fmt.Errorf("purge: %s%s", res.Status, hint)
	}
}

// purge purges the CDN's cache of the paths given, and returns how many
// requests that took.
func (b *Bunny) purge(ctx context.Context, changed []string) (int, error) {
	if len(changed) == 0 {
		return 0, nil
	}
	hosts, err := b.purgeHosts(ctx)
	if err != nil {
		return 0, err
	}
	exact, prefix := purgePaths(changed, b.PrefixOver)

	// A URL ending in a slash is purged as a prefix unless exactPath
	// says otherwise.
	isPrefix := map[string]bool{}
	var urls []string
	for _, host := range hosts {
		for _, path := range prefix {
			urls = append(urls, "https://"+host+path)
			isPrefix["https://"+host+path] = true
		}
	}
	for _, host := range hosts {
		for _, path := range exact {
			urls = append(urls, "https://"+host+path)
		}
	}

	failed := b.each(ctx, "purging", urls, func(ctx context.Context, u string) error {
		query := url.Values{"url": {u}, "exactPath": {strconv.FormatBool(!isPrefix[u])}}
		return b.purgeRequest(ctx, b.APIURL+"/purge?"+query.Encode())
	})
	if len(failed) > 0 {
		return len(urls) - len(failed), fmt.Errorf("%d purge(s) failed; purge the pull zone by hand (jiggle sync --purge-all), "+
			"or the CDN may serve stale or removed files:\n  %s", len(failed), strings.Join(failed, "\n  "))
	}
	b.Log(fmt.Sprintf("purged %d prefix(es) and %d URL(s)", len(prefix)*len(hosts), len(exact)*len(hosts)))
	return len(urls), nil
}
