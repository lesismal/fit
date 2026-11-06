// Ask AI on the Cluster pages (I153): the Overview's fleet and each member's card ask fleet-health, a failed batch of a
// rollout asks rollout-planner, and a drifting member on the Drift page asks cluster-drift. Each opens the assistant's
// panel with the skill, its inputs and a question filled in, and what the page shows of the thing as the selection:
// members' states, a batch's gates, a member's drift, never a credential (the pages hold none). The pages call these
// where they draw; each returns null while the assistant is off for the user (FitAI.askButton).
//
// 集群页面上的“问 AI”（I153）：概览中的集群与每张成员卡片询问 fleet-health，发布中失败的批次询问 rollout-planner，漂移页上
// 漂移的成员询问 cluster-drift。点击后以技能、输入与预填的问题打开助手面板，把页面所示的对象作为选中项（成员状态、批次的健康
// 检查、成员的漂移，从不含凭据：页面中没有凭据）。页面在绘制处调用这些函数；用户的助手关闭时它们返回 null（FitAI.askButton）。
(function () {
  'use strict';
  var FC = window.FitCluster;
  FC.WORDS.push(
    ['ask.fleet', 'How is the fleet? Who is down, behind, drifting or near a certificate\'s expiry, and what should I look at first?',
      '集群状况如何？哪些成员宕机、版本落后、配置漂移或证书即将过期，应先处理什么？'],
    ['ask.fleet.group', 'How are the members of the group {group}? Who needs attention, and what first?', '分组 {group} 的成员状况如何？哪些需要处理，先处理什么？'],
    ['ask.fleet.member', 'How is the member {name} doing, beside the others? Anything to look at?', '成员 {name} 与其他成员相比状况如何？有什么需要关注的？'],
    ['ask.fleet.btn', 'Ask AI about the fleet', '就集群询问 AI'],
    ['ask.batch', 'Batch {n} of the rollout {id} failed: {why}. Why, and how do we go on?', '发布 {id} 的第 {n} 批失败：{why}。原因是什么？如何继续？'],
    ['ask.drift', 'Why does the member {name} drift ({what}), and how is it fixed?', '成员 {name} 为何漂移（{what}）？如何修复？'],
    ['ask.drift.tpl', 'templates {state}', '模板 {state}'], ['ask.drift.check', '{n} consistency differences', '{n} 处一致性差异']
  );

  // brief is what the selection carries of a member's state. / 选中项所带的成员状态摘要。
  function brief(s) {
    var e = s.error || {};
    return {
      name: s.name, status: s.status, since: s.since, groups: s.groups, labels: s.labels,
      error: s.error ? { kind: e.kind, message: e.message } : undefined,
      fit_version: s.node && s.node.fit_version, config_version: s.config && s.config.version,
      drift: s.drift ? s.drift.count : undefined,
      cert_expiry: s.certificates ? s.certificates.nearest_expiry : undefined,
      backends_down: s.upstreams ? s.upstreams.backends_down : undefined, reloads_failed: s.reloads ? s.reloads.failed : undefined
    };
  }

  // askFleet is the Overview's Ask AI: fleet-health on the members shown, rows() returning them and the group filtered
  // by. / 概览的“问 AI”：对所示成员询问 fleet-health；rows() 返回成员与筛选的分组。
  FC.askFleet = function (UI, rows) {
    if (!(window.FitAI && FitAI.askButton)) return null;
    return FitAI.askButton(function () {
      var r = rows(), counts = {};
      r.nodes.forEach(function (s) { counts[s.status] = (counts[s.status] || 0) + 1; });
      return {
        skill: 'fleet-health',
        message: r.group ? UI.t('ask.fleet.group', { group: r.group }) : UI.t('ask.fleet'),
        inputs: { group: r.group || '' },
        selection: { cluster_overview: { counts: counts, members: r.nodes.map(brief) } }
      };
    }, { label: UI.t('ask.fleet.btn') });
  };

  // askCard is a member card's Ask AI: fleet-health on that member. / 成员卡片的“问 AI”：就该成员询问 fleet-health。
  FC.askCard = function (UI, s) {
    if (!(window.FitAI && FitAI.askButton)) return null;
    return FitAI.askButton(function () {
      return { skill: 'fleet-health', message: UI.t('ask.fleet.member', { name: s.name }), inputs: { member: s.name },
        selection: { cluster_member: brief(s) } };
    }, { label: false });
  };

  // askBatch is a failed batch's Ask AI: rollout-planner on the rollout and the batch, its members' gates and errors as
  // the selection. / 失败批次的“问 AI”：就该发布与批次询问 rollout-planner，成员的健康检查与错误作为选中项。
  FC.askBatch = function (UI, r, b) {
    if (!(window.FitAI && FitAI.askButton) || (b.state !== 'failed' && b.state !== 'rolled_back')) return null;
    return FitAI.askButton(function () {
      var by = {};
      (r.members || []).forEach(function (x) { by[x.node] = x; });
      var why = [];
      var members = (b.nodes || []).map(function (n) {
        var x = by[n] || {}, g = x.gate || {};
        (g.reasons || []).forEach(function (s) { if (why.indexOf(s) < 0) why.push(s); });
        if (x.error && why.indexOf(x.error.message) < 0) why.push(x.error.message);
        return { node: n, status: x.status, gate: x.gate, error: x.error, applied: x.applied, rolled_back: x.rolled_back, changed: x.changed };
      });
      return {
        skill: 'rollout-planner',
        message: UI.t('ask.batch', { n: b.index + 1, id: r.id, why: why.slice(0, 3).join('; ') || r.reason || r.state }),
        inputs: { rollout: r.id, batch: String(b.index + 1), group: r.group || '', template: r.template || '' },
        selection: { cluster_rollout: { id: r.id, kind: r.kind, state: r.state, reason: r.reason, strategy: r.strategy,
          batch: { index: b.index + 1, canary: b.canary, state: b.state, members: members } } }
      };
    });
  };

  // askDrift is a drifting member's Ask AI on the Drift page: cluster-drift on it. / 漂移页上漂移成员的“问 AI”：就其询问 cluster-drift。
  FC.askDrift = function (UI, n) {
    if (!(window.FitAI && FitAI.askButton) || !n.drifted) return null;
    return FitAI.askButton(function () {
      var what = [];
      if (n.templates && n.templates.state !== 'in_sync') what.push(UI.t('ask.drift.tpl', { state: n.templates.state }));
      if (n.consistency && n.consistency.count) what.push(UI.t('ask.drift.check', { n: n.consistency.count }));
      return {
        skill: 'cluster-drift',
        message: UI.t('ask.drift', { name: n.node, what: what.join(', ') }),
        inputs: { member: n.node },
        selection: { cluster_drift: { node: n.node, status: n.status, groups: n.groups, templates: n.templates, consistency: n.consistency } }
      };
    }, { label: false });
  };
})();
