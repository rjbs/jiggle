package store

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Create(filepath.Join(t.TempDir(), "jiggle.sqlite"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// insertPhoto makes a bare photo with the given id, by way of the kit,
// and a blob for its original.
func insertPhoto(t *testing.T, s *Store, id string) {
	t.Helper()
	ctx := context.Background()
	modseq, err := Kit.Create(ctx, s.DB, TypePhoto, id)
	if err != nil {
		t.Fatalf("Kit.Create(%s): %v", id, err)
	}
	blob := OriginalBlobID(id)
	_, err = s.DB.ExecContext(ctx, `INSERT INTO blob (id, bytes, ext, path) VALUES (?, 1, 'jpg', ?)`, blob, id+".jpg")
	if err != nil {
		t.Fatalf("inserting a blob for %s: %v", id, err)
	}
	_, err = s.DB.ExecContext(ctx,
		`INSERT INTO photo (id, type, visibility, blob_id, created_modseq, modseq)
		 VALUES (?, 'photo', 'pending', ?, ?, ?)`, id, blob, modseq, modseq)
	if err != nil {
		t.Fatalf("inserting photo %s: %v", id, err)
	}
}

// idSurvives checks that an id put into the database comes out of it, and
// out of a table that refers to it, exactly as it went in.
func idSurvives(t *testing.T, id string) {
	t.Helper()
	s := newStore(t)
	insertPhoto(t, s, id)

	if _, err := s.DB.Exec(`INSERT INTO photo_tag (photo_id, tag) VALUES (?, 'x')`, id); err != nil {
		t.Fatalf("tagging %s: %v", id, err)
	}

	var got, tagged string
	err := s.DB.QueryRow(
		`SELECT photo.id, photo_tag.photo_id FROM photo JOIN photo_tag ON photo_id = photo.id`,
	).Scan(&got, &tagged)
	if err != nil {
		t.Fatalf("reading %s back: %v", id, err)
	}
	if got != id || tagged != id {
		t.Errorf("id %q came back as %q, and %q in photo_tag", id, got, tagged)
	}
}

func TestAwkwardIDs(t *testing.T) {
	idSurvives(t, "3f9a0c21b7e4")
	idSurvives(t, "123456789012")
	idSurvives(t, "000000123456")
	idSurvives(t, "123e45678901")
	idSurvives(t, "0x1234567890")
}

func TestNewPhotoIDs(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	hex12 := regexp.MustCompile(`\A[0-9a-f]{12}\z`)

	seen := map[string]bool{}
	for range 100 {
		id, err := Kit.NewID(ctx, s.DB, TypePhoto)
		if err != nil {
			t.Fatal(err)
		}
		if !hex12.MatchString(id) {
			t.Errorf("new photo id %q is not twelve hex digits", id)
		}
		if seen[id] {
			t.Errorf("new photo id %q was given twice", id)
		}
		seen[id] = true
	}
}

// refused checks that the database turns a statement down.
func refused(t *testing.T, s *Store, desc, query string, args ...any) {
	t.Helper()
	if _, err := s.DB.Exec(query, args...); err == nil {
		t.Errorf("%s: wanted an error, and got none", desc)
	}
}

func TestConstraints(t *testing.T) {
	s := newStore(t)
	insertPhoto(t, s, "aaaaaaaaaaaa")

	refused(t, s, "an unknown visibility",
		`UPDATE photo SET visibility = 'secret'`)
	refused(t, s, "a rotation that isn't a quarter turn",
		`UPDATE photo SET rotate = 45`)
	refused(t, s, "a latitude with no longitude",
		`UPDATE photo SET lat = 40.0`)
	refused(t, s, "a tag on a photo that doesn't exist",
		`INSERT INTO photo_tag (photo_id, tag) VALUES ('bbbbbbbbbbbb', 'x')`)
	refused(t, s, "a blob that doesn't exist",
		`UPDATE photo SET blob_id = 'nope'`)
	refused(t, s, "neither an original nor an import",
		`UPDATE photo SET blob_id = NULL`)
	refused(t, s, "an import's error with no import",
		`UPDATE photo SET import_error = 'it broke'`)
}

func TestCreateRefusesAnExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jiggle.sqlite")
	s, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	if _, err := Create(path); err == nil || !strings.Contains(err.Error(), "already holds a database") {
		t.Errorf("Create over a database: got %v", err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	s.Close()
}
