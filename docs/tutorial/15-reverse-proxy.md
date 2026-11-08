# Reverse proxy

[English](15-reverse-proxy.md) | [简体中文](15-reverse-proxy.zh-CN.md)

## What it does

The reverse proxy is fit's core: a **route** says which requests are its own, a request passes
through the **middleware** in order, and the **handler** answers it; the commonest handler hands the
request to an **upstream** — a named set of servers. An upstream says how requests are spread
(load balancing), which addresses are in rotation (passive and active health checks), and the limits
on connections, queues and timeouts.

A request goes to one route: first the routes of the listener it came on whose `match` it matches —
every field of `match` must hold, and within a field one of the values listed is enough; of those,
the one with the highest `priority`; among equal priorities, the most specific host (exact, then
`*.`, then `**.`, then none), then the most specific path (exact, then the longest prefix), then the
one with the most other matchers set, then the one written first. A request no route takes is
answered 404. The path is made canonical before any route sees it.

The module has two pages: **Routes** and **Upstreams**. Middleware and handlers are modules, named by
`module`, with their `params`; every one is listed below, each linked to its chapter of the
reference.

## Pages

### Routes

`#/gw.reverse/routes`. The page edits the config's `routes` member, with the note above it: routes
match requests and hand them to a handler — proxy passes them to an upstream, static serves files,
respond answers itself; forward proxy routes are on their own page.

- **Tabs by handler module**: **All** · the count, then every module that appears · its count. Coming
  in from a handler in the [Engine](06-engine.md) menu (`?handler=<module>`) lands on that tab; a tab
  clicked afterwards keeps its own choice.
- **Each row** shows the route's name and its match: the handler's module as a badge, the first two
  hosts, the first two paths, the methods, `→ <upstream>`, `@<listeners>`; at the end of the row is
  the **Ask AI** button, which asks the `diagnose` skill about the route — does it answer, and does it
  fail or run slow (no button while the assistant is off for the user).
- **Add route** makes a new row: `name` `route-N` and `handler` `proxy` by default, with the first
  upstream's name when the draft already has one. Every row can be **removed**, **moved up** or
  **moved down**.
- A route's form fields are `name`, `listeners`, `priority`, `match` (`host`, `path`, `path_regexp`,
  `method`, `header`, `query`, `remote_ip`, `expr`), `access`, `middleware` (a list of modules, in
  order) and `handler`.
- The **`.conf`, `.yaml` and `.json` tabs** edit the whole `routes` member — the forward proxy routes
  of the [Forward proxy](16-forward-proxy.md) page included, not only the ones this page shows; text
  typed there is parsed into the same draft as you type, and **Save and apply** checks and applies it
  whole.

### Upstreams

`#/gw.reverse/upstreams`. The page edits the config's `upstreams` member.

- **Each row** shows the upstream's name, `{n} addresses`, a badge for its `load_balancing`, and
  `{up} / {all} up`: up and total come from `GET /status`, green when all are up, amber when some,
  red when none. When a backend is down the row ends with the **Ask AI** button, which asks the
  `diagnose` skill why and what to fix. Health is extra: a row still shows when it cannot be read.
- **Add upstream** makes a new row: `name` `backend-N` and `addresses` `127.0.0.1:8081` by default.
  Every row can be **removed**, **moved up** or **moved down**.
- The form's fields are `name`, `addresses`, `weights`, `load_balancing`, `hash_key`,
  `sticky_cookie`, the limits and the timeouts, `deny_internal`, `protocol` (`http/1.1`, `h2c`, `h2`)
  and `health_check`.
- Like every page that edits the config, this page shares one draft and has its `.conf`, `.yaml` and
  `.json` tabs.

## Key parameters

Only the key parameters are listed; the full field tables are in
[the config reference: Routes](../reference/config.md#routes) and
[Upstreams](../reference/config.md#upstreams), with a table for each object:
[`routes[]`](../reference/config.md#config-route),
[`routes[].match`](../reference/config.md#config-match),
[`upstreams[]`](../reference/config.md#config-upstream),
[`upstreams[].sticky_cookie`](../reference/config.md#config-stickycookie),
[`upstreams[].health_check`](../reference/config.md#config-healthcheck).

### Routes

| field | default | description |
| --- | --- | --- |
| `name` | none | A name unique among the routes, for logs and metrics. |
| `listeners` | every listener | The names of the listeners the route serves. |
| `priority` | none (0) | Routes with a higher priority are picked first; among equal ones, the most specific host, then path, then the number of other matchers set, then the order written. |
| `match` | every request | What a request must match: `host` (exact, or with a leading `*.` or `**.` wildcard), `path` (exact or ending in `*` for a prefix), `path_regexp`, `method`, `header`, `query`, `remote_ip` (the connection's address, not X-Forwarded-For's), `expr` (matchers combined with `&&`, `\|\|`, `!` and parentheses). |
| `access` | none | The client addresses the route serves, deciding before its listener's access and the global one; a request it refuses is answered 403. |
| `middleware` | none | The middleware a request passes through, in order, before the handler; a middleware may answer itself. |
| `handler` | required | The module that answers the request. |

### Upstreams

| field | default | description |
| --- | --- | --- |
| `name` | required | A name unique among the upstreams, by which handlers refer to this one. |
| `addresses` | required | The upstream's servers, each a host and a port, as `10.0.0.1:8080` or `backend:80`. |
| `weights` | 1 each | Each address's weight, in the order of `addresses`: its share under `weighted_round_robin`, and the requests it takes to be as busy as an address of weight 1 under `least_conn`. |
| `load_balancing` | `round_robin`, or `weighted_round_robin` when `weights` are given | How requests are spread over the addresses; see the nine strategies below. |
| `hash_key` | none | The header `header_hash` and the cookie `cookie_hash` hash by. |
| `sticky_cookie` | none | Keeps each client on one address with a cookie: `name` `fit_sticky` by default, `path` `/`, `max_age`, `secure`. |
| `max_conns` | `256` | The most connections open, or being opened, to one address; a request past it waits for one to be free. |
| `max_pending` | `1024` | How many requests may wait for a connection once `max_conns` are busy; one past it is answered 503 at once. |
| `max_idle_conns` | `32` | The idle connections kept to one address; 0 keeps none. |
| `idle_timeout` | `60s` | How long an idle connection is kept before it is closed. |
| `connect_timeout` | `10s` | How long opening a connection may take; a request whose connect times out gets 504. |
| `response_header_timeout` | `60s` | How long the upstream may take, once the request is sent, to begin its response; past it a request gets 504. |
| `response_idle_timeout` | `60s` | How long the upstream may go silent in the middle of a response body. |
| `response_timeout` | `1h` | How long a whole response may take; past it before the response began a request gets 504, and past it later it is cut short. |
| `max_fails` | `1` | The failed requests to an address within `fail_timeout` that take it out of rotation for `fail_timeout` (passive health); 0 keeps every address in. |
| `fail_timeout` | `10s` | The window `max_fails` are counted in, and how long an address that reached them stays out. |
| `health_check` | none | Active health checks: probe each address every `interval` (`http` sends a GET, `tcp` passes once a connection opens), out after `fails` in a row, back after `passes`; `timeout` `2s`, `jitter` a tenth of `interval`. |
| `deny_internal` | off | Refuse to connect where an address, or the address its host name resolves to, is loopback, link-local (`169.254.169.254`, the metadata service of most clouds), unspecified, multicast or the host's own; a request refused so gets 502. |
| `protocol` | `http/1.1` | How requests reach the upstream: `http/1.1`, `h2c`, or `h2` (TLS, with `tls_server_name`, `tls_ca_file`, `tls_cert_file`, `tls_key_file`). |

### The nine load balancing strategies

- `round_robin`: in turn.
- `weighted_round_robin`: in turn by weight, smoothly, as nginx does.
- `least_conn`: to the address with the fewest requests under way for its weight.
- `random`: at random by weight.
- `p2c`: of two addresses chosen at random, the less busy for its weight.
- `ip_hash`: a consistent hash of the client's address, so the same client goes to the same address.
- `uri_hash`: a consistent hash of the request's path and query.
- `header_hash`: a consistent hash of the header `hash_key` names.
- `cookie_hash`: a consistent hash of the cookie `hash_key` names.

The last four keep the same key on the same address while it is up, and adding or removing an address
moves only the keys that went to it or will. A request without the header or cookie goes in turn.

## Handler and middleware modules

### Handlers

| Module | What it does |
| --- | --- |
| [`proxy`](../reference/config.md#http-handlers-proxy) | A reverse proxy to an upstream: splitting by weight, failover, mirroring, retries |
| [`static`](../reference/config.md#http-handlers-static) | Files from a directory: listings, precompressed variants, `try_files` |
| [`respond`](../reference/config.md#http-handlers-respond) | A fixed status, header fields and body |
| [`fastcgi`](../reference/config.md#http-handlers-fastcgi) | A FastCGI application, as PHP-FPM |
| [`media_pages`](../reference/config.md#http-handlers-media_pages) | fit's own media pages: the index, a room call, the publish and play pages |
| [`media_tokens`](../reference/config.md#http-handlers-media_tokens) | Issues time-limited media_auth tokens in batches for a business backend |
| [`hls`](../reference/config.md#http-handlers-hls) | Plays the streaming role's streams over HLS |
| [`httpflv`](../reference/config.md#http-handlers-httpflv) | Plays them as HTTP-FLV and WebSocket-FLV |
| [`whip`](../reference/config.md#http-handlers-whip) | Takes WebRTC streams published with WHIP into fit's media server |
| [`whep`](../reference/config.md#http-handlers-whep) | Plays fit's media server's streams with WHEP |
| [`stream_whip`](../reference/config.md#http-handlers-stream_whip) | Publishes with WHIP into the streaming role's hub |
| [`stream_whep`](../reference/config.md#http-handlers-stream_whep) | Plays the streaming role's streams with WHEP |
| [`webrtc_proxy`](../reference/config.md#http-handlers-webrtc_proxy) | Proxies WHIP and WHEP to other media servers, relaying peers by ICE ufrag |
| [`forward_proxy`](../reference/config.md#http-handlers-forward_proxy) | A forward proxy for HTTP CONNECT, edited on the [Forward proxy](16-forward-proxy.md) page |

The per-module docs are in `schema/doc/modules/`, as
[proxy.md](../../schema/doc/modules/proxy.md),
[static.md](../../schema/doc/modules/static.md),
[respond.md](../../schema/doc/modules/respond.md),
[fastcgi.md](../../schema/doc/modules/fastcgi.md),
[media_pages.md](../../schema/doc/modules/media_pages.md),
[media_tokens.md](../../schema/doc/modules/media_tokens.md),
[hls.md](../../schema/doc/modules/hls.md), [httpflv.md](../../schema/doc/modules/httpflv.md),
[whip.md](../../schema/doc/modules/whip.md), [whep.md](../../schema/doc/modules/whep.md),
[stream_whip.md](../../schema/doc/modules/stream_whip.md),
[stream_whep.md](../../schema/doc/modules/stream_whep.md),
[webrtc_proxy.md](../../schema/doc/modules/webrtc_proxy.md),
[forward_proxy.md](../../schema/doc/modules/forward_proxy.md). `vod` is in the
[media library](22-media-library.md) chapter.

### Middleware

| Module | What it does |
| --- | --- |
| [`basic_auth`](../reference/config.md#http-middleware-basic_auth) | A user and password, against bcrypt hashes |
| [`body_limit`](../reference/config.md#http-middleware-body_limit) | Bounds request bodies, answering 413 past it |
| [`cache`](../reference/config.md#http-middleware-cache) | An HTTP cache, in memory and on disk, shared by the routes of a `zone` |
| [`circuit_breaker`](../reference/config.md#http-middleware-circuit_breaker) | Answers 503 while too many requests fail or are slow, then lets a few through to try |
| [`client_cert`](../reference/config.md#http-middleware-client_cert) | Requires a client certificate and passes its subject on |
| [`compress`](../reference/config.md#http-middleware-compress) | zstd, brotli and gzip, the encoding the client prefers |
| [`concurrency_limit`](../reference/config.md#http-middleware-concurrency_limit) | Bounds the requests of a route under way at once |
| [`forward_auth`](../reference/config.md#http-middleware-forward_auth) | Asks an auth service, and passes fields of its answer on |
| [`headers`](../reference/config.md#http-middleware-headers) | Sets, adds and deletes request and response fields |
| [`jwt`](../reference/config.md#http-middleware-jwt) | Verifies JSON Web Tokens and passes claims on as fields |
| [`rate_limit`](../reference/config.md#http-middleware-rate_limit) | Limits the rate of requests, by client address, a header or the route, with a burst |
| [`redirect`](../reference/config.md#http-middleware-redirect) | Redirects, all requests or those whose path matches a regexp |
| [`rewrite`](../reference/config.md#http-middleware-rewrite) | Rewrites the path: strips or adds a prefix, or replaces what a regexp matches |
| [`wasm`](../reference/config.md#http-middleware-wasm) | Runs a WebAssembly plugin on each request |

The per-module docs are in `schema/doc/modules/`, as
[cache.md](../../schema/doc/modules/cache.md), [jwt.md](../../schema/doc/modules/jwt.md),
[rate_limit.md](../../schema/doc/modules/rate_limit.md),
[forward_auth.md](../../schema/doc/modules/forward_auth.md),
[wasm.md](../../schema/doc/modules/wasm.md).

## In .conf

```fit
listener web :8080 http/1.1 h2c

upstream app {
	addresses 10.0.0.1:8080 10.0.0.2:8080
	weights 3 1
	load_balancing weighted_round_robin
	sticky_cookie {
		name fit_sticky
		max_age 24h
		secure true
	}
	max_conns 256
	max_pending 1024
	max_idle_conns 32
	idle_timeout 60s
	connect_timeout 3s
	response_header_timeout 10s
	max_fails 3
	fail_timeout 30s
	deny_internal true
	health_check {
		type http
		path /healthz
		interval 5s
		timeout 2s
		fails 2
		passes 2
	}
}

route {
	name api
	match {
		host api.example.com
		path /api/*
		method POST PUT DELETE
	}
	middleware rate_limit {
		rate 10
		burst 20
	}
	middleware body_limit {
		max_bytes 10485760
	}
	handler proxy {
		upstream app
		retry {
			attempts 2
			statuses 502 503 504
		}
	}
}

route {
	name admin
	match {
		host api.example.com
		path /admin/*
	}
	middleware basic_auth {
		realm admin
		users {
			username ops
			password_hash $2a$14$Zkx19XLiW6VYouLHR5NmfOFU0z2GTNmpkT/5qqR7hx4IjWJPDhjvG
		}
	}
	handler proxy { upstream app }
}

route {
	name health
	match {
		host api.example.com
		path /health
	}
	handler respond {
		body "ok\n"
	}
}
```

This config passes `./bin/fit validate -c`. An `upstream` without a listener is refused, so the
example adds a minimal `listener`; a `static` handler's `root` must exist on disk for `fit validate`
to read it, so the example has no static route.

## See also

- [The config reference: Routes](../reference/config.md#routes),
  [Upstreams](../reference/config.md#upstreams), [Handlers](../reference/config.md#handlers),
  [Middleware](../reference/config.md#middleware), and the
  [chapter guide in Chinese](../reference/config.zh-CN.md#各章内容)
- [Forward proxy](16-forward-proxy.md), [L4 proxy](17-l4-proxy.md), [Cache](19-cache.md),
  [Streaming](20-streaming.md), [Media auth](21-media-auth.md), [Media library](22-media-library.md),
  [WebRTC](23-webrtc.md)
- The commented templates with every field:
  [templates/conf/conf.d/60-apps.conf](../../templates/conf/conf.d/60-apps.conf) and
  [sites/](../../templates/conf/conf.d/sites/)
