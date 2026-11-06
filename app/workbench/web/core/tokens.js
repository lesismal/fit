// TokensPanel: the link tokens a listener issues (decision 035), which the tunnel's nodes and proxies and the LLM
// mesh's peers link with in place of a shared password: issue one (its token shown once), list them with their
// expiry, last use, members and links, change a token's expiry, and revoke one, which closes its links at once.
// Shared by the tunnel hub's and the LLM roles' pages. Needs ui.js.
//
// TokensPanel：监听方签发的连接令牌（决策 035），隧道的节点与 proxy、LLM 互联的对端用它代替共享密码连接：签发令牌（令牌
// 只显示一次），列出令牌及其过期时间、最近使用、绑定成员与连接，修改过期时间，以及吊销令牌（其连接立即关闭）。隧道 hub 与
// LLM 各角色的页面共用。依赖 ui.js。
//
// TokensPanel.with(UI).mount(el, {list() → Promise<{tokens}>, create(req), expiry(id, req), revoke(id), warning() → text|null})
//   → {refresh}
(function () {
  'use strict';
  function make(UI) {
    var h = UI.h, t = UI.t;
    UI.addI18n({
      'en': {
        'lt.title': 'Link tokens', 'lt.issue': 'Issue token', 'lt.name': 'Name', 'lt.note': 'Note', 'lt.id': 'ID', 'lt.expires': 'Expires',
        'lt.never': 'never', 'lt.expired': 'expired', 'lt.last_used': 'Last used', 'lt.unused': 'not since fit started',
        'lt.members': 'Bound members', 'lt.links': 'Links', 'lt.expiry': 'Expiry', 'lt.revoke': 'Revoke',
        'lt.none': 'No token yet: issue one for each node, proxy or peer that links here.',
        'lt.ttl': 'Lifetime', 'lt.ttl_hint': 'From now, as 720h or 30d; empty: never expires', 'lt.at': 'Or expires at',
        'lt.never_box': 'Never expires', 'lt.issued': 'Token issued', 'lt.shown_once': 'This is the only time the token is shown: copy it now, and set it as the token of the member that links with it. Only a hash of it is kept.',
        'lt.revoke_q': 'Revoke token {name}? The links that use it close at once, and its members may link with another token.',
        'lt.revoked': 'Token revoked', 'lt.expiry_saved': 'Expiry changed', 'lt.expiry_title': 'Expiry of {name}',
        'lt.note_text': 'Each token is its own credential, of no role: what a link may do follows from the role it says hello as. A member is bound to the first token it links with. Expired tokens are refused and their links closed; revoking one closes its links at once.',
        'lt.password_warn': 'These members still link with the shared password, which the next release refuses: issue each a token, then remove the password. {who}'
      },
      'zh-CN': {
        'lt.title': '连接令牌', 'lt.issue': '签发令牌', 'lt.name': '名称', 'lt.note': '备注', 'lt.id': 'ID', 'lt.expires': '过期时间',
        'lt.never': '永不过期', 'lt.expired': '已过期', 'lt.last_used': '最近使用', 'lt.unused': 'fit 启动以来未使用',
        'lt.members': '绑定成员', 'lt.links': '连接', 'lt.expiry': '过期时间', 'lt.revoke': '吊销',
        'lt.none': '暂无令牌：为每个连接到这里的节点、proxy 或对端签发一个。',
        'lt.ttl': '有效期', 'lt.ttl_hint': '从现在起计算，如 720h 或 30d；留空表示永不过期', 'lt.at': '或在此时过期',
        'lt.never_box': '永不过期', 'lt.issued': '令牌已签发', 'lt.shown_once': '令牌只显示这一次：请立即复制，并把它设为使用它连接的成员的令牌。系统只保存它的哈希。',
        'lt.revoke_q': '吊销令牌 {name}？使用它的连接会立即关闭，其绑定成员可改用其他令牌连接。',
        'lt.revoked': '令牌已吊销', 'lt.expiry_saved': '过期时间已修改', 'lt.expiry_title': '{name} 的过期时间',
        'lt.note_text': '每个令牌都是独立的凭据，不绑定角色：连接能做什么取决于它 hello 时声明的角色。成员绑定到它第一次连接所用的令牌。过期的令牌被拒绝且其连接被关闭；吊销令牌会立即关闭其连接。',
        'lt.password_warn': '以下成员仍在用共享密码连接，下一版本将拒绝它：为每个成员签发令牌，然后移除密码。{who}'
      }
    });

    // expiryFields are the inputs of an expiry: a lifetime, a time, or never. read() → {ttl, expires_at} or null when
    // they are wrong. / 过期时间的输入：有效期、时间点或永不过期。read() 返回 {ttl, expires_at}，有误时为 null。
    function expiryFields(withNever) {
      var ttl = h('input.input.mono', { placeholder: '30d', autocomplete: 'off', spellcheck: 'false' });
      var at = h('input.input', { type: 'datetime-local' });
      var never = h('input', { type: 'checkbox' });
      return {
        nodes: [h('div.form-row', UI.field(t('lt.ttl'), ttl, t('lt.ttl_hint')), UI.field(t('lt.at'), at)),
          withNever ? h('label.check', never, ' ', t('lt.never_box')) : null],
        read: function () {
          if (never.checked) return {};
          var v = ttl.value.trim(), when = at.value ? Math.floor(new Date(at.value).getTime() / 1000) : 0;
          if (v && when) { ttl.focus(); return null; }
          return { ttl: v, expires_at: when };
        }
      };
    }

    function mount(el, o) {
      var warn = h('div'), box = h('div');
      function showIssued(is) {
        var code = h('code', { style: { overflowWrap: 'anywhere', userSelect: 'all' } }, is.token);
        UI.modal({
          title: t('lt.issued') + ': ' + is.name, wide: true,
          body: h('div.stack', h('div.callout.callout--warn', UI.icon('key'), ' ', t('lt.shown_once')),
            h('div.row', { style: { flexWrap: 'nowrap' } }, code, UI.btn(t('copy'), 'copy', function () { UI.copy(is.token); }, 'sm'))),
          actions: [{ label: t('close'), kind: 'primary' }]
        });
      }
      function issue() {
        var name = h('input.input', { autocomplete: 'off', placeholder: 'gpu-1', 'data-fill': true });
        var note = h('input.input', { autocomplete: 'off' });
        var ex = expiryFields(false);
        UI.modal({
          title: t('lt.issue'),
          body: h('div.form', h('div.form-row', UI.field(t('lt.name'), name), UI.field(t('lt.note'), note)), ex.nodes),
          actions: [{ label: t('cancel') }, {
            label: t('lt.issue'), kind: 'primary', onclick: function () {
              var e = ex.read();
              if (!name.value.trim()) { name.focus(); return false; }
              if (!e) return false;
              return o.create({ name: name.value.trim(), note: note.value.trim(), ttl: e.ttl, expires_at: e.expires_at }).then(function (is) {
                load();
                showIssued(is);
              });
            }
          }]
        });
      }
      function expiry(v) {
        var ex = expiryFields(true);
        UI.modal({
          title: t('lt.expiry_title', { name: v.name }),
          body: h('div.form', ex.nodes),
          actions: [{ label: t('cancel') }, {
            label: t('save'), kind: 'primary', onclick: function () {
              var e = ex.read();
              if (!e) return false;
              return o.expiry(v.id, e).then(function () { UI.toast(t('lt.expiry_saved')); load(); });
            }
          }]
        });
      }
      function revoke(v) {
        UI.confirm(t('lt.revoke_q', { name: v.name }), t('lt.revoke'), true).then(function (ok) {
          if (ok) o.revoke(v.id).then(function () { UI.toast(t('lt.revoked')); load(); }).catch(UI.fail);
        });
      }
      function draw(tokens) {
        UI.mount(box, UI.table([
          { label: t('lt.name'), render: function (v) { return h('div', h('b', v.name), v.note ? h('div.small.muted', v.note) : null); } },
          { label: t('lt.id'), render: function (v) { return h('span.mono.small', v.id); } },
          { label: t('lt.expires'), render: function (v) {
            if (!v.expires_at) return h('span.muted', t('lt.never'));
            return h('div', UI.fmtTime(v.expires_at), v.expired ? h('div', UI.badge(t('lt.expired'), 'danger')) : null);
          } },
          { label: t('lt.last_used'), render: function (v) {
            return v.last_used ? h('div', UI.ago(v.last_used), h('div.small.muted.mono', v.last_addr || '')) : h('span.muted', t('lt.unused'));
          } },
          { label: t('lt.members'), render: function (v) {
            return (v.members || []).length ? h('div', v.members.map(function (m) { return h('div.small.mono', m); })) : h('span.muted', '—');
          } },
          { label: t('lt.links'), cls: 'num', render: function (v) { return (v.links || []).length; } },
          { label: '', cls: 'actions', render: function (v) {
            return h('div.btn-row', { style: { justifyContent: 'flex-end' } },
              UI.btn(t('lt.expiry'), 'history', function () { expiry(v); }, 'ghost sm'),
              UI.btn(t('lt.revoke'), 'trash', function () { revoke(v); }, 'danger sm'));
          } }
        ], tokens, { empty: t('lt.none') }));
      }
      function load() {
        var w = o.warning ? o.warning() : null;
        UI.mount(warn, w ? h('div.callout.callout--warn', { style: { marginBottom: '12px' } }, UI.icon('alert'), ' ', w) : null);
        return o.list().then(function (r) { draw((r && r.tokens) || []); }).catch(UI.fail);
      }
      UI.mount(el, h('div.stack', warn, UI.card(t('lt.title'), box, UI.btn(t('lt.issue'), 'key', issue, 'primary sm'), true),
        h('div.callout', UI.icon('info'), ' ', t('lt.note_text'))));
      load();
      return { refresh: load };
    }
    // passwordWarning is the warning of the members in who that link with the shared password, or null.
    // passwordWarning 是用共享密码连接的成员 who 的警告，没有则为 null。
    function passwordWarning(who) { return who && who.length ? t('lt.password_warn', { who: who.join(', ') }) : null; }
    return { mount: mount, passwordWarning: passwordWarning };
  }
  window.TokensPanel = make(window.UI);
  window.TokensPanel.with = make;
})();
