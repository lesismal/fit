# 连接数扫描，2026-10-23

[English](sweep-report-2026-10-23.md) | [简体中文](sweep-report-2026-10-23.zh-CN.md)

在压测机上，让每个服务器在 7 档连接数（从 16 到 32,768）下跑完全部场景和协议。fit 为 e079c83（fib b0809b4，含 A42
和 A43），对比 nginx、Caddy、Traefik 和 Envoy，压测工具和参数与 v1.0.0 报告相同。

| 档位 | HTTP/1.1 连接数（wrk） | h2c、h2、HTTP/3 连接数 × 流数（h2load） | h2c/h2/HTTP/3 在途请求数 |
| --- | ---: | ---: | ---: |
| 1 | 16 | 8 × 32 | 256 |
| 2 | 64 | 16 × 32 | 512 |
| 3 | 256 | 64 × 32 | 2,048（压测工具的默认值） |
| 4 | 1,024 | 256 × 32 | 8,192 |
| 5 | 4,096 | 1,024 × 32 | 32,768 |
| 6 | 16,384 | 4,096 × 32 | 131,072 |
| 7 | 32,768 | 8,192 × 32 | 262,144 |

概要：

- **fixed、static 和缓存场景：fit 在每一档都领先**，只有 fixed 的 HTTP/1.1 和 HTTP/3 是 nginx 领先：HTTP/1.1 从 64
  个连接起领先 1–4%（16 个连接时领先 14%），HTTP/3 从 64×32 起领先（8×32 和 16×32 时 fit 领先）。在 h2c 和 h2 上，
  fit 每一档都是第二名的 2.6 到 7 倍。
- **每个服务器都在第 3 到第 5 档达到峰值，第 6、7 档下降。** fit 的 h2c 和 h2 在 256×32 到 1,024×32 达到峰值（fixed
  分别为 393 万和 366 万），到 8,192×32 下降 25–30%；它的 HTTP/1.1 在 4,096 个连接以内保持 77 万，到 32,768 个连接降到
  46 万，nginx 也是如此。nginx 在 h2c 和 h2 上保持得更好（升到 66 万和 101 万）；Envoy 损失了自身峰值的三分之一。
- **fit 的代理在在途请求超过 5,120 后崩溃，超过 131,072 后完全停滞。** 压测配置把它限在这里（到后端的 `max_conns`
  为 4,096，加上默认的 `max_pending` 1,024），超出的请求按设计回 503；但此时它的成功响应远低于后端的能力（h2c 在
  8,192 在途时每秒 7.7 万，nginx 为 20.6 万），而在第 6、7 档的 h2c 和 h2 上，30 秒内一个请求都没有应答，CPU 几乎空闲
  （h2 在 1,024×32 时就已如此）。HTTP/1.1 在超过 4,096 个连接后出现同样情况。
- **第 6、7 档的 h2c 和 h2 上，所有代理都失败了**：nginx、Caddy、Traefik 和 Envoy 每秒只应答几百到几千个请求，而且几乎
  全是 5xx，或者让压测客户端卡死（共有 8 轮被判为卡死）。HTTP/1.1 上，nginx 在 32,768 个连接时仍有 20.1 万，Envoy 10.9 万。
- **压测时的内存**：第 7 档 fixed 场景下，fit 的 HTTP/1.1 峰值为 83 MiB，多路复用协议为 1.0–1.4 GiB；nginx 保持在
  214–599 MiB，但 HTTP/3 达到 7.1 GiB；Envoy 的 HTTP/3 达到 14.1 GiB，Traefik 的 h2c 代理达到 12.4 GiB。
- **fit 的协程数**随在途流数增长，而不是随连接数：fixed 场景下 HTTP/1.1 每一档都在 110–350；h2c 从 8×32 时的 590 增长到
  8,192×32 时的 20,635。
- **关于 HTTP/3 的一点保留**：压测结束后发现，为 QUIC 服务器调高的 UDP 缓冲上限又被改回了 16,384；第 4 档起 Caddy 和
  Traefik 的 HTTP/3 结果可能是在旧上限下跑的（详见[测试环境](#测试环境)）。

结果：每档一个 JSON，`sweep-2026-10-23-l<N>-c<C>-h<H>.json`（档位、HTTP/1.1 连接数、其他协议的连接数），从
[`sweep-2026-10-23-l1-c16-h8.json`](sweep-2026-10-23-l1-c16-h8.json) 到
[`sweep-2026-10-23-l7-c32768-h8192.json`](sweep-2026-10-23-l7-c32768-h8192.json)。每一档的完整表格见
[附录](sweep-2026-10-23-levels.md)（英文）。

## 测试环境

### 硬件和系统

| | |
| --- | --- |
| CPU | AMD Ryzen 7 7800X3D：8 核 16 线程（SMT），96 MiB L3，最高 5.05 GHz；调频策略 `powersave`（内核在 `amd-pstate` 下的默认值） |
| 内存 | 62 GiB；透明大页 `madvise` |
| 操作系统 | Ubuntu 24.04.5 LTS，Linux 7.0.0-38-generic，按发行版默认参数启动（没有关闭任何漏洞缓解） |
| Docker | 29.1.3，cgroup v2（systemd 驱动），overlayfs；每个容器都使用主机网络（`--network host`），`--ulimit nofile=1048576:1048576` |
| 其他容器 | 一个空闲的 etcd（`quay.io/coreos/etcd:v3.6.5`）和 `fitconf` kind 节点（`kindest/node:v1.34.0`，约占一个核的 5%）；没有其他负载 |

**CPU 的划分。** 每个容器用 `--cpuset-cpus` 绑核。Linux 把这颗 CPU 每个核的第一个线程编为 0–7，SMT 兄弟线程编为 8–15，因此：

| 角色 | 逻辑 CPU | 物理核 |
| --- | --- | --- |
| 被测服务器 | 0–6 | 核 0–6 的第一个线程 |
| 压测客户端（wrk、h2load） | 7–13 | 核 7，以及核 0–5 的第二个线程 |
| 代理、WebSocket 和缓存后端 | 14–15 | 核 6 和核 7 的第二个线程 |

被测服务器有 6 个物理核与压测客户端的线程共享，1 个与后端共享。每个服务器都在同样的划分下运行（M1 以来的报告都是如此），所以
对比是公平的；但绝对数值低于各自独占物理核时的结果。

### 内核参数

| 参数 | 值 | |
| --- | ---: | --- |
| `net.core.somaxconn`、`net.ipv4.tcp_max_syn_backlog` | 2,048 | 限制了每个服务器的 accept 队列；未调高 |
| `net.ipv4.tcp_syncookies` | 1 | SYN 队列满了以后连接仍能建立 |
| `net.ipv4.tcp_tw_reuse` | 1 | |
| `net.ipv4.ip_local_port_range` | 1024–65535 | |
| `net.ipv4.ip_local_reserved_ports` | 8080-8081、8443-8445、9000-9005 | 从第 6 档起生效，写在 `/etc/sysctl.d/91-fitbench-reserved-ports.conf`（见下文） |
| `net.core.rmem_max`、`net.core.wmem_max` | 7,500,000 | 写在 `/etc/sysctl.d/90-fitbench-udp-buffers.conf`；见下面的说明 |
| `net.core.netdev_max_backlog` | 1,000 | |
| `net.netfilter.nf_conntrack_max` | 2,000,500 | |
| `fs.nr_open`、`fs.file-max` | 2,000,500 | |

**UDP 缓冲上限。** 2026-10-23 为 QUIC 服务器设为 7,500,000（Caddy 和 Traefik 使用的 quic-go 会申请约 7 MB）。2026-10-24
压测结束后再查看时，它又变回了 16,384：设置它的文件还在，主机也没有重启，说明主机上有东西在运行时把它改回去，和 2026-10-23
之前一样。已按文件重新设置。第 1–3 档是在调高后的上限下跑的：这几档 Caddy 和 Traefik 的 HTTP/3 最大延迟（10–128 ms）
与调高后的情况一致，而不是旧上限下的数秒。从第 4 档起，所有服务器的最大延迟都涨到数秒（包括 nginx），所以无法从结果判断
上限是何时被改回的；第 4 档起 Caddy 和 Traefik 的 HTTP/3 结果可能是在旧上限下跑的。

### 软件

| | 版本 | 镜像 |
| --- | --- | --- |
| nginx | 1.31.4 | `nginx:1.31.4@sha256:b34848eff6db…` |
| Caddy | 2.10.2 | `caddy:2.10.2@sha256:c3d7ee5d2b11…` |
| Traefik | 3.5.0 | `traefik:v3.5.0@sha256:4e7175cfe19b…` |
| Envoy | 1.40.0（BoringSSL） | `envoyproxy/envoy:v1.40.0@sha256:459f2a6404c3…` |
| fit | e079c83，fib b0809b4（`v1.6.1-0.20261023105547-b0809b4bac8f`），Go 1.27.3 | 为 linux/amd64 构建，运行在 `ubuntu:24.04@sha256:68ee22004a91…` 上 |
| wrk | 4.1.0（Debian 构建） | `fitbench-loadgen`，Ubuntu 24.04（[`loadgen/Dockerfile`](../loadgen/Dockerfile)） |
| h2load | 1.59.0，用于 h2c 和 h2 | 同上 |
| 支持 HTTP/3 的 h2load | 1.68.0，基于 ngtcp2 1.16.0、nghttp3 1.12.0、OpenSSL 3.5 | `fitbench-loadgen-h3`，Debian trixie（[`loadgen/Dockerfile.h3`](../loadgen/Dockerfile.h3)） |

镜像的完整摘要见每档 JSON 的 `images` 字段。

### 每一轮怎么跑

每一档是压测工具 `fitbench`（仓库 e079c83）的一次运行：

```bash
fitbench -scenarios fixed,static,proxy,cache-hot,cache-zipf -idle "" \
  -connections <HTTP/1.1 连接数> -h2-connections <连接数> -h2-streams 32
```

其余用默认值：预热 5 秒后测 30 秒，主机网络，上面的 CPU 划分。对每个服务器、场景和协议，它会：

1. 在容器里重新启动服务器，等它能应答，再稳定 1 秒；
2. 用 curl 按该协议请求一次，检查状态码、协议和响应体，配置走样的服务器会直接报错，而不是测了别的东西；
3. 先不计入地压 5 秒，再计入地压 30 秒：
   - HTTP/1.1：`wrk -t 7 -c <连接数> -d 30s --timeout 10s --latency -s report.lua <url>`；
   - h2c 和 h2：`h2load -t 7 -c <连接数> -m 32 -D 30 <url>`（h2 用 `https://`，经 ALPN 协商）；
   - HTTP/3：同上，加 `--alpn-list=h3`；
4. 从服务器 cgroup 的 `cpu.stat` 读取计入的 30 秒内的 CPU 时间；从 `memory.stat` 读取匿名内存，稳定后读一次，压测期间每
   100 ms 读一次。都在主机上读取，不占服务器的资源。

**运行过程中发生的事。**

- **卡死的轮次。** 压测工具等待每个压测客户端时没有超时。第 6 档 Traefik 的 h2 代理让 h2load 卡了 1 小时 40 分钟；此后加了
  看门狗，任何 wrk 或 h2load 运行超过 300 秒就被结束，这一轮记为卡死。共 8 轮卡死，都是第 6、7 档 h2c 或 h2 上的 proxy 或
  static（见[下文](#失败)），没有重跑。
- **没能启动的服务器。** 压测机的临时端口范围覆盖了服务器自己的端口：连接数高时，某个出站连接可能占用其中一个，下一个服务器
  就绑定不了。第 4、5 档 Traefik 的 proxy 没能启动（端口 8444），第 6 档 fit 的每一轮都没能启动（Admin API 的
  127.0.0.1:9005）。经用户同意，压测机现在保留这些端口，在第 6 档期间生效；第 7 档没有再出现。这些服务器在整组跑完后按原档位
  重跑（18:18–18:34 UTC），结果替换了该档 JSON 里失败的行（其 `reruns` 字段注明了哪些）。
- **时间。** 2026-10-23 11:35 至 18:34 UTC，约 7 小时。

## 每个测试项测的是什么

每个服务器在同样的端口上提供同样的路由，所以每个测试项向每个服务器要的是同样的东西：

| 测试项 | 请求 | 服务器做什么 | 响应 | 测的是什么 |
| --- | --- | --- | --- | --- |
| **fixed** | `GET /fixed` | 直接按配置应答，不读文件、不连后端、不走缓存 | 200，`text/plain`，`hello, world`（12 字节） | 服务器自身的请求处理路径：读取和解析请求、路由、写响应。即服务器能力的上限 |
| **static** | `GET /static/small.html` | 从磁盘读文件返回（首个请求后在页缓存里） | 200，一个 1 KiB 的 HTML 文件 | 静态文件处理：查找和打开文件、生成响应头、发送文件内容 |
| **proxy** | `GET /proxy` | 通过 HTTP/1.1 长连接把请求转发给代理后端（跑在 CPU 14–15 上、应答 `hello, world` 的 fit），再把应答转回 | 200，`hello, world` | 反向代理：转发、后端连接池、回传响应。后端每秒约能应答 26 万个请求，这也是这里所有代理能达到的上限 |
| **cache-hot** | `GET /cache/hot` | 从自己的 HTTP 缓存应答；缓存只向缓存后端取过一次（4 KiB，`Cache-Control: max-age=600`） | 200，4 KiB | 缓存的命中路径：同一个热门响应被反复请求 |
| **cache-zipf** | `GET /cache/1` … `/cache/100000`，第 n 热门的路径被请求的频率与 1/n 成正比（齐普夫定律，[`zipf.lua`](../loadgen/zipf.lua)） | 缓存里有的直接应答，没有的去后端取 | 200，4 KiB | 路径分布接近真实情况时的缓存：查找、能保留多少工作集（命中率）、内存。仅 HTTP/1.1 |

并不是每个服务器都能跑每个测试项：Envoy 不提供静态文件，Caddy、Traefik 和 Envoy 在这里没有自带的缓存，Traefik 只能做代理。

| | fixed | static | proxy | cache-hot | cache-zipf |
| --- | :---: | :---: | :---: | :---: | :---: |
| nginx | ✓ | ✓ | ✓ | ✓ | ✓ |
| Caddy | ✓ | ✓ | ✓ | | |
| Traefik | | | ✓ | | |
| Envoy | ✓ | | ✓ | | |
| fit | ✓ | ✓ | ✓ | ✓ | ✓ |

每个测试项都在四种协议上跑，各用自己的端口：

| 协议 | 端口 | | 压测客户端 |
| --- | --- | --- | --- |
| HTTP/1.1 | TCP 8080 | 明文 HTTP/1.1 长连接；每个连接同时只有一个请求 | wrk |
| h2c | TCP 8081 | 明文 HTTP/2，预先知道（prior knowledge）；每个连接 32 个流 | h2load |
| h2 | TCP 8445 | TLS 1.3 上的 HTTP/2，经 ALPN 协商；每个连接 32 个流 | h2load |
| HTTP/3 | UDP 8444 | QUIC 上的 HTTP/3，TLS 1.3；每个连接 32 个流 | 支持 HTTP/3 的 h2load |

h2 和 HTTP/3 使用压测工具生成的一次性自签名 ECDSA P-256 证书。

## 扩展性

每个场景和协议一张表，每个服务器一行，每一档一列。单元格是该服务器在该档的**成功**响应数（每秒，单位千），以及占该档
最佳值的百分比：压测客户端判为错误的响应（5xx、超时）不计入。各行按服务器在任一档达到的最大值（Peak TPS）排序，其次按
该档的 CPU 能效（每 CPU 秒的成功响应数）。**hung**：压测客户端在 300 秒后被结束；**0**：跑完了但没有任何应答；—：该
服务器不支持此场景。

### fixed, HTTP/1.1

| Server | Peak TPS [↓1] | CPU EER at peak [↓2] |        16 |        64 |       256 |     1,024 |     4,096 |    16,384 |    32,768 |
| ------ | ------------: | -------------------: | --------: | --------: | --------: | --------: | --------: | --------: | --------: |
| nginx  |  795,372 100% |         125,387 100% | 562k 100% | 785k 100% | 795k 100% | 792k 100% | 785k 100% | 669k 100% | 465k 100% |
| fit    |  770,809  96% |         125,022  99% | 483k  85% | 752k  95% | 765k  96% | 767k  96% | 771k  98% | 661k  98% | 458k  98% |
| envoy  |  410,423  51% |          58,850  46% | 224k  39% | 369k  47% | 390k  48% | 410k  51% | 365k  46% | 237k  35% | 245k  52% |
| caddy  |  367,082  46% |          54,597  43% | 308k  54% | 333k  42% | 343k  43% | 367k  46% | 351k  44% | 301k  44% | 294k  63% |

### fixed, h2c

| Server |  Peak TPS [↓1] | CPU EER at peak [↓2] |        8×32 |       16×32 |       64×32 |      256×32 |    1,024×32 |    4,096×32 |    8,192×32 |
| ------ | -------------: | -------------------: | ----------: | ----------: | ----------: | ----------: | ----------: | ----------: | ----------: |
| fit    | 3,931,497 100% |         564,181 100% | 1,993k 100% | 2,941k 100% | 3,809k 100% | 3,881k 100% | 3,931k 100% | 3,068k 100% | 2,884k 100% |
| envoy  |   897,971  22% |         128,605  22% |   689k  34% |   828k  28% |   898k  23% |   873k  22% |   668k  16% |   611k  19% |   601k  20% |
| nginx  |   659,968  16% |          94,816  16% |   267k  13% |   430k  14% |   624k  16% |   633k  16% |   628k  15% |   646k  21% |   660k  22% |
| caddy  |   226,358   5% |          33,825   5% |   226k  11% |   213k   7% |   220k   5% |   223k   5% |   212k   5% |   201k   6% |   189k   6% |

### fixed, h2

| Server |  Peak TPS [↓1] | CPU EER at peak [↓2] |        8×32 |       16×32 |       64×32 |      256×32 |    1,024×32 |    4,096×32 |    8,192×32 |
| ------ | -------------: | -------------------: | ----------: | ----------: | ----------: | ----------: | ----------: | ----------: | ----------: |
| fit    | 3,662,259 100% |         526,311 100% | 1,793k 100% | 2,715k 100% | 3,533k 100% | 3,532k 100% | 3,662k 100% | 2,871k 100% | 2,657k 100% |
| nginx  | 1,010,984  27% |         147,018  27% |   267k  14% |   457k  16% |   925k  26% |   931k  26% |   925k  25% |   893k  31% | 1,011k  38% |
| envoy  |   875,861  23% |         125,395  23% |   556k  30% |   727k  26% |   876k  24% |   843k  23% |   643k  17% |   591k  20% |   575k  21% |
| caddy  |   230,570   6% |          33,698   6% |   229k  12% |   216k   7% |   224k   6% |   231k   6% |   215k   5% |   207k   7% |   195k   7% |

### fixed, HTTP/3

| Server |  Peak TPS [↓1] | CPU EER at peak [↓2] |        8×32 |       16×32 |       64×32 |      256×32 |    1,024×32 |    4,096×32 |    8,192×32 |
| ------ | -------------: | -------------------: | ----------: | ----------: | ----------: | ----------: | ----------: | ----------: | ----------: |
| nginx  | 2,278,627 100% |         344,100 100% |   267k  20% |   533k  32% | 2,133k 100% | 2,279k 100% | 1,478k 100% | 1,282k 100% | 1,181k 100% |
| fit    | 1,768,651  77% |         259,932  75% | 1,279k 100% | 1,655k 100% | 1,769k  82% | 1,635k  71% | 1,422k  96% | 1,119k  87% | 1,033k  87% |
| caddy  |   381,836  16% |          55,975  16% |   323k  25% |   347k  20% |   382k  17% |   348k  15% |   353k  23% |   325k  25% |   309k  26% |
| envoy  |   322,457  14% |          46,110  13% |   284k  22% |   314k  18% |   322k  15% |   307k  13% |   303k  20% |   303k  23% |   285k  24% |

### static, HTTP/1.1

| Server | Peak TPS [↓1] | CPU EER at peak [↓2] |        16 |        64 |       256 |     1,024 |     4,096 |    16,384 |    32,768 |
| ------ | ------------: | -------------------: | --------: | --------: | --------: | --------: | --------: | --------: | --------: |
| fit    |  758,011 100% |         109,981 100% | 456k 100% | 737k 100% | 758k 100% | 751k 100% | 725k 100% | 611k 100% | 441k 100% |
| nginx  |  436,996  57% |          62,519  56% | 334k  73% | 437k  59% | 436k  57% | 435k  57% | 433k  59% | 400k  65% | 384k  87% |
| caddy  |  160,532  21% |          23,545  21% | 153k  33% | 161k  21% | 160k  21% | 156k  20% | 146k  20% | 133k  21% | 134k  30% |

### static, h2c

| Server |  Peak TPS [↓1] | CPU EER at peak [↓2] |        8×32 |       16×32 |       64×32 |      256×32 |    1,024×32 |    4,096×32 |    8,192×32 |
| ------ | -------------: | -------------------: | ----------: | ----------: | ----------: | ----------: | ----------: | ----------: | ----------: |
| fit    | 2,493,318 100% |         357,596 100% | 1,533k 100% | 2,005k 100% | 2,444k 100% | 2,493k 100% | 2,381k 100% | 1,924k 100% | 1,788k 100% |
| nginx  |   374,049  15% |          53,637  14% |   219k  14% |   342k  17% |   368k  15% |   365k  14% |   374k  15% |   370k  19% |   374k  20% |
| caddy  |   125,235   5% |          18,427   5% |   123k   8% |   120k   5% |   125k   5% |   125k   5% |   120k   5% |   115k   5% |   109k   6% |

### static, h2

| Server |  Peak TPS [↓1] | CPU EER at peak [↓2] |        8×32 |       16×32 |       64×32 |      256×32 |    1,024×32 |    4,096×32 |    8,192×32 |
| ------ | -------------: | -------------------: | ----------: | ----------: | ----------: | ----------: | ----------: | ----------: | ----------: |
| fit    | 2,122,361 100% |         304,358 100% | 1,288k 100% | 1,715k 100% | 2,085k 100% | 2,122k 100% | 2,001k 100% | 1,677k 100% | 1,560k 100% |
| nginx  |   468,701  22% |          67,248  22% |   206k  15% |   353k  20% |   452k  21% |   457k  21% |   460k  22% |   464k  27% |   469k  30% |
| caddy  |   123,683   5% |          17,998   5% |   122k   9% |   119k   6% |   124k   5% |   123k   5% |   119k   5% |   114k   6% |        hung |

### static, HTTP/3

| Server |  Peak TPS [↓1] | CPU EER at peak [↓2] |      8×32 |       16×32 |       64×32 |    256×32 |  1,024×32 |  4,096×32 |  8,192×32 |
| ------ | -------------: | -------------------: | --------: | ----------: | ----------: | --------: | --------: | --------: | --------: |
| fit    | 1,119,827 100% |         162,795 100% | 873k 100% | 1,102k 100% | 1,120k 100% | 938k 100% | 856k 100% | 782k 100% | 745k 100% |
| nginx  |   767,976  68% |         109,965  67% | 267k  30% |   477k  43% |   768k  68% | 757k  80% | 643k  75% | 589k  75% | 548k  73% |
| caddy  |   160,616  14% |          23,322  14% | 143k  16% |   153k  13% |   161k  14% | 144k  15% | 148k  17% | 143k  18% | 139k  18% |

### proxy, HTTP/1.1

| Server  | Peak TPS [↓1] | CPU EER at peak [↓2] |        16 |        64 |       256 |     1,024 |     4,096 |    16,384 |    32,768 |
| ------- | ------------: | -------------------: | --------: | --------: | --------: | --------: | --------: | --------: | --------: |
| nginx   |  258,527 100% |          54,811 100% | 161k 100% | 255k 100% | 258k 100% | 259k 100% | 246k 100% | 208k 100% | 201k 100% |
| fit     |  246,835  95% |          42,300  77% | 132k  81% | 202k  78% | 243k  94% | 247k  95% | 224k  90% |  76k  36% |  14k   6% |
| envoy   |  156,808  60% |          24,459  44% |  96k  59% | 118k  46% | 138k  53% | 157k  60% | 129k  52% | 107k  51% | 109k  54% |
| traefik |  155,397  60% |          23,840  43% | 121k  74% | 137k  53% | 151k  58% | 155k  60% | 133k  53% |  94k  45% |  41k  20% |
| caddy   |  127,445  49% |          19,174  34% | 105k  65% | 119k  46% | 127k  49% | 126k  48% | 100k  40% |  76k  36% |  36k  17% |

### proxy, h2c

| Server  | Peak TPS [↓1] | CPU EER at peak [↓2] |      8×32 |     16×32 |     64×32 |    256×32 |  1,024×32 | 4,096×32 | 8,192×32 |
| ------- | ------------: | -------------------: | --------: | --------: | --------: | --------: | --------: | -------: | -------: |
| nginx   |  252,889 100% |          46,005 100% | 210k  90% | 253k 100% | 252k 100% | 206k 100% | 201k 100% |        0 |     hung |
| fit     |  237,387  93% |          38,575  83% | 232k 100% | 235k  92% | 237k  94% |  77k  37% |  19k   9% |        0 |        0 |
| envoy   |  233,334  92% |          33,968  73% | 159k  68% | 173k  68% | 233k  92% | 175k  84% | 148k  73% |        0 |        0 |
| traefik |  120,946  47% |          17,555  38% | 121k  52% | 115k  45% | 112k  44% |  98k  47% |  30k  14% |  3k  52% |  4k 100% |
| caddy   |  103,970  41% |          15,412  33% | 104k  44% | 100k  39% |  93k  36% |  82k  39% |  28k  14% |  5k 100% |     hung |

### proxy, h2

| Server  | Peak TPS [↓1] | CPU EER at peak [↓2] |      8×32 |     16×32 |     64×32 |    256×32 |  1,024×32 | 4,096×32 | 8,192×32 |
| ------- | ------------: | -------------------: | --------: | --------: | --------: | --------: | --------: | -------: | -------: |
| nginx   |  280,991 100% |          58,161 100% | 203k  89% | 281k 100% | 242k 100% | 200k 100% | 198k 100% |  1k   8% |     hung |
| fit     |  231,082  82% |          37,258  64% | 227k 100% | 231k  82% | 229k  94% | 177k  88% |   0k   0% |        0 |        0 |
| envoy   |  226,198  80% |          33,438  57% | 160k  70% | 167k  59% | 226k  93% | 172k  86% | 149k  75% |  0k   1% |     hung |
| traefik |  120,673  42% |          17,454  30% | 121k  53% | 114k  40% | 110k  45% |  97k  48% |  33k  16% |     hung |     hung |
| caddy   |  101,823  36% |          15,080  25% | 102k  44% |  97k  34% |  91k  37% |  80k  40% |  29k  14% |  6k 100% |     hung |

### proxy, HTTP/3

| Server  | Peak TPS [↓1] | CPU EER at peak [↓2] |      8×32 |     16×32 |     64×32 |    256×32 |  1,024×32 |  4,096×32 |  8,192×32 |
| ------- | ------------: | -------------------: | --------: | --------: | --------: | --------: | --------: | --------: | --------: |
| nginx   |  266,358 100% |          83,403 100% | 201k  93% | 234k  94% | 266k 100% | 214k 100% | 183k 100% | 117k 100% |  93k  84% |
| fit     |  245,864  92% |          48,129  57% | 216k 100% | 246k 100% | 239k  89% |  89k  41% |  18k   9% |         0 |         0 |
| traefik |  137,760  51% |          19,932  23% | 131k  60% | 138k  56% | 136k  51% | 133k  62% | 123k  67% | 116k  98% | 111k 100% |
| caddy   |  112,820  42% |          16,794  20% | 109k  50% | 113k  45% | 105k  39% |  82k  38% | 102k  55% |  98k  83% |  97k  87% |
| envoy   |   74,919  28% |          10,733  12% |  51k  23% |  55k  22% |  74k  27% |  73k  34% |  75k  40% |  75k  63% |  66k  59% |

### cache-hot, HTTP/1.1

| Server | Peak TPS [↓1] | CPU EER at peak [↓2] |        16 |        64 |       256 |     1,024 |     4,096 |    16,384 |    32,768 |
| ------ | ------------: | -------------------: | --------: | --------: | --------: | --------: | --------: | --------: | --------: |
| fit    |  688,599 100% |          99,758 100% | 421k 100% | 665k 100% | 685k 100% | 689k 100% | 682k 100% | 558k 100% | 419k 100% |
| nginx  |  330,374  47% |          47,321  47% | 258k  61% | 330k  49% | 330k  48% | 327k  47% | 324k  47% | 306k  54% | 298k  71% |

### cache-hot, h2c

| Server |  Peak TPS [↓1] | CPU EER at peak [↓2] |        8×32 |       16×32 |       64×32 |      256×32 |    1,024×32 |    4,096×32 |  8,192×32 |
| ------ | -------------: | -------------------: | ----------: | ----------: | ----------: | ----------: | ----------: | ----------: | --------: |
| fit    | 1,643,692 100% |         236,070 100% | 1,091k 100% | 1,321k 100% | 1,562k 100% | 1,644k 100% | 1,400k 100% | 1,207k 100% | 802k 100% |
| nginx  |   291,635  17% |          41,771  17% |   184k  16% |   263k  19% |   285k  18% |   283k  17% |   292k  20% |   289k  23% | 287k  35% |

### cache-hot, h2

| Server |  Peak TPS [↓1] | CPU EER at peak [↓2] |      8×32 |     16×32 |       64×32 |      256×32 |    1,024×32 |  4,096×32 |  8,192×32 |
| ------ | -------------: | -------------------: | --------: | --------: | ----------: | ----------: | ----------: | --------: | --------: |
| fit    | 1,189,318 100% |         171,245 100% | 807k 100% | 986k 100% | 1,148k 100% | 1,189k 100% | 1,043k 100% | 924k 100% | 856k 100% |
| nginx  |   315,456  26% |          45,191  26% | 211k  26% | 287k  29% |   312k  27% |   315k  26% |   313k  30% | 312k  33% | 309k  36% |

### cache-hot, HTTP/3

| Server | Peak TPS [↓1] | CPU EER at peak [↓2] |      8×32 |     16×32 |     64×32 |    256×32 |  1,024×32 |  4,096×32 |  8,192×32 |
| ------ | ------------: | -------------------: | --------: | --------: | --------: | --------: | --------: | --------: | --------: |
| fit    |  720,439 100% |         107,164 100% | 595k 100% | 720k 100% | 637k 100% | 547k 100% | 504k 100% | 476k 100% | 448k 100% |
| nginx  |  500,388  69% |          71,660  66% | 267k  44% | 375k  52% | 500k  78% | 473k  86% | 432k  85% | 401k  84% | 377k  84% |

### cache-zipf, HTTP/1.1

| Server | Peak TPS [↓1] | CPU EER at peak [↓2] |        16 |        64 |       256 |     1,024 |     4,096 |    16,384 |    32,768 |
| ------ | ------------: | -------------------: | --------: | --------: | --------: | --------: | --------: | --------: | --------: |
| fit    |  644,014 100% |          92,893 100% | 385k 100% | 625k 100% | 644k 100% | 630k 100% | 584k 100% | 501k 100% | 399k 100% |
| nginx  |  312,461  48% |          44,721  48% | 213k  55% | 312k  50% | 312k  48% | 309k  49% | 301k  51% | 281k  56% | 274k  68% |

## 说明

**fixed、static 和缓存场景。** 每个服务器都在第 3 到第 5 档最快：16 个 HTTP/1.1 连接或 8×32 个流不足以让 7 个 CPU 忙起来
（第 1 档 fit 用了 4.1–6.9 个核，nginx 0.6–5.7），而超过 4,096 个连接或 1,024×32 个流后，处理这么多连接和流的开销比它们
承载的工作增长得更快。到第 7 档，fixed 场景下 nginx 和 fit 的 HTTP/1.1 都比各自峰值低约 41%；h2 上 nginx 不降反升，在
8,192×32 时达到最佳，fit 则下降 27%。HTTP/3 上，fixed 从 64×32 起 nginx 超过 fit，而 static 和缓存场景每一档都是 fit 领先。

**代理。** 第 1–3 档时，每个快的代理都能达到后端的上限（2 个 CPU，每秒约 26 万个请求）。之后：

- nginx 的 HTTP/1.1 从 64 个连接起保持 20 万，h2c 和 h2 在 1,024×32 以内也是；4,096×32 时它仅有的少量应答都是 5xx，
  8,192×32 时让 h2load 卡死。
- Envoy 的 h2c 和 h2 在 1,024×32 以内保持 15 万，之后只回 5xx；HTTP/3 每一档都保持在 5.1–7.5 万。
- Caddy 和 Traefik 到 1,024×32 时损失四分之三，4,096×32 时每秒只应答几千个；HTTP/3 每一档都保持在 8.2–13.8 万，nginx
  也保持在 9.3 万以上。
- fit：见下节。

## fit 的代理超出上限之后

fit 的代理最多保持 4,096 个到后端的连接，再加 1,024 个等待连接的请求：共 5,120 个在途请求。超出的请求立即回 503，这是
它的第 11 条规则在起作用，而没有这种上限的 nginx 不会这样做。出问题的是其余请求的处理：

| 轮次 | 在途请求 | 每秒成功 | 回 5xx 的比例 | CPU | 协程数 |
| --- | ---: | ---: | ---: | ---: | ---: |
| HTTP/1.1，第 5 档 | 4,096 | 22.4 万 | 0% | 5.7 | 1,030 |
| h2c，第 4 档 | 8,192 | 7.7 万 | 75% | 5.7 | 8,086 |
| h2，第 5 档 | 32,768 | 30 | 99.98% | 2.0 | 14,749 |
| HTTP/1.1，第 6 档 | 16,384 | 7.6 万 | 68% | 4.5 | 11,624 |
| h2c，第 6 档 | 131,072 | 无应答 | — | 0.02 | 21,191 |
| h2，第 7 档 | 262,144 | 无应答 | — | 0.08 | 25,223 |

有 4,096 个连接连到每秒能应答 26 万个请求的后端，fit 在拒绝多余请求的同时，本应继续应答接近这个数目的请求。实际上它的
成功响应降到三分之一，再降到零；从第 6 档起，h2c 和 h2 上 30 秒内一个请求都不应答，连 503 都没有，CPU 空闲，有 2.1–2.5
万个协程存活：请求在等某个不会到来的东西。

压测客户端只统计状态码类别，不统计具体状态码；按 fit 的配置，超出上限后的 5xx 就是 `max_pending` 的 503。

## 失败

**卡死**（压测客户端在 300 秒后被结束；保留结果，不重跑）：

| 档位 | 服务器 | 轮次 |
| --- | --- | --- |
| 6 | traefik | h2 上的 proxy |
| 7 | nginx | h2c 和 h2 上的 proxy |
| 7 | caddy | h2 上的 static；h2c 和 h2 上的 proxy |
| 7 | traefik | h2 上的 proxy |
| 7 | envoy | h2 上的 proxy |

**无应答**（压测客户端跑完了，但没有任何应答）：第 6、7 档 fit 在 h2c 和 h2 上的 proxy，以及第 7 档 Envoy 在 h2c 上的
proxy。

**没能启动、已重跑**：第 4、5 档 Traefik 的 proxy，以及第 6 档 fit 的全部轮次，见上文。

## fit 需要关注的地方

1. **代理在 h2c 和 h2 上的停滞**：在途流超过 131,072 时，30 秒内没有任何应答，连 503 都没有，有 2.1–2.5 万个协程而 CPU
   空闲。无法服务的请求也必须得到应答（第 1、11 条规则）；要查清它们在等什么。首要怀疑是背压：等待客户端取走的响应有
   256 MiB 的上限（`max_pending_bytes`），一旦超过就让所有连接停止读取，而它可能一直没有排空。
2. **溢出时代理的成功速率**：在途请求维持在 5,120 个时，fit 应当在给多余请求回 503 的同时继续以后端的速率服务，而不是
   降到三分之一甚至更低。要测量 `max_pending` 溢出后时间花在哪里（在压测机上对第 4 档的 h2c 做 perf）。
3. **压测工具**：给每次压测客户端运行加上时间限制，让卡死的轮次自己失败，而不是靠看门狗；并在 `bench/README.md` 里写明，
   压测机必须把服务器的端口排除在临时端口范围之外。
4. **压测配置里的上游上限**与 nginx 不同，nginx 只限制空闲连接：如果要在高连接数下比较代理本身，可以再跑一组把 fit 的
   `max_pending` 调高到与其他服务器的排队能力相当的结果，把上限的影响和代理本身分开。

## 后续，2026-10-24

代理在第 4-7 档的失败是什么原因：在压测机上只跑 fit 的代理（后端、CPU 划分和 h2load 与扫描相同），并在每轮中途
抓取 goroutine 栈：

- **fit 的崩溃是 fib HTTP 客户端的缺陷**，与压测机无关。超过 `max_pending` 之后，客户端每拒绝一个请求，都要在
  客户端唯一的锁内遍历整个等待队列（约 1,000 个请求）；超过上限后几乎每个请求都被拒绝，而上游连接的响应和拨号
  也要拿这把锁。上游事件循环排在成千上万个 `Client.enqueue` 中的 goroutine 后面（4,096×32 时有 14,000 个），
  响应停滞，在途的上游请求数冻结不动：只剩 503。fib 的 [`ee1a859`](https://github.com/lesismal/fib/commit/ee1a859)
  在请求于等待中结束时计数，以常数时间检查上限（fit 的需求 A44）。fit 自身在连接池已持有 `max_conns` 加
  `max_pending` 个请求时，不再构造请求，直接回答 503（`upstream.Pool.Saturated`）。
- **第 6、7 档超出了代理测试能测量的范围。** 131,072 或 262,144 个请求同时在途，而后端只有两个 CPU、每秒约
  26 万次响应，所有代理都失败（nginx 和 Envoy 没有任何应答，或让 h2load 挂住）；fit 超过上限后，CPU 都花在用
  503 回应闭环压测客户端的重试上。今后的扫描（[`bench/sweep.sh`](../sweep.sh)）只在第 1-5 档跑代理。

每秒成功响应数，5 秒预热后测 30 秒，fit 在 CPU 0-6：

| 测试 | 扫描中的 fit | 加上 fib 的修复 | 再加提前 503 | 扫描中的 nginx |
| --- | ---: | ---: | ---: | ---: |
| h2c，256×32 | 77k | 194k | 196k | 206k |
| h2c，1,024×32 | 19k | 133k | 184k | 201k |
| h2c，4,096×32 | 0 | 53k | 85k | 0 |
| HTTP/1.1，16,384 | 76k | 180k | 189k | 208k |

## 配置

下面是这次扫描中每个服务器实际使用的配置，即压测工具从 e079c83 的 [`bench/configs`](../configs) 生成的版本：已填入后端地址
（代理 127.0.0.1:9000，WebSocket 127.0.0.1:9001，缓存 127.0.0.1:9003），nginx 的 worker 数设为服务器的 7 个 CPU。
最重要的几项：

- **日志**：所有服务器都不写访问日志。
- **工作线程**：nginx 7 个 worker 进程；Envoy 7 个 worker 线程（`--cpuset-threads`）；Caddy、Traefik 和 fit 都是 Go 程序，
  线程数等于它们看到的 7 个 CPU。
- **后端连接**：nginx 最多保持 4,096 个到后端的空闲连接（`keepalive`），请求需要多少就开多少；Caddy 和 Traefik 保持
  4,096 个空闲连接，同样按需开；Envoy 的熔断上限为一百万；fit 最多开 4,096 个（`max_conns`），另外最多排队 1,024 个
  （`max_pending`，默认值），再多就回 503。
- **流**：nginx 和 Envoy 每个 HTTP/2 和 HTTP/3 连接允许 256 个并发流；Caddy、Traefik 和 fit 用默认值，都容得下每个连接的
  32 个流。
- **连接数**：nginx 每个 worker `worker_connections 65536`；fit `limits.max_conns` 为 1,000,000；其他服务器在 1,048,576 个
  文件描述符以内没有自己的上限。
- **缓存**：nginx 用磁盘上的 `proxy_cache`（因此在页缓存里），1 GiB，带锁；fit 用内存中的 `cache`，1 GiB。

**nginx**：`nginx.conf`，挂载到 `/etc/nginx/nginx.conf`；启动命令 the image's own (`nginx -g "daemon off;"`).

<details><summary>完整配置</summary>

```nginx
# nginx for the fit benchmark harness. fitbench fills in 7, one
# worker per CPU the server is pinned to, and the addresses of the proxy,
# WebSocket and cache backends.
#
# Port 8080 serves HTTP/1.1 and port 8081 h2c (HTTP/2 with prior knowledge),
# with the same three locations on both; 8080 also proxies WebSockets at /ws.
# Port 8443 is HTTP/1.1 over TLS, for idle TLS connections; port 8445 is
# HTTP/2 over TLS and UDP port 8444 HTTP/3.
worker_processes 7;
worker_rlimit_nofile 1048576;
error_log stderr error;

events {
    worker_connections 65536;
    multi_accept on;
}

http {
    include /etc/nginx/mime.types;
    access_log off;
    server_tokens off;
    sendfile on;
    tcp_nopush on;
    tcp_nodelay on;
    keepalive_timeout 65;
    keepalive_requests 1000000;

    # Every proxy keeps up to 4096 idle connections to the backend, more than
    # the load generators have requests in flight, so none of them closes and
    # reopens upstream connections under load.
    upstream backend {
        server 127.0.0.1:9000;
        keepalive 4096;
        keepalive_requests 1000000;
    }

    upstream wsbackend {
        server 127.0.0.1:9001;
    }

    # The cache scenarios: up to 1 GiB of responses, as fit's cache keeps
    # in memory, here on disk and so in the page cache; enough for every
    # path the scenarios ask for (about 430 MB), since nginx's max_size is
    # enforced only by its cache manager, which at the scenarios' rate lets
    # the cache grow far past a smaller one. Keys for about 128,000.
    proxy_cache_path /var/cache/nginx/fitbench levels=1:2 keys_zone=bench:16m max_size=1g inactive=1h use_temp_path=off;

    upstream cachebackend {
        server 127.0.0.1:9003;
        keepalive 4096;
        keepalive_requests 1000000;
    }

    server {
        listen 8080 reuseport;

        location = /fixed {
            default_type text/plain;
            return 200 "hello, world";
        }
        location /static/ {
            root /srv/www;
        }
        location = /proxy {
            proxy_pass http://backend;
            proxy_http_version 1.1;
            proxy_set_header Connection "";
        }
        location /cache/ {
            proxy_pass http://cachebackend;
            proxy_http_version 1.1;
            proxy_set_header Connection "";
            proxy_cache bench;
            proxy_cache_lock on;
        }
        location = /ws {
            proxy_pass http://wsbackend;
            proxy_http_version 1.1;
            proxy_set_header Upgrade $http_upgrade;
            proxy_set_header Connection "upgrade";
            proxy_read_timeout 1h;
        }
    }

    server {
        listen 8081 reuseport;
        http2 on;
        http2_max_concurrent_streams 256;
        keepalive_requests 1000000;

        location = /fixed {
            default_type text/plain;
            return 200 "hello, world";
        }
        location /static/ {
            root /srv/www;
        }
        location = /proxy {
            proxy_pass http://backend;
            proxy_http_version 1.1;
            proxy_set_header Connection "";
        }
        location /cache/ {
            proxy_pass http://cachebackend;
            proxy_http_version 1.1;
            proxy_set_header Connection "";
            proxy_cache bench;
            proxy_cache_lock on;
        }
    }

    # HTTP/2 over TLS on port 8445 and HTTP/3 on UDP port 8444, with the
    # same locations as h2c.
    server {
        listen 8445 ssl reuseport;
        http2 on;
        http2_max_concurrent_streams 256;
        listen 8444 quic reuseport;
        http3 on;
        http3_max_concurrent_streams 256;
        ssl_certificate /certs/cert.pem;
        ssl_certificate_key /certs/key.pem;
        ssl_protocols TLSv1.3;
        keepalive_requests 1000000;

        location = /fixed {
            default_type text/plain;
            return 200 "hello, world";
        }
        location /static/ {
            root /srv/www;
        }
        location = /proxy {
            proxy_pass http://backend;
            proxy_http_version 1.1;
            proxy_set_header Connection "";
        }
        location /cache/ {
            proxy_pass http://cachebackend;
            proxy_http_version 1.1;
            proxy_set_header Connection "";
            proxy_cache bench;
            proxy_cache_lock on;
        }
    }

    server {
        listen 8443 ssl reuseport;
        ssl_certificate /certs/cert.pem;
        ssl_certificate_key /certs/key.pem;
        ssl_protocols TLSv1.2 TLSv1.3;
    }
}
```

</details>

**Caddy**：`Caddyfile`，挂载到 `/etc/caddy/Caddyfile`；启动命令 the image's own (`caddy run --config /etc/caddy/Caddyfile --adapter caddyfile`).

<details><summary>完整配置</summary>

```caddyfile
# Caddy for the fit benchmark harness. fitbench fills in the addresses of the
# proxy and WebSocket backends.
#
# Port 8080 serves HTTP/1.1 and port 8081 h2c (HTTP/2 with prior knowledge),
# with the same routes on both, /ws proxying WebSockets. Port 8443 is
# HTTP/1.1 over TLS, for idle TLS connections; port 8445 HTTP/2 over TLS and
# UDP port 8444 HTTP/3, with the same routes but /ws. Caddy logs no requests
# unless asked to.
{
	admin off
	auto_https off
	servers :8080 {
		protocols h1
	}
	servers :8081 {
		protocols h1 h2c
	}
	servers :8443 {
		protocols h1
	}
	servers :8444 {
		protocols h3
	}
	# With h2 alone, Caddy drops TLS connections after the client hello;
	# clients that offer h2 still get it, by ALPN.
	servers :8445 {
		protocols h1 h2
	}
}

(routes) {
	handle /fixed {
		header Content-Type text/plain
		respond "hello, world" 200
	}
	handle /static/* {
		root * /srv/www
		file_server
	}
	handle /proxy {
		# Up to 4096 idle backend connections, as every proxy here keeps;
		# Caddy's default of 32 makes it reopen them under load.
		reverse_proxy 127.0.0.1:9000 {
			transport http {
				keepalive_idle_conns 4096
				keepalive_idle_conns_per_host 4096
			}
		}
	}
	handle {
		respond 404
	}
}

http://:8080, http://:8081 {
	handle /ws {
		reverse_proxy 127.0.0.1:9001
	}
	import routes
}

https://:8444, https://:8445 {
	tls /certs/cert.pem /certs/key.pem
	import routes
}

:8443 {
	tls /certs/cert.pem /certs/key.pem
	respond 404
}
```

</details>

**Traefik**：`traefik.yml`，挂载到 `/etc/traefik/traefik.yml`；启动命令 the image's own (`traefik`).

<details><summary>完整配置</summary>

```yaml
# Traefik for the fit benchmark harness. Traefik only proxies: it has no
# fixed response and serves no files, so it runs the proxy scenario alone.
#
# Port 8080 serves HTTP/1.1, and port 8081 h2c (HTTP/2 with prior
# knowledge), which Traefik accepts on any cleartext entry point; 8080 also
# proxies WebSockets at /ws. Port 8443 is TLS, for idle TLS connections; port
# 8445 HTTP/2 over TLS, and UDP port 8444 HTTP/3 (with TLS on TCP 8444
# beside it, as Traefik requires).
entryPoints:
  h1:
    address: ":8080"
  h2c:
    address: ":8081"
  tls:
    address: ":8443"
    http:
      tls: {}
  h2:
    address: ":8445"
    http:
      tls: {}
  h3:
    address: ":8444"
    http3: {}
    http:
      tls: {}
# Up to 4096 idle backend connections, as every proxy here keeps; Traefik's
# default of 200 makes it reopen them under load.
serversTransport:
  maxIdleConnsPerHost: 4096
providers:
  file:
    filename: /etc/traefik/dynamic.yml
log:
  level: ERROR
```

</details>

**Traefik, routes**：`dynamic.yml`，挂载到 `/etc/traefik/dynamic.yml`.

<details><summary>完整配置</summary>

```yaml
# fitbench fills in the addresses of the proxy and WebSocket backends.
http:
  routers:
    proxy:
      rule: "Path(`/proxy`)"
      entryPoints: [h1, h2c]
      service: backend
    proxy-tls:
      rule: "Path(`/proxy`)"
      entryPoints: [h2, h3]
      service: backend
      tls: {}
    ws:
      rule: "Path(`/ws`)"
      entryPoints: [h1]
      service: wsbackend
  services:
    backend:
      loadBalancer:
        servers:
          - url: "http://127.0.0.1:9000"
    wsbackend:
      loadBalancer:
        servers:
          - url: "http://127.0.0.1:9001"
tls:
  stores:
    default:
      defaultCertificate:
        certFile: /certs/cert.pem
        keyFile: /certs/key.pem
```

</details>

**Envoy**：`envoy.yaml`，挂载到 `/etc/envoy/envoy.yaml`；启动命令 `envoy -c /etc/envoy/envoy.yaml --cpuset-threads --log-level error`.

<details><summary>完整配置</summary>

```yaml
# Envoy for the fit benchmark harness. fitbench fills in the addresses of the
# proxy and WebSocket backends; Envoy runs one worker per CPU it is pinned to
# (--cpuset-threads).
#
# Port 8080 serves HTTP/1.1 and port 8081 h2c (HTTP/2 with prior knowledge),
# with the same routes on both; 8080 also proxies WebSockets at /ws. Port
# 8443 is HTTP/1.1 over TLS, for idle TLS connections; port 8445 HTTP/2 over
# TLS and UDP port 8444 HTTP/3, with the same routes as 8080 but /ws. Envoy serves no
# files and has no cache of its own here, so it runs the fixed and proxy
# scenarios. No access log is configured, so none is written.
static_resources:
  listeners:
  - name: h1
    address: {socket_address: {address: 0.0.0.0, port_value: 8080}}
    filter_chains:
    - filters:
      - name: envoy.filters.network.http_connection_manager
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
          stat_prefix: h1
          codec_type: HTTP1
          upgrade_configs: [{upgrade_type: websocket}]
          route_config: &routes
            virtual_hosts:
            - name: all
              domains: ["*"]
              routes:
              - match: {path: /fixed}
                direct_response: {status: 200, body: {inline_string: "hello, world"}}
                response_headers_to_add:
                - header: {key: content-type, value: text/plain}
                  append_action: OVERWRITE_IF_EXISTS_OR_ADD
              - match: {path: /proxy}
                route: {cluster: backend}
              - match: {path: /ws}
                route: {cluster: wsbackend, timeout: 0s, idle_timeout: 3600s}
          http_filters:
          - name: envoy.filters.http.router
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router
  - name: h2c
    address: {socket_address: {address: 0.0.0.0, port_value: 8081}}
    filter_chains:
    - filters:
      - name: envoy.filters.network.http_connection_manager
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
          stat_prefix: h2c
          codec_type: HTTP2
          http2_protocol_options: {max_concurrent_streams: 256}
          route_config: *routes
          http_filters:
          - name: envoy.filters.http.router
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router
  - name: tls
    address: {socket_address: {address: 0.0.0.0, port_value: 8443}}
    filter_chains:
    - transport_socket:
        name: envoy.transport_sockets.tls
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.DownstreamTlsContext
          common_tls_context:
            tls_certificates:
            - certificate_chain: {filename: /certs/cert.pem}
              private_key: {filename: /certs/key.pem}
      filters:
      - name: envoy.filters.network.http_connection_manager
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
          stat_prefix: tls
          codec_type: HTTP1
          route_config: {virtual_hosts: [{name: all, domains: ["*"], routes: []}]}
          http_filters:
          - name: envoy.filters.http.router
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router
  - name: h2
    address: {socket_address: {address: 0.0.0.0, port_value: 8445}}
    filter_chains:
    - transport_socket:
        name: envoy.transport_sockets.tls
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.DownstreamTlsContext
          common_tls_context:
            alpn_protocols: [h2]
            tls_certificates:
            - certificate_chain: {filename: /certs/cert.pem}
              private_key: {filename: /certs/key.pem}
      filters:
      - name: envoy.filters.network.http_connection_manager
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
          stat_prefix: h2
          codec_type: HTTP2
          http2_protocol_options: {max_concurrent_streams: 256}
          route_config: *routes
          http_filters:
          - name: envoy.filters.http.router
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router
  - name: h3
    address: {socket_address: {protocol: UDP, address: 0.0.0.0, port_value: 8444}}
    udp_listener_config:
      quic_options: {}
      downstream_socket_config: {prefer_gro: true}
    filter_chains:
    - transport_socket:
        name: envoy.transport_sockets.quic
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.transport_sockets.quic.v3.QuicDownstreamTransport
          downstream_tls_context:
            common_tls_context:
              tls_certificates:
              - certificate_chain: {filename: /certs/cert.pem}
                private_key: {filename: /certs/key.pem}
      filters:
      - name: envoy.filters.network.http_connection_manager
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
          stat_prefix: h3
          codec_type: HTTP3
          http3_protocol_options: {quic_protocol_options: {max_concurrent_streams: 256}}
          route_config: *routes
          http_filters:
          - name: envoy.filters.http.router
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router
  clusters:
  # Envoy's circuit breakers allow 1024 connections, pending and in-flight
  # requests by default, fewer than h2load keeps in flight; raised so that
  # the proxy is measured rather than its defaults. Each worker keeps its
  # own backend connections, reused with keep-alive.
  - name: backend
    type: STRICT_DNS
    dns_lookup_family: V4_ONLY
    connect_timeout: 5s
    circuit_breakers:
      thresholds: [{max_connections: 1000000, max_pending_requests: 1000000, max_requests: 1000000}]
    load_assignment:
      cluster_name: backend
      endpoints: [{lb_endpoints: [{endpoint: {address: {socket_address: {address: "127.0.0.1", port_value: 9000}}}}]}]
  - name: wsbackend
    type: STRICT_DNS
    dns_lookup_family: V4_ONLY
    connect_timeout: 5s
    circuit_breakers:
      thresholds: [{max_connections: 1000000, max_pending_requests: 1000000, max_requests: 1000000}]
    load_assignment:
      cluster_name: wsbackend
      endpoints: [{lb_endpoints: [{endpoint: {address: {socket_address: {address: "127.0.0.1", port_value: 9001}}}}]}]
```

</details>

**fit**：`fit.json`，挂载到 `/etc/fit/fit.json`；启动命令 `fit run -c /etc/fit/fit.json`.

<details><summary>完整配置</summary>

```json
{
  "admin": {"address": "127.0.0.1:9005"},
  "limits": {"max_conns": 1000000},
  "listeners": [
    {"name": "h1", "address": ":8080", "protocols": ["http/1.1"]},
    {"name": "h2c", "address": ":8081", "protocols": ["h2c"]},
    {"name": "tls", "address": ":8443", "protocols": ["http/1.1"],
      "tls": {"certificates": [{"cert_file": "/certs/cert.pem", "key_file": "/certs/key.pem"}]}},
    {"name": "h2", "address": ":8445", "protocols": ["h2"],
      "tls": {"certificates": [{"cert_file": "/certs/cert.pem", "key_file": "/certs/key.pem"}]}},
    {"name": "h3", "address": ":8444", "protocols": ["h3"],
      "tls": {"certificates": [{"cert_file": "/certs/cert.pem", "key_file": "/certs/key.pem"}]}}
  ],
  "upstreams": [
    {"name": "backend", "addresses": ["127.0.0.1:9000"], "max_conns": 4096, "max_idle_conns": 4096},
    {"name": "wsbackend", "addresses": ["127.0.0.1:9001"]},
    {"name": "cachebackend", "addresses": ["127.0.0.1:9003"], "max_conns": 4096, "max_idle_conns": 4096}
  ],
  "routes": [
    {
      "name": "fixed",
      "match": {"path": ["/fixed"]},
      "handler": {"module": "respond", "params": {"body": "hello, world", "headers": {"Content-Type": "text/plain"}}}
    },
    {
      "name": "static",
      "match": {"path": ["/static/*"]},
      "handler": {"module": "static", "params": {"root": "/srv/www", "precompressed": ["br", "zstd", "gzip"]}}
    },
    {
      "name": "proxy",
      "match": {"path": ["/proxy"]},
      "handler": {"module": "proxy", "params": {"upstream": "backend"}}
    },
    {
      "name": "cache",
      "match": {"path": ["/cache/*"]},
      "middleware": [{"module": "cache", "params": {"zone": "bench", "max_bytes": 1073741824, "max_entry_bytes": 65536}}],
      "handler": {"module": "proxy", "params": {"upstream": "cachebackend"}}
    },
    {
      "name": "ws",
      "match": {"path": ["/ws"]},
      "handler": {"module": "proxy", "params": {"upstream": "wsbackend"}}
    }
  ]
}
```

</details>

**The proxy backend (fit)**：`backend.json`，挂载到 `/etc/fit/fit.json`；启动命令 `fit run -c /etc/fit/fit.json`.

<details><summary>完整配置</summary>

```json
{
  "listeners": [{"name": "backend", "address": ":9000"}],
  "routes": [
    {"handler": {"module": "respond", "params": {"body": "hello, world", "headers": {"Content-Type": "text/plain"}}}}
  ]
}
```

</details>

**The cache backend (fit)**：`cache-backend.json`，挂载到 `/etc/fit/fit.json`；启动命令 `fit run -c /etc/fit/fit.json`.

<details><summary>完整配置</summary>

```json
{
  "admin": {"address": "127.0.0.1:9004"},
  "metrics": {},
  "listeners": [{"name": "cachebackend", "address": ":9003"}],
  "routes": [
    {
      "name": "cache",
      "match": {"path": ["/cache/*"]},
      "handler": {"module": "respond", "params": {"body": "0123456789abcdef… (4,096 bytes)",
        "headers": {"Content-Type": "text/plain", "Cache-Control": "max-age=600"}}}
    }
  ]
}
```

</details>

缓存后端对每个路径都返回 4,096 字节；这里省略了响应体。
