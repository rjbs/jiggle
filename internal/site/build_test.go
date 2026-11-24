package site

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rjbs/jiggle/internal/derive"
	"github.com/rjbs/jiggle/internal/store"
)

// fixture is a library to build a site from: a database, and the files of
// its renditions.
type fixture struct {
	t   *testing.T
	dir string
	s   *store.Store
	log []string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Create(filepath.Join(dir, "jiggle.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &fixture{t: t, dir: dir, s: s}
}

func (f *fixture) exec(query string, args ...any) {
	f.t.Helper()
	if _, err := f.s.DB.Exec(query, args...); err != nil {
		f.t.Fatalf("%s: %v", query, err)
	}
}

func blobID(kind, content string) string {
	return fmt.Sprintf("%s%x", kind, sha256.Sum256([]byte(content)))
}

// photo adds a photo: public, 4000 by 3000, with an original and all its
// renditions, and then with whatever columns set gives it, as the SET of an
// UPDATE.
func (f *fixture) photo(id, set string) {
	f.t.Helper()
	ctx := context.Background()

	original := blobID("O", id)
	f.exec(`INSERT INTO blob (id, bytes, ext, path) VALUES (?, 1, 'jpg', ?)`, original, "originals/"+id+".jpg")

	modseq, err := store.Kit.Create(ctx, f.s.DB, store.TypePhoto, id)
	if err != nil {
		f.t.Fatal(err)
	}
	f.exec(`
		INSERT INTO photo (id, type, visibility, blob_id, width, height, created_modseq, modseq)
		VALUES (?, 'photo', 'public', ?, 4000, 3000, ?, ?)`, id, original, modseq, modseq)
	if set != "" {
		f.exec(`UPDATE photo SET `+set+` WHERE id = ?`, id)
	}

	var mediaType string
	var width, height int64
	if err := f.s.DB.QueryRow(`SELECT type, width, height FROM photo WHERE id = ?`, id).Scan(&mediaType, &width, &height); err != nil {
		f.t.Fatal(err)
	}
	for _, recipe := range derive.RecipesFor(mediaType) {
		content := recipe.Name + " of " + id
		path := filepath.Join("derived", id, recipe.Name)
		full := filepath.Join(f.dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			f.t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			f.t.Fatal(err)
		}

		w, h := recipe.Size(width, height)
		rendition := blobID("R", content)
		f.exec(`INSERT INTO blob (id, bytes, ext, path) VALUES (?, ?, 'x', ?)`, rendition, len(content), path)
		f.exec(`
			INSERT INTO photo_rendition (photo_id, name, blob_id, original, version, rotate, width, height)
			VALUES (?, ?, ?, ?, '1', 0, ?, ?)`, id, recipe.Name, rendition, original, w, h)
	}
}

func (f *fixture) tag(id string, tags ...string) {
	f.t.Helper()
	for _, tag := range tags {
		f.exec(`INSERT INTO photo_tag (photo_id, tag) VALUES (?, ?)`, id, tag)
	}
}

func (f *fixture) album(slug, title string, ids ...string) {
	f.t.Helper()
	modseq, err := store.Kit.Create(context.Background(), f.s.DB, store.TypeAlbum, slug)
	if err != nil {
		f.t.Fatal(err)
	}
	f.exec(`INSERT INTO album (id, slug, title, created_modseq, modseq) VALUES (?, ?, ?, ?, ?)`,
		slug, slug, title, modseq, modseq)
	for i, id := range ids {
		f.exec(`INSERT INTO album_photo (album_id, position, photo_id) VALUES (?, ?, ?)`, slug, i, id)
	}
}

func (f *fixture) out() string { return filepath.Join(f.dir, "site") }

// build builds the site, which must work, and returns the last thing it
// said, which is its count of what it did.
func (f *fixture) build() string {
	f.t.Helper()
	f.log = nil
	err := Build(context.Background(), Options{
		DB:      filepath.Join(f.dir, "jiggle.sqlite"),
		Library: f.dir,
		Out:     f.out(),
		Log:     func(line string) { f.log = append(f.log, line) },
	})
	if err != nil {
		f.t.Fatalf("build: %v", err)
	}
	return f.log[len(f.log)-1]
}

// file returns a file of the site, or fails the test if it isn't there.
func (f *fixture) file(rel string) string {
	f.t.Helper()
	content, err := os.ReadFile(filepath.Join(f.out(), rel))
	if err != nil {
		f.t.Fatalf("the site has no %s", rel)
	}
	return string(content)
}

// has checks that a file of the site has each of some strings in it, and
// lacks that it has none of them.
func (f *fixture) has(rel string, want ...string) {
	f.t.Helper()
	content := f.file(rel)
	for _, s := range want {
		if !strings.Contains(content, s) {
			f.t.Errorf("%s doesn't have %q", rel, s)
		}
	}
}

func (f *fixture) lacks(rel string, unwanted ...string) {
	f.t.Helper()
	content := f.file(rel)
	for _, s := range unwanted {
		if strings.Contains(content, s) {
			f.t.Errorf("%s has %q", rel, s)
		}
	}
}

func (f *fixture) absent(rels ...string) {
	f.t.Helper()
	for _, rel := range rels {
		if _, err := os.Stat(filepath.Join(f.out(), rel)); err == nil {
			f.t.Errorf("the site has %s", rel)
		}
	}
}

// neverMentions checks that no file of the site, by its name or in its
// content, has any of some strings.
func (f *fixture) neverMentions(unwanted ...string) {
	f.t.Helper()
	files := 0
	filepath.WalkDir(f.out(), func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		files++
		content, _ := os.ReadFile(path)
		for _, s := range unwanted {
			if strings.Contains(path, s) || strings.Contains(string(content), s) {
				f.t.Errorf("%s mentions %q", strings.TrimPrefix(path, f.out()), s)
			}
		}
		return nil
	})
	if files == 0 {
		f.t.Errorf("the site has no files, so of course none mentions anything")
	}
}

func TestBuildPublishesAPhoto(t *testing.T) {
	f := newFixture(t)
	f.exec(`INSERT INTO setting (name, value) VALUES ('title', 'Some Photos'), ('base_url', 'https://photos.example.com/')`)
	f.photo("aaaaaaaaaaaa", `title = 'Stephansdom', description = 'A *church*.',
		taken = '2026-07-17T17:23:17+02:00', added = '2026-07-20T09:00:00+02:00'`)
	f.tag("aaaaaaaaaaaa", "vienna", "church")
	f.build()

	f.has("p/aaaaaaaaaaaa/index.html",
		"<title>Stephansdom — Some Photos</title>",
		"<p>A <em>church</em>.</p>",
		`<a href="/2026/07/">17 July 2026, 17:23</a>`,
		`<a href="/tags/vienna/">vienna</a>`,
		`<meta property="og:url" content="https://photos.example.com/p/aaaaaaaaaaaa/">`,
		`srcset="/img/aaaaaaaaaaaa/500.webp 500w, /img/aaaaaaaaaaaa/1024.webp 1024w, /img/aaaaaaaaaaaa/2048.webp 2048w"`,
		"data-pagefind-body")
	f.has("p/aaaaaaaaaaaa/embed.json", `"url":"https://photos.example.com/p/aaaaaaaaaaaa/"`, `"width":4000`)
	f.has("img/aaaaaaaaaaaa/1024.webp", "1024.webp of aaaaaaaaaaaa")
	f.has("index.html", `href="/p/aaaaaaaaaaaa/"`, `alt="Stephansdom"`, "added 20 July 2026")
	f.has("tags/vienna/index.html", `href="/p/aaaaaaaaaaaa/"`)
	f.has("2026/07/index.html", `href="/p/aaaaaaaaaaaa/"`)
	f.has("feed.xml", "<title>Stephansdom</title>", `<category term="church"/>`)
	f.has("static/jiggle.css", "grid")
	f.has("404.html", "Not found")
	f.absent("img/aaaaaaaaaaaa/poster.png", "albums/x/index.html", "pagefind")
}

// neverPublished checks that a photo with some setting is treated as if it
// didn't exist: nothing on the site mentions it, its tag, or its album.
func neverPublished(t *testing.T, desc, set string) {
	t.Helper()
	t.Run(desc, func(t *testing.T) {
		f := newFixture(t)
		f.photo("aaaaaaaaaaaa", `title = 'A public photo', taken = '2026-07-17T17:23:17'`)
		f.photo("5ec4e7000000", `title = 'Quite hidden', taken = '2026-07-17T18:00:00',
			added = '2026-07-20T09:00:00Z', lat = 12.345678, lon = 98.765432, `+set)
		f.tag("5ec4e7000000", "furtive")
		f.album("hideaway", "The Hideaway", "5ec4e7000000")
		f.album("mixed", "Mixed", "5ec4e7000000", "aaaaaaaaaaaa")
		f.exec(`UPDATE album SET cover_photo_id = '5ec4e7000000' WHERE slug = 'mixed'`)
		f.build()

		f.neverMentions("5ec4e7000000", "Quite hidden", "furtive", "hideaway", "Hideaway", "12.34", "98.76")
		f.has("albums/mixed/index.html", "/albums/mixed/aaaaaaaaaaaa/")
		f.has("albums/mixed/photos.json", `"photos":[{"height":1536,"id":"aaaaaaaaaaaa"`)
	})
}

func TestBuildNeverPublishes(t *testing.T) {
	neverPublished(t, "a private photo", `visibility = 'private'`)
	neverPublished(t, "a pending photo", `visibility = 'pending'`)
	neverPublished(t, "a photo still being imported", `import_blob_id = blob_id, blob_id = NULL`)
}

func TestBuildLeavesOutAPhotoMissingARendition(t *testing.T) {
	f := newFixture(t)
	f.photo("aaaaaaaaaaaa", ``)
	f.photo("bbbbbbbbbbbb", `title = 'Half made'`)
	f.exec(`DELETE FROM photo_rendition WHERE photo_id = 'bbbbbbbbbbbb' AND name = 'og.jpg'`)
	f.build()

	f.neverMentions("bbbbbbbbbbbb", "Half made")
	if !strings.Contains(strings.Join(f.log, "\n"), "leaving out 1 photo(s) with missing renditions: bbbbbbbbbbbb") {
		t.Errorf("the build didn't say what it left out: %q", f.log)
	}
}

// With verify, a rendition whose file is gone counts as missing, too.
func TestBuildVerifyChecksRenditionFiles(t *testing.T) {
	f := newFixture(t)
	f.photo("aaaaaaaaaaaa", ``)
	f.photo("bbbbbbbbbbbb", ``)
	f.build()
	f.file("p/bbbbbbbbbbbb/index.html")

	os.Remove(filepath.Join(f.dir, "derived/bbbbbbbbbbbb/og.jpg"))
	err := Build(context.Background(), Options{
		DB: filepath.Join(f.dir, "jiggle.sqlite"), Library: f.dir, Out: f.out(), Verify: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.neverMentions("bbbbbbbbbbbb")
}

func TestBuildUnlisted(t *testing.T) {
	f := newFixture(t)
	f.photo("aaaaaaaaaaaa", `taken = '2026-07-17T10:00:00', added = '2026-07-20T09:00:00Z'`)
	f.photo("bbbbbbbbbbbb", `taken = '2026-07-17T11:00:00', added = '2026-07-20T09:00:00Z', visibility = 'unlisted',
		lat = 12.345678, lon = 98.765432`)
	f.photo("cccccccccccc", `taken = '2026-07-17T12:00:00', added = '2026-07-20T09:00:00Z'`)
	f.photo("dddddddddddd", `visibility = 'unlisted'`)
	f.tag("bbbbbbbbbbbb", "vienna", "secretive")
	f.tag("aaaaaaaaaaaa", "vienna")
	f.album("trip", "Trip", "bbbbbbbbbbbb", "aaaaaaaaaaaa")
	f.album("hidden", "Hidden", "dddddddddddd")
	f.build()

	const noindex = `<meta name="robots" content="noindex">`
	const linksIt = `href="/p/bbbbbbbbbbbb/"`

	// It has a page, which nothing lists.
	f.has("p/bbbbbbbbbbbb/index.html", noindex)
	f.lacks("p/bbbbbbbbbbbb/index.html", "data-pagefind-body", `rel="prev"`, `rel="next"`)
	f.file("img/bbbbbbbbbbbb/1024.webp")
	f.lacks("index.html", linksIt, "/albums/hidden/")

	// The feed shows its album by the cover, which is this photo, but
	// links to the album, not to it.
	f.lacks("feed.xml", "/p/bbbbbbbbbbbb/", "/albums/hidden/")
	f.has("feed.xml", "/img/bbbbbbbbbbbb/1024.webp", "/albums/trip/")
	f.lacks("tags/vienna/index.html", linksIt)
	f.lacks("2026/07/index.html", linksIt)
	f.lacks("map/photos.geojson", "bbbbbbbbbbbb")
	f.absent("tags/secretive/index.html")

	// Its neighbors go past it.
	f.has("p/aaaaaaaaaaaa/index.html", `<a rel="next" href="/p/cccccccccccc/">`)
	f.has("p/cccccccccccc/index.html", `<a rel="prev" href="/p/aaaaaaaaaaaa/">`)
	f.lacks("p/aaaaaaaaaaaa/index.html", noindex)

	// Its album shows it, at a page of its own there, which is a copy.
	f.has("albums/trip/index.html", `href="/albums/trip/bbbbbbbbbbbb/"`)
	f.has("albums/trip/bbbbbbbbbbbb/index.html", noindex,
		`<link rel="canonical" href="/p/bbbbbbbbbbbb/">`,
		`<a rel="next" href="/albums/trip/aaaaaaaaaaaa/">next`, "1 of 2")
	f.lacks("albums/trip/aaaaaaaaaaaa/index.html", noindex, "data-pagefind-body")
	f.has("albums/trip/aaaaaaaaaaaa/index.html", `<a rel="prev" href="/albums/trip/bbbbbbbbbbbb/">`, "2 of 2")
	f.absent("albums/trip/cccccccccccc/index.html")

	// An album of nothing but unlisted photos is itself unlisted.
	f.has("albums/hidden/index.html", noindex)
	f.lacks("albums/index.html", "/albums/hidden/")
	f.has("albums/index.html", "/albums/trip/")
}

func TestBuildPublishesOnlyRoundedLocations(t *testing.T) {
	f := newFixture(t)
	f.exec(`INSERT INTO private_zone (lat, lon, radius) VALUES (40.0, -75.0, 500)`)
	f.photo("aaaaaaaaaaaa", `lat = 48.208412, lon = 16.373129`)
	f.photo("bbbbbbbbbbbb", `lat = 40.001234, lon = -75.001234`)
	f.photo("cccccccccccc", `lat = 51.507351, lon = -0.127758, location_private = 1`)
	f.build()

	f.has("map/photos.geojson", `"coordinates":[16.373,48.208]`, "aaaaaaaaaaaa")
	f.lacks("map/photos.geojson", "bbbbbbbbbbbb", "cccccccccccc")
	f.has("p/aaaaaaaaaaaa/index.html", "jiggleMap.single('photo-map', 48.208, 16.373);", "maplibre-gl.js")
	f.lacks("p/bbbbbbbbbbbb/index.html", "photo-map", "maplibre-gl.js")
	f.neverMentions("48.2084", "16.3731", "40.001", "75.001", "51.507", "0.1277")
}

func TestRebuildingChangesNothingAndPruningRemovesTheStale(t *testing.T) {
	f := newFixture(t)
	f.photo("aaaaaaaaaaaa", `taken = '2026-07-17T10:00:00'`)
	f.photo("bbbbbbbbbbbb", `taken = '2026-07-18T10:00:00', title = 'Soon private'`)
	f.tag("bbbbbbbbbbbb", "fleeting")
	f.build()
	f.file("tags/fleeting/index.html")

	if said := f.build(); !strings.Contains(said, "0 file(s) written") || !strings.Contains(said, "0 linked, 0 pruned") {
		t.Errorf("a second build said: %s", said)
	}

	f.exec(`UPDATE photo SET visibility = 'private' WHERE id = 'bbbbbbbbbbbb'`)
	if said := f.build(); !strings.Contains(said, "1 public photo(s)") || strings.Contains(said, " 0 pruned") {
		t.Errorf("a build after hiding a photo said: %s", said)
	}
	f.neverMentions("bbbbbbbbbbbb", "Soon private", "fleeting")
	f.file("p/aaaaaaaaaaaa/index.html")
}

func TestBuildAVideo(t *testing.T) {
	f := newFixture(t)
	f.exec(`INSERT INTO setting (name, value) VALUES ('base_url', 'https://photos.example.com')`)
	f.photo("aaaaaaaaaaaa", `type = 'video', title = 'Rocky', width = 1920, height = 1080, duration = 102,
		added = '2026-08-01T09:00:00-04:00'`)
	f.album("clips", "Clips", "aaaaaaaaaaaa")
	f.build()

	f.has("p/aaaaaaaaaaaa/index.html",
		`poster="/img/aaaaaaaaaaaa/2048.webp"`, `<source src="/img/aaaaaaaaaaaa/video.mp4" type="video/mp4">`,
		`<meta property="og:type" content="video.other">`,
		`<meta property="og:video" content="https://photos.example.com/img/aaaaaaaaaaaa/video.mp4">`)
	f.has("index.html", `<li class="album"`, "1 photo, added 1 August 2026")
	f.has("albums/clips/index.html", `<li class="video"`, "1:42, added 1 August 2026")
	f.has("albums/clips/photos.json", `"video":{"height":1080,"url":"/img/aaaaaaaaaaaa/video.mp4","width":1920}`)
	f.has("p/aaaaaaaaaaaa/embed.json", `"video":{"height":1080,"url":"https://photos.example.com/img/aaaaaaaaaaaa/video.mp4","width":1920}`)
	f.file("img/aaaaaaaaaaaa/video.mp4")
	f.absent("img/aaaaaaaaaaaa/poster.png")
}

// The feed's discovery link is read by programs that don't decode entities,
// so an apostrophe in the site's title is written as itself there.
func TestFeedDiscoveryLinkNamesTheSitePlainly(t *testing.T) {
	f := newFixture(t)
	f.exec(`INSERT INTO setting (name, value) VALUES ('title', 'rjbs''s "photos" & more')`)
	f.photo("aaaaaaaaaaaa", ``)
	f.build()

	f.has("index.html",
		`href="/feed.xml" title="rjbs's &quot;photos&quot; &amp; more">`,
		`<title>rjbs&#39;s &quot;photos&quot; &amp; more</title>`)
}
