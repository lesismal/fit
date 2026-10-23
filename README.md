# fit

[English](README.md) | [简体中文](README.zh-CN.md)

fit is a reverse proxy, load balancer, TLS terminator, static file server and API gateway built on
[fib](https://github.com/lesismal/fib), in the same space as nginx, Caddy and Traefik.

It serves HTTP/1.1, HTTP/2 and HTTP/3, with HTTPS by default; proxies HTTP, WebSockets, gRPC, TCP
and UDP, with health checks and load balancing; serves static files; caches responses; and takes
its routes from a strict JSON or YAML config, changed in place through an Admin API, or from Docker,
Consul, etcd and Kubernetes. Every milestone's iterations are done, 1.0's included, and 1.0's
external security review is still to come; see [progress](docs/progress.md) for what works, and
[bench](bench/README.md) for how it is measured against nginx, Caddy and Traefik.

## Docs

- [Getting started](docs/getting-started.md): from nothing to a site over HTTPS.
- [Config reference](docs/reference/config.md): every field, generated from the code.
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

fit needs Go 1.27 or later.

```bash
make build
```

```bash
./bin/fit validate -c examples/hello.json
./bin/fit run -c examples/hello.json
```

JSON configs may have `//` and `/* */` comments and trailing commas. A config may also be written
in YAML (`examples/hello.yaml`), and `fit adapt` converts between the two, and converts a common
subset of nginx configs and of Caddy's Caddyfile, listing what it left out; `fit run` takes any of
them as it is.

`fit run` reloads its config file on SIGHUP, keeping the listeners that did not change and the
running config if the new one is broken, and shuts down gracefully on SIGINT or SIGTERM.

`make check` runs gofmt, go vet, staticcheck and the tests with the race detector.
