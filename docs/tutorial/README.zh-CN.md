# fit Web 控制台教程（总纲）

[English](README.md) | [简体中文](README.zh-CN.md)

本教程按照 fit 的 **Web 控制台**（workbench，工作台）的使用顺序编写：先部署一台带控制台
的 fit，再登录，然后按左侧**功能导航栏**的模块逐一讲解。每个模块一章，介绍它的**功能**、
**页面与操作**、**关键参数**，以及对应的 **.conf 配置文件**写法。

读者应已了解 [fit 是什么](../../README.zh-CN.md)、[.conf 格式](../reference/fit.zh-CN.md)
与[配置如何加载](../configuration.zh-CN.md)。教程中的完整字段表不重复罗列，以链接指向
[配置参考](../reference/config.zh-CN.md)（由代码生成，与二进制一致）。

## 教程结构

前 3 章是准备：部署、登录、导航栏总览。其后 24 章与导航栏的模块一一对应，顺序与菜单相同。

| 章 | 文件 | 菜单模块 | 讲什么 |
| --- | --- | --- | --- |
| 01 | [部署](01-deployment.zh-CN.md) | — | 安装 fit；引导文件、store 与 `admin.console`；`fit run -admin` 首次启动；创建用户；访问控制台；Docker、Helm 与 systemd。 |
| 02 | [登录](02-login.zh-CN.md) | — | 登录页；用户与 bcrypt 密码哈希；默认密码与强制修改；会话；锁定与减速；CSRF 与 CSP；修改密码。 |
| 03 | [功能导航栏](03-navigation.zh-CN.md) | — | 工作台布局与 URL 规则；全部模块与页面清单；筛选、收起、主题与语言；角色的动态模块，以及始终显示的数据同步与 BitTorrent。 |
| 04 | [概览](04-overview.zh-CN.md) | 概览 | 运行中配置的概览：版本、连接、路由、上游健康、证书、突增。 |
| 05 | [监听](05-listeners.zh-CN.md) | 监听 | `listener` 的参数：地址、协议、TLS、客户端证书、SOCKS、PROXY 协议、mptcp。 |
| 06 | [引擎](06-engine.zh-CN.md) | 引擎 | 按端口查看运行中的功能：处理器、路由、缓存、流媒体、WebRTC、四层、SOCKS、TLS；实际绑定地址与标签。 |
| 07 | [全局设置](07-global-settings.zh-CN.md) | 全局配置 | `log`、`access_log`、`telemetry`、`metrics`、`pprof`、`limits`、`engine`、`providers`、其余成员，以及整个配置的导入与导出。 |
| 08 | [JSON](08-json.zh-CN.md) | JSON | 整个配置的 JSON：草稿与生效配置两个标签、来源与优先级。 |
| 09 | [版本](09-versions.zh-CN.md) | 版本 | 已应用的配置版本：查看与回滚。 |
| 10 | [一致性检查](10-consistency-check.zh-CN.md) | 一致性检查 | 运行中配置与生效配置的差异：drift 与 info、逐项修正、`fit check`。 |
| 11 | [日志](11-logs.zh-CN.md) | 日志 | fit 自身日志与访问日志的文件、切割、保留、立即切割与清理。 |
| 12 | [集群](12-cluster.zh-CN.md) | 集群 | `admin.cluster` 管理端：成员、分组、模板、发布、告警、用户、跨成员指标/日志/证书、成员的 BitTorrent 角色与分发、NAT 后面的成员。 |
| 13 | [账户](13-account.zh-CN.md) | 账户 | 修改自己的密码、设置其他用户的密码、本节点信息。 |
| 14 | [助手](14-assistant.zh-CN.md) | 助手 | `admin.assistant`：提供方与密钥、级别、对话面板、提案、技能、MCP 服务器、审计日志。 |
| 15 | [反向代理](15-reverse-proxy.zh-CN.md) | 反向代理 | 路由：匹配、处理器、中间件；上游：负载均衡与健康检查；各处理器与中间件模块。 |
| 16 | [正向代理](16-forward-proxy.zh-CN.md) | 正向代理 | `forward_proxy` 路由（HTTP CONNECT）与 SOCKS5。 |
| 17 | [四层代理](17-l4-proxy.zh-CN.md) | 四层代理 | `services`：TCP 与 UDP 转发、SNI 透传、PROXY 协议。 |
| 18 | [TLS 证书](18-tls.zh-CN.md) | TLS | 证书页（运行中的证书）、自动 HTTPS、ACME。 |
| 19 | [缓存](19-cache.zh-CN.md) | 缓存 | 缓存区、清除、`cache` 中间件。 |
| 20 | [流媒体](20-streaming.zh-CN.md) | 流媒体 | `streaming`：RTMP、RTSP、SRT 与 WebRTC 监听、频道与转推；路由上的流媒体处理器。 |
| 21 | [媒体认证](21-media-auth.zh-CN.md) | 媒体认证 | `media_auth` 策略：令牌、用户、密钥环、公开动作、URL 签名。 |
| 22 | [媒体库](22-media-library.zh-CN.md) | 媒体库 | 媒体库条目、频道、播放 URL、指标、配置页与 `vod` 路由。 |
| 23 | [WebRTC](23-webrtc.zh-CN.md) | WebRTC | `webrtc`：TURN 服务器、WHIP/WHEP 媒体服务器、媒体代理。 |
| 24 | [隧道角色](24-tunnel.zh-CN.md) | 隧道角色、tunnel.hub、tunnel.node、tunnel.proxy | `tunnel` 配置；hub、node、proxy 三种角色的页面。 |
| 25 | [LLM 角色](25-llm.zh-CN.md) | LLM 角色、llm.hub、llm.node、llm.proxy | `llm` 配置；hub、node、proxy 三种角色的页面。 |
| 26 | [数据同步](26-sync.zh-CN.md) | 数据同步 | `sync`：从源到 sink 的管道、记录、检查点、死信、批处理作业；管道的各页面与模块配置。 |
| 27 | [BitTorrent](27-bittorrent.zh-CN.md) | BitTorrent | `bittorrent`：客户端（种子、分类、队列、速率、RSS、发布）与 tracker；两种角色的页面与模块配置。 |

## 各章细目

每章的页面、所编辑的配置成员与主要资料来源：

### 01 部署

- 安装：`go install`、预编译二进制、容器镜像 `ghcr.io/lesismal/fit`、Helm chart、systemd unit。
- 两个文件：引导文件（明文，含 `admin`）与 store（加密，含其余配置）；`admin.store` 的字段
  （`path`、`password`、`password_file`、`password_env`）。
- 首次启动：`fit run -admin`（默认用户 `admin`/`admin`，`-timeout` 内必须改密码）；store 的默认位置与密钥文件。
- 创建用户：`fit hash-password`；`admin.console.users`。
- 访问控制台：环回地址（SSH 隧道）、非环回地址必须 `cert_file` 与 `key_file`。
- 打包方式：Docker（端口、volume、store 密码）、Helm（Gateway API 与 Ingress 控制器）、systemd（`fit.service`、`fit@.service`、reload）。
- 资料来源：[部署](../../deploy/README.zh-CN.md)、[快速上手](../getting-started.zh-CN.md)、[控制台与 store](../console.zh-CN.md#两个文件)、README。

### 02 登录

- 登录页与 `POST /api/login`；会话 cookie（`fit_console_<端口>`）、`session_timeout`、`session_lifetime`、`max_sessions`、`max_conns`。
- 默认密码流程：只能打开“修改默认密码”页，超时后失效。
- 安全：地址锁定（15 分钟 5 次失败）、用户减速、虚设哈希、CSRF（`X-Fit-Csrf`）、CSP 与响应头、`admin` 日志。
- 资料来源：[控制台与 store](../console.zh-CN.md#登录与会话)、[控制台与 store](../console.zh-CN.md#用户与-fit-hash-password)、[控制台与 store](../console.zh-CN.md#上限与超时)。

### 03 功能导航栏

- 布局：左侧菜单（一级为模块、二级为页面、三级为端口的功能），右侧页面；顶栏（标题、面包屑、工具、主题与语言）。
- URL 规则：`#/<模块>/<页面>`；模块只显示在控制台提供其服务端模块时。
- 全部模块与页面的清单（与菜单顺序一致），含各模块的小标签（HTTP、HTTP CONNECT、TCP/UDP、TLS、RTMP / RTSP / SRT、媒体令牌、WebRTC、隧道、LLM、管道、客户端 / tracker、点播 / 频道）。
- 筛选菜单、收起为图标、展开与折叠；主题（浅色、深色等）与语言（英文、中文）。
- 角色的动态模块：`tunnel.*` 与 `llm.*` 在角色运行后出现；数据同步与 BitTorrent 属于控制台本身，始终显示。
- 资料来源：`app/workbench/web/core/workbench.js`（菜单顺序 RANK 与各模块注册）、[控制台与 store](../console.zh-CN.md#页面)。

### 04 概览

- 运行中配置的概览：配置版本与来源、重载、连接、路由与四层服务数、上游后端可用数、证书（14 天内到期）、各来源的元素计数、被遮蔽的元素。
- 突增：两次刷新之间增加的错误计数（上游失败、被拒连接、失败的重载）。
- 资料来源：`app/workbench/web/gateway/overview.js`、[配置参考：The config](../reference/config.md#the-config)。

### 05 监听

- 页面：编辑 `listeners`，每行显示地址、协议、TLS/SOCKS 标记；各端口的功能在“引擎”菜单。
- 参数：`address`、`protocols`（`http/1.1`、`h2c`、`h2`、`h3`、`tcp`、`udp`、`socks`）、`tls`（证书文件、ACME、按需）、`client_auth`、`socks`、`proxy_protocol`、`mptcp`。
- 约束：一个地址一个服务器；两个监听不能在同一地址和传输上。
- 资料来源：`templates/conf/conf.d/10-listeners.conf`、[配置参考：Listeners](../reference/config.md#listeners)、[配置](../configuration.zh-CN.md#一个地址一个服务器)。

### 06 引擎

- 按端口查看运行中的监听：每个端口展开为它提供的功能——各处理器（静态文件、直接应答、FastCGI、媒体令牌与媒体页面，以及其路由上按模块 id 列出的其他处理器，如 `vod`、`sync_ingest`、`bt_stream`、`bt_publish`、`bt_tracker`）、反向与正向代理路由、缓存、流媒体与 WebRTC 处理器、四层服务、SOCKS、TLS/ACME/默认 HTTPS——每项链接到编辑它的页面。
- 写入的地址旁给出内核实际绑定的地址；端口行的标签（PROXY 协议、HTTP/3、按需证书、客户端证书、点播）。
- 资料来源：`app/workbench/web/gateway/engine.js`、[控制台与 store](../console.zh-CN.md#页面)。

### 07 全局设置

- 各页编辑一个顶层成员：`log`（按 logger 的级别、输出、格式、`time_zone`、`rotate`）、`access_log`、`telemetry`（OTLP）、`metrics`、`pprof`（即时开关，Admin API 的 `/debug/pprof/`）、`limits`（连接、头部、请求体、写队列）、`engine`（event loop、worker、队列、缓冲、backlog）、`providers`（Docker、Consul、etcd、Kubernetes）。
- “其他”页：schema 中尚无专门页面的成员——`access`（全局允许与拒绝名单）、`media_library` 与 `schema_version`；`sync` 与 `bittorrent` 在各自模块的页面编辑。
- “导入 / 导出”页：整个配置的导入与导出（`.conf`、`.yaml`、`.json`、`.store`）。
- 资料来源：`templates/conf/conf.d/00-runtime.conf`、`30-observability.conf`、`50-providers.conf`、[配置参考](../reference/config.zh-CN.md)各章、[控制台与 store](../console.zh-CN.md#配置的导入与导出)。

### 08 JSON

- 草稿标签：整个配置的 JSON（有 store 时不含 `admin`），与各表单同一份草稿；格式化、打开文件、下载。
- 生效配置标签：基础配置加上 provider 提供的内容，只读；优先级与来源表。
- 资料来源：`app/workbench/web/gateway/modules.js`、[配置](../configuration.zh-CN.md)。

### 09 版本

- 已应用的配置版本（`admin.history` 个）：版本、应用时间、来源、大小、ETag；查看文本；回滚（作为新版本重新应用）。
- 资料来源：`app/workbench/web/gateway/modules.js`、[配置参考：Admin API](../reference/config.md#admin-api)。

### 10 一致性检查

- 运行中的 fit 与实际生效配置的差异：drift（可修正）与 info（运行事实）；各类差异（启动时才生效的设置、角色、provider 的部分、监听、TLS、上游、缓存）。
- 逐项选择修正并一起应用；`fit check` 在终端做同样的事。
- 资料来源：[控制台与 store](../console.zh-CN.md#检查运行中的配置)、`app/workbench/web/gateway/check.js`。

### 11 日志

- fit 自身日志与访问日志写出的文件、各自的切割与保留、磁盘上的文件及大小与时间；**立即切割**与**立即清理**（先预览要删除的文件）。
- `log` 与 `access_log` 的表单（含 `time_zone` 与 `rotate`）。
- 资料来源：`app/workbench/web/gateway/logs.js`、[配置参考：Log](../reference/config.md#log)、[配置参考：Access log](../reference/config.md#access-log)。

### 12 集群

- `admin.cluster` 使本 fit 成为管理端：成员的地址与凭据保存在 store；概览、节点、分组、模板、发布、告警、用户、跨成员的指标/日志/证书，以及成员的 BitTorrent 角色、私有种子的分发与概览页上的卡片。
- 添加、测试与信任成员；NAT 后面的成员以 `admin.cluster_member` 主动连接管理端。
- 资料来源：[集群管理](../cluster.zh-CN.md)、`app/workbench/web/cluster/`、[配置参考：Admin API](../reference/config.md#admin-api)。

### 13 账户

- 修改自己的密码（需当前密码）、设置另一个用户的密码（每个控制台用户都是管理员）；密码保存在 store，重启后生效于登录。
- 本节点：节点 ID、版本、启动时间、运行中配置的版本与 ETag、角色、能力列表。
- 资料来源：[控制台与 store](../console.zh-CN.md#用户与-fit-hash-password)、`app/workbench/web/account/account.js`。

### 14 助手

- `admin.assistant`：提供方（Anthropic、OpenAI、Gemini、Azure OpenAI、Ollama、OpenAI 兼容、`llm.proxy`）、密钥（只写不读）、默认模型、上限。
- 用户级别：`off`、`read`、`edit`、`propose`、`apply`；对话面板（以当前页面为上下文）；提案（审阅并应用）；技能（22 个内置 + 自定义）；MCP 服务器（`/mcp` 与令牌）；审计日志；数据同步与 BitTorrent 两组工具，以及把它们的记录原样发送的设置。
- 资料来源：[控制台与 store](../console.zh-CN.md#助手)、`app/workbench/web/ai/`。

### 15 反向代理

- 路由页：按处理器类型分标签（全部、`proxy`、`static`、`respond`、`fastcgi` 等）；每条路由的 `match`（host、path、method、header、query、remote_ip、expr）、`handler` 与 `middleware`。
- 上游页：`addresses`、`load_balancing`（九种策略）、`sticky_cookie`、连接与排队上限、超时、主动与被动健康检查、`deny_internal`；每行显示后端可用数。
- 处理器与中间件模块清单（`proxy`、`static`、`respond`、`fastcgi`、`media_pages`、`media_tokens`、`vod`、`sync_ingest`、`bt_stream`、`bt_publish`、`bt_tracker`、流媒体与 WebRTC 处理器；`basic_auth`、`body_limit`、`cache`、`circuit_breaker`、`client_cert`、`compress`、`concurrency_limit`、`forward_auth`、`headers`、`jwt`、`rate_limit`、`redirect`、`rewrite`、`wasm`），各章链接到参考。
- 资料来源：`templates/conf/conf.d/60-apps.conf`、[配置参考：Routes](../reference/config.md#routes)、[配置参考：Upstreams](../reference/config.md#upstreams)、[配置参考：Handlers](../reference/config.md#handlers)、[配置参考：Middleware](../reference/config.md#middleware)、[schema/doc](../../schema/doc/) 各模块文档。

### 16 正向代理

- `forward_proxy` 路由：为所列客户端（`clients`）提供 HTTP CONNECT 与 HTTP 代理，可限定端口（`ports`）；环回、链路本地、未指定、组播与本机地址默认拒绝。
- 监听的 `socks` 协议提供 SOCKS5。
- 资料来源：[配置参考：Handlers](../reference/config.md#http-handlers-forward_proxy)、[配置参考：Listeners](../reference/config.md#listeners)。

### 17 四层代理

- `services`：TCP 连接与 UDP 数据报转发到上游；按 SNI 透传 TLS；`proxy_protocol`；`server_names`。
- 资料来源：`templates/conf/conf.d/40-services.conf`、[配置参考：Services](../reference/config.md#services)。

### 18 TLS 证书

- 证书页：各监听当前提供的证书（文件与 ACME），每 30 秒刷新；监听的 `tls` 在“监听”页设置。
- 自动 HTTPS：不写 listener 时自动生成 443 与跳转用的 80；`auto_https` 的字段。
- ACME：CA、账户邮箱、存储（目录、Redis、etcd、Consul）、验证方式、按需申请；`acme` 的字段。
- 资料来源：`templates/conf/conf.d/20-tls.conf`、[配置参考：HTTPS by default](../reference/config.md#https-by-default)、[配置参考：ACME](../reference/config.md#acme)、[schema/doc/acme.md](../../schema/doc/acme.md)、[schema/doc/auto_https.md](../../schema/doc/auto_https.md)。

### 19 缓存

- 缓存区页：各区的键数、内存、磁盘条目与大小、磁盘路径；**清除**（按 URL、标签或全部）。
- `cache` 中间件：路由上启用缓存，`zone`、键与 TTL 的参数。
- 资料来源：[配置参考：Middleware](../reference/config.md#http-middleware-cache)、[schema/doc/modules/cache.md](../../schema/doc/modules/cache.md)。

### 20 流媒体

- `streaming`：fib 媒体 hub（流、播放者、GOP 缓存的上限）；RTMP、RTSP（Digest/Basic/token）、SRT（stream ID、加密）监听器及其上限与超时；WebRTC 端口；每个监听器以 `auth` 引用 media_auth 策略。
- 频道与转推：`streaming.channels`（播放列表、日程、循环、随机）与 `streaming.push`（RTMP、SRT 目标）。
- 路由上的处理器：`hls`、`httpflv`、`whip`、`whep`、`stream_whip`、`stream_whep`、`vod`。
- 资料来源：[流媒体](../streaming.zh-CN.md)、[配置参考：Streaming](../reference/config.md#streaming)、[配置参考：Handlers](../reference/config.md#handlers)。

### 21 媒体认证

- `media_auth` 策略：静态 `tokens`（明文或 SHA-256）、`users`（明文或 bcrypt）、签发限时令牌的 `secret`/`secret_file`/`keys` 密钥环（`fit media-token`）、无需凭据的 `public` 动作、流名模式（`*` 通配）。
- `url_signing`：为观众签发会过期的播放地址。
- 资料来源：[配置参考：Media auth](../reference/config.md#media-auth)、[schema/doc/modules/forward_auth.md](../../schema/doc/modules/forward_auth.md) 之外的 [media_auth 文档](../media-library.zh-CN.md#会过期的播放地址)、[WebRTC](../webrtc.zh-CN.md#认证)。

### 22 媒体库

- 页面（按菜单顺序）：**媒体库**（条目、搜索、过滤、分页、扫描状态、重新扫描）、**频道**（播放列表、日程、循环与随机、推流目标）、**播放 URL**（`url_signing` 的测试 URL、密钥轮换、会话）、**指标**、**媒体库配置**（`media_library`）、**点播路由**（`vod` 路由）、**频道配置**（`streaming.channels` 与 `streaming.push`）、**URL 签名**（各策略的 `url_signing`）。
- `media_library` 的参数：`roots`、`files`、`exclude`、`follow_symlinks`、sidecar、各上限；`media_pages` 处理器提供的观众页面（媒体库页、房间的通话与聊天、发布与播放、播放器）及其字段。
- 资料来源：[媒体库](../media-library.zh-CN.md)、[频道](../channels.zh-CN.md)、[配置参考：Media library](../reference/config.md#media-library)、`app/workbench/web/gateway/media.js`。

### 23 WebRTC

- `webrtc.turn`：UDP、TCP 与 TLS 地址、静态用户与 coturn 限时凭据、中继端口范围、配额与带宽、允许与拒绝的对端。
- `webrtc.media`：WHIP/WHEP 媒体服务器（SFU）：UDP 端口、公网地址、ICE 服务器、上限与超时。
- `webrtc.proxy`：按 ICE ufrag 把对端中继到媒体服务器。
- 资料来源：[WebRTC](../webrtc.zh-CN.md)、[配置参考：WebRTC](../reference/config.md#webrtc)、[schema/doc/engine.md](../../schema/doc/engine.md) 之外的 [schema/doc](../../schema/doc/)。

### 24 隧道角色

- `tunnel` 配置：hub、node（暴露其旁边的服务）、proxy（本地端口与 Web 代理经 hub 转发）；TLS、WSS、QUIC 链路；连接令牌（`tokens` 只存哈希，`fit token`）。
- 角色的页面：tunnel.hub（概览、节点、代理、隧道、监听、令牌、设置）、tunnel.node（概览、hub、服务、隧道）、tunnel.proxy（概览、hub、节点、转发、隧道、信息）。
- 资料来源：[tunnel](../tunnel.zh-CN.md)、[配置参考：Tunnel](../reference/config.md#tunnel)、`app/workbench/web/tunnel/`。

### 25 LLM 角色

- `llm` 配置：hub、node（以本地或远端上游提供 AI 模型）、proxy（OpenAI、Anthropic、Gemini、Ollama、Cohere 兼容接口）；WebSocket mesh、模型、代理超时、API key；连接令牌。
- 角色的页面：llm.hub（概览、节点、模型、代理、流量、对端、设置）、llm.node（概览、模型、GPU、操作、任务、流量、对端、设置）、llm.proxy（概览、模型、节点、流量、密钥、对端、设置）。
- 资料来源：[llm](../llm.zh-CN.md)、[配置参考：LLM](../reference/config.md#llm)、`app/workbench/web/llm/`。

### 26 数据同步

- `sync` 成员与角色：源、转换与编解码、一个或多个 sink 组成的管道；记录及其带类型的值、只在每个 sink 都确认后才保存的检查点、位置与投递语义、重试、`on_error` 与死信、批处理作业（`mode: "once"`、`job`、日程）、事务型 sink 的恰好一次、WASM 插件；连接器（文件、数据库及其变更、主题、索引、桶、对象存储、可观测性与向量存储）及较新的 MQTT、RabbitMQ、NATS JetStream、Pulsar、SQL Server、Doris 与 StarRocks、Loki、OpenTelemetry 日志、InfluxDB、TDengine、Qdrant 与 Milvus。
- 页面：**管道**（状态、吞吐与延迟、已读取与已确认、错误、队列、死信；新建管道）、管道的**概览**（暂停、恢复、重新快照、测试连接、试运行、计数、sink、作业）、**编辑**、**检查点**（设置或重置）、**死信**（重放或丢弃），以及**同步配置**。
- `fit sync` 与 Admin API；助手的 `sync_*` 工具与 `sync-pipeline` 技能。
- 资料来源：[数据同步](../sync.zh-CN.md)、[配置参考：Sync](../reference/config.md#sync)、[控制台与 store](../console.zh-CN.md#数据同步)、[决策 036](../decisions/036-sync.zh-CN.md)、`app/workbench/web/sync/`。

### 27 BitTorrent

- `bittorrent` 成员：客户端角色（种子及其在 `data_dir` 中的状态、`listen`、上限与速率、队列、磁盘检查、分类、`alt_speed`、`watch_dir`、单种子上限、屏蔽名单、把目录发布为 hybrid 种子并以 fit 的静态文件作为 web 种子的 `publish` 条目、RSS 自动下载）与 tracker 角色（经 `bt_tracker` 路由的 HTTP announce 与其自行 `listen` 的 UDP、公开或使用 `media_auth` 策略 passkey 的私有、info hash 允许列表、有上限的 swarm）。
- 页面：**种子**（筛选、批量暂停/恢复/删除、添加种子、备用速率、最近事件）、种子的**概览 / 文件 / Peer / Tracker**、**Tracker**、**RSS**、**发布**、**BitTorrent 配置**。
- 路由上的处理器 `bt_stream`、`bt_publish` 与 `bt_tracker`；Admin API、控制台的事件流、`fit bt`、助手的 `bt_*` 工具；集群的 BitTorrent 页面与分发。
- 资料来源：[BitTorrent](../bittorrent.zh-CN.md)、[配置参考：BitTorrent](../reference/config.md#bittorrent)、[控制台与 store](../console.zh-CN.md#bittorrent)、[决策 037](../decisions/037-bittorrent.zh-CN.md)、`app/workbench/web/bittorrent/`。

## 编写约定（维护者与分章任务）

1. **文件**：每章两个文件，`NN-<slug>.zh-CN.md`（中文）与 `NN-<slug>.md`（英文），`NN` 为两位章号。
   两个文件互为完整翻译，不是摘要；文件头是标题（不带章号，章号在文件名中）、空行、语言切换行
   `[English](NN-<slug>.md) | [简体中文](NN-<slug>.zh-CN.md)`，与仓库现有文档相同。
2. **章节骨架**（可按内容增删小节，但顺序不变）：
   - `## 功能`（`## What it does`）：这个模块做什么，1–3 段。
   - `## 页面与操作`（`## Pages`）：菜单中该模块的每个页面各一小节（`###`），页面显示什么、能做什么操作。
   - `## 参数`（`## Key parameters`）：只列关键参数，表格 `| 字段 | 默认值 | 说明 |`；完整字段表链接到 [配置参考](../reference/config.zh-CN.md) 对应章节。
   - `## .conf 配置`（`## In .conf`）：该模块的 .conf 写法，一个最小但完整的示例（代码块语言标记 `fit`）。
   - `## 相关文档`（`## See also`）：链接到深入文档与参考。
   不编辑配置的页面（概览、引擎、版本、一致性检查、集群等）省略“参数”与“.conf 配置”或只写所涉及的成员。
3. **.conf 示例必须能通过校验**：把示例（含必要的注释行）存成临时文件，运行
   `./bin/fit validate -c <临时文件>`，通过才可写入。示例应是自包含的完整配置：
   只写 `upstream` 而没有监听器会报错，需要时补一个最小 `listener`。示例中的 `admin` 只出现在以
   `admin` 本身为主题的章节：01（引导文件）、02（用户）、12（`admin.cluster`）与 14
   （`admin.assistant`）；控制台页面从不编辑 `admin`，其他章的配置都没有它。
4. **链接**：只用相对路径。章间互链用本目录下的文件名；链到其他文档用 `../<文件>`。
   标题锚点按 GitHub 规则（小写、空格转 `-`、去掉标点，中文保留）。所有相对链接会被
   `go test ./docs` 检查，目标文件与标题必须存在。
5. **术语**：菜单与页面名称以 `app/workbench/web` 中各模块的 i18n 词条为准（中文文件用中文词条，
   英文文件用英文词条）；配置字段名一律用代码中的英文原名。
6. **篇幅**：每章中文 150–400 行为宜（反向代理、媒体库、助手、集群、隧道、LLM、数据同步与 BitTorrent 可更长）；
   英文与中文等长。
7. **不要**运行负载测试或基准（本仓库规则）；`./bin/fit validate` 与 `go test ./docs` 可以运行。
8. 资料来源中列出的文件是**权威**的：页面行为以 `app/workbench/web` 的代码为准，参数以
   `schema/doc` 与 `docs/reference` 为准，.conf 写法以 `templates/conf` 为准。

## 资料来源索引

| 资料 | 路径 |
| --- | --- |
| 控制台与 store（页面、登录、助手、MCP） | [../console.zh-CN.md](../console.zh-CN.md) |
| 配置（格式、加载、修改） | [../configuration.zh-CN.md](../configuration.zh-CN.md) |
| 配置参考（全部字段，代码生成） | [../reference/config.zh-CN.md](../reference/config.zh-CN.md) |
| .conf 格式 | [../reference/fit.zh-CN.md](../reference/fit.zh-CN.md) |
| 完整配置模板（全部字段带注释） | [../../templates/README.zh-CN.md](../../templates/README.zh-CN.md) |
| 部署（镜像、Helm、systemd） | [../../deploy/README.zh-CN.md](../../deploy/README.zh-CN.md) |
| 快速上手 | [../getting-started.zh-CN.md](../getting-started.zh-CN.md) |
| 集群管理 | [../cluster.zh-CN.md](../cluster.zh-CN.md) |
| 流媒体 | [../streaming.zh-CN.md](../streaming.zh-CN.md) |
| 媒体库 | [../media-library.zh-CN.md](../media-library.zh-CN.md) |
| 频道 | [../channels.zh-CN.md](../channels.zh-CN.md) |
| WebRTC | [../webrtc.zh-CN.md](../webrtc.zh-CN.md) |
| tunnel | [../tunnel.zh-CN.md](../tunnel.zh-CN.md) |
| llm | [../llm.zh-CN.md](../llm.zh-CN.md) |
| 数据同步 | [../sync.zh-CN.md](../sync.zh-CN.md) |
| BitTorrent | [../bittorrent.zh-CN.md](../bittorrent.zh-CN.md) |
| 模块文档（处理器、中间件、ACME 等） | [../../schema/doc/](../../schema/doc/) |
| 工作台前端（菜单与页面） | [../../app/workbench/web/](../../app/workbench/web/) |
