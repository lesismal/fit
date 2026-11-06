// The Cluster menu (I142): a first-level entry of the workbench, shown when the console serves the module cluster,
// which a fit with admin.cluster runs (the manager of other fits): Overview, every member's state; Nodes, the members
// added, tested, trusted, edited and removed; and a member's page, #/cluster/node/<name>, opened from both. The pages
// are overview.js, nodes.js and member.js, on shared.js; docs/cluster.md describes them and the API they call.
// Groups (I144, groups.js): resource groups, a group's page with its members and the operations on them
// (#/cluster/group/<name>), and the same over every member (#/cluster/ops). Templates (I145, templates.js): templates,
// their members' sync states, Compare and Import from a node. Rollouts (I146, rollouts.js): plans, their strategies,
// progress batch by batch and the history. Drift and alerts (I147, alerts.js): members out of sync with their diffs and
// a Fix that plans a rollout, the alert rules, webhooks, alerts and silences, and the menu's badge while alerts fire.
// Users and audit (I148, users.js): the users' roles and the audit log; every page is drawn once the user's roles are
// read, and shows only what they allow (FitCluster.can*).
//
// 集群菜单（I142）：工作台的一级入口，在控制台提供模块 cluster 时显示（设置了 admin.cluster 的 fit，即其他 fit 的管理端）：
// 概览，各成员的状态；节点，添加、测试、信任、编辑与删除成员；以及成员页 #/cluster/node/<name>，两者都可打开。页面在
// overview.js、nodes.js 与 member.js 中，基于 shared.js；docs/cluster.md 介绍这些页面及其调用的 API。分组、模板、发布、
// 漂移与告警、用户与审计（I145–I148）将作为本菜单的更多页面加入。分组（I144，groups.js）：资源分组、分组页及对其成员的操作
// （#/cluster/group/<name>），以及覆盖全部成员的同一页面（#/cluster/ops）。发布（I146，rollouts.js）：规划、策略、逐批进度与历史。
// 漂移与告警（I147，alerts.js）：未同步的成员及其差异、规划发布的“修复”，告警规则、Webhook、告警与静默，以及告警触发时菜单上的徽标。
// 用户与审计（I148，users.js）：用户的角色与审计日志；每个页面在读取用户角色后绘制，只显示角色允许的内容（FitCluster.can*）。
(function () {
  'use strict';
  var FC = window.FitCluster;
  WB.module({
    id: 'cluster', server: 'cluster', icon: 'hub', label: 'm.cluster', words: FC.WORDS,
    nav: [{ id: 'overview', icon: 'dashboard', label: 'n.overview' }, { id: 'nodes', icon: 'server', label: 'n.nodes' },
      { id: 'groups', icon: 'cube', label: 'n.groups' }, { id: 'templates', icon: 'layers', label: 'n.templates' },
      { id: 'rollouts', icon: 'send', label: 'n.rollouts' }, { id: 'alerts', icon: 'alert', label: 'n.alerts' },
      { id: 'users', icon: 'users', label: 'n.users' }],
    // The count of alerts firing, on the menu's row (I147). / 菜单行上正在触发的告警数（I147）。
    badge: function () { return FC.alertBadge(); },
    boot: function (UI) { FC.alertsBoot(UI); },
    setup: function (UI, sh) {
      // Each page is drawn once the user's roles are read again (I148), so that it shows only the controls they allow;
      // a user with no role is told so. / 每个页面在重新读取用户角色（I148）之后绘制，只显示角色允许的控件；没有角色的用户
      // 会看到提示。
      function gated(fn) {
        return function (p) {
          return FC.loadMe(UI).then(function () {
            if (!FC.noRole()) return fn(p);
            var el = sh.page('overview', UI.t('m.cluster'));
            UI.clear(sh.tools);
            UI.mount(el, UI.h('div.callout.callout--warn', UI.icon('lock'), ' ', UI.t('role.norole')));
          });
        };
      }
      var routes = {
        overview: function () { FC.pages.overview(UI, sh); },
        nodes: function () { FC.pages.nodes(UI, sh); },
        'node/:name': function (p) { FC.pages.member(UI, sh, p); },
        groups: function () { FC.pages.groups(UI, sh); },
        'group/:name': function (p) { FC.pages.group(UI, sh, p); },
        ops: function () { FC.pages.ops(UI, sh); },
        // Templates (I145, templates.js). / 模板（I145，templates.js）。
        templates: function () { FC.pages.templates(UI, sh); },
        newtemplate: function () { FC.pages.template(UI, sh, null); },
        'template/:name': function (p) { FC.pages.template(UI, sh, p); },
        'rendered/:name': function (p) { FC.pages.rendered(UI, sh, p); },
        compare: function () { FC.pages.compare(UI, sh); },
        // Rollouts (I146, rollouts.js). / 发布（I146，rollouts.js）。
        rollouts: function () { FC.pages.rollouts(UI, sh); },
        newrollout: function () { FC.pages.newrollout(UI, sh); },
        'rollout/:id': function (p) { FC.pages.rollout(UI, sh, p); },
        // Drift and alerts (I147, alerts.js). / 漂移与告警（I147，alerts.js）。
        alerts: function () { FC.pages.alerts(UI, sh, {}); },
        'alerts/:tab': function (p) { FC.pages.alerts(UI, sh, p); },
        // Users and audit (I148, users.js). / 用户与审计（I148，users.js）。
        users: function () { FC.pages.users(UI, sh, 'users'); },
        audit: function () { FC.pages.users(UI, sh, 'audit'); }
      };
      Object.keys(routes).forEach(function (k) { routes[k] = gated(routes[k]); });
      return { routes: routes, fallback: 'overview' };
    }
  });
})();
