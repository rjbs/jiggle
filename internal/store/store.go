// Package store holds jiggle's database: the SQLite schema, and how a
// database is made and opened.  JMAP state, which is to say each type's
// modseq counter and the tombstones of what has been destroyed, is kept by
// Gizmo's sqlitekit, in tables of its own.
//
// Every column that holds an id is TEXT, so that SQLite can't take an id
// made only of digits for a number and drop its leading zeros.  Dozens of
// published photo ids are all digits.  Every table is STRICT, so that a
// column holds only what it says it does.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/rjbs/gizmo/sqlitekit"
)

// Store is an open jiggle database.
type Store struct {
	DB *sql.DB
}

// Close closes the database.
func (s *Store) Close() error { return s.DB.Close() }

// The object types that have JMAP state.
const (
	TypePhoto = "Photo"
	TypeAlbum = "Album"
)

// migrations is applied in order, and PRAGMA user_version records how many
// have run.  Never edit an entry once it has been applied to a database
// worth keeping; append a new one.
//
// Until cutover there is no such database: every one is made from scratch
// by migrate, so until then the first entry is edited freely, and is the
// whole schema.  -- claude, 2026-11-22
var migrations = []string{
	`
-- What jiggle.toml held, of the site's own settings: title, base_url,
-- author and location_precision.  A setting never given has no row.
CREATE TABLE setting (
  name  TEXT NOT NULL PRIMARY KEY,
  value TEXT NOT NULL
) STRICT;

-- A photo taken inside a private zone publishes no location.  The radius
-- is in meters.
CREATE TABLE private_zone (
  lat    REAL NOT NULL,
  lon    REAL NOT NULL,
  radius REAL NOT NULL
) STRICT;

-- A file kept exactly as it came: a photo's original, or an upload that
-- may become one.  Its id, which is its blobId, is the SHA-256 of its
-- bytes, in hex.  The path is relative to the library.
CREATE TABLE blob (
  id    TEXT    NOT NULL PRIMARY KEY,
  bytes INTEGER NOT NULL,
  ext   TEXT    NOT NULL,
  path  TEXT    NOT NULL UNIQUE
) STRICT;

-- taken is a wall-clock time, with a UTC offset only if one is known, and
-- added, flickr_uploaded and original_mtime are times with an offset, all
-- in the form of RFC 3339.
--
-- The original_ columns, width, height and duration are facts about the
-- file that blob_id names, and change only when it does.  The width and
-- height are of the original turned upright by its own EXIF orientation,
-- before the photo's rotate.
CREATE TABLE photo (
  id               TEXT    NOT NULL PRIMARY KEY,
  type             TEXT    NOT NULL CHECK (type IN ('photo', 'video')),
  title            TEXT    NOT NULL DEFAULT '',
  description      TEXT    NOT NULL DEFAULT '',
  taken            TEXT,
  visibility       TEXT    NOT NULL
                   CHECK (visibility IN ('public', 'unlisted', 'private', 'pending')),
  rotate           INTEGER NOT NULL DEFAULT 0 CHECK (rotate IN (0, 90, 180, 270)),
  added            TEXT,
  flickr_id        TEXT,
  flickr_uploaded  TEXT,
  lat              REAL,
  lon              REAL,
  location_private INTEGER NOT NULL DEFAULT 0,
  blob_id          TEXT    UNIQUE REFERENCES blob (id),
  original_name    TEXT,
  original_mtime   TEXT,
  width            INTEGER,
  height           INTEGER,
  duration         REAL,
  created_modseq   INTEGER NOT NULL,
  modseq           INTEGER NOT NULL,
  CHECK ((lat IS NULL) = (lon IS NULL))
) STRICT;
CREATE INDEX photo_modseq ON photo (modseq);

-- A photo's tags, lowercase, in the order they were given.
CREATE TABLE photo_tag (
  photo_id TEXT    NOT NULL REFERENCES photo (id) ON DELETE CASCADE,
  position INTEGER NOT NULL,
  tag      TEXT    NOT NULL,
  PRIMARY KEY (photo_id, position),
  UNIQUE (photo_id, tag)
) STRICT;
CREATE INDEX photo_tag_tag ON photo_tag (tag);

-- An album's slug is its URL, and can be changed; its id can't.
CREATE TABLE album (
  id             TEXT    NOT NULL PRIMARY KEY,
  slug           TEXT    NOT NULL UNIQUE,
  title          TEXT    NOT NULL,
  description    TEXT    NOT NULL DEFAULT '',
  created        TEXT,
  cover_photo_id TEXT    REFERENCES photo (id),
  flickr_id      TEXT,
  created_modseq INTEGER NOT NULL,
  modseq         INTEGER NOT NULL
) STRICT;
CREATE INDEX album_modseq ON album (modseq);
CREATE INDEX album_cover  ON album (cover_photo_id);

-- Membership is recorded only here, so a photo's albums and an album's
-- photos can't disagree.  A photo is in an album once.
CREATE TABLE album_photo (
  album_id TEXT    NOT NULL REFERENCES album (id) ON DELETE CASCADE,
  position INTEGER NOT NULL,
  photo_id TEXT    NOT NULL REFERENCES photo (id),
  PRIMARY KEY (album_id, position),
  UNIQUE (album_id, photo_id)
) STRICT;
CREATE INDEX album_photo_photo ON album_photo (photo_id);

-- A file made from an original: what was made (its name, like 1024.webp),
-- from which original, by which version of the recipe, and with what extra
-- turn.  Those four decide its bytes, and its id, which is its blobId, is
-- made from them.  A rendition is never overwritten.  The width and height
-- are its own, in pixels.
CREATE TABLE rendition (
  id       TEXT    NOT NULL PRIMARY KEY,
  original TEXT    NOT NULL REFERENCES blob (id),
  name     TEXT    NOT NULL,
  version  TEXT    NOT NULL,
  rotate   INTEGER NOT NULL,
  width    INTEGER NOT NULL,
  height   INTEGER NOT NULL,
  bytes    INTEGER NOT NULL,
  path     TEXT    NOT NULL UNIQUE,
  UNIQUE (original, name, version, rotate)
) STRICT;

-- The renditions a photo is shown by now.  They may be older than the
-- photo's rotate or its original, while newer ones are being made.
CREATE TABLE photo_rendition (
  photo_id     TEXT NOT NULL REFERENCES photo (id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  rendition_id TEXT NOT NULL REFERENCES rendition (id),
  PRIMARY KEY (photo_id, name)
) STRICT;
CREATE INDEX photo_rendition_rendition ON photo_rendition (rendition_id);

-- Flickr's own records of what was imported from it, byte for byte: one
-- for each photo, named by its Flickr id, and one named albums.  They hold
-- what the importer had no use for, like comments.  Nothing reads them.
CREATE TABLE flickr_record (
  name TEXT NOT NULL PRIMARY KEY,
  body BLOB NOT NULL
) STRICT;
`,
}

// Kit is Gizmo's state kit as jiggle uses it.  A new photo's id is twelve
// hex digits, so that it looks like the ids photos had when an id was the
// start of the original's digest.  An album's id is Gizmo's default.
var Kit = sqlitekit.New(
	sqlitekit.Type{Name: TypePhoto, Table: "photo", NewID: newPhotoID},
	sqlitekit.Type{Name: TypeAlbum, Table: "album"},
)

func newPhotoID() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Create makes a new, empty database at path, with a fresh epoch.  The
// file must not exist yet.
func Create(path string) (*Store, error) {
	db, err := sqlitekit.Open(path)
	if err != nil {
		return nil, err
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master`).Scan(&n); err != nil {
		db.Close()
		return nil, err
	}
	if n > 0 {
		db.Close()
		return nil, fmt.Errorf("%s already holds a database", path)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{DB: db}, nil
}

// Open opens an existing database made by Create.
func Open(path string) (*Store, error) {
	db, err := sqlitekit.Open(path)
	if err != nil {
		return nil, err
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'photo'`).Scan(&n); err != nil || n == 0 {
		db.Close()
		if err == nil {
			err = errors.New("it has no photo table")
		}
		return nil, fmt.Errorf("%s is not a jiggle database: %w", path, err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &Store{DB: db}, nil
}

func migrate(db *sql.DB) error {
	ctx := context.Background()
	if err := sqlitekit.Migrate(ctx, db, migrations); err != nil {
		return err
	}
	return Kit.Init(ctx, db)
}
