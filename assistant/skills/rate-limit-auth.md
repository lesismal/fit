---
name: rate-limit-auth
title: Rate limits and auth
title_zh: 限流与认证
description: Rate limits, IP allow and deny lists, and basic, JWT or forward auth on routes.
description_zh: 为路由配置限流、IP 允许与拒绝列表，以及 basic、JWT 或转发认证。
tools: [config_get, schema_describe, config_validate, config_propose, page_get, page_edit, page_revert]
inputs:
  - name: route
    description: The route to protect, by name.
examples:
  - Limit /api/* to 10 requests a second per client, with a burst of 20.
  - Put basic auth on the admin route.
  - Check JWTs on /api with the keys at https://id.example.com/jwks.json.
---
You protect routes: limits on how often and how much, and who may pass.

1. Read the routes with `config_get` (`members: ["routes"]`) and find the one meant; ask with
   `ask_user`, offering the routes' names, when it is not clear.
2. Pick the middleware and check its params with `schema_describe`:
   - `rate_limit` by client address, a header or the route, with a rate and a burst;
     `concurrency_limit` for requests under way at once; `body_limit` for bodies;
   - who may pass by address: the route's `match.remote_ip`, or a route before it that answers 403;
   - `basic_auth` with bcrypt hashes (the person makes them with `fit hash-password`: never ask for
     a password, and never write one); `jwt` with keys from files, a JWK set or a secret kept
     elsewhere; `forward_auth` to an auth service.
3. Order matters: limits before auth, so that failed logins are limited too; auth before the
   handler and before `cache`.
4. Keep what the route already has: a middleware is added or changed in place, never the whole route
   rewritten. A value shown as `[redacted]` stays where it is.
5. Check with `config_validate`, then `config_propose`, and say what a client over the limit or
   without credentials gets back.

Without the propose tools, show the config text instead.

When the page the person is on edits every member the change touches (page_context's `editable.members`)
and `page_edit` is offered, draft the change there instead: `page_get` first, then `page_edit` with the same
operations, and say it waits on the page for the person's Save and apply. A change that touches a member the
page does not edit is proposed, or shown as text, as above.
