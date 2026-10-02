# jiggle: a static photo site to replace Flickr

This is the working plan.  It records what we've decided, what's still open,
and the order we expect to build things in.  Major work is tracked as GitHub
issues, and small open questions as Loose Threads (`lt list`); this document
names them but doesn't duplicate their detail.

The storage layer (the library's layout and schema, and what can change
later) is specified in `STORAGE.md`, which supersedes the library sections
here where they differ.

## Goals

Replace Flickr (about 11,000 photos and short videos, about 30 GB) with a
statically generated site that is:

* built on the laptop, incrementally, so adding a day's photos doesn't mean
  reprocessing 11,000 images
* synchronized incrementally to cheap hosting: a Bunny storage zone, or any
  web server by rsync
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
source_mtime = 2026-07-17T11:23:17-04:00   # of the file ingest copied from

[location]
lat         = 48.1214722
lon         = 16.5606139
```

The `[original]` table records facts about an immutable file, so storing them
here is safe, and it means page generation never has to open an image.

An absent `visibility` means public.  A string rather than a boolean leaves
room for other levels later, if ever needed.

Descriptions (of photos and albums) are **Markdown**, rendered as CommonMark
with one change: a newline is a line break, as on Flickr, rather than being
joined into the paragraph.  CommonMark's safe mode omits raw HTML and
neuters `javascript:` links.  Flickr's HTML descriptions are converted to
Markdown at import (#2).

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

**Video** (#3): the original is kept.  A poster frame (`poster.png`, taken
about a second in, and never published) is extracted with ffmpeg, and the
usual image renditions are made from it.  The published `video.mp4` is
re-encoded with ffmpeg:

* H.264 (CRF 23) and stereo AAC, fit within 1920×1920
* rotated upright; phones store portrait clips as landscape plus a flag
* only the first video and audio streams; iPhones add spatial audio and
  timed metadata tracks that browsers can't use
* no metadata at all, which removes GPS
* `+faststart`, so playback starts before the download finishes

Re-encoding rather than remuxing is worth it: iPhone "more compatible" clips
are H.264 already, but at about 15 Mbit/s; the web versions come out 3–5
times smaller.

Video pages use `<video>` with the 2048 rendition as its poster, plus
`og:video` tags; grid tiles for videos get a play badge.

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
* `/p/<id>/` — photo page (its renditions are under `/img/<id>/`): large
  image with `srcset`, title, description,
  date, tags, albums, small map, OpenGraph tags
* `/albums/` and `/albums/<slug>/`
* `/tags/` and `/tags/<tag>/`
* `/archive/` — every year, with a count and a sample of photos
* `/<year>/` — each month of the year, with a sample; `/<year>/<month>/` —
  every photo from that month, oldest first; `/archive/undated/`
* `/map/` — every located photo
* `/search/`

The archive (#4) is how every photo stays reachable without a paged
photostream.  A paged stream would work badly with write-if-changed: one new
photo shifts every page, so every page would be re-uploaded.  With archives,
a new photo changes only its own month, its year, and the indexes.  Photos
are filed by the date on the clock where they were taken.  (Dynamic loading
from a JSON index per month is a possible later improvement.)

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
not in the GeoJSON, not in the search index.  Its TOML keeps the true
location.  Before leaving Flickr, copy the zones from Flickr's settings,
which the export probably doesn't include (#6).

Every published coordinate is also **rounded**, to `location_precision`
decimal places in `jiggle.toml` (3 by default, about 100 meters).  Zones alone
leave a ring: photos taken just outside a zone still publish exact
positions, and over the years they'd trace a circle around the hidden
center.  Rounding blurs that edge, and it means no published photo pins down
an exact spot.  The zone check uses the true location, so rounding can never
move a photo out of a zone.  Make zones generously large, too.

Published files never include location either: `site/` holds only HTML,
static assets, the GeoJSON, and renditions stripped of all metadata.
Originals are never published.  `Site::public_location` is the only source
of published coordinates; anything new that publishes a location (search,
the blog manifest, place names from Flickr) must go through it or the same
zone check.

### Search

Pagefind, run over `site/` after the build.  It produces a static index split
into chunks, so a search downloads only what it needs.

* Only photo pages are indexed (`data-pagefind-body`): title, description,
  date, albums, and tags.  Navigation and labels are excluded.  Results show
  the `h480` rendition.
* `/search/` uses Pagefind's stock UI, themed with the site's colors.  The
  header's search box submits to it as `?q=`.
* Pagefind is pinned (`Jiggle::Search::$PAGEFIND_VERSION`) and runs from
  `PATH` if installed, otherwise via `npx`.  `jiggle build --no-search` skips
  it.
* Its output is deterministic, with content-named chunks, so it goes into
  `site/pagefind/` through the writer, and an unchanged site rewrites no index
  files.
* **Privacy:** Pagefind indexes whatever HTML is in `site/`, so the build
  prunes stale pages (say, of a photo just made private) *before* indexing,
  keeping only the old index until the new one replaces it.  A test covers
  exactly this case.

### OpenGraph

Each photo page gets `og:title`, `og:description`, `og:image` (the `og.jpg`
rendition, as an absolute URL), and `og:url`.

## Syncing

`jiggle sync` publishes `site/` to one of two kinds of target, set in
`jiggle.toml` (see `jiggle help sync`):

* a web server, by `rsync`, which skips unchanged files by size and mtime
  (accurate, because builds leave unchanged files alone) and deletes last
* a Bunny storage zone behind a pull zone, served at a custom hostname by
  CNAME, through Bunny's storage API

Bunny serves a directory's `index.html` at `dir/` and `dir`, and, for a
missing path, `bunnycdn_errors/404.html`, where sync puts a copy of our 404
page; so the site needs no rewriting.  Nothing is compared
remotely: the build's site manifest is diffed against a record (in
`.jiggle/`) of what was last uploaded, so a typical sync uploads the new
photos and the handful of index pages that list them.  Uploads go first,
deletions only once every upload has succeeded, and then the CDN's cache is
purged of every changed or deleted URL, since rendition URLs survive
re-derivation and a photo made private must leave the edge, not just
storage.  Past a few hundred changed files, the whole pull zone is purged
instead.

## Ingest

`jiggle ingest <dir>`: for each file, hash it, skip it if that hash is already
in the library, assign an ID, copy the original into place, write a stub TOML
with EXIF facts (date, offset, GPS, dimensions), and generate its derivatives.

Policies:

* the date comes from `DateTimeOriginal` plus `OffsetTimeOriginal` when
  present; a file with no date is ingested anyway and flagged by `fsck`
* file type is detected from content, not extension
* Live Photos (a JPG plus a short MOV sharing a content identifier): until
  there's a policy, ingest skips the MOV half, recognizing it by its
  `ContentIdentifier` tag, rather than making it a video of its own
* a video's date comes from QuickTime's `CreationDate`, which has an offset;
  `CreateDate` is UTC, so it's used only as a fallback, marked with `Z`

Newly ingested photos are *pending* (below) until someone reviews them in the
editor.

## The editor

`jiggle edit QUERY` is the replacement for Flickr's Uploadr (#10): a
Mojolicious app on localhost that reads and writes the same `meta/` TOML
files, for the part Finder is bad at, like selecting a dozen photos, tagging
them, and adding them to an album.  The workflow is: select a batch, view and
edit it, and write the changes, as often as you like.

**Pending.**  A photo with `pending = true` has not been reviewed, and the
build never publishes it, whatever its visibility.  Ingest sets it, so new
photos can default to public safely.  In the editor it's an ordinary field:
when a batch is ready, select what's done and clear it.  So writing changes
and releasing photos are separate acts: a savepoint halfway through
captioning 200 photos publishes nothing, and a batch can be released in
parts.  Pending is its own key, not a visibility, because visibility is one
of the things decided during review.

**Selecting a batch** is on the command line, with a small query language:

    jiggle edit pending
    jiggle edit private                 # reviewing private photos
    jiggle edit album:berlin-2026
    jiggle edit tag:high-st year:2008

The batch is fixed when the session starts, a list of ids rather than a live
query, so photos don't leave the contact sheet when they're released.

**Viewing and editing.**  A contact sheet of the batch, from renditions in
`derived/` (derive already covers private and pending photos, so no build is
needed), with details and editing controls in a left sidebar.

* Click selects one photo; shift-click a range; ⌘-click toggles one; dragging
  a box selects many.
* With one photo selected, the sidebar edits it; with several, it edits all
  of them.
* Fields: title, description, tags, albums, visibility, pending, `taken`
  (for photos with no EXIF date), location privacy, and rotate (previewed
  with CSS; renditions are remade at the next build).
* Edited, unsaved fields are marked dirty.
* With several photos selected, a field whose values differ is shown as
  mixed.  For single values (title, description, visibility, ...), setting it
  makes them uniform, and the field says so.  **Sets are edited, not
  replaced:** tags and albums show the union with counts ("berlin 10/10",
  "local-conf 4/10"), and a tag can be added to or removed from all of them
  without touching the rest.  Rotate is relative, too: "turn these 90°
  clockwise."
* Albums: add the selection to an album, or to a new one.  Membership lives
  in the album's file, so a write can change `meta/albums/` too.  Reordering
  and choosing a cover come later.
* Leaving the page with unsaved edits asks first.

**Writing changes.**  A "write changes" button at the top right, not in the
sidebar.  (⌘S does the same.)  It freezes the UI, sends each photo's dirty fields, and the server
rewrites the files and makes a git commit in `meta/`.  The client clears
dirty state only once the server says the commit happened.

* The client sends each file's hash as it was loaded.  A file changed on disk
  since (by hand, or a `git pull`) is refused, not overwritten.
* The commit names only the paths the editor wrote (`git commit -- PATHS`),
  so unrelated uncommitted work in `meta/` isn't swept in.  Its message is
  generated ("edit 14 photos: title, tags, pending"), with an optional note.
* A photo whose rotation was written has its renditions remade before the
  page unfreezes, so the sheet shows it turned.
* Album changes are sent as additions and removals, and merged with the
  album files as they are when writing.

**Security.**  Any web page open in the browser can send requests to
localhost, so the server listens on 127.0.0.1 only and requires a random
token, which `jiggle edit` puts in the URL it prints.

**The import helper** is `jiggle ingest --edit DIR`: ingest (marking photos
pending, and deriving), then the editor on everything pending.  The usual source is a directory filled by Image Capture from
the phone and pruned by hand, so still photos (JPEG today, since the phone is in compatibility
mode; perhaps HEIC later) and ordinary videos are the common case.  Live
Photos are rare (the owner keeps them off), so skipping their MOV halves stays fine.  The Live Photo policy and the fallback date for
photos with no EXIF time come due here.

**Order of work**, each step usable on its own:  (All six are built, on the
`jiggle-editor` branch.)

1. `pending` in the model, and the build skipping pending photos.
2. The batch query, and `jiggle edit` serving a read-only contact sheet.
3. Editing one photo, and writing and committing.
4. Multiple selection: mixed values, and editing sets.
5. Albums.
6. The import helper.

**Later, maybe:** location editing on a map, reordering albums and choosing
covers, library-wide re-tagging, and picking a batch in the browser.

## Importing from Flickr

`jiggle import-flickr DIR` takes either of two sources and tells them apart
by their layout.  Both go through ingest, so ids, originals, and file facts
are exactly as for any other photo, and importing twice adds nothing.

**The primary source is Flickr's own data export** (#2), unpacked with one
directory per zip under `metadata/` and `photos/`.  It has one clean format,
album order and covers, coordinates for most photos, three privacy levels
(anything but public imports as private), and each photo's rotation on
Flickr.  Its upload times are on a US Pacific clock and are converted to
instants.  Its rotations mostly repeat what EXIF orientation already calls
for, so only the difference is kept, as `rotate`: a photo turned by hand on
Flickr.  It lacks who may see each photo's location.

**The Net::Flickr::Backup archive** is the other source, and a cross-check:
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
5. Sync to the web host.
6. Flickr importer, once Net::Flickr::Backup is fixed and the ID scheme is
   chosen.
7. Video.
8. Local editor (see *The editor*).
9. Blog plugin, and rewriting the blog's Flickr links.
