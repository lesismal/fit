# fit 路线图

[English](roadmap.md) | [简体中文](roadmap.zh-CN.md)

fit 是基于 [fib](https://github.com/lesismal/fib) 的反向代理、负载均衡、TLS 终结、静态文件服务和
API 网关。本路线图把 [nginx、Caddy、Traefik 对比](comparison.zh-CN.md) 的结论落成里程碑：
每个里程碑交付什么、怎样算完成、建立在 fib 的哪些能力之上。

fit 目前处于 M0，已完成的内容见 [开发进度](progress.zh-CN.md)。[开发计划](dev-plan.zh-CN.md) 把这些里程碑拆成了小迭代，
每个迭代的大小适合一次工作会话。里程碑的先后顺序是确定的，具体内容会随开发中的认识调整。
文中刻意不写日期：里程碑在满足退出标准时结束，而不是按日历结束。

- [目标](#目标)
- [非目标](#非目标)
- [设计原则](#设计原则)
- [架构](#架构)
- [里程碑](#里程碑)
  - [M0 基础](#m0-基础)
  - [M1 反向代理核心](#m1-反向代理核心)
  - [M2 动态配置与 TLS](#m2-动态配置与-tls)
  - [M3 网关能力](#m3-网关能力)
  - [M4 平台集成](#m4-平台集成)
  - [M5 缓存与扩展](#m5-缓存与扩展)
  - [1.0](#10)
- [功能对照](#功能对照)
- [性能目标](#性能目标)
- [风险与待定问题](#风险与待定问题)

## 目标

对比表明，三者各自的长处恰好是另一方的短处。fit 的目标是同时具备三者的长处：

| 对比对象 | fit 保留它的什么 | fit 应该在哪里做得更好 |
| --- | --- | --- |
| nginx | 事件循环、每个空闲连接占用很小、强大的静态文件和缓存 | 进程内改配置，不需要 reload；开源版就有自动 HTTPS、主动健康检查、动态上游和完整指标；扩展是内存安全的 |
| Caddy | 默认 HTTPS、严格可程序化的原生配置、原子替换配置的 Admin API、单个静态二进制 | 每连接的内存和 GC 开销；缓存、限流、四层代理内置，而不是交给插件 |
| Traefik | entryPoint → router → middleware → service 的模型、provider、内置运维能力 | 静态文件和缓存；性能和内存；能在一个地方看到最终生效的配置 |

此外，fib 还给了 fit 两项三者都没有的能力：一个端口多种协议（`mux`、`multiproto`），以及内置的
SOCKS 代理。

## 非目标

- **邮件代理。** nginx 的 `mail` 模块如今用户很少，fit 不做。
- **服务网格。** fit 是边缘和网关代理，不是 sidecar，不打算在集群内部替代 Envoy 或 Linkerd。
- **API 管理。** 开发者门户、API 计费、API key 自助申请等属于建在网关之上的工具。
- **兼容 Lua、njs 或 Yaegi 插件。** fit 的运行时扩展是 WASM。
- **完整兼容 nginx 配置。** M5 的转换器只覆盖常见子集，用于降低迁移成本，不追求每条指令。

## 设计原则

以下原则来自对比文档的 [设计建议](comparison.zh-CN.md#设计建议)，每个里程碑都要对照检查。

1. **数据面保持 fib 的模型。** 请求路径上不引入每连接或每请求的 goroutine。必须阻塞的操作（外部认证、
   拉取 JWKS、插件调用、磁盘缓存未命中）放在各自独立的池里，池之间的等待只能单向，避免互相死锁。
2. **控制面与数据面分离。** 配置解析、provider 监听、证书申请、健康探测都在控制面，产出不可变的运行时
   对象；数据面只通过一个原子指针读取它们。
3. **所有协议共用一套概念模型**：listener → router → middleware → service → upstream，HTTP、TCP、UDP
   一致；fib 的 `mux` 让 listener 可以先按协议分流。
4. **默认安全**，参考 Caddy：默认开启 HTTPS、使用现代 TLS 参数、HTTP 自动跳转 HTTPS、限制请求头和请求体
   大小、Admin API 默认只绑定 localhost 或 Unix socket。
5. **性能、静态文件和缓存以 nginx 为标杆。** 每个里程碑都发布与 nginx、Caddy、Traefik 的基准对比；
   性能回退按 bug 处理。
6. **依赖精简。** 核心只依赖 fib 和标准库。只有自研明显更差的地方（ACME、WASM 运行时）才引入第三方库，
   而且要藏在 fit 自己的接口后面。

## 架构

```mermaid
flowchart LR
    subgraph 控制面
        Src["配置来源<br/>文件 · Admin API · provider（Docker、Kubernetes、Consul、etcd）"] --> Ad["适配器<br/>YAML · 友好格式 · nginx 子集 → 原生 JSON"]
        Ad --> V["按 schema 校验"]
        V --> Cp["编译：provision 所有模块"]
        Cp --> G["不可变运行时图 v(n+1)"]
        ACME["证书管理<br/>ACME · 存储 · OCSP"] --> G
        HC["健康检查<br/>主动探测"] --> G
    end
    G -->|"原子替换；监听用 AddBinding / RemoveBinding 做差量"| L
    subgraph 数据面（基于 fib）
        L["listeners<br/>TCP · UDP · TLS · QUIC"] --> Mx["mux<br/>按协议分流"]
        Mx --> R["routers<br/>host · path · header · SNI 匹配"]
        R --> Mw["middlewares<br/>认证 · 限制 · 重写 · 缓存"]
        Mw --> Sv["services<br/>负载均衡 · 加权 · 镜像 · 故障转移"]
        Sv --> U["上游连接池<br/>fib 异步客户端"]
    end
```

一个请求从头到尾都在它开始时所在的那张图上处理。新图只影响替换之后到达的请求和连接；长连接
（WebSocket、gRPC 流、TCP 隧道）留在旧图上，直到自然结束或被可配置的排空超时关闭。

## 里程碑

每个里程碑列出 **范围**、结束它的 **退出标准**，以及依赖的 **fib 能力**。1.0 之前版本号为 0.x，
每个里程碑结束时发布一个版本。

### M0 基础

后续所有里程碑的骨架，以及衡量它的手段。

**范围**

- 仓库结构：`cmd/fit`、核心包；CI 包含 race 检测、lint 和 fuzz。
- 核心抽象：listener、router、matcher、middleware、service、upstream 接口，以及把配置中的名字解析成模块的
  模块注册表。
- 运行时图：由配置编译、通过原子指针替换、对图做引用计数，旧图在最后一个连接结束时释放。
- 原生配置格式：JSON，并发布 JSON Schema；提供 `fit validate` 和 `fit run`。
- 基准测试框架：用等价配置运行 nginx、Caddy、Traefik 和 fit，使用相同的压测工具（wrk、h2load、
  WebSocket 和 HTTP/3 客户端），记录吞吐、延迟分位数、内存和 CPU。

**退出标准**

- `fit run -c fit.json` 能在 HTTP/1.1 和 HTTP/2 上返回固定响应。
- 基准框架产出 nginx、Caddy、Traefik 的基线数据，并提交到仓库。

**fib 能力**：engine、`AddBinding`、HTTP/1.x 和 HTTP/2 服务端。

### M1 反向代理核心

fit 可以作为反向代理和静态文件服务器使用。

**范围**

- **入口**：HTTP/1.1、HTTP/2（TLS 和 h2c）、HTTP/3；从文件加载证书的 TLS；优雅退出。
- **静态文件**，基于 `http.FileCache` 和 sendfile：预压缩文件、Range 请求、索引文件、类似 `try_files` 的
  回退、ETag 和 `Last-Modified`。
- **反向代理**，为 fib 专门编写，不基于 `httputil.ReverseProxy`：
  - 基于 fib 异步 HTTP/1.x、HTTP/2 客户端的上游连接池；
  - 请求体和响应体双向流式转发，两侧背压联动（客户端慢时对上游 `HoldReads`，反之亦然）；
  - 连接、响应头、空闲超时；客户端断开时通过 `c.Context()` 传播取消；
  - 删除 hop-by-hop 头，设置 `X-Forwarded-*` 和 `Forwarded`，支持可信代理列表。
- **负载均衡**：轮询、加权轮询、最少连接。
- **被动健康检查**：在时间窗口内失败 N 次即标记上游不可用，类似 nginx 的 `max_fails` / `fail_timeout`。
- **访问日志**：结构化 JSON，带缓冲，写入不占用事件循环。
- **SIGHUP 重载**：在进程内构建新图并替换，地址不变的监听保持不动。M2 的 Admin API 就绪之前用它过渡。

**退出标准**

- HTTP/2 入口通过 h2spec；代理一致性测试（头、body、trailer、超时、客户端中途断开）在 HTTP/1.1 和
  HTTP/2 上通过。
- 发布静态文件和反向代理的基准数据，并与 M0 基线对比。
- 测量 1 万和 10 万连接下每个空闲连接的内存，与三者对比。

**fib 能力**：HTTP/1.x、HTTP/2、HTTP/3 服务端和客户端，`http.FileCache`、sendfile、
`Connection.HoldReads`、Router。

### M2 动态配置与 TLS

改配置不丢任何东西，HTTPS 无需手工操作。

**范围**

- **Admin API**（REST，默认在 localhost 或 Unix socket 上，可选认证）：加载整份配置、读取、按路径局部修改、
  回滚到历史版本；配置带版本号和 ETag，避免并发写互相覆盖。
- **原子替换**：新配置完整 provision 之后才替换；失败时正在运行的配置保持不变。监听做差量：新地址用
  `AddBinding`，移除的地址用 `ShutdownBinding` 排空。
- **文件 provider**：监听配置文件变化，走与 API 相同的应用路径。
- **自动 HTTPS**：
  - ACME，支持 HTTP-01 和 TLS-ALPN-01 验证，到期前续期，OCSP stapling；
  - 配置中出现的每个域名默认开启 HTTPS，HTTP 自动跳转；
  - 证书存储抽象为接口，先提供文件系统实现；
  - 按需 TLS，用 "ask" 接口和速率限制防止被滥用。
- **主动健康检查**：HTTP 和 TCP 探测，可配置间隔、阈值和随机抖动。
- **更多负载均衡算法**：随机、P2C（二选一）、按 IP/header/cookie/URI 的一致性哈希、基于 cookie 的粘性会话。
- **Prometheus 指标**：按 router、service、upstream 统计；连接和连接池的 gauge；配置重载和证书到期指标。

**退出标准**

- 抖动测试（在流量持续时进行数千次配置替换）不断开任何连接、不失败任何请求。
- 配置里只写一个域名的站点，能从 Let's Encrypt 测试环境拿到证书并提供 HTTPS。
- 停止响应探测的上游在配置的时间窗口内被摘除，恢复后重新加入。

**fib 能力**：`AddBinding`、`RemoveBinding`、`ShutdownBinding`，`fib/tls` 以及在握手池中执行的
`GetCertificate` 回调。

### M3 网关能力

让反向代理成为 API 网关，并支持四层代理。

**范围**

- **匹配器**：host、path（前缀、精确、正则）、method、header、query、来源 IP 和 CIDR，以及组合它们的
  表达式；显式优先级，没有隐式顺序。
- **重写与头处理**：路径重写、去前缀、跳转、请求和响应头修改。
- **限制**：按键限流（令牌桶，单实例本地）、并发限制、请求大小限制。
- **认证**：Basic；ForwardAuth（认证调用异步进行，请求等待期间不占用 worker）；JWT，JWKS 由控制面拉取并
  缓存；mTLS 客户端证书。
- **弹性**：带预算和幂等规则的重试、熔断、按路由的超时。
- **流量拆分**：加权 service、流量镜像、故障转移 service。
- **压缩**：gzip、zstd、brotli，按请求协商，跳过已压缩的类型。
- **WebSocket 代理**：通过 `Context.Upgrade` 和 `Tunnel`，包括 HTTP/2、HTTP/3 入口上的 WebSocket 转到
  HTTP/1.1 上游。
- **gRPC 代理**：h2c 和 TLS 上游、trailer、deadline 透传。
- **四层**：TCP/UDP 代理、不终结 TLS 按 SNI 分流、PROXY protocol v1/v2 收发，以及通过 `mux` 在一个端口上
  按协议路由——HTTP、TLS、gRPC、SOCKS 和自定义协议并存。
- **上游 TLS**：到上游的 mTLS、自定义 CA、SNI 覆盖。

**退出标准**

- 每个中间件都有一致性测试和基准；没有一个在 worker 上执行阻塞调用。
- WebSocket、gRPC（一元和流式）、TCP/UDP 代理通过端到端测试，包括对端很慢时的背压。
- 单个端口同时服务 HTTP/1.1、HTTP/2、按 SNI 透传的 TLS 和 SOCKS。

**fib 能力**：`mux`、`multiproto`、原始 TCP/UDP handler、`Connection.SetHandler`、`Context.Upgrade`、
`Tunnel`、WebSocket 和 gRPC 包、`socks`、`ConnectProxy`，以及作为起点的 compress、cors、limiter 中间件。

### M4 平台集成

像 Traefik 一样融入容器平台。

**范围**

- **Provider**，全部走与文件和 API 相同的编译路径：
  - Docker，标签即配置；
  - Kubernetes Gateway API（HTTPRoute、GRPCRoute、TLSRoute、TCPRoute、UDPRoute），以及为仍在使用的集群
    提供 Ingress；
  - Consul 和 etcd，用于服务目录和 KV 配置。
- **合并规则** 写成文档且可见：哪个来源优先；提供一个只读的 Admin API 接口，展示最终生效的配置以及每部分
  来自哪里。
- **集群**：共享证书存储（Redis、etcd、Consul）并加锁，让多个实例共同申请和续期证书。
- **仪表盘**：只读，展示 router、service、上游健康状态、证书和最近的配置版本。
- **OpenTelemetry**：链路追踪，并把上下文传播到上游；指标除 Prometheus 外也可通过 OTLP 导出。
- **打包**：容器镜像、Helm chart、systemd 单元。

**退出标准**

- 通过 Kubernetes Gateway API 一致性测试中 fit 声明支持的核心 profile。
- 一个 Docker Compose 示例能按标签路由服务，并随服务启停自动更新。

**fib 能力**：无新增，本里程碑是控制面的工作。

### M5 缓存与扩展

补上 nginx 仍领先于 Go 实现的部分，并确定扩展机制。

**范围**

- **响应缓存**：
  - HTTP 缓存语义（RFC 9111）：`Cache-Control`、`Vary`、校验器、`Age`；
  - 内存和磁盘两级，磁盘 I/O 不占用事件循环；
  - 缓存锁：一次未命中只回源一次，其他请求等待结果；
  - stale-while-revalidate 和 stale-if-error；
  - 通过 Admin API 按 key 和 tag 清除。
- **编译期 Go 模块**：类似 xcaddy 的 `fit build` 命令，用于性能敏感的 handler。
- **WASM 插件**：运行时加载、沙箱隔离、在独立的池中运行；使用纯 Go 运行时（如 wazero）保持静态二进制；
  评估 Proxy-Wasm ABI，以便复用现有的过滤器。
- **配置适配器**：一种人类友好的配置格式；nginx 常见配置子集的转换器，并报告无法转换的部分。
- **FastCGI**，方便 PHP 站点从 nginx 或 Caddy 迁移。

**退出标准**

- 在基准框架的命中率场景下，缓存表现与 nginx `proxy_cache` 相当；在击穿测试（大量客户端请求同一个冷 key）
  中只回源一次。
- 一个修改请求头的 WASM 插件，每请求增加的开销有上限且经过测量。
- nginx 转换器能处理测试集中的示例配置，并列出每一条跳过的指令。

**fib 能力**：`sidepool` / `streampool` 一类处理阻塞工作的池，`http.FileCache` 作为内存层的参考。

### 1.0

1.0 是对稳定性的承诺，而不是功能清单。

- 原生配置 schema 和 Admin API 定为 v1；此后的破坏性变更需要新的主版本和迁移路径。
- 对解析器、代理和配置编译器持续进行 fuzz。
- 对 TLS 处理、Admin API 和插件沙箱进行外部安全审计。
- 文档：快速上手、完整的配置参考，以及从 nginx、Caddy、Traefik 迁移的指南。

## 功能对照

各行与对比文档中的 [功能对比](comparison.zh-CN.md#功能对比) 表对应。

| 功能 | 里程碑 | 依赖的 fib 能力 |
| --- | --- | --- |
| HTTP/1.1、HTTP/2 | M1 | HTTP/1.x、HTTP/2 服务端 |
| HTTP/3（QUIC） | M1 | `http3` |
| h2c 上游 / gRPC 代理 | M1（h2c）、M3（gRPC） | HTTP/2 客户端、`grpc` |
| WebSocket 代理 | M3 | `Context.Upgrade`、`Tunnel`、`websocket` |
| TCP/UDP 四层代理 | M3 | 原始 TCP/UDP handler、`HoldReads` |
| 按 SNI 分流 TLS | M3 | `mux`、`fib/tls` |
| 一个端口多种协议 | M3 | `mux`、`multiproto` |
| SOCKS、CONNECT 正向代理 | M3 | `socks`、`ConnectProxy` |
| 静态文件 | M1 | `http.FileCache`、sendfile |
| FastCGI | M5 | — |
| 负载均衡：轮询、加权、最少连接 | M1 | — |
| 负载均衡：随机、P2C、哈希、粘性会话 | M2 | — |
| 主动健康检查 | M2 | 异步客户端 |
| 被动健康检查 | M1 | — |
| 动态上游 | M2（API）、M4（provider） | — |
| 响应缓存 | M5 | — |
| 限流 / 并发限制 | M3 | `limiter` 中间件 |
| 认证：Basic、ForwardAuth、JWT、mTLS | M3 | `fib/tls` |
| 重写 / 跳转 | M3 | Router |
| 压缩：gzip、zstd、brotli | M3 | `compress` 中间件 |
| 自动 HTTPS（ACME）、按需 TLS | M2 | 在握手池中执行的 `GetCertificate` |
| 无中断改配置 | M1（SIGHUP）、M2（Admin API） | `AddBinding`、`RemoveBinding`、`ShutdownBinding` |
| Kubernetes Gateway API、Ingress | M4 | — |
| Docker 服务发现 | M4 | — |
| Prometheus 指标 | M2 | — |
| OpenTelemetry 追踪 | M4 | — |
| 仪表盘 | M4 | — |
| 结构化日志 | M1 | — |
| 扩展：Go 模块、WASM | M5 | — |
| nginx 配置转换器 | M5 | — |

## 性能目标

性能是 fit 选择 fib 的原因，所以从 M0 起就持续跟踪，而不是最后再调优。具体数值在 M0 于同一硬件上得到基线后
再确定；下表说明 fit 相对其他三者必须达到的位置。

| 指标 | 目标 |
| --- | --- |
| 每个空闲连接的内存（HTTP/1.1 keep-alive、TLS、WebSocket） | 接近 nginx；明显低于 Caddy 和 Traefik |
| 静态文件吞吐 | 不低于 Caddy；向 nginx 看齐 |
| 反向代理吞吐 | 高于 Caddy 和 Traefik |
| 负载下的 p99 延迟 | 不差于 Caddy 和 Traefik；配置替换期间保持平稳 |
| GC 停顿和每请求分配 | 每个版本都测量；增长需要说明理由 |
| 一次配置替换的开销 | 与当前打开的连接数无关 |

基准框架在每次发布以及每次数据面改动时在 CI 中运行，结果连同所用硬件和配置一起发布在仓库中。

## 风险与待定问题

- **ACME 库。** CertMagic 最完整，但带着 Caddy 的设计假设；lego 只是客户端；自研能保持依赖精简，但要做对
  工作量很大。在 M2 决定，并放在 fit 自己的证书管理接口之后，以便日后更换。
- **配置格式。** 原生格式是 JSON；人类友好格式是 YAML、类似 Caddyfile 的语法还是别的，到 M5 之前都待定。
  但 schema 必须早于那时稳定，因为 provider 和 Admin API 都依赖它。
- **fib 的缺口。** fib 不支持 `http.Hijacker`，`Request.Context()` 也不会在客户端断开时取消，所以标准库的代理
  和部分中间件无法直接复用。fit 用 fib 自己的 API 绕开这些限制；fib 缺少的能力在 fib 中补，而不是在 fit 里
  打补丁。
- **会阻塞的扩展点。** ForwardAuth、JWKS、WASM 和磁盘缓存都需要阻塞。每一个都需要独立的池和超时，池之间需要
  固定的等待顺序。做错了会在高负载下损失吞吐甚至死锁，所以每一个都配一个把它压满的负载测试。
- **Gateway API 一致性。** 测试集很大，并随 Gateway API 每个版本变化；M4 先声明一个较窄的 profile，再逐步扩大。
- **WASM 的开销。** 跨越 WASM 边界的调用和内存拷贝可能让插件在热路径上太慢。M5 先测量，再决定是否采用
  Proxy-Wasm ABI。
- **分布式状态。** 本路线图中限流和缓存都是单实例本地的。跨集群共享（Redis 或 gossip 协议）放到 1.0 之后，
  除非用户更早需要。
