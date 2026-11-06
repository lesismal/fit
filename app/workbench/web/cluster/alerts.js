// Cluster → Drift and alerts (I147). Drift: each member's sync state against its templates, as the manager's drift reads
// took it after its polls, and its own consistency check; a member out of sync shows its diff, part by part, read now
// (GET /api/m/cluster/desired/<name>), and Fix plans a sync rollout of it (POST drift/fix), which opens the rollout's
// plan: nothing is applied until a person starts it there. A consistency drift is fixed by a reload of the member, an
// operation asked for and confirmed. Alerts: the alerts firing, those pending (their condition has not lasted the
// rule's time yet), and those resolved, the latest first; a firing alert is silenced from its row. Rules: the alert
// rules' form (member down, drift, a rollout failed, a certificate expiring, upstream backends down, reloads failing),
// each with its time, repeat interval, group and webhooks. Webhooks: where alerts go, generic JSON or a chat service's
// (Slack, Discord, Feishu, DingTalk, WeCom, or a body of one's own), their URLs and secrets written once and never
// shown, a test, and how their sends went. Silences: active and past, added and ended.
//
// The pages are #/cluster/alerts/<tab> (drift, alerts, rules, webhooks, silences). The Cluster menu shows a badge while
// alerts fire, from GET alerts/summary, read every 15 seconds while the workbench is open (FC.alertsBoot). It calls
// /api/m/cluster/drift and /api/m/cluster/alerts/... (docs/cluster.md). Needs shared.js, templates.js (the side-by-side
// diff) and groups.js (the confirm of an operation); before cluster.js.
//
// 集群 → 漂移与告警（I147）。漂移：各成员相对其模板的同步状态（管理端在轮询后的漂移读取所得）以及其自身的一致性检查；未同步的
// 成员可逐部分查看差异（现读 GET /api/m/cluster/desired/<name>），“修复”为其规划一次同步发布（POST drift/fix）并打开该发布的
// 规划：在有人于该页启动之前不应用任何内容。一致性不一致通过对该成员的重新加载修复，这是一次需要确认的操作。告警：正在触发的
// 告警、等待中的告警（条件尚未持续到规则的时长）与已恢复的告警（最新在前）；可在正在触发的告警行上将其静默。规则：告警规则的
// 表单（成员离线、漂移、发布失败、证书即将到期、上游后端不可用、重新加载失败），各有时长、重复间隔、分组与 Webhook。
// Webhook：告警的去处，通用 JSON 或聊天服务的格式（Slack、Discord、飞书、钉钉、企业微信，或自定义消息体），URL 与密钥只写入
// 一次、从不显示，可测试，并显示发送情况。静默：当前与过去的静默，可添加与结束。
// 页面为 #/cluster/alerts/<tab>（drift、alerts、rules、webhooks、silences）。告警触发期间集群菜单显示徽标，来自
// GET alerts/summary，工作台打开期间每 15 秒读取一次（FC.alertsBoot）。调用 /api/m/cluster/drift 与 /api/m/cluster/alerts/...
// （见 docs/cluster.md）。依赖 shared.js、templates.js（并排差异）与 groups.js（操作的确认）；在 cluster.js 之前加载。
(function () {
  'use strict';
  var FC = window.FitCluster;
  FC.WORDS.push(
    ['n.alerts', 'Drift and alerts', '漂移与告警'],
    ['al.tab.drift', 'Drift', '漂移'], ['al.tab.alerts', 'Alerts', '告警'], ['al.tab.rules', 'Rules', '规则'],
    ['al.tab.webhooks', 'Webhooks', 'Webhook'], ['al.tab.silences', 'Silences', '静默'],
    ['al.badge', 'Alerts firing: {n}', '{n} 个告警正在触发'],
    ['al.readonly', 'The alerts are read-only: {why}', '告警为只读：{why}'],
    // Drift / 漂移
    ['dr.intro', 'Each member a template is attached to is read against its templates after its polls, once its config changed; its consistency check comes with every poll. Nothing is applied here: Fix plans a rollout, which you start on its page.',
      '模板所关联的每个成员在其配置变化后，于轮询之后与模板比对读取；一致性检查随每次轮询获得。此处不应用任何内容：“修复”只规划一次发布，需在发布页面启动。'],
    ['dr.only', 'Members with drift only', '只显示有漂移的成员'], ['dr.none', 'No member drifts.', '没有成员存在漂移。'],
    ['dr.member', 'Member', '成员'], ['dr.state', 'State', '状态'], ['dr.templates', 'Templates', '模板'],
    ['dr.check', 'Consistency check', '一致性检查'], ['dr.checked', 'Read', '读取时间'],
    ['dr.counts', '{o} out of sync, {c} in conflict', '{o} 处未同步，{c} 处冲突'],
    ['dr.diffs', '{n} differences', '{n} 处不一致'], ['dr.clean', 'none', '无'], ['dr.notmanaged', 'no template', '无模板'],
    ['dr.notread', 'not read yet', '尚未读取'],
    ['dr.diff', 'Diff', '差异'], ['dr.fix', 'Fix', '修复'], ['dr.fixall', 'Fix all out of sync ({n})', '修复全部未同步（{n}）'],
    ['dr.fix.ask', 'Plan a sync rollout of {names}? Each is brought in sync with its templates once you start the rollout on its page; nothing is applied now.',
      '为 {names} 规划一次同步发布？在发布页面启动后，每个成员才会与其模板同步；现在不会应用任何内容。'],
    ['dr.plan', 'Plan the rollout', '规划发布'], ['dr.adjust', 'Plan by hand', '手动规划'],
    ['dr.reload', 'Reload', '重新加载'], ['dr.openchk', 'Its consistency check', '其一致性检查'],
    ['dr.reading', 'Reading the member…', '正在读取成员……'], ['dr.insync', 'In sync now.', '现在已同步。'],
    ['dr.have', 'On the member', '成员上'], ['dr.want', 'From its templates', '来自模板'], ['dr.absent', '(not there)', '（不存在）'],
    // Alerts / 告警
    ['al.firing', 'Firing', '正在触发'], ['al.silenced', 'Silenced', '已静默'], ['al.pending', 'Pending', '等待中'],
    ['al.resolved', 'Resolved', '已恢复'], ['al.history', 'Past alerts', '历史告警'], ['al.active', 'Active alerts', '活动告警'],
    ['al.none', 'No alert fires.', '没有正在触发的告警。'], ['al.nopending', 'Nothing pending.', '没有等待中的告警。'],
    ['al.nohistory', 'No alert has resolved yet.', '还没有已恢复的告警。'],
    ['al.rule', 'Rule', '规则'], ['al.on', 'On', '对象'], ['al.summary', 'What', '内容'], ['al.since', 'Since', '开始于'],
    ['al.fired', 'Fired', '触发于'], ['al.sends', 'Sent', '已发送'], ['al.resolvedat', 'Resolved', '恢复于'], ['al.why', 'Why', '原因'],
    ['al.why.cond', 'its condition is gone', '条件已消失'], ['al.silence', 'Silence', '静默'], ['al.until', 'pending until {at}', '{at} 前等待'],
    ['al.sev.critical', 'Critical', '严重'], ['al.sev.warning', 'Warning', '警告'], ['al.sev.info', 'Info', '信息'],
    ['al.k.member_down', 'Member down', '成员离线'], ['al.k.drift', 'Drift', '漂移'], ['al.k.rollout_failed', 'Rollout failed', '发布失败'],
    ['al.k.cert_expiry', 'Certificate expiring', '证书即将到期'], ['al.k.backends_down', 'Upstream backends down', '上游后端不可用'],
    ['al.k.reload_failed', 'Reloads failing', '重新加载失败'],
    ['al.k.member_down.hint', 'A member down, or failing its polls otherwise (credentials, certificate), for the rule\'s time.',
      '成员离线，或以其他原因（凭据、证书）轮询失败，持续达到规则的时长。'],
    ['al.k.drift.hint', 'A member out of sync with its templates or in conflict with them, or whose consistency check finds differences.',
      '成员与其模板未同步或冲突，或其一致性检查发现不一致。'],
    ['al.k.rollout_failed.hint', 'A rollout that ended failed, until it is rolled back or the time to resolve passes.',
      '以失败结束的发布，直到被回滚或超过恢复时长。'],
    ['al.k.cert_expiry.hint', 'A member\'s certificate that expires within the days given.', '成员的证书在给定天数内到期。'],
    ['al.k.backends_down.hint', 'At least the given number of a member\'s upstream backends down.', '成员的上游后端中至少有给定数量不可用。'],
    ['al.k.reload_failed.hint', 'A member whose latest reload failed, until one succeeds or the time to resolve passes.',
      '成员最近一次重新加载失败，直到有一次成功或超过恢复时长。'],
    // Rules / 规则
    ['ru.add', 'Add a rule', '添加规则'], ['ru.edit', 'Edit the rule {name}', '编辑规则 {name}'], ['ru.none', 'No rules: no alert is followed.', '没有规则：不跟踪任何告警。'],
    ['ru.name', 'Name', '名称'], ['ru.kind', 'Watches', '监视'], ['ru.severity', 'Severity', '严重程度'], ['ru.group', 'Members', '成员'],
    ['ru.allmembers', 'Every member', '全部成员'], ['ru.group.of', 'the group {g}', '分组 {g}'],
    ['ru.for', 'Lasting (s)', '持续（秒）'], ['ru.for.hint', 'How long the condition must hold before the alert fires; 0 fires at once.',
      '条件须持续多久告警才触发；0 表示立即触发。'],
    ['ru.repeat', 'Repeat every (min)', '重复间隔（分钟）'], ['ru.repeat.hint', 'A firing alert not silenced is sent again this often; 0 never.',
      '未静默的触发告警按此间隔再次发送；0 表示不重复。'],
    ['ru.days', 'Within days', '天数之内'], ['ru.threshold', 'Backends down at least', '不可用后端至少'],
    ['ru.source', 'Watches', '监视内容'], ['ru.source.both', 'Templates and consistency check', '模板与一致性检查'],
    ['ru.source.templates', 'Templates only', '仅模板'], ['ru.source.consistency', 'Consistency check only', '仅一致性检查'],
    ['ru.resolve', 'Resolve after (min)', '自动恢复（分钟）'], ['ru.resolve.hint', 'The alert ends this long after the failure; 0 never by itself.',
      '告警在失败后经过此时长结束；0 表示不会自行结束。'],
    ['ru.webhooks', 'Webhooks', 'Webhook'], ['ru.webhooks.hint', 'None picked: every webhook.', '未选择时：所有 Webhook。'],
    ['ru.allhooks', 'every webhook', '所有 Webhook'], ['ru.disabled', 'Turned off', '已关闭'], ['ru.off', 'off', '关闭'],
    ['ru.saved', 'The rule {name} was saved.', '已保存规则 {name}。'], ['ru.removed', 'The rule {name} was removed.', '已删除规则 {name}。'],
    ['ru.rm.ask', 'Remove the rule {name}? Its alerts resolve.', '删除规则 {name}？其告警将恢复。'],
    ['ru.never', 'never', '从不'],
    // Webhooks / Webhook
    ['wh.add', 'Add a webhook', '添加 Webhook'], ['wh.edit', 'Edit the webhook {name}', '编辑 Webhook {name}'],
    ['wh.none', 'No webhooks: alerts are shown here and sent nowhere.', '没有 Webhook：告警只在此处显示，不发送到任何地方。'],
    ['wh.name', 'Name', '名称'], ['wh.format', 'Format', '格式'], ['wh.url', 'URL', 'URL'], ['wh.secret', 'Secret', '密钥'],
    ['wh.url.hint', 'https, or http to a loopback address. Written once, kept encrypted in the manager\'s store, and never shown again: a chat service\'s URL holds its token.',
      'https，或仅限回环地址的 http。只写入一次，加密保存在管理端的 store 中，之后不再显示：聊天服务的 URL 含有其令牌。'],
    ['wh.secret.hint', 'Signs each notification: fit\'s X-Fit-Signature (HMAC-SHA256 of the timestamp, a dot and the body), or Feishu\'s and DingTalk\'s own signatures. Written once, never shown.',
      '为每条通知签名：fit 的 X-Fit-Signature（时间戳、点号与消息体的 HMAC-SHA256），或飞书与钉钉自己的签名。只写入一次，从不显示。'],
    ['wh.kept', 'Saved — leave empty to keep it', '已保存——留空则保留'], ['wh.clear', 'Remove the secret', '删除密钥'],
    ['wh.template', 'Template', '模板'],
    ['wh.template.line', 'Each alert\'s line: ${severity} ${rule} ${kind} ${node} ${subject} ${on} ${status} ${summary} ${since} ${id}. Empty: fit\'s own.',
      '每个告警一行：${severity} ${rule} ${kind} ${node} ${subject} ${on} ${status} ${summary} ${since} ${id}。留空则使用 fit 的默认格式。'],
    ['wh.template.custom', 'The whole body, JSON: ${text} ${status} ${manager} (in a string), ${count} and ${alerts} (as values).',
      '完整消息体（JSON）：${text} ${status} ${manager}（放在字符串中），${count} 与 ${alerts}（作为值）。'],
    ['wh.template.json', 'fit\'s own JSON: version, source, manager, status, sent, text and the alerts with their states and times.',
      'fit 自己的 JSON：version、source、manager、status、sent、text 以及各告警的状态与时间。'],
    ['wh.f.json', 'JSON (fit\'s own)', 'JSON（fit 格式）'], ['wh.f.slack', 'Slack, Mattermost, Rocket.Chat', 'Slack、Mattermost、Rocket.Chat'],
    ['wh.f.discord', 'Discord', 'Discord'], ['wh.f.feishu', 'Feishu (Lark)', '飞书'], ['wh.f.dingtalk', 'DingTalk', '钉钉'],
    ['wh.f.wecom', 'WeCom', '企业微信'], ['wh.f.custom', 'A body of your own', '自定义消息体'],
    ['wh.timeout', 'Timeout (s)', '超时（秒）'], ['wh.resolved', 'Send resolved alerts', '发送已恢复的告警'], ['wh.disabled', 'Turned off', '已关闭'],
    ['wh.test', 'Test', '测试'], ['wh.test.ok', 'The webhook {name} took the test in {ms} ms.', 'Webhook {name} 在 {ms} 毫秒内接收了测试。'],
    ['wh.test.failed', 'The test of {name} failed: {err}', '{name} 的测试失败：{err}'],
    ['wh.status', 'Sends', '发送情况'], ['wh.sent', '{n} sent', '已发送 {n}'], ['wh.failed', '{n} failed', '失败 {n}'],
    ['wh.dropped', '{n} dropped', '丢弃 {n}'], ['wh.queued', '{n} waiting', '等待 {n}'], ['wh.last', 'last {ago}', '最近 {ago}'],
    ['wh.secretset', 'signed', '已签名'], ['wh.saved', 'The webhook {name} was saved.', '已保存 Webhook {name}。'],
    ['wh.removed', 'The webhook {name} was removed.', '已删除 Webhook {name}。'],
    ['wh.rm.ask', 'Remove the webhook {name}? Its URL and secret are forgotten.', '删除 Webhook {name}？其 URL 与密钥将被删除。'],
    // Silences / 静默
    ['si.add', 'Add a silence', '添加静默'], ['si.none', 'No silences.', '没有静默。'],
    ['si.rule', 'Rule', '规则'], ['si.node', 'Member', '成员'], ['si.anyrule', 'Every rule', '所有规则'], ['si.anynode', 'Every member', '所有成员'],
    ['si.for', 'For', '时长'], ['si.comment', 'Comment', '备注'], ['si.by', 'By', '操作人'], ['si.starts', 'Starts', '开始'], ['si.ends', 'Ends', '结束'],
    ['si.state', 'State', '状态'], ['si.s.active', 'Active', '生效中'], ['si.s.later', 'Not yet', '尚未开始'], ['si.s.ended', 'Ended', '已结束'],
    ['si.end', 'End now', '立即结束'], ['si.added', 'The silence was added.', '已添加静默。'], ['si.ended', 'The silence ended.', '静默已结束。'],
    ['si.hint', 'A silence keeps the alerts it matches from being sent; they still show here. A resolved alert that was sent is still sent resolved.',
      '静默使匹配的告警不被发送，但仍在此处显示。已发送过的告警恢复时仍会发送恢复通知。'],
    ['si.need', 'Pick a rule, a member, or both.', '请选择规则、成员或两者。'],
    ['si.h1', '1 hour', '1 小时'], ['si.h2', '2 hours', '2 小时'], ['si.h4', '4 hours', '4 小时'], ['si.h12', '12 hours', '12 小时'],
    ['si.d1', '1 day', '1 天'], ['si.d3', '3 days', '3 天'], ['si.w1', '1 week', '1 周']
  );

  var SEV_KIND = { critical: 'danger', warning: 'warn', info: 'info' };
  var TABS = ['drift', 'alerts', 'rules', 'webhooks', 'silences'];
  var counts = null, bootUI = null;

  function sevBadge(UI, s) { return UI.badge(UI.t('al.sev.' + s), SEV_KIND[s] || '', true); }
  function kindWord(UI, k) { return UI.t('al.k.' + k); }
  function ms(n) { return Number(n) || 0; }
  function secs(v) { return Math.round(ms(v) / 1000); }
  function mins(v) { return Math.round(ms(v) / 60000); }
  function num(UI, name, v, min) { return UI.h('input.input.input--sm', { name: name, type: 'number', min: min == null ? 0 : min, step: 1, value: v == null ? '' : v }); }
  // onLink is what an alert is on: its member's page, or its rollout's. / 告警的对象：成员页或发布页。
  function onLink(UI, a) {
    if (a.node) return UI.h('a', { href: '#/cluster/node/' + encodeURIComponent(a.node) }, a.node);
    if (a.subject) return UI.h('a', { href: '#/cluster/rollout/' + encodeURIComponent(a.subject) }, a.subject);
    return '—';
  }
  function errorBox(UI) { return UI.h('div.callout.callout--danger', { hidden: true }); }
  function showError(box, e) { box.textContent = (e.field ? e.field + ': ' : '') + e.message; box.hidden = false; }

  // ---- The badge / 徽标 ----
  // alertBadge is the Cluster menu's badge while alerts fire: their count, red with a critical one. / 告警触发期间集群菜单的徽标。
  FC.alertBadge = function () {
    if (!counts || !counts.firing) return null;
    return { text: counts.firing, kind: counts.critical ? 'danger' : 'warn', title: bootUI ? bootUI.t('al.badge', { n: counts.firing }) : '' };
  };
  // setCounts keeps the counts and redraws the menu when they changed. / 保存计数，变化时重绘菜单。
  function setCounts(c) {
    var before = JSON.stringify(counts), now = JSON.stringify(c || null);
    counts = c || null;
    if (before !== now && window.WB && WB.refreshMenu) WB.refreshMenu();
  }
  // alertsBoot reads the counts now and every 15 seconds while the workbench is open. / 立即读取计数，此后每 15 秒读取一次。
  FC.alertsBoot = function (UI) {
    bootUI = UI;
    function read() { FC.call(UI, 'GET', '/api/alerts/summary').then(function (r) { setCounts(r.body); }, function () { /* kept / 保留 */ }); }
    read();
    setInterval(read, 15000);
  };

  // ---- The page / 页面 ----
  FC.pages.alerts = function (UI, sh, p) {
    var h = UI.h, t = UI.t;
    var tab = TABS.indexOf(p && p.tab) >= 0 ? p.tab : 'drift';
    var el = sh.page('alerts', t('n.alerts'), t('m.cluster') + ' / ' + t('n.alerts') + ' / ' + t('al.tab.' + tab));
    var body = h('div.stack');
    UI.mount(el, h('div.stack', UI.tabs(TABS.map(function (x) { return [x, t('al.tab.' + x)]; }), tab, function (x) {
      location.hash = '#/cluster/alerts/' + x;
    }), body));
    UI.mount(body, h('div.row', h('span.spinner'), h('span.muted', t('loading'))));
    var draw = { drift: driftTab, alerts: alertsTab, rules: rulesTab, webhooks: webhooksTab, silences: silencesTab }[tab];
    var reload = function () { draw(UI, sh, body, el, reload); };
    UI.mount(sh.tools, UI.btn(t('refresh'), 'refresh', reload, 'ghost sm'));
    reload();
    if (tab === 'drift' || tab === 'alerts') UI.every(5000, function () { if (!document.querySelector('.modal') && !el.querySelector('details[open]')) reload(); });
  };

  function failed(UI, body, el) {
    return function (e) {
      if (!document.body.contains(el) || e.status === 401) return;
      UI.mount(body, UI.h('div.callout.callout--danger', e.message));
    };
  }

  // ---- Drift / 漂移 ----
  var onlyDrift = true;
  function driftTab(UI, sh, body, el, reload) {
    var h = UI.h, t = UI.t;
    FC.call(UI, 'GET', '/api/drift').then(function (r) {
      if (!document.body.contains(el)) return;
      var b = r.body || {}, nodes = b.nodes || [];
      var shown = nodes.filter(function (n) { return !onlyDrift || n.drifted; });
      var fixable = nodes.filter(function (n) { return n.fixable; }).map(function (n) { return n.node; });
      var only = h('input', { type: 'checkbox', checked: onlyDrift, 'aria-label': t('dr.only') });
      only.addEventListener('change', function () { onlyDrift = only.checked; reload(); });
      var fixAll = UI.btn(t('dr.fixall', { n: fixable.length }), 'send', function () { fix(UI, fixable); }, 'primary sm');
      fixAll.disabled = !fixable.length || !!b.read_only;
      // A fix is a sync rollout: an operator's of each member (I148). / 修复即同步发布：需要在每个成员上是操作员（I148）。
      if (!FC.canAll('operator', fixable)) fixAll = null;
      UI.mount(body, h('div.stack', b.read_only ? h('div.callout.callout--warn', t('readonly', { why: b.read_only })) : null,
        h('div.small.muted', t('dr.intro')),
        h('div.row.row--between.cl-bar', h('label.row', only, ' ', t('dr.only')), fixAll),
        shown.length ? UI.card(null, UI.table([
          { label: t('dr.member'), render: function (n) { return h('a', { href: '#/cluster/node/' + encodeURIComponent(n.node) }, h('b', n.node)); } },
          { label: t('dr.state'), render: function (n) { return FC.stateBadge(UI, n.status); } },
          { label: t('dr.templates'), render: function (n) { return templatesCell(UI, n); } },
          { label: t('dr.check'), render: function (n) {
            var c = n.consistency;
            if (!c) return h('span.muted.small', t('dr.notread'));
            return c.count ? UI.badge(t('dr.diffs', { n: c.count }), 'warn') : h('span.small.muted', t('dr.clean'));
          } },
          { label: t('dr.checked'), render: function (n) { return h('span.small', n.templates && n.templates.checked ? FC.when(n.templates.checked) : '—'); } },
          { label: '', render: function (n) { return actions(UI, n, b.read_only); } }
        ], shown), null, true) : UI.empty(t('dr.none'), 'check')));
    }, failed(UI, body, el));
  }
  function templatesCell(UI, n) {
    var h = UI.h, t = UI.t, s = n.templates;
    if (!n.managed && !s) return h('span.small.muted', t('dr.notmanaged'));
    if (!s) return h('span.small.muted', t('dr.notread'));
    var c = s.counts || {};
    return h('div', UI.badge(t('sy.' + s.state), s.state === 'in_sync' ? 'ok' : s.state === 'out_of_sync' ? 'warn' : s.state === 'unmanaged' ? '' : 'danger', true),
      c.out_of_sync || c.conflict ? h('div.small.muted', t('dr.counts', { o: c.out_of_sync || 0, c: c.conflict || 0 })) : null,
      s.error ? h('div.small.muted', s.error.message) : null, (s.errors || []).length ? h('div.small.muted', s.errors[0]) : null);
  }
  function actions(UI, n, ro) {
    var h = UI.h, t = UI.t, out = [];
    var op = FC.canOn('operator', n.node);
    if (n.fixable) {
      out.push(UI.btn(t('dr.diff'), 'layers', function () { diff(UI, n.node); }, 'ghost sm'));
    }
    if (n.fixable && op) {
      var f = UI.btn(t('dr.fix'), 'send', function () { fix(UI, [n.node]); }, 'primary sm');
      f.disabled = !!ro;
      out.push(f, h('a.small', { href: '#/cluster/newrollout?kind=sync&nodes=' + encodeURIComponent(n.node) }, t('dr.adjust')));
    }
    if (n.consistency && n.consistency.count && op) {
      var rl = UI.btn(t('dr.reload'), 'refresh', function () { FC.confirmOp(UI, 'reload', [n.node]); }, 'ghost sm');
      rl.disabled = !!ro;
      out.push(rl, h('a.small', { href: UI.targetURL(n.node, '#/sys.check/check') }, t('dr.openchk')));
    }
    // Ask AI about its drift (I153, askai.js). / 就其漂移“问 AI”（I153，askai.js）。
    var ask = FC.askDrift ? FC.askDrift(UI, n) : null;
    if (ask) out.push(ask);
    return h('span.btn-row', out);
  }
  // fix plans a sync rollout of names, once confirmed, and opens its plan. / 确认后为 names 规划同步发布并打开其规划。
  function fix(UI, names) {
    var t = UI.t;
    UI.confirm(t('dr.fix.ask', { names: names.join(', ') }), t('dr.plan')).then(function (yes) {
      if (!yes) return;
      FC.call(UI, 'POST', '/api/drift/fix', { nodes: names }).then(function (r) {
        location.hash = '#/cluster/rollout/' + encodeURIComponent(r.body.id);
      }, function (e) { UI.toast(e.message, 'error'); });
    });
  }
  // diff reads the member now and shows each part out of sync, side by side. / 现读成员，并排显示每个未同步的部分。
  function diff(UI, name) {
    var h = UI.h, t = UI.t;
    var box = h('div.stack', h('div.row', h('span.spinner'), h('span.muted', t('dr.reading'))));
    UI.modal({ title: t('dr.diff') + ' · ' + name, wide: true, body: box });
    FC.call(UI, 'GET', '/api/desired/' + encodeURIComponent(name)).then(function (r) {
      var parts = ((r.body && r.body.parts) || []).filter(function (p) { return p.state !== 'in_sync'; });
      if (!parts.length) { UI.mount(box, h('div.callout', t('dr.insync'))); return; }
      UI.mount(box, parts.map(function (p) {
        return h('details.tp-el', { open: parts.length < 4 }, h('summary', h('code', p.path), ' ', UI.badge(t('sy.' + p.state), p.state === 'conflict' ? 'danger' : 'warn'),
          p.reason ? h('span.small.muted', ' ' + t('sy.r.' + p.reason)) : null, p.template ? h('span.small.muted', ' · ' + p.template) : null),
        FC.sideBySide(UI, t('dr.have'), p.have === undefined || p.have === null ? t('dr.absent') : JSON.stringify(p.have, null, 2), t('dr.want'), JSON.stringify(p.want, null, 2)));
      }));
    }, function (e) { UI.mount(box, h('div.callout.callout--danger', e.message)); });
  }

  // ---- Alerts / 告警 ----
  function alertsTab(UI, sh, body, el, reload) {
    var h = UI.h, t = UI.t;
    FC.call(UI, 'GET', '/api/alerts').then(function (r) {
      if (!document.body.contains(el)) return;
      var b = r.body || {}, c = b.counts || {};
      setCounts(c);
      var rule = { label: t('al.rule'), render: function (a) { return h('div', h('b', a.rule), h('div.small.muted', kindWord(UI, a.kind))); } };
      var on = { label: t('al.on'), render: function (a) { return onLink(UI, a); } };
      var what = { label: t('al.summary'), render: function (a) { return h('span.small', a.summary); } };
      var since = { label: t('al.since'), render: function (a) { return h('span.small', UI.ago(FC.sec(a.since))); } };
      UI.mount(body, h('div.stack',
        b.read_only ? h('div.callout.callout--warn', t('al.readonly', { why: b.read_only })) : null,
        h('div.grid.grid--4',
          UI.stat('alert', t('al.firing'), String(c.firing || 0), null, c.critical ? 'danger' : c.firing ? 'warn' : 'ok'),
          UI.stat('alert', t('al.sev.critical'), String(c.critical || 0), null, c.critical ? 'danger' : ''),
          UI.stat('history', t('al.pending'), String(c.pending || 0)),
          UI.stat('shield', t('al.silenced'), String(c.silenced || 0))),
        UI.card(t('al.active'), UI.table([
          { label: '', render: function (a) { return sevBadge(UI, a.severity); } }, rule, on, what, since,
          { label: t('al.sends'), render: function (a) {
            return h('div.small', String(a.sends || 0), a.silenced_by ? h('div', UI.badge(t('al.silenced'), '')) : null);
          } },
          { label: '', render: function (a) {
            return b.read_only || !(a.node ? FC.canOn('operator', a.node) : FC.canCluster('operator')) ? null : UI.btn(t('al.silence'), 'shield', function () { silenceForm(UI, { rule: a.rule, node: a.node }, reload); }, 'ghost sm');
          } }
        ], b.active || [], { empty: t('al.none') }), null, true),
        UI.card(t('al.pending'), UI.table([{ label: '', render: function (a) { return sevBadge(UI, a.severity); } }, rule, on, what, since],
          b.pending || [], { empty: t('al.nopending') }), null, true),
        UI.card(t('al.history'), UI.table([{ label: '', render: function (a) { return sevBadge(UI, a.severity); } }, rule, on, what,
          { label: t('al.fired'), render: function (a) { return h('span.small', FC.when(a.fired)); } },
          { label: t('al.resolvedat'), render: function (a) { return h('span.small', FC.when(a.resolved)); } },
          { label: t('al.why'), render: function (a) { return h('span.small.muted', a.resolved_why || t('al.why.cond')); } }],
        b.history || [], { empty: t('al.nohistory') }), null, true)));
    }, failed(UI, body, el));
  }

  // ---- Rules / 规则 ----
  function rulesTab(UI, sh, body, el, reload) {
    var h = UI.h, t = UI.t;
    Promise.all([FC.call(UI, 'GET', '/api/alerts/config'), FC.call(UI, 'GET', '/api/groups')]).then(function (res) {
      if (!document.body.contains(el)) return;
      var b = res[0].body || {}, etag = res[0].etag, groups = ((res[1].body && res[1].body.groups) || []).map(function (g) { return g.name; });
      var ctx = { cfg: b, etag: etag, groups: groups, reload: reload };
      // Rules and webhooks hold secrets and decide who is told: a cluster-wide admin's (I148).
      // 规则与 Webhook 含密钥并决定通知谁：需要集群范围的管理员（I148）。
      var admin = FC.canCluster('admin');
      var add = admin ? UI.btn(t('ru.add'), 'plus', function () { ruleForm(UI, ctx, null); }, 'primary') : null;
      if (add) add.disabled = !!b.read_only || (b.rules || []).length >= b.bounds.max_rules;
      UI.mount(body, h('div.stack', b.read_only ? h('div.callout.callout--warn', t('al.readonly', { why: b.read_only })) : null,
        h('div.row.cl-bar', add),
        UI.card(null, UI.table([
          { label: t('ru.name'), render: function (x) { return h('div', h('b', x.name), x.disabled ? h('div', UI.badge(t('ru.off'), '')) : null); } },
          { label: t('ru.kind'), render: function (x) { return h('div', kindWord(UI, x.kind), h('div.small.muted', params(UI, x))); } },
          { label: t('ru.severity'), render: function (x) { return sevBadge(UI, x.severity); } },
          { label: t('ru.group'), render: function (x) { return h('span.small', x.group ? t('ru.group.of', { g: x.group }) : t('ru.allmembers')); } },
          { label: t('ru.for'), render: function (x) { return h('span.small', String(secs(x.for_ms))); } },
          { label: t('ru.repeat'), render: function (x) { return h('span.small', x.repeat_ms ? String(mins(x.repeat_ms)) : t('ru.never')); } },
          { label: t('ru.webhooks'), render: function (x) { return h('span.small', (x.webhooks || []).length ? x.webhooks.join(', ') : t('ru.allhooks')); } },
          { label: '', render: function (x) {
            if (b.read_only || !admin) return null;
            return h('span.btn-row', UI.btn(t('edit'), 'edit', function () { ruleForm(UI, ctx, x); }, 'ghost sm'),
              UI.btn(t('delete'), 'trash', function () { removeItem(UI, ctx, '/api/alerts/rules/', x.name, t('ru.rm.ask', { name: x.name }), t('ru.removed', { name: x.name })); }, 'ghost sm'));
          } }
        ], b.rules || [], { empty: t('ru.none') }), null, true)));
    }, failed(UI, body, el));
  }
  function params(UI, x) {
    var t = UI.t;
    if (x.kind === 'cert_expiry') return t('ru.days') + ' ' + x.days;
    if (x.kind === 'backends_down') return t('ru.threshold') + ' ' + x.threshold;
    if (x.kind === 'drift') return t('ru.source.' + (x.source || 'both'));
    if (x.resolve_after_ms) return t('ru.resolve') + ' ' + mins(x.resolve_after_ms);
    return '';
  }
  function removeItem(UI, ctx, path, name, ask, done) {
    UI.confirm(ask, UI.t('delete'), true).then(function (yes) {
      if (!yes) return;
      FC.call(UI, 'DELETE', path + encodeURIComponent(name), undefined, ctx.etag).then(function () {
        UI.toast(done);
        ctx.reload();
      }, function (e) { UI.toast(e.message, 'error'); if (e.status === 412) ctx.reload(); });
    });
  }
  function ruleForm(UI, ctx, x) {
    var h = UI.h, t = UI.t;
    x = x || { kind: 'member_down', for_ms: 60000, repeat_ms: 4 * 3600000, webhooks: [] };
    var name = h('input.input', { name: 'rule-name', value: x.name || '', autocomplete: 'off', maxlength: 64 });
    var kind = h('select.select', { name: 'rule-kind' }, ctx.cfg.kinds.map(function (k) { return h('option', { value: k, selected: k === x.kind }, kindWord(UI, k)); }));
    var kindHint = h('span.field__hint');
    var sev = h('select.select', { name: 'rule-severity' }, ctx.cfg.severities.map(function (s) {
      return h('option', { value: s, selected: s === (x.severity || '') }, t('al.sev.' + s));
    }));
    if (!x.severity) sev.value = '';
    var group = h('select.select', { name: 'rule-group' }, h('option', { value: '' }, t('ru.allmembers')),
      ctx.groups.map(function (g) { return h('option', { value: g, selected: g === x.group }, g); }));
    var forS = num(UI, 'rule-for', secs(x.for_ms)), repeat = num(UI, 'rule-repeat', mins(x.repeat_ms));
    var days = num(UI, 'rule-days', x.days || 14, 1), threshold = num(UI, 'rule-threshold', x.threshold || 1, 1);
    var resolve = num(UI, 'rule-resolve', mins(x.resolve_after_ms));
    var source = h('select.select', { name: 'rule-source' }, ['', 'templates', 'consistency'].map(function (s) {
      return h('option', { value: s, selected: s === (x.source || '') }, t('ru.source.' + (s || 'both')));
    }));
    var picked = {};
    (x.webhooks || []).forEach(function (w) { picked[w] = true; });
    var hooks = h('div.cl-chips', (ctx.cfg.webhooks || []).map(function (w) {
      var c = h('input', { type: 'checkbox', checked: !!picked[w.name], 'aria-label': w.name });
      c.addEventListener('change', function () { picked[w.name] = c.checked; });
      return h('label.cl-chip', c, ' ', w.name);
    }));
    var off = h('input', { type: 'checkbox', name: 'rule-disabled', checked: !!x.disabled });
    var fDays = UI.field(t('ru.days'), days), fThr = UI.field(t('ru.threshold'), threshold), fSrc = UI.field(t('ru.source'), source),
      fRes = UI.field(t('ru.resolve'), resolve, t('ru.resolve.hint'));
    function sync() {
      var k = kind.value;
      kindHint.textContent = t('al.k.' + k + '.hint');
      fDays.hidden = k !== 'cert_expiry'; fThr.hidden = k !== 'backends_down'; fSrc.hidden = k !== 'drift';
      fRes.hidden = k !== 'rollout_failed' && k !== 'reload_failed';
    }
    kind.addEventListener('change', sync);
    sync();
    var err = errorBox(UI);
    UI.modal({
      title: x.name ? t('ru.edit', { name: x.name }) : t('ru.add'),
      body: h('form.form.cl-form.stack', { onsubmit: function (e) { e.preventDefault(); } },
        UI.field(t('ru.name'), name), h('label.field', h('span.field__label', t('ru.kind')), kind, kindHint), UI.field(t('ru.severity'), sev),
        UI.field(t('ru.group'), group), UI.field(t('ru.for'), forS, t('ru.for.hint')), UI.field(t('ru.repeat'), repeat, t('ru.repeat.hint')),
        fDays, fThr, fSrc, fRes, h('label.field', h('span.field__label', t('ru.webhooks')), hooks, h('span.field__hint', t('ru.webhooks.hint'))),
        h('label.row', off, ' ', t('ru.disabled')), err),
      actions: [{ label: t('cancel') }, { label: t('fm.save'), kind: 'primary', onclick: function () {
        err.hidden = true;
        var k = kind.value;
        var req = { name: name.value.trim(), kind: k, severity: sev.value, group: group.value, for_ms: ms(forS.value) * 1000,
          repeat_ms: ms(repeat.value) * 60000, webhooks: Object.keys(picked).filter(function (w) { return picked[w]; }), disabled: off.checked };
        if (k === 'cert_expiry') req.days = ms(days.value);
        if (k === 'backends_down') req.threshold = ms(threshold.value);
        if (k === 'drift') req.source = source.value;
        if (k === 'rollout_failed' || k === 'reload_failed') req.resolve_after_ms = ms(resolve.value) * 60000;
        var p = x.name ? FC.call(UI, 'PUT', '/api/alerts/rules/' + encodeURIComponent(x.name), req, ctx.etag) : FC.call(UI, 'POST', '/api/alerts/rules', req, ctx.etag);
        return p.then(function () { UI.toast(t('ru.saved', { name: req.name })); ctx.reload(); }, function (e) {
          showError(err, e);
          if (e.status === 412) ctx.reload();
          return false;
        });
      } }]
    });
  }

  // ---- Webhooks / Webhook ----
  function webhooksTab(UI, sh, body, el, reload) {
    var h = UI.h, t = UI.t;
    FC.call(UI, 'GET', '/api/alerts/config').then(function (r) {
      if (!document.body.contains(el)) return;
      var b = r.body || {}, ctx = { cfg: b, etag: r.etag, reload: reload };
      var admin = FC.canCluster('admin');
      var add = admin ? UI.btn(t('wh.add'), 'plus', function () { webhookForm(UI, ctx, null); }, 'primary') : null;
      if (add) add.disabled = !!b.read_only || (b.webhooks || []).length >= b.bounds.max_webhooks;
      UI.mount(body, h('div.stack', b.read_only ? h('div.callout.callout--warn', t('al.readonly', { why: b.read_only })) : null,
        h('div.row.cl-bar', add),
        UI.card(null, UI.table([
          { label: t('wh.name'), render: function (w) { return h('div', h('b', w.name), w.disabled ? h('div', UI.badge(t('ru.off'), '')) : null); } },
          { label: t('wh.format'), render: function (w) { return h('div', t('wh.f.' + w.format), w.secret_set ? h('div', UI.badge(t('wh.secretset'), 'info')) : null); } },
          { label: t('wh.url'), render: function (w) { return h('code.small', w.url_host + '/…'); } },
          { label: t('wh.status'), render: function (w) { return statusCell(UI, w.status || {}); } },
          { label: '', render: function (w) {
            if (b.read_only || !admin) return null;
            var test = UI.btn(t('wh.test'), 'zap', function () {
              UI.busy(test, FC.call(UI, 'POST', '/api/alerts/webhooks/' + encodeURIComponent(w.name) + '/test').then(function (res) {
                var x = res.body || {};
                UI.toast(x.ok ? t('wh.test.ok', { name: w.name, ms: x.took_ms }) : t('wh.test.failed', { name: w.name, err: x.error }), x.ok ? '' : 'error');
                reload();
              }, function (e) { UI.toast(e.message, 'error'); }));
            }, 'ghost sm');
            return h('span.btn-row', test, UI.btn(t('edit'), 'edit', function () { webhookForm(UI, ctx, w); }, 'ghost sm'),
              UI.btn(t('delete'), 'trash', function () { removeItem(UI, ctx, '/api/alerts/webhooks/', w.name, t('wh.rm.ask', { name: w.name }), t('wh.removed', { name: w.name })); }, 'ghost sm'));
          } }
        ], b.webhooks || [], { empty: t('wh.none') }), null, true)));
    }, failed(UI, body, el));
  }
  function statusCell(UI, s) {
    var h = UI.h, t = UI.t, parts = [t('wh.sent', { n: s.sent || 0 })];
    if (s.failed) parts.push(t('wh.failed', { n: s.failed }));
    if (s.dropped) parts.push(t('wh.dropped', { n: s.dropped }));
    if (s.queued) parts.push(t('wh.queued', { n: s.queued }));
    return h('div.small', parts.join(' · '), s.last_at ? h('div.muted', t('wh.last', { ago: UI.ago(FC.sec(s.last_at)) })) : null,
      s.last_error ? h('div', { style: { color: 'var(--danger)' } }, s.last_error) : null);
  }
  function webhookForm(UI, ctx, w) {
    var h = UI.h, t = UI.t, editing = !!w;
    w = w || { format: 'json', timeout_ms: 10000, send_resolved: true };
    var name = h('input.input', { name: 'webhook-name', value: w.name || '', autocomplete: 'off', maxlength: 64 });
    var format = h('select.select', { name: 'webhook-format' }, ctx.cfg.formats.map(function (f) { return h('option', { value: f, selected: f === w.format }, t('wh.f.' + f)); }));
    var url = h('input.input', { name: 'webhook-url', type: 'password', autocomplete: 'off', placeholder: editing ? t('wh.kept') : 'https://hooks.example.com/…' });
    var secret = h('input.input', { name: 'webhook-secret', type: 'password', autocomplete: 'new-password', placeholder: w.secret_set ? t('wh.kept') : '' });
    var clear = h('input', { type: 'checkbox', name: 'webhook-clear-secret' });
    var tpl = h('textarea.textarea.textarea--code', { name: 'webhook-template', rows: 4, spellcheck: 'false' }, w.template || '');
    var tplHint = h('span.field__hint');
    var timeout = num(UI, 'webhook-timeout', secs(w.timeout_ms), 1);
    var resolved = h('input', { type: 'checkbox', name: 'webhook-resolved', checked: w.send_resolved !== false });
    var off = h('input', { type: 'checkbox', name: 'webhook-disabled', checked: !!w.disabled });
    var fTpl = h('label.field', h('span.field__label', t('wh.template')), tpl, tplHint);
    function sync() {
      var f = format.value;
      tplHint.textContent = t(f === 'custom' ? 'wh.template.custom' : f === 'json' ? 'wh.template.json' : 'wh.template.line');
      tpl.disabled = f === 'json';
      if (f === 'custom' && !tpl.value) tpl.value = '{"text": "${text}", "status": "${status}", "count": ${count}, "alerts": ${alerts}}';
    }
    format.addEventListener('change', sync);
    sync();
    var err = errorBox(UI);
    UI.modal({
      title: editing ? t('wh.edit', { name: w.name }) : t('wh.add'), wide: true,
      body: h('form.form.cl-form.stack', { onsubmit: function (e) { e.preventDefault(); } },
        UI.field(t('wh.name'), name), UI.field(t('wh.format'), format), UI.field(t('wh.url'), url, t('wh.url.hint')),
        UI.field(t('wh.secret'), secret, t('wh.secret.hint')), w.secret_set ? h('label.row', clear, ' ', t('wh.clear')) : null,
        fTpl, UI.field(t('wh.timeout'), timeout), h('label.row', resolved, ' ', t('wh.resolved')), h('label.row', off, ' ', t('wh.disabled')), err),
      actions: [{ label: t('cancel') }, { label: t('fm.save'), kind: 'primary', onclick: function () {
        err.hidden = true;
        var req = { name: name.value.trim(), format: format.value, url: url.value.trim(), secret: secret.value, clear_secret: clear.checked,
          template: format.value === 'json' ? '' : tpl.value, timeout_ms: ms(timeout.value) * 1000, send_resolved: resolved.checked, disabled: off.checked };
        var p = editing ? FC.call(UI, 'PUT', '/api/alerts/webhooks/' + encodeURIComponent(w.name), req, ctx.etag) : FC.call(UI, 'POST', '/api/alerts/webhooks', req, ctx.etag);
        return p.then(function () { UI.toast(t('wh.saved', { name: req.name })); ctx.reload(); }, function (e) {
          showError(err, e);
          if (e.status === 412) ctx.reload();
          return false;
        });
      } }]
    });
  }

  // ---- Silences / 静默 ----
  function silencesTab(UI, sh, body, el, reload) {
    var h = UI.h, t = UI.t;
    FC.call(UI, 'GET', '/api/alerts/config').then(function (r) {
      if (!document.body.contains(el)) return;
      var b = r.body || {}, now = Date.now();
      function state(s) { return Date.parse(s.ends) <= now ? 'ended' : Date.parse(s.starts) > now ? 'later' : 'active'; }
      // A silence is an operator's of what it silences (I148). / 静默需要在其所静默的对象上是操作员（I148）。
      var add = FC.canAny('operator') ? UI.btn(t('si.add'), 'plus', function () { silenceForm(UI, {}, reload); }, 'primary') : null;
      if (add) add.disabled = !!b.read_only;
      UI.mount(body, h('div.stack', b.read_only ? h('div.callout.callout--warn', t('al.readonly', { why: b.read_only })) : null,
        h('div.row.row--between.cl-bar', add, h('span.small.muted', t('si.hint'))),
        UI.card(null, UI.table([
          { label: t('si.state'), render: function (s) { var st = state(s); return UI.badge(t('si.s.' + st), st === 'active' ? 'info' : '', st === 'active'); } },
          { label: t('si.rule'), render: function (s) { return s.rule || h('span.muted', t('si.anyrule')); } },
          { label: t('si.node'), render: function (s) { return s.node ? h('a', { href: '#/cluster/node/' + encodeURIComponent(s.node) }, s.node) : h('span.muted', t('si.anynode')); } },
          { label: t('si.starts'), render: function (s) { return h('span.small', FC.when(s.starts)); } },
          { label: t('si.ends'), render: function (s) { return h('span.small', FC.when(s.ends)); } },
          { label: t('si.comment'), render: function (s) { return h('div.small', s.comment || '', h('div.muted', t('si.by') + ' ' + s.created_by)); } },
          { label: '', render: function (s) {
            if (b.read_only || state(s) === 'ended' || !(s.node ? FC.canOn('operator', s.node) : FC.canAny('operator'))) return null;
            return UI.btn(t('si.end'), 'x', function () {
              FC.call(UI, 'DELETE', '/api/alerts/silences/' + encodeURIComponent(s.id), undefined, r.etag).then(function () {
                UI.toast(t('si.ended'));
                reload();
              }, function (e) { UI.toast(e.message, 'error'); reload(); });
            }, 'ghost sm');
          } }
        ], b.silences || [], { empty: t('si.none') }), null, true)));
    }, failed(UI, body, el));
  }
  // silenceForm adds a silence, its rule and member filled in from an alert when given. / 添加静默，可由告警预填规则与成员。
  function silenceForm(UI, pre, after) {
    var h = UI.h, t = UI.t;
    Promise.all([FC.call(UI, 'GET', '/api/alerts/config'), FC.members(UI)]).then(function (res) {
      var cfg = res[0].body || {}, names = res[1].nodes.map(function (n) { return n.name; }).sort();
      var rule = h('select.select', { name: 'silence-rule' }, h('option', { value: '' }, t('si.anyrule')),
        (cfg.rules || []).map(function (x) { return h('option', { value: x.name, selected: x.name === pre.rule }, x.name); }));
      var node = h('select.select', { name: 'silence-node' }, h('option', { value: '' }, t('si.anynode')),
        names.map(function (n) { return h('option', { value: n, selected: n === pre.node }, n); }));
      var spans = [['si.h1', 1], ['si.h2', 2], ['si.h4', 4], ['si.h12', 12], ['si.d1', 24], ['si.d3', 72], ['si.w1', 168]];
      var dur = h('select.select', { name: 'silence-for' }, spans.map(function (s) { return h('option', { value: s[1], selected: s[1] === 2 }, t(s[0])); }));
      var comment = h('input.input', { name: 'silence-comment', autocomplete: 'off', maxlength: 512 });
      var err = errorBox(UI);
      UI.modal({
        title: t('si.add'),
        body: h('form.form.cl-form.stack', { onsubmit: function (e) { e.preventDefault(); } }, h('div.small.muted', t('si.hint')),
          UI.field(t('si.rule'), rule), UI.field(t('si.node'), node), UI.field(t('si.for'), dur), UI.field(t('si.comment'), comment), err),
        actions: [{ label: t('cancel') }, { label: t('si.add'), kind: 'primary', onclick: function () {
          err.hidden = true;
          if (!rule.value && !node.value) { showError(err, { message: t('si.need') }); return false; }
          var req = { rule: rule.value, node: node.value, comment: comment.value.trim(), duration_ms: ms(dur.value) * 3600000 };
          return FC.call(UI, 'POST', '/api/alerts/silences', req).then(function () {
            UI.toast(t('si.added'));
            after();
          }, function (e) { showError(err, e); return false; });
        } }]
      });
    }, function (e) { UI.toast(e.message, 'error'); });
  }
})();
