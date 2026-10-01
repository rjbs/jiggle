// jiggle's editor: a contact sheet of one batch, and a sidebar for editing
// the selected photos.  -- claude, 2026-09-30
//
// The page keeps each photo as it was loaded from disk ("saved"), and a map
// of edits: for each photo, the fields whose values differ from saved.  A
// field is dirty when any selected photo has an edit for it.  Writing sends
// the edits, and on success the written photos come back as the new saved
// state, with their edits cleared.
"use strict";

const state = {
  label:    "",
  photos:   [],          // the batch, in order, as loaded from disk
  byId:     new Map(),
  albums:   [],          // every album in the library: { slug, title }
  tags:     [],          // every tag in the library, for suggestions
  edits:    new Map(),   // id => { field: value }
  selected: new Set(),
  anchor:   null,        // where a shift-click range starts
  newAlbums: new Map(),  // key => title, for albums made here and not yet written
  newAlbumCount: 0,
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
  pending:          p => p.pending,
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

async function load() {
  const res = await fetch("/api/batch");
  if (!res.ok) throw new Error(`loading the batch: ${res.status}`);
  const data = await res.json();

  state.label  = data.label;
  state.photos = data.photos;
  state.byId   = new Map(data.photos.map(p => [p.id, p]));
  state.albums = data.albums;
  state.tags   = data.tags;

  for (const id of [...state.selected]) {
    if (!state.byId.has(id)) state.selected.delete(id);
  }
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
      current(p, "pending") ? el("span", { class: "badge pending", text: "pending" }) : null,
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
  $("#sheet").replaceChildren(...state.photos.map(thumbFor));
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
  if (ev.button !== 0 || state.frozen) return;
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
function fieldFrame(name, photos, field, control, noteFn = () => "") {
  const note = el("div", { class: "note" });
  const node = el("div", { class: "field" },
    el("div", { class: "name" },
      name,
      el("button", { type: "button", class: "revert", text: "revert", title: "undo unsaved changes to this field",
        onclick: () => { revert(photos, field); renderSidebar(); } }),
    ),
    control,
    note,
  );
  node.refresh = () => {
    node.classList.toggle("dirty", photos.some(p => isEdited(p, field)));
    note.textContent = noteFn();
  };
  node.refresh();
  return node;
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
    el("option", { value: "public",  text: "public" }),
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

function renderSidebar() {
  const side = $("#sidebar");
  const photos = selectedPhotos();

  if (!photos.length) {
    side.replaceChildren(
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
    checkboxField("Pending", photos, "pending", "not yet reviewed, so not published"),
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
    ...fields,
    one ? facts(one) : null,
  ].filter(Boolean));
}

// ---------------------------------------------------------------------------
// Writing

function refreshWriteButton() {
  const n = state.edits.size;
  const b = $("#write");
  b.disabled = state.frozen || n === 0;
  b.textContent = n ? `Write changes (${n})` : "Write changes";
}

function freeze(on) {
  state.frozen = on;
  document.body.classList.toggle("frozen", on);
  $("#note").disabled = on;
  refreshWriteButton();
}

async function write() {
  if (state.frozen || !state.edits.size) return;
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

  freeze(true);
  setStatus(`writing ${sent.length} photo(s)…`);

  let res, data;
  try {
    res = await fetch("/api/write", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ note: $("#note").value, photos: sent, new_albums: newAlbums }),
    });
    data = await res.json();
  } catch (e) {
    freeze(false);
    setStatus(`writing failed: ${e.message}`, true);
    return;
  }

  freeze(false);

  if (res.status === 409) {
    setStatus(`Nothing written: ${data.conflicts.length} photo(s) changed on disk since loading.  `, true,
      el("button", { type: "button", text: "discard my edits to those and reload them",
        onclick: () => reloadPhotos(data.conflicts) }));
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
  refreshAlbumList();
  $("#note").value = "";

  setStatus(data.commit ? `committed ${data.commit}: ${data.photos.length} photo(s)` : "nothing needed changing");
  renderSheet();
  renderSidebar();
  refreshWriteButton();
}

async function reloadPhotos(ids) {
  const res = await fetch("/api/batch");
  if (!res.ok) { setStatus(`reloading failed: ${res.status}`, true); return; }
  const data = await res.json();
  const fresh = new Map(data.photos.map(p => [p.id, p]));
  for (const id of ids) {
    if (fresh.has(id)) replaceSaved(fresh.get(id));
    state.edits.delete(id);
  }
  setStatus(`reloaded ${ids.length} photo(s) from disk`);
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

  if (ev.target.closest("input, textarea, select")) return;

  if ((ev.metaKey || ev.ctrlKey) && ev.key === "a") {
    ev.preventDefault();
    state.selected = new Set(state.photos.map(p => p.id));
    selectionChanged();
  } else if (ev.key === "Escape") {
    state.selected.clear();
    selectionChanged();
  }
}

async function start() {
  const sheet = $("#sheet");
  sheet.addEventListener("pointerdown", sheetPointerDown);
  sheet.addEventListener("pointermove", sheetPointerMove);
  sheet.addEventListener("pointerup", sheetPointerUp);
  document.addEventListener("keydown", keydown);
  $("#write").addEventListener("click", write);

  window.addEventListener("beforeunload", (ev) => {
    if (state.edits.size) { ev.preventDefault(); ev.returnValue = ""; }
  });

  try {
    await load();
  } catch (e) {
    setStatus(e.message, true);
    return;
  }

  const list = el("datalist", { id: "all-tags" }, state.tags.map(t => el("option", { value: t })));
  document.body.append(list);
  refreshAlbumList();

  $("#label").textContent = state.label;
  document.title = `jiggle editor: ${state.label}`;
  renderSheet();
  renderSidebar();
  refreshWriteButton();
}

start();
