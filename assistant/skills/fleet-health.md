---
name: fleet-health
title: Fleet health
title_zh: 集群体检
description: Reads the members of a manager together and says who is down, behind, drifting, failing reloads or near a certificate's expiry, and what to look at first.
description_zh: 汇总读取管理端的各成员，指出哪些成员宕机、版本落后、配置漂移、重载失败或证书即将过期，以及应先处理什么。
tools: [cluster_members, cluster_metrics, cluster_certs, cluster_log_tail, cluster_rollouts, cluster_templates, status_get, upstream_health, consistency_check, certs_list]
inputs:
  - name: group
    description: Only this group's members.
  - name: member
    description: A member to look at first, by name.
examples:
  - How is the fleet doing?
  - Which members need attention in the eu group?
  - Is anything about to expire?
---
You read the members this manager manages and say, in a short report, what needs a person's
attention, the worst first. You read; you change nothing.

1. `cluster_members` (with `group` when given) for every member's status, versions, config
   version, drift, sync with its templates, nearest certificate expiry, reloads, backends down and
   alerts firing. Count them by status first: "12 members: 10 up, 1 down, 1 error".
2. Group what you find, each with the members by name:
   - **down or in error**: since when and the error's kind (timeout, unreachable, auth, tls_pin,
     password_change); a member that asks for a new password is a person's to log in to and change.
   - **behind**: a fit or fib version other than most members', or a note that its config schema is
     not the manager's (its pages are read-only from here).
   - **drifting**: consistency drift (what runs differs from its config) or templates out of sync or
     in conflict; name the template and the part.
   - **failing**: reloads failed, upstream backends down, a rollout failed (`cluster_rollouts`).
   - **expiring**: `cluster_certs` with `within_days` 30 for the certificates, soonest first.
3. When a member stands out, read it with the read tools' `member` argument: `status_get`,
   `upstream_health`, `consistency_check` or `certs_list` with `member` set to its name.
   `cluster_metrics` compares requests and 5xx across members, now and over the last polls; a
   member far from the others is worth a sentence. `cluster_log_tail` with its name (and `level`
   `warn`) shows what it logged lately.
4. A member that does not answer a read is reported as such, with what its last poll showed; never
   wait on it or retry in a loop.
5. End with what to do first, one line each: fix the drift with the `cluster-drift` skill, plan a
   rollout with `rollout-planner`, renew a certificate, or look at a member's own pages from the
   node switcher. No member's credentials are ever shown to you; never ask for them.
