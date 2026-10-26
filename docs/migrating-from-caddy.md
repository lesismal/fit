# Migrating from Caddy

[English](migrating-from-caddy.md) | [简体中文](migrating-from-caddy.zh-CN.md)

fit shares Caddy's ideas: HTTPS by default, a strict structured config, and an Admin API that swaps
configs atomically. It differs in what is built in (caching, rate limiting, layer 4 proxying, which
Caddy leaves to plugins) and in costing less per connection. fit's own config format, the
[.konf file](reference/fit.md), is written in blocks of directives as a Caddyfile is, with a `site`
for each set of host names, but its directives are fit's own fields. `fit adapt` converts a
Caddyfile's common subset to it, to start from; there is no converter from Caddy's JSON. This guide
maps one to the other, and the [config reference](reference/config.md) has every field.

## A site

```caddyfile
example.com {
	encode zstd gzip
	handle_path /api/* {
		reverse_proxy 127.0.0.1:9000 127.0.0.1:9001 {
			lb_policy least_conn
			health_uri /healthz
		}
	}
	root * /srv/www
	file_server
}
```

is, in fit:

```fit
upstream app {
	addresses 127.0.0.1:9000 127.0.0.1:9001
	load_balancing least_conn
	health_check {
		path /healthz
	}
}

site example.com {
	middleware compress
	route /api/* {
		middleware rewrite { strip_prefix /api }
		handler proxy { upstream app }
	}
	route /* {
		handler static { root /srv/www }
	}
}
```

or, in fit's JSON:

```json
{
  "upstreams": [
    {"name": "app", "addresses": ["127.0.0.1:9000", "127.0.0.1:9001"], "load_balancing": "least_conn",
     "health_check": {"path": "/healthz"}}
  ],
  "routes": [
    {"match": {"host": ["example.com"], "path": ["/api/*"]},
     "middleware": [{"module": "compress"}, {"module": "rewrite", "params": {"strip_prefix": ["/api"]}}],
     "handler": {"module": "proxy", "params": {"upstream": "app"}}},
    {"match": {"host": ["example.com"], "path": ["/*"]},
     "middleware": [{"module": "compress"}],
     "handler": {"module": "static", "params": {"root": "/srv/www"}}}
  ]
}
```

As with Caddy, a config that writes no listeners is served over HTTPS on 443, with certificates from
Let's Encrypt for the routes' host names, and port 80 redirects to it.

## How the parts map

| Caddy | fit |
| --- | --- |
| a site address, `example.com` | `site example.com { ... }`, whose routes match the host; listeners made by HTTPS by default, or written |
| `:8080` | a listener: `listener web { address :8080; protocols http/1.1 h2c }` |
| matchers: `path`, `host`, `method`, `header`, `query`, `remote_ip`, `path_regexp`, `expression` | a route's `match { ... }`, with fields of the same names, and `expr` |
| `handle`, `route` and their order | routes, picked by `priority`, then the most specific host and path |
| `handle_path /api/*` | `route /api/* { middleware rewrite { strip_prefix /api } ... }` |
| `reverse_proxy` | `handler proxy { upstream app }` and an `upstream app { ... }` |
| `lb_policy` | the upstream's `load_balancing`: `round_robin`, `weighted_round_robin`, `least_conn`, `random`, `p2c`, `ip_hash`, `uri_hash`, `header_hash`, `cookie_hash` |
| `health_uri`, `health_interval`, `fail_duration`, `max_fails` | `health_check`, `fail_timeout`, `max_fails` |
| `transport http { versions h2c }` | the upstream's `protocol`: `h2c`, or `h2` over TLS |
| `file_server`, `root`, `browse`, `try_files`, `precompressed` | the `static` handler's `root`, `browse`, `try_files`, `precompressed` |
| `encode` | the `compress` middleware |
| `header`, `request_header` | the `headers` middleware |
| `redir` | the `redirect` middleware |
| `rewrite`, `uri strip_prefix` | the `rewrite` middleware |
| `respond` | the `respond` handler |
| `basicauth` | the `basic_auth` middleware, with bcrypt hashes as Caddy's are |
| `forward_auth` | the `forward_auth` middleware |
| `php_fastcgi` | the `fastcgi` handler, with `split_path`, `index` and `try_files` |
| `tls cert.pem key.pem` | a listener's `tls.certificates` |
| `tls { on_demand }` and `on_demand_tls { ask ... }` | a listener's `tls.on_demand` and `acme.on_demand` |
| `email`, `acme_ca`, `storage` (Redis, Consul) | `acme`'s `email`, `directory`, `storage`, and `shared_storage` for Redis, etcd or Consul |
| `log` in a site | `access_log`, with its `output` and `level` |
| the global `log` and `debug` | `log`: fit's own log, with `output`, `format` (`console` is `text`) and `level`; a `log` that includes only `http.log.access` is the access log |
| `metrics` | `metrics`, served by the Admin API at `/metrics` |
| `tracing` | `telemetry`, over OTLP |
| plugins: rate limiting, caching, layer 4 | built in: `rate_limit`, `cache`, `tcp` and `udp` listeners with services |
| `xcaddy build --with ...` | `fit build --with ...` ([modules](modules.md)) |
| `caddy run --watch`, `caddy reload` | `fit run -watch`, SIGHUP or the Admin API's `POST /load` |
| `caddy validate`, `caddy hash-password` | `fit validate`, `fit hash-password` |

## Converting a Caddyfile

```bash
fit adapt -from caddyfile -c Caddyfile -to fit -o fit.konf
```

writes a .konf file and lists on standard error, with their lines, the directives it left out or
converted with doubts; the [Caddyfile conversion](reference/caddyfile.md) page has every
directive. What converts:

- **Global options**: `email`, `acme_ca`, `acme_ca_root`, `storage file_system`, `admin`,
  `auto_https`, `http_port`, `https_port`, `on_demand_tls`, `servers { protocols }`, `log` and
  `debug`.
- **Snippets** and `import` of them, with `{args[N]}`; `{$ENV}` and `{$ENV:default}`; `#`
  comments, quoted, backquoted and heredoc strings. `import` of files is not converted.
- **Sites** by their addresses: names, `*.` wildcards, `http://`, `https://`, ports, a path, and
  `bind`. Sites served only on the HTTPS port write no listeners, and fit's HTTPS by default serves
  them, as Caddy does; otherwise listeners are written, with HTTP redirected to HTTPS for the
  names served over it. `localhost` and IP addresses are served over HTTP: fit has no internal CA.
- **Matchers**: `*`, paths (with `*` anywhere), and named matchers of `path`, `path_regexp`,
  `host`, `method`, `header`, `query`, `remote_ip`, `client_ip` and `not`. CEL expressions are
  not converted.
- **Directives**: `handle`, `handle_path`, `route`, `reverse_proxy` (its upstreams, `lb_policy`,
  health checks, `header_up`, `header_down`, `transport http`), `file_server`, `root`, `try_files`,
  `respond`, `redir`, `rewrite`, `uri`, `header`, `request_header`, `encode`, `basic_auth`
  (`basicauth`), `forward_auth`, `request_body`, `php_fastcgi`, `tls` and `log`.

A Caddyfile picks a request's handlers by its directives' order and their matchers; fit picks one
route. The converter makes a route for each handler, and puts each middleware directive on the
routes it covers, splitting a route it covers only in part: `basic_auth /admin/*` over a whole
site's `file_server` becomes a route for `/admin/*`, with the authentication, beside the site's.
What differs:

- `rewrite` and `uri` rewrite the path after fit has picked the route, so a rewritten path does not
  choose another one.
- `php_fastcgi` beside `file_server` sends scripts and directories to PHP and the rest to the
  files; a path that is neither is not sent to `index.php`.
- `templates`, `map`, `vars`, `handle_errors`, `abort` and placeholders that fit has no counterpart
  for are left out, each listed.

## The Admin API

fit's Admin API is Caddy's in shape: `GET`, `PUT`, `PATCH` and `DELETE` on `/config/` and a path
into it, `POST /load`, and ETags with `If-Match` against lost updates. The differences:

- A path is a JSON pointer's tokens (`/config/routes/0/handler`); there are no `@id` shortcuts.
- `PATCH` is a JSON merge patch (RFC 7396); Caddy's `POST` to append to an array is not there:
  `PUT` the array.
- fit keeps the configs it applied (`GET /versions`), and `POST /rollback/{n}` applies one again.
- It listens on 127.0.0.1:2019 too, and takes a bearer token (`admin.token_file`) to listen
  elsewhere.

## What works differently

- **The config's shape.** fit's config has listeners, routes, upstreams and services at its top,
  rather than apps and servers; a route is a flat list of middleware and one handler, not nested
  handlers.
- **Not there**: Caddy's internal CA (`tls internal`) and its PKI app, DNS challenges (fit
  answers HTTP-01 and TLS-ALPN-01), the `templates` and `map` directives, and Caddy's events. A
  module built in with `fit build`, or a WASM plugin, takes the place of a plugin.
