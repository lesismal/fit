// The account module: the signed-in console user's own password, and the console users' passwords. Both pages read
// GET /api/users first, which says who the session belongs to and whether a store keeps the users (store: true): a
// change needs one, because the console users are saved in admin.store, not in the config. Passwords are changed with
// POST /api/account/password (self) and POST /api/users/password (another account); the browser never sees a hash.
// Every console user is an administrator, so a reset changes another account's password at once. The node page reads
// GET /api/node: who this fit is (its node ID, made once and kept in the store or beside the config), its host, the
// versions of fit and fib, and what the API offers a manager.
//
// 账户模块：当前控制台用户的密码，以及各控制台用户的密码。两个页面都先读取 GET /api/users，它给出会话所属用户以及
// 用户是否由 store 保存（store: true）：修改需要 store，因为控制台用户保存在 admin.store 中而非配置里。修改密码使用
// POST /api/account/password（自己）与 POST /api/users/password（其他账户）；浏览器不会看到哈希。每个控制台用户都是
// 管理员，因此重置会立即修改对方账户的密码。节点页读取 GET /api/node：本 fit 是谁（节点 ID，只生成一次，保存在 store 或配置
// 旁的文件中）、主机、fit 与 fib 的版本，以及 API 向管理端提供的能力。
(function () {
  'use strict';

  // [key, en, zh] / [键, 英文, 中文]
  var WORDS = [
    ['m.account', 'Account', '账户'],
    ['n.password', 'My password', '我的密码'], ['n.users', 'Users', '用户'],
    ['pw.who', 'Signed in as {user}.', '当前登录：{user}。'],
    ['pw.current', 'Current password', '当前密码'], ['pw.new', 'New password', '新密码'],
    ['pw.confirm', 'Confirm the new password', '确认新密码'],
    ['pw.hint', 'At most 72 bytes (bcrypt\'s limit). Changing it ends the account\'s other sessions.',
      '最多 72 字节（bcrypt 的限制）。修改后，该账户的其他会话会结束。'],
    ['pw.submit', 'Change password', '修改密码'], ['pw.done', 'Password changed for {user}.', '{user} 的密码已修改。'],
    ['pw.need', 'Enter the current password and the new one.', '请输入当前密码和新密码。'],
    ['pw.mismatch', 'The new passwords do not match.', '两次输入的新密码不一致。'],
    ['store.off', 'Password changes need a store: this fit runs without admin.store, so a changed console password would not be saved. Start fit with admin.store to change passwords here.',
      '修改密码需要 store：本 fit 未配置 admin.store，修改后的控制台密码无法保存。请以 admin.store 启动 fit 后再在此修改密码。'],
    ['u.title', 'Console users', '控制台用户'], ['u.user', 'Username', '用户名'], ['u.you', 'you', '当前'],
    ['u.reset', 'Reset password', '重置密码'], ['u.reset.title', 'Reset the password of {user}', '重置 {user} 的密码'],
    ['u.note', 'Every console user is an administrator: a reset changes that account\'s password at once and ends its sessions.',
      '每个控制台用户都是管理员：重置会立即修改该账户的密码，并结束其会话。'],
    ['u.done', 'Password reset for {user}.', '{user} 的密码已重置。'], ['u.need', 'Enter the new password.', '请输入新密码。'],
    ['u.none', 'No console users.', '没有控制台用户。'],
    ['n.node', 'This node', '本节点'],
    ['nd.title', 'Node', '节点'], ['nd.versions', 'Versions', '版本'], ['nd.config', 'Running config', '运行中的配置'],
    ['nd.api', 'For a manager', '供管理端使用'],
    ['nd.id', 'Node ID', '节点 ID'], ['nd.kept', 'Kept in', '保存位置'],
    ['nd.kept.config', 'the config (admin.node_id)', '配置（admin.node_id）'],
    ['nd.kept.store', 'the store (admin.store)', 'store（admin.store）'],
    ['nd.kept.file', 'a file beside the config', '配置旁的文件'],
    ['nd.kept.memory', 'memory only: a new ID after each restart', '仅内存：每次重启后会生成新的 ID'],
    ['nd.host', 'Host name', '主机名'], ['nd.started', 'Started', '启动时间'], ['nd.uptime', 'Uptime', '运行时长'],
    ['nd.fit', 'fit', 'fit'], ['nd.fib', 'fib', 'fib'], ['nd.go', 'Go and platform', 'Go 与平台'],
    ['nd.version', 'Version', '版本'], ['nd.etag', 'ETag', 'ETag'], ['nd.applied', 'Applied', '应用时间'], ['nd.source', 'Source', '来源'],
    ['nd.roles', 'Roles running', '运行中的角色'], ['nd.noroles', 'none', '无'],
    ['nd.schema', 'Config schema', '配置 schema'], ['nd.caps', 'Capabilities', '能力'],
    ['nd.copy', 'Copy the node ID', '复制节点 ID']
  ];

  WB.module({
    id: 'account', server: 'admin', icon: 'users', label: 'm.account', words: WORDS,
    nav: [{ id: 'password', icon: 'key', label: 'n.password' }, { id: 'users', icon: 'users', label: 'n.users' },
      { id: 'node', icon: 'server', label: 'n.node' }],
    setup: function (UI, sh) {
      var h = UI.h, t = UI.t;

      // users reads the console users from the console's own API (scoped to admin): who the session is, whether a
      // store keeps them, and their names. / 从控制台自己的 API（限定在 admin）读取控制台用户：会话用户、是否有 store 保存、用户名。
      function users() { return UI.api('GET', '/api/users'); }

      // storeNotice says why password changes are off. / 说明密码修改为何不可用。
      function storeNotice() { return h('div.callout.callout--warn', UI.icon('info'), ' ', t('store.off')); }
      // errorBox is a hidden error callout. / 隐藏的错误提示。
      function errorBox() { return h('div.callout.callout--danger', { hidden: true }); }
      function showError(box, e) { box.textContent = e && e.message ? e.message : String(e); box.hidden = false; }

      // load reads the users and draws the page with them, or the error of the read. / 读取用户后绘制页面，失败则显示错误。
      function load(el, draw) {
        UI.mount(el, h('div.row', h('span.spinner'), h('span.muted', t('loading'))));
        users().then(function (v) {
          if (!document.body.contains(el)) return;
          draw(el, v || {});
        }, function (e) {
          if (!document.body.contains(el) || (e && e.status === 401)) return;
          UI.mount(el, h('div.callout.callout--danger', e && e.message ? e.message : String(e)));
        });
      }

      // ---- My password / 我的密码 ----
      function passwordPage(el, v) {
        var store = v.store !== false, me = v.user || '';
        var cur = h('input.input', { type: 'password', name: 'current-password', autocomplete: 'current-password', disabled: !store });
        var np = h('input.input', { type: 'password', name: 'new-password', autocomplete: 'new-password', disabled: !store });
        var cf = h('input.input', { type: 'password', name: 'confirm-password', autocomplete: 'new-password', disabled: !store });
        var err = errorBox();
        var btn = h('button.btn.btn--primary', { type: 'submit', disabled: !store }, UI.icon('key'), t('pw.submit'));
        var form = h('form.form.acct-form', {
          onsubmit: function (e) {
            e.preventDefault();
            err.hidden = true;
            if (!store) return;
            if (!cur.value || !np.value) { showError(err, t('pw.need')); return; }
            if (np.value !== cf.value) { showError(err, t('pw.mismatch')); return; }
            UI.busy(btn, UI.api('POST', '/api/account/password', { current_password: cur.value, password: np.value })).then(function (r) {
              cur.value = ''; np.value = ''; cf.value = '';
              UI.toast(t('pw.done', { user: (r && r.user) || me }));
            }, function (e2) { showError(err, e2); });
          }
        }, UI.field(t('pw.current'), cur), UI.field(t('pw.new'), np, t('pw.hint')), UI.field(t('pw.confirm'), cf), err,
          h('div.row', btn));
        UI.mount(el, h('div.stack', store ? null : storeNotice(),
          h('div.small.muted', t('pw.who', { user: me })),
          UI.card(t('n.password'), form)));
      }

      function password() {
        var el = sh.page('password', t('n.password'));
        function draw() { load(el, passwordPage); }
        UI.mount(sh.tools, UI.btn(t('refresh'), 'refresh', draw, 'ghost sm'));
        draw();
      }

      // ---- Users / 用户 ----
      // reset opens the modal that sets another account's password. / 打开为其他账户设置密码的弹窗。
      function reset(username) {
        var np = h('input.input', { type: 'password', name: 'new-password', autocomplete: 'new-password' });
        var cf = h('input.input', { type: 'password', name: 'confirm-password', autocomplete: 'new-password' });
        var err = errorBox();
        function save() {
          err.hidden = true;
          if (!np.value) { showError(err, t('u.need')); return false; }
          if (np.value !== cf.value) { showError(err, t('pw.mismatch')); return false; }
          return UI.api('POST', '/api/users/password', { username: username, password: np.value }).then(function () {
            UI.toast(t('u.done', { user: username }));
          }, function (e) { showError(err, e); return false; });
        }
        UI.modal({
          title: t('u.reset.title', { user: username }),
          body: h('div.stack', h('div.small.muted', t('u.note')),
            UI.field(t('pw.new'), np, t('pw.hint')), UI.field(t('pw.confirm'), cf), err),
          actions: [{ label: t('cancel') }, { label: t('u.reset'), kind: 'primary', onclick: save }]
        });
      }

      function usersPage(el, v) {
        var store = v.store !== false, me = v.user || '', names = v.users || [];
        var table = UI.table([
          { label: t('u.user'), render: function (name) {
            return h('span', h('b', name), name === me ? [' ', UI.badge(t('u.you'), 'primary')] : null);
          } },
          { label: '', cls: 'actions', render: function (name) {
            var title = t('u.reset') + ' ' + name;
            return h('button.btn.btn--ghost.btn--sm', {
              type: 'button', title: title, 'aria-label': title, disabled: !store,
              onclick: function () { reset(name); }
            }, UI.icon('key'), t('u.reset'));
          } }
        ], names, { empty: t('u.none') });
        UI.mount(el, h('div.stack', store ? h('div.callout', UI.icon('info'), ' ', t('u.note')) : storeNotice(),
          UI.card(t('u.title'), h('div.acct-table', table), null, true)));
      }

      function usersList() {
        var el = sh.page('users', t('n.users'));
        function draw() { load(el, usersPage); }
        UI.mount(sh.tools, UI.btn(t('refresh'), 'refresh', draw, 'ghost sm'));
        draw();
      }

      // ---- This node / 本节点 ----
      // when is an RFC 3339 time as the workbench writes times. / 按工作台的格式显示 RFC 3339 时间。
      function when(s) { var ms = Date.parse(s || ''); return isNaN(ms) ? '—' : UI.fmtTime(Math.floor(ms / 1000)); }

      function nodePage(el, n) {
        var kept = ['config', 'store', 'file'].indexOf(n.id_kept) >= 0 ? t('nd.kept.' + n.id_kept) : t('nd.kept.memory');
        var copy = h('button.btn.btn--ghost.btn--icon.btn--sm', { type: 'button', title: t('nd.copy'), 'aria-label': t('nd.copy'),
          onclick: function () { UI.copy(n.id || ''); } }, UI.icon('copy'));
        var roles = n.roles && n.roles.length ? h('span.btn-row', n.roles.map(function (r) { return UI.badge(r); })) : t('nd.noroles');
        var caps = h('span.btn-row', (n.capabilities || []).map(function (c) { return UI.badge(c); }));
        UI.mount(el, h('div.stack',
          UI.card(t('nd.title'), UI.kv([
            [t('nd.id'), h('span.row', h('code.node-id', n.id || '—'), copy)],
            [t('nd.kept'), n.id_kept === 'memory' ? UI.badge(kept, 'warn') : kept],
            [t('nd.host'), n.hostname],
            [t('nd.started'), when(n.started)],
            [t('nd.uptime'), UI.fmtDur(n.uptime_seconds)]
          ])),
          UI.card(t('nd.versions'), UI.kv([
            [t('nd.fit'), h('code', n.fit_version || '—')],
            [t('nd.fib'), h('code', n.fib_version || '—')],
            [t('nd.go'), (n.go_version || '') + ' ' + (n.os || '') + '/' + (n.arch || '')]
          ])),
          UI.card(t('nd.config'), UI.kv([
            [t('nd.version'), String(n.config_version == null ? '' : n.config_version)],
            [t('nd.etag'), h('code', n.config_etag || '—')],
            [t('nd.applied'), when(n.config_applied)],
            [t('nd.source'), n.config_source],
            [t('nd.roles'), roles]
          ])),
          UI.card(t('nd.api'), UI.kv([
            [t('nd.schema'), h('code.node-digest', 'v' + n.schema_version + ' ' + (n.schema_digest || ''))],
            [t('nd.caps'), caps]
          ]))));
      }

      function node() {
        var el = sh.page('node', t('n.node'));
        function draw() {
          UI.mount(el, h('div.row', h('span.spinner'), h('span.muted', t('loading'))));
          UI.api('GET', '/api/node').then(function (v) {
            if (document.body.contains(el)) nodePage(el, v || {});
          }, function (e) {
            if (!document.body.contains(el) || (e && e.status === 401)) return;
            UI.mount(el, h('div.callout.callout--danger', e && e.message ? e.message : String(e)));
          });
        }
        UI.mount(sh.tools, UI.btn(t('refresh'), 'refresh', draw, 'ghost sm'));
        draw();
      }

      return { routes: { password: password, users: usersList, node: node } };
    }
  });
})();
