// Tests of the editor's model: what a query means, and what's asked of the
// server to make edits real.  Run by go test (see editor_node_test.go),
// with node's own test runner.  -- claude, 2026-11-23
import assert from "node:assert/strict";
import { test } from "node:test";

import {
  albumWrites, batchQuery, contested, parseQuery, photoPatch, pickBatch, rebaseSet, uniqueSlug, wholeAlbum,
} from "../../../share/editor/model.mjs";

// The albums these tests know: slug => id.
const ALBUM_IDS = { trip: "A1", cats: "A2" };
const albumId = (slug) => ALBUM_IDS[slug];

// A query asks for a filter and a sort, and picks ids, a limit, and
// perhaps an album to edit.
function queryAsks(text, want) {
  test(`query: ${text}`, () => {
    const query = parseQuery(text);
    const ask = batchQuery(query, albumId);
    // Every batch is of photos with an original; what's given here is the
    // rest of the conditions.
    const conditions = ask.filter.conditions.slice(0, -1);
    assert.deepEqual(ask.filter.conditions.at(-1), { hasBlobId: true }, "only photos with an original");
    assert.equal(ask.filter.operator, "AND");
    assert.deepEqual(
      { conditions, sort: ask.sort, ids: query.ids, limit: query.limit, album: wholeAlbum(query), text: query.text },
      { sort: null, ids: null, limit: null, album: null, text, ...want },
    );
  });
}

function queryFails(text, message) {
  test(`bad query: ${text}`, () => {
    assert.throws(() => batchQuery(parseQuery(text), albumId), { message });
  });
}

queryAsks("all", { conditions: [] });
queryAsks("pending", { conditions: [ { visibility: "pending" } ] });
queryAsks("public unlisted", { conditions: [ { visibility: "public" }, { visibility: "unlisted" } ] });
queryAsks("  private   all ", { conditions: [ { visibility: "private" } ], text: "private all" });
queryAsks("tag:High-St", { conditions: [ { tag: "High-St" } ] });
queryAsks("year:2026", { conditions: [ { takenAfter: "2026-", takenBefore: "2026." } ] });
queryAsks("pending limit:50", { conditions: [ { visibility: "pending" } ], limit: 50 });
queryAsks("id:aaaa0001 id:bbbb0002", { conditions: [], ids: [ "aaaa0001", "bbbb0002" ] });

// One album, and nothing else, is that album, to edit.
queryAsks("album:trip", {
  conditions: [ { inAlbum: "A1" } ], sort: [ { property: "position", inAlbum: "A1" } ], album: "trip",
});
// With anything beside it, it's only part of the album, in the album's order.
queryAsks("album:trip pending", {
  conditions: [ { visibility: "pending" }, { inAlbum: "A1" } ], sort: [ { property: "position", inAlbum: "A1" } ],
});
queryAsks("album:trip limit:2", {
  conditions: [ { inAlbum: "A1" } ], sort: [ { property: "position", inAlbum: "A1" } ], limit: 2,
});
// With two albums, the order is the first one's.
queryAsks("album:cats album:trip", {
  conditions: [ { inAlbum: "A2" }, { inAlbum: "A1" } ], sort: [ { property: "position", inAlbum: "A2" } ],
});

queryFails("", "a query needs at least one term");
queryFails("   ", "a query needs at least one term");
queryFails("pendng", "unknown query term: pendng");
queryFails("year:26", "unknown query term: year:26");
queryFails("limit:0", "unknown query term: limit:0");
queryFails("limit:2 limit:3", "limit given more than once");
queryFails("album:nowhere", "no album named nowhere");

// The photos that match and have no original are counted, by two filters.
test("the filters for photos not yet imported", () => {
  const ask = batchQuery(parseQuery("pending album:trip"), albumId);
  const matching = { operator: "AND", conditions: [ { visibility: "pending" }, { inAlbum: "A1" } ] };
  assert.deepEqual(ask.importing, { operator: "AND", conditions: [ matching, { hasBlobId: false }, { isImporting: true } ] });
  assert.deepEqual(ask.failed, { operator: "AND", conditions: [ matching, { hasBlobId: false }, { hasImportError: true } ] });
});

function picks(text, found, want) {
  test(`batch of ${text} from ${found}`, () => {
    assert.deepEqual(pickBatch(parseQuery(text), found), want);
  });
}

picks("all", [ "a", "b", "c" ], [ "a", "b", "c" ]);
picks("all limit:2", [ "a", "b", "c" ], [ "a", "b" ]);
picks("id:c id:a id:zzz", [ "a", "b", "c" ], [ "a", "c" ]);
picks("id:c id:b limit:1", [ "a", "b", "c" ], [ "b" ]);

function slugs(titles, want) {
  test(`slugs for ${JSON.stringify(titles)}`, () => {
    const taken = new Set([ "trip" ]);
    assert.deepEqual(titles.map(title => uniqueSlug(title, taken)), want);
  });
}

slugs([ "Berlin, 2026-07" ], [ "berlin-2026-07" ]);
slugs([ "  Søren's   Party! " ], [ "søren-s-party" ]);
slugs([ "Trip", "trip", "TRIP" ], [ "trip-2", "trip-3", "trip-4" ]);
slugs([ "!!!", "???" ], [ "album", "album-2" ]);

const PHOTO = {
  id: "p1", title: "one", description: "", visibility: "public", taken: "2026-07-01T10:00:00", rotate: 0,
  tags: { cat: true, pet: true }, location: { lat: 1, lon: 2, isPrivate: false },
};

function patches(desc, edits, want) {
  test(`patch: ${desc}`, () => assert.deepEqual(photoPatch(PHOTO, edits), want));
}

patches("plain fields", { title: "uno", visibility: "private", rotate: 90 }, { title: "uno", visibility: "private", rotate: 90 });
patches("a time", { taken: "2026-07-02T11:00:00+02:00" }, { taken: "2026-07-02T11:00:00+02:00" });
patches("no time", { taken: "" }, { taken: null });
patches("a private location", { location_private: true }, { "location/isPrivate": true });
patches("tags, one by one", { tags: [ "pet", "dog" ] }, { "tags/dog": true, "tags/cat": null });
patches("tags that need escaping", { tags: [ "cat", "pet", "a/b", "c~d" ] }, { "tags/a~1b": true, "tags/c~0d": true });
patches("albums are not the photo's", { albums: [ "A1" ], title: "uno" }, { title: "uno" });

const ALBUMS = new Map([
  [ "A1", { id: "A1", slug: "trip", title: "Trip", description: "", coverPhotoId: "p1", photoIds: [ "p1", "p2", "p3" ] } ],
  [ "A2", { id: "A2", slug: "cats", title: "Cats", description: "", coverPhotoId: null, photoIds: [] } ],
]);

function albumsWritten(desc, { moves = [], newAlbums = [], editing = null }, want) {
  test(`albums: ${desc}`, () => {
    const got = albumWrites({ albums: ALBUMS, moves, newAlbums: new Map(newAlbums), editing });
    assert.deepEqual(got, { create: {}, update: {}, ...want });
  });
}

albumsWritten("nothing", {}, {});
albumsWritten("photos added go at the end, in the order given",
  { moves: [ { id: "p9", from: [], to: [ "A1" ] }, { id: "p8", from: [ "A2" ], to: [ "A2", "A1" ] } ] },
  { update: { A1: { photoIds: [ "p1", "p2", "p3", "p9", "p8" ] } } });
albumsWritten("a photo taken out",
  { moves: [ { id: "p2", from: [ "A1" ], to: [] } ] },
  { update: { A1: { photoIds: [ "p1", "p3" ] } } });
albumsWritten("the cover taken out leaves the first photo as the cover",
  { moves: [ { id: "p1", from: [ "A1" ], to: [] } ] },
  { update: { A1: { photoIds: [ "p2", "p3" ], coverPhotoId: "p2" } } });
albumsWritten("every photo taken out leaves no cover",
  { moves: [ "p1", "p2", "p3" ].map(id => ({ id, from: [ "A1" ], to: [] })) },
  { update: { A1: { photoIds: [], coverPhotoId: null } } });
albumsWritten("a photo moved from one album to another",
  { moves: [ { id: "p3", from: [ "A1" ], to: [ "A2" ] } ] },
  { update: { A1: { photoIds: [ "p1", "p2" ] }, A2: { photoIds: [ "p3" ] } } });
albumsWritten("a new album, and one nothing was put in",
  { moves: [ { id: "p2", from: [ "A1" ], to: [ "A1", "new:1" ] } ], newAlbums: [ [ "new:1", "Trip" ], [ "new:2", "Unused" ] ] },
  { create: { "new:1": { slug: "trip-2", title: "Trip", photoIds: [ "p2" ] } } });
albumsWritten("the album being edited: its fields",
  { editing: { id: "A1", edits: { title: "The Trip", description: "It was long.", cover: "p3" } } },
  { update: { A1: { title: "The Trip", description: "It was long.", coverPhotoId: "p3" } } });
albumsWritten("the album being edited: a new order, with a photo leaving and one arriving",
  { editing: { id: "A1", edits: { order: [ "p3", "p2", "p1" ] } },
    moves: [ { id: "p2", from: [ "A1" ], to: [] }, { id: "p7", from: [], to: [ "A1" ] } ] },
  { update: { A1: { photoIds: [ "p3", "p1", "p7" ] } } });
albumsWritten("edits that change nothing", { editing: { id: "A1", edits: { title: "Trip", order: [ "p1", "p2", "p3" ] } } }, {});

function rebases(desc, { edit, was, now }, want) {
  test(`rebase: ${desc}`, () => assert.deepEqual(rebaseSet(edit, was, now), want));
}

rebases("nothing changed elsewhere", { edit: [ "a", "c" ], was: [ "a", "b" ], now: [ "a", "b" ] }, [ "a", "c" ]);
rebases("one added elsewhere", { edit: [ "a", "c" ], was: [ "a", "b" ], now: [ "a", "b", "x" ] }, [ "a", "c", "x" ]);
rebases("one removed elsewhere", { edit: [ "a", "b", "c" ], was: [ "a", "b" ], now: [ "b" ] }, [ "b", "c" ]);
rebases("the same one added on both sides", { edit: [ "a", "c" ], was: [ "a" ], now: [ "a", "c" ] }, [ "a", "c" ]);
rebases("the same one removed on both sides", { edit: [ "a" ], was: [ "a", "b" ], now: [ "a" ] }, [ "a" ]);

const READ = { title: p => p.title, rotate: p => p.rotate };
const SAME = (a, b) => a === b;

function contests(desc, { edits, was, now }, want) {
  test(`conflict: ${desc}`, () => assert.deepEqual(contested(edits, was, now, READ, SAME), want));
}

contests("another field changed", { edits: { title: "mine" }, was: { title: "old", rotate: 0 }, now: { title: "old", rotate: 90 } }, []);
contests("the same field changed", { edits: { title: "mine" }, was: { title: "old" }, now: { title: "theirs" } }, [ "title" ]);
contests("the same field changed to what the edit asks", { edits: { title: "same" }, was: { title: "old" }, now: { title: "same" } }, []);
contests("a set is not contested", { edits: { tags: [ "x" ] }, was: { title: "old" }, now: { title: "new" } }, []);
