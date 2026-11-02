// The config's shared draft and its schema-driven form editor, for the gateway's and the system's modules (the admin
// server module). Every page that edits the config edits the same draft, read once with its ETag, and one bar in
// the top bar saves it whole (PUT /config with If-Match) or discards it; the problems of a save link to the page and
// the field they are about. admin is left out of the forms: it is set in the file fit was started with.
//
//   var C = FitConfig.with(UI)          the store, sharing one draft whoever asks
//   C.ensure() → Promise                 the schema and the config read (again when it changed and nothing is unsaved)
//   C.page(el, sh, render)               a page of the editor: the bar in sh.tools, render(body) drawn now and after
//                                        each save, discard or reload
//   C.section(name, opts) → node         the form of a top-level member; opts for arrays: {filter(item), make(),
//                                        chips(item) → nodes, addLabel, empty}
//   C.place(fn(path, draft) → hash)      where a problem's path is edited, for its link
//
// 配置的共享草稿及由 schema 生成的表单编辑器，供网关与系统各模块（admin 服务端模块）使用。所有编辑配置的页面都编辑同一份
// 草稿（连同 ETag 只读取一次），顶栏中的同一个保存栏整体保存（带 If-Match 的 PUT /config）或放弃修改；保存失败的问题
// 链接到其所在的页面与字段。表单不含 admin：它在 fit 启动时所用的配置文件中设置。
(function () {
  'use strict';
  var h = UI.h;

  // [key, en, zh] / [键, 英文, 中文]
  var WORDS = [
    ['cf.version', 'Config version {v}', '配置版本 {v}'], ['cf.unsaved', 'Unsaved changes', '有未保存的修改'],
    ['cf.save', 'Save and apply', '保存并应用'], ['cf.discard', 'Discard', '放弃修改'],
    ['cf.discard.q', 'Discard the changes not saved?', '放弃尚未保存的修改？'],
    ['cf.leave', 'The changes not saved will be lost.', '尚未保存的修改将会丢失。'],
    ['cf.applied', 'Applied as config version {v}.', '已应用为配置版本 {v}。'],
    ['cf.warnings', 'Applied as config version {v}, with warnings:', '已应用为配置版本 {v}，但有警告：'],
    ['cf.changed', 'The config was changed elsewhere since it was read. Discard your changes to read it again, or copy them from the JSON page first.',
      '读取之后配置已在别处被修改。放弃修改即可重新读取；也可以先在 JSON 页面复制你的修改。'],
    ['cf.reread', 'Discard and read again', '放弃并重新读取'],
    ['cf.problems', 'The config was not applied; the running config stays.', '配置未应用；运行中的配置保持不变。'],
    ['cf.failed', 'Saving failed: {e}', '保存失败：{e}'],
    ['cf.whole', 'the config', '整个配置'], ['cf.line', 'line {l}, column {c}', '第 {l} 行第 {c} 列'],
    ['cf.raw.bad', 'The JSON page holds text that does not parse: fix it or discard it before saving. The forms show the last JSON that parsed.',
      'JSON 页面中的文本无法解析：保存前请修正或放弃。表单显示的是最后一次可解析的 JSON。'],
    ['cf.unset', 'not set', '未设置'], ['cf.set', 'Set', '设置'], ['cf.leaveout', 'Leave out', '移除'],
    ['cf.add', 'Add', '添加'], ['cf.remove', 'Remove', '删除'], ['cf.up', 'Move up', '上移'], ['cf.down', 'Move down', '下移'],
    ['cf.items', '{n} items', '{n} 项'], ['cf.item', '1 item', '1 项'], ['cf.fields', '{n} fields', '{n} 个字段'], ['cf.field', '1 field', '1 个字段'],
    ['cf.default', 'default: {v}', '默认：{v}'], ['cf.leftout', '(left out)', '（不设置）'], ['cf.leftout.d', '(left out: {v})', '（不设置：{v}）'],
    ['cf.module', 'module', 'module'], ['cf.params', 'params', 'params'], ['cf.name', 'name', '名称'],
    ['cf.json.ph', 'JSON', 'JSON'], ['cf.none', 'Nothing yet.', '暂无。'],
    ['cf.remove.q', 'Remove {what}?', '删除 {what}？'],
    ['cf.deprecated', 'deprecated', '已弃用'],
    ['cf.c.listener', 'Listener', '监听'], ['cf.c.names', 'Names', '域名'], ['cf.c.issuer', 'Issuer', '签发者'], ['cf.c.from', 'Valid from', '生效时间'],
    ['cf.c.until', 'Expires', '过期时间'], ['cf.c.serial', 'Serial', '序列号'], ['cf.c.left', '{n} days left', '剩余 {n} 天'], ['cf.c.expired', 'expired', '已过期'],
    ['cf.c.none', 'No listener serves a certificate.', '没有监听在提供证书。']
  ];
  var en = {}, zh = {};
  WORDS.forEach(function (w) { en[w[0]] = w[1]; zh[w[0]] = w[2]; });
  UI.addI18n({ 'en': en, 'zh-CN': zh });
  var t = UI.t;

  // The store: one, whichever module asks. / 存储：只有一个，供所有模块共用。
  var S = {
    ui: null, schema: null, draft: null, etag: '', version: '', dirty: false,
    errors: [],      // the problems of the last save, {path, line, column, message} / 上次保存的问题
    message: null,   // {kind, text, list, action} shown above the forms / 显示在表单上方的消息
    rawText: null,   // the JSON page's text while it does not parse / JSON 页面中无法解析的文本
    rawError: '',
    open: {},        // path → whether its node is open / 路径 → 节点是否展开
    focus: '',       // the path to show after the next draw / 下次绘制后要定位的路径
    places: [], current: null, loading: null
  };

  window.addEventListener('beforeunload', function (e) {
    if (S.dirty) { e.preventDefault(); e.returnValue = t('cf.leave'); }
  });

  // ---- requests / 请求 ----
  function call(method, path, body, headers) { return S.ui.raw(method, '/api' + path, body, headers); }
  function readJSON(r) {
    return r.text().then(function (x) {
      try { return x ? JSON.parse(x) : {}; } catch (e) { return { error: x }; }
    });
  }
  // fetchConfig reads the config; cond, only when it changed since the ETag held. / 读取配置；cond 时仅在 ETag 变化后读取。
  function fetchConfig(cond) {
    return call('GET', '/config', undefined, cond && S.etag ? { 'If-None-Match': S.etag } : {}).then(function (r) {
      if (r.status === 304) return false;
      return readJSON(r).then(function (body) {
        if (!r.ok) { var e = new Error(body.error || r.status + ' ' + r.statusText); e.status = r.status; throw e; }
        S.etag = r.headers.get('ETag') || '';
        S.version = r.headers.get('Fit-Config-Version') || '';
        S.draft = body && typeof body === 'object' ? body : {};
        S.errors = []; S.rawText = null; S.rawError = '';
        setDirty(false);
        return true;
      });
    });
  }

  function ensure() {
    if (!S.loading) {
      S.loading = Promise.all([S.ui.api('GET', '/api/schema'), fetchConfig(false)]).then(function (r) { S.schema = r[0]; return true; });
      S.loading.catch(function () { S.loading = null; });
      return S.loading;
    }
    return S.loading.then(function () { return S.dirty ? false : fetchConfig(true); });
  }

  // ---- state / 状态 ----
  var bar = null, notice = null;
  function setDirty(d) { S.dirty = d; paintBar(); }
  // changed marks the draft edited. / 标记草稿已修改。
  function changed() {
    if (S.message && S.message.kind !== 'danger') S.message = null;
    setDirty(true);
    paintNotice();
  }

  function redraw() {
    var c = S.current;
    if (!c || !document.body.contains(c.el)) return;
    var sc = document.scrollingElement, main = c.el.closest('.main');
    var y = sc ? sc.scrollTop : 0, my = main ? main.scrollTop : 0;
    draw(c);
    if (sc) sc.scrollTop = y;
    if (main) main.scrollTop = my;
    showFocus(c.el);
  }

  function reload() {
    S.message = null;
    return fetchConfig(false).then(function () { paintNotice(); redraw(); });
  }

  function discard() {
    return (S.dirty || S.rawText != null ? UI.confirm(t('cf.discard.q'), t('cf.discard'), true) : Promise.resolve(true)).then(function (ok) {
      if (ok) return reload();
    });
  }

  function save() {
    if (S.rawText != null) { UI.toast(t('cf.raw.bad'), 'error'); return Promise.resolve(); }
    return call('PUT', '/config', JSON.stringify(S.draft, null, 2), { 'If-Match': S.etag }).then(function (r) {
      return readJSON(r).then(function (res) {
        if (r.ok) {
          var w = res.warnings || [];
          return fetchConfig(false).then(function () {
            S.message = w.length ? { kind: 'warn', text: t('cf.warnings', { v: res.version }), list: w } : null;
            UI.toast(t('cf.applied', { v: res.version }));
            paintNotice(); redraw();
            refreshMenu();
          });
        }
        if (r.status === 401) return;
        if (r.status === 412) {
          S.errors = [];
          S.message = { kind: 'danger', text: t('cf.changed'), action: [t('cf.reread'), reload] };
        } else {
          S.errors = res.errors || [];
          S.message = S.errors.length ? null : { kind: 'danger', text: t('cf.failed', { e: res.error || r.status + ' ' + r.statusText }) };
        }
        paintNotice(); redraw();
        var n = notice && notice.firstChild;
        if (n && n.scrollIntoView) n.scrollIntoView({ block: 'nearest' });
      });
    }).catch(UI.fail);
  }

  // refreshMenu shows the modules of the roles a config started, hides those it stopped. / 刷新菜单：显示新启动角色的模块，隐藏已停止的。
  function refreshMenu() { if (window.WB && WB.refresh) WB.refresh().catch(UI.fail); }

  // rollback applies version n again, as a new version. / 重新应用版本 n，作为新版本。
  function rollback(n) {
    return call('POST', '/rollback/' + encodeURIComponent(n), undefined, { 'If-Match': S.etag }).then(function (r) {
      return readJSON(r).then(function (res) {
        if (!r.ok) { var e = new Error(res.error || r.status + ' ' + r.statusText); e.status = r.status; throw e; }
        return fetchConfig(false).then(function () {
          var w = res.warnings || [];
          S.message = w.length ? { kind: 'warn', text: t('cf.warnings', { v: res.version }), list: w } : null;
          paintNotice(); refreshMenu(); return res;
        });
      });
    });
  }

  // ---- the bar and the notices / 保存栏与提示 ----
  function paintBar() {
    if (!bar) {
      var saveB = UI.btn(t('cf.save'), 'save', function () { UI.busy(saveB, save()).then(paintBar, paintBar); }, 'primary sm');
      var disc = UI.btn(t('cf.discard'), 'undo', function () { UI.busy(disc, discard()).catch(UI.fail).then(paintBar); }, 'sm');
      bar = { el: h('div.row', h('span.cf-state'), disc, saveB), state: null, save: saveB, discard: disc };
      bar.state = bar.el.firstChild;
    }
    var dirty = S.dirty || S.rawText != null;
    UI.mount(bar.state, dirty ? UI.badge(t('cf.unsaved'), 'warn', true) : h('span.muted.small', S.version ? t('cf.version', { v: S.version }) : ''));
    bar.save.disabled = !dirty;
    bar.discard.disabled = !dirty;
  }

  function problemLink(e) {
    var where = e.path || t('cf.whole');
    var at = e.line ? ' (' + t('cf.line', { l: e.line, c: e.column || 0 }) + ')' : '';
    var hash = e.path ? locate(e.path) : (e.line ? '#/sys.json/json' : null);
    var label = h('code', where);
    var link = hash ? h('a', { href: hash, onclick: function (ev) {
      ev.preventDefault();
      S.focus = e.path || '';
      if (location.hash === hash) redraw(); else location.hash = hash;
    } }, label) : label;
    return h('li', link, at, ': ', e.message);
  }

  function paintNotice() {
    paintBar();
    if (!notice) notice = h('div.stack');
    var parts = [];
    if (S.rawText != null) parts.push(h('div.callout.callout--warn', UI.icon('alert'), ' ', t('cf.raw.bad')));
    if (S.message) {
      var m = S.message;
      parts.push(h('div.callout' + (m.kind ? '.callout--' + m.kind : ''),
        h('div.row.row--between', h('span', m.text),
          h('span.btn-row', m.action ? UI.btn(m.action[0], 'refresh', function (ev) { UI.busy(ev.currentTarget, m.action[1]()).catch(UI.fail); }, 'sm') : null,
            h('button.btn.btn--ghost.btn--icon.btn--sm', { type: 'button', 'aria-label': t('close'), onclick: function () { S.message = null; paintNotice(); } }, UI.icon('x')))),
        m.list ? h('ul', { style: { margin: '6px 0 0', paddingLeft: '18px' } }, m.list.map(function (x) { return h('li', x); })) : null));
    }
    if (S.errors.length) {
      parts.push(h('div.callout.callout--danger', h('b', t('cf.problems')),
        h('ul', { style: { margin: '6px 0 0', paddingLeft: '18px' } }, S.errors.map(problemLink))));
    }
    UI.mount(notice, parts);
    notice.hidden = !parts.length;
    return notice;
  }

  // ---- places / 位置 ----
  function locate(path) {
    for (var i = 0; i < S.places.length; i++) {
      var hash = S.places[i](path, S.draft || {});
      if (hash) return hash;
    }
    return null;
  }

  function showFocus(el) {
    if (!S.focus) return;
    var p = S.focus;
    S.focus = '';
    while (p) {
      var n = el.querySelector('[data-path="' + (window.CSS && CSS.escape ? CSS.escape(p) : p) + '"]');
      if (n) {
        for (var a = n; a && a !== el; a = a.parentNode) if (a.tagName === 'DETAILS') a.open = true;
        n.scrollIntoView({ block: 'center' });
        n.classList.add('cf-flash');
        var f = n.querySelector('input, select, textarea');
        if (f) f.focus({ preventScroll: true });
        return;
      }
      var cut = Math.max(p.lastIndexOf('.'), p.lastIndexOf('['));
      p = cut > 0 ? p.slice(0, cut) : '';
    }
  }

  // page draws a page of the editor. / 绘制编辑器的一个页面。
  function draw(c) {
    var body = h('div.stack');
    UI.mount(c.el, paintNotice(), body);
    c.render(body);
  }
  function page(el, sh, render) {
    paintBar();
    UI.mount(sh.tools, bar.el);
    var c = S.current = { el: el, render: render };
    el.appendChild(h('div', UI.icon('refresh'), ' ', t('loading')));
    return ensure().then(function () {
      if (S.current !== c) return;
      draw(c);
      showFocus(el);
    }).catch(function (e) {
      if (e && e.status === 401) return;
      UI.mount(el, h('div.callout.callout--danger', e.message || String(e)));
    });
  }

  // ---- the schema / schema ----
  function resolve(s) {
    var seen = 0;
    while (s && s.$ref && seen++ < 32) {
      var name = decodeURIComponent(s.$ref.replace(/^#\/\$defs\//, '')).replace(/~1/g, '/').replace(/~0/g, '~');
      var def = S.schema.$defs[name] || {};
      var o = {};
      Object.keys(def).forEach(function (k) { o[k] = def[k]; });
      o.description = s.description || def.description;
      o.default = s.default !== undefined ? s.default : def.default;
      s = o;
    }
    return s || {};
  }
  function rootProps() {
    var props = {}, all = (S.schema && S.schema.properties) || {};
    Object.keys(all).forEach(function (k) { if (k !== 'admin') props[k] = all[k]; });
    return props;
  }
  function isModuleRef(s) {
    return s.type === 'object' && s.properties && s.properties.module && Array.isArray(s.properties.module.enum) && s.properties.params;
  }
  function paramsSchema(s, module) {
    var all = s.allOf || [];
    for (var i = 0; i < all.length; i++) {
      var c = all[i];
      if (c.if && c.if.properties && c.if.properties.module && c.if.properties.module.const === module) return resolve(c.then.properties.params);
    }
    return null;
  }
  function kind(s) {
    if (isModuleRef(s)) return 'module';
    if (s.oneOf) return 'scalar';
    if (s.type === 'array') return 'array';
    if (s.type === 'object' && s.properties) return 'object';
    if (s.type === 'object' && s.additionalProperties && typeof s.additionalProperties === 'object') return 'map';
    if (['string', 'integer', 'number', 'boolean'].indexOf(s.type) >= 0) return s.type;
    return 'json';
  }
  function nestedKind(k) { return k === 'object' || k === 'array' || k === 'map' || k === 'module'; }

  // A slot is a place in the draft: its container's slot and its key there. / 槽：草稿中的一个位置。
  function rootSlot() { return { path: '', get: function () { return S.draft; }, set: function (v) { S.draft = v || {}; } }; }
  function child(parent, key, path) {
    return {
      path: path,
      get: function () { var c = parent.get(); return c == null ? undefined : c[key]; },
      set: function (v) {
        var c = parent.get();
        if (c == null) {
          if (v === undefined) return;
          c = typeof key === 'number' ? [] : {};
          parent.set(c);
        }
        if (v === undefined) { if (Array.isArray(c)) c.splice(key, 1); else delete c[key]; } else c[key] = v;
      }
    };
  }
  // join builds an error path as fit's config errors write it: a.b[0]["key"]. / 按 fit 配置错误的格式拼接路径。
  function join(path, key, isKey) {
    if (typeof key === 'number') return path + '[' + key + ']';
    if (isKey) return path + '[' + JSON.stringify(key) + ']';
    return path ? path + '.' + key : key;
  }
  function hasError(path) {
    return S.errors.some(function (e) {
      var p = e.path || '';
      return p === path || p.indexOf(path + '.') === 0 || p.indexOf(path + '[') === 0;
    });
  }

  function emptyOf(s) {
    s = resolve(s);
    switch (kind(s)) {
      case 'object': {
        var o = {};
        (s.required || []).forEach(function (r) {
          var p = resolve(s.properties[r]), k = kind(p);
          if (k === 'array') o[r] = [];
          else if (k === 'object' || k === 'map') o[r] = {};
          else if (k === 'module') o[r] = { module: p.properties.module.enum[0] };
          else if (p.enum) o[r] = p.enum[0];
          else if (k === 'string') o[r] = '';
        });
        return o;
      }
      case 'module': return { module: s.properties.module.enum[0] };
      case 'array': return [];
      case 'map': return {};
      case 'boolean': return false;
      case 'integer': case 'number': return 0;
      default: return s.enum ? s.enum[0] : '';
    }
  }

  function summary(v) {
    if (v === undefined) return t('cf.unset');
    if (Array.isArray(v)) return v.length === 1 ? t('cf.item') : t('cf.items', { n: v.length });
    if (v && typeof v === 'object') {
      if (typeof v.module === 'string') return v.module;
      var n = Object.keys(v).length;
      return n === 1 ? t('cf.field') : t('cf.fields', { n: n });
    }
    return '';
  }
  function itemLabel(v, i) {
    if (v && typeof v === 'object') {
      var ks = ['name', 'module', 'address', 'username', 'path', 'id'];
      for (var k = 0; k < ks.length; k++) if (typeof v[ks[k]] === 'string' && v[ks[k]]) return '#' + i + ' ' + v[ks[k]];
    }
    if (typeof v === 'string' || typeof v === 'number') return '#' + i + ' ' + v;
    return '#' + i;
  }

  // ---- controls / 控件 ----
  // control returns the control of a value of schema s at slot; rerender draws the page again, after a change of
  // shape. / 返回 slot 处 schema 为 s 的值的控件；rerender 在结构变化后重绘页面。
  function control(s, slot, top) {
    s = resolve(s);
    switch (kind(s)) {
      case 'object': return objectControl(s, slot, top);
      case 'array': return arrayControl(s, slot);
      case 'map': return mapControl(s, slot);
      case 'module': return moduleControl(s, slot);
      case 'boolean': return boolControl(s, slot);
      case 'json': return jsonControl(s, slot);
      default: return scalarControl(s, slot);
    }
  }

  function smallBtn(label, iconName, fn, kind2, title) {
    var b = UI.btn(label, iconName, function (ev) { ev.preventDefault(); ev.stopPropagation(); fn(); }, 'sm' + (kind2 ? ' ' + kind2 : ''));
    if (title) { b.title = title; b.setAttribute('aria-label', title); }
    return b;
  }

  function objectControl(s, slot, top) {
    var v = slot.get();
    if (v === undefined && !top) {
      return h('div.row', h('span.muted.small', t('cf.unset')), smallBtn(t('cf.set'), 'plus', function () { slot.set({}); S.open[slot.path] = true; changed(); redraw(); }));
    }
    var box = h('div.cf-form');
    var required = {};
    (s.required || []).forEach(function (r) { required[r] = true; });
    Object.keys(s.properties).forEach(function (name) {
      var p = resolve(s.properties[name]);
      if (p.deprecated && !(v && name in v)) return;
      box.appendChild(field(name, p, child(slot, name, join(slot.path, name)), required[name]));
    });
    if (!top && v !== undefined) {
      box.appendChild(h('div.btn-row', { style: { marginTop: '8px' } },
        smallBtn(t('cf.leaveout'), 'x', function () { slot.set(undefined); changed(); redraw(); }, 'ghost')));
    }
    return box;
  }

  // node is a collapsible box: a nested value, or an item of a list. / 可折叠框：嵌套值或列表项。
  function node(path, head, body, defOpen, cls) {
    var err = hasError(path);
    var d = h('details.cf-node' + (cls ? '.' + cls : '') + (err ? '.is-error' : ''), { 'data-path': path });
    d.open = err || (S.open[path] != null ? S.open[path] : !!defOpen);
    d.addEventListener('toggle', function () { S.open[path] = d.open; });
    d.appendChild(h('summary', h('span.cf-chev', UI.icon('chev')), head));
    d.appendChild(h('div.cf-body', body));
    return d;
  }

  function field(name, s, slot, required) {
    var k = kind(s);
    var err = hasError(slot.path);
    var nameEl = h('span.cf-label', name, required ? h('span.cf-req', '*') : null, s.deprecated ? h('span.badge.badge--warn', { style: { marginLeft: '6px' } }, t('cf.deprecated')) : null);
    if (nestedKind(k)) {
      var v = slot.get();
      return node(slot.path, [nameEl, h('span.muted.small', summary(v))],
        [s.description ? h('div.cf-doc', s.description) : null, control(s, slot)], v !== undefined && k !== 'object');
    }
    return h('div.cf-field' + (err ? '.is-error' : ''), { 'data-path': slot.path },
      h('div.cf-name', nameEl), h('div.cf-ctl', control(s, slot)), s.description ? h('div.cf-doc', s.description) : null);
  }

  function swapOpen(prefix, i, j) {
    var a = prefix + '[' + i + ']', b = prefix + '[' + j + ']', moved = {};
    Object.keys(S.open).forEach(function (k) {
      if (k === a || k.indexOf(a + '.') === 0 || k.indexOf(a + '[') === 0) moved[b + k.slice(a.length)] = S.open[k];
      else if (k === b || k.indexOf(b + '.') === 0 || k.indexOf(b + '[') === 0) moved[a + k.slice(b.length)] = S.open[k];
      else return;
      delete S.open[k];
    });
    Object.keys(moved).forEach(function (k) { S.open[k] = moved[k]; });
  }
  function dropOpen(prefix) {
    Object.keys(S.open).forEach(function (k) { if (k.indexOf(prefix + '[') === 0) delete S.open[k]; });
  }

  function itemButtons(slot, idx, i, n) {
    // idx: the indexes shown, in the array; i: this one's place among them. / idx：显示的项在数组中的下标；i：本项的位置。
    return [
      smallBtn('', 'chev', function () { move(slot, idx[i], idx[i - 1]); }, 'ghost icon', t('cf.up')),
      smallBtn('', 'chev', function () { move(slot, idx[i], idx[i + 1]); }, 'ghost icon', t('cf.down')),
      smallBtn('', 'trash', function () { removeAt(slot, idx[i]); }, 'ghost icon', t('cf.remove'))
    ].map(function (b, k) {
      if (k < 2) b.classList.add(k ? 'cf-down' : 'cf-up');
      if ((k === 0 && i === 0) || (k === 1 && i === n - 1)) b.disabled = true;
      return b;
    });
  }
  function move(slot, a, b) {
    var arr = slot.get();
    var x = arr[a]; arr[a] = arr[b]; arr[b] = x;
    swapOpen(slot.path, a, b);
    changed(); redraw();
  }
  function removeAt(slot, i) {
    var arr = slot.get();
    arr.splice(i, 1);
    dropOpen(slot.path);
    if (arr.length === 0) slot.set(undefined);
    changed(); redraw();
  }

  // arrayControl; opts (a top-level list): {filter, make, chips, addLabel, empty}. / 数组控件；opts 用于顶层列表。
  function arrayControl(s, slot, opts) {
    opts = opts || {};
    var items = resolve(s.items || {});
    var ik = kind(items);
    var simple = !nestedKind(ik) && ik !== 'json';
    var v = slot.get() || [];
    var idx = [];
    v.forEach(function (item, i) { if (!opts.filter || opts.filter(item, i)) idx.push(i); });
    var list = h('div.cf-list');
    idx.forEach(function (i, k) {
      var cs = child(slot, i, join(slot.path, i));
      var buttons = itemButtons(slot, idx, k, idx.length);
      if (simple) {
        list.appendChild(h('div.cf-row' + (hasError(cs.path) ? '.is-error' : ''), { 'data-path': cs.path }, control(items, cs), buttons));
        return;
      }
      var head = [h('span.cf-label', itemLabel(v[i], i)), opts.chips ? h('span.cf-chips', opts.chips(v[i])) : null, h('span.cf-btns', buttons)];
      list.appendChild(node(cs.path, head, control(items, cs, true), idx.length === 1 && !opts.top, opts.top ? 'cf-item' : ''));
    });
    if (opts.top && !idx.length) list.appendChild(UI.empty(opts.empty || t('cf.none')));
    list.appendChild(h('div.btn-row', { style: { marginTop: '8px' } }, smallBtn(opts.addLabel || t('cf.add'), 'plus', function () {
      var arr = slot.get() || [];
      arr.push(opts.make ? opts.make(arr) : emptyOf(items));
      slot.set(arr);
      var p = join(slot.path, arr.length - 1);
      S.open[p] = true;
      S.focus = p;
      changed(); redraw();
    })));
    return list;
  }

  function mapControl(s, slot) {
    var vs = resolve(s.additionalProperties);
    var box = h('div.cf-list');
    var v = slot.get() || {};
    var simple = !nestedKind(kind(vs)) && kind(vs) !== 'json';
    Object.keys(v).forEach(function (key) {
      var keyInput = h('input.input.input--sm.cf-key', { value: key, 'aria-label': t('cf.name') });
      keyInput.addEventListener('change', function () {
        var nk = keyInput.value;
        if (!nk || nk === key || nk in v) { keyInput.value = key; return; }
        var o = {};
        Object.keys(v).forEach(function (k) { o[k === key ? nk : k] = v[k]; });
        slot.set(o);
        changed(); redraw();
      });
      var cs = child(slot, key, join(slot.path, key, true));
      var rm = smallBtn('', 'trash', function () {
        var o = slot.get();
        delete o[key];
        if (Object.keys(o).length === 0) slot.set(undefined);
        changed(); redraw();
      }, 'ghost icon', t('cf.remove'));
      if (simple) box.appendChild(h('div.cf-row' + (hasError(cs.path) ? '.is-error' : ''), { 'data-path': cs.path }, keyInput, control(vs, cs), rm));
      else box.appendChild(h('div.cf-mapitem' + (hasError(cs.path) ? '.is-error' : ''), { 'data-path': cs.path }, h('div.cf-row', keyInput, h('span.cf-spacer'), rm), control(vs, cs, true)));
    });
    box.appendChild(h('div.btn-row', { style: { marginTop: '8px' } }, smallBtn(t('cf.add'), 'plus', function () {
      var o = slot.get() || {};
      var k = 'name', n = 1;
      while (k in o) k = 'name' + ++n;
      o[k] = emptyOf(vs);
      slot.set(o);
      changed(); redraw();
    })));
    return box;
  }

  function moduleControl(s, slot) {
    var v = slot.get();
    var box = h('div');
    var sel = h('select.select.select--sm', { 'aria-label': 'module', style: { maxWidth: '320px' } }, h('option', { value: '' }, t('cf.leftout')),
      s.properties.module.enum.map(function (m) { return h('option', { value: m, selected: v && v.module === m }, m); }));
    sel.addEventListener('change', function () {
      slot.set(sel.value ? { module: sel.value } : undefined);
      changed(); redraw();
    });
    var mpath = join(slot.path, 'module');
    box.appendChild(h('div.cf-field' + (hasError(mpath) ? '.is-error' : ''), { 'data-path': mpath },
      h('div.cf-name', h('span.cf-label', t('cf.module'))), h('div.cf-ctl', sel),
      s.properties.module.description ? h('div.cf-doc', s.properties.module.description) : null));
    if (v && v.module) {
      var ps = paramsSchema(s, v.module);
      var pslot = child(slot, 'params', join(slot.path, 'params'));
      if (ps && ps.properties && Object.keys(ps.properties).length > 0) {
        box.appendChild(h('div.cf-params', { 'data-path': pslot.path }, h('div.muted.small', t('cf.params')), ps.description ? h('div.cf-doc', ps.description) : null, objectControl(ps, pslot, true)));
      } else if (!ps) {
        box.appendChild(h('div.cf-field', { 'data-path': pslot.path }, h('div.cf-name', h('span.cf-label', t('cf.params'))), h('div.cf-ctl', jsonControl({}, pslot))));
      }
    }
    return box;
  }

  function boolControl(s, slot) {
    var c = h('input', { type: 'checkbox', checked: slot.get() === true });
    c.addEventListener('change', function () {
      // Left out when it is the default. / 等于默认值时不设置。
      slot.set(c.checked ? (s.default === true ? undefined : true) : (s.default === true ? false : undefined));
      changed();
    });
    if (s.default === true && slot.get() === undefined) c.checked = true;
    return h('label.check', c, h('span.muted.small', s.default !== undefined ? t('cf.default', { v: String(s.default) }) : ''));
  }

  function scalarControl(s, slot) {
    var v = slot.get();
    var ph = s.default !== undefined ? String(typeof s.default === 'object' ? JSON.stringify(s.default) : s.default) : '';
    if (s.enum) {
      var sel = h('select.select.select--sm', h('option', { value: '' }, ph ? t('cf.leftout.d', { v: ph }) : t('cf.leftout')),
        s.enum.map(function (e) { return h('option', { value: String(e), selected: v === e }, String(e)); }));
      sel.addEventListener('change', function () {
        var e = s.enum.filter(function (x) { return String(x) === sel.value; })[0];
        slot.set(sel.value === '' ? undefined : e);
        changed();
      });
      return sel;
    }
    var numeric = s.type === 'integer' || s.type === 'number';
    var input = h('input.input.input--sm', {
      type: numeric ? 'number' : 'text',
      value: v === undefined ? '' : typeof v === 'object' ? JSON.stringify(v) : String(v),
      placeholder: ph,
      step: s.type === 'integer' ? '1' : numeric ? 'any' : null,
      min: s.minimum, max: s.maximum,
      pattern: s.pattern && !numeric ? s.pattern.replace(/^\^/, '').replace(/\$$/, '') : null,
      spellcheck: 'false'
    });
    input.addEventListener('input', function () {
      var x = input.value, nv;
      if (x === '') nv = undefined;
      else if (numeric) nv = Number(x);
      else if (s.oneOf && /^-?\d+$/.test(x) && s.oneOf.some(function (o) { return o.type === 'integer'; })) nv = Number(x);
      else nv = x;
      slot.set(nv);
      changed();
    });
    return input;
  }

  function jsonControl(s, slot) {
    var v = slot.get();
    var ta = h('textarea.textarea.textarea--code', { spellcheck: 'false', placeholder: t('cf.json.ph'), rows: 4 });
    ta.value = v === undefined ? '' : JSON.stringify(v, null, 2);
    ta.addEventListener('input', function () {
      if (ta.value.trim() === '') { slot.set(undefined); ta.setCustomValidity(''); changed(); return; }
      try { slot.set(JSON.parse(ta.value)); ta.setCustomValidity(''); changed(); } catch (e) { ta.setCustomValidity(e.message); }
    });
    return ta;
  }

  // section returns the form of a top-level member. / 返回顶层成员的表单。
  function section(name, opts) {
    opts = opts || {};
    var s = resolve(rootProps()[name] || {});
    var slot = child(rootSlot(), name, name);
    var body;
    if (kind(s) === 'array') {
      var o = {};
      Object.keys(opts).forEach(function (k) { o[k] = opts[k]; });
      o.top = true;
      body = h('div', { 'data-path': name }, arrayControl(s, slot, o));
    } else {
      body = h('section.card', { 'data-path': name }, h('div.card__body', control(s, slot, true)));
    }
    return h('div.stack', opts.noDoc || !s.description ? null : h('div.callout', UI.icon('info'), ' ', s.description), body);
  }

  // certTable lists the certificates of /status, for the overview and the TLS pages. / 列出 /status 中的证书，供概览与 TLS 页面使用。
  function certTable(UI, list) {
    return UI.table([
      { label: t('cf.c.listener'), render: function (c) { return h('b', c.listener); } },
      { label: t('cf.c.names'), render: function (c) { return (c.names || []).map(function (n) { return UI.badge(n); }); } },
      { label: t('cf.c.issuer'), render: function (c) { return h('span.small', c.issuer); } },
      { label: t('cf.c.from'), render: function (c) { return h('span.small', UI.fmtTime(Date.parse(c.not_before) / 1000)); } },
      { label: t('cf.c.until'), render: function (c) {
        var left = (Date.parse(c.not_after) - Date.now()) / 86400e3;
        return h('span.small', UI.fmtTime(Date.parse(c.not_after) / 1000), ' ',
          UI.badge(left < 0 ? t('cf.c.expired') : t('cf.c.left', { n: Math.floor(left) }), left < 0 ? 'danger' : left < 14 ? 'warn' : 'ok'));
      } },
      { label: t('cf.c.serial'), render: function (c) { return h('code.small', c.serial); } }
    ], list, { empty: t('cf.c.none') });
  }

  window.FitConfig = {
    with: function (ui) {
      if (!S.ui) S.ui = ui;
      return api;
    },
    certTable: certTable
  };
  var api = {
    ensure: function () { return ensure(); },
    page: page, section: section, redraw: redraw, changed: changed, save: save, discard: discard, reload: reload, rollback: rollback,
    place: function (fn) { S.places.push(fn); },
    sections: function () { return Object.keys(rootProps()); },
    describe: function (name) { return resolve(rootProps()[name] || {}).description || ''; },
    resolve: function (s) { return resolve(s); },
    schemaDef: function (name) { return S.schema && S.schema.$defs[name]; },
    get draft() { return S.draft; },
    set draft(v) { S.draft = v; },
    get version() { return S.version; },
    get focus() { return S.focus; },
    get etag() { return S.etag; },
    get dirty() { return S.dirty || S.rawText != null; },
    // the JSON page's text that does not parse (null when it does). / JSON 页面中无法解析的文本（可解析时为 null）。
    get rawText() { return S.rawText; },
    setRaw: function (text, err) { S.rawText = err ? text : null; S.rawError = err || ''; if (!err) S.errors = S.errors.filter(function (e) { return e.path; }); setDirty(S.dirty); },
    notice: function () { return paintNotice(); },
    errors: function () { return S.errors; }
  };
})();
