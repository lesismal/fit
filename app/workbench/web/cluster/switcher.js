// The node switcher (I143), in the header of a manager's workbench (a fit with admin.cluster): a member chosen there
// opens the workbench's own pages, from this bundle, against that member: the page's URL gets ?node=<member>, and
// every request of its modules goes to /api/n/<member>/m/<module>/ on the manager, which passes it on to the member
// with the credentials it keeps (core/ui.js, the manager's cluster/passthrough.go). The page then names the member
// before its title and in a band above it, in a colour of the member's own, so that no one edits the wrong fit; the
// menu is the member's modules the manager opens, without the manager's own (its Cluster menu, the account pages, the
// assistant). A member whose version is not the manager's opens read-only: its writes are refused here and on the
// manager. Choosing the manager again loads its own pages, with nothing of the member's left on them. Every member's
// row and page in the Cluster menu also has Open (FitCluster.rowExtra), which does the same.
//
// 节点切换器（I143），位于管理端（设置了 admin.cluster 的 fit）工作台的页眉：在此选择成员后，以本页面包中的工作台页面针对该成员
// 打开：页面 URL 加上 ?node=<成员>，模块的每个请求都发往管理端的 /api/n/<成员>/m/<模块>/，由管理端以其保存的凭据转给成员
// （core/ui.js，管理端的 cluster/passthrough.go）。页面在标题前与其上方的横条中以该成员专属的颜色标出成员名，以免误改其他 fit；
// 菜单为管理端可打开的成员模块，不含管理端自己的模块（集群菜单、账户页面、助手）。版本与管理端不同的成员以只读方式打开：其写操作
// 在此与管理端都会被拒绝。再次选择管理端即加载其自己的页面，不留下成员的任何数据。集群菜单中每个成员的行与成员页也有“打开”
// （FitCluster.rowExtra），作用相同。
(function () {
  'use strict';
  var h = UI.h, t = UI.t;

  UI.addI18n({
    'en': {
      'sw.label': 'Node', 'sw.manager': 'This manager', 'sw.title': 'Open a member\'s own pages, through this manager',
      'sw.member': 'Member', 'sw.back': 'Back to the manager', 'sw.readonly': 'Read-only',
      'sw.via': 'through the manager, by {access}', 'sw.access.console': 'its console', 'sw.access.admin_api': 'its Admin API\'s token',
      'sw.access.link': 'its link',
      'sw.version': 'fit {v}', 'sw.host': 'host {h}',
      'sw.error': '{name} cannot be opened: {error}',
      'sw.open': 'Open', 'sw.open.title': 'Open {name}\'s own pages through this manager',
      'sw.load': 'Members could not be listed: {error}',
      'sw.ro.not_read': 'The manager has not read it yet, so its version is not known: its pages are read-only until it has (open it again in a moment).',
      'sw.ro.version': 'It runs another version of fit (config schema {member}; this manager\'s {manager}), whose pages this manager\'s do not know: they are read-only.',
      'sw.ro.manager': 'This manager\'s members cannot be written, so a change through it could not be recorded in its audit log: the pages are read-only.',
      'nd.a.node.api': 'changed through the node switcher',
      'sw.st.up': 'up', 'sw.st.down': 'down', 'sw.st.error': 'error', 'sw.st.unknown': 'not read yet',
      'member.readonly': 'This member is open read-only (the band above the page says why): nothing is changed on it from here.',
      'sw.ro.role': 'Your role on this member lets you read its pages, not change them: an admin of the cluster, or of a group it is in, may.',
      'manager.readonly': 'Your role in this cluster lets you read this manager\'s own pages, not change them: only an admin of the whole cluster changes the manager.',
      'sw.mgr.ro': 'Read-only: your role in this cluster is not admin of the whole cluster, so this manager\'s own config, users and services are only read here.'
    },
    'zh-CN': {
      'sw.label': '节点', 'sw.manager': '本管理端', 'sw.title': '通过本管理端打开成员自己的页面',
      'sw.member': '成员', 'sw.back': '返回管理端', 'sw.readonly': '只读',
      'sw.via': '经由管理端，通过{access}', 'sw.access.console': '其控制台', 'sw.access.admin_api': '其 Admin API 的令牌',
      'sw.access.link': '其连接',
      'sw.version': 'fit {v}', 'sw.host': '主机 {h}',
      'sw.error': '无法打开 {name}：{error}',
      'sw.open': '打开', 'sw.open.title': '通过本管理端打开 {name} 自己的页面',
      'sw.load': '无法列出成员：{error}',
      'sw.ro.not_read': '管理端尚未读取该成员，其版本未知：读取之前其页面只读（稍后再次打开即可）。',
      'sw.ro.version': '它运行的是另一版本的 fit（配置 schema {member}；本管理端为 {manager}），本管理端的页面不认识其页面：只读。',
      'sw.ro.manager': '本管理端的成员数据不能写入，通过它所做的修改无法记入其审计日志：页面只读。',
      'nd.a.node.api': '经节点切换器修改',
      'sw.st.up': '正常', 'sw.st.down': '离线', 'sw.st.error': '错误', 'sw.st.unknown': '尚未读取',
      'member.readonly': '此成员以只读方式打开（原因见页面上方的横条）：不能从这里修改它。',
      'sw.ro.role': '你在此成员上的角色只允许读取其页面、不能修改：集群管理员或其所在分组的管理员才可以。',
      'manager.readonly': '你在此集群中的角色只允许读取本管理端自己的页面、不能修改：只有集群范围的管理员才能修改管理端。',
      'sw.mgr.ro': '只读：你在此集群中不是集群范围的管理员，因此本管理端自己的配置、用户与服务在此只能读取。'
    }
  });

  // MANAGER_PAGES are the pages of the manager's own, which a member has not: a member opened from them starts at its
  // overview. / 管理端自己的页面（成员没有）：从这些页面打开成员时从其概览开始。
  var MANAGER_PAGES = /^#\/(cluster|account|ai)(\/|$)/;
  var FIRST = '#/gw.overview/overview';

  // hue is a member's own colour, from its name. / 成员专属的颜色（由名称得出）。
  // FNV-1a and a final mix, so that names a letter apart get hues far apart. / FNV-1a 加最终混合：只差一个字母的名称色相也相距很远。
  function hue(name) {
    var x = 0x811c9dc5;
    for (var i = 0; i < name.length; i++) x = Math.imul(x ^ name.charCodeAt(i), 16777619);
    x ^= x >>> 16; x = Math.imul(x, 0x85ebca6b); x ^= x >>> 13; x = Math.imul(x, 0xc2b2ae35); x ^= x >>> 16;
    return ((x >>> 0) % 36) * 10;
  }
  function colour(name) { return 'hsl(' + hue(name) + ' 62% 46%)'; }

  // open loads the pages of name ('' for the manager's own). / 加载 name 的页面（'' 为管理端自己的）。
  function open(name) {
    var hash = location.hash;
    if (name && (!hash || MANAGER_PAGES.test(hash))) hash = FIRST;
    location.assign(UI.targetURL(name, hash));
  }

  // switcher is the header's select: the manager, then every member with its state, listed when the select is drawn
  // and again when it is opened. / 页眉的选择框：管理端，然后是每个成员及其状态；绘制时列出，展开时再次列出。
  function switcher(target) {
    var sel = h('select.select.select--sm.sw-select', { 'aria-label': t('sw.label'), title: t('sw.title'),
      onchange: function () {
        var to = sel.value;
        open(to);
        // A page that keeps unsaved changes may stay: the select shows it again. / 有未保存修改的页面可能留下：选择框恢复。
        setTimeout(function () { sel.value = target; }, 1000);
      } });
    function fill(nodes) {
      var names = nodes.map(function (n) { return n.name; });
      if (target && names.indexOf(target) < 0) nodes = [{ name: target }].concat(nodes);
      UI.mount(sel, h('option', { value: '' }, t('sw.manager')), nodes.map(function (n) {
        var st = n.status ? ' · ' + t('sw.st.' + n.status) : '';
        return h('option', { value: n.name }, n.name + st);
      }));
      sel.value = target;
    }
    function load() {
      UI.api('GET', '/api/m/cluster/state').then(function (s) { fill((s && s.nodes) || []); }, function (e) {
        sel.title = t('sw.load', { error: e.message });
      });
    }
    fill([]);
    load();
    sel.addEventListener('mousedown', load);
    sel.addEventListener('focus', load);
    return h('label.sw', UI.icon('hub'), h('span.sw-label', t('sw.label')), sel);
  }

  // band is the strip above a member's pages: who it is, how it is reached, read-only and why, or why it cannot be
  // opened, and the way back. / 成员页面上方的横条：成员是谁、如何连接、是否只读及原因，或无法打开的原因，以及返回方式。
  function band(target, mem) {
    var bits = [];
    if (mem.hostname) bits.push(t('sw.host', { h: mem.hostname }));
    if (mem.fit_version) bits.push(t('sw.version', { v: mem.fit_version }));
    if (mem.access) bits.push(t('sw.via', { access: t('sw.access.' + mem.access) }));
    var back = h('a.btn.btn--sm.sw-back', { href: UI.targetURL('', location.hash), onclick: function (e) { e.preventDefault(); open(''); } },
      UI.icon('undo'), t('sw.back'));
    return h('div.sw-band', { role: 'status' },
      h('span.sw-band__dot'), h('span.sw-band__what', t('sw.member')), h('strong.sw-band__name', target),
      bits.length ? h('span.sw-band__bits', bits.join(' · ')) : null,
      mem.read_only ? h('span.badge.badge--warn.sw-ro', { title: mem.reason || '' }, UI.icon('lock'), t('sw.readonly')) : null,
      mem.read_only ? h('span.sw-band__why', why(mem)) : null,
      mem.error ? h('span.sw-band__err', t('sw.error', { name: target, error: mem.error })) : null,
      h('span.sw-band__spacer'), back);
  }

  // why words why a member is read-only. / 说明成员只读的原因。
  function why(mem) {
    function short(d) { d = d || ''; return d.length > 19 ? d.slice(0, 19) + '…' : d; }
    var k = mem.reason_kind;
    if (k === 'not_read' || k === 'version' || k === 'manager' || k === 'role') {
      return t('sw.ro.' + k, { member: short(mem.schema_digest), manager: short(mem.manager_schema_digest) });
    }
    return mem.reason || '';
  }

  WB.frame(function (ctx) {
    var wb = ctx.wb || {}, target = ctx.target;
    // Only a manager has members to switch to. / 只有管理端才有可切换的成员。
    if (!target && (wb.modules || []).indexOf('cluster') < 0) return null;
    var out = { top: switcher(target) };
    // The manager's own pages, read-only for a user who is not an admin of the whole cluster (I148).
    // 管理端自己的页面：对非集群范围管理员的用户只读（I148）。
    if (!target && wb.cluster_me && wb.cluster_me.manager_read_only) {
      out.band = h('div.sw-band.sw-band--manager', { role: 'status' }, h('span.badge.badge--warn.sw-ro', UI.icon('lock'), t('sw.readonly')),
        h('span.sw-band__why', t('sw.mgr.ro')));
      out.cls = 'is-readonly';
    }
    if (target) {
      var mem = wb.member || {};
      out.title = h('span.sw-pill', { title: t('sw.member') + ' ' + target }, UI.icon('server'), target);
      out.band = band(target, mem);
      out.cls = 'is-member' + (mem.read_only ? ' is-readonly' : '');
      out.color = colour(target);
    }
    return out;
  });

  // Open, on every member's row and page of the Cluster menu. / 集群菜单中每个成员的行与成员页上的“打开”。
  if (window.FitCluster) {
    FitCluster.rowExtra(function (_, mem, st) {
      var name = (mem && mem.name) || (st && st.name);
      if (!name) return null;
      return h('a.btn.btn--ghost.btn--sm.sw-open', { href: UI.targetURL(name, FIRST), title: t('sw.open.title', { name: name }) },
        UI.icon('swap'), t('sw.open'));
    });
  }

  window.FitSwitcher = { open: open, colour: colour };
})();
