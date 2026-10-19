# fit 开发进度

[English](progress.md) | [简体中文](progress.zh-CN.md)

本文件按迭代记录实际完成的内容，以及下一次会话从哪里继续。[开发计划](dev-plan.zh-CN.md) 说明每个迭代应该做
什么；本文件说明实际做了什么，以及与计划不同的地方。

## 当前状态

| | |
| --- | --- |
| 最后更新 | 2026-10-19 |
| 里程碑 | M0 基础：已完成（I01–I08）。M1 代理核心：I09–I22 已完成；I23 进行中（一致性部分已完成，基准等待压测机） |
| fib | `github.com/lesismal/fib v1.3.0`，来自模块代理 |
| 可用的功能 | `fit run` 通过 HTTP/1.1、h2c、TLS（HTTP/1.1 和 h2，按 SNI 选择证书）和 HTTP/3 返回固定响应和静态文件，并能代理到 HTTP/1.1 上游，写 JSON 访问日志，收到 SIGHUP 时重载配置，并能优雅退出；路由按 host 和 path 匹配，先看优先级再看具体程度；`fit validate`、`fit schema`、`fit version`；基准框架可以跑 nginx、Caddy、Traefik 和 fit |
| 尚不可用 | 除证书文件外还没有 ACME 和证书管理（I28–I31）；还没有主动健康检查（I32）；还没有预压缩文件、回退和目录列表（I13）；还没有方法、请求头等其他匹配器（I36） |

**M0 已完成。** 退出标准都已满足，但有一点说明：`fit run -c fit.json` 通过 HTTP/1.1 和 HTTP/2（h2c；
基于 TLS 的 HTTP/2 在 I09）返回固定响应；nginx、Caddy、Traefik 的基线已提交，但它们是在 Mac 上的
Docker Desktop 里测的，不是在 Linux 主机上，见 I08。

**下一步：完成 I23** 的基准部分，在压测机上运行：压测框架的 static 和 proxy 场景，以及 10k 和 100k 空闲连接的内存，
结果提交到 `bench/results/` 并与基线比较。压测机拒绝了本会话的 ssh 登录（开发机上还没有密钥）。I24、I32 也都可以
开始（除非已有别的会话在做）。I24、I32 也都可以开始（除非已有别的会话在做）。

可以用下面的话开始下一次会话：

> 完成 docs/dev-plan.zh-CN.md 中的迭代 I23：一致性部分已完成（见 docs/progress.zh-CN.md），在压测机上运行基准。先读 docs/progress.zh-CN.md、docs/dev-plan.zh-CN.md 的索引和该迭代的
> 小节，以及 docs/roadmap.zh-CN.md 中的设计原则，然后只读它列出的包。改动限制在该迭代范围内，补上测试，完成后在
> 两个 dev-plan 文件中标记为 done，并记录到两个 progress 文件中。

## 已有的代码

| 包 | 内容 |
| --- | --- |
| `cmd/fit` | 命令行：`run`（收到 SIGHUP 时重载）、`validate`、`schema`、`version`、`help` |
| `config` | 原生配置类型；带错误路径和位置的严格 JSON 解析器与解码器；`Value`；`Duration`；语义检查 |
| `module` | 模块注册表；`Handler`、`Middleware`、`Matcher` 接口，以及暂定的 `Service`、`Upstream` 接口；`Provisioner`、`Cleaner`；`Env`，把配置中的上游连接池交给 `Provision` |
| `handler/respond` | `respond` handler |
| `handler/proxy` | `proxy` handler：转发到具名上游，去掉逐跳头，按可信代理列表设置 `X-Forwarded-*` 和 `Forwarded` |
| `handler/static` | `static` handler：根目录下的文件，小文件缓存，校验器，条件请求和 Range 请求，预压缩副本，`try_files`，目录列表 |
| `modules/standard` | 引入 fit 自带的所有模块 |
| `runtime` | `Compile(config, Env)` 把配置编译成不可变的 `Graph`，每个 listener 一个路由器、每个上游一个连接池；带原子替换和图引用计数的 `Runtime` |
| `listener` | 在一个 fib engine 上绑定图中的所有 listener（TCP，以及 HTTP/3 的 UDP）；把每个请求分发到当前图的路由；`Reload` 到新图，保留未变的 listener，排空被移除的；优雅退出 |
| `upstream` | `Pool`：按上游的策略（轮询、平滑加权轮询、最少连接）选择后端，通过 fib 的异步客户端以 HTTP/1.1、h2c 或 h2 发送请求，关闭前先排空；`StartEngine` 启动连接池拨号用的 engine |
| `observe` | 访问日志：每个请求一行 JSON，缓冲后由单独的 goroutine 写出，宁可丢行也不阻塞 |
| `tlsmgr` | listener 的 TLS 配置：证书文件、TLS 1.2+、AEAD 密码套件、按协议设置 ALPN |
| `router` | `Router[T]`：按 host 和 path 选出路由，先看优先级再看具体程度，不分配内存 |
| `schema` | JSON Schema 生成；`fit.schema.json`，由测试保证最新 |
| `internal/certtest` | 为测试生成自签名证书 |
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

### I23 M1 一致性测试与基准 — 进行中

已完成（见[报告](../bench/results/m1-conformance-2026-10-19.md)）：

- h2spec 2.0.0 针对 h2c 和 TLS h2 入口运行，分别由 `respond` 应答和经过代理：四次运行均为 145/145。
- `test/e2e/conformance_test.go`：通过 `fit` 二进制、逐字节构造请求的代理一致性测试：同时带两种长度的请求走私、
  冲突的 `Content-Length`、冒号前的空白、缺少和重复的 `Host`、错误的分块大小、字段中的 NUL、超过上限的头、obs-fold、
  `Expect: 100-continue`、HTTP/1.0 客户端、管线化、绝对形式的请求目标、上游返回的 304、204 和 1xx、不说 HTTP 的上游，
  以及任一方的 `Connection: close`。
- 发现：fib v1.3.0 把 `X-A : one` 当作名为 `X-A ` 的字段放行，而 RFC 9112 要求返回 400。代理现在拒绝字段名不是
  token 的请求（在复制请求头的循环中检查），这样它不会到达上游。fib 中的修复在 fib 的 `fix-field-name-whitespace`
  分支上，已提交、未推送，留给下一个 fib 版本。

剩余：在压测机上运行压测框架的 static 和 proxy 场景，以及 10k 和 100k 空闲连接内存，并与基线比较。

## 给后续迭代的说明

- **fib 版本。** fit 使用 fib v1.3.0，它是为 I16 和 I22 在 2026-10-19 的 fib `main` 上打的 tag：带来了
  `fib.New(config, bindings...)` 以及 `AddBinding`、`RemoveBinding`、`ShutdownBinding`，`Connection.NotifyOutput`、
  `Context.HoldBody` 和 `BodyWriter`。如果某个迭代需要更新的 fib，应该发布一个 fib 版本，而不是提交 `replace` 指令。
- **SO_REUSEPORT。** fib v1.3.0 默认开启 `ReusePort` 监听，这会让另一个进程也能绑定同一地址。`listener` 先用普通
  bind 检查每个地址，被占用时报错。
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
