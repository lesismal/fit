// TunnelKit: what the tunnel's three role modules (hub, node, proxy) share: the formatting of latencies, bytes, ages,
// states and protocols, the editor of a node's services, the editor of a hub a node or proxy links to, the calls that
// change the config, and the tunnel diagrams (the map of every tunnel, and one tunnel's whole path). Needs ui.js.
//
// TunnelKit：隧道三个角色模块（hub、node、proxy）共用的部分：延迟、字节、时长、状态与协议的格式化，节点服务编辑器，node 与
// proxy 所连 hub 的编辑器，修改配置的调用，以及隧道图（全部隧道的全景图与单条隧道的完整链路）。依赖 ui.js。
//
// TunnelKit.bytes, dur, ms, rtt, rttText, rttSum, stateBadge, protoBadge, linkProtos, endpointList, pageURL, openPage,
// graph(cols) are plain helpers; TunnelKit.with(UI) adds, on a module's scoped UI: call(path, body, ok) → Promise of
// whether it worked, services(list, extra) → editor, hubEditor(hub|null, onSaved), flow({all, one}) → {open, refresh},
// configNote(role).
//
// TunnelKit.bytes、dur、ms、rtt、rttText、rttSum、stateBadge、protoBadge、linkProtos、endpointList、pageURL、openPage、
// graph(cols) 为普通辅助函数；TunnelKit.with(UI) 基于模块的 scoped UI 增加：call(path, body, ok) → 是否成功的 Promise、
// services(list, extra) → 编辑器、hubEditor(hub|null, onSaved)、flow({all, one}) → {open, refresh}、configNote(role)。
(function () {
  'use strict';
  var h = UI.h, t = UI.t, NS = 'http://www.w3.org/2000/svg';

  // [key, en, zh] / [键, 英文, 中文]
  var WORDS = [
    ['tk.name', 'Name', '名称'], ['tk.address', 'Address', '地址'], ['tk.addr_hp', 'Address (host:port)', '地址 (host:port)'],
    ['tk.desc', 'Description', '描述'], ['tk.host', 'Host', '主机'], ['tk.since', 'Since', '已连接'], ['tk.info', 'Info', '信息'],
    ['tk.proxy', 'Proxy', 'proxy'], ['tk.proxies', 'Proxies', 'proxy'], ['tk.node', 'Node', '节点'], ['tk.nodes', 'Nodes', '节点'],
    ['tk.hub', 'Hub', 'hub'], ['tk.hubs', 'Hubs', 'hub'], ['tk.up', 'Up', '上行'], ['tk.down', 'Down', '下行'], ['tk.age', 'Age', '时长'],
    ['tk.state', 'State', '状态'], ['tk.listen', 'Listen', '监听'], ['tk.local_proxy', 'Local proxy', '本地 proxy'],
    ['tk.sent', 'Sent', '发送'], ['tk.received', 'Received', '接收'], ['tk.local', 'Local', '本地'], ['tk.in', 'In', '入'], ['tk.out', 'Out', '出'],
    ['tk.id', 'ID', 'ID'], ['tk.latency', 'Latency', '延迟'], ['tk.services', 'Services', '服务'], ['tk.service', 'Service', '服务'],
    ['tk.tunnels', 'Tunnels', '隧道'], ['tk.tunnel', 'Tunnel', '隧道'], ['tk.version', 'Version', '版本'], ['tk.protocol', 'Protocol', '协议'],
    ['tk.protocols', 'Protocols', '协议'], ['tk.peer_id', 'Peer ID', 'Peer ID'], ['tk.links', 'Links', '链路'], ['tk.links_n', '{n} links', '{n} 条链路'],
    ['tk.connect', 'Connect', '建连'], ['tk.share', 'Share', '分担'], ['tk.os', 'OS', '系统'], ['tk.uptime', 'Uptime', '运行时长'],
    ['tk.body_enc', 'Body encryption', '消息体加密'], ['tk.bodies_enc', 'bodies encrypted', '消息体已加密'], ['tk.none', 'none', '无'],
    ['tk.any', 'any', '任意'], ['tk.listening', 'listening', '监听中'], ['tk.via', 'Via', '经由'], ['tk.connections', 'Connections', '连接数'],
    ['tk.console', 'Console', '控制台'], ['tk.no_web', 'no web proxy', '无 Web 代理'], ['tk.web_only', 'web proxy only', '仅 Web 代理'],
    ['tk.open_tunnels', 'Open tunnels', '打开的隧道'], ['tk.n_services', '{n} services', '{n} 个服务'], ['tk.n_forwards', '{n} forwards', '{n} 个转发'], ['tk.open_of', '{open} / {max} tunnels', '{open}/{max} 条隧道'],
    ['tk.st.registered', 'registered', '已注册'], ['tk.st.connecting', 'connecting', '连接中'], ['tk.st.failed', 'failed', '失败'],
    ['tk.st.stopped', 'stopped', '已停止'],
    ['tk.query', 'Query', '查询'], ['tk.kick', 'Kick', '踢出'], ['tk.close', 'Close', '关闭'], ['tk.details', 'Details', '详情'],
    ['tk.edit', 'Edit', '编辑'], ['tk.remove', 'Remove', '移除'], ['tk.open', 'Open', '打开'], ['tk.edit_services', 'Edit services', '编辑服务'],
    ['tk.add_service', 'Add service', '添加服务'], ['tk.save_node', 'Save to node', '保存到节点'], ['tk.add_hub', 'Add hub', '添加 hub'],
    ['tk.add_a_hub', 'Add a hub', '添加 hub'], ['tk.hub_n', 'Hub {name}', 'hub {name}'], ['tk.save_connect', 'Save and connect', '保存并连接'],
    ['tk.services_of', 'Services of node {name}', '节点 {name} 的服务'],
    ['tk.services_via', 'Services of node {name} (via {hub})', '节点 {name} 的服务（经由 {hub}）'],
    ['tk.kicked', 'Kicked', '已踢出'], ['tk.tunnel_closed', 'Tunnel closed', '隧道已关闭'], ['tk.hub_saved', 'Hub saved', 'hub 已保存'],
    ['tk.removed', 'Removed', '已移除'], ['tk.services_saved', 'Services saved', '服务已保存'],
    ['tk.services_saved_node', 'Services saved on the node', '服务已保存到节点'], ['tk.disconnect_q', 'Disconnect from {name}?', '断开与 {name} 的连接？'],
    ['tk.kick_q', 'Kick {name}?', '踢出 {name}？'], ['tk.close_q', 'Close tunnel {id}?', '关闭隧道 {id}？'],
    ['tk.pool', 'Pool size', '连接池大小'], ['tk.password', 'Shared password (deprecated)', '共享密码（已弃用）'],
    ['tk.token', 'Token the hub issued', 'hub 签发的令牌'], ['tk.token_ph', 'fitt1.…, from the hub\'s Tokens page', 'fitt1.…，来自 hub 的“令牌”页'],
    ['tk.token_keep', 'keep token {id}', '保留令牌 {id}'], ['tk.cred_need', 'A token, or the hub\'s password', '需要令牌或 hub 的密码'],
    ['tk.token_note', 'A token replaces the password, and a password the token.', '令牌会替换密码，密码也会替换令牌。'], ['tk.body_pw', 'Body AES password', '消息体 AES 密码'],
    ['tk.clear_body', 'Remove the body password', '移除消息体密码'], ['tk.eps', 'Protocols, in the order they are tried', '协议（按尝试顺序）'],
    ['tk.on', 'On', '启用'], ['tk.eps_note', 'Each link dials the first protocol on, and falls back to the next when that fails.',
      '每条链路先拨号第一个启用的协议，失败时依次回退到下一个。'],
    ['tk.pin', 'Hub certificate pin (SHA-256), for WSS, TLS and QUIC', 'Hub 证书指纹（SHA-256），用于 WSS、TLS 与 QUIC'], ['tk.pin_ph', 'from the hub\'s page', '取自 hub 页面'],
    ['tk.insecure', 'Accept any QUIC certificate (testing only: anyone on the path could read and change the links)', '接受任意 QUIC 证书（仅用于测试：路径上的任何人都能读取和篡改链路）'],
    ['tk.eps_secure', 'WSS, TLS and QUIC check the hub\'s certificate by the pin. TCP and WebSocket carry the links in the clear: to a host that is not a loopback one they need the body password.',
      'WSS、TLS 与 QUIC 按指纹校验 hub 的证书。TCP 与 WebSocket 明文传输链路：连接非回环主机时须设置消息体密码。'],
    ['tk.ca_file', 'Checked against the CAs in {file}, set in the config.', '按配置中设置的 {file} 内的 CA 校验。'],
    ['tk.keep', '•••••••• leave blank to keep', '•••••••• 留空则保持不变'], ['tk.name_opt', 'name (optional)', '名称（可选）'],
    ['tk.optional', 'optional', '可选'], ['tk.move_up', 'Up', '上移'], ['tk.move_down', 'Down', '下移'], ['tk.no_services', 'No services', '没有服务'],
    ['tk.map', 'Tunnel map', '隧道全景'], ['tk.path', 'Tunnel path', '隧道链路'], ['tk.all_tunnels', 'All tunnels', '全部隧道'],
    ['tk.control', 'Control link', '控制连接'], ['tk.traffic', 'Traffic', '流量热度'], ['tk.low_high', 'low → high', '低 → 高'],
    ['tk.tunnel_id', 'Tunnel ID', '隧道 ID'], ['tk.local_app', 'Local app', '本地应用'], ['tk.this_page', 'this page', '本页'],
    ['tk.not_visible', 'not visible here', '此处不可见'], ['tk.only_proxy', 'Only the proxy sees the local app.', '只有 proxy 能看到本地应用。'],
    ['tk.closed', 'This tunnel is closed.', '该隧道已关闭。'], ['tk.no_tunnels', 'No open tunnels; dashed lines are control links.', '暂无隧道；虚线为控制连接。'],
    ['tk.link', 'link', '链路'], ['tk.link_over', 'link over {p}', '链路（{p}）'], ['tk.loss', '{n}% loss', '丢包 {n}%'],
    ['tk.tunnels_n', 'Tunnels ({n})', '隧道（{n}）'], ['tk.select', 'Click a box or a line for its details.', '点击方框或连线查看详情。'],
    ['tk.k.app', 'Local app', '本地应用'], ['tk.k.proxy', 'Proxy', 'proxy'], ['tk.k.forward', 'Forward', '转发'], ['tk.k.hub', 'Hub', 'hub'],
    ['tk.k.pod', 'Node', '节点'], ['tk.k.node', 'Service', '服务'],
    ['tk.lat.parts', 'last {last} · min {min} · avg {avg}', '最近 {last} · 最小 {min} · 平均 {avg}'],
    ['tk.config.note', 'This role\'s settings live in fit\'s config, as the tunnel.{role} member: what these pages change is written back into it and applies at once, without a restart, and is kept encrypted when fit keeps its config in admin.store.',
      '本角色的配置保存在 fit 配置的 tunnel.{role} 成员中：在这些页面上的修改会写回该配置并立即生效，无需重启；当 fit 把配置保存在 admin.store 中时，配置以加密方式保存。']
  ];
  var en = {}, zh = {};
  WORDS.forEach(function (w) { en[w[0]] = w[1]; zh[w[0]] = w[2]; });
  UI.addI18n({ 'en': en, 'zh-CN': zh });

  // ---- Formatting / 格式化 ----
  var PROTO_NAMES = { tcp: 'TCP', tls: 'TLS', ws: 'WebSocket', wss: 'WSS', quic: 'QUIC' };
  function bytes(n) {
    n = +n || 0;
    return n < 1024 ? n + ' B' : n < 1048576 ? (n / 1024).toFixed(1) + ' KiB' : n < 1073741824 ? (n / 1048576).toFixed(1) + ' MiB' : (n / 1073741824).toFixed(2) + ' GiB';
  }
  // secs is how long ago time t (RFC 3339) was, in seconds; -1 for none. / 时间 t 距今的秒数；没有时为 -1。
  function secs(v) {
    var d = v ? new Date(v) : null;
    if (!d || isNaN(d) || d.getFullYear() < 1971) return -1;
    return Math.max(0, (Date.now() - d.getTime()) / 1000 | 0);
  }
  // dur is how long ago t was, short: 42s, 5m, 3h, 2d. / t 距今的时长，简写。
  function dur(v) {
    var s = secs(v);
    if (s < 0) return '—';
    return s < 60 ? s + 's' : s < 3600 ? (s / 60 | 0) + 'm' : s < 86400 ? (s / 3600 | 0) + 'h' : (s / 86400 | 0) + 'd';
  }
  // Round trips, as latency.Stats ({last, avg, min, loss, samples}, in milliseconds).
  // 往返时延，即 latency.Stats（{last, avg, min, loss, samples}，单位毫秒）。
  function ms(v) { return v < 1 ? v.toFixed(2) + ' ms' : v < 10 ? v.toFixed(1) + ' ms' : Math.round(v) + ' ms'; }
  function rttGrade(s) { return s.loss >= .2 || s.avg >= 150 ? 'poor' : s.loss >= .05 || s.avg >= 50 ? 'fair' : 'good'; }
  function rttLoss(s) { return s.loss >= .01 ? t('tk.loss', { n: Math.round(s.loss * 100) }) : ''; }
  function rttText(s) { return s && s.samples ? ms(s.avg) + (rttLoss(s) ? ' · ' + rttLoss(s) : '') : ''; }
  // rtt is a link's latency as an element, its details (or tip) in the tooltip. / 连接延迟的元素，详情（或 tip）在提示中。
  function rtt(s, tip) {
    if (!s || !s.samples) return h('span.muted', '—');
    return h('span', h('span.rtt-' + rttGrade(s), { title: tip || t('tk.lat.parts', { last: ms(s.last), min: ms(s.min), avg: ms(s.avg) }) }, ms(s.avg)),
      rttLoss(s) ? h('span.muted.small', ' ' + rttLoss(s)) : null);
  }
  // rttSum is a path of links one after another, unknown if any is. / 依次相连的多段连接组成的路径，任一段未知则未知。
  function rttSum() {
    var ls = Array.prototype.slice.call(arguments);
    if (!ls.length || ls.some(function (s) { return !s || !s.samples; })) return null;
    function add(k) { return ls.reduce(function (a, s) { return a + s[k]; }, 0); }
    return { last: add('last'), avg: add('avg'), min: add('min'), loss: 1 - ls.reduce(function (a, s) { return a * (1 - s.loss); }, 1),
      samples: Math.min.apply(null, ls.map(function (s) { return s.samples; })) };
  }
  // protoOf is the protocol of an address, host:port being TCP. / 地址的协议，host:port 即 TCP。
  function protoOf(a) { var m = String(a || '').match(/^([a-z0-9]+):\/\//i); return m ? m[1].toLowerCase() : 'tcp'; }
  function protoName(p) { return PROTO_NAMES[p] || p; }
  function protoBadge(p) { return UI.badge(protoName(p), p === 'quic' || p === 'tls' || p === 'wss' ? 'primary' : p === 'ws' ? 'info' : ''); }
  // stateBadge is an uplink's state, with its error under it. / 上联的状态，其下为错误信息。
  function stateBadge(state, error) {
    var kind = state === 'registered' ? 'ok' : state === 'failed' ? 'danger' : state === 'connecting' ? 'warn' : '';
    return h('div', UI.badge(t('tk.st.' + state), kind, true), error ? h('div.small.muted', error) : null);
  }
  // linkProtos is what protocols an uplink's links took. / 上联各链路所用的协议。
  function linkProtos(s) {
    var p = s.protos || {}, ks = Object.keys(p);
    if (!ks.length) return null;
    return h('div.small.muted', ks.map(function (k, i) { return [i ? ' · ' : '', protoName(k) + ' ×' + p[k]]; }));
  }
  // endpointList is a hub's endpoints, in the order they are tried, those off struck out. / hub 的各地址，按尝试顺序，停用的划掉。
  function endpointList(s) {
    var eps = s.endpoints || [];
    if (!eps.length) return h('span.mono', s.addr || '');
    return h('div.stack', { style: { gap: '4px' } }, eps.map(function (e, i) {
      return h('div.row', { style: { gap: '6px', opacity: e.enabled ? '1' : '.5', textDecoration: e.enabled ? 'none' : 'line-through' } },
        h('span.muted.small', (i + 1) + '.'), protoBadge(e.protocol), h('span.mono', String(e.address).replace(/^[a-z0-9]+:\/\//i, '')));
    }));
  }
  // pageURL is the http URL of a listen address; one on every address is reached at this page's host.
  // pageURL 为监听地址的 http URL；监听全部地址的经本页面的主机访问。
  function pageURL(addr) {
    var m = String(addr).match(/^(.*):(\d+)$/), host = (m ? m[1] : String(addr)).replace(/^\[|\]$/g, '');
    if (!host || host === '0.0.0.0' || host === '::') host = location.hostname.replace(/^\[|\]$/g, '');
    return 'http://' + (host.indexOf(':') >= 0 ? '[' + host + ']' : host) + (m ? ':' + m[2] : '');
  }
  // openPage opens the page at addr in a new tab, where it asks for its own login. / 在新标签页打开 addr 的页面（它自行要求登录）。
  function openPage(addr) { window.open(pageURL(addr) + '/', '_blank', 'noopener'); }
  // hostOf is the host of an address, host:port or a URL. / 地址（host:port 或 URL）的主机。
  function hostOf(a) { try { return new URL(String(a).indexOf('://') >= 0 ? a : 'tcp://' + a).hostname; } catch (e) { return ''; } }

  // ---- Flow graphs / 隧道图 ----
  // graph(cols) is an empty graph: node adds a node to a column once (later calls return it as it is); link adds a
  // control link; tunnel runs tunnel t ({id, name, up, down, created}) along the nodes of path, making their links
  // tunnel links, with labels[i] ({label, lock, rtt}) on link i.
  // graph(cols) 为空图：node 向某列添加节点（只添加一次，之后返回已有的）；link 添加控制连接；tunnel 让隧道 t 沿 path 中的
  // 节点走，把经过的连接变为隧道连接，labels[i]（{label, lock, rtt}）标在第 i 段上。
  function graph(cols) {
    var g = { cols: cols, nodes: [], links: [], tunnels: {}, byId: {} };
    g.node = function (id, col, p) {
      if (g.byId[id]) return g.byId[id];
      var x = { id: id, col: col, kind: 'node', label: id, sub: '', facts: [], tunnels: {} };
      Object.keys(p || {}).forEach(function (k) { x[k] = p[k]; });
      g.byId[id] = x; g.nodes.push(x);
      return x;
    };
    g.link = function (from, to, p) {
      var key = from + '\n' + to, l = null;
      g.links.forEach(function (v) { if (v.key === key) l = v; });
      if (!l) { l = { key: key, from: from, to: to, tunnels: {}, up: 0, down: 0, dashed: true }; g.links.push(l); }
      Object.keys(p || {}).forEach(function (k) { l[k] = p[k]; });
      return l;
    };
    g.tunnel = function (tn, path, labels) {
      g.tunnels[tn.id] = tn;
      path.forEach(function (id) { if (g.byId[id]) g.byId[id].tunnels[tn.id] = true; });
      for (var i = 0; i + 1 < path.length; i++) {
        var l = g.link(path[i], path[i + 1], labels && labels[i]);
        l.dashed = false; l.tunnels[tn.id] = true; l.up += tn.up; l.down += tn.down;
      }
    };
    return g;
  }
  var FLOW_ICON = { app: 'terminal', proxy: 'proxy', forward: 'forward', hub: 'hub', pod: 'pod', node: 'plug', ssh: 'terminal', web: 'globe' };
  // nodeIcon picks a service's icon by its name. / 按服务名选图标。
  function nodeIcon(name) { return /ssh|shell|term/i.test(name) ? 'ssh' : /web|http|page|ui/i.test(name) ? 'web' : 'node'; }
  function flowState(s) { return s === 'registered' ? 'ok' : s === 'failed' ? 'bad' : s ? 'wait' : ''; }

  var BW = 168, BH = 50;
  // layout orders each column to keep links from crossing (a few sweeps of the barycenter heuristic) and places
  // every box. / 对每列排序以减少连线交叉（几轮重心法），并确定每个方框的位置。
  function layout(g) {
    var n = g.cols.length, cols = g.cols.map(function (_, i) { return g.nodes.filter(function (x) { return x.col === i; }); }), pos = {};
    function place(c) { c.forEach(function (x, i) { pos[x.id] = (i + .5) / c.length; }); }
    cols.forEach(place);
    function sweep(ci, back) {
      var c = cols[ci], b = {};
      c.forEach(function (x) {
        var ns = g.links.filter(function (l) { return back ? l.from === x.id : l.to === x.id; })
          .map(function (l) { return pos[back ? l.to : l.from]; }).filter(function (v) { return v !== undefined; });
        b[x.id] = ns.length ? ns.reduce(function (a, v) { return a + v; }, 0) / ns.length : pos[x.id];
      });
      c.sort(function (p, q) { return b[p.id] - b[q.id]; });
      place(c);
    }
    for (var it = 0; it < 3; it++) {
      for (var i = 1; i < n; i++) sweep(i, false);
      for (var j = n - 2; j >= 0; j--) sweep(j, true);
    }
    var row = g.chain ? 130 : 84, maxK = Math.max.apply(null, [1].concat(cols.map(function (c) { return c.length; })));
    var gap = g.chain ? 140 : 96, pad = BW / 2 + 16, top = g.chain ? 60 : 44;
    g.W = Math.max(760, 2 * pad + (n - 1) * (BW + gap)); g.H = top + maxK * row + 8;
    cols.forEach(function (c, ci) {
      c.forEach(function (x, i) {
        x.x = n === 1 ? g.W / 2 : pad + ci * (g.W - 2 * pad) / (n - 1);
        x.y = top + (maxK - c.length) * row / 2 + i * row + row / 2;
      });
    });
  }
  function s(tag, attrs, text) {
    var el = document.createElementNS(NS, tag);
    Object.keys(attrs || {}).forEach(function (k) { if (attrs[k] != null) el.setAttribute(k, attrs[k]); });
    if (text != null) el.textContent = text;
    return el;
  }
  function cut(v, n) { v = String(v == null ? '' : v); return v.length > n ? v.slice(0, n - 1) + '…' : v; }
  function keys(o) { return Object.keys(o || {}); }

  // svg draws graph g; sel is {node} or {link} selected, on(kind, value) is called on a click.
  // svg 绘制图 g；sel 为所选的 {node} 或 {link}，点击时调用 on(kind, value)。
  function svg(g, sel, on) {
    layout(g);
    var root = s('svg', { 'class': 'lm-svg', viewBox: '0 0 ' + g.W + ' ' + g.H, width: g.W, height: g.H, role: 'img' });
    g.cols.forEach(function (c, i) {
      var x = g.nodes.filter(function (v) { return v.col === i; })[0];
      if (x) root.appendChild(s('text', { 'class': 'lm-colh', x: x.x, y: 22, 'text-anchor': 'middle' }, c));
    });
    var max = 1;
    g.links.forEach(function (l) { max = Math.max(max, g.chain ? Math.max(l.up, l.down) : l.up + l.down); });
    function heat(n) { var r = Math.log1p(n) / Math.log1p(max); return max < 1024 || r < .45 ? 1 : r < .8 ? 2 : 3; }
    var lines = s('g'), boxes = s('g'), els = [];
    // focus dims what tunnels ts and boxes ids do not touch. / 淡化与隧道 ts、方框 ids 无关的部分。
    function focus(ts, ids) {
      els.forEach(function (e) {
        var hit = ids[e.id] || keys(e.tunnels).some(function (k) { return ts[k]; }) || (e.dashed && ids[e.from] && ids[e.to]);
        e.el.style.opacity = hit ? '1' : '.22';
      });
    }
    function unfocus() { els.forEach(function (e) { e.el.style.opacity = ''; }); }
    g.focus = focus; g.unfocus = unfocus;
    g.links.forEach(function (l) {
      var a = g.byId[l.from], b = g.byId[l.to];
      if (!a || !b) return;
      var x1 = a.x + BW / 2, x2 = b.x - BW / 2, dx = (x2 - x1) / 2;
      function curve(o) { return 'M' + x1 + ' ' + (a.y + o) + 'C' + (x1 + dx) + ' ' + (a.y + o) + ',' + (x2 - dx) + ' ' + (b.y + o) + ',' + x2 + ' ' + (b.y + o); }
      var n = keys(l.tunnels).length, w = l.dashed ? 1.4 : g.chain ? 2.6 : 1.8 + 2.4 * Math.log1p(l.up + l.down) / Math.log1p(max);
      var only = !g.chain && !l.dashed && n === 1 ? keys(l.tunnels)[0] : undefined;
      var isSel = sel && sel.link === l.key;
      var grp = s('g', { 'class': 'lm-lk' + (l.dashed ? ' idle' : '') + (isSel ? ' sel' : '') });
      grp.appendChild(s('title', null, a.label + ' → ' + b.label + (rttText(l.rtt) ? ' · ' + rttText(l.rtt) : '') +
        (l.dashed ? '' : ' · ' + n + ' · ↑ ' + bytes(l.up) + ' ↓ ' + bytes(l.down))));
      grp.appendChild(s('path', { 'class': 'lm-hit', d: curve(0) }));
      if (l.dashed) grp.appendChild(s('path', { 'class': 'lm-base', d: curve(0), 'stroke-width': w.toFixed(1) }));
      else if (g.chain) {
        // A tunnel's path has a lane each way, up above and down below, each colored by its own bytes.
        // 隧道链路每个方向一条道，上行在上、下行在下，各按自己的字节数着色。
        [[-6, l.up], [6, l.down]].forEach(function (lane) {
          var lg = s('g', { 'class': 'lm-lk busy h' + heat(lane[1]) });
          lg.appendChild(s('path', { 'class': 'lm-base', d: curve(lane[0]), 'stroke-width': w.toFixed(1) }));
          lg.appendChild(s('path', { 'class': 'lm-flow', d: curve(lane[0]), 'stroke-width': w.toFixed(1) }));
          grp.appendChild(lg);
        });
      } else {
        grp.setAttribute('class', grp.getAttribute('class') + ' busy h' + heat(l.up + l.down));
        grp.appendChild(s('path', { 'class': 'lm-base', d: curve(0), 'stroke-width': w.toFixed(1) }));
        grp.appendChild(s('path', { 'class': 'lm-flow', d: curve(0), 'stroke-width': w.toFixed(1) }));
      }
      var mx = (x1 + x2) / 2, my = (a.y + b.y) / 2;
      if (l.label) {
        var lb = s('text', { 'class': 'lm-rate', x: mx, y: my - (g.chain ? 40 : 12), 'text-anchor': 'middle', 'font-weight': '600' }, l.label);
        grp.appendChild(lb);
        if (l.lock) {
          var ic = UI.icon('lock', 'lm-ico');
          ic.setAttribute('x', mx + l.label.length * 3.1 + 3); ic.setAttribute('y', my - (g.chain ? 40 : 12) - 9);
          ic.setAttribute('width', 10); ic.setAttribute('height', 10);
          grp.appendChild(ic);
        }
      }
      if (g.chain && !l.dashed) {
        grp.appendChild(s('text', { 'class': 'lm-rate', x: mx, y: my - 14, 'text-anchor': 'middle' }, '↑ ' + bytes(l.up)));
        grp.appendChild(s('text', { 'class': 'lm-rate', x: mx, y: my + 24, 'text-anchor': 'middle' }, '↓ ' + bytes(l.down)));
      }
      if (l.rtt && l.rtt.samples && !l.quiet) {
        grp.appendChild(s('text', { 'class': 'lm-rtt rtt-' + rttGrade(l.rtt), x: mx, y: my + (g.chain ? 42 : 15), 'text-anchor': 'middle' }, rttText(l.rtt)));
      }
      grp.addEventListener('mouseenter', function () { var ids = {}; ids[l.from] = ids[l.to] = true; focus(l.tunnels, ids); });
      grp.addEventListener('mouseleave', unfocus);
      grp.addEventListener('click', function () { if (only !== undefined) on('tunnel', only); else on('link', l.key); });
      if (only !== undefined) grp.style.cursor = 'zoom-in';
      lines.appendChild(grp);
      els.push({ el: grp, tunnels: l.tunnels, dashed: l.dashed, from: l.from, to: l.to });
    });
    g.nodes.forEach(function (x) {
      var grp = s('g', { 'class': 'lm-nd k-' + x.kind + (x.self ? ' self' : '') + (sel && sel.node === x.id ? ' sel' : ''), transform: 'translate(' + (x.x - BW / 2) + ' ' + (x.y - BH / 2) + ')' });
      grp.appendChild(s('title', null, x.label + (x.sub ? '\n' + x.sub : '')));
      var tile = s('rect', { 'class': 'lm-tile', width: BW, height: BH, rx: 10 });
      if (x.unknown) tile.setAttribute('stroke-dasharray', '4 4');
      grp.appendChild(tile);
      var ic = UI.icon(FLOW_ICON[x.icon || x.kind] || 'plug', 'lm-ico');
      ic.setAttribute('x', 10); ic.setAttribute('y', 15); ic.setAttribute('width', 20); ic.setAttribute('height', 20);
      grp.appendChild(ic);
      grp.appendChild(s('text', { 'class': 'lm-lb', x: 38, y: x.sub ? 21 : 30 }, cut(x.label, 18)));
      if (x.sub) grp.appendChild(s('text', { 'class': 'lm-sb', x: 38, y: 37 }, cut(x.sub, 22)));
      if (x.state) grp.appendChild(s('circle', { 'class': 'lm-st ' + x.state, cx: BW - 10, cy: 10, r: 4 }));
      if (x.self) grp.appendChild(s('text', { 'class': 'lm-tag', x: BW / 2, y: -6, 'text-anchor': 'middle' }, t('tk.this_page')));
      grp.addEventListener('mouseenter', function () {
        var ids = {}; ids[x.id] = true;
        g.links.forEach(function (l) { if (l.dashed && (l.from === x.id || l.to === x.id)) { ids[l.from] = ids[l.to] = true; } });
        focus(x.tunnels, ids);
      });
      grp.addEventListener('mouseleave', unfocus);
      grp.addEventListener('click', function () { on('node', x.id); });
      boxes.appendChild(grp);
      els.push({ el: grp, id: x.id, tunnels: x.tunnels });
    });
    root.appendChild(lines);
    root.appendChild(boxes);
    return root;
  }

  // ---- With a module's UI / 基于模块的 UI ----
  function make(UI) {
    var h = UI.h, t = UI.t;

    // call posts body to path and says ok, or the config's warnings, or why it failed: a Promise of whether it
    // worked. / 向 path 提交 body，提示成功、配置的警告或失败原因；返回是否成功的 Promise。
    function call(path, body, ok) {
      return UI.api('POST', path, body).then(function (j) {
        if (j && j.warnings && j.warnings.length) UI.toast(j.warnings.join('\n'), 'info'); else UI.toast(ok);
        return true;
      }, function (e) { UI.fail(e); return false; });
    }
    // keepOpen turns call's answer into a modal action's: false keeps it open. / 把 call 的结果转为弹窗操作的结果：false 保持打开。
    function keepOpen(p) { return p.then(function (ok) { return ok ? undefined : false; }); }

    function configNote(role) { return h('div.callout', UI.icon('info'), ' ', t('tk.config.note', { role: role })); }

    // services(list, extra) is an editor of services: a table of inputs, extra(s), when given, being a cell before
    // a row's remove button. → {el, add(s), read(), dirty()} / 服务编辑器：输入框表格，extra(s) 为每行删除按钮前的一列。
    function services(list, extra, onChange) {
      var body = h('tbody'), changed = false;
      function mark() { changed = true; if (onChange) onChange(); }
      function add(sv) {
        sv = sv || {};
        var tr = h('tr',
          h('td', h('input.input.input--sm', { placeholder: t('tk.name').toLowerCase(), value: sv.name || '', oninput: mark })),
          h('td', h('input.input.input--sm.mono', { placeholder: '10.0.0.5:22', value: sv.address || '', oninput: mark })),
          h('td', h('input.input.input--sm', { placeholder: t('tk.optional'), value: sv.description || '', oninput: mark })),
          extra ? h('td', extra(sv)) : null,
          h('td.actions', UI.btn('', 'x', function () { tr.remove(); mark(); }, 'ghost sm icon')));
        body.appendChild(tr);
        return tr;
      }
      (list || []).forEach(add);
      var el = h('div.table-wrap', h('table.table', h('thead', h('tr', h('th', t('tk.name')), h('th', t('tk.addr_hp')), h('th', t('tk.desc')),
        extra ? h('th', t('tk.connect')) : null, h('th', ''))), body));
      return {
        el: el,
        add: function (sv) { var tr = add(sv); mark(); var i = tr.querySelector('input'); if (i) i.focus(); },
        read: function () {
          return Array.prototype.map.call(body.querySelectorAll('tr'), function (tr) {
            var i = tr.querySelectorAll('input');
            return { name: i[0].value.trim(), address: i[1].value.trim(), description: i[2].value.trim() };
          }).filter(function (sv) { return sv.name || sv.address; });
        },
        dirty: function () { return changed; }
      };
    }

    // servicesModal edits a node's services in a modal; save(list) → Promise of whether it worked.
    // 在弹窗中编辑节点的服务；save(list) → 是否成功的 Promise。
    function servicesModal(title, list, save) {
      var ed = services(list.length ? list : [{}]);
      UI.modal({
        title: title, wide: true,
        body: h('div.stack', ed.el, h('div', UI.btn(t('tk.add_service'), 'plus', function () { ed.add({}); }, 'sm'))),
        actions: [{ label: t('cancel') }, { label: t('tk.save_node'), kind: 'primary', onclick: function () { return keepOpen(save(ed.read())); } }]
      });
    }

    // hubEditor edits hub st (an uplink's status) or adds one when st is null: its name, pool, passwords, its
    // endpoints by protocol in the order they are tried, and the pin of the hub's certificate. Passwords never come
    // back: an empty one keeps the one set. / 编辑 hub st（上联状态），st 为 null 时添加：名称、连接池、密码、按尝试顺序
    // 排列的各协议地址与 hub 证书指纹。密码不会返回：留空则保持原值。
    var EP_HINT = { tcp: '127.0.0.1:7000', tls: 'tls://host:7000', ws: 'ws://127.0.0.1:7001/tunnel', wss: 'wss://host:7001/tunnel', quic: 'quic://host:7443' };
    function hubEditor(st, onSaved) {
      var f = {
        name: h('input.input', { value: st ? st.name : '', placeholder: t('tk.name_opt') }),
        pool: h('input.input', { type: 'number', min: 1, max: 100, placeholder: '10', value: st && st.pool_size ? st.pool_size : '' }),
        token: h('input.input.mono', { type: 'password', autocomplete: 'new-password', spellcheck: 'false', placeholder: st && st.token_id ? t('tk.token_keep', { id: st.token_id }) : t('tk.token_ph') }),
        password: h('input.input', { type: 'password', autocomplete: 'new-password', placeholder: st && st.has_password ? t('tk.keep') : t('tk.optional') }),
        body: h('input.input', { type: 'password', autocomplete: 'new-password', placeholder: st && st.has_body_password ? t('tk.keep') : t('tk.optional') }),
        clearBody: h('input', { type: 'checkbox' }),
        pin: h('input.input.mono', { value: st && (st.pin || st.quic_pin) || '', placeholder: t('tk.pin_ph'), autocomplete: 'off', spellcheck: 'false' }),
        insecure: h('input', { type: 'checkbox', checked: !!(st && st.quic_insecure) })
      };
      var eps = ((st && st.endpoints) || []).slice();
      ['wss', 'tls', 'quic', 'ws', 'tcp'].forEach(function (p) { if (!eps.some(function (e) { return e.protocol === p; })) eps.push({ protocol: p, address: '', enabled: false }); });
      var tbody = h('tbody');
      eps.forEach(function (e) {
        var tr = h('tr', { 'data-proto': e.protocol },
          h('td', h('label.check', h('input', { type: 'checkbox', checked: !!e.enabled }))),
          h('td', protoBadge(e.protocol)),
          h('td', h('input.input.input--sm.mono', { value: e.address || '', placeholder: EP_HINT[e.protocol] || '' })),
          h('td.actions',
            UI.btn('', 'chev', function () { var o = tr.previousElementSibling; if (o) o.before(tr); }, 'ghost sm icon'),
            UI.btn('', 'chev', function () { var o = tr.nextElementSibling; if (o) o.after(tr); }, 'ghost sm icon')));
        // The chevrons point up and down. / 箭头分别朝上、朝下。
        var bs = tr.querySelectorAll('td.actions svg');
        bs[0].style.transform = 'rotate(-90deg)'; bs[1].style.transform = 'rotate(90deg)';
        tr.querySelectorAll('td.actions button')[0].title = t('tk.move_up');
        tr.querySelectorAll('td.actions button')[1].title = t('tk.move_down');
        tbody.appendChild(tr);
      });
      UI.modal({
        title: st ? t('tk.hub_n', { name: st.name }) : t('tk.add_a_hub'), wide: true,
        body: h('div.form',
          h('div.form-row', UI.field(t('tk.name'), f.name), UI.field(t('tk.pool'), f.pool)),
          h('div.form-row', UI.field(t('tk.token'), f.token, t('tk.token_note')), UI.field(t('tk.password'), f.password)),
          UI.field(t('tk.body_pw'), f.body),
          st && st.has_body_password ? h('label.check', f.clearBody, ' ', t('tk.clear_body')) : null,
          h('h4', t('tk.eps')),
          h('div.table-wrap', h('table.table', h('thead', h('tr', h('th', t('tk.on')), h('th', t('tk.protocol')), h('th', t('tk.address')), h('th', ''))), tbody)),
          h('div.small.muted', t('tk.eps_note')),
          h('div.small.muted', t('tk.eps_secure')),
          UI.field(t('tk.pin'), f.pin),
          st && st.ca_file ? h('div.small.muted', t('tk.ca_file', { file: st.ca_file })) : null,
          h('label.check', f.insecure, ' ', t('tk.insecure'))),
        actions: [{ label: t('cancel') }, {
          label: t('tk.save_connect'), kind: 'primary', onclick: function () {
            if (!st && !f.password.value && !f.token.value.trim()) { f.token.focus(); UI.toast(t('tk.cred_need'), 'error'); return false; }
            var endpoints = Array.prototype.map.call(tbody.querySelectorAll('tr'), function (tr) {
              var i = tr.querySelectorAll('input');
              return { protocol: tr.getAttribute('data-proto'), address: i[1].value.trim(), enabled: i[0].checked };
            }).filter(function (e) { return e.address; });
            var hub = { name: f.name.value.trim(), endpoints: endpoints, token: f.token.value.trim(), password: f.password.value, body_password: f.body.value,
              pool_size: +f.pool.value || 0, pin: f.pin.value.trim(), quic_insecure: f.insecure.checked };
            var p = st ? call('/api/hubs/update', { name: st.name, hub: hub, clear_body_password: f.clearBody.checked }, t('tk.hub_saved'))
              : call('/api/hubs', hub, t('tk.hub_saved'));
            return keepOpen(p.then(function (ok) { if (ok && onSaved) onSaved(); return ok; }));
          }
        }]
      });
    }
    // delHub asks, then removes hub name. / 确认后移除 hub name。
    function delHub(name, onDone) {
      UI.confirm(t('tk.disconnect_q', { name: name }), t('tk.remove'), true).then(function (ok) {
        if (ok) call('/api/hubs/delete', { name: name }, t('tk.removed')).then(function (v) { if (v && onDone) onDone(); });
      });
    }

    // flow({all() → graph, one(id) → graph|null}) shows the tunnel diagrams in a modal: open() the map of every
    // tunnel, open(id) one tunnel's path. A page calls refresh() after it loads, so an open diagram stays current.
    // flow({all() → 图, one(id) → 图|null}) 在弹窗中显示隧道图：open() 为全部隧道的全景图，open(id) 为一条隧道的链路。
    // 页面每次加载后调用 refresh()，使打开的图保持最新。
    function flow(o) {
      var cur = null; // {id, sel, el: {...}, close}
      function open(id) {
        if (cur) { cur.view = id; cur.sel = null; draw(); return; }
        var head = h('div.row.row--between'), chips = h('div.row', { style: { gap: '6px' } }), canvas = h('div.lm-canvas'), side = h('aside.lm-side');
        var legend = h('div.small.muted.lm-legend',
          h('span', { style: { display: 'inline-block', width: '22px', borderTop: '3px solid var(--primary)', verticalAlign: 'middle' } }), ' ', t('tk.tunnel'), ' · ',
          h('span', { style: { display: 'inline-block', width: '22px', borderTop: '2px dashed var(--border-2)', verticalAlign: 'middle' } }), ' ', t('tk.control'), ' · ',
          t('tk.latency') + ': ', h('span.rtt-good', '<50ms'), ' / ', h('span.rtt-fair', '<150ms'), ' / ', h('span.rtt-poor', '≥150ms'));
        cur = { view: id, sel: null, head: head, chips: chips, canvas: canvas, side: side };
        var me = cur;
        cur.close = UI.modal({
          title: t('tk.tunnels'), wide: true,
          body: h('div.stack', head, chips, legend, h('div.lm-body', canvas, side)),
          onclose: function () { if (cur === me) cur = null; }
        });
        // Wider than a form's modal: the map needs room. / 比表单弹窗更宽：全景图需要空间。
        var box = canvas.closest('.modal__box');
        if (box) box.style.width = 'min(1240px, 100%)';
        draw();
      }
      function refresh() { if (cur && document.body.contains(cur.canvas)) draw(); }
      function draw() {
        var one = cur.view !== undefined && cur.view !== null, g = one ? o.one(cur.view) : o.all();
        UI.mount(cur.head, h('div', h('h3', one ? t('tk.path') : t('tk.map')),
          h('div.small.muted', g ? (one ? (g.tunnels[cur.view] || {}).name : g.cols.join(' → ')) : '')),
          one ? UI.btn(t('tk.all_tunnels'), 'fold', function () { cur.view = null; cur.sel = cur.mapSel || null; draw(); }, 'sm') : null);
        if (!g) {
          UI.mount(cur.chips); UI.mount(cur.side);
          UI.mount(cur.canvas, UI.empty(t('tk.closed'), 'tunnel'));
          return;
        }
        g.chain = one;
        var ts = keys(g.tunnels).map(function (k) { return g.tunnels[k]; });
        var up = ts.reduce(function (a, x) { return a + x.up; }, 0), down = ts.reduce(function (a, x) { return a + x.down; }, 0), tn = ts[0];
        var cs = one ? [[t('tk.tunnel_id'), tn.id], [t('tk.age'), dur(tn.created)], [t('tk.up'), bytes(tn.up)], [t('tk.down'), bytes(tn.down)]]
          : [[t('tk.tunnels'), ts.length], [t('tk.up'), bytes(up)], [t('tk.down'), bytes(down)]];
        UI.mount(cur.chips, cs.map(function (c) { return UI.badge(c[0] + ' · ' + c[1]); }),
          !one && !ts.length ? h('span.small.muted', t('tk.no_tunnels')) : null);
        var left = cur.canvas.scrollLeft, top = cur.canvas.scrollTop;
        UI.mount(cur.canvas, svg(g, cur.sel, function (kind, v) {
          if (kind === 'tunnel') { cur.mapSel = cur.sel; cur.view = v; cur.sel = null; draw(); return; }
          cur.sel = kind === 'node' ? { node: v } : { link: v };
          draw();
        }));
        cur.canvas.scrollLeft = left; cur.canvas.scrollTop = top;
        UI.mount(cur.side, sidePanel(g, cur.sel, ts));
      }
      function sidePanel(g, sel, all) {
        var x = sel && sel.node && g.byId[sel.node], l = null;
        if (sel && sel.link) g.links.forEach(function (v) { if (v.key === sel.link) l = v; });
        if (!x && !l) x = g.nodes.filter(function (v) { return v.self; })[0] || g.nodes[0];
        if (!x && !l) return h('div.muted.small', t('tk.select'));
        var ids = keys(x ? x.tunnels : l.tunnels), ts = ids.map(function (k) { return g.tunnels[k]; }).filter(Boolean);
        function facts(fs) { return UI.kv(fs.filter(function (f) { return f[1] !== undefined && f[1] !== null && f[1] !== ''; })); }
        var list = g.chain ? null : h('div.stack', { style: { gap: '6px' } }, h('h4', t('tk.tunnels_n', { n: ts.length })),
          ts.length ? ts.map(function (tn) {
            var row = h('div.row.row--between', { style: { gap: '6px' } },
              h('div', h('b.small', tn.name), h('div.small.muted', '↑ ' + bytes(tn.up) + ' · ↓ ' + bytes(tn.down) + ' · ' + dur(tn.created))),
              UI.btn(t('tk.details'), null, function () { cur.mapSel = cur.sel; cur.view = tn.id; cur.sel = null; draw(); }, 'sm'));
            row.addEventListener('mouseenter', function () { var one = {}; one[tn.id] = true; if (g.focus) g.focus(one, {}); });
            row.addEventListener('mouseleave', function () { if (g.unfocus) g.unfocus(); });
            return row;
          }) : h('div.small.muted', t('tk.none')));
        if (x) {
          return h('div.stack', h('div.row', UI.icon(FLOW_ICON[x.icon || x.kind] || 'plug'), h('b', x.label)),
            h('div.small.muted', t('tk.k.' + x.kind)), x.note ? h('div.small.muted', x.note) : null,
            facts(x.facts || []), list);
        }
        var a = g.byId[l.from] || {}, b = g.byId[l.to] || {};
        return h('div.stack', h('b', a.label + ' → ' + b.label), h('div.small.muted', l.dashed ? t('tk.control') : t('tk.tunnel')),
          facts([[t('tk.protocol'), l.label || ''], [t('tk.latency'), rttText(l.rtt)], [t('tk.tunnels'), l.dashed ? '' : keys(l.tunnels).length],
            [t('tk.up'), l.dashed ? '' : bytes(l.up)], [t('tk.down'), l.dashed ? '' : bytes(l.down)]]), list);
      }
      return { open: open, refresh: refresh };
    }

    return { call: call, keepOpen: keepOpen, configNote: configNote, services: services, servicesModal: servicesModal,
      hubEditor: hubEditor, delHub: delHub, flow: flow };
  }

  var K = window.TunnelKit = {
    PROTO_NAMES: PROTO_NAMES, bytes: bytes, dur: dur, secs: secs, ms: ms, rtt: rtt, rttText: rttText, rttSum: rttSum, rttGrade: rttGrade,
    protoOf: protoOf, protoName: protoName, protoBadge: protoBadge, stateBadge: stateBadge, linkProtos: linkProtos,
    endpointList: endpointList, pageURL: pageURL, openPage: openPage, hostOf: hostOf,
    graph: graph, nodeIcon: nodeIcon, flowState: flowState
  };
  K.with = function (ui) { var k = make(ui); Object.keys(K).forEach(function (n) { if (!(n in k)) k[n] = K[n]; }); return k; };
})();
