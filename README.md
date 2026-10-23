# fit

[English](README.md) | [简体中文](README.zh-CN.md)

fit is a reverse proxy, load balancer, TLS terminator, static file server and API gateway built on
[fib](https://github.com/lesismal/fib), in the same space as nginx, Caddy and Traefik.

It serves HTTP/1.1, HTTP/2 and HTTP/3, with HTTPS by default; proxies HTTP, WebSockets, gRPC, TCP
and UDP, with health checks and load balancing; serves static files; caches responses; and takes
its routes from a strict config, written in fit's own .fit format, in YAML or in JSON and changed in
place through an Admin API, or from Docker, Consul, etcd and Kubernetes. Every milestone's iterations are done, 1.0's included, and 1.0's
external security review is still to come; see [progress](docs/progress.md) for what works, and
[bench](bench/README.md) for how it is measured against nginx, Caddy and Traefik.

## Docs

- [Getting started](docs/getting-started.md): from nothing to a site over HTTPS.
- [Configuration](docs/configuration.md): the three formats a config may be written in,
  [.fit](docs/reference/fit.md), [YAML](docs/reference/yaml.md) and [JSON](docs/reference/json.md),
  how a config is split across files, and how it is run, checked, converted and changed.
- [Config templates](templates/README.md): a complete config in each format, every field in it with
  its choices in comments.
- [Config reference](docs/reference/config.md): every field and every module's params, chapter by
  chapter, with examples in all three formats; generated from the code.
- Migrating from [nginx](docs/migrating-from-nginx.md), [Caddy](docs/migrating-from-caddy.md) and
  [Traefik](docs/migrating-from-traefik.md).
- [Writing modules](docs/modules.md): extending fit with Go, built in with `fit build`.
- [nginx, Caddy and Traefik compared](docs/comparison.md): what fit takes from each.
- [Roadmap](docs/roadmap.md): goals, design principles and milestones.
- [Development plan](docs/dev-plan.md): the milestones broken into small iterations.
- [Progress](docs/progress.md): what is done, and where to pick up.
- [Compatibility](docs/compatibility.md): what stays stable from 1.0, and how renamed fields are handled.
- [Deploying](deploy/README.md): the container image, the Helm chart and systemd units; and the
  [examples](examples/README.md), with Docker Compose and Kubernetes.

## Building

fit needs Go 1.27.3 or later.

```bash
make build
```

```bash
./bin/fit validate -c examples/hello.fit
./bin/fit run -c examples/hello.fit
```

`examples/hello.fit` answers `/hello` on port 8080:

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

route not-found {
	priority -1
	handler respond {
		status 404
		body "not found\n"
	}
}
```

A config may be a [.fit file](docs/reference/fit.md), fit's own format, as above, YAML
(`examples/hello.yaml`) or JSON (`examples/hello.json`, with `//` and `/* */` comments and trailing
commas allowed), each with the same fields, and may be split across several files and directories
(`fit run -c base.fit -c conf.d`). `fit adapt` converts between the formats, and from nginx configs
and Caddyfiles, listing what it left out. [Configuration](docs/configuration.md) has the details.

`fit run` reloads its config files on SIGHUP, keeping the listeners that did not change and the
running config if the new one is broken, and shuts down gracefully on SIGINT or SIGTERM.

`make check` runs gofmt, go vet, staticcheck and the tests with the race detector.
