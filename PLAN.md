# jiggle: a static photo site to replace Flickr

This is the working plan.  It records what we've decided, what's still open,
and the order we expect to build things in.  Open questions are tracked as
Loose Threads (`lt list`); this document names them but doesn't duplicate
their detail.

## Goals

Replace Flickr (about 11,000 photos and short videos, about 30 GB) with a
statically generated site that is:

* built on the laptop, incrementally, so adding a day's photos doesn't mean
  reprocessing 11,000 images
* synchronized incrementally to cheap object storage (Cloudflare R2 to start)
* backed by plain files: originals, TOML metadata in git, and a derived cache

Features carried over from Flickr: albums, tags, title and description,
photos on a map, simple search, OpenGraph previews for photo pages, and short
video.  Things deliberately left behind: comments, faves, groups, and
friends/family visibility.

## The library

A library is a directory, separate from this code repository, with a
`jiggle.toml` at its root and three trees:

    library/
      jiggle.toml       configuration (site title, base URL, private zones)
      originals/        write-once originals, never modified after ingest
      meta/             one TOML file per photo, plus albums; a git repo
      derived/          generated images and video; a cache, reproducible

Every path is computed from the photo's ID, so any tool (including a shell
one-liner) can find a photo's files without a lookup:

    originals/<shard>/<id>.<ext>
    meta/<shard>/<id>.toml
    derived/<shard>/<id>/<rendition>

The shard is a short prefix of the ID.  All path construction goes through
one routine, so the sharding rule can change without touching callers.

**Backups:** originals sync to the Synology in a mode that never deletes or
overwrites (and from there to B2).  `meta/` is a git repository with a remote.
`derived/` isn't backed up at all.

### Photo metadata

```toml
id          = "3f9a0c21b7e4"
type        = "photo"            # or "video"
title       = "Stephansdom at dusk"
description = """
Longer text.  Markdown.
"""
taken       = 2026-07-17T17:23:17+02:00   # a TOML datetime; offset if known
tags        = [ "vienna", "church" ]
visibility  = "public"           # "private": kept, never published
flickr_id   = "53012345678"      # only on imported photos; provenance

[original]
file        = "IMG_9959.JPG"     # name at ingest time
ext         = "jpg"
sha256      = "…"
bytes       = 3014470
width       = 5712               # after applying EXIF orientation
height      = 4284

[location]
lat         = 48.1214722
lon         = 16.5606139
```

The `[original]` table records facts about an immutable file, so storing them
here is safe, and it means page generation never has to open an image.

An absent `visibility` means public.  A string rather than a boolean leaves
room for other levels later, if ever needed.

### Albums

Albums have order, a cover, and their own title and description, so each gets
its own file, `meta/albums/<slug>.toml`:

```toml
title       = "Vienna, 2026-07"
description = "…"
cover       = "3f9a0c21b7e4"
photos      = [ "3f9a0c21b7e4", "…" ]
```

Tags stay on the photos.

### IDs

Every photo, imported or new, gets an ID in a single new scheme.  Flickr IDs
survive only as `flickr_id`, which the importer uses for joins and a one-time
script uses to rewrite the ~165 Flickr links in the blog.

**A photo's ID is the first 12 hex digits of its original's SHA-256** (#1),
and that's an invariant: replacing an original's bytes makes a new photo with
a new ID.  The shard is the first two hex digits, giving 256 evenly filled
directories.

This makes IDs deterministic (the same file gets the same ID on any machine,
so a re-run import is idempotent), and it makes duplicate detection cheap:
ingest checks whether one metadata file exists, and never needs to load the
library.  `fsck` can check that every ID is a prefix of its recorded digest.

At 48 bits, the chance of any collision is about 1 in 5 million at 11,000
photos, and still about 1 in 225,000 at 50,000.  A collision (same prefix,
different digest) is a fatal error at ingest, left for a person to resolve.
8 hex digits (32 bits) would have been too few: about a 1.4% chance of a
collision in the Flickr import alone.

## Derivatives

Generated with libvips, which is fast and memory-frugal, in parallel.

| rendition     | use                                   | format |
|---------------|---------------------------------------|--------|
| `h480.webp`   | grid thumbnails, 480px tall           | WebP   |
| `500.webp`    | small, `srcset`                       | WebP   |
| `1024.webp`   | medium, `srcset`                      | WebP   |
| `2048.webp`   | large, photo page, `srcset`           | WebP   |
| `og.jpg`      | OpenGraph image, ~1200px              | JPEG   |

Every derivative is:

* **rotated** according to EXIF orientation
* **converted to sRGB** from whatever profile the original has (iPhone photos
  are Display P3); without this, stripping metadata would silently leave
  P3 pixels to be read as sRGB, and the images would look dull
* **stripped** of all metadata, which removes EXIF GPS

**Freshness:** each photo's derived directory holds a small state file
recording, per rendition, the original's SHA-256 and the recipe version that
produced it.  A rendition is rebuilt only when either has changed.  Bumping a
recipe's version regenerates just that rendition across the library.

Video (later): the original is kept; the published rendition is H.264 MP4
with `+faststart`, downscaled if large, plus a poster frame.  Phone
"more compatible" MOVs are already H.264, so this is often just a remux.

## Building the site

`jiggle build` renders the whole site into `site/` every time.  HTML is cheap;
11,000 pages take seconds.  Two rules keep the sync small:

* **write-if-changed:** a file is written only if its bytes differ, so
  unchanged pages keep their mtime and aren't re-uploaded
* **derivatives are hardlinked** into `site/` rather than copied, so `site/`
  is exactly what gets published without doubling disk use

**Private photos are treated as absent** from everything published: photo
pages, album and tag pages, counts, album covers, the map, the search index,
and the manifest for the blog.  Their derivatives exist for local tools but
are never linked into `site/`.

### Pages

* `/` — recent photos
* `/p/<id>/` — photo page: large image with `srcset`, title, description,
  date, tags, albums, small map, OpenGraph tags
* `/albums/` and `/albums/<slug>/`
* `/tags/` and `/tags/<tag>/`
* `/map/` — every located photo
* `/search/`

Large tag pages may need pagination eventually; `loading="lazy"` on
thumbnails goes a long way first.

### Map

Follows the blog's `/travel` page (`_includes/map.html` in `rjbs.cloud`):
MapLibre GL, vendored; OpenFreeMap's "liberty" style; labels rewritten to
prefer English; attribution declared once.  Photo pages use a single-pin map
like `map.html`'s `only` mode.

The difference is scale.  DOM markers are fine for 62 places and hopeless for
11,000, so the photo map loads a GeoJSON file as a clustered source and draws
it with circle and symbol layers.  Clicking a single photo shows its
thumbnail and title.

### Private zones (geofences)

`jiggle.toml` lists private zones, each a center and a radius in meters:

```toml
[[private_zone]]
lat    = 40.0
lon    = -75.0
radius = 500
```

A photo taken inside one publishes no coordinates anywhere: not on its page,
not in the GeoJSON, not in the search index.  Its TOML keeps the true location.  Before
leaving Flickr, copy the zones from Flickr's settings, which the export
probably doesn't include (thread: *Location privacy zones*).

### Search

Pagefind, run over `site/` after the build.  It produces a static index split
into chunks, so a search downloads only what it needs.

### OpenGraph

Each photo page gets `og:title`, `og:description`, `og:image` (the `og.jpg`
rendition, as an absolute URL), and `og:url`.

## Syncing

`rclone sync --checksum site/ r2:<bucket>`.  With write-if-changed and
hardlinked derivatives, a typical sync uploads the new photos and the handful
of index pages that list them.

## Ingest

`jiggle ingest <dir>`: for each file, hash it, skip it if that hash is already
in the library, assign an ID, copy the original into place, write a stub TOML
with EXIF facts (date, offset, GPS, dimensions), and generate its derivatives.

Policies:

* the date comes from `DateTimeOriginal` plus `OffsetTimeOriginal` when
  present; a file with no date is ingested anyway and flagged by `fsck`
* file type is detected from content, not extension
* Live Photos (JPG + MOV with the same basename): a policy is needed before
  ingest handles MOVs at all

Later, a local editor: a Mojolicious app on localhost, reading and writing the
same TOML files, for the part Finder is bad at, such as selecting a dozen
photos, tagging them, and adding them to an album.

## Importing from Flickr

The source is the Net::Flickr::Backup archive:
`YYYY/MM/DD/YYYYMMDD-<flickrid>-<slug>.{jpg,mp4,xml}`, one RDF/XML sidecar
per photo.  It provides title, description, dates, visibility, license, tags,
album membership and album titles and descriptions, and the Flickr place
hierarchy.

The importer:

* groups files by Flickr ID, taken from the XML and cross-checked against the
  filename
* on duplicates left by renames, uses the newest XML and dedupes originals by
  hash
* detects file type from content (one photo has both `.png` and `.jpg`)
* maps `acl:accessor private` to `visibility = "private"`

The backup lacks coordinates for photos geotagged by hand, and album order
and cover.  Both are to be fixed in Net::Flickr::Backup, followed by a fresh
backup (threads: *Coordinates missing*, *Album order and cover*).

## Other tools

* `jiggle fsck` — metadata without an original, original without metadata,
  SHA-256 mismatches (bit rot or a bad copy), albums naming missing photos,
  photos with no date
* the blog plugin, `{% photo <id> %}` — deferred (thread: *Design the Jekyll
  plugin*).  The blog builds in GitHub Actions while the photo site builds on
  the laptop, so the plugin will likely read a manifest from the published
  site.

## Order of work

1. **Builder from the middle**, using a sample library made by a minimal
   ingest of `samples/pic-dump`: library layout, TOML loading, derivatives,
   page rendering, write-if-changed.  Doesn't depend on Flickr at all.
2. Map and OpenGraph.
3. Search.
4. `fsck`.
5. Sync to R2.
6. Flickr importer, once Net::Flickr::Backup is fixed and the ID scheme is
   chosen.
7. Video.
8. Local editor.
9. Blog plugin, and rewriting the blog's Flickr links.
