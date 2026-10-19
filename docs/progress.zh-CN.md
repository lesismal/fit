# fit 开发进度

[English](progress.md) | [简体中文](progress.zh-CN.md)

本文件按迭代记录实际完成的内容，以及下一次会话从哪里继续。[开发计划](dev-plan.zh-CN.md) 说明每个迭代应该做
什么；本文件说明实际做了什么，以及与计划不同的地方。

## 当前状态

| | |
| --- | --- |
| 最后更新 | 2026-10-20 |
| 里程碑 | M0 基础：已完成（I01–I08）。M1 代理核心：已完成（I09–I23）；其审查修复 R01–R07d 已全部完成。M2 动态配置与 TLS：进行中（I24–I29、I32–I34） |
| fib | `github.com/lesismal/fib v1.5.2`，来自模块代理 |
| 可用的功能 | `fit run` 通过 HTTP/1.1、h2c、TLS（HTTP/1.1 和 h2，证书来自文件或经 ACME 的 HTTP-01 获取并续期，按 SNI 选择）和 HTTP/3 返回固定响应和静态文件，并能代理到 HTTP/1.1、h2c 和 h2 上游（带被动和主动的 HTTP、TCP 健康检查，九种负载均衡策略：轮询、加权轮询、最少连接、随机、P2C、按 IP、URI、请求头或 cookie 的一致性哈希，以及粘性 cookie），写 JSON 访问日志，收到 SIGHUP、文件变化（`-watch`）或通过 Admin API（读取、加载、按 JSON pointer 和 merge patch 修改、用 ETag 防止丢失更新、回滚到保存的版本）重载配置，在 Admin API 上提供 Prometheus 指标，并能优雅退出；路由按 host 和 path 匹配，先看优先级再看具体程度；`fit validate`、`fit schema`、`fit version`；基准框架可以跑 nginx、Caddy、Traefik 和 fit |
| 尚不可用 | ACME 只有 HTTP-01：还没有 TLS-ALPN-01、OCSP stapling、默认 HTTPS（I30）和按需 TLS（I31）；还没有方法、请求头等其他匹配器（I36） |

**M0 已完成。** 退出标准都已满足，但有一点说明：`fit run -c fit.json` 通过 HTTP/1.1 和 HTTP/2（h2c；
基于 TLS 的 HTTP/2 在 I09）返回固定响应；nginx、Caddy、Traefik 的基线已提交，但它们是在 Mac 上的
Docker Desktop 里测的，不是在 Linux 主机上，见 I08。R07b 在 Linux 主机上测了 M0 的 fit：固定响应吞吐与现在相差不到
1.3%（HTTP/1.1）和 4.2%（h2c）。

**M1 已完成。** I23 的一致性测试和基准见 [M1 报告](../bench/results/m1-report-2026-10-19.md)：h2spec 全部通过；
在 Linux 主机上，fit 满足路线图中 M1 的所有目标，代理吞吐高于 Caddy 和 Traefik，但在 HTTP/1.1 上是 nginx 的 83%。

**下一步：I30（TLS-ALPN-01、OCSP stapling、默认 HTTPS），然后是 I31（按需 TLS）。** M2 已完成 I24、I25（带配置版本、局部
修改、ETag 和回滚的 Admin API）、I26（旧图排空与连接池共享）、I27（文件 provider）、I28（证书管理器、存储和 ACME 选型）、I29（经 ACME 的 HTTP-01 获取并续期证书）、I32（主动健康检查）、I33（负载
均衡 v2）和 I34（Prometheus 指标）。M2 之前的审查修复 R01、R02 已
完成，热路径工作中不需要 fib 的 R07a、R07b 也已完成；R03–R06 也已完成：fib 提供了这些 API，代理的下载、静态 handler（自 R05b 起包括 sendfile）和这些上限都已使用它们，R07c 也借助 fib 降低了热路径开销。M1 的审查修复已全部完成。

**按规则审查（2026-10-19）。** 已按 [开发规则](rules.zh-CN.md) 审查 M0、M1。代理的下载路径和静态 handler 会在 fib
worker 上无时间上限地等待（已复现：一个不读取的客户端会卡住共享同一 h2c 上游连接的所有请求），还有若干上限没有安全的
默认值。修复见 [开发计划](dev-plan.zh-CN.md#m1-审查修复) 中的 R01–R07；fit 需要 fib 提供的 API 见
[fib-api-requests](fib-api-requests.zh-CN.md)。**R01、R02 只改 fit，应在 M2 之前完成**，即在 I24、I32 之前。

可以用下面的话开始下一次会话：

> 实现 docs/dev-plan.zh-CN.md 中的迭代 I30。先读 docs/progress.zh-CN.md、docs/rules.zh-CN.md、docs/dev-plan.zh-CN.md
> 的索引和该迭代的小节，以及 docs/roadmap.zh-CN.md 中的设计原则，然后只读它列出的包。改动限制在该迭代范围内，补上
> 测试，完成后在两个 dev-plan 文件中标记为 done，并记录到两个 progress 文件中。

## 已有的代码

| 包 | 内容 |
| --- | --- |
| `cmd/fit` | 命令行：`run`（收到 SIGHUP、通过 Admin API 或在 `-watch` 下文件变化时重载）、`validate`、`schema`、`version`、`help` |
| `admin` | `Configs`：每个新配置到达运行中服务器的唯一路径（加载、编译、`listener.Server.Reload`），一次一个，并保存已应用配置的历史，每个都有 ETag；`Update` 在同一把锁下基于运行中的配置做修改，支持 If-Match；`Rollback`；基于 net/http 的 Admin API：`GET/PUT/PATCH/DELETE /config[/<pointer>]`、`POST /load`、`POST /rollback/{n}`、`GET /versions`、`GET /versions/{n}`、`GET /metrics`（由 `WriteMetrics` 写出） |
| `config` | 原生配置类型；带错误路径和位置的严格 JSON 解析器与解码器；`Value`；`Duration`；语义检查 |
| `module` | 模块注册表；`Handler`、`Middleware`、`Matcher` 接口，以及暂定的 `Service`、`Upstream` 接口；`Provisioner`、`Cleaner`；`Env`，把配置中的上游连接池交给 `Provision` |
| `handler/respond` | `respond` handler |
| `handler/proxy` | `proxy` handler：转发到具名上游，去掉逐跳头，按可信代理列表设置 `X-Forwarded-*` 和 `Forwarded`；两个方向都不等待地流式转发，上传由 `HoldBody` 和 `OnDrain` 控制节奏，下载由 `WriteNoWait` 和 `OnWritable` 控制 |
| `handler/static` | `static` handler：根目录下的文件，缓存路径解析结果和小文件内容，所有文件操作都在有界的文件池上，大文件经 `SendFile` 发送；校验器，条件请求和 Range 请求，预压缩副本，`try_files`，目录列表 |
| `modules/standard` | 引入 fit 自带的所有模块 |
| `runtime` | `Compile(config, Env)` 把配置编译成不可变的 `Graph`，每个 listener 一个路由器、每个上游一个连接池，未变的上游共享前一个图的连接池；带原子替换、图引用计数和旧图排空超时的 `Runtime` |
| `listener` | 在一个 fib engine 上绑定图中的所有 listener（TCP，以及 HTTP/3 的 UDP）；把每个请求分发到当前图的路由；`Reload` 到新图，保留未变的 listener，排空被移除的；优雅退出 |
| `upstream` | `Pool`：按上游的粘性 cookie 和策略（轮询、平滑加权轮询、最少连接、随机、P2C、按客户端 IP、URI、请求头或 cookie 的一致性哈希）选择后端，通过 fib 的异步客户端以 HTTP/1.1、h2c 或 h2 发送请求，关闭前先排空；被动健康检查（`max_fails`）和主动健康检查：HTTP、TCP 探测由定时器发起，经 fib 的异步客户端和拨号器发出；`StartEngine` 启动连接池拨号用的 engine |
| `provider/file` | `Watcher`：轮询配置文件，在文件不再变化且内容与上次应用的不同时应用它 |
| `observe` | 访问日志：每个请求一行 JSON，缓冲在各有一把锁的分片中，由单独的 goroutine 写出，宁可丢行也不阻塞；`Track` 和 `EntryOf` 取得请求的日志条目；指标：`Histogram`、按 listener 和路由的 `RequestMetrics`（分片的原子计数器，在有图使用时保存在 `Metrics` 中），以及写 Prometheus 文本格式的 `TextWriter` |
| `tlsmgr` | `Manager`：在 `GetCertificate` 后面为握手选择证书；`Static`：来自文件、按名字索引的证书；`ServerConfig`：listener 的 TLS 配置，TLS 1.2+、AEAD 密码套件、按协议设置 ALPN；`Storage` 与 `FileStorage`：保存 ACME 的数据，带实例间的锁；`ACME`：在自己的 goroutine 上经 HTTP-01 获取证书并续期；`Certs`：listener 的 Manager，组合文件证书和经 ACME 获取的名字 |
| `router` | `Router[T]`：按 host 和 path 选出路由，先看优先级再看具体程度，不分配内存 |
| `schema` | JSON Schema 生成；`fit.schema.json`，由测试保证最新 |
| `internal/certtest` | 为测试生成自签名证书 |
| `internal/pebbletest` | 为测试运行 ACME 测试服务器 Pebble 及其 DNS 服务器；没有二进制文件时跳过测试 |
| `internal/fibtest` | 在测试中不经网络、像 fib 的服务器一样运行 fib handler，并等待之后才应答的 handler：`Serve`、`ServeRequest` |
| `test/e2e` | 编译 fit 二进制，经网络驱动 `fit run` |
| `bench` | 基准框架：`cmd/fitbench`、`cmd/idleconn`、四个服务器的配置、结果 |
| `examples` | `hello.json`、`proxy.json` |

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
- [`bench/results/baseline-2026-10-19-macos-docker.json`](../bench/results/baseline-2026-10-19-macos-docker.json)，
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

详见 [M1 报告](../bench/results/m1-report-2026-10-19.md)。

- 一致性（在开发机上）：h2spec 2.0.0 针对 h2c 和 TLS h2 入口，分别由 `respond` 应答和经过代理，四次运行均为
  145/145；以及 `test/e2e/conformance_test.go`：通过 `fit` 二进制、逐字节构造请求的代理一致性测试（同时带两种长度的
  请求走私、冲突的 `Content-Length`、冒号前的空白、缺少和重复的 `Host`、错误的分块大小、字段中的 NUL、超过上限的头、
  obs-fold、`Expect: 100-continue`、HTTP/1.0 客户端、管线化、绝对形式的请求目标、上游返回的 304、204 和 1xx、不说
  HTTP 的上游，以及任一方的 `Connection: close`）。
- 发现并修复：fib v1.3.0 把 `X-A : one` 当作名为 `X-A ` 的字段放行，而 RFC 9112 要求返回 400。代理拒绝字段名不是
  token 的请求，这样它不会到达上游。fib 中的修复作为 A9 写进了
  [fib-api-requests](fib-api-requests.zh-CN.md)，建议的补丁在 fib 本地分支 `fix-field-name-whitespace` 上，未推送；在此之前
  代理的检查是临时措施。
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
  每秒 228k 请求，约为 I23 时 nginx 258k 的 88%。与 nginx 的重新对比属于 M2 的退出基准（I35）。
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
  challenge 进行中时，从内存应答 `/.well-known/acme-challenge/<token>`；其他请求交给路由。
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

## 给后续迭代的说明

- **fib 版本。** fit 使用 fib v1.5.1。v1.4.0 带来了 R03 的 API（A1–A7、A9、A11、A13，以及 A8 和 A12 的一部分），
  v1.5.0 带来了 A12 的其余部分（`Client.Start`），v1.5.1 带来了 A14 和 A15，v1.5.2 带来了 A16 的第一部分；见 [fib-api-requests](fib-api-requests.zh-CN.md)。v1.3.1 在 v1.3.0 之上只增加了 `ClientConfig.StreamUnknownLengthAtHeader`（A10）。
  v1.3.0 是为 I16 和 I22 在 2026-10-19 的 fib `main` 上打的 tag：带来了
  `fib.New(config, bindings...)` 以及 `AddBinding`、`RemoveBinding`、`ShutdownBinding`，`Connection.NotifyOutput`、
  `Context.HoldBody` 和 `BodyWriter`。如果某个迭代需要更新的 fib，应该发布一个 fib 版本，而不是提交 `replace` 指令。
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
- **测试环境。** 单元测试和 `go test` 微基准在本机运行。压测（`fitbench` 框架、wrk、h2load）在压测机上运行，
  用 `ssh -p 10013 127.0.0.1` 登录。
- **基准测试。** fit 新支持的场景或模块要加进 `bench/cmd/fitbench/servers.go` 和
  `bench/configs/fit/fit.json`。nginx 会预分配连接表（`worker_connections 65536` 时每个 worker 约
  29 MB），这体现在打开任何连接之前的内存里，而不是每连接的开销里。
- **staticcheck。** 用较旧 Go 构建的 staticcheck 读不了 Go 1.27 的导出数据。按 CI 的方式（基于最新的
  `golang.org/x/tools`）构建，再传给 make：`make check STATICCHECK=/path/to/staticcheck`。
- **测试 handler。** `internal/fibtest.Serve` 和 `ServeRequest` 通过 `fibhttp.NewStreamContext` 和一个记录用的
  stream 运行 fib handler，不经过网络。带 content type 的 `Context.Respond` 会忽略 `c.Header()`；需要其他响应头
  字段时用 `WriteResponse`。
- **错误位置。** 错误测试比较带 行:列 的完整消息。期望的列号应该从文档计算，而不是手数。
- **许可证。** 仓库还没有 LICENSE 文件（fib 是 MIT），由维护者决定。
