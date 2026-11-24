package ingest

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rjbs/jiggle/internal/server"
	"github.com/rjbs/jiggle/internal/store"
)

// These tests ingest real files into a real server, which imports them
// with the programs an import uses, and they skip without those.

// world is a server with an empty library, a client of it, and a
// directory of files to ingest.
type world struct {
	t       *testing.T
	in      *Ingest
	dir     string
	uploads atomic.Int32
	log     []string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	for _, p := range []string{"vips", "vipsheader", "exiftool"} {
		if _, err := exec.LookPath(p); err != nil {
			t.Skipf("no %s", p)
		}
	}

	library := t.TempDir()
	s, err := store.Create(filepath.Join(library, "jiggle.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	srv, err := server.New(server.Config{
		Store: s, Library: library, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go srv.Work(ctx)

	w := &world{t: t, dir: t.TempDir()}
	handler := srv.Handler()
	web := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/jmap/upload/") {
			w.uploads.Add(1)
		}
		handler.ServeHTTP(rw, r)
	}))
	t.Cleanup(web.Close)

	client, err := Connect(ctx, web.URL)
	if err != nil {
		t.Fatal(err)
	}
	w.in = &Ingest{Client: client, Poll: 20 * time.Millisecond, Log: func(m string) { w.log = append(w.log, m) }}
	return w
}

// picture puts a JPEG of the given width in the directory.  Pictures of
// different widths are different files.
func (w *world) picture(name, width string) string {
	w.t.Helper()
	path := filepath.Join(w.dir, name)
	if out, err := exec.Command("vips", "black", path, width, "200", "--bands", "3").CombinedOutput(); err != nil {
		w.t.Fatalf("vips: %v: %s", err, out)
	}
	return path
}

// file puts a file with the given content in the directory.
func (w *world) file(name string, content []byte) string {
	w.t.Helper()
	path := filepath.Join(w.dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		w.t.Fatal(err)
	}
	return path
}

func (w *world) read(path string) []byte {
	w.t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		w.t.Fatal(err)
	}
	return data
}

// ingests ingests the directory, waits for the imports, and checks what
// became of each file, given like "a.jpg: ingested", in order of name,
// and how many uploads it took.  With discard, failed imports are
// discarded.  It returns the files.
func (w *world) ingests(desc string, discard bool, uploads int, want ...string) []File {
	w.t.Helper()
	ctx := context.Background()
	before := w.uploads.Load()

	paths, err := Paths([]string{w.dir})
	if err != nil {
		w.t.Fatal(err)
	}
	files, err := w.in.Files(ctx, paths)
	if err == nil {
		err = w.in.Wait(ctx, files)
	}
	if err == nil && discard {
		err = w.in.Discard(ctx, files)
	}
	if err != nil {
		w.t.Fatalf("%s: %v", desc, err)
	}

	got := []string{}
	for _, f := range files {
		got = append(got, filepath.Base(f.Path)+": "+f.Status)
	}
	if !reflect.DeepEqual(got, want) {
		w.t.Errorf("%s:\n got %q\nwant %q\n log %q", desc, got, want, w.log)
	}
	if n := int(w.uploads.Load() - before); n != uploads {
		w.t.Errorf("%s: %d upload(s), and expected %d", desc, n, uploads)
	}
	return files
}

// photo gets a photo from the server.
func (w *world) photo(id string) map[string]any {
	w.t.Helper()
	var answer struct{ List []map[string]any }
	if err := w.in.Client.Call(context.Background(), "Photo/get", map[string]any{"ids": []string{id}}, &answer); err != nil {
		w.t.Fatal(err)
	}
	if len(answer.List) == 0 {
		return nil
	}
	return answer.List[0]
}

// destroy destroys a photo.
func (w *world) destroy(id string) {
	w.t.Helper()
	var answer struct{ Destroyed []string }
	err := w.in.Client.Call(context.Background(), "Photo/set", map[string]any{"destroy": []string{id}}, &answer)
	if err != nil || len(answer.Destroyed) != 1 {
		w.t.Fatalf("destroying %s: %v, %v", id, err, answer)
	}
}

func TestIngestADirectory(t *testing.T) {
	w := newWorld(t)
	a := w.picture("a.jpg", "300")
	w.file("b-copy.jpg", w.read(a))
	w.picture("c.jpg", "310")
	w.file("notes.txt", []byte("not a photo\n"))
	w.file(".hidden.jpg", w.read(a))
	w.file("deeper/d.jpg", w.read(a))

	when := time.Date(2026, 7, 4, 12, 30, 0, 0, time.FixedZone("", -4*3600))
	if err := os.Chtimes(a, when, when); err != nil {
		t.Fatal(err)
	}

	files := w.ingests("the first time", false, 3,
		"a.jpg: ingested", "b-copy.jpg: exists", "c.jpg: ingested", "notes.txt: skipped")

	if files[1].ID != files[0].ID {
		t.Errorf("the copy is said to be photo %q, not %q", files[1].ID, files[0].ID)
	}

	photo := w.photo(files[0].ID)
	original, _ := photo["original"].(map[string]any)
	if photo["blobId"] == nil || photo["importJob"] != nil || photo["visibility"] != "pending" {
		t.Errorf("the photo isn't a pending one with an original: %v", photo)
	}
	if original["name"] != "a.jpg" {
		t.Errorf("the original's name is %v", original["name"])
	}
	modified, _ := original["modified"].(string)
	if got, err := time.Parse(time.RFC3339, modified); err != nil || !got.Equal(when) {
		t.Errorf("the original was modified at %q, not %s", modified, when)
	}

	// Nothing is sent that the server has, not even what it refused.
	w.ingests("again", false, 0,
		"a.jpg: exists", "b-copy.jpg: exists", "c.jpg: exists", "notes.txt: skipped")
}

func TestIngestWhatWasUploadedAlready(t *testing.T) {
	w := newWorld(t)
	a := w.picture("a.jpg", "300")

	f, err := os.Open(a)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := w.in.Client.Upload(context.Background(), "application/octet-stream", f, int64(len(w.read(a)))); err != nil {
		t.Fatal(err)
	}

	w.ingests("an upload that was never imported", false, 0, "a.jpg: ingested")
}

func TestIngestAFileThatFails(t *testing.T) {
	w := newWorld(t)
	whole := w.read(w.picture("a.jpg", "300"))
	w.file("broken.jpg", whole[:len(whole)/8])

	files := w.ingests("left to be dealt with", false, 2, "a.jpg: ingested", "broken.jpg: failed")
	if files[1].Reason == "" {
		t.Errorf("a failed import has no reason")
	}
	if photo := w.photo(files[1].ID); photo == nil || photo["blobId"] != nil {
		t.Errorf("the failed photo should be there, with no original: %v", photo)
	}

	// The failed photo has the file, so the file is in the library.
	files = w.ingests("again", true, 0, "a.jpg: exists", "broken.jpg: exists")

	w.destroy(files[1].ID)
	files = w.ingests("discarded", true, 0, "a.jpg: exists", "broken.jpg: discarded")
	if photo := w.photo(files[1].ID); photo != nil {
		t.Errorf("a discarded photo is still there: %v", photo)
	}
}

func TestNoServer(t *testing.T) {
	web := httptest.NewServer(http.NotFoundHandler())
	defer web.Close()
	if _, err := Connect(context.Background(), web.URL); err == nil || !strings.Contains(err.Error(), "no jiggle server") {
		t.Errorf("connecting to something else: %v", err)
	}
}
