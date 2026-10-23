# fit 压测工具

[English](README.md) | [简体中文](README.zh-CN.md)

这套工具用等价的配置、同样的压测客户端跑 nginx、Caddy、Traefik、Envoy 和 fit，把结果连同硬件、版本和
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
| `-servers` | `nginx,caddy,traefik,envoy,fit` | 要跑的服务器 |
| `-scenarios` | `fixed,static,proxy` | 吞吐场景；为空则不跑 |
| `-protocols` | `http/1.1,h2c,h2,h3` | |
| `-idle` | `http1,tls,ws` | 要测内存的空闲连接类型；为空则不测 |
| `-duration`、`-warmup` | `30s`、`5s` | 每轮测量时长，以及测量前不计入的预热 |
| `-connections` | `256` | wrk 的连接数 |
| `-h2-connections`、`-h2-streams` | `64`、`32` | h2load 的连接数和每连接并发流数，h2c、h2 和 h3 相同 |
| `-idle-connections`、`-idle-settle` | `10000`、`5s` | |
| `-network` | Linux 上为 `host`，其他系统为 `bridge` | |
| `-server-cpus`、`-client-cpus`、`-backend-cpus` | 按 Docker 可用的 CPU 划分 | CPU 列表，如 `0-7` |
| `-nginx-image`、`-caddy-image`、`-traefik-image`、`-envoy-image`、`-base-image` | 固定的版本 | |
| `-out` | `bench/results/run-<时间>.json` | |

fit 的结果只来自压测机，即使用 `-network host` 的 Linux 机器（[规则 16](../docs/rules.zh-CN.md#八压测)）。
工具在 macOS 或 Windows 的 Docker Desktop 下（虚拟机里，走 bridge 网络）仍能启动，但这样跑出的结果不采用：
虚拟机、bridge 网络和共享的 CPU 都有 Linux 主机没有的开销。M0 用这种方式测得的基线只作为记录保留；
以 M1 起在 Linux 上的结果为准。

## 对比两个版本

大多数改动用不到完整的基准框架（[规则 20–23](../docs/rules.zh-CN.md#十按改动大小验证)）。下面两个脚本都在本机
交叉编译 linux/amd64，通过 ssh master 在压测机上运行。

- `bench/ab.sh [选项] A B` 对比 fit 的两个 git ref（或 `work`，即工作区）：只跑 fit，只跑给定的场景和协议
  （`-scenarios`、`-protocols`），预热 2 秒后每次跑 10 秒，A、B 交替 3 轮；`-extra` 合并进 fit 的配置（访问日志、
  指标等）。它在压测机上启动后返回一个 id；`bench/ab.sh -report ID` 取回已完成的结果，汇总每秒请求数和每请求
  CPU，变化超过 1%（`AB_NOISE`）且超出各轮波动才标出；`-wait ID` 等到结束。一个场景一个协议约一分半，默认组合约
  八分钟。
- `bench/microbench.sh [-C 仓库] -pkg 包 -bench 正则 A B` 在压测机上对两个 ref 交替各跑 10 次某个包的微基准，
  本机用 benchstat 对比；`-C ../fib` 用于 fib。约一分钟。

## 各进程跑在哪里

每个进程都在自己的容器里，绑定到各自的 CPU，`nofile` 调到 1048576：

- **被测服务器**：一次一个，用官方镜像（fit 用本次编译的二进制，跑在 `ubuntu:24.04` 上）；
- **压测客户端**：wrk 和 h2load，用 Ubuntu 的 glibc 版本，另有 curl 和 `idleconn`；HTTP/3 用从源码构建的
  h2load 1.68（ngtcp2 1.16、nghttp3 1.12，基于 OpenSSL 3.5 的 QUIC API，见
  [`loadgen/Dockerfile.h3`](loadgen/Dockerfile.h3)）和 Debian trixie 的 curl，在单独的容器里、同样的 CPU 上；
  没有发行版提供支持 HTTP/3 的 h2load，所以第一次运行时要花几分钟构建；
- **代理后端**：fit，对所有请求返回固定响应，端口 9000；
- **缓存后端**：fit 对 `/cache/` 下的每个路径都回答 4 KiB、新鲜期 10 分钟（`Cache-Control: max-age=600`），
  端口 9003，并在 127.0.0.1:9004 的 Admin API 上计数它回答的请求；
- **WebSocket 后端**：`idleconn -serve-ws`，接受升级并保持连接，端口 9001。

有 N 个 CPU 时，后端分八分之一（至少一个），其余的服务器和压测客户端各一半。nginx 每个服务器 CPU
一个 worker；Go 写的服务器通过 CPU 亲和性看到分给它的 CPU。

## 场景

[`configs`](configs) 里每个服务器的配置都在四个端口上提供同样的路由：8080 是 HTTP/1.1，由 wrk 压；
8081 是 h2c（预先知道的明文 HTTP/2），由 h2load 压；8445 是 h2（走 TLS 1.3、经 ALPN 协商的 HTTP/2），由 h2load
压；UDP 8444 是 HTTP/3，走 TLS 1.3，由支持 HTTP/3 的 h2load 压。h2 和 HTTP/3 用工具自带的一次性证书，连接数和
并发流数与 h2c 相同。

自己设置 UDP socket 缓冲区的 QUIC 服务器（Caddy 和 Traefik 通过 quic-go 申请约 7 MB）最多只能拿到主机的
`net.core.rmem_max` 和 `wmem_max`。压测机自 2026-10-23 起把两者设为 7500000，写在
`/etc/sysctl.d/90-fitbench-udp-buffers.conf`。

| 场景 | 路径 | 响应 |
| --- | --- | --- |
| `fixed` | `/fixed` | 200，`text/plain`，`hello, world` |
| `static` | `/static/small.html` | [`www/static/small.html`](www/static/small.html)，1 KiB |
| `proxy` | `/proxy` | 经 HTTP/1.1 长连接代理到后端；`hello, world` |
| `cache-hot` | `/cache/hot` | 经服务器的缓存到缓存后端：同一个路径，反复请求；4 KiB |
| `cache-zipf` | `/cache/1` 到 `/cache/100000` | 同上，100,000 个路径，第 n 热门的路径被请求的频率为 1/n（齐夫定律，见 [`loadgen/zipf.lua`](loadgen/zipf.lua)）；只用 HTTP/1.1 |

缓存场景只有点名时才运行（`-scenarios cache-hot,cache-zipf`）。每个服务器保存 64 MiB 的响应：nginx 的
`proxy_cache` 在磁盘上（因而在页缓存中），并开启锁；fit 的 `cache` 在内存中。100,000 个路径的 4 KiB 响应约
430 MB，因此齐夫场景既测速度，也测各自保住了多少热门路径。其结果另外记录本轮中到达缓存后端的请求数（取自
其计数器），以及命中率，即服务器不经后端回答的请求所占比例。只有 Linux 的 host 网络能访问到后端的计数器。

每个代理都保持最多 4096 个到后端的空闲连接，多于压测客户端同时在途的请求数。用默认值（Caddy 为 32，
Traefik 为 200）时，它们在压力下不断关闭、重开后端连接直到端口耗尽，测到的是默认值而不是代理本身。

每轮之前，工具先用该协议请求一次，检查状态码、协议和响应体是否符合预期，配置走样的服务器会报错，
而不是测了别的东西。服务器不支持的场景记为跳过：Traefik 只能代理，Envoy 不提供静态文件，Caddy、Traefik 和 Envoy 在这里没有自带的缓存；fit 在 I12 支持
`static`，在 I15 支持 `proxy`，在 I69 支持缓存场景。

每轮测量记录每秒请求数、延迟（wrk 给出百分位；h2load 不给百分位，只有均值、标准差和最大值）、按
类型分的错误数、服务器用掉的 CPU（取自其 cgroup 的 `cpu.stat`，以核数计），以及结束时的内存
（cgroup 的 `memory.current`，和 `memory.stat` 里的 `anon`）。

每轮都用一个为它新启动的服务器，上一轮留在内存里的东西不会算进下一轮。服务器稳定后读一次匿名内存
（`anon_start_bytes`），预热和测量期间每 100ms 读一次并保留最大值（`anon_peak_bytes`），都从主机上的 cgroup
文件读取，不占服务器的资源。由此算出每轮的能效：

| 字段 | |
| --- | --- |
| `cpu_us_per_request` | 每个请求花掉服务器的 CPU 时间，单位 µs |
| `requests_per_sec_per_mib` | 每 MiB 峰值匿名内存对应的每秒请求数 |
| `bytes_per_in_flight` | 峰值减去起始匿名内存，再除以在途请求数（`in_flight`：HTTP/1.1 为 256 个连接，h2c、h2 和 HTTP/3 为 64 个连接各 32 个流） |

fit 的协程数取自其 Admin API 的 `go_goroutines`，压测配置让它监听 127.0.0.1:9005（只有主机网络下工具才
连得到）：就绪一秒后读一次（`startup_goroutines`），每次预热和测量期间（每轮都用新启动的服务器）每 100ms 读一次并保留最大值
（`goroutines_max`），空闲连接打开前和保持时各读一次（`goroutines_before`、`goroutines_after`）。计数包括
Admin API 自己的几个协程；短于 100ms 的峰值可能落在两次采样之间。

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

[`results`](results) 存放提交进仓库的结果。`baseline-2026-10-19-macos-docker.json` 是 M0 为 nginx、Caddy 和
Traefik 测得的基线，在 Docker Desktop 上测得，只作为记录保留（见上文）；以 M1 起在 Linux 上的结果为准，即 `m1-*`、
`m2-*` 及其报告。要复现某个结果，在压测机上用其中记录的 `params` 运行：

```bash
go run ./bench/cmd/fitbench -servers nginx,caddy,traefik,envoy,fit -out bench/results/baseline-$(date +%Y%m%d).json
```

报告里的表格由 `benchreport` 从结果 JSON 生成：每个场景和协议一张表，每个服务器一行，按每秒请求数（TPS）、
CPU 能效（每 CPU 秒的请求数）、内存能效（每 MiB 的每秒请求数）依次排序，每个参与排序的值后面跟着它占该列最佳值的
百分比；另有一张每个空闲连接内存的表，最低的排前。`-prev` 为每一行加上相对之前某次运行的吞吐变化：

```bash
go run ./bench/cmd/benchreport -in bench/results/run.json -prev bench/results/earlier.json -prev-name M5
```

结果 JSON 包含 `host`（CPU、内核、内存）、`versions` 和 `images`（带摘要）、`params`，`results`
里每个服务器、场景、协议一项，`idle` 里每个服务器、连接类型一项。
