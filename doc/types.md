# The JMAP types

`jiggle serve` serves a library's database over JMAP ([RFC 8620][rfc]),
by way of [Gizmo](https://github.com/rjbs/gizmo).  There is one account,
`photos`, and one capability, `https://semiotic.systems/jmap/jiggle`, with
two object types under it: Photo and Album.  Each has the standard `/get`,
`/changes`, `/query` and `/set` methods, with the differences from the
standard noted under each type.  Photo also has `/import`.  Neither has
`/queryChanges`.

Type names follow RFC 8620: `Id`, `String`, `Boolean`, `Number`,
`UnsignedInt`, `A|null`, `A[]`, `A[B]`.  A property that is "set by the
server" can be given in an update only as it already is.

[rfc]: https://www.rfc-editor.org/rfc/rfc8620

## Photo

A **Photo** is a photo or a video.  It has the following properties:

- **id**: `Id`
  The id of the photo.  This is set by the server.  The ids of old photos
  begin with digits, and some are nothing but digits.
- **type**: `String`
  `photo` or `video`.  This is set by the server.
- **title**: `String`
  The title of the photo.  May be empty.
- **description**: `String`
  Markdown.  May be empty.
- **taken**: `String|null`
  The time on the clock when the photo was taken, like
  `2026-07-17T17:23:17`, with a UTC offset (or `Z`) only if one is known.
- **visibility**: `String`
  `public`, `unlisted`, `private` or `pending`.
- **rotate**: `Number`
  A further turn clockwise, in degrees: 0, 90, 180 or 270.
- **tags**: `String[Boolean]`
  A set: each tag the photo has, with the value true.  So `tags/cat` can
  be patched to true, to add a tag, or to null, to remove one.  The server
  keeps tags in lowercase.  They have no order, and the site shows them in
  order of name.
- **location**: `Location|null`
  Where the photo was taken.
- **added**: `String|null`
  When the photo was added to the library.  This is set by the server.
- **flickrId**: `String|null`
  For a photo that came from Flickr.  This is set by the server.
- **flickrUploaded**: `String|null`
  For a photo that came from Flickr.  This is set by the server.
- **blobId**: `Id|null`
  The original.  Null for a photo whose first import isn't done.  This is
  set by the server.
- **importJob**: `ImportJob|null`
  An import of the photo; see `Photo/import`.
- **original**: `Original|null`
  Facts about the original.  This is set by the server.
- **renditions**: `String[Rendition]`
  The photo's renditions, by name, like `1024.webp`.  This is set by the
  server.

A **Location** object has the following properties:

- **lat**: `Number`
  The latitude.
- **lon**: `Number`
  The longitude.
- **isPrivate**: `Boolean` (default: false)
  Whether the location is private.

An **ImportJob** object has the following properties:

- **blobId**: `Id`
  The blob being imported.
- **name**: `String|null`
  The file's name, where it came from.
- **modified**: `String|null`
  The file's modification time there, with a UTC offset.
- **error**: `String|null`
  Why the import failed, or null if it hasn't.

An **Original** object has the following properties:

- **name**: `String|null`
  The name of the file the original came from.
- **modified**: `String|null`
  The modification time of that file.
- **size**: `UnsignedInt|null`
  The original's size in bytes.
- **width**: `UnsignedInt|null`
  The original's width in pixels, turned upright by its own EXIF
  orientation, before `rotate`.
- **height**: `UnsignedInt|null`
  The original's height in pixels, turned the same way.
- **duration**: `Number|null`
  The length of a video, in seconds.

A **Rendition** object has the following properties:

- **blobId**: `Id`
  The id of the rendition's blob.
- **width**: `UnsignedInt`
  The rendition's width in pixels.
- **height**: `UnsignedInt`
  The rendition's height in pixels.
- **size**: `UnsignedInt`
  The rendition's size in bytes.

### Photo/get

Standard JMAP `/get` method.

### Photo/changes

Standard JMAP `/changes` method.

### Photo/query

Standard JMAP `/query` method, with these additions.

A **FilterCondition** object has the following properties, any of which
may be given; all must match.  Conditions combine with the standard
`AND`, `OR` and `NOT` FilterOperators.

- **visibility**: `String`
  The photo's visibility equals the value.
- **inAlbum**: `Id`
  The photo is in the Album with this id.
- **tag**: `String`
  The photo has this tag, compared as the site compares tags, by slug:
  `High St` finds photos tagged `high-st`.
- **takenBefore**: `String`
  The photo was taken before this time.
- **takenAfter**: `String`
  The photo was taken at this time or after it.
- **hasBlobId**: `Boolean`
  If true, the photo has an original, and so renditions; if false, it has
  none.
- **isImporting**: `Boolean`
  If true, an import of the photo is under way: it has an `importJob`
  with no error.  If false, none is.
- **hasImportError**: `Boolean`
  If true, the photo's last import failed; if false, it did not.

Times are compared as text, and may be cut short: a photo taken in 2026
is taken after `2026` and before `2027`.  A photo with no date is taken
neither before nor after anything.

The following properties are supported for sorting:

- `taken`
- `added`
- `id`
- `position`: by the photo's place in an album, named by an **inAlbum**:
  `Id` property that the Comparator object carries:

        { "property": "position", "inAlbum": "ALBUM-ID" }

Photos with no value for what they're sorted by, like those not in that
album, come last, either way up.  Times are compared as text here too, so
two with different UTC offsets are not in the order they happened in.
With no sort, photos come in the order they were taken.

A query written for a person, like `album:trip public`, is the client's to
turn into a filter.

### Photo/set

Standard JMAP `/set` method, except that it doesn't create.  A photo is
made from a file, by `Photo/import`; a create is rejected with a
`forbidden` SetError.

Destroying a photo takes it out of every album it's in; an album whose
cover it was gets its first remaining photo as its cover, or none.  The
original is kept, and can still be downloaded.

**Setting `importJob`** on a photo starts an import: `{ "blobId": … }`,
with `name` and `modified` if they're known, and no `error`.  The blob may
be:

- a new upload, to replace the photo's original and keep everything else
  (a photo can't become a video, or a video a photo);
- the blob of an import that failed, to try it again;
- the photo's own `blobId`, to read the file again and make its
  renditions again.

Until the new import succeeds, the photo keeps the original and renditions
it had, and it keeps them if the import fails.  Setting `importJob` while
an import is under way replaces that import.  Setting it to null gives an
import up, or dismisses its error, which only a photo with a `blobId` can
do; a photo with neither is destroyed instead.

**Changing `rotate`** starts an import of the photo's own original, since
every rendition has to be made again.  The response to the `/set` says so.

A replaced original is kept, and can still be downloaded.

### Photo/import

A photo is made by uploading a file and then calling `Photo/import`.  The
method takes `accountId` and `ifInState`, as a `/set` does, and one
argument of its own:

- **photos**: `Id[PhotoImport]`
  A map of creation id to what to make.

A **PhotoImport** object has the following properties, and may have any
property of a Photo that a client may set, except `importJob`:

- **blobId**: `Id`
  The upload.
- **name**: `String|null`
  The file's name, where it came from.
- **modified**: `String|null`
  The file's modification time there, with a UTC offset.

A new photo is `pending` unless it's given a `visibility`.

The method answers as a `/set` does, with `created` and `notCreated`.  A
photo may be rejected with any SetError a `/set` gives, or one of these:

- `blobNotFound`: there's no such upload.
- `invalidPhoto`: the upload isn't a photo or a video, or not of a kind
  Jiggle can use.  That's judged by its content when it's uploaded.
- `alreadyExists`: the blob is already a photo's original, or being
  imported by one, whose id is `existingId`.

The photo exists at once, with no `blobId` and no renditions, and with an
`importJob` whose `error` is null.  The server then reads the file and
makes its renditions.  That takes seconds for a photo, and can take
minutes for a video.

- **When the import succeeds,** in one change to the photo, the import's
  blob becomes the photo's `blobId`, `original` and `renditions` are set,
  and `importJob` becomes null.  `taken` and `location` are filled in from
  the file only where the photo has none.
- **When it fails,** `importJob.error` says why, and nothing else changes.

A client learns of either from `Photo/changes`, or from the event source.

## Album

An **Album** is an ordered list of photos.  It has the following
properties:

- **id**: `Id`
  The id of the album.  This is set by the server.
- **slug**: `String`
  The album's name in its URL and in queries: lowercase letters and
  digits, with single hyphens between.  No two albums have the same slug.
- **title**: `String`
  The title of the album.  Not empty.
- **description**: `String`
  Markdown.  Empty unless given.
- **coverPhotoId**: `Id|null`
  The photo that stands for the album.  The site uses the first photo if
  this is null.
- **photoIds**: `Id[]`
  The album's photos, in order, each once.  Empty unless given.
- **created**: `String|null`
  When the album was made.  This is set by the server.
- **flickrId**: `String|null`
  For an album that came from Flickr.  This is set by the server.

### Album/get

Standard JMAP `/get` method.

### Album/changes

Standard JMAP `/changes` method.

### Album/query

Standard JMAP `/query` method, with these additions.

A **FilterCondition** object has the following properties, any of which
may be given; all must match.  Conditions combine with the standard
FilterOperators.

- **slug**: `String`
  The album's slug equals the value.
- **photoId**: `Id`
  The album holds the Photo with this id.

The following properties are supported for sorting:

- `slug`, which is the default
- `title`
- `created`
- `id`

### Album/set

Standard JMAP `/set` method.  A create or update that gives an album the
slug of another is rejected with an `alreadyExists` SetError.

Destroying an album does nothing to its photos.

## Blobs

A blobId is a letter and the SHA-256 of the blob's bytes: `O` for an
original, `R` for a rendition.  Both can be downloaded.

An upload is an `O` blob from the moment it arrives, whether or not it
becomes a photo.  The same file uploaded twice is one blob.  Any file can
be uploaded; one that isn't a photo or a video is refused when it's
imported.
