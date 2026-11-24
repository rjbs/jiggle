package store

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func at(clock string) time.Time {
	when, err := time.Parse(time.RFC3339, clock)
	if err != nil {
		panic(err)
	}
	return when
}

// photosIn returns the ids of the photos in the database at path, opened as
// a jiggle database.
func photosIn(t *testing.T, path string) []string {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	defer s.Close()
	rows, err := s.DB.Query(`SELECT id FROM photo ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// backsUp checks that a backup made at the given time, of a database that
// is open and has the given photos, has the given name and those photos,
// and has nothing beside it in its directory.
func backsUp(t *testing.T, clock, wantName string, photos ...string) {
	t.Helper()
	s := newStore(t)
	for _, id := range photos {
		insertPhoto(t, s, id)
	}
	var live string
	if err := s.DB.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&live); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(t.TempDir(), "backups")
	got, err := Backup(context.Background(), live, dir, at(clock))
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if want := filepath.Join(dir, wantName); got != want {
		t.Errorf("the backup is %s, want %s", got, want)
	}
	if names := namesIn(t, dir); !reflect.DeepEqual(names, []string{wantName}) {
		t.Errorf("the backup directory holds %v, want only %s", names, wantName)
	}
	if got := photosIn(t, got); !reflect.DeepEqual(got, append([]string{}, photos...)) {
		t.Errorf("the backup has photos %v, want %v", got, photos)
	}

	if _, err := Backup(context.Background(), live, dir, at(clock)); err == nil {
		t.Errorf("a second backup at %s replaced the first", clock)
	}
}

func namesIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestBackup(t *testing.T) {
	backsUp(t, "2026-11-23T09:30:00-05:00", "jiggle-20261123T143000Z.sqlite", "a", "b", "c")
	backsUp(t, "2027-01-02T03:04:05Z", "jiggle-20270102T030405Z.sqlite")
}

func TestBackupOfNothing(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.sqlite")
	if _, err := Backup(context.Background(), missing, filepath.Join(dir, "backups"), at("2026-11-23T00:00:00Z")); err == nil {
		t.Error("backing up a database that isn't there worked")
	}
	if names := namesIn(t, dir); len(names) != 0 {
		t.Errorf("backing up a database that isn't there left %v", names)
	}
}

// prunes checks that pruning a directory of the given files to keep some
// number of backups leaves the files given as left.
func prunes(t *testing.T, keep int, files, left []string) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := PruneBackups(dir, keep)
	if err != nil {
		t.Fatalf("PruneBackups: %v", err)
	}
	if got := namesIn(t, dir); !reflect.DeepEqual(got, left) {
		t.Errorf("keeping %d of %v left %v, want %v", keep, files, got, left)
	}
	if len(removed) != len(files)-len(left) {
		t.Errorf("keeping %d of %v reported removing %v", keep, files, removed)
	}
}

func TestPruneBackups(t *testing.T) {
	const (
		oct = "jiggle-20261001T000000Z.sqlite"
		nov = "jiggle-20261101T000000Z.sqlite"
		dec = "jiggle-20261201T000000Z.sqlite"
	)
	prunes(t, 2, []string{oct, nov, dec}, []string{nov, dec})
	prunes(t, 3, []string{oct, nov, dec}, []string{oct, nov, dec})
	prunes(t, 5, []string{oct}, []string{oct})

	// Whatever isn't named as a backup is neither counted nor removed.
	prunes(t, 1,
		[]string{oct, nov, "jiggle.sqlite", "jiggle-20261201T000000Z.sqlite.partial", "notes.txt"},
		[]string{nov, "jiggle-20261201T000000Z.sqlite.partial", "jiggle.sqlite", "notes.txt"})
}
