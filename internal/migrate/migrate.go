// Package migrate makes a jiggle database from a TOML library, the kind
// Perl jiggle keeps: jiggle.toml, meta/ and originals/.  It is how the Go
// server gets its data until cutover, and is deleted after it.
//
// Nothing in the library is changed or moved.  The database records where
// each original already is.
package migrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rjbs/gizmo/sqlitekit"

	"github.com/rjbs/jiggle/internal/store"
)

// Options says what to migrate, and to where.
type Options struct {
	// Library is the TOML library's directory.
	Library string

	// Out is the database to make.  It must not exist.
	Out string
}

// Report says what a migration did.
type Report struct {
	Photos int
	Videos int
	Albums int

	// Renditions is how many renditions already made were recorded.
	Renditions int

	// FlickrRecords is how many of Flickr's own records were copied.
	FlickrRecords int

	// Warnings are things about the library worth a look that didn't
	// stop the migration.
	Warnings []string
}

// ProblemsError is the error for a library that can't be migrated as it is.
// Each problem names a file and what is wrong with it.
type ProblemsError struct {
	Problems []string
}

func (e *ProblemsError) Error() string {
	const show = 20
	shown := e.Problems
	more := ""
	if len(shown) > show {
		more = fmt.Sprintf("\n  and %d more", len(shown)-show)
		shown = shown[:show]
	}
	return fmt.Sprintf("the library has %d problem(s):\n  %s%s",
		len(e.Problems), strings.Join(shown, "\n  "), more)
}

// Run migrates a library.  If it fails, it leaves no database behind.
func Run(ctx context.Context, opts Options) (*Report, error) {
	if _, err := os.Lstat(opts.Out); err == nil {
		return nil, fmt.Errorf("%s already exists", opts.Out)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	lib, err := readLibrary(opts.Library)
	if err != nil {
		return nil, err
	}
	if len(lib.problems) > 0 {
		return nil, &ProblemsError{Problems: lib.problems}
	}

	report, err := write(ctx, lib, opts.Out)
	if err != nil {
		for _, suffix := range []string{"", "-wal", "-shm"} {
			os.Remove(opts.Out + suffix)
		}
		return nil, err
	}
	return report, nil
}

func write(ctx context.Context, lib *library, out string) (*Report, error) {
	s, err := store.Create(out)
	if err != nil {
		return nil, err
	}
	defer s.Close()

	tx, err := store.Kit.Begin(ctx, s.DB, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	w := &writer{ctx: ctx, tx: tx, lib: lib, report: &Report{}}
	for _, step := range []func() error{w.config, w.photos, w.albums, w.renditions, w.flickr} {
		if err := step(); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	w.report.Warnings = lib.warnings
	return w.report, s.Close()
}

type writer struct {
	ctx    context.Context
	tx     *sqlitekit.Tx
	lib    *library
	report *Report
}

func (w *writer) exec(query string, args ...any) error {
	_, err := w.tx.ExecContext(w.ctx, query, args...)
	return err
}

func (w *writer) config() error {
	c := w.lib.config

	settings := map[string]*string{
		"title":    c.Title,
		"base_url": c.BaseURL,
		"author":   c.Author,
	}
	if c.LocationPrecision != nil {
		places := fmt.Sprint(*c.LocationPrecision)
		settings["location_precision"] = &places
	}
	for name, value := range settings {
		if value == nil {
			continue
		}
		if err := w.exec(`INSERT INTO setting (name, value) VALUES (?, ?)`, name, *value); err != nil {
			return fmt.Errorf("setting %s: %w", name, err)
		}
	}

	for _, z := range c.PrivateZones {
		err := w.exec(`INSERT INTO private_zone (lat, lon, radius) VALUES (?, ?, ?)`, z.Lat, z.Lon, z.Radius)
		if err != nil {
			return fmt.Errorf("private zone: %w", err)
		}
	}
	return nil
}

func (w *writer) photos() error {
	for _, p := range w.lib.photos {
		if err := w.photo(p); err != nil {
			return fmt.Errorf("photo %s: %w", p.ID, err)
		}
		if p.Type == "video" {
			w.report.Videos++
		} else {
			w.report.Photos++
		}
	}
	return nil
}

func (w *writer) photo(p *photoFile) error {
	o := p.Original

	// Two photos can't have one original.  The blob table would refuse
	// the second anyway, but less clearly.
	var other string
	err := w.tx.QueryRowContext(w.ctx, `SELECT id FROM photo WHERE blob_id = ?`, o.SHA256).Scan(&other)
	if err == nil {
		return fmt.Errorf("it has the same original as photo %s", other)
	}

	err = w.exec(`INSERT INTO blob (id, bytes, ext, path) VALUES (?, ?, ?, ?)`,
		o.SHA256, o.Bytes, o.Ext, originalPath(p.ID, o.Ext))
	if err != nil {
		return err
	}

	modseq, err := w.tx.Create(w.ctx, store.TypePhoto, p.ID)
	if err != nil {
		return err
	}

	var lat, lon *float64
	private := false
	if p.Location != nil {
		lat, lon, private = &p.Location.Lat, &p.Location.Lon, p.Location.Private
	}

	err = w.exec(`
		INSERT INTO photo (
		  id, type, title, description, taken, visibility, rotate,
		  added, flickr_id, flickr_uploaded,
		  lat, lon, location_private,
		  blob_id, original_name, original_mtime, width, height, duration,
		  created_modseq, modseq
		) VALUES (?, ?, ?, ?, ?, ?, ?,  ?, ?, ?,  ?, ?, ?,  ?, ?, ?, ?, ?, ?,  ?, ?)`,
		p.ID, p.Type, p.Title, p.Description, p.Taken.ptr(), p.Visibility, p.Rotate,
		p.Added.ptr(), p.FlickrID, p.FlickrUploaded.ptr(),
		lat, lon, private,
		o.SHA256, o.File, o.SourceMtime.ptr(), o.Width, o.Height, o.Duration,
		modseq, modseq)
	if err != nil {
		return err
	}

	for i, tag := range p.Tags {
		err := w.exec(`INSERT INTO photo_tag (photo_id, position, tag) VALUES (?, ?, ?)`, p.ID, i, tag)
		if err != nil {
			return fmt.Errorf("tag %q: %w", tag, err)
		}
	}
	return nil
}

func (w *writer) albums() error {
	for _, a := range w.lib.albums {
		if err := w.album(a); err != nil {
			return fmt.Errorf("album %s: %w", a.slug, err)
		}
		w.report.Albums++
	}
	return nil
}

func (w *writer) album(a *albumFile) error {
	id, err := w.tx.NewID(w.ctx, store.TypeAlbum)
	if err != nil {
		return err
	}
	modseq, err := w.tx.Create(w.ctx, store.TypeAlbum, id)
	if err != nil {
		return err
	}

	err = w.exec(`
		INSERT INTO album (
		  id, slug, title, description, created, cover_photo_id, flickr_id,
		  created_modseq, modseq
		) VALUES (?, ?, ?, ?, ?, ?, ?,  ?, ?)`,
		id, a.slug, *a.Title, a.Description, a.Created.ptr(), a.Cover, a.FlickrID,
		modseq, modseq)
	if err != nil {
		return err
	}

	for i, photoID := range a.Photos {
		err := w.exec(`INSERT INTO album_photo (album_id, position, photo_id) VALUES (?, ?, ?)`, id, i, photoID)
		if err != nil {
			return fmt.Errorf("photo %s: %w", photoID, err)
		}
	}
	return nil
}

// flickr copies Flickr's records, the JSON files of meta/flickr, as they
// are.
func (w *writer) flickr() error {
	dir := filepath.Join(w.lib.root, "meta", "flickr")
	files, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, file := range files {
		name, isJSON := strings.CutSuffix(file.Name(), ".json")
		if !isJSON {
			w.lib.warnf("meta/flickr/%s is not migrated", file.Name())
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, file.Name()))
		if err != nil {
			return err
		}
		if err := w.exec(`INSERT INTO flickr_record (name, body) VALUES (?, ?)`, name, body); err != nil {
			return fmt.Errorf("meta/flickr/%s: %w", file.Name(), err)
		}
		w.report.FlickrRecords++
	}
	return nil
}
