---
name: cluster-drift
title: Cluster drift
title_zh: 集群漂移
description: Explains each member's drift, against its templates or its own config, and plans the fix as a rollout or a reload for a person to start.
description_zh: 解释每个成员的漂移（相对其模板或自身配置），并把修复规划为一次发布或重载，由人启动。
tools: [cluster_members, cluster_templates, cluster_rollouts, consistency_check, config_get, drift_fix_propose, rollout_propose]
inputs:
  - name: member
    description: The member whose drift to explain, by name.
  - name: group
    description: Only this group's members.
examples:
  - Why is node-3 drifting?
  - Fix the drift of the eu group.
---
A member drifts in two ways, and you say which for each one:

- **Against its templates**: a part a template owns is out of sync (the template changed, or the part
  is missing) or in conflict (someone changed it on the member, or the member had its own value
  before the template owned it). `cluster_templates` with the template shows each member's parts
  and why; `config_get` with `member` shows what the member has now.
- **Against its own config**: its consistency check finds that what runs differs from its config
  (`consistency_check` with `member`): a listener not bound, a certificate not loaded, a setting
  that needs a reload.

1. `cluster_members` (with `group` when given) for the members that drift: consistency drift
   above zero, or templates out of sync, in conflict or in error. Start with `member` when given.
2. For each, explain what differs and why, in a line or two, naming the part and the template.
   A conflict means someone changed the member by hand: say that applying the template overwrites
   that change, and ask with `ask_user` whether to keep the member's value (then the template should
   change, with `rollout_propose` kind template and its config) or the template's.
3. Fix with `drift_fix_propose` and the members: a member out of sync or in conflict gets a sync
   rollout's plan, each member's diff checked by its own dry run; a member whose consistency check
   differs gets a reload, checked. Neither starts: give the person the links, where they start them
   with their confirm.
4. A member that is down or another version of fit cannot be fixed now: say so and leave it out.
   A template that does not render for a member (a variable it lacks) is fixed in the template or
   the member's labels, not by a rollout.
