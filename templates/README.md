# Config templates

[English](README.md) | [简体中文](README.zh-CN.md)

A complete config template in each of fit's three formats, to copy and edit. Each holds every
field of the config and of every module's params, each with its description, type, choices and
default in a comment above it:

| Template | Format |
| --- | --- |
| [conf/fit.conf](conf/fit.conf) | fit's own [.conf format](../docs/reference/fit.md) |
| [yaml/fit.yaml](yaml/fit.yaml) | YAML |
| [json/fit.json](json/fit.json) | JSON with comments and trailing commas, which fit reads |

In each, a small config is written out and the rest is commented out, with its default or an
example value, to be uncommented as needed. Each runs as it is (`fit run -c templates/conf/fit.conf`):
a listener on :8080, an upstream and a route answering "hello from fit". In a commented-out
block, a field's comment is a comment within the comment (`# #` or `// //`), so that uncommenting
the block leaves it a comment. The routes show every handler, one route each, and every
middleware module, with all their params.

The templates are generated from the code, as the [config reference](../docs/reference/config.md)
is, and a test fails when they fall behind: regenerate them with `go test ./schema -update`, or
print them for the modules a binary has with `fit schema -template fit`, `yaml` or `json`. The
formats are compared in [Configuration](../docs/configuration.md).

## As a directory

The same template is also a directory of files by kind, for a config kept as one, as
`fit run -c <directory>` reads it ([several files and directories](../docs/reference/fit.md#several-files-and-directories)):
[conf/conf.d](conf/conf.d), [yaml/conf.d](yaml/conf.d) and [json/conf.d](json/conf.d).

| File | What it holds |
| --- | --- |
| `00-runtime` | How fit runs on the host: `limits` (the bytes waiting for slow clients, connections, header and body sizes, the write stall timeout) and `engine` (fib's event loops, workers, task queue, buffers, the backpressure of one connection, backlog) |
| `10-listeners` | The listeners every site shares: addresses, protocols, TLS, client certificates, the PROXY protocol |
| `20-tls` | `auto_https` and `acme` |
| `30-observability` | `access_log`, `telemetry`, `metrics` and `admin` |
| `40-services` | Layer 4 `services` |
| `50-providers` | `providers` |
| `sites/<host>` | One file a site: its routes, and the upstreams it proxies to. `sites/api.example.com` has every field of an upstream; `sites/example.com` every handler and middleware |

Each runs as it is: a listener on :8080, example.com answering "hello from fit", and
api.example.com proxied to 127.0.0.1:9000. listeners, routes, upstreams, services and providers
may be in several files and are joined; any other field is in one file only. Write one with
`fit schema -template fit -dir /etc/fit/conf.d` (or `yaml`, `json`), which refuses to replace a
file already there.
