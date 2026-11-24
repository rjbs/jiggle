# Publishing the site

    jiggle-go build --db jiggle.sqlite --library DIR --out SITE
    jiggle-go sync  --db jiggle.sqlite --site SITE [--to rsync|bunny] [--dry-run] [--purge-all]

`sync` copies a built site to where it's served from.  Nothing syncs by
itself: a build is the last look at the site before it's public.

It refuses to run after a build that didn't finish, whose output may be
half updated.  Build again first.

## Where to

The database's `setting` table says:

| Setting | |
|---|---|
| `publish.rsync.target` | Where rsync copies the site, like `user@example.com:/var/www/photos/`. |
| `publish.rsync.command` | Which rsync to run.  `rsync` unless set. |
| `publish.bunny.zone` | The Bunny storage zone. |
| `publish.bunny.storage_url` | The storage API of the zone's region. |
| `publish.bunny.pull_zone_id` | The pull zone that serves the storage zone. |
| `publish.bunny.purge_hosts` | The hostnames to purge the cache on, separated by spaces.  The pull zone's own unless set. |

With both an rsync target and a Bunny zone, `--to` chooses.

Bunny needs two secrets: the storage zone's password, and the account's
API key, for purging the CDN's cache.  They're read from the environment,
as `BUNNY_STORAGE_PASSWORD` and `BUNNY_API_KEY`, and are never in the
database.  A dry run needs neither.

## rsync

Files gone from the site are removed from the target, which is how a photo
made private leaves the published site.  Unchanged files are skipped by
size and modification time.  With rsync 3 or newer, deletions wait until
the new files are in place.

## Bunny

What to upload isn't found by looking in the storage zone.  The build
leaves a manifest of what each path in the site holds, and the sync keeps a
record of what each path held when it was last uploaded: `--manifest` and
`--record`, by default `SITE.manifest.json` and a `bunny-uploaded.json`
beside it.  A path the two disagree about is uploaded, and a path in the
record and not the manifest is deleted.

**The record is the only account there is of what's in the storage zone.**
Without it, a sync uploads the whole site again, and never deletes what
earlier syncs put there.  Only one machine should sync a site.

- Pages are uploaded after everything they refer to, and nothing is
  deleted until every upload has worked.
- Then the CDN's cache is purged of everything changed or deleted, by
  prefix for a directory with many changes.  `--purge-all` purges the
  whole pull zone instead.
- A sync that fails part way, or is interrupted, has recorded and purged
  what it did.  Sync again to finish.
- A site with no manifest is refused.  It isn't taken for an empty site,
  which would mean deleting everything.

This is the same record, in the same format, that Perl jiggle's `jiggle
sync` keeps in `.jiggle/bunny-uploaded.json`, and the Go build writes the
manifest that Perl's does.  So a site built by `jiggle-go build` into the
library's `site/` can be synced against Perl's record:

    jiggle-go sync --db jiggle.sqlite --site LIBRARY/site \
      --manifest LIBRARY/.jiggle/site-manifest.json \
      --record LIBRARY/.jiggle/bunny-uploaded.json --dry-run

The first such sync uploads a few pictures that it needn't: where two
renditions are the same bytes, the Go build links both from one file, and
the manifest says so.
