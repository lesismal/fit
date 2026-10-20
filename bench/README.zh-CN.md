# fit 压测工具

[English](README.md) | [简体中文](README.zh-CN.md)

这套工具用等价的配置、同样的压测客户端跑 nginx、Caddy、Traefik 和 fit，把结果连同硬件、版本和
参数一起写成 JSON。fit 用它对照[路线图](../docs/roadmap.zh-CN.md)里的性能目标检查自己。

## 运行

在仓库根目录、一台装有 Docker 和 Go 的 Linux 机器上，一条命令：

```bash
go run ./bench/cmd/fitbench
```

它为 Linux 编译 fit 和 `idleconn`，构建压测客户端镜像，拉取各服务器镜像，让每个服务器跑完每个场景，
写出 `bench/results/run-<时间>.json`。默认参数下全程约 20 分钟。`go run ./bench/cmd/fitbench -h`
列出全部参数，常用的有：

| 参数 | 默认值 | |
| --- | --- | --- |
| `-servers` | `nginx,caddy,traefik,fit` | 要跑的服务器 |
| `-scenarios` | `fixed,static,proxy` | 吞吐场景；为空则不跑 |
| `-protocols` | `http/1.1,h2c` | |
| `-idle` | `http1,tls,ws` | 要测内存的空闲连接类型；为空则不测 |
| `-duration`、`-warmup` | `30s`、`5s` | 每轮测量时长，以及测量前不计入的预热 |
| `-connections` | `256` | wrk 的连接数 |
| `-h2-connections`、`-h2-streams` | `64`、`32` | h2load 的连接数和每连接并发流数 |
| `-idle-connections`、`-idle-settle` | `10000`、`5s` | |
| `-network` | Linux 上为 `host`，其他系统为 `bridge` | |
| `-server-cpus`、`-client-cpus`、`-backend-cpus` | 按 Docker 可用的 CPU 划分 | CPU 列表，如 `0-7` |
| `-nginx-image`、`-caddy-image`、`-traefik-image`、`-base-image` | 固定的版本 | |
| `-out` | `bench/results/run-<时间>.json` | |

fit 的结果只来自压测机，即使用 `-network host` 的 Linux 机器（[规则 16](../docs/rules.zh-CN.md#八压测)）。
工具在 macOS 或 Windows 的 Docker Desktop 下（虚拟机里，走 bridge 网络）仍能启动，但这样跑出的结果不采用：
虚拟机、bridge 网络和共享的 CPU 都有 Linux 主机没有的开销。M0 用这种方式测得的基线只作为记录保留；
以 M1 起在 Linux 上的结果为准。

## 各进程跑在哪里

每个进程都在自己的容器里，绑定到各自的 CPU，`nofile` 调到 1048576：

- **被测服务器**：一次一个，用官方镜像（fit 用本次编译的二进制，跑在 `ubuntu:24.04` 上）；
- **压测客户端**：wrk 和 h2load，用 Ubuntu 的 glibc 版本，另有 curl 和 `idleconn`；
- **代理后端**：fit，对所有请求返回固定响应，端口 9000；
- **WebSocket 后端**：`idleconn -serve-ws`，接受升级并保持连接，端口 9001。

有 N 个 CPU 时，后端分八分之一（至少一个），其余的服务器和压测客户端各一半。nginx 每个服务器 CPU
一个 worker；Go 写的服务器通过 CPU 亲和性看到分给它的 CPU。

## 场景

[`configs`](configs) 里每个服务器的配置都在两个端口上提供同样的路由：8080 是 HTTP/1.1，由 wrk 压；
8081 是 h2c（预先知道的明文 HTTP/2），由 h2load 压。

| 场景 | 路径 | 响应 |
| --- | --- | --- |
| `fixed` | `/fixed` | 200，`text/plain`，`hello, world` |
| `static` | `/static/small.html` | [`www/static/small.html`](www/static/small.html)，1 KiB |
| `proxy` | `/proxy` | 经 HTTP/1.1 长连接代理到后端；`hello, world` |

每个代理都保持最多 4096 个到后端的空闲连接，多于压测客户端同时在途的请求数。用默认值（Caddy 为 32，
Traefik 为 200）时，它们在压力下不断关闭、重开后端连接直到端口耗尽，测到的是默认值而不是代理本身。

每轮之前，工具先用该协议请求一次，检查状态码、协议和响应体是否符合预期，配置走样的服务器会报错，
而不是测了别的东西。服务器不支持的场景记为跳过：Traefik 只能代理；fit 在 I12 支持 `static`，在 I15
支持 `proxy`。

每轮测量记录每秒请求数、延迟（wrk 给出百分位；h2load 不给百分位，只有均值、标准差和最大值）、按
类型分的错误数、服务器用掉的 CPU（取自其 cgroup 的 `cpu.stat`，以核数计），以及结束时的内存
（cgroup 的 `memory.current`，和 `memory.stat` 里的 `anon`）。

## 空闲连接

`idleconn` 打开 `-idle-connections` 个连接，每个先发一个请求，然后保持空闲：

| 类型 | 连接 |
| --- | --- |
| `http1` | 8080 上的 HTTP/1.1 长连接，先请求 `/idle`，各服务器都返回 404 |
| `tls` | 同上，走 8443 上的 TLS 1.3，用一次性的 ECDSA P-256 证书 |
| `ws` | 8080 上 `/ws` 的 WebSocket，由服务器代理到 WebSocket 后端 |

每种类型都对一个新启动的服务器测。工具在打开连接前、以及连接空闲 `-idle-settle` 之后各读一次服务器
的匿名内存，用差值除以连接数。匿名内存是服务器各进程持有的内存，包括容器里的所有进程（如 nginx 的
worker），不含页缓存；相当于 RSS 去掉文件映射的部分。WebSocket 的开销包含服务器到后端的那条连接，
因为这里每个服务器都是代理 WebSocket。

## 结果

[`results`](results) 存放提交进仓库的结果：`baseline-*.json` 是 M0 为 nginx、Caddy 和 Traefik
建立的基线，后续里程碑拿 fit 与之比较。要复现某个结果，在相近的硬件上用其中记录的 `params` 运行：

```bash
go run ./bench/cmd/fitbench -servers nginx,caddy,traefik,fit -out bench/results/baseline-$(date +%Y%m%d).json
```

结果 JSON 包含 `host`（CPU、内核、内存）、`versions` 和 `images`（带摘要）、`params`，`results`
里每个服务器、场景、协议一项，`idle` 里每个服务器、连接类型一项。
