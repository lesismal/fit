// Cluster → BitTorrent (I206): the members' BitTorrent roles and the distributions of a file to members over BitTorrent.
//   BitTorrent      #/cluster/bittorrent: every member's client and tracker as the manager reads them now
//                   (GET /api/m/cluster/bittorrent, each member's GET /bittorrent/summary, one read every 2s at most for
//                   every viewer): running or not, the address it takes peers on, its torrents by state, rates, bytes,
//                   peers, publish entries and tracker; their sums; Open, the member's own BitTorrent pages through the node
//                   switcher; and the distributions, the latest first, with New distribution for an operator
//   Distribution    #/cluster/distribution/<id>: its source, publish entry and torrent, each target's status, progress,
//                   rate, peers and error, read every 2s while it runs, with Cancel
//   Overview        a card of the sums and the distributions under way (FitCluster.overviewCard)
// A distribution spreads a torrent a member's private publish entry seeds to other members, which add it with the source and
// each other as their only peers, into a category of their own (its directory); docs/cluster.md says how. Needs shared.js
// and groups.js (FC.groups); before cluster.js.
//
// 集群 → BitTorrent（I206）：成员的 BitTorrent 角色，以及通过 BitTorrent 向成员分发文件。
//   BitTorrent  #/cluster/bittorrent：管理端即时读取的各成员客户端与 tracker（GET /api/m/cluster/bittorrent，即各成员的
//               GET /bittorrent/summary，所有查看者共用、最多每 2 秒读取一次）：是否运行、接受对端的地址、按状态统计的种子、
//               速率、字节数、对端、发布条目与 tracker；合计；“打开”经节点切换器打开该成员自己的 BitTorrent 页面；以及分发
//               列表（最新的在前），操作员可“新建分发”
//   分发        #/cluster/distribution/<id>：其源、发布条目与种子，各目标的状态、进度、速率、对端与错误；运行期间每 2 秒
//               读取一次，可“取消”
//   概览        合计与进行中的分发的卡片（FitCluster.overviewCard）
// 分发把某成员私有发布条目做种的种子传给其他成员：它们以源和彼此作为唯一的对端添加该种子，放入自己的某个分类（其目录）；
// 详见 docs/cluster.md。依赖 shared.js 与 groups.js（FC.groups）；在 cluster.js 之前加载。
(function () {
  'use strict';
  var FC = window.FitCluster;
  FC.WORDS.push(
    ['n.bittorrent', 'BitTorrent', 'BitTorrent'],
    ['bt.intro', 'Each member\'s BitTorrent roles, read from it now (at most every 2s for every viewer). A member down, or one older than this view, is said so.',
      '各成员的 BitTorrent 角色，即时从成员读取（所有查看者共用，最多每 2 秒一次）。离线的成员或早于此视图的成员会单独说明。'],
    ['bt.clients', 'Clients running', '运行中的客户端'], ['bt.torrents', 'Torrents', '种子'], ['bt.down', 'Download', '下载'], ['bt.up', 'Upload', '上传'],
    ['bt.peers', 'Peers', '对端'], ['bt.trackers', 'Trackers', 'Tracker'], ['bt.of', '{n} of {all}', '{all} 个中的 {n} 个'],
    ['bt.member', 'Member', '成员'], ['bt.client', 'Client', '客户端'], ['bt.listen', 'Takes peers at', '接受对端的地址'],
    ['bt.states', 'Torrents by state', '各状态的种子'], ['bt.rates', 'Rates', '速率'], ['bt.bytes', 'Down / up', '下载 / 上传'],
    ['bt.published', 'Published', '已发布'], ['bt.tracker', 'Tracker', 'Tracker'], ['bt.tracker.v', '{t} torrents, {p} peers', '{t} 个种子，{p} 个对端'],
    ['bt.c.running', 'running', '运行中'], ['bt.c.stopped', 'not running', '未运行'], ['bt.c.none', 'no client role', '无客户端角色'],
    ['bt.open', 'Open', '打开'], ['bt.open.title', 'Open {name}\'s own BitTorrent pages through this manager', '通过本管理端打开 {name} 自己的 BitTorrent 页面'],
    ['bt.persec', '{v}/s', '{v}/秒'], ['bt.none', 'No members yet.', '还没有成员。'],
    ['bt.errors', '{n} in error', '{n} 个出错'],
    // Distributions / 分发
    ['ds.title', 'Distributions', '分发'], ['ds.new', 'New distribution', '新建分发'],
    ['ds.intro', 'A distribution spreads a torrent one member publishes privately (a publish entry with private set) to other members: each adds it with the source and the other targets as its only peers, into one of its categories, and the manager follows it until every target has it whole. At most {n} targets, {r} distributions at once.',
      '分发把某个成员私有发布（设置了 private 的发布条目）的种子传给其他成员：每个目标以源和其他目标作为唯一的对端添加该种子，放入自己的某个分类，管理端一直跟进到每个目标都下载完整。最多 {n} 个目标，同时最多 {r} 个分发。'],
    ['ds.empty', 'No distribution yet.', '还没有分发。'],
    ['ds.source', 'Source', '源'], ['ds.publish', 'Publish entry', '发布条目'], ['ds.torrent', 'Torrent', '种子'],
    ['ds.torrent.hint', 'Its ID, or its file\'s path in a per_file entry; empty for an entry of one torrent.', '其 ID，或 per_file 条目中文件的路径；条目只有一个种子时留空。'],
    ['ds.targets', 'Targets', '目标'], ['ds.group', 'A group\'s members', '分组的成员'], ['ds.nogroup', 'Members picked below', '下方选择的成员'],
    ['ds.category', 'Category on the targets', '目标上的分类'], ['ds.category.hint', 'A category each target\'s client has, whose directory it downloads into; its download_dir when empty.',
      '每个目标客户端都有的分类，下载到该分类的目录；留空则为其 download_dir。'],
    ['ds.keep', 'Keep seeding on the targets once done', '完成后在目标上继续做种'],
    ['ds.stall', 'Stall timeout (minutes)', '停滞超时（分钟）'], ['ds.stall.hint', 'A target whose download makes no progress this long fails. 10 when empty.',
      '下载在这段时间内毫无进展的目标判为失败。留空为 10。'],
    ['ds.description', 'Description', '说明'], ['ds.start', 'Start', '开始'], ['ds.started', 'The distribution {id} started.', '分发 {id} 已开始。'],
    ['ds.nosource', 'No member publishes anything yet: a source needs a private publish entry (bittorrent.client.publish).',
      '还没有成员发布任何内容：源需要一个私有的发布条目（bittorrent.client.publish）。'],
    ['ds.pick', 'Pick the members to spread to, or a group.', '请选择要分发到的成员或分组。'],
    ['ds.state', 'State', '状态'], ['ds.created', 'Started', '开始于'], ['ds.ended', 'Ended', '结束于'], ['ds.user', 'By', '发起人'],
    ['ds.counts', '{done} done, {active} under way, {failed} failed, {cancelled} cancelled, {pending} waiting',
      '完成 {done}，进行中 {active}，失败 {failed}，取消 {cancelled}，等待 {pending}'],
    ['ds.s.preparing', 'preparing', '准备中'], ['ds.s.running', 'running', '运行中'], ['ds.s.done', 'done', '完成'],
    ['ds.s.failed', 'failed', '失败'], ['ds.s.cancelled', 'cancelled', '已取消'],
    ['ds.t.pending', 'waiting', '等待'], ['ds.t.adding', 'adding', '添加中'], ['ds.t.downloading', 'downloading', '下载中'],
    ['ds.t.done', 'done', '完成'], ['ds.t.failed', 'failed', '失败'], ['ds.t.cancelled', 'cancelled', '已取消'],
    ['ds.cancel', 'Cancel', '取消分发'], ['ds.cancel.ask', 'Cancel the distribution {id}? The targets still downloading stop, and the torrent and what they downloaded of it are removed from them.',
      '取消分发 {id}？仍在下载的目标将停止，并从它们上面删除该种子及已下载的内容。'],
    ['ds.cancelled', 'The distribution {id} is cancelled.', '分发 {id} 已取消。'],
    ['ds.size', 'Size', '大小'], ['ds.files', '{n} files', '{n} 个文件'], ['ds.sourcepeer', 'Source\'s address', '源的地址'],
    ['ds.peer', 'Address', '地址'], ['ds.progress', 'Progress', '进度'], ['ds.rate', 'Rate', '速率'], ['ds.adopted', 'had it already', '已有该种子'],
    ['ds.removed', 'torrent removed', '种子已删除'], ['ds.back', 'BitTorrent', 'BitTorrent'], ['ds.yes', 'yes', '是'],
    ['ds.k.no_client', 'No client', '无客户端'], ['ds.k.stalled', 'Stalled', '停滞'], ['ds.k.not_private', 'Not private', '非私有'],
    ['ds.card', '{c} of {m} members run a client: {t} torrents, {d} down, {u} up', '{m} 个成员中 {c} 个运行客户端：{t} 个种子，下载 {d}，上传 {u}'],
    ['ds.card.running', '{n} distributions under way', '{n} 个分发进行中']
  );

  var DS_KIND = { preparing: 'info', running: 'info', done: 'ok', failed: 'danger', cancelled: 'warn' };
  var TG_KIND = { pending: '', adding: 'info', downloading: 'info', done: 'ok', failed: 'danger', cancelled: 'warn' };

  // bytes words a count of bytes. / 将字节数格式化。
  function bytes(n) {
    n = +n || 0;
    var u = ['B', 'KiB', 'MiB', 'GiB', 'TiB'], i = 0;
    while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
    return (i ? n.toFixed(n >= 100 ? 0 : 1) : String(Math.round(n))) + ' ' + u[i];
  }
  function rate(UI, v) { return UI.t('bt.persec', { v: bytes(v) }); }
  function problemWord(UI, p) {
    if (!p) return '';
    var k = 'ds.k.' + p.kind;
    var w = ['no_client', 'stalled', 'not_private'].indexOf(p.kind) >= 0 ? UI.t(k) : FC.kindWord(UI, p.kind);
    return w + ': ' + (p.message || '');
  }
  // openBT is a link to the member's own BitTorrent pages, through the node switcher. / 经节点切换器打开成员自己的 BitTorrent 页面。
  function openBT(UI, name) {
    return UI.h('a.btn.btn--ghost.btn--sm', { href: UI.targetURL(name, '#/bittorrent/torrents'), title: UI.t('bt.open.title', { name: name }) },
      UI.icon('swap'), UI.t('bt.open'));
  }

  // ---- The page / 页面 ----
  FC.pages.bittorrent = function (UI, sh) {
    var h = UI.h, t = UI.t;
    var el = sh.page('bittorrent', t('n.bittorrent'));
    var stats = h('div.grid.grid--4.cl-stats'), list = h('div'), dists = h('div');
    var view = null;
    var newBtn = FC.canAny('operator') ? UI.btn(t('ds.new'), 'plus', function () { newDist(UI, view); }, 'primary sm') : null;
    UI.mount(sh.tools, newBtn, UI.btn(t('refresh'), 'refresh', function () { read(); readDists(); }, 'ghost sm'));
    UI.mount(el, h('div.stack', h('div.small.muted', t('bt.intro')), stats, list,
      UI.card(t('ds.title'), dists)));
    UI.mount(list, h('div.row', h('span.spinner'), h('span.muted', t('loading'))));

    function draw() {
      if (!view || !document.body.contains(el)) return;
      var tt = view.totals || {};
      UI.mount(stats,
        UI.stat('download', t('bt.clients'), t('bt.of', { n: tt.running || 0, all: tt.members || 0 }), null, tt.running ? 'ok' : ''),
        UI.stat('layers', t('bt.torrents'), String(tt.torrents || 0), tt.errors ? t('bt.errors', { n: tt.errors }) : null, tt.errors ? 'warn' : 'info'),
        UI.stat('activity', t('bt.rates'), '↓ ' + rate(UI, tt.download_rate) + ' · ↑ ' + rate(UI, tt.upload_rate), null, 'info'),
        UI.stat('radio', t('bt.trackers'), String(tt.trackers || 0), t('bt.tracker.v', { t: tt.tracker_torrents || 0, p: tt.tracker_peers || 0 }), ''));
      var rows = view.members || [];
      if (!rows.length) { UI.mount(list, UI.empty(t('bt.none'), 'server')); return; }
      UI.mount(list, UI.card(null, UI.table([
        { label: t('bt.member'), render: function (m) {
          return h('div', h('a', { href: '#/cluster/node/' + encodeURIComponent(m.name) }, h('b', m.name)), ' ', m.status !== 'up' ? FC.stateBadge(UI, m.status) : null);
        } },
        { label: t('bt.client'), render: function (m) {
          if (m.error) return h('span.small.cl-warn', { title: m.error.message || '' }, problemWord(UI, m.error));
          var c = m.summary && m.summary.client;
          if (!c) return h('span.muted', t('bt.c.none'));
          return h('div', UI.badge(t(c.running ? 'bt.c.running' : 'bt.c.stopped'), c.running ? 'ok' : 'warn', true),
            c.listen ? h('div.small.muted', h('code', c.listen)) : null);
        } },
        { label: t('bt.states'), render: function (m) {
          var c = m.summary && m.summary.client;
          if (!c) return '—';
          var ks = Object.keys(c.states || {}).sort();
          return h('div', h('b', String(c.torrents || 0)), ks.length ? h('div.small.muted', ks.map(function (k) { return k + ' ' + c.states[k]; }).join(' · ')) : null,
            c.errors ? h('div.small.cl-warn', t('bt.errors', { n: c.errors })) : null);
        } },
        { label: t('bt.rates'), cls: 'num', render: function (m) {
          var c = m.summary && m.summary.client;
          return c ? h('div', '↓ ' + rate(UI, c.download_rate), h('br'), '↑ ' + rate(UI, c.upload_rate)) : '—';
        } },
        { label: t('bt.bytes'), cls: 'num', render: function (m) {
          var c = m.summary && m.summary.client;
          return c ? bytes(c.downloaded) + ' / ' + bytes(c.uploaded) : '—';
        } },
        { label: t('bt.peers'), cls: 'num', render: function (m) { var c = m.summary && m.summary.client; return c ? String(c.peers || 0) : '—'; } },
        { label: t('bt.published'), render: function (m) {
          var c = m.summary && m.summary.client;
          if (!c || !(c.publish || []).length) return '—';
          return h('div.small', c.publish.map(function (p) { return h('div', h('code', p.name), ' ' + p.torrents, p.error ? h('span.cl-warn', ' · ' + p.error) : null); }));
        } },
        { label: t('bt.tracker'), render: function (m) {
          var tr = m.summary && m.summary.tracker;
          return tr ? t('bt.tracker.v', { t: tr.torrents || 0, p: tr.peers || 0 }) : '—';
        } },
        { label: '', render: function (m) { return m.summary && m.summary.client ? openBT(UI, m.name) : null; } }
      ], rows), null, true));
    }
    function read() {
      FC.call(UI, 'GET', '/api/bittorrent').then(function (r) { view = r.body || {}; draw(); }, function (e) {
        if (!document.body.contains(el) || e.status === 401) return;
        UI.mount(list, h('div.callout.callout--danger', e.message));
      });
    }
    function readDists() {
      FC.call(UI, 'GET', '/api/distributions').then(function (r) {
        if (!document.body.contains(el)) return;
        var b = r.body || {}, ds = b.distributions || [];
        UI.mount(dists, h('div.stack', h('div.small.muted', t('ds.intro', { n: b.max_targets || 64, r: b.max_running || 2 })),
          !ds.length ? UI.empty(t('ds.empty'), 'download') : UI.table([
            { label: t('ds.state'), render: function (d) { return UI.badge(t('ds.s.' + d.state), DS_KIND[d.state] || '', true); } },
            { label: t('ds.source'), render: function (d) { return h('div', h('b', d.source), h('div.small.muted', d.publish + (d.torrent ? ' / ' + d.torrent.name : ''))); } },
            { label: t('ds.targets'), render: function (d) { return h('div.small', t('ds.counts', countsOf(d))); } },
            { label: t('ds.created'), render: function (d) { return h('span.small.muted', FC.when(d.created)); } },
            { label: t('ds.user'), render: function (d) { return d.user; } }
          ], ds, { onRowClick: function (d) { location.hash = '#/cluster/distribution/' + encodeURIComponent(d.id); } })));
      }, function (e) {
        if (!document.body.contains(el) || e.status === 401) return;
        UI.mount(dists, h('div.callout.callout--danger', e.message));
      });
    }
    read();
    readDists();
    UI.every(5000, function () { if (document.body.contains(el)) { read(); readDists(); } });
  };

  function countsOf(d) {
    var c = d.counts || {};
    return { done: c.done || 0, active: c.active || 0, failed: c.failed || 0, cancelled: c.cancelled || 0, pending: c.pending || 0 };
  }

  // newDist opens the form of a new distribution: the sources are the members whose clients publish something.
  // newDist 打开新建分发的表单：源为客户端发布了内容的成员。
  function newDist(UI, view) {
    var h = UI.h, t = UI.t;
    var members = (view && view.members) || [];
    var sources = members.filter(function (m) { var c = m.summary && m.summary.client; return c && c.running && (c.publish || []).length; });
    if (!sources.length) { UI.toast(t('ds.nosource'), 'error'); return; }
    var source = h('select.select', sources.map(function (m) { return h('option', { value: m.name }, m.name); }));
    var publish = h('select.select');
    var torrent = h('input.input', { placeholder: t('ds.torrent.hint') });
    var group = h('select.select', h('option', { value: '' }, t('ds.nogroup')));
    var category = h('input.input');
    var keep = h('input', { type: 'checkbox' });
    var stall = h('input.input', { type: 'number', min: '1', max: '1440', placeholder: '10' });
    var desc = h('input.input', { maxlength: '256' });
    var boxes = {}, picks = h('div.cl-picks');
    function fillPublish() {
      var m = sources.filter(function (x) { return x.name === source.value; })[0];
      UI.mount(publish, ((m && m.summary.client.publish) || []).map(function (p) { return h('option', { value: p.name }, p.name + ' (' + p.torrents + ')'); }));
      UI.mount(picks, members.filter(function (x) { return x.name !== source.value; }).map(function (x) {
        var c = x.summary && x.summary.client;
        boxes[x.name] = boxes[x.name] || h('input', { type: 'checkbox', disabled: !c || !c.running });
        return h('label.row.small', boxes[x.name], ' ', x.name, !c ? h('span.muted', ' · ' + t('bt.c.none')) : !c.running ? h('span.muted', ' · ' + t('bt.c.stopped')) : null);
      }));
    }
    source.addEventListener('change', fillPublish);
    fillPublish();
    if (FC.groups) {
      FC.groups(UI).then(function (d) {
        UI.mount(group, h('option', { value: '' }, t('ds.nogroup')), d.groups.map(function (g) { return h('option', { value: g.name }, g.name); }));
      }, function () { /* ignore / 忽略 */ });
    }
    var err = h('div.callout.callout--danger', { hidden: true });
    UI.modal({
      title: t('ds.new'), wide: true,
      body: h('div.stack', err,
        UI.field(t('ds.source'), source), UI.field(t('ds.publish'), publish), UI.field(t('ds.torrent'), torrent, t('ds.torrent.hint')),
        UI.field(t('ds.group'), group), UI.field(t('ds.targets'), picks),
        UI.field(t('ds.category'), category, t('ds.category.hint')), h('label.row', keep, ' ', t('ds.keep')),
        UI.field(t('ds.stall'), stall, t('ds.stall.hint')), UI.field(t('ds.description'), desc)),
      actions: [{ label: t('cancel') }, { label: t('ds.start'), kind: 'primary', onclick: function () {
        var picked = Object.keys(boxes).filter(function (n) { return boxes[n].checked && n !== source.value; });
        if (!picked.length && !group.value) { err.hidden = false; err.textContent = t('ds.pick'); return false; }
        var body = { source: source.value, publish: publish.value, torrent: torrent.value.trim(), group: group.value, members: picked,
          category: category.value.trim(), keep_seeding: keep.checked, description: desc.value.trim() };
        if (+stall.value > 0) body.stall_timeout_ms = Math.round(+stall.value * 60000);
        return FC.call(UI, 'POST', '/api/distributions', body).then(function (r) {
          UI.toast(t('ds.started', { id: r.body.id }));
          location.hash = '#/cluster/distribution/' + encodeURIComponent(r.body.id);
        }, function (e) { err.hidden = false; err.textContent = e.message; return false; });
      } }]
    });
  }

  // ---- A distribution's page / 分发页 ----
  FC.pages.distribution = function (UI, sh, p) {
    var h = UI.h, t = UI.t, id = p.id;
    var el = sh.page('bittorrent', id, t('m.cluster') + ' / ' + t('n.bittorrent') + ' / ' + id);
    var body = h('div.stack');
    UI.mount(el, h('div.stack', h('div', h('a.small', { href: '#/cluster/bittorrent' }, '← ' + t('ds.back'))), body));
    UI.mount(body, h('div.row', h('span.spinner'), h('span.muted', t('loading'))));
    var timer = null;
    function draw(d) {
      var names = [d.source].concat((d.targets || []).map(function (x) { return x.node; }));
      var cancel = !d.finished && FC.canAll('operator', names) ? UI.btn(t('ds.cancel'), 'x', function () {
        UI.confirm(t('ds.cancel.ask', { id: d.id }), t('ds.cancel'), true).then(function (yes) {
          if (!yes) return;
          FC.call(UI, 'POST', '/api/distributions/' + encodeURIComponent(d.id) + '/cancel').then(function () {
            UI.toast(t('ds.cancelled', { id: d.id }));
            read();
          }, function (e) { UI.toast(e.message, 'error'); });
        });
      }, 'danger sm') : null;
      var tr = d.torrent;
      UI.mount(body,
        UI.card(d.description || id, h('div.stack',
          UI.kv([
            [t('ds.state'), h('span', UI.badge(t('ds.s.' + d.state), DS_KIND[d.state] || '', true), d.reason ? h('span.small.muted', ' ' + d.reason) : null)],
            [t('ds.source'), h('span', h('a', { href: '#/cluster/node/' + encodeURIComponent(d.source) }, d.source), ' ', openBT(UI, d.source))],
            [t('ds.publish'), h('code', d.publish)],
            [t('ds.torrent'), tr ? h('span', tr.name, h('span.small.muted', ' · ' + tr.id)) : '—'],
            [t('ds.size'), tr ? bytes(tr.size) + ' · ' + t('ds.files', { n: tr.files }) : '—'],
            [t('ds.sourcepeer'), d.source_peer ? h('code', d.source_peer) : '—'],
            [t('ds.category'), d.category || '—'],
            [t('ds.keep'), d.keep_seeding ? t('ds.yes') : '—'],
            [t('ds.created'), FC.when(d.created)], [t('ds.ended'), d.ended ? FC.when(d.ended) : '—'], [t('ds.user'), d.user]
          ]),
          d.error ? h('div.callout.callout--danger', problemWord(UI, d.error)) : null), cancel),
        UI.card(t('ds.targets'), h('div.stack', h('div.small.muted', t('ds.counts', countsOf(d))), UI.table([
          { label: t('bt.member'), render: function (x) { return h('div', h('a', { href: '#/cluster/node/' + encodeURIComponent(x.node) }, h('b', x.node)), ' ', openBT(UI, x.node)); } },
          { label: t('ds.state'), render: function (x) {
            return h('div', UI.badge(t('ds.t.' + x.status), TG_KIND[x.status] || '', true), x.adopted ? h('div.small.muted', t('ds.adopted')) : null,
              x.removed ? h('div.small.muted', t('ds.removed')) : null);
          } },
          { label: t('ds.progress'), render: function (x) {
            return UI.meter(x.state || '', (x.progress || 0) * 100, 100, Math.floor((x.progress || 0) * 100) + '% · ' + bytes(x.completed) + ' / ' + bytes(x.wanted));
          } },
          { label: t('ds.rate'), cls: 'num', render: function (x) { return rate(UI, x.download_rate); } },
          { label: t('bt.peers'), cls: 'num', render: function (x) { return String(x.peers || 0); } },
          { label: t('ds.peer'), render: function (x) { return x.peer ? h('code.small', x.peer) : '—'; } },
          { label: '', render: function (x) { return x.error ? h('span.small.cl-warn', problemWord(UI, x.error)) : null; } }
        ], d.targets || []))));
      if (d.finished && timer) { clearInterval(timer); timer = null; }
    }
    function read() {
      FC.call(UI, 'GET', '/api/distributions/' + encodeURIComponent(id)).then(function (r) {
        if (document.body.contains(el)) draw(r.body || {});
      }, function (e) {
        if (!document.body.contains(el) || e.status === 401) return;
        UI.mount(body, h('div.callout.callout--danger', e.message));
      });
    }
    read();
    timer = UI.every(2000, function () { if (document.body.contains(el)) read(); });
  };

  // ---- The Overview's card / 概览卡片 ----
  FC.overviewCard(function (UI) {
    var h = UI.h, t = UI.t;
    var box = h('div.cl-btcard', { hidden: true });
    FC.call(UI, 'GET', '/api/bittorrent').then(function (r) {
      var v = r.body || {}, tt = v.totals || {}, running = ((v.distributions || {}).running || 0) + ((v.distributions || {}).preparing || 0);
      if (!tt.clients && !running) return;
      box.hidden = false;
      UI.mount(box, UI.card(t('n.bittorrent'), h('div.row.row--between',
        h('div', h('div', t('ds.card', { c: tt.running || 0, m: tt.members || 0, t: tt.torrents || 0, d: rate(UI, tt.download_rate), u: rate(UI, tt.upload_rate) })),
          running ? h('div.small', t('ds.card.running', { n: running })) : null),
        h('a.btn.btn--ghost.btn--sm', { href: '#/cluster/bittorrent' }, UI.icon('download'), t('n.bittorrent')))));
    }, function () { /* ignore / 忽略 */ });
    return box;
  });
})();
