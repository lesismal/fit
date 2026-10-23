# Deploying fit

[English](README.md) | [简体中文](README.zh-CN.md)

fit is one static binary with one config file. This directory packages it three ways: a container
image, a Helm chart for Kubernetes, and systemd units. [examples](../examples/README.md) runs the
image with Docker Compose and the chart in a kind cluster, and CI runs each as written here.

## The container image

The [Dockerfile](../Dockerfile) at the repository's root builds fit without cgo, with `-trimpath`,
on [distroless's static image](https://github.com/GoogleContainerTools/distroless), as its
`nonroot` user (65532); see [decision 016](../docs/decisions/016-container-image.md). The image
holds only `/usr/local/bin/fit` and a placeholder config at `/etc/fit/fit.json`, which answers every
request on port 8080.

```bash
docker build -t fit .
docker run --rm -p 8080:8080 -v "$PWD/examples/hello.json:/etc/fit/fit.json:ro" fit
```

- **Your config** goes at `/etc/fit/fit.json`, or anywhere with `run -c <path>` as the command.
- **Ports below 1024**: Docker lets a container's user bind them; elsewhere, open them with the
  sysctl `net.ipv4.ip_unprivileged_port_start=0`, as the Helm chart does.
- **State** such as ACME's certificates goes in a volume, named in the config's storage directory.
- **The Admin API** listens on 127.0.0.1:2019 by default, inside the container. To reach it from
  outside, give it an address of every interface and a `token_file`, which fit then requires.
- **The Docker provider** reads `/var/run/docker.sock`, which is root's: mount it read-only and run
  fit as root, as the Compose example does, or point the provider's `endpoint` at a socket proxy.
- **Several platforms**: `docker buildx build --platform linux/amd64,linux/arm64 .` compiles each on
  the builder's own platform, without emulation. `--build-arg VERSION=v0.1.0` sets what
  `fit version` prints.

A tag `v<semver>` publishes the image for linux/amd64 and linux/arm64 as
`ghcr.io/lesismal/fit:<version>` and `:<major>.<minor>` ([release.yml](../.github/workflows/release.yml)).
The same tag attaches fit's binaries to its GitHub Release, packed with the READMEs, with their
SHA-256 sums in `checksums.txt`: Linux (amd64, arm64, armv7, 386), macOS (amd64, arm64), Windows
(amd64, arm64) and FreeBSD (amd64, arm64). `make dist` builds the same archives in `dist/`.

## The Helm chart

[helm/fit](helm/fit) runs fit in a cluster as a controller of the Gateway API and of Ingresses,
with the Kubernetes provider: fit serves the Gateways of its GatewayClass `fit` and their routes,
writing their status back, and the Ingresses of its IngressClass `fit`.

```bash
kubectl apply --server-side -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.3.0/standard-install.yaml
helm install fit deploy/helm/fit
```

Install the Gateway API's CRDs first: the chart makes the GatewayClass only when the cluster has
them (or upgrade the release after installing them). The experimental channel's
`experimental-install.yaml` adds TLSRoutes, TCPRoutes and UDPRoutes, which fit serves as well.

What it makes:

- A **Deployment** of fit, as the image's user, with a read-only root file system, no capabilities,
  and the sysctl that lets it bind the ports Gateways listen on. Its config, in a ConfigMap, is a
  listener of fit's own on `healthPort` (9099) answering the kubelet's probes on `/healthz`, and
  the Kubernetes provider, which adds each Gateway's listeners and routes. A changed config rolls
  the pods.
- A **Service** in front of it with the ports the Gateways and Ingresses listen on, 80 and 443 by
  default (`service.ports`; `service.type: LoadBalancer` to expose them).
- A **ClusterRole** to list and watch, across the cluster, namespaces, Services, EndpointSlices,
  Secrets (only those of type `kubernetes.io/tls` are listed), the Gateway API's kinds and,
  with `ingress.enabled`, Ingresses and IngressClasses, and to patch the status of GatewayClasses,
  Gateways and routes.
- The **GatewayClass** `fit` and, with `ingress.enabled` (the default), the **IngressClass** `fit`
  (`ingress.default: true` makes it the cluster's default).

| Value | Default | What it sets |
| --- | --- | --- |
| `image.repository`, `image.tag` | `ghcr.io/lesismal/fit`, the chart's appVersion | The image. |
| `replicas` | 1 | Each replica serves every Gateway and writes the same status. |
| `controllerName` | `lesismal.github.io/fit` | The classes' controller. |
| `gatewayClass.create`, `gatewayClass.name` | `true`, `fit` | The GatewayClass. |
| `ingress.enabled`, `ingress.className`, `ingress.default` | `true`, `fit`, `false` | Ingresses and their class. |
| `ingress.httpPort`, `ingress.httpsPort` | 80, 443 | The ports Ingresses are served on. |
| `provider` | `{}` | More of the provider's settings: `namespaces`, `backends`, `addresses`, `max_objects`, `request_timeout` (see `fit schema`). |
| `healthPort` | 9099 | The port of the probes, apart from any a Gateway uses. |
| `config` | `{}` | A whole config in place of the chart's; keep its health listener, route and provider. |
| `extraArgs` | `[]` | More flags for `fit run`. |
| `service.*` | ClusterIP, ports 80 and 443 | The Service. |

The rest (`resources`, `nodeSelector`, `tolerations`, `affinity`, `podSecurityContext`,
`securityContext`, `podAnnotations`, `serviceAccount`) are as in most charts; see
[values.yaml](helm/fit/values.yaml).

Until a release publishes the image, build it and load it into the cluster, as
[the Kubernetes example](../examples/kubernetes/README.md) does with kind:
`--set image.repository=fit --set image.tag=local`.

## systemd

[systemd](systemd) has `fit.service`, for fit with its config at `/etc/fit/fit.json`, and
`fit@.service`, for several fits on one host, `fit@<name>` running `/etc/fit/<name>.json`.

```bash
make build
sudo install -m 0755 bin/fit /usr/local/bin/fit
sudo install -m 0644 deploy/systemd/fit.service deploy/systemd/fit@.service /etc/systemd/system/
sudo install -m 0644 deploy/systemd/fit.sysusers /usr/lib/sysusers.d/fit.conf
sudo systemd-sysusers
sudo install -D -m 0644 examples/hello.json /etc/fit/fit.json
sudo systemctl daemon-reload
sudo systemctl enable --now fit
```

- fit runs as the system user `fit`, with `CAP_NET_BIND_SERVICE` alone, so it may bind ports 80
  and 443, and with the file system read-only but for `/var/lib/fit` (`StateDirectory`; point ACME's
  storage there) and `/run/fit` (`RuntimeDirectory`; for the Admin API's Unix socket,
  `unix//run/fit/admin.sock`). `fit@<name>` has `/var/lib/fit/<name>` and `/run/fit/<name>`.
- The config is checked with `fit validate` before fit starts. `systemctl reload fit` checks the
  new one and sends fit SIGHUP: a config that does not validate fails the reload, and fit keeps the
  one it runs.
- `systemctl stop fit` sends SIGTERM; fit waits up to 30s (`-shutdown-timeout`) for requests in
  flight, and systemd up to 40s for fit.
- The Docker provider needs the socket's group: `systemctl edit fit`, with
  `[Service]` and `SupplementaryGroups=docker`. Other changes go in drop-ins the same way.
