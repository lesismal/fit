// fit workbench UI core, shared by every module of the workbench. Vanilla JS, no build step.
// Load it synchronously in <head>: it applies the saved theme before the first paint.
// Never put user data into innerHTML: build nodes with UI.h (text is always set as text).
// A module gets UI.scope(server, ns): the same library, whose requests go to its server module's API, /api/m/<server>/,
// and whose words are looked up in its namespace first.
//
// fit workbench UI 核心库，workbench 各模块共用。原生 JS，无构建步骤。
// 在 <head> 中同步加载：首帧渲染前即应用已保存的主题。
// 禁止把用户数据放进 innerHTML：用 UI.h 构建节点（文本始终以文本方式设置）。
// 模块拿到 UI.scope(server, ns)：同一套库，其请求发往服务端模块的 API /api/m/<server>/，其词条先在其命名空间中查找。
(function () {
  'use strict';

  var THEME_KEY = 'fit_theme', LANG_KEY = 'fit_lang';
  // "system" follows prefers-color-scheme (no data-theme attribute). / "system" 跟随系统（不设 data-theme）。
  var THEMES = [
    ['system', 'linear-gradient(135deg,#fff 50%,#161923 50%)'], ['light', '#f5f6fa'], ['dark', '#161923'],
    ['ocean', '#0c2539'], ['sunset', '#fbf4ee'], ['graphite', '#1a1a1a']
  ];
  var LANGS = [['zh-CN', '中文'], ['en', 'English']];

  function load(k) { try { return localStorage.getItem(k); } catch (e) { return null; } }
  function save(k, v) { try { localStorage.setItem(k, v); } catch (e) { /* ignore / 忽略 */ } }

  var root = document.documentElement;
  var theme = load(THEME_KEY) || 'system';
  if (!THEMES.some(function (t) { return t[0] === theme; })) theme = 'system';
  function applyTheme(t) {
    theme = t;
    if (t === 'system') root.removeAttribute('data-theme'); else root.setAttribute('data-theme', t);
  }
  applyTheme(theme);

  var lang = load(LANG_KEY);
  if (!LANGS.some(function (l) { return l[0] === lang; })) lang = (navigator.language || '').toLowerCase().indexOf('zh') === 0 ? 'zh-CN' : 'en';
  root.setAttribute('lang', lang);

  // ---- i18n ----
  var DICT = {
    'en': {
      'theme': 'Theme', 'lang': 'Language', 'theme.system': 'Follow system', 'theme.light': 'Light', 'theme.dark': 'Dark',
      'theme.ocean': 'Ocean', 'theme.sunset': 'Sunset', 'theme.graphite': 'Graphite',
      'ok': 'OK', 'cancel': 'Cancel', 'save': 'Save', 'saved': 'Saved', 'delete': 'Delete', 'edit': 'Edit', 'close': 'Close',
      'stream.broken': 'The connection closed before the answer finished.',
      'refresh': 'Refresh', 'loading': 'Loading…', 'confirm': 'Confirm', 'add': 'Add', 'create': 'Create', 'back': 'Back',
      'logout': 'Log out', 'login': 'Log in', 'username': 'Username', 'password': 'Password', 'login.failed': 'Login failed',
      'online': 'Online', 'offline': 'Offline', 'none': 'None', 'yes': 'Yes', 'no': 'No', 'error': 'Error', 'unknown': 'Unknown',
      'ago.s': '{n}s ago', 'ago.m': '{n}m ago', 'ago.h': '{n}h ago', 'ago.d': '{n}d ago', 'never': 'never',
      'copy': 'Copy', 'copied': 'Copied', 'search': 'Search…', 'menu': 'Menu', 'actions': 'Actions',
      'config.note': 'This role\'s settings live in fit\'s config, as the llm.{role} member: what these pages change is written back into it, and kept encrypted when fit keeps its config in admin.store.'
    },
    'zh-CN': {
      'theme': '主题', 'lang': '语言', 'theme.system': '跟随系统', 'theme.light': '浅色', 'theme.dark': '深色',
      'theme.ocean': '深海', 'theme.sunset': '日落', 'theme.graphite': '石墨',
      'ok': '确定', 'cancel': '取消', 'save': '保存', 'saved': '已保存', 'delete': '删除', 'edit': '编辑', 'close': '关闭',
      'stream.broken': '连接在回复结束前断开。',
      'refresh': '刷新', 'loading': '加载中…', 'confirm': '确认', 'add': '添加', 'create': '创建', 'back': '返回',
      'logout': '退出登录', 'login': '登录', 'username': '用户名', 'password': '密码', 'login.failed': '登录失败',
      'online': '在线', 'offline': '离线', 'none': '无', 'yes': '是', 'no': '否', 'error': '错误', 'unknown': '未知',
      'ago.s': '{n} 秒前', 'ago.m': '{n} 分钟前', 'ago.h': '{n} 小时前', 'ago.d': '{n} 天前', 'never': '从未',
      'copy': '复制', 'copied': '已复制', 'search': '搜索…', 'menu': '菜单', 'actions': '操作',
      'config.note': '本角色的配置保存在 fit 配置的 llm.{role} 成员中：在这些页面上的修改会写回该配置；当 fit 把配置保存在 admin.store 中时，配置以加密方式保存。'
    }
  };
  // addI18n(dict, ns): ns, when given, keeps the words to the module that adds them (see scope).
  // addI18n(dict, ns)：给出 ns 时，词条只属于添加它们的模块（见 scope）。
  function addI18n(d, ns) {
    var pre = ns ? ns + ':' : '';
    Object.keys(d).forEach(function (l) {
      DICT[l] = DICT[l] || {};
      Object.keys(d[l]).forEach(function (k) { DICT[l][pre + k] = d[l][k]; });
    });
  }
  function word(key) { return (DICT[lang] && DICT[lang][key]) || DICT.en[key]; }
  function fill(s, vars) {
    if (vars) s = s.replace(/\{(\w+)\}/g, function (m, k) { return vars[k] != null ? vars[k] : m; });
    return s;
  }
  function t(key, vars) { return fill(word(key) || key, vars); }

  // ---- DOM ----
  // h('div.card#id', {onclick: fn, title: '…'}, child, 'text', [more]) / 构建元素
  function h(sel, attrs) {
    var m = sel.match(/^([a-z0-9]+)?([^]*)$/i);
    var el = document.createElement(m[1] || 'div');
    (m[2].match(/[.#][^.#]+/g) || []).forEach(function (p) {
      if (p[0] === '.') el.classList.add(p.slice(1)); else el.id = p.slice(1);
    });
    var start = 1;
    if (attrs && typeof attrs === 'object' && !(attrs instanceof Node) && !Array.isArray(attrs)) {
      start = 2;
      Object.keys(attrs).forEach(function (k) {
        var v = attrs[k];
        if (v == null || v === false) return;
        if (k.slice(0, 2) === 'on') el.addEventListener(k.slice(2), v);
        else if (k === 'class') String(v).split(/\s+/).filter(Boolean).forEach(function (c) { el.classList.add(c); });
        else if (k === 'style' && typeof v === 'object') Object.keys(v).forEach(function (s) { el.style.setProperty(s.replace(/[A-Z]/g, function (c) { return '-' + c.toLowerCase(); }), v[s]); });
        else if (k === 'value') el.value = v;
        else if (k === 'checked' || k === 'disabled' || k === 'selected' || k === 'hidden') el[k] = !!v;
        else el.setAttribute(k, v === true ? '' : v);
      });
    }
    for (var i = start; i < arguments.length; i++) append(el, arguments[i]);
    return el;
  }
  function append(el, c) {
    if (c == null || c === false) return;
    if (Array.isArray(c)) { c.forEach(function (x) { append(el, x); }); return; }
    el.appendChild(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  function clear(el) { while (el.firstChild) el.removeChild(el.firstChild); return el; }
  function mount(el) { clear(el); for (var i = 1; i < arguments.length; i++) append(el, arguments[i]); return el; }

  // ---- Icons (24×24 stroke paths) / 图标 ----
  var ICONS = {
    logo: '<path d="M12 2 3 7v10l9 5 9-5V7l-9-5Z"/><path d="m3 7 9 5 9-5M12 12v10"/>',
    dashboard: '<rect x="3" y="3" width="7" height="9" rx="1.5"/><rect x="14" y="3" width="7" height="5" rx="1.5"/><rect x="14" y="12" width="7" height="9" rx="1.5"/><rect x="3" y="16" width="7" height="5" rx="1.5"/>',
    server: '<rect x="3" y="3" width="18" height="7" rx="2"/><rect x="3" y="14" width="18" height="7" rx="2"/><path d="M7 6.5h.01M7 17.5h.01"/>',
    gpu: '<rect x="2" y="6" width="20" height="12" rx="2"/><circle cx="9" cy="12" r="3"/><path d="M15 10h3M15 14h3M6 18v3M18 18v3"/>',
    cube: '<path d="M21 16V8l-9-5-9 5v8l9 5 9-5Z"/><path d="m3.3 7 8.7 5 8.7-5M12 22V12"/>',
    chat: '<path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2Z"/>',
    terminal: '<path d="m4 17 6-6-6-6M12 19h8"/>',
    link: '<path d="M10 13a5 5 0 0 0 7.5.5l3-3a5 5 0 0 0-7-7l-1.7 1.7"/><path d="M14 11a5 5 0 0 0-7.5-.5l-3 3a5 5 0 0 0 7 7l1.7-1.7"/>',
    globe: '<circle cx="12" cy="12" r="10"/><path d="M2 12h20M12 2a15 15 0 0 1 0 20M12 2a15 15 0 0 0 0 20"/>',
    settings: '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1Z"/>',
    log: '<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8Z"/><path d="M14 2v6h6M8 13h8M8 17h5"/>',
    users: '<path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M22 21v-2a4 4 0 0 0-3-3.9M16 3.1a4 4 0 0 1 0 7.8"/>',
    key: '<circle cx="7.5" cy="15.5" r="5.5"/><path d="m21 2-9.6 9.6M15.5 7.5l3 3L22 7l-3-3"/>',
    refresh: '<path d="M21 12a9 9 0 1 1-2.6-6.4L21 8"/><path d="M21 3v5h-5"/>',
    plus: '<path d="M12 5v14M5 12h14"/>',
    trash: '<path d="M3 6h18M8 6V4h8v2M19 6l-1 14H6L5 6"/>',
    edit: '<path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z"/>',
    play: '<path d="m6 4 14 8-14 8Z"/>',
    stop: '<rect x="5" y="5" width="14" height="14" rx="2"/>',
    download: '<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M7 10l5 5 5-5M12 15V3"/>',
    upload: '<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M17 8l-5-5-5 5M12 3v12"/>',
    swap: '<path d="M16 3h5v5M4 20 21 3M21 16v5h-5M15 15l6 6M4 4l5 5"/>',
    send: '<path d="m22 2-7 20-4-9-9-4Z"/><path d="M22 2 11 13"/>',
    logout: '<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9"/>',
    menu: '<path d="M3 12h18M3 6h18M3 18h18"/>',
    x: '<path d="M18 6 6 18M6 6l12 12"/>',
    check: '<path d="M20 6 9 17l-5-5"/>',
    alert: '<path d="M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0Z"/><path d="M12 9v4M12 17h.01"/>',
    cpu: '<rect x="4" y="4" width="16" height="16" rx="2"/><rect x="9" y="9" width="6" height="6"/><path d="M9 1v3M15 1v3M9 20v3M15 20v3M20 9h3M20 14h3M1 9h3M1 14h3"/>',
    memory: '<path d="M6 19v-3M10 19v-3M14 19v-3M18 19v-3M8 11V9M16 11V9M12 11V9M2 15h20M2 7a2 2 0 0 1 2-2h16a2 2 0 0 1 2 2v12H2Z"/>',
    disk: '<ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M3 5v14c0 1.7 4 3 9 3s9-1.3 9-3V5"/><path d="M3 12c0 1.7 4 3 9 3s9-1.3 9-3"/>',
    zap: '<path d="M13 2 3 14h9l-1 8 10-12h-9Z"/>',
    thermo: '<path d="M14 4v10.5a4 4 0 1 1-4 0V4a2 2 0 1 1 4 0Z"/>',
    fan: '<path d="M12 12c-3-5-1-9 2-9s3 4-2 9Zm0 0c5 3 9 1 9-2s-4-3-9 2Zm0 0c3 5 1 9-2 9s-3-4 2-9Zm0 0c-5-3-9-1-9 2s4 3 9-2Z"/>',
    activity: '<path d="M22 12h-4l-3 9L9 3l-3 9H2"/>',
    book: '<path d="M4 19.5A2.5 2.5 0 0 1 6.5 17H20V2H6.5A2.5 2.5 0 0 0 4 4.5v15ZM20 17v5H6.5a2.5 2.5 0 0 1 0-5"/>',
    sun: '<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/>',
    info: '<circle cx="12" cy="12" r="10"/><path d="M12 16v-4M12 8h.01"/>',
    copy: '<rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>',
    chev: '<path d="m9 18 6-6-6-6"/>',
    fold: '<path d="m15 18-6-6 6-6"/>',
    route: '<circle cx="6" cy="19" r="3"/><circle cx="18" cy="5" r="3"/><path d="M9 19h8.5a3.5 3.5 0 0 0 0-7h-11a3.5 3.5 0 0 1 0-7H15"/>',
    shield: '<path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10Z"/>',
    lock: '<rect x="3" y="11" width="18" height="11" rx="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/>',
    layers: '<path d="m12 2 10 5-10 5L2 7Z"/><path d="m2 17 10 5 10-5M2 12l10 5 10-5"/>',
    history: '<path d="M3 12a9 9 0 1 0 3-6.7L3 8"/><path d="M3 3v5h5M12 7v5l4 2"/>',
    code: '<path d="m16 18 6-6-6-6M8 6l-6 6 6 6"/>',
    plug: '<path d="M12 22v-5M9 8V2M15 8V2M18 8v4a6 6 0 0 1-12 0V8Z"/>',
    forward: '<path d="M15 17l5-5-5-5"/><path d="M4 18v-2a4 4 0 0 1 4-4h12"/>',
    radio: '<circle cx="12" cy="12" r="2"/><path d="M16.2 7.8a6 6 0 0 1 0 8.4M7.8 16.2a6 6 0 0 1 0-8.4M19.1 4.9a10 10 0 0 1 0 14.2M4.9 19.1a10 10 0 0 1 0-14.2"/>',
    listen: '<path d="M5 12a7 7 0 0 1 14 0M8.5 12a3.5 3.5 0 0 1 7 0"/><circle cx="12" cy="12" r="1"/><path d="M12 13v8"/>',
    pod: '<path d="M12 2.8 20 7.3v9.4L12 21.2 4 16.7V7.3z"/><path d="M4 7.3 12 12l8-4.7M12 12v9.2"/>',
    tunnel: '<path d="M3 8h14M13 4l4 4-4 4M21 16H7M11 12l-4 4 4 4"/>',
    proxy: '<rect x="4" y="5" width="16" height="11" rx="1.5"/><path d="M2 19.5h20M10 8.5l-2 2 2 2M14 8.5l2 2-2 2"/>',
    hub: '<circle cx="12" cy="5.5" r="2.5"/><circle cx="5" cy="18" r="2.5"/><circle cx="19" cy="18" r="2.5"/><path d="M10.8 7.7 6.2 15.8M13.2 7.7l4.6 8.1M7.5 18h9"/>',
    save: '<path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2Z"/><path d="M17 21v-8H7v8M7 3v5h8"/>',
    undo: '<path d="M3 7v6h6"/><path d="M21 17a9 9 0 0 0-15-6.7L3 13"/>'
  };
  function icon(name, cls) {
    var span = document.createElement('span');
    span.innerHTML = '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' + (ICONS[name] || ICONS.info) + '</svg>';
    var svg = span.firstChild;
    if (cls) svg.setAttribute('class', cls);
    return svg;
  }

  // ---- HTTP ----
  // Every request that is not GET/HEAD carries the CSRF header X-Fit-Csrf with the token the server handed out at
  // login (or in GET /api/session). The token lives only in this variable, never in storage.
  // 每个非 GET/HEAD 请求都带 CSRF 请求头 X-Fit-Csrf，值为服务端在登录（或 GET /api/session）时下发的令牌。
  // 令牌只保存在此变量中，不写入任何存储。
  var CSRF = 'X-Fit-Csrf';
  var csrfToken = '';
  var onUnauthorized = null;
  function setCsrf(v) { csrfToken = v || ''; }
  function addCsrf(method, headers) { if (method !== 'GET' && method !== 'HEAD' && csrfToken) headers[CSRF] = csrfToken; }
  // httpError builds the Error of a failed reply: JSON {"error": "message", "code": <int>}.
  // httpError 构建失败应答的 Error：JSON {"error": "message", "code": <int>}。
  function httpError(r, txt) {
    var j = null;
    try { j = txt ? JSON.parse(txt) : null; } catch (e) { /* not json / 非 JSON */ }
    var msg = j && typeof j === 'object' ? (typeof j.error === 'string' ? j.error : (j.error && j.error.message) || j.message) : null;
    var err = new Error(msg || (r.status + ' ' + r.statusText));
    err.status = r.status; err.code = j && typeof j === 'object' && j.code != null ? j.code : undefined;
    return err;
  }
  // api(method, path, body) → Promise of the reply's JSON body (null for 204 or an empty body); a non-2xx status
  // rejects with an Error carrying message, status and code.
  // api(method, path, body) → 应答 JSON 体的 Promise（204 或空应答体为 null）；非 2xx 时 reject 带 message、status 与 code 的 Error。
  // quiet (internal): a 401 does not call the unauthorized callback (the session check and the login form handle it).
  // quiet（内部使用）：401 时不调用未登录回调（由会话检查与登录表单自行处理）。
  function api(method, path, body, quiet) {
    if (this && this.base && path.indexOf('/api/') === 0) path = this.base + path.slice(4);
    var opt = { method: method, headers: {}, credentials: 'same-origin' };
    addCsrf(method, opt.headers);
    if (body !== undefined) { opt.headers['Content-Type'] = 'application/json'; opt.body = JSON.stringify(body); }
    return fetch(path, opt).then(function (r) {
      return r.text().then(function (txt) {
        if (!r.ok) {
          if (r.status === 401 && onUnauthorized && !quiet) onUnauthorized();
          throw httpError(r, txt);
        }
        if (r.status === 204 || !txt) return null;
        try { return JSON.parse(txt); } catch (e) { return txt; }
      });
    });
  }
  // raw(method, path, body, headers) → Promise of the fetch Response, for a caller that needs its headers (as an ETag):
  // the module's base and the CSRF header are applied, a body that is not a string is sent as JSON, and a 401 calls
  // the unauthorized callback; the status is the caller's to check.
  // raw(method, path, body, headers) → fetch Response 的 Promise，供需要应答头（如 ETag）的调用方使用：应用模块前缀与 CSRF
  // 请求头，非字符串的 body 以 JSON 发送，401 时调用未登录回调；状态码由调用方检查。
  function raw(method, path, body, headers) {
    if (this && this.base && path.indexOf('/api/') === 0) path = this.base + path.slice(4);
    var opt = { method: method, headers: {}, credentials: 'same-origin' };
    Object.keys(headers || {}).forEach(function (k) { opt.headers[k] = headers[k]; });
    addCsrf(method, opt.headers);
    if (body !== undefined) {
      opt.headers['Content-Type'] = opt.headers['Content-Type'] || 'application/json';
      opt.body = typeof body === 'string' ? body : JSON.stringify(body);
    }
    return fetch(path, opt).then(function (r) {
      if (r.status === 401 && onUnauthorized) onUnauthorized();
      return r;
    });
  }
  // rpc calls a method of the process itself: POST /api/rpc<method>, e.g. rpc('/mesh/peers') → POST /api/rpc/mesh/peers.
  // rpc 调用进程自身的方法：POST /api/rpc<method>，例如 rpc('/mesh/peers') → POST /api/rpc/mesh/peers。
  function rpc(method, req) { return (this && this.api ? this : UI).api('POST', '/api/rpc' + method, req || {}); }

  // stream reads a streaming endpoint (Server-Sent Events answering a POST) with fetch, so the request carries the
  // CSRF header and a JSON body. onData gets every "message" event (parsed JSON); the promise resolves with the "end"
  // event's data and rejects on an "error" event, an HTTP error (as api) or a connection that closes first.
  // opts: {signal (AbortController.signal: aborting cancels the stream on the server), headers}.
  // stream 用 fetch 读取流式接口（以 Server-Sent Events 应答 POST），因此请求带 CSRF 请求头与 JSON 请求体。
  // onData 收到每个 "message" 事件（已解析的 JSON）；"end" 事件的数据即 promise 的结果，"error" 事件、
  // HTTP 错误（同 api）或连接提前关闭时 reject。opts：{signal（AbortController.signal：中止即在服务端取消流），headers}。
  function stream(path, body, onData, opts) {
    if (this && this.base && path.indexOf('/api/') === 0) path = this.base + path.slice(4);
    opts = opts || {};
    var headers = { 'Content-Type': 'application/json', 'Accept': 'text/event-stream' };
    addCsrf('POST', headers);
    Object.keys(opts.headers || {}).forEach(function (k) { headers[k] = opts.headers[k]; });
    return fetch(path, { method: 'POST', headers: headers, credentials: 'same-origin', body: JSON.stringify(body || {}), signal: opts.signal }).then(function (r) {
      if (!r.ok || (r.headers.get('Content-Type') || '').indexOf('text/event-stream') < 0) {
        return r.text().then(function (txt) {
          if (r.status === 401 && onUnauthorized) onUnauthorized();
          throw httpError(r, txt);
        });
      }
      var reader = r.body.getReader(), dec = new TextDecoder(), buf = '';
      return new Promise(function (resolve, reject) {
        // handle one event block; true when the stream is over. / 处理一个事件块；流结束时返回 true。
        function handle(block) {
          var ev = 'message', data = [];
          block.split('\n').forEach(function (line) {
            if (line.indexOf('event:') === 0) ev = line.slice(6).trim();
            else if (line.indexOf('data:') === 0) data.push(line.slice(5).replace(/^ /, ''));
          });
          if (!data.length) return false; // a comment / 注释
          var v;
          try { v = JSON.parse(data.join('\n')); } catch (e) { v = data.join('\n'); }
          if (ev === 'end') { resolve(v); return true; }
          if (ev === 'error') {
            var err = new Error((v && v.message) || 'stream failed');
            err.code = v && v.code;
            reject(err); return true;
          }
          onData(v);
          return false;
        }
        function pump() {
          reader.read().then(function (x) {
            if (x.done) { reject(new Error(t('stream.broken'))); return; }
            buf += dec.decode(x.value, { stream: true });
            for (var i = buf.indexOf('\n\n'); i >= 0; i = buf.indexOf('\n\n')) {
              var block = buf.slice(0, i);
              buf = buf.slice(i + 2);
              if (handle(block)) { reader.cancel().catch(function () { /* done / 已结束 */ }); return; }
            }
            pump();
          }).catch(function (e) { reader.cancel().catch(function () { /* done / 已结束 */ }); reject(e); });
        }
        pump();
      });
    });
  }
  // rpcStream opens a stream method of the process itself: POST /api/stream<method>. / 打开进程自身的流方法：POST /api/stream<method>。
  function rpcStream(method, req, onData, signal) { return (this && this.stream ? this : UI).stream('/api/stream' + method, req || {}, onData, { signal: signal }); }

  // ---- Session / 会话 ----
  // GET /api/session → {user, csrf} (401 when not logged in); POST /api/login {username, password} → {user, csrf};
  // POST /api/logout → 204; GET /api/me → {user, identity, api_url?}. session and login remember the CSRF token.
  // GET /api/session → {user, csrf}（未登录时 401）；POST /api/login {username, password} → {user, csrf}；
  // POST /api/logout → 204；GET /api/me → {user, identity, api_url?}。session 与 login 会记住 CSRF 令牌。
  function remember(s) { setCsrf(s && s.csrf); return s; }
  function session() { return api('GET', '/api/session', undefined, true).then(remember); }
  function login(user, pass) { return api('POST', '/api/login', { username: user, password: pass }, true).then(remember); }
  function logout() { return api('POST', '/api/logout', {}).then(function (v) { setCsrf(''); return v; }, function (e) { setCsrf(''); throw e; }); }
  function me() { return (this && this.api ? this : UI).api('GET', '/api/me'); }
  // GET /api/workbench → {user, modules: [the IDs of the server modules served]}. / 已登录用户与服务端模块 ID 列表。
  function workbench() { return api('GET', '/api/workbench'); }

  // ---- Feedback / 反馈 ----
  var toastBox = null;
  function toast(msg, kind) {
    if (!toastBox) { toastBox = h('div.toasts'); document.body.appendChild(toastBox); }
    var el = h('div.toast' + (kind ? '.toast--' + kind : ''), h('div', msg));
    toastBox.appendChild(el);
    setTimeout(function () { el.style.transition = 'opacity .3s'; el.style.opacity = '0'; setTimeout(function () { el.remove(); }, 300); }, kind === 'error' ? 6000 : 3000);
  }
  function fail(e) { if (e && e.status === 401) return; toast(e && e.message ? e.message : String(e), 'error'); }

  // modal({title, body, wide, actions:[{label, kind, onclick → false keeps it open / 返回 false 保持打开}]}) → close()
  var modals = [];
  // closeModals closes every modal open, as when the page changes. / 关闭所有打开的弹窗（如切换页面时）。
  function closeModals() { modals.slice().forEach(function (c) { c(); }); }
  function modal(o) {
    var box, wrap, closed = false;
    function close() {
      if (closed) return;
      closed = true;
      modals.splice(modals.indexOf(close), 1);
      wrap.remove(); document.removeEventListener('keydown', onKey); if (o.onclose) o.onclose();
    }
    modals.push(close);
    function onKey(e) { if (e.key === 'Escape') close(); }
    var foot = (o.actions || [{ label: t('close') }]).map(function (a) {
      var b = h('button.btn' + (a.kind ? '.btn--' + a.kind : ''), {
        type: 'button', onclick: function () {
          if (!a.onclick) return close();
          var r = a.onclick(b);
          if (r && typeof r.then === 'function') {
            b.disabled = true;
            r.then(function (v) { b.disabled = false; if (v !== false) close(); }, function (e) { b.disabled = false; fail(e); });
          } else if (r !== false) close();
        }
      }, a.label);
      return b;
    });
    box = h('div.modal__box' + (o.wide ? '.modal__box--wide' : ''),
      h('div.modal__head', h('h3', o.title || ''), h('button.btn.btn--ghost.btn--icon.btn--sm', { type: 'button', onclick: close, 'aria-label': t('close') }, icon('x'))),
      h('div.modal__body', o.body), h('div.modal__foot', foot));
    wrap = h('div.modal', { onmousedown: function (e) { if (e.target === wrap) close(); } }, box);
    document.body.appendChild(wrap);
    document.addEventListener('keydown', onKey);
    var f = box.querySelector('input, textarea, select');
    if (f) setTimeout(function () { f.focus(); }, 30);
    return close;
  }
  function confirmBox(msg, okLabel, danger) {
    return new Promise(function (resolve) {
      var done = false;
      modal({
        title: t('confirm'), body: h('p', { style: { margin: 0 } }, msg),
        onclose: function () { if (!done) resolve(false); },
        actions: [{ label: t('cancel') }, { label: okLabel || t('ok'), kind: danger ? 'danger' : 'primary', onclick: function () { done = true; resolve(true); } }]
      });
    });
  }

  // ---- Formatting / 格式化 ----
  function fmtMB(mb) {
    if (mb == null || mb < 0) return '—';
    if (mb >= 1048576) return (mb / 1048576).toFixed(2) + ' TB';
    if (mb >= 1024) return (mb / 1024).toFixed(mb >= 10240 ? 0 : 1) + ' GB';
    return Math.round(mb) + ' MB';
  }
  function fmtNum(v, digits, unit) { return v == null || v < 0 ? '—' : (+v).toFixed(digits || 0) + (unit || ''); }
  function fmtDur(sec) {
    if (!sec || sec < 0) return '—';
    var d = Math.floor(sec / 86400), hh = Math.floor(sec % 86400 / 3600), m = Math.floor(sec % 3600 / 60);
    return d ? d + 'd ' + hh + 'h' : hh ? hh + 'h ' + m + 'm' : m + 'm';
  }
  function ago(unix) {
    if (!unix) return t('never');
    var s = Math.max(0, Math.floor(Date.now() / 1000 - unix));
    if (s < 60) return t('ago.s', { n: s });
    if (s < 3600) return t('ago.m', { n: Math.floor(s / 60) });
    if (s < 86400) return t('ago.h', { n: Math.floor(s / 3600) });
    return t('ago.d', { n: Math.floor(s / 86400) });
  }
  function fmtTime(unix) {
    if (!unix) return '—';
    var d = new Date(unix * 1000);
    function p(n) { return (n < 10 ? '0' : '') + n; }
    return d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate()) + ' ' + p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds());
  }

  // ---- Components / 组件 ----
  function badge(text, kind, withDot) {
    return h('span.badge' + (kind ? '.badge--' + kind : ''), withDot ? h('span.dot' + (kind === 'ok' ? '.dot--ok' : kind === 'danger' ? '.dot--danger' : kind === 'warn' ? '.dot--warn' : '')) : null, text);
  }
  function onlineBadge(on) { return badge(on ? t('online') : t('offline'), on ? 'ok' : '', true); }
  function meter(label, value, max, text) {
    var pct = max > 0 && value >= 0 ? Math.min(100, value / max * 100) : 0;
    var kind = pct >= 90 ? 'danger' : pct >= 75 ? 'warn' : '';
    return h('div.meter', h('div.meter__top', h('span', label), h('b', text != null ? text : Math.round(pct) + '%')),
      h('div.meter__bar', h('div.meter__fill' + (kind ? '.meter__fill--' + kind : ''), { style: { width: pct + '%' } })));
  }
  function stat(iconName, label, value, sub, kind) {
    return h('div.stat', h('div.stat__icon' + (kind ? '.stat__icon--' + kind : ''), icon(iconName)),
      h('div', h('div.stat__label', label), h('div.stat__value', value), sub ? h('div.stat__sub', sub) : null));
  }
  function kv(pairs) {
    var dl = h('dl.kv');
    pairs.forEach(function (p) { if (p) { dl.appendChild(h('dt', p[0])); dl.appendChild(h('dd', p[1] == null || p[1] === '' ? '—' : p[1])); } });
    return dl;
  }
  function empty(text, iconName) { return h('div.empty', icon(iconName || 'info'), h('div', text)); }
  function card(title, body, tools, flush) {
    return h('section.card', title || tools ? h('div.card__head', h('h2', title || ''), tools) : null, h('div.card__body' + (flush ? '.card__body--flush' : ''), body));
  }
  // table(columns:[{label, cls, render(row) → node|text}], rows, {onRowClick, empty}) / 表格
  function table(cols, rows, o) {
    o = o || {};
    if (!rows || !rows.length) return empty(o.empty || t('none'));
    return h('div.table-wrap', h('table.table',
      h('thead', h('tr', cols.map(function (c) { return h('th', { class: c.cls || '' }, c.label); }))),
      h('tbody', rows.map(function (r) {
        return h('tr', { class: o.onRowClick ? 'is-link' : '', onclick: o.onRowClick ? function (e) { if (!e.target.closest('button, a, input, select')) o.onRowClick(r); } : null },
          cols.map(function (c) { return h('td', { class: c.cls || '' }, c.render(r)); }));
      }))));
  }
  function tabs(items, active, onChange) {
    return h('div.tabs', items.map(function (it) {
      return h('button.tab' + (it[0] === active ? '.is-active' : ''), { type: 'button', onclick: function () { onChange(it[0]); } }, it[1]);
    }));
  }
  function field(label, input, hint) { return h('label.field', h('span.field__label', label), input, hint ? h('span.field__hint', hint) : null); }
  function btn(label, iconName, onclick, kind) {
    return h('button.btn' + (kind ? kind.split(' ').map(function (k) { return '.btn--' + k; }).join('') : ''), { type: 'button', onclick: onclick }, iconName ? icon(iconName) : null, label);
  }
  // busy(button, promise): disables the button while the promise runs. / 执行期间禁用按钮。
  function busy(b, p) {
    b.disabled = true;
    return p.then(function (v) { b.disabled = false; return v; }, function (e) { b.disabled = false; throw e; });
  }
  function copyText(text) {
    var done = function () { toast(t('copied')); };
    if (navigator.clipboard && window.isSecureContext) return navigator.clipboard.writeText(text).then(done);
    var ta = h('textarea', { style: { position: 'fixed', opacity: '0' } }, text);
    document.body.appendChild(ta); ta.select();
    try { document.execCommand('copy'); done(); } catch (e) { /* ignore / 忽略 */ }
    ta.remove();
  }

  // Preference pickers (theme + language). / 偏好选择器（主题 + 语言）。
  function prefs() {
    var th = h('select.select.select--sm', { 'aria-label': t('theme'), style: { width: 'auto' }, onchange: function () { save(THEME_KEY, th.value); applyTheme(th.value); } },
      THEMES.map(function (x) { return h('option', { value: x[0], selected: x[0] === theme }, t('theme.' + x[0])); }));
    var lg = h('select.select.select--sm', { 'aria-label': t('lang'), style: { width: 'auto' }, onchange: function () { save(LANG_KEY, lg.value); location.reload(); } },
      LANGS.map(function (x) { return h('option', { value: x[0], selected: x[0] === lang }, x[1]); }));
    return h('div.prefs', icon('sun', 'muted'), th, lg);
  }

  // every(ms, fn): setInterval until the page changes. / 定时执行，直到切换页面。
  var timers = [];
  function every(ms, fn) { var id = setInterval(fn, ms); timers.push(id); return id; }
  function stopTimers() { timers.forEach(clearInterval); timers = []; }

  // loginPage({brand, sub, onSubmit(user, pass) → Promise}) / 登录页
  function loginPage(el, o) {
    var u = h('input.input', { name: 'username', autocomplete: 'username', value: o.username || '' });
    var p = h('input.input', { name: 'password', type: 'password', autocomplete: 'current-password' });
    var err = h('div.callout.callout--danger', { hidden: true });
    var b = h('button.btn.btn--primary', { type: 'submit', style: { width: '100%', height: '40px' } }, t('login'));
    var form = h('form.form', {
      onsubmit: function (e) {
        e.preventDefault(); err.hidden = true;
        busy(b, o.onSubmit(u.value.trim(), p.value)).catch(function (e2) { err.textContent = e2.message || t('login.failed'); err.hidden = false; });
      }
    }, field(t('username'), u), field(t('password'), p), err, b);
    mount(el, h('div.login', h('div.login__prefs', prefs()), h('div.login__box',
      h('div.login__brand', h('div.brand__logo', icon('logo')), h('div', h('div.brand__name', o.brand), h('div.brand__sub.muted', o.sub || ''))),
      h('div.card', h('div.card__body', { style: { padding: '26px' } }, form)))));
  }

  // configNote(role): where the role's settings are kept (the llm.<role> member of fit's config).
  // configNote(role)：角色配置的保存位置（fit 配置中的 llm.<role> 成员）。
  function configNote(role) { return h('div.callout', icon('info'), ' ', t('config.note', { role: role || '<role>' })); }

  // scope(server, ns) → the library for a module: requests to /api/... go to /api/m/<server>/..., and t looks up ns's
  // words first. / 模块用的库：/api/... 请求发往 /api/m/<server>/...，t 先查 ns 的词条。
  function scope(server, ns) {
    var s = Object.create(UI);
    s.base = server ? '/api/m/' + encodeURIComponent(server) : '';
    // Bound, so that a module may pass them around, as PeersPanel's call. / 绑定，模块可把它们作为回调传递。
    s.raw = raw.bind(s); s.api = api.bind(s); s.rpc = rpc.bind(s); s.stream = stream.bind(s); s.rpcStream = rpcStream.bind(s); s.me = me.bind(s);
    if (ns) {
      s.t = function (key, vars) { return fill(word(ns + ':' + key) || word(key) || key, vars); };
      s.addI18n = function (d) { addI18n(d, ns); };
    }
    return s;
  }

  var UI = window.UI = {
    t: t, addI18n: addI18n, lang: function () { return lang; }, zh: function () { return lang === 'zh-CN'; },
    h: h, mount: mount, clear: clear, icon: icon, base: '',
    api: api, raw: raw, rpc: rpc, stream: stream, rpcStream: rpcStream, setUnauthorized: function (fn) { onUnauthorized = fn; },
    session: session, login: login, logout: logout, me: me, workbench: workbench, configNote: configNote, scope: scope,
    toast: toast, fail: fail, modal: modal, closeModals: closeModals, confirm: confirmBox, busy: busy, copy: copyText,
    fmtMB: fmtMB, fmtNum: fmtNum, fmtDur: fmtDur, ago: ago, fmtTime: fmtTime,
    badge: badge, onlineBadge: onlineBadge, meter: meter, stat: stat, kv: kv, empty: empty, card: card, table: table,
    tabs: tabs, field: field, btn: btn, prefs: prefs, every: every, stopTimers: stopTimers, loginPage: loginPage
  };
})();
