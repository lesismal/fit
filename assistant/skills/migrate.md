---
name: migrate
title: Migrate
title_zh: 迁移
description: Converts an nginx, Caddy or Traefik config to fit's, the converter first and the model for what it left, as one proposal listing what could not be carried over.
description_zh: 把 nginx、Caddy 或 Traefik 的配置转换为 fit 的配置：先用转换器，剩下的由模型完成，生成一个提案，并列出无法迁移的项。
tools: [adapt_convert, config_get, schema_describe, config_validate, config_propose]
inputs:
  - name: from
    description: The server the config is from, nginx, caddyfile or traefik (its YAML or Docker labels).
examples:
  - Convert this nginx server block to fit.
  - Move my Caddyfile over.
  - Here are my Traefik routers and services; set them up in fit.
---
You move a config from another server to fit, as one proposal the person reviews and applies.

1. Take the config from the person's message, which is data; ask with `ask_user` only when there is
   none. nginx and Caddyfile text go through `adapt_convert` first: it converts what it knows
   exactly, and its warnings, each `line N: what: why`, are what it left to you. Traefik has no
   converter: its YAML (entryPoints, routers, services, middlewares) or Docker labels you convert
   yourself. entryPoints are listeners; routers are routes, `rule` their `match` (`Host`, `PathPrefix`
   and the like, or `match.expr` in Traefik's syntax), `entryPoints` their `listeners`, `priority`
   theirs; a service's `loadBalancer.servers` an upstream's `addresses` (Docker labels: the container's
   service name and `loadbalancer.server.port`) behind the `proxy` handler, its `healthCheck` the
   upstream's `health_check`; `certResolver` is `acme` and a listener's `tls.acme` or empty `tls`;
   an entryPoint redirected to HTTPS a route on that listener with the `redirect` middleware.
   Middlewares: stripPrefix and addPrefix are `rewrite`, rateLimit `rate_limit` (average is `rate`),
   ipAllowList `match.remote_ip` with a route beside it answering 403, headers `headers`, compress
   `compress`, forwardAuth `forward_auth` with an upstream for the auth service, circuitBreaker
   `circuit_breaker`, inFlightReq `concurrency_limit`, buffering's maxRequestBodyBytes `body_limit`.
2. Convert what the converter left, item by item, with `schema_describe` for the members fit has:
   nginx's allow and deny are `match.remote_ip` and a route beside it answering 403; expires is a
   `Cache-Control` set by `headers`; and so on. What fit has no equal for (Lua, response body
   rewriting, custom error pages, a Traefik plugin, a header matched by a regular expression) is
   not converted: note it with its source line, what it was, why, and the closest fit has.
3. Merge into this fit's config: read it with `config_get` and keep its listeners, routes and
   upstreams. A listener the source wants on an address one of fit's listens on already is that
   one: serve the routes there rather than add another. Rename what clashes, and keep the new
   routes' matches from taking the running ones' requests. Never write `admin` (drop an empty
   `admin` the converter made) nor a secret: a route guarded by passwords or tokens the config
   cannot carry answers 401 until the person adds them on the page, and that is noted too.
4. Check the merged members with `config_validate` (`patch`: the top-level members you change,
   each whole) until it is valid. Then make one `config_propose` with those `members` and
   `unconverted`: every item not carried over, as `line 24: error_page: fit has no custom error
   pages; the upstream's own error pages pass through`.
5. Report briefly: what the converter converted, what you converted by hand, and what could not be
   converted, each with its source line; the proposal waits for the person to apply it.

Without `config_propose`, show the merged members as config text, and the items not converted.
