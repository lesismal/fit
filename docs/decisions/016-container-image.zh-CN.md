# 016：容器镜像基于 distroless 的 static 镜像，示例使用 whoami

[English](016-container-image.md) | [简体中文](016-container-image.zh-CN.md)

状态：已采纳，见 I66（2026-10-22）。

## 背景

I66 把 fit 打包成容器镜像、Helm chart 和 systemd unit，并提供 Docker Compose 和 Kubernetes 的示例。镜像需要一个
基础镜像，示例需要一个可以转发到的应用。两者都不是 Go 依赖，但 fit 的用户和 CI 都会拉取这些镜像。

## 决定

- **镜像的基础是 `gcr.io/distroless/static-debian12:nonroot`。** fit 不用 cgo 构建，所以不需要 libc；它需要的是
  CA 证书（上游 TLS、ACME、provider 的 HTTPS）、时区数据、带非特权用户的 `/etc/passwd`，以及 `/tmp`。这个镜像
  约 2 MB 就提供了这些，没有 shell 也没有需要打补丁的包管理器。用 `scratch` 要手工拷入这些内容；Alpine 或 Debian
  会带上 fit 从不运行的 shell 和软件包。镜像以 `nonroot`（65532）运行：1024 以下的端口靠 Docker 默认的 sysctl
  或 chart 设置的 sysctl 获得，不靠 root 或 capability。
- **二进制在构建机的平台上编译**（`--platform=$BUILDPLATFORM`，`GOOS` 和 `GOARCH` 取自目标平台），因此
  linux/amd64 和 linux/arm64 都无需模拟即可构建，并使用 BuildKit 的模块缓存和构建缓存。
- **示例转发到 `traefik/whoami`**，固定为 v1.11.0：一个几 MB 的应用，用主机名回答请求，能看出是哪个副本回答的，
  并带有用于健康检查的 `/health`。也可以用 fit 自己的镜像返回固定响应来代替，但每个副本的回答都一样，负载均衡
  就看不出来了。

## 影响

- 镜像里没有 shell：`docker exec` 打不开 shell，调试要通过 `fit` 自己的命令、日志和 Admin API，或临时的调试容器。
- Dockerfile 需要 BuildKit（`RUN --mount`、`$BUILDPLATFORM`）；安装了 buildx 时，它从 Docker 23.0 起就是默认构建器。
