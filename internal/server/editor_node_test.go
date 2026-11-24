package server

import (
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"testing"
)

// The editor is JavaScript, and so are these tests of it, which run in
// node, and are skipped without it.

// needsNode skips the test unless there's a node of version 20 or newer,
// whose test runner the tests are written for.
func needsNode(t *testing.T) {
	t.Helper()
	out, _ := exec.Command("node", "--version").Output()
	m := regexp.MustCompile(`^v(\d+)\.`).FindSubmatch(out)
	if m == nil {
		t.Skip("no node")
	}
	if major, _ := strconv.Atoi(string(m[1])); major < 20 {
		t.Skipf("node %s is older than 20", out)
	}
}

// The editor's model, share/editor-jmap/model.mjs, is tested by
// testdata/model.test.mjs.
func TestEditorModel(t *testing.T) {
	needsNode(t)
	out, err := exec.Command("node", "--test", "testdata/model.test.mjs").CombinedOutput()
	if err != nil {
		t.Errorf("node --test: %v\n%s", err, out)
	}
}

// sheetPhoto adds a photo as the fixture's photo does, and gives it the
// rendition the editor shows in its contact sheet.
func (f *fixture) sheetPhoto(id, set string, tags ...string) {
	f.t.Helper()
	f.photo(id, set, tags...)
	thumb := f.blob("R", "derived/"+id+"/h480.webp", "h480.webp of "+id)
	f.exec(`
		INSERT INTO photo_rendition (photo_id, name, blob_id, original, version, rotate, width, height)
		SELECT id, 'h480.webp', ?, blob_id, '1', 0, 640, 480 FROM photo WHERE id = ?`, thumb, id)
}

// TestEditorInABrowser drives the editor in a real browser, by way of
// testdata/editor-browser.mjs, which reports what it saw.  That needs
// Playwright, which isn't a dependency: set JIGGLE_PLAYWRIGHT to a
// directory where it's installed, with its browsers where it can find
// them, as by PLAYWRIGHT_BROWSERS_PATH.  -- claude, 2026-11-23
func TestEditorInABrowser(t *testing.T) {
	if os.Getenv("JIGGLE_PLAYWRIGHT") == "" {
		t.Skip("set JIGGLE_PLAYWRIGHT to run browser tests")
	}
	needsNode(t)

	f := newFixture(t)
	f.work()
	f.sheetPhoto("aaaa0001", `title = 'one', taken = '2026-07-01T10:00:00'`, "cat", "pet")
	f.sheetPhoto("bbbb0002", `title = 'two', taken = '2026-07-02T10:00:00'`)
	f.sheetPhoto("cccc0003", `title = 'three', taken = '2026-07-03T10:00:00'`)
	f.sheetPhoto("eeee0001", `visibility = 'pending', taken = '2026-09-01T10:00:00'`)
	f.sheetPhoto("eeee0002", `visibility = 'pending', taken = '2026-09-02T10:00:00'`)
	f.sheetPhoto("eeee0003", `visibility = 'pending', taken = '2026-09-03T10:00:00'`)
	f.sheetPhoto("dddd0001", `visibility = 'private'`)
	f.album("trip", "Trip", "aaaa0001", "bbbb0002", "cccc0003")
	f.album("hidden", "Hidden", "dddd0001")

	// Two photos made of real files: one with a real picture, so that its
	// renditions can be made again when it's turned, and one cut short,
	// whose import fails.
	whole := jpeg(t, "600", "400")
	realPhoto := f.imported(whole, `{}`)
	f.settled(realPhoto)
	f.settled(f.imported(whole[:len(whole)/8], `{}`))

	cmd := exec.Command("node", "testdata/editor-browser.mjs", f.BaseURL, realPhoto)
	cmd.Stderr = os.Stderr
	report, err := cmd.Output()
	if err != nil {
		t.Fatalf("the browser: %v\n%s", err, report)
	}
	var seen map[string]any
	if err := json.Unmarshal(report, &seen); err != nil {
		t.Fatalf("no report from the browser: %s", report)
	}

	// saw checks one thing the browser reported.
	saw := func(key, want string) {
		t.Helper()
		is(t, key, seen[key], want)
	}
	// photoHas and albumHas check what the server has, afterward.
	photoHas := func(id, property, want string) {
		t.Helper()
		is(t, "on the server: "+id+"'s "+property, f.get("Photo", id)[property], want)
	}
	albumHas := func(slug, property, want string) {
		t.Helper()
		var album map[string]any
		if ids := f.query("Album", `{"slug": "`+slug+`"}`); len(ids) == 1 {
			album = f.get("Album", ids[0])
		}
		is(t, "on the server: "+slug+"'s "+property, album[property], want)
	}

	saw("albums", `["Hidden", "Trip"]`)
	saw("album_counts", `["1 private", "3 published"]`)
	saw("album_covers", `2`)

	saw("album_query", `"album:trip"`)
	saw("album_order", `["aaaa0001", "bbbb0002", "cccc0003"]`)
	saw("album_order_edited", `["cccc0003", "aaaa0001", "bbbb0002"]`)
	saw("album_write_button", `"Write changes (album)"`)
	saw("album_written", `"written: 1 album(s)"`)
	albumHas("trip", "photoIds", `["cccc0003", "aaaa0001", "bbbb0002"]`)
	albumHas("trip", "coverPhotoId", `"cccc0003"`)

	saw("pending_query", `"pending limit:2"`)
	saw("pending_first", `["eeee0001", "eeee0002"]`)
	saw("refresh_disabled_when_dirty", `true`)
	saw("pending_written", `"written: 1 photo(s)"`)
	saw("pending_next", `["eeee0002", "eeee0003"]`)
	photoHas("eeee0001", "visibility", `"public"`)

	saw("back_query", `"album:trip"`)
	saw("bad_query_status", `"unknown query term: pendng"`)
	saw("after_bad_query", `"album:trip"`)

	saw("tags_edited", `["dog", "pet"]`)
	saw("tags_written", `"written: 1 photo(s) and 1 album(s)"`)
	saw("albums_after", `["Berlin, 2026", "Trip"]`)
	saw("dirty_after_write", `0`)
	photoHas("aaaa0001", "tags", `{"dog": true, "pet": true}`)
	albumHas("berlin-2026", "title", `"Berlin, 2026"`)
	albumHas("berlin-2026", "photoIds", `["aaaa0001"]`)

	saw("pushed_caption", `"deux"`)

	saw("conflict_caption", `"trois"`)
	saw("conflict_note",
		`"the title was changed elsewhere, to “drei”, while being edited here.  Writing keeps the edit; reverting it takes the other change."`)
	saw("conflict_tags", `["elsewhere"]`)
	saw("conflict_written", `"written: 1 photo(s)"`)
	saw("conflict_badges_after", `["cover"]`)
	photoHas("cccc0003", "title", `"trois"`)
	photoHas("cccc0003", "tags", `{"elsewhere": true}`)

	saw("dialogs", `["You have changes that aren't written.  Discard them?"]`)
	saw("albums_after_new", `["Berlin, 2026", "Hidden", "Trip"]`)
	photoHas("aaaa0001", "title", `"one"`)

	saw("waiting_all", `"1 more failed to import"`)
	saw("waiting_albums", `""`)
	saw("turned_written", `"written: 1 photo(s)"`)
	saw("turned_badges", `["pending"]`)
	saw("turned_transform", `""`)
	photoHas(realPhoto, "rotate", `90`)

	saw("errors", `[]`)
}
