// The consistency check: what fit runs compared with the config it runs from, read from the Admin API's
// GET /config/check. Every difference is shown under its category. A drift is actionable: the server suggests one or
// more fixes, and the user confirms one fix per difference with a select and ticks the ones to apply; the selected
// fixes are applied together through POST /config/reconcile with the check's ETag, and the check runs again. An info
// item is a known runtime fact, not an inconsistency, and has no fix. Needs ui.js and workbench.js.
//
// 一致性检查：运行中的 fit 与其运行所依据的配置的比较，来自 Admin API 的 GET /config/check。每处差异按类别显示。
// “不一致”项可以处理：服务端给出一种或多种修复，用户用下拉框为每处差异确认一种修复，并勾选要应用的项；所选修复
// 通过 POST /config/reconcile（带本次检查的 ETag）一起应用，然后重新检查。“说明”项是已知的运行事实，不是不一致，
// 没有修复。依赖 ui.js 与 workbench.js。
(function () {
  'use strict';

  // [key, en, zh] / [键, 英文, 中文]
  var WORDS = [
    ['mod.label', 'Consistency', '一致性检查'],
    ['check.again', 'Check again', '重新检查'],
    ['checked.at', 'Checked at {time}', '检查于 {time}'],
    ['version', 'Config version {v}', '配置版本 {v}'],
    ['loading.check', 'Comparing the running service with its config…', '正在比较运行中的服务与其配置…'],
    ['check.failed', 'The check failed: {e}', '检查失败：{e}'],
    ['drift.n', '{n} drift', '{n} 处不一致'], ['info.n', '{n} info', '{n} 处说明'],
    ['drift', 'drift', '不一致'], ['info', 'info', '说明'],
    ['ok.title', 'The running service and its config agree', '运行中的服务与其配置一致'],
    ['ok.detail', 'Nothing fit runs contradicts the config it runs from.', 'fit 运行的各项都与其配置一致。'],
    ['config.val', 'Config', '配置'], ['running.val', 'Running', '运行中'],
    ['fix', 'Fix', '修复方式'],
    ['apply.this', 'Apply this fix', '应用此修复'],
    ['bar.note', 'Only the fixes you tick are written to the config.', '只会把勾选的修复写入配置。'],
    ['apply.selected', 'Apply the selected fixes ({n})', '应用所选修复（{n}）'],
    ['apply.title', 'Apply the selected fixes', '应用所选的修复'],
    ['apply.note', 'These fixes are applied together as one new config version. Nothing is applied unless you confirm.',
      '这些修复会一起应用为一个新的配置版本；未确认前不会应用任何修改。'],
    ['apply.go', 'Apply', '应用'],
    ['applied', 'Applied as config version {v}.', '已应用为配置版本 {v}。'],
    ['changed', 'The config was changed elsewhere since this check. Check again before applying fixes.',
      '自本次检查后，配置已在别处被修改。应用修复前请重新检查。'],
    ['not.offered', 'No fix is offered here: edit the config on its page, or apply the config again.',
      '此处没有可用的修复：请在相应页面修改配置，或重新应用配置。'],
    ['fix.add', 'Write it into the config', '写入配置'],
    ['fix.delete', 'Remove it from the config', '从配置删除'],
    ['fix.replace', 'Change the config to the running value', '改为运行中的值'],
    ['fix.reapply', 'Apply the config again', '重新应用配置'],
    ['confirm.name', 'Difference', '差异'], ['confirm.cat', 'Category', '类别'], ['confirm.fix', 'Fix', '修复'],
    ['cat.settings', 'Settings', '设置'], ['cat.listeners', 'Listeners', '监听'], ['cat.routes', 'Routes', '路由'],
    ['cat.upstreams', 'Upstreams', '上游'], ['cat.services', 'Layer 4 services', '四层服务'],
    ['cat.tls', 'TLS certificates', 'TLS 证书'], ['cat.roles', 'Roles', '角色'], ['cat.cache', 'Cache', '缓存'],
    ['cat.providers', 'Config sources', '配置来源'],
    ['item.one', '1 item', '1 项'], ['items.n', '{n} items', '{n} 项']
  ];

  // CATS is the order of the categories, the order the Admin API sorts them in. / 类别顺序，与 Admin API 的排序一致。
  var CATS = ['settings', 'listeners', 'routes', 'upstreams', 'services', 'tls', 'roles', 'cache', 'providers'];

  WB.module({
    id: 'sys.check', server: 'admin', group: 'system', order: 4, icon: 'check', label: 'mod.label', words: WORDS,
    nav: [{ id: 'check', icon: 'check', label: 'mod.label' }],
    setup: function (UI, sh) {
      var h = UI.h, t = UI.t;
      // live is the page instance shown: an answer that arrives after the user left is dropped.
      // live 是当前显示的页面实例：用户离开后才到的应答会被丢弃。
      var live = null;

      // pretty is a config or running value as JSON text for a <pre>. / 配置值或运行值，格式化为 JSON 文本。
      function pretty(v) {
        if (v === undefined) return '';
        try { return JSON.stringify(v, null, 2); } catch (e) { return String(v); }
      }
      function has(v) { return v !== undefined; }
      function parse(txt) { try { return txt ? JSON.parse(txt) : {}; } catch (e) { return {}; } }
      function shown(st) { return live === st && st.box && st.box.parentNode === st.el; }

      // catLabel is a category's localized name; an unknown category stays as the server names it.
      // catLabel 返回类别的本地化名称；未知类别照服务端的写法显示。
      function catLabel(c) { return CATS.indexOf(c) >= 0 ? t('cat.' + c) : (c || ''); }
      // fixLabel is a fix action's localized name. / 修复动作的本地化名称。
      var ACTIONS = { add: 1, delete: 1, replace: 1, reapply: 1 };
      function fixLabel(f) { return f && f.action ? (ACTIONS[f.action] ? t('fix.' + f.action) : f.action) : ''; }
      // fixText is the action and, when it has one, the patch it writes. / 修复动作，若有补丁则附上补丁。
      function fixText(f) {
        if (!f) return '';
        var p = f.patch, path = p && p.path ? (p.op ? p.op + ' ' + p.path : p.path) : '';
        return path ? fixLabel(f) + ' · ' + path : fixLabel(f);
      }
      function countLabel(n) { return n === 1 ? t('item.one') : t('items.n', { n: n }); }

      // The tools: the running config's version, when the check ran, and the button that runs it again.
      // 工具栏：运行中的配置版本、检查时间与重新检查按钮。
      function buildTools(st) {
        st.stamp = h('span.muted.small.ck-stamp');
        st.verEl = h('span.muted.small.ck-version');
        st.again = UI.btn(t('check.again'), 'refresh', function () {
          UI.busy(st.again, load(st)).catch(UI.fail);
        }, 'ghost sm');
        UI.mount(sh.tools, st.verEl, st.stamp, st.again);
      }
      function paintTools(st) {
        var r = st.res || {};
        st.verEl.textContent = r.version != null ? t('version', { v: r.version }) : '';
        var when = '';
        if (r.checked_at) {
          var d = new Date(r.checked_at);
          when = isNaN(d.getTime()) ? String(r.checked_at) : d.toLocaleTimeString();
        }
        st.stamp.textContent = when ? t('checked.at', { time: when }) : '';
      }

      // chosen is the drift items the user ticked, with the fix picked for each. / 用户勾选的不一致项及各选的修复。
      function chosen(st) {
        var out = [], diffs = (st.res && st.res.differences) || [];
        diffs.forEach(function (d) {
          if (d.kind !== 'drift') return;
          var s = st.sel[d.id];
          if (!s || !s.on) return;
          var fixes = d.fixes || [];
          var f = fixes[s.fix] || fixes[0];
          if (f) out.push({ diff: d, fix: f });
        });
        return out;
      }
      function paintBar(st) {
        if (!st.applyBtn) return;
        var n = chosen(st).length;
        st.barLabel.textContent = t('apply.selected', { n: n });
        st.applyBtn.disabled = n === 0;
      }

      // ---- the page / 页面 ----
      function page() {
        var st = {
          res: null, sel: {}, notice: null, loading: null, box: null,
          bar: null, applyBtn: null, barLabel: null, stamp: null, verEl: null, again: null
        };
        st.el = sh.page('check', t('mod.label'));
        st.box = h('div.ck-page.stack');
        buildTools(st);
        UI.mount(st.el, st.box);
        UI.mount(st.box, h('div.muted.ck-loading', t('loading.check')));
        live = st;
        load(st);
      }

      function load(st) {
        if (st.loading) return st.loading;
        st.loading = UI.api('GET', '/api/config/check').then(function (r) {
          st.loading = null;
          if (!shown(st)) return;
          st.res = r || {};
          st.sel = {};
          st.notice = null;
          paintTools(st);
          render(st);
        }, function (e) {
          st.loading = null;
          if (!shown(st)) return;
          st.res = null; st.sel = {}; st.notice = null; st.bar = null;
          paintTools(st);
          renderError(st, e);
        });
        return st.loading;
      }

      function renderError(st, e) {
        UI.clear(st.box);
        st.box.appendChild(h('div.callout.callout--danger', UI.icon('alert'), ' ',
          t('check.failed', { e: (e && e.message) || String(e) })));
        var b = UI.btn(t('check.again'), 'refresh', function () {
          UI.busy(b, load(st)).catch(UI.fail);
        }, 'sm');
        st.box.appendChild(h('div.row', b));
      }

      // The config and running values of a difference, each capped and scrollable. / 差异的配置值与运行值，各自限高可滚动。
      function values(d) {
        if (!has(d.config) && !has(d.running)) return null;
        var g = h('div.ck-vals');
        function block(label, v) {
          return h('div.ck-val', h('div.ck-val__label', label), h('pre.ck-pre', pretty(v)));
        }
        if (has(d.config)) g.appendChild(block(t('config.val'), d.config));
        if (has(d.running)) g.appendChild(block(t('running.val'), d.running));
        return g;
      }

      // The fix select and the apply tick of a drift row: one fix is picked, none is applied until ticked.
      // 不一致行的修复下拉框与应用勾选：选中一种修复，勾选之前不会应用。
      function fixControls(st, d) {
        var fixes = d.fixes || [];
        if (!fixes.length) return h('div.ck-fix', h('span.muted.small', t('not.offered')));
        var name = d.name || d.id || '';
        var s = st.sel[d.id] = st.sel[d.id] || { on: false, fix: 0 };
        s.fix = 0;
        var pick = h('select.select.select--sm.ck-fix__select', { 'aria-label': t('fix') + ': ' + name });
        fixes.forEach(function (f, i) {
          pick.appendChild(h('option', { value: String(i) }, fixText(f)));
        });
        pick.value = '0';
        pick.addEventListener('change', function () { s.fix = +pick.value; });
        var box = h('input', { type: 'checkbox', checked: false, 'aria-label': t('apply.this') + ': ' + name });
        box.addEventListener('change', function () { s.on = box.checked; paintBar(st); });
        return h('div.ck-fix',
          h('span.ck-fix__label.muted.small', t('fix')), pick,
          h('label.check.small', box, h('span', t('apply.this'))));
      }

      function row(st, d) {
        var drift = d.kind === 'drift';
        var out = h('div.ck-row' + (drift ? '.is-drift' : '.is-info'), { 'data-id': d.id || '' },
          h('div.ck-row__head',
            UI.badge(drift ? t('drift') : t('info'), drift ? 'warn' : '', drift),
            h('b.ck-row__name', d.name || d.id || '')),
          d.detail ? h('div.ck-row__detail', d.detail) : null,
          values(d));
        if (drift) out.appendChild(fixControls(st, d));
        return out;
      }

      function makeBar(st) {
        st.barLabel = h('span.ck-bar__label');
        st.applyBtn = h('button.btn.btn--primary', { type: 'button', disabled: true, onclick: function () { confirm(st); } },
          UI.icon('check'), st.barLabel);
        st.bar = h('div.ck-bar', h('div.ck-bar__left', h('span.muted.small', t('bar.note'))), st.applyBtn);
        return st.bar;
      }

      function render(st) {
        var r = st.res || {};
        var diffs = r.differences || [];
        var groups = {}, order = [], drift = 0, info = 0;
        diffs.forEach(function (d) {
          if (d.kind === 'drift') drift++; else info++;
          var c = d.category || '';
          if (!groups[c]) { groups[c] = []; order.push(c); }
          groups[c].push(d);
        });
        order = CATS.filter(function (c) { return groups[c]; })
          .concat(order.filter(function (c) { return CATS.indexOf(c) < 0; }));
        UI.clear(st.box);
        if (st.notice) st.box.appendChild(st.notice);
        if (drift === 0) {
          st.box.appendChild(h('div.callout.ck-ok',
            h('div', UI.icon('check'), ' ', h('b', t('ok.title'))),
            h('div.ck-ok__detail.small', t('ok.detail'))));
        } else {
          st.box.appendChild(h('div.row.ck-summary',
            UI.badge(t('drift.n', { n: drift }), 'warn', true),
            info ? UI.badge(t('info.n', { n: info }), '') : null));
        }
        order.forEach(function (c) {
          var rows = groups[c] || [];
          st.box.appendChild(h('section.card.ck-cat', { 'data-cat': c },
            h('div.card__head', h('h2', catLabel(c)), h('span.muted.small', countLabel(rows.length))),
            h('div.card__body.ck-cat__body', rows.map(function (d) { return row(st, d); }))));
        });
        if (drift > 0) {
          st.box.appendChild(makeBar(st));
          paintBar(st);
        } else {
          st.bar = null; st.applyBtn = null; st.barLabel = null;
        }
      }

      // ---- applying / 应用 ----
      function confirm(st) {
        var picks = chosen(st);
        if (!picks.length) return;
        UI.modal({
          title: t('apply.title'), wide: true,
          body: h('div.stack',
            h('p', { style: { margin: 0 } }, t('apply.note')),
            UI.table([
              { label: t('confirm.name'), render: function (p) {
                var diff = p.diff;
                return h('span', h('b', diff.name || diff.id || ''),
                  diff.id && diff.id !== diff.name ? h('div.muted.small', h('code', diff.id)) : null);
              } },
              { label: t('confirm.cat'), render: function (p) { return UI.badge(catLabel(p.diff.category)); } },
              { label: t('confirm.fix'), render: function (p) { return fixText(p.fix); } }
            ], picks)),
          actions: [
            { label: t('cancel') },
            { label: t('apply.go'), kind: 'primary', onclick: function () { return send(st, picks); } }
          ]
        });
      }

      function changedNotice(st) {
        return h('div.callout.callout--danger.ck-notice', UI.icon('alert'), ' ',
          h('span', t('changed')), ' ',
          UI.btn(t('check.again'), 'refresh', function () { st.sel = {}; load(st); }, 'ghost sm'));
      }

      // send applies the picked fixes as one config version, then handles the answer as the config save does.
      // send 把所选修复作为一个配置版本应用，然后按配置保存的方式处理应答。
      function send(st, picks) {
        var body = JSON.stringify({
          fixes: picks.map(function (p) { return { id: p.diff.id, action: p.fix.action }; })
        });
        return UI.raw('POST', '/api/config/reconcile', body, {
          'Content-Type': 'application/json',
          'If-Match': (st.res && st.res.etag) || ''
        }).then(function (r) {
          return r.text().then(function (txt) {
            var out = parse(txt), msg = out.error || (r.status + ' ' + r.statusText);
            if (r.status === 401) return;
            if (r.ok) {
              UI.toast(t('applied', { v: out.version }));
              st.sel = {};
              st.notice = null;
              return load(st);
            }
            if (r.status === 412) {
              st.sel = {};
              st.notice = changedNotice(st);
              render(st);
              return;
            }
            if (r.status === 400) {
              st.sel = {};
              UI.toast(msg, 'error');
              return load(st);
            }
            UI.toast(msg, 'error');
          });
        });
      }

      return { routes: { check: page } };
    }
  });
})();
