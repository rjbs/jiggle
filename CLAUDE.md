# Working on jiggle

`README.md` says what jiggle is, and `doc/` describes it: start with
`doc/library.md` and `doc/types.md`.

jiggle is one Go program, `jiggle-go`.  It was a Perl program first, which
kept a library as TOML files; that was deleted on 2026-11-23, and is in
the history before the commit "Delete Perl jiggle".  Comments that say
something is done "as Perl jiggle did" explain why the site's bytes are
what they are: a page that comes out differently is uploaded again.

## Where work is tracked

- **Major work is GitHub issues** on rjbs/jiggle.  #20 was the move from
  the Perl program to a JMAP server in Go, with its parts as sub-issues
  (#21–#29).
- **Small deferred things are Loose Threads** (`lt`).  When a thread grows
  into major work, or an issue covers it, close the thread with a note
  pointing at the issue.
- **This repository is public.**  Keep private details out of issues,
  commits and this file: hostnames, NAS paths, the real private zones.

## Committing

- Commit and push to `main` as work goes.  Never force-push or rewrite
  published history without asking.
- Run `make test` and check *its* exit status before committing.  Never
  pipe it into `tail` in an `&&` chain: the pipeline's status is `tail`'s,
  so failing tests would commit, and then publish.
- Tests that need vips, ffmpeg, exiftool or node skip without them, so a
  pass doesn't prove the tools work; run `go test -v` and read the skips.
  The test that drives the editor in a browser runs only with
  `JIGGLE_PLAYWRIGHT` set to a directory where Playwright is installed.
- The editor is JavaScript, in `share/editor`, with no build step.  Its
  tests are run by `go test`, from `internal/server/testdata`.

## The schema

`internal/store/store.go` has the schema as a list of migrations.  There
is a real database now, so **never edit a migration that exists; append
one.**

## What it needs

`README.md` lists the programs.  Two things that have gone wrong:

- vips must be 8.15 or newer: renditions are written with `keep=none`.
  Debian 12's 8.14 fails on every image.
- Without `libheif-plugin-libde265`, vips lists HEIF support but can't
  decode HEIC, so iPhone photos fail while JPEGs work.  No test covers this
  yet; check by making a thumbnail of a real HEIC.

Pagefind runs as `npx pagefind@VERSION` (pinned in
`internal/site/search.go`), which needs Node.  A `pagefind` on `PATH` is
used instead, whatever its version.

## The real library

It isn't in this repository; ask where it is.

- **Treat it as read-only** unless asked to change it.  To try something,
  make a library in a temporary directory, as the tests do.
- **Never stop a server by name**, as with `pkill jiggle-go`: the real one
  may be running.  Stop the one you started, by its process id.
- Only one machine syncs the site to Bunny, and the record of what it
  uploaded can't be made again; see `doc/publishing.md`.

## Gizmo

The JMAP machinery is `github.com/rjbs/gizmo`, shared with the user's
other Go JMAP servers.  Ask where they're checked out.
