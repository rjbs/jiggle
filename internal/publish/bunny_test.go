package publish

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// A fakeBunny is the storage API, which checks the password and checksum of
// each upload, and the purge API, which records what it was asked to purge.
// Paths in fail always get a 500, and the next limited purge requests get a
// 429.  It refuses, as Bunny does, to purge URLs on hostnames that aren't
// the pull zone's.
type fakeBunny struct {
	*httptest.Server

	mu        sync.Mutex
	stored    map[string]string
	putOrder  []string
	purged    []string
	fail      map[string]bool
	limited   int
	hostnames []string
}

func newFakeBunny(t *testing.T) *fakeBunny {
	f := &fakeBunny{stored: map[string]string{}, fail: map[string]bool{}, hostnames: []string{"photos.example.com"}}
	mux := http.NewServeMux()

	mux.HandleFunc("PUT /zone/{rel...}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		rel := r.PathValue("rel")
		body, _ := io.ReadAll(r.Body)
		sum := sha256.Sum256(body)
		switch {
		case r.Header.Get("AccessKey") != "pw":
			http.Error(w, "no", 401)
		case f.fail[rel]:
			http.Error(w, "broken", 500)
		case r.Header.Get("Checksum") != strings.ToUpper(hex.EncodeToString(sum[:])):
			http.Error(w, "bad checksum", 400)
		default:
			f.stored[rel] = string(body)
			f.putOrder = append(f.putOrder, rel)
			w.WriteHeader(201)
		}
	})

	mux.HandleFunc("DELETE /zone/{rel...}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		rel := r.PathValue("rel")
		if f.fail[rel] {
			http.Error(w, "broken", 500)
			return
		}
		if _, ok := f.stored[rel]; !ok {
			http.Error(w, "nope", 404)
			return
		}
		delete(f.stored, rel)
	})

	// Bunny's rule: a URL ending in a slash is a prefix unless exactPath
	// is true.
	mux.HandleFunc("POST /purge", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("AccessKey") != "key" {
			http.Error(w, "no", 401)
			return
		}
		if f.limited > 0 {
			f.limited--
			w.Header().Set("Retry-After", "0")
			http.Error(w, "slow down", 429)
			return
		}
		url := r.URL.Query().Get("url")
		known := false
		for _, host := range f.hostnames {
			known = known || strings.HasPrefix(url, "https://"+host+"/")
		}
		if !known {
			http.Error(w, "no such pull zone", 404)
			return
		}
		kind := "exact "
		if strings.HasSuffix(url, "/") && r.URL.Query().Get("exactPath") != "true" {
			kind = "prefix "
		}
		f.purged = append(f.purged, kind+url)
		w.WriteHeader(204)
	})

	mux.HandleFunc("GET /pullzone/77", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		type hostname struct{ Value, CertificateKey string }
		var names []hostname
		for _, host := range f.hostnames {
			names = append(names, hostname{host, "secret"})
		}
		json.NewEncoder(w).Encode(map[string]any{"Id": 77, "Hostnames": names})
	})

	mux.HandleFunc("POST /pullzone/77/purgeCache", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.purged = append(f.purged, "ALL")
		w.WriteHeader(204)
	})

	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

// A world is a fake Bunny and a site to sync to it, which lasts through
// several syncs.
type world struct {
	t     *testing.T
	bunny *fakeBunny
	dir   string
}

func newWorld(t *testing.T) *world {
	return &world{t: t, bunny: newFakeBunny(t), dir: t.TempDir()}
}

type files = map[string]string

// build makes the site hold exactly these files, with a build manifest to
// match.
func (w *world) build(site files) {
	w.t.Helper()
	root := filepath.Join(w.dir, "site")
	if err := os.RemoveAll(root); err != nil {
		w.t.Fatal(err)
	}
	manifest := entriesFile{Version: 1, Files: map[string]entry{}}
	for rel, content := range site {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			w.t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			w.t.Fatal(err)
		}
		sum := sha1.Sum([]byte(content))
		manifest.Files[rel] = entry{SHA1: hex.EncodeToString(sum[:])}
	}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(w.dir, "site.manifest.json"), data, 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) syncer() *Bunny {
	return &Bunny{
		SiteDir:  filepath.Join(w.dir, "site"),
		Manifest: filepath.Join(w.dir, "site.manifest.json"),
		Record:   filepath.Join(w.dir, "state", "bunny-uploaded.json"),
		Zone:     "zone", Password: "pw", StorageURL: w.bunny.URL,
		APIKey: "key", PullZoneID: "77", APIURL: w.bunny.URL,
		PrefixOver: 2, RetryDelay: time.Nanosecond,
	}
}

func (w *world) forgetUploads() {
	w.bunny.stored, w.bunny.putOrder = map[string]string{}, nil
	os.Remove(filepath.Join(w.dir, "state", "bunny-uploaded.json"))
}

func exact(paths ...string) []string {
	var out []string
	for _, p := range paths {
		out = append(out, "exact https://photos.example.com"+p)
	}
	return out
}

func prefix(paths ...string) []string {
	var out []string
	for _, p := range paths {
		out = append(out, "prefix https://photos.example.com"+p)
	}
	return out
}

// page is an index.html, and the two directory URLs that serve it.
func page(dir string) []string { return exact(dir+"/index.html", dir+"/", dir) }

func all(lists ...[]string) []string {
	out := []string{}
	for _, l := range lists {
		out = append(out, l...)
	}
	sort.Strings(out)
	return out
}

// A want is what a sync should do, and leave.
type want struct {
	uploaded, deleted []string
	purged            []string
	stored            files

	// fail is paths that fail every attempt: the sync should fail,
	// naming them.  limited is how many purge requests are refused as
	// too many, first.
	fail      []string
	limited   int
	dryRun    bool
	purgeAll  bool
	errorSays string
}

// syncs builds the site from the files given, syncs it, and checks what
// was uploaded, deleted, and purged, and what the zone holds afterward.
func (w *world) syncs(desc string, site files, want want) {
	w.t.Helper()
	w.build(site)
	w.bunny.purged = nil
	w.bunny.fail = map[string]bool{}
	for _, rel := range want.fail {
		w.bunny.fail[rel] = true
	}
	w.bunny.limited = want.limited

	result, err := w.syncer().Sync(context.Background(), SyncOptions{DryRun: want.dryRun, PurgeAll: want.purgeAll})
	if len(want.fail) > 0 {
		for _, rel := range want.fail {
			if err == nil || !strings.Contains(err.Error(), rel) {
				w.t.Errorf("%s: the error doesn't name %s: %v", desc, rel, err)
			}
		}
		if err == nil || !strings.Contains(err.Error(), want.errorSays) {
			w.t.Errorf("%s: the error doesn't say %q: %v", desc, want.errorSays, err)
		}
	} else {
		if err != nil {
			w.t.Fatalf("%s: %v", desc, err)
		}
		if !reflect.DeepEqual(result.Uploaded, want.uploaded) {
			w.t.Errorf("%s: uploaded %q, want %q", desc, result.Uploaded, want.uploaded)
		}
		if !reflect.DeepEqual(all(result.Deleted), all(want.deleted)) {
			w.t.Errorf("%s: deleted %q, want %q", desc, result.Deleted, want.deleted)
		}
	}

	if got := all(w.bunny.purged); !reflect.DeepEqual(got, all(want.purged)) {
		w.t.Errorf("%s: purged\n got: %q\nwant: %q", desc, got, all(want.purged))
	}
	if want.limited > 0 && w.bunny.limited != 0 {
		w.t.Errorf("%s: %d refused purges were not tried again", desc, w.bunny.limited)
	}
	if !reflect.DeepEqual(w.bunny.stored, want.stored) {
		w.t.Errorf("%s: the zone holds\n got: %q\nwant: %q", desc, w.bunny.stored, want.stored)
	}
}

func with(base files, more files) files {
	out := files{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range more {
		out[k] = v
	}
	return out
}

func minus(base files, gone ...string) files {
	out := with(base, nil)
	for _, k := range gone {
		delete(out, k)
	}
	return out
}

// The syncs of TestSync happen one after another to the same zone, each
// starting from what the last left.
func TestSync(t *testing.T) {
	w := newWorld(t)

	v1 := files{
		"index.html":      "home",
		"p/aa/index.html": "photo a",
		"img/aa/500.webp": "a pixels",
		"p/bb/index.html": "photo b",
		"img/bb/500.webp": "b pixels",
	}
	everything := []string{"img/aa/500.webp", "img/bb/500.webp", "index.html", "p/aa/index.html", "p/bb/index.html"}

	w.syncs("a dry run changes nothing", v1, want{uploaded: everything, stored: files{}, dryRun: true})

	w.syncs("the first sync uploads everything, and purges it", v1, want{
		uploaded: everything,
		purged: all(exact("/index.html", "/"), exact("/img/aa/500.webp", "/img/bb/500.webp"),
			page("/p/aa"), page("/p/bb")),
		stored: v1,
	})

	w.syncs("with nothing changed, nothing is done", v1, want{uploaded: nil, stored: v1})

	v2 := with(minus(v1, "p/bb/index.html", "img/bb/500.webp"), files{"index.html": "home, without b"})
	w.syncs("a changed page is uploaded, and a removed photo deleted, then purged", v2, want{
		uploaded: []string{"index.html"},
		deleted:  []string{"img/bb/500.webp", "p/bb/index.html"},
		purged:   all(exact("/index.html", "/", "/img/bb/500.webp"), page("/p/bb")),
		stored:   v2,
	})

	v3 := with(minus(v2, "img/aa/500.webp"), files{"p/aa/index.html": "photo a, retitled", "p/cc/index.html": "photo c"})
	w.syncs("if an upload fails, nothing is deleted, but what was uploaded is purged", v3, want{
		fail:      []string{"p/cc/index.html"},
		errorSays: "so nothing was deleted",
		purged:    page("/p/aa"),
		stored:    with(v2, files{"p/aa/index.html": "photo a, retitled"}),
	})

	w.syncs("the next sync does only what is left", v3, want{
		uploaded: []string{"p/cc/index.html"},
		deleted:  []string{"img/aa/500.webp"},
		purged:   all(page("/p/cc"), exact("/img/aa/500.webp")),
		stored:   v3,
	})

	v4 := with(v3, files{"404.html": "not found"})
	stored4 := with(v4, files{"bunnycdn_errors/404.html": "not found"})
	w.syncs("the 404 page is uploaded where Bunny looks for it, too", v4, want{
		uploaded: []string{"404.html", "bunnycdn_errors/404.html"},
		purged:   exact("/404.html", "/bunnycdn_errors/404.html"),
		stored:   stored4,
	})

	v5 := with(v4, files{
		"index.html":          "home, new style",
		"p/aa/index.html":     "photo a, new style",
		"p/cc/index.html":     "photo c, new style",
		"p/dd/index.html":     "photo d",
		"img/dd/500.webp":     "d pixels",
		"albums/index.html":   "albums",
		"albums/x/index.html": "album x",
		"albums/y/index.html": "album y",
	})
	stored5 := with(v5, files{"bunnycdn_errors/404.html": "not found"})
	w.syncs("a directory with many changes is purged by prefix, even when Bunny says to slow down", v5, want{
		uploaded: []string{
			"img/dd/500.webp",
			"albums/index.html", "albums/x/index.html", "albums/y/index.html", "index.html",
			"p/aa/index.html", "p/cc/index.html", "p/dd/index.html",
		},
		purged:  all(prefix("/albums/", "/p/"), exact("/albums", "/img/dd/500.webp", "/index.html", "/")),
		stored:  stored5,
		limited: 3,
	})

	w.syncs("purging everything, on request", v5, want{purged: []string{"ALL"}, stored: stored5, purgeAll: true})

	w.bunny.hostnames = []string{"photos.example.com", "jiggle.b-cdn.net"}
	v6 := with(v5, files{"feed.xml": "new"})
	stored6 := with(stored5, files{"feed.xml": "new"})
	w.syncs("purges are made on every one of the pull zone's hostnames", v6, want{
		uploaded: []string{"feed.xml"},
		purged:   all(exact("/feed.xml"), []string{"exact https://jiggle.b-cdn.net/feed.xml"}),
		stored:   stored6,
	})
	w.bunny.hostnames = []string{"photos.example.com"}

	w.bunny.stored["from-the-dashboard.txt"] = "hello"
	v7 := with(v6, files{"feed.xml": "newer"})
	w.syncs("a file put in the zone some other way is left alone", v7, want{
		uploaded: []string{"feed.xml"},
		purged:   exact("/feed.xml"),
		stored:   with(stored6, files{"feed.xml": "newer", "from-the-dashboard.txt": "hello"}),
	})

	// A name that isn't ASCII, and one with characters a URL would take
	// to mean something.
	v8 := with(v7, files{"tags/søren/index.html": "søren", "img/odd name #1?.webp": "odd"})
	w.syncs("names that have to be escaped", v8, want{
		uploaded: []string{"img/odd name #1?.webp", "tags/søren/index.html"},
		purged:   all(exact("/img/odd name #1?.webp"), page("/tags/søren")),
		stored: with(stored6, files{
			"feed.xml": "newer", "from-the-dashboard.txt": "hello",
			"tags/søren/index.html": "søren", "img/odd name #1?.webp": "odd",
		}),
	})
	w.syncs("...and are not taken for changed the next time", v8, want{
		stored: with(stored6, files{
			"feed.xml": "newer", "from-the-dashboard.txt": "hello",
			"tags/søren/index.html": "søren", "img/odd name #1?.webp": "odd",
		}),
	})
}

func TestPurgeOnAHostnameNotThePullZones(t *testing.T) {
	w := newWorld(t)
	w.build(files{"feed.xml": "new"})
	var log []string
	s := w.syncer()
	s.PurgeHosts = []string{"elsewhere.example.com"}
	s.Log = func(m string) { log = append(log, m) }

	_, err := s.Sync(context.Background(), SyncOptions{})
	if err == nil || !strings.Contains(err.Error(), "1 purge(s) failed") {
		t.Errorf("the sync: %v", err)
	}
	if said := strings.Join(log, "\n"); !strings.Contains(said, "404") || !strings.Contains(said, "hostname one of the pull zone's") {
		t.Errorf("it didn't say why: %s", said)
	}
}

// Pages go up only after the files they refer to, even though "albums/"
// sorts before "img/" and "static/".
func TestPagesAreUploadedAfterEverythingElse(t *testing.T) {
	w := newWorld(t)
	w.build(files{"albums/x/index.html": "album", "img/x/500.webp": "pixels", "static/site.css": "css", "index.html": "home"})
	if _, err := w.syncer().Sync(context.Background(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	order := w.bunny.putOrder
	if len(order) != 4 {
		t.Fatalf("uploads: %q", order)
	}
	seenPage := false
	for _, rel := range order {
		if isPage(rel) {
			seenPage = true
		} else if seenPage {
			t.Errorf("%s was uploaded after a page: %q", rel, order)
		}
	}
}

func TestIfAFileFailsToUploadNoPagesAre(t *testing.T) {
	w := newWorld(t)
	w.build(files{"static/site.css": "new css", "index.html": "new home"})
	w.bunny.fail["static/site.css"] = true
	_, err := w.syncer().Sync(context.Background(), SyncOptions{})
	if err == nil || !strings.Contains(err.Error(), "so no pages were uploaded and nothing was deleted") {
		t.Errorf("the sync: %v", err)
	}
	if _, ok := w.bunny.stored["index.html"]; ok {
		t.Error("the page was uploaded")
	}
}

// A site with no manifest is not a site with nothing in it.
func TestNoManifest(t *testing.T) {
	w := newWorld(t)
	w.build(files{"index.html": "home"})
	if _, err := w.syncer().Sync(context.Background(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(w.dir, "site.manifest.json"))

	_, err := w.syncer().Sync(context.Background(), SyncOptions{})
	if err == nil || !strings.Contains(err.Error(), "manifest") {
		t.Errorf("a sync with no manifest: %v", err)
	}
	if !reflect.DeepEqual(w.bunny.stored, files{"index.html": "home"}) {
		t.Errorf("the zone holds %q", w.bunny.stored)
	}
}

// The record is one Perl jiggle can read, and reads one Perl jiggle wrote.
func TestRecordIsPerls(t *testing.T) {
	w := newWorld(t)
	w.build(files{"index.html": "home"})
	manifest := `{"files":{` +
		`"index.html":{"sha1":"aaaa"},` +
		`"img/aa/500.webp":{"key":"ffff:1+rot90","link":"../derived/aa/aa/500.webp"},` +
		`"tags/søren/index.html":{"sha1":"bbbb"}},"version":1}`
	os.WriteFile(filepath.Join(w.dir, "site.manifest.json"), []byte(manifest), 0o644)
	os.MkdirAll(filepath.Join(w.dir, "state"), 0o755)
	os.WriteFile(filepath.Join(w.dir, "state", "bunny-uploaded.json"), []byte(manifest), 0o644)

	result, err := w.syncer().Sync(context.Background(), SyncOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Uploaded)+len(result.Deleted) != 0 {
		t.Errorf("a record the same as the manifest: upload %q, delete %q", result.Uploaded, result.Deleted)
	}

	changed := strings.Replace(manifest, "ffff:1+rot90", "ffff:1+rot180", 1)
	os.WriteFile(filepath.Join(w.dir, "site.manifest.json"), []byte(changed), 0o644)
	result, err = w.syncer().Sync(context.Background(), SyncOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(result.Uploaded) != "[img/aa/500.webp]" || len(result.Deleted) != 0 {
		t.Errorf("a link whose key changed: upload %q, delete %q", result.Uploaded, result.Deleted)
	}
}

func purges(t *testing.T, paths, wantExact, wantPrefix []string) {
	t.Helper()
	exact, prefix := purgePaths(paths, 2)
	if !reflect.DeepEqual(exact, wantExact) || !reflect.DeepEqual(prefix, wantPrefix) {
		t.Errorf("purges for %q:\n got: %q and %q\nwant: %q and %q", paths, exact, prefix, wantExact, wantPrefix)
	}
}

func TestPurgePaths(t *testing.T) {
	purges(t, []string{"feed.xml"}, []string{"/feed.xml"}, nil)
	purges(t, []string{"index.html"}, []string{"/index.html", "/"}, nil)
	purges(t, []string{"p/aa/index.html"}, []string{"/p/aa/index.html", "/p/aa/", "/p/aa"}, nil)
	purges(t, []string{"a.txt", "b.txt", "c.txt"}, []string{"/a.txt", "/b.txt", "/c.txt"}, nil)
	purges(t, []string{"p/a.txt", "p/b.txt", "p/c.txt"}, nil, []string{"/p/"})
	purges(t, []string{"p/a.txt", "p/index.html", "p/c.txt", "q/x"}, []string{"/p", "/q/x"}, []string{"/p/"})
}

// A sync that is stopped part way records what it did, purges it, and
// leaves the rest for the next.
func TestStoppedSync(t *testing.T) {
	w := newWorld(t)
	site := files{}
	for i := range 40 {
		site[fmt.Sprintf("img/%02d.webp", i)] = fmt.Sprint("pixels ", i)
	}
	w.build(site)

	ctx, cancel := context.WithCancel(context.Background())
	s := w.syncer()
	s.Concurrency = 1
	s.Log = func(string) {}
	uploads := 0
	s.HTTP = &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		if r.Method == "PUT" {
			if uploads++; uploads == 10 {
				cancel()
			}
		}
		return http.DefaultTransport.RoundTrip(r)
	})}

	_, err := s.Sync(ctx, SyncOptions{})
	if err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Fatalf("the stopped sync: %v", err)
	}
	done := len(w.bunny.stored)
	if done == 0 || done == len(site) {
		t.Fatalf("it uploaded %d of %d", done, len(site))
	}
	if got := all(w.bunny.purged); !reflect.DeepEqual(got, prefix("/img/")) {
		t.Errorf("it purged %q", got)
	}

	result, err := w.syncer().Sync(context.Background(), SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Uploaded) != len(site)-done || !reflect.DeepEqual(w.bunny.stored, site) {
		t.Errorf("the next sync uploaded %d, after %d of %d", len(result.Uploaded), done, len(site))
	}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
