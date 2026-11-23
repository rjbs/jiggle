# Working on jiggle

`STORAGE.md` is the contract a library keeps; `PLAN.md` is the broader plan.

## Where work is tracked

- **Major work is GitHub issues** on rjbs/jiggle.  `mvp` marked what was
  needed before Flickr could be canceled.  The current big plan is #20, a
  JMAP server in Go replacing the TOML metadata, with its parts as
  sub-issues (#21–#28).
- **Small deferred things are Loose Threads** (`lt`).  When a thread grows
  into major work, or an issue covers it, close the thread with a note
  pointing at the issue.
- **This repository is public.**  Keep private details out of issues,
  commits and this file: hostnames, NAS paths, the real private zones.

## Committing

- Commit and push to `main` (the remote is `github`) as work goes.  Never
  force-push or rewrite published history without asking.
- Run `prove -lr t` and check *its* exit status before committing.  Never
  pipe it into `tail` in an `&&` chain: the pipeline's status is `tail`'s,
  so failing tests would commit, and then publish.
- Tests that need vips, ffmpeg, exiftool or Pagefind skip without them, so
  a pass doesn't prove the tools work; read the skips.  The browser tests
  run only with `JIGGLE_PLAYWRIGHT` set; the one for the Go server's editor
  needs Go and node as well.

## Running on Debian 13

    apt install libvips-tools libheif-plugin-libde265 ffmpeg \
      libimage-exiftool-perl libcmark-dev libxml2-dev cpanminus build-essential

then `cpanm --installdeps .`.

- vips must be 8.15 or newer: renditions are written with `keep=none`.
  Debian 12's 8.14 fails on every image.
- Without `libheif-plugin-libde265`, vips lists HEIF support but can't
  decode HEIC, so iPhone photos fail while JPEGs work.  No test covers this
  yet; check by making a thumbnail of a real HEIC.
- Pagefind runs as `npx pagefind@VERSION` (pinned in `Jiggle::Search`),
  which needs Node.  A `pagefind` on `PATH` is used instead, whatever its
  version.

## The library

The real library isn't in this repository; ask where it is.  Only one
machine syncs it to Bunny: `.jiggle/bunny-uploaded.json` is the record of
what's in the storage zone, and despite `STORAGE.md` calling `.jiggle/`
disposable, losing it means the next sync uploads the whole site.

## The Go server (#20)

- It's meant to share a JMAP package with the user's other Go JMAP servers
  (#21).  Ask where they're checked out.
- Their `go.mod` files want a newer Go than Debian packages.  With
  `GOTOOLCHAIN=auto`, the default, `go` downloads the toolchain `go.mod`
  names; otherwise install Go from go.dev.
