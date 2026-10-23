# Getting started

[English](getting-started.md) | [简体中文](getting-started.zh-CN.md)

This guide takes fit from nothing to a site served over HTTPS, with files, a proxied app and a
config changed while it runs. Each step adds to a config file; the
[config reference](reference/config.md) has every field.

## Install

Build it with Go 1.27.3 or later:

```bash
go install github.com/lesismal/fit/cmd/fit@latest
```

Or from a checkout, with `make build`, which puts it in `bin/fit`. A tagged release is also a
container image, `ghcr.io/lesismal/fit`, for linux/amd64 and arm64; [deploy](../deploy/README.md)
has it, the Helm chart and systemd units. `fit build` makes a fit with modules of your own
([modules](modules.md)).

```bash
fit version
```

## A first config

A config may be a Caddyfile, YAML or JSON ([Configuration](configuration.md) compares them); this
guide uses the Caddyfile, the shortest of the three, which fit converts to its native JSON as it
reads it. Save this as `Caddyfile`:

```caddyfile
:8080 {
	respond /hello "hello from fit"
}
```

A site's address, `:8080`, says where fit listens: HTTP on port 8080, for any host. The
directives in its block say what answers the requests: `respond` answers those its matcher,
`/hello`, takes, with a fixed body. Check the config, then run it:

```bash
fit validate -c Caddyfile
fit run -c Caddyfile
```

```bash
curl localhost:8080/hello
```

fit checks a config strictly: a directive it does not convert is listed with its line, and the
config it converts to is checked as any other, so that a wrong value is an error and fit does not
start. `fit adapt -c Caddyfile` prints the native config it becomes. A request no directive takes
is answered 200 with no body, as Caddy answers.

## Serve files

Add a directory to serve, under the first directive:

```caddyfile
:8080 {
	respond /hello "hello from fit"
	root * ./public
	file_server
}
```

`root` says where the files are, for every request (`*`), and `file_server` serves them. A path
ending in `*` matches a prefix, and a directive with no matcher takes every request; when several
take a request, the most specific wins: `/hello` before the files, whatever their order.
`file_server` serves `index.html` for a directory; `file_server browse` lists one that has none,
and `try_files {path} /index.html` serves a single-page app.

## Proxy to an app

`reverse_proxy` sends requests to a set of backends, an upstream:

```caddyfile
:8080 {
	reverse_proxy /api/* 127.0.0.1:9000 127.0.0.1:9001 {
		health_uri /healthz
		health_interval 5s
	}
	root * ./public
	file_server
}
```

Requests to `/api/` go to the two backends in turn, and a backend that fails its health check is
left out until it passes again. WebSockets and gRPC go through the same way. Every connection,
queue and timeout has a safe default, which `reverse_proxy`'s subdirectives change.

## HTTPS

Give the site a host name for its address:

```caddyfile
example.com {
	root * ./public
	file_server
}
```

fit then listens on 443 for HTTPS, with a certificate for `example.com` from Let's Encrypt, which
it renews, and on 80 only to redirect to HTTPS. The name's DNS must point at the host, and ports 80
and 443 reach it. The global options `email` and `acme_ca` set the ACME account and CA,
`auto_https` turns it off. To use certificates of your own, give them with `tls`:

```caddyfile
example.com {
	tls example.com.pem example.com.key
	root * ./public
	file_server
}
```

fit loads the files again when they change.

## Middleware

Middleware runs before the handler. This compresses responses, and adds a field to each:

```caddyfile
example.com {
	encode
	header Strict-Transport-Security "max-age=31536000"
	root * ./public
	file_server
}
```

The [Caddyfile reference](reference/caddyfile.md#directives) lists the directives that convert,
and the [config reference](reference/config.md#middleware) every middleware fit has: rewrites and
redirects, rate and concurrency limits, Basic auth, JWTs, forward auth, client certificates, a
circuit breaker, a cache, WASM plugins. Those with no Caddyfile directive are written in YAML or
JSON, starting from `fit adapt`'s output.

## Change it while it runs

`fit run` applies the config file again on SIGHUP, or each time it changes with `-watch`. A config
that does not load leaves the running one in place, and says why. Listeners that did not change
keep their connections, and requests under way finish on the config they started with.

A Caddyfile turns the Admin API on, on 127.0.0.1:2019, unless its global options say `admin off`.
It reads and changes the running config, in fit's native JSON: here, the whole of it, then the
first route's params, by their JSON pointer, then a whole new config, converted from the Caddyfile
by `fit adapt`, and back to the first:

```bash
curl localhost:2019/config
curl -X PATCH -H 'Content-Type: application/merge-patch+json' \
  -d '{"body": "hello again"}' localhost:2019/config/routes/0/handler/params
fit adapt -c Caddyfile -o fit.json
curl -X POST -H 'Content-Type: application/json' -d @fit.json localhost:2019/load
curl -X POST localhost:2019/rollback/1
```

It keeps the configs it applied, to read again and roll back to, and serves metrics at `/metrics`
and a dashboard at `/dashboard/`.

## Next

- [Configuration](configuration.md): the same config as a [Caddyfile](reference/caddyfile.md), in
  [YAML](reference/yaml.md) or in [JSON](reference/json.md).
- The [config reference](reference/config.md): every field, its type and default, with examples.
- Coming from another server: [nginx](migrating-from-nginx.md), [Caddy](migrating-from-caddy.md),
  [Traefik](migrating-from-traefik.md).
- [Compatibility](compatibility.md): what stays stable across 1.x releases.
- [Deploy](../deploy/README.md): the container image, Kubernetes and systemd.
