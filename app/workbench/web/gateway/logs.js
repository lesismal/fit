// The Logs page: the file outputs of fit's own log and of the access log, what each rotates and keeps, the files on
// disk, and a rotation and a cleanup a person runs now. The settings themselves are edited in the forms below, over the
// config's shared draft: time_zone, rotate and the rest of log and access_log, with the same save bar and raw tabs as
// any page (config.js). GET /api/logs reads the outputs and their files; POST /api/logs/rotate and POST /api/logs/cleanup
// rotate one output now and remove what retention says, the second previewing the files first. Needs ui.js,
// workbench.js and config.js.
//
// 日志页面：fit 自身日志与访问日志的文件输出、各自的切割与保留设置、磁盘上的文件，以及由人立即执行的切割与清理。设置本身在
// 下方的表单中编辑，走配置的共享草稿：time_zone、rotate 以及 log 与 access_log 的其余字段，保存栏与原始文本标签页与其他页面
// 相同（config.js）。GET /api/logs 读取各输出及其文件；POST /api/logs/rotate 与 POST /api/logs/cleanup 立即切割一个输出、
// 删除超过保留的文件，后者先预览要删除的文件。依赖 ui.js、workbench.js 与 config.js。
(function () {
  'use strict';

  // [key, en, zh] / [键, 英文, 中文]
  var WORDS = [
    ['m.logs', 'Logs', '日志'],
    ['l.note', 'The files fit\'s own log and the access log write, their rotation and retention, and the workbench\'s Logs page. A file output with no rotate grows without bound, as it did before rotation.',
      'fit 自身日志与访问日志写出的文件、各自的切割与保留，以及 workbench 的日志页面。没有 rotate 的文件输出会像引入切割之前一样无限增长。'],
    ['l.loading', 'Reading the log files…', '正在读取日志文件…'],
    ['l.none', 'No log writes to a file: set log.output or access_log.output in the forms below.', '没有日志写文件：请在下方表单设置 log.output 或 access_log.output。'],
    ['l.failed', 'The log files could not be read: {e}', '无法读取日志文件：{e}'],
    ['l.files', '{n} files, {size}', '{n} 个文件，{size}'], ['l.nofiles', 'No files yet.', '暂无文件。'],
    ['l.name', 'File', '文件'], ['l.size', 'Size', '大小'], ['l.modified', 'Modified', '修改时间'],
    ['l.current', 'current', '当前'],
    ['l.rotate', 'Rotation', '切割'],
    ['l.rotatedAt', 'Rotates at {size}', '达到 {size} 时切割'],
    ['l.every', 'every {d}', '每 {d}'],
    ['l.keepAge', 'keeps {d}', '保留 {d}'],
    ['l.keepCount', 'at most {n} files', '最多 {n} 个文件'],
    ['l.keepTotal', 'at most {size}', '最多 {size}'],
    ['l.gzip', 'gzip', 'gzip'],
    ['l.noRotate', 'No rotate: this file grows without bound. Set rotate in the forms below.', '未设置 rotate：此文件会无限增长。请在下方表单设置 rotate。'],
    ['l.dropped', '{n} lines dropped', '已丢弃 {n} 行'],
    ['l.error', 'The last write failed: {e}', '最近一次写入失败：{e}'],
    ['l.rotateNow', 'Rotate now', '立即切割'], ['l.cleanupNow', 'Clean up now', '立即清理'],
    ['l.rotated', 'Rotated {file}.', '已切割 {file}。'],
    ['l.nothing', 'No rotated file is past its retention.', '没有超过保留的切割文件。'],
    ['l.cleanup.q', 'Remove {n} rotated files ({size})?', '删除 {n} 个切割文件（{size}）？'],
    ['l.cleanup.title', 'Clean up the rotated files', '清理切割文件'],
    ['l.done', 'Removed {n} files ({size}).', '已删除 {n} 个文件（{size}）。'],
    ['l.settings', 'Settings', '设置'],
    ['l.log', 'fit\'s own log', 'fit 自身的日志'],
    ['l.access', 'Access log', '访问日志'],
    ['l.zone', 'time zone {z}', '时区 {z}']
  ];

  // bytes formats a size in bytes. / 以字节格式化大小。
  function bytes(n) {
    if (n < 1024) return (n || 0) + ' B';
    if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KiB';
    if (n < 1024 * 1024 * 1024) return (n / (1024 * 1024)).toFixed(1) + ' MiB';
    return (n / (1024 * 1024 * 1024)).toFixed(2) + ' GiB';
  }

  // duration formats a number of seconds as days, hours or minutes when it divides. / 把秒数格式化为天、小时或分钟。
  function duration(sec) {
    if (!sec) return '';
    if (sec % 86400 === 0) return (sec / 86400) + 'd';
    if (sec % 3600 === 0) return (sec / 3600) + 'h';
    if (sec % 60 === 0) return (sec / 60) + 'm';
    return sec + 's';
  }

  // rotateText says what an output's rotation does, or that it does nothing. / 说明一个输出的切割与保留，或说明没有设置。
  function rotateText(t, rot) {
    if (!rot) return null;
    var out = [];
    if (rot.max_bytes) out.push(t('l.rotatedAt', { size: bytes(rot.max_bytes) }));
    if (rot.interval_seconds) out.push(t('l.every', { d: duration(rot.interval_seconds) }));
    if (rot.max_age_seconds) out.push(t('l.keepAge', { d: duration(rot.max_age_seconds) }));
    if (rot.max_files) out.push(t('l.keepCount', { n: rot.max_files }));
    if (rot.max_total_bytes) out.push(t('l.keepTotal', { size: bytes(rot.max_total_bytes) }));
    if (rot.compress) out.push(t('l.gzip'));
    return out.join(' · ');
  }

  WB.module({
    id: 'sys.logs', server: 'admin', group: 'system', order: 5, icon: 'log', label: 'm.logs', words: WORDS,
    nav: [{ id: 'logs', icon: 'log', label: 'm.logs' }],
    setup: function (UI, sh) {
      var h = UI.h, t = UI.t;

      // outputCard is one file output: its settings, the files on disk, and the two actions. / 一个文件输出：设置、磁盘上的文件
      // 与两个操作。
      function outputCard(out, reload) {
        var files = out.files || [];
        var tools = [];
        var rotateBtn = UI.btn(t('l.rotateNow'), 'refresh', function () {
          UI.busy(rotateBtn, UI.api('POST', '/api/logs/rotate', { path: out.path })).then(function () {
            UI.toast(t('l.rotated', { file: out.path }));
            reload();
          }).catch(UI.fail);
        }, 'ghost sm');
        var cleanBtn = UI.btn(t('l.cleanupNow'), 'trash', function () {
          UI.busy(cleanBtn, UI.api('POST', '/api/logs/cleanup', { path: out.path, dry_run: true })).then(function (r) {
            var removed = (r && r.removed) || [];
            if (!removed.length) { UI.toast(t('l.nothing')); return; }
            var total = removed.reduce(function (n, f) { return n + (f.size || 0); }, 0);
            return UI.confirm(t('l.cleanup.q', { n: removed.length, size: bytes(total) }), t('l.cleanupNow'), true).then(function (ok) {
              if (!ok) return;
              return UI.api('POST', '/api/logs/cleanup', { path: out.path }).then(function (rr) {
                var gone = (rr && rr.removed) || [];
                var size = gone.reduce(function (n, f) { return n + (f.size || 0); }, 0);
                UI.toast(t('l.done', { n: gone.length, size: bytes(size) }));
                reload();
              });
            });
          }).catch(UI.fail);
        }, 'ghost sm');
        tools.push(rotateBtn, cleanBtn);

        var body = h('div.lg-out.stack');
        var head = h('div.lg-head',
          h('code.small.lg-path', out.path), ' ',
          (out.loggers || []).map(function (name) { return UI.badge(name); }),
          ' ', UI.badge(out.format || 'line'), ' ', UI.badge(t('l.zone', { z: out.time_zone || 'local' }), 'info'));
        body.appendChild(head);

        var rot = rotateText(t, out.rotate);
        body.appendChild(rot ? h('div.muted.small', t('l.rotate') + ': ' + rot)
          : h('div.callout.callout--warn', UI.icon('alert'), ' ', t('l.noRotate')));
        var notes = [];
        if (out.dropped) notes.push(t('l.dropped', { n: out.dropped }));
        if (out.last_error) notes.push(t('l.error', { e: out.last_error }));
        if (notes.length) body.appendChild(h('div.callout.callout--danger', notes.join(' · ')));

        var cols = [
          { label: t('l.name'), render: function (f) { return h('span', h('code.small', f.name), f.current ? [' ', UI.badge(t('l.current'), 'ok', true)] : null); } },
          { label: t('l.size'), cls: 'num', render: function (f) { return bytes(f.size); } },
          { label: t('l.modified'), render: function (f) { var at = Date.parse(f.modified); return h('span.muted.small', isNaN(at) ? '' : UI.fmtTime(Math.floor(at / 1000))); } }
        ];
        body.appendChild(UI.table(cols, files, { empty: t('l.nofiles') }));
        body.appendChild(h('div.muted.small', t('l.files', { n: files.length, size: bytes(out.total_bytes || 0) })));
        var title = out.access_log ? t('l.access') : t('l.log');
        return UI.card(title, body, h('div.lg-tools', tools), true);
      }

      // page draws the outputs and the forms of log and access_log. / 绘制各输出以及 log 与 access_log 的表单。
      function page() {
        var C = FitConfig.with(UI);
        var el = sh.page('logs', t('m.logs'));
        C.page(el, sh, function (body) {
          body.appendChild(h('div.callout', UI.icon('info'), ' ', t('l.note')));
          var box = h('div.lg-outputs.stack', h('div.muted', t('l.loading')));
          body.appendChild(box);
          function load() {
            UI.api('GET', '/api/logs').then(function (r) {
              var outs = (r && r.outputs) || [];
              if (!outs.length) {
                UI.mount(box, h('div.muted', t('l.none')));
                return;
              }
              box.innerHTML = '';
              outs.forEach(function (out) { box.appendChild(outputCard(out, load)); });
            }).catch(function (e) {
              UI.mount(box, h('div.callout.callout--danger', t('l.failed', { e: e.message || String(e) })));
            });
          }
          load();
          body.appendChild(h('h3.lg-sec', t('l.settings')));
          body.appendChild(C.section('log'));
          body.appendChild(C.section('access_log'));
        }, { members: ['log', 'access_log'] });
      }

      return {
        routes: {
          'logs': page
        }
      };
    }
  });
})();
