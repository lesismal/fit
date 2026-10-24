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
  （`-scenarios`、`-protocols`），预热 2 秒后每次跑 10 秒，A、B 交替 3 轮；`-extra` 追加到 fit 的配置
  `bench/configs/fit/fit.konf`（访问日志、指标等）。它在压测机上启动后返回一个 id；`bench/ab.sh -report ID` 取回已完成的结果，汇总每秒请求数和每请求
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
- **代理后端**：nginx，对 API、大文件下载和上传之外的所有请求返回固定响应，端口 9000
  （[`configs/backends/proxy.conf`](configs/backends/proxy.conf)）；
- **缓存源站**：nginx 对 `/cache/` 下的每个路径都回答 4 KiB，对 `/edge/` 下的路径回答网站的文件，新鲜期 10 分钟
  （`Cache-Control: max-age=600`），端口 9003，并在 127.0.0.1:9004 用 `stub_status` 计数它回答的请求
  （[`configs/backends/origin.conf`](configs/backends/origin.conf)）；
- **WebSocket 后端**：`idleconn -serve-ws`，接受升级并保持连接，端口 9001。

有 N 个 CPU 时，后端分八分之一（至少一个），其余的服务器和压测客户端各一半。nginx 每个服务器 CPU
一个 worker；Go 写的服务器通过 CPU 亲和性看到分给它的 CPU。

## 场景

[`configs`](configs) 里每个服务器的配置都在五个端口上提供同样的路由：8080 是 HTTP/1.1，由 wrk 压；8443 是 TLS 1.3
上的 HTTP/1.1（`https/1.1`），由 wrk 压；8081 是 h2c（预先知道的明文 HTTP/2），由 h2load 压；8445 是 h2（走 TLS
1.3、经 ALPN 协商的 HTTP/2），由 h2load 压；UDP 8444 是 HTTP/3，走 TLS 1.3，由支持 HTTP/3 的 h2load 压。fit 的配置
用它自己的 `.konf` 格式。h2 和 HTTP/3 用工具自带的一次性证书，连接数和
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
其 `stub_status`），以及命中率，即服务器不经源站回答的请求所占比例。只有 Linux 的 host 网络能访问到源站的计数器。

后端都是 nginx，配置在单独的 [`configs/backends`](configs/backends) 目录：它不是被测服务器之一，在自己的 CPU 上（压测方案中为 6 个）
足够快，且对每个服务器都相同，代理的结果反映的是代理本身，而不是它的后端。

各服务器共有的设置都已对齐，见[对齐的设置](#对齐的设置)，没有服务器被其他服务器没有的自身配置限制拖累。

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
| `bytes_per_in_flight` | 峰值减去起始匿名内存，再除以在途请求数（`in_flight`：HTTP/1.1 为 256 个连接，或 wrk 实际打开的其线程数的倍数，h2c、h2 和 HTTP/3 为 64 个连接各 32 个流） |

fit 的协程数取自其 Admin API 的 `go_goroutines`，压测配置让它监听 127.0.0.1:9005（只有主机网络下工具才
连得到）：就绪一秒后读一次（`startup_goroutines`），每次预热和测量期间（每轮都用新启动的服务器）每 100ms 读一次并保留最大值
（`goroutines_max`），空闲连接打开前和保持时各读一次（`goroutines_before`、`goroutines_after`）。计数包括
Admin API 自己的几个协程；短于 100ms 的峰值可能落在两次采样之间。

## 工作负载

[压测方案](#压测方案)中的场景模拟代理的真实用途；负载和请求头在
[`cmd/fitbench/workloads.go`](cmd/fitbench/workloads.go)，wrk 的脚本在 [`loadgen`](loadgen)。

| 场景 | 路径 | 请求内容 |
| --- | --- | --- |
| `api` | `/api/item` | API 网关的流量，经代理：每 5 个请求中 4 个是 GET，返回 1 KiB JSON，第 5 个是 2 KiB 的 POST，返回 256 字节，都带 API 客户端的请求头（约 700 字节：bearer token、cookie）。wrk 用 [`api.lua`](loadgen/api.lua) 混合；h2load 只能发一种请求，分两组运行，五分之四的连接发 GET，其余发 POST |
| `site` | `/site/...` | 网站的 10,000 个文件，由 fitbench 每次以相同方式生成：十分之一是约 30 KiB 的页面，五分之一是 10 到 100 KiB 的样式表和脚本，其余是 5 到 300 KiB 的图片，共约 600 MB，第 n 热门的被请求的频率为 1/n，带浏览器的请求头；只走 HTTPS（TLS 上的 HTTP/1.1、h2、HTTP/3） |
| `edge` | `/edge/...` | 同样的文件经服务器的缓存，从缓存后端取，后端给出 10 分钟的有效期；附命中率 |
| `churn-1`、`churn-10` | `/fixed` | 每个请求或每 10 个请求后关闭连接，每次都有自己的 TLS 握手（wrk 不复用会话）；TLS 上的 HTTP/1.1 |
| `download` | `/site/large.bin` | 1 MiB 的文件 |
| `proxy-download` | `/proxy/large` | 后端的 1 MiB，经代理 |
| `proxy-upload` | `/proxy/upload` | 1 MiB 的 POST，经代理，返回 256 字节 |

`-rate N` 让每轮保持每秒 N 个请求，所有协议都用 h2load（它也支持 HTTP/1.1），并从它的逐请求日志读出延迟分位数；
benchreport 此时按 p99 排名。`-hold-idle N` 在每轮期间向服务器保持 N 个空闲 TLS 连接，并记录服务器关闭了多少。

## 对齐的设置

各服务器的压测配置把它们共有的设置设成同样的值，使方案的最大负载（10,000 个请求在途，4 个 worker）之内，没有服务器被自身配置的
限制拖累：

| 设置 | 值 | 方式 |
| --- | --- | --- |
| 后端连接数 | 最多 16,384 | nginx 带共享 `zone` 的 `max_conns`、Caddy `max_conns_per_host`、Envoy 熔断器、fit `max_conns`；Traefik 没有此设置 |
| 等待后端连接的请求 | 最多 16,384 | fit `max_pending`、Envoy `max_pending_requests`；nginx 不能排队，Caddy 和 Traefik 排队不设上限 |
| 保持的空闲后端连接 | 共 16,384，空闲 60 秒后关闭 | nginx 每个 worker 4,096（共 4 个）和 `keepalive_timeout 60s`；Caddy `keepalive_idle_conns` 和 `keepalive 60s`（默认 2 分钟）；Traefik `maxIdleConnsPerHost` 和 `idleConnTimeout 60s`（默认 90 秒）；Envoy 不设上限，`idle_timeout 60s`（默认 1 小时）；fit `max_idle_conns`、`idle_timeout` |
| 被动健康检查 | 无：一次失败不会让唯一的后端退出轮转 | nginx `max_fails=0`、fit `max_fails 0`（两者默认都是 1）；Caddy、Traefik、Envoy 默认没有 |
| 发给后端的请求头 | 客户端的 `Host`，以及 `X-Forwarded-For`、`-Proto`、`-Host` | nginx `proxy_set_header`（默认把上游名作为 `Host`，且都不发）；Envoy `use_remote_address`，且不为每个请求生成 `x-request-id`（`generate_request_id: false`）；Caddy、Traefik、fit 默认如此，fit 还多发 `Forwarded` |
| 监听队列 | 2,048，即压测机的 `somaxconn` | nginx `backlog=2048`（默认 511）、Envoy `tcp_backlog_size`（默认 128）、fit `engine.backlog`；Go 服务器用 `somaxconn` |
| 请求体大小 | 16 MiB | nginx `client_max_body_size`（默认 1 MiB）、Caddy `request_body`、fit 的默认值；Traefik 和 Envoy 流式转发，不设限 |
| 代理的消息体 | 流式，或在内存中缓冲 | nginx 在内存中缓冲响应，但不写临时文件，上传流式转发；其余流式 |
| 等待后端 | 连接 5 秒，响应 60 秒 | nginx `proxy_connect_timeout`、`proxy_read_timeout`；Caddy `dial_timeout`、`response_header_timeout`；Traefik `forwardingTimeouts`；Envoy `connect_timeout` 和路由超时（默认 15 秒）；fit `connect_timeout` 和代理的 `timeout` |
| 每个客户端连接的请求数 | 不限 | nginx `keepalive_requests` 1,000,000,000（默认 1,000，HTTP/2 和 HTTP/3 也适用）；其余的没有此限制 |
| 空闲客户端连接 | 120 秒后关闭 | nginx `keepalive_timeout`（默认 65 秒）、Caddy `timeouts idle`、Traefik `idleTimeout`、Envoy `idle_timeout`；fit 用 fib 的默认值，不能设置 |
| 每连接的 HTTP/2、HTTP/3 流 | 250 | Go 服务器和 fib 的默认值；nginx 和 Envoy 设成它 |
| HTTP/2 流接收窗口 | 1 MiB | nginx `http2_body_preread_size`（默认 64 KiB）、Envoy（默认 256 MiB）；其余已是 |
| HTTP/2 连接接收窗口 | 能设的设为 16 MiB | Envoy、fit；Caddy 和 Traefik 用 Go 的 1 MiB，可能拖慢它们在 h2 上的大上传 |
| 工作线程 | 4，每 CPU 一个 | nginx `worker_processes`、Envoy `--cpuset-threads`，Go 服务器按绑定的 CPU 数 |
| 缓存 | 1 GiB，单条最多 2 MiB | nginx `proxy_cache_path`、fit 的 `cache` |
| 静态文件 | 对路径所知的信息信任 1 秒 | fit 的静态处理器（`check_interval`），它还把 1 MiB 以内的文件放在内存里，最多 64 MiB；nginx `open_file_cache` 缓存 10,000 个路径，`open_file_cache_valid 1s`，内容经 `sendfile` 来自页缓存；Caddy 每次经页缓存读文件 |

每个 TLS 轮次都记录 curl 和服务器协商的结果（`tls`：版本、加密套件、密钥交换），与同容器的压测客户端协商一致。
压测客户端和 curl 优先 TLS 1.3 的 AES-128-GCM，只提供 X25519（fitbench 的 OpenSSL 配置，`OPENSSL_CONF`）：nginx 和
Envoy 按客户端的顺序选择，OpenSSL 默认把 AES-256-GCM 排在前面；Go 服务器和 fit 自行选择 AES-128-GCM，因此所有服务器
都协商为 AES-128-GCM 和 X25519。

后端则按自身性能调优：6 个 CPU 上各绑一个 worker，连接保持 300 秒，缓存文件元数据，不生成 ETag。

都不写访问日志，也都不压缩，无论是实时压缩还是预压缩文件：nginx 官方镜像只能提供 gzip 预压缩文件，其他服务器
还能提供 brotli 和 zstd，因此都不启用。工作负载仍发送浏览器和 API 客户端的 `Accept-Encoding`，所有服务器都忽略它。

## 压测方案

[`plan.sh`](plan.sh) 在压测机上跑方案，规模按压测机的承载能力确定：16 个线程按物理核划分（CPU n 和 n+8 是核 n 的两个
线程），各角色不共用核：被测服务器用核 0-1（CPU `0-1,8-9`，4 个 worker），后端用核 2-4（`2-4,10-12`），压测客户端用
核 5-7（`5-7,13-15`）。后端与压测客户端一样多，以免由后端决定速度：每个结果都记录后端的 CPU（`backend_cpu_cores`、
`origin_cpu_cores`），都远低于它们的 6 个。内核在所有 socket 的 TCP 缓冲超过 1 GiB 时开始限制，每轮在途的字节数都远低于
此。每轮预热 5 秒、测 15 秒；232 轮约需 1 小时 50 分钟。

| 工作负载 | 场景 | 协议 | 在途请求 | 服务器 | 轮数 |
| --- | --- | --- | --- | --- | ---: |
| W0，原始上限 | `fixed` | HTTP/1.1、h2c、h2、HTTP/3 | 100、500、2,000、5,000、10,000 | nginx、Caddy、Envoy、fit | 80 |
| W1，API 网关 | `api` | HTTP/1.1、h2 | 100、500、2,000、5,000、10,000 | 全部五个 | 50 |
| W2，网站 | `site`、`edge` | TLS 上的 HTTP/1.1、h2、HTTP/3 | 500、2,000 | nginx、Caddy、fit（`edge`：nginx、fit） | 30 |
| W3，连接频繁新建 | `churn-1`、`churn-10` | TLS 上的 HTTP/1.1 | 100、500 个连接 | nginx、Caddy、Envoy、fit | 16 |
| W4，大文件传输 | `download`、`proxy-download`、`proxy-upload` | HTTP/1.1、h2 | 100 | 所有支持的 | 26 |
| W5，空闲加活跃 | `api`，并保持 40,000 个空闲 TLS 连接 | HTTP/1.1、h2 | 500 | 全部五个 | 10 |
| W6，固定速率下的延迟 | `api`，每秒 2 万和 10 万个请求 | HTTP/1.1、h2 | 500 个连接 | 全部五个 | 20 |

HTTP/1.1 每个连接一个请求在途，wrk 给它的 6 个线程各 `connections / threads` 个连接，所以每档是略低于它的 6 的倍数：
96、498、1,998、4,998 和 9,996，每个结果的 `in_flight` 都有记录。h2c、h2 和 HTTP/3 的档位是 h2load 的连接数乘以流数：
100 为 10 × 10，其后每个连接 25 个流（500 为 20 × 25，2,000 为 80 × 25，5,000 为 200 × 25，10,000 为 400 × 25），
每个连接数都不少于压测客户端的 6 个线程。

W6 的速率按各服务器在 4 个 CPU、500 个在途时的上限确定：方案开跑前的试跑中约为每秒 4 万到 20 万（Caddy 的 h2 最低，
nginx 的 HTTP/1.1 最高）。2 万时每个服务器都低于上限，延迟是它自己的而不是排队造成的；10 万时只有 nginx 和 fit
跟得上，其余的延迟显示它们落后的程度。那次试跑中后端（6 个 CPU 上的 nginx）最多用了其中 2 个。

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

吞吐只计成功的响应：压测客户端判为错误的（5xx、超时）不计入，Errors 列给出其数量和比例。没有结果的轮次会说明原因：
**hung**（压测客户端没有结束）、**did not start**（服务器没能启动）或 **no responses**（没有任何应答）。`-sweep` 接受
按连接数从低到高跑出的多次结果（`-connections`、`-h2-connections`），为每个场景和协议生成一张表，每个服务器一行、每次
运行一列，按服务器在任一次达到的最大值排序：

```bash
go run ./bench/cmd/benchreport -sweep bench/results/sweep-l1.json,bench/results/sweep-l2.json
```

[`sweep.sh`](sweep.sh) 在压测机上跑完整的连接数扫描，七档，从 16 个 HTTP/1.1 连接（8×32 个流）到 32,768
个（8,192×32），与 [2026-10-23 的扫描](https://github.com/lesismal/fit/blob/3658ae9/bench/results/sweep-report-2026-10-23.zh-CN.md)相同；proxy 只跑第 1-5 档，
因为超过之后测到的是压测客户端和代理后端，而不是代理本身。

结果 JSON 包含 `host`（CPU、内核、内存）、`versions` 和 `images`（带摘要）、`params`，`results`
里每个服务器、场景、协议一项，`idle` 里每个服务器、连接类型一项。
