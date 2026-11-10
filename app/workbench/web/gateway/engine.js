// The Engine menu: one first-level entry whose submenus are the listener ports, each listing the features that port
// serves and the page that edits each one, in a fixed order: the handlers of its routes, the reverse and forward proxy
// routes, the cache and other middleware, the streaming handlers (with the port's vod routes and the channels its
// egress plays, I138) and WebRTC handlers, and its layer-4, SOCKS, TLS, ACME and automatic-HTTPS settings; its
// protocol, PROXY protocol, HTTP/3, on-demand, client-certificate and VOD badges are small
// tags rather than entries, and its last entry opens the listener form. The module has no page of its own: every entry
// is a link into a page of another module. Its data comes from config.js's shared draft, plus GET /api/status for the
// addresses the kernel bound and the listeners HTTPS by default makes; a config change (the fit:config event) or a
// refresh rebuilds the menu without losing the page shown. Each port, and each entry that lists routes, has an Ask AI
// button (I128) that asks the skill diagnose about it, its routes' names as the skill's input route.
//
// 引擎菜单：一级条目，其子菜单为各监听端口，每个端口按固定顺序列出它所提供的功能及编辑各项功能的页面：路由的处理器、反向与
// 正向代理路由、缓存与其他中间件、流媒体与 WebRTC 处理器，以及四层服务、SOCKS、TLS、ACME 与自动 HTTPS 的设置；其协议、
// PROXY 协议、HTTP/3、按需证书与客户端证书以小标记显示而非条目，最后一项打开监听表单。本模块没有自己的页面：每个条目都
// 链接到其他模块的页面。数据取自 config.js 的共享草稿，另用 GET /api/status 补充内核实际绑定的地址与自动 HTTPS 生成的监听；
// 配置变化（fit:config 事件）或刷新时重建菜单，且不丢失当前页面。每个端口及每个列出路由的条目都有“问 AI”按钮（I128），
// 以技能 diagnose 诊断它，其路由名称作为技能输入 route。依赖 ui.js、workbench.js 与 config.js。
(function () {
  'use strict';

  // [key, en, zh] / [键, 英文, 中文]
  var WORDS = [
    ['m.engine', 'Engine', '引擎'], ['tag.ports', 'Ports', '端口'],
    ['e.loading', 'Loading…', '加载中…'], ['e.none', 'No listener', '未配置监听'],
    ['e.status.failed', 'The running addresses could not be read: the addresses the config writes are shown.',
      '无法读取运行中的地址：显示的是配置中写入的地址。'],
    ['e.port.config', 'Port config', '端口配置'],
    // a port's handlers / 端口的处理器
    ['e.f.static', 'Static files', '静态文件'], ['e.f.respond', 'Respond', '直接应答'], ['e.f.fastcgi', 'FastCGI', 'FastCGI'],
    ['e.f.media_tokens', 'Media tokens', '媒体令牌'], ['e.f.media_pages', 'Media pages', '媒体页面'],
    // routes / 路由
    ['e.reverse', 'Reverse proxy routes', '反向代理路由'], ['e.forward', 'Forward proxy routes', '正向代理路由'],
    ['e.zones', 'Cache zones', '缓存区'], ['e.cached', 'Cached routes', '缓存的路由'],
    ['e.middleware', 'Other middleware', '其他中间件'],
    ['e.mw.title', 'Middleware modules: {list}', '中间件模块：{list}'],
    // streaming and WebRTC / 流媒体与 WebRTC
    ['e.streaming', 'Streaming config', '流媒体配置'], ['e.webrtc', 'WebRTC config', 'WebRTC 配置'],
    ['e.r.hls', 'HLS routes', 'HLS 路由'], ['e.r.httpflv', 'HTTP-FLV routes', 'HTTP-FLV 路由'],
    ['e.r.stream_whip', 'WHIP publish routes', 'WHIP 推流路由'], ['e.r.stream_whep', 'WHEP play routes', 'WHEP 播放路由'],
    ['e.r.whip', 'WHIP routes', 'WHIP 路由'], ['e.r.whep', 'WHEP routes', 'WHEP 路由'],
    ['e.r.webrtc_proxy', 'WebRTC proxy routes', 'WebRTC 代理路由'],
    // video on demand and channels (I138) / 点播与频道
    ['e.r.vod', 'VOD routes', '点播路由'], ['e.channels', 'Channels', '频道'], ['e.tag.vod', 'VOD', '点播'],
    // layer 4, SOCKS and TLS / 四层、SOCKS 与 TLS
    ['e.l4', 'Layer 4 services', '四层服务'], ['e.socks', 'SOCKS forward proxy', 'SOCKS 正向代理'],
    ['e.socks.title', 'The SOCKS rules are edited on the Listeners page.', 'SOCKS 规则在监听页面编辑。'],
    ['e.certs', 'TLS certificates', 'TLS 证书'], ['e.acme', 'ACME certificates', 'ACME 证书'],
    ['e.autohttps', 'Automatic HTTPS', '自动 HTTPS'], ['e.redirect', 'Redirects to HTTPS', '重定向到 HTTPS'],
    // the small tags of a port / 端口的小标记
    ['e.tag.proxy', 'PROXY', 'PROXY'], ['e.tag.h3', 'HTTP/3', 'HTTP/3'],
    ['e.tag.ondemand', 'On-demand', '按需证书'], ['e.tag.mtls', 'Client certs', '客户端证书'],
    ['e.tag.auto', 'Auto HTTPS', '自动 HTTPS'], ['e.tag.mptcp', 'MPTCP', 'MPTCP'],
    // protocols / 协议
    ['e.p.http11', 'HTTP/1.1', 'HTTP/1.1'], ['e.p.h2c', 'h2c', 'h2c'], ['e.p.h2', 'HTTP/2', 'HTTP/2'],
    ['e.p.tcp', 'TCP', 'TCP'], ['e.p.udp', 'UDP', 'UDP'], ['e.p.socks', 'SOCKS', 'SOCKS'],
    // Ask AI (I128): what is asked of the assistant / “问 AI”：向助手提的问题
    ['e.ask.port', 'Diagnose the port {name} ({address}): is it bound and serving, and do any of its routes fail or run slow?',
      '诊断端口 {name}（{address}）：它是否已绑定并在提供服务？它的路由是否有失败或变慢的？'],
    ['e.ask.routes', 'Diagnose the routes under “{what}” on the port {name}: do they answer, and does any of them fail or run slow?',
      '诊断端口 {name} 上“{what}”下的路由：它们是否正常应答？是否有失败或变慢的？']
  ];

  var ui = UI.scope('admin', 'engine');
  var h = ui.h, t = ui.t;
  // C is config.js's shared draft, the same one the editing pages use. / C 是 config.js 的共享草稿，与编辑页面相同。
  var C = FitConfig.with(ui);

  // The menu's data: the draft as last read, the status answer (null when it could not be read), and whether the
  // first read is done (before it, the menu says it is loading). / 菜单数据：上次读取的草稿、status 应答（读取失败为 null），
  // 以及首次读取是否完成（之前菜单显示加载中）。
  var S = { draft: null, status: null, statusFailed: false, loaded: false, failed: false };

  // The entries of a port keep the user's order: the handlers in this order first, then the routes, the middleware,
  // the streaming and WebRTC handlers, and the layer-4, SOCKS and TLS settings. / 端口条目的顺序即用户的顺序。
  var HANDLERS = ['static', 'respond', 'fastcgi', 'media_tokens', 'media_pages'];
  var STREAM_HANDLERS = ['hls', 'httpflv', 'stream_whip', 'stream_whep'];
  var WEBRTC_HANDLERS = ['whip', 'whep', 'webrtc_proxy'];
  var HANDLER_WORDS = { static: 'e.f.static', respond: 'e.f.respond', fastcgi: 'e.f.fastcgi',
    media_tokens: 'e.f.media_tokens', media_pages: 'e.f.media_pages' };
  var HANDLER_ICONS = { static: 'book', respond: 'zap', fastcgi: 'plug', media_tokens: 'key', media_pages: 'log' };
  var STREAM_WORDS = { hls: 'e.r.hls', httpflv: 'e.r.httpflv', stream_whip: 'e.r.stream_whip', stream_whep: 'e.r.stream_whep' };
  var WEBRTC_WORDS = { whip: 'e.r.whip', whep: 'e.r.whep', webrtc_proxy: 'e.r.webrtc_proxy' };
  var STREAM_ICONS = { hls: 'play', httpflv: 'play', stream_whip: 'radio', stream_whep: 'radio' };
  var PROTO_WORDS = { 'http/1.1': 'e.p.http11', h2c: 'e.p.h2c', h2: 'e.p.h2', tcp: 'e.p.tcp', udp: 'e.p.udp', socks: 'e.p.socks' };

  // ---- the config, and the addresses that run / 配置与运行中的地址 ----
  function autoHTTPS(draft) { return !(draft && draft.auto_https && draft.auto_https.disable); }
  // protocolsOf is what a listener serves, with the config's defaults. / 监听所服务的协议，含配置的默认值。
  function protocolsOf(l) {
    if (l && l.protocols && l.protocols.length) return l.protocols.slice();
    return l && l.tls ? ['http/1.1', 'h2'] : ['http/1.1'];
  }
  function hasProtocol(l, p) { return protocolsOf(l).indexOf(p) >= 0; }
  // servesHTTP tells whether a port has routes, which only an HTTP listener does. / 只有 HTTP 监听才有路由。
  function servesHTTP(protocols) {
    return !protocols || !protocols.length ||
      protocols.some(function (p) { return p === 'http/1.1' || p === 'h2' || p === 'h2c' || p === 'h3'; });
  }
  function servesPort(r, name) {
    return !r || !r.listeners || !r.listeners.length || r.listeners.indexOf(name) >= 0;
  }
  // boundMap is the addresses /api/status reports, by listener name. / /api/status 报告的地址，按监听名。
  function boundMap() {
    var m = {};
    ((S.status && S.status.listeners) || []).forEach(function (a) { if (a && a.name && !m[a.name]) m[a.name] = a; });
    return m;
  }
  function readStatus() {
    return ui.api('GET', '/api/status').then(function (st) {
      S.status = st && typeof st === 'object' ? st : {};
      S.statusFailed = false;
    }, function () {
      S.status = null;
      S.statusFailed = true;
    });
  }

  // load reads the draft and the running addresses once, then draws the menu; boot calls it and a config change
  // listens to fit:config. / load 读取草稿与运行地址并绘制菜单；boot 调用它，配置变化监听 fit:config。
  function load() {
    return C.ensure().then(function () {
      S.draft = C.draft || {};
      S.loaded = true;
      S.failed = false;
      return readStatus();
    }).then(function () {
      menu();
    }, function (e) {
      // Keep the menu usable when the config cannot be read: one link to the listener form rather than ports that are
      // not known. / 配置无法读取时菜单仍可用：只留一个指向监听表单的链接，而不显示未知的端口。
      S.loaded = true;
      S.failed = true;
      menu();
      throw e;
    });
  }
  // changed refreshes the cached draft from the shared one (config.js dispatches fit:config after every read) and
  // the running addresses best-effort. / changed 从共享草稿刷新缓存，并尽力重读运行地址。
  function changed() {
    if (!S.loaded) return;
    S.draft = C.draft || {};
    S.failed = false;
    menu();
    readStatus().then(menu);
  }
  function menu() { if (window.WB && WB.refreshMenu) WB.refreshMenu(); }
  window.addEventListener('fit:config', changed);

  // ---- the ports / 端口 ----
  // ports is the Engine menu: the configured listeners in the order written; or, when none are written and HTTPS by
  // default is on, the https and http ports it makes; or one link to the listener form when there is no port at all.
  // ports 即引擎菜单：按书写顺序的已配置监听；未写监听且自动 HTTPS 开启时为它生成的 https 与 http 端口；
  // 一个端口也没有时为指向监听表单的一个链接。
  function ports() {
    if (!S.loaded) return [{ label: t('e.loading') }];
    if (S.failed) return noPort();
    var draft = S.draft || {}, list = draft.listeners || [], out = [], bound = boundMap();
    if (list.length) {
      list.forEach(function (l, i) {
        l = l || {};
        var name = l.name || '#' + (i + 1), protocols = protocolsOf(l);
        out.push(navPort(draft, {
          vod: servesHTTP(protocols) && (draft.routes || []).some(function (r) { return servesPort(r, name) && r && r.handler && r.handler.module === 'vod'; }),
          name: name, address: l.address || '', protocols: protocols, http: servesHTTP(protocols),
          tls: l.tls, socks: l.socks || protocols.indexOf('socks') >= 0, proxyProtocol: l.proxy_protocol,
          mptcp: l.mptcp, generated: false, bound: bound[name]
        }));
      });
      return out;
    }
    if (!autoHTTPS(draft)) return noPort();
    var a = draft.auto_https || {}, https = bound.https, http = bound.http;
    var httpsProtocols = https && https.protocols && https.protocols.length ? https.protocols : ['http/1.1', 'h2'];
    out.push(navPort(draft, {
      name: 'https', address: https && https.address ? https.address : ':' + (a.https_port || 443),
      protocols: httpsProtocols, http: true, generated: true, auto: true, bound: https
    }));
    if (!a.disable_redirects) {
      var httpProtocols = http && http.protocols && http.protocols.length ? http.protocols : ['http/1.1'];
      out.push(navPort(draft, {
        name: 'http', address: http && http.address ? http.address : ':' + (a.http_port || 80),
        protocols: httpProtocols, http: true, generated: true, auto: true, redirect: true, bound: http
      }));
    }
    return out.length ? out : noPort();
  }
  function noPort() { return [{ label: t('e.none'), href: '#/gw.listeners/listeners' }]; }

  // navPort is a port's submenu: its name and address, a line of its protocols and tags, and its feature entries.
  // navPort 即端口的子菜单：名称与地址、协议与小标记一行，以及它的各功能条目。
  // shortAddress keeps a port's address readable in the narrow menu: ":8080" for "127.0.0.1:8080", the
  // configured address when it has no port form (a Unix socket and the like). The whole address is in the title.
  // shortAddress 让窄菜单中的地址可读：127.0.0.1:8080 记为 :8080；不是 host:port 形式的地址（如 Unix socket）原样返回，完整地址在 title 中。
  function shortAddress(address) {
    var m = /^\[?([^\]]*)\]?:(\d+)$/.exec(address || '');
    return m && m[2] ? ':' + m[2] : (address || '');
  }
  function navPort(draft, port) {
    var full = port.name + (port.address ? ' ' + port.address : '');
    var label = port.name + (port.address ? ' ' + shortAddress(port.address) : '');
    // The whole address, the address the kernel bound and a failed status are in the title, not on the menu line.
    // 完整地址、内核实际绑定的地址与读取失败的 status 放在 title 中，不占菜单行。
    var title = [full, '[' + portTag(port) + ']'];
    if (port.bound && port.bound.address && port.bound.address !== port.address) title.push(port.bound.address);
    if (S.statusFailed) title.push(t('e.status.failed'));
    var kids = portKids(draft, port);
    return { id: port.name, label: label, tag: portTag(port), icon: 'listen', kids: kids,
      title: title.join(' — '), ask: portAsk(draft, port, kids) };
  }

  // ---- Ask AI (I128) / 问 AI ----
  // routeFacts is what the assistant is told of a route: its name, match, handler and upstream, never its params' other
  // values. / routeFacts 是告诉助手的路由信息：名称、匹配、处理器与上游，不含其参数的其他值。
  function routeFacts(r) {
    var p = (r && r.handler && r.handler.params) || {};
    return { name: r.name || undefined, match: r.match || undefined, handler: r.handler && r.handler.module,
      upstream: typeof p.upstream === 'string' ? p.upstream : undefined, listeners: r.listeners || undefined,
      middleware: (r.middleware || []).map(function (mw) { return mw && mw.module; }) };
  }
  function routeNames(routes) {
    return routes.map(function (r) { return r && r.name; }).filter(Boolean).join(', ');
  }
  function portFacts(port) {
    return { name: port.name, address: port.address, bound: port.bound && port.bound.address, protocols: port.protocols,
      tls: !!(port.tls && Object.keys(port.tls).length), generated: port.generated || undefined, tags: portTag(port) };
  }
  // portAsk asks diagnose about a port: its facts, its entries, and its routes' names as the skill's input route.
  // portAsk 让 diagnose 诊断一个端口：端口信息、其条目，以及作为技能输入 route 的各路由名称。
  function portAsk(draft, port, kids) {
    return function () {
      var routes = routesOf(draft, port);
      return { skill: 'diagnose',
        message: t('e.ask.port', { name: port.name, address: (port.bound && port.bound.address) || port.address || '—' }),
        inputs: { route: routeNames(routes) },
        selection: { port: portFacts(port), entries: kids.map(function (k) { return k.count != null ? k.label + ' (' + k.count + ')' : k.label; }),
          routes: routes.map(routeFacts) } };
    };
  }
  // routesAsk asks diagnose about an entry of a port's routes. / routesAsk 让 diagnose 诊断端口的某组路由。
  function routesAsk(port, what, routes) {
    return function () {
      return { skill: 'diagnose', message: t('e.ask.routes', { what: what, name: port.name }),
        inputs: { route: routeNames(routes) },
        selection: { port: portFacts(port), entry: what, routes: routes.map(routeFacts) } };
    };
  }

  // portTag is the small line under a port's name: its protocols and the features with no page of their own
  // (PROXY protocol, HTTP/3, the TLS extras, HTTPS by default). / portTag 是端口名称下的小字行：协议，以及没有独立页面的功能。
  function portTag(port) {
    var parts = [];
    (port.protocols || []).forEach(function (p) {
      if (p === 'h3') return; // shown as the HTTP/3 tag below / 由下方的 HTTP/3 标记显示
      parts.push(PROTO_WORDS[p] ? t(PROTO_WORDS[p]) : p);
    });
    if (port.auto) parts.push(t('e.tag.auto'));
    if (port.proxyProtocol) parts.push(t('e.tag.proxy'));
    if ((port.protocols || []).indexOf('h3') >= 0) parts.push(t('e.tag.h3'));
    if (port.tls && port.tls.on_demand) parts.push(t('e.tag.ondemand'));
    if (port.tls && port.tls.client_auth) parts.push(t('e.tag.mtls'));
    if (port.mptcp) parts.push(t('e.tag.mptcp'));
    if (port.vod) parts.push(t('e.tag.vod'));
    return parts.join(' · ');
  }

  // routesOf is the routes serving a port: every route when it names no listeners, and only an HTTP port has them.
  // routesOf 返回服务该端口的路由：路由未写监听时为全部路由；只有 HTTP 端口有路由。
  function routesOf(draft, port) {
    if (!port.http) return [];
    return (draft.routes || []).filter(function (r) { return servesPort(r, port.name); });
  }
  // byModule groups routes by their handler module. / 按处理器模块分组路由。
  function byModule(routes) {
    var out = {};
    routes.forEach(function (r) {
      var m = r && r.handler && r.handler.module;
      if (m) (out[m] = out[m] || []).push(r);
    });
    return out;
  }
  function routeHref(module) { return '#/gw.reverse/routes?handler=' + encodeURIComponent(module); }
  function otherHandlers(by) {
    return Object.keys(by).filter(function (m) {
      return HANDLERS.indexOf(m) < 0 && m !== 'proxy' && m !== 'forward_proxy' && m !== 'vod' &&
        STREAM_HANDLERS.indexOf(m) < 0 && WEBRTC_HANDLERS.indexOf(m) < 0;
    }).sort();
  }

  // portKids lists the entries of a port in the user's order; only the features the port really has are shown, and
  // the listener form comes last. / portKids 按用户的顺序列出端口的条目；只显示端口真正具有的功能，监听表单在最后。
  function portKids(draft, port) {
    var out = [], routes = routesOf(draft, port), by = byModule(routes);
    // rs, the routes an entry lists, give it an Ask AI button (I128). / rs 为条目所列的路由，使其带“问 AI”按钮（I128）。
    function add(label, href, icon, count, title, rs) {
      out.push({ label: label, href: href, icon: icon, count: count, title: title, ask: rs && rs.length ? routesAsk(port, label, rs) : undefined });
    }
    // 1. The handlers serving the port, in the fixed order, then any other handler by its module id.
    // 1. 服务该端口的处理器（按固定顺序），其后为其他以模块 ID 为名的处理器。
    HANDLERS.forEach(function (m) {
      if (by[m]) add(t(HANDLER_WORDS[m]), routeHref(m), HANDLER_ICONS[m], by[m].length, null, by[m]);
    });
    otherHandlers(by).forEach(function (m) { add(m, routeHref(m), 'code', by[m].length, null, by[m]); });
    // 2. The port's protocol and feature tags are in portTag, not entries. / 2. 协议与功能小标记在 portTag 中，不是条目。
    // 3. The reverse and forward proxy routes. / 3. 反向与正向代理路由。
    if (by.proxy) add(t('e.reverse'), routeHref('proxy'), 'route', by.proxy.length, null, by.proxy);
    if (by.forward_proxy) add(t('e.forward'), '#/gw.forward/routes', 'forward', by.forward_proxy.length, null, by.forward_proxy);
    // 4. Cache middleware (its zones and the routes that use it), then the other middleware grouped.
    // 4. 缓存中间件（其缓存区与使用它的路由），其余中间件归为一组。
    var cache = routes.filter(function (r) {
      return (r.middleware || []).some(function (mw) { return mw && mw.module === 'cache'; });
    });
    if (cache.length) {
      add(t('e.zones'), '#/gw.cache/zones', 'disk');
      add(t('e.cached'), '#/gw.reverse/routes', 'route', cache.length, null, cache);
    }
    var mwRoutes = [], mwMods = {};
    routes.forEach(function (r) {
      var got = false;
      (r.middleware || []).forEach(function (mw) {
        if (mw && mw.module && mw.module !== 'cache') { got = true; mwMods[mw.module] = 1; }
      });
      if (got) mwRoutes.push(r);
    });
    if (mwRoutes.length) {
      add(t('e.middleware'), '#/gw.reverse/routes', 'layers', mwRoutes.length,
        t('e.mw.title', { list: Object.keys(mwMods).sort().join(', ') }), mwRoutes);
    }
    // 5. Streaming: its page, then each handler's filtered routes, then its vod routes and the channels its egress
    // plays (I138). / 5. 流媒体：其页面与各处理器的过滤路由，然后是点播路由与其输出所播放的频道（I138）。
    var stream = STREAM_HANDLERS.filter(function (m) { return by[m]; });
    var channels = (draft.streaming && draft.streaming.channels) || [];
    if (stream.length || by.vod) {
      add(t('e.streaming'), '#/gw.streaming/streaming', 'play');
      stream.forEach(function (m) { add(t(STREAM_WORDS[m]), routeHref(m), STREAM_ICONS[m], by[m].length, null, by[m]); });
      if (by.vod) add(t('e.r.vod'), '#/gw.medialib/vod', 'disk', by.vod.length, null, by.vod);
      if (channels.length && (by.hls || by.httpflv || by.stream_whep)) {
        add(t('e.channels'), '#/gw.medialib/channels', 'radio', channels.length,
          channels.map(function (c) { return c && c.name; }).filter(Boolean).join(', '));
      }
    }
    // 6. WebRTC: its page, then each handler's filtered routes. / 6. WebRTC：其页面与各处理器的过滤路由。
    var webrtc = WEBRTC_HANDLERS.filter(function (m) { return by[m]; });
    if (webrtc.length) {
      add(t('e.webrtc'), '#/gw.webrtc/webrtc', 'radio');
      webrtc.forEach(function (m) { add(t(WEBRTC_WORDS[m]), routeHref(m), 'radio', by[m].length, null, by[m]); });
    }
    // 7. Layer 4, SOCKS, TLS, ACME and automatic HTTPS. / 7. 四层、SOCKS、TLS、ACME 与自动 HTTPS。
    var services = (draft.services || []).filter(function (s) {
      return s && (s.listeners || []).indexOf(port.name) >= 0;
    });
    if (services.length) add(t('e.l4'), '#/gw.l4/services', 'layers', services.length);
    if (port.socks) add(t('e.socks'), '#/gw.listeners/listeners', 'proxy', null, t('e.socks.title'));
    var tls = port.tls;
    if (tls && Object.keys(tls).length) {
      add(t('e.certs'), '#/gw.tls/certs', 'shield', (tls.certificates || []).length || 1);
      if ((tls.acme || []).length) add(t('e.acme'), '#/gw.tls/acme', 'key', tls.acme.length);
    } else if (autoHTTPS(draft) && servesHTTP(port.protocols) && (port.generated ? port.name !== 'http' : !!tls)) {
      // HTTPS by default fills in a written listener's tls only when it is there and empty, as tls {}; one
      // without tls stays plain HTTP. / 自动 HTTPS 只补全写了但为空的 tls（如 tls {}）；没写 tls 的监听仍是明文 HTTP。
      add(t('e.autohttps'), '#/gw.tls/auto_https', 'lock');
    }
    if (port.generated && port.name === 'http') add(t('e.redirect'), '#/gw.tls/auto_https', 'forward');
    // Our addition: the listener form one click away, last. / 我们的补充：监听表单一键可达，置于最后。
    add(t('e.port.config'), '#/gw.listeners/listeners', 'listen');
    return out;
  }

  WB.module({
    id: 'engine', server: 'admin', label: 'm.engine', icon: 'zap', tag: 'tag.ports', words: WORDS,
    nav: ports, boot: load
  });
})();
