# Migrating from Caddy

[English](migrating-from-caddy.md) | [简体中文](migrating-from-caddy.zh-CN.md)

fit shares Caddy's ideas: HTTPS by default, a strict structured config, and an Admin API that swaps
configs atomically. It differs in what is built in (caching, rate limiting, layer 4 proxying, which
Caddy leaves to plugins) and in costing less per connection. There is no converter from the
Caddyfile or Caddy's JSON; this guide maps one to the other, and the
[config reference](reference/config.md) has every field.

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
| a site address, `example.com` | a route's `match.host`; listeners made by HTTPS by default, or written |
| `:8080` | a listener: `{"name": "web", "address": ":8080", "protocols": ["http/1.1", "h2c"]}` |
| matchers: `path`, `host`, `method`, `header`, `query`, `remote_ip`, `path_regexp`, `expression` | `match`'s fields of the same names, and `expr` |
| `handle`, `route` and their order | routes, picked by `priority`, then the most specific host and path |
| `handle_path /api/*` | `match.path` and the `rewrite` middleware's `strip_prefix` |
| `reverse_proxy` | the `proxy` handler and an upstream |
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
| `log` | `access_log` |
| `metrics` | `metrics`, served by the Admin API at `/metrics` |
| `tracing` | `telemetry`, over OTLP |
| plugins: rate limiting, caching, layer 4 | built in: `rate_limit`, `cache`, `tcp` and `udp` listeners with services |
| `xcaddy build --with ...` | `fit build --with ...` ([modules](modules.md)) |
| `caddy run --watch`, `caddy reload` | `fit run -watch`, SIGHUP or the Admin API's `POST /load` |
| `caddy validate`, `caddy hash-password` | `fit validate`, `fit hash-password` |

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
