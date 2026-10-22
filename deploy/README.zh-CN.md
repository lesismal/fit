# 部署 fit

[English](README.md) | [简体中文](README.zh-CN.md)

fit 是一个静态二进制加一个配置文件。本目录提供三种打包方式：容器镜像、Kubernetes 的 Helm chart、systemd
unit。[examples](../examples/README.zh-CN.md) 用 Docker Compose 运行镜像，在 kind 集群里安装 chart；CI
按这里写的步骤逐一运行它们。

## 容器镜像

仓库根目录的 [Dockerfile](../Dockerfile) 不用 cgo、带 `-trimpath` 构建 fit，基础镜像是
[distroless 的 static 镜像](https://github.com/GoogleContainerTools/distroless)，以其 `nonroot` 用户（65532）
运行；见[决策 016](../docs/decisions/016-container-image.zh-CN.md)。镜像里只有 `/usr/local/bin/fit`，以及
`/etc/fit/fit.json` 处的一个占位配置，在 8080 端口回答所有请求。

```bash
docker build -t fit .
docker run --rm -p 8080:8080 -v "$PWD/examples/hello.json:/etc/fit/fit.json:ro" fit
```

- **自己的配置**放在 `/etc/fit/fit.json`，或放在任意位置并以 `run -c <路径>` 作为命令。
- **1024 以下的端口**：Docker 允许容器的用户绑定；在别处用 sysctl
  `net.ipv4.ip_unprivileged_port_start=0` 放开，Helm chart 就是这样做的。
- **状态**（如 ACME 的证书）放在 volume 里，在配置的存储目录中指定。
- **Admin API** 默认在容器内的 127.0.0.1:2019 监听。要从外部访问，给它所有网卡的地址和 `token_file`，
  fit 随后会要求带上 token。
- **Docker provider** 读取属于 root 的 `/var/run/docker.sock`：以只读方式挂载并以 root 运行 fit（Compose
  示例的做法），或把 provider 的 `endpoint` 指向一个 socket 代理。
- **多平台**：`docker buildx build --platform linux/amd64,linux/arm64 .` 在构建机自己的平台上交叉编译每个目标，
  不需要模拟。`--build-arg VERSION=v0.1.0` 设置 `fit version` 打印的版本。

打 `v<semver>` 标签会把 linux/amd64 和 linux/arm64 的镜像发布为 `ghcr.io/lesismal/fit:<version>` 和
`:<major>.<minor>`（[release.yml](../.github/workflows/release.yml)）。

## Helm chart

[helm/fit](helm/fit) 在集群里把 fit 作为 Gateway API 和 Ingress 的控制器运行，使用 Kubernetes provider：fit
服务其 GatewayClass `fit` 的 Gateway 及其路由并回写状态，也服务其 IngressClass `fit` 的 Ingress。

```bash
kubectl apply --server-side -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.3.0/standard-install.yaml
helm install fit deploy/helm/fit
```

先安装 Gateway API 的 CRD：只有集群里有这些 CRD 时 chart 才会创建 GatewayClass（也可以装好 CRD 后再 upgrade
release）。experimental 通道的 `experimental-install.yaml` 还会加上 TLSRoute、TCPRoute 和 UDPRoute，fit
同样支持。

chart 创建的内容：

- fit 的 **Deployment**：以镜像的用户运行，根文件系统只读，不带任何 capability，并用 sysctl 允许它绑定 Gateway
  监听的端口。它的配置放在 ConfigMap 里：fit 自己的一个 listener，在 `healthPort`（9099）上用 `/healthz`
  回答 kubelet 的探针；以及 Kubernetes provider，它会加入每个 Gateway 的 listener 和路由。配置变化时 Pod
  会滚动更新。
- 前面的 **Service**，端口是 Gateway 和 Ingress 监听的端口，默认 80 和 443（`service.ports`；要对外暴露可设
  `service.type: LoadBalancer`）。
- **ClusterRole**：在整个集群范围 list、watch namespace、Service、EndpointSlice、Secret（只列出
  `kubernetes.io/tls` 类型的）、Gateway API 的各种资源，以及开启 `ingress.enabled` 时的 Ingress 和
  IngressClass；并 patch GatewayClass、Gateway 和各路由的状态。
- **GatewayClass** `fit`，以及开启 `ingress.enabled`（默认开启）时的 **IngressClass** `fit`
  （`ingress.default: true` 让它成为集群的默认 IngressClass）。

| 值 | 默认 | 作用 |
| --- | --- | --- |
| `image.repository`、`image.tag` | `ghcr.io/lesismal/fit`、chart 的 appVersion | 镜像。 |
| `replicas` | 1 | 每个副本都服务所有 Gateway，并写入相同的状态。 |
| `controllerName` | `lesismal.github.io/fit` | 两种 class 的控制器名。 |
| `gatewayClass.create`、`gatewayClass.name` | `true`、`fit` | GatewayClass。 |
| `ingress.enabled`、`ingress.className`、`ingress.default` | `true`、`fit`、`false` | Ingress 及其 class。 |
| `ingress.httpPort`、`ingress.httpsPort` | 80、443 | 服务 Ingress 的端口。 |
| `provider` | `{}` | provider 的其他设置：`namespaces`、`backends`、`addresses`、`max_objects`、`request_timeout`（见 `fit schema`）。 |
| `healthPort` | 9099 | 探针端口，不能与 Gateway 使用的端口重复。 |
| `config` | `{}` | 用一份完整配置替换 chart 生成的；请保留其中的健康检查 listener、路由和 provider。 |
| `extraArgs` | `[]` | `fit run` 的其他参数。 |
| `service.*` | ClusterIP，端口 80 和 443 | Service。 |

其余的值（`resources`、`nodeSelector`、`tolerations`、`affinity`、`podSecurityContext`、`securityContext`、
`podAnnotations`、`serviceAccount`）与多数 chart 相同；见 [values.yaml](helm/fit/values.yaml)。

在正式发布镜像之前，先构建镜像并载入集群，就像 [Kubernetes 示例](../examples/kubernetes/README.zh-CN.md)
用 kind 做的那样：`--set image.repository=fit --set image.tag=local`。

## systemd

[systemd](systemd) 里有 `fit.service`，用于配置在 `/etc/fit/fit.json` 的 fit；以及 `fit@.service`，用于同一台
主机上运行多个 fit，`fit@<name>` 运行 `/etc/fit/<name>.json`。

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

- fit 以系统用户 `fit` 运行，只带 `CAP_NET_BIND_SERVICE`，因此可以绑定 80 和 443 端口；文件系统只读，只有
  `/var/lib/fit`（`StateDirectory`，ACME 的存储放这里）和 `/run/fit`（`RuntimeDirectory`，用于 Admin API 的
  Unix socket，`unix//run/fit/admin.sock`）可写。`fit@<name>` 对应 `/var/lib/fit/<name>` 和 `/run/fit/<name>`。
- fit 启动前先用 `fit validate` 检查配置。`systemctl reload fit` 检查新配置后给 fit 发 SIGHUP：配置校验不通过时
  reload 失败，fit 继续使用正在运行的配置。
- `systemctl stop fit` 发送 SIGTERM；fit 最多等 30s（`-shutdown-timeout`）让进行中的请求完成，systemd 最多等
  fit 40s。
- Docker provider 需要 socket 所属的组：`systemctl edit fit`，写入 `[Service]` 和 `SupplementaryGroups=docker`。
  其他修改也以同样方式放在 drop-in 里。
