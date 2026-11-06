// The cards of what the cluster's propose tools made (I153): rollout_propose and drift_fix_propose plan a rollout,
// cluster_op_propose checks an operation, on the manager, and none joins the conversation's proposal: Apply never starts
// them. Under the tool's result the panel draws a card with the link the result names, to the rollout's plan on the
// Rollouts page or to the operations page filled in, where a person starts it with that page's own confirm. The link is
// read from the result as data, and only a workbench link (#/cluster/...) is drawn; nothing of it is inserted as HTML.
//
// 集群提案工具所生成内容的卡片（I153）：rollout_propose 与 drift_fix_propose 在管理端规划一次发布，cluster_op_propose 检查
// 一次操作，均不加入对话的提案：“应用”从不启动它们。面板在工具结果下绘制一张卡片，链接到结果中给出的发布规划（发布页）或
// 预填的操作页，由人在该页以其自身的确认启动。链接作为数据从结果中读取，只绘制工作台链接（#/cluster/...），不作为 HTML 插入。
(function () {
  'use strict';
  var WORDS = [
    ['pl.rollout', 'Rollout plan', '发布规划'], ['pl.op', 'Operation, not started', '操作（未启动）'], ['pl.fix', 'Drift fix', '漂移修复'],
    ['pl.note', 'Nothing was applied. Open it to review each member\'s diff and dry run, and start it there with its confirm.',
      '尚未应用任何内容。打开后可查看每个成员的差异与试运行，并在该页确认后启动。'],
    ['pl.noteOp', 'Nothing was run. Open it to review the members and start it there with its confirm.', '尚未执行任何操作。打开后可查看成员，并在该页确认后启动。'],
    ['pl.open', 'Open', '打开'], ['pl.state', 'State: {s}', '状态：{s}']
  ];
  var en = {}, zh = {};
  WORDS.forEach(function (w) { en[w[0]] = w[1]; zh[w[0]] = w[2]; });
  UI.addI18n({ 'en': en, 'zh-CN': zh }, 'ai');

  var TITLE = { rollout_propose: 'pl.rollout', cluster_op_propose: 'pl.op', drift_fix_propose: 'pl.fix' };

  // linkOf reads the first workbench link of a result, a JSON string's escapes undone. / 读取结果中的第一个工作台链接（还原 JSON 转义）。
  function linkOf(content) {
    var m = /"link":"(#\/cluster\/(?:[^"\\]|\\.)*)"/.exec(content || '');
    if (!m) return '';
    try { return JSON.parse('"' + m[1] + '"'); } catch (e) { return ''; }
  }

  window.FitAI = window.FitAI || {};
  // resultCard returns the card of a cluster propose tool's result, or null. / 返回集群提案工具结果的卡片，或 null。
  FitAI.resultCard = function (UI, name, content, isErr) {
    var h = UI.h, t = UI.t, key = TITLE[name], link = linkOf(content);
    if (!key || isErr || !link) return null;
    var st = /"state":"([a-z_]+)"/.exec(content || '');
    var op = link.indexOf('#/cluster/rollout/') !== 0;
    return h('div.callout.callout--info.small.ai-plan',
      h('div.row.row--between', h('b', UI.icon(op ? 'zap' : 'send'), ' ', t(key)),
        h('a.btn.btn--primary.btn--sm', { href: link }, t('pl.open'))),
      st && !op ? h('div', t('pl.state', { s: st[1] })) : null,
      h('div.muted', t(op ? 'pl.noteOp' : 'pl.note')));
  };
  FitAI.planLink = linkOf;
})();
