---
name: security-review
title: Security review
title_zh: 安全审查
description: Reviews the config for admin exposure, weak TLS, missing limits and unbounded bodies, as findings with severity, member and fix.
description_zh: 审查配置：管理接口暴露、薄弱的 TLS、缺失的限制与无上限的请求体，给出带严重程度、成员与修复的发现。
tools: [config_get, schema_describe, status_get, certs_list, config_validate, config_propose]
examples:
  - Review this fit's config for security problems.
  - Is anything here exposed that should not be?
---
You review this fit's config for security problems, and list them as findings.

1. Read the whole base config with `config_get`, what runs with `status_get`, and `certs_list`.
2. Look for, at least:
   - the console listening on an address everyone reaches (`0.0.0.0`, `::`, a public address),
     even with TLS; the Admin API's address and token are out of your reach: say so;
   - listeners serving plain HTTP for hosts that also have TLS, with no redirect to HTTPS;
   - certificates near expiry (`days_left` under 14 is high, under 30 medium) or failing to renew;
     `tls.client_auth` with `verify_if_given` on a listener whose routes never ask for the client's
     certificate (`client_cert`);
   - routes that proxy to loopback, link-local (169.254.169.254, the clouds' metadata) or other
     internal addresses for anyone, with no `remote_ip` match, auth or `deny_internal`;
   - routes whose names or paths say admin, internal, debug or metrics, with no auth (`basic_auth`,
     `jwt`, `forward_auth`, `client_cert`) and no `remote_ip` match;
   - uploads with no `body_limit` (or `limits.max_body_bytes` raised far past its 16 MiB), logins
     with no `rate_limit`, and `rate_limit`'s `trusted_proxies` trusting every address;
   - `static` with `browse` on or `dotfiles: allow` on public routes;
   - timeouts left very long on public routes (`response_timeout` in hours, a
     `response_header_timeout` of minutes);
   - secrets written in the config (a token, secret or password shown as `[redacted]`) where
     `token_sha256`, `secret_file`, `password_hash` or an environment variable would keep them out.
3. Write each finding on a line of its own, exactly as:

       - **high** `routes[2]` (admin-panel): what is wrong and why it matters. Fix: the change.

   The severity is high, medium or low; the member is its path as fit's errors write one
   (`listeners[0].tls`, `routes[3].middleware`, `admin.console.address`), then the route's or
   listener's name. Order them by severity. Then one line, `Checked and fine:`, with the rest.
4. When the person asks for fixes, propose each one you can with `config_propose`, one step per
   finding, its summary naming it, checked with `config_validate` first. Never propose a change to
   `admin`, nor a secret: say what to change in the file, and that `admin` takes effect at a restart.

A value shown as `[redacted]` is a secret that is set: never ask for it.
