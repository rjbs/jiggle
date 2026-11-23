package site

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rjbs/gizmo/sqlitekit"

	"github.com/rjbs/jiggle/internal/derive"
	"github.com/rjbs/jiggle/internal/store"
	"github.com/rjbs/jiggle/share"
)

// Options says what to build, and where.
type Options struct {
	// DB is the database to build from.
	DB string

	// Library is the directory the database's paths are relative to,
	// where the renditions are.
	Library string

	// Out is the directory to build the site in.
	Out string

	// Manifest is the file that records what the last build made; see
	// Writer.  If empty, it is the output directory's name with
	// ".manifest.json" added, beside it.
	Manifest string

	// Verify checks files on disk and doesn't trust the manifest.
	Verify bool

	// Search builds the search index, or nil to build without one.  (The
	// search page is still written; it just won't find anything.)
	Search *Search

	// Log is given a line for each thing worth saying.  It may be nil.
	Log func(string)
}

// Build renders the entire site into the output directory, then removes
// anything left there from earlier builds that this one didn't produce.
//
// The renditions must already exist.  The database is read in a single
// transaction, so the site is of the library at one moment.
func Build(ctx context.Context, opts Options) error {
	b := &builder{opts: opts}
	if opts.Manifest == "" {
		b.opts.Manifest = strings.TrimRight(opts.Out, "/") + ".manifest.json"
	}

	lib, err := b.load(ctx)
	if err != nil {
		return err
	}

	var present func(*Photo) bool
	if opts.Verify {
		present = b.renditionsPresent
	}
	b.site = NewSite(lib, present)
	if n := len(b.site.Unrendered); n > 0 {
		b.logf("leaving out %d photo(s) with missing renditions: %s", n, strings.Join(b.site.Unrendered, " "))
	}

	b.w, err = NewWriter(opts.Out, b.opts.Manifest, opts.Verify)
	if err != nil {
		return err
	}

	steps := []struct {
		name string
		run  func() error
	}{
		{"home page", b.home},
		{"photo pages", b.photoPages},
		{"albums and tags", b.collections},
		{"album photo pages", b.albumPhotoPages},
		{"archive", b.archive},
		{"map, search page, and static files", b.others},
		{"search index", b.searchIndex},
		{"pruning", func() error {
			if err := b.w.Prune(Pruning{}); err != nil {
				return err
			}
			return b.w.SaveManifest()
		}},
	}
	for _, step := range steps {
		start := time.Now()
		if err := step.run(); err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
		b.logf("site: %s in %.1fs", step.name, time.Since(start).Seconds())
	}

	st := b.w.Stats
	b.logf("%d public photo(s); %d file(s) written, %d unchanged, %d linked, %d pruned",
		len(b.site.Published), st.Written, st.Unchanged, st.Linked, st.Pruned)
	return nil
}

type builder struct {
	opts Options
	site *Site
	w    *Writer
}

func (b *builder) logf(format string, args ...any) {
	if b.opts.Log != nil {
		b.opts.Log(fmt.Sprintf(format, args...))
	}
}

func (b *builder) load(ctx context.Context) (*Library, error) {
	db, err := sqlitekit.OpenReadOnly(b.opts.DB)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	tx, err := store.Kit.Read(ctx, db)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	return Load(ctx, tx)
}

// renditionsPresent is whether the files of a photo's published renditions
// are all there.
func (b *builder) renditionsPresent(p *Photo) bool {
	for _, recipe := range derive.RecipesFor(p.Type) {
		if !recipe.Publish {
			continue
		}
		if _, err := os.Stat(filepath.Join(b.opts.Library, p.Renditions[recipe.Name].Path)); err != nil {
			return false
		}
	}
	return true
}

// write puts a rendered page, or the error from rendering it, where it
// goes.
func (b *builder) write(rel string, content []byte, err error) error {
	if err != nil {
		return fmt.Errorf("%s: %w", rel, err)
	}
	return b.w.WriteFile(rel, content)
}

func (b *builder) home() error {
	content, err := b.site.homePage(b.site.HomeItems(100))
	return b.write("index.html", content, err)
}

func (b *builder) photoPages() error {
	s := b.site
	photos := s.Published

	// Each page links to the nearest listed photos, later and earlier, so
	// no listed page links to an unlisted one.  An unlisted photo's own
	// page has no neighbors: it's reached from an album, whose own page
	// for it has the album's.  The photos are newest first.
	later := make([]*Photo, len(photos))
	earlier := make([]*Photo, len(photos))
	var last *Photo
	for i, p := range photos {
		later[i] = last
		if p.IsListed() {
			last = p
		}
	}
	last = nil
	for i := len(photos) - 1; i >= 0; i-- {
		earlier[i] = last
		if photos[i].IsListed() {
			last = photos[i]
		}
	}

	for i, p := range photos {
		var prev, next *link
		if p.IsListed() {
			if earlier[i] != nil {
				prev = &link{PhotoURL(earlier[i]), "earlier"}
			}
			if later[i] != nil {
				next = &link{PhotoURL(later[i]), "later"}
			}
		}

		content, err := s.photoPage(p, prev, next, nil)
		if err := b.write("p/"+p.ID+"/index.html", content, err); err != nil {
			return err
		}

		for _, recipe := range derive.RecipesFor(p.Type) {
			if !recipe.Publish {
				continue
			}
			// A rendition's blobId is a digest of its bytes, so it
			// changes exactly when the link has to be made again.
			r := p.Renditions[recipe.Name]
			rel := strings.TrimPrefix(RenditionURL(p, recipe.Name), "/")
			if err := b.w.LinkFile(rel, filepath.Join(b.opts.Library, r.Path), r.BlobID); err != nil {
				return err
			}
		}

		if err := b.w.WriteFile("p/"+p.ID+"/embed.json", toJSON(s.embedData(p))); err != nil {
			return err
		}
	}
	return nil
}

func (b *builder) collections() error {
	s := b.site

	content, err := s.albumsPage()
	if err := b.write("albums/index.html", content, err); err != nil {
		return err
	}
	for _, a := range s.Albums {
		dir := "albums/" + a.Slug + "/"
		content, err := s.albumPage(a)
		if err := b.write(dir+"index.html", content, err); err != nil {
			return err
		}
		if err := b.w.WriteFile(dir+"photos.json", toJSON(s.albumData(a))); err != nil {
			return err
		}
	}

	content, err = s.tagsPage()
	if err := b.write("tags/index.html", content, err); err != nil {
		return err
	}
	for _, tag := range s.Tags {
		content, err := s.tagPage(tag)
		if err := b.write("tags/"+tag.Slug+"/index.html", content, err); err != nil {
			return err
		}
	}
	return nil
}

// Each photo in an album has a page there, too, at /albums/SLUG/ID/, whose
// neighbors are its neighbors in the album, so a visitor can go through an
// album in its order.  These are copies of the photo's own page, so they
// name it as canonical, and they're left out of the search index, or every
// album's photos would be found two or three times.
func (b *builder) albumPhotoPages() error {
	for _, a := range b.site.Albums {
		for i, p := range a.Photos {
			var prev, next *link
			if i > 0 {
				prev = &link{AlbumPhotoURL(a, a.Photos[i-1]), "previous"}
			}
			if i+1 < len(a.Photos) {
				next = &link{AlbumPhotoURL(a, a.Photos[i+1]), "next"}
			}
			content, err := b.site.photoPage(p, prev, next, &inAlbum{Album: a, Position: i + 1})
			if err := b.write("albums/"+a.Slug+"/"+p.ID+"/index.html", content, err); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *builder) archive() error {
	s := b.site

	content, err := s.archivePage()
	if err := b.write("archive/index.html", content, err); err != nil {
		return err
	}
	if len(s.Undated) > 0 {
		content, err := s.undatedPage()
		if err := b.write("archive/undated/index.html", content, err); err != nil {
			return err
		}
	}

	for i, y := range s.Years {
		content, err := s.yearPage(i)
		if err := b.write(y.Year+"/index.html", content, err); err != nil {
			return err
		}
	}
	months := s.AllMonths()
	for i, m := range months {
		content, err := s.monthPage(months, i)
		if err := b.write(m.Year+"/"+m.Month+"/index.html", content, err); err != nil {
			return err
		}
	}
	return nil
}

func (b *builder) others() error {
	s := b.site

	content, err := s.plainPage("map", "Map", true)
	if err := b.write("map/index.html", content, err); err != nil {
		return err
	}
	if err := b.w.WriteFile("map/photos.geojson", toJSON(s.geoJSON())); err != nil {
		return err
	}
	content, err = s.plainPage("search", "Search", false)
	if err := b.write("search/index.html", content, err); err != nil {
		return err
	}
	content, err = s.plainPage("404", "Not found", false)
	if err := b.write("404.html", content, err); err != nil {
		return err
	}
	content, err = s.FeedXML()
	if err := b.write("feed.xml", content, err); err != nil {
		return err
	}

	return fs.WalkDir(share.Static, "static", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		content, err := share.Static.ReadFile(path)
		return b.write(path, content, err)
	})
}

// Pagefind indexes whatever HTML is in the output, so stale pages (like one
// for a photo just made private) must be pruned before it runs.  The old
// index is kept until the new one is written, so unchanged index files are
// left alone.
func (b *builder) searchIndex() error {
	if err := b.w.Prune(Pruning{Except: "pagefind/"}); err != nil {
		return err
	}

	// The index is made from the pages, so if none was written or
	// removed, the old index is still right, and Pagefind (which reads
	// every page) needn't run.  Keep finds nothing to keep without a
	// manifest.
	if b.w.HTMLChanges == 0 {
		if kept := b.w.Keep("pagefind/"); kept > 0 {
			b.logf("search index: no page changed; kept %d file(s)", kept)
			return nil
		}
	}

	if b.opts.Search != nil {
		n, err := b.opts.Search.IndexSite(b.opts.Out, b.w)
		if err != nil {
			return err
		}
		b.logf("search index: %d file(s)", n)
		return nil
	}

	// Without search, a stale index can't be kept: it might still hold
	// the text of a photo made private since.  It's pruned, which leaves
	// the site with no search until it's built again with search.
	b.logf("search index: pages changed, and search is off, so the old index " +
		"is removed; build with search before syncing")
	return nil
}
