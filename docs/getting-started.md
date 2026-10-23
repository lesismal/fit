# Getting started

[English](getting-started.md) | [简体中文](getting-started.zh-CN.md)

This guide takes fit from nothing to a site served over HTTPS, with files, a proxied app and a
config changed while it runs. Each step adds to a config file; the
[config reference](reference/config.md) has every field.

## Install

Build it with Go 1.27 or later:

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

fit's config is JSON, or YAML with the same fields. Save this as `fit.json`:

```json
{
  "listeners": [{"name": "web", "address": ":8080", "protocols": ["http/1.1", "h2c"]}],
  "routes": [
    {"match": {"path": ["/hello"]}, "handler": {"module": "respond", "params": {"body": "hello from fit\n"}}}
  ]
}
```

A listener says where fit listens, and what it speaks there. A route says which requests it takes
(`match`), what they go through (`middleware`) and what answers them (`handler`), each a module
with its `params`. Check the config, then run it:

```bash
fit validate -c fit.json
fit run -c fit.json
```

```bash
curl localhost:8080/hello
```

fit checks a config strictly: an unknown field, a value of the wrong type or a missing one is an
error, with its line and column, and fit does not start. A request no route matches gets 404.

## Serve files

Add a route that serves a directory, after the first:

```json
{"match": {"path": ["/*"]}, "handler": {"module": "static", "params": {"root": "./public"}}}
```

A path ending in `*` matches a prefix. When several routes match, the most specific wins: `/hello`
before `/*`, whatever their order. `static` serves `index.html` for a directory; `browse` lists one
that has none, and `try_files` serves a single-page app.

## Proxy to an app

An upstream is a set of backends, by name; the `proxy` handler sends requests to one:

```json
{
  "listeners": [{"name": "web", "address": ":8080", "protocols": ["http/1.1", "h2c"]}],
  "upstreams": [
    {"name": "app", "addresses": ["127.0.0.1:9000", "127.0.0.1:9001"],
     "health_check": {"path": "/healthz", "interval": "5s"}}
  ],
  "routes": [
    {"match": {"path": ["/api/*"]}, "handler": {"module": "proxy", "params": {"upstream": "app"}}},
    {"match": {"path": ["/*"]}, "handler": {"module": "static", "params": {"root": "./public"}}}
  ]
}
```

Requests to `/api/` go to the two backends in turn, and a backend that fails its health check is
left out until it passes again. WebSockets and gRPC go through the same way. Every connection,
queue and timeout has a safe default, which the upstream's fields change.

## HTTPS

Give the routes host names, and leave the listeners out:

```json
{
  "routes": [
    {"match": {"host": ["example.com"], "path": ["/*"]},
     "handler": {"module": "static", "params": {"root": "./public"}}}
  ]
}
```

fit then listens on 443 for HTTPS, with a certificate for `example.com` from Let's Encrypt, which
it renews, and on 80 only to redirect to HTTPS. The name's DNS must point at the host, and ports 80
and 443 reach it. `acme` sets the CA and where certificates are kept, `auto_https` the ports, or
turns it off. To use certificates of your own, write the listener:

```json
{"name": "web", "address": ":443", "protocols": ["http/1.1", "h2", "h3"],
 "tls": {"certificates": [{"cert_file": "example.com.pem", "key_file": "example.com.key"}]}}
```

fit loads the files again when they change.

## Middleware

Middleware runs in order before the handler. This compresses responses, and adds a field to each:

```json
{"match": {"path": ["/*"]},
 "middleware": [
   {"module": "compress"},
   {"module": "headers", "params": {"response": {"set": {"Strict-Transport-Security": "max-age=31536000"}}}}
 ],
 "handler": {"module": "static", "params": {"root": "./public"}}}
```

The [reference](reference/config.md#middleware) lists them all: rewrites and redirects, rate and
concurrency limits, Basic auth, JWTs, forward auth, client certificates, a circuit breaker, a
cache, WASM plugins.

## Change it while it runs

`fit run` applies the config file again on SIGHUP, or each time it changes with `-watch`. A config
that does not load leaves the running one in place, and says why. Listeners that did not change
keep their connections, and requests under way finish on the config they started with.

With `"admin": {}` in the config, the Admin API, on 127.0.0.1:2019 by default, reads and changes
the running config: here, the whole of it, then the first route's params, by their JSON pointer,
then a whole new config, and back to the first:

```bash
curl localhost:2019/config
curl -X PATCH -H 'Content-Type: application/merge-patch+json' \
  -d '{"body": "hello again\n"}' localhost:2019/config/routes/0/handler/params
curl -X POST -H 'Content-Type: application/json' -d @fit.json localhost:2019/load
curl -X POST localhost:2019/rollback/1
```

It keeps the configs it applied, to read again and roll back to, and serves metrics at `/metrics`
and a dashboard at `/dashboard/`.

## Next

- The [config reference](reference/config.md): every field, its type and default.
- Coming from another server: [nginx](migrating-from-nginx.md), [Caddy](migrating-from-caddy.md),
  [Traefik](migrating-from-traefik.md).
- [Compatibility](compatibility.md): what stays stable across 1.x releases.
- [Deploy](../deploy/README.md): the container image, Kubernetes and systemd.
