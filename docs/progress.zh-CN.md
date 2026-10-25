# fit 开发进度

[English](progress.md) | [简体中文](progress.zh-CN.md)

本文件按迭代记录实际完成的内容，以及下一次会话从哪里继续。[开发计划](dev-plan.zh-CN.md) 说明每个迭代应该做
什么；本文件说明实际做了什么，以及与计划不同的地方。

## 当前状态

| | |
| --- | --- |
| 最后更新 | 2026-10-25 |
| 里程碑 | M0 基础：已完成（I01–I08）。M1 代理核心：已完成（I09–I23）；其审查修复 R01–R07f 已全部完成。M2 动态配置与 TLS：已完成（I24–I35，含 I30b 和 I30c）。M3 网关功能：已完成（I36–I55，含 I53b）。M4 平台集成：已完成（I56–I66）。M5 缓存与扩展：已完成（I67–I75）。1.0：迭代已完成（I76–I78）；此后的 I79 增加了 Caddyfile 转换器和带注释的 JSON，I80 增加了 fit 自己的 .konf 格式和多文件配置，I81 增加了 Proxy-Wasm 的响应回调 |
| fib | fib `main` 的 `dbe5c274`（`v1.6.1-0.20261025150053-dbe5c27424ec`）：在 `b0809b4` 之上加了 A46（在别处完成工作的响应体过滤器，为 I81 而加）以及自 `b0809b4` 起的 HTTP/2、HTTP/3 和 TLS 改进；`b0809b4` 本身是 v1.6.0 加上 A42（HTTP/3 服务端把一轮的数据报一次批量发出，`a34c5db`）、A43（TLS 上的 h2 把 cork 期间的一轮合成一个 record 加密，`5a02929`）和 A43 测试的一处修复；按 commit 而不是 tag 依赖，见规则 19。此前为 `github.com/lesismal/fib v1.6.0`（`55e4140`，第一个带有 fit 自 v1.5.6 以来所需改动的 tag，并带有 fib 的 TLS 修复：会话恢复时重新验证对端，并要求 Go 1.27.3；此前为 fib `main` 的 `fcb1718`：`86071b8` 加上为 I67 实现的 A40、为 I77 实现的修复：请求体仍在到达时发出的完整响应声明 Connection: close，以及两个偶发失败测试的修复：`benchmark/rpc` 的 `TestClientCountsFailures` 和 `http` 的 `TestBodyWriterHoldsNoGoroutine`；此前为 fib `main` 的 `86071b8`，即 `4f1c3e9` 加上 fib 的 prefork 与 eBPF reuseport 均衡 `5fc8250`、`aa0ba7e`，以及 A38；按 commit 而非 tag 依赖，见规则 19）；自 I49 和 I50 起带 A32 和 A33，自 I51 和 I52 起带 A34 和 A35，自 I53 和 I53b 起带 A36 和 A37，自 I66 的 CI 修复起带 A38，自 v1.0.0 报告起带 A42 和 A43 |
| 可用的功能 | `fit run` 通过 HTTP/1.1、h2c、TLS（HTTP/1.1 和 h2，证书来自文件（文件变化时重新加载）或经 ACME 的 HTTP-01 或 TLS-ALPN-01 获取并续期（保存在目录中，或供多台主机上的实例共享每张证书和质询而保存在 Redis、etcd 或 Consul 中），或在握手时按需获取，按 SNI 选择，并 staple OCSP 响应；只写了域名的配置默认使用 HTTPS 并跳转）和 HTTP/3 返回固定响应和静态文件，并能代理到 HTTP/1.1、h2c 和 h2 上游（可为上游指定 CA、服务器名和客户端证书）（带被动和主动的 HTTP、TCP 健康检查，九种负载均衡策略：轮询、加权轮询、最少连接、随机、P2C、按 IP、URI、请求头或 cookie 的一致性哈希，以及粘性 cookie），写 JSON 访问日志，收到 SIGHUP、文件变化（`-watch`）或通过 Admin API（读取、加载、按 JSON pointer 和 merge patch 修改、用 ETag 防止丢失更新、回滚到保存的版本）重载配置，用 W3C trace context 追踪请求并传给上游，通过 OTLP/HTTP 导出 span 和指标，按 RFC 9111 的规则把响应保存在 HTTP 缓存里，内存和磁盘两层，重启后仍能找回（同一 zone 的路由共享，reload 后保留），多个客户端只获取一次，刷新期间或上游失败时以陈旧响应回答，并可通过 Admin API 按 URL 或标签清除，在 Admin API 上提供 Prometheus 指标和只读仪表盘（路由、服务、上游及其健康状态、证书和配置版本），按写明的优先级规则把 provider 提供的内容合并进配置，并展示生效配置及其每部分的来源（`GET /config/effective`），从 Docker 容器的标签获取路由、上游和服务并随容器启停跟进，从 Consul 服务目录中服务的 tag 获取上游、路由和服务并跟进其健康检查，从 Consul KV 存储和 etcd 的键获取 listener、路由、上游和服务，服务 Kubernetes 集群中属于其 GatewayClass 的 Gateway（Gateway API），含 HTTP、HTTPS（证书来自 Secret）、TLS 透传、TCP 和 UDP listener，以及它们的 HTTPRoute、GRPCRoute、TLSRoute、TCPRoute 和 UDPRoute，并回写它们的状态，以及属于 fit 的 IngressClass 的 Ingress 和少量注解，并能优雅退出；路由按 host、path、方法、请求头、查询参数、远端 IP 和 path 正则匹配，或用组合它们的表达式匹配，依次看优先级、具体程度和设置的匹配器个数；中间件可以重写 path、跳转，以及设置、添加和删除请求和响应的字段，并能限制路由的请求速率（按客户端 IP、头部或路由）、同时处理的请求数和请求体大小，并能要求用户名和密码（Basic 认证，bcrypt）或客户端证书（TLS 监听器可以要求客户端证书，或在客户端提供时验证，即 mTLS），询问认证服务（ForwardAuth），或用来自文件或 JWK 集合的密钥验证 JSON Web Token；代理在预算内重试，并用超时限制路由的请求；熔断器在路由的请求持续失败时停止转发；代理按权重把请求拆分到多个上游，把一部分镜像到另一个上游，并能故障转移到备用上游；响应在写出时用 zstd、brotli 或 gzip 压缩；能把 HTTP/1.1、h2 和 h3 入口的 WebSocket 代理到 HTTP/1.1 上游，把 gRPC 调用双向流式代理到 h2 和 h2c 上游，代理处失败时回应 gRPC 状态；tcp 监听器把连接逐字节代理到上游，udp 监听器按客户端会话代理数据报，TLS 连接按 SNI 分流且不终止，受信任代理的 PROXY protocol 被接收（HTTP 监听器也可以）并可发送给上游（services）；一个端口同时服务 HTTP、TLS（终止，或按名字透传）、SOCKS 和原始 TCP，按客户端先发的内容区分；带客户端与目标访问规则的 SOCKS（CONNECT）和 HTTP CONNECT 正向代理；`fit validate`、`fit schema`、`fit version`、`fit hash-password`；fit 打包为容器镜像（linux/amd64 和 arm64，打标签时发布）、把它作为 Gateway API 与 Ingress 控制器运行的 Helm chart，以及 systemd unit，并附有 Docker Compose 和 Kubernetes 示例；基准框架可以跑 nginx、Caddy、Traefik 和 fit |
| 尚不可用 | 1.0 的外部安全审计，在代码之外安排 |

**M0 已完成。** 退出标准都已满足，但有一点说明：`fit run -c fit.json` 通过 HTTP/1.1 和 HTTP/2（h2c；
基于 TLS 的 HTTP/2 在 I09）返回固定响应；nginx、Caddy、Traefik 的基线已提交，但它们是在 Mac 上的
Docker Desktop 里测的，不是在 Linux 主机上，见 I08。R07b 在 Linux 主机上测了 M0 的 fit：固定响应吞吐与现在相差不到
1.3%（HTTP/1.1）和 4.2%（h2c）。

**M1 已完成。** I23 的一致性测试和基准见 [M1 报告](https://github.com/lesismal/fit/blob/3658ae9/bench/results/m1-report-2026-10-19.md)：h2spec 全部通过；
在 Linux 主机上，fit 满足路线图中 M1 的所有目标，代理吞吐高于 Caddy 和 Traefik，但在 HTTP/1.1 上是 nginx 的 83%。

**M2 已完成。** 它的三个退出测试在 CI 中以单独的任务通过（I35）；CI 的完整竞态测试任务已恢复通过：`TestStuckClientWithoutStallTimeout` 改为按进展等待，而不是固定时间（见下文）。基准见 [M2 报告](https://github.com/lesismal/fit/blob/3658ae9/bench/results/m2-report-2026-10-20.md)：fit 仍满足 M1 的所有目标，静态文件吞吐是 M1 时的 1.3 到 1.8 倍，代理在 HTTP/1.1 上是 nginx 的 86%（M1 时 83%），在 h2c 上是 94%。**M3 已完成。** 它的退出测试是 `make m3-exit` 和 CI 中单独的任务（I55）：中间件和 handler 都不在 worker 上等待（`test/workers`），
每个都有基准测试；WebSocket、gRPC 和 TCP/UDP 代理在对端慢时压住快的一侧；一个端口同时服务 HTTP/1.1、HTTP/2、按 SNI 透传的 TLS 和
SOCKS。基准见 [M3 报告](https://github.com/lesismal/fit/blob/3658ae9/bench/results/m3-report-2026-10-22.md)：fit 仍满足 M1 的所有目标，代理在 HTTP/1.1 上是 nginx 的 94%
（M2 时 86%），在 h2c 上是 95%，一个空闲 WebSocket 占 5.5 KiB，nginx 为 17.8。**M4 中，I56（provider 框架与生效配置）、I57（Docker provider）、I58（Gateway API 的 HTTPRoute）、I59（其他路由、HTTPS listener 与状态）、I60（Kubernetes Ingress）、I61（Gateway API 一致性测试，fit 通过其 HTTP profile 的 core）、I62（Consul 与 etcd provider）、I63（证书共享于 Redis、etcd 或 Consul）、I64（只读仪表盘）I65（OpenTelemetry：通过 OTLP 导出追踪和指标）和 I66（打包与示例）已完成，M4 的退出标准已满足：fit 通过 Gateway API 一致性测试 HTTP profile 的 core（I61），Docker Compose 示例按 label 路由，并随容器启动和停止而更新（I66）。** **M4 已完成。** 基准结果见 [M4 报告](https://github.com/lesismal/fit/blob/3658ae9/bench/results/m4-report-2026-10-23.md)：fit 仍满足 M1 的全部目标；数据面与 M3 相当，但 h2c 例外：M3 与 M4 构建的 A/B 对比显示 fit 慢 1.5% 到 1.8%，是在 I59–I64 间逐步累积的，请求路径并无改动（见下方记录）；10k 空闲连接每个多占 0.1 到 0.5 KiB。**M5 中，I67（HTTP 缓存的语义与内存层）、I68（其磁盘层）和 I69（锁、陈旧应答与清除）已完成：雪崩只获取一次，在基准工具的缓存场景中，fit 保存的内容与 nginx 的 `proxy_cache` 相同，HTTP/1.1 上速率约为其两倍。I70 的 `fit build` 可以构建带有其他模块、或不带 provider 的 fit。I71 在 wazero 中运行 WebAssembly 插件，插件在自己的池上运行；I72 支持 Proxy-Wasm 过滤器的请求阶段；I73 接受 YAML 配置，I74 转换 nginx 配置；I75 通过 FastCGI 运行 PHP。** **M5 已完成。** 基准结果见 [M5 报告](https://github.com/lesismal/fit/blob/3658ae9/bench/results/m5-report-2026-10-23.md)：fit 仍满足 M1 的全部目标；M4 与 M5 构建的 A/B 对比中所有场景都在噪声以内（最多 0.7%）；缓存的命中与 nginx 的 `proxy_cache` 相同，速率在 HTTP/1.1 上是其 2.1 倍、h2c 上是 5.4 倍；空闲连接的开销与 M4 相同。**1.0 的迭代已完成**：I76 在 CI 中（也每天）对十一个目标做 fuzz，语料已提交；I77 把配置和 Admin API 冻结为 v1，附 [兼容性策略](compatibility.zh-CN.md)、schema 版本，以及六个重命名字段的弃用旧名；I78 增加了 [快速上手](getting-started.zh-CN.md)、由代码生成的 [配置参考](reference/config.md)，以及从 nginx、Caddy 和 Traefik 迁移的指南。1.0 尚需的是代码之外的外部安全审计，以及 fib 的 `main` 合入 fit 所依赖的分支。 M2 已完成 I24、I25（带配置版本、局部
修改、ETag 和回滚的 Admin API）、I26（旧图排空与连接池共享）、I27（文件 provider）、I28（证书管理器、存储和 ACME 选型）、I29（经 ACME 的 HTTP-01 获取并续期证书）、I30（TLS-ALPN-01 和 OCSP stapling）、I30b（默认 HTTPS）、I30c（按 ARI 续期）、I31（按需 TLS）、I32（主动健康检查）、I33（负载
均衡 v2）、I34（Prometheus 指标）、I35（退出测试）、I35b（监视证书文件）和 I35c（丢弃闲置的按需名字）。R07f 剖析了代理每个请求的 CPU：余下可省的在 fib 中，已作为 A20–A22 提出。M2 之前的审查修复 R01、R02 已
完成，热路径工作中不需要 fib 的 R07a、R07b 也已完成；R03–R06 也已完成：fib 提供了这些 API，代理的下载、静态 handler（自 R05b 起包括 sendfile）和这些上限都已使用它们，R07c 也借助 fib 降低了热路径开销。M1 的审查修复已全部完成。

**按规则审查（2026-10-19）。** 已按 [开发规则](rules.zh-CN.md) 审查 M0、M1。代理的下载路径和静态 handler 会在 fib
worker 上无时间上限地等待（已复现：一个不读取的客户端会卡住共享同一 h2c 上游连接的所有请求），还有若干上限没有安全的
默认值。修复见 [开发计划](dev-plan.zh-CN.md#m1-审查修复) 中的 R01–R07；fit 需要 fib 提供的 API 见
[fib-api-requests](fib-api-requests.zh-CN.md)。**R01、R02 只改 fit，应在 M2 之前完成**，即在 I24、I32 之前。

可以用下面的话开始下一次会话：

> 实现 docs/dev-plan.zh-CN.md 中的迭代 I62。先读 docs/progress.zh-CN.md、docs/rules.zh-CN.md、docs/dev-plan.zh-CN.md
> 的索引和该迭代的小节，以及 docs/roadmap.zh-CN.md 中的设计原则，然后只读它列出的包。改动限制在该迭代范围内，补上
> 测试，完成后在两个 dev-plan 文件中标记为 done，并记录到两个 progress 文件中。

## 已有的代码

| 包 | 内容 |
| --- | --- |
| `adapter`、`adapter/yaml`、`adapter/nginx`、`adapter/caddyfile` | 把其他格式转换成原生 JSON 的适配器，按名称、扩展名或文件名选择，错误映射回源文件；YAML 双向转换；nginx 配置和 Caddy 的 Caddyfile 的常用子集，未转换的部分按行报告 |
| `cmd/fit` | fit 的 main 包：导入所有模块和 provider 后调用 `cli.Main` |
| `cli` | 命令行：`run`（收到 SIGHUP、通过 Admin API 或在 `-watch` 下文件变化时重载，并按已注册的种类运行配置中的 provider）、`validate`、`adapt`、`schema`、`build`、`version`、`hash-password`、`help` |
| `plugin/build` | `Build`：生成并用 go 命令构建一个带更多或更少模块的 fit 的 main 模块；`ParseModule`、`Imports`、`MainSource` |
| `admin` | `Configs`：每个新配置到达运行中服务器的唯一路径（加载、编译、`listener.Server.Reload`），一次一个，并保存已应用配置的历史，每个都有 ETag；`Update` 在同一把锁下基于运行中的配置做修改，支持 If-Match；`Rollback`；基于 net/http 的 Admin API：`GET/PUT/PATCH/DELETE /config[/<pointer>]`、`POST /load`、`POST /rollback/{n}`、`GET /versions`、`GET /versions/{n}`、`GET /metrics`（由 `WriteMetrics` 写出）；`AddProvider` 和 `Provide`，每次应用都把 provider 的片段合并进基础配置；`GET /config/effective`；`GET /status`（listener、连接、上游及后端健康状态、证书）；`GET /cache` 与 `POST /cache/purge`；嵌入的仪表盘，位于 `/dashboard/` |
| `config` | 原生配置类型（包括 `providers`）；带错误路径和位置的严格 JSON 解析器与解码器，接受重命名字段的旧名字并给出警告（`Config.Warnings`）；`SchemaVersion`；`Value`；`Duration`；语义检查 |
| `module` | 模块注册表；`Handler`、`Middleware`、`Matcher` 接口，以及暂定的 `Service`、`Upstream` 接口；`Provisioner`、`Cleaner`；`Env`，把配置中的上游连接池交给 `Provision` |
| `handler/respond` | `respond` handler |
| `handler/forward` | `forward_proxy` handler：HTTP/1.1、HTTP/2 和 HTTP/3 上的 CONNECT，即带 `l4.Rules` 的 fib `ConnectProxy` |
| `handler/proxy` | `proxy` handler：转发到具名上游，去掉逐跳头，按可信代理列表设置 `X-Forwarded-*` 和 `Forwarded`；两个方向都不等待地流式转发，上传由 `HoldBody` 和 `OnDrain` 控制节奏，下载由 `WriteNoWait` 和 `OnWritable` 控制；WebSocket 与上游的 tunnel 对接；gRPC 调用带 `TE: trailers`，把 `grpc-timeout` 作为期限，失败时回应 gRPC 状态 |
| `middleware/rewrite` | `rewrite` 中间件（去前缀、正则、加前缀）和 `redirect` |
| `middleware/limit` | `rate_limit`（按客户端 IP、头部或路由的令牌桶，键数有上限）、`concurrency_limit`、`body_limit` |
| `middleware/auth` | `jwt`（用标准库验证 JWS，密钥来自 PEM 文件、密钥文件或由控制面获取的 JWK 集合，保存已验证的令牌）；`forward_auth`（经 fib 异步客户端向认证服务发送子请求）；`basic_auth`（bcrypt 哈希在单独的有界池上校验，通过的密码以带密钥的 SHA-256 记住）和 `client_cert`（要求或可选经验证的客户端证书，并传递其主体）；`UserOf` |
| `middleware/breaker` | `circuit_breaker`：按路由的错误率和慢响应率在关闭、打开、半开之间切换 |
| `middleware/cache` | `cache`：按 RFC 9111 实现的共享 HTTP 缓存，响应经响应体过滤器随发送保存，以 `Age` 应答，按 `Vary` 区分变体，304，请求指令，失效，`Cache-Status`；内存层，每个 zone 16 个分片，各有 LRU，reload 后保留；磁盘层，每个响应一个文件，索引在内存中，读写在自己的池上进行，启动时读回；缓存锁，经自己的流发出的缓存自身请求，stale-while-revalidate 与 stale-if-error，按 URL、标签或全部清除（`Purge`、`Zones`） |
| `plugin/wasm` | `wasm`：wazero 中的 WebAssembly 插件，在带队列和看门狗的实例池上运行，fit 自己的字段与应答 ABI，以及 Proxy-Wasm 的请求阶段；`guest`：fit ABI 的 wasip1 Go 端 |
| `middleware/compress` | `compress`：经 fib 响应体过滤器的 zstd、brotli 和 gzip，流式，编码器池化且有上限 |
| `middleware/headers` | `headers` 中间件：删除、设置和添加请求与响应的字段 |
| `handler/fastcgi` | `fastcgi`：fit 自己的 FastCGI 客户端，基于 fib 连接，有连接池和上限，双向流式；PHP 参数、split path info、try_files |
| `handler/static` | `static` handler：根目录下的文件，缓存路径解析结果和小文件内容，所有文件操作都在有界的文件池上，大文件经 `SendFile` 发送；校验器，条件请求和 Range 请求，预压缩副本，`try_files`，目录列表 |
| `modules/standard` | 引入 fit 自带的所有模块 |
| `runtime` | `Compile(config, Env)` 把配置编译成不可变的 `Graph`，每个 listener 一个路由器、每个上游一个连接池，未变的上游共享前一个图的连接池；带原子替换、图引用计数和旧图排空超时的 `Runtime` |
| `listener` | 在一个 fib engine 上绑定图中的所有 listener（TCP，以及 HTTP/3 的 UDP）；把服务 HTTP、tcp、socks 中多种协议的监听器放在 fib 的 `mux` 之后；把每个请求分发到当前图的路由；`Reload` 到新图，保留未变的 listener，排空被移除的；优雅退出 |
| `l4` | 四层代理：`Service` 把 tcp 监听器的连接代理到其上游的一个地址，经 fib 引擎拨号，每一侧的数据在其回调中发往另一侧，另一侧未发送超过 64 KB 时暂停这一侧的读取；空闲超时、`max_conns`，随所属 graph 一起 drain；`Clients`：服务接手连接后监听器处理器的回调；`Entry`：监听器按 SNI 选择的服务，没有服务认领的 TLS 连接在监听器有 tls 时交给它自己的 TLS（`HandOff`）；`Rules`：谁可以使用正向代理、可以连到哪里，主机名在连上后按解析出的地址判断；`NewSOCKS`：按这些规则配置的 fib SOCKS 服务器 |
| `upstream` | `Pool`：按上游的粘性 cookie 和策略（轮询、平滑加权轮询、最少连接、随机、P2C、按客户端 IP、URI、请求头或 cookie 的一致性哈希）选择后端，通过 fib 的异步客户端以 HTTP/1.1、h2c 或 h2 发送请求，关闭前先排空；被动健康检查（`max_fails`）和主动健康检查：HTTP、TCP 探测由定时器发起，经 fib 的异步客户端和拨号器发出；`StartEngine` 启动连接池拨号用的 engine |
| `provider` | `Provider`：发送片段的配置来源；`RegisterKind` 和 `LookupKind`：二进制文件拥有的 provider 种类，由 `provider/standard` 导入；`Run`：运行 provider，并一次一个地应用它们最新的片段；`Parse` 和 `Merge`：按决策 009 的规则把片段合并进基础配置，记录每个元素的来源和被遮蔽的内容，并把错误映射回写下它的地方 |
| `provider/docker` | Docker provider：用普通 HTTP 访问 Engine API（事件，然后列出容器），把容器标签转换为上游、路由和服务，副本共享上游 |
| `provider/labels` | 从前缀下的标签读取路由、服务和上游设置（Docker 的标签和 Consul 的 tag 都这样读），并生成片段 |
| `provider/consul` | 基于 net/http 和阻塞查询的 Consul provider：`Catalog` 把为 fit 打了 tag 的服务转换为其通过检查的实例组成的上游，以及 tag 写出的路由和服务；`KV` 从前缀下的键获取片段 |
| `provider/etcd` | etcd provider：etcd 的 v3 JSON 网关，先 watch 前缀再读取键，从键获取片段，支持用户认证，端点轮流尝试 |
| `provider/internal/watch`、`provider/internal/kvpart` | `Follower`：保持 watch、在变化后和每个间隔读取来源并退避重试的循环；由保存片段的键生成片段 |
| `provider/kubernetes` | Kubernetes provider：基于 net/http 的自有客户端（service account 或 kubeconfig），每种资源一个 list-and-watch informer（使用服务器提供的第一个版本），把 Gateway API 的 GatewayClass 和 Gateway（HTTP、证书来自 Secret 并写成文件的 HTTPS、TLS 透传、TCP、UDP）及其 HTTPRoute、GRPCRoute、TLSRoute、TCPRoute 和 UDPRoute 转换为 listener、路由、service 和以 Service endpoint 为地址的上游；状态以 merge patch 回写；设置 `ingress` 时还有属于 fit 的 IngressClass 的 Ingress 和少量注解 |
| `provider/file` | `Watcher`：轮询配置文件，在文件不再变化且内容与上次应用的不同时应用它 |
| `observe` | 访问日志：每个请求一行 JSON，缓冲在各有一把锁的分片中，由单独的 goroutine 写出，宁可丢行也不阻塞；`Track` 和 `EntryOf` 取得请求的日志条目；指标：`Histogram`、按 listener 和路由的 `RequestMetrics`（分片的原子计数器，在有图使用时保存在 `Metrics` 中），以及写 Prometheus 文本格式的 `TextWriter`；`Tracer`：W3C trace context 与采样，server span 写成 OTLP JSON 放入有界分片，经 OTLP/HTTP 导出；`MetricWriter`、`OTLPWriter` 和 `MetricsExporter`：通过 OTLP 导出指标 |
| `tlsmgr/sharedstore` | `Store`：保存在 Redis（RESP2）、etcd（JSON 网关）或 Consul（KV）中的 `tlsmgr.Storage`，带持有期间续期的租约锁 |
| `internal/apiclient` | 访问服务 API 的 HTTP 客户端（带 TLS 文件）和有界读取，供 provider 和共享存储使用 |
| `tlsmgr` | `Manager`：在 `GetCertificate` 后面为握手选择证书；`ClientAuth`：监听器验证客户端证书所用的 CA；`Static`：来自文件、按名字索引的证书；`ServerConfig`：listener 的 TLS 配置，TLS 1.2+、AEAD 密码套件、按协议设置 ALPN；`Storage` 与 `FileStorage`：保存 ACME 的数据，带实例间的锁；`ACME`：在自己的 goroutine 上经 HTTP-01 或 TLS-ALPN-01 获取证书并续期；`OCSP`：保存并 staple OCSP 响应；按需 TLS（`ACME.OnDemand`），带 ask URL 和限制；`Certs`：listener 的 Manager，组合文件证书和经 ACME 获取的名字 |
| `router` | `Router[T]`：先用 map 按 host（精确、匹配一级标签的 `*.`、匹配一级或多级的 `**.`）和 path 找出路由，再运行其匹配器（`Methods`、`Headers`、`Query`、`RemoteIP`、`PathRegexp`，以及它们的表达式 `ParseExpr`），依次看优先级、具体程度和匹配器个数，不分配内存 |
| `schema` | JSON Schema 生成，带 schema 版本，并把重命名字段的旧名字标为 deprecated；`fit.schema.json`，由测试保证最新；`Reference`，Markdown 格式的配置参考 `docs/reference/config.md` |
| `docs` | 文档；`TestLinks` 检查仓库 Markdown 中的每个相对链接 |
| `internal/certtest` | 为测试生成自签名证书 |
| `internal/pebbletest` | 为测试运行 ACME 测试服务器 Pebble 及其 DNS 服务器；没有二进制文件时跳过测试 |
| `internal/filepool` | `Pool`：为文件操作专设的 goroutine，以 worker 数和队列为界，超时的任务过期：static 处理器和缓存磁盘层共用此实现 |
| `internal/fibtest` | 在测试中不经网络、像 fib 的服务器一样运行 fib handler，并等待之后才应答的 handler：`Serve`、`ServeRequest` |
| `test/e2e` | 编译 fit 二进制，经网络驱动 `fit run` |
| `bench` | 基准框架：`cmd/fitbench`、`cmd/idleconn`、四个服务器的配置、结果 |
| `deploy` | `Dockerfile`（在仓库根目录）：容器镜像；`deploy/helm/fit`：Helm chart；`deploy/systemd`：`fit.service`、`fit@.service`、sysusers 文件及其测试 |
| `examples` | `hello.json`、`proxy.json`；`docker-compose` 和 `kubernetes`，各带一个由 CI 运行的 `test.sh`；`module/hello`，用 `fit build` 编译进来的独立模块 |

目前的配置形如：

```json
{
  "listeners": [
    {"name": "web", "address": ":8080", "protocols": ["http/1.1", "h2c"]}
  ],
  "routes": [
    {
      "name": "hello",
      "listeners": ["web"],
      "priority": 10,
      "match": {"host": ["example.com"], "path": ["/hello", "/api/*"]},
      "middleware": [],
      "handler": {"module": "respond", "params": {"status": 200, "body": "hello\n"}}
    }
  ]
}
```

## 迭代记录

### I01 仓库骨架与 CI — 已完成

- `github.com/lesismal/fit` 的 `go.mod`，Go 1.27（与 fib 一致），依赖 fib v1.2.0。
- `cmd/fit` 的 `version` 命令：打印 fit 的版本（可用 `-ldflags "-X main.version=..."` 设置，否则使用 Go 记录的
  版本）、从构建信息读取的 fib 版本，以及 Go 版本和平台。
- `Makefile`：`build`、`test`、`race`、`vet`、`lint`、`fmt`、`fmt-check`、`schema`、`fuzz`，以及 `check`
  （fmt-check、vet、staticcheck、race 测试）。
- `.github/workflows/ci.yml`：gofmt；针对 linux、darwin、windows 的 go vet 和 staticcheck；在 ubuntu 和 macOS
  上开启 race 检测的测试；`FuzzLoad` 运行 30 秒。
- `README.md` 和 `README.zh-CN.md`。

与计划的差异：包在迭代填充时才创建，而不是预先建好，因为 Go 中空包没有意义。CI 还没有运行过：首次推送时运行。

### I02 配置类型 v0 与严格加载 — 已完成

- 类型：`Config`、`Listener`（name、address、protocols）、`Route`（name、listeners、priority、match、
  middleware、handler）、`Match`（host、path）、`ModuleRef`（module、params）。
- fit 自己的解码器，见 [决定 001](decisions/001-strict-config-decoding.zh-CN.md)：未知字段、类型错误、缺少必填
  字段、超出范围的数字、不在枚举中的值、重复的键都是错误，全部报告（不止第一个），并带路径和 行:列。只差大小写的
  字段名会提示 "did you mean"。
- `Config.Validate`：listener 和 route 名称唯一、地址合法（允许端口 0，供测试使用）、两个 listener 不能用同一地址、
  协议不能重复、route 引用的 listener 存在、host 合法（精确、`*.` 通配或 IP）、path 合法（以 `/` 开头，`*` 只能在
  末尾）。
- `Config` 保存原样写入的内容，不填默认值，可以序列化回同样的 JSON；params 保持成员顺序。
- 测试：覆盖每种错误的表驱动测试、往返测试，以及 `FuzzLoad`（运行了四百万个输入，没有失败）。

与计划的差异：route 有 `middleware`（列表）和 `handler`（一个模块），而不是单一的 handler 链；这符合路线图中
router → middleware → handler 的模型，也让每个模块所属的命名空间一目了然。协议目前接受 `http/1.1` 和 `h2c`；
I09 和 I10 会加入 `h2` 和 `h3`。

### I03 JSON Schema 与 `fit validate` — 已完成

- `schema.Generate` 根据配置类型的标签（通过 `config.Fields`，与解码器使用同一份描述）和已注册的模块生成
  draft 2020-12 的 JSON Schema：模块引用的 `module` 是该命名空间中模块的枚举，`params` 遵循对应模块的 schema。
- `schema/fit.schema.json` 已提交；它过期时 `TestSchemaUpToDate` 会失败，`make schema`（或
  `go test ./schema -update`）会重新生成。示例配置已用 Python 的 `jsonschema` 对照它校验通过，一份错误配置中的
  三处错误也都被它拒绝。
- `fit validate -c file` 加载配置后再编译，所以模块参数也会被检查；错误输出为 `file:line:col: path: message`，
  退出码为 1。`fit schema` 输出 schema。

与计划的差异：schema 放在顶层的 `schema` 包，而不是 `config/schema`，因为它需要模块注册表，而模块注册表本身依赖
`config`。

### I04 模块注册表与核心接口 — 已完成

- `module.Register`、`Lookup`、`Namespace`、`Load`、`Cleanup`。ID 带命名空间（`http.handlers.respond`）；配置中
  写最后一段，命名空间由名字出现的位置决定（`handler` → `http.handlers`，`middleware` → `http.middleware`）。
- `Load` 把参数严格解码到模块的结构体，再调用 `Provision`，其上下文的 `Errorf` 可以指向某个参数；普通错误会被
  加上参数的路径和位置。未知模块的错误会列出该命名空间中已有的模块。
- `module.Handler` 就是 fib 的 `http.Handler`，所以可以复用 fib 的 handler 和中间件。`Middleware.Wrap` 在编译时
  调用一次。`Service` 和 `Upstream` 标记为暂定，在 I14 和 I19 中确定。
- `respond`：状态码（默认 200，拒绝 1xx）、头（检查名称和值）、body（在 204 和 304 上拒绝）；响应只构建一次并
  共享，fib 允许这样做。

### I05 运行时图与原子替换 — 已完成

- `runtime.Compile` provision 所有模块，把中间件包在 handler 外面，给每个 listener 分配路由（全部路由，或指名它的
  路由），按优先级排序、同优先级保持书写顺序，并应用默认协议。它报告每一个失败的模块，任何失败时都会清理已
  provision 的模块。
- `Runtime.Current` 无锁：用 CAS 引用当前图，并拒绝计数已归零的图，所以与替换竞争的读者不会让正在清理的图
  "复活"。`Runtime.Swap` 给图编号并释放旧图；最后一次 `Release` 按逆序清理其模块。
- 测试（race 检测下）：8 个读者通过当前图处理请求的同时替换图 300 次；没有读者看到已清理的模块，每张图都恰好
  清理一次。

与计划的差异：没有实现路由匹配；`Route` 原样保存 host 和 path，供 I11 的路由器使用。

### I06 HTTP 入口与 `fit run` — 已完成

- `listener.Start` 把运行时当前图中的每个 listener 绑定为同一个 fib engine 的一个 binding，它们共用一个
  事件循环、任务池和缓冲池。每个 binding 的 handler 是一个 `fibhttp.ServerHandler`，里面的分发器在每个
  请求上取当前图，按名字找到自己的 listener，匹配路由并调用路由的 handler；handler 返回时释放图。没有
  匹配的路由返回 404，没有图返回 503。
- 协议映射到 fib 的 HTTP 配置：只有 `http/1.1` 时设置 `DisableHTTP2`（同时拒绝 `Upgrade: h2c`），只有
  `h2c` 时设置 `HTTP2Only`，两者都有时同一端口同时支持 HTTP/1.1、预先知道的 h2c 和 `Upgrade: h2c`。
- `Server.Shutdown` 就是 fib 的 `Engine.Shutdown`：listener 停止接受连接，空闲连接关闭，忙的连接在响应
  写完后关闭（HTTP/2 发 GOAWAY）；然后关闭 engine。
- `fit run -c file [-shutdown-timeout 30s]` 加载并编译配置，每个 listener 打印一行实际绑定的地址；收到
  SIGINT 或 SIGTERM 时优雅退出，退出码为 0，超时则为 1。第二个信号直接结束进程。配置错误的打印方式
  与 `fit validate` 相同，共用一个辅助函数。
- 测试：`listener` 通过 HTTP/1.1 和 h2c 访问两条路由（host 与 path 匹配、404），单协议 listener 拒绝
  另一种协议，替换图后使用新图，地址被占用时报错；退出测试用一个阻塞的 handler 挂住请求，验证
  HTTP/1.1 和 h2c 下 `Shutdown` 都会等它、请求能正常完成、图中的模块恰好清理一次。`test/e2e` 编译
  二进制、运行 `fit run`，通过 HTTP/1.1 和 h2c 拿到响应，在有空闲连接时发送 SIGTERM 并期望正常退出；
  配置错误时退出码为 1 并打印错误。另外用 curl 手动验证了预先知道的 h2c、`Upgrade: h2c` 和 SIGTERM。

与计划的差异：

- 路由匹配曾是 `listener/match.go` 里一个线性的临时实现（精确和 `*.` 通配的 host，精确和前缀的 path，
  按优先级取第一个匹配，不分配内存），让路由在 I11 之前就按配置的意思工作。I11 已用 `router` 包替换它。
- listener 在服务器启动时就固定了。替换后的图会改变同名 listener 的路由；新图里没有的 listener 返回
  404，新图里新增的 listener 不会被绑定。I26 负责 listener 的增量变更。
- 请求只在 handler 返回之前持有图，而不是直到响应结束。这对 `respond` 足够；稍后才响应的 handler
  （从 I15 的代理开始）需要持有图直到响应结束或被取消（`Context.OnFinish` 和 `OnCancel`，或
  `Retain`）。
- e2e 测试没有覆盖进行中的请求，因为目前自带的模块都无法挂住请求；这由进程内的 `listener` 测试覆盖。
  e2e 测试不在 Windows 上编译，因为那里没有 SIGTERM。

### I07 基准框架：吞吐 — 已完成

- `bench/cmd/fitbench`，一个只用标准库和 `docker` 命令行的 Go 程序，见
  [决定 002](decisions/002-benchmark-harness.zh-CN.md)。`go run ./bench/cmd/fitbench`（或 `make bench`）
  为 Linux 编译 fit，构建压测客户端镜像（Ubuntu 上的 wrk、h2load 和 curl），用固定版本的官方镜像运行
  各服务器（nginx 1.31.4、Caddy 2.10.2、Traefik 3.5.0；fit 跑在 `ubuntu:24.04` 上），写出
  `bench/results/run-<时间>.json`。
- `bench/configs` 里四个服务器的配置，在 8080（HTTP/1.1，wrk）和 8081（h2c，h2load）上提供同样的路由：
  `fixed`、1 KiB 的 `static` 文件，以及代理到返回同样固定响应的 fit 后端的 `proxy`。
- 每轮之前用 curl 核对状态码、协议和响应体；然后预热，再测量。每个结果记录每秒请求数、延迟（wrk 的
  百分位通过 Lua `done` 钩子以 JSON 输出；h2load 给出均值、标准差和最大值）、按类型分的错误数、服务器的
  CPU 核数和内存（取自其 cgroup）。报告记录 CPU 型号、内核、内存、版本、镜像摘要和参数。各容器绑定
  到各自的 CPU；nginx 每个服务器 CPU 一个 worker。
- 测试：wrk 和 h2load 的解析（用真实的输出格式）、cgroup 解析、CPU 划分和名字选择。框架本身端到端
  跑过多次，有短时的也有完整时长的。

与计划的差异：

- 用 Go 程序而不是脚本，这样解析逻辑有测试，并且有 Docker 的地方都能跑。
- Traefik 既不能返回固定响应也不能提供文件，只跑 `proxy`；fit 只跑 `fixed`，直到 I12（`static`）和
  I15（`proxy`）把各自的场景加进 `servers.go` 和 `configs/fit/fit.json`。
- 静态文件放在 Docker volume 里：Docker Desktop 的 bind mount 让 nginx 只有每秒 1.4 万请求，换成
  volume 后是 35 万。
- 每个代理都保持最多 4096 个空闲的后端连接。用默认值时，Caddy（32）和 Traefik（200）在 256 或 2048 个
  在途请求下不断重开后端连接直到端口耗尽：Caddy 跌到每秒 1600 个请求并返回 502。
- 目前只在 Docker Desktop（bridge 网络）上跑过，还没有在 Linux 主机上用 `-network host` 跑过，而
  发布的数字应该走后者。

### I08 基准框架：空闲连接内存与基线 — 已完成

- `bench/cmd/idleconn` 每次 64 个并发地打开 N 个连接，每个发一个请求后保持住，直到标准输入关闭：
  `http1`（请求 `/idle` 得到 404 后的长连接）、`tls`（同上，走 8443 上的 TLS）和 `ws`（由服务器代理
  到后端的 WebSocket）。`idleconn -serve-ws` 就是这个后端。
- `fitbench -idle http1,tls,ws` 对新启动的服务器测每种连接：打开连接前、以及连接空闲 `-idle-settle`
  后各读一次服务器 cgroup 的匿名内存，差值除以连接数。TLS listener 用框架生成的一次性 ECDSA P-256
  证书。
- [`bench/results/baseline-2026-10-19-macos-docker.json`](https://github.com/lesismal/fit/blob/3658ae9/bench/results/baseline-2026-10-19-macos-docker.json)，
  使用默认参数（每轮 30 秒、wrk 256 个连接、h2load 64×32 个流、1 万个空闲连接）。`bench/README.zh-CN.md`
  说明如何复现。

每秒请求数，以及每个空闲连接的匿名内存（每种 1 万个）：

| | nginx | Caddy | Traefik | fit |
| --- | ---: | ---: | ---: | ---: |
| fixed，HTTP/1.1 | 811,923 | 249,350 | — | 1,018,822 |
| fixed，h2c | 914,776 | 163,137 | — | 3,651,820 |
| static，HTTP/1.1 | 724,292 | 155,336 | — | — |
| static，h2c | 663,958 | 112,823 | — | — |
| proxy，HTTP/1.1 | 170,101 | 97,915 | 142,490 | — |
| proxy，h2c | 158,202 | 69,446 | 85,954 | — |
| 空闲 HTTP/1.1 | 531 B | 21,733 B | 22,812 B | 956 B |
| 空闲 TLS | 29,943 B | 51,561 B | 51,928 B | — |
| 空闲 WebSocket（代理） | 22,281 B | 131,578 B | 130,963 B | — |

在这台机器上，HTTP/1.1 的数字每次运行之间最多相差四分之一（nginx 的固定响应在两次完整运行中分别是
111 万和 81 万，较慢的那次有几十个 wrk 超时）；h2c 和内存的数字只差几个百分点。nginx 在打开任何连接
之前就有 176 MB 内存，是它预分配的连接表；Caddy 是 13 MB，Traefik 22 MB，fit 9 MB。

与计划的差异：

- 内存用的是 cgroup 的匿名内存而不是 RSS：它覆盖容器内所有进程，不会重复计算共享页，也不含页缓存。
- 基线是在一台 Apple M3 Max 的 Docker Desktop 上测的（给 Docker 14 个 CPU，bridge 网络），这是本次
  会话唯一可用的机器。服务器之间的比例有意义，绝对数字与 Linux 主机上的不同。应在专用的 Linux 主机上
  重跑，并把那份结果作为要发布的基线提交。
- 路线图提到的 WebSocket 和 HTTP/3 压测客户端不在 M0 内，见决定 002。

### I11 路由 v1：host 与 path — 已完成

- `router` 包：`New` 由一组路由（host、path、优先级和任意类型的值）构建 `Router[T]`，
  `Match(host, path)` 返回服务该请求的路由的值，没有匹配时返回 false，listener 据此返回 404。host 可以是
  精确的，也可以是 `*.` 通配（恰好一个标签），忽略大小写、端口、IPv6 的方括号和末尾的点；path 可以是精确的，
  也可以是以 `*` 结尾的前缀；没有 host 或没有 path 的路由匹配所有 host 或 path。
- 多个路由同时匹配时的规则：优先级最高的；其次 host 最具体的（精确，然后通配，然后没有 host）；其次 path
  最具体的（精确，然后最长的前缀，然后没有 path）；最后是写在前面的。先看 host 再看 path，与 nginx 和
  Caddy 一致：它们先按名字选出 server 或站点，再看 path。这条规则写在包文档和 `priority` 字段的说明里，
  因此 schema 中也有。
- 实现方式：一个按精确 host、一个按通配后缀（`.example.com`）索引节点的 map，再加一个给没有 host 的路由用的
  节点。节点把精确 path 放在一个 map 里，把前缀按长度分别放在 map 里，从长到短尝试。一次查找最多查三个 host
  节点，每个节点对精确 path 查一次 map，对每种不同的前缀长度查一次，一旦剩下的都不可能胜出就停止。含大写
  字母的 host 在栈上的缓冲区里转成小写，再通过 `m[string(b)]` 查找，Go 对这种写法不分配内存。
- `runtime.Compile` 为每个 listener 用它的路由构建一个路由器（`Listener.Router`）；`Listener.Routes`
  和以前一样按优先级排序。`listener` 的分发器用请求的 `Host` 和 `URL.Path` 调用 `Router.Match`；
  `listener/match.go` 及其测试已删除。
- 测试：host 和 path 的表驱动测试，以及顺序和平局的表驱动测试（优先级压过具体程度；低优先级的路由仍能服务
  高优先级路由不匹配的请求；精确 host 优先于通配，通配优先于没有 host；host 优先于 path；精确 path 优先于
  较长前缀，较长前缀优先于较短前缀，较短前缀优先于没有 path；平局时取写在前面的路由；后写的、优先级更高的
  路由会替换同一模式下先写的路由；一个路由的每个模式各自匹配）。`TestMatchAllocs` 用 `AllocsPerRun`
  在 103 个路由的路由器上验证 `Match` 不分配内存，覆盖小写和大小写混合的 host、通配、IPv6 和未命中。
  `BenchmarkMatch`：101 个路由时每次查找约 60 ns，0 次分配（Apple M3 Max）。`runtime` 的测试验证每个
  listener 的路由器只在自己的路由中选择；`listener` 的服务测试加了一个写在最后的通配路由，它在 HTTP/1.1
  和 h2c 下都胜过没有 host 的路由。`go vet ./...` 和 `go test -race ./...` 通过。

与计划的差异：

- 以前优先级相同时取写在前面的路由；现在按计划取最具体的路由。如果配置里较宽泛的路由写在前面且优先级相同，
  现在会由较具体的路由胜出。
- `Router` 对路由的值是泛型的，这样 `runtime` 可以构建它，而 `router` 不需要引用 `runtime`。host 和 path
  的匹配内置在路由器里，而不是由 `module.Matcher` 模块组成；`module.Matcher` 留到 I36 加入需要整个请求的
  匹配器时再用。

### I12 静态文件 I — 已完成

- `handler/static` 包，模块名 `static`，参数有 `root`（必填）、`index`（默认 `["index.html"]`，`[]` 表示
  不用）、`strip_prefix`、`max_file_bytes`（默认 1 MiB）和 `max_cache_bytes`（默认 64 MiB）。已在
  `modules/standard` 中注册，schema 已重新生成。
- 请求处理：只接受 GET 和 HEAD，其他返回 405 并带 `Allow`；去掉 `strip_prefix`（路径没有该前缀时返回 404）；
  含 `..` 段、NUL 或反斜杠的路径返回 400；其余路径清理后拼到根目录下并 stat。请求目录但没有结尾斜杠时
  301 重定向到带斜杠的路径（保留查询串）；有斜杠时返回第一个是普通文件的索引文件，否则 404。带结尾斜杠
  请求文件、或目标不是普通文件，返回 404；stat 因权限被拒返回 403。
- 不超过 `max_file_bytes` 的文件在 `max_cache_bytes` 允许时缓存在内存中，响应头只构建一次（`Content-Type`
  按扩展名或内容嗅探、`Last-Modified`、`ETag`、`Accept-Ranges`），所有响应共享。没有条件和 Range 的请求
  直接用 `WriteResponse` 发出该响应头和缓存的字节；有条件或 Range 的请求交给 `net/http.ServeContent`，它
  处理 `If-None-Match`、`If-Modified-Since`、`If-Match`、`If-Unmodified-Since`、`Range`（包括 multipart）和
  `If-Range`。更大的文件，或缓存放不下的文件，通过 `ServeContent` 从磁盘读取。只要文件大小和修改时间不变，
  缓存副本就有效；文件被删除后从缓存中移除。
- `internal/fibtest.ServeRequest` 用测试构造的请求运行 handler，并像服务器一样在 handler 返回时结束通过
  `Write` 写出的响应。
- 测试：`handler/static` 覆盖 MIME 类型（按扩展名和嗅探）、HEAD、空文件、转义的文件名、按顺序尝试的索引
  文件、带或不带查询串的重定向、404、带 `Allow` 的 405；穿越尝试 `/../`、`/a/../../`、`%2e%2e`、`..%2f`、
  `..%5c`、`%00` 和 `/..` 都返回 400，且不会返回根目录外的文件，而 `..dots` 这样的文件名正常返回；上面
  所有条件请求和 Range 情况，分别从内存和磁盘服务；文件修改后返回新内容和新 `ETag`，删除后返回 404 并从
  缓存移除；缓存上限；`strip_prefix`；`"index": []`；403；以及参数错误。`test/e2e` 用构建出的二进制通过
  HTTP/1.1 和 h2c 服务一个小文件和一个 1 MiB 文件，覆盖 HEAD、304、Range、索引、重定向和原始 `..` 路径。
  `BenchmarkServeCached`：缓存的 1 KiB 文件每请求约 1.8 µs、672 B、8 次分配（含测试上下文，Apple M3 Max）。
  `go vet ./...` 和 `go test -race ./...` 通过。

与计划的不同：

- handler 没有使用 fib 的 `http.FileCache`：它在 v1.2.0 中不发送 `ETag`，并且自己应答请求，因此不知道索引
  文件，也会自己写 404；见[决定 003](decisions/003-static-file-cache.zh-CN.md)。handler 每个请求 stat 一次
  文件，与 nginx 默认行为相同，而 `FileCache` 在 Linux 上不需要。
- 任何 `..` 段都返回 400，即使它不会离开根目录，与 nginx 拒绝越过根目录的路径一致；符号链接无论指向哪里都会
  跟随，与 nginx 和 Caddy 的默认行为一致。

### I14 上游连接池 v1（HTTP/1.1） — 已完成

- `config.Duration`：一段时长，写成 `time.ParseDuration` 接受的字符串（`"10s"`、`"1m30s"`），不能为负。
  解码器和 schema 像对待 `Value` 一样识别它；schema 给它一个正则，有测试对照解码器检查这个正则。
- `upstream` 包：`Config` 有 `addresses`（必填，每个是 `host:port`）、`max_conns`（每个地址，默认不限）、
  `max_idle_conns`（每个地址，默认 32，0 表示不保留）、`idle_timeout`（60s）和 `connect_timeout`（10s）。
  `Config.Check` 拒绝空列表、URL（并给出提示）、缺少主机或端口、端口越界和重复地址，每个错误都带路径和位置。
- `New(engine, config)` 在一个运行中的 engine 上用 `fibhttp.Client` 创建 `Pool`，各项上限映射到
  `MaxConnsPerHost`、`MaxIdleConnsPerHost`、`IdleConnTimeout` 和 `DialTimeout`，不设整体 `Timeout`（I17）。
  fib 的客户端本身就按 `host:port` 管理连接池：复用 keep-alive 连接，超过上限的请求排队，保留部分空闲连接并
  按定时器关闭，所以连接池只增加了配置、默认值和地址选择。`Pool.Do` 轮流使用各个地址（原子计数，无锁），
  设置请求 URL 的 scheme 和 host，不改动 `Host` 字段，然后交给客户端，回调收到响应或错误。`Close` 关闭空闲
  连接，并让排队中的请求失败。
- 测试使用会统计连接数的 `httptest` 后端：依次发送 20 个请求只用一个连接；`max_idle_conns: 0` 时每个请求
  一个新连接；`max_conns: 2` 且后端挂住两个请求时，另外六个请求排队，不会打开第三个连接，最终八个请求全部
  完成，同时最多两个连接、两个请求在途；`idle_timeout: "100ms"` 关闭空闲连接，下一个请求打开新连接；`Close`
  关闭空闲连接并拒绝新请求；两个地址各承担十个请求中的五个，各用一个连接；连接被拒绝，以及在
  `connect_timeout: "200ms"` 下连接 TEST-NET-1，都能很快失败；所有配置错误的消息和位置。`config` 和 `schema`
  测试 `Duration`。该包在 `-race` 下运行十次均通过；`go vet ./...` 和 `go test -race ./...` 通过。

与计划的不同：

- 上游配置还没有进入配置文件：`upstream.Config` 是解码器可以接受的类型，由 I15 决定放在哪里：作为路由按名字
  引用的顶层 `upstreams` 列表（类似 nginx 的 `upstream` 块和 Traefik 的 service），或者内联在代理参数中（类似
  Caddy）。在此之前 schema 不变。
- `New` 接受用于拨号的 engine。它从哪里来由 I15 决定：listener 在图编译之后才创建 engine，所以要么在 engine
  创建后再为图建立连接池，要么用一个单独的客户端 engine。共享服务器的 engine 能让代理请求留在它到达的事件
  循环上，与 nginx 一样。
- 连接池已经会在多个地址之间轮流分配请求，否则每个上游只有一个地址，`addresses` 就成了只有一项的列表；
  I19 会用它的选择接口替换这一做法。在那之前 `module.Upstream` 和 `module.Service` 仍是暂定接口。

### I15 反向代理 I：转发与头处理 — 已完成

- 配置：顶层 `upstreams` 列表，元素是 `config.Upstream`（`name` 加上 I14 的字段），由 `Config.Validate` 检查
  （名字，以及原来 `upstream.Config.Check` 里的地址检查）。该类型从 `upstream` 移到了 `config`（`upstream` 依赖
  `config`）。见[决定 004](decisions/004-upstreams-and-async-handlers.zh-CN.md)。
- `runtime.Compile(c, Env)` 在提供模块之前通过 `Env.Engine` 为每个上游创建连接池，用 `module.Env` 传给模块
  （`module.Load` 接受它；`Context.Upstream(name)` 查找），并在清理模块之后关闭它们。`fit run` 用
  `upstream.StartEngine` 启动客户端 engine，图释放后停止；`fit validate` 不带 engine 编译，此时连接池让每个请求
  失败。`Pool.Close` 现在会排空：在途请求的回调都执行完后才关闭客户端。
- `handler/proxy` 包，模块名 `proxy`，参数有 `upstream`（必填）、`trusted_proxies`（IP 和 CIDR）和
  `host_header`。它用客户端的方法、路径（保持收到时的转义）、查询串、头和 body（`c.Body()`，fib 默认整体读取）
  构造上游请求，retain 请求，调用 `Pool.Do`，在回调里写回状态码、头和 body，然后 release。逐跳头（`Connection`
  及其列出的头、`Keep-Alive`、`Proxy-Connection`、`Proxy-Authenticate`、`Proxy-Authorization`、`TE`、`Trailer`、
  `Transfer-Encoding`、`Upgrade`）双向都去掉。除非设置了 `host_header`，否则把客户端的 `Host` 发给上游。客户端
  没发 `User-Agent` 时不会凭空加一个。在 `trusted_proxies` 中的客户端，会把它自己追加到 `X-Forwarded-For` 和
  `Forwarded`，并保留它的 `X-Forwarded-Proto` 和 `X-Forwarded-Host`；其他客户端这四个头都会被替换。`Forwarded`
  中的 IPv6 地址和带端口的 host 会加引号。上游请求失败返回 502。
- 测试通过真实的 listener 和客户端 engine 进行：方法、转义的路径和查询串、`Host`、重复的和自定义的头、8 KB 的
  body 通过 HTTP/1.1 和 h2c 到达上游，状态码和头正确返回；请求中的逐跳头被去掉，用原始后端验证响应中的逐跳头
  也被去掉；不可信和可信的转发头；`host_header`；连接被拒绝时返回 502；HEAD 保留上游的 `Content-Length`；两种
  协议下 64 个并发请求；IPv4、IPv6 和 unknown 的 `Forwarded` 元素；配置错误。`config` 测试上游检查，`upstream`
  测试排空和无 engine 的连接池，`cmd/fit` 校验 `examples/proxy.json`，`test/e2e` 通过构建出的二进制做代理。代理
  测试在 `-race` 下运行五次均通过；`go vet ./...` 和 `go test -race ./...` 通过。

与计划的不同：

- 计划中的“一个上游”是配置 `upstreams` 中的一个具名上游，它可以有多个地址并轮流使用（I14）。
- `TE` 和其他逐跳头一样被去掉，包括 `TE: trailers`；gRPC（I49）需要保留它，并配合 trailer（I17）。
- 代理用 `Retain` 持有的是请求而不是图；决定 004 说明了为什么连接池的排空就足够了。

### I21 访问日志 — 已完成

- 配置：顶层 `access_log`，包括 `output`（默认 `"stdout"`，也可以是 `"stderr"` 或追加写入的文件）、
  `flush_interval`（1s）和 `buffer_bytes`（4 MiB）。`fit run` 打开它，通过 `runtime.Env.AccessLog` 交给图，
  并在图之后关闭它，关闭时写出缓冲的内容。`fit validate` 不会打开它。
- `observe` 包：`AccessLog.Log` 把一行 JSON（UTC 毫秒时间、listener、路由、客户端 IP、协议、方法、host、URI、
  状态码、收发字节数、耗时、上游及上游耗时、user agent、referer）格式化到栈上的缓冲区，再在短暂持锁的情况下追加
  到日志缓冲区；不分配内存。日志自己的 goroutine 每隔 `flush_interval`，或者积累到 64 KB 时，换出缓冲区并写出。
  当输出慢或被阻塞、等待写出的数据达到 `buffer_bytes` 时，新行被丢弃并计数，输出恢复后把计数写到 stderr：请求
  永远不等磁盘。字符串手工转义，无效的 UTF-8 变成 U+FFFD，保证每一行都是合法 JSON。
- `listener` 的分发器在图有日志时创建一个 `observe.Entry`（开始时间、listener、路由、收到的字节数），用
  `observe.Attach` 挂到请求的 context 上，并注册 `Context.OnFinish` 钩子，用 fib 发出的状态码和大小记录日志。代理在
  写响应前填入上游地址和耗时。404 也会记录。
- 测试：`observe` 检查一行的字段和转义、IPv6 和缺省字段、对照 `encoding/json` 的 JSON 字符串转义、按间隔刷新、
  积累 64 KB 时刷新、输出被阻塞（写出卡住时一秒内记录 10000 行，每一行要么写出要么计入丢弃，丢弃被报告）、
  `Close` 会刷新并丢弃之后的行，以及文件输出。`handler/proxy` 通过真实 listener 在 HTTP/1.1 和 h2c 下记录代理和
  未匹配的请求并检查所有字段；`test/e2e` 用文件日志运行 `fit run`，在 SIGTERM 后找到这些行。`BenchmarkLog`：14
  个 goroutine 同时记录时每行约 416 ns、零分配（Apple M3 Max）。`go vet ./...` 和 `go test -race ./...` 通过。

与计划的不同：

- 计划中的“基准显示日志不阻塞 worker”由输出阻塞测试和 `BenchmarkLog` 证明。开关日志的压测原本要在压测机上
  进行，但压测机拒绝了本会话的 ssh 密钥；配置好密钥登录后可以在那里补跑。
- 日志属于进程而不是图，这样图替换之后才结束的响应仍有日志可写。I22 决定修改了 `access_log` 的重载怎么处理，以及
  SIGHUP 是否为日志轮转重新打开文件。
- 所有 listener 共用一个日志；按 listener 或路由分开的日志、采样和其他格式，等有需要时再加。高并发竞争下单锁的
  开销可见（14 个 goroutine 时每行 416 ns）；如果 I23 的数据需要，改成每 worker 一个缓冲区。

### I16 反向代理 II：流式转发与背压 — 已完成

- fit 先升级到了 fib v1.3.0（单独一个提交），其中有无等待转发所需的一切：`Context.HoldBody`、带 `OnDrain` 的
  `BodyWriter`，以及在连接有空间时发送推送式 body 的客户端。以 fib 的参考代理 `fib-examples/gateway` 为蓝本。
- listener 设置 `StreamRequestBody`，阈值 64KB（`listener.StreamThreshold`）；上游连接池设置
  `StreamResponseBody`，阈值相同（`upstream.StreamThreshold`）：更小的 body 仍然整体到达，走 I15 的路径，一次
  `WriteResponse`。
- 上传：body 以流方式到达的请求（`c.RequestBody() != nil`）以 `fibhttp.BodyWriter` 作为 body、带上客户端的
  `ContentLength`（分块时为 -1）发往上游。body 通过 `c.OnBody` 到来；每一段写入 `BodyWriter`，它复制后立即返回；
  超过其上限（256KB）时回调用 `c.HoldBody(true)` 挂起 body——那里积压 256KB 后就停止读取客户端——`OnDrain` 在
  上游取走积压后恢复。没有任何等待。上游请求头中不再复制 `Content-Length` 和 `Expect`：长度用请求自己的，
  `100 Continue` 由 fib 自己应答。
- 下载：body 以流方式到达的响应用 `resp.OnBody` 接收；第一段时发出状态码和头（上游有 `Content-Length` 时带上，
  这样只有上游本身分块时客户端才收到分块），每段用 `c.Write` 写出并 flush，最后一段结束响应。回调运行在上游
  engine 的 worker 上；客户端更慢时，`Write` 在排队达到 64KB（HTTP/1）或流窗口用尽（HTTP/2）时等待，等待期间
  不读上游连接，于是上游被 TCP 或它自己的流窗口减速。这是 fib 设计的节流方式（“what holds the connection back
  is fn itself”），等待发生在上游 engine 的池上，而不是 listener 的。上游在 body 中途失败时关闭 HTTP/1 客户端
  连接，让截断可见；在第一段之前失败则返回 502。客户端在 body 中途离开会结束上游响应。
- 测试（`handler/proxy/stream_test.go`）：通过真实 listener，在 HTTP/1.1 和 h2c 下，带 `Content-Length` 和
  分块两种方式，各下载 256MB、上传 256MB：下载的客户端每 4MB 暂停 20ms，上传的上游同样如此。每个字节都对照
  一个周期为素数的模式检查，丢失、重复或错位都能发现；长度和分帧也被检查。存活堆（`/gc/heap/live:bytes`，
  每 10ms 采样）在任何情况下最多上涨 10MB，上限为 16MB；传 1GB 时涨幅相同，说明内存不随 body 增长。在 `-race`
  下测试传 64MB。`go vet ./...` 和 `go test -race ./...` 通过。

与计划的不同：

- 计划中的 `HoldReads` 联动，在上传方向是 `HoldBody` 和 `BodyWriter.OnDrain`（通过 fib 挂起客户端的读），在
  下载方向是 fib 对 `Context.Write` 的节流（挂起上游的读）。fit 自己从不调用 `HoldReads`。
- 下载方向在上游 engine 的 worker 上于 `Context.Write` 中等待。fib v1.3.0 没有在队列满时返回而不是等待的
  `Context` 写接口；如果 I23 的数据显示大量慢客户端占住了上游 engine 的池，就该向 fib 要这个接口。
- 上游在 body 中途失败时，HTTP/2 客户端的流会被正常结束而不是被重置；I17 会正确处理失败。

### I22 SIGHUP 重载 — 已完成

- `listener.Server` 现在创建不带 binding 的 engine，再用 `Engine.AddBinding` 添加每个 listener。`Server.Reload(g)`
  把它切换到新图：名字、地址和协议都没变的 listener 保留它的 binding、端口和连接；其余的在替换之前绑定；然后
  `Runtime.Swap(g)`；然后 `g` 中没有的或有变化的 listener 在后台用 `Engine.ShutdownBinding` 排空，最长为排空超时
  （`fit run` 的 `-shutdown-timeout`）。如果某个 listener 无法绑定，为 `g` 建立的 binding 会被移除，`g` 被释放，
  正在运行的图保持不变。在同一地址上被替换的 listener（新协议或新名字）与旧的并排绑定——fib 的 SO_REUSEPORT
  允许这样做——所以不会出现没有监听的时刻；如果失败，旧的先停止监听，它的连接继续完成。`Addrs` 返回当前的
  listener。
- `fit run` 把 SIGHUP 变成一次重载：用同一个上游 engine 和访问日志加载并编译文件，调用 `Reload`，输出
  `fit: reloading`、每个被移除和新增的 listener 各一行，以及 `fit: reloaded <file> (config version N)`。加载、
  编译或绑定失败的配置在 stderr 输出 `fit: reload failed; the running config stays` 及其错误。重载期间到达的信号
  合并为下一次重载。`access_log` 有变化时会提示需要重启才生效。
- 测试：`listener` 的重载中，一个 listener 保持不变（端口相同，跨越重载的 keep-alive 连接能看到新路由），一个
  有变化（协议），一个新增，一个被移除（不再接受连接），并检查图的版本；地址已被占用的重载会失败，解除它已绑定
  的 listener，旧图继续服务；固定端口上的 listener 原地更换协议。`test/e2e` 用八个客户端——keep-alive 和每请求
  新连接、HTTP/1.1 和 h2c——访问 `fit run`，期间经历一次修改路由并新增 listener 的重载，以及一次配置有错的重载
  （正在运行的配置继续服务并输出错误）：五次运行每次约 36000 个请求，没有失败。`go vet ./...` 和
  `go test -race ./...` 通过。

与计划的不同：

- listener 按名字、地址和协议一起判断是否保留，而不是只看地址，这样两个端口为 0 的 listener 不会混淆，协议
  有变化的 listener 会按新协议服务。
- 重载时不会重新打开或替换访问日志；`access_log` 的变化要等重启。为日志轮转重新打开文件（nginx 用 USR1）等有
  需要时再做。
- 每个图都会新建上游连接池，所以重载后旧图排空时会丢掉它的空闲上游连接。I26 可以把连接池带过去。

### I17 反向代理 III：超时、取消、错误 — 已完成

- `config.Upstream` 新增 `response_header_timeout` 和 `response_idle_timeout`（默认各 60s）；`connect_timeout`
  已经有了。`upstream.Pool.Timeouts` 把前两个交给代理，由代理执行，因为只有代理知道响应何时开始、body 何时停顿。
- 代理的请求处理移到了 `handler/proxy/exchange.go`。每个上游请求带一个代理会按原因取消的 context：客户端离开
  （`Context.OnCancel`）、响应头计时器（在 `Do` 时启动，响应到达时停止）、空闲计时器（流式响应开始时启动，每收到
  一段 body 重置）。取消会让 fib 的客户端放弃请求并结束正在流式传输的 body。
- 响应开始之前的失败：响应头或空闲超时、连接超时（fib 拨号返回的 `os.ErrDeadlineExceeded`）、body 被挂起超过
  `MaxBodyHold`，返回 504；客户端 body 出错返回 400，读超时返回 408，超过 `MaxStreamedBodyBytes` 返回 413；其他
  返回 502。客户端已离开则不应答。响应开始之后：有声明长度的 body 不足长度就结束，fib 会把它变成关闭 HTTP/1 连接
  或重置 HTTP/2 流；分块的 body 关闭 HTTP/1 连接；HTTP/2 上没有声明长度的 body 以 `Fit-Error` trailer 结束（与 fib
  的 gateway 一样），因为 fib 无法重置这样的流。
- Trailer：响应的 trailer 会被转发，无论整体（放在 `Response.Trailer`）还是流式（`Trailer` 声明在逐跳过滤中保留，
  值在 `Finish` 之前用 `http.TrailerPrefix` 设置）。请求的 trailer 也会转发：带 trailer 的 body 即使整体到达，也
  通过 `BodyWriter` 分块发送，body 结束时把值复制到上游请求的 `Trailer` 中——在 HTTP/2 上名字也是那时才知道。
- 测试（`handler/proxy/failure_test.go`，不在 Windows 上运行），HTTP/1.1 和 h2c：连接到 backlog 已满的 listener
  超时得到 504；从不应答的后端触发 `response_header_timeout`（约 150ms 后 504）；发送 200KB 后沉默的后端触发
  `response_idle_timeout`，无论有无长度，客户端都能看出 body 被截断；后端在 body 中途断开连接也一样；在响应头之前
  放弃的客户端、下载 256KB 后关闭的客户端、上传失败的客户端，都会让上游请求被取消（后端看到请求 context 结束）；
  小 body 和流式 body 的 trailer 双向转发；`statusOf` 对每种原因的映射。该包在 `-race` 下运行三次均通过；
  `go vet ./...` 和 `go test -race ./...` 通过。

与计划的不同：

- 超时配置在上游上而不是路由上；I43 会加上按路由的超时。
- 上游在 body 中途失败时，没有声明长度的 HTTP/2 响应以 trailer 结束而不是重置流，因为 fib v1.3.0 不允许 handler
  请求重置流。

### I19 负载均衡 v1 — 已完成

- 配置：`config.Upstream` 新增 `weights`（每个地址一个，1 到 1000）和 `load_balancing`：`round_robin`、
  `weighted_round_robin` 或 `least_conn`。省略时为 `round_robin`，给了权重则为 `weighted_round_robin`。`Validate`
  拒绝权重个数与地址个数不符，以及 `round_robin` 带权重。
- `upstream.Balancer` 是选择接口 `Pick() *Backend`；`Backend` 是地址、权重和在途请求数。轮询是一个原子计数器。
  加权轮询是 nginx 的平滑算法，加互斥锁：权重 5、1、1 得到 `a a b a c a a`。最少连接扫描所有后端，找按权重计算
  在途请求最少的（交叉相乘比较，不做除法），起点随每次选择移动，让空闲后端分担负载；读取计数不加锁。
- `Pool.Pick` 选择并把请求计为在途，`Pool.Send` 发往该后端，`Pool.Done` 结束；`Pool.Do` 是三者合一，供回调返回
  即结束的调用者使用。代理在一次交换开始时选择，结束时（包括 body）调用 `Done`，所以长时间的下载在整个过程中
  都算作繁忙。连接池的排空（I15）现在也从 `Pick` 计到 `Done`，所以连接池关闭前会等待流式 body。访问日志的
  `upstream` 字段是所选后端的地址。
- 测试：轮询得到 `abcabc`，3000 次选择每个恰好 1000 次；加权轮询得到 nginx 的序列和精确比例（7000 次中
  5000/1000/1000；5000 次中 3000/2000）；最少连接在空闲后端间均分，跳过繁忙的直到其他追上，并让在途请求数按权重
  2:1 分布；每种策略在 `-race` 下由 8 个 goroutine 并发选择；`AllocsPerRun` 确认 `Pick` 没有分配。通过连接池时，
  权重 3:1 让 40 个请求分为 30 和 10，`least_conn` 下持有慢请求的地址在接下来 20 个请求中一个也不分到。`config`
  测试新增的检查。`BenchmarkPick`（14 个 goroutine，Apple M3 Max）：轮询 52 ns，加权 107 ns，最少连接 67 ns，
  零分配。`go vet ./...` 和 `go test -race ./...` 通过。

与计划的不同：

- 权重是 `addresses` 旁边的一个列表（与 Caddy 一样），而不是用对象替换地址字符串，这样 `addresses` 保持 I14 时
  的简单形式。
- 加权最少连接顺带实现了，所以 `least_conn` 也接受权重，与 nginx 一样。

### I20 被动健康检查 — 已完成

- `config.Upstream` 新增 `max_fails`（默认 1，0 表示不检查）和 `fail_timeout`（10s），与 nginx 相同：某地址在
  `fail_timeout` 内失败 `max_fails` 次，就退出轮转 `fail_timeout`；之后重新接受请求，一次成功即清除记录。
- `Backend` 在计数器旁保存状态：`downUntil`（原子变量，Unix 纳秒，正常时为 0），以及当前窗口内的失败次数（由互斥锁
  保护，只有失败和失败后的第一次成功才会加锁）。选择时只读 `downUntil`，只有它被设置时才读时钟，所以一切正常时
  选择没有额外开销。所有策略都跳过已下线的后端；轮询和最少连接会把轮次移过它们，让负载均匀分给其余后端。如果所有
  后端都下线，就忽略健康状态，而不是让所有请求失败，因为可能应答的地址总比没有好（nginx 对单个 server 忽略健康
  状态也是这个原因）。
- `Pool.Done(b, failed)` 报告请求如何结束。代理在上游无法连接、中途断开或超时（502、504，以及被截断的响应）时
  计为失败；客户端离开或发送 body 失败（400、408、413）不算。HTTP 状态码不算失败。`Pool.Do` 把取消以外的错误
  计为失败。
- 测试：窗口内两次失败让后端下线，一次成功让它恢复，不同窗口的失败不累加，`max_fails: 0` 永不下线；每种策略都
  跳过下线的后端并把它的负载均匀分出去，不分配内存，全部下线时忽略健康状态，窗口过后重新接纳后端。通过连接池时，
  端口开始拒绝连接的地址只造成一次失败请求，之后不再分到请求；端口重新监听后，该地址在 `fail_timeout` 后重新加入
  并承担一半请求。通过代理时，带一个拒绝连接地址的上游在 20 个请求中只有 1 个返回 502，其余由正常地址应答。
  `go vet ./...` 和 `go test -race ./...` 通过。

与计划的不同：

- 没有重试：遇到失败地址的那个请求仍然失败。在其他地址上重试是 I43 的内容。
- 状态变化还没有记录日志；指标（I34）会报告它们。

### I18 HTTP/2 上游 — 已完成

- `config.Upstream` 新增 `protocol`：`http/1.1`（默认）、`h2c`（明文 HTTP/2，prior knowledge）或 `h2`（TLS，通过
  ALPN 提供 h2 和 http/1.1，不选 h2 的服务器也能访问），`h2` 配合 `tls_server_name` 和 `tls_ca_file`。`Validate`
  拒绝没有 `h2` 时的 TLS 字段；`upstream.New` 现在会返回错误（CA 文件无法读取或不含证书），`Compile` 带位置报告该
  错误（并就此停止，而不是再报告每个引用该上游的路由）。
- 连接池对 h2c 设置 fib 的 `UnencryptedHTTP2`，对 h2 设置 `tls.Config`（至少 TLS 1.2，给了 CA 文件时用它代替系统
  根证书），请求发往 `https://`。fib 的客户端在每个 HTTP/2 连接上复用请求，直到服务器的
  `SETTINGS_MAX_CONCURRENT_STREAMS`，超过后再开新连接，受 `max_conns` 限制。
- h2 测试发现并修复的代理问题：缺少 `User-Agent` 时现在发送一个存在但为空的字段，fib 的 HTTP/1 和 HTTP/2 客户端
  都将其理解为“不发送”（`[""]` 会让 HTTP/2 客户端发送一个空值）。请求声明的 trailer 也从它的 `Trailer` 头读取，
  因为 HTTP/2 客户端的 trailer 随 body 结束才到达，而 Go 的服务器只保留声明过的 trailer。还有 HTTP/2 测试暴露的
  一个竞争：fib 的客户端在发送请求头时读取请求的 `Trailer`，这可能晚于 body 结束，所以整体 body 的 trailer 现在在
  请求发出前就设置好；带 trailer 的流式上传则通过 `trailerPipe`，由 fib 客户端在它自己的 goroutine 上读取，在结束
  前一刻填入 trailer 的值。
- 测试：I15–I17 的代理测试以子测试的形式分别针对 HTTP/1.1、h2c 和 h2（TLS）后端运行——转发（检查上游看到的是
  HTTP/2）、请求逐跳头、转发头、`host_header`、64 个并发请求、HEAD、被动健康检查、双向各 256MB 流式传输且堆有
  上限、响应头和空闲超时、上游在 body 中途断开（现在用 `http.ErrAbortHandler` panic，它会关闭 HTTP/1 连接、重置
  HTTP/2 流）、客户端中止、双向 trailer。`config` 和 `upstream` 测试 TLS 设置的错误。流式测试的堆上限从 16MB 提高到
  32MB：堆中也包含测试自己的 HTTP/2 客户端和后端。代理包在 `-race` 下运行两次均通过；`go vet ./...` 和
  `go test -race ./...` 通过。

与计划的不同：

- fit 没有自己的每连接流数限制：fib 的客户端遵守服务器的限制，`max_conns` 限制连接数。fib v1.3.0 没有降低它的设置。
- 上游 TLS 的其余部分（客户端证书、跳过校验、密码套件选择）属于 I54。
- 带 trailer 的流式上传需要一个 goroutine，这是“转发不占 goroutine”的唯一例外；如果 fib 的 `BodyWriter` 能在关闭时
  接收 trailer 就能去掉它，值得向 fib 提出。

### I09 基于证书文件的 TLS 入口 — 已完成

- 配置：listener 新增 `tls: {"certificates": [{"cert_file", "key_file"}, ...]}`，协议新增 `h2`（基于 TLS 的
  HTTP/2）。带 TLS 的 listener 不写协议时服务 `http/1.1` 和 `h2`。`Validate` 拒绝没有 TLS 的 `h2`、带 TLS 的 `h2c`，
  以及没有证书的 TLS。
- `tlsmgr` 包（按计划只支持静态证书）：`ServerConfig` 加载证书对，返回一个 `tls.Config`：至少 TLS 1.2，TLS 1.2 的
  密码套件只保留 ECDHE 搭配 AES-GCM 或 ChaCha20-Poly1305（TLS 1.3 的套件都可以，也不可配置），ALPN 按协议设置
  （两者都有时用 `fibhttp.ConfigureTLS`，否则只有 `h2` 或 `http/1.1`）；以及证书和 ALPN 列表的指纹。无法加载的证书是
  带路径的配置错误。
- `runtime.Compile` 为每个 TLS listener 构建配置，所以 `fit validate` 也会检查证书文件。`listener` 用
  `fibtls.NewServer` 包装 HTTP handler；它的 HTTP 配置在允许 HTTP/2 时把 `h2` 与 `h2c` 同等对待。证书放在
  `Config.Certificates` 中，fib 的握手从中选出第一个适合客户端 hello 的证书——首先看 SNI，包括通配符——没有匹配的
  名字时用第一个：fib 把这样的握手作为状态机运行，不占 goroutine，而用 `GetCertificate` 就会失去这一点。listener
  的重载标识包含指纹，所以证书文件变化后的重载会在旧 listener 旁重新绑定，新连接得到新证书。
- 测试：`tlsmgr` 检查每种协议组合的配置、指纹随协议和证书而变，以及文件不存在、文件没有 PEM、私钥不匹配时的错误
  （带路径），还有校验规则。`listener` 通过 SNI（精确和通配符）在 HTTP/1.1 和 HTTP/2 下提供两个证书，未知名字得到
  第一个，拒绝 TLS 1.1，只服务配置的协议，并在重载时换上新证书。`test/e2e` 用两个各有证书的主机名运行
  `fit run`，覆盖 HTTP/1.1 和 h2，一个由 `respond` 应答，一个被代理，其上游看到 `X-Forwarded-Proto: https`。
  `internal/certtest` 为测试生成自签名证书。`go vet ./...` 和 `go test -race ./...` 通过。

与计划的不同：

- 证书由 fib 的握手从 `Config.Certificates` 中选择，而不是由 fit 自己的 `GetCertificate` 选择；证书管理器（I28）
  对运行中会变化的证书需要 `GetCertificate`，静态证书应继续走这条路径。
- 证书文件变化只在重载时重新读取；监视文件属于 I28。

### I13 静态文件 II — 已完成

- `static` 新增 `precompressed`（`br`、`zstd`、`gzip` 任选；默认不用）、`try_files` 和 `browse`（默认关闭）。
- 预压缩副本：对要服务的文件，在所列编码中查找 `name.br`、`name.zst` 和 `name.gz`，按客户端 `Accept-Encoding` 的
  q 值排序（支持通配符和 `q=0` 拒绝，相同时按配置顺序），服务磁盘上最合适的副本，带 `Content-Encoding`、原文件的
  `Content-Type`，以及副本自己的 `ETag` 和 `Last-Modified`，所以条件请求和 Range 都针对该表示。设置了
  `precompressed` 时，所有文件响应都带 `Vary: Accept-Encoding`。副本像文件一样按自己的路径缓存；太大无法缓存的
  副本从磁盘读取并带 `Content-Length`。每尝试一种编码多一次 stat，与 nginx 的 `gzip_static` 多一次 open 相当。
- `try_files` 接受带 `{path}`（请求路径）的路径，与 nginx 相同；以 `/` 结尾的表示目录，用其索引文件服务（有
  `browse` 时列出）。第一个能解析的被服务，否则 404；在 `try_files` 下，请求目录时不带斜杠不会被重定向。
  `["{path}", "{path}/", "/index.html"]` 可服务单页应用。每个候选路径都经过与请求路径相同的穿越检查。
- `browse` 列出没有索引文件的目录：一个 HTML 表格，目录在前，各组按名字排序，省略点文件，带大小和修改时间；名字
  做 HTML 转义，href 做路径转义，所以 `?`、`#`、`&`、`<` 仍然是名字的一部分。
- Sendfile：缓存放不下的文件经 `net/http.ServeContent` 交给 fib 的 `Context.ReadFrom`，在 HTTP/1 上通过 sendfile(2)
  发送（整个文件或一个范围），不读入内存（I12 起就是如此，现在写进了文档和代码注释）。HTTP/2 则需要分帧发送。
- 解析现在是独立的一步（`resolve`），普通请求和 `try_files` 共用；缓存和响应头接收一个 `variant`（决定类型的名字、
  编码、是否加 Vary）。
- 基准：fit 现在服务压测框架的 `static`（开启 `precompressed`；没有副本的 1KiB `small.html` 会让客户端接受的每种
  编码多一次 stat）和 `proxy` 场景，与 nginx、Caddy 一样；`bench/configs/fit/fit.json` 中的上游保留 4096 个空闲
  连接，与其他服务器一致。配置能通过校验，本地 `fit run` 在 HTTP/1.1 和 h2c 下返回该场景的 body。压测本身在 I23
  于压测机上运行。
- 测试：编码协商（偏好、q 值、相同时的顺序、`*`、拒绝、缺少副本、没有 `Accept-Encoding`）、副本的不同 ETag 和
  304、副本的 Range、从磁盘读取的副本、不设 `precompressed` 时副本是普通文件；`acceptQ`；单页应用的 `try_files`
  （文件、带与不带斜杠的目录、客户端路由、没有索引的目录、穿越）、全部找不到、配合 `browse`；目录列表（转义、顺序、
  点文件、上级链接、无 `browse` 时 404）；新参数的错误。`go vet ./...` 和 `go test -race ./...` 通过。

与计划的不同：

- 基准场景已经接入并检查过，但没有运行：压测机再次拒绝了本会话的 ssh 密钥，所以数据在 I23 时补上。

### I10 HTTP/3 入口 — 已完成

- 配置：协议新增 `h3`，需要 `tls`。它需要显式开启（与 nginx 一样）：不写协议的 TLS listener 服务 HTTP/1.1 和 h2。
- `listener`：一个 listener 现在最多对应 engine 的两个 binding：一个 TCP 用于 HTTP/1.1、h2c 和 h2，一个 UDP 用于 h3，
  端口与 TCP 得到的相同（所以端口为 0 的地址两者也是同一端口），由 `http3.NewHandlerWithConfig` 服务，TLS 配置经
  `http3.ConfigureTLS`（TLS 1.3，ALPN `h3`），请求 body 超过 64KB 时像 TCP 一样流式传递。两者使用同一套证书。只有
  `h3` 的 listener 只绑定 UDP。UDP 端口也像 TCP 一样先用普通 bind 检查。重载和退役会处理两个 binding；`Addr` 在
  `UDPAddr` 中报告 UDP 地址。指纹现在包含协议，所以增加或去掉 `h3` 会重新绑定。
- Alt-Svc：TCP 一侧的分发器在每个响应上注册 `OnHeader` 钩子，添加 `Alt-Svc: h3=":port"; ma=86400`（fib 的
  `http3.AltSvc`）。钩子和值每个 listener 只创建一次，注册时不分配内存；没有 `h3` 的 listener 不注册。fib 交给钩子的
  是 handler 传给 `WriteResponse` 的响应头的副本，所以 `respond` 和静态缓存共享的响应头不会被修改；有测试检查这一点。
- 测试：`listener` 在 TCP 端口上通过 h3（fib 的 HTTP/3 客户端，`Proto` 为 HTTP/3.0）访问路由，在 HTTP/1.1 和 h2 响应
  （包括 404）中看到指向它的 `Alt-Svc`；只有 h3 的 listener 不接受 TCP；没有 `h3` 时没有 `Alt-Svc`；去掉 `h3` 的重载会
  释放 UDP 端口；一个带 `h3` 和一个不带的 listener 共享同一个 `respond` 时，只有前者带 `Alt-Svc`。`test/e2e` 以 h3 运行
  `fit run`，通过 h2 访问 `/hello` 并看到 `Alt-Svc`，再在 Alt-Svc 指出的端口上通过 h3 访问，最后正常退出。
  `go vet ./...`（包括 Windows）和 `go test -race ./...` 通过。

与计划的不同：

- 在同一地址上被替换的 listener，在旧的排空期间会在旁边再绑定一个 UDP socket；这段时间内数据报由内核按哈希分到两个
  socket，落在这个窗口里的 QUIC 连接可能中断，客户端会重连。TCP 连接不受这种影响。

### I23 M1 一致性测试与基准 — 已完成

详见 [M1 报告](https://github.com/lesismal/fit/blob/3658ae9/bench/results/m1-report-2026-10-19.md)。

- 一致性（在开发机上）：h2spec 2.0.0 针对 h2c 和 TLS h2 入口，分别由 `respond` 应答和经过代理，四次运行均为
  145/145；以及 `test/e2e/conformance_test.go`：通过 `fit` 二进制、逐字节构造请求的代理一致性测试（同时带两种长度的
  请求走私、冲突的 `Content-Length`、冒号前的空白、缺少和重复的 `Host`、错误的分块大小、字段中的 NUL、超过上限的头、
  obs-fold、`Expect: 100-continue`、HTTP/1.0 客户端、管线化、绝对形式的请求目标、上游返回的 304、204 和 1xx、不说
  HTTP 的上游，以及任一方的 `Connection: close`）。
- 发现并修复：fib v1.3.0 把 `X-A : one` 当作名为 `X-A ` 的字段放行，而 RFC 9112 要求返回 400。代理拒绝字段名不是
  token 的请求，这样它不会到达上游。fib 中的修复作为 A9 写进了
  [fib-api-requests](fib-api-requests.zh-CN.md)，fib v1.4.0 已对这样的请求返回 400（`25064b3`）；代理的检查作为纵深防御保留，
  它就在本来就要复制头部的循环里。
- 基准（在压测机上：Ryzen 7 7800X3D，Ubuntu 24.04，host 网络）：`fitbench` 以默认参数运行 nginx 1.31.4、Caddy
  2.10.2、Traefik 3.5.0 和 fit，空闲连接 10k 和 100k；结果是 `bench/results/m1-2026-10-19-linux.json` 和
  `m1-2026-10-19-linux-idle100k.json`。fit 在 HTTP/1.1 和 h2c 上每秒千请求数：fixed 783 和 3811（nginx 812 和 643）；
  static 580 和 1328（nginx 440 和 373，Caddy 164 和 125）；proxy 215 和 230（nginx 258 和 249，Caddy 129 和 94，
  Traefik 152 和 113）。每个空闲连接的内存：HTTP/1.1 在 10k 时 1.5 KiB、100k 时 1.1 KiB（nginx 0.5，Caddy 和 Traefik
  20 到 22）；TLS 9.1 和 7.4 KiB（nginx 29，Caddy 和 Traefik 43 到 46）。p99 在所有场景都低于 Caddy 和 Traefik，在
  HTTP/1.1 上高于 nginx。路线图中 M1 的所有目标都满足。
- 为在压测机上运行，压测框架新增 `-root` 和 `-bin`（为没有 Go 的主机使用预先构建的二进制）；`idleconn` 从多个回环
  地址发起连接（`-sources`，每 40k 一个），用 `IP_BIND_ADDRESS_NO_PORT` 绑定，并以 RST 关闭，这样 100k 连接的端口够用，
  也不给下一次运行留下 TIME_WAIT；fit 现在也服务压测框架的 TLS 空闲连接。
- `go vet ./...` 和 `go test -race ./...` 通过。

与计划的不同：

- 没有把修复拆成新迭代：期间到来的审查规划了 R01–R07，报告中的线索交给它们。代理在 HTTP/1.1 上是 nginx 的 83%
  （fit 用 6.1 个核，nginx 用 4.7 个），以及 fit 在 HTTP/1.1 上的 p99 长尾，由 R07 研究；下载路径的等待由 R04 去掉。
- 100k 时的 WebSocket 空闲内存对所有服务器都无法测量：代理到唯一一个 WebSocket 后端地址的连接在 64504 个时用尽端口。
  压测框架需要在 I47（fit 代理 WebSocket）之前支持多个后端地址。nginx 在其余连接还在建立时，按 75s 的
  `keepalive_timeout` 关闭了 100k 空闲连接中的约 45k，所以应比较它 10k 时的数字。
- 与 M0 基线（M3 Max 上的 Docker Desktop）相比，fit 的 fixed HTTP/1.1 吞吐从 nginx 的 1.25 倍变为 0.96 倍，空闲
  HTTP/1.1 内存从 0.9 KiB 变为 1.5 KiB。两台机器不同；R07 会在这台主机上测量两个提交，确定是不是 fit 的变化。

### R01 慢客户端的等待有上限 — 已完成

- 配置：新的顶层 `limits` 对象中的 `limits.write_stall_timeout`（默认 30s），`Limits.WriteStall()` 给出默认值。
  `runtime.Compile` 通过 `module.Env` 把它交给模块；`module.Context.WriteStallTimeout()` 在 provision 时读取。
- 代理的下载路径通过 `exchange.write` 写出每一段：`SetWriteDeadline(now + stall)`、`Write`、`FlushError`，然后解除
  截止时间。不再读取的客户端会在截止时间过后写入失败（fib 关闭其 HTTP/1 连接或重置其 HTTP/2、HTTP/3 流），上游响应
  用 `ClientResponse.Close` 放弃，worker 回到上游连接上。`Finish` 也带截止时间。
- 静态 handler 把 Context 包装为 `stallWriter` 交给 `ServeContent`：每次 `Write` 使用同样的“设置再解除”截止时间；
  `ReadFrom` 在明文 HTTP/1 连接上仍走 fib 的 `ReadFrom`（sendfile，不等待），在 TLS、HTTP/2、HTTP/3 上则通过有上限的
  `Write` 复制——这些情况下 fib 原本会通过会等待的写来复制。不支持截止时间的 Context（`http.ErrNotSupported`）就不带
  截止时间写入。
- 测试（`handler/proxy/stall_test.go`），stall 超时 1s，客户端覆盖 HTTP/1.1、h2c 和 h3：审查中的两种情况——一个客户端
  不再读取来自 `max_conns: 1` 的 h2c 上游的 200MB 下载，以及八个这样的客户端对 `max_conns: 4` 的 HTTP/1.1 上游；两种
  情况下另外十个请求都得到服务（分别在 stall 后 1s 和 2s 内），每个卡住的客户端的响应都被截断。去掉截止时间运行一次，
  其他请求会在 10s 后超时，与审查测到的一致。慢慢读取的客户端，以及暂停两倍 stall 时长的上游，都能拿到完整响应。
  `handler/static`：不再读取 64MB 文件的客户端在 h2c 和 h3 上响应被截断（去掉截止时间则拿到全部）。`config` 测试新
  上限。`go vet ./...` 和 `go test -race ./...` 通过；流式测试仍然通过。

与计划的不同：

- `write_stall_timeout` 放在顶层 `limits` 对象中，而不是每个 listener 上：用它限制写入的 handler 被一个路由服务的所有
  listener 共享，而 R02 也把 listener 的其他上限放在 `limits` 中。
- 截止时间在每次写之前设置、之后解除，而不是至多每秒刷新一次：即使没有等待，fib 也会在截止时间过后重置 HTTP/2 或
  HTTP/3 流，所以一直保留的截止时间会截断上游暂停的响应，比如 server-sent events。与 nginx 的 `send_timeout` 一样，只
  计算写入等待客户端的时间。
- 没有设置 listener 的 `WriteTimeout`，也没有设置 fib HTTP/3 的：它们限制的是整个响应，会截断长时间的下载。
- 明文 HTTP/1 连接上通过 sendfile 发送的文件没有截止时间：没有 worker 等待它，客户端没取走的数据在内核中。R05（异步
  文件发送）会给它加上。
- 没有做前后对比的基准：压测框架的场景走的是 R01 没有改动的路径（一次写出的小代理响应、没有条件的缓存文件）。

### R02 安全的默认上限 — 已完成

- 上游：`max_conns` 默认每个地址 256（原来不限）；`max_pending`（1024）限制等待连接的请求数，超过时立即返回 503；
  `response_timeout`（1h）通过 fib 客户端的 `Timeout` 限制整个响应，所以响应开始前超时返回 504，开始后则被截断。在
  fib 能统计等待请求之前（R06），连接池按 `max_conns × 地址数 × 流数 + max_pending` 统计从 `Pick` 到 `Done` 的在途请求，
  HTTP/1.1 连接按 1 个流，HTTP/2 连接按 100 个（RFC 9113 的下限）；超过时 `Pick` 返回 nil，`Pool.Do` 报告
  `upstream.ErrBusy`。
- `limits` 新增 `max_pending_bytes`（256 MiB），设置为 listener engine 的 fib `MaxPendingBytes`；以及
  `max_header_bytes`（1 MiB）和 `max_body_bytes`（16 MiB），设置到每个 listener 的 HTTP/1、HTTP/2、HTTP/3 配置中。
  `max_body_bytes` 也限制流式 body（`MaxStreamedBodyBytes`）：自 I16 起超过 64 KB 的 body 都以流方式到达，仅靠 fib 的
  `MaxBodyBytes` 它们是不受限的。修改了头或 body 上限的重载会重新绑定 listener；engine 的预算需要重启才生效。
  `listener.Server.Stats` 返回 engine 的计数器。
- `static` 新增 `browse_max_entries`（10000）：目录列表最多读取这么多条目并排序，目录还有更多时会注明，而不是整个读取。
- 所有默认值都在 schema 中：新增 `default` 结构体标签（类型首次读取时检查是否为 JSON），schema 输出为 JSON Schema 的
  `default`；共 23 个字段带有默认值（上游和访问日志的字段、`limits`、`respond` 的状态码、`static` 的参数）。依赖其他字段
  的默认值（listener 的协议、`load_balancing`）仍只写在描述中。`examples/proxy.json` 把上限和上游设置按默认值写出。
- 达到每个上限的测试：对一个地址打开 256 个连接，超过 `max_pending` 的请求立即被拒绝（`upstream`）；经过代理超过
  `max_pending` 返回 503，`response_timeout` 在响应前和响应中触发（HTTP/1.1 和 h2c），以及超过 `max_body_bytes` 的流式
  上传：声明长度的返回 413，分块的被中止（`handler/proxy`）；超过 `max_header_bytes` 的头返回 431，超过 `max_body_bytes`
  的整体和流式 body 返回 413（HTTP/1.1 和 h2c，`listener`）；一个不读取的客户端耗尽 `max_pending_bytes`，fib 的计数器
  显示待发字节超过预算并因此暂停读取，直到客户端开始读取（`listener`）；目录列表在 `browse_max_entries` 处截断
  （`handler/static`）；以及 schema 中的默认值（`schema`）。代理测试共用的配置为 256 MB 流式测试调高了 `max_body_bytes`。
  `go vet ./...` 和 `go test -race ./...` 通过。
- 前后对比基准（压测机，只跑 fit，各两轮）：static 在 HTTP/1.1 和 h2c 上每秒 577k 和 1.33M 请求，proxy 215k 和 228k，
  前后差异在 0.5% 以内。压测框架的 fit 配置现在为后端设置 `max_conns: 4096`（其他服务器保持 4096 个连接）：h2c 代理
  场景有 2048 个在途请求，超过了新的默认值。

与计划的不同：

- 上限测试没有在 fit 或 fib 中发现问题，只发现 I14 的测试辅助函数 `newBackend` 的连接钩子在关闭超过 64 个连接后会阻塞，
  卡住后端：任何突发超过约 96 个连接的测试都会挂起。现在它的发送不再阻塞。
- `max_pending_bytes` 是 fib 的预算，fib 在一轮读取结束后检查它，所以预算耗尽时一次读完的请求仍会被服务；它阻止的是后续
  的读取，测试通过 fib 的计数器展示了这一点。fib 提醒少数不读取的对端可能占住预算。流式发送的对端会被 R01 的 stall 超时
  切断；整体排队响应的对端不会，能有多少这样的对端由 R06 的连接上限来限制。
- `max_conns`、`max_pending` 或 `limits` 字段显式写 0 等同默认值，因为配置无法区分这些字段的 0 和未写；没有上限可以被
  关闭。

### I24 配置版本与 Admin API 骨架 — 已完成

- `admin.Configs` 是新配置的唯一路径，无论来自 SIGHUP 还是 Admin API（以后还有文件 provider）：
  `Apply(data, source)` 加载、编译配置，并用 `listener.Server.Reload` 把服务器切换过去，全程持有一把锁，所以多次应用
  依次进行；任何一步失败，运行中的配置保持不变。它按原样保存最近 `admin.history` 个配置（10 个），连同
  `runtime.Runtime.Swap` 给每个图的版本号、应用时间和来源。`fit run` 的 SIGHUP 重载现在走这条路径，输出不变。
- Admin API（`admin.Server`）基于 net/http，配置里没有 `admin` 时不开启：`GET /config` 按原样返回运行中的配置，版本号
  在 `Fit-Config-Version` 中；`POST /load` 应用请求体，返回新版本号、新增和移除的 listener，以及警告（`access_log` 或
  `admin` 的修改需要重启才生效）；配置有错时返回 400，带每个错误的路径、行、列和信息；listener 无法绑定时返回 500；
  `GET /versions` 列出保存的配置，`GET /versions/{n}` 返回其中一个。`fit run` 打印 `fit: admin API on <地址>`，API
  每应用一个配置也打印一行。
- 配置：顶层的 `admin`，包括 `address`（127.0.0.1:2019，或 `unix/<路径>` 表示 Unix 套接字，权限 0600；已退出进程留下
  的套接字会被替换，仍在使用的则拒绝）、`token_file`（每个请求都必须携带的 bearer 令牌，按常数时间比较）、`history`、
  `max_config_bytes`（4 MiB）和 `max_conns`（16）。除非设置了 `token_file`，地址必须是回环地址或套接字。带 `Origin`
  的请求得到 403，`POST /load` 只接受 `application/json`，在没有令牌的 TCP 地址上 `Host` 字段必须是回环主机名，所以
  网页和 DNS 重绑定都无法到达 API。超时：头部 10s，请求体和响应 60s，空闲 60s；头部最多 64 KiB。
- 测试：`admin` 加载配置并读回，listener 按新配置服务；拒绝无法解码、编译或绑定的配置（400、400、500），以
  `text/plain` 发送的配置（415），以及超过 `max_config_bytes` 的配置，带长度的（读取前 413）和分块的（到达上限时 413），
  运行中的配置和历史都不变；令牌（缺失、错误、其他方案、正确；空的令牌文件）；Origin 和 Host 检查；历史裁剪到
  `history`；八个并发加载，每个都完整应用并得到自己的版本号；一个停止发送配置的客户端在读取超时时被切断，期间其他请求
  照常服务，一个中途断开的客户端不应用任何东西；超过 `max_conns` 的连接立即关闭，其他连接离开后又能服务；Unix 套接字、
  它的权限、替换残留的套接字和拒绝正在使用的套接字；以及警告。`config` 测试新的检查；`test/e2e/admin_test.go` 运行
  带 API 的 fit，加载一个新增 listener 的配置，从 `/config` 和 `/versions/1` 读回，被拒绝一个错误的配置，并看到
  SIGHUP 重新应用文件，成为版本 3。`go vet ./...`、`GOOS=windows go vet ./...` 和 `go test -race ./...` 通过。
- 没有做基准：数据面没有变化；API 在它旁边、在自己的 goroutine 上运行。
- 决策记录 [005](decisions/005-admin-api.zh-CN.md)：API 用 net/http，默认关闭，令牌放在文件里，配置按原样保存。

与计划的不同：

- 配置里没有 `admin` 时 Admin API 不开启（`"admin": {}` 在默认地址开启它），而 Caddy 默认开启；见决策 005。
- `runtime` 没有修改：`Runtime.Swap` 已经给图编号；历史属于控制面，与应用路径一起放在 `admin` 中。
- 计划中没有的 `GET /versions` 和 `GET /versions/{n}` 用来查看历史；用 `/versions` 而不是 `/config/versions`，因为
  I25 会把 `/config` 下的路径用作 JSON pointer。
- 历史只在内存中；SIGHUP 会用文件覆盖通过 API 加载的配置，重启从文件开始。
- 端口为 0 的 listener 只有在新配置中仍写作端口 0 时才会保留：listener 按书写的地址匹配，所以 e2e 测试中保留的那个
  仍写 `127.0.0.1:0`。

### I32 主动健康检查 — 已完成

- 配置：上游的 `health_check`，包括 `type`（默认 `http`，或 `tcp`）、`path`（`/`）、`host`、`port`、
  `expect_status`（未写时为 200–399）、`interval`（5s）、`timeout`（2s，不超过 interval）、`jitter`（interval 的十分之一，
  小于 interval），以及 `fails` 和 `passes`（各 2）：连续多少次探测失败使地址退出轮转、连续多少次成功使它回来。`tcp`
  检查上写 `path`、`host` 或 `expect_status` 会被拒绝。
- `upstream` 的探测器由 `upstream.New` 为有 engine 的连接池创建，由 `Pool.Close` 停止（所以 `fit validate` 不探测）：
  每个后端的探测依次进行，每次由 `time.AfterFunc` 定时器发起、在 fib 的回调中结束，下一次安排在上一次开始后
  `interval` 加一个随机抖动，第一次在启动后一个抖动之内。没有 goroutine 等待探测，也没有探测运行在 listener 的
  worker 上。http 探测是一个 GET，经由一个 `fibhttp.Client` 发出，协议和 TLS 设置与连接池相同，但连接是它自己的（每个
  地址两个，不保留空闲连接），这样探测能看出后端是否还接受新连接，也不会排在连接池的请求后面；回调在响应头到达时就
  运行（`StreamResponseBody`，阈值 0），body 用 `ClientResponse.Close` 放弃。tcp 探测是 `Engine.DialWithHandler`，连上
  后立即关闭。
- 结果写入后端的 `downUntil`，即 `Pick` 唯一读取的字段：探测判定后端下线时为 `math.MaxInt64`，否则为被动健康检查设置
  的值。探测和 `max_fails` 各自独立地让后端退出轮转，请求成功不会让被探测判定下线的后端回来。`Pick` 没有变化；所有
  后端都下线时仍忽略健康状态。`Backend.ProbedDown` 报告探测的判定。
- 测试（`upstream/probe_test.go`）：一个健康接口变为 503 的后端在配置的时间窗口内（两次探测的间隔和抖动加上第二次的
  超时，为负载高的主机放宽一倍）退出并重新加入轮转，期间经连接池的请求都避开它；`fails: 2` 下单次失败不会让它退出；
  对一个关闭又重新打开的端口做 tcp 探测；慢上游（超时后才应答，或接受连接后从不应答）和中途断开的连接（响应头被截断）
  会被移出，1 GiB 的 body 不会被等待；`expect_status`、`host` 和 `port`；经 h2c 和带 CA 文件的 h2 探测；连接池关闭后
  探测停止；探测连接不被保留；探测与被动健康检查同时生效。`config` 测试新的检查和默认值。`go vet ./...`、
  `GOOS=windows go vet ./...` 和 `go test -race ./...` 通过。
- `examples/proxy.json` 加入了健康检查和 Admin API。
- 没有跑压测框架：请求走的路径与之前相同（`Pick` 读取同一个字段），没有 `health_check` 的配置（压测框架的配置就是
  这样）不会启动探测。`go test -bench BenchmarkPick`（并行，14 线程，本机 Mac）前后对比：轮询 54 和 57 ns，加权轮询
  106 和 107，最少连接 66 和 66，在该基准两次运行之间的波动范围内。

与计划的不同：

- 重载会创建新的连接池（I22 起如此），所以每次重载或经 Admin API 加载后，健康状态从所有后端在线重新开始，原先下线的
  后端会先回到轮转，直到再失败 `fails` 次探测。I26 可以在上游未变的图之间共享连接池，届时状态得以保留。
- 每个地址最多两个探测连接，而不是一个：在 race 检测下，一个放弃了大 body 的探测，其连接在 fib 客户端中最长要 750ms
  才关闭完，下一次探测因等待它而超时。fit 的其他部分不受此影响。
- 后端退出或重新加入轮转时没有日志；I34 的指标会显示它。

### I25 Admin API：局部修改、ETag、回滚 — 已完成

- `GET /config/<pointer>` 返回运行中配置里 JSON pointer（RFC 6901）指向的值；`PUT` 把它设为请求体（替换成员或元素、
  新增成员、在 `-` 处追加），`PATCH` 把请求体作为 JSON merge patch（RFC 7396，以 `application/merge-patch+json` 或
  `application/json` 发送）合并进去，`DELETE` 删除它。单独的 `/config` 表示整个配置：`PUT /config` 像 `POST /load`
  一样替换它，`DELETE /config` 被拒绝。不存在的成员或下标得到 404，不是 JSON（或有重复键）的请求体得到 400。
- 每次修改都通过 `Configs.Update` 应用一个完整配置：它在给应用排序的那把锁下取运行中的配置、做修改并应用结果，所以
  多个写入者的修改都基于前一个修改。让配置失效的修改连同错误一起被拒绝，什么都不变。
- 每个版本都有 ETag，即配置原文 SHA-256 的前 96 位，在每个关于配置的应答中以 `ETag` 发送，并由 `GET /versions`
  列出。带 `If-Match`（一个 ETag、一组 ETag 或 `*`）的修改只在运行中的配置仍是那一个时才做，否则返回 412；
  `POST /load` 和回滚也支持它。`If-None-Match` 指向运行中的配置时，`GET /config` 返回 304。
- `POST /rollback/{n}` 把版本 `n` 的配置作为新版本再次应用，来源为 `rollback`；已不在历史中的版本得到 404。
- 修改后的配置由 fit 写出（`admin/jsonptr.go`：文档的树，成员保持顺序、数字保持原样，每层缩进两个空格）；整体发送的
  配置按原样保存。
- 测试（`admin/edit_test.go`）：pointer 及其转义；在文档中读取、设置、追加、删除和合并，以及成员不存在、下标越界或
  有前导零、对标量取下级时的错误；顺序、数字和字符保持不变；读取一个值、`PUT`、`PATCH`、追加和删除路由，每次都立即
  生效，以及写出的配置；验证失败（地址、模块、必填字段、未知字段）、不是 JSON 或指向不存在位置的修改被拒绝，什么都不变；
  ETag、`If-None-Match`，以及用正确的、过期的、列表中的和 `*` 的 ETag 做 `If-Match`，过期的 ETag 对各种修改都被拒绝；
  八个写入者读取、修改并带 `If-Match` 写回，遇到 412 就重试，旁边八个不带它追加，最终十六条路由都恰好出现一次，版本数
  也对应；回滚到仍保存的版本、带 `If-Match` 回滚，以及回滚到已不在的版本。`go vet ./...`、`GOOS=windows go vet ./...`
  和 `go test -race ./...` 通过。
- 决策记录 [005](decisions/005-admin-api.zh-CN.md) 补充了这些格式：pointer、merge patch、ETag、回滚。

与计划的不同：

- `PATCH` 是在 pointer 处做 JSON merge patch，而不是替换那里的值（`PUT` 已经能做）；不接受 JSON Patch（RFC 6902）文档。
- ETag 是配置的哈希，而不是版本号，因为重启后版本号会重新开始。
- 修改后配置的排版是 fit 的，不是原来书写的样子。

### I33 负载均衡 v2 — 已完成

- `load_balancing` 新增 `random`（按权重）、`p2c`（随机抽两个后端，取按权重计在途请求较少的那个），以及 `ip_hash`、
  `uri_hash`、`header_hash` 和 `cookie_hash`：按客户端 IP 地址（不含端口）、路径和查询串、或 `hash_key` 指定的请求头
  或 cookie 做一致性哈希。没有该请求头或 cookie 的请求按轮询分配。`sticky_cookie`（`name`、`path`、`max_age`、
  `secure`；总是 `HttpOnly` 和 `SameSite=Lax`）在任何策略之上把客户端固定在一个地址：cookie 指向一个在线地址的请求
  发往那里，其他请求的应答带上它所去地址的 cookie，排在上游自己的 `Set-Cookie` 之后。cookie 的值是地址 SHA-256 的前
  64 位，因此在地址相同的每个 fit 中都一样，也不透露地址的其他信息。
- 哈希环为每个后端每单位权重放 160 个点，只由地址决定位置（FNV-1a 加 MurmurHash3 的收尾），与 nginx 的
  `hash ... consistent` 相同；一个键落到哈希值处或之后的第一个点，该后端下线时落到下一个后端在线的点，所以下线后端的键
  会移走、随它恢复而回来，其他键都不动。所有策略都跳过下线的后端，除非全部下线，与以前一样。
- `Balancer.Pick` 和 `Pool.Pick` 接收请求（没有请求时为 nil，如 `Pool.Do` 的调用者）；`Pool.SetCookie` 给出代理要加到
  应答上的内容。请求路径上没有内存分配：键直接从请求中读取，请求头名称和 `Set-Cookie` 字段在创建连接池时就已准备好。
- 测试：`random`（均匀和按权重）和 `p2c`（三个中最忙的从不被选、请求保持时各后端持平、按权重、只有一个在线）的分布；
  每种键的哈希分布，均匀和按权重；相同的键去相同的后端；加入第四个后端只把键移给它，约四分之一；一个后端下线只移走它
  自己的键，随它恢复而回来；`ip_hash` 不计端口，`uri_hash` 计入查询串；cookie 解析；没有键的请求按轮询；新策略和粘性
  cookie 每次选择都不分配内存；跳过下线后端；`upstream` 中的粘性 cookie（没有时设置、有时遵从、其后端下线或指向不存在
  时替换、属性），以及经过代理时保留上游自己的 cookie、`header_hash` 让每个用户固定在一个后端（`handler/proxy`）；配置
  错误。`go vet ./...`、`GOOS=windows go vet ./...` 和 `go test -race ./...` 通过。
- 前后对比基准（压测机，只跑 fit，proxy 场景，按压测框架配置用轮询，各两轮，交替进行）：HTTP/1.1 之前每秒 215k 和
  216k 请求，之后 215k 和 213k；h2c 之前 228k 和 228k，之后 228k 和 227k，差异在 1% 以内，与同一二进制两轮之间的差距
  相当。`go test -bench BenchmarkPickNew`（并行，14 线程，本机 Mac）：新策略每次选择 1.5 到 4 ns，cookie_hash 最慢，
  都不分配内存。

与计划的不同：

- 粘性会话是 `load_balancing` 旁边的 `sticky_cookie`，而不是一种单独的策略，这样没有 cookie 或其地址下线的客户端可以
  由任何策略来分配。
- 一致性哈希用哈希环（与 nginx 相同），而不是 rendezvous 哈希：无论有多少地址，查找都是一次二分查找。

### I26 监听差量与旧图排空 — 已完成

- 监听差量在 I22 已经完成：未变的 listener 保留，新的在替换前用 `AddBinding` 加入，移除的用 `ShutdownBinding`
  排空。I26 补上让长请求留在它开始时的图上、以及结束它们的部分。
- 在返回后仍服务请求的 handler 为请求持有它的图：`module.Env` 带上图，`module.Context.Graph()` 返回一个
  `module.Holder`，代理在一次转发开始时持有，在 `exchange.release` 中释放。图、它的模块和连接池一直保留到其上最后
  一次转发结束。
- `Runtime.SetDrainTimeout`（默认 30s；`listener.Server.SetDrainTimeout` 也会设置它，所以即 `fit run` 的
  `-shutdown-timeout`）：`Swap` 为换下的图启动一个定时器，图被清理时停止；定时器触发时，实现 `module.Drainer`
  的模块结束它们仍在处理的请求。代理让每次转发的 context 派生自 32 个排空 context 之一（随机选择，使同时开始的
  转发不共用一把锁），`Drain` 把它们全部取消：响应还没开始的得到 503，正在进行的被截断，两者都不算后端的失败。
- 图之间共享连接池：`runtime.Env.Previous` 指向被替换的图，配置（按 JSON）和 CA 文件内容都未变的上游用
  `Pool.Acquire` 接管它的连接池；`Pool.Close` 释放一次持有，最后一次关闭连接池。连接和主动健康检查（I32）的状态
  跨重载保留；变化了的上游得到自己的连接池。`admin.Configs` 传入运行中的图。
- 测试：`runtime` 在超时后排空仍被请求持有的图、超时前不排空，请求释放后清理它，请求先结束或超时为负时从不排空；
  共享未变上游（含 CA 文件）的连接池，不共享变化了的或 CA 文件变化了的。`upstream`：被持有两次的连接池。
  `handler/proxy`：跨越重载的下载经共享的连接池继续传输直到排空超时，然后被截断，而重载后开始的下载继续，后端仍在
  轮转中；排空时还在等待响应的请求得到 503。`test/e2e/churn_test.go` 通过 Admin API 替换配置 2000 次（分布在六秒内），
  同时八个客户端发送短请求（HTTP/1.1 和 h2c、长连接和新连接、固定响应和代理），四个客户端下载持续一秒的流；每次替换
  都改变固定响应，每 50 次改变上游，每 100 次增减一个 listener：三次运行中每次约 337,000 个短请求、32 次下载，没有
  失败。`go vet ./...`、`GOOS=windows go vet ./...` 和 `go test -race ./...` 通过。
- 前后对比基准（压测机，只跑 fit，proxy 场景）：HTTP/1.1 每秒 216k 和 214k 请求，h2c 228k 和 229k，差异在 1% 以内。
  只有一轮有效：第二轮时压测机上开始了另一个基准测试（一个占用四个核的 WebSocket 服务器），两个版本都下降了 8% 到
  33%；为了不干扰它，重跑已停止。待压测机空闲时再测一次。

与计划的不同：

- 流失测试中的长连接是经代理的流式下载，不是 WebSocket，WebSocket 在 I47。
- 被排空的是在返回后才应答的 handler 的请求，即代理的；handler 仍在运行的请求（正在发送的静态文件）通过分发器持有
  图，按原来的方式结束。
- 每次代理转发都要把自己的 context 注册到代理的一个排空 context 上，每请求多一次加锁；分散在 32 个 context 上，
  基准测试中看不出影响。

### I27 文件 provider — 已完成

- `provider/file.Watcher` 在独立的 goroutine 上每隔一个间隔（1s）查看配置文件的大小和修改时间；变化之后保持不变达到
  防抖时间（500ms）时读取文件（最多 64 MiB），若其 SHA-256 与上次应用的不同就应用它。编辑器的多次写入只触发一次重载，
  touch 不触发，通过重命名替换的文件和原地写入的一样能被发现。失败的配置、不存在或过大的文件只报告一次，监视继续：下一次
  编辑会重新尝试，文件恢复后会被应用。`Applied` 记录通过其他方式应用的内容。
- `fit run -watch`（以及 `-watch-interval`）启动它，打印 `fit: watching <文件>`；文件变化时打印
  `fit: <文件> changed; reloading`，并像 SIGHUP 一样通过 `admin.Configs.Apply` 应用，输出相同；SIGHUP 会告诉监视器它
  应用了什么。关闭开始前先停止监视，这样关闭期间不会应用配置。
- 测试（`provider/file`）：一次编辑被应用，重命名替换的被应用，touch 不被应用；连续四次写入只应用一次，即最后一次；错误
  的编辑只报告一次，之后的修正被应用；文件被删除时报告一次，恢复后被应用；超过上限的文件被拒绝；SIGHUP 已应用的内容
  不再应用。`test/e2e/watch_test.go` 运行 `fit run -watch`，编辑文件后看到新路由生效，错误的编辑在 stderr 中报告且运行中的
  配置保持不变，修正后被应用。`go vet ./...`、`GOOS=windows go vet ./...` 和 `go test -race ./...` 通过。
- 决策记录 [006](decisions/006-file-provider-polls.zh-CN.md)：采用轮询而不是文件系统事件。
- 没有做基准：数据面没有变化。

与计划的不同：

- 监视器采用轮询，而不是文件系统事件；见决策 006。
- 它由 `fit run` 的参数开启，而不是在配置中。

### R07a fit 自身的热路径开销 — 已完成

R07 拆成三部分（[开发计划](dev-plan.zh-CN.md#r07a-fit-自身的热路径开销)）：R07a 和 R07b 不需要 fib，R07c 等待 A7 和
A11–A13。

- 访问日志（`observe`）：缓冲拆成若干分片，数量是不超过 GOMAXPROCS 的 2 的幂（最多 64），每个分片有自己的锁，彼此填充
  隔开。一行日志按客户端地址（`RemoteAddr` 的 `maphash`）进入对应分片，所以同一连接的日志保持顺序。分片里满 64 KB 时，
  在自己的锁下把缓冲经一个有界 channel 交给写出 goroutine；每次刷新时，写出 goroutine 也以同样方式把每个分片未满的缓冲
  放进这个 channel，所以 channel 中每个分片的缓冲都按填充的顺序排列。`buffer_bytes` 限制的是等待写出的字节加上各分片持有
  的字节。分片对照一个原子计数检查这个上限，计数在每次交出缓冲时更新一次，而不是每行一次，所以每个分片最多可以超出这个
  上限 64 KB。写出后的缓冲经一个有界的空闲列表复用。日志行的时间戳改为手写格式化，不再用 `Time.AppendFormat`；JSON
  字符串转义改为查字节表。
- `observe.Track(r)` 取代 `Attach`：日志条目和携带它的 context 合并为一次分配（`tracked`，一个 `Value` 方法会返回该条目
  的 `context.Context`）。
- `listener`：每个分发器记住自己的 listener 在上次查找的图中的位置（图版本和下标，放在一个原子指针后面），所以每张图只有
  第一次请求会扫描图中的 listener。
- `upstream`：平滑加权轮询在创建负载均衡器时一次算出长度为 `总权重 / 最大公约数` 的调度序列（最多 65,536 项，超出时仍在锁
  下计算），每次选择用一次原子加法取下一项。遇到已下线的后端就再取一项，所以它的份额按权重分给其他后端；全部下线时与
  之前一样忽略健康状态。
- 测试：`observe` 的 `TestShards`（GOMAXPROCS 为 8 时 16 个客户端同时写日志；每行要么写出要么计为丢弃，每个客户端的行
  保持顺序，最后没有残留计数）、`TestTrack`、`TestAppendTime`；`listener` 的 `TestDispatchFollowsReload`（重载后 listener
  在图中位置变化，以及一张图中它没有路由）；`upstream` 的 `TestWeightedRoundRobinDown`、`TestWeightedRoundRobinSchedule`
  （最大公约数，以及多到无法预先计算的权重）、`TestWeightedRoundRobinConcurrent`（8 × 7000 次选择份额精确）。新增的
  `BenchmarkDispatch` 打开访问日志，经分发器服务一个 `respond` 路由，每个 P 作为一个独立客户端；`BenchmarkLog` 也改为
  这样。`go vet ./...` 和 `go test -race ./...` 通过。
- 开发用 Mac（M3 Max）上的微基准，前后对比，五次取中位数，GOMAXPROCS 为 1、8、16 时的 ns/op：`BenchmarkLog` 359、
  335、424 → 223、49、34；`BenchmarkDispatch` 1009、750、852 → 969、485、387，分配 13 → 12 次；
  `BenchmarkPick/weighted_round_robin` 17.2、116、115 → 2.5、45、57，与普通轮询相同，现在的开销在共享计数器上。
- 压测主机上只跑 fit，两个构建交替各跑两轮，每秒千次请求（HTTP/1.1 和 h2c）：不开访问日志时，固定响应 781 和 3,768 →
  780 和 3,770，代理 212 和 228 → 212 和 229，差别都在 0.4% 以内；访问日志写到 `/dev/null` 时，固定响应 732 和 2,108 →
  752 和 2,296（+2.7% 和 +8.9%），代理 207 和 226 → 208 和 228。

与计划的不同：

- 分发器查找 listener 用的是按图版本的缓存，而不是编译时建立的索引：分发器比图活得久，而图中的 listener 切片不变，所以
  索引就是位置，找一次即可。
- 健康状态变化时不重建调度序列：遇到下线的后端就再取一项，在线后端之间的份额同样按权重分配，也不需要在探测 goroutine
  上重建。
- 访问日志仍让固定响应在 h2c 上损失约 40% 的吞吐（不开时 377 万，开时 230 万）。`BenchmarkDispatch` 的 12 次分配中有
  3 次是 fib 因为注册了 `OnFinish` 钩子而复制响应头，但这种钩子不能修改响应头：已作为
  [A13](fib-api-requests.zh-CN.md#a13只有-onfinish-钩子的响应不复制响应头) 向 fib 提出。`r.WithContext` 复制请求要等
  A7。两者都归 R07c。

### R07b 代理的内存分配 — 已完成

- `handler/proxy`：先分配 exchange，由它持有构造发往上游请求所需的东西：请求本身（`base`，`out` 是 `WithContext` 从它
  复制出的副本）、它的 URL 和四个转发字段的值，请求头中的切片指向这些值。`clientAddr` 同时返回客户端地址的字符串：
  IPv4 地址直接取 `RemoteAddr` 的一部分（netip 只接受规范形式，所以它已经是规范的），只有 IPv6 才格式化，且只做一次而不是
  两次；`forwardedElement` 用一个预先定好大小的 `strings.Builder` 构造。整体到达的响应用 `io.ReadFull` 读进与其长度相同的
  缓冲，而不是经过会超额增长的 `bytes.Buffer`；400 和 503 的响应体改为包级变量。
- `BenchmarkProxy`（`handler/proxy/bench_test.go`）：用一个几乎不分配的原始客户端，经 keep-alive 连接把小的 GET 请求经
  代理发到 HTTP/1.1 后端。为此测试辅助函数 `startFit` 和 `serveBackend` 改为接受 `testing.TB`。`TestForwardedElement` 检查
  `clientAddr` 对 IPv4、映射地址、IPv6 和带 zone 的地址的结果，以及 IPv4 地址不分配。`go vet ./...` 和
  `go test -race ./...` 通过。
- 开发用 Mac 上：每个代理请求的分配从 95 次、13.0 KB 降到 81 次、11.3 KB（包括 net/http 后端）；fit 自身代码现在分配的是
  exchange、请求头 map、`OnCancel`、定时器和 `onResponse` 的闭包及响应头定时器、context 和它的请求副本、Forwarded 元素和
  响应体读取。其余来自 fib 的客户端、后端和压测客户端：内存 profile 是
  [A11](fib-api-requests.zh-CN.md#a11不复制即取得整体到达的客户端响应体) 和
  [A12](fib-api-requests.zh-CN.md#a12减少每个客户端请求的定时器和-context) 的证据，其中包括每个请求四个定时器。
- 压测主机上，R07a 的构建与本构建交替各跑两轮，每秒千次请求：HTTP/1.1 代理 211.7 → 219.0（+3.4%），p99 3.91 → 3.51 ms，
  h2c 代理 228.4 → 229.6；打开访问日志时 204.5 → 211.8（+3.6%）和 226.3 → 228.1。
- I23 留下的 M0 问题在压测主机上有了结论：`f96aa03`（M0，fib v1.2.0）的 fit 与本构建，用 M0 的配置各跑两轮：固定响应在
  HTTP/1.1 和 h2c 上 792 和 3,929 → 782 和 3,764（−1.3% 和 −4.2%），常驻内存 19 → 28 MB。所以从 M0 基线到 I23，fit 的
  固定响应吞吐从 nginx 的 1.25 倍降到 0.96 倍，是换了机器（M3 Max 上的 Docker Desktop 换成 Linux 主机）造成的，不是 fit；
  属于 fit 的那几个百分点交给 R07c。

与计划的不同：除了交给 fib 的部分外没有。代理的响应头定时器保留到 fib 的客户端提供相应设置为止（A12）。

### R03 转发不等待所需的 fib API — 已完成

- fib v1.4.0（tag 在 `ecd62b0`）提供了 A1–A7、A9、A11、A13，以及 A8 和 A12 的一部分。fit 升级到该版本，自身代码不变：
  只改了 `go.mod` 和 `go.sum`。在它之上 `go vet ./...` 和 `go test -race ./...` 通过。
- [fib-api-requests](fib-api-requests.zh-CN.md) 及其英文版写明了每个请求的状态、提交，以及实现的 API 与请求的差别。对后续
  迭代重要的差别：`fib.Config.MaxConns` 作用于整个引擎，不能按 binding 设置（R06）；`MaxPendingPerHost` 只在 `http` 中
  （R06）；`ResponseHeaderTimeout` 是客户端配置项，正适合每个上游池一个客户端（R07c）；响应开始流式发送之前 `OnWritable`
  返回 false（R04）；`SendFile` 没有接入 `FileCache`，206 和 `Content-Range` 由 handler 设置（R05）。
- fib 中没有做的：`WriteStallTimeout`（A8），有了 `WriteNoWait` 已不需要；每个可取消请求上的 `context.AfterFunc`（A12），
  需要 `Do` 返回取消句柄。fib 记录的问题：复用的连接已关闭而重新排队的请求，其响应头超时不会重新计时。

与计划的不同：没有。按规则 15，fib 中的工作由 fib 自己的会话完成。

### R04 代理下载不再等待 — 已完成

- `handler/proxy`：下载路径不再等待。上游响应体的每一段用 `Context.WriteNoWait` 写给客户端，除最后一段外都刷新；一旦它
  报告排队给客户端的数据超过响应的上限（64 KB），就用 `ClientResponse.HoldBody(true)` 暂停上游响应体，`Context.OnWritable`
  在排队数据降到一半时恢复。暂停期间，HTTP/1 上游连接不再读取，HTTP/2 或 HTTP/3 上游只是不再给这个流窗口，所以读得慢的
  客户端只拖住自己的下载，不影响别的。`write_stall_timeout` 仍然限制每次等待：暂停时设置写截止时间，恢复时取消；截止时间
  到了或客户端离开时，`OnWritable` 的函数会得知，并放弃这个响应。exchange 中的一把互斥锁让响应体的回调（在上游 worker
  上，恢复后在 fib 的 side pool 上运行）与 `OnWritable` 的回调依次执行。
- 带 trailer 的上传改为普通的推送式请求体：一个 `BodyWriter`，以分块方式发送，`Trailer` 中预先写好声明的名字，结束时用
  `CloseWithTrailer` 交出客户端请求体结束时带来的值。`trailerPipe` 以及 fib 客户端为读取它而占用的 goroutine 都去掉了。
- 测试：`TestStuckClientWithoutStallTimeout`，停滞超时设为一小时，经 HTTP/1.1、h2c 或 h3 的客户端从 `max_conns: 1` 的
  h2c 上游下载 200 MB 时停止读取：另外十个请求都在一秒内得到服务，fit 的堆增长不到 32 MB，卡住的客户端恢复读取后拿到完整
  响应体。在 R03 的代码上，另外的请求会在 10 s 后超时。`TestStuckClientOnSharedConnection` 现在期望其他请求立即完成，并从
  客户端卡住时起等过停滞超时，而不是在其他请求之后再等。R01 的其他停滞测试、256 MB 流式测试、trailer 测试和 SSE 测试
  不改动即通过。`BenchmarkProxyDownload` 经代理流式传输 64 MB：开发用 Mac 上四次取中位数，HTTP/1.1 1,915 → 2,260 MB/s，
  h2c 1,275 → 1,290 MB/s。`go vet ./...` 和 `go test -race ./...` 通过。

与计划的不同：

- R01 的测试在停滞超时设为一小时时并不都能通过，也不可能都通过：上游是 HTTP/1.1 时，卡住的下载会占住它读取的那条上游
  连接，nginx 也是如此，所以在 `max_conns` 下（`TestStuckClientsPastMaxConns`）是停滞超时为其他请求腾出连接。上游是 h2c
  时，一个流可以单独暂停，不依赖停滞超时，新测试证明了这一点。
- 没有在压测主机上跑：基准框架的代理场景返回整体到达的小响应体，用一次 `WriteResponse` 写出，R04 不改动这条路径。

### R06 通过 fib 实现连接和队列上限 — 已完成

- `limits.max_conns`（默认 100,000）就是 listener 引擎的 fib `Config.MaxConns`：超过它的连接在被接受时立即关闭，计入
  `Stats().ConnsRefused`。与 `max_pending_bytes` 一样，它属于引擎，在启动时设置，重载不会改变它。
- 上游的 `max_pending` 就是 fib 的 `ClientConfig.MaxPendingPerHost`，`upstream.ErrBusy` 就是 fib 的 `ErrTooManyPending`，
  代理以 503 应答，且不计为后端的失败。R02 对进行中请求的计数，以及每个 HTTP/2 连接 100 个流的估计，都去掉了：
  `Pool.Pick` 总是会选出后端。
- 基准配置把 `max_conns` 设为 1,000,000，用于 10 万空闲连接的测试；`examples/proxy.json` 写出默认值。
- 测试：`listener` 的 `TestConnLimit`（两个 listener 共 `max_conns` 3：第四个连接不被应答即关闭并计数，另一个关闭后新开
  的连接能得到服务）；`upstream` 的 `TestDefaultMaxConns` 和 `handler/proxy` 的 `TestMaxPending` 触达 fib 的上限。
  `go vet ./...` 和 `go test -race ./...` 通过。

与计划的不同：

- `max_pending` 按地址计算，而不是按上游：fib 的上限是按主机的。它的文档已说明。
- fib v1.4.0 的上限也计入正在为其拨号的、等待连接的请求，所以冷启动时超过 `max_pending` 的突发请求，在 `max_conns`
  允许的连接还在建立时就被拒绝：`TestDefaultMaxConns` 的突发中 256 个只建立了 179 个。已作为
  [A15](fib-api-requests.zh-CN.md#a15maxpendingperhost-不计入正在为其拨号的请求) 向 fib 提出，在 v1.5.1 中完成；测试恢复为
  同时发出 256 个请求。
- 没有跑基准：基准框架的场景都不会触达这两个上限，而 `Pick` 少了一次原子加法和一次比较。

### R05 静态文件不阻塞 — 已完成

- `handler/static`：fib worker 上不再有任何文件操作。缓存在 `check_interval`（新参数，默认 1s）内保存一个请求路径解析出的
  结果：文件及其索引文件和压缩副本、要列出或重定向的目录、或者没有可服务的内容；和以前一样也保存小文件的内容。能用这些
  应答的请求在 worker 上应答，不做系统调用；条件请求和范围请求对内存中的字节运行 `ServeContent`，收集后整体写出。其他
  请求（间隔内没见过的路径、要读取、打开或列出的文件）交给包内自己的文件池：`max(16, 4 × GOMAXPROCS)` 个 goroutine 和
  4,096 的队列，所有静态 handler 共用。队列放不下的请求立即得到 503；池在 10 s 内没有应答的请求（还在排队，或磁盘挂住）
  届时得到 503，后来的应答被丢弃（`queued.claim`）。路径查找结果最多保存 16,384 个，请求不存在路径的客户端无法让它超过这个数。
- 大到不能缓存的文件经 fib 的 `SendFile` 发送：`ServeContent` 对一个只记录要复制的范围、而不实际复制的 writer 运行
  （`rangeWriter`、`offsetFile`），`transfer` 把范围一段一段交给 `SendFile`。fib 逐段复制的情况下每段 64 KB，这样 fib 在一段
  之内不会等待；普通 HTTP/1 上每段 256 KB，因为 sendfile 只是把它排队。段与段之间在写截止时间为停滞超时的情况下等待
  `OnWritable`，与代理的下载相同。对这种文件请求多个范围时返回整个文件，因为 `ServeContent` 会经管道复制 multipart 响应体。
  `stall.go` 及其等待式写入已去掉。
- `internal/fibtest.ServeRequest` 改为像 fib 的服务器那样经 `fibhttp.Serve` 运行 handler，并最多等待 `fibtest.Timeout`，
  让之后才应答的 handler 也能测试，fib v1.5.1 支持这样做（A14）。
- 测试：`TestNoDiskOnHit`（`check_interval` 内已知的路径不经文件池即应答，四次请求只有一个池任务，间隔过后再查看磁盘；
  不能缓存的大文件每次都有一个任务）；`TestSlowDisk`（池中唯一的 worker 卡住：需要磁盘的路径在池超时后得到 503，缓存中的
  路径立即应答，队列满后立即 503）；`TestStuckClient` 在 h2c 和 h3 上改经 `SendFile`；`TestSlowReader`（以约 1.3 MB/s
  经 HTTP/1.1 和 h2c 读取 3 MB，超过 1s 停滞超时，完整到达）；`TestConditional` 中从磁盘请求多个范围得到整个文件；
  `TestChangedFile` 把间隔设为 0。`go vet ./...` 和 `go test -race ./...` 通过。
- 压测主机上只跑 fit，R06 的构建与本构建各跑两轮，每秒千次请求：HTTP/1.1 静态文件 587 → 772（+31%），h2c 1,327 → 2,457
  （+85%），常驻内存 31 MB 降到 26 MB。

与计划的不同：

- 没有用 fib 的 `FileCache`：它在调用者的 goroutine（也就是 fib worker）上读取未命中的文件，在没有 inotify 的地方每个请求都
  stat，并通过 `ServeContent` 的等待式写入发送大文件；也没有 fit 的索引文件、try_files、目录列表、zstd 副本和 ETag。缓存
  是 fit 自己的，每个文件操作都经过文件池。
- 磁盘上的变化在 `check_interval` 内体现，而不是立即体现，与 nginx 的 `open_file_cache_valid` 相同。
- 普通 HTTP/1 上经 sendfile 发送的文件既不受节奏控制，也不会被停滞超时切断：fib v1.5.1 不把排队的文件计入连接未发送的输出，
  所以 `OnWritable` 总是报告有空间（R01 已记录过）。已作为
  [A16](fib-api-requests.zh-CN.md#a16sendfile-排队的文件计入连接未发送的输出) 向 fib 提出；`TestStuckClient/http/1.1` 在
  R05b 升级到包含它的 fib 版本之前跳过。

### R05b sendfile 发送的文件受节奏控制和上限约束 — 已完成

- fib v1.5.2 把 `SendFile` 排队的文件计入连接的积压（A16，`6f66bcc`）。fit 升级到该版本，其他不变：静态 handler 的传输
  在段与段之间、在停滞超时的写截止时间下等待 `OnWritable`，现在在普通 HTTP/1 上也会等待。
- `TestStuckClient/http/1.1` 恢复运行：停止读取经 sendfile 发送的 64 MB 文件的客户端，在 1 s 停滞超时后被切断，与 h2c 和
  h3 相同；`TestSlowReader` 在 HTTP/1.1 和 h2c 上仍能完整拿到 3 MB。`go vet ./...` 和 `go test -race ./...` 通过。

与计划的不同：fib 没有在 `SendFile` 内部增加停滞上限，所以范围仍然分段发送，普通 HTTP/1 上每段 256 KB，其他情况 64 KB。
没有跑基准：基准框架的静态场景服务的是缓存中的文件，不走这条路径。

### R07c 依赖 fib 的热路径开销 — 已完成

- 访问日志的条目放在 fib Context 的值槽中（A7）：`observe.Track(c)` 和 `EntryOf(c)` 接受 Context，不再为它复制请求。
- 代理经 `Client.Start` 发送请求，用它的 `Call` 放弃请求：请求没有自己的 context（没有 `WithCancelCause`、没有
  `WithContext` 复制，也没有让 fib 用 `context.AfterFunc` 监视的东西）。放弃的原因（客户端离开、空闲超时或排空）记录在
  exchange 中（`cancel`、`causeErr`），一把小锁保证在取消先到达时也能正确保存 `Call`。排空时在每个代理的 32 个加锁集合中
  找到进行中的 exchange，而不是取消一个父 context。
- `response_header_timeout` 交给每个池客户端的 fib `ResponseHeaderTimeout`，与请求共用一个定时器，代理自己的定时器去掉了；
  fib 的 `ErrResponseHeaderTimeout` 对应 504。
- 整体到达的响应直接以 fib 客户端持有的字节（`BodyBytes`，A11）写给客户端，不再复制。A13 不需要 fit 改动。
- `fibtest.Recorder.Wait` 对已经写出的响应不再创建定时器和 channel，`BenchmarkDispatch` 之前把它们也算进去了。
- 测试：`TestStatusOf` 覆盖 `ErrResponseHeaderTimeout`、`ErrBusy` 和空闲超时的原因；`TestTrack` 覆盖值槽；排空、超时、
  流式和访问日志测试不改动即通过。`go vet ./...` 和 `go test -race ./...` 通过。
- 开发用 Mac 上的分配：`BenchmarkProxy` 每请求 77 → 65 次、11.1 → 10.0 KB（包括 net/http 后端）。使用相同 fibtest 的
  `BenchmarkDispatch` 从 9 次变为 10 次，单线程 636 → 558 ns，8 核 406 → 396 ns：基准中每个请求的 Context 都是新的，fib 要在
  上面创建值槽所在的钩子；服务器的 Context 会被回收复用并保留它们。
- 压测主机上，R05b 的构建与本构建各跑两轮，每秒千次请求（HTTP/1.1 和 h2c）：不开访问日志时，代理 218.9 和 236.0 → 228.4
  和 237.3（+4.3% 和 +0.5%），HTTP/1.1 p99 3.71 → 3.21 ms，固定响应 778 和 3,774 → 781 和 3,774；开访问日志时，代理 214.7
  和 235.1 → 226.2 和 236.9（+5.4% 和 +0.8%），固定响应 787 和 2,683 → 788 和 2,691。

与计划的不同：

- `response_header_timeout` 现在从请求发完开始计时，与它的文档和 fib 客户端的计时一致；代理原来的定时器从请求开始计时，
  包括建立连接和上传。
- I23 的问题（代理在 HTTP/1.1 上只有 nginx 的 83%）：在这台主机上，fit 的代理经过 R07a–R07c 从 212k（I23 的构建）提高到
  每秒 228k 请求，约为 I23 时 nginx 258k 的 88%。[M2 报告](https://github.com/lesismal/fit/blob/3658ae9/bench/results/m2-report-2026-10-20.md) 在同一轮里重新对比：226k 对 nginx 的 264k，即 86%，每个请求的 CPU 是 nginx 的 1.5 倍。
- 打开访问日志时，h2c 固定响应仍比不开时少约 30%（269 万对 377 万），A13 已经去掉了响应头复制：剩下的是在 worker 上格式化
  日志行和钩子本身的开销。这不是回归，也不属于 M1。

### I28 证书管理与存储；ACME 选型 — 已完成

- `tlsmgr.Manager` 是握手取得证书的接口：`GetCertificate(*tls.ClientHelloInfo)`。`tlsmgr.Static` 是来自文件的证书的
  Manager（`LoadStatic`，错误仍指向出错证书的路径）：从每个证书所覆盖的名字（小写的 DNS 名、IP 地址），以及每个
  `*.` 名字的父域名，索引到对应的证书，保持配置中的顺序。握手得到该名字下客户端支持的第一个证书，否则是该名字下的
  第一个，名字没有对应证书或客户端未给出名字时是所有证书中的第一个，与 fib 从 `Config.Certificates` 中选择的结果一致；
  只有一个证书的名字省去检查。在 100 个证书中查找耗时 26ns，不分配内存。
- `tlsmgr.ServerConfig(protocols, get)` 构造 listener 的 `tls.Config`，只设 `GetCertificate`，不设 `Certificates`。
  `runtime.Listener` 用 `Certificates tlsmgr.Manager` 取代原来的 `tls.Config` 和指纹；`listener` 为每个 binding 只构造一次
  配置（HTTP/1、HTTP/2、HTTP/3 都是），其 `GetCertificate` 从原子指针读取 binding 的 Manager，`Reload` 在新图生效后把它
  换成新图的。只改证书的重载保留 binding 及其连接，新的握手得到新证书。
- `tlsmgr.Storage` 用来保存 ACME 的数据（I29）：`Load`、`Store`（要么全部写入，要么不写）、`Delete`、`List`，以及跨进程
  的 `Lock`/`Unlock`；这些方法可能阻塞，带 context，只在管理器自己的 goroutine 上调用。`FileStorage` 把每个 key 存成根目录
  下的一个文件（先写临时文件、sync、再改名；文件 0600、目录 0700；值最大 4 MiB；拒绝含空段、`.`、`..`、以斜杠开头或含
  反斜杠的 key），每个锁是一个用 `O_EXCL` 创建的文件，持有者每 5s touch 一次，等待者在它 30s 未更新后删除它。
- 决定说明 [007](decisions/007-acme-client.zh-CN.md)：证书由 fit 自己管理，协议用 `golang.org/x/crypto/acme`，ARI 由
  fit 自己实现；不用 CertMagic、lego、`autocert`，暂不用 acmez。
- 测试：`tlsmgr` 覆盖按精确名字、大小写与末尾点号、通配符（只匹配一级）、IP 地址、顺序和密钥类型（不支持 ECDSA 的客户端
  得到 RSA 证书）选择，无名字或未知名字时得到第一个；各协议组合的配置；带路径的加载错误。`FileStorage`：存、替换、读、
  按前缀列出（锁不在其中）、删除、非法 key、值的上限、已取消的 context；两个 storage 共用一个目录，八个持有者轮流取锁，
  从未同时持有；保持新鲜的锁在超过过期时间后仍需等待，释放后被等待者取得；过期的锁被接管。`listener`：证书被改写后
  重载，listener 和跨越重载的连接都保留，HTTP/1.1、h2、h3 的新连接得到新证书而不是旧证书。`go vet ./...`、
  `GOOS=windows go vet ./...` 和 `go test -race ./...` 通过。
- 基准：`listener.BenchmarkTLSHandshake`，新连接上不复用会话的完整 TLS 1.3 握手，改动前（`Config.Certificates`）和改动后
  （`GetCertificate`）在 M3 Max 上交替跑五轮：平均每次握手 68.8µs 对 68.8µs，各轮分别在 70.2–67.4µs 和 70.8–67.6µs 之间。
  fib 对带 `GetCertificate` 的握手仍用状态机，只有回调交给它的握手池。压测工具的场景不做握手，因此没有运行。

与计划的差异：

- 静态证书也走 `GetCertificate`，而没有像 I09 的说明建议的那样留在 `Config.Certificates`：基准显示没有代价，换来的是
  证书变化时不必重新绑定 listener。
- 没有监视证书文件；重载（SIGHUP、对配置文件的 `-watch`、Admin API）会重新读取它们，并且不再重新绑定 listener。监视
  证书文件，或由续期循环来发现它们的变化，留给 I29–I30。
- 存储位置还没有配置项：它随 `Storage` 的第一个使用者 I29 一起加入。

### R07d 访问日志的开销 — 已完成

R07c 之后，开着访问日志时 h2c 固定响应比不开慢约 30%。在压测主机上用 `perf` 分析（fit 用 7 个核，h2load 64 个连接、
32 个流，日志写到 `/dev/null`）：fit 约 23% 的 CPU 在日志下面，每个请求 0.6 µs，分散在多处：逐字节转义 JSON 字符串，
每行都让 `Header.Get` 对 "User-Agent" 和 "Referer" 做规范化，按浮点数格式化时长，每行完整格式化一遍日期时间，分片的
锁，两次读时钟，以及 fib 里的 `SetValue` 和 `OnFinish`。

- `appendString` 一次扫描八个字节（`plainLen`、`plainWord`），每段不需转义的字节一次追加。
- User-Agent 和 Referer 用规范的键直接从请求头 map 读取（`headerValue`），不再重复规范化。
- 时长由整数微秒格式化（`appendSeconds`），结果与 `AppendFloat` 保留六位小数相同，不再格式化浮点数。
- 精确到秒的日期时间每秒格式化一次并共享（`AccessLog.appendTime`，一个指向上一秒文本的原子指针），每行只追加毫秒。
- 分片数从每个 GOMAXPROCS 一个改为四个，最多 64 个：请求的分片由客户端决定而不是 worker，分片数和 worker 一样多时，
  两个 worker 常常选到同一个分片。
- 测试：`TestPlainLen` 对照查表检查每个位置上的每个字节；`TestAppendStringLong` 在长字符串的每个偏移放一个需转义的字符；
  `TestAppendSeconds` 对照 `strconv`；`TestLogTime` 跨秒、跨时区对照 `appendTime`；`TestLogLine` 检查 `Log` 写出的就是
  `appendLine` 格式化的内容。`go vet ./...` 和 `go test -race ./...` 通过。
- 开发用的 Mac 上：`BenchmarkLog` 单线程 230 → 181 ns，前后都没有分配；`BenchmarkDispatch` 单线程 580 → 550 ns，8 线程
  390 → 350 ns，前后都是 10 次分配。
- 压测主机上，`ea59148` 和它加上本改动的干净构建，各跑两轮、每轮 20 s，开着访问日志的 h2c 固定响应：每秒 2.69M 和
  2.69M → 2.86M 和 2.87M 请求（+6.6%），不开日志是 3.80M；日志的开销从 29% 降到 24.5%。基准框架交替跑两轮，HTTP/1.1
  固定响应和代理没有变化（788k、226k 和 236k），限制它们的不是日志。

与计划的差异：

- 剩下的约每请求 0.45 µs，是在 worker 上格式化日志行本身，以及 fib 的部分：整体写出的响应注册 `OnFinish` 时要从 fib 的
  全局池取一个 `responseWriter`，`SetValue` 要拿 Context 的锁，合计约 85 ns。这部分已作为
  [A17](fib-api-requests.zh-CN.md#a17降低每个请求上-onfinish-和-setvalue-的开销) 向 fib 提出。
- 把日志行的格式化移出 worker 只会转移开销，不会消除：在这个基准里服务端的核已经全部占满。

### I34 Prometheus 指标 — 已完成

- Admin API 在 `GET /metrics` 以 Prometheus 文本格式（0.0.4）提供指标，由 fit 自己写出（`observe.TextWriter`），不用
  客户端库。只有配置中有 `metrics`（`"metrics": {}`，需要同时有 `admin`，可在重载时开关）时才统计请求：配置中有 `admin`
  时 `fit run` 创建 `observe.Metrics`，配置有 `metrics` 的图从中取得计数器。`/metrics` 的其余部分始终提供。
- 按 listener 和路由统计请求：`fit_http_requests_total{listener,route,code}`，按状态码类别（`2xx` 等）计数；
  `fit_http_request_duration_seconds{listener,route}`，从调用 handler 到响应交给连接的耗时直方图，桶从 5ms 到 10s。
  没有名字的路由记为 `#` 加它在配置中的位置（`#1`），没有路由接受的请求记在 `route=""` 下。编译图时每个 listener 的
  路由取得各自的计数器（`runtime.Listener.Requests`，按 `Route.Index` 索引），请求按下标找到它们，在 dispatcher 的
  `OnFinish` 中计数——开启访问日志时本来就会注册这个回调。计数器在图之间共享，只要有图在用就保留：重载保留的路由继续
  累计，没有图再用的路由不再输出。
- 计数只用原子加法，分散在 8 个 128 或 192 字节的分片上。分片按请求 Context 的地址选择，fib 把 Context 放在偏向归还它
  的那个核的池中：若随机选择，每个核都会碰到每个分片，4 核时每个请求的开销是 37ns 而不是 6ns。14 个核同时计同一路由时
  为 15ns。
- 上游：`fit_upstream_requests_in_flight{upstream}`；`fit_upstream_response_duration_seconds{upstream}`，由代理记录的
  得到响应头（或失败）所用时间的直方图；每个后端的 `fit_upstream_backend_requests_total{upstream,backend,result}`
  （success 或 failure，以 `Pool.Done` 得到的结果为准）、`fit_upstream_backend_requests_in_flight` 和
  `fit_upstream_backend_up`。重载替换掉的连接池从零开始计数。
- listener 的 engine：`fit_connections_open`、`fit_connections_refused_total`、`fit_pending_bytes`、
  `fit_reads_paused_total{reason}`，来自 `listener.Server.Stats`。配置：`fit_config_reloads_total{result}`，即首个配置之后
  应用的配置数及其中失败的数量（`Configs.Reloads`），`fit_config_version`，`fit_config_last_applied_timestamp_seconds`。
  证书：`fit_tls_certificate_not_after_seconds{listener,name,serial}`，来自每个 listener 的 `tlsmgr.Static` 中的证书。
  还有 `fit_access_log_dropped_total`，以及来自 `runtime/metrics` 的 `go_goroutines` 和两项 Go 运行时内存数据。
- 测试：`observe` 检查直方图的边界（包含上界）、总和与计数、状态码类别、8 个 goroutine 同时计数、注册表的生命周期、
  带转义的文本格式，以及分片的大小。`runtime`：两个图共享一个 listener 的计数器；某个 listener 取得计数器之后编译失败
  的图会把它们还回去；所有图释放后不再剩下计数器。`admin`：经过两个 listener（一个带 TLS）和一个上游（一个后端正常
  应答、一个拒绝连接）发出请求，做一次成功重载和一次失败重载后抓取：各项计数、耗时直方图、后端结果、证书到期时间、
  重载次数和版本都符合预期，并逐行检查输出（每个指标族的 HELP 和 TYPE 只出现一次且在样本之前；没有重复样本）；
  被重载改名的路由不再输出；没有 Metrics 时不输出请求指标。`test/e2e`：带 Admin API 的 `fit run` 在重载前后统计请求，
  `/metrics` 中能看到。`go vet ./...`、`GOOS=windows go vet ./...` 和 `go test -race ./...` 通过。
- 每个请求的开销：`listener.BenchmarkDispatch`，14 核、不经网络：既无访问日志也无指标时每请求 333ns，只开日志 350ns，
  只开指标 438ns，两者都开 360ns。只开指标时要为第一个 `OnFinish` 回调付费，它要从 fib 的池中取一个 writer 等（测试
  工具为每个请求新建 Context）；开着日志时，指标只多约 10ns，不多分配内存。[fib-api-requests](fib-api-requests.zh-CN.md)
  中的 A17 请 fib 降低 `OnFinish` 的开销。
- 压测主机上的基准：fitbench，只测 fit，改动前（R07d）和改动后交替各跑两轮，都开启 Admin API。不开 `metrics` 时所有场景
  相差都在 1.5% 以内：固定响应 HTTP/1.1 每秒 777k → 779k、h2c 3.79M → 3.84M，静态文件 772k → 775k 和 2.46M → 2.45M，
  代理 229k → 227k 和 237k → 238k。开启 `metrics` 时 h2c 固定响应 3.29M（−13%），h2c 静态文件 2.25M（−9%），HTTP/1.1
  代理 −1.9%，其余不变；HTTP/1.1 受压测工具限制在约 775k，看不出开销。h2c 固定响应上的 `perf`（不开 3.86M，开启 3.28M）：
  每个请求多出的 0.32µs 中，约 110ns 是 fib 的 `OnFinish` 路径，60ns 是两次时钟读取，30–45ns 是闭包的分配与回收，15ns
  是 fit 的计数。[A18](fib-api-requests.zh-CN.md#a18不经回调取得响应的状态和请求到达的时间) 请 fib 提供响应的状态和请求
  到达的时间，使在 handler 返回前就已应答的请求只需读一次时钟、不需回调即可计数。

与计划的差异：

- 没有按 service 的指标：fit 还没有 service 这一层（流量拆分在 M3）；路由和上游有各自的指标。
- 状态码按类别计数，这样请求能按下标找到计数器；精确状态码需要一个 map，或者每个路由为 500 个状态码各准备一个计数器。
- 最初的版本只要开启 Admin API 就统计请求；基准显示了它的开销，于是统计请求改为单独的设置项，默认关闭：在压测主机上它让最快的路径慢 8% 到 13%（h2c 固定响应 −13%，h2c 静态文件 −9%），A18 请 fib 提供能降低这一开销的
  功能。

### I29 ACME HTTP-01 与续期 — 已完成

- 配置：listener 的 `tls` 新增 `acme`，即要获取证书的主机名，可与 `certificates` 并存或取代它（两者至少要有一个）；名字不能
  是通配符、IP 地址，也不能重复。顶层 `acme` 说明从哪里获取：`directory`（默认 Let's Encrypt）、`email`、自建 CA 用的
  `ca_file`、`storage`（`$XDG_DATA_HOME/fit` 或 `~/.local/share/fit`）、`max_concurrent`（2）、获取一张证书的 `timeout`
  （10m）以及每个发往 CA 的请求的 `request_timeout`（30s）。与 `access_log`、`admin` 一样，`acme` 的设置在启动时读取，重载
  改动它们会得到警告；名字则跟随每次重载。
- `tlsmgr.ACME`，控制面的证书管理器，基于 `golang.org/x/crypto/acme`（决策 [007](decisions/007-acme-client.zh-CN.md)）：
  有自己的调度 goroutine 和 `max_concurrent` 个 worker，可以阻塞在 CA 和存储上，每次获取都受 `timeout` 限制。
  `Manage(names)` 设置名字；`Certificate(name)` 和 `HTTPChallenge(token)` 只读内存（一个原子 map 和一个 `sync.Map`），供握手
  和请求使用。对每个名字，worker 先取存储中尚未到续期时间的证书；否则拿到存储中该名字的锁，再看一次，然后申请：账户（首次
  使用时生成并保存密钥，有自己的锁）、订单、HTTP-01 应答、用新的 P-256 密钥 finalize，证书链和密钥作为一个 PEM 值保存，
  保证两者一起变化。
- 剩下三分之一有效期时续期，再按序列号提前最多五十分之一有效期，让一起获取的证书错开，共享存储的实例对续期时间看法一致。
  失败后一分钟重试，每次翻倍，最多一小时，再随机减去最多十分之一；失败及等待时间写到 stderr。
- `tlsmgr.Certs`，列了名字的 listener 的 Manager：列出的名字给获取到的证书，其他名字以及还没获取到的列出的名字给文件中的
  证书；没有文件时，不带名字的客户端得到第一个名字的证书，无证书可给的握手得到错误。
- `runtime`：`Env.ACME` 是 `fit run` 启动时创建的管理器，不论配置是否列了名字都创建，以便重载时加入名字；图携带它和所有
  listener 的名字，`listener` 在图成为当前图后（启动时和每次重载）把名字告诉它。dispatcher 在明文请求上、对应 token 的
  challenge 进行中时，从内存应答 `/.well-known/acme-challenge/<token>`；其他请求交给路由。没有管理器时编译（`fit validate`
  就是这样），listener 列出的名字不去申请，只提供它文件中的证书（合并后修复：此前 `fit validate` 会拒绝所有列了名字的配置，
  现在 `cmd/fit` 的 validate 测试覆盖了这一点）。
- `internal/pebbletest` 从 `$FIT_PEBBLE_BIN` 或 `PATH` 启动 Pebble v2.10.1 及其 DNS 测试服务器（对所有名字都答
  127.0.0.1）；找不到时测试跳过。CI 会安装它们。
- 测试：`TestPebble` 从 Pebble 获取证书（用 `HTTPChallenge` 应答 challenge），检查它链到 Pebble 的根、账户密钥已保存，
  续期（续期时间设为签发后 3s）后序列号变化，并让同一存储上的第二个管理器直接使用续期后的证书而不再申请。
  `TestACMEFromPebble`（e2e）运行 fit 二进制，明文 listener 在 Pebble 的 HTTP 端口上，TLS listener 只有 `acme`：客户端拿到
  页面，证书能对 Pebble 的根验证通过；没有进行中 challenge 的 token 交给路由。不需要 Pebble 的：`TestSlowCA`，CA 永不应答、
  五个名字、`max_concurrent` 为 2：每个名字超时后放弃并重试，CA 同时最多看到两个请求，期间握手和 challenge 照常应答，
  `Close` 立即返回；`TestCutCA`，CA 关闭每个连接，重试间隔越来越长；`TestStoredCertificate`，存储中的证书无需 CA 即可使用；
  `TestManage`，去掉的名字不再提供证书也不再重试；`TestCerts`、`TestRenewAt`、`TestBackoff`、`TestCAKey`；`TestValidate`
  中的配置检查。`go vet ./...`、`GOOS=windows go vet ./...` 和 `go test -race ./...` 在有无 Pebble 时都通过；ACME 测试
  连续五次通过。

与计划的差异：

- 决策 007 说由 fit 自己实现的 ARI（CA 建议的续期窗口）这次没有做：续期按有效期计算。放到 I30。
- 没有跑基准：数据面在明文请求上多了一次指针判断和一次路径前缀比较，列出名字的握手多了一次经原子指针的 map 查找。
- 计划只涉及 `tlsmgr` 和 `listener`；为了配置、接线和测试，`config`、`schema`、`runtime`、`admin`、`cmd/fit` 和 CI 也有改动。

### I30 TLS-ALPN-01、OCSP stapling — 已完成

- TLS-ALPN-01（RFC 8737）：每个 TLS listener 在 ALPN 列表最后提供 `acme-tls/1`，所以只有单独提供它的客户端（检查验证的
  CA 正是这样）才会协商到它；`tlsmgr.Certs` 在该 listener 列出的名字有验证进行中时，给这样的 hello 验证用的证书（由
  `TLSALPN01ChallengeCert` 生成），否则什么都不给。`ACME.ALPNChallenge` 读一个 `sync.Map`。fib 的 TLS 不需要新功能：它的
  服务端按服务端顺序选择 ALPN，把客户端的协议列表交给 `GetCertificate`，既不是 h2 也不是 http/1.1 的连接按 HTTP/1 读取，
  CA 在握手后就关闭它。
- `acme.challenges`（不写时为 `["http-01", "tls-alpn-01"]`）指定应答哪些验证及顺序；连续失败 n 次后的尝试从第 n 个开始，
  这样 CA 访问不到的验证方式不会让每次尝试都失败。
- OCSP stapling：`tlsmgr.OCSP` 由 `fit run` 创建，在图之后关闭，为每个指明了响应者、且证书链中有签发者的证书保存响应，
  包括文件中的证书（编译时 `Static.Staple`，图清理时 `Release`）和申请到的证书（`ACME.UseOCSP`）。它在自己的 goroutine 上
  获取响应，同时最多两个，每次限 10s 和 64 KiB，用 `golang.org/x/crypto/ocsp` 对照签发者检查响应，把有效的响应 staple 到
  证书的副本上，握手通过原子指针读取。响应有效期过半时重新获取（没有下次更新时间时每小时一次），失败后一分钟再试，
  加倍直到一小时，并且绝不在响应的下次更新时间之后继续使用它。证书按证书链保存并计数使用者，所以重载生成的图共享同一
  响应。被吊销的证书不 staple 并报告；经 ACME 申请的证书立即续期，且不再从存储中取回这一张。
- `internal/certtest` 增加一个 CA，签发指明 OCSP 响应者的证书；`internal/pebbletest` 可以设置 TLS-ALPN-01 的端口。
- 测试：`TestPebbleTLSALPN` 只通过 TLS-ALPN-01 从 Pebble 取得证书，由使用 listener 配置的 TLS 服务端应答，之后验证用的
  证书被删除；`TestChallengeFallback` 把没人应答的 `tls-alpn-01` 排在第一：第一次尝试失败，下一次经 HTTP-01 应答成功；
  `TestCertsALPN`：只对列出的名字、且只单独提供 `acme-tls/1` 的 hello 给验证证书。`test/e2e` 的
  `TestACMETLSALPNFromPebble` 让 fit 程序只用一个 TLS listener 监听 Pebble 的 TLS-ALPN-01 端口：fib 的 TLS 应答验证，
  客户端拿到页面。OCSP 针对测试控制的响应者：响应 staple 在副本上；有效期两秒时在过半时刷新；响应者宕机时保留仍有效的
  响应，过期后丢弃，恢复后再次 staple；被吊销的证书不 staple 并报告；同一证书链加入两次共享一个 staple、只获取一次，最后
  一次释放后删除；响应者扣住所有请求时同时最多两个请求，`Close` 立即返回；`Static` 提供 staple 后的副本并能释放。
  `listener` 的 `TestOCSPStapled`：TLS 1.3 和 1.2 的客户端都拿到 fib 的 TLS staple 的响应。`go vet ./...`、
  `GOOS=windows go vet ./...` 和 `go test -race ./...` 通过（含 Pebble）。
- 基准：`listener.BenchmarkTLSHandshake`，改动前后交替四轮：中位数每次握手 70.9µs 和 70.2µs，在笔记本的噪声范围内。
  请求路径没有变化。

与计划的差异：

- 拆分：默认 HTTPS 移到 I30b，I29 留给 I30 的 ARI 移到 I30c，使这次迭代接近计划的目标规模。
- Let's Encrypt 已停止 OCSP，它的证书不会有 staple；stapling 用于其他 CA 和文件中的证书。
- 没有关闭 stapling 的设置：不指明响应者的证书没有任何开销。

### I31 按需 TLS — 已完成

- 配置：listener 的 `tls` 可以设置 `on_demand: true`，可以单独使用，也可以与 `certificates`、`acme` 同时使用，并需要顶层
  的 `acme.on_demand`：`ask` 是询问某个名字能否获得证书的 URL，带 `domain=<name>` 参数（2xx 以外的回答都表示拒绝）；
  `rate` 和 `interval`（每分钟 10 个）；`max_names`（1000）；`timeout` 是握手最多等待多久（10s，即 fib 的握手超时）。
  和 `acme` 的其余部分一样在启动时读取；修改它的重载会得到警告。
- `tlsmgr.Certs` 对列出的名字照旧处理，文件中有证书的名字（`Static.Has`，精确或通配符匹配）用文件中的证书；客户端请求的
  其他名字交给 `ACME.OnDemand`，由 `GetCertificate` 在 fib 的握手池上调用，绝不在 worker 上。它不经询问就拒绝不是主机名
  的名字（IP 地址、只有一段、通配符、不合法的段）；内存中已有证书就直接返回；否则先检查速率和 `max_names`，再询问 ask URL，
  把名字计入两者，交给 ACME 的 worker 作为受管名字处理：像 I29 那样从存储加载或申请，之后续期。同一名字的多个握手等待同一张
  证书；每个最多等待 `timeout`，超时后证书仍继续申请，留给下一次握手。拒绝会被记住一分钟（最多 10,000 个名字）；ask URL
  不应答时拒绝但不记住。第一张证书申请失败的名字会被丢弃，不再计数；重载保留按需加入的名字。
- 测试：对一个允许的名字同时发起四个握手，从 Pebble 得到同一张证书，只询问一次、只下一个订单，之后从内存提供，在不列名字
  的重载后也是如此；被拒绝的名字每分钟只询问一次；不应答的 ask URL 每次都拒绝；速率为每小时一个时，第二个名字不经询问就被
  拒绝，一小时后被允许；超过 `max_names` 的名字被拒绝；CA 切断连接时，等待中的握手得知失败，名字被丢弃且不再计数；不合法的
  名字不经询问就被拒绝；文件中有的名字用文件中的证书，没有名字的 hello 也是；没有 `acme.on_demand` 时什么都不申请。
  `test/e2e` 的 `TestOnDemandFromPebble`：fit 程序在允许的名字的握手中申请证书并提供页面，被拒绝的名字握手失败。配置检查在
  `TestValidate` 中。`go vet ./...`、`GOOS=windows go vet ./...` 和 `go test -race ./...` 通过（含 Pebble）。
- 没有基准：没有 `on_demand` 的 listener 走原来的路径，请求的路径没有变化。

与计划的差异：

- 速率限制针对加入名字，从存储加载证书也计入；存储中已有证书仍需 ask URL 同意，因为名字可能已不再被允许。
- 按需加入的名字在重启前一直保留并续期，不因长期不用而丢弃，期间由 `max_names` 限制总数。I35c 起闲置后丢弃。
- 证书到期指标只列出 listener 列出的名字，不含按需加入的名字。

### I30b 默认 HTTPS — 已完成

- `listeners` 可以不写。默认 HTTPS 开启时，没写 listener 的配置由一个 `https` listener（`:443`，HTTP/1.1 和 h2）提供服务，
  证书经 ACME 为其路由匹配的域名申请；另有一个 `http` listener（`:80`），应答 ACME 的 HTTP-01 验证，把其他所有请求以 308
  跳转到 `https://<host>[:port]<target>`，使客户端重复其方法和请求体。路由可以指定这两个 listener 中的任何一个；`http`
  不服务路由。写了 `tls` 但内容为空的 TLS listener，为它服务的路由的域名申请证书。写全了的 listener 按原样提供服务，所以
  现有配置不变。
- 名字：至少两段的域名；跳过通配符、IP 地址和只有私有网络才能解析的名字（`localhost`、`.localhost`、`.local`、
  `.internal`、`.home.arpa`、`.invalid`）。
- `auto_https`：`disable`（不生成 listener，空的 `tls` 被拒绝）、`disable_redirects`（没有 `http` listener：证书经
  TLS-ALPN-01 获取）、`http_port`（80）和 `https_port`（443）。逻辑在 `config.Config.Effective` 中，`Validate` 和
  `runtime.Compile` 都用它：没有 listener 又没有可申请证书的名字的配置被拒绝，指定不存在的 listener 的路由、两个端口相同
  也被拒绝。`runtime.Listener` 增加 `RedirectHTTPS`，即 dispatcher 跳转到的端口；访问日志和指标把它记为没有路由接受的请求。
- Let's Encrypt 测试环境：`test/e2e` 的 `TestLetsEncryptStaging` 用只写了域名的配置针对测试环境 CA 运行 fit，检查提供的是
  它签发的证书。需要在一台该域名从互联网解析到、并能监听 80 和 443 端口的主机上手动运行：
  `FIT_LE_STAGING_NAME=<name> go test ./test/e2e -run TestLetsEncryptStaging -v -timeout 10m`
  （`FIT_LE_STAGING_EMAIL` 设置账户邮箱）；否则跳过。
- 测试：`config` 的 `TestAutoHTTPSName` 和 `TestEffective`（按 listener 的名字、其他 listener 上的路由、配置不被修改、
  端口、不跳转）以及 `TestValidate` 中的检查；`listener` 的 `TestAutoHTTPSRedirect`：没写 listener 的配置得到两个 listener，
  明文请求以 308 跳转，保留主机、端口和目标，覆盖 GET 和 POST、IPv6 主机、没有验证进行中的验证路径；`test/e2e` 的
  `TestAutoHTTPSFromPebble`：fit 程序用只写了 `auto.fit.test`（以及 Pebble 的地址和端口）的配置，经 `http` listener 上的
  HTTP-01 取得证书，通过 HTTPS 提供页面，并跳转 HTTP；`fit validate` 接受这样的配置。`go vet ./...`、
  `GOOS=windows go vet ./...` 和 `go test -race ./...` 通过（含 Pebble）。
- 没有基准：dispatcher 只是多读一个已有 listener 的字段。

与计划的差异：

- 写全了的 listener 不加跳转：默认 HTTPS 补上配置没写的部分，不改变配置写了的内容。为写出的 listener 跳转随 I38 的跳转功能
  提供。
- `https` listener 提供 HTTP/1.1 和 h2，不含 h3，与写出的 TLS listener 的默认值相同。
- Let's Encrypt 测试环境的测试需要公网域名和 80、443 端口，压测主机都没有。2026-10-21 在 1f56c44 上手动运行：一台带
  DuckDNS 域名的公网 Debian 12 主机上，fit 用只写了域名的配置从测试环境 CA（"(STAGING) Baloney Bulgur YE2"）获取证书并
  提供，用时 10 秒。主机上的 Go 比 fit 要求的旧，所以 fit 和 `go test -c` 的二进制交叉编译后复制过去，测试的 `PATH` 上放
  一个替身 `go`，把预编译的 fit 复制到 `TestMain` 构建它的位置。

### I30c ACME 续期信息（ARI） — 已完成

- `tlsmgr.ACME` 在每次运行使某个名字有了证书之后，询问 CA 的 ARI（RFC 9773）对它的建议：`renewalInfo` URL 取自 CA 的目录，
  只读一次（x/crypto 的 `acme.Directory` 不包含它），证书的标识由其颁发者密钥标识和序列号的 DER 编码分别做 base64url 组成。
  时间窗已开始时证书立即到期；时间窗尚未开始时，在其中随机选一个时间。证书在这个时间、按有效期计算的续期时间和再次询问的
  时间三者中最早的那个到期：再次询问的时间由 CA 的 Retry-After 决定（秒数或日期），限制在一分钟到一天之间，没有给出时为
  六小时。ARI 的建议只对被询问的那张证书有效，所以期间被另一实例续期的证书会照常从存储中取用。
- 没有 ARI 或不应答的 CA，按有效期续期，六小时后再问；失败会记录日志，其他不变。
- 对 CA 的 ARI 请求在 ACME 的 worker 上执行，受其超时限制，应答最多读 64 KiB。
- `internal/pebbletest` 增加 `SetRenewalInfo`，让 Pebble 为某张证书建议时间窗。
- 测试：`TestCertID`（RFC 9773 的例子、最高位为 0 的序列号、没有密钥标识）、`TestRetryAfter`；针对测试自己的 CA：存储中离
  到期还远、但时间窗已开始的证书立即续期，续期期间仍提供原证书，并按其标识询问 CA；时间窗尚未开始时不续期，续期时间选在
  窗口内，并按 Retry-After 再次询问。针对 Pebble：使用它为所签发证书建议的时间窗（在证书有效期内），Pebble 把时间窗移到
  过去后证书被续期。`tlsmgr` 的测试在竞态检测下连续通过三次（含 Pebble）。`go vet ./...`、`GOOS=windows go vet ./...`
  和 `go test -race ./...` 通过。
- 没有基准：数据面没有变化。

与计划的差异：

- 新订单不注明它替换的证书（ARI 的 `replaces`）：x/crypto 的 `AuthorizeOrder` 无法发送它。CA 用它让续期不受速率限制，
  所以 ARI 要求的续期和其他订单一样提交。2026-10-20（M2 之后）再次检查：最新发布的 x/crypto v0.58.0 和 2026-10-16 的
  master（`0ae6809`）都不能发送它。`AuthorizeOrder` 接受 `OrderOption`，但该接口是封闭的，只提供 `notBefore`、
  `notAfter` 和 `profile`，客户端也没有导出发送自定义签名请求的方法。要发送它，就得在 fit 中自己签名 ACME 请求，或换用
  别的 ACME 库（acmez 和 lego 支持），违背决定 007；留待 x/crypto 支持。Pebble v2.10.1 接受 `replaces`，届时即可写测试。

### I35 M2 退出测试 — 已完成

- M2 的三条退出标准（见路线图）是 `test/e2e` 中运行 fit 程序的三个测试：`TestChurn`（I26：在流量下通过 Admin API 切换
  配置 2,000 次，没有请求失败）、`TestAutoHTTPSFromPebble`（I30b：只写了域名的配置通过 HTTPS 提供服务，证书来自 Pebble，
  HTTP 跳转到 HTTPS），以及新增的 `TestHealthCheckRotation`：一个上游有两个后端，带 HTTP 健康检查（间隔 200ms、超时
  100ms、失败两次、通过两次）；其中一个后端的探测路径不再应答，其他请求照常处理，所以只有健康检查能把它移出。它在时间窗内
  （失败次数 × 间隔 + 超时 + 一个间隔的余量，700ms；实测 380–540ms）离开轮转，离开期间不再收到请求，并在其时间窗内
  （通过次数 × 间隔 + 一个间隔，600ms；实测 350–380ms）回来；没有请求失败。
- `make m2-exit` 运行这三个测试；CI 增加 "M2 exit tests" 任务运行它们，并要求有 Pebble：设置 `$FIT_PEBBLE_REQUIRED` 时
  `pebbletest` 失败而不是跳过。该任务在 CI 的 `929ec73`（第 15 次运行）上通过，lint 和 fuzz 任务也通过。Let's Encrypt
  测试环境的检查是 `TestLetsEncryptStaging`（I30b），手动运行。
- CI 的完整 `go test -race ./...` 任务（Linux 和 macOS）自 M1 审查（`d4b9c5b`）以来每次推送都失败，此前没有被注意到。
  用三个 CPU（与 runner 相同）运行测试，复现了一个原因：fit 的代理读取请求声明的 trailer 名字，与 fib 的 HTTP/2 服务端在
  请求体结束时设置请求的 `Trailer` 之间的数据竞争。这是 fib 的问题：作为
  [A19](fib-api-requests.zh-CN.md#a19流式请求的-trailer-在交出请求体结尾的地方设置) 提出，已修复并发布于 fib v1.5.3，
  fit 已升级；三个 CPU 下 `TestTrailers` 连续通过五次，而 v1.5.2 五次中失败一次。这两个任务在 v1.5.3 上仍然失败。查看其日志需要登录
  GitHub，所以 CI 现在还会把每个失败包的测试输出写成注解（`internal/testsummary`），任何人都能读取。由此找到原因：
  `TestStuckClientWithoutStallTimeout` 在客户端恢复读取后只给它 5 秒读完 200MB 下载的剩余部分，而在竞态检测器下、runner
  的 CPU 上读不完（Linux 上是 h3；macOS 上是 h2c 和 h3；本地用 `-cpu 2` 可复现）。现在卡住的客户端只在连续 5 秒没有新数据
  到达时才放弃。下一次运行显示了第二个原因，是 fit 自己的 bug：Linux 上的 `TestWatch` 漏掉了第一次修改。
  `fit run` 先启动监听器再启动 watcher，而 watcher 在启动时才第一次查看文件的大小和修改时间，所以在两者之间、大小不变的
  修改从此看不出变化，永远不会被应用。现在 watcher 启动时视文件为刚刚改变，由内容决定是否应用（`TestEditBeforeWatching`）。之后在 5608205 上，`TestPassiveHealth/h2c` 从拒绝连接的地址收到两个 502，而一次失败就应把它移出轮转：代理先应答失败的请求，再通知连接池，所以在较慢的机器上，客户端接下来的请求可能在它被移出之前又被分到这个地址。现在先通知连接池。

与计划的差异：

- CI 中的证书测试针对 Pebble；Let's Encrypt 测试环境需要公网域名和 80、443 端口，按 I30 完成标准的允许，写成文档手动运行。2026-10-21 手动运行通过（见 I30b），路线图的三条退出标准都已按原文达成。
- 计划中的 I35 不含基准；R07c 的记录留到 M2 结束时做的 nginx 对比，就是 I35 之后在 59083f3 上跑的 [M2 报告](https://github.com/lesismal/fit/blob/3658ae9/bench/results/m2-report-2026-10-20.md)。压测工具只测空闲连接时，汇总表原来不列服务器名，现已修正。

### I35b 监视证书文件 — 已完成

- 运行中的配置所加载的证书或私钥文件变化时，`fit run` 重新应用该配置（`-watch-certs`，默认开启；`-watch-interval`
  现在同时设置两个监视器的轮询间隔）。`tlsmgr.FileWatcher` 在独立的 goroutine 上轮询每个文件的大小和修改时间，与
  配置文件监视器相同（决定 006）；它们保持不变 500ms 后读取文件，把 SHA-256 与运行中的配置加载时的结果比较，后者
  由 `LoadStatic` 记录（`Static.Files`，汇总在 `Graph.CertFiles`）。有差异时通过 `admin.Configs.Reapply` 重新应用
  运行中的配置，走与所有重载相同的路径，作为来源为 `certificates` 的新版本；监听保留，只有证书改变。因此加载失败的
  证书对（先写了证书、还没写私钥）像任何失败的重载一样失败，只报告一次，正在使用的证书不变；同样的文件在再次变化前
  不会重试。读不到的文件（比如正在被替换）只报告一次。
- 通过其他方式（SIGHUP、Admin API、`-watch`）应用的配置也会和它的文件比较一次，因为文件可能在它读取之后变化；两个
  监听对同一文件加载到不同内容（中途被改）时，会重新加载。
- 证书和私钥文件最多读取 1 MiB（`MaxCertFileBytes`），原来 `tls.LoadX509KeyPair` 会整个读入。
- 证书到期指标列出按需获取的证书：开启按需 TLS 的监听会为其文件未覆盖的任何域名提供 ACME 持有的证书，现在也列出它们。
- 测试：`tlsmgr.TestFileWatcher`（分两步写入的续期只应用一次，touch 或改了又改回不应用，失败的证书对只报告一次、
  变化后重试，文件消失只报告一次）、`TestFileWatcherNewConfig`、`TestLoadStatic` 中的文件摘要、
  `TestLoadStaticErrors` 中超过上限的文件、`TestCerts` 中的按需证书，以及 `e2e.TestCertificateFilesWatched`：给
  运行中的 fit 换上新证书，与私钥不匹配时报告错误并继续使用旧证书，再换上私钥后，握手得到新证书。

与计划的差异：

- 没有基准：数据面没有变化；监视器在独立的 goroutine 上每秒 stat 几个文件。
- 重新应用整个配置，而不是原地替换某个监听的证书：重载保留监听以及上游连接池和健康状态（I26），代价是一次编译，
  换来续期也有每次重载都有的检查、回滚和指标。

### R07e 借助 fib 降低钩子和文件发送的开销（A16–A18） — 已完成

M2 报告之后，维护者同意由 fit 的会话自己完成 A16–A18 在 fib 中的部分，并推送、打 tag：fib v1.5.4
（`fe60763`、`b39373d`）。

- fib 中：回收的 Context 保留清空后的 response writer，不再为每个需要它的请求（例如访问日志的 `OnFinish`）
  去 `writerPool` 取放（A17）。`SetValue` 和 `Value` 不加锁：`Context.cancel` 改为原子指针，值在请求交给其他
  goroutine 之前设置。`Context.Status()` 返回已发送或已开始的响应的状态码（A18）。`SendFileStall` 为段与段之间
  等待客户端的每一次设上限（A16）。`Context` 保持 160 字节，`closing` 和 `lingers` 合用一个字节给状态码腾出
  空间：第一版多占了 16 字节，被 fib 自己的测试发现，HTTP/2 和 HTTP/3 的 stream 超出了各自的分配尺寸档。fib
  完整的竞态测试通过；`TestClientStreamTimeoutMidBody` 在整套测试的负载下失败过一次，单独连续跑 30 次都通过，
  它所在的客户端路径不受这次修改影响。
- fit 中：`module.Inline`（`respond` 实现了它）让没有中间件的路由成为 `Route.Inline`；开启指标、关闭访问日志时，
  分发器在 handler 返回后用 `Context.Status()` 为这类请求以及自己的 404 和跳转计数，不注册 `OnFinish` 回调。
  `Status` 只在这里读取：其他 handler 的响应可能同时在别的 goroutine 上写出。静态 handler 把整段范围交给
  `SendFileStall`，原来自己分段发送的 `transfer` 已删除；`TestStuckClient` 在所有协议上通过。
- 基准机上，fit 修改前（59083f3，fib 8c637e0）与修改后（fib v1.5.4）单独运行，各两轮交替（每秒请求数，均值）：

| 场景 | 协议 | 原配置 | 访问日志 | 指标 |
| --- | --- | --- | --- | --- |
| fixed | h2c | 3,798k → 3,769k（−0.8%） | 2,863k → 2,926k（+2.2%） | 3,294k → 3,575k（+8.5%） |
| static | h2c | 2,439k → 2,435k（−0.2%） | 2,054k → 2,098k（+2.1%） | 2,257k → 2,284k（+1.2%） |
| proxy | h2c | 238k → 234k（−1.4%） | 236k → 235k（−0.6%） | 237k → 235k（−0.9%） |

  HTTP/1.1 上所有数字都在 0.6% 以内。原配置下 h2c 的 fixed 和 proxy 再跑三轮，为 −0.6% 和 −1.3%。二分：
  fit 5608205 分别配 fib 8c637e0、fe60763（A16，这些路径用不到）和 b39373d，A16 单独为 +0.7% 和 −0.5%，
  即两次构建之间的噪声；从 A16 到 A17–A18 为 −1.3% 和 −0.4%。没有钩子的 h2c fixed 响应不取 writer，只多一次
  状态码赋值。按构建间的波动范围看待，不做改动。

与计划的差异：

- 访问日志只提高了 2%，不是 A17 估计的其开销的六分之一：剩下的是格式化日志行本身。
- `Received` 没有加入 fib；见 A18。

### I35c 丢弃闲置的按需名字 — 已完成

- `acme.on_demand` 新增 `idle_timeout`（默认一周）：按需加入的名字在这段时间内没有任何握手请求时被丢弃。它不再计入
  `max_names`，证书不再提供、也不再列入证书到期指标，OCSP 响应被归还；证书留在存储中，所以下一个请求该名字的握手会从
  存储重新加入它：需要 ask URL 同意并计入速率，证书仍有效时不会重新下单。和 `acme` 的其他设置一样，只在启动时读取。
- 握手把使用记录在该名字的 `atomic.Int64` 中，无锁读取（放在一个 `sync.Map` 里，只在加入或丢弃名字时变化），每秒最多
  写一次，避免同一名字的握手都去写它。ACME 的调度器原本就为下一个到期的名字醒来，现在也为下一个将要闲置的名字醒来，并在
  ACME 的锁下丢弃它；正在处理的名字等这次运行结束。重载后列出的名字不再算按需加入，不会被丢弃。
- 测试：`TestOnDemandIdle` 使用测试自己的时钟：在 CA 不存在的情况下，从存储加入两个名字，占满 `max_names`；
  `idle_timeout` 为一小时，其中一个在第 40 分钟再次被请求；第 59 分钟两者都未丢弃，第 61 分钟另一个被丢弃，不再提供、
  列出或计数，并且有了加入新名字的余地；再次请求时从存储重新加入，ask URL 被问第二次；前一个在最后一次握手一小时后被丢弃。
  `TestOnDemandIdleTimer`：使用真实时钟、100ms，调度器自己醒来丢弃它。linux、darwin 和 windows 的 `go vet` 与
  staticcheck，以及 `go test -race ./...`（含 Pebble）通过。
- 没有基准：按需名字的握手多了一次 `sync.Map` 读取，每秒最多一次写入；请求路径没有变化。

与计划的差异：

- 无。

### R07f 剖析代理每个请求的 CPU（A20–A22） — 已完成

M2 报告显示，fit 的代理在 HTTP/1.1 上是 nginx 的 86%，每个请求的 CPU 是 nginx 的 1.5 倍。

- 在基准机上用 `perf` 剖析：fit（2b37a8e，fib v1.5.4）和 nginx 1.31.4 各用 7 个 CPU，代理到用 2 个 CPU 的 fit 后端，
  wrk（7 个线程、256 个连接）走 HTTP/1.1：fit 每秒 226.8k 个请求，每个 29.7 µs CPU；nginx 265.3k，20.7 µs。两者的
  内核时间差不多（fit 15.2 µs，nginx 17.0 µs；每个请求 5.9 和 6.3 次系统调用）；差别在用户态：14.5 µs 对 3.6 µs。fit 的
  用户态时间中，fib 的代码及其调用的部分占 10.3 µs，GC 后台标记 1.3 µs，运行时其余部分 1.2 µs，fit 自己的代码 1.8 µs。
- 属于 fib 的：客户端用 net/http 的 `Request.Write` 写每个上游请求（1.49 µs），在新建的 4 KiB `bufio.Reader` 上用
  `net/http.ReadResponse` 读每个响应头（2.23 µs），合计占 CPU 的八分之一、分配的一半以上；事件循环每次休眠都要布防和撤防
  中继 epoll 实例（每个请求 0.48 次 `epoll_ctl`，nginx 没有）；客户端每个请求都重置连接的空闲定时器（0.23 µs）。已作为
  [A20](fib-api-requests.zh-CN.md#a20不经-nethttp-写客户端请求读响应头)、
  [A21](fib-api-requests.zh-CN.md#a21事件循环每次休眠只调用一次-epoll_ctl而不是两次) 和
  [A22](fib-api-requests.zh-CN.md#a22客户端连接的空闲计时不必每个请求重置定时器) 提出。A20 已在 fib v1.5.6 中
  实现（见下文），A22 已在 fib `7f632cb` 中实现，fit 从 `a6d78cd` 起使用；A21 无法实现，已关闭，因为 `EPOLLONESHOT`
  省不掉这次调用（见 fib-api-requests 中的 A21）。
- 属于 fit 的：代理在复制请求头时就跳过逐跳字段、`Content-Length` 和 `Expect`，而不是复制后逐个删除；`removeHopByHop`
  （响应也用）对每个字段只看一次；`validToken` 查表。新增的 `BenchmarkOutgoing`（构造上游请求）：压测工具的请求
  227 → 214 ns，带九个字段的浏览器请求 684 → 541 ns，分配次数不变（3 和 6）。在基准机上单独测 fit 的 proxy，前后交替各三轮：
  HTTP/1.1 227.7k → 227.0k（−0.3%），h2c 234.8k → 234.9k，都在噪声范围内，因为这只是 29.7 µs 中的 0.05 µs。
- linux、darwin 和 windows 的 `go vet` 与 staticcheck，以及代理测试在竞态检测下通过。

- 之后 A20 由本会话经维护者同意在 fib 中实现，发布为 v1.5.6（`0a9339f`，CI 第 214 次运行通过），fit 已升级。在基准机上，
  fit 3f25679 分别用 v1.5.5 和 A20，前后交替各三轮：代理在 HTTP/1.1 上从每秒 226.6k 提高到 245.8k（+8.4%，约为 nginx 265k 的
  93%），每个请求的 CPU 从 29.7 µs 降到 26.0 µs；h2c 上从 235.5k 到 238.1k（+1.1%）。在 v1.5.6 上，fit 的竞态测试（含 Pebble）
  以及 linux、darwin 和 windows 的 `go vet` 与 staticcheck 都通过。

与计划的差异：

- fit 能去掉的很少：它的 1.8 µs 大部分用于构造 fib 的 `Client.Do` 所需的 `net/http` 头部 map 和 `Request`，A20 不改变
  这些。代理与 nginx 余下的差距中，A22 之后已在 fib 中实现，A21 无法实现，已关闭，
  因为 `EPOLLONESHOT` 省不掉这次调用。

### I36 匹配器 v2 与优先级 — 已完成

- 路由的 `match` 除 `host` 和 `path` 外新增：`method`（按原样比较）、`header`（名字到值：请求须有每个头部，且其某个字段
  的值是列出的值之一；未列出值时任意值都可以）、`query`（键到值，规则同上，解码后比较）、`remote_ip`（连接对端须落在的 IP
  地址和 CIDR 前缀；映射到 IPv6 的 IPv4 地址按 IPv4 算，不读 X-Forwarded-For）和 `path_regexp`（Go 正则表达式，不写 `^` 和
  `$` 时不锚定）。设置的每个字段都须匹配；同一字段内任一项匹配即可。`config.Validate` 逐项检查：方法和头部名须是 token，
  查询键不能为空，地址和前缀能解析，正则表达式能编译。
- `router` 包：`Matcher`（`Match(*http.Request) bool`），以及构造它的 `Methods`、`Headers`、`Query`、`RemoteIP` 和
  `PathRegexp`。`Route.Matchers` 须全部匹配。`Router.Match` 现在接收请求本身，而不是它的 host 和 path。`runtime` 在编译
  配置时构造路由的匹配器，便宜的在前（`Route.Matchers`）；`listener` 的分发器传入 `c.Request`。
- 路由的顺序（见包文档和 `priority` 字段的说明）：优先级最高的；然后 host 最具体的；然后 path 最具体的；**然后设置的匹配器
  最多的**（每个字段算一个：`method`、`header`、`query`、`remote_ip`、`path_regexp`）；最后是先写的。所以在优先级相同、
  host 和 path 相同时，带头部匹配器的 `GET /api` 胜过 `GET /api`，后者又胜过 `/api`；匹配器不匹配的路由让给下一个。
- 实现：host 和 path 仍然用 map 查找，但每个节点现在为每个 path 和前缀保存其下的所有路由，按优劣排序，并在第一个没有匹配器
  的路由之后截断（它之后的路由不可能被选中）。查找依次尝试精确 host、通配符、无 host，每个里面依次尝试精确 path 和由长到短的
  前缀；只有当一个列表的第一个路由能胜过已找到的路由时，才运行这个列表的匹配器，因此在同一 host 上已有更好的路由匹配后，
  优先级更低或更不具体的路由的匹配器不会运行。匹配不分配内存：查询串原地扫描、边解码边比较，远端地址用 `netip` 解析；
  `PathRegexp` 只在 Go 的 `regexp` 分配时分配。
- 测试：每个匹配器的表驱动测试（`TestMatchers`：方法；头部存在与带值；用 `%` 和 `+` 转义的查询以及无效转义；远端 IPv4、
  IPv6、映射和带 zone 的地址；正则表达式）、它们的错误，以及顺序（`TestMatcherOrder`：落到下一个路由、匹配器多的胜过少的、
  具体程度胜过匹配器、优先级胜过两者、精确 path 回退到前缀再回退到更不具体的 host、平局时取先写的、不应运行的匹配器）；
  `TestMatchersAllocs` 检查在 53 个路由上带方法、头部、查询和远端 IP 匹配器的 `Match` 不分配内存；`config` 的
  `TestValidate` 加入新错误及其位置；`runtime` 的 `TestCompileMatchers` 编译一个包含所有匹配器的配置并检查其路由器。
- 检查：在基准机上用 `golang:1.27-bookworm` 镜像运行 `go vet ./...` 和受影响包（router、config、runtime、listener、
  schema、admin、cmd/fit、handler/...、module、observe、tlsmgr、upstream、test/e2e）的 `go test -race`，均通过；linux、darwin 和 windows 的 `go vet` 与 staticcheck 通过。
- 在基准机上用 `bench/microbench.sh` 比较 main 和本改动，各 10 轮交替：`router` 的 `BenchmarkMatch`（101 个路由，
  无匹配器）92.4 ns → 56.5 ns（−39%：条目改为按指针传递而不是复制，并且先尝试精确 host），`listener` 的
  `BenchmarkDispatch/bare` 111.0 ns → 109.5 ns（−1.4%），分配次数不变（0 和 4）。`bench/ab.sh` 单独测 fit 的固定响应，
  10 秒一轮、交替三轮：HTTP/1.1 每秒 778k → 778k 个请求（−0.1%），h2c 3,842k → 3,852k（+0.2%），都在噪声范围内。

与计划的差异：

- 匹配器是路由 `match` 的字段，内置在路由器里，而不是 `module.Matcher` 模块；`module.Matcher` 保留给以后可能需要它的模块。
  `remote_ip` 是连接的对端；从可信代理的头部取客户端地址可以在 I39 中加入，它的限流需要这个地址。

### I37 表达式匹配器 — 已完成

- 路由的 `match` 新增 `expr`：用 `&&`、`||`、`!` 和括号组合匹配器的表达式，`&&` 比 `||` 结合得更紧，与 Go 相同：
  `Host("a.example", "*.b.example") && (PathPrefix("/api/") || Method("POST"))`。可用的匹配器有 `Host`（精确或 `*.`
  通配符，忽略大小写、端口和末尾的点）、`Path`、`PathPrefix`、`PathRegexp`、`Method`、`Header(name, value...)`、
  `Query(key, value...)` 和 `RemoteIP`，参数是按 Go 方式加引号的字符串（`"..."` 或 `` `...` ``）。它与路由的其他字段是
  “与”的关系，在路由顺序中算一个匹配器；它里面的 host 和 path 不会让路由更具体，所以只有 `expr` 的路由按没有 host 和 path
  的路由来尝试，要用 `priority` 来安排它的位置，字段说明中写明了这一点。
- `router.ParseExpr` 把它解析为 `Expr`：一个 `Matcher`，`String` 以规范形式打印它，只带结构需要的括号。它是一个基于小型
  扫描器的递归下降解析器；错误给出所在字节（`at byte 22: there is no matcher Nope`），`config.Validate` 把它报告在 `expr`
  字段上。表达式最长 64 KiB，嵌套最深 100 层，因此配置无法让解析器无限递归；一长串 `&&` 并不算深。`runtime` 在编译路由时
  编译它，放在路由其他匹配器之后。
- 测试：`TestParseExpr`（优先级、分组、规范打印、引号）、`TestParseExprErrors`（27 个错误的表达式，各带消息和位置；长度和
  深度限制；一串 1,001 个匹配器）、`TestExprMatch`（一个用到所有运算符和大多数匹配器的表达式，对十个请求）、
  `TestExprAllocs`（求值不分配内存），以及 `FuzzParseExpr`：解析器从不 panic，解析出的表达式打印后再解析得到相同结果。
  在基准机上模糊测试 2 分钟，450 万个输入，没有失败。`config` 和 `runtime` 的测试中也加入了 `expr`。router、config、
  runtime、listener、schema、cmd/fit 和 test/e2e 的竞态测试在基准机上通过。

与计划的差异：

- 实质上没有。表达式不参与路由器的 host 和 path map：表达式中的 host 和 path 通过求值来检查，在 map 按路由的 `host` 和
  `path` 字段找出候选路由之后；需要时可以在 `expr` 旁边同时设置这两个字段。

### I38 重写、跳转与头处理 — 已完成

- `middleware/rewrite`，fit 的第一个中间件：`rewrite` 在处理器（以及代理转发到的上游）看到请求之前修改它的 path：
  `strip_prefix`（列表中第一个在段边界上是 path 前缀的，所以 `/api` 把 `/api/users` 变成 `/users`、把 `/api` 变成
  `/`，不动 `/apix`）、`regexp` 和 `replacement`（Go 的 `ReplaceAllString`，支持 `$1` 和 `${name}`）、`add_prefix`，
  按此顺序。路由已经按客户端发来的 path 选定。新 path 重新转义（清空 `RawPath`），所以客户端发来的 `%2F` 重写后成为
  `/`。`redirect` 以跳转回应，目标为 `to`，其中 `{scheme}`、`{host}`、`{path}`、`{query}` 和 `{uri}` 代表请求的对应
  部分；设置 `regexp` 时只跳转匹配的 path，`to` 中的 `$1` 代表其分组，其余请求交给处理器。`status` 为 301、302（默认）、
  303、307 或 308。
- `middleware/headers`：`headers` 有 `request` 和 `response`，各自先 `delete`（名字，或以 `*` 结尾的前缀），再 `set`，
  再 `add`。请求的修改在处理器之前进行；响应的修改在 `OnHeader` 钩子中进行，钩子函数在 provision 时构造一次，因此
  无论处理器用哪种方式写响应都会生效。名字在 provision 时规范化；每个请求得到自己的值，因为读取头部的代码可能原地修改它们。
- 两者都在 `modules/standard` 中注册，schema 中有它们的参数。
- `internal/fibtest` 的 `Recorder` 现在保存响应头部和正文的副本：fib 的 `Respond` 把池中的头部交给流，流返回后
  清空它，导致记录器看到的钩子添加的字段消失。
- 测试：每个中间件的表驱动测试：`TestRewrite`（在段边界上和带结尾 `/` 的去前缀、转义的斜杠、带编号和命名分组并替换每处
  匹配的正则、去掉开头 `/` 的替换、加前缀、三者按顺序）、`TestRedirect`（所有请求、永久跳转、TLS 下的每个占位符、固定
  URL、正则匹配与不匹配）、`TestHeaders`（set、add、delete、按前缀删除、全部删除、请求和响应、值不在请求间共享）、
  `TestResponseOfAnyKind`（`Respond` 和 `Write`），以及参数错误。`go vet ./...` 以及 middleware、modules、schema、
  runtime、internal、handler 和 listener 的竞态测试在基准机上通过；linux、darwin 和 windows 的 `go vet` 与
  staticcheck 通过。
- 没有基准：没有这些中间件的路由与之前一样运行，而它们是新增的。

与计划的差异：

- `redirect` 和 `rewrite` 一起放在 `middleware/rewrite` 中。头部的值暂不支持占位符。

### I39 限流、并发与大小限制 — 已完成

- `middleware/limit` 中有三个中间件。`rate_limit`：每个键一个令牌桶，每秒 `rate` 个令牌，最多 `burst` 个（默认为 rate
  向上取整），每个请求取一个，没有时回应 429（或 `status`），并带以整秒计的 `Retry-After`。`key` 为 `ip`（默认）、
  `route`（整个路由一个桶）或 `header:Name`（没有该头部时按 ip）；设置 `trusted_proxies` 时，客户端是 X-Forwarded-For
  中最后一个不是可信代理的地址，全部可信时取第一个，与 nginx 的 `real_ip_recursive` 相同；无法解析的列表从该处起不再相信。
  `concurrency_limit`：同时最多 `max` 个请求在处理中，超出的立即回应 429（或 `status`），而不是让它等待。`body_limit`：
  `Content-Length` 超过 `max_bytes` 的请求在处理器看到之前回应 413。
- 令牌桶分在 64 个各有一把锁的分片中，键为 `netip.Addr`（不分配内存）或头部的值，超过 128 字节的值用带随机种子的 64 位
  哈希代替，因此每个键的开销有上限。令牌按纳令牌计数，用整数，使补充累加精确。`max_keys`（默认 100000）限制键的数量：
  已满分片中的新键会在最多八个键中淘汰一个：第一个此刻桶已满的（淘汰它不丢失任何信息），否则淘汰其中第一个。
- `concurrency_limit` 下的请求有三种结束方式，每种都只归还一次名额：处理器返回且未保留请求；响应已发出（`OnFinish`，在
  处理器运行之前注册，因为代理可能在返回之前就已回应）；或客户端离开。fib 的 `OnCancel` 只保存一个回调，因此处理器的回调
  （代理对每个请求都会注册）替换了中间件的回调，`TestConcurrencyClientGone` 发现了这一点：名额一直被占用。已作为
  [A26](fib-api-requests.zh-CN.md#a26一个-context-上可以有多个-oncancel-回调) 提出；在此之前，中间件用
  `context.AfterFunc` 等待请求的 `Context()`，只在处理器返回且请求被保留时才获取它（对已结束的请求获取到的 context 已被
  取消），响应发出后停止。
- `body_limit` 看不到没有声明长度的请求体，它们送到处理器的 `OnBody`：在 fib 有按请求的限制之前，只受监听器的限制约束，
  已作为 [A25](fib-api-requests.zh-CN.md#a25按请求限制请求体大小流式的也包括在内) 提出。
- 测试：`TestRateBucket`（突发、带 `Retry-After` 的拒绝、按半个和整个令牌补充且不超过突发，使用测试时钟）、`TestRateSlow`
  （每秒 0.01、`Retry-After: 100`、其他状态码）、`TestRateKeys`（按 ip、映射到 IPv6 的 IPv4 按 IPv4、按路由、按头部且长值
  取哈希、在可信代理之后）、`TestClientIP`（11 个 X-Forwarded-For 用例）、`TestRateMemoryBounded`（`max_keys` 为 640 时
  10 万个键最多保留 640 个）、`TestEvictPrefersFull`、`TestRateAllocs`（按 ip 计数的请求，包括经 X-Forwarded-For 的，
  不分配内存）、`TestConcurrency`（内联请求；两个被保留并在别处回应，第三个被拒绝，其中一个回应后又能进入）、
  `TestConcurrencyClientGone`（经网络，三轮：挂起的请求占用唯一名额，客户端挂断后归还），`TestBody`，以及参数错误。
  `go vet ./...` 以及 middleware、modules 和 schema 的竞态测试在基准机上通过；linux、darwin 和 windows 的 `go vet` 与
  staticcheck 通过。
- 基准机上的基准（`bench/microbench.sh`，16 个线程并行）：`BenchmarkRate` 在 1000 个键上每个请求 16.0 ns，在保留 10 万个
  的一百万个键上（会淘汰）31.8 ns，所有线程共用一个键（`key: route`，一个分片的锁）时 126.6 ns，都不分配内存；
  `BenchmarkConcurrency`（经 fibtest 的内联处理器）裸跑 315 ns，加上限制 506 ns，多 5 次分配（名额、它的钩子和 fib 的钩子列表）。

与计划的差异：

- fib 的 `middleware/limiter` 按固定或滑动窗口计数，并保留所有键直到清扫；fit 的是对键数量有上限的令牌桶，键按 fit 的
  配置需要来取，因此不使用 fib 的。
- 在 A25 之前，`body_limit` 只覆盖声明了长度的请求体；在 A26 之前，`concurrency_limit` 使用请求的 context。fib
  `cfb4f09` 之后两者都已完成，见下。

**fib `cfb4f09` 之后（A25、A26）。** fit 改用 fib `main` 的 `cfb4f09`。`body_limit` 调用
`Context.LimitBody(max_bytes)`：声明长度超过它的、整体到达且更长的、以及流式且已收到更多的请求体，都以 413 拒绝；没有
声明长度、增长超过它的请求体以 `ErrBodyTooLarge` 结束，处理器的 `OnBody` 会看到；处理器若不回应就放手，fib 回应 413。
既然 fib 保留每个回调，`concurrency_limit` 在处理器运行之前与 `OnFinish` 一起注册自己的 `OnCancel`，不再获取请求的
context。`TestBodyOverNetwork` 经 HTTP/1.1 发送恰好达到和超过限制的分块请求体（小到整体到达的和大到流式的）以及声明长度的
请求体；`TestBody` 和 `TestConcurrencyClientGone` 照旧通过。完整的竞态测试在基准机上通过。没有基准：不使用这两个中间件的
路由不经过它们，`OnCancel` 已由 fib 测量（A26）。

### I40 Basic 认证与客户端 mTLS — 已完成

- `middleware/auth` 的 `basic_auth` 用 HTTP Basic 认证要求用户名和密码（回应 401 并带
  `WWW-Authenticate: Basic realm="…", charset="UTF-8"`），并用 bcrypt 哈希校验，`users` 由 `username` 和
  `password_hash` 组成。`realm` 默认为 fit；`user_header` 把一个请求字段设为用户名，替换客户端发来的同名字段；除非设置
  `forward_authorization`，Authorization 字段在处理器之前删除。`fit hash-password` 输出标准输入中密码的哈希（`-cost`，
  默认 10）；`htpasswd -B` 生成的也一样。
- 一次 bcrypt 校验要几十毫秒 CPU，放在 fib worker 上会拖住它上面的所有连接（规则 1），因此在单独的池上运行：CPU 数的
  四分之一，至少一个 worker，队列 1024。请求以保留状态（`Context.Async`）等待，并持有它的配置；密码通过后，链的其余部分
  在池的 goroutine 上运行，和在别处回应的处理器一样。队列没有空位的、或等待超过 5 秒的校验回应 503 并带
  `Retry-After: 1`；客户端已离开的直接丢弃。通过的密码按用户以中间件自己的密钥做 SHA-256 记住，该用户之后的请求直接在
  worker 上放行；每个用户只记一个，内存受配置约束。因此大量错误密码最多占用池的那份 CPU，已登录的用户照常放行。未知用户的
  密码同样对另一个用户的哈希校验一次，使拒绝所需时间与用户是否存在无关。
- TLS 监听器的 `tls.client_auth` 要求客户端提供证书，并以 `ca_files` 验证：`mode` 为 `require`（默认）时，没有有效证书的
  客户端在握手时被拒绝；`verify_if_given` 允许客户端不提供证书。验证由 fib 的 TLS 自己完成（监听器配置中的 `ClientAuth`
  和 `ClientCAs`），覆盖 HTTP/1.1、h2 和 HTTP/3。CA 文件像证书文件一样被监视；改变模式或 CA 的重载会重新绑定监听器（绑定后
  TLS 配置不可变），不改变的重载保留它。`config.Validate` 拒绝这样的监听器：要求客户端证书、经 ACME 获取自己的证书、且只
  允许 TLS-ALPN-01（CA 不会发送证书）。
- `client_cert` 只放行带有监听器验证过的证书（`VerifiedChains`）的请求，其余回应 403，除非设置 `optional`；
  `subject_header` 把一个请求字段设为证书主体（`CN=alice,O=Example`，控制字符被替换），并删除客户端发来的同名字段。请求
  被认证为谁（Basic 认证的用户，否则是证书主体）记录在 `observe.Entry.User`，访问日志写为 `user`，后续中间件用
  `auth.UserOf` 读取。
- 测试：`TestBasic`（无凭据、错误密码、未知用户、别的用户的密码、记住与未记住、伪造的用户字段）、
  `TestBasicForwardAuthorization`、`TestBasicBusy`（池没有空位、等待过久的校验、池满时已记住的密码照常放行）、
  `TestBasicParams`、`TestClientCert`（已验证、没有、未验证、明文、可选、与 `basic_auth` 一起）、`TestHeaderSafe`；经网络的
  `TestClientAuthRequire` 和 `TestClientAuthVerifyIfGiven`（CA 的客户端、无证书、另一个 CA 的证书，覆盖 HTTP/1.1、h2 和
  HTTP/3）、`TestClientAuthReload`，以及 `TestBasicOverNetwork`（1 MB 的分块和声明长度的请求体经过 `basic_auth` 和其后的
  `body_limit`，第一个在请求体到达期间等待校验，另有一个超过限制）；`TestLoadClientAuth`、`config` 的校验用例、
  `TestHashPassword` 和访问日志的 `user`。所有改动的包及其依赖者的竞态测试在基准机上通过；linux、darwin 和 windows 的
  `go vet` 与 staticcheck 通过。
- 没有基准：不使用新中间件的路由照旧运行，没有 `client_auth` 的监听器得到相同的 TLS 配置。

与计划的差异：

- 证书主体经 `auth.UserOf` 和请求字段提供给后续中间件，而不是一个通用的按请求存储；I42 的 claims 可能需要它。bcrypt
  来自 fit 已依赖的 `golang.org/x/crypto`（决策 007），没有需要记录的新依赖。
- Basic 认证的用户写在配置中；像 htpasswd 那样的用户文件留待以后。

### I41 ForwardAuth — 已完成

- `middleware/auth` 的 `forward_auth` 向认证服务（配置中的一个 upstream，即 `upstream`，带其负载均衡、健康检查和限制）
  的 `uri`（默认 `/`）发送 GET，带上请求的字段（除逐跳字段和请求体相关字段之外的全部，或只有 `request_headers` 列出的）
  以及 `X-Forwarded-Method`、`-Proto`、`-Host`、`-Uri` 和 `-For`，它们替换客户端发来的同名字段。2xx 的回应放行请求，并把
  `copy_headers` 列出的字段从回应复制到请求上，客户端发来的这些字段被删除；其他回应原样交给客户端：状态码、字段，以及整体
  到达的请求体（不超过池的 64 KiB 阈值；更大的被放弃，回应不带请求体）。无法连接的认证服务回应 502，超过 `timeout`
  （默认 10s）回应 504，被配置排空打断的回应 503。
- 子请求经 fib 的异步客户端发送（`Pool.Pick`、`Send`、`Done`）；请求以保留状态等待并持有其配置，链的其余部分在客户端的
  回调中运行，因此等待中的请求不占用 worker（规则 1）。客户端离开时由 `OnCancel` 放弃子请求，超时由定时器放弃，配置排空时
  由 `Drain` 放弃仍在等待的。超时与代理一样计为认证服务后端的失败。
- 测试：`TestForwardAuth`（放行，带认证服务看到的字段和复制的用户；客户端伪造的复制字段被删除；跳转到登录页的回应连同其
  字段和请求体交给客户端；带 200 KiB 请求体的拒绝不带请求体交给客户端；超时的认证服务）、`TestForwardAuthUnreachable`、
  `TestForwardAuthClientGone`（认证服务看到它的请求被放弃）、`TestForwardAuthSlowService`（128 个请求同时各在认证服务上
  等待半秒，多于服务器的 worker 数，同时另一个路由的十个请求都在 250 ms 内回应；128 个请求一起全部放行）以及
  `TestForwardAuthParams`。`middleware/auth`、`schema` 和 `cmd/fit` 的竞态测试在基准机上通过；linux、darwin 和 windows 的
  `go vet` 与 staticcheck 通过。
- 没有基准：不使用该中间件的路由照旧运行。

与计划的差异：

- 认证服务是配置中的 upstream，而不是中间件自己的地址，因此有池的限制、健康检查和负载均衡。客户端自己的
  `X-Forwarded-*` 不被采信，因为这个中间件暂时没有可信代理列表。

### I42 JWT 与 JWKS — 已完成

- `middleware/auth` 的 `jwt` 只放行带有 JSON Web Token（紧凑格式的 JWS）且签名验证通过、claims 成立的请求：`exp` 必须
  存在且在未来，`nbf`（如有）在过去，两者都允许 `leeway` 的偏差；设置了 `issuer` 和 `audience` 时，`iss` 须等于
  `issuer`，`aud`（字符串或列表）须包含 `audience` 之一。RS256/384/512、PS256/384/512、ES256/384/512、EdDSA（Ed25519）
  和 HS256/384/512 用标准库验证；`algorithms` 可以收窄。`none` 永不通过，带 `crit` 的头部和与密钥不符的算法也不通过：
  HMAC 需要配置自己的密钥，因此公钥不能被当作 HMAC 密钥，ECDSA 算法需要对应的曲线。令牌在 Authorization 的 `Bearer`
  之后，或（`token_from`）在另一个字段、cookie 或查询参数中；没有令牌的请求回应 401 并带
  `WWW-Authenticate: Bearer realm="fit"`，令牌无效的再加上 `error="invalid_token"`。
- 密钥：`public_key_files`（PEM 公钥或证书）、`hmac_secret_file`（至少 32 字节），以及 `jwks_url`：一个 JWK 集合，由
  控制面的 goroutine 获取（net/http，10s，最多 1 MiB）——加载配置时、每隔 `jwks_refresh`（默认 1h）、以及令牌的 `kid`
  不在集合中时（最多每 10 秒一次）。这样的请求、以及在首次获取完成之前到来的请求，以保留状态等待正在进行的或下一次获取
  （最多 1024 个等待，超出的被拒绝），完成后重新验证。获取失败时保留原有的密钥。用于加密的密钥、HMAC 密钥、小于 2048 位
  的 RSA 密钥和不在曲线上的 EC 点被排除。`Cleanup` 取消 goroutine，不等待。
- claims 提供给后续中间件：`auth.ClaimsOf`，保存在 `observe.Entry.Claims`；除非之前的中间件已经设定，`sub` 是请求被
  认证的身份（`UserOf`，访问日志的 `user`）。`claims_headers` 把请求字段设为 claims（字符串原样，其他为 JSON，控制字符
  被替换），并删除客户端发来的这些字段，无论是否放行。
- 验证 RSA、ECDSA 或 Ed25519 签名要占用 worker 几十微秒的 CPU，而一个令牌在有效期内会反复发送，因此通过的令牌按其
  SHA-256 保存在 16 个分片中，每片最多 1024 个，满了随机忘掉一个；保存的令牌只对验证它时的那组密钥有效，因此 JWK 集合
  删除的密钥立即失效，且每次使用都检查 `exp` 和 `nbf`。
- 测试：`TestJWTAlgorithms`（全部 13 种算法；P-256 密钥用于 ES384、另一个 RSA 密钥、none、把 RSA 公钥当作 HMAC 密钥；
  `algorithms`）、`TestJWTClaims`（exp 和 nbf 在偏差内外、缺少 exp 或为字符串、小数的 exp、issuer、audience、被篡改的
  载荷、格式错误的令牌、crit、超过 16 KiB 的令牌、时钟越过 exp）、`TestJWTTokenFrom`、`TestJWTClaimsHeaders`、
  `TestJWTParams`、`TestParseJWKS`、`TestJWTCache`、`TestJWKSRotation`（首次获取前的请求等待它；新的 kid 触发获取并等待；
  被删除的密钥不再通过；同时 20 个未知 kid 最多每 minRefetch 获取一次）和 `TestJWKSRefresh`（每 50ms 获取；解析失败的
  集合保留原有密钥）。完整的竞态测试在基准机上通过；linux、darwin 和 windows 的 `go vet` 与 staticcheck 通过。
- 基准机上的基准（`BenchmarkJWT`，经 fibtest 通过 `jwt` 的一个请求，3 次）：重新验证时 HS256 3.2 µs、RS256 25.7 µs、
  EdDSA 39.8 µs、ES256 54.5 µs；已保存的 0.47–0.65 µs，5 次分配。

与计划的差异：

- 没有使用 JWT 库：标准库能验证所有算法，解析很小，因此没有新依赖。claims 保存在访问日志的条目里——fit 唯一的按请求存储
  ——而不是单独的存储。

### I43 重试与按路由超时 — 已完成

- 代理的 `retry` 把请求再发一次，发往上游策略选出的下一个后端，第一次之后最多 `attempts`（默认 2）次：连接未能建立的请求
  （拨号错误，或该后端已有 `max_pending` 个请求在等待），无论方法，因为它从未发出；以及 GET、HEAD、OPTIONS、TRACE、PUT
  和 DELETE（RFC 9110 的幂等方法）在响应开始之前失败的，或响应状态码在 `statuses` 中的（只能是 5xx；默认没有），此时放弃
  其响应体。请求体流式发送的（超过 64 KB、仍在到达或带 trailer）永不重发，因为请求体已经发走；整体到达的从其字节重发。
  重试受预算限制，与 Finagle 和 Linkerd 相同：每个请求增加 `budget_ratio`（0.2）次重试，每秒增加
  `budget_min_per_second`（10）次，最多保存十秒的量，初始为一秒的量，因此重试最多给已在失败的上游增加约五分之一的负载。
  失败的尝试照旧计为其后端的失败；因状态码重试的不计。
- 代理的 `timeout` 限制一个请求从代理接收到响应结束的时间，包括重试：响应开始之前超过的回应 504，正在进行的响应像上游中断
  那样被截断。不计为后端的失败。默认没有；上游自己的超时照旧有效。
- 测试（`handler/proxy/retry_test.go`）：`TestRetryConnectFailure`（一个拒绝连接的后端和一个正常的：每个 GET 和 POST
  都由正常的后端回应，各一次）、`TestRetryNotResentOnceSent`（读完请求后关闭连接的后端：POST 到达一次，PUT 在 4 次
  尝试中各到达一次，fib 的客户端自己重发时为两次）、`TestRetryStatuses`（一个回应 503、一个回应 200 的后端：GET 都得到
  200；POST 得到 503 且不重试）、`TestRetryBudget`（50 个 GET 发往回应 503 的后端，`budget_ratio` 为 0.2 且没有每秒
  额度：最多 10 次重试）、`TestRouteTimeout`（慢上游得到 504；下载被截断）和 `TestRetryParams`。完整的竞态测试在
  基准机上通过。
- 基准机上的 `bench/ab.sh`，代理场景，3 轮各 10s，main 对比本改动（未设置 retry 或 timeout）：HTTP/1.1 每秒 246k → 245k
  请求（−0.6%），h2c 240k → 240k（−0.1%），都在噪声范围内。

与计划的差异：

- 重试属于代理，而不是 `middleware/retry`：重发需要代理的请求、后端和池。状态码重试只用于幂等请求，预算由一个代理（一个
  路由）的请求共享，而不是由上游共享。

### I44 熔断 — 已完成

- `middleware/breaker` 的 `circuit_breaker` 是路由请求上的熔断器，有三种状态。关闭时放行请求，并在最近的 `window`
  （10s，分十步）内统计失败的响应（5xx，或 `statuses` 中的状态码）和开始得比 `slow_threshold` 慢的响应（从请求到响应头；
  默认不统计）；统计到至少 `min_requests`（20）个且失败达到 `error_ratio`（0.5），或慢响应达到 `slow_ratio`（0.5）时打开。
  打开时立即以 503 回应每个请求，并用 `Retry-After` 说明何时再试，持续 `open_duration`（30s）。之后半开，放行
  `half_open_requests`（5）个请求，其余回应 503：它们都正常则关闭，窗口清空；有一个失败或慢则再次打开。客户端在得到回应
  之前离开的请求不计入，半开时的这类请求归还其名额。
- 结果在 `OnHeader` 钩子中得到，无论处理器以何种方式回应；客户端离开在 `OnCancel` 中得到；状态和窗口由一把锁保护，每个
  请求获取两次。`State` 报告当前状态。
- 测试，使用测试时钟：`TestBreakerTransitions`（低于比例和低于 `min_requests` 时保持关闭；超过后打开，503 带
  `Retry-After` 30，之后为 10；`open_duration` 之后半开；失败的探测请求使其再次打开；正常的探测请求使其关闭，窗口清空）、
  `TestBreakerHalfOpenLimit`（同时只放行 `half_open_requests` 个；被取消的探测请求归还名额）、`TestBreakerWindow`
  （离开窗口的失败不计入）、`TestBreakerSlowAndStatuses` 和 `TestBreakerParams`。完整的竞态测试在基准机上通过；linux、
  darwin 和 windows 的 `go vet` 与 staticcheck 通过。
- 基准机上的基准（`BenchmarkBreaker`，经 fibtest 的内联处理器，3 次）：裸跑 75.6 ns，在关闭的熔断器后 444 ns，多 9 次分配
  （结果对象、它的两个钩子、fib 的钩子列表）。

与计划的差异：

- 熔断器属于路由（作为中间件），而不是上游：共享一个上游的路由各有自己的熔断器。延迟以慢响应的比例计入，而不是平均值。

### I45 流量拆分：权重、镜像、故障转移 — 已完成

- 代理可用 `upstreams` 代替 `upstream`：每个上游带一个 `weight`（1 到 1000），请求按权重随机分配到它们之上，与金丝雀发布
  相同；之后每个上游按自己的策略选择后端，请求的重试留在它的上游内。
- `failover` 指定一个上游：当选中的上游没有在轮转中的后端（因失败即 `max_fails`，或因健康检查被移出），而它自己有时，由它
  接收请求；由 `upstream.Pool.Available` 判断。在此之前照旧使用选中的上游。
- `mirror` 把请求体整体到达（最多 64 KB）的请求中的一部分（`percent`，默认全部）复制一份发往另一个上游，方法、路径、字段和
  请求体相同，并丢弃其回应。副本在请求继续之前生成，有自己的头部、URL 和请求体，因为它可能比请求活得久；它经镜像的池异步
  发送，请求从不等待它。每个代理同时最多 256 个副本在途；超出时不再复制，因此慢的或不可用的镜像最多占用这么多。
- 测试：`TestSplitWeights`（800 个 3:1 的请求落在 600 和 200 的五个标准差之内）、`TestSplitFailover`（`max_fails` 为 1 的
  拒绝连接的上游：第一个请求 502，其余发往故障转移上游）、`TestMirror`（副本带有方法、路径、查询、字段和请求体）、
  `TestMirrorDoesNotSlow`（同时 400 个请求，旁边是一个要一秒才回应的镜像，全部在 0.9s 内回应，镜像收到 1 到 256 个副本）、
  `TestMirrorPercent`（25%：400 个中约 100 个）和 `TestSplitParams`。`handler/proxy`、`upstream`、`config`、`runtime`、
  `admin`、`schema`、`cmd/fit` 和 `test/e2e` 的竞态测试在基准机上通过。
- 基准机上的 `bench/ab.sh`，代理场景，3 轮各 10s，main 对比本改动（一个上游，无镜像或故障转移）：HTTP/1.1 每秒 246k → 246k
  请求（−0.0%），h2c 240k → 240k（+0.0%）。

与计划的差异：

- 拆分属于代理（按请求选择上游），而不是 `upstream` 包；`upstream` 只增加了 `Pool.Available`。流式的请求体不做镜像，因为
  复制它们意味着缓冲。

### I46 压缩 — 已完成

- `middleware/compress` 的 `compress` 用 zstd、brotli 或 gzip 压缩响应：选请求的 `Accept-Encoding` 最偏好的
  （q 值、`*`、`x-gzip`），同样偏好的取 `encodings` 中靠前的（默认 zstd、br、gzip）。满足以下条件的响应被压缩：状态码
  带 body 且不是 206，自身没有 `Content-Encoding` 或 `Content-Range`，`Cache-Control` 不含 `no-transform`，类型可压缩
  （fib 的 `compress.Compressible`：文本、JSON、JavaScript、XML、SVG、字体），声明的长度（如有）至少为 `min_length`
  （1024）。这样的响应带 `Vary: Accept-Encoding`（客户端不接受任何编码或请求是 HEAD 时也带），强 `ETag` 变为弱的。
  `level` 为 fastest、default（gzip 5、zstd 默认、brotli 4）或 better（gzip 7、zstd better、brotli 6）。
- 压缩是 fib 的响应体过滤器（[A27](fib-api-requests.zh-CN.md#a27在写出时改写响应体的钩子)，`Context.OnBodyFilter`，由本会话
  在 fib 中实现并作为 `fc7ec5e` 推送到其 `main`）：响应体在写出时压缩，因此流式的响应（代理超过 64 KB 的响应或
  server-sent events）仍然流式，处理器 flush 的内容也从编码器中 flush 出去；整体发送的响应整体压缩。除编码器的窗口外不缓冲
  任何东西。
- 编码器按编码放在池中，每个响应 reset 一次；zstd 以一个 goroutine 的并发（调用者自己的）运行，每个编码器的窗口为 256 KB，
  因此一个只占几百 KB。`max_in_flight`（1024）限制同时压缩的响应数，超出时不压缩。响应在 body 结束时归还名额；客户端先
  离开时在 `OnCancel` 中归还，编码器交给垃圾回收而不是放回池中，因为可能还有写入在使用它。
- 新依赖 `github.com/klauspost/compress` v1.20.3（zstd、gzip）和 `github.com/andybalholm/brotli` v1.2.6，都是纯 Go，
  自身没有依赖：[决策 008](decisions/008-compression-libraries.zh-CN.md)。
- 测试：`TestCompress`（经 HTTP/1.1 和 h2c：每种编码、偏好与 q 值、`*`、`identity` 和 q=0、带 ETag 的整体响应和分段写出的
  大响应，都解压为原 body，线上至多为其十分之一；小的、图片、已编码、206 和 no-transform 的响应不处理；HEAD）、
  `TestCompressStream`（一个 server-sent event 经每种编码，在处理器写下一个之前解压到达）、`TestCompressBounded`
  （`max_in_flight` 为 1：第二个响应不压缩，流式的客户端离开后名额归还）、`TestNegotiate` 和 `TestCompressParams`。完整的
  竞态测试在基准机上通过；linux、darwin 和 windows 的 `go vet` 与 staticcheck 通过。
- 基准机上的基准（`BenchmarkCompress`，经 fibtest 整体发送 16 KB 的 JSON，3 次）：不压缩 2.3 µs；gzip fastest 8.8 µs、
  default 9.1 µs；brotli 10.6 µs 和 16.6 µs；zstd 6.2 µs 和 6.4 µs；21 或 22 次分配，约 2 KB。

与计划的差异：

- 需要 fib 的响应体过滤器（A27），由本会话加入 fib：fib 唯一能看到 body 的钩子会把流式响应整体缓存。fib 自己的
  `middleware/compress`（gzip 和 deflate，经 `OnResponse`）没有使用，但用了它的 `Compressible`。

### I47 WebSocket 代理 — 已完成

- 代理把要求切换协议的请求（HTTP/1.1 的 `Connection: Upgrade` 加 `Upgrade`，WebSocket 或其他协议）作为 HTTP/1.1 upgrade
  转给上游，带上它的字段和转发字段，经上游的池发送（`upstream.Pool.SendUpgrade`，即 fib 的 `Client.StartUpgrade`，A29），
  因此握手使用池的拨号、TLS、限制和负载均衡。请求以保留状态等待，不占用 worker。上游回应 101 时，在上游客户端的回调中用
  上游的回应（它选定的子协议和扩展）切换客户端的请求（对保留请求调用 `Context.Upgrade`，A28），并把两个 tunnel 连接起来；
  其他回应交给客户端，没有回应时与其他请求一样回应 502、503 或 504。
- 中继把每一侧到达的字节原样交给另一侧，当另一侧未发送超过 64 KB 时暂停这一侧的读取：HTTP/1 上由
  `Connection.NotifyOutput` 告知何时排空，不需要任何等待；两条连接自身的写背压都关闭（与 fib 的中继相同），因为由中继来
  调节。一侧结束后，另一侧在已发送的数据发出后结束。两个方向在 `tunnel_idle_timeout`（默认 10 分钟）内都没有数据的
  tunnel 被关闭。配置排空时关闭它的 tunnel，客户端在 101 之前离开时放弃握手。tunnel 结束之前后端都计为在途。
- 测试（`websocket_test.go`、`websocket_unix_test.go`）：`TestWebSocketEcho`（带 key 对应 accept 和选定子协议的 101、带
  字段的上游握手、回显、同时每个方向 16 MB、客户端连接结束时上游连接也结束）、`TestWebSocketBackpressure`（客户端一秒不
  读：上游被暂停，发出的不到 32 MB 的一半，之后客户端收到全部数据）、`TestWebSocketRefused`（带 body 和字段的 403；没有
  上游时 502）和 `TestWebSocketIdleTimeout`。完整的竞态测试在基准机上通过；linux、darwin 和 windows 的 `go vet` 与
  staticcheck 通过。
- 基准机上的空闲 WebSocket 内存，用基准框架的空闲模式（`-idle ws`），1 万个连接到保持它们的后端
  （`bench/results/i47-ws-idle-2026-10-21.json`）：fit 每个连接 5,761 字节，nginx 18,176 字节。fit 的基准配置加了 `/ws`
  路由，fit 加了 `ws` 空闲类型。

与计划的差异：

- 需要 fib 的三项能力，由本会话加入（A28-A30）：处理器返回后再 Upgrade 被保留的请求、客户端请求收到 101 时移交连接、
  HTTP/3 客户端发送 extended CONNECT。背压测试起初停滞，原因是测试在连接建立后才缩小 socket 的接收缓冲区，使 Linux 进入
  零窗口探测，而不是中继的问题：现在在连接之前设置缓冲区。

### I48 HTTP/2 和 HTTP/3 入口上的 WebSocket — 已完成

- 针对 `websocket` 的 extended CONNECT（h2 和 h2c 上的 RFC 8441，h3 上的 RFC 9220）作为它所代表的 HTTP/1.1 握手发往上游：
  GET，带 `Connection: Upgrade`、`Upgrade: websocket`、由 fit 生成的 `Sec-WebSocket-Key`（HTTP/2 或 HTTP/3 的 WebSocket
  没有它），以及客户端的 `Sec-WebSocket-Version`、`-Protocol` 和 `-Extensions`。上游的 101 变成打开客户端流的 200，不带
  `Sec-WebSocket-Accept`。针对其他协议的 extended CONNECT 回应 501。服务器保留的伪头部字段（如 `:protocol`）不发往上游。
- 客户端的 tunnel 是多路复用连接上的一个流，fib 只能等待它排空（A31）：它的读取被暂停期间，每个被暂停的 tunnel 有一个
  goroutine 在 `WaitSendable` 中等待（与 fib 自己的 CONNECT 中继相同），直到 A31 替换它。
- 测试：`TestWebSocketExtendedConnect` 经 h2（TLS）、h2c 和 h3 入口打开 WebSocket，都到 HTTP/1.1 上游：带选定子协议、
  不带 accept 的 200，带 key 和版本的上游握手，回显，每个方向 1 MB。net/http 的 Transport 拒绝 `:protocol` 字段，因此 h2
  和 h2c 的客户端是用 fib 的 `hpack` 写的最简 HTTP/2 客户端；h3 用 fib 的 HTTP/3 客户端（A30）。在基准机上通过。
- 除 I47 的之外没有基准：代理的其他请求只多一次判断（请求是否要求切换协议）。

与计划的差异：

- 实质上没有；上面的临时方案等待 A31。

### I49 gRPC 代理 — 已完成

- gRPC 调用（`application/grpc` 或其子类型的 HTTP/2 或 HTTP/3 POST）像其他请求一样被代理：消息到达即双向流式转发，上游的
  trailer（带 `grpc-status` 和 `grpc-message`）跟在响应之后；它需要 h2 或 h2c 上游。代理额外做的：向上游发送
  `TE: trailers`；`grpc-timeout` 像路由的 `timeout` 一样在代理处限制调用，取两者中较短的，并把选中的告诉上游（路由的
  timeout 较短或调用没有设置时，它成为调用的 `grpc-timeout`）；代理无法完成的调用按 gRPC 的方式应答，而不是客户端读不懂的
  HTTP 状态：响应开始前，用 trailers-only 响应（状态 200，带 `grpc-status` 和 `grpc-message`）：没有上游、连接被拒或失败、
  drain 或 `max_pending` 时为 `UNAVAILABLE`，超时为 `DEADLINE_EXCEEDED`，请求体过大为 `RESOURCE_EXHAUSTED`；响应中途被切断时，
  用这些 trailer 代替 `Fit-Error`。两种超时都不计入后端的失败。
- gRPC 客户端可能要等第一条小消息的应答才发下一条，所以监听器在请求头到达时就把 gRPC 调用的请求体交给代理，而不是等它到达
  64 KB：[A32](fib-api-requests.zh-CN.md#a32按请求选择从请求头起流式交给处理器的请求体)，fib `http` 和 `http3` 的
  `Config.StreamRequestBodyAtHeader`，由本会话实现并推送到 fib 的 `main`（`0bd2447`），同时 fib 的 HTTP/2 客户端
  发送原先会被去掉的 `te: trailers`。
- 测试（`handler/proxy/grpc_test.go`），经 fit 到 fib 的 gRPC 服务器，h2c 和 h2（TLS）上游，使用 fib 的 gRPC 客户端和 JSON
  编解码：`TestGRPCUnary`（回显、200 KB、上游的状态和 trailer 被传回、上游看到约 5 秒的期限）、`TestGRPCServerStreaming`
  （回传 2,000 条消息）、`TestGRPCClientStreaming`（上传 500 条 1 KB 消息，一个应答）、`TestGRPCBidiStreaming`（50 条消息，
  每条在下一条发出前得到应答，其中有 300 KB 的）、`TestGRPCNoUpstream`（`UNAVAILABLE`）、`TestGRPCUpstreamGone`（上游在两条
  消息后停止的服务端流，在这两条之后以 `UNAVAILABLE` 结束）、`TestGRPCTimeouts`（上游挂起时，调用 300ms 的 `grpc-timeout` 和
  路由 2s 的 timeout 各自由 fit 回应 `DEADLINE_EXCEEDED`，路由的 timeout 作为调用的期限传给上游，之后后端仍在轮转中）和
  `TestGRPCHelpers`。没有 A32 时 `TestGRPCBidiStreaming` 卡在第一个应答上。`handler/proxy`、`listener` 及依赖它们的包的竞态
  测试在基准机上通过，带 A32 的 fib `http`、`http3`、`grpc` 也通过；`go vet` 和 staticcheck 在 linux、darwin、windows（fib
  还有 freebsd）上通过。
- 基准机上的 `bench/ab.sh`，代理场景，3 轮各 10 秒，`fa1886a` 对比本改动（fib 为 `9ab22e8`）：HTTP/1.1 每秒 245k → 244k 请求
  （−0.3%），h2c 238k → 237k（−0.5%），都在噪声内。fib 的微基准（基准机上的 `bench/microbench.sh`，各 10 次），`main` 对比分支：服务器的 `ServerKeepAlive` 和
  `ServerKeepAlivePOST` 在噪声内不变，单独解析器的 `ParserKeepAliveGET` +5.9%、`ParserContentLength` +4.5%，尽管两者都不走新
  代码；只在 `main` 上加入新的 `Config` 字段就让这两项 +2.5% 和 +1.8%，其余是代码布局。

与计划的差异：

- 需要 fib 的 A32，由本会话实现。监听器的请求体读取超时（fib 的 5 分钟）仍限制客户端流式发送的调用，上游的
  `response_idle_timeout`（60 秒）限制静默这么久的调用：更长的流需要调大它们。

### I50 TCP 代理 — 已完成

- `protocols` 为 `["tcp"]` 的监听器提供原始 TCP，在新的顶层 `services` 中列出它的服务把它的连接逐字节代理到服务的上游：
  `{"name": "pg", "listeners": ["db"], "upstream": "postgres"}`。tcp 监听器不提供其他协议、不接受 `tls`；每个只属于一个服务，
  路由不能指定它。上游就是 HTTP 用的那个：它的地址、`load_balancing`（`ip_hash` 按客户端地址；连接没有的 header、cookie 和
  URI 哈希依次轮转）、`max_fails` 和健康检查，以及它的 `connect_timeout`（`upstream.Pool.ConnectTimeout`）；连接失败时换到下一个
  选中的地址重试，最多三个。服务的 `idle_timeout`（10 分钟）关闭两个方向都没有数据的连接，`max_conns`（10,000）限制它同时代理的
  连接：超出的连接一被接受就关闭，监听器自己的 `limits.max_conns` 也照常生效。
- 包 `l4` 的中继不做任何等待，与 I47 的中继相同：上游连接经 fib 引擎异步拨号（`Engine.DialWithHandler`），期间暂停客户端读取；
  每一侧的数据在收到它的回调里发往另一侧；另一侧未发送的数据超过 64 KB 时暂停这一侧的读取，`Connection.NotifyOutput` 告知排空后
  恢复；两个连接自己的写背压都关闭。一侧结束后，等它发出的数据送完再结束另一侧（不支持半关闭，与 nginx 默认关闭的
  `proxy_half_close` 相同）。连接保留它开始时的 graph：保留监听器的重载把新连接交给新服务，旧连接在 drain 超时后关闭。
- 测试（`l4/l4_test.go`、`config`）：`TestTCPEcho`（回显，以及同时 50 个连接）、`TestTCPLarge`（同时每个方向 32 MB）、
  `TestTCPSlowClient`（客户端一秒内不读，上游发送 64 MB：上游发出的不到一半，之后客户端收到全部）、`TestTCPSlowUpstream`
  （反方向同理）、`TestTCPHangUp`（上游关闭前发送的内容到达客户端，然后结束；上游得知客户端关闭）、`TestTCPConnectFailure`
  （上游拒绝连接时关闭客户端；一个拒绝的地址和一个可用的地址，每个连接都到达可用的那个）、`TestTCPIdleTimeout`、
  `TestTCPMaxConns`、`TestTCPReload` 以及配置检查。在上游仍在拨号、读取被暂停时写入并关闭的客户端，其关闭一直不被察觉：fib 会丢失读取暂停期间看到的对端结束
  （[A33](fib-api-requests.zh-CN.md#a33读取被暂停时看到的对端结束不再丢失)），由本会话在 fib 中修复。竞态测试在基准机上通过，
  `l4` 连续 28 次通过；更早的一次 `l4` 运行失败过一次，之后没有再现，原因未查到。
- 基准机上的 `BenchmarkTCPProxy`，一个连接在 loopback 上每次回显 256 KB（5 次）：直连回显服务器 11.0 GB/s，经 fit 2.18 GB/s。
  没有 `bench/ab.sh`：HTTP 路径只是每个监听器绑定时多一次判断，每个请求没有。

与计划的差异：

- TCP 服务是顶层的 `services` 列表，而不是路由上的处理器，因为连接没有可供路由的请求；I52 的 SNI 分流会给它们加上匹配条件。
  服务还没有访问日志和指标。

### I51 UDP 代理 — 已完成

- `protocols` 为 `["udp"]` 的监听器接收数据报，列出它的服务把数据报代理到自己的上游：服务的 `listeners` 现在可以同时列出 tcp
  和 udp 监听器，因此一个服务可以在两者上提供 DNS；tcp 和 udp 监听器可以共用一个地址（配置按传输层检查地址，HTTP 和 HTTP/3
  也算在内）。会话即客户端地址：fib 把每个向监听器发送数据报的地址当作一个连接，它的会话自己拨一个 UDP socket 到上游地址，
  地址的选择与 TCP 相同（`ip_hash` 按客户端地址），因此上游看到每个客户端一个地址。两个方向的每个数据报都作为一个数据报转发，
  不论大小；会话的 socket 打开之前到达的数据报最多缓存 64 KB，其余丢弃，发送失败的数据报也丢弃：UDP 没有背压。会话在两个方向
  都没有数据超过 `idle_timeout` 后结束（UDP 默认 1 分钟，TCP 默认 10 分钟），排空时结束，上游拒绝时也结束（ICMP 拒绝会关闭它的
  socket，并计入后端的失败）；客户端的下一个数据报开始新的会话。`max_conns` 把会话与连接一起计数，超出的会话一打开就关闭。
- 引擎自己对静默 UDP 对端的清扫，整个引擎只有一个超时，HTTP/3 也依赖它，因此会话不参与它，而像 TCP 中继那样用自己的定时器：
  [A34](fib-api-requests.zh-CN.md#a34udp-对端自己的空闲超时或不超时)，`Connection.SetUDPIdleTimeout`，由本会话在 fib 中实现，
  已推送到 fib 的 `main`（`11de9eb`）。
- 测试（`l4/udp_test.go`、`config`）：`TestUDPEcho`（1、1,400 和 60,000 字节（macOS 和各 BSD 默认不发送超过 9,216 字节的数据报，在那里是 9,000）的数据报都完整返回；同时 200 个客户端，每个往返
  10 次，每个都是一个会话，在上游是一个地址）、`TestUDPSessionsExpire`（空闲的会话在 300 毫秒后结束，繁忙的会话保持它的 socket，
  空闲客户端的下一个数据报来自新的 socket）、`TestUDPMaxConns`、`TestUDPRefused`（上游拒绝时结束会话；一个拒绝的地址和一个可用
  的地址时，客户端得到可用地址的回答）和 `TestUDPReload`（会话保持它的服务直到 drain 超时，之后转到新服务），以及配置检查。
  它们在基准机上的竞态检测下通过，UDP 测试连续 10 次通过；fib 的 `TestUDPPeerIdleTimeout` 也通过。
- 没有基准：UDP 是新路径，HTTP 的路径只是每个监听器绑定时多一次比较。

与计划的差异：

- 需要 fib 的 A34，由本会话实现并推送到 fib 的 `main`。fib 自己的 `TestUDPSendBatch`、
  `TestUDPSendBatchSegments` 和 `TestUDPBurstFromManyPeers` 在基准机的 Docker 容器中失败，fib 的 `main` 与分支相同；它们与这次
  改动无关，以 fib 的 CI 为准。

### I52 SNI 分流与 PROXY protocol — 已完成

- 服务的 `server_names`（`"a.example.com"`，或 `"*.example.com"` 匹配下一级的一个标签）按 ClientHello 请求的名字分流它的 tcp 监听器
  上的 TLS 连接，不终止 TLS：字节原样转发，由后端自己的证书完成握手。一个 tcp 监听器现在可以被多个服务列出，其中至多一个没有
  `server_names`，它接收没有名字匹配的连接、非 TLS 的连接和没有名字的 hello；没有这样的服务时这些连接被关闭。监听器在服务接手前
  读取 hello（可跨多个记录），最多 64 KB、10 秒内（`l4.OpeningTimeout`）；之后服务拨号上游，已读的数据最先发送。包 `l4` 的
  `Entry` 是 tcp 和 udp 监听器现在交出连接的地方。
- PROXY protocol v1 和 v2。监听器的 `proxy_protocol: {"trusted": [...]}` 在来自这些地址的每个连接开头读取头部（受信任的连接没有
  头部时关闭；来自其他地址的连接照常服务），头部中的客户端成为连接的对端：对 tcp 监听器，用于 `ip_hash` 和向后转发；对基于 TCP 的
  HTTP 监听器（无论有无 TLS，头部在 TLS 之前），成为请求的 `RemoteAddr`，`X-Forwarded-For` 和访问日志随之使用它。这需要
  [A35](fib-api-requests.zh-CN.md#a35按-proxy-头替换连接的对端地址)，`Connection.SetRemoteAddrPort`，由本会话在 fib 中实现并推送到
  `main`（`530d222`）。服务的 `proxy_protocol: "v1"` 或 `"v2"` 先向上游发送头部，写明客户端及它连接的地址（有传入头部时用
  传入头部的）。
- 测试（`l4/codec_test.go`、`l4/sni_test.go`、`config`）：`TestReadSNI`（crypto/tls 的 hello 整体、按 100 字节分记录、它的每个前缀、
  没有名字的 hello，以及 HTTP）、`TestProxyRoundTrip` 和 `TestParseProxy`（两个版本、IPv4 和 IPv6、每个前缀、UNKNOWN、LOCAL、Unix
  地址族、TLV 以及格式错误的头部），两个解析器的模糊测试（在基准机上各 30 秒），`TestEntryPick`，`TestSNIRouting`（按精确名和
  通配名选到两个 TLS 后端，用后端证书完成握手，明文客户端到 fallback，没有服务的名字被关闭），`TestSNIHalfHello`，
  `TestProxyProtocolToUpstream`（上游收到的 v1 行和 v2 头部），`TestProxyProtocolFromClient`（受信任客户端发送的 v1 和 v2，分多次写入，
  以 v1 向后转发；受信任客户端没有头部时关闭；不受信任的客户端以自身地址服务）和 `TestProxyProtocolHTTP`（上游的
  `X-Forwarded-For` 是头部中的客户端，IPv4 的 v1 和 IPv6 的 v2，或不受信任的客户端自己），以及配置检查。它们在基准机上的竞态检测下
  通过，`l4` 连续 10 次通过，完整测试集也通过；fib 的 `TestSetRemoteAddrPort` 也通过。
- 没有基准：没有 `proxy_protocol` 的 HTTP 监听器与以前相同；tcp 中继的回调在两种附件类型间选择，原来是断言一种。

与计划的差异：

- PROXY 头由 tcp 服务发送，HTTP 代理尚不向上游发送，UDP 和 HTTP/3 也不接收；并且需要 fib 的 A35。

### I53 一个端口多种协议；SOCKS — 已完成

- 监听器的 `protocols` 现在可以在 HTTP 协议之外列出 `tcp` 和新增的 `socks`：服务其中多种的监听器用 fib 的 `mux`
  （`listener.Server.sniff`）按客户端先发的内容在同一端口上区分它们。TLS ClientHello 交给 `server_names` 包含其名字的服务，
  像 I52 那样透传；否则在有 `tls` 的监听器上终止 TLS，作为 HTTPS 服务（按 ALPN 走 HTTP/1.1 或 h2）；否则交给该监听器没有
  `server_names` 的服务。SOCKS 握手交给 SOCKS；明文监听器上的 HTTP/1.1 请求或 h2c 前言交给 HTTP；其余交给没有 `server_names`
  的服务，没有就关闭。10 秒不说话的客户端会被关闭，所以服务端先说话的协议需要单独的监听器。来自 `proxy_protocol` 受信任地址的
  PROXY 头在这一切之前读取。监听器上的 `tls` 现在表示它的 HTTP 是 HTTPS；不服务 HTTP 的监听器不接受 `tls`。
- 交接：`l4.Entry.Open` 接收监听器的 TLS 处理器（`term`），没有服务认领的 TLS 连接在读完 hello 后（没有服务带名字时立即）连同已读
  的数据经 `Connection.SetHandler` 交给它，与 fib 的 mux 交接方式相同；此时释放 graph，之后每个请求各自持有。
- `socks` 单独或与其他协议一起：经 fib 的 `socks` 服务器提供 SOCKS5、SOCKS4 和 SOCKS4a 的 CONNECT，不认证；BIND 与 UDP
  ASSOCIATE 被拒绝。监听器的 `socks` 规定谁能用、能连到哪里（`config.ForwardProxy`）：`clients`（必填）、`allow` 与 `deny`（主机名、
  `*.example.com` 表示其下所有名字、地址和 CIDR 范围）、`ports`、`connect_timeout`（10 秒）和 `idle_timeout`（5 分钟）。范围也作用于
  主机名解析出的地址：`l4.Rules.Dial` 经 fib 拨号这样的名字（fib 在自己的池里解析），在连接的 `OnOpen` 里检查连上的地址，规则拒绝的
  在 SOCKS 服务器得知之前关闭（应答 2）。规则变化时监听器重新绑定，与限制变化时相同。
- fib 的 SOCKS 中继没有空闲超时（规则 10）：[A36](fib-api-requests.zh-CN.md#a36socks-与-connect-中继的空闲超时)，
  `socks.Config.IdleTimeout`，由本会话在 fib 中实现并推送到 fib 的 `main`（`4f1c3e9`）。
- 测试（`l4/forward_test.go`、`l4/sniff_test.go`、`test/e2e/multiproto_test.go`、`config`）：`TestRulesDest`（客户端、名字、通配符、
  范围、端口、deny 先于 allow、留给地址判断的名字）、`TestSOCKS`（按地址，以及只有其地址被允许的 `localhost`；拒绝端口、地址和
  BIND；地址落在 deny 中的名字在连上后被拒；不允许的客户端）、`TestSOCKSIdleTimeout`、`TestSniffClear`（明文的一个端口上同时有
  HTTP/1.1、h2c、按名字透传的 TLS、SOCKS 和原始字节），以及 e2e 的 `TestOnePortManyProtocols`（一个端口上用 fit 的证书提供
  HTTP/1.1 和 h2 的 HTTPS、把 TLS 透传给用自己证书的后端、SOCKS 和原始 TCP；一个有 tls 且唯一服务不带名字的监听器；一个只有
  HTTPS 和 SOCKS、没有 tcp 的监听器）。完整的竞态测试在基准主机上通过，fib 的 `http`、`socks`、`mux` 和 `multiproto` 包也通过。
- 没有基准测试：只服务 HTTP 的监听器绑定方式不变，区分协议的监听器只在连接开始时付出代价。

与计划的差异：

- CONNECT handler 移到 I53b。区分协议的端口上的自定义协议由 tcp 服务承担，fit 没有自己的匹配器。SOCKS 还没有用户名和密码：
  fib 的 `Authenticate` 在 worker 上运行，bcrypt 会阻塞它。

### I53b CONNECT 正向代理 — 已完成

- `forward_proxy` handler 在匹配 CONNECT 的路由上（`"match": {"method": ["CONNECT"]}`）提供 HTTP/1.1、HTTP/2 和 HTTP/3 的
  CONNECT，参数就是 SOCKS 的那套规则：规则拒绝的客户端或目标回 403，地址被拒的主机名回 502，拨号超过 `connect_timeout` 回 504；
  不是 CONNECT 的请求回 405。它是 fib 的 `ConnectProxy`，其 `Allow` 检查客户端（有 PROXY 头时是头里的客户端）和目标，`Dial` 是
  `l4.Rules.Dial`。
- fib 的 `ConnectProxy` 在服务请求的 worker 上等拨号（规则 1）：[A37](fib-api-requests.zh-CN.md#a37connectproxy-在拨号回调里应答)
  让它 retain 请求并在拨号回调里应答，A36 给它的隧道加上空闲超时。
- 测试（`handler/forward`）：`TestConnect`（按地址和 `localhost`；拒绝端口、地址和不是 host:port 的目标；地址落在 deny 中的名字回
  502；不允许的客户端）、`TestConnectH2C`（h2c 流上的隧道，用 Go 自己的客户端）和 `TestConnectIdleTimeout`；fib 的
  `TestConnectProxyDoesNotWait` 和 `TestConnectProxyIdleTimeout`。它们在基准主机上的竞态检测下通过。
- 没有基准测试：这是新的 handler，不在其他路由的路径上。

与计划的差异：

- 它的隧道不会像 tcp 服务的连接那样随 graph 的 drain 关闭：它们按空闲超时、对端或监听器自己的 drain 结束。对绝对 `http://` URL
  的请求（普通的 HTTP 正向代理）回 405。

### I54 上游 TLS — 已完成

- I18 留下的上游 TLS 部分：协议为 `h2` 的上游可以设置 `tls_cert_file` 和 `tls_key_file`，即客户端证书（及其证书链）和私钥，fit 在
  服务器要求时出示（双向 TLS），代理的请求和健康检查的探测都会用到；与 I18 的 `tls_ca_file`（自定义 CA）和 `tls_server_name`
  （SNI 中发送并用于校验的名字）并存。配置拒绝只有其中一个，或不是 `h2` 时设置它们；无法加载的文件或不匹配的私钥报错在
  `tls_cert_file` 上。
- 上游的 TLS 文件（CA、证书和私钥）像监听器的证书文件一样被监视（I35b）：变化时重新加载配置；它们的内容也参与判断重载能否沿用
  上游的连接池，所以新证书会得到新的连接池，未变的则保留连接。
- 测试（`upstream/tls_test.go`、`runtime`、`config`）：`TestClientCertificate`（后端要求由它自己的 CA 签发的客户端证书，后端证书是
  example.com 的，按地址访问并用 `tls_server_name`：带客户端证书时请求和探测都经 HTTP/2 到达，不带时失败）、
  `TestTLSCertFileErrors`、`TestSharedPools`（文件被监视；证书和私钥变化后换新连接池）以及配置检查。完整的竞态测试在基准主机上通过。
- 没有基准测试：TLS 配置每个连接池只构建一次。

与计划的差异：

- 没有跳过上游证书校验的设置，密码套件沿用 Go 对 TLS 1.2 及以上的默认值（I18 曾说明可能如此）；tcp 服务的上游仍不由 fit 加 TLS。

### I55 M3 退出测试 — 已完成

- M3 的三条退出标准（路线图）对应 `make m3-exit` 和 CI 中单独的任务“M3 exit tests”：
  - **中间件不在 worker 上做阻塞调用，且每个都有基准测试。** 新包 `test/workers` 对 fit 注册的每个中间件和 handler，在其后放上
    慢的东西：被包装的 handler 600 ms 后才从定时器应答，`forward_auth` 的认证服务和 `proxy` 的上游需要 600 ms，`basic_auth` 在它的
    池上校验 bcrypt。`TestNoModuleBlocks` 检查 ServeHTTP 在 200 ms 内返回，而应答仍要等慢的部分给出（自己应答的除外：`redirect`、
    `respond`，以及文件已缓存后的 `static`）。`TestEveryModuleCovered` 在有模块注册却没有用例时失败，新中间件无法绕过这项检查；
    `forward_proxy` 交给 fib 的 `TestConnectProxyDoesNotWait`，因为 CONNECT 需要一个可切换的连接。`BenchmarkModule` 是每个模块的
    基准测试，其后没有慢的部分。各中间件自己包里的测试就是它的一致性测试。
  - **WebSocket、gRPC（一元与流式）和 TCP/UDP 代理，包括慢对端下的背压**：WebSocket 测试（回显、从 h2 的扩展 CONNECT、上游猛发而
    客户端不读）、gRPC 测试（一元、服务端流、客户端流和双向流，上游为 h2c 和 h2），以及 TCP 和 UDP 的测试（回显、慢客户端、慢上游）。
    gRPC 原先没有慢对端测试：新增的 `TestGRPCBackpressure` 让不读的客户端压住 32 MB 的服务端流（一秒内上游发出的消息不到一半），
    让不读的上游压住 32 MB 的客户端流，然后所有消息都到达。
  - **一个端口同时服务 HTTP/1.1、HTTP/2、按 SNI 透传的 TLS 和 SOCKS**：I53 的 `TestOnePortManyProtocols`，以及 `TestSNIRouting`、
    `TestSOCKS` 和 `TestSniffClear`。
- 它们在基准主机上、从 `1486c28` 的独立 checkout 运行，竞态检测下通过；fit 推送后由 CI 运行。
- 里程碑的基准，即带所有服务器的完整测试框架（规则 21）：[M3 报告](https://github.com/lesismal/fit/blob/3658ae9/bench/results/m3-report-2026-10-22.md)。

与计划的差异：

- 检查中间件不阻塞的方式是测它多快返回，而不是在 fib 里加钩子：在 worker 上等待，表现为 ServeHTTP 持续得与所等之物一样久。

### I56 Provider 框架与生效配置 — 已完成

- `provider`（新包）：`Provider` 是配置来源，每次变化时把它提供的全部内容作为片段发送：一个只含 `listeners`、
  `routes`、`upstreams` 和 `services` 的 JSON 对象。`Run` 让每个 provider 在自己的 goroutine 上运行，并在自己的
  goroutine 上一次一个地应用它们的片段；每个 provider 只有最新的片段等待，所以等待的内容以每个 provider 一个片段为界。
  `Parse` 读取片段（最大 16 MiB，元素须为对象，`name` 须为字符串），`Merge` 按决策 [009](decisions/009-provider-merging.zh-CN.md)
  的规则把片段合并进基础配置：基础配置优先于所有 provider，先添加的 provider 优先于后添加的；片段的元素放在基础配置的
  元素之后，基础配置的下标不变；名字已被前面来源占用的有名字元素被略去并记为被遮蔽；其他设置只来自基础配置。合并后的
  配置是基础配置原文按原样插入片段元素，所以 `Merged.Errors` 能把每个错误映射回去：映射到基础配置中，给出那里的行列；
  或映射到 provider 的片段中，给出那里的路径和位置，并注明 `(provided by <name>)`。`Merged.Sources` 记录每个顶层成员
  和数组元素的来源。
- `admin`：`Configs` 保存基础配置（`GET /config`、其 pointer、ETag 和回滚针对的那份）和每个 provider 最后应用的片段。
  `AddProvider` 添加 provider（基础配置来源的名字不可用）；`Provide` 把 provider 的片段合并进运行中的基础配置，作为
  来源为该 provider 的新版本应用，任何失败都保留运行中的配置和该 provider 之前的片段，与上一个相同的片段直接跳过。
  每次应用（文件、SIGHUP、Admin API、回滚、证书）都会合并当前的片段，所以通过 API 加载配置会保留 provider 提供的内容，
  回滚恢复基础配置时也带上它们。`GET /config/effective` 返回实际运行的配置、版本号和基础配置的 ETag、按优先级排列的
  来源、每个元素的来源，以及被遮蔽的内容。
- `fit run` 还没有接入任何 provider：除文件外还没有 provider，而文件仍是基础配置的来源（I57 加入 Docker，以及配置中的
  provider 列表）。
- 测试：`provider` 的 `TestMerge`（两个 provider 加基础配置：顺序、来源、被基础配置和被先添加的 provider 遮蔽、基础
  配置原文保留）、`TestMergeIntoEmpty`（基础配置缺少或为空的数组）、`TestParseRefused`、`TestErrors`（基础配置中和片段
  中的错误各自给出正确的行列）、`TestPosition`、`TestRun`（一次一个应用、等待的片段被替换、错误被报告、取消后返回）；
  `admin` 的 `TestProviders`（两个 provider 的路由被服务、名字被遮蔽、坏片段被拒绝并给出片段中的路径、相同片段不重复
  应用、加载和回滚保留 provider 的片段、破坏 provider 路由的基础配置被拒绝、空片段撤走路由、重载计数）和
  `TestFileAndProviders`（文件 provider 和由 `provider.Run` 运行的假 provider 一起工作，文件的修改和 provider 的片段
  被合并，生效配置的来源）。它们以及 `provider/file`、`cmd/fit` 和 `test/e2e` 在基准主机上通过竞态检测（在那里的
  Docker 中用 `golang:1.27`，基于 HEAD 加本次改动）；vet（包括 Windows）和 staticcheck 通过。
- 无基准：只涉及控制面（规则 21）。

与计划的不同：

- provider 的名字不加命名空间（Traefik 的 `name@provider`）：对它们的引用位于模块参数中，而 fit 不解读模块参数；
  改为由先占用名字的来源遮蔽后来者（决策 009）。

### I57 Docker provider — 已完成

- `config`：`providers`，按优先级排列的列表，每项是一种 provider，可带名字（默认为其种类；小写、唯一、不能用基础
  配置来源的名字）。`docker` 有 `endpoint`（`unix:///var/run/docker.sock`，或面向 socket 代理的 `tcp://host:port`）、
  `expose_all`（默认关：只取带 `<prefix>.enable=true` 标签的容器）、`network`、`use_published_ports`（供 Docker 之外、
  访问不到容器地址的 fit 使用）、`label_prefix`（`fit`）、`max_containers`（1000）、`refresh_interval`（30s）和
  `request_timeout`（10s）。启动时读取；修改它们会像 `admin`、`acme` 一样给出需要重启的警告。
- `provider/docker`（新包）：用普通 `net/http` 和不带版本的路径访问 Engine API（决策
  [010](decisions/010-docker-api-over-http.zh-CN.md)）：先打开只含容器的 `GET /events`，再 `GET /containers/json`
  （除非 `expose_all`，带标签过滤），事件后 200ms（略过健康检查产生的 exec 事件）和每个 `refresh_interval` 再列一次；
  定时列出时即使未变也发送片段，让因配置其他部分而被拒绝的片段得到重试。事件流断开或 daemon 出错会被报告，并每隔
  1s 到 30s 重试，其间保留上一个片段。有界：列表最大 16 MiB 并限时 `request_timeout`，一行事件 1 MiB，最多
  `max_containers` 个容器。标签：`fit.enable`、`fit.name`（上游名；默认是 Compose 的项目和服务 `<project>-<service>`，
  否则是容器名）、`fit.port`（否则取唯一的 TCP 端口）、`fit.network`、`fit.upstream`（上游其他设置的 JSON 对象）、
  路由 `fit.routes.<r>.{host,path,path_regexp,method,expr,priority,listeners,middleware,handler}`（名为 `<name>.<r>`，
  除非 `handler` 另有指定，否则代理到该上游），以及四层服务 `fit.services.<s>.{listeners,port,server_names,proxy_protocol}`。
  同名的容器（副本）共享一个上游；不健康或健康检查尚在启动的容器先不加入。标签有误的容器被略去，并报告一次原因；
  同样的容器得到字节相同的片段。
- `cmd/fit`：`fit run` 把配置中的 provider 按顺序加入 `admin.Configs`，用 `provider.Run` 运行它们，打印
  `fit: provider <name> watching Docker at <endpoint>`，每次应用片段打印 `fit: applied what provider <name> provides (config version N)`，
  被拒绝的片段只报告一次（错误指向 provider 写下它的地方），直到再次应用成功。它们和文件监视器一起在退出前停止。
  `-prefork` 下每个子进程各自运行。
- 测试：`config`（没有种类的 provider、两个同名、用了基础配置来源的名字、无效名字、endpoint、标签前缀）；
  `provider/docker` 的 `TestTranslate`（Compose 副本合为一个上游，指定端口、handler、中间件和独立端口四层服务的
  具名容器，未启用、启用为 false、不健康或健康检查启动中的容器被略去，九种错误标签被报告；顺序不影响片段）、
  `TestTranslateOptions`（换前缀的 `expose_all`、host 网络、两个网络、`network`、`use_published_ports`、
  `max_containers`）、基于 Unix socket 假 daemon 的 `TestProvider`（启动和停止事件、exec 事件不触发列出、三个事件
  只列一次、事件流断开后恢复、daemon 返回 500 后恢复）、`TestProviderRefresh`、`TestProviderNoDaemon`、
  `TestReadEvents`；`test/e2e` 的 `TestDockerProvider` 用真实 daemon 运行带该 provider 的 `fit run`：一个副本被服务，
  第二个加入上游，停掉第一个后它被移除，最后一个停掉后路由消失，`/config/effective` 显示来源。它们以及 `config`、
  `schema`（已重新生成）、`provider/...`、`admin`、`cmd/fit` 和整个 `test/e2e` 在基准主机上通过竞态检测（基于 HEAD
  加本次改动的 worktree）；linux、darwin、windows 的 vet 和 staticcheck 通过。
- 无基准：只涉及控制面（规则 21）。

与计划的不同：

- e2e 测试在装有 `docker compose` 的地方（如 CI 的 runner）使用它，否则用 `docker run` 加上 Compose 设置的标签——
  provider 看到的 Compose 也就是这些。基准主机没有 Compose（在那里安装被拒绝），所以在那里用 `docker run` 运行；
  CI 用 Compose 运行。没有 Docker daemon、不在 Linux 上（macOS 主机访问不到容器地址）或 `FIT_E2E_DOCKER=0` 时跳过。

### I58 Kubernetes Gateway API I：HTTPRoute — 已完成

- `config`：种类为 `kubernetes` 的 provider，含 `kubeconfig` 和 `context`（否则在集群中用 pod 的 service account，或
  `$KUBECONFIG`，或 `~/.kube/config`）、`controller_name`（`lesismal.github.io/fit`）、`namespaces`（全部）、
  `listen_host`（所有地址）、`backends`（`endpoints` 或 `cluster_ip`）、`max_objects`（每种 10000）和
  `request_timeout`（10s）。
- `provider/kubernetes`（新包），基于 `net/http` 的自有客户端（决策 [011](decisions/011-kubernetes-client.zh-CN.md)）：
  凭据来自 service account（token 轮换时重新读取）或 kubeconfig（CA、客户端证书和私钥，文件或 data 均可，token 或
  token 文件，`insecure-skip-tls-verify`；exec 和 auth-provider 插件被拒绝并给出说明），用唯一的新依赖
  `go.yaml.in/yaml/v3` 读取。每种资源一个 informer（Namespace、GatewayClass、Gateway、HTTPRoute、ReferenceGrant、
  Service、EndpointSlice）：每页 500 个分页列出，再从列表的 resourceVersion 开始带 bookmark 的 watch，每次五分钟；
  410 Gone 后重新列出，list 失败后每隔 1s 到 30s 重试，未提供的资源（404，没有 CRD）视为空并每 30s 再看，最多保存
  `max_objects` 个。所有资源都列出之前不发送任何内容；之后在变化后 200ms 发送片段，并每 30s 即使未变也发送，让被拒绝的
  片段得到重试。
- 转换：fit 的 GatewayClass 下 Gateway 的每个 HTTP listener 成为 fit 在该端口上的 listener（`<provider>-<port>`，
  `http/1.1` 和 `h2c`，同端口的 Gateway 共享）。HTTPRoute 按 parentRefs（section 和端口）挂到那些 `allowedRoutes`
  接受它（`Same`、`All`、按 Namespace 标签的 `Selector`、kinds）且主机名与它相交的 listener 上；为相同主机服务它的
  listener 共享路由。每条规则的每个 match 成为 fit 的一条路由：路径 `Exact`、`PathPrefix`（写成 `/p` 和 `/p/*`）和
  `RegularExpression`（加锚点），精确的请求头和查询参数，方法；优先级编码了 Gateway API 的先后次序（主机，然后精确
  路径、最长前缀、方法、请求头、查询参数），其余按创建顺序写出。过滤器：`RequestHeaderModifier` 和
  `ResponseHeaderModifier` 对应 `headers`，`URLRewrite` 对应 `rewrite` 和代理的 `host_header`，`RequestRedirect`
  对应 `redirect`，`RequestMirror` 对应代理的 `mirror`；其他过滤器返回 500。后端：Service 的端口成为上游
  `<ns>.<service>.<port>`，地址为其就绪的 endpoint（或 cluster IP），`appProtocol: kubernetes.io/h2c` 时用 `h2c`，
  ExternalName 用其名字；按权重拆分代理；其他命名空间的后端需要 ReferenceGrant；没有有效后端的规则返回 500，后端
  没有就绪 endpoint 的返回 503。被略去的内容报告一次并说明原因。
- `cmd/fit` 像运行 Docker provider 一样运行它，打印 `fit: provider <name> watching Kubernetes for GatewayClasses of <controller>`。
- 测试：`config`（一个 provider 两种种类、`listen_host`、`controller_name`、`backends`）；`provider/kubernetes` 的
  `TestTranslateGateway`（一条路由挂在两个 listener 上服务不同主机、其他命名空间的路由按 section 挂载、被 `Same` 和
  选择器拒绝的路由、忽略其他控制器的路由、加权后端、请求头过滤器、带主机名的重写、缺少 grant、没有就绪 endpoint 的
  Service、没有后端、重定向、不支持的 match 和过滤器；对象顺序不影响片段；grant 放行跨命名空间后端）、
  `TestTranslateOptions`（`cluster_ip`、`listen_host`、`namespaces`、`controller_name`）、`TestPrecedence`、
  `TestIntersect`、`TestSelects`、`TestFilters`、`TestMirror`；用 kubeconfig 经 TLS 访问假 API server 的
  `TestProvider`（所有资源列出前不发送、返回 404 的资源、两页、来自 watch 的路由和 endpoint、410 触发重新列出、结束的
  watch 被重建、删除）、`TestProviderUnauthorized`、`TestKubeconfig`（相对 kubeconfig 的文件路径、token 文件、
  context、insecure、拒绝插件、错误证书、`$KUBECONFIG`、service account）、`TestTokenRotates`。`test/e2e` 的
  `TestKubernetesGateway` 针对 kind 集群运行带该 provider 的 `fit run`：Gateway 和指向 Deployment 的 HTTPRoute 被
  服务，第二个副本加入，加上响应头过滤器，删除路由；CI 新增的 "Kubernetes e2e (kind)" 任务创建集群、安装 Gateway API
  v1.3.0 的 CRD，并经节点路由 pod 网络。`config`、`schema`（已重新生成）、`provider/...`、`admin`、`cmd/fit` 和
  `test/e2e` 在基准主机上通过竞态检测（基于 HEAD 加本次改动的 worktree）；linux、darwin、windows 的 vet 和
  staticcheck 通过。
- 无基准：只涉及控制面（规则 21）。

与计划的不同：

- kind e2e 测试只在 CI 中运行：基准主机没有 kind 和 kubectl，在那里会跳过。它还没有运行过，第一次推送时运行。
- 还不支持 HTTPS listener（来自 Secret 的证书）：fit 的 TLS 读取证书文件，所以需要把 Secret 写成文件，或在配置中
  直接给出证书。它与其他 listener 协议一起移到 I59。
- 按正则匹配请求头和查询参数、后端上的过滤器，以及不带主机名的重定向端口会被报告并略去。通配主机在 fit 的路由中
  匹配一级标签，在 Gateway API 中匹配任意级；I61 的一致性测试会说明其中哪些需要补上。

### I59 Kubernetes Gateway API II：其他路由与状态 — 已完成

- `config`：Kubernetes provider 的 `certificates_dir`，HTTPS listener 的证书写到这里（未设置时用 fit 自己在临时目录下
  建的目录，fit 停止时删除）。它的文档写明了新的路由种类，以及 fit 的账号需要的权限：list 和 watch 类型为
  `kubernetes.io/tls` 的 Secret，以及 patch GatewayClass、Gateway 和路由的状态。
- `provider/kubernetes`：
  - 新增 watch 的资源，共十二个 informer：GRPCRoute（v1）、TLSRoute（v1，否则 v1alpha3，否则 v1alpha2：informer
    列出服务器提供的第一个版本，并在那里 watch 和写状态）、TCPRoute 和 UDPRoute（v1alpha2）、ReferenceGrant
    （v1beta1，否则 v1），以及只限类型为 `kubernetes.io/tls` 的 Secret（用 field selector）。
  - 每个端口按协议族建 listener：HTTP（`http/1.1`、`h2c`）；HTTPS（`http/1.1`、`h2`，带 `tls.certificates`）与 TLS
    透传（`tcp`，按 server name 选择 service）共用 fit 的一个 listener，按 ClientHello 中的名字区分；TCP（`tcp`）；
    UDP（`udp`，名为 `<provider>-<port>-udp`，可与同端口的 TCP 族并存）。端口已被另一协议族占用（按 Gateway 从旧到新）
    的 listener 标为 `Conflicted`（`ProtocolConflict`）且不服务；未知协议、超出范围的端口、要终止 TLS 的 TLS
    listener 和透传的 HTTPS listener 被拒绝。tcp 或 udp listener 只在有 service 接收它时才写出，这是 fit 的配置要求的。
  - HTTPS 证书：每个 `certificateRef` 指向一个 Secret（其他命名空间的需要允许 Gateway 引用的 ReferenceGrant），用
    `tls.X509KeyPair` 检查，在发送片段之前写成一个包含证书链和私钥的 PEM 文件（0600，目录 0700），名为
    `<ns>.<name>.<SHA-256 的 12 位十六进制>.pem`，因此续期后的 Secret 是新文件和新片段；片段发出后删除不再被引用的
    这类文件。所有 ref 都不可用的 listener 不服务（`ResolvedRefs`：`InvalidCertificateRef` 或 `RefNotPermitted`）。
  - GRPCRoute：与 HTTPRoute 一样，每条规则的每个 match 成为一条路由：方法 `Exact`（service 加 method 写成
    `/svc/method`，只有 service 写成 `/svc/*`，只有 method 用路径正则）和 `RegularExpression`（加锚点的路径正则），
    精确的请求头，优先级按 API 对 GRPCRoute 的先后次序（主机、service、method、请求头）；后端走 h2c，上游名为
    `<ns>.<svc>.<port>.h2c`，端口的 `appProtocol` 已是 h2c 时不加后缀；支持请求头修改和镜像。
  - TLSRoute、TCPRoute 和 UDPRoute：每条路由一个 service，名为 `<ns>.<name>.<tls|tcp|udp>`，放在它挂载的 listener
    上，TLSRoute 按它与 listener 主机名相交得到的 server name，发往第一条规则的后端（更多规则会被报告并略去）：一个
    Service 端口的上游，或由多个 Service 的 endpoint 组成的一个上游，每个后端的权重在它的 endpoint 之间平分；UDP
    端口的上游以 `.udp` 结尾。listener 的连接以及每个 server name 归最早的路由；后来的被拒绝。
  - 五种路由共用一个挂载步骤：每个 listener 接受的种类（HTTP 和 HTTPS：HTTPRoute 和 GRPCRoute；TLS、TCP、UDP：
    各自的路由），由 `allowedRoutes.kinds` 收窄，协议不接受的种类记为 `InvalidRouteKinds`。
  - 状态（`status.go`）：每次发出片段后，把与对象现有状态不同的状态以 JSON merge patch 写到 status 子资源，每个
    patch 写明它基于的 resourceVersion，基于旧对象的 patch 会被拒绝（409），变化会经 watch 到来并触发下一轮。
    fit 的 GatewayClass 为 `Accepted`；Gateway 有 `Accepted`（有 listener 无效时原因为 `ListenersNotValid`）和
    `Programmed`；每个 listener 有 `supportedKinds`、`attachedRoutes`、`Accepted`、`Programmed`、`ResolvedRefs`
    和 `Conflicted`；每条路由在它指向的每个 fit 的 Gateway 上有 `Accepted` 或原因（`NoMatchingParent`、
    `NotAllowedByListeners`、`NoMatchingListenerHostname`）、`ResolvedRefs` 或原因（`BackendNotFound`、
    `RefNotPermitted`、`InvalidKind`），有部分被略去时还有 `PartiallyInvalid`（`UnsupportedValue`）；保留其他控制器
    的条目，路由不再挂在 fit 的 Gateway 上时删除 fit 的条目。状态不变时条件保留其 `lastTransitionTime`；
    `observedGeneration` 是对象的 generation。写入在单独的 goroutine 上进行，总是写最新的一组；某种资源写入失败时
    只报告一次，直到成功。
- 测试：`provider/kubernetes` 的 `TestHTTPS`（来自 Secret 的证书、跨命名空间的 grant、不匹配的私钥、缺失的 Secret、
  不是 Secret 的 ref、同一端口上的 HTTPS 与 TLS 透传、协议冲突、无处写证书）、`TestGRPCRoute`（各种 method 匹配、
  请求头、先后次序、h2c 上游、只接受 HTTPRoute 的 listener）、`TestL4Routes`（TCP、UDP 和 TLS 在各自的 listener 上，
  同一端口上的 TCP 和 UDP，两个 Service 间的权重，被拒绝的第二条路由，指向 TCP 端口的 UDPRoute）、`TestStatus`
  （GatewayClass、Gateway 及其 listener，路由被拒绝或后端无法解析的各种原因，保留其他控制器的条目，删除过时的条目，
  写过一次后不再写，状态转换的时间）、`TestObjectPath`，以及针对假 API server 的 `TestProviderStatus`（假 API server
  现在会按 resourceVersion 应用状态 patch：状态写一次后在 resync 中保持不变，被拒绝的写入会重做，v1alpha2 的
  TLSRoute，证书文件写出并随 Secret 删除）。转换测试产生的每个片段都用 `config.Load` 加载。`test/e2e` 的
  `TestKubernetesGateway` 还用 kubectl 等待 GatewayClass、Gateway 和路由的状态，服务带 Secret 证书的 HTTPS listener，
  并在装有 CRD 时服务一条 TCPRoute；CI 的 kind 任务改装 Gateway API v1.3.0 的 experimental channel。`make
  test-changed` 选出的 27 个包（配置改动波及它们）在基准主机上通过竞态检测；linux、darwin、windows 的 vet 和
  staticcheck 通过。
- 无基准：只涉及控制面（规则 21）。

与计划的不同：

- `Programmed` 表示 fit 收到了这个 listener，而不是包含它的配置已被应用：provider 收不到合并的结果。运行中的配置
  拒绝某个片段时，错误在合并处报告，而状态仍为 `Programmed`。
- 终止 TLS 的 TLS listener（`tls.mode: Terminate`，用于 TLSRoute）被拒绝：fit 的 tcp service 只透传 TLS。TCPRoute
  和 TLSRoute 只服务第一条规则。不读也不写 Gateway 的地址；I61 的一致性测试会说明其中哪些需要补上。
- 同一端口上主机名相同的 HTTPS listener 不报告为冲突：它们的证书并列提供，按 SNI 选择。
- 与 I58 一样，e2e 新增的部分只在 CI 中运行：基准主机没有 kind 和 kubectl。

### I60 Kubernetes Ingress — 已完成

- `config`：Kubernetes provider 的 `ingress`，用于开启 Ingress，含 `http_port`（80）和 `https_port`（443），检查它们
  是端口且不相同。未设置时关闭，因为开启后 fit 的账号需要 list 和 watch Ingress 与 IngressClass，而无权 list 的资源
  会挡住所有片段。
- `provider/kubernetes`（`ingress.go`）：
  - 只在设置 `ingress` 时多两个 informer：Ingress 和 IngressClass（networking.k8s.io/v1）。
  - fit 的 Ingress：`ingressClassName`（否则 `kubernetes.io/ingress.class` 注解）指向 `controller` 为
    `controller_name` 的 IngressClass 的，以及在这样的类带有 `ingressclass.kubernetes.io/is-default-class: "true"`
    时未指定类的；限于监视的命名空间，按从旧到新。
  - 每条规则的每个 path 成为 fit 的一条路由 `<ns>.<name>.ing.r<i>.p<j>`，放在 `http_port` 的 listener 上；对 Ingress
    的 `tls` 覆盖的主机（按名字或通配，或没有 hosts 的条目覆盖所有主机）还放在 `https_port` 的 listener 上，其 Secret
    的证书按 Gateway 的方式写出（I59）。路径 `Exact`，以及按路径段匹配的 `Prefix`（`ImplementationSpecific` 被 fit
    当作 `Prefix`），写成 `/p` 和 `/p/*`；优先级按 Ingress 的先后次序（精确主机，然后通配；精确路径，然后最长前缀）。
    默认后端是匹配一切、优先级为 0 的路由。后端是按端口号或端口名指定的 Service 端口，其上游与 Gateway 的路由共用；
    Service 或端口不存在、或后端是 resource 时返回 503。listener 与 Gateway 产生的相同，所以 Ingress 端口与同协议族
    的 Gateway listener 共用 fit 的一个 listener，与其他协议族冲突时报告，规则不在那里服务。
  - `fit.lesismal.github.io/` 下的注解：`ssl-redirect`（TLS 主机的 HTTP 请求以 308 重定向到
    `https://<host>[:https_port]{uri}`；通配主机或任意主机只在 80 到 443 时重定向，否则报告并经 HTTP 服务）、
    `rewrite-target`（替换匹配到的前缀，`Exact` 时替换整个路径）、`backend-protocol`（`HTTP`、`H2C`、`GRPC`，后两者
    走 h2c）、`limit-rps`（按客户端地址的 `rate_limit`）、`proxy-body-size`（`body_limit`，字节数或带 k、m、g）、
    `allow-source-range`（路由匹配这些地址，旁边一条路由对其他地址返回 403；无法解析的范围让该 Ingress 的所有路由
    返回 403，而不是放开）、`request-timeout`（代理的 `timeout`）。该前缀下的其他注解以及无法解析的值会被报告并略去。
- 测试：`config`（端口超出范围、两者同一端口）；`provider/kubernetes` 的 `TestIngress`（按名字、注解和默认选择类，
  略去其他控制器的，TLS 和重定向，限流和超时，按名字的端口，`Exact` 先于 `Prefix`，带来源范围的通配主机，重写和
  h2c，默认后端，缺失的 Secret，错误的注解，返回 503 的后端，无法解析的范围，没有默认类，没有 `ingress`）、
  `TestIngressPorts`（重定向中的其他端口、不重定向的通配主机、HTTPS 端口被 Gateway 的 TCP listener 占用）、
  `TestParseSize`、针对假 API server 的 `TestProviderIngress`。`test/e2e` 的 `TestKubernetesGateway` 还在自己的端口上
  服务一个属于它自己 IngressClass 的 Ingress。`make test-changed` 选出的 27 个包在基准主机上通过竞态检测；linux、
  darwin、windows 的 vet 和 staticcheck 通过。
- 无基准：只涉及控制面（规则 21）。

与计划的不同：

- Ingress 没有状态：fit 不知道别人从哪个地址访问它（Service、节点或前面的负载均衡器），而 Ingress 的
  `status.loadBalancer` 正是这个地址。
- 注解是 fit 自己前缀下的，不是 ingress-nginx 的：两者含义和默认值不同（ingress-nginx 默认重定向 TLS 主机），读取它的
  名字等于承诺 fit 做不到的事。从它迁移的用户改一下保留的几个注解名即可。
- 与 I58 一样，e2e 新增的部分只在 CI 中运行。

### I61 Gateway API 一致性测试 — 已完成

- 测试套件：`test/conformance`，单独的 Go 模块（决策 [012](decisions/012-gateway-conformance.zh-CN.md)），依赖
  `sigs.k8s.io/gateway-api` v1.3.0，其测试运行 Gateway API 的一致性测试套件；`test/conformance/run.sh` 创建 fit 的
  GatewayClass，启动带 Kubernetes provider 的 `fit run`（Gateway 在 127.0.0.1 上，并写进它们的状态），以
  `-conformance-profiles GATEWAY-HTTP` 运行测试套件并写出报告。fit 声明该 profile 的 core：它的 33 个测试，即
  Gateway、HTTPRoute 和 ReferenceGrant。
- `config`：Kubernetes provider 的 `addresses`，即写进每个 Gateway 的 `status.addresses` 的 IP 地址或主机名
  （`IPAddress` 或 `Hostname`），客户端和测试套件从这里读取 Gateway 的访问地址；未设置时保留原有的地址。
- `router` 和 `config`：路由的 host 可以是 `**.` 通配，如 `**.example.com`，表示一级或多级标签，与 Gateway API 的
  `*.example.com` 相同（fit 的 `*.` 仍表示一级）。router 把这类 host 按后缀放进单独的 map，具体程度低于 `*.`、高于
  无 host，且只在存在这类 host 时才查找请求 host 的后缀，最长的优先，最多四个；默认 HTTPS 不为它申请证书。
- `provider/kubernetes`：HTTPRoute 和 GRPCRoute 的主机名中 Gateway API 的通配写成 `**.`（Ingress 仍用 `*.`，在
  Ingress 中它表示一级）。已被接受但未服务的 listener（例如证书缺失）仍接受路由并计入 `attachedRoutes`，它接受的路由
  为 Accepted，并为状态解析其后端，只是不产生 fit 的路由。
- 第一次在基准主机上运行时 33 个测试通过 30 个：`HTTPRouteHostnameIntersection` 和
  `HTTPRouteListenerHostnameMatching` 把 `foo.bar.anotherwildcard.io` 发给只匹配一级的 `*.` 通配，
  `GatewayWithAttachedRoutes` 要求 Secret 缺失的 listener 也有 `attachedRoutes` 和 `ResolvedRefs`；以上改动修复了
  它们，第二次运行 33 个全部通过。
- CI：单独的任务 "Gateway API conformance (kind)"：带 experimental CRD 的 kind 集群，用 `CAP_NET_BIND_SERVICE`
  构建的 fit 以绑定 80 和 443 端口，运行 `run.sh`，并把报告和 fit 的日志保留为 artifact。
- 测试：`router` 的 `TestDeepWildcard`（一级和多级、大小写、裸域名、具体程度低于 `*.` 高于无 host、路径匹配时最长
  后缀优先）；`config`（`**.` 路由主机、拒绝单独的 `**.`、默认不为 `**.` 申请证书）；`provider/kubernetes`（通配写成
  `**.`、证书缺失的 listener 上的路由被挂载、接受并报告缺失的后端）。`make test-changed` 选出的 28 个包在基准主机上
  通过竞态检测；linux、darwin、windows 的 vet 和 staticcheck 通过，包括一致性测试模块。`scripts/test-changed.sh`
  现在略去嵌套模块：一致性测试模块的 `go.mod` 曾让它测试全部包。
- 基准（规则 21，router 在热路径上）：`bench/microbench.sh` 比较 `main` 与本次改动的 `BenchmarkMatch`，起初按级别
  循环加上新分支慢了 3.7%（56.8 到 58.9 ns）；把各级展开、没有 `**.` 主机匹配时深层一步是空循环后，快了 1.0%（56.8 到
  56.2 ns，p=0.002），且不分配内存。`bench/ab.sh`（fixed、static 和 proxy，HTTP/1.1 和 h2c，3 轮各 10s，期间停掉
  kind 节点）：每一组都在噪声范围内，-0.5% 到 +0.5%。
- 基准主机现在在 `/root/dev/bin` 中有 kind v0.30.0（用它的 Go 构建）和 kubectl v1.34.0（发布的二进制，已校验
  checksum），以及一个 kind 集群 `fitconf`，其 pod 网络经节点路由，用于运行一致性测试。

与计划的不同：

- 只声明了该 profile 的 core：其扩展特性（按路径和端口重定向、重写、镜像、按方法和查询参数匹配等）留给以后的运行，
  在其测试通过后在 `run.sh` 中用 flag 逐个声明。
- 所有 Gateway 都在同一地址上访问：fit 把共享端口的 Gateway 合并成一个 listener，所以请求能到达该端口上所有 Gateway
  的路由。core 测试不区分它们；扩展测试 `GatewayHTTPListenerIsolation` 会区分。

### I62 Consul 与 etcd provider — 已完成

- `config`：三种 provider。`consul`（`ConsulProvider`）：`endpoint`（http 或 https，默认本地 agent）、`datacenter`、
  `token_file`、`ca_file`、`cert_file` 与 `key_file`、`expose_all`、`tag_prefix`（`fit`）、`max_services`（1000）、
  `refresh_interval`（30s）和 `request_timeout`（10s）。`consul_kv`（`ConsulKVProvider`）和 `etcd`（`EtcdProvider`，
  用 `endpoints`、`username` 和 `password_file` 代替 token 和 datacenter）取 `prefix`（`fit/`）和 `max_keys`（1000）。
  端点须为 http 或 https 加主机和端口，不带路径；证书需要私钥，用户需要密码文件。
- `provider/labels`：Docker provider 读取标签的部分被移出，使 Consul 的 tag 以同样方式读取：前缀下的路由、服务和上游
  设置，以及一组分组生成的片段。`provider/docker` 改用它，提供的内容不变。
- `provider/internal/watch`：三者共用的循环 `Follower`。它在 `Source` 上保持一个 watch，watch 就位后整体读取来源
  （两者之间的变化不会漏掉），变化后（去抖 200ms）和每个 `refresh_interval` 再读，片段变化或到了间隔时发送，每个
  问题在持续期间只报告一次，来源失败时每隔 1s 到 30s 重试；`Run` 在 watch 结束后才返回。`Client`：带 CA 和客户端
  证书的 HTTP 客户端，每次 watch 新建，以便读到新文件。
- `provider/internal/kvpart`：前缀下的每个键保存一个片段，即 `provider.Parse` 接受的含 listeners、routes、upstreams
  和 services 的 JSON 对象；provider 的片段是它们按键顺序的合并。以 '/' 结尾的键和空值被跳过；值不是片段的键，或超出
  `max_keys` 的键，被排除并报告。
- `provider/consul`：`Catalog` 用阻塞查询轮询 `/v1/catalog/services` 和 `/v1/health/state/any`，任一变化时列出服务，
  取带 `<prefix>.enable=true` tag 的服务（`expose_all` 时取全部），并从 `/v1/health/service/<name>?passing=1` 读取每个
  服务通过检查的实例。每个服务是一个上游，地址取实例的地址（否则取其节点的），端口取 `<prefix>.port` 或注册的端口；
  形如 `<prefix>.<key>=<value>` 的 tag 与容器的标签一样读取，`<prefix>.name` 为上游命名。`KV` 轮询
  `/v1/kv/<prefix>?recurse`（404 即没有键）。阻塞查询遵循 Consul 的指引（index 回退时重新开始，小于 1 时取 1，同一
  轮询的查询至少相隔 1s）；token 放在 `X-Consul-Token`，datacenter 放在 `dc`。
- `provider/etcd`：经 JSON 网关访问 etcd 的 v3 API。对前缀范围的 watch（`POST /v3/watch`，每行一个 JSON 对象）须在
  `request_timeout` 内说明已创建；然后 `POST /v3/kv/range` 读取键，最多 `max_keys` 个（超出的报告）。事件触发（去抖后）
  重新读取，进度通知不触发；watch 被取消（例如压缩之后）或流结束时，在下一个端点上重新开始。设置用户时，
  `POST /v3/auth/authenticate` 为 watch 的请求取得 token，密码文件每次都重新读取。
- `cmd/fit`：`fit run` 启动这三种 provider，并说明各自监视什么。
- 决策 [013](decisions/013-consul-and-etcd-over-http.zh-CN.md)：用 net/http，不用客户端库，不增加依赖。
- 测试：`provider/consul` 针对支持阻塞查询的假 Consul（`TestTranslate`：tag、名称、端口、取自节点的地址、服务自己的
  上游、所有问题；`TestCatalog`：实例注册、检查失败和注销，Consul 失败后恢复，每个请求都带 token 和 datacenter，未启用
  的服务从不查询；`TestCatalogMaxServices`；`TestKV`：键的增删、404、index 回退）；`provider/etcd` 针对假网关（经 watch
  跟进键的写入和删除、进度通知、压缩和流结束、不可达的端点、认证、`max_keys`、从未创建的 watch、错误密码、`rangeEnd`、
  `readWatch`）；`provider/internal/watch`（`TestFollower`、`TestFollowerInterval`）；`provider/internal/kvpart`；
  `config`（各种 provider 的设置）。`test/e2e` 的 `TestConsulProviders` 让 fit 针对 Docker 中 dev 模式的 Consul 1.21.5
  运行，刷新间隔保持 30s：服务实例的注册、一个实例 TTL 检查失败、最后一个注销，以及来自键的路由的写入和删除；
  `TestEtcdProvider` 针对开启认证的 etcd v3.6.5：键的写入、修改和删除。两者、`TestDockerProvider`（移到
  `provider/labels` 之后）以及涉及的包都在基准测试主机上通过竞态检测；linux、darwin 和 windows 的 vet 与 staticcheck
  通过。
- 第一次运行发现了假服务暴露出的竞态：`Follower` 返回时没有等待 watch 的 goroutine，轮询可能比 `Run` 活得更久。现在
  它会等待；测试的 `background` 辅助函数也会在测试清理前等待 `Run` 返回。
- 无基准测试：provider 属于控制面（规则 21）。

与计划的差别：

- KV 的值是 JSON 片段，一个键一个，而不是 Traefik 那样一个设置一个键的键树：与配置文件和 Admin API 形状相同，由同一个
  解析器检查，多个键也让多个写入方各自维护自己的片段。
- 不支持 Consul Enterprise 的命名空间和分区，也不支持 etcd 的 v2 API（决策 013）。

### I63 共享证书存储 — 已完成

- `config`：`acme.shared_storage`，`redis`（`address`、`username`、`password_file`、`db`、`tls`、`ca_file`、
  `cert_file`、`key_file`）、`etcd`（`endpoints`、`username`、`password_file` 和 TLS 文件）与 `consul`（`endpoint`、
  `datacenter`、`token_file` 和 TLS 文件）三者恰好设一个，另有 `prefix`（`fit/`）、`lock_ttl`（30s，至少 10s）和
  `request_timeout`（10s）；不能与 `storage` 同时设置。修改它的重载会被拒绝，与对 `acme` 的任何修改相同。
- `tlsmgr/sharedstore`：`Store`，基于各种后端的 `tlsmgr.Storage`，负责加前缀，拒绝 `FileStorage` 拒绝的键和超过
  4 MiB 的值，`List` 不列出 `<prefix>locks/` 下的锁，每把锁由一个 goroutine 每隔三分之一 TTL 续期直到 `Unlock`（同一
  进程的另一个 goroutine 与另一个进程一样等待）。Redis：最多四个连接上的 RESP2（带用户的 AUTH、SELECT、TLS），应答
  有界；GET、SET、DEL，以及对前缀中通配字符转义后的 SCAN；锁用带随机 token 的 `SET NX PX`，由校验 token 的脚本续期和
  删除。etcd：JSON 网关，`/v3/kv/range`、`put`、`deleterange`，用户的 token（被拒时重新获取一次），端点不可达时换下
  一个；锁用 lease 加针对键的 create revision 的事务，用 `/v3/lease/keepalive` 续期，撤销 lease 来释放。Consul：
  `/v1/kv` 上的 `?raw`、`?keys`、PUT 和 DELETE；锁由 session（`TTL`、`Behavior: delete`、无锁延迟）获取键，续期和
  销毁。
- `internal/apiclient`：I62 为 provider 写的带 TLS 文件的 HTTP 客户端和有界读取，从 `provider/internal/watch` 移出，
  供 `tlsmgr/sharedstore` 一起使用。
- `tlsmgr`：`newACME` 根据 `shared_storage` 构建存储（并在 `Close` 中关闭）；存储表明自己是 `Shared` 时，ACME 在质询
  进行期间把应答保存在其中：HTTP-01 的 key authorization 按 token，TLS-ALPN-01 的证书按名称，结束后删除。
  `LookupHTTPChallenge` 在 ACME 的 goroutine 上到存储中查找 token，同时最多 16 个，每个限时 5s；token 不是 base64url
  或所有槽位都被占用时立即应答。对列出的名称，若本实例没有其 TLS-ALPN-01 质询，握手时在 fib 的握手池上到存储中查找。
- `listener`：使用共享存储时，内存中没有应答的质询请求先 `Retain`，由 `LookupHTTPChallenge` 的回调应答：应答内容，
  或 404。
- 决策 [014](decisions/014-shared-certificate-storage.zh-CN.md)：不用客户端库的三种存储、租约锁、共享质询。
- 测试：`tlsmgr/sharedstore` 的 `TestStores` 针对 Docker 中的 Redis 8.2.2（带密码和数据库）、etcd v3.6.5（第一个
  端点不存在）和 Consul 1.21.5 并行运行同一套测试：键的存储、替换、由第二个存储读取、按前缀列出（不含锁，通配字符按
  字面处理）、删除、错误的键、值的上限、已取消的 context；两个存储上的八个持有者轮流取得锁，从不同时有两个；持有超过
  TTL 的锁会续期并被等待，释放后被取得；停止续期的持有者的锁在 TTL 后被取得（10s；Consul 为 20s）。`TestReadReply`
  读取 RESP 应答并拒绝错误和超大的应答。`tlsmgr` 的 `TestSharedPebble`：同一共享存储上的两个 ACME 针对 Pebble，
  Pebble 只能到达第二个；第一个的证书经存储中的应答完成质询后获得，第二个被要求管理同一名称时取用该证书而不另行获取；
  之后存储中不再有质询。`TestLookupHTTPChallenge`（无存储、错误的 token、16 的上限）和 `TestCertsSharedALPN`。
  `test/e2e` 的 `TestACMESharedStorage`：两个 `fit run` 进程依次使用 Redis、etcd 和 Consul，Pebble 只能到达第一个的
  HTTP-01 端口：第二个获得只有它管理的名称，两者都管理的名称只获取一次并由两者提供同一张证书。`config`（各种存储的
  设置、`storage` 与 `shared_storage` 同时设置）。`startFitWant` 用于 "address" 键不全属于 listener 的配置，直接给出
  listener 数量。涉及的包和 ACME e2e 测试在基准测试主机上通过竞态检测；该主机现在有与 CI 相同的 Pebble v2.10.1，位于
  `/root/dev/gopath/bin`；linux、darwin、windows 和 freebsd 的 vet 与 staticcheck 通过。
- 无基准测试：证书管理属于控制面（规则 21）；dispatcher 的改动只在使用共享存储时、`/.well-known/acme-challenge/`
  下的请求才会走到。

与计划的差别：

- 除证书外还共享质询：否则同一地址后的两个实例只有在 CA 恰好到达下单的那个时才能共享签发。
- 使用共享存储时，token 在哪里都找不到的质询路径得到 404，而不是交给路由（决策 014）。

### I64 仪表盘 — 已完成

- `admin`：`GET /status`，即 fit 当前在做而配置中没有的信息：运行中配置的版本、应用时间和来源、重载次数、绑定的
  listener 及其协议、打开和被拒绝的连接及待发送字节、每个上游进行中的请求及其后端（是否在轮转中，是否被健康检查移出；
  进行中、成功和失败的请求），以及每个 listener 提供的证书（名称、签发者、序列号、有效期），按上游排序，证书按
  listener 和到期时间排序。它读取的内容与 `WriteMetrics` 相同。
- `admin/dashboard`：仪表盘，用 `go:embed` 嵌入的 HTML、CSS 和纯 JavaScript，在 `/dashboard/` 提供（`/dashboard` 会
  重定向），Content-Security-Policy 只允许自身文件和源，另有 `nosniff`、禁止嵌入框架和 `no-cache`。六个标签页：概览
  （版本、重载、连接、路由、在线后端、14 天内到期的证书；listener；按优先级排列的来源，及各自提供了什么、什么被遮蔽）、
  路由（listener、匹配、中间件、handler 和上游、来源）、四层服务、上游及其后端的健康状态（可按名称或地址过滤）、带剩余
  天数的证书，以及配置版本（可查看每个版本的配置）。每 5s 刷新一次（可关闭），数据只作为文本写入，从不作为 HTML，API
  返回 401 时要求输入 token。`admin.disable_dashboard` 可关闭它。
- `admin` 的 guard：仪表盘的文件无需 token 即可获取，来自 API 自身源的 GET 或 HEAD 会被放行；决策 005 增加了 I64 的
  更新说明。
- 测试：`admin` 的 `TestStatus`（请求后一个在线、一个失效的后端，以及证书）、`TestDashboard`（重定向、安全头、页面
  引用的每个文件、404 和 405、允许来自自身源的读取并拒绝其他源、拒绝来自自身源的写入）、`TestDashboardToken`（文件
  无需 token，端点和 `/dashboard/` 下的写入需要 token）、`TestDashboardDisabled`，以及 `TestDashboardReadOnly`：读取
  嵌入的文件，出现 GET 以外的方法、提交的表单、其他源或 `innerHTML` 即失败；`test/e2e` 的 `TestDashboard` 针对
  `fit run`。页面在浏览器中经 ssh 隧道针对基准测试主机上的 `fit run` 目视检查过：一个在线后端和一个被健康检查移出的
  后端、一张证书和两个版本，浅色和深色模式下每个标签页都正常渲染，控制台没有错误，并修复了一处被包在表格单元格里的
  表头行。`admin` 和 `config` 在基准测试主机上通过竞态检测；linux、darwin、windows 和 freebsd 的 vet 与
  staticcheck 通过。
- 无基准测试：Admin API 属于控制面（规则 21）。

与计划的差别：

- 新增 `GET /status`：上游健康状态和证书原先只在 Prometheus 指标中，不应让页面去解析。

### I65 OpenTelemetry — 已完成

- `config`：`telemetry`，在 fit 启动时读取：`endpoint`（OTLP/HTTP，`http://127.0.0.1:4318`）、`headers_file`
  （`Name: value` 行，如 API key）、`ca_file`、`cert_file`、`key_file`、`service_name`（`fit`）、`timeout`（10s），
  以及 `traces`（`sample_ratio`，默认 0：只追踪调用方采样的；`max_queue_bytes`，4 MiB；`interval`，5s）和
  `metrics`（`interval`，30s）至少一个。`telemetry.metrics` 像 `metrics` 一样统计请求，无需 Admin API。修改
  `telemetry` 的重载会提示重启后生效。
- `observe`（决策 [015](decisions/015-opentelemetry-without-the-sdk.zh-CN.md)，不用 SDK）：`Tracer.Sample` 读取
  `traceparent`（W3C，版本 00 以及更高版本的前几个字段；无效的不跟随），遵循调用方的决定，没有调用方的请求按比例
  采样，随机数来自 `math/rand/v2`；比例为 0 时，未采样的父级甚至不解析。`TrackTraced` 用一次分配为采样的请求创建
  `Entry` 及其 trace；trace 和 JWT 的 claims 现在放在 entry 的同一个指针后面（`Trace()`、`Claims()`、`SetClaims`），
  使其他请求的 entry 保持 112 字节。`Tracer.End` 把 server span 写成 OTLP JSON（`<method> <route>`、HTTP 语义约定的
  属性和 fit 自己的、5xx 时为错误状态），放进八个分片之一，超过 `max_queue_bytes` 的丢弃并计数；一个 goroutine 每隔
  interval 或积累 256 KiB 时导出到 `/v1/traces`，把分片的缓冲区还回去供后续 span 使用，失败在持续期间只报告一次。
  `MetricWriter` 由 `TextWriter` 和新的 `OTLPWriter` 实现：计数器为累积单调 sum，gauge，耗时直方图为以秒为单位的
  显式分桶直方图；`MetricsExporter` 每隔 interval 发往 `/v1/metrics`，fit 停止时再发一次。
- `listener`：dispatcher 在有 tracer 时询问它，采样的请求在响应结束时记录 span，与访问日志的行和指标并列；没有 tracer
  时的闭包与以前相同。`handler/proxy`：`outgoing` 把被追踪请求的 `traceparent`（指向 fit 的 span）发给上游（WebSocket
  升级也是）；未追踪的原样转发。`admin`：`WriteMetricsTo(MetricWriter)`，以及 `fit_trace_spans_total`（exported、
  failed、dropped）。`cmd/fit`：tracer 在 graph 之后关闭，指标导出器在 configs 建好后启动，并为 resource 设置
  `observe.Version`。
- 测试：`observe`（`TestParseTraceparent`：有效、未采样、其他标志、更高版本以及十一种无效形式；`TestSample`：遵循
  调用方、比例 0、1 和 20000 个请求上的 0.5；`TestTracerExport`：针对接收方的两个 span 的 OTLP JSON、属性、转义、
  headers 文件、resource；`TestTracerBounds`：超过 `max_queue_bytes` 时丢弃、失败的接收方只报告一次及其恢复；
  `TestMetricsExporter`：计数器、gauge 和直方图的分桶），`listener` 的 `TestDispatchTrace`，`handler/proxy` 的
  `TestOutgoingTraceparent`，`config`（`TestParseHeaders`、telemetry 设置、超过 1 的比例）。`test/e2e` 的
  `TestTelemetry` 针对 Docker 中的 OpenTelemetry Collector 0.137.0，其 file exporter 写出收到的内容：调用方采样的
  请求以调用方 trace 中、指向 fit span 的 `traceparent` 到达上游，collector 收到该 span 且其父为调用方的 span；没有
  `traceparent` 的请求既不被追踪，也不会被加上；collector 收到 fit 的指标和 resource 的 `service.name`。涉及的包和
  e2e 测试在基准测试主机上通过竞态检测；linux、darwin、windows 和 freebsd 的 vet 与 staticcheck 通过。
- 基准测试（规则 21，dispatcher 和代理的 `outgoing` 在热路径上），在基准测试主机上进行，期间停止 kind 节点。
  `bench/ab.sh`（fixed 和 proxy，HTTP/1.1 和 h2c，3 轮各 10s，无 telemetry）：每组都在噪声范围内，-0.4% 到 +0.7%。
  `bench/microbench.sh` 的 `BenchmarkDispatch`，`main` 对比本次改动：bare 205.8 到 205.8 ns，log 256.7 到 260.9（不
  显著），log+metrics 258.4 到 260.2（不显著），metrics 219.1 到 222.1（+1.4%，p=0.017：tracer 的 nil 检查），分配
  次数和字节数不变；有 tracer 时，不采样为 226.6 ns，全部采样为 889 ns，每个请求 6 KiB，其中大部分是同一进程中导出器
  的。`BenchmarkSample`：没有 traceparent 4.5 ns，比例 0 时未采样的 8.9 ns，开始新 trace 26 ns，跟随调用方 54 ns，
  都不分配；`BenchmarkEnd`：写一个 847 字节的 span 314 ns，不分配。第一次运行发现 bare 慢 1.4%、entry 大 62 字节、
  `End` 1.2 µs、未采样父级 47 ns；上面的改动（只在有 tracer 时才处理 trace、trace 与 claims 放在一起、缓冲区复用、
  不解析未采样的父级）把它们降到了现在的数值。

与计划的差别：

- 不用 SDK，只支持 JSON 的 OTLP/HTTP（决策 015）。
- 每个请求一个 server span；对上游的调用没有单独的 client span。

### I66 打包与示例 — 已完成

- **容器镜像**（[Dockerfile](../Dockerfile)，决策 [016](decisions/016-container-image.zh-CN.md)）：fit 不用 cgo，带
  `-trimpath` 和 `-X main.version` 构建，在构建机的平台上交叉编译并使用 BuildKit 的缓存，基础镜像为
  `gcr.io/distroless/static-debian12:nonroot`，以 65532 运行；`/etc/fit/fit.json` 处有一个占位配置。`.dockerignore`
  只发送构建需要的内容。[release.yml](../.github/workflows/release.yml) 在打 `v<semver>` 标签时把 linux/amd64 和
  linux/arm64 的镜像发布到 `ghcr.io/lesismal/fit:<version>` 和 `:<major>.<minor>`；目前还没有打标签。
- **Helm chart**（`deploy/helm/fit`）：fit 的 Deployment，以镜像的用户运行，根文件系统只读，不带 capability，1024
  以下的端口由安全的 sysctl `net.ipv4.ip_unprivileged_port_start` 放开；ConfigMap 里是 fit 自己在 `healthPort`（9099）
  上用 `/healthz` 回答探针的 listener，以及 Kubernetes provider（在集群内，通过 service account），配置变化时滚动
  更新；Service 暴露 Gateway 的端口；ClusterRole 包含 provider list、watch 和 patch 的资源；集群有 CRD 时创建
  GatewayClass `fit`，以及 IngressClass `fit`。可配置镜像、副本数、两种 class、Ingress 端口、provider 的其他设置、
  完整配置、额外参数和常见的 Pod 设置。
- **systemd**（`deploy/systemd`）：`fit.service` 和 `fit@.service`（`/etc/fit/<name>.json`），以 sysusers 创建的
  用户 `fit` 运行，只带 `CAP_NET_BIND_SERVICE`，文件系统只读，只有 `/var/lib/fit` 和 `/run/fit` 可写；启动前和
  reload 时先 `fit validate`，reload 再发送 SIGHUP；停止时等 40s，fit 自己的关闭等待是 30s。
  `systemd-analyze security`：3.1，OK。
- **示例**：`examples/docker-compose`，fit 的镜像用 Docker provider 位于 `traefik/whoami` 的两个副本前面，按它们的
  label 路由；`examples/kubernetes`，在 kind 集群里安装 chart，用 Gateway 加 HTTPRoute 以及 Ingress 指向同一个应用。
  [deploy/README.zh-CN.md](../deploy/README.zh-CN.md) 和各示例的 README（两种语言）说明了做法。
- **测试**：每个示例的 `test.sh` 执行其 README 的步骤并检查响应：Compose 示例的 host 由两个副本回答，副本停止后
  返回 404，再启动后恢复（M4 的退出标准）；Kubernetes 示例用现场构建的镜像安装 chart，等 Gateway 变为 Programmed，
  再通过 Gateway 和 Ingress 分别得到两个 pod 的回答。`deploy/systemd/test.sh` 安装 unit，以 `fit` 用户服务 80 端口，
  reload 修改后的配置，拒绝无效配置且继续服务，并运行 `fit@second`。CI 在两个新 job `packaging` 和 `helm`（含
  `helm lint`）中运行它们。三者都在基准机上通过：示例使用放在基准机临时目录里的 Docker Compose v2.39.1、buildx
  v0.26.1 和 Helm v3.18.4，systemd 测试在一个用完即删的 systemd 容器里运行。

与计划的差别：

- Dockerfile 放在仓库根目录，`docker build .` 能直接找到，而不在 `deploy` 下。
- 还没有发布镜像：发布流程等待打标签，所以 chart 和示例在运行处构建镜像并载入。

### I67 缓存 I：HTTP 语义与内存层 — 已完成

- **`middleware/cache` 的 `cache`**，按 RFC 9111 对共享缓存的规则实现的 HTTP 缓存：保存 GET 的响应，
  条件是状态码可理解（200、203、300、301、308、404、405、410、414、501，以及带自身有效期的 302 和 307），
  不含 `no-store`、`private` 或 `no-cache`，不对一切请求变化（`Vary: *`），不设置 cookie（与 nginx 一样不保存），
  不带 trailer，且不超过 `max_entry_size`；带 `Authorization` 的请求，其响应只有在声明 `public`、`s-maxage`
  或 `must-revalidate` 时才保存。新鲜期取自 `s-maxage`、`max-age`，或 `Expires` 相对 `Date` 的差；否则对允许
  推断的状态码，取 `default_ttl`，或距 `Last-Modified` 时间的十分之一，最多一天；年龄由 `Date`、`Age` 和请求
  耗时算出（第 4.2.3 节）。已超过新鲜期的响应不保存。
- **应答。** 新鲜的响应以其 `Age` 回答 GET 和 HEAD；对 `If-None-Match`（弱比较或 `*`）或
  `If-Modified-Since` 满足的请求回答 304，带上 RFC 9110 列出的字段。请求的 `no-cache`（以及没有
  `Cache-Control` 时的 `Pragma: no-cache`）、`max-age` 和 `min-fresh` 会让它转发；`max-stale` 可以接受陈旧响应，
  除非该响应声明了 `must-revalidate`、`proxy-revalidate` 或 `s-maxage`；`only-if-cached` 在没有保存时得到 504。
  带 `Range` 的请求转发，其 200 响应会被保存。可能改变资源的请求（GET、HEAD、OPTIONS、TRACE 之外）成功后，
  删除其 URI 以及同主机的 `Location`、`Content-Location` 所保存的内容。对 GET 和 HEAD 的每个应答都用
  `Cache-Status`（RFC 9211）说明缓存的处理：`fit; hit; ttl=N`，或
  `fit; fwd=uri-miss|vary-miss|stale|request|bypass`，响应被保存时加上 `; stored`。
- **变体。** 响应的 `Vary` 指明随之保存的请求字段，比较其值时去掉列表成员两侧的空白；每个 key 最多保存
  8 个变体，最旧的先移除。key 是 scheme、主机和请求目标。
- **再验证。** 客户端自己的验证器匹配的陈旧响应，会被源站对它们回答的 304 刷新：按 304 更新字段
  （RFC 9111 第 3.2 节）并重新计算有效期。客户端没有发送验证器时，缓存尚不发送自己的验证器；这需要缓存自己
  发出的请求，由 I69 的锁和后台更新带来。
- **内存层**（`store`）：一个 zone 的响应分在 16 个分片中，每个分片有自己的锁、`max_size`（128 MiB）的
  十六分之一和最近最少使用顺序；每条计入响应体、字段、key 和 256 字节。`max_entry_size`（1 MiB，最多为
  `max_size` 的十六分之一）限制单个响应体。zone（`zone`，默认 `default`）由所有指定它的缓存共享，并在
  reload 后保留：新配置的缓存在旧配置的缓存归还之前就拿到它，无人使用时释放。
- **随发送保存。** 响应经过一个不改动内容的 fib 响应体过滤器时被复制，因此不会为保存而压住响应，流式响应
  照常流式，全部发送完毕后才保存：长度与声明一致，且没有因错误或 trailer（代理的 `Fit-Error`）而中断。
  进行中的副本合计不超过 `max_size` 的四分之一；超出时响应照常发送但不保存。客户端离开或写入失败的响应，
  通过 fib 新增的 `BodyAborter`（见下）归还其副本。
- **fib**（A40，由本会话在 fib 分支 `a40-filter-abort` 上实现，`3998953`）：`BodyAborter`，响应在响应体结束前
  就结束时，以 `Abort` 代替 `Close`；以及在已返回过滤器的钩子之后运行的钩子拿不到 `Content-Length`。没有前者，
  中途离开的客户端会永久占用上限中的一份；compress 也有同样的缺口，它在 `OnCancel` 中归还在途名额，而写入
  失败的响应写完后 `OnCancel` 不再运行，现在它在 `Abort` 中归还名额和编码器。没有后者，排在 compress 之后的
  缓存把未压缩的长度当作压缩后响应体的长度，于是什么也不保存。fib 的测试、竞态检测（fib 根包有四个 UDP 和
  套接字缓冲区测试在基准机上失败，`main` 与该分支相同）、linux、darwin、windows、freebsd 的 vet，以及 linux、
  darwin、windows 的 staticcheck 均通过；`bench/microbench.sh -C ../fib-a40 -pkg ./http -bench BenchmarkStream`
  没有发现变化（benchstat ~）。
- **测试**（`middleware/cache`，凡经网络的都在 HTTP/1.1 和 h2c 上）：`TestHit`（保存后以 `Age` 和验证器回答，
  HEAD 由 GET 的响应回答并带长度，另一查询串分开）、`TestStorable`（21 种响应，按规则保存或不保存）、
  `TestFreshness`（在移动的时钟上检查 max-age、Expires 和推断）、`TestDefaultTTL`、`TestVary`、
  `TestConditional`（缓存自己回答 304，以及不满足的前置条件）、`TestRevalidate`、`TestRequestDirectives`、
  `TestInvalidate`、`TestBodies`（分段写出的响应体连同长度被保存；短于声明长度的不保存；超过 `max_entry_size`
  的完整透传）、`TestClientGone`（慢源站的客户端离开：不保存，归还上限，下一个请求被保存）、
  `TestCompressed`（在 compress 之后，每种编码是一个变体）、`TestEviction`、`TestVariantsBounded`、`TestZones`
  和 `TestParams`。`test/workers` 为缓存加了一个用例。完整的竞态测试在基准机上通过；`go vet` 通过。
- **基准**，在基准机上（`BenchmarkHit`，通过 fibtest 由缓存回答 4 KB 响应，5 次）：每次命中 1.48–1.50 µs，
  15 次分配、5.4 KB，其中大部分字节是 fibtest 对响应体的复制。

与计划的差别：

- 缓存回答客户端的验证器，并用源站对它们的 304 刷新陈旧响应，但不发送自己的验证器：缓存自己的请求随 I69 到来。
- 设置 cookie 的响应不保存，与 nginx 的 `proxy_cache` 一致；204 和 206 不保存，因为缓存借以保存响应的 fib
  响应体过滤器对没有响应体的状态码不运行，而缓存不回答范围请求。
- 需要 fib 的 A40，由本会话实现。fit 的 `go.mod` 指向它在 fib 分支 `a40-filter-abort` 上的 commit；按规则 15 和
  19，在 fib 的 CI 在该 commit 上通过、且它进入 fib 的 `main`（需维护者同意）之前，不推送 fit。

### I68 缓存 II：磁盘层 — 已完成

- **`disk`**，zone 的磁盘层（`path` 必填；`max_size` 默认 1 GiB；`max_entries` 默认 100000）：保存在内存中的每个
  响应也写入磁盘，每个响应一个文件（12 字节的头、JSON 形式的元数据、响应体），先写入 `tmp` 中的文件再改名到位，
  因此文件要么完整，要么不存在。文件的索引保存在内存中，无需接触磁盘就能知道请求的响应是否在磁盘上、是否新鲜；
  内存层已不再持有的响应会被读回，以 `Cache-Status: fit; hit; ttl=N; detail=disk` 回答，并重新保存到内存中。
- **工作线程从不等待磁盘。** 读、写和删除都在缓存自己的池上运行（`internal/filepool`，static 处理器的文件池也
  移到了这里：每个 CPU 4 个 worker，至少 16 个，队列 4096）。响应在磁盘上的请求会连同配置一起被持有，并在池上
  读取；读取失败，或计时器（5s）到期时仍未完成（例如磁盘挂起），请求就作为未命中从当时所在之处继续转发。写入时
  队列已满，或已有 64 MiB 等待写入，则不写入，响应只保存在内存中。
- **重启。** zone 的磁盘层创建时，由一个 goroutine 从目录读回索引：读取每个文件的元数据，删除不完整或不属于缓存
  的文件，清空 `tmp`。请求不等待它；在完成之前，请求在磁盘上能找到的会少一些。zone 在 reload 后保留其磁盘层，
  配置换了目录时改用新目录，配置去掉磁盘层时就丢弃它；两个 zone 不能共用一个目录。
- **删除。** 改变资源的请求也会从磁盘删除其响应；在删除之前排队的写入完成时不会被保留（索引中的序号，被删除的
  key 在索引中时一直保留），因此重启不会让该响应回来。条目底下的文件消失时，删除该条目。超过 `max_size` 或
  `max_entries` 时，最近最少使用的 key 连同其文件被移除。
- **测试**：`TestDiskRestart`（写入五个响应，其中有流式的和两个变体；进程的 zone 被归还后，新的 zone 读回索引，
  先从磁盘、再从内存回答每一个，还有一个 304，都不经源站）、`TestDiskPromotion`（1 MiB 的内存，后面是磁盘上的
  4 MiB）、`TestDiskNeverBlocks`（磁盘挂起：ServeHTTP 在 100 ms 内返回，请求在池的超时后作为未命中转发）、
  `TestDiskBrokenFiles`（读回索引时删除被截断的文件、垃圾文件和中断的写入；条目底下的文件消失）、
  `TestDiskInvalidate`（DELETE 删除文件，重启后不会回来；删除前排队的写入被丢弃）、`TestDiskBounds` 和
  `TestDiskZones`。它们在基准机上的竞态检测下通过，重复十次，依赖 `handler/static` 和 `internal/filepool`
  的包也都通过。

与计划的差别：

- 磁盘上的响应体就是内存层保存的那些，最多 `max_entry_size`：磁盘能保存更多响应，而不是更大的响应。磁盘上的陈旧
  响应不会被 304 刷新，因为刷新需要其响应体在内存中；请求作为未命中转发。
- 一个目录只能由一个 fit 进程使用：两个进程共用一个目录时，各自会有自己的索引。

### I69 缓存 III：锁、陈旧内容、清除 — 已完成

- **缓存锁。** GET 未命中时，若已有另一个请求在获取同一个 key，它就等待那次获取（连同配置一起被持有），并由
  其保存的内容回答；获取在响应被保存后结束，或者一旦确知不会保存就结束（不保存的响应其头部确定之时、响应体
  超过 `max_entry_size`、客户端离开），此时等待者各自去获取。等待者在 `lock_timeout`（5s）后自行转发，每个
  key 最多 4096 个等待者，超过两倍 `lock_timeout` 的获取视为不会结束。`disable_lock` 关闭此功能。等待者在结束
  获取的 goroutine 上被唤醒，各自不阻塞地被回答或转发。
- **缓存自己的请求。** fib 的 `NewStreamContext` 为缓存发出的请求运行路由的处理器，响应写到缓存自己的流
  （`fetchStream`）上；该流在缓存的钩子保存响应后丢弃收到的内容，并在请求结束时结束获取（`StreamEnder`）。借此
  缓存用自己的验证器（`If-None-Match`，否则 `If-Modified-Since`）再验证陈旧响应，这是 I67 做不到的：发现它的
  请求与其他请求一起等待这次获取，304 会刷新该响应。
- **stale-while-revalidate**（RFC 5861，响应未声明时用 `stale_while_revalidate` 参数）：窗口内的陈旧响应立即
  回答（`detail=stale-while-revalidate`），并在后台重新获取，每个 key 一次，每个 zone 同时最多 256 个；在此期间已
  被刷新的不再重新获取。**stale-if-error**（`stale_if_error`）：重新获取陈旧响应失败（没有响应，或 500、502、503、
  504）时，在窗口内用它回答等待的请求（`detail=stale-if-error`）。声明了 `must-revalidate`、`proxy-revalidate`
  或 `s-maxage` 的响应两者都不适用。两者对磁盘上的响应同样有效。
- **清除**（`cache.Purge`，以及 Admin API 的 `POST /cache/purge`，`{"zone", "urls", "tags", "all"}`，回答
  `{"purged": n}`，没有缓存使用的 zone 回答 404）：按 URL，所有变体；按标签，即响应的 `Cache-Tag`（逗号分隔）和
  `Surrogate-Key`（空格分隔）字段，最多 32 个、每个 128 字节，保存在每个 zone 的索引中，计数内存和磁盘上的响应；
  或全部。清除会到达磁盘，并丢弃仍在排队的写入。`GET /cache` 显示每个 zone 保存的内容。
- **基准工具**（`bench`）：两个场景，`cache-hot`（一个路径，两种协议）和 `cache-zipf`（100,000 个路径，按齐夫
  定律 s = 1 请求，由 HTTP/1.1 压测客户端的脚本生成），经每个服务器的缓存到一个单独的缓存后端，后者在其 Admin API
  上计数到达它的请求，由此得出每个结果的命中率。nginx 用 `proxy_cache` 和 `proxy_cache_lock`，fit 用 `cache`；
  Caddy 和 Traefik 没有自带的缓存。只有点名时才运行。
- **结果**，在基准机上（[m5-cache-2026-10-23-linux.json](https://github.com/lesismal/fit/blob/3658ae9/bench/results/m5-cache-2026-10-23-linux.json)，
  缓存均为 1 GiB，足以容纳所有路径；预热 10s 后测 30s）：

  | 场景 | nginx | fit |
  | --- | ---: | ---: |
  | `cache-hot` HTTP/1.1 | 332,436 req/s | 684,362 req/s |
  | `cache-hot` h2c | 291,559 req/s | 1,573,104 req/s |
  | `cache-zipf` HTTP/1.1 | 318,132 req/s，命中 99.975% | 650,532 req/s，命中 99.9996% |

  两者都保存了被请求的每个响应；在同样的 7 个 CPU 上，fit 在 HTTP/1.1 上回答约两倍的请求，在 h2c 上约五倍。
  改用 64 MiB 的缓存（约为路径的 15%）时，fit 自己回答了 78.0%，接近只保存最热门路径的缓存可达的 84%
  （[m5-cache-64m-2026-10-23-linux.json](https://github.com/lesismal/fit/blob/3658ae9/bench/results/m5-cache-64m-2026-10-23-linux.json)），为 444,374 req/s；
  nginx 在这个速率下守不住自己的大小：它的 `max_size` 由缓存管理进程执行，跟不上，缓存在磁盘上涨到 100–770 MB
  （命中 94.6% 到 99.4%，见[加快管理进程的那次运行](https://github.com/lesismal/fit/blob/3658ae9/bench/results/m5-cache-64m-nginx-manager-2026-10-23-linux.json)），
  而小到足以限制它的 key 区一满就让 482,684 个请求失败。因此场景比较的是能容纳工作集的缓存。
- **测试**：`TestStampede`（100 个客户端，HTTP/1.1 和 h2c，请求一个冷路径，源站 200 ms 后回答：一次获取，全部
  得到回答）、`TestLockNotKept`（一旦确知响应不会保存，等待者立即转发）、`TestLockTimeout`、`TestDisableLock`、
  `TestStaleWhileRevalidate`（立即以陈旧响应回答，一次后台获取，一个 304，之后新鲜；超出窗口则先获取再回答）、
  `TestStaleIfError`（响应自己的窗口和参数的窗口）、`TestRevalidate`（缓存的验证器和客户端的）、`TestPurge`
  （按 URL（主机大小写不同）、按两种字段的标签、全部；内存和磁盘）和 `TestTagsOf`；`admin` 的 `TestCachePurge`；
  fitbench 的 `TestParseRequestCount`。缓存的测试在基准机上的竞态检测下运行十次均通过，变更涉及的包也都通过。

与计划的差别：

- 64 MiB 的比较无法让 nginx 守住其大小，因此公布的比较是能容纳工作集的缓存；两组数字都已记录。

### I70 `fit build` 与编译期模块 — 已完成

- **`fit build`**（`plugin/build`，以及 `build` 命令）：生成一个 main 包，导入 fit 的命令行、`modules/standard` 和
  `provider/standard`（除非用 `-without` 去掉其中之一），以及每个 `-with` 模块的根包（它在 `init` 中注册自己的模块）；
  为它建立一个模块（`go mod init`，fit 和各模块按版本引入，默认 `latest`，或用其源码目录替换，再 `go mod tidy`），
  并用 `PATH` 上的 go 命令以 `-trimpath` 构建，版本标为 `<fit 的版本>+build`。`-fit` 指定基于哪个 fit 构建：一个版本，
  或 fit 源码所在的目录；省略时取正在运行的 fit 构建时记录的版本（Go 为有改动的检出生成的版本去掉 `+dirty`；由
  `fit build` 构建的 fit 给出它所基于的那个）。`-o`（默认 `fit`），`-keep dir` 保留生成的模块。`GOOS` 和 `GOARCH`
  可为其他系统构建；go 命令自身的 `GOFLAGS` 和工作区对构建的模块不起作用。
- **命令行成了一个包** `cli`（`Main`、`Version`），由 `cmd/fit` 和构建出的 main 包调用；`cmd/fit` 现在只是它的 main，
  加上所有模块和 provider 的导入，仍然接受 `-X main.version`。
- **provider 按种类自行注册**（`provider.RegisterKind`、`LookupKind`、`RegisteredKinds`），各包在 `init` 中注册，
  `provider/standard` 导入 fit 自带的四种，因此构建时可以去掉它们；引用了二进制文件所没有的种类的配置会被拒绝，并列出
  它有的种类。
- **模块模板**：[docs/modules.zh-CN.md](modules.zh-CN.md)（及其英文版）说明模块是什么、参数及其 tag、`Provision`、
  `Cleanup` 和 `Drain`、它遵守的规则、如何测试以及如何编译进来；[examples/module/hello](../examples/module/hello)
  是一个独立的模块（有自己的 `go.mod`），是给其路由的响应加上 `X-Hello` 的中间件。
- **测试**：`TestBuildWithModule` 从检出的源码构建带示例模块、不带 provider 的 fit，检查 `fit version` 显示
  `test+build`，校验并运行使用该模块的配置（响应带 `X-Hello`），并确认使用 Docker provider 的配置被拒绝；没有 go 命令
  或使用 `-short` 时跳过。另有 `TestParseModule` 和 `TestImports`。命令行的测试随之迁移。变更涉及的包在基准机上的
  竞态检测下通过；linux、darwin、windows 的 vet 和 staticcheck 通过。
- **不带 provider**（见 [M4 报告中 h2c 变慢的部分](https://github.com/lesismal/fit/blob/3658ae9/bench/results/m4-report-2026-10-23.md#the-h2c-slowdown)）：
  不带 `provider/standard` 的 `fit build` 生成 19.9 MB 的二进制文件，带上时为 21.1 MB。用 `bench/ab.sh`（现在可以用
  `bin:PATH` 指定在别处构建好的二进制文件）比较两者（都由 `fit build` 构建），h2c 上：fixed 每秒 3.81M → 3.85M 个请求
  （+1.0%，3 轮中 3 轮），proxy 238k → 236k（在噪声范围内）。所以 provider 增加的代码让 h2c 上的 fixed 慢约 1%，
  对 proxy 没有可测的影响：解释了 M4 中 fixed 的 1.8% 的一部分，不能解释 proxy 的 1.5%。

与计划的差别：

- 计划只提到 `plugin/build` 和 `cmd/fit`；命令行还移到了 `cli` 包（生成的 main 必须导入它），provider 也改为注册式，
  以便去掉。

### I71 WASM I：运行时与插件池 — 已完成

- **`plugin/wasm` 的 `wasm`** 中间件为其路由的每个请求运行一个 WebAssembly 插件，运行时为 wazero（[决策 017](decisions/017-wasm-runtime.zh-CN.md)；
  `github.com/tetratelabs/wazero` v1.12.0，纯 Go，唯一的依赖是 `golang.org/x/sys`）。插件是导出 `fit_on_request` 的
  WASI reactor；它可以读取请求的方法、路径、查询串、主机、客户端地址和自己的 `config`，读取、设置、添加和删除请求的
  字段（立即生效）以及响应的字段（在响应头确定时生效），自己回答请求（`respond`），并写日志（每秒最多 100 行）。宿主函数
  在模块 `fit` 中，文档见该包。
- **`plugin/wasm/guest`** 是 ABI 的 Go 端，用于 `GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared`：`OnRequest`、
  `RequestHeader`、`SetRequestHeader`、`SetResponseHeader`、`Respond` 等。
- **池**：`instances` 个 goroutine（fit 的 CPU 数，最多 16），各有自己的实例（创建时运行 `_initialize`）；长度为 `queue`
  （1024）的队列，满时请求立即得到 503；在队列中等待的请求受 `wait_timeout`（1s）限制；看门狗通过取消实例的 context 停止
  超过 `timeout`（100ms）的调用，之后重建该实例；每个实例最多 `max_memory`（128 MiB）。失败、陷入 trap 或被停止的调用回答
  500，或在 `on_error: skip` 时让请求不经插件继续。fib worker 上不运行任何东西：请求连同配置被持有，从池的 goroutine 上继续。
  代码对加载它的所有配置只编译一次（最后一个配置之后也保留在 wazero 的缓存中），实例在加载配置时创建。
- **测试**（`plugin/wasm`，`testdata/headers` 由 go 命令为 wasip1 构建，没有 go 命令时跳过）：`TestHeaders`（设置和删除请求
  字段，设置和添加响应字段，经过每个实例）、`TestRespond`、`TestFailures`（超时的死循环、panic、申请超过 `max_memory` 的内存：
  回答 500，或 `skip` 时继续，下一个请求由重建的实例处理）、`TestNeverBlocks`（ServeHTTP 在 100 ms 内返回；一个调用在运行，
  一个在等待，第三个立即得到 503）、`TestShared`、`TestParams`。`test/workers` 将该中间件记为由 `TestNeverBlocks` 检查。
  完整的竞态测试在基准机上通过。
- **开销**，在基准机上：`BenchmarkWASM`，经过测试用插件的请求（读取路径、方法和配置，对字段做四处修改），与不经插件的请求
  相比，经 fibtest：5.1 µs 对 0.31 µs，34 次分配对 7 次。`BenchmarkGuestCall`，仅调用本身：3.55 µs；因此交给池再交回约
  1.2 µs。每一部分都有上限：实例数和队列、超时、内存。把 wazero 链接进 fit（多 3.9 MB，从 21.1 MB 到 24.9 MB）在基准工具上
  没有影响：用 `bench/ab.sh` 比较 I70 与本次的构建，HTTP/1.1 和 h2c 上的 fixed 与 proxy 四项都在噪声范围内。

与计划的差别：

- ABI 只在请求阶段：插件在请求时修改响应的字段，响应开始后不再被调用，否则需要在池上调用期间压住响应头；Proxy-Wasm 的
  响应回调留给 I72 处理。

### I72 WASM II：Proxy-Wasm 子集 — 已完成

- **Proxy-Wasm 的请求阶段**（[决策 018](decisions/018-proxy-wasm-subset.zh-CN.md)）：`wasm` 中间件把导出
  `proxy_abi_version_0_2_0` 或 `_0_2_1` 的插件当作 Proxy-Wasm 过滤器。每个实例在创建时以 `vm_config` 和 `config` 启动其
  root context（过滤器拒绝时加载失败）；每个请求是一个独立的 context：创建，交给它请求头（`proxy_on_request_headers`，
  没有请求体的请求标为流结束），然后 done、log、delete。模块 `env` 中的宿主函数：日志（info 及以上，每秒最多 100 行）、
  header map（请求的，含 `:method`、`:path`、`:authority` 和 `:scheme`；响应的，修改在响应头确定时生效；取值、全部取出、
  添加、替换、删除、整体设置）、本地响应（状态码、响应体、头、gRPC 状态）、VM 和插件配置缓冲区、属性（请求的 path、URL
  path、查询串、主机、方法、scheme、协议，客户端地址，`plugin_name`，以及 `properties` 设定的）、每个实例的指标；外部调用、
  共享数据和队列、定时器、外部函数、设置属性和缓冲区、继续或关闭流，都返回 `Unimplemented`。暂停请求却不回答的过滤器按
  `on_error` 处理为失败。
- **不调用响应阶段的回调**：在响应头确定时调用插件，需要在池上调用期间压住响应头，fib 做不到；记为
  [A41](fib-api-requests.zh-CN.md#a41异步钩子运行期间压住响应头)。
- **已有过滤器，无需修改**：`testdata/proxywasm` 原样收录 Proxy-Wasm Go SDK 的 `http_routing` 和 `properties` 示例（Apache
  2.0，作为独立模块，以免 fit 依赖该 SDK），由测试为 wasip1 构建。`TestProxyWasmRouting`（过滤器读取插件配置并修改
  `:authority`，其后的处理器看到的请求主机随之改变）、`TestProxyWasmProperties`（缺少路由属性指定字段的请求经
  `send_local_response` 得到 401；带上该字段，或没有该属性时继续）、`TestPairs`。plugin/wasm 的测试在基准机上的竞态检测下通过。
- **开销**，在基准机上：`BenchmarkProxyWasm`，经过 `http_routing` 的请求（它掷骰子、读取配置并以 info 级别写一行日志）：
  8.4 µs、28 次分配；fit 自己的 ABI 为 5.1 µs（I71）。

与计划的差别：

- 计划要求实现常见过滤器所需的调用，或说明为何不行：请求阶段已实现，并能原样运行已有过滤器；响应阶段如说明所述，等待 A41。
- **后来（2026-10-25）：响应回调在 I81 借助 A41 和 A46 实现**（[决策 023](decisions/023-proxy-wasm-response.zh-CN.md)）。

### I73 更友好的配置格式 — 已完成

- **YAML**（[决策 019](decisions/019-yaml-config.zh-CN.md)）：`adapter/yaml` 把 YAML 配置逐键转换成原生 JSON（YAML 1.2
  的类型；解析锚点、别名和合并键，上限为一百万个节点、256 层、64 MiB 的 JSON；只能有一个文档；键必须是字符串且不重复），
  并记录每个配置路径的值写在哪里；也把原生 JSON 转换成 YAML（`FromJSON`），再转回来得到同样的 JSON。go.yaml.in/yaml/v3
  本来就是 fit 的依赖（决策 011）。
- **`adapter`**：适配器按名称和文件扩展名注册；`Adapt`、`ReadFile`、`ForPath`，以及 `Result.MapErrors`，后者把配置错误
  的位置移到源文件中，取其路径上适配器知道位置的最近的值。
- **命令行**：`fit run`（启动时、收到 SIGHUP 时以及 `-watch` 下）和 `fit validate` 像接受 JSON 一样接受 `.yaml` 或
  `.yml` 文件，错误给出 YAML 中的行列号；`fit adapt`（`-c`、`-from`、`-to json|yaml`、`-o`）进行转换，像 `fit run` 加载时
  那样检查结果，并列出适配器无法转换的内容。`examples/hello.yaml` 和 `examples/proxy.yaml` 是 JSON 示例的对应版本。
- **测试**：`TestRoundTrip`（所有示例和基准配置，JSON 到 YAML 再到 JSON 不变，YAML 到 JSON 再到 YAML 稳定）、
  `TestExamples`（YAML 版本与 JSON 内容一致）、`TestYAML`（类型、YAML 1.2 的字符串、块标量、锚点与合并）、`TestErrors`
  （两个文档、非字符串或重复的键、无穷大、未知标签、别名炸弹）、`TestPositions`；`adapter` 的 `TestParent` 和 `TestForPath`；
  命令行的 `TestYAMLConfig`（YAML 文件 4:27 处的错误）和 `TestAdapt`。变更涉及的包在基准机上的竞态检测下通过。

与计划的差别：

- Admin API 仍只接受 JSON；YAML 文件的配置以其转换成的原生 JSON 保存和显示。

### I74 nginx 配置转换 — 已完成

- **`adapter/nginx`**，注册用于 `.conf` 和 `.nginx` 文件：nginx 语法的解析器（块、引号与转义、注释、`${var}`；上限为
  100,000 条指令、64 层嵌套），以及把常用子集转换为原生 JSON 的转换器，每条未转换的指令都按行号顺序报告其所在行：
  - **server**：`listen`（地址，`[::]` 和 `*` 当作 fit 的 `:port`，`ssl`，`http2` 及 `http2` 指令当作 h2 或 h2c，
    `quic` 当作 h3），同一地址上的 server 共用一个 listener，各自的证书（`ssl_certificate`、`ssl_certificate_key`）
    合在一起；`server_name`（精确名称，`*.` 和 `.` 当作 fit 的 `**.`，`_` 表示不匹配主机）；server 级的 `return`
    成为覆盖整个 server 的一条路由；没有 location 接收的请求，由 server 的 root 或 404 回答。
  - **location**：`=`、前缀、`^~`、`~` 和 `~*`（Go 的正则表达式；Go 不接受的会被跳过），可嵌套，按 nginx 的顺序设定
    优先级（精确，然后 `^~`，然后按顺序的正则，然后最长前缀），并像 nginx 一样从 `http` 到 `server` 到 `location` 继承
    （`add_header`、`limit_req` 和 `proxy_set_header` 只在内层没有设置时继承）。
  - **内容**：`proxy_pass` 到 `upstream` 或地址（为地址建立 upstream；`https` 当作 h2；其 URI 当作对 location 前缀的
    重写；`proxy_set_header` 的字面值、`Host`，以及 fit 自己会设置的转发字段；`proxy_connect_timeout` 和
    `proxy_read_timeout` 设在 upstream 上）；`root`、`alias`、`index`、`try_files` 转换为 static 处理器（`$uri` 当作
    `{path}`）；`return` 转换为 respond，或重定向，其中 `$scheme`、`$host`、`$request_uri`、`$uri`、`$args` 转换为
    fit 的占位符；没有内容的 location 回答 404。
  - **upstream**：server 的 `weight`、`max_fails`、`fail_timeout`、`max_conns`，`down` 被去掉，`backup` 成为单独的
    upstream，由代理故障转移过去；`least_conn`、`ip_hash`、`random`，以及按 URI、客户端地址、头或 cookie 的 `hash`；
    `keepalive` 当作 `max_idle_conns`。
  - **中间件**：`rewrite` 转换为 rewrite 中间件（像 nginx 一样替换整个路径），带 `redirect`、`permanent` 或 URL 时转换为
    重定向；`limit_req` 及其 `limit_req_zone` 转换为 `rate_limit`（按地址、头或路由；没有 `nodelay` 时给出警告，因为
    fit 是拒绝而不是延迟）；`client_max_body_size` 转换为 `body_limit`；`add_header` 转换为 `headers`；`gzip on`
    转换为 `compress`。
  - **跳过并说明原因**：进程设置（`user`、`worker_processes`、`events`、`pid`、`error_log`）、`stream`、`include`、
    `map`、`if`、命名 location、`error_page`、日志、TLS 调优（`ssl_protocols` 等，fit 自行选择）、由 fit 默认值代替的
    缓冲区和超时、含 fit 无法展开的变量的值，以及其他一切。
- **命令行**：`fit adapt -c nginx.conf` 进行转换并列出被跳过的指令；`fit run` 和 `fit validate` 也接受 `.conf` 文件，
  错误指向产生该值的 nginx 行。
- **测试**：`TestSamples`（三个示例配置：带重定向和含备份的负载均衡 upstream 的 TLS 反向代理、单页应用站点，以及带有
  fit 不转换内容的完整 `nginx.conf`：都能加载，警告恰好位于被跳过的那些行）、`TestProxySample`、`TestParse`、
  `TestDirectives`（名称、listen、return、rewrite、proxy_pass、upstream 的 hash、limit_req）、`TestUnits`，以及运行
  转换结果的 `TestServe`：经 `try_files` 的文件、`return`、重定向，以及带 URI 的 `proxy_pass` 到后端；命令行的
  `TestAdaptNginx`。它们在基准机上的竞态检测下通过。

与计划的差别：

- 除计划列出的指令外，还转换了 `client_max_body_size`、`add_header`、`gzip`、`try_files`、`alias`、`index` 以及代理的头
  和超时，因为几乎每个配置都有它们。

### I75 FastCGI — 已完成

- **`handler/fastcgi` 的 `fastcgi`** 从 FastCGI 应用（如 PHP-FPM）获取回答，地址为 `address`（主机和端口，或 `unix/` 加
  socket 路径）。客户端是 fit 自己的：通过 fib 引擎（`module.Env` 和 `module.Context.Engine` 现在把它交给模块）拨号的 fib
  连接上收发 FastCGI 记录，每个连接一次一个请求并带 `KEEP_CONN`，空闲连接留给后续请求（`max_idle_conns` 16，
  `idle_timeout` 60s），最多 `max_conns`（64）个，没有空闲连接的请求在长度为 `max_pending`（1024）的队列中等待，超出则回答
  503。worker 上不等待任何东西。
- **双向流式且有上限**：请求体随到随发（`OnBody`），连接未发出的数据超过 64 KB 时用 `HoldBody` 压住；响应随应用写出随发回
  （`WriteNoWait` 并 flush），客户端排队的数据超过 64 KB 时暂停读取该连接，每次等待受写停滞超时限制。CGI 头最多读
  `max_header_bytes`（64 KiB），其 `Status` 和 `Location`（无状态码时为 302）转换为 HTTP 的。只有请求按 FastCGI 的方式
  结束、请求体也全部发出时，连接才会保留。
- **超时**：`connect_timeout`（5s，504）、`response_header_timeout`（60s，504）、`response_idle_timeout`（60s），整体为
  `timeout`（10m）。开始后中断的响应像代理那样对客户端中断（HTTP/1 关闭连接，HTTP/2 流以 `Fit-Error` trailer 结束）；客户端
  离开时关闭与应用的连接。应用的标准错误写入 fit 的日志，每秒最多 100 行。
- **失效的空闲连接**（1.0 迭代之后增加）：从空闲集合取出的、或由上一个请求直接交接的连接，可能正被应用关闭（如 PHP-FPM 的
  worker 达到 `pm.max_requests` 时），而 fit 还没收到通知。连接在本请求未收到任何数据时关闭的话，没有请求体且方法可安全重复
  （GET、HEAD、OPTIONS、TRACE）的请求会在新拨号的连接上重发一次，而不是回答 502，与 net/http 客户端的做法相同；其他请求仍回答
  502，因为应用可能已经执行过它。`TestStaleIdle` 检查这两种情况。
- **PHP 参数**：CGI/1.1 和 PHP 的参数（`SCRIPT_FILENAME`、`SCRIPT_NAME`、`PATH_INFO`、`PATH_TRANSLATED`、`DOCUMENT_ROOT`、
  `DOCUMENT_URI`、`REQUEST_URI`、`REQUEST_SCHEME`、`HTTPS`、`REDIRECT_STATUS` 等），请求的字段作为 `HTTP_*`（`Proxy` 除外，
  防 httpoxy），以及 `env` 中的参数。`split_path`（`.php`）把路径分成脚本和 path info，`index`（`index.php`）是目录的脚本。
  `try_files` 像 nginx 那样，在处理器自己的池上于 `root` 下查找路径来选定脚本，使不是独立脚本的请求交给前端控制器。
- **测试**：以 Go 的 `net/http/fcgi` 作为应用，在 HTTP/1.1 和 h2c 上：`TestParams`、`TestKeepAlive`（20 个请求共用一个连接；
  脚本的状态码和 Location）、`TestLargeBodies`（10.5 MiB 上传，含分块上传，以及开头慢读的 16 MiB 下载）、
  `TestTimeoutsAndLimits`（慢脚本得到 504；一个忙、一个等待、第三个得到 503）、`TestCutConnections`（应用在响应中途断开连接；
  客户端离开，随之关闭与应用的连接）、`TestTryFilesAndUnix`（Unix socket、前端控制器）、`TestSplit`、`TestRecords`。
  **针对 PHP-FPM**（Docker 中的 `php:8.3-fpm-alpine`，`test/e2e` 的 `TestPHPFPM`，HTTP/1.1 和 h2c）：`$_SERVER` 中的脚本、
  文件名、path info、查询串、方法、URI、字段、主机和地址，且不含 httpoxy；POST 表单和 3 MB 上传；状态码、字段和 cookie；
  重定向；4 MiB 响应；经 `try_files` 的前端控制器。`test/workers` 为该处理器加了用例。它们在基准机上的竞态检测下通过。

### I76 持续 fuzz — 已完成

- **十一个 fuzz 目标**：原有的四个（配置加载的 `FuzzLoad`、路由表达式解析器的 `FuzzParseExpr`、四层嗅探与 PROXY protocol 的
  `FuzzReadSNI` 和 `FuzzParseProxy`），以及新增的七个：`FuzzHopByHop`（代理不留下任何逐跳字段，也不留下 Connection 点名的字段）、
  `FuzzForwarded`（由任意地址和主机生成的 Forwarded 元素是一个字段值，不含控制字符）、`FuzzGRPCTimeout`（grpc-timeout 的解析器，
  以及代理写出的值读回后不会更短）、`FuzzReader`（FastCGI 的记录读取器，以任意分片喂入任意内容，保留的数据不超过一条记录）、
  `FuzzConvert`（nginx 转换器从不 panic，输出 JSON 和带行号的警告）、`FuzzAdapt`（YAML 转换为 JSON，该 JSON 写回 YAML 后转换
  结果相同）和 `FuzzDirectives`（缓存的 Cache-Control、Vary 和 Cache-Tag 解析器给出有界的值）。
- **CI 中**：每次推送的 `fuzz` 任务从已提交的语料把每个目标各跑 15s；`.github/workflows/fuzz.yml` 每天把每个目标各跑 10 分钟
  （也可以按需运行任意时长），增长的语料通过 Actions 缓存在各次运行间延续，失败的输入作为 artifact 保存，修复时加入
  `testdata/fuzz`。
- **语料**：每个目标在基准机上跑了两分钟，发现的有价值输入提交在各包的 `testdata/fuzz` 下（约 2,700 个输入，260 KB），`go test`
  会把它们作为种子运行。
- **发现**：YAML 适配器的两个往返缺陷，均已修复并保留输入：`-0.` 变成 JSON 的 `-0`，经 YAML 回来后成为整数 0（负零现在写为 0）；
  字符串 `<<` 不加引号写回 YAML 后被读作合并键（现在加引号）。其他目标没有发现问题。

### I77 配置与 Admin API v1 冻结 — 已完成

- **[`docs/compatibility.zh-CN.md`](compatibility.zh-CN.md)**（及英文版）是 1.0 起的策略：覆盖什么（原生配置 schema 版本
  1，含标准模块的参数；Admin API v1；命令行；指标；访问日志字段；`module` 和 `plugin/build` 包），不覆盖什么（消息、仪表盘、
  WASM 宿主函数和 Proxy-Wasm 子集，在 1.0 中是实验性的），1.x 版本可以做什么（增加；只为安全或无上限的资源收紧默认值；
  重命名，见下文），以及新版本如何到来（改动清单；`fit adapt` 从上一版本转换，新 fit 至少在一个版本内仍带警告地读取上一版本；
  Admin API v2 在 `/v2/` 下与 v1 并存）。
- **schema 的版本**：配置可以写 `"schema_version": 1`；它是可选的，读取版本 1 的 fit 拒绝其他版本，而不是错误地读取。schema
  的标题写明 "schema version 1"（`config.SchemaVersion`）。
- **重命名的字段保留旧名字**：`renamed:"old"` 标签让解码器接受旧名字，并在其路径、行和列给出警告，警告由文档保存
  （`Config.Warnings`，包括编译时解码的模块参数中的警告）；同时给出两个名字是错误。`fit validate` 和 `fit run` 在启动和每次
  重载时打印警告，位置是文件中的位置（YAML 也一样），Admin API 在加载的 `warnings` 中返回它们。schema 把旧名字列为
  `deprecated`。
- **审查** 了每个字段的名字，发现六个不符合其他字段遵循的规则（以字节计的大小以 `_bytes` 结尾，周期以 `_interval` 结尾），
  已重命名：`cache` 的 `max_size`、`max_entry_size` 和 `disk.max_size` 改为 `max_bytes`、`max_entry_bytes` 和
  `disk.max_bytes`；`compress` 的 `min_length` 改为 `min_bytes`；`jwt` 的 `jwks_refresh` 改为 `jwks_refresh_interval`；
  `wasm` 的 `max_memory` 改为 `max_memory_bytes`。对应的 Go 字段一并改名。Admin API 的路径和结构保持不变。
- **测试**：`TestRenamed` 和 `TestSchemaVersion`（config），`TestValidate`（cli）和 `TestWarnings`（admin）中的重命名字段，
  经 `Apply` 和 `POST /load`。它们在基准机上的竞态检测下通过。

### I78 参考文档与迁移指南 — 已完成

- **[快速上手](getting-started.zh-CN.md)**：安装；第一个配置的检查与运行；静态文件；带健康检查的 upstream；默认 HTTPS，
  以及来自文件的证书；中间件；以及通过 SIGHUP、`-watch` 和 Admin API 在运行中修改配置。其中每个配置都通过了
  `fit validate`，其 Admin API 命令在基准机上对 fit 实际运行过，得到的回答与指南所述一致。
- **[配置参考](reference/config.md)**，由 `schema.Reference` 从解码器和 schema 所用的同一组类型和标签生成：配置及其中每个
  对象各一张表，标题是其路径（`listeners[].tls`），每个标准模块的参数也各一张表，列出每个字段的类型（含范围和枚举）、默认值
  和说明，重命名字段的旧名字标为 deprecated，并相互链接。`module.Info` 增加了 `Doc`，即模块的说明，每个标准模块都已填写。
  `go test ./schema -update` 生成它，内容过期或有字段、模块缺少说明时 schema 的测试会失败，从而保证每个字段都有说明。
  `fit schema -reference` 打印当前二进制所含模块（包括 `fit build` 加入的）的参考。参考是英文的，来自代码；其中文页说明如何阅读。
- **迁移指南**：从 [nginx](migrating-from-nginx.zh-CN.md)（先用 `fit adapt`，然后是指令到字段的对照表，以及行为上的不同：
  路由顺序、没有变量、默认 HTTPS、默认开启上限）、[Caddy](migrating-from-caddy.zh-CN.md)（完整翻译一个站点、指令对照表、
  两者 Admin API 的区别）和 [Traefik](migrating-from-traefik.zh-CN.md)（entryPoints、router 和 service；规则，`match.expr`
  接受 Traefik 的语法，包括反引号；中间件；provider 和 Docker 标签）迁移。各有中英文版。
- **文档能构建**：`docs` 包的 `TestLinks` 检查仓库中 86 个 Markdown 文件里的每个相对链接：所指文件存在，`#` 之后的标题或
  锚点也存在（按 GitHub 的生成规则）。没有发现失效的链接。
- README 改为说明 fit 现在能做什么，并链接到这些指南。
- **测试**：`TestReferenceUpToDate`、`TestReferenceNeedsDescriptions`（schema），带 `-reference` 的 `TestSchema`（cli），
  `TestLinks`（docs）。它们与模块增加了说明的各包一起，在基准机上的竞态检测下通过。

### I79 Caddyfile 转换器、带注释的 JSON — 已完成

- **`adapter/caddyfile`**，注册给名为 `Caddyfile` 和以 `.caddyfile` 结尾的文件（`adapter.Register` 现在除扩展名外也接受
  完整文件名，`ForPath` 会匹配它们）：Caddy 语法的词法和语法分析器（普通、双引号、反引号和 heredoc token，`#` 注释，
  `\` 续行，`{$ENV}` 和 `{$ENV:default}`，片段及带 `{args[N]}` 的 `import`，单站点可省略大括号；token 数、嵌套和导入都有
  上限），以及常用子集的转换器（[决定 020](decisions/020-caddyfile-and-jsonc.zh-CN.md)）：全局选项（`email`、`acme_ca`、
  `acme_ca_root`、`storage file_system`、`admin`、`auto_https`、`http_port`、`https_port`、`on_demand_tls`、
  `servers { protocols }`）；带协议、端口、路径和 `bind` 的站点地址；命名匹配器（任意位置含 `*` 的 `path`、`path_regexp`、
  `host`、`method`、`header`、`query`、`remote_ip`、`client_ip`，`not` 写成 `match.expr`）；`handle`、`handle_path`、
  `route`、`reverse_proxy`（含端口范围和协议的 upstream、`lb_policy`、健康检查、`header_up`、`header_down`、
  `transport http`、重试）、`file_server`、`root`、`try_files`、`respond`、`redir`、`rewrite`、`uri`、`header`、
  `request_header`、`encode`、`basic_auth`（包括旧的 base64 哈希）、`forward_auth`、`request_body`、`php_fastcgi`、`tls`
  （证书文件、按需、客户端认证）和 `log`。只在 HTTPS 端口上提供的站点不写 listener，交给默认 HTTPS；否则写出 listener，
  并在 HTTP 端口上用一条路由把 HTTPS 域名跳转过去。每个处理器是一条路由；每个中间件指令按 Caddy 的指令顺序放到它覆盖的
  路由上，只覆盖一部分的路由会被拆开，root、try_files、rewrite、uri 和 redir 在一个块里只取最具体的，与 Caddy 一致。
  所有跳过的部分都按行给出警告。`fit adapt`、`fit run` 和 `fit validate` 接受 Caddyfile；从 Caddy 迁移的指南写明了能转换
  什么、哪些行为不同。
- **带注释和末尾逗号的 JSON**：`config.Standardize` 把 `//` 和 `/* */` 注释以及 `]`、`}` 前的逗号（字符串内的不算，`[,]`
  仍是错误）逐字节替换为空白，位置保持与源文件一致。fit 的 JSON 解析器会调用它，所以 `config.Load` 和 `ParseValue` 接受
  这样的 JSON；`adapter.Adapt` 对 JSON 文件调用它，Admin API 的 `Configs.update` 在保存配置前调用它，因此与 provider
  合并、按 pointer 编辑和读回的都是标准 JSON。
- **已知差距**（转换器会列出）：`rewrite` 和 `uri` 在选定路由之后生效；`php_fastcgi` 与 `file_server` 并用时，既不是文件
  也不是脚本的路径不会转给 `index.php`，因为 fit 的 FastCGI 处理器无法从文件回退到前端控制器；Caddy 的内部 CA、CEL 表达式、
  `templates`、`map`、`handle_errors` 和导入文件不转换。
- **测试**：`TestSamples`、`TestSiteSample`、`TestFullSample`、`TestLex`（注释、字符串、heredoc、环境变量、续行、错误）、
  `TestParse`（片段、跨行的地址、无大括号的站点）、`TestDirectives`（地址、匹配器、拆分、handle、route、PHP、代理选项、
  全局选项）、`TestIntersect`、`TestUnits`、`TestServe`（转换后的 Caddyfile 端到端提供服务）和 `FuzzConvert`（加入 CI 的
  fuzz 任务，附语料）（adapter/caddyfile）；`TestStandardize`、`TestLoadJSONC`（config）；`TestLoadWithComments`（admin）；
  `TestAdaptCaddyfile`、`TestJSONWithComments`（cli）。它们在基准测试机上以竞态检测器通过；`FuzzConvert` 在那里跑了 30 秒、
  约一百万个输入，没有失败。只涉及控制面：不做基准测试（规则 21）。
- **I79 之后的配置文档**：[配置](configuration.zh-CN.md) 是三种格式的入口，顺序为 Caddyfile、YAML、JSON，同一个站点各写
  一份，并说明配置如何运行、检查、转换和修改；每种格式一页（[Caddyfile](reference/caddyfile.zh-CN.md) 逐条列出指令、全局
  选项、地址、匹配器以及指令如何变成路由；[YAML](reference/yaml.zh-CN.md)；[JSON](reference/json.zh-CN.md)）；
  [配置参考](reference/config.md) 分章组织，配置的每个部分以及处理器和中间件各一章，每章说明用途，并在字段表前给出 Caddyfile
  （有对应写法时）、YAML 和 JSON 的示例，开头有目录；中文页逐章说明并链接到英文字段表。各章是 `schema/doc` 中的 Markdown，
  嵌入后由 `schema.Reference` 编入生成的页面；YAML 示例由 JSON 生成。`TestChapterExamples`（schema）加载每章的 JSON，
  用模块解码每个模块的参数，并无警告地转换每个 Caddyfile；`TestConfigExamples`（docs）加载格式页面上的每个配置；
  `TestExampleCaddyfiles`（cli）验证 `examples/hello.caddyfile` 和 `proxy.caddyfile`，即 JSON 示例的 Caddyfile 版本。
- **配置模板**：[templates](../templates/README.zh-CN.md) 为每种格式提供一份完整配置。`templates/yaml/fit.yaml` 和
  `templates/json/fit.json` 由 `schema.Template`（也可用 `fit schema -template yaml|json`）从与配置参考相同的类型和标签生成：
  包含配置和每个模块参数的全部字段，每个字段的说明、类型、可选值和默认值写在注释中，一个小配置是启用的，其余以默认值或
  示例值注释掉；路由部分列出每个处理器和每个中间件的全部参数。`templates/caddyfile/Caddyfile` 手写：包含转换器接受的全部
  全局选项、地址形式、匹配器、指令和子指令。测试：`TestTemplatesUpToDate`、`TestTemplatesLoad`（都能加载，且包含全部字段）
  和 `TestCaddyfileTemplate`（schema）。顺带修复两处问题：`fit validate` 拒绝所有带 `fastcgi` 处理器的配置，因为编译时需要
  engine（现在与 upstream 一样只为检查而编译；`TestValidateFastCGI`）；Caddyfile 转换器把没有 root 的 `php_fastcgi` 写成
  fit 不接受的 `"."`（现在跳过并给出警告），并对站点级的 `try_files` 每条路由警告一次（现在只警告一次）。

### I80 fit 自己的 .konf 格式；多文件配置 — 已完成

- **`adapter/fitconf`**，注册为 `.konf` 文件的适配器（[决定 021](decisions/021-fit-format-and-config-files.zh-CN.md)）：
  指令块的词法和语法分析器（裸词、双引号和反引号词、`\` 转义、`#` 注释、指令间的 `;`、`{}`、`{$ENV}` 和 `{$ENV:default}`；
  对词数和嵌套有上限），以及转换器：通过 `config.Fields` 把每条指令当作其所在块要解码成的 Go 类型的一个字段来读取。标量取
  一个参数，按字段类型解释（类型不对的值作为字符串保留，交给配置检查在所写位置报告）；列表取各个参数，重复的行会追加；
  对象和 map 取一个块；对象列表每个元素一个块；模块写名字再跟一个按模块自己参数类型解码的块；顶层的 `listener`、`route`、
  `upstream`、`service` 和 `provider` 用单数，参数是名字。不认识的字段按所写内容转换，交给配置检查指出。
  `site <hosts...> { route <paths...> { ... } }` 在转换前展开为普通路由，共享站点的 listener、优先级、match 和中间件。每个
  路径都记录位置，因此配置错误指向 `.konf` 文件。`fitconf.FromJSON` 把原生 JSON 写成 `.konf` 文件，转换回来得到同样的配置
  （路由逐条写出，不归并成站点）。
- **多个文件和目录**：`fit run`、`fit validate` 和 `fit adapt` 的 `-c` 可以重复，也可以指定目录；目录代表其中（递归、按字典序）
  的 `.konf`、`.yaml`、`.yml` 和 `.json` 文件，隐藏文件除外（`adapter.Files`，最多 10,000 个文件、64 MiB）。`adapter.AdaptSet`
  逐个转换并合并：顶层列表拼接，其他字段只能来自一个文件，`schema_version` 可以来自多个文件但必须一致；`Set.MapErrors` 把
  合并后配置中的错误映射到所在文件、该文件内的路径和位置（为此 JSON 文件也能按路径查位置）。`config.Error` 增加了 `File`，
  打印在位置之前。`provider/file` 的监视器监视一组路径，每次查看时重新展开，因此目录中加入或移除文件也算变化，并交出读取的
  每个文件；SIGHUP 和 `-watch` 都重新读取全部文件。
- **文档中的格式**：配置文档、快速上手、README 和模板讲的是 `.konf`、YAML 和 JSON。新增 [.konf 参考](reference/fit.zh-CN.md)；
  Caddyfile 页面改为 [转换 Caddyfile](reference/caddyfile.zh-CN.md)，作为迁移工具，与 nginx 转换器一起保留，推荐用 `-to fit`
  转换；迁移指南改用 `.konf`。配置参考各章给出由 `FromJSON` 从其 JSON 生成的 `.konf` 示例，或章节自己的示例（路由一章用了
  站点），`TestChapterExamples` 检查二者是同一份配置；各章的 Caddyfile 示例和 Caddyfile 列已删除。`templates/konf/fit.konf`
  与其他模板一起生成（`fit schema -template fit`），可直接加载；`templates/caddyfile` 已删除，`examples/*.caddyfile` 改为
  `examples/*.konf`。
- **测试**：`TestConvert`、`TestErrors`、`TestPositions`、`TestRoundTrip`（示例、测试配置和 JSON 模板写成 `.konf` 再读回）、
  `TestWord`、`TestRegistered` 和 `FuzzAdapt`（加入 CI 的模糊测试，附带它发现的两个种子：模块的空参数写回时丢了 `{}`；类型未知的列表中像数字的字符串
  写回时没加引号）
  （adapter/fitconf）；`TestFiles`、`TestMerge`、`TestMergeConflicts`、`TestMapErrors`（adapter）；`TestDirectory`
  （provider/file）；`TestExampleFitFiles`、`TestAdaptFit`、`TestSeveralFiles`（cli）；`TestConfigDirectory`（test/e2e：由
  `.konf`、YAML 和 JSON 文件组成的目录以 `-watch` 运行，加入、移除和写坏文件）；模板和章节测试（schema）以及
  `TestConfigExamples`（docs：格式页面、快速上手和 README 中的每个 `.konf` 示例都能加载）。它们在基准测试主机上开启 race
  检测通过，`FuzzAdapt` 在那里运行 3 分钟、约 180 万个输入，没有失败。仅涉及控制面：无基准测试（规则 21）。
- **后续（2026-10-24）：`.fit` 先改为 `.conf`，再改为 `.konf`。** 该格式的文件改用 `.konf`，`.fit` 和 `.conf` 都不再读取；nginx
  和 Caddyfile 适配器不认扩展名和文件名：`fit run` 和 `fit validate` 只加载 fit 自己的格式，nginx 配置或 Caddyfile 用
  `fit adapt -from nginx` 或 `-from caddyfile` 转换（[决策 021](decisions/021-fit-format-and-config-files.zh-CN.md#后续扩展名改为-konf)）。
  上面的条目保留当时的名字。

### I81 WASM III：Proxy-Wasm 的响应回调 — 已完成

- **响应回调**（[决策 023](decisions/023-proxy-wasm-response.zh-CN.md)）：导出了 `proxy_on_response_headers`、`_body` 或
  `_trailers` 的 Proxy-Wasm 插件跟随它放行的每个请求的响应，其上下文在创建它的实例上保持打开（每个实例最多 `max_streams`
  个，默认 1024；超过时返回 503）。这些调用排在该实例的 goroutine 上（`worker.later`），先于新请求，从不在写响应的
  goroutine 上运行。`proxy_on_response_headers` 在 fib 的 `OnHeaderAsync`（A41）压住响应头时运行：插件读取 `:status` 和
  字段，修改字段，请求阶段对响应字段的修改先生效。body 经 fib 的 `OnBodyFilterAsync`（A46，fit 的会话为此在 fib 中实现）交给
  `proxy_on_response_body`：每次调用拿到上次之后写入的部分，用 `proxy_get_buffer_bytes`、`proxy_set_buffer_bytes` 读取和替换；
  暂停的插件持有 body，下次看到它持有的全部内容，最多 `max_body_bytes`（1 MiB）；最后一次调用带上 trailer（map 类型 3），
  有 trailer 时交给 `proxy_on_response_trailers`，插件也可以添加。之后，或请求先结束时（`OnCancel`、过滤器被中止），上下文
  done、log、delete，为它持有的配置随之释放。`response: headers` 只给响应头，`none` 都不给。属性 `response.code` 读取状态码。
- **失败**：回调失败、trap 或超时，持有的 body 超过 `max_body_bytes`，以及响应开始后发送本地响应，都让响应失败（关闭 HTTP/1
  连接或重置流）；`on_error: skip` 时响应的其余部分绕过插件继续。重新创建的实例会丢失原有的上下文，它们的响应按失败处理。
- **测试**：Proxy-Wasm Go SDK 的 `http_headers` 和 `http_body` 示例原样收录于 `testdata/proxywasm`（加上 `http_headers` 需要的
  `gjson`），另有 fit 自己的过滤器 `testdata/proxywasm/response`：`TestProxyWasmResponseHeaders`、`TestProxyWasmResponseBody`
  （追加、前置、替换；`response: headers`）、`TestProxyWasmResponseTrailers`（分几次写出的 body 每部分转为大写，trailer 被读取、
  删除和添加；没有 trailer 的 body 被加上一个；204 不调用 body 回调）、`TestProxyWasmResponseFails`（迟到的本地响应、超时的调用、
  超过上限的 body，`on_error` 分别为 fail 和 skip）、`TestProxyWasmMaxStreams`、`TestProxyWasmResponseClientGone`、
  `TestBodyBuffer`；每个测试都检查所有上下文已关闭、对配置的持有都已归还。`test/e2e` 的 `TestProxyWasmResponse` 经过滤器代理
  带 trailer 流式发送的 5 字节和 300 KB，覆盖 HTTP/1.1、h2c、h2、TLS 上的 HTTP/1.1 和 HTTP/3；`TestProxyWasmResponseSlowPeers`
  让一个客户端在经过过滤器的 64 MB 响应上停止读取，并经每次调用空转 20ms 的过滤器流式传输 512 KB，同时不经过滤器和经过滤器的
  请求都立即得到应答。它们在竞态检测下通过，vet 和 staticcheck 也通过。
- **fib**：A46 已在 fib 的 `main`（`dbe5c274`），带测试，所涉及的包及其依赖方在竞态检测下通过，linux、
  darwin、windows、freebsd 的 vet 和 staticcheck 通过；fit 按 commit 依赖它（规则 15、19）。

与计划的差别：

- 这是计划最后一项之后新增的迭代：它等待 A41，body 部分等待 A46，后者由 fit 的会话加入 fib。

## 给后续迭代的说明

- **h2c 比 M3 慢 1.5–1.8%（见 [M4 报告](https://github.com/lesismal/fit/blob/3658ae9/bench/results/m4-report-2026-10-23.md#the-h2c-slowdown)）。** 用 `bench/ab.sh` 对比 M3 的构建（`1486c28`）与 M4 的构建（`0dd5956`）：fixed h2c −1.8%，proxy h2c −1.5%，其余在噪声范围内。二分后发现它在 I59–I64 间逐步累积，每一步都在噪声范围内（I59–I61 −0.8%，I62–I64 −0.8%）；不是 fib 升到 `86071b8`，也不是 I65 的 tracer 检查；I61 加入 `**.` 主机后，`router.Match` 的微基准没有变化。这几个迭代因 provider、各类客户端、共享存储和仪表盘使 fit 的代码段从 6.14 MB 增至 6.42 MB，这暗示是代码布局的影响，但未能证明。由于请求路径没有改动，按规则 22 记录而不修复；I70 测量了不带 provider 构建的 fit：h2c 上 fixed 快 1.0%，proxy 在噪声范围内；因此代码体积解释了 fixed 的 1.8% 的一部分，不能解释 proxy 的 1.5%，后者仍未查明。
- **fib 版本。** fit 使用 fib v1.6.0（`55e4140`），并随之使用 Go 1.27.3。v1.6.0 即 `fcb1718` 加上安全修复 `d639b52`：Go 1.27.3（CVE-2026-111943）不再信任一组中间 CA，fib 的完整握手从工具链得到这一点，因此 fib 需要 Go 1.27.3；且恢复的 TLS 会话会像完整握手一样重新验证对端：服务器端验证会话票据中的客户端证书链，QUIC 客户端验证仅按服务器名保存的会话。按规则 19 的例外使用 tag，因为所需的 fib 版本已有 tag。此前 fit 使用 fib `main` 的 `fcb1718`（`v1.5.7-0.20261023040847-fcb1718d079a`），即 `b5d406a` 加上两个在其后使 fib 的 CI 失败的偶发测试的修复。`http` 的 `TestBodyWriterHoldsNoGoroutine` 统计整个进程的 goroutine，而在 h2c 上每个上传的请求体交给引擎共享的处理器池，该池在任务找不到空闲 worker 时新增一个，并每秒回收空闲的 worker，因此在 CI 的 ubuntu runner 上，被测量的 40 个上传增加了 47 个；现在它像 `TestRetainStress` 一样不计入停驻的池 worker。`benchmark/rpc` 的客户端在运行结束时立即读取计数，未等进行中的调用完成，因此 `TestClientCountsFailures` 在慢速 macOS runner 上以竞态检测运行 50ms 时，看不到任何 gRPC 调用完成（calls=0 fails=0），使 fib 在 `b5d406a` 上的 CI 失败；现在每个调用者至少发起一次调用，并在全部完成后读取计数。`b5d406a`（`v1.5.7-0.20261023021744-b5d406a1d28c`）自 I77 起使用，即 `3998953` 加上一处修复：处理器在请求体仍在到达时发出完整响应，而剩下的请求体多于服务器愿意读取丢弃的量（分块请求体、超过 256 KiB 的请求体，或在等 100 Continue 的请求体）时，响应声明 Connection: close，因为请求结束后连接会关闭。没有它，fit 的 `TestBasicOverNetwork` 在基准机上 30 次中失败 17 次：密码错误的 401 以 keep-alive 发出，Go 的客户端把下一个请求发到了 fib 随后关闭的连接上（"use of closed network connection"）。有了它，30 次全部通过；fib 的 `http` 测试在竞态检测下通过，各 OS 的 vet 和 staticcheck 通过，`BenchmarkServerKeepAlive`、`...POST` 和 `...Hooked` 没有变化，分配相同：在基准机上交替运行 10 次时，benchstat 认为 `ServerKeepAlive` 慢了 15%（p=0.023，波动 ±20%），交替运行 20 次未能证实（746 ns 对 753 ns，p=0.92），而这一改动只在该路径上增加了一次类型断言。`3998953`（`v1.5.7-0.20261022203111-39989533638b`，自 I67 起）即 `86071b8` 加上 A40（`BodyAborter`，以及过滤器之后的钩子拿不到 `Content-Length`）。两者于 2026-10-23 经维护者同意进入 fib 的 `main`。此前 fit 使用 fib `main` 的 `86071b8`（`v1.5.7-0.20261022124907-86071b8bb871`），即 `4f1c3e9` 加上 `5fc8250` 和 `aa0ba7e`（prefork，以及用 eBPF 在 reuseport 组内均衡连接，fit 的 `main` 尚未使用）和 A38（`86071b8`，请求体失败时 HTTP/1 响应声明连接将关闭；没有它，fit 的 `TestBodyOverNetwork` 会遇到重置）。`4f1c3e9`（`v1.5.7-0.20261021175136-4f1c3e9c13c9`）即 `c3e27ed` 加上 A36（SOCKS 与 CONNECT 中继的空闲超时）和 A37（`ConnectProxy` 在拨号回调里应答）。`c3e27ed`（`v1.5.7-0.20261021172359-c3e27ed1667e`）即 `9ab22e8` 加上 A34（`11de9eb`，UDP 对端自己的空闲超时）、A35（`530d222`，替换连接的对端地址）和 `c3e27ed`（http3 原始测试客户端的竞态：在读取方就绪前到达的流数据会丢失，导致 fib 的 macOS CI 失败）。`9ab22e8` 即 `929924e` 加上 A32（`0bd2447`，按请求选择从请求头起流式交给处理器的请求体；HTTP/2 客户端发送 `te: trailers`）和 A33（`9ab22e8`，读取被暂停时看到的对端结束不再丢失）。`929924e` 即 `fc7ec5e` 加上 A28-A30（处理器返回后 Upgrade 被保留的请求、`Client.StartUpgrade`、HTTP/3 客户端的 extended CONNECT）。`fc7ec5e` 即 `cfb4f09` 加上 A27（`Context.OnBodyFilter`）。`cfb4f09` 即 `8e04da9` 加上 A25（`Context.LimitBody`）、A26（保留每个 `OnCancel` 回调）和一处 QUIC 修复。`8e04da9` 即 `06992a5` 加上两处测试修复（HTTP/2 保留 body 的测试等 handler 返回后再释放；SOCKS UDP 测试等所有数据报到齐后再放行拨号）。`06992a5` 即 `b101811` 加上一处修复，让内核分配的 UDP 端口在 bind 时不带 SO_REUSEADDR 和 SO_REUSEPORT，改在 bind 之后设置，因为 Linux 可能把已被占用的端口再分给另一个服务器（在 fib 的测试里，客户端的 QUIC 握手连到了别的测试的服务器，并正确地拒绝了它的证书）。`b101811` 即 `a6d78cd` 加上 fib 偶发失败测试的修复，以及由此发现的一个 taskpool bug 的修复（一个分片能用光共享额度，MaxWorkers 为 16 时同时跑了 17 个阻塞任务）。`a6d78cd` 在 v1.5.6 之后带来：A22（`7f632cb`，客户端连接只在没有待触发的空闲定时器时才设置它）和 A24（`a6d78cd`，taskpool 新建的环不写任何单元，`handler/proxy` 在竞态检测下的峰值从 1.42 GB 降到 540–720 MB）。按规则 19 按 commit 依赖，不打 v1.5.7 tag。v1.5.6 带来 A20：HTTP/1 客户端自己写请求头、读响应头（见下文 R07f 的记录），以及 v1.5.5 中修复
  fib CI 的改动（连接在发送中途关闭时关闭排队文件的描述符；HTTP/2 接管连接前先解除 HTTP/1 的空闲期限）和 `Close` 切断的连接也
  运行 `OnClose`（A23：没有它，空闲 HTTP/2 连接的 idle 定时器会把每个已关闭的引擎留在内存中，`handler/proxy` 在竞态检测下峰值达 5.3GB，交换导致 macOS 上一次 h2c 上传超时）。v1.5.4 带来 A16 的停滞上限（`SendFileStall`）、A17 和 A18 的第一部分（`Context.Status`），经维护者同意由 fit 的会话在 fib 中实现（R07e）；在此之前，fib `main` 的 `8c637e0`（v1.5.3 之后）增加了连接调试状态和测试。v1.4.0 带来了 R03 的 API（A1–A7、A9、A11、A13，以及 A8 和 A12 的一部分），
  v1.5.0 带来了 A12 的其余部分（`Client.Start`），v1.5.1 带来了 A14 和 A15，v1.5.2 带来了 A16 的第一部分，v1.5.3 带来了
  A19：流式请求的 `Trailer` 在交出请求体结尾的地方设置，CI 的竞态检测曾在代理的 `TestTrailers/h2c` 中发现它；见 [fib-api-requests](fib-api-requests.zh-CN.md)。v1.3.1 在 v1.3.0 之上只增加了 `ClientConfig.StreamUnknownLengthAtHeader`（A10）。
  v1.3.0 是为 I16 和 I22 在 2026-10-19 的 fib `main` 上打的 tag：带来了
  `fib.New(config, bindings...)` 以及 `AddBinding`、`RemoveBinding`、`ShutdownBinding`，`Connection.NotifyOutput`、
  `Context.HoldBody` 和 `BodyWriter`。如果某个迭代需要更新的 fib，就按 commit hash 升级到那个 fib commit（`go get github.com/lesismal/fib@<hash>`，规则 19），
  绝不使用 `replace` 指令或 `go.work`。
- **SO_REUSEPORT。** fib v1.3.0 默认开启 `ReusePort` 监听，这会让另一个进程也能绑定同一地址。`listener` 先用普通
  bind 检查每个地址，被占用时报错。
- **图的生命周期与异步 handler。** `listener` 的分发器在 handler 返回时释放图。稍后才应答的 handler 用
  `module.Context.Graph()` 自己持有图直到结束，并实现 `module.Drainer`，在图被排空时结束仍在处理的请求，代理就是
  这样做的（决定 004）。
- **没有长度的代理响应体。** 在 fib v1.3.1 之前，fib 的客户端要等这种响应体到达 64 KB 后才交给代理
  （`StreamResponseBodyThreshold`），所以像 server-sent events 这样缓慢涓流的响应会晚到客户端：在
  `handler/proxy/sse_test.go` 中，无论上游是 HTTP/1.1、h2c 还是 h2，第一个事件都要约 610 ms 后、流结束时才到达。fib v1.3.1
  增加了 `StreamUnknownLengthAtHeader`（[A10](fib-api-requests.zh-CN.md#a10长度未知的客户端响应从响应头起就以流方式交付)），
  `upstream.New` 打开了它：这种响应体从响应头起就交给代理，带长度的响应体仍和以前一样在 64 KB 以内整体接收。该测试在
  各种上游协议上都运行并通过。没有做基准：压测框架的代理后端带 `Content-Length` 应答，走的路径和以前相同。
- **测试环境。** 本机只运行普通测试：单元测试、竞态检测、vet、staticcheck。所有压测都在基准机上运行（规则 16–18），
  用 `ssh -S ~/.ssh/fitbench.sock -p 10013 root@127.0.0.1` 登录：`fitbench` 框架、wrk、h2load、`go test` 微基准、
  perf，以及压力或偶发失败的复现（高 `-count`、`-cpu`）。基准机上没有 Go：用 `GOOS=linux GOARCH=amd64` 交叉编译
  （`go build`、`go test -c`）后在那里运行。上文记录的在开发用 Mac 上跑的微基准早于这条规定。竞态检测无法从 Mac 交叉
  编译；从 I36 起，竞态测试改在基准机的 Docker 中运行：把执行过 `go mod vendor` 的源码副本同步到基准机，在
  `golang:1.27-bookworm` 镜像中以 `GOFLAGS=-mod=vendor` 运行 `go test -race`，构建缓存放在一个卷里。
- **基准测试。** fit 新支持的场景或模块要加进 `bench/cmd/fitbench/servers.go` 和
  `bench/configs/fit/fit.json`。nginx 会预分配连接表（`worker_connections 65536` 时每个 worker 约
  29 MB），这体现在打开任何连接之前的内存里，而不是每连接的开销里。
- **staticcheck。** 用较旧 Go 构建的 staticcheck 读不了 Go 1.27 的导出数据。按 CI 的方式（基于最新的
  `golang.org/x/tools`）构建，再传给 make：`make check STATICCHECK=/path/to/staticcheck`。
- **测试 handler。** `internal/fibtest.Serve` 和 `ServeRequest` 通过 `fibhttp.NewStreamContext` 和一个记录用的
  stream 运行 fib handler，不经过网络。带 content type 的 `Context.Respond` 会忽略 `c.Header()`；需要其他响应头
  字段时用 `WriteResponse`。
- **错误位置。** 错误测试比较带 行:列 的完整消息。期望的列号应该从文档计算，而不是手数。
