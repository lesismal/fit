# 反向代理

[English](15-reverse-proxy.md) | [简体中文](15-reverse-proxy.zh-CN.md)

## 功能

反向代理是 fit 的核心：**路由**说哪些请求归它，请求按顺序穿过**中间件**，最后由**处理器**应答；
处理器最常见的一个把请求交给**上游**——一组按名字引用的服务器。上游决定请求怎么分（负载均衡）、
哪些地址可用（被动与主动健康检查）、以及连接与超时的上限。

一条请求只走一条路由：先是它所在的监听中匹配它的路由——`match` 的每个字段都要成立，字段内则
列出的值中有一个成立即可；其中 `priority` 最高的先选；同优先级时按 host 的具体程度（精确、`*.`、
`**.`、无），再按 path 的具体程度（精确、最长的前缀），再按设了多少其他匹配器，最后按书写顺序。
没有路由接住的请求答 404。路径在任何路由看到它之前先规范化。

反向代理模块有两页：**路由**和**上游**。处理器与中间件是模块，按 `module` 命名，带各自的 `params`；
下面列出全部模块，各链接到参考的一章。

## 页面与操作

### 路由（Routes）

`#/gw.reverse/routes`。本页编辑配置的 `routes` 成员，页面上方的提示写着：路由匹配请求并交给处理器：
proxy 转发到上游，static 提供静态文件，respond 直接应答。正向代理路由在其单独的页面中。

- **按处理器的模块分标签**：**全部** · 条数，然后是每个出现过的模块 · 条数。从[引擎](06-engine.zh-CN.md)菜单
  的某个处理器进来时（`?handler=<module>`）会直接落到那个标签；之后点别的标签以点击的为准。
- **每一行**显示路由的名字与它的匹配：处理器的模块作为一个标记，前两个 host、前两个 path、方法、
  `→ <upstream>`、`@<listeners>`；行末是**问 AI**按钮，以技能 `diagnose` 诊断这条路由——它是否正常应答，
  是否失败或变慢（没有助手时不显示）。
- **添加路由**新建一行：默认 `name` 为 `route-N`、`handler` 为 `proxy`（草稿里已有上游时带上第一个的
  名字）。每行可**删除**、**上移**、**下移**。
- 一条路由的表单字段是 `name`、`listeners`、`priority`、`match`（`host`、`path`、`path_regexp`、`method`、
  `header`、`query`、`remote_ip`、`expr`）、`access`、`middleware`（按顺序的一组模块）与 `handler`。
- **`.conf`、`.yaml`、`.json` 三个标签**编辑的是整个 `routes` 成员——包括[正向代理](16-forward-proxy.zh-CN.md)
  页编辑的正向代理路由，而不只是本页显示的那些；输入的文本随输入解析进同一份草稿，**保存并应用**时
  整体校验后应用。

### 上游（Upstreams）

`#/gw.reverse/upstreams`。本页编辑配置的 `upstreams` 成员。

- **每一行**显示上游的名字、`{n} 个地址`、`load_balancing` 的标记，以及 `{up} / {all} 可用`：可用与总数
  来自 `GET /status`，全可用是绿色，部分可用是黄色，全不可用是红色。有后端不可用时行末是**问 AI**按钮，
  以技能 `diagnose` 问原因与该修什么。健康状态只是附加信息，读不到时行仍然显示。
- **添加上游**新建一行：默认 `name` 为 `backend-N`、`addresses` 为 `127.0.0.1:8081`。每行可**删除**、
  **上移**、**下移**。
- 表单的字段是 `name`、`addresses`、`weights`、`load_balancing`、`hash_key`、`sticky_cookie`、各上限与
  超时、`deny_internal`、`protocol`（`http/1.1`、`h2c`、`h2`）与 `health_check`。
- 与所有编辑配置的页面一样，本页共用同一份草稿，也有 `.conf`、`.yaml`、`.json` 三个标签。

## 参数

只列关键参数；完整字段表见[配置参考：Routes](../reference/config.md#routes)与
[Upstreams](../reference/config.md#upstreams)，各对象另有其表：
[`routes[]`](../reference/config.md#config-route)、
[`routes[].match`](../reference/config.md#config-match)、
[`upstreams[]`](../reference/config.md#config-upstream)、
[`upstreams[].sticky_cookie`](../reference/config.md#config-stickycookie)、
[`upstreams[].health_check`](../reference/config.md#config-healthcheck)。

### 路由

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `name` | 无 | 路由之间唯一的名字，供日志与指标。 |
| `listeners` | 所有监听 | 这条路由服务的监听的名字。 |
| `priority` | 无（0） | 优先级高的先选；同优先级时按 host、path 的具体程度，再按其他匹配器的数量，再按书写顺序。 |
| `match` | 所有请求 | 请求必须匹配什么：`host`（精确或 `*.`、`**.` 通配）、`path`（精确或以 `*` 结尾的前缀）、`path_regexp`、`method`、`header`、`query`、`remote_ip`（连接的地址，不是 X-Forwarded-For）、`expr`（用 `&&`、`\|\|`、`!` 与括号组合匹配器）。 |
| `access` | 无 | 这条路由服务的客户端地址，先于其监听与全局的 `access` 决定；被拒绝的请求答 403。 |
| `middleware` | 无 | 请求在处理器之前按顺序穿过的中间件；中间件也可以自己应答。 |
| `handler` | 必填 | 应答请求的模块。 |

### 上游

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `name` | 必填 | 上游之间唯一的名字，处理器用它引用。 |
| `addresses` | 必填 | 上游的服务器地址，如 `10.0.0.1:8080` 或 `backend:80`。 |
| `weights` | 每个 1 | 按 `addresses` 顺序的权重：`weighted_round_robin` 下的份额，以及 `least_conn` 下与权重 1 一样忙所需的请求数。 |
| `load_balancing` | `round_robin`（写了 `weights` 时 `weighted_round_robin`） | 请求如何分给各地址，见下面九种策略。 |
| `hash_key` | 无 | `header_hash` 的头部与 `cookie_hash` 的 cookie 的名字。 |
| `sticky_cookie` | 无 | 用 cookie 把每个客户端固定在一个地址上：`name` 默认 `fit_sticky`，`path` 默认 `/`，`max_age`、`secure`。 |
| `max_conns` | `256` | 到一个地址最多打开（或正在打开）的连接数；超过的请求等一个空出来。 |
| `max_pending` | `1024` | `max_conns` 占满后还能等待的请求数；超过的立刻答 503。 |
| `max_idle_conns` | `32` | 为一个地址保留的空闲连接数；0 不保留。 |
| `idle_timeout` | `60s` | 空闲连接保留多久后关闭。 |
| `connect_timeout` | `10s` | 建立连接最多多久；超时的请求答 504。 |
| `response_header_timeout` | `60s` | 请求发出后上游开始应答最多多久；超过答 504。 |
| `response_idle_timeout` | `60s` | 响应体中间上游可以沉默多久。 |
| `response_timeout` | `1h` | 整个响应最多多久；开始前超过答 504，之后超过被截断。 |
| `max_fails` | `1` | `fail_timeout` 内多少次失败请求把一个地址取出轮转（被动健康检查）；0 不取出。 |
| `fail_timeout` | `10s` | 计数的窗口，也是地址被取出的时长。 |
| `health_check` | 无 | 主动健康检查：按 `interval` 探测每个地址（`http` 发 GET，`tcp` 只需连上），`fails` 次连续失败取出、`passes` 次连续通过放回；`timeout` 默认 `2s`，`jitter` 默认 `interval` 的十分之一。 |
| `deny_internal` | 关闭 | 拒绝连接环回、链路本地（`169.254.169.254`，多数云的元数据服务）、未指定、组播和本机自己的地址；被拒绝的请求答 502。 |
| `protocol` | `http/1.1` | 请求如何到达上游：`http/1.1`、`h2c`，或 `h2`（TLS，配 `tls_server_name`、`tls_ca_file`、`tls_cert_file`、`tls_key_file`）。 |

### 九种负载均衡策略

- `round_robin`：依次轮流。
- `weighted_round_robin`：按权重依次轮流，平滑地，如 nginx 那样。
- `least_conn`：交给按其权重算请求最少的地址。
- `random`：按权重随机。
- `p2c`：随机挑两个地址，交给其中按其权重算较不忙的那个。
- `ip_hash`：按客户端地址的一致哈希，同一客户端总到同一地址。
- `uri_hash`：按请求的路径与查询的一致哈希。
- `header_hash`：按 `hash_key` 所指头部的一致哈希。
- `cookie_hash`：按 `hash_key` 所指 cookie 的一致哈希。

后四种在地址可用期间把同一个 key 送到同一地址；增删一个地址只移动原本归它或会归它的 key。没有该头部
或 cookie 的请求依次轮流。

## 处理器与中间件模块

### 处理器

| 模块 | 做什么 |
| --- | --- |
| [`proxy`](../reference/config.md#http-handlers-proxy) | 反向代理到上游：按权重分流、故障转移、镜像、重试 |
| [`static`](../reference/config.md#http-handlers-static) | 提供目录中的文件：目录列表、预压缩文件、`try_files` |
| [`respond`](../reference/config.md#http-handlers-respond) | 固定的状态码、头部和响应体 |
| [`fastcgi`](../reference/config.md#http-handlers-fastcgi) | FastCGI 应用，如 PHP-FPM |
| [`media_pages`](../reference/config.md#http-handlers-media_pages) | 提供 fit 自己的媒体页面：索引、通话、发布与播放页 |
| [`media_tokens`](../reference/config.md#http-handlers-media_tokens) | 为业务后端批量签发限时的 media_auth 令牌 |
| [`hls`](../reference/config.md#http-handlers-hls) | 播放流媒体角色的流，HLS 切片 |
| [`httpflv`](../reference/config.md#http-handlers-httpflv) | 以 HTTP-FLV 与 WebSocket-FLV 播放 |
| [`whip`](../reference/config.md#http-handlers-whip) | 以 WHIP 把 WebRTC 流发布到 fit 的媒体服务器 |
| [`whep`](../reference/config.md#http-handlers-whep) | 以 WHEP 播放 fit 媒体服务器的流 |
| [`stream_whip`](../reference/config.md#http-handlers-stream_whip) | 以 WHIP 发布到流媒体角色的 hub |
| [`stream_whep`](../reference/config.md#http-handlers-stream_whep) | 以 WHEP 播放流媒体角色的流 |
| [`webrtc_proxy`](../reference/config.md#http-handlers-webrtc_proxy) | 把 WHIP 与 WHEP 代理到别的媒体服务器，按 ICE ufrag 中继 |
| [`forward_proxy`](../reference/config.md#http-handlers-forward_proxy) | HTTP CONNECT 正向代理，在[正向代理](16-forward-proxy.zh-CN.md)页编辑 |

各模块的详细文档在 `schema/doc/modules/`，如 [proxy.md](../../schema/doc/modules/proxy.md)、
[static.md](../../schema/doc/modules/static.md)、[respond.md](../../schema/doc/modules/respond.md)、
[fastcgi.md](../../schema/doc/modules/fastcgi.md)、
[media_pages.md](../../schema/doc/modules/media_pages.md)、
[media_tokens.md](../../schema/doc/modules/media_tokens.md)、
[hls.md](../../schema/doc/modules/hls.md)、[httpflv.md](../../schema/doc/modules/httpflv.md)、
[whip.md](../../schema/doc/modules/whip.md)、[whep.md](../../schema/doc/modules/whep.md)、
[stream_whip.md](../../schema/doc/modules/stream_whip.md)、
[stream_whep.md](../../schema/doc/modules/stream_whep.md)、
[webrtc_proxy.md](../../schema/doc/modules/webrtc_proxy.md)、
[forward_proxy.md](../../schema/doc/modules/forward_proxy.md)。`vod` 在[媒体库](22-media-library.zh-CN.md)一章。

### 中间件

| 模块 | 做什么 |
| --- | --- |
| [`basic_auth`](../reference/config.md#http-middleware-basic_auth) | 用户名和密码（bcrypt 哈希） |
| [`body_limit`](../reference/config.md#http-middleware-body_limit) | 限制请求体大小，超过答 413 |
| [`cache`](../reference/config.md#http-middleware-cache) | HTTP 缓存，内存与磁盘两级，按 `zone` 共享 |
| [`circuit_breaker`](../reference/config.md#http-middleware-circuit_breaker) | 失败或过慢的请求太多时先答 503，再放行几个试 |
| [`client_cert`](../reference/config.md#http-middleware-client_cert) | 要求客户端证书，并把其 subject 传下去 |
| [`compress`](../reference/config.md#http-middleware-compress) | zstd、brotli、gzip，按客户端偏好 |
| [`concurrency_limit`](../reference/config.md#http-middleware-concurrency_limit) | 限制同时在途的请求数 |
| [`forward_auth`](../reference/config.md#http-middleware-forward_auth) | 问认证服务可不可以，并把它答案中的字段传下去 |
| [`headers`](../reference/config.md#http-middleware-headers) | 设置、追加、删除请求与响应头 |
| [`jwt`](../reference/config.md#http-middleware-jwt) | 校验 JSON Web Token，并把 claims 作为头部传下去 |
| [`rate_limit`](../reference/config.md#http-middleware-rate_limit) | 按客户端地址、头部或整条路由限流，允许突发 |
| [`redirect`](../reference/config.md#http-middleware-redirect) | 跳转，可按正则 |
| [`rewrite`](../reference/config.md#http-middleware-rewrite) | 改写路径：去掉或加上前缀，或替换正则匹配的部分 |
| [`wasm`](../reference/config.md#http-middleware-wasm) | 在每个请求上运行 WebAssembly 插件 |

各模块的详细文档在 `schema/doc/modules/`，如
[cache.md](../../schema/doc/modules/cache.md)、[jwt.md](../../schema/doc/modules/jwt.md)、
[rate_limit.md](../../schema/doc/modules/rate_limit.md)、
[forward_auth.md](../../schema/doc/modules/forward_auth.md)、
[wasm.md](../../schema/doc/modules/wasm.md)。

## .conf 配置

```fit
listener web :8080 http/1.1 h2c

upstream app {
	addresses 10.0.0.1:8080 10.0.0.2:8080
	weights 3 1
	load_balancing weighted_round_robin
	sticky_cookie {
		name fit_sticky
		max_age 24h
		secure true
	}
	max_conns 256
	max_pending 1024
	max_idle_conns 32
	idle_timeout 60s
	connect_timeout 3s
	response_header_timeout 10s
	max_fails 3
	fail_timeout 30s
	deny_internal true
	health_check {
		type http
		path /healthz
		interval 5s
		timeout 2s
		fails 2
		passes 2
	}
}

route {
	name api
	match {
		host api.example.com
		path /api/*
		method POST PUT DELETE
	}
	middleware rate_limit {
		rate 10
		burst 20
	}
	middleware body_limit {
		max_bytes 10485760
	}
	handler proxy {
		upstream app
		retry {
			attempts 2
			statuses 502 503 504
		}
	}
}

route {
	name admin
	match {
		host api.example.com
		path /admin/*
	}
	middleware basic_auth {
		realm admin
		users {
			username ops
			password_hash $2a$14$Zkx19XLiW6VYouLHR5NmfOFU0z2GTNmpkT/5qqR7hx4IjWJPDhjvG
		}
	}
	handler proxy { upstream app }
}

route {
	name health
	match {
		host api.example.com
		path /health
	}
	handler respond {
		body "ok\n"
	}
}
```

这份配置用 `./bin/fit validate -c` 校验通过。只写 `upstream` 而没有监听器会被拒绝，所以示例补了一个最小的
`listener`；`static` 的 `root` 要磁盘上真实存在，`fit validate` 才会通过，所以示例里没有它。

## 相关文档

- [配置参考：Routes](../reference/config.md#routes)、[Upstreams](../reference/config.md#upstreams)、
  [Handlers](../reference/config.md#handlers)、[Middleware](../reference/config.md#middleware)，与
  [中文的章节说明](../reference/config.zh-CN.md#各章内容)
- [正向代理](16-forward-proxy.zh-CN.md)、[四层代理](17-l4-proxy.zh-CN.md)、[缓存](19-cache.zh-CN.md)、
  [流媒体](20-streaming.zh-CN.md)、[媒体认证](21-media-auth.zh-CN.md)、[媒体库](22-media-library.zh-CN.md)、
  [WebRTC](23-webrtc.zh-CN.md)
- 带全部字段的注释模板：[templates/conf/conf.d/60-apps.conf](../../templates/conf/conf.d/60-apps.conf)与
  [sites/](../../templates/conf/conf.d/sites/)
