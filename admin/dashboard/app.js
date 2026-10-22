// fit's dashboard: read-only pages of the Admin API's GET endpoints,
// /status, /config/effective and /versions. It sends nothing but GETs.
"use strict";

const tokenKey = "fit-admin-token";
const refreshEvery = 5000;
const tabs = ["overview", "routes", "services", "upstreams", "certificates", "versions"];
let timer = null;

// el makes an element with a class, and children: elements, or strings
// set as text, never as HTML.
function el(tag, cls, ...children) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  for (const c of children) {
    if (c === null || c === undefined) continue;
    e.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return e;
}

function pill(text, kind) { return el("span", "pill " + (kind || "plain"), text); }
function code(text) { return el("code", "", text); }

function cells(...values) {
  const tr = el("tr");
  for (const v of values) tr.append(v instanceof Node && (v.tagName === "TD" || v.tagName === "TH") ? v : el("td", "", v));
  return tr;
}

function fill(tableId, rows, columns, emptyText) {
  const body = document.querySelector("#" + tableId + " tbody");
  body.replaceChildren(...rows);
  if (rows.length === 0) {
    const td = el("td", "empty", emptyText);
    td.colSpan = columns;
    body.append(el("tr", "", td));
  }
}

function list(values) {
  const span = el("span");
  (values || []).forEach((v) => span.append(pill(v)));
  if (!values || values.length === 0) span.append(el("span", "muted", "all"));
  return span;
}

function when(t) {
  if (!t) return "";
  const d = new Date(t);
  return isNaN(d) ? String(t) : d.toLocaleString();
}

function ago(t) {
  const s = Math.round((Date.now() - new Date(t)) / 1000);
  if (!isFinite(s)) return "";
  if (s < 60) return s + "s ago";
  if (s < 3600) return Math.round(s / 60) + "m ago";
  if (s < 86400) return Math.round(s / 3600) + "h ago";
  return Math.round(s / 86400) + "d ago";
}

function bytes(n) {
  if (n < 1024) return n + " B";
  if (n < 1 << 20) return (n / 1024).toFixed(1) + " KiB";
  return (n / (1 << 20)).toFixed(1) + " MiB";
}

class Unauthorized extends Error {}

// get reads path from the Admin API, with the token when one is kept.
async function get(path, asText) {
  const headers = {};
  const token = sessionStorage.getItem(tokenKey);
  if (token) headers["Authorization"] = "Bearer " + token;
  const resp = await fetch(path, { method: "GET", headers, cache: "no-store", credentials: "omit" });
  if (resp.status === 401) throw new Unauthorized();
  if (!resp.ok) {
    let msg = resp.status + " " + resp.statusText;
    try { msg = (await resp.json()).error || msg; } catch (e) { /* not JSON */ }
    throw new Error(path + ": " + msg);
  }
  return asText ? resp.text() : resp.json();
}

// sourcesByPath maps each element's JSON pointer in the effective config
// to the source it came from.
function sourcesByPath(effective) {
  const m = new Map();
  for (const s of effective.sources || []) m.set(s.path, s.source);
  return m;
}

function matchSummary(m) {
  if (!m) return el("span", "muted", "everything");
  const span = el("span");
  const add = (label, values) => {
    if (!values || values.length === 0) return;
    span.append(el("div", "", el("span", "muted", label + " "), ...values.map((v) => code(v))));
  };
  add("host", m.host);
  add("path", m.path);
  add("path_regexp", m.path_regexp);
  add("method", m.method);
  for (const [k, vs] of Object.entries(m.header || {})) add("header " + k, vs.length ? vs : ["(present)"]);
  for (const [k, vs] of Object.entries(m.query || {})) add("query " + k, vs.length ? vs : ["(present)"]);
  add("remote_ip", m.remote_ip);
  if (m.expr) add("expr", [m.expr]);
  return span.childNodes.length ? span : el("span", "muted", "everything");
}

function renderOverview(status, effective) {
  const up = status.upstreams.reduce((a, u) => a + u.backends.filter((b) => b.up).length, 0);
  const all = status.upstreams.reduce((a, u) => a + u.backends.length, 0);
  const soon = status.certificates.filter((c) => new Date(c.not_after) - Date.now() < 14 * 86400e3).length;
  const cfg = effective.config || {};
  const tile = (label, value, sub, kind) => {
    const v = el("div", "value", value);
    if (kind) v.style.color = "var(--" + kind + ")";
    return el("div", "tile", el("div", "label", label), v, sub ? el("div", "sub", sub) : null);
  };
  document.getElementById("tiles").replaceChildren(
    tile("Config version", status.version, status.source + ", " + ago(status.applied)),
    tile("Reloads", status.reloads.succeeded, status.reloads.failed + " failed", status.reloads.failed ? "warn" : null),
    tile("Open connections", status.connections.open, status.connections.refused + " refused"),
    tile("Routes", (cfg.routes || []).length, (cfg.services || []).length + " layer 4 services"),
    tile("Backends up", up + " / " + all, status.upstreams.length + " upstreams", up < all ? "bad" : null),
    tile("Certificates", status.certificates.length, soon ? soon + " expire within 14 days" : "none expire within 14 days", soon ? "warn" : null),
  );
  fill("listeners", status.listeners.map((l) => cells(l.name, code(l.address), list(l.protocols))), 3, "No listeners");

  const counts = new Map();
  for (const name of effective.precedence || []) counts.set(name, { listeners: 0, routes: 0, upstreams: 0, services: 0 });
  for (const s of effective.sources || []) {
    const kind = s.path.split("/")[1];
    if (!counts.has(s.source)) counts.set(s.source, { listeners: 0, routes: 0, upstreams: 0, services: 0 });
    if (kind in counts.get(s.source) && s.path.split("/").length === 3) counts.get(s.source)[kind]++;
  }
  fill("sources", [...counts].map(([name, c]) => cells(name, c.listeners, c.routes, c.upstreams, c.services)), 5, "No sources");

  const shadowed = effective.shadowed || [];
  const box = document.getElementById("shadowed");
  box.replaceChildren();
  if (shadowed.length) {
    box.append(el("h2", "", "Shadowed"));
    const t = el("table", "", el("thead", "", cells(...["Source", "Element", "Name"].map((h) => el("th", "", h)))));
    const body = el("tbody");
    shadowed.forEach((s) => body.append(cells(s.source, code(s.from), s.name)));
    t.append(body);
    box.append(t);
  }
}

function renderRoutes(effective) {
  const sources = sourcesByPath(effective);
  const routes = (effective.config || {}).routes || [];
  fill("routes-table", routes.map((r, i) => {
    const name = el("td", "", r.name ? el("strong", "", r.name) : el("span", "muted", "#" + i));
    if (r.priority) name.append(el("div", "muted", "priority " + r.priority));
    const handler = r.handler || {};
    const target = handler.params && handler.params.upstream ? el("div", "muted", "→ " + handler.params.upstream) : null;
    const mw = el("span");
    (r.middleware || []).forEach((m) => mw.append(pill(m.module || "?")));
    return cells(name, list(r.listeners), matchSummary(r.match), mw, el("span", "", code(handler.module || "?"), target), pill(sources.get("/routes/" + i) || "?"));
  }), 6, "No routes");
}

function renderServices(effective) {
  const sources = sourcesByPath(effective);
  const services = (effective.config || {}).services || [];
  fill("services-table", services.map((s, i) => cells(
    el("strong", "", s.name || "#" + i), list(s.listeners), s.upstream ? code(s.upstream) : "",
    (s.server_names || []).length ? list(s.server_names) : "", s.proxy_protocol || "", pill(sources.get("/services/" + i) || "?"),
  )), 6, "No layer 4 services");
}

function renderUpstreams(status, effective) {
  const configured = new Map();
  const sources = sourcesByPath(effective);
  ((effective.config || {}).upstreams || []).forEach((u, i) => configured.set(u.name, { u, source: sources.get("/upstreams/" + i) }));
  const box = document.getElementById("upstreams-list");
  const blocks = status.upstreams.map((u) => {
    const conf = configured.get(u.name) || {};
    const up = u.backends.filter((b) => b.up).length;
    const kind = up === u.backends.length ? "ok" : up === 0 ? "bad" : "warn";
    const head = el("div", "upstream-head", el("span", "name", u.name), pill(up + " / " + u.backends.length + " up", kind),
      el("span", "muted", u.in_flight + " in flight"));
    if (conf.u && conf.u.load_balancing) head.append(el("span", "muted", conf.u.load_balancing));
    if (conf.source) head.append(pill(conf.source));
    const t = el("table", "", el("thead", "", cells(...["Backend", "State", "In flight", "Succeeded", "Failed"].map((h) => el("th", "", h)))));
    const body = el("tbody");
    u.backends.forEach((b) => body.append(cells(code(b.address),
      b.up ? pill("up", "ok") : pill(b.probed_down ? "down (health check)" : "down (failures)", "bad"),
      el("td", "num", b.in_flight), el("td", "num", b.succeeded), el("td", "num", b.failed))));
    t.append(body);
    const block = el("div", "upstream", head, t);
    block.dataset.text = u.name + " " + u.backends.map((b) => b.address).join(" ");
    return block;
  });
  box.replaceChildren(...blocks);
  if (blocks.length === 0) box.append(el("p", "muted", "No upstreams."));
  applyFilters();
}

function renderCertificates(status) {
  fill("certificates-table", status.certificates.map((c) => {
    const left = (new Date(c.not_after) - Date.now()) / 86400e3;
    const kind = left < 0 ? "bad" : left < 14 ? "warn" : "ok";
    const label = left < 0 ? "expired" : Math.floor(left) + " days left";
    return cells(c.listener, list(c.names), c.issuer, when(c.not_before), el("span", "", when(c.not_after), " ", pill(label, kind)), code(c.serial));
  }), 6, "No certificates");
}

function renderVersions(versions) {
  fill("versions-table", versions.versions.slice().reverse().map((v) => {
    const view = el("button", "link", "view");
    view.type = "button";
    view.addEventListener("click", () => showVersion(v.version));
    const num = el("td", "", el("strong", "", v.version));
    if (v.version === versions.current) num.append(" ", pill("running", "ok"));
    return cells(num, when(v.applied), pill(v.source), el("td", "num", bytes(v.bytes)), code(v.etag), view);
  }), 6, "No versions");
}

async function showVersion(n) {
  try {
    const text = await get("/versions/" + encodeURIComponent(n), true);
    let pretty = text;
    try { pretty = JSON.stringify(JSON.parse(text), null, 2); } catch (e) { /* as written */ }
    document.getElementById("version-title").textContent = "Version " + n;
    document.getElementById("version-body").textContent = pretty;
    document.getElementById("version-view").hidden = false;
  } catch (e) {
    showError(e);
  }
}

function showError(e) {
  if (e instanceof Unauthorized) {
    document.getElementById("login").hidden = false;
    document.querySelectorAll(".tab").forEach((t) => { t.hidden = true; });
    return;
  }
  const box = document.getElementById("error");
  box.textContent = e.message || String(e);
  box.hidden = false;
}

async function refresh() {
  try {
    const [status, effective, versions] = await Promise.all([get("/status"), get("/config/effective"), get("/versions")]);
    document.getElementById("error").hidden = true;
    document.getElementById("login").hidden = true;
    renderOverview(status, effective);
    renderRoutes(effective);
    renderServices(effective);
    renderUpstreams(status, effective);
    renderCertificates(status);
    renderVersions(versions);
    applyFilters();
    showTab();
    document.getElementById("updated").textContent = "updated " + new Date().toLocaleTimeString();
  } catch (e) {
    showError(e);
  }
}

function applyFilters() {
  document.querySelectorAll(".filter").forEach((f) => {
    const q = f.value.trim().toLowerCase();
    const target = document.getElementById(f.dataset.for);
    const items = target.tagName === "TABLE" ? target.querySelectorAll("tbody tr") : target.querySelectorAll(".upstream");
    items.forEach((it) => {
      const text = (it.dataset.text || it.textContent).toLowerCase();
      it.hidden = q !== "" && !text.includes(q);
    });
  });
}

function showTab() {
  if (!document.getElementById("login").hidden) return;
  const want = location.hash.slice(1);
  const tab = tabs.includes(want) ? want : "overview";
  document.querySelectorAll(".tab").forEach((t) => { t.hidden = t.id !== tab; });
  document.querySelectorAll("#tabs a").forEach((a) => a.classList.toggle("active", a.dataset.tab === tab));
}

function schedule() {
  clearInterval(timer);
  timer = null;
  if (document.getElementById("auto").checked) timer = setInterval(refresh, refreshEvery);
}

window.addEventListener("hashchange", showTab);
document.getElementById("auto").addEventListener("change", schedule);
document.querySelectorAll(".filter").forEach((f) => f.addEventListener("input", applyFilters));
document.getElementById("login").addEventListener("submit", (ev) => {
  ev.preventDefault();
  const v = document.getElementById("token").value.trim();
  if (v) sessionStorage.setItem(tokenKey, v);
  document.getElementById("token").value = "";
  refresh();
});

showTab();
refresh();
schedule();
