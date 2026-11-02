// The tunnel node's module: overview, the hubs it links to (added, changed and removed here), the services it
// reaches (edited here and told to the hubs) and its tunnels (with the tunnel map and each tunnel's path). What it
// changes is written into tunnel.node in fit's config. Needs ui.js and tunnel/shared.js.
//
// 隧道 node 模块：概览、所连的 hub（可在此添加、修改与移除）、可访问的服务（在此编辑并通知 hub）与隧道（含隧道全景图与
// 每条隧道的链路）。所做修改写入 fit 配置的 tunnel.node。依赖 ui.js 与 tunnel/shared.js。
(function () {
  'use strict';
  // [key, en, zh] / [键, 英文, 中文]
  var WORDS = [
    ['mod.label', 'Node', '节点'],
    ['nav.overview', 'Overview', '概览'], ['nav.hubs', 'Hubs', 'hub'], ['nav.services', 'Services', '服务'], ['nav.tunnels', 'Tunnels', '隧道'],
    ['ov.node', 'This node', '本节点'], ['ov.hubs_sub', '{n} registered', '{n} 个已注册'], ['ov.services_sub', 'reached by this node', '本节点可访问'],
    ['ov.tunnels_sub', 'in {in} · out {out}', '入 {in} · 出 {out}'], ['ov.meta', 'Meta', '元信息'],
    ['services.title', 'Services this node reaches', '本节点可访问的服务'], ['services.revert', 'Revert', '还原'],
    ['services.save', 'Save and tell hubs', '保存并通知 hub'], ['services.dirty', 'Unsaved changes', '有未保存的修改'],
    ['no.hubs', 'No hubs: add the hub this node links to.', '没有 hub：添加本节点要连接的 hub。'],
    ['no.tunnels', 'No open tunnels.', '暂无打开的隧道。']
  ];
  var en = {}, zh = {};
  WORDS.forEach(function (w) { en[w[0]] = w[1]; zh[w[0]] = w[2]; });
  var I18N = { 'en': en, 'zh-CN': zh };

  WB.module({
    id: 'tunnel.node', group: 'tunnel', order: 2, icon: 'pod', label: 'mod.label', i18n: I18N,
    nav: [
      { id: 'overview', icon: 'dashboard', label: 'nav.overview' }, { id: 'hubs', icon: 'hub', label: 'nav.hubs' },
      { id: 'services', icon: 'plug', label: 'nav.services' }, { id: 'tunnels', icon: 'tunnel', label: 'nav.tunnels' }
    ],
    setup: function (UI, sh) {
      var h = UI.h, t = UI.t, K = TunnelKit.with(UI);
      var state = { hubs: [], services: [], tunnels: [] };

      // poll loads the status now and every 3s, calling draw with it; flow, when given, is refreshed too.
      // poll 立即并每 3 秒加载一次状态，用它调用 draw；给出 flow 时一并刷新。
      function poll(draw, flow) {
        function load() {
          return UI.api('GET', '/api/status').then(function (st) {
            state = st; state.hubs = st.hubs || []; state.services = st.services || []; state.tunnels = st.tunnels || [];
            draw();
            if (flow) flow.refresh();
          }).catch(UI.fail);
        }
        load();
        UI.every(3000, load);
        return load;
      }
      function serviceRTT(name) { return state.service_rtt && state.service_rtt[name]; }
      function transport(a) { return t('tk.link_over', { p: K.protoName(K.protoOf(a)) }); }

      // ---- Tunnel diagrams: proxies, hubs, this node and its services. A node's tunnel end sends the service's
      // bytes (out) down and gets the proxy's (in). / 隧道图：proxy、hub、本节点及其服务。节点端把服务的字节（out）
      // 向下发送，收到 proxy 的字节（in）。
      function tunnelOf(tn) { return { id: tn.id, name: tn.peer + ' → ' + tn.hub + ' → ' + tn.service, up: tn.in, down: tn.out, created: tn.created }; }
      function flowMe(g, col) {
        var st = state;
        return g.node('me', col, { kind: 'pod', label: st.name, sub: t('tk.n_services', { n: st.services.length }), self: true, state: 'ok',
          facts: [[t('tk.hubs'), st.hubs.length], [t('tk.services'), st.services.length], [t('tk.version'), st.version]] });
      }
      function flowHub(g, col, name) {
        var sv = state.hubs.filter(function (x) { return x.name === name; })[0];
        return g.node('s:' + name, col, { kind: 'hub', label: name, sub: sv ? sv.addr : '', state: sv ? K.flowState(sv.state) : 'wait',
          facts: sv ? [[t('tk.address'), sv.addr], [t('tk.state'), sv.error || t('tk.st.' + sv.state)], [t('tk.latency'), K.rttText(sv.rtt)],
            [t('tk.protocol'), transport(sv.addr)], [t('tk.body_enc'), sv.body_encrypted ? 'AES-256-GCM' : t('tk.none')], [t('tk.peer_id'), sv.peer_id || ''],
            [t('tk.links'), sv.pool + '/' + sv.pool_size], [t('tk.tunnels'), sv.tunnels]] : [] });
      }
      function flowService(g, col, name) {
        var sv = state.services.filter(function (x) { return x.name === name; })[0] || { name: name };
        return g.node('n:' + name, col, { kind: 'node', icon: K.nodeIcon(name), label: name, sub: sv.address || '',
          facts: [[t('tk.address'), sv.address], [t('tk.desc'), sv.description], [t('tk.connect'), K.rttText(serviceRTT(name))]] });
      }
      function flowAll() {
        var st = state, g = K.graph([t('tk.proxies'), t('tk.hubs'), t('tk.node'), t('tk.services')]);
        flowMe(g, 2);
        st.hubs.forEach(function (sv) { flowHub(g, 1, sv.name); g.link('s:' + sv.name, 'me', { rtt: sv.rtt }); });
        st.services.forEach(function (sv) { flowService(g, 3, sv.name); g.link('me', 'n:' + sv.name, { rtt: serviceRTT(sv.name) }); });
        st.tunnels.forEach(function (tn) {
          g.node('c:' + tn.peer, 0, { kind: 'proxy', label: tn.peer, state: 'ok' }); flowHub(g, 1, tn.hub); flowService(g, 3, tn.service);
          g.tunnel(tunnelOf(tn), ['c:' + tn.peer, 's:' + tn.hub, 'me', 'n:' + tn.service]);
        });
        return g;
      }
      function flowOne(id) {
        var st = state, tn = st.tunnels.filter(function (x) { return x.id === id; })[0];
        if (!tn) return null;
        var g = K.graph([t('tk.local_app'), t('tk.proxy'), t('tk.hub'), t('tk.node'), t('tk.service')]);
        var sv = st.hubs.filter(function (x) { return x.name === tn.hub; })[0] || {}, enc = sv.body_encrypted;
        g.node('app', 0, { kind: 'app', label: t('tk.local_app'), sub: t('tk.not_visible'), unknown: true, note: t('tk.only_proxy') });
        g.node('c:' + tn.peer, 1, { kind: 'proxy', label: tn.peer, state: 'ok' });
        flowHub(g, 2, tn.hub);
        flowMe(g, 3).facts.push([t('tk.local'), tn.local]);
        flowService(g, 4, tn.service);
        var c = tn.connect_rtt ? { last: tn.connect_rtt, avg: tn.connect_rtt, min: tn.connect_rtt, loss: 0, samples: 1 } : null;
        g.tunnel(tunnelOf(tn), ['app', 'c:' + tn.peer, 's:' + tn.hub, 'me', 'n:' + tn.service], [
          { label: 'TCP' }, { label: t('tk.link'), lock: enc }, { label: transport(sv.addr || ''), lock: enc, rtt: sv.rtt }, { label: 'TCP', rtt: c }]);
        return g;
      }
      function newFlow() { return K.flow({ all: flowAll, one: flowOne }); }
      function mapButton(flow) { return UI.btn(t('tk.map'), 'route', function () { flow.open(); }, 'sm'); }

      // ---- Overview / 概览 ----
      function overview(el) {
        var flow = newFlow(), stats = h('div.grid.grid--4'), info = h('div'), hubs = h('div');
        UI.mount(sh.tools, mapButton(flow));
        UI.mount(el, h('div.stack', stats, h('div.grid.grid--2', UI.card(t('ov.node'), info), UI.card(t('nav.hubs'), hubs, null, true))));
        poll(function () {
          var st = state, reg = st.hubs.filter(function (x) { return x.state === 'registered'; }).length;
          var tin = st.tunnels.reduce(function (a, x) { return a + x.in; }, 0), tout = st.tunnels.reduce(function (a, x) { return a + x.out; }, 0);
          UI.mount(stats,
            UI.stat('hub', t('nav.hubs'), reg + ' / ' + st.hubs.length, t('ov.hubs_sub', { n: reg }), st.hubs.length && reg === st.hubs.length ? 'ok' : 'warn'),
            UI.stat('plug', t('nav.services'), st.services.length, t('ov.services_sub'), 'info'),
            UI.stat('tunnel', t('nav.tunnels'), st.tunnels.length, t('ov.tunnels_sub', { 'in': K.bytes(tin), out: K.bytes(tout) })),
            UI.stat('activity', t('tk.open_tunnels'), st.open + ' / ' + st.max_tunnels, null));
          var meta = st.meta || {};
          UI.mount(info, UI.kv([[t('tk.name'), h('b', st.name)], [t('tk.version'), st.version],
            [t('tk.tunnels'), t('tk.open_of', { open: st.open, max: st.max_tunnels })],
            [t('ov.meta'), Object.keys(meta).length ? h('div', Object.keys(meta).map(function (k) { return h('div.small.mono', k + '=' + meta[k]); })) : '']]));
          UI.mount(hubs, UI.table([
            { label: t('tk.name'), render: function (x) { return h('b', x.name); } },
            { label: t('tk.state'), render: function (x) { return K.stateBadge(x.state, x.error); } },
            { label: t('tk.latency'), render: function (x) { return K.rtt(x.rtt); } },
            { label: t('tk.tunnels'), cls: 'num', render: function (x) { return x.tunnels; } }
          ], st.hubs, { empty: t('no.hubs'), onRowClick: function () { location.hash = '#/tunnel.node/hubs'; } }));
        }, flow);
      }

      // ---- Hubs / hub ----
      function hubsPage(el) {
        var box = h('div'), load;
        var add = UI.btn(t('tk.add_hub'), 'plus', function () { K.hubEditor(null, function () { setTimeout(load, 300); }); }, 'primary sm');
        UI.mount(el, h('div.stack', K.configNote('node'), UI.card(t('nav.hubs'), box, add, true)));
        load = poll(function () {
          UI.mount(box, UI.table([
            { label: t('tk.name'), render: function (x) { return h('b', x.name); } },
            { label: t('tk.protocols'), render: K.endpointList },
            { label: t('tk.state'), render: function (x) {
              return h('div', K.stateBadge(x.state, x.error), K.linkProtos(x), x.body_encrypted ? h('div.small.muted', UI.icon('lock'), ' ', t('tk.bodies_enc')) : null);
            } },
            { label: t('tk.latency'), render: function (x) { return K.rtt(x.rtt); } },
            { label: t('tk.hub'), render: function (x) { return x.hub || '—'; } },
            { label: t('tk.peer_id'), render: function (x) { return h('div', x.peer_id || '—', h('div.small.muted', t('tk.links_n', { n: x.pool + '/' + x.pool_size }))); } },
            { label: t('tk.tunnels'), cls: 'num', render: function (x) { return x.tunnels; } },
            { label: '', cls: 'actions', render: function (x) {
              return h('div.btn-row', { style: { justifyContent: 'flex-end' } },
                UI.btn(t('tk.edit'), 'edit', function () { K.hubEditor(x, function () { setTimeout(load, 300); }); }, 'ghost sm'),
                UI.btn(t('tk.remove'), 'trash', function () { K.delHub(x.name, function () { setTimeout(load, 300); }); }, 'danger sm'));
            } }
          ], state.hubs, { empty: t('no.hubs') }));
        });
      }

      // ---- Services: edited in place; a poll does not overwrite them once changed. / 服务：原地编辑；修改后轮询不覆盖。
      function servicesPage(el) {
        var box = h('div'), ed = null, dirtyNote = h('span.small.muted');
        function fill() {
          ed = K.services(state.services, function (sv) { return sv.name ? K.rtt(serviceRTT(sv.name)) : ''; }, function () { dirtyNote.textContent = t('services.dirty'); });
          dirtyNote.textContent = '';
          UI.mount(box, ed.el);
        }
        var save = UI.btn(t('services.save'), 'save', function () {
          UI.busy(save, K.call('/api/services', ed.read(), t('tk.services_saved'))).then(function (ok) {
            if (ok) setTimeout(function () { load().then(fill); }, 300);
          });
        }, 'primary');
        var tools = h('div.btn-row', UI.btn(t('tk.add_service'), 'plus', function () { ed.add({}); }, 'sm'),
          UI.btn(t('services.revert'), 'undo', function () { load().then(fill); }, 'sm'));
        UI.mount(el, h('div.stack', K.configNote('node'), UI.card(t('services.title'), h('div.stack', box,
          h('div.btn-row', { style: { padding: '0 16px 16px' } }, save, dirtyNote)), tools, true)));
        var first = true;
        var load = poll(function () {
          if (first || !ed || !ed.dirty()) { first = false; fill(); }
        });
      }

      // ---- Tunnels / 隧道 ----
      function tunnelsPage(el) {
        var flow = newFlow(), box = h('div'), load;
        UI.mount(el, UI.card(t('nav.tunnels'), box, mapButton(flow), true));
        load = poll(function () {
          UI.mount(box, UI.table([
            { label: t('tk.hub'), render: function (x) { return x.hub; } },
            { label: t('tk.id'), render: function (x) { return h('span.mono.small', x.id); } },
            { label: t('tk.proxy'), render: function (x) { return x.peer; } },
            { label: t('tk.service'), render: function (x) { return h('b', x.service); } },
            { label: t('tk.address'), render: function (x) { return h('span.mono', x.addr); } },
            { label: t('tk.connect'), render: function (x) {
              return x.connect_rtt ? K.rtt({ last: x.connect_rtt, avg: x.connect_rtt, min: x.connect_rtt, loss: 0, samples: 1 }) : '';
            } },
            { label: t('tk.local'), render: function (x) { return h('span.mono.small', x.local); } },
            { label: t('tk.in'), cls: 'num', render: function (x) { return K.bytes(x.in); } },
            { label: t('tk.out'), cls: 'num', render: function (x) { return K.bytes(x.out); } },
            { label: t('tk.age'), render: function (x) { return K.dur(x.created); } },
            { label: '', cls: 'actions', render: function (x) {
              return h('div.btn-row', { style: { justifyContent: 'flex-end' } },
                UI.btn(t('tk.details'), 'route', function () { flow.open(x.id); }, 'ghost sm'),
                UI.btn(t('tk.close'), 'x', function () {
                  K.call('/api/tunnels/close', { hub: x.hub, id: x.id }, t('tk.tunnel_closed')).then(function () { load(); });
                }, 'danger sm'));
            } }
          ], state.tunnels, { empty: t('no.tunnels') }));
        }, flow);
      }

      return {
        fallback: 'overview',
        routes: {
          'overview': function () { overview(sh.page('overview', t('nav.overview'))); },
          'hubs': function () { hubsPage(sh.page('hubs', t('nav.hubs'))); },
          'services': function () { servicesPage(sh.page('services', t('nav.services'))); },
          'tunnels': function () { tunnelsPage(sh.page('tunnels', t('nav.tunnels'))); }
        }
      };
    }
  });
})();
