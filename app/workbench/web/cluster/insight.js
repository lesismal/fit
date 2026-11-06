// Cluster → Metrics, Logs and Certificates (I150), and the Overview's sparklines.
//   Metrics       #/cluster/metrics?m=<metric>&node=a,b&points=90: the series the manager keeps from each poll
//                 (GET /api/m/cluster/metrics): requests, 4xx and 5xx a second, connections, bytes queued for clients
//                 and reloads, a chart with a line per member and one with a line per group (its members summed), and a
//                 table of the members with a sparkline and their latest counters; read again as often as the manager polls
//   Logs          #/cluster/logs?node=a,b&level=warn&logger=tls&contains=x&follow=1: the logs of the members chosen, merged
//                 by time (GET /api/m/cluster/logs, from each member's ring); followed every 2s with the cursor the manager
//                 answers; at most MAX_LINES lines kept on the page; a member that cannot be read is said apart
//   Certificates  #/cluster/certificates?within=30: every member's certificates, the soonest to expire first
//                 (GET /api/m/cluster/certificates), with their names, issuer and days left; members not read listed apart
//   Overview      a sparkline of each member's requests a second (or its connections, when it counts no requests) beside
//                 its state, from one read of the series every poll interval (FitCluster.rowExtra)
// The series and the inventory are any role's to read, as the members' states are; a member's logs need a role on it,
// and the Logs page offers only those members. Needs shared.js and groups.js (FC.groups); before cluster.js.
//
// 集群 → 指标、日志与证书（I150），以及概览中的迷你走势图。
//   指标  #/cluster/metrics?m=<指标>&node=a,b&points=90：管理端从每次轮询保存的序列（GET /api/m/cluster/metrics）：每秒请求、
//         4xx 与 5xx、连接数、为客户端排队的字节数与重新加载次数；一张图每个成员一条线，一张图每个分组一条线（其成员之和），以及
//         成员表（迷你走势图与最新计数）；按管理端的轮询频率重新读取
//   日志  #/cluster/logs?node=a,b&level=warn&logger=tls&contains=x&follow=1：所选成员的日志按时间合并（GET /api/m/cluster/logs，
//         来自各成员的日志环）；跟随时每 2 秒带着管理端应答的游标读取；页面最多保留 MAX_LINES 行；无法读取的成员单独说明
//   证书  #/cluster/certificates?within=30：各成员的证书，最早到期的在前（GET /api/m/cluster/certificates），含名称、签发者与剩余
//         天数；尚未读取的成员单独列出
//   概览  每个成员状态旁的每秒请求迷你走势图（成员不统计请求时为连接数），每个轮询间隔读取一次序列（FitCluster.rowExtra）
// 序列与证书清单任何角色都可读取，与成员状态相同；成员的日志需要在该成员上有角色，日志页只提供这些成员。依赖 shared.js 与
// groups.js（FC.groups）；在 cluster.js 之前加载。
(function () {
  'use strict';
  var FC = window.FitCluster;
  FC.WORDS.push(
    ['n.metrics', 'Metrics', '指标'], ['n.logs', 'Logs', '日志'], ['n.certs', 'Certificates', '证书'],
    // Metrics / 指标
    ['mx.requests', 'Requests a second', '每秒请求'], ['mx.errors_4xx', '4xx a second', '每秒 4xx'], ['mx.errors_5xx', '5xx a second', '每秒 5xx'],
    ['mx.connections', 'Connections', '连接'], ['mx.pending_bytes', 'Bytes queued for clients', '为客户端排队的字节'],
    ['mx.reloads', 'Configs applied', '应用的配置'], ['mx.reloads_failed', 'Configs that failed', '失败的配置'],
    ['mx.metric', 'Metric', '指标'], ['mx.window', 'Window', '时间范围'], ['mx.minutes', 'last {n} min', '最近 {n} 分钟'],
    ['mx.members', 'By member', '按成员'], ['mx.groups', 'By group (members summed)', '按分组（成员之和）'],
    ['mx.filter', 'Members: name, or a,b', '成员：名称，或 a,b'],
    ['mx.first', 'The first {n} of {all} members are drawn: filter by name to see others.', '只绘制了 {all} 个成员中的前 {n} 个：按名称筛选可查看其他成员。'],
    ['mx.intro', 'From each poll of the members, every {s}s: rates are over the time since the poll before; a gap is a member down, restarted, or one that counts no requests (its config sets no metrics).',
      '来自对成员的每次轮询（每 {s} 秒）：速率为与上次轮询之间的平均；断开处表示成员离线、重启，或不统计请求（其配置未设置 metrics）。'],
    ['mx.none', 'No members yet.', '还没有成员。'], ['mx.nodata', 'Nothing read yet.', '尚无数据。'],
    ['mx.member', 'Member', '成员'], ['mx.trend', 'Trend', '走势'], ['mx.now', 'Now', '当前'], ['mx.total', 'Requests since start', '启动以来的请求'],
    ['mx.total5', '5xx since start', '启动以来的 5xx'], ['mx.nocount', 'not counted', '未统计'],
    ['mx.persec', '{v}/s', '{v}/秒'],
    // Logs / 日志
    ['lg.members', 'Members', '成员'], ['lg.group', 'Pick a group', '选择分组'], ['lg.all', 'All', '全部'], ['lg.none', 'None', '无'],
    ['lg.level', 'Least level', '最低级别'], ['lg.any', 'Any level', '任意级别'], ['lg.logger', 'Logger, as server or tls', '日志器，如 server 或 tls'],
    ['lg.contains', 'Text', '文本'], ['lg.follow', 'Follow', '跟随'], ['lg.clear', 'Clear', '清空'], ['lg.read', 'Read', '读取'],
    ['lg.pick', 'Choose the members whose logs to read.', '请选择要读取日志的成员。'],
    ['lg.max', 'At most {n} members at once.', '一次最多 {n} 个成员。'],
    ['lg.empty', 'No line matches yet.', '暂无匹配的日志行。'],
    ['lg.kept', '{n} lines; the page keeps the latest {max}.', '{n} 行；页面保留最新的 {max} 行。'],
    ['lg.following', 'Following: read every 2s.', '跟随中：每 2 秒读取一次。'],
    ['lg.failed', '{name} could not be read: {why}', '无法读取 {name}：{why}'],
    ['lg.intro', 'Each member keeps its latest 1,000 messages in memory; the manager reads them from the members chosen at once and merges them by time.',
      '每个成员在内存中保留最新的 1,000 条消息；管理端同时从所选成员读取并按时间合并。'],
    ['lg.time', 'Time', '时间'],
    // Certificates / 证书
    ['ce.within', 'Expiring', '到期'], ['ce.all', 'Any time', '任意时间'], ['ce.days', 'within {n} days', '{n} 天内'],
    ['ce.search', 'Member, name or issuer', '成员、名称或签发者'], ['ce.left', 'Left', '剩余'], ['ce.expires', 'Expires', '到期时间'],
    ['ce.member', 'Member', '成员'], ['ce.listener', 'Listener', '监听器'], ['ce.names', 'Names', '名称'], ['ce.issuer', 'Issuer', '签发者'],
    ['ce.serial', 'Serial', '序列号'], ['ce.read', 'Read', '读取于'],
    ['ce.none', 'No member serves a certificate, as last read.', '按最近一次读取，没有成员提供证书。'],
    ['ce.nomatch', 'No certificate matches the filter.', '没有符合筛选条件的证书。'],
    ['ce.unread', 'Not read yet: {names}', '尚未读取：{names}'],
    ['ce.nocert', 'Serving no certificate: {names}', '未提供证书：{names}'],
    ['ce.intro', 'The certificates each member\'s listeners serve, as its latest poll read them, the soonest to expire first.',
      '各成员监听器提供的证书（按其最近一次轮询读取），最早到期的在前。'],
    ['ce.count', '{n} certificates on {m} members', '{m} 个成员上的 {n} 张证书']
  );

  var METRICS = ['requests', 'errors_4xx', 'errors_5xx', 'connections', 'pending_bytes', 'reloads', 'reloads_failed'];
  var RATES = { requests: 1, errors_4xx: 1, errors_5xx: 1 };
  var MAX_LINES_DRAWN = 12;
  var MAX_LINES = 2000;
  var MAX_TAIL_NODES = 16;

  // query reads the hash's query, as overview.js does. / 读取哈希中的查询参数，与 overview.js 相同。
  function query() {
    var q = {}, i = location.hash.indexOf('?');
    if (i < 0) return q;
    location.hash.slice(i + 1).split('&').forEach(function (p) {
      var j = p.indexOf('='), k = decodeURIComponent(j < 0 ? p : p.slice(0, j)), v = j < 0 ? '' : decodeURIComponent(p.slice(j + 1).replace(/\+/g, ' '));
      if (k) q[k] = v;
    });
    return q;
  }
  function setQuery(page, q) {
    var parts = Object.keys(q).filter(function (k) { return q[k] !== '' && q[k] != null && q[k] !== false; }).map(function (k) {
      return encodeURIComponent(k) + '=' + encodeURIComponent(q[k] === true ? '1' : q[k]);
    });
    try { history.replaceState(null, '', '#/cluster/' + page + (parts.length ? '?' + parts.join('&') : '')); } catch (e) { /* ignore / 忽略 */ }
  }
  function list(s) { return String(s || '').split(',').map(function (x) { return x.trim(); }).filter(Boolean); }

  // PALETTE tells lines apart in light and dark themes alike; palette(names) gives each name of a sorted list its
  // colour, so that a member keeps its colour while the members do not change. / PALETTE 在浅色与深色主题下都能区分折线；
  // palette(names) 按排序后的名称列表为每个名称分配颜色，成员不变时颜色不变。
  var PALETTE = ['#6e6ef0', '#0b8ad9', '#18a058', '#e09400', '#e5484d', '#9b5de5', '#14b8a6', '#d6336c', '#84cc16', '#f97316', '#06b6d4', '#64748b'];
  var colours = {};
  function palette(names) {
    names.slice().sort().forEach(function (n, i) { colours[n] = PALETTE[i % PALETTE.length]; });
  }
  function colour(name) { return colours[name] || PALETTE[0]; }
  function fmtBytes(n) {
    if (n == null) return '—';
    var u = ['B', 'KiB', 'MiB', 'GiB', 'TiB'], i = 0;
    while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
    return (i ? n.toFixed(n < 10 ? 1 : 0) : String(Math.round(n))) + ' ' + u[i];
  }
  // fmt formats a value of metric m. / 按指标 m 格式化数值。
  function fmt(UI, m, v) {
    if (v == null) return '—';
    if (m === 'pending_bytes') return fmtBytes(v);
    if (RATES[m]) return UI.t('mx.persec', { v: v < 10 ? (+v).toFixed(2) : (+v).toFixed(v < 100 ? 1 : 0) });
    return String(Math.round(v));
  }
  // clock is a time of day, 24-hour, to the millisecond. / 24 小时制的时刻，精确到毫秒。
  function clock(d) {
    function p(n, w) { return String(n).padStart(w || 2, '0'); }
    return p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds()) + '.' + p(d.getMilliseconds(), 3);
  }
  function last(arr) {
    for (var i = (arr || []).length - 1; i >= 0; i--) if (arr[i] != null) return arr[i];
    return null;
  }

  // ---- Charts / 图表 ----
  var SVG = 'http://www.w3.org/2000/svg';
  function svg(tag, attrs) {
    var el = document.createElementNS(SVG, tag);
    Object.keys(attrs || {}).forEach(function (k) { el.setAttribute(k, attrs[k]); });
    return el;
  }
  // path is the SVG path of a line, broken where a value is null. / 折线的 SVG 路径，值为 null 处断开。
  function path(ts, vs, x, y) {
    var d = '', pen = false;
    for (var i = 0; i < ts.length; i++) {
      if (vs[i] == null) { pen = false; continue; }
      d += (pen ? 'L' : 'M') + x(ts[i]).toFixed(1) + ' ' + y(vs[i]).toFixed(1);
      pen = true;
    }
    return d;
  }
  // chart draws lines [{name, colour, t: [ms], v: [number|null]}] over a shared time axis, with a legend and the values
  // under the pointer. / chart 在共同的时间轴上绘制折线，带图例与指针处的数值。
  function chart(UI, lines, metric, o) {
    var h = UI.h, W = 640, H = (o && o.height) || 180, L = 56, R = 8, T = 10, B = 22;
    var t0 = Infinity, t1 = -Infinity, vmax = 0;
    lines.forEach(function (ln) {
      ln.t.forEach(function (t, i) {
        t0 = Math.min(t0, t); t1 = Math.max(t1, t);
        if (ln.v[i] != null) vmax = Math.max(vmax, ln.v[i]);
      });
    });
    if (!isFinite(t0)) return UI.empty(UI.t('mx.nodata'), 'activity');
    if (t1 === t0) t1 = t0 + 1;
    vmax = vmax > 0 ? vmax * 1.1 : 1;
    function x(t) { return L + (t - t0) / (t1 - t0) * (W - L - R); }
    function y(v) { return T + (1 - v / vmax) * (H - T - B); }
    var root = svg('svg', { viewBox: '0 0 ' + W + ' ' + H, class: 'ci-chart', role: 'img', 'aria-label': UI.t('mx.' + metric) });
    [0, 0.5, 1].forEach(function (f) {
      var yy = y(vmax * f);
      root.appendChild(svg('line', { x1: L, x2: W - R, y1: yy, y2: yy, class: 'ci-grid' }));
      var lab = svg('text', { x: L - 6, y: yy + 4, class: 'ci-axis', 'text-anchor': 'end' });
      lab.textContent = metric === 'pending_bytes' ? fmtBytes(vmax * f) : String(+(vmax * f).toPrecision(3));
      root.appendChild(lab);
    });
    [[t0, 'start'], [t1, 'end']].forEach(function (p) {
      var lab = svg('text', { x: x(p[0]), y: H - 6, class: 'ci-axis', 'text-anchor': p[1] });
      lab.textContent = new Date(p[0]).toLocaleTimeString();
      root.appendChild(lab);
    });
    lines.forEach(function (ln) {
      var p = svg('path', { d: path(ln.t, ln.v, x, y), class: 'ci-line', stroke: ln.colour });
      var title = svg('title');
      title.textContent = ln.name;
      p.appendChild(title);
      root.appendChild(p);
    });
    var cursor = svg('line', { x1: 0, x2: 0, y1: T, y2: H - B, class: 'ci-cursor', visibility: 'hidden' });
    root.appendChild(cursor);
    var tip = h('div.ci-tip', { hidden: true });
    var wrap = h('div.ci-wrap', root, tip);
    root.addEventListener('mousemove', function (e) {
      var box = root.getBoundingClientRect(), px = (e.clientX - box.left) / box.width * W;
      if (px < L) { cursor.setAttribute('visibility', 'hidden'); tip.hidden = true; return; }
      var at = t0 + (px - L) / (W - L - R) * (t1 - t0), rows = [];
      lines.forEach(function (ln) {
        var best = -1;
        ln.t.forEach(function (t, i) { if (best < 0 || Math.abs(t - at) < Math.abs(ln.t[best] - at)) best = i; });
        if (best >= 0) rows.push([ln, ln.v[best], ln.t[best]]);
      });
      if (!rows.length) return;
      cursor.setAttribute('x1', x(rows[0][2])); cursor.setAttribute('x2', x(rows[0][2]));
      cursor.setAttribute('visibility', 'visible');
      UI.mount(tip, h('div.small.muted', new Date(rows[0][2]).toLocaleTimeString()), rows.slice(0, 14).map(function (r) {
        return h('div.ci-tip__row', h('span.ci-swatch', { style: { background: r[0].colour } }), h('span', r[0].name), h('b', fmt(UI, metric, r[1])));
      }));
      tip.hidden = false;
      tip.style.left = Math.min(box.width - 180, Math.max(0, e.clientX - box.left + 12)) + 'px';
    });
    root.addEventListener('mouseleave', function () { cursor.setAttribute('visibility', 'hidden'); tip.hidden = true; });
    var legend = h('div.ci-legend', lines.map(function (ln) {
      return h('span.ci-legend__item', h('span.ci-swatch', { style: { background: ln.colour } }), ln.name);
    }));
    return h('div.ci-chartbox', wrap, legend);
  }
  // sparkline is a small line of values. / 由数值组成的小折线。
  function sparkline(UI, vs, colourOf, label) {
    var W = 84, H = 22, vmax = 0, n = vs.length;
    vs.forEach(function (v) { if (v != null) vmax = Math.max(vmax, v); });
    vmax = vmax > 0 ? vmax : 1;
    var root = svg('svg', { viewBox: '0 0 ' + W + ' ' + H, width: W, height: H, class: 'ci-spark', role: 'img', 'aria-label': label || '' });
    var ts = vs.map(function (_, i) { return i; });
    root.appendChild(svg('path', { d: path(ts, vs, function (i) { return n > 1 ? i / (n - 1) * (W - 2) + 1 : W / 2; }, function (v) { return 1 + (1 - v / vmax) * (H - 2); }),
      class: 'ci-line', stroke: colourOf || 'currentColor' }));
    if (label) { var tt = svg('title'); tt.textContent = label; root.appendChild(tt); }
    return root;
  }
  FC.chart = chart;
  FC.sparkline = sparkline;

  // ---- Metrics / 指标 ----
  FC.pages.metrics = function (UI, sh) {
    var h = UI.h, t = UI.t;
    var el = sh.page('metrics', t('n.metrics'));
    var q = query();
    var f = { m: METRICS.indexOf(q.m) >= 0 ? q.m : 'requests', node: q.node || '', points: +q.points || 90 };
    var data = null, busy = false, every = 0, timer = 0;
    var metric = h('select.select.select--sm', { 'aria-label': t('mx.metric') }, METRICS.map(function (m) {
      return h('option', { value: m, selected: m === f.m }, t('mx.' + m));
    }));
    var win = h('select.select.select--sm', { 'aria-label': t('mx.window') });
    var filter = h('input.input.input--sm', { type: 'search', placeholder: t('mx.filter'), 'aria-label': t('mx.filter'), value: f.node });
    function windows() {
      var iv = (data && data.interval_ms) || 10000, max = (data && data.points_max) || 360;
      var opts = [30, 90, 180, max].filter(function (n, i, a) { return n <= max && a.indexOf(n) === i; });
      if (opts.indexOf(f.points) < 0) opts.push(f.points);
      UI.mount(win, opts.sort(function (a, b) { return a - b; }).map(function (n) {
        return h('option', { value: n, selected: n === f.points }, t('mx.minutes', { n: Math.max(1, Math.round(n * iv / 60000)) }));
      }));
    }
    function changed(reread) {
      f = { m: metric.value, node: filter.value.trim(), points: +win.value || f.points };
      setQuery('metrics', { m: f.m === 'requests' ? '' : f.m, node: f.node, points: f.points === 90 ? '' : f.points });
      if (reread) read(); else draw();
    }
    metric.addEventListener('change', function () { changed(false); });
    filter.addEventListener('input', function () { changed(false); });
    win.addEventListener('change', function () { changed(true); });
    var intro = h('div.small.muted'), body = h('div.stack');
    UI.mount(sh.tools, UI.btn(t('refresh'), 'refresh', function () { read(); }, 'ghost sm'));
    UI.mount(el, h('div.stack', intro, h('div.cl-filter', metric, win, filter), body));
    UI.mount(body, h('div.row', h('span.spinner'), h('span.muted', t('loading'))));

    function matches(name) {
      var terms = list(f.node);
      if (!terms.length) return true;
      if (terms.length > 1) return terms.indexOf(name) >= 0;
      return name.toLowerCase().indexOf(terms[0].toLowerCase()) >= 0;
    }
    function lineOf(name, s, key) { return { name: name, colour: colour(key || name), t: s.t || [], v: s[f.m] || [] }; }
    function draw() {
      if (!data || !document.body.contains(el)) return;
      intro.textContent = t('mx.intro', { s: Math.round((data.interval_ms || 10000) / 1000) });
      palette((data.nodes || []).map(function (n) { return n.name; }).concat((data.groups || []).map(function (g) { return 'group:' + g.name; })));
      var nodes = (data.nodes || []).filter(function (n) { return matches(n.name); });
      if (!(data.nodes || []).length) { UI.mount(body, UI.empty(t('mx.none'), 'server')); return; }
      var drawn = nodes.slice(0, MAX_LINES_DRAWN);
      var groups = data.groups || [];
      UI.mount(body,
        UI.card(t('mx.members') + ' · ' + t('mx.' + f.m), h('div.stack', chart(UI, drawn.map(function (n) { return lineOf(n.name, n.series); }), f.m),
          nodes.length > drawn.length ? h('div.small.muted', t('mx.first', { n: drawn.length, all: nodes.length })) : null)),
        groups.length ? UI.card(t('mx.groups') + ' · ' + t('mx.' + f.m), chart(UI, groups.map(function (g) { return lineOf(g.name, g.series, 'group:' + g.name); }), f.m)) : null,
        UI.card(null, UI.table([
          { label: t('mx.member'), render: function (n) {
            return h('div', h('a', { href: '#/cluster/node/' + encodeURIComponent(n.name) }, h('b', n.name)), ' ', FC.stateBadge(UI, n.status));
          } },
          { label: t('mx.trend'), render: function (n) { return sparkline(UI, (n.series[f.m] || []).slice(-30), colour(n.name), t('mx.' + f.m)); } },
          { label: t('mx.now'), cls: 'num', render: function (n) { return fmt(UI, f.m, last(n.series[f.m])); } },
          { label: t('mx.connections'), cls: 'num', render: function (n) { return fmt(UI, 'connections', last(n.series.connections)); } },
          { label: t('mx.total'), cls: 'num', render: function (n) {
            var r = n.counters && n.counters.requests;
            return r ? String(r.total) : h('span.muted.small', t('mx.nocount'));
          } },
          { label: t('mx.total5'), cls: 'num', render: function (n) {
            var r = n.counters && n.counters.requests;
            return r ? String((r.classes || {})['5xx'] || 0) : '—';
          } }
        ], nodes, { onRowClick: function (n) { filter.value = n.name; changed(false); } }), null, true));
    }
    function read() {
      if (busy) return;
      busy = true;
      FC.call(UI, 'GET', '/api/metrics?points=' + f.points).then(function (r) {
        busy = false;
        data = r.body || {};
        windows();
        var want = Math.min(10000, Math.max(2000, data.interval_ms || 10000));
        if (want !== every) {
          every = want;
          if (timer) clearInterval(timer);
          timer = UI.every(every, function () { if (document.body.contains(el)) read(); });
        }
        draw();
      }, function (e) {
        busy = false;
        if (!document.body.contains(el) || e.status === 401) return;
        UI.mount(body, h('div.callout.callout--danger', e.message));
      });
    }
    windows();
    read();
  };

  // ---- Logs / 日志 ----
  var LEVELS = ['debug', 'info', 'warn', 'error'];
  var LEVEL_KIND = { error: 'danger', warn: 'warn', info: 'info', debug: '' };
  FC.pages.logs = function (UI, sh) {
    var h = UI.h, t = UI.t;
    var el = sh.page('logs', t('n.logs'));
    var q = query();
    var chosen = list(q.node), cursor = '', lines = [], busy = false, timer = 0, failures = {};
    var level = h('select.select.select--sm', { 'aria-label': t('lg.level') }, h('option', { value: '' }, t('lg.any')), LEVELS.map(function (l) {
      return h('option', { value: l, selected: l === q.level }, l);
    }));
    var logger = h('input.input.input--sm', { type: 'search', placeholder: t('lg.logger'), 'aria-label': t('lg.logger'), value: q.logger || '', spellcheck: 'false' });
    var contains = h('input.input.input--sm', { type: 'search', placeholder: t('lg.contains'), 'aria-label': t('lg.contains'), value: q.contains || '' });
    var follow = h('input', { type: 'checkbox', checked: q.follow === '1', 'aria-label': t('lg.follow') });
    var picker = h('div.ci-picker'), groupSel = h('select.select.select--sm', { 'aria-label': t('lg.group') }, h('option', { value: '' }, t('lg.group')));
    var out = h('div.ci-log', { role: 'log', 'aria-live': 'polite' }), note = h('div.small.muted'), problems = h('div.stack');
    var members = [];
    UI.mount(sh.tools, UI.btn(t('lg.clear'), 'x', function () { lines = []; drawLines(); }, 'ghost sm'),
      UI.btn(t('lg.read'), 'refresh', function () { restart(); }, 'ghost sm'));
    UI.mount(el, h('div.stack', h('div.small.muted', t('lg.intro')),
      UI.card(t('lg.members'), h('div.stack', h('div.row', groupSel,
        UI.btn(t('lg.all'), 'check', function () { chosen = members.slice(0, MAX_TAIL_NODES); drawPicker(); restart(); }, 'ghost sm'),
        UI.btn(t('lg.none'), 'x', function () { chosen = []; drawPicker(); restart(); }, 'ghost sm')), picker)),
      h('div.cl-filter', level, logger, contains, h('label.row', follow, ' ', t('lg.follow'))), problems, note, out));

    function save() {
      setQuery('logs', { node: chosen.join(','), level: level.value, logger: logger.value.trim(), contains: contains.value.trim(), follow: follow.checked });
    }
    function drawPicker() {
      UI.mount(picker, members.length ? members.map(function (name) {
        var box = h('input', { type: 'checkbox', checked: chosen.indexOf(name) >= 0 });
        box.addEventListener('change', function () {
          if (box.checked && chosen.length >= MAX_TAIL_NODES) { box.checked = false; UI.toast(t('lg.max', { n: MAX_TAIL_NODES }), 'warn'); return; }
          chosen = box.checked ? chosen.concat([name]) : chosen.filter(function (x) { return x !== name; });
          restart();
        });
        return h('label.ci-chip', { style: { '--ci-colour': colour(name) } }, box, ' ', name);
      }) : h('span.muted.small', t('mx.none')));
    }
    function drawLines() {
      if (!chosen.length) { UI.mount(out, UI.empty(t('lg.pick'), 'log')); UI.mount(note, ''); return; }
      if (!lines.length) { UI.mount(out, UI.empty(t('lg.empty'), 'log')); } else {
        var stick = out.scrollTop + out.clientHeight >= out.scrollHeight - 8;
        UI.mount(out, lines.map(function (r) {
          var d = new Date(r.time);
          return h('div.ci-line-row', h('span.ci-time', { title: r.time }, isNaN(d) ? r.time : clock(d)),
            h('span.ci-node', { style: { color: colour(r.node) } }, r.node), UI.badge(r.level, LEVEL_KIND[r.level] || ''),
            h('span.ci-logger', r.logger), h('span.ci-msg', r.message));
        }));
        if (stick) out.scrollTop = out.scrollHeight;
      }
      UI.mount(note, t('lg.kept', { n: lines.length, max: MAX_LINES }), follow.checked ? ' · ' + t('lg.following') : '');
    }
    function drawProblems() {
      UI.mount(problems, Object.keys(failures).map(function (name) {
        var p = failures[name];
        return h('div.callout.callout--warn.small', t('lg.failed', { name: name, why: FC.kindWord(UI, p.kind) + ': ' + (p.message || '') }));
      }));
    }
    function read() {
      if (busy || !chosen.length || !document.body.contains(el)) return;
      busy = true;
      var qs = ['node=' + encodeURIComponent(chosen.join(','))];
      if (level.value) qs.push('level=' + level.value);
      if (logger.value.trim()) qs.push('logger=' + encodeURIComponent(logger.value.trim()));
      if (contains.value.trim()) qs.push('contains=' + encodeURIComponent(contains.value.trim()));
      if (cursor) qs.push('cursor=' + encodeURIComponent(cursor));
      var asked = chosen.join(',');
      FC.call(UI, 'GET', '/api/logs?' + qs.join('&')).then(function (r) {
        busy = false;
        if (!document.body.contains(el) || asked !== chosen.join(',')) return;
        var b = r.body || {};
        cursor = b.cursor || cursor;
        lines = lines.concat(b.records || []);
        if (lines.length > MAX_LINES) lines = lines.slice(lines.length - MAX_LINES);
        failures = {};
        (b.nodes || []).forEach(function (n) { if (n.error) failures[n.name] = n.error; });
        drawProblems();
        drawLines();
      }, function (e) {
        busy = false;
        if (!document.body.contains(el) || e.status === 401) return;
        UI.mount(problems, h('div.callout.callout--danger', e.message));
      });
    }
    function restart() {
      save();
      cursor = ''; lines = []; failures = {};
      drawProblems();
      drawLines();
      if (timer) { clearInterval(timer); timer = 0; }
      read();
      if (follow.checked) timer = UI.every(2000, read);
    }
    [level].forEach(function (x) { x.addEventListener('change', restart); });
    follow.addEventListener('change', restart);
    [logger, contains].forEach(function (x) {
      var wait = 0;
      x.addEventListener('input', function () { clearTimeout(wait); wait = setTimeout(restart, 400); });
    });
    groupSel.addEventListener('change', function () {
      var g = groupSel.value;
      groupSel.value = '';
      if (!g || !FC.groups) return;
      FC.groups(UI).then(function (d) {
        var gr = (d.groups || []).filter(function (x) { return x.name === g; })[0];
        if (!gr) return;
        chosen = (gr.nodes || []).filter(function (n) { return members.indexOf(n) >= 0; }).slice(0, MAX_TAIL_NODES);
        drawPicker();
        restart();
      }, UI.fail);
    });
    FC.members(UI).then(function (d) {
      // A member's logs need a role on it (I148): only those are offered. / 成员的日志需要在其上有角色（I148）：只提供这些成员。
      members = d.nodes.map(function (n) { return n.name; }).filter(function (n) { return FC.canOn('viewer', n); });
      palette(members);
      chosen = chosen.filter(function (n) { return members.indexOf(n) >= 0; }).slice(0, MAX_TAIL_NODES);
      drawPicker();
      restart();
    }, function (e) { UI.mount(picker, h('div.callout.callout--danger', e.message)); });
    if (FC.groups) {
      FC.groups(UI).then(function (d) {
        UI.mount(groupSel, h('option', { value: '' }, t('lg.group')), (d.groups || []).map(function (g) { return h('option', { value: g.name }, g.name); }));
      }, function () { /* ignore / 忽略 */ });
    }
    UI.mount(picker, h('div.row', h('span.spinner'), h('span.muted', t('loading'))));
  };

  // ---- Certificates / 证书 ----
  FC.pages.certificates = function (UI, sh) {
    var h = UI.h, t = UI.t;
    var el = sh.page('certificates', t('n.certs'));
    var q = query();
    var within = h('select.select.select--sm', { 'aria-label': t('ce.within') }, [['', t('ce.all')], ['7', t('ce.days', { n: 7 })], ['30', t('ce.days', { n: 30 })],
      ['90', t('ce.days', { n: 90 })]].map(function (o) { return h('option', { value: o[0], selected: o[0] === (q.within || '') }, o[1]); }));
    var text = h('input.input.input--sm', { type: 'search', placeholder: t('ce.search'), 'aria-label': t('ce.search'), value: q.q || '' });
    var body = h('div.stack'), data = null;
    UI.mount(sh.tools, UI.btn(t('refresh'), 'refresh', function () { read(); }, 'ghost sm'));
    UI.mount(el, h('div.stack', h('div.small.muted', t('ce.intro')), h('div.cl-filter', text, within), body));
    UI.mount(body, h('div.row', h('span.spinner'), h('span.muted', t('loading'))));
    function changed() { setQuery('certificates', { q: text.value.trim(), within: within.value }); draw(); }
    text.addEventListener('input', changed);
    within.addEventListener('change', changed);
    function draw() {
      if (!data || !document.body.contains(el)) return;
      var all = data.certificates || [], days = within.value === '' ? null : +within.value, txt = text.value.trim().toLowerCase();
      var rows = all.filter(function (c) {
        if (days != null && c.days_left > days) return false;
        return !txt || [c.node, c.issuer, c.listener].concat(c.names || []).join(' ').toLowerCase().indexOf(txt) >= 0;
      });
      var unread = (data.nodes || []).filter(function (n) { return !n.read; }).map(function (n) { return n.name; });
      var none = (data.nodes || []).filter(function (n) { return n.read && !n.count; }).map(function (n) { return n.name; });
      var members = {};
      all.forEach(function (c) { members[c.node] = 1; });
      UI.mount(body,
        h('div.small.muted', t('ce.count', { n: all.length, m: Object.keys(members).length })),
        !all.length ? UI.empty(t('ce.none'), 'shield') : !rows.length ? UI.empty(t('ce.nomatch'), 'shield') : UI.card(null, UI.table([
          { label: t('ce.left'), render: function (c) { return FC.expiry(UI, c.not_after); } },
          { label: t('ce.expires'), render: function (c) { return h('span.small.ci-nowrap', FC.when(c.not_after)); } },
          { label: t('ce.member'), render: function (c) {
            return h('div', h('a', { href: '#/cluster/node/' + encodeURIComponent(c.node) }, h('b', c.node)), ' ', c.status !== 'up' ? FC.stateBadge(UI, c.status) : null);
          } },
          { label: t('ce.listener'), render: function (c) { return c.listener || '—'; } },
          { label: t('ce.names'), render: function (c) { return (c.names || []).length ? h('div.ci-names', c.names.map(function (n) { return h('div', n); })) : '—'; } },
          { label: t('ce.issuer'), render: function (c) { return c.issuer || '—'; } },
          { label: t('ce.serial'), render: function (c) { return h('code.small.ci-serial', { title: c.serial || '' }, c.serial || '—'); } },
          { label: t('ce.read'), render: function (c) { return h('span.small.muted', c.read ? UI.ago(FC.sec(c.read)) : '—'); } }
        ], rows), null, true),
        none.length ? h('div.small.muted', t('ce.nocert', { names: none.join(', ') })) : null,
        unread.length ? h('div.small.muted', t('ce.unread', { names: unread.join(', ') })) : null);
    }
    function read() {
      FC.call(UI, 'GET', '/api/certificates').then(function (r) { data = r.body || {}; draw(); }, function (e) {
        if (!document.body.contains(el) || e.status === 401) return;
        UI.mount(body, h('div.callout.callout--danger', e.message));
      });
    }
    read();
    UI.every(30000, function () { if (document.body.contains(el)) read(); });
  };

  // ---- The Overview's sparklines / 概览的迷你走势图 ----
  // One read of the series a poll interval at most, shared by every card and row; each sparkline is a placeholder filled
  // when the read comes back, so that the overview, which draws again on its own, shows them at once.
  // 每个轮询间隔最多读取一次序列，供所有卡片与行共用；每个走势图先是占位，读取返回后填充，因此自行重绘的概览能立即显示。
  var spark = { at: 0, busy: false, by: {}, every: 10000 };
  function fillSparks(UI) {
    Array.prototype.forEach.call(document.querySelectorAll('.ci-sparkslot'), function (slot) {
      var s = spark.by[slot.getAttribute('data-node')];
      if (!s || slot.getAttribute('data-at') === String(spark.at)) return;
      slot.setAttribute('data-at', String(spark.at));
      var m = s.counts ? 'requests' : 'connections', vs = (s.series[m] || []).slice(-30);
      UI.mount(slot, sparkline(UI, vs, colour(slot.getAttribute('data-node')), UI.t('mx.' + m) + ': ' + fmt(UI, m, last(vs))));
    });
  }
  function readSparks(UI) {
    if (spark.busy || Date.now() - spark.at < spark.every) return;
    spark.busy = true;
    FC.call(UI, 'GET', '/api/metrics?points=30').then(function (r) {
      var b = r.body || {};
      spark.busy = false;
      spark.at = Date.now();
      spark.every = Math.min(10000, Math.max(2000, b.interval_ms || 10000));
      spark.by = {};
      palette((b.nodes || []).map(function (n) { return n.name; }));
      (b.nodes || []).forEach(function (n) { spark.by[n.name] = { series: n.series || {}, counts: !!(n.counters && n.counters.requests) }; });
      fillSparks(UI);
    }, function () { spark.busy = false; spark.at = Date.now(); });
  }
  FC.rowExtra(function (UI, mem, st) {
    // The overview's cards and rows alone: they pass the state without the member. / 只用于概览的卡片与行：它们只传入状态。
    if (mem || !st || !st.name || !document.querySelector('.cl-stats')) return null;
    var slot = UI.h('span.ci-sparkslot', { 'data-node': st.name });
    setTimeout(function () { readSparks(UI); fillSparks(UI); }, 0);
    return slot;
  });
})();
