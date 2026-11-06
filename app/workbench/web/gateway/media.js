// The media library's pages (I138): one module in the flat menu, beside streaming and media auth, for the whole video
// on demand and channels track (I132-I136). Its pages read the Admin API and edit the config's shared draft:
//
//   library            each library's items, searched, filtered (codec, resolution, problems) and paged from
//                      GET /media/library/<name>; a rescan with its progress
//   item/:lib/:id      an item: its renditions, tracks, codecs, audio, subtitles, poster and sidecar, its probe error, and
//                      a preview player (hls.js, vendor/hls.min.js, loaded when first needed) on a URL that
//                      POST /media/preview signs for the logged-in user, good for two minutes
//   channels           each channel of streaming.channels as a timeline: its list, dragged in from the library, and its
//                      schedule on a day in its time zone, loop and shuffle — edits of the shared draft, applied by Save
//                      and apply from the next item on; what plays now and next, start, stop, skip, a preview; and every
//                      push target's state, error, bytes and bit rate
//   playback           a policy's url_signing: a test URL issued as media_tokens issues one, played and never stored; the
//                      keys by name and age (never a secret), rotated one or all after a confirm; the live playback
//                      sessions, paged, each endable, with each subject's count against max_sessions
//   metrics            per library, vod handler, policy and channel: viewers, requests and bytes a second, the caches'
//                      hit rates and bytes, the file pool's queue and refusals, the 403s by reason (GET /media/stats)
//   config, vod, channels_config, url_signing
//                      the config pages: media_library, the vod routes, streaming.channels with push, and each media_auth
//                      policy's url_signing, with the UI/.conf/.yaml/.json tabs of every config page (I120); a save's
//                      problems at those members link back here
//
// No page keeps a URL, a token or a signature: a preview's URL lives in its player only, the test URL in its form until
// the page changes; secrets never reach the browser. Ask AI (I128): an item's probe error (explain), a push target in
// error and the 403s by reason (diagnose). Needs ui.js, workbench.js and config.js; loaded before features.js, so that
// its places of a save's problems come first.
//
// 媒体库页面（I138）：扁平菜单中的一个模块，位于流媒体与媒体认证旁，涵盖点播与频道的整条线（I132-I136）。各页读取 Admin API，
// 并编辑配置的共享草稿：library 为各媒体库的条目（搜索、按编码/分辨率/问题过滤、分页）与重新扫描进度；item 为单个条目的
// 码流、轨道、编码、音频、字幕、海报与 sidecar、探测错误，以及预览播放器（hls.js，首次需要时加载 vendor/hls.min.js），
// 其 URL 由 POST /media/preview 为当前登录用户签发，两分钟有效；channels 以时间线编辑各频道（从媒体库拖入条目、按其时区编排
// 日程、循环与随机），这些修改进入共享草稿，经“保存并应用”后从下一个条目起生效，并显示正在播放与下一个、开始/停止/跳过、
// 预览，以及各推流目标的状态、错误、字节数与码率；playback 为策略的 url_signing：按 media_tokens 的方式签发测试 URL 并播放
// （从不保存），按名称与时长显示密钥（从不显示密钥本身）、确认后轮换单个或全部，分页列出播放会话（可逐个结束）及各主体相对
// max_sessions 的数量；metrics 为各媒体库、vod 处理器、策略与频道的观看数、每秒请求与字节、缓存命中率与字节、文件池队列与拒绝、
// 按原因统计的 403；另有 media_library、vod 路由、streaming.channels（含 push）与各策略 url_signing 的配置页，带 UI/.conf/
// .yaml/.json 标签页（I120），保存问题链接回这里。页面不保存任何 URL、令牌或签名，密钥从不到达浏览器。
(function () {
  'use strict';

  // [key, en, zh] / [键, 英文, 中文]
  var WORDS = [
    ['m.media', 'Media library', '媒体库'], ['tag.media', 'VOD / channels', '点播 / 频道'],
    ['n.library', 'Libraries', '媒体库'], ['n.channels', 'Channels', '频道'], ['n.playback', 'Playback URLs', '播放 URL'],
    ['n.metrics', 'Metrics', '指标'], ['n.config', 'Library config', '媒体库配置'], ['n.vod', 'VOD routes', '点播路由'],
    ['n.chconfig', 'Channels config', '频道配置'], ['n.urlsign', 'URL signing', 'URL 签名'], ['n.item', 'Item', '条目'],
    // libraries / 媒体库
    ['l.none', 'No media library runs: add one on the library config page, then save and apply.', '没有运行中的媒体库：请在“媒体库配置”页添加，然后保存并应用。'],
    ['l.config', 'Library config', '媒体库配置'], ['l.roots', 'Roots', '根目录'], ['l.items', 'Items', '条目'],
    ['l.errors', 'With errors', '有错误'], ['l.problems', 'Problems', '问题'], ['l.scanned', 'Last scan', '上次扫描'],
    ['l.scans', 'Scans', '扫描次数'], ['l.bytes', 'Catalog memory', '目录内存'], ['l.rescan', 'Rescan', '重新扫描'],
    ['l.rescan.q', 'Scan the library {name} again now?', '立即重新扫描媒体库 {name}？'],
    ['l.rescan.queued', 'A scan of {name} is queued.', '已安排扫描 {name}。'], ['l.rescan.waiting', 'A scan of {name} was already waiting.', '{name} 已有等待中的扫描。'],
    ['l.scanning', 'Scanning: {probed} of {probe} files probed, {files} looked at', '扫描中：已探测 {probed}/{probe} 个文件，共查看 {files} 个'],
    ['l.pending', 'A scan waits to start', '有扫描等待开始'], ['l.idle', 'Idle', '空闲'],
    ['l.truncated', 'The catalog is cut short: {why}', '目录被截断：{why}'],
    ['l.problist', 'Files the last scan reported', '上次扫描报告的文件'],
    ['f.search', 'Search titles and IDs', '搜索标题与 ID'], ['f.codec', 'Codec', '编码'], ['f.res', 'Resolution', '分辨率'],
    ['f.err', 'Problems', '问题'], ['f.any', 'Any', '全部'], ['f.with', 'With problems', '有问题'], ['f.without', 'Without problems', '无问题'],
    ['f.sd', 'Under 720p', '低于 720p'], ['f.hd', '720p', '720p'], ['f.fhd', '1080p', '1080p'], ['f.uhd', '4K and up', '4K 及以上'],
    ['p.of', '{from}–{to} of {n}', '第 {from}–{to} 项，共 {n} 项'], ['p.prev', 'Previous', '上一页'], ['p.next', 'Next', '下一页'],
    ['i.title', 'Title', '标题'], ['i.id', 'ID', 'ID'], ['i.duration', 'Duration', '时长'], ['i.res', 'Resolution', '分辨率'],
    ['i.codecs', 'Codecs', '编码'], ['i.files', 'Files', '文件'], ['i.size', 'Size', '大小'], ['i.problem', 'Problem', '问题'],
    ['i.files.v', '{r} video · {a} audio · {s} subtitles', '{r} 视频 · {a} 音频 · {s} 字幕'], ['i.none', 'No item matches.', '没有匹配的条目。'],
    // an item / 条目
    ['it.back', 'Back to the library', '返回媒体库'], ['it.missing', 'No item {id} in the library {lib}.', '媒体库 {lib} 中没有条目 {id}。'],
    ['it.path', 'Path', '路径'], ['it.root', 'Root', '根目录'], ['it.modified', 'Modified', '修改时间'],
    ['it.renditions', 'Renditions', '视频码流'], ['it.audio', 'Audio files', '音频文件'], ['it.subtitles', 'Subtitles', '字幕'],
    ['it.poster', 'Poster', '海报'], ['it.sidecar', 'Sidecar', 'Sidecar'], ['it.nofile', 'None', '无'],
    ['it.name', 'Name', '名称'], ['it.file', 'File', '文件'], ['it.bitrate', 'Bit rate', '码率'], ['it.tracks', 'Tracks', '轨道'],
    ['it.lang', 'Language', '语言'], ['it.format', 'Format', '格式'], ['it.default', 'default', '默认'], ['it.forced', 'forced', '强制'],
    ['it.error', 'Probe error', '探测错误'],
    ['it.ask', 'The media file {path} of the library {lib} failed: “{error}”. Explain why, and how to make a file fit can serve.',
      '媒体库 {lib} 中的媒体文件 {path} 出错：“{error}”。请解释原因，以及如何制作 fit 能提供的文件。'],
    ['tr.kind', 'Kind', '类型'], ['tr.codec', 'Codec', '编码'], ['tr.detail', 'Detail', '详情'], ['tr.samples', 'Samples', '采样数'],
    ['tr.table', 'Sample table', '采样表'],
    // preview / 预览
    ['pv.title', 'Preview', '预览'], ['pv.start', 'Preview', '预览'], ['pv.again', 'Preview again', '重新预览'], ['pv.stop', 'Stop', '停止'],
    ['pv.note', 'Plays through a URL the workbench signs for you, {user}, good until {at}; the segments it fetches carry their own grant, until refused.',
      '通过工作台为你（{user}）签发的 URL 播放，有效至 {at}；它获取的分段带有自己的授权，直到被拒绝为止。'],
    ['pv.signed.url', 'signed URL', '签名 URL'], ['pv.signed.token', 'short-lived token', '短期令牌'], ['pv.signed.public', 'public', '公开'],
    ['pv.cors', 'The route {route} sets no cors: a browser may refuse the preview from the console\'s origin. Set cors on the route.',
      '路由 {route} 未设置 cors：浏览器可能拒绝从控制台的源进行预览。请在该路由上设置 cors。'],
    ['pv.cors.link', 'VOD routes', '点播路由'],
    ['pv.refused', 'Refused ({code}): {reason}. The preview stopped.', '被拒绝（{code}）：{reason}。预览已停止。'],
    ['pv.failed', 'The player stopped: {reason}', '播放器已停止：{reason}'],
    ['pv.unsupported', 'This browser plays neither HLS nor Media Source Extensions.', '此浏览器既不支持 HLS，也不支持 Media Source Extensions。'],
    ['pv.nohls', 'hls.js could not be loaded.', '无法加载 hls.js。'],
    ['pv.stopped', 'Stopped.', '已停止。'],
    // channels / 频道
    ['c.none', 'No channel yet: a channel plays a library\'s items into the streaming hub as a live stream.', '还没有频道：频道把媒体库的条目作为直播流播放到流媒体中心。'],
    ['c.add', 'Add channel', '添加频道'], ['c.remove', 'Remove channel', '删除频道'], ['c.remove.q', 'Remove the channel {name} from the draft?', '从草稿中删除频道 {name}？'],
    ['c.note', 'The list and the schedule are the config\'s: Save and apply them, and the channel plays them from its next item on.',
      '列表与日程属于配置：保存并应用后，频道从下一个条目起按其播放。'],
    ['c.name', 'Stream name', '流名称'], ['c.library', 'Library', '媒体库'], ['c.order', 'Shuffle', '随机播放'], ['c.loop', 'Loop', '循环'],
    ['c.tz', 'Time zone', '时区'], ['c.rendition', 'Rendition', '码流'], ['c.rendition.ph', 'the best', '最佳'],
    ['c.state', 'State', '状态'], ['c.now', 'Now', '正在播放'], ['c.next', 'Next', '下一个'], ['c.nextslot', 'Next slot', '下一个时段'],
    ['c.buffered', 'Read ahead', '预读'], ['c.counters', 'Played · skipped · underruns', '已播放 · 跳过 · 欠载'],
    ['c.start', 'Start', '开始'], ['c.stop', 'Stop', '停止'], ['c.skip', 'Skip', '跳过'], ['c.skipto', 'Play now', '立即播放'],
    ['c.stop.q', 'Stop the channel {name}? Its stream goes quiet until it is started again.', '停止频道 {name}？在再次开始之前它的流将静默。'],
    ['c.notrunning', 'Not running yet: Save and apply the config to start it.', '尚未运行：保存并应用配置后启动。'],
    ['c.list', 'Playlist', '播放列表'], ['c.list.drop', 'Drag items here from the library, or add them with +.', '把媒体库中的条目拖到这里，或用 + 添加。'],
    ['c.list.total', '{n} items · {d}', '{n} 个条目 · {d}'], ['c.drag', 'Drag to reorder', '拖动以调整顺序'],
    ['c.schedule', 'Schedule', '日程'], ['c.slot.add', 'Add slot', '添加时段'],
    ['c.slot.at', 'At', '时间'], ['c.slot.item', 'Item', '条目'],
    ['c.slot.hint', 'HH:MM every day; mon,wed,fri 20:00 or sat-sun 09:30 on those days; 2026-12-31 23:00 once. Drop an item on the day to start it there.',
      'HH:MM 每天；mon,wed,fri 20:00 或 sat-sun 09:30 表示这些日子；2026-12-31 23:00 表示一次。把条目拖到当天的时间轴上即可在该时间开始。'],
    ['c.slot.bad', 'Not a time: HH:MM, days HH:MM, or a date and time.', '不是有效时间：HH:MM、星期 HH:MM，或日期加时间。'],
    ['c.today', 'Today in {tz}', '{tz} 今天'],
    ['c.picker', 'Library items', '媒体库条目'], ['c.picker.add', 'Add to the playlist', '添加到播放列表'],
    ['c.picker.none', 'Pick the channel\'s library to drag its items in.', '选择频道的媒体库后即可拖入其条目。'],
    ['c.unknown', 'not in the library', '不在媒体库中'],
    ['c.push', 'Push targets', '推流目标'], ['c.push.none', 'No push target.', '没有推流目标。'],
    ['c.push.all', 'Every push target', '全部推流目标'],
    ['ps.name', 'Target', '目标'], ['ps.url', 'Server', '服务器'], ['ps.state', 'State', '状态'], ['ps.error', 'Last error', '最近错误'],
    ['ps.bytes', 'Sent', '已发送'], ['ps.bitrate', 'Bit rate', '码率'], ['ps.connects', 'Connects · failures', '连接 · 失败'],
    ['ps.restart', 'Restart', '重启'], ['ps.retry', 'retry in {s}s', '{s} 秒后重试'],
    ['ps.ask', 'The push target {name} of the stream {stream} is {state}: “{error}”. Why, and what should I fix?',
      '流 {stream} 的推流目标 {name} 处于 {state}：“{error}”。原因是什么？应该修复什么？'],
    ['c.status.failed', 'The channels\' state could not be read: {e}', '无法读取频道状态：{e}'],
    // playback URLs / 播放 URL
    ['u.none', 'No media_auth policy runs.', '没有运行中的 media_auth 策略。'],
    ['u.nosign', 'The policy {name} has no url_signing: its URLs do not expire. Turn it on on the URL signing page.',
      '策略 {name} 没有 url_signing：其 URL 不会过期。请在“URL 签名”页开启。'],
    ['u.settings', 'URL signing', 'URL 签名'], ['u.edit', 'Edit', '编辑'],
    ['u.keys', 'Keys', '密钥'], ['u.key', 'Key', '密钥'], ['u.single', '(the policy\'s secret)', '（策略的密钥）'],
    ['u.source', 'Kept in', '保存于'], ['u.file', 'its secret_file', 'secret_file 文件'], ['u.inline', 'the config (inline)', '配置中（内联）'],
    ['u.age', 'Age', '时长'], ['u.rotate', 'Rotate', '轮换'], ['u.rotateall', 'Rotate every key', '轮换全部密钥'],
    ['u.rotate.q', 'Rotate {key}? Every URL and token it signed is refused at once, and players using them stop.',
      '轮换 {key}？它签发的每个 URL 与令牌立即被拒绝，正在使用它们的播放器会停止。'],
    ['u.rotateall.q', 'Rotate every key of {name}? Every URL and token the policy signed is refused at once.',
      '轮换 {name} 的全部密钥？该策略签发的每个 URL 与令牌立即被拒绝。'],
    ['u.rotated', 'Rotated: {keys}.', '已轮换：{keys}。'], ['u.inline.t', 'An inline secret is rotated by editing the config.', '内联密钥需通过编辑配置来轮换。'],
    ['u.test', 'Test URL', '测试 URL'], ['u.subject', 'Subject', '主体'], ['u.path', 'Path', '路径'], ['u.address', 'Address', '地址'],
    ['u.ttl', 'Lifetime', '有效期'], ['u.route', 'Through the route', '经由路由'], ['u.route.none', 'none: the policy\'s bounds', '无：使用策略的限制'],
    ['u.issue', 'Issue and play', '签发并播放'], ['u.item', 'Item', '条目'], ['u.item.pick', 'Pick an item to fill in its path', '选择条目以填入其路径'],
    ['u.test.note', 'The URL is shown here and played below; it is not kept anywhere, and is gone when you leave the page.',
      'URL 显示在这里并在下方播放；它不会被保存，离开页面即消失。'],
    ['u.expires', 'Expires {at}, session {sid}', '过期时间 {at}，会话 {sid}'],
    ['u.sessions', 'Playback sessions', '播放会话'], ['u.sess.none', 'No session plays.', '没有正在播放的会话。'],
    ['u.sid', 'Session', '会话'], ['u.resource', 'Resource', '资源'], ['u.start', 'Started', '开始'], ['u.last', 'Last request', '最近请求'],
    ['u.count', 'Of the subject', '该主体'], ['u.end', 'End', '结束'], ['u.ended', 'ended', '已结束'],
    ['u.end.q', 'End the session {sid} of {subject}? Its player is refused from its next request on.', '结束 {subject} 的会话 {sid}？其播放器从下一次请求起被拒绝。'],
    ['u.end.done', 'Session ended.', '会话已结束。'], ['u.filter', 'Subject', '主体'], ['u.refusals', 'Refused URLs by reason', '按原因统计的被拒 URL'],
    ['u.sessions.n', '{n} sessions, {s} subjects', '{n} 个会话，{s} 个主体'],
    // metrics / 指标
    ['x.note', 'Counted by fit since it started; the rates are over the last refresh, every {s}s.', '自 fit 启动以来的计数；速率为最近一次刷新（每 {s} 秒）间的变化。'],
    ['x.libraries', 'Libraries', '媒体库'], ['x.vod', 'Video on demand', '点播'], ['x.policies', 'Playback URLs', '播放 URL'],
    ['x.channels', 'Channels', '频道'], ['x.none', 'Nothing to count yet.', '暂无可统计的内容。'],
    ['x.queue', 'File pool', '文件池'], ['x.queue.v', '{busy} busy · {queued} queued of {queue}', '{busy} 忙 · 排队 {queued}/{queue}'],
    ['x.refused', 'Pool refusals', '文件池拒绝'], ['x.refused.v', '{refused} refused · {expired} expired', '{refused} 被拒 · {expired} 超时'],
    ['x.tables', 'Sample tables', '采样表'], ['x.cache', 'Segment cache', '分段缓存'], ['x.hit', '{p} hits', '命中 {p}'],
    ['x.requests', 'Requests', '请求'], ['x.sent', 'Sent', '发送'], ['x.persec', '{v}/s', '{v}/秒'],
    ['x.viewers', 'Viewers now', '当前观看'], ['x.viewers.v', '{n} sessions', '{n} 个会话'],
    ['x.forbidden', '403 by reason', '按原因统计的 403'], ['x.players', 'Players', '播放者'],
    ['x.ask', 'The playback URLs of the policy {name} are refused 403: {list}. Why, and what should I fix?',
      '策略 {name} 的播放 URL 被拒绝（403）：{list}。原因是什么？应该修复什么？'],
    ['x.failed', 'The counts could not be read: {e}', '无法读取统计：{e}'],
    // the config pages / 配置页
    ['cf.lib.add', 'Add library', '添加媒体库'], ['cf.vod.add', 'Add VOD route', '添加点播路由'],
    ['cf.vod.none', 'No vod route: a vod route serves a library\'s items as HLS.', '没有点播路由：点播路由以 HLS 提供媒体库的条目。'],
    ['cf.vod.note', 'Routes whose handler is vod. The raw tabs hold the routes member: every route, not only these.',
      '处理器为 vod 的路由。原始文本标签页为 routes 成员：全部路由，而不只是这些。'],
    ['cf.ch.add', 'Add channel', '添加频道'], ['cf.push.add', 'Add push target', '添加推流目标'],
    ['cf.ch.note', 'streaming.channels and streaming.push. The raw tabs hold the whole streaming member. The channels page edits the same lists as a timeline.',
      'streaming.channels 与 streaming.push。原始文本标签页为整个 streaming 成员。“频道”页以时间线编辑同样的列表。'],
    ['cf.us.note', 'Each media_auth policy\'s url_signing. The raw tabs hold the whole media_auth member, its secrets left as the draft holds them.',
      '各 media_auth 策略的 url_signing。原始文本标签页为整个 media_auth 成员。'],
    ['cf.us.none', 'No media_auth policy: add one on the media auth page.', '没有 media_auth 策略：请在“媒体认证”页添加。'],
    ['cf.us.policy', 'Policy {name}', '策略 {name}'], ['cf.mediaauth', 'Media auth', '媒体认证']
  ];

  var ID = 'gw.medialib';
  var CODECS = ['h264', 'h265', 'av1', 'aac', 'opus', 'mp3'];
  // RES maps a resolution filter to the item heights it takes. / 分辨率过滤与其高度范围。
  var RES = { sd: { max_height: 719 }, hd: { min_height: 720, max_height: 1079 }, fhd: { min_height: 1080, max_height: 2159 }, uhd: { min_height: 2160 } };
  var PAGE = 50, STATS_EVERY = 2000, CHANNEL_EVERY = 2000, SESSIONS_EVERY = 5000, SCAN_EVERY = 1000;

  // ---- formatting / 格式化 ----
  function enc(s) { return encodeURIComponent(s); }
  function idPath(id) { return String(id).split('/').map(enc).join('/'); }
  function fmtBytes(n) {
    if (n == null || n < 0 || isNaN(n)) return '—';
    var u = ['B', 'KiB', 'MiB', 'GiB', 'TiB'], i = 0;
    while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
    return (i ? n.toFixed(n >= 100 ? 0 : 1) : String(Math.round(n))) + ' ' + u[i];
  }
  function fmtBits(n) {
    if (!n || n < 0) return '—';
    if (n >= 1e6) return (n / 1e6).toFixed(n >= 1e7 ? 0 : 1) + ' Mb/s';
    if (n >= 1e3) return Math.round(n / 1e3) + ' kb/s';
    return Math.round(n) + ' b/s';
  }
  function fmtClock(sec) {
    if (sec == null || isNaN(sec) || sec < 0) return '—';
    sec = Math.floor(sec);
    var hh = Math.floor(sec / 3600), mm = Math.floor(sec % 3600 / 60), ss = sec % 60;
    function p(n) { return (n < 10 ? '0' : '') + n; }
    return (hh ? hh + ':' + p(mm) : mm) + ':' + p(ss);
  }
  function pct(a, b) { return b > 0 ? Math.round(a / b * 100) + '%' : '—'; }
  function unix(s) { var t = Date.parse(s || ''); return isNaN(t) ? 0 : t / 1000; }
  function routeName(r, i) { return (r && r.name) || '#' + (i + 1); }
  function isVod(r) { return !!(r && r.handler && r.handler.module === 'vod'); }
  function uniqueName(list, base, key) {
    var names = {};
    (list || []).forEach(function (x) { if (x && x[key || 'name']) names[x[key || 'name']] = 1; });
    if (!names[base]) return base;
    for (var i = 2; ; i++) if (!names[base + i]) return base + i;
  }
  function askOf(spec, o) { return window.FitAI && FitAI.askButton ? FitAI.askButton(spec, o) : null; }

  // ---- hls.js, loaded when a player first needs it / hls.js：播放器首次需要时加载 ----
  var hlsLoading = null;
  function loadHls() {
    if (window.Hls) return Promise.resolve(window.Hls);
    if (!hlsLoading) {
      hlsLoading = new Promise(function (resolve, reject) {
        var s = document.createElement('script');
        s.src = 'vendor/hls.min.js';
        s.onload = function () { resolve(window.Hls); };
        s.onerror = function () { hlsLoading = null; reject(new Error('hls.js')); };
        document.head.appendChild(s);
      });
    }
    return hlsLoading;
  }

  // players are those on the page shown; a new page stops them, so none plays on unseen. / 当前页面的播放器；切换页面时停止。
  var players = [];
  function stopPlayers() { players.splice(0).forEach(function (p) { p.destroy(); }); }
  window.addEventListener('hashchange', stopPlayers);

  // reasonOf is a refusal's reason, from its body: mediaauth's "...: expired" or the text. / 拒绝原因（取自应答体）。
  function reasonOf(text) {
    var s = String(text || '').trim().split('\n')[0];
    var m = /refused:\s*(.+)$/.exec(s);
    return (m ? m[1] : s).slice(0, 200) || '—';
  }

  // bodyOf is the body of hls.js's failed request, its XMLHttpRequest, as text: the refusal's reason fit wrote.
  // bodyOf 返回 hls.js 失败请求（其 XMLHttpRequest）的应答体文本：fit 写入的拒绝原因。
  function bodyOf(xhr) {
    try {
      if (!xhr || typeof xhr !== 'object') return '';
      if (xhr.responseType === '' || xhr.responseType === 'text') return xhr.responseText || '';
      if (xhr.response instanceof ArrayBuffer) return new TextDecoder().decode(new Uint8Array(xhr.response, 0, Math.min(xhr.response.byteLength, 512)));
    } catch (e) { /* not readable / 不可读 */ }
    return '';
  }

  // player plays url with hls.js (or the browser's own HLS), muted, and stops at the first refusal, saying why: an
  // expired URL whose grant ended, a rotated key, an ended session. / 用 hls.js（或浏览器自带的 HLS）静音播放 url，首次被拒绝
  // 时停止并说明原因：URL 与其授权过期、密钥已轮换、会话已结束。
  function player(ui, url, o) {
    var h = ui.h, t = ui.t;
    o = o || {};
    var video = h('video.md-video', { controls: true, playsinline: true, preload: 'auto', poster: o.poster || null });
    video.muted = true;
    var note = h('div.md-player__note');
    var box = h('div.md-player', video, note);
    var hls = null, dead = false;
    function stop(msg, kind) {
      if (dead) return;
      dead = true;
      if (hls) { try { hls.destroy(); } catch (e) { /* ignore / 忽略 */ } hls = null; }
      try { video.pause(); video.removeAttribute('src'); video.load(); } catch (e) { /* ignore / 忽略 */ }
      box.classList.add('is-stopped');
      box.setAttribute('data-state', kind || 'refused');
      UI.mount(note, h('div.callout' + (kind === 'stopped' ? '' : '.callout--danger'), UI.icon(kind === 'stopped' ? 'stop' : 'alert'), ' ', msg));
      if (o.onStop) o.onStop(kind || 'refused');
    }
    var p = { el: box, video: video, stopped: function () { return dead; }, destroy: function () { stop(t('pv.stopped'), 'stopped'); } };
    players.push(p);
    box.setAttribute('data-state', 'loading');
    loadHls().then(function (Hls) {
      if (dead) return;
      if (Hls && Hls.isSupported()) {
        // No worker: the console's CSP has no worker-src. A short buffer: a preview needs little, and a refusal (an
        // ended session, a rotated key) then shows within seconds rather than once a long buffer has played out.
        // 不用 worker：控制台的 CSP 没有 worker-src。缓冲较短：预览所需不多，被拒绝（会话结束、密钥轮换）时几秒内即可显现，
        // 而不必等很长的缓冲播完。
        hls = new Hls({ enableWorker: false, maxBufferLength: 8, maxMaxBufferLength: 16, backBufferLength: 16 });
        hls.on(Hls.Events.ERROR, function (ev, d) {
          var code = d && d.response && d.response.code;
          if (code === 401 || code === 403 || code === 404 || code === 410) {
            stop(t('pv.refused', { code: code, reason: reasonOf(bodyOf(d.networkDetails) || d.response.text) }));
          } else if (d && d.fatal) {
            stop(t('pv.failed', { reason: (d.details || d.type || '') + (code ? ' (' + code + ')' : '') }));
          }
        });
        hls.on(Hls.Events.MANIFEST_PARSED, function () { box.setAttribute('data-state', 'playing'); video.play().catch(function () { /* muted autoplay may still wait / 静音自动播放仍可能等待 */ }); });
        hls.loadSource(url);
        hls.attachMedia(video);
      } else if (video.canPlayType('application/vnd.apple.mpegurl')) {
        video.src = url;
        video.addEventListener('error', function () { stop(t('pv.failed', { reason: video.error ? video.error.message || video.error.code : '' })); });
        video.addEventListener('playing', function () { box.setAttribute('data-state', 'playing'); });
        video.play().catch(function () { /* ignore / 忽略 */ });
      } else stop(t('pv.unsupported'));
    }, function () { stop(t('pv.nohls')); });
    return p;
  }

  // previewCard is a card that asks POST /media/preview for a URL signed for the user, on a click, and plays it.
  // previewCard：点击后请求 POST /media/preview 为用户签发的 URL 并播放。
  function previewCard(ui, req, title) {
    var h = ui.h, t = ui.t;
    var body = h('div.stack'), current = null;
    var btn = UI.btn(t('pv.start'), 'play', start, 'primary sm');
    btn.classList.add('md-preview-btn');
    var stopBtn = UI.btn(t('pv.stop'), 'stop', function () { if (current) current.destroy(); }, 'sm');
    function start() {
      if (current) current.destroy();
      UI.busy(btn, ui.api('POST', '/api/media/preview', req)).then(function (r) {
        var at = UI.fmtTime(r.expires);
        var parts = [h('div.row.small', UI.badge(t('pv.signed.' + (r.signed || 'url')), r.signed === 'public' ? '' : 'ok'),
          h('span.muted', t('pv.note', { user: String(r.subject || '').replace(/^workbench:/, ''), at: at })))];
        if (!r.cors) {
          parts.push(h('div.callout.callout--warn.small', UI.icon('alert'), ' ', t('pv.cors', { route: r.route || '' }), ' ',
            h('a', { href: '#/' + ID + '/vod' }, t('pv.cors.link'))));
        }
        current = player(ui, r.url, { poster: r.poster || null });
        parts.push(current.el);
        UI.mount(body, parts);
        btn.lastChild.textContent = t('pv.again');
      }).catch(function (e) { UI.mount(body, h('div.callout.callout--danger', UI.icon('alert'), ' ', e.message || String(e))); });
    }
    return UI.card(title || t('pv.title'), body, h('div.btn-row', btn, stopBtn));
  }

  // ---- places of a save's problems / 保存问题的位置 ----
  // Registered now, before features.js and modules.js register theirs: the media members' problems link here.
  // 现在注册，早于 features.js 与 modules.js：媒体相关成员的问题链接到这里。
  FitConfig.with(UI.scope('admin', ID)).place(function (path, d) {
    var m = /^([A-Za-z_]+)(?:\[(\d+)\])?(?:\.([A-Za-z_]+))?/.exec(path || '');
    if (!m) return null;
    if (m[1] === 'media_library') return '#/' + ID + '/config';
    if (m[1] === 'routes' && m[2] != null && isVod((d.routes || [])[+m[2]])) return '#/' + ID + '/vod';
    if (m[1] === 'streaming' && m[2] == null) {
      var sub = /^streaming\.(channels|push)\b/.exec(path);
      if (sub) return '#/' + ID + '/channels_config';
    }
    if (m[1] === 'media_auth' && m[3] === 'url_signing') return '#/' + ID + '/url_signing';
    return null;
  });

  // ---- the module / 模块 ----
  WB.module({
    id: ID, server: 'admin', group: 'gateway', order: 9.5, icon: 'play', label: 'm.media', tag: 'tag.media', words: WORDS,
    nav: [
      { id: 'library', icon: 'disk', label: 'n.library' }, { id: 'channels', icon: 'radio', label: 'n.channels' },
      { id: 'playback', icon: 'key', label: 'n.playback' }, { id: 'metrics', icon: 'activity', label: 'n.metrics' },
      { id: 'config', icon: 'settings', label: 'n.config' }, { id: 'vod', icon: 'route', label: 'n.vod' },
      { id: 'channels_config', icon: 'settings', label: 'n.chconfig' }, { id: 'url_signing', icon: 'lock', label: 'n.urlsign' }
    ],
    setup: function (ui, sh) {
      var h = ui.h, t = ui.t;
      var C = FitConfig.with(ui);
      function api(method, path, body) { return ui.api(method, '/api' + path, body); }
      function fail(el, e) { UI.mount(el, h('div.callout.callout--danger', UI.icon('alert'), ' ', (e && e.message) || String(e))); }

      // ---- libraries / 媒体库 ----
      // The browser's state, kept while the workbench is open: the library picked and each library's filters and page.
      // 浏览状态（工作台打开期间保留）：所选媒体库及各库的过滤与页码。
      var lib = { name: '', filters: {} };
      function filtersOf(name) {
        return lib.filters[name] || (lib.filters[name] = { q: '', codec: '', res: '', err: '', offset: 0 });
      }
      function query(f) {
        var q = ['offset=' + f.offset, 'limit=' + PAGE];
        if (f.q) q.push('q=' + enc(f.q));
        if (f.codec) q.push('codec=' + enc(f.codec));
        if (f.err) q.push('error=' + f.err);
        var r = RES[f.res];
        if (r) Object.keys(r).forEach(function (k) { q.push(k + '=' + r[k]); });
        return q.join('&');
      }
      function scanLine(st) {
        st = st || {};
        if (st.scanning) {
          return UI.meter(t('l.scanning', { probed: st.probed || 0, probe: st.probe || 0, files: st.files || 0 }), st.probed || 0, st.probe || 1,
            (st.probe ? pct(st.probed || 0, st.probe) : '…'));
        }
        return h('span.muted.small', st.pending ? t('l.pending') : t('l.idle'));
      }
      function libraryPage() {
        var el = sh.page('library', t('n.library'));
        el.appendChild(h('div', UI.icon('refresh'), ' ', t('loading')));
        api('GET', '/media/library').then(function (r) {
          var libs = (r && r.libraries) || [];
          if (!libs.length) {
            UI.mount(el, UI.empty(t('l.none'), 'disk'), h('div.btn-row', h('a.btn', { href: '#/' + ID + '/config' }, UI.icon('settings'), t('l.config'))));
            return;
          }
          if (!libs.some(function (l) { return l.name === lib.name; })) lib.name = libs[0].name;
          var tabs = libs.length > 1 ? UI.tabs(libs.map(function (l) { return [l.name, l.name + ' · ' + l.items]; }), lib.name, function (k) { lib.name = k; libraryPage(); }) : null;
          var cur = libs.filter(function (l) { return l.name === lib.name; })[0];
          var head = h('div'), items = h('div.stack');
          UI.mount(el, tabs, head, items);
          drawLibrary(head, cur);
          drawItems(items, cur.name);
          // The rescan's progress, while a scan runs or waits. / 扫描进行或等待期间的进度。
          if (cur.status && (cur.status.scanning || cur.status.pending)) follow(head, cur.name, items);
        }).catch(function (e) { fail(el, e); });
      }
      function follow(head, name, items) {
        var id = UI.every(SCAN_EVERY, function () {
          api('GET', '/media/library').then(function (r) {
            var l = ((r && r.libraries) || []).filter(function (x) { return x.name === name; })[0];
            if (!l || !document.body.contains(head)) { clearInterval(id); return; }
            drawLibrary(head, l);
            if (!l.status.scanning && !l.status.pending) { clearInterval(id); drawItems(items, name); }
          }).catch(function () { clearInterval(id); });
        });
      }
      function drawLibrary(head, l) {
        var st = l.status || {};
        var rescan = UI.btn(t('l.rescan'), 'refresh', function () {
          UI.confirm(t('l.rescan.q', { name: l.name }), t('l.rescan')).then(function (ok) {
            if (!ok) return;
            UI.busy(rescan, api('POST', '/media/library/' + enc(l.name) + '/rescan')).then(function (r) {
              UI.toast(t(r && r.queued ? 'l.rescan.queued' : 'l.rescan.waiting', { name: l.name }));
              l.status = (r && r.status) || st;
              drawLibrary(head, l);
              follow(head, l.name, head.nextSibling);
            }).catch(UI.fail);
          });
        }, 'sm');
        rescan.classList.add('md-rescan');
        var body = h('div.stack',
          h('div.md-stats',
            UI.stat('disk', t('l.items'), String(l.items), l.errors ? t('l.errors') + ': ' + l.errors : null, l.errors ? 'warn' : ''),
            UI.stat('alert', t('l.problems'), String(l.problems || 0), null, l.problems ? 'warn' : ''),
            UI.stat('history', t('l.scanned'), l.scanned ? UI.ago(unix(l.scanned)) : t('never'), t('l.scans') + ': ' + (st.scans || 0)),
            UI.stat('memory', t('l.bytes'), fmtBytes(l.bytes))),
          h('div.md-scan', scanLine(st)),
          UI.kv([[t('l.roots'), (l.roots || []).map(function (x) { return h('code.small', { style: { marginRight: '8px' } }, x); })]]),
          l.truncated ? h('div.callout.callout--warn', UI.icon('alert'), ' ', t('l.truncated', { why: l.truncated })) : null);
        UI.mount(head, UI.card(l.name, body, rescan));
      }
      function drawItems(box, name) {
        var f = filtersOf(name);
        var search = h('input.input.input--sm.md-search', { type: 'search', placeholder: t('f.search'), value: f.q, 'aria-label': t('f.search') });
        var timer = null;
        search.addEventListener('input', function () {
          clearTimeout(timer);
          timer = setTimeout(function () { f.q = search.value.trim(); f.offset = 0; load(); }, 250);
        });
        function select(label, value, opts, set) {
          var s = h('select.select.select--sm', { 'aria-label': label }, opts.map(function (o) { return h('option', { value: o[0], selected: o[0] === value }, o[1]); }));
          s.addEventListener('change', function () { set(s.value); f.offset = 0; load(); });
          return h('label.md-filter', h('span.muted.small', label), s);
        }
        var bar = h('div.row.md-filters', search,
          select(t('f.codec'), f.codec, [['', t('f.any')]].concat(CODECS.map(function (c) { return [c, c]; })), function (v) { f.codec = v; }),
          select(t('f.res'), f.res, [['', t('f.any')], ['sd', t('f.sd')], ['hd', t('f.hd')], ['fhd', t('f.fhd')], ['uhd', t('f.uhd')]], function (v) { f.res = v; }),
          select(t('f.err'), f.err, [['', t('f.any')], ['true', t('f.with')], ['false', t('f.without')]], function (v) { f.err = v; }));
        var list = h('div');
        UI.mount(box, UI.card(null, h('div.stack', bar, list)));
        // seq keeps the last request's answer: an earlier one that comes late is dropped. / 只采用最后一次请求的应答。
        var seq = 0;
        function load() {
          var mine = ++seq;
          UI.mount(list, h('div.muted', UI.icon('refresh'), ' ', t('loading')));
          api('GET', '/media/library/' + enc(name) + '?' + query(f)).then(function (r) {
            if (mine !== seq) return;
            var rows = (r && r.page) || [];
            var problems = (r && r.problem_list) || [];
            var pager = h('div.row.row--between.md-pager',
              h('span.muted.small', r.matched ? t('p.of', { from: r.offset + 1, to: r.offset + rows.length, n: r.matched }) : ''),
              h('div.btn-row',
                UI.btn(t('p.prev'), null, function () { f.offset = Math.max(0, f.offset - PAGE); load(); }, 'sm'),
                UI.btn(t('p.next'), null, function () { f.offset += PAGE; load(); }, 'sm')));
            pager.querySelectorAll('button')[0].disabled = f.offset <= 0;
            pager.querySelectorAll('button')[1].disabled = r.offset + rows.length >= r.matched;
            UI.mount(list, UI.table([
              { label: t('i.title'), render: function (it) { return h('a.md-item-link', { href: itemHash(name, it.id) }, it.title || it.id); } },
              { label: t('i.id'), render: function (it) { return h('code.small', it.id); } },
              { label: t('i.duration'), render: function (it) { return fmtClock(it.duration); } },
              { label: t('i.res'), render: function (it) { return it.height ? it.width + '×' + it.height : '—'; } },
              { label: t('i.codecs'), render: function (it) { return (it.codecs || []).map(function (c) { return UI.badge(c); }); } },
              { label: t('i.files'), render: function (it) { return h('span.small.muted', t('i.files.v', { r: it.renditions, a: it.audio, s: it.subtitles })); } },
              { label: t('i.size'), render: function (it) { return fmtBytes(it.size); } },
              { label: t('i.problem'), render: function (it) { return it.problems ? UI.badge(it.error ? t('error') : t('l.problems'), 'danger') : ''; } }
            ], rows, { empty: t('i.none'), onRowClick: function (it) { location.hash = itemHash(name, it.id); } }), pager,
            problems.length ? h('details.md-problems', h('summary', t('l.problist') + ' · ' + problems.length),
              h('ul', problems.slice(0, 200).map(function (p) { return h('li.small', h('code', (p.root ? p.root + ' ' : '') + (p.path || '')), ': ', p.error); }))) : null);
          }).catch(function (e) { fail(list, e); });
        }
        load();
      }
      function itemHash(name, id) { return '#/' + ID + '/item/' + enc(name) + '/' + enc(id); }

      // ---- an item / 条目 ----
      function itemPage(p) {
        var el = sh.page('library', p.id, t('n.library') + ' / ' + p.lib + ' / ' + p.id);
        sh.tools.appendChild(h('a.btn.btn--sm', { href: '#/' + ID + '/library' }, UI.icon('chev'), t('it.back')));
        el.appendChild(h('div', UI.icon('refresh'), ' ', t('loading')));
        lib.name = p.lib;
        api('GET', '/media/library/' + enc(p.lib) + '/items/' + idPath(p.id)).then(function (it) {
          WB.select({ library: p.lib, item: it.id, title: it.title, error: it.error || undefined });
          var parts = [];
          var errs = [];
          if (it.error) errs.push({ path: it.path, error: it.error });
          (it.renditions || []).concat(it.audio || [], it.subtitles || []).forEach(function (f) { if (f.error) errs.push({ path: f.path, error: f.error }); });
          errs.forEach(function (e) {
            parts.push(h('div.callout.callout--danger', h('div.ai-askrow', h('b', t('it.error')),
              askOf({ skill: 'explain', message: t('it.ask', { path: e.path, lib: p.lib, error: e.error }), inputs: { error: e.error },
                selection: { library: p.lib, item: it.id, file: e.path, error: e.error } })),
              h('div', h('code.small', e.path), ': ', e.error)));
          });
          parts.push(UI.card(it.title || it.id, UI.kv([
            [t('i.id'), h('code', it.id)], [t('it.root'), h('code.small', it.root)], [t('it.path'), h('code.small', it.path)],
            [t('i.duration'), fmtClock(it.duration)], [t('i.size'), fmtBytes(it.size)], [t('it.modified'), UI.fmtTime(unix(it.modified))],
            [t('it.poster'), it.poster ? h('code.small', it.poster.path) : t('it.nofile')],
            [t('it.sidecar'), it.sidecar ? h('span', h('code.small', it.sidecar.path), ' · ', fmtBytes(it.sidecar.size), ' · ', UI.fmtTime(unix(it.sidecar.modified))) : t('it.nofile')]
          ])));
          if (!it.error) parts.push(previewCard(ui, { library: p.lib, item: it.id }));
          parts.push(UI.card(t('it.renditions'), renditions(it.renditions || [])));
          if ((it.audio || []).length) parts.push(UI.card(t('it.audio'), renditions(it.audio)));
          parts.push(UI.card(t('it.subtitles'), UI.table([
            { label: t('it.name'), render: function (s) { return h('span', s.name || '—', s.default ? [' ', UI.badge(t('it.default'), 'ok')] : null, s.forced ? [' ', UI.badge(t('it.forced'))] : null); } },
            { label: t('it.lang'), render: function (s) { return s.language || '—'; } },
            { label: t('it.format'), render: function (s) { return s.format; } },
            { label: t('it.file'), render: function (s) { return h('code.small', s.path); } },
            { label: t('i.size'), render: function (s) { return fmtBytes(s.size); } },
            { label: t('i.problem'), render: function (s) { return s.error ? h('span.small', UI.badge(t('error'), 'danger'), ' ', s.error) : ''; } }
          ], it.subtitles || [], { empty: t('it.nofile') })));
          UI.mount(el, h('div.stack', parts));
        }).catch(function (e) {
          if (e && e.status === 404) UI.mount(el, UI.empty(t('it.missing', { id: p.id, lib: p.lib })));
          else fail(el, e);
        });
      }
      function renditions(list) {
        return h('div.stack', list.map(function (r) {
          var tracks = UI.table([
            { label: tr('tr.kind'), render: function (x) { return x.kind; } },
            { label: tr('tr.codec'), render: function (x) { return h('span', UI.badge(x.codec, x.codec === 'unknown' ? 'warn' : ''), ' ', h('code.small', x.codec_string || '')); } },
            { label: tr('tr.detail'), render: function (x) {
              return x.kind === 'video' ? (x.width + '×' + x.height) : x.sample_rate ? (x.sample_rate + ' Hz' + (x.channels ? ' · ' + x.channels + ' ch' : '')) : '';
            } },
            { label: tr('it.lang'), render: function (x) { return x.language || '—'; } },
            { label: tr('i.duration'), render: function (x) { return fmtClock(x.duration); } },
            { label: tr('tr.samples'), render: function (x) { return String(x.samples); } },
            { label: tr('tr.table'), render: function (x) { return fmtBytes(x.table_bytes); } }
          ], r.tracks || []);
          return h('div.md-rendition',
            h('div.row', h('b', r.name || '—'), r.default ? UI.badge(t('it.default'), 'ok') : null, r.language ? UI.badge(r.language) : null,
              r.height ? h('span.muted.small', r.width + '×' + r.height) : null, h('span.muted.small', fmtBits(r.bitrate)),
              h('span.muted.small', fmtClock(r.duration)), h('code.small', r.codecs || ''), r.error ? UI.badge(t('error'), 'danger') : null),
            h('div.small', h('code', r.path), ' · ', fmtBytes(r.size)),
            r.error ? h('div.small', r.error) : null, tracks);
        }));
      }
      function tr(k) { return t(k); }

      // ---- channels / 频道 ----
      var chSel = '';
      // catalogs caches each library's items by ID, for titles and durations: at most PICK of them, a page at a time.
      // catalogs 缓存各媒体库的条目（按 ID），用于标题与时长：每次一页，至多 PICK 个。
      var catalogs = {}, chPreview = {}, PICK = 200;
      function catalog(name) {
        if (!name) return Promise.resolve(null);
        if (catalogs[name]) return catalogs[name];
        catalogs[name] = api('GET', '/media/library/' + enc(name) + '?limit=' + PICK).then(function (r) {
          var by = {};
          ((r && r.page) || []).forEach(function (it) { by[it.id] = it; });
          return { by: by, matched: (r && r.matched) || 0 };
        }, function () { delete catalogs[name]; return null; });
        return catalogs[name];
      }
      function channelsPage() {
        var el = sh.page('channels', t('n.channels'));
        var libs = [], status = {}, statusErr = '', statusEl = null, pushAll = null;
        function draftChannels() { var s = C.draft.streaming; return (s && s.channels) || []; }
        function refreshStatus() {
          return Promise.all([api('GET', '/streaming/channels').catch(function (e) { return { error: e }; }),
            api('GET', '/streaming/push').catch(function () { return null; })]).then(function (r) {
            status = {};
            statusErr = r[0] && r[0].error && r[0].error.status !== 404 ? r[0].error.message : '';
            ((r[0] && r[0].channels) || []).forEach(function (c) { status[c.name] = c; });
            if (statusEl && document.body.contains(statusEl)) drawStatus(statusEl);
            if (pushAll && document.body.contains(pushAll)) UI.mount(pushAll, pushTable((r[1] && r[1].push) || []));
          });
        }
        catalogs = {}; chPreview = {};
        api('GET', '/media/library').then(function (r) { libs = ((r && r.libraries) || []).map(function (l) { return l.name; }); }, function () { libs = []; })
          .then(function () { if (document.body.contains(el)) return C.page(el, sh, render, { members: ['streaming'] }); });
        UI.every(CHANNEL_EVERY, refreshStatus);

        function render(body) {
          var chs = draftChannels();
          body.appendChild(h('div.callout', UI.icon('info'), ' ', t('c.note')));
          var add = UI.btn(t('c.add'), 'plus', function () {
            var s = C.draft.streaming = C.draft.streaming || {};
            s.channels = s.channels || [];
            var name = uniqueName(s.channels, 'tv/channel' + (s.channels.length + 1));
            s.channels.push({ name: name, library: libs[0] || '', items: [] });
            chSel = name;
            C.changed(); C.redraw();
          }, 'sm');
          add.classList.add('md-add-channel');
          if (!chs.length) {
            body.appendChild(UI.empty(t('c.none'), 'radio'));
            body.appendChild(h('div.btn-row', add));
          } else {
            if (!chs.some(function (c) { return c.name === chSel; })) chSel = chs[0].name;
            body.appendChild(h('div.row.row--between', UI.tabs(chs.map(function (c) { return [c.name, c.name]; }), chSel, function (k) { chSel = k; C.redraw(); }), add));
            var i = 0;
            chs.forEach(function (c, k) { if (c.name === chSel) i = k; });
            body.appendChild(channelEditor(chs, i));
          }
          pushAll = h('div');
          body.appendChild(UI.card(t('c.push.all'), pushAll));
          refreshStatus();
        }

        // channelEditor is a channel's status, its settings, its playlist and its schedule. / 频道的状态、设置、播放列表与日程。
        function channelEditor(chs, i) {
          var ch = chs[i];
          WB.select({ channel: ch.name, library: ch.library, items: (ch.items || []).length, schedule: (ch.schedule || []).length });
          var path = 'streaming.channels[' + i + ']';
          statusEl = h('div.md-status', { 'data-channel': ch.name });
          drawStatus(statusEl);
          var picker = h('div.md-picker'), list = h('div.md-list'), sched = h('div.md-schedule');
          var settings = settingsRow(ch, path, function () { C.changed(); drawPicker(); drawList(); drawSchedule(); });
          function changed() { C.changed(); drawList(); drawSchedule(); }
          function drawList() { playlist(list, ch, path, changed); }
          function drawSchedule() { schedule(sched, ch, path, changed); }
          function drawPicker() { pickerOf(picker, ch, function (id) { ch.items = ch.items || []; ch.items.push(id); changed(); }); }
          drawPicker(); drawList(); drawSchedule();
          var remove = UI.btn(t('c.remove'), 'trash', function () {
            UI.confirm(t('c.remove.q', { name: ch.name }), t('c.remove'), true).then(function (ok) {
              if (!ok) return;
              chs.splice(i, 1);
              if (!chs.length) delete C.draft.streaming.channels;
              C.changed(); C.redraw();
            });
          }, 'sm ghost');
          // The preview's card is kept across the page's redraws, so that a draft's edit does not stop it.
          // 预览卡片在页面重绘之间保留，编辑草稿不会使其停止。
          var pv = chPreview[ch.name] = chPreview[ch.name] || previewCard(ui, { channel: ch.name });
          return h('div.stack',
            UI.card(ch.name, statusEl), pv,
            UI.card(null, h('div.stack', settings, h('div.md-timeline', h('div.stack', list, sched), picker)), remove));
        }
        function settingsRow(ch, path, redrawAll) {
          var name = h('input.input.input--sm', { value: ch.name || '', 'aria-label': t('c.name'), 'data-path': path + '.name' });
          name.addEventListener('change', function () { ch.name = name.value.trim(); chSel = ch.name; C.changed(); C.redraw(); });
          var libSel = h('select.select.select--sm', { 'aria-label': t('c.library'), 'data-path': path + '.library' },
            (libs.indexOf(ch.library) < 0 && ch.library ? [ch.library] : []).concat(libs).map(function (l) { return h('option', { value: l, selected: l === ch.library }, l); }));
          libSel.addEventListener('change', function () { ch.library = libSel.value; redrawAll(); });
          var shuffle = h('input', { type: 'checkbox', checked: ch.order === 'shuffle' });
          shuffle.addEventListener('change', function () { if (shuffle.checked) ch.order = 'shuffle'; else delete ch.order; C.changed(); });
          var loop = h('input', { type: 'checkbox', checked: ch.loop !== false });
          loop.addEventListener('change', function () { if (loop.checked) delete ch.loop; else ch.loop = false; C.changed(); });
          var tz = h('input.input.input--sm', { value: ch.time_zone || '', placeholder: 'UTC', 'aria-label': t('c.tz'), 'data-path': path + '.time_zone', 'data-fill': true });
          tz.addEventListener('change', function () { if (tz.value.trim()) ch.time_zone = tz.value.trim(); else delete ch.time_zone; C.changed(); redrawAll(); });
          var rend = h('input.input.input--sm', { value: ch.rendition || '', placeholder: t('c.rendition.ph'), 'aria-label': t('c.rendition'), 'data-path': path + '.rendition' });
          rend.addEventListener('change', function () { if (rend.value.trim()) ch.rendition = rend.value.trim(); else delete ch.rendition; C.changed(); });
          return h('div.md-settings',
            UI.field(t('c.name'), name), UI.field(t('c.library'), libSel), UI.field(t('c.tz'), tz), UI.field(t('c.rendition'), rend),
            h('label.check.md-check', shuffle, h('span', t('c.order'))), h('label.check.md-check', loop, h('span', t('c.loop'))));
        }

        // playlist draws the channel's items as a bar of their lengths and a list to reorder, drop into and remove from.
        // playlist 把频道的条目画为按时长比例的条形与列表：可调整顺序、拖入与删除。
        function playlist(box, ch, path, changed) {
          catalog(ch.library).then(function (cat) {
            var items = ch.items || [];
            var by = (cat && cat.by) || {};
            var total = 0;
            items.forEach(function (id) { total += (by[id] && by[id].duration) || 0; });
            var st = status[ch.name], nowId = st && st.now && st.now.item;
            var bar = h('div.md-bar', items.map(function (id, k) {
              var d = (by[id] && by[id].duration) || 0;
              return h('div.md-bar__seg' + (id === nowId ? '.is-now' : '') + (by[id] ? '' : '.is-unknown'),
                { 'data-item': id, style: { flexGrow: String(Math.max(d, total / 50 || 1)) }, title: ((by[id] && by[id].title) || id) + ' · ' + fmtClock(d) }, String(k + 1));
            }));
            var drag = null;
            var rows = items.map(function (id, k) {
              var it = by[id];
              var rm = UI.btn('', 'trash', function () { items.splice(k, 1); if (!items.length) delete ch.items; changed(); }, 'ghost icon sm');
              rm.setAttribute('aria-label', t('cf.remove'));
              var row = h('div.md-row' + (id === nowId ? '.is-now' : ''), {
                draggable: 'true', 'data-index': k, 'data-item': id, 'data-path': path + '.items[' + k + ']', title: t('c.drag'),
                ondragstart: function (e) { drag = k; e.dataTransfer.effectAllowed = 'move'; e.dataTransfer.setData('text/plain', 'fit-row:' + k); },
                ondragover: function (e) { e.preventDefault(); row.classList.add('is-over'); },
                ondragleave: function () { row.classList.remove('is-over'); },
                ondrop: function (e) {
                  e.preventDefault(); e.stopPropagation();
                  row.classList.remove('is-over');
                  var data = e.dataTransfer.getData('text/plain') || '';
                  if (data.indexOf('fit-item:') === 0) { items.splice(k, 0, data.slice(9)); ch.items = items; changed(); return; }
                  if (drag == null || drag === k) return;
                  var x = items.splice(drag, 1)[0];
                  items.splice(k, 0, x);
                  drag = null;
                  changed();
                }
              }, h('span.md-grip', '⋮⋮'), h('span.md-row__n', String(k + 1)),
              h('span.md-row__title.ellipsis', it ? it.title || id : id), h('code.small.muted', id),
              it ? h('span.small.muted', fmtClock(it.duration)) : UI.badge(t('c.unknown'), 'warn'), rm);
              return row;
            });
            var drop = h('div.md-drop', {
              ondragover: function (e) { e.preventDefault(); drop.classList.add('is-over'); },
              ondragleave: function () { drop.classList.remove('is-over'); },
              ondrop: function (e) {
                e.preventDefault();
                drop.classList.remove('is-over');
                var data = e.dataTransfer.getData('text/plain') || '';
                if (data.indexOf('fit-item:') === 0) { ch.items = items; items.push(data.slice(9)); changed(); }
                else if (drag != null) { items.push(items.splice(drag, 1)[0]); drag = null; changed(); }
              }
            }, t('c.list.drop'));
            UI.mount(box, h('div.stack',
              h('div.row.row--between', h('b', t('c.list')), h('span.muted.small', t('c.list.total', { n: items.length, d: fmtClock(total) }))),
              items.length ? bar : null, h('div.md-rows', rows), drop));
          });
        }

        // schedule draws today's slots on a day in the channel's time zone, and the slots' list. / 日程：按频道时区画当天时段与列表。
        function schedule(box, ch, path, changed) {
          catalog(ch.library).then(function (cat) {
            var by = (cat && cat.by) || {};
            var slots = ch.schedule || [];
            var tz = ch.time_zone || 'UTC';
            var now = zoned(new Date(), tz);
            var day = h('div.md-day', { title: t('c.slot.hint') });
            for (var hr = 0; hr <= 24; hr += 3) day.appendChild(h('span.md-day__tick', { style: { left: (hr / 24 * 100) + '%' } }, (hr < 10 ? '0' : '') + hr + ':00'));
            if (now) day.appendChild(h('span.md-day__now', { style: { left: (now.minutes / 1440 * 100) + '%' } }));
            slots.forEach(function (s, k) {
              var at = slotToday(s.at, now);
              if (at == null) return;
              var d = (by[s.item] && by[s.item].duration) || 0;
              day.appendChild(h('span.md-day__slot', { style: { left: (at / 1440 * 100) + '%', width: Math.max(0.6, Math.min(d / 60, 1440 - at) / 1440 * 100) + '%' },
                title: s.at + ' · ' + ((by[s.item] && by[s.item].title) || s.item), 'data-slot': k }, (by[s.item] && by[s.item].title) || s.item));
            });
            day.addEventListener('dragover', function (e) { e.preventDefault(); });
            day.addEventListener('drop', function (e) {
              e.preventDefault();
              var data = e.dataTransfer.getData('text/plain') || '';
              if (data.indexOf('fit-item:') !== 0) return;
              var r = day.getBoundingClientRect();
              var min = Math.round(Math.max(0, Math.min(1, (e.clientX - r.left) / r.width)) * 1440 / 5) * 5 % 1440;
              ch.schedule = slots;
              slots.push({ at: hhmm(min), item: data.slice(9) });
              changed();
            });
            var rows = slots.map(function (s, k) {
              var at = h('input.input.input--sm.md-at', { value: s.at || '', 'aria-label': t('c.slot.at'), placeholder: '20:00', 'data-path': path + '.schedule[' + k + '].at' });
              at.addEventListener('change', function () {
                if (!validAt(at.value.trim())) { at.setCustomValidity(t('c.slot.bad')); at.reportValidity(); return; }
                at.setCustomValidity('');
                s.at = at.value.trim();
                changed();
              });
              var ids = (ch.items || []).slice();
              if (s.item && ids.indexOf(s.item) < 0) ids.unshift(s.item);
              Object.keys(by).forEach(function (id) { if (ids.indexOf(id) < 0) ids.push(id); });
              var item = h('select.select.select--sm', { 'aria-label': t('c.slot.item'), 'data-path': path + '.schedule[' + k + '].item' },
                ids.map(function (id) { return h('option', { value: id, selected: id === s.item }, (by[id] && by[id].title ? by[id].title + ' · ' : '') + id); }));
              item.addEventListener('change', function () { s.item = item.value; changed(); });
              var rm = UI.btn('', 'trash', function () { slots.splice(k, 1); if (!slots.length) delete ch.schedule; changed(); }, 'ghost icon sm');
              rm.setAttribute('aria-label', t('cf.remove'));
              return h('div.md-slot', at, item, rm);
            });
            var add = UI.btn(t('c.slot.add'), 'plus', function () {
              ch.schedule = slots;
              slots.push({ at: hhmm(now ? (Math.ceil(now.minutes / 15) * 15) % 1440 : 1200), item: (ch.items || [])[0] || Object.keys(by)[0] || '' });
              changed();
            }, 'sm');
            UI.mount(box, h('div.stack',
              h('div.row.row--between', h('b', t('c.schedule')), h('span.muted.small', t('c.today', { tz: tz }))),
              day, h('div.md-slots', rows), h('div.row', add, h('span.muted.small', t('c.slot.hint')))));
          });
        }

        // pickerOf lists the channel's library's items, each dragged into the playlist or the day, or added with +.
        // pickerOf 列出频道媒体库的条目：可拖入播放列表或当天时间轴，或用 + 添加。
        function pickerOf(box, ch, add) {
          var search = h('input.input.input--sm', { type: 'search', placeholder: t('f.search'), 'aria-label': t('f.search') });
          var list = h('div.md-picker__list');
          function draw() {
            if (!ch.library) { UI.mount(list, UI.empty(t('c.picker.none'))); return; }
            catalog(ch.library).then(function (cat) {
              var q = search.value.trim().toLowerCase();
              var items = Object.keys((cat && cat.by) || {}).map(function (id) { return cat.by[id]; }).filter(function (it) {
                return !it.error && (!q || it.id.toLowerCase().indexOf(q) >= 0 || String(it.title || '').toLowerCase().indexOf(q) >= 0);
              });
              UI.mount(list, items.length ? items.map(function (it) {
                var plus = UI.btn('', 'plus', function () { add(it.id); }, 'ghost icon sm');
                plus.setAttribute('aria-label', t('c.picker.add'));
                plus.title = t('c.picker.add');
                return h('div.md-pick', { draggable: 'true', 'data-item': it.id,
                  ondragstart: function (e) { e.dataTransfer.effectAllowed = 'copy'; e.dataTransfer.setData('text/plain', 'fit-item:' + it.id); } },
                h('span.md-grip', '⋮⋮'), h('div.md-pick__text', h('div.ellipsis', it.title || it.id), h('div.small.muted.ellipsis', it.id + ' · ' + fmtClock(it.duration))), plus);
              }) : UI.empty(t('i.none')));
            });
          }
          search.addEventListener('input', draw);
          UI.mount(box, h('div.stack', h('b', t('c.picker')), search, list));
          draw();
        }

        // drawStatus is what the channel does now: its state, now and next with the position, its counters, start, stop,
        // skip, and its push targets. / 频道当前状态：正在播放与下一个（含位置）、计数、开始/停止/跳过与推流目标。
        function drawStatus(box) {
          var name = box.getAttribute('data-channel'), st = status[name];
          if (statusErr) { UI.mount(box, h('div.callout.callout--danger', t('c.status.failed', { e: statusErr }))); return; }
          if (!st) { UI.mount(box, h('div.callout.callout--warn', UI.icon('info'), ' ', t('c.notrunning'))); return; }
          // The playlist marks the item playing now. / 播放列表标出正在播放的条目。
          var nowId = st.now && st.now.item;
          document.querySelectorAll('.md-row[data-item], .md-bar__seg[data-item]').forEach(function (x) {
            x.classList.toggle('is-now', x.getAttribute('data-item') === nowId);
          });
          function act(action, item, b) {
            var p = api('POST', '/streaming/channels/' + idPath(name), item ? { action: action, item: item } : { action: action }).then(function (r) {
              status[name] = r; drawStatus(box);
            });
            return (b ? UI.busy(b, p) : p).catch(UI.fail);
          }
          var stateKind = st.state === 'playing' ? 'ok' : st.state === 'stopped' ? 'danger' : 'warn';
          var start = UI.btn(t('c.start'), 'play', function () { act('start', null, start); }, 'sm');
          var stop = UI.btn(t('c.stop'), 'stop', function () {
            UI.confirm(t('c.stop.q', { name: name }), t('c.stop'), true).then(function (ok) { if (ok) act('stop', null, stop); });
          }, 'sm');
          var skip = UI.btn(t('c.skip'), 'forward', function () { act('skip', null, skip); }, 'sm');
          start.classList.add('md-act-start'); stop.classList.add('md-act-stop'); skip.classList.add('md-act-skip');
          start.disabled = st.state !== 'stopped';
          stop.disabled = skip.disabled = st.state === 'stopped';
          function itemLine(x, now) {
            if (!x) return '—';
            var pos = now ? Math.min(x.position || 0, x.duration || 0) : x.offset || 0;
            return h('div.md-now',
              h('div.row', h('b', x.title || x.item), h('code.small.muted', x.item), x.rendition ? UI.badge(x.rendition) : null, UI.badge(x.from)),
              now ? UI.meter(fmtClock(pos) + ' / ' + fmtClock(x.duration), pos, x.duration || 1, fmtClock((x.duration || 0) - pos)) :
                h('span.small.muted', UI.fmtTime(unix(x.started))));
          }
          UI.mount(box, h('div.stack',
            h('div.row', UI.badge(st.state, stateKind, true), st.error ? h('span.small.muted', st.error) : null, h('span.btn-row', start, stop, skip)),
            UI.kv([
              [t('c.now'), itemLine(st.now, true)],
              [t('c.next'), st.next ? h('div.row', itemLine(st.next, false), (function () {
                var b = UI.btn(t('c.skipto'), 'forward', function () { act('skip', st.next.item, b); }, 'sm ghost');
                return b;
              })()) : '—'],
              [t('c.nextslot'), st.next_slot ? st.next_slot.item + ' · ' + UI.fmtTime(unix(st.next_slot.at)) : '—'],
              [t('c.buffered'), fmtClock(st.buffered) + ' · ' + fmtBytes(st.buffered_bytes)],
              [t('c.counters'), st.played + ' · ' + st.failed + ' · ' + st.underruns]
            ]),
            h('b', t('c.push')), pushTable(st.push || [])));
        }
        function pushTable(list) {
          return UI.table([
            { label: t('ps.name'), render: function (p) { return h('span', h('b', p.name), p.channel ? null : h('span.muted.small', ' · ' + p.stream)); } },
            { label: t('ps.url'), render: function (p) { return h('code.small', p.url); } },
            { label: t('ps.state'), render: function (p) {
              var k = p.state === 'connected' ? 'ok' : p.state === 'stopped' ? 'danger' : 'warn';
              return h('span', UI.badge(p.state, k, true), p.retry_in ? h('span.small.muted', ' ' + t('ps.retry', { s: Math.ceil(p.retry_in) })) : null);
            } },
            { label: t('ps.error'), render: function (p) {
              if (!p.error) return '—';
              return h('div.ai-askrow', h('span.small', p.error), askOf({ skill: 'diagnose',
                message: t('ps.ask', { name: p.name, stream: p.stream, state: p.state, error: p.error }), inputs: {},
                selection: { push: { name: p.name, stream: p.stream, channel: p.channel, protocol: p.protocol, server: p.url, state: p.state, error: p.error,
                  connects: p.connects, failures: p.failures } } }, { label: false }));
            } },
            { label: t('ps.bytes'), render: function (p) { return fmtBytes(p.bytes); } },
            { label: t('ps.bitrate'), render: function (p) { return fmtBits(p.bitrate); } },
            { label: t('ps.connects'), render: function (p) { return p.connects + ' · ' + p.failures; } },
            { label: t('actions'), render: function (p) {
              function act(a, b) {
                return UI.busy(b, api('POST', '/streaming/push/' + idPath(p.name), { action: a })).then(refreshStatus).catch(UI.fail);
              }
              var s1 = UI.btn(t('c.start'), 'play', function () { act('start', s1); }, 'sm ghost');
              var s2 = UI.btn(t('c.stop'), 'stop', function () { act('stop', s2); }, 'sm ghost');
              var s3 = UI.btn(t('ps.restart'), 'refresh', function () { act('restart', s3); }, 'sm ghost');
              s1.disabled = p.state !== 'stopped';
              s2.disabled = p.state === 'stopped';
              return h('div.btn-row', s1, s2, s3);
            } }
          ], list, { empty: t('c.push.none') });
        }
      }

      // ---- playback URLs, keys and sessions / 播放 URL、密钥与会话 ----
      var polSel = '';
      function playbackPage() {
        var el = sh.page('playback', t('n.playback'));
        catalogs = {};
        el.appendChild(h('div', UI.icon('refresh'), ' ', t('loading')));
        Promise.all([api('GET', '/media/auth'), C.ensure().catch(function () { return null; })]).then(function (r) {
          var pols = (r[0] && r[0].policies) || [];
          if (!pols.length) { UI.mount(el, UI.empty(t('u.none'), 'key')); return; }
          if (!pols.some(function (p) { return p.name === polSel; })) {
            var signing = pols.filter(function (p) { return p.url_signing; })[0];
            polSel = (signing || pols[0]).name;
          }
          var pol = pols.filter(function (p) { return p.name === polSel; })[0];
          WB.select({ policy: pol.name, url_signing: pol.url_signing, sessions: pol.sessions, refusals: pol.refusals });
          var tabs = pols.length > 1 ? UI.tabs(pols.map(function (p) { return [p.name, p.name]; }), polSel, function (k) { polSel = k; playbackPage(); }) : null;
          var parts = [tabs];
          if (!pol.url_signing) {
            parts.push(h('div.callout.callout--warn', UI.icon('info'), ' ', t('u.nosign', { name: pol.name }), ' ',
              h('a', { href: '#/' + ID + '/url_signing' }, t('n.urlsign'))));
          } else {
            var s = pol.settings || {};
            parts.push(UI.card(t('u.settings'), UI.kv([
              ['ttl', s.ttl], ['play_grace', s.play_grace], ['session_idle', s.session_idle],
              ['max_sessions', s.max_sessions ? String(s.max_sessions) : '∞'], ['on_limit', s.on_limit], ['cookie', String(!!s.cookie)],
              ['bind_address', s.bind_address], ['referers', (s.referers || []).join(', ') || '—'], ['origins', (s.origins || []).join(', ') || '—']
            ]), h('a.btn.btn--sm', { href: '#/' + ID + '/url_signing' }, UI.icon('edit'), t('u.edit'))));
            parts.push(testCard(pol));
          }
          parts.push(keysCard(pol));
          parts.push(refusalsCard(pol));
          var sess = h('div');
          parts.push(UI.card(t('u.sessions'), sess));
          UI.mount(el, h('div.stack', parts));
          sessions(sess, pol);
        }).catch(function (e) { fail(el, e); });
      }
      function keysCard(pol) {
        var keys = pol.keys || [];
        var ring = keys.some(function (k) { return k.name; });
        function rotate(body, label, b) {
          return UI.confirm(body.all ? t('u.rotateall.q', { name: pol.name }) : t('u.rotate.q', { key: label }), t('u.rotate'), true).then(function (ok) {
            if (!ok) return;
            return UI.busy(b, api('POST', '/media/auth/' + enc(pol.name) + '/rotate', body)).then(function (r) {
              UI.toast(t('u.rotated', { keys: ((r && r.rotated) || []).map(function (k) { return k || t('u.single'); }).join(', ') }));
              playbackPage();
            }).catch(UI.fail);
          });
        }
        var all = null;
        if (ring && keys.length > 1) {
          all = UI.btn(t('u.rotateall'), 'refresh', function () { rotate({ all: true }, '', all); }, 'sm danger');
          all.disabled = keys.some(function (k) { return !k.file; });
          if (all.disabled) all.title = t('u.inline.t');
        }
        return UI.card(t('u.keys'), UI.table([
          { label: t('u.key'), render: function (k) { return h('span', h('b', k.name || t('u.single')), k['default'] ? [' ', UI.badge(t('it.default'), 'ok')] : null); } },
          { label: t('u.source'), render: function (k) { return k.file ? t('u.file') : t('u.inline'); } },
          { label: t('u.age'), render: function (k) { return k.modified ? h('span', { title: UI.fmtTime(unix(k.modified)) }, UI.ago(unix(k.modified))) : '—'; } },
          { label: t('actions'), render: function (k) {
            var b = UI.btn(t('u.rotate'), 'refresh', function () { rotate(k.name ? { key: k.name } : {}, k.name || t('u.single'), b); }, 'sm');
            b.classList.add('md-rotate');
            b.disabled = !k.file;
            if (!k.file) b.title = t('u.inline.t');
            return b;
          } }
        ], keys), all);
      }
      function refusalsCard(pol) {
        var r = pol.refusals || {}, reasons = Object.keys(r);
        var total = reasons.reduce(function (a, k) { return a + (r[k] || 0); }, 0);
        return UI.card(t('u.refusals'), h('div.stack',
          h('div.md-reasons', reasons.map(function (k) { return h('div.md-reason' + (r[k] ? '.is-hot' : ''), h('span.muted.small', k), h('b', String(r[k] || 0))); })),
          h('span.muted.small', t('u.sessions.n', { n: pol.sessions || 0, s: pol.subjects || 0 }))),
        total ? askOf(forbiddenAsk(pol.name, r), { label: true }) : null);
      }
      function testCard(pol) {
        var draft = C.draft || {};
        var vods = (draft.routes || []).filter(isVod).filter(function (r) { return r.handler.params && r.handler.params.auth === pol.name; });
        var subject = h('input.input.input--sm', { value: 'test-viewer', 'aria-label': t('u.subject') });
        var pathIn = h('input.input.input--sm.md-path', { placeholder: '/vod/films/films/bbb/index.m3u8', 'aria-label': t('u.path') });
        var address = h('input.input.input--sm', { placeholder: '203.0.113.7', 'aria-label': t('u.address') });
        var ttl = h('input.input.input--sm', { placeholder: (pol.settings && pol.settings.ttl) || '10m', value: '2m', 'aria-label': t('u.ttl') });
        var route = h('select.select.select--sm', { 'aria-label': t('u.route') }, h('option', { value: '' }, t('u.route.none')),
          (pol.token_routes || []).map(function (r) { return h('option', { value: r.route }, r.route + (r.url_prefixes && r.url_prefixes.length ? ' · ' + r.url_prefixes.join(', ') : '')); }));
        var pick = h('select.select.select--sm', { 'aria-label': t('u.item') }, h('option', { value: '' }, t('u.item.pick')));
        vods.forEach(function (r) {
          var p = r.handler.params, prefix = p.prefix || '/vod/' + p.library + '/';
          if (prefix.slice(-1) !== '/') prefix += '/';
          catalog(p.library).then(function (cat) {
            Object.keys((cat && cat.by) || {}).forEach(function (id) {
              if (cat.by[id].error) return;
              pick.appendChild(h('option', { value: prefix + id + '/index.m3u8' }, p.library + ' · ' + (cat.by[id].title || id)));
            });
          });
        });
        pick.addEventListener('change', function () { if (pick.value) pathIn.value = pick.value; });
        var out = h('div.stack.md-test-out');
        var issue = UI.btn(t('u.issue'), 'play', function () {
          var body = { subject: subject.value.trim(), path: pathIn.value.trim() };
          if (address.value.trim()) body.address = address.value.trim();
          if (ttl.value.trim()) body.ttl = ttl.value.trim();
          if (route.value) body.route = route.value;
          UI.busy(issue, api('POST', '/media/auth/' + enc(pol.name) + '/urls', body)).then(function (r) {
            var shown = h('input.input.input--sm.md-url', { value: r.url, readonly: true, 'aria-label': 'URL' });
            var copy = UI.btn(t('copy'), 'copy', function () { UI.copy(r.url); }, 'sm');
            var parts = [h('div.row', shown, copy), h('div.small.muted', t('u.expires', { at: UI.fmtTime(r.expires), sid: r.session }))];
            if (/^https?:/.test(r.url)) parts.push(player(ui, r.url).el);
            UI.mount(out, parts);
          }).catch(function (e) { UI.mount(out, h('div.callout.callout--danger', e.message || String(e))); });
        }, 'primary sm');
        issue.classList.add('md-issue');
        return UI.card(t('u.test'), h('div.stack',
          h('div.callout.small', UI.icon('info'), ' ', t('u.test.note')),
          h('div.md-form', UI.field(t('u.subject'), subject), UI.field(t('u.item'), pick), UI.field(t('u.path'), pathIn),
            UI.field(t('u.address'), address), UI.field(t('u.ttl'), ttl), UI.field(t('u.route'), route)),
          h('div.btn-row', issue), out));
      }
      function sessions(box, pol) {
        var st = { offset: 0, subject: '' };
        var filter = h('input.input.input--sm', { type: 'search', placeholder: t('u.filter'), 'aria-label': t('u.filter') });
        filter.addEventListener('change', function () { st.subject = filter.value.trim(); st.offset = 0; load(); });
        var list = h('div');
        UI.mount(box, h('div.stack', h('div.row', filter, UI.btn(t('refresh'), 'refresh', function () { load(); }, 'sm')), list));
        var seq = 0;
        function load() {
          var mine = ++seq;
          var q = '?offset=' + st.offset + '&limit=' + PAGE + (st.subject ? '&subject=' + enc(st.subject) : '');
          return api('GET', '/media/auth/' + enc(pol.name) + '/sessions' + q).then(function (r) {
            if (!document.body.contains(list) || mine !== seq) return;
            var counts = {};
            ((r && r.subjects) || []).forEach(function (s) { counts[s.subject] = s.sessions; });
            var rows = (r && r.page) || [];
            var pager = h('div.row.row--between.md-pager',
              h('span.muted.small', r.matched ? t('p.of', { from: r.offset + 1, to: r.offset + rows.length, n: r.matched }) : ''),
              h('div.btn-row',
                UI.btn(t('p.prev'), null, function () { st.offset = Math.max(0, st.offset - PAGE); load(); }, 'sm'),
                UI.btn(t('p.next'), null, function () { st.offset += PAGE; load(); }, 'sm')));
            pager.querySelectorAll('button')[0].disabled = st.offset <= 0;
            pager.querySelectorAll('button')[1].disabled = r.offset + rows.length >= r.matched;
            UI.mount(list, UI.table([
              { label: t('u.subject'), render: function (s) { return h('b', s.subject); } },
              { label: t('u.count'), render: function (s) {
                var n = counts[s.subject] || 0, max = r.max_sessions;
                return UI.badge(n + ' / ' + (max || '∞'), max && n >= max ? 'warn' : '');
              } },
              { label: t('u.sid'), render: function (s) { return h('code.small', s.id); } },
              { label: t('u.resource'), render: function (s) { return h('code.small', s.resource); } },
              { label: t('u.address'), render: function (s) { return s.address || '—'; } },
              { label: t('u.start'), render: function (s) { return UI.ago(unix(s.start)); } },
              { label: t('u.last'), render: function (s) { return UI.ago(unix(s.last)); } },
              { label: t('actions'), render: function (s) {
                if (s.ended) return UI.badge(t('u.ended'), 'danger');
                var b = UI.btn(t('u.end'), 'stop', function () {
                  UI.confirm(t('u.end.q', { sid: s.id, subject: s.subject }), t('u.end'), true).then(function (ok) {
                    if (!ok) return;
                    UI.busy(b, api('POST', '/media/auth/' + enc(pol.name) + '/sessions/end', { subject: s.subject, id: s.id })).then(function () {
                      UI.toast(t('u.end.done'));
                      load();
                    }).catch(UI.fail);
                  });
                }, 'sm danger');
                b.classList.add('md-end');
                return b;
              } }
            ], rows, { empty: t('u.sess.none') }), pager);
          }).catch(function (e) { fail(list, e); });
        }
        load();
        UI.every(SESSIONS_EVERY, function () { if (document.body.contains(list)) load(); });
      }

      // ---- metrics / 指标 ----
      function forbiddenAsk(name, r) {
        return function () {
          var list = Object.keys(r || {}).filter(function (k) { return r[k]; }).map(function (k) { return k + ' ' + r[k]; }).join(', ');
          return { skill: 'diagnose', message: t('x.ask', { name: name, list: list }), inputs: { status: '403' },
            selection: { policy: name, refusals: r } };
        };
      }
      function metricsPage() {
        var el = sh.page('metrics', t('n.metrics'));
        var box = h('div.stack');
        UI.mount(el, h('div.callout.small', UI.icon('info'), ' ', t('x.note', { s: STATS_EVERY / 1000 })), box);
        var last = null;
        function rate(cur, prev, dt) { return prev == null || dt <= 0 ? null : Math.max(0, (cur - prev) / dt); }
        // prefixes are the paths of each library's vod routes, as the config writes them: a session's resource under one
        // of them is a viewer of that library. / prefixes 为各媒体库 vod 路由的路径前缀：资源位于其下的会话即该库的观看者。
        var prefixes = {};
        function prefixesOf(draft) {
          prefixes = {};
          ((draft && draft.routes) || []).filter(isVod).forEach(function (r) {
            var p = r.handler.params || {}, pre = p.prefix || '/vod/' + p.library + '/';
            if (pre.slice(-1) !== '/') pre += '/';
            (prefixes[p.library] = prefixes[p.library] || []).push(pre);
          });
        }
        function viewersOf(s, library) {
          var n = 0;
          (s.policies || []).forEach(function (pol) {
            (pol.resources || []).forEach(function (r) {
              if ((prefixes[library] || []).some(function (pre) { return r.resource.indexOf(pre) === 0; })) n += r.sessions;
            });
          });
          return n;
        }
        var ready = C.ensure().then(function () { prefixesOf(C.draft); }, function () { /* the counts without viewers / 无观看数 */ });
        function load() {
          return ready.then(function () { return api('GET', '/media/stats'); }).then(function (s) {
            if (!document.body.contains(box)) return;
            var prev = last;
            last = s;
            var dt = prev ? (s.time - prev.time) / 1000 : 0;
            function before(list, key, name) { return prev ? ((prev[list] || []).filter(function (x) { return x[key] === name; })[0] || null) : null; }
            var parts = [];
            var libs = s.libraries || [];
            if (libs.length) {
              parts.push(UI.card(t('x.libraries'), h('div.md-cards', libs.map(function (l) {
                var p = before('libraries', 'name', l.name), tb = l.tables || {}, pool = l.pool || {};
                var lookups = (tb.hits || 0) + (tb.misses || 0);
                var dh = p ? (tb.hits || 0) - ((p.tables || {}).hits || 0) : 0, dm = p ? (tb.misses || 0) - ((p.tables || {}).misses || 0) : 0;
                return h('div.md-metric', { 'data-library': l.name }, h('b', l.name),
                  UI.kv([
                    [t('l.items'), l.items + (l.errors ? ' · ' + t('l.errors') + ' ' + l.errors : '')],
                    [t('x.queue'), t('x.queue.v', { busy: pool.busy || 0, queued: pool.queued || 0, queue: pool.queue || 0 })],
                    [t('x.refused'), t('x.refused.v', { refused: pool.refused || 0, expired: pool.expired || 0 })],
                    [t('x.tables'), fmtBytes(tb.bytes) + ' / ' + fmtBytes(tb.max) + ' · ' + t('x.hit', { p: dh + dm > 0 ? pct(dh, dh + dm) : pct(tb.hits || 0, lookups) })]
                  ]));
              }))));
            }
            var vods = s.vod || [];
            if (vods.length) {
              parts.push(UI.card(t('x.vod'), h('div.md-cards', vods.map(function (v) {
                var p = before('vod', 'library', v.library), c = v.cache || {}, pc = (p && p.cache) || {};
                var rq = rate(v.requests, p && p.requests, dt), by = rate(v.bytes, p && p.bytes, dt);
                var dh = p ? (c.hits || 0) - (pc.hits || 0) : 0, dm = p ? (c.misses || 0) - (pc.misses || 0) : 0;
                var viewers = viewersOf(s, v.library);
                return h('div.md-metric', { 'data-vod': v.library }, h('b', v.library),
                  UI.kv([
                    [t('x.viewers'), t('x.viewers.v', { n: viewers })],
                    [t('x.requests'), rq == null ? String(v.requests) : t('x.persec', { v: rq.toFixed(1) })],
                    [t('x.sent'), by == null ? fmtBytes(v.bytes) : t('x.persec', { v: fmtBytes(by) })],
                    [t('x.cache'), fmtBytes(c.bytes) + ' / ' + fmtBytes(c.max) + ' · ' + t('x.hit', { p: dh + dm > 0 ? pct(dh, dh + dm) : pct(c.hits || 0, (c.hits || 0) + (c.misses || 0)) })]
                  ]));
              }))));
            }
            var pols = s.policies || [];
            if (pols.length) {
              parts.push(UI.card(t('x.policies'), h('div.md-cards', pols.map(function (pol) {
                var r = pol.refusals || {}, p = before('policies', 'name', pol.name), pr = (p && p.refusals) || {};
                var any = Object.keys(r).some(function (k) { return r[k]; });
                return h('div.md-metric', { 'data-policy': pol.name }, h('div.ai-askrow', h('b', pol.name), any ? askOf(forbiddenAsk(pol.name, r), { label: false }) : null),
                  UI.kv([[t('x.viewers'), t('x.viewers.v', { n: pol.sessions || 0 })]]),
                  h('div.muted.small', t('x.forbidden')),
                  h('div.md-reasons', Object.keys(r).map(function (k) {
                    var d = p ? (r[k] || 0) - (pr[k] || 0) : 0;
                    return h('div.md-reason' + (r[k] ? '.is-hot' : ''), h('span.muted.small', k), h('b', String(r[k] || 0)), d > 0 ? h('span.small', ' +' + d) : null);
                  })));
              }))));
            }
            var chs = s.channels || [];
            if (chs.length) {
              parts.push(UI.card(t('x.channels'), UI.table([
                { label: t('c.name'), render: function (c) { return h('b', c.name); } },
                { label: t('c.state'), render: function (c) { return UI.badge(c.state, c.state === 'playing' ? 'ok' : 'warn', true); } },
                { label: t('x.players'), render: function (c) { return String(c.players || 0); } },
                { label: t('c.push'), render: function (c) {
                  return (c.push || []).map(function (p) { return h('div.small', p.name, ' · ', UI.badge(p.state, p.state === 'connected' ? 'ok' : 'warn'), ' ', fmtBits(p.bitrate), ' · ', fmtBytes(p.bytes)); });
                } }
              ], chs)));
            }
            UI.mount(box, parts.length ? parts : UI.empty(t('x.none'), 'activity'));
          }).catch(function (e) { if (document.body.contains(box)) fail(box, t('x.failed', { e: e.message || String(e) })); });
        }
        load();
        UI.every(STATS_EVERY, load);
      }

      // ---- the config pages / 配置页 ----
      function configPage(nav, title, members, render) {
        var el = sh.page(nav, title);
        return C.page(el, sh, render, { members: members });
      }
      function libraryConfig() {
        configPage('config', t('n.config'), ['media_library'], function (body) {
          body.appendChild(C.section('media_library', {
            addLabel: t('cf.lib.add'),
            make: function (arr) { return { name: uniqueName(arr, 'films'), roots: ['/srv/media'] }; },
            chips: function (l) { return ((l && l.roots) || []).slice(0, 3).map(function (r) { return h('code.small', r); }); }
          }));
        });
      }
      function vodConfig() {
        configPage('vod', t('n.vod'), ['routes'], function (body) {
          var libs = (C.draft.media_library || []).map(function (l) { return l && l.name; }).filter(Boolean);
          body.appendChild(h('div.callout', UI.icon('info'), ' ', t('cf.vod.note')));
          body.appendChild(C.section('routes', {
            noDoc: true, addLabel: t('cf.vod.add'), empty: t('cf.vod.none'),
            filter: function (r) { return isVod(r); },
            make: function () {
              var l = libs[0] || 'films';
              return { match: { path: ['/vod/' + l + '/*'] }, handler: { module: 'vod', params: { library: l } } };
            },
            chips: function (r) {
              var p = (r && r.handler && r.handler.params) || {}, m = (r && r.match) || {};
              return [UI.badge(p.library || '?', 'primary')].concat((m.path || []).slice(0, 2).map(function (x) { return h('code.small', x); }),
                p.auth ? [h('span.muted.small', 'auth ' + p.auth)] : [], p.cors ? [] : [h('span.muted.small', 'no cors')]);
            }
          }));
        });
      }
      function channelsConfig() {
        configPage('channels_config', t('n.chconfig'), ['streaming'], function (body) {
          body.appendChild(h('div.callout', UI.icon('info'), ' ', t('cf.ch.note'), ' ', h('a', { href: '#/' + ID + '/channels' }, t('n.channels'))));
          body.appendChild(C.subsection('streaming.channels', {
            addLabel: t('cf.ch.add'),
            make: function (arr) {
              var l = ((C.draft.media_library || [])[0] || {}).name || '';
              return { name: uniqueName(arr, 'tv/channel' + (arr.length + 1)), library: l };
            },
            chips: function (c) { return [c && c.library ? UI.badge(c.library, 'primary') : null, h('span.muted.small', ((c && c.items) || []).length + ' items')]; }
          }));
          body.appendChild(C.subsection('streaming.push', {
            addLabel: t('cf.push.add'),
            make: function () { return { stream: '', url: 'rtmp://' }; },
            chips: function (p) { return [p && p.stream ? h('code.small', p.stream) : null]; }
          }));
        });
      }
      function urlSigningConfig() {
        configPage('url_signing', t('n.urlsign'), ['media_auth'], function (body) {
          var pols = C.draft.media_auth || [];
          body.appendChild(h('div.callout', UI.icon('info'), ' ', t('cf.us.note')));
          if (!pols.length) {
            body.appendChild(UI.empty(t('cf.us.none'), 'key'));
            body.appendChild(h('div.btn-row', h('a.btn', { href: '#/gw.mediaauth/media_auth' }, UI.icon('key'), t('cf.mediaauth'))));
            return;
          }
          pols.forEach(function (p, i) {
            body.appendChild(h('div.stack', h('b', t('cf.us.policy', { name: (p && p.name) || '#' + (i + 1) })),
              C.subsection('media_auth[' + i + '].url_signing', { noDoc: i > 0 })));
          });
        });
      }

      return {
        routes: {
          'library': libraryPage, 'item/:lib/:id': itemPage, 'channels': channelsPage, 'playback': playbackPage,
          'metrics': metricsPage, 'config': libraryConfig, 'vod': vodConfig, 'channels_config': channelsConfig,
          'url_signing': urlSigningConfig
        },
        fallback: 'library'
      };
    }
  });

  // ---- time in a channel's zone / 频道时区中的时间 ----
  // zoned is the time of day and the weekday and date of d in the IANA zone tz ('Local' for the browser's), or null for
  // a zone the browser does not know. / zoned 返回 d 在 IANA 时区 tz 中的当天分钟数、星期与日期；浏览器不认识的时区为 null。
  function zoned(d, tz) {
    try {
      var o = tz && tz !== 'Local' ? { timeZone: tz } : {};
      o.hourCycle = 'h23'; o.year = 'numeric'; o.month = '2-digit'; o.day = '2-digit'; o.hour = '2-digit'; o.minute = '2-digit'; o.weekday = 'short';
      var parts = {};
      new Intl.DateTimeFormat('en-US', o).formatToParts(d).forEach(function (p) { parts[p.type] = p.value; });
      return { minutes: (+parts.hour % 24) * 60 + (+parts.minute), weekday: String(parts.weekday).toLowerCase().slice(0, 3),
        date: parts.year + '-' + parts.month + '-' + parts.day };
    } catch (e) { return null; }
  }
  var DAYS = ['sun', 'mon', 'tue', 'wed', 'thu', 'fri', 'sat'];
  var AT = /^(?:((?:[a-z]{3}(?:-[a-z]{3})?)(?:,[a-z]{3}(?:-[a-z]{3})?)*)\s+)?(?:(\d{4}-\d{2}-\d{2})[ T])?(\d{1,2}):(\d{2})(?::(\d{2}))?$/i;
  function validAt(s) {
    var m = AT.exec(s || '');
    if (!m || (m[1] && m[2]) || +m[3] > 23 || +m[4] > 59 || (m[5] && +m[5] > 59)) return false;
    return !m[1] || m[1].toLowerCase().split(',').every(function (r) { return r.split('-').every(function (d) { return DAYS.indexOf(d) >= 0; }); });
  }
  // slotToday is the minute of the day a slot starts today in its zone, or null when it does not play today.
  // slotToday 返回时段今天开始的分钟数（按其时区），今天不播放时为 null。
  function slotToday(at, now) {
    var m = AT.exec(at || '');
    if (!m || !validAt(at)) return null;
    if (now && m[2] && m[2] !== now.date) return null;
    if (now && m[1]) {
      var today = DAYS.indexOf(now.weekday), on = false;
      m[1].toLowerCase().split(',').forEach(function (r) {
        var ab = r.split('-'), a = DAYS.indexOf(ab[0]), b = DAYS.indexOf(ab[1] || ab[0]);
        if (a <= b ? today >= a && today <= b : today >= a || today <= b) on = true;
      });
      if (!on) return null;
    }
    return +m[3] * 60 + +m[4];
  }
  function hhmm(min) {
    var hh = Math.floor(min / 60) % 24, mm = min % 60;
    return (hh < 10 ? '0' : '') + hh + ':' + (mm < 10 ? '0' : '') + mm;
  }
})();
