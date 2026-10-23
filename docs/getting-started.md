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

A config may be a .fit file, YAML or JSON ([Configuration](configuration.md) compares them); this
guide uses the .fit format, fit's own, written in blocks of directives. Save this as `fit.fit`:

```fit
listener web {
	address :8080
	protocols http/1.1 h2c
}

route hello {
	match {
		path /hello
	}
	handler respond {
		body "hello from fit\n"
	}
}
```

A listener says where fit listens, and what it speaks there. A route says which requests it takes
(`match`), what they go through (`middleware`) and what answers them (`handler`), each a module
with its params in a block. Check the config, then run it:

```bash
fit validate -c fit.fit
fit run -c fit.fit
```

```bash
curl localhost:8080/hello
```

fit checks a config strictly: an unknown field, a value of the wrong type or a missing one is an
error, with its line and column, and fit does not start. A request no route matches gets 404.

## Serve files

Add a route that serves a directory, after the first:

```fit
listener web {
	address :8080
	protocols http/1.1 h2c
}

route hello {
	match {
		path /hello
	}
	handler respond {
		body "hello from fit\n"
	}
}

route files {
	match {
		path /*
	}
	handler static {
		root ./public
	}
}
```

A path ending in `*` matches a prefix. When several routes match, the most specific wins: `/hello`
before `/*`, whatever their order. `static` serves `index.html` for a directory; `browse` lists one
that has none, and `try_files` serves a single-page app.

## Proxy to an app

An upstream is a set of backends, by name; the `proxy` handler sends requests to one:

```fit
listener web {
	address :8080
	protocols http/1.1 h2c
}

upstream app {
	addresses 127.0.0.1:9000 127.0.0.1:9001
	health_check {
		path /healthz
		interval 5s
	}
}

route api {
	match {
		path /api/*
	}
	handler proxy {
		upstream app
	}
}

route files {
	match {
		path /*
	}
	handler static {
		root ./public
	}
}
```

Requests to `/api/` go to the two backends in turn, and a backend that fails its health check is
left out until it passes again. WebSockets and gRPC go through the same way. Every connection,
queue and timeout has a safe default, which the upstream's fields change.

## HTTPS

Write the routes in a site named by its host names, and leave the listeners out:

```fit
upstream app {
	addresses 127.0.0.1:9000 127.0.0.1:9001
}

site example.com {
	route /api/* {
		handler proxy { upstream app }
	}
	route /* {
		handler static { root ./public }
	}
}
```

A site's routes match its host names, and each its own paths. fit then listens on 443 for HTTPS,
with a certificate for `example.com` from Let's Encrypt, which it renews, and on 80 only to
redirect to HTTPS. The name's DNS must point at the host, and ports 80 and 443 reach it. `acme`
sets the CA and where certificates are kept, `auto_https` the ports, or turns it off. To use
certificates of your own, write the listener, and name it in the site:

```fit
listener web {
	address :443
	protocols http/1.1 h2 h3
	tls {
		certificates {
			cert_file example.com.pem
			key_file example.com.key
		}
	}
}

site example.com {
	listeners web
	route /* {
		handler static { root ./public }
	}
}
```

fit loads the files again when they change.

## Middleware

Middleware runs in order before the handler. In a site, middleware runs for each of its routes;
this compresses responses, and adds a field to each:

```fit
site example.com {
	middleware compress
	middleware headers {
		response {
			set {
				Strict-Transport-Security "max-age=31536000"
			}
		}
	}
	route /* {
		handler static { root ./public }
	}
}
```

The [reference](reference/config.md#middleware) lists them all: rewrites and redirects, rate and
concurrency limits, Basic auth, JWTs, forward auth, client certificates, a circuit breaker, a
cache, WASM plugins.

## Several files

As a config grows, split it: `-c` takes a directory, and every `.fit`, `.yaml`, `.yml` and `.json`
file in it, in its subdirectories too, is read, the routes and upstreams of each joined:

```bash
fit run -c /etc/fit
```

with, say, `/etc/fit/base.fit` holding the listeners and `acme`, and a file in `/etc/fit/sites/`
for each site. A field other than the lists, as `admin` or `acme`, is given in one file only. The
[.fit reference](reference/fit.md#several-files-and-directories) has the details.

## Change it while it runs

`fit run` applies the config again on SIGHUP, or each time one of its files changes with `-watch`,
a file added to its directory or taken out included. A config that does not load leaves the running
one in place, and says why. Listeners that did not change keep their connections, and requests
under way finish on the config they started with.

With `admin {}` in the config, the Admin API, on 127.0.0.1:2019 by default, reads and changes
the running config, in fit's native JSON: here, the whole of it, then the first route's params, by
their JSON pointer, then a whole new config, converted from the .fit file by `fit adapt`, and back
to the first:

```bash
curl localhost:2019/config
curl -X PATCH -H 'Content-Type: application/merge-patch+json' \
  -d '{"body": "hello again\n"}' localhost:2019/config/routes/0/handler/params
fit adapt -c fit.fit -o fit.json
curl -X POST -H 'Content-Type: application/json' -d @fit.json localhost:2019/load
curl -X POST localhost:2019/rollback/1
```

It keeps the configs it applied, to read again and roll back to, and serves metrics at `/metrics`
and a dashboard at `/dashboard/`.

## Next

- [Configuration](configuration.md): the same config as a [.fit file](reference/fit.md), in
  [YAML](reference/yaml.md) or in [JSON](reference/json.md).
- The [config reference](reference/config.md): every field, its type and default, with examples.
- Coming from another server: [nginx](migrating-from-nginx.md), [Caddy](migrating-from-caddy.md),
  [Traefik](migrating-from-traefik.md).
- [Compatibility](compatibility.md): what stays stable across 1.x releases.
- [Deploy](../deploy/README.md): the container image, Kubernetes and systemd.
