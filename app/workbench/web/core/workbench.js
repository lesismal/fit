// The workbench: a login, then a menu on the left whose first level is the modules themselves — the gateway's global
// pages, the account pages, the protocol and feature modules, the tunnel's and the LLM feature's roles — each module's
// pages as its second level, and the page picked on the right. A module is shown when the console serves its server
// module (GET /api/workbench lists them): admin.console serves the config's own and every role's running beside it, a
// role's own console only that role's. Needs ui.js.
//
// A module registers itself with WB.module(def):
//   id      the module's ID, the first part of its pages' URLs: #/<id>/<route>
//   server  the server module whose API it calls, /api/m/<server>/; id when left out
//   group   where it sorts in the menu when RANK does not name its id: tunnel or llm sort their roles by order,
//           anything else sorts last
//   order   its place in its group (the roles of a group sort by it; RANK names the menu's first modules instead)
//   icon, label (a key of its words, or of UI's)
//   tag     a key of its words: the small tag under its menu label (a module may also be named in TAGS instead)
//   words   [[key, en, zh], ...], or i18n {en: {key: text}, 'zh-CN': {...}}: its words, which its UI.t looks up first
//   nav     its pages in the menu: [{id, icon, label (a key)}, ...], one page making the module a leaf; or a function
//           returning such an array, called again whenever the menu is rebuilt (WB.refreshMenu), whose labels, tags
//           and kids are text already (the module words them itself). A nav item may instead be an external link
//           ({label, href, icon?}) to a page of another module, or a collapsible node ({id, label, tag?, kids:
//           [{label, href?, icon?, count?, title?}]}) whose children are third-level links (a kid without href is a
//           muted row). A module with one page, or one external link, is a leaf.
//   setup(UI, sh) → {routes: {'route/:param': fn(params)}, fallback} or a Promise of it. Called once, on the
//           module's first visit. UI is the library scoped to the module; sh.page(navId, title, crumb) marks the
//           menu item, sets the title and returns the emptied content element, and sh.tools is the top bar's tools.
//   boot(UI)  optional: called once after the shell is built; its promise may do async work and then WB.refreshMenu.
//
// workbench：先登录，左侧菜单的第一级即各模块（网关的全局页、账户页面、各协议与功能模块、隧道与 LLM 各角色），第二级为模块的
// 页面，右侧为所选页面。控制台提供某服务端模块时（GET /api/workbench 列出）才显示该模块：admin.console 提供配置本身的
// 模块与同进程运行的每个角色的模块，角色自己的控制台只提供该角色的。模块用 WB.module(def) 注册，字段见上。
// nav 也可为函数（菜单重建时再次调用，其标签为模块自己取好的文字）；条目可为指向其他模块页面的外链，或带 kids 的可折叠节点，
// 其子项为第三级链接。boot 在外框构建完成后调用一次。
(function () {
  'use strict';
  var h = UI.h, t = UI.t;

  UI.addI18n({
    'en': {
      'wb.sub': 'Workbench', 'wb.filter': 'Filter the menu', 'wb.fold': 'Collapse the menu', 'wb.none': 'Nothing here',
      'wb.empty': 'This console serves no module: nothing runs that has pages.',
      'wb.global': 'Global',
      'tag.http': 'HTTP', 'tag.connect': 'HTTP CONNECT', 'tag.tcpudp': 'TCP/UDP', 'tag.tls': 'TLS',
      'tag.streaming': 'RTMP / RTSP / SRT', 'tag.mediaauth': 'media tokens', 'tag.webrtc': 'WebRTC',
      'tag.tunnel': 'Tunnel', 'tag.llm': 'LLM',
      'dp.title': 'Change the default password',
      'dp.note': 'You signed in as {user} with the default password fit run -admin set. It is temporary: change it now, before anything else. Until it is changed, the console does nothing else.',
      'dp.left': 'The default password stops working in {left}.',
      'dp.at': 'It stops working at {at}.',
      'dp.current': 'Current (default) password', 'dp.new': 'New password', 'dp.confirm': 'Confirm the new password',
      'dp.hint': 'At most 72 bytes (bcrypt\'s limit).',
      'dp.need': 'Enter the current password and the new one.',
      'dp.mismatch': 'The new passwords do not match.',
      'dp.same': 'The new password must differ from the current one.',
      'dp.submit': 'Change password and continue', 'dp.done': 'Password changed for {user}.',
      'dp.expired': 'The default password has expired and no longer works. Restart fit with -admin to get a new one, then sign in and change it in time.'
    },
    'zh-CN': {
      'wb.sub': '工作台', 'wb.filter': '筛选菜单', 'wb.fold': '收起菜单', 'wb.none': '没有匹配项',
      'wb.empty': '此控制台没有提供任何模块：没有带页面的功能在运行。',
      'wb.global': '全局配置',
      'tag.http': 'HTTP', 'tag.connect': 'HTTP CONNECT', 'tag.tcpudp': 'TCP/UDP', 'tag.tls': 'TLS',
      'tag.streaming': 'RTMP / RTSP / SRT', 'tag.mediaauth': '媒体令牌', 'tag.webrtc': 'WebRTC',
      'tag.tunnel': '隧道', 'tag.llm': 'LLM',
      'dp.title': '修改默认密码',
      'dp.note': '你以 {user} 身份使用 fit run -admin 设置的默认密码登录。该密码是临时的：请先修改它。修改之前，控制台不能进行其他操作。',
      'dp.left': '默认密码将在 {left} 后失效。',
      'dp.at': '失效时间：{at}。',
      'dp.current': '当前（默认）密码', 'dp.new': '新密码', 'dp.confirm': '确认新密码',
      'dp.hint': '最多 72 字节（bcrypt 的限制）。',
      'dp.need': '请输入当前密码和新密码。',
      'dp.mismatch': '两次输入的新密码不一致。',
      'dp.same': '新密码必须与当前密码不同。',
      'dp.submit': '修改密码并继续', 'dp.done': '{user} 的密码已修改。',
      'dp.expired': '默认密码已过期，不能再使用。请以 -admin 重新启动 fit 获取新的默认密码，然后登录并及时修改。'
    }
  });

  // RANK is the menu's order: the global/common pages first, then the account pages, then the protocol and feature
  // modules. The modules it does not name sort after those by group and order, so a role keeps its place without
  // renumbering. / RANK 即菜单顺序：先全局/常用页，再账户页面，再各协议与功能模块。未列出的模块按组与 order 排在后面，
  // 角色因此无需改 order。
  var RANK = ['gw.overview', 'gw.listeners', 'engine', 'sys.config', 'sys.json', 'sys.versions', 'sys.check', 'account',
    'gw.reverse', 'gw.forward', 'gw.l4', 'gw.tls', 'gw.cache', 'gw.streaming', 'gw.mediaauth', 'gw.webrtc', 'gw.tunnel', 'gw.llm'];
  // TAGS are the small muted protocol or feature tags under a module's label. / 模块标签下方的小号协议或功能标记。
  var TAGS = { 'gw.reverse': 'tag.http', 'gw.forward': 'tag.connect', 'gw.l4': 'tag.tcpudp', 'gw.tls': 'tag.tls', 'gw.cache': 'tag.http',
    'gw.streaming': 'tag.streaming', 'gw.mediaauth': 'tag.mediaauth', 'gw.webrtc': 'tag.webrtc', 'gw.tunnel': 'tag.tunnel', 'gw.llm': 'tag.llm' };
  // LABELS are the names the menu words itself (sys.config's own word still names its pages). / 菜单自己取词的名称。
  var LABELS = { 'sys.config': 'wb.global' };
  var defs = [];
  var OPEN_KEY = 'fit_wb_open', MINI_KEY = 'fit_wb_mini';

  function load(k) { try { return localStorage.getItem(k); } catch (e) { return null; } }
  function save(k, v) { try { localStorage.setItem(k, v); } catch (e) { /* ignore / 忽略 */ } }

  var refresh = function () { return Promise.resolve(); };
  window.WB = {
    // refresh reads the modules served again and rebuilds the menu when they changed, as after a config that starts
    // or stops a role. / 重新读取所提供的模块，变化时重建菜单（如启动或停止角色的配置之后）。
    refresh: function () { return refresh(); },
    // refreshMenu calls every dynamic module's nav again and rebuilds only the sidebar, keeping the page shown and
    // its open state and filter; the Engine menu follows a config change with it. / 重新调用各动态模块的 nav，只重建侧栏
    // （保留当前页面、展开状态与筛选）；引擎菜单据此跟随配置变化。
    refreshMenu: function () {
      if (!navEl) return;
      try {
        mods.forEach(function (m) { if (m.dyn) m.nav = navOf(m.def); });
        rebuildTree();
      } catch (e) { UI.fail(e); }
    },
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
  var mods = [], byId = {}, open = {}, items = {}, nodes = {}, ports = {}, current = null;
  var shownPage = { module: '', navId: '' };
  // locked: the session must change its default password first, so no page is drawn. / 须先修改默认密码，不绘制任何页面。
  var locked = false;
  var root, titleEl, crumbEl, toolsEl, content, navEl, filterEl;

  try { open = JSON.parse(load(OPEN_KEY) || '{}') || {}; } catch (e) { open = {}; }

  // word(m, key) is a module's word. / 模块的词。
  function word(m, key) { return m.ui ? m.ui.t(key) : UI.t(key); }
  // labelOf is the menu's name for a module: its own word, or the menu's for the few it renames.
  // labelOf 返回模块在菜单中的名称：通常是模块自己的词，少数由菜单改名。
  function labelOf(m) { return LABELS[m.id] ? t(LABELS[m.id]) : word(m, m.label); }
  // rank is a module's place in the flat menu. / 模块在扁平菜单中的位置。
  function rank(m) {
    var i = RANK.indexOf(m.id);
    if (i >= 0) return i;
    return (m.group === 'tunnel' ? 100 : m.group === 'llm' ? 200 : 300) + (m.order || 0);
  }
  // navOf resolves a module's nav: an array, or a function that is called again on every rebuild.
  // navOf 解析模块的 nav：数组，或每次重建时再次调用的函数。
  function navOf(d) {
    try { return typeof d.nav === 'function' ? (d.nav() || []) : (d.nav || []); } catch (e) { return []; }
  }
  // navLabel is a nav item's text: a module's word, or, in a dynamic nav, the text the module wrote itself.
  // navLabel 返回导航条目的文字：静态 nav 取模块的词条，动态 nav 用模块自己写好的文字。
  function navLabel(m, s) { return m.dyn ? String(s == null ? '' : s) : word(m, s); }
  // tagOf is the small tag under a module's label: its own word, or the menu's.
  // tagOf 返回模块标签下方的小标记：模块自己的词，或菜单的词。
  function tagOf(m) { return m.tag ? word(m, m.tag) : TAGS[m.id] ? t(TAGS[m.id]) : null; }
  // text is a module's (or a port's) label row: the label, and the tag under it when it has one, so that the labels
  // of the modules whose tags are long still read in full. / 标签行：标签，若有小标记则小字置于其下，长标记不会截断标签。
  function text(m, label, tag) {
    return h('div.tree__text', h('span.ellipsis', label), tag ? h('span.tree__tag', tag) : null);
  }

  function setOpen(key, on, el) {
    open[key] = on;
    if (el) {
      el.classList.toggle('is-open', on);
      var head = el.querySelector(':scope > .tree__head');
      if (head) head.setAttribute('aria-expanded', on ? 'true' : 'false');
    }
    save(OPEN_KEY, JSON.stringify(open));
  }

  // tree builds the menu: modules, and the pages of each module with several of them under it. A module with one
  // page or one external link is a direct link; a nav item with kids is a collapsible node whose children are the
  // third level. / 构建菜单：一级为模块，多页模块的页面在其下（缩进）；只有一页或一个外链的模块直接链接；
  // 带 kids 的条目为可折叠节点，其子项为第三级。
  function tree() {
    return mods.map(function (m) {
      var label = labelOf(m), tag = tagOf(m);
      var one = m.nav.length === 1 ? m.nav[0] : null;
      // A module of one external link is itself that link. It is not marked active: the page it opens belongs to
      // another module, whose own entry is what marks it. / 只有一个外链的模块本身即该外链；它不算作活动项：它打开的页面
      // 属于其他模块，由那个模块的条目标记。
      if (one && one.href && !one.kids) {
        return h('a.nav__item.tree__leaf.tree__mod', { href: one.href, title: navLabel(m, one.title || one.label) },
          UI.icon(m.icon || one.icon), text(m, label, tag));
      }
      // A module of one page is a page of its own. / 只有一页的模块直接链接到该页。
      if (one && one.id && !one.kids && !one.href) {
        var a = h('a.nav__item.tree__leaf.tree__mod', { href: '#/' + m.id + '/' + one.id, title: label },
          UI.icon(m.icon || one.icon), text(m, label, tag));
        items[m.id + '/' + one.id] = a;
        return a;
      }
      var mkey = 'm:' + m.id, isOpen = open[mkey] !== false;
      var kids = h('div.tree__kids');
      m.nav.forEach(function (p) { kids.appendChild(item(m, p)); });
      var node = h('div.tree__node' + (isOpen ? '.is-open' : ''), { 'data-key': mkey },
        h('a.nav__item.tree__head', {
          href: '#/' + m.id, title: label, 'aria-expanded': isOpen ? 'true' : 'false',
          onclick: function (e) { e.preventDefault(); toggle(node, mkey); }
        }, UI.icon(m.icon), text(m, label, tag), h('span.tree__chev', UI.icon('chev'))), kids);
      nodes[m.id] = node;
      return node;
    });
  }
  // item is a module's child in the menu: a page, an external link, a collapsible node (a port), or a muted row.
  // item 是模块在菜单中的子项：页面、外链、可折叠节点（端口）或无链接的灰色行。
  function item(m, n) {
    if (n.kids) return portNode(m, n);
    if (n.href) return extLink(m, n);
    if (n.id) return leaf(m, n);
    return h('span.nav__item.tree__leaf.tree__d3.tree__kid--muted', { title: navLabel(m, n.label) },
      n.icon ? UI.icon(n.icon) : null, h('span.ellipsis', navLabel(m, n.label)));
  }
  function toggle(node, key) { setOpen(key, !node.classList.contains('is-open'), node); }
  function leaf(m, n) {
    var label = navLabel(m, n.label);
    var a = h('a.nav__item.tree__leaf.tree__d2', { href: '#/' + m.id + '/' + n.id, title: label }, UI.icon(n.icon), h('span.ellipsis', label));
    items[m.id + '/' + n.id] = a;
    return a;
  }
  function extLink(m, n) {
    var label = navLabel(m, n.label);
    return h('a.nav__item.tree__leaf.tree__d2.tree__link', { href: n.href, title: navLabel(m, n.title || n.label), 'data-href': n.href },
      n.icon ? UI.icon(n.icon) : null, h('span.ellipsis', label));
  }
  // portNode is a collapsible node (one listener port): its head toggles it, its kids are the third level; a port is
  // collapsed until it is opened. / portNode 是可折叠节点（一个监听端口）：头部切换展开，子项为第三级；端口默认收起。
  function portNode(m, n) {
    var pkey = 'p:' + m.id + '/' + n.id, isOpen = open[pkey] === true;
    var kids = h('div.tree__kids');
    (n.kids || []).forEach(function (k) { kids.appendChild(kid(m, k)); });
    var node = h('div.tree__node.tree__port' + (isOpen ? '.is-open' : ''), { 'data-key': pkey },
      h('a.nav__item.tree__head.tree__d2.tree__port__head', {
        href: '#', title: navLabel(m, n.title || n.label), 'aria-expanded': isOpen ? 'true' : 'false',
        onclick: function (e) { e.preventDefault(); toggle(node, pkey); }
      }, UI.icon(n.icon || 'listen'), text(m, navLabel(m, n.label), navLabel(m, n.tag)), h('span.tree__chev', UI.icon('chev'))), kids);
    ports[pkey] = node;
    return node;
  }
  // kid is a port's feature: a link to the page that edits it, with its count, or a muted row without a page.
  // kid 是端口的一项功能：指向编辑其的页面的链接（带数量），或无页面的灰色行。
  function kid(m, k) {
    var label = navLabel(m, k.label), title = navLabel(m, k.title || k.label);
    var count = k.count != null && k.count !== '' ? h('span.tree__count', String(k.count)) : null;
    var ico = k.icon ? UI.icon(k.icon) : null;
    if (!k.href) return h('span.nav__item.tree__leaf.tree__d3.tree__kid--muted', { title: title }, ico, h('span.ellipsis', label), count);
    return h('a.nav__item.tree__leaf.tree__d3.tree__link', { href: k.href, title: title, 'data-href': k.href }, ico, h('span.ellipsis', label), count);
  }

  // rebuildTree draws the sidebar again from the modules' nav, keeping the open state and the filter, and marks the
  // page shown. / 用各模块的 nav 重新绘制侧栏，保留展开状态与筛选，并标记当前页面。
  function rebuildTree() {
    if (!navEl) return;
    items = {}; nodes = {}; ports = {};
    UI.mount(navEl, tree());
    filter(filterEl ? filterEl.value : '');
    markActive();
  }

  // filter hides the menu items whose labels lack q; it is recursive, so a module or port stays visible when any of
  // its descendants matches, and every matching descendant is shown. A head match shows all its children, and a
  // node is open while filtering. / filter 隐藏标签不含 q 的菜单项；它递归进行：任一后代匹配时模块或端口仍显示，
  // 且显示所有匹配的后代；头部自身匹配时显示其全部子项；筛选时节点展开。
  function filter(q) {
    q = (q || '').trim().toLowerCase();
    var on = !!q;
    function matches(el) { return !on || el.textContent.toLowerCase().indexOf(q) >= 0; }
    function kidsOf(n) {
      var out = [];
      Array.prototype.forEach.call(n.children, function (c) { if (c.classList.contains('tree__kids')) out.push(c); });
      return out[0] || null;
    }
    function filterNode(n, force) {
      n.classList.toggle('is-filtered', on);
      var head = n.querySelector(':scope > .tree__head') || n;
      var headHit = force || !on || matches(head);
      var box = kidsOf(n), any = false;
      if (box) {
        Array.prototype.forEach.call(box.children, function (c) {
          var shown;
          if (c.classList.contains('tree__node')) shown = filterNode(c, headHit);
          else { shown = headHit || matches(c); c.hidden = on && !shown; }
          if (shown) any = true;
        });
      }
      var vis = !on || headHit || any;
      n.hidden = on && !vis;
      return vis;
    }
    Array.prototype.forEach.call(navEl.children, function (c) {
      if (c.classList.contains('tree__node')) filterNode(c, false);
      else c.hidden = on && !matches(c);
    });
  }

  // markActive marks the menu item of the page shown, its module, and every link whose target is the page shown.
  // markActive 标记当前页面所属的菜单项与模块，以及目标为当前页面的每个链接。
  function markActive() {
    var key = shownPage.module ? shownPage.module + '/' + shownPage.navId : '';
    Object.keys(items).forEach(function (k) { items[k].classList.toggle('is-active', k === key); });
    Object.keys(nodes).forEach(function (k) { nodes[k].classList.toggle('has-active', k === shownPage.module); });
    markLinks();
  }
  // markLinks highlights the kid (or external) link whose href is the hash shown. / 高亮 href 即当前哈希的子项（或外链）。
  function markLinks() {
    if (!navEl) return;
    var hash = location.hash, hit = {};
    navEl.querySelectorAll('a[data-href]').forEach(function (a) {
      var on = a.getAttribute('data-href') === hash;
      a.classList.toggle('is-active', on);
      if (on) a.setAttribute('aria-current', 'page'); else a.removeAttribute('aria-current');
      var port = a.closest('.tree__port');
      if (on && port) hit[port.getAttribute('data-key')] = true;
    });
    Object.keys(ports).forEach(function (k) { ports[k].classList.toggle('has-active', !!hit[k]); });
  }

  // The shell a module's routes draw into. / 模块路由绘制所用的外框。
  function shellFor(m) {
    return {
      tools: toolsEl,
      page: function (navId, title, crumb) {
        // The page shown is what a menu rebuild marks again. / 所显示的页面在菜单重建时再次被标记。
        shownPage = { module: m.id, navId: navId };
        markActive();
        var mk = 'm:' + m.id;
        if (nodes[m.id] && !nodes[m.id].classList.contains('is-open')) setOpen(mk, true, nodes[m.id]);
        // The breadcrumb is the module, then the page for a module that has several. / 面包屑为模块，多页模块再加页面。
        var where = [labelOf(m)];
        var nav = m.nav.filter(function (n) { return n.id === navId; })[0];
        if (m.nav.length > 1 && nav) where.push(navLabel(m, nav.label));
        titleEl.textContent = title || '';
        crumbEl.textContent = crumb || where.join(' / ');
        document.title = (title ? title + ' · ' : '') + 'fit';
        UI.clear(toolsEl);
        return UI.clear(content);
      }
    };
  }

  function setup(m) {
    // A module of links only (the Engine menu) has no routes of its own. / 只有链接的模块（引擎菜单）没有自己的路由。
    if (typeof m.setup !== 'function') {
      if (!m.ready) { m.routes = {}; m.fallback = ''; m.ready = Promise.resolve(); }
      return m.ready;
    }
    if (!m.ready) {
      m.ready = Promise.resolve().then(function () { return m.setup(m.ui, shellFor(m)); }).then(function (r) {
        m.routes = (r && r.routes) || {};
        m.fallback = (r && r.fallback) || (m.nav[0] && m.nav[0].id) || '';
      });
      m.ready.catch(function () { m.ready = null; });
    }
    return m.ready;
  }

  // firstHash is the first target a module's menu offers: a page of its own, or a link. / 模块菜单的第一目标：自己的页面或链接。
  function firstHash(m) {
    var out = m.fallback ? '#/' + m.id + '/' + m.fallback : '';
    function walk(list) {
      for (var i = 0; i < list.length && !out; i++) {
        var n = list[i];
        if (n.id && !n.kids) out = '#/' + m.id + '/' + n.id;
        else if (n.href && n.href.indexOf('#/') === 0) out = n.href;
        else if (n.kids) walk(n.kids);
      }
    }
    walk(m.nav || []);
    return out;
  }
  // firstPage is the first page of any module that has one, for a URL that names none. / 任何有页面的模块的第一页。
  function firstPage() {
    for (var i = 0; i < mods.length; i++) {
      var m = mods[i];
      if (typeof m.setup !== 'function') continue;
      for (var j = 0; j < m.nav.length; j++) if (m.nav[j].id && !m.nav[j].kids) return '#/' + m.id + '/' + m.nav[j].id;
    }
    return '';
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
    if (locked) return;
    UI.stopTimers();
    UI.closeModals();
    root.classList.remove('nav-open');
    markLinks();
    var path = location.hash.replace(/^#\/?/, '').split('?')[0];
    var slash = path.indexOf('/');
    var id = slash < 0 ? path : path.slice(0, slash), rest = slash < 0 ? '' : path.slice(slash + 1);
    var m = byId[id];
    if (!m) {
      var first = mods.length ? firstHash(mods[0]) : '';
      if (first && first !== location.hash) location.replace(first);
      return;
    }
    var seq = current = {};
    setup(m).then(function () {
      if (seq !== current) return;
      var run = match(m.routes, rest);
      // The URL names no page of the module: go to the first target it has, or to any page. / URL 未指定页面时转到其第一目标。
      if (!run) {
        var to = firstHash(m) || firstPage();
        if (to && to !== location.hash) location.replace(to);
        return;
      }
      run();
    }).catch(UI.fail);
  }

  function build(wb, logout) {
    var served = {};
    (wb.modules || []).forEach(function (id) { served[id] = true; });
    mods = defs.filter(function (d) { return served[d.server]; }).map(function (d) {
      var m = Object.create(d);
      m.ui = UI.scope(d.server, d.id);
      m.def = d;
      // A nav function is called now and again on every rebuild. / 函数形式的 nav 现在调用，并在每次重建时再次调用。
      m.dyn = typeof d.nav === 'function';
      m.nav = navOf(d);
      return m;
    });
    mods.sort(function (a, b) { return rank(a) - rank(b); });
    byId = {}; items = {}; nodes = {}; ports = {};
    shownPage = { module: '', navId: '' };
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

  // boot calls each module's boot once after the shell is built; its async work may end with WB.refreshMenu.
  // boot 在外框构建完成后为每个模块调用一次 boot；其异步工作可在结束时调用 WB.refreshMenu。
  function boot() {
    mods.forEach(function (m) {
      var d = m.def;
      if (!d || typeof d.boot !== 'function' || d.booted) return;
      d.booted = true;
      Promise.resolve().then(function () { return d.boot(m.ui); }).catch(UI.fail);
    });
  }

  // fmtLeft is a countdown's text: 9:05, or 1:02:03. / 倒计时文字：9:05 或 1:02:03。
  function fmtLeft(ms) {
    var s = Math.max(0, Math.ceil(ms / 1000)), hh = Math.floor(s / 3600), mm = Math.floor(s % 3600 / 60), ss = s % 60;
    function p(n) { return (n < 10 ? '0' : '') + n; }
    return (hh ? hh + ':' + p(mm) : mm) + ':' + p(ss);
  }

  // changePage is the full page a session on the default password of `fit run -admin` gets instead of the workbench:
  // the current and a new password, and the time left before the default password stops working. It looks like the
  // login page. o: {session, onDone(), onLogout()}; it returns a function that stops its countdown.
  // changePage 是使用 `fit run -admin` 默认密码的会话看到的整页（代替工作台）：当前密码与新密码，以及默认密码失效前的
  // 剩余时间，外观同登录页。o：{session, onDone(), onLogout()}；返回停止其倒计时的函数。
  function changePage(el, o) {
    var s = o.session || {}, user = s.user || '';
    var until = s.password_expires ? Date.parse(s.password_expires) : NaN;
    var name = h('input', { type: 'text', name: 'username', autocomplete: 'username', value: user, hidden: true, readonly: true });
    var cur = h('input.input', { type: 'password', name: 'current-password', autocomplete: 'current-password' });
    var np = h('input.input', { type: 'password', name: 'new-password', autocomplete: 'new-password' });
    var cf = h('input.input', { type: 'password', name: 'confirm-password', autocomplete: 'new-password' });
    var err = h('div.callout.callout--danger', { hidden: true });
    var left = h('div.callout.callout--warn', { hidden: isNaN(until) });
    var b = h('button.btn.btn--primary', { type: 'submit', style: { width: '100%', height: '40px' } }, t('dp.submit'));
    var out = h('button.btn', { type: 'button', style: { width: '100%' }, onclick: function () { stop(); o.onLogout(); } }, UI.icon('logout'), t('logout'));
    function showErr(e) { err.textContent = e && e.message ? e.message : String(e); err.hidden = false; }
    var form = h('form.form', {
      onsubmit: function (e) {
        e.preventDefault(); err.hidden = true;
        if (!cur.value || !np.value) { showErr(t('dp.need')); return; }
        if (np.value !== cf.value) { showErr(t('dp.mismatch')); return; }
        if (np.value === cur.value) { showErr(t('dp.same')); return; }
        UI.busy(b, UI.api('POST', '/api/m/admin/account/password', { current_password: cur.value, password: np.value })).then(function (r) {
          stop();
          UI.toast(t('dp.done', { user: (r && r.user) || user }));
          o.onDone();
        }, showErr);
      }
    }, name, h('div.callout', UI.icon('info'), ' ', t('dp.note', { user: user })), left,
      UI.field(t('dp.current'), cur), UI.field(t('dp.new'), np, t('dp.hint')), UI.field(t('dp.confirm'), cf), err, b, out);
    var body = h('div.card__body', { style: { padding: '26px' } }, form);
    // expired replaces the form: the default password no longer works, only a restart with -admin helps.
    // expired 替换表单：默认密码已失效，只能以 -admin 重新启动。
    function expired() {
      stop();
      UI.mount(body, h('div.form', h('div.callout.callout--danger', UI.icon('alert'), ' ', t('dp.expired')),
        h('button.btn.btn--primary', { type: 'button', style: { width: '100%', height: '40px' }, onclick: o.onLogout }, UI.icon('logout'), t('logout'))));
    }
    var timer = null;
    function tick() {
      if (isNaN(until)) return;
      var ms = until - Date.now();
      if (ms <= 0) { expired(); return; }
      UI.mount(left, UI.icon('alert'), ' ', t('dp.left', { left: fmtLeft(ms) }), ' ',
        h('span.small.muted', t('dp.at', { at: UI.fmtTime(until / 1000) })));
    }
    function stop() { if (timer) clearInterval(timer); timer = null; }
    UI.mount(el, h('div.login', h('div.login__prefs', UI.prefs()), h('div.login__box',
      h('div.login__brand', h('div.brand__logo', UI.icon('logo')), h('div', h('div.brand__name', 'fit'), h('div.brand__sub.muted', t('dp.title')))),
      h('div.card', body))));
    tick();
    if (!isNaN(until) && until > Date.now()) timer = setInterval(tick, 1000);
    setTimeout(function () { if (document.body.contains(cur)) cur.focus(); }, 30);
    return stop;
  }

  document.addEventListener('DOMContentLoaded', function () {
    var app = document.getElementById('app');
    var started = false;
    // stopChange stops the change page's countdown; checking: a session read after a 403 is under way.
    // stopChange 停止修改页的倒计时；checking：403 之后的会话读取正在进行。
    var stopChange = null, checking = false;
    function unlock() { if (stopChange) stopChange(); stopChange = null; locked = false; }
    function showLogin() {
      unlock();
      UI.stopTimers();
      UI.closeModals();
      UI.loginPage(app, { brand: 'fit', sub: t('wb.sub'), username: 'admin', onSubmit: function (u, p) { return UI.login(u, p).then(enter); } });
    }
    function logout() { unlock(); UI.logout().then(showLogin, showLogin); }
    // enter goes on with a session: to the change page while its default password must be changed, else to the
    // workbench. / enter 以会话继续：须修改默认密码时进入修改页，否则进入工作台。
    function enter(s) {
      if (s && s.must_change_password) { showChange(s); return; }
      unlock();
      return run();
    }
    function showChange(s) {
      unlock();
      locked = true;
      UI.stopTimers();
      UI.closeModals();
      stopChange = changePage(app, {
        session: s, onLogout: logout,
        // The same session goes on: read it again, for its CSRF token and its state. / 同一会话继续：重新读取其 CSRF 令牌与状态。
        onDone: function () { UI.session().then(enter).catch(function (e) { if (e && e.status === 401) showLogin(); else UI.fail(e); }); }
      });
    }
    // A reply anywhere that asks for the default password's change sends the user to the change page.
    // 任何要求修改默认密码的应答都把用户带到修改页。
    UI.setMustChange(function () {
      if (locked || checking) return;
      checking = true;
      UI.session().then(function (s) {
        checking = false;
        if (s && s.must_change_password) showChange(s);
      }, function (e) { checking = false; if (e && e.status === 401) showLogin(); });
    });
    var served = '';
    function run() {
      return UI.workbench().then(function (wb) {
        served = (wb.modules || []).join(',');
        UI.mount(app, build(wb, logout));
        if (!started) { window.addEventListener('hashchange', route); started = true; }
        boot();
        route();
      });
    }
    refresh = function () {
      if (locked) return Promise.resolve();
      return UI.workbench().then(function (wb) {
        if (locked || (wb.modules || []).join(',') === served) return;
        var keep = mods.filter(function (m) { return m.ready; });
        served = (wb.modules || []).join(',');
        UI.mount(app, build(wb, logout));
        // A module set up before keeps its setup; the page shown is drawn again. / 已初始化的模块保留其初始化结果；当前页面重新绘制。
        keep.forEach(function (k) { if (byId[k.id]) byId[k.id] = k; });
        mods = mods.map(function (m) { return byId[m.id]; });
        boot();
        route();
      });
    };
    UI.setUnauthorized(showLogin);
    UI.session().then(enter).catch(function (e) { if (e && e.status === 401) showLogin(); else UI.fail(e); });
  });
})();
