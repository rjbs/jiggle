# The JMAP types

`jiggle-go serve` serves a library's database over JMAP ([RFC 8620][rfc]),
by way of [Gizmo](https://github.com/rjbs/gizmo).  There is one account,
`photos`, and one capability, `https://semiotic.systems/jmap/jiggle`, with
two types under it: Photo and Album.  Each has `/get`, `/changes`, `/set`
and `/query`, which behave as the RFC says, except where this says
otherwise.

[rfc]: https://www.rfc-editor.org/rfc/rfc8620

## Photo

A photo or a video.

| Property | Type | |
|---|---|---|
| `id` | Id | Set by the server.  The ids of old photos begin with digits, and some are nothing but digits. |
| `type` | String | `photo` or `video`.  Set by the server. |
| `title` | String | May be empty. |
| `description` | String | Markdown.  May be empty. |
| `taken` | String or null | The time on the clock when the photo was taken, like `2026-07-17T17:23:17`, with a UTC offset (or `Z`) only if one is known. |
| `visibility` | String | `public`, `unlisted`, `private` or `pending`. |
| `rotate` | Number | A further turn clockwise, in degrees: 0, 90, 180 or 270. |
| `tags` | String[Boolean] | A set: each tag the photo has, with the value true.  So `tags/cat` can be patched to true, to add a tag, or to null, to remove one.  The server keeps tags in lowercase.  They have no order, and the site shows them in order of name. |
| `location` | Object or null | `lat`, `lon`, and `isPrivate`, which is false unless given. |
| `added` | String or null | When the photo was added to the library.  Set by the server. |
| `flickrId`, `flickrUploaded` | String or null | For a photo that came from Flickr.  Set by the server. |
| `blobId` | Id or null | The original.  Set by the server. |
| `original` | Object or null | Facts about the original: `name` and `modified`, of the file it came from; `size`, in bytes; `width` and `height`, turned upright by its own EXIF orientation, before `rotate`; and `duration`, in seconds, for a video.  Set by the server. |
| `renditions` | Object | By name, like `1024.webp`: each rendition's `blobId`, its `width` and `height` in pixels, and its `size` in bytes.  Set by the server. |

A property the server sets can be given in an update only as it already
is.

**`Photo/set`** updates and destroys.  It doesn't create: every create is
`forbidden`, because a photo is made from a file, by an import.
Destroying a photo takes it out of every album it's in; an album whose
cover it was gets its first remaining photo as its cover, or none.  The
original is kept, and can still be downloaded.

**`Photo/query`** takes a FilterCondition with these properties, all of
which a photo must meet, and FilterOperators (`AND`, `OR`, `NOT`) over
them.

| Property | Type | |
|---|---|---|
| `visibility` | String | The photo's visibility. |
| `inAlbum` | Id | An album the photo is in. |
| `tag` | String | A tag the photo has, compared as the site compares tags, by slug: `High St` finds photos tagged `high-st`. |
| `takenBefore` | String | The photo was taken before this time. |
| `takenAfter` | String | The photo was taken at this time or after it. |

Times are compared as text, and may be cut short: a photo taken in 2026
is taken after `2026` and before `2027`.  A photo with no date is taken
neither before nor after anything.

It sorts by `taken`, `added` or `id`, and by `position`, a photo's place
in the album that the comparator names with a property of its own,
`inAlbum`:

    { "property": "position", "inAlbum": "ALBUM-ID" }

Photos with no value for what they're sorted by, like those not in that
album, come last, either way up.  Times are compared as text here too, so
two with different UTC offsets are not in the order they happened in.
With no sort, photos come in the order they were taken.

A query written for a person, like `album:trip public`, is the client's to
turn into a filter.

## Album

An ordered list of photos.

| Property | Type | |
|---|---|---|
| `id` | Id | Set by the server. |
| `slug` | String | The album's name in its URL and in queries: lowercase letters and digits, with single hyphens between.  No two albums have the same slug; a second is `alreadyExists`. |
| `title` | String | Not empty. |
| `description` | String | Markdown.  Empty unless given. |
| `coverPhotoId` | Id or null | The photo that stands for the album.  The site uses the first photo if this is null. |
| `photoIds` | Id[] | The album's photos, in order, each once.  Empty unless given. |
| `created` | String or null | When the album was made.  Set by the server. |
| `flickrId` | String or null | For an album that came from Flickr.  Set by the server. |

Destroying an album does nothing to its photos.

**`Album/query`** takes a FilterCondition with two properties, `slug` and
`photoId`, the id of a photo the album must hold, and FilterOperators over
them.  It sorts by `slug` (which is the default), `title`, `created` or
`id`.

## Blobs

A blobId is a letter and the SHA-256 of the blob's bytes: `O` for an
original, `R` for a rendition.  Both can be downloaded.  Nothing can be
uploaded yet.
