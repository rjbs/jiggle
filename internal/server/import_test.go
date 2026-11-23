package server

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rjbs/gizmo/jmaptest"
)

// These tests import real files, made here, with the programs an import
// uses, and skip without them.

func needs(t *testing.T, programs ...string) {
	t.Helper()
	for _, p := range programs {
		if _, err := exec.LookPath(p); err != nil {
			t.Skipf("no %s", p)
		}
	}
}

func run(t *testing.T, name string, args ...string) {
	t.Helper()
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v: %s", name, strings.Join(args, " "), err, out)
	}
}

// jpeg returns a JPEG of the given size, with the given tags set by
// exiftool.  Pictures of different sizes are different files.
func jpeg(t *testing.T, width, height string, tags ...string) []byte {
	t.Helper()
	needs(t, "vips", "vipsheader", "exiftool")
	path := filepath.Join(t.TempDir(), "photo.jpg")
	run(t, "vips", "black", path, width, height, "--bands", "3")
	if len(tags) > 0 {
		run(t, "exiftool", append(append([]string{"-q", "-overwrite_original"}, tags...), path)...)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// clip returns a two-second video, 320 by 240, with sound.
func clip(t *testing.T) []byte {
	t.Helper()
	needs(t, "ffmpeg", "ffprobe", "vips", "vipsheader", "exiftool")
	path := filepath.Join(t.TempDir(), "clip.mov")
	run(t, "ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=duration=2:size=320x240:rate=10",
		"-f", "lavfi", "-i", "sine=duration=2", "-pix_fmt", "yuv420p", path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// work starts the server's work on imports, which stops with the test.
func (f *fixture) work() {
	ctx, cancel := context.WithCancel(context.Background())
	f.t.Cleanup(cancel)
	go f.srv.Work(ctx)
}

// upload uploads a file and returns its blobId.
func (f *fixture) upload(data []byte) string {
	f.t.Helper()
	up := f.Upload("application/octet-stream", data)
	if up.Status != 201 {
		f.t.Fatalf("the upload: status %d: %s", up.Status, up.Body)
	}
	return up.BlobID
}

// imports makes one Photo/import call and returns its answer, which has
// the shape of a /set's.
func (f *fixture) imports(photos map[string]any) *jmaptest.Set {
	f.t.Helper()
	return f.Request(jmaptest.Do("Photo/import", jmaptest.Args{"photos": photos})).SingleSentence("Photo/import").AsSet()
}

// imported uploads a file and imports it, with the further properties
// given as JSON, and returns the new photo's id.
func (f *fixture) imported(data []byte, props string) string {
	f.t.Helper()
	photo := map[string]any{}
	if err := json.Unmarshal([]byte(props), &photo); err != nil {
		f.t.Fatal(err)
	}
	photo["blobId"] = f.upload(data)
	return f.imports(map[string]any{"new": photo}).AssertNoErrors().CreatedID("new")
}

// settled waits until a photo's import is over, one way or the other, and
// returns the photo.
func (f *fixture) settled(id string) map[string]any {
	f.t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		photo := f.get("Photo", id)
		job, _ := photo["importJob"].(map[string]any)
		if job == nil || job["error"] != nil {
			return photo
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("the import of %s is still going: %v", id, photo)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// sizes gives the size of each of a photo's renditions, like "500x375",
// by name.
func sizes(photo map[string]any) map[string]string {
	out := map[string]string{}
	for name, r := range photo["renditions"].(map[string]any) {
		r := r.(map[string]any)
		w, _ := json.Marshal(r["width"])
		h, _ := json.Marshal(r["height"])
		out[name] = string(w) + "x" + string(h)
	}
	return out
}

func TestImportAPhoto(t *testing.T) {
	f := newFixture(t)
	data := jpeg(t, "3000", "2000", "-DateTimeOriginal=2026:07:17 17:23:17", "-OffsetTimeOriginal=+02:00",
		"-GPSLatitude=33.5", "-GPSLatitudeRef=S", "-GPSLongitude=151.25", "-GPSLongitudeRef=E")
	blobID := f.upload(data)

	set := f.imports(map[string]any{"new": map[string]any{
		"blobId": blobID, "name": "IMG_0001.JPG", "modified": "2026-07-19T17:49:45-04:00",
		"title": "Legs", "tags": map[string]bool{"Vienna": true},
	}}).AssertNoErrors()
	id := set.CreatedID("new")
	created := set.Created("new")
	delete(created, "id")
	is(t, "what the client is told", created, `{
		"type": "photo", "description": "", "visibility": "pending", "rotate": 0,
		"tags": {"vienna": true}, "added": "2026-11-23T12:00:00Z", "blobId": null, "renditions": {},
		"importJob": {"blobId": "`+blobID+`", "error": null, "name": "IMG_0001.JPG", "modified": "2026-07-19T17:49:45-04:00"}
	}`)
	is(t, "it is importing", f.query("Photo", `{"isImporting": true}`), `["`+id+`"]`)
	is(t, "it has no original yet", f.query("Photo", `{"hasBlobId": true}`), `[]`)
	before := f.state("Photo")

	f.work()
	photo := f.settled(id)
	is(t, "the photo", map[string]any{
		"importJob": photo["importJob"], "blobId": photo["blobId"], "original": photo["original"],
		"taken": photo["taken"], "location": photo["location"], "title": photo["title"], "visibility": photo["visibility"],
	}, `{
		"importJob": null, "blobId": "`+blobID+`", "title": "Legs", "visibility": "pending",
		"original": {"name": "IMG_0001.JPG", "modified": "2026-07-19T17:49:45-04:00", "size": `+jsonOf(len(data))+`,
		             "width": 3000, "height": 2000, "duration": null},
		"taken": "2026-07-17T17:23:17+02:00", "location": {"lat": -33.5, "lon": 151.25, "isPrivate": false}
	}`)
	is(t, "its renditions", sizes(photo), `{
		"h480.webp": "720x480", "500.webp": "500x333", "1024.webp": "1024x683", "2048.webp": "2048x1365", "og.jpg": "1200x800"
	}`)
	is(t, "it is no longer importing", f.query("Photo", `{"isImporting": true}`), `[]`)
	is(t, "it has an original", f.query("Photo", `{"hasBlobId": true}`), `["`+id+`"]`)

	var changes struct {
		Updated []string `json:"updated"`
	}
	f.Request(jmaptest.Do("Photo/changes", jmaptest.Args{"sinceState": before})).SingleSentence("Photo/changes").Decode(&changes)
	is(t, "the import's end is a change to the photo", changes.Updated, `["`+id+`"]`)

	small := photo["renditions"].(map[string]any)["500.webp"].(map[string]any)["blobId"].(string)
	if dl := f.Download(small, "small.webp", ""); dl.Status != 200 || dl.Header.Get("Content-Type") != "image/webp" {
		t.Errorf("a rendition: status %d, type %s", dl.Status, dl.Header.Get("Content-Type"))
	}
	if dl := f.Download(blobID, "original.jpg", ""); dl.Status != 200 || len(dl.Body) != len(data) {
		t.Errorf("the original: status %d, %d bytes", dl.Status, len(dl.Body))
	}
}

func jsonOf(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func TestImportKeepsWhatAPersonSaid(t *testing.T) {
	f := newFixture(t)
	f.work()
	id := f.imported(jpeg(t, "600", "400", "-DateTimeOriginal=2026:07:17 17:23:17",
		"-GPSLatitude=33.5", "-GPSLatitudeRef=S", "-GPSLongitude=151.25", "-GPSLongitudeRef=E"),
		`{"taken": "2001-02-03T04:05:06", "location": {"lat": 1, "lon": 2, "isPrivate": true}, "visibility": "public"}`)
	photo := f.settled(id)
	is(t, "the photo", map[string]any{"taken": photo["taken"], "location": photo["location"], "visibility": photo["visibility"]},
		`{"taken": "2001-02-03T04:05:06", "location": {"lat": 1, "lon": 2, "isPrivate": true}, "visibility": "public"}`)
}

func TestImportAVideo(t *testing.T) {
	f := newFixture(t)
	f.work()
	id := f.imported(clip(t), `{"name": "clip.mov"}`)
	photo := f.settled(id)
	if photo["importJob"] != nil {
		t.Fatalf("the import failed: %v", photo["importJob"])
	}
	is(t, "its type", photo["type"], `"video"`)
	is(t, "its renditions", sizes(photo), `{
		"poster.png": "320x240", "h480.webp": "320x240", "500.webp": "320x240", "1024.webp": "320x240",
		"2048.webp": "320x240", "og.jpg": "320x240", "video.mp4": "320x240"
	}`)
	original := photo["original"].(map[string]any)
	if d, _ := original["duration"].(float64); d < 1.9 || d > 2.1 {
		t.Errorf("its duration is %v", original["duration"])
	}
}

// importRefused makes an import that must fail with the given error.
func importRefused(t *testing.T, f *fixture, desc, props, errorType string, properties ...string) map[string]any {
	t.Helper()
	var photo map[string]any
	if err := json.Unmarshal([]byte(props), &photo); err != nil {
		t.Fatal(err)
	}
	before := f.state("Photo")
	set := f.imports(map[string]any{"new": photo})
	serr := set.NotCreated("new")
	if serr["type"] != errorType {
		t.Errorf("%s: the error is %v, not %s", desc, serr, errorType)
	}
	if properties != nil {
		is(t, desc+": the properties at fault", serr["properties"], jsonOf(properties))
	}
	if set.NewState() != before {
		t.Errorf("%s: the state changed", desc)
	}
	return serr
}

func TestImportRefused(t *testing.T) {
	f := newFixture(t)
	good := f.upload(jpeg(t, "60", "40"))
	text := f.upload([]byte("these are not the pixels you're looking for\n"))
	f.photo("aaaaaaaaaaaa", "")
	used := f.get("Photo", "aaaaaaaaaaaa", "blobId")["blobId"].(string)
	rendition := f.get("Photo", "aaaaaaaaaaaa", "renditions")["renditions"].(map[string]any)["500.webp"].(map[string]any)["blobId"].(string)

	importRefused(t, f, "no blob", `{"title": "x"}`, "invalidProperties", "blobId", "modified", "name")
	importRefused(t, f, "a blob there isn't", `{"blobId": "O00"}`, "blobNotFound")
	importRefused(t, f, "a rendition", `{"blobId": "`+rendition+`"}`, "blobNotFound")
	importRefused(t, f, "a file that isn't a picture", `{"blobId": "`+text+`"}`, "invalidPhoto")
	serr := importRefused(t, f, "another photo's original", `{"blobId": "`+used+`"}`, "alreadyExists")
	if serr["existingId"] != "aaaaaaaaaaaa" {
		t.Errorf("the photo that has the blob is given as %v", serr["existingId"])
	}
	importRefused(t, f, "a bad property", `{"blobId": "`+good+`", "visibility": "secret", "sparkle": 1}`,
		"invalidProperties", "sparkle", "visibility")
	importRefused(t, f, "a bad time", `{"blobId": "`+good+`", "modified": "yesterday"}`,
		"invalidProperties", "blobId", "modified", "name")
	importRefused(t, f, "an importJob", `{"blobId": "`+good+`", "importJob": {"blobId": "`+good+`"}}`,
		"invalidProperties", "importJob")
	importRefused(t, f, "what the server sets", `{"blobId": "`+good+`", "type": "video", "added": "2020-01-01T00:00:00Z"}`,
		"invalidProperties", "added", "type")

	id := f.imports(map[string]any{"new": map[string]any{"blobId": good}}).AssertNoErrors().CreatedID("new")
	serr = importRefused(t, f, "a blob being imported", `{"blobId": "`+good+`"}`, "alreadyExists")
	if serr["existingId"] != id {
		t.Errorf("the photo importing the blob is given as %v, not %s", serr["existingId"], id)
	}

	f.Request(jmaptest.Do("Photo/import", jmaptest.Args{"ifInState": "nonsense", "photos": map[string]any{}})).
		SingleSentence("error").AssertError("stateMismatch")
}

func TestUploadTheSameFileTwice(t *testing.T) {
	f := newFixture(t)
	data := jpeg(t, "60", "40")
	first := f.Upload("image/jpeg", data)
	second := f.Upload("text/plain", data)
	if first.BlobID != second.BlobID || !strings.HasPrefix(first.BlobID, "O") {
		t.Errorf("the blobs are %s and %s", first.BlobID, second.BlobID)
	}
	if first.Type != "image/jpeg" || second.Type != "image/jpeg" {
		t.Errorf("their types are %s and %s", first.Type, second.Type)
	}
}

// A file that says it's a JPEG and can't be read fails its import, which
// leaves a photo with an error and nothing else.
func TestImportThatFails(t *testing.T) {
	f := newFixture(t)
	f.work()
	whole := jpeg(t, "600", "400")
	id := f.imported(whole[:len(whole)/8], `{}`)

	photo := f.settled(id)
	job, _ := photo["importJob"].(map[string]any)
	if job == nil || !strings.Contains(jsonOf(job["error"]), "vips") {
		t.Fatalf("the import: %v", photo["importJob"])
	}
	if photo["blobId"] != nil || len(photo["renditions"].(map[string]any)) != 0 {
		t.Errorf("the photo has an original, or renditions: %v", photo)
	}
	is(t, "it has an error", f.query("Photo", `{"hasImportError": true}`), `["`+id+`"]`)
	is(t, "it is not importing", f.query("Photo", `{"isImporting": true}`), `[]`)

	// It can't give up, having nothing else, but it can try another file.
	gaveUp := f.set("Photo", jmaptest.Args{"update": map[string]any{id: map[string]any{"importJob": nil}}})
	if got := gaveUp.NotUpdated(id)["type"]; got != "invalidProperties" {
		t.Errorf("giving up the only import: %v", got)
	}
	better := f.upload(whole)
	f.set("Photo", jmaptest.Args{"update": map[string]any{id: map[string]any{"importJob": map[string]any{"blobId": better}}}}).
		AssertNoErrors()
	photo = f.settled(id)
	if photo["importJob"] != nil || photo["blobId"] != better {
		t.Errorf("after a better file: %v", photo)
	}
}

func TestReplaceAnOriginal(t *testing.T) {
	f := newFixture(t)
	f.work()
	id := f.imported(jpeg(t, "600", "400"), `{"title": "Legs", "name": "old.jpg"}`)
	before := f.settled(id)
	old := before["blobId"].(string)

	replacement := f.upload(jpeg(t, "400", "600"))
	f.set("Photo", jmaptest.Args{"update": map[string]any{id: map[string]any{
		"importJob": map[string]any{"blobId": replacement, "name": "new.jpg"},
	}}}).AssertNoErrors()
	after := f.settled(id)

	is(t, "the photo", map[string]any{"blobId": after["blobId"], "title": after["title"], "importJob": after["importJob"]},
		`{"blobId": "`+replacement+`", "title": "Legs", "importJob": null}`)
	original := after["original"].(map[string]any)
	is(t, "its original", map[string]any{"name": original["name"], "width": original["width"], "height": original["height"]},
		`{"name": "new.jpg", "width": 400, "height": 600}`)
	is(t, "a rendition", sizes(after)["500.webp"], `"333x500"`)
	if dl := f.Download(old, "old.jpg", ""); dl.Status != 200 {
		t.Errorf("the original it had: status %d", dl.Status)
	}
}

func TestTurningAPhotoRemakesItsRenditions(t *testing.T) {
	f := newFixture(t)
	id := f.imported(jpeg(t, "600", "400"), `{"name": "IMG_1.jpg"}`)
	f.work()
	before := f.settled(id)
	blobID := before["blobId"].(string)

	set := f.set("Photo", jmaptest.Args{"update": map[string]any{id: map[string]any{"rotate": 90}}}).AssertNoErrors()
	var got struct {
		Updated map[string]map[string]any `json:"updated"`
	}
	set.Decode(&got)
	is(t, "the client is told of the import", got.Updated[id],
		`{"importJob": {"blobId": "`+blobID+`", "error": null, "name": "IMG_1.jpg", "modified": null}}`)

	after := f.settled(id)
	is(t, "a rendition, turned", sizes(after)["500.webp"], `"333x500"`)
	is(t, "the rendition as it was", sizes(before)["500.webp"], `"500x333"`)
	original := after["original"].(map[string]any)
	is(t, "the original is as it was", map[string]any{"blobId": after["blobId"], "name": original["name"], "width": original["width"]},
		`{"blobId": "`+blobID+`", "name": "IMG_1.jpg", "width": 600}`)
}

// With nobody working on imports, one can be seen to begin and be given up.
func TestGivingUpAnImport(t *testing.T) {
	f := newFixture(t)
	f.photo("aaaaaaaaaaaa", "")
	before := f.get("Photo", "aaaaaaaaaaaa")

	f.set("Photo", jmaptest.Args{"update": map[string]any{"aaaaaaaaaaaa": map[string]any{"rotate": 180}}}).AssertNoErrors()
	if f.get("Photo", "aaaaaaaaaaaa", "importJob")["importJob"] == nil {
		t.Fatalf("turning the photo began no import")
	}
	f.set("Photo", jmaptest.Args{"update": map[string]any{"aaaaaaaaaaaa": map[string]any{"rotate": 0, "importJob": nil}}}).
		AssertNoErrors()
	is(t, "the photo is as it was", f.get("Photo", "aaaaaaaaaaaa"), jsonOf(before))
}

func TestImportJobRefused(t *testing.T) {
	f := newFixture(t)
	f.photo("aaaaaaaaaaaa", "")
	f.photo("bbbbbbbbbbbb", "")
	other := f.get("Photo", "bbbbbbbbbbbb", "blobId")["blobId"].(string)
	video := f.upload(clip(t))

	refused := func(desc, job, errorType string) {
		t.Helper()
		var value any
		if err := json.Unmarshal([]byte(job), &value); err != nil {
			t.Fatal(err)
		}
		set := f.set("Photo", jmaptest.Args{"update": map[string]any{"aaaaaaaaaaaa": map[string]any{"importJob": value}}})
		if got := set.NotUpdated("aaaaaaaaaaaa")["type"]; got != errorType {
			t.Errorf("%s: %v, want %s", desc, got, errorType)
		}
	}
	refused("another photo's original", `{"blobId": "`+other+`"}`, "alreadyExists")
	refused("a blob there isn't", `{"blobId": "O00"}`, "blobNotFound")
	refused("a video, for a photo", `{"blobId": "`+video+`"}`, "invalidProperties")
	refused("an error", `{"blobId": "`+video+`", "error": "it broke"}`, "invalidProperties")
	refused("no blob", `{"name": "x.jpg"}`, "invalidProperties")
	refused("something else", `{"blobId": "`+video+`", "priority": 1}`, "invalidProperties")
	refused("not an object", `"`+video+`"`, "invalidProperties")
}
