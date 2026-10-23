# fit

[English](README.md) | [简体中文](README.zh-CN.md)

fit is a reverse proxy, load balancer, TLS terminator, static file server and API gateway built on
[fib](https://github.com/lesismal/fib), in the same space as nginx, Caddy and Traefik.

fit is in early development. Today it serves fixed responses over HTTP/1.1 and h2c from a JSON
config; see [progress](docs/progress.md) for what works, and [bench](bench/README.md) for how it is
measured against nginx, Caddy and Traefik.

## Docs

- [nginx, Caddy and Traefik compared](docs/comparison.md): what fit takes from each.
- [Roadmap](docs/roadmap.md): goals, design principles and milestones.
- [Development plan](docs/dev-plan.md): the milestones broken into small iterations.
- [Progress](docs/progress.md): what is done, and where to pick up.
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

A config may also be written in YAML (`examples/hello.yaml`), and `fit adapt` converts between the
two.

`fit run` reloads its config file on SIGHUP, keeping the listeners that did not change and the
running config if the new one is broken, and shuts down gracefully on SIGINT or SIGTERM.

`make check` runs gofmt, go vet, staticcheck and the tests with the race detector.
