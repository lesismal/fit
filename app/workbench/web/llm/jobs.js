// Jobs: the background jobs of a node (model.Job) — a list and an output follower. Used by the node console (its own
// jobs) and the hub console (a managed node's jobs, through call / stream functions that route to that node).
// Needs ui.js.
//
// Jobs：节点的后台作业（model.Job）—— 列表与输出跟随。节点控制台（自身作业）与 hub 控制台（代管节点的作业，
// 经路由到该节点的 call / stream 函数）共用。依赖 ui.js。
//
// o = {call(method, req) → Promise (default UI.rpc), stream(method, req, onData, signal) → Promise (default UI.rpcStream; null = poll)}
// Jobs.follow(job, o, onDone)  opens a modal following job's output until it ends
// Jobs.mount(el, o) → {refresh}  the job list (click a row to follow it)
// Jobs.badge(status)
(function () {
  'use strict';
  // make(UI) builds it on a module's UI (UI.scope), whose requests go to the module's API; Jobs.with(UI) is
  // make. / make(UI) 基于模块的 UI（UI.scope）构建，其请求发往该模块的 API；Jobs.with(UI) 即 make。
  function make(UI) {    var h = UI.h, t = UI.t;

    UI.addI18n({
      'en': {
        'job.job': 'Job', 'job.jobs': 'Jobs', 'job.kind': 'Kind', 'job.title': 'Title', 'job.status': 'Status', 'job.created': 'Created',
        'job.finished': 'Finished', 'job.output': 'Output', 'job.cancel': 'Cancel job', 'job.none': 'No jobs yet', 'job.exit': 'exit {c}',
        'job.s.running': 'Running', 'job.s.done': 'Done', 'job.s.failed': 'Failed', 'job.s.canceled': 'Canceled'
      },
      'zh-CN': {
        'job.job': '作业', 'job.jobs': '作业', 'job.kind': '类型', 'job.title': '标题', 'job.status': '状态', 'job.created': '创建时间',
        'job.finished': '结束时间', 'job.output': '输出', 'job.cancel': '取消作业', 'job.none': '暂无作业', 'job.exit': '退出码 {c}',
        'job.s.running': '运行中', 'job.s.done': '完成', 'job.s.failed': '失败', 'job.s.canceled': '已取消'
      }
    });

    var KIND = { running: 'warn', done: 'ok', failed: 'danger', canceled: '' };
    function badge(s) { return UI.badge(t('job.s.' + s), KIND[s], s === 'running'); }

    function opts(o) {
      o = o || {};
      return { call: o.call || UI.rpc, stream: o.stream === null ? null : (o.stream || UI.rpcStream) };
    }

    // follow opens a modal that follows a job's output until it ends. With stream it subscribes to /node/job/follow
    // (output arrives as it is written); /node/job/get from the current offset fills the gap after a broken stream
    // before following again, and is the only source without stream (polled every second).
    // follow 打开跟随作业输出直到结束的弹窗。有 stream 时订阅 /node/job/follow（输出随写入到达）；流断开后先用
    // /node/job/get 从当前偏移补齐缺口再重新订阅；没有 stream 时只用 /node/job/get（每秒轮询）。
    function follow(job, o, onDone) {
      o = opts(o);
      var call = o.call, stream = o.stream;
      var pre = h('pre.console', '');
      var status = h('span', badge(job.status));
      var offset = 0, timer = null, stopped = false, ctrl = null;
      var cancelBtn = UI.btn(t('job.cancel'), 'stop', function () {
        UI.busy(cancelBtn, call('/node/job/cancel', { id: job.id })).catch(UI.fail);
      }, 'danger sm');
      // apply shows one job snapshot (output from its offset). / 显示一个作业快照（从其偏移开始的输出）。
      function apply(j) {
        if (j.output) {
          var atBottom = pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 30;
          if (j.offset < offset) pre.textContent = '';
          pre.textContent += j.output;
          if (atBottom) pre.scrollTop = pre.scrollHeight;
        }
        if (j.output_len != null) offset = j.output_len;
        UI.mount(status, badge(j.status), j.exit_code != null ? h('span.muted.small', ' ' + t('job.exit', { c: j.exit_code })) : null,
          j.error ? h('span.small', { style: { color: 'var(--danger)' } }, ' ' + j.error) : null);
        cancelBtn.hidden = j.status !== 'running';
      }
      function ended(j) { if (onDone) onDone(j); }
      function followStream() {
        if (stopped) return;
        ctrl = new AbortController();
        stream('/node/job/follow', { id: job.id, offset: offset }, apply, ctrl.signal).then(function (j) {
          apply(j);
          ended(j);
        }).catch(function (e) {
          if (stopped) return;
          pre.textContent += '\n[' + e.message + ']\n';
          timer = setTimeout(poll, 3000);
        });
      }
      function poll() {
        if (stopped) return;
        call('/node/job/get', { id: job.id, offset: offset }).then(function (j) {
          apply(j);
          if (j.status !== 'running') ended(j);
          else if (stream) followStream();
          else timer = setTimeout(poll, 1000);
        }).catch(function (e) { if (!stopped) timer = setTimeout(poll, 3000); pre.textContent += '\n[' + e.message + ']\n'; });
      }
      UI.modal({
        title: t('job.job') + ' #' + job.id + ' · ' + job.title, wide: true,
        body: h('div.stack', h('div.row.row--between', status, cancelBtn), pre),
        onclose: function () { stopped = true; clearTimeout(timer); if (ctrl) ctrl.abort(); }
      });
      if (stream) followStream(); else poll();
    }

    // mount shows the job list (/node/job/list), refreshed every 4 s while no modal is open.
    // mount 显示作业列表（/node/job/list），没有弹窗时每 4 秒刷新。
    function mount(el, o) {
      o = opts(o);
      var cols = [
        { label: '#', render: function (j) { return j.id; } },
        { label: t('job.title'), render: function (j) { return h('b', j.title); } },
        { label: t('job.kind'), render: function (j) { return UI.badge(j.kind); } },
        { label: t('job.status'), render: function (j) { return h('div.row', badge(j.status), j.exit_code != null ? h('span.small.muted', t('job.exit', { c: j.exit_code })) : null); } },
        { label: t('job.created'), render: function (j) { return h('span.small', UI.fmtTime(j.created_at)); } },
        { label: t('job.output'), cls: 'num', render: function (j) { return h('span.small.muted', j.output_len + ' B'); } }];
      function load() {
        return o.call('/node/job/list', {}).then(function (list) {
          UI.mount(el, UI.card(t('job.jobs'), UI.table(cols, list, { empty: t('job.none'), onRowClick: function (j) { follow(j, o, load); } }),
            UI.btn(t('refresh'), 'refresh', load, 'sm'), true));
        }).catch(UI.fail);
      }
      load();
      UI.every(4000, function () { if (document.body.contains(el) && !document.querySelector('.modal')) load(); });
      return { refresh: load };
    }

    return { follow: follow, mount: mount, badge: badge };
  }
  window.Jobs = make(window.UI);
  window.Jobs.with = make;
})();
