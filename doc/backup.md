# Backing up a library

A library served by `jiggle-go serve` is two things that can't be made
again: the database, and the originals.

## The database

    jiggle-go backup --db jiggle.sqlite --to DIR [--keep N]

writes `DIR/jiggle-YYYYMMDDTHHMMSSZ.sqlite`, named for the time in UTC, and
prints its path.  It's safe while the server is running, and the backup is
the database as it was at one moment.  Copying the live file is not safe: a
database in use is the file, its write-ahead log beside it, and whatever is
half written to either.

A backup is one file, a whole SQLite database, and it has been checked for
corruption before it gets its name.  A backup that fails leaves nothing
behind but, at worst, a file whose name ends in `.partial`.

With `--keep N`, all but the newest N backups in the directory are then
removed.  Only files named as backups are counted or removed.

There is no history of changes and no undo (#23), so the backups are the
only way back from a mistake, to the time of the last one.  Run it from
cron, as often as that should be, and keep enough of them to notice a
mistake in time.

To restore, stop the server, put the backup where the database was, and
remove the old database's `-wal` and `-shm` files if they are there.

## The originals

Everything under `originals/` in the library.  The files never change once
they're there, so any tool that copies new files will do.  A file under
`originals/incoming/` is an upload still arriving, and needn't be kept.

Back the database up first.  A backup of the database that knows of an
original the other backup hasn't got is a photo that can't be restored; the
other way around is only an unused file.

## What needn't be backed up

Renditions can be made again from the originals, and the site can be built
again from the database and the renditions.  Both take long enough that
copies are worth having, but nothing is lost with them.
