package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/rjbs/gizmo/sqlitekit"
)

// A backup's name is the time it was made, in UTC, so that backups sort by
// name into the order they were made in.
const backupName = "jiggle-20060102T150405Z.sqlite"

var backupNameRE = regexp.MustCompile(`\Ajiggle-[0-9]{8}T[0-9]{6}Z\.sqlite\z`)

// Backup writes a copy of the database at path into the directory dir, and
// returns the copy's path.  The copy is the database as it stood at one
// moment, whatever is being written to it meanwhile, so this is safe while
// a server is running, which copying the file is not.
//
// The copy is one file, complete in itself, with nothing of the write-ahead
// log beside it.  It is checked before it gets its name: a file with a
// backup's name is a whole database that SQLite finds no fault in.
//
// The database is only read, and is not brought up to this program's
// schema first: a backup is of what is there.
func Backup(ctx context.Context, path, dir string, now time.Time) (string, error) {
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	db, err := sqlitekit.OpenReadOnly(path)
	if err != nil {
		return "", err
	}
	defer db.Close()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	to := filepath.Join(dir, now.UTC().Format(backupName))
	if _, err := os.Stat(to); err == nil {
		return "", fmt.Errorf("%s already exists", to)
	}
	partial := to + ".partial"
	if err := os.Remove(partial); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	defer os.Remove(partial)

	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, partial); err != nil {
		return "", fmt.Errorf("copying %s: %w", path, err)
	}
	if err := checkBackup(ctx, partial); err != nil {
		return "", fmt.Errorf("the copy of %s is no good: %w", path, err)
	}
	if err := os.Rename(partial, to); err != nil {
		return "", err
	}
	return to, nil
}

// checkBackup has SQLite look a database over for corruption and for rows
// that refer to rows there aren't.
func checkBackup(ctx context.Context, path string) error {
	db, err := sqlitekit.OpenReadOnly(path)
	if err != nil {
		return err
	}
	defer db.Close()

	var result string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check(1)`).Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return errors.New(result)
	}
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("a row refers to a row that isn't there")
	}
	return rows.Err()
}

// PruneBackups removes all but the newest keep of the backups in dir, and
// returns the paths of those it removed.  Only files named as Backup names
// them are counted or removed.
func PruneBackups(dir string, keep int) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.Type().IsRegular() && backupNameRE.MatchString(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)

	var removed []string
	for i := 0; i < len(names)-keep; i++ {
		path := filepath.Join(dir, names[i])
		if err := os.Remove(path); err != nil {
			return removed, err
		}
		removed = append(removed, path)
	}
	return removed, nil
}
