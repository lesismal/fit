// Cluster → Nodes → Add by enrollment (I149): members behind NAT, or with no port open to the manager, link out to it
// (admin.cluster_member on the member) and are managed through their link. Add by enrollment names the member, gives
// its token's lifetime and the URL the member reaches the manager's link listener at (admin.cluster.link_address),
// and shows, once, the token and the config line to paste into the member's admin; a member enrolled already is made
// a new token from its row. Each linked member's row says whether it is linked or waiting, and why it waits. It hosts
// itself in the Nodes page's bar and rows (FitCluster.addAction, FitCluster.rowExtra), and edits a linked member's
// name and labels (FitCluster.linkForm), the only things of it a person changes. Needs shared.js; before cluster.js.
//
// 集群 → 节点 → 通过注册添加（I149）：位于 NAT 之后、或未向管理端开放端口的成员主动连接管理端（成员上的
// admin.cluster_member），并通过这条连接被管理。“通过注册添加”填写成员名称、令牌有效期与成员访问管理端连接监听
// （admin.cluster.link_address）所用的 URL，并只显示一次令牌及要粘贴到成员 admin 中的配置行；已注册的成员可在其行上
// 重新生成令牌。每个连接型成员的行显示“已连接”或“等待中”及等待的原因。本文件挂在节点页的工具栏与行上
// （FitCluster.addAction、FitCluster.rowExtra），并编辑连接型成员的名称与标签（FitCluster.linkForm），这是此类成员
// 唯一可由人修改的内容。依赖 shared.js；在 cluster.js 之前加载。
(function () {
  'use strict';
  var FC = window.FitCluster;
  FC.WORDS.push(
    ['ce.add', 'Add by enrollment', '通过注册添加'],
    ['ce.title', 'Add a member by enrollment', '通过注册添加成员'],
    ['ce.note', 'For a fit behind NAT, or with no port open to this manager: it links out to the manager with a token made here, used once for a credential of its own, and is managed through its link as the others are over HTTP.',
      '用于位于 NAT 之后、或未向本管理端开放端口的 fit：它用这里生成的令牌主动连接管理端，令牌只用一次，换取它自己的凭据，之后通过这条连接被管理，如同其他成员通过 HTTP 被管理。'],
    ['ce.off', 'This manager takes no links: set admin.cluster.link_address, and restart it.', '本管理端不接受连接：请设置 admin.cluster.link_address 并重启。'],
    ['ce.name', 'Name', '名称'], ['ce.labels', 'Labels', '标签'],
    ['ce.ttl', 'Token lifetime', '令牌有效期'], ['ce.ttl.hint', 'As 24h or 7d; 24h when empty, 30d at most.', '如 24h 或 7d；留空为 24h，最长 30d。'],
    ['ce.url', 'Manager URL', '管理端 URL'], ['ce.url.hint', 'Where the member reaches this manager\'s link listener: wss://host:port/cluster/link.', '成员访问本管理端连接监听的地址：wss://host:port/cluster/link。'],
    ['ce.make', 'Make the token', '生成令牌'],
    ['ce.made', 'Enrollment token for {name}', '{name} 的注册令牌'],
    ['ce.once', 'This is the only time the token is shown: paste the line into the member\'s admin now, then start or restart it. Only a hash of the token is kept, and it is used once.',
      '令牌只显示这一次：请立即把这一行粘贴到成员配置的 admin 中，然后启动或重启它。系统只保存令牌的哈希，且令牌只能使用一次。'],
    ['ce.line', 'Config line for the member\'s admin', '成员 admin 的配置行'],
    ['ce.expires', 'The token expires at {at}.', '令牌于 {at} 过期。'],
    ['ce.fp', 'Manager certificate (pinned by the member)', '管理端证书（成员会固定它）'],
    ['ce.again', 'New token', '新令牌'], ['ce.again.title', 'Make {name} a new enrollment token', '为 {name} 生成新的注册令牌'],
    ['ce.again.note', 'The member keeps linking with its credential until it enrolls with the new token, which then replaces it.',
      '在成员用新令牌注册之前，它继续用原凭据连接；注册后新凭据取代原凭据。'],
    ['ce.linked', 'Linked', '已连接'], ['ce.waiting', 'Waiting', '等待中'],
    ['ce.w.enroll', 'waiting for it to enroll, until {at}', '等待注册，截至 {at}'],
    ['ce.w.expired', 'its token expired: make it a new one', '令牌已过期：请生成新令牌'],
    ['ce.w.link', 'waiting for it to link again', '等待其重新连接'],
    ['ce.from', 'from {addr}', '来自 {addr}'],
    ['ce.refused', 'Last refused: {reason}', '最近被拒：{reason}'],
    ['ce.access', 'Linked out, by enrollment', '主动连接（注册）'],
    ['ce.cred', 'Credential', '凭据'], ['ce.cred.set', 'enrolled', '已注册'], ['ce.cred.none', 'not enrolled', '未注册'],
    ['ce.edit', 'Edit {name}', '编辑 {name}'],
    ['nd.a.node.enroll_token', 'enrollment token made', '生成注册令牌'], ['nd.a.node.enrolled', 'enrolled', '已注册'],
    ['k.not_linked', 'Not linked', '未连接'],
    ['k.not_linked.hint', 'The member links out to the manager and has no link now: it has not enrolled yet, or it links again, with backoff, once it reaches the manager.',
      '该成员主动连接管理端，当前没有连接：尚未注册，或在能访问管理端后按退避重新连接。']
  );
  if (FC.KINDS.indexOf('not_linked') < 0) FC.KINDS.push('not_linked');

  // linkInfo reads where members link to: {enabled, url, fingerprint, ...}. / 读取成员连接的位置。
  function linkInfo(UI) { return FC.call(UI, 'GET', '/api/link').then(function (r) { return r.body || {}; }); }

  // shown shows an enrollment made: the token, once, and the config line. / 显示生成的注册：令牌（仅一次）与配置行。
  function shown(UI, e) {
    var h = UI.h, t = UI.t;
    var line = e.config_line || '';
    var code = h('code', { style: { overflowWrap: 'anywhere', userSelect: 'all', display: 'block', whiteSpace: 'pre-wrap' } }, line);
    var exp = FC.sec(e.expires);
    UI.modal({
      title: t('ce.made', { name: e.node && e.node.name }), wide: true,
      body: h('div.stack',
        h('div.callout.callout--warn', UI.icon('key'), ' ', t('ce.once')),
        h('div', h('b', t('ce.line')), h('div.row', { style: { flexWrap: 'nowrap', alignItems: 'flex-start' } }, code,
          UI.btn(t('copy'), 'copy', function () { UI.copy(line); }, 'sm'))),
        exp ? h('div.small.muted', t('ce.expires', { at: UI.fmtTime(exp) })) : null,
        e.config && e.config.fingerprint ? h('div.small', h('span.muted', t('ce.fp') + ': '), h('code.cl-fp', e.config.fingerprint)) : null),
      actions: [{ label: t('close'), kind: 'primary' }]
    });
  }

  // form opens the form of an enrollment: of a new member, or, with name, a new token for that member.
  // form 打开注册表单：新成员的，或给出 name 时为该成员生成新令牌。
  function form(UI, name, done) {
    var h = UI.h, t = UI.t;
    linkInfo(UI).then(function (info) {
      if (!info.enabled) { UI.modal({ title: t('ce.title'), body: h('div.callout.callout--warn', t('ce.off')), actions: [{ label: t('close') }] }); return; }
      var nm = h('input.input', { name: 'enroll-name', autocomplete: 'off', spellcheck: 'false', placeholder: 'edge-nat-1' });
      var labels = h('input.input', { name: 'enroll-labels', autocomplete: 'off', spellcheck: 'false', placeholder: 'site=home' });
      var ttl = h('input.input.mono', { name: 'enroll-ttl', autocomplete: 'off', spellcheck: 'false', placeholder: '24h' });
      var url = h('input.input', { name: 'enroll-url', autocomplete: 'off', spellcheck: 'false', value: info.url || '' });
      var err = h('div.callout.callout--danger', { hidden: true });
      UI.modal({
        title: name ? t('ce.again.title', { name: name }) : t('ce.title'), wide: true,
        body: h('div.form',
          h('div.callout', UI.icon('info'), ' ', t(name ? 'ce.again.note' : 'ce.note')),
          name ? null : h('div.form-row', UI.field(t('ce.name'), nm), UI.field(t('ce.labels'), labels)),
          h('div.form-row', UI.field(t('ce.ttl'), ttl, t('ce.ttl.hint')), UI.field(t('ce.url'), url, t('ce.url.hint'))), err),
        actions: [{ label: t('cancel') }, {
          label: t('ce.make'), kind: 'primary', onclick: function () {
            if (!name && !nm.value.trim()) { nm.focus(); return false; }
            var body = { ttl: ttl.value.trim(), url: url.value.trim() === (info.url || '') ? '' : url.value.trim() };
            if (!name) { body.name = nm.value.trim(); body.labels = FC.parseLabels(labels.value); }
            var path = name ? FC.nodePath(name) + '/enroll' : '/api/enroll';
            return FC.call(UI, 'POST', path, body).then(function (r) {
              if (done) done();
              shown(UI, r.body || {});
            }, function (e) {
              err.hidden = false;
              err.textContent = e.message;
              return false;
            });
          }
        }]
      });
    }).catch(UI.fail);
  }

  // linkForm edits a linked member's name and labels. / 编辑连接型成员的名称与标签。
  function linkForm(UI, o) {
    var h = UI.h, t = UI.t, mem = o.member;
    var nm = h('input.input', { name: 'link-name', value: mem.name, autocomplete: 'off', spellcheck: 'false' });
    var labels = h('input.input', { name: 'link-labels', value: FC.labelsText(mem.labels), autocomplete: 'off', spellcheck: 'false' });
    var err = h('div.callout.callout--danger', { hidden: true });
    UI.modal({
      title: t('ce.edit', { name: mem.name }),
      body: h('div.form', h('div.form-row', UI.field(t('ce.name'), nm), UI.field(t('ce.labels'), labels)), err),
      actions: [{ label: t('cancel') }, {
        label: t('save'), kind: 'primary', onclick: function () {
          var body = { name: nm.value.trim(), labels: FC.parseLabels(labels.value), access: 'link' };
          return FC.call(UI, 'PUT', FC.nodePath(mem.name), body, o.etag).then(function (r) {
            if (o.then) o.then(r.body);
          }, function (e) {
            if (e.status === 412 && o.reload) o.reload();
            err.hidden = false;
            err.textContent = e.message;
            return false;
          });
        }
      }]
    });
  }

  // linkBadge says whether a linked member is linked or waiting, and why. / 说明连接型成员已连接还是等待中及原因。
  function linkBadge(UI, mem, st) {
    if (!mem || mem.access !== 'link') return null;
    var h = UI.h, t = UI.t, l = (st && st.link) || {}, lv = mem.link || {};
    var why = '';
    if (l.linked) why = l.remote ? t('ce.from', { addr: l.remote }) : '';
    else if (!l.enrolled && lv.token_waiting) {
      var by = FC.sec(l.enroll_by || lv.token_expires);
      why = by && by * 1000 < Date.now() ? t('ce.w.expired') : t('ce.w.enroll', { at: by ? UI.fmtTime(by) : '—' });
    } else why = t('ce.w.link');
    var refused = l.refused && !l.linked ? t('ce.refused', { reason: l.refused.reason + (l.refused.message ? ' — ' + l.refused.message : '') }) : '';
    return h('span.cl-link', { title: [why, refused].filter(Boolean).join('\n') },
      UI.badge(t(l.linked ? 'ce.linked' : 'ce.waiting'), l.linked ? 'ok' : 'warn', true),
      why ? h('span.small.muted', ' ' + why) : null);
  }

  // The Nodes page's bar: Add by enrollment beside Add a node. / 节点页工具栏：“添加节点”旁的“通过注册添加”。
  FC.addAction(function (UI, ctx) {
    if (!FC.canAny('admin')) return null;
    var b = UI.btn(UI.t('ce.add'), 'key', function () { form(UI, null, ctx.reload); }, 'ghost');
    b.disabled = !!ctx.readOnly;
    return b;
  });
  // Each linked member's row: linked or waiting, and a new token for it. / 每个连接型成员的行：已连接或等待中，以及新令牌。
  FC.rowExtra(function (UI, mem, st) {
    var badge = linkBadge(UI, mem, st);
    if (!badge) return null;
    if (!FC.canOn('admin', mem.name)) return UI.h('div.cl-link-row', badge);
    var again = UI.btn(UI.t('ce.again'), 'key', function () { form(UI, mem.name); }, 'ghost sm');
    again.title = UI.t('ce.again.title', { name: mem.name });
    return UI.h('div.cl-link-row', badge, again);
  });

  FC.enroll = form;
  FC.linkForm = linkForm;
  FC.linkBadge = linkBadge;
  // linkAccess is the words of a linked member's access and credential, for the pages' columns.
  // 连接型成员的访问方式与凭据的文字，供页面各列使用。
  FC.linkAccess = function (UI) { return UI.t('ce.access'); };
  FC.linkCreds = function (UI, mem) {
    var set = mem.link && mem.link.credential_set;
    return UI.badge(UI.t('ce.cred') + ' · ' + UI.t(set ? 'ce.cred.set' : 'ce.cred.none'), set ? 'ok' : 'warn');
  };
})();
