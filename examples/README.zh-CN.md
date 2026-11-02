# 示例

[English](README.md) | [简体中文](README.zh-CN.md)

下面每个目录是一项功能的配置：一份不大、带注释的配置，只写用户实际会设置的字段，并用三种格式各写一遍：
fit 的 [.conf 格式](../docs/reference/fit.zh-CN.md)（`fit.conf`）、[YAML](../docs/reference/yaml.zh-CN.md)
（`fit.yaml`）和[带注释的 JSON](../docs/reference/json.zh-CN.md)（`fit.json`）。三份内容相同，由测试检查它们一致且都有效。
每个模块的全部字段见[配置模板](../templates/README.zh-CN.md)。

在仓库根目录下运行目录里的一个文件，因为示例里的文件路径相对于仓库根目录：

```bash
fit run -c examples/static/fit.conf
```

不要运行整个目录：`fit run -c examples/static` 会把三个文件合并成一份配置，每个监听器和路由都出现三次。

10.0.0.11、127.0.0.1:9000、auth.example.com 这类地址代表你自己的后端和服务，主机名代表你自己的域名。密码只是示例：
[basic-auth](basic-auth/fit.conf)、[console](console/fit.conf) 以及 llm 和 tunnel 控制台的用户都用
`example-password` 登录，其 bcrypt 哈希由 `fit hash-password` 生成。

## 处理器

| 示例 | 内容 |
| --- | --- |
| [hello](hello/fit.conf) | 在 8080 端口返回固定响应，其他路径返回 404。 |
| [static](static/fit.conf) | 从 [static/www](static/www) 提供文件：目录列表、预压缩文件、小文件缓存在内存中。 |
| [proxy](proxy/fit.conf) | 反向代理到两个上游，带限制、超时和健康检查。 |
| [load-balancing](load-balancing/fit.conf) | 加权最少连接、粘性 Cookie、主动与被动健康检查、有预算的重试、金丝雀分流、故障转移和流量镜像。 |
| [grpc](grpc/fit.conf) | 通过 h2c 接收 gRPC，经 HTTP/2 代理到 gRPC 服务器。 |
| [fastcgi](fastcgi/fit.conf) | 通过 Unix 套接字上的 PHP-FPM 运行 PHP，带前端控制器。 |
| [forward-proxy](forward-proxy/fit.conf) | HTTP CONNECT 正向代理，限制客户端、目标和端口。 |
| [socks](socks/fit.conf) | 独立监听器上的 SOCKS5 和 SOCKS4 正向代理。 |

## 路由

| 示例 | 内容 |
| --- | --- |
| [routing](routing/fit.conf) | 按路径、方法、请求头、查询参数、客户端地址、正则表达式和表达式匹配路由，以及优先级。 |
| [virtual-hosts](virtual-hosts/fit.conf) | 按主机名区分站点，精确名、通配符和默认站点；.conf 文件里用 `site` 块。 |

## 中间件

| 示例 | 内容 |
| --- | --- |
| [basic-auth](basic-auth/fit.conf) | 用 bcrypt 哈希校验的 HTTP Basic 认证。 |
| [jwt](jwt/fit.conf) | 用 JWK 集、签发者和受众校验 JSON Web Token，并把声明作为请求头传下去。 |
| [forward-auth](forward-auth/fit.conf) | 每个请求先询问认证服务，并传递其返回的用户字段。 |
| [client-cert](client-cert/fit.conf) | 只在部分路由要求客户端证书，并传递证书主题。 |
| [rate-limit](rate-limit/fit.conf) | 在可信代理之后，按客户端地址和 API key 限制请求速率。 |
| [concurrency-limit](concurrency-limit/fit.conf) | 限制一个路由同时处理的请求数。 |
| [body-limit](body-limit/fit.conf) | 在监听器限制之外，按路由限制请求体大小。 |
| [circuit-breaker](circuit-breaker/fit.conf) | 上游失败或变慢时，路由暂停向它发送请求。 |
| [cache](cache/fit.conf) | 遵循 RFC 9111 的 HTTP 缓存，带缓存锁和过期响应。 |
| [compress](compress/fit.conf) | 用 zstd、brotli 和 gzip 压缩响应。 |
| [headers](headers/fit.conf) | 设置安全响应头、删除内部字段、设置请求头。 |
| [rewrite](rewrite/fit.conf) | 按前缀和正则表达式改写路径。 |
| [redirect](redirect/fit.conf) | 重定向一个主机名和一个迁移了的栏目。 |
| [wasm](wasm/fit.conf) | 在独立的池上运行 WebAssembly 插件 [continue.wasm](wasm/continue.wasm)，源码为 [continue.wat](wasm/continue.wat)。 |

## TLS 与监听器

| 示例 | 内容 |
| --- | --- |
| [https](https/fit.conf) | 使用自有证书的 HTTPS，80 端口重定向；证书文件不在这里，所以测试不校验它。 |
| [auto-https](auto-https/fit.conf) | 默认 HTTPS：不写监听器，为路由的主机名从 Let's Encrypt 获取证书。 |
| [acme-on-demand](acme-on-demand/fit.conf) | 在握手时按需获取证书，由 ask URL 决定哪些名字可以，并限制速率和数量。 |
| [acme-shared-storage](acme-shared-storage/fit.conf) | 多个实例通过 Redis 共享 ACME 证书。 |
| [mtls](mtls/fit.conf) | 双向 TLS：每个客户端都必须出示[某个 CA](mtls/clients-ca.pem) 签发的证书。 |
| [http3](http3/fit.conf) | 在同一端口上同时提供基于 QUIC 的 HTTP/3、HTTP/1.1 和 HTTP/2。 |
| [proxy-protocol](proxy-protocol/fit.conf) | 接收可信负载均衡器发来的 PROXY 协议头，得到客户端的真实地址。 |
| [limits](limits/fit.conf) | 所有监听器的限制，以及 fib 的事件循环、工作协程和缓冲区。 |

## 四层

| 示例 | 内容 |
| --- | --- |
| [l4-tcp](l4-tcp/fit.conf) | TCP 直通到 PostgreSQL，以及按 SNI 路由 TLS 而不终结它，并向上游发送 PROXY 头。 |
| [l4-udp](l4-udp/fit.conf) | UDP 数据报发往两台 DNS 解析器，每个客户端地址一个会话。 |

## 提供者

| 示例 | 内容 |
| --- | --- |
| [provider-docker](provider-docker/fit.conf) | 从 Docker 容器的 label 读取路由。 |
| [provider-kubernetes](provider-kubernetes/fit.conf) | Kubernetes 集群的 Gateway API 和 Ingress。 |
| [provider-consul](provider-consul/fit.conf) | 从 Consul 目录的标签读取路由，发往健康的实例。 |
| [provider-consul-kv](provider-consul-kv/fit.conf) | 从 Consul KV 存储的键读取配置片段。 |
| [provider-etcd](provider-etcd/fit.conf) | 从 etcd 的键读取配置片段。 |
| [docker-compose](docker-compose/README.zh-CN.md) | 在 Compose 里，fit 的镜像位于应用的容器前面，按容器的 label 路由。 |
| [kubernetes](kubernetes/README.zh-CN.md) | 在 kind 集群里用 fit 的 Helm chart 服务一个 Gateway 和一个 Ingress。 |

## 可观测性

| 示例 | 内容 |
| --- | --- |
| [logging](logging/fit.conf) | 按 logger 配置 fit 自身的日志，以及访问日志。 |
| [telemetry](telemetry/fit.conf) | 通过 OTLP/HTTP 导出 OpenTelemetry 链路和指标。 |
| [metrics](metrics/fit.conf) | Admin API 的 /metrics 提供 Prometheus 指标，按监听器和路由统计。 |

## Admin API 与控制台

| 示例 | 内容 |
| --- | --- |
| [admin](admin/fit.conf) | 在 Unix 套接字上提供 Admin API，以及它保留的配置历史。 |
| [console](console/fit.conf) | 需要登录的控制台网页，以及加密保存在 admin.store 中的配置。 |

## LLM 与隧道

| 示例 | 内容 |
| --- | --- |
| [llm-hub](llm-hub/fit.conf) | llm 的 hub，节点和 proxy 连接到它。 |
| [llm-node](llm-node/fit.conf) | 通过 hub 提供本地 Ollama 模型的节点。 |
| [llm-proxy](llm-proxy/fit.conf) | proxy：为 agent 提供调用各节点模型的 OpenAI 和 Anthropic 兼容 API。 |
| [tunnel-hub](tunnel-hub/fit.conf) | 隧道 hub，支持 TCP 和 WebSocket。 |
| [tunnel-node](tunnel-node/fit.conf) | NAT 后面的节点，通过 hub 暴露它的服务。 |
| [tunnel-proxy](tunnel-proxy/fit.conf) | 把本地端口转发到某个节点的服务。 |

同一组角色的示例都在 127.0.0.1 上互相连接，所以可以原样在一台机器上运行，每个在单独的终端里 `fit run -c`。
llm 的示例写明了 `id`，否则 fit 会生成一个并写回文件。

## WebRTC

| 示例 | 内容 |
| --- | --- |
| [webrtc-turn](webrtc-turn/fit.conf) | TURN 服务器，带固定用户和限时凭据。 |
| [whip-whep](whip-whep/fit.conf) | fit 作为媒体服务器：用 WHIP 推流，用 WHEP 播放。 |
| [streaming](streaming/fit.conf) | 流媒体角色：经 RTMP、RTSP、SRT 或 WHIP 推流，经这些协议以及 HTTP-FLV、HLS 和 WHEP 播放，受 media_auth 策略保护。 |
| [webrtc-proxy](webrtc-proxy/fit.conf) | fit 后面的 WHIP 和 WHEP 媒体服务器，媒体经由一个 UDP 端口中继。 |

## 模块

| 示例 | 内容 |
| --- | --- |
| [module/hello](module/hello/hello.go) | 自己写的中间件，用 `fit build` 编进 fit。 |

[docker-compose](docker-compose/test.sh) 和 [kubernetes](kubernetes/test.sh) 的 `test.sh` 按其 README
的步骤运行示例并检查响应；CI 会运行它们。fit 的打包方式见 [deploy](../deploy/README.zh-CN.md)。
