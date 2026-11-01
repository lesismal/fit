// LinkMap: a role's links and traffic, drawn as a diagram of columns (who calls → this role → whom it calls → what it
// serves) over a table of the same links. A page builds a graph with LinkMap.graph from its status (/mesh/status and
// its role's lists) and LinkMap.mount polls it: the bytes each link carried are totals, and the rates are worked out
// here between two polls. Latency is coloured good, fair or poor. Vanilla SVG, built with createElementNS: no user
// text ever goes through innerHTML. Needs ui.js. Adapted from the tunnel's flow.js to the llm UI kit.
//
// LinkMap：角色的连接与流量，绘制为分列的拓扑图（调用方 → 本角色 → 被调用方 → 所提供的内容），下方为同样连接的表格。
// 页面用 LinkMap.graph 依据其状态（/mesh/status 及角色的列表）构建图，LinkMap.mount 定期刷新：连接传输的字节为累计值，
// 速率在此根据两次刷新之间的差值计算。延迟按好、中、差着色。原生 SVG，用 createElementNS 构建：用户文本从不经过 innerHTML。
// 依赖 ui.js。由 tunnel 的 flow.js 改写为 llm 的 UI 库。
//
// LinkMap.graph(cols) → g; g.node(id, col, {kind, label, sub, state: 'ok'|'bad'|'wait', self, facts: [[k, v]]});
// g.link(from, to, {fwd, back (bytes, the request's way and the answer's; left out: not known), rtt, calls, errors, inflight});
// LinkMap.mount(el, {load() → Promise of g, interval}) → {refresh}
(function () {
  'use strict';
  var h = UI.h, t = UI.t, NS = 'http://www.w3.org/2000/svg';

  UI.addI18n({
    'en': {
      'lm.map': 'Link map', 'lm.links': 'Links and traffic', 'lm.from': 'From', 'lm.to': 'To', 'lm.rtt': 'Latency',
      'lm.fwd': 'Requests', 'lm.back': 'Answers', 'lm.calls': 'Calls', 'lm.rate': '{r}/s', 'lm.none': 'No links yet.',
      'lm.good': 'good', 'lm.fair': 'fair', 'lm.poor': 'poor', 'lm.legend': 'Line width: traffic · dashed: idle · latency: ',
      'lm.calls_v': '{c} calls, {e} failed, {i} running', 'lm.select': 'Click a box or a line for its details.',
      'lm.kind': 'Kind', 'lm.total': 'Total', 'lm.now': 'Now', 'lm.stale': 'Refresh failed, showing the last state: {m}',
      'lm.k.proxy': 'Proxy', 'lm.k.hub': 'Hub', 'lm.k.node': 'Node', 'lm.k.model': 'Model', 'lm.k.upstream': 'Upstream',
      'lm.k.agents': 'Agents', 'lm.this': 'this page'
    },
    'zh-CN': {
      'lm.map': '链路图', 'lm.links': '连接与流量', 'lm.from': '来源', 'lm.to': '去向', 'lm.rtt': '延迟',
      'lm.fwd': '请求', 'lm.back': '应答', 'lm.calls': '调用', 'lm.rate': '{r}/秒', 'lm.none': '暂无连接。',
      'lm.good': '好', 'lm.fair': '中', 'lm.poor': '差', 'lm.legend': '线宽：流量 · 虚线：空闲 · 延迟：',
      'lm.calls_v': '{c} 次调用，{e} 次失败，{i} 次进行中', 'lm.select': '点击方框或连线查看详情。',
      'lm.kind': '类型', 'lm.total': '累计', 'lm.now': '当前', 'lm.stale': '刷新失败，显示的是上次的状态：{m}',
      'lm.k.proxy': '代理', 'lm.k.hub': 'Hub', 'lm.k.node': '节点', 'lm.k.model': '模型', 'lm.k.upstream': '上游',
      'lm.k.agents': 'Agent', 'lm.this': '本页面'
    }
  });

  var ICON = { proxy: 'swap', hub: 'globe', node: 'server', model: 'cube', upstream: 'zap', agents: 'users' };

  // ---- Formatting / 格式化 ----
  function bytes(n) {
    n = +n || 0;
    if (n < 1024) return Math.round(n) + ' B';
    var u = ['KB', 'MB', 'GB', 'TB'], i = -1;
    do { n /= 1024; i++; } while (n >= 1024 && i < u.length - 1);
    return n.toFixed(n >= 100 ? 0 : 1) + ' ' + u[i];
  }
  function rate(n) { return t('lm.rate', { r: bytes(n) }); }
  function rttGrade(r) {
    if (!r || !r.samples) return '';
    var v = r.avg || r.last;
    return v < 50 ? 'good' : v < 200 ? 'fair' : 'poor';
  }
  function rttText(r) { return r && r.samples ? (r.avg || r.last).toFixed(r.avg < 10 ? 2 : 1) + ' ms' : '—'; }
  function rttBadge(r) {
    var g = rttGrade(r);
    return g ? UI.badge(rttText(r) + ' · ' + t('lm.' + g), g === 'good' ? 'ok' : g === 'fair' ? 'warn' : 'danger', true) : h('span.muted', '—');
  }
  function cut(s, n) { s = String(s == null ? '' : s); return s.length > n ? s.slice(0, n - 1) + '…' : s; }

  // ---- Status helpers / 状态辅助 ----
  // peers folds the links of a status by peer: their identity, bytes summed, and the latency of the link with the
  // most samples. / peers 按对端合并状态中的连接：身份、字节数之和，以及样本最多的连接的延迟。
  function peers(links) {
    var out = {};
    (links || []).forEach(function (c) {
      var p = out[c.id] || (out[c.id] = { id: c.id, role: c.role, name: c.name || c.hostname || c.id, conns: [], in: 0, out: 0, rtt: null });
      var l = c.link || {};
      p.conns.push(c);
      p.in += l.bytes_in || 0; p.out += l.bytes_out || 0;
      if (l.rtt && l.rtt.samples && (!p.rtt || l.rtt.samples > p.rtt.samples)) p.rtt = l.rtt;
    });
    return out;
  }
  // sum adds up routes by fn(route) → key. / sum 按 fn(route) 的键汇总路由。
  function sum(routes, fn) {
    var out = {};
    (routes || []).forEach(function (r) {
      var k = fn(r), s = out[k] || (out[k] = { calls: 0, errors: 0, inflight: 0, req: 0, resp: 0 });
      s.calls += r.calls || 0; s.errors += r.errors || 0; s.inflight += r.inflight || 0;
      s.req += r.req_bytes || 0; s.resp += r.resp_bytes || 0;
    });
    return out;
  }

  // ---- Graph / 图 ----
  function graph(cols) {
    var g = { cols: cols, nodes: [], links: [], byId: {} };
    g.node = function (id, col, p) {
      var x = g.byId[id];
      if (!x) { x = g.byId[id] = { id: id, col: col, kind: 'node', label: id, sub: '', facts: [] }; g.nodes.push(x); }
      Object.keys(p || {}).forEach(function (k) { x[k] = p[k]; });
      return x;
    };
    g.link = function (from, to, p) {
      var key = from + '\n' + to, l = null;
      for (var i = 0; i < g.links.length; i++) if (g.links[i].key === key) l = g.links[i];
      if (!l) { l = { key: key, from: from, to: to, fwd: null, back: null }; g.links.push(l); }
      Object.keys(p || {}).forEach(function (k) { l[k] = p[k]; });
      return l;
    };
    return g;
  }

  // layout orders each column to keep the lines from crossing (a few sweeps of the barycenter heuristic) and gives
  // every box its place. / layout 对每列排序以减少连线交叉（几轮重心法），并为每个方框确定位置。
  var BW = 176, BH = 50, ROW = 78, GAP = 200;
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
    var maxK = Math.max.apply(null, [1].concat(cols.map(function (c) { return c.length; })));
    g.W = Math.max(760, n * BW + (n - 1) * GAP + 32); g.H = 50 + maxK * ROW + 10;
    var pad = BW / 2 + 16;
    cols.forEach(function (c, ci) {
      c.forEach(function (x, i) {
        x.x = n === 1 ? g.W / 2 : pad + ci * (g.W - 2 * pad) / (n - 1);
        x.y = 50 + (maxK - c.length) * ROW / 2 + i * ROW + ROW / 2;
      });
    });
  }

  function s(tag, attrs, text) {
    var el = document.createElementNS(NS, tag);
    Object.keys(attrs || {}).forEach(function (k) { if (attrs[k] != null) el.setAttribute(k, attrs[k]); });
    if (text != null) el.textContent = text;
    return el;
  }

  // svg draws g. sel is the id of the box or the key of the line selected. / svg 绘制 g；sel 为选中方框的 ID 或连线的键。
  function svg(g, sel, onSelect) {
    layout(g);
    var root = s('svg', { 'class': 'lm-svg', viewBox: '0 0 ' + g.W + ' ' + g.H, width: g.W, height: g.H, role: 'img' });
    g.cols.forEach(function (c, i) {
      var x = g.nodes.filter(function (v) { return v.col === i; })[0];
      var cx = x ? x.x : (g.cols.length === 1 ? g.W / 2 : (BW / 2 + 16) + i * (g.W - BW - 32) / (g.cols.length - 1));
      root.appendChild(s('text', { 'class': 'lm-colh', x: cx, y: 22, 'text-anchor': 'middle' }, c));
    });
    var max = 1;
    g.links.forEach(function (l) { max = Math.max(max, (l.rateF || 0) + (l.rateB || 0)); });
    var lines = s('g'), boxes = s('g');
    g.links.forEach(function (l) {
      var a = g.byId[l.from], b = g.byId[l.to];
      if (!a || !b) return;
      var x1 = a.x + BW / 2, x2 = b.x - BW / 2, dx = (x2 - x1) / 2;
      var d = 'M' + x1 + ' ' + a.y + 'C' + (x1 + dx) + ' ' + a.y + ',' + (x2 - dx) + ' ' + b.y + ',' + x2 + ' ' + b.y;
      var r = (l.rateF || 0) + (l.rateB || 0), busy = r > 0 || (l.inflight || 0) > 0;
      var heat = !busy ? 0 : r < 1024 ? 1 : Math.log(r + 1) / Math.log(max + 1) < .6 ? 2 : 3;
      var w = 1.5 + (busy ? 3.5 * Math.log(r + 1) / Math.log(max + 2) : 0);
      var grp = s('g', { 'class': 'lm-lk h' + heat + (busy ? ' busy' : ' idle') + (sel === l.key ? ' sel' : '') });
      grp.appendChild(s('title', null, a.label + ' → ' + b.label + (l.rtt && l.rtt.samples ? ' · ' + rttText(l.rtt) : '') +
        (l.fwd == null ? '' : ' · → ' + bytes(l.fwd) + ' ← ' + bytes(l.back))));
      grp.appendChild(s('path', { 'class': 'lm-hit', d: d }));
      grp.appendChild(s('path', { 'class': 'lm-base', d: d, 'stroke-width': w.toFixed(1) }));
      if (busy) grp.appendChild(s('path', { 'class': 'lm-flow', d: d, 'stroke-width': w.toFixed(1) }));
      var mx = (x1 + x2) / 2, my = (a.y + b.y) / 2;
      if (busy && l.fwd != null) grp.appendChild(s('text', { 'class': 'lm-rate', x: mx, y: my - 7, 'text-anchor': 'middle' }, '→ ' + rate(l.rateF || 0) + '  ← ' + rate(l.rateB || 0)));
      var gr = rttGrade(l.rtt);
      if (gr) grp.appendChild(s('text', { 'class': 'lm-rtt rtt-' + gr, x: mx, y: my + 15, 'text-anchor': 'middle' }, rttText(l.rtt)));
      grp.addEventListener('click', function () { onSelect(l.key); });
      lines.appendChild(grp);
    });
    g.nodes.forEach(function (x) {
      var grp = s('g', { 'class': 'lm-nd k-' + x.kind + (x.self ? ' self' : '') + (sel === x.id ? ' sel' : ''), transform: 'translate(' + (x.x - BW / 2) + ' ' + (x.y - BH / 2) + ')' });
      grp.appendChild(s('title', null, x.label + (x.sub ? '\n' + x.sub : '')));
      grp.appendChild(s('rect', { 'class': 'lm-tile', width: BW, height: BH, rx: 10 }));
      var ic = UI.icon(ICON[x.kind] || 'server');
      ic.setAttribute('x', 10); ic.setAttribute('y', 15); ic.setAttribute('width', 20); ic.setAttribute('height', 20);
      ic.setAttribute('class', 'lm-ico');
      grp.appendChild(ic);
      grp.appendChild(s('text', { 'class': 'lm-lb', x: 38, y: x.sub ? 21 : 30 }, cut(x.label, 19)));
      if (x.sub) grp.appendChild(s('text', { 'class': 'lm-sb', x: 38, y: 37 }, cut(x.sub, 24)));
      if (x.state) grp.appendChild(s('circle', { 'class': 'lm-st ' + x.state, cx: BW - 10, cy: 10, r: 4 }));
      if (x.self) grp.appendChild(s('text', { 'class': 'lm-tag', x: BW / 2, y: -6, 'text-anchor': 'middle' }, t('lm.this')));
      grp.addEventListener('click', function () { onSelect(x.id); });
      boxes.appendChild(grp);
    });
    root.appendChild(lines);
    root.appendChild(boxes);
    return root;
  }

  function linkFacts(g, l) {
    var a = g.byId[l.from], b = g.byId[l.to];
    return [[t('lm.from'), a ? a.label : l.from], [t('lm.to'), b ? b.label : l.to], [t('lm.rtt'), rttBadge(l.rtt)],
      l.fwd == null ? null : [t('lm.fwd'), bytes(l.fwd) + ' · ' + rate(l.rateF || 0)],
      l.fwd == null ? null : [t('lm.back'), bytes(l.back) + ' · ' + rate(l.rateB || 0)],
      l.calls != null ? [t('lm.calls'), t('lm.calls_v', { c: l.calls, e: l.errors || 0, i: l.inflight || 0 })] : null]
      .concat(l.facts || []);
  }

  function side(g, sel) {
    var x = g.byId[sel], l = null;
    g.links.forEach(function (v) { if (v.key === sel) l = v; });
    if (!x && !l) x = g.nodes.filter(function (v) { return v.self; })[0];
    if (!x && !l) return h('div.muted.small', t('lm.select'));
    if (l) return h('div.stack', h('b', (g.byId[l.from] || {}).label + ' → ' + (g.byId[l.to] || {}).label), UI.kv(linkFacts(g, l)));
    return h('div.stack', h('div.row', UI.icon(ICON[x.kind] || 'server'), h('b', x.label)),
      h('div.muted.small', t('lm.k.' + x.kind)), x.sub ? h('div.small', x.sub) : null, UI.kv(x.facts || []));
  }

  function table(g) {
    return UI.table([
      { label: t('lm.from'), render: function (l) { return (g.byId[l.from] || {}).label || l.from; } },
      { label: t('lm.to'), render: function (l) { return (g.byId[l.to] || {}).label || l.to; } },
      { label: t('lm.rtt'), render: function (l) { return rttBadge(l.rtt); } },
      { label: t('lm.fwd'), cls: 'num', render: function (l) { return l.fwd == null ? '—' : h('div', bytes(l.fwd), h('div.small.muted', rate(l.rateF || 0))); } },
      { label: t('lm.back'), cls: 'num', render: function (l) { return l.back == null ? '—' : h('div', bytes(l.back), h('div.small.muted', rate(l.rateB || 0))); } },
      { label: t('lm.calls'), cls: 'num', render: function (l) {
        if (l.calls == null) return '—';
        return h('div', String(l.calls), l.errors ? h('div.small', UI.badge(String(l.errors), 'danger')) : null,
          l.inflight ? h('div.small.muted', '▶ ' + l.inflight) : null);
      } }
    ], g.links, { empty: t('lm.none') });
  }

  // mount polls o.load() every o.interval (3s) and draws its graph; the rates of a line come from its bytes now and
  // at the poll before. / mount 每隔 o.interval（3 秒）调用 o.load() 并绘制其图；连线的速率由其本次与上次的字节数算出。
  function mount(el, o) {
    var prev = {}, sel = null, last = null;
    var note = h('div'), canvas = h('div.lm-canvas'), sideEl = h('aside.lm-side'), tbl = h('div');
    var legend = h('div.lm-legend.small.muted', t('lm.legend'), h('span.rtt-good', t('lm.good')), ' / ', h('span.rtt-fair', t('lm.fair')), ' / ', h('span.rtt-poor', t('lm.poor')));
    UI.mount(el, h('div.stack', note,
      UI.card(t('lm.map'), h('div.stack', legend, h('div.lm-body', canvas, sideEl)), UI.btn(t('refresh'), 'refresh', function () { load(); }, 'sm')),
      UI.card(t('lm.links'), tbl, null, true)));
    function draw(g) {
      last = g;
      // Keep where the map was scrolled to across redraws. / 重绘时保持地图的滚动位置。
      var left = canvas.scrollLeft, top = canvas.scrollTop;
      UI.mount(canvas, svg(g, sel, function (k) { sel = k; draw(last); }));
      canvas.scrollLeft = left; canvas.scrollTop = top;
      UI.mount(sideEl, side(g, sel));
      UI.mount(tbl, table(g));
    }
    function load() {
      return o.load().then(function (g) {
        var at = g.at || Date.now(), next = {};
        g.links.forEach(function (l) {
          if (l.fwd == null) return;
          var p = prev[l.key], dt = p ? (at - p.at) / 1000 : 0;
          l.rateF = dt > 0 && l.fwd >= p.fwd ? (l.fwd - p.fwd) / dt : 0;
          l.rateB = dt > 0 && l.back >= p.back ? (l.back - p.back) / dt : 0;
          next[l.key] = { fwd: l.fwd, back: l.back, at: at };
        });
        prev = next;
        UI.mount(note);
        draw(g);
      }).catch(function (e) {
        if (e && e.status === 401) return;
        if (!last) UI.fail(e);
        else UI.mount(note, h('div.callout.callout--warn', t('lm.stale', { m: e && e.message ? e.message : String(e) })));
      });
    }
    load();
    UI.every(o.interval || 3000, function () { if (document.body.contains(el)) load(); });
    return { refresh: load };
  }

  window.LinkMap = {
    graph: graph, mount: mount, peers: peers, sum: sum, svg: svg,
    bytes: bytes, rate: rate, rttGrade: rttGrade, rttText: rttText, rttBadge: rttBadge
  };
})();
