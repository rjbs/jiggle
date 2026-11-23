package migrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rjbs/jiggle/internal/derive"
	"github.com/rjbs/jiggle/internal/store"
)

// testLibrary is a TOML library in a temporary directory.
type testLibrary struct {
	t    *testing.T
	root string

	// manifest is what derived/manifest.json will hold: for each photo
	// and rendition, a digest and a version.
	manifest map[string]map[string]map[string]string
}

func newLibrary(t *testing.T) *testLibrary {
	t.Helper()
	l := &testLibrary{t: t, root: t.TempDir(), manifest: map[string]map[string]map[string]string{}}
	l.file("jiggle.toml", "format = 2\n")
	return l
}

// file writes a file in the library, making its directory.
func (l *testLibrary) file(rel, content string) {
	l.t.Helper()
	path := filepath.Join(l.root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		l.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		l.t.Fatal(err)
	}
}

func digestFor(id string) string { return id + strings.Repeat("0", 64-len(id)) }

// renditionOf is what stands for the bytes of a rendition of a photo.
func renditionOf(id, name string) string { return name + " of " + id }

// blobIDOf is the blobId of a blob of the given kind, O or R, and content.
func blobIDOf(kind, content string) string {
	return fmt.Sprintf("%s%x", kind, sha256.Sum256([]byte(content)))
}

// render makes every rendition of a photo, as a placeholder file and an
// entry in the manifest.  The turn is what Perl jiggle adds to a version
// for a photo with a rotate, like "+rot90".
func (l *testLibrary) render(id, mediaType, turn string) {
	l.t.Helper()
	l.manifest[id] = map[string]map[string]string{"_failed": {}}
	for _, recipe := range derive.RecipesFor(mediaType) {
		l.file(filepath.Join("derived", id[:2], id, recipe.Name), renditionOf(id, recipe.Name))
		l.manifest[id][recipe.Name] = map[string]string{
			"sha256":  digestFor(id),
			"version": recipe.VersionFor(mediaType) + turn,
		}
	}
}

// writeManifest writes derived/manifest.json, unless the test has taken
// the manifest away.
func (l *testLibrary) writeManifest() {
	l.t.Helper()
	if l.manifest == nil {
		return
	}
	data, err := json.Marshal(map[string]any{"version": 1, "photos": l.manifest})
	if err != nil {
		l.t.Fatal(err)
	}
	l.file("derived/manifest.json", string(data))
}

// photo adds a photo with the given id: its metadata file, holding the
// given TOML before a stock [original], a one-byte original, and its
// renditions.
func (l *testLibrary) photo(id, toml string) {
	l.t.Helper()
	l.file(filepath.Join("meta", id[:2], id+".toml"), fmt.Sprintf(`id = %q
%s

[original]
file = "IMG_0001.JPG"
ext = "jpg"
sha256 = %q
bytes = 1
width = 4000
height = 3000
`, id, toml, digestFor(id)))
	l.file(filepath.Join("originals", id[:2], id+".jpg"), "x")
	l.render(id, "photo", "")
}

func (l *testLibrary) album(slug, toml string) {
	l.t.Helper()
	l.file(filepath.Join("meta", "albums", slug+".toml"), toml)
}

func (l *testLibrary) out() string { return filepath.Join(l.root, "jiggle.sqlite") }

// migrated migrates the library, which must work, and returns the database
// and the migration's report.
func (l *testLibrary) migrated() (*sql.DB, *Report) {
	l.t.Helper()
	l.writeManifest()
	report, err := Run(context.Background(), Options{Library: l.root, Out: l.out()})
	if err != nil {
		l.t.Fatalf("migrate: %v", err)
	}
	s, err := store.Open(l.out())
	if err != nil {
		l.t.Fatal(err)
	}
	l.t.Cleanup(func() { s.Close() })
	return s.DB, report
}

// hasProblem checks that the library can't be migrated, that one of the
// problems found says what's wanted, and that no database was left behind.
func (l *testLibrary) hasProblem(want string) {
	l.t.Helper()
	l.writeManifest()
	_, err := Run(context.Background(), Options{Library: l.root, Out: l.out()})

	var problems *ProblemsError
	switch {
	case err == nil:
		l.t.Errorf("wanted a problem like %q, and the migration worked", want)
	case !errors.As(err, &problems):
		if !strings.Contains(err.Error(), want) {
			l.t.Errorf("wanted a problem like %q, and got: %v", want, err)
		}
	case !strings.Contains(strings.Join(problems.Problems, "\n"), want):
		l.t.Errorf("wanted a problem like %q, and got: %v", want, problems.Problems)
	}

	if _, err := os.Stat(l.out()); err == nil {
		l.t.Errorf("a failed migration left a database behind")
	}
}

// rowsAre checks what a query returns, with every value as text and NULL
// as "NULL".  Each row wanted is its values, joined by "|".
func rowsAre(t *testing.T, db *sql.DB, query string, want ...string) {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	defer rows.Close()

	cols, _ := rows.Columns()
	var got []string
	for rows.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		strs := make([]string, len(cols))
		for i, v := range vals {
			strs[i] = "NULL"
			if v.Valid {
				strs[i] = v.String
			}
		}
		got = append(got, strings.Join(strs, "|"))
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s\n got: %q\nwant: %q", query, got, want)
	}
}

func warningsAre(t *testing.T, report *Report, want ...string) {
	t.Helper()
	if !reflect.DeepEqual(report.Warnings, want) {
		t.Errorf("warnings\n got: %q\nwant: %q", report.Warnings, want)
	}
}

func TestMinimalPhoto(t *testing.T) {
	l := newLibrary(t)
	l.photo("3f9a0c21b7e4", ``)
	db, report := l.migrated()

	rowsAre(t, db, `SELECT id, type, title, description, taken, visibility, rotate, added FROM photo`,
		"3f9a0c21b7e4|photo|||NULL|public|0|NULL")
	rowsAre(t, db, `SELECT lat, lon, location_private, flickr_id, flickr_uploaded FROM photo`,
		"NULL|NULL|0|NULL|NULL")
	rowsAre(t, db, `SELECT blob_id = 'O' || id || '0000000000000000000000000000000000000000000000000000' FROM photo`,
		"1")
	rowsAre(t, db, `SELECT original_name, original_mtime, width, height, duration FROM photo`,
		"IMG_0001.JPG|NULL|4000|3000|NULL")
	rowsAre(t, db, `SELECT bytes, ext, path FROM blob WHERE id LIKE 'O%'`,
		"1|jpg|originals/3f/3f9a0c21b7e4.jpg")
	rowsAre(t, db, `SELECT count(*) FROM photo_tag`, "0")
	warningsAre(t, report)

	if report.Photos != 1 || report.Videos != 0 || report.Albums != 0 {
		t.Errorf("report: %+v", report)
	}
}

func TestEverythingPhoto(t *testing.T) {
	l := newLibrary(t)
	l.file("meta/00/000000123456.toml", `
id = "000000123456"
type = "video"
title = "we've got \"legs\""
description = """
Two lines,
and a 🦵."""
taken = 2008-01-06T19:36:11
tags = ["high-st", "café"]
visibility = "unlisted"
rotate = 270
added = 2026-09-30T10:15:00-04:00
flickr_id = "2173311823"
flickr_uploaded = 2008-01-06T21:32:33-05:00

[original]
file = "legs.mov"
ext = "mov"
sha256 = "0000001234560000000000000000000000000000000000000000000000000000"
bytes = 3
width = 1920
height = 1080
duration = 7.165
source_mtime = 2026-07-19T17:49:45-04:00

[location]
lat = 40.623775
lon = -75.373222
private = true
`)
	l.file("originals/00/000000123456.mov", "mov")
	l.render("000000123456", "video", "+rot270")
	db, report := l.migrated()

	rowsAre(t, db, `SELECT id, type, title, description, taken, visibility, rotate, added FROM photo`,
		"000000123456|video|we've got \"legs\"|Two lines,\nand a 🦵.|2008-01-06T19:36:11|unlisted|270|2026-09-30T10:15:00-04:00")
	rowsAre(t, db, `SELECT lat, lon, location_private, flickr_id, flickr_uploaded FROM photo`,
		"40.623775|-75.373222|1|2173311823|2008-01-06T21:32:33-05:00")
	rowsAre(t, db, `SELECT original_name, original_mtime, width, height, duration FROM photo`,
		"legs.mov|2026-07-19T17:49:45-04:00|1920|1080|7.165")
	rowsAre(t, db, `SELECT bytes, ext, path FROM blob WHERE id LIKE 'O%'`,
		"3|mov|originals/00/000000123456.mov")
	rowsAre(t, db, `SELECT tag FROM photo_tag ORDER BY tag`, "café", "high-st")

	if report.Photos != 0 || report.Videos != 1 {
		t.Errorf("report: %+v", report)
	}
}

// takenIs checks that a datetime written one way in a metadata file is
// that same text in the database.
func takenIs(t *testing.T, written, want string) {
	t.Helper()
	l := newLibrary(t)
	l.photo("aaaaaaaaaaaa", "taken = "+written)
	db, _ := l.migrated()
	rowsAre(t, db, `SELECT taken FROM photo`, want)
}

func TestDatetimesKeepTheirForm(t *testing.T) {
	takenIs(t, "2018-12-25T12:20:35", "2018-12-25T12:20:35")
	takenIs(t, "2018-12-25T12:20:35Z", "2018-12-25T12:20:35Z")
	takenIs(t, "2018-12-25T12:20:35+00:00", "2018-12-25T12:20:35+00:00")
	takenIs(t, "2018-12-25T12:20:35-05:00", "2018-12-25T12:20:35-05:00")
	takenIs(t, "2018-07-17T17:23:17+02:00", "2018-07-17T17:23:17+02:00")
	takenIs(t, "2018-12-25T12:20:35+05:45", "2018-12-25T12:20:35+05:45")
}

func TestTagsAreLowercaseAndUnique(t *testing.T) {
	l := newLibrary(t)
	l.photo("aaaaaaaaaaaa", `tags = ["Vienna", "church", "vienna", "ÉTÉ"]`)
	l.photo("bbbbbbbbbbbb", `tags = ["fine", "fine"]`)
	db, report := l.migrated()

	rowsAre(t, db, `SELECT photo_id, tag FROM photo_tag ORDER BY photo_id, tag`,
		"aaaaaaaaaaaa|church", "aaaaaaaaaaaa|vienna", "aaaaaaaaaaaa|été", "bbbbbbbbbbbb|fine")
	warningsAre(t, report, "1 photo(s) had tags with capitals, now lowercase")
}

func TestAlbums(t *testing.T) {
	l := newLibrary(t)
	l.photo("aaaaaaaaaaaa", ``)
	l.photo("bbbbbbbbbbbb", ``)
	l.photo("cccccccccccc", ``)
	l.album("zebras", `
title = "Zebras"
description = "Stripes."
created = 2008-01-06T21:49:26-05:00
cover = "aaaaaaaaaaaa"
flickr_id = "72157603651676472"
photos = ["cccccccccccc", "aaaaaaaaaaaa", "cccccccccccc", "bbbbbbbbbbbb"]
`)
	l.album("aardvarks", `title = "Aardvarks"`)
	db, report := l.migrated()

	rowsAre(t, db, `SELECT slug, title, description, created, cover_photo_id, flickr_id FROM album ORDER BY slug`,
		"aardvarks|Aardvarks||NULL|NULL|NULL",
		"zebras|Zebras|Stripes.|2008-01-06T21:49:26-05:00|aaaaaaaaaaaa|72157603651676472")
	rowsAre(t, db, `
		SELECT slug, position, photo_id FROM album_photo JOIN album ON album.id = album_id
		ORDER BY slug, position`,
		"zebras|0|cccccccccccc", "zebras|1|aaaaaaaaaaaa", "zebras|2|bbbbbbbbbbbb")
	rowsAre(t, db, `SELECT count(*) FROM album WHERE length(id) = 16 AND id <> slug`, "2")
	warningsAre(t, report, "meta/albums/zebras.toml: listed 1 photo(s) twice")

	if report.Albums != 2 {
		t.Errorf("report: %+v", report)
	}
}

func TestConfig(t *testing.T) {
	l := newLibrary(t)
	l.file("jiggle.toml", `
format   = 2
title    = "Photos"
base_url = "https://photos.example.com/"
location_precision = 2

[[private_zone]]
lat    = 40.0
lon    = -75.0
radius = 500

[[private_zone]]
lat    = 48.2
lon    = 16.37
radius = 1200.5

[publish.bunny]
zone = "jiggle"
`)
	l.file("meta/.keep", "")
	db, report := l.migrated()

	rowsAre(t, db, `SELECT name, value FROM setting ORDER BY name`,
		"base_url|https://photos.example.com/", "location_precision|2", "title|Photos")
	rowsAre(t, db, `SELECT lat, lon, radius FROM private_zone ORDER BY lat`,
		"40|-75|500", "48.2|16.37|1200.5")
	warningsAre(t, report, "jiggle.toml: [publish] is not migrated")
}

func TestEveryObjectHasItsOwnModseq(t *testing.T) {
	l := newLibrary(t)
	l.photo("aaaaaaaaaaaa", ``)
	l.photo("bbbbbbbbbbbb", ``)
	l.album("one", `title = "One"`)
	db, _ := l.migrated()

	rowsAre(t, db, `SELECT id, created_modseq, modseq FROM photo ORDER BY id`,
		"aaaaaaaaaaaa|1|1", "bbbbbbbbbbbb|2|2")
	rowsAre(t, db, `SELECT created_modseq, modseq FROM album`, "1|1")
	rowsAre(t, db, `SELECT type, highest_modseq FROM gizmo_state ORDER BY type`, "Album|1", "Photo|2")
}

func TestProblems(t *testing.T) {
	problem := func(desc, want string, spoil func(l *testLibrary)) {
		t.Helper()
		t.Run(desc, func(t *testing.T) {
			t.Helper()
			l := newLibrary(t)
			l.photo("aaaaaaaaaaaa", ``)
			spoil(l)
			l.hasProblem(want)
		})
	}

	problem("a key nothing reads", "meta/bb/bbbbbbbbbbbb.toml: unknown key(s): colour",
		func(l *testLibrary) { l.photo("bbbbbbbbbbbb", `colour = "red"`) })
	problem("a format 1 photo", "unknown key(s): pending",
		func(l *testLibrary) { l.photo("bbbbbbbbbbbb", `pending = true`) })
	problem("a format 1 library", "is format 1",
		func(l *testLibrary) { l.file("jiggle.toml", "format = 1\n") })
	problem("a library with no format", "is format 0",
		func(l *testLibrary) { l.file("jiggle.toml", "title = \"Photos\"\n") })
	problem("a file named for another photo", `meta/bb/bbbbbbbbbbbb.toml: its id is "cccccccccccc"`,
		func(l *testLibrary) {
			l.photo("bbbbbbbbbbbb", ``)
			l.photo("cccccccccccc", ``)
			os.Rename(filepath.Join(l.root, "meta/cc/cccccccccccc.toml"), filepath.Join(l.root, "meta/bb/bbbbbbbbbbbb.toml"))
		})
	problem("bad TOML", "meta/bb/bbbbbbbbbbbb.toml: ",
		func(l *testLibrary) { l.file("meta/bb/bbbbbbbbbbbb.toml", "id = \n") })
	problem("an unknown visibility", `unknown visibility "secret"`,
		func(l *testLibrary) { l.photo("bbbbbbbbbbbb", `visibility = "secret"`) })
	problem("an unknown type", `unknown type "hologram"`,
		func(l *testLibrary) { l.photo("bbbbbbbbbbbb", `type = "hologram"`) })
	problem("a rotation that isn't a quarter turn", "rotate must be 0, 90, 180, or 270, not 45",
		func(l *testLibrary) { l.photo("bbbbbbbbbbbb", `rotate = 45`) })
	problem("a date for a datetime", "meta/bb/bbbbbbbbbbbb.toml: ",
		func(l *testLibrary) { l.photo("bbbbbbbbbbbb", `taken = 2018-12-25`) })
	problem("an impossible date", "meta/bb/bbbbbbbbbbbb.toml: ",
		func(l *testLibrary) { l.photo("bbbbbbbbbbbb", `taken = 2026-02-30T12:00:00`) })
	problem("no [original]", "no [original]",
		func(l *testLibrary) { l.file("meta/bb/bbbbbbbbbbbb.toml", `id = "bbbbbbbbbbbb"`) })
	problem("a missing original", "its original, originals/aa/aaaaaaaaaaaa.jpg, is missing",
		func(l *testLibrary) { os.Remove(filepath.Join(l.root, "originals/aa/aaaaaaaaaaaa.jpg")) })
	problem("an original of the wrong size", "is 5 bytes, not 1",
		func(l *testLibrary) { l.file("originals/aa/aaaaaaaaaaaa.jpg", "xxxxx") })
	problem("two photos with one original", "it has the same original as photo aaaaaaaaaaaa",
		func(l *testLibrary) {
			l.photo("bbbbbbbbbbbb", ``)
			meta := filepath.Join(l.root, "meta/bb/bbbbbbbbbbbb.toml")
			toml, _ := os.ReadFile(meta)
			os.WriteFile(meta, []byte(strings.Replace(string(toml), digestFor("bbbbbbbbbbbb"), digestFor("aaaaaaaaaaaa"), 1)), 0o644)
		})
	problem("an album with no title", "meta/albums/one.toml: no title",
		func(l *testLibrary) { l.album("one", `photos = []`) })
	problem("an album of a photo that doesn't exist", "meta/albums/one.toml: lists photo bbbbbbbbbbbb, which doesn't exist",
		func(l *testLibrary) { l.album("one", `title = "One"`+"\n"+`photos = ["aaaaaaaaaaaa", "bbbbbbbbbbbb"]`) })
	problem("an album whose cover doesn't exist", "its cover is photo bbbbbbbbbbbb, which doesn't exist",
		func(l *testLibrary) { l.album("one", `title = "One"`+"\n"+`cover = "bbbbbbbbbbbb"`) })
}

func TestEveryProblemIsReported(t *testing.T) {
	l := newLibrary(t)
	l.photo("aaaaaaaaaaaa", `visibility = "secret"`)
	l.photo("bbbbbbbbbbbb", `rotate = 45`)
	l.hasProblem("secret")
	l.hasProblem("not 45")
}

func TestOutMustNotExist(t *testing.T) {
	l := newLibrary(t)
	l.photo("aaaaaaaaaaaa", ``)
	l.file("jiggle.sqlite", "precious")

	_, err := Run(context.Background(), Options{Library: l.root, Out: l.out()})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("migrating over a file: got %v", err)
	}
	if got, _ := os.ReadFile(l.out()); string(got) != "precious" {
		t.Errorf("the file in the way is now %q", got)
	}
}

func TestRenditions(t *testing.T) {
	l := newLibrary(t)
	l.photo("aaaaaaaaaaaa", ``)
	db, report := l.migrated()

	rowsAre(t, db, `
		SELECT name, version, photo_rendition.rotate, photo_rendition.width, photo_rendition.height,
		       original = photo.blob_id
		FROM photo_rendition JOIN photo ON photo.id = photo_id
		ORDER BY photo_rendition.width`,
		"500.webp|1|0|500|375|1",
		"h480.webp|1|0|640|480|1",
		"1024.webp|1|0|1024|768|1",
		"og.jpg|1|0|1200|900|1",
		"2048.webp|1|0|2048|1536|1")
	rowsAre(t, db, `
		SELECT blob.id, bytes, ext, path
		FROM photo_rendition JOIN blob ON blob.id = blob_id
		WHERE name IN ('og.jpg', '500.webp') ORDER BY name`,
		blobIDOf("R", "500.webp of aaaaaaaaaaaa")+"|24|webp|derived/aa/aaaaaaaaaaaa/500.webp",
		blobIDOf("R", "og.jpg of aaaaaaaaaaaa")+"|22|jpg|derived/aa/aaaaaaaaaaaa/og.jpg")
	rowsAre(t, db, `SELECT count(*) FROM blob WHERE id LIKE 'R%' AND length(id) = 65`, "5")
	warningsAre(t, report)

	if report.Renditions != 5 {
		t.Errorf("report: %+v", report)
	}
}

// A video has a poster, and its images are made from that.  A photo's
// extra turn swaps the width and height of what's made from it, and is in
// Perl jiggle's versions, but is a column here.
func TestRenditionsOfATurnedVideo(t *testing.T) {
	l := newLibrary(t)
	l.photo("aaaaaaaaaaaa", "type = \"video\"\nrotate = 90")
	l.render("aaaaaaaaaaaa", "video", "+rot90")
	db, report := l.migrated()

	rowsAre(t, db, `
		SELECT name, version, rotate, width, height FROM photo_rendition ORDER BY height, name`,
		"h480.webp|1+poster1|90|360|480",
		"500.webp|1+poster1|90|375|500",
		"1024.webp|1+poster1|90|768|1024",
		"og.jpg|1+poster1|90|900|1200",
		"video.mp4|1|90|1440|1920",
		"2048.webp|1+poster1|90|1536|2048",
		"poster.png|1|90|3000|4000")
	rowsAre(t, db, `SELECT ext, count(*) FROM blob WHERE id LIKE 'R%' GROUP BY ext ORDER BY ext`,
		"jpg|1", "mp4|1", "png|1", "webp|4")
	warningsAre(t, report)
}

// Renditions with the same bytes are one blob, whether they're two of one
// photo's or of two photos.  An original with those bytes is another.
func TestRenditionsWithTheSameBytes(t *testing.T) {
	l := newLibrary(t)
	l.photo("aaaaaaaaaaaa", ``)
	l.photo("bbbbbbbbbbbb", ``)
	l.file("derived/aa/aaaaaaaaaaaa/1024.webp", "x")
	l.file("derived/aa/aaaaaaaaaaaa/2048.webp", "x")
	l.file("derived/bb/bbbbbbbbbbbb/og.jpg", "x")
	db, report := l.migrated()

	rowsAre(t, db, `
		SELECT photo_id, name, blob.path
		FROM photo_rendition JOIN blob ON blob.id = blob_id
		WHERE blob_id = '`+blobIDOf("R", "x")+`' ORDER BY photo_id, name`,
		"aaaaaaaaaaaa|1024.webp|derived/aa/aaaaaaaaaaaa/1024.webp",
		"aaaaaaaaaaaa|2048.webp|derived/aa/aaaaaaaaaaaa/1024.webp",
		"bbbbbbbbbbbb|og.jpg|derived/aa/aaaaaaaaaaaa/1024.webp")
	rowsAre(t, db, `SELECT count(*) FROM blob WHERE id LIKE 'R%'`, "8")
	rowsAre(t, db, `SELECT count(*) FROM photo_rendition`, "10")
	warningsAre(t, report)

	if report.Renditions != 10 {
		t.Errorf("report: %+v", report)
	}
}

// notRendered checks that a photo whose renditions have been spoiled in
// some way has those left out of the database, with a warning.
func notRendered(t *testing.T, desc string, wantRenditions string, wantWarnings []string, spoil func(l *testLibrary)) {
	t.Helper()
	t.Run(desc, func(t *testing.T) {
		t.Helper()
		l := newLibrary(t)
		l.photo("aaaaaaaaaaaa", ``)
		l.photo("bbbbbbbbbbbb", ``)
		spoil(l)
		db, report := l.migrated()

		rowsAre(t, db, `SELECT photo_id, count(*) FROM photo_rendition GROUP BY photo_id ORDER BY photo_id`,
			"aaaaaaaaaaaa|5", wantRenditions)
		rowsAre(t, db, `SELECT count(*) = (SELECT count(*) FROM photo_rendition) FROM blob WHERE id LIKE 'R%'`, "1")
		warningsAre(t, report, wantWarnings...)
	})
}

func TestRenditionsNotRecorded(t *testing.T) {
	incomplete := []string{"1 photo(s) don't have all their renditions"}

	notRendered(t, "made from another original", "bbbbbbbbbbbb|4", incomplete,
		func(l *testLibrary) { l.manifest["bbbbbbbbbbbb"]["og.jpg"]["sha256"] = digestFor("cccccccccccc") })
	notRendered(t, "made by another version of the recipe", "bbbbbbbbbbbb|4", incomplete,
		func(l *testLibrary) { l.manifest["bbbbbbbbbbbb"]["og.jpg"]["version"] = "0" })
	notRendered(t, "made before the photo was turned", "bbbbbbbbbbbb|4", incomplete,
		func(l *testLibrary) { l.manifest["bbbbbbbbbbbb"]["og.jpg"]["version"] = "1+rot90" })
	notRendered(t, "never made", "bbbbbbbbbbbb|4", incomplete,
		func(l *testLibrary) { delete(l.manifest["bbbbbbbbbbbb"], "og.jpg") })
	notRendered(t, "in the manifest, but not on the disk", "bbbbbbbbbbbb|4",
		[]string{"1 rendition(s) in derived/manifest.json have no file", incomplete[0]},
		func(l *testLibrary) { os.Remove(filepath.Join(l.root, "derived/bb/bbbbbbbbbbbb/og.jpg")) })
}

func TestNoManifest(t *testing.T) {
	l := newLibrary(t)
	l.photo("aaaaaaaaaaaa", ``)
	l.manifest = nil
	db, report := l.migrated()

	rowsAre(t, db, `SELECT count(*) FROM photo_rendition`, "0")
	warningsAre(t, report, "there is no derived/manifest.json, so no renditions are recorded")
}

func TestFlickrRecords(t *testing.T) {
	l := newLibrary(t)
	l.photo("aaaaaaaaaaaa", `flickr_id = "2173311823"`)
	l.file("meta/flickr/2173311823.json", "{ \"comments\" :\t[]}\n")
	l.file("meta/flickr/albums.json", "[]")
	l.file("meta/flickr/README", "what these are")
	db, report := l.migrated()

	rowsAre(t, db, `SELECT name, CAST(body AS TEXT) FROM flickr_record ORDER BY name`,
		"2173311823|{ \"comments\" :\t[]}\n", "albums|[]")
	warningsAre(t, report, "meta/flickr/README is not migrated")

	if report.FlickrRecords != 2 {
		t.Errorf("report: %+v", report)
	}
}

func TestALibraryWithNothingInIt(t *testing.T) {
	l := newLibrary(t)
	l.manifest = nil
	db, report := l.migrated()

	rowsAre(t, db, `SELECT (SELECT count(*) FROM photo), (SELECT count(*) FROM album)`, "0|0")
	warningsAre(t, report, "there is no derived/manifest.json, so no renditions are recorded")
}
