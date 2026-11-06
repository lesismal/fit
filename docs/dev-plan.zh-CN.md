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
| `adapter/...` | 配置适配器：友好格式、nginx |
| `bench` | 基准测试框架，nginx、Caddy、Traefik 和 fit 的配置，测试结果 |
| `test/e2e` | 端到端测试和一致性测试 |
| `internal/fibtest` | 在测试中不经网络运行 fib handler |
| `examples` | 示例配置；已迁到 [fit-examples](https://github.com/lesismal/fit-examples) |

## 索引

状态：`todo`、`doing` 或 `done`。下一个可用的迭代编号是 I162。

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
| I128 | 在工作处询问 AI：保存错误、一致性、引擎菜单、表单字段、指标与角色页面上的入口 | I127 | todo |
| I129 | 迁移与审查：`migrate` 与 `security-review` 端到端，在固定配置集上评估 | I127 | todo |
| I130 | 基于工具注册表的 MCP 服务器：工具、作为 prompts 的 skill、作为 resources 的配置与 schema；按用户签发、最高 `propose` 的 token；提议只在 workbench 中应用 | I126, I127 | todo |
| **媒体库** | | | |
| I131 | 决定 032；fib A64，非分片 MP4 文件的读取器：经 `io.ReaderAt` 读取样本表，按时间和关键帧查找样本，内存有上限 | I116 | todo |
| I132 | 媒体库：扫描 `media_library` 的根目录生成条目目录（轨道、编码、多码率、字幕、海报），在有上限的文件池上探测，Admin API | I131 | todo |
| I133 | 点播：处理器 `vod`，从样本表按关键帧切分的 HLS（fMP4 或 MPEG-TS）、多码率、音轨与字幕组、Range、`auth` | I132 | todo |
| I134 | 频道：媒体库条目作为直播流推入 hub，按顺序、循环或排期；`push` 到外部 RTMP 和 SRT 服务器 | I132 | todo |
| I135 | 会过期的播放 URL：绑定资源、地址或会话的 `url_signing`，授权传递到分片，`media_tokens` 的 `POST <prefix>/urls`，`max_sessions` | I119, I133 | todo |
| I136 | 加密分片：AES-128 HLS，密钥只提供给持有该资源授权的请求 | I135 | todo |
| I137 | 影视站端到端：示例、`media_pages` 的媒体库与播放页面、文档 | I134, I135 | todo |
| I138 | workbench 的媒体库页面：菜单条目、配置页面、带预览的媒体库浏览、时间线上的频道、推流目标、测试 URL、密钥与当前会话、指标 | I135, I123 | todo |
| I139 | 在 workbench 中管理媒体文件：断点续传上传、重命名、移动、删除与 sidecar，有上限并需确认 | I138 | todo |
| **集群管理** | | | |
| I140 | 决定 033；成员所需：`GET /node`、`PUT`/`PATCH /config` 试运行、程序可用的登录、失败登录退避；配置页的校验按钮 | I121 | todo |
| I141 | 管理节点：`admin.cluster`，成员及其凭据保存在 store 中（控制台密码或 Admin API token，TLS 固定），有上限的状态轮询 | I140 | todo |
| I142 | workbench 的集群菜单：概览、节点和节点详情页；`docs/cluster.md` | I141 | todo |
| I143 | 从管理节点打开成员自己的页面：节点切换器，用保存的会话转发其 API | I142 | todo |
| I144 | 按标签或名字的资源组，对多个成员的批量操作（重载、回滚、检查、清缓存）及逐成员结果；资源组页面 | I142 | todo |
| I145 | 模板：组内成员共享的配置路径，带变量、同步状态，冲突在成员上标出；模板页面 | I144 | todo |
| I146 | 发布：逐成员 diff 和试运行，一次全部、滚动或金丝雀，健康门槛，失败回滚；发布页面 | I145 | todo |
| I147 | 漂移与告警：以发布修复漂移，告警规则发送到 webhook；漂移与告警页面 | I146 | todo |
| I148 | 管理节点的用户及各组角色，审计日志；用户与审计页面 | I146 | todo |
| I149 | NAT 后的成员主动连到管理节点，用一次性 token 注册；通过注册添加 | I141 | todo |
| I150 | 跨成员的指标、日志与证书，CLI 的 `fit cluster`，助手的集群工具；对应页面 | I147, I125 | todo |
| **AI 助手（续）** | | | |
| I152 | 助手的媒体库工具与 skill：编目、频道、会话，以及以提议方式进行的重新扫描、密钥轮换与文件修改；`vod-setup`、`channel-scheduler`、`library-health`、`playback-troubleshoot` | I127, I138 | todo |
| I153 | 跨集群的助手：读取成员状态、模板与发布，以发布计划提出修改，检查管理节点的角色；`fleet-health`、`rollout-planner`、`cluster-drift` | I127, I146, I148 | todo |
| I154 | 助手在页面上的修改：`edit` 级别，以及把修改作为未保存草稿写进当前页面编辑器与表单的页面工具，可撤销，从不提交——**保存并应用** 仍由人完成 | I125, I126, I120 | todo |
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

### I129 迁移与审查

- **做**：`migrate`：nginx 与 Caddyfile 原文先经 `fit adapt`，转换器报告无法转换的部分交给模型，Traefik 的 YAML 或 label 完全由
  模型转换，每个结果都是一个提议，并列出无法映射的项；`security-review`：带严重程度、成员与修复提议的发现。一组固定的输入配置
  及期望结果，按需针对选定模型运行，报告有多少能转换为可通过校验并保留路由的配置。
- **完成条件**：该组配置转换并通过校验，未转换项被列出；审查找出其测试集中预设的问题；该组用 `make assistant-eval` 运行，
  提供方由环境变量设置，默认不在 CI 中运行。
- **涉及**：`assistant`、`adapter`、`cli`、`docs`。

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

### I152 助手的媒体库工具与 skill

- **做**：上表中媒体类的工具与 skill，基于 I132–I136 的媒体库、`vod`、频道与 `url_signing`；propose 工具与其他提议一样应用（I126），
  需点击应用并在弹窗中确认；文件类提议在文件被某频道使用或正被某会话播放时给出警告。媒体库页面（I138）上的“问 AI”入口：条目的
  探测错误（`library-health`）、出错的频道推流目标（`channel-scheduler`）、按原因统计的 403（`playback-troubleshoot`）。
- **完成条件**：每个 skill 在固定媒体库上、针对录制模型完成其任务；录制的请求中不出现任何密钥、签名或播放 URL；除非允许，subject
  以哈希形式出现；删除某频道正在播放的文件的提议会指出该频道；在 race 检测下通过。
- **涉及**：`assistant`、`admin`、`medialib`、`app/workbench`、`docs`。

### I153 跨集群的助手

- **做**：在管理节点上（I141）提供上表中集群类的工具与 skill；只读工具的 `member` 参数经管理节点的会话访问该成员的 API；propose
  工具转为发布计划或批量操作，显示在发布页面上，只有经其自身的确认才开始（I146）。助手的级别与管理节点按组的角色（I148）都要检查，
  以更窄者为准。集群页面上的“问 AI”入口：概览卡片（`fleet-health`）、失败的批次（`rollout-planner`）、漂移项（`cluster-drift`）。
  MCP 服务器（I130）在管理节点上同样提供这些工具。
- **完成条件**：用自然语言为某组提出的修改成为带每个成员 diff 与试运行的发布计划，且只在确认后执行；`operator` 不能对其无权编辑的
  模板提出修改；录制的请求中不出现任何成员凭据；某成员慢或下线时，跨成员读取仍在上限之内；在 race 检测下通过。
- **涉及**：`assistant`、`cluster`、`app/workbench`、`docs`。

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

### I134 频道：把文件作为直播流播放并向外推流

- **做**：`streaming.channels`：频道以其流名向 hub 推送一组媒体库条目，按顺序、随机、循环或按某时区的排期开始时间播放，用 fit 的定时器按
  样本时间戳控制节奏，条目之间时间戳保持连续（HLS 中为 discontinuity）；流媒体角色的每种出口都能播放它（HLS、HTTP-FLV、WHEP，以及
  RTMP、RTSP 和 SRT 播放），各按其 `auth`；`push` 目标把频道或 hub 中任意流转推到外部 RTMP 或 SRT 服务器（如 CDN 的接入点），断开后带退避
  重连；频道在重载时原地修改，新的列表或排期从下一个条目起生效；Admin API 列出频道和推流目标，并可开始、停止和跳过。
- **完成标准**：一个频道连续播放两个条目，经每种出口用 ffmpeg 和 hls.js 播放，没有间断，时间戳没有跳变；排期的条目按时开始；推流目标
  （fit 自身，以及容器中的外部服务器）收到流，并在目标重启后恢复；在 race 检测下通过。
- **涉及**：`streaming`、`medialib`、`config`、`schema`、`docs`、`test/workers`。

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

### I136 加密分片

- **做**：`vod` 和频道 HLS 上的 `encrypt`：分片用 AES-128 加密（`METHOD=AES-128`，MPEG-TS 和 fMP4 均可），每个条目或频道一个从媒体库密钥派生
  的密钥，频道每 `key_rotation` 个分片轮换一次；密钥 URI 只对持有该资源授权的请求提供，使没有授权而复制走的分片无法播放；加密在文件池上进行，
  缓冲区有上限。
- **完成标准**：hls.js 和 ffmpeg 能播放加密的条目和频道；没有授权或授权属于其他资源的密钥请求被拒绝；单独取得的分片无法解码；在 Linux 上测量
  每个分片的开销。
- **涉及**：`handler/vod`、`streaming`、`medialib`、`schema`、`docs`。

### I137 影视站端到端

- **做**：[fit-examples](https://github.com/lesismal/fit-examples) 中的示例：一个媒体库、`vod` 和频道路由、置于路由中间件之后供影视站 API
  使用的 `media_tokens`、一个让观众登录并发放 URL 的小型 Go API，以及一个使用 hls.js 的页面。`media_pages` 增加在策略登录之后的媒体库页面和
  播放页面（hls.js 随附，Apache-2.0）。`docs/media-library.md`，中英文。
- **完成标准**：在 Linux 的 Docker 中，无头 Chromium 登录示例 API，经其签发的 URL 播放一部电影和一个频道、拖动进度，并在 URL 及其授权过期
  后被拒绝；`TestChapterExamples` 和 schema 描述覆盖新字段。
- **涉及**：`handler/mediapages`、`schema`、`docs`、`test/docker`、fit-examples。

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

### I139 在 workbench 中管理媒体文件

- **做**：在 workbench 中管理媒体库的文件：上传到根目录的 `upload_dir`，分块断点续传（`POST` 创建、`PATCH` 按偏移写入、`HEAD` 查询续传位置），
  受 `max_upload_bytes`、并发上传数和磁盘剩余空间限制，在文件池上写入临时文件、完整后重命名到位，再进行探测；重命名、在根目录内的文件夹间
  移动和删除，各需确认；用表单编辑 sidecar 的标题、海报、多码率和字幕；移动或删除正被频道使用或仍在播放的文件前给出警告；每个修改都需要登录
  用户、经 CSRF 校验，审计日志存在后（I126）写入审计日志。
- **完成标准**：数 GB 的上传在连接中断后能续传，探测完成后可以播放；超过上限的上传返回 413 且不留下残缺文件；根目录之外的路径被拒绝；sidecar
  指定了 `id` 时重命名保持条目 `id` 不变；删除的条目从目录和缓存中移除；在 race 检测下通过。
- **涉及**：`medialib`、`admin`、`app/workbench`、`schema`、`docs`。

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

### I143 从管理节点打开成员自己的页面

- **做**：workbench 页头的节点切换器：选择一个成员后，workbench 自己的页面（配置及其文本标签页、引擎、一致性、版本、各角色页面）都针对
  该成员，管理节点用保存的会话把 `/api/n/<member>/...` 转发到成员的 API；schema 从成员读取；标题栏以单独的颜色标出成员名，避免改错实例；
  bundle 不认识其版本的成员以只读方式打开。经切换器的每次写入都以其用户记入管理节点的审计日志。
- **完成标准**：每个配置页都能读取和保存成员的配置，保存错误链接到正确字段；成员上过期的 ETag 像在本地一样被拒绝；更旧或更新版本的成员
  以只读打开；切回管理节点后页面上不残留成员数据；请求体上限有效；浏览器检查两种语言通过。
- **页面**：节点切换器；所有已有页面（针对成员）。
- **涉及**：`cluster`、`app/console`、`app/workbench`、`docs`。

### I144 资源组与批量操作

- **做**：组：标签选择器、按名字列出的成员，或两者兼有；一个成员可属于多个组；供 I145 使用的组变量。对一个组或选中成员的操作，以有上限的
  并发执行、按成员返回结果：重载、回滚到上一版本、运行一致性检查、按键或前缀清除缓存区、测试连通性。`/api/m/cluster/groups` 和
  `POST /api/m/cluster/ops`。
- **完成标准**：选择器的成员随标签变化而变化；对五个成员（其中一个宕机）的操作报告四个完成、一个失败及其错误；清除操作到达每个成员的缓存区；
  在竞态检测下通过。
- **页面**：集群 → 资源组：列表、组的表单（选择器及其预览成员、变量）、组页面（成员状态和操作栏）；概览按组筛选；每个操作在列出成员的对话
  框中确认，并按成员显示结果。
- **涉及**：`cluster`、`app/workbench`、`docs`。

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

### I147 漂移与告警

- **做**：每次轮询检查漂移：每个成员对照其模板及其自身的一致性检查；修复即对该成员的一次发布（I146），从不自动应用。告警规则：成员宕机一段
  时间、漂移、发布失败、证书在若干天内到期、上游后端下线、重载失败；发送到 webhook（通用 JSON，附聊天服务模板），带重复间隔和静默，
  webhook 的密钥保存在 store 中；告警记录有上限。
- **完成标准**：每条规则在条件持续到设定时间后触发一次，恢复后解除；静默有效；超时的 webhook 不拖慢轮询器；在竞态检测下通过。
- **页面**：集群 → 漂移与告警：未同步成员及其 diff，**修复** 打开发布计划；告警规则表单、webhook、当前和历史告警、静默；有告警时集群菜单
  显示角标。
- **涉及**：`cluster`、`app/workbench`、`docs`。

### I148 用户、角色与审计日志

- **做**：管理节点的控制台用户在各组上有角色：`viewer`（只读）、`operator`（批量操作和发布已有模板）、`admin`（成员、凭据、模板、组），
  在每个端点和每次经切换器的写入上检查；两者都开启时与助手的等级（I126）一致。每个集群操作的审计日志：用户、时间、成员、操作、diff 摘要、
  结果及前后版本，保存在 store 中，有上限；可导出为 JSON lines。
- **完成标准**：每个角色在 API 中被拒绝其无权的操作，页面中相应控件隐藏；I141–I147 的每个操作都留下记录；日志在上限内跨重启保留；在竞态
  检测下通过。
- **页面**：集群 → 用户与审计：用户在各组上的角色；审计日志可按用户、成员、操作和时间筛选，每条可打开其 diff。
- **涉及**：`cluster`、`login`、`app/workbench`、`docs`。

### I149 NAT 后的成员主动连出

- **做**：成员上的 `admin.cluster_member`：管理节点的 URL，以及在管理节点生成、只能使用一次以换取自己凭据的注册 token；成员用 fib 的客户端
  经 WebSocket 主动连到管理节点，带退避重连，管理节点经该连接访问成员的 API，与经 HTTP 访问其他成员相同。成员在注册时固定管理节点的证书；
  在管理节点上删除的成员失去其凭据。
- **完成标准**：没有开放端口的成员能像其他成员一样使用以上全部功能；连接在管理节点重启和网络中断后恢复；已用或过期的注册 token 被拒绝；
  在竞态检测下通过。
- **页面**：节点页增加 **通过注册添加**：生成带有效期的 token 和要粘贴到成员上的配置行，并显示成员已连接或等待中。
- **涉及**：`cluster`、`config`、`admin`、`app/workbench`、`schema`、`docs`。

### I150 跨集群的指标、日志与证书

- **做**：管理节点从每次轮询保存的时间序列，每个成员有上限：每秒请求数、4xx 和 5xx、连接数、字节数、重载；按组汇总。经 fit 的日志环
  （I125）同时追踪多个成员的日志，按时间合并。证书清单：每个成员的证书及其名字、签发者和到期时间。CLI 的 `fit cluster`（节点、状态、组、
  diff、发布的开始/状态/中止），经管理节点的 Admin API。助手的集群工具见 I153。
- **完成标准**：时间序列与成员自身的指标一致；合并追踪跟随三个成员；某成员即将到期的证书排在最前；CLI 端到端驱动一次发布；在竞态检测下通过。
- **页面**：集群 → 指标（按成员和组的图表）、日志（选择成员、筛选、跟随）、证书（清单，按到期排序）；概览中的迷你趋势图。
- **涉及**：`cluster`、`cli`、`app/workbench`、`docs`。

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
