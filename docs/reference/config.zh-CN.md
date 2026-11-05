# 配置参考

[English](config.md) | [简体中文](config.zh-CN.md)

配置参考由 `schema.Reference` 从 fit 的配置类型和模块直接生成，与解码器和 JSON Schema 读取的是同一组类型和标签，因而不会与
fit 实际接受的配置脱节；每一章的说明和示例来自 `schema/doc`，示例由测试逐个加载验证。字段说明写在代码的 `doc` 标签里，是
英文的，所以完整的字段表只有 [英文版](config.md)；本页用中文说明每一章讲什么，并链接到英文版对应的位置。

配置可以写成 [.conf 文件](fit.zh-CN.md)、[YAML](yaml.zh-CN.md) 或 [JSON](json.zh-CN.md)，三者的字段都与参考完全相同，
也可以分散在多个文件中。参考中每一章的示例都依次给出 .conf、YAML 和 JSON 三种写法。
配置如何加载、检查和修改，见 [配置](../configuration.zh-CN.md)。

## 各章内容

| 章 | 讲什么 | 包含的对象 |
| --- | --- | --- |
| [The config](config.md#the-config) | 配置的顶层：各部分是什么，最小的配置 | 顶层字段 `schema_version`、`listeners`、`routes` 等 |
| [Listeners](config.md#listeners) | 监听地址和协议：`http/1.1`、`h2c`、`h2`、`h3`、`tcp`、`udp`、`socks`；`mptcp`（默认关闭）在内核支持时（Linux 5.6+、`net.mptcp.enabled`）以多路径 TCP（Multipath TCP）监听该监听器的 TCP 连接，不支持时回退到普通 TCP，没有多路径 TCP 的客户端也会回退，`udp` 监听器和 HTTP/3 没有该功能；TLS 证书（文件、ACME、按需申请）、客户端证书（mTLS）、PROXY 协议 | [`listeners[]`](config.md#config-listener)、[`tls`](config.md#config-tls)、[`certificates[]`](config.md#config-certificate)、[`client_auth`](config.md#config-clientauth)、[`socks`](config.md#config-forwardproxy)、[`proxy_protocol`](config.md#config-proxyprotocol)、[`mptcp`](config.md#config-listener) |
| [Routes](config.md#routes) | 路由：匹配条件、中间件和处理器；请求如何选中一条路由（优先级、host 和 path 的具体程度、匹配器数量、书写顺序）；`expr` 表达式；路由匹配前路径先规范化（去掉点段、合并重复斜杠，越过根的 `..` 及解码后构成空段或点段的 `%2F`、`%5C` 答 400），中间件和处理器拿到同一路径 | [`routes[]`](config.md#config-route)、[`match`](config.md#config-match) |
| [Upstreams](config.md#upstreams) | 被代理的服务器：九种负载均衡策略、粘性 cookie、连接数和排队上限、各类超时、被动和主动健康检查、h2c 和 TLS；`deny_internal` 拒绝连接环回、链路本地（云元数据）、未指定、组播和本机地址 | [`upstreams[]`](config.md#config-upstream)、[`sticky_cookie`](config.md#config-stickycookie)、[`health_check`](config.md#config-healthcheck) |
| [Services](config.md#services) | 四层代理：TCP 连接和 UDP 数据报，按 SNI 透传 TLS，向 upstream 发送 PROXY 协议 | [`services[]`](config.md#config-service) |
| [Log](config.md#log) | fit 自身的日志：按 logger（server、tls、provider、engine 等）分类，每个 logger 可单独设最低级别、输出（stdout、stderr 或文件）和格式（与 fib 相同的 line、text、json）；`time_zone` 决定行的时间与文件按 `interval` 切割所用时区（默认本地，可设 UTC 或 IANA 时区）；`rotate` 按大小/时间切割文件、压缩并按最大时间、数量、总大小清理旧的切割文件 | [`log`](config.md#config-log)、[`rotate`](config.md#config-rotate) |
| [Access log](config.md#access-log) | 每个请求一行的访问日志，格式为 JSON（默认）、text（logfmt）或 line，带有界缓冲；可按状态码对应的级别只记录 4xx/5xx；`time_zone` 决定行的时间（默认本地，可设 UTC 或 IANA 时区），`rotate` 与 `log` 的相同 | [`access_log`](config.md#config-accesslog)、[`rotate`](config.md#config-rotate) |
| [Telemetry](config.md#telemetry) | OpenTelemetry：链路追踪和指标，经 OTLP/HTTP 导出 | [`telemetry`](config.md#config-telemetry)、[`traces`](config.md#config-tracesexport)、[`metrics`](config.md#config-metricsexport) |
| [Limits](config.md#limits) | 所有 listener 的上限：连接数、头部和请求体大小、等待慢客户端的响应字节（默认为机器内存的一半）、写停顿超时 | [`limits`](config.md#config-limits) |
| [Engine](config.md#engine) | fib 网络引擎的设置：event loop 数量、worker 数、任务队列、读缓冲、单连接背压水位、backlog；不写的取 fib 的默认值 | [`engine`](config.md#config-engine) |
| [Admin API](config.md#admin-api) | 读取和替换运行中配置的 Admin API：地址、令牌、TLS（`cert_file`、`key_file`，非回环地址必需；`client_ca_file` 要求客户端证书；证书文件变化时重新读取）、保留的版本数、上限、仪表盘；`admin` 和 `console` 的 `mptcp`（默认关闭）在内核支持时以多路径 TCP 监听，Unix 套接字没有路径可用；加密保存配置的 store（`fit store export`、`fit store import`）；用户名密码登录、可编辑全部配置的 web 控制台（[控制台与 store](../console.zh-CN.md)、[决定 027](../decisions/027-console-and-store.zh-CN.md)） | [`admin`](config.md#config-admin)、[`admin.mptcp`](config.md#config-admin)、[`admin.console`](config.md#config-console)、[`admin.console.mptcp`](config.md#config-console)、[`admin.store`](config.md#config-store)、[`admin.console.users[]`](config.md#config-consoleuser) |
| [HTTPS by default](config.md#https-by-default) | 默认 HTTPS：不写 listener 时自动生成 443 和跳转用的 80；关闭、关闭跳转、改端口 | [`auto_https`](config.md#config-autohttps) |
| [Metrics](config.md#metrics) | 按 listener 和路由统计请求，供 Admin API 的 `/metrics` | [`metrics`](config.md#config-metrics) |
| [ACME](config.md#acme) | 证书的申请和续期：CA、账户邮箱、存储（目录，或 Redis、etcd、Consul 共享）、验证方式、按需申请 | [`acme`](config.md#config-acme)、[`shared_storage`](config.md#config-sharedstorage)、[`redis`](config.md#config-redisstorage)、[`etcd`](config.md#config-etcdstorage)、[`consul`](config.md#config-consulstorage)、[`on_demand`](config.md#config-ondemand) |
| [LLM](config.md#llm) | llm 的角色：hub、node（以本地或远端上游提供 AI 模型）、proxy（向本机或局域网的 agent 提供 OpenAI、Anthropic、Gemini、Ollama、Cohere 兼容接口）；WebSocket mesh、模型、代理超时、API key；网页修改写回配置（[决定 028](../decisions/028-llm-and-tunnel.zh-CN.md)、[llm](../llm.zh-CN.md)） | [`llm`](config.md#config-llm)、[`hub`](config.md#config-llmhub)、[`node`](config.md#config-llmnode)、[`proxy`](config.md#config-llmproxy)、[`console`](config.md#config-roleconsole) |
| [Tunnel](config.md#tunnel) | tunnel 的角色：hub、node（暴露其旁边的服务）、proxy（本地端口和 Web 代理经 hub 转发）；TCP、WebSocket、QUIC 链路；网页修改写回配置（[决定 028](../decisions/028-llm-and-tunnel.zh-CN.md)、[tunnel](../tunnel.zh-CN.md)） | [`tunnel`](config.md#config-tunnel)、[`hub`](config.md#config-tunnelhub)、[`node`](config.md#config-tunnelnode)、[`proxy`](config.md#config-tunnelproxy) |
| [WebRTC](config.md#webrtc) | 与网关一起运行的 WebRTC：TURN 服务器（fib 的），UDP、TCP 和 TLS 地址，`turn` 的 `mptcp`（默认关闭）在内核支持时以多路径 TCP 监听 `listen_tcp` 和 `listen_tls`，`listen` 的 UDP 套接字没有该功能，静态用户和 coturn 的限时凭据、中继地址和端口范围、配额、每个分配的许可和通道上限、带宽、允许和拒绝的对端（可带端口）；whip 和 whep 的媒体服务器（SFU）：共用的 UDP 端口、公网地址、告知对端的 ICE 服务器和 fit 自己的 TURN、PeerConnection、流、观看者、码率的上限和超时；webrtc_proxy 的媒体代理：一个 UDP 端口按 ICE ufrag 把对端中继到它的媒体服务器，会话和中继的上限、空闲超时、通往媒体服务器的套接字的源地址和端口范围；重载时哪些字段原地生效（[决定 029](../decisions/029-webrtc.zh-CN.md)、[WebRTC](../webrtc.zh-CN.md)） | [`webrtc`](config.md#config-webrtc)、[`turn`](config.md#config-turnserver)、[`turn.mptcp`](config.md#config-turnserver)、[`tls`](config.md#config-turntls)、[`users[]`](config.md#config-turnuser)、[`media`](config.md#config-webrtcmedia)、[`ice_servers[]`](config.md#config-iceserver)、[`proxy`](config.md#config-webrtcproxy) |
| [Media auth](config.md#media-auth) | 媒体认证策略：谁可以推送、播放哪些流；静态 token（明文或 SHA-256）、用户（明文密码或 bcrypt 哈希）、签发限时 token 的 `secret`、`secret_file` 或 `keys` 密钥环（每个密钥有自己的 secret 或 secret_file，轮换一个密钥只拒绝它签发的 token；`fit media-token`）、无需凭据即可进行的动作（`public`）、流名模式（`*` 通配）；whip、whep 和 webrtc_proxy 用 `auth` 按名称引用（[决定 030](../decisions/030-media-auth-and-pages.zh-CN.md)、[WebRTC](../webrtc.zh-CN.md#认证)） | [`media_auth[]`](config.md#config-mediaauth)、[`tokens[]`](config.md#config-mediatoken)、[`users[]`](config.md#config-mediauser)、[`keys[]`](config.md#config-mediakey) |
| [Streaming](config.md#streaming) | 流媒体角色：fib 的媒体 hub（流、每路流的播放者、GOP 缓存的上限），RTMP、RTSP（Digest、Basic 或查询参数 token；RTP 交织在 TCP 上）、SRT（stream ID 的 `s=`、`u=` 或资源的查询参数；加密）监听器及其连接上限和超时，`rtmp` 和 `rtsp` 的 `mptcp`（默认关闭）在内核支持时以多路径 TCP 监听，不支持时回退到普通 TCP，`stream_whip` 和 `stream_whep` 的 WebRTC 端口；每个监听器以 `auth` 引用 media_auth 策略；重载时 `auth` 原地生效（[决定 030](../decisions/030-media-auth-and-pages.zh-CN.md)、[流媒体](../streaming.zh-CN.md)） | [`streaming`](config.md#config-streaming)、[`rtmp`](config.md#config-streamrtmp)、[`rtmp.mptcp`](config.md#config-streamrtmp)、[`rtsp`](config.md#config-streamrtsp)、[`rtsp.mptcp`](config.md#config-streamrtsp)、[`srt`](config.md#config-streamsrt)、[`webrtc`](config.md#config-streamwebrtc) |
| [Providers](config.md#providers) | 从 Docker、Consul 目录、Consul KV、etcd、Kubernetes（Gateway API 和 Ingress）获取路由等配置，以及合并规则；Kubernetes 的 ExternalName Service 后端默认不解析，需 `allow_external_name_services` 打开，且仍拒绝环回、链路本地（云元数据）、未指定、组播和本机地址 | [`providers[]`](config.md#config-provider)、[`docker`](config.md#config-dockerprovider)、[`consul`](config.md#config-consulprovider)、[`consul_kv`](config.md#config-consulkvprovider)、[`etcd`](config.md#config-etcdprovider)、[`kubernetes`](config.md#config-kubernetesprovider)、[`ingress`](config.md#config-kubernetesingress) |
| [Handlers](config.md#handlers) | 路由的处理器，每个模块一节，含用途、示例和参数表；`static` 和 `fastcgi` 默认对含有以点开头的段的路径（如 `/.git/config`、`/.env`，`/.well-known/` 除外）答 404，Windows 上形如 8.3 短文件名的段（如 `.git` 的 `GIT~1`）也一样，`dotfiles: allow` 才提供 | [`fastcgi`](config.md#http-handlers-fastcgi)、[`forward_proxy`](config.md#http-handlers-forward_proxy)、[`media_pages`](config.md#http-handlers-media_pages)、[`media_tokens`](config.md#http-handlers-media_tokens)、[`proxy`](config.md#http-handlers-proxy)、[`respond`](config.md#http-handlers-respond)、[`static`](config.md#http-handlers-static)、[`webrtc_proxy`](config.md#http-handlers-webrtc_proxy)、[`whep`](config.md#http-handlers-whep)、[`whip`](config.md#http-handlers-whip)、[`hls`](config.md#http-handlers-hls)、[`httpflv`](config.md#http-handlers-httpflv)、[`stream_whep`](config.md#http-handlers-stream_whep)、[`stream_whip`](config.md#http-handlers-stream_whip) |
| [Middleware](config.md#middleware) | 路由的中间件，每个模块一节，含用途、示例和参数表 | [`basic_auth`](config.md#http-middleware-basic_auth)、[`body_limit`](config.md#http-middleware-body_limit)、[`cache`](config.md#http-middleware-cache)、[`circuit_breaker`](config.md#http-middleware-circuit_breaker)、[`client_cert`](config.md#http-middleware-client_cert)、[`compress`](config.md#http-middleware-compress)、[`concurrency_limit`](config.md#http-middleware-concurrency_limit)、[`forward_auth`](config.md#http-middleware-forward_auth)、[`headers`](config.md#http-middleware-headers)、[`jwt`](config.md#http-middleware-jwt)、[`rate_limit`](config.md#http-middleware-rate_limit)、[`redirect`](config.md#http-middleware-redirect)、[`rewrite`](config.md#http-middleware-rewrite)、[`wasm`](config.md#http-middleware-wasm) |

## 处理器和中间件

| 模块 | 用途 |
| --- | --- |
| `proxy` | 反向代理到 upstream，按权重分流、故障转移、镜像、重试 |
| `static` | 提供目录中的文件，目录列表、预压缩文件、`try_files` |
| `respond` | 固定的状态码、头部和响应体 |
| `fastcgi` | FastCGI 应用，如 PHP-FPM |
| `forward_proxy` | HTTP CONNECT 正向代理；不论 `allow` 写了什么，环回、链路本地（含云元数据服务 `169.254.169.254`）、未指定和组播地址以及本机自己的地址都被拒绝，除非 `allow` 用落在其范围之内的范围（如 `127.0.0.0/8`，`0.0.0.0/0` 不算）或单独写出的本机地址点名它们；监听器的 `socks` 相同 |
| `basic_auth` | 用户名和密码（bcrypt） |
| `body_limit` | 限制请求体大小 |
| `cache` | HTTP 缓存，内存和磁盘两级 |
| `circuit_breaker` | 熔断 |
| `client_cert` | 要求客户端证书 |
| `compress` | zstd、brotli、gzip 压缩 |
| `concurrency_limit` | 限制并发请求数 |
| `forward_auth` | 询问认证服务 |
| `headers` | 设置、追加、删除请求和响应头 |
| `jwt` | 校验 JSON Web Token |
| `rate_limit` | 限流 |
| `redirect` | 跳转 |
| `rewrite` | 改写路径 |
| `wasm` | WebAssembly 插件 |

## 如何阅读字段表

每张表的列是：字段名、类型（`duration` 是如 `"1.5s"`、`"2m"` 的字符串；`bytes or percent` 是字节数（整数），或 fit 可用内存的百分比（如 `"50%"` 的字符串）；整数会注明范围；`object` 链接到它自己的一节）、
默认值（不写时取的值），以及说明。标为 `Required` 的字段必须写出。标为 `Deprecated` 的是重命名前的旧名字，仍可使用，
但会得到警告，见 [兼容性](../compatibility.zh-CN.md)。没有字段的对象（如 `metrics`）写 `{}` 即可开启。

用 `go test ./schema -update` 重新生成；内容过期、有字段缺少说明或某个示例加载失败时，`schema` 包的测试会失败。
`fit schema -reference` 打印当前二进制（包括 `fit build` 编入的模块）的参考。
