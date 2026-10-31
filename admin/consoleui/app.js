// The fit console: a login, then a form for every part of the config made
// from fit's JSON Schema, a raw JSON editor, and the versions applied. It
// reads and changes the config through the console's /api/admin/, the Admin
// API's endpoints, sending the whole config with If-Match on each save.
"use strict";

const $ = (id) => document.getElementById(id);

const state = {
  csrf: "",
  schema: null,
  draft: null,   // the config being edited, admin aside
  etag: "",
  version: 0,
  dirty: false,
  section: "",
  errors: [],    // the problems of the last save, as {path, message}
};

// ---- requests ----

async function api(method, path, body, headers = {}) {
  const init = { method, headers: { ...headers }, credentials: "same-origin" };
  if (method !== "GET") init.headers["X-Fit-Csrf"] = state.csrf;
  if (body !== undefined) {
    init.headers["Content-Type"] = "application/json";
    init.body = typeof body === "string" ? body : JSON.stringify(body, null, 2);
  }
  const resp = await fetch(path, init);
  if (resp.status === 401 && path !== "/api/login") {
    showLogin("Your session ended; log in again.");
    throw new Error("logged out");
  }
  return resp;
}

async function json(resp) {
  try {
    return await resp.json();
  } catch {
    return {};
  }
}

// ---- messages ----

function message(text, kind) {
  const m = $("message");
  m.textContent = text;
  m.className = "banner" + (kind ? " " + kind : "");
  m.hidden = !text;
}

function el(tag, attrs = {}, ...children) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === undefined || v === null || v === false) continue;
    if (k === "class") e.className = v;
    else if (k === "text") e.textContent = v;
    else if (k.startsWith("on")) e.addEventListener(k.slice(2), v);
    else e.setAttribute(k, v === true ? "" : v);
  }
  for (const c of children) if (c !== undefined && c !== null) e.append(c);
  return e;
}

// ---- login ----

function showLogin(text) {
  $("app").hidden = $("tabs").hidden = $("who").hidden = true;
  $("login").hidden = false;
  message(text || "", text ? "warn" : "");
  $("username").focus();
}

$("login").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const resp = await fetch("/api/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    credentials: "same-origin",
    body: JSON.stringify({ username: $("username").value, password: $("password").value }),
  });
  const body = await json(resp);
  $("password").value = "";
  if (!resp.ok) {
    message(body.error || "Logging in failed (" + resp.status + ").");
    return;
  }
  await enter(body);
});

$("logout").addEventListener("click", async () => {
  if (state.dirty && !confirm("Discard the changes not saved?")) return;
  await api("POST", "/api/logout");
  state.csrf = "";
  state.dirty = false;
  showLogin("Logged out.");
});

async function enter(session) {
  state.csrf = session.csrf;
  $("user").textContent = session.user;
  $("login").hidden = true;
  $("app").hidden = $("tabs").hidden = $("who").hidden = false;
  message("");
  if (!state.schema) state.schema = await json(await api("GET", "/api/schema"));
  await reload();
  showTab(location.hash.slice(1) || "config");
}

// ---- the config ----

async function reload() {
  const resp = await api("GET", "/api/admin/config");
  if (!resp.ok) {
    message("Reading the config failed: " + ((await json(resp)).error || resp.status));
    return;
  }
  state.etag = resp.headers.get("ETag") || "";
  state.version = resp.headers.get("Fit-Config-Version") || "";
  state.draft = await resp.json();
  state.errors = [];
  setDirty(false);
  renderProblems();
  renderSections();
  renderSection();
  $("raw").value = JSON.stringify(state.draft, null, 2);
}

function setDirty(d) {
  state.dirty = d;
  $("status").textContent = (d ? "Changed, not saved. " : "") + "Running config version " + state.version + ".";
  $("save").disabled = !d;
  $("discard").disabled = !d;
}

function changed() {
  setDirty(true);
  $("raw").value = JSON.stringify(state.draft, null, 2);
  markSections();
}

$("raw").addEventListener("input", () => setDirty(true));

$("discard").addEventListener("click", () => {
  if (confirm("Discard the changes not saved?")) reload();
});

$("save").addEventListener("click", save);

async function save() {
  let body;
  if (currentTab() === "json") {
    try {
      state.draft = JSON.parse($("raw").value);
    } catch (e) {
      message("The JSON does not parse: " + e.message);
      return;
    }
  }
  body = JSON.stringify(state.draft, null, 2);
  $("save").disabled = true;
  const resp = await api("PUT", "/api/admin/config", body, { "If-Match": state.etag });
  const res = await json(resp);
  if (resp.ok) {
    const warnings = res.warnings || [];
    await reload();
    message("Applied as config version " + res.version + "." + (warnings.length ? " " + warnings.join("; ") : ""), warnings.length ? "warn" : "ok");
    return;
  }
  $("save").disabled = false;
  if (resp.status === 412) {
    message("The config was changed elsewhere since it was read. Discard your changes to read it again, or copy them from the JSON tab first.");
    return;
  }
  state.errors = res.errors || [];
  message(state.errors.length ? "" : res.error || "Saving failed (" + resp.status + ").");
  renderProblems();
  renderSection();
}

function renderProblems() {
  const p = $("problems");
  p.replaceChildren();
  p.hidden = state.errors.length === 0;
  if (p.hidden) return;
  const ul = el("ul");
  for (const e of state.errors) {
    const where = e.path || "the config";
    const section = (e.path || "").split(/[.[]/)[0];
    const link = section && section in rootProps()
      ? el("button", { class: "link", type: "button", text: where, onclick: () => { showTab("config"); openSection(section); } })
      : el("code", { text: where });
    ul.append(el("li", {}, link, ": " + e.message));
  }
  p.append(el("strong", { text: "The config was not applied; the running config stays." }), ul);
}

function hasError(path) {
  return state.errors.some((e) => e.path === path || (e.path || "").startsWith(path + ".") || (e.path || "").startsWith(path + "["));
}

// ---- tabs ----

function currentTab() {
  return document.querySelector("nav a.active")?.dataset.tab || "config";
}

function showTab(name) {
  if (!["config", "json", "versions"].includes(name)) name = "config";
  if (currentTab() === "json" && name !== "json" && state.dirty) {
    try {
      state.draft = JSON.parse($("raw").value);
      renderSections();
      renderSection();
    } catch (e) {
      message("The JSON does not parse, so the form keeps what it had: " + e.message);
    }
  }
  for (const a of document.querySelectorAll("nav a")) a.classList.toggle("active", a.dataset.tab === name);
  for (const s of document.querySelectorAll(".tab")) s.hidden = s.id !== name;
  if (name === "versions") loadVersions();
}

for (const a of document.querySelectorAll("nav a")) {
  a.addEventListener("click", (ev) => {
    ev.preventDefault();
    history.replaceState(null, "", "#" + a.dataset.tab);
    showTab(a.dataset.tab);
  });
}

window.addEventListener("beforeunload", (ev) => {
  if (state.dirty) ev.preventDefault();
});

// ---- the schema ----

function resolve(s) {
  let seen = 0;
  while (s && s.$ref && seen++ < 32) {
    const name = decodeURIComponent(s.$ref.replace(/^#\/\$defs\//, "")).replace(/~1/g, "/").replace(/~0/g, "~");
    const def = state.schema.$defs[name] || {};
    s = { ...def, description: s.description || def.description, default: s.default ?? def.default };
  }
  return s || {};
}

function rootProps() {
  const props = { ...(state.schema.properties || {}) };
  delete props.admin;
  return props;
}

function isModuleRef(s) {
  return s.type === "object" && s.properties && s.properties.module && Array.isArray(s.properties.module.enum) && s.properties.params;
}

function paramsSchema(s, module) {
  for (const c of s.allOf || []) {
    if (c.if?.properties?.module?.const === module) return resolve(c.then.properties.params);
  }
  return null;
}

function kind(s) {
  if (isModuleRef(s)) return "module";
  if (s.oneOf) return "scalar";
  if (s.type === "array") return "array";
  if (s.type === "object" && s.properties) return "object";
  if (s.type === "object" && s.additionalProperties && typeof s.additionalProperties === "object") return "map";
  if (["string", "integer", "number", "boolean"].includes(s.type)) return s.type;
  return "json";
}

// A slot is a place in the draft: its container's slot and its key there.
function rootSlot() {
  return { get: () => state.draft, set: (v) => { state.draft = v ?? {}; }, path: "" };
}

function child(parent, key, path) {
  return {
    path,
    get() {
      const c = parent.get();
      return c == null ? undefined : c[key];
    },
    set(v) {
      let c = parent.get();
      if (c == null) {
        if (v === undefined) return;
        c = typeof key === "number" ? [] : {};
        parent.set(c);
      }
      if (v === undefined) delete c[key];
      else c[key] = v;
    },
  };
}

function join(path, key) {
  if (typeof key === "number") return path + "[" + key + "]";
  return path ? path + "." + key : key;
}

// ---- the form ----

function renderSections() {
  const ul = $("sections");
  ul.replaceChildren();
  const props = rootProps();
  if (!state.section || !(state.section in props)) state.section = "listeners" in props ? "listeners" : Object.keys(props)[0];
  for (const name of Object.keys(props)) {
    if (props[name].deprecated && !(name in (state.draft || {}))) continue;
    const a = el("a", { href: "#config", "data-section": name, text: name, onclick: (ev) => { ev.preventDefault(); openSection(name); } });
    ul.append(el("li", {}, a));
  }
  markSections();
}

function markSections() {
  for (const a of document.querySelectorAll("#sections a")) {
    a.classList.toggle("active", a.dataset.section === state.section);
    a.classList.toggle("set", state.draft != null && a.dataset.section in state.draft);
  }
}

function openSection(name) {
  state.section = name;
  markSections();
  renderSection();
  window.scrollTo(0, 0);
}

function renderSection() {
  const form = $("form");
  form.replaceChildren();
  if (!state.draft || !state.schema) return;
  const name = state.section;
  const s = resolve(rootProps()[name]);
  form.append(el("h2", { text: name }), el("p", { class: "doc", text: s.description || "" }));
  const slot = child(rootSlot(), name, name);
  form.append(control(s, slot, () => renderSection(), true));
}

// control returns the control for a value of schema s at slot; rerender
// draws the section again, after a change of shape.
function control(s, slot, rerender, top) {
  s = resolve(s);
  switch (kind(s)) {
    case "object": return objectControl(s, slot, rerender, top);
    case "array": return arrayControl(s, slot, rerender);
    case "map": return mapControl(s, slot, rerender);
    case "module": return moduleControl(s, slot, rerender);
    case "boolean": return boolControl(s, slot);
    case "json": return jsonControl(s, slot);
    default: return scalarControl(s, slot);
  }
}

function objectControl(s, slot, rerender, top) {
  const v = slot.get();
  if (v === undefined && !top) {
    return el("div", {}, el("span", { class: "unset", text: "left out " }),
      el("button", { class: "small", type: "button", text: "Set", onclick: () => { slot.set({}); changed(); rerender(); } }));
  }
  const box = el("div", { class: "object" });
  const required = new Set(s.required || []);
  for (const [name, ps] of Object.entries(s.properties)) {
    const p = resolve(ps);
    if (p.deprecated && !(v && name in v)) continue;
    const cs = child(slot, name, join(slot.path, name));
    box.append(field(name, p, cs, rerender, required.has(name)));
  }
  if (!top && v !== undefined) {
    box.append(el("div", { class: "actions" },
      el("button", { class: "small danger", type: "button", text: "Leave out", onclick: () => { slot.set(undefined); changed(); rerender(); } })));
  }
  return box;
}

function field(name, s, slot, rerender, required) {
  const k = kind(s);
  const nested = k === "object" || k === "array" || k === "map" || k === "module";
  const label = el("div", { class: "name" }, name, required ? el("span", { class: "req", text: " *" }) : null);
  const f = el("div", { class: "field" + (hasError(slot.path) ? " error" : ""), "data-path": slot.path });
  if (nested) {
    const d = el("details", { class: "node" + (hasError(slot.path) ? " error" : "") });
    if (hasError(slot.path) || slot.get() !== undefined && k !== "object") d.open = true;
    d.append(el("summary", {}, el("span", { class: "label", text: name }), required ? el("span", { class: "req", text: "*" }) : null,
      el("span", { class: "muted", text: summary(s, slot.get()) })));
    d.append(el("div", { class: "body" }, el("p", { class: "doc muted", text: s.description || "" }), control(s, slot, rerender)));
    return d;
  }
  f.append(label, el("div", { class: "control" }, control(s, slot, rerender)));
  if (s.description) f.append(el("div", { class: "doc", text: s.description }));
  return f;
}

function summary(s, v) {
  if (v === undefined) return "left out";
  if (Array.isArray(v)) return v.length + (v.length === 1 ? " item" : " items");
  if (v && typeof v === "object") {
    if (v.module) return v.module;
    const n = Object.keys(v).length;
    return n + (n === 1 ? " field" : " fields");
  }
  return "";
}

function itemLabel(v, i) {
  if (v && typeof v === "object") {
    for (const k of ["name", "module", "address", "username", "path"]) if (typeof v[k] === "string" && v[k]) return "#" + i + " " + v[k];
  }
  if (typeof v === "string" || typeof v === "number") return "#" + i + " " + v;
  return "#" + i;
}

function emptyOf(s) {
  s = resolve(s);
  switch (kind(s)) {
    case "object": {
      const o = {};
      for (const r of s.required || []) {
        const p = resolve(s.properties[r]);
        const k = kind(p);
        if (k === "array") o[r] = [];
        else if (k === "object" || k === "map") o[r] = {};
        else if (k === "module") o[r] = { module: p.properties.module.enum[0] };
        else if (p.enum) o[r] = p.enum[0];
        else if (k === "string") o[r] = "";
      }
      return o;
    }
    case "module": return { module: s.properties.module.enum[0] };
    case "array": return [];
    case "map": return {};
    case "boolean": return false;
    case "integer": case "number": return 0;
    default: return s.enum ? s.enum[0] : "";
  }
}

function arrayControl(s, slot, rerender) {
  const items = resolve(s.items || {});
  const list = el("div", { class: "list" });
  const v = slot.get() || [];
  const simple = !["object", "array", "map", "module", "json"].includes(kind(items));
  v.forEach((item, i) => {
    const cs = child(slot, i, join(slot.path, i));
    const buttons = [
      el("button", { class: "small", type: "button", text: "↑", title: "move up", disabled: i === 0, onclick: (ev) => { ev.preventDefault(); move(slot, i, -1); rerender(); } }),
      el("button", { class: "small", type: "button", text: "↓", title: "move down", disabled: i === v.length - 1, onclick: (ev) => { ev.preventDefault(); move(slot, i, 1); rerender(); } }),
      el("button", { class: "small danger", type: "button", text: "Remove", onclick: (ev) => { ev.preventDefault(); removeAt(slot, i); rerender(); } }),
    ];
    if (simple) {
      list.append(el("div", { class: "row" + (hasError(cs.path) ? " error" : "") }, control(items, cs, rerender), ...buttons));
      return;
    }
    const d = el("details", { class: "node" + (hasError(cs.path) ? " error" : "") });
    d.open = hasError(cs.path) || v.length === 1;
    d.append(el("summary", {}, el("span", { class: "label", text: itemLabel(item, i) }), el("span", { class: "spacer" }), ...buttons));
    d.append(el("div", { class: "body" }, control(items, cs, rerender, true)));
    list.append(d);
  });
  list.append(el("div", { class: "actions" }, el("button", { class: "small", type: "button", text: "Add", onclick: () => {
    const arr = slot.get() || [];
    arr.push(emptyOf(items));
    slot.set(arr);
    changed();
    rerender();
  } })));
  return list;
}

function move(slot, i, d) {
  const arr = slot.get();
  [arr[i], arr[i + d]] = [arr[i + d], arr[i]];
  changed();
}

function removeAt(slot, i) {
  const arr = slot.get();
  arr.splice(i, 1);
  if (arr.length === 0) slot.set(undefined);
  changed();
}

function mapControl(s, slot, rerender) {
  const vs = resolve(s.additionalProperties);
  const box = el("div", { class: "map" });
  const v = slot.get() || {};
  for (const key of Object.keys(v)) {
    const keyInput = el("input", { class: "key", value: key, "aria-label": "name" });
    keyInput.addEventListener("change", () => {
      const nk = keyInput.value;
      if (!nk || nk === key || nk in v) { keyInput.value = key; return; }
      const o = {};
      for (const k of Object.keys(v)) o[k === key ? nk : k] = v[k];
      slot.set(o);
      changed();
      rerender();
    });
    const cs = child(slot, key, join(slot.path, key));
    box.append(el("div", { class: "row" }, keyInput, control(vs, cs, rerender, true),
      el("button", { class: "small danger", type: "button", text: "Remove", onclick: () => {
        const o = slot.get();
        delete o[key];
        if (Object.keys(o).length === 0) slot.set(undefined);
        changed();
        rerender();
      } })));
  }
  box.append(el("div", { class: "actions" }, el("button", { class: "small", type: "button", text: "Add", onclick: () => {
    const o = slot.get() || {};
    let k = "name", n = 1;
    while (k in o) k = "name" + ++n;
    o[k] = emptyOf(vs);
    slot.set(o);
    changed();
    rerender();
  } })));
  return box;
}

function moduleControl(s, slot, rerender) {
  const v = slot.get();
  const box = el("div", { class: "module" });
  const sel = el("select", { "aria-label": "module" }, el("option", { value: "", text: "(left out)" }),
    ...s.properties.module.enum.map((m) => el("option", { value: m, text: m, selected: v && v.module === m })));
  sel.addEventListener("change", () => {
    slot.set(sel.value ? { module: sel.value } : undefined);
    changed();
    rerender();
  });
  box.append(el("div", { class: "field" }, el("div", { class: "name", text: "module" }), el("div", { class: "control" }, sel)));
  if (v && v.module) {
    const ps = paramsSchema(s, v.module);
    const pslot = child(slot, "params", join(slot.path, "params"));
    if (ps && ps.properties && Object.keys(ps.properties).length > 0) {
      box.append(el("div", { class: "params" }, el("div", { class: "muted", text: "params" }), objectControl(ps, pslot, rerender, true)));
    } else if (!ps) {
      box.append(el("div", { class: "field" }, el("div", { class: "name", text: "params" }), el("div", { class: "control" }, jsonControl({}, pslot))));
    }
  }
  return box;
}

function boolControl(s, slot) {
  const c = el("input", { type: "checkbox", checked: slot.get() === true });
  c.addEventListener("change", () => {
    slot.set(c.checked ? true : undefined);
    changed();
  });
  return c;
}

function scalarControl(s, slot) {
  const v = slot.get();
  const placeholder = s.default !== undefined ? String(typeof s.default === "object" ? JSON.stringify(s.default) : s.default) : "";
  if (s.enum) {
    const sel = el("select", {}, el("option", { value: "", text: placeholder ? "(left out: " + placeholder + ")" : "(left out)" }),
      ...s.enum.map((e) => el("option", { value: e, text: e, selected: v === e })));
    sel.addEventListener("change", () => {
      slot.set(sel.value === "" ? undefined : sel.value);
      changed();
    });
    return sel;
  }
  const numeric = s.type === "integer" || s.type === "number";
  const input = el("input", {
    type: numeric ? "number" : "text",
    value: v === undefined ? "" : typeof v === "object" ? JSON.stringify(v) : String(v),
    placeholder,
    step: s.type === "integer" ? "1" : numeric ? "any" : undefined,
    min: s.minimum, max: s.maximum,
    pattern: s.pattern && !numeric ? s.pattern.replace(/^\^/, "").replace(/\$$/, "") : undefined,
  });
  input.addEventListener("input", () => {
    const t = input.value;
    let nv;
    if (t === "") nv = undefined;
    else if (numeric) nv = Number(t);
    else if (s.oneOf && /^-?\d+$/.test(t) && s.oneOf.some((o) => o.type === "integer")) nv = Number(t);
    else nv = t;
    slot.set(nv);
    changed();
  });
  return input;
}

function jsonControl(s, slot) {
  const v = slot.get();
  const t = el("textarea", { class: "params", spellcheck: "false", placeholder: "JSON" });
  t.value = v === undefined ? "" : JSON.stringify(v, null, 2);
  t.addEventListener("input", () => {
    if (t.value.trim() === "") {
      slot.set(undefined);
      t.setCustomValidity("");
      changed();
      return;
    }
    try {
      slot.set(JSON.parse(t.value));
      t.setCustomValidity("");
      changed();
    } catch (e) {
      t.setCustomValidity(e.message);
    }
  });
  return t;
}

// ---- versions ----

async function loadVersions() {
  const resp = await api("GET", "/api/admin/versions");
  const vs = await json(resp);
  const tbody = $("versions-table").tBodies[0];
  tbody.replaceChildren();
  for (const v of (Array.isArray(vs) ? vs : vs.versions || []).slice().reverse()) {
    const view = el("button", { class: "link", type: "button", text: "View", onclick: () => viewVersion(v.version) });
    const back = v.version == state.version ? el("span", { class: "muted", text: "running" })
      : el("button", { class: "link", type: "button", text: "Roll back to it", onclick: () => rollback(v.version) });
    tbody.append(el("tr", {}, el("td", { text: String(v.version) }), el("td", { text: new Date(v.applied).toLocaleString() }),
      el("td", { text: v.source }), el("td", { text: v.bytes + " B" }), el("td", {}, view, " · ", back)));
  }
}

async function viewVersion(n) {
  const resp = await api("GET", "/api/admin/versions/" + n);
  const text = await resp.text();
  let pretty = text;
  try { pretty = JSON.stringify(JSON.parse(text), null, 2); } catch {}
  $("version-title").textContent = "Version " + n;
  $("version-body").textContent = pretty;
  $("version-view").hidden = false;
}

async function rollback(n) {
  if (state.dirty && !confirm("Rolling back discards the changes not saved. Go on?")) return;
  if (!confirm("Apply version " + n + " again, as a new version?")) return;
  const resp = await api("POST", "/api/admin/rollback/" + n, undefined, { "If-Match": state.etag });
  const res = await json(resp);
  if (!resp.ok) {
    message("Rolling back failed: " + (res.error || resp.status));
    return;
  }
  await reload();
  await loadVersions();
  message("Version " + n + " applied again as version " + res.version + ".", "ok");
}

// ---- start ----

(async () => {
  const resp = await fetch("/api/session", { credentials: "same-origin" });
  if (resp.ok) await enter(await resp.json());
  else showLogin();
})();
