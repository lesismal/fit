# fit 介绍 PPT — 页面规范（spec）

交付物：`fit-intro.univer`（Slide Unit）+ `fit-intro.pptx`。16 页，16:9，设计画布 960×540。
语言：简体中文。所有事实来自本仓库 README/docs/bench；基准数字来自 `bench/results/plan-2026-10-30/`。

## 全局设计常量

- 字体：`PingFang SC`（正文/标题）；代码 `Menlo`。
- 正文字号（设计 px）：标题 34、副标题 15、卡片标题 14、正文 12.5、注释 11、眉标 11。
- 颜色：
  - 深底页：bg `#0A1020`，panel `#141D33`，文字 `#E9EFFA`，弱文字 `#8C9DBB`，强调 `#4D8DFF`，青 `#22D3EE`
  - 浅底页：bg `#F7F9FC`，card `#FFFFFF`，ink `#0B1220`，body `#3B4A61`，muted `#6B7A90`，line `#E3E9F2`
  - 强调：蓝 `#2F6BFF`，蓝浅底 `#EAF0FF`，青 `#0891B2`，绿 `#0FA97A`，琥珀 `#D97706`，红 `#D93654`
- 浅色页统一骨架：眉标(56,52,11px muted,大写) · 标题(56,92,34px) · 可选副标题(56,120,14px body) ·
  分隔线 y=146 · 正文区 166–470 · 页脚左(56,512,10px muted) 页脚右页码(904,512,10px,anchor=end)。
- 深色页无页脚，除封面/尾页外不使用深色。

## 页面清单

### P1 封面（深色）
- 眉标：`APACHE-2.0 · GO · FIB ENGINE`
- 主标题：`fit`
- 副标题：`反向代理 · 负载均衡 · TLS 终结 · 静态文件 · API 网关`
- 一句话：`基于 fib 网络引擎：一个静态二进制，做到 nginx 的性能、Caddy 的默认安全与 Traefik 的模型。`
- 右下角三行小字：`github.com/lesismal/fit` / `v1.0.0 · Apache-2.0` / `2026.11`
- 图形：右侧同心圆弧 + `fib` 核心 + 三条协议引线（纯装饰）。

### P2 为什么是 fit
- 眉标：`01 / 背景`
- 标题：`三者的长处，恰好是另一方的短处`
- 副标题：`nginx、Caddy、Traefik 各自把一件事做到极致，也各自留下了明显的缺口。`
- 三列卡片：
  - nginx（青）：强项 `事件循环 · 静态文件与缓存 · 资源占用`；缺口 `自研 DSL 难以校验与生成` / `改配置要 reload 进程` / `自动 HTTPS、主动健康检查、动态上游要 Plus`
  - Caddy（蓝）：强项 `默认 HTTPS · 严格可编程配置 · 原子 Admin API`；缺口 `每连接一个 goroutine，高并发下内存与 GC 成本高` / `缓存、限流、四层代理依赖第三方插件`
  - Traefik（紫）：强项 `provider 驱动 · 容器与 K8s 原生 · 开箱可观测`；缺口 `通常是三者中最慢、最重` / `不是 Web 服务器：没有静态文件与缓存`
- 底部结论条：`fit 的选择：把三者最擅长的部分放进同一套数据面，再用 fib 补上它们共同的短板。`

### P3 fit 是什么
- 眉标：`02 / 概览`；标题：`一套数据面，六件主要工作`
- 辐射图：中心 `fit` / `fib engine`；六个节点：
  1. `反向代理` / `HTTP · WebSocket · gRPC · TCP · UDP`
  2. `负载均衡` / `9 种策略 · 主动 + 被动健康检查`
  3. `TLS 终结` / `ACME 自动签发 · OCSP · mTLS · SNI`
  4. `静态文件与缓存` / `sendfile · 两级缓存 · Cache Lock`
  5. `API 网关治理` / `认证 · 限流 · 熔断 · 重写`
  6. `动态配置` / `Admin API · provider · 控制台`
- 底部一行：`同一引擎上还跑着 WebRTC/TURN、tunnel 与 LLM 共享（见第 12 页）。`

### P4 架构
- 眉标：`03 / 架构`；标题：`控制面与数据面分离`
- 上带（控制面）：`配置来源` → `适配与严格校验` → `provision 所有模块` → `不可变运行时图 v(n+1)`
  小字：`文件 · Admin API · Docker · Consul · etcd · Kubernetes`
- 中间箭头带：`原子指针替换 · 监听器差量增删（AddBinding / RemoveBinding）`
- 下带（数据面，fib）：`listeners` → `mux` → `routers` → `middlewares` → `services` → `上游连接池`
- 三条注释：`配置变更不影响在途请求` / `长连接留在旧图，排空后释放` / `请求路径上没有阻塞调用`

### P5 引擎
- 眉标：`04 / 性能来源`；标题：`请求路径上，没有一个 goroutine`
- 副标题：`fit 不把每个连接交给一个 goroutine —— 数据面直接跑在 fib 的事件循环上。`
- 五条要点（左 3 右 2）：
  - `空闲连接几乎不占内存` `只保留一个连接结构；读写缓冲借用一轮即归还，没有常驻 bufio 栈。`
  - `背压是端到端单向的` `HoldReads 让慢客户端的等待传回上游；写 watermark 与全局待发字节预算兜底。`
  - `静态文件接近零拷贝` `sendfile + 内存文件缓存 + 预压缩孪生文件，命中即写。`
  - `可阻塞的工作有界外置` `外部认证、插件、磁盘缓存未命中放在各自的池里，池间单向等待，不会互锁。`
  - `需要时可 prefork` `按 CPU 隔离堆与 GC，SO_REUSEPORT 共享端口，行为与 nginx 的 worker 一致。`
- 右侧对比小注：`对比：Caddy / Traefik 在 net/http 上每连接一个 goroutine，40k 空闲 TLS 连接时 fit 用 475 MiB，nginx 1,305 MiB。`（数字来自 bench）

### P6 协议
- 眉标：`05 / 协议`；标题：`一套模型，覆盖七层与四层`
- 表（协议 / 能力 / 关键点）：
  - `HTTP/1.1` `双向流式代理` `逐跳头清理 · X-Forwarded-* / Forwarded · 可信代理`
  - `HTTP/2` `h2 / h2c 入口与上游` `流控与背压联动 · gRPC 双向流`
  - `HTTP/3` `QUIC · TLS 1.3` `不依赖 quic-go，与 fib 同引擎`
  - `WebSocket` `h1 / h2 / h3 入口` `代理到 HTTP/1.1 上游，升级与双向隧道`
  - `TCP / UDP` `四层服务` `按 SNI 透传不终止 · PROXY protocol`
  - `SOCKS / CONNECT` `正向代理` `客户端与目标访问规则`
  - `多协议单端口` `按首字节分流` `HTTP、TLS、SOCKS、裸 TCP 共存`

### P7 流量治理
- 眉标：`06 / 治理`；标题：`把流量交给策略，而不是脚本`
- 2×2 四象限：
  - `负载均衡` `9 种` 明细：`轮询 · 加权轮询 · 最少连接 · 随机 · P2C · IP/URI/Header/Cookie 一致性哈希 · 粘性 cookie`
  - `健康检查` `主动 + 被动` 明细：`主动 HTTP/TCP 探测 · 被动 max_fails / fail_timeout · 出轮与恢复`
  - `韧性` `重试 · 熔断 · 超时` 明细：`预算内重试 · 熔断器 · 连接/响应/空闲超时 · 故障转移与镜像`
  - `准入控制` `限流 · 并发 · 体积` 明细：`按 IP/Header/路由限流 · 并发上限 · body 与连接上限 · 写停顿超时`

### P8 基准
- 眉标：`07 / 基准`；标题：`在 nginx 的主场上与它打平`
- 副标题：`固定响应 HTTP/1.1，500 在途：fit 671,426 rps / p99 0.87 ms，nginx 668,401 rps / p99 2.09 ms。`
- 左：四根横向条形（fit 671k / nginx 668k / Envoy 261k / Caddy 257k），标注 rps
- 右：`更多读数` 四行：`h2c 400×25 · fit 3.45M` / `site HTTPS/1.1 · 98.6k（nginx 81.5k）` / `proxy-download 1 MiB · 14.4k（nginx 5.1k）` / `40k 空闲 TLS · 475 MiB（nginx 1,305 MiB）`
- 脚注：`AMD Ryzen 7 7800X3D · Ubuntu 24.04 · nginx 1.31.4 / Caddy 2.10.2 / Traefik 3.5.0；单机回环、6 个服务器 CPU；数据：bench/results/plan-2026-10-30。诚实读数：代理到后端的 API 场景仍为 nginx 的 66–78%。`

### P9 配置
- 眉标：`08 / 配置`；标题：`一份严格配置，三种写法`
- 左：深色代码卡（hello.conf 片段）
- 右：四个要点：`严格解码` `未知字段、类型错误都带路径与位置报错` / `同一份 schema` `fit schema 输出 JSON Schema，编辑器可提示` / `可拆分` `fit run -c base.conf -c conf.d` / `可迁移` `fit adapt 在 .conf / YAML / JSON / nginx / Caddyfile 之间转换，并列出未转换部分`
- 底部：`SIGHUP 或 -watch 重载：地址不变的 listener 原地保留，新配置有错时继续用正在运行的配置。`

### P10 变更流程
- 眉标：`09 / 运维`；标题：`改配置不用重启，也不用担心改坏`
- 四步流程：`校验` → `provision 新图` → `原子替换` → `旧图排空`
- 三张卡：`Admin API` `GET/PUT/PATCH/DELETE /config、ETag 防丢更新、历史版本与回滚、/config/effective 看生效配置` / `加密 store` `配置加密保存在 admin.store，fit store export / import` / `Web 控制台` `登录后修改全部配置，所有页面合为一个 workbench`

### P11 可观测
- 眉标：`10 / 可观测`；标题：`出问题时，先看清发生了什么`
- 三列：`日志` `访问日志 JSON / logfmt / line；有界缓冲；按 logger 分级；可按 4xx/5xx 只记错误` / `指标` `Prometheus /metrics；按 listener 与路由；/status 列出 listener、连接、上游健康与证书` / `链路` `OpenTelemetry 追踪，W3C trace context 透传给上游，OTLP/HTTP 导出 span 与指标`

### P12 边缘角色
- 眉标：`11 / 同一引擎上的其它角色`；标题：`网关之外，还是媒体服务器、隧道与模型网关`
- 三条横带：
  - `WebRTC` `TURN 服务器（UDP / TCP / TLS）+ 以 fit 为媒体服务器的 WHIP / WHEP + 单 UDP 端口代理其它媒体服务器`
  - `tunnel` `hub / node / proxy 三角色，TCP、WebSocket、QUIC 链路，把 NAT 后的服务安全暴露出来`
  - `LLM` `hub / node / proxy 共享模型，向 agent 提供 OpenAI、Anthropic、Gemini、Ollama、Cohere 兼容接口`
- 底部：`一条配置、一个二进制、一套引擎。`

### P13 扩展
- 眉标：`12 / 扩展`；标题：`用 Go 扩展，用 WASM 热插拔`
- 左：三步 `写一个 Go 包` → `module.Register("http.middleware.hello", …)` → `fit build -with github.com/you/fit-hello`；代码片段
- 右：三条 `22 个内置 handler / middleware 模块，参数严格解码并进入 JSON Schema` / `WASM 插件在沙箱中运行，给自己的有界池，Proxy-Wasm 兼容` / `模块直接跑在 fib worker 上，与内置模块零差别`

### P14 现状
- 眉标：`13 / 现状`；标题：`M0–M5 与 1.0 的迭代全部完成`
- 四格大数字：`16.4 万行` Go 代码（674 个文件）/ `1,564` 个测试函数 / `56` 个可运行示例 / `22 + 5 + 9` 模块 + provider + 角色
- 一行状态：`已完成：全部里程碑与 1.0 迭代 | 待办：1.0 的外部安全审计 | 进行中：P2P 与 VPN`
- 一行：`Apache-2.0 · 依赖精简，核心只依赖 fib 与标准库`

### P15 上手
- 眉标：`14 / 上手`；标题：`三条命令，一个 HTTPS 站点`
- 左：三步命令（安装 / 校验 / 运行），右：hello 配置代码 + 结果说明
- 底部：`也提供 Linux/macOS/Windows/FreeBSD 预编译二进制、ghcr.io/lesismal/fit 镜像、Helm chart 与 systemd unit。`

### P16 尾页（深色）
- 大字：`一个二进制，把三者最擅长的事做回来。`
- 链接：`github.com/lesismal/fit` / `docs/getting-started.md` / `examples/` / `bench/`
- 角标：`fit 1.0 · Apache-2.0 · built on fib`
