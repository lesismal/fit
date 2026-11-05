// PeersPanel: the mesh of a process — its identity, its rpc listener, who it is connected to (in / out) and the
// addresses it dials. Shared by the node, hub and proxy consoles. Needs ui.js.
//
// PeersPanel：进程的互联 —— 自身身份、rpc 监听、与谁相连（入站 / 出站）以及它主动连接的地址。node、hub、proxy
// 控制台共用。依赖 ui.js。
//
// PeersPanel.mount(el, {call(method, req) → Promise (default UI.rpc), onPeer(conn) optional}) → {refresh}
(function () {
  'use strict';
  // make(UI) builds it on a module's UI (UI.scope), whose requests go to the module's API; PeersPanel.with(UI) is
  // make. / make(UI) 基于模块的 UI（UI.scope）构建，其请求发往该模块的 API；PeersPanel.with(UI) 即 make。
  function make(UI) {    var h = UI.h, t = UI.t;

    UI.addI18n({
      'en': {
        'pp.self': 'This process', 'pp.listen': 'RPC listener', 'pp.not_listening': 'Not listening', 'pp.listening': 'Listening', 'pp.conns': 'Connections',
        'pp.targets': 'Dial targets', 'pp.role': 'Role', 'pp.name': 'Name', 'pp.id': 'ID', 'pp.dir': 'Direction', 'pp.in': 'incoming', 'pp.out': 'outgoing',
        'pp.remote': 'Remote', 'pp.since': 'Connected', 'pp.addr': 'Address', 'pp.state': 'State', 'pp.peer': 'Peer',
        'pp.host': 'Host', 'pp.version': 'Version', 'pp.pid': 'PID', 'pp.started': 'Started', 'pp.console': 'Web console',
        'pp.peer_role': 'Peer role', 'pp.role_hint': 'The peer must announce this role, otherwise it is refused',
        'pp.add': 'Connect to…', 'pp.add_title': 'Connect to a peer', 'pp.addr_hint': 'host:port of the peer\'s rpc listener',
        'pp.password': 'Password of the peer\'s listener', 'pp.remove': 'Disconnect', 'pp.kick': 'Kick',
        'pp.remove_confirm': 'Stop connecting to {a}?', 'pp.kick_confirm': 'Close the connections of {n}? Outgoing ones will reconnect.',
        'pp.no_conns': 'No connections', 'pp.no_targets': 'This process does not dial any peer', 'pp.connected': 'Connected', 'pp.connecting': 'Connecting…',
        'pp.stale': 'Refresh failed, showing the last known state: {m}', 'pp.draining': 'draining',
        'pp.listen_edit': 'Change listener', 'pp.listen_addr': 'Listen address', 'pp.listen_addr_hint': 'e.g. 0.0.0.0:9101; empty stops listening',
        'pp.listen_pw': 'Listener password', 'pp.listen_pw_keep': 'empty keeps the current password', 'pp.listen_pw_hint': 'Peers dialing this process need it (it never goes over the wire)',
        'pp.has_pw': 'password set', 'pp.no_pw': 'no password',
        'pp.pin': 'Certificate pin (SHA-256)', 'pp.pin_hint': 'The fingerprint the peer listener\'s pages show; the link goes over TLS to that certificate alone. A peer off loopback needs a pin or a CA file',
        'pp.ca_file': 'CA file', 'pp.ca_file_hint': 'Or a PEM file of the authorities the peer\'s certificate chains to, on this machine',
        'pp.server_name': 'Server name', 'pp.server_name_hint': 'With a CA file: the name the certificate must have; the address\'s host when empty',
        'pp.tls': 'TLS', 'pp.tls_pin': 'TLS, pinned', 'pp.tls_ca': 'TLS, CA', 'pp.plain': 'plain ws (loopback only)',
        'pp.self_signed': 'TLS with a self-signed certificate this process makes', 'pp.self_signed_hint': 'Peers pin it by the fingerprint shown here; it is kept across restarts',
        'pp.cert_file': 'Certificate file', 'pp.key_file': 'Key file', 'pp.cert_hint': 'PEM files on this machine; with both, links are taken over TLS. A listener off loopback needs TLS',
        'pp.fingerprint': 'Fingerprint (SHA-256)', 'pp.tls_self': 'TLS, self-signed', 'pp.tls_file': 'TLS, certificate file'
      },
      'zh-CN': {
        'pp.self': '本进程', 'pp.listen': 'RPC 监听', 'pp.not_listening': '未监听', 'pp.listening': '监听中', 'pp.conns': '连接',
        'pp.targets': '主动连接目标', 'pp.role': '角色', 'pp.name': '名称', 'pp.id': 'ID', 'pp.dir': '方向', 'pp.in': '入站', 'pp.out': '出站',
        'pp.remote': '远端地址', 'pp.since': '连接时间', 'pp.addr': '地址', 'pp.state': '状态', 'pp.peer': '对端',
        'pp.host': '主机', 'pp.version': '版本', 'pp.pid': 'PID', 'pp.started': '启动时间', 'pp.console': 'Web 控制台',
        'pp.peer_role': '对端角色', 'pp.role_hint': '对端必须声明此角色，否则拒绝连接',
        'pp.add': '连接到…', 'pp.add_title': '连接到对端', 'pp.addr_hint': '对端 rpc 监听的 host:port',
        'pp.password': '对端监听的密码', 'pp.remove': '断开', 'pp.kick': '踢下线',
        'pp.remove_confirm': '停止连接 {a}？', 'pp.kick_confirm': '关闭与 {n} 的连接？出站连接会自动重连。',
        'pp.no_conns': '暂无连接', 'pp.no_targets': '本进程没有主动连接任何对端', 'pp.connected': '已连接', 'pp.connecting': '连接中…',
        'pp.stale': '刷新失败，显示的是上次的状态：{m}', 'pp.draining': '排空中',
        'pp.listen_edit': '修改监听', 'pp.listen_addr': '监听地址', 'pp.listen_addr_hint': '例如 0.0.0.0:9101；留空表示停止监听',
        'pp.listen_pw': '监听密码', 'pp.listen_pw_keep': '留空表示保留当前密码', 'pp.listen_pw_hint': '连接本进程的对端需要此密码（密码不会在网络上传输）',
        'pp.has_pw': '已设置密码', 'pp.no_pw': '未设置密码',
        'pp.pin': '证书指纹（SHA-256）', 'pp.pin_hint': '对端监听页面上显示的指纹；连接走 TLS，且只连向该证书。非回环的对端需要指纹或 CA 文件',
        'pp.ca_file': 'CA 文件', 'pp.ca_file_hint': '或本机上的 PEM 文件，包含对端证书须链接到的证书颁发机构',
        'pp.server_name': '服务器名称', 'pp.server_name_hint': '配合 CA 文件：证书须具有的名称；留空时取地址中的主机',
        'pp.tls': 'TLS', 'pp.tls_pin': 'TLS，固定指纹', 'pp.tls_ca': 'TLS，CA 校验', 'pp.plain': '明文 ws（仅限回环）',
        'pp.self_signed': '以本进程生成的自签名证书走 TLS', 'pp.self_signed_hint': '对端按此处显示的指纹固定它；重启后保持不变',
        'pp.cert_file': '证书文件', 'pp.key_file': '私钥文件', 'pp.cert_hint': '本机上的 PEM 文件；两者都设置时以 TLS 接受连接。非回环的监听需要 TLS',
        'pp.fingerprint': '指纹（SHA-256）', 'pp.tls_self': 'TLS，自签名', 'pp.tls_file': 'TLS，证书文件'
      }
    });

    var ROLES = ['node', 'hub', 'proxy'];
    var ROLE_KIND = { 'node': 'primary', 'hub': 'info', 'proxy': 'ok' };
    function roleBadge(r) { return UI.badge(r || '—', ROLE_KIND[r] || ''); }

    // kickRefresh is how long to wait after a kick before refreshing: outgoing connections need a moment to reconnect.
    // kickRefresh 踢下线后刷新前的等待时间：出站连接需要片刻来重连。
    var kickRefresh = 1500;

    function mount(el, o) {
      o = o || {};
      var call = o.call || UI.rpc;
      var selfRole = '', last = null;
      var note = h('div'), box = h('div');
      UI.mount(el, note, box);
      // act runs an action. Its reply may be lost although it took effect (the connection it came over was replaced,
      // the peer was reconnecting, ...): on an error the current state decides, done(r) tells whether the action is
      // visible in it, and the action then resolves to that state.
      // act 执行操作。操作可能已生效但应答丢失（请求所经的连接被替换、对端正在重连等）：出错时以当前状态为准，done(r) 判断
      // 该操作是否已在其中生效，此时操作以该状态完成。
      function act(p, done) {
        return p.catch(function (e) {
          return call('/mesh/peers', {}).then(function (r) { if (!done(r)) throw e; return r; }, function () { throw e; });
        }).then(function () { return load(true); });
      }
      // A process connects to the other two roles only. / 进程只连接另外两种角色。
      function add() {
        var a = h('input.input', { placeholder: '192.168.1.10:9201', 'data-fill': true }), p = h('input.input', { type: 'password', autocomplete: 'new-password' });
        var role = h('select.select', ROLES.filter(function (r) { return r !== selfRole; }).map(function (r) { return h('option', { value: r }, r); }));
        var pin = h('input.input.mono', { autocomplete: 'off', spellcheck: 'false' });
        var ca = h('input.input.mono', { autocomplete: 'off', spellcheck: 'false', placeholder: '/etc/fit/ca.pem' });
        var sn = h('input.input.mono', { autocomplete: 'off', spellcheck: 'false' });
        UI.modal({
          title: t('pp.add_title'),
          body: h('div.form', UI.field(t('pp.peer_role'), role, t('pp.role_hint')), UI.field(t('pp.addr'), a, t('pp.addr_hint')), UI.field(t('pp.password'), p),
            UI.field(t('pp.pin'), pin, t('pp.pin_hint')), UI.field(t('pp.ca_file'), ca, t('pp.ca_file_hint')), UI.field(t('pp.server_name'), sn, t('pp.server_name_hint'))),
          actions: [{ label: t('cancel') }, {
            label: t('pp.add'), kind: 'primary', onclick: function () {
              var req = { addr: a.value.trim(), password: p.value, role: role.value, pin: pin.value.trim(), ca_file: ca.value.trim(), server_name: sn.value.trim() };
              return act(call('/mesh/dial', req), function (r) {
                return (r.targets || []).some(function (x) { return x.addr === req.addr && x.role === req.role; });
              });
            }
          }]
        });
      }
      // editListen changes the rpc listener (ListenReq; an empty password keeps the current one).
      // editListen 修改 rpc 监听（ListenReq；密码留空表示保留当前密码）。
      function editListen() {
        var ls = (last && last.listen) || {};
        var a = h('input.input', { value: ls.addr || '', placeholder: '0.0.0.0:9101', 'data-fill': true });
        var p = h('input.input', { type: 'password', autocomplete: 'new-password', placeholder: ls.has_password ? t('pp.listen_pw_keep') : '' });
        var ss = h('input', { type: 'checkbox', checked: !!ls.self_signed });
        var cf = h('input.input.mono', { autocomplete: 'off', spellcheck: 'false', value: ls.cert_file || '' });
        var kf = h('input.input.mono', { autocomplete: 'off', spellcheck: 'false', value: ls.key_file || '' });
        UI.modal({
          title: t('pp.listen_edit'),
          body: h('div.form', UI.field(t('pp.listen_addr'), a, t('pp.listen_addr_hint')), UI.field(t('pp.listen_pw'), p, t('pp.listen_pw_hint')),
            h('label.check', ss, t('pp.self_signed')), h('div.field__hint', t('pp.self_signed_hint')),
            h('div.form-row', UI.field(t('pp.cert_file'), cf), UI.field(t('pp.key_file'), kf)), h('div.field__hint', t('pp.cert_hint'))),
          actions: [{ label: t('cancel') }, {
            label: t('save'), kind: 'primary', onclick: function () {
              // The TLS of the listener comes whole with the request: a self-signed certificate already made is kept.
              // 监听的 TLS 设置随请求整体提交：已生成的自签名证书会被保留。
              var req = { addr: a.value.trim(), password: p.value, self_signed: ss.checked, cert_file: cf.value.trim(), key_file: kf.value.trim() };
              return act(call('/mesh/listen', req), function (r) { return (r.listen || {}).addr === req.addr && !(r.listen || {}).error; })
                .then(function () { UI.toast(t('saved')); });
            }
          }]
        });
      }
      function listenView(ls) {
        ls = ls || {};
        var state = ls.error ? UI.badge(t('error'), 'danger', true) : ls.addr ? UI.badge(t('pp.listening'), 'ok', true) : UI.badge(t('pp.not_listening'));
        var tls = !ls.addr ? '—' : ls.tls ? UI.badge(t(ls.self_signed ? 'pp.tls_self' : 'pp.tls_file'), 'ok') : UI.badge(t('pp.plain'), 'warn');
        return h('div.stack',
          UI.kv([[t('pp.state'), state], [t('pp.addr'), ls.addr ? h('code', ls.addr) : '—'],
            [t('pp.listen_pw'), ls.has_password ? t('pp.has_pw') : t('pp.no_pw')], [t('pp.tls'), tls],
            [t('pp.fingerprint'), ls.fingerprint ? h('code.small', { style: { wordBreak: 'break-all' } }, ls.fingerprint) : '—']]),
          ls.error ? h('div.callout.callout--danger', ls.error) : null);
      }
      function draw(r) {
        last = r;
        UI.mount(note);
        var self = r.self || {};
        selfRole = self.role || '';
        var conns = UI.table([
          { label: t('pp.role'), render: function (c) { return roleBadge(c.role); } },
          { label: t('pp.name'), render: function (c) { return h('div', h('b', c.name || c.hostname), h('div.small.muted.mono', c.id)); } },
          { label: t('pp.dir'), render: function (c) { return h('div.row', UI.badge(t(c.direction === 'in' ? 'pp.in' : 'pp.out')), c.draining ? UI.badge(t('pp.draining'), 'warn') : null); } },
          { label: t('pp.remote'), render: function (c) { return h('div', h('span.mono.small', c.remote), c.target ? h('div.small.muted.mono', c.target) : null); } },
          { label: t('pp.since'), render: function (c) { return h('span.small', UI.ago(c.connected_at)); } },
          {
            label: '', cls: 'actions', render: function (c) {
              return UI.btn(t('pp.kick'), 'x', function () {
                UI.confirm(t('pp.kick_confirm', { n: c.name || c.id }), t('pp.kick'), true).then(function (ok) {
                  if (!ok) return;
                  // Kicked once none of its connections at the time is left (outgoing ones come back as new ones).
                  // 当时的连接都已不在即视为已踢下线（出站连接会以新连接的形式回来）。
                  var old = (last.conns || []).filter(function (x) { return x.id === c.id; }).map(function (x) { return x.conn_id; });
                  act(call('/mesh/kick', { id: c.id }), function (r2) {
                    return !(r2.conns || []).some(function (x) { return old.indexOf(x.conn_id) >= 0; });
                  }).then(function () { setTimeout(function () { load(true); }, kickRefresh); }).catch(UI.fail);
                });
              }, 'ghost sm');
            }
          }], r.conns, { empty: t('pp.no_conns'), onRowClick: o.onPeer });
        var targets = UI.table([
          { label: t('pp.addr'), render: function (x) {
            return h('div', h('b.mono', x.addr), h('div.small.muted', x.pin ? h('span', { title: x.pin }, t('pp.tls_pin')) : x.ca_file ? h('span', { title: x.ca_file }, t('pp.tls_ca')) : t('pp.plain')));
          } },
          { label: t('pp.peer_role'), render: function (x) { return roleBadge(x.role); } },
          { label: t('pp.state'), render: function (x) { return x.connected ? UI.badge(t('pp.connected'), 'ok', true) : h('div', UI.badge(t('pp.connecting'), 'warn', true), x.error ? h('div.small', { style: { color: 'var(--danger)' } }, x.error) : null); } },
          { label: t('pp.peer'), render: function (x) { return x.peer ? h('div', h('b', x.peer.name || x.peer.hostname), h('div.small.muted.mono', x.peer.id)) : '—'; } },
          {
            label: '', cls: 'actions', render: function (x) {
              return UI.btn(t('pp.remove'), 'trash', function () {
                UI.confirm(t('pp.remove_confirm', { a: x.addr }), t('pp.remove'), true).then(function (ok) {
                  if (ok) act(call('/mesh/undial', { addr: x.addr }), function (r2) { return !(r2.targets || []).some(function (y) { return y.addr === x.addr; }); }).catch(UI.fail);
                });
              }, 'danger sm');
            }
          }], r.targets, { empty: t('pp.no_targets') });
        UI.mount(box, h('div.stack', { style: { gap: '18px' } },
          h('div.grid.grid--2',
            UI.card(t('pp.self'), UI.kv([[t('pp.role'), roleBadge(self.role)], [t('pp.name'), self.name], [t('pp.id'), h('code', self.id)],
              [t('pp.host'), self.hostname], [t('pp.version'), self.version], [t('pp.pid'), self.pid],
              [t('pp.started'), self.started_at ? UI.fmtTime(self.started_at) : '—'], ['RPC', self.rpc_addr], [t('pp.console'), self.http_url]])),
            UI.card(t('pp.listen'), listenView(r.listen), UI.btn(t('edit'), 'edit', editListen, 'sm'))),
          UI.card(t('pp.conns') + ' (' + (r.conns || []).length + ')', conns, UI.btn(t('refresh'), 'refresh', function () { load(); }, 'sm'), true),
          UI.card(t('pp.targets'), targets, UI.btn(t('pp.add'), 'plus', add, 'primary sm'), true)));
      }
      // load refreshes the panel. A failed background refresh (quiet) keeps the last state with a note instead of a
      // toast every few seconds.
      // load 刷新面板。后台刷新（quiet）失败时保留上次的状态并显示提示，而不是每隔几秒弹一次错误。
      function load(quiet) {
        return call('/mesh/peers', {}).then(draw).catch(function (e) {
          if (e && e.status === 401) return;
          if (!quiet || !last) UI.fail(e);
          if (last) UI.mount(note, h('div.callout.callout--warn', { style: { marginBottom: '18px' } }, t('pp.stale', { m: e && e.message ? e.message : String(e) })));
        });
      }
      load();
      UI.every(5000, function () { if (document.body.contains(el) && !document.querySelector('.modal')) load(true); });
      return { refresh: load };
    }

    return { mount: mount, roleBadge: roleBadge, ROLES: ROLES };
  }
  window.PeersPanel = make(window.UI);
  window.PeersPanel.with = make;
})();
