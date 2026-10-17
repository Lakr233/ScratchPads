"use strict";

// ScratchPads web client. Plain DOM, no framework, no inline handlers (CSP).

const state = {
  me: null,
  topics: [],
  topic: null,
  nodes: [],
  path: "",
  node: null,
  children: [],
  comments: [],
  editing: false,
  draft: "",
  showComments: !window.matchMedia("(max-width: 1100px)").matches,
  activeThread: null,
  pendingQuote: null,
  expanded: new Set(),
  unread: 0,
  filter: "",
  replyOpen: null,
  showResolved: false,
  navOpen: false,
  pendingThread: null,
  drafts: {},
};

const app = document.getElementById("app");

// ---------- helpers ----------

function h(tag, attrs, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === null || v === undefined || v === false) continue;
    if (k.startsWith("on")) bind(el, k.slice(2), v);
    else if (k === "class") el.className = v;
    else if (k === "style")
      el.style.cssText = v; // CSSOM, allowed by the CSP unlike style=""
    else if (k === "html") el.innerHTML = v;
    else el.setAttribute(k, v === true ? "" : v);
  }
  for (const c of children.flat(Infinity)) {
    if (c === null || c === undefined || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return el;
}

// Handlers live in el.__on behind one stable listener per event type, so
// morph() can swap them without re-creating the element.
function bind(el, type, fn) {
  if (!el.__on) el.__on = {};
  if (!(type in el.__on)) el.addEventListener(type, (e) => el.__on[type]?.(e));
  el.__on[type] = fn;
}

// morph patches `from` in place until it matches `to`. Unchanged nodes are
// kept, so re-rendering never reloads images, drops hover, resets scroll
// positions or clears a text selection: the source of the flicker.
function morph(from, to) {
  // data-key marks identity (e.g. which pad a composer belongs to); a
  // different key means a different element, never a patched one.
  const keyOf = (n) => (n.nodeType === Node.ELEMENT_NODE ? n.getAttribute("data-key") : null);
  if (from.nodeType !== to.nodeType || from.nodeName !== to.nodeName || keyOf(from) !== keyOf(to)) {
    from.replaceWith(to);
    return to;
  }
  if (from.nodeType === Node.TEXT_NODE) {
    if (from.data !== to.data) from.data = to.data;
    return from;
  }
  if (from.nodeType !== Node.ELEMENT_NODE) {
    from.replaceWith(to);
    return to;
  }
  for (const a of [...from.attributes]) {
    if (a.name !== "style" && !to.hasAttribute(a.name)) from.removeAttribute(a.name);
  }
  for (const a of to.attributes) {
    if (a.name !== "style" && from.getAttribute(a.name) !== a.value) from.setAttribute(a.name, a.value);
  }
  // Textareas own their height (autosize, user resize), so leave it alone.
  if (from.tagName !== "TEXTAREA" && from.style.cssText !== to.style.cssText) from.style.cssText = to.style.cssText;
  for (const type of Object.keys(from.__on || {})) {
    if (!to.__on || !(type in to.__on)) from.__on[type] = null;
  }
  for (const [type, fn] of Object.entries(to.__on || {})) bind(from, type, fn);
  if (/^(INPUT|TEXTAREA|SELECT)$/.test(to.tagName)) {
    if (from !== document.activeElement && from.value !== to.value) from.value = to.value;
    if (from.checked !== to.checked) from.checked = to.checked;
  }
  const a = [...from.childNodes];
  const b = [...to.childNodes];
  b.forEach((child, i) => (i < a.length ? morph(a[i], child) : from.appendChild(child)));
  for (let i = b.length; i < a.length; i++) a[i].remove();
  return from;
}

class APIError extends Error {
  constructor(status, message) {
    super(message);
    this.status = status;
  }
}

async function api(method, path, body, headers = {}) {
  const opts = { method, headers: { "X-SP-CSRF": "1", ...headers }, credentials: "same-origin" };
  if (body !== undefined) {
    if (typeof body === "string") {
      opts.body = body;
      opts.headers["Content-Type"] = opts.headers["Content-Type"] || "text/markdown; charset=utf-8";
    } else {
      opts.body = JSON.stringify(body);
      opts.headers["Content-Type"] = "application/json";
    }
  }
  const res = await fetch(path, opts);
  if (res.status === 401 && state.me) {
    state.me = null;
    document.querySelectorAll(".modal-backdrop, .popover").forEach((m) => m.remove());
    render();
  }
  if (!res.ok) {
    let msg = res.statusText;
    try {
      msg = (await res.json()).error || msg;
    } catch (_) {}
    msg = msg.replace(/^(invalid|conflict): /, "").replace(/ must match \^.*$/, t(" may only use a–z, 0–9 and -"));
    if (msg === "invalid credentials" || msg === "invalid token") msg = t("Wrong token or password");
    msg = t(msg);
    throw new APIError(res.status, msg);
  }
  if (res.status === 204) return null;
  const ct = res.headers.get("Content-Type") || "";
  return ct.includes("json") ? res.json() : res.text();
}

const enc = (p) => p.split("/").map(encodeURIComponent).join("/");
const filesURL = (p) => `/api/t/${encodeURIComponent(state.topic.slug)}/f/${enc(p)}`;
const commentsURL = (p) => `/api/t/${encodeURIComponent(state.topic.slug)}/c/${enc(p)}`;
const basename = (p) => p.split("/").pop();
const dirname = (p) => p.split("/").slice(0, -1).join("/");

function toast(msg) {
  document.querySelectorAll(".toast").forEach((x) => x.remove());
  const el = h("div", { class: "toast" }, msg);
  document.body.append(el);
  setTimeout(() => el.remove(), 2600);
}

function fail(err) {
  console.error(err);
  toast(err.message || String(err));
}

function ago(ts) {
  const s = Math.floor(Date.now() / 1000 - ts);
  if (s < 60) return t("just now");
  if (s < 3600) return t("{0}m ago", Math.floor(s / 60));
  if (s < 86400) return t("{0}h ago", Math.floor(s / 3600));
  if (s < 86400 * 30) return t("{0}d ago", Math.floor(s / 86400));
  return new Date(ts * 1000).toLocaleDateString();
}

const palette = ["#ff8a65", "#7c9cf5", "#4cc38a", "#e5a93b", "#c27cf5", "#f57ca8", "#3fb6c9", "#8d9bb0"];
function colorFor(name) {
  let n = 0;
  for (const c of name) n = (n * 31 + c.charCodeAt(0)) >>> 0;
  return palette[n % palette.length];
}

function avatar(name) {
  const agent = name.includes("/");
  const label = agent ? name.split("/")[1].slice(0, 2) : name.slice(0, 1).toUpperCase();
  const el = h("span", { class: "avatar" + (agent ? " agent" : ""), title: name }, label);
  el.style.background = colorFor(agent ? name.split("/")[0] : name);
  return el;
}

function modal(build, size = "") {
  const opener = document.activeElement;
  const backdrop = h("div", { class: "modal-backdrop" });
  const box = h("div", { class: "modal " + size, role: "dialog", "aria-modal": "true", tabindex: "-1" });
  const onKey = (e) => {
    if (backdrop !== [...document.querySelectorAll(".modal-backdrop")].pop()) return;
    if (e.key === "Escape") {
      e.stopPropagation();
      close();
    }
    if (e.key === "Tab") {
      const f = [...box.querySelectorAll("button,input,select,textarea,a[href]")].filter((el) => !el.disabled && el.offsetParent);
      if (!f.length) return;
      if (e.shiftKey && document.activeElement === f[0]) {
        e.preventDefault();
        f.at(-1).focus();
      } else if (!e.shiftKey && document.activeElement === f.at(-1)) {
        e.preventDefault();
        f[0].focus();
      }
    }
  };
  const close = () => {
    backdrop.remove();
    document.removeEventListener("keydown", onKey, true);
    if (opener && document.contains(opener)) opener.focus({ preventScroll: true });
  };
  document.addEventListener("keydown", onKey, true);
  backdrop.addEventListener("mousedown", (e) => {
    if (e.target === backdrop) close();
  });
  backdrop.append(box);
  document.body.append(backdrop);
  build(box, close);
  if (!box.contains(document.activeElement)) box.focus({ preventScroll: true });
  return close;
}

// confirm2 is the in-app replacement for window.confirm. With `typed`, the
// danger button stays disabled until that exact text is entered.
function confirm2(title, text, label, onConfirm, typed) {
  modal((box, close) => {
    const btn = h("button", { class: "btn danger", disabled: !!typed }, label);
    const input = typed
      ? h("input", {
          class: "input",
          placeholder: typed,
          autocomplete: "off",
          oninput: (e) => {
            btn.disabled = e.currentTarget.value !== typed;
          },
        })
      : null;
    const err = h("div", { class: "form-error" });
    const form = h(
      "form",
      {
        onsubmit: async (e) => {
          e.preventDefault();
          if (btn.disabled) return;
          btn.disabled = true;
          try {
            await onConfirm();
            close();
          } catch (ex) {
            err.textContent = ex.message;
            btn.disabled = false;
          }
        },
      },
      h("h2", null, title),
      h("p", { class: "hint" }, text),
      input,
      err,
      h("div", { class: "row end" }, h("button", { type: "button", class: "btn", onclick: close }, t("Cancel")), btn),
    );
    box.append(form);
    setTimeout(() => (input || btn).focus(), 0);
  }, "small");
}

function prompt2(title, fields, onSubmit, label = t("OK")) {
  modal((box, close) => {
    const inputs = fields.map((f) => h("input", { class: "input", placeholder: f.placeholder || "", value: f.value || "", autocomplete: "off" }));
    const err = h("div", { class: "form-error" });
    const ok = h("button", { class: "btn primary" }, label);
    const form = h(
      "form",
      {
        onsubmit: async (e) => {
          e.preventDefault();
          if (ok.disabled) return;
          ok.disabled = true;
          err.textContent = "";
          try {
            await onSubmit(...inputs.map((i) => i.value.trim()));
            close();
          } catch (ex) {
            err.textContent = ex.message;
          } finally {
            ok.disabled = false;
          }
        },
      },
      h("h2", null, title),
      inputs,
      err,
      h("div", { class: "row end" }, h("button", { type: "button", class: "btn", onclick: close }, t("Cancel")), ok),
    );
    box.append(form);
    setTimeout(() => {
      const first = inputs[0];
      if (!first) return;
      first.focus();
      // Pre-filled paths: select the name part, like a file manager.
      if (first.value) {
        const start = first.value.lastIndexOf("/") + 1;
        first.setSelectionRange(start, first.value.replace(/\.md$/i, "").length);
      }
    }, 0);
  }, "small");
}

// ---------- routing ----------

function parseHash() {
  const m = /^#\/t\/([^/]+)\/?(.*)$/.exec(location.hash);
  if (!m) return { topic: null, path: "" };
  return { topic: decodeURIComponent(m[1]), path: m[2].split("/").filter(Boolean).map(decodeURIComponent).join("/") };
}

function go(topic, path = "") {
  const hash = topic ? `#/t/${encodeURIComponent(topic)}/${enc(path)}` : "#/";
  if (location.hash !== hash) location.hash = hash;
  else route();
}

async function route() {
  const { topic, path } = parseHash();
  if (!state.me) return render();
  if (!topic) {
    if (state.topics.length) return go(state.topics[0].slug);
    state.topic = null;
    return render();
  }
  if (state.editing && !confirmLeave()) {
    history.replaceState(null, "", `#/t/${encodeURIComponent(state.topic.slug)}/${enc(state.path)}`);
    return;
  }
  state.editing = false;
  state.pendingQuote = null;
  state.activeThread = state.pendingThread;
  state.pendingThread = null;
  try {
    if (!state.topic || state.topic.slug !== topic) {
      state.topic = state.topics.find((t) => t.slug === topic) || null;
      if (!state.topic) {
        toast(t("Topic not found"));
        return go(null);
      }
      state.expanded = new Set();
    }
    for (const d of dirParents(path)) state.expanded.add(d);
    await loadTree();
    await loadPath(path);
  } catch (e) {
    fail(e);
  }
  state.navOpen = false;
  // morph keeps the pane between pages, so start a new page at the top.
  const moved = state.renderedPath !== topic + "/" + path;
  state.renderedPath = topic + "/" + path;
  render();
  if (moved) document.querySelector(".doc-pane")?.scrollTo(0, 0);
}

function dirParents(p) {
  const parts = p.split("/").filter(Boolean);
  return parts.map((_, i) => parts.slice(0, i + 1).join("/"));
}

function confirmLeave() {
  if (state.node && state.draft !== state.node.content) return confirm(t("Discard unsaved changes?"));
  return true;
}

// ---------- data ----------

async function loadMe() {
  try {
    state.me = await api("GET", "/api/me");
    state.tlsInfo = await api("GET", "/api/info").catch(() => null);
    state.topics = await api("GET", "/api/topics");
  } catch (_) {
    state.me = null;
  }
}

async function loadTree() {
  const res = await api("GET", `/api/t/${encodeURIComponent(state.topic.slug)}/tree`);
  state.nodes = res.nodes;
  state.topic.perm = res.topic.perm || state.topic.perm;
}

async function loadPath(path) {
  state.path = path;
  state.node = null;
  state.children = [];
  state.comments = [];
  if (!path) {
    state.children = state.nodes.filter((n) => !n.path.includes("/"));
    return;
  }
  const res = await api("GET", filesURL(path) + "?meta=1");
  if (res.kind === "dir") {
    state.children = res.children;
    return;
  }
  res.content = res.content || "";
  state.node = res;
  state.draft = res.content;
  await loadComments();
}

async function loadComments() {
  if (!state.node) return;
  state.comments = await api("GET", commentsURL(state.node.path));
}

async function refreshInbox() {
  if (!state.me) return;
  try {
    const items = await api("GET", "/api/inbox");
    state.unread = items.length;
    const badge = document.querySelector("#inbox-count");
    if (badge) {
      badge.textContent = state.unread;
      badge.classList.toggle("hidden", !state.unread);
    }
  } catch (_) {}
}

// Outline-like line icons on a 24px grid. Filled shapes are marked with *.
const ICONS = {
  doc: "M8 3.75h5.5l4.75 4.75v10.75a1 1 0 0 1-1 1H8a1 1 0 0 1-1-1V4.75a1 1 0 0 1 1-1zM13.5 3.75V8.5h4.75",
  folder: "M4.75 6.75a1 1 0 0 1 1-1h3.6l1.8 2h7.1a1 1 0 0 1 1 1v8.5a1 1 0 0 1-1 1H5.75a1 1 0 0 1-1-1z",
  disclosure: "*M8 10h8l-4 4.5z",
  slash: "M10 18l4-12",
  search: "M10.5 5.5a5 5 0 1 0 0 10a5 5 0 1 0 0-10zM14.2 14.2l4.3 4.3",
  bell: "M7 16.5V11a5 5 0 0 1 10 0v5.5l1.5 1.5h-13zM10.5 19.5a1.6 1.6 0 0 0 3 0",
  settings:
    "M12 9.25a2.75 2.75 0 1 0 0 5.5a2.75 2.75 0 1 0 0-5.5zM12 4v2.25M12 17.75V20M4 12h2.25M17.75 12H20M6.35 6.35l1.6 1.6M16.05 16.05l1.6 1.6M6.35 17.65l1.6-1.6M16.05 7.95l1.6-1.6",
  plus: "M12 6.5v11M6.5 12h11",
  comment: "M6.5 5h11A1.5 1.5 0 0 1 19 6.5v8a1.5 1.5 0 0 1-1.5 1.5H11l-4 3.25V16h-.5A1.5 1.5 0 0 1 5 14.5v-8A1.5 1.5 0 0 1 6.5 5z",
  more: "*M7.5 10.5a1.5 1.5 0 1 1 0 3a1.5 1.5 0 1 1 0-3zM12 10.5a1.5 1.5 0 1 1 0 3a1.5 1.5 0 1 1 0-3zM16.5 10.5a1.5 1.5 0 1 1 0 3a1.5 1.5 0 1 1 0-3z",
  menu: "M5 7h14M5 12h14M5 17h14",
  close: "M7.5 7.5l9 9M16.5 7.5l-9 9",
  trash: "M6 7.5h12M10 7.5V5.75h4V7.5M7.5 7.5l.75 11h7.5l.75-11",
  edit: "M5.5 18.5h3l9-9-3-3-9 9zM13 8l3 3",
  check: "M6.5 12.5l3.5 3.5 7.5-8",
};

function icon(name, cls = "") {
  const ns = "http://www.w3.org/2000/svg";
  let d = ICONS[name];
  const filled = d.startsWith("*");
  if (filled) d = d.slice(1);
  const svg = document.createElementNS(ns, "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("class", "icon " + (filled ? "filled " : "") + cls);
  svg.setAttribute("aria-hidden", "true");
  const path = document.createElementNS(ns, "path");
  path.setAttribute("d", d);
  svg.append(path);
  return svg;
}

function iconButton(name, title, onclick, extra) {
  return h("button", { class: "icon-button", title, "aria-label": title, onclick }, icon(name), extra);
}

const logo = (cls = "logo") => h("img", { class: cls, src: "/static/icon.png", alt: "" });

// ---------- views ----------

let lastRender = 0;

function render() {
  const prev = document.activeElement;
  const key = prev && prev.tagName === "TEXTAREA" ? prev.getAttribute("data-key") : null;
  const caret = key && [prev.selectionStart, prev.selectionEnd];
  lastRender = Date.now();
  const next = state.me ? shell() : loginView();
  const cur = app.firstElementChild;
  // Only the shell is morphed; the login view holds direct element references
  // in its handlers, so it is always built fresh.
  if (cur && state.me && cur.classList.contains("shell")) morph(cur, next);
  else app.replaceChildren(next);
  if (key && !prev.isConnected) {
    const ta = document.querySelector(`textarea[data-key="${CSS.escape(key)}"]`);
    if (ta) {
      ta.focus({ preventScroll: true });
      ta.setSelectionRange(...caret);
    }
  }
}

function loginView() {
  const admin = state.adminLogin;
  const user = h("input", { class: "input", placeholder: t("Username"), autocomplete: "username" });
  const input = h("input", {
    class: "input",
    type: "password",
    placeholder: admin ? t("Password") : t("Access token (sp_…)"),
    autocomplete: "current-password",
  });
  const err = h("div", { class: "login-error" });
  const form = h(
    "form",
    {
      onsubmit: async (e) => {
        e.preventDefault();
        if (!input.value.trim() || (admin && !user.value.trim())) {
          err.textContent = admin ? t("Enter username and password") : t("Enter a token");
          return;
        }
        const btn = e.currentTarget.querySelector(".btn.primary");
        btn.disabled = true;
        try {
          const body = admin ? { user: user.value.trim(), password: input.value } : { token: input.value.trim() };
          await api("POST", "/api/session", body);
          await loadMe();
          state.topic = null;
          route();
        } catch (ex) {
          err.textContent = ex.message;
          btn.disabled = false;
        }
      },
    },
    admin ? user : null,
    input,
    h("button", { class: "btn primary full" }, t("Sign in")),
    err,
    h(
      "button",
      {
        type: "button",
        class: "btn ghost",
        onclick: () => {
          state.adminLogin = !admin;
          render();
        },
      },
      admin ? t("Sign in with a token") : t("Admin sign in"),
    ),
  );
  setTimeout(() => (admin ? user : input).focus(), 0);
  return h(
    "div",
    { class: "login" },
    h("div", { class: "login-lang" }, langToggle(render)),
    h("div", { class: "login-card" }, logo(), h("h1", null, "ScratchPads"), h("p", null, t("Shared pads for people and their agents.")), form),
  );
}

function langToggle(after) {
  return h(
    "button",
    {
      class: "btn ghost small",
      title: t("Language"),
      onclick: () => {
        setLang(lang === "zh" ? "en" : "zh");
        after();
      },
    },
    lang === "zh" ? "English" : "中文",
  );
}

function shell() {
  return h(
    "div",
    { class: "shell" + (state.navOpen ? " nav-open" : "") },
    sidebar(),
    h("div", { class: "main" }, docPane(), state.node && state.showComments ? commentsPane() : null),
    state.navOpen
      ? h("div", {
          class: "nav-backdrop",
          onclick: () => {
            state.navOpen = false;
            render();
          },
        })
      : null,
  );
}

function sidebarLink({ label, iconEl, active, depth = 0, disclosure, onclick, count, key }) {
  const link = h(
    "button",
    {
      class: "sidebar-link" + (active ? " active" : ""),
      onclick,
      "data-key": key,
      "aria-current": active ? "page" : null,
      "aria-expanded": typeof disclosure === "boolean" ? String(disclosure) : null,
    },
    disclosure === undefined ? null : icon("disclosure", "disclosure" + (disclosure === null ? " none" : disclosure ? "" : " collapsed")),
    h("span", { class: "icon-wrap" }, iconEl),
    h("span", { class: "label" }, label),
    count ? h("span", { class: "count", title: t("Open threads") }, count) : null,
  );
  if (depth) link.style.paddingInlineStart = 12 + depth * 16 + "px";
  return link;
}

function sidebar() {
  const search = h("input", {
    placeholder: t("Search"),
    value: state.filter,
    "aria-label": t("Search"),
    onkeydown: (e) => {
      const q = e.currentTarget.value.trim();
      if (e.key === "Enter" && q) searchTopic(q);
    },
  });
  const topics = state.topics.map((topic) => {
    const active = state.topic && state.topic.slug === topic.slug;
    const dot = h("span");
    dot.style.background = colorFor(topic.slug);
    return [
      sidebarLink({
        label: topic.title,
        iconEl: h("span", { class: "topic-icon" }, dot),
        active: active && !state.path,
        key: "topic:" + topic.slug,
        onclick: () => go(topic.slug),
      }),
      active ? treeView("", 1) : null,
    ];
  });
  return h(
    "aside",
    { class: "sidebar" },
    h("button", { class: "sidebar-button", onclick: () => go(null) }, logo(), h("span", { class: "title" }, "ScratchPads")),
    state.topic ? h("label", { class: "search-input" }, icon("search"), search) : null,
    h(
      "div",
      { class: "sidebar-scroll" },
      h(
        "div",
        { class: "sidebar-group" },
        h(
          "div",
          { class: "sidebar-heading" },
          h("span", { class: "grow" }, t("Topics")),
          state.me.kind === "user" ? h("button", { class: "icon-button small", title: t("New topic"), onclick: newTopic }, icon("plus")) : null,
        ),
        topics,
        state.topics.length ? null : h("div", { class: "sidebar-heading" }, t("No topics yet")),
      ),
    ),
    h(
      "div",
      { class: "sidebar-footer" },
      h("button", { class: "sidebar-button", onclick: openSettings }, avatar(state.me.name), h("span", { class: "title" }, state.me.name)),
      h(
        "button",
        {
          class: "icon-button",
          title: t("Inbox"),
          "aria-label": t("Inbox"),
          "aria-haspopup": "dialog",
          "aria-expanded": closeInbox ? "true" : "false",
          onclick: openInbox,
        },
        icon("bell"),
        h("span", { id: "inbox-count", class: "dot" + (state.unread ? "" : " hidden") }, state.unread),
      ),
    ),
  );
}

function treeView(prefix, depth) {
  const kids = state.nodes.filter((n) => dirname(n.path) === prefix);
  kids.sort((a, b) => (a.kind === b.kind ? a.path.localeCompare(b.path) : a.kind === "dir" ? -1 : 1));
  return kids.map((n) => {
    const isDir = n.kind === "dir";
    const open = state.expanded.has(n.path);
    const item = sidebarLink({
      label: basename(n.path),
      iconEl: icon(isDir ? "folder" : "doc"),
      active: state.path === n.path,
      depth,
      disclosure: isDir ? open : null,
      count: n.comments,
      key: n.path,
      onclick: (e) => {
        if (isDir && (e.target.closest(".disclosure") || state.path === n.path)) {
          open ? state.expanded.delete(n.path) : state.expanded.add(n.path);
          return render();
        }
        if (isDir) state.expanded.add(n.path);
        go(state.topic.slug, n.path);
      },
    });
    return [item, isDir && open ? treeView(n.path, depth + 1) : null];
  });
}

function canWrite() {
  return state.topic && (state.topic.perm === "write" || state.topic.perm === "admin");
}

function breadcrumb() {
  const parts = state.path ? state.path.split("/") : [];
  const base = `#/t/${encodeURIComponent(state.topic.slug)}/`;
  const crumbs = [h("a", { class: "crumb" + (parts.length ? "" : " current"), href: base }, state.topic.title)];
  parts.forEach((p, i) => {
    crumbs.push(icon("slash", "crumb-slash"));
    crumbs.push(h("a", { class: "crumb" + (i === parts.length - 1 ? " current" : ""), href: base + enc(parts.slice(0, i + 1).join("/")) }, p));
  });
  return h("nav", { class: "breadcrumbs" }, crumbs);
}

function docPane() {
  const menu = h(
    "button",
    {
      class: "icon-button menu-btn",
      title: t("Menu"),
      onclick: () => {
        state.navOpen = !state.navOpen;
        render();
      },
    },
    icon("menu"),
  );
  if (!state.topic) {
    return h(
      "div",
      { class: "doc-pane" },
      h("div", { class: "header" }, menu),
      h(
        "div",
        { class: "empty" },
        logo(),
        h("p", null, state.me.kind === "user" ? t("Create a topic to start scratching.") : t("Ask your owner to grant you a topic.")),
      ),
    );
  }
  const actions = [];
  if (state.node) {
    if (state.editing) {
      actions.push(h("button", { class: "btn ghost", onclick: cancelEdit }, t("Cancel")));
      actions.push(h("button", { class: "btn primary", onclick: save }, t("Save")));
    } else {
      const open = state.comments.filter((c) => !c.parent_id && !c.resolved).length;
      actions.push(
        h(
          "button",
          {
            class: "btn ghost",
            title: t("Comments"),
            "aria-pressed": state.showComments ? "true" : "false",
            onclick: () => {
              state.showComments = !state.showComments;
              render();
            },
          },
          icon("comment"),
          open ? String(open) : null,
        ),
      );
      if (canWrite()) actions.push(h("button", { class: "btn", onclick: startEdit }, icon("edit"), t("Edit")));
      if (canWrite()) actions.push(iconButton("more", t("More"), nodeMenu));
    }
  } else if (canWrite()) {
    actions.push(h("button", { class: "btn ghost", onclick: () => newNode("dir") }, t("New folder")));
    actions.push(h("button", { class: "btn primary", onclick: () => newNode("file") }, icon("plus"), t("New pad")));
    if (state.path) actions.push(iconButton("more", t("More"), nodeMenu));
  }
  return h(
    "div",
    { class: "doc-pane" },
    h("header", { class: "header" }, menu, breadcrumb(), h("div", { class: "actions" }, actions)),
    state.node ? fileView() : folderView(),
  );
}

function fileView() {
  const n = state.node;
  const doc = h(
    "div",
    { class: "doc" },
    h("h1", { class: "doc-title" }, basename(n.path).replace(/\.md$/i, "")),
    meta(t("{0} updated {1}", n.updated_by, ago(n.updated_at)), t("rev {0}", n.rev)),
  );
  if (state.editing) {
    const ta = h("textarea", {
      class: "editor",
      spellcheck: "false",
      oninput: (e) => {
        state.draft = e.target.value;
        autosize(e.target);
      },
      onkeydown: (e) => {
        if ((e.metaKey || e.ctrlKey) && e.key === "s") {
          e.preventDefault();
          save();
        }
        // execCommand keeps the native undo stack, unlike setting .value.
        if (e.key === "Tab" && !e.shiftKey) {
          e.preventDefault();
          document.execCommand("insertText", false, "  ");
        }
        if (e.key === "Escape") cancelEdit();
      },
    });
    ta.value = state.draft;
    doc.append(ta);
    setTimeout(() => {
      autosize(ta);
      ta.focus({ preventScroll: true });
      ta.setSelectionRange(0, 0);
    }, 0);
    return doc;
  }
  const prose = h("div", { class: "prose", html: Markdown.render(n.content || ""), onmouseup: () => setTimeout(selectionButton, 0) });
  if (!n.content.trim()) prose.append(h("p", { class: "doc-meta" }, t("Empty pad.")));
  highlightQuotes(prose);
  doc.append(prose);
  return doc;
}

function meta(...parts) {
  return h(
    "div",
    { class: "doc-meta" },
    parts.flatMap((p, i) => (i ? [h("span", null, "·"), h("span", null, p)] : [h("span", null, p)])),
  );
}

// Measuring needs height:auto, which briefly shrinks the textarea and would
// clamp the pane's scroll to the top; restore it afterwards.
function autosize(ta) {
  const pane = ta.closest(".doc-pane");
  const top = pane ? pane.scrollTop : 0;
  ta.style.height = "auto";
  ta.style.height = Math.max(ta.scrollHeight, window.innerHeight * 0.6) + "px";
  if (pane) pane.scrollTop = top;
}

function folderView() {
  const kids = state.children.slice().sort((a, b) => (a.kind === b.kind ? a.path.localeCompare(b.path) : a.kind === "dir" ? -1 : 1));
  const title = state.path ? basename(state.path) : state.topic.title;
  const files = kids.filter((n) => n.kind === "file").length;
  return h(
    "div",
    { class: "doc" },
    h("h1", { class: "doc-title" }, title),
    meta(t("{0} folders", kids.length - files), t("{0} pads", files), t("you can {0}", t(state.topic.perm))),
    kids.length
      ? h(
          "div",
          { class: "list-rows" },
          kids.map((n) =>
            h(
              "a",
              { class: "list-row", href: `#/t/${encodeURIComponent(state.topic.slug)}/${enc(n.path)}` },
              icon(n.kind === "dir" ? "folder" : "doc"),
              h("span", { class: "name" }, basename(n.path)),
              h("span", { class: "meta" }, n.kind === "file" ? `${n.updated_by} · ${ago(n.updated_at)}` : ""),
            ),
          ),
        )
      : h("div", { class: "empty" }, t("Nothing here yet.")),
  );
}

// Wrap the first occurrence of each open thread's quote in <mark>s, across
// formatting and block boundaries; whitespace matches loosely.
function highlightQuotes(root) {
  const threads = state.comments.filter((c) => !c.parent_id && !c.resolved && c.quote);
  for (const thread of threads) {
    const nodes = [];
    let text = "";
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    for (let n; (n = walker.nextNode()); ) {
      if (n.parentElement.closest("mark")) continue;
      nodes.push({ n, start: text.length });
      text += n.data;
    }
    const pattern = thread.quote
      .trim()
      .split(/\s+/)
      .map((w) => w.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"))
      .join("\\s*");
    const m = pattern && new RegExp(pattern).exec(text);
    if (!m) continue;
    const from = m.index;
    const to = m.index + m[0].length;
    for (const { n, start } of nodes) {
      const a = Math.max(from, start) - start;
      const b = Math.min(to, start + n.data.length) - start;
      if (a >= b || !n.data.slice(a, b).trim()) continue;
      const range = document.createRange();
      range.setStart(n, a);
      range.setEnd(n, b);
      range.surroundContents(
        h("mark", { "data-thread": thread.id, class: state.activeThread === thread.id ? "active" : "", onclick: () => focusThread(thread.id) }),
      );
    }
  }
}

function selectionButton() {
  document.querySelector(".selection-btn")?.remove();
  if (state.me.kind !== "user") return;
  const sel = window.getSelection();
  const text = sel && sel.toString().trim();
  if (!text || !sel.rangeCount) return;
  const prose = document.querySelector(".doc > .prose");
  if (!prose || !prose.contains(sel.anchorNode)) return;
  const rect = sel.getRangeAt(0).getBoundingClientRect();
  const pane = document.querySelector(".doc-pane");
  const paneRect = pane.getBoundingClientRect();
  const btn = h(
    "button",
    {
      class: "btn selection-btn",
      onmousedown: (e) => e.preventDefault(),
      onclick: () => {
        state.pendingQuote = text.slice(0, 2000);
        state.showComments = true;
        btn.remove();
        render();
        setTimeout(() => document.querySelector(".new-thread textarea")?.focus(), 0);
      },
    },
    icon("comment"),
    t("Comment"),
  );
  pane.append(btn);
  // Above the selection unless that would cover the header, centred on it.
  const headerBottom = pane.querySelector(".header").getBoundingClientRect().bottom;
  const above = rect.top - btn.offsetHeight - 8;
  const y = above < headerBottom ? rect.bottom + 8 : above;
  btn.style.top = y - paneRect.top + pane.scrollTop + "px";
  btn.style.left = Math.max(8, Math.min(pane.clientWidth - btn.offsetWidth - 8, rect.left - paneRect.left + rect.width / 2 - btn.offsetWidth / 2)) + "px";
}

function focusThread(id) {
  state.activeThread = id;
  state.showComments = true;
  render();
  setTimeout(() => document.querySelector(`.thread[data-id="${id}"]`)?.scrollIntoView({ block: "nearest", behavior: "smooth" }), 0);
}

// Comments: a person highlights text to start a thread; everyone else,
// agents included, replies underneath it.
function commentsPane() {
  const roots = state.comments.filter((c) => !c.parent_id);
  const replies = (id) => state.comments.filter((c) => c.parent_id === id);
  const open = roots.filter((c) => !c.resolved);
  const resolved = roots.filter((c) => c.resolved);
  const scroll = h("div", { class: "comments-scroll" });

  if (state.pendingQuote && state.me.kind === "user") scroll.append(composer(state.pendingQuote, null));
  for (const thread of open) scroll.append(threadView(thread, replies(thread.id)));
  if (!open.length && !state.pendingQuote) {
    scroll.append(
      h(
        "div",
        { class: "comments-empty" },
        icon("comment"),
        h(
          "p",
          null,
          state.me.kind === "user" ? t("Select text in the pad to start a comment.") : t("Agents reply to comments; people start them by selecting text."),
        ),
      ),
    );
  }
  if (resolved.length) {
    scroll.append(
      h(
        "button",
        {
          class: "resolved-toggle",
          "aria-expanded": String(state.showResolved),
          onclick: () => {
            state.showResolved = !state.showResolved;
            render();
          },
        },
        icon("disclosure", state.showResolved ? "" : "collapsed"),
        t("Resolved ({0})", resolved.length),
      ),
    );
    if (state.showResolved) for (const thread of resolved) scroll.append(threadView(thread, replies(thread.id)));
  }

  return h(
    "aside",
    { class: "comments-pane" },
    h(
      "div",
      { class: "comments-head" },
      t("Comments"),
      iconButton("close", t("Close"), () => {
        state.showComments = false;
        render();
      }),
    ),
    scroll,
  );
}

function openReply(id) {
  state.activeThread = id;
  state.replyOpen = id;
  render();
  setTimeout(() => document.querySelector(`.thread[data-id="${id}"] .composer textarea`)?.focus({ preventScroll: true }), 0);
}

function threadView(root, replies) {
  const active = state.activeThread === root.id;
  const el = h("div", {
    class: "thread" + (root.resolved ? " resolved" : "") + (active ? " active" : ""),
    "data-id": root.id,
    "data-key": "thread:" + root.id,
    onclick: (e) => {
      if (e.target.closest("button, textarea, a, .composer")) return;
      if (!root.resolved && state.replyOpen !== root.id) openReply(root.id);
      else if (!active) {
        state.activeThread = root.id;
        render();
      }
    },
  });
  if (root.quote) {
    el.append(
      h(
        "div",
        {
          class: "thread-quote",
          title: root.quote,
          onclick: () => {
            state.activeThread = root.id;
            render();
            document.querySelector(`mark[data-thread="${root.id}"]`)?.scrollIntoView({ block: "center", behavior: "smooth" });
          },
        },
        root.quote,
      ),
    );
  }
  el.append(commentView(root, false));
  if (replies.length)
    el.append(
      h(
        "div",
        { class: "replies" },
        replies.map((c) => commentView(c, true)),
      ),
    );
  if (!root.resolved && state.replyOpen === root.id) {
    el.append(composer(null, root.id));
  } else {
    el.append(
      h(
        "div",
        { class: "thread-actions" },
        root.resolved ? null : h("button", { class: "btn ghost small", onclick: () => openReply(root.id) }, t("Reply")),
        h(
          "button",
          {
            class: "btn ghost small",
            onclick: async () => {
              try {
                await api("POST", `/api/comments/${root.id}/${root.resolved ? "reopen" : "resolve"}`);
                await Promise.all([loadComments(), loadTree()]);
                render();
              } catch (e) {
                fail(e);
              }
            },
          },
          root.resolved ? null : icon("check"),
          root.resolved ? t("Reopen") : t("Resolve"),
        ),
      ),
    );
  }
  return el;
}

function commentView(c, isReply) {
  const mine = c.author === state.me.name;
  return h(
    "div",
    { class: "comment" + (isReply ? " reply" : "") },
    h(
      "div",
      { class: "comment-head" },
      avatar(c.author),
      h("span", { class: "author" }, c.author),
      c.author.includes("/") ? h("span", { class: "badge" }, t("agent")) : null,
      h("span", { class: "time" }, ago(c.created_at)),
      mine || state.topic.perm === "admin"
        ? h(
            "button",
            {
              class: "icon-button small del",
              title: t("Delete"),
              onclick: () =>
                confirm2(t("Delete comment"), isReply ? t("Delete this reply?") : t("Delete this comment and its replies?"), t("Delete"), async () => {
                  await api("DELETE", `/api/comments/${c.id}`);
                  await Promise.all([loadComments(), loadTree()]);
                  render();
                }),
            },
            icon("trash"),
          )
        : null,
    ),
    h("div", { class: "comment-body prose", html: Markdown.render(c.body) }),
  );
}

function composer(quote, parent) {
  const key = parent ? "reply:" + parent : "new:" + (state.node && state.node.path);
  const ta = h("textarea", {
    class: "input",
    rows: parent ? 1 : 2,
    "data-key": key,
    placeholder: parent ? t("Reply…") : t("Add a comment…"),
    oninput: (e) => {
      state.drafts[key] = e.target.value;
    },
    onkeydown: (e) => {
      if ((e.metaKey || e.ctrlKey) && e.key === "Enter") submit();
      if (e.key === "Escape") {
        if (parent) state.replyOpen = null;
        else state.pendingQuote = null;
        e.target.blur();
        render();
      }
    },
  });
  ta.value = state.drafts[key] || "";
  // Read from state, not `ta`: morph may have kept an older textarea.
  const submit = async () => {
    const body = (state.drafts[key] || "").trim();
    if (!body || state.posting) return;
    state.posting = true;
    try {
      const payload = { body };
      if (parent) payload.parent = String(parent);
      else if (quote) payload.quote = quote;
      const c = await api("POST", commentsURL(state.node.path), payload);
      delete state.drafts[key];
      state.replyOpen = null;
      state.pendingQuote = null;
      state.activeThread = parent || c.id;
      await Promise.all([loadComments(), loadTree()]);
      render();
      setTimeout(() => document.querySelector(`.thread[data-id="${state.activeThread}"]`)?.scrollIntoView({ block: "nearest", behavior: "smooth" }), 0);
    } catch (e) {
      fail(e);
    } finally {
      state.posting = false;
    }
  };
  return h(
    "div",
    { class: "composer " + (parent ? "reply" : "new-thread") },
    quote ? h("div", { class: "thread-quote" }, quote) : null,
    ta,
    h(
      "div",
      { class: "row" },
      h("span", { class: "hint" }, t("⌘↩ to send")),
      h(
        "button",
        {
          class: "btn ghost small",
          onclick: () => {
            if (parent) state.replyOpen = null;
            else state.pendingQuote = null;
            render();
          },
        },
        t("Cancel"),
      ),
      h("button", { class: "btn primary small", onclick: submit }, parent ? t("Reply") : t("Comment")),
    ),
  );
}

// ---------- actions ----------

function startEdit() {
  state.editing = true;
  state.draft = state.node.content;
  render();
}

function cancelEdit() {
  if (!confirmLeave()) return;
  state.editing = false;
  render();
}

async function save() {
  try {
    await api("PUT", filesURL(state.node.path), state.draft, { "If-Match": `"${state.node.rev}"` });
    state.editing = false;
    await loadTree();
    await loadPath(state.node.path);
    toast(t("Saved"));
    render();
  } catch (e) {
    if (e.status !== 412) return fail(e);
    const fresh = await api("GET", filesURL(state.node.path) + "?meta=1").catch(() => null);
    if (!fresh) return;
    confirm2(t("Save conflict"), t("Someone else changed this pad. Overwrite their changes with yours?"), t("Overwrite"), async () => {
      state.node.rev = fresh.rev;
      await save();
    });
  }
}

function newNode(kind) {
  const base = state.node ? dirname(state.node.path) : state.path;
  prompt2(
    kind === "dir" ? t("New folder") : t("New pad"),
    [{ placeholder: kind === "dir" ? t("folder name") : t("name.md") }],
    async (name) => {
      if (!name) throw new Error(t("Name is required"));
      if (kind === "file" && !/\.[a-z0-9]+$/i.test(name)) name += ".md";
      const p = base ? base + "/" + name : name;
      if (kind === "dir") await api("PUT", filesURL(p) + "/", "");
      else {
        try {
          await api("PUT", filesURL(p), `# ${name.replace(/\.md$/i, "")}\n\n`, { "If-None-Match": "*" });
        } catch (e) {
          throw e.status === 412 ? new Error(t("A pad with that name already exists")) : e;
        }
      }
      await loadTree();
      go(state.topic.slug, p);
    },
    t("Create"),
  );
}

function nodeMenu() {
  const p = state.path;
  modal((box, close) => {
    box.append(
      h("h2", null, basename(p)),
      h(
        "div",
        { class: "row node-actions" },
        h(
          "button",
          {
            class: "btn",
            onclick: () => {
              close();
              prompt2(
                t("Move / rename"),
                [{ value: p }],
                async (to) => {
                  await api("POST", `/api/t/${encodeURIComponent(state.topic.slug)}/move`, { from: p, to });
                  await loadTree();
                  go(state.topic.slug, to);
                },
                t("Move"),
              );
            },
          },
          t("Move / rename"),
        ),
        state.node
          ? h(
              "button",
              {
                class: "btn",
                onclick: () => {
                  close();
                  showHistory();
                },
              },
              t("History"),
            )
          : null,
        h("span", { style: "flex:1" }),
        h(
          "button",
          {
            class: "btn danger",
            onclick: () => {
              close();
              confirm2(t("Delete"), state.node ? t("Delete {0}?", p) : t("Delete {0} and everything inside?", p), t("Delete"), async () => {
                await api("DELETE", filesURL(p) + "?recursive=1");
                await loadTree();
                go(state.topic.slug, dirname(p));
              });
            },
          },
          t("Delete"),
        ),
      ),
    );
  });
}

async function showHistory() {
  const n = state.node;
  const revs = await api("GET", `/api/t/${encodeURIComponent(state.topic.slug)}/history/${enc(n.path)}`);
  modal((box) => {
    const view = h("div", { class: "secret", style: "max-height:300px;overflow:auto;user-select:text" }, t("Pick a revision."));
    box.append(
      h("h2", null, t("History")),
      h("p", { class: "hint" }, n.path),
      h(
        "div",
        { class: "list" },
        revs.map((r) =>
          h(
            "div",
            { class: "list-item" },
            h("span", { class: "grow" }, `${t("rev {0}", r.rev)} · ${r.author} · ${ago(r.created_at)} · ${r.size}B`),
            h(
              "button",
              {
                class: "btn small",
                onclick: async () => {
                  view.textContent = await api("GET", filesURL(n.path) + "?rev=" + r.rev);
                },
              },
              t("View"),
            ),
          ),
        ),
      ),
      view,
    );
  });
}

function newTopic() {
  prompt2(
    t("New topic"),
    [{ placeholder: t("slug (a-z, 0-9, -)") }, { placeholder: t("Title") }],
    async (slug, title) => {
      const created = await api("POST", "/api/topics", { slug, title });
      state.topics = await api("GET", "/api/topics");
      go(created.slug);
    },
    t("Create"),
  );
}

async function searchTopic(q) {
  const hits = await api("GET", `/api/t/${encodeURIComponent(state.topic.slug)}/search?q=${encodeURIComponent(q)}`).catch(fail);
  if (!hits) return;
  modal((box, close) => {
    box.append(
      h("h2", null, t("Search “{0}”", q)),
      hits.length
        ? h(
            "div",
            { class: "list", style: "margin-top:12px" },
            hits.map((hit) =>
              h(
                "div",
                {
                  class: "list-item clickable",
                  onclick: () => {
                    close();
                    go(state.topic.slug, hit.path);
                  },
                },
                h("div", { class: "grow" }, h("strong", null, hit.path), h("div", { class: "excerpt" }, hit.snippet)),
              ),
            ),
          )
        : h("p", { class: "hint" }, t("No matches.")),
    );
  });
}

let closeInbox = null;

async function openInbox(e) {
  // currentTarget is reset once the handler awaits, so read it first.
  const trigger = e.currentTarget;
  const viaKeyboard = e.detail === 0;
  if (closeInbox) return closeInbox();
  const items = await api("GET", "/api/inbox?all=1").catch(fail);
  if (!items || closeInbox) return;
  const rect = trigger.getBoundingClientRect();
  const close = (refocus) => {
    pop.remove();
    closeInbox = null;
    document.removeEventListener("mousedown", onDown);
    document.removeEventListener("keydown", onKey);
    trigger.setAttribute("aria-expanded", "false");
    if (refocus) trigger.focus();
  };
  const onDown = (ev) => {
    if (!pop.contains(ev.target) && !trigger.contains(ev.target)) close();
  };
  const onKey = (ev) => {
    if (ev.key === "Escape") close(true);
  };
  const pop = h(
    "div",
    { class: "popover", role: "dialog", "aria-label": t("Inbox") },
    h(
      "div",
      { class: "comments-head" },
      t("Inbox"),
      h(
        "button",
        {
          class: "btn ghost small",
          onclick: async () => {
            await api("POST", "/api/inbox/read");
            close(true);
            refreshInbox();
          },
        },
        t("Mark all read"),
      ),
    ),
    items.length
      ? items.map((it) =>
          h(
            "div",
            {
              class: "inbox-item" + (it.read ? "" : " unread"),
              tabindex: "0",
              role: "button",
              onkeydown: (ev) => {
                if (ev.key === "Enter" || ev.key === " ") {
                  ev.preventDefault();
                  ev.currentTarget.click();
                }
              },
              onclick: async () => {
                close();
                await api("POST", "/api/inbox/read", { id: String(it.id) }).catch(() => {});
                refreshInbox();
                state.pendingThread = it.comment_id || null;
                go(it.topic, it.path);
              },
            },
            h("span", { class: "unread-dot" + (it.read ? " read" : "") }),
            h(
              "div",
              { style: "min-width:0" },
              h(
                "div",
                null,
                tx(it.kind === "mention" ? "{0} mentioned you in {1}" : "{0} replied in {1}", h("strong", null, it.actor), h("strong", null, it.path)),
              ),
              h("div", { class: "excerpt" }, it.excerpt),
              h("div", { class: "when" }, `${it.topic} · ${ago(it.created_at)}`),
            ),
          ),
        )
      : h("div", { class: "empty", style: "padding:32px" }, t("All caught up")),
  );
  pop.style.left = Math.max(8, rect.left - 300) + "px";
  pop.style.bottom = window.innerHeight - rect.top + 8 + "px";
  pop.style.maxHeight = Math.min(440, rect.top - 16) + "px";
  document.body.append(pop);
  trigger.setAttribute("aria-expanded", "true");
  closeInbox = close;
  document.addEventListener("mousedown", onDown);
  document.addEventListener("keydown", onKey);
  if (viaKeyboard) pop.querySelector(".inbox-item, .btn")?.focus();
}

// ---------- settings ----------

// installPrompt is pasted into an agent as-is: it installs the skill, stores
// the credentials in the agent's global memory and checks they work.
function installPrompt(name, token) {
  const url = location.origin;
  const pin = state.tlsInfo && state.tlsInfo.pin;
  const curl = pin ? `curl -fsS -k --pinnedpubkey "sha256//${pin}"` : "curl -fsS";
  return [
    `You now have access to ScratchPads (${url}), a shared space where people and agents write markdown pads and talk in comments. You are the agent "${name}".`,
    "",
    "1. Install the skill (Claude Code path shown; use your own skills folder otherwise):",
    `   mkdir -p ~/.claude/skills/scratchpads && ${curl} ${url}/skill.md -o ~/.claude/skills/scratchpads/SKILL.md`,
    "",
    "2. Save these to your GLOBAL memory (not a project file) so every session can use ScratchPads:",
    `   - SP_URL=${url}`,
    `   - SP_TOKEN=${token}   (secret: never print it, commit it, or write it into pads or comments)`,
    pin ? `   - SP_PIN=${pin}   (pins the self-signed certificate)` : null,
    `   - My ScratchPads identity is ${name}.`,
    "",
    "3. Check it works; this must print your name:",
    `   ${curl} -H "Authorization: Bearer ${token}" ${url}/api/me`,
  ]
    .filter((line) => line !== null)
    .join("\n");
}

// A one-time secret with a copy button; it stays until the tab is left.
function secretBlock(caption, text) {
  const btn = h(
    "button",
    {
      type: "button",
      class: "btn small",
      onclick: async () => {
        try {
          await navigator.clipboard.writeText(text);
          btn.textContent = t("Copied");
        } catch (_) {
          toast(t("Copy failed, select the text instead"));
        }
      },
    },
    t("Copy"),
  );
  return h("div", { class: "secret-block" }, h("p", { class: "hint" }, caption), h("div", { class: "secret" }, text), h("div", { class: "row end" }, btn));
}

// formRow is an inline form: Enter submits, the button is disabled while the
// request runs, and errors show under the row instead of in a toast.
function formRow(fields, label, onSubmit) {
  const err = h("div", { class: "form-error" });
  const btn = h("button", { class: "btn primary" }, label);
  const form = h(
    "form",
    {
      class: "inline-form",
      onsubmit: async (e) => {
        e.preventDefault();
        if (btn.disabled) return;
        btn.disabled = true;
        err.textContent = "";
        try {
          await onSubmit();
        } catch (ex) {
          err.textContent = ex.message;
        } finally {
          btn.disabled = false;
        }
      },
    },
    h("div", { class: "row" }, fields, btn),
    err,
  );
  return form;
}

function openSettings() {
  modal((box, close) => {
    const body = h("div", { class: "settings-body" });
    const tabs =
      state.me.kind === "user"
        ? [
            ["agents", t("My agents")],
            ["access", t("Topic access")],
            ["tokens", t("Tokens")],
          ]
        : [["tokens", t("Tokens")]];
    if (state.me.admin) tabs.push(["users", t("Users")]);
    let current = tabs[0][0];
    const tabBar = h("div", { class: "tabs" });
    // Tabs render off-screen and swap in only if still selected, so quick
    // switching never mixes two tabs and the modal does not collapse.
    const show = (key, after) => {
      current = key;
      tabBar.replaceChildren(
        ...tabs.map(([k, label]) => h("button", { type: "button", class: "tab" + (k === current ? " active" : ""), onclick: () => show(k) }, label)),
      );
      body.style.minHeight = body.offsetHeight + "px";
      const slot = h("div");
      const reload = (next) => show(key, next);
      ({ agents: agentsTab, access: accessTab, tokens: tokensTab, users: usersTab })[key](slot, reload).then(() => {
        if (current !== key) return;
        if (after) slot.append(after);
        const scroller = box.closest(".modal") || box;
        const top = scroller.scrollTop;
        body.replaceChildren(slot);
        body.style.minHeight = "";
        scroller.scrollTop = top;
      }, fail);
    };
    box.append(
      h(
        "div",
        { class: "modal-head" },
        h("h2", null, t("Settings")),
        langToggle(() => {
          close();
          render();
          openSettings();
        }),
        h(
          "button",
          {
            type: "button",
            class: "btn ghost small",
            onclick: async () => {
              await api("DELETE", "/api/session").catch(() => {});
              location.hash = "#/";
              location.reload();
            },
          },
          t("Sign out"),
        ),
        iconButton("close", t("Close"), close),
      ),
      h("p", { class: "hint" }, t("Signed in as {0}", state.me.name + (state.me.admin ? t(" (admin)") : ""))),
      tabBar,
      body,
    );
    show(current);
  });
}

async function agentsTab(body, reload) {
  const agents = await api("GET", "/api/agents");
  const slot = h("input", { class: "input", placeholder: t("slot, e.g. 0, 1, codex"), autocomplete: "off" });
  const out = h("div");
  body.append(
    h("h3", null, t("Agent group")),
    h("p", { class: "hint" }, t("Agents are named {0}/<slot>. Grant {0}/* to give the whole group access to a topic.", state.me.name)),
    h(
      "div",
      { class: "list" },
      agents.length
        ? agents.map((a) =>
            h(
              "div",
              { class: "list-item" },
              avatar(a.name),
              h("span", { class: "grow" }, a.name),
              h(
                "button",
                {
                  type: "button",
                  class: "btn small",
                  onclick: async (e) => {
                    const btn = e.currentTarget;
                    btn.disabled = true;
                    try {
                      const res = await api("POST", "/api/tokens", { for: a.name, label: "settings" });
                      out.replaceChildren(secretBlock(t("Paste this into {0}. The token is shown only once:", a.name), installPrompt(a.name, res.token)));
                    } catch (ex) {
                      fail(ex);
                    }
                    btn.disabled = false;
                  },
                },
                t("New token"),
              ),
              h(
                "button",
                {
                  type: "button",
                  class: "btn ghost small danger",
                  onclick: () =>
                    confirm2(t("Delete agent"), t("Delete {0} and its tokens?", a.name), t("Delete"), async () => {
                      await api("DELETE", `/api/agents/${encodeURIComponent(a.name.split("/")[1])}`);
                      reload();
                    }),
                },
                t("Delete"),
              ),
            ),
          )
        : h("div", { class: "list-item muted" }, t("No agents yet.")),
    ),
    formRow([slot], t("Add agent"), async () => {
      const res = await api("POST", "/api/agents", { slot: slot.value.trim() });
      reload(secretBlock(t("Paste this into {0}. The token is shown only once:", res.agent.name), installPrompt(res.agent.name, res.token)));
    }),
    out,
  );
}

async function accessTab(body, reload) {
  if (!state.topic) return body.append(h("p", { class: "hint" }, t("Open a topic first.")));
  const grants = await api("GET", `/api/topics/${encodeURIComponent(state.topic.slug)}/grants`);
  const admin = state.topic.perm === "admin";
  const subject = h("input", { class: "input", placeholder: t("alice, alice/1 or alice/*"), autocomplete: "off" });
  const perm = h(
    "select",
    { class: "input perm-select" },
    ["read", "write", "admin"].map((p) => h("option", { value: p, selected: p === "write" }, t(p))),
  );
  const slug = state.topic.slug;
  body.append(
    h("h3", null, state.topic.title),
    h("p", { class: "hint" }, t("Read can view and comment. Write can edit pads. Admin can manage access.")),
    h(
      "div",
      { class: "list" },
      grants.map((g) =>
        h(
          "div",
          { class: "list-item" },
          avatar(g.subject.replace("/*", "")),
          h("span", { class: "grow" }, g.subject),
          h("span", { class: "badge" }, t(g.perm)),
          admin
            ? h(
                "button",
                {
                  type: "button",
                  class: "btn ghost small danger",
                  onclick: async () => {
                    try {
                      await api("DELETE", `/api/topics/${encodeURIComponent(slug)}/grants/${enc(g.subject)}`);
                      reload();
                    } catch (e) {
                      fail(e);
                    }
                  },
                },
                t("Remove"),
              )
            : null,
        ),
      ),
    ),
    admin
      ? formRow([subject, perm], t("Grant"), async () => {
          await api("PUT", `/api/topics/${encodeURIComponent(slug)}/grants/${enc(subject.value.trim())}`, { perm: perm.value });
          reload();
        })
      : null,
    admin
      ? h(
          "div",
          { class: "row danger-zone" },
          h(
            "button",
            {
              type: "button",
              class: "btn danger",
              onclick: () =>
                confirm2(
                  t("Delete topic"),
                  t('Type "{0}" to delete this topic and everything in it.', slug),
                  t("Delete topic"),
                  async () => {
                    await api("DELETE", `/api/topics/${encodeURIComponent(slug)}`);
                    state.topics = await api("GET", "/api/topics");
                    state.topic = null;
                    document.querySelectorAll(".modal-backdrop").forEach((m) => m.remove());
                    go(null);
                  },
                  slug,
                ),
            },
            t("Delete topic"),
          ),
        )
      : null,
  );
}

async function tokensTab(body, reload) {
  const tokens = await api("GET", "/api/tokens");
  body.append(
    h("h3", null, t("Active tokens")),
    h(
      "div",
      { class: "list" },
      tokens.map((tok) =>
        h(
          "div",
          { class: "list-item" },
          h("span", { class: "grow" }, `#${tok.id} ${tok.principal}${tok.label ? " · " + tok.label : ""}`),
          tok.current ? h("span", { class: "badge" }, t("this session")) : null,
          h("span", { class: "muted" }, tok.last_used_at ? t("used {0}", ago(tok.last_used_at)) : t("never used")),
          h(
            "button",
            {
              type: "button",
              class: "btn ghost small danger",
              onclick: () =>
                confirm2(
                  t("Revoke token"),
                  tok.current ? t("This is the session you are using. Revoking it signs you out.") : t("Revoke token #{0}?", tok.id),
                  t("Revoke"),
                  async () => {
                    await api("DELETE", `/api/tokens/${tok.id}`);
                    if (tok.current) location.reload();
                    else reload();
                  },
                ),
            },
            t("Revoke"),
          ),
        ),
      ),
    ),
  );
}

async function usersTab(body, reload) {
  const all = await api("GET", "/api/users");
  const name = h("input", { class: "input", placeholder: t("username"), autocomplete: "off" });
  body.append(
    h("h3", null, t("Users")),
    h(
      "div",
      { class: "list" },
      all
        .filter((u) => u.kind === "user")
        .map((u) =>
          h(
            "div",
            { class: "list-item" },
            avatar(u.name),
            h("span", { class: "grow" }, u.name),
            u.admin ? h("span", { class: "badge" }, t("admin")) : null,
            h("span", { class: "muted" }, t("{0} agents", all.filter((a) => a.owner === u.name).length)),
            u.name !== state.me.name
              ? h(
                  "button",
                  {
                    type: "button",
                    class: "btn ghost small danger",
                    onclick: () =>
                      confirm2(t("Delete user"), t("Delete user {0} and all their agents?", u.name), t("Delete"), async () => {
                        await api("DELETE", `/api/users/${encodeURIComponent(u.name)}`);
                        reload();
                      }),
                  },
                  t("Delete"),
                )
              : null,
          ),
        ),
    ),
    formRow([name], t("Add user"), async () => {
      const res = await api("POST", "/api/users", { name: name.value.trim() });
      reload(secretBlock(t("Token for {0}, shown once:", res.user.name), res.token));
    }),
  );
}

// ---------- live refresh ----------

async function poll() {
  if (!state.me || document.hidden || state.editing || !state.topic) return;
  // A live text selection would be lost if the highlighted marks moved.
  const sel = window.getSelection();
  if ((sel && !sel.isCollapsed) || document.querySelector(".selection-btn")) return;
  try {
    const before = JSON.stringify([state.nodes, state.comments, state.node && state.node.rev]);
    await loadTree();
    if (state.node) {
      const fresh = await api("GET", filesURL(state.node.path) + "?meta=1");
      fresh.content = fresh.content || "";
      if (fresh.rev !== state.node.rev) {
        state.node = fresh;
        state.draft = fresh.content;
      }
      await loadComments();
    }
    if (JSON.stringify([state.nodes, state.comments, state.node && state.node.rev]) !== before || Date.now() - lastRender > 60000) render();
  } catch (_) {}
}

window.addEventListener("hashchange", route);
document.addEventListener("selectionchange", () => {
  const sel = window.getSelection();
  if (!sel || sel.isCollapsed) document.querySelector(".selection-btn")?.remove();
});
window.addEventListener("beforeunload", (e) => {
  if (state.editing && state.node && state.draft !== state.node.content) e.preventDefault();
});
setInterval(poll, 6000);
setInterval(refreshInbox, 15000);

(async () => {
  await loadMe();
  await route();
  refreshInbox();
})();
