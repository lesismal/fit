// The tunnel hub's module: overview, the nodes and proxies linked to it, its tunnels (with the tunnel map and each
// tunnel's path), its listeners and its settings (passwords, the console socket, the hub's certificate). What it
// changes is written into tunnel.hub in fit's config. Needs ui.js and tunnel/shared.js.
//
// 隧道 hub 模块：概览、连到本 hub 的节点与 proxy、隧道（含隧道全景图与每条隧道的链路）、监听与设置（各密码、控制台
// WebSocket、hub 证书）。所做修改写入 fit 配置的 tunnel.hub。依赖 ui.js 与 tunnel/shared.js。
(function () {
  'use strict';
  // [key, en, zh] / [键, 英文, 中文]
  var WORDS = [
    ['mod.label', 'Hub', 'Hub'],
    ['nav.overview', 'Overview', '概览'], ['nav.nodes', 'Nodes', '节点'], ['nav.proxies', 'Proxies', 'proxy'], ['nav.tunnels', 'Tunnels', '隧道'],
    ['nav.listeners', 'Listeners', '监听'], ['nav.settings', 'Settings', '设置'],
    ['ov.nodes_sub', '{n} services', '{n} 个服务'], ['ov.proxies_sub', '{n} forwarding', '{n} 个有隧道'],
    ['ov.tunnels_sub', '↑ {up} · ↓ {down}', '↑ {up} · ↓ {down}'], ['ov.listeners_sub', '{n} failing', '{n} 个失败'],
    ['ov.listeners_ok', 'all open', '全部已打开'], ['ov.hub', 'This hub', '本 hub'], ['ov.links', 'Registered links', '已注册链路'],
    ['peer_addrs', 'Peer addresses', '对端地址'], ['socket', 'Console socket', '控制台 WebSocket'], ['started', 'Started', '启动时间'],
    ['no.nodes', 'No node is linked: nodes link to this hub at the addresses on the Listeners page.', '没有节点连接：节点通过“监听”页上的地址连接本 hub。'],
    ['no.proxies', 'No proxy is linked.', '没有 proxy 连接。'], ['no.tunnels', 'No open tunnels.', '暂无打开的隧道。'],
    ['no.listeners', 'No listeners: nodes and proxies cannot link.', '没有监听：节点与 proxy 无法连接。'],
    ['node_queried', 'Node queried', '已查询节点'],
    ['add_listener', 'Add listener', '添加监听'], ['edit_listener', 'Edit listener', '编辑监听'], ['path', 'Path', '路径'],
    ['peers_dial', 'Peers dial', '对端连接地址'], ['listener_saved', 'Listener saved', '监听已保存'], ['listener_removed', 'Listener removed', '监听已移除'],
    ['remove_listener_q', 'Remove the listener on {addr}?', '移除 {addr} 上的监听？'],
    ['listeners_note', 'Changes apply at once, without a restart. Removing a QUIC listener closes the links it carries; other listeners\' links stay open until they close.',
      '修改立即生效，无需重启。移除 QUIC 监听会关闭其上的链路；其他监听上的链路保持到自行关闭。'],
    ['listeners_tls', 'TLS, WSS and QUIC listeners authenticate the hub to nodes and proxies that pin its certificate. TCP and WS carry the links in the clear: off a loopback address they need the body password.',
      'TLS、WSS 与 QUIC 监听向固定了本 hub 证书指纹的节点和 proxy 证明 hub 的身份。TCP 与 WS 明文传输链路：用于非回环地址时须设置消息体密码。'],
    ['own_cert', 'its own certificate', '自有证书'],
    ['set.title', 'Passwords and console socket', '密码与控制台 WebSocket'], ['set.password', 'Registration password', '注册密码'],
    ['set.body', 'Body AES password', '消息体 AES 密码'], ['set.clear_body', 'Remove the body password', '移除消息体密码'],
    ['set.socket_pw', 'Console socket password', '控制台 WebSocket 密码'], ['set.clear_socket', 'Remove it, using the registration password', '移除它，改用注册密码'],
    ['set.socket_addr', 'Console over an encrypted WebSocket, address', '经加密 WebSocket 的控制台地址'],
    ['set.socket_ph', 'empty: none, such as :7002', '留空则不启用，如 :7002'],
    ['set.pw_unset', 'not set: peers cannot register', '未设置：对端无法注册'], ['set.socket_default', 'default: the registration password', '默认：注册密码'],
    ['set.note', 'Applied at once. Registered peers stay linked; a new body password applies to the links opened from now on.',
      '立即生效。已注册的对端保持连接；新的消息体密码只用于此后新建的链路。'],
    ['set.save', 'Save settings', '保存设置'], ['set.saved', 'Settings saved', '设置已保存'],
    ['quic.title', 'Hub certificate', 'Hub 证书'], ['quic.fp', 'Fingerprint (SHA-256)', '指纹（SHA-256）'],
    ['quic.note', 'Nodes and proxies pin this fingerprint, as their hub\'s certificate pin, for the hub\'s TLS, WSS and QUIC listeners.', '节点和 proxy 把此指纹填作 hub 的证书指纹，用以固定本 hub 的 TLS、WSS 与 QUIC 监听的证书。'],
    ['quic.none', 'Made, and written into the config, when a TLS, WSS or QUIC listener is added.', '添加 TLS、WSS 或 QUIC 监听时生成，并写入配置。']
  ];
  var en = {}, zh = {};
  WORDS.forEach(function (w) { en[w[0]] = w[1]; zh[w[0]] = w[2]; });
  var I18N = { 'en': en, 'zh-CN': zh };

  WB.module({
    id: 'tunnel.hub', group: 'tunnel', order: 1, icon: 'hub', label: 'mod.label', i18n: I18N,
    nav: [
      { id: 'overview', icon: 'dashboard', label: 'nav.overview' }, { id: 'nodes', icon: 'pod', label: 'nav.nodes' },
      { id: 'proxies', icon: 'proxy', label: 'nav.proxies' }, { id: 'tunnels', icon: 'tunnel', label: 'nav.tunnels' },
      { id: 'listeners', icon: 'listen', label: 'nav.listeners' }, { id: 'settings', icon: 'settings', label: 'nav.settings' }
    ],
    setup: function (UI, sh) {
      var h = UI.h, t = UI.t, K = TunnelKit.with(UI);
      var state = { peers: [], tunnels: [], listeners: [] };

      // ---- State / 状态 ----
      function nodes() { return state.peers.filter(function (p) { return p.info.type === 'node'; }); }
      function proxies() { return state.peers.filter(function (p) { return p.info.type === 'proxy'; }); }
      function peerOf(type, name) { return state.peers.filter(function (p) { return p.info.type === type && p.info.name === name; })[0]; }
      function serviceRTT(p, name) { return p && p.service_rtt && p.service_rtt[name]; }
      function rttOf(type, name) { var p = peerOf(type, name); return p && p.rtt; }

      // poll loads the status now and every 3s, calling draw with it; flow, when given, is refreshed too.
      // poll 立即并每 3 秒加载一次状态，用它调用 draw；给出 flow 时一并刷新。
      function poll(draw, flow) {
        function load() {
          return UI.api('GET', '/api/status').then(function (st) {
            state = st; state.peers = st.peers || []; state.tunnels = st.tunnels || []; state.listeners = st.listeners || [];
            draw();
            if (flow) flow.refresh();
          }).catch(UI.fail);
        }
        load();
        UI.every(3000, load);
        return load;
      }

      // ---- Cells / 单元格 ----
      function nameCell(p) { return h('b', p.info.name); }
      function addrCell(p) {
        return h('div', h('span.mono', p.addr), h('div.small.muted', { title: (p.places || []).map(function (i) { return '#' + i; }).join(' ') }, t('tk.links_n', { n: p.conns })));
      }
      function hostCell(p) { return h('div', p.info.hostname || '—', h('div.small.muted', [p.info.os, p.info.version].filter(Boolean).join(' '))); }
      // pathCell is a tunnel's latency: proxy to hub to node, and the node's connects to the service, the parts in
      // the tooltip. / 隧道的延迟：proxy 到 hub 到节点，加上节点到服务的建连，各段在提示中。
      function pathCell(tn) {
        var c = rttOf('proxy', tn.proxy), p = rttOf('node', tn.node), n = serviceRTT(peerOf('node', tn.node), tn.service), sum = K.rttSum(c, p);
        if (!sum) return K.rtt(null);
        return K.rtt(n && n.samples ? K.rttSum(c, p, n) : sum, tn.proxy + ' ↔ hub ' + K.rttText(c) + ' · hub ↔ ' + tn.node + ' ' + K.rttText(p) +
          (n && n.samples ? ' · ' + tn.node + ' → ' + tn.service + ' ' + K.rttText(n) : ''));
      }

      // ---- Actions / 操作 ----
      function kick(p, reload) {
        UI.confirm(t('tk.kick_q', { name: p.info.name }), t('tk.kick'), true).then(function (ok) {
          if (ok) K.call('/api/peers/kick', { id: p.id }, t('tk.kicked')).then(reload);
        });
      }
      function closeTunnel(tn, reload) {
        K.call('/api/tunnels/close', { id: tn.id }, t('tk.tunnel_closed')).then(reload);
      }
      function editServices(p, reload) {
        K.servicesModal(t('tk.services_of', { name: p.info.name }), p.info.services || [], function (list) {
          return K.call('/api/peers/services', { id: p.id, services: list }, t('tk.services_saved_node')).then(function (ok) { if (ok) reload(); return ok; });
        });
      }

      // ---- Tunnel diagrams / 隧道图 ----
      function peerFacts(p) {
        return p ? [[t('tk.address'), p.addr], [t('tk.latency'), K.rttText(p.rtt)], [t('tk.host'), p.info.hostname], [t('tk.os'), p.info.os],
          [t('tk.version'), p.info.version], [t('tk.peer_id'), p.id], [t('tk.since'), K.dur(p.since)]] : [];
      }
      function tunnelOf(tn) { return { id: tn.id, name: tn.proxy + ' → ' + tn.node + '/' + tn.service, up: tn.up, down: tn.down, created: tn.created }; }
      function flowMe(g, col) {
        var st = state;
        return g.node('me', col, { kind: 'hub', label: st.name, sub: (st.listen || [])[0] || '', self: true, state: 'ok',
          facts: [[t('peer_addrs'), (st.listen || []).join(' ')], [t('socket'), st.socket || ''], [t('tk.body_enc'), st.body_encrypted ? 'AES-256-GCM' : t('tk.none')],
            [t('tk.version'), st.version], [t('tk.uptime'), K.dur(st.started)]] });
      }
      function flowPeer(g, type, col, name) {
        var p = peerOf(type, name);
        return g.node((type === 'node' ? 'p:' : 'c:') + name, col, { kind: type === 'node' ? 'pod' : 'proxy', label: name,
          sub: p ? p.info.hostname || p.addr : '', state: p ? 'ok' : 'wait', facts: peerFacts(p) });
      }
      function flowService(g, col, node, name) {
        var p = peerOf('node', node), sv = (p && (p.info.services || []).filter(function (x) { return x.name === name; })[0]) || { name: name };
        return g.node('n:' + node + '/' + name, col, { kind: 'node', icon: K.nodeIcon(name), label: name, sub: sv.address || '',
          facts: [[t('tk.node'), node], [t('tk.address'), sv.address], [t('tk.desc'), sv.description], [t('tk.connect'), K.rttText(serviceRTT(p, name))]] });
      }
      function flowAll() {
        var g = K.graph([t('tk.proxies'), t('tk.hub'), t('tk.nodes'), t('tk.services')]);
        flowMe(g, 1);
        proxies().forEach(function (p) { flowPeer(g, 'proxy', 0, p.info.name); g.link('c:' + p.info.name, 'me', { rtt: p.rtt }); });
        nodes().forEach(function (p) {
          flowPeer(g, 'node', 2, p.info.name); g.link('me', 'p:' + p.info.name, { rtt: p.rtt });
          (p.info.services || []).forEach(function (sv) {
            flowService(g, 3, p.info.name, sv.name);
            g.link('p:' + p.info.name, 'n:' + p.info.name + '/' + sv.name, { rtt: serviceRTT(p, sv.name) });
          });
        });
        state.tunnels.forEach(function (tn) {
          flowPeer(g, 'proxy', 0, tn.proxy); flowPeer(g, 'node', 2, tn.node); flowService(g, 3, tn.node, tn.service);
          g.tunnel(tunnelOf(tn), ['c:' + tn.proxy, 'me', 'p:' + tn.node, 'n:' + tn.node + '/' + tn.service]);
        });
        return g;
      }
      function flowOne(id) {
        var tn = state.tunnels.filter(function (x) { return x.id === id; })[0];
        if (!tn) return null;
        var g = K.graph([t('tk.local_app'), t('tk.proxy'), t('tk.hub'), t('tk.node'), t('tk.service')]), enc = state.body_encrypted;
        g.node('app', 0, { kind: 'app', label: t('tk.local_app'), sub: t('tk.not_visible'), unknown: true, note: t('tk.only_proxy') });
        flowPeer(g, 'proxy', 1, tn.proxy); flowMe(g, 2); flowPeer(g, 'node', 3, tn.node); flowService(g, 4, tn.node, tn.service);
        g.tunnel(tunnelOf(tn), ['app', 'c:' + tn.proxy, 'me', 'p:' + tn.node, 'n:' + tn.node + '/' + tn.service], [
          { label: 'TCP' }, { label: t('tk.link'), lock: enc, rtt: rttOf('proxy', tn.proxy) }, { label: t('tk.link'), lock: enc, rtt: rttOf('node', tn.node) },
          { label: 'TCP', rtt: serviceRTT(peerOf('node', tn.node), tn.service) }]);
        return g;
      }
      function newFlow() { return K.flow({ all: flowAll, one: flowOne }); }
      function mapButton(flow) { return UI.btn(t('tk.map'), 'route', function () { flow.open(); }, 'sm'); }

      // ---- Overview / 概览 ----
      function overview(el) {
        var flow = newFlow();
        UI.mount(sh.tools, mapButton(flow));
        var stats = h('div.grid.grid--4'), info = h('div'), lsBox = h('div'), tunnels = h('div');
        UI.mount(el, h('div.stack', stats, h('div.grid.grid--2', UI.card(t('ov.hub'), info), UI.card(t('nav.listeners'), lsBox, null, true)),
          UI.card(t('nav.tunnels'), tunnels, null, true)));
        poll(function () {
          var st = state, ns = nodes(), ps = proxies(), ls = st.listeners;
          var svc = ns.reduce(function (a, p) { return a + (p.info.services || []).length; }, 0);
          var busy = ps.filter(function (p) { return p.tunnels > 0; }).length;
          var up = st.tunnels.reduce(function (a, x) { return a + x.up; }, 0), down = st.tunnels.reduce(function (a, x) { return a + x.down; }, 0);
          var bad = ls.filter(function (l) { return l.error; }).length;
          UI.mount(stats,
            UI.stat('pod', t('nav.nodes'), ns.length, t('ov.nodes_sub', { n: svc }), ns.length ? 'ok' : ''),
            UI.stat('proxy', t('nav.proxies'), ps.length, t('ov.proxies_sub', { n: busy }), 'info'),
            UI.stat('tunnel', t('nav.tunnels'), st.tunnels.length, t('ov.tunnels_sub', { up: K.bytes(up), down: K.bytes(down) })),
            UI.stat('listen', t('nav.listeners'), ls.length, bad ? t('ov.listeners_sub', { n: bad }) : t('ov.listeners_ok'), bad || !ls.length ? 'warn' : 'ok'));
          UI.mount(info, UI.kv([
            [t('tk.name'), h('b', st.name)], [t('tk.version'), st.version],
            [t('peer_addrs'), (st.listen || []).length ? h('div', st.listen.map(function (a) { return h('div.mono', a); })) : t('tk.none')],
            [t('socket'), st.socket ? h('span.mono', st.socket) : t('tk.none')],
            [t('tk.body_enc'), st.body_encrypted ? UI.badge('AES-256-GCM', 'ok') : t('tk.none')],
            [t('ov.links'), st.links], [t('tk.uptime'), K.dur(st.started)]]));
          UI.mount(lsBox, UI.table([
            { label: t('tk.protocol'), render: function (l) { return K.protoBadge(l.protocol); } },
            { label: t('peers_dial'), render: function (l) { return l.error ? h('span', UI.badge(t('tk.st.failed'), 'danger', true), h('div.small', l.error)) : h('span.mono', l.url); } }
          ], ls, { empty: t('no.listeners'), onRowClick: function () { location.hash = '#/tunnel.hub/listeners'; } }));
          UI.mount(tunnels, tunnelTable(st.tunnels.slice(-10), flow, null));
        }, flow);
      }

      // ---- Nodes / 节点 ----
      function nodesPage(el) {
        var box = h('div'), load;
        UI.mount(el, UI.card(t('nav.nodes'), box, null, true));
        load = poll(function () {
          UI.mount(box, UI.table([
            { label: t('tk.id'), render: function (p) { return p.id; } },
            { label: t('tk.name'), render: nameCell },
            { label: t('tk.address'), render: addrCell },
            { label: t('tk.latency'), render: function (p) { return K.rtt(p.rtt); } },
            { label: t('tk.host'), render: hostCell },
            { label: t('tk.services'), render: function (p) {
              var ss = p.info.services || [];
              if (!ss.length) return h('span.muted', t('tk.none'));
              return h('div.stack', { style: { gap: '4px' } }, ss.map(function (sv) {
                var r = serviceRTT(p, sv.name);
                return h('div.small', h('b', sv.name), ' → ', h('span.mono', sv.address), sv.description ? h('span.muted', ' ' + sv.description) : null,
                  r && r.samples ? [' · ', K.rtt(r)] : null);
              }));
            } },
            { label: t('tk.tunnels'), cls: 'num', render: function (p) { return p.tunnels; } },
            { label: t('tk.since'), render: function (p) { return K.dur(p.since); } },
            { label: '', cls: 'actions', render: function (p) {
              var q = UI.btn(t('tk.query'), 'refresh', function () {
                UI.busy(q, K.call('/api/peers/refresh', { id: p.id }, t('node_queried'))).then(load);
              }, 'ghost sm');
              return h('div.btn-row', { style: { justifyContent: 'flex-end' } }, q,
                UI.btn(t('tk.edit_services'), 'edit', function () { editServices(p, load); }, 'ghost sm'),
                UI.btn(t('tk.kick'), 'x', function () { kick(p, load); }, 'danger sm'));
            } }
          ], nodes(), { empty: t('no.nodes') }));
        });
      }

      // ---- Proxies / proxy ----
      function proxiesPage(el) {
        var box = h('div'), load;
        UI.mount(el, UI.card(t('nav.proxies'), box, null, true));
        load = poll(function () {
          UI.mount(box, UI.table([
            { label: t('tk.id'), render: function (p) { return p.id; } },
            { label: t('tk.name'), render: nameCell },
            { label: t('tk.address'), render: addrCell },
            { label: t('tk.latency'), render: function (p) { return K.rtt(p.rtt); } },
            { label: t('tk.host'), render: hostCell },
            { label: t('tk.info'), render: function (p) {
              var m = p.info.meta || {}, ks = Object.keys(m);
              return ks.length ? h('div', ks.map(function (k) { return h('div.small.mono', k + '=' + m[k]); })) : h('span.muted', '—');
            } },
            { label: t('tk.tunnels'), cls: 'num', render: function (p) { return p.tunnels; } },
            { label: t('tk.since'), render: function (p) { return K.dur(p.since); } },
            { label: '', cls: 'actions', render: function (p) { return UI.btn(t('tk.kick'), 'x', function () { kick(p, load); }, 'danger sm'); } }
          ], proxies(), { empty: t('no.proxies') }));
        });
      }

      // ---- Tunnels / 隧道 ----
      function tunnelTable(list, flow, reload) {
        return UI.table([
          { label: t('tk.id'), render: function (x) { return h('span.mono.small', x.id); } },
          { label: t('tk.proxy'), render: function (x) { return x.proxy; } },
          { label: t('tk.node'), render: function (x) { return x.node; } },
          { label: t('tk.service'), render: function (x) { return h('b', x.service); } },
          reload ? { label: t('tk.address'), render: function (x) { return h('span.mono', x.addr); } } : null,
          { label: t('tk.latency'), render: pathCell },
          { label: t('tk.up'), cls: 'num', render: function (x) { return K.bytes(x.up); } },
          { label: t('tk.down'), cls: 'num', render: function (x) { return K.bytes(x.down); } },
          { label: t('tk.age'), render: function (x) { return K.dur(x.created); } },
          reload ? { label: '', cls: 'actions', render: function (x) {
            return h('div.btn-row', { style: { justifyContent: 'flex-end' } },
              UI.btn(t('tk.details'), 'route', function () { flow.open(x.id); }, 'ghost sm'),
              UI.btn(t('tk.close'), 'x', function () { closeTunnel(x, reload); }, 'danger sm'));
          } } : null
        ].filter(Boolean), list, { empty: t('no.tunnels'), onRowClick: reload ? null : function (x) { flow.open(x.id); } });
      }
      function tunnelsPage(el) {
        var flow = newFlow(), box = h('div'), load;
        UI.mount(el, UI.card(t('nav.tunnels'), box, mapButton(flow), true));
        load = poll(function () { UI.mount(box, tunnelTable(state.tunnels, flow, function () { load(); })); }, flow);
      }

      // ---- Listeners / 监听 ----
      function listenerKey(l) { return { protocol: l.protocol, address: l.address, path: l.path || '' }; }
      function editListener(l, reload) {
        var proto = h('select.select', ['wss', 'tls', 'quic', 'ws', 'tcp'].map(function (p) { return h('option', { value: p, selected: l ? l.protocol === p : p === 'wss' }, K.protoName(p)); }));
        var addr = h('input.input.mono', { value: l ? l.address : '', placeholder: ':7000', 'data-fill': true });
        var path = h('input.input.mono', { value: l ? l.path || '' : '', placeholder: '/tunnel', 'data-fill': true });
        var pathRow = UI.field(t('path'), path);
        function sync() { pathRow.hidden = proto.value !== 'ws' && proto.value !== 'wss'; }
        proto.addEventListener('change', sync);
        sync();
        UI.modal({
          title: l ? t('edit_listener') : t('add_listener'),
          body: h('div.form', h('div.form-row', UI.field(t('tk.protocol'), proto), UI.field(t('tk.addr_hp'), addr)), pathRow,
            h('div.small.muted', t('listeners_tls'))),
          actions: [{ label: t('cancel') }, {
            label: t('save'), kind: 'primary', onclick: function () {
              if (!addr.value.trim()) { addr.focus(); return false; }
              var nl = { protocol: proto.value, address: addr.value.trim(), path: path.value.trim() };
              var p = l ? K.call('/api/listeners/update', { old: listenerKey(l), new: nl }, t('listener_saved')) : K.call('/api/listeners', nl, t('listener_saved'));
              return K.keepOpen(p.then(function (ok) { if (ok) setTimeout(reload, 200); return ok; }));
            }
          }]
        });
      }
      function delListener(l, reload) {
        UI.confirm(t('remove_listener_q', { addr: l.url || l.address }), t('tk.remove'), true).then(function (ok) {
          if (ok) K.call('/api/listeners/delete', listenerKey(l), t('listener_removed')).then(function () { setTimeout(reload, 200); });
        });
      }
      function listenersPage(el) {
        var box = h('div'), load;
        var add = UI.btn(t('add_listener'), 'plus', function () { editListener(null, load); }, 'primary sm');
        UI.mount(el, h('div.stack', K.configNote('hub'), UI.card(t('nav.listeners'), box, add, true), h('div.callout', UI.icon('info'), ' ', t('listeners_note'), ' ', t('listeners_tls'))));
        load = poll(function () {
          UI.mount(box, UI.table([
            { label: t('tk.protocol'), render: function (l) { return K.protoBadge(l.protocol); } },
            { label: t('tk.address'), render: function (l) { return h('span.mono', l.address); } },
            { label: t('path'), render: function (l) { return l.path ? h('span.mono', l.path) : ''; } },
            { label: t('peers_dial'), render: function (l) { return l.error ? h('span', UI.badge(t('tk.st.failed'), 'danger', true), h('div.small', l.error)) : h('span.mono', l.url); } },
            { label: t('quic.fp'), render: function (l) { return l.fingerprint ? h('span', l.own_cert ? UI.badge(t('own_cert'), 'info') : null, h('div.small.mono', { style: { overflowWrap: 'anywhere' } }, l.fingerprint)) : ''; } },
            { label: '', cls: 'actions', render: function (l) {
              return h('div.btn-row', { style: { justifyContent: 'flex-end' } },
                UI.btn(t('tk.edit'), 'edit', function () { editListener(l, load); }, 'ghost sm'),
                UI.btn(t('tk.remove'), 'trash', function () { delListener(l, load); }, 'danger sm'));
            } }
          ], state.listeners, { empty: t('no.listeners') }));
        });
      }

      // ---- Settings / 设置 ----
      function settingsPage(el) {
        var form = h('div'), quic = h('div');
        UI.mount(el, h('div.stack', K.configNote('hub'), h('div.grid.grid--2', UI.card(t('set.title'), form), UI.card(t('quic.title'), quic))));
        function load() {
          return UI.api('GET', '/api/status').then(function (st) { draw(st); }).catch(UI.fail);
        }
        function draw(st) {
          var set = st.settings || {};
          var f = {
            password: h('input.input', { type: 'password', autocomplete: 'new-password', placeholder: set.has_password ? t('tk.keep') : t('set.pw_unset') }),
            body: h('input.input', { type: 'password', autocomplete: 'new-password', placeholder: set.has_body_password ? t('tk.keep') : t('tk.optional') }),
            clearBody: h('input', { type: 'checkbox' }),
            socket: h('input.input', { type: 'password', autocomplete: 'new-password', placeholder: set.has_socket_password ? t('tk.keep') : t('set.socket_default') }),
            clearSocket: h('input', { type: 'checkbox' }),
            socketAddr: h('input.input.mono', { value: set.socket_address || '', placeholder: t('set.socket_ph') })
          };
          var save = h('button.btn.btn--primary', { type: 'submit' }, UI.icon('save'), t('set.save'));
          UI.mount(form, h('form.form', {
            onsubmit: function (e) {
              e.preventDefault();
              UI.busy(save, K.call('/api/settings', { password: f.password.value, body_password: f.body.value, clear_body_password: f.clearBody.checked,
                socket_password: f.socket.value, clear_socket_password: f.clearSocket.checked, socket_address: f.socketAddr.value.trim() }, t('set.saved')))
                .then(function (ok) { if (ok) setTimeout(load, 300); });
            }
          },
          UI.field(t('set.password'), f.password),
          UI.field(t('set.body'), f.body),
          set.has_body_password ? h('label.check', f.clearBody, ' ', t('set.clear_body')) : null,
          UI.field(t('set.socket_pw'), f.socket),
          set.has_socket_password ? h('label.check', f.clearSocket, ' ', t('set.clear_socket')) : null,
          UI.field(t('set.socket_addr'), f.socketAddr),
          h('div.small.muted', t('set.note')),
          h('div.btn-row', save)));
          var fp = st.fingerprint || st.quic_fingerprint;
          if (fp) {
            UI.mount(quic, h('div.stack',
              UI.field(t('quic.fp'), h('div.row', { style: { flexWrap: 'nowrap' } }, h('code', { style: { overflowWrap: 'anywhere' } }, fp),
                UI.btn(t('copy'), 'copy', function () { UI.copy(fp); }, 'sm'))),
              h('div.small.muted', t('quic.note'))));
          } else UI.mount(quic, UI.empty(t('quic.none'), 'lock'));
        }
        load();
      }

      return {
        fallback: 'overview',
        routes: {
          'overview': function () { overview(sh.page('overview', t('nav.overview'))); },
          'nodes': function () { nodesPage(sh.page('nodes', t('nav.nodes'))); },
          'proxies': function () { proxiesPage(sh.page('proxies', t('nav.proxies'))); },
          'tunnels': function () { tunnelsPage(sh.page('tunnels', t('nav.tunnels'))); },
          'listeners': function () { listenersPage(sh.page('listeners', t('nav.listeners'))); },
          'settings': function () { settingsPage(sh.page('settings', t('nav.settings'))); }
        }
      };
    }
  });
})();
