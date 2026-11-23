package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rjbs/gizmo/jmap"
	"github.com/rjbs/gizmo/jmaptest"

	"github.com/rjbs/jiggle/internal/store"
)

// fixture is a server over a library made for the test, and a client of it.
type fixture struct {
	*jmaptest.Tester
	t   *testing.T
	dir string
	s   *store.Store
	srv *Server
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Create(filepath.Join(dir, "jiggle.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	srv, err := New(Config{
		Store: s, Library: dir,
		Now: func() time.Time { return time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC) },
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	web := httptest.NewServer(srv.Handler())
	t.Cleanup(web.Close)

	tester := jmaptest.New(t, web.URL, jmap.CoreCapability, Capability)
	tester.AccountID = "photos"
	return &fixture{Tester: tester, t: t, dir: dir, s: s, srv: srv}
}

func (f *fixture) exec(query string, args ...any) {
	f.t.Helper()
	if _, err := f.s.DB.Exec(query, args...); err != nil {
		f.t.Fatalf("%s: %v", query, err)
	}
}

// blob adds a blob with the given content, as a file in the library, and
// returns its id.
func (f *fixture) blob(kind, path, content string) string {
	f.t.Helper()
	full := filepath.Join(f.dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	id := fmt.Sprintf("%s%x", kind, sha256.Sum256([]byte(content)))
	f.exec(`INSERT INTO blob (id, bytes, ext, path) VALUES (?, ?, ?, ?)`,
		id, len(content), strings.TrimPrefix(filepath.Ext(path), "."), path)
	return id
}

// photo adds a photo: public, 4000 by 3000, with an original and one
// rendition, and then with whatever columns set gives it, as the SET of an
// UPDATE.
func (f *fixture) photo(id, set string, tags ...string) {
	f.t.Helper()
	original := f.blob("O", "originals/"+id+".jpg", "original of "+id)
	modseq, err := store.Kit.Create(context.Background(), f.s.DB, store.TypePhoto, id)
	if err != nil {
		f.t.Fatal(err)
	}
	f.exec(`
		INSERT INTO photo (id, type, visibility, blob_id, original_name, width, height, created_modseq, modseq)
		VALUES (?, 'photo', 'public', ?, ?, 4000, 3000, ?, ?)`, id, original, "IMG_"+id+".jpg", modseq, modseq)
	if set != "" {
		f.exec(`UPDATE photo SET `+set+` WHERE id = ?`, id)
	}
	for _, tag := range tags {
		f.exec(`INSERT INTO photo_tag (photo_id, tag) VALUES (?, ?)`, id, tag)
	}
	rendition := f.blob("R", "derived/"+id+"/500.webp", "500.webp of "+id)
	f.exec(`
		INSERT INTO photo_rendition (photo_id, name, blob_id, original, version, rotate, width, height)
		VALUES (?, '500.webp', ?, ?, '1', 0, 500, 375)`, id, rendition, original)
}

// album adds an album whose id is its slug, with the photos in order and
// the first of them its cover.
func (f *fixture) album(slug, title string, ids ...string) {
	f.t.Helper()
	modseq, err := store.Kit.Create(context.Background(), f.s.DB, store.TypeAlbum, slug)
	if err != nil {
		f.t.Fatal(err)
	}
	var cover any
	if len(ids) > 0 {
		cover = ids[0]
	}
	f.exec(`INSERT INTO album (id, slug, title, cover_photo_id, created_modseq, modseq) VALUES (?, ?, ?, ?, ?, ?)`,
		slug, slug, title, cover, modseq, modseq)
	for i, id := range ids {
		f.exec(`INSERT INTO album_photo (album_id, position, photo_id) VALUES (?, ?, ?)`, slug, i, id)
	}
}

// get returns one object of a type, with the given properties, or all of
// them if none are given.  It fails the test if there is no such object.
func (f *fixture) get(typ, id string, props ...string) map[string]any {
	f.t.Helper()
	args := jmaptest.Args{"ids": []string{id}}
	if len(props) > 0 {
		args["properties"] = props
	}
	var got struct {
		List []map[string]any `json:"list"`
	}
	f.Request(jmaptest.Do(typ+"/get", args)).SingleSentence(typ + "/get").Decode(&got)
	if len(got.List) != 1 {
		f.t.Fatalf("%s %s: got %d objects, want one", typ, id, len(got.List))
	}
	return got.List[0]
}

// state returns the state of a type.
func (f *fixture) state(typ string) string {
	f.t.Helper()
	return f.Request(jmaptest.Do(typ+"/get", jmaptest.Args{"ids": []string{}})).SingleSentence(typ + "/get").Str("state")
}

// set makes one /set call and returns its answer.
func (f *fixture) set(typ string, args jmaptest.Args) *jmaptest.Set {
	f.t.Helper()
	return f.Request(jmaptest.Do(typ+"/set", args)).SingleSentence(typ + "/set").AsSet()
}

// is fails the test unless got and want are the same JSON, where want is
// given as JSON.
func is(t *testing.T, desc string, got any, want string) {
	t.Helper()
	var g, w any
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(raw, &g)
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("%s: the wanted JSON is bad: %v", desc, err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s:\n got %s\nwant %s", desc, raw, want)
	}
}

func TestSession(t *testing.T) {
	f := newFixture(t)
	session := f.Session()
	is(t, "the accounts", session["accounts"], `{"photos": {
		"name": "jiggle", "isPersonal": true, "isReadOnly": false,
		"accountCapabilities": {"https://semiotic.systems/jmap/jiggle": {}}
	}}`)
}

// ---- Photo ------------------------------------------------------------------

func TestPhotoGet(t *testing.T) {
	f := newFixture(t)
	f.photo("aaaaaaaaaaaa", `title = 'Legs', description = 'Two of them.', taken = '2026-07-17T17:23:17+02:00',
		added = '2026-07-20T09:00:00+02:00', lat = 48.25, lon = 16.5, location_private = 1, rotate = 90,
		flickr_id = '123', flickr_uploaded = '2010-01-02T03:04:05Z', original_mtime = '2026-07-17T15:23:20Z'`,
		"vienna", "legs")
	f.photo("bbbbbbbbbbbb", `type = 'video', visibility = 'pending', duration = 102.5`)

	original := fmt.Sprintf("O%x", sha256.Sum256([]byte("original of aaaaaaaaaaaa")))
	rendition := fmt.Sprintf("R%x", sha256.Sum256([]byte("500.webp of aaaaaaaaaaaa")))
	is(t, "a photo with everything", f.get("Photo", "aaaaaaaaaaaa"), `{
		"id": "aaaaaaaaaaaa", "type": "photo", "title": "Legs", "description": "Two of them.",
		"taken": "2026-07-17T17:23:17+02:00", "visibility": "public", "rotate": 90,
		"tags": {"vienna": true, "legs": true}, "location": {"lat": 48.25, "lon": 16.5, "isPrivate": true},
		"added": "2026-07-20T09:00:00+02:00", "flickrId": "123", "flickrUploaded": "2010-01-02T03:04:05Z",
		"blobId": "`+original+`",
		"original": {"name": "IMG_aaaaaaaaaaaa.jpg", "modified": "2026-07-17T15:23:20Z", "size": 24,
		             "width": 4000, "height": 3000, "duration": null},
		"renditions": {"500.webp": {"blobId": "`+rendition+`", "width": 500, "height": 375, "size": 24}},
		"importJob": null
	}`)

	is(t, "a video with next to nothing, and only some of it",
		f.get("Photo", "bbbbbbbbbbbb", "type", "title", "taken", "visibility", "tags", "location", "added", "original"), `{
		"id": "bbbbbbbbbbbb", "type": "video", "title": "", "taken": null, "visibility": "pending",
		"tags": {}, "location": null, "added": null,
		"original": {"name": "IMG_bbbbbbbbbbbb.jpg", "modified": null, "size": 24,
		             "width": 4000, "height": 3000, "duration": 102.5}
	}`)

	f.Request(jmaptest.Do("Photo/get", jmaptest.Args{"ids": nil, "properties": []string{"nope"}})).
		SingleSentence("error").AssertError("invalidArguments")
}

// Ids that the RFC advises against are in published URLs, and must be
// served as the strings they are.
func TestAwkwardPhotoIDs(t *testing.T) {
	f := newFixture(t)
	ids := []string{"000000123456", "123456789012", "123e45678901"}
	for _, id := range ids {
		f.photo(id, "")
	}
	f.album("digits", "Digits", ids...)

	got := f.Request(jmaptest.Do("Photo/get", jmaptest.Args{"ids": ids, "properties": []string{"id"}})).
		SingleSentence("Photo/get")
	is(t, "none is missing", got.Strs("notFound"), `[]`)
	is(t, "a query finds them", f.query("Photo", `{"inAlbum": "digits"}`),
		`["000000123456", "123456789012", "123e45678901"]`)
	is(t, "an album lists them", f.get("Album", "digits", "photoIds")["photoIds"],
		`["000000123456", "123456789012", "123e45678901"]`)
}

// photoBecomes updates a photo that has a title, a date, two tags and a
// location, and requires that the properties named in want then be as it
// gives them.
func photoBecomes(t *testing.T, desc, patch, want string) {
	t.Helper()
	t.Run(desc, func(t *testing.T) {
		f := newFixture(t)
		f.photo("aaaaaaaaaaaa", `title = 'Legs', taken = '2026-07-17T17:23:17', lat = 1.5, lon = 2.5`, "vienna", "legs")
		before := f.state("Photo")

		var update, wanted map[string]any
		if err := json.Unmarshal([]byte(patch), &update); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(want), &wanted); err != nil {
			t.Fatal(err)
		}
		set := f.set("Photo", jmaptest.Args{"update": map[string]any{"aaaaaaaaaaaa": update}}).AssertNoErrors()
		if set.NewState() == before {
			t.Errorf("the state didn't change")
		}

		var names []string
		for name := range wanted {
			names = append(names, name)
		}
		got := f.get("Photo", "aaaaaaaaaaaa", names...)
		delete(got, "id")
		is(t, "the photo", got, want)
	})
}

func TestPhotoUpdate(t *testing.T) {
	photoBecomes(t, "text", `{"title": "Arms", "description": "Not legs."}`, `{"title": "Arms", "description": "Not legs."}`)
	photoBecomes(t, "no title", `{"title": ""}`, `{"title": ""}`)
	photoBecomes(t, "a date with an offset", `{"taken": "2026-01-02T03:04:05-05:00"}`, `{"taken": "2026-01-02T03:04:05-05:00"}`)
	photoBecomes(t, "a date in UTC, to a fraction", `{"taken": "2026-01-02T03:04:05.25Z"}`, `{"taken": "2026-01-02T03:04:05.25Z"}`)
	photoBecomes(t, "no date", `{"taken": null}`, `{"taken": null}`)
	photoBecomes(t, "visibility and rotation", `{"visibility": "unlisted", "rotate": 270}`, `{"visibility": "unlisted", "rotate": 270}`)
	photoBecomes(t, "tags, which are kept in lowercase",
		`{"tags": {"Legs": true, "rock & roll": true, "LEGS": true}}`, `{"tags": {"legs": true, "rock & roll": true}}`)
	photoBecomes(t, "no tags", `{"tags": {}}`, `{"tags": {}}`)
	photoBecomes(t, "one more tag", `{"tags/pet": true}`, `{"tags": {"vienna": true, "legs": true, "pet": true}}`)
	photoBecomes(t, "one tag fewer", `{"tags/vienna": null}`, `{"tags": {"legs": true}}`)
	photoBecomes(t, "one tag for another", `{"tags/vienna": null, "tags/Wien": true}`, `{"tags": {"legs": true, "wien": true}}`)
	photoBecomes(t, "a location", `{"location": {"lat": -33.5, "lon": 151, "isPrivate": true}}`,
		`{"location": {"lat": -33.5, "lon": 151, "isPrivate": true}}`)
	photoBecomes(t, "a location, public unless it says otherwise", `{"location": {"lat": 0, "lon": 0}}`,
		`{"location": {"lat": 0, "lon": 0, "isPrivate": false}}`)
	photoBecomes(t, "part of a location", `{"location/isPrivate": true}`,
		`{"location": {"lat": 1.5, "lon": 2.5, "isPrivate": true}}`)
	photoBecomes(t, "no location", `{"location": null}`, `{"location": null}`)
	photoBecomes(t, "what the server sets, given as it is", `{"type": "photo", "title": "Arms"}`, `{"title": "Arms"}`)
}

// photoRefuses makes an update to a photo that must fail, as
// invalidProperties naming the given properties, and change nothing.
func photoRefuses(t *testing.T, patch string, properties ...string) {
	t.Helper()
	t.Run(patch, func(t *testing.T) {
		f := newFixture(t)
		f.photo("aaaaaaaaaaaa", `lat = 1.5, lon = 2.5`, "vienna")
		before := f.state("Photo")

		var update map[string]any
		if err := json.Unmarshal([]byte(patch), &update); err != nil {
			t.Fatal(err)
		}
		set := f.set("Photo", jmaptest.Args{"update": map[string]any{"aaaaaaaaaaaa": update}})
		serr := set.NotUpdated("aaaaaaaaaaaa")
		if serr["type"] != "invalidProperties" {
			t.Errorf("the error is %v, not invalidProperties", serr["type"])
		}
		want, _ := json.Marshal(properties)
		is(t, "the properties at fault", serr["properties"], string(want))
		if set.NewState() != before {
			t.Errorf("the state changed")
		}
	})
}

func TestPhotoUpdateRefused(t *testing.T) {
	photoRefuses(t, `{"title": null}`, "title")
	photoRefuses(t, `{"description": 12}`, "description")
	photoRefuses(t, `{"taken": "yesterday"}`, "taken")
	photoRefuses(t, `{"taken": "2026-02-30T12:00:00"}`, "taken")
	photoRefuses(t, `{"taken": "2026-02-03"}`, "taken")
	photoRefuses(t, `{"visibility": "secret"}`, "visibility")
	photoRefuses(t, `{"rotate": 45}`, "rotate")
	photoRefuses(t, `{"rotate": "90"}`, "rotate")
	photoRefuses(t, `{"tags": "vienna"}`, "tags")
	photoRefuses(t, `{"tags": ["vienna"]}`, "tags")
	photoRefuses(t, `{"tags": {"vienna": true, "": true}}`, "tags")
	photoRefuses(t, `{"tags": {" vienna": true}}`, "tags")
	photoRefuses(t, `{"tags/vienna": false}`, "tags")
	photoRefuses(t, `{"tags/legs": 1}`, "tags")
	photoRefuses(t, `{"location": {"lat": 91, "lon": 0}}`, "location")
	photoRefuses(t, `{"location": {"lat": 1}}`, "location")
	photoRefuses(t, `{"location": {"lat": 1, "lon": 2, "name": "home"}}`, "location")
	photoRefuses(t, `{"location/isPrivate": "yes"}`, "location")
	photoRefuses(t, `{"type": "video"}`, "type")
	photoRefuses(t, `{"blobId": null, "original": null, "renditions": {}}`, "blobId", "original", "renditions")
	photoRefuses(t, `{"added": "2026-01-01T00:00:00Z", "flickrId": "1", "sparkle": true, "title": 1}`,
		"added", "flickrId", "sparkle", "title")
}

func TestPhotoSetTellsWhatItChanged(t *testing.T) {
	f := newFixture(t)
	f.photo("aaaaaaaaaaaa", "")
	set := f.set("Photo", jmaptest.Args{"update": map[string]any{
		"aaaaaaaaaaaa": map[string]any{"title": "Legs", "tags": map[string]bool{"Vienna": true}},
	}}).AssertNoErrors()
	var got struct {
		Updated map[string]any `json:"updated"`
	}
	set.Decode(&got)
	is(t, "what the client is told", got.Updated, `{"aaaaaaaaaaaa": {"tags": {"vienna": true}}}`)
}

func TestPhotoUpdateThatChangesNothing(t *testing.T) {
	f := newFixture(t)
	f.photo("aaaaaaaaaaaa", `title = 'Legs'`, "vienna")
	before := f.state("Photo")
	set := f.set("Photo", jmaptest.Args{"update": map[string]any{
		"aaaaaaaaaaaa": map[string]any{"title": "Legs", "tags": map[string]bool{"vienna": true}},
	}}).AssertNoErrors()
	if set.NewState() != before {
		t.Errorf("the state changed, from %s to %s", before, set.NewState())
	}
}

func TestPhotoSetDoesNotCreate(t *testing.T) {
	f := newFixture(t)
	set := f.set("Photo", jmaptest.Args{"create": map[string]any{"new": map[string]any{"title": "Legs"}}})
	if got := set.NotCreated("new")["type"]; got != "forbidden" {
		t.Errorf("creating a photo: %v, want forbidden", got)
	}
	if got := f.set("Photo", jmaptest.Args{"update": map[string]any{"nobody": map[string]any{"title": "x"}}}).
		NotUpdated("nobody")["type"]; got != "notFound" {
		t.Errorf("updating no photo: %v, want notFound", got)
	}
}

func TestPhotoDestroy(t *testing.T) {
	f := newFixture(t)
	for _, id := range []string{"aaaaaaaaaaaa", "bbbbbbbbbbbb", "cccccccccccc"} {
		f.photo(id, "", "vienna")
	}
	f.album("both", "Both", "aaaaaaaaaaaa", "bbbbbbbbbbbb")
	f.album("only", "Only", "aaaaaaaaaaaa")
	f.album("neither", "Neither", "cccccccccccc")
	photos, albums := f.state("Photo"), f.state("Album")
	original := f.get("Photo", "aaaaaaaaaaaa", "blobId")["blobId"].(string)

	set := f.set("Photo", jmaptest.Args{"destroy": []string{"aaaaaaaaaaaa", "nobody"}})
	is(t, "destroyed", set.DestroyedIDs(), `["aaaaaaaaaaaa"]`)
	if got := set.NotDestroyed("nobody")["type"]; got != "notFound" {
		t.Errorf("destroying no photo: %v, want notFound", got)
	}

	is(t, "the photos left", f.query("Photo", `{}`), `["bbbbbbbbbbbb", "cccccccccccc"]`)
	is(t, "an album that had it and another", f.get("Album", "both", "photoIds", "coverPhotoId"),
		`{"id": "both", "photoIds": ["bbbbbbbbbbbb"], "coverPhotoId": "bbbbbbbbbbbb"}`)
	is(t, "an album that had only it", f.get("Album", "only", "photoIds", "coverPhotoId"),
		`{"id": "only", "photoIds": [], "coverPhotoId": null}`)

	changed := func(typ, since string) map[string]any {
		var got map[string]any
		f.Request(jmaptest.Do(typ+"/changes", jmaptest.Args{"sinceState": since})).SingleSentence(typ + "/changes").Decode(&got)
		return map[string]any{"updated": got["updated"], "destroyed": got["destroyed"]}
	}
	is(t, "Photo/changes", changed("Photo", photos), `{"updated": [], "destroyed": ["aaaaaaaaaaaa"]}`)
	got := changed("Album", albums)
	is(t, "Album/changes, destroyed", got["destroyed"], `[]`)
	if updated := fmt.Sprint(got["updated"]); !strings.Contains(updated, "both") ||
		!strings.Contains(updated, "only") || strings.Contains(updated, "neither") {
		t.Errorf("Album/changes: updated %s, want both and only", updated)
	}

	if dl := f.Download(original, "legs.jpg", ""); dl.Status != 200 {
		t.Errorf("the original of a destroyed photo: status %d, want 200", dl.Status)
	}
}

// ---- Photo/query --------------------------------------------------------------

// query returns the ids that a /query with the given filter finds.
func (f *fixture) query(typ, filter string, sort ...map[string]any) []string {
	f.t.Helper()
	args := jmaptest.Args{"filter": json.RawMessage(filter)}
	if len(sort) > 0 {
		args["sort"] = sort
	}
	return f.Request(jmaptest.Do(typ+"/query", args)).SingleSentence(typ + "/query").Strs("ids")
}

// queryFixture is a library with photos to tell apart, named for when they
// were taken:
//
//	p2001  public    2001-05-01  tags: high st, vienna    in album trip (2nd), best
//	p2002  unlisted  2002-01-01  tags: vienna             in album trip (1st)
//	p2003  private   2003-01-01  tags: High-St (as migrated data might have)
//	p2004  pending   2004-01-01                           in album best
//	pnone  public    no date
func queryFixture(t *testing.T) *fixture {
	f := newFixture(t)
	f.photo("p2001", `taken = '2001-05-01T10:00:00', added = '2026-01-05T00:00:00Z'`, "high st", "vienna")
	f.photo("p2002", `taken = '2002-01-01T10:00:00+02:00', added = '2026-01-04T00:00:00Z', visibility = 'unlisted'`, "vienna")
	f.photo("p2003", `taken = '2003-01-01T10:00:00', added = '2026-01-03T00:00:00Z', visibility = 'private'`, "High-St")
	f.photo("p2004", `taken = '2004-01-01T10:00:00', visibility = 'pending'`)
	f.photo("pnone", `added = '2026-01-01T00:00:00Z'`)
	f.album("trip", "Trip", "p2002", "p2001")
	f.album("best", "Best", "p2001", "p2004")
	return f
}

// finds requires that a Photo/query with the filter find the photos, which
// it gives in the order they were taken.
func finds(t *testing.T, f *fixture, filter string, want ...string) {
	t.Helper()
	if want == nil {
		want = []string{}
	}
	wanted, _ := json.Marshal(want)
	is(t, filter, f.query("Photo", filter), string(wanted))
}

func TestPhotoQuery(t *testing.T) {
	f := queryFixture(t)
	finds(t, f, `null`, "p2001", "p2002", "p2003", "p2004", "pnone")
	finds(t, f, `{}`, "p2001", "p2002", "p2003", "p2004", "pnone")
	finds(t, f, `{"visibility": "public"}`, "p2001", "pnone")
	finds(t, f, `{"visibility": "unlisted"}`, "p2002")
	finds(t, f, `{"visibility": "private"}`, "p2003")
	finds(t, f, `{"visibility": "pending"}`, "p2004")
	finds(t, f, `{"visibility": "secret"}`)
	finds(t, f, `{"inAlbum": "trip"}`, "p2001", "p2002")
	finds(t, f, `{"inAlbum": "nowhere"}`)
	finds(t, f, `{"inAlbum": "trip", "visibility": "public"}`, "p2001")
	finds(t, f, `{"tag": "vienna"}`, "p2001", "p2002")
	finds(t, f, `{"tag": "high-st"}`, "p2001", "p2003")
	finds(t, f, `{"tag": "HIGH_ST"}`, "p2001", "p2003")
	finds(t, f, `{"tag": "nothing"}`)
	finds(t, f, `{"takenAfter": "2002", "takenBefore": "2003"}`, "p2002")
	finds(t, f, `{"takenAfter": "2002-01-01T10:00:00+02:00"}`, "p2002", "p2003", "p2004")
	finds(t, f, `{"takenBefore": "2002-01-01T10:00:00+02:00"}`, "p2001")
	finds(t, f, `{"takenBefore": "1999"}`)
}

func TestPhotoQueryOperators(t *testing.T) {
	f := queryFixture(t)
	finds(t, f, `{"operator": "AND", "conditions": [{"inAlbum": "trip"}, {"inAlbum": "best"}]}`, "p2001")
	finds(t, f, `{"operator": "OR", "conditions": [{"inAlbum": "trip"}, {"inAlbum": "best"}]}`, "p2001", "p2002", "p2004")
	finds(t, f, `{"operator": "NOT", "conditions": [{"inAlbum": "trip"}, {"inAlbum": "best"}]}`, "p2003", "pnone")
	finds(t, f, `{"operator": "AND", "conditions": [
		{"operator": "OR", "conditions": [{"visibility": "public"}, {"visibility": "unlisted"}]},
		{"operator": "NOT", "conditions": [{"tag": "high st"}]}
	]}`, "p2002", "pnone")
	finds(t, f, `{"operator": "AND", "conditions": []}`, "p2001", "p2002", "p2003", "p2004", "pnone")
	finds(t, f, `{"operator": "OR", "conditions": []}`)
	finds(t, f, `{"operator": "NOT", "conditions": []}`, "p2001", "p2002", "p2003", "p2004", "pnone")
}

func queryFails(t *testing.T, f *fixture, args, errorType string) {
	t.Helper()
	var decoded jmaptest.Args
	if err := json.Unmarshal([]byte(args), &decoded); err != nil {
		t.Fatal(err)
	}
	f.Request(jmaptest.Do("Photo/query", decoded)).SingleSentence("error").AssertError(errorType)
}

func TestPhotoQueryRefused(t *testing.T) {
	f := queryFixture(t)
	queryFails(t, f, `{"filter": {"title": "Legs"}}`, "unsupportedFilter")
	queryFails(t, f, `{"filter": {"query": "public"}}`, "unsupportedFilter")
	queryFails(t, f, `{"filter": {"visibility": 1}}`, "unsupportedFilter")
	queryFails(t, f, `{"filter": {"operator": "XOR", "conditions": []}}`, "unsupportedFilter")
	queryFails(t, f, `{"filter": {"operator": "AND", "conditions": [{"sparkle": true}]}}`, "unsupportedFilter")
	queryFails(t, f, `{"filter": {"operator": "AND", "visibility": "public"}}`, "invalidArguments")
	queryFails(t, f, `{"filter": "public"}`, "invalidArguments")
	queryFails(t, f, `{"sort": [{"property": "title"}]}`, "unsupportedSort")
	queryFails(t, f, `{"sort": [{"property": "position"}]}`, "invalidArguments")
	queryFails(t, f, `{"sort": [{"property": "taken", "inAlbum": "trip"}]}`, "invalidArguments")
}

func TestPhotoQuerySort(t *testing.T) {
	f := queryFixture(t)
	descending := map[string]any{"property": "taken", "isAscending": false}
	is(t, "newest first, undated last", f.query("Photo", `{}`, descending),
		`["p2004", "p2003", "p2002", "p2001", "pnone"]`)
	is(t, "by when added, those never added last", f.query("Photo", `{}`, map[string]any{"property": "added"}),
		`["pnone", "p2003", "p2002", "p2001", "p2004"]`)
	is(t, "an album, in its own order",
		f.query("Photo", `{"inAlbum": "trip"}`, map[string]any{"property": "position", "inAlbum": "trip"}),
		`["p2002", "p2001"]`)
	is(t, "everything, by place in an album, those not in it last, and backward",
		f.query("Photo", `{}`, map[string]any{"property": "position", "inAlbum": "trip", "isAscending": false}),
		`["p2001", "p2002", "p2003", "p2004", "pnone"]`)
	is(t, "an album, by when taken", f.query("Photo", `{"inAlbum": "trip"}`, map[string]any{"property": "taken"}),
		`["p2001", "p2002"]`)

	var page struct {
		IDs   []string `json:"ids"`
		Total int      `json:"total"`
	}
	f.Request(jmaptest.Do("Photo/query", jmaptest.Args{"position": 1, "limit": 2, "calculateTotal": true})).
		SingleSentence("Photo/query").Decode(&page)
	is(t, "a page", page.IDs, `["p2002", "p2003"]`)
	if page.Total != 5 {
		t.Errorf("total %d, want 5", page.Total)
	}
}

// ---- Album ------------------------------------------------------------------

func albumFixture(t *testing.T) *fixture {
	f := newFixture(t)
	for _, id := range []string{"aaaaaaaaaaaa", "bbbbbbbbbbbb", "cccccccccccc"} {
		f.photo(id, "")
	}
	f.album("trip", "Trip", "aaaaaaaaaaaa", "bbbbbbbbbbbb")
	return f
}

func TestAlbumCreate(t *testing.T) {
	f := albumFixture(t)
	set := f.set("Album", jmaptest.Args{"create": map[string]any{
		"new":  map[string]any{"slug": "vienna-2026", "title": "Vienna", "photoIds": []string{"cccccccccccc", "aaaaaaaaaaaa"}},
		"bare": map[string]any{"slug": "bare", "title": "Bare"},
	}}).AssertNoErrors()

	created := set.Created("new")
	id := set.CreatedID("new")
	delete(created, "id")
	is(t, "what the client is told", created,
		`{"description": "", "created": "2026-11-23T12:00:00Z"}`)
	is(t, "the album", f.get("Album", id), `{
		"id": "`+id+`", "slug": "vienna-2026", "title": "Vienna", "description": "",
		"created": "2026-11-23T12:00:00Z", "coverPhotoId": null, "flickrId": null,
		"photoIds": ["cccccccccccc", "aaaaaaaaaaaa"]
	}`)
	is(t, "an album with no photos", f.get("Album", set.CreatedID("bare"), "photoIds")["photoIds"], `[]`)
	is(t, "the photos of the new album, in its order",
		f.query("Photo", `{"inAlbum": "`+id+`"}`, map[string]any{"property": "position", "inAlbum": id}),
		`["cccccccccccc", "aaaaaaaaaaaa"]`)
}

// albumBecomes updates the album "trip", of two photos with the first its
// cover, and requires that the properties named in want then be as it gives
// them.
func albumBecomes(t *testing.T, desc, patch, want string) {
	t.Helper()
	t.Run(desc, func(t *testing.T) {
		f := albumFixture(t)
		var update, wanted map[string]any
		if err := json.Unmarshal([]byte(patch), &update); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(want), &wanted); err != nil {
			t.Fatal(err)
		}
		f.set("Album", jmaptest.Args{"update": map[string]any{"trip": update}}).AssertNoErrors()
		var names []string
		for name := range wanted {
			names = append(names, name)
		}
		got := f.get("Album", "trip", names...)
		delete(got, "id")
		is(t, "the album", got, want)
	})
}

func TestAlbumUpdate(t *testing.T) {
	albumBecomes(t, "text", `{"title": "Voyage", "description": "By sea."}`, `{"title": "Voyage", "description": "By sea."}`)
	albumBecomes(t, "a new slug", `{"slug": "voyage-2"}`, `{"slug": "voyage-2"}`)
	albumBecomes(t, "a slug that isn't ASCII", `{"slug": "søren"}`, `{"slug": "søren"}`)
	albumBecomes(t, "photos, in a new order, with one more",
		`{"photoIds": ["cccccccccccc", "bbbbbbbbbbbb", "aaaaaaaaaaaa"]}`,
		`{"photoIds": ["cccccccccccc", "bbbbbbbbbbbb", "aaaaaaaaaaaa"], "coverPhotoId": "aaaaaaaaaaaa"}`)
	albumBecomes(t, "no photos", `{"photoIds": [], "coverPhotoId": null}`, `{"photoIds": [], "coverPhotoId": null}`)
	albumBecomes(t, "a cover", `{"coverPhotoId": "bbbbbbbbbbbb"}`, `{"coverPhotoId": "bbbbbbbbbbbb"}`)
}

// albumRefuses makes a change to albums that must fail with the given
// error for the given properties, where the album "trip" exists and "new"
// is one to create.
func albumRefuses(t *testing.T, how, key, props, errorType string, properties ...string) {
	t.Helper()
	t.Run(how+" "+props, func(t *testing.T) {
		f := albumFixture(t)
		f.album("taken", "Taken")
		before := f.state("Album")

		var object map[string]any
		if err := json.Unmarshal([]byte(props), &object); err != nil {
			t.Fatal(err)
		}
		set := f.set("Album", jmaptest.Args{how: map[string]any{key: object}})
		var serr map[string]any
		if how == "create" {
			serr = set.NotCreated(key)
		} else {
			serr = set.NotUpdated(key)
		}
		if serr["type"] != errorType {
			t.Errorf("the error is %v, not %s", serr["type"], errorType)
		}
		if properties != nil {
			want, _ := json.Marshal(properties)
			is(t, "the properties at fault", serr["properties"], string(want))
		}
		if set.NewState() != before {
			t.Errorf("the state changed")
		}
	})
}

func TestAlbumSetRefused(t *testing.T) {
	albumRefuses(t, "create", "new", `{"title": "No slug"}`, "invalidProperties", "slug")
	albumRefuses(t, "create", "new", `{"slug": "no-title"}`, "invalidProperties", "title")
	albumRefuses(t, "create", "new", `{"slug": "x", "title": " "}`, "invalidProperties", "title")
	albumRefuses(t, "create", "new", `{"slug": "taken", "title": "Dupe"}`, "alreadyExists")
	albumRefuses(t, "create", "new", `{"slug": "x", "title": "X", "id": "mine"}`, "invalidProperties", "id")
	albumRefuses(t, "create", "new", `{"slug": "x", "title": "X", "created": "2020-01-01T00:00:00Z"}`, "invalidProperties", "created")
	albumRefuses(t, "create", "new", `{"slug": "x", "title": "X", "photoIds": ["nobody"]}`, "invalidProperties", "photoIds")
	albumRefuses(t, "update", "trip", `{"slug": "taken"}`, "alreadyExists")
	albumRefuses(t, "update", "trip", `{"slug": "Trip"}`, "invalidProperties", "slug")
	albumRefuses(t, "update", "trip", `{"slug": "a trip"}`, "invalidProperties", "slug")
	albumRefuses(t, "update", "trip", `{"slug": "a/trip"}`, "invalidProperties", "slug")
	albumRefuses(t, "update", "trip", `{"slug": "-trip"}`, "invalidProperties", "slug")
	albumRefuses(t, "update", "trip", `{"slug": ""}`, "invalidProperties", "slug")
	albumRefuses(t, "update", "trip", `{"title": null}`, "invalidProperties", "title")
	albumRefuses(t, "update", "trip", `{"photoIds": ["aaaaaaaaaaaa", "aaaaaaaaaaaa"]}`, "invalidProperties", "photoIds")
	albumRefuses(t, "update", "trip", `{"photoIds": "aaaaaaaaaaaa"}`, "invalidProperties", "photoIds")
	albumRefuses(t, "update", "trip", `{"coverPhotoId": "nobody"}`, "invalidProperties", "coverPhotoId")
	albumRefuses(t, "update", "trip", `{"flickrId": "12"}`, "invalidProperties", "flickrId")
	albumRefuses(t, "update", "nothing", `{"title": "x"}`, "notFound")
}

// A slug freed by a destroy can be taken in the same call.
func TestAlbumDestroy(t *testing.T) {
	f := albumFixture(t)
	set := f.set("Album", jmaptest.Args{
		"destroy": []string{"trip"},
		"create":  map[string]any{"new": map[string]any{"slug": "trip", "title": "Another trip"}},
	}).AssertNoErrors()
	is(t, "destroyed", set.DestroyedIDs(), `["trip"]`)
	is(t, "the albums", f.query("Album", `{}`), `["`+set.CreatedID("new")+`"]`)
	is(t, "the photos are all still there", f.query("Photo", `{}`), `["aaaaaaaaaaaa", "bbbbbbbbbbbb", "cccccccccccc"]`)
}

func TestAlbumQuery(t *testing.T) {
	f := albumFixture(t)
	f.album("best", "Zenith", "bbbbbbbbbbbb")
	f.album("alps", "Mountains")
	f.exec(`UPDATE album SET created = '2020-01-01T00:00:00Z' WHERE id = 'best'`)
	f.exec(`UPDATE album SET created = '2021-01-01T00:00:00Z' WHERE id = 'trip'`)

	is(t, "all, by slug", f.query("Album", `{}`), `["alps", "best", "trip"]`)
	is(t, "by slug", f.query("Album", `{"slug": "best"}`), `["best"]`)
	is(t, "by a slug no album has", f.query("Album", `{"slug": "worst"}`), `[]`)
	is(t, "by photo", f.query("Album", `{"photoId": "bbbbbbbbbbbb"}`), `["best", "trip"]`)
	is(t, "by photo and slug", f.query("Album", `{"photoId": "bbbbbbbbbbbb", "slug": "trip"}`), `["trip"]`)
	is(t, "by title, backward", f.query("Album", `{}`, map[string]any{"property": "title", "isAscending": false}),
		`["best", "trip", "alps"]`)
	is(t, "newest first, undated last", f.query("Album", `{}`, map[string]any{"property": "created", "isAscending": false}),
		`["trip", "best", "alps"]`)

	f.Request(jmaptest.Do("Album/query", jmaptest.Args{"filter": map[string]any{"title": "Trip"}})).
		SingleSentence("error").AssertError("unsupportedFilter")
	f.Request(jmaptest.Do("Album/query", jmaptest.Args{"sort": []map[string]any{{"property": "photoIds"}}})).
		SingleSentence("error").AssertError("unsupportedSort")
}

// ---- blobs and push -----------------------------------------------------------

func TestDownload(t *testing.T) {
	f := newFixture(t)
	f.photo("aaaaaaaaaaaa", "")
	photo := f.get("Photo", "aaaaaaaaaaaa", "blobId", "renditions")

	original := f.Download(photo["blobId"].(string), "legs.jpg", "")
	if original.Status != 200 || string(original.Body) != "original of aaaaaaaaaaaa" {
		t.Errorf("the original: status %d, body %q", original.Status, original.Body)
	}
	if got := original.Header.Get("Content-Type"); got != "image/jpeg" {
		t.Errorf("the original's type is %q", got)
	}

	rendition := photo["renditions"].(map[string]any)["500.webp"].(map[string]any)["blobId"].(string)
	part := f.DownloadRange(rendition, "small.webp", "", "bytes=0-7")
	if part.Status != 206 || string(part.Body) != "500.webp" {
		t.Errorf("part of a rendition: status %d, body %q", part.Status, part.Body)
	}
	if got := part.Header.Get("Content-Type"); got != "image/webp" {
		t.Errorf("the rendition's type is %q", got)
	}

	if got := f.Download("O"+strings.Repeat("0", 64), "x", ""); got.Status != 404 {
		t.Errorf("a blob there isn't: status %d, want 404", got.Status)
	}

	// A blob the database has and the library has lost.
	os.Remove(filepath.Join(f.dir, "originals", "aaaaaaaaaaaa.jpg"))
	if got := f.Download(photo["blobId"].(string), "legs.jpg", ""); got.Status != 404 {
		t.Errorf("a blob with no file: status %d, want 404", got.Status)
	}
}

func TestPush(t *testing.T) {
	f := newFixture(t)
	f.photo("aaaaaaaaaaaa", "")
	f.album("trip", "Trip", "aaaaaaaaaaaa")

	stream := f.Listen("types=*&closeafter=no&ping=0")
	defer stream.Close()

	// A stream begins with the states as they stand.
	is(t, "the first event", stream.NextState()["photos"], `{"Photo": "`+f.state("Photo")+`", "Album": "`+f.state("Album")+`"}`)

	set := f.set("Photo", jmaptest.Args{"update": map[string]any{"aaaaaaaaaaaa": map[string]any{"title": "Legs"}}})
	is(t, "an update to a photo", stream.NextState()["photos"], `{"Photo": "`+set.NewState()+`"}`)

	set = f.set("Photo", jmaptest.Args{"destroy": []string{"aaaaaaaaaaaa"}})
	changed := stream.NextState()["photos"]
	if changed["Photo"] != set.NewState() || changed["Album"] != f.state("Album") {
		t.Errorf("destroying a photo in an album: told of %v", changed)
	}
}
