# Configuration

[English](configuration.md) | [简体中文](configuration.zh-CN.md)

fit reads its config from one file or several, in any of three formats: a **.conf** file, **YAML**
or **JSON**. Each says the same things, field for field: the addresses fit listens on, the routes
that pick what answers each request, the upstreams requests are proxied to, settings for logs,
limits, certificates and the Admin API, and the roles fit runs beside the gateway: the LLM feature's, the
tunnel's and WebRTC's. JSON is what the Admin API takes and returns; the three
formats have exactly the same fields, and any of them can be converted to the others.

| Format | Best for | Reference |
| --- | --- | --- |
| .conf | Configs written by hand: blocks of directives, as nginx and Caddy write theirs, with sites | [.conf reference](reference/fit.md) |
| YAML | Configs written by hand or by tools, with anchors for what repeats | [YAML reference](reference/yaml.md) |
| JSON | What the Admin API takes and returns, and what tools write | [JSON reference](reference/json.md) |

Every field, its type, default and meaning, with examples in all three formats, is in the
[config reference](reference/config.md). [Templates](../templates/README.md) holds a complete
config in each format, every field in it, ready to copy and edit.

## The formats

### .conf

fit's own format: a directive for each field, a block for each object, and `site` to write the
routes of a set of host names together. Files ending in `.conf`.

```fit
upstream api {
	addresses 127.0.0.1:9000
}

# Served over HTTPS, with a certificate obtained for example.com.
site example.com {
	middleware compress
	route /api/* {
		handler proxy { upstream api }
	}
	route {
		handler static { root /srv/www }
	}
}
```

The syntax, how each type of field is written, sites, and several files: the
[.conf reference](reference/fit.md).

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
last element are allowed. Files ending in `.json`, and any other file named on its own, `fit.json`
by default.

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
| `listeners` | Addresses and protocols; made by HTTPS by default when left out; each may opt into Multipath TCP where the kernel offers it, off by default | [Listeners](reference/config.md#listeners) |
| `routes` | A match, middleware and a handler for each kind of request | [Routes](reference/config.md#routes) |
| `upstreams` | Servers to proxy to, balanced and health checked | [Upstreams](reference/config.md#upstreams) |
| `services` | TCP and UDP proxying | [Services](reference/config.md#services) |
| handlers | `proxy`, `static`, `respond`, `fastcgi`, `forward_proxy`; `whip`, `whep` and `webrtc_proxy` for WebRTC; `httpflv`, `hls`, `stream_whip` and `stream_whep` for streaming | [Handlers](reference/config.md#handlers) |
| middleware | authentication, rate limits, caching, compression, headers, rewrites and more | [Middleware](reference/config.md#middleware) |
| `log`, `access_log`, `telemetry`, `metrics` | Logs, traces and metrics | [Log](reference/config.md#log), [Access log](reference/config.md#access-log), [Telemetry](reference/config.md#telemetry), [Metrics](reference/config.md#metrics) |
| `limits` | Bounds on connections, headers, bodies and memory | [Limits](reference/config.md#limits) |
| `auto_https`, `acme` | HTTPS by default and certificates | [HTTPS by default](reference/config.md#https-by-default), [ACME](reference/config.md#acme) |
| `engine` | fib's event loops, workers and buffers | [Engine](reference/config.md#engine) |
| `admin` | The Admin API; the config kept encrypted in a store (`store`); the console (`console`) | [Admin API](reference/config.md#admin-api), [the console](console.md) |
| `llm` | The LLM feature's hub, node and proxy, which share AI models among machines and serve them to agents | [LLM](reference/config.md#llm), [the LLM feature](llm.md) |
| `tunnel` | The tunnel's hub, node and proxy, which reach services behind NAT | [Tunnel](reference/config.md#tunnel), [the tunnel](tunnel.md) |
| `webrtc` | A TURN server (`turn`), the media port of `whip` and `whep` (`media`), and the relay of `webrtc_proxy` (`proxy`) | [WebRTC](reference/config.md#webrtc), [WebRTC](webrtc.md) |
| `streaming` | The streaming role: a media hub with RTMP, RTSP and SRT listeners and the WebRTC port of `stream_whip` and `stream_whep` | [Streaming](reference/config.md#streaming), [streaming](streaming.md) |
| `providers` | Docker, Consul, etcd and Kubernetes | [Providers](reference/config.md#providers) |
| `schema_version` | The version of the config, 1; optional | [Compatibility](compatibility.md#the-schemas-version) |

## Several files and directories

A config may be split across files, in any of the formats, and directories of them, each given
with `-c`:

```bash
fit run -c /etc/fit/base.conf -c /etc/fit/sites
```

A directory stands for its `.conf`, `.yaml`, `.yml` and `.json` files, in its subdirectories too,
in lexical order, those whose names begin with a `.` left out. The lists at the top of the config,
`listeners`, `routes`, `upstreams`, `services` and `providers`, are joined in the files' order;
any other field is given in one file only. An error names its file. The details:
[several files and directories](reference/fit.md#several-files-and-directories). A `.conf`
file may also name files itself: `import` merges whole config files, as `-c` does, and
`include` reads a file's directives in its place, in any block
([import and include](reference/fit.md#import-and-include)). A whole
template as a directory, with a file for each kind (the runtime, listeners, certificates,
observability, layer 4 services, providers) and one for each site, is in
[the config templates](../templates/README.md#as-a-directory); `fit schema -template fit -dir <dir>`
writes one.

## Running, checking and converting

The format is told from each file's name; the commands are the same for all three:

```bash
fit run -c fit.conf
```

```bash
fit validate -c fit.yaml -c conf.d
```

`fit validate` loads the config as `fit run` would and checks every module's params, printing each
error with its file, line and column, in the file as written, whatever its format. `fit adapt`
converts between the formats, merging several files into one:

| From | To | Command |
| --- | --- | --- |
| JSON or YAML | .conf | `fit adapt -c fit.json -to fit -o fit.conf` |
| .conf or YAML | JSON | `fit adapt -c fit.conf -o fit.json` |
| .conf or JSON | YAML | `fit adapt -c fit.conf -to yaml -o fit.yaml` |
| a directory | one file | `fit adapt -c conf.d -to fit -o fit.conf` |

`fit schema` prints the config's JSON Schema, for editors to complete and check JSON and YAML as
they are written; `fit schema -template fit` prints the .conf template. `fit hash-password` asks for a
password twice, masked, from a terminal (or reads it from standard input) and prints its bcrypt hash, for `basic_auth`'s users, the
console's and `media_auth`'s. `fit media-token -c fit.conf -policy live -publish 'live/*' -play '*' -ttl 1h -subject obs`
prints a token signed with a [`media_auth`](reference/config.md#media-auth) policy's `secret`, for a WHIP or WHEP client to send as its
bearer token; a running fit with the same secret takes it until it expires.

## Changing a running config

`fit run` applies its config files again on SIGHUP, or with `-watch` each time one changes, or a
directory's files are added or taken out, whatever their format. A config that fails to load or
compile is refused, and the running one stays. The [Admin API](reference/config.md#admin-api)
reads and replaces the running config as JSON, by whole config or by JSON pointer, with versions
to roll back to.

With `admin.store`, the config is kept encrypted in a file of its own, the store, and fit runs from
it: the files fit is started with give `admin`, and the rest of what they hold is only the store's
first content, used when the store does not exist yet. Every config applied, through the Admin API,
the console or a role's pages, is written to the store; SIGHUP reads the store again, and `-watch`
is refused. `fit store export -c fit.conf` prints what the store holds, as JSON, and
`fit store import -c fit.conf new.json` checks a config and seals it as the store, replacing it.
`admin.console` serves web pages behind logins from which every part of the config but `admin` is
changed. [The console](console.md) has the details.

## Coming from another server

`fit adapt` converts an nginx config or a Caddyfile to fit's config, listing what it leaves out,
to start from: `fit adapt -from nginx -c nginx.conf -to fit -o fit.conf`. The guides for
[nginx](migrating-from-nginx.md), [Caddy](migrating-from-caddy.md) and
[Traefik](migrating-from-traefik.md) show the same setup in each server and in fit, and how their
parts map onto fit's.
