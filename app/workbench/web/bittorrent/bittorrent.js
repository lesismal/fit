// The BitTorrent pages (I204): one module in the menu for the bittorrent.client and bittorrent.tracker roles (I197-I203).
// They read and change the roles through the Admin API's /bittorrent endpoints (I203), passed on by the console, and
// edit the config's shared draft:
//
//   torrents           every torrent in queue order: name, size, a progress bar, state and why the role holds it, rates,
//                      time left, ratio, peers and category; filtered by state, category and name; picked torrents paused,
//                      resumed or removed (with or without their data, after a confirm); Add (a magnet link, a .torrent file
//                      or its URL, with a category, paused, sequential); the client's recent events
//   torrent/:id[/:tab] a torrent: Overview (its numbers, pieces had, a magnet link to copy, the category, queue position,
//                      sequential and its own limits, edited in place; pause, resume, recheck and remove), Files (each
//                      file's progress and priority, set one by one or for the files picked), Peers and Trackers (its
//                      trackers by tier and web seeds)
//   tracker            the tracker's totals and refusals, the allow-list's swarms, paged, and a user's passkey looked up
//   publish            each publish entry, its state and problems, and its torrents with their .torrent file and magnet link
//   config             the whole bittorrent member: client, categories, alt_speed, publish and the tracker
//
// The alternative rates (auto, on, off) are in the top bar of every client page. The pages poll the list every two seconds
// for rates and progress; beside it, the client's events stream on the console's own route
// (GET /api/m/admin/bittorrent/events), so that an added, removed or changed torrent shows at once and a completion or a
// storage error is told; on a member opened through a manager they poll only. Needs ui.js, workbench.js and config.js.
//
// BitTorrent 页面（I204）：菜单中的一个模块，对应 bittorrent.client 与 bittorrent.tracker 角色（I197-I203）。各页经控制台转发的
// Admin API /bittorrent 接口（I203）读取并修改角色，并编辑配置的共享草稿：torrents 按队列顺序列出种子（名称、大小、进度条、状态
// 及被保持的原因、速率、剩余时间、分享率、peer 与分类），可按状态、分类与名称筛选，可对所选种子暂停、恢复或删除（可连同数据，
// 需确认），“添加”可用磁力链接、.torrent 文件或其 URL，并指定分类、暂停与顺序下载，另列出客户端最近的事件；torrent/:id 为单个
// 种子：概览（各项数字、已有分片、可复制的磁力链接，以及就地编辑的分类、队列位置、顺序下载与自身限制；暂停、恢复、重新校验与删除）、
// 文件（各文件进度与优先级，可逐个或对所选文件设置）、Peer 与 Tracker（按层级列出的 tracker 与 Web 种子）。tracker 页显示
// tracker 的总计与拒绝、白名单中种子的 swarm（分页）以及按用户查询 passkey。publish 页列出各发布条目、其状态与问题，及其种子的
// .torrent 文件与磁力链接。config 为整个 bittorrent 成员。备用速率（自动、开、关）位于每个客户端页面的顶栏。页面每两秒轮询列表
// 以更新速率与进度；同时经控制台自己的路由（GET /api/m/admin/bittorrent/events）接收客户端的事件流，使添加、删除或状态变化即时
// 显示，并提示完成与存储错误；经管理端打开成员时只轮询。
(function () {
  'use strict';

  var ID = 'bittorrent';
  // POLL is how often the pages read the torrents; EVENTS the events the list page keeps; SWARMS the tracker's page.
  // POLL 为页面读取种子的间隔；EVENTS 为列表页保留的事件数；SWARMS 为 tracker 页的每页条数。
  var POLL = 2000, EVENTS = 30, SWARMS = 100, LIST = 1000;
  var STATES = ['metadata', 'checking', 'downloading', 'seeding', 'queued', 'paused', 'stopped', 'error'];
  var PRIORITIES = [0, 1, 4, 7];

  // [key, en, zh] / [键, 英文, 中文]
  var WORDS = [
    ['m.bt', 'BitTorrent', 'BitTorrent'], ['tag.bt', 'client / tracker', '客户端 / tracker'],
    ['n.torrents', 'Torrents', '种子'], ['n.tracker', 'Tracker', 'Tracker'], ['n.publish', 'Publishing', '发布'], ['n.config', 'BitTorrent config', 'BitTorrent 配置'],
    ['n.overview', 'Overview', '概览'], ['n.files', 'Files', '文件'], ['n.peers', 'Peers', 'Peer'], ['n.trackers', 'Trackers', 'Tracker'],
    // states / 状态
    ['s.metadata', 'metadata', '获取元数据'], ['s.checking', 'checking', '校验中'], ['s.downloading', 'downloading', '下载中'], ['s.seeding', 'seeding', '做种中'],
    ['s.queued', 'queued', '排队中'], ['s.paused', 'paused', '已暂停'], ['s.stopped', 'stopped', '已停止'], ['s.error', 'error', '错误'],
    ['held.starting', 'starting', '启动中'], ['held.queued', 'queued by the role', '由角色排队'], ['held.disk', 'short of disk space', '磁盘空间不足'],
    // the role / 角色
    ['r.off', 'The bittorrent.client role does not run here: set bittorrent.client on the BitTorrent config page, then save and apply.',
      'bittorrent.client 角色未在此运行：请在“BitTorrent 配置”页设置 bittorrent.client，然后保存并应用。'],
    ['r.trackerOff', 'The bittorrent.tracker role does not run here: set bittorrent.tracker on the BitTorrent config page, then save and apply.',
      'bittorrent.tracker 角色未在此运行：请在“BitTorrent 配置”页设置 bittorrent.tracker，然后保存并应用。'],
    ['r.toConfig', 'BitTorrent config', 'BitTorrent 配置'],
    // the alternative rates / 备用速率
    ['alt.label', 'Alternative rates', '备用速率'], ['alt.auto', 'auto (schedule)', '自动（按计划）'], ['alt.on', 'on', '开'], ['alt.off', 'off', '关'],
    ['alt.active', 'holding: ↑ {up} ↓ {down}', '生效中：↑ {up} ↓ {down}'], ['alt.idle', 'not holding', '未生效'],
    ['alt.none', 'The config has no alt_speed: the alternative rates never hold.', '配置中没有 alt_speed：备用速率不会生效。'],
    ['alt.set', 'The alternative rates are {mode}.', '备用速率已设为{mode}。'],
    // the list / 列表
    ['l.add', 'Add torrent', '添加种子'], ['l.pause', 'Pause', '暂停'], ['l.resume', 'Resume', '恢复'], ['l.remove', 'Remove', '删除'],
    ['l.state', 'State', '状态'], ['l.allStates', 'Every state', '全部状态'], ['l.category', 'Category', '分类'], ['l.allCats', 'Every category', '全部分类'],
    ['l.noCat', 'No category', '无分类'], ['l.search', 'Name contains…', '名称包含…'],
    ['l.name', 'Name', '名称'], ['l.size', 'Size', '大小'], ['l.progress', 'Progress', '进度'], ['l.down', 'Down', '下载'], ['l.up', 'Up', '上传'],
    ['l.eta', 'Time left', '剩余时间'], ['l.ratio', 'Ratio', '分享率'], ['l.peers', 'Peers', 'Peer'],
    ['l.none', 'No torrent yet: add one with Add torrent.', '还没有种子：请点击“添加种子”。'], ['l.noMatch', 'No torrent matches the filters.', '没有符合筛选条件的种子。'],
    ['l.count', '{n} of {total} torrents', '共 {total} 个种子，显示 {n} 个'], ['l.picked', '{n} picked', '已选 {n} 个'],
    ['l.pickAll', 'Pick every torrent shown', '选择所显示的全部种子'], ['l.temporary', 'on the fly', '临时'],
    ['l.peersOf', '{peers} ({seeds} seeds)', '{peers}（{seeds} 个做种）'],
    ['l.paused', '{n} paused.', '已暂停 {n} 个。'], ['l.resumed', '{n} resumed.', '已恢复 {n} 个。'], ['l.removed', '{n} removed.', '已删除 {n} 个。'],
    ['l.failed', '{name}: {error}', '{name}：{error}'],
    ['l.remove.q', 'Remove {n} torrents from the client?', '从客户端删除 {n} 个种子？'],
    ['l.withData', 'Delete their downloaded files too', '同时删除已下载的文件'],
    ['l.withData1', 'Delete its downloaded files too', '同时删除已下载的文件'],
    ['l.dataNote', 'Without it, the files stay on disk.', '不勾选则文件保留在磁盘上。'],
    ['l.events', 'Recent events', '最近事件'], ['l.noEvents', 'No event since the page opened.', '页面打开以来没有事件。'],
    ['l.live', 'live', '实时'], ['l.polling', 'polling', '轮询'],
    // events / 事件
    ['ev.added', 'added', '已添加'], ['ev.removed', 'removed', '已删除'], ['ev.state', 'state', '状态'], ['ev.completed', 'completed', '已完成'],
    ['ev.error', 'storage error', '存储错误'], ['ev.peer_banned', 'peer banned', 'peer 被封禁'], ['ev.seed_limit', 'seed limit', '做种上限'],
    ['ev.alt_speed', 'alternative rates', '备用速率'], ['ev.reset', 'events missed: the list is read again', '错过了事件：已重新读取列表'],
    ['ev.doneToast', '{name} completed.', '{name} 已完成。'], ['ev.errToast', '{name}: {error}', '{name}：{error}'],
    ['ev.fromTo', '{from} → {to}', '{from} → {to}'], ['ev.dataDeleted', 'with its data', '连同数据'],
    // the add dialog / 添加对话框
    ['a.title', 'Add a torrent', '添加种子'], ['a.magnet', 'Magnet link', '磁力链接'], ['a.file', '.torrent file', '.torrent 文件'], ['a.url', 'URL', 'URL'],
    ['a.magnetHint', 'magnet:?xt=urn:btih:…', 'magnet:?xt=urn:btih:…'], ['a.urlHint', 'The http or https URL of a .torrent file, which fit downloads.', 'http 或 https 的 .torrent 文件 URL，由 fit 下载。'],
    ['a.fileHint', 'At most 32 MiB.', '最大 32 MiB。'], ['a.category', 'Category', '分类'], ['a.paused', 'Add it paused', '添加后暂停'],
    ['a.sequential', 'Download its pieces in order (to play while it downloads)', '按顺序下载分片（便于边下边播）'],
    ['a.add', 'Add', '添加'], ['a.need', 'Give a magnet link, a .torrent file or a URL.', '请提供磁力链接、.torrent 文件或 URL。'],
    ['a.big', 'The file is larger than 32 MiB.', '文件大于 32 MiB。'], ['a.added', '{name} added.', '已添加 {name}。'],
    // a torrent / 单个种子
    ['t.notFound', 'No torrent {id} is held by the client.', '客户端中没有种子 {id}。'], ['t.list', 'Torrents', '种子列表'],
    ['t.recheck', 'Recheck', '重新校验'], ['t.recheck.q', 'Check {name}\'s files again, piece by piece? It stops while it is checked, and its peers connect again.',
      '逐个分片重新校验 {name} 的文件？校验期间它会停止，peer 会重新连接。'],
    ['t.rechecking', '{name} is checked again.', '{name} 正在重新校验。'],
    ['t.remove.q', 'Remove {name} from the client?', '从客户端删除 {name}？'], ['t.removed', '{name} removed.', '已删除 {name}。'],
    ['t.pausedMsg', '{name} paused.', '{name} 已暂停。'], ['t.resumedMsg', '{name} resumed.', '{name} 已恢复。'],
    ['t.downloaded', 'Downloaded', '已下载'], ['t.uploaded', 'Uploaded', '已上传'], ['t.rates', 'Rates', '速率'],
    ['t.pieces', 'Pieces', '分片'], ['t.piecesOf', '{have} of {all}', '{have} / {all}'], ['t.wanted', 'Wanted', '需要'],
    ['t.id', 'ID', 'ID'], ['t.hash1', 'Info hash (v1)', 'Info hash（v1）'], ['t.hash2', 'Info hash (v2)', 'Info hash（v2）'], ['t.dir', 'Directory', '目录'],
    ['t.added', 'Added', '添加时间'], ['t.magnet', 'Magnet link', '磁力链接'], ['t.error', 'Error', '错误'], ['t.held', 'Held', '保持原因'],
    ['t.settings', 'Settings', '设置'], ['t.queue', 'Queue position', '队列位置'], ['t.queueHint', 'From 0, the top: the queue starts torrents in this order.', '从 0（最前）开始：队列按此顺序启动种子。'],
    ['t.sequential', 'Sequential download', '顺序下载'], ['t.set', 'Set', '设置'], ['t.changed', 'Changed.', '已修改。'],
    ['t.limits', 'Its own limits', '自身限制'], ['t.limitsNote', 'Under the client\'s and in place of its category\'s; empty fields are not set. In force now: {eff}.',
      '在客户端限制之下，并取代其分类的限制；留空即不设置。当前生效：{eff}。'],
    ['t.upRate', 'Upload rate (bytes/s)', '上传速率（字节/秒）'], ['t.downRate', 'Download rate (bytes/s)', '下载速率（字节/秒）'],
    ['t.maxConns', 'Connections', '连接数'], ['t.slots', 'Upload slots', '上传槽位'], ['t.seedRatio', 'Seed ratio', '做种分享率'],
    ['t.seedTime', 'Seed time', '做种时长'], ['t.seedTimeHint', 'A duration, as 72h; -1s seeds for ever.', '时长，如 72h；-1s 表示永久做种。'],
    ['t.saveLimits', 'Save limits', '保存限制'], ['t.clearLimits', 'Clear limits', '清除限制'], ['t.badNumber', '{field} is not a number.', '{field} 不是数字。'],
    ['t.noLimits', 'none', '无'],
    // files / 文件
    ['f.path', 'Path', '路径'], ['f.priority', 'Priority', '优先级'], ['f.setPicked', 'Set the picked files to', '将所选文件设为'],
    ['f.none', 'No files yet: the magnet link\'s metadata has not come.', '还没有文件：磁力链接的元数据尚未获取。'],
    ['prio.0', 'skip', '不下载'], ['prio.1', 'low', '低'], ['prio.4', 'normal', '普通'], ['prio.7', 'high', '高'],
    // peers / Peer
    ['p.addr', 'Address', '地址'], ['p.client', 'Client', '客户端'], ['p.flags', 'Connection', '连接'], ['p.pieces', 'Pieces', '分片'],
    ['p.none', 'No peer is connected.', '没有已连接的 peer。'], ['p.out', 'outgoing', '主动连接'], ['p.in', 'incoming', '被动连接'],
    ['p.utp', 'uTP', 'uTP'], ['p.enc', 'encrypted', '加密'], ['p.seed', 'seed', '做种者'], ['p.choked', 'choked', '阻塞'], ['p.snubbed', 'snubbed', '冷落'],
    // trackers / Tracker
    ['tr.url', 'URL', 'URL'], ['tr.tier', 'Tier', '层级'], ['tr.none', 'No tracker: peers are found by the DHT and peer exchange.', '没有 tracker：通过 DHT 与 peer 交换寻找 peer。'],
    ['tr.seeds', 'Web seeds', 'Web 种子'], ['tr.note', 'As its .torrent file or magnet link names them.', '即其 .torrent 文件或磁力链接中列出的。'],
    // the tracker / Tracker 角色
    ['k.torrents', 'Torrents', '种子'], ['k.peers', 'Peers', 'Peer'], ['k.announces', 'Announces', 'Announce'], ['k.scrapes', 'Scrapes', 'Scrape'],
    ['k.refused', 'Refused', '拒绝'], ['k.access', 'Who may announce', '谁可以 announce'], ['k.private', 'Private', '私有'], ['k.public', 'Public', '公开'], ['k.allowedN', '{n} info hashes', '{n} 个 info hash'], ['k.noList', 'none: every torrent', '无：接受所有种子'], ['k.udp', 'UDP', 'UDP'],
    ['k.of', '{n} of {max}', '{n} / {max}'], ['k.ref.unregistered', 'not in the allow-list', '不在白名单中'], ['k.ref.passkey', 'bad passkey', 'passkey 无效'],
    ['k.ref.access', 'refused by access', '被访问控制拒绝'], ['k.swarms', 'Swarms of the allow-list', '白名单中种子的 swarm'],
    ['k.swarmsNote', 'A public tracker cannot list the torrents it tracks; the allow-list\'s are listed.', '公开 tracker 无法列出其跟踪的种子；此处列出白名单中的种子。'],
    ['k.hash', 'Info hash', 'Info hash'], ['k.seeders', 'Seeders', '做种者'], ['k.leechers', 'Leechers', '下载者'], ['k.completed', 'Completed', '已完成'],
    ['k.tracked', 'Tracked', '已跟踪'], ['k.prev', 'Previous', '上一页'], ['k.next', 'Next', '下一页'],
    ['k.passkey', 'A user\'s passkey', '用户的 passkey'], ['k.user', 'User', '用户'], ['k.lookup', 'Look up', '查询'],
    ['k.passkeyNote', 'A private tracker\'s passkey of a media_auth user, for the user\'s announce URL: shown to you here, never kept.',
      '私有 tracker 中 media_auth 用户的 passkey，用于该用户的 announce URL：只在此显示给你，从不保存。'],
    ['k.publicNote', 'The tracker is public: it has no passkeys.', '该 tracker 是公开的：没有 passkey。'],
    // publishing / 发布
    ['b.none', 'No directory is published: add one to bittorrent.client.publish on the BitTorrent config page.',
      '没有发布任何目录：请在“BitTorrent 配置”页的 bittorrent.client.publish 中添加。'],
    ['b.dir', 'Directory', '目录'], ['b.mode', 'Mode', '模式'], ['b.webSeed', 'Web seed', 'Web 种子'], ['b.scanned', 'Scanned', '扫描时间'],
    ['b.pending', 'Pending', '待处理'], ['b.pendingN', '{n} files waiting to settle or being built', '{n} 个文件等待稳定或正在构建'],
    ['b.problems', 'Problems', '问题'], ['b.dropped', 'and {n} more', '另有 {n} 个'], ['b.files', 'Files', '文件'], ['b.built', 'Built', '构建时间'], ['b.links', 'Links', '链接'], ['b.torrentFile', '.torrent', '.torrent'],
    ['b.magnet', 'magnet', '磁力链接'], ['b.copyMagnet', 'Copy the magnet link', '复制磁力链接'], ['b.noTorrents', 'No torrent built yet.', '尚未构建任何种子。']
  ];

  var enc = encodeURIComponent;
  function fmtBytes(n) {
    n = +n || 0;
    if (n >= 1099511627776) return (n / 1099511627776).toFixed(2) + ' TiB';
    if (n >= 1 << 30) return (n / (1 << 30)).toFixed(2) + ' GiB';
    if (n >= 1 << 20) return (n / (1 << 20)).toFixed(1) + ' MiB';
    if (n >= 1 << 10) return (n / (1 << 10)).toFixed(1) + ' KiB';
    return n + ' B';
  }
  function fmtRate(n) { return +n >= 1 ? fmtBytes(Math.round(+n)) + '/s' : '—'; }
  // fmtETA words the seconds left: seconds under a minute, then UI.fmtDur's. / 剩余秒数：一分钟内以秒计，否则用 UI.fmtDur。
  function fmtETA(sec) { return !sec || sec < 0 ? '—' : sec < 60 ? Math.ceil(sec) + 's' : UI.fmtDur(sec); }
  // durText words nanoseconds as a duration the Admin API reads back: 72h, 90m, 45s; -1s for ever.
  // durText 把纳秒表示为 Admin API 可读回的时长：72h、90m、45s；永久为 -1s。
  function durText(ns) {
    if (!ns) return '';
    if (ns < 0) return '-1s';
    var sec = Math.round(ns / 1e9);
    return sec % 3600 === 0 ? sec / 3600 + 'h' : sec % 60 === 0 ? sec / 60 + 'm' : sec + 's';
  }
  function unix(s) { var v = Date.parse(s || ''); return isNaN(v) || v <= 0 ? 0 : v / 1000; }
  // ratio is uploaded over size, as seed_ratio counts it; eta the seconds left of what it wants, while it downloads.
  // ratio 为已上传量除以大小（与 seed_ratio 的计算一致）；eta 为下载中时剩余所需的秒数。
  function ratio(s) { return s.size > 0 ? (s.uploaded || 0) / s.size : 0; }
  function eta(s) { var left = (s.wanted || 0) - (s.completed || 0); return s.state === 'downloading' && left > 0 && s.download_rate >= 1 ? left / s.download_rate : 0; }
  function stateKind(s) {
    if (!s) return '';
    return { downloading: 'info', seeding: 'ok', checking: 'info', metadata: 'info', queued: 'warn', paused: '', stopped: '', error: 'danger' }[s.state] || '';
  }
  // progressBar draws a fraction as a bar with its percentage. / 将比例绘制为进度条并显示百分比。
  function progressBar(h, frac, kind) {
    var pct = Math.max(0, Math.min(100, (+frac || 0) * 100));
    return h('div.bt-progress' + (kind ? '.bt-progress--' + kind : ''), { title: pct.toFixed(1) + '%' },
      h('div.bt-progress__bar', h('div.bt-progress__fill', { style: { width: pct + '%' } })), h('span.bt-progress__text', pct.toFixed(1) + '%'));
  }

  // ---- places of a save's problems / 保存问题的位置 ----
  FitConfig.with(UI.scope('admin', ID)).place(function (path) {
    return /^bittorrent\b/.test(path || '') ? '#/' + ID + '/config' : null;
  });

  WB.module({
    id: ID, server: 'admin', group: 'gateway', order: 14, icon: 'download', label: 'm.bt', tag: 'tag.bt', words: WORDS,
    nav: [{ id: 'torrents', icon: 'download', label: 'n.torrents' }, { id: 'tracker', icon: 'radio', label: 'n.tracker' },
      { id: 'publish', icon: 'upload', label: 'n.publish' }, { id: 'config', icon: 'settings', label: 'n.config' }],
    setup: function (ui, sh) {
      var h = ui.h, t = ui.t;
      var C = FitConfig.with(ui);
      function api(method, path, body) { return ui.api(method, '/api' + path, body); }
      function fail(el, e) { UI.mount(el, h('div.callout.callout--danger', UI.icon('alert'), ' ', (e && e.message) || String(e))); }
      // off draws the callout of a role that does not run here. / 绘制角色未在此运行的提示。
      function off(el, key) {
        UI.mount(el, h('div.callout.callout--warn', UI.icon('info'), ' ', t(key), ' ', h('a', { href: '#/' + ID + '/config' }, t('r.toConfig'))));
      }
      function stateWord(st) { var w = t('s.' + st); return w === 's.' + st ? st : w; }
      function stateBadge(s) { return UI.badge(stateWord(s.state), stateKind(s), true); }
      function prioWord(p) { var w = t('prio.' + p); return w === 'prio.' + p ? String(p) : w; }
      function torrentPath(id) { return '/bittorrent/torrents/' + enc(id); }

      // categories reads the categories of the running config's client, once a page. / 读取运行配置中客户端的分类（每页一次）。
      function categories() {
        return api('GET', '/config').then(function (c) {
          var cl = c && c.bittorrent && c.bittorrent.client;
          return ((cl && cl.categories) || []).map(function (x) { return x.name; }).filter(Boolean);
        }).catch(function () { return []; });
      }

      // ---- the alternative rates, in the top bar / 顶栏中的备用速率 ----
      function altSpeed() {
        var sel = h('select.select.select--sm', { 'aria-label': t('alt.label'), style: { width: 'auto' }, disabled: true },
          ['auto', 'on', 'off'].map(function (m) { return h('option', { value: m }, t('alt.' + m)); }));
        var state = h('span.small.muted');
        var box = h('div.bt-alt', { title: t('alt.label') }, UI.icon('zap'), h('span.small', t('alt.label')), sel, state);
        function draw(a) {
          sel.disabled = UI.readOnly();
          sel.value = a.mode;
          UI.mount(state, a.active ? UI.badge(t('alt.active', { up: fmtRate(a.upload_rate), down: fmtRate(a.download_rate) }), 'warn')
            : h('span', { title: a.configured ? '' : t('alt.none') }, a.configured ? t('alt.idle') : t('alt.none')));
        }
        sel.onchange = function () {
          var mode = sel.value;
          sel.disabled = true;
          api('PUT', '/bittorrent/alt_speed', { mode: mode }).then(function (a) { draw(a); UI.toast(t('alt.set', { mode: t('alt.' + mode) })); })
            .catch(function (e) { UI.fail(e); load(); });
        };
        function load() { return api('GET', '/bittorrent/alt_speed').then(draw).catch(function () { box.hidden = true; }); }
        load();
        return { el: box, load: load, draw: draw };
      }

      // ---- the event stream / 事件流 ----
      // live opens the client's events on the console's route and hands each to onEvent(type, data), reconnecting with
      // Last-Event-ID after a break while anchor is on the page; onState(true|false) hears whether it streams. Through a
      // manager (UI.target()) it is not opened: the manager's pass-through answers whole responses.
      // live 经控制台的路由打开客户端事件流，把每个事件交给 onEvent(type, data)；anchor 仍在页面上时断开后带 Last-Event-ID 重连；
      // onState(true|false) 获知是否在流式接收。经管理端（UI.target()）时不打开：管理端的转发只回答完整的应答。
      function live(anchor, onEvent, onState) {
        if (UI.target() || !window.fetch || !window.AbortController) { onState(false); return; }
        var last = '', ac = null, gone = false, tries = 0;
        var watch = setInterval(function () {
          if (!document.body.contains(anchor)) { gone = true; clearInterval(watch); if (ac) ac.abort(); }
        }, 1000);
        function handle(block) {
          var ev = 'message', data = [], id = '';
          block.split('\n').forEach(function (line) {
            if (line.indexOf('event:') === 0) ev = line.slice(6).trim();
            else if (line.indexOf('data:') === 0) data.push(line.slice(5).replace(/^ /, ''));
            else if (line.indexOf('id:') === 0) id = line.slice(3).trim();
          });
          if (!data.length) return;
          if (id) last = id;
          var v;
          try { v = JSON.parse(data.join('\n')); } catch (e) { v = {}; }
          onEvent(ev, v);
        }
        function open() {
          if (gone) return;
          ac = new AbortController();
          var headers = { 'Accept': 'text/event-stream' };
          if (last) headers['Last-Event-ID'] = last;
          fetch(ui.base + '/bittorrent/events', { headers: headers, credentials: 'same-origin', signal: ac.signal }).then(function (r) {
            if (!r.ok || (r.headers.get('Content-Type') || '').indexOf('text/event-stream') < 0) {
              // Not streamable here (no role, the bound of streams, an older fit): the page polls.
              // 此处无法流式接收（无角色、流数已达上限或较旧的 fit）：页面改为轮询。
              onState(false);
              if (r.status === 503) setTimeout(open, 15000);
              return;
            }
            tries = 0;
            onState(true);
            var reader = r.body.getReader(), dec = new TextDecoder(), buf = '';
            function pump() {
              return reader.read().then(function (x) {
                if (x.done) throw new Error('closed');
                buf += dec.decode(x.value, { stream: true }).replace(/\r/g, '');
                for (var i = buf.indexOf('\n\n'); i >= 0; i = buf.indexOf('\n\n')) {
                  handle(buf.slice(0, i));
                  buf = buf.slice(i + 2);
                }
                return pump();
              });
            }
            return pump();
          }).catch(function () {
            onState(false);
            if (!gone && tries++ < 20) setTimeout(open, 5000);
          });
        }
        open();
      }

      // ---- the torrents / 种子列表 ----
      function listPage() {
        var el = sh.page('torrents', t('n.torrents'));
        var alt = altSpeed();
        sh.tools.appendChild(alt.el);
        var addBtn = UI.btn(t('l.add'), 'plus', function () { addDialog(cats, load); }, 'primary');
        sh.tools.appendChild(addBtn);
        var f = { state: '', category: null, q: '' };
        var picked = {}, shown = [], cats = [], events = [], streaming = false;
        var stateSel = h('select.select.select--sm', { 'aria-label': t('l.state'), style: { width: 'auto' } },
          [h('option', { value: '' }, t('l.allStates'))].concat(STATES.map(function (s) { return h('option', { value: s }, stateWord(s)); })));
        var catSel = h('select.select.select--sm', { 'aria-label': t('l.category'), style: { width: 'auto' } });
        var search = h('input.input.input--sm', { type: 'search', placeholder: t('l.search'), 'aria-label': t('l.search'), style: { width: '200px' } });
        var bulk = h('div.btn-row');
        var count = h('span.small.muted'), mode = h('span');
        var box = h('div'), evBox = h('div');
        UI.mount(el, h('div.stack',
          h('div.row.row--between', h('div.row', stateSel, catSel, search), h('div.row', count, mode)),
          bulk, box, UI.card(t('l.events'), evBox)));
        box.appendChild(h('div', UI.icon('refresh'), ' ', t('loading')));
        function drawCats() {
          UI.mount(catSel, [h('option', { value: '*' }, t('l.allCats')), h('option', { value: '' }, t('l.noCat'))].concat(cats.map(function (c) {
            return h('option', { value: c }, c);
          })));
          catSel.value = f.category == null ? '*' : f.category;
        }
        drawCats();
        categories().then(function (c) { cats = c; drawCats(); });
        stateSel.onchange = function () { f.state = stateSel.value; load(); };
        catSel.onchange = function () { f.category = catSel.value === '*' ? null : catSel.value; load(); };
        var typing = null;
        search.oninput = function () { clearTimeout(typing); typing = setTimeout(function () { f.q = search.value.trim(); load(); }, 300); };

        function pickedIDs() { return shown.filter(function (s) { return picked[s.id]; }); }
        function drawBulk() {
          var ps = pickedIDs(), ro = UI.readOnly();
          var b = [h('span.small.muted', t('l.picked', { n: ps.length })),
            UI.btn(t('l.pause'), 'stop', function () { bulkAction(ps, 'pause'); }),
            UI.btn(t('l.resume'), 'play', function () { bulkAction(ps, 'resume'); }),
            UI.btn(t('l.remove'), 'trash', function () { bulkRemove(ps); }, 'danger')];
          b.slice(1).forEach(function (x) { x.disabled = !ps.length || ro; });
          UI.mount(bulk, b);
        }
        // each runs fn on every torrent of list, one after another, and toasts what came of it.
        // each 依次对 list 中的每个种子执行 fn，并提示结果。
        function each(list, fn, doneKey) {
          var n = 0, errs = [];
          return list.reduce(function (p, s) {
            return p.then(function () { return fn(s).then(function () { n++; }, function (e) { errs.push(t('l.failed', { name: s.name || s.id, error: e.message })); }); });
          }, Promise.resolve()).then(function () {
            if (n) UI.toast(t(doneKey, { n: n }));
            if (errs.length) UI.toast(errs.join('\n'), 'error');
            load();
          });
        }
        function bulkAction(list, action) {
          return each(list, function (s) { return api('POST', torrentPath(s.id), { action: action }); }, action === 'pause' ? 'l.paused' : 'l.resumed');
        }
        function bulkRemove(list) {
          removeDialog(t('l.remove.q', { n: list.length }), t('l.withData'), function (data) {
            return each(list, function (s) {
              return api('DELETE', torrentPath(s.id) + (data ? '?delete_data=1' : '')).then(function () { delete picked[s.id]; });
            }, 'l.removed');
          });
        }

        function load() {
          var q = ['limit=' + LIST];
          if (f.state) q.push('state=' + enc(f.state));
          if (f.category != null) q.push('category=' + enc(f.category));
          if (f.q) q.push('q=' + enc(f.q));
          return api('GET', '/bittorrent/torrents?' + q.join('&')).then(function (r) {
            if (!document.body.contains(box)) return;
            shown = r.torrents || [];
            UI.mount(count, t('l.count', { n: shown.length, total: r.total || 0 }));
            drawList(r);
            drawBulk();
          }).catch(function (e) {
            if (!document.body.contains(box)) return;
            if (e.status === 404) { off(box, 'r.off'); addBtn.disabled = true; alt.el.hidden = true; bulk.hidden = true; return; }
            fail(box, e);
          });
        }
        function drawList(r) {
          if (!shown.length) {
            UI.mount(box, UI.empty(f.state || f.category != null || f.q ? t('l.noMatch') : t('l.none'), 'download'));
            return;
          }
          var all = h('input', { type: 'checkbox', 'aria-label': t('l.pickAll'), title: t('l.pickAll'),
            checked: shown.every(function (s) { return picked[s.id]; }),
            onchange: function (e) { shown.forEach(function (s) { picked[s.id] = e.target.checked; }); drawList(r); drawBulk(); } });
          UI.mount(box, UI.card(null, UI.table([
            { label: all, cls: 'bt-pick', render: function (s) {
              return h('input', { type: 'checkbox', checked: !!picked[s.id], 'aria-label': s.name || s.id,
                onchange: function (e) { picked[s.id] = e.target.checked; drawBulk(); } });
            } },
            { label: t('l.name'), render: function (s) {
              return h('div.bt-name', h('a', { href: '#/' + ID + '/torrent/' + enc(s.id), title: s.name || s.id }, h('b', s.name || s.id)),
                s.error ? h('div.small.bt-bad.bt-clip', { title: s.error }, s.error) : null,
                s.temporary ? h('span.small.muted', ' ' + t('l.temporary')) : null);
            } },
            { label: t('l.size'), cls: 'bt-num', render: function (s) { return fmtBytes(s.wanted || s.size); } },
            { label: t('l.progress'), render: function (s) { return progressBar(h, s.progress, s.complete ? 'ok' : s.state === 'error' ? 'danger' : ''); } },
            { label: t('l.state'), render: function (s) {
              return h('div', stateBadge(s), s.held ? h('div.small.muted', t('held.' + s.held)) : null);
            } },
            { label: t('l.down'), cls: 'bt-num', render: function (s) { return fmtRate(s.download_rate); } },
            { label: t('l.up'), cls: 'bt-num', render: function (s) { return fmtRate(s.upload_rate); } },
            { label: t('l.eta'), cls: 'bt-num', render: function (s) { return fmtETA(eta(s)); } },
            { label: t('l.ratio'), cls: 'bt-num', render: function (s) { return ratio(s).toFixed(2); } },
            { label: t('l.peers'), cls: 'bt-num', render: function (s) { return t('l.peersOf', { peers: s.peers || 0, seeds: s.seeders || 0 }); } },
            { label: t('l.category'), render: function (s) { return s.category ? UI.badge(s.category, 'primary') : h('span.muted', '—'); } }
          ], shown), null, true));
        }

        function drawEvents() {
          if (!events.length) { UI.mount(evBox, h('div.small.muted', t('l.noEvents'))); return; }
          UI.mount(evBox, h('ul.bt-events', events.map(function (e) {
            var what = e.type === 'state' ? t('ev.fromTo', { from: stateWord(e.previous || '?'), to: stateWord(e.state || '?') })
              : e.type === 'alt_speed' ? t('alt.' + (e.state || 'off')) : e.type === 'removed' && e.data_deleted ? t('ev.dataDeleted') : '';
            return h('li', h('span.small.muted', UI.fmtTime(unix(e.time)).slice(11)), ' ',
              UI.badge(t('ev.' + e.type) === 'ev.' + e.type ? e.type : t('ev.' + e.type), e.type === 'error' ? 'danger' : e.type === 'completed' ? 'ok' : ''), ' ',
              e.torrent ? h('a', { href: '#/' + ID + '/torrent/' + enc(e.torrent) }, e.name || e.torrent.slice(0, 12)) : null,
              what ? ' ' + what : '', e.error ? h('span.small.bt-bad', ' ' + e.error) : null, e.peer ? h('code.small', ' ' + e.peer) : null);
          })));
        }
        function drawMode() { UI.mount(mode, UI.badge(streaming ? t('l.live') : t('l.polling'), streaming ? 'ok' : '', true)); }
        var soon = null;
        live(box, function (type, e) {
          if (type === 'reset') { events.unshift({ type: 'reset', time: new Date().toISOString() }); load(); }
          else if (type === 'end' || type === 'lagged') return;
          else {
            events.unshift(e);
            if (e.type === 'completed') UI.toast(t('ev.doneToast', { name: e.name || e.torrent }), 'ok');
            if (e.type === 'error') UI.toast(t('ev.errToast', { name: e.name || e.torrent, error: e.error || '' }), 'error');
            if (e.type === 'alt_speed') alt.load();
            // A burst of events reads the list once. / 一阵事件只读取一次列表。
            clearTimeout(soon);
            soon = setTimeout(load, 150);
          }
          if (events.length > EVENTS) events.length = EVENTS;
          drawEvents();
        }, function (on) { streaming = on; drawMode(); });
        drawEvents();
        drawMode();
        load();
        UI.every(POLL, load);
      }

      // removeDialog asks before a removal, with a checkbox for the data; go(withData) removes.
      // removeDialog 删除前确认，带“同时删除数据”复选框；go(withData) 执行删除。
      function removeDialog(question, dataLabel, go) {
        var data = h('input', { type: 'checkbox' });
        UI.modal({
          title: t('confirm'),
          body: h('div.stack', h('p', { style: { margin: 0 } }, question), h('label.row', data, h('span', dataLabel)), h('div.small.muted', t('l.dataNote'))),
          actions: [{ label: t('cancel') }, { label: t('l.remove'), kind: 'danger', onclick: function () { return go(data.checked); } }]
        });
      }

      // ---- the add dialog / 添加对话框 ----
      function addDialog(cats, done) {
        var how = 'magnet';
        var magnet = h('textarea.input.bt-magnet', { rows: 3, placeholder: t('a.magnetHint') });
        var file = h('input', { type: 'file', accept: '.torrent,application/x-bittorrent' });
        var url = h('input.input', { type: 'url', placeholder: 'https://example.com/file.torrent' });
        var cat = h('select.select', [h('option', { value: '' }, t('l.noCat'))].concat(cats.map(function (c) { return h('option', { value: c }, c); })));
        var paused = h('input', { type: 'checkbox' }), seq = h('input', { type: 'checkbox' });
        var err = h('div.callout.callout--danger', { hidden: true });
        var src = h('div');
        function drawSrc() {
          UI.mount(src, UI.tabs([['magnet', t('a.magnet')], ['file', t('a.file')], ['url', t('a.url')]], how, function (k) { how = k; drawSrc(); }),
            how === 'magnet' ? UI.field(t('a.magnet'), magnet) : how === 'file' ? UI.field(t('a.file'), file, t('a.fileHint')) : UI.field(t('a.url'), url, t('a.urlHint')));
        }
        drawSrc();
        UI.modal({
          title: t('a.title'), wide: true,
          body: h('div.form', src, UI.field(t('a.category'), cat), h('label.row', paused, h('span', t('a.paused'))), h('label.row', seq, h('span', t('a.sequential'))), err),
          actions: [{ label: t('cancel') }, { label: t('a.add'), kind: 'primary', onclick: function () {
            err.hidden = true;
            var opts = { category: cat.value, paused: paused.checked, sequential: seq.checked }, p;
            if (how === 'file') {
              var fl = file.files && file.files[0];
              if (!fl) { err.textContent = t('a.need'); err.hidden = false; return false; }
              if (fl.size > 32 << 20) { err.textContent = t('a.big'); err.hidden = false; return false; }
              // The file goes raw, the options in the query (the Admin API's application/x-bittorrent).
              // 文件以原始字节发送，选项放在查询参数中（Admin API 的 application/x-bittorrent）。
              var q = 'category=' + enc(opts.category) + '&paused=' + opts.paused + '&sequential=' + opts.sequential;
              p = ui.raw('POST', '/api/bittorrent/torrents?' + q, fl, { 'Content-Type': 'application/x-bittorrent' }).then(function (r) {
                return r.text().then(function (txt) {
                  var j = null;
                  try { j = JSON.parse(txt); } catch (e) { /* not json / 非 JSON */ }
                  if (!r.ok) throw new Error((j && j.error) || r.status + ' ' + r.statusText);
                  return j;
                });
              });
            } else {
              var v = (how === 'magnet' ? magnet.value : url.value).trim();
              if (!v) { err.textContent = t('a.need'); err.hidden = false; return false; }
              opts[how] = v;
              p = api('POST', '/bittorrent/torrents', opts);
            }
            return p.then(function (st) {
              UI.toast(t('a.added', { name: (st && (st.name || st.id)) || '' }));
              done();
            }).catch(function (e) { err.textContent = e.message; err.hidden = false; return false; });
          } }]
        });
      }

      // ---- a torrent / 单个种子 ----
      function tabsOf(id, active) {
        var base = '#/' + ID + '/torrent/' + enc(id);
        return h('div.tabs', [['overview', base], ['files', base + '/files'], ['peers', base + '/peers'], ['trackers', base + '/trackers']].map(function (x) {
          return h('a.tab' + (x[0] === active ? '.is-active' : ''), { href: x[1] }, t('n.' + x[0]));
        }));
      }
      function torrentPage(p) {
        var id = p.id, tab = p.tab || 'overview';
        if (['overview', 'files', 'peers', 'trackers'].indexOf(tab) < 0) tab = 'overview';
        var el = sh.page('torrents', id.slice(0, 12), t('m.bt') + ' / ' + t('n.torrents') + ' / ' + id.slice(0, 12));
        var alt = altSpeed();
        sh.tools.appendChild(alt.el);
        var head = h('div'), body = h('div.stack'), tools = h('div.btn-row');
        UI.mount(el, h('div.stack', head, tabsOf(id, tab), body));
        sh.tools.appendChild(tools);
        var cats = null, drawnForms = false, last = null;
        categories().then(function (c) { cats = c; if (last && tab === 'overview') drawForms(last); });
        var statsBox = h('div'), formsBox = h('div');
        if (tab === 'overview') UI.mount(body, statsBox, formsBox);
        function load() {
          return api('GET', torrentPath(id)).then(function (s) {
            if (!document.body.contains(body)) return;
            last = s;
            UI.mount(head, h('div.row', h('h2.bt-title', s.name || s.id), stateBadge(s), s.held ? UI.badge(t('held.' + s.held), 'warn') : null,
              s.category ? UI.badge(s.category, 'primary') : null));
            drawTools(s);
            if (tab === 'overview') {
              drawStats(s);
              if (!drawnForms) drawForms(s);
            } else if (tab === 'files') drawFiles(s);
            else if (tab === 'peers') loadPeers();
            else if (tab === 'trackers' && !body.firstChild) loadTrackers();
          }).catch(function (e) {
            if (!document.body.contains(body)) return;
            if (e.status === 404) {
              UI.mount(head);
              UI.mount(body, h('div.callout.callout--warn', UI.icon('info'), ' ', /role/.test(e.message) ? t('r.off') : t('t.notFound', { id: id })),
                h('div.btn-row', h('a.btn', { href: '#/' + ID + '/torrents' }, UI.icon('download'), t('t.list'))));
              UI.mount(tools);
              return;
            }
            fail(body, e);
          });
        }
        function act(fn, msg) {
          return fn().then(function () { if (msg) UI.toast(msg); return load(); }).catch(UI.fail);
        }
        function drawTools(s) {
          var name = s.name || s.id;
          var pr = s.paused
            ? UI.btn(t('l.resume'), 'play', function () { act(function () { return api('POST', torrentPath(id), { action: 'resume' }); }, t('t.resumedMsg', { name: name })); }, 'primary')
            : UI.btn(t('l.pause'), 'stop', function () { act(function () { return api('POST', torrentPath(id), { action: 'pause' }); }, t('t.pausedMsg', { name: name })); });
          var re = UI.btn(t('t.recheck'), 'refresh', function () {
            UI.confirm(t('t.recheck.q', { name: name }), t('t.recheck')).then(function (ok) {
              if (ok) act(function () { return api('POST', torrentPath(id), { action: 'recheck' }); }, t('t.rechecking', { name: name }));
            });
          });
          var rm = UI.btn(t('l.remove'), 'trash', function () {
            removeDialog(t('t.remove.q', { name: name }), t('l.withData1'), function (data) {
              return api('DELETE', torrentPath(id) + (data ? '?delete_data=1' : '')).then(function () {
                UI.toast(t('t.removed', { name: name }));
                location.hash = '#/' + ID + '/torrents';
              });
            });
          }, 'danger');
          if (UI.readOnly()) [pr, re, rm].forEach(function (b) { b.disabled = true; });
          UI.mount(tools, pr, re, rm);
        }
        function drawStats(s) {
          var r = ratio(s);
          UI.mount(statsBox, h('div.stack',
            s.error ? h('div.callout.callout--danger', UI.icon('alert'), ' ', h('b', t('t.error')), ' ', s.error) : null,
            h('div.bt-stats',
              UI.stat('download', t('l.progress'), (s.progress * 100).toFixed(1) + '%', fmtBytes(s.completed) + ' / ' + fmtBytes(s.wanted), s.complete ? 'ok' : ''),
              UI.stat('activity', t('t.rates'), '↓ ' + fmtRate(s.download_rate), '↑ ' + fmtRate(s.upload_rate)),
              UI.stat('swap', t('t.downloaded') + ' / ' + t('t.uploaded'), fmtBytes(s.downloaded), fmtBytes(s.uploaded)),
              UI.stat('users', t('l.peers'), String(s.peers || 0), t('l.peersOf', { peers: s.peers || 0, seeds: s.seeders || 0 })),
              UI.stat('refresh', t('l.ratio'), r.toFixed(2), t('l.eta') + ': ' + fmtETA(eta(s))),
              UI.stat('layers', t('t.pieces'), t('t.piecesOf', { have: s.pieces_have || 0, all: s.pieces || 0 }), progressBar(h, s.pieces ? s.pieces_have / s.pieces : 0))),
            UI.card(null, UI.kv([
              [t('t.id'), h('code', s.id)],
              s.info_hash ? [t('t.hash1'), h('code', s.info_hash)] : null,
              s.info_hash_v2 ? [t('t.hash2'), h('code', s.info_hash_v2)] : null,
              [t('l.size'), fmtBytes(s.size) + (s.wanted !== s.size ? ' · ' + t('t.wanted') + ' ' + fmtBytes(s.wanted) : '')],
              [t('t.dir'), h('code', s.dir || '—')],
              [t('t.added'), UI.fmtTime(unix(s.added))],
              s.held ? [t('t.held'), t('held.' + s.held)] : null,
              s.magnet ? [t('t.magnet'), h('div.row', h('code.bt-clip', { title: s.magnet }, s.magnet), UI.btn(t('copy'), 'copy', function () { UI.copy(s.magnet); }, 'sm'))] : null
            ]))));
        }
        function limitsText(l) {
          if (!l) return t('t.noLimits');
          var parts = [];
          if (l.upload_rate) parts.push('↑ ' + fmtRate(l.upload_rate));
          if (l.download_rate) parts.push('↓ ' + fmtRate(l.download_rate));
          if (l.max_conns) parts.push(t('t.maxConns') + ' ' + l.max_conns);
          if (l.upload_slots) parts.push(t('t.slots') + ' ' + l.upload_slots);
          if (l.seed_ratio) parts.push(t('t.seedRatio') + ' ' + l.seed_ratio);
          if (l.seed_time) parts.push(t('t.seedTime') + ' ' + (l.seed_time < 0 ? '∞' : UI.fmtDur(l.seed_time / 1e9)));
          return parts.length ? parts.join(' · ') : t('t.noLimits');
        }
        function patch(body) {
          return api('PATCH', torrentPath(id), body).then(function (s) { UI.toast(t('t.changed')); drawnForms = false; last = s; drawForms(s); drawStats(s); return s; });
        }
        // drawForms draws the settings once, and again after a change, never under the person's typing.
        // drawForms 只绘制一次设置表单，修改后再重绘，不会打断正在输入的内容。
        function drawForms(s) {
          drawnForms = true;
          var ro = UI.readOnly();
          var cat = h('select.select', { disabled: ro }, [h('option', { value: '' }, t('l.noCat'))].concat((cats || []).concat(s.category && (cats || []).indexOf(s.category) < 0 ? [s.category] : []).map(function (c) {
            return h('option', { value: c, selected: c === (s.category || '') }, c);
          })));
          cat.value = s.category || '';
          cat.onchange = function () { patch({ category: cat.value }).catch(function (e) { UI.fail(e); cat.value = s.category || ''; }); };
          var pos = h('input.input', { type: 'number', min: 0, value: s.queue_position, style: { width: '110px' }, disabled: ro });
          var setPos = UI.btn(t('t.set'), 'check', function () { patch({ queue_position: +pos.value }).catch(UI.fail); }, 'sm');
          var seq = h('input', { type: 'checkbox', checked: !!s.sequential, disabled: ro, onchange: function (e) { patch({ sequential: e.target.checked }).catch(UI.fail); } });
          var l = s.limits || {};
          var fields = [['upload_rate', 't.upRate'], ['download_rate', 't.downRate'], ['max_conns', 't.maxConns'], ['upload_slots', 't.slots'], ['seed_ratio', 't.seedRatio']].map(function (x) {
            return { key: x[0], label: t(x[1]), input: h('input.input', { type: 'number', step: x[0] === 'seed_ratio' ? '0.1' : '1', value: l[x[0]] || '', disabled: ro }) };
          });
          var seedTime = h('input.input', { placeholder: '72h', value: durText(l.seed_time), disabled: ro });
          var lerr = h('div.callout.callout--danger', { hidden: true });
          var save = UI.btn(t('t.saveLimits'), 'save', function () {
            var lim = {}, bad = null;
            fields.forEach(function (f) {
              var v = f.input.value.trim();
              if (v === '') return;
              if (isNaN(+v)) bad = f.label; else lim[f.key] = +v;
            });
            if (bad) { lerr.textContent = t('t.badNumber', { field: bad }); lerr.hidden = false; return; }
            if (seedTime.value.trim()) lim.seed_time = seedTime.value.trim();
            lerr.hidden = true;
            UI.busy(save, patch({ limits: lim })).catch(function (e) { lerr.textContent = e.message; lerr.hidden = false; });
          }, 'primary');
          var clear = UI.btn(t('t.clearLimits'), 'x', function () { UI.busy(clear, patch({ limits: null })).catch(UI.fail); });
          if (ro) { setPos.disabled = save.disabled = clear.disabled = true; }
          UI.mount(formsBox, h('div.grid.grid--2.bt-forms',
            UI.card(t('t.settings'), h('div.form',
              UI.field(t('l.category'), cat),
              UI.field(t('t.queue'), h('div.row', pos, setPos), t('t.queueHint')),
              h('label.row', seq, h('span', t('t.sequential'))))),
            UI.card(t('t.limits'), h('div.form',
              h('div.small.muted', t('t.limitsNote', { eff: limitsText(s.effective_limits) })),
              h('div.bt-limits', fields.map(function (f) { return UI.field(f.label, f.input); }), UI.field(t('t.seedTime'), seedTime, t('t.seedTimeHint'))),
              lerr, h('div.btn-row', save, clear)))));
        }
        var filesPicked = {};
        function drawFiles(s) {
          // Not under the person's hand: a select open or focused keeps the table. / 不打断操作：下拉框获得焦点时不重绘表格。
          if (body.contains(document.activeElement) && document.activeElement.tagName === 'SELECT') return;
          var files = s.files || [];
          if (!files.length) { UI.mount(body, UI.empty(t('f.none'), 'log')); return; }
          var ro = UI.readOnly();
          function setPrio(list, p) {
            return api('PATCH', torrentPath(id), { priorities: list.map(function (f) { return { index: f.index, priority: p }; }) })
              .then(function (st) { UI.toast(t('t.changed')); drawFiles(st); }).catch(UI.fail);
          }
          var bulkSel = h('select.select.select--sm', { style: { width: 'auto' }, disabled: ro }, PRIORITIES.map(function (p) { return h('option', { value: p }, prioWord(p)); }));
          var bulkBtn = UI.btn(t('t.set'), 'check', function () {
            var list = files.filter(function (f) { return filesPicked[f.index]; });
            if (list.length) setPrio(list, +bulkSel.value);
          }, 'sm');
          bulkBtn.disabled = ro;
          var all = h('input', { type: 'checkbox', 'aria-label': t('l.pickAll'), checked: files.every(function (f) { return filesPicked[f.index]; }),
            onchange: function (e) { files.forEach(function (f) { filesPicked[f.index] = e.target.checked; }); drawFiles(s); } });
          UI.mount(body, h('div.stack', h('div.row', h('span.small', t('f.setPicked')), bulkSel, bulkBtn),
            UI.card(null, UI.table([
              { label: all, cls: 'bt-pick', render: function (f) {
                return h('input', { type: 'checkbox', checked: !!filesPicked[f.index], 'aria-label': f.path, onchange: function (e) { filesPicked[f.index] = e.target.checked; } });
              } },
              { label: t('f.path'), render: function (f) { return h('span.bt-path', { title: f.path }, f.path); } },
              { label: t('l.size'), cls: 'bt-num', render: function (f) { return fmtBytes(f.length); } },
              { label: t('l.progress'), render: function (f) { return progressBar(h, f.length ? f.completed / f.length : 0, f.priority === 0 ? 'muted' : ''); } },
              { label: t('f.priority'), render: function (f) {
                var sel = h('select.select.select--sm', { style: { width: 'auto' }, disabled: ro, 'aria-label': t('f.priority') },
                  PRIORITIES.map(function (p) { return h('option', { value: p, selected: p === f.priority }, prioWord(p)); }));
                sel.value = String(f.priority);
                sel.onchange = function () { setPrio([f], +sel.value); };
                return sel;
              } }
            ], files), null, true)));
        }
        function loadPeers() {
          return api('GET', torrentPath(id) + '/peers').then(function (r) {
            if (!document.body.contains(body)) return;
            var peers = r.peers || [];
            if (!peers.length) { UI.mount(body, UI.empty(t('p.none'), 'users')); return; }
            UI.mount(body, UI.card(null, UI.table([
              { label: t('p.addr'), render: function (p) { return h('code', p.addr); } },
              { label: t('p.client'), render: function (p) { return p.client || h('span.muted', '—'); } },
              { label: t('p.flags'), render: function (p) {
                return h('span.small', [p.outgoing ? t('p.out') : t('p.in'), p.utp ? t('p.utp') : 'TCP', p.encrypted ? t('p.enc') : '', p.seed ? t('p.seed') : '',
                  p.peer_choking ? t('p.choked') : '', p.snubbed ? t('p.snubbed') : ''].filter(Boolean).join(' · '));
              } },
              { label: t('p.pieces'), cls: 'bt-num', render: function (p) { return String(p.pieces || 0); } },
              { label: t('l.down'), cls: 'bt-num', render: function (p) { return fmtRate(p.download_rate); } },
              { label: t('l.up'), cls: 'bt-num', render: function (p) { return fmtRate(p.upload_rate); } },
              { label: t('t.downloaded') + ' / ' + t('t.uploaded'), cls: 'bt-num', render: function (p) { return fmtBytes(p.downloaded) + ' / ' + fmtBytes(p.uploaded); } }
            ], peers), null, true));
          }).catch(function (e) { if (document.body.contains(body)) fail(body, e); });
        }
        function loadTrackers() {
          return api('GET', torrentPath(id) + '/trackers').then(function (r) {
            if (!document.body.contains(body)) return;
            var tr = r.trackers || [], ws = r.web_seeds || [];
            UI.mount(body, h('div.small.muted', t('tr.note')),
              UI.card(t('n.trackers'), tr.length ? UI.table([
                { label: t('tr.tier'), cls: 'bt-num', render: function (x) { return String(x.tier); } },
                { label: t('tr.url'), render: function (x) { return h('code.bt-clip', { title: x.url }, x.url); } }
              ], tr) : UI.empty(t('tr.none'), 'radio'), null, !!tr.length),
              ws.length ? UI.card(t('tr.seeds'), h('ul.bt-list', ws.map(function (u) { return h('li', h('code', u)); }))) : null);
          }).catch(function (e) { if (document.body.contains(body)) fail(body, e); });
        }
        load();
        if (tab !== 'trackers') UI.every(tab === 'files' ? POLL * 2 : POLL, load);
      }

      // ---- the tracker / Tracker ----
      function trackerPage() {
        var el = sh.page('tracker', t('n.tracker'));
        var statsBox = h('div'), swarmBox = h('div'), keyBox = h('div');
        var offset = 0, priv = false;
        UI.mount(el, h('div.stack', statsBox, swarmBox, keyBox));
        statsBox.appendChild(h('div', UI.icon('refresh'), ' ', t('loading')));
        function loadStats() {
          return api('GET', '/bittorrent/tracker/stats').then(function (s) {
            if (!document.body.contains(statsBox)) return;
            priv = s.private;
            var ref = s.refused || {}, refused = Object.keys(ref).reduce(function (n, k) { return n + (ref[k] || 0); }, 0);
            UI.mount(statsBox, h('div.stack', h('div.bt-stats',
              UI.stat('layers', t('k.torrents'), String(s.torrents), t('k.of', { n: s.torrents, max: s.max_torrents })),
              UI.stat('users', t('k.peers'), String(s.peers), t('k.of', { n: s.peers, max: s.max_peers })),
              UI.stat('radio', t('k.announces'), String(s.announces), t('k.scrapes') + ': ' + s.scrapes),
              UI.stat('shield', t('k.refused'), String(refused), Object.keys(ref).sort().map(function (k) { return t('k.ref.' + k) + ' ' + ref[k]; }).join(' · '), refused ? 'warn' : ''),
              UI.stat('lock', t('k.access'), s.private ? t('k.private') : t('k.public'),
                [s.allowed ? t('k.allowedN', { n: s.allowed }) : t('k.noList'), s.udp ? t('k.udp') + ' ' + s.udp : ''].filter(Boolean).join(' · ')))));
            drawKey();
          }).catch(function (e) {
            if (!document.body.contains(statsBox)) return;
            if (e.status === 404) { off(statsBox, 'r.trackerOff'); UI.mount(swarmBox); UI.mount(keyBox); return; }
            fail(statsBox, e);
          });
        }
        function loadSwarms() {
          return api('GET', '/bittorrent/tracker/torrents?offset=' + offset + '&limit=' + SWARMS).then(function (r) {
            if (!document.body.contains(swarmBox)) return;
            var list = r.torrents || [];
            if (!list.length && !offset) { UI.mount(swarmBox, UI.card(t('k.swarms'), h('div.small.muted', t('k.swarmsNote')))); return; }
            var prev = UI.btn(t('k.prev'), null, function () { offset = Math.max(0, offset - SWARMS); loadSwarms(); }, 'sm');
            var next = UI.btn(t('k.next'), null, function () { offset += SWARMS; loadSwarms(); }, 'sm');
            prev.disabled = offset === 0;
            next.disabled = offset + list.length >= (r.total || 0);
            UI.mount(swarmBox, UI.card(t('k.swarms'), h('div.stack', h('div.small.muted', t('k.swarmsNote')), UI.table([
              { label: t('k.hash'), render: function (x) { return h('code', x.info_hash); } },
              { label: t('k.seeders'), cls: 'bt-num', render: function (x) { return String(x.seeders); } },
              { label: t('k.leechers'), cls: 'bt-num', render: function (x) { return String(x.leechers); } },
              { label: t('k.completed'), cls: 'bt-num', render: function (x) { return String(x.completed); } },
              { label: t('k.tracked'), render: function (x) { return x.tracked ? UI.badge(t('yes'), 'ok') : h('span.muted', t('no')); } }
            ], list), r.total > SWARMS ? h('div.row', prev, next, h('span.small.muted', (offset + 1) + '–' + (offset + list.length) + ' / ' + r.total)) : null)));
          }).catch(function () { if (document.body.contains(swarmBox)) UI.mount(swarmBox); });
        }
        var keyDrawn = false;
        function drawKey() {
          if (keyDrawn) return;
          keyDrawn = true;
          if (!priv) { UI.mount(keyBox, UI.card(t('k.passkey'), h('div.small.muted', t('k.publicNote')))); return; }
          var user = h('input.input', { placeholder: 'alice', style: { width: '220px' } });
          var out = h('div');
          var go = UI.btn(t('k.lookup'), 'key', function () {
            var u = user.value.trim();
            if (!u) return;
            UI.busy(go, api('GET', '/bittorrent/tracker/passkey?user=' + enc(u))).then(function (r) {
              UI.mount(out, h('div.row', h('code', r.passkey), UI.btn(t('copy'), 'copy', function () { UI.copy(r.passkey); }, 'sm')));
            }).catch(function (e) { fail(out, e); });
          }, 'primary');
          user.onkeydown = function (e) { if (e.key === 'Enter') go.click(); };
          UI.mount(keyBox, UI.card(t('k.passkey'), h('div.stack', h('div.small.muted', t('k.passkeyNote')), h('div.row', UI.field(t('k.user'), user), go), out)));
        }
        loadStats().then(loadSwarms);
        UI.every(POLL * 2, loadStats);
      }

      // ---- publishing / 发布 ----
      function publishPage() {
        var el = sh.page('publish', t('n.publish'));
        var box = h('div.stack');
        UI.mount(el, box);
        box.appendChild(h('div', UI.icon('refresh'), ' ', t('loading')));
        function copyBtn(magnet) {
          var b = UI.btn('', 'copy', function () { UI.copy(magnet); }, 'sm ghost');
          b.title = t('b.copyMagnet');
          b.setAttribute('aria-label', t('b.copyMagnet'));
          return b;
        }
        function torrentHref(name, tid) { return ui.base + '/bittorrent/publish/' + enc(name) + '/torrents/' + enc(tid); }
        function load() {
          return api('GET', '/bittorrent/publish').then(function (r) {
            if (!document.body.contains(box)) return;
            var entries = r.entries || [];
            if (!entries.length) { UI.mount(box, UI.empty(t('b.none'), 'upload')); return; }
            UI.mount(box, entries.map(function (e) {
              var ts = e.torrents || [];
              return UI.card(e.name, h('div.stack',
                e.error ? h('div.callout.callout--danger', UI.icon('alert'), ' ', e.error) : null,
                UI.kv([[t('b.dir'), h('code', e.dir)], [t('b.mode'), e.mode], e.web_seed ? [t('b.webSeed'), h('code', e.web_seed)] : null,
                  [t('b.scanned'), e.scanned ? UI.ago(unix(e.scanned)) : t('never')], e.pending ? [t('b.pending'), t('b.pendingN', { n: e.pending })] : null]),
                (e.problems || []).length ? h('details.bt-problems', h('summary', UI.icon('alert'), ' ', t('b.problems'), ' (' + (e.problems.length + (e.problems_dropped || 0)) + ')'),
                  h('ul', e.problems.map(function (p) { return h('li.small', p); }), e.problems_dropped ? h('li.small.muted', t('b.dropped', { n: e.problems_dropped })) : null)) : null,
                ts.length ? UI.table([
                  { label: t('l.name'), render: function (x) {
                    // Published torrents are seeded beside the client's list, not in it: no link to a torrent's page.
                    // 发布的种子在客户端列表之外做种：不链接到单个种子页。
                    return h('div', h('b', x.path || x.name), h('div.small.muted', h('code', x.id.slice(0, 12))), x.error ? h('div.small.bt-bad', x.error) : null);
                  } },
                  { label: t('l.size'), cls: 'bt-num', render: function (x) { return fmtBytes(x.size); } },
                  { label: t('b.files'), cls: 'bt-num', render: function (x) { return String(x.files); } },
                  { label: t('l.state'), render: function (x) { return UI.badge(stateWord(x.state), stateKind(x), true); } },
                  { label: t('b.built'), render: function (x) { return UI.ago(unix(x.built)); } },
                  { label: t('b.links'), render: function (x) {
                    return h('div.btn-row', h('a.btn.btn--sm', { href: torrentHref(e.name, x.id), download: (x.name || x.id) + '.torrent' }, UI.icon('download'), t('b.torrentFile')),
                      h('a.btn.btn--sm', { href: x.magnet, title: x.magnet }, UI.icon('link'), t('b.magnet')),
                      copyBtn(x.magnet));
                  } }
                ], ts) : h('div.small.muted', t('b.noTorrents'))));
            }));
          }).catch(function (e) {
            if (!document.body.contains(box)) return;
            if (e.status === 404) { off(box, 'r.off'); return; }
            fail(box, e);
          });
        }
        load();
        UI.every(POLL * 2.5, load);
      }

      function configPage() {
        var el = sh.page('config', t('n.config'));
        return C.page(el, sh, function (body) { body.appendChild(C.section('bittorrent')); }, { members: ['bittorrent'] });
      }

      return {
        routes: {
          'torrents': listPage, 'torrent/:id': torrentPage, 'torrent/:id/:tab': torrentPage,
          'tracker': trackerPage, 'publish': publishPage, 'config': configPage
        },
        fallback: 'torrents'
      };
    }
  });
})();
