package migrate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/rjbs/jiggle/internal/derive"
	"github.com/rjbs/jiggle/internal/store"
)

// manifestFile is derived/manifest.json, Perl jiggle's record of the
// renditions it has made: for each photo and rendition, the digest of the
// original and the version of the recipe that made it.  A photo's entry may
// also have a "_failed", which is not a rendition.
type manifestFile struct {
	Version int `json:"version"`
	Photos  map[string]map[string]struct {
		SHA256  string `json:"sha256"`
		Version string `json:"version"`
	} `json:"photos"`
}

// derivedPath is where a rendition of a photo is, relative to the library.
func derivedPath(id, name string) string {
	return filepath.Join("derived", shard(id), id, name)
}

// found is a rendition in derived/ that is to be recorded.
type found struct {
	photo   *photoFile
	recipe  derive.Recipe
	version string
	path    string
	bytes   int64

	// sha256 is the digest of the file, and err what went wrong finding
	// it out.
	sha256 string
	err    error
}

// renditions records the renditions already in derived/, so that none has
// to be made again.  A rendition is recorded if the manifest says it is
// current, by the test Perl jiggle uses, and its file is there.  Anything
// else is left for the server to make.
//
// A rendition's blobId is a digest of its bytes, so every file recorded is
// read, which is most of the time a migration takes.
func (w *writer) renditions() error {
	var manifest manifestFile
	path := filepath.Join(w.lib.root, "derived", "manifest.json")
	switch data, err := os.ReadFile(path); {
	case errors.Is(err, os.ErrNotExist):
		w.lib.warnf("there is no derived/manifest.json, so no renditions are recorded")
		return nil
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(data, &manifest); err != nil {
			return fmt.Errorf("reading derived/manifest.json: %w", err)
		}
		if manifest.Version != 1 {
			return fmt.Errorf("derived/manifest.json is version %d, and migrate reads only version 1", manifest.Version)
		}
	}

	var all []*found
	incomplete, lost := 0, 0
	for _, p := range w.lib.photos {
		entry := manifest.Photos[p.ID]

		// Perl jiggle makes a photo's extra turn part of each
		// rendition's version.  Here it's a column of its own.
		turn := ""
		if p.Rotate != 0 {
			turn = fmt.Sprintf("+rot%d", p.Rotate)
		}

		complete := true
		for _, recipe := range derive.RecipesFor(p.Type) {
			version := recipe.VersionFor(p.Type)
			have, ok := entry[recipe.Name]
			if !ok || have.SHA256 != p.Original.SHA256 || have.Version != version+turn {
				complete = complete && !recipe.Publish
				continue
			}

			file := derivedPath(p.ID, recipe.Name)
			info, err := os.Stat(filepath.Join(w.lib.root, file))
			if errors.Is(err, os.ErrNotExist) {
				lost++
				complete = complete && !recipe.Publish
				continue
			}
			if err != nil {
				return err
			}
			all = append(all, &found{photo: p, recipe: recipe, version: version, path: file, bytes: info.Size()})
		}
		if !complete {
			incomplete++
		}
	}

	w.digest(all)

	for _, f := range all {
		if f.err != nil {
			return f.err
		}
		if err := w.rendition(f); err != nil {
			return fmt.Errorf("photo %s, %s: %w", f.photo.ID, f.recipe.Name, err)
		}
		w.report.Renditions++
	}

	if lost > 0 {
		w.lib.warnf("%d rendition(s) in derived/manifest.json have no file", lost)
	}
	if incomplete > 0 {
		w.lib.warnf("%d photo(s) don't have all their renditions", incomplete)
	}
	return nil
}

// digest works out the SHA-256 of every rendition found, several at a time.
func (w *writer) digest(all []*found) {
	work := make(chan *found)
	var workers sync.WaitGroup
	for range runtime.GOMAXPROCS(0) {
		workers.Go(func() {
			for f := range work {
				f.sha256, f.err = digestFile(filepath.Join(w.lib.root, f.path))
			}
		})
	}
	for _, f := range all {
		work <- f
	}
	close(work)
	workers.Wait()
}

func digestFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (w *writer) rendition(f *found) error {
	p := f.photo
	width, height := p.Original.Width, p.Original.Height
	if p.Rotate%180 != 0 {
		width, height = height, width
	}
	width, height = f.recipe.Size(width, height)

	// Two renditions can have the same bytes: a small photo's 1024.webp
	// and 2048.webp are one file twice, since nothing is enlarged.  They
	// are one blob, at the path of whichever was met first.
	id := store.RenditionBlobID(f.sha256)
	ext := strings.TrimPrefix(filepath.Ext(f.recipe.Name), ".")
	err := w.exec(`INSERT INTO blob (id, bytes, ext, path) VALUES (?, ?, ?, ?) ON CONFLICT (id) DO NOTHING`,
		id, f.bytes, ext, f.path)
	if err != nil {
		return err
	}
	return w.exec(`
		INSERT INTO photo_rendition (photo_id, name, blob_id, original, version, rotate, width, height)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, f.recipe.Name, id, store.OriginalBlobID(p.Original.SHA256), f.version, p.Rotate, width, height)
}
