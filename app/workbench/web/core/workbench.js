// The workbench: a login, then a menu on the left, a tree of groups (the gateway, the tunnel, the LLM feature,
// the system), their modules and the modules' pages, and the page picked on the right. A module is shown when the
// console serves its server module (GET /api/workbench lists them): admin.console serves the config's own and every
// role's running beside it, a role's own console only that role's. Needs ui.js.
//
// A module registers itself with WB.module(def):
//   id      the module's ID, the first part of its pages' URLs: #/<id>/<route>
//   server  the server module whose API it calls, /api/m/<server>/; id when left out
//   group   the group it goes under: gateway, tunnel, llm or system
//   order   its place in the group
//   icon, label (a key of its words, or of UI's)
//   words   [[key, en, zh], ...], or i18n {en: {key: text}, 'zh-CN': {...}}: its words, which its UI.t looks up first
//   nav     [{id, icon, label (a key)}, ...]: its pages in the menu; one page makes the module a leaf
//   setup(UI, sh) → {routes: {'route/:param': fn(params)}, fallback} or a Promise of it. Called once, on the
//           module's first visit. UI is the library scoped to the module; sh.page(navId, title, crumb) marks the
//           menu item, sets the title and returns the emptied content element, and sh.tools is the top bar's tools.
//
// workbench：先登录，左侧为菜单树（网关、隧道、LLM、系统四组，组下为模块，模块下为页面），右侧为所选页面。控制台提供某模块的
// 服务端模块时（GET /api/workbench 列出）才显示该模块：admin.console 提供配置本身的模块与同进程运行的每个角色的模块，角色自己的
// 控制台只提供该角色的。模块用 WB.module(def) 注册，字段见上。
(function () {
  'use strict';
  var h = UI.h, t = UI.t;

  UI.addI18n({
    'en': {
      'wb.sub': 'Workbench', 'wb.filter': 'Filter the menu', 'wb.fold': 'Collapse the menu', 'wb.none': 'Nothing here',
      'wb.empty': 'This console serves no module: nothing runs that has pages.',
      'g.gateway': 'Gateway', 'g.tunnel': 'Tunnel', 'g.llm': 'LLM', 'g.system': 'System', 'g.other': 'Other'
    },
    'zh-CN': {
      'wb.sub': '工作台', 'wb.filter': '筛选菜单', 'wb.fold': '收起菜单', 'wb.none': '没有匹配项',
      'wb.empty': '此控制台没有提供任何模块：没有带页面的功能在运行。',
      'g.gateway': '网关', 'g.tunnel': '隧道', 'g.llm': 'LLM', 'g.system': '系统', 'g.other': '其他'
    }
  });

  var GROUPS = [['gateway', 'globe'], ['tunnel', 'swap'], ['llm', 'cpu'], ['system', 'settings']];
  var defs = [];
  var OPEN_KEY = 'fit_wb_open', MINI_KEY = 'fit_wb_mini';

  function load(k) { try { return localStorage.getItem(k); } catch (e) { return null; } }
  function save(k, v) { try { localStorage.setItem(k, v); } catch (e) { /* ignore / 忽略 */ } }

  var refresh = function () { return Promise.resolve(); };
  window.WB = {
    // refresh reads the modules served again and rebuilds the menu when they changed, as after a config that starts
    // or stops a role. / 重新读取所提供的模块，变化时重建菜单（如启动或停止角色的配置之后）。
    refresh: function () { return refresh(); },
    module: function (def) {
      def.server = def.server || def.id;
      def.order = def.order || 0;
      defs.push(def);
      if (def.i18n) UI.addI18n(def.i18n, def.id);
      if (def.words) {
        var en = {}, zh = {};
        def.words.forEach(function (w) { en[w[0]] = w[1]; zh[w[0]] = w[2]; });
        UI.addI18n({ 'en': en, 'zh-CN': zh }, def.id);
      }
    }
  };

  // The state of the page shown. / 当前页面的状态。
  var mods = [], byId = {}, open = {}, items = {}, nodes = {}, current = null;
  var root, titleEl, crumbEl, toolsEl, content, navEl, filterEl;

  try { open = JSON.parse(load(OPEN_KEY) || '{}') || {}; } catch (e) { open = {}; }

  // label(m, key) is a module's word. / 模块的词。
  function word(m, key) { return m.ui ? m.ui.t(key) : UI.t(key); }

  function setOpen(key, on, el) {
    open[key] = on;
    if (el) el.classList.toggle('is-open', on);
    save(OPEN_KEY, JSON.stringify(open));
  }

  // tree builds the menu: groups → modules → pages. / 构建菜单树：组 → 模块 → 页面。
  function tree() {
    var out = [];
    var groups = GROUPS.slice();
    mods.forEach(function (m) { if (!groups.some(function (g) { return g[0] === m.group; })) groups.push([m.group, 'cube']); });
    groups.forEach(function (g) {
      var ms = mods.filter(function (m) { return m.group === g[0]; }).sort(function (a, b) { return a.order - b.order; });
      if (!ms.length) return;
      var gkey = 'g:' + g[0];
      var kids = h('div.tree__kids');
      ms.forEach(function (m) {
        if (m.nav.length === 1) {
          kids.appendChild(leaf(m, m.nav[0], word(m, m.label), m.icon, 1));
          return;
        }
        var mkey = 'm:' + m.id;
        var mk = h('div.tree__kids');
        m.nav.forEach(function (n) { mk.appendChild(leaf(m, n, word(m, n.label), n.icon, 2)); });
        var node = h('div.tree__node' + (open[mkey] !== false ? '.is-open' : ''), { 'data-key': mkey },
          h('a.nav__item.tree__head.tree__d1', { href: '#/' + m.id, onclick: function (e) { e.preventDefault(); toggle(node, mkey); } },
            UI.icon(m.icon), h('span.ellipsis', word(m, m.label)), h('span.tree__chev', UI.icon('chev'))), mk);
        nodes[m.id] = node;
        kids.appendChild(node);
      });
      var gnode = h('div.tree__node.tree__group' + (open[gkey] !== false ? '.is-open' : ''), { 'data-key': gkey },
        h('a.nav__item.tree__head', { href: '#', onclick: function (e) { e.preventDefault(); toggle(gnode, gkey); } },
          UI.icon(g[1]), h('span.ellipsis', t('g.' + g[0])), h('span.tree__chev', UI.icon('chev'))), kids);
      out.push(gnode);
    });
    return out;
  }
  function toggle(node, key) { setOpen(key, !node.classList.contains('is-open'), node); }
  function leaf(m, n, label, iconName, depth) {
    var a = h('a.nav__item.tree__leaf.tree__d' + depth, { href: '#/' + m.id + '/' + n.id, title: label }, UI.icon(iconName || n.icon), h('span.ellipsis', label));
    items[m.id + '/' + n.id] = a;
    return a;
  }

  // filter hides the menu items whose labels lack q. / 隐藏标签不含 q 的菜单项。
  function filter(q) {
    q = q.trim().toLowerCase();
    navEl.querySelectorAll('.tree__node').forEach(function (n) { n.classList.toggle('is-filtered', !!q); });
    navEl.querySelectorAll('.tree__leaf').forEach(function (a) { a.hidden = !!q && a.textContent.toLowerCase().indexOf(q) < 0; });
    navEl.querySelectorAll('.tree__node').forEach(function (n) {
      var head = n.querySelector(':scope > .tree__head');
      var hit = head.textContent.toLowerCase().indexOf(q) >= 0;
      if (hit && q) n.querySelectorAll('.tree__leaf').forEach(function (a) { a.hidden = false; });
      n.hidden = !!q && !hit && !n.querySelector('.tree__leaf:not([hidden])');
    });
  }

  // The shell a module's routes draw into. / 模块路由绘制所用的外框。
  function shellFor(m) {
    return {
      tools: toolsEl,
      page: function (navId, title, crumb) {
        Object.keys(items).forEach(function (k) { items[k].classList.toggle('is-active', k === m.id + '/' + navId); });
        Object.keys(nodes).forEach(function (k) { nodes[k].classList.toggle('has-active', k === m.id); });
        var mk = 'm:' + m.id, gk = 'g:' + m.group;
        if (nodes[m.id] && !nodes[m.id].classList.contains('is-open')) setOpen(mk, true, nodes[m.id]);
        var g = navEl.querySelector('[data-key="' + gk + '"]');
        if (g && !g.classList.contains('is-open')) setOpen(gk, true, g);
        var where = [t('g.' + m.group)];
        if (m.nav.length > 1) where.push(word(m, m.label));
        titleEl.textContent = title || '';
        crumbEl.textContent = crumb || where.join(' / ');
        document.title = (title ? title + ' · ' : '') + 'fit';
        UI.clear(toolsEl);
        return UI.clear(content);
      }
    };
  }

  function setup(m) {
    if (!m.ready) {
      m.ready = Promise.resolve().then(function () { return m.setup(m.ui, shellFor(m)); }).then(function (r) {
        m.routes = (r && r.routes) || {};
        m.fallback = (r && r.fallback) || (m.nav[0] && m.nav[0].id) || '';
      });
      m.ready.catch(function () { m.ready = null; });
    }
    return m.ready;
  }

  function match(routes, path) {
    var parts = path.split('/').filter(Boolean);
    var keys = Object.keys(routes);
    for (var i = 0; i < keys.length; i++) {
      var pat = keys[i].split('/').filter(Boolean), params = {}, ok = pat.length === parts.length;
      for (var j = 0; ok && j < pat.length; j++) {
        if (pat[j][0] === ':') params[pat[j].slice(1)] = decodeURIComponent(parts[j]);
        else if (pat[j] !== parts[j]) ok = false;
      }
      if (ok) return function () { return routes[keys[i]](params); };
    }
    return null;
  }

  // route shows the page the URL names: #/<module>/<route>. / 显示 URL 所指的页面。
  function route() {
    UI.stopTimers();
    UI.closeModals();
    root.classList.remove('nav-open');
    var path = location.hash.replace(/^#\/?/, '').split('?')[0];
    var slash = path.indexOf('/');
    var id = slash < 0 ? path : path.slice(0, slash), rest = slash < 0 ? '' : path.slice(slash + 1);
    var m = byId[id];
    if (!m) {
      if (mods.length) location.replace('#/' + mods[0].id + '/' + (mods[0].nav[0] ? mods[0].nav[0].id : ''));
      return;
    }
    var seq = current = {};
    setup(m).then(function () {
      if (seq !== current) return;
      var run = match(m.routes, rest);
      if (!run) { location.replace('#/' + m.id + '/' + m.fallback); return; }
      run();
    }).catch(UI.fail);
  }

  function build(wb, logout) {
    var served = {};
    (wb.modules || []).forEach(function (id) { served[id] = true; });
    mods = defs.filter(function (d) { return served[d.server]; }).map(function (d) {
      var m = Object.create(d);
      m.ui = UI.scope(d.server, d.id);
      return m;
    });
    var groupOrder = GROUPS.map(function (g) { return g[0]; });
    mods.sort(function (a, b) {
      var ga = groupOrder.indexOf(a.group), gb = groupOrder.indexOf(b.group);
      return (ga < 0 ? 99 : ga) - (gb < 0 ? 99 : gb) || a.order - b.order;
    });
    byId = {}; items = {}; nodes = {};
    mods.forEach(function (m) { byId[m.id] = m; });

    titleEl = h('div.topbar__title'); crumbEl = h('div.topbar__crumb'); toolsEl = h('div.row'); content = h('main.content');
    filterEl = h('input.input.input--sm.tree__filter', { type: 'search', placeholder: t('wb.filter'), 'aria-label': t('wb.filter'), oninput: function () { filter(filterEl.value); } });
    navEl = h('nav.nav.tree', tree());
    var fold = h('button.btn.btn--ghost.btn--icon.btn--sm.wb-fold', { type: 'button', title: t('wb.fold'), 'aria-label': t('wb.fold'), onclick: function () {
      root.classList.toggle('is-mini'); save(MINI_KEY, root.classList.contains('is-mini') ? '1' : '0');
    } }, UI.icon('fold'));
    var foot = h('div.sidebar__foot', h('div.row', UI.icon('users'), h('span.ellipsis', wb.user || '')),
      h('a.nav__item', { href: '#', onclick: function (e) { e.preventDefault(); logout(); } }, UI.icon('logout'), h('span', t('logout'))));
    var side = h('aside.sidebar', h('div.brand', h('div.brand__logo', UI.icon('logo')), h('div.brand__text', h('div.brand__name', 'fit'), h('div.brand__sub', t('wb.sub'))), fold),
      filterEl, navEl, foot);
    var menu = h('button.btn.btn--ghost.btn--icon.menu-btn', { type: 'button', 'aria-label': t('menu'), onclick: function () { root.classList.toggle('nav-open'); } }, UI.icon('menu'));
    var top = h('header.topbar', menu, h('div', titleEl, crumbEl), h('div.topbar__spacer'), toolsEl, UI.prefs());
    root = h('div.shell' + (load(MINI_KEY) === '1' ? '.is-mini' : ''), side, h('div.main', top, content));
    if (!mods.length) UI.mount(content, UI.empty(t('wb.empty')));
    return root;
  }

  document.addEventListener('DOMContentLoaded', function () {
    var app = document.getElementById('app');
    var started = false;
    function showLogin() {
      UI.stopTimers();
      UI.closeModals();
      UI.loginPage(app, { brand: 'fit', sub: t('wb.sub'), username: 'admin', onSubmit: function (u, p) { return UI.login(u, p).then(run); } });
    }
    function logout() { UI.logout().then(showLogin, showLogin); }
    var served = '';
    function run() {
      return UI.workbench().then(function (wb) {
        served = (wb.modules || []).join(',');
        UI.mount(app, build(wb, logout));
        if (!started) { window.addEventListener('hashchange', route); started = true; }
        route();
      });
    }
    refresh = function () {
      return UI.workbench().then(function (wb) {
        if ((wb.modules || []).join(',') === served) return;
        var keep = mods.filter(function (m) { return m.ready; });
        served = (wb.modules || []).join(',');
        UI.mount(app, build(wb, logout));
        // A module set up before keeps its setup; the page shown is drawn again. / 已初始化的模块保留其初始化结果；当前页面重新绘制。
        keep.forEach(function (k) { if (byId[k.id]) byId[k.id] = k; });
        mods = mods.map(function (m) { return byId[m.id]; });
        route();
      });
    };
    UI.setUnauthorized(showLogin);
    UI.session().then(run).catch(function (e) { if (e && e.status === 401) showLogin(); else UI.fail(e); });
  });
})();
