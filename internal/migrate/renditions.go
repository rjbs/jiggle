package migrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rjbs/jiggle/internal/derive"
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

// renditions records the renditions already in derived/, so that none has
// to be made again.  A rendition is recorded if the manifest says it is
// current, by the test Perl jiggle uses, and its file is there.  Anything
// else is left for the server to make.
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

	incomplete, lost := 0, 0
	for _, p := range w.lib.photos {
		o := p.Original
		entry := manifest.Photos[p.ID]

		// Perl jiggle makes a photo's extra turn part of each
		// rendition's version.  Here it's a column of its own.
		turn := ""
		if p.Rotate != 0 {
			turn = fmt.Sprintf("+rot%d", p.Rotate)
		}
		width, height := o.Width, o.Height
		if p.Rotate%180 != 0 {
			width, height = height, width
		}

		complete := true
		for _, recipe := range derive.RecipesFor(p.Type) {
			version := recipe.VersionFor(p.Type)
			have, ok := entry[recipe.Name]
			if !ok || have.SHA256 != o.SHA256 || have.Version != version+turn {
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

			id := derive.BlobID(o.SHA256, recipe.Name, version, p.Rotate)
			rw, rh := recipe.Size(width, height)
			err = w.exec(`
				INSERT INTO rendition (id, original, name, version, rotate, width, height, bytes, path)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				id, o.SHA256, recipe.Name, version, p.Rotate, rw, rh, info.Size(), file)
			if err == nil {
				err = w.exec(`INSERT INTO photo_rendition (photo_id, name, rendition_id) VALUES (?, ?, ?)`,
					p.ID, recipe.Name, id)
			}
			if err != nil {
				return fmt.Errorf("photo %s, %s: %w", p.ID, recipe.Name, err)
			}
			w.report.Renditions++
		}
		if !complete {
			incomplete++
		}
	}

	if lost > 0 {
		w.lib.warnf("%d rendition(s) in derived/manifest.json have no file", lost)
	}
	if incomplete > 0 {
		w.lib.warnf("%d photo(s) don't have all their renditions", incomplete)
	}
	return nil
}
