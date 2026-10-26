# 配置参考

[English](config.md) | [简体中文](config.zh-CN.md)

配置参考由 `schema.Reference` 从 fit 的配置类型和模块直接生成，与解码器和 JSON Schema 读取的是同一组类型和标签，因而不会与
fit 实际接受的配置脱节；每一章的说明和示例来自 `schema/doc`，示例由测试逐个加载验证。字段说明写在代码的 `doc` 标签里，是
英文的，所以完整的字段表只有 [英文版](config.md)；本页用中文说明每一章讲什么，并链接到英文版对应的位置。

配置可以写成 [.konf 文件](fit.zh-CN.md)、[YAML](yaml.zh-CN.md) 或 [JSON](json.zh-CN.md)，三者的字段都与参考完全相同，
也可以分散在多个文件中。参考中每一章的示例都依次给出 .konf、YAML 和 JSON 三种写法。
配置如何加载、检查和修改，见 [配置](../configuration.zh-CN.md)。

## 各章内容

| 章 | 讲什么 | 包含的对象 |
| --- | --- | --- |
| [The config](config.md#the-config) | 配置的顶层：各部分是什么，最小的配置 | 顶层字段 `schema_version`、`listeners`、`routes` 等 |
| [Listeners](config.md#listeners) | 监听地址和协议：`http/1.1`、`h2c`、`h2`、`h3`、`tcp`、`udp`、`socks`；TLS 证书（文件、ACME、按需申请）、客户端证书（mTLS）、PROXY 协议 | [`listeners[]`](config.md#config-listener)、[`tls`](config.md#config-tls)、[`certificates[]`](config.md#config-certificate)、[`client_auth`](config.md#config-clientauth)、[`socks`](config.md#config-forwardproxy)、[`proxy_protocol`](config.md#config-proxyprotocol) |
| [Routes](config.md#routes) | 路由：匹配条件、中间件和处理器；请求如何选中一条路由（优先级、host 和 path 的具体程度、匹配器数量、书写顺序）；`expr` 表达式 | [`routes[]`](config.md#config-route)、[`match`](config.md#config-match) |
| [Upstreams](config.md#upstreams) | 被代理的服务器：九种负载均衡策略、粘性 cookie、连接数和排队上限、各类超时、被动和主动健康检查、h2c 和 TLS | [`upstreams[]`](config.md#config-upstream)、[`sticky_cookie`](config.md#config-stickycookie)、[`health_check`](config.md#config-healthcheck) |
| [Services](config.md#services) | 四层代理：TCP 连接和 UDP 数据报，按 SNI 透传 TLS，向 upstream 发送 PROXY 协议 | [`services[]`](config.md#config-service) |
| [Log](config.md#log) | fit 自身的日志：按 logger（server、tls、provider、engine 等）分类，每个 logger 可单独设最低级别、输出（stdout、stderr 或文件）和格式（与 fib 相同的 line、text、json） | [`log`](config.md#config-log) |
| [Access log](config.md#access-log) | 每个请求一行的访问日志，格式为 JSON（默认）、text（logfmt）或 line，带有界缓冲；可按状态码对应的级别只记录 4xx/5xx | [`access_log`](config.md#config-accesslog) |
| [Telemetry](config.md#telemetry) | OpenTelemetry：链路追踪和指标，经 OTLP/HTTP 导出 | [`telemetry`](config.md#config-telemetry)、[`traces`](config.md#config-tracesexport)、[`metrics`](config.md#config-metricsexport) |
| [Limits](config.md#limits) | 所有 listener 的上限：连接数、头部和请求体大小、等待慢客户端的响应字节（默认为机器内存的一半）、写停顿超时 | [`limits`](config.md#config-limits) |
| [Engine](config.md#engine) | fib 网络引擎的设置：event loop 数量、worker 数、任务队列、读缓冲、单连接背压水位、backlog；不写的取 fib 的默认值 | [`engine`](config.md#config-engine) |
| [Admin API](config.md#admin-api) | 读取和替换运行中配置的 Admin API：地址、令牌、保留的版本数、上限、仪表盘 | [`admin`](config.md#config-admin) |
| [HTTPS by default](config.md#https-by-default) | 默认 HTTPS：不写 listener 时自动生成 443 和跳转用的 80；关闭、关闭跳转、改端口 | [`auto_https`](config.md#config-autohttps) |
| [Metrics](config.md#metrics) | 按 listener 和路由统计请求，供 Admin API 的 `/metrics` | [`metrics`](config.md#config-metrics) |
| [ACME](config.md#acme) | 证书的申请和续期：CA、账户邮箱、存储（目录，或 Redis、etcd、Consul 共享）、验证方式、按需申请 | [`acme`](config.md#config-acme)、[`shared_storage`](config.md#config-sharedstorage)、[`redis`](config.md#config-redisstorage)、[`etcd`](config.md#config-etcdstorage)、[`consul`](config.md#config-consulstorage)、[`on_demand`](config.md#config-ondemand) |
| [Providers](config.md#providers) | 从 Docker、Consul 目录、Consul KV、etcd、Kubernetes（Gateway API 和 Ingress）获取路由等配置，以及合并规则 | [`providers[]`](config.md#config-provider)、[`docker`](config.md#config-dockerprovider)、[`consul`](config.md#config-consulprovider)、[`consul_kv`](config.md#config-consulkvprovider)、[`etcd`](config.md#config-etcdprovider)、[`kubernetes`](config.md#config-kubernetesprovider)、[`ingress`](config.md#config-kubernetesingress) |
| [Handlers](config.md#handlers) | 路由的处理器，每个模块一节，含用途、示例和参数表 | [`fastcgi`](config.md#http-handlers-fastcgi)、[`forward_proxy`](config.md#http-handlers-forward_proxy)、[`proxy`](config.md#http-handlers-proxy)、[`respond`](config.md#http-handlers-respond)、[`static`](config.md#http-handlers-static) |
| [Middleware](config.md#middleware) | 路由的中间件，每个模块一节，含用途、示例和参数表 | [`basic_auth`](config.md#http-middleware-basic_auth)、[`body_limit`](config.md#http-middleware-body_limit)、[`cache`](config.md#http-middleware-cache)、[`circuit_breaker`](config.md#http-middleware-circuit_breaker)、[`client_cert`](config.md#http-middleware-client_cert)、[`compress`](config.md#http-middleware-compress)、[`concurrency_limit`](config.md#http-middleware-concurrency_limit)、[`forward_auth`](config.md#http-middleware-forward_auth)、[`headers`](config.md#http-middleware-headers)、[`jwt`](config.md#http-middleware-jwt)、[`rate_limit`](config.md#http-middleware-rate_limit)、[`redirect`](config.md#http-middleware-redirect)、[`rewrite`](config.md#http-middleware-rewrite)、[`wasm`](config.md#http-middleware-wasm) |

## 处理器和中间件

| 模块 | 用途 |
| --- | --- |
| `proxy` | 反向代理到 upstream，按权重分流、故障转移、镜像、重试 |
| `static` | 提供目录中的文件，目录列表、预压缩文件、`try_files` |
| `respond` | 固定的状态码、头部和响应体 |
| `fastcgi` | FastCGI 应用，如 PHP-FPM |
| `forward_proxy` | HTTP CONNECT 正向代理 |
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
