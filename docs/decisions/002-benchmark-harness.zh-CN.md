# 002：在容器里运行、由 Go 程序驱动的压测工具

[English](002-benchmark-harness.md) | [简体中文](002-benchmark-harness.zh-CN.md)

状态：已采纳，见 I07 和 I08（2026-10-19）。

## 背景

路线图要求 fit 以 nginx 为标杆，每个里程碑都要发布与 nginx、Caddy、Traefik 对比的数字：吞吐、延迟
百分位、CPU，以及每个空闲连接的内存。只有当每个服务器用等价的配置跑同样的场景、用同样的 CPU 和
同样的压测客户端，并且任何人都能用一条命令重跑时，这些数字才有意义。

## 决定

- **全部在 Docker 里运行。** 每个服务器用固定版本的官方镜像（fit 用本次编译的二进制，跑在
  `ubuntu:24.04` 上）；压测客户端、代理后端和 WebSocket 后端各有自己的容器。每个容器用
  `--cpuset-cpus` 绑定到各自的 CPU。在 Linux 上它们共用主机网络（`--network host`），没有额外开销；
  在其他系统上走 bridge 网络，结果只适合在同一台机器上比较。
- **驱动程序是一个 Go 程序** `bench/cmd/fitbench`，只用标准库和 `docker` 命令行。解析 wrk 和
  h2load 输出的代码有单元测试，shell 脚本做不到这一点。wrk 通过 Lua 的 `done` 钩子输出 JSON，
  数字直接读取，而不是从给人看的摘要里解析。
- **压测客户端**：HTTP/1.1 用 wrk，h2c 用 h2load，都用 Ubuntu 的 glibc 版本，因为 musl 的内存分配器
  在高请求率下会拖慢压测客户端。
- **等价性是检查出来的，不是假定的**：每轮之前先请求一次该场景，核对状态码、协议和响应体。
- **CPU 和内存取自服务器的 cgroup**（v2）：测量期间 `cpu.stat` 中 `usage_usec` 的增量，以及
  `memory.stat` 中的 `anon`。匿名内存包含容器内的所有进程（nginx 的 worker 和 master），不含页
  缓存，这正是这里想用 RSS 表达的；把各进程的 RSS 相加会重复计算共享页。
- **空闲连接内存**由 `bench/cmd/idleconn` 测量：这个 Go 工具打开 N 个连接，每个发一个请求后保持住；
  每种连接都对新启动的服务器测，每连接开销是匿名内存的增量除以 N。
- **静态文件放在 Docker volume 里**，而不是 bind mount：在 Docker Desktop 上，bind mount 慢到会成为
  测量对象本身。

## 影响

- 主机上只需要 Docker 和 Go。
- Traefik 既不能返回固定响应也不能提供文件，只跑代理场景；fit 只跑它的模块已支持的场景，其余随
  模块落地而加入。
- h2load 不报告延迟百分位，h2c 的结果只有均值、标准差和最大值。以后可以换成能报告百分位的 HTTP/2
  压测客户端（类 wrk 工具的 HTTP/2 模式，或 fit 自己基于 fib 写的），结果格式不变。
- 路线图提到的 WebSocket 和 HTTP/3 压测客户端不在 M0 内：WebSocket 暂由空闲 WebSocket 测量覆盖，
  HTTP/3 等 I10。
