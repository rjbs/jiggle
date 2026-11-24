package site

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// outDir is an output directory, with a manifest beside it, that is built
// into again and again.
type outDir struct {
	t    *testing.T
	dir  string
	root string
}

func newOutDir(t *testing.T) *outDir {
	t.Helper()
	dir := t.TempDir()
	return &outDir{t: t, dir: dir, root: filepath.Join(dir, "site")}
}

func (o *outDir) manifest() string { return filepath.Join(o.dir, "manifest.json") }

// source makes a file outside the output, to be linked into it.
func (o *outDir) source(name, content string) string {
	o.t.Helper()
	path := filepath.Join(o.dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		o.t.Fatal(err)
	}
	return path
}

// build runs one build: a new writer, whatever the build does with it, a
// prune and a saved manifest.  It returns the writer, for its counts.
func (o *outDir) build(verify bool, do func(w *Writer)) *Writer {
	o.t.Helper()
	w := o.writer(verify)
	do(w)
	if err := w.Prune(Pruning{}); err != nil {
		o.t.Fatal(err)
	}
	if err := w.SaveManifest(); err != nil {
		o.t.Fatal(err)
	}
	return w
}

func (o *outDir) writer(verify bool) *Writer {
	o.t.Helper()
	w, err := NewWriter(o.root, o.manifest(), verify)
	if err != nil {
		o.t.Fatal(err)
	}
	return w
}

// write and link are a build's steps, which must work.
func (o *outDir) write(w *Writer, rel, content string) {
	o.t.Helper()
	if err := w.WriteFile(rel, []byte(content)); err != nil {
		o.t.Fatal(err)
	}
}

func (o *outDir) link(w *Writer, rel, source, key string) {
	o.t.Helper()
	if err := w.LinkFile(rel, source, key); err != nil {
		o.t.Fatal(err)
	}
}

// holds checks that the output is exactly the given files, each written as
// "path=content".
func (o *outDir) holds(want ...string) {
	o.t.Helper()
	var got []string
	filepath.WalkDir(o.root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		content, _ := os.ReadFile(path)
		rel, _ := filepath.Rel(o.root, path)
		got = append(got, filepath.ToSlash(rel)+"="+string(content))
		return nil
	})
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		o.t.Errorf("the output\n got: %q\nwant: %q", got, want)
	}
}

func statsAre(t *testing.T, w *Writer, want Stats) {
	t.Helper()
	if w.Stats != want {
		t.Errorf("stats: got %+v, want %+v", w.Stats, want)
	}
}

func TestWriterWritesOnlyWhatChanged(t *testing.T) {
	for _, verify := range []bool{false, true} {
		o := newOutDir(t)
		jpg := o.source("a.jpg", "pixels")

		first := o.build(verify, func(w *Writer) {
			o.write(w, "index.html", "one")
			o.write(w, "p/a/index.html", "two")
			o.link(w, "img/a/a.jpg", jpg, "v1")
		})
		statsAre(t, first, Stats{Written: 2, Linked: 1})
		o.holds("index.html=one", "p/a/index.html=two", "img/a/a.jpg=pixels")

		second := o.build(verify, func(w *Writer) {
			o.write(w, "index.html", "one")
			o.write(w, "p/a/index.html", "changed")
			o.link(w, "img/a/a.jpg", jpg, "v1")
		})
		statsAre(t, second, Stats{Written: 1, Unchanged: 2})
		if second.HTMLChanges != 1 {
			t.Errorf("HTML changes: got %d, want 1", second.HTMLChanges)
		}
		if second.TrustingManifest() == verify {
			t.Errorf("with verify %v, trusting the manifest is %v", verify, second.TrustingManifest())
		}
		o.holds("index.html=one", "p/a/index.html=changed", "img/a/a.jpg=pixels")
	}
}

func TestWriterPrunesWhatIsNoLongerMade(t *testing.T) {
	for _, verify := range []bool{false, true} {
		o := newOutDir(t)
		o.build(verify, func(w *Writer) {
			o.write(w, "index.html", "one")
			o.write(w, "p/a/index.html", "two")
			o.write(w, "p/b/index.html", "three")
		})

		second := o.build(verify, func(w *Writer) {
			o.write(w, "index.html", "one")
			o.write(w, "p/b/index.html", "three")
		})
		statsAre(t, second, Stats{Unchanged: 2, Pruned: 1})
		o.holds("index.html=one", "p/b/index.html=three")
		if _, err := os.Stat(filepath.Join(o.root, "p/a")); err == nil {
			t.Errorf("with verify %v, the emptied directory p/a is still there", verify)
		}
	}
}

// A file put in the output by hand isn't in the manifest, so only a build
// that checks the disk finds it.
func TestWriterVerifyFindsStrays(t *testing.T) {
	o := newOutDir(t)
	make := func(w *Writer) { o.write(w, "index.html", "one") }
	o.build(false, make)

	os.WriteFile(filepath.Join(o.root, "stray.html"), []byte("stray"), 0o644)
	os.WriteFile(filepath.Join(o.root, "index.html"), []byte("scribbled"), 0o644)

	statsAre(t, o.build(false, make), Stats{Unchanged: 1})
	o.holds("index.html=scribbled", "stray.html=stray")

	statsAre(t, o.build(true, make), Stats{Written: 1, Pruned: 1})
	o.holds("index.html=one")
}

func TestWriterRemakesALinkWhenItsKeyChanges(t *testing.T) {
	o := newOutDir(t)
	jpg := o.source("a.jpg", "old pixels")
	o.build(false, func(w *Writer) { o.link(w, "img/a.jpg", jpg, "v1") })

	// A rendition is replaced by a new file, not written over, so the old
	// link goes on showing the old one.
	os.Remove(jpg)
	o.source("a.jpg", "new pixels")

	statsAre(t, o.build(false, func(w *Writer) { o.link(w, "img/a.jpg", jpg, "v1") }), Stats{Unchanged: 1})
	o.holds("img/a.jpg=old pixels")

	statsAre(t, o.build(false, func(w *Writer) { o.link(w, "img/a.jpg", jpg, "v2") }), Stats{Linked: 1})
	o.holds("img/a.jpg=new pixels")
}

func TestWriterRefusesToMakeAPathTwice(t *testing.T) {
	o := newOutDir(t)
	w := o.writer(false)
	o.write(w, "index.html", "one")
	if err := w.WriteFile("index.html", []byte("two")); err == nil || !strings.Contains(err.Error(), "produced twice") {
		t.Errorf("writing a path twice: got %v", err)
	}
}

// A build that stops before saving its manifest may have written files the
// manifest doesn't know, so the next build checks the disk.
func TestWriterDistrustsTheManifestAfterAStoppedBuild(t *testing.T) {
	o := newOutDir(t)
	o.build(false, func(w *Writer) { o.write(w, "index.html", "one") })

	stopped := o.writer(false)
	o.write(stopped, "index.html", "one")
	o.write(stopped, "p/secret/index.html", "since made private")
	if !stopped.TrustingManifest() {
		t.Errorf("the stopped build didn't trust the manifest")
	}

	next := o.writer(false)
	if next.TrustingManifest() {
		t.Errorf("the build after a stopped one trusts the manifest")
	}
	o.write(next, "index.html", "one")
	next.Prune(Pruning{})
	next.SaveManifest()
	o.holds("index.html=one")

	if !o.writer(false).TrustingManifest() {
		t.Errorf("the build after that doesn't trust the manifest")
	}
}

func TestWriterKeepAndPruneByPrefix(t *testing.T) {
	o := newOutDir(t)
	o.build(false, func(w *Writer) {
		o.write(w, "index.html", "one")
		o.write(w, "old.html", "old")
		o.write(w, "pagefind/index.bin", "index")
		o.write(w, "pagefind/chunk.bin", "chunk")
	})

	w := o.writer(false)
	o.write(w, "index.html", "one")
	w.Prune(Pruning{Except: "pagefind/"})
	o.holds("index.html=one", "pagefind/index.bin=index", "pagefind/chunk.bin=chunk")
	if w.HTMLChanges != 1 {
		t.Errorf("HTML changes after pruning a page: got %d, want 1", w.HTMLChanges)
	}

	if kept := w.Keep("pagefind/"); kept != 2 {
		t.Errorf("kept %d file(s), want 2", kept)
	}
	w.Prune(Pruning{})
	w.SaveManifest()
	o.holds("index.html=one", "pagefind/index.bin=index", "pagefind/chunk.bin=chunk")

	// And what was kept is in the new manifest, so it can be pruned later.
	o.build(false, func(w *Writer) { o.write(w, "index.html", "one") })
	o.holds("index.html=one")
}
