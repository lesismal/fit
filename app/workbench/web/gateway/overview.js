// The gateway's overview: what fit runs now, read only, from the Admin API's GET endpoints (/status,
// /config/effective, /versions): the running config's version, reloads, connections, the listeners, the upstreams'
// backends and their health, the routes and services and where each came from, the certificates. Needs ui.js,
// workbench.js and config.js (for the certificate table). Ask AI (I128): an upstream with backends down, and a spike
// of the error counters between two refreshes (requests failed on an upstream, connections refused, reloads failed),
// have a button that asks the skill diagnose about it; a spike stays listed for a minute.
//
// 网关概览：当前运行状态，只读，来自 Admin API 的 GET 接口（/status、/config/effective、/versions）：运行配置的版本、
// 重载、连接、监听、上游后端及其健康状态、路由与四层服务及其来源、证书。依赖 ui.js、workbench.js 与 config.js（证书表）。
// “问 AI”（I128）：有后端不可用的上游，以及两次刷新之间错误计数的突增（上游的失败请求、被拒绝的连接、失败的重载）都有按钮，
// 以技能 diagnose 诊断它；突增在列表中保留一分钟。
(function () {
  'use strict';

  // [key, en, zh] / [键, 英文, 中文]
  var WORDS = [
    ['mod.label', 'Overview', '概览'],
    ['t.version', 'Config version', '配置版本'], ['t.version.sub', '{source}, {ago}', '{source}，{ago}'],
    ['t.reloads', 'Reloads', '重载'], ['t.reloads.sub', '{n} failed', '{n} 次失败'],
    ['t.conns', 'Open connections', '当前连接'], ['t.conns.sub', '{n} refused', '{n} 个被拒绝'],
    ['t.routes', 'Routes', '路由'], ['t.routes.sub', '{n} layer 4 services', '{n} 个四层服务'],
    ['t.backends', 'Backends up', '可用后端'], ['t.backends.sub', '{n} upstreams', '{n} 个上游'],
    ['t.certs', 'Certificates', '证书'], ['t.certs.soon', '{n} expire within 14 days', '{n} 个将在 14 天内过期'],
    ['t.certs.ok', 'none expire within 14 days', '14 天内无过期'],
    ['listeners', 'Listeners', '监听'], ['name', 'Name', '名称'], ['address', 'Address', '地址'], ['protocols', 'Protocols', '协议'],
    ['no.listeners', 'No listeners.', '没有监听。'],
    ['sources', 'Sources', '配置来源'], ['source', 'Source', '来源'], ['upstreams', 'Upstreams', '上游'], ['services', 'Services', '四层服务'],
    ['routes', 'Routes', '路由'], ['shadowed', 'Shadowed', '被遮蔽'], ['element', 'Element', '元素'], ['by', 'Kept from', '保留自'],
    ['shadowed.note', 'Elements providers provided that the config leaves out, because a source before them took their names.',
      '配置来源提供、但因名称已被更靠前的来源占用而未采用的元素。'],
    ['match', 'Match', '匹配'], ['middleware', 'Middleware', '中间件'], ['handler', 'Handler', '处理器'], ['everything', 'everything', '全部请求'],
    ['all', 'all', '全部'], ['priority', 'priority {n}', '优先级 {n}'], ['no.routes', 'No routes.', '没有路由。'],
    ['upstream', 'Upstream', '上游'], ['server_names', 'Server names', '服务器名'], ['proxy_protocol', 'PROXY protocol', 'PROXY 协议'],
    ['no.services', 'No layer 4 services.', '没有四层服务。'],
    ['health', 'Upstream health', '上游健康状态'], ['backend', 'Backend', '后端'], ['state', 'State', '状态'],
    ['inflight', 'In flight', '在途'], ['succeeded', 'Succeeded', '成功'], ['failed', 'Failed', '失败'],
    ['up', 'up', '可用'], ['down.probe', 'down (health check)', '不可用（健康检查）'], ['down.fail', 'down (failures)', '不可用（失败过多）'],
    ['u.up', '{up} / {all} up', '{up} / {all} 可用'], ['u.inflight', '{n} in flight', '在途 {n}'], ['no.upstreams', 'No upstreams.', '没有上游。'],
    ['certs', 'Certificates', '证书'], ['filter', 'Filter', '筛选'],
    ['versions', 'Recent versions', '最近版本'], ['all.versions', 'All versions', '全部版本'], ['running', 'running', '运行中'],
    ['applied', 'Applied', '应用时间'], ['version', 'Version', '版本'], ['updated', 'Updated {time}', '更新于 {time}'],
    ['auto', 'Auto refresh', '自动刷新'], ['edit', 'Edit', '编辑'],
    // Ask AI (I128): spikes, and what is asked of the assistant / “问 AI”：突增，以及向助手提的问题
    ['sp.failed', '{n} more requests failed on the upstream {name} in {s}s', '{s} 秒内上游 {name} 又有 {n} 个请求失败'],
    ['sp.refused', '{n} more connections were refused in {s}s', '{s} 秒内又有 {n} 个连接被拒绝'],
    ['sp.reloads', '{n} more config reloads failed in {s}s', '{s} 秒内又有 {n} 次配置重载失败'],
    ['ask.up', 'The upstream {name} has {down} of {all} backends down. Why, and what should I fix?',
      '上游 {name} 的 {all} 个后端中有 {down} 个不可用。原因是什么？应该修复什么？'],
    ['ask.spike', 'Diagnose this spike: {what}. What caused it, and what should I fix?', '诊断这次突增：{what}。原因是什么？应该修复什么？']
  ];

  WB.module({
    id: 'gw.overview', server: 'admin', group: 'gateway', order: 1, icon: 'dashboard', label: 'mod.label', words: WORDS,
    nav: [{ id: 'overview', icon: 'dashboard', label: 'mod.label' }],
    setup: function (UI, sh) {
      var h = UI.h, t = UI.t;
      var auto = true;

      // ---- Ask AI (I128) / 问 AI ----
      function askOf(spec) { return window.FitAI && FitAI.askButton ? FitAI.askButton(spec, { label: false }) : null; }
      // routesTo is the names of the routes proxying to upstream name. / 代理到上游 name 的路由名称。
      function routesTo(cfg, name) {
        return (cfg.routes || []).filter(function (r) {
          var p = r && r.handler && r.handler.params;
          return p && p.upstream === name;
        }).map(function (r) { return r.name; }).filter(Boolean);
      }
      function backends(u) {
        return u.backends.map(function (b) { return { address: b.address, up: b.up, probed_down: b.probed_down, in_flight: b.in_flight, succeeded: b.succeeded, failed: b.failed }; });
      }
      function upAsk(u, n, cfg) {
        return function () {
          var names = routesTo(cfg, u.name);
          return { skill: 'diagnose', message: t('ask.up', { name: u.name, down: u.backends.length - n, all: u.backends.length }),
            inputs: { route: names.join(', ') },
            selection: { upstream: { name: u.name, in_flight: u.in_flight, backends: backends(u) }, routes: names } };
        };
      }
      // A spike is an error counter that rose between two refreshes: {key, text, at, ask}, kept SPIKE_MS.
      // 突增是两次刷新之间增加的错误计数：{key, text, at, ask}，保留 SPIKE_MS。
      var SPIKE_MS = 60000, prev = null, spikes = {};
      function counters(st) {
        var c = { refused: (st.connections || {}).refused || 0, reloads: (st.reloads || {}).failed || 0, failed: {}, at: Date.now() };
        (st.upstreams || []).forEach(function (u) { c.failed[u.name] = u.backends.reduce(function (a, b) { return a + (b.failed || 0); }, 0); });
        return c;
      }
      // spot compares st with the refresh before and records the counters that rose. / 与上次刷新比较，记录增加的计数。
      function spot(st, cfg) {
        var now = counters(st), secs = prev ? Math.max(1, Math.round((now.at - prev.at) / 1000)) : 0;
        function add(key, what, sel) {
          var spec = function () {
            return { skill: 'diagnose', message: t('ask.spike', { what: what }), inputs: sel.route ? { route: sel.route } : null,
              selection: { spike: sel, what: what, at: new Date(now.at).toISOString() } };
          };
          spikes[key] = { text: what, at: now.at, ask: spec };
        }
        if (prev) {
          if (now.refused > prev.refused) add('refused', t('sp.refused', { n: now.refused - prev.refused, s: secs }), { counter: 'connections.refused', delta: now.refused - prev.refused, seconds: secs, total: now.refused });
          if (now.reloads > prev.reloads) add('reloads', t('sp.reloads', { n: now.reloads - prev.reloads, s: secs }), { counter: 'reloads.failed', delta: now.reloads - prev.reloads, seconds: secs, total: now.reloads });
          (st.upstreams || []).forEach(function (u) {
            var a = now.failed[u.name], b = prev.failed[u.name];
            if (b == null || a <= b) return;
            add('u:' + u.name, t('sp.failed', { n: a - b, name: u.name, s: secs }), { counter: 'upstream.failed', upstream: u.name, delta: a - b, seconds: secs,
              total: a, backends: backends(u), route: routesTo(cfg, u.name).join(', ') });
          });
        }
        prev = now;
        Object.keys(spikes).forEach(function (k) { if (now.at - spikes[k].at > SPIKE_MS) delete spikes[k]; });
      }

      function list(values) { return values && values.length ? values.map(function (v) { return UI.badge(v); }) : h('span.muted.small', t('all')); }
      function sec(unix) { return Date.parse(unix) / 1000; }
      function sourcesByPath(e) {
        var m = {};
        (e.sources || []).forEach(function (s) { m[s.path] = s.source; });
        return m;
      }
      function matchSummary(m) {
        if (!m) return h('span.muted.small', t('everything'));
        var out = [];
        function add(label, values) {
          if (!values || !values.length) return;
          out.push(h('div.small', h('span.muted', label + ' '), values.map(function (v) { return [h('code', v), ' ']; })));
        }
        add('host', m.host); add('path', m.path); add('path_regexp', m.path_regexp); add('method', m.method);
        Object.keys(m.header || {}).forEach(function (k) { add('header ' + k, m.header[k].length ? m.header[k] : ['(present)']); });
        Object.keys(m.query || {}).forEach(function (k) { add('query ' + k, m.query[k].length ? m.query[k] : ['(present)']); });
        add('remote_ip', m.remote_ip);
        if (m.expr) add('expr', [m.expr]);
        return out.length ? out : h('span.muted.small', t('everything'));
      }
      // filtered hides the rows of a box whose text lacks q. / 隐藏文本不含 q 的行。
      function filterInput(onChange) {
        return h('input.input.input--sm', { type: 'search', placeholder: t('filter'), style: { width: '200px' }, oninput: function (e) { onChange(e.target.value); } });
      }
      function applyFilter(box, sel, q) {
        q = (q || '').trim().toLowerCase();
        box.querySelectorAll(sel).forEach(function (r) { r.hidden = !!q && r.textContent.toLowerCase().indexOf(q) < 0; });
      }

      function page() {
        // A visit compares its own refreshes only. / 每次访问只比较本次访问中的刷新。
        prev = null;
        var el = sh.page('overview', t('mod.label'));
        var stamp = h('span.muted.small');
        var autoBox = h('input', { type: 'checkbox', checked: auto, onchange: function () { auto = autoBox.checked; } });
        UI.mount(sh.tools, stamp, h('label.check.small', autoBox, h('span', t('auto'))),
          UI.btn(t('refresh'), 'refresh', function () { load(); }, 'ghost sm'));
        var tiles = h('div.grid.grid--3'), spBox = h('div.ov-spikes.stack');
        var lsBox = h('div'), srcBox = h('div'), shBox = h('div'), upBox = h('div.stack'), rtBox = h('div'), svBox = h('div'), certBox = h('div'), verBox = h('div');
        var rq = '', uq = '';
        var rtTools = h('div.row', filterInput(function (q) { rq = q; applyFilter(rtBox, 'tbody tr', rq); }),
          h('a.btn.btn--ghost.btn--sm', { href: '#/gw.reverse/routes' }, UI.icon('edit'), t('edit')));
        var upTools = h('div.row', filterInput(function (q) { uq = q; applyFilter(upBox, '.ov-upstream', uq); }),
          h('a.btn.btn--ghost.btn--sm', { href: '#/gw.reverse/upstreams' }, UI.icon('edit'), t('edit')));
        UI.mount(el, spBox, tiles,
          h('div.grid.grid--2', UI.card(t('listeners'), lsBox, h('a.btn.btn--ghost.btn--sm', { href: '#/gw.listeners/listeners' }, UI.icon('edit'), t('edit')), true),
            UI.card(t('sources'), srcBox, null, true)),
          shBox,
          UI.card(t('health'), upBox, upTools),
          UI.card(t('routes'), rtBox, rtTools, true),
          UI.card(t('services'), svBox, h('a.btn.btn--ghost.btn--sm', { href: '#/gw.l4/services' }, UI.icon('edit'), t('edit')), true),
          UI.card(t('certs'), certBox, h('a.btn.btn--ghost.btn--sm', { href: '#/gw.tls/certs' }, UI.icon('shield'), t('certs')), true),
          UI.card(t('versions'), verBox, h('a.btn.btn--ghost.btn--sm', { href: '#/sys.versions/versions' }, UI.icon('history'), t('all.versions')), true));

        function load() {
          return Promise.all([UI.api('GET', '/api/status'), UI.api('GET', '/api/config/effective'), UI.api('GET', '/api/versions')]).then(function (r) {
            if (!document.body.contains(el)) return;
            var st = r[0] || {}, eff = r[1] || {}, vs = r[2] || {};
            var cfg = eff.config || {}, srcs = sourcesByPath(eff);
            var ups = st.upstreams || [], certs = st.certificates || [];
            var up = 0, all = 0;
            ups.forEach(function (u) { all += u.backends.length; up += u.backends.filter(function (b) { return b.up; }).length; });
            var soon = certs.filter(function (c) { return Date.parse(c.not_after) - Date.now() < 14 * 86400e3; }).length;
            var rl = st.reloads || {}, cn = st.connections || {};
            spot(st, cfg);
            var ks = Object.keys(spikes).sort(function (a, b) { return spikes[b].at - spikes[a].at; });
            UI.mount(spBox, ks.map(function (k) {
              var x = spikes[k];
              return h('div.callout.callout--warn', h('span', UI.icon('activity'), ' ', x.text, ' ', h('span.muted.small', UI.ago(x.at / 1000))), askOf(x.ask));
            }));
            UI.mount(tiles,
              UI.stat('layers', t('t.version'), st.version, t('t.version.sub', { source: st.source, ago: UI.ago(sec(st.applied)) }), 'info'),
              UI.stat('refresh', t('t.reloads'), rl.succeeded, t('t.reloads.sub', { n: rl.failed || 0 }), rl.failed ? 'warn' : 'ok'),
              UI.stat('link', t('t.conns'), cn.open, t('t.conns.sub', { n: cn.refused || 0 })),
              UI.stat('route', t('t.routes'), (cfg.routes || []).length, t('t.routes.sub', { n: (cfg.services || []).length })),
              UI.stat('server', t('t.backends'), up + ' / ' + all, t('t.backends.sub', { n: ups.length }), up < all ? 'warn' : 'ok'),
              UI.stat('shield', t('t.certs'), certs.length, soon ? t('t.certs.soon', { n: soon }) : t('t.certs.ok'), soon ? 'warn' : 'ok'));

            UI.mount(lsBox, UI.table([
              { label: t('name'), render: function (l) { return h('b', l.name); } },
              { label: t('address'), render: function (l) { return h('code', l.address); } },
              { label: t('protocols'), render: function (l) { return list(l.protocols); } }
            ], st.listeners || [], { empty: t('no.listeners') }));

            var counts = {}, order = [];
            function bucket(n) { if (!counts[n]) { counts[n] = { listeners: 0, routes: 0, upstreams: 0, services: 0 }; order.push(n); } return counts[n]; }
            (eff.precedence || []).forEach(bucket);
            (eff.sources || []).forEach(function (s) {
              var parts = s.path.split('/'), c = bucket(s.source);
              if (parts.length === 3 && parts[1] in c) c[parts[1]]++;
            });
            UI.mount(srcBox, UI.table([
              { label: t('source'), render: function (n) { return UI.badge(n, 'primary'); } },
              { label: t('listeners'), cls: 'num', render: function (n) { return counts[n].listeners; } },
              { label: t('routes'), cls: 'num', render: function (n) { return counts[n].routes; } },
              { label: t('upstreams'), cls: 'num', render: function (n) { return counts[n].upstreams; } },
              { label: t('services'), cls: 'num', render: function (n) { return counts[n].services; } }
            ], order));

            var shadowed = eff.shadowed || [];
            UI.mount(shBox, shadowed.length ? UI.card(t('shadowed'), h('div', h('div.callout.callout--warn', { style: { margin: '12px 16px' } }, t('shadowed.note')), UI.table([
              { label: t('source'), render: function (s) { return UI.badge(s.source); } },
              { label: t('element'), render: function (s) { return h('code', s.from); } },
              { label: t('name'), render: function (s) { return s.name; } },
              { label: t('by'), render: function (s) { return s.by ? UI.badge(s.by) : '—'; } }
            ], shadowed)), null, true) : null);

            var conf = {};
            (cfg.upstreams || []).forEach(function (u, i) { conf[u.name] = { u: u, source: srcs['/upstreams/' + i] }; });
            UI.mount(upBox, ups.length ? ups.map(function (u) {
              var c = conf[u.name] || {}, n = u.backends.filter(function (b) { return b.up; }).length;
              var kind = n === u.backends.length ? 'ok' : n === 0 ? 'danger' : 'warn';
              return h('div.ov-upstream.stack',
                h('div.row', h('b', u.name), UI.badge(t('u.up', { up: n, all: u.backends.length }), kind, true),
                  h('span.muted.small', t('u.inflight', { n: u.in_flight })), c.u && c.u.load_balancing ? UI.badge(c.u.load_balancing) : null,
                  c.source ? UI.badge(c.source, 'primary') : null, n < u.backends.length ? askOf(upAsk(u, n, cfg)) : null),
                UI.table([
                  { label: t('backend'), render: function (b) { return h('code', b.address); } },
                  { label: t('state'), render: function (b) { return b.up ? UI.badge(t('up'), 'ok', true) : UI.badge(b.probed_down ? t('down.probe') : t('down.fail'), 'danger', true); } },
                  { label: t('inflight'), cls: 'num', render: function (b) { return b.in_flight; } },
                  { label: t('succeeded'), cls: 'num', render: function (b) { return b.succeeded; } },
                  { label: t('failed'), cls: 'num', render: function (b) { return b.failed; } }
                ], u.backends));
            }) : UI.empty(t('no.upstreams')));
            applyFilter(upBox, '.ov-upstream', uq);

            UI.mount(rtBox, UI.table([
              { label: t('name'), render: function (r) { return h('div', r.name ? h('b', r.name) : h('span.muted', '#' + r._i), r.priority ? h('div.muted.small', t('priority', { n: r.priority })) : null); } },
              { label: t('listeners'), render: function (r) { return list(r.listeners); } },
              { label: t('match'), render: function (r) { return matchSummary(r.match); } },
              { label: t('middleware'), render: function (r) { return (r.middleware || []).map(function (m) { return UI.badge(m.module || '?'); }); } },
              { label: t('handler'), render: function (r) {
                var hd = r.handler || {}, p = hd.params || {};
                return h('div', h('code', hd.module || '?'), p.upstream ? h('div.muted.small', '→ ' + p.upstream) : null);
              } },
              { label: t('source'), render: function (r) { return UI.badge(srcs['/routes/' + r._i] || '?'); } }
            ], (cfg.routes || []).map(function (r, i) { var o = Object.create(r); o._i = i; return o; }), { empty: t('no.routes') }));
            applyFilter(rtBox, 'tbody tr', rq);

            UI.mount(svBox, UI.table([
              { label: t('name'), render: function (s) { return h('b', s.name || '#' + s._i); } },
              { label: t('listeners'), render: function (s) { return list(s.listeners); } },
              { label: t('upstream'), render: function (s) { return s.upstream ? h('code', s.upstream) : '—'; } },
              { label: t('server_names'), render: function (s) { return (s.server_names || []).length ? list(s.server_names) : '—'; } },
              { label: t('proxy_protocol'), render: function (s) { return s.proxy_protocol || '—'; } },
              { label: t('source'), render: function (s) { return UI.badge(srcs['/services/' + s._i] || '?'); } }
            ], (cfg.services || []).map(function (s, i) { var o = Object.create(s); o._i = i; return o; }), { empty: t('no.services') }));

            UI.mount(certBox, FitConfig.certTable(UI, certs));

            UI.mount(verBox, UI.table([
              { label: t('version'), render: function (v) { return h('span', h('b', String(v.version)), v.version === vs.current ? [' ', UI.badge(t('running'), 'ok', true)] : null); } },
              { label: t('applied'), render: function (v) { return h('span', UI.fmtTime(sec(v.applied)), ' ', h('span.muted.small', UI.ago(sec(v.applied)))); } },
              { label: t('source'), render: function (v) { return UI.badge(v.source); } }
            ], (vs.versions || []).slice(-5).reverse()));

            stamp.textContent = t('updated', { time: new Date().toLocaleTimeString() });
          }).catch(UI.fail);
        }
        load();
        UI.every(5000, function () { if (auto) load(); });
      }
      return { routes: { 'overview': page } };
    }
  });
})();
