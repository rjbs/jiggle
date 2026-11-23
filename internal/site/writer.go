package site

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Writer puts files into the output tree, touching only what changed.
//
// The site is rebuilt in full every time, but the sync to the web host
// compares files, so the build must not disturb files whose content hasn't
// changed.  Content is written only when it differs from what's there, and
// renditions are hardlinked from where they're kept, not copied.
//
// The writer also remembers every path it was asked to produce.  After a
// build, Prune removes everything else, which is how a photo that became
// private (or was destroyed) disappears from the published site.
//
// # The manifest
//
// If given a manifest file, the writer records there what it put at each
// path: a digest of written content, or the source and key of a link.  The
// next build then compares against the manifest instead of the files:
// unchanged content isn't read back, unchanged links aren't stat'ed, and
// pruning deletes the manifest's leftover paths without walking the tree.
//
// The manifest is trusted, so a file changed by hand in the output isn't
// noticed.  With Verify, or with no readable manifest, every file is
// checked against the disk instead, and the manifest is rebuilt.
type Writer struct {
	root     string
	manifest string
	verify   bool

	// old is what the last build recorded, or nil if there's nothing to
	// trust.
	old map[string]record

	// produced is what this build has made: relative path to record.
	produced map[string]record

	Stats Stats

	// HTMLChanges is how many HTML files were written or removed, which
	// tells whether the search index could need rebuilding.
	HTMLChanges int
}

// Stats counts what a Writer did.
type Stats struct {
	Written, Unchanged, Linked, Pruned int
}

// record is what the manifest says of one path: the SHA-1 of content that
// was written, or the source and key of a link.
type record struct {
	SHA1 string `json:"sha1,omitempty"`
	Link string `json:"link,omitempty"`
	Key  string `json:"key,omitempty"`
}

type manifestData struct {
	Version int               `json:"version"`
	Files   map[string]record `json:"files"`
}

// NewWriter returns a writer into the directory root.  The manifest may be
// empty, for a writer that keeps none.
//
// A build that stops partway (killed, or failing on an error) may have
// written files its manifest never records.  If one of those is for a photo
// that's since been made private, a prune by the manifest would never
// remove it.  So a marker file says a build is under way, and is removed
// only when the manifest is saved; finding it means the last build didn't
// finish, and the manifest isn't trusted.
func NewWriter(root, manifest string, verify bool) (*Writer, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	w := &Writer{root: root, manifest: manifest, verify: verify, produced: map[string]record{}}
	if manifest == "" {
		return w, nil
	}

	_, err = os.Stat(w.marker())
	interrupted := err == nil
	if err := os.MkdirAll(filepath.Dir(manifest), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(w.marker(), nil, 0o644); err != nil {
		return nil, err
	}
	if verify || interrupted {
		return w, nil
	}

	data, err := os.ReadFile(manifest)
	if err != nil {
		return w, nil
	}
	var m manifestData
	if json.Unmarshal(data, &m) == nil && m.Files != nil {
		w.old = m.Files
	}
	return w, nil
}

func (w *Writer) marker() string { return w.manifest + ".building" }

// TrustingManifest reports whether the writer is comparing against a
// manifest, and not checking the disk (because of verify, or because
// there's no manifest, or because the last build didn't finish).
func (w *Writer) TrustingManifest() bool { return w.old != nil }

func (w *Writer) claim(rel string, r record) (string, error) {
	if _, ok := w.produced[rel]; ok {
		return "", fmt.Errorf("path %s produced twice", rel)
	}
	w.produced[rel] = r
	return filepath.Join(w.root, filepath.FromSlash(rel)), nil
}

func (w *Writer) changed(rel string) {
	if strings.HasSuffix(rel, ".html") {
		w.HTMLChanges++
	}
}

// WriteFile writes content at a path relative to the root, with slashes,
// unless that is what the file holds already.
func (w *Writer) WriteFile(rel string, content []byte) error {
	sum := sha1.Sum(content)
	digest := hex.EncodeToString(sum[:])
	dest, err := w.claim(rel, record{SHA1: digest})
	if err != nil {
		return err
	}

	if w.old != nil {
		if w.old[rel].SHA1 == digest {
			w.Stats.Unchanged++
			return nil
		}
	} else if info, err := os.Stat(dest); err == nil && info.Size() == int64(len(content)) {
		if had, err := os.ReadFile(dest); err == nil && bytes.Equal(had, content) {
			w.Stats.Unchanged++
			return nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(dest, content, 0o644); err != nil {
		return err
	}
	w.Stats.Written++
	w.changed(rel)
	return nil
}

// LinkFile makes the path a hardlink to source, unless it already is one.
// The key should change whenever the source is replaced, and the link is
// remade when it does: a replaced file is a new file, and an old hardlink
// would go on showing the old one.  If hardlinking fails (say, because the
// output is on another filesystem), the file is copied instead.
func (w *Writer) LinkFile(rel, source, key string) error {
	// The source is recorded relative to the output directory, so that
	// the whole library can move without every link looking changed.
	abs, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	from, err := filepath.Rel(w.root, abs)
	if err != nil {
		return err
	}
	from = filepath.ToSlash(from)

	dest, err := w.claim(rel, record{Link: from, Key: key})
	if err != nil {
		return err
	}

	if had, ok := w.old[rel]; ok && had.Link == from && had.Key == key {
		w.Stats.Unchanged++
		return nil
	}

	src, err := os.Stat(source)
	if err != nil {
		return err
	}
	dst, err := os.Stat(dest)
	if err == nil && os.SameFile(src, dst) {
		w.Stats.Unchanged++
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err == nil {
		if err := os.Remove(dest); err != nil {
			return err
		}
	}
	if os.Link(source, dest) != nil {
		if err := copyFile(source, dest); err != nil {
			return err
		}
	}
	w.Stats.Linked++
	return nil
}

func copyFile(source, dest string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Keep claims every path under the prefix that the last build produced, as
// if it had been produced again, leaving the files alone.  It's for output
// that a build can tell needn't be remade, like a search index when no page
// changed.  It returns the number of paths kept, which is zero if there's
// no manifest to go by.
func (w *Writer) Keep(prefix string) int {
	n := 0
	for rel, had := range w.old {
		if !strings.HasPrefix(rel, prefix) {
			continue
		}
		if _, ok := w.produced[rel]; ok {
			continue
		}
		w.produced[rel] = had
		w.Stats.Unchanged++
		n++
	}
	return n
}

// Pruning says which files a Prune may remove: with Except, those outside a
// path prefix, and with Only, those inside one.  That lets a build prune in
// two passes, around a step that reads the output tree.
type Pruning struct {
	Except string
	Only   string
}

// Prune removes every file that wasn't produced during this run, then
// removes any directories left empty.
//
// With a manifest, the files to remove are the ones it lists that weren't
// produced.  Without one, the whole tree is walked.
func (w *Writer) Prune(p Pruning) error {
	wanted := func(rel string) bool {
		if _, ok := w.produced[rel]; ok {
			return false
		}
		if p.Except != "" && strings.HasPrefix(rel, p.Except) {
			return false
		}
		if p.Only != "" && !strings.HasPrefix(rel, p.Only) {
			return false
		}
		return true
	}

	dirs := map[string]bool{}
	remove := func(rel, path string) error {
		if err := os.Remove(path); err != nil {
			return err
		}
		w.Stats.Pruned++
		w.changed(rel)
		return nil
	}

	if w.old != nil {
		for rel := range w.old {
			if !wanted(rel) {
				continue
			}
			path := filepath.Join(w.root, filepath.FromSlash(rel))
			if _, err := os.Lstat(path); err != nil {
				continue
			}
			if err := remove(rel, path); err != nil {
				return err
			}
			for dir := filepath.Dir(path); dir != w.root && strings.HasPrefix(dir, w.root); dir = filepath.Dir(dir) {
				dirs[dir] = true
			}
		}
	} else {
		err := filepath.WalkDir(w.root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, os.ErrNotExist) && path == w.root {
					return fs.SkipAll
				}
				return err
			}
			if entry.IsDir() {
				if path != w.root {
					dirs[path] = true
				}
				return nil
			}
			rel, err := filepath.Rel(w.root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if !wanted(rel) {
				return nil
			}
			return remove(rel, path)
		})
		if err != nil {
			return err
		}
	}

	// Deepest first, so a parent is examined after its children are gone.
	// Removing a directory that isn't empty fails, harmlessly.
	deepest := make([]string, 0, len(dirs))
	for dir := range dirs {
		deepest = append(deepest, dir)
	}
	sort.Slice(deepest, func(i, j int) bool { return len(deepest[i]) > len(deepest[j]) })
	for _, dir := range deepest {
		os.Remove(dir)
	}
	return nil
}

// SaveManifest records what this build produced, for the next one.  Call it
// after the final Prune.
func (w *Writer) SaveManifest() error {
	if w.manifest == "" {
		return nil
	}
	data, err := json.Marshal(manifestData{Version: 1, Files: w.produced})
	if err != nil {
		return err
	}
	if err := os.WriteFile(w.manifest, data, 0o644); err != nil {
		return err
	}
	return os.Remove(w.marker())
}
