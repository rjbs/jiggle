# jiggle

jiggle keeps a library of photos and short videos, and publishes it as a
static web site.  It was written to replace Flickr.

It is one program, `jiggle-go`, with these parts:

- **`serve`** serves the library over JMAP, and serves an editor, which is
  a web page and a JMAP client like any other.  The library is a SQLite
  database and a directory of files.
- **`ingest`** adds photos, by uploading them to the server.
- **`build`** builds the site: a page for each photo, albums, tags, an
  archive by date, a map, search, and a feed.
- **`sync`** publishes the built site, to a Bunny storage zone or to a web
  server by rsync, sending only what changed.
- **`backup`** makes a safe copy of the database.

`jiggle-go COMMAND -h` lists a command's options.

    make install    # builds jiggle-go into ~/bin
    make test

## Documentation

- [doc/library.md](doc/library.md): what a library is on disk
- [doc/types.md](doc/types.md): the JMAP types and methods, Photo and Album
- [doc/ingest.md](doc/ingest.md): adding photos
- [doc/publishing.md](doc/publishing.md): building and syncing the site
- [doc/backup.md](doc/backup.md): backing up, and restoring

## What it needs

- **Go**, of the version `go.mod` names.  With `GOTOOLCHAIN=auto`, which
  is the default, an older `go` downloads it.
- **libvips** 8.15 or newer (`vips` and `vipsheader`), to make images.
- **ffmpeg** (and `ffprobe`), to encode video and take its poster.
- **exiftool**, to read what a file is and what it says of itself.
- **Node**, for `build` to make the search index with Pagefind, by `npx`.
  `build --no-search` does without.

On Debian 13:

    apt install libvips-tools libheif-plugin-libde265 ffmpeg libimage-exiftool-perl

Without `libheif-plugin-libde265`, vips says it supports HEIF and can't
decode HEIC, so photos from an iPhone fail while JPEGs work.

## History

jiggle was first a Perl program that kept a library as TOML files, one for
each photo, and built the site on a laptop.  The one library there was
moved to the database on 2026-11-23, and the Perl program and the tool
that moved the library were then deleted.  They are in this repository's
history, before the commit "Delete Perl jiggle".
