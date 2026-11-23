// The parts of jiggle's editor that are about data and not about the page:
// what a query means, and what to ask of the server to make edits real.
// Nothing here touches the document or the network, so that it can all be
// tested without a browser.  -- claude, 2026-11-23

const VISIBILITIES = new Set([ "pending", "private", "public", "unlisted" ]);

// Reads a query as a person types it: terms, separated by spaces, all of
// which a photo must match.
//
//   pending         not yet reviewed (and so not published)
//   private         private
//   public          public: published and listed
//   unlisted        unlisted: published, but listed only in albums
//   all             every photo
//   album:SLUG      in the album with that slug
//   tag:TAG         tagged TAG, compared as the site compares tags (by slug)
//   year:YYYY       taken in that year
//   id:ID           the photo with that id; given more than once, any of them
//   limit:N         only the first N of the photos that match
//
// The server knows none of this: it takes JMAP filters.  This returns what
// to make a filter of: conditions, each a Photo FilterCondition; albums,
// the slugs of the albums named, whose ids aren't known here; ids, the ids
// named, or null if none were; limit, or null; and text, the query with its
// spaces tidied.  It throws an Error whose message is for the person who
// typed the query.
export function parseQuery(text) {
  const terms = text.split(/\s+/).filter(term => term !== "");
  if (!terms.length) throw new Error("a query needs at least one term");

  const query = { text: terms.join(" "), conditions: [], albums: [], ids: null, limit: null };
  for (const term of terms) {
    let m;
    if (term === "all") {
      // every photo matches
    } else if (VISIBILITIES.has(term)) {
      query.conditions.push({ visibility: term });
    } else if ((m = /^album:(.+)$/.exec(term))) {
      query.albums.push(m[1]);
    } else if ((m = /^tag:(.+)$/.exec(term))) {
      query.conditions.push({ tag: m[1] });
    } else if ((m = /^year:([0-9]{4})$/.exec(term))) {
      // Times are compared as text.  Everything taken in 2026 begins
      // "2026-", and "." is the character after "-", so "2026." is past
      // all of it and before any other year.
      query.conditions.push({ takenAfter: `${m[1]}-`, takenBefore: `${m[1]}.` });
    } else if ((m = /^id:(\S+)$/.exec(term))) {
      (query.ids ??= []).push(m[1]);
    } else if ((m = /^limit:([1-9][0-9]*)$/.exec(term))) {
      if (query.limit !== null) throw new Error("limit given more than once");
      query.limit = Number(m[1]);
    } else {
      throw new Error(`unknown query term: ${term}`);
    }
  }
  return query;
}

// The slug of the one album a query is the whole of, if that's what it is:
// exactly one term, album:SLUG.  That album can be edited along with its
// photos.  With any other term beside it, the batch is only part of the
// album.
export function wholeAlbum(query) {
  const only = query.albums.length === 1 && !query.conditions.length && query.ids === null && query.limit === null;
  return only && query.text === `album:${query.albums[0]}` ? query.albums[0] : null;
}

// Makes the filter and sort of a Photo/query for a parsed query.  albumId
// gives the id of the album with a slug, or undefined if there is none,
// which is an error.  Photos come in the order of the first album named,
// or else in the order they were taken, which is the server's default.
//
// A batch has only photos with an original, since a photo without one has
// no renditions to show.
export function batchQuery(query, albumId) {
  const conditions = [ ...query.conditions ];
  let sort = null;
  for (const slug of query.albums) {
    const id = albumId(slug);
    if (id === undefined) throw new Error(`no album named ${slug}`);
    conditions.push({ inAlbum: id });
    sort ??= [ { property: "position", inAlbum: id } ];
  }
  const matching = { operator: "AND", conditions };
  return {
    filter: { operator: "AND", conditions: [ ...conditions, { hasBlobId: true } ] },
    sort,
    // Those that match and aren't in the batch, for want of an original.
    importing: { operator: "AND", conditions: [ matching, { hasBlobId: false }, { isImporting: true } ] },
    failed:    { operator: "AND", conditions: [ matching, { hasBlobId: false }, { hasImportError: true } ] },
  };
}

// Picks a batch out of the ids a Photo/query found: only the ids the query
// named, if it named any, and only as many as its limit.
export function pickBatch(query, found) {
  let ids = found;
  if (query.ids !== null) {
    const wanted = new Set(query.ids);
    ids = ids.filter(id => wanted.has(id));
  }
  return query.limit === null ? ids : ids.slice(0, query.limit);
}

// A slug for a new album with the given title: lowercase, with runs of
// anything but letters and digits made into hyphens, and a number added if
// an album has the slug already.  taken is a Set of the slugs in use, to
// which the new one is added.
export function uniqueSlug(title, taken) {
  let slug = title.toLowerCase().replace(/[^\p{Ll}\p{Lo}\p{N}]+/gu, "-").replace(/^-+|-+$/g, "");
  if (slug === "") slug = "album";

  let candidate = slug;
  for (let n = 2; taken.has(candidate); n++) candidate = `${slug}-${n}`;
  taken.add(candidate);
  return candidate;
}

// A name, as one step of the JSON Pointer that a JMAP patch's key is.
const pointerStep = (name) => name.replace(/~/g, "~0").replace(/\//g, "~1");

// The fields of a photo that the editor edits, and how each is read from a
// Photo as the server gives it.  A field's value is undefined on a photo
// that hasn't got the field at all: only a photo with a location can have
// its location kept private.
//
// "albums" is edited as a field of a photo, too, but it isn't a property of
// one: see albumWrites.
export const PHOTO_FIELDS = {
  title:            p => p.title,
  description:      p => p.description,
  tags:             p => Object.keys(p.tags).sort(),
  visibility:       p => p.visibility,
  taken:            p => p.taken ?? "",
  rotate:           p => p.rotate,
  location_private: p => p.location ? p.location.isPrivate : undefined,
};

// Makes the patch for a Photo/set update, of a photo as the server last
// gave it and the editor's edits to it: an object of field to new value.
// Tags are patched one by one, so that a tag added somewhere else meanwhile
// is not taken away again.
export function photoPatch(photo, edits) {
  const patch = {};
  for (const [field, value] of Object.entries(edits)) {
    if (field === "albums") continue;
    if (field === "tags") {
      const had = new Set(Object.keys(photo.tags));
      const has = new Set(value);
      for (const tag of has) if (!had.has(tag)) patch[`tags/${pointerStep(tag)}`] = true;
      for (const tag of had) if (!has.has(tag)) patch[`tags/${pointerStep(tag)}`] = null;
    } else if (field === "taken") {
      patch.taken = value === "" ? null : value;
    } else if (field === "location_private") {
      patch["location/isPrivate"] = value;
    } else {
      patch[field] = value;
    }
  }
  return patch;
}

// Works out the Album/set that makes the editor's edits to albums real.
//
//   albums     every album, as the server last gave it: a Map of id to Album
//   moves      for each photo whose albums were edited, in the order the
//              photos are shown in: { id, from, to }, the albums it was in
//              and the albums it should be in.  An album is named by its
//              id, or by the key of a new album.
//   newAlbums  a Map of key to title, for albums that don't exist yet
//   editing    the album being edited as a whole, if one is: { id, edits },
//              where edits may have title, description, cover, and order,
//              the ids of the album's photos in a new order
//
// It returns { create, update }, as the arguments of an Album/set, each
// possibly empty.  A new album that no photo is put in isn't created.
// Photos added to an album go at its end.  An album whose cover leaves it
// gets its first photo as its cover, as it does when the server takes a
// destroyed photo out of an album.
export function albumWrites({ albums, moves, newAlbums, editing }) {
  const added = new Map(), removed = new Map();
  const note = (map, key, id) => { if (!map.has(key)) map.set(key, []); map.get(key).push(id); };
  for (const { id, from, to } of moves) {
    for (const key of to) if (!from.includes(key)) note(added, key, id);
    for (const key of from) if (!to.includes(key)) note(removed, key, id);
  }

  const create = {}, update = {};
  const taken = new Set([ ...albums.values() ].map(album => album.slug));
  for (const [key, title] of newAlbums) {
    if (!added.has(key)) continue;
    create[key] = { slug: uniqueSlug(title, taken), title, photoIds: added.get(key) };
  }

  for (const [id, album] of albums) {
    const edits = editing && editing.id === id ? editing.edits : {};
    const gone = new Set(removed.get(id) ?? []);
    const photoIds = (edits.order ?? album.photoIds).filter(photo => !gone.has(photo));
    for (const photo of added.get(id) ?? []) if (!photoIds.includes(photo)) photoIds.push(photo);

    let cover = "cover" in edits ? edits.cover : album.coverPhotoId;
    if (cover !== null && !photoIds.includes(cover) && album.photoIds.includes(cover)) cover = photoIds[0] ?? null;

    const change = {};
    if (!sameList(photoIds, album.photoIds)) change.photoIds = photoIds;
    if (cover !== album.coverPhotoId) change.coverPhotoId = cover;
    for (const field of [ "title", "description" ]) {
      if (field in edits && edits[field] !== album[field]) change[field] = edits[field];
    }
    if (Object.keys(change).length) update[id] = change;
  }
  return { create, update };
}

// Carries an edit to a set over a change made to the set elsewhere.  edit
// is the members the editor wants, worked out from the set as it was; now
// is the set as it has become.  What was added elsewhere is added to the
// edit, and what was taken away is taken away, so that the edit still asks
// for only what the editor changed.
export function rebaseSet(edit, was, now) {
  const kept = edit.filter(x => now.includes(x) || !was.includes(x));
  return [ ...kept, ...now.filter(x => !was.includes(x) && !kept.includes(x)) ];
}

// The fields among a photo's edits that a change made elsewhere has made a
// question of: those whose saved value changed, from was to now, to
// something other than what the edit asks for.  Writing such an edit would
// undo the other change without anyone having seen it.  read gives a
// field's value from an object, and sets are left out, being rebased.
export function contested(edits, was, now, read, same) {
  return Object.keys(edits).filter(field => {
    if (!(field in read)) return false;
    const [ before, after ] = [ read[field](was), read[field](now) ];
    return !same(before, after) && !same(after, edits[field]);
  });
}

export const sameList = (a, b) => a.length === b.length && a.every((x, i) => x === b[i]);

// Whether two lists have the same members, in whatever order.
export const sameSet = (a, b) => a.length === b.length && a.every(x => b.includes(x));

// Cuts a list into pieces of at most the given size.
export function chunks(list, size) {
  const out = [];
  for (let i = 0; i < list.length; i += size) out.push(list.slice(i, i + size));
  return out;
}
