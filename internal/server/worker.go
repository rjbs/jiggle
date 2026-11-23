package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rjbs/jiggle/internal/derive"
	"github.com/rjbs/jiggle/internal/store"
)

// The work of an import is done here, after the request that began it.
//
// The imports to work on are the photos with an import and no error:
// there is no queue but that, so a server that stops in the middle picks
// up where it was.  An import is done in two lanes, because one of them
// is slow: a video's encoding, of which only one or two run at once, and
// everything else, which is reading the file's facts and making its
// images (a video's, from its poster).
//
// Each lane leaves the renditions it makes in import_rendition.  When the
// last of them is there, whichever lane made it switches the photo over,
// in one transaction: the import's blob becomes the photo's original, the
// renditions become the photo's, and the import is cleared.  Until then,
// and if the import fails, the photo is as it was.
//
// An import can change while it's being worked on: given up, or begun
// again with another blob, or the photo turned.  So nothing is written for
// an import without checking that it's still the one the work was for,
// and a rendition says what it was made of, which is how one made for an
// import that has since changed is known to be stale.

// lane is one of the two lanes of work.
type lane int

const (
	imageLane lane = iota
	videoLane
)

// recipes is the recipes of a lane, for a type of media.
func (l lane) recipes(mediaType string) []derive.Recipe {
	var out []derive.Recipe
	for _, r := range derive.RecipesFor(mediaType) {
		if (r.Kind == derive.Video) == (l == videoLane) {
			out = append(out, r)
		}
	}
	return out
}

// job is the work of one lane on one photo's import.
type job struct {
	lane   lane
	photo  string
	typ    string
	blobID string
	rotate int
}

func (j job) key() string { return fmt.Sprintf("%d %s", j.lane, j.photo) }

// errSuperseded is what becomes of work on an import that is no longer the
// photo's.
var errSuperseded = errors.New("the import was given up or replaced")

// kick says that there may be imports to work on.
func (s *Server) kick() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Work does the work of imports until ctx is done.
func (s *Server) Work(ctx context.Context) {
	lanes := map[lane]chan job{imageLane: make(chan job), videoLane: make(chan job)}
	done := make(chan job)
	worker := func(jobs <-chan job) {
		for {
			select {
			case <-ctx.Done():
				return
			case j := <-jobs:
				s.do(ctx, j)
				select {
				case done <- j:
				case <-ctx.Done():
					return
				}
			}
		}
	}
	for range s.cfg.ImageWorkers {
		go worker(lanes[imageLane])
	}
	for range s.cfg.VideoWorkers {
		go worker(lanes[videoLane])
	}

	// Jobs are handed only to workers that are waiting for one, so that
	// what there is to do is looked up again, fresh, whenever a worker
	// comes free.
	underway := map[string]bool{}
	for {
		jobs, err := s.jobs(ctx)
		if err != nil && ctx.Err() == nil {
			s.cfg.Log.Error("looking for imports to work on", "error", err)
		}
		for _, j := range jobs {
			if underway[j.key()] {
				continue
			}
			select {
			case lanes[j.lane] <- j:
				underway[j.key()] = true
			default:
			}
		}

		select {
		case <-ctx.Done():
			return
		case j := <-done:
			delete(underway, j.key())
		case <-s.wake:
		case <-time.After(time.Minute):
		}
	}
}

// jobs finds the work there is to do: for each import, the lanes that
// haven't made every rendition of theirs, as the photo now stands.
func (s *Server) jobs(ctx context.Context) ([]job, error) {
	tx, err := store.Kit.Read(ctx, s.store.DB)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
		SELECT id, type, import_blob_id, rotate FROM photo
		WHERE import_blob_id IS NOT NULL AND import_error IS NULL ORDER BY modseq`)
	if err != nil {
		return nil, err
	}
	var imports []job
	for rows.Next() {
		var j job
		if err := rows.Scan(&j.photo, &j.typ, &j.blobID, &j.rotate); err != nil {
			rows.Close()
			return nil, err
		}
		imports = append(imports, j)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	var jobs []job
	for _, j := range imports {
		made, err := madeFor(ctx, tx, j)
		if err != nil {
			return nil, err
		}
		for _, l := range []lane{imageLane, videoLane} {
			for _, r := range l.recipes(j.typ) {
				if _, ok := made[r.Name]; !ok {
					j.lane = l
					jobs = append(jobs, j)
					break
				}
			}
		}
	}
	return jobs, nil
}

// queryer is a database, or a transaction on one.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// madeFor returns the renditions an import has made that are good for the
// job: made of its blob, with its turn, by the recipes as they are now.
// They are given by name, each with its blob's path.
func madeFor(ctx context.Context, q queryer, j job) (map[string]string, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT r.name, r.version, b.path FROM import_rendition r JOIN blob b ON b.id = r.blob_id
		WHERE r.photo_id = ? AND r.original = ? AND r.rotate = ?`, j.photo, j.blobID, j.rotate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	versions := map[string]string{}
	for _, r := range derive.RecipesFor(j.typ) {
		versions[r.Name] = r.VersionFor(j.typ)
	}
	made := map[string]string{}
	for rows.Next() {
		var name, version, path string
		if err := rows.Scan(&name, &version, &path); err != nil {
			return nil, err
		}
		if versions[name] == version {
			made[name] = path
		}
	}
	return made, rows.Err()
}

// do does a job, and records the import as failed if it fails.
func (s *Server) do(ctx context.Context, j job) {
	err := s.work(ctx, j)
	switch {
	case err == nil, errors.Is(err, errSuperseded), ctx.Err() != nil:
		return
	}
	s.cfg.Log.Warn("an import failed", "photo", j.photo, "blob", j.blobID, "error", err)
	failure := s.current(ctx, j, func(tx *txn) (bool, error) {
		_, err := tx.ExecContext(ctx, `UPDATE photo SET import_error = ? WHERE id = ?`, err.Error(), j.photo)
		return true, err
	})
	if failure != nil && !errors.Is(failure, errSuperseded) {
		s.cfg.Log.Error("recording that an import failed", "photo", j.photo, "error", failure)
	}
}

// current runs fn in a transaction, if the photo's import is still the one
// the job is for and hasn't failed, and commits.  If it isn't, the error is
// errSuperseded.  fn reports whether it changed the photo as a client sees
// it, which a rendition made for an import doesn't.
func (s *Server) current(ctx context.Context, j job, fn func(tx *txn) (changed bool, err error)) error {
	tx, err := s.begin(ctx, true)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var found bool
	err = tx.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM photo WHERE id = ? AND import_blob_id = ? AND import_error IS NULL)`,
		j.photo, j.blobID).Scan(&found)
	if err != nil {
		return err
	}
	if !found {
		return errSuperseded
	}
	changed, err := fn(tx)
	if err != nil {
		return err
	}
	if changed {
		if _, err := tx.Touch(ctx, store.TypePhoto, j.photo); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// work makes the renditions of a job's lane that haven't been made, and
// then finishes the import if that was the last of them.
func (s *Server) work(ctx context.Context, j job) error {
	var path string
	err := s.store.DB.QueryRowContext(ctx, `SELECT path FROM blob WHERE id = ?`, j.blobID).Scan(&path)
	if err != nil {
		return err
	}
	original := filepath.Join(s.cfg.Library, filepath.FromSlash(path))

	facts, err := derive.ReadFacts(ctx, original)
	switch {
	case err != nil:
		return err
	case !facts.Supported || facts.Media.Type != j.typ:
		return fmt.Errorf("the file is not a %s jiggle can use", j.typ)
	case facts.LivePhoto:
		return errors.New("the file is the motion of a Live Photo, which is not yet supported")
	case facts.Width <= 0 || facts.Height <= 0:
		return errors.New("the file doesn't say how large its picture is")
	}

	made, err := madeFor(ctx, s.store.DB, j)
	if err != nil {
		return err
	}
	src := derive.Source{Type: j.typ, Original: original, Rotate: j.rotate}
	if facts.Duration != nil {
		src.Duration = *facts.Duration
	}
	// A video's images are made from its poster, which is made first.
	poster := func() string {
		if path, ok := made["poster.png"]; ok {
			return filepath.Join(s.cfg.Library, filepath.FromSlash(path))
		}
		return ""
	}

	for _, r := range j.lane.recipes(j.typ) {
		if _, ok := made[r.Name]; ok {
			continue
		}
		src.Poster = poster()
		path, err := s.makeRendition(ctx, j, r, src)
		if err != nil {
			return fmt.Errorf("%s: %w", r.Name, err)
		}
		made[r.Name] = path
	}
	return s.finish(ctx, j, facts)
}

// makeRendition makes one rendition for a job, keeps it as a blob, and
// records it among those the import has made.  It returns the blob's path.
//
// A rendition's blob is named for its own bytes, so it's never written
// over: one made again with the same bytes is the same blob, and one that
// comes out differently, as from a newer libvips, is another.
func (s *Server) makeRendition(ctx context.Context, j job, r derive.Recipe, src derive.Source) (string, error) {
	making := filepath.Join(s.cfg.Library, "renditions", "making")
	if err := os.MkdirAll(making, 0o755); err != nil {
		return "", err
	}
	// The name keeps the recipe's extension, which is how libvips and
	// ffmpeg choose what to write.
	ext := strings.TrimPrefix(filepath.Ext(r.Name), ".")
	tmp, err := os.CreateTemp(making, "*."+ext)
	if err != nil {
		return "", err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	warnings, err := r.Make(ctx, src, tmp.Name())
	if err != nil {
		return "", err
	}
	if warnings != "" {
		s.cfg.Log.Warn("making a rendition", "photo", j.photo, "rendition", r.Name, "warnings", warnings)
	}
	width, height, err := r.MadeSize(ctx, tmp.Name())
	if err != nil {
		return "", err
	}
	digest, size, err := digestOf(tmp.Name())
	if err != nil {
		return "", err
	}

	id := store.RenditionBlobID(digest)
	path := "renditions/" + digest[:2] + "/" + digest + "." + ext
	full := filepath.Join(s.cfg.Library, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", err
	}

	err = s.current(ctx, j, func(tx *txn) (bool, error) {
		// The blob may be there already, at a path of its own, as a
		// rendition migrated from the old library is.
		err := tx.QueryRowContext(ctx, `SELECT path FROM blob WHERE id = ?`, id).Scan(&path)
		if errors.Is(err, sql.ErrNoRows) {
			if err := os.Rename(tmp.Name(), full); err != nil {
				return false, err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO blob (id, bytes, ext, path) VALUES (?, ?, ?, ?)`, id, size, ext, path)
		}
		if err != nil {
			return false, err
		}
		_, err = tx.ExecContext(ctx, `
			INSERT OR REPLACE INTO import_rendition (photo_id, name, blob_id, original, version, rotate, width, height)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			j.photo, r.Name, id, j.blobID, r.VersionFor(j.typ), j.rotate, width, height)
		return false, err
	})
	return path, err
}

func digestOf(path string) (digest string, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	hash := sha256.New()
	size, err = io.Copy(hash, f)
	return hex.EncodeToString(hash.Sum(nil)), size, err
}

// finish switches a photo over to what its import has made, if all of it
// has been made, for the photo as it now is.  If not, there is more work
// to do, in the other lane or in this one again, and this does nothing.
//
// What only the server sets is the file's: the photo's original, and the
// facts about it.  What a person can set, the date and the place, is the
// file's only where the photo has none.
func (s *Server) finish(ctx context.Context, j job, facts *derive.Facts) error {
	return s.current(ctx, j, func(tx *txn) (bool, error) {
		var rotate int
		if err := tx.QueryRowContext(ctx, `SELECT rotate FROM photo WHERE id = ?`, j.photo).Scan(&rotate); err != nil {
			return false, err
		}
		j.rotate = rotate
		made, err := madeFor(ctx, tx, j)
		if err != nil {
			return false, err
		}
		for _, r := range derive.RecipesFor(j.typ) {
			if _, ok := made[r.Name]; !ok {
				return false, nil
			}
		}

		var taken, lat, lon any
		if facts.Taken != "" {
			taken = facts.Taken
		}
		if facts.Location != nil {
			lat, lon = facts.Location.Lat, facts.Location.Lon
		}
		steps := []struct {
			sql  string
			args []any
		}{
			{`DELETE FROM photo_rendition WHERE photo_id = ?`, []any{j.photo}},
			{`INSERT INTO photo_rendition SELECT * FROM import_rendition WHERE photo_id = ?`, []any{j.photo}},
			{`DELETE FROM import_rendition WHERE photo_id = ?`, []any{j.photo}},
			{`UPDATE photo
			  SET blob_id = import_blob_id, original_name = import_name, original_mtime = import_mtime,
			      width = ?, height = ?, duration = ?,
			      taken = coalesce(taken, ?), lat = coalesce(lat, ?), lon = coalesce(lon, ?),
			      import_blob_id = NULL, import_error = NULL, import_name = NULL, import_mtime = NULL
			  WHERE id = ?`,
				[]any{facts.Width, facts.Height, facts.Duration, taken, lat, lon, j.photo}},
		}
		for _, step := range steps {
			if _, err := tx.ExecContext(ctx, step.sql, step.args...); err != nil {
				return false, err
			}
		}
		return true, nil
	})
}
