---
name: reverse-proxy
title: Reverse proxy
title_zh: 反向代理
description: Sets up an upstream pool with health checks, load balancing, timeouts and retries, and the route that proxies to it.
description_zh: 配置上游池：健康检查、负载均衡、超时与重试，以及代理到它的路由。
tools: [config_get, schema_describe, config_validate, upstream_health, status_get, metrics_query, config_propose, page_get, page_edit, page_revert]
inputs:
  - name: upstream
    description: The pool's name, to change one that exists or name a new one.
  - name: addresses
    description: The backends, as host:port, comma separated.
examples:
  - Put app behind fit with three backends, health checked on /healthz, least connections.
  - The api upstream times out under load; set sensible timeouts and retries.
---
You set up or tune a pool of backends that a route proxies to.

1. Read: `config_get` with `members: ["upstreams", "routes"]`, and `upstream_health` for the pool
   when it exists (which backends are up, failing or probed down). For tuning, read its traffic with
   `metrics_query` (`upstream`).
2. Work out the pool with `schema_describe` on `upstreams` for the exact members:
   - `addresses` (and `weights` when the backends differ in size);
   - `load_balancing` as asked, the default otherwise; `hash_key` or `sticky_cookie` only for
     sessions that must stay on one backend;
   - `health_check` with a cheap path the backends answer, an `interval` of a few seconds, `fails`
     and `passes`; passive `max_fails` and `fail_timeout` beside it;
   - `connect_timeout`, `response_header_timeout` and `response_timeout` that fit what the backends
     do (a streaming or long-polling service needs a long `response_timeout`);
   - `max_conns` and `max_pending` so that a slow backend is not drowned.
3. The route: reuse the one that proxies to the pool, or add one (`proxy` handler with
   `upstream`), and retries in the proxy's params only for requests that may be sent twice.
4. Ask with `ask_user` for what you cannot read: the backends' addresses, the health path.
5. Check with `config_validate` (`patch` with the members you change), then `config_propose`.
6. Say what the pool does when a backend fails, in two or three lines.

Without the propose tools, show the config text instead.

When the page the person is on edits every member the change touches (page_context's `editable.members`)
and `page_edit` is offered, draft the change there instead: `page_get` first, then `page_edit` with the same
operations, and say it waits on the page for the person's Save and apply. A change that touches a member the
page does not edit is proposed, or shown as text, as above.
