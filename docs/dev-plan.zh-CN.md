# fit 开发计划

[English](dev-plan.md) | [简体中文](dev-plan.zh-CN.md)

本计划把 [路线图](roadmap.zh-CN.md) 拆成一系列小迭代。每个迭代是一份能在一次工作会话、一个 commit 或 PR
内完成的工作：小到它涉及的代码、需要的文档和测试能放进一次上下文，并在会话空间用完之前做完。

- [如何进行一次迭代](#如何进行一次迭代)
- [包结构](#包结构)
- [索引](#索引)
- [M0 基础](#m0-基础)
- [M1 反向代理核心](#m1-反向代理核心)
- [M1 审查修复](#m1-审查修复)
- [M2 动态配置与 TLS](#m2-动态配置与-tls)
- [M3 网关能力](#m3-网关能力)
- [M4 平台集成](#m4-平台集成)
- [M5 缓存与扩展](#m5-缓存与扩展)
- [1.0](#10)
- [P2P 与 NAT 穿透](#p2p-与-nat-穿透)
- [VPN：fit 节点组成的网络](#vpnfit-节点组成的网络)
- [控制台：登录与加密的 store](#控制台登录与加密的-store)
- [Agent 与 tunnel](#agent-与-tunnel)
- [WebRTC](#webrtc)
- [媒体：认证、流媒体与页面](#媒体认证流媒体与页面)
- [workbench 中的 AI 助手](#workbench-中的-ai-助手)
- [媒体库：点播与频道](#媒体库点播与频道)
- [集群管理](#集群管理)
- [日志：文件、切割与保留](#日志文件切割与保留)
- [数据同步：从生产者到消费者](#数据同步从生产者到消费者)

## 如何进行一次迭代

**选一个。** [开发进度](progress.zh-CN.md) 说明了当前进展和下一个迭代。否则在 [索引](#索引) 中取第一个状态为
`todo`、且依赖全部为 `done` 的迭代。彼此没有依赖的迭代
可以在不同分支上并行进行。

**只读需要的内容。** 一次会话从以下内容开始：

1. [开发进度](progress.zh-CN.md)、本文件的 [索引](#索引) 和该迭代自己的小节；
2. 路线图中的 [设计原则](roadmap.zh-CN.md#设计原则)；
3. 该迭代 **涉及** 中列出的包，以及 **参考** 中列出的 fib 文档或包。

不需要通读整个仓库、对比文档或其他迭代的小节。

**保持小。** 目标是改动不超过约 1500 行（含测试）。如果迭代比预想的大，就停在一个可用、有测试的位置，
把剩余部分在本文件中拆成新的迭代（`I17b` 等），然后完成当前迭代。

**收尾到位。** 迭代完成的条件是：

- **完成标准** 中的各项都满足；
- `go vet ./...` 和 `go test -race ./...` 通过，新代码有测试；
- 请求路径上没有任何操作阻塞 fib 的 worker（见原则 1）；
- 改动符合 [开发规则](rules.zh-CN.md) 的每一条；
- 在同一个 commit 中把索引里的状态改为 `done`，中英文两个文件都要改；
- 在 [开发进度](progress.zh-CN.md) 及其英文版中记录做了什么、如何验证、与计划的差异，以及下一个迭代；
- 迭代中做出的决定（库的选择、格式等）以简短说明记录在 `docs/decisions/NNN-title.md`。

**会话提示词。** 可以用下面的话开始一次会话：

> 实现 docs/dev-plan.zh-CN.md 中的迭代 IXX。先读 docs/progress.zh-CN.md、docs/dev-plan.zh-CN.md 的索引和该迭代的
> 小节，以及 docs/roadmap.zh-CN.md 中的设计原则，然后只读它列出的包。改动限制在该迭代范围内，补上测试，完成后在
> 两个 dev-plan 文件中标记为 done，并记录到两个 progress 文件中。

**fib。** fit 依赖已发布版本的 `github.com/lesismal/fib`。在 fib 中发现的缺口写进
[fib-api-requests](fib-api-requests.zh-CN.md) 交给维护者，由维护者修改 fib 并发布；fit 随后升级到该版本（[开发规则](rules.zh-CN.md)
第 15 条）。在此之前，迭代先做不依赖该缺口的部分，临时措施记录在 progress 中。

## 包结构

I01 建立、后续迭代逐步填充的结构。名字可以调整，但每个迭代应该尽量少地涉及这些包。

| 包 | 内容 |
| --- | --- |
| `cmd/fit` | 可执行文件；命令在 `cli` 中：`run`、`validate`、`adapt`、`schema`、`build`、`version`、`hash-password`、`store` |
| `config` | 原生配置类型、严格的 JSON 加载 |
| `schema` | 由配置类型和已注册模块生成的 JSON Schema |
| `module` | 模块注册表和核心接口 |
| `modules/standard` | 引入 fit 自带的所有模块 |
| `runtime` | 把配置编译成不可变的图、原子替换、图的引用计数 |
| `listener` | 基于 fib binding 的监听、协议入口、监听差量 |
| `router` | 路由器和匹配器 |
| `handler/...` | 终端 handler：`respond`、`static`、`proxy`、`fastcgi` |
| `middleware/...` | 重写、头处理、限制、认证、压缩、缓存、重试、熔断 |
| `upstream` | 上游连接池、负载均衡、被动和主动健康检查 |
| `l4` | TCP 和 UDP 代理、SNI 分流、PROXY protocol |
| `p2p/stun` | listener 上的 STUN 服务器，走 UDP 和 TCP；I86 时其消息改用 fib 的 `webrtc/stun`（决定 029） |
| `webrtc/turn` | TURN 服务器角色 `webrtc.turn`，基于 fib 的 `webrtc/turn/turnserver`（决定 029）；P2P 的中继 |
| `webrtc/media` | 角色 `webrtc.media` 和 `webrtc.proxy`，以及 handler `whip`、`whep` 和 `webrtc_proxy`（决定 029） |
| `p2p` | 连通候选对上的隧道、信令、配置里的 P2P 应用 |
| `vpn/packet` | IP 头与 overlay 路由表 |
| `vpn/tun` | Linux、macOS、FreeBSD 和 Windows 上的 TUN 设备 |
| `vpn/relay` | 中转协议、服务端，以及挑选传输的客户端 |
| `vpn` | 节点、协调者、配置里的 VPN 应用 |
| `tlsmgr` | 证书管理、存储、ACME、OCSP |
| `admin` | Admin API 和配置版本；控制台（决定 027） |
| `admin/store` | 加密的 store，fitstore 格式（决定 027） |
| `login` | 登录、会话、CSRF 令牌和锁定，供控制台和角色的页面使用 |
| `app`、`app/console` | 配置中的角色：注册表与管理器、经 `Host.Edit` 写回，以及角色的页面（决定 028） |
| `llm/...` | llm 的角色：`hub`、`node`、`proxy` |
| `tunnel/...` | tunnel 的角色：`hub`、`node`、`proxy` |
| `provider/...` | file、docker、kubernetes、consul、etcd |
| `observe` | 访问日志、指标、追踪 |
| `logs` | fit 自身的日志：logger、级别，以及日志和访问日志写出、切割与保留的文件（I155–I156） |
| `plugin/...` | 编译期模块和 WASM |
| `sync/...` | 数据同步：记录、流水线、检查点、变换，以及每个连接器及其线协议（`sync/mysql`、`sync/postgres`、`sync/mongo`、`sync/kafka` 等；决定 036） |
| `adapter/...` | 配置适配器：友好格式、nginx |
| `bench` | 基准测试框架，nginx、Caddy、Traefik 和 fit 的配置，测试结果 |
| `test/e2e` | 端到端测试和一致性测试 |
| `internal/fibtest` | 在测试中不经网络运行 fib handler |
| `examples` | 示例配置；已迁到 [fit-examples](https://github.com/lesismal/fit-examples) |

## 索引

状态：`todo`、`doing` 或 `done`。下一个可用的迭代编号是 I223。

| ID | 迭代 | 依赖 | 状态 |
| --- | --- | --- | --- |
| **M0** | | | |
| I01 | 仓库骨架与 CI | — | done |
| I02 | 配置类型 v0 与严格加载 | I01 | done |
| I03 | JSON Schema 与 `fit validate` | I02 | done |
| I04 | 模块注册表与核心接口 | I02 | done |
| I05 | 运行时图与原子替换 | I04 | done |
| I06 | HTTP 入口与 `fit run` | I05 | done |
| I07 | 基准框架：吞吐 | I06 | done |
| I08 | 基准框架：空闲连接内存与基线 | I07 | done |
| **M1** | | | |
| I09 | 从证书文件加载的 TLS 入口 | I06 | done |
| I10 | HTTP/3 入口 | I09 | done |
| I11 | 路由 v1：host 与 path | I06 | done |
| I12 | 静态文件 I | I11 | done |
| I13 | 静态文件 II | I12 | done |
| I14 | 上游连接池 v1（HTTP/1.1） | I05 | done |
| I15 | 反向代理 I：转发与头处理 | I11、I14 | done |
| I16 | 反向代理 II：流式转发与背压 | I15 | done |
| I17 | 反向代理 III：超时、取消、错误 | I16 | done |
| I18 | HTTP/2 上游 | I17 | done |
| I19 | 负载均衡 v1 | I14 | done |
| I20 | 被动健康检查 | I19 | done |
| I21 | 访问日志 | I06 | done |
| I22 | SIGHUP 重载 | I05、I06 | done |
| I23 | M1 一致性测试与基准 | I13、I18、I20、I21、I22 | done |
| **M1 审查修复** | | | |
| R01 | 慢客户端的等待有上限 | I13、I17 | done |
| R02 | 安全的默认上限 | I13、I20 | done |
| R03 | 不等待转发所需的 fib API（在 fib 中） | — | done |
| R04 | 代理下载不等待 | R01、R03 | done |
| R05 | 静态文件不阻塞 | R01、R03 | done |
| R05b | sendfile 发送的文件受节奏控制和上限约束 | R05、A16 | done |
| R06 | 通过 fib 实现连接和排队上限 | R02、R03 | done |
| R07a | fit 自身的热路径开销 | I23 | done |
| R07b | 代理的内存分配 | I23 | done |
| R07c | 依赖 fib 的热路径开销 | R03、R07a、R07b | done |
| R07d | 访问日志的开销 | R07c | done |
| R07e | 借助 fib 降低钩子和文件发送的开销（A16–A18） | R05b、R07d、I34 | done |
| R07f | 剖析代理每个请求的 CPU（A20–A22） | R07e、I35 | done |
| **M2** | | | |
| I24 | 配置版本与 Admin API 骨架 | I22 | done |
| I25 | Admin API：局部修改、ETag、回滚 | I24 | done |
| I26 | 监听差量与旧图排空 | I24 | done |
| I27 | 文件 provider | I24 | done |
| I28 | 证书管理与存储；ACME 选型 | I09 | done |
| I29 | ACME HTTP-01 与续期 | I28 | done |
| I30 | TLS-ALPN-01、OCSP stapling | I29 | done |
| I30b | 默认 HTTPS | I30 | done |
| I30c | ACME 续期信息（ARI） | I29 | done |
| I31 | 按需 TLS | I30 | done |
| I32 | 主动健康检查 | I20 | done |
| I33 | 负载均衡 v2 | I19 | done |
| I34 | Prometheus 指标 | I23 | done |
| I35 | M2 退出测试 | I25、I26、I27、I30b、I31、I32、I33、I34 | done |
| I35b | 监视证书文件 | I27、I28、I34 | done |
| I35c | 丢弃闲置的按需名字 | I31、I35b | done |
| **M3** | | | |
| I36 | 匹配器 v2 与优先级 | I11 | done |
| I37 | 表达式匹配器 | I36 | done |
| I38 | 重写、跳转与头处理 | I36 | done |
| I39 | 限流、并发与大小限制 | I36 | done |
| I40 | Basic 认证与客户端 mTLS | I36 | done |
| I41 | ForwardAuth | I40 | done |
| I42 | JWT 与 JWKS | I40 | done |
| I43 | 重试与按路由超时 | I17 | done |
| I44 | 熔断 | I43 | done |
| I45 | 流量拆分：加权、镜像、故障转移 | I19 | done |
| I46 | 压缩 | I36 | done |
| I47 | WebSocket 代理 | I17 | done |
| I48 | HTTP/2、HTTP/3 入口上的 WebSocket | I47、I10 | done |
| I49 | gRPC 代理 | I18 | done |
| I50 | TCP 代理 | I05 | done |
| I51 | UDP 代理 | I50 | done |
| I52 | SNI 分流与 PROXY protocol | I50 | done |
| I53 | 一个端口多种协议；SOCKS 与 CONNECT | I52 | done |
| I53b | CONNECT 正向代理 | I53 | done |
| I54 | 上游 TLS | I18 | done |
| I55 | M3 退出测试 | I37–I54 | done |
| **M4** | | | |
| I56 | Provider 框架与生效配置 | I27 | done |
| I57 | Docker provider | I56 | done |
| I58 | Kubernetes Gateway API I：HTTPRoute | I56 | done |
| I59 | Kubernetes Gateway API II：其他路由与状态 | I58 | done |
| I60 | Kubernetes Ingress | I58 | done |
| I61 | Gateway API 一致性测试 | I59 | done |
| I62 | Consul 与 etcd provider | I56 | done |
| I63 | 共享证书存储 | I30 | done |
| I64 | 仪表盘 | I56 | done |
| I65 | OpenTelemetry | I34 | done |
| I66 | 打包与示例 | I57 | done |
| **M5** | | | |
| I67 | 缓存 I：HTTP 语义与内存层 | I23 | done |
| I68 | 缓存 II：磁盘层 | I67 | done |
| I69 | 缓存 III：缓存锁、stale、清除 | I68、I25 | done |
| I70 | `fit build` 与编译期模块 | I04 | done |
| I71 | WASM I：运行时与插件池 | I36 | done |
| I72 | WASM II：Proxy-Wasm 子集 | I71 | done |
| I73 | 友好配置格式 | I03 | done |
| I74 | nginx 配置转换器 | I73 | done |
| I75 | FastCGI | I17 | done |
| **1.0** | | | |
| I76 | 持续 fuzz | I55 | done |
| I77 | 配置与 Admin API v1 冻结 | I69、I74 | done |
| I78 | 参考文档与迁移指南 | I77 | done |
| I79 | Caddyfile 转换器、带注释的 JSON | I74 | done |
| I80 | fit 自己的 .conf 格式；多文件配置 | I79 | done |
| I81 | WASM III：Proxy-Wasm 的响应回调 | I72 | done |
| I82 | fit 自身的日志：logger 与级别；转换 nginx 和 Caddy 的日志配置 | I21、I79 | done |
| I83 | fit 的日志采用 fib 的格式；logger 可单独设置；访问日志的格式 | I82 | done |
| **P2P** | | | |
| I84 | STUN 消息与 STUN 服务器 | I51 | done |
| I85 | 朝某地址打开 UDP 对端（A49，在 fib 中） | — | done |
| I86 | STUN 客户端与 NAT 行为，基于 fib 的 `webrtc/stun` | I84, I85, I108 | todo |
| I87 | TURN 服务器 `webrtc.turn`，基于 fib 的 `turnserver` | I85, I108 | done |
| I88 | TURN 客户端，fib 的 `turn.Client` | I86, I87 | todo |
| I89 | ICE agent，fib 的 `webrtc/ice` | I86, I88 | todo |
| I90 | QUIC DATAGRAM 帧（A50，在 fib 中） | — | todo |
| I91 | 连通候选对上的隧道 | I89, I90 | todo |
| I92 | 信令与配置里的 P2P 应用 | I87, I91 | todo |
| I93 | P2P 互通与收尾测试 | I92 | todo |
| **VPN** | | | |
| I94 | IP 包与 overlay 路由表 | — | done |
| I95 | 包设备描述符挂到 poller（A51，在 fib 中） | — | todo |
| I96 | Linux、macOS 和 FreeBSD 上的 TUN 设备 | I94, I95 | todo |
| I97 | Windows 上基于 Wintun 的 TUN 设备 | I94 | todo |
| I98 | 中转帧与中转服务端（WebSocket） | I94 | todo |
| I99 | QUIC 和 HTTP/2 上的中转；按顺序选择传输并回退 | I90, I98, S04 | todo |
| I100 | 协调者与 netmap | I92, I98 | todo |
| I101 | 节点：TUN 与对端之间，直连或中转 | I91, I96, I99, I100 | todo |
| I102 | VPN 配置、Admin API 与 dashboard 页面 | I101 | todo |
| I103 | VPN 端到端与收尾测试 | I97, I102 | todo |
| **控制台** | | | |
| I104 | 加密保存的配置（`admin.store`）；带登录、由 schema 生成编辑器的控制台 | I77 | done |
| **Agent 与 tunnel** | | | |
| I105 | fit 配置中的角色：`app`、`app/console`、`login`，经 `EditMember` 写回 | I104 | done |
| I106 | tunnel 的角色，来自 fibhub：`tunnel/server`、`tunnel/node`、`tunnel/client` | I105 | done |
| I107 | agent 的角色，来自 agenthub，全部基于 fib：`agent/hub`、`agent/node`、`agent/client` | I105 | done |
| **WebRTC** | | | |
| I108 | 决定 029：fit 的 WebRTC 基于 fib；fib `80c7f76`；按实际实现修正 A49；A55–A58 | I85 | done |
| I109 | WHIP 和 WHEP：共享 `webrtc.Mux`（`webrtc.media`）上的 SFU | I108 | done |
| I110 | 代理到媒体服务器的 WHIP 和 WHEP；按 ICE ufrag 路由 UDP | I109 | done |
| I111 | 基于 fib A55–A59 的 WebRTC：经 TCP 和 TLS 的 TURN、上限、事件、原地修改与排空；代理的源地址和端口 | I87, I110 | done |
| I112 | agent 的模型接口：OpenAI 及其兼容接口、Anthropic、Gemini、Azure OpenAI、Ollama 与 Cohere 的全部无状态推理端点 | I107 | done |
| I113 | LLM 角色的节点列表、连接与流量：hub 的节点同步给 proxy，每条连接的字节数与延迟，每条路由的调用，每个角色页面上的链路图，以及角色其余设置在页面上的修改 | I112 | done |
| **媒体** | | | |
| I115 | 媒体认证：命名的 `media_auth` 策略（token、用户、签名 token、公开动作、流模式），`mediaauth` 包，`whip`、`whep` 和 `webrtc_proxy` 的 `auth`，`fit media-token` | I109, I110 | done |
| I116 | 流媒体角色：fib 的 `media/hub`，带 RTMP、RTSP 和 SRT 监听，以及 `httpflv`、`hls`、`stream_whip` 和 `stream_whep` 处理器，各有 `auth` | I115 | done |
| I117 | 媒体页面：`media_pages`，登录、推流、播放和房间通话，可开关麦克风和摄像头，以及有上限的房间 API | I115 | done |
| I118 | 媒体页面的房间聊天：经房间 API 收发的文字消息和小文件、页面开关，以及成员加入时的麦克风和摄像头 | I117 | done |
| I119 | 从一个路由签发媒体 token：`media_tokens` 处理器、用策略 `keys` 密钥环中的一个密钥批量签发、流名的前缀限制，以及通过轮换一个密钥或全部密钥来吊销 | I115 | done |
| I120 | workbench 的配置页面：平铺的模块菜单、每个页面按 `.conf`/`.yaml`/`.json` 原文编辑并经应用路径校验，以及控制台用户在页面中修改密码并保存在 store 中 | I114 | done |
| I121 | 运行中的服务与它所依据的配置之间的一致性：不一致与说明两类差异及可选修复、一致性页面、`fit check`，以及 `GET /config/check`/`POST /config/reconcile` | I120 | done |
| I122 | workbench 的功能标签与监听端口：流媒体、媒体认证、WebRTC、隧道与 LLM 配置各自成页，每个监听端口列出它所提供的功能并链接到各自的页面 | I121 | done |
| I123 | workbench 的引擎菜单：一个一级条目，每个监听端口是其功能的子菜单，按用户要求的顺序（处理器、反向/正向代理、缓存与中间件、流媒体、WebRTC、四层/SOCKS/TLS/ACME/默认 HTTPS），端口行带该端口的功能标签 | I122 | done |
| **AI 助手** | | | |
| I124 | 决定 031；`admin.assistant` 及其设置页面；各 API 的模型客户端，流式、带工具调用，基于 fib 的异步 HTTP 客户端；密钥保存在 store 中 | I120 | done |
| I125 | workbench 的对话面板与只读工具：工具注册表、SSE 对话、页面上下文、脱敏、fit 日志环 | I124 | done |
| I126 | 提议：修改类工具、diff 与应用流程、按用户的级别与审计日志 | I125 | done |
| I127 | Skill：格式、内置集合、Skills 页面、导入导出、`/name` | I126 | done |
| I128 | 在工作处询问 AI：保存错误、一致性、引擎菜单、表单字段、指标与角色页面上的入口 | I127 | done |
| I129 | 迁移与审查：`migrate` 与 `security-review` 端到端，在固定配置集上评估 | I127 | done |
| I130 | 基于工具注册表的 MCP 服务器：工具、作为 prompts 的 skill、作为 resources 的配置与 schema；按用户签发、最高 `propose` 的 token；提议只在 workbench 中应用 | I126, I127 | done |
| **媒体库** | | | |
| I131 | 决定 032；fib A64，非分片 MP4 文件的读取器：经 `io.ReaderAt` 读取样本表，按时间和关键帧查找样本，内存有上限 | I116 | done |
| I132 | 媒体库：扫描 `media_library` 的根目录生成条目目录（轨道、编码、多码率、字幕、海报），在有上限的文件池上探测，Admin API | I131 | done |
| I133 | 点播：处理器 `vod`，从样本表按关键帧切分的 HLS（fMP4 或 MPEG-TS）、多码率、音轨与字幕组、Range、`auth` | I132 | done |
| I134 | 频道：媒体库条目作为直播流推入 hub，按顺序、循环或排期；`push` 到外部 RTMP 和 SRT 服务器 | I132 | done |
| I135 | 会过期的播放 URL：绑定资源、地址或会话的 `url_signing`，授权传递到分片，`media_tokens` 的 `POST <prefix>/urls`，`max_sessions` | I119, I133 | done |
| I136 | 加密分片：AES-128 HLS，密钥只提供给持有该资源授权的请求 | I135 | done |
| I137 | 影视站端到端：示例、`media_pages` 的媒体库与播放页面、文档 | I134, I135 | done |
| I138 | workbench 的媒体库页面：菜单条目、配置页面、带预览的媒体库浏览、时间线上的频道、推流目标、测试 URL、密钥与当前会话、指标 | I135, I123 | done |
| I139 | 在 workbench 中管理媒体文件：断点续传上传、重命名、移动、删除与 sidecar，有上限并需确认 | I138 | done |
| **集群管理** | | | |
| I140 | 决定 033；成员所需：`GET /node`、`PUT`/`PATCH /config` 试运行、程序可用的登录、失败登录退避；配置页的校验按钮 | I121 | done |
| I141 | 管理节点：`admin.cluster`，成员及其凭据保存在 store 中（控制台密码或 Admin API token，TLS 固定），有上限的状态轮询 | I140 | done |
| I142 | workbench 的集群菜单：概览、节点和节点详情页；`docs/cluster.md` | I141 | done |
| I143 | 从管理节点打开成员自己的页面：节点切换器，用保存的会话转发其 API | I142 | done |
| I144 | 按标签或名字的资源组，对多个成员的批量操作（重载、回滚、检查、清缓存）及逐成员结果；资源组页面 | I142 | done |
| I145 | 模板：组内成员共享的配置路径，带变量、同步状态，冲突在成员上标出；模板页面 | I144 | done |
| I146 | 发布：逐成员 diff 和试运行，一次全部、滚动或金丝雀，健康门槛，失败回滚；发布页面 | I145 | done |
| I147 | 漂移与告警：以发布修复漂移，告警规则发送到 webhook；漂移与告警页面 | I146 | done |
| I148 | 管理节点的用户及各组角色，审计日志；用户与审计页面 | I146 | done |
| I149 | NAT 后的成员主动连到管理节点，用一次性 token 注册；通过注册添加 | I141 | done |
| I150 | 跨成员的指标、日志与证书，CLI 的 `fit cluster`，助手的集群工具；对应页面 | I147, I125 | done |
| **AI 助手（续）** | | | |
| I152 | 助手的媒体库工具与 skill：编目、频道、会话，以及以提议方式进行的重新扫描、密钥轮换与文件修改；`vod-setup`、`channel-scheduler`、`library-health`、`playback-troubleshoot` | I127, I138 | done |
| I153 | 跨集群的助手：读取成员状态、模板与发布，以发布计划提出修改，检查管理节点的角色；`fleet-health`、`rollout-planner`、`cluster-drift` | I127, I146, I148 | done |
| I154 | 助手在页面上的修改：`edit` 级别，以及把修改作为未保存草稿写进当前页面编辑器与表单的页面工具，可撤销，从不提交——**保存并应用** 仍由人完成 | I125, I126, I120 | done |
| **日志** | | | |
| I155 | 决定 034；文件写入器：有上限的队列与自己的写 goroutine，`log` 的 `time_zone`（默认本地，可设 UTC 或 IANA 时区），按大小和时间切割，按时间、数量和总大小保留，压缩，以及 `log` 的文件输出及其 `rotate` 配置 | I21, I82, I83 | done |
| I156 | 访问日志的文件走同一个写入器：`access_log.rotate` 与 `time_zone`，`log` 与访问日志对同一路径共用一个写入器，先写尽缓冲再切割，以及配置拒绝同一路径的两组设置 | I155 | done |
| I157 | workbench 的日志页面：每个输出、写向它的 logger 与格式、磁盘上的文件及其大小和时间、切割与保留表单、带预览和确认的立即切割与立即清理，以及警告 | I155, I156, I120 | done |
| **性能** | | | |
| I158 | 让 fib v1.7.1 的 HTTP/2 请求检查开销尽可能低：fit 的 `fixed` 和 `static` 在 h2c 上回到与 fib `5c0e5b9` 噪声内的水平（2026-11-05 为 −2.4% 和 −1.7%），检查全部保留 | — | done |
| **Agent 与隧道（续）** | | | |
| I159 | 为隧道与 LLM 功能的 mesh 派发 token：监听方生成、列出、设置过期与吊销 token，任何角色都可用 token 代替共享密码，每个 token 是独立凭据，可设有效期；对端无需知道长期密码，没有对所有部署都通用的猜测表，也不会因为知道某个角色的密码就能冒充另一个成员 | I106, I113 | done |
| I160 | 隧道与 LLM mesh 的 TLS（2026-11-06 审查所得）：链路的证书选项与地址分开传递，地址中的任何内容都不能覆盖 pin；非 loopback 时拒绝 `quic_insecure`；没有 store 时自签证书在重启后保留；LLM 监听的证书文件变化时重新加载 | I159 | done |
| I161 | Admin API 与控制台的未认证连接，以及 Windows 与大小写不敏感文件系统上的文件名（2026-11-06 审查所得）：写与读同样受限，为已认证客户端保留连接位；fastcgi 像 static 一样拒绝反斜杠；换了大小写的扩展名不会以源码形式返回 | — | done |
| I162 | 一个地址，一个服务器：配置校验拒绝在同一地址和传输上监听的两处配置，跨监听器、Admin API、控制台和所有角色，错误中写明两者；每个绑定在绑定时检查没有其他套接字占着它的地址；fib 的 TURN 中继不再共用端口范围内的同一端口（A70） | — | done |
| **数据同步** | | | |
| I163 | 决定 036；`sync`：记录模型、流水线（源、变换、目标，`stream` 或 `once`）、有上限的队列、批次、推进水位线且绝不拆开事务的确认、检查点文件、重试与 `on_error` | I04 | done |
| I164 | 内置变换：`filter`、`route`、`fields`、`key`、`cast`、`mask`、`envelope` | I163 | done |
| I165 | 同步的 WASM 插件：变换、编解码器，以及基于 fit 的 HTTP 客户端与定时器的源和目标连接器；guest SDK | I163 | done |
| I165b | 同步 WASM 插件的其余部分：在压测主机上测量，需要时加入记录的二进制形式；测试中构建并运行 TinyGo 和 Rust 插件；fit-examples 中的示例 | I165 | done |
| I166 | 文件作为源（跟踪与通配，跟随切割与截断，多行合并）与目标（经 I155 的文件写入器） | I163, I155 | done |
| I167 | 像 DataX 一样的批处理作业：分块并行快照、按块续传，`fit sync run --once`，调度 | I163 | done |
| I168 | Elasticsearch 与 OpenSearch 作为目标：按 `_id` 的 `_bulk`、逐条错误、遇 429 退避 | I163 | done |
| I169 | Elasticsearch 与 OpenSearch 作为源：point in time 快照，之后按游标轮询 | I168 | done |
| I170 | ClickHouse 作为目标（去重令牌、`ReplacingMergeTree`）与源（按游标轮询） | I163 | done |
| I171 | HTTP 作为源（`sync_ingest`，目标收到记录后才应答）与目标 | I163 | done |
| I172 | MySQL 客户端：握手、TLS、两种密码插件、文本查询与预处理语句 | I163 | done |
| I173 | MySQL binlog：事件、校验和与所有列类型，含 MySQL 与 MariaDB | I172 | done |
| I174 | MySQL 与 MariaDB 作为源：从检查点的 GTID 集合以副本身份读取，列名、DDL，每个 XID 一个检查点 | I173 | done |
| I175 | 不加锁、按键分块的 MySQL 快照，及其到 binlog 的交接 | I174 | done |
| I176 | MySQL 与 TiDB 作为目标：按键 upsert 与 delete，保持源事务 | I172 | done |
| I177 | PostgreSQL 客户端：启动、TLS、SCRAM-SHA-256、两种查询协议、`COPY`、复制命令 | I163 | done |
| I178 | PostgreSQL 作为源：从 slot 读取 `pgoutput`，LSN 只在目标确认后才确认 | I177 | done |
| I179 | 取自 slot 导出快照的 PostgreSQL 快照，精确交接 | I178 | done |
| I180 | PostgreSQL 作为目标：`ON CONFLICT` upsert、delete，快照用 `COPY` | I177 | done |
| I181 | MongoDB 客户端：BSON、`OP_MSG`、主节点发现、SCRAM-SHA-256、TLS | I163 | done |
| I182 | MongoDB 作为源：从 resume token 读取 change stream，按 `_id` 的快照 | I181 | done |
| I183 | MongoDB 作为目标：按 `_id` 批量 upsert 与 delete | I181 | done |
| I183b | 源与目标共用一套 MongoDB 连接参数 | I182, I183 | done |
| I184 | Kafka 客户端：metadata、produce、fetch、offset、所有压缩、SASL、TLS | I163 | done |
| I185 | Kafka 作为源与目标，offset 存在 fit 的检查点中；经 TiCDC 的 TiDB | I184, I176 | done |
| I186 | Redis 作为源（stream）与目标（stream 或键） | I163 | done |
| I187 | S3 及兼容存储作为目标（滚动的对象）与源（按键顺序的对象） | I163 | done |
| I188 | Admin API 与 `fit sync`：状态、暂停、恢复、检查点、连接测试、试运行 | I163 | done |
| I188b | 早先连接器的连接测试 | I188 | done |
| I189 | 指标、日志与死信，可从 Admin API 重放 | I188 | done |
| I190 | workbench 的 Sync 页面，以及助手的同步工具 | I188, I189, I123 | done |
| I191 | 写入 MySQL、TiDB 和 PostgreSQL 时恰好一次：检查点写在目标的事务中 | I176, I180 | done |
| I192 | CI 中的互通与恢复矩阵 | I176, I180, I183, I185 | done |
| I193 | 对比 Filebeat、Canal、Debezium 和 DataX 的基准测试 | I192 | done |
| I194 | 同步的文档与示例 | I190 | done |
| I211 | RabbitMQ 作为源（队列与 stream 队列，目标拿到后确认）与目标（publisher confirms） | I163 | done |
| I212 | NATS JetStream 作为源（持久 pull consumer，序号存入检查点）与目标（`Nats-Msg-Id` 去重） | I163 | done |
| I213 | Apache Pulsar 作为源（按消息 ID 的 reader，或订阅）与目标（序列号与代理去重） | I163 | done |
| I214 | MQTT 3.1.1 与 5 作为源（持久会话，QoS 1/2 在目标拿到后确认）与目标 | I163 | done |
| I215 | SQL Server 客户端：TDS 7.4、TLS、SQL 认证、带类型参数的 RPC、批量导入、取消 | I163 | done |
| I216 | SQL Server 作为源：按 LSN 读 CDC 变更表，不加锁的快照在其 LSN 处衔接 | I215 | done |
| I217 | SQL Server 作为目标：`MERGE` upsert、删除、批量导入，检查点写在目标中实现精确一次 | I215, I191 | done |
| I218 | Doris 与 StarRocks 作为目标（带 label 的 Stream Load、删除标记）与源（按游标轮询） | I172 | done |
| I219 | Loki 与 OpenTelemetry 日志作为目标（push API、OTLP/HTTP），文件到它们并对比 Promtail | I166 | done |
| I220 | InfluxDB（line protocol）与 TDengine（schemaless）作为目标；MQTT 到 InfluxDB 端到端 | I214 | done |
| I221 | Milvus 与 Qdrant 作为目标，按键 upsert 与删除，配 `embed` 变换 | I163 | done |
| I222 | 第二批进入恢复矩阵、CI 互通分组、基准测试与文档 | I211–I221 | done |
| **访问控制** | | | |
| I195 | 客户端地址的允许与拒绝名单（白名单与黑名单）：全局 `access`，以及每个 listener、路由、service 和角色各自的 `access`，局部先决定；能在 accept 时决定的经 fib 的 `Binding.Filter`（A73）拒绝，不能的在请求时或选定 service 后拒绝；名单文件变化时重新应用 | — | done |
| **BitTorrent** | | | |
| I196 | fib 为 BitTorrent 客户端补 A75–A79：存储与哈希不上 fib worker、快速续传、运行时可改的限制、按地址拒绝 peer、客户端跑在调用者的引擎上 | — | done |
| I197 | `bittorrent.client` 角色：配置、跑在共享引擎上、`data_dir` 下的状态文件、恢复与 reload（决定 037） | I196 | done |
| I198 | 添加任务（磁力链、文件、URL、`watch_dir`）、分类与媒体库重扫、排队、磁盘检查 | I197 | done |
| I199 | `alt_speed` 调度、单任务限制、角色的 `access`、`access` 读 `.p2p` 和 `.dat` 黑名单（所有模块可用） | I197 | done |
| I200 | `bt_stream`：经 HTTP Range 边下边播，允许按磁力链临时添加 | I197 | done |
| I201 | `bittorrent.tracker` 角色：HTTP 的 `bt_tracker`、UDP、私有 passkey、白名单、统计 | I196 | done |
| I202 | 把目录发布成带 web seed 的 hybrid 种子，跟随磁盘变化 | I197 | done |
| I203 | Admin API、事件 SSE 流、`fit bt`、指标 | I198 | done |
| I204 | 工作台的 BitTorrent 页面、`schema-zh.js`、AI 助手函数 | I203 | done |
| I205 | 与 libtorrent、anacrolix 互通，故障测试，基准主机上的基准，文档与示例 | I198–I204 | done |
| I206 | （第二阶段）集群汇总各成员的任务；经 BitTorrent 把大文件分发给成员 | I203 | done |
| I207 | （第二阶段）集群共享的 tracker Store | I201 | done |
| I208 | （第二阶段）RSS 自动下载 | I198 | done |
| I209 | 各角色用上 fib A80–A86：发布只哈希一次、生成可按 context 停止，列出公开 tracker 的种子并原地修改其上限，客户端事件队列有界 | I201、I202、I203 | done |
| I210 | 客户端角色用上 fib A87–A88：`/trackers` 显示各 tracker 的 announce 状态、peer 来源与 Web 种子状态，就地重新校验 | I203、I204 | done |

## M0 基础

### I01 仓库骨架与 CI

- **做什么**：`github.com/lesismal/fit` 的 `go.mod`，Go 版本与 fib 一致；按 [包结构](#包结构) 建立 M0
  需要的空包；`cmd/fit version`；Makefile；CI 运行 `go vet`、staticcheck 和 `go test -race`；指向文档的简短
  README。
- **完成标准**：`main` 上 CI 通过；`fit version` 打印自身版本和 fib 版本。
- **涉及**：仓库根目录、`cmd/fit`。

### I02 配置类型 v0 与严格加载

- **做什么**：原生配置的 Go 类型：`listeners`（地址、协议）、`routes`（匹配条件、handler 链）、按模块名引用的
  `handlers` 及其原始 JSON 参数。加载时拒绝未知字段，错误带 JSON 路径。暂时只有 `respond` handler
  （状态码、头、body）。
- **完成标准**：表驱动测试覆盖合法配置、未知字段、类型错误、缺失字段，每种都有准确的错误路径。
- **涉及**：`config`。

### I03 JSON Schema 与 `fit validate`

- **做什么**：原生配置的 JSON Schema，发布在 `schema` 下；一个保证 schema 与 Go 类型同步的测试；
  `fit validate -c file`，出错时以非零码退出并给出可读的错误。
- **完成标准**：类型中新增字段而 schema 未加（或反之）时，同步测试失败。
- **涉及**：`config`、`cmd/fit`。

### I04 模块注册表与核心接口

- **做什么**：handler、middleware、matcher、service、upstream 的接口；按命名空间名字（`http.handlers.respond`）
  注册的注册表，provision 阶段解码并校验参数；注册 `respond`。
- **完成标准**：未知模块名和错误参数在 provision 阶段失败，并带配置路径。
- **涉及**：`module`、`handler/respond`。
- **参考**：[对比文档](comparison.zh-CN.md#caddy) 中对 Caddy 模块模型的总结。

### I05 运行时图与原子替换

- **做什么**：把配置编译成不可变的图（listener → route → 已 provision 的模块）；用原子指针保存当前图；
  对图做引用计数，连接固定在它开始时的图上，最后一个引用释放时调用模块的清理。
- **完成标准**：在 race 检测下，测试在 goroutine 持有引用时替换图，并验证每张图的清理恰好执行一次。
- **涉及**：`runtime`。

### I06 HTTP 入口与 `fit run`

- **做什么**：启动 fib engine，按图绑定监听，提供 HTTP/1.1 和 h2c，分发到路由的 handler 链；`fit run -c file`；
  收到 SIGINT、SIGTERM 时优雅退出。
- **完成标准**：e2e 测试用一个 `respond` 路由启动 `fit run`，通过 HTTP/1.1 和 h2c 拿到响应；退出时等待正在处理
  的请求。
- **涉及**：`listener`、`cmd/fit`、`test/e2e`。
- **参考**：fib `docs/http1.md`、`docs/http2.md`、`engine.go`、`binding.go`。

### I07 基准框架：吞吐

- **做什么**：`bench/` 中提供 nginx、Caddy、Traefik 和 fit 的等价配置（固定响应、小静态文件、代理到固定后端）；
  运行 wrk 和 h2load 的脚本，结果以 JSON 记录，包含硬件和版本信息。
- **完成标准**：在 Linux 主机上一条命令就能让四个服务器跑完所有场景。
- **涉及**：`bench`。

### I08 基准框架：空闲连接内存与基线

- **做什么**：一个打开 N 个空闲连接（HTTP/1.1 keep-alive、TLS、WebSocket）并读取服务端 RSS 的 Go 工具；
  加入基准框架；提交 nginx、Caddy、Traefik 的基线结果。
- **完成标准**：存在 `bench/results/baseline-*.json`，`bench/` 中的 README 说明如何复现。
- **涉及**：`bench`。

## M1 反向代理核心

### I09 从证书文件加载的 TLS 入口

- **做什么**：基于 `fib/tls` 的 TLS 监听，证书从文件加载并按 SNI 选择；ALPN 支持 h2 和 http/1.1；合理的 TLS
  默认值（TLS 1.2 及以上、现代密码套件）。
- **完成标准**：e2e 测试覆盖两个使用不同证书的域名，分别走 HTTP/1.1 和 h2。
- **涉及**：`listener`、`tlsmgr`（仅静态证书）。
- **参考**：fib `tls`、`GetCertificate` 和握手池。

### I10 HTTP/3 入口

- **做什么**：基于 fib `http3` 的 HTTP/3 监听，与 TLS 共用证书来源；同一 host 的 TCP 监听上通告 `Alt-Svc`。
- **完成标准**：e2e 测试通过 h3 访问同一路由，并在 h2 上看到 `Alt-Svc`。
- **涉及**：`listener`。
- **参考**：fib `docs/http3.md`。

### I11 路由 v1：host 与 path

- **做什么**：带 host（精确和通配）、path（精确和前缀）匹配器的路由器；先按显式优先级，再按最具体优先；
  无匹配时返回 404。
- **完成标准**：表驱动测试覆盖排序和平局；热路径上查找不分配内存。
- **涉及**：`router`。

### I12 静态文件 I

- **做什么**：基于 `http.FileCache` 的 `static` handler：根目录、索引文件、ETag、`Last-Modified`、条件请求、
  Range 请求、MIME 类型；拒绝路径穿越。
- **完成标准**：测试覆盖条件请求、Range 请求和穿越尝试。
- **涉及**：`handler/static`。
- **参考**：fib `http/file_cache*.go`。

### I13 静态文件 II

- **做什么**：按 `Accept-Encoding` 选择预压缩文件（`.br`、`.zst`、`.gz`）；类似 `try_files` 的回退（用于单页应用）；
  大文件走 sendfile；目录浏览可选，默认关闭。
- **完成标准**：测试覆盖编码协商和回退；基准中有使用它的场景。
- **涉及**：`handler/static`、`bench`。

### I14 上游连接池 v1（HTTP/1.1）

- **做什么**：上游配置（地址、最大连接数、空闲超时、连接超时）；每个上游一个基于 fib 异步 HTTP/1.x 客户端的
  连接池；连接复用和空闲淘汰。
- **完成标准**：针对测试后端的测试展示复用、上限和淘汰。
- **涉及**：`upstream`。
- **参考**：fib `http/client.go`。

### I15 反向代理 I：转发与头处理

- **做什么**：`proxy` handler，把 method、path、query 和头转发到一个上游，并把响应写回；删除 hop-by-hop 头；
  设置 `X-Forwarded-For/Proto/Host` 和 `Forwarded`；由可信代理列表决定是否保留传入的转发头。
- **完成标准**：头处理测试通过；小 body 往返正确。
- **涉及**：`handler/proxy`。

### I16 反向代理 II：流式转发与背压

- **做什么**：请求体和响应体双向流式转发，不缓冲整个 body；背压联动（另一侧待发字节超过水位时，对领先的一侧
  `HoldReads`）。
- **完成标准**：慢客户端和慢上游的测试在传输数百 MB 时内存有界；chunked 和 content-length 两种 body 都正确。
- **涉及**：`handler/proxy`。
- **参考**：fib `Connection.HoldReads`、`backpressure_test.go`、`http/body*.go`。

### I17 反向代理 III：超时、取消、错误

- **做什么**：连接、响应头、空闲超时；客户端断开时通过 `c.Context()` 取消上游请求；失败映射为 502 和 504；
  转发 trailer。
- **完成标准**：测试覆盖每种超时、body 传输中途客户端断开，以及 trailer。
- **涉及**：`handler/proxy`。
- **参考**：fib `http/context.go`。

### I18 HTTP/2 上游

- **做什么**：基于 fib HTTP/2 客户端的 h2（TLS）和 h2c 上游，在池化连接上多路复用流，并限制每连接的流数。
- **完成标准**：I15–I17 的代理测试在 h2 和 h2c 后端上同样通过。
- **涉及**：`upstream`、`handler/proxy`。
- **参考**：fib `http/h2_client.go`。

### I19 负载均衡 v1

- **做什么**：选择接口，以及轮询、平滑加权轮询、最少连接；每个上游可有多个地址。
- **完成标准**：分布测试在容差范围内；选择过程不分配内存。
- **涉及**：`upstream`。

### I20 被动健康检查

- **做什么**：在时间窗口内失败 N 次即标记地址不可用，期间跳过它，冷却后重试；类似 nginx 的
  `max_fails` / `fail_timeout`。
- **完成标准**：使用会失败的后端的测试，展示它被摘除并重新加入。
- **涉及**：`upstream`。

### I21 访问日志

- **做什么**：结构化 JSON 访问日志，包含常用字段（时间、客户端、host、method、path、状态码、大小、耗时、上游）；
  带缓冲，写入不占用事件循环；输出到 stdout 或文件。
- **完成标准**：基准显示日志不阻塞 worker；字段有测试。
- **涉及**：`observe`。

### I22 SIGHUP 重载

- **做什么**：收到 SIGHUP 时加载并编译配置文件；成功则替换图；地址不变的监听保留，其余增删；失败则保留正在
  运行的图并记录错误。
- **完成标准**：e2e 测试在负载下重载，没有失败的请求；错误的配置不影响旧配置继续服务。
- **涉及**：`cmd/fit`、`runtime`、`listener`。
- **参考**：fib `AddBinding`、`RemoveBinding`、`ShutdownBinding`。

### I23 M1 一致性测试与基准

- **做什么**：在 h2 入口上跑 h2spec；在 `test/e2e` 中建立代理一致性测试；用基准框架跑静态文件和代理场景，以及
  1 万、10 万连接下的空闲内存；修复发现的问题，或拆成新的迭代。
- **完成标准**：结果提交到 `bench/results/`，并有一份与基线对比的简短报告。
- **涉及**：`test/e2e`、`bench`。

## M1 审查修复

2026-10-19 按 [开发规则](rules.zh-CN.md) 审查 M0、M1 时，发现了违反规则的代码。下面的迭代分三步修复：R01、R02 只改
fit，在 M2 之前完成；R03 是 fib 中的工作，需求见 [fit 需要 fib 提供的 API](fib-api-requests.zh-CN.md)；R04–R06 和
R07c 使用这些 API。R07a、R07b 是 R07（热路径）中不需要 fib 的部分。

审查发现的问题，以及各自在哪个迭代修复：

| 问题 | 规则 | 位置 | 修复迭代 |
| --- | --- | --- | --- |
| 代理下载在读取上游连接的 worker 上于 `Context.Write` 中等待，且没有时间上限。一个不读取的客户端会卡住共享同一 h2c 上游连接的所有请求，几个就能耗尽 `max_conns`；两者都已复现 | 1、8、10 | `handler/proxy/exchange.go`，`onBody` | R01 加上限，R04 根治 |
| 静态文件在 worker 上 stat、读取、打开文件，并通过没有时间上限的等待式写入发送；目录列表一次读取整个目录 | 1、4、10、11 | `handler/static` | R01、R02 加上限，R05 根治 |
| listener 没有设置 `WriteTimeout` 或停滞超时 | 10 | `listener/listener.go`，`httpConfig` 和 `http3Config` | R01 |
| 不写 `max_conns` 的上游没有连接上限；等待连接的请求数量没有上限；响应没有总超时 | 10、11 | `upstream/upstream.go`、`config` | R02、R06 |
| 引擎既不限制排队输出总量，也不限制连接数 | 11 | `listener/listener.go`，`Start` | R02、R06 |
| 带 trailer 的上传占用一个阻塞在管道读取上的 goroutine | 1、4 | `handler/proxy/exchange.go`，`trailerPipe` | R04 |
| 每行访问日志一次加锁；每个请求为日志条目复制一次请求并创建 context；每个请求扫描一遍图中的 listener；加权轮询每次选择都加锁 | 12 | `observe`、`listener`、`upstream/balance.go` | R07a、R07c |
| 没有测试验证不读取的客户端不影响其他请求；静态文件没有慢客户端测试 | 13 | 测试 | R01、R05 |

M0、M1 的其余部分符合规则：配置、模块注册表、运行时图、路由器、`respond`、代理的上传方向，以及访问日志的写出。

### R01 慢客户端的等待有上限

- **做什么**：新增 listener 设置 `write_stall_timeout`（默认 30 秒），限制每次等待客户端取走输出的时长。代理的下载路径
  和静态 handler 在写入前调用 `Context.SetWriteDeadline(now + stall)`（最多每秒刷新一次，以节省系统调用）。这样，不读取
  的客户端会失去它的响应（HTTP/1 连接被关闭，HTTP/2、HTTP/3 stream 被重置），等待它的 goroutine 被放开，代理随后放弃
  上游响应。加入复现审查结论的回归测试：共享 h2c 上游连接上的不读取客户端，以及针对 `max_conns` 的多个不读取客户端，
  分别在 HTTP/1.1、h2c、h3 上测试。
- **完成标准**：在这些测试中，卡住的客户端存在时其他请求照常得到处理，卡住的请求在停滞超时内结束；流式测试仍然通过。
- **涉及**：`config`、`schema`、`listener`、`handler/proxy`、`handler/static`、`test/e2e`。
- **参考**：fib `Context.SetWriteDeadline`、`Config.WriteTimeout`；[fib-api-requests](fib-api-requests.zh-CN.md#证据)
  中的证据。

### R02 安全的默认上限

- **做什么**：为审查发现的每个缺失上限设置默认值，并且都可以在配置中修改：每个上游地址的 `max_conns`（默认 256）；
  每个上游的 `max_pending`，即等待连接的请求数（默认 1024；超过时立即返回 503，R06 之前由 fit 自己计数）；每个上游的
  `response_timeout`，即响应的总时长上限（默认 1 小时）；顶层 `limits` 对象，包括引擎的 `max_pending_bytes`（256MB）
  和 listener 的 `max_header_bytes`、`max_body_bytes`（即 fib 的 1MB 和 16MB，现在显式可见）；目录列表的
  `browse_max_entries`（10000）。所有默认值都写进 schema。
- **完成标准**：每个上限都有一个触达它的测试；schema 和示例展示这些默认值。
- **涉及**：`config`、`schema`、`upstream`、`handler/proxy`、`handler/static`、`listener`。

### R03 不等待转发所需的 fib API

- **做什么**：维护者在 fib 中实现 [fib-api-requests](fib-api-requests.zh-CN.md) 中的 A1、A2、A3，A4–A7 和
  A11–A13 按 fib 的安排进行，并发布新的 fib 版本；fit 升级到该版本，行为不变。
- **完成标准**：fit 在新的 fib 版本上构建并通过测试；fib-api-requests 中注明每个 API 是已提供、已调整还是被拒绝。
- **涉及**：fib；fit 中的 `go.mod` 和文档。

### R04 代理下载不等待

- **做什么**：用 `WriteNoWait`、`OnWritable` 和 `ClientResponse.HoldBody` 重写下载路径，与上传路径已在使用的
  `HoldBody`、`OnDrain` 对称；fib 提供 `BodyWriter` 的 trailer（A6）后，用它替换 `trailerPipe`。
- **完成标准**：代理代码中没有任何会等待的写入；把停滞超时调到一小时后 R01 的测试仍然通过，说明不再依赖它；慢客户端只
  拖慢它自己的上游 stream；256MB 流式测试中内存保持有界。
- **涉及**：`handler/proxy`。

### R05 静态文件不阻塞

- **做什么**：基于 fib 的 `http.FileCache` 提供文件，不再每个请求 stat 一次；缓存未命中时的 open、stat、读取和目录列表
  放到 fit 自己的有界文件 I/O 池中执行，并设超时；大文件用 A3 发送。Range 和条件请求保持可用。
- **完成标准**：静态代码不在 worker 上调用任何文件操作或会等待的写入；有慢客户端和不读取客户端的测试，覆盖 HTTP/1.1、
  h2、h3，以及慢磁盘（通过池的测试钩子）；有改动前后的基准对比。
- **涉及**：`handler/static`。

### R05b sendfile 发送的文件受节奏控制和上限约束

- **做什么**：fib 把 `SendFile` 排队的文件计入连接未发送的输出（A16）之后，升级到那个 fib 版本，运行
  `TestStuckClient/http/1.1`；如果 fib 的 `SendFile` 自己限制等待，就把整个范围交给它，而不是分段。
- **完成标准**：普通 HTTP/1 上停止读取经 sendfile 发送的文件的客户端，在停滞超时后被切断，跳过的测试恢复运行。
- **涉及**：`handler/static`、`go.mod`。

### R06 通过 fib 实现连接和排队上限

- **做什么**：引擎的连接数上限（A4），对应配置 `limits.max_conns`；用客户端的排队上限（A5）替换 R02 中 fit 自己的计数。
- **完成标准**：测试能触达这两个上限；R02 的临时计数代码已删除。
- **涉及**：`listener`、`upstream`、`config`。

### R07a fit 自身的热路径开销

R07 拆成三部分：fit 自己能做的（R07a、R07b）和需要 fib 的（R07c）。

- **做什么**（以 I23 的数据为依据）：访问日志改用分片缓冲，每个分片有自己的锁；日志条目和它的 context 合并为一次分配；
  每个 listener 找自己的路由时不必扫描整张图；平滑加权轮询改为无锁实现，即预先计算的调度序列加原子下标；为分发器和
  多核写日志增加基准测试。
- **完成标准**：改动前后每请求的耗时和分配次数记录在开发进度中，压测主机上吞吐没有下降。
- **涉及**：`observe`、`listener`、`upstream`。

### R07b 代理的内存分配

- **做什么**：减少 fit 自身代码在每个代理请求上的分配：发往上游的请求、它的 URL 和转发字段的值放进 exchange 的同一次
  分配；客户端的 IPv4 地址不再格式化；整体到达的响应以一次分配读出；增加一个使用原始客户端的代理基准测试。fib 客户端
  的分配写进 fib-api-requests。
- **完成标准**：同 R07a，针对代理场景。
- **涉及**：`handler/proxy`。

### R07c 依赖 fib 的热路径开销

- **做什么**：日志条目放进 Context 的值槽（A7），不再用 `r.WithContext`；整体到达的响应不复制即写出（A11）；客户端
  每个请求的定时器和 context（A12），并把代理的响应头超时定时器交给 fib 的客户端；记日志的响应不复制响应头（A13）；
  然后在压测主机上研究 I23 留下的代理在 HTTP/1.1 上只有 nginx 83% 的吞吐和 p99 长尾。
- **完成标准**：同 R07a；代理每请求的分配次数在改动前后记录在开发进度中。
- **涉及**：`observe`、`listener`、`handler/proxy`、`upstream`。

### R07d 访问日志的开销

- **做什么**：R07c 之后，开着访问日志时 h2c 固定响应比不开慢约 30%。在压测主机上做性能分析，去掉 fit 为每行日志付出的
  开销：对已经规范的键再做规范化的请求头查找、按浮点数格式化的时长、每行都格式化一遍的日期、逐字节扫描的 JSON 字符串、
  日志分片上的锁争用；属于 fib 的写进 fib-api-requests。
- **完成标准**：同 R07a，针对开着访问日志的固定响应，HTTP/1.1 和 h2c。
- **涉及**：`observe`。

### R07e 借助 fib 降低钩子和文件发送的开销（A16–A18）

在 M2 报告之后，经维护者同意，由 fit 的会话直接在 fib 中完成。

- **做什么**：fib 中，Context 为下一个请求保留 response writer，值槽不加锁（A17）；`Context.Status`（A18）；
  `SendFileStall`（A16）。fit 中，对在返回前应答的 handler 不注册 OnFinish 钩子即可计数；文件的范围整段交给
  `SendFileStall` 发送；然后切换到 fib 新版本。
- **完成标准**：在基准机上对比开启访问日志、开启指标时 h2c 上 fixed 和 static 的前后数据；整段发送时
  `TestStuckClient` 在所有协议上通过。
- **涉及**：fib 的 `http`；fit 的 `module`、`runtime`、`listener`、`handler/respond`、`handler/static`。

### R07f 剖析代理每个请求的 CPU（A20–A22）

M2 报告显示，fit 的代理在 HTTP/1.1 上是 nginx 的 86%，每个请求的 CPU 是 nginx 的 1.5 倍。

- **做什么**：在基准机上对比 nginx 剖析代理；去掉属于 fit 的开销，属于 fib 的连同证据写进 fib-api-requests。
- **完成标准**：每个请求的 CPU 按内核、fib、fit 和运行时分开；fit 的部分用微基准和压测工具做前后对比；fib 的请求写好。
- **涉及**：`handler/proxy`；`docs/fib-api-requests`。

## M2 动态配置与 TLS

### I24 配置版本与 Admin API 骨架

- **做什么**：保存已应用配置的历史和版本号；Admin API 默认在 localhost 或 Unix socket 上，可选 bearer token：
  `GET /config`、`POST /load`；与 SIGHUP 走相同的编译和替换路径。
- **完成标准**：e2e 测试通过 API 加载配置并读回。
- **涉及**：`admin`、`runtime`。

### I25 Admin API：局部修改、ETag、回滚

- **做什么**：按 JSON pointer 的 `GET/PUT/PATCH/DELETE /config/<path>`；用 ETag 和 `If-Match` 防止更新丢失；
  `POST /rollback/<version>`。
- **完成标准**：测试覆盖并发写入、校验失败的修改和回滚。
- **涉及**：`admin`。

### I26 监听差量与旧图排空

- **做什么**：对比新旧图的监听：新增用 `AddBinding`，移除的用 `ShutdownBinding` 排空；长连接留在旧图上，直到
  结束或排空超时。
- **完成标准**：抖动测试（流量持续时替换数千次，包括类似 WebSocket 的长连接）不丢任何东西。
- **涉及**：`listener`、`runtime`。

### I27 文件 provider

- **做什么**：监听配置文件，对变化去抖，通过 Admin API 的应用路径生效；出错时报告但不中止。
- **完成标准**：测试修改文件后看到变化生效；错误的修改被报告并忽略。
- **涉及**：`provider/file`。

### I28 证书管理与存储；ACME 选型

- **做什么**：`GetCertificate` 背后的证书管理接口（静态证书迁移到它下面）；带锁的存储接口及文件系统实现；
  一份在 CertMagic、lego 和自研 ACME 客户端之间做出选择的决定说明。
- **完成标准**：静态证书通过证书管理工作；`docs/decisions` 中有 ACME 选型的决定说明。
- **涉及**：`tlsmgr`、`docs/decisions`。

### I29 ACME HTTP-01 与续期

- **做什么**：用 HTTP-01 验证申请证书；在 HTTP 监听上应答验证；到期前带随机抖动和退避续期；保存账户密钥和
  证书。
- **完成标准**：针对 Pebble（ACME 测试服务器）的测试能签发和续期证书。
- **涉及**：`tlsmgr`、`listener`。

### I30 TLS-ALPN-01、OCSP stapling

- **做什么**：TLS-ALPN-01 验证，以及可选择的验证方式；OCSP stapling 及刷新，对文件中的证书和申请到的证书都适用。
- **完成标准**：测试只通过 TLS-ALPN-01 从 Pebble 取得证书，由 fib 的 TLS 应答；OCSP 响应在 TLS 1.3 和 1.2 下都被
  staple，会刷新，过期后不再使用。
- **涉及**：`tlsmgr`、`runtime`。

为使每部分都在一次迭代的目标规模之内，I30 拆成三部分：其余部分是 I30b 和 I30c。

### I30b 默认 HTTPS

- **做什么**：配置中的每个域名默认获得托管证书和 HTTP→HTTPS 跳转，可关闭。
- **完成标准**：只写了域名的配置能在 Pebble 下提供 HTTPS；针对 Let's Encrypt 测试环境的测试有文档，可手动运行。
- **涉及**：`config`、`runtime`、`listener`。

### I30c ACME 续期信息（ARI）

- **做什么**：按 CA 建议的续期时间窗（RFC 9773）续期，按其 Retry-After 再次查询；CA 没有时按有效期续期；决定 007
  把 ARI 留给 fit 自己实现。
- **完成标准**：针对 Pebble 和一个建议时间窗已过的 CA 的测试都在时间窗内续期。
- **涉及**：`tlsmgr`。

### I31 按需 TLS

- **做什么**：对未知域名在握手时签发证书，由 "ask" URL 和速率限制把关；握手在握手池上等待，不占用 worker。
- **完成标准**：测试覆盖允许、拒绝和被限速的域名。
- **涉及**：`tlsmgr`。

### I32 主动健康检查

- **做什么**：由控制面执行 HTTP 和 TCP 探测，可配置间隔、超时、阈值和随机抖动；结果更新数据面读取的上游状态。
- **完成标准**：测试展示后端在配置的时间窗口内被摘除并重新加入。
- **涉及**：`upstream`。

### I33 负载均衡 v2

- **做什么**：随机、P2C、按 IP/header/cookie/URI 的一致性哈希、基于 cookie 的粘性会话。
- **完成标准**：分布和粘性测试通过；新增地址时哈希保持稳定。
- **涉及**：`upstream`。

### I34 Prometheus 指标

- **做什么**：由 fit 自己输出 Prometheus 文本格式（除非决定说明另有结论，否则不引入重量级客户端库）；按 router、
  service、upstream 的计数器和直方图；连接和连接池 gauge；重载和证书到期指标；在 Admin 监听上提供。
- **完成标准**：测试抓取该接口；基准显示每请求的开销。
- **涉及**：`observe`、`admin`。

### I35 M2 退出测试

- **做什么**：把路线图 M2 退出标准中的抖动测试、自动 HTTPS 测试和健康检查测试集中放入 `test/e2e`，在 CI 中运行。
- **完成标准**：三项在 CI 中全部通过。
- **涉及**：`test/e2e`。

### I35b 监视证书文件

I09 把它留给 I28，I28 又留给 I29–I30，但都没有做；在复查 M2 时发现。

- **做什么**：运行中的配置所加载的证书或私钥文件变化时（例如 fit 之外的续期工具更新了它们），重新应用该配置，
  像文件 provider 一样轮询；加载失败的证书对不影响正在使用的证书。证书到期指标也列出按需获取的证书。
- **完成标准**：端到端测试中，续期后的证书对无需手动重载即被使用；先写入证书、尚未写入私钥时会报告错误且不做任何改变。
- **涉及**：`tlsmgr`、`runtime`、`admin`、`cmd/fit`。

### I35c 丢弃闲置的按需名字

I31 留下的问题：按需加入的名字一直保留到重启；在复查 M2 时发现。

- **做什么**：按需加入的名字在 `acme.on_demand` 的 `idle_timeout` 内没有任何握手请求时将其丢弃（默认值安全），避免
  `max_names` 被永久占满；它的证书留在存储中，证书到期指标不再列出它。
- **完成标准**：由时间驱动的测试表明闲置的名字被丢弃，不再提供、列出或计数，之后可从存储重新加入；期间被请求过的名字保留。
- **涉及**：`config`、`tlsmgr`。

## M3 网关能力

### I36 匹配器 v2 与优先级

- **做什么**：method、header、query、来源 IP 和 CIDR、正则 path 匹配器；显式的路由优先级，以及有文档说明的平局规则。
- **完成标准**：表驱动测试覆盖每个匹配器和排序。
- **涉及**：`router`。

### I37 表达式匹配器

- **做什么**：用 `&&`、`||`、`!` 和括号组合匹配器的小型表达式语言（`Host("a.com") && PathPrefix("/api")`），
  在 provision 时编译。
- **完成标准**：解析器测试和 fuzz；求值不分配内存。
- **涉及**：`router`。

### I38 重写、跳转与头处理

- **做什么**：路径重写（去前缀、正则替换）、跳转，以及请求和响应头的设置、添加、删除中间件。
- **完成标准**：每个中间件都有表驱动测试。
- **涉及**：`middleware/rewrite`、`middleware/headers`。

### I39 限流、并发与大小限制

- **做什么**：按客户端 IP、header 或路由为键的令牌桶限流；并发（in-flight）限制；请求体大小限制；返回 429 和 413。
- **完成标准**：测试和基准；大量键时限流器内存有界。
- **涉及**：`middleware/limit`。
- **参考**：fib `middleware/limiter`。

### I40 Basic 认证与客户端 mTLS

- **做什么**：使用哈希密码的 Basic 认证；TLS 监听上的客户端证书校验，证书主体可供后续中间件使用。
- **完成标准**：测试覆盖正确和错误的凭据与证书。
- **涉及**：`middleware/auth`、`listener`。

### I41 ForwardAuth

- **做什么**：向认证服务发送子请求，按其应答继续、拒绝或复制头；请求等待应答期间不占用 worker。
- **完成标准**：测试覆盖允许、拒绝，以及负载下认证服务很慢时 worker 不被耗尽。
- **涉及**：`middleware/auth`。

### I42 JWT 与 JWKS

- **做什么**：校验 JWT（签名、过期、audience、issuer）；JWKS 由控制面拉取和刷新；claims 可供后续中间件使用。
- **完成标准**：测试覆盖密钥轮换和无效 token。
- **涉及**：`middleware/auth`。

### I43 重试与按路由超时

- **做什么**：对幂等请求和连接失败重试，带重试预算；按路由的总超时。
- **完成标准**：测试显示重试在预算处停止，且绝不重发已经发出 body 的非幂等请求。
- **涉及**：`handler/proxy`、`middleware/retry`。

### I44 熔断

- **做什么**：按上游的错误率和延迟熔断，有关闭、打开、半开三种状态。
- **完成标准**：状态转换测试。
- **涉及**：`middleware/breaker`。

### I45 流量拆分：加权、镜像、故障转移

- **做什么**：加权 service；把一部分流量镜像到第二个 service（丢弃其响应）；故障时转移到备用 service。
- **完成标准**：每项都有测试；镜像不拖慢主请求。
- **涉及**：`upstream`。

### I46 压缩

- **做什么**：gzip、zstd、brotli 响应压缩，按 `Accept-Encoding` 协商，跳过很小和已压缩的响应；关于 zstd 和 brotli
  库的决定说明。
- **完成标准**：测试和基准。
- **涉及**：`middleware/compress`、`docs/decisions`。
- **参考**：fib `middleware/compress`。

### I47 WebSocket 代理

- **做什么**：在代理中识别升级请求，升级上游连接，再用 `Context.Upgrade` 和 `Tunnel` 连接两侧，双向背压。
- **完成标准**：经过 fit 的 echo 和大消息测试；测量空闲 WebSocket 的内存。
- **涉及**：`handler/proxy`。
- **参考**：fib `http/tunnel.go`、`websocket`。

### I48 HTTP/2、HTTP/3 入口上的 WebSocket

- **做什么**：在 h2 和 h3 入口上接受扩展 CONNECT 的 WebSocket，并代理到 HTTP/1.1 上游。
- **完成标准**：h2 和 h3 上的 e2e 测试。
- **涉及**：`handler/proxy`。
- **参考**：fib `http/h2_tunnel.go`、`http3/tunnel.go`。

### I49 gRPC 代理

- **做什么**：经 h2 和 h2c 上游代理 gRPC：trailer、`grpc-timeout` 转为 deadline、双向流式、代理出错时返回 gRPC
  状态。
- **完成标准**：一元、服务端流、客户端流、双向流测试通过。
- **涉及**：`handler/proxy`。
- **参考**：fib `grpc`。

### I50 TCP 代理

- **做什么**：配置中的 TCP 监听和 TCP service；双向转发字节并背压；连接和空闲超时；复用 `upstream` 的负载均衡。
- **完成标准**：吞吐测试和慢对端测试。
- **涉及**：`l4`、`config`。

### I51 UDP 代理

- **做什么**：UDP 监听，按客户端地址建立会话，带空闲超时和数量限制。
- **完成标准**：多客户端测试；会话会过期。
- **涉及**：`l4`。

### I52 SNI 分流与 PROXY protocol

- **做什么**：读取 ClientHello，不终结 TLS 按 SNI 路由；监听上接受、向上游发送 PROXY protocol v1/v2。
- **完成标准**：测试覆盖按 SNI 透传和两个版本的 PROXY protocol。
- **涉及**：`l4`、`listener`。

### I53 一个端口多种协议；SOCKS 与 CONNECT

- **做什么**：监听选项，用 fib `mux` 在一个端口上区分 HTTP、TLS（终结或透传）、SOCKS 和自定义协议；带访问规则的
  SOCKS 和 CONNECT 正向代理 handler。
- **完成标准**：e2e 测试中一个端口同时服务 HTTP/1.1、h2、TLS 透传和 SOCKS。
- **涉及**：`listener`、`l4`。
- **参考**：fib `mux`、`multiproto`、`socks`、`http/connect_proxy.go`。
- **拆分**：CONNECT handler 移到 I53b，因为 fib 的 `ConnectProxy` 要先改成不在 worker 上等拨号。

### I53b CONNECT 正向代理

- **做什么**：`forward_proxy` 路由 handler，基于 fib 的 `ConnectProxy`（改为不等拨号，A37；加上空闲超时，A36），在 HTTP/1.1、
  HTTP/2 和 HTTP/3 上提供 CONNECT，访问规则与 SOCKS 相同。
- **完成标准**：测试经它在 HTTP/1.1 和 h2c 上建立隧道，拒绝规则不允许的客户端和目标（主机名也按解析出的地址判断），并关闭空闲隧道。
- **涉及**：`handler/forward`、`l4`。

### I54 上游 TLS

- **做什么**：到上游的 TLS，支持自定义 CA、客户端证书（mTLS）和 SNI 覆盖。
- **完成标准**：针对要求客户端证书的后端的测试。
- **涉及**：`upstream`。

### I55 M3 退出测试

- **做什么**：把 M3 退出标准放入 `test/e2e` 和基准框架；一项检查（基准或测试钩子）确认没有中间件阻塞 worker。
- **完成标准**：全部在 CI 中通过；结果已提交。
- **涉及**：`test/e2e`、`bench`。

## M4 平台集成

### I56 Provider 框架与生效配置

- **做什么**：provider 接口；按成文的优先级规则合并多个 provider 的配置；记录每部分的来源；Admin API 提供
  `GET /config/effective`。
- **完成标准**：测试合并文件和模拟 provider，并展示来源。
- **涉及**：`provider`、`admin`。

### I57 Docker provider

- **做什么**：通过 Unix socket 监听 Docker API（事件和容器），把标签转成路由和 service；关于使用 Docker SDK 还是
  直接 HTTP 的决定说明。
- **完成标准**：Docker Compose 的 e2e 测试能随容器启停更新。
- **涉及**：`provider/docker`。

### I58 Kubernetes Gateway API I：HTTPRoute

- **做什么**：Kubernetes 客户端（关于 client-go 还是更轻量方案的决定说明）；监听 GatewayClass、Gateway 和
  HTTPRoute，并转换成配置。
- **完成标准**：使用模拟 API server 的测试，以及基于 kind 的 e2e 测试。
- **涉及**：`provider/kubernetes`。

### I59 Kubernetes Gateway API II：其他路由与状态

- **做什么**：GRPCRoute、TLSRoute、TCPRoute、UDPRoute；HTTPS listener，证书来自 Secret（I58 留下的）；把状态条件写回资源。
- **完成标准**：每种路由类型都有测试；状态反映被接受和被拒绝的路由。
- **涉及**：`provider/kubernetes`。

### I60 Kubernetes Ingress

- **做什么**：转换 Ingress 资源和一小组注解。
- **完成标准**：每项支持的 Ingress 功能都有测试。
- **涉及**：`provider/kubernetes`。

### I61 Gateway API 一致性测试

- **做什么**：针对核心 profile 运行 Gateway API 一致性测试；修复失败项，或拆成新的迭代。
- **完成标准**：声明的 profile 在 CI 中通过。
- **涉及**：`test/e2e`、`provider/kubernetes`。

### I62 Consul 与 etcd provider

- **做什么**：从 Consul catalog 获取服务；从 Consul KV 和 etcd 获取配置。
- **完成标准**：针对本地 Consul 和 etcd 的测试。
- **涉及**：`provider/consul`、`provider/etcd`。

### I63 共享证书存储

- **做什么**：基于 Redis、etcd、Consul 的存储实现，带分布式锁，由一个实例签发、其他实例复用。
- **完成标准**：在 Pebble 测试中两个实例共享签发。
- **涉及**：`tlsmgr`。

### I64 仪表盘

- **做什么**：嵌入二进制的只读 Web 界面，读取 Admin API：router、service、上游健康、证书、配置版本。
- **完成标准**：页面能针对运行中的 fit 渲染；没有任何写操作。
- **涉及**：`admin`。

### I65 OpenTelemetry

- **做什么**：W3C trace context 传播到上游的链路追踪；span 通过 OTLP 导出；指标也可通过 OTLP 导出；关于 OTel SDK
  及其依赖体积的决定说明。
- **完成标准**：针对 OTLP collector 的测试；基准显示采样关闭和开启时的开销。
- **涉及**：`observe`。

### I66 打包与示例

- **做什么**：容器镜像、Helm chart、systemd 单元，以及 Docker Compose 和 Kubernetes 示例。
- **完成标准**：示例按文档可以运行。
- **涉及**：`deploy`、`examples`。

## M5 缓存与扩展

### I67 缓存 I：HTTP 语义与内存层

- **做什么**：遵循 RFC 9111 规则的缓存中间件（可存储性、新鲜度、`Vary`、用校验器重新验证、`Age`）；有容量上限、
  类 LRU 淘汰的内存层。
- **完成标准**：每条缓存规则都有测试；命中的基准。
- **涉及**：`middleware/cache`。

### I68 缓存 II：磁盘层

- **做什么**：索引在内存中的磁盘层；磁盘读写在独立的池上进行；条目从磁盘提升到内存。
- **完成标准**：测试在重启后重建索引；worker 从不等待磁盘。
- **涉及**：`middleware/cache`。

### I69 缓存 III：缓存锁、stale、清除

- **做什么**：缓存锁让一次未命中只回源一次；stale-while-revalidate 和 stale-if-error；通过 Admin API 按 key 和
  tag 清除。
- **完成标准**：击穿测试只产生一次上游请求；在基准框架中与 nginx `proxy_cache` 比较命中率场景。
- **涉及**：`middleware/cache`、`admin`、`bench`。

### I70 `fit build` 与编译期模块

- **做什么**：`fit build --with module@version`，生成引入额外模块的 main 包并构建二进制，类似 xcaddy；有文档的
  模块模板。
- **完成标准**：一个外部示例模块能编译进 fit 并运行。
- **涉及**：`cmd/fit`、`plugin/build`。

### I71 WASM I：运行时与插件池

- **做什么**：用纯 Go 运行时加载 WASM 模块（决定说明，默认 wazero）；调用在独立的池上执行；读取和修改头的最小 ABI。
- **完成标准**：修改头的插件可用；每请求的开销经过测量且有上限。
- **涉及**：`plugin/wasm`、`docs/decisions`。

### I72 WASM II：Proxy-Wasm 子集

- **做什么**：如果 I71 的数据允许，实现常见过滤器需要的 Proxy-Wasm HTTP 过滤器调用；否则记录原因。
- **完成标准**：一个现有的 Proxy-Wasm 过滤器无需修改即可运行，或决定说明给出替代方案。
- **涉及**：`plugin/wasm`。

### I73 友好配置格式

- **做什么**：从人类友好格式（决定说明：YAML、类似 Caddyfile 的语法或其他）到原生 JSON 的适配器；`fit adapt`。
- **完成标准**：示例配置的往返测试。
- **涉及**：`adapter`、`cmd/fit`。

### I74 nginx 配置转换器

- **做什么**：解析 nginx 配置，把常见子集（server、location、proxy_pass、root、return、rewrite、upstream、
  ssl_certificate、limit_req）转换成原生 JSON；列出所有跳过的部分。
- **完成标准**：示例配置能转换，每条跳过的指令都被报告。
- **涉及**：`adapter/nginx`。

### I75 FastCGI

- **做什么**：基于 fib 连接、带连接池的 FastCGI 客户端；带常用 PHP 参数和 split path info 的 `fastcgi` handler。
- **完成标准**：针对 PHP-FPM 的 e2e 测试。
- **涉及**：`handler/fastcgi`。

## 1.0

### I76 持续 fuzz

- **做什么**：为配置加载、表达式解析器、nginx 解析器、代理的头处理和 PROXY protocol 解析器编写 fuzz 目标；在 CI
  中定期运行。
- **完成标准**：fuzz 目标在 CI 中运行，语料已提交。
- **涉及**：各包中的 fuzz 测试。

### I77 配置与 Admin API v1 冻结

- **做什么**：审查 schema 和 API 中值得保留的命名和结构；标记为 v1；撰写兼容性策略；对改名的部分加弃用警告。
- **完成标准**：存在 `docs/compatibility.md`，schema 带有版本号。
- **涉及**：`config`、`admin`、`docs`。

### I78 参考文档与迁移指南

- **做什么**：快速上手指南；由 schema 生成的配置参考；从 nginx、Caddy、Traefik 迁移的指南，中英文各一份。
- **完成标准**：文档能构建，每个配置字段都有描述。
- **涉及**：`docs`。

### I79 Caddyfile 转换器、带注释的 JSON

- **做什么**：像 I74 转换 nginx 配置那样，把 Caddy 的 Caddyfile 的常用子集转换为原生 JSON，按行列出所有跳过的部分；
  原生 JSON 允许 `//` 和 `/* */` 注释及末尾逗号，错误仍指向原来的行和列（[决定 020](decisions/020-caddyfile-and-jsonc.zh-CN.md)）。
- **完成标准**：示例 Caddyfile 能转换，每个跳过的指令都被报告，转换结果能端到端提供服务；带注释的 JSON 能从文件和
  Admin API 加载。
- **涉及**：`adapter/caddyfile`、`adapter`、`config`、`admin`、`cli`。

### I80 fit 自己的 .conf 格式；多文件配置

- **做什么**：fit 自己的块格式 `.conf`，按配置的类型和每个模块的参数逐字段读取，使它能表达 JSON 和 YAML 能表达的一切，
  用 `site` 把一组域名的路由写在一起，并能由 JSON 写回；配置可以从多个文件和目录读取并合并，错误指向所在文件；Caddyfile
  和 nginx 转换器保留为迁移工具，不再出现在配置文档中（[决定 021](decisions/021-fit-format-and-config-files.zh-CN.md)）。
- **完成标准**：每个模板、示例和参考示例都有能与其 JSON 一样加载的 `.conf` 写法；由三种格式的文件组成的目录能运行，加入、
  修改或移除文件时会重新加载；文档和模板讲的是 `.conf`、YAML 和 JSON。
- **涉及**：`adapter/fitconf`、`adapter`、`config`、`provider/file`、`cli`、`schema`、`docs`、`templates`、`examples`。

### I81 WASM III：Proxy-Wasm 的响应回调

- **做什么**：在插件的池上实现 Proxy-Wasm 的响应回调 `proxy_on_response_headers`、`_body` 和 `_trailers`：响应头由 fib 的 A41
  压住，body 经 fib 新增的异步响应体过滤器（A46）流式处理；跟随的响应数和持有的 body 都有上限，调用受插件的超时限制
  （[决定 023](decisions/023-proxy-wasm-response.zh-CN.md)）。
- **完成标准**：Proxy-Wasm 过滤器能读取和修改响应的头、body 和 trailer，SDK 的 `http_headers` 和 `http_body` 示例原样运行，
  覆盖 HTTP/1.1、h2c、h2 和 HTTP/3；不读取的客户端或慢插件不拖住其他请求。
- **涉及**：`plugin/wasm`、fib 的 `http`。

### I82 fit 自身的日志：logger 与级别

- **做什么**：顶层的 `log`，记录 fit 关于自身的消息，按 logger 分类（server、admin、tls、provider、telemetry、access_log、
  plugin、fastcgi，以及 engine：fib 和写入 Go 的 `log`、`log/slog` 的一切），每个 logger 有最低级别 debug、info、warn 或
  error，重载即生效；以文本（与原来相同）或 JSON 写出，按级别分到 stdout 和 stderr，或写到同一个输出。访问日志的 `level`
  可以只记录以 4xx 和 5xx、或只以 5xx 应答的请求。`fit adapt` 转换 nginx 的 `error_log` 和 `access_log`，以及 Caddy 的全局
  `log`、`debug` 和站点的 `log { level }`。
- **完成标准**：fit 写出的每条消息都有 logger 和级别；不写 `log` 的配置写出与原来相同的各行，按级别写到 stdout 或 stderr；级别在重载时改变；
  转换器的测试覆盖每条指令；访问日志的微基准测试显示没有开销。
- **涉及**：`logs`（新）、`config`、`cli`、`admin`、`runtime`、`observe`、`tlsmgr`、`provider`、`plugin/wasm`、
  `handler/fastcgi`、`adapter/nginx`、`adapter/caddyfile`、`schema`、`docs`。

### I83 fit 的日志采用 fib 的格式；logger 可单独设置；访问日志的格式

- **做什么**：fit 的日志改用 fib 的 `log` 包（fib 的 `log/log.go`）写出，由它的 handler 按 fib 自己的格式写行，使 fit 与 fib
  的日志一致：`log.format` 为 `line`（默认）、`text`（logfmt）或 `json`，fib 的消息通过 `fiblog.SetLogger` 归入 `engine`
  logger。`log.loggers` 让某个 logger 拥有自己的 `level`、`output` 和 `format`，覆盖 `log` 的设置。`access_log.format` 以
  `json`（默认，字段不变）、`text` 或 `line` 写出每个请求的一行，字段相同。Caddy 的 `format console` 转为 `line`。
- **完成标准**：每个 logger 以自己的格式写到自己的输出；engine 的消息（含 fib 的）归 engine logger；访问日志的三种格式字段
  相同，客户端发来的内容被引号转义；logger 的输出或格式改变时重载给出警告；JSON 访问日志的基准测试显示没有开销。
- **涉及**：`logs`、`config`、`cli`、`admin`、`observe`、`adapter/caddyfile`、`adapter/nginx`、`schema`、`test/e2e`、
  `docs`；fib 的 `log`（fib `185658f1`、`33a039f1`）。

路线图 1.0 中的外部安全审计不是一个迭代：它在 I77 完成后于代码之外安排。

## P2P 与 NAT 穿透

两台在 NAT 后面的主机通过 ICE 打出的洞直接相连，打不出洞时经由 TURN 中继，在它们之间承载 TCP 和 UDP 服务。设计见
[决定 025](decisions/025-p2p.zh-CN.md)：STUN、TURN 和 ICE 基于 fib，可与其他实现互通，选中的候选对上跑 QUIC；
[决定 029](decisions/029-webrtc.zh-CN.md) 修订了它，I86–I89 改用 fib 的 `webrtc/stun`、`webrtc/turn` 和 `webrtc/ice`，
不再写 fit 自己的包。

### I84 STUN 消息与 STUN 服务器

- **做**：`p2p/stun` 包：STUN 消息（RFC 8489）解析不分配内存、构造采用追加；ICE 和 TURN 需要的属性，包括
  (XOR-)MAPPED-ADDRESS、USERNAME、REALM、NONCE、ERROR-CODE、SOFTWARE、MESSAGE-INTEGRITY 和 MESSAGE-INTEGRITY-SHA256
  （短期和长期密钥）、FINGERPRINT 和 UNKNOWN-ATTRIBUTES；RFC 7983 判断 STUN 数据报的方法；作为 fib handler 的 STUN 服务器，
  在 UDP 和 TCP 上应答 Binding 请求。
- **完成标准**：RFC 5769 的测试向量能解析并校验通过，消息能往返，畸形消息被拒绝（有模糊测试），UDP 和 TCP 上的 Binding
  请求得到其来源地址的应答，未知的必须理解属性得到 420。
- **涉及**：`p2p/stun`、`docs`。

### I85 朝某地址打开 UDP 对端（A49，在 fib 中）

- **做**：在 fib 中按 fib 的标准实现 [A49](fib-api-requests.zh-CN.md#a49在监听-socket-上朝某地址打开-udp-对端)；fit 升级到
  那个提交。
- **完成标准**：朝某地址打开的对端从监听端口先发出数据并收到应答；它与其他对端一样超时、关闭和计数，在所有后端上都是如此。
- **涉及**：fib 的 engine；`go.mod`。
- **由 I108 完成**：fib 已把 A49 实现为同步的 `96a2331`，fit 随 fib `80c7f76` 采用。

### I86 STUN 客户端与 NAT 行为

- **做**：在 fib UDP 对端上用 `webrtc/stun` 的事务做 Binding 事务（RFC 8489 第 6.2.1 节的 RTO 翻倍、Rc 和 Rm），UDP 和
  TCP 都支持；`p2p/stun` 的消息换成 `webrtc/stun` 的，保留其服务器；服务器反射地址；RFC 5780 的映射和过滤测试，用于日志和 ICE 选择候选；服务器有第二个地址时应答 OTHER-ADDRESS 和
  CHANGE-REQUEST。
- **完成标准**：用假时钟验证事务按计划超时和重传，丢失的应答会重试，NAT 测试在测试中能区分与端点无关和与地址相关的映射。
- **涉及**：`p2p/stun`。

### I87 TURN 服务器

- **做**：基于 fib 的 `webrtc/turn/turnserver`、运行在 listener 引擎上的角色 `webrtc.turn`（[决定 029](decisions/029-webrtc.zh-CN.md)）：
  UDP 地址、realm、静态用户（密码或长期密钥）和 coturn 的 `use-auth-secret`；中继地址、公网地址和端口范围；allocation
  总数和每用户数、每个用户每个方向每秒中继的字节数、生存期；在 fib 对内部地址的拒绝之上允许和拒绝的对端；重载时修改用户、
  secret、对端和带宽无需重启；经 `app` 在 `/metrics` 中的指标；`webrtc` 日志；schema 中的一章。经 TCP 和 TLS 连到服务器、
  permission 和 channel 上限、事件和排空由 fib 提供（A55–A58），在 I111 中接入。
- **完成标准**：fib 的 TURN 客户端经 fit 分配，经 indication 和 channel 与对端双向中继；错误的密码和过期的限时凭据被拒绝；
  配额以 486 和 508 拒绝；超出速率的带宽被丢弃；私有地址的对端除非允许否则被拒绝；重载用户时 allocation 保留，重载地址时
  服务器随之迁移；中途断开的客户端在生存期结束时释放 allocation；在竞态检测下通过。
- **涉及**：`webrtc/turn`、`config`、`app`、`admin`、`schema`、`docs`。

### I88 TURN 客户端

- **做**：从 fib UDP 对端驱动 fib 的 `turn.Client`（分配、刷新、permission 和 channel、401/438 的 nonce 往返），也经 TCP 和
  TLS 驱动（A55，自 `84a67f7` 起在 fib 中）；经 allocation 与对端互发数据，绑定后走 channel。
- **完成标准**：客户端与 fit 的服务器配合工作，使 allocation 存活超过其生存期；过期的 nonce 会重试。
- **涉及**：`p2p`。

### I89 ICE agent

- **做**：在 `p2p` 中使用 fib 的 `webrtc/ice` agent：每个组件一个 socket 上的主机、服务器反射和中继候选；优先级、候选对和检查表（RFC 8445），包括
  controlling 和 controlled 角色及其冲突；常规提名；trickle（RFC 8838）；同意保鲜（RFC 7675）；RFC 8839 语法的候选；把候选对
  的路径作为 `quic.PacketConn`。候选和候选对有上限；定时器用 fib 的。
- **完成标准**：两个 agent 经回环、经每种模拟 NAT 相连，两端都是对称 NAT 时经 TURN 中继相连；失去同意会让候选对失败；
  不阻塞任何 worker。
- **涉及**：`p2p`。

### I90 QUIC DATAGRAM 帧（A50，在 fib 中）

- **做**：在 fib 中实现 [A50](fib-api-requests.zh-CN.md#a50quic-datagram-帧)；fit 升级到那个提交。
- **完成标准**：datagram 双向传输，从不重传，超出对端上限时被拒绝。
- **涉及**：fib 的 `http3/quic`；`go.mod`。

### I91 连通候选对上的隧道

- **做**：ICE 候选对上的 QUIC，双方用证书指纹认证；到服务的 TCP 连接作为双向 stream，UDP 流作为带流 ID 的 DATAGRAM 帧；
  双向背压（规则 8）；stream 和流的上限；从中继到直连的路径变化由 QUIC 的迁移承接。
- **完成标准**：TCP 和 UDP 服务经隧道可达，覆盖慢对端、大传输和中途断开（规则 13），直连和中继都是如此。
- **涉及**：`p2p`。

### I92 信令与配置里的 P2P 应用

- **做**：汇合点 fit 上的信令端点，按名字配对对端，交换它们的凭据、指纹和候选（trickle），每个对端都经过认证；配置里
  监听上的 `stun` 服务器，以及暴露或访问服务、经 `webrtc.turn`（I87）或其他 TURN 服务器中继的 `p2p` 对端；schema、Admin API、指标；各友好格式中的写法。
- **完成标准**：两个在模拟 NAT 后面的 fit 进程经第三个 fit 访问彼此的服务，只靠配置建立，重载时不中断已打开的隧道。
- **涉及**：`p2p`、`config`、`schema`、`listener`、`runtime`、`admin`、`observe`、`test/e2e`、`docs`。

### I93 P2P 互通与收尾测试

- **做**：fit 的 STUN 和 TURN 服务器对 coturn 和 pion 的客户端，fit 的 agent 对 pion 和 libjuice；在压测机上测量直连和中继
  隧道的吞吐和延迟。
- **完成标准**：互通测试在 CI 或一次有记录的运行中通过，测量结果已提交。
- **涉及**：`test/e2e`、`bench`、`docs`。

## VPN：fit 节点组成的网络

节点加入同一张 overlay 网络，各有一个地址和一块 TUN 网卡；IP 包在它们之间经 ICE 打出的洞直连，或经 server 节点中转，
中转按配置的顺序经 QUIC、WebSocket 或 HTTP/2 到达并失败回退，也可以在 web 页面上把节点固定为某一种传输。设计见
[决定 026](decisions/026-vpn.zh-CN.md)：建在 P2P 里程碑之上，IP 包作为端到端连接上的 QUIC DATAGRAM 帧，中转是这条连接的
又一条路径。

### I94 IP 包与 overlay 路由表

- **做**：`vpn/packet` 包：不分配内存地读取 IPv4 和 IPv6 头（版本、长度、协议、源和目的地址），拒绝 TUN 不该交出的包；
  overlay 路由表，由前缀及其所属节点一次建成，最长前缀查找既不分配也不加锁，并能检查对端的包是否来自它的前缀。
- **完成标准**：两种版本的合法与畸形包能区分（有模糊测试），最长前缀优先，建表时拒绝重复和无效的路由，查找不分配内存。
- **涉及**：`vpn/packet`、`docs`。

### I95 包设备描述符挂到 poller（A51，在 fib 中）

- **做**：在 fib 中按 fib 的标准实现 [A51](fib-api-requests.zh-CN.md#a51包设备的描述符在-poller-上读写)，覆盖 epoll 和 kqueue；fit 升级到那个提交。
- **完成标准**：挂到 engine 上的描述符每次 `OnData` 交付一个包、每次 `Send` 发送一个包，设备满时丢弃并计数而不是无界排队，
  随 engine 关闭；在每个 Unix 后端上用类似包设备的描述符（`SOCK_DGRAM` socketpair）测试。
- **涉及**：fib 的 engine；`go.mod`。

### I96 Linux、macOS 和 FreeBSD 上的 TUN 设备

- **做**：`vpn/tun` 包：打开设备（`/dev/net/tun`、`utun`、`/dev/tun`），设置名称、MTU、IPv4 和 IPv6 地址及路由，关闭时撤销；
  包经 A51 收发，macOS 和 FreeBSD 的 4 字节地址族头在收发时去掉和加上；设置工作放在控制面的有界池上。
- **完成标准**：在每个系统上以 root 运行，设备带地址启动，ping 其前缀内的地址能从设备读到，写回的应答能到达 ping；没有权限
  时打开失败并给出清楚的错误。CI 以 root 运行 Linux 测试；macOS 和 FreeBSD 手动运行并记录。
- **涉及**：`vpn/tun`。

### I97 Windows 上基于 Wintun 的 TUN 设备

- **做**：Windows 上的 `vpn/tun`：运行时加载 `wintun.dll`，创建 adapter 和 session，每个设备一个 goroutine 等待接收环并把包
  交给 fib worker，发送由 worker 直接完成；地址和路由用 IP Helper API；Windows 发布包附带该 DLL。
- **完成标准**：以管理员运行，通过与 I96 相同的 ping 测试；缺少 DLL 时给出清楚的错误；关闭设备会结束其 goroutine。
- **涉及**：`vpn/tun`、发布打包。

### I98 中转帧与中转服务端（WebSocket）

- **做**：`vpn/relay` 包：帧（`DATA`、`GONE`、`PING`、`PONG`）的编解码不分配内存；服务端是一个 WebSocket handler，用票据认证
  会话，按节点 ID 转发 `DATA` 并由会话填写源 ID，对没有会话的节点回 `GONE`，会话队列满时丢弃；WebSocket 客户端；决定 026
  中的限制和超时。
- **完成标准**：两个客户端经服务端交换帧，无法伪造源 ID，慢会话丢弃并计数而不是排队，断开的会话释放其节点，过期或错误的
  票据被拒绝。
- **涉及**：`vpn/relay`。

### I99 QUIC 和 HTTP/2 上的中转；按顺序选择传输并回退

- **做**：QUIC 上（`DATA` 作为 DATAGRAM 帧，其余在一条流上）和 HTTP/2 扩展 CONNECT 流上的中转服务端与客户端；客户端按
  `relay.transports` 的顺序带连接超时拨号、失败回退、带退避从头开始，探测排在当前传输之前的传输并切到第一个应答的，先建
  后断；固定的 `relay.transport`；供 dashboard 显示的状态（传输、尝试次数、最近错误）。
- **完成标准**：轮流封掉每种传输，客户端落到下一种；更好的传输恢复后切回，且不丢失会话中正在传输的帧；固定的传输从不回退；
  三种传输承载相同的帧；定时器用假时钟测试。
- **涉及**：`vpn/relay`。

### I100 协调者与 netmap

- **做**：在 server 节点上：用 token 加入，从配置的网段分配节点 ID 和 overlay 地址（IPv4 和 IPv6）并跨重启保持；netmap（对端、
  地址、路由、指纹、按节点的 `p2p` 和 `relay.transport`）有变化时经 I92 的信令端点推送；签发并续期中转票据；批准的子网路由；
  节点数和路由数的限制。
- **完成标准**：节点加入并得到稳定的地址，能看到彼此出现和离开，节点设置的修改能送达该节点，token 错误或超过限制的节点被
  拒绝。
- **涉及**：`vpn`、`p2p`。

### I101 节点：TUN 与对端之间，直连或中转

- **做**：节点：从 TUN 读到的包由 `vpn/packet` 路由到对端的端到端 QUIC 连接，作为 DATAGRAM 帧发送；收到的包按对端的前缀检查
  后写入 TUN；每个对端的连接先经中转打开，再经路径迁移切到 ICE 的直连地址对，consent 失效时切回；任一端关闭 `p2p` 时跳过
  ICE；连接建立期间等待的包有界；超大包回 ICMP too-big。
- **完成标准**：在模拟 NAT 后面的网络命名空间里，两个节点能直连 ping 并承载 TCP 和 UDP；NAT 是对称型或 `p2p` 关闭时经中转；
  从中转切到直连时 TCP 连接保持不断（规则 13：慢对端、大传输、中途断开）。
- **涉及**：`vpn`、`test/e2e`。

### I102 VPN 配置、Admin API 与 dashboard 页面

- **做**：配置里的 `vpn` 应用（server 和节点）、schema 和友好格式的写法；Admin API 的节点列表和按节点设置；dashboard 页面：
  节点上显示它的对端、每条路径（直连或中转、经哪种传输）、往返时延和字节数，以及它自己的 `relay.transport` 和 `p2p`；server
  上显示所有节点及同样的设置，随 netmap 下发，并显示哪一处设置生效；指标。
- **完成标准**：仅凭配置就能组建 VPN；在任一页面上修改节点的传输，会切换它的中转会话而不断开对端的连接；重载保持隧道不断。
- **涉及**：`vpn`、`config`、`schema`、`admin`、`observe`、`runtime`、`docs`。

### I103 VPN 端到端与收尾测试

- **做**：在 CI 的 Linux 上做端到端运行（命名空间、NAT、轮流封掉每种中转传输），macOS、FreeBSD 和 Windows 节点手动加入并
  记录；在基准测试机上测量直连和经每种中转传输的吞吐与时延。
- **完成标准**：运行通过，测量结果已提交。
- **涉及**：`test/e2e`、`bench`、`docs`。

## 控制台：登录与加密的 store

fit 在配置开启时提供一个用户名密码登录的控制台，在上面可以修改配置的每个部分；配置以 fit 自己的格式加密保存在单独的文件里，
密码写在 fit 启动时用的配置文件中。设计见 [决定 027](decisions/027-console-and-store.zh-CN.md)。

### I104 加密保存的配置；控制台

- **做**：`admin.store`：fitstore 格式（Argon2id、XChaCha20-Poly1305）；store 作为 fit 运行所用的基础配置，由引导文件
  提供种子，每次修改都写入，SIGHUP 时重新读取；`admin` 来自引导文件，从不保存也从不展示；`fit store export` 和
  `fit store import`。`admin.console`：独立的服务，支持 TLS；bcrypt 登录并按地址限制失败次数；会话放在 cookie 里，有空闲
  和绝对过期；CSRF token 和 Origin 检查；在进程内调用 Admin API 的处理函数；页面上 schema 的每个对象、列表和模块都有表单，
  另有 JSON 编辑器，以及版本列表和回滚。
- **完成标准**：在控制台修改的配置开始运行、写入 store、重启后依然有效；密码错误或字节被改动时 fit 无法启动；登录、锁定、
  会话、CSRF 和其他来源都有测试；每个页面在浏览器里可用。
- **涉及**：`admin`、`admin/store`、`config`、`cli`、`schema`、`templates`、`docs`。

## Agent 与 tunnel

agenthub 和 fibhub 这两个基于 fib 的程序并入 fit，成为其配置中的角色，运行在它的引擎上，设置保存在它的 store
中（[决定 028](decisions/028-llm-and-tunnel.zh-CN.md)）。

### I105 fit 配置中的角色

- **做什么**：配置中的 `agent` 和 `tunnel`，每个角色一个成员，带 `RoleConsole`；`app`：角色注册表，在 listener
  之后随每个配置推进的 `Manager`，编译配置时的 `app.Check`，以及 `Host.Edit`，它在自己的 goroutine 上经
  `admin.Configs.EditMember` 应用修改后的角色成员；`login`，把控制台的登录拆成独立的包；`app/console`，listener
  引擎上的角色网页；`fit run`、`fit build` 和 `cmd/fit` 运行角色。
- **完成标准**：配置能启动、迁移和停止角色；角色失败作为警告；修改被应用、保存进 store，无法加载时被拒绝；控制台的
  登录在两种服务器上都通过测试。
- **涉及**：`app`、`login`、`admin`、`config`、`cli`、`listener`、`runtime`、`plugin/build`。

### I106 tunnel

- **做什么**：fibhub 的 server、pod 和 client 成为 `tunnel.server`、`tunnel.node` 和 `tunnel.client`：经 TCP、
  WebSocket 和 QUIC（在引擎的 UDP binding 上）的链路，带背压的隧道，端口转发和 Web 代理，基于 `app/console` 的
  网页，每次修改都写回；去掉 fibhub 的 store 和命令行。
- **完成标准**：e2e 测试在 fib 引擎上通过，含从 store 重启；Docker 环境的检查通过。
- **涉及**：`tunnel`、`config`、`test/docker/tunnel`、`docs`。

### I107 agent

- **做什么**：agenthub 的 hub、node 和 client 成为 `agent.hub`、`agent.node` 和 `agent.client`，移植到当前的 fib：
  基于 fib WebSocket、流上带信用额度的 mesh，基于 fib 异步客户端、双向流式的 node 代理，阻塞工作放在有界池中，
  基于 `app/console` 的网页，每次修改都写回；去掉 agenthub 的状态文件、锁和密码。
- **完成标准**：含慢速、大体积和中途断开请求体的 e2e 测试在 fib 引擎上通过，含从 store 重启；Docker 环境的检查通过。
- **涉及**：`agent`、`config`、`test/docker/agent`、`docs`。

## WebRTC

fit 基于 fib 的 `webrtc` 各包提供 WebRTC 服务：TURN 服务器、以 fit 为媒体服务器的 WHIP 和 WHEP，以及 fit 作为其他媒体
服务器前面的代理，代理它们的信令和媒体（[决定 029](decisions/029-webrtc.zh-CN.md)）。TURN 服务器是 P2P 里程碑中的 I87。

### I108 基于 fib 各包的 WebRTC

- **做**：决定 029，修订 025；本文件和进度文件中的行；fit 升级到 fib `80c7f76`，第一个带 `webrtc` 的提交；按 fib 实际实现
  （`96a2331`）修正 A49 条目；为 TURN 服务器缺少的东西向 fib 提出请求（A55–A58）。
- **完成标准**：完整测试套件在 fib `80c7f76` 上通过，文档链接都能解析。
- **涉及**：`go.mod`、`docs`。

### I109 WHIP 和 WHEP：共享 Mux 上的 SFU

- **做**：角色 `webrtc.media`，listener 引擎 UDP binding 上的一个 `webrtc.Mux`，带公网地址和 ICE 服务器；handler 模块
  `whip` 和 `whep`：按路由给出的名字区分流，推流者的 RTP 转发给观看者并转发关键帧请求，用路由的中间件认证，限制流数、
  每个流的观看者数、PeerConnection 总数和码率；按 RFC 9725 和 WHEP 草案支持 DELETE 和 PATCH（trickle），在 `Link` 头中
  给出 ICE 服务器（`webrtc.turn` 有 secret 时由它生成 TURN 凭据）；重载和停止时关闭一切。
- **完成标准**：fib 的 `PeerConnection` 经 fit 推流和播放；推流者离开时其观看者结束；慢速和断开的观看者不拖住推流者；
  各项限制生效；在竞态检测下通过。
- **涉及**：`webrtc`、`modules`、`config`、`schema`、`docs`。

### I110 代理的 WHIP 和 WHEP；按 ICE ufrag 路由 UDP

- **做**：经 fit 的上游连接池把 WHIP 和 WHEP 信令代理到上游媒体服务器，把应答中的候选改写为 fit 的公网 UDP 端口，并记住
  其 ufrag 对应的后端媒体地址；一种 L4 UDP 服务，按新对端第一个数据报中的 STUN USERNAME 选路，之后粘性地中继它的五元组，
  带上限和空闲超时。实现为处理器 `webrtc_proxy` 和 `webrtc.media` 旁边的角色 `webrtc.proxy`，而不是 `l4` 服务的一种
  模式：会话要挺过重载，而 L4 服务会随其所属的图被排空；处理器和中继通过角色共享路由（[决定 029](decisions/029-webrtc.zh-CN.md)）。
- **完成标准**：一个 fit 端口后面的两个媒体服务器各自得到自己的对端；对端在 Binding 请求之前的数据报被丢弃；后端消失时
  释放其路由；在竞态检测下通过。
- **涉及**：`webrtc`、`upstream`、`config`、`schema`、`docs`。

### I111 基于 fib A55–A59 的 WebRTC

- **做**：fit 升级到 fib `84a67f7`，带 A55–A59。`webrtc.turn`：消除 I87 记录的对规则 1 的违反（A56）；`max_permissions`
  和 `max_channels`（A57）；能看到端口的对端检查，`relay_ip` 只在中继端口和 `allowed_peers` 带端口列出的端口上可达，绝不
  到达 fit 的其他端口（A57）；`OnEvent` 进入 `webrtc` 日志和 `/metrics`，`Stats()` 成为 gauge（A57）；重载原地修改配额、
  上限、生存期和凭据，地址改变时旧服务器在 listener 的排空超时内排空（A58）；`listen_tcp` 和 `listen_tls`，证书像 listener
  一样来自文件，`max_stream_message` 和 `stream_idle_timeout`，空闲地址探测也覆盖 TCP（A55）。`webrtc.proxy`：`relay_ip`、
  `port_min` 和 `port_max`，加载时检查，中继用 `DialWith` 拨号，端口范围耗尽计为丢弃（A59）。fib 请求 A55–A59 标为完成。
- **完成标准**：经 fit、用 fib 客户端的 TCP 和 TLS allocation 双向中继；上限以 508 拒绝；带端口的检查拒绝 fit 的其他端口，
  并让两个客户端互相中继；事件和指标计数；重载在修改限制时保留 allocation；地址改变时排空；中继从配置的端口范围发出，
  范围满时丢弃并计数；在竞态检测下通过。
- **涉及**：`webrtc`、`config`、`app`、`runtime`、`tlsmgr`、`cli`、`schema`、`docs`、`go.mod`。

### I112 agent 的模型接口

- **做**：在 `openai` 与 `anthropic` 之外增加模型种类 `gemini`、`azure_openai`、`ollama` 与 `cohere`，Azure 带
  `api_version`；每种的每个无状态推理调用都是表中的一项：OpenAI 在 chat、responses、completions、embeddings 之外的
  moderations、images、audio 与 rerank，Gemini 的生成、流式、计数与嵌入方法，Ollama 的 chat、generate 与 embed，Cohere
  v2 的 chat、embed 与 rerank。模型从 JSON 请求体、multipart 表单字段或 Gemini 的路径读取，`upstream` 设在读取的位置；
  node 上每种的 URL、Key 请求头与检查；client 上各厂商 SDK 的路径、Key 请求头、错误格式与模型列表；
  `ProxyReq.content_type` 与 `query`。把 agent.client 描述为从本机或局域网经公网 hub 到其他机器上 node 的链路；agent 的
  文档与代码不再提及更早的程序。
- **完成标准**：每种都经 client 端到端调用，直连与经 hub 都可，上游看到各自的 URL、Key 请求头与模型；上传流式转发，只有
  为设置模型才在 `max_body_bytes` 之内完整读入；二进制、SSE 与 NDJSON 应答原样转发；慢 agent、慢上游、大请求体与连接中断
  均正常；在竞态检测下通过。
- **涉及**：`agent`、`config`、`schema`、`docs`、`examples`、`test/docker/agent`。

### I113 LLM 角色的节点列表、连接与流量

- **做**：hub 的节点列表（基本信息、硬件、模型、负载、与节点的连接及其流量），proxy 连接时拉取、变化时由 hub 推送，合并
  推送且有上限；proxy 只读的节点页面。每条互联连接双向的字节数与 ping 的往返时间；每个角色按路由统计模型调用，其字节数
  随流传输读取；`/mesh/status`；仿照 tunnel，在每个角色的页面上画出链路图，并列出连接及其流量。页面上可以修改此前不能
  修改的设置：每个角色的 `limits`、node 的上游上限、hub 的 `max_nodes`、proxy 的 `api`，经写回生效，按配置的规则校验。
- **完成标准**：加入、变化或离开的节点在列表的上限之内显示在 proxy 上；已知调用的字节数在 proxy、hub 与 node 上一致，
  内联与流式请求体都是；连接两端都有延迟；中途被切断的调用在每个看到它中断的角色上计为失败；设置的修改写回配置，错误的值
  以 400 拒绝；在竞态检测下通过。
- **涉及**：`llm`、`docs`。

## 媒体：认证、流媒体与页面

用户要求 fit 接收媒体的每个协议都有可配置的认证、一个流媒体服务器，以及使用它们的页面
（[决定 030](decisions/030-media-auth-and-pages.zh-CN.md)）。先做一套命名的策略；流媒体角色和页面在其上并行构建。

### I115 媒体认证策略

- **做**：顶层 `media_auth`：命名的策略，包括静态 token（或其 SHA-256）、用户（明文密码或 bcrypt 哈希）、fit 签发 token
  用的 secret、公开动作和 token 有效期，每个 token 和用户带它可以推送和播放的流模式。`mediaauth` 包：各项检查，从不阻塞
  worker（bcrypt 在自己有上限的池上）；从 HTTP（Bearer、Basic、`?token=`、cookie）和 URL（`token`，`user` 和 `pass`）读取
  凭据；签名 token；按 engine 和名称查找的策略，由角色 `media_auth` 在重载时原地修改。`whip`、`whep` 和 `webrtc_proxy`
  （配合 `action`）的 `auth`；`fit media-token`。
- **完成标准**：对每个处理器，没有凭据的 POST 以 401 和 Bearer 质询应答，凭据不允许时 403，允许时 201，覆盖静态、签名和
  查询参数 token、Basic 和 bcrypt 用户；会话的 DELETE 不需要凭据；重载原地修改策略；`fit media-token` 的 token 被 fit 接受；
  配置拒绝未知策略、空策略、错误的模式、同时给出 token 和 token_sha256；在竞态检测下通过。
- **涉及**：`mediaauth`、`config`、`module`、`runtime`、`app`、`webrtc/media`、`cli`、`schema`、`docs`。

### I116 流媒体角色

- **做**：由 fit 运行 fib 的 `media/hub`：顶层 `streaming`，带 RTMP、RTSP 和 SRT 监听，各有 `auth`、上限（流数、每条流的
  播放者数、超时）以及按 fit 惯例的重载；HTTP 出口作为处理器：`httpflv`、`hls`，以及基于 fib `media/rtc` 的 `stream_whip`
  和 `stream_whep`，各有 `auth`；按各协议的方式传递凭据（RTMP 和 SRT 的查询参数或 streamid，RTSP 的 Basic 和 Digest，HTTP
  经 `mediaauth.FromHTTP`，HLS 把查询参数中的 token 带到分片）。
- **完成标准**：Linux 容器中的 ffmpeg 带与不带有效凭据经 RTMP、RTSP 和 SRT 推流，并经每种出口播放；上限和重载正常；在
  竞态检测下通过。
- **涉及**：`streaming`（新）、`config`、`app`、`schema`、`docs`、`examples`、`scripts`、`test/workers`。

### I117 媒体页面

- **做**：处理器 `media_pages`，资源内嵌，提供首页、经 WHIP 和 WHEP 的房间通话、推流和播放页面；用策略的用户登录，签发
  签名 token 作为 HttpOnly cookie；有上限的房间 API，成员经它通告麦克风和摄像头状态；每个推流者始终提供一个音频和一个视频
  transceiver，没有设备时用合成轨道，开关从不重新协商；检查 webrtc.media 对静默推流者和请求未发送媒体类型的观看者的处理。
- **完成标准**：页面在带假设备和不带设备的无头 Chromium 中可用；房间 API 的上限、认证和连接中断在 Go 测试中正常；一个示例
  配置把它们串起来。
- **涉及**：`handler/mediapages`（新）、`webrtc/media`、`schema`、`docs`、`examples`。

### I118 媒体页面的房间聊天

- **做**：房间通话中的聊天：文字消息和小文件（最大 `max_file_bytes`，默认 5 MiB）经房间 API
  （`POST .../chat`、`POST .../files`、`GET .../files/<id>`，以及 `events` 上的 `history` 事件和每条消息一个
  `chat` 事件）收发，成员用加入时得到的 key 标明、key 从不放在 URL 里；`pages` 决定是否提供页面，登录和房间 API
  保留；`mic_default` 和 `cam_default` 决定成员加入时麦克风和摄像头是否打开，URL 的 `mic=0|1` 和 `cam=0|1` 覆盖
  它们；聊天有上限：每个房间 64 条消息、所有房间共 64 MiB 文件字节，跟不上的关注者被断开。
- **完成标准**：两个成员通过页面互发一条文字消息和一个文件，后加入的关注者得到历史；超过 `max_file_bytes` 的文件
  以 413 应答，超过 `max_message_bytes` 的请求体以 413 应答；`pages: false` 时页面的路径为 404，登录和房间 API
  仍在；各项上限、淘汰和慢关注者在竞态检测下成立；页面的浏览器测试以麦克风和摄像头关闭加入，打开它们，并发送一条
  消息和一个文件。
- **涉及**：`handler/mediapages`、`schema`、`docs`、`test/docker/mediapages`。

### I119 从一个路由签发媒体 token

- **做**：处理器 `media_tokens`，一个供业务后端调用的路由，用来一次签发许多 `media_auth` token 并吊销它们：批量签发
  （`POST <prefix>`，每个 token 的 subject、publish 和 play 模式、ttl）、路由的 `prefix` 以及对流名的 `prefixes`
  限制，还有 `max_ttl`、`max_tokens`、`max_body_bytes` 和 `revoke_timeout`；`media_auth` 新增 `keys` 密钥环，每个
  密钥是一个名称及其自己的 `secret` 或 `secret_file`，用来代替单个 `secret` 或 `secret_file`：token 用一个密钥
  签发（批次 `key` 指定的那个，或环中的第一个），并用每个密钥校验，所以轮换一个密钥（`POST <prefix>/revoke` 带
  `{"key": ...}`）只拒绝该密钥签发的 token，其他密钥的 token 保持有效，`{"all": true}` 则轮换所有密钥；吊销把 32
  个随机字节原子地写入每个被轮换密钥的文件，在自己的有界线程池上完成，然后替换该密钥，之后签发的 token 在过期前有效；
  文件保留新 secret，重启后吊销仍然有效，应用配置时也会重新读取；两个密钥共用同一 secret 会被拒绝；路由的中间件认证
  调用方，因此处理器不是 Admin API；`media_auth` 的 `secret`、`secret_file` 和 `keys` 三者取一。
- **完成标准**：业务后端的路由签发一批 token，策略按所请求的流接受它们、对其他流拒绝，且这批 token 由请求 `key` 指定的
  密钥签发；坏的 subject、模式、ttl、key 或前缀之外的路径被拒绝，并且整批都不签发；吊销一个密钥后，其文件保存至少 16
  字节的新 secret，它签发的旧 token 被拒绝，新 token 被由该文件编译出的策略接受，而其他密钥的 token 仍然通过；`all`
  轮换每个密钥；重启后吊销仍然有效；路由的中间件、各项上限以及吊销池的 503、504 和 500 在竞态检测下成立；
  `TestChapterExamples` 和 workbench 的 schema 描述覆盖新字段和参数。
- **涉及**：`handler/mediatokens`（新）、`config`、`mediaauth`、`cli`、`modules/standard`、`schema`、`docs`、
  `test/workers`。

### I120 workbench 的配置页面与控制台密码

- **做什么**：每个控制台管理员账号可以修改自己的密码，已登录用户也可以重置另一个账号的密码，修改保存在 store 文件中；配置菜单
  去掉网关/系统分组树，全局模块与各协议模块在左侧平铺并列；每个配置页面既能用表单编辑（树形，每个可省略成员带复选框与输入框），
  也能用 `.conf`、`.yaml`、`.json` 原文编辑该页面所管的成员，保存前经过与 `fit validate` 及应用相同的检查。fitstore 的载荷
  升级为第 2 版：`config` 下是基础配置，`users` 下是改过密码的控制台用户，第 1 版的 store 仍可读取；`admin.WithConsoleUsers`、
  `Configs` 的控制台用户覆盖、`login` 的 `Users`、`HasUser`、`SetPassword` 与 `EndUserSessions`；端点 `GET /api/users`、
  `POST /api/account/password` 与 `POST /api/users/password`；`POST /config/render`、`POST /config/parse` 与
  `PUT /config/text?format=&members=`（把页面的成员合并进配置并经同一条路径应用，错误映射到原文的行与列）；`app/workbench/web`
  的平铺菜单、账户模块、每页的标签页，以及表单的“是否设置”复选框。
- **完成条件**：每个控制台用户在页面中修改自己的密码（其他用户也可重置），重启后仍然有效并保存在 store 中，没有 store 时返回
  `409`；浏览器中菜单平铺（英文与中文）；页面的 `.conf`、`.yaml`、`.json` 标签页能呈现该部分、接受修改并应用，无法解析或无法
  加载的文本被拒绝并显示错误与行列号，且不应用任何修改；bundle 的文件全部被加载，workbench 的 schema 说明保持完整；在竞态检测下。
- **涉及**：`admin`、`admin/store`、`login`、`cli`、`app/workbench`、`docs`。

### I121 运行中的服务与它所依据的配置之间的一致性

- **做什么**：检查运行中的 fit 实际所做与它运行所依据的配置是否一致：启动时才生效的设置（`log`、`access_log`、`telemetry`、
  `engine`、`acme`、`providers`）与运行中的引擎上限；配置中的角色与正在运行的角色；provider 加入生效配置但不在基础配置中的元素；
  已绑定的监听及其地址；按监听 `tls.acme` 名字所提供的证书；上游池及其后端健康；以及缓存区。每个差异都有稳定的 ID、类别、种类
  （`drift` 可操作，或 `info` 只是运行事实并非不一致）、配置值与运行值，以及可选修复：在基础配置中 `add`、`delete`、
  `replace`，或 `reapply`。`GET /config/check` 返回运行中的版本、ETag 与差异；`POST /config/reconcile` 接收选中的
  `{"fixes": [{"id", "action"}]}` 与 `If-Match`，在应用锁内重新检查，经同一条配置路径应用补丁，并按 `PUT /config` 的方式应答。
  workbench 的一致性页面显示每处差异及其值，让用户逐项选择并确认一个修复，然后把所选一起应用并重新检查；`fit check` 在终端通过
  Admin API 做同样的事，支持 `-json` 与 `-fix`，由引导文件的 `admin` 指明 API 地址。
- **完成条件**：启动后改过的只在启动时生效的成员或引擎上限被列为 drift，带生效值与回退修复，应用后检查干净；配置了但未运行的角色、
  基础配置中缺少的 provider 元素、缺失或多余监听/上游池、未被证书覆盖的 `tls.acme` 名字、缺失的缓存区都能被发现并给出修复；任一修复
  或合并后的配置加载失败时不做任何应用；过期的 `If-Match` 被拒绝；页面在浏览器中以中英文工作，CLI 能列出差异并逐项修复，退出码
  0/1/2；在竞态检测下。
- **涉及**：`admin`、`cli`、`app/workbench`、`docs`。

### I122 workbench 的功能标签与监听端口

- **做什么**：把配置的 `streaming`、`media_auth`、`webrtc`、`tunnel`、`llm` 成员放到平铺菜单中各自的页面上（菜单中带标明类别的
  小标签），不再收在“全局配置”与“其他”页面下，编辑器仍带 UI/`.conf`/`.yaml`/`.json` 标签页与保存错误链接；在“监听”页面为每个
  端口列出它提供的功能——按处理器分组的 HTTP 路由（反向代理、正向代理、静态文件、直接应答、FastCGI、媒体令牌、流媒体与 WebRTC
  处理器）、路由中间件、四层服务、SOCKS，以及带证书/ACME 或默认 HTTPS 的 TLS——每项链接到编辑它的页面，并给出内核实际绑定的
  地址；配置未写监听时给出默认 HTTPS 生成的端口；处理器的链接打开按该处理器筛选的路由页面。
- **完成条件**：菜单在协议页面之后显示这些功能条目（中英文）；五个页面编辑各自的成员（含原始文本标签页），保存错误链接到对应
  页面；“监听”页面为每个已配置（或生成）的端口显示卡片及其功能、数量与链接，`/api/status` 给出实际绑定地址（失败时降级），
  深链落到筛选后的路由页面；“全局配置/其他”下不再有重复的配置页面；bundle 测试通过，页面在浏览器中可用；触及的 Go 代码（本次
  无）在竞态检测下。
- **涉及**：`app/workbench`、`docs`。

### I123 workbench 的引擎菜单

- **做什么**：在 workbench 菜单中新增一级 **引擎** 条目，其子菜单为各监听端口（配置写入的监听，以及配置未写监听时由默认 HTTPS
  生成的端口），每个端口展开为它所提供的功能，按用户要求的顺序：其处理器（静态文件、直接应答、FastCGI、媒体令牌、媒体页面——各自
  链接到按该处理器筛选的路由页面，然后是其他处理器模块）、反向代理与正向代理路由、缓存（缓存区页面与路由页面）及归组的其他
  中间件、流媒体处理器（流媒体页面及每个处理器的筛选路由）、WebRTC 处理器（WebRTC 页面及筛选路由）、四层服务、SOCKS、TLS 证书、
  ACME、默认 HTTPS 与 HTTP 重定向，最后是指向监听表单的链接；端口行显示地址与功能标签（PROXY 协议、HTTP/3、按需证书与客户端
  证书），菜单随配置变化更新。workbench 菜单获得按模块的动态导航、链接到其他页面的第三级条目、只重建菜单以及递归筛选；监听页面
  保留监听表单并指向引擎菜单。
- **完成条件**：菜单中引擎位于监听之后（中英文）；每个端口按其顺序展开出条目，链接与数量正确，端口行显示协议与功能标签、
  `/api/status` 的实际地址、默认 HTTPS 与无监听两种情况，以及活动链接高亮；点击条目落到正确页面（按顺序要求筛选路由）；配置保存后
  菜单更新且不丢失当前页面；监听页面不再绘制端口卡片；bundle 测试通过，mock 与真实浏览器检查成立。
- **涉及**：`app/workbench`、`docs`。

## workbench 中的 AI 助手

2026-11-04 提出，已审核。workbench 增加一个助手：每个页面旁的对话面板，读取运行中的 fit 在做什么、加以解释，并起草配置
修改——写进页面自己的编辑器与表单，或作为校验过的 diff 由用户点击应用——提交与保存始终由人完成。它是用户所设置的模型
API 的客户端（带密钥的托管 API、本地服务器，或本 fit 自己的 `llm.proxy`），自身不带模型。决定 031 记录下面的选择。

原则，按约束顺序：

- **模型起草，由人提交、应用与保存。** 没有任何工具会写入配置、版本或 store：助手做出的始终是草稿，由人完成。在页面上，它把
  用户要求的修改写进该页面的编辑器或表单字段，作为未保存的草稿（I154），用户在页面中审阅、修改，并像其他页面修改一样提交与
  保存。在页面之外，会改动东西的工具只是往一个提议中添加内容（经应用路径校验过的 diff）；任何修改都离不开人：具备相应级别、
  已登录的用户点击应用，并在显示 diff 的弹窗中二次确认，修改才会生效，带 `If-Match` 并保存，像其他修改一样进入 `/versions`
  与审计日志。没有无人值守模式。
- **页面只编辑，不提交。** 页面工具只改变当前页面编辑器与表单中所保存的内容：它们从不调用配置接口（不 `PUT`/`PATCH /config`，不做
  reconcile、回滚或清缓存），从不保存，也不改变版本、ETag 或 `/versions`，不会作为修改进入审计日志；页面的 **保存并应用**
  始终是人的点击，经页面原有的应用路径执行，记为这个人的修改。当前页面无法表达的修改，改用提议（I126）。
- **不阻塞 fib worker**（规则 1）：模型调用走 fib 的异步 HTTP 客户端，流式回答边到边解析，工具在控制台的池上运行；每个会话、
  工具结果、流缓冲与审计条目都有上限。
- **密钥不外泄**：模型 API 密钥保存在 store 中，只写、掩码显示，从不发给浏览器；交给模型的一切工具结果都去掉密码哈希、密钥、
  token 与证书私钥；日志只有在设置允许时才发送。
- **工具输出是数据，不是指令**：工具返回的配置原文、日志与头部在提示中作为数据隔离；其中任何内容都不能扩大助手的权限。
- **默认关闭**：没有 `admin.assistant` 就没有面板，也没有任何外发调用。

### 功能（工具注册表）

每个功能是一个工具，带名称、JSON schema、级别（`read`、`edit` 或 `propose`）与开销上限。`GET /api/m/ai/tools` 列出它们；
面板的功能页展示它们，每个可在设置中启用或禁用。

| 分组 | 工具 | 级别 | 作用 |
| --- | --- | --- | --- |
| 配置 | `config_get` | read | 基础或生效配置，整体或按成员，`.conf`/`.yaml`/`.json`，已脱敏 |
| 配置 | `schema_describe` | read | 某成员的 schema、说明、默认值与示例（中英文） |
| 配置 | `config_validate` | read | 按 `fit validate` 校验原文或补丁，错误带行列 |
| 配置 | `versions_diff` | read | 保留的版本，以及两个版本之间的 diff |
| 运行时 | `status_get` | read | 已绑定的监听、运行中的角色、引擎限制、运行时长 |
| 运行时 | `metrics_query` | read | 按路由、上游、监听的计数与直方图，限定时间窗口 |
| 运行时 | `upstream_health` | read | 上游池、成员、健康状态与最近的失败 |
| 运行时 | `consistency_check` | read | `GET /config/check` 的差异与修复 |
| 运行时 | `certs_list` | read | 所提供的证书、域名、签发者、到期时间、ACME 状态 |
| 运行时 | `cache_zones` | read | 缓存区、大小、命中率 |
| 角色 | `llm_nodes`、`tunnel_nodes` | read | 角色的节点列表、链路、延迟与流量 |
| 日志 | `log_tail` | read | fit 自身日志，来自新增的有上限内存环，按 logger 与级别，已脱敏 |
| 日志 | `access_log_sample` | read | 匹配某路由、状态码或客户端的最近访问日志行，有上限 |
| 页面 | `page_get` | read | 当前页面、它能编辑的成员与表单字段，以及未保存的原文 |
| 页面 | `page_edit` | edit | 把草稿写进当前页面的编辑器或表单字段：设置、添加或删除某成员；标为未保存，从不提交 |
| 页面 | `page_revert` | edit | 撤销助手在该页面上的草稿，恢复到起草前页面自己的原文 |
| 修改 | `config_propose` | propose | 按成员的补丁，以三种格式渲染为 diff 并校验，等待点击 |
| 修改 | `reconcile_propose` | propose | 选定的一组一致性修复 |
| 修改 | `rollback_propose` | propose | 回滚到某个保留版本 |
| 修改 | `cache_purge_propose` | propose | 按缓存区、键或前缀清除 |
| 转换 | `adapt_convert` | read | nginx 或 Caddyfile 原文经 `fit adapt` 转换，并列出未能转换的部分 |
| Agent | `skill_load` | read | 某个已启用 skill 的完整提示词，受用户级别约束 |
| Agent | `ask_user` | read | 带选项的问题，在面板中显示为按钮；用户的选择即结果 |
| 媒体（I152） | `media_library_search` | read | 搜索并筛选某个媒体库的条目（编码、分辨率、探测错误），分页 |
| 媒体（I152） | `media_item_get` | read | 某个条目的轨道、编码、清晰度版本、字幕、sidecar 与探测错误 |
| 媒体（I152） | `channels_status` | read | 每个频道当前与下一个节目及播放位置，以及其推流目标的状态、字节数与最近错误 |
| 媒体（I152） | `playback_sessions` | read | 按资源与地址列出的在线会话、每个 subject 相对 `max_sessions` 的计数、按原因统计的 403；除非设置允许，subject 以哈希显示 |
| 媒体（I152） | `media_scan_propose` | propose | 重新扫描某个媒体库 |
| 媒体（I152） | `media_session_end_propose` | propose | 结束在线播放会话 |
| 媒体（I152） | `media_key_rotate_propose` | propose | 按名称轮换 `media_auth` 的一个密钥或全部密钥 |
| 媒体（I152） | `media_file_propose` | propose | 重命名、移动、删除媒体库文件或编辑其 sidecar，并列出正在播放它们的地方 |
| 集群（I153） | `cluster_members` | read | 管理节点的成员：健康、版本、配置版本、漂移、标签、组 |
| 集群（I153） | `cluster_templates` | read | 模板、其拥有的路径与变量、每个成员的同步状态 |
| 集群（I153） | `cluster_rollouts` | read | 进行中的发布及其历史，逐批次，带每个成员的结果 |
| 集群（I153） | `cluster_metrics`、`cluster_log_tail`、`cluster_certs` | read | 管理节点按成员与组的时间序列、跨成员追踪的日志、证书清单 |
| 集群（I153） | `member` 参数 | read | 上面的每个只读工具（如 `config_get`、`consistency_check`）经管理节点的会话在指定成员上运行 |
| 集群（I153） | `rollout_propose` | propose | 对模板或某组成员的修改，作为发布计划：每个成员的 diff、试运行、策略与健康门槛 |
| 集群（I153） | `cluster_op_propose` | propose | 对多个成员的操作（重载、回滚、校验、清除缓存） |
| 集群（I153） | `drift_fix_propose` | propose | 以一次发布修复成员相对模板的漂移 |

所有工具都触及不到：`admin.console` 的用户与密码、store 的密钥、`admin.assistant` 本身、Admin API 自身的地址与 token，以及
配置之外的任何文件；`media_auth` 的密钥、播放 URL 及其签名（测试 URL 只在 I138 的表单中签发）以及上传；集群成员的凭据、
注册 token，以及添加或删除成员。页面工具只能触及当前页面的编辑器与该页面所编辑的成员和字段，不能写进其中的密钥字段；
草稿与任何工具结果一样被脱敏并受上限约束。

### Skill

skill 是一个命名的任务：说明何时使用的描述、提示词、可调用的工具子集（永远不超过用户的级别）、可选的输入与可选的模型。内置
skill 嵌入在 bundle 中；用户可在 Skills 页面添加自己的（保存在 store 中），以带 front matter 的 Markdown 导入导出，并可启用或
禁用任意一个。在对话中用 `/name` 运行某个 skill；不指定时助手根据已启用 skill 的描述自行选择。

| Skill | 作用 | 工具 |
| --- | --- | --- |
| `explain` | 解释当前页面、某成员或某个值，并链接到文档 | 配置、schema |
| `route-builder` | 根据一句话生成路由、匹配器、处理器与中间件 | 配置、修改 |
| `reverse-proxy` | 带健康检查、负载均衡、超时与重试的上游池 | 配置、运行时、修改 |
| `tls-acme` | ACME 或文件证书、默认 HTTPS、重定向、客户端证书 | 配置、证书、修改 |
| `rate-limit-auth` | 路由上的限流、IP 名单、basic、JWT 或 forward auth | 配置、修改 |
| `cache-tuning` | 根据命中率与路由流量调整缓存区与规则 | 缓存、指标、修改 |
| `diagnose` | 某路由为何返回 502/504 或变慢：结合健康状态、指标与日志 | 运行时、日志 |
| `drift-explain` | 解释每个一致性差异并推荐修复 | 一致性、修改 |
| `log-analysis` | 归类并解释 fit 日志与访问日志中的错误与异常 | 日志 |
| `security-review` | 管理入口暴露、TLS 设置、缺失的限制与无上限的请求体，给出带修复的发现 | 配置、状态、修改 |
| `migrate` | nginx、Caddy 或 Traefik 配置迁移到 fit：先用转换器，剩余部分交给模型 | 转换、配置、修改 |
| `media-setup` | 根据服务描述配置流媒体、媒体认证、WebRTC 与媒体页面 | 配置、修改 |
| `llm-mesh-setup` | 根据所描述的机器配置 LLM hub、node、proxy 角色及其模型 | 配置、角色、修改 |
| `tunnel-setup` | 为 NAT 后的服务配置隧道 hub、node 与 proxy | 配置、角色、修改 |
| `vod-setup`（I152） | 根据站点描述，为站点的影片配置媒体库、`vod` 路由与 `url_signing` | 配置、媒体、修改 |
| `channel-scheduler`（I152） | 根据用自然语言描述的节目生成频道的列表或排期，以及推流目标 | 媒体、配置、修改 |
| `library-health`（I152） | 探测错误、缺失的清晰度版本或字幕、播放器无法播放的编码，以及 `scripts/` 中准备文件的 ffmpeg 步骤 | 媒体 |
| `playback-troubleshoot`（I152） | 观众为何被拒或卡顿：按原因统计的 403、会话上限、过期的授权、缓存与文件池 | 媒体、指标、日志 |
| `fleet-health`（I153） | 综合读取各成员状态：哪些下线、落后、漂移或证书即将到期 | 集群 |
| `rollout-planner`（I153） | 规划跨组的修改：策略、金丝雀成员、批次、健康门槛与回滚 | 集群、修改 |
| `cluster-drift`（I153） | 解释每个成员的漂移，并以一次发布修复 | 集群、修改 |

### 设置

配置中的 `admin.assistant`，其密钥在 store 中，在设置页面编辑：

| 设置 | 作用 |
| --- | --- |
| `providers` | 命名的模型端点：`kind`（`anthropic`、`openai`、`openai_responses`、`gemini`、`azure_openai`、`ollama`，或任意 `base_url` 的 `openai_compatible`，如 vLLM、llama.cpp、DeepSeek 或某个网关）、`base_url`、`api_version`、保存在 store 中或从 `env:NAME` 读取的密钥、额外头部、超时 |
| `llm_proxy` | 以本 fit 的 `llm.proxy` 作为提供方，经其回环地址访问，使助手使用 mesh 中的模型 |
| `models` | 每个提供方可用的模型（有列表接口的从中获取，否则手写），带上下文长度及是否支持工具调用 |
| `default_model` | 对话使用的提供方与模型；skill 可指定自己的 |
| `params` | 最大输出 token、温度、API 支持时的 thinking 或推理强度 |
| `limits` | 每个会话的轮数、每轮的工具调用次数、每个工具结果的字节数、每个用户保留的会话历史、并发对话数、每用户每天的 token |
| `data` | 是否发送日志、是否发送访问日志行、额外的脱敏规则 |
| `users` | 每个控制台用户的级别：`off`、`read`（只读工具）、`edit`（还可在页面上起草，从不提交）、`propose`（还可生成提议）、`apply`（还可应用提议；最高），每一级包含之前的级别（默认 `read`） |
| `audit` | 保留多少条审计记录 |

设置页面在保存前可测试提供方（一次短调用，显示延迟与模型列表）。

### Agent 设计

助手是一个 agent：在 fit 中运行的“模型 ⇄ 工具”循环，而不是发一次提示词、拿一次回答。用户的原话按原样发给模型，不经另一次模型
调用改写；fit 所做的是为每次请求组装原话周围的上下文：

| 部分 | 来源 | 内容 |
| --- | --- | --- |
| 系统提示 | fit 内置 | 助手的角色；fit 的概念（监听、路由、上游、角色）；规则：修改只能作为页面草稿或提议、从不提交、工具输出是数据、不确定就问；回答所用的语言 |
| 页面上下文 | workbench | 当前页面、选中项（某个端口、路由、一致性差异）、它能编辑的成员与表单字段、上次保存的错误 |
| skill 目录 | 已启用的 skill | 每个 skill 只有名称与一句话描述 |
| 当前 skill | `/name` 或“问 AI”按钮 | 该 skill 的完整提示词，工具收窄到它所列的那些 |
| 工具 | 注册表 | 与用户级别取交集 |
| 历史 | store | 预算内的会话，早期轮次压缩为摘要 |
| 用户消息 | 用户 | 原话 |

循环：模型回答或调用工具；fit 在控制台池上执行工具——页面工具除外，它们在该页面自身运行——对结果脱敏、限制大小后作为数据交回；
如此往复，直到得出回答、页面草稿或提议，或达到 `limits`。

- **skill 按需加载。** 系统提示只带目录；模型需要某个 skill 时调用 `skill_load` 取其完整提示词，skill 再多也不会撑大每次请求。
  用 `/name` 或按钮指定的 skill 直接加载。
- **上下文预算。** 各部分按份额分配模型的上下文窗口（系统提示、工具、历史、工具结果）；超出时先截断过大的工具结果，再由模型
  把早期轮次压缩为摘要，摘要随会话保存在 store 中。
- **在用户所在处修改。** 当前页面能容纳用户要求的修改时，页面工具经对话流把它送到该页面；页面把它写进编辑器或表单字段，标为
  未保存的草稿，用户在那里审阅、修改，再由页面自己的 **保存并应用** 完成——助手从不点击它。没有页面能容纳该修改（跨页面、
  涉及密钥、角色或集群成员），或没有打开页面时，改用提议（I126）。
- **先问清楚再修改。** 缺少必要信息的修改（上游地址、域名）先提问而不是猜：`ask_user` 工具在面板中显示问题，选项作为按钮，
  用户的选择作为工具结果返回。

迭代 I124–I130、I152 与 I154 见[索引](#索引)。

### I124 助手的设置与模型客户端

- **做**：决定 031。schema 与配置中的 `admin.assistant`，其密钥保存在 store 中（fitstore 载荷版本 3，仍可读版本 2），掩码显示且
  只写。新包 `assistant/model` 中的模型客户端：对每种 `kind`，构造带系统提示、消息与工具的请求，以有上限的缓冲解析流式回答
  （SSE，Ollama 为 NDJSON）为文本、thinking 与工具调用，统计用量；超时、浏览器离开时取消、仅在首字节之前重试；`llm_proxy`
  提供方。workbench 的 `ai` 模块，含设置页面、提供方测试与模型列表，中英文。`GET/PUT /api/m/ai/settings`、
  `POST /api/m/ai/providers/test`。
- **完成条件**：每种 kind 都针对其 API 的录制假服务调用通过，并在 `test/docker` 中针对 Ollama 与一个 OpenAI 兼容服务器调用通过，
  工具调用往返成功；密钥从不出现在响应、页面或日志中；慢速、中断或超大的流被限制并失败；在 race 检测下通过。
- **涉及**：`assistant`（新）、`admin`、`admin/store`、`config`、`schema`、`app/workbench`、`docs`。

**待审查的决策**（实现时所做，2026-11-06；在用户裁定前按此执行）：

1. **设置存放在哪里。** `admin.assistant` 给出初始设置；设置页保存后改用 store 的 `assistant` 一节，与 store 中的配置一样。`admin`
   整体只在重启时变化且不进 store，页面无法保存到其中。备选：单独的覆盖层；页面不允许编辑。
2. **没有 store 就不保存**：`PUT` 答 409；页面仍可读取与测试，密钥来自 `api_key_env`，页面输入的密钥绝不落到明文文件。
3. **只有存在小节时才写第 3 版**：没有小节的 store 仍写第 2 版，旧版 fit 可以打开。备选：总写第 3 版。
4. **导出不含密钥**：导出只带配置与用户。备选：因导出已加密而包含密钥。
5. **掩码显示密钥最后四个字符**，仅限 16 个字符以上的密钥。备选：只显示“已设置”。
6. **非 loopback 不通过明文 http 发送密钥**，与控制台和 Admin API 一致。备选：允许局域网地址（如局域网中的 vLLM）。
7. **`llm_proxy` 是一种 provider 类型**，而不是计划表中单独的成员：经 loopback 用 proxy 的某个 API key 访问，证书固定（pin），
   不按 CA 校验。
8. **重试**：在得到任何应答之前，对传输错误与可重试状态（408、429、500、502–504、529，Retry-After 最多 10 秒）重试；
   首字节超时从不重试。
9. **各 API 的细节**：Gemini 用 `parametersJsonSchema`；Chat Completions 用 `stream_options.include_usage`；openai 与 azure 用
   `max_completion_tokens` 与 `reasoning_effort`，其他用 `max_tokens`；Anthropic 的思考预算 2048/8192/24576 加到 `max_tokens`
   上，开启思考时不带 temperature；Responses 用 `store: false`。
10. **计划表中的所有限制现在都在 schema 里**，带默认值；I124 只执行 `max_stream_bytes`，其余由使用它们的迭代执行。
11. **`users` 是列表**，元素为 `{username, level}`，每个都须是控制台用户。
12. **防止更新丢失**：本进程内的保存计数器对过期页面答 412。备选：基于哈希的 ETag。
13. **provider 测试**：最多同时 4 个，模型列表 15 秒、调用 30 秒，最多 32 个 token，不重试；`models_only` 只读列表。
14. **模型连接**：在监听器的引擎上，最多 16 个 fib 客户端（每种连接超时与固定证书一个）；需要第 17 个时淘汰最旧的，等其调用
    结束后关闭。

### I125 对话面板与只读工具

- **做**：按 [Agent 设计](#agent-设计) 实现 agent 循环及其上下文：系统提示、页面上下文、skill 目录、截断工具结果与压缩早期
  轮次的预算，以及 `ask_user`；上表中只读工具的注册表，各有 schema、级别与上限，在控制台池上通过进程内 Admin API 运行；对每个结果去除密钥；
  为 `log_tail` 增加 fit 自身日志的有上限内存环。`POST /api/m/ai/chat` 以 SSE 流式输出事件（文本、工具调用、工具结果、用量、
  结束），循环受 `limits` 限制；会话按用户保存在 store 中（加密、跨重启保留），数量与大小有上限，可列出与删除。面板：可从每个页面打开，当前页面与选中项作为上下文发送，
  流式 Markdown，工具调用折叠显示，停止、重试、新会话；功能页面。
- **完成条件**：关于某路由、某上游的健康状态或某条日志错误的问题由工具给出回答，中英文；录制的模型请求中没有任何密钥；超过
  上限的工具结果被截断并注明；长会话保持在模型窗口之内，早期轮次被压缩为摘要；`ask_user` 的问题显示为按钮，用户的选择到达模型；
  用户原话不经改写到达模型；循环在上限处停止；两个用户的会话互相隔离，并在上限内跨重启保留；在 race 检测下通过，面板在浏览器中可用。
- **涉及**：`assistant`、`admin`、`admin/store`、`observe`、`app/workbench`、`docs`。

**待审查的决策**（实现时所做，2026-11-06；在用户裁定前按此执行）：

1. **工具在哪里运行。** 控制台池的请求辅助函数会答 504 并打断流，因此新增 `console.Request.Submit`，在池上运行而不占住请求，
   每个工具 30 秒。备选：助手自己的池。
2. **脱敏** 在循环中对每个结果做一次，JSON 工具内部再按结构做：名字像密钥的成员（password、secret、token、key、hash；不含
   `*_file`、`*_env`、`max_*`）以及文本中按模式识别的密钥。`admin.assistant`、`admin.console.users`、`admin.store`、`admin.address`
   与 `admin.token_file` 完全不给出。备选：只按列出的路径。
3. **`access_log_sample`** 读取访问日志文件的最后 512 KiB，而不是环形缓冲，后者会在请求路径上多一次复制。
4. **到达 `max_turns`** 时拒绝新消息（409，需开新对话），而不是悄悄折叠。
5. **到达 `max_tool_calls`** 后的调用得到错误结果，回答以 `max_tool_calls` 结束；不再做不带工具的收尾调用，Anthropic 会拒绝这种
   历史。
6. **每日 token** 按用户计在其对话小节中，UTC 零点清零。
7. **对话大小**：常量，每个对话 512 KiB、每个用户 8 MiB；超出时先去掉已摘要的消息，再去掉最早的轮次。备选：单独的设置。
8. **没有 store 时** 对话保存在内存中直到重启，面板会说明。
9. **上下文预算**：约每 token 3 字节；模型未设 `context` 时为 32,768 token；回答占 `max_output_tokens`，最多四分之一；每次对话只
   尝试一次摘要，失败则更大幅度地截断历史。
10. **页面上下文** 放在系统提示中，脱敏且最多 16 KiB，使用户的原话不变地到达模型。
11. **最后一个事件** 在对话与对话位释放之后写出，立即发送的下一条消息不会被拒绝。
12. **慢浏览器**：事件不等待地写出，受限制约束；60 秒没有读取的流被切断，对话停止。备选：暂停模型的流。
13. **停止**：浏览器中止请求并调用 `POST /api/m/ai/chat/stop`；保留部分回答并标为已停止，没有结果的调用得到错误结果。
14. **语言**：模型得到英文的工具描述；Functions 页以 workbench 的语言显示。

### I126 提议、级别与审计日志

- **做**：propose 工具：模型可以连续调用多个，每次都向该会话唯一的待定提议添加一步；提议包含各步骤、基础版本的 ETag、合并后的
  `.conf`/`.yaml`/`.json` 三种 diff 与整体校验结果；面板显示它及每一步，并提供应用与放弃。点击应用会弹出显示完整 diff 的确认窗口，
  只有确认后才以该用户身份带 `If-Match` 执行：配置类步骤合并后经 `PUT /config/text` 作为一次修改应用（reconcile 与回滚走各自的
  接口）并保存，之后再执行缓存清除；配置部分要么全部生效要么全不生效；过期的提议被拒绝，并在新版本上重新提议。来自 `admin.assistant.users` 的按用户级别，在每个工具与每次
  应用时检查。store 中的审计日志：用户、时间、会话、skill、工具调用、提议、结果与版本；`GET /api/m/ai/audit` 及其页面。
- **完成条件**：用自然语言提出的修改成为有效提议，应用后成为 `/versions` 列出的新版本；多次 propose 调用合成一个提议、作为一个
  版本应用，其中一步失败则全部不应用；没有点击和弹窗确认就不会应用，取消弹窗时配置与 store 都不变；`read` 或 `edit` 用户
  拿不到 propose 工具，`propose` 用户不能应用；校验失败的提议不能应用；禁止的成员不能被提议；审计日志在上限内跨重启保留；
  在 race 检测下通过。
- **涉及**：`assistant`、`admin`、`admin/store`、`app/workbench`、`docs`。

**待审查的决策**（实现时所做，2026-11-06；在用户裁定前按此执行）：

1. **谁可以应用。** 只有级别 `apply`；`apply` 用户能列出并应用或丢弃任何用户待处理的提案，提议者在审计日志中记为 `owner`，否则
   `propose` 用户的提案永远无法应用。备选：每个用户的提案各自私有。
2. **一个提案只有一类配置步骤**：补丁、修复或一次回滚，各经自己的端点成为恰好一个版本，混合需要两次调用、无法全有或全无；
   缓存清除可以与任何一类混合。备选：把修复与回滚并入一次合并的 `PUT /config/text`。
3. **提案不涉及 `admin`**：其中有禁止的成员，`PUT /config/text?members` 也拒绝它，且只在重启时生效；回滚到 `admin` 不同的版本
   同样被拒绝。
4. **助手从不写入密钥**：名字像密钥的成员下的值必须与配置中同一路径的值相同（抄回的 `[redacted]` 会被还原）或为空；新的密钥
   由人在页面上设置。备选：允许，并在 diff 中标出。
5. **补丁按顶层成员提交**（`PUT /config/text?members=` 只带改动的成员），配置其余部分保持原样，成员顺序由保序的 JSON 树保留。
6. **使整体无效的步骤被保留** 等后续步骤修好；本身就错误的步骤被拒绝。
7. **只有缓存清除的提案永不过期**；应用时再检查其缓存区。
8. **diff 在每次查看时重新生成**，每种格式 256 KiB；一个提案最多 32 步、参数 256 KiB。
9. **diff 处处脱敏**，面板与对话框中与给模型的一样。
10. **对话正在回答时不能应用**（409）；应用与丢弃只在 revision 未变时提交。
11. **审计日志** 只记录调用了工具的回答，不记录普通问答，以免占满上限；每个控制台用户都可读，与控制台日志一样；取消的对话框
    不发送任何内容，不记录。
12. **丢弃** 需要 `off` 以外的任何级别；删除带待处理提案的对话记为一次丢弃。
13. **应用在自己的 context 上运行**，浏览器离开也不会只应用一半。

### I127 Skill

- **做**：skill 格式（name、title、description、inputs、tools、model、prompt、examples），为带 front matter 的 Markdown；上表的
  内置 skill，嵌入；用户 skill 保存在 store 中，数量与大小有上限；Skills 页面可列出、查看、添加、编辑、启用、禁用、导入与导出；
  面板中的 `/name` 与 skill 选择器；系统提示中的目录与取完整提示词的 `skill_load`；skill 的工具与用户级别取交集。
- **完成条件**：每个内置 skill 在固定示例配置上、针对录制模型完成其任务；在页面中添加的用户 skill 可运行并跨重启保留；未指定的 skill 从目录中选出并经 `skill_load` 加载，
  未加载时发出的提示只带目录；指定了超出用户级别工具的 skill 不能调用它；错误的 front matter 被拒绝并给出行号；中英文；在 race 检测下通过。
- **涉及**：`assistant`、`admin/store`、`app/workbench`、`docs`。

**待审查的决策**（实现时所做，2026-11-06；在用户裁定前按此执行）：

1. **谁可以修改技能**：级别 `apply`；其他人只能查看与运行，因为技能会改变所有用户的助手。备选：`propose`；每个用户私有的技能。
2. **整个控制台一套**，所有用户共用，团队技能与 I130 的 MCP prompt 都需要如此。备选：每个用户一个小节。
3. **上限是常量**：64 个用户技能，每个 32 KiB，与 I125 的对话大小一致。备选：`limits.max_skills`。
4. **用户技能不能用内置技能的名字**；要改内置技能，导出、改名、添加并禁用原技能。备选：用户技能覆盖内置技能。
5. **`skill_load` 让技能成为对话的技能**：工具收窄且不再包含 `skill_load`；后续轮次保持该技能直到清除。备选：只加载 prompt。
6. **prompt 只发送一次**：技能生效时其 prompt 在系统提示中，标为 fit 自己的指令且不能覆盖规则，之前的 `skill_load` 结果在请求中
   替换为简短说明（保存与显示的仍是全文）。
7. **目录** 只在提供 `skill_load` 且没有生效技能时发送，最多占窗口的十分之一，超出则只列名字。
8. **目录列出所有启用的技能**，不论用户级别；运行时再收窄工具。备选：隐藏工具全都高于用户级别的技能。
9. **`ask_user`** 总是技能可用的，不论其列出哪些工具。
10. **技能列出本 fit 没有的工具** 时接受并如实显示，使为后续工具编写的技能能够加载；内置技能有测试保证只列出已注册的工具。
11. **技能的模型**：provider 存在时为 `provider/model`，否则视为默认 provider 的模型；从开始对话的那条消息起生效。
12. **`/name`** 只在命名一个启用的技能时才算技能（`/etc/hosts …` 原样发送）；API 拒绝单独的 `/name`（400），面板只设置标签。
13. **输入** 以脱敏的 `<skill_inputs>` 数据放进系统提示，绝不混入用户的话，每项 2 KiB，随对话保存；缺少的输入列给模型。
14. **被禁用或删除的技能** 在下一条消息时离开其对话。
15. **面板的 Markdown** 合并折行的列表项并嵌套缩进项，技能的 prompt 需要这样。

### I128 在工作处询问 AI

- **做**：在以下位置增加“问 AI”按钮，打开面板并预填 skill 与上下文：保存错误（`explain`，带错误与原文）、每个一致性差异
  （`drift-explain`）、每个引擎端口与路由（`diagnose`）、每个表单字段（`explain`，带其 schema 路径）、指标突增或上游下线
  （`diagnose`），以及 LLM 与隧道的链路图。
- **完成条件**：每个入口以正确的 skill 与上下文打开面板，首个回答用到了它们；助手关闭或用户级别为 `off` 时不显示按钮；中英文；
  bundle 测试与浏览器检查通过。
- **涉及**：`app/workbench`、`docs`。

**待审查的决策**（实现时所做，2026-11-06；在用户裁定前按此执行）：

1. **立即发送**：一次点击就发出问题，显示为用户的消息，点一下就得到关于该对象的回答。备选：预填后等用户按回车，每次点击少一次模型调用。
2. **链路图** 用 `diagnose`，针对选中的方框或连线，否则针对整张图，按绘制的样子作为选中内容发送。备选：`explain`；会提议配置的
   `llm-mesh-setup` 或 `tunnel-setup`。
3. **指标突增** 指两次 Overview 刷新之间某个错误计数上升（某上游的失败请求、被拒绝的连接、失败的重载），列出 60 秒，因为 fit 不保存
   指标历史。备选：阈值；带历史的指标页。
4. **Engine 路由** 指列出路由的每个端口条目，以及 Routes 页上的每条路由，因为 Engine 菜单没有单条路由的行。
5. **Consistency 的 info 行** 也有按钮，问题不同，仍用 `drift-explain`。
6. **不发送差异的值**：`drift-explain` 用 `consistency_check` 读取脱敏后的值。
7. **保存错误的文本**：每个问题所在行前后约四行带行号的内容，设置密钥的行在页面上与服务端一样脱敏；表单保存时为问题路径处的草稿值；
   最多 20 个问题。备选：整段文本截断。
8. **表单字段的输入 `path`** 是其 schema 路径（去掉下标与键）；带下标的路径放在选中内容里。
9. **上下文复选框**：点击 Ask AI 会重新勾选面板的“发送本页”，使选中内容随问题发送。
10. **隧道的路径对话框** 在点击 Ask AI 时关闭，以便显示面板。
11. **图标或文字**：表单字段、路由与上游标签以及菜单行用图标（悬停或聚焦时显示名称）；提示框、Consistency 行与链路图卡片用带文字的
    “Ask AI”。
12. **页面上的上限**：字符串 1,500 字符、列表 40 项、选中内容 8 KiB，在服务端 16 KiB 页面上下文之内。

### I129 迁移与审查

- **做**：`migrate`：nginx 与 Caddyfile 原文先经 `fit adapt`，转换器报告无法转换的部分交给模型，Traefik 的 YAML 或 label 完全由
  模型转换，每个结果都是一个提议，并列出无法映射的项；`security-review`：带严重程度、成员与修复提议的发现。一组固定的输入配置
  及期望结果，按需针对选定模型运行，报告有多少能转换为可通过校验并保留路由的配置。
- **完成条件**：该组配置转换并通过校验，未转换项被列出；审查找出其测试集中预设的问题；该组用 `make assistant-eval` 运行，
  提供方由环境变量设置，默认不在 CI 中运行。
- **涉及**：`assistant`、`adapter`、`cli`、`docs`。

**待审查的决策**（实现时所做，2026-11-06；在用户裁定前按此执行）：

1. **评估器** 是 `assistant` 包中的测试代码（`eval_test.go` 及其辅助文件），由 `make assistant-eval` 以
   `go test -run '^TestAssistantEval$'` 运行，复用控制台与 Admin API 的测试夹具，不增加未测试的代码。备选：CLI 命令；单独的包。
2. **没有 CLI 命令**，虽然计划的涉及范围列了 `cli`：计划只提到 `make assistant-eval`，而 `fit adapt` 已经按行打印警告。备选：
   `fit assistant eval`，供用户测试自己的模型。
3. **未转换的项** 是 `config_propose` 可选的 `unconverted` 列表（100 项，每项 500 字节），随步骤保存并与提案一起显示。备选：只写在
   回答文本中。
4. **发现** 是固定格式的行（严重程度、成员、修复），解析时宽松（编号、方括号、中文）。备选：专门的发现工具或事件。
5. **“保留路由”** 指：应用提案、编译、把每个用例的请求送进真实的 `Router.Match`，检查上游、处理器、重定向、拒绝或文本。备选：
   比较 JSON 结构。
6. **配置提到而本机没有的文件**：仅在评估中，缺失的 `*_file` 代表生成的证书，静态 `root` 代表临时目录。
7. **运行中的 `ask_user`** 以第一个选项回答，最多三次。
8. **实际运行有用例失败时以非零退出**，仍打印报告。
9. **`config_get` 读取未设置的成员** 时返回 `null`（`.conf` 与 YAML 中为注释）。
10. **选择器不是密钥**：仅限名为 `key` 且值为 `ip`、`route` 或 `header:<token>` 的成员；`hash_key` 与其他 `key` 仍按密钥处理。
11. **nginx 的 `auth_basic` 按关闭处理**：其路由回答 401，直到用带用户的 `basic_auth` 取代；`return` 位置仍先回答，`auth_basic off`
    重新开放。备选：只警告，像以前一样让内容公开。
12. **nginx 中的 Lua**：跳过 `*_by_lua_block` 块并按行报告；跳过 Lua 字符串、注释与长括号中的花括号。备选：拒绝含 Lua 的配置。
13. **实际运行的设置**：级别 `apply`、40 次工具调用、每个工具结果 64 KiB、16,384 个输出 token，使整份配置容纳在一个提案中。
14. **拒绝把 `llm_proxy` 作为 `FIT_EVAL_KIND`**，提示改用指向 proxy 的 `openai_compatible`。

### I130 基于工具注册表的 MCP 服务器

- **做**：`admin.assistant.mcp`，默认关闭：在控制台的 binding 上 `/mcp` 提供一个 MCP 服务器（Streamable HTTP，POST 与 SSE），
  让外部 agent（Claude Code、Cursor、自己的 SDK）使用面板所用的能力。注册表中的工具即其 tools，已启用的 skill 即其 prompts，
  基础与生效配置、schema、版本与文档即其 resources（`fit://config/effective`、`fit://schema/{path}`、`fit://versions/{n}`），
  脱敏方式与发给模型的相同。在 workbench 的 MCP 页面为每个用户签发 MCP token：名称、过期时间与可选的地址名单，只显示一次，
  以哈希保存在 store 中，并在该页面吊销；token 继承其用户的级别，最高为 `propose`。propose 工具往该 token 的待定提议中添加内容，
  返回提议 ID 与 workbench 链接；没有 apply 工具——提议只能在 workbench 中点击应用并在弹窗中确认后才生效。每个 token 的会话数、
  请求字节、并发调用与每分钟调用数都有上限；允许的地址默认只有回环；每次调用都记入审计日志，来源为 `mcp` 并带 token 名称。
  fit 自身的助手不调用外部 MCP 服务器。
- **完成条件**：MCP 客户端（测试中用官方 SDK，手工用 Claude Code）能列出 tools、prompts 与 resources，读取配置与状态，并提出一个
  在 workbench 中显示、只有确认后才应用的提议；`read` token 拿不到 propose 工具；过期、已吊销或错误的 token，以及不在地址名单中
  的调用方都被拒绝；工具结果与 resources 中不出现任何密钥；面对慢客户端、大请求与中断的流，上限都成立；中英文；在 race 检测下通过。
- **涉及**：`assistant`、`admin`、`admin/store`、`app/console`、`app/workbench`、`schema`、`docs`。

**待审查的决策**（实现时所做，2026-11-06；在用户裁定前按此执行）：

1. **协议修订版**：同一端点支持 2026-07-28 与 2025-03-26 至 2025-11-25，因为最新修订版改变了传输方式，而 Claude Code 等客户端可能仍
   打开会话。备选：只支持最新版；只支持旧版。
2. **设置存放在哪里**：`mcp` 是助手设置的成员，设置页保存后进入 store，经 `PUT /api/m/ai/settings` 编辑。备选：只在重启时读取的成员。
3. **令牌级别**：`read` 或 `propose`；`edit` 只增加不经 MCP 提供的页面工具。`propose` 令牌需要用户为 `propose` 或 `apply`，每次调用
   重新套用用户的级别。
4. **令牌的保存**：256 位密钥保存为 SHA-256（密钥熵已满，不用 bcrypt），按 ID 查找，常数时间比较；没有 store 时保存到重启为止；
   过期超过 7 天的令牌在下次写入时删除。备选：bcrypt；没有 store 时不允许令牌。
5. **谁能看到谁的令牌**：自己的；`apply` 用户能看到并吊销所有人的。MCP 关闭时也可以创建令牌。
6. **地址**：令牌的地址列表只收窄服务器的 `allow`，从不放宽；列表之外的调用方在检查令牌前即得到 403。
7. **超出上限的会话** 结束最久未用的那个，因为客户端很少发送 DELETE；空闲会话在 `session_idle_timeout` 后结束。
8. **并发请求** 超出上限时回答 429，不排队；同一令牌的提议调用依次执行，读取则不。
9. **提案所在的对话**：每个令牌一个，名为 `MCP: <name>`，被删除后重新创建；面板中正忙的对话拒绝 MCP 的提议调用。
10. **审计**：新的类型 `tool`，记录每次 `tools/call`、`resources/read` 与 `prompts/get`，结果为 `ok` 或 `error`；列表调用不记录；与其他
    记录共用上限。
11. **应答**：只有 `tools/call` 用 SSE，其余用 JSON；拒绝批量请求（400）；未知方法在新修订版答 404、旧修订版答 200，与各自规定一致；
    GET 答 405。
12. **缓存**：列表带 `ttlMs: 0` 与 `cacheScope: "private"`；不声明 `listChanged`。
13. **资源** 最多 1 MiB，超出截断并注明。
14. **默认值**：每个用户 10 个令牌，最长 90 天（上限 3650），4 个会话，空闲 1 小时，每个请求 1 MiB，同时 4 个，每分钟 120 个。
15. **作为资源的文档**：生成的配置参考，整体与按章（仅英文）；其他文档不在二进制中。
16. **SDK 测试** 是独立模块（`test/mcpsdk`），用 `go build -C ../..` 构建 fit 而从不 import 它，使 fit 的依赖不增加；由单独的 CI 任务运行。
17. **时钟回拨** 不会让每分钟计数器损失额度。

### I152 助手的媒体库工具与 skill

- **做**：上表中媒体类的工具与 skill，基于 I132–I136 的媒体库、`vod`、频道与 `url_signing`；propose 工具与其他提议一样应用（I126），
  需点击应用并在弹窗中确认；文件类提议在文件被某频道使用或正被某会话播放时给出警告。媒体库页面（I138）上的“问 AI”入口：条目的
  探测错误（`library-health`）、出错的频道推流目标（`channel-scheduler`）、按原因统计的 403（`playback-troubleshoot`）。
- **完成条件**：每个 skill 在固定媒体库上、针对录制模型完成其任务；录制的请求中不出现任何密钥、签名或播放 URL；除非允许，subject
  以哈希形式出现；删除某频道正在播放的文件的提议会指出该频道；在 race 检测下通过。
- **涉及**：`assistant`、`admin`、`medialib`、`app/workbench`、`docs`。

**待审查的决策**（实现时所做，2026-11-06；在用户裁定前按此执行）：

1. **媒体步骤是动作**，像缓存清除一样可与任何类配置修改同在一个提案中，在配置之后、清除之前按顺序应用，某一步失败时结果为 `partial`。
   备选：只含媒体步骤的提案，那样就无法在一次修改中“改名文件并更新频道列表”。
2. **以 `media_tag` 确认所见**，它是是否在用、列出或正在播放该条目的频道、是否有观众播放的摘要；不一致时回答 412 并重新提供提案，只对
   用户看到在用的条目发送 `force`。该摘要不含频道的当前与下一项以及观众数，因为它们每秒都在变。备选：总是 force。
3. **`admin.assistant.data.send_subjects`**，默认关闭，与 `send_logs`、`send_access_log` 并列；否则主体以 `subject-` 加每次启动生成的密钥所做
   HMAC 的 16 位十六进制发送，fit 运行期间稳定；提议工具两种形式都接受。备选：把密钥保存在 store 中使哈希跨重启（会话本身不跨重启）。
4. **地址与会话 ID 原样发送**，因为计划按资源与地址列出会话。备选：地址也哈希。
5. **推流目标的 `url` 作为密钥**，在 `config_get`、提案与页面草稿中都脱敏：此前 `config_get` 会把推流密钥发给模型。
6. **MCP 在 token 的级别内提供媒体工具**；提案只在 workbench 中应用。
7. **应用前已结束或消失的会话** 从结束会话的步骤中去掉并给出警告；只有一个都不再播放时才拒绝该步骤。
8. **只轮换 `secret_file` 中的密钥**；写在配置中的密钥被拒绝，由人修改；没有技能会轮换密钥。
9. **技能的工具**：`library-health` 可提议重新扫描与文件修改，但被告知除非要求绝不删除；`playback-troubleshoot` 可提议结束会话；`vod-setup`
   与 `channel-scheduler` 也有页面工具。
10. **`library-health` 自带 ffmpeg 命令**，因为 `scripts/` 中没有准备文件的脚本。
11. **`TestChatSummarizes` 的窗口** 随工具与提示的增长从 3,600 增至 5,600 token。

### I153 跨集群的助手

- **做**：在管理节点上（I141）提供上表中集群类的工具与 skill；只读工具的 `member` 参数经管理节点的会话访问该成员的 API；propose
  工具转为发布计划或批量操作，显示在发布页面上，只有经其自身的确认才开始（I146）。助手的级别与管理节点按组的角色（I148）都要检查，
  以更窄者为准。集群页面上的“问 AI”入口：概览卡片（`fleet-health`）、失败的批次（`rollout-planner`）、漂移项（`cluster-drift`）。
  MCP 服务器（I130）在管理节点上同样提供这些工具。
- **完成条件**：用自然语言为某组提出的修改成为带每个成员 diff 与试运行的发布计划，且只在确认后执行；`operator` 不能对其无权编辑的
  模板提出修改；录制的请求中不出现任何成员凭据；某成员慢或下线时，跨成员读取仍在上限之内；在 race 检测下通过。
- **涉及**：`assistant`、`cluster`、`app/workbench`、`docs`。

**待审查的决策**（实现时所做，2026-11-07；在用户裁定前按此执行；也记在决定 031 与决定 033 的第 146–156 项）：

1. **`assistant` 与 `cluster` 经一个只含普通类型的接口相连**（`assistant.Cluster`，由 `cluster.Manager.Assistant()` 实现），两者互不导入。
   备选：工具放在 `cluster` 中，或 `assistant` 导入 `cluster`。
2. **用自然语言提出的修改成为两种新的发布之一**：补丁（请求中写出的部分，用于没有模板拥有的配置；需要在每个成员上有 admin），或模板草稿
   （成员按草稿规划，只在集群范围管理员开始发布时保存；模板在此之后被修改则拒绝）。备选：只用模板，或在生成计划时就保存模板。
3. **密钥**：助手的 `[redacted]` 以 `[secret]` 发出，保留已有的密钥；该处没有密钥的成员为未就绪；模型写出的密钥被拒绝。
4. **没有工具会启动任何东西**：`rollout_propose` 返回计划与其页面链接，由该页面自己的确认启动；`cluster_op_propose` 按 `StartOp` 的方式检查
   操作，并链接到已填好确认的操作页面；它们都不是对话提案的步骤，因此“应用”不会执行它们。备选：在管理端保存提议的操作。
5. **提案卡片** 由工具结果的 `link` 绘制，没有新的流事件；经 MCP 时回答中带有工作台的绝对 URL。
6. **计划记录 `source` 与 `suggested`**（策略），开始表单据此预填。
7. **等待**：计划、跨成员读取、成员读取与日志追踪各 20 秒，在对话工具的 30 秒上限之内；届时计划未就绪会说明并继续进行。
8. **`member` 只能读取**：配置、生效配置、检查、版本、状态、指标、缓存、节点、schema、媒体与流媒体的读取，以及文本转换；从不写入，
   不涉及令牌或日志文件。成员运行其他 fit 版本时附加说明。
9. **复用 I150**：证书取自其清单（不调用成员），指标为实时读取加其序列的 12 个点，`cluster_log_tail` 即其 `TailLogs`，受 `data.send_logs` 控制。
10. **不是管理端的 fit 上隐藏集群 skill。**
11. **角色要求与端点一致**：任何角色可读列表；指标、日志与 `member` 读取需要在所读的每个成员上有角色；角色与助手级别以更窄者为准。
12. **问 AI**：概览工具栏与每张成员卡片（`fleet-health`）、失败或已回滚的批次（`rollout-planner`）、每个漂移的成员（`cluster-drift`）；
    `diagnose` 按钮保留。
13. **新建发布表单** 增加“在此写出的部分”（补丁）与模板草稿，只对管理员显示。

### I154 在页面上的修改，由人提交与保存

- **做**：`admin.assistant.users` 中的 `edit` 级别（I124 的设置页面提供它），以及上表的页面工具。面板把每次页面工具调用经对话流
  送到当前页面，页面的回答作为下一个事件返回，等待有超时上限，浏览器离开时结束。页面把调用应用到文本编辑器（I120 的
  `.conf`/`.yaml`/`.json` 标签页）或按 schema 路径对应的表单字段（I122、I123 的功能页面），标出自己起草的内容，并返回结果；
  页面声明自己可编辑的成员与字段，使助手知道当前页面能容纳哪些修改，页面自己对草稿的检查（解析、标签页格式）与手动编辑时相同。
  助手没有提交或保存的路径：页面工具从不调用 `PUT`/`PATCH /config`、`/config/reconcile`、回滚或清缓存；在用户点击页面的
  **保存并应用** 之前，版本、ETag 与 `/versions` 保持不变，而该按钮经页面原有的应用路径执行，记为这个人的修改。草稿与工具
  结果一样被脱敏并受上限约束；页面工具的调用在审计日志中记为工具调用，而不是修改。页面工具不经 MCP 提供（I130），因为 MCP
  没有打开的页面可作用。没有页面能表达该修改或没有打开页面时，助手改为给出提议（I126）。
- **完成条件**：在页面上要求修改某个值时，助手把它写进该页面的编辑器或表单，标为未保存的草稿；不发出任何配置请求，在用户保存
  之前版本、ETag 与 `/versions` 都不变；随后用户的 **保存并应用** 像其他页面修改一样提交并保存为新版本，而助手没有为它发出任何
  请求；草稿可撤销，页面恢复到起草前的原文；页面无法表达的修改变成提议；`read` 用户拿不到页面工具，`edit` 用户拿得到；触及密钥
  字段或禁止成员的页面修改被拒绝；超过上限的草稿被拒绝并说明；中英文；浏览器检查通过；在 race 检测下通过。
- **涉及**：`assistant`、`app/workbench`、`admin`、`docs`。

**待审查的决策**（实现时所做，2026-11-06；在用户裁定前按此执行）：

1. **页面如何应答**：SSE 流保持打开时调用 `POST /api/m/ai/chat/page`，应答作为调用结果返回，浏览器离开时结束。备选：像 `ask_user`
   那样结束流，再用新请求继续。
2. **何时提供页面工具**：只在页面上下文声明了 `editable.members` 时，因此取消“把本页作为上下文发送”也会去掉它们。备选：总是提供，
   由页面拒绝。
3. **`page_get` 是 `read` 级别**，与计划表一致；read 用户会被告知不能起草。
4. **草稿格式** 与 `config_propose` 相同（`members`、JSON pointer `ops`），或带 `format` 的 `text`，模型只需学一种。备选：点分 schema 路径。
5. **两次检查**：fit 检查（页面的成员、不含 `admin`、不含密钥、能解析、256 KiB），页面再按其草稿与 `/config/parse` 检查；fit 解析
   失败的文本不会进入编辑器。
6. **密钥**：名字像密钥的成员下只允许抄回的 `[redacted]`，由页面还原；从仍存在的对象中删掉密钥字段被拒绝；任何 `[redacted]` 都
   按草稿中的值放回，没有则拒绝。备选：像 I126 那样允许空值或删除。
7. **`add` 会在页面上创建缺失的父级**，与 `config_propose` 的 ops 不同，因为草稿常常要加到页面还没有的成员中。
8. **撤销** 恢复助手第一次起草之前的快照，含原始文本；用户编辑过之后，助手的 `page_revert` 被拒绝，页面自己的撤销会先询问。
9. **文本草稿** 原样留在其标签页，除非含 `[redacted]`，此时由草稿重新渲染该标签页，使保存的文本带真实密钥。
10. **所有页面共用一份草稿**：存在助手草稿时，每个编辑页都显示提示、标记与撤销，因为配置页共用一份草稿。
11. **技能**：五个修改类技能列出页面工具，`explain` 列出 `page_get`；`migrate` 与 `security-review` 归 I129。
12. **页面的等待** 为 30 秒，与 `toolTimeout` 一样是常量。备选：`limits` 中的设置。

### 审核结论（2026-11-04）

- 批量提议：允许；模型在一轮中的多次 propose 调用合成一个提议，作为一次修改应用。
- 不做无人值守应用：每个修改都必须点击应用，并在弹窗中二次确认后才生效并保存配置。
- 会话保存在 store 中，跨重启保留，受 `limits` 的上限约束。
- 不提供 CLI：助手只在 workbench 中。
- MCP（2026-11-05）：fit 基于同一注册表、级别与提议向外部 agent 提供 MCP（I130）；应用仍只在 workbench 中；fit 的助手不调用外部
  MCP 服务器。
- 页面上的修改（2026-11-05）：助手可以把用户要求的修改起草到当前页面的编辑器或表单字段中；它从不提交或保存——**保存并应用**
  仍由人完成——在此之前版本、ETag 与审计都不变；页面无法表达的修改以提议方式给出（I126）；页面工具不经 MCP 提供（I130）。

## 媒体库：点播与频道

2026-11-05 提出。影视站把电影和剧集存为文件，希望在 Web 上点播、也按排期作为频道播放，只给自己的 API 放行的观众看，而且只在一段时间内有效。
fit 为此增加媒体库：把目录中的文件编目，不转码地以 HLS 点播，也作为直播频道推入流媒体 hub，按需再推到其他服务器；以及会过期的播放
URL：由影视站的 API 为某个观众、某个片子签发，fit 在每个播放列表、分片和密钥请求上校验。它建立在流媒体角色（I116）、媒体认证（I115）
和媒体 token（I119）之上；决定 032 记录下面的选择。

原则，按约束力排序：

- **文件事先准备好，不转码。** fit 按文件存储的原样提供其中的编码（H.264、H.265、AV1；AAC、Opus）。多码率是各自独立、事先做好的文件；
  `scripts/` 中给出用 ffmpeg 制作的脚本。进程内转码不做，与流媒体角色一致。
- **不阻塞 worker**（规则 1）：探测文件、读取样本表和读取分片字节都在媒体库自己的有上限的文件池上执行；请求被保留等待，由池继续。目录、
  样本表和生成的分片各有字节上限（规则 11）。
- **谁能看由影视站的 API 决定，fit 负责校验。** fit 不保存影视站的账号。API 自己或经 `media_tokens` 为观众、资源和有效期签发 URL，fit
  在 worker 上立即校验，不回调。
- **泄露的 URL 很快失效，也打不开别的。** URL 有效期短，绑定一个资源（一部片子或一个频道），按需绑定观众地址或会话；播放器取的每个播放
  列表、分片、密钥和字幕都带着授权；每个主体的播放会话数有上限。
- **默认关闭**：没有 `media_library` 就不提供文件、不扫描。

不在本方向内：转码、带 license 服务器的 DRM（Widevine、FairPlay、PlayReady）、按观众加水印，以及对象存储中的媒体库（有需求时作为后续
迭代；文件池把读取放在一个接口后面，以便日后加入）。

### I131 决定 032；fib 中的 MP4 读取器

- **做**：决定 032。在 [fib-api-requests.md](fib-api-requests.zh-CN.md) 中向 fib 提出 A64：非分片 MP4（ISO BMFF）文件的读取器，放在
  fib 的 `media/fmp4` 或新的 `media/mp4` 中；通过 `io.ReaderAt` 解析 `moov`（`stts`、`ctts`、`stss`、`stsc`、`stsz`/`stz2`、
  `stco`/`co64`、编辑列表），不读整个文件；`moov` 在文件尾部也能找到；像 `media/av` 那样给出每条轨道的编码配置；按时间和关键帧查找样本；
  样本表以紧凑形式保存，构建前即可知道其大小；做模糊测试。fib 加入后 fit 升级到该提交。
- **完成标准**：fib 的测试读取 ffmpeg 生成的各编码组合文件，`moov` 在头部和尾部、带 B 帧和编辑列表，每个样本的时间、偏移和大小都与
  ffprobe 一致；样本表内存在其上限内；对读取器的模糊测试没有 panic。
- **涉及**：`docs/decisions`（032）、`docs/fib-api-requests.md`、fib 的 `media`、`go.mod`。

**待审查的决策**（实现时所做，2026-11-06；在用户裁定前按此执行）：

1. **读取器放在哪里**：fib 的新包 `media/mp4`，使用导出的 `fmp4.ParseSampleEntry`；`fmp4` 读取内存中的分片字节，而这里通过 `ReaderAt`
   读取文件并保存样本表；编解码器在一处解析。备选：放进 `media/fmp4`；两者共用的内部包。
2. **读取器不持有文件**：`Open` 不保存引用；`ReadTable(r)` 与 `ReadSample(r, …)` 接收读取器，使 fit 的文件池可以关闭并重新打开文件。
   备选：`File` 持有读取器。
3. **紧凑的样本表**：`stts` 与 `ctts` 游程编码（逐样本更小时 `ctts` 逐样本存），`stz2` 用 16 位大小，每个 chunk 一个偏移与首样本；带
   B 帧时约每样本 8.4 字节。备选：每样本 24 字节以上的条目，更快但内存为三倍。
4. **编辑列表**：偏移为开头的空编辑减去第一个播放编辑的媒体时间，所有测试文件都与 ffprobe 一致；末尾裁剪与之后的编辑保留但不应用。
   备选：应用全部编辑。
5. **时间是文件的 tick**：`DTS` 与 `PTS` 与 ffprobe 加 `-ignore_editlist` 时相同；`Table.Time` 应用编辑列表；给出 `MinCTS` 而不是平移 DTS。
6. **严格与宽松**：缺样本的表、文件之外的 chunk 与分片文件被拒绝；过短的 `ctts`、未排序或越界的 `stss` 与有符号的 `ctts` v0 按播放器
   的方式读取。
7. **`SeekKeyframe` 按 PTS 查找**，假定关键帧按解码顺序呈现，fib 支持的所有编解码器都是如此。
8. **上限是常量**（`MaxTracks`、`MaxBoxes` 等），不是选项。
9. **测试文件**：七个小文件入库（连同 ffprobe 输出共 129 KB，由 `media/mp4/testdata/gen.sh` 生成），较长的文件由 CI 中 `media/interop`
   的 `TestMP4` 检查。
10. **fib CI 的 fuzz 任务** 只加入 `media/mp4:FuzzOpen`；其他媒体 fuzz 目标原本就不在其中，保持不变。

### I132 媒体库

- **做**：顶层 `media_library`：命名的媒体库，各有 `roots`（目录）、包含与排除模式和 `scan_interval`；启动时、按间隔以及
  `POST /media/library/<name>/rescan` 时扫描；条目目录，每个条目有稳定的 `id`（来自其在根目录下的路径，或 sidecar 中指定）、标题、时长、
  轨道与编码、大小、多码率（按命名规则或 sidecar `<name>.json` 分组的文件）、音轨、字幕（`.vtt`，`.srt` 转为 WebVTT）和海报；探测在文件池上
  进行，受条目数、并发探测数和 sidecar 大小限制；探测失败的文件连同错误列出，但不提供；根目录之外的路径被拒绝（`..`，以及指向外部的符号
  链接，除非 `follow_symlinks`）；目录按 engine 保存，重载时原地修改，与 `media_auth` 的策略一样；Admin API 的
  `GET /media/library/<name>` 和 `GET /media/library/<name>/items/<id>`。
- **完成标准**：一个由 ffmpeg 生成文件组成的媒体库列出每个条目，时长和编码与 ffprobe 一致，多码率和字幕正确分组；新增、修改或删除的文件在
  重新扫描后体现；损坏的文件连同错误列出；根目录之外的路径或符号链接被拒绝；上限和重载在 race 检测下成立。
- **涉及**：`medialib`（新）、`config`、`app`、`admin`、`schema`、`docs`、`test/workers`。

**待审查的决策**（实现时所做，2026-11-06；在用户裁定前按此执行）：

1. **模式字段的名字** 是 `files` 与 `exclude`，而不是计划中的“include 与 exclude”，因为 `include` 是 `.conf` 格式的指令。备选：`match` 与
   `ignore`；一个块。
2. **模式语法**：不含 `/` 的匹配文件名，含 `/` 的匹配根目录下的路径，`**` 跨目录；`exclude` 也剪掉目录；点开头的名字总是跳过。备选：
   gitignore 的规则；正则表达式。
3. **ID** 在 URL 中安全时为根目录下去掉版本标签与扩展名的路径；否则不安全的部分变为 `-`，后接路径哈希的 8 位十六进制，使 ID 稳定且唯一。
   备选：可逆转义（中日韩文件名不可读）；只在冲突时加哈希（出现冲突文件时已有 ID 会改变）。
4. **重复的 ID**（跨根目录或 sidecar）：按根目录与路径顺序第一个胜出，其余的报告出来。备选：ID 中带根目录。
5. **命名**：版本标签为 `NNNp[fps]`、`Nk`、`Nkbps` 与 `audio[-lang…]`；字幕与海报归到基名匹配最长的条目；sidecar 的列表与以它命名的文件
   合并，可引用子目录但不能高于自身所在目录。
6. **每个库一个池**，立即创建，默认值与 static 处理器相同（每 CPU 4 个，至少 16；队列 4096；10 秒）；`max_probes` 防止扫描挤占服务。
   备选：每个引擎一个池。
7. **样本表缓存放在 `medialib`**，由 I133 与 I134 在同一上限（`max_table_bytes`）下共用，连同 `Table` 与 `Subtitle`；为此新增
   `max_table_bytes`、`max_subtitle_bytes`、`file_workers`、`file_queue` 与 `file_timeout`。
8. **sidecar** 每次扫描重新读取，一次扫描中的字节数受 `max_catalog_bytes` 限制。
9. **默认值**：`scan_interval` 10 分钟（至少 10 秒，不能关闭），`max_items` 20,000，`max_files` 200,000，`max_catalog_bytes` 64 MiB，
   `max_probes` 4，sidecar 64 KiB（最多 1 MiB），字幕 2 MiB，样本表 128 MiB。
10. **Admin API 查找库** 用上游引擎上的 `medialib.Lookup`，而不是经过 `app.Manager`，后者的锁在整个应用过程中都被持有。
11. **`HasApps` 不计 `media_library`**，与 `media_auth` 一样：配置仍需要 listener 或路由。
12. **测试文件的换行**：`medialib/testdata/.gitattributes` 设为 `* -text`，使 Windows CI 不改动逐字节比较的字幕。

### I133 点播

- **做**：基于媒体库的处理器 `vod`：每个条目一个主播放列表（多码率作为 variant，带 `BANDWIDTH`、`RESOLUTION` 和 `CODECS`；音轨组与字幕
  组），媒体播放列表由 fMP4 分片组成，或为旧播放器用 MPEG-TS（`container`），按接近 `segment_duration` 的关键帧切分，请求时从样本表生成并
  放入有上限的缓存；初始化分片；文件本身支持 `Range`（206、416），供下载和简单播放器使用，fib 允许时走 sendfile；`ETag` 和
  `Last-Modified` 来自文件，`Cache-Control` 由路由设置，便于 CDN 缓存分片；`auth` 使用 `media_auth` 策略，动作 `play`，流名
  `vod/<library>/<id>`。
- **完成标准**：无头 Chromium 中的 hls.js 和 ffmpeg 能播放每个条目并拖动进度，每个分片都从关键帧开始，音频与视频相差不超过一帧，Safari
  原生播放器手动验证；ffprobe 认为每个分片都有效；播放中被修改或删除的文件返回 404 并清除其缓存；上限在 race 检测下成立；在 Linux 上测量
  每秒分片数和 RSS，对比 nginx 提供事先切好的同样文件。
- **涉及**：`handler/vod`（新）、`medialib`、`schema`、`docs`、`test/workers`、`bench`。

**待审查的决策**（实现时所做，2026-11-06；在用户裁定前按此执行）：

1. **URL 名字** 平铺在条目的目录下（`index[.c].m3u8`、`v0.c.m3u8`、`v0.N.m4s`、`v0.mp4` …），两种容器都始终可用，`container` 只决定
   `index.m3u8` 是哪一种。备选：每种容器一个目录；像 `hls` 处理器那样用 `?format=` 查询参数，这对 CDN 的缓存键与 I135 的路径签名都不友好。
2. **两种容器共用一个时钟**，文件的 0 时刻在 1.4 秒，与 ffmpeg 的 MPEG-TS 相同，使 fMP4 不需要编辑列表也保留 B 帧与 AAC 预滚的时间，字幕
   只需一个 `X-TIMESTAMP-MAP`。备选：fMP4 从 0 开始并在 init 中放编辑列表，播放器对此支持不一；每个条目一个偏移。
3. **分段位置**：离起点加 `segment_duration` 最近的关键帧，但不早于其一半；文件末尾算作关键帧；`max_segment_bytes` 计入音频。备选：
   ffmpeg 的“到达时长时或之后的第一个关键帧”，分段更长也更不均匀。
4. **分段中的音频** 是在其视频下播放的那部分，使跳转后立即有声音；备用音频文件按自己的网格切分。
5. **主音频**：只有每个版本都带音频时，版本自带的音频才作为音频组的默认项（不带 URI）；否则用 sidecar 的默认音频文件，或第一个。
6. **BANDWIDTH** 是目录中的平均码率（加上最大的备用音频），使多版本播放列表无需读取样本表。备选：峰值，需读取每个版本的样本表。
7. **缓存**：每个 store 一个 LRU，存放切分计划、媒体播放列表（仅无查询参数时）、init、分段与 WebVTT；构建只在其池上的工作开始时占用
   字节；大于缓存的分段照常提供但不保存；找不到空间的构建回答 503。
8. **缓存共享**：参数相同的处理器在重载之间共用，与 `hls` 处理器共用 muxer 一样。
9. **`cache_control` 是处理器的参数**，默认不设，而计划说由路由设置，因为 headers 中间件分不清分段与 404；ETag 由文件的时间与大小，加上
   类型与切分参数的哈希构成。
10. **上限**：`max_segment_bytes` 32 MiB（64 KiB 到 1 GiB 与缓存四分之一中较小者之间），`segment_duration` 1 到 60 秒，`timeout` 30 秒。
11. **状态码**：库尚未应用或首次扫描之前回答 503 并带 Retry-After 1；过大或没有关键帧的分段回答 500。
12. **Range 发送** 从 `handler/static/send.go` 复制，而不是移到共用的包，因为 static 的测试用到其内部实现。
13. **只播放文件的第一条视频与音频轨道**；不提供 MP3（两种 muxer 都不支持）。
14. **`test/workers`** 把 `vod` 列在 `notCovered` 中，指向 `handler/vod` 的 `TestNoWorkerBlocks`，因为它需要库角色与一次扫描。
15. **ffmpeg 的跳转检查** 只在 ffmpeg 能在它自己切分的同一文件中跳转时进行，因为 6.1 与 7.1 在其自己的 fMP4 HLS 上都会失败。
16. **hls.js 1.5.20** 在构建测试镜像时下载，不随仓库提供（I137 可以收入）。
17. **基准测试**：fit 运行于 `ubuntu:24.04`，nginx 用其镜像，均为主机网络，使用 CPU 0–3，wrk 使用 8–15；合成的库；nginx 开启
    `open_file_cache`。
18. **无需 fib 请求**：`fmp4.Fragment`、`AppendInit`、`mpegts.Muxer` 与 `SendFileStall` 已足够。要缩小缓存分段与 nginx 的差距，需要不在用户
    空间复制地发送它们（基于内存的 SendFile，或从 tmpfs 缓存 sendfile）：尚未提出。

### I134 频道：把文件作为直播流播放并向外推流

- **做**：`streaming.channels`：频道以其流名向 hub 推送一组媒体库条目，按顺序、随机、循环或按某时区的排期开始时间播放，用 fit 的定时器按
  样本时间戳控制节奏，条目之间时间戳保持连续（HLS 中为 discontinuity）；流媒体角色的每种出口都能播放它（HLS、HTTP-FLV、WHEP，以及
  RTMP、RTSP 和 SRT 播放），各按其 `auth`；`push` 目标把频道或 hub 中任意流转推到外部 RTMP 或 SRT 服务器（如 CDN 的接入点），断开后带退避
  重连；频道在重载时原地修改，新的列表或排期从下一个条目起生效；Admin API 列出频道和推流目标，并可开始、停止和跳过。
- **完成标准**：一个频道连续播放两个条目，经每种出口用 ffmpeg 和 hls.js 播放，没有间断，时间戳没有跳变；排期的条目按时开始；推流目标
  （fit 自身，以及容器中的外部服务器）收到流，并在目标重启后恢复；在 race 检测下通过。
- **涉及**：`streaming`、`medialib`、`config`、`schema`、`docs`、`test/workers`。

**待审查的决策**（实现时所做，2026-11-06；在用户裁定前按此执行）：

1. **独立的包** `streaming/channels`，供 Admin API 导入，因为 `streaming` 的测试导入了 `admin`，放在 `streaming` 中会形成循环。备选：
   接口背后的 `app.Manager.Role(id)`。
2. **只在轨道不同时调用 `SetTracks`**，使编码相同的影片无缝连续；fib 的各出口在每次 `SetTracks` 时都会平移时间戳（A68）。备选：每个条目都
   调用，每个条目一个不连续点。
3. **条目按呈现时间衔接**；完全在起点之前的音频（AAC 预滚）被丢弃；条目在其最晚轨道的起点加时长处结束。备选：按解码时间，留下约 120 ms
   的间隙。
4. **“按时开始”** 指时段的第一帧画面在频道时间线上该时段的时刻显示，视频最多提前 B 帧延迟发送。
5. **已在进行中的时段** 在启动时、或重载唤醒空闲频道时，从其位置之前的关键帧加入；重载时已经过去的时段被跳过。
6. **没有可播放的内容**：频道保持发布且静默；不循环的列表播完后，先停止再启动会从第一个条目重新播放。
7. **默认值**：像素最多、其次码率最高的版本；音频退回到条目的默认音频文件；`time_zone` 为 UTC；`loop` 开启。
8. **上限**：`read_ahead` 3 秒（0.5 秒到 1 分钟），`max_buffer_bytes` 16 MiB，每次读取一秒；包迟到超过一秒时频道暂停，计为一次欠载。
9. **推流**：流未发布时目标每 `retry_min` 轮询一次；订阅等待 5 秒；连接保持 `retry_max` 之后退避重新开始；重载时流、URL 与
   `connect_timeout` 不变则保留连接。
10. **名字**：没有名字的推流目标为 `stream#n`，顶层的在前；频道状态列出其流的所有目标；经 Admin API 的操作不保存到配置。
11. **互通测试** 推流到 MediaMTX 1.15.3；Linux 测试程序在带 ffmpeg、Chromium 与 hls.js 1.5.20 的 Debian trixie 镜像中运行。
12. **fib A68 按轨道类型比较**，而不是由 hub 调用的 `hub.TracksChanger`：调用方无需改动，而“同一发布者即同一时钟”对中途重新开始的
    SRT 编码器并不成立。WHEP 现在对同类的任何回退都重新计时（以前放过最多一秒的回退）；FLV 与 RTMP 的序列头带其所属轨道的最后
    时间；HLS 在新 writer 的第一个包时决定；类型分为视频与其余（两条音频轨道共用一个）。

### I135 会过期的播放 URL

- **做**：`media_auth` 中的签名播放 URL：`url_signing`，用策略 `keys` 密钥环（I119）中的一个密钥签名，每个 URL 带 `exp`、`kid`，按需带观众
  地址（`ip`，或其 /24、/64）和会话 `sid`，以及 `sig`：对资源路径前缀（`/vod/<library>/<id>/` 或频道的路径）、过期时间和所绑定字段的 HMAC；
  在 worker 上校验，不回调。播放列表给其分片、初始化分片、密钥和字幕发放该资源自己的授权，放在它们的 URI 中，或作为资源路径上的 cookie
  （`cookie`，供会丢弃查询参数的播放器），有效期为 URL 过期时间加 `play_grace` 与条目时长二者中较长者，使电影不会播到一半停止，而泄露的
  URL 仍会失效。`media_tokens` 增加 `POST <prefix>/urls`：影视站的 API 给出观众（主体、地址）、资源和不超过 `max_ttl` 的 ttl，直接拿回完整
  URL。`max_sessions` 按 `sid` 统计每个主体的播放会话，拒绝新会话或结束最早的（`on_limit`）；`referers` 和 `origins` 白名单；吊销通过轮换
  密钥，与 I119 相同。token 和签名不写入任何日志。
- **完成标准**：`/urls` 返回的 URL 能在 hls.js 中播放；过期后其播放列表返回 403，而正在播放的播放器可以播到授权结束；一个条目的 URL 用于
  另一个条目，或路径改动任何一个字节，都被拒绝；绑定地址的 URL 从其他地址访问被拒绝；`max_sessions: 2` 时第三个会话被拒绝，或结束最早的；
  轮换密钥只拒绝它签发的 URL；访问日志显示 `REDACTED`；在 race 检测下通过。
- **涉及**：`mediaauth`、`handler/vod`、`handler/mediatokens`、`streaming`、`config`、`schema`、`docs`、`test/workers`。

**待审查的决策**（实现时所做，2026-11-06；在用户裁定前按此执行）：

1. **资源** 是路径到最后一个 `/` 为止（`/x/cam.flv` 即 `/x/cam/`），各处理器按自己算出的资源检查。备选：签名流名，但那样 URL 不绑定路由。
2. **每个 URL 都带 `sub`**，用于按主体统计会话并在访问日志中给出用户；文档建议用不透明的 ID。备选：哈希后的主体，I138 的会话列表就
   无法显示。
3. **子授权的有效期** 为 URL 过期时间与“现在加条目时长”中较晚者，再加 `play_grace`，并由用它获取的播放列表原样传递，泄露的授权无法
   续期；直播流为过期时间加 `play_grace`。
4. **签名** 为 16 字节的 HMAC-SHA256（约 160 字节查询参数），所用密钥与 token 密钥分开派生，token 的 MAC 永远不会被当作 URL 的签名
   通过；只有一个 secret 时省略 `kid`。
5. **默认值**：`ttl` 10 分钟、`play_grace` 30 分钟、`session_idle` 2 分钟、`max_tracked_sessions` 100,000，每秒最多清扫一次；超出上限时
   新会话回答 503。
6. **每次 `/urls` 请求一个观众**，最多 `max_tokens` 个路径，未给出时随机生成 `sid`，路径前加 `url_base`。备选：批量观众。
7. **Cookie**：cookie 模式只把授权放在 cookie 中，使播放列表仍可缓存；cookie 在 TLS 下为 `Secure; SameSite=None`，否则为 `SameSite=Lax`；
   应答 URL 授权的播放列表带 `private, no-store`。
8. **`referers` 与 `origins`** 是来源模式；没有该请求头的请求放行。
9. **URL 被拒** 时回答 403 并在正文中给出原因，按原因计数；请求同时带有 token 或 Basic 时，错误的 cookie 退回到那些检查。
10. **脱敏** 放在 `vod`、`hls` 与 `httpflv` 的最前面，顺带修复了提前应答时把 `?token=` 记入日志的问题；拒绝时现在带上处理器的 `cors`。
11. **I133 的 `TestPoolGivesUp`** 在负载下约十次失败一次，现在先等池排空。
12. **互通测试框架**（`test/docker/vod`）经 `url_signing` 提供服务，`max_sessions` 为 1 且 `end_oldest`；`LOCK` 是 `e2e.sh` 的新选项，
    `collector.py` 代表网站的 API。

### I136 加密分片

- **做**：`vod` 和频道 HLS 上的 `encrypt`：分片用 AES-128 加密（`METHOD=AES-128`，MPEG-TS 和 fMP4 均可），每个条目或频道一个从媒体库密钥派生
  的密钥，频道每 `key_rotation` 个分片轮换一次；密钥 URI 只对持有该资源授权的请求提供，使没有授权而复制走的分片无法播放；加密在文件池上进行，
  缓冲区有上限。
- **完成标准**：hls.js 和 ffmpeg 能播放加密的条目和频道；没有授权或授权属于其他资源的密钥请求被拒绝；单独取得的分片无法解码；在 Linux 上测量
  每个分片的开销。
- **涉及**：`handler/vod`、`streaming`、`medialib`、`schema`、`docs`。

**待审查的决策**（实现时所做，2026-11-06；在用户裁定前按此执行）：

1. **加密需要 `auth`**：`vod` 的 `encrypt` 要求它，没有 `auth` 的 `hls` 处理器不提供频道密钥（403），因为公开的密钥什么也保护不了。
2. **每个条目一个密钥，IV 为分段序号**，不写出：条目的各版本共用密钥与同一序号的 IV，只泄露它们的首块是否相同。备选：每个版本一个密钥；
   每个分段写出 IV，需要每个分段一个密钥标签。
3. **init 保持明文**：影片播放列表中密钥在 `#EXT-X-MAP` 之后，频道播放列表在之后的 MAP 前写 `METHOD=NONE`。备选：init 也加密，并写出 IV。
4. **密钥的名字**：条目为 `aes.key`，频道为 `key-<session>-<epoch>.key`。
5. **密钥由派生得到，从不保存**：以库从其 `secret` 派生的密钥计算 HMAC-SHA256（覆盖类型、资源与周期），取前 16 字节；同一 secret 的多个
   fit 提供相同的分段；更换 secret 会改变所有密钥与 ETag，CDN 中的旧副本必须清除（已写入文档）。
6. **频道的密钥包含 Muxer 的会话**，重启后绝不会重用密钥与 IV；密钥只提供到下一个分段的周期，观众无法提前取得之后的密钥。
7. **加密的 `vod` 不提供明文文件**（404），因为下载会使加密失效；字幕与海报保持明文。
8. **频道只加密 HLS**；RTMP、RTSP、SRT、HTTP-FLV、WHEP 与推流在各自的 `auth` 下保持明文。
9. **加密频道不提供低延迟 HLS**（去掉其标签，part 回答 404），因为在 fib 之外无法加密 part。
10. **频道加密是 fit 中的临时方案**，直到 fib 请求 A69 让 fib 的 Muxer 能够加密：每个分段复制一次并在库的池上加密一次，在处理器的
    `max_bytes` 内缓存，重写播放列表。备选：A69 之前不提供加密频道。
11. **`key_rotation`** 默认 10 个分段（最多 100,000），没有 `encrypt` 时无意义。

**跨新轨道的低延迟 HLS**（fib A71，I136 之后，2026-11-06），待审查的决策：

12. **在 fib 的播放列表与分段结尾处修复，不在 hls.js 或 fit 中绕过**：分段第一个 part 的 preload hint 写在该分段的 discontinuity 与
    `EXT-X-MAP` 之后（备选：`TYPE=MAP` 的 preload hint，hls.js 不理会）；hls.js 为 fragment hint 保留旧 init（`mergeDetails`）、以及按
    part 读过的分段的 `EXTINF` 放置下一个不连续点，都视为需要适应的播放器行为，没有向上游报告。
13. **新的 `EXT-X-MAP` 之后，hint 再次写出当前生效的密钥**，而非下一个分段的密钥：在那个分段切出之前，任何播放列表都不写它的密钥
    （`KeyEpoch`）；hls.js 从列出其第一个 part 的播放列表读取下一个分段的密钥。
14. **因新轨道、流结束或时钟跳变而结束的分段，时长到其各轨道中最早结束的那个为止**（每个轨道的最后一个包加上它之前的步长；在最后一个
    part 开始之前就结束的轨道不计）：音频与视频结束时刻不同时，下一个条目与较长轨道的尾部重叠，而不是留下空洞，因为播放器会替换重叠的
    部分，却会在空洞处卡住。备选：只取视频的结尾，音频先结束时会留下只缺音频的空洞。
15. **保持现状**：新轨道的第一个关键帧之前轨道又改变一次时，此前的播放列表已在 hint 处写出第一次改变的 init；hls.js 会沿用它。罕见
    （发布者在一个关键帧间隔内再次设置轨道），未处理。
16. **互操作测试**：`live/secll` 重新播放轨道不同的条目（a 与 c），并新增明文的 `live/mixll`，两者都必须按 part 加载过不连续点；
    ffprobe 读取 `live/mixll` 的低延迟播放列表；出现致命错误时页面报告其加载与追加日志。

### I137 影视站端到端

- **做**：[fit-examples](https://github.com/lesismal/fit-examples) 中的示例：一个媒体库、`vod` 和频道路由、置于路由中间件之后供影视站 API
  使用的 `media_tokens`、一个让观众登录并发放 URL 的小型 Go API，以及一个使用 hls.js 的页面。`media_pages` 增加在策略登录之后的媒体库页面和
  播放页面（hls.js 随附，Apache-2.0）。`docs/media-library.md`，中英文。
- **完成标准**：在 Linux 的 Docker 中，无头 Chromium 登录示例 API，经其签发的 URL 播放一部电影和一个频道、拖动进度，并在 URL 及其授权过期
  后被拒绝；`TestChapterExamples` 和 schema 描述覆盖新字段。
- **涉及**：`handler/mediapages`、`schema`、`docs`、`test/docker`、fit-examples。

**待审查的决策**（实现时所做，2026-11-06；在用户裁定前按此执行）：

1. **新字段**：`libraries` 为列表，`vod` 为含 `{library}` 与 `{id}` 的 URL 模板，`channels` 为开关且需要 `hls`。备选：每个处理器一个库；
   从配置的路由读取 `vod`。
2. **播放页自己签名**，经策略的 `url_signing` 而不是 `media_tokens`，因此不需要额外的凭据；主体是用户名，因为这些用户是 fit 自己的员工；
   每个 URL 绑定请求者的地址，每次播放一个新会话，有效期为 `url_signing` 的 `ttl`；没有 `url_signing` 时用普通 URL 加登录 cookie。备选：
   不绑定地址；哈希后的主体。
3. **列表经过过滤**，只显示观众可以播放的内容，其余隐藏。备选：全部列出，播放时拒绝。
4. **上限**：16 个库，每页 60 个条目（最多 500），最多列出 1,000 个频道，页面对尚未扫描的库最多重试 30 次。
5. **hls.js 1.5.20**，与测试框架相同的版本，原样收入（包括 `sourceMappingURL` 行），页面开启时一直提供，同一站点的页面（如示例的页面）都能
   加载。
6. **该依赖** 作为决定 032 中的 I137 条目记录，而不是单独的决定。
7. **示例的 API** 放在 fit-examples 的根模块中，使 CI 的 `go vet ./...` 覆盖它；`golang.org/x/crypto` 成为那里的直接依赖。备选：嵌套模块。
8. **提交了占位的 secret**（`video-site/secret`，“replace-me”），因为 `media_tokens` 需要 `secret_file`；注释说明要替换。
9. **示例的有效期**：URL 2 分钟，`play_grace` 30 分钟，`max_sessions` 2 且 `end_oldest`；观众 alice 与 bob，员工 `staff`，口令与其他示例
   一样是 `example-password`。
10. **端到端测试** 放在 fit 中（`test/docker/videosite`），以 `EXAMPLES` 接收 fit-examples 的检出，用 Python 标准库经
    `--remote-debugging-pipe` 驱动 Chromium，并通过修改示例的 `fit.conf` 缩短有效期，那几行改变时会明确失败。
11. **导出 `mediaauth.Whois` 与 `ViewerAddr`**，而不是把 token 与地址的逻辑复制到 `mediapages`。

### I138 workbench 的媒体库页面

- **做**：在 workbench 中、登录之后管理本方向的全部功能，中英文。
  - **菜单**：扁平菜单中在流媒体和媒体认证旁增加 `media_library` 页面（I122）；引擎菜单（I123）中每个端口的流媒体条目列出其 `vod` 路由和频道，
    端口行增加 `VOD` 标签。
  - **配置页面**：`media_library`（根目录、模式、扫描间隔、上限、`follow_symlinks`）、`vod` 路由（媒体库、容器格式、分片时长、缓存、`encrypt`）、
    带 `push` 目标的 `streaming.channels`，以及 `media_auth` 的 `url_signing`（绑定字段、`play_grace`、`cookie`、`max_sessions`、`on_limit`、
    `referers`、`origins`），与所有配置页面一样有 UI、`.conf`、`.yaml` 和 `.json` 标签，经应用路径校验（I120）。
  - **媒体库浏览**：每个媒体库的条目以表格展示，经 Admin API 搜索、筛选（编码、分辨率、探测错误）和分页；条目页面显示其轨道、编码、多码率、
    字幕、海报和 sidecar，探测错误（如有），以及一个预览播放器（hls.js），使用 workbench 为当前登录用户签发的短期 URL；重新扫描媒体库并显示进度。
  - **频道**：以时间线编辑每个频道的列表或排期（从媒体库拖入条目、按其时区设置开始时间、循环和随机），显示正在播放和接下来播放的条目及进度，
    开始、停止和跳过，以及预览；每个推流目标的状态（已连接、重试中、最近的错误）、字节数和码率。
  - **播放 URL 与会话**：一个经 `media_tokens` 签发测试 URL 的表单（主体、资源、地址、ttl），带播放器展示，从不保存；策略的密钥及其名称和
    使用时长（从不显示密钥本身），确认后轮换一个或全部，与 I119 相同；按主体、资源、地址和开始时间列出当前播放会话，每个都可结束，并显示每个
    主体相对 `max_sessions` 的会话数。
  - **指标**：每个媒体库和频道的当前观看人数、每秒请求数和字节数、分片缓存和样本表缓存的命中率与字节数、文件池的队列与拒绝数，以及按原因
    （过期、资源不符、地址、会话上限）统计的 403，来自 fit 的指标。
- **完成标准**：每个页面在真实浏览器中对运行中的 fit、以及在前端包的 mock 测试中都能工作，中英文；从每个页面保存的配置经校验后应用，错误链接
  回对应字段；预览经签名 URL 播放，被拒绝后停止；在时间线上修改的频道排期从下一个条目起生效；轮换密钥和结束会话立即生效；除预览自己的短期
  URL 外，没有任何密钥、token 或签名到达浏览器；Admin API 新增的读接口有上限并分页，在 race 检测下通过。
- **涉及**：`app/workbench`、`admin`、`medialib`、`streaming`、`mediaauth`、`docs`。

**待审查的决策**（实现时所做，2026-11-06；在用户裁定前按此执行）：

1. **一个模块** `gw.medialib`，八个页面，而不是加到 Streaming 与 Media auth 页面中，使这条线的页面在一起、其他页面不变。
2. **测试 URL** 由 Admin API 签发（`POST /media/auth/{policy}/urls`），可在某个 `media_tokens` 路由的范围内，因为 workbench 没有该路由中间件的
   凭据。备选：携带网站 API 凭据的代理。
3. **预览签名** 的主体为 `workbench:<user>`，每次一个新会话，2 分钟；没有 `url_signing` 的路由用短期 token；公开路由不签名播放。
4. **预览直接从 listener 播放**：控制台的 CSP 允许 `vod` 与 `hls` 的 listener，路由未设 `cors` 时页面会提示；否决了经控制台代理媒体，因为
   控制台会缓冲整个正文。
5. **播放器缓冲较短**（8–16 秒），使会话结束或密钥轮换后预览在几秒内停止；用 hls.js 的默认值，短片会被整段取完，永远不会被拒绝。
6. **在 workbench 中轮换** 只适用于保存在密钥文件中的密钥，像 `media_tokens` 的吊销那样先写文件再轮换；写在配置中的密钥回答 409。
7. **密钥的年龄** 取其文件的修改时间，从不读取密钥本身；写在配置中的密钥不显示年龄。
8. **频道的修改经共享草稿** 与 Save and apply，与任何修改一样经过检查；频道页的原始标签页包含整个 `streaming` 成员。
9. **分辨率过滤** 在 API 中是 `min_height` 与 `max_height`，页面中分组为 720p 以下、720p、1080p、4K 及以上。
10. **库的观众** 是其 `vod` 路由前缀下的在线会话；没有 `url_signing` 时无法知道观众。
11. **只有一份 hls.js**，在 workbench 中，由 `media_pages` 提供（删除了 I137 中相同的副本）。
12. **Ask AI** 在 I152 的媒体技能之前使用 `explain` 与 `diagnose`。
13. **写在配置中的密钥** 仍会显示在 URL 签名页的原始标签页中，与配置编辑器在 Media auth 页上一样；对 `GET /config` 脱敏会改变整个编辑器。

### I139 在 workbench 中管理媒体文件

- **做**：在 workbench 中管理媒体库的文件：上传到根目录的 `upload_dir`，分块断点续传（`POST` 创建、`PATCH` 按偏移写入、`HEAD` 查询续传位置），
  受 `max_upload_bytes`、并发上传数和磁盘剩余空间限制，在文件池上写入临时文件、完整后重命名到位，再进行探测；重命名、在根目录内的文件夹间
  移动和删除，各需确认；用表单编辑 sidecar 的标题、海报、多码率和字幕；移动或删除正被频道使用或仍在播放的文件前给出警告；每个修改都需要登录
  用户、经 CSRF 校验，审计日志存在后（I126）写入审计日志。
- **完成标准**：数 GB 的上传在连接中断后能续传，探测完成后可以播放；超过上限的上传返回 413 且不留下残缺文件；根目录之外的路径被拒绝；sidecar
  指定了 `id` 时重命名保持条目 `id` 不变；删除的条目从目录和缓存中移除；在 race 检测下通过。
- **涉及**：`medialib`、`admin`、`app/workbench`、`schema`、`docs`。

**待审查的决策**（实现时所做，2026-11-06；在用户裁定前按此执行）：

1. **每个库一个 `upload_dir`**，位于某个根目录之内（或就是根目录），因为配置中的根目录是普通字符串，一个目标让上限简单。备选：每个根目录
   一个字段；相对第一个根目录的路径。
2. **协议采用 tus 的形式**（`Upload-Offset`、`Upload-Length`、POST、PATCH、HEAD、DELETE），但不是完整的 tus 服务器（`Tus-Resumable`、
   `Upload-Metadata`、扩展）。
3. **正文超过上传大小或磁盘已满时放弃整个上传** 并删除其文件，不留下部分文件。备选：退回到最后一个完好的偏移。
4. **改名** 只改条目目录中以其命名的文件（`base.*`、`base-poster.*`），并改写引用它们的 sidecar；会被识别为版本标签或海报的新名字被拒绝。
   备选：改所有文件并整体改写 sidecar。
5. **移动** 带上所有文件，保持它们在条目目录下的路径，使 sidecar 的引用有效；缺少的目录会被创建。
6. **正在使用的条目**（被频道列出或正在播放，或在会话中播放）的改名、移动、删除或改变其 ID 的 sidecar 编辑会被拒绝（409），除非 `force`；
   页面在确认对话框中显示使用情况。备选：只在页面上警告。
7. **“仍在播放”** 指在带 `url_signing` 的策略下、资源为该条目 `vod` 前缀的在线会话；无法统计会话时应答会说明。
8. **修改后的目录**：条目立即离开目录，且不出现在修改之前开始的扫描结果中；重新扫描时按其现状列出。
9. **上传在重启后仍在**，靠每个 `.part` 旁的 `.json`；过期与孤儿文件在每次扫描开始时清理，时机随 `scan_interval`；重启时超过 `max_uploads`
   的上传被删除。备选：只在内存中；单独的定时器。
10. **写入从不跟随指向外部的链接**：写入的每个文件解析后都在其根目录内，无论 `follow_symlinks`（用于读取）如何设置。
11. **名字在所有系统上都有效**：不含 `\`、`:`、`*?"<>|`、控制字符、开头的 `.`、结尾的 `.` 或空格、Windows 设备名；每个名字 255 字节，
    路径 1,024 字节，32 层；只能上传库会读取的类型。
12. **审计**：类型 `media`，来源 `workbench`，每次修改一条（`media.upload.begin`、`media.upload`、`media.upload.cancel`、`media.rename`、
    `media.move`、`media.delete`、`media.sidecar`），在控制台运行助手时写入；用 Admin API token 做的修改记入日志而不进审计。
13. **经控制台流式传输的 PATCH** 的正文有 10 分钟（取代控制台的 30 秒读超时），任何流式正文上限 1 TiB，`LimitBody` 设为上传剩余的大小；
    workbench 每次发送 8 MiB。
14. **Admin API 自己的服务器也接受 PATCH**，每次读 64 KiB，脚本可用 token 上传，受其一分钟读超时限制。
15. **页面一次上传一个文件**，每块最多重试 30 次，间隔最长 15 秒；`max_uploads` 限制其他客户端。
16. **sidecar 编辑** 带上 sidecar 的时间，文件已改变时被拒绝（409）；引用必须存在、位于根目录中且类型正确；其他条目的 ID 被拒绝；新的 sidecar
    不会覆盖已有的文件。
17. **默认值**：32 GiB、4 个上传、1 GiB 空闲、24 小时；最多列出 1,024 个上传与 2,000 个目录。

## 集群管理

2026-11-05 提出。一个 fit 管理多个 fit：专门用作管理节点的实例（或任何开启该功能的实例）保存其他 fit 实例（成员）的地址和管理员凭据，
主动连接它们、集中展示、分组，并一起修改它们的配置：先预览，在每个成员上校验，再分阶段发布。每项功能都在 workbench 左侧菜单的一级
**集群** 入口下有对应页面；这里没有只有 API 的功能。决定 033 记录下面的选择。

原则，按约束力排序：

- **管理节点不在数据路径上。** 无论管理节点是否运行，成员都按自己的配置提供服务；管理节点宕机或被移除，只失去管理能力，别的不受影响。
  成员之间没有共识，也不共享运行时状态（限流计数、缓存、会话）：那是另一个数据面方向。
- **成员改动尽量少。** 管理节点使用成员已有的 API：用用户名和密码登录成员的控制台，经控制台转发 Admin API；或直接用 Admin API 及其
  bearer token。成员上的每次修改都走该成员自己的应用路径并带 `If-Match`，记入它的 `/versions`，可在成员上回滚。
- **凭据只存在管理节点的 store 中。** 集群管理需要 `admin.store`；成员的密码和 token 加密保存其中，在页面中只写一次、显示为掩码，从不
  发给浏览器、不写入日志。成员证书必须校验（CA，或经人确认后固定的 SHA-256 指纹）；没有静默的 `insecure`。
- **由人来应用。** 对多个成员的修改按成员展示 diff，由每个成员试运行校验而不应用，确认对话框之后才应用，分批进行、带健康检查门槛，
  失败即回滚。漂移只报告，点一下修复；本方向不做无人值守的自动同步。
- **不阻塞 worker**（规则 1）：管理节点对成员的调用走 fib 的异步 HTTP 客户端；扇出并发有上限，每次调用有超时（规则 10），每个响应、缓存、
  历史和审计条目都有上限（规则 11）。
- **默认关闭**：没有 `admin.cluster` 就没有集群菜单，也没有对外调用。

下文用语：**管理节点**，负责管理的 fit；**成员**（节点），被管理的 fit；**组**（资源组），按标签或名字选出的一组成员；**模板**，管理节点
在所关联组的成员上拥有的配置项；**发布**，一次修改分阶段应用到多个成员。

workbench 的集群菜单，由下列迭代逐步建成：

| 入口 | 页面 | 迭代 |
| --- | --- | --- |
| 概览 | 每个成员的健康、版本、配置版本、漂移、最近的证书到期和连接数，卡片或表格，可按组和标签筛选 | I142 |
| 节点 | 添加、编辑、删除和测试成员；凭据只写一次；首次连接时确认证书指纹；成员详情页 | I142 |
| 节点切换器 | 位于页头：从管理节点打开任一成员自己的页面（配置、引擎、一致性、版本） | I143 |
| 资源组 | 按标签选择器或名字定义的组、其成员，以及批量操作和每个成员的结果 | I144 |
| 模板 | 模板、其拥有的路径和变量，关联到组；每个成员的同步状态；从成员导入；对比两个成员 | I145 |
| 发布 | 计划（每个成员的 diff 和校验）、策略、逐批进度、暂停、继续、中止、回滚；历史 | I146 |
| 漂移与告警 | 每个成员相对模板的漂移，一键修复（以发布进行）；告警规则和 webhook | I147 |
| 用户与审计 | 管理节点的用户及其在各组上的角色；每个集群操作的审计日志 | I148 |
| 指标、日志、证书 | 每个成员和组随时间的请求、错误和连接；跨成员追踪日志；证书清单 | I150 |

不在本方向内：成员间的共识或共享运行时状态；替代 Kubernetes 或 GitOps（Kubernetes provider 仍是由集群资源驱动 fit 的方式）；
带备机的管理节点（其 store 可像任何 store 一样导出导入）；升级成员的二进制（I151 记录这个问题，不是计划）。

### I140 决定 033；成员被管理所需的能力

- **做**：决定 033。在每个 fit 上增加，使管理节点能管理它：Admin API 的 `GET /node`（经控制台为 `/api/node`）：一次生成并保存在 store
  中（没有 store 时保存在配置文件旁）的节点 ID、主机名、fit 和 fib 的版本、启动时间、运行中配置的版本和 ETag、运行的角色、schema 摘要，
  以及管理节点在使用某端点前检查的能力列表；试运行，`PUT`/`PATCH /config?dry_run=1` 和 `PUT /config/text?dry_run=1`，让配置走完整个
  应用路径（编译并加载）而不应用，回答与应用时相同；程序可用的登录响应（CSRF token 除 `/api/session` 外也在登录的 JSON 中返回）；按用户
  和按地址对失败登录退避减速并设上限，使网络上的成员控制台经得起猜测。
- **完成标准**：`/node` 在重启后返回相同 ID，有无 store 均如此；试运行拒绝应用会拒绝的内容、错误相同，且版本、配置和 store 不变；程序能
  登录、读取并带 CSRF token 应用；连续第十次错误密码被减速，退避结束后正确密码仍能登录；在竞态检测下通过。
- **页面**：workbench 的账号菜单显示节点 ID 和版本；配置页面在保存旁增加 **校验** 按钮，执行试运行并在保存出错的位置显示错误。
- **涉及**：`docs/decisions`（033）、`admin`、`admin/store`、`login`、`app/console`、`app/workbench`、`docs`。

**待审查的决策**（实现时所做，2026-11-06；在用户裁定前按此执行）：

1. **没有 store 时节点 ID** 保存在配置旁的文件中（`fit.node-id`），与各角色保存其文件的位置相同；之后有了 store 的 fit 会接过它。备选：
   `$XDG_STATE_HOME`；没有 store 就没有 ID。
2. **随机的 UUIDv4**；`admin.node_id` 接受 1–64 个 `[A-Za-z0-9._-]`。备选：主机名，它会随主机改变，在容器中也会重复。
3. **`admin.node_id`** 不在计划中，为配置只读且没有 store 的容器而加，否则每次启动都会得到新 ID。
4. **试运行** 适用于 `PUT`、`PATCH` 与 `DELETE /config`、`/load` 与 `/config/text`；不包括回滚、reconcile 与导入。
5. **试运行会短暂绑定新地址**，使已被占用的端口像应用时一样被拒绝。
6. **试运行编译时不用引擎**，与 `fit validate` 一样，上游连接池既不拨号也不探测。备选：真实环境，还能发现共享池的差异。
7. **被减速的用户被拒绝（429）而不是延迟**，因为挂住请求会占用登录池；每次猜测后最多 5 分钟。备选：延迟；豁免 loopback。
8. **不是用户的名字共用一个失败计数**，使表保持有界；未知名字会一起被减速。
9. **地址锁定时间翻倍**，从 15 分钟到 1 小时，安静 15 分钟后重新计算。备选：固定 15 分钟。
10. **用户连续失败 10 次后被减速**，高于地址的 5 次，使从一个地址猜测的人无法让真正的用户被减速。
11. **账户页面上的密码检查失败也计数**，与登录失败一样。
12. **`/api/node` 也放在控制台顶层**，与 `/api/m/admin/node` 并存，按计划的命名。
13. **试运行的应答** 给出运行中配置的 `version` 与 `etag`（用于下一次 `If-Match`）、`new_etag` 与 `unchanged`，并带 `Fit-Dry-Run: 1`；不计为一次重载。
14. **能力名称** 用点分、排序，含义永不改变；`store` 与 `console` 只在开启时出现。
15. **Check 只在有未保存的修改时可用**，与 Save 一样。
16. **决定 033 也把计划中七个待定问题按提议写下**，如不处理二进制升级（I151），待审查。
17. **`Retry-After` 保持原来的取整**（整秒加一），正文的 `retry_after` 为同一数字。

### I141 管理节点：成员、凭据与状态

- **做**：`admin.cluster`（`poll_interval`、`timeout`、`max_nodes`、`max_concurrency`、`max_response_bytes`），没有 `admin.store` 时拒绝；
  store 的载荷增加 `cluster`：成员、组、模板、发布和审计日志，载荷版本提升，旧版本仍可读取。成员有名字、标签、URL 和访问方式：`console`
  （用户名和密码；收到 401 或会话结束时重新登录；成员要求修改密码时报告而不代改）或 `admin_api`（bearer token）；TLS 用系统 CA、`ca`
  PEM 或固定的 `fingerprint`。控制台池上的轮询器每隔 `poll_interval` 读取每个成员的 `/node`、`/status` 和 `/config/check`，保存其可达性、
  延迟、最近错误、版本、运行时长、配置版本和 ETag、重载计数、连接数、下线的上游后端、最近证书到期和漂移数。管理节点的端点作为控制台模块
  `cluster`：`GET/POST/PUT/DELETE /api/m/cluster/nodes[/<name>]`、`POST .../nodes/<name>/test`（返回证书指纹供人确认）和
  `GET /api/m/cluster/state`。
- **完成标准**：在 Docker 中一个管理节点和三个成员：用密码和用 token 添加的成员都显示状态；错误密码、不受信任的证书、不匹配的指纹和宕机的
  成员分别报告；成员重启和管理节点重启后都能恢复；任何响应、页面或日志中都不出现密码、token 或会话 cookie；轮询器的并发和响应上限有效；
  在竞态检测下通过。
- **页面**：除 I142 基于这些端点绘制的页面外暂无。
- **涉及**：`cluster`（新）、`config`、`admin`、`admin/store`、`app`、`schema`、`docs`、`test/docker`。

**待审查的决策**（实现时所做，2026-11-06；在用户裁定前按此执行；也记在决定 033 的第 8–19 项）：

1. **用 store 小节而不是 payload 字段**：成员保存在 store 的 `cluster` 小节中，这是一个有自己版本的文档，旧版 fit 打开后原样写回，导出时不包含，
   因此凭据不会随导出离开；payload 字段与第 4 版格式会使所有旧版 fit 拒绝整个 store。备选：计划中的 payload 字段与第 4 版格式。
2. **`admin.cluster` 还需要 `admin.console`**，由它提供端点。备选：也放在 Admin API 中，I150 的 CLI 可能需要。
3. **轮询器是独立的 goroutine**，只发起异步调用，而不是控制台池上的任务，那些任务属于请求。
4. **每个成员一个 fib 客户端**，最多两个连接，使每个成员的 TLS 按其自己的设置检查并保留其证书。
5. **`ca` 与 `fingerprint` 互斥，且没有 `insecure`**；pin 只信任叶证书本身，不论其名字与日期。
6. **登录退避**：1 分钟起翻倍到 15 分钟，编辑或测试成功后重置；成员的 429 会等待其结束；要求改密码的控制台按同样的退避重新检查，每次都登出
   会话。
7. **会话会被登出**：删除或修改成员时、每次测试后，以及管理节点停止时（最多 2 秒），使管理节点不会占满成员的 `max_sessions`。
8. **测试有自己的上限**（4），与 `max_concurrency` 分开；`POST /test` 可测试尚未添加的成员，已保存成员的测试可以带草稿。
9. **审计日志** 记录添加、更新与删除：谁、何时、哪个成员以及改动字段的名字，从不记录其值，1,000 条；测试与轮询不记录。
10. **凭据从不显示**，连最后几个字符也不显示：只有 `password_set` 与 `token_set`。
11. **默认值**：`poll_interval` 10 秒，`timeout` 10 秒，`max_nodes` 256，`max_concurrency` 8，`max_response_bytes` 4 MiB。
12. **成员的 `http` URL 只允许 loopback**，凭据从不以明文跨网络传输。
13. **标签** 与 Kubernetes 相同（最多 32 个，键与值 63 个字符）。
14. **store 中条目无效的成员** 带错误显示且不轮询，而不是删掉。
15. **成员宕机时保留最后读到的状态**。
16. **改名是带新 `name` 的 `PUT`**；改名后的成员有新的轮询节点与会话。

### I142 集群菜单：概览、节点和节点详情页

- **做**：workbench 左侧菜单的一级 **集群** 入口，设置了 `admin.cluster` 时显示，含 **概览** 和 **节点**。概览：每个成员一张卡片或一行，
  按状态着色，显示健康、fit 版本、配置版本、漂移、最近证书到期、连接数和延迟；可按组、标签和状态筛选、排序，随轮询刷新。节点：用表单添加
  和编辑成员（名字、标签、URL、访问方式、只写一次并显示为掩码的凭据、TLS），**测试** 显示证书指纹供人信任，删除需确认。成员详情页：经
  管理节点读取的 `/node`、监听器、上游及其后端、证书、版本和一致性。菜单和所有页面均有中英文；`docs/cluster.md` 中英文，包括仅作管理
  节点的配置（`fit run -admin` 加 `admin.cluster`），以及在 I149 之前经 tunnel 管理 NAT 后成员的方法。
- **完成标准**：在浏览器中，对 I141 的 Docker 环境，添加、测试、信任、编辑和删除成员；概览显示 I141 的每种失败并能恢复；凭据字段从不显示
  已写入的内容；bundle 测试和浏览器检查在两种语言下通过。
- **页面**：集群 → 概览、节点、节点详情页。
- **涉及**：`app/workbench`、`cluster`、`docs`。

**待审查的决策**（实现时所做，2026-11-06；在用户裁定前按此执行；也记在决定 033 的第 20–27 项）：

1. **成员页面读取成员** 经轮询器会话上一个只有四个 GET（`node`、`status`、`versions`、`check`）的窄通道，最多同时 8 个，而不是现在就做 I143 的
   通用通道，或保留轮询器最后的应答（每个成员都占内存，而且过时）。
2. **由 `fit run` 而不是配置检查管理节点的 store**，使计划中只有 `admin.cluster` 的 `fit run -admin` 能运行：配置旁保存的 store 在校验时看不到；
   `fit validate` 不再报告没有 store 的管理节点。备选：`-admin` 把 `admin.store` 写入它校验的配置。
3. **菜单的位置**：全局页面之后（Logs 之后），Account 之前。
4. **暂无按组过滤**：在 I144 之前用标签选择器（`k=v`、`k!=v`、`k`）；up 绿色、error 琥珀色、down 红色、unknown 灰色。
5. **概览每个轮询间隔读取 `/state`**，间隔在 2 到 10 秒之间，服务端不推送。备选：变化流。
6. **信任证书**：确认完整的指纹后在表单中固定它并重新测试；已保存的成员立即保存；被替换的证书在红色警告之后才信任。
7. **扩展点**（`FitCluster.addAction`、`rowExtra`），使 I149 不必修改这些页面就能添加内容。
8. **Nodes 显示最近 20 条审计记录**；完整的日志归 I148。
9. **I149 之前位于 NAT 后的成员**：隧道的 hub 与 proxy 放在管理节点中，成员以 loopback 的 `http` URL 添加，到成员的那一跳是隧道固定证书的
   TLS。备选：单独的 hub 主机。

### I143 从管理节点打开成员自己的页面

- **做**：workbench 页头的节点切换器：选择一个成员后，workbench 自己的页面（配置及其文本标签页、引擎、一致性、版本、各角色页面）都针对
  该成员，管理节点用保存的会话把 `/api/n/<member>/...` 转发到成员的 API；schema 从成员读取；标题栏以单独的颜色标出成员名，避免改错实例；
  bundle 不认识其版本的成员以只读方式打开。经切换器的每次写入都以其用户记入管理节点的审计日志。
- **完成标准**：每个配置页都能读取和保存成员的配置，保存错误链接到正确字段；成员上过期的 ETag 像在本地一样被拒绝；更旧或更新版本的成员
  以只读打开；切回管理节点后页面上不残留成员数据；请求体上限有效；浏览器检查两种语言通过。
- **页面**：节点切换器；所有已有页面（针对成员）。
- **涉及**：`cluster`、`app/console`、`app/workbench`、`docs`。

**待审查的决策**（实现时所做，2026-11-06；在用户裁定前按此执行；也记在决定 033 的第 56–67 项）：

1. **路径用计划中的 `/api/n/<member>/...`**，位于控制台顶层，借助一个小而通用的 `console.Module.Prefixes`；`/api/m/cluster/n/...` 被拒绝，只留一种方式。
2. **通道在成员自己的连接上调用成员**，而不是经 `Manager.Call`，从而能看到控制台会话（要求改密码的 403 会结束会话），并按控制台路径访问角色模块。
   备选：扩展 `Manager.Call`。
3. **当前显示的成员保存在 URL 的 `?node=` 中**，切换会重新加载页面，一个 fit 的数据不会留在另一个上，每个成员的页面有自己的链接与标签页，未保存的
   修改由浏览器自己的提示保护。
4. **“bundle 认识的版本”** 指与管理节点相同的配置 schema 摘要；管理节点也按最近一次轮询的摘要拒绝写入。备选：相同的 fit 版本或小版本；只在页面中
   只读。
5. **写入** 指除 GET、HEAD、render、parse 与试运行之外的一切；每次写入都审计，不论成员如何应答（没有应答时为“可能已应用也可能没有”，登录失败
   时为“未发送”）。
6. **审计条目异步写入 store**，不改变成员列表的 ETag；最多 64 个写入等待其条目，再多一个在发送前被拒绝（429）；成员列表由更新的 fit 写入的管理
   节点拒绝切换器的写入，因为它无法记录它们。
7. **上限**：每个成员 3 个调用加 16 个等待，等待超过 `timeout` 回答 503，总计 64 个。
8. **请求头与状态码**：两个方向都用固定列表；浏览器会渲染的内容类型改为 `application/octet-stream`；成员的 401 与要求改密码的 403 改为 502；流式
   回答 501。
9. **不转发**：成员的控制台用户与密码、pprof、dashboard、`cluster`、`ai`，以及 LLM 角色，它们的页面用 POST 与流读取，每次轮询都会成为一次被
   审计的写入。
10. **`GET /schema`** 加到每个 fit 的 Admin API（`schema`）；旧成员按管理节点的 schema 绘制，无论如何都是只读的。备选：这类成员没有页面。
11. **标记**：标题前的标签、标签页标题、一条色带，以及顶栏中由成员名字生成颜色的一行；只读时 Save 变暗，写入以提示拒绝。
12. **顺带的两处修复**：对 `http` 成员的调用会带上查询参数，以及登录被拒时问题对象上的数据竞争。

### I144 资源组与批量操作

- **做**：组：标签选择器、按名字列出的成员，或两者兼有；一个成员可属于多个组；供 I145 使用的组变量。对一个组或选中成员的操作，以有上限的
  并发执行、按成员返回结果：重载、回滚到上一版本、运行一致性检查、按键或前缀清除缓存区、测试连通性。`/api/m/cluster/groups` 和
  `POST /api/m/cluster/ops`。
- **完成标准**：选择器的成员随标签变化而变化；对五个成员（其中一个宕机）的操作报告四个完成、一个失败及其错误；清除操作到达每个成员的缓存区；
  在竞态检测下通过。
- **页面**：集群 → 资源组：列表、组的表单（选择器及其预览成员、变量）、组页面（成员状态和操作栏）；概览按组筛选；每个操作在列出成员的对话
  框中确认，并按成员显示结果。
- **涉及**：`cluster`、`app/workbench`、`docs`。

**待审查的决策**（实现时所做，2026-11-06；在用户裁定前按此执行；也记在决定 033 的第 43–55 项）：

1. **成员关系是计算出来的，从不保存**：按当前标签得到选择器的匹配加上指名的成员。
2. **选择器** 是 Kubernetes 的等值条件加 `!k`，没有 `in` 与 `notin`；空选择器不选中任何成员。
3. **没有对应成员的名字会保留并显示为缺失**，而不是拒绝；改名会跟随，删除会把名字去掉，操作会跳过它。
4. **变量是普通字符串**：最多 64 个，名字由字母、数字与 `_` 组成，值 4 KiB，不是密钥；带类型或保密的值归 I145 的模板。
5. **文档版本不变**：组保存在第 2 版中，I149 的 fit 会保留其 `groups`；成员与组共用一个 ETag。
6. **“重载”是再次应用运行中的配置**（新的 `POST /reload`，`config.reload`），而不是 SIGHUP 那样重新读取文件或 store，那会丢掉管理节点经 API 做的
   修改；旧成员没有退路。
7. **回滚到 ETag 与运行中不同的最新保存版本**，带 `If-Match`，中间的重载不算一步；不能选择版本。
8. **按前缀清除是成员上新增的功能**（`POST /cache/purge` 的 `prefixes`，`cache.prefix`）：绝对 URL，主机小写，像完全清除那样在分片与磁盘索引的锁下
   扫描键；操作也可按 tag 与全部清除。
9. **操作是异步的**（202，由页面跟进），有自己的上限：同时 4 个、每个成员一个、保留 32 个。
10. **审计条目** 包含每个成员的结果（错误截到 256 字节），日志总计约 2 MiB；只读的管理节点拒绝操作（409），因为它无法审计。
11. **测试操作经 `Manager.Call`**，使用节点的会话。
12. **新的问题类型** `refused` 与 `no_earlier_config`，使页面不显示误导的提示。
13. **页面**：菜单中的 Groups，组页面即其操作页面，`#/cluster/ops` 用于任意选择，起初全部成员都选中，没有取消按钮。

### I145 模板：组内共享的配置

- **做**：模板拥有配置中的路径：整个顶层项（`media_auth`、`tls`、`log`）和具名元素（按名字的 `routes`、`upstreams`、`listeners` 和
  `services`；模板中的路由必须有名字）；值可以使用变量（`${node.name}`、`${node.labels.region}`、组变量）。模板关联到组，按顺序解决重叠；
  成员的期望配置是它自己的配置、其中被拥有的路径取自模板，其余路径归成员自己。每个成员的同步状态：已同步、未同步（附 diff）、或冲突（有人在
  成员上改了被拥有的路径）。可从成员配置生成模板（从中选择路径）；并排对比两个成员的配置；模板中的密钥值保存在 store 中并显示为掩码；指向
  文件的路径（`cert_file`、`token_file`）提示为成员本地文件。管理节点告知每个成员它拥有哪些路径（`PUT /node/managed`，保存在成员的
  store 中），成员自己的 workbench 把这些字段标为受管，本地修改前先询问。
- **完成标准**：关联到组的路由和上游模板按成员及其变量渲染；成员的其他路由不受影响；在成员上修改受管路由会在管理节点显示为冲突，并在成员
  页面上被标出；从成员配置生成的模板能重现该配置；在竞态检测下通过。
- **页面**：集群 → 模板：列表；模板编辑器（表单及与配置页相同的 `.conf`/`.yaml`/`.json` 文本标签页，列出拥有的路径和变量）、关联的组、
  每个成员的渲染结果和同步状态；**对比** 两个成员；**从节点导入**。
- **涉及**：`cluster`、`admin`、`app/workbench`、`docs`。

**待审查的决策**（实现时所做，2026-11-06；在用户裁定前按此执行；也记在决定 033 的第 68–84 项）：

1. **模板拥有的内容从其片段中读出**，而不是路径列表：顶层成员整体拥有（`media_auth` 整体），`listeners`、`routes`、`services` 与 `upstreams` 的元素按名字
   拥有；从不拥有 `admin` 或 `schema_version`。计划中的 `tls` 不是顶层成员，TLS 随其 listener。备选：JSON pointer；`media_auth` 按策略。
2. **元素名字不含变量**，因为模板靠名字找到元素。
3. **重叠按 `order`**（−1,000 到 1,000）再按名字决定，后者胜出；每个部分说明它覆盖了哪个模板。
4. **变量是字符串**；成员缺少的变量是该成员的错误，从不是空字符串；组变量只来自该成员所在的、模板所附加的组，后面的组胜出；没有默认值。
5. **同步哈希保存在成员上**，记在其记录中，而不是管理节点的文档中，后者会随成员数乘部分数增长；模板拥有之前成员就有的值是冲突（`own_value`），
   因为应用会覆盖它。
6. **通知成员的时机**：页面读取它们时，以及模板、组或成员发生任何变化之后；写入日志，不进审计日志。
7. **值按其规范 JSON 的 SHA-256 比较**（键排序，数字按原样），因此 `1.0` 与 `1` 不同。
8. **按名字识别密钥**，沿用助手的规则加上推流 URL（`rate_limit` 的 `key` 除外），在每个应答中都打码。
9. **新元素加到成员列表的末尾**；模板不会从成员中删除任何东西，不再拥有的部分保持原样并列为已释放。
10. **store 的文档保持第 2 版**，因为旧版 fit 会原样保留模板。
11. **读取同步状态在请求内完成**：同时 4 个，30 秒，之后未读到的成员回答为待定；只保存不含值的摘要。
12. **文本标签页经集群模块自己的端点转换**，片段可以不经配置检查转换；表单是模板页面自己的，因为配置页面共用运行中配置的草稿。
13. **成员页面只询问从不拒绝**，每个部分一次，直到重新读取配置。
14. **schema 不同的成员** 在其同步状态上得到提示（I143 的 `compatible`），而不是错误。

### I146 发布

- **做**：发布来自模板的修改、对未同步成员的同步，或把一个成员的某部分复制到其他成员。发布计划：每个成员的 diff 和该成员的试运行（I140），
  并记录其 ETag。策略：一次全部、按批大小或百分比滚动并暂停、或金丝雀（先发前几个成员，等人确认再继续）。每批之后的健康门槛：成员可达、重载
  成功、无新增漂移，若设置则在一个窗口内 5xx 比例低于阈值。门槛失败时发布停止并回滚该批（每个成员经 `/rollback/{n}` 带 `If-Match` 回到
  原版本），按需回滚全部。自计划以来 ETag 已变的成员被跳过并标记，不被覆盖。暂停、继续、中止；每个成员同时只有一个发布；发布及每个成员的
  结果和版本保存在 store 中。
- **完成标准**：在 Docker 中五个成员：滚动发布分批应用；一个成员得到能加载但未通过门槛的配置，发布停止且该成员被回滚；金丝雀等待确认；计划后
  在本地被修改的成员被跳过；中途重启的管理节点显示发布暂停在停止处并可继续；在竞态检测下通过。
- **页面**：集群 → 发布：新发布的计划（成员、diff、试运行错误链接到成员页面）、策略表单、显示 diff 的确认对话框；进行中发布的逐批进度及
  每个成员的门槛结果，暂停、继续、中止和回滚；历史。
- **涉及**：`cluster`、`app/workbench`、`docs`、`test/docker`。

**待审查的决策**（实现时所做，2026-11-06；在用户裁定前按此执行；也记在决定 033 的第 85–102 项）：

1. **发布以类型化的形式保存在文档的 `rollouts` 中**（仍为第 2 版）；就绪成员的期望配置逐字节保存（随 store 加密）直到发布结束，之后只保留哈希与改动的
   路径；保留最近 50 个。精确的字节使试运行的 `new_etag` 在重启后仍然有效。备选：单独的小节；应用时重新生成期望配置。
2. **计划是冻结的**：成员得到计划的内容；计划一小时后过期；同时最多 4 个计划中或进行中的发布。备选：每批重新渲染模板。
3. **未就绪的成员带原因被排除**，启动时只用就绪的成员；用户还可以排除更多。备选：有任何未就绪成员就不能启动。
4. **成员在计划中时冲突会被覆盖**；diff 显示每个部分的同步状态与原因。备选：自动排除冲突的部分或成员。
5. **批次按成员名字排列**：金丝雀是最前面的几个，之后按数量、按向上取整的百分比，或其余全部一批。备选：手工或按标签选择金丝雀。
6. **检查的默认值**：等待 5 秒；设置 5xx 阈值时，30 秒的窗口按 20 个请求判断，更少时带说明通过；不统计请求的成员在应用任何内容之前就使其批次
   失败。
7. **5xx 比例需要成员端的修改**：`/status` 的 `requests`，只增不减的总数（`status.requests`，成员的配置设置 `metrics` 时才统计），由管理节点读取两次。
   备选：成员上带窗口的端点。
8. **应用被拒绝会使其批次的检查失败** 并回滚；以 412 跳过的成员不会。
9. **回滚到成员在计划时运行的版本**，从不强制；之后被修改或不再保留该版本的成员标为 `rollback_failed`；检查失败时回滚其批次，`on_failure: all` 时
   回滚已应用的全部。
10. **应答丢失的应用按 ETag 判定**（成员的 `config_etag` 与试运行的 `new_etag` 比较），否则标为 `unsure`。
11. **暂停** 在批次之间生效；**中止** 在正在应用的成员完成后停止，缩短检查的等待，并保留已应用的内容（回滚需单独要求）。
12. **管理节点重启后** 运行中的发布变为暂停，正在制定的计划过期，进行中的回滚变为失败以便再次要求；只有人能继续。
13. **启动后的发布持有其成员**，暂停与等待期间也持有，直到结束；计划不持有任何成员。
14. **检查点写入不改变成员列表的 ETag**（`appendAudit`）；写入失败记入日志，发布继续。
15. **每个发布一个 goroutine**，一个批次中同时最多 `max_concurrency` 个成员；测试使用毫秒级时间，而不是注入的时钟。
16. **API 的时间以毫秒为单位**（`pause_ms`、`wait_ms`、`window_ms`）。
17. **页面**：Rollouts 位于 Templates 之后；新建发布可由 URL 查询参数预填；试运行错误链接到成员页面、其自己的页面与其渲染结果。
18. **没有 store 的成员上的 `admin`** 由 `PUT /config` 原样接受；模板与复制从不拥有 `admin`，发布从不改变它。

### I147 漂移与告警

- **做**：每次轮询检查漂移：每个成员对照其模板及其自身的一致性检查；修复即对该成员的一次发布（I146），从不自动应用。告警规则：成员宕机一段
  时间、漂移、发布失败、证书在若干天内到期、上游后端下线、重载失败；发送到 webhook（通用 JSON，附聊天服务模板），带重复间隔和静默，
  webhook 的密钥保存在 store 中；告警记录有上限。
- **完成标准**：每条规则在条件持续到设定时间后触发一次，恢复后解除；静默有效；超时的 webhook 不拖慢轮询器；在竞态检测下通过。
- **页面**：集群 → 漂移与告警：未同步成员及其 diff，**修复** 打开发布计划；告警规则表单、webhook、当前和历史告警、静默；有告警时集群菜单
  显示角标。
- **涉及**：`cluster`、`app/workbench`、`docs`。

**待审查的决策**（实现时所做，2026-11-06；在用户裁定前按此执行；也记在决定 033 的第 103–117 项）：

1. **告警保存在单独的 store 小节** `cluster.alerts` 中，而不是文档中，因为旧版 fit 写文档时会把它们丢掉；第 3 版会让旧版 fit 只读。
2. **只在漂移可能改变时重新读取**：配置的 ETag 不同、还没有状态，或状态超过 10 分钟。
3. **读取漂移不通知成员** 它们拥有什么；发布与页面读取会通知。
4. **一致性漂移通过确认后的重载修复**，而不是发布；Fix 只制定发布计划，从不启动。
5. **`member_down` 包括 down 与 error**；`rollout_failed` 每个失败的发布一个告警，直到回滚或 24 小时；`reload_failed` 从管理节点第一次读取开始计数，
   直到成功、重启或一小时。
6. **没有保存规则时存在默认规则**；没有 webhook 时只显示在页面与标记上。
7. **规则的 webhook**：空列表表示所有 webhook；被规则引用的 webhook 不能删除（409）；改名会同步到规则。
8. **只要发送过触发通知就发送恢复通知**，静默期间也发送，规则被删除时发给所有 webhook，使接收方能关闭已打开的告警。
9. **发送失败不重试**：64 个的队列丢弃最旧的，超时 10 秒（1 到 60 秒），聊天服务返回 200 但带错误码算作失败。
10. **模板只做 `${...}` 替换**，不是 Go 模板，用户写的内容不会被执行；自定义正文在保存时检查是否为 JSON。
11. **webhook URL 是密钥**（聊天 token 在其中），只允许 https 或 loopback 的 http。
12. **上限**：静默最多提前 30 天，结束后保留 7 天，最多 256 个；触发或待定的告警 2,048 个，已恢复的 500 个；待定告警不跨重启保留。
13. **触发与恢复写入日志而不是审计日志**，因为没有人操作；每个方法都接收用户，供 I148 的角色使用。
14. **告警有自己的 ETag**，与成员列表的分开。
15. **标记** 是对核心的一个小补充（模块上的 `badge()`），因为没有扩展点能画出它。

### I148 用户、角色与审计日志

- **做**：管理节点的控制台用户在各组上有角色：`viewer`（只读）、`operator`（批量操作和发布已有模板）、`admin`（成员、凭据、模板、组），
  在每个端点和每次经切换器的写入上检查；两者都开启时与助手的等级（I126）一致。每个集群操作的审计日志：用户、时间、成员、操作、diff 摘要、
  结果及前后版本，保存在 store 中，有上限；可导出为 JSON lines。
- **完成标准**：每个角色在 API 中被拒绝其无权的操作，页面中相应控件隐藏；I141–I147 的每个操作都留下记录；日志在上限内跨重启保留；在竞态
  检测下通过。
- **页面**：集群 → 用户与审计：用户在各组上的角色；审计日志可按用户、成员、操作和时间筛选，每条可打开其 diff。
- **涉及**：`cluster`、`login`、`app/workbench`、`docs`。

**待审查的决策**（实现时所做，2026-11-06；在用户裁定前按此执行；也记在决定 033 的第 118–133 项）：

1. **角色保存在 store 的文档中**，由集群级管理员实时修改，配置中的 `admin.cluster.admins` 作为恢复途径。备选：放在配置中，那只在重启时生效，且任何控制台
   用户都能编辑。
2. **设置任何角色之前**，每个控制台用户都是集群级管理员，与以前一样；第一次保存必须保留一个管理员。备选：升级后所有人都是 viewer，会把人锁在外面。
3. **各角色能做什么**：viewer 只读；operator 运行操作、测试已保存的成员、制定并控制模板与同步发布；admin 还能编辑成员与凭据、注册、测试草稿、经切换器
   写入并制定复制计划；组、模板与角色需要集群级管理员，因为它们决定谁在哪里有什么角色。
4. **对成员的修改按其现状与修改后的样子检查**，标签改变无法让它离开编辑者的范围；组角色随改名而改，随组删除而去掉。
5. **列表不按角色过滤**；读取成员自己的内容需要在该成员上有角色。备选：过滤所有列表。
6. **默认角色** 未设置时为无；列出但没有角色的用户仍为无。
7. **路由器默认拒绝**：用 `Handle` 添加的端点只允许集群级管理员写入；`Checked` 端点自行决定。
8. **设置角色后管理节点自己的页面归集群级管理员**，否则 viewer 可以编辑配置并在下次重启后成为管理员。
9. **助手的上限**：集群级管理员不受限，operator 最多 `propose`，其他人 `read`；集群工具取角色与级别中较窄者。
10. **只有存在角色时才写第 3 版**；旧版 fit 之后以只读打开文档。
11. **diff 保存 API 显示的内容**，密钥打码；切换器写入只保存正文摘要；保存修订号，因为 ETag 在重启后重新开始。
12. **成员测试会被审计**，推翻 I141 的第九项决策；轮询与读取不审计。
13. **两个上限在每条写入路径上都生效**，以前有些路径只计条数。
14. **审计过滤**：用户、成员、操作（完整或按前缀）与 RFC 3339 时间；`?format=jsonl` 导出 JSON 行。
15. **页面隐藏** 角色不能用的控件，而不只是禁用；非管理员看到的模板编辑器为只读。
16. **I147 的端点**：读取漂移与告警任何角色都可以；Fix 与静默需要在成员上（或组上，或对不属于组的规则在集群上）是 operator；规则、webhook 与 webhook 的
    测试需要集群级管理员。备选：静默与规则一样只给管理员。

### I149 NAT 后的成员主动连出

- **做**：成员上的 `admin.cluster_member`：管理节点的 URL，以及在管理节点生成、只能使用一次以换取自己凭据的注册 token；成员用 fib 的客户端
  经 WebSocket 主动连到管理节点，带退避重连，管理节点经该连接访问成员的 API，与经 HTTP 访问其他成员相同。成员在注册时固定管理节点的证书；
  在管理节点上删除的成员失去其凭据。
- **完成标准**：没有开放端口的成员能像其他成员一样使用以上全部功能；连接在管理节点重启和网络中断后恢复；已用或过期的注册 token 被拒绝；
  在竞态检测下通过。
- **页面**：节点页增加 **通过注册添加**：生成带有效期的 token 和要粘贴到成员上的配置行，并显示成员已连接或等待中。
- **涉及**：`cluster`、`config`、`admin`、`app/workbench`、`schema`、`docs`。

**待审查的决策**（实现时所做，2026-11-06；在用户裁定前按此执行；也记在决定 033 的第 28–42 项）：

1. **链路接到单独的 listener**（`link_address`），而不是控制台，后者 32 个连接、每个客户端地址四分之一的限制会限制同一 NAT 后的成员数。
2. **管理节点的链路证书** 自签名，保存在 store 中，从不续期，由配置行固定，因此不需要 CA。
3. **token 与凭据** 是有各自前缀（`fite1.`、`fitc1.`）的 `internal/linktoken` 密钥，以哈希保存，在标签 `fit cluster link v1` 下双向证明。
4. **生成 token 时就添加成员**，显示为等待；token 在凭据到达成员之前就已用掉。备选：成员确认的两步注册。
5. **链路在进程内承载 Admin API**，具有其 token 的权限，不含 dashboard 与 pprof，控制台密码为临时密码时拒绝；成员不需要控制台。备选：成员经
   loopback 转发到自己的控制台或 Admin API。
6. **成员保存其凭据** 在 store 中或配置旁（`fit.cluster-member`，0600），只用于同一指纹；配置中换了 token 就重新注册。
7. **最终的拒绝会让成员停止**（已用、过期、未知、证明错误、已吊销；后三者还会忘掉凭据），直到新的 token 与重启；`busy` 与 `replaced` 会重试。
8. **`admin.cluster_member` 只在启动时读取**，与 `admin` 的其余部分一样。
9. **上限**：每 15 秒 ping，45 秒无响应视为断开，退避 1 秒到 1 分钟并加抖动，握手 10 秒，每条链路 16 个调用，成员上 2 个 worker 与 16 个排队，
   请求正文在 `max_response_bytes` 之内。
10. **文档为第 2 版**，旧版管理节点无法通过覆盖写入丢掉链路。
11. **token 默认有效 24 小时**，可设 1 分钟到 30 天。
12. **每个凭据一条链路**：新链路取代旧链路，两个共用凭据的 fit 会轮流连接。
13. **`Manager.Call`** 是调用成员（HTTP 或链路）的唯一方式，供 I143 起使用。
14. **I142 的页面** 在其扩展点旁改了几行，因为扩展点没有覆盖列与编辑表单中的 `link` 访问方式。
15. **fib WebSocket 的缺口**（A52，强制关闭；A53，发送背压）采用与 LLM mesh 相同的临时方案：按远端地址找连接，队列以在途调用数乘其大小为限。

### I150 跨集群的指标、日志与证书

- **做**：管理节点从每次轮询保存的时间序列，每个成员有上限：每秒请求数、4xx 和 5xx、连接数、字节数、重载；按组汇总。经 fit 的日志环
  （I125）同时追踪多个成员的日志，按时间合并。证书清单：每个成员的证书及其名字、签发者和到期时间。CLI 的 `fit cluster`（节点、状态、组、
  diff、发布的开始/状态/中止），经管理节点的 Admin API。助手的集群工具见 I153。
- **完成标准**：时间序列与成员自身的指标一致；合并追踪跟随三个成员；某成员即将到期的证书排在最前；CLI 端到端驱动一次发布；在竞态检测下通过。
- **页面**：集群 → 指标（按成员和组的图表）、日志（选择成员、筛选、跟随）、证书（清单，按到期排序）；概览中的迷你趋势图。
- **涉及**：`cluster`、`cli`、`app/workbench`、`docs`。

**待审查的决策**（实现时所做，2026-11-07；在用户裁定前按此执行；也记在决定 033 的第 134–145 项）：

1. **CLI 通过管理端的 Admin API 访问管理端**（令牌与 `-c`，与 `fit check` 相同），由调用 Manager 方法的处理函数在 `/cluster/...` 下提供。
   备选：登录控制台，代价是脚本中要有密码。
2. **Admin API 作为集群范围的管理员行事**，即用户 `Admin API`，不论角色如何；同名的控制台用户没有角色。备选：在配置中为令牌设角色。
3. **序列中的“字节”为为客户端排队的字节**（`pending_bytes`），不是吞吐：字节计数器会位于请求路径上。备选：按路由统计字节，在基准测试主机上测量（后续工作）。
4. **序列保存在管理端内存中**，每个成员 360 次轮询（约 30 KiB），重启后丢失。备选：保存在 store 中，每次轮询都要写入。
5. **速率在读取时计算**，成员重启、相隔超过三个间隔或成员不统计请求时为空；分组按轮询间隔相加。
6. **序列与证书清单任何角色都可读取**，与状态相同（第 122 条）；日志需要在每个成员上有角色。
7. **追踪读取成员的日志环**，经成员新增端点 `GET /logs/recent`（能力 `logs.recent`，环中消息编号），而不是文件：16 个成员、每个成员
   1,000 条消息、占用 8 个成员读取名额之一；页面保留 2,000 行。
8. **证书清单即轮询器读取的 `/status`**，每个成员 256 张证书，而不是重新读取。
9. **概览迷你走势图是一个 `rowExtra`**（I142 的扩展点）；未修改 `overview.js`。
10. **`fit cluster rollout start`** 规划、打印、除非 `-yes` 否则询问，带 `-wait` 时跟随；失败、中止、回滚或过期退出码为 1；`state`、`diff`、
    `certs -within` 在有需要关注的情况时退出码为 1。
11. **`diff` 不告诉成员管理端在其上拥有什么**，与漂移读取相同（第 105 条）。
12. **图表为工作台自己的内联 SVG**，按成员名称取色，经 CSSOM 设置（控制台的 CSP 拒绝内联样式）；不引入图表库。

### I151 升级成员的二进制（一个问题，不是计划）

跨成员升级 fit 本身（下载发布包、用固定的公钥校验签名、替换二进制并把监听套接字交给新进程重启，按 I146 发布）需要 fit 尚不具备的平滑重启。
决定 033 记录是否采纳，或交给软件包、容器和编排系统，概览的版本列已能为其服务。

### 待审核的问题（2026-11-05）

1. 访问成员：控制台的用户名和密码（如需求所述）和 Admin API 的 token 都提供，密码优先。两者都保留？
2. 凭据要求管理节点开启 `admin.store`。可以接受，还是也允许明文文件？
3. I143 用管理节点的 bundle 打开成员页面（统一 UI，未知版本只读），而不是整体代理成员自己的 bundle。同意？
4. 模板拥有整个顶层项和具名元素；没有名字的路由不能共享。够用吗？
5. 不做无人值守同步：漂移只报告，由人发布修复。同意，还是按组提供可选的 `auto_sync`？
6. I149（成员主动连出）放在 I145–I146 之前还是之后？在此之前经 tunnel 访问。
7. 二进制升级（I151）是否纳入范围？

## 日志：文件、切割与保留

fit 自身的日志和访问日志都会写文件，而今天的文件只打开一次，fit 运行期间一直追加：没有东西切割它，也没有东西清理它，
改变它的路径或格式还要重启。于是文件一直长到磁盘写满，运维只能在 fit 之外切割，workbench 也看不到这些文件及其占用。
这条路线为文件输出提供切割策略与保留策略，由两种日志共用：按大小、按时间（在该日志时区的边界）切割，以先到者为准，压缩
切割出的文件，并按最大时间、最大数量和最大总大小删除旧文件。每种日志按自己的 `time_zone` 写出行的时间——默认本地，可由
配置设为 `utc` 或某个 IANA 时区——它的文件也在该时区的边界切割。workbench 的日志页面展示文件、以表单修改设置，并提供
由人执行的立即切割与立即清理。决定 034 记录下面的选择。

原则，按约束顺序：

- **一个文件一个写入器，不占用 fib 的 worker。** 每个输出路径一个文件写入器，各有有上限的队列和自己的写 goroutine，由写向
  该处的所有 logger 和访问日志共用。一次写入只复制字节就返回：队列满时拒收并计数，任何 logger、handler 或请求路径都不碰
  磁盘（规则 1）。切割、保留与压缩在写入器的 goroutine 和 `internal/filepool` 上运行，并受超时约束（规则 4）。
- **不增加请求路径的开销。** 这里新增的一切都在写入器的 goroutine 和它的池上运行：入队、大小与时间检查、切割、清理、压缩。
  访问日志的一行仍在原来的位置格式化且不分配，队列把内容复制进复用的缓冲区，因此一次写入在队列有余量时不分配、也不取任何
  会让 worker 等待的锁；包的基准测试在压测主机上对比改动前后（规则 21）没有回退。
- **切割不丢内容。** 队列中的内容先写进文件，再改名并打开新文件；无论设置如何组合，一行只会出现在一个文件里，不会被拆开、
  丢弃或写两次。压缩先写临时文件再改名，失败时保留原文件。
- **默认本地时间，一个文件一个时区。** 日志的行和它的文件的时间切割都用它的 `time_zone`：不写时 `local`，或 `utc`，或
  `Asia/Shanghai` 这样的 IANA 时区（fit 内嵌 `time/tzdata`，没有 zoneinfo 的容器也能用）。因此默认 `24h` 在本地午夜切割，
  `time_zone: "utc"` 时在 UTC 午夜切割；同一种日志的每种格式都写同一个时区。两种日志共用文件时必须写同一时区、在同一边界
  切割，否则配置被拒绝。
- **只动写入器自己的文件。** 一次清理只删除由该路径按写入器的命名规则生成的文件，且从不对正在写的文件动手；目录中的其他
  文件绝不触碰。
- **能即时生效的即时生效。** 重载原地改变 `time_zone`、切割、保留与压缩设置，写入器保留它已有的文件；文件的路径或 logger
  的格式仍需重启，workbench 会说明哪个是哪个。一致性检查（I121）不再把已即时生效的 `time_zone` 与切割、保留、压缩当作只能
  重启的差异。
- **一切有上限。** 队列的字节数、一次清理读取的文件数、同时进行的压缩数都有上限；慢磁盘最多占用队列那么多内存，被丢弃的
  内容计数并报告。

### I155 决定 034；文件写入器：按大小和时间切割、保留、压缩

- **做**：决定 034。包 `logs`：一个路径上的文件写入器，1 MiB 的队列和自己的写 goroutine；`Write` 复制进复用的缓冲区并入队，
  队列满时拒收并计数。切割：若一次写入会使文件超过 `max_bytes`，先切割——比它还大的一行自成一个文件——或在一次写入的时间
  进入比文件更晚的周期时切割，周期按日志的 `time_zone`，两者以先到者为准；所以默认 `24h` 在本地午夜切割，
  `time_zone: "utc"` 时在 UTC 午夜切割。切割出的文件命名为 `<path>.<yyyy-mm-ddThh-mm-ss.mmm>`（UTC，切割时刻，与日志的
  时区无关），重名时加 `-1`、`-2`，压缩后加 `.gz`，压缩在 `internal/filepool` 上（临时文件后改名；失败时保留原文件，清理
  跳过正在压缩的文件）。保留：打开时、每次切割后以及每十分钟清理一次：先删早于 `max_age` 的文件，再删超出 `max_files` 的，
  最后从最旧的删到切割文件总量不超过 `max_total_bytes`；只删写入器按规则生成的名字，从不删正在写的文件。`config.Log` 增加
  `time_zone`——不写时 `local`，或 `utc`，或某个 IANA 时区——每个 logger 的每种格式都按它写出行的时间，引擎的也一样，
  由包在 fib handler 外的一层把记录的时间换到该时区。`config.Log` 与 `config.Logger` 增加 `rotate`：`log.rotate` 作用于
  `log.output`，logger 自己的输出用它自己的；字段留空时 `max_bytes` 1 GiB、`max_age` 720h、`max_total_bytes` 1 GiB，
  `compress` 打开，`interval` 与 `max_files` 关闭，设为 0 即关闭——因此 `"rotate": {}` 是一套有界的策略，不写 `rotate` 则
  保持今天的单文件，并在启动时记一行说明它会无限增长。一个管理器（`logs.Files`，由 `fit run` 创建并交给 `logs.New`）为每个
  路径打开一个写入器，由写向该处的 logger 共用，重载时由它原地修改 `time_zone`、切割、保留与压缩；logger 的输出路径仍在
  启动时固定。
- **完成标准**：logger 的文件在 `max_bytes`、每个 `interval` 边界以及两者中先到者处切割；默认 `time_zone` 时 `24h` 在本地
  午夜切割，`time_zone: "utc"` 时在 UTC 午夜切割，设置 IANA 时区时在该时区的午夜切割，`1h` 在该时区的整点切割，假时钟逐项
  验证；每种格式的行都带所设时区，引擎的也一样；经过一百次切割，每一行都在且只在一个文件里，没有拆开、丢失或重复，大于
  `max_bytes` 的一行自成一个文件；文件按时间、数量和总大小从旧到新删除，正在写的文件从不删除，写入器没有生成的文件从不
  删除；`compress` 打开时切割出的文件是可解压回原文的 gzip、原文件已删、`max_total_bytes` 按压缩后的大小计算，压缩失败时
  保留原文件并记一行日志；队列满时拒收并计数，慢磁盘（filepool 的测试钩子）不阻塞任何 logger 调用，磁盘恢复后报告丢弃数；
  重载原地改变 `time_zone` 与每个切割、保留与压缩字段，而路径或格式的改动仍提示需要重启；重启后在写第一行之前先清理；
  `fit validate` 拒绝负的上限、小于 1s 的 `interval`、不认识的 `time_zone`，以及两个 logger 对同一路径给出不同的 `rotate`；
  schema 描述与文档覆盖每个字段；日志的基准测试与写入器的写入，在压测主机上以 benchstat 对比改动前后（规则 21）没有回退，
  且队列有余量时每行不分配，时区转换是每条消息或每秒一次、绝不每行一次；在竞态检测下通过。
- **涉及**：`logs`、`internal/filepool`、`config`、`cli`、`admin`、`schema`、`docs`。

### I156 访问日志的文件走同一个写入器；一个文件一个写入器

- **做**：`access_log.rotate`（复用 `config.Rotate`）与 `access_log.time_zone`；`observe.AccessLog` 把批次写进管理器为它的
  路径保留的写入器，而不再自己打开文件；它的 `Close` 先写尽自己的缓冲，再写尽写入器的。它的行按自己的 `time_zone` 写出：
  不写时 `local`，或 `utc`，或某个 IANA 时区；它的文件的时间切割也在该时区的边界。`json`、`text` 原先写 UTC、`line` 写
  本地时间，现在每种格式都跟随该设置，默认本地。`log` 的 logger 与访问日志写同一路径时共用一个写入器；配置给共享路径两组
  不同的 `rotate`，或两个不同的 `time_zone` 时拒绝，避免两个写入器互相切割同一个文件。重载原地更新访问日志的 `time_zone`、
  切割、保留与压缩；它的路径与格式仍和现在一样需要重启。写入器拒收的批次记为访问日志丢弃的行数，与它自己缓冲的丢弃一样。
  按秒缓存时间戳的做法保留：选定时区后每秒转换一次，而不是每行一次。
- **完成标准**：访问日志的文件和 fit 自身日志一样按大小和时间切割、被清理，跨切割没有行丢失或重复；默认 `time_zone` 时
  `24h` 在本地午夜切割，设为 `utc` 时在 UTC 午夜切割，设为 IANA 时区时在该时区的午夜切割，假时钟逐项验证；`json`、`text`、
  `line` 默认都写本地时间，设为某时区时都写该时区（`json`、`text` 带该时区的偏移，`line` 为它的当地时间），每秒最多转换
  一次；两种日志写同一路径时共用一个写入器，每行完整；`fit validate` 拒绝同一路径的两组不同 `rotate` 或两个不同
  `time_zone`；慢磁盘时像缓冲一样丢弃并计数；重载不重启即可改变访问日志的 `time_zone`、切割、保留与压缩；`Close` 写出两种
  日志缓冲的内容；访问日志的基准测试在压测主机上以 benchstat 对比改动前后（规则 21）没有显著变化，请求路径每行没有更多分配；
  在竞态检测下通过。
- **涉及**：`observe`、`logs`、`config`、`admin`、`schema`、`docs`。

### I157 workbench 的日志页面

- **做**：一级 **日志** 页面：每个文件输出一行，含路径、写向它的 logger 与格式、日志的 `time_zone` 与切割、保留设置、当前
  文件与切割出的文件及其大小和时间、合计、丢弃的行数和最近一次写入错误；设置以页面表单和 `.conf`、`.yaml`、`.json` 原文
  标签页编辑，写入 `log.time_zone`、`log.rotate`、`log.loggers.<name>.rotate` 或 `access_log.time_zone`、
  `access_log.rotate`，并和任何页面修改一样经唯一的应用路径校验并应用（I120），`time_zone`、切割、保留与压缩无需重启即
  生效；对没有 `rotate` 的输出、已超过保留却未清理的文件、最近写入失败的输出给出警告。**立即切割** 与 **立即清理**：后者
  先预览将删除的文件，并在列出这些文件的确认框中确认；两者走控制台自己的接口，在登录与 CSRF 之后，并记入 `admin` logger。
  `GET /api/logs` 列出各输出及其文件，有上限；`POST /api/logs/rotate` 与 `POST /api/logs/cleanup` 接收输出的 `path`，后者
  还接收 `dry_run`；不是 fit 打开的 `path` 一律拒绝；列表在 `internal/filepool` 上读取，每个输出最多 1,000 个文件，回答有
  上限；配置输出名之外的东西不列出也不删除。链接到 `log`、`access_log` 页面和一致性检查（I121）。
- **完成标准**：菜单显示日志，页面以中英文列出每个输出的文件、大小、时间、合计及其生效的时区；以表单修改 `max_age`、
  `max_files`、`max_total_bytes`、`max_bytes`、`interval`、`compress` 或 `time_zone` 经页面应用路径生效且无需重启，而路径
  或格式的改动提示需要重启；**立即切割** 后页面显示新文件与被切割的文件；**立即清理** 先预览，确认后只删除预览中的文件，
  取消则什么都不删，写入器未生成的名字从不列出或删除；写入失败、压缩失败与丢弃行数都会显示；超过列表上限的回答被截断并
  说明；只有登录且可访问该页面的用户能操作，CSRF 被检查，每次操作记入 `admin` logger；中英文浏览器测试通过；涉及 Go 的
  部分在竞态检测下通过。
- **涉及**：`app/workbench`、`admin`、`logs`、`observe`、`docs`。

## Agent 与隧道（续）

### I159 隧道与 LLM mesh 的派发 token

- **原因**：2026-11-06 的安全审查发现，这两个功能都用所有成员共享的密码认证链路：隧道 hub 的 `password`（以及
  `body_password`），LLM mesh 监听方的 `password` 或 f1628565 加入的角色密码。于是持有某角色密码的成员可以向同角色的其他
  成员冒充监听方，或用另一成员的 ID 打招呼、分走它的调用（包括 prompt）；而应答拨号或录下链路的一方，能拿到离线猜密码所需的
  材料：它自选挑战的 HMAC，或所有安装共用盐值、仅 1 万轮的 PBKDF2。链路本身在非 loopback 时已改为 TLS 或 QUIC（同日决定），
  路径上的攻击者已被挡住；剩下的问题是密钥共享、长期有效且可被猜测。
- **做**：决定 035。由监听方派发 token，代替共享密码：
  - **不绑定角色。** 有效 token 由 node、proxy 或 hub 出示都被接受；链路能做什么，仍由它打招呼时声明的角色和监听方对该角色的
    规则（`allow_hub_manage`、`allow_hub_services`、`allow_proxy_services`）决定。
  - **每个 token 是独立凭据。** token 有 ID、名称、创建时间、可选的过期时间和可选的备注；监听方只保存其密钥的哈希。成员的 ID
    绑定到它首次注册所用的 token，其他 token 不能夺取。
  - **有效期。** token 可在指定时间或一段时长后过期；过期 token 在下一次握手时被拒绝，它持有的链路在限定时间内关闭。
  - **管理。** 监听方的页面、Admin API 模块和 CLI 可以生成 token（只显示一次）、列出 token 及其最近使用和正在使用的链路、修改
    过期时间、吊销 token（立即关闭其链路）。每次变更记入 `admin` logger。
  - **握手。** token 的密钥是高熵的（至少 128 位，随机生成），对挑战的 HMAC 不泄露可猜的东西。双方各加自己的 nonce，监听方
    反向证明 token，证明绑定双方 nonce 与方向。`body_password` 的密钥由 token 经 HKDF 派生，不再用共享盐值的 PBKDF2。
  - **共享密码。** 为便于升级，保留一个版本仍可接受，之后拒绝；仍有成员使用时页面给出警告。
- **完成标准**：node、proxy 与 hub 通过 TLS 或 QUIC 用 token 连接监听方；token 被任何角色接受，角色规则仍生效；一个成员的
  token 不能以另一个成员的 ID 注册；过期 token 被拒绝，其链路在限定时间内关闭；吊销 token 的链路立即关闭；生成、列出、修改
  过期与吊销在页面、Admin API 和 CLI 中都可用并有日志；token 密钥只显示一次、只以哈希保存；握手证明绑定双方 nonce 与方向，
  反射或重放的证明被拒绝；共享密码仍能连接并给出警告；中英文文档说明 token 与升级方法；在竞态检测下通过。
- **涉及**：`tunnel/...`、`llm/...`、`config`、`admin`、`app`、`cli`、`schema`、`docs`。

**待审查的决策**（实现时所做，2026-11-06；在用户裁定前按此执行）：

1. **证明方式。** 类似 SCRAM：监听方保存 SHA-256(HMAC(secret))，所以拿到配置副本也不能建链；监听方的证明密钥与正文密钥由这个
   哈希经 HKDF 得出，因此持有哈希者可以冒充监听方或读取加密的 tcp 链路。备选：用保存的密钥做普通 HMAC（配置本身成为凭据）；
   每个成员一对密钥。
2. **成员绑定持久保存** 在 token 的 `members` 中，首次使用时通过一次配置修改写入，重启后仍有效；成员换 token 需先吊销旧的。
   备选：只在内存中；显式的重新绑定操作。
3. **成员身份** 在隧道中是 `node/<name>` 或 `proxy/<name>`，在 LLM mesh 中是角色 ID，与各自已有的对端标识一致。
4. **隧道注册前增加一次 `challenge` 往返**，无论正文是否加密，hub 都贡献一个 nonce。备选：复用加密 hello 的随机字节，但它只在
   设置 `body_password` 时存在。
5. **正文加密。** 仍由 `body_password` 开启；密钥是 token 的正文密钥与 `body_password` 经 HKDF 所得，hello 以明文带 token ID。
   备选：只要有 token 就开启。
6. **token 的位置。** 在角色上（`tunnel.hub.tokens`、`llm.<role>.tokens`），不在 `listen` 下，因此在页面上编辑监听器不会丢失它们。
7. **允许没有密码的监听方**，只接受 token，没有 token 时给出警告，这样角色可以在第一个 token 之前建立。
8. **过期** 由定时器在下一个过期时间检查，至少每分钟一次；文档给出的上限就是这一分钟。
9. **最近使用与在线链路** 只在运行时保存，不在每次握手时写入配置。
10. **上限**：每个监听方 1,024 个 token，每个 token 256 个成员；有效期为 Go duration 或 `Nd`。
11. **去掉密码**：页面 API 中的 `clear_password`（隧道 hub 设置）与 `clear_passwords`（LLM 监听方的编辑）。

### I160 审查后的隧道与 LLM mesh 的 TLS

- **原因**：对 2026-11-06 新增 TLS（fit `3622aee1`、`6bd2c39c`、`fc685c08`）的独立审查确认证书固定与握手本身没有问题，但发现：
  - **端点地址中的片段可以覆盖其 pin（Medium）。** `checkTunnelEndpoint`（`config/tunnel.go`）拒绝地址带查询串，却不拒绝
    `#fragment`；`uplink.Dials` 以字符串方式在地址后拼接 `?pin=…`，而 `TLSClientConfig` 以及 quic 与 ws 的拨号都在第一个 `?`
    处切分，这个 `?` 可能位于片段中。于是在配置了 hub 真实 pin 的情况下，`tls://hub:7000#?pin=<其他>&` 会连到另一张证书；
    `quic://hub:7000#?insecure=1&` 会关闭 quic 的校验；`#?ca=/path&` 会替换 CA；`tls://hub:443#?x=` 会丢掉 pin 而改用系统根证书。
    hub 与 node 的页面会原样传递端点地址（`EndpointReq.address()`），因此能填写端点或把端点交给运维粘贴的人都能利用。已在
    tls、wss 与 quic 上证实。
  - **非 loopback 时接受 `quic_insecure`（Low）**，这正是新规则对 tcp 与 ws 所拒绝的“hub 未经认证”的情形。
  - **不算缺陷，但一并修复：** 没有 `admin.store` 时，hub 的自签证书每次启动都重新生成，对端的 pin 因此失效；LLM 监听的
    `cert_file` 只在启动时读取，磁盘上轮换的密钥需要重启才生效。
- **做**：把链路的证书选项（pin、CA、insecure）以 `ClientOptions` 传给传输层的 Dial，不再放进地址；用 `net/url` 构造与解析拨号
  URL；在校验与页面 API 中拒绝端点地址里的片段、userinfo、`?` 或 `#`；非 loopback 且未设置 `body_password` 时拒绝
  `quic_insecure`；没有 store 时把自签证书保存在配置目录（0600）；LLM 监听的证书文件变化时重新加载，与隧道 hub 的监听一致。
- **完成标准**：审查中 tls、wss 与 quic 的片段攻击在校验中被拒绝，且直接交给 Dial 时也无法改变 hub 证书的校验方式；
  非 loopback 的 `quic_insecure` 被拒绝并给出清楚的错误；没有 store 的 hub 重启后指纹不变；LLM 监听无需重启即可使用轮换的
  证书；中英文文档说明这些；在竞态检测下通过。
- **涉及**：`config`、`tunnel/internal/transport/...`、`tunnel/internal/uplink`、`tunnel/hub`、`tunnel/node`、`llm/...`、`docs`。

**待审查的决策**（实现时所做，2026-11-06；在用户裁定前按此执行）：

1. **`quic_insecure` 与 `pin` 或 `ca_file` 同时设置** 时在非 loopback 上可以加载：校验由 pin 或 CA 决定，它在那里不起作用。
   备选：只要有非 loopback 的 quic 端点就拒绝。
2. **已禁用的端点也计入** `quic_insecure` 检查，与普通 tcp 和 ws 端点一致。
3. **保存证书的文件**：第一个 `-c` 路径旁的 `<去掉扩展名的配置路径>.<角色 id>.pem`，配置目录则为 `<目录>/fit.<角色 id>.pem`；
   stdin 或有 store 时不写。备选：固定文件名；单独的状态目录。
4. **没有 store 时只有文件** 保存证书，不写入运行中的配置，使其与磁盘上的文件一致。备选：两者都写。
5. **保存的文件里没有证书** 时 Apply 失败且不动该文件，hub 因此不启动，而不是悄悄生成新证书改变 pin。
6. **保存的文件无法写入**（只读挂载）时退回旧行为，并警告下次启动证书会变。备选：拒绝启动。
7. **LLM 的 `self_signed` 监听方也** 以同样方式保存证书（`fit.llm.<role>.pem`），虽然计划只提到 hub。
8. **LLM 监听方轮换的证书原地替换**：已有链路与调用不受影响，代价是带 `cert_file` 的监听方 `GetCertificate` 要经过 fib 的
   握手池一跳。备选：像隧道 hub 那样重开监听器，会切断所有入站链路。
9. **文件加载失败的重载** 只记日志不返回错误，文件再次变化时 `-watch-certs` 会重试。
10. **通过 `-watch-certs` 监视文件**（`app.Info.CertFiles` 进入运行时图），而不是每个角色一个监视器；隧道 hub 监听器的文件以前
    只在 Apply 时读取，现在也被监视。
11. **页面 API 执行完整的 `config.CheckTunnelEndpoint`**，以与配置相同的消息答 400。

### I161 审查后的未认证连接与文件名

- **原因**：同一次审查还发现：
  - **Admin API 的限制只覆盖读（Medium）。** `limitConn` 把读截止时间限制在 accept + `unauthTimeout`（`admin/server.go`），但
    net/http 的写截止时间仍是 `writeTimeout`，且每读到一个流水线请求就重新设置一次；不带 token 的客户端流水线发出约 70 个
    dashboard 请求、每分钟只读几个字节，就能占住一个连接位约 70 分钟（已证实为限制的 49 倍）。
  - **限制的是单个连接的寿命，不是攻击者的份额（Medium，设计）。** 每 4.9 秒重连一次的客户端，从少数几个地址或从不计入的
    loopback，仍能几乎一直占满 Admin API 或控制台的连接位。
  - **fastcgi 不拒绝反斜杠（Medium，Windows）。** 它只用按 `/` 切分的 `Hidden`、`HasShortName` 与 `Alias` 检查脚本，没有用
    `dotfile.Scan`；于是 `/a%5C.git%5Cx.php` 会执行 `root\a\.git\x.php`，`a\GIT~1\` 与 `a\CON.php` 也能通过。已通过 HTTP 证实。
  - **换了大小写的扩展名会以源码返回（Medium，Windows NTFS 与 macOS APFS）。** 区分大小写的 `\.php$` 路由匹配不到
    `/x.PHP`，请求落到共享根目录的 `static`，打开 `x.php`：以文本返回 PHP 源码。nginx 迁移文档正是这样映射
    `location ~ \.php$` 的。
- **做**：未认证的 Admin API 连接在 accept + `unauthTimeout` 时无论在做什么都关闭（与控制台的定时器一样），写截止时间同样受限；
  dashboard 对未认证连接的应答带 `Connection: close`；连接位满时关闭最早的未认证连接，而不是拒绝新连接，并为已认证连接保留
  一部分 `max_conns`，Admin API 与控制台都如此。fastcgi 使用 `dotfile.Scan`，对反斜杠答 400，普通路径与 `try_files` 都如此。
  在大小写不敏感的文件系统上，`static` 拒绝扩展名在任意大小写下与 fastcgi `split_path` 扩展名相同、或名字与磁盘上不一致的请求；
  迁移文档改用 `(?i)\.php$`。
- **完成标准**：审查中的流水线客户端在限制时间到时被关闭；不断重连的攻击者无法把已认证的管理员挡在外面；反斜杠路径在所有
  系统上都被 fastcgi 拒绝；在 Windows 与 macOS 上，共享根目录下的 `/x.PHP` 不会返回源码；在竞态检测下通过。
- **涉及**：`admin`、`app/console`、`handler/fastcgi`、`handler/static`、`internal/dotfile`、`docs`。

**待审查的决策**（实现时所做，2026-11-06；在用户裁定前按此执行）：

1. **新连接关闭哪个未认证连接。** 关闭持有未认证连接最多的那个客户端最早的连接，loopback 与 Unix socket 客户端算作同一个
   客户端；计划写的是“最早的”。这样从其他地址重连的攻击者关掉的是自己的连接，而不是新管理员的。备选：单纯最早优先；连接
   满一定时长才可被关闭。
2. **已认证连接的份额。** `max_conns` 的四分之一（16 → 未认证 12，32 → 24），且至少总留一个位给未认证连接，与已有的每客户端
   四分之一一致。备选：单独的配置字段。
3. **`static` 如何得知 fastcgi 的 `split_path`。** 进程级列表，fastcgi 建立时把扩展名加入（`dotfile.AddScripts`），只增不减，
   因此重载只会扩大拒绝范围。备选：`static` 上的 `script_extensions` 参数。
4. **`static` 如何读取磁盘上的名字。** Windows 用 `FindFirstFile`，其他系统读目录：每个路径段 O(目录项数)，只在大小写不敏感
   根目录的缓存未命中时发生。Unicode 规范化形式与磁盘不同（macOS 上 NFC 对 NFD）的名字被拒绝。
5. **大小写变体的应答。** 404，与隐藏的点文件一致。备选：400。
6. **fastcgi 的 `try_files`。** 只有反斜杠、NUL 或 `..` 答 400；Windows 别名与点文件仍像以前一样跳过。
7. **nginx 转换器。** 把含 `\.php` 的 `~` 正则写成 `(?i)` 并给出警告，与迁移文档一起改。备选：只改文档。

### I162 一个地址，一个服务器

- **原因**：fib 的引擎以 SO_REUSEPORT 监听，让每个轮询器各在自己的套接字上接受连接；内核也会让同一用户任何同样设置它的
  套接字无错误地监听同一地址，并把连接分给两者。2026-11-06 的 TestWebProxy 偶发失败正是如此：网页代理和控制台被放到同一
  端口上都在监听，20 个请求中有 13 个由错误的服务器应答。配置校验只拒绝单个角色内的部分冲突（两个监听器、转发与网页代理），
  不跨角色；控制台、LLM 角色的监听器和 API、隧道 hub 的监听器和 console socket、转发和网页代理在绑定时也没有
  `listener`、`streaming`、`webrtc/turn` 和 `webrtc/media` 所做的普通绑定检查。
- **做什么**：在 `config` 中对配置让 fit 监听的每个地址做一次检查（监听器、默认 HTTPS 生成的监听器、`admin`、
  `admin.console`、`llm` 角色的控制台、监听器和 API、隧道角色的控制台、hub 的监听器和 console socket、proxy 的转发和
  网页代理、流媒体的四个端口、TURN 的三个列表、`webrtc.media` 和 `webrtc.proxy`），地址先规范化，在两者中靠后的那处报错并
  写明靠前的那处；它取代的各角色内的重复检查移除。绑定时的普通绑定检查集中到 `internal/bindcheck` 一个包，fit 添加的每个
  绑定都使用。决定如何对待别的进程。
- **完成标准**：任意两类地址在校验中都会冲突，有测试；每个角色都拒绝另一个以 SO_REUSEPORT 监听的 fib 引擎占着的地址，
  测试在去掉检查时失败；中英文文档；竞态测试和 CI 通过。
- **涉及**：`config`、`internal/bindcheck`、`internal/bindtest`、`listener`、`app/console`、`llm`、`tunnel`、
  `streaming`、`webrtc`、`docs`；fib 的 `webrtc/turn/turnserver`（A70）。

**待审查的决策**（实现时所做，2026-11-06；在用户裁定前按此执行）：

1. **别的进程：绑定时做普通绑定检查，保留 fib 的 ReusePort。** fib 对不开 `ReusePort` 的引擎所做的也是同样的探测（其
   `internal/listener` 中的 `claimAddress` 不带 SO_REUSEPORT 绑定，在轮询器绑定之前关闭），两者都不是原子的：在中间几微秒
   里占住地址的套接字发现不了，同一用户自己设置 SO_REUSEPORT 的套接字之后也能加入，那是该套接字自己的选择。关闭
   `ReusePort` 作用于整个引擎，会让所有 UDP 地址（HTTP/3、隧道的 QUIC、TURN、WebRTC、SRT）只由一个轮询器读取，也会让在原
   地址上替换的监听器失去无空档的重载。因此没有为 fit 的绑定向 fib 提出新 API。备选：fib 的 `Binding.Exclusive`，保留占位
   套接字（绑定后再设 SO_REUSEPORT），是原子的，若那几微秒的窗口将来要紧再做。
2. **通配地址与具体地址在同一端口上算冲突。** `:8080` 与 `127.0.0.1:8080` 被拒绝，尽管内核会把具体地址的连接交给它自己的
   套接字而不是分摊：在 Linux 上绑定时的检查本来就会拒绝这样一对（一方的普通绑定与另一方的监听套接字冲突），所以校验先在
   所有系统上一致地指出。同一 TURN `listen` 列表中重叠的两个地址同样被拒绝。备选：只比完全相同的地址。
3. **规范化。** 空主机、`0.0.0.0`、`::` 和 `::ffff:0.0.0.0` 是同一个通配地址，因此 `0.0.0.0:80` 与 `[::1]:80` 也算冲突，
   尽管 Linux 允许二者都绑定（多拒绝了）；`localhost` 即 `127.0.0.1`，与 Go 解析监听地址一致；IP 以规范形式比较，含 zone；
   其他主机名小写比较，加载时不解析，所以同一 IP 的两个名字只在绑定时被发现。
4. **一处检查，新的消息。** 各角色的检查（监听器、流媒体的端口、隧道 hub 的监听器、隧道 proxy 的转发和网页代理、TURN 的
   列表、`webrtc.proxy` 对 `webrtc.media`、流媒体的 WebRTC 端口对 WebRTC 角色）被取代；消息现在以配置路径或监听器名称写明靠前
   的那处，以及传输协议。各部分的顺序是监听器、`admin`、`llm`、`tunnel`、`streaming`、`webrtc`；错误报在靠后的那处。
5. **校验看不到的。** 提供者（Docker、Kubernetes Gateway）在运行时生成的监听器，以及角色页面改动的地址，只在绑定时检查。
   TURN 的中继端口范围和 `webrtc.proxy` 的源端口范围不是监听地址，不与监听器比较；中继现在会跳过范围内被其他套接字占着的
   端口（A70）。
6. **一次重载中在不同种类之间移动地址。** 把地址从一种绑定移到另一种（网页代理的给转发、一个角色控制台的给另一个角色）的
   重载，若新绑定时旧的仍开着，新绑定的检查会失败；失败会被报告，其余照常运行，下一次应用时再绑定。每一种仍像以前一样先关
   自己的旧绑定再绑新的。
7. **fib 的 TURN 中继（A70）。** 在这里发现：设置 `port_min`/`port_max` 时，两个分配可能共用同一个中继端口。已在 fib
   （`856ad5b`）中以预留端口集合加同样的普通绑定修复；fit 已移到该提交，它还带来另一会话的 `925f292` 和 `0ddaba3`
   （media/hls）。

## 访问控制

### I195 客户端地址的允许与拒绝名单

- **为什么**：2026-11-08 提出：fit 要支持客户端地址的允许名单（白名单）与拒绝名单（黑名单），既能全局配置，也能为每个服务模块
  单独配置，模块自己的优先于全局的。
- **做什么**：顶层以及 listener、路由、service 和各角色上的 `config.Access`（`allow`、`deny`、`allow_files`、`deny_files`）；
  `access` 包把名单编译为有序区间、按二分查找，`Policy` 是适用的各名单（最具体的在前），`Gate` 在应用配置时替换；fib 的
  `Binding.Filter`（A73），使被拒的对端不花掉一个 Connection；Admin API、控制台与每个角色的监听也一并受控。
- **完成标准**：能在 accept 时得知策略的，被拒对端在 accept 时关闭或丢弃其数据报；路由、service 或角色自己的 access 双向覆盖
  全局的，有测试；重载改变策略而不重新绑定；名单文件被监视；中英文文档；race 测试与 CI 通过。
- **涉及**：`access`、`config`、`runtime`、`listener`、`l4`、`app`、`admin`、`app/console`、`streaming`、`webrtc`、`tunnel`、
  `llm`、`schema`、`docs`；fib 的 `core`（A73）。

**待评审的决定**（实现时做出，2026-11-08；在用户裁定前有效）：

1. **各层如何组合。** 每个名单都可以决定：命中其 `deny` 则拒绝，否则命中其 `allow` 则放行，否则 `allow` 非空时拒绝其外的一切；
   都不是则它不表态，由外一层决定（路由、listener、全局；service、listener、全局；角色、全局）。没有任何名单表态的地址放行。
   因此更具体的名单双向覆盖，同一名单内 deny 优先于 allow。备选：局部名单整体替换全局名单，或局部名单只能收紧。
2. **在哪里决定。** listener 的策略就是全部时，在 accept 时经 `Binding.Filter` 决定：被拒的 TCP 连接在读取任何内容前关闭，
   被拒的 UDP 或 HTTP/3 对端的数据报被丢弃。listener 的路由或 service 有自己的 access、或 listener 接受 PROXY 头时，改为稍后
   决定：HTTP 请求回 403（纯文本），tcp 连接在 PROXY 头与 ClientHello 确定 service 后关闭，socks 客户端在其请求时拒绝。
   只有一个 service 且无 server_names 的 tcp 或 udp listener 在 accept 时按该 service 的策略决定。
3. **用哪个地址。** 连接的地址，或 PROXY 头写明的地址；从不用客户端自己能写的 `X-Forwarded-For`。在 CDN 之后，用 PROXY 协议
   或 CDN 自己的名单。
4. **变更作用于之后开始的连接。** 已打开的连接、在 accept 时决定的 listener 上 HTTP/2 与 HTTP/3 连接后续的请求、已知的 UDP
   对端都不复查。重载保留绑定，替换其 gate。
5. **名单文件。** 每行一项，`#` 与 `;` 注释，每个文件至多 64 MiB；应用配置时读取，并像证书文件一样被监视，变化时重新应用配置。
   读取失败的文件使配置失败，与坏的证书文件一样。
6. **ACME。** 应答 HTTP-01 或 TLS-ALPN-01 挑战的 listener 上的允许名单必须放行 CA 的验证服务器；fit 不为其开洞，因为它们的
   地址不公开。已写入参考文档。
7. **fib 的 Filter（A73）。** 不计入 `Stats.ConnsRefused`（仍只计 `MaxConns`）；Unix 套接字与 `OpenUDPPeer`（ICE 自己的对端）
   不询问；被拒的 UDP 对端每个数据报都重新询问而不记住，策略变化即时生效，也不随被拒对端增长。
8. **角色。** 每个角色一个 `access`，覆盖它监听的所有套接字：`streaming`（RTMP、RTSP、SRT 及其 WebRTC 端口）、
   `webrtc.turn`、`webrtc.media`、`webrtc.proxy`、`tunnel.hub`（listeners 与 `console_socket`）、`tunnel.proxy`（forwards 与
   web proxies）、`llm.hub`、`llm.node` 与 `llm.proxy`（`listen`，以及 proxy 的 `api`）；不按子监听分别设置。`tunnel.node`
   只为其页面监听，因此没有。每个角色在每次 Apply 时都设置其 gate（全局名单可能在成员不变时变化），access 不进入任何重启判断。
   经 fib 的 `webrtc.Mux` 共享的 WebRTC 端口需要 `MuxConfig.Filter`（A74）。
9. **Admin API、控制台与角色页面。** `admin.access`、`admin.console.access` 与各角色的 `console.access` 先于全局名单决定，
   全局名单同样作用于它们：全局 `allow` 不含 127.0.0.1 时，本机客户端也进不了 Admin API；文档建议把 `admin.access` 的 `allow`
   设为 `127.0.0.1` 与 `::1` 以保留本机访问。它们随每次应用的配置生效，尽管 `admin` 的其余设置只在启动时读取。监听 Unix 套接字的
   Admin API 从不检查，此时设置 `admin.access` 是校验错误。备选：全局名单完全不作用于 Admin API。

## 性能

不属于任何功能线的吞吐与延迟工作。

### I158 fib v1.7.1 的 HTTP/2 请求检查的开销

- **原因**：2026-11-05 在压测机上，fib v1.7.1（安全版本）相对其前一个提交 `5c0e5b9`，使 fit 的 `fixed` 和 `static`
  场景在 h2c 上每秒请求数分别下降 2.4% 和 1.7%（`bench/ab.sh`，5 轮，结论 SLOWER）；fit 安全轮的 A/B 中又测得同样的结果
  （`fixed` −2.5%，`static` −1.8%）；HTTP/1.1 与 `proxy` 在噪声内。fib 的 `BenchmarkH2ServerStatic/1KB` 慢 0.99%
  （p=0.004），`BenchmarkH2ServerGET` 慢 1%（p=0.05）。该轮中 fit 自身的改动在噪声内。可能的开销来自 fib 的 `dabe569`
  （逐字节检查每个请求的 `:method`、`:scheme`、`:authority` 与 `:path`）和 `46af102`（每个 DATA 帧都累计整体收集的
  body 的字节数）。
- **做**：在 fib 中，于压测机上分别对 `5c0e5b9` 和当前提交的 `BenchmarkH2ServerGET`、`BenchmarkH2ServerStatic` 取 profile
  并对比。让这些检查只花必要的开销：在已有的解码过程中顺带检查伪头部字段，用一张表代替现在的多次查找；对 HEADERS 帧即
  结束的请求跳过 body 计数。所有检查都保留。然后让 fit 依赖该 fib 提交（规则 19）。
- **完成标准**：压测机上 `bench/ab.sh -scenarios fixed,static -protocols h2c` 显示新的 fib 与 `5c0e5b9` 在噪声内；fib 的
  微基准相对 `5c0e5b9` 没有显著退步；fib 针对这些检查的安全测试仍通过；fib 的 CI 通过，fit 已在该提交上。
- **涉及**：fib `http`（`h2_server.go`、`h2_pseudo.go`），fit `go.mod`。

**待审查的决策**（实现时所做，2026-11-06；在用户裁定前按此执行）：

1. **记住 `:authority`。** 前两个提交后 `BenchmarkH2ServerGET` 仍慢 0.7%，于是连接记住上一个有效的 `:authority`，只在它变化时
   重新检查（`cb9bbbe`）：客户端每次发送同样的字符串，解码出的字符串是不会变的副本。备选：停在 `089a5b0`，GET 慢 0.7%，代码更简单。
2. **响应一侧也改了。** `089a5b0` 用同一次查表检查响应的已知键，虽然计划只提到请求检查；它对 `static` 有帮助，可以单独去掉。
3. **逐跳字段改为比较。** 原来的 map 在所有地方（包括 HTTP/2 客户端）改为比较；行为不变。
4. **A/B 构建的是什么。** fit 的 main 调用了 `Context.SetIdleTimeout`，`5c0e5b9` 没有它，因此两边都从 fit `35270ef4`（v1.7.1 的
   固定版本，与发现开销的那次 A/B 相同）构建，在临时 modfile 中用 `replace`。由于用户的 `qwen27b.service` 占着 8081，运行时把
   基准框架的 h2c 端口临时改为 8091，未提交。

## 数据同步：从生产者到消费者

2026-11-08 提出。fit 新增 `sync`：由流水线从生产者（**源**，source）读取变更或记录，写入消费者（**目标**，sink），
如同 Filebeat 之于文件，DataX、Canal 或 Debezium 之于数据库。源与目标像其他角色一样在配置中设置；下面每一种基础设施
既可作源也可作目标，因此任意两者都能连接；内置连接器和变换覆盖不到的，由 WASM 插件扩展。流水线可以一直运行（流式：
跟踪文件、跟随 binlog），也可以只运行一次（批处理作业：复制一张表后停止）。决定 036 记录下面的选择。

原则，按约束顺序：

- **不丢数据，且由检查点保证。** 源的位置（事务结束处的 binlog GTID 集合或文件与偏移、PostgreSQL 的 LSN、MongoDB 的
  resume token、文件的标识与偏移、Kafka 的 offset、游标的最后取值）只有在流水线的每个目标都确认了它之前的所有记录后
  才保存；重启、崩溃或 `kill -9` 之后从上次保存的位置继续。投递语义为至少一次；目标以幂等方式写入（按键 upsert、
  文档 ID、去重令牌），因此重放后收敛到相同状态。服务端为读者保留数据的源（PostgreSQL 的 slot、Kafka 的 offset）也只在
  此时、绝不提前被告知位置。
- **事务保持完整。** 检查点不会落在源事务的中间；同一事务的记录带有事务 ID 和边界，SQL 目标可以把它们写成自己的一个事务。
  从源到目标按键保序；目标可以并行处理不同的键，同一个键绝不乱序。
- **一切有上限**（规则 11）：阶段之间队列的字节数与记录数、批次的字节数、记录数与时间、单条记录的大小、插件的内存与时间、
  保留的死信。慢的目标通过队列减慢源，并报告延迟；fit 内没有任何东西无限增长。源的服务端在此期间保留的数据（slot 的 WAL、
  binlog）也会报告，并有告警阈值。
- **不占用 fib 的 worker。** 同步不在请求路径上：流水线的源、变换和目标运行在各自的 goroutine 上，每条流水线有上限；基于
  HTTP 的系统（Elasticsearch、ClickHouse、HTTP、S3）经由 fib 的异步 HTTP 客户端；到数据库的连接少而长，每个连接一个
  goroutine。接收记录的监听器（HTTP 源）在不阻塞 worker 的情况下移交记录（规则 1）。
- **依赖精简**（设计原则 6）：线协议（MySQL 及其复制流、PostgreSQL 及逻辑复制、MongoDB 的 OP_MSG 与 BSON、Kafka、RESP、
  SigV4）在 fit 中自行实现，如同 fib 自己的协议，每一个都在 Docker 中对照真实服务端检查；不引入任何驱动。规范记录是 fit
  自己的类型，任何连接器的类型都不会泄漏到另一个连接器。
- **一种记录模型。** 每个源产生、每个目标接收同一种记录：源及其命名空间（`db.table`、索引、集合、文件、topic）、操作
  （`insert`、`update`、`delete`、`ddl`、`message`）、键、变更前后的行（带类型的值：null、bool、int、uint、float、以文本
  表示的 decimal、string、bytes、time、date、JSON、array、map）、提交时间、事务，以及不透明的位置。变换和插件只看到它。
- **凭据与 fit 其他地方一样**：存放在 `admin.store` 中，或由环境变量名指定（`password_env`，如同助手的 `api_key_env`），在页面中只写一次、以掩码显示；凡服务端提供 TLS
  就使用并校验，没有静默的 `insecure`。
- **默认关闭**：没有 `sync` 就没有外连，也不在磁盘上留状态。

下文用词：**流水线**，一个源、它的变换和一个或多个目标，共用一个检查点；**快照**，表中已有的行，在其变更之前或代替其变更
读取一次；**游标**，在系统没有变更流时源据以轮询的单调列或字段（更新时间、版本号）。

每种基础设施作为源和作为目标分别是什么：

| 基础设施 | 作为源 | 它的检查点 | 作为目标 | 迭代 |
| --- | --- | --- | --- | --- |
| 文件行 | 像 Filebeat 一样跟踪路径和通配：发现新文件，跟随改名与截断式切割，保留不完整的末行，合并多行 | 文件的设备号、inode 和文件头指纹，以及偏移 | 行（原样、JSON、CSV 或模板）经 fit 的文件写入器写出，切割并保留（I155） | I166 |
| MySQL、MariaDB | 行格式的 binlog，以副本身份读取（`COM_BINLOG_DUMP_GTID`）；按主键分块的快照 | 事务结束处的 GTID 集合，或文件与偏移 | 按主键 upsert 与 delete，批量写入，可按源事务写入 | I172–I176 |
| TiDB | 经 TiCDC 的变更（canal-json 或 TiCDC simple 协议，来自 Kafka）；经 MySQL 协议的快照 | Kafka offset，记录中带提交 TS | MySQL 目标：TiDB 使用 MySQL 协议 | I185 |
| PostgreSQL | 使用 `pgoutput`、slot 和 publication 的逻辑复制；取自 slot 导出快照的快照 | LSN，只在目标确认后才向 slot 确认 | `INSERT ... ON CONFLICT DO UPDATE` 与 delete，批量写入；快照用 `COPY` | I177–I180 |
| ClickHouse | 没有变更流：经 HTTP 接口按游标轮询，按查询做快照 | 游标的最后取值及该值上的键 | 经 HTTP 插入（`JSONEachRow`），带去重令牌；delete 写成 `ReplacingMergeTree` 的版本与删除标记 | I170 |
| Elasticsearch、OpenSearch | 用 point in time 与 `search_after` 做快照；之后按游标字段轮询 | 游标的最后排序值 | 按由键得到的 `_id` 做 `_bulk` index 与 delete，逐条处理错误，遇 429 退避 | I168、I169 |
| MongoDB | 带 `fullDocument` 的 change stream，按 `_id` 范围的快照 | resume token | bulk write：按 `_id` upsert 与 delete | I181–I183 |
| Kafka（及 Redpanda） | 从 fit 自己保存的 offset 读取 topic 与分区 | 每个分区的 offset | 带键生产，批量、压缩 | I184、I185 |
| Redis | stream（从某个 ID 起 `XREAD`） | 最后的 ID | `XADD`，或每条记录一个键（`SET`/`HSET`/`DEL`） | I186 |
| HTTP | 记录 POST 到 fit 的路由，目标收到后才应答 | 无：客户端重试直到收到应答 | 以 JSON 或 NDJSON 批量 POST | I171 |
| S3 及兼容存储 | 某前缀下的新对象，按键顺序，逐行读取 | 最后的键及其偏移 | 由批量行组成的对象，按大小和时间滚动 | I187 |
| WASM 连接器 | 经 fit 的 HTTP 客户端和定时器读取的插件 | 插件返回的不透明字节 | 经同样方式写出的插件 | I165 |

配置中的流水线大致如下（名称可能在 I163 中调整）：

```json
"sync": {
  "state_dir": "/var/lib/fit/sync",
  "pipelines": [{
    "name": "orders-to-search",
    "source": {"type": "mysql", "addr": "db:3306", "user": "repl", "password_env": "MYSQL_REPL_PASSWORD",
               "server_id": 4101, "tables": ["shop.orders", "shop.order_items"], "snapshot": "initial"},
    "transforms": [{"type": "route", "map": {"shop.orders": "orders"}},
                   {"type": "wasm", "module": "plugins/mask.wasm"}],
    "sinks": [{"name": "es", "type": "elasticsearch", "urls": ["https://es:9200"],
               "batch": {"max_records": 1000, "max_bytes": "5MiB", "max_wait": "1s"}}],
    "on_error": "stop"
  }]
}
```

workbench 的 Sync 菜单由下面的迭代逐步建成：流水线列表及其状态、延迟和吞吐；由 schema 生成的流水线编辑器，带
**测试连接**和试运行（显示前几条记录，不写入也不保存位置）；检查点的查看与重置（需确认）；错误与死信；暂停、恢复和重新快照。

不在这条路线内：跨源的 SQL join 或聚合（流处理器的工作；WASM 变换一次只看到一个批次）；把 DDL 重放到 SQL 目标（I163 决定；
只报告、不执行）；同一条流水线同时在多个 fit 实例上运行（一条流水线运行在一个 fit 上；集群路线可以安排它的位置）；
对没有事务的目标做到恰好一次。

### I163 决定 036；记录、流水线与检查点

- **做什么**：决定 036（上述原则、记录模型、至少一次加幂等目标、协议在 fit 中自行实现，以及下面待定的选择）。包 `sync`：
  记录及其带类型的值，有一种规范的 JSON 形式；`config.Sync`，含 `state_dir` 和 `pipelines`（源、变换、目标，`mode` 为
  `stream` 或 `once`，`on_error` 为 `stop`、`skip` 或 `dead_letter`，队列与批次上限），注册进模块注册表，使连接器成为模块
  （`sync.sources.*`、`sync.sinks.*`、`sync.transforms.*`），由 `fit validate` 检查并在 schema 中描述；运行时：源写入有上限
  的队列，变换按顺序执行，每个目标按记录数、字节数和时间分批并确认；确认推进一条由连续位置组成的水位线，它绝不落在事务
  中间；检查点存储把水位线写到 `<state_dir>/<pipeline>.json`（临时文件、`fsync`、改名），最多每 `checkpoint_interval` 一次
  以及停止时，流水线从它启动；带退避的重试，之后按 `on_error` 处理；测试用的 `generate` 源以及 `memory` 和 `stdout` 目标；
  重载时增加、删除并重启配置变化了的流水线，其他流水线继续运行。
- **完成标准**：记录从 `generate` 流到两个目标，检查点是两者都已确认的最小值；目标在批次中途失败、写入与检查点之间崩溃、
  在随机位置 `kill -9`（由一个能在任意位置停下运行时的测试辅助完成）都不丢数据，只重放检查点之后的部分；检查点从不落在
  事务中间；队列满时源停止，目标卡住时内存保持在上限之内；`once` 在源结束时停止并写下检查点；`dead_letter` 把记录及其错误
  写到指定的目标；重载只重启发生变化的流水线；在竞态检测器下通过。
- **涉及**：`docs/decisions`（036）、`sync`、`config`、`module`、`schema`、`cli`、`docs`。

**待审查的决策**（实现时所做，2026-11-08；在用户裁定前按此执行）：

1. **包路径。** 目录是 `sync`，包名是 `fitsync`：规划中的路径（`sync/file`、`sync/mysql/wire`）和配置中的 `sync` 保持不变，
   包名也不会遮蔽运行时要导入的标准库 `sync`。另一种做法：目录叫 `datasync`。
2. **目标的形状。** `{"name", "sink": {"module", "params"}, "batch"}`：模块放在单独的引用中，schema、解码器和 `module.Load`
   像对待所有模块引用一样对待它。另一种做法：规划草图中扁平的 `{"name", "type", ...参数}`，需要专门的解码器和 schema。
3. **死信。** `dead_letter` 是单独的目标，`on_error: dead_letter` 时必填、否则被拒绝，而不是 `sinks` 之一（那样它还会收到每条
   记录）。死信是一条 `message` 记录，其 `after` 包含 `error`、`at`、`attempts` 和 `record`（规范 JSON）。另一种做法：
   `dead_letter` 指定 `sinks` 中的一个。
4. **设置 `sync` 时 `state_dir` 必填**：检查点放在哪里值得写明。另一种做法：像 ACME 的存储一样默认放在
   `$XDG_DATA_HOME/fit/sync` 下。
5. **重试设置属于流水线**，由它的目标、死信目标和源的重启共用。另一种做法：每个目标各有一份。
6. **批次错误没有指出哪条记录时**整批重试；次数用完后把第一条记录当作失败的记录交给 `on_error`，其余以新的次数重试，所以
   宕机的目标会让每条被跳过的记录都耗尽重试次数。另一种做法：逐条写一遍找出坏记录。
7. **变换按记录调用**（`Apply(r, emit)`），在分发 goroutine 上运行；面向 WASM 的批量接口由 I165 加入。另一种做法：现在就提供
   批量接口。
8. **源的错误使流水线在退避后从检查点重启，只要失败就一直重启**；只有目标和变换的错误按 `on_error` 处理。另一种做法：源的
   错误用完次数后也按 `on_error` 处理。
9. **字节数是整数**（`max_bytes: 4194304`），与 `rotate` 和媒体库一致，而不是规划草图中的 `"5MiB"`。另一种做法：为所有字节
   字段引入带单位的大小类型。
10. **队列上限从 `Emit` 起计算一条记录，直到水位线越过它**，所以一个上限就覆盖了队列、变换和每个目标待写的批次；大于
    `max_bytes` 的记录在队列清空后单独通过。另一种做法：每个阶段各有上限。
11. **检查点保存后才告诉源**（`Confirmer`），而不是水位线一动就告诉，PostgreSQL 复制槽因此绝不会释放文件里还没有的部分。
12. **崩溃测试用钩子**（`Options.Crash` 在六个具名位置像 `kill -9` 一样杀掉流水线），而不是真的杀掉一个子进程。另一种做法：
    用 SIGKILL 杀掉一个辅助程序，更慢，也不能移植到 Windows。
13. **还没有指标、Admin API 或状态端点**：`Runtime.Status` 留给 I188 和 I189；在此之前，流水线的状态见它的日志，logger 为单独的
    `sync`。

### I164 内置变换

- **做什么**：`filter`（按带通配的命名空间、按操作、按字段值）、`route`（命名空间改名或映射，例如表到索引或 topic）、
  `fields`（保留、删除、改名、设常量、移入或移出嵌套 map）、`key`（从字段选择或构造键）、`cast`（字段类型）、`mask`
  （对字段做哈希或置空）和 `envelope`（把记录变成扁平文档、before/after 对或 Debezium 的形状，供需要的目标使用）；每个都是
  模块，按顺序串联。
- **完成标准**：每个变换都有覆盖所有值类型和操作的表驱动测试；一串变换每条记录至多复制一次记录；delete 在任何会删掉键字段
  的变换后仍保有键，否则拒绝该配置。
- **涉及**：`sync/transform`、`schema`、`docs`。

**待审查的决策**（实现时所做，2026-11-08；在用户裁定前按此执行）：

1. **放在哪里。** 就在包 `fitsync` 中（`sync/transform*.go`），而不是包 `sync/transform`：它们要读值的内部和记录的
   复制标记。另一种做法：子包，只用导出的访问方法。
2. **一串变换只复制一次。** 记录由某个变换用 `Clone` 做出时，`Record.Own` 返回它本身，否则返回一个副本；`Emit` 清掉
   这个标记，所以源的记录保持读到时的样子（死信用它），一串变换无论有多少个改动记录，都只复制一次。变换对自己拥有的每条
   记录只 emit 一次。分发器的 `emit` 闭包现在只创建一次，而不是每条记录、每个变换各一次（那样会分配）。改动过的值仍会为
   新的字段列表分配内存。另一种做法：每个改动的变换各复制一次，或者就地改动源的记录。
3. **字段按行顶层的名字指定**，没有点号路径；`fields` 的 `unnest` 和 `nest` 把字段移入或移出 map。行是 `after`，`after`
   为 null 时是 `before`。另一种做法：处处用点号路径或 JSON pointer。
4. **`filter` 按文本比较字段**：`equals` 和 `in` 是字符串，与值的文本（`"42"`、`"true"`、RFC 3339 时间）比较，
   `present` 判断缺失或 null。另一种做法：用规范 JSON 形式的带类型值。
5. **`route`**：先按 `map` 精确匹配，再按 glob `rules`，其 `to` 是由 `{namespace}`、`{first}`、`{rest}` 和 `{last}`
   组成的模板；`unmatched` 为 `keep` 或 `drop`。不转小写，也不用正则表达式。另一种做法：带捕获组的正则表达式。
6. **`fields`** 依次做 `unnest`、`nest`、`rename`、`drop` 或 `keep`（不能同时），最后 `set`；名字冲突使记录失败；改名也作用
   于键的字段，使键指向目标的列名；其他操作都不碰键；`set` 取规范 JSON 形式的值。
7. **`key`**：`before` 缺少这些字段的 delete 保留已有的键；其他缺字段的记录失败；DDL 原样通过；格式有 `map`、`value`
   和 `join`（`:`）。
8. **在字段被移除之后构造的键在校验时被拒绝**：`key` 位于删除、不保留、改名改走、嵌入或展开其字段的 `fields` 之后，
   位于 `null` 的 `mask` 之后，或位于 `pair` 或 `debezium` 的 `envelope` 之后。另一种做法：允许，运行时让记录失败。
9. **`cast`** 也作用于键，按其文档中的表转换：int 和 uint 按 Unix 秒转成时间，时间取其自身偏移下的日期，任何值都能按
   普通 JSON 转成 `json`，bytes 只有是 UTF-8 时才能转成 string；null 保持 null。另一种做法：为时间提供 `layout` 或单位
   选项。
10. **`mask`** 默认 `hmac_sha256`，密钥只从 `key_env` 读取（不能写在配置里），在流水线加载时读取，所以 `fit validate`
    也需要这个变量；允许 `sha256`，文档注明对小集合可被反推；还有 `null`。值按其文本哈希，结果为小写十六进制字符串；
    哈希作用于键的字段，`null` 不作用于键。另一种做法：密钥放在 `admin.store`，或者在第一条记录时才读取。
11. **`envelope`** 只改造 insert、update 和 delete（DDL 和 message 原样通过）；`before` 变为 null，文档放在 `after`；
    扁平的 delete 取 `before`，没有时取键；`deletes` 为 `keep`、`drop`，或 `rewrite` 成 update。Debezium 形状的
    `ts_ms`（顶层和 `source` 中）取提交时间，`op` 为 `c`、`u` 或 `d`（有快照之前没有 `r`），`db` 和 `table` 在命名空间的
    第一个 `.` 处拆开。
12. **规模**：代码约 1,170 行、测试约 730 行，超出计划的 1,500 行，因为七个变换要放在一起才说得通（链的检查、复制次数）；
    没有拆出 I164b。

### I165 同步的 WASM 插件：变换、编解码器与连接器

- **做什么**：在 `plugin/wasm` 的 HTTP ABI 旁增加宿主模块 `fit_sync`，共用其运行时、实例池和上限。**变换**插件导出
  `fit_sync_transform(batch)`：读取批次（记录的规范 JSON，基准测试需要时再加二进制形式），并通过 `record_emit` 为每条记录
  输出零条或多条记录，由此实现过滤、修改、拆分和路由。**编解码器**插件把字节变成记录（`fit_sync_decode`，用于文件行、
  Kafka 消息、HTTP 请求体和 S3 对象），把记录变成字节（`fit_sync_encode`）。**连接器**插件可以是源
  （`fit_sync_poll(checkpoint)` 返回记录和下一个检查点）或目标（`fit_sync_write(batch)`），通过宿主函数完成 I/O：经 fib 异步
  客户端向配置允许的主机发出 HTTP 请求、定时器、插件配置和日志；没有原始套接字。每次调用都有时间和内存上限；陷入异常或
  超限的插件使该批次失败，随后按 `on_error` 处理。guest SDK（`plugin/wasm/guest`，Go 与 TinyGo）和一个 Rust crate 增加
  同步相关的调用，并附示例。
- **完成标准**：一个 TinyGo 变换能丢弃、修改和拆分记录；一个编解码器能从文件读取自定义行格式；一个连接器插件能把分页的
  HTTP API 读入目标，并在重启后从检查点继续；超出时间或内存、或调用了不允许的主机的插件使批次失败，检查点保持不动；
  实例池让流水线的吞吐与做同样工作的内置变换相差不超过 20%，在压测主机上测量。
- **涉及**：`plugin/wasm`、`plugin/wasm/guest`、`sync`、`docs`、fit-examples。

**待审查的决策**（实现时所做，2026-11-08；在用户裁定前按此执行）：

1. **在按记录的接口旁加一个批量接口**（I163 的决策 7）。`BatchTransform` 在 `Transform` 之上加
   `ApplyBatch(in, emit(i, o), fail(i, err)) error`；分发器把手上的记录逐个变换地处理（`sync/stages.go`），批量变换一次
   调用处理全部，失败仍按源的顺序处理，所以 `on_error` 和检查点的行为不变。另一种做法：只按记录调用，每条记录一次 WASM
   调用。
2. **失败的单位是一次调用，而不是分发器手上的整批。** `wasm` 变换把记录按 `batch_records`（256）分成多次调用，在至多
   `instances`（CPU 数，至多 4）个实例上同时运行，并按顺序交出结果；一次调用失败、陷入异常、超过 `timeout` 或申请超过
   `max_memory_bytes` 的内存，会使它的每条记录失败，由 `on_error` 处理；插件也可以用 `fail(i)` 使单条记录失败。
   另一种做法：让分发器手上的全部记录失败。
3. **ABI 的形状。** 每个导出函数接收其输入的长度，插件用 `input(buf, len)` 取得输入，就像请求 ABI 的读取函数写入插件的
   缓冲区那样；结果经 `record_emit(index, json)`（索引不回退）、`fail(index, msg)`、`output(bytes)` 和
   `checkpoint(json)` 交出；导出函数返回 0、1（轮询已追上）或 2（失败）。插件的配置和日志就是请求 ABI 自己的
   `request_get(5)` 和 `log`，在同步调用中也能使用。另一种做法：宿主写入插件经导出的 `malloc` 分配的内存。
4. **与请求插件共用什么、不共用什么。** 编译后的插件、它的运行时、内存上限、编译缓存和宿主模块是共用的（`loadPlugin`，
   它不再检查 `fit_on_request`：由中间件检查）；池是同步模块自己的：空闲实例放在通道里，由流水线的 goroutine 等待，首次
   需要时创建，调用失败后重建，每次调用以其 context 上的截止时间为界，而不用请求池的队列和看门狗。Go 插件导出所有同步
   调用，因为包 `guest` 声明了它们全部；没有为之注册处理函数的调用会失败。另一种做法：两者用同一种池。
5. **HTTP 和 sleep 只给连接器。** 变换或编解码器的插件调用 `http_request` 或 `sleep` 会使其调用失败。`allow_hosts` 的
   条目是主机（任意端口）、`host:port` 或 `*.domain`；为空则什么都不允许。请求经 fib 的异步客户端，在角色的引擎
   （`Options.Engine`，上游引擎）上发出，插件的调用在流水线的 goroutine 上于其 `timeout`（连接器为 30s）内等待；请求体是
   请求 JSON 中的文本，响应体至多 `max_response_bytes`（4 MiB）。另一种做法：用 base64 传二进制请求体；按 URL 前缀而
   不是主机限制。
6. **一次轮询是一个整体。** 源清除插件记录上带的事务和位置，把这次轮询的下一个检查点放在它的最后一条记录上，所以检查点
   绝不落在一次轮询内部；没有记录却移动了检查点的轮询只移动内存中的检查点，重启后会再轮询一次。轮询失败会让流水线从其
   检查点重新开始（I163 的决策 8）。另一种做法：由插件给每条记录设置位置。
7. **目标的插件用 `fail(i)` 指出失败的记录**；它指出的最早一条就是 `BatchError` 的索引，其前面的记录已写入。
8. **编解码器现在是一个命名空间**，`sync.codecs`，接口为 `Codec`（`Decode(data, emit)`、`Encode(b, recs)`），模块为
   `wasm`（它的两个导出函数各自可选，至少要有一个）；I166 之前没有源或目标使用它，所以测试通过读取一个插件自定义行格式的
   文件并写回来证明它。参考中有一部分 *Sync codecs*。另一种做法：把这个接口留给 I166。
9. **测试用 Go 而不是 TinyGo。** 测试在运行时构建 Go 的 `wasip1` 插件，与请求插件的测试一样；这台机器上没有 TinyGo，
   所以计划中的 TinyGo 变换是一个 Go 变换，TinyGo 的构建留给 I165b。guest SDK 手工读取批次、写出记录，而不用
   `encoding/json`，后者的反射在 WebAssembly 中很慢（下面的基准从每条 160 µs 降到 46.5 µs），这也避开了 TinyGo 的反射。
10. **Rust crate**（`plugin/wasm/guest/rust`，`fit-guest`，没有依赖）包装导入函数，并把批次拆成各条记录；它的单元测试在
    宿主上运行，但还没有运行过由它为 `wasm32-wasip1` 构建的插件，因为这台机器缺少这个目标：作为插件未经测试，与示例一起
    留给 I165b，示例放进 fit-examples。
11. **基准测试远未达到 20%**，在本机：`BenchmarkSyncTransform` 在 Apple M4 Pro 上（不是压测主机），内置的 `route` 每条
    81 ns；在 Go 插件中做同样工作的 `wasm` 变换，一个实例每条 46.5 µs，四个实例每条 12.6 µs。主要开销是 JSON 进出 Go
    插件；因此需要规划决策 8 所说的二进制形式，与压测主机上的测量一起放在 I165b。另一种做法：在二进制形式完成前不结束
    I165。
12. **模板列出单个引用的所有模块。** 流水线的 `source` 和目标的 `sink` 以前只写出其命名空间的第一个模块；现在其余模块
    带着全部参数以注释形式跟在后面，所以模板写出了每个字段，`allow_hosts` 也在内。处理器和中间件以外的模块章节是
    `schema/doc/modules/<namespace>_<name>.md`（`sync_transforms_wasm.md`），因为 `wasm` 是多个命名空间中的模块名。
13. **规模**：代码约 2,000 行（含 guest SDK 的 640 行和 Rust crate 的 180 行），测试约 860 行，超出计划的 1,500 行；
    已完成的部分不再拆出，剩下的由 I165b 承担。

### I165b 同步 WASM 插件的其余部分

- **做什么**：在压测主机上运行 `BenchmarkSyncTransform`，以及经过 `wasm` 变换的流水线；如果实例池仍达不到内置变换的
  20% 以内，加入规划决策 8 允许的记录二进制形式（以及分配更少的宿主解码器）；在测试（和 CI）中构建并运行一个 TinyGo
  变换和一个 Rust 插件；在 fit-examples 中提供变换、编解码器、源和目标的示例。
- **完成标准**：记录了压测主机上的数字，且 20% 成立，或者差距得到解释并被接受；TinyGo 和 Rust 插件通过与 Go 插件相同的
  测试。
- **涉及**：`plugin/wasm`、`plugin/wasm/guest`、`sync`、fit-examples。

**待审决定**（除压测主机上的运行外均已完成，2026-11-09；在用户裁定前有效；该运行完成前本项保持 todo）：

1. **同样的测试，每种客体一个子测试。** `TestSyncTransform`、`TestSyncTransformFailures`、`TestSyncCodec`、
   `TestSyncConnectors`、`TestSyncTransformInAPipeline` 和 `TestSyncFailuresKeepTheCheckpoint` 分别以 `/go`、
   `/tinygo`、`/rust` 运行（`plugin/wasm/guests_test.go`）：TinyGo 原样构建 Go 插件自己的源码 `testdata/sync`
   （`tinygo build -target=wasip1 -buildmode=c-shared`，默认参数，与用户一样）；Rust 构建 `testdata/rust`，一个基于
   `fit-guest` crate、行为相同的插件。全部通过（本地容器中：TinyGo 0.44.0 配 Go 1.27.3，Rust 1.99.0）。备选：按计划
   字面，各写一个更小的 TinyGo 变换和 Rust 变换。
2. **缺少工具链时跳过，除非要求。** 没有 `tinygo`，或没有带 `wasm32-wasip1` 目标的 `cargo`/`rustc` 时，该客体的子测试
   跳过，因此 Test 和 Coverage 任务（没有这些工具链）不受影响，也没有改动生产代码；`FIT_WASM_GUESTS=tinygo,rust`
   让缺少的工具链变成失败。CI 新任务 **WASM guests (TinyGo, Rust)** 安装 TinyGo 0.44.0（.deb）和 Rust 1.99.0
   （`rustup`，`--target wasm32-wasip1`），运行 crate 的 `cargo test`，并在设置该变量的情况下以 `-race` 运行
   `TestSync*`。备选：构建标签；或在 testdata 中放预先构建的插件。
3. **Rust crate 像 Go 客体一样手工读取记录。** `fit-guest` 新增 `Record`、`parse_batch`、`Row`、值辅助函数（`int`、
   `string`、`boolean`、`as_int`、`as_str` 等）、`members`/`elements`、`emit_record`、`request`（由各部分组成 HTTP
   请求）和 `response_header`，仍然没有依赖；`records` 保留。备选：测试插件和示例使用 serde_json，crate 保持 I165 的样子。
4. **BenchmarkSyncTransform 覆盖每种客体**：`builtin`，然后是 `wasm-<go|tinygo|rust>-<1|4>`，无法构建的客体跳过，因此
   压测主机需要安装 TinyGo 和 Rust（版本同 CI 任务）才能得到全部数字。本地（Apple M4 Pro 上的容器，不是压测主机）每条
   记录：内置 206 ns；Go 单实例 49.9 µs、四实例 15.3 µs；TinyGo 45.8 / 14.3 µs；Rust 30.2 / 11.3 µs。每种客体每条记录
   都有约 209 次宿主分配（每批 256 条 53.5k 次），所以占主导的是宿主对插件输出 JSON 的解码，而不是客体语言：压测主机的
   运行最可能要求的是二进制形式及分配更少的宿主解码器。留给主会话与主机运行一起处理。
5. **示例**：fit-examples 分支 `wasm-sync-examples` 在 I194 的 `sync-wasm-transform` 之外新增 `sync-wasm-codec`（从文件
   读取自己的行格式并写回，坏行进入死信）、`sync-wasm-source`（由 fit 自己提供的分页 API，检查点是页面及其中的进度）和
   `sync-wasm-sink`（以 NDJSON 发送到由 fit 自己应答的 webhook）；每个都只需 fit 即可运行，且已实际运行；插件用 Go 编写，
   也能用 TinyGo 0.44 构建（已验证），注释中有说明。那里没有 Rust 示例：它的 CI 只构建 Go。备选：增加 Rust 示例，并在
   fit-examples 的测试中加入 Rust 工具链。

**记录的二进制形式**（规划决定 8，2026-11-09 在压测主机运行之前完成，因为本地数字已经要求它；与其余各项一样待审）：

6. **形式。** `fitsync` 在规范 JSON 之外新增记录的二进制形式（`sync/binary.go`）：字符串是 uvarint 长度加字节；值是一个
   标签（`0x10` 加其种类：null；bool 一个字节；int 为 zigzag varint；uint 为 uvarint；float 为 8 字节；decimal、
   string、bytes 和 json 为字符串；time 为 Unix 秒、纳秒和偏移；date 为年、月、日），数组和映射在计数前有 4 字节的长度，
   插件无需读入即可越过任何值；`0x1F` 表示以规范 JSON 写的值，手写的值依然可用；这些标签是任何 JSON 文本都不会以之开头的
   控制字符，值的首字节即说明其形式。记录依次是 op 和事务标志字节、source 与 namespace、key、before 与 after、RFC 3339
   文本的提交时间、事务 id 和位置的 JSON。批是计数加记录；插件交回的是任意多个“下标加记录”。备选：记录框架仍用 JSON，
   只有值用二进制；现成格式（CBOR、protobuf、FlatBuffers），每种客体都需要相应的库。
7. **按插件协商，双向使用。** 导出 `fit_sync_form(offered)` 的插件在实例创建时被提供形式 1，返回它采用的形式，不超过所提供
   的（超过则加载失败，与 trap 一样）；采用者以该形式读取批（变换、编码、写入），并通过新的导入 `records_emit(buf, len)`
   一次交回多条记录。不导出它的插件（之前构建的所有插件）照旧读取 JSON、调用 `record_emit`，不受影响；`record_emit` 在
   两种形式下都是 JSON。每个实例必须采用与第一个相同的形式。基于新 SDK 构建的插件导入 `records_emit`，因此需要有它的 fit；
   反过来则成立。备选：在自定义段中写版本；另一套导出。
8. **分配很少的宿主解码器。** `fitsync.BinaryReader` 把插件交回的内容复制一次，作为字符串，记录中的字符串、位置和字节都是
   它的切片，记录、映射字段和数组元素从块中切出；`fitsync.BinaryBatch` 写批时记下每条记录的值、提交时间和位置所在，
   为输入记录 i 交回的记录中，与记录 i 逐字节相同的部分直接取记录 i 的（值一经创建不再改变），不再读取：只改 namespace
   的变换完全不解码值。插件调用的缓冲放在该插件的池中复用，`ApplyBatch` 的调用者自己也是工作者之一。每条记录的宿主分配：
   之前约 209 次，现在 0.18 到 0.21 次。备选：不复制、直接在插件内存上建记录，实例再次运行后即不安全。
9. **Go SDK 使用它，并复用调用的内存。** `guest.Value`（任一形式的 `[]byte`）取代 `Record.Key`、`Before`、`After` 和
   `Row` 字段中的 `json.RawMessage`；使用辅助函数和 `[]byte` 字面量的代码照常编译，把值写成 `json.RawMessage` 的代码需要
   修改。辅助函数读取两种形式，按采用的形式构造值；新增 `Decimal` 和 `AsDecimal`，`Value.MarshalJSON`（以及
   `Record.MarshalJSON`）写出规范 JSON；在 init 中调用 `JSONForm()` 可让插件保持 JSON。`Emit` 以二进制形式汇集记录，在
   导出返回时（或每 64 KiB）交出。输入和批的记录由下一次调用复用，这对 TinyGo 的分配器帮助最大（64 条一批，从每条 6 µs
   降到 0.45 µs）：处理函数拿到的批及其记录中的内容只属于本次调用，要保留就复制。fit-examples 的 `sync-wasm-codec` 通过
   扫描值的 JSON 文本读取价格，在二进制形式下读不到：应改用 `guest.AsDecimal`（或调用 `guest.JSONForm()`），留给
   fit-examples。备选：每次调用都用新内存；把值转换成 JSON 交给插件。
10. **TinyGo 插件以 `-scheduler=none` 构建。** TinyGo 默认调度器为每次导出调用启动一个 goroutine，此处每次调用约 430 µs，
    与形式无关；同步插件不需要 goroutine，所以测试以 `-scheduler=none` 构建，模块章节中也作此说明。这取代决定 1 中的
    “默认参数”。备选：默认参数，TinyGo 插件慢十到一百倍。
11. **Rust crate 通过默认 feature 使用它。** `fit-guest` 在默认 feature `binary` 下导出 `fit_sync_form`；
    `default-features = false` 保持 JSON，供用 `records` 拆批并用 JSON 库解析的插件使用。`parse_batch`、`Row` 和辅助
    函数读取两种形式、构造采用的形式（新增 `decimal`、`as_decimal`、`value_json`）；`emit_record` 立即交出一条记录，
    `Emitter` 汇集记录，在导出结束时被 drop 时交出。备选：仅当插件选择时才用二进制。
12. **测试以两种形式运行每种客体**：决定 1 中的每个 `TestSync*` 以 `<guest>/binary` 和 `<guest>/json` 运行，后者不提供
    任何形式（`offerForm`），与以前的 fit 一样，也就是旧插件所见；`TestSyncBinaryEmit` 和 `TestSyncFormNegotiation`
    用手工构造的模块测试 SDK 不会做的事；`sync/binary_test.go` 覆盖整个 `binary.go`。`BenchmarkSyncTransform` 改为
    `wasm-<guest>-<form>-<instances>`，并报告 `allocs/record`。
13. **测量结果与剩余差距。** `BenchmarkSyncTransform`，每条记录，64 条一批，Apple M4 Pro（不是压测主机）。之前，在容器中
    （Docker Desktop，8 CPU）：内置 279 ns；Go 单实例 49.3 µs、四实例 15.2 µs；TinyGo 44.9 / 14.7 µs；Rust
    30.8 / 11.4 µs；每条 209 次宿主分配。之后，二进制形式，同一容器：内置 255 ns；Go 3.1 / 2.6 µs；TinyGo 1.5 / 1.4 µs；
    Rust 1.1 / 1.06 µs；0.18 到 0.21 次分配。容器中四实例几乎无益：其虚拟机唤醒空闲线程太慢，其他工作者要等第一次调用结束
    才开始（逐次计时可见），所以同一构建也在本机运行了（TinyGo 和 Rust 插件在容器中构建）：内置 89 ns；Go 3.2 µs / 1.03 µs；
    TinyGo 1.5 µs / 502 ns；Rust 1.08 µs / 437 ns。JSON 形式与以前相同（本机 Go 51.8 / 12.8 µs）。因此四实例是内置的
    4.9 倍（Rust）到 11.6 倍（Go），不在 20% 以内：剩下的是插件自己的工作，即在其内存中读取批并逐条写回记录（Go 的
    WebAssembly 代码最慢），以及宿主写批（每条约 110 ns）；宿主读取现在主要是比较。更多实例、更大的 `batch_records`、
    只在被问到时才读取记录各部分的客体，或者一个“该值原样”的标记让客体不复制未改的值，都能缩小差距；对每条记录都经过的
    插件来说，很难达到只克隆一条记录、查一次映射的内置变换的 20% 以内。由压测主机的运行决定。
14. **压测主机的运行（2026-11-09），接受剩余差距。** 在压测主机上（Ryzen 7 7800X3D，Linux 7.0，Go 1.27.3，TinyGo 0.44.0，
    Rust 1.99.0，容器绑定八个 CPU），五次运行的每条记录平均值，单实例 / 四实例：内置 109 ns；二进制形式 Go 3.38 / 1.15 µs、
    TinyGo 1.73 µs / 647 ns、Rust 1.64 µs / 590 ns，每条 0.18 到 0.21 次宿主分配；JSON 形式 Go 55.6 / 16.3 µs、TinyGo
    43.2 / 13.3 µs、Rust 32.7 / 9.4 µs，209 次分配。二进制形式比 JSON 形式快 15 到 28 倍；四实例是内置的 5.4 倍（Rust）到
    10.5 倍（Go），不在 20% 以内，原因见决定 13。差距按其解释接受而不再缩小：插件用于内置变换做不到的事，需要其吞吐的流水线
    运行更多实例。运行及其输出见 [`bench/results/sync-2026-11-09/wasm`](../bench/results/sync-2026-11-09/wasm/bench.txt)。
    备选：下一步做客体侧的“未改”标记，减少复制。

### I166 文件作为源与目标

- **做什么**：源 `file`：路径与通配，每 `scan_interval` 扫描一次；每个文件以设备号、inode 和文件开头字节的指纹识别，因此
  能跟随改名，复用的 inode 被视为新文件；截断后从头读；读取中被删除的文件先读完；每行不超过 `max_line_bytes`，不完整的
  末行保留到它结束或超过 `close_after`；按起始模式合并多行；同时读取的文件数有上限；检查点是每个文件的偏移；由编解码器
  （`line`、`json`、`csv`、WASM 编解码器）生成记录。目标 `file`：记录按行写出（原样、JSON、CSV 或模板），经 `logs` 的文件
  写入器，因此切割、保留和压缩沿用 I155。
- **完成标准**：跟踪经过改名切割、复制后截断和删除时不丢行也不重复行，用一个写编号行、在随机位置崩溃的写入程序检查；一万个
  文件时保持在打开文件数上限内；超长的行被截断并计数；目标与日志一样切割。
- **涉及**：`sync/file`、`logs`、`docs`。

**待审查的决策**（实现时所做，2026-11-08；在用户裁定前按此执行）：

1. **代码位置。** 源和目标在包 `sync/file`（`syncfile`）中；编解码器 `line`、`json` 和 `csv` 是 `fitsync` 本身中
   `sync.codecs` 的模块（`sync/codec.go`），以便 Kafka、HTTP 和 S3 也能使用。模块的 `codec` 是其参数中的一个模块引用，
   由新增的 `module.Context.Load` 用该模块自己的配置加载；不设置时为 `line`。另一种做法：每个连接器一个 `format` 枚举。
2. **检查点**是每个文件的设备号、inode、指纹（哈希和长度）、路径、偏移、跳过标志及其记录键中的文件部分，以及最近被截断的
   文件的开头（见下文），以 JSON 表示。尚未读取的文件和被暂缓的副本不计入；扫描不再找到的已关闭文件在被错过 `close_after` 之后才被遗忘（决策 15），因此检查点
   至多包含通配符匹配到的文件（`max_files`，100000）。一个位置要写出整个列表，所以源只在行与行之间设置位置，放在每一轮读完
   各文件后的最后一条记录上，或每 1,000 条记录的最后一条上（为此它留住一条记录），而不是每条记录都设。另一种做法：每条记录
   都带位置，或源自己的状态文件。
3. **文件的身份**是其设备号和 inode，以及其前 `fingerprint_bytes`（1024）字节的指纹，随文件增长到该长度时取得；已知
   inode 的开头不再匹配，或其大小小于偏移时，从 0 重新读取。**Windows** 没有 inode：使用卷序列号和
   `GetFileInformationByHandle` 给出的文件索引，以共享读、写和删除的方式打开文件，使写入方仍可改名或删除它们；扫描时
   短暂打开每个匹配到的文件以识别它。两者都没有的系统（fit 不为其发布）使用路径的哈希，因此在那里改名即是新文件。
   Windows 上的另一种做法：只用路径和指纹。
4. **跟随复制后截断，而不只是截断。** 被截断文件的开头、偏移和键保留 `close_after` 那么久（也在检查点中）；开头与之匹配
   的新文件是它的副本，以原文件的键从该偏移接着读。开头与一个尚未截断的文件匹配的新文件被暂缓，直到那个文件被截断，然后
   同样处理；如果先过了 `close_after`，它就是一份备份，整个读取（宁可重复，不可丢失）。没有这一点，copytruncate 要么丢失
   原文件未读的尾部，要么把副本再读一遍。发现时比原文件的指纹还短的副本被当作新文件。另一种做法：忽略副本，如 Filebeat 的
   inode 模式。
5. **记录的键。** 编解码器没有给出键的记录得到 `{"file", "offset"}`（一行的第二条及以后的记录再加 `n`）：file 为
   `<设备号>:<inode>:<首行的哈希>`，副本沿用原文件的，因此崩溃后再次读到的行有与先前相同的键，幂等的目标会将其合并。
   用首行而不是截断次数来区分截断前后的内容，因为在检查点与崩溃之间截断两次时，重放得到的次数会不同。注意：同一 inode
   先后的内容若首行相同，会共用键。另一种做法：不设键；用计数。
6. **"不丢行也不重复行"** 的测试方式是：每一行都到达，且没有一行以两个键到达，除非较早的那次在其所在运行保存的检查点之后
   （被杀死或被停止）；同一个键下的重复属于至少一次语义，由幂等的目标合并。崩溃后发现的副本无法得知其原文件那次运行写出但
   未保存的内容，所以只有这种情况可能以第二个键重复。
7. **编解码器拒绝的行**变成一条 message 记录，其 `after` 保存 `line` 和 `error`，而不是被丢弃，也不使源失败（那样会在同一行
   上无限重启）。另一种做法：跳过并计数；`on_error`。
8. **不完整的行不占内存**：源每一轮从最后一个完整行的末尾读起，所以不完整的行被重新读取而不是保留；多行事件被保留（每个打开的
   文件至多 `max_line_bytes`），其起点就是检查点的偏移。`close_after`（1m）既是不完整的末行或事件等待的时间，也是关闭空闲
   文件的时间；不完整的行在增长也算作活动。在 `once` 中，文件最后不完整的行按现状取用。
9. **超长的行**在 `max_line_bytes`（1 MiB）处截断，其余部分跳过（跳过状态在重启后仍保留），计数（`Source.LinesCut`），
   第一次和每第一千次记日志；超过上限的事件同样截断。该计数还不是指标（I188、I189）。
10. **打开的文件。** `max_open_files`（256）：超过时关闭一个空闲文件（已读到末尾）以打开另一个，已删除的优先，并先取用其
    不完整的末行，因为它无法再次打开；扫描发现已关闭的文件增长时再次打开它。打开期间被删除或改名到通配符之外的文件读到末尾，
    在 `close_after` 内不再增长后关闭。关闭期间被改名到通配符之外且有未读行的文件，源再也读不到，与 Filebeat 相同。
    `poll_interval`（250ms）是所有打开的文件都读到末尾后的等待时间。
11. **目标经由进程的日志文件写出**（`logs.DefaultFiles`，新增的 `logs.File.WriteSync`）：一个批次在其各行写入、文件在目标的
    写入 goroutine 上同步之后才确认，每次写入约 64 KiB 的完整行，使轮转落在行与行之间；`WriteSync` 等待磁盘，在同步自己的
    goroutine 上这没有问题。文件在第一次写入时打开，因此 `fit validate` 不会打开任何文件，并在流水线停止时关闭。一个路径
    一个目标：该路径上已有另一个文件目标，或 fit 的日志或访问日志时，在第一次写入时拒绝（目标随后重试），而不是在检查配置时。
    `rotate` 和 `time_zone` 与日志的相同。各行由编解码器生成，或由 `template`（`{field}`、`{$namespace}`、`{$op}`、`{$key}`、
    `{$commit_time}`、`{$record}`、`{{`）生成。编解码器无法编码的记录是该批次失败的记录，其之前的已写出。另一种做法：目标
    自己的写入器；在 `fit validate` 中拒绝共用的路径。
12. **`json` 解码**：整数为 int，超出 int64 时为 uint；带小数或指数的数为 float；超出 float 范围的为 decimal；成员保持其顺序。
    `csv` 把一行的值读作字符串，按 `columns` 命名，超出的命名为 `c<n>`。
13. **Windows 上的测试**：重命名、截断和删除被打开文件的测试在 Windows 上跳过，那里这些操作的规则不同；其他测试在那里运行。
    vet 对 Windows、FreeBSD 和 Linux 通过；测试只在 macOS 上运行过。
14. **规模**：代码约 1,500 行，测试约 920 行（含一万个文件和崩溃的测试），超出计划的 1,500 行；没有剩余工作，因此没有 I166b。
15. **Linux 会立即复用 inode，文件在 stat 与读取之间会改变**（在 Linux 上发现：在 macOS 的 APFS 上通过的崩溃测试，在那里
    40 次中失败 7 次），由此有四条规则。一个已关闭文件的 inode 若已装着别的内容（关闭期间被截断，或被释放后由新文件占用，
    副本也在其中），被当作新发现的文件：其旧开头留给副本，新内容与保留的开头和已知文件匹配。扫描发现它被移动、大小改变或
    修改时间不同时（定宽的行让同样大小的新文件很常见），以及每次再打开它时，都会检查。扫描错过的已关闭文件在错过
    `close_after` 之后才被遗忘（检查点中的 `missing`），因为在列出目录与 stat 之间的改名会让文件在一次扫描中看不到，而立即
    遗忘会让它的副本无从匹配。每次读取之后都对照文件开头检查：在取大小与读取之间的截断会丢弃这次读取并从头读该文件，因此
    绝不会在旧偏移处取到新内容。当一个单元是文件的第一个时，键中的首行取自与该单元记录相同的那次读取。验证：崩溃测试在
    Linux 上（Docker，4 个 CPU，竞态检测下）连续 100 次通过，在 4 个和 6 个 CPU 下各连续 500 次通过；macOS 上 40 次。
    另一种做法：立即遗忘，只在大小改变时检查开头。

### I167 像 DataX 一样的批处理作业

- **做什么**：把 `mode: "once"` 做成作业：源的快照按块并行读取（源有键范围时按键范围），由 `max_parallel` 个写入者写出，
  进度（已读、已写、失败的行数和预计剩余时间）记在检查点中，作业中断后从已完成的块继续；`fit sync run <pipeline> --once`
  不启动服务器、从命令行运行一个作业并以其结果退出；调度（`schedule`，cron 语法）再次启动作业。
- **完成标准**：一个在两个分块的 `generate`/`memory` 连接器之间复制一百万行的作业在中途被杀死，重启后完成，且不重复已完成
  的块；命令以非零状态退出并给出失败记录数；调度能运行，且绝不与仍在运行的那次重叠。
- **涉及**：`sync`、`cli`、`docs`。

**待评审的决定**（实现时做出，2026-11-08；在用户裁定前有效）：

1. **分块接口。** 能拆分快照的源在 `Source` 之外实现 `Chunker`：`Chunks(ctx)` 在作业开始时规划整个快照，
   `ReadChunk(ctx, chunk, from, emit)` 从块的开头、或从该块自己的某个位置之后读取一块。`Chunk` 由命名空间、源自己的 JSON
   形式的键边界 `lo` 和 `hi`（含 `lo`、不含 `hi`，nil 表示无界）以及估计的行数组成。规划保存在检查点中（`job.chunks`），
   因此续传的作业读取它当初规划的块，即使数据已经改变；最多 100,000 块。没有块的规划表示源整体读取（`Run`）。I175、I179、
   I182 和 I169 实现它。另一种做法：边进行边找块（上一块结束时查询下一块的上界，如 Debezium 的增量快照），对很大的表检查点
   更小，但一次只能规划一块。
2. **一块只有在每个目标都拿到其全部记录后才算完成，靠的是 I163 的水位线。** 作业的位置为
   `{"done": "0-41,43", "at": [{"chunk": 42, "from": <该块的位置>}]}`，即截至携带它的那条记录时各块的状态。各块的读取者
   经同一把锁发出记录，每个都把最后一条记录留到读到下一条时再发，因此一块的最后一条记录携带标记该块完成的位置；位置放在
   这条记录上，以及每 100 条中的一条上。由于水位线只有在其之前发出的每条记录都被确认后才越过一条记录，保存的位置绝不会
   声称某个目标还没有的块或块内记录；运行时的水位线、检查点文件和崩溃点都不变。另一种做法：每块一个水位线。
3. **并行写入者只用于分块的快照，且只用于声明支持的目标。** `max_parallel` 既是同时读取的块数，也是每个 `ParallelSink`
   （新的标记接口；`memory` 是一个，数据库目标将会是）的写入者数；一条记录交给其键的哈希（键的规范 JSON 的 FNV-1a；
   没有键的记录按序号）选中的写入者，所以一个键绝不会乱序写入。源不拆分的作业以及所有流式流水线，每个目标仍只有一个写入者，
   因为它们的记录带有事务和同一个键的先后变更。另一种做法：每个写入者一个模块实例（文件目标的"一个路径一个目标"会拒绝），
   或所有作业都并行写入。
4. **快照记录不带事务**：同时读取的各块相互交错，因此 `ReadChunk` 的每条记录自成一个事务（清除 `Tx`），每条都结束一个事务。
5. **进度是检查点的 `job` 部分**：`started`、`finished`、`read`、`written`、`failed`、`rows`、`eta`、`chunks_done` 和规划。
   `written` 计每个目标都已确认的源记录（含被变换丢弃的），`failed` 计 `on_error` 为 `skip` 或 `dead_letter` 时处理掉的，
   两者都截至已保存的位置；`read` 计发出的记录，重启后检查点之后的记录再读时会再计一次。ETA 是规划中剩余的行数按当前运行的
   速度所需的时间；源不估计行数时没有。`Status` 增加 `Job`（实时）和 `Next`。检查点的版本仍为 1：这部分是可选的。
   另一种做法：单独的进度文件。
6. **已完成的作业不会自己再次运行。** 检查点中作业已完成时，fit 重启或重载都不会再运行它（其状态为 `finished`）；I167 之前，
   `once` 会从其检查点再次运行，因此文件源会读取新增的内容。调度的下一个时间点或 `fit sync run` 会开始一个新作业：分块的源
   从头开始，重新规划；其他源从上一个作业结束处开始，因此文件或游标复制新增的内容。新作业的检查点连同其规划在作业开始时
   就写出，使在第一个检查点之前被中断的作业也能被认出。I165 的连接器测试原先靠重启运行时再次运行已完成的作业，现在改用
   `RunPipeline`。另一种做法：每个新作业都从头开始；或者像以前那样，`once` 在每次
   启动时都再运行。
7. **调度的语法**：`job.schedule` 用 cron 的五个字段（分钟、小时、月中的日、月、星期几；`*`、值、范围、列表、步长、月份和
   星期的名称、7 表示星期日，以及 Vixie cron 的规则：两个日字段都受限时，匹配任一个的日子都运行）或 `@yearly`、
   `@annually`、`@monthly`、`@weekly`、`@daily`、`@midnight`、`@hourly`；没有秒字段、`@reboot` 或 `@every`。
   `job.time_zone` 为 IANA 名称或 `Local`，不设置时为 `UTC`，与流媒体频道的时间表相同。夏令时切换跳过的时间点不运行；
   重复的时间点只运行一次。解析器是 fit 自己的（`internal/cron`，约 190 行），没有依赖。另一种做法：robfig/cron，带秒字段。
8. **不重叠，不补跑。** 流水线的 goroutine 运行一个作业之后再等待下一个时间点，所以运行绝不重叠；运行期间经过的时间点被跳过
   （下一次运行在它结束之后的第一个时间点），fit 停机期间经过的时间点也不补跑，只是被中断的作业（检查点未完成）在 fit 启动时
   立即继续。另一种做法：像 anacron 那样，对错过的时间点立即运行一次。
9. **`mode` 不是 `once` 时拒绝 `job`**；`max_parallel` 不设置时为 4，最多 256。
10. **`fit sync run [-c config]... [--once] <pipeline>`**，选项可在名称之前或之后：它像 `fit validate` 那样加载配置（不编译
    配置的其余部分），只运行这一条流水线，然后退出。`--once` 把流式流水线作为作业运行；没有它时流水线按其模式运行，直到结束
    或收到 SIGINT、SIGTERM。已完成的作业重新开始，不等待调度，源连续失败 `retry.max_attempts` 次就停止它
    （`Options.SourceAttempts`），而不像在服务器中那样无限重启。日志以 info 级别写到 stderr，而不是配置的日志输出，以免写入
    正在运行的 fit 的日志文件；进度写到 stdout。退出码：0 全部写入；3 有记录失败，失败数写在 stderr；1 流水线停止，或作业被
    中断；2 用法错误。另一种做法：记录失败时也以 1 退出。
11. **`fit sync run` 没有 fib 引擎**，所以 WASM 连接器的 `http_request` 在其下会失败；它也不检查正在运行的 fit 是否在运行同一
    流水线，两者会写同一个检查点。另一种做法：它自己的引擎；`state_dir` 中的锁文件。
12. **一百万行的测试**在写出一半记录后，在六个崩溃点中随机选一个杀死作业，然后检查：检查点记为已完成的块中的记录恰好写了
    一次，每条记录至少写了一次，最多只有正在进行的四块被写了两次。不开竞态检测时复制一百万条记录（100 块，每块 10,000），
    在竞态检测下复制 200,000 条（100 块，每块 2,000），因为那里一百万条一次要约 20 秒；它在测试专用的目标 `counter` 中按 id
    计数，因为 `memory` 会保留一百万条记录（约 1 GB）。调度的测试驱动一个假的 `Clock`（`Options.Clock`）。
13. **顺带的修复：`memory` 目标与超出批次的 `BatchError`。** 索引等于批次长度的 `BatchError` 会让存储保留整个批次，而运行时
    把该索引视为越界、把整个批次再写一遍：I163 的 `TestSinkFailsMidBatch` 在 `origin/main` 上于 Linux 中 1,000 次失败 14 次
    （200 条得到 203 条）。现在索引超出批次时存储什么也不保留，与运行时的理解一致；连续 1,000 次通过。
14. **规模**：代码约 1,160 行（含 cron 解析器和 `fit sync run`），测试约 540 行，超出计划的 1,500 行；没有剩余工作，因此没有 I167b。

### I168 Elasticsearch 与 OpenSearch 作为目标

- **做什么**：目标 `elasticsearch`（OpenSearch 经同一实现，自动识别其差异）：经 fib 异步 HTTP 客户端的 `_bulk`，insert 与
  update 用 `index` 或 `create`，delete 用 `delete`，`_id` 取自记录的键，索引取自命名空间或带日期的模板，支持 routing 和
  pipeline 选项；读取逐条结果，429 与 5xx 退避重试，mapping 错误交给 `on_error`；basic 认证、API key、带 CA 的 TLS。
- **完成标准**：在 Docker 中与 Elasticsearch 8 和 OpenSearch 2 互通：insert、update 和 delete 在重放后收敛；一个 bulk 中有
  一个坏文档时只有它失败；429 带来的背压减慢流水线而不丢记录。
- **涉及**：`sync/elasticsearch`、`test/sync`、`docs`。

**待审查的决策**（实现时所做，2026-11-08；在用户裁定之前一直有效）：

1. **放在哪里。** 包 `sync/elasticsearch`（`syncelastic`）：目标，以及 I169 的源将共用的 `Conn`（URL、凭据、TLS、故障转移、
   集群的 `Info`）。连接参数由目标自己声明，因为 fit 的参数解码器和 schema 不接受嵌入的结构体；源将声明同样的参数。备选：
   一个嵌套的 `connection` 对象。
2. **写什么。** insert、update 或 message 写成行（`after`，`after` 为 null 时取 `before`）的普通 JSON 文档（`fitsync.AppendPlain`，
   现已导出：数字原样，decimal 写成数字，bytes 为 base64，时间为 RFC 3339）；delete 是按 `_id` 的 `delete`；DDL 被确认但不写。
   其他形状交给 `envelope` 变换，把删除的行保留为文档用它的 `deletes: "rewrite"`。备选：在目标中加一个文档形状的选项。
3. **`_id`** 取自键：单个值取其文本，只有一个字段的 map 取该值的文本，多个字段的 map 把各文本用 `:` 连接，每段中的 `\` 和 `:`
   用 `\` 转义；bytes 用 base64，数组和 map 用普通 JSON。没有键的 insert 或 message 由集群生成 `_id`（不幂等，文档已说明）；
   没有键的 update 或 delete 永久失败，超过 512 字节的 `_id` 也永久失败，在发送前检查，因为集群会拒绝整个 bulk。备选：键值的
   JSON，无歧义但不便查找。
4. **索引模板**：`{namespace}`，以及用 `yyyy`、`yy`、`MM`、`dd`、`HH` 和 `.`、`-`、`_` 写的日期，与 Logstash 和 Beats 的写法
   一致，取提交时间（源未给出时取写入时间）在 `time_zone` 中的日期，默认 UTC；`{{` 表示花括号；名称不转小写（被拒绝的名称
   使记录失败）。备选：Go 的布局，或把命名空间转为小写。
5. **`action: "create"`** 把 409 视为已写入，使重放到数据流时收敛，代价是对已有文档的更新丢失；delete 的 404 也视为已写入。
   **`routing`** 指定行中的一个字段（没有 `before` 的 delete 取键中的字段），缺少它的记录失败；**`pipeline`** 放在 bulk 的
   URL 上发送。
6. **永久错误，运行时的新概念。** `fitsync.Permanent(err)` 标记重试也无法解决的错误；运行时把因它失败的记录直接交给
   `on_error`，不经流水线的重试。目标把 429 和 5xx 以外的每种逐条拒绝（mapping 错误、错误的索引名）以及无法生成条目的记录
   都标记为永久错误。此前一个坏文档会让每个批次以退避发送 `retry.max_attempts` 次。备选：所有错误都交给流水线的重试。
7. **一条失败对其他条目意味着什么。** 各条目独立成功。集群以 429 或 5xx 回应的条目，目标在同一次 `Write` 中单独重发，退避
   从 200ms 起倍增到 30s，并带上批次中同一文档（索引、routing 和 `_id`）的后续条目，使文档的最后一次变更仍是最后一次，即使
   其前面的某次被重写。429 只要持续就一直重发（背压）；5xx 重发五次（`DefaultItemAttempts`），然后写入在它处失败。写入失败时，
   `BatchError` 指出第一条未写入的记录：它之前的记录都已写入；它之后集群已接受的记录由运行时重发，运行时从该记录起按顺序再写
   这个批次，每条只确认一次。永久拒绝排在所有待重试条目之前时，写入立即结束。备选：遇到第一个失败即返回、不重发任何条目，
   把所有重试留给流水线。
8. **整个 bulk 的 429** 以同样的退避等待，只要集群如此回应就一直等，占住目标的 goroutine，于是队列填满、源停下，正如计划所
   要求；只有流水线停止才结束它。逐条的 429 也一样。因此一个永远回应 429 的集群会让流水线停滞而不是让记录失败，延迟会显示
   这一点（I189）。备选：把 429 计入 `retry.max_attempts`，那样 `on_error: skip` 会在负载下丢弃记录。
9. **故障转移**：依次使用 `urls`，从上次应答的那个开始；连接出错、超时、502、503 或 504 时换下一个；全部失败时整个写入失败，
   由流水线的重试处理。不做节点嗅探。备选：像官方客户端那样嗅探集群节点。
10. **请求受 `max_request_bytes` 限制**（8 MiB）：更大的批次按顺序分成多个 bulk 发送；超过它的单个文档永久失败。应答用
    `filter_path` 只读条目的状态和错误，上限 64 MiB。
11. **OpenSearch 的差异**：由根端点的 `version.distribution` 识别，在第一次写入时询问（根端点需要凭据，所以失败后会再问）。
    唯一据此处理的差异是 API 密钥，OpenSearch 没有：带 API 密钥时根端点的 401 会说明这一点，distribution 为 `opensearch` 时
    拒绝 API 密钥。Elasticsearch 7 和 8、OpenSearch 1 和 2 接受相同的 `_bulk`。备选：一个 `distribution` 参数。
12. **秘密只来自环境变量**（`password_env`、`api_key_env`），在流水线加载时读取，与 `mask` 变换的 `key_env` 一样；
    `admin.store` 等页面（I190）能写入时再支持。TLS 用 `ca_file`、`cert_file` 和 `key_file`、`server_name`；无法跳过校验。
13. **引擎。** 目标的客户端运行在角色的引擎上（`Options.Engine`）；没有引擎时（`fit sync run`、测试），在第一次写入时创建
    一个自己的 fib 引擎，在 `Cleanup` 时停止，因此 `fit sync run` 可以写入 Elasticsearch，不像 WASM 连接器的 HTTP（I167 的
    决策 11）。备选：没有引擎时拒绝运行。
14. **`ParallelSink`。** 目标可以同时接受多个写入（一个客户端，可被多个 goroutine 安全使用），因此作业给它
    `job.max_parallel` 个写入者；运行时的键哈希让一个文档的变更留在一个写入者上，保持顺序。
15. **互通测试**在 `test/sync`（包 `synctest`），没有 `FIT_SYNC_ES_URL` 和 `FIT_SYNC_OS_URL` 时跳过，设置
    `FIT_SYNC_INTEROP=required` 时改为失败；`test/sync/run.sh` 启动 `docker.elastic.co/elasticsearch/elasticsearch:8.19.3` 和
    `opensearchproject/opensearch:2.19.3`，各为单节点，开启安全（自有 CA 的 TLS、basic 认证），写线程池为一个线程、队列为一，
    使八个写入者的作业得到真实的 429（测试检查集群的拒绝计数增加了）。CI 在每次推送时于作业 `sync-interop` 中运行它。备选：
    GitHub 的服务容器，它无法把节点生成的 CA 交给测试。
16. **规模**：代码约 900 行，测试约 750 行（针对假集群的单元测试、互通测试及其脚本），加上文档超出计划的 1,500 行；没有剩余
    工作，因此没有 I168b。

### I169 Elasticsearch 与 OpenSearch 作为源

- **做什么**：源 `elasticsearch`：对索引模式用 point in time 和基于 `_shard_doc` 的 `search_after` 做快照；之后若 `cursor`
  指定了字段（更新时间、序号），则按该字段和 `_id` 用 `search_after` 轮询；检查点是最后的排序值。轮询看不到删除；文档说明
  这一点，并建议使用软删除字段。
- **完成标准**：一百万个文档的快照完整且有序；轮询能拿到快照期间写入的更新，包括游标值相同的多条；重启后无缺口地继续。
- **涉及**：`sync/elasticsearch`、`test/sync`、`docs`。

**待审查的决策**（实现时所做，2026-11-08；在用户裁定之前一直有效）：

1. **记录是什么。** 源 `elasticsearch` 与目标同包，声明同样的连接参数并共用 `Conn`。记录的命名空间是文档自己的索引
   （`_index`，不是模式），键是 map `{"_id": "<id>"}`：目标的 `DocID` 会把它还原成同一个 `_id`，SQL 目标把它当作名为 `_id`
   的列；`after` 是按 `json` 编解码器的规则读出的 `_source`（`fitsync.ParsePlain`，现已导出）：没有小数部分的数字为 int，超出
   int64 为 uint，否则为 float，超出 float64 为 decimal；日期保持字符串。备选：用 `DocID` 的逆运算把 `_id` 解成键的各字段，
   这会把并非 fit 生成的 `_id` 拆错；或按映射给值定类型。
2. **操作。** 快照记录是 insert（没有 Debezium 的 `r` 那样的 `read` 操作）；轮询到的文档 `_version` 为 1 时是 insert，否则是
   update。日期游标的值是轮询记录的提交时间；快照记录没有。备选：为快照读取加一种新操作或标记。
3. **快照**经 point in time 用 `search_after` 读取：Elasticsearch 按 `_shard_doc`，OpenSearch 按 `_id`，因为 OpenSearch 没有
   `_shard_doc`（"No mapping found for [_shard_doc]"），且在 `/_search/point_in_time` 打开时间点、应答 `pit_id`。没有时间点的
   集群被拒绝（Elasticsearch 7.12 之前、OpenSearch 2.4 之前）；流不回退到 scroll。OpenSearch 拒绝为不匹配任何索引的模式打开
   时间点，源在 `_count` 为 0 时把它当作空。备选：对旧集群用 scroll。
4. **流的快照中断后从头开始。** 快照记录除最后一条外不带位置，最后一条记下轮询从哪里开始（没有游标时为 `done`）：新时间点
   的排序值与旧的不同，且源停止时会关闭它的时间点。`snapshot: "cursor"` 改为按游标读取已有文档，可续读，适合大到不宜重读的
   索引。备选：把时间点的 id 放进位置，重启后试用，但这只在 `keep_alive` 之内、且源没有关闭它时才有效。
5. **轮询不按 `_id` 排序。** Elasticsearch 8 拒绝按 `_id` 排序（"Fielddata access on the _id field is disallowed"），所以轮询
   只按游标排序，取 `gte` 最后值，并排除在该值已读过的 `_id`（`must_not` `ids`），每页都是一次新的搜索。位置是
   `{"cursor": <最后的排序值>, "ids": [...], "past": bool}`，放在每页最后一条记录上；同一值超过 1,000 个 `_id` 时，该值其余的
   文档立即经时间点读完（`past`，之后用 `gt`）。备选：要求开启 `indices.id_field_data.enabled`，或在参数中给一个唯一的决胜字段。
6. **迟到的写入不补读。** 没有重叠窗口：轮询越过检查点之后，游标值写成不高于检查点的文档会被漏掉，文档已说明。对 date 或
   date_nanos 游标，轮询落后集群自己的 `now` `delay`（2s，按整秒）（查询中的日期运算，与 fit 的时钟无关），以覆盖刷新间隔；
   数字和 keyword 没有延迟。备选：重读检查点以下的一个窗口，按 `_id` 和 `_version` 去掉已读的，代价是窗口占用的内存。
7. **轮询从哪里开始**：游标的最大值（在 `delay` 之内），在快照打开时间点之前查询，所以快照期间的改动在快照之后被轮询到；
   该值上的文档会再读一次（幂等的目标会合并）。`snapshot: "none"` 不读快照，直接从那里开始。游标的类型取自
   `_mapping/field`，各索引须是同一类型（date、date_nanos、数字、keyword），在还没有索引映射它时会再次查询；date_nanos 的值以
   带六位小数的 epoch 毫秒发送。
8. **删除**：`deleted_field`，一个顶层字段，其值（不为 null 或 false）把文档变成对其 `_id` 的删除，文档作为 `before`，快照和
   轮询都如此。备选：交给变换，但目前没有变换能由字段生成删除。
9. **作业的分块**：只在没有游标时分块（有游标的作业像流一样读，下一次作业只读改动），按 `_count` 每 `chunk_documents`
   （100,000）个文档一块，最多 1,024 块（`index.max_slices_per_scroll`）。每块是 `_id` 哈希的一个切片，经 scroll 而非时间点读取：
   Elasticsearch 拒绝按 `_id` 切分时间点（单分片索引上报 "cannot slice on [_id] when using [point-in-time]"），而它默认的切片是
   Lucene 文档编号的区间，换一个时间点就不同，续跑的作业会读到别的文档。中断的块从头重读（块内没有位置）；每块是其 scroll
   打开时的样子，而不是整个作业的同一时刻。备选：按游标区间分块，这需要游标的百分位数。
10. **边界。** 每页 `page_size`（1,000，最多 10,000）个文档，应答超过 `max_page_bytes`（8 MiB）时减半，低于其四分之一时再加倍；
    超过客户端 64 MiB 上限的应答以一半数量重新请求（scroll 的下一页不能改大小：它失败，之后的搜索减半）。429 像目标那样等过去；
    超时或有分片失败的应答使本次运行失败，从检查点重新开始，而不是留下缺口。时间点和 scroll 在结束或失败时关闭，各有 10s
    的超时；流最多打开一个，作业最多 `job.max_parallel` 个，`keep_alive` 为 5m。
11. **没有 `query` 参数**：读取索引中的每个文档；不需要的由 `filter` 变换丢弃。备选：在参数中给一个查询，用于快照和轮询。
12. **互通测试**读取 `FIT_SYNC_SOURCE_DOCS` 个文档（默认 100,000，CI 中如此）：批量载入后整体读取的快照，每个一次（OpenSearch 的
    按 `_id` 顺序）；一个流，在其快照期间有 125 个文档被改、5 个被写成同一时间，以每页 50 个轮询，每个读到一次，之后从检查点
    重启，只读到检查点之后写入的四个（其中三个在同一值）；一个作业经目标把 20,000 个文档分七个切片、每次四个复制到另一个
    索引。不留下打开的时间点或 scroll。一百万个文档于 2026-11-08 在本地运行（`FIT_SYNC_SOURCE_DOCS=1000000`，ES 8.19.3 与
    OpenSearch 2.19.3，两个分片，512 MiB 堆），完整读取、每个一次，分别用时 26s 和 31s。
13. **规模**：约 710 行代码和 1,230 行测试（带假集群的单元测试、互通测试），超过计划的 1,500 行；没有剩余工作，所以没有 I169b。

### I170 ClickHouse 作为目标与源

- **做什么**：经 fib 异步客户端使用 HTTP 接口：目标 `clickhouse`，批次以 `JSONEachRow` 插入，`insert_deduplication_token`
  由批次首尾位置生成，使重放的批次被服务端丢弃；delete 与 update 写成 `ReplacingMergeTree` 的行，带版本列和删除标记（或在
  配置时使用轻量 `DELETE`）；源 `clickhouse`，按查询做快照，之后按游标列轮询。
- **完成标准**：在 Docker 中与 ClickHouse 互通；同一批次发送两次只存一份；重放后用 `FINAL` 查询的最终状态正确；源从游标继续。
- **涉及**：`sync/clickhouse`、`test/sync`、`docs`。

**待审查的决策**（实现时所做，2026-11-08；在用户裁定之前一直有效）：

1. **放在哪里。** 包 `sync/clickhouse`（`syncclickhouse`）：目标、源和它们自己的 `Conn`（带故障转移的 URL、basic 认证、TLS、
   `database`、角色的引擎或自己的引擎，与 I168 相同），从 `X-ClickHouse-Exception-Code` 读取 ClickHouse 的错误码。连接参数
   由每个连接器各自平铺声明，与 I168 相同。备选：与 `sync/elasticsearch` 共用一个 HTTP 连接包。
2. **一行是什么。** insert、update 或 message 写成其行（`after`，`after` 为 null 时取 `before`）的普通 JSON
   （`fitsync.AppendPlain`：数字原样，decimal 写成数字，bytes 为 base64，时间为 RFC 3339，以
   `date_time_input_format=best_effort` 读取，JSON 值写成对象，ClickHouse 可读入 `String` 列），再加上 `_version`
   （`version_column`）和 `_deleted`（`deleted_column`，为 0）。`deletes: "flag"` 时 delete 写成删除前的行（`before`），
   没有时写键的字段，`_deleted` 为 1。DDL 被确认但不写。目标从不创建或修改表：文档给出要建的
   `ReplacingMergeTree(_version, _deleted)`。`plain: true` 写不带这两列的行，用于只追加的表。备选：按第一条记录的类型建表。
3. **版本**是提交时间的 Unix 纳秒，或 `version_field` 的值（uint、非负 int，或取纳秒的时间）；只有源没有提交时间时才用写入
   时间。版本必须来自记录而不是时钟：有去重时，重放可能以新令牌再次写入某个键较旧的变更，而它曾写过的后一个批次被当作重复
   丢弃，时钟版本会让较旧的变更胜出。同一个键的两次变更版本相同时，留下最后插入的那次（binlog 的提交时间只到秒），
   `version_field` 就是为此而设。备选：从位置生成版本，但并非每个源的位置都是数字。
4. **去重令牌**是 `fit-` 加上对表、首尾记录的位置和条数做 SHA-256 后的 40 个十六进制数字；没有位置的记录以它之前最近的有
   位置记录（`+n`）或之后的（`-n`）定位，完全没有位置的一段不带令牌发送。一个批次按同一张表的连续记录分段，每段一个
   `INSERT`，受 `max_request_bytes`（8 MiB）限制，各有自己的令牌。令牌对复制表有效，对普通 `MergeTree` 只在设置了
   `non_replicated_deduplication_window` 时有效，文档和 `table` 参数的说明写明了这一点；目标不做检查。备选：对每条记录的
   位置做哈希，但批次切法不同时它也会变。
5. **轻量删除时关闭去重。** `deletes: "delete"` 时每段连续的删除是一条 `DELETE FROM t WHERE (k1 = v1 AND k2 = v2) OR ...`
   （至多 128 KiB，低于 `max_query_size`），在插入之间按顺序执行，插入则带 `insert_deduplicate=0` 发送：重放时再次执行的
   `DELETE` 会删掉之后的插入写入的行，而去重的插入不会把它补回来。这是互通测试发现的（应有 180 行，只有 162 行）。因此
   重放会再次存入行，由 `ReplacingMergeTree` 合并。键用 `=` 与字面量比较（数字原样，字符串加引号，时间经
   `parseDateTime64BestEffort`，日期经 `toDate`，null 用 `IS NULL`），因为字面量类型与列类型不同时 ClickHouse 拒绝元组的
   `IN`。备选：用带类型的查询参数做 `IN`，这需要知道键列的类型。
6. **错误。** 指出某一行（`(at row N)`）的数据错误使该记录永久失败（`fitsync.Permanent`），同一插入中它之前的行以自己的
   令牌单独重发，因此只有它失败，与 I168 的坏文档一样；没有指出行的数据错误（解析、转换或类型错误码）使该段第一条记录
   永久失败。其他错误（未知的表或列、认证、分区片过多、5xx、连接）都由流水线重试，这样在建表期间不会跳过记录。目标不在
   一次写入内重试：ClickHouse 没有逐行的 429。备选：把未知的表或列视为永久错误。
7. **`settings`**，随每个查询发送的映射，用于 `async_insert` 之类；连接器自己使用的名称（`query`、`database`、
   `insert_deduplication_token`、`insert_deduplicate`、`param_*`）被拒绝。备选：不支持设置，或为常用设置各设一个参数。
8. **源按键集分页读取。** 它读 `SELECT * FROM (<表或查询>) WHERE (cursor, key) > (...) ORDER BY cursor, key LIMIT page_rows`，
   边界作为与列同类型的查询参数，其值取 `toString` 的输出，因此任何类型都能精确往返（fit 自己不格式化浮点数或时间）。位置为
   `{"after": [{"t": "<类型>", "v": "<文本>"}, ...]}`，即最后读到的行的排序列。`key` 必须给出，不从 `system.tables` 读取；
   查询须包含键列和游标列，且没有自己的 `ORDER BY`、`LIMIT` 或 `FORMAT`。备选：默认用表的排序键。
9. **行都是 insert**（轮询分不出插入和更新；目标都按 upsert 写），`deleted_column` 为 true 或非零时为 delete，因此目标写出
   的表用 `deleted_column: "_deleted"` 即可读回；从 ClickHouse 中删除的行看不到。`DateTime` 类型的游标作为记录的提交时间。
   值按列类型转换（`ToValue`）：128 和 256 位整数为 decimal，`Map`、`Tuple` 和 `JSON` 为 JSON。备选：快照之后读到的行
   作为 `update`。
10. **迟到的行会漏掉。** 之后写入、游标值不大于已读值的行（两次插入的分区片可见顺序与游标顺序不同，时钟落后）看不到；文档
    说明游标必须增长，如 `DEFAULT now64()`。备选：`poll_lag`，只读早于某个延迟的行。
11. **`snapshot: "never"`** 在没有检查点的流水线启动时查询最大的游标值；在第一条记录写入检查点之前重启会再查一次，漏掉期间
    写入的行。备选：让源能立即保存起始位置。
12. **没有游标时**源按键读一遍就结束，流模式也是如此，这会停止流水线。备选：拒绝没有游标的流。
13. **I167 的 `Chunker`。** 设置 `chunk_rows` 时，作业按键规划分块：先 `count()`，再从上一块的 `lo` 起用
    `ORDER BY key LIMIT 1 OFFSET chunk_rows` 找到每块的 `lo`，每块一次查询，排序至多需要 `chunk_rows` 个键的内存，键是表的
    排序键时按序读取；`lo` 和 `hi` 与位置用同样的边界形式。分块按键分页读取；下一个作业从头重新规划（I167 的决策 6）。没有
    `chunk_rows` 时作业按游标读取，下一个作业从它之后继续。备选：对整个键用 `row_number()` 的一次查询，它会一次排序所有键。
14. **互通。** `test/sync/run.sh` 还会启动 `clickhouse/clickhouse-server:25.8`（`fit-sync-ch`，端口 29202，带密码的用户
    `fit`，走 HTTP），测试（`test/sync/clickhouse_test.go`）读取 `FIT_SYNC_CH_URL`、`FIT_SYNC_CH_USER` 和
    `FIT_SYNC_CH_PASSWORD`，没有时跳过；CI 的 `sync-interop` 作业与其他测试一起运行它们。没有针对 ClickHouse 试 TLS（其 TLS
    需要容器内单独的配置文件）；代码与 I168 相同。备选：一个带 TLS 的容器。
15. **`ParallelSink`**，与 I168 相同：作业给目标 `job.max_parallel` 个写入者，一个键对应一个写入者。
16. **规模**：代码约 1,000 行，测试约 500 行，加上文档在计划的规模之内；没有剩余工作，因此没有 I170b。

### I171 HTTP 作为源与目标

- **做什么**：源 `http`，fit 路由上的处理器 `sync_ingest`：JSON、NDJSON 或编解码器格式的 POST 变成记录，只有在流水线的目标
  确认后才回 200，队列满或等待超过 `timeout` 时回 503 并带 `Retry-After`；路由的中间件（认证、限流）照常生效。目标 `http`：
  以 JSON 或 NDJSON 批量 POST，头部与认证取自配置，5xx 和超时重试，幂等键由批次的位置生成。
- **完成标准**：一个重试直到收到应答的客户端在 fit 重启前后不丢数据；队列满时回 503 且不阻塞 worker；目标的重试带同一个
  幂等键。
- **涉及**：`sync/httpsync`、`handler`、`docs`。

**待审查的决策**（实现时所做，2026-11-08；在用户裁定之前一直有效）：

1. **放在哪里，处理器如何找到源。** 源和目标在包 `sync/httpsync`（`httpsync`）；处理器 `sync_ingest` 在包
   `handler/syncingest`。处理器指定流水线（`pipeline`），通过进程内的注册表（`httpsync.Lookup`）找到它的源，源在其 `Run`
   运行期间登记在那里；为此 `fitsync.Run` 增加了 `Pipeline`，即流水线的名字。没有源登记时（流水线不在配置中、失败后正在重启、
   已停止），处理器回 503。备选：源自己的名字（`endpoint`）由处理器指定，或把处理器放进 `httpsync`。
2. **在水位线处应答，而不是在检查点处。** 新的可选接口 `fitsync.Acknowledger`（`Acknowledged(pos)`）在水位线移动时、在运行
   的锁之外被告知其新位置；源对到该位置为止的每个请求回 200。`Confirmer` 要等检查点保存后才应答，最多晚
   `checkpoint_interval`，而 HTTP 源没有可以续传的东西：所有目标都拿到一个请求的记录时，它们就安全了。备选：`Confirmer`。
3. **一个请求是一个事务。** 记录多于一条时带 `Tx.ID` `http-<run>-<n>` 及 `First`、`Last`，最后一条记录带位置
   `{"run": "<随机>", "request": <n>}`，因此水位线不会停在请求中间，SQL 目标可以用自己的一个事务写入它。这个位置像其他位置
   一样保存在检查点中，重启时被忽略。记录的 `source` 为 `http`；没有命名空间时用 `namespace`（`http`）；没有提交时间时用请求
   到达的时间。
4. **`on_error` 处理掉的记录算作已确认。** 被变换或目标判为失败、被跳过或送入 `dead_letter` 的记录，其请求仍回 200：失败的
   记录如何处理由流水线的策略决定，对所有源都一样。`on_error: stop` 时流水线停止，等待中的请求在它停止时（模块的 `Cleanup`）
   或在各自的 `timeout` 时回 503。备选：对这样的请求回 422 或 500。
5. **限额与背压。** 处理器在 worker 上随 `OnBody` 读取请求体，存入受其 `max_body_bytes`（8 MiB，至多 1 GiB；超过回 413）
   限制的缓冲区，不等待地交给源（`Offer`）；源在流水线的 goroutine 上解码，因此 WASM 编解码器从不在 worker 上运行。源限制它
   持有的东西：`max_pending_requests`（1024）从 `Offer` 起计数一个请求，直到它最终得到应答，处理器已因超时回了 503 的请求在
   其记录被确认前仍然计数；`max_pending_bytes`（64 MiB）计数尚未解码的请求体（没有等待者时总能放下一个）。队列满时源停在
   `Emit`，它的待处理请求被填满，下一个请求立即回 503。备选：只用流水线的 `queue`，再由运行时增加一个不阻塞的 `Emit`。
6. **请求体的格式。** 没有 `codec` 时，请求体是一串 JSON 值，每个是对象或对象数组，因此一个解析器同时读 JSON（一个对象或
   一个数组）和 NDJSON；`Content-Type` 为 `application/json`、`application/x-ndjson`、`application/ndjson`、
   `application/jsonl`、`application/x-jsonlines`、`+json` 类型、`text/plain` 或没有，其他回 415。`form` 与 `json` 编解码器
   的相同（`plain` 行或完整的 `record`）。有 `codec` 时接受任意 `Content-Type`，`framing` 为 `lines`（每一行，跳过空行，去掉
   `\r`）或 `body`。请求体在发出任何记录之前完整解码：一个坏值回 400，什么都不写。备选：每种 `Content-Type` 严格对应一种格式。
7. **至少一次，源不去重。** 因超时回了 503 的请求仍可能被写入，客户端重发就会写两次；记录有键时（`form: "record"` 或 `key`
   变换）幂等的目标会合并它们。源不保存客户端的 `Idempotency-Key`。备选：保存一段时间内见过的键，对重复的请求回 200 而不写入。
8. **应答**：JSON，200 时为 `{"records": n}`，其他为 `{"records": 0, "error": "..."}`；其他方法回 405 并带 `Allow: POST`；
   `Retry-After` 取自 `retry_after`（1s），以整秒计，至少为 1；`Cache-Control: no-store`。空请求体立即回 200，没有记录。
9. **校验时不做交叉检查。** `fit validate` 不拒绝指定了不存在的流水线、或其源不是 `http` 的 `sync_ingest`：模块的
   `Provision` 看不到配置的 `sync`。运行时回 503。备选：在配置校验中检查。
10. **重启与停止。** 源失败后重启的流水线对其上一次运行发出但未被确认的每个请求回 503（那次运行的记录随之丢弃）；因重载或
    fit 停止而停止的流水线不再接收请求（尚未解码的回 503），让目标写完手中的记录，从而对它们的请求回 200，其余回 503。在作业
    （`mode: "once"`）中，这个源永不结束。
11. **目标的重试就是流水线的重试。** 5xx、408、429、超时或连接错误使写入失败，流水线的 `retry` 从失败的请求起重发（之前的
    请求通过 `BatchError` 确认）；其他非 2xx 的应答，包括 3xx（fib 的客户端不跟随重定向），都是 `Permanent`，立即交给
    `on_error`。429 不像 `elasticsearch` 目标那样一直等待，也不读 `Retry-After`：它计入 `retry.max_attempts`。备选：像 I168 的
    决策 8 那样等待 429 过去，并遵循 `Retry-After`。
12. **幂等键**是对每条记录的位置（先写长度）和请求体做 SHA-256 后的 32 位十六进制数字（128 位），放在 `idempotency_header`
    （`Idempotency-Key`）中。只用位置区分不了批次，因为大多数记录没有位置（文件源每一千条放一个，HTTP 源放在请求的最后一条）；
    只用请求体又会把不同位置上相同内容的两个批次当成一个。重发的请求记录和请求体相同，因此键相同；超过 `max_request_bytes`
    的批次从失败的记录起会以同样方式再拆分，因此失败的请求带着原来的键回来。崩溃后重放时批次分法不同，就得到不同的键：接收方
    仍然需要幂等的写入。备选：像 I170 的去重令牌那样只用首尾位置。
13. **目标的形态。** `format` 为 `ndjson`（默认）或 `json`（数组），`form` 为 `plain` 或 `record`，或用 `codec` 加
    `content_type`；`method` 为 `POST` 或 `PUT`；`headers` 不能设置 `Authorization`、`Content-Type`、`Content-Length`、`Host`
    或幂等键头；basic 认证（`username`、`password_env`）或 bearer 令牌（`bearer_token_env`），与 I168 一样只取自环境变量；
    TLS 用 `ca_file`、`cert_file`、`key_file`、`server_name`，不可跳过校验；一个 `url`，没有故障转移；应答最多读 1 MiB，错误
    中带其前 300 字节；使用角色的引擎，或自己的引擎（`fit sync run`）；是 `ParallelSink`。备选：像 `elasticsearch` 目标那样
    支持多个 URL 和故障转移。
14. **测试。** 重启测试在测试进程中提供路由并运行同步，在八个客户端 POST 400 条记录（每条都重发直到收到 200）时停止监听器、
    配置和同步运行时，再在另一个地址上启动它们：每条记录都在目标中。`memory` 目标的存储在进程中比运行时活得久，代替持久的
    目标。队列满测试卡住目标，第二个请求立即收到带 `Retry-After` 的 503，同时另一个路由的二十个请求都得到应答。目标的测试让
    一个请求先遇到 503 再遇到超时，三次发送带同一个键。测试在 macOS 上于竞态检测器下运行；按用户为这一轮设定的范围，没有在
    Linux 上运行，也没有运行 `./sync/...` 和 `./handler/...` 的全面测试，它们由合并时运行。
15. **规模**：代码约 1,000 行（含运行时的钩子），测试约 940 行；没有剩余工作，因此没有 I171b。

### I172 MySQL 客户端：连接、认证与查询

- **做什么**：包 `sync/mysql/wire`：握手、TLS、`mysql_native_password` 和 `caching_sha2_password`（快速路径，以及经 TLS 或
  服务端 RSA 公钥的完整认证）、文本查询与结果集、二进制协议的预处理语句、带参数的多行插入、事务、每次调用的超时；支持
  MySQL 5.7、8.0、8.4，MariaDB 10.11 与 11。
- **完成标准**：在 Docker 中与每个版本互通：每种列类型经预处理语句往返一致；每种认证插件都能认证；服务端在结果中途关闭
  连接时返回错误，而不是挂起。
- **涉及**：`sync/mysql/wire`、`test/sync`。

**待审查的决策**（实现时所做，2026-11-08；在用户裁定之前一直有效）：

1. **放在哪里。** 包 `sync/mysql/wire`，包名 `mysqlwire`：一个库，没有自己的模块、参数或 schema；I174–I176 声明连接器的
   参数，再交给 `mysqlwire.Config`。备选：包名 `wire`，会与 I177 的 PostgreSQL 包同名。
2. **在 goroutine 上的阻塞连接，而不是 fib 的连接。** `Conn` 是一个 `net.Conn`（TLS 用 `crypto/tls`），同一时刻只由一个
   goroutine 使用，正如决定 036 所说的数据库连接：少、长，各在自己的 goroutine 上，不占 fib 的 worker。MySQL 协议是请求和
   应答、按顺序流式返回的结果集，之后（I174）是按顺序读的 binlog；用 fib 的异步连接也要在 fib 的事件循环上重组包，再交给
   流水线的 goroutine，并另做背压。备选：在 fib 的事件循环上解析，经 channel 交出。
3. **超时。** `Config.Timeout`（60s）限制每次等待服务端的时间：拨号以及一次调用中的每次读和写，而不是整个调用，因此
   持续读取的长结果不会被截断；调用的 context 限制整个调用（截止时间与取消）。被二者之一结束的调用会关闭连接，流的任何
   错误也是如此，因为之后无法分辨服务端发来的是什么；服务端自己的错误（`*Error`）不影响连接。被取消的查询不会经第二个
   连接 `KILL QUERY`：服务端发现连接断开后自行停止。备选：用第二个连接杀掉查询。
4. **TLS。** 设置了 `Config.TLS` 就必须用 TLS，没有“preferred”：不提供 TLS 的服务端被拒绝；除非配置给出名字，校验的是
   地址中的主机名。MySQL 5.7 官方镜像只提供 RSA 与 DHE 密钥交换，Go 只有 RSA 的，且自 Go 1.22 起默认关闭：连 5.7 的客户端
   须在 `tls.Config` 中列出 `TLS_RSA_*` 套件，互通测试就是这样做的；是否为此提供参数由连接器（I174）决定。备选：由本包对
   所有服务端打开 RSA 套件。
5. **密码插件**：`mysql_native_password`；`caching_sha2_password` 的快速路径与完整认证，经 TLS 或 Unix socket 明文发送，
   否则用 `ServerPublicKey` 加密，再否则在设置了 `AllowPublicKeyRetrieval` 时向服务端索取公钥（默认关闭：中间人可以换成自己
   的公钥）；`sha256_password` 同理；`mysql_clear_password` 只经 TLS 或 Unix socket，或设置 `AllowCleartextPassword`。握手中
   的插件客户端没有时，以 `mysql_native_password` 应答，由服务端切换到用户的插件。不支持：MariaDB 的 `client_ed25519` 和
   `parsec`、GSSAPI 以及 4.1 之前的口令散列，各自报错并给出名字。备选：`client_ed25519`，需要在 fit 中实现 MariaDB 自己的
   Ed25519 变体。
6. **值按列的类型给出**，文本协议与二进制协议一致：整数与 `YEAR` 为 `int64` 或 `uint64`（按 `UNSIGNED`），`FLOAT` 为
   `float32`，`DOUBLE` 为 `float64`，`DATE`、`DATETIME`、`TIMESTAMP` 为 `mysqlwire.DateTime`（没有时区；保留零日期和为零的
   部分，`time.Time` 表示不了），`TIME` 为 `time.Duration`，NULL 为 `nil`，其余为 `[]byte`（字符串、`DECIMAL` 的文本、`BIT`、
   `ENUM`、`SET`、`JSON`、`GEOMETRY`），含义由列的类型与排序规则给出。一行中的 `[]byte` 在下一次 `Next` 之前指向连接的缓冲区，
   因此读快照时不必逐值复制。转换为 `fitsync.Value` 是 I174 与 I175 的事。备选：直接从协议得到 `fitsync.Value`。
7. **参数**是 Go 的值：`nil`、`bool`、各种整数（作为有符号或无符号的 `BIGINT`）、`float32`、`float64`、`string`、`[]byte`
   与 `json.RawMessage`（都作为 `MYSQL_TYPE_STRING`，因此每种服务端的 JSON 列都能接收）、`time.Time`（`Config.Location` 中的
   `DATETIME`，默认 UTC）、`DateTime` 与 `time.Duration`；其他类型在发送前即被拒绝。
8. **带参数的 `Exec` 与 `Query` 每次都预处理、执行并关闭**语句，不缓存语句；不带参数时发送文本。要多次执行时用 `Prepare`
   得到的语句。备选：语句缓存，或在客户端拼接参数。
9. **多行插入**（`InsertRows`）：`head`、每行一个 `(?,...)` 与 `tail`（`ON DUPLICATE KEY UPDATE ...`），在 65,535 个占位符
   和服务端的 `max_allowed_packet`（每个连接查询一次，留 1 KiB 余量）允许的范围内用尽量少的语句；一次调用中每种行数的语句
   只预处理一次，调用结束时关闭。这些语句不是一个整体，除非在事务中执行（`Begin`、`Commit`、`Rollback`，以及取自服务端
   状态的 `InTransaction`）。
10. **一次一个调用。** 下一个调用之前必须读完或关闭结果的行（否则 `ErrBusy`）；`Rows.Close` 读完剩下的部分，而不是断开
    连接。多个结果集（存储过程的）用 `NextResultSet` 遍历。
11. **上限**：一次读的包（连同它的行）至多 `MaxPacketSize`（64 MiB），超过 1 MiB 的缓冲区用后释放，至多 4,096 列。
12. **没有做的**：压缩、`LOAD DATA LOCAL`（以空文件应答并拒绝）、`CLIENT_MULTI_STATEMENTS`、`COM_STMT_SEND_LONG_DATA`
    （一个值必须放得进 `max_allowed_packet`）、游标、连接属性与会话跟踪。副本的命令（`COM_REGISTER_SLAVE`、
    `COM_BINLOG_DUMP_GTID`）以及为它们准备的原始包接口归 I174。
13. **互通。** `test/sync/mysql.sh` 由 `test/sync/run.sh` 引入，也可单独运行（`-run MySQL`），启动 `mysql:5.7`（以
    `linux/amd64` 运行）、`mysql:8.0`、`mysql:8.4`（带 `--mysql-native-password=ON`）、`mariadb:10.11` 与 `mariadb:11`
    （`fit-sync-my-*`，端口 29210–29214），各自使用由 `openssl` 生成的 CA 签发的 TLS 证书、root 密码以及 64 MiB 的
    `max_allowed_packet`；测试（`test/sync/mysql_test.go`）读取 `FIT_SYNC_MYSQL`（`name=host:port,...`）、
    `FIT_SYNC_MYSQL_PASSWORD` 与 `FIT_SYNC_MYSQL_CA`，没有时跳过。CI 作业的名字仍只列出 MySQL 之前的系统；没有改动
    `.github`。备选：为 MySQL 单设一个作业。
14. **规模**：代码约 1,630 行，针对进程内假服务端的单元测试约 1,890 行（不用容器时语句覆盖率 96.8%），互通测试约 700 行；
    没有剩余工作，因此没有 I172b。

### I173 MySQL binlog：事件与值

- **做什么**：解码 binlog：format description、rotate、GTID（MySQL 与 MariaDB 两种）、query、带可选元数据的 table map、
  `WRITE`/`UPDATE`/`DELETE_ROWS` v1 与 v2、JSON 部分更新、XID、heartbeat；校验 CRC32；每种列类型到记录的值（decimal、带
  小数秒的时间类型、`BIT`、`SET`、`ENUM`、作为字节的 `GEOMETRY`、JSON 的二进制形式）。
- **完成标准**：testdata 中从每个服务端版本采集的 binlog 解码出测试写入的行；对解码器的模糊测试既不 panic，也不超出其分配上限。
- **涉及**：`sync/mysql/binlog`。

**待审查的决策**（实现时所做，2026-11-09；在用户裁定前有效）：

1. **位置。** 包 `sync/mysql/binlog`（`binlog`），与 `mysqlwire` 一样是库：`Decoder` 每次接收一个完整事件（头、体与校验和），
   保存 format description 与各 table map；其上有读取 binlog 文件的 `Reader`。副本的命令（`COM_REGISTER_SLAVE`、
   `COM_BINLOG_DUMP_GTID`）仍归 I174。备选：由解码器自己读流。
2. **值是 `fitsync.Value`**，按计划所写，而不是 `mysqlwire` 的类型：整数为 `Int`，无符号列为 `Uint`（`YEAR` 为 `Int`）；
   `FLOAT` 取最短十进制表示的 `Float`；`DECIMAL` 为 `Decimal`；`DATE` 为 `Date`；`DATETIME` 为 `Decoder.Location`（默认 UTC）
   中的 `Time`；`TIMESTAMP` 为 UTC 的 `Time`；零日期或含零部分的日期为 MySQL 写法的 `String`；`TIME` 为 `String`
   （`[-]HH:MM:SS[.小数]`，fitsync 没有时长类型）；`BIT` 为 `Uint`；`ENUM` 与 `SET` 在名字已知时为名字的 `String`，否则为
   序号或位的 `Uint`；字符串为列字符集下其字节的 `String`（不转换），二进制串（字符集 63）为 `Bytes`，字符集未知时
   `CHAR`/`VARCHAR` 为 `String`、`BLOB`/`TEXT` 为 `Bytes`；JSON（MySQL 的二进制形式）为 MySQL 打印形式的 `JSON` 文本；
   `GEOMETRY` 与 `VECTOR` 为 `Bytes`。备选：`mysqlwire` 的类型，由 I174 映射。
3. **由源补全 table map。** `TableMap` 的 `Columns` 是导出的；可选元数据缺少的部分（5.7、MariaDB 默认的 `NO_LOG`、MySQL 的
   `MINIMAL` 没有列名）由 I174 在解码其后的 rows 事件前从 `information_schema` 补上（名字、`Unsigned`、`Charset`、
   `EnumValues`、`SetValues`），在 `Decoder.OnTableMap` 中完成：每个 table map 解码后、其后的 rows 之前调用（transaction payload 内的
   也一样，其 rows 在同一次调用中解码）；`TableMap.Metadata` 说明服务端发送了哪些。测试按 workload 的描述做同样的事。观察到并遵循：
   MySQL 在符号位图中把 `YEAR` 算作数值列，MariaDB 在字符集元数据中把 `GEOMETRY` 算作字符列。
4. **校验和。** format description 指明时校验每个事件的 CRC32，description 自身也校验；`SetChecksum` 用于副本在其之前收到的
   事件（人造的 rotate）。
5. **JSON 部分更新**（`binlog_row_value_options=PARTIAL_JSON`）会被应用：后值是前值应用差异（替换、插入、删除）后的结果，
   目标总能拿到整个文档；没有前值（`binlog_row_image` 不是 `FULL`）时拒绝该事件（`ErrUnsupported`）。备选：把差异传下去。
6. **MySQL 8.3 的带标签 GTID 与 8.0.26 的 heartbeat v2** 按 MySQL 的序列化格式解码（版本、大小、最后必需字段，然后是字段 ID
   与变长值；UUID 为 16 个整数；GNO 与逻辑时钟为 zigzag 有符号），依据 MySQL 源码推出，并以 8.4 中带标签事务的 binlog 核对；
   带标签的 previous-GTIDs 格式也是如此。
7. **压缩。** 解码 MariaDB 压缩的 query 与 rows 事件（zlib，标准库），以及 MySQL 的 `Transaction_payload`
   （`binlog_transaction_compression`，ZSTD 经由 fit 已为 Kafka 依赖的 `github.com/klauspost/compress/zstd`）；payload 中的
   事件放在 `Payload.Events`，解压后大小以 `MaxEventSize` 为上限（随流实际给出的数据读取，解码器的窗口与内存同样受限）。互通
   workload 在 MySQL 8.0 上为一个事务开启压缩。备选：拒绝 payload，由 I174 要求 `binlog_transaction_compression=OFF`。
8. **不进一步解码**（`*Unknown`，即事件体字节）：`Intvar`、`Rand`、`User_var`、`Stop`、`Incident`、`Ignorable`、`XA_prepare`、
   view change、v0 rows 事件。拒绝：5.0 之前的 `DECIMAL`、MariaDB 10.3 之前的时间格式（`mysql56_temporal_format=OFF`），以及
   加密的 MariaDB 文件（`Start_encryption`；副本收到的流已解密）。
9. **上限**：事件最多 `MaxEventSize`（64 MiB）；一个 rows 事件的值最多 `MaxRowsSize`（64 MiB，事件两倍更大时取之），按
   fitsync 的方式估算（每个值 32 字节加其文本）；4,096 列；`MaxTables`（4,096）个 table map，超过即全部忘掉（每个事务自带其
   map）；JSON 深 100 层，文本最多为其字节的 16 倍（偏移可能共享一个值）；不占字节的行被拒绝。`Reader` 的缓冲与解压后的事件
   随实际读到的字节增长，从不按头声称的长度分配。模糊测试 `FuzzReader` 检查不 panic 且分配不超过输入的 2,048 倍加 4 MiB；
   `TestMutations` 把每个采集到的事件逐字节改动、在每个长度截断后解码。
10. **互通与 testdata。** `test/sync/mysql.sh` 现在启动的每个服务端都开启行格式 binlog、MySQL 开启 GTID、关闭
    `secure_file_priv` 并设 `UMASK=0644`（MariaDB 与 5.7 的 `LOAD_FILE` 只读所有人可读的文件）；`TestMySQLBinlogInterop`
    （在 `sync/mysql/binlog` 中，`mysql.sh` 与 `run.sh` 现在也运行该包）在单独的 binlog 文件中执行 `testdata/workload.json`，
    用 `LOAD_FILE` 读出文件并检查解码得到 workload 的行（只看它自己的库：`go test` 同时运行写同一批服务端的
    `./test/sync`）；设置 `FIT_SYNC_MYSQL_BINLOG_TESTDATA` 时写出文件，`testdata/*.binlog`
    即如此采集（8.4 与 MariaDB 11 用 `binlog_row_metadata=FULL`，由测试设置并恢复；8.0 为 `MINIMAL`；5.7 与 MariaDB 10.11
    没有）。`TestCapturedBinlogs` 无需容器即可检查它们。备选：副本协议，归 I174。
11. **规模**：代码约 2,870 行（含注释），单元测试约 1,710 行（语句覆盖 99.6%，无容器），互通与 workload 约 260 行；没有剩余
    工作，因此没有 I173b。

### I174 MySQL 与 MariaDB 作为源

- **做什么**：源 `mysql`：以其 `server_id` 作为副本连接，检查 `binlog_format=ROW` 和 `binlog_row_image=FULL`，从检查点的
  GTID 集合（或文件与偏移）开始，否则从当前开始；服务端支持时列名取自 `binlog_row_metadata=FULL`，否则在启动时以及流中每个
  DDL 之后读取 `information_schema`；`tables` 按通配包含与排除；每个 XID 处一个检查点；DDL 作为 `ddl` 记录；用 heartbeat
  计算延迟；重连后从最后投递的事务继续。
- **完成标准**：与每个版本互通：多表的 insert、update 和 delete，流中途的 DDL，服务端重启，以及 binlog 被清除（报错说明需要
  重新快照，而不是沉默）；在随机位置 `kill -9` fit 不丢任何事务。
- **涉及**：`sync/mysql`、`test/sync`、`docs`。

**待评审的决定**（实现时做出，2026-11-09；在用户裁定前有效）：

1. **放在哪里。** 源 `mysql`（`sync.sources.mysql`）放在包 `sync/mysql` 中，与 I176 的目标并列，文件为 `source*.go`；它采用
   目标的连接参数，但没有 `database`（经由同一个 `wireConfig`，所以 `tls_rsa` 在这里同样服务 MySQL 5.7），另有 `server_id`
   （必填，不能为 0）、`tables` 和 `exclude`（`db.table` 通配；不设置时为除 `mysql`、`sys`、`information_schema`、
   `performance_schema` 之外的所有表）、`keys` 和 `heartbeat`。
2. **副本命令放进 `mysqlwire`**，只做增加：`Conn.Command`（以 OK 应答的命令，如 `COM_REGISTER_SLAVE`）、`Conn.StartBinlog`
   和 `Conn.ReadEvent`（binlog 包中的事件；非阻塞 dump 结束时为 `io.EOF`；服务器的 `*Error`），命令常量导出。进入流式读取的
   连接只属于该流：其他调用返回 `ErrBusy`，`Close` 不发送 `COM_QUIT`。
3. **两条连接**：一条读流；另一条在首次需要时建立，在第一条读流期间用于 `information_schema` 和 `SHOW BINARY LOGS`。两者都在
   流水线自己的 goroutine 上。
4. **服务器有 GTID 时按 GTID 续读。** MySQL 且 `gtid_mode = ON`：用 `COM_BINLOG_DUMP_GTID`，带 `BINLOG_THROUGH_GTID` 和检查点
   的集合；MariaDB（总是有）：`@slave_connect_state`（关闭严格模式）加 `COM_BINLOG_DUMP`；否则按文件与偏移。位置总是同时
   保存两者，`{"gtids", "file", "pos"}`，GTID 集合采用服务器的写法（MariaDB 为 `gtid_binlog_pos` 形式，每个域的最后一个
   GTID）。含 MySQL 8.3 带标签 GTID 的集合按文件与偏移续读：dump 命令中它们的编码尚未实现。备选：按 MySQL 源码实现带标签
   GTID 的编码并在 8.4 上验证。
5. **从当前开始，第一条记录之前没有检查点。** 没有检查点时，流从服务器当前的 binlog 位置开始（8.2 起用 `SHOW BINARY LOG
   STATUS`，否则 `SHOW MASTER STATUS`，MariaDB 另读 `gtid_binlog_pos`）；与 I182 的决定 11 相同，不读的表的事务不移动检查点。
   因此一条只读冷门表的流水线重启后会重读服务器自其最后一条记录以来写的 binlog，若服务器已清除这些 binlog，它会要求重新
   快照，尽管它的表没有丢任何数据。备选：一条携带位置但没有行的记录（记录模型没有这种记录），或由源在检查点之外保存位置。
6. **列名取自 binlog，否则取自 `information_schema`。** 带列名的表映射（`binlog_row_metadata = FULL`）直接使用，主键也随之
   给出；否则每张表读取一次列（名称、`unsigned`、是否二进制、`ENUM` 和 `SET` 的值）和主键（`STATISTICS`），保留到流中下一个
   DDL，届时全部清除。表变更前写下的事件按表的当前结构命名：列数不同时，超出较短一方的列按位置命名为 `@n`，并记录警告。
   列的字符集只区分二进制与否（字符串保留原字节，I173 的决定 2）。备选：像 Debezium 那样根据 DDL 重放维护 schema 历史。
7. **键与键的变化。** 键是主键，或 `keys` 为该表指定的列；两者都没有的表，其记录没有键，按键写入的目标会拒绝它们（快照时，
   I175 直接拒绝这样的表）。键改变的 update 变成旧键的 delete 加新行的 insert，同在其事务中，与 I178 的决定 5 相同。备选：
   在 `before` 中带旧键的 update，`mysql` 目标能处理，其他目标不能。
8. **事务。** 记录的 `Tx.ID` 是 GTID（`uuid:gno`、`uuid:tag:gno`、MariaDB 的 `domain-server-sequence`），没有 GTID 时为事务
   第一个事件的 `file:offset`；源扣住一条记录以便标出最后一条，只有最后一条携带位置，即其 `XID`（或 `COMMIT`）之后的文件与
   偏移；压缩在 payload 中的事务照常读取。DDL（`CREATE`、`ALTER`、`DROP`、`RENAME`、`TRUNCATE`）自成一个事务：能从语句看出
   表时（库名缺省为语句的默认库）是 `db.table` 的 `ddl` 记录，否则在源读取该库时是 `db` 的记录，`after` 为 `statement` 和
   `database`；其他语句（`GRANT`、`FLUSH` 等）跳过。
9. **宁可停下也不丢。** 每次连接（以及**测试连接**）都检查 `log_bin`、`binlog_format = ROW` 和 `binlog_row_image = FULL`；
   缺列的 rows 事件（某会话的 `binlog_row_image`）和所读库中基于语句的变更（某会话的 `binlog_format`）是错误，流水线会从检查点
   重启，直到人工修正。备选：跳过此类事件并记录警告。
10. **流中断后从原处重读。** 连接的错误（不是服务器的）最多连续重连五次（100 ms 起翻倍），从最后交出的事务之后继续；在事务
    中间中断的，整个事务重读，已交出的记录按计数跳过，因为 binlog 内容相同。服务器的错误交给流水线：1236（服务器已没有该
    位置，binlog 已清除）报错说明需要重新快照。
11. **以字节计的延迟**（I189 的 `Run.ReportLag`）：事务之外收到心跳时为 0（服务器无可发送），并在事务结束时至多每 10 秒一次
    报告服务器 binlog 中超出当前位置的字节数（在第二条连接上 `SHOW BINARY LOGS`：当前文件的剩余部分加之后的文件；被拒绝时
    不报告，该语句需要 `REPLICATION CLIENT`）。读取长事务期间不计算。备选：落后的 GTID 数，在没有 GTID 的服务器和 MariaDB 的
    多个域下并不均匀。
12. **心跳**：会话把 `@master_heartbeat_period` 和 `@source_heartbeat_period`（8.0.26 改名；两个名字都设，不认识的无害）设为
    `heartbeat`，即 10 s，它必须小于 `timeout`，因为每次读最多等那么久；校验和同样设置 `@master_binlog_checksum` 和
    `@source_binlog_checksum`；MariaDB 设 `@mariadb_slave_capability = 4` 以得到其 GTID 事件。
13. **作业**（`mode: "once"`）请求非阻塞 dump：服务器在 binlog 末尾结束流，源随之停止（I175 让作业变成快照）。
14. **互通**（`test/sync/mysql_source_test.go`，基于 `test/sync/mysql.sh` 的五个服务器）：一个事务中跨两张表的变更、一次
    update、一次键改变、流中途的 DDL 及其后的行、delete，逐条比较记录；从检查点重启；在检查点下真实清除 binlog（报错要求
    快照）；连接测试；以及一条经 I176 目标的 MySQL 到 MySQL 流水线，在混合写入负载下于随机位置被杀（`Options.Crash`），并在
    设置 `FIT_SYNC_MYSQL_PREFIX`（`mysql.sh` 现已导出）时用 `docker restart` 在其运行中重启服务器，最终校验和相同。清除 binlog
    时取一个锁文件（`$TMPDIR/fit-sync-mysql-binlog.lock`），`TestMySQLBinlogInterop` 现在也取它，因为 `go test` 会在同一组
    服务器上与 `./test/sync` 并行运行那个包，而它读取自己的 binlog 文件。
15. **规模**：约 1,430 行代码（其中 `mysqlwire` 74 行），1,240 行基于伪连接和构造事件的单元测试（不用容器时包覆盖率 96.5%），
    420 行互通测试；没有遗留，不需要 I174b。

### I175 MySQL 快照及其到 binlog 的交接

- **做什么**：`snapshot: "initial"`（没有检查点时）、`"always"` 或 `"never"`：先读 binlog 位置（`SHOW BINARY LOG STATUS` 或
  `SHOW MASTER STATUS`，以及已执行的 GTID），然后在带一致性快照的 `REPEATABLE READ` 事务中按主键分块读取每张表，不加全局锁；
  之后从先前读到的位置开始跟随流，快照期间的变更会再次应用，在幂等目标中收敛；块的进度记在检查点中；没有主键的表除非指定
  了键，否则拒绝。
- **完成标准**：对快照期间仍在写入的表做快照，结束时目标与源相同（对两边做校验和检查）；中断的快照从下一个块继续。
- **涉及**：`sync/mysql`、`test/sync`。

**待评审的决定**（实现时做出，2026-11-09；在用户裁定前有效）：

1. **参数。** `snapshot`：`initial`（默认，与计划草图和 `mongo` 源一致）、`always` 或 `never`；`batch_size`，每次查询读取的
   行数（1,000，至多 100,000）；`chunk_rows`，作业的块大小（100,000）。
2. **不加锁的交接。** 先在快照连接上读取 binlog 位置（8.2 起 `SHOW BINARY LOG STATUS`，否则 `SHOW MASTER STATUS`，以及 MySQL
   已执行的 GTID 或 MariaDB 的 `gtid_binlog_pos`），再读表；之后流从该位置开始，读表期间的变更会再次到来，在幂等目标中收敛。
   不用 `FLUSH TABLES WITH READ LOCK`，也不锁表。
3. **每页一个事务。** 每次读取 `batch_size` 行的查询都在各自的 `START TRANSACTION WITH CONSISTENT SNAPSHOT`（`REPEATABLE READ`）
   中执行，而不是整个快照一个事务：长事务会在繁忙的服务器上阻碍旧行版本的清理，而交接并不需要单一视图，因为流会重放该位置
   之后的一切。备选：每张表一个事务，或整个快照一个事务（单一视图，代价如上）。
4. **按键分页。** 按 `db.table` 名称顺序读表，每张表按键读取：`WHERE (k1, k2) > (?, ?) ORDER BY k1, k2 LIMIT n`，上次读到的键作为
   参数（时间以 UTC 文本传入：会话的 `time_zone` 为 `+00:00`）。
5. **同一行，无论由谁读出，都是同一条记录。** 客户端的值按 binlog 的方式定型（`binlog.Column`）：整数（无符号为 `Uint`，`YEAR`
   为 `Int`），`FLOAT` 取最短十进制表示，decimal，日期，`DATETIME` 为 UTC，`TIMESTAMP` 为时刻，零日期和 `TIME` 为服务器的文本
   （按列的小数位数），`BIT` 为 `Uint`，`JSON`，二进制串和 `GEOMETRY` 为字节，其余为文本；因此一行的快照记录与流记录相同。
6. **每条记录都带进度。** 快照记录没有事务，携带流的起点及 `{"snapshot": {"table": "db.table", "key": <键的映射的规范 JSON>}}`；
   快照的最后一条记录只携带起点，所以在它之后重启会读流。中断的快照从最后保存的键之后继续，比块更细。没有行的快照什么也不
   保存，重启时会重新读取（与 I182 的决定 11 相同）。
7. **`always`** 在源每次启动时都读快照，包括出错重启之后，除非检查点处于快照之中，此时继续该快照；`never` 从检查点或当前开始。
   备选：`always` 只在 fit 启动或重载时生效。
8. **键。** 没有主键的表会被拒绝，除非 `keys` 指定了其键列：报错并指明该表，于是流水线反复重启，直到 `keys` 或 `tables` 改变。
   `keys` 也可以选用某个唯一键代替主键。
9. **作业。** `Chunks` 把每张表按键分成范围，每块 `chunk_rows` 行，边界是沿键用 `LIMIT 1 OFFSET chunk_rows` 找到的键（扫描索引，
   精确，不抽样）；最后一块的行数用 `information_schema.TABLES.TABLE_ROWS`（估计值）估算；每块在自己的连接上读取。`snapshot:
   "never"` 时作业不分块，读 binlog 到末尾（I174）。备选：像 `mongo` 源那样由抽样得到边界。
10. **互通**（`test/sync/mysql_source_test.go`，五个服务器）：一张 3,000 行的表，在以每页 50 行读取其快照期间被混合负载写入，
    经目标 `mysql` 复制，流水线在随机位置（包括快照中）被杀并重启，最终与原表校验和相同；一个 2,000 行的作业，按 300 行一块、
    同时读四块，被杀后重启完成，结果相同。
11. **规模**：约 530 行代码、380 行单元测试（不用容器时包覆盖率 96.6%）和 150 行互通测试；没有遗留，不需要 I175b。

### I176 MySQL 与 TiDB 作为目标

- **做什么**：目标 `mysql`：insert 与 update 写成 `INSERT ... ON DUPLICATE KEY UPDATE`，delete 按键删除，按表合并成多行语句
  批量写入；`transactions: "source"` 把每个源事务写成一个事务，`"batch"` 把若干个合成一个；`ddl: "report"`（默认）或
  `"stop"`；适用于 TiDB，其差异（事务大小、`AUTO_RANDOM`）予以处理或拒绝。
- **完成标准**：在 Docker 中与 MySQL 8.4 和 TiDB 互通：MySQL 到 MySQL 的流水线在混合写入负载下，经过重放和一次杀进程后，
  结束时校验和相同。
- **涉及**：`sync/mysql`、`test/sync`、`docs`。

**待评审的决定**（实现时做出，2026-11-08；在用户裁定前有效）：

1. **放在哪里。** 包 `sync/mysql`，包名 `syncmysql`，I174 的源也放在这里；连接参数（`address`、`username`、`password_env`、
   `database`、`tls`、`ca_file`、`cert_file`、`key_file`、`server_name`、`tls_rsa`、`server_public_key_file`、`timeout`）由一个
   两者共用的函数转换成 `mysqlwire.Config`。`tls_rsa` 回答了 I172 的第 4 个问题：一个默认关闭的参数，加入 `TLS_RSA_*` 套件，
   供 MySQL 5.7 的镜像使用。
2. **用 `VALUES()` 做 upsert。** 对行的每一列写 `INSERT ... ON DUPLICATE KEY UPDATE c=VALUES(c), ...`：MySQL 8.0.20 起弃用
   `VALUES()`，改用行别名，但 MySQL 5.7、MariaDB 和 TiDB 没有行别名，这一种写法对它们都适用。备选：在 MySQL 8.0.19 及以上用别名。
3. **按记录的键删除**，键是不含 null 的列映射（单值键或含 null 的键使记录永久失败）：`DELETE ... WHERE k IN (?, ...)`，
   多列键用 `(a, b) IN ((?, ?), ...)`，每条语句至多 1,000 个键。键改变了的 update（其 `before` 中键列的值不同）先删除旧键的行。
4. **源事务可以跨批次保持打开。** `transactions: "source"` 下，一个在源事务中间结束的批次让服务器的事务保持打开，并确认其中
   的记录：在所有目标都拿到事务的最后一条记录之前，检查点不会越过事务的结尾，所以崩溃后会重放整个事务。目标记住打开的事务
   中有什么，旧连接出错后，新连接先把它们重写一遍，再写本批。`transactions: "batch"` 在批次中最后一条结束源事务的记录处提交，
   使保持打开的部分只属于一个源事务；否则水位线可能越过一个服务器仍可能回滚的源事务。备选：每批结束时提交，跨批次的源事务
   分几部分提交。
5. **没有事务的记录**（快照的，或源本身没有事务）在 `transactions: "source"` 下每批（或两个源事务之间的每一段）放在服务器的
   一个事务中。
6. **拒绝。** SQLSTATE 属于 22 类（数据）、23 类（约束）或 42 类（表或列不存在、语句或访问被拒）的服务器错误，以及 1300、
   1364、1366、1411、3140，使记录永久失败；多行语句被拒时（InnoDB 和 TiDB 只撤销该语句），逐行重写以找出出错的记录。它之前写入
   的内容会提交，即使是源事务的一部分，该记录交由 `on_error` 处理；`on_error: "stop"` 时流水线从检查点重放该事务。死锁（1213）、
   锁等待超时（1205）、TiDB 的冲突（8002、8022、9007）和连接错误会关闭连接，整批重写。备选：回滚整个源事务，并使其第一条记录失败。
7. **`ddl: "stop"` 使 DDL 记录永久失败**，交由 `on_error` 处理：目标无法不顾 `on_error` 地停止流水线，所以 `skip` 或
   `dead_letter` 时流水线会越过它继续。`report` 以 info 级别记录命名空间和语句（前 200 字节）。备选：在 `fitsync` 中给目标一个
   停止流水线的办法。
8. **TiDB** 按版本识别（`-TiDB-`）。会话设置 `allow_auto_random_explicit_insert`，使 `AUTO_RANDOM` 键接受源的值；
   `max_transaction_bytes` 不设置时为 16 MiB（TiDB 的 `txn-total-size-limit` 为 100 MiB，且计入索引），超过它的源事务分几部分
   提交并给出警告，而不是被拒绝。备选：拒绝这样的事务，停止流水线。
9. **会话**设置 `time_zone = '+00:00'`（时间以 UTC 写入：`TIMESTAMP` 保存时刻，`DATETIME` 保存 UTC 的钟面时间），并在
   `sql_mode` 中加入 `NO_AUTO_VALUE_ON_ZERO`，这是互通测试发现的：写入 `AUTO_INCREMENT` 或 `AUTO_RANDOM` 列的键 0 被赋了新值。
   备选：一个 `time_zone` 参数。
10. **一个连接，不是 `ParallelSink`**：作业的快照经一个写入者写入。备选：作业中每个写入者一个连接。
11. **测试连接**在自己的连接上登录并执行 `SELECT 1`。
12. **互通。** 计划中 MySQL 到 MySQL 的流水线需要 I174 的源；在此之前，一个产生混合负载的测试源（每个事务 1 到 6 个对 300 个
    键（含 0）的 insert、update、改键和 delete）经运行时写入每台服务器，由崩溃钩子在随机位置杀掉并从检查点重启，然后在没有检查点
    时整个重放：表（`AUTO_INCREMENT` 键，TiDB 上为 `AUTO_RANDOM`，含 `DECIMAL`、`DATETIME(6)`、`JSON` 和 `VARBINARY` 列）最终与
    直接写入预期行的表按服务器端校验和相等，两种 `transactions` 模式，在 MySQL 5.7、8.0、8.4，MariaDB 10.11、11 和 TiDB v8.5.3
    上。`test/sync/mysql.sh` 在它们旁边启动 TiDB（`$MYSQL_PREFIX-tidb`，`pingcap/tidb:v8.5.3`，`TIDB_PORT` 29217，root 无密码、
    无 TLS；`TIDB=0` 不启动），因此 `test/sync/run.sh` 和 CI 也会启动它；测试读取 `FIT_SYNC_TIDB`。MySQL 到 MySQL 的运行由 I174 加上。
13. **规模**：约 790 行代码，1,000 行针对假连接和假服务器的单元测试（98.1% 语句覆盖率，无容器），300 行互通测试；没有剩余工作，
    因此没有 I176b。

### I177 PostgreSQL 客户端：连接、认证与查询

- **做什么**：包 `sync/postgres/wire`：启动、TLS（使用 TLS 时默认 `sslmode` 为 `verify-full`）、SCRAM-SHA-256（含通道绑定）、
  MD5 除非显式开启否则拒绝、简单与扩展查询协议、`COPY FROM STDIN`、复制连接的命令；支持 PostgreSQL 13 到 17。
- **完成标准**：在 Docker 中与每个版本互通：常见类型都能经扩展协议往返，一百万行的 `COPY`，带与不带通道绑定的 SCRAM。
- **涉及**：`sync/postgres/wire`、`test/sync`。

**待审查的决策**（实现时所做，2026-11-08；在用户裁定之前有效）：

1. **位置。** 包 `sync/postgres/wire`，包名 `pgwire`，以免与同时导入的 `sync/mysql/wire` 冲突。备选：包名 `wire`。
2. **基于 `net.Conn` 与 `crypto/tls` 的阻塞连接，而不是 fib。** 一个 `Conn` 由它自己的一个 goroutine 使用，与决定 036 中
   数据库连接的做法一致（少、长、不占 fib 的 worker）：协议是一问一答，COPY 和复制流需要阻塞写带来的背压，通道绑定需要从
   `crypto/tls` 取得服务端证书。只有 `SendStandbyStatus` 可以与等在 `ReceiveReplication` 中的 goroutine 并行。备选：fib 的
   异步连接加回调。
3. **`sslmode`**：`disable`、`require`、`verify-ca` 与默认的 `verify-full`；没有 `prefer` 和 `allow`，它们静默退回明文，
   决定 036 不允许。用 `SSLRequest` 请求 TLS；不使用 PostgreSQL 17 的直接 TLS（`sslnegotiation=direct`）。`Config.TLS`
   携带根证书、客户端证书和服务器名；连接器将从 `ca_file` 等参数构造它。备选：像 libpq 一样默认 `prefer`。
4. **通道绑定**（`tls-server-end-point`）默认 `prefer`：使用 TLS 且服务端提供时用 SCRAM-SHA-256-PLUS；使用 TLS 而服务端不
   提供时发 `y,,`（被剥掉的提供会被服务端发现）；没有 TLS 时发 `n,,`。`require` 在服务端没有用 -PLUS 认证时失败，服务端根本
   不要密码时也失败。以 Ed25519 签名的证书没有对应的哈希：`prefer` 下不绑定，`require` 下报错。备选：TLS 下默认 `require`。
5. **SCRAM 不做 SASLprep**：密码按字节使用，与 PostgreSQL 对 SASLprep 拒绝的密码的处理相同；ASCII 密码两种方式结果一样，
   规范化会改变的非 ASCII 密码无法认证。服务端要求超过 4,194,304 次迭代时拒绝。备选：在 fit 中实现 SASLprep（RFC 4013）。
6. **MD5 和明文密码默认拒绝**，除非设置 `AllowMD5` 或 `AllowCleartext`；不支持 Kerberos、GSSAPI 和 SSPI。
7. **上下文结束会断开连接。** 在单独的连接上发送 `CancelRequest`（尽力而为，明文 TCP），并以上下文的错误关闭连接；调用方新建
   连接，因为协议无法从任意位置恢复。wire 没有自己的单次调用超时：调用方用上下文限定；上下文没有截止时间时，`ConnectTimeout`
   （10 秒）限定连接过程。服务端错误（`*Error`，带 SQLSTATE 和各字段）不影响连接继续使用。备选：等待被取消查询的错误并保留连接。
8. **扩展协议每次 `Query` 两个往返**：先 Parse 和 Describe，再 Bind 和 Execute，这样能知道参数类型，并对这里能解码的类型以
   二进制请求结果，其余类型用文本。`Prepare` 命名一条语句，之后每次执行一个往返。参数以文本发送，只有 `bytea` 参数的
   `[]byte` 以二进制发送。备选：二进制参数，或语句缓存。
9. **取值。** 整数和 `oid` 为 `int64`，`float4`/`float8` 为 `float64`，`numeric` 为 `Numeric`（精确的十进制文本，含 `NaN`
   和无穷），`json`/`jsonb` 为 `JSON`，`uuid` 为字符串，`bytea` 为 `[]byte`，`date` 为 UTC 零点，`timestamp` 为其墙上时间
   按 UTC 表示，`timestamptz` 为 UTC，日期和时间戳的无穷为字符串 `infinity`/`-infinity`，这些类型的数组为 `[]any`（多维时
   嵌套）；其他类型（`time`、`timetz`、`interval`、`money`、`inet`、枚举、范围、复合类型）为服务端的文本。参数：Go 的数值、
   布尔、字符串，`time.Time` 按参数类型写出（`timestamp` 用其所在时区的墙上时间，`timestamptz` 带偏移；年份 1 到 9999），
   `time.Duration` 作为 interval，`Numeric`、`JSON`、指针，以及作为数组字面量的切片。备选：为 time 和 interval 定义 fit
   自己的类型。
10. **上限**：服务端的单条消息至多 `MaxMessageBytes`（64 MiB）；`SimpleQuery` 保留的结果行总量同样受此限制，用于命令和小结果；
    `Query` 流式读取行。备选：除服务端的 1 GB 外不设上限。
11. **COPY**：`CopyIn` 每个 CopyData 攒 64 KiB；`WriteRow` 与 `AppendCopyRow` 写 COPY 的文本格式；数据传输中服务端的错误
    在 `Close` 时得知；`Abort` 发送 CopyFail。另外提供了计划之外的 `CopyTo`（COPY TO STDOUT），供 I179 和 I180 的测试做
    校验和。不提供二进制 COPY。备选：写入时同时留意服务端的错误。
12. **复制命令**：`IDENTIFY_SYSTEM`；`CREATE_REPLICATION_SLOT`，只建逻辑 slot，15 起用新语法、之前用旧语法，带快照动作
    （`export`、`use`、`nothing`）；`DROP_REPLICATION_SLOT [WAIT]`；带加引号插件选项的 `START_REPLICATION ... LOGICAL`；
    读成 `XLogData` 和 `Keepalive` 的流；standby status 更新；`EndReplication`。不在这里：物理复制、`BASE_BACKUP`、
    `TIMELINE_HISTORY`、`READ_REPLICATION_SLOT`、`ALTER_REPLICATION_SLOT`，以及解码 `pgoutput` 的消息，那是 I178 的工作
    （pgoutput 以文本发送值，因此 I178 还要按类型 OID 把文本转成值）。
13. **互通。** `test/sync/postgres.sh` 启动 `postgres:13`…`17-bookworm`（`fit-sync-pg13`…，端口 29513–29517），TLS 用容器内
    `openssl` 生成的 CA 并复制出来，用户 `fit` 用 SCRAM，`wal_level=logical`；`test/sync/run.sh` 引入它，CI 的
    `sync-interop` 作业运行这些测试（`test/sync/postgres_test.go`，读取 `FIT_SYNC_PG`、`FIT_SYNC_PG_CA_DIR`、
    `FIT_SYNC_PG_USER`、`FIT_SYNC_PG_PASSWORD`，没有时跳过）。PostgreSQL 13 的 initdb 除非给出 `--auth-host=scram-sha-256`，
    否则把超级用户密码存为 MD5，脚本设置了它。MD5 的拒绝只针对假服务端测试。备选：为一个 MD5 用户加一行 pg_hba。
14. **单元测试针对进程内按脚本运行的后端**（TLS 证书在测试中生成，SCRAM 的服务端一侧，统计取消请求），与 `sync/elasticsearch`
    的假集群相同：不用容器时覆盖包内 96.9% 的语句。备选：回放抓包。
15. **规模**：代码连注释约 2,600 行，单元测试约 2,000 行，互通测试约 500 行，超出计划中一个客户端的规模，因为复制命令和
    COPY TO 也在其中；没有剩余工作，因此没有 I177b。

### I178 PostgreSQL 作为源

- **做什么**：源 `postgres`：publication 与 slot 不存在时创建（`create: true`）或要求已存在；以 `pgoutput`（协议 2，服务端
  支持时流式传输大事务）执行 `START_REPLICATION`；relation、insert、update、delete、truncate、begin 和 commit 消息变成记录，
  按 relation 的类型 OID 定类型；检查 `REPLICA IDENTITY`，使 delete 与 update 带有键；只在目标确认后才在 standby status
  update 中发送已刷写的 LSN，并应答 keepalive；报告 slot 的延迟，带告警阈值。
- **完成标准**：与每个版本互通：多表变更、大事务、truncate、服务端重启；slot 的 `confirmed_flush_lsn` 从不超过目标已有的
  位置；`kill -9` fit 不丢任何事务。
- **涉及**：`sync/postgres`、`test/sync`、`docs`。

**待审阅的决定**（实现时做出，2026-11-08；在用户裁定前有效）：

1. **位置。** 包 `sync/postgres`，与其他连接器一样命名为 `syncpostgres`，源在 `source*.go` 中，与 I180 的目标同包；注册
   `sync.sources.postgres`。备选：每个方向一个包。
2. **一条复制连接做全部事情。** 源以 `replication=database` 连接，在其上用简单查询协议执行目录查询（`SHOW wal_level`、
   `pg_publication`、`pg_publication_tables`、`pg_class`、`pg_replication_slots`）和 `CREATE PUBLICATION`，然后执行
   `CREATE_REPLICATION_SLOT` 和 `START_REPLICATION`；一次运行就是管道 goroutine 上的一条连接，任何错误后重新建立。
   备选：再开一条普通连接用于目录查询和 slot 延迟。
3. **`create`** 把不存在的 publication 建成 `FOR TABLE` `tables`（不用需要超级用户的 `FOR ALL TABLES`），把不存在的 slot
   建成不带快照的；两者此后都不修改也不删除，因此移除的管道的 slot 由运维者删除（文档已说明）。不设 `create` 时两者都必须
   已存在；已有的 publication 必须包含 `tables` 列出的每张表。备选：管道离开配置时删除 slot，或修改 publication 以匹配
   `tables`。
4. **启动时检查副本标识**：publication 发布 update 或 delete 时，`REPLICA IDENTITY NOTHING` 的表、或默认标识而没有主键的表
   视为错误（于是管道像任何源错误一样重启并说明原因）。只发布 insert 的 publication 不需要键。备选：只告警，记录不带键。
5. **键**取主键（从目录读取）；用 `REPLICA IDENTITY USING INDEX` 或没有主键时取标识的列（`FULL` 且无主键时为全部列）。键改变的
   update 变成旧键的 delete 加新行的 insert，都在原事务中，使按键写入的目标不会两者都保留。备选：`before` 带旧键的 update，
   交给目标处理。
6. **未改变的 TOAST 值**（pgoutput 的 `u`）不放入 `after`，`REPLICA IDENTITY FULL` 时从 `before` 取；只更新给定字段的目标会
   保留旧值。备选：用查询读取（与之后的写入有竞争）或让记录失败。
7. **truncate** 变成每张表一条 `ddl` 记录，`after` 为 `{"statement": "TRUNCATE schema.table [RESTART IDENTITY]
   [CASCADE]", "truncate": true}`，只报告不执行，与决定 036 对 DDL 的处理一致。备选：目标会执行的专门操作。
8. **按类型 OID 解析 pgoutput 的文本值**：会话在启动时设置 `DateStyle ISO`、`TimeZone UTC`、`extra_float_digits 3`、
   `bytea_output hex` 和 `IntervalStyle postgres`，因此格式已知。整数和 `oid` 为 int，浮点为 float（含 `NaN`、`Infinity`），
   `numeric` 为 decimal，`bytea` 为 bytes，`json`/`jsonb` 为 JSON，`date` 为 date，`timestamp` 为 UTC 时间，`timestamptz`
   为带服务器偏移的时间，这些类型的数组（含边界、嵌套、引号）为数组；`uuid` 以及其他类型（`time`、`interval`、`inet`、
   枚举、范围、复合类型）、公元前日期、9999 年以后的年份和无穷值为服务器的文本，与 I177 的二进制值一致。不使用 pgoutput 的
   `binary` 选项（14+）。备选：服务器支持时用二进制。
9. **事务**：记录的 `Tx.ID` 是事务 xid 的十进制；源扣住一条记录，以便给最后一条标上 `Last`；只有最后一条带位置
   `{"lsn": "X/Y"}`，即提交的结束 LSN，`START_REPLICATION` 从它恢复。提交在检查点之前的事务（slot 起点更早）被跳过。
   没有本管道记录的事务不产生任何记录。
10. **告诉 slot 的位置**：write、flush、apply 都是最后保存的检查点，在每次 `Confirm` 时、每个 `status_interval`（10s）
    （无论是否变化，作为防止 `wal_sender_timeout` 的心跳）以及服务器要求时立即发送。**当交出的每个事务都已在检查点中时**，
    告诉 slot 的是此前没有任何欠账的最新位置：没有记录的事务的结束位置、事务之外 keepalive 的位置；这样冷清的表不会让繁忙的
    未发布表的 WAL 一直保留。这会告诉 slot 一个比检查点文件更靠后的位置，之所以安全是因为二者之间没有属于本管道的内容；
    手工回拨的检查点不能早于 slot 的 `confirmed_flush_lsn`，文档已说明。备选：只告诉检查点，slot 保留 WAL 直到下一次
    发布的变更（Debezium 不开心跳时的默认做法）。
11. **流式传输**（`streaming: "auto"`）：PostgreSQL 14 起用协议 2 并开启 `streaming on`，13 用协议 1（`off` 保持协议 1）。
    流式事务的消息保存在 `spool_dir`（默认系统临时目录）下各自的文件中，直到其 stream commit，再作为一个事务读回；stream
    abort 删除该文件，子事务的 abort 按 xid 丢弃其变更；一次运行结束时删除留下的文件。其大小只受服务器上事务本身的限制。
    不使用协议 3 和 4（两阶段、并行应用）。备选：关闭流式传输，由服务器溢写大事务并在提交时发送。
12. **slot 的延迟**是服务器 WAL 末端（来自 `XLogData` 和 keepalive）与上次告诉 slot 的位置之间的 WAL；超过 `lag_warning`
    （1 GiB）时记录一条警告，回落时记录一条 info；`Source.Lag` 留给 I189 的指标。备选：在第二条连接上查询
    `pg_replication_slots`。
13. **作业**（`mode: "once"`）读到启动时服务器的 WAL 末端（`IDENTIFY_SYSTEM`）为止，每次状态更新都请求 keepalive；有了
    I179 后它将是快照的模式。
14. **`Test`** 连接并检查 `wal_level`、publication、其各表的标识和 slot，不创建任何东西。
15. **留给 I179 的接缝**：`prepare` 返回它创建的 slot（`setup.slot`，带一致点），创建时用 `SnapshotNothing`，I179 在此改为
    请求导出快照；`Run` 中留有在 `START_REPLICATION` 之前执行快照的位置；位置是一个结构体，I179 在其中加入进度。
16. **互通测试**（`test/sync/postgres_source_test.go`，`test/sync/postgres.sh` 启动的 PostgreSQL 13 到 17）：一个事务中跨两张表
    的变更、键的改变、未改变的 TOAST 值、`REPLICA IDENTITY FULL`、truncate、带回滚 savepoint 的 30,000 行事务（在测试服务器
    上用 `ALTER SYSTEM` 把 `logical_decoding_work_mem` 设为 64kB，使其在 14+ 上流式传输，由
    `pg_stat_replication_slots.stream_txns` 验证，在 13 上溢写）；在任何检查点之前 `confirmed_flush_lsn` 不动，每个检查点时
    恰好等于它；从检查点重启；写入未发布的表使 slot 前进；终止 walsender，并在有 `FIT_SYNC_PG_PREFIX`（现由 `postgres.sh`
    导出）时用 `docker restart` 重启服务器，不丢任何东西；以及在写入 300 个事务的同时，把写往内存目标的管道在随机位置像
    `kill -9` 那样杀掉，最终每一行都在。备选：测试自己的容器用于重启。
17. **规模**：约 1,360 行代码、970 行单元测试（伪造的复制连接，无容器时覆盖包内 98.6% 的语句）和 510 行互通测试；没有剩余
    工作，没有 I178b。

### I179 PostgreSQL 快照

- **做什么**：以 `EXPORT_SNAPSHOT` 创建 slot（或为新快照创建新 slot），在 `SET TRANSACTION SNAPSHOT` 到该快照的事务中按键分块
  并行读取各表；之后从 slot 的一致点开始跟随流，因此交接是精确的；块的进度记在检查点中。
- **完成标准**：在写入进行中做的快照，结束时目标与源的校验和相同；恢复的快照若其导出快照已不存在，则该表从头开始并说明原因。
- **涉及**：`sync/postgres`、`test/sync`。

**待审阅的决定**（实现时做出，2026-11-09；在用户裁定前有效）：

1. **`snapshot`：`initial`（默认）或 `never`。** `initial` 在没有检查点时读取各表；I178 的行为（只读变更）即 `never`，其测试现已
   设置它。没有 `always`：新的快照由 Admin API 发起（I188 的 `Snapshot`，它会删除检查点）。备选：默认 `never`，保持 I178 的行为。
2. **快照从哪里来。** `create` 新建的 slot 用 `EXPORT_SNAPSHOT` 创建，读表期间本次运行的复制连接保持空闲，之后在其一致点
   `START_REPLICATION`。slot 已存在时（手工创建，或为新快照删除了检查点），在第二条复制连接上用 `EXPORT_SNAPSHOT` 创建一个
   **临时 slot**，流从*它的*一致点开始，服务器以此为解码起点（之前提交的事务被跳过），因此这里的交接同样精确；快照结束时临时
   slot 随其连接消失。永久 slot 从不删除或重建（I178 决定 3）。备选：删除并重建 slot，或者不用 slot 取当时的快照、从 slot 的
   位置重放。
3. **按键分块并行读取。** 表按主键或其 `REPLICA IDENTITY USING INDEX` 索引（与流的键一致）切成每段 `chunk_rows`（100,000）行
   的范围：先 `count(*)`，再按键做 `row_number()` 取每第 `chunk_rows` 行的键，二者都在导出快照中，因此在该快照中何时规划都
   相同。两者都没有的表（无键的 `FULL`，或只发布插入）是一个整表读取的块。同时读取 `snapshot_parallel`（4，最多 64）个块，
   每个读者用自己的普通连接，每块在 `BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY; SET TRANSACTION SNAPSHOT` 中通过游标读取
   （每次 `FETCH` 至多 1,000 行，行大时更少，每次约 16 MiB）。边界是键的文本，作为行与无类型字面量比较，因此服务器能读回的
   任何键类型都可用。备选：`ctid` 范围（PostgreSQL 14+），或用抽样定边界。
4. **快照记录**是没有事务（`Tx` 为空）、没有提交时间的 insert，其键和值与流对同一行给出的完全相同（按类型 OID 解析文本值，
   列相同：不含生成列，保留 publication 的列列表），并应用 publication 的行过滤（15+）。备选：每块一个事务。
5. **快照期间的位置**为 `{"lsn": <流的起点>, "snapshot": {"id", "tables": [{"name", "chunks"}], "done": "0-41,43", "at":
   [{"chunk", "key"}]}}`，与 I167 的作业一样放在每块的最后一条记录和每 100 条中的一条上，记录经同一把锁发出，每块扣留一条；
   快照的最后一条记录只带 `{"lsn": ...}`。因此检查点从不声称目标缺少的块或行已完成。备选：把整个规划放进位置。
6. **续传。** 快照中途失败的运行保留其临时 slot 的连接，从而保留导出快照，以及规划，五分钟（`holdFor`）；检查点指向该快照的
   下一次运行在其中精确续传，从每块最后的键之后继续。否则（fit 重启、超过五分钟、slot 由本次运行自己创建而运行会关闭其连接、
   或 `SET TRANSACTION SNAPSHOT` 返回 22023）快照已不存在：所有块都已完成的表保持完成，其余各表从新的临时 slot 的快照**整表
   重读**，并用一条警告列出它们，而流仍从第一个快照的点开始，因此两者之间的变更在重读的表上重放，在按键写入的目标中收敛
   （与 I175 的 MySQL 快照收敛一样）。备选：无论完成多少，都从新的临时 slot 精确地重做整个快照。
7. **作业**（`mode: "once"`）实现 I167 的 `Chunker`：`Chunks` 规划 publication 的各表（publication 不存在时用 `tables`；不需要也
   不创建 slot），`ReadChunk` 在自己连接上的独立 `REPEATABLE READ` 事务中读一个块，从 `{"key": [...]}` 之后续传；无键表的块
   整块重读。作业的各块不在同一个导出快照中（之后没有可交接的流，而快照会在整个作业期间阻挡 vacuum）：每块自身一致，与 MySQL
   和 MongoDB 的相同。`never` 时作业读到服务器末端的变更，与 I178 相同。备选：作业用一个导出快照，由一条从 `Chunks` 到作业结束
   的连接持有。
8. **目录查询**除主键外还读取副本标识索引的列和非生成列；15+ 上读 `pg_publication_tables` 的 `attnames` 和 `rowfilter`。
   备选：`SELECT *`，包含生成列，忽略列列表和行过滤。
9. **互通测试**（`test/sync/postgres_snapshot_test.go`，PostgreSQL 13 到 17）：一边插入一边读 3,000 行的表，分别经 `create`
   新建的 slot 和已存在 slot 旁的临时 slot，每行恰好一次（没有既在快照又在流中的）；三张表（int 键、text 加 int 的复合键、
   `REPLICA IDENTITY FULL` 无键）在被更新、删除、插入和改键的同时，写往内存目标的管道在第五次保存检查点时被杀掉（通常在快照
   中途）后再运行，此时快照已不存在、各表重读，最终与各表逐行相等；作业的各块各读一次，以及从某个键之后续传一个块。各版本
   连续三次通过。
10. **规模**：约 790 行代码、580 行单元测试（伪造服务器的 SQL 和 slot，无容器时覆盖包内 98.1% 的语句）和 300 行互通测试；
    没有剩余工作，没有 I179b。

### I180 PostgreSQL 作为目标

- **做什么**：目标 `postgres`：`INSERT ... ON CONFLICT (key) DO UPDATE` 和按键 delete，按表批量写入，快照用 `COPY` 写入临时表
  再合并，需要时保持源事务；类型由记录的值映射，映射写进文档。
- **完成标准**：PostgreSQL 到 PostgreSQL、MySQL 到 PostgreSQL 的流水线在混合负载下，经过重放和一次杀进程后，结束时校验和相同。
- **涉及**：`sync/postgres`、`test/sync`、`docs`。

**待审查的决策**（实现时所做，2026-11-08；在用户裁定前按此执行）：

1. **位置**：包 `sync/postgres/sink`（`syncpgsink`），而非同时由 I178 的源使用的 `sync/postgres`；连接参数（`address`、
   `user`、`password_env`、`database`、`sslmode`、`ca_file`、`cert_file`、`key_file`、`server_name`、`channel_binding`、
   `timeout`）为目标自己的扁平参数，如 I168、I186，两者都完成后可与源共用。`user` 必填；不提供 MD5 和明文密码。备选：与源同一个包。
2. **连接**：阻塞的 `pgwire` 连接组成的小池，每个写入者一个（流一个，作业 `job.max_parallel` 个，即 `ParallelSink`），
   在流水线的 goroutine 上；断开或停在事务中的连接被关闭。`Test`（I188 的 `Tester`）连接并执行 `SELECT 1`。
3. **语句**：upsert 为 `INSERT ... VALUES (...),(...) ON CONFLICT (key) DO UPDATE SET` 行的其他各列 `= EXCLUDED.col`
   （只有键列的行为 `DO NOTHING`）；删除为 `DELETE ... WHERE key IN (...)`（多列键用行构造）。一条语句是同一表、操作和列集合的
   连续记录，最多 `max_statement_rows`（1000）行、65535 个参数，同一键只出现一次（取 upsert 的最后一行，避免服务器的
   “cannot affect row a second time”；仍遇到时（如 `1.0` 与 `1.00`）逐行写入）。参数以文本发送，由服务器按列定类型。
   备选：固定大小的预备语句。
4. **快照按大小用 COPY，而非按类型**：`copy_min_rows`（100）行及以上的一串 upsert 先 COPY 到
   `CREATE TEMPORARY TABLE ... ON COMMIT DROP AS SELECT cols FROM t WITH NO DATA`，再用一条 `INSERT ... SELECT ... ON CONFLICT`
   合并；`copy: "always"` 或 `"never"` 可覆盖。快照的记录与其他插入无异，只能看一串的大小。备选：只在作业中用 COPY。
5. **类型映射**：值写成 PostgreSQL 能读的文本（见文档中的表）：时间带自己的偏移（`timestamp` 列取墙上时间，`timestamptz` 取时刻），
   日期为 `YYYY-MM-DD`，字节在语句中也写成 bytea 的十六进制文本（`\x..`），使复制和插入的行相同，数组为数组字面量，映射为 JSON；
   字符串含 NUL 时记录失败。备选：按列类型发送二进制参数。
6. **表**为 `table`（`{namespace}`），在第一个点处分为模式和表，MySQL 源的 `db.table` 即模式和表；目标不建表。键列为键的字段
   或 `key_columns`（取自键或行）；upsert 的行补上缺少的键列。
7. **事务**：`batch`（默认）每个批次一个事务。`source` 每个源事务一个；未结束事务的记录在目标中跨批次暂存、被确认
   （检查点不会落在事务中，不能越过它们），随其最后一条写入；暂存的事务若下一条记录属于别的事务或又是其第一条（重放），则丢弃。
   备选：跨写入保持打开的事务，连接断开时会丢掉已确认的记录。
8. **错误**：SQLSTATE 类 22 和 23 为永久错误；其他错误（表或列不存在、无唯一索引的 `42P10`、连接断开、`40001`）回滚批次并重试。
   遇到永久错误时在保存点后逐条重写以找出那条记录：`batch` 时其前的记录提交、它失败；`source` 时其整个源事务不写入
   （其后的记录确认但不写），暂存记录被拒绝时由批次的第一条记录承担失败，错误中写明事务。备选：`source` 时写入去掉被拒记录的事务。
9. **DDL 被确认而不执行**（决策 036）。备选：`ddl: "stop"`。
10. **互通**：`test/sync/postgres_sink_test.go` 使用 `test/sync/postgres.sh` 的服务器（此处在 13 和 17 上，TLS 加 SCRAM）：
    generate 的所有类型分别插入和复制，校验和相同，并按映射读回；被拒绝的值与不存在的表；37 个双列键在三条一组的事务中混合
    upsert 和删除，流水线在每个崩溃点被杀后重启，两种事务模式下结束时校验和都等于按序应用的结果。计划中的 PostgreSQL 到
    PostgreSQL、MySQL 到 PostgreSQL 流水线需要 I178 和 I174 的源，均未完成，留给 I192 的矩阵。备选：等源完成。
11. **规模**：约 900 行代码、1,400 行测试（进程内运行目标语句的假服务器上的单元测试覆盖包的 98.8%）；无遗留，因此没有 I180b。

### I181 MongoDB 客户端：BSON 与 OP_MSG

- **做什么**：包 `sync/mongo/wire`：BSON 与记录值之间的编解码（无对应类型的保留为带标签的值）、带 section 的 `OP_MSG`、
  `hello` 与副本集主节点发现、SCRAM-SHA-256、TLS、带 `getMore` 的游标、超时；支持 MongoDB 6、7 与 8。
- **完成标准**：在 Docker 中与副本集互通；BSON 模糊测试；故障切换后能找到新主节点。
- **涉及**：`sync/mongo/wire`、`test/sync`。

**待审查的决策**（实现时所做，2026-11-08；在用户裁定之前一直有效）：

1. **放在哪里。** 包 `sync/mongo/wire`（`mongowire`）：只是客户端，没有连接器、参数和 schema；I182 的源和 I183 的目标声明连接
   参数（`uri`，或主机、凭据与 TLS），据此或用 `ParseURI` 构造 `Options`。备选：连接参数在这里声明，由两者共用。
2. **阻塞式客户端，不在 fib 上。** 一个连接是一个 `net.Conn`（配合 `crypto/tls`），由调用它的 goroutine 一问一答地读写，
   一次一条命令；同步的连接器在自己的 goroutine 上调用它，从不在 fib worker 上（规则 1）。MongoDB 在每个连接上按序应答，
   到数据库的连接少而长，change stream 的 `getMore` 在服务端等待；在 fib 的连接上用回调在这里得不到什么，却要为 SCRAM、
   游标和发现写状态机。备选：基于 fib 连接与回调的客户端。
3. **无对应类型的值以规范扩展 JSON 加标签。** ObjectId 读成 `{"$oid": "<hex>"}`，timestamp 读成
   `{"$timestamp": {"t": .., "i": ..}}`，子类型不为 0 的 binary 读成 `{"$binary": {"base64": .., "subType": "04"}}`，
   `$regularExpression`、`$numberDecimal`（仅 NaN 与无穷）、`$minKey`、`$maxKey`、`$undefined`、`$symbol`、`$code`
   （带 `$scope`）和 `$dbPointer` 同理；正好是这种形状的 map 写回时还原为其类型，所以读出再写回的文档不变。一个真实文档若
   唯一的字段是 24 位十六进制的 `$oid`，会被写成 ObjectId（MongoDB 不在存储的文档中保留 `$` 开头的名字）。备选：ObjectId 写成
   普通十六进制字符串，MongoDB 目标就无法把它与字符串区分。
4. **整数是 int64。** int32 与 int64 都读成 int；int 不论大小都写成 int64，使一个字段只有一种 BSON 类型（`$type` 查询和校验器
   看到的）；超出 int64 的 uint 写成 decimal128。MongoDB 到 MongoDB 时 int32 字段因此变成 long。备选：放得下时写成 int32。
5. **decimal 精确，否则拒绝。** decimal 写成 decimal128；34 位或指数范围放不下时报错，不舍入。decimal128 按 BSON 规范的写法
   读出：指数小时为普通写法（`-12.3400`），否则为科学计数法（`1E+3`），JSON 也把它当数字。备选：舍入到 34 位。
6. **时间是 UTC 毫秒。** time 或 date 写成 datetime，向下取整到毫秒，丢掉偏移；datetime 读成 UTC 时间。JSON 值写成它所含的
   内容：对象为文档，整数为 int64（超出时 34 位放得下则为 decimal128，否则为 double），其他数字为 double。备选：JSON 写成字符串。
7. **只用主节点，需要时再找。** `Client` 把每条命令发给可写的主节点（副本集的主节点、单机服务器或 mongos）：先种子，再服务器
   提到的成员，从节点指出的主节点优先，一轮接一轮（100 ms 增长到 1 s），直到 `ServerSelectionTimeout`（30 s）；设置了
   `ReplicaSet` 时不接受其他副本集的服务器。没有监控 goroutine：只在命令遇到连接错误或“不是主节点”的回答（代码 10107、
   13435、13436、189、91、11600、11602、10058）时才重新寻找。不从从节点读，不按 election ID 检查过时的主节点。备选：驱动程序的
   服务器发现与监控，带心跳。
8. **一条命令至多发送两次。** `Run` 和 `Cursor` 在这类错误后把命令再发给新找到的主节点一次；写入因此可能执行两次，同步的
   写入（按 `_id` upsert 与 delete）允许这样。没有 session 和可重试写（`lsid`、`txnNumber`）；`getMore` 从不重发（游标只在
   一台服务器上）。备选：带 session 的可重试写。
9. **有界连接池。** 到主节点至多 `MaxConns`（4）个连接；游标在耗尽或关闭前一直占着它的连接（`getMore` 必须去同一台服务器），
   所以忘了关闭的游标会占一个位置。主节点变化时丢弃空闲连接。
10. **超时。** 一次往返受 context 的截止时间约束，没有时受 `Timeout`（60 s）约束；被任一方切断的往返会关闭连接，因为它的回答
    可能仍会到来。`ConnectTimeout`（10 s）约束拨号、TLS、`hello` 和认证。大于 `MaxMessageBytes`（48,000,000，服务器声明更小时
    取其值）的回答在读取前被拒绝。互通测试发现 socket 的截止时间可能早于 context 自己的计时器触发；这样的错误报告为
    context 的 `DeadlineExceeded`。
11. **只用 SCRAM-SHA-256，不做 SASLprep。** 密码按原样使用，这对可打印 ASCII 与 SASLprep 结果相同；不支持 SCRAM-SHA-1、
    X.509、LDAP 和 AWS 认证；服务器只为该用户提供 SCRAM-SHA-1 时报错并说明。加盐密码按客户端缓存（至多八个），连接池的每个
    连接不必各跑一次 PBKDF2；接受 4,096 到 10,000,000 次迭代。备选：SASLprep 的表。
12. **不支持的部分。** 拒绝 4.2（wire version 8）之前的服务器（测试了 6.0、7.0 和 8.0）；不压缩；回答带校验和时检查，自己从不
    发送；`hello` 走 `OP_MSG`，从不用旧的 `OP_QUERY`。
13. **连接字符串。** `ParseURI` 读取 `mongodb://` 及 `replicaSet`、`authSource`、`authMechanism`（SCRAM-SHA-256）、
    `tls`/`ssl`、`tlsCAFile`、`tlsCertificateKeyFile`、`connectTimeoutMS`、`socketTimeoutMS`、`serverSelectionTimeoutMS`、
    `maxPoolSize` 和 `appName`；其他选项都被拒绝，包括 `tlsInsecure`，所以不会悄悄漏掉什么；不读 `mongodb+srv://`（需要 DNS
    SRV 和 TXT 查询）。备选：经解析器支持 SRV。
14. **`Doc(k, v, ...)`** 供其他包构造命令，因为 vet 在那里要求 `E` 的字段带名字。
15. **互通服务器。** 每个版本是一个容器中三个成员的副本集（`test/sync/mongo.sh`），每个成员容器内外端口相同，因为客户端按副本集
    给成员起的名字（`localhost:<port>`）连接；keyfile 认证、一个 root 用户、带本次运行自有 CA 的 `preferTLS`。
    `test/sync/run.sh` 在 29661–29683 上启动它们，每次运行都重新创建（`KEEP=1` 也一样），因为证书属于本次运行。CI 的
    `sync-interop` 作业经 `run.sh` 运行它们；这里没有更新 `ci.yml` 中的注释。

### I182 MongoDB 作为源

- **做什么**：源 `mongo`：在数据库或集合上开 change stream，`fullDocument: updateLookup`（或配合 pre-image 用
  `whenAvailable`），resume token 作为检查点；先取流的开始时间、再按 `_id` 范围做快照，使交接有重叠；invalidate 或 token 已
  超出 oplog 时报错并说明需要快照。
- **完成标准**：与副本集互通：insert、update、replace 和 delete，故障切换，fit 重启；`kill -9` 前后不丢数据。
- **涉及**：`sync/mongo`、`test/sync`、`docs`。

**待审查的决策**（实现时所做，2026-11-08；在用户裁定之前一直有效）：

1. **连接参数，与目标共用。** 包 `sync/mongo`（`syncmongo`）；其 `conn.go` 放 I183 的目标共用的部分：`ConnParams`、
   `NewClient` 和 `Ping`，对应参数 `uri`、`password_env` 和 `timeout`，每个连接器以相同的名字和说明各自声明。`uri` 是
   `mongowire.ParseURI` 读取的 `mongodb://` 连接串（主机、`replicaSet`、`authSource`、`tls`、`tlsCAFile`、
   `tlsCertificateKeyFile`、超时）：MongoDB 用户熟悉它，TLS 也不需要 fit 自己的参数。备选：像其他连接器那样用 `hosts`、
   `username`、`ca_file` 等参数。
2. **`uri` 中不放密码。** 用户写在 `uri` 里，密码只放在 `password_env`；`uri` 带密码会被拒绝，有用户却没有 `password_env`
   也会被拒绝。备选：接受 `uri` 中的密码。
3. **键是 `{"_id": ...}`**，快照和变更流都一样，而不是事件的整个 `documentKey`（分片集合的 documentKey 还含分片键），使同一
   文档的快照记录和变更记录键相同。备选：原样用 `documentKey`。
4. **位置用记录的规范 JSON。** 检查点是 `{"token": <resume token>}`；快照期间是
   `{"start": <流的起始 token>, "ns": "db.coll", "id": <最后的 _id>}`；作业的块是 `{"id": ...}`；token 和 `_id` 都是规范
   JSON（`{"map": {"_data": "82..."}}`、`{"int": 5}`），使每种 BSON 类型都能原样还原。备选：只存 token 的 `_data`，`_id`
   用扩展 JSON。
5. **快照的交接。** 流的起点是快照前打开（随即关闭）的一个 change stream 的 post-batch resume token；随后快照按集合名的顺序
   逐个读取集合，按 `_id` 用 `find` 的 `min` 和 `max` 走 `_id` 索引（`hint`）：`$gt` 过滤只在同一 BSON 类型内比较，多种类型的
   `_id` 会被漏掉；读关注 `majority`，读到的不会被回滚。快照的最后一条记录带起始 token，在它之后重启就读变更流。备选：用应答
   的 `operationTime` 做 `startAtOperationTime`。
6. **没有文档的更新被跳过**：`updateLookup` 时文档之后已被删除的（删除随后到来）；`whenAvailable` 时没有后像的。备选：`after`
   为 null 的更新，或以 `updateDescription` 作为行。
7. **事务。** 带 `lsid` 和 `txnNumber` 的变更属于事务 `<会话 ID 的十六进制>:<编号>`；第一条立即标记，最后一条要等下一条变更
   属于别的事务、或一次取变更的请求空手而回（等 `max_await`，1s）才知道：在此之前该记录被扣住。备选：每条变更自成一个事务，
   检查点于是可以落在事务内部（MongoDB 能从那里恢复）。
8. **流覆盖什么。** 一个集合：该集合的流；多个：数据库的流，加 `ns.coll` 上的 `$match`（保留 `invalidate` 和
   `dropDatabase`）；不设 `database`：整个部署的流（`allChangesForCluster`），其快照为除 `admin`、`config`、`local` 外的所有
   数据库，除视图、时间序列和 `system.` 集合外的所有集合。`drop`、`rename` 和 `dropDatabase` 为 `ddl` 记录（`after`：
   `operationType`、`ns`、`to`）；不请求扩展事件。备选：每个数据库各开一个流。
9. **游标原地恢复。** 因连接断开、故障切换（不再是主节点）、游标被服务器超时回收（目标卡住队列期间）或带
   `ResumableChangeStreamError` 标签的错误而中断的游标，从最后读到的 token 或 `_id` 重新打开，连续最多五次（100 ms 起翻倍），
   之后错误交给管道，由其从检查点重启。备选：所有错误都交给管道重启。
10. **新快照由用户发起。** oplog 已不含的 token（代码 286、280、260）或 invalidate 报错并说明需要新快照（管道的快照控制，或
    重置其检查点）；源不自行开始快照。备选：立即开始新快照并告警。
11. **第一条记录前没有检查点。** 尚未读到任何记录的管道没有可保存的位置：`snapshot: "never"` 时在第一条变更前重启会再次从
    “现在”开始，空快照会以新的起点再读一遍。备选：一条无行记录携带 post-batch token，而记录模型没有这种记录。
12. **作业。** 作业的快照按每个集合的 `count` 切分，每块 `chunk_documents`（100,000）个，边界取自每块四个 `_id` 的有序
    `$sample`；`snapshot: "never"` 时作业不切分，从检查点读变更流直到一次请求空手而回。备选：`splitVector`（需要同步用户
    少有的权限）或 `$bucketAuto`（扫描整个集合）。
13. **互通测试**（`test/sync/mongo_source_test.go`，用 `test/sync/mongo.sh` 的 MongoDB 6、7、8 副本集）：经 TLS 先快照、再读
    每种变更和一个事务，从某个位置重启，drop 与 invalidate；流进行中让主节点降级；集合不断变化时在随机位置杀掉
    （`Options.Crash`）并复制到 memory 目标，最终一致；作业的块。

### I183 MongoDB 作为目标

- **做什么**：目标 `mongo`：无序 bulk write，upsert（按由键得到的 `_id` 做 replace）和 delete，读取逐文档错误，write concern
  取自配置。
- **完成标准**：MySQL 到 MongoDB、MongoDB 到 MongoDB 的流水线在重放后收敛。
- **涉及**：`sync/mongo`、`test/sync`、`docs`。

**待审查的决定**（实现时做出，2026-11-08；在用户裁定前有效）：

1. **放在哪里。** 包 `sync/mongo`（`syncmongo`），与 I182 的源相邻：目标在 `sink.go`，其连接参数在 `sinkconn.go`（`uri` 或
   `hosts`、`replica_set`、`username`、`password_env`、`auth_source`、`tls`、`ca_file`、`cert_file`、`key_file`、
   `server_name`、`connect_timeout`、`timeout`、`max_conns`），像 I168 和 I186 一样平铺声明；它与 I182 同时开发，两套连接参数
   在合并时合为一套。备选：在 `mongowire` 中只声明一次（I181 的决定 1）。
2. **`uri` 中不放密码。** 含密码的连接字符串被拒绝；密码由 `password_env` 提供，与 fit 的所有凭据一样。设置了 `tls` 及其文件
   时，它们代替字符串中的 TLS 选项；只有 `cert_file` 没有 `key_file` 时，它同时包含证书和私钥，如 MongoDB 的
   `tlsCertificateKeyFile`。备选：用 `uri_env` 保存整个字符串。
3. **`_id` 取自键。** 只有一个字段的键取该字段的值（`{"id": 7}` 得到 `_id: 7`，MongoDB 源的 `{"_id": ...}` 得到其值），多个
   字段的键按顺序组成嵌入文档，标量键取其本身，带标记的值（`{"$oid": ...}`）取其 BSON 类型；没有键的记录取行中的 `_id`，两者都
   没有的记录永久失败，键中有 null 字段的也是如此。行自身若有 `_id`，以键得到的为准。备选：`id_field` 参数，或像 I168 那样把
   复合键拼成字符串。
4. **upsert 即替换。** insert、update 和 message 写成 `update`，语句为 `{q: {_id}, u: <行，_id 在前>, upsert: true}`，整篇文档
   被替换；delete 写成带 `limit: 1` 的 `delete`。不用 `$set` 只写改变的字段：所有源给出的都是整行（MongoDB 源使用
   `updateLookup`）。DDL 被确认但不写；不是 map 的行永久失败。备选：为部分行提供 `$set` 模式。
5. **无序写入，但每个键有序。** 一个批次中只发送每篇文档的最后一条记录（它覆盖之前的记录），因此每个集合的一条 `update` 和
   一条 `delete` 中的语句涉及互不相同的文档，`ordered: false` 不会打乱同一键的顺序；命令按 `max_request_bytes`（8 MiB）和
   100,000 条语句切分。某篇文档失败时，从该文档在批次中的第一条记录起，按互不相同文档组成的段依次重写，于是 `BatchError`
   恰好指出失败的那条记录，它之前的每条记录都已写入。命令级错误（或其 write concern 的错误）指出最早未写入文档的第一条记录。
   备选：有序 bulk write，逐条记录，每个批次都更慢。
6. **错误。** 文档的写错误是永久的（校验器的 121、唯一索引的 11000、文档过大、错误的 `_id`），除非其代码表示可能会过去（写冲突、
   主节点降级、中断、锁和时间限制）；命令级错误由流水线重试（认证、主节点丢失、带 `wtimeout` 的 `writeConcernError`），除非是
   语句或集合本身的问题（代码 2、9、14、72、73、10334）。超过 16 MiB 的文档，或 `decimal128` 无法精确表示的小数，在发送之前
   就失败。备选：未知代码视为暂时错误。
7. **Write concern。** `write_concern` 即 `w`（`majority`、数字、标签集），`journal` 即 `j`，`write_timeout` 即 `wtimeout`；
   不设置时用服务器的默认值（5.0 起副本集上为 majority）。拒绝 `w: 0`：不确认的写入无法做检查点。
8. **文档写到哪里。** 命名空间 `db.collection` 同时给出两者（在第一个 `.` 处切分）；`database` 代替数据库（此时没有 `.` 的
   命名空间即集合），`collection` 代替集合。名称只由服务器检查（代码 73，永久）。目标从不创建集合或索引：MongoDB 在第一次写入
   时创建集合。备选：像 I186 那样的 `{namespace}` 模板。
9. **没有事务。** 源的事务不被保留：每篇文档的写入是原子的，一个批次不是。事务需要 session，而 I181 不支持（其决定 8）。
10. **`ParallelSink`**（作业的写入者共享客户端的连接池，`max_conns`，默认 4），以及 `Tester`：测试时查找主节点、认证并 ping。
11. **互通。** `test/sync/mongo_sink_test.go`，基于 I181 的副本集（`test/sync/mongo.sh`，MongoDB 6、7 和 8）：100 个键的
    3,000 次变更按随机批次写入，每批有时从更早的记录起重写，最终与变更所留下的状态一致；`generate` 各种类型的记录经在随机崩溃
    点被杀的流水线、走 TLS 写入，最终与未被杀的一次运行相同；一个包含各种 BSON 类型（ObjectId、字符串和整数 `_id`、
    `decimal128`、时间戳、UUID、正则表达式、最小和最大键、嵌套数组）的集合，以 `_id` 为键读成记录，经目标写两次，副本逐字节
    相同；校验器的拒绝为永久错误；连接测试。MySQL 到 MongoDB 和 MongoDB 到 MongoDB 的*流水线*有待 I174 和 I182 的源；在那之前
    由集合复制代替后者，I192 的矩阵将运行两者。备选：等这些源合并后再完成 I183。
12. **规模**：代码约 620 行，测试约 950 行；没有遗留，没有 I183b。

### I183b 一套 MongoDB 连接参数

I182 的源和 I183 的目标是同时做的，连接参数不同：源有 `uri`、`password_env` 和 `timeout`，目标有 `uri` 或 `hosts`
及十几个其他参数。现合为一套，配置 MongoDB 时对两者都一样。2026-11-09 完成。所做的决定，待审查：

1. **两者都用目标的那一套。** `sync/mongo/conn.go` 中的 `ConnParams`：`uri` 或 `hosts`、`replica_set`、`username`、
   `password_env`、`auth_source`、`tls`、`ca_file`、`cert_file`、`key_file`、`server_name`、`connect_timeout`、
   `timeout`、`max_conns`；源在原有三个之外得到其余参数，没有改名，两边已有的配置都不变。`sinkconn.go` 已删除；
   `NewClient` 为两者检查参数并创建客户端。备选：只用源的三个参数，TLS 与用户写在 `uri` 中。
2. **在两者中平铺声明，由测试保持一致。** `config` 不读取嵌入结构体，所以 `Source` 和 `Sink` 各自把 `ConnParams` 的字段放在
   最前面，名称、类型和标签都相同（说明改为对两者都适用），由 `conn()` 复制；`TestConnParamsShared` 在有任何差异时失败。
   备选：嵌套的 `connection` 对象（新的配置形状），或让 `config` 支持嵌入结构体（共享代码，未改动）。
3. **源的 `uri` 不再必填**：与目标一样，`uri` 或 `hosts`，在 `Provision` 中检查。
4. **参数只替换连接串中对应的设置。** 连接串的 `appName` 现在保留（之前目标会用 `fit-sync` 覆盖它），不设置 `timeout` 时
   连接串的 `socketTimeoutMS` 也保留（之前源会用 60s 覆盖它）；都没有设置时为 60s 和 `fit-sync`。检查沿用目标的：`uri` 中带密码被拒绝，
   `password_env` 需要用户（`username` 或 `uri` 中的用户），用户需要 `password_env`，`username` 与 `uri` 中的用户不能同时给出，
   TLS 文件需要 `tls`。
5. **目标的连接测试改用 `Ping`**，与源相同，错误以 `mongo:` 开头。
6. **互通**：`TestMongoSourceStream` 中源的 TLS 子测试改用 `username`、`tls` 和 `ca_file` 连接，不再把用户和 TLS 写在 `uri`
   中；其他测试仍用 `uri`。

### I184 Kafka 客户端

- **做什么**：包 `sync/kafka/wire`：metadata、produce、fetch、list offsets 和 API versions 所需的协议版本；带 gzip、snappy、
  lz4 与 zstd 的 v2 record batch；SASL PLAIN 与 SCRAM、TLS；跟随 leader 变化；支持 Kafka 3.x 与 Redpanda。
- **完成标准**：在 Docker 中与两者互通；每种压缩双向可用；fetch 与 produce 中途 leader 变化不丢数据。
- **涉及**：`sync/kafka/wire`、`test/sync`。

**待审查的决策**（实现时所做，2026-11-08；在用户裁定之前一直有效）：

1. **放在哪里。** 包 `sync/kafka/wire`，包名 `kafkawire`（而非 `wire`，MySQL、PostgreSQL 与 MongoDB 客户端的 `sync/*/wire`
   也会用它）。`Config`（引导 broker、客户端 ID、TLS、SASL、超时、重试时长、大小上限）构造的 `Client` 提供 `Metadata`、
   `Partitions`、`Produce`/`ProduceRecords`、`Fetch`、`ListOffsets`/`Offset`/`OffsetAt`，计划之外还有 `CreateTopics` 与
   `DeleteTopics`（供测试用，也供 I185 为目标建 topic），以及 `PartitionFor`，即 Java 客户端的 murmur2 分区器，使同一个键落在
   Java 生产者放它的分区。备选：不做管理 API，由测试用 broker 自带的工具建 topic。
2. **阻塞连接，不用 fib。** 每个 broker 连接是一个 `net.Conn`（TLS 经 `crypto/tls`），在互斥锁下写入、可同时有多个请求在途，
   由连接自己的 goroutine 读取并按序把每个响应交给调用者；broker 对一个连接一次只处理一个请求，所以会在 broker 上等待的
   fetch 对每个 broker 另有一个连接，与 produce、metadata 和 offset 用的连接分开。这就是决定 036 的“到数据库的连接少而长，
   各占一个 goroutine”：不占用 fib 的 worker，Kafka 的长轮询从事件循环中也得不到好处。备选：fib 引擎加每连接一个解析器。
3. **只用非 flexible 的 API 版本。** Produce 3–8、Fetch 4–11、ListOffsets 1–5、Metadata 1–8、CreateTopics 2–4、
   DeleteTopics 1–3、SaslHandshake 1、SaslAuthenticate 1、ApiVersions 0；连接时由 ApiVersions 取双方都支持的最高版本。
   这些都在 Kafka 4.0 的基线（KIP-896）与 Redpanda 支持的范围内，不需要 tagged field；zstd 需要 Produce 7，较旧的 broker
   上会被拒绝。备选：用紧凑编码的 flexible 版本，只有 broker 不再支持这些版本时才需要。
4. **编解码器。** gzip 用标准库；snappy 与 zstd 用 fit 已为 HTTP 压缩引入的 `klauspost/compress`（没有新依赖）；LZ4 在包内
   实现（帧格式与 xxHash32，约 300 行），因为 fit 的依赖中没有 LZ4。snappy 按 Java 客户端的写法以 xerial 分帧写出，读取时
   接受裸数据或分帧；LZ4 写成独立的 64 KiB 块，读取时接受任意块大小、链接块与校验和。每次解压在分配内存之前就受
   `MaxBatchBytes`（64 MiB）限制，响应受 `MaxResponseBytes`（128 MiB）限制。级别：gzip 1–9、zstd 1–22，0 为默认。备选：
   引入 pierrec/lz4 作为新依赖。
5. **不幂等，至少一次。** 批次不带 producer ID：应答丢失（连接断开、leader 停止）的 produce 会再发一次，可能写入两次；
   leader 变化的互通测试中 26 万条记录有 50 条写了两次，没有丢失。调用者对每个分区只有一个 produce 在途时，分区内保持顺序。
   备选：`InitProducerId` 与序号（幂等生产者），若 I185 的目标需要去重再加。
6. **以重试跟随 leader。** 因可重试原因（不是 leader、没有 leader、未知 topic、副本不足、超时、连接失败）被拒的分区，
   在 100 ms 起、加倍至 1 s 的退避后重发，错误表明 leader 已变时先重新读取 metadata，直到调用的 context 结束或过了
   `RetryFor`（30 s）；不发送 leader epoch（-1），所以客户端不按 epoch 防范过时的 leader。超过 `RequestTimeout`（30 s，
   在请求它等待的时间之外）仍无应答的 broker 会使其连接及连接上的所有请求失败。备选：按次数重试。
7. **一次调用，多个分区。** `Produce`、`Fetch` 与 `ListOffsets` 接受多个分区，按 leader 分组，每个 leader 一个请求，各
   leader 并行，按顺序返回每个分区的结果；每次调用每个分区至多一个批次。fetch 在每个 leader 都应答后才返回，所以有记录的分区
   要等空闲 broker 的 `MaxWait`；在别的分区已拿到记录之后重试的分区不再等待。备选：每个 broker 一个持续向 channel 输出的
   fetcher，I185 的源可以在此之上加。
8. **不是消费者组。** 没有组、提交和心跳 API：offset 由调用者保存在同步的检查点中（I185）。`ReadCommitted` 丢弃已中止事务
   的记录和控制批次；magic 0 与 1 的消息集（0.11 之前的 broker 写的，或从那时留下的）报错而不读取。备选：读取旧消息集。
9. **SASL。** PLAIN、SCRAM-SHA-256 与 SCRAM-SHA-512（校验服务端签名，迭代次数至多 2^20）；有生命周期的会话在其 90 % 时
   重新连接。不支持 OAUTHBEARER 与 GSSAPI。这里允许不带 TLS 的 PLAIN；I185 的配置可以给出警告。备选：拒绝不带 TLS 的
   PLAIN。
10. **互通。** `test/sync/kafka.sh` 启动 Kafka 3.9.1（三个同时是 controller 的 KRaft broker，各有 PLAINTEXT、SASL_PLAINTEXT
    与 SASL_SSL 监听器，以及脚本自己的 CA）和 Redpanda v25.2.9（一个节点，PLAINTEXT 与 SASL 监听器），并打印环境变量；
    `test/sync/run.sh` 会调用它，所以 CI 的 `sync-interop` 作业也运行这些测试。leader 变化的测试在生产和读取记录的同时，
    从测试中用 `docker` 停止并杀掉分区的 leader（`FIT_SYNC_KAFKA_CONTAINERS` 中列出的容器）；Redpanda 只有一个节点，没有
    leader 变化。在 Kafka 上每种编解码器也作为 topic 的 `compression.type`，由 broker 从另一种编解码器重写，再由客户端读取；
    Redpanda 不重新压缩，所以在那里每种编解码器是写出再读回。没有试 Kafka 4.x。备选：也用 Redpanda 集群测 leader 变化。
11. **规模**：代码约 3,000 行，测试约 2,800 行（在进程内的假集群上做单元测试，不用容器时语句覆盖率 98.7 %）；没有 I184b。

### I185 Kafka 作为源与目标；经 TiCDC 的 TiDB

- **做什么**：源 `kafka`：从 fit 检查点中的 offset 读取 topic 与分区（不用消费组，使检查点与其他部分在一起；可选地提交到消费组
  以便观察），每个 topic 一个编解码器（`json`、原样、canal-json、Debezium 的 JSON、TiCDC simple 协议、WASM 编解码器）；目标
  `kafka`：记录的键作为消息键，topic 取自命名空间，批量并压缩，默认要求所有 ISR 确认。TiDB 作为源：TiCDC changefeed 以
  canal-json 或 simple 协议写入 Kafka，解码为带提交 TS 的记录；在此之前经 MySQL 协议按某个 TS（`tidb_snapshot`）做快照。
- **完成标准**：与 Kafka 和 Redpanda 互通；Kafka 到 Kafka 的复制在杀进程前后保持按键顺序；Docker 中带 TiCDC 的 TiDB 集群同步到
  PostgreSQL，结束时校验和相同。
- **涉及**：`sync/kafka`、`sync/tidb`、`test/sync`、`docs`。

**待审的决定**（实现时做出，2026-11-09；在用户裁定前有效）：

1. **位置。** 包 `sync/kafka`（`synckafka`）：源与目标 `kafka`，以及编解码器 `raw`、`canal_json`、`debezium` 和
   `ticdc_simple`；它们是 Kafka 上的变更格式，但作为 `sync.codecs` 的模块也可读文件、HTTP 请求体和 S3 对象。包 `sync/tidb`
   （`synctidb`）：源 `tidb`。`syncmysql` 导出 `ConnParams` 与 `WireConfig`（两行），使 tidb 源的连接沿用 mysql 连接器的参数和
   检查。备选：编解码器放进 `sync/cdc` 包。
2. **offset 只存在检查点中，不提交到消费组。** 计划中“可选地提交到消费组以便观察”未实现（I184 的客户端没有消费组 API）；源改为报告
   延迟：每次拉取后调用 `Run.ReportLag`，单位 `offsets`（I189 的 `LagOffsets`），即各分区高水位减下一个 offset 之和。offset 包含
   控制批次和压缩留下的空洞，因此只是大约落后的记录数。备选：向参数中指定的消费组 `OffsetCommit`。
3. **位置** 为 `{"offsets": {topic: {partition: 下一个 offset}}}`，包含所有分区，设在每条消息的最后一条记录上。一条消息变成的
   多条记录是一个事务（编解码器给的 ID、TiCDC 的提交 TS 或 `topic/partition@offset`），检查点不会落在一条消息内部。不产生记录的
   消息（TiCDC 每秒一次的 watermark、bootstrap）本身不推进检查点：由下一条记录的位置覆盖；长时间无数据后重启会重读这些
   watermark。备选：由运行时不经目标直接确认的“位置占位”记录。
4. **每个源一个编解码器**，而非计划中的每个 topic 一个：一种格式一条流水线。两侧默认 `raw`，Kafka 到 Kafka 的复制逐字节相同；
   值为 null 的消息是对其键的删除（`tombstones: "delete"`，Debezium 删除后的墓碑用 `skip`），目标把带键的删除写成墓碑
   （`deletes: "tombstone"`，或 `encode`）。编解码器也可以解码键（新增 `KeyDecoder`）：`debezium` 把 Debezium 的键事件读成键的
   各列。编解码器拒绝的值成为一条含该值与错误的 message 记录，与文件源的行相同。
5. **键与分区。** 记录的键为字符串、字节、decimal 或 JSON 时按其文本发出，否则写成普通 JSON（`{"id":1}`），并按 Java 客户端的
   murmur2 对这些字节选分区；一个批次中无键的记录发往同一分区，下一批次发往下一个分区。分区数一分钟后重新读取。
6. **目标是至少一次，不是幂等生产者**（I184 的决定 5 不变）：Kafka 到 Kafka 的互通测试在 12 次杀进程中把 3,000 条消息复制成
   3,076 条，每个键首次出现的副本保持顺序。备选：幂等生产者（`InitProducerId` 与序号），它也只在一个生产者会话内去重。
7. **批次。** 每个分区的消息按不超过 `max_batch_bytes`（1 MiB，压缩前）的记录批次发送，各分区的第一批在一次 produce 中，第二批在
   下一次；某分区被拒绝即在该处停止，`BatchError` 指向批次中最早未写入的记录。多条消息的批次遇到 `MESSAGE_TOO_LARGE` 时逐条
   重发；单条消息则该记录永久失败（`Permanent`）。默认 `acks: "all"`、`compression: "lz4"`；只有设置 `create_topics` 才创建 topic。
8. **从哪里开始读。** 没有检查点时从 `start`（`earliest`）开始；检查点中没有的分区或之后新增的分区（每分钟检查一次）从最早处开始。
   被保留策略删除的 offset 使流水线停止（`offset_reset: "error"`），而不是静默跳过。默认只读已提交的消息（`read_uncommitted` 可改）。
   作业在每个分区开始时的末尾处停止。
9. **tidb 源** 以嵌套的 `kafka` 接收 kafka 源的参数，编解码器由 `protocol` 决定（`canal-json`，需 `enable-tidb-extension=true` 以带
   提交 TS；或 `simple`）。快照在为此开启的事务的 `@@tidb_current_ts` 上用 `tidb_snapshot` 读取，按主键分页（`page_rows`），每行的
   位置为表及其键；提交 TS 不大于该 TS 的变更被丢弃。changefeed 必须早于快照创建，快照必须在 `tidb_gc_life_time` 内读完；二者都
   不检查。只保留 `tables` 的变更；不设 `tables` 时不做快照。备选：经 TiCDC 的 API 从快照 TS 启动 changefeed，这需要 TiCDC 的地址与
   权限。
10. **值。** TiCDC 的 canal-json 把 ENUM 和 SET 写成数字、二进制列写成 ISO-8859-1 文本；`canal_json` 把二进制读回字节，ENUM 和 SET
    保留为数字文本，tidb 源再按启动时读到的表结构换成标签，与其快照一致。BIT 为 uint，TIME 为文本，DATETIME 与 TIMESTAMP 为
    `time_zone`（UTC）中的时间，bool 编码为 tinyint `1`/`0`。simple 协议按其 bootstrap 与 DDL 消息中的表结构定型；在此之前的行
    （源在两次 bootstrap 之间启动）保持文本且无键，除非 tidb 源已从 `information_schema` 预置表结构。TiCDC 的 Debezium 把二进制
    列写成 base64 字符串且没有表明类型（仍为字符串），decimal 写成 double（float）；`debezium` 编解码器在有 schema 时解码 Kafka
    Connect 的 `Decimal` 和 Debezium 的时间类型，写出时不带 schema。
11. **互通。** `test/sync/tidb.sh` 在 `kafka.sh` 的网络上启动 PD、TiKV、TiDB 和 TiCDC v8.5.3（TiDB 在 `TIDB_CDC_PORT`，29690），
    `run.sh` 在 Kafka 之后启动它，CI 的 `sync-interop` 因此会运行这些测试：在 Kafka 与 Redpanda 上把 3,000 条带键消息从一个 topic
    复制到另一个，随机位置杀进程；`generate` 的记录以 `json`、`canal_json` 和 `debezium` 经目标写入再经源读回；TiDB 经两种协议同步
    到 PostgreSQL 17：先快照 300 行，再写入 600 次混合负载（插入、更新、删除、事务中改键），随机位置杀进程，最终校验和相同。
    在 Docker 中运行 TiCDC 可行；其消息保存在 `sync/kafka/testdata` 中，供编解码器的单元测试使用。
12. **规模**：约 2,300 行代码（编解码器 1,040 行），1,600 行针对模拟集群和模拟 TiDB 的单元测试（不用容器时分别覆盖 97.5% 和 96.7%
    的语句），520 行互通测试；无 I185b。

### I186 Redis 作为源与目标

- **做什么**：支持 TLS 与 ACL 认证的 RESP2/RESP3 客户端（`sync/redis/wire`）；源 `redis`，从检查点的 ID 起用 `XREAD` 读取
  stream；目标 `redis`，`XADD` 到由命名空间得到的 stream，或每条记录一个键（`SET` 文档、`HSET` 字段、delete 时 `DEL`），可选
  TTL，使用 pipeline。
- **完成标准**：在 Docker 中与 Redis 7 和 Valkey 互通；一个 stream 在杀进程前后复制不丢数据。
- **涉及**：`sync/redis`、`test/sync`、`docs`。

**待审查的决定**（实现时做出，2026-11-08；在用户裁定之前有效）：

1. **同步自己的 RESP 客户端，而不是共享存储的。** `tlsmgr/sharedstore` 已经能与 Redis 通信，但其客户端不导出、只支持 RESP2、
   只读两层深的应答、没有 pipeline，且绑定 `config.RedisStorage`；`sync/redis/wire`（包 `wire`）是计划中的那个：RESP2 与
   RESP3（所有 RESP3 类型，跳过属性，丢弃推送），`HELLO 3 AUTH user pass SETNAME`，服务器不认识 `HELLO` 时退回 RESP2 并用
   `AUTH`，`SELECT`，TLS，pipeline，context 结束或到期时打断阻塞的读（此后连接作废并重新拨号），应答有上限（字符串 64 MiB，
   每个应答 1<<20 个元素和 256 MiB，嵌套 8 层）。`sharedstore` 保持原样，它不在本项的包内。备选：后续把 `sharedstore` 改用
   `wire`，让 fit 只有一个 Redis 客户端。
2. **各一个连接，在流水线的 goroutine 上。** 源和目标各持有一个阻塞连接（Redis 是数据库：少量长连接，不用 fib 的引擎），首次
   使用时拨号，断开后重拨；目标不是 `ParallelSink`，作业经一个写入者写它。备选：一个小连接池和 `ParallelWrites`。
3. **不用消费者组。** 源用 `XREAD` 从检查点的条目 ID 之后读取，检查点是唯一的游标；服务器上不确认、不删除任何东西，也不建组。
   备选：`XREADGROUP`，由 `Confirmer` 发 `XACK`，适合运维期望看到组的待处理列表的服务器。
4. **位置是每个流的最后 ID**，`{"streams": {...}}`，每条记录都带，重启后每个流从目标已有的最后一个条目之后继续读。后来加到
   `streams` 的流按 `snapshot` 开始；移除的流从位置中去掉。读到之前已被修剪掉的条目看不到，也不会被发现。流的键必须是 UTF-8
   （在检查点里是 JSON）。没有检查点时 `snapshot: "never"` 每次启动都读取最后 ID，如同 ClickHouse 源读取最大游标值。
5. **按 ID 恰好一次写入流，否则至少一次。** `entry_id: "auto"`（默认）由服务器选 ID，重放会再次追加条目；`entry_id: "key"`
   以记录的键（其 `id` 字段，或键本身）为 ID，服务器拒绝不大于流最后 ID 的条目（`equal or smaller than the target stream top
   item`）时算作已写入：从 `redis` 源复制的流在多次杀进程后仍每个条目恰好一份，ID 不变。这要求每个流的 ID 递增，如流自己的
   ID。备选：每个条目附带写去重键，或用 Lua 脚本。
6. **条目的形式。** `form: "fields"`：行的字段为文本，映射和数组为普通 JSON，略去 null，没有字段的行被拒绝；源把非 UTF-8
   的值读成字节，给每个条目的键为 `{"id": "<条目 ID>"}`，提交时间为 ID 的毫秒数。`form: "record"`：一个名为 `record` 的字段
   （固定，不是参数）存放规范 JSON；读回时记录保留其操作、键、命名空间（除非设置了源的 `namespace`）、行和提交时间，丢弃其
   事务（一个事务的条目未必都在），不是记录的条目按 fields 读取，而不是让源失败——那样它会无休止地重启。`op_field` 在
   `form: "fields"` 时把操作放进一个字段；没有它时，删除按其行写出，与插入无异。
7. **键。** `key` 是 `{namespace}` 和 `{key}` 的模板，多字段的键以 `:` 连接各值，没有键的记录被拒绝；`value` 为 `json`（行
   的普通 JSON，`SET`）、`record`（规范 JSON，`SET`）或 `hash`（`MULTI`、`DEL`、`HSET`、`PEXPIRE`、`EXEC`，不留下旧行的字段，
   读者也看不到写了一半的键）；删除为 `DEL`；`ttl` 为 `SET ... PX` 或 `PEXPIRE`。`mode: "keys"` 时设置流的参数、
   `mode: "stream"` 时设置键的参数都会被拒绝。
8. **哪些服务器错误会重试。** `LOADING`、`OOM`、`READONLY`、`BUSY`、`TRYAGAIN`、`MASTERDOWN`、`NOREPLICAS`、`CLUSTERDOWN`、
   `MOVED`、`ASK`、`NOAUTH`、`NOPERM`、`WRONGPASS`、`MISCONF`、连接错误和超时交给流水线的 `retry`；服务器对记录命令回的其他
   错误（`WRONGTYPE`、被拒的 ID）属于该记录，是永久的，由 `on_error` 处理。pipeline 中途断开的连接让这次往返的第一条记录失败，
   之前往返的记录已确认。
9. **默认值与名称。** `address` 默认 `127.0.0.1:6379`，与 `acme.shared_storage.redis` 相同；只有 `password_env` 没有
   `username` 时以默认用户认证；`protocol` 为 0（自动）、2 或 3；连接名为 `fit-sync`；`count` 1000，`block` 5s（至少
   1ms，绝不 `BLOCK 0`），`max_pipeline` 1000，`timeout` 10s；`snapshot` 为 `initial` 或 `never`，与 ClickHouse 源相同。
   单机服务器或主节点：不支持 Redis Cluster 或 Sentinel。
10. **配置表中 `sync` 那一行。** `docs/configuration*.md` 有三行 `sync`，是 I169、I170、I171 各自合并时留下的；现在重新合成
    一行，并加上 Redis。

### I187 S3 及兼容存储作为源与目标

- **做什么**：经 fib 异步 HTTP 客户端的 SigV4 签名；目标 `s3`，由批量行（JSON、CSV 或编解码器）组成的对象，按大小和时间滚动，
  名称由含日期和批次首个位置的模板生成，可选 gzip，完整后才写出；源 `s3`，按键顺序列出某前缀下的对象并逐行读取，最后的键和
  偏移作为检查点。
- **完成标准**：在 Docker 中与 MinIO 互通；重放时写了两次的对象名称和内容都相同；源能在对象中间继续。
- **涉及**：`sync/s3`、`test/sync`、`docs`。

**待审查的决策**（实现时所做，2026-11-08；在用户裁定之前一直有效）：

1. **放在哪里，SigV4 在此实现。** 包 `sync/s3`（`syncs3`）：目标、源、它们自己的 `Conn`（角色的引擎或自己的引擎，与 I168
   相同）以及 `Signer`，即 AWS 签名第 4 版，已用 AWS 公布的四个示例校验。fit 和 fib 中原本没有 SigV4 签名或 S3 客户端（媒体库
   和备份都写本地磁盘），因此无可复用；`Signer` 导出，供以后需要 S3 的部分使用。备选：现在就建一个 `internal/sigv4` 包。
2. **凭证。** 配置中写 `access_key_id`，其密钥由 `secret_access_key_env` 指定的环境变量提供，临时凭证用 `session_token_env`；
   都不设置时请求不签名（公开的存储桶）。凭证在管道加载时读取一次：不读实例配置文件、web identity 或 `~/.aws` 文件，临时凭证
   不会续期。备选：AWS 的默认凭证链。
3. **寻址方式。** 不设置 `endpoint`（即 AWS）时 `addressing` 为 `virtual`（存储桶在主机名中），否则为 `path`，所有兼容服务都
   支持；HTTPS 上 `virtual` 方式遇到带点的存储桶名会被拒绝，因为没有证书能覆盖它。存储桶名会被检查（3 到 63 个小写字母、数字、
   `.`、`-`、`_`）。备选：一律 `path`。
4. **签名覆盖什么。** 整个请求体的 SHA-256（不用 `UNSIGNED-PAYLOAD`，不用分块签名）：对象本来就在内存里，服务端也会因此拒收不
   完整的请求体。签名的头为 `host`、`content-type`、`content-md5` 和 `x-amz-*`；请求走 HTTP/1.1（ALPN），因为 S3 不支持
   HTTP/2。备选：不签请求体，省去哈希。
5. **一个对象就是一个批次。** 目标把收到的每个批次写成一个对象，超过 `max_object_bytes`（64 MiB，最大 5 GiB，即单次 `PUT` 的
   上限）时按顺序写成多个；因此对象按目标的 `batch`（`max_records`、`max_bytes`、`max_wait`）滚动，文档提示用户连同 `queue`
   一起调大。它不跨批次保留任何东西：确认尚未写入对象的记录会违反检查点的规则，而分段上传的分段在完成前不是对象，所以不用分段
   上传。备选：目标跨批次攒行、等对象写入后再确认，运行时无法表达这种方式（等待中的 `Write` 会卡住管道）。
6. **命名。** `key` 是取自对象第一条记录的模板（默认 `{namespace}/{date}/{timestamp}-{position}{ext}`）：其 UTC 提交时间（没有
   时用写入时间，重放时不会重现）和 `{position}`，即对其命名空间和位置做 SHA-256 得到的 20 位十六进制数；没有位置时取它之前最近
   的位置及距离（`+n`）或之后最近的位置（`-n`），整批都没有位置时取对象的行内容。`{position}` 是必需的。同一批次再写一次得到相同
   的名称和字节（gzip 头不含时间和文件名），覆盖原对象；重放若被切成不同的批次，则得到不同的名称，于是检查点到崩溃之间的记录可能
   出现在两个对象中：与所有目标一样是至少一次。命名空间和键开头的 `/` 被去掉；超过 1024 字节的键使其记录永久失败。备选：像 Kafka
   Connect 那样按偏移对齐对象，这需要数值型的位置。
7. **命名空间。** `key` 含命名空间时，命名空间每变化一次就按顺序开始一个新对象；交错的命名空间会产生很小的对象。备选：在批次内按
   命名空间分组，这会打乱不同命名空间记录之间的顺序。
8. **行。** `codec` 与 `file` 目标相同（默认 `line`，`json`、`csv`、WASM 编解码器），`{ext}` 按编解码器名称为 `.txt`、
   `.jsonl` 或 `.csv`，`gzip` 时再加 `.gz`，`Content-Type` 与之对应（gzip 时为 `application/gzip`，不设
   `Content-Encoding`，下载得到的就是对象本身）。不提供 `file` 目标那样的 `template`。备选：默认 `json`。
9. **错误。** 任何状态的 `PUT` 失败（存储桶不存在、拒绝访问、5xx）都由管道重试；编解码器无法编码的记录、或键过长，则永久失败，
   之前的对象已写入。批次第一个对象之后的失败报告为该对象第一条记录处的 `BatchError`。备选：把 403 和 404 当作永久错误。
10. **源按键列出。** 在 `prefix` 下用 `ListObjectsV2` 从最后一个键（`start-after`，最初为 `start_after`）开始列出，每页 1,000
    个；以 `/` 结尾的键被跳过。键必须递增：在最后一个键之前新增的对象不会被看到，读过之后被覆盖的对象也不会重读（`s3` 目标的名称
    随时间递增）。流模式每隔 `poll_interval`（30s）重新列出；作业列出一遍后结束。不提供后缀或 glob 过滤。备选：记住看过的每个
    键（无界增长），或用 S3 的事件通知。
11. **读取。** 对象按 `range_bytes`（8 MiB，最大 64 MiB）分段读取，每段带上列出时 ETag 的 `If-Match`：读取过程中被修改的对象使
    本次运行失败，管道从检查点重新开始；已删除（404）的对象记一条警告后跳过；到达末尾的分段（416）即为结束。键以 `.gz` 结尾的对象
    被解压（`decompress`），续读时从头重读，跳过偏移之前的行。行的处理与 `file` 源相同：去掉换行前的 `\r`，读取没有换行的最后一行，
    超过 `max_line_bytes`（1 MiB）截断，编解码器失败的行成为带错误的 message 记录。备选：每个对象一次 `GET` 整体读取，受客户端
    响应大小上限约束。
12. **记录与位置。** 命名空间为对象的键（或 `namespace`），键为 `{object, offset}`（编解码器从一行生成的第二条及以后的记录再加
    `n`），提交时间为对象的最后修改时间。每行的最后一条记录带有该行之后的位置 `{"key", "offset"}`，对象最后一行再加
    `"end": true`，因此重启后在对象内部或其后继续。源不是 `Chunker`：作业按顺序读取对象。备选：作业按对象分块。
13. **互通测试。** `test/sync/run.sh` 还会启动 MinIO 和 SeaweedFS 的 S3 网关（`fit-sync-minio` 在 29215，`fit-sync-seaweed` 在
    29216，各有一个访问密钥；`FIT_SYNC_MINIO_*`、`FIT_SYNC_SEAWEED_*`）。MinIO 官方镜像已无法从 Docker Hub 或 Quay 拉取（pull
    access denied），因此脚本使用其社区构建 `pgsty/minio:RELEASE.2026-08-04T00-00-00Z` 和 `chrislusf/seaweedfs:4.50`。只走
    HTTP；TLS 在单元测试中用 `httptest` 服务器验证。备选：Garage，或从源码构建 MinIO。
14. **`ParallelSink`**：作业的每个写入者写出各自命名的对象。
15. **规模**：约 1,300 行代码和 1,350 行测试（不启动容器时覆盖率 97.9%）；没有遗留，因此没有 I187b。

### I188 Admin API 与 `fit sync`

- **做什么**：Admin API 中的 `/sync/pipelines`（也经控制台）：每条流水线的状态、位置、延迟、吞吐、队列、错误和最后一个错误；
  暂停、恢复、重新快照；检查点的查看、设置与重置（会跳过或重放数据的重置需要 `confirm`）；测试源或目标的连接；试运行，读取
  前几条记录、经过变换后返回，不写入也不保存位置。在此之上提供 `fit sync status|pause|resume|checkpoint|test|dry-run|run`。
- **完成标准**：每个端点都有测试，经控制台的也有；试运行后检查点与目标保持原样；暂停让源停下并保留检查点。
- **涉及**：`sync`、`admin`、`cli`、`docs`。

**待审查的决定**（实现时所做，2026-11-08；在用户裁定之前有效）：

1. **Admin API 按引擎找到运行时**，与找到 streaming 角色的频道一样：角色启动时 `fitsync.Register(engine, rt)`，处理器中
   `Lookup(s.configs.env.Engine)`；`app.Manager` 不变。备选：在 `app.Manager` 上加 `Role(id)`。
2. **端点。** `GET /sync/pipelines` 与 `/sync/pipelines/{name}`；动作与频道的一样，`POST /sync/pipelines/{name}`，
   `{"action": "pause" | "resume" | "snapshot", "confirm"}`；`/sync/pipelines/{name}/checkpoint` 的 `GET`、`PUT`
   （`{"position", "confirm"}`）与 `DELETE`（`?confirm=1`，因 DELETE 没有请求体而用查询参数）；`POST .../test` 与
   `.../dry-run`；以及带 `{"pipeline": {...}}`（尚未应用的流水线配置，供 I190 的编辑器使用）的 `POST /sync/test` 与
   `/sync/dry-run`，它们不需要角色在运行。控制台与其他端点一样在 `/api/m/admin/` 下提供它们，每次改动都连同用户记入日志。
   `/node` 列出能力 `sync`。
3. **什么需要 `confirm`。** 位置是源自己的、不透明的，无法判断一次移动是跳过还是重放：因此检查点的任何改变都需要
   `confirm`，即设置成与已保存位置不同的位置，以及在有检查点时重置或重新快照；没有它就回 409，并给出当前的检查点。设置成
   相同的位置，以及没有检查点时的重置或快照，不需要。备选：由源提供比较位置的接口，只对跳过要求确认。
4. **重新快照就是删除检查点并立即重启流水线**：源按其参数开始（数据库先快照再跟随变更），作业重新开始，不管其调度。**重置**
   与之相同，只是有调度的作业等待下一个时间。没有像 Debezium 的信号那样在运行中的流旁边做增量快照；数据库源可以通过
   `Snapshotter` 接口加上。备选：快照只用于作业。
5. **设置检查点**先停止流水线（写完它持有的并保存检查点），再经同样的临时文件加改名写入压缩后的位置，然后重启。位置的形式与
   `GET .../checkpoint` 显示的一样，分块作业的位置是作业的形式；作业的进度保留，清除其 `finished`，使作业立即从该位置继续。
   `null` 或空的位置被拒绝：那是重置。
6. **暂停卡住 `Emit`**：源在下一条记录处等待，已读的仍会写出、检查点仍会保存，所以暂停的流水线的检查点最终覆盖它读过的全部。
   状态以 `paused` 代替 `running`，任何状态下都有 `paused: true`。暂停属于运行时而不属于配置：流水线自身的重启（源失败、控制
   操作）和不改变它的重载之后仍然有效，改变它的重载或 fit 重启之后不再有效。暂停期间 `http` 源的请求一直等待，直到
   `sync_ingest` 的超时回 503。备选：把暂停保存在配置或检查点文件中。
7. **恢复也会从检查点重新启动已停止**（on_error `stop`、死信未能写出）或被杀死的流水线，修好目标后无需重载。
8. **连接测试是可选接口** `fitsync.Tester`（`Test(ctx) error`），实现在源或目标上；`generate`、`memory` 与 `stdout` 直接
   通过；没有实现它的连接器报告为 `tested: false`，并提示用试运行，不算测试失败。测试的是运行中流水线自己的模块，因为有些模块
   不能再加载第二个实例（文件目标每个路径一个），测试期间持有运行时的锁，以免重载在此期间释放它们；上限 10 秒。`file`、
   `elasticsearch`、`clickhouse`、`http` 与 `wasm` 还没有实现它（留给它们的负责人，见下）。备选：每次测试都重新加载所有模块。
9. **试运行重新加载源和变换**（不加载目标），以 `Once` 从检查点运行源（`"from": "start"` 时从头；分块作业的检查点是作业的
   形式，从头读），取至多 `limit` 条记录（10，至多 1,000）、4 MiB，或 `timeout`（5 秒，至多 1 分钟）内到来的记录，经过变换，
   回答每条源记录的规范 JSON 及其变换结果或变换的错误，以及源是否结束、超时或失败。不确认、不通知源、不保存任何东西。源以
   流水线名 `<name>#dry-run` 运行，使按流水线名查找的源（`http` 源）不会顶替运行中的那个；停止后 5 秒内不返回的源不再等待，
   它返回后再释放。
10. **Status 的计数**从运行时启动该流水线起计：读取、确认、失败的记录数，错误数（写入失败的尝试、变换失败的记录、未写出的
    死信、源的失败）及最后一个错误和时间，队列与其上限，最近十秒的吞吐，以及延迟，即最后确认的记录在其提交时间之后多久被
    确认。落后于源头部的延迟（slot 的字节、主题的 offset）属于 I189。`Status` 加上了 JSON 名称；其检查点按原本的 JSON 输出。
11. **`fit sync status|pause|resume|snapshot|checkpoint|test|dry-run`** 经引导配置指定的 Admin API 工作，与 `fit token` 一样
    接受 `-c`、`-timeout`（90 秒）、`-json` 和 TLS 参数；在计划的列表中加上了 `snapshot`。试运行的等待时间是 `-wait`，因为
    `-timeout` 是请求的。`fit sync test` 在有测试失败时退出码为 1，API 拒绝时每个命令退出码为 1（409 时提示加 `-confirm`），
    用法错误时为 2。
12. **规模**：代码连同注释约 1,600 行（sync 930、admin 360、cli 340），测试约 910 行；计划的内容没有遗漏，但连接器的
    `Test` 方法（第 8 点）留给 I188b 或各连接器自己的迭代。

### I188b 早先连接器的连接测试

- **做什么**：为 I188 之前建成的连接器实现 `fitsync.Tester`：`file`、`elasticsearch`、`clickhouse`、`http`、`redis` 和
  `s3` 的源与目标，以及 `wasm`（若有合理的测试）；一个不写任何记录的廉价检查，针对各包已有的 fake 做单元测试。
- **完成标准**：每个连接器按自己将要访问的方式测试所访问的对象，通过与失败都有单元测试。
- **涉及**：`sync/file`、`sync/elasticsearch`、`sync/clickhouse`、`sync/httpsync`、`sync/redis`、`sync/s3`、`docs`。

**待审查的决定**（实现时做出，2026-11-09；在用户裁定之前有效）：

1. **Elasticsearch 与 OpenSearch。** `Conn.Test` 每次都重新请求根端点（不信任 `Info` 缓存的结果），从而集群有应答、
   凭据被接受、且是两者之一（对 OpenSearch 使用 API key 会失败，与写入时一样）。目标到此为止：不写入就无法知道能否写这些
   索引，而 `_has_privileges` 只有 Elasticsearch 有。源还会对 `index` 计数（`/_count`），这需要读权限；不匹配任何索引的
   模式或索引（404）算通过，因为源把它当作空。备选：404 算失败。
2. **ClickHouse。** 在 `database` 中执行 `SELECT 1`（数据库不存在则失败），并检查应答为 `1`。目标到此为止（不插入就无法
   检查插入权限）；源还会带着自己的 settings 执行 `SELECT * FROM (<表或查询>) LIMIT 0`，不读任何行，但需要表存在且有
   查询权限。备选：对 `table` 不含 `{namespace}` 的目标执行 `EXISTS TABLE`，这需要只写用户可能没有的权限。
3. **Redis 与 Valkey。** 使用测试自己的连接（模块的连接属于流水线的 goroutine，一次只能一个使用者）：拨号时发送 HELLO、
   认证并选择 `db`，然后 `PING`，随后关闭。不检查流或键（`TYPE` 需要 `@keyspace`）。
4. **S3。** 列出一个键的 `ListObjectsV2`：源在 `prefix` 下列出，与它读取时一样；目标在桶的根下列出，且 `AccessDenied`
   算通过，因为 S3 只对通过认证的请求这样回答（密钥错误是 `InvalidAccessKeyId`，secret 错误是 `SignatureDoesNotMatch`，
   桶不存在是 `NoSuchBucket`），而目标的用户可能只能上传对象而不能列出。不用对桶的 `HEAD`：其应答没有正文，区分上述情况
   的错误码就丢了。备选：上传一个标记对象，但这是写入。
5. **file。** 源：每个路径或 glob 的起始目录（不含 `*`、`?`、`[` 的最长前导部分）存在且可列出，当前 glob 匹配到的每个普通
   文件都能以只读方式打开，最多前 1,000 个；没有匹配到文件算通过，因为源会等待文件出现；只打开普通文件（打开 FIFO 会等待
   写入方）。目标：fit 的日志没有写这个路径；已存在的文件是普通文件（不是链接，日志文件拒绝链接），能以追加方式打开且不创建；
   文件不存在，或 `rotate` 会在旁边产生新文件时，其目录能新建文件 `.<name>.fit-test-*`，建好立即删除：这是所有测试中唯一的
   写入，不写任何记录。另一个 file 目标写同一路径的情况不报告：应用前测试的配置（`POST /sync/test`）会替换正在运行的那个
   目标，与真正的冲突无法区分。备选：Unix 上用 `access(2)`，Windows 没有。
6. **http 目标：对 `url` 发送 `HEAD`**，带目标的头部和凭据，没有正文和幂等键：按 HTTP 的规则是安全的，接收方也不会当作
   批次。2xx，以及除 401、403、407 以外的 4xx 算通过，因为只路由自己方法的接收端点可能对 HEAD 回答 404 或 405，这仍说明
   主机、TLS 和服务器都在；401、403、407 失败（凭据被拒），重定向（目标不跟随）、5xx、连接或 TLS 错误也失败。备选：只做
   TCP 和 TLS 握手（不测试凭据），或加 `test_method`/`test_url` 参数。
7. **http 源：直接通过**，与 `generate` 一样：它不访问任何东西，是客户端访问 fit；是否接收请求是流水线的状态，不是连接。
8. **wasm 源与目标：没有测试。** 它们访问的对象由插件自己决定：fit 只知道 `allow_hosts`，即不带 scheme、端口和路径的模式，
   探测它们测试不到插件实际使用的任何东西，通过了反而误导。它们仍是 `tested: false`，并提示使用试运行。合适的做法是一个
   guest 导出函数 `fit_sync_test`，插件有它时由可选的 `Tester` 调用；这会改变 guest ABI 和 `plugin/wasm/guest`，留给以后的
   条目。
9. **规模**：代码连同注释约 300 行，测试约 400 行；Redis 的 fake 会应答 `PING`。除决定 8 外，计划没有遗留。

### I189 指标、日志与死信

- **做什么**：每条流水线与每个目标的指标：进出的记录数与字节数、批次、重试、错误、死信、队列占用、按时间计的延迟（提交时间到
  确认）与按位置计的延迟（slot 或 binlog 落后的字节数、Kafka 落后的 offset）；状态变化写日志，错误日志限速；死信保存在有上限
  的文件写入器中，含记录、错误和尝试次数，可从 Admin API 重放。
- **完成标准**：每个指标都有测试检查；修复原因后重放的死信恰好到达目标一次。
- **涉及**：`sync`、`observe`、`admin`、`docs`。

**待审查的决策**（实现时做出，2026-11-09；在用户裁定之前有效）：

1. **死信是一个目标模块 `dead_letters`**，作为 `dead_letter` 目标使用（I163 的决策 3 不变）：每封死信是
   `<state_dir>/<pipeline>.dead_letters.jsonl` 中的一行 JSON（`path` 可另指，相对路径位于 `state_dir` 下），在写入被确认前同步到磁盘；
   删除也各记一行，被删的超过保留的加 1 MiB 时压缩文件。运行时在创建流水线时告诉它状态目录和流水线名；同一文件的两个目标（重载交替时）
   共用它。它不能出现在流水线的 `sinks` 中。其他目标仍可作 `dead_letter`，但只有 `dead_letters` 能列出和重放。备选：只要 `on_error`
   为 `dead_letter`，就由运行时自己保存。
2. **上限**：`max_records` 10,000，`max_bytes` 64 MiB；`when_full` 为 `refuse`（默认：写入失败，尝试用完后流水线停在该记录之前，
   不丢数据）或 `drop_oldest`（计入 `fit_sync_dead_letters_dropped_total`）。大于 `max_bytes` 的死信永久失败。备选：默认丢弃最旧的。
3. **重放会先停止流水线**（写完手中的记录并保存检查点），在请求的 goroutine 上自己写入死信，再重新启动流水线，与 `SetCheckpoint`
   一样，这样重放是每个目标唯一的写入者，运行时的队列和水位都不变；已停止的流水线也会像 `Resume` 一样重新启动。目标写失败的记录只写到
   该目标，按变换后的样子（死信的 `at` 为 `sink <name>`）；变换失败的记录重新经过变换，再写到每个目标。写入按目标的 `batch` 上限分批，
   按 `retry` 重试；仍失败、目标已不存在或不含记录的死信保留并报告，写成功的删除。已完成作业的死信也能重放。备选：把重放的记录放进运行中
   流水线的队列、不停止，这需要分发器按记录路由。
4. **重放按记录原样写入**：同一键后来已到达目标的变更会被覆盖；文档中已说明。备选：键有更新的记录时拒绝重放，但 fit 无从得知。
5. **指标**通过角色的 `app.Metered` 从 `Runtime.Status` 取得，与其他角色一样（`fit_sync_*`，标签 `pipeline`，`fit_sync_sink_*` 另有
   `sink`）；计数器自流水线启动起累计（改变它的重载会从 0 开始，正如 Prometheus 对重启的预期）。字节数是队列估算的记录大小，不是线上
   字节。目标的 `batches` 是尝试的写入次数，`errors` 是失败的，`retries` 是重试的，`failed` 是最终仍失败的记录。没有吞吐量 gauge：
   对计数器取 `rate()` 即可。状态（`GET /sync/pipelines/{name}`）新增 `read_bytes`、`sinks`、`source_lag` 和 `dead_letters`。
   备选：批写入耗时的直方图。
6. **落后于源服务器的延迟**由源自己告知：得知时调用 `Run.ReportLag(behind, unit)`，或实现 `Lagger`（`Lag() int64`，单位为字节，
   除非有 `LagUnit()` 另说），每次取状态或抓取指标时询问；postgres 源的 `Lag`（其 slot 的 WAL）已经这样读取，无需改动它。MySQL 的
   binlog 字节数和 Kafka 落后的 offset 留给这些源的负责人。备选：运行时自己查询服务器。
7. **日志**：流水线每次运行的开始（从检查点或从头）、调度的下次时间、被杀、暂停、恢复、完成与停止都会记录；目标的尝试失败、变换失败、
   被跳过的记录、写入与未写入的死信是警告，每条流水线每 10 秒最多一行，下一行说明压下了多少（目标尝试失败原先是 debug 行）。备选：
   按错误消息分别限速。
8. **端点**：`GET /sync/pipelines/{name}/dead-letters?after=&limit=`（100，最多 1,000，从旧到新，还有更多时带 `more`），
   `POST .../dead-letters/replay`，带 `{"ids"}` 或 `{"all": true}`，`DELETE .../dead-letters?id=..&id=..` 或 `?all=1`；
   `dead_letter` 目标不是 `dead_letters` 时为 404。`fit sync dead-letters [-after] [-limit]`，`-replay` 和 `-drop` 带 id 或
   `-all`；重放后仍有保留的死信时退出码为 1。
9. **规模**：代码约 1,000 行（sync 760、admin 110、cli 150），测试约 900 行。

### I190 workbench 的 Sync 页面

- **做什么**：一级菜单 **Sync**：流水线表格，含状态、延迟和吞吐迷你图；流水线页面，含由 schema 生成的编辑器（各连接器的字段）、
  **测试连接**、显示记录的**试运行**、需确认才能重置的检查点、可重放的错误与死信、暂停、恢复和重新快照；助手的同步只读工具和
  一个同步技能（I124 的注册表）。
- **完成标准**：在 workbench 的测试中，完全通过页面新建、测试、试运行、保存并观察一条流水线，中英文均可。
- **涉及**：`app/workbench`、`assistant`、`docs`。

**待审查的决策**（实现时做出，2026-11-09；在用户裁定之前有效）：

1. **一个模块 `sync`**，使用 Admin API 的服务端模块，菜单中排在网关各功能之后，菜单中有两页（**管道**、**同步配置**），单个
   管道的四页以链接形式的标签页呈现（`#/sync/pipeline/<name>`、`/edit/<name>`、`.../checkpoint`、`.../dead_letters`）。只要有
   `admin.console` 就显示，不论 sync 角色是否运行：没有运行时，列表会说明并列出草稿中的管道，因此第一条管道也能在页面上创建。
   `sync` 不再出现在“其他”配置页。备选：仅在角色运行时显示该模块。
2. **迷你走势图来自页面自身的轮询**：每 2 秒 `GET /sync/pipelines`，工作台打开期间保留最近 60 次（两分钟），吞吐取
   `throughput`，延迟取 `lag_seconds`；页面打开之前没有历史。备选：像集群图表那样取 `/metrics` 的序列。
3. **编辑器就是配置编辑器**：在共享草稿中用 `C.subsection('sync.pipelines[i]')`，带 `sync` 的 UI/.conf/.yaml/.json 标签页
   及配置栏的“检查”与“保存并应用”；**测试连接**与**试运行**把草稿中的管道原样发给 `POST /sync/test` 与 `/sync/dry-run`
   （从源的起点开始），因此保存前即可检查；运行中管道自己的按钮测试并试运行正在运行的配置。**新建管道**询问名称、取自 schema
   模块的源与 sink（默认 generate 与 memory），配置尚无 `sync` 时还询问 `state_dir`，然后打开编辑器。**移除管道**将其从草稿中
   删除；其检查点文件保留。
4. **确认**：重新快照、设置或重置检查点、重放与丢弃都先询问，说明哪些记录会被跳过、重写或丢失，然后发送 `confirm`，因此不会
   显示 API 的 409；暂停与恢复不询问。检查点页只在点击“刷新”时重新读取，不会在编辑时刷新。死信每页 50 条，可加载更多。
5. **助手的同步工具是四个只读工具**，分组 `sync`，级别 read：`sync_pipelines`（全部的摘要，或单个的完整状态及运行中的配置）、
   `sync_test`、`sync_dry_run`（5 条，至多 20 条，5 秒）与 `sync_dead_letters`（20 条，至多 100 条）。测试与试运行会像页面按钮
   一样连接源与 sink 的服务器，但不写入任何内容，因此级别为 read。没有任何工具修改管道：暂停、恢复、快照、检查点、重放与丢弃
   都留给人在页面上操作，技能中也如此说明。备选：像媒体工具那样，以这些操作为步骤的提案工具。
6. **默认不发送记录的值**：试运行与死信中的记录交给模型时，其 key、before、after 中的每个值都替换为其类型（`(string)`、
   `(int)`，map 按字段名），除非新设置 `admin.assistant.data.send_records`（设置页中的复选框）允许；命名空间、操作、位置与
   错误始终发送。备选：始终原样发送，像其他结果一样脱敏。
7. **在管理端**，`sync_pipelines` 与 `sync_dead_letters` 接受 `member`（GET，管理端会转发）；测试与试运行不接受，因为管理端
   只转发读取。
8. **一个技能 `sync-pipeline`**：创建管道（用页面工具在页面上起草，或作为提案），以及找出管道停滞、延迟、停止或积压死信的
   原因。管道最近的错误与死信旁有运行它的“问 AI”按钮。
9. **密钥仍是环境变量**：页面像编辑其他字段一样编辑 `password_env` 等；从页面把密钥写入 `admin.store`（I168 的决策 12 等待
   I190）需要连接器读取 store 引用，这不是对 `sync` 包或连接器的小改动，因此留给后续条目。
10. **页面的测试**：workbench 没有 JavaScript 运行器，因此其测试像 `TestAskSkills` 一样以文本读取页面：`TestSyncWords` 检查
    同步页面显示的每个词都有中英文且占位符一致、管道的每种状态都有词、页面调用了其提供的每个接口；`TestAskSkills` 检查“问 AI”
    的 spec。整个流程（创建、测试、试运行、保存、观察、暂停、恢复、快照、设置与重置检查点、重放与丢弃死信）在浏览器中以两种语言
    手工检查，针对运行 `generate`→`memory` 与 `generate`→`http`（连接被拒）→`dead_letters` 的 fit。备选：CI 中的无头浏览器。
11. **规模**：页面约 650 行、CSS 20 行，`assistant` 中 Go 代码约 330 行，技能 50 行；测试 330 行。

### I191 写入事务型目标时恰好一次

- **做什么**：对 MySQL、TiDB 和 PostgreSQL 目标提供 `checkpoint: "sink"`：位置与记录在同一事务中写入目标的一张表
  （`_fit_sync_checkpoints`），启动时优先于本地文件读回，因此重放不会重复写入。
- **完成标准**：在目标提交与本地检查点之间注入崩溃，没有任何行被写两次（用一张记录写入次数的目标表检查）；本地文件仍服务于
  其他目标。
- **涉及**：`sync/mysql`、`sync/postgres`、`sync`。

**待审查的决策**（构建时做出，2026-11-09；在用户裁定前有效）：

1. **`fitsync` 中一个小接口**：`CheckpointSink`，即带 `KeepsCheckpoint()`（参数为 `checkpoint: "sink"`）和
   `ReadCheckpoint(ctx, pipeline, lineage)` 的 `Sink`；后者在每次运行前于管道的 goroutine 上调用，返回目标保存的位置及其
   谱系，并告诉目标此后写什么（`sync/sinkcheckpoint.go`；在 `start`、重置和 `SetCheckpoint` 中挂钩，检查点文件加一个
   `lineage` 字段）。每次提交写入其最后一条结束源事务的记录的位置。备选：每次 `Write` 都传入位置。
2. **何时使用目标的位置**：仅当管道的每个目标都保存检查点、位置都相同、且与文件同一谱系；否则用文件，与以前一样至少一次。
   所以只在全部为事务型目标的管道中做到恰好一次，旁边有较慢的目标时它们照样重放。备选：每个保存检查点的目标按位置匹配跳过
   其位置之前的记录，旁边有别的目标时也恰好一次，但源若不能逐字节给出同一位置，之后的记录全会丢失。
3. **谱系**：检查点文件中的一个随机 ID，在重置、手工设置位置、从源开头开始的新作业时更换，在目标写入它之前先存入文件；
   其他谱系的目标行被忽略，因此这些操作不会被目标中较旧的位置撤销。带保存检查点目标的管道重置后留下一个无位置的文件而不是
   没有文件，以免被当作文件丢失；文件不见了的流（丢了 `state_dir`）采用各目标共同的谱系和位置；作业不采用，它的计划在文件中。
   备选：重置时清除目标的行，这要求当时每个目标都能连上。
4. **分块读取的作业只用文件**：它们的位置是作业的，由多个写入者同时写，提交顺序不定。备选：每个写入者一行。
5. **目标的行可能落后于文件**（单独的 DDL 记录不提交任何东西），这是安全的：运行从目标已提交处开始，绝不越过它。一次提交中
   最后一个位置之后的内容在崩溃后会再写一次：大于 `max_transaction_bytes` 而分几部分提交的源事务、没有位置的记录，以及在
   `on_error: "stop"` 下 MySQL 目标在被拒绝记录之前提交的内容。
6. **这张表属于目标自己并由它创建**（`CREATE TABLE IF NOT EXISTS`，不同于决策 036 第九条的数据表）：`checkpoint_table`，默认
   `_fit_sync_checkpoints`，MySQL 在 `database` 中（此时必须设置），PostgreSQL 按 `search_path`；每个管道名一行（`pipeline`、
   `lineage`、文本形式的 `position`（不用 JSON，`jsonb` 会重排键）、`updated`）。两个 fit 把同名管道写进同一张表会共用一行；
   删除该行和文件即重新开始。备选：以 fit 的节点和管道为键。
7. **之前运行留下的暂存内容被丢弃**于 `ReadCheckpoint`（MySQL 的打开事务、PostgreSQL 暂存的记录）：运行从它之前开始。不设
   `checkpoint: "sink"` 时 MySQL 目标仍像 I176 那样在新连接上重写它。
8. **PostgreSQL 需要 `transactions: "source"`**，`checkpoint: "sink"` 使它成为默认；拒绝 `batch`，因为它的事务可能结束在源事务
   中间。MySQL 两种模式都可以：`batch` 本就在一批中最后一条结束源事务的记录处提交。TiDB 同 MySQL。
9. **互通**：`test/sync/exactly_once_test.go`，测试源产生 3000 个不同键的插入，事务为一到四条，写入一张用 `BEFORE INSERT`
   触发器统计每次写行的表（重写的 upsert 也会触发），流水线在随机位置被杀十二次（其中包括提交之后、文件检查点之前），再运行到
   结束：`checkpoint: "sink"` 时写入次数等于行数，每条记录一次；`checkpoint: "file"` 时不丢失，有些写了两次，予以报告。
   MySQL 5.7、8.0、8.4 和 MariaDB 10.11、11 以作业运行两种 `transactions` 模式；PostgreSQL 13 到 17 以流运行，也用 `COPY`
   复制。TiDB 没有触发器：`TestMySQLSinkAcrossKills` 增加一个 `checkpoint: "sink"` 模式，在每个服务器和 TiDB 上运行，跨多次
   杀死和一次重放后校验和相同。
10. **规模**：约 330 行代码、900 行单元测试（运行时对一个测试用的保存检查点目标，在每个点被杀；每个目标对其假服务器）和
    230 行互通测试；没有剩余，因此没有 I191b。

### I192 互通与恢复矩阵

- **做什么**：`test/sync`：涵盖每个连接器的服务端及其版本的 Docker Compose 矩阵，以及一个测试：每个源对一组固定的目标写入混合
  负载，在随机位置多次杀掉 fit，结束时比较目标与源；凡改动 `sync/` 的推送都在 CI 中运行，完整矩阵每晚运行，从不跳过（v1.7.1
  的 CI 规则）。
- **完成标准**：矩阵在 CI 中运行并通过；坏掉的连接器会让它失败。
- **涉及**：`test/sync`、CI 配置。

**待审查的决定**（实现时做出，2026-11-09；在用户裁定前有效）：

1. **每个源一条流水线，同时写入所有目标**（`TestMatrix`，`test/sync/matrix_test.go`）：PostgreSQL 17、MySQL 8.4、MongoDB 8、
   Elasticsearch、ClickHouse 和 Redis（键），六个目标都拿到记录后才保存检查点，结束时每个目标逐行与源比较（源也与负载自己的
   模型比较）。每个源一份负载，各目标对照同一个真值。备选：每对源和目标一条流水线，负载和时间都是六倍。
2. **源是变更源**：PostgreSQL、MySQL 和 MariaDB、MongoDB、经 TiCDC 的 TiDB，以及 Kafka 中的 Debezium 事件；每个源在 `mxSources`
   中是一项（族、负载是否有事务和改键、一个填表并应用负载的 setup）。按游标轮询或读取追加的源（Elasticsearch、ClickHouse、S3、
   Redis 流、文件、HTTP）没有可镜像的更新和删除，保留各自的杀进程测试。备选：在矩阵中给它们只插入的负载。
3. **负载**：一个由 `id`、`v`（含引号、反斜杠和非 ASCII 的文本）、`n`（可空 bigint，五行中一行为 NULL）和 `ver`（负载计数）
   组成的行模型：插入、更新、删除、改键，以及两到四个变更的事务；流水线启动前 200 行（它的快照），之后在 4 次杀进程中写 500
   个事务（每晚 1,000 行、2,500 个事务、10 次杀进程），相隔二到十毫秒。MongoDB 的负载没有事务（客户端不支持会话），改键是一次
   删除加一次插入，Kafka 中的 Debezium 事件也是如此。类型由各连接器的测试覆盖。
4. **杀进程**与各连接器测试相同：崩溃钩子在随机的点和次数触发，第一次运行一定被杀，最后一次运行到底。
5. **矩阵发现：先快照后流的源在第一个检查点之前被杀会丢变更。** PostgreSQL、MySQL 和 MongoDB（以及同样形态的 TiDB）在没有
   检查点时重新启动，会做新的快照并从它开始流，两次运行之间的变更永远读不到；第一次运行已写入目标、其间又被删除的行会一直留在
   目标中（每个目标都出现，每次运行几行）。**已修复**（2026-11-09），由运行时保存源的起点：`fitsync.Run.SaveStart(p)`，sync 包中
   小而增量的 API（运行时为非作业的流水线填上 `Run.Start`，试运行没有），没有检查点的源在第一条记录之前用它给出重新启动的运行
   应当从哪里继续的位置；运行时立即把它保存为检查点（经过崩溃点 `checkpoint_written` 和 `checkpoint_saved`，不告诉 `Confirmer`）。
   PostgreSQL 给出导出点上、没有完成任何块的快照状态，于是重启时从新快照重读每张表，并从第一个点开始流（检查点落在快照中时
   本就走这条路，槽从未确认到该点之后）；MySQL 给出 binlog 起点和空的快照位置；MongoDB 给出起始令牌、快照尚未开始；TiDB 给出
   快照的 TSO，在同一 TSO 重读（在 `tidb_gc_life_time` 之内，否则以已有的 “older than the GC safe point” 失败）。在较新的快照上
   重放的流在按键写入的目标中收敛，每个键的最后一次变更最后到达。矩阵在每个源的第一次运行的第一个检查点之前杀掉它（该次运行的
   `checkpoint_interval` 为一小时，在随机一次写入时杀），去掉了 `settle` 和 `FIT_SYNC_MATRIX_EARLY`；禁用 `SaveStart` 时它失败，
   启用时通过。备选：每个源从服务器保留的最早位置开始流（PostgreSQL 槽的已确认 LSN 只对比运行活得久的槽成立，MySQL 和 MongoDB
   没有这样的标记：最旧的 binlog 或 oplog 条目会重放太多，或已不在）；源在有记录被确认之前什么都不保存（即这个缺陷）。
6. **矩阵发现：ClickHouse 的默认值敌不过快照。** 按提交时间做版本（默认）时，没有提交时间的记录（快照的行）用写入时间，于是
   杀进程后重读的行会压过之后重放的、更早提交的变更。**已修复**（2026-11-09）：这样的记录版本为 0，低于任何变更；同一版本的行
   保留最后写入的，与 `ReplacingMergeTree` 的做法一致，因此完全没有提交时间的源仍按到达顺序写入。标记删除本就带有其提交时间的
   版本：看到的 “没有版本” 来自 `version_field`，只有键的删除没有它。矩阵的 ClickHouse 目标现在用默认值
   （`ReplacingMergeTree(_version, _deleted)`、标记删除、用 `FINAL` 读取）。遗留：同一版本的平局（一个事务两次修改同一个键、
   MariaDB 的秒级提交时间）按写入顺序决定，被去重的重放可能弄错；这些情况仍用 `version_field`。备选：用位置做版本（同一个源的
   不同位置种类之间无法比较），快照记录带上快照的时间（只有 TiDB 的有；PostgreSQL 的导出点没有时间）。
7. **没有 Docker Compose 文件**：服务器由 `test/sync/run.sh` 及其辅助脚本启动，和所有互通测试一样，现在按族划分（`SYNC_FAMILIES`：
   search、mysql、postgres、mongo、kafka、tidb、matrix），每族在 CI 中是一个单独的任务、在自己的运行器上并行运行，`-timeout` 和
   `timeout-minutes` 按其大小设定；`matrix` 每种服务器启动一个，不含 OpenSearch、Valkey、S3、Redpanda 和单机 TiDB。`SYNC_PREFIX`
   和端口变量允许在第一套旁边再运行一套。备选：Compose 文件，即对同一批服务器的第二份描述。
8. **每次推送都运行，不只是触及 `sync/` 的**：遵守 v1.7.1 的规则（不跳过任务），只用一个工作流；完整矩阵（每个源在同类的每个
   服务器上，包括 Redpanda）在 `sync-matrix.yml` 中于 UTC 02:41 运行，也可手动触发，每个源族一个任务。备选：路径过滤，会跳过任务。
9. **TiKV 需要 123,880 个打开文件**：CI 运行 42504979157 在 `tidb.sh` 失败，因为 TiKV 拒绝运行器的 65,536，TiDB 随之放弃；TiKV
   加上 `--ulimit nofile=1048576`，TiDB 和 TiCDC 加上 `--restart on-failure`。
10. **规模**：约 1,100 行测试，run.sh 按族重组（约 150 行改动），`kafka.sh`（`REDPANDA=0`）、`mongo.sh`（`MONGO_VERSIONS`）和
    `tidb.sh` 有小的增补；决定 5 和 6 随后已修复，因此没有 I192b。

### I193 基准测试

- **做什么**：在压测主机上：文件对比 Filebeat（每秒行数、CPU、内存），MySQL 到 Elasticsearch、MySQL 到 Kafka 对比 Canal 与
  Debezium，PostgreSQL 到 Kafka 对比 Debezium，MySQL 批量复制对比 DataX；附 profile，发现的开销予以修复或记录。
- **完成标准**：结果写入 `bench` 和带评分的报告（规则 24），并说明 fit 所处的位置。
- **涉及**：`bench`、`docs`。

**测试工具**（2026-11-09 搭建；主机运行、结果与报告尚待完成，因此 I193 仍为 todo）：
[`bench/sync`](../bench/sync/README.zh-CN.md)。`bench/sync/run.sh SCENARIO` 逐个运行 fit 与对手，每次面对全新的
服务器（场景 `files`、`mysql-es`、`mysql-kafka`、`pg-kafka`、`mysql-batch`），在 Docker 私有网络中、不占主机端口、
绑定 CPU（搬运者、驱动、服务器各占不同物理核），结束时全部清理；`bench/sync/plan.sh DATE` 将每个项目各运行 3 次，
写入 `bench/results/sync-DATE` 并生成 `tables.md` 与 `scores.md`。`bench/sync/syncbench`（Go，使用 fit 自己的协议
客户端）写入负载、在目标处计数、通过 Docker API 采样搬运者，并保存 fit 的 pprof CPU 与堆 profile；
`bench/cmd/benchreport` 渲染 `"kind": "sync"` 结果并评分（工作负载 S1-S5）。已在 macOS Docker（arm64）上以
`SIZE=smoke` 端到端冒烟测试；尚未在 Linux 上运行。

**待评审的决定**（搭建时做出，2026-11-09；在用户裁定前有效）：

1. **吞吐取自积压**：流场景写入时 `docker pause` 搬运者，写完再恢复，使其按自身速度排空积压，写入端不成为瓶颈；
   文件与批量的数据在搬运者启动前已就绪（由 syncbench 启动其容器）。延迟取自单独的固定速率稳定阶段（2,000 条/秒、
   20 秒），按每次写入的提交时间与目标计数匹配。备选：搬运者运行时写入（`PAUSE=0`，保留为选项）。
2. **只在目标处计数**：输出文件的行数、Elasticsearch `_count`（其 1 秒刷新计入所有搬运者的延迟）、Kafka 偏移量
   （每行一条消息）、Canal flat message 按 `data` 行数（Canal 将一个事务中同一表的行放进一条消息）、目标表
   `COUNT(*)`。重复计数的记录会报告但不计分。
3. **流的起点靠预热事务，直到目标计数上升**，计数稳定后作为基数：搬运者接入之前写入的行，Canal、Debezium
   （`no_data`）与 fit（`snapshot never`）都不会读取，固定的预热数会卡住。
4. **各搬运者用默认设置，仅在比较需要时对齐**：Filebeat 的 harvester 退避为 250ms、队列刷新为 1s（即 fit 的
   `poll_interval` 与批 `max_wait`）；fit 向 Kafka 写 Debezium 信封；每个 topic 一个分区；批量复制两侧均为 4 个读写
   并发、1,000 行一批；不用 TLS；服务器持久化同等放宽。JVM 堆保持各镜像默认（Canal 2-3 GiB、Connect 最多 2 GiB、
   DataX 1 GiB），RSS 如实反映。备选：统一堆大小。
5. **Debezium 到 Elasticsearch** 为 Debezium MySQL 连接器加 Confluent Elasticsearch sink 14.1.2，运行于同一
   Kafka Connect，Kafka broker 计为服务器而非搬运者；topic 在源端改名（`RegexRouter`），因为该 sink 不允许改 topic
   的 SMT。**Canal 到 Elasticsearch** 为 canal-server 加其 client adapter（由 1.1.8 发布包构建），两个容器都算搬运者。
6. **镜像**：固定标签（MySQL 8.0.42、PostgreSQL 17.5、Elasticsearch 7.17.28、Kafka 3.9.1、Filebeat 8.17.4、
   canal-server v1.1.8、Debezium connect 3.0.8.Final、Temurin 8u452）；Canal adapter、DataX 202309（1.7 GB 压缩包中只取
   引擎与 MySQL 插件）与 Confluent sink 按固定 sha256 下载并构建为本地镜像。用 MySQL 8.0 而非 8.4，因为 DataX 与 Canal
   的驱动需要 `mysql_native_password`。
7. **评分**：S1 文件 25%（四个项目：128 B 与 1 KiB 行、1 与 16 个文件），S2 MySQL→ES 20%，S3 MySQL→Kafka 20%，
   S4 PostgreSQL→Kafka 15%，S5 批量 20%；指标与 HTTP 计划相同（吞吐 50%、CPU 25%、内存 15%、延迟 10%），批量无延迟
   （55/25/20）。在 benchreport 中与 HTTP 计划并列，一个计划目录只属其一。
8. **冒烟运行中所见（规模太小，不可作结论）**：fit 低速率下的延迟就是其 sink 批 `max_wait`（1 秒）；fit
   PostgreSQL→Kafka 排空时 CPU 很低，Canal 与 Debezium 的 Elasticsearch 路径不到 1,000 行/秒且 CPU 很低，均需在主机
   规模下查看后再下结论。

**主机的首次运行**（2026-11-09，`bench/results/sync-2026-11-09`；Ryzen 7 7800X3D，16 线程，Docker 29.1.3）及其结果：

- **Canal 的 mysql-es 在 Linux 上每轮都失败**：其 client adapter 拒绝了整个 `es7` 适配器（SnakeYAML 报
  “unacceptable code point”），因而什么都没写。测试工具挂载的是 `configs/canal/es7` 整个目录，而 adapter 会加载其中每个
  文件：用 macOS `tar` 复制到主机时，`items.yml` 旁多了一个 AppleDouble 文件 `._items.yml`。已修复：镜像（现为
  `fit-syncbench/canal-adapter:1.1.8-2`）删去发布包自带的示例映射，run.sh 只挂载 `items.yml`；README 中的复制命令改为
  `COPYFILE_DISABLE=1 tar`。已在 Docker Desktop 上复查。
- **fit 的批量复制（S5）以 0.84 核达到 13.98 万行/秒，DataX 以 0.47 核达到 17.3 万**（fit 46 MiB，DataX 432）。CPU
  profile（`s5-mysql-batch/profiles-r*`）：`encoding/json.Marshal` 占 14.5%（每条记录都用反射序列化快照位置，7.5%；
  规范 JSON 的字符串写入函数 `appendString` 每个字符串都经过 `json.Marshal`，4%）；mysql sink 的 `InsertRows` 为每批
  准备语句占 12.9%，其中解析服务器发来的 4,000 个参数定义（1,000 行 × 4 列，无人读取）占 11%；GC 约 24%，多半由此而来。
  已修复并附测试与基准：协议客户端的 `Prepare` 读取参数定义而不解析（`skipColumns`；准备 4,000 个参数的语句分配由
  2.18 MB 降至 0.42 MB，余下为假服务器所致）；快照位置手工写出（`appendSnapPos`，由 239 ns、6 次分配降至 31 ns、1 次，
  与 `json.Marshal` 逐字节相同）；`appendString` 对纯 ASCII 直接写出（8 ns，无分配），其余仍走 `json.Marshal`，输出不变
  （导出为 `fitsync.AppendJSONString`）。未做、待评审：在同一连接上跨批缓存 InsertRows 的预备语句，可省去准备的往返与
  服务器的工作，但会改变客户端“不留下打开的语句”的约定。
- **S3（MySQL 到 Kafka）**：Debezium 编码器以 map 构建信封再用 `encoding/json` 序列化，占 fit CPU 的 35%
  （`kafka.(*Debezium).Encode`，连同行的纯 JSON 共 43%）。已修复：事件手工写入池化缓冲区（`appendDebezium`），字符串用
  `fitsync.AppendJSONString`，与 map 的输出逐字节相同：成员按排序后的顺序（after、before、op、source、ts_ms），数字、
  decimal、base64 字节、RFC 3339 时间与日期照旧，JSON 文档仍经 `encoding/json`，使其中的 `<`、`>`、`&`、U+2028、U+2029
  照旧转义。旧路径（`encodeMaps`）保留，处理快速路径交回的情形：NaN 或无穷浮点数（同样的错误）、不是 JSON 数字的
  decimal 或无法序列化的 JSON 文档（照旧略去该记录）、没有事件的操作。两者由覆盖所有值类型、null、嵌套 map 与数组、
  删除、键与命名空间的用例表，以及模糊测试（`FuzzDebeziumEncode`，500 万次无差异）约束为相同的字节与错误。
  `BenchmarkDebeziumEncode`（六列的更新，Apple M4 Pro）：由 2,500 ns、2,700 B、58 次分配降至 310 ns、384 B、1 次分配
  （即消息本身的字节）；写入复用缓冲区为 276 ns、零分配。编码器不写 schema 信封（从来没有）；解码两种都读。
  **待评审**：canal_json 的编码器同样是 map 经 `encoding/json`，但不在任何基准的路径上（S3、S4 中 fit 写 Debezium），
  在剖析显示它之前保持不变。
- **再看 S5：在等待而非计算**（2026-11-09）：d5db60fd 之后，主机上 fit 以 0.47 核达到 12.2 万行/秒，DataX 以 0.59 核
  达到 22 万。在本地查明（Docker Desktop，按基准方式运行的 MySQL 8.0.42，200 万行，`fit sync run --once`，采样两台服务器的
  进程列表，并单独探测源与目标）：
  1. **mysql 目标只有一个写入者。** 它不是 `ParallelSink`，作业的四个读块者喂给同一个连接：目标的进程列表里每次只有一条
     INSERT，单个写入者的上限是 13 万行/秒，正是主机的数字。源从来不是瓶颈：四个读块者单独读取为 141 万行/秒，写入者工作
     时它们闲着（`Sleep`）。
  2. **按键哈希分派的并行写入者在 B 树中争用。** 四个写入者、每条记录交给其键哈希到的写入者时，每批都含有正在读取的四个块
     中的键，写入者向同样的叶子页插入：16.1 万行/秒。单独探测目标，四个写入者、每批 1,000 行：连续键的批 24 万行/秒，同样
     的键在写入者之间交错 18.3 万（-25%）。
  3. **每批一条预备语句**（准备、执行、关闭：两次往返，加上服务器解析 4,000 个占位符）对比一条文本语句：端到端 +11%
     （20.4 万到 22.7 万）。
  4. 保持不变，已测量：`ON DUPLICATE KEY UPDATE` 相对普通 INSERT（DataX 的 `insert` 模式）让服务器多花约 8%，每批的
     START TRANSACTION 与 COMMIT 约 3%。
  5. **队列满时写入者干等 `max_wait`**，在写入者跟随读块者之后出现：`bench/sync/run.sh` 中 fit 只有 9.8 万到 10.4 万行/秒、
     0.21 到 0.25 核，两台服务器的进程列表显示所有连接都睡了好几秒。队列（10,000 条记录）只从最旧的记录起腾出空间；某个读块者
     落后时（每个块都要新开连接并读表定义），它的写入者手里不足 1,000 条、其中有最旧的那条，于是等批的 `max_wait`（1 秒）
     盼更多记录，而满了的队列放不进来，其他写入者早已写完自己的。

  已修复并附测试：mysql 目标成为 `ParallelSink`，每个连接的状态成为一个 `writer`，第一个给流，作业的写入者取备用的，各有
  自己的连接；作业的记录交给读取它们的读块者对应的写入者（`sync` 包中的 `Record.lane`；其他记录仍按键的哈希），使一个写入者
  的批是一个块中连续的键，与 DataX 的通道写入一样；目标的行以文本语句发送（`mysqlwire.InsertRowsText`，目标的连接对每次
  `InsertRows` 都用它），每个值写成代表 InsertRows 所发参数的字面量；源等待队列空间时，持有最旧记录的写入者不等批满就写
  （`sync` 包，`run.waiting`；任何流水线，一个写入者或多个）；快照的行与目标的语句花更少的 CPU（不再每个值复制列定义，每行
  少一个切片，列预先分配，转义用查表）。本地（Docker Desktop 的数字仅供参考）`fit sync run --once` 与修改前的代码交替运行
  （200 万行，三轮中的后两轮；队列修复之前）：修改前 12.0 万、13.1 万行/秒，修改后 21.2 万、23.2 万。
  `RECORDS=2000000 bench/sync/run.sh -n 2 mysql-batch` 包含全部修改：fit 21.4 万、24.0 万行/秒，0.47、0.54 核（57 MiB），
  DataX 23.0 万、22.4 万，0.50、0.46 核（443 MiB）；队列修复之前同样的运行 fit 为 10.4 万、9.3 万。`BenchmarkInsertRowsText` 对比 `BenchmarkInsertRowsPrepared`（sysbench 形状的 1,000 行一批，假
  服务器回 OK）：每批 13 次分配对 24,032 次。

  **待评审的决定**（2026-11-09）：
  1. **用带字面量的文本语句，而非按连接缓存预备语句。** 缓存也能省去准备的往返与解析，但会在服务器上留下打开的语句（每种
     批宽一条，各占服务器内存，受所有客户端共享的 `max_prepared_stmt_count` 限制），还要处理缓存的淘汰与重连后丢失的语句；
     文本两者都不需要，保住客户端“不留下打开的语句”的约定，也正是 JDBC 的 `rewriteBatchedStatements` 为 DataX 发送的形式。
     每个值按参数的含义写出：整数为数字，浮点数为带指数的最短形式（DOUBLE，与二进制协议所发相同；单独的 `0.1` 是
     DECIMAL），字符串、字节与 JSON 为连接字符集的带引号字符串（二进制协议对三者都这样发送；go-sql-driver 给字节写的
     `_binary` 字符串会被 JSON 列拒绝，3144），时间为与二进制形式等长的 DATETIME 文本，时长为 TIME 文本；引号按会话的
     `sql_mode` 转义（`NO_BACKSLASH_ESCAPES` 取自服务器状态标志 0x0200）。逐字节转义只在多字节字符中绝不藏有 `'`、`"`、
     `\` 的字符集中精确：utf8mb4（客户端所用）、utf8mb3、latin1、ascii 与 binary；其他排序规则的连接（GBK、Big5、SJIS 等）
     照旧发送参数。`TestMySQLTextInsertsMatchPrepared`（test/sync）把每种列类型、极值/null/零值的行、17 MiB 的
     LONGBLOB、每个字节值与每个需转义的字符两种方式各写一遍，在 `''`、`NO_BACKSLASH_ESCAPES` 与严格模式下写入 MySQL 5.7、
     8.0、8.4 与 MariaDB 10.11、11：两张表读回逐字节相同。
  2. **目标的写入者**：第一个写入者属于流，所以流跨批保持打开的事务不受影响；作业的备用写入者在 Write 重叠时创建，由
     Cleanup 关闭。作业的记录没有源事务，所以每次 Write 都提交它写的内容，没有写入者把事务带进下一次。作业中用
     `checkpoint sink` 时每个写入者把作业的位置写到同一行；作业只用本地文件（I191），那一行在作业中仅供参考。
  3. **按读块者分道**，在 `sync` 包中（共享代码的改动，已更新决定 036 第 2 条）：只读一次的键交给一个写入者；若某个变换
     由两个块的行生成同一个键的记录，它们由两个写入者写入，而并行读取的两个块本来就没有顺序。
  4. **源等待空间时不等批满就写**，只由持有最旧记录的写入者写，其他写入者的批照旧写满；流也适用（`queue` 小于目标的
     `batch.max_records` 时不再每次等 `max_wait`）。备选：每个写入者一个队列。
  5. **未做**：只有一条语句的批不加 START TRANSACTION 与 COMMIT（两次往返，本地约 3%；目标的事务规则与测试需要单独的路径）；
     保留 `ON DUPLICATE KEY UPDATE` 以保持幂等（恢复的作业会再写其块的行）；源的分页（每次查询 1,000 行，各在自己的一致性
     快照事务中，行复制两次）不变，在这里从来不是瓶颈。
- 需在主机上重跑：`s3-mysql-kafka`（Debezium 编码器）、`s2-mysql-es`（Canal）与 `s5-mysql-batch`（fit 的修复，现含并行
  写入者、文本语句与队列满时的写出）；其余项目保留（流只有一个写入者，总持有最旧的记录，10,000 条的队列也总能填满 1,000 条
  的批，所以这一修复不改变它们）。所有流与文件项目的延迟列均已
  填写，批量项目按设计没有延迟列。
- **重跑中的 `s2-mysql-es` 每轮都失败**（2026-11-09：积压排空后，目标停在 540,010 条中的 509,010 条长达 30 分钟，fit 没有
  日志）：主机磁盘已满 99%（458 GB 只剩 5.6 GB），Elasticsearch 日志写着 “flood stage disk watermark [95%] exceeded ...
  all indices on this node will be marked read-only”。只读的索引对每次写入回 429，目标按设计把它当作背压等待，只在 debug
  级别记录。不是 fit 的流水线：流的每个目标只有一个写入者（分道与队列满时的写出只在作业有多个写入者时起作用），同一构建在
  本地以完整规模通过该项。fit 中已修复：持续 `busyWarnEvery`（30 秒）的 429，无论整个 bulk 还是单个条目，都以警告记录集群的
  回答，持续期间每 30 秒再记一次（`TestBusyWarns`）。**主机上：先清理磁盘**（至少低于 ES 85% 的低水位）再重跑
  `s2-mysql-es`；磁盘满也危及其他项目的服务器。

### I194 同步的文档与示例

- **做什么**：`docs/sync.md` 及其中文版：概念、每个连接器的设置、保证，以及每个源对服务端的要求（binlog 设置、复制用户的权限、
  publication、副本集）、系统之间的类型映射、插件、运维；fit-examples 中的示例：文件到 Elasticsearch、MySQL 到 PostgreSQL、
  PostgreSQL 到 Kafka、一个 WASM 变换。
- **完成标准**：链接检查通过；每个示例都在 fit-examples 的 CI 中运行。
- **涉及**：`docs`、fit-examples。

**待审查的决策**（实现时所做，2026-11-09；在用户裁定前按此执行）：

1. **一份指南，`docs/sync.md` 及其中文版**，从 README 和配置页的 `sync` 一行链接过来：概念、每个连接器及一份能加载的 .conf
   配置、每个源对服务端的要求、类型映射、变换与编解码器、插件、作业、Admin API、`fit sync` 与工作台、指标、死信、运维和互通。
   每个字段的说明留在生成的参考中，指南链接过去而不重复。备选：每个连接器一页。
2. **fit-examples 中的四个示例**（`sync-files-to-elasticsearch`、带 `checkpoint: "sink"` 的 `sync-mysql-to-postgres`、
   `sync-postgres-to-kafka`、`sync-wasm-transform`），在那里的 `sync-examples` 分支上，未推送。「在 fit-examples 的 CI 中运行」
   理解为其测试中的 `fit validate`，与那里的每个示例相同；只有 WASM 示例不需要服务器即可运行（文件到 stdout，已用
   `fit sync run --once` 手工检查）。其余示例对真实服务器的运行属于 I192 的矩阵，或以后的 Compose 示例。备选：每个示例一个
   Compose 文件，在 fit-examples 的 CI 中运行。
3. **fit-examples 的测试把示例的密码变量**（`ES_PASSWORD`、`KAFKA_PASSWORD`、`MYSQL_PASSWORD`、`PG_PASSWORD`）设为替代值，
   因为 `fit validate` 像 `fit run` 一样读取 `password_env`；并在验证前**从 Go 源码构建 WASM 插件**（`GOOS=wasip1`）；4.5 MB 的
   `.wasm` 不提交（`.gitignore`）。备选：提交 `.wasm`，或 TinyGo 构建的更小的（I165b）。
4. **`sync-mysql-to-postgres` 自 I174 合并后与其他示例一样做验证**（已移出 `noValidate`）；fit-examples 的 go.mod 移到
   含 I174、I179 和 I185 的已推送 fit，sync-examples 分支随这次升级一起推送。
5. **MySQL 源的部分按 I174 和 I175 的规格写成**，早于它们合并，I192 的矩阵也尚未描述：两处在指南中都以 `MERGE CHECK` 注释
   （GitHub 上不显示）标出，供合并时对照合并后的代码检查。备选：合并前先不写。
6. **指南中的配置由 `TestConfigExamples` 加载检查**，该测试现在也读取 `sync*.md`（I174 合并后每个配置块都能加载；编写时还逐个用
   `fit validate` 检查过）。记录的规范 JSON 示例标为 `json record`，不被当作配置。MySQL 源的部分已对照合并后的 I174/I175 源及其
   schema 章节检查。
7. **规模**：英文指南约 1000 行，中文约 890 行；fit-examples 中四个示例和 30 行测试。计划的内容没有遗漏，只有决策 2 中对服务器的运行除外。

### 待审查的决策（2026-11-08）

规划时所做；在用户裁定前按此执行，I163 把它们写入决定 036。

1. **协议在 fit 中自行实现，不用驱动。** MySQL、PostgreSQL、MongoDB、Kafka、Redis 和 SigV4 在 `sync/*/wire` 中实现，如同 fib
   实现自己的协议，每一个都对照真实服务端测试。备选：在 fit 自己的接口之后使用 go-mysql、pgx、MongoDB 驱动和 franz-go——眼下
   工作量少得多，但违背设计原则 6，并带来数百个间接依赖包。
2. **至少一次加幂等目标；只在目标有事务时做到恰好一次（I191）。** 备选：在每个目标中记录已写位置，而多数目标无法原子地保存它。
3. **默认把检查点存为每条流水线一个本地文件**，而不是 `admin.store` 或目标中。因此一条流水线运行在一个 fit 上；迁移它就是迁移
   它的文件。备选：存进 store，使它随导出的 store 一起迁移。
4. **每条流水线一个检查点，取最慢的目标。** 慢目标会拖住其他目标的位置（但不拖住它们的写入，写入由队列限制）。备选：每个目标
   一条流水线，各自连接源。
5. **MySQL 快照不加锁、靠重放收敛**（I175），而不采用 DBLog 与 Flink CDC 的水位线算法；PostgreSQL 的导出快照本身就让交接精确。
6. **ClickHouse 和 Elasticsearch 作为源时按游标轮询**，看不到删除；它们没有可跟随的变更流。
7. **TiDB 的变更经 TiCDC 走 Kafka**，而不是 TiDB 旧的 binlog（Pump/Drainer，已弃用）。
8. **WASM ABI 先以 JSON 传递记录**，只有 I165 的基准测试显示 JSON 的开销超过 20% 时才加二进制形式；连接器插件获得 HTTP 和定时器，
   没有原始套接字。
9. **DDL 只报告，不重放到 SQL 目标**；目标中的 schema 由人创建（之后可以加一个打印建议 DDL 的 `fit sync schema` 命令）。
   Elasticsearch、MongoDB 和 Kafka 不需要 schema。
10. **顺序：** 先做核心、变换、WASM 和文件（I163–I167），然后是基于 HTTP 的系统（I168–I171，借助 fib 的客户端成本低），然后是
    数据库（I172–I183），然后是 Kafka、Redis 和 S3，最后是运维。同意，还是先做数据库？

### 同步：第二批连接器（2026-11-10 规划）

用户于 2026-11-10 从清单中选定：四种消息队列（RabbitMQ、NATS JetStream、Pulsar、MQTT）、SQL Server、Doris 与
StarRocks、日志投递（Loki、OpenTelemetry 日志）、时序库（InfluxDB、TDengine）与向量数据库（Milvus、Qdrant）。本次未选：
Oracle、Cassandra 与 ScyllaDB、DynamoDB、Iceberg、Snowflake 与 BigQuery、带 Schema Registry 的 Avro、syslog 与 GELF。
每一项沿用第一批的规则：协议在 fit 中实现（`sync/*/wire`，不引入驱动，决策 036 第 1 条），数据库与消息代理的客户端在各自
的 goroutine 上，基于 HTTP 的系统用 fib 的异步客户端，至少一次写入幂等的目标，用 `fitsync.Tester` 做连接测试，用针对假
服务端的单元测试满足 98% 的覆盖率门槛，在 Docker 中做互通并归入 `sync-interop` 自己的一组，每一节记录待审查的决策。

**待用户裁定**（来自 2026-11-10 的审查）：

- I220–I222 与规划：
  - **2026-11-10 已裁定，按建议。** **时序目标中的删除**（I220 决策 1）。选择：保持默认 `deletes skip`，或默认 `fail`，或真正删除（TDengine `mode sql` 用
    `DELETE ... WHERE ts =`，只有设置了 `time_field` 才能找到该行；InfluxDB 2 的删除 API 逐点调用）。建议：保持 `skip`，时序是
    历史；只在有需求时再加 TDengine `mode sql` 的删除。
  - **2026-11-10 已裁定，按建议。** **向量目标是否创建集合**（I221 决策 3）。选择：从不创建（现状），或加一个可选的 `create`，带维数、距离与主键类型。建议：
    保持不创建；索引与 schema 是 fit 无法从记录推断的选择。
  - **2026-11-10 已裁定，按建议。** **同步计划的权重**（I222 决策 8）。选择：保持 S1 20、S2 15、S3 15、S4 10、S5 15、S6 5、S7 7、S8 6、S9 7，或其他权重；以及
    时序与向量目标是否加场景（MQTT 到 InfluxDB 有 Telegraf 作为对手，向量目标没有）。建议：保持权重，在下一次计划运行时再把 MQTT
    到 InfluxDB 作为 S10 加入。**已裁定** 2026-11-10：保持权重，MQTT 到 InfluxDB 作为 S10 加入，占 5%，从 S1 拿出
    （现为 15%）；见决策 8。
- I217–I219：
  - **2026-11-10 已裁定：按建议（label 带上流水线世代）。** **Doris 的 label 比重置活得久**（I218 决定 1）：流水线重置或表重建后，在 `label_keep_max_second`（三天）之内重新导入的
    记录会被静默地以“已导入”拒绝，除非手动修改 `label_prefix`。选择：保持现状，或把流水线的 lineage 放进 label（告知每个
    目标 lineage，而不只是 `CheckpointSink`）。建议：用 lineage，这样重置会自动改变 label；`label_prefix` 保留。
  - **2026-11-10 已裁定：按建议（保留 Promtail，加入 Alloy）。** **Loki 基准的对比对象**（I219 决定 9）：已弃用并到了生命周期终点的 Promtail，或其后继 Grafana Alloy。建议：I222 把
    `files-loki` 加入 `plan.sh` 时，在 Promtail 旁边加上 Alloy。
  - **2026-11-10 已裁定：按建议（两者并存，名称模板也接受 `{$namespace}`）。** **两种模板语法**（I219 决定 11）：名称模板（SQL 目标与 doris 的 `table`、influxdb、tdengine、qdrant）把命名空间写作
    `{namespace}`；记录模板（loki、otlp_logs、file 目标）写作 `{$namespace}`，其中 `{namespace}` 是字段。选择：两种都保留，
    或统一为一种。建议：都保留，并让名称模板也接受 `{$namespace}`，这不会破坏任何配置。
- I211–I213
  - **2026-11-10 已裁定：按建议（加 `Fit-Key` 头）。** [I212](#i212-nats-jetstream-作为源与目标) 决策 6，NATS 消息上的键：RabbitMQ 连接器把记录的键放在 `fit-key` 头中，
    NATS 的完全不带，带键的 NATS 到 NATS 复制会丢掉键。建议：像 RabbitMQ 一样写入并读取 `Fit-Key` 头。
- I214–I216（40 项决定中 34 项保留，2 项修改，4 项待用户；修改的是 I216 的决定 5 与 12；下列为这 4 项及审查发现的三项未决问题）：
  - **2026-11-10 已裁定：按建议（自动 UNSUBSCRIBE 去掉的过滤器）。** **从配置中去掉的 MQTT 过滤器**（I214 决定 8，以及未实现的 UNSUBSCRIBE）：其消息仍在会话中到来并被交出。建议：对之前
    订阅过、现在不再列出的过滤器发 UNSUBSCRIBE，旧过滤器记在检查点中；不要对每条消息检查主题，EMQX 的主题重写会让它丢掉
    需要的消息。
  - **2026-11-10 已裁定：未按建议：遗嘱消息与增强认证都做。** **MQTT 遗嘱消息与增强认证**（I214 的未解决项）。建议：两者都先不做，直到所用的 broker 需要 AUTH（SCRAM、Kerberos）；
    同步客户端的遗嘱没有读者。
  - **2026-11-10 已裁定：按建议（`mssql.sh` 补证书，strict 互通进 CI）。** **TDS 8 `strict` 从未对真实服务器运行过**（I215 决定 2）。建议：给 `mssql.sh` 配上 TDS 8 要求的证书，并在 CI 中运行其
    互通；在此之前在指南中注明 `strict` 未经测试。
  - **2026-11-10 已裁定：按建议（源查询中转为 nvarchar）。** **代码页不是 1252 与 UTF-8 的 varchar**（I215 决定 5）：非 ASCII 值以字节到达目标。建议：源在查询中把这类列转为
    nvarchar（无依赖）；只有目标也要写入它们时才引入 `x/text`。
  - **2026-11-10 已裁定：未按建议：Entra ID 与 Kerberos 都做。** **Entra ID 与 Windows 认证**（I215 决定 12）。建议：若需要 Azure SQL，先做 Entra ID（服务主体的令牌）；Kerberos 其后。
  - **2026-11-10 已裁定：按建议（在 `sync` 中加入只含位置的记录）。** **冷表的检查点**（I216 决定 12）：它只随所读表的记录前进，因此在 CDC 清理的保留期之后重启会要求快照，尽管什么也没丢。
    建议：在 `sync` 中加入只带位置的记录，源在一个窗口中没有其表的记录时发出（MySQL 与 PostgreSQL 的过滤表也会用到）。
  - **2026-11-10 已裁定：按建议（保持至少一次，无需开发）。** **精确的快照衔接**（I216 决定 3，可选）：快照期间变更的行会再来一次，与 I175 一样。要精确，需把每批的 LSN 与键范围
    存入检查点，并对文本键使用服务器的排序规则顺序。建议：保持至少一次（幂等目标会吸收）；若仅追加的 Kafka 消费者需要
    精确，只对整数键这样做。

### I211 RabbitMQ 作为源与目标

- **做**：AMQP 0-9-1 客户端（`sync/rabbitmq/wire`）：连接与信道协商、TLS、PLAIN 与 EXTERNAL 认证、心跳、流控、
  `basic.qos`、`basic.consume`、`basic.ack` 与 `nack`、publisher confirms，帧大小有界。源 `rabbitmq`：以预取消费一个或多个
  队列，每条投递在所有目标都拿到后才确认（源的位置在代理处：检查点中不存，重启后未确认的消息被重新投递），记录中带头部与
  路由键；支持 quorum 队列与 stream 队列（stream 队列的偏移存入检查点）。目标 `rabbitmq`：按路由键模板发布到交换机，持久化，
  一批在代理确认后才被确认，消息 ID 取自记录的位置。
- **完成标准**：在 Docker 中与 RabbitMQ 3.13 和 4.x 互通；跨多次 kill 消费完一个队列不丢消息；stream 队列从其偏移续读；
  publisher confirms 在代理收到前一直持有这一批。
- **涉及**：`sync/rabbitmq`、`test/sync`、`docs`。
- **完成** 2026-11-10：`sync/rabbitmq/wire`（在 fit 中实现的 AMQP 0-9-1：协商、PLAIN 与 EXTERNAL、TLS、心跳、
  `connection.blocked`、`channel.flow`、`basic.qos`、消费、ack 与 nack、publisher confirms、`basic.return`，帧受 `frame_max`
  限制、消息体受 `max_message_bytes` 限制）以及 `sync/rabbitmq` 中的源与目标 `rabbitmq`；针对进程内代理的单元测试（覆盖率
  98.8% 与 98.5%）；`test/sync/rabbitmq.sh` 与 `TestRabbitMQ*` 在 Docker 中对 RabbitMQ 3.13 和 4.1 运行三次均通过：跨多次 kill
  复制一个 quorum 队列（3000 条消息，约 3100 次投递，无丢失，结束时源队列为空）、stream 队列从其偏移续读、经 TLS 的一批在
  publisher confirms 前一直被持有。尚未加入 `run.sh` 的分组与 CI（I222）。

**待审查的决策**（I211，实现时所做，2026-11-10；在用户裁定前有效）：

1. **[kept]**（2026-11-10：一个标签序列使一次 `basic.ack multiple` 覆盖所有队列；预取本就按消费者，每队列一个信道只增状态、不增吞吐。）**每个连接一个信道，每个源或目标一个连接**：源的各消费者（每个队列和 stream 一个）共用信道 1 及其投递标签，一次
   `basic.ack multiple` 即可覆盖所有队列直到流水线的水位。备选：每个队列一个信道；客户端中的信道池。
2. **[kept]**（2026-11-10：队列的位置在代理处，等检查点文件只会占住预取名额。它留下的缺口（没有延迟）已补上：源每 10s 以被动 `queue.declare` 查询各队列就绪的消息数，作为以 `messages` 计的延迟报告，`TestRabbitMQExternal` 在 3.13 与 4.1 上检查（bb76f5e9）。）**在水位（`fitsync.Acknowledger`）上确认，而非检查点保存后（`Confirmer`）**：`Acknowledged` 记下位置中的标签，由会话的
   goroutine 发送 `basic.ack multiple=true`；其他会话（位置中的运行 ID）的位置被忽略。队列的位置在代理处，无需等待本地
   检查点文件；早确认可释放预取。stream 队列的偏移也在检查点中，检查点尚未保存时从较旧的偏移重放（至少一次）。备选：在
   `Confirm` 时确认。
3. **[kept]**（2026-11-10：`Run` 结束即关闭会让每次重启都把在途消息重新入队。）**会话比 `Run` 活得久**：`Run` 返回后连接仍保持，使目标中仍在处理的记录的确认能到达代理；在下一次 `Run` 或 `Cleanup` 时
   关闭，余下的重新入队。备选：`Run` 结束即关闭（重复更多）。
4. **[kept]**（2026-11-10：单独确认在之后任何投递发出之前写出，不会与水位的批量确认竞争；占位记录白费一轮目标写入。）**不产生记录的投递立即单独确认**，stream 中早于所请求偏移的消息也一样，否则它会占用预取名额直到之后的批量确认。备选：
   占位记录。
5. **[kept]**（2026-11-10：AMQP 0-9-1 无法查询队列类型，stream 需要 `x-stream-offset`；分开列出明确且廉价。）**stream 队列单独列出（`streams`）**，不做探测：AMQP 0-9-1 无法在没有管理 API 的情况下查询队列类型。备选：`queues` 中加
   `stream:` 前缀，或 HTTP 管理 API。
6. **[kept]**（2026-11-10：`queue.declare` 的计数不含未确认与重投的消息且与生产者竞争；所有确认后的静默才是不丢消息的终点。）**作业在静默时结束**：`mode once` 下 `idle_end`（2s）内无消息、且所有已发出的记录都已确认时结束。队列没有终点，
   `queue.declare` 的计数不含未确认与重投的消息，且与生产者竞争。备选：消费到开始时读到的数量。
7. **[kept]**（2026-11-10：与 NATS、Pulsar 源的元数据同形；`fit-key` 让键在 RabbitMQ 到 RabbitMQ 的复制中往返，I212 决策 6 裁定后 NATS 的 `Fit-Key` 也是如此。）**消息元数据**：消息记录（raw 编解码器或编解码器的错误记录）在 `value` 旁有 `exchange`、`routing_key`、`queue`、
   `redelivered`、`headers`（按名排序的 map）和已设置的属性（`message_id`、`content_type`、`timestamp` 等）；编解码器生成的行
   只在 `metadata_field` 下带有它们。键取自目标写入的 `fit-key` 头（字符串与字节原样，其他键为纯 JSON），否则为 null。备选：
   以消息 ID 或路由键为键。
8. **[kept]**（2026-11-10：官方镜像没有代理端去重，稳定的 `message_id` 是目标能给消费者的全部；ID 现在还包含流水线名（I213 决策 12）。）**目标的头与 ID**：每条消息带 `fit-key`（记录有键时）、`fit-op` 与 `fit-namespace` 头，`message_id` 来自
   `fitsync.RecordIDs`，`timestamp` 为提交时间，除 `transient` 外投递模式为 2。RabbitMQ 没有代理端去重，重写的一批会再发布一次，
   消费者按 `message_id` 丢弃重复。备选：社区的消息去重插件（`x-deduplication-header`），镜像中没有。
9. **[kept]**（2026-11-10：默认大声失败是安全的一侧（丢弃即静默丢失），`unroutable: drop` 留给本就要丢弃的交换机。）**默认不可路由的消息为永久错误**：以 `mandatory` 发布，`basic.return` 以 `fitsync.Permanent` 使其记录失败，由 `on_error` 处理
   而不是无休止重试；`unroutable: drop` 不带 `mandatory` 发布。`basic.nack` 从其记录起失败并重试。超过 `max_message_bytes`
   （16 MiB）的消息体为永久错误。
10. **[kept]**（2026-11-10：最多等 `timeout` 再交给流水线重试，只有一条恢复路径；源有自己的连接，告警不会挡住它。）**流控**：`connection.blocked` 或 `channel.flow` 生效时，发布最多等待 `timeout`，之后这一批失败由流水线重试；源从不因此
    阻塞。心跳默认 30s（代理的更小时用代理的）；错过两次即结束连接。
11. **[changed]**（2026-11-10：EXTERNAL 现已对真实代理测试：`rabbitmq.sh` 启用 `rabbitmq_auth_mechanism_ssl`（`ssl_cert_login_from = common_name`、`verify_peer`）并生成客户端证书（CN=fit）；`TestRabbitMQExternal` 在 3.13 与 4.1 上经 TLS 以 EXTERNAL 写入并读取一个 quorum 队列，通过；脚本现在连同容器的卷一起删除（bb76f5e9）。集群故障转移仍未测试。）**互通服务器**：`test/sync/rabbitmq.sh` 运行 RabbitMQ 3.13 和 4.1，各一个节点，带用户与 TLS 监听；尚未接入
    `test/sync/run.sh`（I222 的 `mq` 分组）。未测试：对真实代理的 EXTERNAL（需要 `rabbitmq_auth_mechanism_ssl` 与客户端证书）、
    集群节点故障转移（每次拨号按顺序尝试各地址）。

### I212 NATS JetStream 作为源与目标

- **做**：NATS 客户端（`sync/nats/wire`）：文本协议、TLS、令牌、用户名与密码、NKey 与 JWT 凭据，在集群各服务器间重连；
  经其 JSON 主题调用 JetStream API。源 `nats`：持久的 pull consumer（可按需创建），消息在目标拿到后确认，流序号存入记录与
  检查点（consumer 丢失或重建时从检查点的序号开始）；也支持核心 NATS 主题，至多一次，用于遥测。目标 `nats`：JetStream 发布，
  `Nats-Msg-Id` 取自记录的位置，使流的去重窗口丢弃重发的一批；可选核心发布。
- **完成标准**：在 Docker 中与 nats-server 2.10 和 2.11（三节点集群）互通；跨多次 kill 和服务器重启复制一个流后两边相同；
  重发的一批只存一次。
- **涉及**：`sync/nats`、`test/sync`、`docs`。
- **完成** 2026-11-10：`sync/nats/wire`（在 fit 中实现的 NATS 文本协议：TLS、令牌、用户名与密码、以 ed25519 签名的 NKey
  与 JWT 凭据，在各服务器与 INFO 的 `connect_urls` 间重连、lame duck 模式、keep-alive；经其主题调用 JetStream API）以及
  `sync/nats` 中的源与目标 `nats`，进程内的假服务器与 JetStream 在 `sync/nats/natstest`（覆盖率 99.4% 与 97.8%，三者合计
  98.3%）；`test/sync/nats.sh` 与 `TestNATS*` 对 nats-server 2.10 和 2.11（各三个 JetStream 节点）通过：跨 6 到 12 次 kill 和
  一个节点重启，20,000 条消息恰好复制一次，各运行三次；重发的一批只存一次。第一次互通运行丢失约 15% 的流（下面的决策 2
  是修复）。尚未加入 `run.sh` 的分组与 CI（I222）。

**待审查的决策**（I212，实现时所做，2026-11-10；在用户裁定前有效）：

1. **[kept]**（2026-11-10：在水位处所有目标都已拿到消息；更晚确认只会占住 `max_ack_pending` 名额。）**源何时确认**：在 `fitsync.Acknowledger`（所有目标都拿到的水位）上，而非 `Confirmer`（检查点已保存）。两者之间崩溃会让
   consumer 领先于检查点，这是安全的：目标已有这些消息。`Acknowledged` 把工作交给源的确认 goroutine（不得阻塞）。备选：
   `Confirm`，确认更晚，待确认消息（max_ack_pending）更多而无益。
2. **[kept]**（2026-11-10：在 ack all 下这是保持位置单调的办法，配合决策 13 互通在多次 kill 中把 20,000 条消息恰好复制一次；显式确认加集合会让检查点不再是下限。）**fit 创建的 consumer 用 ack 策略 all，且当某次运行开始时发现它持有已投递未确认的消息（上次运行被 kill）时，从下限重建**：
   下限为检查点的序号与 consumer 确认下限中较晚者。JetStream 只在 ack_wait 之后才在较新消息之间重投这些消息；ack all 下确认
   较新的消息会把它们未读就确认掉（第一次互通运行因此丢失约 15%）。重建后按序投递，位置单调，ack all 安全且廉价。备选：
   ack 策略 explicit 加已发出序号集合（位置可回退，检查点不再是下限）；每次开始时等过 ack_wait。
3. **[kept]**（2026-11-10：fit 无法重建用户的 consumer，只有显式确认与服务器的下限安全；开始时报错胜过静默丢失。）**用户自己的 consumer（create_consumer 关闭）从其确认下限读起，而非检查点，且必须为 ack 策略 explicit**（none 为至多一次），
   ack 策略 all 在开始时报错：fit 无法重建它，其重投会乱序，只有显式确认和服务器自己的下限是安全的。备选：接受 all 并告警。
4. **[kept]**（2026-11-10：消息一到即读，没有拉取把一批压到 `max_wait`；nats.go 的 Fetch 也如此。）**一次拉取取走 consumer 现有的（no_wait，至多 batch），只有在没有时才以拉取一条等待至多 max_wait**：消息一到即读，而非整批
   拉取等到 max_wait（nats.go 的 Fetch 也如此）。备选：短过期的一次整批拉取；心跳（idle_heartbeat）——短拉取下不需要。
5. **[kept]**（2026-11-10：窗口属于流，由用户设定大小。ID 现在还包含流水线名（I213 决策 12），两条来源相同的流水线写入同一个流时不再把对方的消息当作重复丢弃。）**`Nats-Msg-Id` 来自 `fitsync.RecordIDs`**（记录来源的哈希；没有来源的源，为记录的锚定位置及与它的距离的哈希；见决策 12）：流的去重窗口（默认 2 分钟）丢弃重写的
   一批；更长中断后的重放会存下副本（至少一次）。窗口属于流，写入文档，fit 不设置。备选：按记录内容生成 ID（两条相同的记录
   会被丢弃）。
6. **[ruled 2026-11-10: done as recommended]**（2026-11-10：这是消息格式的选择：RabbitMQ 连接器写入并读取 `fit-key`，NATS 的什么都不写，带键的 NATS 到 NATS 复制会丢掉键。建议：像 RabbitMQ 一样写入并读取 `Fit-Key` 头。已实现：目标在 JetStream 与 core 发布中写 `Fit-Key`（与 RabbitMQ 相同的编码：字符串和字节原样，其他为普通 JSON），并像 RabbitMQ 写 `fit-op`、`fit-namespace` 那样写 `Fit-Op`、`Fit-Namespace`；头的一行容不下的值（CR、LF、两端空白）不写，而不是让记录失败。源在编解码器未设置键时读回 `Fit-Key`，与 RabbitMQ 一样不读 `Fit-Op`、`Fit-Namespace`：操作来自编解码器，命名空间来自主题或 `namespace`，不应被头覆盖；这两个头仍在消息元数据的 `headers` 中。有单元测试和 interop `TestNATSKeys`（NATS 到 NATS 的复制）。）**消息中没有键**：NATS 没有消息键；目标只发送 `Nats-Msg-Id`，源的记录除非编解码器设置否则没有键（`key` 变换可从字段生成）。
   删除与其他记录一样由编解码器编码。备选：写入再读回一个 `Fit-Key` 头——自创，别的程序不读。
7. **[kept]**（2026-11-10：与 RabbitMQ、Pulsar 源的元数据同形。）**消息元数据**：消息记录（raw、json plain、编解码器的错误）在其值旁有 `subject`、`headers`（map，多值为数组），以及来自
   JetStream 的 `stream`、`sequence`、`time`，编解码器生成的字段优先；其他编解码器生成的行只在 `metadata_field` 下带有它们。
8. **[kept]**（2026-11-10：core NATS 本就至多一次，其上的作业没有终点。报告的延迟（pending）现以 `messages` 而非 `offsets` 计（f43478c6）。仍未解决：`buffer` 满时的丢弃不在流水线状态中。）**core 模式至多一次且没有位置**：记录不带位置，检查点从不移动；core 模式的作业（mode once）报错，因为 core 主题没有终点。
   `buffer` 满时丢弃消息（客户端计数，尚未在状态中报告）。
9. **[kept]**（2026-11-10：不缓冲意味着失败可见并由流水线重试，目标由流的窗口兜底；nats.go 的缓冲会把它掩盖。）**重连**：连接断开、两个 PING 无应答或服务器进入 lame duck 模式时，客户端换到它所知（配置与 INFO 的 connect_urls）的下一个
   服务器；进行中的发布与拉取以 ErrDisconnected 失败（重连期间不缓冲），于是目标的批次被重写（去重），源的运行重启。备选：
   nats.go 的重连缓冲——以内存为代价掩盖失败。
10. **[kept]**（2026-11-10：INFO 之后升级 TLS 是 nats-server 的默认；`handshake_first` 只用于要求它的服务器，等有人要再加。）**TLS 在 INFO 之后升级**，与 nats-server 的默认一致；不支持 `handshake_first`（2.10.4 的 TLS 优先）。不接受 `tls://` 或
    `nats://` URL：服务器写成 host:port。
11. **[kept]**（2026-11-10：两个包的测试共用一个假服务器，而非两个各自漂移的。）**假服务器是一个包 `sync/nats/natstest`**，供 wire 与连接器的测试共用；它计入仓库的覆盖率（两包测试合计 98%）。
12. **[kept]**（2026-11-10：已由 `Origin` 修复（I213 决策 10-13）。）**在 I213 的互通中发现，这里同样成立**：`fitsync.RecordIDs` 只对在相同位置被再次读到的记录重复；`kafka` 与 `pulsar` 源在
    每个位置中写入所有分区的进度，按运行时交错的顺序，所以由它们供给的 NATS 流会再次存下重放的记录（至少一次）。修复属于
    `RecordIDs` 与这些源（I213 的决策 1）。**已修复** 2026-11-10（分支 `fix-recordid`，I213 的决策 1 与 10-13）：记录带有源设置的
    `Origin`，不论运行怎样交错各分区都不变，`RecordIDs` 先按它命名记录，再按位置；`kafka`、`pulsar` 源设置它，`nats`（流与
    序列号）也设置，所以由它们任一供给的 NATS 流在去重窗口内丢弃重放。
13. **[kept]**（2026-11-10：在 2.10 与 2.11 上每次投递（包括重投）都会递增 consumer 序号，缺口是可靠的信号；信任 info 正是失败的做法。）**在 sync2 的 CI 中发现（2026-11-10）：fit 创建的 consumer 投递了运行从未读到的消息时，重建它。**
    `TestNATSStreamAcrossKills` 在 nats-2.10 上丢失 20,000 条中的 236 条（复制了 19764 条）；本地约十次运行有一次丢失 46 到
    1,631 条，两个版本都有。与 `RecordIDs` 无关：每条丢失的消息都被发出或投递过，从未被发布，来源的 ID 也不冲突。两条路径，
    都以 ack all 下确认了较新的消息、从而把丢失的那些一并确认告终：(a) 节点重启后开始的运行从状态落后的 leader 读到 consumer 的
    info（没有待处理，已投递等于下限），保留了 consumer，随后从被 kill 的运行停下处起接收投递；(b) consumer 已向一次拉取投递后，
    该拉取因 leader 迁移得到 409，再次找到 consumer 后越过它们继续读。每次投递（包括重投）都会递增 consumer 序号（在 2.10 与
    2.11 上验证），所以源现在检查每次投递都紧接它取到的上一次，起点为开始时创建或找到 consumer 时 info 中的值；出现缺口时，或
    409 之后再次找到的 consumer 的已投递序号不是自己的时，从下限重建 consumer。备选：ack 策略 explicit（决策 2 的备选）；信任
    info（失败的做法）。**已修复** 2026-11-10（分支 `fix-recordid`，I213 的决策 1 与 10-13）：记录带有源设置的 `Origin`，`nats`
    也设置（流与序列号），所以由任一这样的源供给的 NATS 流在去重窗口内丢弃重放。

### I213 Apache Pulsar 作为源与目标

- **做**：Pulsar 客户端（`sync/pulsar/wire`）：二进制协议（其 protobuf 命令手工编码）、topic 查找与分区 topic、TLS 与令牌
  认证、批量、压缩（LZ4、zstd、zlib、snappy）。源 `pulsar`：按检查点中的消息 ID 对每个分区用 reader 读取（无需订阅状态），
  或按配置用持久订阅，在目标拿到后做累积确认。目标 `pulsar`：producer 的序列号取自记录的位置，配合代理去重，按键路由分区，
  批量发送。
- **完成标准**：在 Docker 中与 Pulsar 3.x 和 4.x standalone 互通；跨多次 kill 复制一个分区 topic 后两边相同；开启去重时重发
  的一批只存一次。
- **涉及**：`sync/pulsar`、`test/sync`、`docs`。
- **完成** 2026-11-10：`sync/pulsar/wire`（Pulsar 二进制协议，protobuf 手工编码：带重定向与代理的查找、分区元数据、producer、
  reader 与订阅、flow 许可、批量、LZ4、zlib、zstd 与 snappy、TLS 与令牌、keep-alive）以及 `sync/pulsar` 中的源与目标 `pulsar`；
  为此导出了 kafkawire 的 LZ4 块代码（`LZ4Block`、`LZ4Unblock`）；针对进程内代理的单元测试（覆盖率 97.8% 与 97.1%）；
  `test/sync/pulsar.sh` 与 `TestPulsar*` 对 Pulsar 3.0.11 和 4.0.5 standalone 运行五次均通过：两种模式下跨多次 kill 复制一个
  分区 topic，结果包含每条消息、每个键内有序，但含重放的副本（下面的决策 1：多分区源的位置）；位置可重复的源重发的一批只存
  一次。互通发现并修复了作业的终点丢掉一个批次最后一条的问题。尚未加入 `run.sh` 的分组与 CI（I222）。

**待审查的决策**（I213，实现时所做，2026-11-10；在用户裁定前有效）：

1. **[kept]**（2026-11-10：读回对目标是本地的，不需要 fit 的状态；有了 `Origin`，68 次 kill 运行中 65 次精确复制，其余至多 0.65%（被 kill 进程的最后一次发送），至少一次；事务需要协调器且提交要与检查点绑定。）**"重发的一批只存一次"如何成立**：每条消息带属性 `fit-id`（`fitsync.RecordIDs`，取自记录的位置）；`dedup read_back`（默认）时，
   目标在创建一个分区的 producer 时读回该分区最后 64 个条目（`GET_LAST_MESSAGE_ID` 之后在 `last - 64` 处的 reader），记下其
   producer 名存下的 `fit-id`（以及之后存下的每一个，每分区至多 100,000），并丢弃一批中 `fit-id` 已有的记录。producer 的序列号
   接着 `PRODUCER_SUCCESS` 的 `last_sequence_id`，使代理去重接受它们，只丢弃客户端自己重复的发送。原因：Pulsar 的代理去重按
   producer 名与递增的序列号，不透明的位置给不出；读回对目标是本地的，不需要 fit 的状态。局限：超过一个分区 64 个 Pulsar 批次
   的重放，或另一个 producer 写入之后的重放，会再存一次（至少一次）；需要消费权限。备选：Pulsar 事务（需要事务协调器，且提交
   要与 fit 的检查点绑定）；像 I191 那样的 `checkpoint: sink`；纯至少一次（`dedup off`，也提供）；本地文件记录每个位置的最后
   序列号。**互通中发现**：`fitsync.RecordIDs` 只在源以相同位置再次读到记录时重复。`kafka` 与 `pulsar` 源在每条记录的位置中
   写入所有分区的进度，按运行时交错的顺序，所以重启后再次读到的记录位置不同、`fit-id` 也不同：随机 kill 的 Pulsar 到 Pulsar
   复制有每条消息、每个键的首份按序，其后有重放的副本（TestPulsarAcrossKills 与 Kafka 测试一样检查这一点；观察到 0 到 22%
   的副本）。位置可重复的源（generate、数据库日志、文件）的一批只存一次（TestPulsarBatchTwice）。修复属于 `RecordIDs` 与多分区
   源两者，例如源可设置的逐记录标识（分区与偏移，或消息 ID），`RecordIDs` 优先于位置使用它。
   **已修复** 2026-11-10（分支 `fix-recordid`；下面的决策 10-13 待审阅）：`fitsync.Record` 有 `Origin`（`Stream string`、
   `N [4]uint64`），由源设置的固定结构；`RecordIDs` 对源、来源以及记录在转换由其源记录生成的记录中的序号做哈希，只有没有来源的
   记录才退回位置。`kafka` 设置主题、分区、偏移和记录在消息中的序号；`pulsar` 设置分区全名、ledger、entry、批内序号和序号。
   跨 kill 的 Pulsar 互通现在 68 次运行中有 65 次把 2000 条消息复制为 2000 条（Pulsar 3.0.11 与 4.0.5，两种模式）；其余三次有 2、
   4、13 份副本（136,000 条消息中的 19 条，单次最多 0.65%，修复前为 0-22%），来自此修复不涉及之处：被 kill 的进程的一次发送，
   代理在新生产者读回之后才存下。
2. **[kept]**（2026-11-10：Java 与 Go 客户端的默认，fit 的键与其他生产者的落在同一分区。）**按 Java 客户端的默认方式路由分区**：`JavaStringHash`（分区键的 Java `String.hashCode`，`& Integer.MAX_VALUE`，对分区数
   取模），pulsar-client-go 默认也用它；字节键按 Java 客户端发送时的 base64 计算哈希。没有键的记录每个目标批次去一个分区，下一批
   换下一个。备选：Murmur3_32Hash（Java 客户端的另一方案，C++ 客户端的默认又不同）。
3. **[kept]**（2026-11-10：批内序号级确认需要代理设置；读了一部分的批次其余部分只是被重读并跳过。）**源的位置是每个分区最后发出的消息**（ledger、entry、批内序号，一个条目读完时为 -1），以分区全名为键。reader 从它开始；
   读了一部分的批次会被代理重发，源跳过检查点已有的部分。`mode subscription` 中同样用这些 ID 过滤订阅重发的消息，并在开始时
   对检查点再做一次累积确认以防确认丢失；读了一部分的批次只确认到它之前的条目（不用 `ack_set`），其余部分重启后重来并被跳过。
   备选：批内序号级确认（`ack_set`），需要代理的 `acknowledgmentAtBatchIndexLevelEnabled`。
4. **[kept]**（2026-11-10：与 RabbitMQ、NATS 元数据一致；携带源的属性是要等人提出的功能。）**记录中的消息元数据**，与 RabbitMQ 和 NATS 源一致：消息记录（raw 编解码器或编解码器错误）在 `value` 旁有 `topic`（分区
   全名）、`message_id`、`properties`（map）、`producer`、`publish_time` 与 `event_time`；编解码器生成的行只在设置了
   `metadata_field` 时在其下带有它们。CommitTime 为事件时间，否则为发布时间。目标只写入 `fit-id` 属性；Pulsar 到 Pulsar 的复制
   不带源的属性。
5. **[kept]**（2026-11-10：报错胜过静默跳过；分块重组等有人要再做。）**不读取**：加密消息与分块消息（超过 `maxMessageSize` 被 producer 拆开的消息）使源报错而非被跳过；标记消息被跳过。备选：
   有界的分块重组，日后按需。
6. **[kept]**（2026-11-10：一条恢复路径，即流水线的，与 Kafka 连接器一致。）**客户端内部不重连**：连接丢失时其 producer 与 consumer 失败，源的运行或目标的写入返回错误；流水线的重试重新创建它们
   （目标的 producer 重新读回）。keep-alive 每 30s 一次 ping，60s 内未收到代理任何数据的连接被关闭。原因：只有一条恢复路径，
   即流水线的，与 Kafka 连接器一致。
7. **[kept]**（2026-11-10：在代理无视许可时约束内存。）**接收队列受 flow 许可约束**（`receiver_queue`，默认 1000，降到一半时补充）；代理超出许可两倍队列加 10,000 条时连接失败。
   帧受代理的 `maxMessageSize`（默认 5 MiB，来自 `CONNECTED`）加 10 KiB 限制，消息解压后的大小受 64 MiB 限制。
8. **[changed]**（2026-11-10：`pulsar.sh` 还启动 pulsar-auth（4.0.5）：只开 TLS 代理端口，令牌认证，HS256 密钥与角色 fit 的令牌由脚本生成；`TestPulsarTokenTLS` 检查不带令牌或不带 CA 的客户端被拒绝，目标（读回）与源（作业）两者齐备时可用；通过（915612a8）。尚未报告延迟：reader 的积压需要 `mode reader` 不发送的确认。）**互通服务器为不带认证的 standalone**：Pulsar 3.0.11 与 4.0.5，开启去重，在 127.0.0.1 的主机端口上发布（`test/sync/pulsar.sh`，
   与 `kafka.sh` 一样独立，尚未加入 `run.sh` 的分组，由 I222 加为 `mq`）。令牌认证与 TLS 只针对进程内假代理测试。备选：带
   `authenticationEnabled` 与脚本生成密钥的 standalone。
9. **[kept]**（2026-11-10：LZ4 块代码只有一份；移入 internal 包只是口味。）**LZ4 使用 kafkawire 的块代码**，导出为 `kafkawire.LZ4Block` 与 `LZ4Unblock`（Pulsar 的 LZ4 是原始块，大小在元数据中）；
   zstd 与 snappy 用 klauspost/compress（已是依赖），zlib 用标准库。备选：把 LZ4 代码移入两者共用的 internal 包。

`RecordIDs` 修复的决策（决策 1 的，2026-11-10，分支 `fix-recordid`；待审阅）：

10. **[kept]**（2026-11-10：固定结构，热路径无分配，ID 只需要相等比较。）**`Origin` 是 `Record` 上的固定结构，`Stream string` 与 `N [4]uint64`**，由源设置；`Stream` 非空即有来源。只比较相等，所以
    各源自选内容：`kafka` 主题 / 分区、偏移、消息内序号；`pulsar` 分区名 / ledger、entry、批内序号、序号；`nats` 流 / 序列号、
    序号；`rabbitmq` stream 队列 / 偏移、序号（classic 与 quorum 队列没有：再次投递的消息可能换了 tag）；`mysql` 事务的 GTID 或
    binlog 文件与偏移（其 `Tx.ID`）/ 记录在其中的序号；`postgres` `"lsn"` / 提交 LSN、变更序号；`mssql` CDC 事务的 LSN / 变更
    序号（变更跟踪没有：之后读同一版本范围会合并出别的变更）；`mongo` resume token。Kafka、Pulsar、NATS、RabbitMQ、MySQL、
    PostgreSQL 与 SQL Server 设置它不分配内存（用已有的字符串）；MongoDB 每条记录一个字符串。未设置：MQTT（没有坐标）、文件、
    快照与作业的分块（它们的位置已是逐记录的），以及未要求的源（Redis streams、TiDB、Elasticsearch、ClickHouse、Doris、S3、
    HTTP）。备选：每条记录构造一个字符串（热路径上的分配）；`func() []byte`（每条记录一个闭包）；由 `RecordIDs` 计算记录在消息
    中的序号（批次在消息内被切开时会变）。
11. **[kept]**（2026-11-10：源记录内的序号在批次切开时不变，距离做不到。）**转换由一条源记录生成的多条记录按序号区分**，即 `Record.sub`，管道在一条源记录生成多条时、在它们到达目标之前设置；它是
    ID 的一部分，所以批次在一条源记录的记录中间切开时 ID 不变。备选：像位置那样用到这段最后一条记录的距离（切开会让两条记录
    同一个 ID）。
12. **[changed]**（2026-11-10：两条流水线相同的来源（两个 Kafka 集群上同名的主题、两台 MySQL 上相同的 binlog 文件和偏移）写入同一个 NATS 流时 ID 相同，流的窗口丢掉了第二条流水线的记录。现在流水线在记录交给目标前设置其流水线名（未导出的 `Record.pipeline`），`AppendOrigin` 以它开头：流水线、源、来源、序号。改名的流水线本就从头开始（检查点按名字）；没有来源的记录的 ID 逐字节不变（f43478c6）。）**ID 是 `'o'` 加 `Record.AppendOrigin`**（源、来源的流与数字、序号）**的 SHA-256 的 32 位十六进制**，用栈上缓冲：一次分配，
    即字符串本身。没有来源的记录的 ID 逐字节不变，所以重放中途升级的管道只改变带来源的记录的 ID，一次。备选：来源的可读文本
    （更长，目标还得限制属性或头的长度）。
13. **[kept]**（2026-11-10：所有按记录命名的目标用同样的名字，随决策 12 现在也按流水线区分。）**其他按位置命名记录的目标也用来源**：`http` 的 `Idempotency-Key` 用每条记录的来源代替位置做哈希（长度的最高位置 1，没有
    来源的记录的键不变）；`doris` 的 label 用这段第一条和最后一条记录的来源定位；`s3` 的 `{position}` 取第一条记录的来源。来自
    Kafka 或 Pulsar、切法相同的重放于是重复同样的键、label 或对象名。Loki 不按位置命名任何东西，不变。备选：保持原样，那里
    至少一次。

### I214 MQTT 作为源与目标

- **做**：支持 3.1.1 与 5 的 MQTT 客户端（`sync/mqtt/wire`）：TLS、用户名与密码、keep-alive、持久会话、QoS 0、1、2、主题
  过滤器、MQTT 5 属性（用户属性、消息过期、主题别名）。源 `mqtt`：以持久会话订阅，QoS 1 与 2 的消息在目标拿到后才确认
  （`PUBACK`、`PUBCOMP`），主题作为命名空间，载荷经编解码器解析。目标 `mqtt`：按 QoS 发布到主题模板，可选 retain，在途消息
  数有界。
- **完成标准**：在 Docker 中与 EMQX 5 和 Mosquitto 2 互通；fit 被 kill 期间发布的 QoS 1 消息在其重启后全部投递；QoS 2 每条
  只投递一次。
- **涉及**：`sync/mqtt`、`test/sync`、`docs`。
- **已完成** 2026-11-10：包 `sync/mqtt/wire`（`mqttwire`）：`Dial` 与 `Config`（服务器、TLS、版本 3.1.1 或 5、用户名与密码、
  keep-alive、clean start 或带过期时间的会话、receive maximum、topic alias maximum、包大小与在途数上限）、`Subscribe`、
  `Messages`（以 receive maximum 为界的 channel）与 `Ack`（PUBACK，或 PUBREC 并在收到 PUBREL 后 PUBCOMP）、`Send`/`Publish`
  及可等待的 `Delivery`、主题名与过滤器的检查和匹配（含 `$share`）、双向的 MQTT 5 属性（用户属性、消息过期、content type、
  response topic、correlation data、双向主题别名），每个连接一个读 goroutine 和一个 keep-alive goroutine。包 `sync/mqtt`
  （`syncmqtt`）：源与目标 `mqtt`，注册于 `modules/standard`；schema、参考、模板、`schema-zh.js`；指南中两种语言的
  [MQTT](sync.zh-CN.md#mqtt)。针对进程内假 broker 的单元测试：两个包的语句覆盖率均为 100%，race 检查无误。互通
  （`test/sync/mqtt_test.go`、`test/sync/mqtt.sh`）在 Mosquitto 2.0.22 与 EMQX 5.8.6 上、3.1.1 与 5 下通过：流水线在随机点被
  kill 时以及停止期间发布的 QoS 1 消息在重启后全部到达（在 `written` 处 kill 后有重复）；QoS 2 消息每条只到一次，优雅停止时
  也是；目标的消息由订阅者读回，源以作业运行；经 TLS 的用户名与密码，错误密码被拒绝。互通发现 3.1.1 的 broker 会在 SUBACK
  之前发送会话中排队的消息，多于客户端 channel 的容量（已修复：源在自己的 goroutine 上订阅，同时接收消息）。
- **已完成** 2026-11-10，在对决定 8 与遗嘱、AUTH 未解决项的裁定之后：`mqttwire` 新增 `Unsubscribe`（两个版本的
  UNSUBSCRIBE 与 UNSUBACK，版本 5 的原因码，0x11 视为成功，0x80 及以上为 `ReasonError`）、`Config.Will`（主题、负载、QoS、
  retain，版本 5 中还有遗嘱延迟与消息属性），以及 `Config.Auth`，即 MQTT 5 的增强认证：CONNECT 中给出其方法名的
  `Authenticator`，交换 AUTH 包直到 CONNACK，由该方法校验其数据，连接期间可用 `Reauthenticate`（AUTH 0x19）重新认证；
  `NewSCRAM` 实现 SCRAM-SHA-1、SCRAM-SHA-256 与 SCRAM-SHA-512（RFC 5802、7677；`crypto/pbkdf2`），并校验服务器签名。连接器
  新增 `will` 与 `auth_method`；源的位置带有已订阅的主题过滤器（首次运行立即以 `SaveStart` 保存），会话恢复的运行在订阅之前
  先对检查点中有而 `topics` 中已没有的过滤器取消订阅。单元测试：`sync/mqtt` 语句覆盖率 100%，`sync/mqtt/wire` 99.9%（剩下的
  一行是 `pbkdf2.Key` 的错误，只有仅 FIPS 模式会返回），含 RFC 5802 与 RFC 7677 的向量，race 检查无误。互通
  （`TestMQTTWill`、`TestMQTTUnsubscribe`、`TestMQTTSCRAM`；`mqtt.sh` 另起一个带 SCRAM-SHA-256 认证器和用户 fit 的 EMQX
  节点）在 Mosquitto 2.0.22 与 EMQX 5.8.6 上、3.1.1 与 5 下通过，原有的 `TestMQTT*` 仍然通过：目标的连接被切断后，遗嘱带着
  版本 5 的属性到达订阅者，正常关闭后则没有；两次运行之间去掉的过滤器不再投递任何消息，无论是 fit 不在时为它排队的还是之后
  发布的，第三次运行表明会话中已没有它；SCRAM-SHA-256 能连接，源经由它读取，错误密码和 SCRAM-SHA-512 被拒绝（0x87），
  匿名客户端也被拒绝。EMQX 5.8.6 接受重新认证的完整交换后不发送成功的 AUTH：其 channel 卡在踢掉自己的会话上，5s 后断开连接
  （日志：`session_stepdown_request_timeout`）；客户端的重新认证在假 broker 上验证。

**待审查的决定**（实现时做出，2026-11-10；在用户裁定前有效）：

1. **[kept]**（2026-11-10：经两端复制的主题逐字节不变，且不必引入 sync/kafka；把 `Raw` 移入 `sync` 是重构，对用户没有收益） **不设编解码器时用源自己的形式**，而非 Kafka 的 `raw`：after 含 `topic`、`payload`（字节）、`qos`、`retain` 以及消息带有的
   版本 5 属性；目标不设编解码器时发布 after 的 `payload`，因此经两者复制的主题逐字节不变。原因：为 `Raw` 引入 sync/kafka
   会带入其客户端，而 MQTT 的元数据在记录中没有别的位置。备选：把 `Raw` 移入 `sync` 并作为默认。
2. **[kept]**（2026-11-10：同一主题的消息在目标的通道中保持顺序，正如保留主题只有一个值；编解码器仍可设置自己的键） **记录的键是其主题**，除非编解码器设了键：保留的主题类似压缩主题的键，同键的记录在目标的通道中保持顺序。备选：无键。
3. **[kept]**（2026-11-10：map 在各目标与编解码器中都自然易读，同名多次出现时保留每个值；只丢失不同名之间的顺序） **用户属性**为 map；同名多次出现时值为数组。备选：键值对数组。
4. **[kept]**（2026-11-10：留住 PUBREC 是崩溃时不丢 QoS 2 消息的唯一办法；互通显示 QoS 2 消息每条只到一次，优雅停止时也是） **QoS 2 在 PUBLISH 时交出，PUBREC 留到目标拿到后**，收到 PUBREL 时发送 PUBCOMP，已持有 ID 的重复 PUBLISH 被丢弃：每个
   连接一次，PUBREC 前崩溃则重投。备选：立即 PUBREC，崩溃时会丢消息。
5. **[kept]**（2026-11-10：免去重载时的重投；`Cleanup` 与下一次 `Run` 会关闭它） **连接比 `Run` 的 context 活得久**，直到 `Cleanup` 或下一次 `Run`，流水线排空时目标写入的记录的确认在 DISCONNECT 前发出，
   以免多余的重投。
6. **[kept]**（2026-11-10：MQTT 要求按 PUBLISH 到达的顺序确认（4.6），每个连接一个写者正好做到） **`Acknowledged` 只入队**；每个连接一个 goroutine 按顺序写确认；编解码器没有产生记录的消息随相邻消息一起释放。
7. **[kept]**（2026-11-10：作业需要结束点，其他队列源的作业也这样结束） **作业中**源在订阅后读到 `idle`（5s）内没有消息，等每条消息都确认后结束；之后的消息留给下一次运行。备选：拒绝 once 模式。
8. **[ruled 2026-11-10: done]**（2026-11-10：按建议裁定并已实现：位置带有已订阅的过滤器，会话恢复的运行对检查点中有而 `topics` 中已没有的过滤器取消订阅；见决定 16 至 18。此前：去掉的过滤器留在持久会话中，其消息照样交出。） **每次 `Run` 都重新订阅**（主题可能已变），版本 5 中 retain handling 为 1，使保留消息不在每次重启时重发；3.1.1 的 broker
   会重发，由幂等目标吸收。配置中去掉的过滤器在下次运行时被取消订阅。
9. **[kept]**（2026-11-10：两种方式的投递都是至少一次；持久会话需要每个目标一个稳定的 client ID 并处理 DUP，换不来更强的保证） **目标的会话是 clean 的，client ID 随机**（`fit-sync-<random>`），除非另行设置：投递反正是至少一次。备选：持久会话，以
   DUP 重发 QoS 2。
10. **[kept]**（2026-11-10：空的保留消息是 MQTT 清除主题的方式；有人需要别的做法时再加 `deletes` 参数） **`retain` 时的删除是一条空的保留消息**，它清除该主题；否则为编解码器的编码。备选：`deletes` 参数。
11. **[kept]**（2026-11-10：已对照版本 5 的 PUBACK 与 DISCONNECT 原因码：列出的这些重试也无法通过，0x80 以下的（0x10，无匹配订阅者）表示成功） **重试无法解决的拒绝立即交给 `on_error`**：版本 5 的原因码 0x87、0x90、0x95、0x99、0x9A 与 0x9B，超过 broker 上限的包，
    它不具备的功能，无效主题，编解码器的错误；超出配额（0x97）及其余的都会重试。
12. **[kept]**（2026-11-10：测试绝不能接管运行中流水线的会话；ACL 的注意事项已写入指南） **`Test` 用自己的 client ID 连接**（`<client_id>-test-<hex>`，clean 会话），因此不会接管运行中流水线的会话；源的还会订阅
    并检查授予的 QoS。按 client ID 绑定 ACL 的 broker 可能拒绝测试的 ID。
13. **[kept]**（2026-11-10：与其他队列源一样约束内存，且每项都是参数） **上限**：双向包大小 1 MiB，源的 `receive_maximum` 128（最多约持有 128 个包），目标的 `max_inflight` 64，keep-alive 60s。
14. **[kept]**（2026-11-10：集群各节点共享会话，依次尝试即可恢复它） **`servers` 是依次尝试的列表**，集群的各节点共享会话。
15. **[kept]**（2026-11-10：3.1.1 禁止这样做（MQTT-3.1.2-22）；在 provision 时拒绝，比 broker 拒绝更早说明原因） **版本 3.1.1 在 provision 时拒绝没有用户名的密码**，与其协议一致。

裁定之后做出的决定（2026-11-10），待审查：

16. **[2026-11-10 已裁定：保留]** **已订阅的过滤器保存在每个位置中**，检查点即最后确认的那个位置，流水线首次运行时立即保存（`SaveStart`）；去掉的过滤器在
    收到 UNSUBACK 之前也留在位置中，使先结束的运行下次再试。过滤器与检查点中不同的运行在订阅后每 200ms 提供一条只含位置的
    记录（`Run.EmitPosition`，I216 决定 14–16），直到流水线接受一条（在 `checkpoint_interval` 内没有记录时接受）或某条消息
    的记录带上它们：在没有消息的运行中加上又去掉的过滤器同样会被保存并取消订阅。（2026-11-10，合入只含位置的记录之后；此前
    这样的过滤器从未进入检查点，会留在会话中。）该位置的 run 与 seq 是最后一条消息的，因此确认它不会释放新的消息。备选：在
    `state_dir` 中、检查点之外另存一个源自己的文件。
17. **[2026-11-10 已裁定：订阅标识符]** **去掉的过滤器的消息**：broker 可能在
    UNSUBACK 之前、也可能（MQTT 允许，已排队的消息）在之后发送，由版本 5 的订阅标识符区分。`topics` 中每个过滤器以一个标识符
    订阅（SUBSCRIBE 属性 0x0B），各用一个 SUBSCRIBE，因为一个 SUBSCRIBE 只带一个标识符；broker 在 PUBLISH 上放上消息所匹配
    订阅的标识符（属性 0x0B，可重复，即 sync/mqtt/wire 的 `Message.SubscriptionIDs`）。位置在 `filters` 旁保存过滤器到标识符的
    映射（`ids`），使过滤器在各次运行间保持其标识符（恢复的会话所保留的消息带着订阅时给的那个），并保存下一个要给出的标识符
    （`next_id`）：新过滤器取新的标识符，标识符从不重复给出。去掉的过滤器的标识符在其 UNSUBACK 之前随它留在 `ids` 中；之后无需
    再记住，因为 fit 之前给出过（小于本次运行起始的 `next_id`）而 `topics` 中没有过滤器持有的标识符只能属于去掉的过滤器。消息
    带的标识符全是这样的标识符时，不论主题，被确认而不交出，因此 EMQX 的重写规则以过滤器不匹配的主题投递其消息时不会丢失（在
    带重写规则的 EMQX 5 上验证，`TestMQTTRewrite`）；带有 `topics` 中某个过滤器的标识符的消息照样交出。为某个之后去掉的过滤器
    排队的消息，即使其主题被替代它加上的过滤器匹配，也不交出（`TestMQTTUnsubscribe`）。原先的主题规则留给标识符无法判定的
    消息：不带标识符的（使用标识符之前所做的订阅，即从上一版本升级；不发送标识符的 broker）、带有 fit 未给出的标识符的（检查点
    丢失）、版本 3.1.1 的所有消息，以及 CONNACK 表明 Subscription Identifier Available 为 0（属性 0x29）的 broker 的所有消息
    （这样的运行不带标识符、以一个 SUBSCRIBE 订阅，其位置不含 `ids`，只含 `next_id`）。按主题规则，在该次运行中，主题匹配某个
    去掉的过滤器且不匹配 `topics` 中任何过滤器的消息被确认而不交出。wire 客户端接受 `Subscription.ID`（1 到 268435455，每次
    Subscribe 调用一个，3.1.1 或服务器不接受时拒绝：`SubscriptionIDsAvailable`），并把标识符为 0 的 PUBLISH 视为协议错误。
    Mosquitto 2 与 EMQX 5 都发送标识符，会话保留的消息上也有（`TestMQTTSubscriptionIDs`），并且把两个订阅都匹配的消息作为两份
    副本发送，每份带一个标识符（MQTT 5 允许服务器任选其一）：去掉的过滤器的那份不交出，而按主题规则，当 `topics` 中某个过滤器
    也匹配时它会被交出，成为重复。
18. **[2026-11-10 已裁定：保留]** **被拒绝的 UNSUBSCRIBE 使运行失败**，与被拒绝的 SUBSCRIBE 一样（版本 5 的 0x80 及以上）；0x11（订阅不存在）视为成功。
    未恢复的会话（`SessionPresent` 为 false）不取消任何订阅。
19. **[2026-11-10 已裁定：保留]** **`will` 是源与目标共用的一个对象**，负载为文本；fit 关闭连接（停止、重载、Cleanup）发送正常的 DISCONNECT，遗嘱随之作废，
    因此只有连接丢失或保活超时才发布遗嘱。遗嘱延迟向上取整到秒；会话先结束时更早发布，目标的会话是 clean 的，因此立即发布。
    备选：字节负载（base64）；停止时发送 DISCONNECT 0x04（带遗嘱）。
20. **[2026-11-10 已裁定：保留]** **增强认证只做 SCRAM**：`auth_method` 为 `SCRAM-SHA-1`、`SCRAM-SHA-256` 或 `SCRAM-SHA-512`，需要版本 5、`username` 与
    `password_env`；此时 CONNECT 带用户名但不带密码。不做通道绑定（`n,,`），不做 SASLprep（密码原样使用，对 ASCII 无差别），
    服务器给出的迭代次数最多 2^20，服务器签名错误则结束连接。EMQX 5 只提供 SHA-256 与 SHA-512；SHA-1 留给其他 broker，由
    RFC 5802 的向量和假 broker 验证。
21. **[2026-11-10 已裁定：保留]** **重新认证只在线协议客户端中提供**（`Reauthenticate`）；连接器不按定时器重新认证：目前没有需求，且 EMQX 5.8.6 在重新认证
    的交换之后会断开连接（见上）。备选：`reauth_interval` 参数。
22. **[2026-11-10 已裁定：保留]** **不做基于 MQTT AUTH 的 Kerberos（GSSAPI）**：broker 很少提供，且需要一个 krb5 实现；正在为 SQL Server 编写的实现以后可以
    承载它。备选：现在就做。
23. **[2026-11-10 已裁定：保留]** **SCRAM 互通在单独的 EMQX 节点上运行**（`${PREFIX}emqx-scram`，端口 29456）：EMQX 的认证器会拒绝它无法判断的客户端，
    包括匿名客户端，共用节点会让其他测试失败。EMQX 每个认证器只有一种 SCRAM 算法，因此互通覆盖 SCRAM-SHA-256；SHA-512 在
    那里验证为被拒绝。

由此留下的问题：**[ruled 2026-11-10: done]**（已实现：遗嘱消息、UNSUBSCRIBE，以及以 SCRAM 进行的增强认证与重新认证；Kerberos
不做，见决定 22）增强认证（AUTH 包曾会结束连接）、遗嘱消息与 UNSUBSCRIBE 当时尚未实现；I222 加入家族之后
`test/sync/run.sh` 才会运行 `mqtt.sh`。

### I215 SQL Server 客户端：TDS、认证与查询

- **做**：包 `sync/mssql/wire`：TDS 7.4 prelogin、TLS（按 SQL Server 在 TDS 内部的协商方式）、SQL 认证、批与 RPC 调用
  （带类型参数的 `sp_executesql`）、包含各种列类型的结果集（包括 `datetime2`、`datetimeoffset`、`decimal`、
  `uniqueidentifier`、`varbinary(max)`、`xml`、`nvarchar(max)` 的 PLP 流）、批量导入（`INSERT BULK`）、事务、取消（attention
  包）、每次调用都有超时。
- **完成标准**：在 Docker 中与 SQL Server 2019 和 2022（Linux 镜像）互通：每种类型往返一致；被取消的查询在服务端被取消；
  服务端在结果中途关闭连接时返回错误而不是挂起。
- **涉及**：`sync/mssql/wire`、`test/sync`。
- **已完成** 2026-11-10：包 `sync/mssql/wire`（`mssqlwire`）：`Dial`（prelogin，加密方式 `on`、`strict` 或 `off`，
  使用 SQL 认证的 LOGIN7）、`Exec`、`Query` 与 `QueryAll`（一个批，或经 `sp_executesql` 带类型参数：
  `Param{Name, Value, Type, Output}`、`ParseType`）、`Call`（存储过程，及其返回状态与输出参数）、`Rows`（逐行读取结果集，
  `NextResultSet`，`Close` 取消其余部分）、`BulkInsert`（`INSERT BULK` 与 BULK LOAD 包，使用表自身的列类型，支持提示）、
  `Begin`、`Commit` 与 `Rollback`（ENVCHANGE 给出的事务描述符放进每个请求的头部），调用的 context 结束时以 attention
  取消，`Config.Timeout` 约束每次等待与写入。针对进程内假服务器的单元测试（prelogin 内的 TLS 与 TDS 8 的 TLS，每种类型
  双向，每种类型与 token 的每个截断前缀，attention 的应答迟到、永不到来或在应答之后，服务器在结果中途关闭或沉默）：语句
  覆盖率 98.0%，race 检查无误。互通（`test/sync/mssql_test.go`、`test/sync/mssql.sh`）在 SQL Server 2019 与 2022（Linux
  镜像，amd64 模拟运行）上通过：每种类型作为参数写入并读回，服务器生成的字面量，`encrypt` 为 on 与 off，错误密码，
  含 max 类型与 text 的 5000 行批量导入，回滚与提交的事务，被取消的 `WAITFOR`（服务端不留请求，连接继续可用），提前关闭
  的结果，以及结果中途被 KILL 的会话返回错误而不是挂起。
- **裁决已实现**（2026-11-10）：TDS 8 `strict` 在 Docker 中对 SQL Server 2025 运行（2022 的 Linux 镜像拒绝它），纳入 CI 的
  mssql 组；源把其他代码页的 varchar 读为 nvarchar；Entra ID（FEDAUTH 与 `EntraClient`）、Kerberos（`krb5`，在 fit 中实现）
  与 NTLMv2 的 Windows 认证、参数 `auth`。单元测试：`mssqlwire` 98.0 %、`krb5` 97.5 %、`syncmssql` 98.0 %。

**待审查的决定**（实现时做出，2026-11-10；在用户裁定前有效）：

1. **[kept]**（2026-11-10：源与目标一次只发一个请求，与 MySQL 和 PostgreSQL 客户端一样；MARS 会增加无人使用的状态） **包及其形态**：`sync/mssql/wire`，包名 `mssqlwire`，一个供单个 goroutine 使用的阻塞式 `Conn`，与 MySQL 和 PostgreSQL
   客户端一致（决定 036）：无连接池、无 MARS、一次一个请求。备选：在一个连接上用 MARS 会话，源与目标都不需要。
2. **[ruled 2026-11-10: done]** （2026-11-10：已实现：`mssql.sh` 按 Linux 上 SQL Server 的要求制作证书，并为 TDS 8 启动 SQL Server 2025；`TestMSSQLWireStrict` 在其上通过，使用 TLS 1.3，`protocol_version` 为 0x08000000，证书按脚本的 CA 校验，CI 的 mssql 组运行它；客户端无需修改。2022 的 Linux 镜像无论证书如何都会在 ServerHello 之后关闭 strict 连接（错误 17821，microsoft/mssql-docker#878）；2019 没有 TDS 8。提交 `e0ec5fc9`；决定 13） **加密方式 `on`（默认）、`strict` 与 `off`**：`on` 为整个连接使用 TLS，在 prelogin 中协商、握手放在 TDS 包内，证书
   必须验证（没有"信任服务器证书"）；`strict` 为 TDS 8（先 TLS，ALPN `tds/8.0`）；`off` 完全不用 TLS，服务器要求加密时
   拒绝连接。不提供 TDS 的仅登录加密（密码加密、数据不加密）：看似加密实则不然。`strict` 通过单元测试但未通过互通：镜像
   在证书未按 TDS 8 的要求配置前拒绝它（`mssql.sh` 的证书尚未如此），因此其互通测试只在 `FIT_SYNC_MSSQL_STRICT=1` 时运行。
   备选：把仅登录加密作为第四种方式。
3. **[kept]**（2026-11-10：两种失败都由互通发现；上限保留调用方给出的 `TLS.MaxVersion`） **TDS 7.4 内的 TLS 为 1.2，每个包一个 record**：互通发现 SQL Server 会断开包被 Go 的动态 record 大小拆到多个 record 的
   连接，且 TLS 1.3 的大 ClientHello 放进 prelogin 包时握手偶尔失败；客户端关闭动态 record 大小，并在 prelogin 内把版本
   上限设为 1.2（若给了 `TLS.MaxVersion` 则保留），prelogin 包为 4096 字节。
4. **[kept]**（2026-11-10：每个值都精确保留，源可直接映射到记录；`*big.Rat` 会为原样往返的文本分配内存） **值的 Go 类型**：所有整数为 `int64`；`real` 为 `float32`，`float` 为 `float64`；`bit` 为 `bool`；`decimal`、`numeric`、
   `money` 与 `smallmoney` 为 `Decimal`（其精确文本）；`date`、`datetime`、`smalldatetime` 与 `datetime2` 为 UTC 的
   `time.Time`（其钟面时间，无时区）；`datetimeoffset` 为带其偏移固定时区的 `time.Time`；`time` 为自午夜起的
   `time.Duration`；`uniqueidentifier` 为按文本字节序的 `UUID`；char、nchar、text、ntext 与 xml 为 `string`（去掉 xml 的
   字节序标记）；binary、image、rowversion 与 CLR 类型为 `[]byte`；`sql_variant` 为其基础类型的值。备选：`time` 作为
   第一天的 `time.Time`，decimal 作为 `*big.Rat`。
5. **[ruled 2026-11-10: done]** （2026-11-10：已实现：源从 `sys.columns` 读取每列的转换长度（`COLLATIONPROPERTY(collation_name, 'CodePage')`），在快照、CDC 与变更跟踪的查询中把其他代码页的 varchar、char 与 text 选为 `CAST(col AS nvarchar(n|max)) AS col`；互操作 `TestMSSQLSourceCodePage`（2022、2019）中 Chinese_PRC_CI_AS 的文本以文本到达记录。提交 `78078483`；决定 14） **不加依赖的代码页**：UTF-8 排序规则或代码页 1252（拉丁排序规则，SQL Server 的默认）的 varchar 会被解码；其他代码页中
   含非 ASCII 字节的值以 `[]byte` 返回，varchar 参数拒绝其代码页没有的字符。原因：fit 没有 `golang.org/x/text`，而多数
   模式对非拉丁文本使用 UTF-16 的 nvarchar。备选：用 `x/text` 支持所有代码页；或由源在查询中把这类列转为 nvarchar。
6. **[kept]**（2026-11-10：`datetimeoffset` 列保留时刻；`datetime2` 或 `datetime` 列取值自身的钟面时间（SQL Server 丢弃偏移），而目标发送的是 UTC（`AsTime().UTC()`），因此这类列得到 UTC 的钟面时间，决定 4 读回的正是同一时刻。`datetime2(7)` 会无谓地丢掉 `datetimeoffset` 列的偏移） **由 Go 类型决定参数类型**（`Param.Type` 未指定时）：整数 `bigint`，`float32` `real`，`float64` `float`，`string`
   `nvarchar(4000)` 或 `nvarchar(max)`，`[]byte` `varbinary(8000)` 或 `varbinary(max)`，`time.Time` `datetimeoffset(7)`
   （无论列类型都保留时刻），`time.Duration` `time(7)`，`Decimal` `decimal(38, 其小数位)`，`UUID` `uniqueidentifier`，nil
   `nvarchar(4000)`。备选：`time.Time` 作为其 UTC 钟面时间的 `datetime2(7)`。
7. **[kept]**（2026-11-10：互通发现服务器拒绝另一种做法（错误 4804）） **PLP 值以未知长度发送**，单个 chunk：互通发现批量导入拒绝已知长度的 PLP（错误 4804），而参数可以接受。
8. **[kept]**（2026-11-10：互通发现服务器拒绝这些类型自身的行格式；拒绝 `sql_variant` 胜过静默转换） **批量导入读取表的列类型**（`SELECT TOP 0`，以及从 `sys.columns` 读排序规则，在 `INSERT BULK` 中写为 `COLLATE`），并把
   text、ntext、image 与 xml 列作为 `varchar(max)`、`nvarchar(max)`、`varbinary(max)` 与 `nvarchar(max)` 发送，由服务器转换：
   互通发现服务器拒绝这些类型自身的行格式。拒绝 `sql_variant` 列；列无法接受的值返回错误，导入继续、该行不写入。不提供
   `KEEP_IDENTITY` 提示（标识值由服务器生成，除非调用方设置 `IDENTITY_INSERT`）。
9. **[kept]**（2026-11-10：TDS 消息不能截断，10 秒上限让失去响应的服务器无法卡住调用） **取消**：调用的 context 结束时发送 attention；其应答最多等待 10 秒，之后关闭连接。请求仍在发送时 context 结束则关闭
   连接，因为 TDS 消息不能截断。与应答交错的 attention 在下一次调用前读完。`Rows.Close` 在结尾前以同样方式取消其余部分。
10. **[kept]**（2026-11-10：调用方按自己的节奏读取长结果时，只要服务器还在发送就不能超时） **超时**：`Config.Timeout`（60 秒）约束连接与登录、每次等待服务器下一个包以及每次写入；调用的总时长由其 context 决定，
    因此调用方按自己的节奏读取长结果时，只要服务器还在发送就不会超时。
11. **[kept]**（2026-11-10：两种做法都需要 ENVCHANGE 给出的描述符；SQL 语句让隔离级别与错误由服务器在其位置报告） **事务经 SQL 完成**（`BEGIN TRANSACTION`、`COMMIT`、`ROLLBACK`，先设置隔离级别），ENVCHANGE 给出的描述符放进每个请求的
    ALL_HEADERS，不使用 TDS 的事务管理器请求。备选：TM_BEGIN_XACT 等。
12. **[ruled 2026-11-10: done]** （2026-11-10：已实现：Entra ID（LOGIN7 的 FEDAUTH，安全令牌方式；基于 net/http 的 `EntraClient`：客户端密码、证书断言、托管标识，或来自环境变量或文件的令牌）、经 SSPI 的 Windows 认证（SPNEGO 中的 Kerberos，`sync/mssql/wire/krb5`，在 fit 中实现）与 NTLMv2、Azure 的 routing ENVCHANGE 跟随一次、源与目标的参数 `auth`。互操作：Kerberos 对 MIT KDC 与 SQL Server 2022（票据被接受，随后因没有 AD 而拒绝登录）；Entra ID 与 NTLM 只对假服务测试。MARS、Always Encrypted、UTF-8 功能扩展与表值参数仍不包括。提交 `35523fbe`、`07036415`；决定 15 到 19） **未包括**：Windows、Kerberos 与 Entra ID 认证，Azure 的 routing ENVCHANGE（忽略），MARS，Always Encrypted，UTF-8 功能
    扩展，表值参数。
13. **[2026-11-10 已裁定：保留]**（2026-11-10） **`strict` 在 SQL Server 2025 上测试，单独一个服务器**：`mssql.sh` 在 2022 与 2019 之外
    另启动只用于 TDS 8 的 2025（`MSSQL_STRICT`，默认开启，轻量矩阵中关闭），因为 2022 的 Linux 镜像拒绝 strict；其余测试
    仍在 2022 与 2019 上。2025 允许 TLS 1.2 与 1.3（strict 协商为 1.3）；2022 按微软指南保持 1.2，因为允许 1.3 时它的 TDS 7
    登录会挂起。证书放在属于 mssql 用户的卷中（700、600），CN 为容器主机名 `mssql.fit.test`。备选：所有测试也在 2025 上跑
    （CI 中第三次完整运行）。
14. **[ruled 2026-11-10: kept, with a warning at discovery]**（2026-11-10：已实现：发现表时以及之后接入新表时，对通过 CDC 读取的表，源从表自身的 `sys.columns` 读出排序规则代码页既不是 65001 也不是数据库代码页（`DATABASEPROPERTYEX(DB_NAME(), 'Collation')`）的 varchar、char 与 text 列，每次运行每列记录一次警告，写明表、列、两种排序规则，以及数据库代码页没有的字符经 CDC 以 `?` 到达目标。决定：排序规则不同于数据库但代码页相同的列不警告（CDC 保留其字符，互操作中的 Chinese_PRC_BIN 列即如此）；连接测试把同样内容报告为警告而不失败（`fitsync.WarningTester`、`TestResult.Warnings`，由 `fit sync test` 与工作台的测试卡片显示），管道状态不显示（它只含计数与错误，日志中已有）。互操作 `TestMSSQLSourceCodePage`（2022、2019）：fit_sync_cp936 中的 Latin1_General_CI_AS 列被警告，其 `cañón` 由快照完整读取，经 CDC 为 `ca?ón`。提交 `b89fc3d7`） **代码页转换**：排序规则代码页既不是 1252 也不是 65001 的 varchar、char 或 text 列转换为
    `nvarchar(n)`（n 为其字节长度，字符数不会超过它），超过 4000 字节及 `max` 与 text 用 `nvarchar(max)`；键列也转换（其值
    成为文本，由服务器与列比较），快照的表加别名，使键的顺序与条件仍作用于表自身的列。只有在数据库自身的排序规则下，CDC
    才保留这类列中的非 ASCII 字符（否则为问号，微软记载的限制）：写入文档，不做检测。备选：在客户端用 `x/text` 解码字节；
    在发现表时对排序规则不同于数据库的 CDC 列给出警告。
15. **[2026-11-10 已裁定：保留]**（2026-11-10） **Entra ID 只用安全令牌方式**：客户端在 LOGIN7 之前取得令牌（`Config.AccessToken`），来自
    服务主体（客户端凭据：密码，或证书的 RS256 断言并带 `x5t`）、托管标识（IMDS，设置了 `IDENTITY_ENDPOINT` 时为 App Service
    的端点）或每次连接时读取的环境变量或文件；在过期前五分钟内保留。不用 ADAL 方式（FEDAUTHINFO、交互或密码流程），同步服务
    没有用户来完成它们。带令牌时拒绝 `encrypt off`。现在跟随 Azure 的 routing ENVCHANGE 一次（Azure 内托管标识的连接会被
    重定向）。未对 Azure 测试：只有对假令牌端点与假 TDS 服务器的单元测试。
16. **[2026-11-10 已裁定：保留]**（2026-11-10） **Kerberos 在 fit 中实现，而不是 `github.com/jcmturner/gokrb5/v8`**：决定 036 第 1 条倾向
    在 fit 中实现协议，gokrb5 会带来七个模块（包括它自己的 asn1 分支）；fit 需要的密码学是 AES、HMAC-SHA1 与 PBKDF2（标准库
    与已经依赖的 `x/crypto`）。`sync/mssql/wire/krb5`：AS-REQ（密码加加密时间戳预认证，或 keytab）、TGS-REQ、SPNEGO 中带
    相互认证的 AP-REQ，连同测试用的接受方约 1750 行，按 RFC 3961/3962 的测试向量、MIT KDC 与 SQL Server 2022 检验。未包括：RC4 与 DES、UDP、
    用 DNS 查找 KDC、跨领域引用、续期、FAST、PKINIT、优先提供 MS-KRB5 OID。备选：gokrb5，覆盖更广（RC4、UDP、引用），
    代价是这些依赖。
17. **[2026-11-10 已裁定：保留]**（2026-11-10） **也提供 NTLMv2**（`auth method ntlm`）：SQL Server 对域外 Windows 账户常用它，基于
    `x/crypto/md4` 约 190 行；不提供 NTLMv1 与 LM，没有 MIC 与签名（TDS 不签名）。文档说明它较弱（服务器不被验证，交换可被
    离线猜测）。只对 MS-NLMP 的向量与假服务器测试：Linux 上的 SQL Server 不接受 AD 登录的 NTLM。备选：只有 Kerberos。
18. **[2026-11-10 已裁定：保留]**（2026-11-10） **参数 `auth`，源与目标共用的对象**（`method` 为 entra、kerberos 或 ntlm，加各方式的参数，
    其他方式的参数被拒绝），使 SQL 认证的参数保持原样；`username` 不再由 schema 要求，而由预配要求，`method entra` 除外。
    `Config.SSPI` 为每次登录生成交换（Kerberos 的每次登录有状态）。
19. **[2026-11-10 已裁定：保留]**（2026-11-10） **没有 Active Directory 的 Kerberos 互操作**：`mssql.sh` 构建 MIT KDC（`test/sync/krb5`）并把
    SPN 的 keytab 交给 SQL Server 2022：服务器接受票据、回答 AP-REP（客户端校验），随后拒绝登录（18452），因为 Linux 上的
    SQL Server 经 AD 的 LDAP 把主体映射到 Windows 登录名。测试中的 TDS 服务器用 `krb5.Acceptor` 接受 KDC 的票据，完成整个
    登录。未测试：对域中 SQL Server 的完整登录（在 Docker 中运行 Samba AD 并配置 SQL Server 的 AD 设置，眼下判断过重）、
    对任何服务器的 NTLM、对 Azure 的 Entra ID。

### I216 SQL Server 作为源

- **做**：源 `mssql`：按 LSN 轮询 SQL Server 的 CDC 变更表（`cdc.fn_cdc_get_all_changes_*`），每个事务的变更放在一起，LSN
  存入检查点；对每张被捕获的表不加锁按键读取快照（启用了快照隔离时使用它），并像 I175 一样在快照前读到的 LSN 处衔接；经
  `cdc.ddl_history` 看到 DDL；检查数据库与表是否开启 CDC，错误中给出开启的 SQL。可选更轻量的 change tracking（只有键，再
  读回行）。
- **完成标准**：与 SQL Server 2019 和 2022 互通：在写入下读取快照并继续读取变更，跨多次 kill 后与源相同；新加入 CDC 的表
  会被读取。
- **涉及**：`sync/mssql`、`test/sync`、`docs`。
- **已完成** 2026-11-10：包 `sync/mssql`（`syncmssql`），源 `mssql`（`source.go`、`snapshot.go`、`cdc.go`、`ct.go`），注册于
  `modules/standard`；schema、参考、模板、`schema-zh.js`；指南中两种语言的 [SQL Server](sync.zh-CN.md#sql-server)，含服务器的
  设置与授权。针对模拟源所用查询的假服务器的单元测试（分批读取并续读的快照、减半的窗口、分页读取的单个大事务、两个捕获
  实例、跟随期间新加入的表、DDL、清理越过位置、change tracking、每个查询与每次 emit 的失败）：语句覆盖率 98.6%，race 检查
  无误。互通（`test/sync/mssql_source_test.go`）在 SQL Server 2019 与 2022 上两次通过：一张 500 行的表在读取快照与变更期间
  被 300 个含插入、更新、删除与改键的事务写入，在随机点被 kill，结束时按键相等，CDC 与 change tracking 皆然；流水线运行中
  才被捕获、已有行的第二张表先整表读取再跟随；新增的列成为 DDL 记录；被点名却未捕获的表让连接测试失败，并给出
  `sp_cdc_enable_table` 调用。
- **S9 的重复**（2026-11-10 审查）：基准中多出的 10 行（fit 第 3 轮、Debezium 连接器第 2 轮，Kafka 目标处 500,010 条）
  既不是重复，也不是快照的重放：S9 以 `snapshot never` 运行，没有重启，fit 的日志里也没有错误。是测试工具的预热：第一个
  10 行的事务 2s 内没到目标时它会再写一个；随后第一个让计数上升，而计数在 1s 的稳定期内保持不变之后第二个才到（CDC 的捕获
  作业每秒轮询一次，两个搬运工具的 p99 延迟为 1.5 到 1.6s），于是测量前的目标计数漏掉了它，排空时把这 10 行算作多出。恰好
  多出 `TX_ROWS` 行、且两个搬运工具都出现，与此相符。已在 syncbench 中修复（`52644ae9`）：预热写过不止一个事务时，计数须再
  保持 2s 不变（启动时间仍止于它首次稳定之时）；单元测试 `TestWarmLateTransaction`。源中没有原因：窗口只在交出任何记录之前
  重读（减半），失败的批会让流水线重启（会记日志，而没有），清理检查只会失败，窗口的边界是提交 LSN，每个只读一次。
  本地冒烟运行（`SIZE=smoke`，fit，3 轮，跑了两次）结果精确，启动时间不变（4.2s）；第一次中有一轮超时，原因是工具的
  prepare 重启 CDC 捕获作业太快、使其停着，也已修复（`33e2ceb3`）。

**待审查的决定**（实现时做出，2026-11-10；在用户裁定前有效）：

1. **[kept]**（2026-11-10：与 PostgreSQL 源的写法一致；一个源读一个数据库，数据库是参数） **命名空间为 `schema.table`**，数据库是参数，与 PostgreSQL 源的写法一致；键为主键，否则为启用 CDC 时指定的唯一索引，否则
   为 `keys`。备选：`database.schema.table`。
2. **[kept]**（2026-11-10：内存有界，窗口内保持服务器的顺序；流式归并需要每个捕获实例一个连接（没有 MARS）） **按 `cdc.lsn_time_mapping` 的事务分窗口**：最多 `window_transactions`（1000）个事务，读取其中所有表的变更并按 LSN、
   `__$seqval`、操作排序，内存中最多持有 `window_rows`（10000）行；超过时窗口减半，超过它的单个事务按表依次以 `window_rows`
   分页读取（对序列值用 `TOP ... WITH TIES`，更新的两行不会被拆开）：这样的事务的记录按表依次到来，而不是服务器跨表的
   顺序。备选：每个捕获实例一个连接并流式归并（一个连接上没有 MARS）。
3. **[kept；2026-11-10 已裁定：保持至少一次]**（2026-11-10：读已提交的批只取短暂的共享锁，并由从之前读到的 LSN 起的重放补全；拒绝会把无法允许快照隔离的数据库挡在门外。11-10 S9 的重复并非这种重放（S9 以 `snapshot never` 运行；见“已完成”中的说明）。重放可以做到精确，但有代价：未做，见本节开头的清单） **快照的每一批在数据库允许时于快照隔离事务中读取，否则用读已提交**（短暂的共享锁，从不用读取未提交行的 `NOLOCK`）；
   它在发现表之前读到的 CDC 最高 LSN 处衔接，并在第一条记录前保存为起点（I175）。备选：没有快照隔离时拒绝做快照。
4. **[kept]**（2026-11-10：每次追上只多一个查询；永远追不上的源本就无限落后，追上时即会发现该表） **CDC 新开始捕获的表在源追上且捕获实例列表变化时被发现**，即所有实例都创建于位置的 `Since`（上次发现表时服务器的
   时钟，保存在检查点中）之后的表：每张先整表读取，再从其实例的起点跟随。这类表在捕获开始与被发现之间的变更由其快照
   覆盖。备选：固定的重新发现间隔。
5. **[changed]**（2026-11-10：源运行期间，旧实例在其到新实例起点为止的变更读完之前被删除，现在会让源失败并要求新的快照（在追上时捕获实例变化、或读取失败时发现）；完全不再被捕获的表记一条日志。源停止期间的删除不在 CDC 的表中留下痕迹，仍然发现不了。提交 `ae364b6a`，互通 `TestMSSQLSourceDroppedInstance`） **表的多个捕获实例依次读取**，每个从其起点读到下一个的起点，因此模式变化产生的新实例无需快照即可跟随；在其变更被读取
   之前删掉的旧实例会无声地丢失这些变更（未解决）。
6. **[kept]**（2026-11-10：保守且每个窗口一个查询；每个实例的最小 LSN 也会随新实例移动，无法区分清理与模式变化） **以 `cdc.lsn_time_mapping` 最旧的 LSN 越过检查点的 LSN 判定清理**：流水线失败并要求新的快照，偏保守（所读的表即使没有
   被删除的内容也可能失败）。备选：比较每个捕获实例的最小 LSN，但新实例也会移动它。
7. **[kept]**（2026-11-10：完整的名称意味着承诺读取该表，通配模式则描述现在与将来要读的表） **`tables` 中完整写出名称的表必须被捕获**（错误中给出 `sp_cdc_enable_table` 调用）；通配模式只匹配已捕获的表，以便之后
   加入的表被接上；一张表也没有则报错。备选：通配匹配到的每张表都必须被捕获。
8. **[kept]**（2026-11-10：提交时间是元数据，服务器通常运行在 UTC；每次轮询读取偏移，也仍会把窗口中切换前的事务算错） **提交时间**取 `lsn_time_mapping` 的 `tran_end_time`，为服务器时钟，用连接时读到的服务器 UTC 偏移（`SYSDATETIMEOFFSET`）
   转为 UTC：流水线运行期间的夏令时切换会让它们偏移，直到重新连接。
9. **[kept]**（2026-11-10：这是 CDC 对 DDL 的唯一记录；语句正是目标可以记录或应用的内容） **DDL 记录**来自 `cdc.ddl_history`，命名空间为该表的，语句放在 after 的 `statement` 中，不带位置；CDC 本身只在新的捕获
   实例中捕获新列（决定 5）。
10. **[kept]**（2026-11-10：这是更轻量模式写明的取舍（无变更前的行、变更合并）；只在启动时发现表，符合它用于固定模式的场景） **Change tracking**（`mode change_tracking`）：每次轮询读取所有表在检查点版本之后、当前版本及以前变更的键，按键分页，
    允许时全部在一个快照隔离事务中，并连接回行；一次轮询的记录是一个事务（`ct-<version>`），不存在的行为删除，更新没有
    变更前的行；需要主键；表只在每次启动时发现（运行中不会接上新表）。
11. **[kept]**（2026-11-10：与其他源对这些类型给出的形式相同） **值**：`time` 为文本（按其精度的 `hh:mm:ss.fffffff`），`uniqueidentifier` 为其文本，`datetimeoffset` 为时刻，`datetime2`
    与 `datetime` 为 UTC 时间（其钟面），`date` 为日期，`money` 为十进制数，`xml` 为字符串，`sql_variant` 为其基础值。
12. **[ruled 2026-11-10: done as recommended]**（2026-11-10：现在报告延迟：位置之后 `cdc.lsn_time_mapping` 中的事务数（取窗口的数目，窗口满时最多每秒一次 `COUNT_BIG`），change tracking 时为版本数，作为 `fit_sync_source_lag`（提交 `ae364b6a`）。作业不提供 `Chunker`：保留，与其他 CDC 源一致。检查点曾只随所读表的记录前进：被捕获的忙表之间的一张冷表会保留旧检查点，清理的保留期越过它之后重启会要求新的快照，尽管它什么也没丢。已实现：`sync` 中只含位置的记录（`Run.EmitPosition`），按顺序经过队列，跳过转换和目标，不计为记录，在它之前的记录都确认后移动检查点；`mssql` 在每个 CDC 窗口和 change tracking 版本区间之后交出一条，`mysql` 在其他表的 binlog 事务结束时，`postgres` 在空事务结束和事务之间的 keepalive 时。见决定 14 至 16。） **未包括**：作业逐表读取快照，不分块（没有 `Chunker`）；不报告延迟；检查点只随所读表的记录前进，因此其他被捕获表的事务
    在重启后会被再次读取。
13. **[kept]**（2026-11-10：目标无需第三个包即可共用参数及其检查） **`ConnParams` 与 `WireConfig` 从 `sync/mssql` 导出**，供 I217 的目标共用连接参数及其检查。
14. **[2026-11-10 已裁定：保留]**（2026-11-10，随决定 12 的裁定）**只含位置的记录由流水线而不是源限流**：源在每段没有自己记录的读取之后
    调用 `Run.EmitPosition(func() Position)`，流水线只在 `checkpoint_interval`（5s）内没有任何一种记录时才调用该函数并入队，所以
    忙表靠自己的记录移动检查点，冷表每个检查点间隔至多移动一次；用函数可让 binlog 源不必为每个跳过的事务序列化 GTID 集合。
    试运行和分块读取不取它。这种记录不导出（`Record.posOnly`）：转换、目标和死信都看不到，状态中的读取、确认和目标计数不含
    它，记录 ID 和转换的编号不受影响。备选：新的 `Op`（每个转换和目标都得跳过它）；每个源自己限流。
15. **[2026-11-10 已裁定：保留]**（2026-11-10）**保存检查点的目标与只含位置的记录**：这样的目标保存它写过的最后位置，只含位置的记录越过
    它而不写入。与其为每条记录写目标的表，检查点文件在其 `position` 是只含位置的记录的位置时记下那个位置（`wrote`），各目标都
    停在 `wrote` 的运行从文件中更靠后的位置开始（不重放）；目标停在其他位置时照旧采用。备选：把每条只含位置的记录告诉
    `CheckpointSink`，冷表每个检查点间隔要在其服务器上做一次事务。
16. **[2026-11-10 已裁定：保留]**（2026-11-10）**各个源在何处交出**：`mssql` 在读完每个 CDC 窗口之后（其末尾 LSN，含未读或未被捕获的表
    的事务），以及每个不含所读表变更的 change tracking 版本区间之后；`mysql` 在每个不含所读表记录的事务结束时（包括其他数据库的
    DDL），心跳时不交（空闲服务器的 binlog 不动）；`postgres` 在没有记录的事务结束时（其他表的，15 之前的服务器会发送）以及没有
    暂存流式事务时事务之间的 keepalive（15 及以后跳过空事务），只在运行起点之后。`postgres` 的复制槽本来就在事务之间被告知位置
    （`idle`），服务器不为其他表保留 WAL；只含位置的记录让检查点也跟上。

### I217 SQL Server 作为目标

- **做**：目标 `mssql`：按键用 `MERGE`（或按配置先 `UPDATE` 再 `INSERT`）upsert，按键删除，每批一个事务或保留源的事务，
  快照用批量导入，检查点写在目标的事务中以实现精确一次（同 I191）。
- **完成标准**：互通：MySQL 到 SQL Server、SQL Server 到 SQL Server 的流水线在随机位置被 kill 后结果相同；
  `checkpoint: sink` 时精确一次成立。
- **涉及**：`sync/mssql`、`test/sync`、`docs`。
- **完成** 2026-11-10：`sync/mssql/sink.go`，目标 `mssql` 与源同在 `syncmssql` 包中，基于 I215 的 `mssqlwire`（与源共用
  `ConnParams` 和 `WireConfig`）：一段行用 `MERGE ... WITH (HOLDLOCK)` upsert（`upsert update_insert`：先 `UPDATE`，没有行时
  再 `INSERT`），按键删除，更新改了键时删除旧行，一段中每行只写一次（键再次出现时开始下一条语句），每条语句最多 1000 行、
  2000 个参数；不属于源事务的连续插入达到 `bulk_rows`（1000）时批量装载到 `#fit_stage` 再合并；`transactions source` 或
  `batch`，从不只提交源事务的一部分，打开的事务在新连接上重写；`SET XACT_ABORT ON`，语句被拒后由 `XACT_STATE()` 决定是否
  重新开始事务并重写其中的语句；逐条语句找出被拒的记录；NULL 以其列的类型发送（每个连接用 `SELECT TOP 0` 读一次）；
  `identity_insert`；`checkpoint sink`（I191 的 `CheckpointSink`）写入 `_fit_sync_checkpoints`。单元测试针对一个把语句
  应用到表、支持事务（可被注定回滚或否）的假连接，`sync/mssql` 覆盖率 97.8%。互操作（`test/sync/mssql_sink_test.go`、
  `TestMSSQLSink*`，基于 `test/sync/mssql.sh`，本地为 SQL Server 2022）：从中间和开头重放后，`MERGE`、先 `UPDATE` 后
  `INSERT` 与批量装载都收敛；坏行单独失败；generate 的各类型记录（`IDENTITY` 键）经随机被杀的流水线；`checkpoint sink`
  时两种 `transactions` 都恰好一次（多次被杀后 0 行重写，`checkpoint file` 时有重写）；SQL Server 到 SQL Server（CDC 源）
  与 MySQL 8.4 到 SQL Server 在随机被杀后结果一致。两种语言的 `docs/sync.md`、schema、`schema-zh.js`。

**待评审的决定**（实现时做出，2026-11-10；在用户裁定前有效）：

1. **[kept]**（2026-11-10：一个连接把每个源事务及检查点放在服务器的同一个事务里；备用写入者会把它们拆开，而目前没有需要这点速度的场景。）
   **一个写入者，不是 `ParallelSink`**：作业经一个连接写入。备选：MySQL 目标那样的备用写入者，各有连接和事务。
2. **[kept]**（2026-11-10：分段提交源事务正是目标承诺绝不做的，SQL Server 能承受大事务。顺带修复：未提交事务持有的操作不再在每个批次整体复制（长事务的代价是平方级，提交 `fix(sync/mssql)`）。）
   **没有 `max_transaction_bytes`**：源事务无论多大都是 SQL Server 的一个事务（其日志随之增长）；MySQL 的上限是为 TiDB 设的。
   备选：像 MySQL 目标那样把大事务分段提交。
3. **[kept]**（2026-11-10：语句级回滚对转换错误不成立；`XACT_STATE()` 加上重写事务，才使被拒记录单独失败，互操作中的坏行测试证明了这一点。）
   **`SET XACT_ABORT ON`，语句被拒后查 `XACT_STATE()`**：被拒语句的事务整体回滚（编译错误时保持原样），目标重新开始事务、
   重写其中的语句，再查找那条记录。备选：`XACT_ABORT OFF`，依赖语句级回滚，但有些错误（转换）并不遵守。
4. **[kept]**（2026-11-10：直接批量装载到表会因重放快照中已有的行而失败；经临时表能收敛，互操作中带批量装载的重放证明了这一点。）
   **批量装载经临时表再合并进目标表**（`SELECT TOP 0 ... INTO #fit_stage ... UNION ALL`，不带 `IDENTITY` 属性），这样重放的
   快照也能收敛；直接装载到目标表会因已有的行而失败。只用于不属于源事务的插入，且达到 `bulk_rows` 条；因某个值失败的装载
   改写成语句，由语句找出那条记录。
5. **[changed]**（2026-11-10：类型每个连接只读一次，而永久错误不会重连，所以连接期间被修改的列会一直拒绝按旧类型发送的 NULL（257），直到重启。现在永久拒绝的语句会丢弃该表的类型缓存，并在记录失败前逐个重写其操作（`TestSinkTypesChanged`；互操作 `TestMSSQLSinkTypeRefusals`，SQL Server 2022）。）
   **NULL 参数按其列的类型发送**，每张表每个连接用 `SELECT TOP 0 *` 读一次：客户端默认的 `nvarchar` NULL 不能转换为
   `varbinary`（错误 257）。改变列类型的 DDL 在下一个连接时才会看到。
6. **[kept]**（2026-11-10：带点的命名空间是所有 SQL 目标的约定，`route` 变换可以改名。指南原先建议的 `dbo.{namespace}` 会把带点的命名空间变成 `db.schema.table`；现在改为建议 `route`（`rules { match shop.*; to dbo.{rest} }`）。）
   **表名由命名空间按点拆成** `schema.table` 或 `db.schema.table`，所以 MySQL 源的 `shop.orders` 是 schema `shop` 中的表：
   用 `dbo.{namespace}` 这样的 `table` 或直接写表名来改变映射。备选：一个默认 schema 参数。
7. **[changed]**（2026-11-10：缺少类型冲突的错误号：206（时间写入 int）和 257（文本写入 varbinary），以及批量装载的 4815、4864 和 8102（更新 `IDENTITY` 列）：这样的记录会被流水线无限重试，而不是交给 `on_error`。已补上，并有互操作测试。）
   **永久拒绝的错误** 是一组错误号（转换 245、8114、8115，截断 8152 和 2628，NULL 写入 NOT NULL 515，约束 547、2601、2627，
   对象和列 207、208，语法 102、156 等）；其余（死锁 1205、锁与快照冲突、超时）重试。
8. **[kept]**（2026-11-10：`MERGE ... WITH (HOLDLOCK)` 是在并发写入下仍成立的单语句 upsert；`update_insert` 服务于不用 `MERGE` 的用户。）
   **默认用 `MERGE`**，带 `WITH (HOLDLOCK)` 防止同一键的并发插入；不用 `MERGE` 的用户用 `update_insert`。
9. **[kept]**（2026-11-10：2022 覆盖了目标的所有路径；2019 由 CI 的 `mssql` 族运行。）
   **本地互操作只在 SQL Server 2022 上运行**（2019 在 `mssql.sh` 的默认集合中，由 CI 的 `mssql` 族运行），MySQL 流水线用
   `mysql.sh` 的环境在 MySQL 8.4 上运行；没有它时跳过。

### I218 Doris 与 StarRocks 作为目标与源

- **做**：目标 `doris`（也用于 StarRocks）：用 fib 的客户端经 HTTP 做 Stream Load，JSON 或 CSV 请求体，label 取自批的位置，
  重发的一批会以已导入被拒绝；删除经 unique 模型的删除标记（`__DORIS_DELETE_SIGN__`，StarRocks 的 `__op`），可选部分列更新，
  跟随 frontend 到 backend 的重定向。源 `doris`：经 MySQL 协议（I172 的客户端）按查询读快照并按游标列轮询，与 I170 对
  ClickHouse 的做法相同。
- **完成标准**：在 Docker 中与 Doris 2.1 或 3.0 和 StarRocks 3.x 互通；重发的一批只导入一次；重放 upsert 与删除后行正确。
- **涉及**：`sync/doris`、`test/sync`、`docs`。
- **完成** 2026-11-10：`sync/doris` 包（`sink.go`、`source.go`、`values.go`），目标与源在 `modules/standard` 注册；与 I219
  一起，第二轮基于 HTTP 的目标共用的连接 `sync/internal/httpconn`（之后已并入 `sync/internal/fibrest`，见 I219 的决定 11；fib 的客户端，依次尝试各 URL，基本或 bearer 认证，TLS，
  跟随重定向，408、429 与 5xx 时退避重试）；为源导出 `syncmysql.WireValue`。单元测试针对会重定向到假后端的假前端（label、
  请求头、请求体、已存在与仍在运行的 label、拆分找出坏行）和假 MySQL 连接（分页、续读、轮询、删除、`snapshot never`），
  包覆盖率 98.6%。互操作（`test/sync/olap.sh`、`test/sync/olap_test.go`）：Doris 2.1.10（一个前端和一个后端，跟随前端的
  重定向）与 StarRocks 3.4.8（allin1，直接导入其后端）：同一批两次导入 Duplicate Key 表只存一份；插入、更新与删除从中间
  和开头以不同批大小重放后行正确，JSON 与 CSV 都是；部分列更新；坏行单独失败；源分页、续读、轮询；generate 的各类型记录
  经随机被杀的流水线后结果一致。schema、参考文档、`schema-zh.js`、两种语言的 `docs/sync.md`、
  `schema/doc/modules/sync_*_doris.md`。

**待评审的决定**（实现时做出，2026-11-10；在用户裁定前有效）：

1. **[ruled 2026-11-10: done as recommended]**（2026-11-10：在 `label_keep_max_second` 之内重置流水线或重建表，记录曾会被静默地以“已导入”拒绝，除非用户改 `label_prefix`。已实现：检查点文件总带有谱系，在流水线首次启动、重置、手动设置位置和新的作业时生成，重启保留；到达目标的每条记录都带着它（`Record.Lineage`），label 对它做哈希，所以重置会自行改变 label；`label_prefix` 保留。见决定 12 和 13。）
   **一次导入的 label 是前缀加上数据库、表、这段记录首末位置和条数的哈希**，与 ClickHouse 的令牌相同。label 比删除重建的
   表活得久（Doris 保留 `label_keep_max_second`，三天），所以重置或新建表之后用户要改 `label_prefix`，否则记录会以“已导入”
   被拒绝。备选：label 中带上流水线的谱系（目前只有 `CheckpointSink` 能拿到）；用 Stream Load 的两阶段提交配合检查点做到
   恰好一次。
2. **[kept]**（2026-11-10：CDC 源的行是完整的，并集就是所有列；对某行缺少的列用表默认值，会让同一次导入中的行彼此不同，稀疏的行用 `partial_columns`。）
   **每次导入都指明列**，即这段记录各行列的并集，因此缺少其中某列的行把它导入为 null（`partial_columns` 时保持不变）。备选：
   整行时不发 `columns`（缺少的列取表的默认值）；按列集合切分。
3. **[kept]**（2026-11-10：总是发送删除列会让导入 Duplicate Key 表的每次导入都失败；为删除单独做 `merge_type: DELETE` 导入会让混合的一段导入次数加倍。）
   **只有含删除的一段才发送删除列**，删除写删除前的行或键；这样插入可导入任何模型，`deletes ignore` 适用于 Duplicate Key
   表。备选：总是发送；删除用单独的 `merge_type: DELETE` 导入。
4. **[kept]**（2026-11-10：各系统文档中的形式，两者都在互操作中运行过；StarRocks CSV 的 `\N` 限制已写入文档，默认是 JSON。）
   **JSON 在 Doris 上每行一个（`read_json_by_line`），在 StarRocks 上为数组（`strip_outer_array`）**，各自的常用形式。
   **CSV 用 `enclose` 与 `escape` 加引号**，Doris 上加 `trim_double_quotes`（否则保留引号），null 在 Doris 上写 `\N`，在先做
   反转义的 StarRocks 上写 `\\N`：字符串 `\N` 无法以 CSV 写入 StarRocks（默认是 JSON）。
5. **[kept]**（2026-11-10：对半拆分用 log n 次导入找到那条记录，每半的 label 都能同样地再次生成，所以重放会遇到相同的 label；每条记录一次导入则要 n 次。）
   **因行被拒的导入对半拆分**，各用自己的 label 导入，直到找出那一条，它永久失败；其余都导入。备选：整段交给 `on_error`；
   每条记录一次导入。
6. **[kept]**（2026-11-10：这是 Stream Load 自己的状态；仍在运行的 label 再问一次，而不是交给 `on_error`。）
   **`Label Already Exists` 在之前的导入为 `FINISHED`、`VISIBLE` 或 `COMMITTED` 时算已导入**，仍在运行时再问（不是交给
   `on_error` 的错误）；`Publish Timeout` 算已导入（已提交，稍后可见）。
7. **[kept]**（2026-11-10：与其他 JSON 目标的写法相同。）
   **时间写成 `time_zone` 中的 `DATETIME` 文本，默认 UTC；字节为 base64**，与其他 JSON 目标相同。
8. **[kept]**（2026-11-10：检查凭据需要一张表，而目标只有从记录中才知道表，第一次导入的错误会指明原因。源的分块作业读取可以等作业需要时再做。）
   **目标的连接测试请求 `/api/health`**，不检查凭据；用一次空导入可以检查，但需要一张表。**源不支持作业的分块读取**
   （clickhouse 源的 `chunk_rows`）：作业按游标读。位置中保存 SQL 字面量，读回时校验。
9. **[kept]**（2026-11-10：两个系统的文档都没有行值比较；展开的写法是同一个条件。）
   **元组比较展开书写**（`(a > x) OR (a = x AND b > y)`），因为两个系统都不比较行值。
10. **[kept]**（2026-11-10：已完成，见 I219 的决定 11。）
    **第二轮基于 HTTP 的目标共用一个客户端包**（`sync/internal/fibrest`，见 I219 的决定 11），而不是像 `clickhouse` 和 `elasticsearch` 那样各包
    一份。
11. **[kept]**（2026-11-10：“完成标准”要求 Doris 2.1 或 3.0；StarRocks 直接导入后端，与 Doris 重定向之后走的是同一段代码。）
    **互操作用 Doris 2.1 与 StarRocks 3.4**：StarRocks 的前端重定向到其后端自己的地址 `127.0.0.1:8040`，在宿主机上与 Doris
    的后端冲突，所以它的导入直接发往后端；Doris 的重定向经过测试。Doris 3.0 不在其中。
12. **[2026-11-10 已裁定：保留]**（2026-11-10，随决定 1 的裁定）**每条流水线都有谱系**：检查点文件中的一个随机 ID（`lineage`，此前只有
    `CheckpointSink` 的流水线才有），在流没有谱系也没有位置而启动时生成，并在源运行前立即保存（流的启动现在会写一个没有位置的
    检查点，所以第一次检查点之前的崩溃不会改变 label），重置、手动设置位置和新的作业（按块从头再跑的作业；从位置再跑的不分块
    作业保留它）也会生成；重启和恢复的作业保留它。目标以 `Record.Lineage()` 读取，在记录到达目标前与流水线名一起设置；死信也带
    着它。旧版 fit 保存的、有位置的检查点在重置前没有谱系，其目标生成的名字逐字节不变，所以升级不会用新 label 重放；目标保存
    检查点的流水线本来就有谱系。备选：代数编号（ID 无需比较）；给旧检查点也生成谱系（升级会改变 label，检查点之后的记录会
    在 Duplicate Key 表中导入两次）。
13. **[2026-11-10 已裁定：保留]**（2026-11-10）**哪些名字带谱系**：`doris` 的 label 和 `http` 的 `Idempotency-Key`，它们的服务器在目标
    重建后仍保留已接收的内容，所以重置后的记录会再次写入。记录 ID（`nats` 的 `Nats-Msg-Id`、`pulsar` 的 `fit-id`、`rabbitmq`
    的 `message_id`）不带：重置后再次读到的消息仍是同一条消息，消费者丢弃它是对的，而且 JetStream 的窗口只有几分钟。`s3` 的
    对象名不带：再次写入的对象覆盖自身，这正是重置想要的。`clickhouse` 的去重令牌不带：ClickHouse 把它保存在表中，随重建的
    表一起消失。

### I219 Loki 与 OpenTelemetry 日志作为目标

- **做**：目标 `loki`：push API（`/loki/api/v1/push`，JSON，或 snappy 压缩的 protobuf），按模板生成标签确定流（基数有界：
  只取配置的字段作标签），行经编解码器生成，按租户设置 `X-Scope-OrgID`，按 Loki 的规则处理乱序。目标 `otlp_logs`：OTLP/HTTP
  日志记录（protobuf），资源与日志属性由记录映射，复用 fit 追踪导出已有的部分。两者都批量发送，遇 429 与 5xx 重试。
- **完成标准**：在 Docker 中与 Loki 3 和 OpenTelemetry Collector 互通；跨多次 kill 把文件送到 Loki 和 OTLP 不丢数据；在 I193
  的测试工具中与 Filebeat 并列做基准测试。
- **涉及**：`sync/loki`、`sync/otlp`、`test/sync`、`bench/sync`、`docs`。
- **完成** 2026-11-10：`sync/loki` 与 `sync/otlp`（目标 `otlp_logs`）包，在 `modules/standard` 注册，基于 I218 的
  `sync/internal/fibrest`（决定 11），以及 `sync/internal/pb`（手写 protobuf：logproto 的 `PushRequest`、OTLP 的
  `ExportLogsServiceRequest`、部分成功的读取）和 `sync/internal/tmpl` 的记录模板（标签与属性）。单元测试针对能解码两种编码的
  假接收端：流、标签、元数据、租户、时间、拆分找出坏记录、部分成功、带类型的 body；`loki` 与 `otlp` 覆盖率均为 99.0%。
  互操作（`test/sync/obs.sh`、`test/sync/obs_test.go`）：Loki 3.5.5 与 OpenTelemetry Collector（contrib 0.137.0，读回其文件
  导出器的输出）：两种编码，重放后 Loki 只存一份，文件经随机被杀的流水线送到 Loki 和 OTLP，每一行都送达。基准场景
  `files-loki` 与 `files` 并列（fit 对比 Promtail 3.5.5，按 Loki 的 `loki_distributor_lines_received_total` 计数），此处未运行。
  schema、参考文档、`schema-zh.js`，两种语言的 `docs/sync.md` 与 `bench/sync/README`。

**待评审的决定**（实现时做出，2026-11-10；在用户裁定前有效）：

1. **[kept]**（2026-11-10：只有配置中写出的字段才成为标签，这就是上限；超出时 Loki 自己的流数限制会拒绝（429，会重试），而 fit 自己设上限就只能丢弃记录。）
   **流的标签只来自 `labels` 模板**，默认 `{"job": "fit", "namespace": "{$namespace}"}`，值为空的标签不写；此外不限制一批的
   流数。备选：限制流数；每个字段都作标签，另加排除列表。
2. **[kept]**（2026-11-10：目前没有哪个源的位置是时间，没有更好的时间可用；指南写的就是至少一次。）
   **没有时间的记录使用写入时间，同一批中相差一纳秒**，这样 Loki 的去重不会合并不同的行，但它们重放后会再存一次（至少一次）。
   备选：取源位置作时间（文件源目前没有可用作时间的位置）。
3. **[kept]**（2026-11-10：Loki 对被拒的推送接收它能接收的部分，再次发送的内容只存一份；互操作中的重放只存了一份。）
   **一次推送中每个流的条目按时间排序**；400 或 413 拆分直到找出那条记录，Loki 拒绝其余之前已接收的条目由它的去重吸收。
4. **[kept]**（2026-11-10：没有新增依赖。）
   **snappy 用 `klauspost/compress/s2`**，fit 为 Kafka 已依赖它，不是新依赖。
5. **[kept]**（2026-11-10：之后还有流水线的重试，所以长时间的 429 仍会等待；五次尝试避免一个请求占住目标。）
   **请求在目标内重试**：408、429 与 5xx，共五次（fibrest 的默认），遵从 `Retry-After`（最多 30s），之后交给流水线自己的重试；不像
   elasticsearch 目标那样在 429 持续期间一直等。
6. **[kept]**（2026-11-10：沿用 fit 遥测的资源；目标必须用 fib 的客户端并发送 protobuf。）
   **otlp_logs 复用 fit 追踪导出的部分**：资源（`service.name`、取自 `observe.Version` 的 `service.version`）、scope `fit`、
   由 `config.ParseHeaders` 读取的 `headers_file`；不复用其 `net/http` 导出器（目标用 fib 的客户端），也不复用其只有 JSON 的
   编码（protobuf 手写，JSON 作为选项）。
7. **[kept]**（2026-11-10：部分成功不指明是哪些记录，`on_error` 无从接手；只能给出警告。）
   **otlp_logs 的默认值**：body 为编解码器生成的行，属性 `fit.namespace` 与 `fit.op`，开启 gzip；`body row` 时超过 int64 的
   uint 以及 decimal、时间和 JSON 为字符串。部分成功记为警告，因为接收端不说明丢弃了哪些记录。
8. **[changed]**（2026-11-10：loki 的测试请求 `/loki/api/v1/labels`，需要读权限：只能推送的凭据（托管 Loki 的只写令牌、只转发推送 API 的网关）无法通过。现在改为以该租户、按目标的编码推送一个没有条目的流，Loki 3.5.5 答 204 且不存任何东西（不含流的推送它答 422 拒绝）；互操作 `TestLokiPush` 会运行它。otlp_logs 的空导出保持不变。）
   **连接测试**：loki 以该租户请求 `/loki/api/v1/labels`；otlp_logs 导出一个空请求。
9. **[ruled 2026-11-10: done as recommended]**（2026-11-10：Promtail 已被弃用并到了生命周期终点，与它对比可能显得是在和已停止维护的工具比：见本节开头的列表。）
   已完成：Grafana Alloy（`grafana/alloy:v1.21.3`）作为 `files-loki` 的第三个默认搬运者，与 fit 和 Promtail 并列
   （`bench/sync/configs/alloy/config.alloy`：`local.file_match` + `loki.source.file` → `loki.write`，与 Promtail 相同的标签、
   1 MiB 批次最多等待 1s 发送、从文件开头读、位置保存在容器内、相同的 CPU 与全新的 Loki）；该场景的权重不变。
   **基准对比 Promtail 而不是 Filebeat**（Filebeat 没有 Loki 输出），尽管 Promtail 已被 Alloy 取代；`files-loki` 还未加入
   `plan.sh`，由 I222 连同权重加入。
10. **[kept]**（2026-11-10：不开认证的 Loki 把所有推送都归入同一个租户，在那里区分不了租户；单元测试逐个请求检查请求头。）
    **互操作中的 Loki 不开认证**，所以租户只由单元测试检查。
11. **[ruled 2026-11-10: done as recommended]**（2026-11-10，修改：一个客户端包的做法成立（五个连接器和 `embed` 在用）。它带着凭据和请求体跟随重定向，从 https 到 http 时会明文发送；现在这样的重定向是一个错误，并指明出路：在 `urls` 中写后端自己的 http 地址（`TestTLS`，提交 `fix(sync/fibrest)`）。两种模板语法曾交给用户，2026-11-10 按建议裁定。已实现：名称模板把 `{$namespace}` 当作 `{namespace}`（influxdb、tdengine、qdrant 与 milvus 经 `tmpl.Parse`；SQL 目标自行解析的 `table` 经 `tmpl.Canonical`：clickhouse、doris、mysql、postgres、mssql），一种写法两类模板通用；名称模板中其他 `{$...}` 现在在加载时报错，而不是当作名为 `$...` 的字段。主题、subject、路由键、Redis 键、S3 键、索引与 route 的模板仍只认 `{namespace}`：它们的占位符集合不同，裁定针对的是名称模板。）
    **七个 HTTP 目标共用一个客户端包和一个模板包**（与 I220、I221 合并时决定，2026-11-10）：I218/I219 的
    `sync/internal/httpconn` 与 I220/I221 的 `sync/internal/fibrest` 做的是同一件事；保留 `fibrest`，因为已有五个连接器
    （以及 `embed` 的固定证书）在用它，并补上 `httpconn` 有的部分：多个 `urls` 依次尝试（502、503、504 与连接错误换下一个）、
    带凭据跟随重定向（`Request.Redirects`，用于 Doris 的前端）、`Authorization`（来自环境变量的 basic 或 bearer）、
    `HeaderFields`（参数中的请求头，拒绝连接器自己的）、`Connect`（把模块参数变成客户端，错误为模块的）、带类型的
    `ResponseError` 与 `IsStatus`。doris、loki 与 otlp_logs 改用它；`httpconn` 删除。默认值用 fibrest 的（五次尝试，退避最多
    30s）；三个目标保留各自 60s 的超时。它们把被拒请求对半拆分的做法保留，与 fibrest 的 `WriteFinding`（逐条单独写）并存，
    后者适合较少整体拒绝请求的服务器。**模板**：`sync/internal/tmpl` 同时包含两种：名称模板（`{namespace}` 与必须存在的
    字段，用于 influxdb、tdengine 与 qdrant），以及来自 `rectmpl` 的记录模板（`{$namespace}`、`{$op}`，字段缺失时为空，
    与 file 目标相同），即 `ParseRecord` 与 `TimeOf`；`rectmpl` 删除。两种语法保持原样，因为各自沿用其目标已写入文档的配置。
    `vecutil`（向量目标的）与 `pb`（日志目标的 protobuf）没有重复。备选：保留 `httpconn`，把那四个目标改过去；所有目标用一种
    模板语法，会破坏其中一方的配置。

### I220 InfluxDB 与 TDengine 作为目标

- **做**：目标 `influxdb`：以 line protocol 写入 InfluxDB 2 的 `/api/v2/write`（可选 3 的 `/api/v3/write_lp`、1.x 的
  `/write`），measurement、tag 与 field 按配置从记录映射，时间戳取自字段或提交时间，设置精度，gzip 压缩。目标 `tdengine`：
  经 TDengine 的 REST 接口写 schemaless line protocol（或用 SQL `INSERT`，按模板确定超级表与子表）。写入按序列与时间幂等。
- **完成标准**：在 Docker 中与 InfluxDB 2、3 和 TDengine 3 互通；同一批写两次每个点只存一次；以 MQTT（I214）到 InfluxDB 作为
  端到端的物联网流水线。
- **涉及**：`sync/influxdb`、`sync/tdengine`、`test/sync`、`docs`。
- **部分完成** 2026-11-10：目标 `influxdb`（`sync/influxdb`：API v2、v3 与 v1，记录到数据点的映射与 line protocol，
  `tdengine` 共用）与 `tdengine`（`sync/tdengine`：schemaless 行写到 taosAdapter 的 `/influxdb/v1/write`，或经 `/rest/sql`
  以 `INSERT ... USING` 写超级表），二者与 I221 共用一个客户端（`sync/internal/fibrest`：fib 的异步客户端、TLS、密钥、
  408/429/5xx 重试、被拒请求中的记录逐条重写以找出被拒的那条）和模板（`sync/internal/tmpl`）；连接测试；针对假服务的单元测试
  （influxdb 99.0%、tdengine 98.6%、fibrest 98.8%、tmpl 100%）；在 Docker 中与 InfluxDB 2.7（API v2 与 v1）、InfluxDB 3 Core
  和 TDengine 3.3.6 互通（`test/sync/tsdb_test.go`，由 `test/sync/obs.sh` 运行）：一批写入后再从中间、从头各写一次，每个点只留
  一个且为最后的值，类型不对的点在其记录处单独失败；中英文指南中的章节。2026-11-10 亦完成 MQTT（I214）到 InfluxDB 端到端：`TestInfluxDBFromMQTT`（`test/sync/iot_test.go`，`obs` 族带
  Mosquitto）：QoS 1 的 JSON 读数由源的会话保留，经在随机时刻被杀的流水线，每条成为一个数据点。

**待审查的决策**（实现时做出，2026-11-10；在用户裁定前有效）：

1. **[已裁定 2026-11-10：按建议保留]**（2026-11-10：产品选择：时序库是历史，还是源表行的副本；见上方列表。）
   **删除不写**（`deletes skip`，计为已写入），或交给 `on_error`（`deletes fail`）：line protocol 没有删除，InfluxDB 3 不能删除
   单点，InfluxDB 2 的 `/api/v2/delete`（谓词加时间范围）开销大。备选：逐点调用 v2 的删除 API；TDengine `mode sql` 中用
   `DELETE FROM sub WHERE ts = ...`。
2. **[kept]**（2026-11-10：`time_field`
   是文档给出的让重放写出相同数据点的方法，参数说明也写明了回退值不会重复；让记录失败会让没有时间的文件和
   generate 流水线停下，而 MQTT 的提交时间本来就是接收时间。）
   **既无 `time_field` 也无提交时间的记录用写入时间**，重放时不会重复（如文件的行）。备选：让这类记录失败；由服务器打时间戳。
3. **[kept]**（2026-11-10：对 TDengine 3.3.6
   的探测表明拒绝码都在代码所列范围内（值类型不对 0x216、未知列 0x2602、schemaless 类型冲突
   0x3002）；读 InfluxDB 3 的行号只对给出行号的那一种服务器省请求。）
   **被拒的请求逐条重写**以找出被拒的记录（InfluxDB 的 400、413、422，v3 用 `accept_partial=false`；TDengine 的 0x216、
   0x2600-0x26ff（表或库不存在除外）、0x3000-0x3fff），因为数据点幂等所以安全；InfluxDB 2 的部分写入会整批重写。备选：读取
   InfluxDB 3 报告的行号（其他不提供）。
4. **[kept]**（2026-11-10：同一次探测：缺数据库（0x388）和缺超级表（0x2603）会重试而不是拒绝，在用户创建它们之前这是对的；`/rest/sql`
   如所写返回 200 加 `code`。）
   **TDengine 的 schemaless 错误是 HTTP 500 加 `[0xNNNN]`**：由错误码区分拒绝（见上）与可重试的错误；`/rest/sql` 返回 200 加
   `code`。不单列表示繁忙的错误码：其他错误都先退避重试，再由流水线重试。
5. **[kept]**（2026-11-10：四个目标和一个变换共用一个经过测试的客户端；把 Doris 和 Loki
   迁过来是整理，不是缺口。）
   **HTTP 目标共用一个客户端包**（`sync/internal/fibrest`），供这两个目标、I221 的目标和 `embed` 变换使用，而不是把
   elasticsearch 与 http 目标的客户端复制第五份；Doris（I218）与 Loki（I219）也可使用。备选：像第一批那样每个包一个客户端。
6. **[kept]**（2026-11-10：互通在 InfluxDB 2、3 与 TDengine
   中写入并读回了这些类型；decimal 写为浮点数正是两种服务器保存的方式。）
   **类型**：int 为 `i`，uint 为 `u`（v2、v3；v1 为 `i`，超出 int64 则失败），TDengine 为 `i64`、`u64`、`f64`；decimal 写为浮点数；
   其他类型写为字符串（bytes 为 base64，map 与数组为普通 JSON）；默认开启 gzip；精度默认 `ns`（InfluxDB）和 `ms`（TDengine 的
   默认值）。标签为文本；`measurement` 中引用的字段仍保留为字段。
7. **[kept]**（2026-11-10：与 TDengine 自己的 schemaless 写入命名方式相同（t_
   加哈希），每个序列稳定，远小于 192 个字符。）
   **TDengine `mode sql` 的默认子表名**为 `t_` 加超级表与标签的 MD5，每个请求一条 INSERT、每条记录一个子句；超级表由用户创建。
8. **[kept]**（2026-11-10：已完成：I222 把 `obs` 做成了 `run.sh` 的一族和 CI
   任务，仍会设置 `FIT_SYNC_OBS`。）
   **互通所用的服务器有单独的脚本 `test/sync/obs.sh`**（`FIT_SYNC_OBS` 控制 `FIT_SYNC_INTEROP=required` 是否生效），直到 I222
   把它们并入 run.sh 的 `obs` 分组；InfluxDB 3 Core 每次写入要等约一秒落 WAL，测试耗时因此偏长。

### I221 向量数据库作为目标：Milvus 与 Qdrant

- **做**：目标 `qdrant`：经 Qdrant 的 REST API 按由键稳定导出的 ID（UUID 或整数）upsert 点，向量取自字段，其余作为 payload，
  按 ID 删除；目标 `milvus`：经 Milvus 2.4+ 的 REST v2 API 按主键 upsert 与删除。变换 `embed` 通过兼容 OpenAI 的 embeddings
  API（fit 自己的 `llm` 代理或任意端点）从文本字段计算向量，批量调用，按内容哈希缓存，失败按 `on_error` 处理。
- **完成标准**：在 Docker 中与 Qdrant 1.x 和 Milvus 2.4+（standalone）互通；带 embedding 复制 PostgreSQL 的行，跨多次 kill
  后两者结果相同；删除到达两者。
- **涉及**：`sync/qdrant`、`sync/milvus`、`sync/transforms`、`test/sync`、`docs`。
- **完成** 2026-11-10：目标 `qdrant`（`sync/qdrant`）与 `milvus`（`sync/milvus`），变换 `embed`（`sync/embed`，一个
  `BatchTransform`），基于 I220 的 `sync/internal/fibrest` 与 `sync/internal/vecutil`（把一批记录缩减为每个键的最后一条，键的
  文本）；`module.Env.LLMProxy` 与 sync 角色的成员携带配置的 `llm.proxy.api`（`fit sync run`、`fit validate`，以及 Admin API
  经运行中的 runtime 对提交的流水线做的测试与试运行也有）；schema、参考、模板、`schema-zh.js`；针对假服务的单元测试（qdrant
  98.0%、milvus 96.7%、embed 99.1%、vecutil 100%）；在 Docker 中与 Qdrant 1.15 和 Milvus 2.5.17 standalone 互通
  （`test/sync/vector_test.go`）：重放的写入收敛，删除到达两者，维数不对的向量在其记录处单独失败，generate 的插入、更新与删除经
  `embed`（测试中的假 embeddings API）写入两者，在随机时刻多次 kill 后，每一行都在且带有其文本的向量，删除的都已不在；中英文
  指南中的章节。以 PostgreSQL 作为该流水线的源并跨 kill 随 I222 完成：`TestEmbedPostgresAcrossKills`，矩阵的 PostgreSQL
  源与负载经 `embed` 写入两者。

**待审查的决策**（实现时做出，2026-11-10；在用户裁定前有效）：

1. **[kept]**（2026-11-10：稳定，且与 elasticsearch 目标的 `_id`
   文本相同；UUIDv5 是命名空间下的 SHA-1，并不更好。）
   **Qdrant 的点 ID**：单个无符号整数或 UUID 字符串原样使用；其他键用其文本（与 elasticsearch 目标的 `_id` 文本相同）的
   SHA-256 前 16 字节构成 UUID，版本 8。备选：fit 自己的命名空间下的 UUIDv5；同时把键的文本存进 payload。
2. **[kept]**（2026-11-10：互通表明从中间和从头重放在两者中都收敛。）
   **一批记录缩减为每个键的最后一条**，删除与 upsert 分别成请求，因为 Qdrant 和 Milvus 都不保证同一请求中同一 ID 两次写入的
   顺序。这样批内不同键之间的顺序无关紧要。
3. **[已裁定 2026-11-10：按建议保留]**（2026-11-10：产品选择；见上方列表。）
   **集合由用户创建**，目标从不创建：维数、距离、索引与 schema 是 fit 无法从记录推断的选择。备选：按第一个向量的维数创建。
4. **[changed]**（2026-11-10：对 Milvus 2.5.17 的探测表明 upsert
   的键会被转换为主键类型，但删除 filter 中另一类型的字面量会被拒绝（1100），所以整数键配
   `VarChar` 主键时每次删除都失败并交给
   `on_error`。现在该目标在第一次删除时读取集合描述，按主键类型写键（f8f9f26f，`TestMilvusDeleteKeyTypes`
   针对 Milvus 2.5）。错误码成立：向量维数不对、缺向量、Int64 无效为 1804，VarChar
   过长为 1100，缺集合为 100（重试）。）
   **Milvus 的错误码**：1 到 9（未就绪、限速与配额）重试，1100 与 1800-1899 视为拒绝（逐条找出），其他错误交给流水线重试；
   主键对单个整数键为 `Int64`，否则为键文本的 `VarChar`；删除用 `pk in [...]`；`username` 与 `password_env` 作为令牌
   `user:password` 发送。
5. **[kept]**（2026-11-10：符合规则 1（普通 goroutine 等待 fib 的异步客户端，从不占用
   worker），失败按记录交给 `on_error`。）
   **`embed` 作为 `BatchTransform` 运行在流水线的分发 goroutine 上**，请求走 fib 的异步客户端并在此等待（从不占用 fib worker，
   规则 1），同时最多 `max_parallel` 个，各自受 `timeout` 与 `attempts` 限制；失败的请求让其记录失败（`on_error`），被拒的请求
   （400、413、422）逐个文本重新请求；超过 `max_text_bytes` 的文本让记录失败而不截断；没有这些字段的记录不加向量直接通过。
6. **[kept]**（2026-11-10：内存上限关心的是字节；float32 就是目标保存的精度。）
   **缓存按字节限制**（`cache_bytes`，32 MiB），最少使用的先淘汰，键为模型、维数与文本的 SHA-256，向量存为 float32（模型本身的
   精度，内存减半；缓存的向量与新取的相同）。备选：按条数限制。
7. **[kept]**（2026-11-10：llm.proxy 的 API
   地址很少变化，每批查一次没有收益；流水线重新加载时会取到。）
   **默认使用 llm.proxy**：`module.Env` 新增 `LLMProxy`；sync 角色的成员改为 `*fitsync.Member{Sync, LLMProxy}`，序列化时只写
   sync；经回环地址调用其 API 并固定其证书，与助手的 `llm_proxy` 提供方相同。`llm.proxy` 的变更在流水线重新加载（其自身变更）
   时生效，不影响正在运行的流水线。备选：每批查一次；任何 llm.proxy 变更都重启流水线。
8. **[kept]**（2026-11-10：已完成：`TestEmbedPostgresAcrossKills`（I222）让
   PostgreSQL 经 `embed` 跨 kill 写入两者。）
   **端到端互通以 `generate` 为源**并在随机时刻 kill，而不是 PostgreSQL（`obs` 的服务器中还没有）；PostgreSQL 跨 kill 属于
   I222（见上方未完成）。

### I222 第二批进入矩阵、基准测试与文档

- **做**：新连接器进入 I192 的恢复矩阵（可作源的：RabbitMQ、NATS、Pulsar、MQTT、SQL Server、Doris），并进入 CI 的
  `sync-interop` 分组（`mq` 任务含 RabbitMQ、NATS、Pulsar 与 MQTT，`mssql` 任务，`olap` 任务含 Doris 与 StarRocks，`obs`
  任务含 Loki、OpenTelemetry、InfluxDB、TDengine、Qdrant 与 Milvus）；I193 的测试工具在有常用对手的地方增加场景（RabbitMQ
  与 MQTT 到 Kafka 对比 Kafka Connect 的连接器，SQL Server 到 Kafka 对比 Debezium，文件到 Loki 对比 Promtail）；
  `docs/sync.md` 与 fit-examples 的示例覆盖每个连接器。
- **完成标准**：CI 的任务全部通过；基准主机的运行写成报告（规则 24）；指南与示例能加载（`TestConfigExamples`、fit-examples
  的 validate）。
- **涉及**：`test/sync`、`.github/workflows`、`bench/sync`、`docs`、fit-examples。
- **完成** 2026-11-10，主会话负责的运行除外（CI 任务、基准主机的计划与报告）：矩阵的第二批（`test/sync/matrix2_test.go`）：
  RabbitMQ、NATS JetStream、Pulsar 与 MQTT 中的 Debezium 事件，经 CDC 读取的 SQL Server，按游标轮询的 Doris，各自跨 kill
  写入六个目标；`obs` 中的 `TestEmbedPostgresAcrossKills`（PostgreSQL 经 `embed` 写入 Qdrant 与 Milvus）与
  `TestInfluxDBFromMQTT`；`run.sh` 的 `mq`、`mssql`、`olap`、`obs` 族，调用各脚本（`up` 与 `down`），矩阵的服务器与源由
  `MATRIX_SERVERS` 选择（各一台，`MQ_SYSTEMS`），各脚本端口错开以便多族同时运行；CI 的 `sync-interop` 增加 `mq`、`mssql`、
  `olap`、`obs`、`matrix-mq`、`matrix-db`，每晚的 `sync-matrix.yml` 为每个新源在其同类的每台服务器上各一个任务；测试工具的
  `rabbitmq-kafka` 与 `mqtt-kafka` 场景（对比带 Apache Camel 连接器 4.14.5 的 Kafka Connect）与 `mssql-kafka` 场景（对比
  Debezium 的 SQL Server 连接器），syncbench 为它们增加的源，以及计划的 S6（文件到 Loki，对比 Promtail）到 S9 及其权重；
  `docs/sync.md`（中英文）与 fit-examples 的九个示例（分支 `sync-round2`）。本机运行过：整个 `obs` 族，除 RabbitMQ 外的 `mq`，
  矩阵中 NATS、Pulsar、MQTT、SQL Server 与 Doris 的源；RabbitMQ 的容器在这台 Mac 上带脚本的挂载无法启动（`.erlang.cookie:
  eacces`），所以其测试与矩阵运行首先在 CI 上进行。顺带修复：`TestNATSStreamAcrossKills` 在其随机源上的竞争，以及
  `TestLokiFilesAcrossKills` 把重放的行计入所等待的行数。
- **备注** 2026-11-11（同步计划中每条记录的 CPU，以及 S9 的延迟）：从 20482001（11-10）到 1f794c1b（11-11），fit 每条记录的
  CPU 在吞吐不变时上升 8–20%（S1 128 字节行 0.99 → 1.18 核，S3 2.23 → 2.40，S9 1.52 → 1.83），全部在 GC 中：S1 的 pprof
  差异把 2.15s 中的 +0.47s 记在 `runtime.scanObject`。每个 `Record` 新增的管道名与 lineage（两个字符串）使它从 504 字节变为
  536 字节，超过 512——Go 不带 malloc 头、按 span 标记的最大对象——于是每条记录从 512 字节的尺寸级别移到 576 的级别，并改由
  `scanObject` 扫描；`emit` 还为每条记录读一次时钟（EmitPosition 的空闲间隔）。已修复（bc1f6f7b）：用一个指向本次运行常量的指针
  （`recordRun`）代替两个字符串，`posOnly`、`made` 与 `sub` 放进 `Op` 之后的填充（重回 504 字节；`TestRecordSize` 保证不超过
  512），`emitPosition` 从第一次发现自上次调用以来没有发出记录的调用开始计算空闲间隔，`entry` 重回 72 字节。新增的
  `BenchmarkPipeline`（一个按行读的源经 dispatch 到一个丢弃记录的目标）在基准主机上每条记录的 ns 与字节：20482001 427/914，
  1f794c1b 505/987，修复后 426/916。主机上全量运行，仅 fit，各一轮，本构建与 1f794c1b 先后运行，strata（LLM 服务）在运行：
  S1 128b-1f 1.03 对 1.17 核（均为 91.2 万条/秒；11-10 报告 0.99，11-11 报告 1.18），S3 2.22 对 2.27（均为 29.9 万；2.23、
  2.40），S9（两者各九轮，交替运行）每条记录 CPU 5.13 对 5.38 µs（−4.6%；11-10 报告 5.17，11-11 报告 5.32），吞吐在轮间 ±12%
  的波动内相同（中位数 30.5 万与 32.3 万）。两部分中几乎全部来自记录的大小：本构建在 `emit` 中重新读时钟为 5.14 µs，只改了
  `emitPosition` 的 1f794c1b 为 5.31。S9 的 p99 延迟（1,597 → 2,408 ms）不是代码的回归：各九轮中 1f794c1b 的 p99 为
  1,579–2,419 ms（中位数 1,879），本构建为 1,379–2,319（中位数 1,820）。CDC 的捕获作业每秒扫描一次，源追上后也每秒轮询一次
  （`poll_interval`），所以一次运行的延迟取决于两个定时器的相对相位，在一次运行内固定、在运行之间随机：11-11 同一构建的三轮
  p50 为 1,439、1,008、1,398 ms。p50 确有差别：1f794c1b 中位数 1,289 ms（979–1,468），本构建 879（748–1,379），11-10 为
  859–997；改动两部分各三轮（见上）未能分辨是哪一部分，也不知道其中任何一部分如何影响延迟；留作未决。未改动：更短的
  `poll_interval`（Debezium 默认 500ms）会缩小这一波动，代价是空闲时更多查询。

**待审查的决策**（实现时做出，2026-11-10；在用户裁定前有效）：

1. **[kept]**（2026-11-10：只有这样队列才携带矩阵要检查的删除与键。）
   **矩阵中的队列携带 Debezium 事件**，与 Kafka 的运行相同，键由 `key` 变换从 id 构造（它们的消息没有编解码器能读的键）；
   每个源的负载写在一个队列、流、主题或 MQTT 主题上，所以同一键的变更保持顺序。备选：普通 JSON 行、不经信封（那样就没有删除）。
2. **[kept]**（2026-11-10：Doris 的事务只能插入，软删除是轮询源看到删除的方式。）
   **矩阵中的 Doris 软删除**：删除设置 `deleted` 标记（源的 `deleted_column`）并增加游标 `ver`；改键为一次删除加一次插入；
   该标记在写入目标前由 `fields` 变换去掉；不用多语句事务（Doris 的事务只支持插入）。
3. **[kept]**（2026-11-10：由 runner 的内存决定；SQL Server、Doris、Pulsar
   与六个目标放在一个任务里装不下。）
   **CI 把第二批的矩阵作为另外两个任务运行**（`matrix-mq`、`matrix-db`），各自带六个目标的服务器与每种源一台，而不是放进第一批的
   任务——那样带上 SQL Server、Doris 与 Pulsar 会超出一台 runner 的容量。`MATRIX_SERVERS` 中写上第二批的族时只运行其源；
   第一批的源留在 `matrix` 中。
4. **[kept]**（2026-11-10：各族必须能一起运行；这些开关让每种只启动一台。）
   **端口错开**：NATS 29440、MQTT 29450、Pulsar 29540（原先都占 29420 或 29500），并以 `NATS_VERSIONS`、`PULSAR_VERSIONS`、
   `RABBITMQ_IMAGES`、`EMQX`、`MSSQL_VERSIONS`、`OLAP` 让矩阵各启动一台；`FIT_SYNC_INTEROP=required` 下 `natsClusters` 与
   `pulsarServers` 现在至少需要一台，而不是两台都要。
5. **[kept]**（2026-11-10：两个测试用到的服务器不值得自成一族。）
   **`obs` 族为其两条流水线的测试启动 PostgreSQL 17 与 Mosquitto**，而不是让它们自成一族。
6. **[kept]**（2026-11-10：由许可证决定；基准主机的冒烟运行确认了配置正确。）
   **RabbitMQ 与 MQTT 的对手用 Apache Camel 的 Kafka 连接器**（Apache 2.0），装在 Debezium 的 Kafka Connect 镜像中，安装包以
   sha256 固定；Confluent 的 RabbitMQ 与 MQTT 连接器是商业许可，Lenses 的部分需要许可证密钥。基准主机的冒烟运行（2026-11-10）
   确认 kamelet 的属性名无误；RabbitMQ 连接器不声明任何东西（`autoDeclare` 为 false），读 syncbench 建的持久队列 `bench`，因为它自己
   声明的队列不是持久的，RabbitMQ 会拒绝。Mosquitto 的队列不设上限（`max_queued_messages 0`），让暂停的对手的消息等着它。
7. **[kept]**（2026-11-10：两个对手读同样的变更表，捕获作业不限制任何一方。）
   **为基准调整 SQL Server 的捕获作业**（每秒一次，每次扫描 5000 个事务，100 次扫描），使 CDC 的捕获不成为任何一方的瓶颈；
   两者读同样的变更表。
8. **[changed]**（2026-11-10：用户裁定保持权重，并把 MQTT 到 InfluxDB 作为 S10 加入，对比 Telegraf 1.40.3
   （`bench/sync/run.sh mqtt-influxdb`，`plan.sh` 的 `s10-mqtt-influxdb`）。S10 的权重按对保持的权重改动最小的方式得出：
   5%，即计划中最小的工作负载权重（S6 的），整个从最大的 S1 中拿出，于是 S1 由 20% 变为 15%，S2 到 S9 不变；总和仍为
   100%。现为 S1 15%、S2 15%、S3 15%、S4 10%、S5 15%、S6 5%、S7 7%、S8 6%、S9 7%、S10 5%。）
   **计划的权重**：S1 20%、S2 15%、S3 15%、S4 10%、S5 15%、S6 5%、S7 7%、S8 6%、S9 7%：第一批保留四分之三，大致维持原有比例。
9. **[kept]**（2026-11-10：已完成：fit-examples 的
   main（9659d7e）含这九个示例，`go.mod` 指向 fit 1fdb2407。）
   **fit-examples 的九个示例需要带第二批的 fit**（fit 推送后 `go.mod` 移到那个提交）；本机经指向本工作树的 `go.work` 验证。

**待审查的决策**（规划时做出，2026-11-10；在用户裁定前有效）：

1. **[kept]**（2026-11-10：决策 036 第 1 条；每种协议都已不用驱动实现并通过互通。）
   **每种协议都在 fit 中实现，与第一批相同**：AMQP 0-9-1、NATS、Pulsar 的二进制协议（手工编码 protobuf）、MQTT、TDS。
   备选：用驱动（amqp091-go、nats.go、pulsar-client-go、paho、go-mssqldb）——工作量更小，但违反决策 036 第 1 条，且依赖树
   很大（尤其是 pulsar-client-go）。
2. **[kept]**（2026-11-10：I211-I214 按此实现；矩阵中的队列跨 kill 没有丢失。）
   **位置由代理自己保存的（RabbitMQ 队列、NATS consumer、Pulsar 订阅、MQTT 会话）只在目标拿到后才确认**，fit 的检查点中
   不存；代理有偏移的（RabbitMQ stream 队列、JetStream 序号、Pulsar 消息 ID）也存入检查点，使丢失的 consumer 不会丢失位置。
3. **[kept]**（2026-11-10：I216 按此实现，change tracking 也有。）
   **SQL Server 经其 CDC 变更表，而不是读取事务日志**；日志格式没有文档。可选更轻量的 change tracking。
4. **[kept]**（2026-11-10：I218 按此实现；一个目标和一个源，用 `flavor` 区分。）
   **Doris 与 StarRocks 共用一个连接器**，用 `flavor` 参数区分，因为 Stream Load 与 unique 模型的删除标记只是名字不同。
5. **[kept]**（2026-11-10：I221 按此实现；端到端测试使用它。）
   **向量目标配一个 `embed` 变换**调用 embeddings API，使从数据库到向量库的流水线无需别的组件；配置了 fit 自己的 `llm` 代理时
   默认使用它。
6. **[kept]**（2026-11-10：已遵循，并行实现可行。）
   **顺序**：I211–I214（各队列，彼此独立），I215 之后是 I216 与 I217（SQL Server），I218 到 I221（基于 HTTP 的目标，独立且
   成本低），然后 I222。队列与 HTTP 目标可以像第一批一样并行开发。

## BitTorrent：下载与做种、tracker、发布

2026-11-08 规划，[决定 037](decisions/037-bittorrent.zh-CN.md)。fit 在 fib 的 `bittorrent` 包上提供 BitTorrent：下载和做种的客户端角色、
边下边播的处理器、tracker 角色，以及把目录发布成种子。fib 的客户端要先补 A75–A79（I196）：它现在在 fib worker 上做磁盘读写和哈希。
第一阶段是 I196–I205；第二阶段（I206–I208）是集群、集群共享的 tracker Store 和 RSS。I197 完成后，I198–I202 可以并行，每项一个 worktree 子代理。

原则，按约束先后：

- **不阻塞 worker**（规则 1）：存储、哈希、`Reader` 的读、生成种子、状态文件都在有界 pool 上；pool 满时暂停读该 peer。
- **一切有界**（规则 11）：任务数、连接数（总数、单任务、半开）、元数据与 .torrent 大小、URL 下载、`watch_dir` 单次扫描、事件队列、
  `bt_stream` 的流和临时添加的任务，以及下载可占的磁盘。
- **状态在 `data_dir`，重启不重新哈希**（A76）；reload 当场改限制（A77），只有 `listen` 或 DHT、uTP 开关变化才重建客户端。
- **一个端口，在 fit 的共享引擎上**（A79），受配置的 `access`（A78）和 `limits` 约束。

### I196 fib 为 BitTorrent 客户端补 A75–A79

- **为什么**：fib 的客户端在读取 peer 消息的 fib worker 上写块、读上传数据、校验 piece；每次启动都重新哈希全部数据；限制在
  `NewClient` 时固定；不能按地址拒绝 peer；总是自建引擎。
- **做什么**：在 fib 中做 A75（存储与哈希放到有界 pool，满时 `HoldReads`）、A76（`Torrent.ResumeData`、`AddOptions.Resume`）、
  A77（`Client.SetLimits`、`Torrent.SetLimits`）、A78（`Config.Filter`、`Client.Refilter`）、A79（客户端跑在调用者的引擎上）；
  文档和待审决定写在 fib 的 `docs/bittorrent*.md`；fit 跟进该 commit。
- **完成标准**：测试表明慢存储不会卡住同引擎的其他连接；带续传数据重启后不为校验读数据就完成；改限制不断开 peer；被拒地址不能经
  TCP 或 uTP 连入，也不会被拨号；两个客户端共用一个引擎，关掉一个另一个不受影响；fib 的 CI、race 与 Linux 测试通过。
- **涉及**：fib 的 `bittorrent`、`bittorrent/peer`、`bittorrent/utp`、`bittorrent/dht`，可能还有 `core`；fit 的 `go.mod`、
  `docs/fib-api-requests*.md`。
- **完成**于 2026-11-08，fib `1f1d2e7`（A75–A79 的实现见 [fib-api-requests](fib-api-requests.zh-CN.md)）；fit 已在其上。四个 worktree
  子代理各做一项（A78 与 A79 一起），由主会话合并；合并 A75 与 A76 时，续传计划和 `ResumeData` 改为经 A75 的保护访问存储；
  `TestResumeRejected` 在 Linux 上有竞态（已在测试中修复）。验证：macOS 上 fib 的 `go test -race ./...`，Linux（golang:1.27.3）上
  `./bittorrent/...` 与 `./core`，FreeBSD 与 Windows 的 vet。

**待审阅的决定**（实现时做出，2026-11-08；详情与备选见 fib 的
[docs/bittorrent.zh-CN.md](https://github.com/lesismal/fib/blob/main/docs/bittorrent.zh-CN.md)）：

1. **A75**：每个客户端一个磁盘 pool、一个 FIFO 队列；`DiskWorkers` 为 min(GOMAXPROCS, 8)；只有收到的块计入 64 MiB 上限，
   且是软上限（worker 上从不拒块，暂停的连接在降到一半时恢复）；发现存储已关闭的写入被丢弃；关闭只等正在进行的 I/O；
   初始校验、web seed 写入、`Reader` 读取和 `StorageOpener` 不在 pool 上（opener 在磁力链 info 到达时在 worker 上调用）。
2. **A76**：调用者保存续传数据；信任大小和 mtime；数据中没有的 piece 和未完成的 piece 重新下载；无法使用的数据报告为包装
   `ErrResumeData` 的 `EventError`；`ResumeGen` 是"该保存了"的信号；校验期间 `ResumeData` 返回错误。
3. **A77**：独立的 `Limits` 类型；零值保持 `Config` 中的含义；`TorrentLimits` 中零值继承、负值不限；下载令牌在请求时扣除，
   取消不退还（几秒内约 1.5 倍超出）；最没用的 peer 先关。
4. **A78**：一个 `netip.AddrPort` 谓词；UDP 绑定不过滤（UDP tracker 共用），改由 uTP 和 DHT 过滤；`Refilter` 与设置策略分开，
   fit 一次变更可以重新过滤多个客户端。
5. **A79**：用 `Config.SharedEngine` 而不是第二个构造函数；引擎必须已在运行。

遗留：fib 文档说客户端在 hybrid 磁力链下载后计算 v2 piece layer，但 `Torrent.computeLayers` 从未被调用；在接上并测试之前，
磁力链得到的 hybrid 任务做种时无法应答 v2 的 hash 请求。

### I197 `bittorrent.client` 角色

- **为什么**：其他各项都建立在它上面。
- **做什么**：fit 的 `bittorrent` 包，注册 `bittorrent.client` 角色；配置（`listen`、`data_dir`、`download_dir`、`encryption`、`dht`、
  `pex`、`utp`、`lsd`、`limits`、`access`、`console`），默认值按决定 037；客户端跑在共享引擎上；状态文件（任务、分类、优先级、
  .torrent、续传数据；临时文件、fsync、rename，至多每几秒一次以及停止时）；启动时恢复；reload 原地生效；schema、参考文档、`schema-zh.js`。
- **完成标准**：添加的任务在重启后仍在，且不经校验就续传；reload 改限制保留 peer；改 `listen` 保留任务；慢 peer、断连、磁盘错误测试；
  中英文文档。
- **涉及**：`bittorrent`、`app`、`config`、`schema`、`docs`。
- **完成** 2026-11-09：`bittorrent` 包（`role.go`、`state.go`、`api.go`），角色在 `app/standard` 注册；配置
  `bittorrent.client`（`config/bittorrent.go`）及其检查，其端口加入地址冲突检查（TCP 和 UDP），新增 `bittorrent` 日志记录器；
  schema、参考、模板、`schema-zh.js`；中英文 [BitTorrent](bittorrent.zh-CN.md) 文档。角色控制台的代码从
  `tunnel/internal/common` 移到 `app/roleconsole`，由隧道的角色和本角色共用。测试：经 app Manager 启停角色；两个角色在回环上
  下载；重启后不读取数据即拥有全部分片（统计存储读取次数）；磁力链接的 .torrent 被保存并据以恢复；reload 改限制保留 peer
  和客户端；改 `listen` 保留任务（端口被占时恢复旧客户端）；改 `data_dir`；限速（慢）链路下载中途断开做种方并换另一个；
  access 关闭已连接的 peer；存储写入错误；状态目录无法写入；损坏和超大的状态文件；损坏的任务保留在列表中；API 的拒绝
  （上限、错误输入、通向 `download_dir` 之外的名称）；控制台的路由。

**待审查的决定**（实现时做出，2026-11-09；在用户裁定前有效）：

1. **状态是三类文件而不是一个**：`torrents.json`（列表）、`torrents/<id>.torrent`、`resume/<id>.resume`，各自经临时文件、同步、
   改名写入。原因：.torrent 可达数 MiB（v2 分片层、大量文件），续传数据随分片和文件数增长，单个文件每次保存都要重写所有任务的
   字节；按任务分文件只重写变化的部分。备选：一个包含全部内容的 bencode 或 JSON 文件；嵌入式数据库。
2. **无法读取的状态文件使角色不启动**（`ErrState`，错误中给出文件；配置的其余部分照常运行）：JSON 无法解析、版本更新、超过
   100000 个任务、ID 重复或格式错误、相对目录、或超过 64 MiB。原因：以空列表启动会在下次保存时覆盖列表、丢失所有任务。
   备选：把坏文件改名留存（`torrents.json.bad-<时间>`）后以空列表启动。
3. **自身文件无法读取的任务保留在列表中**，带错误（状态 `error`）并照常保存，直到被移除，其他任务照常运行；无法读取的续传
   数据被丢弃，该任务全部重新校验。备选：丢弃这样的任务并记一行日志。
4. **`listen` 和 `data_dir` 必填；`download_dir` 默认 `<data_dir>/downloads`。** 原因：只在配置写明的地方打开 BitTorrent 端口，
   不隐式采用 fib 的 `:6881`。备选：`listen` 默认 `:6881`；`download_dir` 必填。
5. **用 `disk_queue_bytes` 而不是 `disk_queue`**，遵循 fit 的 `*_bytes` 命名；开放 **`save_interval`**（默认 5s）。列表的变化
   （添加、移除、暂停、恢复、优先级、顺序下载）在 API 调用中立即保存；续传数据在 `ResumeGen` 变化时由保存协程写入。
6. **哪些更改新建客户端**：`listen`、`encryption`、`dht`、`pex`、`utp`、`lsd`、`disk_workers`、`disk_queue_bytes`（都只在 fib 的
   `NewClient` 中读取），以及 `data_dir`。**新的 `data_dir` 像重启一样从该目录的状态开始**，旧目录保留已保存的内容。备选：把
   任务迁到新目录；拒绝这项更改。
7. **新客户端无法创建时**（端口被占）**恢复旧客户端**，用旧配置和同样的任务，reload 报告错误；两者之间客户端短暂不可用。
   备选：关闭旧客户端前先探测新端口（旧客户端占着同一端口时无法探测）。
8. **任务 ID** 是 v1 info hash 的十六进制，或截断到 20 字节的 v2 hash，即 fib 的 `Client.Torrent` 所查的值。
9. **因做种上限暂停的任务保存为暂停**，以免重启（fib 的上传计数从零开始）后再次做种。做种比例和时间从添加或 fit 启动时
   算起（fib 的计数不持久化）。备选：在状态中保存上传和下载总量（供 I203 的统计）。
10. **存储错误**（fib 的 `EventError`，任务继续运行）保留在任务状态中，直到有分片校验通过或状态改变，每种只记一次日志；
    `ErrResumeData` 只记日志。
11. **`max_torrents`** 默认 1000（最多 100000）；调低时不移除任务、拒绝新增；状态中超过它的任务全部恢复。
12. **角色控制台的代码移到 `app/roleconsole`**，与隧道的角色共用，而不是复制；控制台在 I203/I204 之前只读地提供
    `GET /api/torrents` 和 `/api/torrents/<id>`。
13. **角色的 `access`（A78）放在 I197**，按任务说明：每次 Apply 设置 gate 后调用 `Client.Refilter`；`.p2p`/`.dat` 列表格式仍在 I199。
14. **不可配置**：peer ID 前缀（fib 的 `-FB0001-`）、tracker 开关、每秒拨号数、DHT 路由节点（公共节点）、预分配。

遗留：fib 客户端为 `OnEvent` 排队事件用的是无上限的切片；fit 的处理只取一个短锁，队列会排空，但上限应在 fib 中（需要时提 fib 请求）。

### I198 添加任务、分类与排队

- **做什么**：磁力链、.torrent 上传、.torrent URL（fit 的 HTTP 客户端，限大小）、`watch_dir`（.torrent 和 .magnet 文件，添加后移走）；
  分类有目录、速率、做种限制，可选绑定 `media_library`，完成后重扫；`max_active_downloads` 与 `max_active_seeds` 加排队；
  下载前和下载中检查剩余空间（medialib 的 diskfree），不足时暂停并报错；路径限制在分类目录内。
- **完成标准**：每种添加方式都有测试，包括错误和超大输入；下载到库根目录的完成任务出现在其目录中；一个任务完成时队列启动下一个。
- **涉及**：`bittorrent`、`medialib`。
- **完成**于 2026-11-09，与 I199 一起：`bittorrent/manage.go`（管理 goroutine：队列、磁盘检查、备用速率、每个种子的限制、
  完成）、`fetch.go`（在 fib 的异步 HTTP 客户端上的 `AddURL`）、`watch.go`（`watch_dir`）；配置 `categories`、`watch_dir`、
  `watch_interval`、`watch_max_files`、`url_timeout`、`limits.max_active_downloads`、`max_active_seeds`、`min_free_bytes`
  及其检查；`medialib.DiskFree`；状态保存每个种子的分类、自己的限制和是否完成，列表顺序即队列顺序。API：`Add` 带
  `Category` 和 `Limits`，`AddURL`，`SetCategory`，`SetTorrentLimits`，`SetQueuePosition`，`AltSpeed`，`SetAltSpeed`；状态
  给出分类、自己的和实际生效的限制、队列位置、是否完成，以及角色暂停它的原因。测试：分类（目录、限制合并、重载就地改变
  分类限制、跨重启保留、未知和跑出目录的被拒绝），下载队列（一个完成后下一个开始、队列位置把一个移到前面、跨重启的顺序），
  做种队列（暂停和移除让出位置），磁盘（空间不足不启动、空间够了启动、慢速对端下载中途空间耗尽时暂停、做种不受限），
  下载到媒体库根目录的种子出现在其目录中、只扫描一次、重启后不再扫描，`watch_dir`（.torrent、.magnet、分类子目录、坏的、
  空的、隐藏的和其他文件、已存在的种子），其上限（每次查看的文件数、正在写入的文件、因 max_torrents 留待以后、过大的文件、
  不存在的目录），URL（重定向、重定向到磁力链接、错误的 scheme、错误的应答、重定向循环、从不应答的服务器、响应体中途断开、
  超过上限的响应体、同时超过 MaxURLFetches 个）。

**待评审的决定**（实现时所做，2026-11-09；在用户裁定前有效）：

1. **URL 下载**：响应体上限 32 MiB，而非 fib 元数据的 128 MiB 上限；同时最多 4 个，再多一个立即拒绝（`ErrBusy`）而不排队，
   因此最多持有 128 MiB；最多 5 次重定向，包括重定向到磁力链接；`url_timeout`（30s）约束整个下载。原因：.torrent 响应体
   在添加前整体持有，实际的远小于 32 MiB。备选：fib 的上限；有界的 URL 添加队列。
2. **`AddURL` 等待下载完成**，在调用者的 goroutine 上（从不在 fib 的 worker 上；fib 的回调只把应答交出去），返回种子的
   状态。备选：立即返回一个任务，由 Admin API 报告进度（I203）。
3. **URL 不做内网地址防护**：API 属于运维者（Admin API、CLI、带认证的页面）。备选：像 upstream 的 `deny_internal` 一样
   拒绝环回、链路本地和私有地址。
4. **`watch_dir`**：分类的文件是以其名称命名的子目录中的文件，其他文件不带分类；文件在连续两次查看中大小和修改时间都不变后
   才被取走（而不按文件年龄，假时钟或慢速复制会骗过它）；处理过的文件移入旁边的 `.done` 或 `.failed`（`<name>.error`
   文件说明原因），种子已存在算作完成，可能会过去的错误（`max_torrents`、状态写不进去、客户端未运行）使文件留下，每次
   查看再试。默认：每 10s 一次，每次最多 100 个文件（最多 10000），每个文件最多读 16 个目录项。备选：删除处理过的文件；
   所有文件共用一个 `.done`；为监视目录指定分类或暂停添加的选项。
5. **分类的限制是每个种子的限制**（fib 的 `TorrentLimits`），而不是分类内种子共享的一份额度：fib 只有客户端和每个种子的
   令牌桶。种子自己的限制逐字段取代分类的。备选：每个分类一个共享令牌桶（需要时向 fib 提请求）。
6. **分类的相对 `dir` 留在 `download_dir` 之下**；绝对路径按原样使用（配置属于运维者）。**`SetCategory` 不移动文件**：
   fib 无法移动运行中种子的存储；种子只换用新分类的限制和媒体库。从配置中删除的分类仍记录在其种子上，种子失去其限制，
   分类恢复后重新获得。备选：移动文件（停止、改名、带续传数据重新添加，或 fib 的 `Torrent.Move`）；拒绝更改分类。
7. **默认不排队**（`max_active_downloads` 和 `max_active_seeds` 为 0），而非 qBittorrent 的 3 和 5：服务器上的种子已受
   `max_torrents` 和连接数限制约束。队列按列表顺序，`SetQueuePosition` 改变顺序，状态文件保存它；被移到正在运行的种子之前
   的种子使其暂停；停滞的种子算作活动；获取元数据的磁力链接和校验数据的种子算作下载中；哪些种子被暂停不保存，启动时
   重新计算。备选：qBittorrent 的默认值；不计入慢速种子；保存暂停状态。
8. **每个种子都以暂停状态加入，由角色启动**（恢复、添加、继续），在同一次持有角色锁期间，使队列和磁盘检查只在一处。
   管理者每秒查看一次（不可配置）。
9. **磁盘检查**：`min_free_bytes` 默认 1 GiB，与媒体库上传相同；暂停中的下载在磁盘空闲空间够它还要下载的量加
   `min_free_bytes` 时启动（同一次查看中先启动的种子预留其所需，按目录而非按设备计），运行中的下载在低于 `min_free_bytes`
   时暂停，空间恢复后自行启动；做种从不暂停；运行中的下载之后还要写的量不预留。备选：无默认值（关闭）；也为运行中的下载
   预留；不自行重新启动。
10. **一次完成只算一次**：每秒轮询（种子的 `Done`），保存在状态中（`completed`），只在 `SetFilePriority` 之后才会再算；
    然后在角色的引擎上按名称查找该分类的媒体库并请求扫描（`Library.Rescan`，会合并请求）。未运行的媒体库写入日志。
    备选：fib 的 `EventCompleted`，重启时的校验会再次发出它。
11. **medialib 增加 `DiskFree`**，是其 diskfree 辅助函数的简单导出；重新扫描的 API 原已存在。

遗留：fib 没有按分类（组）的速率令牌桶，也无法移动种子的存储；角色的新 API 尚未出现在其控制台的 HTTP 路由上（I203）。

**合并 I198 与 I200 时的决定（待审阅）：** `bt_stream` 临时添加的种子不排队，也不占 `max_active_downloads` 或
`max_active_seeds` 的名额，因为有观众在等，且这类种子已受 `max_temporary` 和 `max_temporary_bytes` 限制；磁盘检查照常适用
（`TestTemporaryNotQueued`）。备选：与其他种子一样排队，那样观众要一直等到 `read_timeout`。

### I199 速率调度、单任务限制与黑名单

- **做什么**：`alt_speed` 按周调度；分类和单任务限制（A77）；角色上的 `access` 经 A78 生效，策略变化后调用 `Client.Refilter`；
  `access` 的名单文件也读 eMule 的 `.p2p` 和 `.dat`，按内容识别格式，所有模块可用。
- **完成标准**：用假时钟测试调度切换速率；拒绝名单会关闭已连接的 peer；`.p2p` 和 `.dat` 的解析（范围、注释、级别、坏行）在 `access` 的测试中覆盖。
- **涉及**：`bittorrent`、`access`、`config`、`docs`。
- **完成**于 2026-11-09，与 I198 一起：`access` 逐行读取名单文件，每行是地址或前缀、范围、eMule 的 `ipfilter.dat` 行或
  PeerGuardian 的 `.p2p` 行，所有模块可用（`access/lists.go`；其集合由范围构建）；`alt_speed`（速率、每周 `schedules`、
  `time_zone`）由管理者按角色的时钟应用，API 的 `on`/`off`/`auto` 保存在状态中；分类和单个种子的限制经 fib 的
  `TorrentLimits` 生效，出现在 API 和状态中；角色的 access 在每次 Apply 时已经重新过滤（I197），名单文件变化会再次应用
  配置。测试：`access`（范围、`.dat` 级别和前导零、名称含冒号、逗号、短横线和 `#` 的 `.p2p`、注释、给出文件和行号的坏行、
  过长的行、经策略使用的混合文件、范围组成的允许名单、范围与线性检查对照）；时间表（跨午夜的时间段、到 24:00 的周末、
  时区）以及角色在假时钟上切换速率、API 的选择跨重启保留、去掉 `alt_speed` 的重载；`.p2p` 拒绝文件关闭已连接的对端，
  level 大于 127 的 `.dat` 行不拒绝。

**待评审的决定**（实现时所做，2026-11-09；在用户裁定前有效）：

1. **level 大于 127 的 `ipfilter.dat` 行不列入**，无论文件用在哪里（因此拒绝文件屏蔽 level 0 到 127）：这是 qBittorrent
   的读法。eMule 在默认过滤级别 127 下只屏蔽低于 127 的级别，因此 level 恰为 127 的行在这里被屏蔽，在 eMule 中不被屏蔽。
   备选：eMule 的 `< 127`；可配置的级别。
2. **逐行区分格式**，不按文件名或文件头：先试地址或前缀，再试单独的范围（`first-last`，也可单独使用），再试 `.dat`
   （`first - last , level , name`），再试 `.p2p`（最后一个 `:` 之后的范围）。行尾的 `#` 或 `;` 注释只从普通格式中去掉，
   因为 `.p2p` 名称可能含 `#`；以 `#`、`;` 或 `//` 开头的行是注释。范围中的 IPv4 地址可带前导零；也接受 IPv6 范围。
3. **坏行仍使整个文件出错**，给出文件和行号，与以前相同（配置不应用，旧配置保留），而不像 eMule 和 qBittorrent 那样跳过。
   原因：悄悄少列的名单对允许名单更糟。备选：跳过坏行并在一条日志中计数。
4. **`access.ReadFile` 返回范围**（`[]access.Range`）而非前缀，一行可达 16 KiB（原为 4 KiB）；范围不拆成前缀，因此公开的
   名单每行只占一个范围。
5. **备用速率**：0 表示不限（qBittorrent 的做法）；只切换两个速率，不切换连接数限制；时间表的 `days` 是时间段开始的日子，
   `to` 早于 `from` 的时间段延续到第二天，`to` 可为 `24:00`（`00:00` 表示结束于午夜）；`time_zone` 默认本地，与 fit 的日志
   相同；API 的 `on`/`off` 在回到 `auto` 之前取代时间表，并保存在 `torrents.json`（`alt_speed`）中；配置中没有 `alt_speed`
   时速率从不切换。备选：单独的备用连接数限制；不保存该选择。
6. **单个种子的限制是 fit 的 `BitTorrentTorrentLimits`**，含义同 fib 的 `TorrentLimits`（0 采用客户端的，负数表示该种子
   不受限），状态只保存种子自己的限制，应用时合并在其分类的限制之上。

### I200 `bt_stream`：边下边播

- **做什么**：路由处理器，按 info hash 加文件序号或路径提供任务中的文件，支持 Range；请求的区段经 fib 的 `Reader` 在有界 pool 上读取，
  因此优先下载；`max_streams`、读超时、预读上限；`media_auth`；允许按磁力链临时添加，有数量与磁盘上限，最后一个流结束后过期。
- **完成标准**：对仍在下载的文件发出浏览器式的 Range 请求，在其 piece 到达后得到回应；慢读者、断连、流数已满都有测试。
- **涉及**：`bittorrent`、`handler`、`mediaauth`。
- **完成** 2026-11-09：路由处理器 `bt_stream`（`handler/btstream`，加入 `modules/standard`）：`<prefix><info hash>/<file path>`、
  `/<n>`（文件索引）和 `/`（最大的文件），`GET` 和 `HEAD`，单个 `Range`（206、416），针对由种子和文件组成的 `ETag` 的
  `If-None-Match`/`If-Range`，按扩展名的 `Content-Type`，`Accept-Ranges`；经 fib 的 `Reader` 在流自己的 goroutine 上读取，
  最多 `max_streams` 个，用 `WriteNoWait` 写出、由 `OnWritable` 继续；`read_timeout`、`readahead_bytes`、`info_timeout`；
  `auth`（对 `bt/<info hash>` 的 play）；设置 `magnet` 时按 `magnet` 查询参数添加（需要 publish），临时种子受 `max_temporary`
  和 `max_temporary_bytes` 限制，只下载被播放的文件，在最后一个流结束 `temporary_ttl` 后以及角色启动时连同数据移除。角色的
  钩子：`AddTemporary`、`SetTemporary`、`RemoveTemporary`、`TemporaryBytes`、`SetFilePriorities`、`Status.Temporary`，
  `torrents.json` 中的 `temporary`。schema、参考、模板、`schema-zh.js`，中英文
  [BitTorrent](bittorrent.zh-CN.md#边下载边播放) 文档；`test/workers` 指向该处理器自己的 `TestNoWorkerBlocks`。测试（两个角色
  在回环上，关闭 DHT 和 LSD，没有 tracker）：对尚无任何分块的文件请求一个范围，加入做种方后得到回应；按路径、索引和最大
  文件取整个文件；后缀范围和开放范围、416、HEAD、304、`If-Range`、多个范围；客户端停止读取的 24 MiB 文件（交给连接的数据
  稳定在远小于文件的量，macOS 上一次运行为 768 KiB，之后文件完整到达）；等待分块时和流式发送时断开连接（流及其对种子的占用结束）；`max_streams` 已满；
  `read_timeout`；角色未运行；临时种子的生命周期（无 publish 时添加得到 401/403、另一种子的磁力链接、优先级、
  `max_temporary`、停留期后连同数据移除、再次添加），另一个流读取时保留，`max_temporary_bytes`；经请求头和查询参数的播放
  授权；`Drain`；流等待时 ServeHTTP 已返回；`parseRange`；参数检查；角色的钩子，以及下次启动时移除临时种子。

**待审查的决定**（实现时做出，2026-11-09；在用户裁定前有效）：

1. **URL**：`<prefix><info hash>/<file path>`（fib 的 `File.Path` 给出的路径，以种子名称开头），`<prefix><info hash>/<n>` 为
   索引 `n` 的文件，文件名只由数字组成时路径优先于索引，`<prefix><info hash>/` 为最大的文件。原因：播放器能看到文件名和扩展名，
   索引方便脚本，最大的文件通常就是磁力链接所指的内容。备选：索引放在查询参数（`?file=n`）或单独的路径段（`/i/<n>`）；不提供
   最大文件的形式。
2. **即时添加用文件 URL 的 `magnet` 查询参数**，它必须是路径中那个种子的链接（否则 400）；此时请求除 `play` 外还需要对
   `bt/<info hash>` 的 `publish`，且 `magnet` 需要 `auth`（没有则拒绝配置）。原因：播放器保留的 URL 无论种子是否已在都对每个
   范围有效；从互联网下载是比播放更强的权限。备选：路径形式（`<prefix>magnet/<link>`）；先单独请求添加，再用普通 URL；
   play 即可添加；允许无 `auth` 的 `magnet`。
3. **有界池是每个流一个 goroutine，最多 `max_streams` 个**，没有队列：超过时 503。goroutine 会等待分块（受 `read_timeout`
   限制），但从不等待客户端：`WriteNoWait` 报告超过水位线时它结束，由 `OnWritable` 启动另一个。原因：读取只能阻塞，每个流同时
   只需要一个；带队列的共享池会让某个种子缺失的分块拖慢其他流。备选：固定 worker 加队列的共享 `filepool`；也等待客户端的
   goroutine。
4. **`max_streams` 只计一个处理器（一条路由）自己的流**，重载时不共享：旧配置的流在其排空时结束。备选：按引擎和前缀共享的
   计数。
5. **响应随第一个数据块开始**：第一次读取超过 `read_timeout` 得到 504，之后的超时使响应在达到 `Content-Length` 前中断。
   备选：一知道范围就立即发送响应头。
6. **临时种子只下载被播放的文件**（其他文件优先级为 0，请求到时再提高）；`max_temporary_bytes` 计算它们想要的字节（fib 的
   `Stats.Wanted`），超过时回答 **507**，刚添加的种子被移除。接近上限时同时添加的两个种子可能都被拒绝。备选：下载整个种子；
   按总长度计算；检查磁盘剩余空间（I198 的 diskfree 落地后）。
7. **临时种子在角色的状态中标记**（`torrents.json` 中的 `temporary`），角色读取状态时（启动、新的 `data_dir`）连同数据移除。
   原因：否则重启会把它们变成永不移除的普通种子。备选：不持久化；重启后保留并重新计时。
8. **停留计时是共享的**，所有 `bt_stream` 处理器之间、跨重载，按角色和种子；`temporary_ttl` 取最后结束的那个流所在处理器的值；
   `RemoveTemporary` 只移除仍是临时的种子，因此被改为普通种子（`SetTemporary(id, false)`）的会保留；种子正被移除时到来的流
   先等待，再重新添加或得到 404。
9. **多个范围以整个文件回答**（200），而不是 `multipart/byteranges`：播放器每次只请求一个范围。`ETag` 是强校验的
   `"<info hash>-<index>"`；没有 `Last-Modified`。
10. **默认值**：`max_streams` 32（最多 4096），`read_timeout` 60s，`readahead_bytes` 8 MiB（64 KiB 到 256 MiB），
    `info_timeout` 60s，`max_temporary` 4（最多 1000），`max_temporary_bytes` 16 GiB，`temporary_ttl` 10m（最多 7 天）；
    数据块 64 KiB，即 fib 的水位线。
11. **回答**：角色未运行 503（`Retry-After: 5`）；达到 `max_temporary` 或 `max_torrents` 503（`Retry-After: 30`）；`magnet`
    关闭 403；角色没有且未带磁力链接的种子 404；尚未开始的流下种子被移除 503。
12. **`Content-Type` 用处理器自己的表**（视频、音频、字幕、图片），而不是 Go 的 `mime`，后者首次使用时会读取系统文件。

遗留：通过 API 添加一个已作为临时种子持有的种子会得到 `ErrExists`；把它变为普通种子（`SetTemporary(id, false)`）属于
I198/I203 的添加路径。fib 的 `Reader.Close` 会等待进行中的 `Read`（两者取同一把锁），因此处理器先取消读取的 context；能打断
读取的 `Close` 可在别的使用者需要时作为一个小的 fib 请求。未运行：Linux 上的 race 测试和全量测试（留给主会话）。

### I201 `bittorrent.tracker` 角色

- **做什么**：在 fib 的 `tracker.Server` 上提供 `bt_tracker`（HTTP announce 与 scrape）和角色的 UDP `listen`；公开或私有（passkey 来自
  `media_auth`），info hash 白名单（内联或文件，监视变化）；统计。
- **完成标准**：libtorrent 和 anacrolix 经 HTTP 与 UDP 向它 announce；私有 tracker 拒绝未知 passkey 和 info hash；上限有测试。
- **涉及**：`bittorrent`、`handler`、`mediaauth`。
- **完成** 2026-11-09：包 `bittorrent/tracker`（`tracker.go`、`hooks.go`、`handler.go`、`api.go`），角色 `bittorrent.tracker` 和处理器
  `bt_tracker` 由 `app/standard` 和 `modules/standard` 注册；配置 `bittorrent.tracker`（`config/bittorrent_tracker.go`：`listen`、
  `interval`、`min_interval`、`peer_ttl`、`max_torrents`、`max_peers_per_torrent`、`max_peers`、`max_numwant`、`max_scrape`、`auth`、
  `allow`、`allow_files`、`access`、`console`）及其检查，其 UDP 端口纳入地址冲突检查，其文件被监视（`app.Info.CertFiles`）；`mediaauth`
  中的用户 passkey 和 `AllowPasskey`（`passkey.go`）；schema、参考、模板、`schema-zh.js`；中英文 [BitTorrent](bittorrent.zh-CN.md)
  中的 tracker 一节。测试：经 app Manager 的角色；用 fib 的 tracker 客户端经 HTTP（通过 `bt_tracker` 模块）和 UDP 的 announce 与
  scrape，角色的统计；两个 fib 客户端只经 tracker 互相找到并下载一个私有种子（做种方用令牌经 HTTP，下载方用用户 passkey 经 UDP）；
  用户和令牌的 passkey 放在路径和查询中、经 HTTP 和 UDP，缺失、未知或授权不含该种子时被拒，scrape 亦然，策略未加载时拒绝一切；内联和
  文件中的允许列表，被拒的哈希，文件变化后重新应用，坏行和缺失文件使重载失败且保留之前的列表；存储上限和 `max_numwant`；access 在
  建立连接前拒绝 UDP、以失败原因拒绝 HTTP，重载后解除；原地重载（interval 保留 swarm、端口被占时一切照旧、`listen` 移动和移除、新
  上限新建存储）；逐字节发送 announce 的客户端、中途断开的客户端、垃圾数据报；控制台路由；passkey 脱敏；`mediaauth` 的 passkey
  （keyring、其他密钥、公开播放）；配置检查与冲突。与 libtorrent、anacrolix 的互通在 I205。

**待审决定**（实现时作出，2026-11-09；在用户裁决前有效）：

1. **用户的 passkey 是派生的，不存储**：HMAC-SHA256(策略的签名密钥, "fit bittorrent passkey\0" + 用户名) 的前 32 位十六进制；
   令牌（静态的或签发的）原样作为 passkey；passkey 的授权须能播放 `bittorrent/<十六进制 info hash>`。原因：`media_auth` 已有用户、
   令牌、按模式的授权和密钥轮换，而密码不能放进 .torrent 文件永久保存的 URL；派生无需新状态，轮换 secret 即吊销所有 passkey。有
   keyring 时接受每个密钥的 passkey，给出默认密钥的。备选：`media_auth` 中每个用户一个 `passkey` 字段；角色自己的文件保存 passkey；
   只用长期有效的签发令牌。
2. **私有 tracker 在策略未加载时拒绝一切**（"the tracker's passkeys are not available"）；`public` 含 `play` 的策略让所有人进入，与
   流相同。
3. **存储的默认上限是 fit 的，低于 fib 的**：`max_torrents` 100000（fib 1048576），`max_peers_per_torrent` 10000（65536），
   `max_peers` 500000（2097152），最多约 80 MiB；最大值是 fib 的默认值（`max_peers` 最多 4194304）。备选：用 fib 的默认值。
4. **存储上限或 `peer_ttl` 的更改会新建存储**，swarm 从空开始（客户端在其间隔内重新 announce），因为 fib 的 `MemoryStore` 只在创建时
   接受它们；interval、`min_interval`、`max_numwant` 和 `max_scrape` 在同一存储上新建 fib `Server`，用角色唯一的 UDP 密钥，swarm 和
   UDP 连接 ID 都保留。备选：拒绝此类更改直到重启；等 fib 支持运行时设置上限。
   **I209 已改**：上限和 `peer_ttl` 现在原地修改（fib 的 `MemoryStore.SetConfig`，A85），保留 swarm；它们与 interval 同时改变时，
   先设置上限，再在同一存储上新建 `Server`。
5. **允许列表文件有坏行或无法读取时重载失败**，之前的列表继续生效。原因：否则一个笔误会悄悄拒绝某个种子，缺失文件则拒绝所有种子。
   备选：跳过坏行并警告。
6. **HTTP 的角色 `access` 在 `bt_tracker` 处理器中检查**（BEP 3 风格的失败原因 "refused"），UDP 由绑定的 `Filter` 在建立连接前检查；
   HTTP 由 fit 的监听器接受，其 access 是全局的和监听器的。备选：角色的 access 只管 UDP。
7. **访问日志中永不出现 passkey**：请求 URI 中的 `?passkey=` 总被替换为 `REDACTED`；`announce`/`scrape` 之前的路径段只在私有时替换，
   以便公开 tracker 的 `/tracker/announce` 保持可读。
8. **单个种子的统计按指名的种子给出，列表只列允许列表中的种子**；公开 tracker 无法列出它跟踪的内容，因为 fib 的 `MemoryStore` 无法
   枚举其种子。在存储旁另建索引就是绕过 fib（规则 15），所以改为向 fib 提需求（见下）。计数：已服务的 announce 和 scrape，按原因的
   拒绝（`unregistered` 按每个被拒哈希计，含 scrape；`passkey`；`access`）。
   **I209 已改**：公开 tracker 经 fib 的 `MemoryStore.Range`（A85）按 info hash 列出存储中有 peer 的种子。
9. **用户的 passkey 在角色控制台上读取**，`GET /api/passkey?user=`，在其登录之后，直到 I203 的 Admin API 和 I204 的页面；控制台的
   其他路由是 `GET /api/stats`、`/api/torrents`（`offset`、`limit` ≤ 1000）和 `/api/torrents/<hash>`。
10. **私有 scrape 需要授权覆盖其所有种子的 passkey**；不指名任何种子的 scrape 得不到任何内容，正如 fib 的处理，无论谁来问。
11. **`bt_tracker` 没有参数**：fib 按路径最后一段分派，所以由路由的匹配决定 tracker 在哪里（`/announce`、`/tracker/*`）。
12. **不可配置**：fib 的 `TrustIPParam`（关：peer 记录为其请求来自的地址），默认 numwant（fib 的 50），UDP 密钥（每个角色随机，重载
    时保留，重启时不保留：UDP 客户端会重新连接）。

由此遗留：要向 fib 提的需求——`MemoryStore` 无法列出其种子（`Range(func(InfoHash, Stats) bool)` 可给出公开 tracker 的单种子统计），
也无法在运行时改变上限（`SetConfig(MemoryStoreConfig)` 可让这类重载保留 swarm）；fib 的客户端在做种方检查已有数据时 announce
`started`，检查结束时 announce `completed`，于是做种方被算作一次完成的下载。tracker 还没有指标（I203 的指标）。

### I202 把目录发布成种子

- **做什么**：`publish` 块：把目录生成 hybrid 种子（每个文件一个，或整个目录一个）并做种；`.torrent` 和磁力链经路由给出；web seed 指向
  fit 的静态文件；磁盘变化时重新生成，与静态文件跟随磁盘的方式相同；哈希在有界 pool 上。
- **完成标准**：客户端只从 fit 的做种、或只从 web seed 都能下载发布的文件；文件变化后生成新种子，旧种子停止。
- **涉及**：`bittorrent`、`handler`。
- **完成** 2026-11-09：`bittorrent.client.publish` 条目（`config/bittorrent_publish.go`：`name`、`dir`、`mode`
  `per_file`|`whole_dir`、`web_seed`、`announce`、`private`、`piece_length`、`files`、`exclude`、`check_interval`、`max_files`、
  `max_total_bytes`、`max_hashing`，及其检查）；角色的发布器（`bittorrent/publish.go`、`publish_walk.go`）：每个条目一个遍历
  goroutine 和 `max_hashing` 个生成 goroutine，用 fib 的 `metainfo.Builder` 生成 hybrid 种子，经只读存储在原处做种，保存在
  `data_dir/publish/<name>/` 并按文件标记恢复；角色代码只加了六行钩子（`role.go`：字段、`New`、`Apply`、`restoreAll`、
  `closeClient`、`Stop`）。处理器 `bt_publish`（`handler/btpublish`，在 `modules/standard` 中）：JSON 索引、`<id>.torrent`、
  `<id>.magnet`，没有客户端时 `503`。schema、参考、模板、`schema-zh.js`、模块的章节，中英文 [BitTorrent](bittorrent.zh-CN.md)
  中的发布一节。测试：目录中每个文件由 fib 客户端只从角色的做种下载；`whole_dir` 种子被下载（点文件和符号链接不发布），
  新增文件时被取代；停止发布角色后，只从 fit 自己的 `static` 路由作为 web seed 下载文件；重启不生成任何种子、不读取数据
  （统计存储读取次数）；只改上限的 reload 不生成种子；移除条目（种子停止、状态删除、文件保留）；改变的文件得到新种子，
  旧种子离开客户端且不再提供；删除和新增文件；`max_files`、`max_total_bytes`、`files`/`exclude`、空文件、超过上限的
  `whole_dir` 条目、重复文件、不存在的目录、生成失败后在文件改变前不重试；两种模式的 web seed URL、tracker 和私有标记；
  只读存储；路由（索引、.torrent、磁力链接、HEAD、404、405、指定条目）；配置检查。

**待审查的决定**（实现时做出，2026-11-09；在用户裁定前有效）：

1. **条目放在 `bittorrent.client.publish`**，而不是顶层的 `bittorrent.publish`。原因：发布运行在客户端上，状态保存在其
   `data_dir` 中，嵌套使依赖关系体现在结构上，角色的成员仍是一个结构体（不改共享代码）。没有客户端运行时处理器返回
   `503`。备选：由客户端角色读取 `bittorrent.publish`（其成员类型要变）；一个查找客户端的独立角色。
2. **命名**：`mode` 为 `per_file`（默认）或 `whole_dir`；包含模式叫 **`files`**，与媒体库相同，因为 `include` 是 fit
   .conf 格式的指令。备选：`each`/`whole`；`match`。
3. **发布的种子属于发布器，不在角色的列表中**：不在 `torrents.json` 或 `List` 中，不计入 `max_torrents`，不受做种限制
   一直做种（`TorrentLimits` 为 −1）。原因：它们的生命周期跟随配置和磁盘，而不是 API。备选：作为带条目标记的记录
   放进 `torrents.json`，受 `max_torrents` 限制。
4. **状态及其键**：每个条目在 `data_dir/publish/<name>/` 中保存 `index.json`（每个种子的文件及其 fib `filewatch.Stamp`：
   大小、修改时间、状态改变时间、inode、设备）和 `<id>.torrent`/`<id>.resume`；只有生成键（`dir`、`mode`、`web_seed`、
   `announce`、`private`、`piece_length`）相同时才使用该索引。索引在变化后的下一次遍历、客户端关闭或角色停止时写入，
   而不是每次生成后写入（那样每个文件重写一次，文件多时是平方级）；两者之间崩溃会重新生成这些种子。续传数据在种子拥有
   全部分片后保存一次。备选：只用大小和修改时间，与 fib 的续传数据相同（会漏掉大小和时间都相同的替换）；每次生成后写索引。
5. **稳定**：新增或改变的文件在其标记保持一个 `check_interval` 后才生成种子（状态改变时间已经这么久，或上一次遍历看到的
   相同），以免对正在复制的文件反复哈希。备选：立即生成；单独的 `settle` 选项。
6. **遍历而不监视**：跟随磁盘的方式是每隔 `check_interval`（10s）遍历 `dir`，不用 fib 的 `filewatch` 监视，因为遍历本来
   就要 stat 每个文件以比较标记，而生成要等文件稳定。备选：Linux 上用监视提前下一次遍历。
7. **只读存储**：拒绝写入（空写入即创建空文件，为空操作），因此校验失败的分片绝不会被下载并覆盖发布的文件；这样的种子
   保留（状态会显示），直到文件改变。备选：移除校验失败的种子。
8. **不发布的内容**：名称以点开头的、符号链接（从不跟随：链接可能发布 `dir` 之外的内容）、fib 存储会改写的名称，以及
   `per_file` 模式下的空文件。备选：与媒体库一样提供 `dir` 内的 `follow_symlinks`。
9. **上限**：每个条目 `max_files` 1000（≤100000）、`max_total_bytes` 1 TiB、`max_hashing` 1（≤16），最多 64 个条目，一次
   遍历最多 10×`max_files`+10000 个条目；超过上限时 `per_file` 按路径发布前面的文件，`whole_dir` 什么也不发布；每个问题都
   报告，最多 20 条。条目之间不共享 pool。备选：角色共用一个哈希 pool。
10. **遍历错误**：无法读取的 `dir` 让种子保持原样；未能读取 `dir` 一部分的遍历不停止任何种子（`whole_dir` 也不据此重新
    生成）。备选：停止未见到的文件的种子。
11. **失败与重复**：生成失败连同其文件的标记被记住，在标记改变前不再重试；客户端已有其 info hash 的种子（同一文件两份、
    两个条目、手动添加的种子）连同错误一起列出，只做种一次。
12. **`bt_publish`**：按路径的最后一段区分请求（`/` 或 `index.json`、`<id>.torrent`、文本形式的 `<id>.magnet`），索引中的
    URL 相对于索引，`.torrent` 文件保存在条目快照的内存中（上限为 `max_files` × fib 的 metainfo 上限），在 worker 上不取
    角色的锁（`ClientRunning` 是原子量）。备选：`strip_prefix` 参数；磁力链接用重定向；在 pool 上从磁盘读取 .torrent。
13. **种子的字段**：每个 tracker 自成一层，`created by` 为 `fit`，创建日期为生成时间；`whole_dir` 的 web seed 是 URL 的
    上一级，因此其最后一段必须是目录名（配置检查）。
14. **改名或移除的条目**删除其状态（改名的条目以新名称重新生成）；只改上限或 `check_interval` 时从其状态重启，不生成任何种子。

遗留（fib，已报告，未绕过）：

- **生成的种子第一次添加时会再校验一次**：fib 不能以"已完整"的方式添加种子，所以新生成的种子被哈希两次（Builder 一次，
  客户端的初始校验一次）；重启时借助续传数据不会。fib 请求：`AddOptions.Seed`（像 libtorrent 的 seed mode 那样信任存储
  已完整），或由 Builder 的结果得到续传数据。
- **`metainfo.Builder` 在文件结束前保留其每个 16 KiB 块的 v2 叶子哈希**，即文件大小的 1/512（1 TiB 的文件在哈希时占
  2 GiB，乘以 `max_hashing`）；fib 请求：边读边把叶子折叠成分片根。
- **`metainfo.Builder` 不能取消**：fit 经由传给它的 `fs.FS` 的读取来停止生成；`Builder.BuildContext` 会更干净。
- 未运行：Windows（名称处理）、Linux CI、libtorrent 或 anacrolix 从 fit 的静态 web seed 下载；发布的 Admin API 和页面属于
  I203/I204。

### I203 Admin API、事件、`fit bt`、指标

- **做什么**：`/bittorrent/torrents`（列表、添加、上传、暂停、继续、删除、重新校验、优先级、限制）及其文件、peer、tracker，
  `/bittorrent/tracker/stats`，事件的 SSE 流；`fit bt add|ls|info|pause|resume|rm|limit`；按分类和状态的指标；事件写入 fit 日志。
- **涉及**：`bittorrent`、`admin`、`cli`、`observe`。
- **完成** 2026-11-09：Admin API 的 `/bittorrent/...`（`admin/bittorrent.go`）：列出种子（按 `state`、`category`、`q` 过滤，
  分页），从磁力链接、URL 或 .torrent 文件添加（带 base64 的 JSON、`application/x-bittorrent`，或逐部分读取的表单，32 MiB），
  单个种子及其文件，动作（`pause`、`resume`、`recheck`），`PATCH`（暂停、顺序下载、分类、限制、队列位置、优先级），带
  `delete_data` 的 `DELETE`，`.../files`、`.../peers`、`.../trackers`，`/bittorrent/alt_speed`（GET、PUT），
  `/bittorrent/publish[/<name>]`，tracker 的 `stats`、`torrents[/<hash>]` 和 `passkey`；角色的错误映射为状态码；`/node` 中的
  能力 `bittorrent`。事件流 `GET /bittorrent/events`（`admin/btevents.go`）基于角色的事件（`bittorrent/events.go`：added、
  removed、state、completed、error、peer_banned、seed_limit、alt_speed；最多 32 个订阅者、每个 256 个事件缓冲的 hub，保留
  最近 1024 个供 `Last-Event-ID` 使用，`reset`、`lagged`、`end`、心跳），事件写入 `bittorrent` 日志。角色 API 新增 `Peers`、
  `Trackers`、`Recheck` 和 `Subscribe`（`bittorrent/inspect.go`），`Add` 会保留 `bt_stream` 即时添加的种子。指标
  （`bittorrent/metrics.go`、`bittorrent/tracker/metrics.go`，两个角色都实现 `app.Metered`）：按分类的字节数、速率、peer、
  做种 peer 和校验失败，按分类和状态的种子数，封禁，备用速率，事件订阅者，已发布种子；tracker 的种子、peer、上限、announce、
  scrape 和拒绝。`fit bt ls|add|info|pause|resume|rm|limit|alt`（`cli/bt.go`）。文档：[BitTorrent](bittorrent.zh-CN.md#admin-api)
  （Admin API、事件、`fit bt`、指标）中英文两版，参考手册的章节，fib 请求 A87（任务的 tracker 及其 announce 状态）和 A88
  （原地重新校验）。测试：未运行角色时的每个端点（404），令牌保护（401），每种添加方式及其错误（重复、错误链接、没有或两个
  来源、错误 JSON、错误 scheme、失败的 URL、未知分类、错误限制、不是种子、不支持的类型、错误表单、超限的表单字段和文件
  （有无 `Content-Length`）、过多部分、`max_torrents`），列表的过滤、分页及其错误，任意大小写 ID 的种子，文件、peer、
  tracker（来自 .torrent 文件和磁力链接），动作及其错误，`PATCH` 及其错误，带与不带数据的移除，备用速率，发布，tracker 的
  端点，错误到状态码的表，控制台（登录、添加被记录、事件流的 501）；事件流：添加的事件、心跳、按 `Last-Event-ID` 重放、
  `reset`、错误 ID、32 个订阅者与 503、关闭的流释放名额、不读取的客户端在 40000 个事件到来时以 `lagged` 被断开、角色停止时
  的 `end`；hub 的上限；一次下载、暂停、备用速率、封禁、做种限制、存储错误和移除经过的角色事件及其日志，重放和停止时的结束；
  两个角色之间的 peer，磁力链接的 tracker 和 web seed，重新校验再次读取数据；按分类的指标在移除和停止后保留，封禁和备用速率；
  tracker 的指标；`fit bt` 的命令、请求和输出，ID 前缀和名称，拒绝和用法错误。

**待审查的决定**（实现时做出，2026-11-09；在用户裁定前有效）：

1. **Admin API 不为角色 API 另设池**：每次调用运行在请求的 goroutine 上，Admin API 上是 net/http 的，控制台后面是控制台的
   有界池，与 sync 和媒体端点相同；两者都不是 fib worker，并受 `admin.max_conns` 约束。备选：角色自己的 API 池。
2. **状态码**：没有该种子 404，该角色不在此运行 404，错误输入 400，已添加或损坏 409，`max_torrents`（以及 `max_temporary`）
   **429**，URL 下载或事件流过多以及客户端未运行 **503** 并带 `Retry-After: 5`，失败的 URL 502。理由：429 表示调用者要求的
   超过了它可持有的；503 是会过去的状态。备选：`max_torrents` 用 409 或 507；角色不在此运行用 503。
3. **添加接受三种请求体**：JSON（文件为 base64）、以 `application/x-bittorrent` 发送的原始文件（选项在查询中），以及逐部分
   读取的 `multipart/form-data`；.torrent 文件上限 **32 MiB**，与按 URL 的相同（而非 fib 的 128 MiB），表单其他字段 64 KiB，
   最多 16 部分。以 `201` 和 `Location` 应答。备选：fib 的上限；`max_config_bytes`；只接受 JSON。
4. **`PATCH` 不是原子的**：按固定顺序（暂停、顺序下载、分类、限制、队列位置、优先级）逐项修改并各自保存，第一项失败时
   应答其错误，之前的修改保留；限制在任何修改之前解析。理由：角色的 API 没有事务。备选：先对照状态检查所有字段再应用。
5. **`recheck` 把种子从 fib 客户端移除（保留文件），再不带续传数据重新加入**（续传文件被删除，校验中崩溃也会重新校验），
   因为 fib 无法重新校验运行中的种子；其连接和 fib 的计数从头开始，队列位置、优先级、限制和暂停保留。已提为 fib 请求 A88。
   备选：在 fib 有 `Torrent.Recheck` 之前应答 501。
   **I210 已改**：重新校验改用 fib 的 `Torrent.Recheck`（A88）就地进行：不再移除，连接与计数保留，状态显示 `checking`；
   续传数据仍在开始时删除，校验结束后重新保存。
6. **`/trackers` 列出 .torrent 文件或磁力链接中的 tracker**（带 tier）和 web seed，不含 announce 状态：fib 不公开（fib 请求 A87）。
   **I210 已改**：`/trackers` 从 fib 的 `Torrent.Trackers`、`Sources` 与 `WebSeeds`（A87）给出各 tracker 的 announce 状态、
   peer 来源与 Web 种子状态；`web_seeds` 改为对象。
7. **慢的事件订阅者被断开，而不是被跳过事件**：每个流 256 个事件的缓冲；填满时收到 `event: lagged` 并被关闭，用
   `Last-Event-ID` 重连，从角色保留的最近 1024 个事件重放；有缺口，或 ID 超过最后一个（fit 重启过：ID 每次启动从 1 开始）时，
   流以 `event: reset` 开头。最多 32 个流（503），每 15s 一行注释，角色停止或 Admin API 关闭时发 `end`。**不可配置**，因此
   不改 schema。理由：悄悄错过事件的流会显示错误的状态；带重放的重连在常见情况下不丢任何事件。备选：丢弃最旧的事件并
   标出缺口；为上限加配置。
8. **事件流只在 Admin API 自己的地址上提供**：控制台的代理整段应答（其 recorder），因此在那里应答 501。I204 的页面需要一个
   流式的控制台路由（在 fib 上，配合 `OnWritable`），或改为轮询。备选：现在就做这个路由。
9. **状态变化由管理器每秒的查看察觉**，API 修改后立即察觉，方法是比较每个种子状态信息中的状态与上次发布的状态，因为 fib 的
   事件不说种子何时开始下载、做种或校验。事件在 API、管理器和 fib 的事件 goroutine 上发布，从不等待。
10. **添加 `bt_stream` 即时添加的种子会原样保留它**为普通种子（不应用请求的分类、限制和暂停），应答其状态而不是 409
    （I200 的遗留点）。备选：也应用请求的选项。
11. **指标**：角色每次查看（以及移除、重新校验、关闭客户端之前）把 fib 每个种子的计数累加到按分类的总计中，因此计数器比种子
    长寿，但 fit 启动时从零开始（不持久化，同 I197 的决定 9）；无分类为 `category=""`，不另设总计；抓取读快照，不取角色的锁；
    封禁是客户端范围的；tracker 的指标即其 `Stats`。备选：把总计保存在 `torrents.json` 中。
12. **日志**：添加、移除、封禁和做种限制为 info，状态变化为 debug；完成、存储错误和备用速率保持原有的日志行（info、error、info）。
13. **`fit bt`**：种子以 info hash、至少 4 位的唯一 ID 前缀或名称指定，经列表解析；速率接受 `K`、`M`、`G`（二进制）；文件以
    原始形式发送，除非 `-up`/`-down` 需要 JSON；某个种子被拒时，试完其余的再以 1 退出。没有 `peers` 或 `events` 命令。备选：
    只接受 ID；`fit bt events`。
14. **用户的 passkey 也在 Admin API 上**（`/bittorrent/tracker/passkey?user=`），受其令牌保护，与角色控制台的并存。列表默认
    每页 1000 个种子，最多 10000。

遗留：fib 请求 A87 和 A88；供页面使用的控制台流式路由（I204）；事件和指标计数不持久化。
未运行：Linux 上的 race 测试和完整测试集（留给主会话）。

### I204 工作台的 BitTorrent 页面

- **做什么**：任务列表（速率、进度、ETA、分享率），任务的文件、peer、tracker 和 piece，添加对话框，tracker 页，发布页；
  schema 描述补到 `schema-zh.js`；AI 助手函数（列出、添加、暂停），遵守其审批规则。
- **涉及**：`app/workbench`、`assistant`。
- **完成** 2026-11-09：**BitTorrent** 菜单（`app/workbench/web/bittorrent/`，位于数据同步之后）：种子（状态、保持原因、大小、
  进度条、速率、剩余时间、分享率、peer、分类；经 Admin API 按状态、分类与名称筛选；对所选种子暂停、恢复或删除（可连同数据）；
  “添加种子”可用磁力链接、原样发送的 .torrent 文件或 URL，并指定分类、暂停与顺序下载；客户端最近事件），单个种子的概览（各项
  数字、已有分片、hash、目录、磁力链接；分类、队列位置、顺序下载与自身限制就地修改，旁边显示生效的限制；暂停、恢复、重新校验、
  删除）、文件（逐个或对所选文件设置优先级）、Peer 与 Tracker；客户端页面顶栏中的备用速率（自动、开、关）；Tracker（总计与上限、
  按原因统计的拒绝、分页的白名单 swarm、私有 tracker 按用户查询 passkey）；发布（条目、问题、种子的 .torrent 下载与磁力链接）；
  BitTorrent 配置。中英文。实时更新：控制台自己的路由 `GET /api/m/admin/bittorrent/events`（`admin/btconsole.go`），在 fib 的
  连接上写出角色的事件流（worker 订阅并写出开头，该流自己的 goroutine 以 `WriteNoWait` 写出，超过水位时等待 `OnWritable`，
  最多 30s），支持 `Last-Event-ID`，受角色的 32 个订阅者限制；页面在读取事件流的同时每 2s 轮询速率，无法流式接收时只轮询。
  Admin API 新增 `GET /bittorrent/publish/<name>/torrents/<id>`，提供已发布种子的 .torrent 文件。`schema-zh.js` 已包含所有
  BitTorrent 描述（I197–I203 已补齐，测试通过）。助手（`assistant/tools_bittorrent.go`、`proposals_bt.go`）：只读工具
  `bt_torrents`、`bt_torrent`、`bt_tracker_stats`（不含磁力链接，tracker 只给协议、主机与最后一段路径，Web 种子去掉用户与
  查询，peer 不带 ID，不含 passkey），提议工具 `bt_add_propose`、`bt_pause_propose`、`bt_resume_propose`，其步骤与提案的媒体
  步骤放在一起，按当前运行状态检查，并在配置之后由确认的人执行；功能页的 `bittorrent` 分组、提案卡片的词条、系统提示、管理端
  上的 `member`。文档：[BitTorrent](bittorrent.zh-CN.md#页面)（页面、助手、控制台事件流、.torrent 端点）、
  [控制台](console.zh-CN.md#bittorrent)（页面与工具）、参考的章节，中英文。测试：`admin`（会话之后的控制台事件流、添加的事件、
  心跳、按 `Last-Event-ID` 与 `?after` 重放、reset、错误的 ID、无角色时 404、32 个上限与带 `Retry-After` 的 503、释放名额、
  不读取的客户端以 `lagged` 断开、角色停止时的 `end`；.torrent 端点及其 404），`assistant`（各只读工具及其参数与错误、角色未
  运行、结果中无机密；对话与 MCP 中的提议工具，以人的身份按顺序执行，含配置修改、种子已不存在、失败使结果为 `partial`；拒绝
  步骤的每项检查；级别与分组），`app/workbench`（每个词条都有中英文且被使用，每种状态、保持原因、事件与优先级都有词条，页面
  调用的端点，助手工具的词条）。已在浏览器面板中对本地 fit 检查：列表、添加、实时事件、恢复、连同数据删除、种子概览与限制、
  备用速率、tracker、发布及 .torrent 下载、配置，中英文。

**待审查的决定**（实现时做出，2026-11-09；在用户裁定前有效）：

1. **实时更新采用控制台流式路由加轮询**：控制台在 fib 的连接上以自己的路由提供事件流，而不是页面只轮询，因为事件能即时说明
   轮询要晚些才知道的事（完成、错误、封禁）；列表仍每 2s 读取一次，以更新事件不携带的速率与进度。备选：只轮询；控制台代理
   对所有端点提供流式模式。
2. **控制台事件流在超过水位后最多等待客户端 30s**（`btStall`），之后断开；与 Admin API 的事件流共用角色的 32 个订阅者。不可
   配置。备选：控制台自己的上限；使用控制台的 `write_stall_timeout`。
3. **经管理端的节点切换器时页面只轮询**：其转发只回答完整的应答。备选：`cluster` 中的流式转发。
4. **一个模块 `bittorrent`，服务端模块为 `admin`**，凡有 `admin.console` 处即显示（与数据同步相同），角色未运行时各页说明并
   链接到 BitTorrent 配置；角色自己的 `console` 只提供其只读路由，没有这些页面。备选：每个角色一个模块，只在其运行时显示；
   在角色自己的控制台上提供页面。
5. **备用速率位于客户端页面（种子列表与单个种子）的顶栏**，而非每个页面。备选：在每个页面的 `topbar` 中。
6. **助手的写操作是提议工具**（添加、暂停、恢复），其步骤与提案的媒体步骤放在一起（`MediaAction`，视图与应用应答中的
   `media`）：按当前运行状态检查，由级别为 apply 的人二次确认后执行，与媒体步骤相同。没有删除、重新校验或设置限制的工具：
   这些仍由人在页面上操作。备选：视图中单独的 `bittorrent` 成员；`bt_remove_propose` 等。
7. **交给模型的内容**：不含磁力链接，tracker 只给协议、主机与路径最后一段（私有 tracker 的 passkey 在路径中），Web 种子去掉
   用户与查询，peer 不带 ID，不含 passkey（由 Tracker 页面为人读取）；peer 的地址原样发送，与媒体工具发送地址相同。备选：
   像播放主体一样对 peer 地址做哈希。
8. **分享率为已上传量除以大小**，与 `seed_ratio` 的计算一致；剩余时间为下载中按当前速率计算的所需时间，两者由页面与工具计算，
   而非角色。备选：已上传除以已下载（qBittorrent 的算法）。
9. **分片显示为已有数 / 总数及进度条**，而不是分片图：角色的状态只给出计数。备选：在角色 API 中由 fib 的
   `Torrent.HavePiece` 生成分片图（遗留）。
10. **已发布种子的 .torrent 文件由新的 Admin API 端点提供**，`GET /bittorrent/publish/<name>/torrents/<id>`，从角色的快照读取，
    而不是链接到 `bt_publish` 路由（配置未必有该路由，且它在监听的源上提供）。已发布的种子在客户端列表之外做种，因此不链接到
    单个种子页。
11. **可选的分类取自运行中的配置**（`GET /config`），而非草稿：只在草稿中的分类会被角色拒绝。备选：取自草稿。
12. **页面以原始字节添加 .torrent 文件**（`application/x-bittorrent`，选项放在查询中），发送前检查 32 MiB 上限。备选：表单。
13. **文件只提供四种优先级**：不下载、低、普通、高（0、1、4、7，即 fib 命名的几种），尽管角色接受 0–7。备选：全部八种。
14. **允许对 `bt_stream` 即时添加的种子使用 `bt_add_propose`，并附警告**：它会原样转为普通种子保留（I203 的决定 10）。
15. `TestChatSummarizes` 的窗口从 5600 增至 6200 个 token，以容纳新工具的描述。

遗留：分片图（角色 API，由 fib 的 `Torrent.HavePiece` 生成）；角色自己控制台上的页面；节点切换器的流式转发。未运行：Linux 上的
race 测试与完整测试套件（留给主会话）。

### I205 互通、故障测试、基准与文档

- **做什么**：fit 的客户端和 tracker 在容器里与 libtorrent、anacrolix 互通（沿用 fib 的互通方式）；负载下的磁盘满、重启和 reload；
  吞吐与 CPU 只在基准主机上测；`docs/bittorrent*.md`、参考文档、fit-examples 中的示例。
- **涉及**：`test`、`bench`、`docs`、fit-examples。
- **完成** 2026-11-09（分支 `bt-i205`；fit-examples 分支 `bittorrent-example`）：
  - **互通**：`test/btinterop`，一个独立模块（anacrolix/torrent 1.61 和 fib 的 metainfo 生成器；决定 037 涵盖这一依赖），其测试
    从检出的代码构建 fit 并运行，经 Admin API 操控，对手是测试进程中的 anacrolix 和经 `libtorrent_peer.py` 的 libtorrent-rasterbar
    2.0.11（`FIT_LIBTORRENT=1`）；`run.sh` 在 docker 中运行全部，CI 的 `bittorrent-interop` 任务在 runner 上用 Ubuntu 的
    python3-libtorrent 和 cgo 运行。46 个用例全部通过：fit 从两者下载并向两者做种（v1、混合，以及与 libtorrent 的 v2；.torrent
    文件和磁力链接；TCP 和 uTP；MSE 关闭、优先和要求 RC4；对端的传输和加密经 Admin API 检查）；私有 tracker 经 HTTP（令牌的
    passkey）和 UDP（Admin API 给出的用户 passkey，BEP 41），用于 anacrolix 和 libtorrent 的 swarm，fit 自己的客户端也在其中，
    未知 passkey 被拒绝；`bt_stream` 在从 anacrolix 下载 24 MiB 文件的同时播放它（中间的 Range 在种子下载到 8% 时得到应答），
    以及按磁力链接即时添加；只用发布条目的 web seed（fit 的 `static` 路由），`per_file` 和 `whole_dir` 由 libtorrent 下载，
    `whole_dir` 由 anacrolix 下载。
  - **故障测试**（`bittorrent/failure_test.go`），与 I197–I203 的并列：下载中磁盘写满（写入返回 ENOSPC 而空闲空间检查仍认为
    有空间：错误出现在状态中，块被重新请求；随后检查使其暂停；然后完成，没有哈希失败）；三个下载进行到一半时停止并重启
    （每个分片不读取即再次拥有，然后完成）；下载中改变 `listen`（分片保留，不读取，完成）；唯一的做种方在一个分片的块中途被
    切断，然后恢复；两个做种方之一在分片中途停滞（约 2s 内从另一个完成下载，远早于 fib 60s 的 snub）。
  - **基准**在基准主机上：`bench/bt.sh`、`bench/bt/host.sh`、`bench/bt/summary.py` 和 `test/btinterop/btbench`；一个 1 GiB
    的种子放在 `/dev/shm`，被测客户端用 4 个 CPU，四个 fib 对端用另外 8 个，fit 与 anacrolix 和单独的 fib 客户端对比，明文和
    RC4，3 轮交替运行；结果在 `bench/results/bt-2026-11-09`（中位数）。向 4 个下载方做种时 fit 明文 1688 MiB/s（anacrolix 1110），
    每 GiB 0.60 CPU 秒（1.23），RC4 933（744），峰值 RSS 46–56 MiB（anacrolix 约 1050）。从 4 个做种方下载时 fit 明文 705 MiB/s
    （anacrolix 1039，单独的 fib 478），RC4 553（759、363），每字节 CPU 与 anacrolix 相同，4 个 CPU 用了不到 2 个：受 fib 的
    种子锁限制（见下）。
  - **文档**：中英文两版的 [BitTorrent](bittorrent.zh-CN.md)：四部分的概览，包含所有部分的配置（经 `fit validate` 验证），
    合并重复的状态行，新增互通、性能、限制与安全三节；配置概览的行和 README 的文档列表，中英文两版。**示例**：fit-examples
    的 `bittorrent/`（带分类、`alt_speed`、`watch_dir` 和发布条目的客户端，私有 tracker，`bt_tracker`、`bt_publish`、`static`、
    带 `magnet` 的 `bt_stream`，Admin API），列入其 README；fit-examples 固定的版本已经包含这些角色。

**待审查的决定**（实现时做出，2026-11-09；在用户裁定前有效）：

1. **互通测试经 Admin API 驱动 fit 二进制**，与 test/mcpsdk 相同，而不是在进程内运行角色：它们检查的是用户运行的东西，
   模块不需要 `replace` fit，同一套工具也用于基准。模块只为测试种子按 fit 的版本依赖 fib。备选：进程内运行角色，加
   `replace github.com/lesismal/fit => ../..`。
2. **anacrolix 用例在该模块的每次 `go test` 中运行；libtorrent 用例需要 `FIT_LIBTORRENT=1`**，与 fib 相同；CI 两者都运行，
   带 `-race`，在 25 分钟内。该模块不在 fit 主模块的 `go test ./...` 中（独立模块）。
3. **互通传输以 2 MiB/s 运行**（fit 自己的 `limits`），使一个用例持续的秒数足以经 Admin API 看到其对端，而 Admin API 只列出
   已连接的对端。备选：每个对端连接时发一个事件。
4. **其他客户端的缺陷在测试中绕开并记录，暂未向上游修复**：anacrolix 把 v2/混合的单文件种子当作目录（其 web seed 拒绝不以
   `/` 结尾的 URL，因此 anacrolix 不获取 `per_file` 的 web seed，并在文件名下面寻找文件），其 web seed 跳过较短的最后一个
   分片，1.61 在 announce 它所校验的混合种子时 panic；与它一起的用例使用 v1 或 `whole_dir`。备选：报告给 anacrolix，在修复前
   跳过这些用例。
5. **基准的对照是 anacrolix，另加单独的 fib 客户端**；基准主机上没有安装 libtorrent，经其 Python 绑定测量会把 Python 也测进去。
   另一端是 fib 的客户端（其做种方取用第一个的续传数据，不做校验）；数据放在 `/dev/shm`，不测磁盘；1 GiB，v1，1 MiB 分片，
   TCP；CPU 和峰值 RSS 取自 `/proc`。不是计划报告：没有分数和排名（规则 24 针对完整计划）。主机的 LLM 服务保持运行
   （空闲；13 GiB 空闲内存），这是用户的偏好。
6. **btbench 放在 test/btinterop 中**（`btbench/`），即已有 anacrolix 的模块，而不在 `bench/`（其 Go 代码属于 fit 的模块）。
   它等待 fit 的 `pieces_have` 而不是 `complete`，后者由角色在管理器每秒的查看中设置（见遗留）。
7. **故障测试用存储包装和空闲空间函数模拟磁盘写满**，而不是 tmpfs 或 loop 设备（需要 root，仅限 Linux）；被切断和停滞的
   对端经测试自己的 TCP 中继模拟。
8. **文档的配置示例是带占位符的 .conf**（不用 `${...}`），其中的 tracker 为公开，使发布的种子不携带 passkey；安全说明解释原因。

遗留：

- **fib：种子的下载受其锁限制**（待提的 fib 请求）：每收到一个块都要取 `Torrent.mu` 并补充该对端的请求，`nextRequest`/`pickPiece`
  用 `math/rand` 扫描分片（在单独 fib 从 4 个做种方下载 1 GiB 的 profile 中占 CPU 的 24%，`pickPiece` 21%），且每个 16 KiB
  块各是一次 `pwrite`（30%）。建议：对端预留一个分片并请求其各块而不再重新挑选，在其流水线低于 reqq 的一半时补充；
  最稀有优先改用在 have/bitfield 时更新的桶，而不是扫描；在磁盘池上按分片或按连续段写块（`pwritev`）。fit 无需改 API 即可
  获得下载速率的提升。
- **单独的 fib 在其自己的引擎上下载速率只有 fit 的 0.68**：值得在 fib 中看看不带 `SharedEngine` 创建的客户端的引擎默认值。
- **fit：`Status.Complete` 最多滞后一秒**（管理器的查看，I198 的决定 10）：轮询完成的 API 客户端要等它；在查询时由 fib 的
  种子计算，只要计数仍由管理器负责，就不会把一次完成计两次。
- 向上游报告上述 anacrolix 缺陷。fit-examples 的 `bittorrent/` 用静态令牌把发布的种子 announce 到其私有 tracker，每个下载者
  因此共用该令牌（其注释称该令牌为占位符；文档的安全说明建议发布到公开的 tracker）。
- 未运行：完整的 `go test -race ./...` 和 Linux 上的 race 测试（留给主会话）；互通测试在 docker（Linux）中运行，anacrolix 的
  用例也在 macOS 上运行过。

### I206 （第二阶段）集群与 BitTorrent 分发

- **做什么**：集群汇总各成员的任务和速率；经私有种子加直连 peer，把大文件（llm 节点的模型、发布包）分发到一组成员，与 rollout 配合。
- **涉及**：`cluster`、`bittorrent`、`app/workbench`。
- **完成** 2026-11-09（分支 `bt-i206`）：**视图**（`cluster/btview.go`）：成员的 Admin API 新增 `GET /bittorrent/summary`
  （`admin/btsummary.go`，能力 `bittorrent.summary`）：两个角色的简短汇总，无论是否运行都返回 200（客户端是否运行、接受对端的地址、
  按状态统计的种子、速率、字节数、对端、错误、发布条目；tracker 的统计）。管理端按需读取各成员（`Manager.BitTorrent`），经
  `Manager.Call` 同时最多 `max_concurrency` 个，一次读取 2 秒内共用、进行中的读取会被加入；离线及没有该能力的成员不请求。
  `GET /api/m/cluster/bittorrent`（任何角色）与管理端 Admin API 的 `GET /cluster/bittorrent`。**分发**（`cluster/distribute.go`）：
  从源成员的私有发布条目（I202）到分组的成员或所选成员；管理端读取源的摘要、条目与 `.torrent` 文件（私有，≤1 TiB）、各目标的摘要，
  让每个目标以源和其他目标为 `peers` 把种子添加到某分类（地址为客户端监听的地址，否则为成员 URL 的主机，否则为 `peer_hosts`），
  每 2 秒跟进进度（完成、错误、被删除、停滞），结束时从它添加过种子的目标上删除该种子（完整的保留文件，否则删除；`keep_seeding`
  让完整的继续做种）；取消；审计条目 `bt.distribute`；`POST`/`GET /api/m/cluster/distributions`、`GET .../<id>`、
  `POST .../<id>/cancel`，Admin API 的 `/cluster/` 下同样提供。**页面**：集群 → BitTorrent（`app/workbench/web/cluster/bittorrent.js`）：
  统计块、成员表（“打开”经节点切换器进入成员自己的 BitTorrent 页面）、分发列表、新建分发、分发页及取消；概览卡片
  （`FitCluster.overviewCard`，`shared.js` 的新扩展点）。**助手**：`cluster_bittorrent`；读取工具的 `member` 参数现在可以读到成员的
  BitTorrent 读取接口（`memberPaths` 原先缺少它们，带 `member` 的 `bt_*` 会被拒绝），但从不触及 passkey、事件或写操作。文档：
  [集群](cluster.zh-CN.md#bittorrent成员的种子与分发)与 [BitTorrent](bittorrent.zh-CN.md#集群中)，中英文。测试：基于假成员的视图
  （合计、没有角色的成员、按能力与按 404 判断的旧成员、离线、TTL 与合并的读取、截断）、助手的读取与成员路径、Admin API、角色
  （查看者被拒、分组的操作员只能对其成员）；本机回环上进程内 fit 之间的分发（3 MiB 文件逐字节传到两个目标，第三个没有客户端的目标
  单独失败；再经模块以 `keep_seeding` 分发；再次时被采用）、取消（种子与数据被删除）、停滞、`peer_hosts` 只给主机、源非私有、
  没有条目或种子、没有客户端、`StartDist` 的各种拒绝（字段、64 个目标、同时 2 个、忙碌的目标、准备中取消）、保留列表、对端地址；
  有无角色时的 `GET /bittorrent/summary`。

**待审阅的决定**（实现时做出，2026-11-09；在用户裁定前有效）：

1. **分发是独立的资源，而非发布（rollout）的一种步骤**：发布应用配置，带演练、闸门与回滚，下载都不需要；按操作的方式建模（内存中
   保留最近 16 个，结束时记入审计日志），不修改配置因此不占用成员阻挡发布或操作；一个成员同时只是一个分发的目标。备选：发布步骤；
   保存在 store 中跨重启。
2. **源是私有的发布条目（I202），而非一次性构建**：发布条目已用种子模式构建、可跨重启并跟随磁盘，因此管理端不必给角色增加构建路径
   （不改 `bittorrent/`）；非私有的条目被拒绝（`not_private`）。条目列出的 tracker 允许（运维自己的私有 tracker）。备选：
   `Role.Share(path)`，在线程池上一次性构建，成员需要异步任务 API。
3. **“配置的目标目录”即目标上的分类**（或其 `download_dir`）：角色的添加 API 没有自己的目录参数，分类就是配置中的目录。备选：
   在配置允许的根目录内的 `AddRequest.Dir`。
4. **对端地址**：目标客户端监听的地址（若为具体地址），否则为其成员 URL 的主机加客户端端口，否则为请求中的 `peer_hosts`（连出成员
   需要）。源与其他每个目标都是每个目标的对端（最多 65 个）。备选：用成员标签给出其 BitTorrent 主机。
5. **结束时**：从分发添加过种子的目标上删除该种子，完整的保留文件、不完整的（失败、取消、停滞）删除；`keep_seeding` 让完整的继续
   做种；已有该种子的目标被采用、跟进、从不删除；从不触碰源的发布条目。备选：所有种子都保留做种。
6. **上限**：64 个目标、同时 2 个、保留 16 个、1 TiB、停滞超时 10 分钟（1 秒–24 小时）、每 2 秒读取进度；均为常量，不加配置。
   备选：`admin.cluster.distributions`。
7. **视图按需读取，而非由轮询器读取**，缓存 2 秒并合并并发读取，任何角色可读；不占用读取名额（`MaxReads`），因为同时只有一次扇出。
   备选：随每次轮询读取摘要并保存在成员状态中。
8. **角色**：开始与取消都需要在源和每个目标上是操作员（与操作相同）。
9. **管理端重启会忘记进行中的分发**；目标保留已下载的内容与种子（角色不会跨重启保留其对端）。

遗留：成员的 `AddRequest.Peers` 不跨重启保存，分发中途重启的目标没有对端而停滞（角色可将其存入 `torrents.json`）；`fit cluster`
尚无 `bt`/`distribute` 命令（Admin API 已提供）；`.torrent` 文件受 `max_response_bytes` 限制（默认 4 MiB；小分片的 1 TiB 混合种子
会超出）。未发现 fib 的缺口。未运行：Linux 上的 race、完整测试、浏览器中的页面。

**I206 之后修复（主会话，2026-11-09）：** 角色把种子添加时给的 peer（`AddRequest.Peers`）保存在 `torrents.json` 中，最多 128 个、
每个最长 256 字节，并在每次启动时再交给它，这样分发途中重启的目标能重新找到它的 peer（`TestPeersKeptAcrossRestart`）。

### I207 （第二阶段）集群共享的 tracker Store

- **做什么**：基于 fit 共享存储（redis、etcd）的 `tracker.Store`，让多个成员上的 tracker 如同一个。
- **涉及**：`bittorrent`、fit 的共享存储。
- **已完成** 2026-11-09（分支 `bt-i207`）：包 `bittorrent/tracker/sharedstore`（`store.go`、`redis.go`），一个 fib
  `tracker.Store`，并有 `MemoryStore` 的 `Range`、`SetConfig`、`Config`、`Torrents` 和 `Peers`。每个实例立即从自己的 fib
  `MemoryStore` 应答；存储自己的一个 goroutine 完成每次 Redis 往返（`request_timeout`）：announce 进入有界队列（按 peer
  与种子合并，完成标记在后来的 announce 中保留），以 Lua 脚本流水线写入，以 Redis 的时钟为准（做种者和下载者存于按过期时间
  排序的有序集合，地址存于哈希，另有完成次数、按最晚过期时间计分的种子索引和 peer 计数，每次改动都由脚本完成）；每个被服务
  种子的共享 swarm 随机样本（`ZRANDMEMBER`）及计数每 `refresh` 读取一次；announce 先以样本中的 peer、再以本实例的 peer
  应答（做种者只得到下载者，不含自己，跳过过期的），计数各取较大者；上限在 Redis 中对所有实例合计执行；每分钟清扫 peer 已
  全部过期的种子；`Range` 经第二个连接用 `ZSCAN`（Redis 无法读取时为本实例的种子）。配置 `bittorrent.tracker.store`
  （`redis` 与 acme 的 `RedisStorage` 同形，`prefix`、`request_timeout`、`refresh`、`queue`、`cache_torrents`、
  `cache_peers`）及其检查（Redis 的检查与 `acme.shared_storage` 共用为 `checkRedisStorage`）；角色经接口持有存储，块变化
  时新建存储（在其他改动之前，坏的 TLS 文件使角色保持原样），变化和停止时关闭；`Stats.Store` 与
  `fit_bittorrent_tracker_store_*` 指标。schema、参考、模板、`schema-zh.js`；两种语言的 [BitTorrent](bittorrent.zh-CN.md)
  tracker 一节、指标和重载。测试：存储的各情形（两个实例互见 peer、做种者得到下载者、跨实例的 scrape 与完成次数、停止；共享
  上限与 SetConfig 调高；过期与清扫；跨实例 Range 与提前停止；地址族与 numwant；Redis 不可达时从内存应答、排队、恢复后写入）
  分别对 Docker 中的真实 Redis（`redis:8.2.2`，无 Docker 时跳过，同 `tlsmgr/sharedstore`）和假实现运行；队列、重新入队、
  缓存上限与淘汰、合并、过时样本、失败的写入、超过一轮的写入量；TLS 文件；地址编码与样本解析；两个引擎上的两个 tracker 角色
  共享一个 Redis，经 fib 的客户端走 HTTP 和 UDP（peer、scrape、统计、列表、指标、重载到新前缀和内存）；不可达的存储从内存
  应答，坏的 TLS 文件使重载失败；配置检查。运行：`bittorrent/tracker/...`、`config` 的 `go test -race`；
  `go test ./schema/ ./app/workbench/ ./docs/ ./test/workers/`；vet、gofmt。

**待评审的决定**（构建时作出，2026-11-09；在用户裁定前有效）：

1. **从内存应答，在后台共享**，而不是每个请求询问 Redis：fib 的 `Server` 在 worker 上调用 `Store.Announce` 并须在返回前
   得到应答，在那里往返 Redis 会阻塞 worker（规则 1）。代价：实例在 `refresh` 内得知其他实例的 peer，尚未服务过的种子第一次
   announce 只得到本实例的 peer。备选：fib 提供异步 `Store`（见下），再每个 announce 询问 Redis。
2. **只支持 Redis**：etcd 和 Consul 是共识存储，每次 announce 都是一次 Raft 写入，不适合每个客户端每个间隔都写的 peer 表；
   存储的 `backend` 接口为其他种类留有余地。`store` 采用与 `acme.shared_storage.redis` 同形的 `redis { ... }`（同一个
   `RedisStorage` 类型），而不是引用 acme 的块。备选：带租约的 etcd。
3. **Redis 不可达：退回内存，从不拒绝**：以本实例的 peer 和不超过三个 refresh 的样本应答，有界排队，Redis 恢复后写入；
   退避 100ms 起加倍到 `refresh`，最多 10s。备选：拒绝 announce，那样 Redis 一停整个 tracker 集群都停。
4. **上限沿用 tracker 自己的，作用两次**：作用于每个实例的内存，也作用于 Redis 中所有实例的合计（`max_torrents` 对索引、
   `max_peers_per_torrent` 对 swarm、`max_peers` 对计数）；超出的 announce 应答但不共享（计为被拒）。共享存储没有单独的上限。
   备选：`store` 中设单独字段。
5. **Lua 脚本以 Redis 的时钟（`TIME`）为准**，每次写入带上它在 fit 队列中的时长，各实例时钟不必一致；键从不自行过期（使
   peer 计数保持准确），由 announce 和每个实例每分钟一次的清扫（至多 4096 个种子）移除，因此没有 tracker 运行时键会滞留；
   Redis 的 `maxmemory-policy` 须为 `noeviction`。需要 Redis 6.2（`ZRANDMEMBER`）；不支持 Redis Cluster（一个种子的键没有
   hash tag）。备选：`PEXPIRE` 的键加近似计数。
6. **只为在本实例 announce 或 scrape 过的种子取样本**（`cache_torrents` 4096；仅凭 scrape 最多占四分之三；`peer_ttl` 内未用
   即移出），每个样本 `cache_peers`（100）个做种者和同样多的下载者。备选：集群所有种子的样本，内存和 Redis 开销随集群规模增长。
7. **先给样本中的 peer，再给本实例的**，使应答是整个集群的样本；计数按字段取本实例与样本中较大者。备选：按本实例在 swarm
   中的占比混合。
8. **`store` 块变化时新建存储**，本实例的 swarm 在内存中重新开始（客户端在其间隔内再次 announce），排队的写入放弃；更短的
   `peer_ttl` 在 Redis 中只作用于新写入（已共享的 peer 保留原过期时间），不同于 fib 在内存中的 `SetConfig`。备选：沿用内存存储。
9. **有存储时 `Stats` 的种子和 peer 是共享存储的**（本实例的在 `store.local_*`），`ListTorrents` 从 Redis 列出所有实例的种子；
   工作台的 Tracker 页面和助手的 `bt_tracker_stats` 未改动（照旧显示总数；JSON 中有 `store`）。
10. **复用 sync 的 RESP 客户端（`sync/redis/wire`）**，而非 tlsmgr/sharedstore 私有的那个，因其有流水线和应答上限；用 RESP2。

由此留下：要提交给 fib 的请求——**异步 `Store` API**（如 `AnnounceAsync(a StoreAnnounce, done func(StoreResult, error))` 与
`ScrapeAsync`，Server 保留 HTTP context，或稍后应答 UDP 数据报，在 `done` 中回复），使共享存储能每个 announce 询问 Redis，
消除首次 announce 的缺口；退一步，**`StoreResult.Interval`**（存储在应答不完整时请客户端早些再来）。工作台页面可以显示
`store`（可达、排队、丢弃）。无基准测试：tracker 不在网关热路径上（规则 21）。

### I208 （第二阶段）RSS 自动下载

- **做什么**：按计划轮询订阅源，用规则（模式、分类、大小上限、剧集去重）添加任务；订阅源、条目和历史都有上限。
- **涉及**：`bittorrent`。
- **完成** 2026-11-09（分支 `bt-i208`）：配置 `bittorrent.client.rss`（`config/bittorrent_rss.go`）：`feeds`（name、url、
  interval、enabled、max_items、headers、headers_env）、`rules`（name、feeds、patterns、exclude、category、paused、min_size、
  max_size、episodes、max_per_poll、enabled）、`history_size`、`max_feed_bytes`、`timeout`，及其检查。角色（`bittorrent/rss.go`、
  `rss_parse.go`、`rss_rules.go`、`rss_state.go`）：RSS 自己的 goroutine 每个 tick 启动到期的轮询，最多同时 4 个，每次轮询在
  自己的 goroutine 上；订阅源用 fib 的异步 HTTP 客户端下载（独立的客户端，受 `max_feed_bytes` 约束），带 `ETag` 和
  `Last-Modified` 条件请求，`timeout` 内完成，最多 5 次重定向到 http(s)；用 `encoding/xml` 读取（RSS 2.0、1.0、Atom；torrent
  命名空间、附件、info hash；UTF-8 和 ISO-8859-1），各项有界（条目、标题、ID、URL、磁力链接）；Apply 时编译的规则挑选条目，
  经 `Add` 或 `addURL` 添加（`AddURL` 内部现在带 context，停止时结束轮询中的下载）；每个条目连同结果记在 `data_dir/rss.json`
  中（每个订阅源、每条规则的集数都有上限，每次轮询后和停止时原子写入），开关也在其中。API：`RSS`、`RSSItems`、`PollFeed`、
  `SetFeedEnabled`、`SetRuleEnabled`。事件 `rss_added`；指标 `fit_bittorrent_rss_polls_total{feed,result}`、
  `fit_bittorrent_rss_items_added_total`、`fit_bittorrent_rss_items_failed_total`。Admin API（`admin/btrss.go`）：
  `GET /bittorrent/rss`、`GET /bittorrent/rss/items`、`POST /bittorrent/rss/feeds/{name}/poll`、`PUT /bittorrent/rss/feeds/{name}`
  与 `.../rules/{name}`。`fit bt rss [ls] | items | poll | enable | disable | reset [-rule]`（`cli/bt_rss.go`）。工作台的
  BitTorrent 菜单新增 **RSS**（订阅源带开关和“立即轮询”，规则带开关，最近条目可按订阅源与结果筛选并链接到添加的种子；事件中
  的 `rss_added`），中英文。助手的读工具 `bt_rss`（订阅源 URL 只给出 scheme、主机和最后一段，略去条目 ID），管理端的成员也可用。
  schema、模板、参考（及其中文章节行）、`schema-zh.js`、两种语言的 [BitTorrent](bittorrent.zh-CN.md#rss-自动下载) 和
  [控制台](console.zh-CN.md)。测试：解析（各种来源的 RSS、Atom、RDF、ISO-8859-1、上限、格式错误、不是订阅源、未知字符集）、
  集数键、规则（通配符、`re:`、排除、大小、订阅源）、对本地服务器的轮询（磁力与 .torrent 附件条目按规则的分类与暂停添加、一集
  的第二种画质被略去、未匹配和超大条目、发送 headers 与 headers_env、事件、指标、`If-None-Match` 与 304、变化的订阅源只添加新
  条目）、`max_per_poll` 留下其余且不发条件请求、`max_torrents` 把条目留到以后、历史、集数与开关跨重启保留、超过 `history_size`
  遗忘最旧条目、坏的 `rss.json` 被移到一旁、坏订阅源（超过 `max_feed_bytes`、格式错误、HTML、500、重定向循环、重定向到 ftp、
  从不应答的服务器、截断正文的服务器；跟随正常重定向）、.torrent URL 在 3 次轮询后放弃、坏种子立即放弃、假时钟上的计划（第一次
  轮询、间隔前不轮询、关闭时不轮询、重新打开后再轮询）、重载删除订阅源与规则及其历史、轮询进行中拒绝另一次、停止结束等待服务器
  的轮询；配置检查；Admin API（每个接口及其错误，无角色时 404）；`fit bt rss`（请求、输出、拒绝、用法错误）；助手工具（没有
  机密到达模型）；页面的词条与接口。

**待评审的决定**（实现时做出，2026-11-09；在用户裁定前有效）：

1. **模式是对整个标题的不区分大小写的通配符**（`*` 任意一串、`?` 一个字符，其余按字面），**或 `re:` 加 Go 正则表达式，在标题
   任意位置找到即可**。字段名为 `patterns` 而非 `include`：`include` 是 `.conf` 格式的指令，无法在其中写出。备选：只用正则；
   子串匹配（qBittorrent 的“必须包含”）。
2. **剧集**：`S01E02`（以及 `S1E2`、`S01.E02`，范围 `S01E02E03`、`S01E02-03`、`S01E02-E03`，范围终点至多比起点大 50，使
   `S01E02-1080p` 不算范围）、`1x02`，以及日期 `YYYY-MM-DD`/`YYYY.MM.DD`/`YYYY MM DD`；键为节目名（集数之前的标题，小写，标点
   变空格，去掉开头的 `[组]`/`(组)`/`【组】` 标签）加集数，**按规则区分**；一集的第一个条目胜出，因此 repack 和 proper 不再
   下载；没有集数的标题（整季包、电影）只按 ID 区分。已持有的种子（`exists`）也记下其集数。每条规则记住 10000 集。备选：按
   客户端而非按规则；接受之后的 PROPER/REPACK；qBittorrent 智能过滤的集数列表语法。
3. **按顺序第一条订阅源、模式和大小界限都满足的规则挑中条目**；大小界限是匹配的一部分，因此对前一条规则过大的条目可被后一条
   规则挑中。条目大小取 torrent 命名空间的 `contentLength`，否则网站的 `size`（`1.5 GiB`），否则附件的 `length`；**大小未知的
   通过**。备选：规则有界限时拒绝未知大小；所有匹配的规则。
4. **读到的每个条目都记为已见**，包括未匹配的，因此之后添加的规则不会回看已见条目；**新订阅源的第一次轮询把整个文档当作新的**
   （受 `max_per_poll` 约束），与 qBittorrent 相同。备选：只记被挑中的条目；订阅源选项在第一次轮询时跳过存量；遗忘订阅源历史的
   API。
5. **留到以后的条目不记住，也不保留订阅源的校验器**：超过 `max_per_poll`，或添加可能稍后成功（`max_torrents`、`ErrBusy`、客户端
   未运行、停止），使下次轮询读取整个订阅源而不是 304。下载失败的 .torrent URL 在 3 次轮询中重试（`attempts`），坏种子或坏链接
   立即放弃。
6. **来源顺序**：磁力链接（torrent 命名空间、附件或链接），然后 .torrent 附件的 URL，然后单独的 info hash（加标题做成磁力
   链接），然后条目的链接（若是网页，会因不是种子而失败）。URL 按 I198 的 `AddURL` 添加，共用其同时 4 个下载。
7. **默认值与上限**：interval 15m（至少 1m）、max_items 200（最多 1000）、history_size 1000（最多 10000，至少为每个订阅源的
   max_items）、max_feed_bytes 4 MiB（最多 32 MiB）、timeout 30s、max_per_poll 5（最多 100）、100 个订阅源、200 条规则、32 个
   512 字节的模式、16 个请求头、同时 4 次轮询（不可配置）、标题 1 KiB、ID 1 KiB（超过则取哈希）、URL 8 KiB。
8. **机密**：`headers`（字面值）和 `headers_env`（每次轮询读取的环境变量）；`GET /config` 不做遮盖，按配置显示订阅源 URL 和
   字面请求头。角色的状态给出去掉用户信息和查询串的订阅源 URL；助手得到 scheme、主机和最后一段路径，不给条目 ID。**不做内部地址
   防护**，与 I198 的决定 3 一致：URL 是运维者的配置。
9. **字符集**：UTF-8、ASCII 和 ISO-8859-1，不引入依赖（fit 没有 `golang.org/x/text`）；其他字符集使轮询失败。XML 宽松读取
   （HTML 实体，非严格）。
10. **`rss.json` 与 `torrents.json` 分开**，有自己的锁（在角色的锁之后获取）和 goroutine，不用管理器的（轮询要等 HTTP）。无法
    读取的 **移到一旁成为 `rss.json.bad`，角色从一无所见开始**，而不是像 `torrents.json` 那样拒绝启动：受影响的只是重复添加，
    `exists` 会拦下。配置中不再有的订阅源和规则在下次保存时遗忘。计数（轮询、错误、添加）不持久化；规则的 `added` 持久化。
11. **开关**：Admin API 对订阅源或规则设置的 `enabled` 一直有效，保存在 `rss.json` 中，直到用 `null` 改回；强制轮询对已停用的
    订阅源也有效；正在轮询的订阅源再次轮询为 **409**，带 `Retry-After`；没有该订阅源或规则为 404。
12. **页面**：BitTorrent 菜单中的 RSS 页，每 10s 读取一次；“立即轮询”等待轮询完成。`fit bt rss poll` 在轮询结果为错误时以 1
    退出。
13. `TestChatSummarizes` 的窗口因 `bt_rss` 的描述从 6200 增至 6500 token。

遗留：RSS 页未在浏览器中检查（其词条与接口有测试）；附件的 `length` 有时是 .torrent 文件的大小而非内容大小，此时大小界限会
误读（有 `contentLength` 或 `size` 的订阅源无此问题）；没有智能过滤的集数列表或“N 天内忽略”；不支持 gzip（fib 的客户端不解压，
因此 fit 不发送 `Accept-Encoding`）。未运行：Linux 上的 race 测试和全量测试（留给主会话）。

### I209 各角色用上 fib A80–A86 新增的能力

- **为什么**：I197、I200、I201 和 I202 留下了 fib 需求（A80–A86，已在 fib `ecdcec0` 完成，fit 在 `e633fc2` 上）：发布的文件被
  哈希两次，生成只能靠让读失败来停止，公开 tracker 无法列出其种子，重载 tracker 的上限会让 swarm 从空开始，客户端的事件队列
  无界，`Reader.Close` 要等待进行中的读。
- **做什么**：发布以 `AddOptions.Seed` 添加新生成的种子（之后用续传数据，两者从不同时：`ErrSeedResume`），并以 `BuildContext`
  生成；tracker 经 `MemoryStore.Range` 列出公开 tracker 的种子（Admin API、控制台、页面；分页、有界），并以 `SetConfig` 应用新的
  上限和 `peer_ttl`；客户端设置 `Config.EventQueue` 并呈现 `EventDropped`（事件流、`fit_bittorrent_events_dropped_total`）；
  `bt_stream` 在安全时去掉 A83 使之多余的部分。
- **完成标准**：新发布加入时不读取数据；停止时生成及时结束；公开 tracker 的种子可列出并分页；重载上限保留 swarm；被丢弃的事件
  被计数并发布；两种语言的文档。
- **涉及**：`bittorrent`、`bittorrent/tracker`、`handler/btstream`、`admin`、`assistant`、`config`、`app/workbench`、`docs`。
- **完成** 2026-11-09（分支 `bt-i209`）：**发布**（`bittorrent/publish.go`、`publish_walk.go`）：本进程生成且尚无续传数据的条目
  以 `AddOptions.Seed` 加入，之后只用其续传数据（拥有全部分片后保存，seed 模式下立即如此，未校验的分片仍标为未校验）；每个条目有
  一个 context，停止时取消，结束其遍历和生成（`Builder.BuildContext`），取代了原来的原子标志和让读失败的 `fs.File` 包装（保留了
  一个测试用的读钩子）。**Tracker**（`bittorrent/tracker`）：`ListTorrents(offset, limit, after)`，带 `Source`（`allow_list` 或
  `store`）、`Total` 和 `Next`；公开 tracker 的列表经 `Range` 按 info hash 排序，用堆保留 `after` 之后最小的 `offset`+`limit` 个；
  `Torrents` 签名不变；控制台的 `/api/torrents` 和 Admin API 的 `/bittorrent/tracker/torrents` 接受 `after`（错误的 `after` 以及有
  允许列表时为 400），并应答 `source` 和 `next`；重载上限或 `peer_ttl` 时对现有存储调用 `SetConfig`，interval 也变时在其上新建
  `Server`；四个字段的说明（schema、参考、模板、`schema-zh.js`）随之更新；Tracker 页面按来源说明列表；助手 `bt_tracker_stats` 的
  说明。**客户端**（`bittorrent/role.go`、`events.go`、`metrics.go`）：`Config.EventQueue` 设为 `EventQueue`（fib 的默认值
  4096）；`EventDropped` 被计数（`fit_bittorrent_events_dropped_total`），由管理器下一次查看发布为一个带 `dropped` 的
  `events_dropped` 事件；页面给出其文字。**`bt_stream`**：未删除任何东西（见下）；注释说明了原因。文档：
  [BitTorrent](bittorrent.zh-CN.md)（发布只哈希一次、生成可停止、新事件类型、客户端的队列、指标、tracker 的重载与列表、Admin API
  一行、限制）两种语言；[fib-api-requests](fib-api-requests.zh-CN.md#a80事件队列有界) 中 A80–A86 标为已使用。测试：新发布加入时
  不读存储且全部分片为信任状态，随后被提供（有读取，信任计数降到 0）；以每兆字节读取 100ms 生成 24 MiB 文件，停止后 3 次读取内
  中断；公开 tracker 的 25 个种子按 offset 和按 `after` 列出（每个恰好一次、有序）、控制台路由、有允许列表时拒绝 `after`；重载上限
  和 `peer_ttl` 保留存储及其 swarm，调低的上限拒绝新种子和新 peer，上限与 interval 同时改变（同一存储、新 server），改短的
  `peer_ttl` 使 peer 过期；Admin API 的 `source`、`after` 及错误的 `after`；两次丢弃被计数并只发布一次；页面文字。已运行：
  `bittorrent/...`、`handler/btstream`、`admin`、`assistant`、`config` 的 `go test -race`；`go test ./schema/ ./app/workbench/
  ./docs/ ./test/workers/`；vet、gofmt。

**待评审的决定**（实现时做出，2026-11-09；在用户裁定前有效）：

1. **不加 `event_queue` 配置项**：角色把 fib 的默认值（4096）设为常量。原因：角色的处理器只取一个短锁就返回，而 fib 丢弃的事件
   （分片校验通过、哈希失败、peer 连接与断开）角色从不发布，调整它对运维没有好处。备选：`bittorrent.client` 中的 `event_queue`
   （重载时新建客户端，因为 fib 在 `NewClient` 时读取）。
2. **被丢弃的事件每秒至多一个 `events_dropped` 事件**，由管理器的查看带上自上次以来的计数发布，而不是每个 fib `EventDropped` 一个，
   以免一阵突发把事件流的真实事件挤出保留供重放的 1024 个，或使订阅者落后；日志为 debug。被丢弃的分片事件可能让存储错误多显示
   一会儿，直到下一个分片通过或状态变化（与以前一样，它们也会清除）。备选：逐个转发 `EventDropped`；只要指标。
3. **公开 tracker 的列表按 info hash 排序，按 `offset` 或 `after` 分页**：`Range` 不给顺序，所以每页用堆保留 `after` 之后最小的
   `offset`+`limit` 个哈希，耗时与跟踪的种子数（至多 `max_torrents`）成正比，内存与页大小成正比；`total` 是有 peer 的种子数（给了
   `after` 时为其后的）。允许列表保持自己的顺序并拒绝 `after`（400）。不在 worker 上：控制台路由在其池中运行，Admin API 在请求的
   goroutine 上。备选：角色定期刷新的存储快照；以分片和位置为游标、按 `Range` 的顺序（种子增减时不稳定）；按 swarm 大小排序。
4. **只有本进程生成且尚无续传数据的种子用 seed 模式**：从 `index.json` 恢复但没有续传数据的种子（保存前崩溃）仍完整校验，因为其
   文件不是刚哈希过的。备选：它们也用 seed 模式，因为遍历会比较其标记（但只在加入之后）。
5. **生成只由条目的 context 取消**：条目停止后让读失败的 `fs.File` 包装已删除，因此生成在读完一兆字节之内停止（`BuildContext`
   的检查），阻塞的读不会被打断（与以前一样）。
6. **`bt_stream` 行为不变**：它从不在 `Read` 进行时关闭 reader；流的 context 由客户端离开和 `Drain` 取消，也结束对种子占用和 info
   的等待，并是每次读取 `read_timeout` 的父 context，因此 A83 在这里没有可删的东西。备选：在 `OnCancel` 中关闭 reader（需要跨
   goroutine 共享 reader，没有好处）。
7. **调低的 tracker 上限不丢弃任何东西**（fib 的 `SetConfig`）：超出的部分随 peer 停止或在 `peer_ttl` 内过期而离开。备选：立即
   裁剪（fib 不提供）。

由此遗留：A84（磁力链添加的 hybrid 种子的 piece layer）和 A86（做种方不宣告 `completed`）经由客户端生效，fit 无需改动，但 fit
本身未测试；一个纯 v2 对端从 fit 做种的磁力链 hybrid 种子获取哈希的互通用例可覆盖 A84。未运行：Linux 上的 race 测试和完整测试
（留给主会话）。

### I210 客户端角色用上 fib A87–A88 新增的能力

- **原因**：I203 留下两个权宜之计（其决定 5 与 6）：重新校验把种子从 fib 客户端移除再加入，断开连接、清零计数；`/trackers`
  只列出 .torrent 文件或磁力链接中的内容，没有 announce 状态。fib A87（`Torrent.Trackers`、`Sources`、`WebSeeds`）与 A88
  （`Torrent.Recheck`）已在 fib `4c6780e` 完成；fit 在 `404da7e` 上。
- **要做**：`Role.Recheck` 改用 `Torrent.Recheck`（不移除；连接与计数保留；状态显示 `checking`；之后保存续传数据）；
  `/trackers`（Admin API、控制台、页面、助手）显示各 tracker 的状态（上次与下次 announce、错误或警告、失败次数、peer、做种数、
  下载数、完成数、间隔）、peer 来源与 Web 种子状态，助手照旧隐去 URL。
- **完成标准**：重新校验保持 peer 连接与计数，被损坏的文件失去分片并重新下载；trackers 接口显示正常应答、拒绝与无法连接的
  tracker；隐去规则成立；页面为所显示内容提供文案；两种语言的文档。
- **涉及**：`bittorrent`、`admin`、`assistant`、`app/workbench`、`docs`。
- **已完成** 2026-11-09（分支 `bt-i210`）：**重新校验**（`bittorrent/inspect.go`）：`Role.Recheck` 就地调用 `Torrent.Recheck`，
  删除续传数据文件并忘掉为新客户端保留的副本（校验期间重启或替换客户端都会再次完整校验），标记待保存，保存器在 fib 给出续传
  数据后写入（校验期间会出错）；清除为该种子保留的存储错误；立即发布进入 `checking` 的状态变化。fib 拒绝（种子处于错误状态或
  正被移除）为 `ErrCannotCheck`，在 Admin API 上为 409。指标统计不再让该种子的计数从头开始。**Tracker**：`Role.Trackers` 返回
  `Trackers{Trackers, Sources, WebSeeds}`：每个 tracker 有 `url`、`tier` 及其 announce 状态（`updating`、`last_announce`、
  `next_announce`、`error`、`refused`、`warning`、`fails`、以秒计的 `interval` 与 `min_interval`、`peers`、`seeders`、
  `leechers`、`downloaded`（未知为 -1）、`scraped`），hybrid 种子的 v2 哈希在 `v2` 中；按名称列出来源（`tracker`、`dht`、
  `pex`、`lsd`、`manual`、`other`）及 `enabled`、`candidates`、`connected`；HTTP(S) Web 种子及 `active`、`fails`、`dropped`、
  `error`、`next_try`。不在 fib 客户端中的种子（损坏的）列出其元信息或磁力链接中的 tracker 与 Web 种子，状态未知。Admin API
  原样应答（`admin/bittorrent.go`）；控制台照常转发。**页面**（`app/workbench/web/bittorrent/bittorrent.js`）：Tracker 标签页
  每 4 秒重新读取，显示各 tracker 的状态标记、错误或警告、失败次数、上次与下次 announce、间隔、peer 与计数，hybrid 种子的 v2
  哈希另列一表，以及 peer 来源与 Web 种子状态；重新校验的确认说明 peer 保持连接；助手的“功能”页更新 `bt_torrent` 的文案。
  **助手**（`assistant/tools_bittorrent.go`）：`bt_torrent` 给出 tracker 状态、来源与 Web 种子状态；tracker 的 URL 仍只保留
  协议、主机与路径最后一段，Web 种子的去掉用户与查询；错误与警告中的每个 URL 都按 tracker 的方式显示，tracker 或 Web 种子自身
  URL 中可能是秘密的部分（用户、最后一段之前的路径、长度不少于 6 的查询值）替换为 `[redacted]`；运行较早 fit 的成员（Web 种子
  为纯 URL）仍可读取。集群的成员路径已允许 `/trackers`，无需改动。文档：[BitTorrent](bittorrent.zh-CN.md)（Admin API 的重新校验
  与 `/trackers` 行、Tracker 标签页、助手工具与隐去规则），两种语言；[fib-api-requests](fib-api-requests.zh-CN.md) 中 A87 与 A88
  标为已使用；I203 的决定 5 与 6 注明已改。测试：被重新校验（读取放慢）的做种方处于 `checking`、续传数据已删除，之后以同一个 fib
  任务继续做种，下载方仍连接（地址与 ID 相同），上传计数不减，无哈希失败，续传数据重新写入，下载方以正确内容完成；文件被改了
  两个字节的下载方在重新校验后失去两个分片并从做种方重新下载，文件随后正确；本地 fib tracker 上三个层级的 tracker：正常应答的
  （间隔 3600，1 个做种者，0 个下载者，scrape 得到 0 次完成，约一小时后下次 announce，两个哈希都如此）、拒绝的（`refused` 及
  原因）、无法连接的（有错误、非 `refused`、计数为 -1）；来源（tracker 启用、DHT 未启用、手动启用）；暂停时没有下次 announce；
  应答 404 的 Web 种子及其错误与失败次数；磁力链接在 info 到来前的 tracker 与 Web 种子；Admin API 的结构（无法连接的 tracker
  的状态、`v2`、六个来源、无 Web 种子）与 `ErrCannotCheck` 的 409；`bt_torrent` 在新结构下（状态照给，URL 与错误中没有
  passkey，Web 种子错误中没有用户与查询）与较早成员的结构下；`redactText` 的用例；页面的文案与字段。运行：`bittorrent`、`admin`、
  `assistant` 的 `go test -race`；`go test ./schema/ ./app/workbench/ ./docs/ ./test/workers/`；vet、gofmt。

**待审决定**（实现时做出，2026-11-09；在用户裁定前有效）：

1. **`web_seeds` 改为对象**（`{"url", "active", "fails", "dropped", "error", "next_try"}`），而不是 URL 列表旁另附状态，这改变了
   Admin API 的结构；页面与助手两种结构都能读（运行较早 fit 的集群成员）。理由：每种东西一个列表，与 `trackers` 一致。备选：
   `web_seeds` 保持为 URL，另加 `web_seed_state`。
2. **hybrid 种子的 v2 announce 是每个 tracker 中的 `v2` 对象**，与 fib 一致，页面只在 hybrid 种子时另列一表。备选：每个 URL 与
   哈希一行。
3. **时间用 RFC 3339，为零时省略；间隔为整秒；未知计数为 -1**，与 fib 一致。`refused` 只在 tracker 自己拒绝时（`*tracker.Error`）
   设置，让页面区分“被拒绝”与“无应答”。备选：时长用 Go 的纳秒，与 Admin API 其他限制一致。
4. **重新校验开始时仍删除续传数据文件**（并忘掉内存中的副本），校验期间崩溃或换了客户端都会再次完整校验；fib 给出续传数据后由
   保存器重新写入。理由：fib 的 `ResumeData` 在校验期间出错，旧文件会信任校验可能失败的分片。备选：保留旧文件（崩溃后会信任
   未再校验的分片）。
5. **fib 拒绝重新校验时应答 409**（`ErrCannotCheck`），与损坏的种子一样：是调用方可以改变的状态（先恢复它）。备选：400。
6. **Tracker 标签页每 4 秒轮询**（列表间隔的两倍，与“文件”一致），此前只读取一次。备选：只提供刷新按钮。
7. **助手按模式隐去错误与警告**：其中每个 URL 按 tracker URL 处理，tracker 或 Web 种子自身 URL 中可能是秘密的部分（用户、最后
   一段之前的路径、查询值）长度不少于 6 时替换，免得短的截断普通词语。错误文本中短于 6 的秘密会漏过（passkey 远长于此）。
   备选：助手的应答中去掉错误与警告。
8. **不在 fib 客户端中的种子（损坏的）应答其元信息中的列表，状态未知、没有来源**，而不是 409，与之前一致。

由此遗留：fit 中没有；运行较早 fit 的集群成员以旧结构应答 `/trackers`，助手能读（已测试），页面能显示（Web 种子没有状态）。
未运行：Linux 上的 race 测试和完整测试（留给主会话）。
