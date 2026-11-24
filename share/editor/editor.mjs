// jiggle's editor: a contact sheet of a batch chosen by a query, and a
// sidebar for editing the selected photos; or, with no query, a list of the
// library's albums.  -- claude, 2026-09-30
//
// It is a JMAP client, and gets everything from the server's API: the
// photos and albums as objects, the pictures as blobs, and word of changes
// made elsewhere from the event source.  -- claude, 2026-11-23
//
// The batch belongs to the page: a query is answered with photos, and the
// page keeps them until it asks again (a new query, or Refresh), so photos
// don't drop out of the batch as they're edited.
//
// The page keeps each photo as the server last gave it ("saved"), and a map
// of edits: for each photo, the fields whose values differ from saved.  A
// field is dirty when any selected photo has an edit for it.  Writing sends
// the edits, and an edit is cleared when the server says it was made.  A
// change made elsewhere replaces what's saved, and leaves the edits alone.
import { Client, MethodError } from "./jmap.mjs";
import {
  PHOTO_FIELDS, albumWrites, batchQuery, chunks, contested, parseQuery, photoPatch, pickBatch, rebaseSet, sameSet,
  wholeAlbum,
} from "./model.mjs";

const CAPABILITY = "https://semiotic.systems/jmap/jiggle";

let jmap;

// What the page knows of the whole library, whatever the batch is.
const lib = {
  states:   { Photo: null, Album: null },
  index:    new Map(),   // every photo: id => { visibility, tags }
  albums:   new Map(),   // every album: id => Album
  albumsOf: new Map(),   // photo id => [ album id ]
  thumbs:   new Map(),   // photo id => the blobId of its thumbnail, for covers
};

const state = {
  query:    null,        // the batch's query, or null for the album list
  ask:      null,        // the batch's filters; see batchQuery
  photos:   [],          // the batch, in order, as the server last gave them
  byId:     new Map(),
  waiting:  { importing: 0, failed: 0 },  // photos that match and have no original yet
  edits:    new Map(),   // id => { field: value }
  errors:   new Map(),   // id => why the photo's last write was refused
  conflicts: new Map(),  // id => the fields changed elsewhere, under edits here, and not yet looked at
  rendered: new Map(),   // id => the rotate its renditions were made with, when that isn't its rotate
  selected: new Set(),
  anchor:   null,        // where a shift-click range starts
  newAlbums: new Map(),  // key => title, for albums made here and not yet written
  newAlbumCount: 0,
  album:    null,        // the id of the album being edited, if the batch is one
  albumEdits: {},        // field => value, like a photo's edits
  albumConflicts: new Set(),  // like a photo's conflicts
  frozen:   false,
};

const $ = (sel) => document.querySelector(sel);

function el(tag, attrs = {}, ...kids) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === null || v === undefined || v === false) continue;
    if (k === "class") node.className = v;
    else if (k === "text") node.textContent = v;
    else if (k.startsWith("on")) node.addEventListener(k.slice(2), v);
    else if (k in node && typeof v !== "string") node[k] = v;
    else node.setAttribute(k, v === true ? "" : v);
  }
  for (const kid of kids.flat()) {
    if (kid === null || kid === undefined || kid === false) continue;
    node.append(kid instanceof Node ? kid : document.createTextNode(kid));
  }
  return node;
}

function setStatus(text, bad = false, ...extra) {
  const s = $("#status");
  s.replaceChildren(text, ...extra);
  s.classList.toggle("bad", bad);
}

// ---------------------------------------------------------------------------
// The model

// How each field is read from a photo as saved.  A photo's albums aren't a
// property of it: they're the albums whose photoIds have it.
const SAVED = {
  ...PHOTO_FIELDS,
  albums: p => lib.albumsOf.get(p.id) ?? [],
};

const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);

// Tags and albums are sets: a tag removed and put back is no change, though
// it would now be last.
const SET_FIELDS = new Set([ "tags", "albums" ]);
const sameValue = (field, a, b) => SET_FIELDS.has(field) ? sameSet(a, b) : same(a, b);

// The fields with one value, which two changes at once can't both have.
const SCALAR_FIELDS = Object.fromEntries(Object.entries(PHOTO_FIELDS).filter(([field]) => !SET_FIELDS.has(field)));

function current(p, field) {
  const e = state.edits.get(p.id);
  return e && field in e ? e[field] : SAVED[field](p);
}

function isEdited(p, field) {
  const e = state.edits.get(p.id);
  return !!e && field in e;
}

// Sets a field on each photo to what fn gives, from its current value.
function change(photos, field, fn) {
  for (const p of photos) {
    const value = fn(current(p, field), p);
    const e = state.edits.get(p.id) || {};
    if (sameValue(field, value, SAVED[field](p))) delete e[field];
    else e[field] = value;
    if (Object.keys(e).length) state.edits.set(p.id, e);
    else state.edits.delete(p.id);
    settled(p.id);
  }
  for (const p of photos) refreshThumb(p.id);
  refreshWriteButton();
}

function revert(photos, field) {
  change(photos, field, (_, p) => SAVED[field](p));
}

// Forgets why a photo's last write was refused, once the photo has been
// edited again.
function settled(id) {
  state.errors.delete(id);
  pruneConflicts(id);
}

// Forgets any conflict over a field of a photo that is no longer edited.
function pruneConflicts(id) {
  const e = state.edits.get(id) ?? {};
  const fields = state.conflicts.get(id);
  if (!fields) return;
  for (const field of fields) if (!(field in e)) fields.delete(field);
  if (!fields.size) state.conflicts.delete(id);
}

// Drops every edit that asks for what is already so, which is what an edit
// becomes when the same change is made elsewhere, or when it's written.
function normalizeEdits() {
  for (const [id, e] of state.edits) {
    const p = state.byId.get(id);
    for (const field of Object.keys(e)) {
      const was = SAVED[field](p);
      if (was === undefined || sameValue(field, e[field], was)) delete e[field];
    }
    if (!Object.keys(e).length) state.edits.delete(id);
    pruneConflicts(id);
  }

  const album = theAlbum();
  if (!album) { state.albumEdits = {}; state.albumConflicts.clear(); return; }
  // A new order is of the photos the album had.  If it has others now, the
  // order means nothing.
  if ("order" in state.albumEdits && !sameSet(state.albumEdits.order, album.photoIds)) delete state.albumEdits.order;
  for (const field of Object.keys(state.albumEdits)) {
    if (same(state.albumEdits[field], ALBUM_SAVED[field](album))) delete state.albumEdits[field];
  }
  for (const field of state.albumConflicts) if (!(field in state.albumEdits)) state.albumConflicts.delete(field);
}

// The album, when the batch is one: like a photo, it's kept as saved, with
// a set of edits.  Its order is the order of the sheet.
const ALBUM_SAVED = {
  title:       a => a.title,
  description: a => a.description,
  cover:       a => a.coverPhotoId,
  order:       a => a.photoIds,
};

const theAlbum = () => state.album === null ? null : lib.albums.get(state.album) ?? null;

function albumCurrent(field) {
  return field in state.albumEdits ? state.albumEdits[field] : ALBUM_SAVED[field](theAlbum());
}

function albumChange(field, value) {
  if (same(value, ALBUM_SAVED[field](theAlbum()))) delete state.albumEdits[field];
  else state.albumEdits[field] = value;
  state.errors.delete(`album:${state.album}`);
  if (!(field in state.albumEdits)) state.albumConflicts.delete(field);
  refreshWriteButton();
}

const albumDirty = () => Object.keys(state.albumEdits).length > 0;
const albumEdited = (field) => field in state.albumEdits;

// The album can be edited only if every photo in it is in the batch: one
// added to it since the batch was made, or one still being imported, can't
// be placed in an order.
function albumEditable() {
  const album = theAlbum();
  return !!album && album.photoIds.every(id => state.byId.has(id));
}

// Shows the sheet in the album's order, then any photos no longer in it.
function applyOrder() {
  if (!albumEditable()) return;
  const order = albumCurrent("order");
  const placed = new Set(order);
  state.photos = [
    ...order.map(id => state.byId.get(id)),
    ...state.photos.filter(p => !placed.has(p.id)),
  ];
}

const inAlbum = (p) => state.album === null || current(p, "albums").includes(state.album);

// ---------------------------------------------------------------------------
// Keeping up with the server
//
// Everything that reads from the server or writes to it takes its turn in
// one queue, so that what the page knows changes in one place at a time.

let turn = Promise.resolve();

function inTurn(fn) {
  const result = turn.then(fn);
  turn = result.catch(() => {});
  return result;
}

// A change made elsewhere to something that has edits here is carried into
// the edits, if it can be, and otherwise is a conflict: the edit stays, the
// thing is marked, and it isn't written until it has been seen so marked.
// Sets can always be carried: a tag added elsewhere is added to the edit.
// A field with one value can't, when both sides have changed it.
function adoptAlbums(list) {
  const was = { album: theAlbum(), albumsOf: lib.albumsOf };
  readAlbums(list);

  for (const [id, e] of state.edits) {
    if ("albums" in e) e.albums = rebaseSet(e.albums, was.albumsOf.get(id) ?? [], lib.albumsOf.get(id) ?? []);
  }
  const now = theAlbum();
  if (was.album && now) {
    for (const field of contested(state.albumEdits, was.album, now, ALBUM_SAVED, same)) state.albumConflicts.add(field);
  }
}

function readAlbums(list) {
  lib.albums = new Map(list.map(a => [ a.id, a ]));
  lib.albumsOf = new Map();
  for (const a of list) {
    for (const id of a.photoIds) {
      if (!lib.albumsOf.has(id)) lib.albumsOf.set(id, []);
      lib.albumsOf.get(id).push(a.id);
    }
  }
}

const indexed = (p) => ({ visibility: p.visibility, tags: Object.keys(p.tags) });

// Loads what the page knows of the whole library.  The states are read
// first, so that a later /changes brings anything this just missed.
async function loadLibrary() {
  const [ none, noAlbums, found, albums ] = await jmap.request([
    [ "Photo/get", { ids: [] } ],
    [ "Album/get", { ids: [] } ],
    [ "Photo/query", {} ],
    [ "Album/get", { ids: null } ],
  ]);
  const photos = await jmap.getAll("Photo", found.ids, [ "visibility", "tags" ]);

  lib.states = { Photo: none.state, Album: noAlbums.state };
  lib.index = new Map(photos.list.map(p => [ p.id, indexed(p) ]));
  lib.thumbs = new Map();
  adoptAlbums(albums.list);
}

// A saved photo is replaced by the photo as the server now has it.
function replaceSaved(record) {
  const old = state.byId.get(record.id);

  // A photo whose rotate has changed keeps its old renditions until the
  // new ones are made, and is shown turned from those meanwhile.
  const thumb = (p) => p.renditions["h480.webp"]?.blobId;
  if (thumb(old) !== thumb(record)) state.rendered.delete(record.id);
  else if (old.rotate !== record.rotate) state.rendered.set(record.id, renderedRotate(old));

  const e = state.edits.get(record.id);
  if (e) {
    if ("tags" in e) e.tags = rebaseSet(e.tags, SAVED.tags(old), SAVED.tags(record));
    const fields = contested(e, old, record, SCALAR_FIELDS, same);
    if (fields.length) state.conflicts.set(record.id, new Set([ ...(state.conflicts.get(record.id) ?? []), ...fields ]));
  }

  const i = state.photos.findIndex(p => p.id === record.id);
  if (i >= 0) state.photos[i] = record;
  state.byId.set(record.id, record);
}

function dropFromBatch(id) {
  state.photos = state.photos.filter(p => p.id !== id);
  state.byId.delete(id);
  state.edits.delete(id);
  state.errors.delete(id);
  state.conflicts.delete(id);
  state.rendered.delete(id);
  state.selected.delete(id);
  if ("order" in state.albumEdits) state.albumEdits.order = state.albumEdits.order.filter(x => x !== id);
}

// Asks for every change to a type since the state the page has of it, and
// returns { created, updated, destroyed }, or null if the server can't say,
// in which case the page has to read everything again.
async function changesTo(type) {
  const all = { created: [], updated: [], destroyed: [] };
  for (;;) {
    const [ got ] = await jmap.request([ [ `${type}/changes`, { sinceState: lib.states[type] } ] ], { every: false });
    if (got instanceof MethodError) {
      if (got.type === "cannotCalculateChanges") return null;
      throw got;
    }
    for (const kind of Object.keys(all)) all[kind].push(...got[kind]);
    lib.states[type] = got.newState;
    if (!got.hasMoreChanges) return all;
  }
}

// Brings the page up to date with the server, and returns what changed:
// { photos, albums }, each a Set of ids.
async function sync() {
  const changed = { photos: new Set(), albums: new Set() };

  const photos = await changesTo("Photo");
  const albums = photos && await changesTo("Album");
  if (!photos || !albums) {
    await loadLibrary();
    const fresh = await jmap.getAll("Photo", state.photos.map(p => p.id));
    const got = new Set(fresh.list.map(p => p.id));
    for (const p of [ ...state.photos ]) if (!got.has(p.id)) dropFromBatch(p.id);
    for (const p of fresh.list) replaceSaved(p);
    changed.photos = got;
    changed.albums = new Set(lib.albums.keys());
  } else {
    const ids = [ ...new Set([ ...photos.created, ...photos.updated ]) ];
    for (const p of (await jmap.getAll("Photo", ids)).list) {
      lib.index.set(p.id, indexed(p));
      lib.thumbs.delete(p.id);
      if (state.byId.has(p.id)) replaceSaved(p);
      changed.photos.add(p.id);
    }
    for (const id of photos.destroyed) {
      lib.index.delete(id);
      if (state.byId.has(id)) dropFromBatch(id);
      changed.photos.add(id);
    }
    changed.albums = new Set([ ...albums.created, ...albums.updated, ...albums.destroyed ]);
    if (changed.albums.size) adoptAlbums((await jmap.call("Album/get", { ids: null })).list);
  }

  if (state.album !== null && !lib.albums.has(state.album)) state.album = null;
  normalizeEdits();
  if (changed.photos.size && state.ask) await countWaiting();
  return changed;
}

// Counts the photos that match the batch's query and aren't in it, having
// no original yet: those still being imported, and those whose import
// failed.
async function countWaiting() {
  const count = (filter) => [ "Photo/query", { filter, calculateTotal: true, limit: 1 } ];
  const [ importing, failed ] = await jmap.request([ count(state.ask.importing), count(state.ask.failed) ]);
  state.waiting = { importing: importing.total, failed: failed.total };
}

// Shows what a sync changed.
function showChanges(changed) {
  if (!changed.photos.size && !changed.albums.size) return;
  refreshTagList();
  refreshAlbumList();
  refreshWaiting();
  refreshWriteButton();
  if (state.query === null) { renderAlbumList(); return; }

  applyOrder();
  renderSheet();
  // The sidebar is left alone while it's being typed in.  Its fields read
  // what's saved when they need it, so they aren't wrong, only not redrawn.
  if (!document.activeElement || !document.activeElement.closest("#sidebar")) renderSidebar();
}

// Called with the server's states when they change.
function heard(states) {
  if (states.Photo === lib.states.Photo && states.Album === lib.states.Album) return;
  inTurn(sync).then(showChanges, (e) => setStatus(`keeping up with the server: ${e.message}`, true));
}

// ---------------------------------------------------------------------------
// Batches

// Asks whether unwritten edits can be thrown away, if there are any.
function mayDiscard() {
  if (!unwritten()) return true;
  return confirm("You have changes that aren't written.  Discard them?");
}

function discardEdits() {
  state.edits.clear();
  state.errors.clear();
  state.conflicts.clear();
  state.albumEdits = {};
  state.albumConflicts.clear();
  state.newAlbums.clear();
}

function albumWithSlug(slug) {
  for (const a of lib.albums.values()) if (a.slug === slug) return a;
  return undefined;
}

// Fetches the batch a query picks: { query, ask, photos, waiting }.
async function fetchBatch(q) {
  await sync();
  const query = parseQuery(q);
  const ask = batchQuery(query, (slug) => albumWithSlug(slug)?.id);

  // With ids named, the limit is on what's left when the rest are gone.
  const args = { filter: ask.filter };
  if (ask.sort) args.sort = ask.sort;
  if (query.limit !== null && query.ids === null) args.limit = query.limit;

  const found = await jmap.call("Photo/query", args);
  const ids = pickBatch(query, found.ids);
  const got = new Map((await jmap.getAll("Photo", ids)).list.map(p => [ p.id, p ]));
  return { query, ask, photos: ids.map(id => got.get(id)).filter(Boolean) };
}

// Shows the batch a query picks.  With keep, the selection is kept for
// photos still in it, as for Refresh.
async function runQuery(q, { push = true, keep = false } = {}) {
  if (!mayDiscard()) return;

  let batch;
  setStatus("loading…");
  try {
    batch = await inTurn(async () => {
      const batch = await fetchBatch(q);
      state.ask = batch.ask;
      await countWaiting();
      return batch;
    });
  } catch (e) {
    setStatus(e.message, true);
    return;
  }

  discardEdits();
  if (!keep) { state.selected.clear(); state.anchor = null; }

  state.query  = batch.query.text;
  state.photos = batch.photos;
  state.byId   = new Map(batch.photos.map(p => [ p.id, p ]));
  state.rendered.clear();
  const slug = wholeAlbum(batch.query);
  state.album = slug === null ? null : albumWithSlug(slug).id;
  applyOrder();
  for (const id of [ ...state.selected ]) if (!state.byId.has(id)) state.selected.delete(id);

  const url = `/?${new URLSearchParams({ q: state.query })}`;
  if (push) history.pushState({ q: state.query }, "", url);
  else history.replaceState({ q: state.query }, "", url);

  $("#query").value = state.query;
  document.title = `jiggle: ${state.query}`;
  setStatus(`${state.photos.length} photo(s)`);
  refreshTagList();
  refreshAlbumList();
  refreshWaiting();
  renderSheet();
  renderSidebar();
  refreshWriteButton();
}

function refresh() {
  if (state.query === null || unwritten()) return;
  runQuery(state.query, { push: false, keep: true });
}

// Says how many photos match the query and can't be shown yet.
function refreshWaiting() {
  const { importing, failed } = state.query === null ? { importing: 0, failed: 0 } : state.waiting;
  const node = $("#waiting");
  node.textContent = [
    importing ? `${importing} more still being imported` : null,
    failed ? `${failed} more failed to import` : null,
  ].filter(Boolean).join("; ");
  node.classList.toggle("bad", failed > 0);
}

// ---------------------------------------------------------------------------
// The contact sheet

const THUMB_H = 150;

const RENDITION_TYPE = { webp: "image/webp", png: "image/png", jpg: "image/jpeg", mp4: "video/mp4" };

// The URL of one of a photo's renditions, or null if it hasn't got it.  A
// blob never changes, so neither does what's at its URL, and a rendition
// that is made again is a new blob at a new one.
function renditionUrl(p, name) {
  const r = p.renditions[name];
  return r ? jmap.blobUrl(r.blobId, name, RENDITION_TYPE[name.split(".").pop()]) : null;
}

// An import of the photo is under way, or has failed.
const importing = (p) => !!p.importJob && p.importJob.error === null;
const importFailed = (p) => !!p.importJob && p.importJob.error !== null;

const renderedRotate = (p) => state.rendered.get(p.id) ?? p.rotate;

// The turn to give a photo's renditions, with CSS, to show it as it will
// be: for a rotation that isn't written, or one whose renditions aren't
// made yet.
function rotationDelta(p) {
  return (((current(p, "rotate") - renderedRotate(p)) % 360) + 360) % 360;
}

function thumbFor(p) {
  const delta = rotationDelta(p);
  const r = p.renditions["h480.webp"];
  const ratio = r ? r.width / r.height : 4 / 3;

  // The image is laid out as it was made, then turned; the frame is the
  // size it takes up once turned.
  let imgW = Math.round(THUMB_H * ratio), imgH = THUMB_H;
  if (delta % 180) { imgW = THUMB_H; imgH = Math.round(THUMB_H / ratio); }
  const frameW = delta % 180 ? imgH : imgW;

  const img = el("img", { src: renditionUrl(p, "h480.webp"), loading: "lazy", alt: "" });
  Object.assign(img.style, {
    width: `${imgW}px`, height: `${imgH}px`,
    left: `${(frameW - imgW) / 2}px`, top: `${(THUMB_H - imgH) / 2}px`,
    transform: delta ? `rotate(${delta}deg)` : "",
  });

  const title = current(p, "title");
  const node = el("figure", { class: "thumb", "data-id": p.id, style: `width: ${frameW + 10}px` },
    el("div", { class: "frame", style: `width: ${frameW}px` }, img),
    el("div", { class: "badges" },
      state.errors.has(p.id) ? el("span", { class: "badge out", text: "not written", title: state.errors.get(p.id) }) : null,
      state.conflicts.has(p.id) ? el("span", { class: "badge out", text: "changed elsewhere" }) : null,
      state.album !== null && theAlbum() && albumCurrent("cover") === p.id ? el("span", { class: "badge cover", text: "cover" }) : null,
      inAlbum(p) ? null : el("span", { class: "badge out", text: "out of album" }),
      importing(p) ? el("span", { class: "badge pending", text: "importing" }) : null,
      importFailed(p) ? el("span", { class: "badge out", text: "import failed", title: p.importJob.error }) : null,
      current(p, "visibility") === "pending" ? el("span", { class: "badge pending", text: "pending" }) : null,
      current(p, "visibility") === "private" ? el("span", { class: "badge", text: "private" }) : null,
      current(p, "visibility") === "unlisted" ? el("span", { class: "badge", text: "unlisted" }) : null,
      p.type === "video" ? el("span", { class: "badge", text: "▶" }) : null,
    ),
    el("figcaption", { text: photoName(p) }),
  );
  node.classList.toggle("selected", state.selected.has(p.id));
  node.classList.toggle("dirty", state.edits.has(p.id));
  return node;
}

function renderSheet() {
  const sheet = $("#sheet");
  sheet.classList.remove("albums");
  if (!state.photos.length) {
    sheet.replaceChildren(el("p", { class: "empty", text: "No photos match." }));
    return;
  }
  sheet.replaceChildren(...state.photos.map(thumbFor));
}

function refreshThumb(id) {
  const old = document.querySelector(`.thumb[data-id="${id}"]`);
  if (old) old.replaceWith(thumbFor(state.byId.get(id)));
}

function paintSelection() {
  for (const node of document.querySelectorAll(".thumb")) {
    node.classList.toggle("selected", state.selected.has(node.dataset.id));
  }
}

function selectionChanged() {
  paintSelection();
  renderSidebar();
}

function indexOf(id) {
  return state.photos.findIndex(p => p.id === id);
}

function clickThumb(ev, id) {
  const toggle = ev.metaKey || ev.ctrlKey;

  if (ev.shiftKey && state.anchor && state.byId.has(state.anchor)) {
    const [a, b] = [indexOf(state.anchor), indexOf(id)].sort((x, y) => x - y);
    if (!toggle) state.selected.clear();
    for (const p of state.photos.slice(a, b + 1)) state.selected.add(p.id);
  } else if (toggle) {
    if (state.selected.has(id)) state.selected.delete(id);
    else state.selected.add(id);
    state.anchor = id;
  } else {
    state.selected = new Set([id]);
    state.anchor = id;
  }

  selectionChanged();
}

// Dragging a box selects the photos it touches.  With ⌘ held, it toggles
// them against the selection it started from, as the Finder does.  Thumbnail
// positions are measured once, when the drag starts, in the sheet's scrolled
// coordinates, so a drag over thousands of photos stays quick.
let drag = null;

function sheetPointerDown(ev) {
  if (ev.button !== 0 || state.frozen || state.query === null) return;
  const sheet = $("#sheet");
  const thumb = ev.target.closest(".thumb");

  // Leave any field being edited, so its value is kept before the
  // selection (and so the sidebar) changes.
  if (document.activeElement && document.activeElement.closest("#sidebar")) {
    document.activeElement.blur();
  }

  drag = {
    x: ev.clientX, y: ev.clientY,
    thumb: thumb ? thumb.dataset.id : null,
    toggle: ev.metaKey || ev.ctrlKey,
    base: new Set(state.selected),
    boxing: false,
    rects: null,
    origin: sheet.getBoundingClientRect(),
  };

  sheet.setPointerCapture(ev.pointerId);
  ev.preventDefault();
}

function measureThumbs(sheet, origin) {
  return [...sheet.querySelectorAll(".thumb")].map(node => {
    const r = node.getBoundingClientRect();
    return {
      id: node.dataset.id,
      left:   r.left   - origin.left + sheet.scrollLeft,
      top:    r.top    - origin.top  + sheet.scrollTop,
      right:  r.right  - origin.left + sheet.scrollLeft,
      bottom: r.bottom - origin.top  + sheet.scrollTop,
    };
  });
}

function sheetPointerMove(ev) {
  if (!drag) return;
  const sheet = $("#sheet");

  if (!drag.boxing) {
    if (Math.hypot(ev.clientX - drag.x, ev.clientY - drag.y) < 5) return;
    drag.boxing = true;
    drag.rects = measureThumbs(sheet, drag.origin);
    drag.startX = drag.x - drag.origin.left + sheet.scrollLeft;
    drag.startY = drag.y - drag.origin.top  + sheet.scrollTop;
    $("#box").hidden = false;
  }

  // Scroll when the pointer is near the top or bottom edge.
  const edge = 40;
  if (ev.clientY > drag.origin.bottom - edge) sheet.scrollTop += 20;
  else if (ev.clientY < drag.origin.top + edge) sheet.scrollTop -= 20;

  const nowX = ev.clientX - drag.origin.left + sheet.scrollLeft;
  const nowY = ev.clientY - drag.origin.top  + sheet.scrollTop;
  const box = {
    left:  Math.min(drag.startX, nowX), right:  Math.max(drag.startX, nowX),
    top:   Math.min(drag.startY, nowY), bottom: Math.max(drag.startY, nowY),
  };

  const b = $("#box");
  b.style.left   = `${box.left - sheet.scrollLeft + drag.origin.left}px`;
  b.style.top    = `${box.top  - sheet.scrollTop  + drag.origin.top}px`;
  b.style.width  = `${box.right - box.left}px`;
  b.style.height = `${box.bottom - box.top}px`;

  const next = drag.toggle ? new Set(drag.base) : new Set();
  for (const r of drag.rects) {
    const hit = r.left < box.right && r.right > box.left
             && r.top < box.bottom && r.bottom > box.top;
    if (!hit) continue;
    if (drag.toggle && drag.base.has(r.id)) next.delete(r.id);
    else next.add(r.id);
  }

  state.selected = next;
  paintSelection();
}

function sheetPointerUp(ev) {
  if (!drag) return;
  const was = drag;
  drag = null;
  $("#box").hidden = true;

  if (was.boxing) {
    renderSidebar();
  } else if (was.thumb) {
    if (state.byId.has(was.thumb)) clickThumb(ev, was.thumb);
  } else if (!was.toggle && !ev.shiftKey) {
    state.selected.clear();
    selectionChanged();
  }
}

// ---------------------------------------------------------------------------
// The sidebar
//
// Each field is built for the current selection, and keeps itself up to
// date as it's edited, rather than the whole sidebar being rebuilt on every
// keystroke, which would lose the focus.

function selectedPhotos() {
  return state.photos.filter(p => state.selected.has(p.id));
}

function distinct(photos, field) {
  const seen = new Map();
  for (const p of photos) {
    const v = current(p, field);
    seen.set(JSON.stringify(v), v);
  }
  return [...seen.values()];
}

// The frame of a field: a name, a revert button shown when it's dirty, the
// control, and a note.  refresh() recomputes dirtiness and the note.
function frame(name, control, { dirty, revert, note = () => "" }) {
  const noteNode = el("div", { class: "note" });
  const node = el("div", { class: "field" },
    el("div", { class: "name" },
      name,
      el("button", { type: "button", class: "revert", text: "revert", title: "undo unsaved changes to this field",
        onclick: () => { revert(); renderSheet(); renderSidebar(); } }),
    ),
    control,
    noteNode,
  );
  node.refresh = () => {
    node.classList.toggle("dirty", dirty());
    noteNode.textContent = note();
  };
  node.refresh();
  return node;
}

function fieldFrame(name, photos, field, control, noteFn = () => "") {
  return frame(name, control, {
    dirty:  () => photos.some(p => isEdited(p, field)),
    revert: () => revert(photos, field),
    note:   noteFn,
  });
}

// Notes for a single-valued field when several photos are selected: what
// editing it will do.
function uniformNote(photos, mixed) {
  if (photos.length < 2) return "";
  return mixed ? `These differ.  Setting this gives all ${photos.length} the same value.`
               : `Editing this changes all ${photos.length}.`;
}

function textField(name, photos, field, { multiline = false } = {}) {
  const values = distinct(photos, field);
  const mixed = values.length > 1;
  let wasMixed = mixed;

  const input = el(multiline ? "textarea" : "input", {
    type: multiline ? null : "text",
    placeholder: mixed ? `mixed: ${values.length} different values` : "",
  });
  input.value = mixed ? "" : values[0];

  const node = fieldFrame(name, photos, field, input, () => uniformNote(photos, wasMixed));
  node.classList.toggle("mixed", mixed);

  input.addEventListener("input", () => {
    change(photos, field, () => input.value);
    node.refresh();
  });
  return node;
}

function takenField(photos) {
  const values = distinct(photos, "taken");
  const mixed = values.length > 1;
  const input = el("input", {
    type: "text",
    placeholder: mixed ? `mixed: ${values.length} different values` : "2026-09-30T10:15:00, maybe with -04:00",
  });
  input.value = mixed ? "" : values[0];

  let invalid = false;
  const node = fieldFrame("Taken", photos, "taken", input,
    () => invalid ? "Not a date and time like 2026-09-30T10:15:00, with or without an offset like -04:00."
                  : uniformNote(photos, mixed));
  node.classList.toggle("mixed", mixed);

  const RE = /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(Z|[-+]\d\d:\d\d)?$/;
  input.addEventListener("input", () => {
    const v = input.value.trim();
    invalid = v !== "" && !RE.test(v);
    node.classList.toggle("invalid", invalid);
    if (!invalid) change(photos, "taken", () => v);
    node.refresh();
  });
  return node;
}

function visibilityField(photos) {
  const values = distinct(photos, "visibility");
  const mixed = values.length > 1;
  const select = el("select", {},
    mixed ? el("option", { value: "", text: "mixed", disabled: true }) : null,
    el("option", { value: "pending", text: "pending: not yet reviewed, so not published" }),
    el("option", { value: "public",   text: "public: published" }),
    el("option", { value: "unlisted", text: "unlisted: published, but only albums link to it" }),
    el("option", { value: "private", text: "private: kept, never published" }),
  );
  select.value = mixed ? "" : values[0];

  const node = fieldFrame("Visibility", photos, "visibility", select, () => uniformNote(photos, mixed));
  node.classList.toggle("mixed", mixed);
  select.addEventListener("change", () => {
    change(photos, "visibility", () => select.value);
    node.refresh();
  });
  return node;
}

function checkboxField(name, photos, field, text, extraNote = () => "") {
  const values = distinct(photos, field);
  const mixed = values.length > 1;
  const box = el("input", { type: "checkbox" });
  box.checked = !mixed && !!values[0];
  box.indeterminate = mixed;
  const isDirty = () => photos.some(p => isEdited(p, field));

  const node = fieldFrame(name, photos, field, el("label", { class: "check" }, box, text),
    () => [ extraNote(), mixed && !isDirty() ? `Mixed: ${photos.filter(p => current(p, field)).length} of ${photos.length}.` : "" ]
            .filter(Boolean).join("  "));
  node.classList.toggle("mixed", mixed);

  box.addEventListener("change", () => {
    change(photos, field, () => box.checked);
    node.refresh();
  });
  return node;
}

function rotateField(photos) {
  const describe = () => {
    const values = distinct(photos, "rotate");
    if (values.length > 1) return "mixed";
    return values[0] ? `${values[0]}° clockwise` : "none";
  };
  const value = el("span", { class: "value", text: describe() });
  const turn = (by) => () => {
    change(photos, "rotate", r => (((r + by) % 360) + 360) % 360);
    value.textContent = describe();
    node.refresh();
  };

  const node = fieldFrame("Extra rotation", photos, "rotate",
    el("div", { class: "row" },
      value,
      el("button", { type: "button", text: "⟲", title: "turn 90° counterclockwise", onclick: turn(-90) }),
      el("button", { type: "button", text: "⟳", title: "turn 90° clockwise", onclick: turn(90) }),
    ),
    () => [
      photos.length > 1 ? "Turns each of them from where it is." : "",
      photos.some(importing) ? "Its pictures are being made again, and will show when they're done." : "",
    ].filter(Boolean).join("  "),
  );
  return node;
}

// A field whose value is a set: tags, or albums.  It shows the union across
// the selection, each with how many have it, and each can be added to or
// removed from all of them, leaving the rest alone.  Members removed but not
// yet written are shown struck through, so they can be put back.
//
// labelOf gives a member's text, and resolve turns what was typed into a
// member, or null.  isNew says whether a member is one made here and not yet
// written, which is marked by CSS, so that copying the chip's text copies
// only the name.  With split, what's typed is split at commas into several:
// tags can't contain commas, but album titles often do.
function setField(photos, { name, field, labelOf = (v) => v, isNew = () => false, resolve, placeholder, list, split = false }) {
  const control = el("div");
  const node = fieldFrame(name, photos, field, control);
  const n = photos.length;

  const add = (v) => change(photos, field, vs => vs.includes(v) ? vs : [ ...vs, v ]);
  const remove = (v) => change(photos, field, vs => vs.filter(x => x !== v));
  const restore = (v) => change(photos, field, (vs, p) =>
    SAVED[field](p).includes(v) && !vs.includes(v) ? [ ...vs, v ] : vs);

  const input = el("input", { type: "text", list, placeholder: n > 1 ? `${placeholder} to all ${n}` : placeholder });
  const enter = () => {
    for (const text of split ? input.value.split(",") : [ input.value ]) {
      const v = resolve(text.trim());
      if (v !== null) add(v);
    }
    input.value = "";
    draw();
  };

  const draw = () => {
    const counts = new Map();
    for (const p of photos) for (const v of current(p, field)) counts.set(v, (counts.get(v) || 0) + 1);

    const removed = new Set();
    for (const p of photos) for (const v of SAVED[field](p)) if (!counts.has(v)) removed.add(v);

    const added = (v) => photos.some(p => current(p, field).includes(v) && !SAVED[field](p).includes(v));
    const members = [ ...counts.keys(), ...removed ]
      .sort((a, b) => labelOf(a).localeCompare(labelOf(b)));

    const chips = members.map(v => {
      if (removed.has(v)) {
        return el("span", { class: "chip removed" }, labelOf(v),
          el("button", { type: "button", text: "↺", title: "put it back", onclick: () => { restore(v); draw(); } }));
      }
      const c = counts.get(v);
      const partial = c < n;
      return el("span", { class: `chip${partial ? " partial" : ""}${added(v) ? " added" : ""}${isNew(v) ? " new" : ""}` },
        labelOf(v),
        n > 1
          ? el("button", { type: "button", class: "count", text: `${c}/${n}`,
              title: partial ? "add to all of them" : "all of them have it",
              disabled: !partial, onclick: () => { add(v); draw(); } })
          : null,
        el("button", { type: "button", text: "×", title: n > 1 ? "remove from all of them" : "remove",
          onclick: () => { remove(v); draw(); } }),
      );
    });

    control.replaceChildren(el("div", { class: "chips" }, chips), input);
    node.refresh();
  };

  input.addEventListener("keydown", (ev) => {
    if (ev.key === "Enter" || (split && ev.key === ",")) {
      ev.preventDefault();
      enter();
      input.focus();
    } else if (ev.key === "Backspace" && input.value === "" && n === 1) {
      const vs = current(photos[0], field);
      if (vs.length) { remove(vs[vs.length - 1]); draw(); input.focus(); }
    }
  });
  // Something typed but not entered is added when leaving the field.
  input.addEventListener("blur", () => { if (input.value.trim()) enter(); });

  draw();
  return node;
}

// Tags are lowercase; the server keeps them so.
function tagsField(photos) {
  return setField(photos, {
    name: "Tags", field: "tags", placeholder: "add a tag", list: "all-tags", split: true,
    resolve: (text) => text === "" ? null : text.toLowerCase(),
  });
}

// Albums are named by id, or, for one made here and not yet written, by a
// key ("new:1") that the write turns into an id.  Typing an album's title
// picks it; typing anything else makes a new album with that title.
function albumTitle(id) {
  if (state.newAlbums.has(id)) return state.newAlbums.get(id);
  const a = lib.albums.get(id);
  return a ? a.title : id;
}

function albumsField(photos) {
  return setField(photos, {
    name: "Albums", field: "albums", placeholder: "add to an album", list: "all-albums",
    labelOf: albumTitle,
    isNew: (id) => state.newAlbums.has(id),
    resolve: (text) => {
      if (text === "") return null;
      const lc = text.toLowerCase();
      for (const a of lib.albums.values()) if (a.title.toLowerCase() === lc || a.slug === text) return a.id;
      for (const [key, title] of state.newAlbums) if (title.toLowerCase() === lc) return key;
      const key = `new:${++state.newAlbumCount}`;
      state.newAlbums.set(key, text);
      refreshAlbumList();
      return key;
    },
  });
}

function refreshDatalist(id, values) {
  const list = el("datalist", { id }, values.map(value => el("option", { value })));
  const old = document.getElementById(id);
  if (old) old.replaceWith(list); else document.body.append(list);
}

// Every tag in the library, for suggestions.
function refreshTagList() {
  const tags = new Set();
  for (const p of lib.index.values()) for (const tag of p.tags) tags.add(tag);
  refreshDatalist("all-tags", [ ...tags ].sort((a, b) => a.localeCompare(b)));
}

function refreshAlbumList() {
  const titles = [ ...lib.albums.values() ].map(a => a.title).sort((a, b) => a.localeCompare(b));
  refreshDatalist("all-albums", [ ...titles, ...state.newAlbums.values() ]);
}

function preview(p) {
  const img = el("img", { src: renditionUrl(p, "500.webp"), alt: "" });
  const delta = rotationDelta(p);
  if (delta) img.style.transform = `rotate(${delta}deg)`;
  return el("div", { class: "preview" }, img);
}

// Starts a photo's failed import again.
async function retryImport(p) {
  const { blobId, name, modified } = p.importJob;
  setStatus("starting the import again…");
  try {
    const changed = await inTurn(async () => {
      const got = await jmap.call("Photo/set", { update: { [p.id]: { importJob: { blobId, name, modified } } } });
      const refused = got.notUpdated && got.notUpdated[p.id];
      if (refused) throw new Error(refused.description || refused.type);
      return sync();
    });
    setStatus("the import was started again");
    showChanges(changed);
  } catch (e) {
    setStatus(`starting the import again: ${e.message}`, true);
  }
}

function facts(p) {
  const o = p.original;
  return el("div", { class: "facts" },
    el("div", { text: `id ${p.id}` }),
    o ? el("div", { text: `${p.type}, ${o.width} × ${o.height}${o.duration ? `, ${o.duration.toFixed(1)} s` : ""}` }) : null,
    o && o.name ? el("div", { text: `file ${o.name}` }) : null,
    p.added ? el("div", { text: `added ${p.added}` }) : null,
    p.flickrId ? el("div", { text: `Flickr ${p.flickrId}` }) : null,
    importing(p) ? el("div", { text: "being imported: its pictures are being made" }) : null,
    importFailed(p) ? el("div", { class: "bad" }, `import failed: ${p.importJob.error} `,
      el("button", { type: "button", class: "text", text: "try again", onclick: () => retryImport(p) })) : null,
  );
}

// What went wrong when the selected photos were last written, if anything.
function troubles(photos) {
  const refused = photos.filter(p => state.errors.has(p.id));
  const changed = photos.filter(p => state.conflicts.has(p.id));
  if (!refused.length && !changed.length) return null;
  return el("div", { class: "field invalid" },
    refused.map(p => el("div", { class: "note", text: `${photoName(p)} was not written: ${state.errors.get(p.id)}` })),
    changed.flatMap(p => [ ...state.conflicts.get(p.id) ].map(field => el("div", { class: "note", text:
      `${photos.length > 1 ? `${photoName(p)}: ` : ""}${FIELD_NAMES[field]} was changed elsewhere, to ${shown(SAVED[field](p))}, while being edited here.  Writing keeps the edit; reverting it takes the other change.` }))),
  );
}

const FIELD_NAMES = {
  title: "the title", description: "the description", visibility: "the visibility", taken: "the time taken",
  rotate: "the rotation", location_private: "whether the location is private",
  cover: "the cover", order: "the order",
};

const shown = (value) => value === "" ? "nothing" : typeof value === "string" ? `“${value}”` : JSON.stringify(value);

// The album's own fields, shown when no photo is selected.
function albumTextField(name, field, { multiline = false, required = false } = {}) {
  const input = el(multiline ? "textarea" : "input", { type: multiline ? null : "text" });
  input.value = albumCurrent(field);

  let invalid = false;
  const node = frame(name, input, {
    dirty:  () => albumEdited(field),
    revert: () => albumChange(field, ALBUM_SAVED[field](theAlbum())),
    note:   () => invalid ? "It can't be empty." : "",
  });

  input.addEventListener("input", () => {
    invalid = required && input.value.trim() === "";
    node.classList.toggle("invalid", invalid);
    if (!invalid) albumChange(field, input.value);
    node.refresh();
  });
  return node;
}

function photoName(p) {
  return current(p, "title") || (p.original && p.original.name) || p.id;
}

function coverField() {
  const id = albumCurrent("cover");
  const p = id && state.byId.get(id);
  return frame("Cover", el("div", { class: "value", text: p ? photoName(p) : "none, so the first photo" }), {
    dirty:  () => albumEdited("cover"),
    revert: () => albumChange("cover", ALBUM_SAVED.cover(theAlbum())),
    note:   () => p && current(p, "visibility") === "private"
      ? "It's private, so the site shows the album's first published photo instead."
      : "To change it, select a photo and choose “Make cover”.",
  });
}

// Puts the album in order by when its photos were taken or added, oldest
// first (dir 1) or newest first (dir -1).  Photos without a date go last,
// and ties keep their places.
function sortAlbum(key, dir) {
  const when = (id) => {
    const p = state.byId.get(id);
    const t = Date.parse(key === "taken" ? current(p, "taken") : p.added);
    return Number.isNaN(t) ? null : t;
  };
  const order = albumCurrent("order");
  const dated   = order.filter(id => when(id) !== null).sort((a, b) => dir * (when(a) - when(b)));
  const undated = order.filter(id => when(id) === null);
  albumChange("order", [ ...dated, ...undated ]);
  applyOrder();
  renderSheet();
}

function orderField() {
  const sort = (text, title, key, dir) =>
    el("button", { type: "button", text, title, onclick: () => { sortAlbum(key, dir); node.refresh(); } });

  const node = frame("Order", el("div", { class: "sorts" },
    el("span", { text: "taken" }),
    sort("oldest", "sort by date taken, oldest first", "taken", 1),
    sort("newest", "sort by date taken, newest first", "taken", -1),
    el("span", { text: "added" }),
    sort("oldest", "sort by date added, oldest first", "added", 1),
    sort("newest", "sort by date added, newest first", "added", -1),
  ), {
    dirty:  () => albumEdited("order"),
    revert: () => { albumChange("order", ALBUM_SAVED.order(theAlbum())); applyOrder(); },
    note:   () => "Sorting puts photos without a date last.  To move some to the start or end, select them.",
  });
  return node;
}

function albumPanel() {
  const album = theAlbum();
  if (!albumEditable()) {
    return [
      el("h2", { text: "Album" }),
      el("p", { class: "empty", text:
        "This album has photos that aren't in the batch: they were added to it since, or are still being imported.  So the album itself can't be edited here.  Refresh to edit it." }),
    ];
  }
  const refused = state.errors.get(`album:${album.id}`);
  return [
    el("h2", { text: `Album: ${album.slug}` }),
    refused ? el("div", { class: "field invalid" }, el("div", { class: "note", text: `The album was not written: ${refused}` })) : null,
    state.albumConflicts.size ? el("div", { class: "field invalid" }, [ ...state.albumConflicts ].map(field =>
      el("div", { class: "note", text: `The album: ${FIELD_NAMES[field]} was changed elsewhere while being edited here.  Writing keeps the edit; reverting it takes the other change.` }))) : null,
    albumTextField("Title", "title", { required: true }),
    albumTextField("Description (Markdown)", "description", { multiline: true }),
    coverField(),
    orderField(),
  ].filter(Boolean);
}

// Moves the selected photos, in their order, to the start or end of the
// album.
function moveSelected(toStart) {
  const order = albumCurrent("order");
  const moving = order.filter(id => state.selected.has(id));
  const rest   = order.filter(id => !state.selected.has(id));
  albumChange("order", toStart ? [ ...moving, ...rest ] : [ ...rest, ...moving ]);
  applyOrder();
  renderSheet();
}

// The album controls for the selection.
function albumSelectionField(photos) {
  const one = photos.length === 1 ? photos[0] : null;
  const isCover = one && albumCurrent("cover") === one.id;

  const node = frame("Album", el("div", { class: "row" },
    el("button", { type: "button", class: "text", text: "Move to start", onclick: () => { moveSelected(true); node.refresh(); } }),
    el("button", { type: "button", class: "text", text: "Move to end", onclick: () => { moveSelected(false); node.refresh(); } }),
    one ? el("button", { type: "button", class: "text", text: "Make cover", disabled: isCover || !inAlbum(one),
      onclick: () => { albumChange("cover", one.id); refreshThumbs(); renderSidebar(); } }) : null,
  ), {
    dirty:  () => albumEdited("order") || albumEdited("cover"),
    revert: () => {
      albumChange("order", ALBUM_SAVED.order(theAlbum()));
      albumChange("cover", ALBUM_SAVED.cover(theAlbum()));
      applyOrder();
    },
    note:   () => isCover ? "This is the album's cover." : "",
  });
  return node;
}

function refreshThumbs() {
  for (const p of state.photos) refreshThumb(p.id);
}

function renderSidebar() {
  if (state.query === null) return;
  const side = $("#sidebar");
  const photos = selectedPhotos();

  if (!photos.length) {
    side.replaceChildren(
      ...(theAlbum() ? albumPanel() : []),
      el("h2", { text: `${state.photos.length} photo(s) in this batch` }),
      el("p", { class: "empty", text:
        "Click a photo to select it.  ⌘-click adds or removes one, shift-click selects a range, and dragging selects everything the box touches.  ⌘A selects all; Escape selects none." }),
    );
    return;
  }

  const one = photos.length === 1 ? photos[0] : null;
  const located = photos.filter(p => p.location);

  const fields = [
    textField("Title", photos, "title"),
    textField("Description (Markdown)", photos, "description", { multiline: true }),
    tagsField(photos),
    albumsField(photos),
    visibilityField(photos),
    takenField(photos),
    located.length
      ? checkboxField("Location", located, "location_private", "keep the location private",
          () => located.length < photos.length ? `${located.length} of ${photos.length} have a location.` : "")
      : null,
    rotateField(photos),
  ];

  side.replaceChildren(...[
    el("h2", { text: one ? ((one.original && one.original.name) || one.id) : `${photos.length} photos selected` }),
    one ? preview(one) : null,
    troubles(photos),
    albumEditable() ? albumSelectionField(photos) : null,
    ...fields,
    one ? facts(one) : null,
  ].filter(Boolean));
}

// ---------------------------------------------------------------------------
// Writing

const unwritten = () => state.edits.size > 0 || albumDirty();

function refreshWriteButton() {
  const n = state.edits.size;
  const b = $("#write");
  b.disabled = state.frozen || !unwritten();
  $("#refresh").disabled = state.frozen || state.query === null || unwritten();
  const what = [ n ? `${n}` : null, albumDirty() ? "album" : null ].filter(Boolean).join(" + ");
  b.textContent = what ? `Write changes (${what})` : "Write changes";
}

function freeze(on) {
  state.frozen = on;
  document.body.classList.toggle("frozen", on);
  refreshWriteButton();
}

// What to ask of the server to make the edits real: the updates of a
// Photo/set, by id, and the create and update of an Album/set.  Whatever
// is in conflict is left out.
function writePlan() {
  const photos = {};
  const moves = [];
  for (const p of state.photos) {
    const e = state.edits.get(p.id);
    if (!e || state.conflicts.has(p.id)) continue;
    const patch = photoPatch(p, e);
    if (Object.keys(patch).length) photos[p.id] = patch;
    if ("albums" in e) moves.push({ id: p.id, from: SAVED.albums(p), to: e.albums });
  }
  const editing = state.album !== null && albumDirty() && !state.albumConflicts.size
    ? { id: state.album, edits: state.albumEdits } : null;
  return { photos, albums: albumWrites({ albums: lib.albums, moves, newAlbums: state.newAlbums, editing }) };
}

const refusal = (error) => error.description || error.type;

// Sends one round of writes, and returns what came of it: how many photos
// and albums were written, and which type, if any, was refused because its
// state had moved on, and so wasn't written at all.
//
// Each object is written or refused by itself.  One that's written has its
// edits cleared, and one that's refused keeps them, with the reason.
async function sendWrites(plan) {
  const out = { photos: 0, albums: 0, stale: { Photo: false, Album: false } };
  const calls = [];

  const { create, update } = plan.albums;
  const albumCalls = Object.keys(create).length + Object.keys(update).length;
  if (albumCalls) calls.push([ "Album/set", { ifInState: lib.states.Album, create, update } ]);

  // Only the first of several Photo/set calls can say what state it
  // expects, since each changes it for the next.
  const sets = chunks(Object.keys(plan.photos), jmap.maxObjectsInSet);
  sets.forEach((ids, i) => calls.push([ "Photo/set", {
    ...(i === 0 ? { ifInState: lib.states.Photo } : {}),
    update: Object.fromEntries(ids.map(id => [ id, plan.photos[id] ])),
  } ]));

  const results = await jmap.request(calls, { every: false });
  results.forEach((got, i) => {
    const type = calls[i][0].split("/")[0];
    if (got instanceof MethodError) {
      if (got.type !== "stateMismatch") throw got;
      out.stale[type] = true;
      return;
    }

    if (type === "Album") {
      for (const [key, made] of Object.entries(got.created ?? {})) {
        for (const e of state.edits.values()) {
          if (e.albums) e.albums = e.albums.map(a => a === key ? made.id : a);
        }
        state.newAlbums.delete(key);
        out.albums++;
      }
      for (const id of Object.keys(got.updated ?? {})) {
        if (id === state.album) state.albumEdits = {};
        state.errors.delete(`album:${id}`);
        out.albums++;
      }
      for (const [key, error] of Object.entries(got.notCreated ?? {})) {
        out.refused = `the album “${state.newAlbums.get(key)}” was not made: ${refusal(error)}`;
      }
      for (const [id, error] of Object.entries(got.notUpdated ?? {})) {
        state.errors.set(`album:${id}`, refusal(error));
        out.refused = `the album “${albumTitle(id)}” was not written: ${refusal(error)}`;
      }
      return;
    }

    for (const id of Object.keys(got.updated ?? {})) {
      // The edits to its albums are the albums' to clear.
      const e = state.edits.get(id);
      const kept = "albums" in e ? { albums: e.albums } : null;
      if (kept) state.edits.set(id, kept); else state.edits.delete(id);
      state.errors.delete(id);
      out.photos++;
    }
    for (const [id, error] of Object.entries(got.notUpdated ?? {})) state.errors.set(id, refusal(error));
  });
  return out;
}

// Writes the edits, and returns what came of it, for the status line.
//
// A write says what state it expects each type to be in, and is refused if
// the state has moved on.  Then the page catches up, which finds any
// conflicts with what it was writing, and it tries again with whatever
// isn't in conflict.
//
// The conflicts there were when the write was asked for have been on show,
// so writing is taken to mean that the edits should win.  Any found while
// writing have not been seen, and are left unwritten, and shown.
async function writeAll() {
  const total = { photos: 0, albums: 0, refused: null, conflicts: 0 };
  state.conflicts.clear();
  state.albumConflicts.clear();
  await sync();

  for (let tries = 0; tries < 4; tries++) {
    const plan = writePlan();
    if (!Object.keys(plan.photos).length && !Object.keys(plan.albums.create).length
      && !Object.keys(plan.albums.update).length) break;

    const sent = await sendWrites(plan);
    total.photos += sent.photos;
    total.albums += sent.albums;
    total.refused = sent.refused ?? total.refused;

    await sync();
    if (!sent.stale.Photo && !sent.stale.Album) break;
  }
  total.conflicts = state.conflicts.size + (state.albumConflicts.size ? 1 : 0);
  return total;
}

async function write() {
  if (state.frozen || !unwritten()) return;
  if (document.activeElement) document.activeElement.blur();

  freeze(true);
  setStatus("writing…");

  let did;
  try {
    did = await inTurn(writeAll);
  } catch (e) {
    setStatus(`writing failed: ${e.message}`, true);
    return;
  } finally {
    freeze(false);
    refreshTagList();
    refreshAlbumList();
    refreshWaiting();
    applyOrder();
    if (state.query !== null) { renderSheet(); renderSidebar(); }
    refreshWriteButton();
  }

  const wrote = [
    did.photos ? `${did.photos} photo(s)` : null,
    did.albums ? `${did.albums} album(s)` : null,
  ].filter(Boolean).join(" and ");
  const refused = [ ...state.errors.keys() ].filter(key => !key.startsWith("album:")).length;
  const problems = [
    did.conflicts ? `${did.conflicts} of the things edited here were changed elsewhere meanwhile, and weren't written; look them over, and write again` : null,
    refused ? `${refused} photo(s) were refused` : null,
    did.refused,
  ].filter(Boolean);

  setStatus([ wrote ? `written: ${wrote}` : (problems.length ? "nothing written" : "nothing needed changing"), ...problems ].join("; "),
    problems.length > 0);
}

// ---------------------------------------------------------------------------

function keydown(ev) {
  if ((ev.metaKey || ev.ctrlKey) && ev.key === "s") {
    ev.preventDefault();
    write();
    return;
  }

  if (ev.target.closest("input, textarea, select") || state.query === null) return;

  if ((ev.metaKey || ev.ctrlKey) && ev.key === "a") {
    ev.preventDefault();
    state.selected = new Set(state.photos.map(p => p.id));
    selectionChanged();
  } else if (ev.key === "Escape") {
    state.selected.clear();
    selectionChanged();
  }
}

// ---------------------------------------------------------------------------
// The album list, shown when there's no query

async function showAlbums({ push = true } = {}) {
  if (!mayDiscard()) return;

  try {
    await inTurn(sync);
  } catch (e) {
    setStatus(`loading the albums: ${e.message}`, true);
    return;
  }

  discardEdits();
  Object.assign(state, { query: null, ask: null, photos: [], byId: new Map(), album: null, selected: new Set(), anchor: null });
  if (push) history.pushState({ q: null }, "", "/");
  else history.replaceState({ q: null }, "", "/");

  $("#query").value = "";
  document.title = "jiggle: albums";
  setStatus("");
  refreshWaiting();
  renderAlbumList();
  refreshWriteButton();
}

// Every album, for the album list, sorted by title: with its cover (or its
// first photo), and how many of its photos have each visibility.  Albums
// with nothing published, which the site leaves out, are included.
function albumOverview() {
  return [ ...lib.albums.values() ].map(a => {
    const counts = { public: 0, unlisted: 0, pending: 0, private: 0 };
    for (const id of a.photoIds) {
      const p = lib.index.get(id);
      if (p) counts[p.visibility]++;
    }
    return { slug: a.slug, title: a.title, cover: a.coverPhotoId ?? a.photoIds[0] ?? null, counts };
  }).sort((a, b) => a.title.toLowerCase().localeCompare(b.title.toLowerCase()));
}

// Finds the thumbnails of the albums' covers, which the page doesn't know
// until it asks, and puts each in its card.
async function showCovers(albums) {
  const unknown = [ ...new Set(albums.map(a => a.cover).filter(id => id !== null && !lib.thumbs.has(id))) ];
  if (unknown.length) {
    const got = await inTurn(() => jmap.getAll("Photo", unknown, [ "renditions" ]));
    for (const p of got.list) lib.thumbs.set(p.id, p.renditions["h480.webp"]?.blobId ?? null);
  }
  for (const node of document.querySelectorAll(".album[data-cover]")) {
    const blobId = lib.thumbs.get(node.dataset.cover);
    const slot = node.querySelector(".nocover");
    if (blobId && slot) slot.replaceWith(el("img", { src: jmap.blobUrl(blobId, "h480.webp", "image/webp"), loading: "lazy", alt: "" }));
  }
}

function renderAlbumList() {
  const albums = albumOverview();
  const counts = ({ counts: c }) => [
    c.public   ? `${c.public} published`  : null,
    c.unlisted ? `${c.unlisted} unlisted` : null,
    c.pending  ? `${c.pending} pending`   : null,
    c.private  ? `${c.private} private`   : null,
  ].filter(Boolean).join(" · ") || "empty";

  const cards = albums.map(a => el("a", {
      class: "album", href: `/?q=album:${encodeURIComponent(a.slug)}`, "data-title": a.title.toLowerCase(),
      "data-cover": a.cover,
      onclick: (ev) => { ev.preventDefault(); runQuery(`album:${a.slug}`); },
    },
    el("div", { class: "nocover" }),
    el("div", { class: "title", text: a.title }),
    el("div", { class: `counts${a.counts.public ? "" : " unpublished"}`, text: counts(a) }),
  ));

  const sheet = $("#sheet");
  sheet.classList.add("albums");
  sheet.replaceChildren(...cards);
  showCovers(albums).catch((e) => setStatus(`loading the covers: ${e.message}`, true));

  const filter = el("input", { type: "search", placeholder: "filter by title" });
  filter.addEventListener("input", () => {
    const want = filter.value.trim().toLowerCase();
    for (const card of sheet.querySelectorAll(".album")) card.hidden = !card.dataset.title.includes(want);
  });

  const quick = (q) => el("button", { type: "button", class: "text", text: q, onclick: () => runQuery(q) });
  $("#sidebar").replaceChildren(
    el("h2", { text: `${albums.length} album(s)` }),
    el("div", { class: "field" }, filter),
    el("p", { class: "empty", text:
      "Albums with nothing published are listed too, though the site leaves them out.  Open one to edit it, or type a query above." }),
    el("div", { class: "row quick" }, quick("pending limit:50"), quick("private")),
  );
}

// ---------------------------------------------------------------------------

function go(q) {
  q = q.trim();
  return q === "" ? showAlbums() : runQuery(q);
}

async function start() {
  const sheet = $("#sheet");
  sheet.addEventListener("pointerdown", sheetPointerDown);
  sheet.addEventListener("pointermove", sheetPointerMove);
  sheet.addEventListener("pointerup", sheetPointerUp);
  document.addEventListener("keydown", keydown);
  $("#write").addEventListener("click", write);
  $("#refresh").addEventListener("click", refresh);
  $("#albums").addEventListener("click", () => showAlbums());
  $("#query-form").addEventListener("submit", (ev) => { ev.preventDefault(); go($("#query").value); });

  window.addEventListener("beforeunload", (ev) => {
    if (unwritten()) { ev.preventDefault(); ev.returnValue = ""; }
  });

  // Back and Forward move between queries.  Unwritten edits are kept if
  // the move is declined, though the address bar has already moved.
  window.addEventListener("popstate", () => {
    const q = new URLSearchParams(location.search).get("q");
    if (q) runQuery(q, { push: false });
    else showAlbums({ push: false });
  });

  setStatus("loading…");
  try {
    jmap = await Client.connect(CAPABILITY);
    await inTurn(loadLibrary);
  } catch (e) {
    setStatus(`can't reach the server: ${e.message}`, true);
    return;
  }
  jmap.watch(heard);

  const q = new URLSearchParams(location.search).get("q");
  if (q) await runQuery(q, { push: false });
  else await showAlbums({ push: false });
}

start();
