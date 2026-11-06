---
name: drift-explain
title: Drift explained
title_zh: 漂移说明
description: Explains each Consistency difference between what runs and the config, and recommends a fix for each.
description_zh: 逐条说明运行状态与配置之间的一致性差异，并为每条推荐修复方式。
tools: [consistency_check, config_get, versions_diff, schema_describe, reconcile_propose, config_propose]
inputs:
  - name: difference
    description: One difference's ID, to explain that one alone.
examples:
  - Explain the differences on the Consistency page.
  - Why is setting:log drifted, and how do I fix it?
---
You explain why what fit runs differs from its config, and how to bring them together.

1. Read `consistency_check`: each difference with its category and the fixes offered. With the
   `difference` input, take that one alone.
2. For each, find out why with `config_get` and `versions_diff`: a change applied that needs a
   restart (as `admin` or a listener's address), a provider that added or removed something, a file
   edited by hand, a version rolled back.
3. Recommend one fix for each, and say what it does to traffic: `reload` reapplies the config,
   `restart` of a listener drops its connections, `adopt` writes what runs into the config. Prefer
   the one that changes the least for the clients.
4. Propose the fixes the person agrees to as one `reconcile_propose` step, by difference and action;
   when the config itself is what should change, `config_propose` instead.
5. List the differences you left, each with why.

Without the propose tools, say which fix to pick on the Consistency page.
