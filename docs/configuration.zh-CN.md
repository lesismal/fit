# 配置

[English](configuration.md) | [简体中文](configuration.zh-CN.md)

fit 从一个或多个文件读取配置，可以是三种格式之一：**.conf** 文件、**YAML** 或 **JSON**。三者逐字段表达相同的内容：fit
监听的地址、为每个请求选择应答者的路由、请求被代理到的 upstream，日志、限制、证书和 Admin API 的设置，以及 fit 在网关旁边运行的角色：llm、tunnel 和 WebRTC。Admin API
收发的是 JSON；三种格式的字段完全相同，彼此之间都可以转换。

| 格式 | 适合 | 参考 |
| --- | --- | --- |
| .conf | 手写的配置：像 nginx 和 Caddy 那样的指令块，支持站点 | [.conf 参考](reference/fit.zh-CN.md) |
| YAML | 手写或工具生成的配置，可用锚点复用重复内容 | [YAML 参考](reference/yaml.zh-CN.md) |
| JSON | Admin API 收发的格式，工具生成的格式 | [JSON 参考](reference/json.zh-CN.md) |

每个字段的类型、默认值和含义，以及三种格式的示例，见 [配置参考](reference/config.zh-CN.md)。[配置模板](../templates/README.zh-CN.md)
为每种格式提供一份包含全部字段的完整配置，可复制后修改。

## 三种格式

### .conf

fit 自己的格式：每个字段一条指令，每个对象一个块，用 `site` 把一组域名的路由写在一起。以 `.conf` 结尾的文件。

```fit
upstream api {
	addresses 127.0.0.1:9000
}

# 以 HTTPS 提供，自动为 example.com 申请证书。
site example.com {
	middleware compress
	route /api/* {
		handler proxy { upstream api }
	}
	route {
		handler static { root /srv/www }
	}
}
```

语法、各类字段的写法、站点和多个文件，见 [.conf 参考](reference/fit.zh-CN.md)。

### YAML

用 YAML 语法写的 fit 配置：可写注释，大多数值无需引号和括号，重复内容可用锚点和别名。以 `.yaml` 或 `.yml` 结尾的文件。

```yaml
# 同一个站点。
upstreams:
  - name: api
    addresses: ["127.0.0.1:9000"]
routes:
  - match: {host: [example.com], path: ["/api/*"]}
    middleware: [{module: compress}]
    handler: {module: proxy, params: {upstream: api}}
  - match: {host: [example.com]}
    middleware: [{module: compress}]
    handler: {module: static, params: {root: /srv/www}}
```

YAML 如何对应到配置，以及需要注意的地方，见 [YAML 参考](reference/yaml.zh-CN.md)。

### JSON

fit 的原生格式，严格读取：未知字段、类型错误的值、重复的键都是错误，并给出行号和列号。允许 `//` 和 `/* */` 注释，以及
最后一个元素后的逗号。以 `.json` 结尾的文件，以及单独指定的其他文件，都按 JSON 读取，默认是 `fit.json`。

```json
{
  // 同一个站点。
  "upstreams": [{"name": "api", "addresses": ["127.0.0.1:9000"]}],
  "routes": [
    {"match": {"host": ["example.com"], "path": ["/api/*"]},
     "middleware": [{"module": "compress"}],
     "handler": {"module": "proxy", "params": {"upstream": "api"}}},
    {"match": {"host": ["example.com"]},
     "middleware": [{"module": "compress"}],
     "handler": {"module": "static", "params": {"root": "/srv/www"}}},
  ],
}
```

更多内容见 [JSON 参考](reference/json.zh-CN.md)。

## 配置包含什么

| 部分 | 是什么 | 参考 |
| --- | --- | --- |
| `listeners` | 地址和协议；不写时由默认 HTTPS 生成；每个监听器可在内核支持时选用多路径 TCP（Multipath TCP），默认关闭 | [Listeners](reference/config.md#listeners) |
| `routes` | 每类请求的匹配条件、中间件和处理器 | [Routes](reference/config.md#routes) |
| `upstreams` | 被代理的服务器，带负载均衡和健康检查 | [Upstreams](reference/config.md#upstreams) |
| `services` | TCP 和 UDP 代理 | [Services](reference/config.md#services) |
| 处理器 | `proxy`、`static`、`respond`、`fastcgi`、`forward_proxy`；WebRTC 用的 `whip`、`whep` 和 `webrtc_proxy`；流媒体用的 `httpflv`、`hls`、`stream_whip` 和 `stream_whep`；媒体库用的 `vod` | [Handlers](reference/config.md#handlers) |
| 中间件 | 认证、限流、缓存、压缩、头部、改写等 | [Middleware](reference/config.md#middleware) |
| `log`、`access_log`、`telemetry`、`metrics`、`pprof` | 日志、链路追踪、指标和运行时性能剖析 | [Log](reference/config.md#log)、[Access log](reference/config.md#access-log)、[Telemetry](reference/config.md#telemetry)、[Metrics](reference/config.md#metrics)、[Profiling](reference/config.md#pprof) |
| `limits` | 连接、头部、请求体和内存的上限 | [Limits](reference/config.md#limits) |
| `auto_https`、`acme` | 默认 HTTPS 和证书 | [HTTPS by default](reference/config.md#https-by-default)、[ACME](reference/config.md#acme) |
| `engine` | fib 的事件循环、worker 和缓冲区 | [Engine](reference/config.md#engine) |
| `admin` | Admin API；加密保存配置的 store（`store`）；控制台（`console`） | [Admin API](reference/config.md#admin-api)、[控制台](console.zh-CN.md) |
| `llm` | llm 的 hub、node 和 proxy，在多台机器间共享 AI 模型并提供给 agent 调用 | [LLM](reference/config.md#llm)、[llm](llm.zh-CN.md) |
| `tunnel` | tunnel 的 hub、node 和 proxy，用于访问 NAT 后的服务 | [Tunnel](reference/config.md#tunnel)、[tunnel](tunnel.zh-CN.md) |
| `webrtc` | TURN 服务器（`turn`）、`whip` 和 `whep` 的媒体端口（`media`）、`webrtc_proxy` 的转发（`proxy`） | [WebRTC](reference/config.md#webrtc)、[WebRTC](webrtc.zh-CN.md) |
| `streaming` | 流媒体角色：带 RTMP、RTSP、SRT 监听器以及 `stream_whip` 和 `stream_whep` 的 WebRTC 端口的媒体 hub；由媒体库条目组成的频道，以及推流目标 | [Streaming](reference/config.md#streaming)、[流媒体](streaming.zh-CN.md)、[频道](channels.zh-CN.md) |
| `media_library` | 媒体库：把媒体文件所在的目录编目成条目，及其版本、音轨、字幕和海报 | [Media library](reference/config.md#media-library)、[媒体库](media-library.zh-CN.md) |
| `sync` | 同步角色：从源读取记录并写入目标的流水线，只在所有目标都确认后才保存检查点；其连接器是 `sync.sources`、`sync.sinks` 和 `sync.transforms` 中的模块 | [Sync](reference/config.md#sync)、[决定 036](decisions/036-sync.zh-CN.md) |
| `providers` | Docker、Consul、etcd 和 Kubernetes | [Providers](reference/config.md#providers) |
| `schema_version` | 配置的版本，1；可不写 | [兼容性](compatibility.zh-CN.md#schema-的版本) |

## 多个文件和目录

一份配置可以分散在多个文件（任一种格式）和目录中，每个用一个 `-c` 给出：

```bash
fit run -c /etc/fit/base.conf -c /etc/fit/sites
```

目录代表其中（包括子目录中）的 `.conf`、`.yaml`、`.yml` 和 `.json` 文件，按字典序，名字以 `.` 开头的除外。配置顶层的列表
`listeners`、`routes`、`upstreams`、`services` 和 `providers` 按文件顺序拼接；其他字段只能在一个文件中给出。错误会指出
所在的文件。详见 [多个文件和目录](reference/fit.zh-CN.md#多个文件和目录)。`.conf` 文件自己也可以引用文件：`import` 像 `-c`
一样合并完整的 config 文件，`include` 在任意块中原地读入一个文件的指令（[import 与 include](reference/fit.zh-CN.md#import-与-include)）。按种类分文件（运行规格、listener、证书、
可观测性、四层服务、provider，每个站点一个文件）的完整目录模板见 [配置模板](../templates/README.zh-CN.md#目录形式)，
也可用 `fit schema -template fit -dir <目录>` 生成。

## 运行、检查和转换

格式由每个文件的名字判断；三种格式的命令相同：

```bash
fit run -c fit.conf
```

```bash
fit validate -c fit.yaml -c conf.d
```

`fit validate` 像 `fit run` 那样加载配置，并检查每个模块的参数，每个错误都给出文件、行号和列号，指向原始文件中的位置，
无论哪种格式。`fit adapt` 在格式之间转换，并可把多个文件合并为一个：

| 从 | 到 | 命令 |
| --- | --- | --- |
| JSON 或 YAML | .conf | `fit adapt -c fit.json -to fit -o fit.conf` |
| .conf 或 YAML | JSON | `fit adapt -c fit.conf -o fit.json` |
| .conf 或 JSON | YAML | `fit adapt -c fit.conf -to yaml -o fit.yaml` |
| 一个目录 | 一个文件 | `fit adapt -c conf.d -to fit -o fit.conf` |

`fit schema` 打印配置的 JSON Schema，编辑器可以用它在编写 JSON 和 YAML 时补全和检查；`fit schema -template fit` 打印
.conf 模板。`fit hash-password` 在终端中提示输入两次密码（输入显示为 `*`，非终端时从标准输入读取）并打印其 bcrypt 哈希，用于 `basic_auth` 的用户、控制台的用户和 `media_auth` 的用户。
`fit media-token -c fit.conf -policy live -publish 'live/*' -play '*' -ttl 1h -subject obs` 打印一个用
[`media_auth`](reference/config.md#media-auth) 策略的 `secret` 签名的 token，供 WHIP 或 WHEP 客户端作为 bearer token 发送；
使用同一 secret 的运行中的 fit 在其过期前都接受它。`fit token create -c fit.conf -role tunnel.hub -name lab -ttl 90d`
经 Admin API 向运行中的 fit 申请一个由其[隧道 hub](tunnel.zh-CN.md#令牌)或 [LLM 角色](llm.zh-CN.md#令牌)签发的连接
令牌，并打印它，只此一次；`fit token list`、`expiry` 和 `revoke` 列出令牌及其最近使用、修改某个令牌的过期时间、吊销某个令牌。

## 一个地址，一个服务器

fib 的引擎以 SO_REUSEPORT 监听，让每个事件循环各在自己的套接字上接受连接；这样内核也会让同一用户的第二个服务器
无错误地监听同一地址，并把连接分给两者。fit 不让这种情况发生：

- **在配置中。** 加载配置时（`fit validate`、`fit run`、重载、Admin API 或角色的页面），两处在同一地址和同一传输上
  监听的配置会被拒绝，错误中写明两者：两个监听器；监听器与 Admin API 或 `admin.console`；角色的页面、链路监听器或
  API（`llm`）；隧道 hub 的监听器和 `console_socket`，隧道 proxy 的转发和网页代理；流媒体的 RTMP、RTSP、SRT 和
  WebRTC 端口；TURN 服务器的 `listen`、`listen_tcp` 和 `listen_tls`，`webrtc.media` 和 `webrtc.proxy`。
  `:8080`、`0.0.0.0:8080` 和 `[::]:8080` 是同一个地址，即所有地址，也包含 `127.0.0.1:8080`；`localhost` 即
  `127.0.0.1`。同一号码的 TCP 端口和 UDP 端口不冲突，所以 HTTP 和 HTTP/3 可共用一个端口；由内核挑选的 0 端口
  从不冲突。

  ```text
  tunnel.proxy.web_proxies[0].listen: listener "web" listens on TCP :8080 already, the same address as 0.0.0.0:8080; two servers on one address would share its connections, so give one of them another port
  ```

- **绑定时。** 每个监听器和角色在绑定之前，先以普通绑定检查是否有其他套接字占着它的地址（已在那里的套接字会拒绝
  这种绑定）：别的进程的，或之后被某个角色的页面改到那里的。若有，它以 `TCP 127.0.0.1:8080 is taken: another
  process, or another part of fit, listens there` 失败，配置的其余部分照常运行，与任何无法绑定的地址一样。

## 修改运行中的配置

无论哪种格式，`fit run` 在收到 SIGHUP 时，或使用 `-watch` 时每当某个文件变化、目录中加入或移除文件，都会重新应用配置。
加载或编译失败的配置会被拒绝，运行中的配置保持不变。[Admin API](reference/config.md#admin-api) 以 JSON 读取和替换运行中的
配置，可以整体替换，也可以按 JSON pointer 修改，并保留版本以便回滚。任何带 `?dry_run=1` 的修改都会走完整个应用流程
（加载、编译、试绑新监听的地址）接受检查，应答与应用时相同，但什么都不应用；`GET /node` 说明本 fit 是谁（节点 ID、版本、
运行中的配置与 API 的能力），`GET /schema` 给出它接受的配置的 JSON Schema，供管理其他 fit 的 fit 使用
（[决定 033](decisions/033-cluster-management.zh-CN.md)）。

设置 `admin.store` 后，配置加密保存在单独的文件 store 中，fit 从它运行：启动 fit 的文件给出 `admin`，其中其余的内容只是
store 的初始内容，仅在 store 还不存在时使用。每个被应用的配置，无论来自 Admin API、控制台还是某个角色的页面，都会写入
store；SIGHUP 重新读取 store，`-watch` 会被拒绝。`fit store export -c fit.conf` 以 JSON 打印 store 中的配置，
`fit store import -c fit.conf new.json` 检查一份配置并把它封存为 store，替换原有内容。`admin.console` 提供需要登录的网页，
可以修改配置中 `admin` 以外的所有部分。详见 [控制台](console.zh-CN.md)。

## 从其他服务器迁移

`fit adapt` 可以把 nginx 配置或 Caddyfile 转换为 fit 的配置，作为起点，并列出未转换的部分：
`fit adapt -from nginx -c nginx.conf -to fit -o fit.conf`。[nginx](migrating-from-nginx.zh-CN.md)、[Caddy](migrating-from-caddy.zh-CN.md)
和 [Traefik](migrating-from-traefik.zh-CN.md) 的迁移指南给出同一套配置在各服务器和 fit 中的写法，以及各部分如何对应到 fit。
