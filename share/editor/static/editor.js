// jiggle's editor: a contact sheet of one batch, and a sidebar for the
// selected photos.  -- claude, 2026-09-30
"use strict";

const state = {
  label:    "",
  photos:   [],          // the batch, in order, as loaded from disk
  byId:     new Map(),
  albums:   [],          // every album in the library: { slug, title }
  selected: new Set(),
  anchor:   null,        // where a shift-click range starts
};

const $ = (sel) => document.querySelector(sel);

function el(tag, attrs = {}, ...kids) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === null || v === undefined || v === false) continue;
    if (k === "class") node.className = v;
    else if (k === "text") node.textContent = v;
    else if (k.startsWith("on")) node.addEventListener(k.slice(2), v);
    else node.setAttribute(k, v === true ? "" : v);
  }
  for (const kid of kids.flat()) {
    if (kid === null || kid === undefined || kid === false) continue;
    node.append(kid instanceof Node ? kid : document.createTextNode(kid));
  }
  return node;
}

function setStatus(text, bad = false) {
  const s = $("#status");
  s.textContent = text;
  s.classList.toggle("bad", bad);
}

async function load() {
  const res = await fetch("/api/batch");
  if (!res.ok) throw new Error(`loading the batch: ${res.status}`);
  const data = await res.json();

  state.label  = data.label;
  state.photos = data.photos;
  state.byId   = new Map(data.photos.map(p => [p.id, p]));
  state.albums = data.albums;

  for (const id of [...state.selected]) {
    if (!state.byId.has(id)) state.selected.delete(id);
  }
}

// ---------------------------------------------------------------------------
// The contact sheet

function thumbFor(photo) {
  const h = 150;
  const w = Math.round(h * photo.width / photo.height);
  const src = `/r/${photo.id}/h480.webp`;

  return el("figure", { class: "thumb", "data-id": photo.id, style: `width: ${w + 10}px` },
    el("img", { src, width: w, height: h, loading: "lazy", alt: "" }),
    el("div", { class: "badges" },
      photo.pending ? el("span", { class: "badge pending", text: "pending" }) : null,
      photo.visibility === "private" ? el("span", { class: "badge", text: "private" }) : null,
      photo.type === "video" ? el("span", { class: "badge", text: "▶" }) : null,
    ),
    el("figcaption", { text: photo.title || photo.file || photo.id }),
  );
}

function renderSheet() {
  const sheet = $("#sheet");
  sheet.replaceChildren(...state.photos.map(thumbFor));
  paintSelection();
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
  if (ev.button !== 0) return;
  const sheet = $("#sheet");
  const origin = sheet.getBoundingClientRect();
  const thumb = ev.target.closest(".thumb");

  drag = {
    x: ev.clientX, y: ev.clientY,
    thumb: thumb ? thumb.dataset.id : null,
    toggle: ev.metaKey || ev.ctrlKey,
    base: new Set(state.selected),
    boxing: false,
    rects: null,
    origin,
  };

  sheet.setPointerCapture(ev.pointerId);
  ev.preventDefault();
}

function measureThumbs(sheet, origin) {
  return [...sheet.querySelectorAll(".thumb")].map(node => {
    const r = node.getBoundingClientRect();
    return {
      id: node.dataset.id,
      left: r.left - origin.left + sheet.scrollLeft,
      top:  r.top  - origin.top  + sheet.scrollTop,
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

function selectedPhotos() {
  return state.photos.filter(p => state.selected.has(p.id));
}

// The values a field has across the selection: [ value, ... ], distinct.
function distinct(photos, get) {
  const seen = new Map();
  for (const p of photos) {
    const v = get(p);
    const key = JSON.stringify(v);
    if (!seen.has(key)) seen.set(key, v);
  }
  return [...seen.values()];
}

function shown(value) {
  if (value === null || value === undefined || value === "") return el("span", { class: "empty", text: "none" });
  if (value === true)  return "yes";
  if (value === false) return "no";
  return String(value);
}

function readOnlyField(name, photos, get) {
  const values = distinct(photos, get);
  const mixed = values.length > 1;
  return el("div", { class: `field${mixed ? " mixed" : ""}` },
    el("div", { class: "name", text: name }),
    mixed ? el("div", { class: "note", text: `mixed: ${values.length} different values` })
          : el("div", { class: "value" }, shown(values[0])),
  );
}

function setField(name, photos, get) {
  const counts = new Map();
  for (const p of photos) for (const v of get(p)) counts.set(v, (counts.get(v) || 0) + 1);
  const names = [...counts.keys()].sort((a, b) => a.localeCompare(b));

  return el("div", { class: "field" },
    el("div", { class: "name", text: name }),
    names.length
      ? el("div", { class: "chips" }, names.map(n => {
          const c = counts.get(n);
          const partial = c < photos.length;
          return el("span", { class: `chip${partial ? " partial" : ""}` },
            n,
            photos.length > 1 ? el("span", { class: "count", text: `${c}/${photos.length}` }) : null,
          );
        }))
      : el("div", { class: "value" }, shown(null)),
  );
}

function albumTitle(slug) {
  const a = state.albums.find(a => a.slug === slug);
  return a ? a.title : slug;
}

function renderSidebar() {
  const side = $("#sidebar");
  const photos = selectedPhotos();

  if (!photos.length) {
    side.replaceChildren(
      el("h2", { text: `${state.photos.length} photo(s) in this batch` }),
      el("p", { class: "empty", text:
        "Click a photo to select it.  ⌘-click adds or removes one, shift-click selects a range, and dragging selects everything the box touches.  ⌘A selects all." }),
    );
    return;
  }

  const one = photos.length === 1 ? photos[0] : null;

  side.replaceChildren(
    el("h2", { text: one ? one.file || one.id : `${photos.length} photos selected` }),
    one ? el("img", { class: "preview", src: `/r/${one.id}/500.webp`, alt: "" }) : null,
    readOnlyField("Title", photos, p => p.title),
    readOnlyField("Description", photos, p => p.description),
    setField("Tags", photos, p => p.tags),
    setField("Albums", photos, p => p.albums.map(albumTitle)),
    readOnlyField("Visibility", photos, p => p.visibility),
    readOnlyField("Pending", photos, p => p.pending),
    readOnlyField("Taken", photos, p => p.taken),
    readOnlyField("Location", photos, p => p.location ? (p.location.private ? "private" : "published") : null),
    readOnlyField("Extra rotation", photos, p => p.rotate ? `${p.rotate}°` : null),
    one ? facts(one) : null,
  );
}

function facts(p) {
  return el("div", { class: "facts" },
    el("div", { text: `id ${p.id}` }),
    el("div", { text: `${p.type}, ${p.width} × ${p.height}${p.duration ? `, ${p.duration.toFixed(1)} s` : ""}` }),
    p.added ? el("div", { text: `added ${p.added}` }) : null,
    p.flickr_id ? el("div", { text: `Flickr ${p.flickr_id}` }) : null,
  );
}

// ---------------------------------------------------------------------------

function keydown(ev) {
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

  try {
    await load();
  } catch (e) {
    setStatus(e.message, true);
    return;
  }

  $("#label").textContent = state.label;
  document.title = `jiggle editor: ${state.label}`;
  renderSheet();
  renderSidebar();
}

start();
