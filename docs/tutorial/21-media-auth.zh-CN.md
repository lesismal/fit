# 媒体认证

[English](21-media-auth.md) | [简体中文](21-media-auth.zh-CN.md)

## 功能

`media_auth` 是配置里一组具名的策略，规定谁可以推送、播放哪些流。取媒体的处理器与角色用 `auth` 按名称引用一条策略：
`whip`、`whep`、`webrtc_proxy`、`hls`、`httpflv`、`vod`、`media_pages`、`media_tokens`，以及[流媒体](20-streaming.zh-CN.md)
的每个监听器。策略检查的是流的路径：推流需要 `publish`，播放需要 `play`；没有 `auth` 的处理器任何人都能进，按路由
自己的中间件放行。

一条策略有三类凭据：`tokens` 中的静态 token（写明文 `token`，或写它的 `token_sha256`，这样配置里不保存 token
本身）；`users` 中的用户，各带明文 `password` 或 `password_hash`（bcrypt 哈希，由 `fit hash-password` 生成）；以及
`secret`、`secret_file` 或 `keys` 密钥环——三者之一——用来签发带过期时间的媒体令牌，由 `fit media-token` 打印或由
`media_tokens` 处理器签发，令牌带着它的主体、过期时间与流。每个 token 和用户列出它可以 `publish` 与 `play` 的流的
模式：`*` 匹配任意一串字符，所以 `live/*`、`room1-*` 和 `*` 都可用；空列表不授予任何权限。`public` 列出任何人都
无需凭据就能做的动作，如 `play`。

`url_signing` 为观众开启会过期的播放地址：为一个观众、一个资源、一段时间签名的 URL，`vod`、`hls` 和 `httpflv`
处理器在每个请求上校验它。见[会过期的播放地址](../media-library.zh-CN.md#会过期的播放地址)。

没有凭据时应答 `401` 并带 `Bearer` 质询（`WWW-Authenticate: Bearer realm="fit"`）；token 未知、过期或被篡改，或
密码错误时同样 `401`，并带 `error="invalid_token"`；凭据不允许对该流做该动作时 `403`；`url_signing` 拒绝的 URL
返回 `403` 并说明原因。检查从不占住 worker：token 按其 SHA-256 查找或校验签名，明文密码以常数时间比较，
`password_hash` 在 mediaauth 自己的小线程池上检查，通过后被记住。

## 页面与操作

### 媒体认证

菜单里的“媒体认证”模块只有一个页面，编辑配置的 `media_auth` 成员：各策略及其 `tokens`、`users`、`keys`、`public`、
`token_ttl` 与 `url_signing`。页面有 UI、`.conf`、`.yaml`、`.json` 四个标签页，编辑的是共享草稿中属于这一页的部分；
顶栏的**保存并应用**保存整份草稿，**检查**走完整的应用流程只检查不应用，**放弃修改**丢弃草稿。保存的问题链接回
这一页，无论问题是在哪个页面被发现的——例如某条路由的 `auth` 指向配置中没有的策略。

在这一页能做的事：

- 建立一条策略：写 `name`（`whip`、`whep`、`webrtc_proxy` 与各角色按它引用），写它的签发密钥（`secret`、`secret_file`
  或 `keys`），写 `public` 的动作。
- 写 `tokens`：静态 token 或它的 SHA-256，以及它可以 `publish` 与 `play` 的流。日志里只出现 `token:<name>`，从不
  写出 token 本身。
- 写 `users`：明文密码或 bcrypt 哈希，以及他们的流。密码是机密：草稿里 `[redacted]` 的值不会被读出，助手抄回的
  值按草稿中保存的样子放回。
- 写 `keys` 密钥环：每个密钥一个名字与它自己的 `secret` 或 `secret_file`，用于按分片签发与吊销——轮换一个密钥只
  拒绝它签发的 token，其他分片照常运行。`secret_file` 在启动时必须至少 16 字节，进程还必须能写入它所在的目录。

`url_signing` 也在“媒体库”模块的 **URL 签名**页编辑，测试 URL 在**播放 URL**页签发与播放（
[媒体库](22-media-library.zh-CN.md)）；两处编辑的是同一份草稿。重载在原处修改策略，作用于之后的请求与会话：已建立
的会话保留。

## 参数

关键参数如下；完整字段表见[配置参考：Media auth](../reference/config.md#media-auth)。

### media_auth

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `name` | — | 必填。策略的名称，字母、数字、`_`、`.` 和 `-`；各处理器与角色按它引用 |
| `tokens` | 无 | 静态 bearer token，各带它可以推送与播放的流 |
| `users` | 无 | 带密码的用户，各带他们可以推送与播放的流 |
| `secret` | 无 | 签发令牌用的密钥，至少 16 字节（HMAC-SHA256）；`fit media-token` 与媒体页面的登录令牌都由它签发；不写时由进程的随机密钥签发登录令牌，重启后失效 |
| `secret_file` | 无 | 保存密钥的文件，每次应用配置时读取：重写它再应用即拒绝此前签发的每个 token |
| `keys` | 无 | 密钥环：每个密钥一个名字与它自己的 `secret` 或 `secret_file`；token 不带密钥 id，对环中每个密钥都校验 |
| `public` | 无 | 任何人都无需凭据就能做的动作（`publish`、`play`）；不写时每个请求都需要凭据 |
| `token_ttl` | 12h | fit 签发的令牌的有效时间，除非 `fit media-token` 的 `-ttl` 另有说明 |
| `url_signing` | 关闭 | 会过期的播放地址；需要 `secret`、`secret_file` 或 `keys` |

凭据的送达方式按协议：HTTP 上是 `Authorization: Bearer`、`Authorization: Basic`、`token` 查询参数或
`fit_media_token` cookie；RTMP 与 SRT 没有头部，用查询参数或 stream ID；RTSP 用 Digest（明文密码的用户）或 Basic。
查询参数中的 token 与签名在访问日志中写成 `REDACTED`。

### tokens

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `name` | `token` | 日志里叫什么：`token:<name>` |
| `token` | — | token 本身，至少 16 字符；或 `token_sha256`，二选一 |
| `token_sha256` | — | token 的 SHA-256（十六进制），`printf %s "$TOKEN" \| sha256sum` 打印的值 |
| `publish` | 无 | 它可以推送的流的模式 |
| `play` | 无 | 它可以播放的流的模式 |

### users

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `name` | — | 必填。用户名，不含冒号或控制字符 |
| `password` | — | 明文密码；RTSP 的 Digest 需要它；或 `password_hash`，二选一 |
| `password_hash` | — | bcrypt 哈希（`fit hash-password` 生成），只能在 Basic 与查询参数上用，在自己的池上检查 |
| `publish` | 无 | 他可以推送的流的模式 |
| `play` | 无 | 他可以播放的流的模式 |

### keys

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `key` | — | 必填。密钥的名字，1–64 个字母、数字、`_`、`.`、`~` 和 `-`；`media_tokens` 请求用它指明用哪个密钥签发、以及吊销哪个 |
| `secret` | — | 该密钥的 secret，至少 16 字节；或 `secret_file`，二选一 |
| `secret_file` | — | 保存该密钥的文件；轮换它只拒绝这个密钥签发的 token |

两个密钥共享同一个 secret 会被配置拒绝：token 从不带密钥 id，若两个密钥相同，轮换其中一个会留下另一个仍可用。
密钥环最多 256 个密钥。

### url_signing

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `ttl` | 10m | 请求未指明 ttl 时 URL 的有效时间，且不超过路由的 `max_ttl` |
| `bind_address` | `ip` | 绑定到观众的地址：`ip` 只认那个地址，`subnet` 认它的 `/24`（IPv4）或 `/64`（IPv6） |
| `play_grace` | 30m | 播放列表交出的授权在 URL 过期时间与条目结束时间二者中较晚者之后再存活多久 |
| `cookie` | 关闭 | 授权以 `fit_media_grant` cookie 设在资源路径上，供丢弃播放列表查询参数的播放器使用 |
| `max_sessions` | 无 | 一个主体的播放会话数；不写时不限 |
| `on_limit` | `refuse` | 超过时：`refuse` 应答 `403`，或 `end_oldest` 结束最早开始的会话 |
| `session_idle` | 2m | 一个会话多久没有请求就不再算在播放 |
| `max_tracked_sessions` | 100000 | 策略跟踪的全部主体的会话数上限；再多一个新会话应答 `503` |
| `referers`、`origins` | 无 | 允许播放的页面与脚本的来源；发了别的 Referer 或 Origin 的请求应答 `403`，没发的放行 |
| `trusted_proxies` | 无 | fit 之前的代理，绑定地址的 URL 校验时相信它的 `X-Forwarded-For` |

### media_tokens

处理器 `media_tokens` 是业务后端经自己的路由批量签发令牌与播放 URL 的方式：它本身不校验调用方凭据——由路由上的
`jwt`、`basic_auth`、`forward_auth` 或不带 `optional` 的 `client_cert` 校验——并且需要一条带 `secret_file` 的策略，
或每个密钥都带 `secret_file` 的密钥环，这样吊销才能改写文件并在重启后仍然有效。完整字段见
[配置参考：Handlers](../reference/config.md#http-handlers-media_tokens)。

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `prefix` | `/media-tokens` | 路由提供服务的路径；路由必须匹配它及其下一层 |
| `auth` | — | 必填。令牌所属的策略：单个 `secret_file`，或由它们组成的密钥环 |
| `prefixes` | 无 | 可以为哪些流名前缀签发令牌：每个 `publish` 与 `play` 模式都必须以其中之一开头 |
| `max_ttl` | 24h | 请求可以为令牌或 URL 要求的最长有效期；更长应答 `400` |
| `max_tokens` | 100 | 一次请求最多要求的令牌或 URL 数 |
| `max_body_bytes` | 64 KiB | 路由接受的最大请求体 |
| `revoke_timeout` | 10s | 吊销等待其密钥文件写入多久后应答 `504` |
| `url_base` | 无 | `/urls` 应答中的 URL 在其路径之前的前缀 |
| `url_prefixes` | 无 | `/urls` 可以为其签发 URL 的路径前缀 |

`POST <prefix>` 接收 `{"key", "tokens": [{"subject", "publish", "play", "ttl"}]}`，应答
`{"tokens": [{"token", "subject", "expires", "key"}]}`；`key` 指定用密钥环中的哪个密钥签发，不写时用第一个。
`POST <prefix>/revoke` 轮换指定密钥的 secret 文件，`{"key": "k2"}` 或 `{"all": true}`，使它签发的令牌立即被拒绝，
而其他密钥的令牌在过期前仍然有效。策略有 `url_signing` 时，`POST <prefix>/urls` 签发会过期的播放 URL：
`{"subject", "address", "session", "ttl", "key", "paths"}`，路径最多 `max_tokens` 个，每个都以 `url_prefixes`
之一开头。

## .conf 配置

```fit
listener web :8080

media_auth site {
	secret a-secret-of-at-least-16-bytes
	token_ttl 12h

	tokens studio {
		token_sha256 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
		publish live/*
	}

	users alice {
		password_hash $2a$14$Zkx19XLiW6VYouLHR5NmfOFU0z2GTNmpkT/5qqR7hx4IjWJPDhjvG
		publish live/*
		play *
	}

	public play

	url_signing {
		ttl 10m
		bind_address subnet
		play_grace 30m
		max_sessions 2
		on_limit end_oldest
		referers https://www.example.com
	}
}

route flv { match { path /live/* }; handler httpflv { auth site } }
route hls { match { path /hls/* }; handler hls { prefix /hls/; auth site } }
```

于是 `fit media-token -policy site -publish 'live/*' -ttl 1h` 打印一个带过期时间的令牌，OBS 用它推流；网站的 API
为观众签发会过期的播放地址，`hls` 与 `httpflv` 在每个请求上校验它。命令行还接受 `-play`（与 `-publish` 一样可重复，
或用逗号分隔）、`-subject`，以及密钥环上的 `-key`；它需要策略的 `secret`、`secret_file` 或 `keys`，因为用进程随机密钥
签发的令牌无法被运行中的 fit 校验。要经路由签发大量 token（一个会议、一个分组、一个区域一个密钥），用 `media_tokens`
处理器：它需要策略的 `secret_file` 或密钥环的 `secret_file`，并且必须放在 `jwt`、`basic_auth`、`forward_auth` 或不带
`optional` 的 `client_cert` 之后——它不检查任何调用方凭据。见
[从一个路由签发 token](../webrtc.zh-CN.md#从一个路由签发-token)。

## 相关文档

- [配置参考：Media auth](../reference/config.md#media-auth)：全部字段与各对象。
- [WebRTC](../webrtc.zh-CN.md#认证)：`whip`、`whep` 与 `webrtc_proxy` 的 `auth`、应答的码、会话的 URL 不需要凭据。
- [会过期的播放地址](../media-library.zh-CN.md#会过期的播放地址)：URL 里有什么、校验什么、播放列表交出什么、会话与吊销。
- [流媒体](20-streaming.zh-CN.md)：各监听器与处理器的 `auth`。
- [媒体库](22-media-library.zh-CN.md)：URL 签名与播放 URL 的页面。
- [反向代理](15-reverse-proxy.zh-CN.md)：路由上的 `media_tokens` 与 `media_pages` 处理器。
- [决定 030](../decisions/030-media-auth-and-pages.zh-CN.md)：策略与页面的设计。
