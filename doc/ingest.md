# Adding photos

    jiggle ingest [--url URL] [--no-wait] [--discard-failed] FILE-OR-DIR...

`ingest` is a client of a running `jiggle serve`: it uploads files and
calls `Photo/import` (see [types.md](types.md)), as any other client could.
It needs nothing but the server's URL, so it can run on another machine.

The server is at `--url`, or at `JIGGLE_URL` in the environment, or at
`http://localhost:8739`, where `serve` listens if it's told nothing.

A directory means the files in it whose names don't begin with a dot.
Directories inside it are left alone.  The files are never changed or
removed.

## What happens to a file

Each file comes to one of these:

- **ingested:** a photo was made of it, `pending`, with the file's name
  and modification time.
- **already in the library:** some photo has this exact file as its
  original, or is importing it.
- **skipped:** it isn't a photo or a video of a kind Jiggle can use.  The
  server judges that by the file's content, not its name.
- **failed to import:** a photo was made, and then the server couldn't
  read the file: it's damaged, say, or it's the motion half of a Live
  Photo.  The photo is left, with the reason in its `importJob`, and with
  no original and no renditions, so it's in no album and isn't published.
  With `--discard-failed`, such photos are destroyed instead.
- **not sent:** the file couldn't be read or uploaded.  The rest are still
  ingested, and `ingest` exits with an error at the end.

## What's sent

A file's content is sent only if the server doesn't have it.  An upload's
blob id is `O` and the SHA-256 of its bytes, so `ingest` hashes the file
and tries the import first; only when the server answers `blobNotFound`
does it upload.  Ingesting a whole card again, or again after an
interruption, sends only what's new.

An upload is one POST, and can't be resumed (#32).  A large video interrupted
partway starts over.

## Waiting

The server makes a photo's renditions after `Photo/import` answers, which
takes seconds for a photo and can take minutes for a video.  `ingest`
waits for all of it, reporting as it goes, and then says which imports
failed.  `--no-wait` skips that.  Interrupting it stops the waiting, not
the server's work.

It ends with the URL of the editor showing everything pending.
