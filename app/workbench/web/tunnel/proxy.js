// The tunnel proxy's module: overview, the hubs it links to (each with the console its web proxy serves), the nodes
// and services they have, its forwards, its tunnels (with the tunnel map and each tunnel's path) and the info it
// tells the hubs. What it changes is written into tunnel.proxy in fit's config. Needs ui.js and tunnel/shared.js.
//
// 隧道 proxy 模块：概览、所连的 hub（各带其 Web 代理所提供的控制台）、hub 上的节点与服务、转发、隧道（含隧道全景图与
// 每条隧道的链路）以及告知 hub 的信息。所做修改写入 fit 配置的 tunnel.proxy。依赖 ui.js 与 tunnel/shared.js。
(function () {
  'use strict';
  // [key, en, zh] / [键, 英文, 中文]
  var WORDS = [
    ['mod.label', 'Proxy', 'proxy'],
    ['nav.overview', 'Overview', '概览'], ['nav.hubs', 'Hubs', 'hub'], ['nav.nodes', 'Nodes and services', '节点与服务'],
    ['nav.forwards', 'Forwards', '转发'], ['nav.tunnels', 'Tunnels', '隧道'], ['nav.info', 'Proxy info', 'proxy 信息'],
    ['ov.proxy', 'This proxy', '本 proxy'], ['ov.hubs_sub', '{n} registered', '{n} 个已注册'], ['ov.nodes_sub', '{n} services', '{n} 个服务'],
    ['ov.forwards_sub', '{n} failing', '{n} 个失败'], ['ov.forwards_ok', 'all listening', '全部监听中'],
    ['ov.tunnels_sub', 'sent {out} · received {in}', '发送 {out} · 接收 {in}'],
    ['query_nodes', 'Query nodes', '查询节点'], ['nodes_queried', 'Nodes queried', '已查询节点'],
    ['forward', 'Forward…', '转发…'], ['add_forward', 'Add forward', '添加转发'], ['forward_btn', 'Forward', '转发'],
    ['forward_added', 'Forward added', '转发已添加'], ['forward_removed', 'Forward removed', '转发已移除'],
    ['remove_forward_q', 'Stop forwarding {listen}?', '停止转发 {listen}？'],
    ['f.listen', 'Local address', '本地地址'], ['f.listen_ph', '127.0.0.1:2222', '127.0.0.1:2222'],
    ['f.hub_ph', 'hub (optional: any)', 'hub（可选，默认任意）'],
    ['f.note', 'The local address takes connections and opens a tunnel to the node\'s service for each, through the hub named or any hub that has the node.',
      '本地地址接受连接，并为每个连接经指定的 hub（或任一有该节点的 hub）打开到节点服务的隧道。'],
    ['web.address', 'Address', '地址'], ['web.via', 'Via', '经由'], ['web.conns', 'Connections', '连接数'],
    ['meta.title', 'Info told to hubs', '告知 hub 的信息'],
    ['meta.key', 'Key', '键'], ['meta.value', 'Value', '值'], ['meta.add', 'Add', '添加'],
    ['meta.save', 'Update hubs', '更新到 hub'], ['meta.saved', 'Info sent to hubs', '信息已发送到 hub'],
    ['meta.note', 'The hubs show these pairs on their Proxies page.', 'hub 在其 “proxy” 页上显示这些键值。'],
    ['no.hubs', 'No hubs: add the hub this proxy links to.', '没有 hub：添加本 proxy 要连接的 hub。'],
    ['no.nodes', 'No nodes: the hubs have none, or are not registered yet.', '没有节点：hub 上没有节点，或尚未注册。'],
    ['no.forwards', 'No forwards: forward a local address to a node\'s service.', '没有转发：把本地地址转发到节点的服务。'],
    ['no.tunnels', 'No open tunnels.', '暂无打开的隧道。']
  ];
  var en = {}, zh = {};
  WORDS.forEach(function (w) { en[w[0]] = w[1]; zh[w[0]] = w[2]; });
  var I18N = { 'en': en, 'zh-CN': zh };

  WB.module({
    id: 'tunnel.proxy', group: 'tunnel', order: 3, icon: 'proxy', label: 'mod.label', i18n: I18N,
    nav: [
      { id: 'overview', icon: 'dashboard', label: 'nav.overview' }, { id: 'hubs', icon: 'hub', label: 'nav.hubs' },
      { id: 'nodes', icon: 'pod', label: 'nav.nodes' }, { id: 'forwards', icon: 'forward', label: 'nav.forwards' },
      { id: 'tunnels', icon: 'tunnel', label: 'nav.tunnels' }, { id: 'info', icon: 'info', label: 'nav.info' }
    ],
    setup: function (UI, sh) {
      var h = UI.h, t = UI.t, K = TunnelKit.with(UI);
      var state = { hubs: [], forwards: [], web_proxies: [], tunnels: [] };

      // poll loads the status now and every 3s, calling draw with it; flow, when given, is refreshed too.
      // poll 立即并每 3 秒加载一次状态，用它调用 draw；给出 flow 时一并刷新。
      function poll(draw, flow) {
        function load() {
          return UI.api('GET', '/api/status').then(function (st) {
            state = st; ['hubs', 'forwards', 'web_proxies', 'tunnels'].forEach(function (k) { state[k] = st[k] || []; });
            state.hubs.forEach(function (x) { x.nodes = x.nodes || []; });
            draw();
            if (flow) flow.refresh();
          }).catch(UI.fail);
        }
        load();
        UI.every(3000, load);
        return load;
      }
      function hubOf(name) { return state.hubs.filter(function (x) { return x.name === name; })[0]; }
      function nodeVia(hub, node) { var x = hubOf(hub); return x && x.nodes.filter(function (p) { return p.name === node; })[0]; }
      function nodeOf(name) {
        for (var i = 0; i < state.hubs.length; i++) {
          var p = state.hubs[i].nodes.filter(function (n) { return n.name === name; })[0];
          if (p) return p;
        }
        return null;
      }
      function serviceRTT(p, name) { return p && p.service_rtt && p.service_rtt[name]; }
      function transport(a) { return t('tk.link_over', { p: K.protoName(K.protoOf(a)) }); }

      // pathRTT is the latency to node p through hub x: proxy to hub to node, the parts in the tooltip.
      // 经 hub x 到节点 p 的延迟：proxy 到 hub 到节点，各段在提示中。
      function pathRTT(x, p) {
        var sum = K.rttSum(x.rtt, p.rtt);
        return sum ? K.rtt(sum, 'proxy ↔ ' + x.name + ' ' + K.rttText(x.rtt) + ' · ' + x.name + ' ↔ ' + p.name + ' ' + K.rttText(p.rtt)) : K.rtt(null);
      }
      // tunnelRTT is a tunnel's latency: its path, and the node's connects to the service.
      // 隧道的延迟：其路径，加上节点到服务的建连。
      function tunnelRTT(tn) {
        var x = hubOf(tn.hub), p = x && nodeVia(tn.hub, tn.node), n = serviceRTT(p, tn.service);
        if (!p) return K.rtt(null);
        var sum = K.rttSum(x.rtt, p.rtt);
        if (!sum) return K.rtt(null);
        return K.rtt(n && n.samples ? K.rttSum(x.rtt, p.rtt, n) : sum, 'proxy ↔ ' + x.name + ' ' + K.rttText(x.rtt) + ' · ' + x.name + ' ↔ ' + tn.node + ' ' +
          K.rttText(p.rtt) + (n && n.samples ? ' · ' + tn.node + ' → ' + tn.service + ' ' + K.rttText(n) : ''));
      }
      // proxyOf is the web proxy that serves hub x's console: the one named after x, or else the one whose URL is on
      // x's host. / 为 hub x 的控制台服务的 Web 代理：与 x 同名的，否则 URL 在 x 主机上的。
      function proxyOf(x) {
        var ws = state.web_proxies;
        return ws.filter(function (w) { return w.name === x.name; })[0] ||
          ws.filter(function (w) { return !w.name && K.hostOf(w.url) && K.hostOf(w.url) === K.hostOf(x.addr); })[0];
      }

      // ---- Actions / 操作 ----
      function editServices(hub, node, reload) {
        var p = nodeVia(hub, node);
        if (!p) return;
        K.servicesModal(t('tk.services_via', { name: node, hub: hub }), p.services || [], function (list) {
          return K.call('/api/nodes/services', { hub: hub, node: node, services: list }, t('tk.services_saved_node')).then(function (ok) { if (ok) setTimeout(reload, 300); return ok; });
        });
      }
      // addForward asks for a forward, f filling the form. / 弹窗添加转发，f 预填表单。
      function addForward(f, reload) {
        f = f || {};
        var listen = h('input.input.mono', { placeholder: t('f.listen_ph') }), node = h('input.input', { value: f.node || '', placeholder: t('tk.node').toLowerCase() });
        var service = h('input.input', { value: f.service || '', placeholder: t('tk.service').toLowerCase() }), hub = h('input.input', { value: f.hub || '', placeholder: t('f.hub_ph') });
        UI.modal({
          title: t('add_forward'),
          body: h('div.form', UI.field(t('f.listen'), listen), h('div.form-row', UI.field(t('tk.node'), node), UI.field(t('tk.service'), service)),
            UI.field(t('tk.hub'), hub), h('div.small.muted', t('f.note'))),
          actions: [{ label: t('cancel') }, {
            label: t('forward_btn'), kind: 'primary', onclick: function () {
              var req = { listen: listen.value.trim(), node: node.value.trim(), service: service.value.trim(), hub: hub.value.trim() };
              if (!req.listen) { listen.focus(); return false; }
              if (!req.node) { node.focus(); return false; }
              if (!req.service) { service.focus(); return false; }
              return K.keepOpen(K.call('/api/forwards', req, t('forward_added')).then(function (ok) { if (ok && reload) setTimeout(reload, 300); return ok; }));
            }
          }]
        });
      }
      function delForward(f, reload) {
        UI.confirm(t('remove_forward_q', { listen: f.addr || f.listen }), t('tk.remove'), true).then(function (ok) {
          if (ok) K.call('/api/forwards/delete', { listen: f.listen }, t('forward_removed')).then(function () { setTimeout(reload, 300); });
        });
      }

      // ---- Tunnel diagrams: this proxy, its forwards, hubs, nodes and their services. A proxy's tunnel end sends the
      // local app's bytes (out) up. / 隧道图：本 proxy、转发、hub、节点及其服务。proxy 端把本地应用的字节（out）向上发送。
      function tunnelOf(tn) { return { id: tn.id, name: tn.peer + ' → ' + tn.node + '/' + tn.service, up: tn.out, down: tn.in, created: tn.created }; }
      function flowMe(g, col) {
        var st = state;
        return g.node('me', col, { kind: 'proxy', label: st.name, sub: t('tk.n_forwards', { n: st.forwards.length }), self: true, state: 'ok',
          facts: [[t('tk.hubs'), st.hubs.length], [t('nav.forwards'), st.forwards.length], [t('tk.version'), st.version]] });
      }
      function flowForward(g, col, listen) {
        var f = state.forwards.filter(function (x) { return x.listen === listen; })[0] || { listen: listen };
        return g.node('f:' + listen, col, { kind: 'forward', label: f.addr || listen, sub: f.node ? '→ ' + f.node + '/' + f.service : '', state: f.error ? 'bad' : 'ok',
          facts: [[t('tk.listen'), f.addr || listen], [t('tk.node'), f.node], [t('tk.service'), f.service], [t('tk.hub'), f.hub || t('tk.any')],
            [t('tk.state'), f.error || t('tk.listening')]] });
      }
      function flowHub(g, col, name) {
        var x = hubOf(name);
        return g.node('s:' + name, col, { kind: 'hub', label: name, sub: x ? [K.rttText(x.rtt), x.addr].filter(Boolean).join(' · ') : '', state: x ? K.flowState(x.state) : 'wait',
          facts: x ? [[t('tk.address'), x.addr], [t('tk.state'), x.error || t('tk.st.' + x.state)], [t('tk.latency'), K.rttText(x.rtt)], [t('tk.protocol'), transport(x.addr)],
            [t('tk.body_enc'), x.body_encrypted ? 'AES-256-GCM' : t('tk.none')], [t('tk.peer_id'), x.peer_id || ''], [t('tk.links'), x.pool + '/' + x.pool_size],
            [t('tk.tunnels'), x.tunnels]] : [] });
      }
      function flowNode(g, col, name) {
        var p = nodeOf(name);
        return g.node('p:' + name, col, { kind: 'pod', label: name, sub: p ? p.hostname : '', state: p ? 'ok' : 'wait',
          facts: p ? [[t('tk.address'), p.addr], [t('tk.host'), p.hostname], [t('tk.os'), p.os], [t('tk.version'), p.version]] : [] });
      }
      function flowService(g, col, node, name) {
        var p = nodeOf(node), n = (p && (p.services || []).filter(function (x) { return x.name === name; })[0]) || { name: name };
        return g.node('n:' + node + '/' + name, col, { kind: 'node', icon: K.nodeIcon(name), label: name, sub: n.address || '',
          facts: [[t('tk.node'), node], [t('tk.address'), n.address], [t('tk.desc'), n.description], [t('tk.connect'), K.rttText(serviceRTT(p, name))]] });
      }
      function flowAll() {
        var st = state, g = K.graph([t('tk.proxy'), t('nav.forwards'), t('tk.hubs'), t('tk.nodes'), t('tk.services')]);
        flowMe(g, 0);
        st.forwards.forEach(function (f) { flowForward(g, 1, f.listen); g.link('me', 'f:' + f.listen); });
        st.hubs.forEach(function (x) {
          flowHub(g, 2, x.name);
          x.nodes.forEach(function (p) {
            flowNode(g, 3, p.name); g.link('s:' + x.name, 'p:' + p.name, { rtt: p.rtt });
            (p.services || []).forEach(function (n) { flowService(g, 4, p.name, n.name); g.link('p:' + p.name, 'n:' + p.name + '/' + n.name, { rtt: serviceRTT(p, n.name) }); });
          });
        });
        // A forward may take any hub that has its node. The latency to a hub is on the hub, rather than on each of the
        // links into it. / 转发可经任一有其节点的 hub。到 hub 的延迟标在 hub 上，而非每条进入它的连接上。
        st.forwards.forEach(function (f) {
          st.hubs.forEach(function (x) {
            if ((!f.hub || f.hub === x.name) && x.nodes.some(function (p) { return p.name === f.node; })) g.link('f:' + f.listen, 's:' + x.name, { rtt: x.rtt, quiet: true });
          });
        });
        st.tunnels.forEach(function (tn) {
          flowForward(g, 1, tn.peer); flowHub(g, 2, tn.hub); flowNode(g, 3, tn.node); flowService(g, 4, tn.node, tn.service);
          g.tunnel(tunnelOf(tn), ['me', 'f:' + tn.peer, 's:' + tn.hub, 'p:' + tn.node, 'n:' + tn.node + '/' + tn.service]);
        });
        return g;
      }
      function flowOne(id) {
        var st = state, tn = st.tunnels.filter(function (x) { return x.id === id; })[0];
        if (!tn) return null;
        var g = K.graph([t('tk.local_app'), t('tk.proxy'), t('tk.hub'), t('tk.node'), t('tk.service')]), x = hubOf(tn.hub) || {}, enc = x.body_encrypted;
        g.node('app', 0, { kind: 'app', label: t('tk.local_app'), sub: tn.remote, state: 'ok', facts: [[t('tk.address'), tn.remote]] });
        var me = flowMe(g, 1);
        me.facts.unshift([t('tk.listen'), tn.local || tn.peer]); me.sub = tn.local || tn.peer;
        flowHub(g, 2, tn.hub); flowNode(g, 3, tn.node); flowService(g, 4, tn.node, tn.service);
        var p = nodeVia(tn.hub, tn.node) || {};
        g.tunnel(tunnelOf(tn), ['app', 'me', 's:' + tn.hub, 'p:' + tn.node, 'n:' + tn.node + '/' + tn.service], [
          { label: 'TCP' }, { label: transport(x.addr || ''), lock: enc, rtt: x.rtt }, { label: t('tk.link'), lock: enc, rtt: p.rtt },
          { label: 'TCP', rtt: serviceRTT(p, tn.service) }]);
        return g;
      }
      function newFlow() { return K.flow({ all: flowAll, one: flowOne }); }
      function mapButton(flow) { return UI.btn(t('tk.map'), 'route', function () { flow.open(); }, 'sm'); }

      // ---- Overview / 概览 ----
      function overview(el) {
        var flow = newFlow(), stats = h('div.grid.grid--4'), info = h('div'), fw = h('div');
        UI.mount(sh.tools, mapButton(flow));
        UI.mount(el, h('div.stack', stats, h('div.grid.grid--2', UI.card(t('ov.proxy'), info), UI.card(t('nav.forwards'), fw, null, true))));
        poll(function () {
          var st = state, reg = st.hubs.filter(function (x) { return x.state === 'registered'; }).length;
          var names = {}, svc = 0;
          st.hubs.forEach(function (x) { x.nodes.forEach(function (p) { if (!names[p.name]) { names[p.name] = true; svc += (p.services || []).length; } }); });
          var bad = st.forwards.filter(function (f) { return f.error; }).length;
          var tin = st.tunnels.reduce(function (a, x) { return a + x.in; }, 0), tout = st.tunnels.reduce(function (a, x) { return a + x.out; }, 0);
          UI.mount(stats,
            UI.stat('hub', t('nav.hubs'), reg + ' / ' + st.hubs.length, t('ov.hubs_sub', { n: reg }), st.hubs.length && reg === st.hubs.length ? 'ok' : 'warn'),
            UI.stat('pod', t('tk.nodes'), Object.keys(names).length, t('ov.nodes_sub', { n: svc }), 'info'),
            UI.stat('forward', t('nav.forwards'), st.forwards.length, bad ? t('ov.forwards_sub', { n: bad }) : t('ov.forwards_ok'), bad ? 'warn' : 'ok'),
            UI.stat('tunnel', t('nav.tunnels'), st.tunnels.length, t('ov.tunnels_sub', { out: K.bytes(tout), 'in': K.bytes(tin) })));
          var meta = st.meta || {};
          UI.mount(info, UI.kv([[t('tk.name'), h('b', st.name)], [t('tk.version'), st.version],
            [t('tk.tunnels'), t('tk.open_of', { open: st.open, max: st.max_tunnels })],
            [t('nav.info'), Object.keys(meta).length ? h('div', Object.keys(meta).map(function (k) { return h('div.small.mono', k + '=' + meta[k]); })) : '']]));
          UI.mount(fw, forwardTable(null));
        }, flow);
      }

      // ---- Hubs: a card each, with the console its web proxy serves; a web proxy that is no hub's gets a card of its
      // own. / hub：每个一张卡片，带其 Web 代理所提供的控制台；不属于任何 hub 的 Web 代理单独一张卡片。
      function webBlock(w) {
        if (!w) return h('div.small.muted', t('tk.no_web'));
        var a = w.addr || w.listen;
        if (w.error) return h('div.callout.callout--danger', w.error);
        return h('div.stack', UI.kv([[t('web.address'), h('span.mono', K.pageURL(a))], [t('web.via'), h('span.mono', w.url)], [t('web.conns'), w.conns]]),
          h('div', UI.btn(t('tk.open'), 'globe', function () { K.openPage(a); }, 'primary sm')));
      }
      function hubCard(title, addr, stateEl, facts, w, buttons) {
        return h('section.card',
          h('div.card__head', h('div.row', { style: { flex: '1', minWidth: '0' } }, UI.icon('hub'), h('div', h('h2', title), addr ? h('div.small.muted.mono', addr) : null)), stateEl),
          h('div.card__body.stack', facts.length ? UI.kv(facts) : null, h('h4', t('tk.console')), webBlock(w)),
          buttons ? h('div.card__foot', buttons) : null);
      }
      function hubsPage(el) {
        var box = h('div'), load;
        var add = UI.btn(t('tk.add_hub'), 'plus', function () { K.hubEditor(null, function () { setTimeout(load, 300); }); }, 'primary sm');
        UI.mount(sh.tools, add);
        UI.mount(el, h('div.stack', K.configNote('proxy'), box));
        load = poll(function () {
          var st = state, used = st.hubs.map(proxyOf);
          var lone = st.web_proxies.filter(function (w) { return used.indexOf(w) < 0; });
          var cards = st.hubs.map(function (x) {
            var q = UI.btn(t('query_nodes'), 'refresh', function () {
              UI.busy(q, K.call('/api/nodes/refresh', { hub: x.name }, t('nodes_queried'))).then(function () { load(); });
            }, 'sm');
            return hubCard(x.name, x.addr, h('div', K.stateBadge(x.state, x.error), K.linkProtos(x)), [
              [t('tk.protocols'), h('div', (x.endpoints || []).filter(function (e) { return e.enabled; }).map(function (e, i) { return [i ? ' → ' : '', K.protoBadge(e.protocol)]; }))],
              [t('tk.hub'), x.hub], [t('tk.latency'), K.rtt(x.rtt)], [t('tk.peer_id'), x.peer_id || ''], [t('tk.links'), x.pool + '/' + x.pool_size],
              [t('tk.nodes'), x.nodes.length], [t('tk.tunnels'), x.tunnels], [t('tk.body_enc'), x.body_encrypted ? 'AES-256-GCM' : t('tk.none')]
            ], proxyOf(x), [q,
              UI.btn(t('tk.edit'), 'edit', function () { K.hubEditor(x, function () { setTimeout(load, 300); }); }, 'sm'),
              UI.btn(t('tk.remove'), 'trash', function () { K.delHub(x.name, function () { setTimeout(load, 300); }); }, 'danger sm')]);
          }).concat(lone.map(function (w) { return hubCard(w.name || w.url, '', UI.badge(t('tk.web_only')), [], w, null); }));
          UI.mount(box, cards.length ? h('div.grid.grid--2', cards) : UI.card(null, UI.empty(t('no.hubs'), 'hub')));
        });
      }

      // ---- Nodes and services / 节点与服务 ----
      function nodesPage(el) {
        var box = h('div'), load;
        UI.mount(el, UI.card(t('nav.nodes'), box, null, true));
        load = poll(function () {
          var rows = [];
          state.hubs.forEach(function (x) { x.nodes.forEach(function (p) { rows.push({ hub: x, node: p }); }); });
          UI.mount(box, UI.table([
            { label: t('tk.hub'), render: function (r) { return r.hub.name; } },
            { label: t('tk.node'), render: function (r) { return h('div', h('b', r.node.name), h('div.small.muted', [r.node.hostname, r.node.addr].filter(Boolean).join(' '))); } },
            { label: t('tk.latency'), render: function (r) { return pathRTT(r.hub, r.node); } },
            { label: t('tk.share'), cls: 'num', render: function (r) { return Math.round(r.node.share * 100) + '%'; } },
            { label: t('tk.services'), render: function (r) {
              var ss = r.node.services || [];
              if (!ss.length) return h('span.muted', t('tk.none'));
              return h('div.stack', { style: { gap: '4px' } }, ss.map(function (n) {
                var rt = serviceRTT(r.node, n.name);
                return h('div.row', { style: { gap: '6px' } }, h('span.small', h('b', n.name), ' → ', h('span.mono', n.address),
                  n.description ? h('span.muted', ' ' + n.description) : null, rt && rt.samples ? [' · ', K.rtt(rt)] : null),
                  UI.btn(t('forward'), 'forward', function () { addForward({ hub: r.hub.name, node: r.node.name, service: n.name }, null); }, 'ghost sm'));
              }));
            } },
            { label: '', cls: 'actions', render: function (r) { return UI.btn(t('tk.edit_services'), 'edit', function () { editServices(r.hub.name, r.node.name, load); }, 'ghost sm'); } }
          ], rows, { empty: t('no.nodes') }));
        });
      }

      // ---- Forwards / 转发 ----
      function forwardTable(reload) {
        return UI.table([
          { label: t('tk.listen'), render: function (f) { return h('span.mono', f.addr || f.listen); } },
          { label: t('tk.node'), render: function (f) { return f.node; } },
          { label: t('tk.service'), render: function (f) { return h('b', f.service); } },
          { label: t('tk.hub'), render: function (f) { return f.hub || h('span.muted', t('tk.any')); } },
          { label: t('tk.state'), render: function (f) {
            return f.error ? h('div', UI.badge(t('tk.st.failed'), 'danger', true), h('div.small', f.error)) : UI.badge(t('tk.listening'), 'ok', true);
          } },
          reload ? { label: '', cls: 'actions', render: function (f) {
            return h('div.btn-row', { style: { justifyContent: 'flex-end' } },
              f.error ? null : UI.btn(t('tk.open'), 'globe', function () { K.openPage(f.addr || f.listen); }, 'ghost sm'),
              UI.btn(t('tk.remove'), 'trash', function () { delForward(f, reload); }, 'danger sm'));
          } } : null
        ].filter(Boolean), state.forwards, { empty: t('no.forwards') });
      }
      function forwardsPage(el) {
        var box = h('div'), load;
        var add = UI.btn(t('add_forward'), 'plus', function () { addForward(null, load); }, 'primary sm');
        UI.mount(el, h('div.stack', K.configNote('proxy'), UI.card(t('nav.forwards'), box, add, true)));
        load = poll(function () { UI.mount(box, forwardTable(function () { load(); })); });
      }

      // ---- Tunnels / 隧道 ----
      function tunnelsPage(el) {
        var flow = newFlow(), box = h('div'), load;
        UI.mount(el, UI.card(t('nav.tunnels'), box, mapButton(flow), true));
        load = poll(function () {
          UI.mount(box, UI.table([
            { label: t('tk.hub'), render: function (x) { return x.hub; } },
            { label: t('tk.id'), render: function (x) { return h('span.mono.small', x.id); } },
            { label: t('tk.listen'), render: function (x) { return h('span.mono.small', x.peer); } },
            { label: t('tk.node'), render: function (x) { return x.node; } },
            { label: t('tk.service'), render: function (x) { return h('b', x.service); } },
            { label: t('tk.address'), render: function (x) { return h('span.mono', x.addr); } },
            { label: t('tk.latency'), render: tunnelRTT },
            { label: t('tk.local_proxy'), render: function (x) { return h('span.mono.small', x.remote); } },
            { label: t('tk.sent'), cls: 'num', render: function (x) { return K.bytes(x.out); } },
            { label: t('tk.received'), cls: 'num', render: function (x) { return K.bytes(x.in); } },
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

      // ---- Proxy info: the meta told to the hubs, as key/value rows. / proxy 信息：告知 hub 的元信息，以键值行编辑。
      function infoPage(el) {
        var body = h('tbody'), box = h('div');
        function row(k, v) {
          var tr = h('tr', h('td', h('input.input.input--sm.mono', { value: k || '', placeholder: 'key' })),
            h('td', h('input.input.input--sm', { value: v || '', placeholder: 'value' })),
            h('td.actions', UI.btn('', 'x', function () { tr.remove(); }, 'ghost sm icon')));
          body.appendChild(tr);
        }
        var save = UI.btn(t('meta.save'), 'send', function () {
          var meta = {};
          Array.prototype.forEach.call(body.querySelectorAll('tr'), function (tr) {
            var i = tr.querySelectorAll('input'), k = i[0].value.trim();
            if (k) meta[k] = i[1].value.trim();
          });
          UI.busy(save, K.call('/api/meta', meta, t('meta.saved'))).then(function (ok) { if (ok) setTimeout(load, 300); });
        }, 'primary');
        UI.mount(el, h('div.stack', K.configNote('proxy'), UI.card(t('meta.title'), box)));
        function load() {
          return UI.api('GET', '/api/status').then(function (st) {
            var meta = st.meta || {};
            UI.clear(body);
            Object.keys(meta).forEach(function (k) { row(k, meta[k]); });
            if (!Object.keys(meta).length) row('', '');
            UI.mount(box, h('div.stack', h('div.small.muted', t('meta.note')),
              h('div.table-wrap', h('table.table', h('thead', h('tr', h('th', t('meta.key')), h('th', t('meta.value')), h('th', ''))), body)),
              h('div.btn-row', UI.btn(t('meta.add'), 'plus', function () { row('', ''); }, 'sm'), save)));
          }).catch(UI.fail);
        }
        load();
      }

      return {
        fallback: 'overview',
        routes: {
          'overview': function () { overview(sh.page('overview', t('nav.overview'))); },
          'hubs': function () { hubsPage(sh.page('hubs', t('nav.hubs'))); },
          'nodes': function () { nodesPage(sh.page('nodes', t('nav.nodes'))); },
          'forwards': function () { forwardsPage(sh.page('forwards', t('nav.forwards'))); },
          'tunnels': function () { tunnelsPage(sh.page('tunnels', t('nav.tunnels'))); },
          'info': function () { infoPage(sh.page('info', t('nav.info'))); }
        }
      };
    }
  });
})();
