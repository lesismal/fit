// Cluster → Users and audit (I148): the manager's console users and their roles, and the audit log of every cluster
// action. #/cluster/users: each user's role cluster-wide (viewer reads, operator also runs operations and rollouts of the
// templates there are, admin also changes members, their credentials, templates, groups and the roles), its roles on
// groups, which raise its role on their members, and its assistant's level as the roles bound it; a cluster-wide admin
// changes them, the others read them. #/cluster/audit: the entries filtered by user, member, operation and time (the
// filters live in the URL's query, so a member's page links to its own), each opening its diff, digest, versions and
// outcomes; Export saves the entries picked as JSON lines. It calls /api/m/cluster/access, /me and /audit
// (docs/cluster.md). Needs shared.js; before cluster.js.
//
// 集群 → 用户与审计（I148）：管理端的控制台用户及其角色，以及每个集群操作的审计日志。#/cluster/users：每个用户在集群范围的
// 角色（查看者只读；操作员还可运行操作，以及已有模板的发布；管理员还可修改成员及其凭据、模板、分组和角色）、在分组上的角色
// （提升其在分组成员上的角色），以及受角色约束后的助手级别；由集群范围的管理员修改，其他人只读。#/cluster/audit：按用户、成员、
// 操作与时间筛选的条目（筛选条件保存在 URL 的查询中，成员页可链接到自己的条目），每条可打开其差异、摘要、版本与各成员结果；
// “导出”把所选条目保存为 JSON Lines。调用 /api/m/cluster/access、/me 与 /audit（见 docs/cluster.md）。依赖 shared.js；在
// cluster.js 之前加载。
(function () {
  'use strict';
  var FC = window.FitCluster;
  FC.WORDS.push(
    ['n.users', 'Users and audit', '用户与审计'],
    ['us.users', 'Users and roles', '用户与角色'], ['us.audit', 'Audit log', '审计日志'],
    ['us.user', 'User', '用户'], ['us.console', 'Console user', '控制台用户'], ['us.role', 'Cluster-wide', '集群范围'],
    ['us.groups', 'On groups', '分组上'], ['us.assistant', 'Assistant', '助手'],
    ['us.notconsole', 'not a console user', '不是控制台用户'], ['us.fixed', 'admin.cluster.admins', 'admin.cluster.admins'],
    ['us.fixed.title', 'Named in admin.cluster.admins: an admin of the cluster whatever the roles say, until the config changes and fit restarts.',
      '由 admin.cluster.admins 指定：无论角色如何都是集群管理员，直到修改配置并重启 fit。'],
    ['us.open', 'No roles are set: every console user is an admin of the cluster, as before roles were. Set them here; once saved, each user does only what its roles let it, and only a cluster-wide admin changes this manager\'s own config.',
      '尚未设置角色：每个控制台用户都是集群管理员，与引入角色之前相同。在此设置；保存后，每个用户只能做其角色允许的事，且只有集群范围的管理员才能修改本管理端自己的配置。'],
    ['us.roles.hint', 'viewer reads; operator also runs operations and rollouts of the templates there are; admin also changes members and their credentials, writes through the node switcher and copies config between members, and, cluster-wide, changes groups, templates and these roles. A role on a group is a role on its members.',
      '查看者只读；操作员还可运行操作，以及已有模板的发布；管理员还可修改成员及其凭据、经节点切换器写入、在成员间复制配置，在集群范围内还可修改分组、模板以及这些角色。分组上的角色即其成员上的角色。'],
    ['us.default', 'Users not listed', '未列出的用户'], ['us.default.hint', 'The role of a console user these roles do not name.', '这些角色未指定的控制台用户的角色。'],
    ['us.addgroup', 'Add a group role', '添加分组角色'], ['us.adduser', 'Add a user', '添加用户'], ['us.adduser.ph', 'user name', '用户名'],
    ['us.save', 'Save roles', '保存角色'], ['us.saved', 'Roles saved.', '角色已保存。'],
    ['us.save.ask', 'Save these roles? They take effect at once, for every page and request of every user.', '保存这些角色？它们立即对每个用户的每个页面与请求生效。'],
    ['us.readonly', 'Only an admin of the whole cluster changes roles: you see them here as they are.', '只有集群范围的管理员才能修改角色：此处仅显示当前角色。'],
    ['us.cap', 'bounded to {level} by the roles', '受角色限制为 {level}'], ['us.you', 'you', '你'],
    ['us.noai', 'off', '未开启'], ['us.none', 'No users.', '没有用户。'],
    // The audit log / 审计日志
    ['au.user', 'User', '用户'], ['au.node', 'Member', '成员'], ['au.action', 'Operation', '操作'], ['au.since', 'From', '起'], ['au.until', 'Until', '止'],
    ['au.anyone', 'Anyone', '任何人'], ['au.anything', 'Any operation', '任何操作'], ['au.apply', 'Filter', '筛选'], ['au.clear', 'Clear', '清除'],
    ['au.export', 'Export JSON lines', '导出 JSON Lines'], ['au.export.title', 'Save the entries picked, one JSON object a line', '保存所选条目，每行一个 JSON 对象'],
    ['au.kept', '{n} of {total} entries; the log keeps at most {max} entries and {mb} MiB, the oldest going first, across restarts.',
      '{total} 条中的 {n} 条；日志最多保留 {max} 条、{mb} MiB，最旧的先删除，重启后仍保留。'],
    ['au.when', 'When', '时间'], ['au.members', 'Members', '成员'], ['au.outcome', 'Outcome', '结果'], ['au.detail', 'Detail', '详情'],
    ['au.none', 'No entries match.', '没有匹配的条目。'], ['au.open', 'Open', '打开'],
    ['au.o.ok', 'ok', '成功'], ['au.o.failed', 'failed', '失败'], ['au.o.partial', 'partly', '部分成功'], ['au.o.unknown', 'unknown', '未知'],
    ['au.o.not_sent', 'not sent', '未发送'],
    ['au.entry', '{action} by {user}', '{user} 的{action}'], ['au.versions', 'Versions', '版本'], ['au.digest', 'Digest', '摘要'],
    ['au.diff', 'Diff', '差异'], ['au.nodiff', 'This entry keeps no diff: {why}', '此条目不保存差异：{why}'],
    ['au.nodiff.api', 'what was sent through the node switcher holds the member\'s config and secrets, so only its digest is kept; the member\'s own Versions page compares its versions before and after.',
      '经节点切换器发送的内容包含成员的配置与密钥，因此只保存其摘要；成员自己的“版本”页面可对比前后版本。'],
    ['au.nodiff.op', 'an operation changes no config of the manager\'s; each member\'s versions are in its outcome.', '操作不修改管理端的配置；各成员的版本见其结果。'],
    ['au.nodiff.none', 'nothing of the manager\'s changed.', '管理端没有内容被修改。'],
    ['au.clipped', 'The diff is cut at {kb} KiB; the digest is of the whole of it.', '差异截断于 {kb} KiB；摘要针对完整差异。'],
    ['au.outcomes', 'Members\' outcomes', '各成员结果'], ['au.status', 'Status', '状态'], ['au.config', 'Config', '配置'], ['au.error', 'Error', '错误'],
    ['au.rollout', 'Open the rollout', '打开发布'], ['au.versionsof', '{name}\'s versions', '{name} 的版本'], ['au.of', 'Changes of this member', '此成员的修改'],
    ['nd.a.node.test', 'tested', '测试'], ['nd.a.access.update', 'roles changed', '修改角色'], ['nd.audit.all', 'All changes', '全部修改']
  );

  var ACTIONS = ['node', 'group', 'template', 'op', 'rollout', 'access'];
  var OUTCOME_KIND = { ok: 'ok', failed: 'danger', partial: 'warn', unknown: 'warn', not_sent: 'danger', done: 'ok', aborted: 'warn', rolled_back: 'warn', expired: '' };

  // query reads the hash's query. / 读取哈希中的查询参数。
  function query() {
    var q = {}, i = location.hash.indexOf('?');
    if (i < 0) return q;
    location.hash.slice(i + 1).split('&').forEach(function (p) {
      var j = p.indexOf('=');
      if (j > 0) q[decodeURIComponent(p.slice(0, j))] = decodeURIComponent(p.slice(j + 1));
    });
    return q;
  }
  function qs(q) {
    return Object.keys(q).filter(function (k) { return q[k]; }).map(function (k) { return encodeURIComponent(k) + '=' + encodeURIComponent(q[k]); }).join('&');
  }
  function actionWord(UI, a) { var w = UI.t('nd.a.' + a); return w === 'nd.a.' + a ? a : w; }
  function outcomeBadge(UI, o) {
    if (!o) return null;
    var w = UI.t('au.o.' + o);
    if (w === 'au.o.' + o) w = UI.t('ro.s.' + o) === 'ro.s.' + o ? o : UI.t('ro.s.' + o);
    return UI.badge(w, OUTCOME_KIND[o] || '', true);
  }
  // nodesOf are the members an entry acted on. / 条目所涉及的成员。
  function nodesOf(e) {
    var out = [];
    function add(n) { if (n && out.indexOf(n) < 0) out.push(n); }
    add(e.node);
    (e.nodes || []).forEach(add);
    (e.outcomes || []).forEach(function (o) { add(o.node); });
    return out;
  }
  function diffView(UI, text) {
    var h = UI.h;
    return h('pre.cl-diff', String(text).replace(/\n$/, '').split('\n').map(function (l) {
      var k = l.indexOf('@@') === 0 ? 'hunk' : l.indexOf('+++') === 0 || l.indexOf('---') === 0 ? 'file' : l[0] === '+' ? 'add' : l[0] === '-' ? 'del' : '';
      return h('span' + (k ? '.cl-diff__' + k : ''), l + '\n');
    }));
  }
  function short(d) { d = d || ''; return d.length > 23 ? d.slice(0, 23) + '…' : d; }

  // openEntry shows an entry whole: its diff, digest, versions and outcomes. / 完整显示一个条目：差异、摘要、版本与结果。
  function openEntry(UI, e) {
    var h = UI.h, t = UI.t;
    var nodes = nodesOf(e);
    var digest = e.digest ? h('span.row', h('code.small.cl-fp', { title: e.digest }, short(e.digest)),
      h('button.btn.btn--ghost.btn--icon.btn--sm', { type: 'button', title: t('copy'), 'aria-label': t('copy'), onclick: function () { UI.copy(e.digest); } }, UI.icon('copy'))) : '—';
    var versions = e.before || e.after ? (e.before || '?') + ' → ' + (e.after || '?') : '—';
    var why = e.action === 'node.api' ? 'au.nodiff.api' : e.op ? 'au.nodiff.op' : 'au.nodiff.none';
    var links = [];
    if (e.rollout) links.push(h('a.btn.btn--ghost.btn--sm', { href: '#/cluster/rollout/' + encodeURIComponent(e.rollout) }, UI.icon('send'), t('au.rollout')));
    if (e.action === 'node.api' && e.node) {
      links.push(h('a.btn.btn--ghost.btn--sm', { href: UI.targetURL(e.node, '#/sys.versions/versions'), target: '_blank', rel: 'noopener' },
        UI.icon('history'), t('au.versionsof', { name: e.node })));
    }
    var outcomes = (e.outcomes || []).length ? UI.table([
      { label: t('au.node'), render: function (o) { return h('a', { href: '#/cluster/node/' + encodeURIComponent(o.node) }, o.node); } },
      { label: t('au.status'), render: function (o) { return h('span.small', o.status); } },
      { label: t('au.versions'), render: function (o) { return o.before || o.after ? (o.before || '?') + ' → ' + (o.after || '?') : '—'; } },
      { label: t('au.config'), render: function (o) { return o.digest ? h('code.small', { title: o.digest }, short(o.digest)) : '—'; } },
      { label: t('au.detail'), render: function (o) { return h('span.small.muted', [o.detail, o.error].filter(Boolean).join(' · ')); } }
    ], e.outcomes) : null;
    UI.modal({
      title: t('au.entry', { action: actionWord(UI, e.action), user: e.user || '—' }), wide: true,
      body: h('div.stack',
        UI.kv([
          [t('au.when'), FC.when(e.at)], [t('au.user'), e.user || '—'], [t('au.action'), h('code', e.action)],
          [t('au.members'), nodes.length ? nodes.join(', ') : '—'],
          e.group ? [t('ro.group'), e.group] : null, e.template ? [t('ro.template'), e.template] : null,
          [t('au.outcome'), outcomeBadge(UI, e.outcome) || '—'], [t('au.versions'), versions], [t('au.digest'), digest],
          [t('au.detail'), e.detail || '—']
        ].filter(Boolean)),
        links.length ? h('div.btn-row', links) : null,
        h('div', h('b', t('au.diff'))),
        e.diff ? diffView(UI, e.diff) : h('div.small.muted', t('au.nodiff', { why: t(why) })),
        e.clipped ? h('div.small.muted', t('au.clipped', { kb: 16 })) : null,
        outcomes ? h('div', h('b', t('au.outcomes'))) : null, outcomes)
    });
  }

  // ---- Users and roles / 用户与角色 ----
  function usersPage(UI, sh, el) {
    var h = UI.h, t = UI.t;
    var body = h('div');
    UI.mount(el, body);
    var data = null, etag = '', draft = null;
    var editable = FC.canCluster('admin');

    function read() {
      return FC.call(UI, 'GET', '/api/access').then(function (r) {
        data = r.body || {}; etag = r.etag;
        draft = { default_role: data.default_role || '', users: (data.users || []).map(function (u) {
          return { user: u.user, role: u.role || '', groups: Object.assign({}, u.groups || {}) };
        }) };
        draw();
      }, function (e) { UI.mount(body, h('div.callout.callout--danger', e.message)); });
    }
    function find(name) { for (var i = 0; i < draft.users.length; i++) if (draft.users[i].user === name) return draft.users[i]; return null; }
    function info(name) { for (var i = 0; i < (data.users || []).length; i++) if (data.users[i].user === name) return data.users[i]; return {}; }
    function roleSelect(value, onChange, none, label) {
      var s = h('select.select.select--sm', { 'aria-label': label, onchange: function () { onChange(s.value); } },
        none ? h('option', { value: '' }, t('role.none')) : null,
        FC.ROLES.map(function (r) { return h('option', { value: r, selected: r === value }, FC.roleWord(UI, r)); }));
      s.value = value || '';
      return s;
    }
    // groupsCell is a user's roles on groups: a select each, and one to add a group, for an admin; words for the others.
    // groupsCell 为用户在分组上的角色：管理员看到每个分组一个选择框，以及添加分组的选择框；其他人看到文字。
    function groupsCell(u) {
      var names = Object.keys(u.groups || {}).sort();
      var chips = names.map(function (g) {
        var missing = (data.groups || []).indexOf(g) < 0;
        var role = editable ? roleSelect(u.groups[g], function (v) { u.groups[g] = v; }, false, g + ' · ' + u.user) : h('b', FC.roleWord(UI, u.groups[g]));
        return h('span.cl-rchip' + (missing ? '.cl-rchip--missing' : ''), h('span', g + ':'), role,
          editable ? h('button.cl-rchip__x', { type: 'button', title: t('delete'), 'aria-label': t('delete') + ' ' + g, onclick: function () { delete u.groups[g]; draw(); } }, '×') : null);
      });
      var free = editable ? (data.groups || []).filter(function (g) { return names.indexOf(g) < 0; }) : [];
      if (free.length) {
        var gs = h('select.select.select--sm', { 'aria-label': t('us.addgroup') + ' · ' + u.user }, h('option', { value: '' }, t('us.addgroup') + '…'),
          free.map(function (g) { return h('option', { value: g }, g); }));
        gs.onchange = function () { if (gs.value) { u.groups[gs.value] = 'operator'; draw(); } };
        chips.push(gs);
      }
      return chips.length ? h('span.cl-rchips', chips) : h('span.muted', '—');
    }
    function draw() {
      var me = FC.me() || {};
      var rows = draft.users.slice().sort(function (a, b) { return a.user < b.user ? -1 : 1; });
      var parts = [];
      if (data.read_only) parts.push(h('div.callout.callout--warn', t('readonly', { why: data.read_only })));
      if (data.open) parts.push(h('div.callout', UI.icon('info'), ' ', t('us.open')));
      if (!editable) parts.push(h('div.callout', UI.icon('lock'), ' ', t('us.readonly')));
      parts.push(h('div.small.muted', t('us.roles.hint')));
      var table = UI.table([
        { label: t('us.user'), render: function (u) {
          var i = info(u.user);
          return h('div', h('b', u.user), u.user === me.user ? h('span.small.muted', ' (' + t('us.you') + ')') : null,
            !i.console && i.user ? h('div.small.muted', t('us.notconsole')) : null,
            i.fixed ? h('div', { title: t('us.fixed.title') }, UI.badge(t('us.fixed'), 'info')) : null);
        } },
        { label: t('us.role'), render: function (u) {
          var i = info(u.user);
          if (!editable || i.fixed) return FC.roleWord(UI, i.fixed ? 'admin' : u.role);
          return roleSelect(u.role, function (v) { u.role = v; }, true, t('us.role') + ' · ' + u.user);
        } },
        { label: t('us.groups'), render: groupsCell },
        { label: t('us.assistant'), render: function (u) {
          var i = info(u.user);
          if (!i.assistant) return h('span.muted.small', t('us.noai'));
          if (i.assistant_cap && i.assistant_cap !== i.assistant) {
            return h('span', h('s.muted', i.assistant), ' ', h('b', i.assistant_cap), h('div.small.muted', t('us.cap', { level: i.assistant_cap })));
          }
          return h('span', i.assistant);
        } }
      ], rows, { empty: t('us.none') });
      parts.push(UI.card(null, table, null, true));
      var def = editable ? roleSelect(draft.default_role, function (v) { draft.default_role = v; }, true, t('us.default'))
        : h('b', FC.roleWord(UI, draft.default_role));
      parts.push(h('div.row', UI.field(t('us.default'), def, t('us.default.hint'))));
      if (editable) {
        var name = h('input.input.input--sm', { placeholder: t('us.adduser.ph'), autocomplete: 'off', spellcheck: 'false', 'aria-label': t('us.adduser') });
        var add = UI.btn(t('us.adduser'), 'plus', function () {
          var n = name.value.trim();
          if (!n || find(n)) return;
          draft.users.push({ user: n, role: 'viewer', groups: {} });
          draw();
        }, 'ghost sm');
        var save = UI.btn(t('us.save'), 'save', function () {
          UI.confirm(t('us.save.ask'), t('us.save')).then(function (yes) {
            if (!yes) return;
            var out = { default_role: draft.default_role, users: draft.users.map(function (u) { return { user: u.user, role: u.role, groups: u.groups }; }) };
            UI.busy(save, FC.call(UI, 'PUT', '/api/access', out, etag)).then(function () {
              UI.toast(t('us.saved'));
              FC.loadMe(UI).then(read);
            }, function (e) { UI.toast(e.status === 412 ? t('fm.stale') : e.message, 'error'); if (e.status === 412) read(); });
          });
        }, 'primary');
        save.disabled = !!data.read_only;
        parts.push(h('div.row.row--between', h('span.row', name, add), save));
      }
      UI.mount(body, h('div.stack', parts));
    }
    read();
  }

  // ---- The audit log / 审计日志 ----
  function auditPage(UI, sh, el) {
    var h = UI.h, t = UI.t;
    var q = query();
    var user = h('select.select.select--sm', { 'aria-label': t('au.user') }, h('option', { value: '' }, t('au.anyone')));
    var node = h('input.input.input--sm', { placeholder: t('au.node'), 'aria-label': t('au.node'), list: 'cl-au-nodes', autocomplete: 'off', spellcheck: 'false', value: q.node || '' });
    var nodeList = h('datalist', { id: 'cl-au-nodes' });
    var action = h('select.select.select--sm', { 'aria-label': t('au.action') }, h('option', { value: '' }, t('au.anything')));
    var since = h('input.input.input--sm', { type: 'datetime-local', 'aria-label': t('au.since'), title: t('au.since') });
    var until = h('input.input.input--sm', { type: 'datetime-local', 'aria-label': t('au.until'), title: t('au.until') });
    function toLocal(s) {
      var d = new Date(s || '');
      if (isNaN(d)) return '';
      var off = d.getTimezoneOffset() * 60000;
      return new Date(d.getTime() - off).toISOString().slice(0, 16);
    }
    function toISO(v) { var d = new Date(v || ''); return isNaN(d) ? '' : d.toISOString().replace(/\.\d{3}Z$/, 'Z'); }
    since.value = toLocal(q.since); until.value = toLocal(q.until);
    var exp = h('a.btn.btn--ghost.btn--sm', { title: t('au.export.title'), download: '' }, UI.icon('download'), t('au.export'));
    var filters = h('div.cl-filter', user, node, nodeList, action, since, until,
      UI.btn(t('au.apply'), 'check', apply, 'ghost sm'), UI.btn(t('au.clear'), 'x', function () { location.hash = '#/cluster/audit'; }, 'ghost sm'), exp);
    var note = h('div.small.muted'), list = h('div');
    UI.mount(el, h('div.stack', filters, note, list));
    function current() { return { user: user.value || q.user || '', node: node.value.trim(), action: action.value || q.action || '', since: toISO(since.value), until: toISO(until.value) }; }
    function apply() { location.hash = '#/cluster/audit' + (qs(current()) ? '?' + qs(current()) : ''); }
    node.addEventListener('keydown', function (e) { if (e.key === 'Enter') apply(); });
    var f = { user: q.user || '', node: q.node || '', action: q.action || '', since: q.since || '', until: q.until || '' };
    exp.href = UI.base + '/api/audit?' + qs(Object.assign({ format: 'jsonl' }, f));
    FC.members(UI).then(function (m) {
      UI.mount(nodeList, m.nodes.map(function (n) { return h('option', { value: n.name }); }));
    }, function () { /* ignore / 忽略 */ });
    FC.call(UI, 'GET', '/api/audit?' + qs(f)).then(function (r) {
      var b = r.body || {};
      (b.users || []).forEach(function (u) { user.appendChild(h('option', { value: u, selected: u === f.user }, u)); });
      var acts = ACTIONS.slice();
      (b.actions || []).forEach(function (a) { if (acts.indexOf(a) < 0) acts.push(a); });
      acts.forEach(function (a) {
        var label = ACTIONS.indexOf(a) >= 0 ? a + '.*' : actionWord(UI, a) + ' (' + a + ')';
        action.appendChild(h('option', { value: a, selected: a === f.action }, label));
      });
      var entries = (b.entries || []).slice().reverse();
      UI.mount(note, t('au.kept', { n: entries.length, total: b.total || 0, max: b.max_entries || 0, mb: Math.round((b.max_bytes || 0) / 1048576) }));
      UI.mount(list, UI.card(null, UI.table([
        { label: t('au.when'), render: function (e) { return h('span.small', FC.when(e.at)); } },
        { label: t('au.user'), render: function (e) { return e.user || '—'; } },
        { label: t('au.action'), render: function (e) {
          return h('span', h('span', actionWord(UI, e.action)), e.group ? h('span.small.muted', ' · ' + e.group) : null, e.template ? h('span.small.muted', ' · ' + e.template) : null);
        } },
        { label: t('au.members'), render: function (e) {
          var n = nodesOf(e);
          return h('span.small', n.slice(0, 3).join(', ') + (n.length > 3 ? ' +' + (n.length - 3) : ''));
        } },
        { label: t('au.outcome'), render: function (e) { return outcomeBadge(UI, e.outcome); } },
        { label: t('au.detail'), render: function (e) { return h('span.small.muted.cl-msg', { title: e.detail || '' }, e.detail || ''); } },
        { label: '', cls: 'actions', render: function (e) { return UI.btn(t('au.open'), 'code', function () { openEntry(UI, e); }, 'ghost sm'); } }
      ], entries, { empty: t('au.none'), onRowClick: function (e) { openEntry(UI, e); } }), null, true));
    }, function (e) { UI.mount(list, h('div.callout.callout--danger', e.message)); });
  }

  // page draws #/cluster/users or #/cluster/audit, with the tabs between them. / 绘制带标签页的用户或审计页面。
  FC.pages.users = function (UI, sh, tab) {
    var h = UI.h, t = UI.t;
    var el = sh.page('users', t('n.users'));
    var box = h('div');
    UI.mount(el, h('div.stack', UI.tabs([['users', t('us.users')], ['audit', t('us.audit')]], tab, function (to) {
      if (to !== tab) location.hash = '#/cluster/' + to;
    }), box));
    UI.mount(sh.tools, UI.btn(t('refresh'), 'refresh', function () { FC.pages.users(UI, sh, tab); }, 'ghost sm'));
    if (tab === 'audit') auditPage(UI, sh, box); else usersPage(UI, sh, box);
  };
  FC.auditEntry = openEntry;
})();
