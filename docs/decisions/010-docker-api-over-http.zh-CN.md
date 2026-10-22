# 010：Docker provider 用普通 HTTP 访问 Engine API

[English](010-docker-api-over-http.md) | [简体中文](010-docker-api-over-http.zh-CN.md)

状态：已采纳，见 I57（2026-10-22）。

## 背景

I57 的 Docker provider 从 Docker daemon 读取容器并跟踪它们的事件；daemon 的 Engine API 是 Unix socket（或在 socket
代理之后的 TCP）上的 HTTP。可以用 Docker SDK（`github.com/docker/docker/client`，现为 `github.com/moby/moby/client`）
访问，也可以直接用 `net/http`。

## 决定

- **直接用 `net/http`，不用 SDK。** provider 只需要两个调用：`GET /containers/json`（运行中的容器，含标签、网络和
  端口）和 `GET /events`（JSON 行组成的流），外加查询参数中的 `filters`。用标准库只需几十行。SDK 会把 moby 模块及其
  依赖树（OpenTelemetry、containerd 的类型、docker/go-connections 等）带进二进制，却只为两个调用；它的版本随 daemon
  变动，导入路径也曾变过。
- **不带版本的路径。** daemon 对不带版本的路径按自己的 API 版本处理。Docker 29 拒绝低于 1.44 的版本，所以固定一个
  旧版本会在那里失败；而所读的字段（`Id`、`Names`、`State`、`Status`、`Labels`、`Ports`、`HostConfig.NetworkMode`、
  `NetworkSettings.Networks.*.IPAddress`）自 1.24 起每个版本都有。
- **端点**：`unix://<path>`（默认 `/var/run/docker.sock`），或面向 socket 代理的 `tcp://host:port`，都是普通 HTTP；
  到远程 daemon 的 TLS 有需要时再加。
- **有界，且在控制面**：provider 在自己的 goroutine 上运行；容器列表整体读取，最大 16 MiB，限时 `request_timeout`；
  一行事件最大 1 MiB；最多转换 `max_containers` 个容器；连接断开后每隔 1s 到 30s 重试。

## 影响

- `go.mod` 不增加依赖。
- 将来若需要旧版本没有的字段，provider 会读取 `/version` 并按 daemon 支持的来，而不是选定一个版本。
