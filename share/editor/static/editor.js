// jiggle's editor: a contact sheet of a batch chosen by a query, and a
// sidebar for editing the selected photos; or, with no query, a list of the
// library's albums.  -- claude, 2026-09-30
//
// The batch belongs to the page: the server answers a query with photos,
// and the page keeps them until it asks again (a new query, or Refresh), so
// photos don't drop out of the batch as they're edited.
//
// The page keeps each photo as it was loaded from disk ("saved"), and a map
// of edits: for each photo, the fields whose values differ from saved.  A
// field is dirty when any selected photo has an edit for it.  Writing sends
// the edits, and on success the written photos come back as the new saved
// state, with their edits cleared.
"use strict";

const state = {
  query:    null,        // the batch's query, or null for the album list
  photos:   [],          // the batch, in order, as loaded from disk
  byId:     new Map(),
  albums:   [],          // every album in the library: { slug, title }
  tags:     [],          // every tag in the library, for suggestions
  edits:    new Map(),   // id => { field: value }
  selected: new Set(),
  anchor:   null,        // where a shift-click range starts
  newAlbums: new Map(),  // key => title, for albums made here and not yet written
  newAlbumCount: 0,
  album:    null,        // the album being edited, if the batch is one
  albumEdits: {},        // field => value, like a photo's edits
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

// How each field is read from a photo as saved.  location_private exists
// only for photos with a location.
const SAVED = {
  title:            p => p.title,
  description:      p => p.description,
  tags:             p => p.tags,
  albums:           p => p.albums,
  visibility:       p => p.visibility,
  taken:            p => p.taken ?? "",
  rotate:           p => p.rotate,
  location_private: p => p.location ? p.location.private : undefined,
};

const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);

// Tags and albums are sets: a tag removed and put back is no change, though
// it would now be last.
const SET_FIELDS = new Set([ "tags", "albums" ]);
const sameValue = (field, a, b) => SET_FIELDS.has(field)
  ? a.length === b.length && a.every(x => b.includes(x))
  : same(a, b);

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
  }
  for (const p of photos) refreshThumb(p.id);
  refreshWriteButton();
}

function revert(photos, field) {
  change(photos, field, (_, p) => SAVED[field](p));
}

// The album, when the batch is one: like a photo, it's kept as saved, with
// a set of edits.  Its order is the order of the sheet.
const ALBUM_SAVED = {
  title:       a => a.title,
  description: a => a.description,
  cover:       a => a.cover ?? null,
  order:       a => a.photos,
};

function albumCurrent(field) {
  return field in state.albumEdits ? state.albumEdits[field] : ALBUM_SAVED[field](state.album);
}

function albumChange(field, value) {
  if (same(value, ALBUM_SAVED[field](state.album))) delete state.albumEdits[field];
  else state.albumEdits[field] = value;
  refreshWriteButton();
}

const albumDirty = () => Object.keys(state.albumEdits).length > 0;
const albumEdited = (field) => field in state.albumEdits;

// The album can be edited only if every photo in it is in the batch: one
// added to it since the editor started can't be placed in an order.
function albumEditable() {
  return !!state.album && state.album.photos.every(id => state.byId.has(id));
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

const inAlbum = (p) => !state.album || current(p, "albums").includes(state.album.slug);

// Fetches a batch, returning its data, or throwing the server's complaint.
async function fetchBatch(params) {
  const res = await fetch(`/api/batch?${new URLSearchParams(params)}`);
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || `loading the batch: ${res.status}`);
  return data;
}

function adopt(data) {
  state.query  = data.query;
  state.photos = data.photos;
  state.byId   = new Map(data.photos.map(p => [p.id, p]));
  state.albums = data.albums;
  state.tags   = data.tags;
  state.album  = data.album || null;
  applyOrder();

  for (const id of [...state.selected]) {
    if (!state.byId.has(id)) state.selected.delete(id);
  }
  refreshTagList();
  refreshAlbumList();
}

// Asks whether unwritten edits can be thrown away, if there are any.
function mayDiscard() {
  if (!unwritten()) return true;
  return confirm("You have changes that aren't written.  Discard them?");
}

function discardEdits() {
  state.edits.clear();
  state.albumEdits = {};
  state.newAlbums.clear();
}

// Shows the batch a query picks.  With keep, the selection is kept for
// photos still in it, as for Refresh.
async function runQuery(q, { push = true, keep = false } = {}) {
  if (!mayDiscard()) return;

  let data;
  setStatus("loading…");
  try {
    data = await fetchBatch({ q });
  } catch (e) {
    setStatus(e.message, true);
    return;
  }

  discardEdits();
  if (!keep) { state.selected.clear(); state.anchor = null; }
  adopt(data);

  const url = `/?${new URLSearchParams({ q: data.query })}`;
  if (push) history.pushState({ q: data.query }, "", url);
  else history.replaceState({ q: data.query }, "", url);

  $("#query").value = data.query;
  document.title = `jiggle: ${data.query}`;
  setStatus(`${data.photos.length} photo(s)`);
  renderSheet();
  renderSidebar();
  refreshWriteButton();
}

function refresh() {
  if (state.query === null || unwritten()) return;
  runQuery(state.query, { push: false, keep: true });
}

function replaceSaved(record) {
  const i = state.photos.findIndex(p => p.id === record.id);
  if (i >= 0) state.photos[i] = record;
  state.byId.set(record.id, record);
}

// ---------------------------------------------------------------------------
// The contact sheet

const THUMB_H = 150;

// Rendition URLs carry the metadata's version, so that renditions remade
// after a rotation is written are fetched again rather than taken from the
// browser's cache.
//
// The extra turn an unsaved rotation adds, previewed with CSS.
function rotationDelta(p) {
  return (((current(p, "rotate") - p.rotate) % 360) + 360) % 360;
}

function thumbFor(p) {
  const delta = rotationDelta(p);
  const ratio = p.width / p.height;

  // The image is laid out as saved, then turned; the frame is the size it
  // takes up once turned.
  let imgW = Math.round(THUMB_H * ratio), imgH = THUMB_H;
  if (delta % 180) { imgW = THUMB_H; imgH = Math.round(THUMB_H / ratio); }
  const frameW = delta % 180 ? imgH : imgW;

  const img = el("img", { src: `/r/${p.id}/h480.webp?v=${p.version}`, loading: "lazy", alt: "" });
  Object.assign(img.style, {
    width: `${imgW}px`, height: `${imgH}px`,
    left: `${(frameW - imgW) / 2}px`, top: `${(THUMB_H - imgH) / 2}px`,
    transform: delta ? `rotate(${delta}deg)` : "",
  });

  const title = current(p, "title");
  const node = el("figure", { class: "thumb", "data-id": p.id, style: `width: ${frameW + 10}px` },
    el("div", { class: "frame", style: `width: ${frameW}px` }, img),
    el("div", { class: "badges" },
      state.album && albumCurrent("cover") === p.id ? el("span", { class: "badge cover", text: "cover" }) : null,
      inAlbum(p) ? null : el("span", { class: "badge out", text: "out of album" }),
      current(p, "visibility") === "pending" ? el("span", { class: "badge pending", text: "pending" }) : null,
      current(p, "visibility") === "private" ? el("span", { class: "badge", text: "private" }) : null,
      p.type === "video" ? el("span", { class: "badge", text: "▶" }) : null,
    ),
    el("figcaption", { text: title || p.file || p.id }),
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
    clickThumb(ev, was.thumb);
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
    el("option", { value: "public",  text: "public: published" }),
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
    () => photos.length > 1 ? "Turns each of them from where it is." : "",
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

// Tags are lowercase; see Jiggle::Photo.
function tagsField(photos) {
  return setField(photos, {
    name: "Tags", field: "tags", placeholder: "add a tag", list: "all-tags", split: true,
    resolve: (text) => text === "" ? null : text.toLowerCase(),
  });
}

// Albums are named by slug, or, for one made here and not yet written, by a
// key ("new:1") that the write turns into a slug.  Typing an album's title
// picks it; typing anything else makes a new album with that title.
function albumTitle(slug) {
  if (state.newAlbums.has(slug)) return state.newAlbums.get(slug);
  const a = state.albums.find(a => a.slug === slug);
  return a ? a.title : slug;
}

function albumsField(photos) {
  return setField(photos, {
    name: "Albums", field: "albums", placeholder: "add to an album", list: "all-albums",
    labelOf: albumTitle,
    isNew: (slug) => state.newAlbums.has(slug),
    resolve: (text) => {
      if (text === "") return null;
      const lc = text.toLowerCase();
      const found = state.albums.find(a => a.title.toLowerCase() === lc || a.slug === text);
      if (found) return found.slug;
      for (const [key, title] of state.newAlbums) if (title.toLowerCase() === lc) return key;
      const key = `new:${++state.newAlbumCount}`;
      state.newAlbums.set(key, text);
      refreshAlbumList();
      return key;
    },
  });
}

function refreshTagList() {
  const list = el("datalist", { id: "all-tags" }, state.tags.map(t => el("option", { value: t })));
  const old = document.getElementById("all-tags");
  if (old) old.replaceWith(list); else document.body.append(list);
}

function refreshAlbumList() {
  const titles = [ ...state.albums.map(a => a.title), ...state.newAlbums.values() ];
  const list = el("datalist", { id: "all-albums" }, titles.map(t => el("option", { value: t })));
  const old = document.getElementById("all-albums");
  if (old) old.replaceWith(list); else document.body.append(list);
}

function preview(p) {
  const img = el("img", { src: `/r/${p.id}/500.webp?v=${p.version}`, alt: "" });
  const delta = rotationDelta(p);
  if (delta) img.style.transform = `rotate(${delta}deg)`;
  return el("div", { class: "preview" }, img);
}

function facts(p) {
  return el("div", { class: "facts" },
    el("div", { text: `id ${p.id}` }),
    el("div", { text: `${p.type}, ${p.width} × ${p.height}${p.duration ? `, ${p.duration.toFixed(1)} s` : ""}` }),
    p.file ? el("div", { text: `file ${p.file}` }) : null,
    p.added ? el("div", { text: `added ${p.added}` }) : null,
    p.flickr_id ? el("div", { text: `Flickr ${p.flickr_id}` }) : null,
  );
}

// The album's own fields, shown when no photo is selected.
function albumTextField(name, field, { multiline = false, required = false } = {}) {
  const input = el(multiline ? "textarea" : "input", { type: multiline ? null : "text" });
  input.value = albumCurrent(field);

  let invalid = false;
  const node = frame(name, input, {
    dirty:  () => albumEdited(field),
    revert: () => albumChange(field, ALBUM_SAVED[field](state.album)),
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
  return current(p, "title") || p.file || p.id;
}

function coverField() {
  const id = albumCurrent("cover");
  const p = id && state.byId.get(id);
  return frame("Cover", el("div", { class: "value", text: p ? photoName(p) : "none, so the first photo" }), {
    dirty:  () => albumEdited("cover"),
    revert: () => albumChange("cover", ALBUM_SAVED.cover(state.album)),
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
    revert: () => { albumChange("order", ALBUM_SAVED.order(state.album)); applyOrder(); },
    note:   () => "Sorting puts photos without a date last.  To move some to the start or end, select them.",
  });
  return node;
}

function albumPanel() {
  if (!albumEditable()) {
    return [
      el("h2", { text: "Album" }),
      el("p", { class: "empty", text:
        "Photos have been added to this album since the editor started, so the album itself can't be edited here.  Restart the editor to edit it." }),
    ];
  }
  return [
    el("h2", { text: `Album: ${state.album.slug}` }),
    albumTextField("Title", "title", { required: true }),
    albumTextField("Description (Markdown)", "description", { multiline: true }),
    coverField(),
    orderField(),
  ];
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
      albumChange("order", ALBUM_SAVED.order(state.album));
      albumChange("cover", ALBUM_SAVED.cover(state.album));
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
  const side = $("#sidebar");
  const photos = selectedPhotos();

  if (!photos.length) {
    side.replaceChildren(
      ...(state.album ? albumPanel() : []),
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
    el("h2", { text: one ? (one.file || one.id) : `${photos.length} photos selected` }),
    one ? preview(one) : null,
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
  $("#note").disabled = on;
  refreshWriteButton();
}

async function write() {
  if (state.frozen || !unwritten()) return;
  if (document.activeElement) document.activeElement.blur();

  // Album membership goes as additions and removals, which the server merges
  // with the album files as they are then.
  const used = new Set();
  const sent = [ ...state.edits.entries() ].map(([id, edits]) => {
    const p = state.byId.get(id);
    const changes = { ...edits };
    if ("albums" in changes) {
      const was = SAVED.albums(p);
      changes.albums = {
        add:    changes.albums.filter(a => !was.includes(a)),
        remove: was.filter(a => !changes.albums.includes(a)),
      };
      for (const a of changes.albums.add) if (state.newAlbums.has(a)) used.add(a);
    }
    return { id, version: p.version, changes };
  });
  const newAlbums = [ ...used ].map(key => ({ key, title: state.newAlbums.get(key) }));
  // In album mode, the album is always named, so it comes back as written,
  // with any photos taken out of it; its version is checked only if it has
  // changes.
  const album = state.album
    ? { slug: state.album.slug, version: state.album.version, changes: { ...state.albumEdits } }
    : undefined;
  const albumChanged = albumDirty();

  freeze(true);
  setStatus(`writing ${sent.length} photo(s)${albumChanged ? " and the album" : ""}…`);

  let res, data;
  try {
    res = await fetch("/api/write", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ note: $("#note").value, photos: sent, new_albums: newAlbums, album }),
    });
    data = await res.json();
  } catch (e) {
    freeze(false);
    setStatus(`writing failed: ${e.message}`, true);
    return;
  }

  freeze(false);

  if (res.status === 409) {
    const what = [
      data.conflicts.length ? `${data.conflicts.length} photo(s)` : null,
      data.album_conflict ? "the album" : null,
    ].filter(Boolean).join(" and ");
    setStatus(`Nothing written: ${what} changed on disk since loading.  `, true,
      el("button", { type: "button", text: "discard my edits to those and reload them",
        onclick: () => reloadPhotos(data.conflicts, data.album_conflict) }));
    return;
  }
  if (!res.ok) {
    setStatus(`Nothing written: ${data.error || res.status}`, true);
    return;
  }

  // Edits made while writing are impossible (the page is frozen), so every
  // photo sent is now as written, or was already as asked.
  for (const record of data.photos) replaceSaved(record);
  for (const { id } of sent) state.edits.delete(id);
  state.albums = data.albums;
  state.newAlbums.clear();
  if (data.album !== undefined) {
    state.album = data.album;
    state.albumEdits = {};
    applyOrder();
  }
  refreshAlbumList();
  $("#note").value = "";

  setStatus(data.commit
    ? `committed ${data.commit}: ${[ data.photos.length ? `${data.photos.length} photo(s)` : null, albumChanged ? "the album" : null ].filter(Boolean).join(" and ")}`
    : "nothing needed changing");
  renderSheet();
  renderSidebar();
  refreshWriteButton();
}

async function reloadPhotos(ids, album = false) {
  let photos, albumData;
  try {
    if (ids.length) photos = (await fetchBatch({ ids: ids.join(",") })).photos;
    if (album) albumData = (await fetchBatch({ q: `album:${state.album.slug}` })).album;
  } catch (e) {
    setStatus(`reloading failed: ${e.message}`, true);
    return;
  }
  const fresh = new Map((photos || []).map(p => [p.id, p]));
  for (const id of ids) {
    if (fresh.has(id)) replaceSaved(fresh.get(id));
    state.edits.delete(id);
  }
  if (album) {
    state.album = albumData || null;
    state.albumEdits = {};
    applyOrder();
  }
  setStatus(`reloaded ${[ ids.length ? `${ids.length} photo(s)` : null, album ? "the album" : null ]
    .filter(Boolean).join(" and ")} from disk`);
  renderSheet();
  renderSidebar();
  refreshWriteButton();
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

  let res, data;
  try {
    res = await fetch("/api/albums");
    data = await res.json();
    if (!res.ok) throw new Error(data.error || res.status);
  } catch (e) {
    setStatus(`loading the albums: ${e.message}`, true);
    return;
  }

  discardEdits();
  Object.assign(state, { query: null, photos: [], byId: new Map(), album: null, selected: new Set(), anchor: null });
  if (push) history.pushState({ q: null }, "", "/");
  else history.replaceState({ q: null }, "", "/");

  $("#query").value = "";
  document.title = "jiggle: albums";
  setStatus("");
  renderAlbumList(data.albums);
  refreshWriteButton();
}

function renderAlbumList(albums) {
  const counts = (a) => [
    a.published ? `${a.published} published` : null,
    a.pending   ? `${a.pending} pending`     : null,
    a.private   ? `${a.private} private`     : null,
  ].filter(Boolean).join(" · ") || "empty";

  const cards = albums.map(a => el("a", {
      class: "album", href: `/?q=album:${encodeURIComponent(a.slug)}`, "data-title": a.title.toLowerCase(),
      onclick: (ev) => { ev.preventDefault(); runQuery(`album:${a.slug}`); },
    },
    a.cover ? el("img", { src: `/r/${a.cover}/h480.webp`, loading: "lazy", alt: "" }) : el("div", { class: "nocover" }),
    el("div", { class: "title", text: a.title }),
    el("div", { class: `counts${a.published ? "" : " unpublished"}`, text: counts(a) }),
  ));

  const sheet = $("#sheet");
  sheet.classList.add("albums");
  sheet.replaceChildren(...cards);

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

  const q = new URLSearchParams(location.search).get("q");
  if (q) await runQuery(q, { push: false });
  else await showAlbums({ push: false });
}

start();
