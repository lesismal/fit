// The Cluster menu's shared parts (I142), for a fit with admin.cluster, the manager of other fits: its words, the
// colours of a member's state, the words of each kind of problem, the requests to the manager's module cluster
// (/api/m/cluster/, with the members' ETag as If-Match on every change), the member's form (add and edit, its
// credentials written once and never shown), a test's result with the certificate a person trusts by its fingerprint,
// and the confirm of a removal. The pages are overview.js, nodes.js and member.js; cluster.js registers the module.
// Every function takes the module's scoped UI, whose requests go to the module's API and whose words are its own.
//
// Where later iterations add to these pages, without editing them:
//   FitCluster.addAction(fn)  fn(UI, ctx) → a button for the Nodes page's bar, beside Add a node (I149's Add by
//                             enrollment), or null; ctx is {readOnly, reload()}
//   FitCluster.rowExtra(fn)   fn(UI, member, state) → a node shown beside a member's state on the Overview's cards and
//                             rows and the Nodes page's rows (I149's linked or waiting), or null
//   FitCluster.overviewCard(fn) fn(UI) → a node shown on the Overview above its filter (I206's BitTorrent card), or null
//
// Cluster 菜单的共用部分（I142），供设置了 admin.cluster 的 fit（其他 fit 的管理端）使用：词条、成员状态的颜色、各类问题的
// 说明、对管理端模块 cluster 的请求（/api/m/cluster/，每次修改以成员列表的 ETag 作为 If-Match）、成员表单（添加与编辑；凭据
// 只写入一次，从不显示）、测试结果及供人按指纹信任的证书，以及删除前的确认。页面在 overview.js、nodes.js 与 member.js 中；
// cluster.js 注册模块。每个函数都接收模块限定的 UI，其请求发往模块的 API，词条为模块自己的。
// 后续迭代的扩展点（无需修改这些页面）：FitCluster.addAction(fn) 在节点页的工具栏“添加节点”旁加按钮（I149 的“通过注册添加”）；
// FitCluster.rowExtra(fn) 在概览卡片与行、节点页的行中成员状态旁加内容（I149 的已连接或等待中）。
(function () {
  'use strict';

  // [key, en, zh] / [键, 英文, 中文]
  var WORDS = [
    ['m.cluster', 'Cluster', '集群'],
    ['n.overview', 'Overview', '概览'], ['n.nodes', 'Nodes', '节点'],
    // States / 状态
    ['st.up', 'Up', '正常'], ['st.down', 'Down', '离线'], ['st.error', 'Error', '错误'], ['st.unknown', 'Not read yet', '尚未读取'],
    ['st.all', 'All states', '全部状态'],
    // Kinds of problems / 问题类型
    ['k.unreachable', 'Unreachable', '无法连接'],
    ['k.unreachable.hint', 'No connection: refused, no route or no such host. Is the member running, and is its URL right?',
      '无法建立连接：被拒绝、无路由或主机不存在。成员是否在运行？URL 是否正确？'],
    ['k.timeout', 'Timed out', '超时'],
    ['k.timeout.hint', 'The member did not answer within admin.cluster.timeout: it is overloaded, or something on the way drops the packets.',
      '成员未在 admin.cluster.timeout 内应答：它负载过高，或途中有设备丢弃了数据包。'],
    ['k.tls_untrusted', 'Certificate not trusted', '证书不受信任'],
    ['k.tls_untrusted.hint', 'Neither the system\'s CAs nor the member\'s CA trust its certificate. Compare its fingerprint with the member\'s own, then trust it, or give the CA that signs it.',
      '系统 CA 与成员的 CA 都不信任其证书。请将其指纹与成员自身的证书指纹比对后信任它，或填写签发它的 CA。'],
    ['k.tls_pin_mismatch', 'Certificate changed', '证书已变化'],
    ['k.tls_pin_mismatch.hint', 'The member presents a certificate other than the one pinned. Trust the new one only if you know it was renewed; otherwise something else answers at its address.',
      '成员出示的证书不是已固定的那张。只有确认证书确实已更新时才信任新证书；否则可能是别的服务在该地址应答。'],
    ['k.tls', 'TLS failed', 'TLS 失败'],
    ['k.tls.hint', 'The TLS handshake failed: is the URL https to a port that serves TLS?', 'TLS 握手失败：URL 是否以 https 指向提供 TLS 的端口？'],
    ['k.auth_failed', 'Credentials refused', '凭据被拒绝'],
    ['k.auth_failed.hint', 'The member refused the user name and password, or the token. Edit the member with the right ones; a refused login is not tried again for a while unless the member is changed or tested.',
      '成员拒绝了用户名与密码或令牌。请编辑成员填写正确的凭据；登录被拒后一段时间内不会重试，除非修改或测试该成员。'],
    ['k.rate_limited', 'Logins slowed', '登录受限'],
    ['k.rate_limited.hint', 'The member refuses logins for a while after too many failed ones. The manager waits what it asks.',
      '失败的登录过多，成员暂时拒绝登录。管理端会等待其要求的时间。'],
    ['k.password_change_required', 'Password change needed', '需要修改密码'],
    ['k.password_change_required.hint', 'The member\'s console asks for the user\'s temporary password to be changed. Change it on the member, then edit the member here with the new one.',
      '成员的控制台要求先修改该用户的临时密码。请在成员上修改，然后在此编辑成员填写新密码。'],
    ['k.forbidden', 'Forbidden', '被禁止'],
    ['k.forbidden.hint', 'The member refused the call (403).', '成员拒绝了该调用（403）。'],
    ['k.unsupported', 'Not supported', '不支持'],
    ['k.unsupported.hint', 'The member does not serve what the manager reads: it is not a fit, or a fit older than cluster management.',
      '成员不提供管理端读取的接口：它不是 fit，或是早于集群管理的 fit。'],
    ['k.http_status', 'Unexpected status', '意外的状态码'],
    ['k.http_status.hint', 'The member answered a status the manager did not expect. Is the URL the member\'s console, or its Admin API for a token?',
      '成员应答了管理端未预期的状态码。URL 是否为成员的控制台地址（使用令牌时为其 Admin API 地址）？'],
    ['k.bad_answer', 'Bad answer', '应答无效'],
    ['k.bad_answer.hint', 'The member\'s answer is not the JSON a fit answers.', '成员的应答不是 fit 应答的 JSON。'],
    ['k.too_large', 'Answer too large', '应答过大'],
    ['k.too_large.hint', 'The member\'s answer is larger than admin.cluster.max_response_bytes.', '成员的应答大于 admin.cluster.max_response_bytes。'],
    // Facts / 信息
    ['f.health', 'Health', '健康'], ['f.fit', 'fit', 'fit'], ['f.config', 'Config version', '配置版本'], ['f.drift', 'Drift', '不一致'],
    ['f.cert', 'Nearest certificate', '最近到期的证书'], ['f.conns', 'Connections', '连接'], ['f.latency', 'Latency', '延迟'],
    ['f.polled', 'Read', '最近读取'], ['f.since', 'since {ago}', '{ago}起'], ['f.failures', '{n} failed polls in a row', '连续 {n} 次读取失败'],
    ['f.asof', 'The values below are as last read, {ago}.', '以下数值为最后一次成功读取时（{ago}）的值。'],
    ['f.days', '{n} days', '{n} 天'], ['f.expired', 'expired', '已过期'], ['f.nocert', 'none', '无'],
    ['f.drift.none', 'in sync', '一致'], ['f.drift.n', '{n} differences', '{n} 处差异'],
    ['f.backends.down', '{n} of {all} backends down', '{all} 个后端中 {n} 个不可用'],
    ['f.ms', '{n} ms', '{n} 毫秒'], ['f.retry', 'retried in {n}s', '{n} 秒后重试'],
    // Fields of a member / 成员字段
    ['fm.name', 'Name', '名称'], ['fm.name.hint', 'How the manager names the member: letters, digits, \'_\', \'.\' and \'-\'.',
      '管理端对成员的称呼：字母、数字、“_”、“.”与“-”。'],
    ['fm.labels', 'Labels', '标签'], ['fm.labels.hint', 'key=value, separated by commas or lines, as env=prod, region=eu. The overview filters by them.',
      'key=value，以逗号或换行分隔，如 env=prod, region=eu。概览按标签筛选。'],
    ['fm.url', 'URL', 'URL'], ['fm.url.hint', 'The member\'s console (or its Admin API, for a token): https, or http to a loopback address only.',
      '成员的控制台地址（使用令牌时为其 Admin API 地址）：https，或仅限回环地址的 http。'],
    ['fm.access', 'Access', '访问方式'], ['fm.access.console', 'Console: user name and password', '控制台：用户名与密码'],
    ['fm.access.admin_api', 'Admin API: bearer token', 'Admin API：Bearer 令牌'],
    ['fm.username', 'User name', '用户名'], ['fm.password', 'Password', '密码'], ['fm.token', 'Token', '令牌'],
    ['fm.secret.kept', 'Saved — leave empty to keep it', '已保存——留空则保留'],
    ['fm.secret.hint', 'Written once, kept encrypted in the manager\'s store, and never shown again, here or anywhere.',
      '只写入一次，加密保存在管理端的 store 中，之后在此处或任何地方都不再显示。'],
    ['fm.tls', 'Certificate', '证书'], ['fm.tls.system', 'Trusted by the system\'s CAs', '由系统 CA 信任'],
    ['fm.tls.ca', 'Trusted by a CA of its own (PEM)', '由指定的 CA 信任（PEM）'],
    ['fm.tls.pin', 'Pinned by its fingerprint', '按指纹固定'],
    ['fm.ca', 'CA certificates (PEM)', 'CA 证书（PEM）'], ['fm.fingerprint', 'SHA-256 fingerprint', 'SHA-256 指纹'],
    ['fm.fingerprint.hint', 'Test the member to read it, and compare it with the member\'s own: openssl x509 -noout -fingerprint -sha256 -in cert.pem.',
      '测试成员即可读到它，请与成员自身的指纹比对：openssl x509 -noout -fingerprint -sha256 -in cert.pem。'],
    ['fm.http', 'http: no certificate to verify.', 'http：没有需要验证的证书。'],
    ['fm.add', 'Add a node', '添加节点'], ['fm.edit', 'Edit {name}', '编辑 {name}'],
    ['fm.test', 'Test', '测试'], ['fm.save', 'Save', '保存'], ['fm.testing', 'Testing…', '测试中…'],
    ['fm.added', '{name} added.', '已添加 {name}。'], ['fm.saved', '{name} saved.', '已保存 {name}。'],
    ['fm.stale', 'The members changed since this page read them: they were read again. Check and save again.',
      '本页读取后成员已变化：已重新读取。请检查后再次保存。'],
    // Tests and certificates / 测试与证书
    ['ts.ok', 'Reached in {ms} ms: fit {fit} on {host}.', '{ms} 毫秒内连通：{host} 上的 fit {fit}。'],
    ['ts.failed', 'The test failed', '测试失败'], ['ts.node', 'Node ID', '节点 ID'],
    ['ts.mustchange', 'The member asks for its temporary password to be changed first.', '成员要求先修改其临时密码。'],
    ['ct.title', 'The member\'s certificate', '成员的证书'], ['ct.fp', 'SHA-256 fingerprint', 'SHA-256 指纹'],
    ['ct.subject', 'Subject', '主题'], ['ct.issuer', 'Issuer', '签发者'], ['ct.names', 'Names', '名称'],
    ['ct.valid', 'Valid', '有效期'], ['ct.self', 'self-signed', '自签名'], ['ct.verified', 'Verified by', '验证方式'],
    ['ct.v.pin', 'the pinned fingerprint', '固定的指纹'], ['ct.v.ca', 'the member\'s CA', '成员的 CA'], ['ct.v.system', 'the system\'s CAs', '系统 CA'],
    ['ct.v.none', 'not trusted', '不受信任'],
    ['ct.trust', 'Trust this certificate', '信任此证书'],
    ['ct.trust.title', 'Trust the certificate of {name}', '信任 {name} 的证书'],
    ['ct.trust.note', 'Compare this fingerprint with the one the member itself shows (its certificate\'s file, its console\'s page or its log) before trusting it. Once trusted, the manager accepts this certificate alone for the member, whatever its names and dates.',
      '信任前，请将此指纹与成员自身显示的指纹（其证书文件、控制台页面或日志）比对。信任之后，管理端对该成员只接受这一张证书，不论其名称与有效期。'],
    ['ct.trust.changed', 'This certificate is not the one pinned before. Trust it only if you know the member\'s certificate was replaced.',
      '这张证书不是之前固定的那张。只有确认成员的证书已被更换时才信任它。'],
    ['ct.trusted', 'The certificate of {name} is trusted.', '已信任 {name} 的证书。'],
    ['ct.pinned', 'Pinned in the form and tested again: save to keep it.', '已固定到表单中并再次测试：保存后生效。'],
    // Removal / 删除
    ['rm.ask', 'Remove the node {name}? The manager logs its session out and forgets its credentials; the member itself keeps running as it is.',
      '删除节点 {name}？管理端将注销其会话并删除其凭据；成员本身照常运行。'],
    ['rm.ok', 'Remove', '删除'], ['rm.done', '{name} removed.', '已删除 {name}。'],
    // Ask AI / 问 AI
    ['ask.member', 'The cluster member {name} is {status}: {kind}. {message} Why, and what should I do?',
      '集群成员 {name} 状态为{status}：{kind}。{message} 原因是什么？我该怎么做？'],
    ['readonly', 'The manager is read-only: {why}', '管理端为只读：{why}'],
    // Roles (I148) / 角色（I148）
    ['role.viewer', 'viewer', '查看者'], ['role.operator', 'operator', '操作员'], ['role.admin', 'admin', '管理员'], ['role.none', 'none', '无'],
    ['role.norole', 'You have no role in this cluster, so its pages show nothing to you. An admin of the cluster gives roles on the Users and audit page.',
      '你在此集群中没有角色，因此其页面不向你显示任何内容。集群管理员可在“用户与审计”页面分配角色。'],
    ['role.forbidden', 'Your role does not allow this: {why}', '你的角色不允许此操作：{why}']
  ];

  // KINDS are the kinds of a member's problems, as the manager names them. / 管理端对成员问题的分类。
  var KINDS = ['unreachable', 'timeout', 'tls_untrusted', 'tls_pin_mismatch', 'tls', 'auth_failed', 'rate_limited',
    'password_change_required', 'forbidden', 'unsupported', 'http_status', 'bad_answer', 'too_large'];
  // STATE_KIND is the badge colour of each state; ORDER sorts the states worst first. / 各状态的徽章颜色；ORDER 按严重程度排序。
  var STATE_KIND = { up: 'ok', down: 'danger', error: 'warn', unknown: '' };
  var ORDER = { down: 0, error: 1, unknown: 2, up: 3 };

  var addActions = [], rowExtras = [], overviewCards = [];

  // ---- Roles (I148) / 角色（I148） ----
  // me is the user's roles, GET /api/me, read again before each page is drawn (cluster.js): {user, open, role (cluster-
  // wide), top (the highest anywhere), groups {name: role}, nodes {name: role}, manager_read_only}. The pages draw only
  // the controls the roles allow; the manager refuses the rest whatever the page sends. Until it is read, or when it
  // cannot be, everything is drawn and the manager decides.
  // me 为用户的角色（GET /api/me），每次绘制页面前重新读取（cluster.js）：{user, open, role（集群范围）, top（任意处最高）,
  // groups {名称: 角色}, nodes {名称: 角色}, manager_read_only}。页面只绘制角色允许的控件；无论页面发送什么，其余的都由
  // 管理端拒绝。读到之前或无法读取时，所有控件照常绘制，由管理端决定。
  var me = null;
  var RANK = { viewer: 1, operator: 2, admin: 3 };
  function rank(r) { return RANK[r] || 0; }
  function loadMe(UI) {
    return call(UI, 'GET', '/api/me').then(function (r) { me = r.body || null; return me; }, function () { me = null; return null; });
  }
  // canCluster: the role cluster-wide; canAny: anywhere; canOn: on the member node; canAll: on every member of names;
  // canGroup: on the group g. / canCluster：集群范围；canAny：任意处；canOn：成员 node 上；canAll：names 中每个成员上；
  // canGroup：分组 g 上。
  function canCluster(role) { return !me || rank(me.role) >= rank(role); }
  function canAny(role) { return !me || rank(me.top) >= rank(role); }
  function canOn(role, node) { return !me || rank((me.nodes || {})[node] || me.role) >= rank(role); }
  function canAll(role, names) { return (names || []).every(function (n) { return canOn(role, n); }); }
  function canGroup(role, g) { return !me || rank((me.groups || {})[g] || me.role) >= rank(role); }
  function noRole() { return !!me && !me.open && !me.top; }
  function roleWord(UI, r) { return UI.t('role.' + (RANK[r] ? r : 'none')); }

  // ---- Requests / 请求 ----
  // call sends method path to the module, with the ETag as If-Match when given: a Promise of {status, body, etag}; a
  // status that is not 2xx rejects with an Error carrying status, field (a field refused) and problem (a member's).
  // call 向模块发送 method path（给出 ETag 时作为 If-Match）：返回 {status, body, etag} 的 Promise；非 2xx 时 reject 一个带
  // status、field（被拒绝的字段）与 problem（成员的问题）的 Error。
  function call(UI, method, path, body, etag) {
    var headers = {};
    if (etag) headers['If-Match'] = etag;
    return UI.raw(method, path, body, headers).then(function (r) {
      return r.text().then(function (txt) {
        var j = null;
        try { j = txt ? JSON.parse(txt) : null; } catch (e) { j = null; }
        if (!r.ok) {
          var msg = j && typeof j.error === 'string' ? j.error : r.status + ' ' + r.statusText;
          var err = new Error(j && j.field ? j.field + ': ' + msg : msg);
          err.status = r.status;
          err.field = j && j.field;
          err.problem = j && j.problem;
          throw err;
        }
        return { status: r.status, body: j, etag: r.headers.get('ETag') || '' };
      });
    });
  }
  // members reads the members and their ETag. / 读取成员列表及其 ETag。
  function members(UI) {
    return call(UI, 'GET', '/api/nodes').then(function (r) {
      var b = r.body || {};
      return { etag: r.etag, nodes: b.nodes || [], maxNodes: b.max_nodes || 0, readOnly: b.read_only || '' };
    });
  }
  function nodePath(name) { return '/api/nodes/' + encodeURIComponent(name); }

  // ---- Words and facts / 词条与信息 ----
  function stateWord(UI, s) { return UI.t('st.' + (STATE_KIND.hasOwnProperty(s) ? s : 'unknown')); }
  function stateBadge(UI, s) { return UI.badge(stateWord(UI, s), STATE_KIND[s] || '', true); }
  function kindWord(UI, k) { return KINDS.indexOf(k) >= 0 ? UI.t('k.' + k) : String(k || ''); }
  function kindHint(UI, k) { return KINDS.indexOf(k) >= 0 ? UI.t('k.' + k + '.hint') : ''; }
  function sec(s) { var ms = Date.parse(s || ''); return isNaN(ms) || ms <= 0 ? 0 : Math.floor(ms / 1000); }
  function when(s) { var u = sec(s); return u ? UI.fmtTime(u) : '—'; }
  // daysLeft is the whole days until s, negative once past; null without s. / 距 s 的整天数，过期为负；无 s 时为 null。
  function daysLeft(s) {
    var ms = Date.parse(s || '');
    if (isNaN(ms) || ms <= 0) return null;
    return Math.floor((ms - Date.now()) / 86400000);
  }
  // expiry is a certificate's expiry: its days left, coloured as it nears. / 证书到期：剩余天数，临近时着色。
  function expiry(UI, s) {
    var d = daysLeft(s);
    if (d == null) return h0('span.muted', UI.t('f.nocert'));
    var kind = d < 7 ? 'danger' : d < 30 ? 'warn' : 'ok';
    return UI.badge(d < 0 ? UI.t('f.expired') : UI.t('f.days', { n: d }), kind);
  }
  function h0() { return UI.h.apply(UI, arguments); }
  // labelsText is labels as key=value, comma separated. / 标签写为以逗号分隔的 key=value。
  function labelsText(labels) {
    return Object.keys(labels || {}).sort().map(function (k) { return labels[k] === '' ? k : k + '=' + labels[k]; }).join(', ');
  }
  // parseLabels reads key=value terms separated by commas or lines. / 读取以逗号或换行分隔的 key=value。
  function parseLabels(s) {
    var out = {};
    String(s || '').split(/[,\n]/).forEach(function (p) {
      p = p.trim();
      if (!p) return;
      var i = p.indexOf('=');
      if (i < 0) out[p] = ''; else out[p.slice(0, i).trim()] = p.slice(i + 1).trim();
    });
    return out;
  }
  // labelChips are a member's labels; each, clicked, filters the overview by it. / 成员的标签；点击即按其筛选概览。
  function labelChips(UI, labels) {
    var keys = Object.keys(labels || {}).sort();
    if (!keys.length) return null;
    return UI.h('span.cl-labels', keys.map(function (k) {
      var term = labels[k] === '' ? k : k + '=' + labels[k];
      return UI.h('a.cl-label', { href: '#/cluster/overview?label=' + encodeURIComponent(term), title: term }, term);
    }));
  }
  // problemBox is a member's problem: its kind, its message and what to do about it, with Ask AI when given.
  // problemBox 显示成员的问题：类型、消息与处理建议，给出时带“问 AI”。
  function problemBox(UI, p, ask) {
    if (!p) return null;
    var extra = p.kind === 'rate_limited' && p.retry_after ? ' (' + UI.t('f.retry', { n: p.retry_after }) + ')' : '';
    return UI.h('div.callout.callout--' + (p.kind === 'unreachable' || p.kind === 'timeout' ? 'danger' : 'warn') + '.cl-problem',
      UI.h('div.row.row--between', UI.h('b', kindWord(UI, p.kind) + extra), ask || null),
      UI.h('div.cl-problem__msg', p.message || ''),
      kindHint(UI, p.kind) ? UI.h('div.small.muted', kindHint(UI, p.kind)) : null);
  }
  // askMember is the Ask AI spec of a member in trouble: the skill diagnose, the member's state as the selection
  // (never a credential: the state holds none). / 成员出问题时“问 AI”的 spec：技能 diagnose，成员状态作为选中项（状态中没有凭据）。
  function askMember(UI, st) {
    if (!st || !st.error || !(window.FitAI && FitAI.askButton)) return null;
    return FitAI.askButton(function () {
      var e = st.error || {};
      return {
        skill: 'diagnose',
        message: UI.t('ask.member', { name: st.name, status: stateWord(UI, st.status), kind: kindWord(UI, e.kind), message: e.message || '' }),
        selection: { cluster_member: { name: st.name, url: st.url, access: st.access, status: st.status, since: st.since,
          failures: st.failures, reachable: st.reachable, error: { kind: e.kind, message: e.message, status: e.status, retry_after: e.retry_after },
          certificate: e.certificate || st.tls || undefined, fit_version: st.node && st.node.fit_version, polled: st.polled, seen: st.seen } }
      };
    });
  }
  function extrasOf(UI, mem, st) {
    var out = [];
    rowExtras.forEach(function (fn) { try { var x = fn(UI, mem, st); if (x) out.push(x); } catch (e) { /* ignore / 忽略 */ } });
    return out.length ? out : null;
  }

  // ---- Certificates and tests / 证书与测试 ----
  // certCard is what a certificate says, its fingerprint first, with Trust when onTrust is given.
  // certCard 显示证书内容（指纹在前），给出 onTrust 时带“信任”按钮。
  function certCard(UI, c, onTrust) {
    if (!c) return null;
    var verified = c.verified ? UI.t('ct.v.' + c.verified) : UI.t('ct.v.none');
    var copy = UI.h('button.btn.btn--ghost.btn--icon.btn--sm', { type: 'button', title: UI.t('copy'), 'aria-label': UI.t('copy'),
      onclick: function () { UI.copy(c.fingerprint || ''); } }, UI.icon('copy'));
    return UI.h('div.cl-cert',
      UI.h('div.row.row--between', UI.h('b', UI.t('ct.title')),
        UI.h('span.btn-row', c.self_signed ? UI.badge(UI.t('ct.self'), 'info') : null, UI.badge(verified, c.verified ? 'ok' : 'warn'))),
      UI.kv([
        [UI.t('ct.fp'), UI.h('span.row', UI.h('code.cl-fp', c.fingerprint || '—'), copy)],
        [UI.t('ct.subject'), c.subject],
        [UI.t('ct.issuer'), c.issuer],
        [UI.t('ct.names'), (c.names || []).join(', ')],
        [UI.t('ct.valid'), when(c.not_before) + ' → ' + when(c.not_after)]
      ]),
      onTrust ? UI.h('div.row', UI.btn(UI.t('ct.trust'), 'shield', function () { onTrust(c); }, 'primary sm')) : null);
  }
  // confirmTrust asks a person to compare the fingerprint before trusting it: a Promise of true or false.
  // confirmTrust 请人在信任前比对指纹：返回 true 或 false 的 Promise。
  function confirmTrust(UI, name, c, changed) {
    return new Promise(function (resolve) {
      var done = false;
      UI.modal({
        title: UI.t('ct.trust.title', { name: name }),
        body: UI.h('div.stack', changed ? UI.h('div.callout.callout--danger', UI.icon('alert'), ' ', UI.t('ct.trust.changed')) : null,
          UI.h('div.small', UI.t('ct.trust.note')), certCard(UI, c)),
        onclose: function () { if (!done) resolve(false); },
        actions: [{ label: UI.t('cancel') }, { label: UI.t('ct.trust'), kind: changed ? 'danger' : 'primary', onclick: function () { done = true; resolve(true); } }]
      });
    });
  }
  // testResult draws a test's result; onTrust, when given, is offered for a certificate not trusted.
  // testResult 绘制测试结果；给出 onTrust 时，对不受信任的证书提供“信任”。
  function testResult(UI, res, onTrust) {
    res = res || {};
    var e = res.error, c = res.certificate;
    var untrusted = e && (e.kind === 'tls_untrusted' || e.kind === 'tls_pin_mismatch');
    var head;
    if (res.ok) {
      var n = res.node || {};
      head = UI.h('div.callout.callout--ok', UI.icon('check'), ' ', UI.t('ts.ok', { ms: res.latency_ms, fit: n.fit_version || '?', host: n.hostname || '?' }),
        n.id ? UI.h('div.small.muted', UI.t('ts.node') + ': ', UI.h('code', n.id)) : null);
    } else {
      head = problemBox(UI, e || { kind: '', message: UI.t('ts.failed') });
    }
    return UI.h('div.stack.cl-test', head, certCard(UI, c, untrusted && onTrust ? function (cert) { onTrust(cert, e.kind === 'tls_pin_mismatch'); } : null));
  }

  // ---- The member's form / 成员表单 ----
  // memberForm opens the form of a new member (mem null) or of mem, with Test, and saves it: then is called with the
  // member saved. etag is the members' ETag the page read; a 412 reloads through reload.
  // memberForm 打开新成员（mem 为 null）或 mem 的表单（带“测试”）并保存；保存后以所存成员调用 then。etag 为页面读到的
  // 成员 ETag；412 时通过 reload 重新读取。
  function memberForm(UI, o) {
    var mem = o.member, editing = !!mem;
    mem = mem || { name: '', labels: {}, url: '', access: 'console', username: '', tls: {} };
    var h = UI.h, t = UI.t;
    function input(attrs) { return h('input.input', attrs); }
    var name = input({ name: 'cluster-name', value: mem.name, autocomplete: 'off', spellcheck: 'false', placeholder: 'edge-1' });
    var labels = h('input.input', { name: 'cluster-labels', value: labelsText(mem.labels), autocomplete: 'off', spellcheck: 'false', placeholder: 'env=prod, region=eu' });
    var url = input({ name: 'cluster-url', value: mem.url, type: 'url', autocomplete: 'off', spellcheck: 'false', placeholder: 'https://fit-1.example.com:2020' });
    var access = h('select.select', { name: 'cluster-access' },
      ['console', 'admin_api'].map(function (a) { return h('option', { value: a, selected: mem.access === a }, t('fm.access.' + a)); }));
    var username = input({ name: 'cluster-username', value: mem.username || '', autocomplete: 'off', spellcheck: 'false', placeholder: 'admin' });
    // The credentials are never filled in: a saved one is only said to be there, and kept when left empty. The inputs
    // are of type password with autocomplete new-password, so that a browser neither shows nor fills them.
    // 凭据从不回填：已保存的只提示其存在，留空则保留。输入框为 password 类型且 autocomplete 为 new-password，浏览器既不显示也不自动填充。
    var password = input({ name: 'cluster-password', type: 'password', autocomplete: 'new-password',
      placeholder: editing && mem.password_set ? t('fm.secret.kept') : '' });
    var token = input({ name: 'cluster-token', type: 'password', autocomplete: 'new-password', spellcheck: 'false',
      placeholder: editing && mem.token_set ? t('fm.secret.kept') : '' });
    var tls = mem.tls || {};
    var mode = h('select.select', { name: 'cluster-tls' },
      [['system', 'fm.tls.system'], ['ca', 'fm.tls.ca'], ['pin', 'fm.tls.pin']].map(function (m) {
        var on = m[0] === (tls.fingerprint ? 'pin' : tls.ca ? 'ca' : 'system');
        return h('option', { value: m[0], selected: on }, t(m[1]));
      }));
    var ca = h('textarea.textarea.textarea--code', { name: 'cluster-ca', rows: 5, spellcheck: 'false', placeholder: '-----BEGIN CERTIFICATE-----' }, tls.ca || '');
    var fp = input({ name: 'cluster-fingerprint', value: tls.fingerprint || '', autocomplete: 'off', spellcheck: 'false', placeholder: 'ab:cd:…' });
    var fields = {}, hints = {};
    function field(key, label, el, hint) {
      var err = h('span.field__hint.cl-err', { hidden: true });
      fields[key] = err;
      var f = UI.field(label, el, hint);
      f.appendChild(err);
      return hints[key] = f;
    }
    var fName = field('name', t('fm.name'), name, t('fm.name.hint'));
    var fLabels = field('labels', t('fm.labels'), labels, t('fm.labels.hint'));
    var fURL = field('url', t('fm.url'), url, t('fm.url.hint'));
    var fAccess = field('access', t('fm.access'), access);
    var fUser = field('username', t('fm.username'), username);
    var fPass = field('password', t('fm.password'), password, t('fm.secret.hint'));
    var fToken = field('token', t('fm.token'), token, t('fm.secret.hint'));
    var fMode = field('tls', t('fm.tls'), mode);
    var fCA = field('tls.ca', t('fm.ca'), ca);
    var fFP = field('tls.fingerprint', t('fm.fingerprint'), fp, t('fm.fingerprint.hint'));
    var httpNote = h('div.small.muted', t('fm.http'));
    var credRow = h('div.form-row', fUser, fPass);
    var result = h('div.cl-result');
    var err = h('div.callout.callout--danger', { hidden: true });

    function sync() {
      var console_ = access.value === 'console', https = /^https:/i.test(url.value.trim());
      credRow.hidden = !console_; fToken.hidden = console_;
      fMode.hidden = !https; httpNote.hidden = https || !url.value.trim();
      fCA.hidden = !https || mode.value !== 'ca'; fFP.hidden = !https || mode.value !== 'pin';
    }
    access.addEventListener('change', sync); mode.addEventListener('change', sync); url.addEventListener('input', sync);
    sync();

    // draft is the member as the form has it: an empty password or token keeps the one saved.
    // draft 为表单中的成员：密码或令牌留空即保留已保存的。
    function draft() {
      var https = /^https:/i.test(url.value.trim());
      var d = { name: name.value.trim(), labels: parseLabels(labels.value), url: url.value.trim(), access: access.value,
        username: access.value === 'console' ? username.value : '', password: access.value === 'console' ? password.value : '',
        token: access.value === 'admin_api' ? token.value.trim() : '', tls: {} };
      if (https && mode.value === 'ca') d.tls.ca = ca.value.trim();
      if (https && mode.value === 'pin') d.tls.fingerprint = fp.value.trim();
      return d;
    }
    function clearErrors() {
      err.hidden = true;
      Object.keys(fields).forEach(function (k) { fields[k].hidden = true; });
    }
    function showError(e) {
      var f = e && e.field && fields[e.field] ? e.field : e && e.field && e.field.indexOf('tls') === 0 ? 'tls' : '';
      if (f) {
        fields[f].textContent = e.message.replace(/^[a-z.]+: /, '');
        fields[f].hidden = false;
      } else {
        err.textContent = e && e.message ? e.message : String(e);
        err.hidden = false;
      }
    }
    // trust pins the certificate in the form, after the person compared it, and tests again.
    // trust 在人比对之后把证书固定到表单中，并再次测试。
    function trust(c, changed) {
      confirmTrust(UI, name.value.trim() || mem.name || '?', c, changed).then(function (yes) {
        if (!yes) return;
        mode.value = 'pin'; fp.value = c.fingerprint; ca.value = '';
        sync();
        UI.toast(t('ct.pinned'));
        test();
      });
    }
    var testBtn = UI.btn(t('fm.test'), 'zap', function () { test(); }, 'ghost');
    function test() {
      clearErrors();
      UI.mount(result, h('div.row', h('span.spinner'), h('span.muted', t('fm.testing'))));
      var p = editing ? call(UI, 'POST', nodePath(mem.name) + '/test', draft()) : call(UI, 'POST', '/api/test', draft());
      return UI.busy(testBtn, p).then(function (r) {
        UI.mount(result, testResult(UI, r.body, trust));
      }, function (e) { UI.clear(result); showError(e); });
    }

    var body = h('form.form.cl-form', { onsubmit: function (e) { e.preventDefault(); } },
      h('div.form-row', fName, fAccess), fURL, fLabels,
      credRow, fToken, httpNote, fMode, fCA, fFP,
      h('div.row', testBtn), result, err);
    UI.modal({
      title: editing ? t('fm.edit', { name: mem.name }) : t('fm.add'), wide: true, body: body,
      actions: [{ label: t('cancel') }, {
        label: t('fm.save'), kind: 'primary', onclick: function () {
          clearErrors();
          var d = draft(), p = editing ? call(UI, 'PUT', nodePath(mem.name), d, o.etag) : call(UI, 'POST', '/api/nodes', d, o.etag);
          return p.then(function (r) {
            // What was written leaves the page with the form. / 写入的内容随表单离开页面。
            password.value = ''; token.value = '';
            UI.toast(t(editing ? 'fm.saved' : 'fm.added', { name: d.name || mem.name }));
            if (o.then) o.then(r.body, r.etag);
          }, function (e) {
            if (e.status === 412) { UI.toast(t('fm.stale'), 'error'); if (o.reload) o.reload(); return false; }
            showError(e);
            return false;
          });
        }
      }]
    });
  }

  // inputOf is a saved member as PUT takes it: its credentials left empty, which keeps them.
  // inputOf 把已保存的成员转为 PUT 的输入：凭据留空，即保留。
  function inputOf(mem) {
    return { name: mem.name, labels: mem.labels || {}, url: mem.url, access: mem.access, username: mem.username || '',
      password: '', token: '', tls: { ca: (mem.tls && mem.tls.ca) || '', fingerprint: (mem.tls && mem.tls.fingerprint) || '' } };
  }
  // trustSaved pins c for a saved member, after the person compared it: a Promise of whether it was saved.
  // trustSaved 在人比对之后为已保存的成员固定证书 c：返回是否已保存的 Promise。
  function trustSaved(UI, mem, c, changed, etag) {
    return confirmTrust(UI, mem.name, c, changed).then(function (yes) {
      if (!yes) return false;
      var d = inputOf(mem);
      d.tls = { fingerprint: c.fingerprint };
      return call(UI, 'PUT', nodePath(mem.name), d, etag).then(function () {
        UI.toast(UI.t('ct.trusted', { name: mem.name }));
        return true;
      }, function (e) {
        UI.toast(e.status === 412 ? UI.t('fm.stale') : e.message, 'error');
        return false;
      });
    });
  }
  // remove removes a member after a confirm: a Promise of whether it was removed.
  // remove 确认后删除成员：返回是否已删除的 Promise。
  function remove(UI, name, etag) {
    return UI.confirm(UI.t('rm.ask', { name: name }), UI.t('rm.ok'), true).then(function (yes) {
      if (!yes) return false;
      return call(UI, 'DELETE', nodePath(name), undefined, etag).then(function () {
        UI.toast(UI.t('rm.done', { name: name }));
        return true;
      }, function (e) {
        UI.toast(e.status === 412 ? UI.t('fm.stale') : e.message, 'error');
        return false;
      });
    });
  }
  // testSaved tests a saved member and shows the result, with Trust for a certificate not trusted.
  // testSaved 测试已保存的成员并显示结果，对不受信任的证书提供“信任”。
  function testSaved(UI, mem, etag, after) {
    var box = UI.h('div', UI.h('div.row', UI.h('span.spinner'), UI.h('span.muted', UI.t('fm.testing'))));
    var close = UI.modal({ title: UI.t('fm.test') + ' · ' + mem.name, wide: true, body: box });
    call(UI, 'POST', nodePath(mem.name) + '/test').then(function (r) {
      UI.mount(box, testResult(UI, r.body, function (c, changed) {
        trustSaved(UI, mem, c, changed, etag).then(function (ok) { if (ok) { close(); if (after) after(); } });
      }));
    }, function (e) { UI.mount(box, UI.h('div.callout.callout--danger', e.message)); });
  }

  window.FitCluster = {
    WORDS: WORDS, KINDS: KINDS, STATE_KIND: STATE_KIND, ORDER: ORDER, pages: {},
    call: call, members: members, nodePath: nodePath,
    stateWord: stateWord, stateBadge: stateBadge, kindWord: kindWord, kindHint: kindHint, sec: sec, when: when,
    daysLeft: daysLeft, expiry: expiry, labelsText: labelsText, parseLabels: parseLabels, labelChips: labelChips,
    problemBox: problemBox, askMember: askMember, extras: extrasOf,
    certCard: certCard, testResult: testResult, memberForm: memberForm, trustSaved: trustSaved, remove: remove, testSaved: testSaved,
    // addAction and rowExtra are the extension points above. / 见上文的扩展点。
    addAction: function (fn) { if (typeof fn === 'function') addActions.push(fn); },
    addActions: function (UI, ctx) {
      var out = [];
      addActions.forEach(function (fn) { try { var b = fn(UI, ctx); if (b) out.push(b); } catch (e) { UI.fail(e); } });
      return out;
    },
    rowExtra: function (fn) { if (typeof fn === 'function') rowExtras.push(fn); },
    overviewCard: function (fn) { if (typeof fn === 'function') overviewCards.push(fn); },
    overviewCards: function (UI) {
      var out = [];
      overviewCards.forEach(function (fn) { try { var x = fn(UI); if (x) out.push(x); } catch (e) { /* ignore / 忽略 */ } });
      return out;
    },
    // The user's roles (I148). / 用户的角色（I148）。
    loadMe: loadMe, me: function () { return me; }, canCluster: canCluster, canAny: canAny, canOn: canOn, canAll: canAll,
    canGroup: canGroup, noRole: noRole, roleWord: roleWord, ROLES: ['viewer', 'operator', 'admin']
  };
})();
