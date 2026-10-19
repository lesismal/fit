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
| `cmd/fit` | 命令行：`run`、`validate`、`adapt`、`build`、`version` |
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
| `tlsmgr` | 证书管理、存储、ACME、OCSP |
| `admin` | Admin API 和配置版本 |
| `provider/...` | file、docker、kubernetes、consul、etcd |
| `observe` | 访问日志、指标、追踪 |
| `plugin/...` | 编译期模块和 WASM |
| `adapter/...` | 配置适配器：友好格式、nginx |
| `bench` | 基准测试框架，nginx、Caddy、Traefik 和 fit 的配置，测试结果 |
| `test/e2e` | 端到端测试和一致性测试 |
| `internal/fibtest` | 在测试中不经网络运行 fib handler |
| `examples` | 示例配置 |

## 索引

状态：`todo`、`doing` 或 `done`。

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
| R07c | 依赖 fib 的热路径开销 | R03、R07a、R07b | todo |
| **M2** | | | |
| I24 | 配置版本与 Admin API 骨架 | I22 | done |
| I25 | Admin API：局部修改、ETag、回滚 | I24 | done |
| I26 | 监听差量与旧图排空 | I24 | done |
| I27 | 文件 provider | I24 | done |
| I28 | 证书管理与存储；ACME 选型 | I09 | todo |
| I29 | ACME HTTP-01 与续期 | I28 | todo |
| I30 | TLS-ALPN-01、OCSP stapling、默认 HTTPS | I29 | todo |
| I31 | 按需 TLS | I30 | todo |
| I32 | 主动健康检查 | I20 | done |
| I33 | 负载均衡 v2 | I19 | done |
| I34 | Prometheus 指标 | I23 | todo |
| I35 | M2 退出测试 | I25、I26、I27、I31、I32、I33、I34 | todo |
| **M3** | | | |
| I36 | 匹配器 v2 与优先级 | I11 | todo |
| I37 | 表达式匹配器 | I36 | todo |
| I38 | 重写、跳转与头处理 | I36 | todo |
| I39 | 限流、并发与大小限制 | I36 | todo |
| I40 | Basic 认证与客户端 mTLS | I36 | todo |
| I41 | ForwardAuth | I40 | todo |
| I42 | JWT 与 JWKS | I40 | todo |
| I43 | 重试与按路由超时 | I17 | todo |
| I44 | 熔断 | I43 | todo |
| I45 | 流量拆分：加权、镜像、故障转移 | I19 | todo |
| I46 | 压缩 | I36 | todo |
| I47 | WebSocket 代理 | I17 | todo |
| I48 | HTTP/2、HTTP/3 入口上的 WebSocket | I47、I10 | todo |
| I49 | gRPC 代理 | I18 | todo |
| I50 | TCP 代理 | I05 | todo |
| I51 | UDP 代理 | I50 | todo |
| I52 | SNI 分流与 PROXY protocol | I50 | todo |
| I53 | 一个端口多种协议；SOCKS 与 CONNECT | I52 | todo |
| I54 | 上游 TLS | I18 | todo |
| I55 | M3 退出测试 | I37–I54 | todo |
| **M4** | | | |
| I56 | Provider 框架与生效配置 | I27 | todo |
| I57 | Docker provider | I56 | todo |
| I58 | Kubernetes Gateway API I：HTTPRoute | I56 | todo |
| I59 | Kubernetes Gateway API II：其他路由与状态 | I58 | todo |
| I60 | Kubernetes Ingress | I58 | todo |
| I61 | Gateway API 一致性测试 | I59 | todo |
| I62 | Consul 与 etcd provider | I56 | todo |
| I63 | 共享证书存储 | I30 | todo |
| I64 | 仪表盘 | I56 | todo |
| I65 | OpenTelemetry | I34 | todo |
| I66 | 打包与示例 | I57 | todo |
| **M5** | | | |
| I67 | 缓存 I：HTTP 语义与内存层 | I23 | todo |
| I68 | 缓存 II：磁盘层 | I67 | todo |
| I69 | 缓存 III：缓存锁、stale、清除 | I68、I25 | todo |
| I70 | `fit build` 与编译期模块 | I04 | todo |
| I71 | WASM I：运行时与插件池 | I36 | todo |
| I72 | WASM II：Proxy-Wasm 子集 | I71 | todo |
| I73 | 友好配置格式 | I03 | todo |
| I74 | nginx 配置转换器 | I73 | todo |
| I75 | FastCGI | I17 | todo |
| **1.0** | | | |
| I76 | 持续 fuzz | I55 | todo |
| I77 | 配置与 Admin API v1 冻结 | I69、I74 | todo |
| I78 | 参考文档与迁移指南 | I77 | todo |

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

### I30 TLS-ALPN-01、OCSP stapling、默认 HTTPS

- **做什么**：TLS-ALPN-01 验证；OCSP stapling 及刷新；配置中的每个域名默认获得托管证书和 HTTP→HTTPS 跳转，
  可关闭。
- **完成标准**：只写了域名的配置能在 Pebble 下提供 HTTPS；针对 Let's Encrypt 测试环境的测试有文档，可手动运行。
- **涉及**：`tlsmgr`、`runtime`。

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

- **做什么**：GRPCRoute、TLSRoute、TCPRoute、UDPRoute；把状态条件写回资源。
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

路线图 1.0 中的外部安全审计不是一个迭代：它在 I77 完成后于代码之外安排。
