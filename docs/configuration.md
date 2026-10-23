# Configuration

[English](configuration.md) | [简体中文](configuration.zh-CN.md)

fit reads one config file, in any of three formats: a **Caddyfile**, **YAML** or **JSON**. Each
says the same things: the addresses fit listens on, the routes that pick what answers each
request, the upstreams requests are proxied to, and settings for logs, limits, certificates and
the Admin API. JSON is fit's native format; YAML has exactly its fields, and a Caddyfile is
converted to them.

| Format | Best for | Reference |
| --- | --- | --- |
| Caddyfile | Short, readable configs of sites; configs brought from Caddy | [Caddyfile reference](reference/caddyfile.md) |
| YAML | Every field of fit, with comments, and anchors for what repeats | [YAML reference](reference/yaml.md) |
| JSON | Every field of fit; what the Admin API takes and returns, and what tools write | [JSON reference](reference/json.md) |

Every field, its type, default and meaning, with examples in all three formats, is in the
[config reference](reference/config.md).

## The formats

### Caddyfile

Caddy's own format: a block for each site, named by its address, with directives inside. fit
converts a common subset of Caddy's directives to its config, and lists, with their lines, what it
leaves out. A file named `Caddyfile`, or ending in `.caddyfile`, is read as one.

```caddyfile
# Served over HTTPS, with a certificate obtained for example.com.
example.com {
	encode zstd gzip
	reverse_proxy /api/* 127.0.0.1:9000
	root * /srv/www
	file_server
}
```

What converts, directive by directive: the [Caddyfile reference](reference/caddyfile.md).

### YAML

fit's config with YAML's syntax: comments, no quotes or braces for most values, and anchors and
aliases for what repeats. Files ending in `.yaml` or `.yml`.

```yaml
# The same site.
upstreams:
  - name: api
    addresses: ["127.0.0.1:9000"]
routes:
  - match: {host: [example.com], path: ["/api/*"]}
    middleware: [{module: compress}]
    handler: {module: proxy, params: {upstream: api}}
  - match: {host: [example.com]}
    middleware: [{module: compress}]
    handler: {module: static, params: {root: /srv/www}}
```

How YAML maps onto the config, and its pitfalls: the [YAML reference](reference/yaml.md).

### JSON

fit's native format, strictly read: an unknown field, a value of the wrong type or a key written
twice is an error, given with its line and column. `//` and `/* */` comments and a comma after the
last element are allowed. Any other file, `fit.json` by default.

```json
{
  // The same site.
  "upstreams": [{"name": "api", "addresses": ["127.0.0.1:9000"]}],
  "routes": [
    {"match": {"host": ["example.com"], "path": ["/api/*"]},
     "middleware": [{"module": "compress"}],
     "handler": {"module": "proxy", "params": {"upstream": "api"}}},
    {"match": {"host": ["example.com"]},
     "middleware": [{"module": "compress"}],
     "handler": {"module": "static", "params": {"root": "/srv/www"}}},
  ],
}
```

More: the [JSON reference](reference/json.md).

## What a config holds

| Part | What it is | Reference |
| --- | --- | --- |
| `listeners` | Addresses and protocols; made by HTTPS by default when left out | [Listeners](reference/config.md#listeners) |
| `routes` | A match, middleware and a handler for each kind of request | [Routes](reference/config.md#routes) |
| `upstreams` | Servers to proxy to, balanced and health checked | [Upstreams](reference/config.md#upstreams) |
| `services` | TCP and UDP proxying | [Services](reference/config.md#services) |
| handlers | `proxy`, `static`, `respond`, `fastcgi`, `forward_proxy` | [Handlers](reference/config.md#handlers) |
| middleware | authentication, rate limits, caching, compression, headers, rewrites and more | [Middleware](reference/config.md#middleware) |
| `access_log`, `telemetry`, `metrics` | Logs, traces and metrics | [Access log](reference/config.md#access-log), [Telemetry](reference/config.md#telemetry), [Metrics](reference/config.md#metrics) |
| `limits` | Bounds on connections, headers, bodies and memory | [Limits](reference/config.md#limits) |
| `auto_https`, `acme` | HTTPS by default and certificates | [HTTPS by default](reference/config.md#https-by-default), [ACME](reference/config.md#acme) |
| `admin` | The Admin API | [Admin API](reference/config.md#admin-api) |
| `providers` | Docker, Consul, etcd and Kubernetes | [Providers](reference/config.md#providers) |

## Running, checking and converting

The format is told from the file's name; the commands are the same for all three:

```bash
fit run -c Caddyfile
```

```bash
fit validate -c fit.yaml
```

`fit validate` loads the config as `fit run` would and checks every module's params, printing each
error with its file, line and column, in the file as written, a Caddyfile's or YAML's included.
`fit adapt` converts:

| From | To | Command |
| --- | --- | --- |
| Caddyfile | JSON | `fit adapt -c Caddyfile -o fit.json` |
| nginx | JSON | `fit adapt -c nginx.conf -o fit.json` |
| YAML | JSON | `fit adapt -c fit.yaml -o fit.json` |
| JSON (or any of the above) | YAML | `fit adapt -c fit.json -to yaml -o fit.yaml` |

Converting a Caddyfile or an nginx config lists on standard error what was left out. `fit schema`
prints the config's JSON Schema, for editors to complete and check JSON and YAML as they are
written.

## Changing a running config

`fit run` applies its config file again on SIGHUP, or each time the file changes with `-watch`,
whatever its format. A config that fails to load or compile is refused, and the running one stays.
The [Admin API](reference/config.md#admin-api) reads and replaces the running config as JSON, by
whole config or by JSON pointer, with versions to roll back to.

## Coming from another server

[nginx](migrating-from-nginx.md) and [Caddy](migrating-from-caddy.md) configs convert with
`fit adapt`; [Traefik](migrating-from-traefik.md)'s map field by field. Each guide shows the same
setup in both servers.
