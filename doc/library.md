# A library

A library is a database and a directory.

    jiggle.sqlite            the database: every photo and album, and settings
    LIBRARY/
      originals/AB/DIGEST.EXT    the photos and videos, exactly as they came
      renditions/AB/DIGEST.EXT   the images and video made from them

The database is given to each command as `--db`, and the directory as
`--library`.  The database needn't be inside the directory.

## Files

Every file is a **blob**, with a row in the database's `blob` table that
gives its path, relative to the library.  A blob's id is a letter and the
SHA-256 of its bytes: `O` for an original, which an upload is from the
moment it arrives, and `R` for a rendition.  `AB` in a path is the first
two digits of that digest.

- **Originals are never modified**, and are kept read-only.  An original
  replaced by a better file is still kept.
- **Renditions are never overwritten.**  One made again with the same
  bytes is the same blob; with different bytes, it's a new one beside the
  old.
- **Nothing records where the library is.**  A library can be moved or
  renamed.  A built site shares its pictures with the library by
  hardlinks, so move the two together with `mv` or `rsync -aH`.
- `originals/incoming/` and `renditions/making/` hold files while they
  arrive or are made.  Nothing removes what a killed server leaves there.

Nothing yet removes a blob that no photo uses (#31).

## What can't be made again

The database and `originals/`.  See [backup.md](backup.md).  Renditions
can be made again from the originals, though nothing does that for a whole
library yet (#30).

If the site is synced to Bunny, the record of what was uploaded can't be
made again either, short of uploading the whole site; see
[publishing.md](publishing.md).

## Settings

The database's `setting` table holds the site's settings, one row each:

| Setting | |
|---|---|
| `title` | The site's title. |
| `base_url` | Where the site is published, like `https://photos.example.com`. |
| `author` | Who the feed says the photos are by. |
| `location_precision` | How many decimal places of a location are published. |
| `publish.…` | Where the site is synced to; see [publishing.md](publishing.md). |

The `private_zone` table holds circles, each a `lat`, a `lon` and a
`radius` in meters.  A photo taken inside one is published with no
location.

No command makes a new library, or changes a setting.  Settings are
changed with `sqlite3`.

## A library that began with Perl Jiggle

Jiggle once kept a library as files alone, and the one library that was
moved to a database still has that layout around it:

- **Its originals and renditions are where they were**, at
  `originals/AB/ID.EXT` and under `derived/`, and the `blob` table has
  those paths.  So `derived/` is not a cache to be cleared: it holds the
  renditions of every photo that hasn't had them made again.
- **A photo's id** was the first twelve hex digits of its original's
  digest.  New photos get twelve random hex digits.
- `jiggle.toml` and `meta/`, which held what the database holds now, are
  read by nothing.
- `.jiggle/` held Perl's caches.  Nothing reads them, except
  `bunny-uploaded.json`, if `sync` is told to keep its record there.
