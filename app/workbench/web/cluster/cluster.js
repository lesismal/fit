// The Cluster menu (I142): a first-level entry of the workbench, shown when the console serves the module cluster,
// which a fit with admin.cluster runs (the manager of other fits): Overview, every member's state; Nodes, the members
// added, tested, trusted, edited and removed; and a member's page, #/cluster/node/<name>, opened from both. The pages
// are overview.js, nodes.js and member.js, on shared.js; docs/cluster.md describes them and the API they call.
// Groups (I144, groups.js): resource groups, a group's page with its members and the operations on them
// (#/cluster/group/<name>), and the same over every member (#/cluster/ops). Templates (I145, templates.js): templates,
// their members' sync states, Compare and Import from a node. Rollouts, drift and alerts,
// users and audit (I146–I148) come as more pages of this menu.
//
// 集群菜单（I142）：工作台的一级入口，在控制台提供模块 cluster 时显示（设置了 admin.cluster 的 fit，即其他 fit 的管理端）：
// 概览，各成员的状态；节点，添加、测试、信任、编辑与删除成员；以及成员页 #/cluster/node/<name>，两者都可打开。页面在
// overview.js、nodes.js 与 member.js 中，基于 shared.js；docs/cluster.md 介绍这些页面及其调用的 API。分组、模板、发布、
// 漂移与告警、用户与审计（I145–I148）将作为本菜单的更多页面加入。分组（I144，groups.js）：资源分组、分组页及对其成员的操作
// （#/cluster/group/<name>），以及覆盖全部成员的同一页面（#/cluster/ops）。
(function () {
  'use strict';
  var FC = window.FitCluster;
  WB.module({
    id: 'cluster', server: 'cluster', icon: 'hub', label: 'm.cluster', words: FC.WORDS,
    nav: [{ id: 'overview', icon: 'dashboard', label: 'n.overview' }, { id: 'nodes', icon: 'server', label: 'n.nodes' },
      { id: 'groups', icon: 'cube', label: 'n.groups' }, { id: 'templates', icon: 'layers', label: 'n.templates' }],
    setup: function (UI, sh) {
      return {
        routes: {
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
          compare: function () { FC.pages.compare(UI, sh); }
        },
        fallback: 'overview'
      };
    }
  });
})();
