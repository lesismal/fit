// Cluster → Nodes (I142): the members the manager keeps (GET /api/m/cluster/nodes, with its ETag) beside their states
// (GET state): their names, labels, URLs, access, credentials (only whether one is saved: never their values), how
// their certificates are trusted, and their states; Add a node (the form, with Test and Trust), and for each member
// Test, Trust (a certificate not trusted, or not the one pinned), Edit and Remove, each change sent with the ETag the
// page read as If-Match. The bar's place beside Add a node takes what later iterations add (FitCluster.addAction:
// I149's Add by enrollment). The manager's recent changes (GET audit) close the page. #/cluster/nodes?add=1 opens the
// form at once.
//
// 集群 → 节点（I142）：管理端保存的成员（GET /api/m/cluster/nodes 及其 ETag）及其状态（GET state）：名称、标签、URL、
// 访问方式、凭据（只显示是否已保存，从不显示其值）、证书的信任方式与状态；“添加节点”（表单，带测试与信任），每个成员可
// 测试、信任（不受信任或不是已固定的证书）、编辑与删除，每次修改以页面读到的 ETag 作为 If-Match。工具栏中“添加节点”旁的
// 位置留给后续迭代（FitCluster.addAction：I149 的“通过注册添加”）。页面末尾为管理端最近的修改（GET audit）。
// #/cluster/nodes?add=1 直接打开表单。
(function () {
  'use strict';
  var FC = window.FitCluster;
  FC.WORDS.push(
    ['nd.count', '{n} of at most {max} members (max_nodes)', '{n} 个成员，最多 {max} 个（max_nodes）'],
    ['nd.access', 'Access', '访问方式'], ['nd.creds', 'Credentials', '凭据'], ['nd.tls', 'Certificate', '证书'],
    ['nd.state', 'State', '状态'], ['nd.name', 'Name', '名称'], ['nd.url', 'URL', 'URL'],
    ['nd.as', 'console, as {user}', '控制台，用户 {user}'], ['nd.token', 'Admin API token', 'Admin API 令牌'],
    ['nd.set', 'saved', '已保存'], ['nd.unset', 'missing', '缺失'],
    ['nd.http', 'http (loopback)', 'http（回环）'],
    ['nd.tls.system', 'System CAs', '系统 CA'], ['nd.tls.ca', 'Its CA', '指定 CA'], ['nd.tls.pin', 'Pinned', '已固定'],
    ['nd.none', 'No members yet. Add the fits this one manages: their console\'s URL and a user\'s name and password, or their Admin API\'s URL and token.',
      '还没有成员。请添加本 fit 管理的其他 fit：其控制台 URL 与用户名、密码，或其 Admin API 的 URL 与令牌。'],
    ['nd.audit', 'Recent changes', '最近的修改'], ['nd.audit.none', 'No changes yet.', '还没有修改。'],
    ['nd.when', 'When', '时间'], ['nd.who', 'Who', '用户'], ['nd.what', 'What', '操作'], ['nd.detail', 'Detail', '详情'],
    ['nd.a.node.add', 'added', '添加'], ['nd.a.node.update', 'changed', '修改'], ['nd.a.node.remove', 'removed', '删除']
  );

  FC.pages.nodes = function (UI, sh) {
    var h = UI.h, t = UI.t;
    var el = sh.page('nodes', t('n.nodes'));
    var list = null, states = {}, readOnly = '';
    var head = h('div.row.row--between.cl-bar'), body = h('div'), audit = h('div');
    UI.mount(el, h('div.stack', head, body, audit));
    UI.mount(body, h('div.row', h('span.spinner'), h('span.muted', t('loading'))));
    UI.mount(sh.tools, UI.btn(t('refresh'), 'refresh', function () { read(); }, 'ghost sm'));

    // After a change the poller reads the member at once: its state is read again shortly.
    // 修改后轮询器会立即读取该成员：稍后再次读取其状态。
    function saved() { read(); setTimeout(function () { if (document.body.contains(el)) read(); }, 1500); }
    function add() { FC.memberForm(UI, { etag: list && list.etag, reload: read, then: saved }); }
    function edit(mem) {
      // A linked member (I149, enroll.js) changes only its name and labels. / 连接型成员（I149）只改名称与标签。
      var f = mem.access === 'link' && FC.linkForm ? FC.linkForm : FC.memberForm;
      f(UI, { member: mem, etag: list && list.etag, reload: read, then: saved });
    }

    function creds(m) {
      if (m.access === 'link' && FC.linkCreds) return FC.linkCreds(UI, m);
      if (m.access === 'admin_api') return UI.badge(t('fm.token') + ' · ' + t(m.token_set ? 'nd.set' : 'nd.unset'), m.token_set ? 'ok' : 'danger');
      return h('span', h('span.small', m.username || '—'), ' ', UI.badge(t('fm.password') + ' · ' + t(m.password_set ? 'nd.set' : 'nd.unset'), m.password_set ? 'ok' : 'danger'));
    }
    function tlsOf(m) {
      if (m.access === 'link') return h('span.muted.small', '—');
      if (!/^https:/i.test(m.url)) return h('span.muted.small', t('nd.http'));
      var tls = m.tls || {};
      if (tls.fingerprint) return h('div', UI.badge(t('nd.tls.pin'), 'primary'), h('div', h('code.small.cl-fp', { title: tls.fingerprint }, tls.fingerprint.slice(0, 23) + '…')));
      return UI.badge(t(tls.ca ? 'nd.tls.ca' : 'nd.tls.system'));
    }
    function stateOf(m) {
      var s = states[m.name];
      if (!s) return FC.stateBadge(UI, 'unknown');
      return h('div', FC.stateBadge(UI, s.status), s.error ? h('div.small.cl-msg', { title: s.error.message }, FC.kindWord(UI, s.error.kind)) : null,
        FC.extras(UI, m, s));
    }
    function actions(m) {
      var s = states[m.name], e = s && s.error;
      var untrusted = e && e.certificate && (e.kind === 'tls_untrusted' || e.kind === 'tls_pin_mismatch');
      function b(label, icon, fn, kind) {
        var x = UI.btn(label, icon, fn, kind || 'ghost sm');
        x.title = label + ' ' + m.name; x.setAttribute('aria-label', label + ' ' + m.name);
        return x;
      }
      var out = [b(t('fm.test'), 'zap', function () { FC.testSaved(UI, m, list.etag, saved); })];
      if (untrusted) {
        var tb = b(t('ct.trust'), 'shield', function () {
          FC.trustSaved(UI, m, e.certificate, e.kind === 'tls_pin_mismatch', list.etag).then(function (ok) { if (ok) saved(); });
        }, 'primary sm');
        tb.disabled = !!readOnly;
        out.push(tb);
      }
      var eb = b(t('edit'), 'edit', function () { edit(m); }), rb = b(t('delete'), 'trash', function () {
        FC.remove(UI, m.name, list.etag).then(function (ok) { if (ok) read(); });
      }, 'ghost sm');
      eb.disabled = rb.disabled = !!readOnly;
      out.push(eb, rb);
      return h('span.btn-row', out);
    }

    function draw() {
      if (!list || !document.body.contains(el)) return;
      var addBtn = UI.btn(t('fm.add'), 'plus', add, 'primary');
      addBtn.disabled = !!readOnly || list.nodes.length >= list.maxNodes;
      // The bar: Add a node, then what later iterations add beside it (FitCluster.addAction).
      // 工具栏：“添加节点”，以及后续迭代在其旁加入的按钮（FitCluster.addAction）。
      var more = FC.addActions(UI, { readOnly: !!readOnly, reload: read });
      UI.mount(head, h('span.btn-row.cl-add', addBtn, more), h('span.small.muted', t('nd.count', { n: list.nodes.length, max: list.maxNodes })));
      var parts = [];
      if (readOnly) parts.push(h('div.callout.callout--warn', t('readonly', { why: readOnly })));
      if (!list.nodes.length) parts.push(UI.empty(t('nd.none'), 'server'));
      else parts.push(UI.card(null, UI.table([
        { label: t('nd.name'), render: function (m) {
          return h('div', h('a', { href: '#/cluster/node/' + encodeURIComponent(m.name) }, h('b', m.name)), FC.labelChips(UI, m.labels));
        } },
        { label: t('nd.url'), render: function (m) { return h('code.small.cl-url', { title: m.url }, m.url); } },
        { label: t('nd.access'), render: function (m) {
          if (m.access === 'link' && FC.linkAccess) return FC.linkAccess(UI);
          return m.access === 'admin_api' ? t('nd.token') : t('nd.as', { user: m.username || '?' });
        } },
        { label: t('nd.creds'), render: creds },
        { label: t('nd.tls'), render: tlsOf },
        { label: t('nd.state'), render: stateOf },
        { label: '', cls: 'actions', render: actions }
      ], list.nodes), null, true));
      UI.mount(body, h('div.stack', parts));
    }

    function drawAudit(entries) {
      var rows = (entries || []).slice(-20).reverse();
      UI.mount(audit, UI.card(t('nd.audit'), UI.table([
        { label: t('nd.when'), render: function (a) { return h('span.small', FC.when(a.at)); } },
        { label: t('nd.who'), render: function (a) { return a.user; } },
        { label: t('nd.what'), render: function (a) {
          var w = t('nd.a.' + a.action);
          return h('span', h('b', a.node || ''), ' ', h('span.muted', w === 'nd.a.' + a.action ? a.action : w));
        } },
        { label: t('nd.detail'), render: function (a) { return h('span.small.muted', a.detail || ''); } }
      ], rows, { empty: t('nd.audit.none') }), null, true));
    }

    var opened = false;
    function read() {
      return Promise.all([FC.members(UI), FC.call(UI, 'GET', '/api/state')]).then(function (r) {
        list = r[0]; readOnly = list.readOnly;
        states = {};
        ((r[1].body && r[1].body.nodes) || []).forEach(function (s) { states[s.name] = s; });
        draw();
        // #/cluster/nodes?add=1 opens the form once. / #/cluster/nodes?add=1 只打开一次表单。
        if (!opened && /[?&]add=1\b/.test(location.hash) && !readOnly) { opened = true; add(); }
        FC.call(UI, 'GET', '/api/audit').then(function (a) { if (document.body.contains(el)) drawAudit(a.body && a.body.entries); }, function () { /* ignore / 忽略 */ });
      }, function (e) {
        if (!document.body.contains(el) || e.status === 401) return;
        UI.mount(body, h('div.callout.callout--danger', e.message));
      });
    }
    read();
    // The states follow the poller while the page is shown. / 页面显示期间状态跟随轮询器。
    UI.every(10000, function () {
      if (document.querySelector('.modal')) return;
      FC.call(UI, 'GET', '/api/state').then(function (r) {
        states = {};
        ((r.body && r.body.nodes) || []).forEach(function (s) { states[s.name] = s; });
        draw();
      }, function () { /* ignore / 忽略 */ });
    });
  };
})();
