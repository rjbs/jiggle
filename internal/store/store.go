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
// There is such a database now.  The first entry is the whole schema as it
// was when the first library was brought over from Perl jiggle's TOML
// files, on 2026-11-23.  -- claude, 2026-11-23
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

-- A file: an original, kept exactly as it came, or a rendition made from
-- one.  Its id, which is its blobId, is a letter and the SHA-256 of its
-- bytes, in hex.  The letter is O for an original, which an upload is from
-- the moment it arrives, whether or not it has become a photo yet, and R
-- for a rendition.  So the same bytes can be both, as two blobs, each kept
-- for as long as its own kind is.  Two renditions with the same bytes are
-- one blob.  The path is relative to the library.
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
--
-- The import_ columns are an import under way, or one that failed, if
-- import_error says why: the file being made into the photo's original,
-- and the name and modification time it had where it came from.  A new
-- photo has an import and no blob_id yet.  A photo has one or the other,
-- or both, while its original is being replaced or read again.
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
  import_blob_id   TEXT    UNIQUE REFERENCES blob (id),
  import_error     TEXT,
  import_name      TEXT,
  import_mtime     TEXT,
  created_modseq   INTEGER NOT NULL,
  modseq           INTEGER NOT NULL,
  CHECK ((lat IS NULL) = (lon IS NULL)),
  CHECK (blob_id IS NOT NULL OR import_blob_id IS NOT NULL),
  CHECK (import_blob_id IS NOT NULL OR
         (import_error IS NULL AND import_name IS NULL AND import_mtime IS NULL))
) STRICT;
CREATE INDEX photo_modseq ON photo (modseq);
CREATE INDEX photo_import ON photo (import_blob_id) WHERE import_blob_id IS NOT NULL;

-- A photo's tags, lowercase.  They have no order of their own, and are
-- shown in order of name.
CREATE TABLE photo_tag (
  photo_id TEXT NOT NULL REFERENCES photo (id) ON DELETE CASCADE,
  tag      TEXT NOT NULL,
  PRIMARY KEY (photo_id, tag)
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

-- The renditions a photo is shown by now: for each name, like 1024.webp,
-- the blob, its size in pixels, and what it was made from: which original,
-- by which version of the recipe, and with what extra turn.  Those may be
-- older than the photo's own original and rotate, while newer renditions
-- are being made.  They don't decide the blob's bytes, which can change
-- when the programs that make renditions do.
CREATE TABLE photo_rendition (
  photo_id TEXT    NOT NULL REFERENCES photo (id) ON DELETE CASCADE,
  name     TEXT    NOT NULL,
  blob_id  TEXT    NOT NULL REFERENCES blob (id),
  original TEXT    NOT NULL REFERENCES blob (id),
  version  TEXT    NOT NULL,
  rotate   INTEGER NOT NULL,
  width    INTEGER NOT NULL,
  height   INTEGER NOT NULL,
  PRIMARY KEY (photo_id, name)
) STRICT;
CREATE INDEX photo_rendition_blob     ON photo_rendition (blob_id);
CREATE INDEX photo_rendition_original ON photo_rendition (original);

-- The renditions made so far by a photo's import, which become the
-- photo's renditions, all at once, when every one of them has been made.
-- Until then the photo is shown by the ones it had.
CREATE TABLE import_rendition (
  photo_id TEXT    NOT NULL REFERENCES photo (id) ON DELETE CASCADE,
  name     TEXT    NOT NULL,
  blob_id  TEXT    NOT NULL REFERENCES blob (id),
  original TEXT    NOT NULL REFERENCES blob (id),
  version  TEXT    NOT NULL,
  rotate   INTEGER NOT NULL,
  width    INTEGER NOT NULL,
  height   INTEGER NOT NULL,
  PRIMARY KEY (photo_id, name)
) STRICT;
CREATE INDEX import_rendition_blob ON import_rendition (blob_id);

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

// OriginalBlobID is the blobId of an original, or of an upload that may
// become one, whose bytes have the given SHA-256, in hex.
func OriginalBlobID(sha256 string) string { return "O" + sha256 }

// RenditionBlobID is the blobId of a rendition whose bytes have the given
// SHA-256, in hex.
func RenditionBlobID(sha256 string) string { return "R" + sha256 }

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
