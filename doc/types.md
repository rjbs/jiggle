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
| `tags` | String[] | In the order given.  The server keeps them in lowercase, each once. |
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

**`Photo/query`** takes a filter with one property, `query`: a jiggle
query, which is terms with spaces between, all of which a photo must
match.

    pending         not yet reviewed (and so not published)
    private         private
    public          public: published and listed
    unlisted        unlisted: published, but listed only in albums
    all             every photo
    album:SLUG      in the album with that slug
    tag:TAG         tagged TAG, compared as the site compares tags (by slug)
    year:YYYY       taken in that year
    id:ID           the photo with that id; given more than once, any of them
    limit:N         only the first N of the photos that match

An unknown term is `unsupportedFilter`, and an album that doesn't exist is
`invalidArguments`.  No filter, or an empty query, matches every photo.

It sorts by `taken`, `added` or `id`.  Times are compared as text, so two
with different UTC offsets are not in the order they happened in.  Photos
with no value for a property sort last, either way up.  With no sort,
photos come in album order when the query names an album (the first
named, if several), and otherwise in the order they were taken, with
undated photos last.

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

**`Album/query`** takes a filter with two properties, both optional:
`slug`, and `photoId`, the id of a photo the album must hold.  It sorts by
`slug` (which is the default), `title`, `created` or `id`.

## Blobs

A blobId is a letter and the SHA-256 of the blob's bytes: `O` for an
original, `R` for a rendition.  Both can be downloaded.  Nothing can be
uploaded yet.
