# fit 开发进度

[English](progress.md) | [简体中文](progress.zh-CN.md)

本文件按迭代记录实际完成的内容，以及下一次会话从哪里继续。[开发计划](dev-plan.zh-CN.md) 说明每个迭代应该做
什么；本文件说明实际做了什么，以及与计划不同的地方。

## 当前状态

| | |
| --- | --- |
| 最后更新 | 2026-10-19 |
| 里程碑 | M0 基础：已完成（I01–I08）。M1 代理核心：I11、I12、I14、I15 已完成 |
| fib | `github.com/lesismal/fib v1.2.0`，来自模块代理 |
| 可用的功能 | `fit run` 通过 HTTP/1.1 和 h2c 返回固定响应和静态文件，并能代理到 HTTP/1.1 上游，并能优雅退出；路由按 host 和 path 匹配，先看优先级再看具体程度；`fit validate`、`fit schema`、`fit version`；基准框架可以跑 nginx、Caddy、Traefik 和 fit |
| 尚不可用 | 还没有 TLS；代理的 body 整体缓冲，也还没有超时（I16、I17）；还没有预压缩文件、回退和目录列表（I13）；还没有方法、请求头等其他匹配器（I36） |

**M0 已完成。** 退出标准都已满足，但有一点说明：`fit run -c fit.json` 通过 HTTP/1.1 和 HTTP/2（h2c；
基于 TLS 的 HTTP/2 在 I09）返回固定响应；nginx、Caddy、Traefik 的基线已提交，但它们是在 Mac 上的
Docker Desktop 里测的，不是在 Linux 主机上，见 I08。

**下一个迭代：I16，反向代理 II（流式与背压）**，I15 完成后它可以开始了。本会话接下来做 I21；I09、I13、I19、
I22 也都可以开始（除非已有别的会话在做）。

可以用下面的话开始下一次会话：

> 实现 docs/dev-plan.zh-CN.md 中的迭代 I16。先读 docs/progress.zh-CN.md、docs/dev-plan.zh-CN.md 的索引和该迭代的
> 小节，以及 docs/roadmap.zh-CN.md 中的设计原则，然后只读它列出的包。改动限制在该迭代范围内，补上测试，完成后在
> 两个 dev-plan 文件中标记为 done，并记录到两个 progress 文件中。

## 已有的代码

| 包 | 内容 |
| --- | --- |
| `cmd/fit` | 命令行：`run`、`validate`、`schema`、`version`、`help` |
| `config` | 原生配置类型；带错误路径和位置的严格 JSON 解析器与解码器；`Value`；`Duration`；语义检查 |
| `module` | 模块注册表；`Handler`、`Middleware`、`Matcher` 接口，以及暂定的 `Service`、`Upstream` 接口；`Provisioner`、`Cleaner`；`Env`，把配置中的上游连接池交给 `Provision` |
| `handler/respond` | `respond` handler |
| `handler/proxy` | `proxy` handler：转发到具名上游，去掉逐跳头，按可信代理列表设置 `X-Forwarded-*` 和 `Forwarded` |
| `handler/static` | `static` handler：根目录下的文件，小文件缓存，校验器，条件请求和 Range 请求 |
| `modules/standard` | 引入 fit 自带的所有模块 |
| `runtime` | `Compile(config, Env)` 把配置编译成不可变的 `Graph`，每个 listener 一个路由器、每个上游一个连接池；带原子替换和图引用计数的 `Runtime` |
| `listener` | 在一个 fib engine 上绑定图中的所有 listener；把每个请求分发到当前图的路由；优雅退出 |
| `upstream` | `Pool`：通过 fib 的异步客户端以 HTTP/1.1 向上游发送请求，关闭前先排空；`StartEngine` 启动连接池拨号用的 engine |
| `router` | `Router[T]`：按 host 和 path 选出路由，先看优先级再看具体程度，不分配内存 |
| `schema` | JSON Schema 生成；`fit.schema.json`，由测试保证最新 |
| `internal/fibtest` | 在测试中不经网络运行 fib handler：`Serve`、`ServeRequest` |
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

## 给后续迭代的说明

- **fib 版本。** fit 使用模块代理上的 fib v1.2.0。本地 fib 仓库的 `main` 比这个 tag 多约 30 个提交。如果某个迭代
  需要更新的 fib，应该发布一个 fib 版本，而不是提交 `replace` 指令。
- **图的生命周期与异步 handler。** `listener` 的分发器在 handler 返回时释放图。稍后才应答的 handler 要自己
  保证用到的东西可用：代理用 `Context.Retain` 持有请求，并依赖连接池在关闭前排空（决定 004）。
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
