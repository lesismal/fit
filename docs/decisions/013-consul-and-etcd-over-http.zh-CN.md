# 013：Consul 与 etcd provider 用 net/http 访问它们的 HTTP API

[English](013-consul-and-etcd-over-http.md) | [简体中文](013-consul-and-etcd-over-http.zh-CN.md)

状态：已采纳，见 I62（2026-10-22）。

## 背景

I62 从 Consul 的服务目录读取服务，从 Consul 的 KV 存储和 etcd 读取配置。Consul 有官方客户端
`github.com/hashicorp/consul/api`；etcd 有走 gRPC 的 `go.etcd.io/etcd/client/v3`。两者也都以 HTTP 和 JSON 提供
provider 所需的一切：Consul 的 API 只有 HTTP，etcd 在客户端端口上为 v3 API 提供 JSON 网关，默认开启。

## 决定

- **用 `net/http`，不用客户端库。** Consul provider 需要 `GET /v1/catalog/services`、
  `GET /v1/health/service/<name>?passing`、`GET /v1/health/state/any` 和 `GET /v1/kv/<prefix>?recurse`，每个也作为
  阻塞查询使用（`index` 与 `wait`，index 取自 `X-Consul-Index`）。etcd provider 需要 `POST /v3/kv/range`、
  `POST /v3/watch`（应答是 JSON 对象组成的流，每行一个）和 `POST /v3/auth/authenticate`。用标准库只需几百行。etcd 的
  客户端为三个调用把 gRPC、protobuf 和 etcd 的 API 模块带进二进制；Consul 的会带来 HashiCorp 的辅助模块，而其阻塞
  查询仍要手动驱动。
- **按 Consul 的指引做阻塞查询**：index 回退时从 0 重新开始，小于 1 时按 1 处理，同一个 watch 的两次查询至少相隔
  1s，避免 index 不停变化时 provider 空转。目录 provider 对服务和健康检查做轮询，任一变化后（经去抖）整体读取目录，
  与 Traefik 的 provider 相同，而不是为每个服务各开一个阻塞查询。
- **etcd 先 watch 后读取**：从当前修订版开始的 watch 在 etcd 说明它已创建时就位，这必须在 `request_timeout` 内到来；
  只有此后才读取键，两者之间的变化不会漏掉。watch 被取消时（例如压缩之后）重新开始。
- **密钥放在文件里**：Consul 的 ACL token 和 etcd 的密码从文件读取，每次 watch 开始时读一次，因此不写在配置中
  （Admin API 会展示配置），也可以轮换。到两者的 TLS 可设 CA，以及客户端证书和私钥，读取方式相同。
- **有界，且在控制面**：provider 在自己的 goroutine 上运行；应答整体读取，最大 32 MiB，watch 的一条消息最大 32 MiB；
  最多取 `max_services` 或 `max_keys` 个；每个请求最多耗时 `request_timeout`（阻塞查询的五分钟等待之外）；连接断开后
  每隔 1s 到 30s 重试，etcd 的端点轮流尝试。

## 影响

- `go.mod` 不增加依赖。
- etcd 的网关须保持开启（`--enable-grpc-gateway`，默认开启）；关闭它的集群无法读取。不支持 etcd 的 v2 API 和
  `v3beta` 路径。
- 不涉及 Consul Enterprise 的命名空间和管理分区；以后可作为设置项加入，转成查询参数。
