// What a manager of many fits owns in this fit's config (I145), on this fit's own config pages: GET
// /api/m/admin/node/managed, which the manager keeps up to date (its PUT /node/managed), lists the parts its templates
// own here, a top-level member whole (log, media_auth, ...) or an element of listeners, routes, services or upstreams
// by name, each with whether it changed here since the manager saw it in sync. The forms mark those parts (a bar on
// their left and a badge, amber when changed here, which the manager shows as a conflict), and a change to one asks
// first: focusing a field of it, clicking one of its buttons, editing a raw tab of a page that holds one, and Save and
// apply with a change to one not asked about. Asked once per part until the config is read again. Nothing here stops
// a change: the member stays its own, and the manager reports what changed.
//
// 成员的配置页面上，管理端（集群管理）在本 fit 配置中拥有的部分（I145）：GET /api/m/admin/node/managed（由管理端通过
// PUT /node/managed 保持最新）列出其模板在此拥有的部分——整个顶层成员（log、media_auth 等），或 listeners、routes、
// services、upstreams 中按名称的元素——以及每个部分在管理端看到其同步之后是否在此被修改。表单标出这些部分（左侧色条与
// 徽标；在此修改过的为琥珀色，管理端将其显示为冲突），修改前先询问：聚焦其字段、点击其按钮、编辑包含它的页面的原始文本
// 标签页，以及在未询问的情况下“保存并应用”对其的修改。每个部分询问一次，直到重新读取配置。这里不阻止修改：成员仍归自己，
// 管理端会报告修改。
(function () {
  'use strict';
  var h = UI.h;
  var WORDS = [
    ['mg.owned', 'Managed by the cluster ({template})', '由集群管理（{template}）'],
    ['mg.changed', 'Changed here since the manager set it', '管理端设置之后在此被修改'],
    ['mg.ask', '{part} is managed by the cluster manager {manager}, from its template {template}. A change here is reported there as a conflict, and a rollout of the template would put its value back. Change it here anyway?',
      '{part} 由集群管理端 {manager} 通过其模板 {template} 管理。在此修改会在管理端显示为冲突，模板的发布会恢复其值。仍要在此修改吗？'],
    ['mg.ask.save', 'These parts are managed by the cluster manager {manager}: {parts}. Changing them here is reported there as a conflict, and a rollout of their templates would put their values back. Save anyway?',
      '这些部分由集群管理端 {manager} 管理：{parts}。在此修改会在管理端显示为冲突，模板的发布会恢复其值。仍要保存吗？'],
    ['mg.ask.raw', 'This page holds parts managed by the cluster manager {manager}: {parts}. Changing them here is reported there as a conflict. Edit the text anyway?',
      '本页包含由集群管理端 {manager} 管理的部分：{parts}。在此修改会在管理端显示为冲突。仍要编辑文本吗？'],
    ['mg.go', 'Change it here', '仍要修改'], ['mg.save', 'Save anyway', '仍要保存']
  ];
  var en = {}, zh = {};
  WORDS.forEach(function (w) { en[w[0]] = w[1]; zh[w[0]] = w[2]; });
  UI.addI18n({ 'en': en, 'zh-CN': zh });
  var t = UI.t;

  var rec = null, base = null, allowed = {}, asking = false, bypass = false, queued = false;

  function cfg() { return window.FitConfig && FitConfig.with ? FitConfig.with(UI) : null; }
  function draft() { var c = cfg(); return c ? c.draft || {} : {}; }
  function clone(v) { return v === undefined ? undefined : JSON.parse(JSON.stringify(v)); }
  function keyOf(p) { return p.name ? p.member + '[' + p.name + ']' : p.member; }
  function who() { return rec.manager_host ? rec.manager + ' (' + rec.manager_host + ')' : rec.manager; }
  // valueOf is the part's value in d, and dataPath where the forms draw it. / 部分在 d 中的值，以及表单中的 data-path。
  function locate(d, p) {
    if (!p.name) return { value: d[p.member], dataPath: p.member };
    var list = Array.isArray(d[p.member]) ? d[p.member] : [];
    for (var i = 0; i < list.length; i++) if (list[i] && list[i].name === p.name) return { value: list[i], dataPath: p.member + '[' + i + ']' };
    return { value: undefined, dataPath: '' };
  }
  function parts() { return rec && rec.paths ? rec.paths : []; }
  function partOf(key) { return parts().filter(function (p) { return keyOf(p) === key; })[0]; }

  function read() {
    UI.scope('admin').api('GET', '/api/node/managed', undefined, true).then(function (r) {
      rec = r && r.paths && r.paths.length ? r : null;
      allowed = {};
      schedule();
    }, function () { rec = null; });
  }

  // mark marks the parts the forms drawn show. / 标出当前表单中显示的部分。
  function mark() {
    queued = false;
    document.querySelectorAll('[data-mg-key]').forEach(function (el) {
      if (!rec || !partOf(el.getAttribute('data-mg-key'))) {
        el.classList.remove('mg-owned', 'mg-changed');
        el.removeAttribute('data-mg-key');
        var b = el.querySelector(':scope > summary > .mg-badge, :scope > .mg-badge');
        if (b) b.remove();
      }
    });
    if (!rec) return;
    var d = draft();
    parts().forEach(function (p) {
      var at = locate(d, p).dataPath;
      if (!at) return;
      var el = document.querySelector('[data-path="' + (window.CSS && CSS.escape ? CSS.escape(at) : at) + '"]');
      if (!el) return;
      var key = keyOf(p);
      el.classList.add('mg-owned');
      el.classList.toggle('mg-changed', !!p.changed);
      el.setAttribute('data-mg-key', key);
      var host = el.tagName === 'DETAILS' ? el.querySelector(':scope > summary') : el;
      var old = host.querySelector(':scope > .mg-badge');
      if (old) old.remove();
      var badge = UI.badge(p.changed ? t('mg.changed') : t('mg.owned', { template: p.template || '' }), p.changed ? 'warn' : 'primary', true);
      badge.classList.add('mg-badge');
      badge.title = t('mg.owned', { template: p.template || '' }) + ' · ' + who();
      if (host === el) el.insertBefore(badge, el.firstChild); else host.appendChild(badge);
    });
  }
  function schedule() {
    if (queued) return;
    queued = true;
    (window.requestAnimationFrame || setTimeout)(mark);
  }

  // pageParts are the owned parts of the members the page shown edits. / 当前页面所编辑成员中被管理的部分。
  function pageParts() {
    var pg = window.WB && WB.page ? WB.page() : null, ms = pg && pg.editable && pg.editable.members ? pg.editable.members : [];
    return parts().filter(function (p) { return ms.indexOf(p.member) >= 0; });
  }
  function ask(msg, ok) {
    asking = true;
    return UI.confirm(msg, ok).then(function (yes) { asking = false; return yes; });
  }
  function control(el) { return el && el.matches && el.matches('input, select, textarea, button'); }

  // guard asks before a field or a button of an owned part is used. / 使用被管理部分的字段或按钮前先询问。
  function guard(ev) {
    if (!rec || bypass) return;
    var target = ev.target;
    if (!control(target)) return;
    if (target.classList.contains('cf-raw')) {
      var ps = pageParts().filter(function (p) { return !allowed[keyOf(p)]; });
      if (!ps.length) return;
      ev.preventDefault(); ev.stopPropagation();
      if (target.blur) target.blur();
      if (asking) return;
      ask(t('mg.ask.raw', { manager: who(), parts: ps.map(keyOf).join(', ') }), t('mg.go')).then(function (yes) {
        if (!yes) return;
        ps.forEach(function (p) { allowed[keyOf(p)] = true; });
        target.focus();
      });
      return;
    }
    var el = target.closest('[data-mg-key]');
    if (!el) return;
    var key = el.getAttribute('data-mg-key'), p = partOf(key);
    if (!p || allowed[key]) return;
    ev.preventDefault(); ev.stopPropagation();
    if (ev.type === 'focusin' && target.blur) target.blur();
    if (asking) return;
    ask(t('mg.ask', { part: key, manager: who(), template: p.template || '' }), t('mg.go')).then(function (yes) {
      if (!yes) return;
      allowed[key] = true;
      if (target.tagName === 'BUTTON' || target.type === 'checkbox') target.click(); else target.focus();
    });
  }

  // saveGuard asks before Save and apply sends a change to an owned part not asked about. / “保存并应用”提交未询问过的
  // 被管理部分的修改前先询问。
  function saveGuard(ev) {
    if (!rec || bypass) return;
    var b = ev.target && ev.target.closest && ev.target.closest('button');
    if (!b || b.textContent.trim() !== t('cf.save').trim()) return;
    var d = draft(), changed = parts().filter(function (p) {
      return !allowed[keyOf(p)] && JSON.stringify(locate(d, p).value) !== JSON.stringify(locate(base || {}, p).value);
    });
    if (!changed.length) return;
    ev.preventDefault(); ev.stopPropagation();
    if (asking) return;
    ask(t('mg.ask.save', { manager: who(), parts: changed.map(keyOf).join(', ') }), t('mg.save')).then(function (yes) {
      if (!yes) return;
      changed.forEach(function (p) { allowed[keyOf(p)] = true; });
      bypass = true;
      try { b.click(); } finally { bypass = false; }
    });
  }

  document.addEventListener('focusin', guard, true);
  document.addEventListener('click', function (ev) {
    saveGuard(ev);
    if (!ev.defaultPrevented && ev.target && (ev.target.tagName === 'BUTTON' || ev.target.type === 'checkbox' || (ev.target.closest && ev.target.closest('button')))) {
      var target = ev.target.tagName === 'BUTTON' || ev.target.type === 'checkbox' ? ev.target : ev.target.closest('button');
      guard({ type: 'click', target: target, preventDefault: function () { ev.preventDefault(); }, stopPropagation: function () { ev.stopPropagation(); } });
    }
  }, true);
  // A new draft read: what the manager owns read again, and the base to compare a save with. / 读取了新的草稿：重新读取
  // 被管理的部分，以及保存时比较的基准。
  window.addEventListener('fit:config', function () { base = clone(draft()); read(); });
  window.addEventListener('hashchange', schedule);
  document.addEventListener('DOMContentLoaded', function () {
    if (window.MutationObserver) new MutationObserver(schedule).observe(document.body, { childList: true, subtree: true });
  });

  window.FitManaged = { read: read, record: function () { return rec; } };
})();
