# fit

[![CI](https://github.com/lesismal/fit/actions/workflows/ci.yml/badge.svg)](https://github.com/lesismal/fit/actions/workflows/ci.yml) [![Coverage](https://img.shields.io/endpoint?url=https%3A%2F%2Fraw.githubusercontent.com%2Flesismal%2Ffit%2Fbadges%2Fcoverage.json)](https://github.com/lesismal/fit/actions/workflows/ci.yml) [![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

[English](README.md) | [简体中文](README.zh-CN.md)

fit is a reverse proxy, load balancer, TLS terminator, static file server and API gateway built on
[fib](https://github.com/lesismal/fib), in the same space as nginx, Caddy and Traefik.

It serves HTTP/1.1, HTTP/2 and HTTP/3, with HTTPS by default; proxies HTTP, WebSockets, gRPC, TCP
and UDP, with health checks and load balancing; serves static files; caches responses; and takes
its routes from a strict config, written in fit's own .konf format, in YAML or in JSON and changed in
place through an Admin API, or from Docker, Consul, etcd and Kubernetes. Every milestone's iterations are done, 1.0's included, and 1.0's
external security review is still to come; see [progress](docs/progress.md) for what works, and
[bench](bench/README.md) for how it is measured against nginx, Caddy and Traefik.

## Docs

- [Getting started](docs/getting-started.md): from nothing to a site over HTTPS.
- [Configuration](docs/configuration.md): the three formats a config may be written in,
  [.konf](docs/reference/fit.md), [YAML](docs/reference/yaml.md) and [JSON](docs/reference/json.md),
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

## Installing

With Go 1.27.3 or later, `go install` builds fit into `$(go env GOPATH)/bin`, the latest release
or any version, tag or commit you name:

```bash
go install github.com/lesismal/fit/cmd/fit@latest
```

```bash
go install github.com/lesismal/fit/cmd/fit@v1.0.0
```

`go get github.com/lesismal/fit@latest` (or `@v1.0.0`) adds fit to a module of your own, to use
it as a library or to build fit with plugins (`fit build`); it installs no command.

Without Go, download a prebuilt binary from
[GitHub Releases](https://github.com/lesismal/fit/releases): Linux (amd64, arm64, armv7, 386),
macOS (amd64, arm64), Windows (amd64, arm64) and FreeBSD (amd64, arm64), each a `.tar.gz` (a
`.zip` for Windows) named `fit_<version>_<os>_<arch>`, with their SHA-256 sums in
`checksums.txt`. The latest release, for Linux on amd64:

```bash
VERSION=$(curl -fsSLI -o /dev/null -w '%{url_effective}' https://github.com/lesismal/fit/releases/latest | sed 's|.*/v||')
curl -fsSLO "https://github.com/lesismal/fit/releases/download/v${VERSION}/fit_${VERSION}_linux_amd64.tar.gz"
tar -xzf "fit_${VERSION}_linux_amd64.tar.gz"
sudo install "fit_${VERSION}_linux_amd64/fit" /usr/local/bin/fit
```

Set `VERSION=1.0.0` instead for a given version. A container image is published as well:
`ghcr.io/lesismal/fit` ([deploy/README.md](deploy/README.md)).

## Building

fit needs Go 1.27.3 or later.

```bash
make build
```

```bash
./bin/fit validate -c examples/hello.konf
./bin/fit run -c examples/hello.konf
```

`examples/hello.konf` answers `/hello` on port 8080:

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

A config may be a [.konf file](docs/reference/fit.md), fit's own format, as above, YAML
(`examples/hello.yaml`) or JSON (`examples/hello.json`, with `//` and `/* */` comments and trailing
commas allowed), each with the same fields, and may be split across several files and directories
(`fit run -c base.konf -c conf.d`). `fit adapt` converts between the formats, and from nginx configs
and Caddyfiles, listing what it left out. [Configuration](docs/configuration.md) has the details.

`fit run` reloads its config files on SIGHUP, keeping the listeners that did not change and the
running config if the new one is broken, and shuts down gracefully on SIGINT or SIGTERM.

`make check` runs gofmt, go vet, staticcheck and the tests with the race detector.

## License

Apache License 2.0, see [LICENSE](LICENSE).
