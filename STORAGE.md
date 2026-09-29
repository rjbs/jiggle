# jiggle's storage

This is the specification of a jiggle library: what's in it, what each part
promises, and what can and can't change later.  `PLAN.md` is the broader plan;
this is the contract the code keeps.

## The library

A library is a directory:

    jiggle.toml      configuration, including the format version
    originals/       the photos and videos, exactly as they came
    meta/            metadata: what a person edits, and what was imported
    derived/         renditions made from the originals; a cache
    site/            the published site; rebuilt from the rest
    .jiggle/         jiggle's own caches and manifests; disposable

**Nothing inside the library records where the library is.**  Every path jiggle
stores is relative, so a library can be moved or renamed and its next build
does nothing.  (A test moves one to check.)  `site/` shares its renditions with
`derived/` through hardlinks, so move a library with `mv`, or with
`rsync -aH`; a plain copy duplicates them.  If that happens, `jiggle build
--verify` restores the links.

**What to back up:** `originals/` (write-once, so sync it without overwriting),
`meta/` (a git repository), and `jiggle.toml`.  Everything else can be rebuilt.

## `jiggle.toml`

```toml
format   = 1        # the library format; see below
title    = "Photos"
base_url = "https://photos.example.com"

location_precision = 3    # decimal places for published locations

[[private_zone]]          # photos taken inside publish no location
lat    = 40.0
lon    = -75.0
radius = 500              # meters
```

**`format`** versions the layout and schema described here.  A library without
one is format 1, which is today's.  jiggle refuses a library whose format is
newer than it knows.  When the format changes, a migration moves libraries
forward.  `jiggle init DIR` makes a new library at the current format.

## `originals/<shard>/<id>.<ext>`

- **A photo's id is the first 12 hex digits of its original's SHA-256.**  The
  shard is the id's first 2 digits.  Ids are deterministic: the same file gets
  the same id on any machine, which makes importing idempotent and duplicates
  obvious.
- **Originals are never modified**, not even to add metadata.  They're
  read-only on disk.  Replacing an original's bytes makes a *new* photo with a
  new id.
- The extension comes from the file's content, not its name: `jpg`, `png`,
  `heic`, `webp`, `gif`, `mov`, `mp4`, `m4v`, or `avi`.
- On macOS, ingest clones originals (`cp -c`) when the source is on the same
  APFS volume, so importing from a local archive costs no extra space.

**Committed:** the id scheme.  Once photo URLs are shared, changing it would
break them.

## `meta/`

`meta/` is the part of a library that becomes precious: once a person starts
editing it, it can't be re-derived.  It should be a git repository.

### Photos: `meta/<shard>/<id>.toml`

```toml
id = "3f9a0c21b7e4"
type = "photo"                    # or "video"
title = "we've got legs"
description = "Markdown; newlines are line breaks"
taken = 2008-01-06T19:36:11       # TOML datetime; an offset only if known
tags = ["high-st"]                # as typed; normalized only for URLs
visibility = "public"             # or "private": kept, never published
rotate = 90                       # extra clockwise turn; usually absent
added = 2008-01-06T21:32:33-05:00   # when it joined the collection
flickr_id = "2173311823"          # imported photos only
flickr_uploaded = 2008-01-06T21:32:33-05:00

[original]                        # facts about the original; don't edit
file = "weve-got-legs_2173311823_o.jpg"   # its name when ingested
ext = "jpg"
sha256 = "3f9a0c21b7e4…"
bytes = 2911886
width = 2816                      # upright by its EXIF orientation
height = 2112
duration = 7.165                  # videos only
source_mtime = 2026-07-19T17:49:45-04:00   # ingested files only

[location]
lat = 40.623775
lon = -75.373222
private = true                    # never publish this location
```

- `taken` is a wall-clock time.  It has an offset when the file's EXIF gives
  one; Flickr's dates don't carry a real one, so imported dates usually don't.
- `added` is when the photo joined the collection: set at ingest, or to the
  Flickr upload time on import.  Photos imported before it existed have only
  `flickr_uploaded`, which means the same.  The feed sorts by it.
- `rotate` is a turn beyond what the file's EXIF orientation calls for, like a
  photo rotated by hand on Flickr.  Changing it remakes that photo's
  renditions.
- `visibility`: Flickr's "friend & family" is imported as private.  The
  original value is kept in the raw record (below).

### Albums: `meta/albums/<slug>.toml`

```toml
title = "dining table, 2008-01"
description = "Markdown"
created = 2008-01-06T21:49:26-05:00
cover = "3f9a0c21b7e4"
flickr_id = "72157603651676472"
photos = [
  "3f9a0c21b7e4",
  "…",
]
```

The slug is the file's name, and the album's URL.  Albums are listed newest
first by `created`.  Order is by the `photos` list; membership is recorded
only here, so reordering an album is an edit to one file.

### Flickr's records: `meta/flickr/`

`<flickr id>.json` for each imported photo, and `albums.json`, copied from
Flickr's data export byte for byte.  They hold everything the importer
doesn't use (comments, people, notes, counts, license, location accuracy,
Flickr's privacy level), so none of it is lost.  jiggle reads nothing from
them after import.

## `derived/`

`derived/<shard>/<id>/` holds each photo's renditions: `h480.webp` (grids),
`500.webp`, `1024.webp`, `2048.webp` (photo pages), and `og.jpg` (link
previews), plus `video.mp4` and `poster.png` for a video.  Every rendition is
upright, in sRGB, and stripped of all metadata, so none carries a location.

`derived/manifest.json` records, for each rendition, the original's digest and
the recipe version that made it.  A rendition is remade only when one of
those changes, or its photo's `rotate` does.  A photo whose renditions failed
is noted there and not retried until its original changes.

**Committed:** nothing, except the rendition names, which appear in published
URLs.  All of `derived/` can be deleted and remade (about 80 CPU-minutes for
12,000 photos).

## `site/`

The published site, rebuilt in full by every build, with files written only
when their content changes and renditions hardlinked from `derived/`.

**Private photos are never in `site/`**: not as pages, thumbnails, map points,
search entries, or album covers.  Nor are the locations of photos in private
zones or with private locations, and every published location is rounded.

**Committed once shared:** the URLs.

    /p/<id>/                 a photo, and its renditions: /p/<id>/1024.webp
    /albums/<slug>/
    /tags/<tag slug>/
    /<year>/  /<year>/<month>/  /archive/
    /map/  /search/  /feed.xml

## `.jiggle/`

- `meta-cache.json`: parsed metadata, reused for files whose size and
  modification time haven't changed
- `site-manifest.json`: what each published path holds, so a build compares
  against it instead of re-reading the site.  A `.building` marker beside it
  means a build was interrupted, and the next build checks the disk instead.

Everything here can be deleted, at the cost of one slower build.
`jiggle build --verify` ignores the manifests and checks everything.

## What's one-way

Importing is reversible until the first hand edit of `meta/`.  Ids come from
file contents, so deleting `meta/` and importing again gives the same ids,
and the existing renditions are reused.  After hand edits, re-importing would
overwrite them, so changes to how import maps Flickr's data need a migration
script over `meta/` instead: best reviewed as a git diff.

The other commitment is the URLs, once they've been shared.
