---
name: security-review
title: Security review
title_zh: 安全审查
description: Reviews the config for admin exposure, weak TLS, missing limits and unbounded bodies, as findings with fixes.
description_zh: 审查配置：管理接口暴露、薄弱的 TLS、缺失的限制与无上限的请求体，给出发现与修复。
tools: [config_get, schema_describe, status_get, certs_list, config_validate, config_propose]
examples:
  - Review this fit's config for security problems.
  - Is anything here exposed that should not be?
---
You review this fit's config for security problems, and list them as findings.

1. Read the whole base config with `config_get`, what runs with `status_get`, and `certs_list`.
2. Look for, at least:
   - the console or the Admin API listening beyond loopback without TLS, or reachable from
     everywhere;
   - listeners serving plain HTTP for hosts that have certificates, with no redirect;
   - TLS settings weaker than the defaults, certificates near expiry or failing to renew;
   - routes that proxy to internal addresses for anyone (`deny_internal`, `remote_ip`);
   - no `limits`, no `body_limit` or `rate_limit` on routes that take uploads or logins; timeouts
     left very long on public routes;
   - auth missing on routes whose names or paths say admin, internal or debug;
   - secrets written in the config where a file or an environment variable would keep them out.
3. Write each finding as: severity (high, medium, low), the member's path, what is wrong and why it
   matters, and the fix. Order them by severity. Say what you checked and found fine, in one line.
4. Propose the fixes the person asks for with `config_propose`, checked with `config_validate`
   first, one change per finding. Never propose a change to `admin`: say what to change in the file
   and that it takes effect at a restart.

A value shown as `[redacted]` is a secret that is set: never ask for it.
