// The Sync pages (I190): one module in the menu for the sync role's pipelines (I163-I189). They read the Admin API's
// /sync endpoints (I188, I189) and edit the config's shared draft:
//
//   pipelines          every pipeline as a table: its state, throughput and lag as sparklines of the last polls, its
//                      records read and acknowledged, its errors and last error, its queue against its bounds and the
//                      dead letters kept; the pipelines of the draft that do not run (not saved yet, or the role off) too;
//                      New pipeline
//   pipeline/:name     a pipeline's overview: its state with Pause, Resume and New snapshot (after a confirm), Test
//                      connection and Dry run of what runs, its counters, queue, source lag, a job's progress and each
//                      sink's records, batches, retries and errors, and its last error (Ask AI: sync-pipeline)
//   pipeline/:name/checkpoint
//                      the checkpoint file: the position shown, set by hand or reset, each after a confirm that says
//                      records may be skipped or written again
//   pipeline/:name/dead_letters
//                      the dead letters a dead_letters sink keeps, paged, picked and replayed or dropped after a confirm
//   edit/:name         the pipeline's editor, made from the schema (each source's, transform's and sink's own fields),
//                      in the shared draft with the UI/.conf/.yaml/.json tabs of every config page; Test connection and
//                      Dry run of the draft as it stands (POST /sync/test and /sync/dry-run), before Save and apply
//   config             the whole sync member: state_dir and every pipeline
//
// A pipeline is so made, tested, dry-run, saved and watched from these pages alone. The records a dry run reads and a
// dead letter holds are shown to the person signed in, never kept. Needs ui.js, workbench.js and config.js.
//
// 同步页面（I190）：菜单中的一个模块，对应 sync 角色的管道（I163-I189）。各页读取 Admin API 的 /sync 接口（I188、I189），
// 并编辑配置的共享草稿：pipelines 以表格列出各管道的状态、吞吐与延迟（最近若干次轮询的迷你走势图）、已读取与已确认的记录数、
// 错误数与最近的错误、队列相对上限的占用与保留的死信；草稿中未运行的管道（尚未保存或角色未启用）也列出；可新建管道。
// pipeline/:name 为管道概览：状态及暂停、恢复、重新快照（需确认），对运行中管道的连接测试与试运行，计数、队列、源端延迟、
// 作业进度、各 sink 的记录、批次、重试与错误，以及最近的错误（问 AI：sync-pipeline）。checkpoint 页显示检查点文件，可手动
// 设置或重置位置，均需确认（可能跳过或重写记录）。dead_letters 页分页列出 dead_letters sink 保存的死信，可选择后重放或丢弃
// （需确认）。edit/:name 为由 schema 生成的管道编辑器（各 source、transform 与 sink 的字段），编辑共享草稿，带 UI/.conf/
// .yaml/.json 标签页；保存前可对草稿进行连接测试与试运行（POST /sync/test 与 /sync/dry-run）。config 为整个 sync 成员。
// 因此仅凭这些页面即可创建、测试、试运行、保存并观察一个管道。试运行读取的记录与死信中的记录只显示给登录者，从不保存。
(function () {
  'use strict';

  var ID = 'sync';
  // POLL is how often the pages read the pipelines' state; HISTORY the polls a sparkline shows.
  // POLL 为页面读取管道状态的间隔；HISTORY 为迷你走势图显示的轮询次数。
  var POLL = 2000, HISTORY = 60, LETTERS = 50;

  // [key, en, zh] / [键, 英文, 中文]
  var WORDS = [
    ['m.sync', 'Sync', '数据同步'], ['tag.sync', 'pipelines', '管道'],
    ['n.pipelines', 'Pipelines', '管道'], ['n.config', 'Sync config', '同步配置'],
    ['n.overview', 'Overview', '概览'], ['n.edit', 'Edit', '编辑'], ['n.checkpoint', 'Checkpoint', '检查点'], ['n.letters', 'Dead letters', '死信'],
    // the list / 列表
    ['p.new', 'New pipeline', '新建管道'], ['p.name', 'Pipeline', '管道'], ['p.state', 'State', '状态'],
    ['p.throughput', 'Throughput', '吞吐'], ['p.lag', 'Lag', '延迟'], ['p.records', 'Read / acknowledged', '已读取 / 已确认'],
    ['p.errors', 'Errors', '错误'], ['p.queue', 'Queue', '队列'], ['p.letters', 'Dead letters', '死信'],
    ['p.none', 'No pipeline yet: make one with New pipeline, then save and apply.', '还没有管道：请点击“新建管道”，然后保存并应用。'],
    ['p.off', 'The sync role does not run: no pipeline is applied. Pipelines of the draft are listed; Save and apply starts them.',
      'sync 角色未运行：没有已应用的管道。下面列出草稿中的管道；“保存并应用”后即启动。'],
    ['p.unsaved', 'not applied', '未应用'],
    ['p.rate', '{n}/s', '{n}/s'], ['p.secs', '{n}s', '{n} 秒'],
    // states / 状态
    ['s.starting', 'starting', '启动中'], ['s.running', 'running', '运行中'], ['s.paused', 'paused', '已暂停'], ['s.retrying', 'retrying', '重试中'],
    ['s.finished', 'finished', '已完成'], ['s.stopped', 'stopped', '已停止'], ['s.scheduled', 'scheduled', '等待调度'], ['s.killed', 'killed', '已终止'],
    // a new pipeline / 新建管道
    ['nw.title', 'New pipeline', '新建管道'], ['nw.name', 'Name', '名称'], ['nw.source', 'Source', '源'], ['nw.sink', 'Sink', '目标（sink）'],
    ['nw.dir', 'State directory (sync.state_dir)', '状态目录（sync.state_dir）'],
    ['nw.dirHint', 'Each pipeline\'s checkpoint is kept here. Set once, for every pipeline.', '各管道的检查点保存在此目录。只需设置一次，所有管道共用。'],
    ['nw.bad', 'A name is letters, digits, \'_\', \'.\' and \'-\', starting with a letter or digit, unique.', '名称由字母、数字、“_”、“.”与“-”组成，以字母或数字开头，且不能重复。'],
    ['nw.make', 'Make it', '创建'],
    ['nw.made', 'Pipeline {name} is in the draft: fill in its fields, test it, then save and apply.', '管道 {name} 已加入草稿：请填写字段并测试，然后保存并应用。'],
    // overview / 概览
    ['o.pause', 'Pause', '暂停'], ['o.resume', 'Resume', '恢复'], ['o.snapshot', 'New snapshot', '重新快照'],
    ['o.snapshot.q', 'Read {name}\'s source again from its start? Its checkpoint is removed and the pipeline restarted: the sinks get every record again, written over what they hold.',
      '从头重新读取 {name} 的源？其检查点将被删除并重启管道：sink 会再次收到所有记录，覆盖已有内容。'],
    ['o.paused', '{name} is paused: its source waits, what it read is still written.', '{name} 已暂停：源等待，已读取的记录仍会写入。'],
    ['o.resumed', '{name} runs again.', '{name} 已恢复运行。'], ['o.snapped', '{name} reads its source again from its start.', '{name} 正从头重新读取源。'],
    ['o.test', 'Test connection', '测试连接'], ['o.dry', 'Dry run', '试运行'],
    ['o.read', 'Read', '已读取'], ['o.acked', 'Acknowledged', '已确认'], ['o.failed', 'Failed', '失败'], ['o.bytes', 'Read bytes', '已读取字节'],
    ['o.lagSource', 'Behind the source', '落后于源端'], ['o.lastCommit', 'Last commit', '最近提交时间'],
    ['o.queueRecords', 'Queue: records', '队列：记录'], ['o.queueBytes', 'Queue: bytes', '队列：字节'],
    ['o.next', 'Starts next', '下次启动'], ['o.lastError', 'Last error', '最近的错误'], ['o.errorAt', 'at {at}', '时间 {at}'],
    ['o.ask', 'Why does the pipeline {name} fail with: {error}', '管道 {name} 为什么失败：{error}'],
    ['o.job', 'Job', '作业'], ['o.jobStarted', 'Started', '开始'], ['o.jobFinished', 'Finished', '完成'], ['o.jobWritten', 'Written', '已写入'],
    ['o.jobChunks', 'Chunks done', '已完成分块'], ['o.jobEta', 'Time left', '剩余时间'],
    ['o.records', 'Records', '记录'], ['o.sinkBytes', 'Bytes', '字节'],
    ['o.sinks', 'Sinks', 'Sink'], ['o.sink', 'Sink', 'Sink'], ['o.batches', 'Batches', '批次'], ['o.retries', 'Retries', '重试'],
    ['o.dl', 'Dead letters', '死信'], ['o.dlWritten', 'Written', '已写入'], ['o.dlNot', 'Not written', '未能写入'], ['o.dlReplayed', 'Replayed', '已重放'],
    ['o.dlKept', 'Kept', '保留'], ['o.notRunning', '{name} does not run: it is not in the running config. Save and apply the draft to start it.',
      '{name} 未运行：它不在运行中的配置里。保存并应用草稿即可启动。'],
    // tests and dry runs / 测试与试运行
    ['t.title', 'Connection test', '连接测试'], ['t.part', 'Part', '部分'], ['t.module', 'Module', '模块'], ['t.result', 'Result', '结果'],
    ['t.took', 'Took', '耗时'], ['t.ok', 'passed', '通过'], ['t.fail', 'failed', '失败'], ['t.untested', 'no test: try a dry run', '无测试：请试运行'], ['t.warn', 'warning', '警告'],
    ['t.allOk', 'Every connection tested passed.', '所有已测试的连接均通过。'], ['t.someFail', 'A connection failed.', '有连接失败。'],
    ['d.title', 'Dry run', '试运行'], ['d.limit', 'Records', '记录数'], ['d.wait', 'Wait', '等待'], ['d.from', 'From', '起点'],
    ['d.fromCk', 'the checkpoint', '检查点'], ['d.fromStart', 'the start', '起始位置'], ['d.run', 'Run', '运行'],
    ['d.note', 'Reads the first records through the transforms; nothing is written and no position saved.', '读取前几条记录并经过各转换；不写入任何内容，也不保存位置。'],
    ['d.in', 'Read', '读取'], ['d.out', 'To the sinks', '交给 sink'], ['d.dropped', 'dropped by the transforms', '被转换丢弃'],
    ['d.ended', 'The source ended.', '源已读完。'], ['d.timedOut', 'The wait ended before the records did.', '等待结束时记录尚未读满。'],
    ['d.sourceErr', 'The source failed: {e}', '源失败：{e}'], ['d.none', 'No record came.', '没有读到记录。'], ['d.count', '{n} records', '{n} 条记录'],
    // checkpoint / 检查点
    ['c.none', 'No checkpoint is saved yet: the pipeline starts as its source\'s params say.', '尚未保存检查点：管道将按其源的参数开始。'],
    ['c.saved', 'Saved', '保存时间'], ['c.lineage', 'Lineage', '谱系'], ['c.position', 'Position', '位置'],
    ['c.set', 'Set position', '设置位置'], ['c.reset', 'Reset', '重置'],
    ['c.set.q', 'Set {name}\'s checkpoint to this position? The pipeline stops, and starts again from it: records before it are skipped, or those after it written again.',
      '将 {name} 的检查点设为此位置？管道将停止并从该位置重新开始：之前的记录会被跳过，或之后的记录会被再次写入。'],
    ['c.reset.q', 'Reset {name}\'s checkpoint? It is removed and the pipeline starts again as its source\'s params say: records may be read and written again.',
      '重置 {name} 的检查点？检查点将被删除，管道按其源的参数重新开始：记录可能被再次读取并写入。'],
    ['c.done', 'The checkpoint of {name} is set.', '{name} 的检查点已设置。'], ['c.wasReset', 'The checkpoint of {name} is reset.', '{name} 的检查点已重置。'],
    ['c.badJSON', 'The position is not JSON: {e}', '位置不是合法的 JSON：{e}'],
    ['c.hint', 'In the form the checkpoint shows it, as the source writes it.', '格式与检查点所示相同，即源所写的格式。'],
    // dead letters / 死信
    ['l.none', 'No dead letter is kept.', '没有保留的死信。'],
    ['l.noStore', 'This pipeline keeps no dead letters to replay: set on_error to dead_letter, with a dead_letter sink of module dead_letters.',
      '此管道不保留可重放的死信：请将 on_error 设为 dead_letter，并配置模块为 dead_letters 的 dead_letter sink。'],
    ['l.id', 'ID', 'ID'], ['l.saved', 'Saved', '保存时间'], ['l.at', 'Where', '位置'], ['l.attempts', 'Attempts', '尝试次数'],
    ['l.error', 'Error', '错误'], ['l.record', 'Record', '记录'], ['l.replay', 'Replay selected', '重放所选'], ['l.replayAll', 'Replay all', '全部重放'],
    ['l.drop', 'Drop selected', '丢弃所选'], ['l.dropAll', 'Drop all', '全部丢弃'], ['l.more', 'More', '更多'],
    ['l.kept', '{kept} kept, {bytes} of {max}', '保留 {kept} 条，{bytes} / {max}'],
    ['l.replay.q', 'Replay {n} dead letters? The pipeline stops, each record is written again as it was (over any later change of its key), and it starts again.',
      '重放 {n} 条死信？管道将停止，每条记录按原样再次写入（覆盖该键之后的任何修改），然后重新启动。'],
    ['l.replayAll.q', 'Replay every dead letter of {name}?', '重放 {name} 的全部死信？'],
    ['l.drop.q', 'Drop {n} dead letters? Their records are lost.', '丢弃 {n} 条死信？其记录将丢失。'],
    ['l.dropAll.q', 'Drop every dead letter of {name}? Their records are lost.', '丢弃 {name} 的全部死信？其记录将丢失。'],
    ['l.replayed', '{n} replayed, {f} kept.', '已重放 {n} 条，保留 {f} 条。'], ['l.dropped', '{n} dropped.', '已丢弃 {n} 条。'],
    ['l.ask', 'Why do the dead letters of {name} fail with: {error}', '管道 {name} 的死信为什么失败：{error}'],
    // the editor / 编辑器
    ['e.missing', 'The draft has no pipeline named {name}.', '草稿中没有名为 {name} 的管道。'],
    ['e.note', 'Edits go into the shared draft; Test connection and Dry run check the draft as it stands, before Save and apply.',
      '修改进入共享草稿；“测试连接”与“试运行”检查当前草稿，无需先保存并应用。'],
    ['e.delete', 'Remove the pipeline', '移除管道'],
    ['e.delete.q', 'Remove {name} from the draft? Save and apply stops it; its checkpoint file stays.', '从草稿中移除 {name}？保存并应用后它将停止；其检查点文件保留。'],
    ['e.list', 'Pipelines', '管道列表']
  ];

  var enc = encodeURIComponent;
  function askOf(spec, o) { return window.FitAI && FitAI.askButton ? FitAI.askButton(spec, o) : null; }
  function pretty(v) { try { return JSON.stringify(v, null, 2); } catch (e) { return String(v); } }
  function fmtBytes(n) {
    n = +n || 0;
    if (n >= 1 << 30) return (n / (1 << 30)).toFixed(1) + ' GiB';
    if (n >= 1 << 20) return (n / (1 << 20)).toFixed(1) + ' MiB';
    if (n >= 1 << 10) return (n / (1 << 10)).toFixed(1) + ' KiB';
    return n + ' B';
  }
  function unix(s) { var v = Date.parse(s || ''); return isNaN(v) || v <= 0 ? 0 : v / 1000; }
  var NAME = /^[A-Za-z0-9][A-Za-z0-9_.-]*$/;
  function stateKind(st) {
    if (!st) return '';
    if (st.paused) return 'warn';
    return { running: 'ok', finished: 'ok', scheduled: 'info', starting: 'info', retrying: 'warn', stopped: 'danger', killed: 'danger' }[st.state] || '';
  }

  // History of each pipeline's throughput and lag, by poll, kept while the workbench is open: what the sparklines draw.
  // 各管道吞吐与延迟的历史（按轮询记录，工作台打开期间保留），即迷你走势图所绘内容。
  var hist = {};
  function remember(list) {
    (list || []).forEach(function (p) {
      var x = hist[p.name] || (hist[p.name] = { tp: [], lag: [] });
      x.tp.push(+p.throughput || 0);
      x.lag.push(+p.lag_seconds || 0);
      if (x.tp.length > HISTORY) { x.tp.shift(); x.lag.shift(); }
    });
  }
  var SVG = 'http://www.w3.org/2000/svg';
  // sparkline draws vs as a small line, w wide. / 将 vs 绘制为宽 w 的迷你折线。
  function sparkline(vs, label, w) {
    var W = w || 96, H = 22, max = 0, n = vs.length;
    vs.forEach(function (v) { if (v > max) max = v; });
    max = max > 0 ? max : 1;
    var d = vs.map(function (v, i) {
      var x = n > 1 ? i / (n - 1) * (W - 2) + 1 : W / 2, y = 1 + (1 - v / max) * (H - 2);
      return (i ? 'L' : 'M') + x.toFixed(1) + ' ' + y.toFixed(1);
    }).join(' ');
    var svg = document.createElementNS(SVG, 'svg');
    svg.setAttribute('viewBox', '0 0 ' + W + ' ' + H); svg.setAttribute('width', W); svg.setAttribute('height', H);
    svg.setAttribute('class', 'sy-spark'); svg.setAttribute('role', 'img'); svg.setAttribute('aria-label', label || '');
    if (d) {
      var path = document.createElementNS(SVG, 'path');
      path.setAttribute('d', d); path.setAttribute('fill', 'none'); path.setAttribute('stroke', 'currentColor'); path.setAttribute('stroke-width', '1.5');
      svg.appendChild(path);
    }
    var title = document.createElementNS(SVG, 'title'); title.textContent = label || ''; svg.appendChild(title);
    return svg;
  }

  // ---- places of a save's problems / 保存问题的位置 ----
  FitConfig.with(UI.scope('admin', ID)).place(function (path, d) {
    var m = /^sync(?:\.pipelines\[(\d+)\])?/.exec(path || '');
    if (!m) return null;
    var p = m[1] != null && d && d.sync && (d.sync.pipelines || [])[+m[1]];
    return p && p.name ? '#/' + ID + '/edit/' + enc(p.name) : '#/' + ID + '/config';
  });

  WB.module({
    id: ID, server: 'admin', group: 'gateway', order: 13, icon: 'swap', label: 'm.sync', tag: 'tag.sync', words: WORDS,
    nav: [{ id: 'pipelines', icon: 'swap', label: 'n.pipelines' }, { id: 'config', icon: 'settings', label: 'n.config' }],
    setup: function (ui, sh) {
      var h = ui.h, t = ui.t;
      var C = FitConfig.with(ui);
      function api(method, path, body) { return ui.api(method, '/api' + path, body); }
      function fail(el, e) { UI.mount(el, h('div.callout.callout--danger', UI.icon('alert'), ' ', (e && e.message) || String(e))); }
      function stateBadge(st) {
        if (!st) return UI.badge(t('p.unsaved'), '', true);
        var word = st.paused ? t('s.paused') : t('s.' + st.state);
        return UI.badge(word === 's.' + st.state ? st.state : word, stateKind(st), true);
      }
      // fmtLag words a lag in seconds: to the hundredth under 100s, in days, hours and minutes beyond.
      // fmtLag 表示以秒计的延迟：100 秒以内精确到 0.01 秒，超过则以天、小时与分钟表示。
      function fmtLag(sec) { return !sec ? '—' : sec < 100 ? t('p.secs', { n: (+sec).toFixed(2) }) : UI.fmtDur(sec); }
      function draftPipelines() { return (C.draft && C.draft.sync && C.draft.sync.pipelines) || []; }
      function pipePath(name) { return '/sync/pipelines/' + enc(name); }
      // tabsOf links a pipeline's pages. / 管道各页面之间的链接。
      function tabsOf(name, active) {
        var base = '#/' + ID + '/';
        return h('div.tabs', [['overview', base + 'pipeline/' + enc(name)], ['edit', base + 'edit/' + enc(name)],
          ['checkpoint', base + 'pipeline/' + enc(name) + '/checkpoint'], ['letters', base + 'pipeline/' + enc(name) + '/dead_letters']].map(function (x) {
          return h('a.tab' + (x[0] === active ? '.is-active' : ''), { href: x[1] }, t('n.' + x[0]));
        }));
      }

      // ---- the pipelines / 管道列表 ----
      function listPage() {
        var el = sh.page('pipelines', t('n.pipelines'));
        sh.tools.appendChild(UI.btn(t('p.new'), 'plus', newPipeline, 'primary'));
        var box = h('div.stack');
        UI.mount(el, box);
        box.appendChild(h('div', UI.icon('refresh'), ' ', t('loading')));
        function load() {
          return Promise.all([api('GET', '/sync/pipelines').catch(function (e) { if (e.status === 404) return null; throw e; }), C.ensure().catch(function () { return null; })]).then(function (r) {
            if (!document.body.contains(box)) return;
            var running = r[0] && r[0].pipelines;
            remember(running);
            drawList(box, running);
          }).catch(function (e) { if (document.body.contains(box)) fail(box, e); });
        }
        load();
        UI.every(POLL, load);
      }
      function drawList(box, running) {
        var byName = {}, rows = [];
        (running || []).forEach(function (p) { byName[p.name] = p; rows.push({ name: p.name, st: p }); });
        draftPipelines().forEach(function (p) { if (p && p.name && !byName[p.name]) rows.push({ name: p.name, st: null }); });
        var parts = [];
        if (!running) parts.push(h('div.callout', UI.icon('info'), ' ', t('p.off')));
        if (!rows.length) {
          parts.push(UI.empty(t('p.none'), 'swap'));
          UI.mount(box, parts);
          return;
        }
        parts.push(UI.card(null, UI.table([
          { label: t('p.name'), render: function (r) { return h('a.sy-name', { href: '#/' + ID + '/pipeline/' + enc(r.name) }, h('b', r.name)); } },
          { label: t('p.state'), render: function (r) { return stateBadge(r.st); } },
          { label: t('p.throughput'), render: function (r) {
            if (!r.st) return '—';
            var x = hist[r.name] || { tp: [] };
            return h('span.sy-trend', sparkline(x.tp, t('p.throughput')), h('span.small', t('p.rate', { n: (+r.st.throughput || 0).toFixed(1) })));
          } },
          { label: t('p.lag'), render: function (r) {
            if (!r.st) return '—';
            var x = hist[r.name] || { lag: [] }, sl = r.st.source_lag;
            return h('span.sy-trend', sparkline(x.lag, t('p.lag')), h('span.small', fmtLag(r.st.lag_seconds)),
              sl ? h('span.muted.small', ' · ' + sl.behind + ' ' + sl.unit) : null);
          } },
          { label: t('p.records'), render: function (r) { return r.st ? h('span.small', (r.st.read || 0) + ' / ' + (r.st.acknowledged || 0)) : '—'; } },
          { label: t('p.errors'), render: function (r) {
            if (!r.st) return '—';
            return h('div', h('span' + (r.st.errors ? '.sy-bad' : ''), String(r.st.errors || 0)),
              r.st.error ? h('div.small.muted.sy-clip', { title: r.st.error }, r.st.error) : null);
          } },
          { label: t('p.queue'), render: function (r) {
            var q = r.st && r.st.queue;
            if (!q || !q.max_records) return '—';
            return h('div.sy-meter', UI.meter('', q.records, q.max_records, q.records + ' / ' + q.max_records));
          } },
          { label: t('p.letters'), render: function (r) {
            var s = r.st && r.st.dead_letters && r.st.dead_letters.store;
            return s ? h('a', { href: '#/' + ID + '/pipeline/' + enc(r.name) + '/dead_letters' }, String(s.kept)) : '—';
          } }
        ], rows, { onRowClick: function (r) { location.hash = '#/' + ID + '/pipeline/' + enc(r.name); } }), null, true));
        UI.mount(box, parts);
      }

      // ---- a new pipeline / 新建管道 ----
      // moduleNames are the modules of the namespace ns (sync.sources, sync.sinks), as the schema lists them.
      // moduleNames 为命名空间 ns（sync.sources、sync.sinks）的模块，取自 schema。
      function moduleNames(ns) {
        var d = C.schemaDef(ns), m = d && d.properties && d.properties.module;
        return ((m && m['enum']) || []).slice().sort();
      }
      function newPipeline() {
        C.ensure().then(function () {
          var sources = moduleNames('sync.sources'), sinks = moduleNames('sync.sinks').filter(function (m) { return m !== 'dead_letters'; });
          var taken = {};
          draftPipelines().forEach(function (p) { if (p && p.name) taken[p.name] = true; });
          var n = 1;
          while (taken['pipeline-' + n]) n++;
          var name = h('input.input', { value: 'pipeline-' + n });
          var src = h('select.select', sources.map(function (m) { return h('option', { value: m, selected: m === 'generate' }, m); }));
          var snk = h('select.select', sinks.map(function (m) { return h('option', { value: m, selected: m === 'memory' }, m); }));
          var hasSync = !!(C.draft && C.draft.sync);
          var dir = hasSync ? null : UI.fillable(h('input.input', { placeholder: '/var/lib/fit/sync' }));
          var err = h('div.callout.callout--danger', { hidden: true });
          UI.modal({
            title: t('nw.title'),
            body: h('div.form', UI.field(t('nw.name'), name), UI.field(t('nw.source'), src), UI.field(t('nw.sink'), snk),
              dir ? UI.field(t('nw.dir'), dir, t('nw.dirHint')) : null, err),
            actions: [{ label: t('cancel') }, { label: t('nw.make'), kind: 'primary', onclick: function () {
              var v = name.value.trim();
              if (!NAME.test(v) || taken[v]) { err.textContent = t('nw.bad'); err.hidden = false; return false; }
              if (!C.draft.sync) C.draft.sync = { state_dir: (dir && dir.value.trim()) || '/var/lib/fit/sync', pipelines: [] };
              if (!C.draft.sync.pipelines) C.draft.sync.pipelines = [];
              C.draft.sync.pipelines.push({ name: v, source: { module: src.value }, sinks: [{ name: snk.value, sink: { module: snk.value } }] });
              C.changed();
              UI.toast(t('nw.made', { name: v }));
              location.hash = '#/' + ID + '/edit/' + enc(v);
            } }]
          });
        }).catch(UI.fail);
      }

      // ---- a pipeline's overview / 管道概览 ----
      function pipelinePage(p) {
        var name = p.name;
        var el = sh.page('pipelines', name, t('m.sync') + ' / ' + t('n.pipelines') + ' / ' + name);
        var head = h('div'), body = h('div.stack');
        UI.mount(el, tabsOf(name, 'overview'), head, body);
        var tools = h('div.btn-row');
        sh.tools.appendChild(tools);
        var results = h('div.stack');
        function load() {
          return api('GET', pipePath(name)).then(function (st) {
            if (!document.body.contains(body)) return;
            remember([st]);
            drawTools(tools, st, load, results);
            drawOverview(body, st, results);
          }).catch(function (e) {
            if (!document.body.contains(body)) return;
            if (e.status === 404) {
              UI.mount(body, h('div.callout', UI.icon('info'), ' ', t('o.notRunning', { name: name })),
                h('div.btn-row', h('a.btn', { href: '#/' + ID + '/edit/' + enc(name) }, UI.icon('edit'), t('n.edit'))));
              return;
            }
            fail(body, e);
          });
        }
        load();
        UI.every(POLL, load);
      }
      // drawTools draws the pipeline's buttons, again only when Pause turns into Resume or back, so that a poll does not
      // replace a button being clicked. / 绘制管道的按钮；仅当“暂停”与“恢复”互换时才重绘，以免轮询替换正在点击的按钮。
      function drawTools(tools, st, reload, results) {
        var name = st.name, resume = !!(st.paused || st.state === 'stopped' || st.state === 'killed');
        if (tools.getAttribute('data-resume') === String(resume)) return;
        tools.setAttribute('data-resume', String(resume));
        function act(b, body, msg) {
          return UI.busy(b, api('POST', pipePath(name), body)).then(function () { UI.toast(t(msg, { name: name })); reload(); }).catch(UI.fail);
        }
        var pause = resume ?
          UI.btn(t('o.resume'), 'play', function () { act(pause, { action: 'resume' }, 'o.resumed'); }) :
          UI.btn(t('o.pause'), 'stop', function () { act(pause, { action: 'pause' }, 'o.paused'); });
        var snap = UI.btn(t('o.snapshot'), 'refresh', function () {
          UI.confirm(t('o.snapshot.q', { name: name }), t('o.snapshot'), true).then(function (ok) {
            if (ok) act(snap, { action: 'snapshot', confirm: true }, 'o.snapped');
          });
        });
        var test = UI.btn(t('o.test'), 'plug', function () {
          UI.busy(test, api('POST', pipePath(name) + '/test')).then(function (r) { UI.mount(results, testCard(r), results.querySelector('.sy-dry')); }).catch(UI.fail);
        });
        var dry = UI.btn(t('o.dry'), 'play', function () { dryRunCard(results, function (o) { return api('POST', pipePath(name) + '/dry-run', o); }, true); });
        UI.mount(tools, pause, snap, test, dry);
      }
      function drawOverview(body, st, results) {
        var x = hist[st.name] || { tp: [], lag: [] }, parts = [];
        parts.push(h('div.row', stateBadge(st), st.next ? h('span.small.muted', t('o.next') + ': ' + UI.fmtTime(unix(st.next))) : null));
        if (st.error) {
          parts.push(h('div.callout.callout--danger', h('div.ai-askrow', h('b', t('o.lastError')),
            st.error_at ? h('span.small.muted', ' ' + t('o.errorAt', { at: UI.fmtTime(unix(st.error_at)) })) : null,
            askOf({ skill: 'sync-pipeline', message: t('o.ask', { name: st.name, error: st.error }), inputs: { pipeline: st.name, error: st.error },
              selection: { pipeline: st.name, state: st.state, error: st.error, errors: st.errors } })),
            h('div.sy-error', st.error)));
        }
        parts.push(h('div.sy-stats',
          UI.stat('activity', t('p.throughput'), t('p.rate', { n: (+st.throughput || 0).toFixed(1) }), sparkline(x.tp, t('p.throughput'), 140)),
          UI.stat('history', t('p.lag'), fmtLag(st.lag_seconds), sparkline(x.lag, t('p.lag'), 140)),
          UI.stat('download', t('o.read'), String(st.read || 0), t('o.bytes') + ': ' + fmtBytes(st.read_bytes)),
          UI.stat('check', t('o.acked'), String(st.acknowledged || 0), st.last_commit ? t('o.lastCommit') + ': ' + UI.fmtTime(unix(st.last_commit)) : null, 'ok'),
          UI.stat('alert', t('p.errors'), String(st.errors || 0), t('o.failed') + ': ' + (st.failed || 0), st.errors ? 'warn' : ''),
          st.source_lag ? UI.stat('layers', t('o.lagSource'), st.source_lag.behind + ' ' + st.source_lag.unit, null, 'info') : null));
        var q = st.queue || {};
        parts.push(UI.card(t('p.queue'), h('div.grid.grid--2',
          UI.meter(t('o.queueRecords'), q.records || 0, q.max_records || 0, (q.records || 0) + ' / ' + (q.max_records || 0)),
          UI.meter(t('o.queueBytes'), q.bytes || 0, q.max_bytes || 0, fmtBytes(q.bytes) + ' / ' + fmtBytes(q.max_bytes)))));
        if (st.job) {
          var j = st.job;
          parts.push(UI.card(t('o.job'), UI.kv([[t('o.jobStarted'), UI.fmtTime(unix(j.started))], [t('o.jobFinished'), j.finished ? UI.fmtTime(unix(j.finished)) : '—'],
            [t('o.read'), String(j.read || 0)], [t('o.jobWritten'), String(j.written || 0)], [t('o.failed'), String(j.failed || 0)],
            j.chunks ? [t('o.jobChunks'), (j.chunks_done || 0) + ' / ' + j.chunks.length] : null, j.eta ? [t('o.jobEta'), j.eta] : null])));
        }
        parts.push(UI.card(t('o.sinks'), UI.table([
          { label: t('o.sink'), render: function (s) { return h('b', s.name); } },
          { label: t('o.records'), render: function (s) { return String(s.records || 0); } },
          { label: t('o.sinkBytes'), render: function (s) { return fmtBytes(s.bytes); } },
          { label: t('o.batches'), render: function (s) { return String(s.batches || 0); } },
          { label: t('o.retries'), render: function (s) { return String(s.retries || 0); } },
          { label: t('p.errors'), render: function (s) { return h('span' + (s.errors ? '.sy-bad' : ''), String(s.errors || 0)); } },
          { label: t('o.failed'), render: function (s) { return String(s.failed || 0); } }
        ], st.sinks || []), null, true));
        var dl = st.dead_letters;
        if (dl) {
          var store = dl.store;
          parts.push(UI.card(t('o.dl'), UI.kv([[t('o.dlWritten'), String(dl.written || 0)], [t('o.dlNot'), String(dl.not_written || 0)],
            [t('o.dlReplayed'), String(dl.replayed || 0)],
            store ? [t('o.dlKept'), h('a', { href: '#/' + ID + '/pipeline/' + enc(st.name) + '/dead_letters' },
              t('l.kept', { kept: store.kept, bytes: fmtBytes(store.bytes), max: fmtBytes(store.max_bytes) }))] : null])));
        }
        parts.push(results);
        UI.mount(body, parts);
      }

      // testCard shows a connection test's results. / 显示连接测试的结果。
      function testCard(r) {
        var list = (r && r.results) || [];
        return UI.card(t('t.title'), h('div.stack',
          h('div.callout' + (r && r.ok ? '.callout--ok' : '.callout--danger'), UI.icon(r && r.ok ? 'check' : 'alert'), ' ', t(r && r.ok ? 't.allOk' : 't.someFail')),
          UI.table([
            { label: t('t.part'), render: function (x) { return h('b', x.part); } },
            { label: t('t.module'), render: function (x) { return h('code.small', x.module); } },
            { label: t('t.result'), render: function (x) {
              if (!x.tested) return UI.badge(t('t.untested'), 'info');
              // A passed test's warnings: what works but loses data. / 通过的测试的警告：能用但会丢数据之处。
              return h('div', UI.badge(x.ok ? t('t.ok') : t('t.fail'), x.ok ? 'ok' : 'danger', true), x.error ? h('div.small.sy-error', x.error) : null,
                (x.warnings || []).map(function (w) { return h('div.small', UI.badge(t('t.warn'), 'warn'), ' ', w); }));
            } },
            { label: t('t.took'), render: function (x) { return (+x.took_ms || 0).toFixed(0) + ' ms'; } }
          ], list)));
      }
      // dryRunCard puts a dry run's form in box, whose Run calls run(options); withFrom offers the checkpoint as start.
      // 在 box 中放入试运行表单，“运行”调用 run(options)；withFrom 时可选从检查点开始。
      function dryRunCard(box, run, withFrom) {
        var old = box.querySelector('.sy-dry');
        if (old) old.remove();
        var limit = h('input.input', { type: 'number', min: 1, max: 1000, value: 10, style: { width: '90px' } });
        var wait = h('input.input', { value: '5s', style: { width: '80px' } });
        var from = withFrom ? h('select.select', h('option', { value: 'checkpoint' }, t('d.fromCk')), h('option', { value: 'start' }, t('d.fromStart'))) : null;
        var out = h('div.stack');
        var go = UI.btn(t('d.run'), 'play', function () {
          var o = { limit: +limit.value || 10, timeout: wait.value.trim() || '5s' };
          if (from) o.from = from.value;
          UI.busy(go, run(o)).then(function (d) { UI.mount(out, dryResult(d)); }).catch(function (e) { fail(out, e); });
        }, 'primary');
        var card = UI.card(t('d.title'), h('div.stack', h('div.muted.small', t('d.note')),
          h('div.row.sy-form', UI.field(t('d.limit'), limit), UI.field(t('d.wait'), wait), from ? UI.field(t('d.from'), from) : null, go), out));
        card.classList.add('sy-dry');
        box.appendChild(card);
        card.scrollIntoView({ block: 'nearest' });
      }
      function dryResult(d) {
        var recs = (d && d.records) || [], parts = [];
        if (d && d.source_error) parts.push(h('div.callout.callout--danger', UI.icon('alert'), ' ', t('d.sourceErr', { e: d.source_error })));
        if (d && d.ended) parts.push(h('div.callout', UI.icon('info'), ' ', t('d.ended')));
        if (d && d.timed_out) parts.push(h('div.callout', UI.icon('info'), ' ', t('d.timedOut')));
        if (!recs.length) { parts.push(UI.empty(t('d.none'), 'info')); return parts; }
        parts.push(h('div.small.muted', t('d.count', { n: recs.length })));
        recs.forEach(function (r, i) {
          parts.push(h('div.sy-rec', h('div.sy-rec__col', h('div.small.muted', '#' + (i + 1) + ' · ' + t('d.in')), h('pre.cf-pre.sy-pre', pretty(r['in']))),
            h('div.sy-rec__col', h('div.small.muted', t('d.out')),
              r.error ? h('div.callout.callout--danger.small', r.error) :
                !(r.out || []).length ? h('div.small.muted', t('d.dropped')) :
                  (r.out || []).map(function (o) { return h('pre.cf-pre.sy-pre', pretty(o)); }))));
        });
        return parts;
      }

      // ---- the checkpoint / 检查点 ----
      function checkpointPage(p) {
        var name = p.name;
        var el = sh.page('pipelines', name, t('m.sync') + ' / ' + name + ' / ' + t('n.checkpoint'));
        var body = h('div.stack');
        UI.mount(el, tabsOf(name, 'checkpoint'), body);
        function load() {
          body.appendChild(h('div', UI.icon('refresh'), ' ', t('loading')));
          api('GET', pipePath(name) + '/checkpoint').then(function (r) { drawCheckpoint(body, name, r && r.checkpoint, load); }).catch(function (e) { fail(body, e); });
        }
        // The checkpoint moves while the pipeline runs: read again on a click, not under the person's edit.
        // 管道运行时检查点会移动：点击时重新读取，而不是在编辑过程中自动刷新。
        sh.tools.appendChild(UI.btn(t('refresh'), 'refresh', function () { UI.clear(body); load(); }));
        load();
      }
      function drawCheckpoint(body, name, ck, reload) {
        var text = h('textarea.textarea.textarea--code', { rows: 6, spellcheck: false }, ck && ck.position ? pretty(ck.position) : '');
        var set = UI.btn(t('c.set'), 'save', function () {
          var pos;
          try { pos = JSON.parse(text.value); } catch (e) { UI.toast(t('c.badJSON', { e: e.message }), 'error'); return; }
          UI.confirm(t('c.set.q', { name: name }), t('c.set'), true).then(function (ok) {
            if (!ok) return;
            UI.busy(set, api('PUT', pipePath(name) + '/checkpoint', { position: pos, confirm: true })).then(function () {
              UI.toast(t('c.done', { name: name })); UI.clear(body); reload();
            }).catch(UI.fail);
          });
        });
        var reset = UI.btn(t('c.reset'), 'trash', function () {
          UI.confirm(t('c.reset.q', { name: name }), t('c.reset'), true).then(function (ok) {
            if (!ok) return;
            UI.busy(reset, api('DELETE', pipePath(name) + '/checkpoint?confirm=1')).then(function () {
              UI.toast(t('c.wasReset', { name: name })); UI.clear(body); reload();
            }).catch(UI.fail);
          });
        }, 'danger');
        var parts = [];
        if (!ck) parts.push(h('div.callout', UI.icon('info'), ' ', t('c.none')));
        else {
          parts.push(UI.card(t('n.checkpoint'), UI.kv([[t('c.saved'), UI.fmtTime(unix(ck.saved))], ck.lineage ? [t('c.lineage'), h('code.small', ck.lineage)] : null,
            ck.job ? [t('o.job'), h('pre.cf-pre.sy-pre', pretty(ck.job))] : null])));
        }
        parts.push(UI.card(t('c.position'), h('div.stack', UI.field(t('c.position'), text, t('c.hint')), h('div.btn-row', set, ck ? reset : null))));
        UI.mount(body, parts);
      }

      // ---- dead letters / 死信 ----
      function lettersPage(p) {
        var name = p.name;
        var el = sh.page('pipelines', name, t('m.sync') + ' / ' + name + ' / ' + t('n.letters'));
        var body = h('div.stack');
        UI.mount(el, tabsOf(name, 'letters'), body);
        var state = { letters: [], more: false, store: null, picked: {} };
        function load(after) {
          return api('GET', pipePath(name) + '/dead-letters?limit=' + LETTERS + (after ? '&after=' + after : '')).then(function (r) {
            if (!after) { state.letters = []; state.picked = {}; }
            state.letters = state.letters.concat((r && r.letters) || []);
            state.more = !!(r && r.more); state.store = r && r.store;
            drawLetters(body, name, state, load);
          }).catch(function (e) {
            if (e.status === 404 && !/no pipeline/.test(e.message || '')) { UI.mount(body, h('div.callout', UI.icon('info'), ' ', t('l.noStore'))); return; }
            fail(body, e);
          });
        }
        body.appendChild(h('div', UI.icon('refresh'), ' ', t('loading')));
        load(0);
      }
      function drawLetters(body, name, state, load) {
        var picked = function () { return Object.keys(state.picked).filter(function (k) { return state.picked[k]; }).map(Number); };
        function replay(ids) {
          var n = ids ? ids.length : (state.store && state.store.kept) || 0;
          var q = ids ? t('l.replay.q', { n: n }) : t('l.replayAll.q', { name: name }) + ' ' + t('l.replay.q', { n: n });
          UI.confirm(q, ids ? t('l.replay') : t('l.replayAll')).then(function (ok) {
            if (!ok) return;
            api('POST', pipePath(name) + '/dead-letters/replay', ids ? { ids: ids } : { all: true }).then(function (r) {
              var failed = (r && r.failed) || [];
              UI.toast(t('l.replayed', { n: ((r && r.replayed) || []).length, f: failed.length }) + (failed.length ? ' ' + failed[0].error : ''), failed.length ? 'error' : '');
              load(0);
            }).catch(UI.fail);
          });
        }
        function drop(ids) {
          var q = ids ? t('l.drop.q', { n: ids.length }) : t('l.dropAll.q', { name: name });
          UI.confirm(q, ids ? t('l.drop') : t('l.dropAll'), true).then(function (ok) {
            if (!ok) return;
            var qs = ids ? ids.map(function (id) { return 'id=' + id; }).join('&') : 'all=1';
            api('DELETE', pipePath(name) + '/dead-letters?' + qs).then(function (r) { UI.toast(t('l.dropped', { n: (r && r.dropped) || 0 })); load(0); }).catch(UI.fail);
          });
        }
        var parts = [];
        var s = state.store;
        if (s) {
          parts.push(h('div.row.row--between', h('span.muted.small', t('l.kept', { kept: s.kept, bytes: fmtBytes(s.bytes), max: fmtBytes(s.max_bytes) }), ' · ', h('code.small', s.path)),
            h('div.btn-row',
              UI.btn(t('l.replay'), 'play', function () { var ids = picked(); if (ids.length) replay(ids); }),
              UI.btn(t('l.replayAll'), 'play', function () { replay(null); }, 'primary'),
              UI.btn(t('l.drop'), 'trash', function () { var ids = picked(); if (ids.length) drop(ids); }),
              UI.btn(t('l.dropAll'), 'trash', function () { drop(null); }, 'danger'))));
        }
        if (!state.letters.length) { parts.push(UI.empty(t('l.none'), 'check')); UI.mount(body, parts); return; }
        var first = state.letters.filter(function (l) { return l.error; })[0];
        if (first) {
          parts.push(h('div.ai-askrow', askOf({ skill: 'sync-pipeline', message: t('l.ask', { name: name, error: first.error }),
            inputs: { pipeline: name, error: first.error }, selection: { pipeline: name, dead_letter: { id: first.id, at: first.at, error: first.error, attempts: first.attempts } } })));
        }
        parts.push(UI.card(null, UI.table([
          { label: '', render: function (l) {
            return h('input', { type: 'checkbox', checked: !!state.picked[l.id], 'aria-label': String(l.id), onchange: function (e) { state.picked[l.id] = e.target.checked; } });
          } },
          { label: t('l.id'), render: function (l) { return h('code.small', String(l.id)); } },
          { label: t('l.saved'), render: function (l) { return h('span.small', UI.fmtTime(unix(l.saved))); } },
          { label: t('l.at'), render: function (l) { return l.at ? h('code.small', l.at) : '—'; } },
          { label: t('l.attempts'), render: function (l) { return String(l.attempts || 0); } },
          { label: t('l.error'), render: function (l) { return l.error ? h('span.small.sy-error', l.error) : '—'; } },
          { label: t('l.record'), render: function (l) {
            return h('details', h('summary.small', (l.record && l.record.namespace) || t('l.record')), h('pre.cf-pre.sy-pre', pretty(l.record)));
          } }
        ], state.letters), null, true));
        if (state.more) parts.push(h('div.btn-row', UI.btn(t('l.more'), 'download', function () { load(state.letters[state.letters.length - 1].id); })));
        UI.mount(body, parts);
      }

      // ---- the editor / 编辑器 ----
      // The pipeline edited, by its index in the draft, found by name when the page opens: a rename keeps the page.
      // 正在编辑的管道（以其在草稿中的下标记录，页面打开时按名称查找）：改名后页面不变。
      var editing = { name: '', idx: -1 };
      function editPage(p) {
        var el = sh.page('pipelines', p.name, t('m.sync') + ' / ' + p.name + ' / ' + t('n.edit'));
        editing = { name: p.name, idx: -1 };
        return C.page(el, sh, function (body) {
          var list = draftPipelines();
          if (editing.idx < 0 || !list[editing.idx]) {
            editing.idx = -1;
            list.forEach(function (x, i) { if (x && x.name === editing.name) editing.idx = i; });
          }
          var i = editing.idx, cur = list[i];
          if (!cur) {
            body.appendChild(h('div.callout.callout--warn', UI.icon('alert'), ' ', t('e.missing', { name: editing.name })));
            body.appendChild(h('div.btn-row', h('a.btn', { href: '#/' + ID + '/pipelines' }, UI.icon('swap'), t('e.list'))));
            return;
          }
          body.appendChild(tabsOf(cur.name || editing.name, 'edit'));
          body.appendChild(h('div.callout', UI.icon('info'), ' ', t('e.note')));
          body.appendChild(C.subsection('sync.pipelines[' + i + ']'));
          var results = h('div.stack');
          var test = UI.btn(t('o.test'), 'plug', function () {
            UI.busy(test, api('POST', '/sync/test', { pipeline: draftPipelines()[i] })).then(function (r) {
              var dry = results.querySelector('.sy-dry');
              UI.mount(results, testCard(r), dry);
            }).catch(function (e) { fail(results, e); });
          }, 'primary');
          var dry = UI.btn(t('o.dry'), 'play', function () {
            dryRunCard(results, function (o) { o.pipeline = draftPipelines()[i]; return api('POST', '/sync/dry-run', o); }, false);
          });
          var del = UI.btn(t('e.delete'), 'trash', function () {
            var nm = (draftPipelines()[i] || {}).name || editing.name;
            UI.confirm(t('e.delete.q', { name: nm }), t('e.delete'), true).then(function (ok) {
              if (!ok) return;
              C.draft.sync.pipelines.splice(i, 1);
              C.changed();
              location.hash = '#/' + ID + '/pipelines';
            });
          }, 'danger');
          body.appendChild(h('div.btn-row', test, dry, del));
          body.appendChild(results);
        }, { members: ['sync'] });
      }
      function configPage() {
        var el = sh.page('config', t('n.config'));
        return C.page(el, sh, function (body) { body.appendChild(C.section('sync')); }, { members: ['sync'] });
      }

      return {
        routes: {
          'pipelines': listPage, 'pipeline/:name': pipelinePage, 'pipeline/:name/checkpoint': checkpointPage,
          'pipeline/:name/dead_letters': lettersPage, 'edit/:name': editPage, 'config': configPage
        },
        fallback: 'pipelines'
      };
    }
  });
})();
