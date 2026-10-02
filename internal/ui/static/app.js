"use strict";

// Routes live in the hash so the back button works everywhere:
//   #/photos/2026            folder
//   #/photos/2026?view=a.jpg viewer open on a.jpg
const $ = (id) => document.getElementById(id);

const ICONS = {
  folder: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/></svg>',
  play: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M8 5v14l11-7z"/></svg>',
  audio: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M9 18V6l10-2v12M9 18a3 3 0 1 1-6 0 3 3 0 0 1 6 0zm10-2a3 3 0 1 1-6 0 3 3 0 0 1 6 0z"/></svg>',
};

const SORTS = {
  newest: { label: "Newest", cmp: (a, b) => b.mtime - a.mtime },
  name: { label: "A–Z", cmp: (a, b) => a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: "base" }) },
};

const TEXT_LIMIT = 1024 * 1024; // show at most 1 MB of text inline

const state = {
  path: ".",
  entries: [],
  viewable: [], // entries the viewer can step through, in display order
  sort: load("sort", "newest"),
  index: -1,
  loadedPath: null,
  pushed: false, // viewer was opened by a tap, so "back" returns to the folder
};

// ---------- helpers ----------

function load(key, def) {
  try { return localStorage.getItem("kropka." + key) || def; } catch { return def; }
}
function save(key, val) {
  try { localStorage.setItem("kropka." + key, val); } catch { /* private mode */ }
}

function encPath(p) {
  return p.split("/").map(encodeURIComponent).join("/");
}
function join(dir, name) {
  return dir === "." ? name : dir + "/" + name;
}
function rawURL(dir, name, download) {
  return "/raw/" + encPath(join(dir, name)) + (download ? "?dl=1" : "");
}
function el(tag, attrs = {}, html) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === undefined || v === null) continue;
    if (k === "text") e.textContent = v;
    else e.setAttribute(k, v);
  }
  if (html) e.innerHTML = html;
  return e;
}
function fmtSize(n) {
  if (n < 1024) return n + " B";
  const units = ["KB", "MB", "GB", "TB"];
  let i = -1;
  do { n /= 1024; i++; } while (n >= 1024 && i < units.length - 1);
  return n.toFixed(n < 10 ? 1 : 0) + " " + units[i];
}
function fmtDate(ms) {
  const d = new Date(ms);
  const now = Date.now();
  const diff = (now - ms) / 1000;
  if (diff < 60) return "just now";
  if (diff < 3600) return Math.floor(diff / 60) + " min ago";
  if (diff < 86400 && d.getDate() === new Date(now).getDate()) {
    return d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
  }
  return d.toLocaleDateString(undefined, { day: "numeric", month: "short", year: d.getFullYear() === new Date(now).getFullYear() ? undefined : "numeric" });
}
function ext(name) {
  const i = name.lastIndexOf(".");
  return i > 0 ? name.slice(i + 1, i + 5) : "file";
}

// ---------- routing ----------

function parseHash() {
  let h = decodeURIComponent(location.hash.slice(1) || "/");
  let view = null;
  const q = h.indexOf("?view=");
  if (q >= 0) {
    view = h.slice(q + 6);
    h = h.slice(0, q);
  }
  const path = h.replace(/^\/+|\/+$/g, "") || ".";
  return { path, view };
}
function hashFor(path, view) {
  const p = path === "." ? "/" : "/" + path;
  return "#" + encodeURI(p) + (view ? "?view=" + encodeURIComponent(view) : "");
}

async function route() {
  const { path, view } = parseHash();
  if (path !== state.loadedPath) {
    closeViewer(true);
    await loadDir(path);
  }
  if (view) openViewer(view);
  else closeViewer(true);
}

// ---------- listing ----------

async function loadDir(path, { keepScroll = false } = {}) {
  state.path = path;
  renderCrumbs();
  const status = $("status");
  if (!keepScroll) {
    status.textContent = "Loading…";
    status.hidden = false;
  }
  let res;
  try {
    res = await fetch("/api/ls?path=" + encodeURIComponent(path), { cache: "no-store" });
  } catch {
    showStatus("Can’t reach kropka. Is it still running?");
    return;
  }
  if (res.status === 401) {
    showStatus("Session expired. Open the link printed in the terminal again.");
    return;
  }
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    state.entries = [];
    state.loadedPath = path;
    renderList();
    showStatus(
      res.status === 404 ? "This folder doesn’t exist."
        : body.error === "not a folder" ? "This is a file, not a folder."
        : body.error || "Something went wrong.");
    return;
  }
  const data = await res.json();
  state.entries = data.entries || [];
  state.loadedPath = path;
  renderList();
  if (!keepScroll) window.scrollTo(0, 0);
}

function showStatus(msg) {
  const s = $("status");
  s.textContent = msg;
  s.hidden = false;
  for (const id of ["folders", "grid", "files"]) $(id).hidden = true;
}

function renderCrumbs() {
  const nav = $("crumbs");
  nav.replaceChildren();
  const parts = state.path === "." ? [] : state.path.split("/");
  nav.append(el("a", { href: "#/", text: document.title.replace(/^kropka · /, "") || "~" }));
  let acc = "";
  for (const p of parts) {
    acc = acc ? acc + "/" + p : p;
    nav.append(el("span", { class: "sep", text: "/", "aria-hidden": "true" }));
    nav.append(el("a", { href: hashFor(acc), text: p }));
  }
  nav.scrollLeft = nav.scrollWidth;
}

function renderSortButton() {
  $("sort").textContent = SORTS[state.sort].label;
}

function renderList() {
  const cmp = SORTS[state.sort].cmp;
  const dirs = state.entries.filter((e) => e.kind === "dir").sort(cmp);
  const media = state.entries.filter((e) => e.kind === "image" || e.kind === "video").sort(cmp);
  const others = state.entries.filter((e) => e.kind !== "dir" && e.kind !== "image" && e.kind !== "video").sort(cmp);

  // The viewer walks media first, then inline-viewable files, matching the page order.
  state.viewable = [...media, ...others.filter((e) => e.kind === "text" || e.kind === "audio")];

  const folders = $("folders");
  folders.replaceChildren(...dirs.map((d) => {
    const a = el("a", { class: "folder", href: hashFor(join(state.path, d.name)) }, ICONS.folder);
    a.append(el("span", { text: d.name }));
    return a;
  }));
  folders.hidden = dirs.length === 0;

  const grid = $("grid");
  grid.replaceChildren(...media.map((m) => {
    const b = el("button", { class: "tile", type: "button", "data-name": m.name, "aria-label": m.name, title: m.name });
    const src = rawURL(state.path, m.name);
    if (m.kind === "image") {
      b.append(el("img", { src, alt: "", loading: "lazy", decoding: "async" }));
    } else {
      b.append(el("video", { src: src + "#t=0.1", preload: "metadata", muted: "", playsinline: "" }));
      b.append(el("span", { class: "tile-badge" }, ICONS.play));
    }
    return b;
  }));
  grid.hidden = media.length === 0;

  const files = $("files");
  files.replaceChildren(...others.map((f) => {
    const inline = f.kind === "text" || f.kind === "audio";
    const row = inline
      ? el("button", { class: "file", type: "button", "data-name": f.name })
      : el("a", { class: "file", href: rawURL(state.path, f.name, f.kind === "other"), target: "_blank", rel: "noopener" });
    row.append(el("span", { class: "ext", text: ext(f.name) }));
    const main = el("span", { class: "file-main" });
    main.append(el("div", { class: "file-name", text: f.name }));
    main.append(el("div", { class: "file-meta", text: fmtSize(f.size) + " · " + fmtDate(f.mtime) }));
    row.append(main);
    return row;
  }));
  files.hidden = others.length === 0;

  const s = $("status");
  if (state.entries.length === 0) {
    s.textContent = "This folder is empty.";
    s.hidden = false;
  } else {
    s.hidden = true;
  }
}

// ---------- viewer ----------

function openViewer(name) {
  const i = state.viewable.findIndex((e) => e.name === name);
  if (i < 0) {
    // Unknown file (deleted, or a stale link): fall back to the folder.
    history.replaceState(null, "", hashFor(state.path));
    closeViewer(true);
    return;
  }
  state.index = i;
  $("viewer").hidden = false;
  document.body.classList.add("locked");
  showItem();
}

function closeViewer(fromRoute) {
  const v = $("viewer");
  if (v.hidden) return;
  stopMedia();
  v.hidden = true;
  document.body.classList.remove("locked");
  state.index = -1;
  if (fromRoute) { state.pushed = false; return; }
  if (state.pushed) history.back();
  else history.replaceState(null, "", hashFor(state.path));
  state.pushed = false;
}

function stopMedia() {
  for (const m of $("v-stage").querySelectorAll("video, audio")) {
    m.pause();
    m.removeAttribute("src");
    m.load();
  }
}

function go(delta) {
  const n = state.index + delta;
  if (n < 0 || n >= state.viewable.length) return;
  // replaceState: stepping through photos shouldn't fill the history
  history.replaceState(null, "", hashFor(state.path, state.viewable[n].name));
  state.index = n;
  showItem();
}

async function showItem() {
  const item = state.viewable[state.index];
  const stage = $("v-stage");
  stopMedia();
  stage.replaceChildren();

  $("v-name").textContent = item.name;
  $("v-count").textContent = state.index + 1 + " / " + state.viewable.length;
  $("v-download").href = rawURL(state.path, item.name, true);
  $("v-prev").hidden = state.index === 0;
  $("v-next").hidden = state.index === state.viewable.length - 1;

  const src = rawURL(state.path, item.name);
  if (item.kind === "image") {
    stage.append(el("img", { src, alt: item.name }));
    preloadNeighbors();
  } else if (item.kind === "video") {
    stage.append(el("video", { src, controls: "", autoplay: "", playsinline: "" }));
  } else if (item.kind === "audio") {
    stage.append(el("audio", { src, controls: "", autoplay: "" }));
  } else if (item.kind === "text") {
    const pre = el("pre", { text: "Loading…" });
    stage.append(pre);
    try {
      const res = await fetch(src, { headers: { Range: "bytes=0-" + (TEXT_LIMIT - 1) } });
      const text = await res.text();
      if (state.viewable[state.index] !== item) return; // user moved on
      pre.textContent = item.size > TEXT_LIMIT ? text + "\n\n… (truncated, download for the full file)" : text;
    } catch {
      pre.textContent = "Couldn’t load this file.";
    }
  }
}

function preloadNeighbors() {
  for (const d of [1, -1]) {
    const e = state.viewable[state.index + d];
    if (e && e.kind === "image") new Image().src = rawURL(state.path, e.name);
  }
}

// Swipe: horizontal to step, down to close. Ignored while pinch-zoomed.
function setupGestures() {
  const v = $("viewer");
  let x0 = 0, y0 = 0, t0 = 0, tracking = false;

  v.addEventListener("touchstart", (e) => {
    if (e.touches.length !== 1 || zoomed()) { tracking = false; return; }
    const t = e.touches[0];
    x0 = t.clientX; y0 = t.clientY; t0 = Date.now();
    tracking = !e.target.closest("video, audio, pre, .viewer-top");
  }, { passive: true });

  v.addEventListener("touchmove", (e) => {
    if (!tracking || e.touches.length !== 1) return;
    const t = e.touches[0];
    const dx = t.clientX - x0, dy = t.clientY - y0;
    const media = $("v-stage").firstElementChild;
    if (!media) return;
    if (Math.abs(dx) > Math.abs(dy)) {
      media.style.transition = "none";
      media.style.transform = `translateX(${dx}px)`;
    } else if (dy > 0) {
      media.style.transition = "none";
      media.style.transform = `translateY(${dy}px)`;
      media.style.opacity = String(Math.max(0.3, 1 - dy / 400));
    }
  }, { passive: true });

  v.addEventListener("touchend", (e) => {
    if (!tracking) return;
    tracking = false;
    const t = e.changedTouches[0];
    const dx = t.clientX - x0, dy = t.clientY - y0;
    const fast = Date.now() - t0 < 300;
    const media = $("v-stage").firstElementChild;
    if (media) {
      media.style.transition = "";
      media.style.transform = "";
      media.style.opacity = "";
    }
    if (Math.abs(dx) > Math.abs(dy) && (Math.abs(dx) > 80 || (fast && Math.abs(dx) > 30))) {
      go(dx < 0 ? 1 : -1);
    } else if (dy > 120 || (fast && dy > 60)) {
      closeViewer(false);
    }
  });
}

function zoomed() {
  return window.visualViewport && window.visualViewport.scale > 1.01;
}

// ---------- wiring ----------

function init() {
  renderSortButton();

  fetch("/api/info").then((r) => (r.ok ? r.json() : null)).then((info) => {
    if (!info) return;
    document.title = "kropka · " + info.name;
    renderCrumbs();
  }).catch(() => {});

  $("sort").addEventListener("click", () => {
    state.sort = state.sort === "newest" ? "name" : "newest";
    save("sort", state.sort);
    renderSortButton();
    renderList();
  });
  $("refresh").addEventListener("click", () => loadDir(state.path, { keepScroll: true }));

  const openFrom = (e) => {
    const b = e.target.closest("[data-name]");
    if (!b) return;
    state.pushed = true;
    location.hash = hashFor(state.path, b.dataset.name);
  };
  $("grid").addEventListener("click", openFrom);
  $("files").addEventListener("click", openFrom);

  $("v-close").addEventListener("click", () => closeViewer(false));
  $("v-prev").addEventListener("click", () => go(-1));
  $("v-next").addEventListener("click", () => go(1));
  $("v-stage").addEventListener("click", (e) => {
    if (e.target === e.currentTarget) closeViewer(false); // tap on the backdrop
  });

  document.addEventListener("keydown", (e) => {
    if ($("viewer").hidden) return;
    if (e.key === "Escape") closeViewer(false);
    else if (e.key === "ArrowRight") go(1);
    else if (e.key === "ArrowLeft") go(-1);
  });

  // Coming back to the tab (e.g. after generating new files): refresh quietly.
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible" && $("viewer").hidden) {
      loadDir(state.path, { keepScroll: true });
    }
  });

  setupGestures();
  window.addEventListener("hashchange", route);
  route();
}

init();
