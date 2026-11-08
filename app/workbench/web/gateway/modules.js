// The gateway's and the system's modules that edit the config (the admin server module): listeners, the reverse
// proxy (routes and upstreams), the forward proxy, layer 4 services, TLS, the cache zones, then the rest of the
// config's members, the config as JSON and its versions. They share config.js's draft and its save bar.
// Needs ui.js, workbench.js and config.js. A route of the Routes page, and an upstream with backends down on the
// Upstreams page, have an Ask AI button (I128) that asks the skill diagnose about it.
//
// 编辑配置的网关与系统模块（admin 服务端模块）：监听、反向代理（路由与上游）、正向代理、四层代理、TLS、缓存，以及
// 配置的其余成员、JSON 形式的配置与配置版本。它们共用 config.js 的草稿与保存栏。依赖 ui.js、workbench.js 与 config.js。
// “路由”页的每条路由，以及“上游”页中有后端不可用的上游，都有“问 AI”按钮（I128），以技能 diagnose 诊断它。
(function () {
  'use strict';

  // [key, en, zh] / [键, 英文, 中文]
  var WORDS = [
    ['m.listeners', 'Listeners', '监听'], ['m.reverse', 'Reverse proxy', '反向代理'], ['m.forward', 'Forward proxy', '正向代理'],
    ['m.l4', 'L4 proxy', '四层代理'], ['m.tls', 'TLS', 'TLS 证书'], ['m.cache', 'Cache', '缓存'],
    ['m.config', 'Global', '全局配置'], ['m.json', 'JSON', 'JSON'], ['m.versions', 'Versions', '版本'],
    ['s.listeners', 'Listeners', '监听'], ['s.routes', 'Routes', '路由'], ['s.upstreams', 'Upstreams', '上游'],
    ['s.services', 'Layer 4 services', '四层服务'], ['s.auto_https', 'Automatic HTTPS', '自动 HTTPS'], ['s.acme', 'ACME', 'ACME'],
    ['s.certs', 'Certificates', '证书'], ['s.zones', 'Cache zones', '缓存区'],
    ['s.log', 'Log', '日志'], ['s.access_log', 'Access log', '访问日志'], ['s.telemetry', 'Telemetry', '遥测'],
    ['s.metrics', 'Metrics', '指标'], ['s.pprof', 'Profiling', '性能剖析'], ['s.limits', 'Limits', '限制'], ['s.engine', 'Engine', '引擎'],
    ['s.providers', 'Providers', '配置来源'], ['s.tunnel', 'Tunnel roles', '隧道角色'], ['s.llm', 'LLM roles', 'LLM 角色'],
    ['s.webrtc', 'WebRTC', 'WebRTC'], ['s.other', 'Other', '其他'], ['s.json', 'Config as JSON', 'JSON 配置'],
    ['s.versions', 'Config versions', '配置版本'],
    // routes / 路由
    ['r.all', 'All', '全部'], ['r.add', 'Add route', '添加路由'], ['r.none', 'No routes here.', '这里还没有路由。'],
    ['r.reverse.note', 'Routes match requests and hand them to a handler: proxy passes them to an upstream, static serves files, respond answers itself. Forward proxy routes are on their own page.',
      '路由匹配请求并交给处理器：proxy 转发到上游，static 提供静态文件，respond 直接应答。正向代理路由在其单独的页面中。'],
    ['r.raw.note', 'This is the routes member of the config: every route, the forward proxy routes of the forward proxy page included, not only the ones this page shows.',
      '这是配置的 routes 成员：全部路由，也包括“正向代理”页面编辑的正向代理路由，而不只是本页显示的那些。'],
    ['fw.note', 'Routes whose handler is forward_proxy: HTTP CONNECT (and plain HTTP) proxying for the clients listed. Listeners can also serve SOCKS5 with the socks protocol.',
      '处理器为 forward_proxy 的路由：为所列客户端提供 HTTP CONNECT（及普通 HTTP）代理。监听也可以通过 socks 协议提供 SOCKS5 代理。'],
    ['fw.add', 'Add forward proxy route', '添加正向代理路由'], ['fw.none', 'No forward proxy route yet.', '还没有正向代理路由。'],
    ['fw.socks', 'Listeners serving SOCKS: {list}', '提供 SOCKS 的监听：{list}'],
    ['l.add', 'Add listener', '添加监听'], ['l.engine.note', 'Each port\'s features are grouped under the Engine menu in the sidebar; this page edits the listeners themselves.',
      '各端口的功能都在侧边栏的“引擎”菜单中分组列出；本页编辑监听本身。'], ['u.add', 'Add upstream', '添加上游'], ['sv.add', 'Add service', '添加服务'],
    ['u.up', '{up} / {all} up', '{up} / {all} 可用'], ['u.addrs', '{n} addresses', '{n} 个地址'],
    // Ask AI (I128): what is asked of the assistant / “问 AI”：向助手提的问题
    ['r.ask', 'Diagnose the route {name}: does it answer, and does it fail or run slow?', '诊断路由 {name}：它是否正常应答？是否失败或变慢？'],
    ['u.ask', 'The upstream {name} has {down} of {all} backends down. Why, and what should I fix?',
      '上游 {name} 的 {all} 个后端中有 {down} 个不可用。原因是什么？应该修复什么？'],
    // TLS / 证书
    ['c.note', 'The certificates the listeners serve now, files and ACME alike. Set a listener\'s tls on the Listeners page; ACME and automatic HTTPS have their own pages.',
      '各监听当前提供的证书（文件证书与 ACME 证书）。监听的 tls 在“监听”页设置；ACME 与自动 HTTPS 各有单独页面。'],
    // cache / 缓存
    ['z.zone', 'Zone', '缓存区'], ['z.keys', 'Keys', '键'], ['z.bytes', 'Memory', '内存'], ['z.disk', 'Disk entries', '磁盘条目'],
    ['z.diskbytes', 'Disk', '磁盘'], ['z.path', 'Disk path', '磁盘路径'], ['z.none', 'No cache zone is in use: add the cache middleware to a route.', '没有使用中的缓存区：在路由中添加 cache 中间件。'],
    ['z.purge', 'Purge', '清除'], ['z.purgeall', 'Purge all', '全部清除'], ['z.anyzone', 'Every zone', '所有缓存区'],
    ['z.urls', 'URLs, one per line (absolute, as requests name them)', 'URL，每行一个（绝对 URL，与请求一致）'],
    ['z.tags', 'Tags, one per line (Cache-Tag / Surrogate-Key)', '标签，每行一个（Cache-Tag / Surrogate-Key）'],
    ['z.all', 'Everything in the zone', '清除缓存区中的全部内容'], ['z.purged', '{n} keys purged.', '已清除 {n} 个键。'],
    ['z.need', 'Name URLs, tags, or everything.', '请填写 URL、标签，或选择全部清除。'],
    ['z.all.q', 'Purge everything zone {zone} keeps?', '清除缓存区 {zone} 中的全部内容？'], ['z.every', 'every zone', '所有缓存区'],
    // system / 系统
    ['role.note', 'A role set here starts when the config is applied, and stops when it is left out. Its own pages show in this menu once it runs.',
      '在这里设置的角色在配置应用后启动，移除后停止。角色运行后，其页面会出现在菜单中。'],
    ['pprof.note', 'While pprof is set, the Admin API serves Go\'s runtime profiles at /debug/pprof/ on its own address (admin.address, 127.0.0.1:2019 by default), behind its token; the console does not serve them. Save and apply turns them on or off at once, with no restart; leave pprof out when done. Then, for example: go tool pprof http://127.0.0.1:2019/debug/pprof/profile?seconds=30',
      '设置 pprof 后，Admin API 在其自己的地址（admin.address，默认 127.0.0.1:2019）的 /debug/pprof/ 上提供 Go 运行时的性能剖析数据，需其令牌访问；控制台不提供。“保存并应用”后立即开启或关闭，无需重启；用完后移除 pprof。例如：go tool pprof http://127.0.0.1:2019/debug/pprof/profile?seconds=30'],
    ['other.none', 'Every member of the config has its own page.', '配置的每个成员都有自己的页面。'],
    ['j.draft', 'Draft', '草稿'], ['j.effective', 'Effective', '生效配置'],
    ['j.note', 'The whole config as JSON, admin aside when fit keeps its config in admin.store. Edits here are the same draft the forms edit; Save and apply checks it and applies it as one.',
      '整个配置的 JSON（fit 使用 admin.store 保存配置时不含 admin）。这里的修改与各表单编辑的是同一份草稿；“保存并应用”会校验并整体应用。'],
    ['j.bad', 'Does not parse: {e}', '无法解析：{e}'], ['j.import', 'Open file', '打开文件'], ['j.export', 'Download', '下载'],
    ['j.format', 'Format', '格式化'], ['j.eff.note', 'The config as it runs: the base config with what the providers provide, read only.', '实际运行的配置：基础配置加上各配置来源提供的内容，只读。'],
    ['j.precedence', 'Precedence', '优先级'], ['j.sources', 'Sources', '来源'], ['j.path', 'Path', '路径'], ['j.source', 'Source', '来源'],
    ['v.version', 'Version', '版本'], ['v.applied', 'Applied', '应用时间'], ['v.source', 'Source', '来源'], ['v.size', 'Size', '大小'],
    ['v.etag', 'ETag', 'ETag'], ['v.running', 'running', '运行中'], ['v.view', 'View', '查看'], ['v.rollback', 'Roll back', '回滚'],
    ['v.title', 'Version {n}', '版本 {n}'], ['v.rollback.q', 'Apply version {n} again, as a new version?', '将版本 {n} 重新应用为一个新版本？'],
    ['v.dirty.q', 'Rolling back discards the changes not saved. Go on?', '回滚会放弃尚未保存的修改。继续？'],
    ['v.done', 'Version {n} applied again as version {v}.', '版本 {n} 已重新应用为版本 {v}。'],
    ['v.note', 'The configs applied, newest first, as many as fit keeps. Rolling back applies an old one again as a new version.',
      '已应用的配置，最新的在前，保留数量由 fit 决定。回滚即把旧版本重新应用为新版本。']
  ];

  var ROLES = { tunnel: 1, llm: 1 };
  // The config's members the Global menu edits, and those edited on a page of their own: the feature pages in
  // features.js, and the protocol pages above. A member in ELSEWHERE is not listed by the "other" page either.
  // Global 菜单编辑的成员，以及在各自独立页面编辑的成员（features.js 中的功能页面与上面的协议页面）。“其他”页面也不再列出它们。
  var SYS = ['log', 'access_log', 'telemetry', 'metrics', 'pprof', 'limits', 'engine', 'providers'];
  var ELSEWHERE = { listeners: 1, routes: 1, upstreams: 1, services: 1, auto_https: 1, acme: 1, admin: 1,
    tunnel: 1, llm: 1, webrtc: 1, streaming: 1, media_auth: 1, sync: 1 };
  var SYS_ICONS = { log: 'log', access_log: 'book', telemetry: 'activity', metrics: 'activity', pprof: 'zap', limits: 'shield', engine: 'cpu',
    providers: 'plug', other: 'cube' };

  // restMembers is the config's members that no other page edits: what the "other" page owns. / 其余无页面编辑的成员。
  function restMembers(C) {
    return C.sections().filter(function (s) { return !ELSEWHERE[s] && SYS.indexOf(s) < 0; });
  }

  function isForward(r) { return !!(r && r.handler && r.handler.module === 'forward_proxy'); }
  function uniqueName(list, base) {
    var names = {};
    (list || []).forEach(function (x) { if (x && x.name) names[x.name] = 1; });
    for (var i = 1; ; i++) if (!names[base + '-' + i]) return base + '-' + i;
  }

  // Where each problem is edited. / 每个问题所在的编辑页面。
  var placed = false;
  function places(C) {
    if (placed) return;
    placed = true;
    C.place(function (path, d) {
      var m = /^([A-Za-z_]+)(?:\[(\d+)\])?/.exec(path);
      if (!m) return null;
      var sec = m[1], i = m[2];
      switch (sec) {
        case 'listeners': return '#/gw.listeners/listeners';
        case 'routes': return i != null && isForward((d.routes || [])[+i]) ? '#/gw.forward/routes' : '#/gw.reverse/routes';
        case 'upstreams': return '#/gw.reverse/upstreams';
        case 'services': return '#/gw.l4/services';
        case 'auto_https': case 'acme': return '#/gw.tls/' + sec;
        case 'admin': return null;
        // The feature members' pages are registered by features.js (sync's by sync/sync.js); nothing here claims them, or the "other" page,
        // which no longer edits them. / 功能成员的页面由 features.js 注册；此处不认领它们，也不再指向“其他”页面。
        case 'tunnel': case 'llm': case 'webrtc': case 'streaming': case 'media_auth': case 'sync': return null;
      }
      return SYS.indexOf(sec) >= 0 ? '#/sys.config/' + sec : '#/sys.config/other';
    });
  }

  // edit is the route of a page that edits the config; opts.members are the config members it edits (for the
  // UI/.conf/.yaml/.json tabs), opts.rawNote a note shown above the raw text. / 编辑配置的页面路由；opts 见 config.js 的 page。
  function edit(UI, sh, nav, title, render, opts) {
    var C = FitConfig.with(UI);
    places(C);
    var el = sh.page(nav, title);
    return C.page(el, sh, render, opts);
  }
  function members(names) { return { members: names }; }

  function chips(UI, list) { return list.filter(Boolean).map(function (x) { return typeof x === 'string' ? UI.h('span.muted.small', x) : x; }); }

  // askOf is an Ask AI button (ai/ask.js), or null without the assistant. / “问 AI”按钮，没有助手时为 null。
  function askOf(spec, o) { return window.FitAI && FitAI.askButton ? FitAI.askButton(spec, o) : null; }
  // routeAsk asks diagnose about a route: its name as the input route, its match, handler and upstream as the
  // selection. / routeAsk 让 diagnose 诊断一条路由：名称为输入 route，匹配、处理器与上游为选中项。
  function routeAsk(UI, r) {
    return function () {
      var m = (r && r.match) || {}, p = (r && r.handler && r.handler.params) || {};
      var name = (r && r.name) || [].concat(m.host || [], m.path || []).join(' ') || '?';
      return { skill: 'diagnose', message: UI.t('r.ask', { name: name }), inputs: { route: (r && r.name) || '' },
        selection: { route: { name: r && r.name, match: r && r.match, handler: r && r.handler && r.handler.module,
          upstream: typeof p.upstream === 'string' ? p.upstream : undefined, listeners: r && r.listeners,
          middleware: ((r && r.middleware) || []).map(function (mw) { return mw && mw.module; }) } } };
    };
  }
  // upstreamAsk asks diagnose about an upstream with backends down: the routes that proxy to it as the input route.
  // upstreamAsk 让 diagnose 诊断有后端不可用的上游：代理到它的路由作为输入 route。
  function upstreamAsk(UI, u, hl, routes) {
    return function () {
      var names = (routes || []).filter(function (r) {
        var p = r && r.handler && r.handler.params;
        return p && p.upstream === u.name;
      }).map(function (r) { return r.name; }).filter(Boolean);
      return { skill: 'diagnose', message: UI.t('u.ask', { name: u.name, down: hl.all - hl.up, all: hl.all }),
        inputs: { route: names.join(', ') },
        selection: { upstream: { name: u.name, addresses: u.addresses, load_balancing: u.load_balancing, up: hl.up, all: hl.all,
          backends: hl.backends }, routes: names } };
    };
  }

  function routeChips(UI, r) {
    var h = UI.h, m = (r && r.match) || {}, out = [];
    if (r && r.handler && r.handler.module) out.push(UI.badge(r.handler.module, 'primary'));
    (m.host || []).slice(0, 2).forEach(function (x) { out.push(h('code.small', x)); });
    (m.path || []).slice(0, 2).forEach(function (x) { out.push(h('code.small', x)); });
    if (m.method && m.method.length) out.push(h('span.muted.small', m.method.join(',')));
    var p = r && r.handler && r.handler.params;
    if (p && p.upstream) out.push(h('span.muted.small', '→ ' + p.upstream));
    if (r && r.listeners && r.listeners.length) out.push(h('span.muted.small', '@' + r.listeners.join(',')));
    out.push(askOf(routeAsk(UI, r), { label: false }));
    return chips(UI, out);
  }

  // ---- Listeners / 监听 ----
  WB.module({
    id: 'gw.listeners', server: 'admin', group: 'gateway', order: 2, icon: 'listen', label: 'm.listeners', words: WORDS,
    nav: [{ id: 'listeners', icon: 'listen', label: 's.listeners' }],
    setup: function (UI, sh) {
      var h = UI.h, t = UI.t;
      return {
        routes: {
          'listeners': function () {
            edit(UI, sh, 'listeners', t('s.listeners'), function (body) {
              var C = FitConfig.with(UI);
              // Each port's features are in the Engine menu; the listener form is what this page edits.
              // 各端口的功能在引擎菜单中；本页编辑监听表单。
              body.appendChild(h('div.callout', UI.icon('info'), ' ', t('l.engine.note')));
              body.appendChild(C.section('listeners', {
                addLabel: t('l.add'),
                make: function (arr) { return { name: uniqueName(arr, 'web'), address: ':8080', protocols: ['http/1.1'] }; },
                chips: function (l) {
                  return chips(UI, [l && l.address ? UI.h('code.small', l.address) : null].concat(((l && l.protocols) || []).map(function (p) { return UI.badge(p); }),
                    [l && l.tls ? UI.badge('tls', 'ok') : null, l && l.socks ? UI.badge('socks', 'info') : null]));
                }
              }));
            }, members(['listeners']));
          }
        }
      };
    }
  });

  // ---- Reverse proxy / 反向代理 ----
  WB.module({
    id: 'gw.reverse', server: 'admin', group: 'gateway', order: 3, icon: 'route', label: 'm.reverse', words: WORDS,
    nav: [{ id: 'routes', icon: 'route', label: 's.routes' }, { id: 'upstreams', icon: 'server', label: 's.upstreams' }],
    setup: function (UI, sh) {
      var h = UI.h, t = UI.t;
      var sel = 'all';
      // deep is the ?handler=<module> the page was entered with, so that an Engine menu link lands on that handler's
      // routes: it is applied once per visit, and a tab clicked afterwards keeps its own choice.
      // deep 是进入页面时的 ?handler=<module>，使引擎菜单的链接落到该处理器的路由上：每次访问只采用一次，之后点击的标签页保留
      // 自己的选择。
      var deep = null;
      function routesPage() {
        deep = null;
        edit(UI, sh, 'routes', t('s.routes'), function (body) {
          var C = FitConfig.with(UI);
          var rs = (C.draft.routes || []).filter(function (r) { return !isForward(r); });
          var mods = {};
          rs.forEach(function (r) { if (r && r.handler && r.handler.module) mods[r.handler.module] = (mods[r.handler.module] || 0) + 1; });
          // A problem's link wins over the deep link, so that its route is shown even when filtered. / 问题链接优先于深链，使其路由显示。
          var want = null;
          var q = /[?&]handler=([^&]+)/.exec(location.hash);
          if (q) { try { want = decodeURIComponent(q[1]); } catch (e) { want = q[1]; } }
          if (C.focus && C.focus.indexOf('routes') === 0) { sel = 'all'; deep = want || deep; } else if (want && mods[want] && want !== deep) {
            sel = want;
            deep = want;
          }
          if (sel !== 'all' && !mods[sel]) sel = 'all';
          var items = [['all', t('r.all') + ' · ' + rs.length]].concat(Object.keys(mods).sort().map(function (k) { return [k, k + ' · ' + mods[k]]; }));
          body.appendChild(h('div.callout', UI.icon('info'), ' ', t('r.reverse.note')));
          body.appendChild(UI.tabs(items, sel, function (k) { sel = k; C.redraw(); }));
          body.appendChild(C.section('routes', {
            noDoc: true, addLabel: t('r.add'), empty: t('r.none'),
            filter: function (r) { return !isForward(r) && (sel === 'all' || (r && r.handler && r.handler.module) === sel); },
            make: function (arr) {
              var mod = sel === 'all' ? 'proxy' : sel;
              var r = { name: uniqueName(arr, 'route'), handler: { module: mod } };
              var ups = C.draft.upstreams || [];
              if (mod === 'proxy' && ups.length && ups[0].name) r.handler.params = { upstream: ups[0].name };
              return r;
            },
            chips: function (r) { return routeChips(UI, r); }
          }));
        }, { members: ['routes'], rawNote: t('r.raw.note') });
      }
      function upstreamsPage() {
        var el = sh.page('upstreams', t('s.upstreams'));
        var health = {};
        UI.api('GET', '/api/status').then(function (st) {
          (st.upstreams || []).forEach(function (u) {
            health[u.name] = { up: u.backends.filter(function (b) { return b.up; }).length, all: u.backends.length,
              backends: u.backends.map(function (b) { return { address: b.address, up: b.up, probed_down: b.probed_down, in_flight: b.in_flight, succeeded: b.succeeded, failed: b.failed }; }) };
          });
        }).catch(function () { /* health is extra / 健康状态只是附加信息 */ }).then(function () {
          var C = FitConfig.with(UI);
          places(C);
          C.page(el, sh, function (body) {
            body.appendChild(C.section('upstreams', {
              addLabel: t('u.add'),
              make: function (arr) { return { name: uniqueName(arr, 'backend'), addresses: ['127.0.0.1:8081'] }; },
              chips: function (u) {
                var hl = u && health[u.name];
                return chips(UI, [
                  t('u.addrs', { n: ((u && u.addresses) || []).length }),
                  u && u.load_balancing ? UI.badge(u.load_balancing) : null,
                  hl ? UI.badge(t('u.up', hl), hl.up === hl.all ? 'ok' : hl.up ? 'warn' : 'danger', true) : null,
                  hl && hl.up < hl.all ? askOf(upstreamAsk(UI, u, hl, C.draft.routes), { label: false }) : null
                ]);
              }
            }));
          }, { members: ['upstreams'] });
        });
      }
      return { routes: { 'routes': routesPage, 'upstreams': upstreamsPage } };
    }
  });

  // ---- Forward proxy / 正向代理 ----
  WB.module({
    id: 'gw.forward', server: 'admin', group: 'gateway', order: 4, icon: 'forward', label: 'm.forward', words: WORDS,
    nav: [{ id: 'routes', icon: 'forward', label: 'm.forward' }],
    setup: function (UI, sh) {
      var h = UI.h, t = UI.t;
      return {
        routes: {
          'routes': function () {
            edit(UI, sh, 'routes', t('m.forward'), function (body) {
              var C = FitConfig.with(UI);
              body.appendChild(h('div.callout', UI.icon('info'), ' ', t('fw.note')));
              var socks = (C.draft.listeners || []).filter(function (l) { return l && (l.socks || (l.protocols || []).indexOf('socks') >= 0); });
              if (socks.length) {
                body.appendChild(h('div.row', UI.icon('listen'), h('span', t('fw.socks', { list: '' })),
                  socks.map(function (l) { return h('a', { href: '#/gw.listeners/listeners' }, UI.badge((l.name || '?') + ' ' + (l.address || ''), 'info')); })));
              }
              body.appendChild(C.section('routes', {
                noDoc: true, addLabel: t('fw.add'), empty: t('fw.none'),
                filter: isForward,
                make: function (arr) {
                  return { name: uniqueName(arr, 'forward'), match: { method: ['CONNECT'] },
                    handler: { module: 'forward_proxy', params: { clients: ['127.0.0.1/32'], ports: [443] } } };
                },
                chips: function (r) {
                  var p = (r && r.handler && r.handler.params) || {};
                  return chips(UI, [(p.clients || []).length ? h('span.muted.small', (p.clients || []).join(', ')) : null,
                    (p.ports || []).length ? h('span.muted.small', ':' + p.ports.join(',')) : null,
                    r && r.listeners && r.listeners.length ? h('span.muted.small', '@' + r.listeners.join(',')) : null]);
                }
              }));
            }, { members: ['routes'], rawNote: t('r.raw.note') });
          }
        }
      };
    }
  });

  // ---- L4 / 四层代理 ----
  WB.module({
    id: 'gw.l4', server: 'admin', group: 'gateway', order: 5, icon: 'layers', label: 'm.l4', words: WORDS,
    nav: [{ id: 'services', icon: 'layers', label: 's.services' }],
    setup: function (UI, sh) {
      var t = UI.t;
      return {
        routes: {
          'services': function () {
            edit(UI, sh, 'services', t('s.services'), function (body) {
              var C = FitConfig.with(UI);
              body.appendChild(C.section('services', {
                addLabel: t('sv.add'),
                make: function (arr) {
                  var ups = C.draft.upstreams || [], ls = (C.draft.listeners || []).filter(function (l) { return (l.protocols || []).indexOf('tcp') >= 0 || (l.protocols || []).indexOf('udp') >= 0; });
                  return { name: uniqueName(arr, 'service'), listeners: ls.length && ls[0].name ? [ls[0].name] : [], upstream: ups.length && ups[0].name ? ups[0].name : '' };
                },
                chips: function (s) {
                  return chips(UI, [s && s.listeners && s.listeners.length ? '@' + s.listeners.join(',') : null,
                    s && s.upstream ? '→ ' + s.upstream : null,
                    s && s.proxy_protocol ? UI.badge('proxy_protocol ' + s.proxy_protocol) : null]);
                }
              }));
            }, members(['services']));
          }
        }
      };
    }
  });

  // ---- TLS / 证书 ----
  WB.module({
    id: 'gw.tls', server: 'admin', group: 'gateway', order: 6, icon: 'lock', label: 'm.tls', words: WORDS,
    nav: [{ id: 'certs', icon: 'shield', label: 's.certs' }, { id: 'auto_https', icon: 'lock', label: 's.auto_https' }, { id: 'acme', icon: 'key', label: 's.acme' }],
    setup: function (UI, sh) {
      var h = UI.h, t = UI.t;
      function certs() {
        var el = sh.page('certs', t('s.certs'));
        var box = h('div');
        UI.mount(el, h('div.callout', UI.icon('info'), ' ', t('c.note')), UI.card(t('s.certs'), box, null, true));
        function load() {
          UI.api('GET', '/api/status').then(function (st) { UI.mount(box, FitConfig.certTable(UI, st.certificates || [])); }).catch(UI.fail);
        }
        load();
        UI.every(30000, load);
      }
      function sec(name) {
        return function () {
          edit(UI, sh, name, t('s.' + name), function (body) { body.appendChild(FitConfig.with(UI).section(name)); }, members([name]));
        };
      }
      return { routes: { 'certs': certs, 'auto_https': sec('auto_https'), 'acme': sec('acme') } };
    }
  });


  // ---- Cache / 缓存 ----
  WB.module({
    id: 'gw.cache', server: 'admin', group: 'gateway', order: 7, icon: 'disk', label: 'm.cache', words: WORDS,
    nav: [{ id: 'zones', icon: 'disk', label: 's.zones' }],
    setup: function (UI, sh) {
      var h = UI.h, t = UI.t;
      function bytes(n) { return n == null ? '—' : n < 1024 ? n + ' B' : n < 1048576 ? (n / 1024).toFixed(1) + ' KiB' : UI.fmtMB(n / 1048576); }
      function purge(req) {
        return UI.api('POST', '/api/cache/purge', req).then(function (r) { UI.toast(t('z.purged', { n: (r && r.purged) || 0 })); });
      }
      function zones() {
        var el = sh.page('zones', t('s.zones'));
        var box = h('div'), zoneSel = h('select.select.select--sm', h('option', { value: '' }, t('z.anyzone')));
        var urls = h('textarea.textarea.textarea--code', { rows: 4, spellcheck: 'false', placeholder: 'https://example.com/page' });
        var tags = h('textarea.textarea.textarea--code', { rows: 3, spellcheck: 'false' });
        var all = h('input', { type: 'checkbox' });
        var go = UI.btn(t('z.purge'), 'trash', function () {
          var lines = function (ta) { return ta.value.split('\n').map(function (x) { return x.trim(); }).filter(Boolean); };
          var req = { zone: zoneSel.value, urls: lines(urls), tags: lines(tags), all: all.checked };
          if (!req.urls.length && !req.tags.length && !req.all) { UI.toast(t('z.need'), 'error'); return; }
          (req.all ? UI.confirm(t('z.all.q', { zone: req.zone || t('z.every') }), t('z.purge'), true) : Promise.resolve(true)).then(function (ok) {
            if (ok) UI.busy(go, purge(req).then(load)).catch(UI.fail);
          });
        }, 'danger');
        UI.mount(el, UI.card(t('s.zones'), box, UI.btn(t('refresh'), 'refresh', function () { load(); }, 'ghost sm'), true),
          UI.card(t('z.purge'), h('div.form', UI.field(t('z.zone'), zoneSel), UI.field(t('z.urls'), urls), UI.field(t('z.tags'), tags),
            h('label.check', all, h('span', t('z.all'))), h('div.btn-row', go))));
        function load() {
          return UI.api('GET', '/api/cache').then(function (zs) {
            zs = zs || [];
            var cur = zoneSel.value;
            UI.mount(zoneSel, h('option', { value: '' }, t('z.anyzone')), zs.map(function (z) { return h('option', { value: z.zone, selected: z.zone === cur }, z.zone); }));
            UI.mount(box, UI.table([
              { label: t('z.zone'), render: function (z) { return h('b', z.zone); } },
              { label: t('z.keys'), cls: 'num', render: function (z) { return z.keys; } },
              { label: t('z.bytes'), cls: 'num', render: function (z) { return bytes(z.bytes); } },
              { label: t('z.disk'), cls: 'num', render: function (z) { return z.disk_entries || '—'; } },
              { label: t('z.diskbytes'), cls: 'num', render: function (z) { return z.disk_bytes ? bytes(z.disk_bytes) : '—'; } },
              { label: t('z.path'), render: function (z) { return z.disk_path ? h('code.small', z.disk_path) : '—'; } },
              { label: '', cls: 'actions', render: function (z) {
                var b = UI.btn(t('z.purgeall'), 'trash', function () {
                  UI.confirm(t('z.all.q', { zone: z.zone }), t('z.purge'), true).then(function (ok) {
                    if (ok) UI.busy(b, purge({ zone: z.zone, all: true }).then(load)).catch(UI.fail);
                  });
                }, 'ghost sm');
                return b;
              } }
            ], zs, { empty: t('z.none') }));
          }).catch(UI.fail);
        }
        load();
        UI.every(5000, load);
      }
      return { routes: { 'zones': zones } };
    }
  });

  // ---- System: the config's other members / 系统：配置的其余成员 ----
  WB.module({
    id: 'sys.config', server: 'admin', group: 'system', order: 1, icon: 'settings', label: 'm.config', words: WORDS,
    // The last page, transfer, exports and imports the whole config (config.js's dialogs). / 最后一页 transfer 导出与导入整个配置。
    nav: SYS.concat(['other']).map(function (s) { return { id: s, icon: SYS_ICONS[s], label: 's.' + s }; })
      .concat([{ id: 'transfer', icon: 'swap', label: 'cf.ie.page' }]),
    setup: function (UI, sh) {
      var h = UI.h, t = UI.t;
      var routes = {};
      SYS.forEach(function (name) {
        routes[name] = function () {
          edit(UI, sh, name, t('s.' + name), function (body) {
            if (ROLES[name]) {
              body.appendChild(h('div.callout.callout--warn', UI.icon('info'), ' ', t('role.note')));
            }
            if (name === 'pprof') body.appendChild(h('div.callout', UI.icon('info'), ' ', t('pprof.note')));
            body.appendChild(FitConfig.with(UI).section(name));
          }, members([name]));
        };
      });
      routes.other = function () {
        edit(UI, sh, 'other', t('s.other'), function (body) {
          var C = FitConfig.with(UI);
          var rest = restMembers(C);
          if (!rest.length) { body.appendChild(UI.empty(t('other.none'))); return; }
          rest.forEach(function (s) { body.appendChild(h('h3', { style: { margin: '8px 0 0' } }, h('code', s))); body.appendChild(C.section(s)); });
        }, { members: function () { return restMembers(FitConfig.with(UI)); } });
      };
      // transfer: the whole config to a file and back; each page with format tabs does the same for its part.
      // transfer：整个配置导出为文件或从文件导入；带格式标签的页面对其部分做同样的事。
      routes.transfer = function () {
        edit(UI, sh, 'transfer', t('cf.ie.page'), function (body) {
          var C = FitConfig.with(UI);
          body.appendChild(h('div.callout', UI.icon('info'), ' ', t('cf.ie.page.note')));
          body.appendChild(UI.card(t('cf.ie.export.title', { what: t('cf.ie.whole') }), h('div.stack',
            h('div.small.muted', t('cf.ie.export.note')), h('div.small.muted', t('cf.ie.formats')),
            h('div.btn-row', UI.btn(t('cf.ie.export'), 'download', function () { C.exportDialog([], 'yaml'); }, 'primary')))));
          body.appendChild(UI.card(t('cf.ie.import.title', { what: t('cf.ie.whole') }), h('div.stack',
            h('div.callout.callout--warn', UI.icon('alert'), ' ', t('cf.ie.warn.whole')),
            h('div.btn-row', UI.btn(t('cf.ie.import'), 'upload', function () { C.importDialog([]); }, 'danger')))));
        });
      };
      return { routes: routes };
    }
  });

  // ---- System: the config as JSON / 系统：JSON 配置 ----
  WB.module({
    id: 'sys.json', server: 'admin', group: 'system', order: 2, icon: 'code', label: 'm.json', words: WORDS,
    nav: [{ id: 'json', icon: 'code', label: 'm.json' }],
    setup: function (UI, sh) {
      var h = UI.h, t = UI.t;
      var tab = 'draft';
      function draftTab(body, C) {
        var ta = h('textarea.textarea.textarea--code.cf-raw', { spellcheck: 'false', 'aria-label': t('s.json') });
        var err = h('div.callout.callout--danger', { hidden: true });
        ta.value = C.rawText != null ? C.rawText : JSON.stringify(C.draft, null, 2);
        function parse() {
          try {
            var v = JSON.parse(ta.value);
            if (!v || typeof v !== 'object' || Array.isArray(v)) throw new Error('not an object');
            C.draft = v;
            C.setRaw(ta.value, null);
            err.hidden = true;
          } catch (e) {
            C.setRaw(ta.value, e.message);
            err.textContent = t('j.bad', { e: e.message });
            err.hidden = false;
          }
          C.changed();
        }
        if (C.rawText != null) { try { JSON.parse(ta.value); } catch (e) { err.textContent = t('j.bad', { e: e.message }); err.hidden = false; } }
        ta.addEventListener('input', parse);
        var file = h('input', { type: 'file', accept: '.json,application/json', hidden: true, onchange: function () {
          var f = file.files[0];
          if (!f) return;
          f.text().then(function (x) { ta.value = x; parse(); file.value = ''; });
        } });
        var tools = h('div.btn-row', file,
          UI.btn(t('j.format'), 'code', function () { if (C.rawText == null) { ta.value = JSON.stringify(C.draft, null, 2); } }, 'ghost sm'),
          UI.btn(t('copy'), 'copy', function () { UI.copy(ta.value); }, 'ghost sm'),
          UI.btn(t('j.import'), 'upload', function () { file.click(); }, 'ghost sm'),
          UI.btn(t('j.export'), 'download', function () {
            var a = h('a', { href: URL.createObjectURL(new Blob([ta.value], { type: 'application/json' })), download: 'fit-config-' + (C.version || 'draft') + '.json' });
            document.body.appendChild(a); a.click(); a.remove();
            setTimeout(function () { URL.revokeObjectURL(a.href); }, 1000);
          }, 'ghost sm'));
        body.appendChild(h('div.callout', UI.icon('info'), ' ', t('j.note')));
        body.appendChild(UI.card(t('s.json'), h('div.stack', err, ta), tools));
      }
      function effectiveTab(body) {
        var box = h('div.stack', h('div.muted', t('loading')));
        body.appendChild(h('div.callout', UI.icon('info'), ' ', t('j.eff.note')));
        body.appendChild(box);
        UI.api('GET', '/api/config/effective').then(function (e) {
          UI.mount(box,
            UI.card(t('j.precedence'), h('div.btn-row', (e.precedence || []).map(function (p, i) { return [i ? h('span.muted', '›') : null, UI.badge(p, i ? '' : 'primary')]; }))),
            UI.card(t('j.sources'), UI.table([
              { label: t('j.path'), render: function (s) { return h('code.small', s.path); } },
              { label: t('j.source'), render: function (s) { return UI.badge(s.source); } },
              { label: '', render: function (s) { return s.from ? h('code.small.muted', s.from) : ''; } }
            ], e.sources || []), null, true),
            UI.card(t('j.effective') + ' · ' + t('v.version') + ' ' + e.version, h('pre.cf-pre', JSON.stringify(e.config, null, 2)),
              UI.btn(t('copy'), 'copy', function () { UI.copy(JSON.stringify(e.config, null, 2)); }, 'ghost sm')));
        }).catch(function (e) { UI.mount(box, h('div.callout.callout--danger', e.message)); });
      }
      return {
        routes: {
          'json': function () {
            edit(UI, sh, 'json', t('s.json'), function (body) {
              var C = FitConfig.with(UI);
              body.appendChild(UI.tabs([['draft', t('j.draft')], ['effective', t('j.effective')]], tab, function (k) { tab = k; C.redraw(); }));
              if (tab === 'draft') draftTab(body, C); else effectiveTab(body);
            });
          }
        }
      };
    }
  });

  // ---- System: versions / 系统：版本 ----
  WB.module({
    id: 'sys.versions', server: 'admin', group: 'system', order: 3, icon: 'history', label: 'm.versions', words: WORDS,
    nav: [{ id: 'versions', icon: 'history', label: 'm.versions' }],
    setup: function (UI, sh) {
      var h = UI.h, t = UI.t;
      function bytes(n) { return n < 1024 ? n + ' B' : (n / 1024).toFixed(1) + ' KiB'; }
      function rollback(n, after) {
        var C = FitConfig.with(UI);
        return (C.dirty ? UI.confirm(t('v.dirty.q'), t('v.rollback'), true) : Promise.resolve(true)).then(function (ok) {
          return ok && UI.confirm(t('v.rollback.q', { n: n }), t('v.rollback'), true);
        }).then(function (ok) {
          if (!ok) return false;
          return C.rollback(n).then(function (res) { UI.toast(t('v.done', { n: n, v: res.version })); if (after) after(); });
        });
      }
      function view(n, running, after) {
        UI.raw('GET', '/api/versions/' + encodeURIComponent(n)).then(function (r) {
          return r.text().then(function (x) {
            if (!r.ok) { var j = {}; try { j = JSON.parse(x); } catch (e) { /* text / 文本 */ } throw new Error(j.error || r.status + ' ' + r.statusText); }
            var pretty = x;
            try { pretty = JSON.stringify(JSON.parse(x), null, 2); } catch (e) { /* as written / 原样 */ }
            var actions = [{ label: t('copy'), onclick: function () { UI.copy(pretty); return false; } }];
            if (!running) actions.push({ label: t('v.rollback'), kind: 'danger', onclick: function () { return rollback(n, after); } });
            actions.push({ label: t('close') });
            UI.modal({ title: t('v.title', { n: n }), wide: true, body: h('pre.cf-pre', pretty), actions: actions });
          });
        }).catch(UI.fail);
      }
      function page() {
        var el = sh.page('versions', t('s.versions'));
        var C = FitConfig.with(UI);
        places(C);
        C.page(el, sh, function (body) {
          var box = h('div', h('div.muted', t('loading')));
          body.appendChild(h('div.callout', UI.icon('info'), ' ', t('v.note')));
          body.appendChild(UI.card(t('s.versions'), box, UI.btn(t('refresh'), 'refresh', function () { load(); }, 'ghost sm'), true));
          function load() {
            UI.api('GET', '/api/versions').then(function (vs) {
              var list = (vs.versions || []).slice().reverse();
              UI.mount(box, UI.table([
                { label: t('v.version'), render: function (v) { return h('span', h('b', String(v.version)), v.version === vs.current ? [' ', UI.badge(t('v.running'), 'ok', true)] : null); } },
                { label: t('v.applied'), render: function (v) { var s = Date.parse(v.applied) / 1000; return h('span', UI.fmtTime(s), ' ', h('span.muted.small', UI.ago(s))); } },
                { label: t('v.source'), render: function (v) { return UI.badge(v.source); } },
                { label: t('v.size'), cls: 'num', render: function (v) { return bytes(v.bytes); } },
                { label: t('v.etag'), render: function (v) { return h('code.small', v.etag); } },
                { label: '', cls: 'actions', render: function (v) {
                  var running = v.version === vs.current;
                  var b = running ? null : UI.btn(t('v.rollback'), 'undo', function () { UI.busy(b, rollback(v.version, load)).catch(UI.fail); }, 'ghost sm');
                  return h('span.btn-row', { style: { justifyContent: 'flex-end' } }, UI.btn(t('v.view'), 'log', function () { view(v.version, running, load); }, 'ghost sm'), b);
                } }
              ], list));
            }).catch(UI.fail);
          }
          load();
        });
      }
      return { routes: { 'versions': page } };
    }
  });
})();
