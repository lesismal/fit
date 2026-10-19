# fit

[English](README.md) | [简体中文](README.zh-CN.md)

fit is a reverse proxy, load balancer, TLS terminator, static file server and API gateway built on
[fib](https://github.com/lesismal/fib), in the same space as nginx, Caddy and Traefik.

fit is in early development and does not serve traffic yet. Today it can load and check a config;
see [progress](docs/progress.md) for what works.

## Docs

- [nginx, Caddy and Traefik compared](docs/comparison.md): what fit takes from each.
- [Roadmap](docs/roadmap.md): goals, design principles and milestones.
- [Development plan](docs/dev-plan.md): the milestones broken into small iterations.
- [Progress](docs/progress.md): what is done, and where to pick up.

## Building

fit needs Go 1.27 or later.

```bash
make build
```

```bash
./bin/fit validate -c examples/hello.json
```

`make check` runs gofmt, go vet, staticcheck and the tests with the race detector.
