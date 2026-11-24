# jiggle: a static photo site to replace Flickr

This is the design: what was decided, and why.  It began as the working
plan for a Perl program that kept a library as TOML files, and what's here
is the part of that plan that's still true, now that jiggle is a JMAP
server in Go over a database (#20).  What a library is on disk is in
`doc/library.md`, and the JMAP types are in `doc/types.md`.  This document
names those and doesn't repeat them.

Major work is tracked as GitHub issues, and small open questions as Loose
Threads (`lt list`).

## Goals

Replace Flickr (about 12,000 photos and short videos) with a statically
generated site that is:

* built incrementally, so adding a day's photos doesn't mean reprocessing
  12,000 images
* synchronized incrementally to cheap hosting: a Bunny storage zone, or any
  web server by rsync
* backed by originals that are never modified, and a database of everything
  said about them

Features carried over from Flickr: albums, tags, title and description,
photos on a map, simple search, OpenGraph previews for photo pages, and short
video.  Things deliberately left behind: comments, faves, groups, and
friends/family visibility.

## Photos and albums

`doc/types.md` describes them.  Some of the reasoning:

**Facts about the original** (its name, size, dimensions) are recorded with
the photo.  The file is immutable, so that's safe, and it means page
generation never has to open an image.

**Visibility** is a string, not a boolean, which left room for other
levels, and two were added: `unlisted`, and `pending` (see *The editor*).

Descriptions (of photos and albums) are **Markdown**, rendered as CommonMark
with one change: a newline is a line break, as on Flickr, rather than being
joined into the paragraph.  Raw HTML is omitted and `javascript:` links are
neutered.  Flickr's HTML descriptions were converted to Markdown at import
(#2).

**Albums** have order, a cover, and their own title and description.  Tags
stay on the photos.

**IDs.**  A photo's id is twelve hex digits.  At 48 bits, the chance of any
collision is about 1 in 5 million at 11,000 photos, and still about 1 in
225,000 at 50,000.  8 hex digits (32 bits) would have been too few: about a
1.4% chance of a collision in the Flickr import alone.  Flickr's ids survive
only as `flickrId`.

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

* **rotated** according to EXIF orientation, and then by the photo's own
  `rotate`
* **converted to sRGB** from whatever profile the original has (iPhone photos
  are Display P3); without this, stripping metadata would silently leave
  P3 pixels to be read as sRGB, and the images would look dull
* **stripped** of all metadata, which removes EXIF GPS

**Freshness:** each of a photo's renditions records the original, the
recipe version and the rotation that produced it.  Changing a photo's
`rotate` makes its renditions again.  Making them again across the library
when a recipe's version changes isn't built yet (#30).

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

`jiggle build` renders the whole site every time.  HTML is cheap; 12,000
pages take seconds.  Two rules keep the sync small:

* **write-if-changed:** a file is written only if its bytes differ, so
  unchanged pages keep their mtime and aren't re-uploaded
* **derivatives are hardlinked** into the site rather than copied, so the
  site is exactly what gets published without doubling disk use

**Private and pending photos are treated as absent** from everything
published: photo pages, album and tag pages, counts, album covers, the map,
the search index, and the feed.  Their derivatives exist for the editor but
are never linked into the site.

**Unlisted photos have pages, but only albums link to them.**  They're in
no list: not the home page, the feed, tags, the archive, the map, or
search.  An album of nothing but unlisted photos is itself unlisted.

### Pages

* `/` — recent photos
* `/p/<id>/` — photo page (its renditions are under `/img/<id>/`): large
  image with `srcset`, title, description,
  date, tags, albums, small map, OpenGraph tags
* `/albums/` and `/albums/<slug>/`; `/albums/<slug>/<id>/` is a photo's page
  within the album, whose arrows go through the album in its order (it names
  `/p/<id>/` as canonical, and isn't in the search index); `/albums/<slug>/photos.json`
  is the album's list for the full-screen viewer, which the album page and
  its photo pages open (see jiggle-viewer.js)
* `/tags/` and `/tags/<tag>/`
* `/archive/` — every year, with a count and a sample of photos
* `/<year>/` — each month of the year, with a sample; `/<year>/<month>/` —
  every photo from that month, oldest first; `/archive/undated/`
* `/map/` — every located photo
* `/search/`
* `/feed.xml`

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

The library has private zones, each a center and a radius in meters (see
`doc/library.md`).

A photo taken inside one publishes no coordinates anywhere: not on its page,
not in the GeoJSON, not in the search index.  The database keeps the true
location.

Every published coordinate is also **rounded**, to the `location_precision`
setting's number of decimal places (3 by default, about 100 meters).  Zones
alone leave a ring: photos taken just outside a zone still publish exact
positions, and over the years they'd trace a circle around the hidden
center.  Rounding blurs that edge, and it means no published photo pins down
an exact spot.  The zone check uses the true location, so rounding can never
move a photo out of a zone.  Make zones generously large, too.

A photo can also be marked, by itself, as having a private location.

Published files never include location either: the site holds only HTML,
static assets, the GeoJSON, and renditions stripped of all metadata.
Originals are never published.  `Site.PublicLocation` is the only source
of published coordinates; anything new that publishes a location must go
through it or the same zone check.

### Search

Pagefind, run over the site after the build.  It produces a static index split
into chunks, so a search downloads only what it needs.

* Only photo pages are indexed (`data-pagefind-body`): title, description,
  date, albums, and tags.  Navigation and labels are excluded.  Results show
  the `h480` rendition.
* `/search/` uses Pagefind's stock UI, themed with the site's colors.  The
  header's search box submits to it as `?q=`.
* Pagefind is pinned (`PagefindVersion` in `internal/site/search.go`) and
  runs from `PATH` if installed, otherwise via `npx`.  `build --no-search`
  skips it.
* Its output is deterministic, with content-named chunks, so it goes into
  `pagefind/` through the writer, and an unchanged site rewrites no index
  files.
* **Privacy:** Pagefind indexes whatever HTML is in the site, so the build
  prunes stale pages (say, of a photo just made private) *before* indexing,
  keeping only the old index until the new one replaces it.  A test covers
  exactly this case.

### OpenGraph

Each photo page gets `og:title`, `og:description`, `og:image` (the `og.jpg`
rendition, as an absolute URL), and `og:url`.

## Syncing

`jiggle sync` publishes the site to one of two kinds of target, set in
the database (see `doc/publishing.md`):

* a web server, by `rsync`, which skips unchanged files by size and mtime
  (accurate, because builds leave unchanged files alone) and deletes last
* a Bunny storage zone behind a pull zone, served at a custom hostname by
  CNAME, through Bunny's storage API

Bunny serves a directory's `index.html` at `dir/` and `dir`, and, for a
missing path, `bunnycdn_errors/404.html`, where sync puts a copy of our 404
page; so the site needs no rewriting.  Nothing is compared
remotely: the build's site manifest is diffed against a record of what was
last uploaded, so a typical sync uploads the new
photos and the handful of index pages that list them.  Uploads go first,
deletions only once every upload has succeeded, and then the CDN's cache is
purged of every changed or deleted URL, since a photo made private must
leave the edge, not just storage.  Bunny rate-limits purges, so a top-level
directory with many changes is purged by prefix; renditions live under
`/img/`, apart from the pages, so that a change to every page purges none of
them.

The pull zone's caching settings matter, and live in Bunny's dashboard:
the edge keeps everything for a month (sync purges what changes), but
browsers are told `max-age=0`, so they check before reusing a page or
stylesheet (a quick 304, by ETag or Last-Modified), except for renditions
under `/img/`, which an edge rule lets browsers keep for a month.  So a
stylesheet change is one upload, not a change to every page.

## Ingest

`jiggle ingest <dir>` uploads each file to the server and imports it
(#28; see `doc/ingest.md`).  The server keeps the original, reads its facts
(date, offset, GPS, dimensions), and makes its derivatives.

Policies:

* the date comes from `DateTimeOriginal` plus `OffsetTimeOriginal` when
  present; a file with no date is ingested anyway
* file type is detected from content, not extension
* a file already in the library, by its digest, isn't ingested again
* Live Photos (a JPG plus a short MOV sharing a content identifier): there
  is still no policy.  The MOV half is recognized by its `ContentIdentifier`
  tag, and its import fails, rather than making it a video of its own
* a video's date comes from QuickTime's `CreationDate`, which has an offset;
  `CreateDate` is UTC, so it's used only as a fallback, marked with `Z`

Newly ingested photos are *pending* (below) until someone reviews them in the
editor.

## The editor

The editor is the replacement for Flickr's Uploadr (#10, #27): a web page,
served by `jiggle serve`, for the part Finder is bad at, like selecting a
dozen photos, tagging them, and adding them to an album.  It is a JMAP
client like any other, and everything it does, another client could.  The
workflow is: select a batch, view and edit it, and write the changes, as
often as you like.

**Pending.**  A photo whose visibility is `pending` has not been reviewed,
and the build never publishes it.  Ingest makes new photos pending.
Reviewing one is choosing its visibility, and until then
writing changes publishes nothing, so a savepoint halfway through captioning
200 photos is safe.

At first, pending was a key of its own, beside a visibility, so that a photo
could be marked "public, but not yet" and released later.  In practice,
photos were released as they were reviewed, the staging went unused, and two
fields were a tax on everything that decides what's published, so pending
became a visibility (2026-10-05).  If staging is ever wanted, it can come
back as a separate "hold".

**Selecting a batch** is by a small query language, typed into the page, or
given in its URL as `?q=`:

    pending limit:50
    private                 reviewing private photos
    album:berlin-2026
    tag:high-st year:2008

The query language is the editor's own.  It turns a query into a JMAP
filter, and the server knows nothing of it.

The batch belongs to the page: a list of ids rather than a live query, so
photos don't leave the contact sheet when they're released.  Refresh runs
the query again (once everything is written), which is how a long pending
queue is worked through fifty at a time.  With no query, the page lists
every album, including ones with nothing published; opening one is the query
`album:SLUG`, which edits the album itself, too: its title, description,
cover, and order.

The editor edits metadata and nothing else: ingest, build, and sync
stay on the command line.

**Viewing and editing.**  A contact sheet of the batch, from the photos'
renditions (which private and pending photos have too, so no build is
needed), with details and editing controls in a left sidebar.

* Click selects one photo; shift-click a range; ⌘-click toggles one; dragging
  a box selects many.
* With one photo selected, the sidebar edits it; with several, it edits all
  of them.
* Fields: title, description, tags, albums, visibility, `taken`
  (for photos with no EXIF date), location privacy, and rotate (previewed
  with CSS until the server has made the renditions again).
* Edited, unsaved fields are marked dirty.
* With several photos selected, a field whose values differ is shown as
  mixed.  For single values (title, description, visibility, ...), setting it
  makes them uniform, and the field says so.  **Sets are edited, not
  replaced:** tags and albums show the union with counts ("berlin 10/10",
  "local-conf 4/10"), and a tag can be added to or removed from all of them
  without touching the rest.  Rotate is relative, too: "turn these 90°
  clockwise."
* Albums: add the selection to an album, or to a new one.  In an album's
  own batch, photos can be moved within it, and one made its cover.
* Leaving the page with unsaved edits asks first.

**Writing changes.**  A "write changes" button at the top right, not in the
sidebar.  (⌘S does the same.)  It sends each photo's dirty fields, and
clears dirty state only once the server says they were written.

* The page hears of changes made elsewhere as they happen, by the server's
  event source, and shows them.
* A change made elsewhere to a field with an unwritten edit is a conflict,
  shown on the field before anything is written over.  Writing keeps the
  edit; reverting it takes the other change.  Edits to sets don't conflict:
  a tag added elsewhere is kept beside one added here.
* A photo whose rotation was written shows an "importing" badge until its
  renditions have been made again.

**Security.**  Any web page open in the browser can send requests to
localhost.  So the server refuses requests that come from another site's
pages, and requests whose `Host` isn't a name it knows itself by, which
stops a page that has rebound its own name to the server's address.
Nothing authenticates a request on the plain listener, which is why it
listens on localhost alone by default.

**The import helper** is `jiggle ingest DIR`, which ends by giving the
editor's URL for everything pending.  The usual source is a directory filled
by Image Capture from the phone and pruned by hand, so still photos (JPEG
today, since the phone is in compatibility mode; perhaps HEIC later) and
ordinary videos are the common case.  Live Photos are rare (the owner keeps
them off).  The Live Photo policy and the fallback date for photos with no
EXIF time come due here.

**Later, maybe:** location editing on a map, dragging photos to reorder an
album, and library-wide re-tagging.

## What came from Flickr

The library began as an import of Flickr's own data export (#2), done by
the Perl program and since deleted with it.  What it left in the database:

* each imported photo's `flickrId`, and when it was uploaded there
* `rotate`, for a photo turned by hand on Flickr: Flickr's rotations mostly
  repeated what EXIF orientation already calls for, so only the difference
  was kept
* visibility: anything but public on Flickr was imported as private
* Flickr's own records, byte for byte, in the `flickr_record` table, which
  hold what the importer had no use for, like comments.  Nothing reads them.

## Not built

* `fsck`: photos without an original, originals without a photo, SHA-256
  mismatches (bit rot or a bad copy), photos with no date
* history and undo (#23), and export (#29)
* removing blobs that nothing uses (#31), and uploads that can be resumed
  (#32)
* the blog plugin, `{% photo <id> %}`.
  The blog builds in GitHub Actions, so the plugin will likely read a
  manifest from the published site.
* making a new library, and changing its settings, other than with `sqlite3`
