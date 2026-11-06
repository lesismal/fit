// Cluster → Overview (I142): every member's state as the manager's poller last read it (GET /api/m/cluster/state),
// as cards or a table: its state coloured, how long it has been so and why, fit's version, the running config's
// version, the drift its consistency check counts, the certificate that expires first, the connections, upstream
// backends down and the latency of its GET /node. Filtered by a text, a label selector (env=prod, region, tier!=db)
// and a state, sorted, and read again as often as the poller reads, while the page is shown. A member in trouble has
// Ask AI (diagnose). The filter lives in the URL (#/cluster/overview?label=...&state=...), so that a label clicked
// anywhere opens the overview filtered by it. Groups (I144) add their filter here.
//
// 集群 → 概览（I142）：管理端轮询器最后一次读到的各成员状态（GET /api/m/cluster/state），以卡片或表格显示：着色的状态、
// 持续时长与原因、fit 版本、运行配置的版本、一致性检查统计的不一致数、最早到期的证书、连接数、不可用的上游后端以及其
// GET /node 的延迟。可按文本、标签选择器（env=prod, region, tier!=db）与状态筛选、排序；页面显示期间按轮询的频率重新读取。
// 出问题的成员带“问 AI”（diagnose）。筛选条件保存在 URL 中（#/cluster/overview?label=...&state=...），在任何地方点击标签
// 即打开按其筛选的概览。分组（I144）在此加入其筛选。
(function () {
  'use strict';
  var FC = window.FitCluster;
  FC.WORDS.push(
    ['ov.total', 'Members', '成员'], ['ov.cards', 'Cards', '卡片'], ['ov.table', 'Table', '表格'],
    ['ov.search', 'Name or URL', '名称或 URL'], ['ov.label', 'Labels: env=prod, region, tier!=db', '标签：env=prod, region, tier!=db'],
    ['ov.sort', 'Sort', '排序'], ['ov.sort.state', 'State, worst first', '状态（严重的在前）'], ['ov.sort.name', 'Name', '名称'],
    ['ov.sort.latency', 'Latency, slowest first', '延迟（慢的在前）'], ['ov.sort.cert', 'Certificate expiry, soonest first', '证书到期（早的在前）'],
    ['ov.sort.drift', 'Drift, most first', '不一致（多的在前）'], ['ov.sort.conns', 'Connections, most first', '连接（多的在前）'],
    ['ov.sort.version', 'fit version', 'fit 版本'],
    ['ov.none', 'No members yet: add the fits this one manages on the Nodes page.', '还没有成员：请在“节点”页添加本 fit 管理的其他 fit。'],
    ['ov.nomatch', 'No member matches the filter.', '没有符合筛选条件的成员。'], ['ov.clear', 'Clear the filter', '清除筛选'],
    ['ov.shown', '{n} of {all} shown', '显示 {all} 个中的 {n} 个'],
    ['ov.every', 'Read every {s}s, as the manager polls', '每 {s} 秒读取一次，与管理端轮询一致'],
    ['ov.open', 'Open', '打开'], ['ov.name', 'Member', '成员'], ['ov.state', 'State', '状态']
  );

  var VIEW_KEY = 'fit_cluster_view';
  function load(k) { try { return localStorage.getItem(k); } catch (e) { return null; } }
  function save(k, v) { try { localStorage.setItem(k, v); } catch (e) { /* ignore / 忽略 */ } }

  // query reads the hash's query: #/cluster/overview?label=...&state=... / 读取哈希中的查询参数。
  function query() {
    var q = {}, i = location.hash.indexOf('?');
    if (i < 0) return q;
    location.hash.slice(i + 1).split('&').forEach(function (p) {
      var j = p.indexOf('='), k = decodeURIComponent(j < 0 ? p : p.slice(0, j)), v = j < 0 ? '' : decodeURIComponent(p.slice(j + 1).replace(/\+/g, ' '));
      if (k) q[k] = v;
    });
    return q;
  }
  // selector reads a label selector: terms key=value, key!=value or key, separated by commas; every term must hold.
  // selector 读取标签选择器：以逗号分隔的 key=value、key!=value 或 key，每一项都须满足。
  function selector(s) {
    var terms = String(s || '').split(',').map(function (x) { return x.trim(); }).filter(Boolean).map(function (x) {
      var m = x.match(/^([^!=]+?)\s*(!=|=)\s*(.*)$/);
      return m ? { key: m[1].trim(), op: m[2], value: m[3].trim() } : { key: x, op: 'has' };
    });
    return function (labels) {
      labels = labels || {};
      return terms.every(function (tm) {
        var has = Object.prototype.hasOwnProperty.call(labels, tm.key);
        if (tm.op === 'has') return has;
        if (tm.op === '=') return has && labels[tm.key] === tm.value;
        return !has || labels[tm.key] !== tm.value;
      });
    };
  }
  // numbers of a fit version, for sorting: v1.2.3 → [1, 2, 3]. / 版本号的数字，用于排序。
  function vnums(v) { return String(v || '').replace(/^v/, '').split(/[.+-]/).map(function (x) { return parseInt(x, 10) || 0; }); }
  function cmpVersion(a, b) {
    var x = vnums(a), y = vnums(b);
    for (var i = 0; i < Math.max(x.length, y.length); i++) if ((x[i] || 0) !== (y[i] || 0)) return (x[i] || 0) - (y[i] || 0);
    return 0;
  }
  function expiryMs(st) {
    var ms = Date.parse((st.certificates && st.certificates.nearest_expiry) || '');
    return isNaN(ms) || ms <= 0 ? Infinity : ms;
  }
  var SORTS = {
    state: function (a, b) { return (FC.ORDER[a.status] - FC.ORDER[b.status]) || (b.failures || 0) - (a.failures || 0); },
    name: function () { return 0; },
    latency: function (a, b) { return (b.latency_ms || 0) - (a.latency_ms || 0); },
    cert: function (a, b) { return expiryMs(a) - expiryMs(b); },
    drift: function (a, b) { return ((b.drift && b.drift.count) || 0) - ((a.drift && a.drift.count) || 0); },
    conns: function (a, b) { return ((b.connections && b.connections.open) || 0) - ((a.connections && a.connections.open) || 0); },
    version: function (a, b) { return cmpVersion(a.node && a.node.fit_version, b.node && b.node.fit_version); }
  };

  FC.pages.overview = function (UI, sh) {
    var h = UI.h, t = UI.t;
    var el = sh.page('overview', t('n.overview'));
    var q = query();
    var f = { text: q.q || '', label: q.label || '', state: q.state || '', sort: q.sort && SORTS[q.sort] ? q.sort : 'state' };
    var view = load(VIEW_KEY) === 'table' ? 'table' : 'cards';
    var data = null, busy = false, timer = 0, every = 0;

    // The filter's controls; each change redraws and writes the URL, without a reload of the page.
    // 筛选控件：每次修改都重绘并写入 URL，不重新加载页面。
    var text = h('input.input.input--sm', { type: 'search', placeholder: t('ov.search'), 'aria-label': t('ov.search'), value: f.text });
    var label = h('input.input.input--sm.cl-sel', { type: 'search', placeholder: t('ov.label'), 'aria-label': t('ov.label'), value: f.label, spellcheck: 'false' });
    var state = h('select.select.select--sm', { 'aria-label': t('ov.state') },
      [['', t('st.all')], ['up', t('st.up')], ['down', t('st.down')], ['error', t('st.error')], ['unknown', t('st.unknown')]].map(function (o) {
        return h('option', { value: o[0], selected: o[0] === f.state }, o[1]);
      }));
    var sort = h('select.select.select--sm', { 'aria-label': t('ov.sort') },
      ['state', 'name', 'latency', 'cert', 'drift', 'conns', 'version'].map(function (k) {
        return h('option', { value: k, selected: k === f.sort }, t('ov.sort.' + k));
      }));
    function changed() {
      f = { text: text.value.trim(), label: label.value.trim(), state: state.value, sort: sort.value };
      var parts = [];
      if (f.text) parts.push('q=' + encodeURIComponent(f.text));
      if (f.label) parts.push('label=' + encodeURIComponent(f.label));
      if (f.state) parts.push('state=' + f.state);
      if (f.sort !== 'state') parts.push('sort=' + f.sort);
      try { history.replaceState(null, '', '#/cluster/overview' + (parts.length ? '?' + parts.join('&') : '')); } catch (e) { /* ignore / 忽略 */ }
      draw();
    }
    [text, label].forEach(function (x) { x.addEventListener('input', changed); });
    [state, sort].forEach(function (x) { x.addEventListener('change', changed); });
    var cardsBtn = UI.btn(t('ov.cards'), 'dashboard', function () { setView('cards'); }, 'ghost sm');
    var tableBtn = UI.btn(t('ov.table'), 'layers', function () { setView('table'); }, 'ghost sm');
    function setView(v) { view = v; save(VIEW_KEY, v); draw(); }

    var stats = h('div.grid.grid--4.cl-stats'), info = h('div.callout.callout--warn', { hidden: true });
    var list = h('div.cl-list'), note = h('div.small.muted');
    UI.mount(sh.tools, h('span.btn-row', cardsBtn, tableBtn), UI.btn(t('refresh'), 'refresh', function () { read(); }, 'ghost sm'));
    UI.mount(el, h('div.stack', info, stats, h('div.cl-filter', text, label, state, sort), list, note));
    UI.mount(list, h('div.row', h('span.spinner'), h('span.muted', t('loading'))));

    // shown are the members the filter keeps, sorted. / 筛选后保留的成员，已排序。
    function shown(nodes) {
      var sel = selector(f.label), txt = f.text.toLowerCase();
      var out = nodes.filter(function (s) {
        if (f.state && s.status !== f.state) return false;
        if (txt && (s.name + ' ' + s.url).toLowerCase().indexOf(txt) < 0) return false;
        return sel(s.labels);
      });
      var by = SORTS[f.sort] || SORTS.state;
      return out.sort(function (a, b) { return by(a, b) || (a.name < b.name ? -1 : a.name > b.name ? 1 : 0); });
    }
    function statTile(icon, key, n, kind, st) {
      var tile = UI.stat(icon, key === 'ov.total' ? t(key) : t('st.' + key), String(n), null, kind);
      tile.classList.add('cl-stat');
      tile.setAttribute('role', 'button'); tile.setAttribute('tabindex', '0');
      tile.title = key === 'ov.total' ? t('st.all') : t('st.' + key);
      function pick() { state.value = st; changed(); }
      tile.addEventListener('click', pick);
      tile.addEventListener('keydown', function (e) { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); pick(); } });
      if (f.state === st && st) tile.classList.add('is-active');
      return tile;
    }

    // facts are a member's facts, as the card and the row show them. / 卡片与表格行所示的成员信息。
    function facts(s) {
      var stale = s.status !== 'up' && s.seen ? h('div.small.muted', t('f.asof', { ago: UI.ago(FC.sec(s.seen)) })) : null;
      var drift = s.drift ? (s.drift.count ? UI.badge(t('f.drift.n', { n: s.drift.count }), 'warn') : UI.badge(t('f.drift.none'), 'ok')) : '—';
      var cert = s.certificates && s.certificates.nearest_expiry ? h('span.cl-cert-exp', FC.expiry(UI, s.certificates.nearest_expiry),
        ' ', h('span.small.muted', (s.certificates.nearest_names || []).join(', '))) : h('span.muted', t('f.nocert'));
      var down = s.upstreams && s.upstreams.backends_down ? UI.badge(t('f.backends.down', { n: s.upstreams.backends_down, all: s.upstreams.backends }), 'warn') : null;
      return {
        fit: s.node ? h('code', s.node.fit_version || '—') : '—',
        config: s.config ? h('span', 'v' + s.config.version, s.config.source ? h('span.small.muted', ' · ' + s.config.source) : null) : '—',
        drift: drift, cert: cert,
        conns: s.connections ? h('span', String(s.connections.open), down ? [' ', down] : null) : '—',
        latency: s.node ? t('f.ms', { n: s.latency_ms }) : '—', stale: stale
      };
    }
    function health(s) {
      return h('span.cl-health', FC.stateBadge(UI, s.status),
        h('span.small.muted', ' ' + t('f.since', { ago: UI.ago(FC.sec(s.since)) })),
        s.error ? h('span.cl-health__why', { title: s.error.message || '' }, FC.kindWord(UI, s.error.kind)) : null,
        FC.extras(UI, null, s));
    }
    function card(s) {
      var x = facts(s), link = '#/cluster/node/' + encodeURIComponent(s.name);
      return h('section.card.cl-card.cl-card--' + (FC.STATE_KIND[s.status] || 'muted'),
        h('div.card__head', h('h2.ellipsis', { title: s.name }, h('a', { href: link }, s.name)), FC.stateBadge(UI, s.status)),
        h('div.card__body',
          h('div.small.muted', t('f.since', { ago: UI.ago(FC.sec(s.since)) }),
            s.failures ? ' · ' + t('f.failures', { n: s.failures }) : '', FC.extras(UI, null, s)),
          h('div.small.muted.ellipsis', { title: s.url }, s.url),
          FC.labelChips(UI, s.labels),
          s.error ? FC.problemBox(UI, s.error, FC.askMember(UI, s)) : null,
          x.stale,
          UI.kv([[t('f.fit'), x.fit], [t('f.config'), x.config], [t('f.drift'), x.drift], [t('f.cert'), x.cert],
            [t('f.conns'), x.conns], [t('f.latency'), x.latency],
            [t('f.polled'), s.polled ? UI.ago(FC.sec(s.polled)) : t('st.unknown')]])));
    }
    function table(rows) {
      return UI.table([
        { label: t('ov.name'), render: function (s) {
          return h('div', h('a', { href: '#/cluster/node/' + encodeURIComponent(s.name) }, h('b', s.name)), FC.labelChips(UI, s.labels));
        } },
        { label: t('ov.state'), render: function (s) {
          var ask = FC.askMember(UI, s);
          return h('div', health(s), s.error ? h('div.small.muted.cl-msg', { title: s.error.message }, s.error.message) : null, ask);
        } },
        { label: t('f.fit'), render: function (s) { return facts(s).fit; } },
        { label: t('f.config'), render: function (s) { return facts(s).config; } },
        { label: t('f.drift'), render: function (s) { return facts(s).drift; } },
        { label: t('f.cert'), render: function (s) { return facts(s).cert; } },
        { label: t('f.conns'), cls: 'num', render: function (s) { return facts(s).conns; } },
        { label: t('f.latency'), cls: 'num', render: function (s) { return facts(s).latency; } }
      ], rows, { onRowClick: function (s) { location.hash = '#/cluster/node/' + encodeURIComponent(s.name); } });
    }

    function draw() {
      if (!data || !document.body.contains(el)) return;
      cardsBtn.classList.toggle('is-active', view === 'cards');
      tableBtn.classList.toggle('is-active', view === 'table');
      var nodes = data.nodes || [], c = { up: 0, down: 0, error: 0, unknown: 0 };
      nodes.forEach(function (s) { c[s.status in c ? s.status : 'unknown']++; });
      UI.mount(stats, statTile('server', 'ov.total', nodes.length, 'info', ''), statTile('check', 'up', c.up, 'ok', 'up'),
        statTile('alert', 'error', c.error, c.error ? 'warn' : '', 'error'), statTile('x', 'down', c.down, c.down ? 'warn' : '', 'down'));
      info.hidden = !data.read_only;
      info.textContent = data.read_only ? t('readonly', { why: data.read_only }) : '';
      if (!nodes.length) {
        UI.mount(list, h('div.stack', UI.empty(t('ov.none'), 'server'),
          h('div.row', h('a.btn.btn--primary', { href: '#/cluster/nodes?add=1' }, UI.icon('plus'), t('fm.add')))));
        UI.mount(note, '');
        return;
      }
      var rows = shown(nodes);
      if (!rows.length) {
        UI.mount(list, h('div.stack', UI.empty(t('ov.nomatch')), h('div.row', UI.btn(t('ov.clear'), 'x', function () {
          text.value = ''; label.value = ''; state.value = ''; changed();
        }, 'ghost sm'))));
      } else {
        UI.mount(list, view === 'table' ? UI.card(null, table(rows), null, true) : h('div.cl-cards', rows.map(card)));
      }
      UI.mount(note, t('ov.shown', { n: rows.length, all: nodes.length }), ' · ', t('ov.every', { s: Math.round(every / 1000) }));
    }

    // read reads the state, then sets the refresh to follow the poller: as often as it polls, 2s to 10s apart.
    // read 读取状态，然后让刷新跟随轮询器：与其轮询一样频繁，间隔 2 到 10 秒。
    function read() {
      if (busy) return;
      busy = true;
      FC.call(UI, 'GET', '/api/state').then(function (r) {
        busy = false;
        data = r.body || {};
        var want = Math.min(10000, Math.max(2000, data.interval_ms || 10000));
        if (want !== every) {
          every = want;
          if (timer) clearInterval(timer);
          timer = UI.every(every, read);
        }
        draw();
      }, function (e) {
        busy = false;
        if (!document.body.contains(el) || e.status === 401) return;
        UI.mount(list, h('div.callout.callout--danger', e.message));
      });
    }
    read();
  };
})();
