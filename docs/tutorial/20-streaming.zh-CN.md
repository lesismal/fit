# 流媒体

[English](20-streaming.md) | [简体中文](20-streaming.zh-CN.md)

## 功能

流媒体是 fit 在网关旁、在同一个引擎上运行的一个角色：它跑 fib 的媒体 hub。一路流按其路径（如 `live/cam`）只推送
一次，经 RTMP、RTSP、SRT 或 WHIP 推入，即可经其中任何一种协议播放，也可经 HTTP-FLV、HLS 和 WHEP 播放。不做转码：
每种协议承载它能承载的那些编码。

hub 里每路流缓存它最新的 GOP（从最后一个关键帧开始），最多 `gop_cache_bytes`，使新的播放者立刻从一幅完整画面开始。
推流者占用它的路径：同一路径的第二个推流者在第一个离开之前会被拒绝。包在收到它的 worker 上交给每个播放者，从不等待
任何播放者；落后的播放者会被丢包（视频丢到下一个关键帧为止），落后更多则被关闭。

四个监听器属于这个角色：`rtmp`、`rtsp`、`srt` 与 `webrtc`（`stream_whip` 和 `stream_whep` 的 UDP 端口）。它们与
路由上的 `hls`、`httpflv`、`vod` 处理器一起构成整条线：监听器收流和放流，处理器在 HTTP 上放流。每个监听器和处理器
都可以用 `auth` 引用一条[媒体认证](21-media-auth.zh-CN.md)策略，按流的路径检查：推流需要 `publish`，播放需要
`play`；不设 `auth` 时任何人都可以推流和播放。

`channels` 把[媒体库](22-media-library.zh-CN.md)的条目作为直播流播放进 hub，按顺序、随机、循环或按排期，各以其
名称为流名；`push` 把频道或任意流转推到外部 RTMP 或 SRT 服务器，断开后以退避方式重连。有频道的流媒体角色无需接收
推流的监听器。

本角色目前还不做的事：请求 UDP 的 RTSP SETUP 应答 `461`——RTP 只走连接上的交织传输，基于 UDP 的 RTP 还在等 fib
（请求 A61）；WebRTC 只能承载 H.264、H.265、AV1、Opus 与 G.711，因此 H.264 加 AAC 的 RTMP 流经 WHEP 播放时只有
视频，Opus 也不会被转成 AAC 供 RTMP 用；`push` 只转推 RTMP、RTMPS 与 SRT，不转推 HLS 或 DASH；本角色的 WebRTC
一侧是 `streaming.webrtc` 端口上的 `stream_whip`、`stream_whep` 处理器，而不是 [WebRTC](23-webrtc.zh-CN.md) 的
`webrtc.media` SFU。

| 协议 | 推流 | 播放 | 配置位置 |
| --- | --- | --- | --- |
| RTMP | `rtmp://host:1935/live/cam` | 同左 | `streaming.rtmp` |
| RTSP | `rtsp://host:8554/live/cam`（ANNOUNCE、RECORD） | DESCRIBE、SETUP、PLAY | `streaming.rtsp` |
| SRT | stream ID `#!::r=live/cam,m=publish` | stream ID `live/cam` | `streaming.srt` |
| WHIP 和 WHEP | `POST /whip/live/cam` | `POST /whep/live/cam` | `stream_whip`、`stream_whep` 处理器，用 `streaming.webrtc` 的端口 |
| HTTP-FLV、WebSocket-FLV | — | `GET /live/cam.flv` | `httpflv` 处理器 |
| HLS | — | `GET /hls/live/cam/index.m3u8` | `hls` 处理器 |

## 页面与操作

### 流媒体

菜单里的“流媒体”模块只有一个页面，编辑配置的 `streaming` 成员：hub 的三个上限、各监听器、`access`、`channels`
与 `push`。页面有 UI、`.conf`、`.yaml`、`.json` 四个标签页，编辑的是共享草稿中属于这一页的部分；顶栏的**保存并应用**
把整份草稿保存，**检查**走完整的应用流程（加载、编译、试绑新监听地址）只检查不应用，**放弃修改**丢弃草稿。保存的
问题链接回这一页，无论问题是在哪个页面被发现的。

页面上方有一条提示：**流媒体处理器本身（HLS、HTTP-FLV 与 WHIP/WHEP 推流）在路由中设置**，并链接到“路由”页
（[反向代理](15-reverse-proxy.zh-CN.md)）。这一页只写监听器与角色自己的成员；处理器在路由上按 `auth` 引用同一策略。

在这一页能做的事：

- 写各监听器的 `listen` 与 `auth`，以及它们的连接上限与超时；`auth` 写策略的名称，策略在[媒体认证](21-media-auth.zh-CN.md)页建立。
- 写 `access`：这个角色允许的客户端地址，先于全局 `access` 决定；它的列表文件会被监视，变化时重新应用配置。
- 写 `channels` 与 `push`：频道把媒体库的条目播放成直播流，推流目标把流转推到外部服务器。这两部分也在“媒体库”模块的
  **频道配置**页编辑（[媒体库](22-media-library.zh-CN.md)），两处编辑的是同一份草稿。

重载时 `auth` 在原处生效：已连接的客户端保留，之后的推流和播放按当前策略检查。修改监听器的其他字段会重启该监听器并
关闭其连接；修改 `max_streams`、`max_players` 或 `gop_cache_bytes` 会重启 hub，结束所有流和连接。频道与推流目标原地
修改：频道新的列表或排期从其下一个条目起生效，流与 URL 不变的推流目标保持连接。去掉 `streaming` 会停止该角色，其
处理器返回 `503`。

## 参数

关键参数如下；完整字段表见[配置参考：Streaming](../reference/config.md#streaming)，处理器各节见
[配置参考：Handlers](../reference/config.md#handlers)。

### hub

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `max_streams` | 64 | 同时存在的流数；再多一个推流者会被拒绝 |
| `max_players` | 256 | 一路流在所有协议上的播放者数；HLS 每路流每种格式计一个 |
| `gop_cache_bytes` | 8 MiB | 每路流缓存的 GOP |

### rtmp

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `listen` | — | 必填。RTMP 客户端连的 TCP 地址，如 `:1935` |
| `auth` | 无 | 推流与播放要满足的 media_auth 策略；凭据在流名或 app 的查询参数：`?token=T` 或 `?user=U&pass=P` |
| `max_conns` | 1024 | 连接数；再多一个在打开时被关闭 |
| `handshake_timeout` | 10s | 从连接到推流或播放 |
| `idle_timeout` | 30s | 什么都不发送的推流者、输出不再前进的播放者 |
| `play_wait` | 无 | 播放者可以等待尚无推流者的流；不设置时这样的播放立即失败 |
| `max_message_bytes` | 8 MiB | 最大的 RTMP 消息；更大的关闭连接 |
| `mptcp` | 关闭 | 内核支持时（Linux 5.6+）以多路径 TCP 监听 |

被拒绝时返回 `NetStream.Publish.Unauthorized` 或 `NetStream.Play.Failed`，随后关闭连接。

### rtsp

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `listen` | — | 必填。RTSP 客户端连的 TCP 地址，如 `:8554`；RTP 交织在同一条连接上（RTP/AVP/TCP） |
| `mptcp` | 关闭 | 内核支持时（Linux 5.6+）以多路径 TCP 承载 RTSP 连接 |
| `auth` | 无 | 请求要满足的策略；凭据是明文密码用户的 Digest、`auth_methods` 列出 `basic` 时的 Basic，或 URL 查询中的 `?token=T` |
| `auth_methods` | `digest` | 401 提供的方案；`password_hash` 的用户只能用 `basic` |
| `realm` | `fit` | 质询的 realm，Digest 的应答由它计算 |
| `max_conns` | 1024 | 连接数；再多一个在打开时被关闭 |
| `session_timeout` | 60s | 什么都不发送的会话 |
| `idle_timeout` | 60s | 没有会话的连接 |
| `play_wait` | 无 | 播放者可以等下一个推流者 |

要求 UDP 的 SETUP 得到 `461 Unsupported Transport`；ffmpeg 和多数播放器随后改用 TCP。被拒绝时返回带质询的 `401`，
或 `403`。

### srt

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `listen` | — | 必填。SRT 调用方到达的 UDP 地址，如 `:8890` |
| `auth` | 无 | 调用方要满足的策略：`m=publish` 是推流，其他是播放；凭据是 stream ID 的 `s=`（`#!::r=live/cam,m=publish,s=T`）、`u=` 与 `s=`，或资源查询中的 token |
| `max_conns` | 1024 | 会话数；再多一个以 `1402` 拒绝 |
| `latency` | 120ms | 接收延迟；调用方要求更大的以其为准 |
| `passphrase` | 无 | 为每个会话加密（10–79 字符），调用方也必须持有 |
| `peer_idle_timeout` | 5s | 什么都不发送（包括 keep-alive）的对端 |
| `play_wait` | 无 | 播放者可以等待尚无推流者的流；不设置时以 `1404` 拒绝 |

被拒绝的握手以 `1401`（未认证）或 `1403`（禁止）应答。播放者从下一个关键帧开始，而不是从缓存的 GOP 开始。

### webrtc

`stream_whip` 和 `stream_whep` 共用的 UDP 端口，fit 在其上是 ICE lite agent；它不是
[WebRTC](23-webrtc.zh-CN.md)的 `webrtc.media`。

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `listen` | — | 必填。所有 PeerConnection 共用的 UDP 端口，如 `:8188` |
| `public_ips` | 无 | 告知对端到达该端口的地址（1:1 NAT 之后） |
| `ice_servers` | 无 | 在 `Link` 头中告知对端的 STUN 与 TURN 服务器 |
| `max_sessions` | 256 | WHIP 和 WHEP 会话数；再多一个应答 `503` |
| `connect_timeout` | 15s | 从 answer 到 PeerConnection 连通 |
| `play_wait` | 无 | WHEP 请求可以等待尚无推流者的流；不设置时立即 `404` |
| `max_offer_bytes` | 32 KiB | 一个 offer，最多 64 KiB；更大的应答 `400` |

### channels 与 push

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `channels.name` | — | 必填。频道在 hub 中的流名，如 `tv/films`；改名是另一个频道 |
| `channels.library` | — | 必填。条目所属的 `media_library` |
| `channels.items` | 无 | 要播放的条目 ID，按顺序；最多 10000 |
| `channels.order` | `sequence` | 或 `shuffle`：每轮以随机顺序把每个条目播放一次 |
| `channels.loop` | 打开 | 列表播完后从第一个重新播放 |
| `channels.schedule` | 无 | 排期：在某个时间开始某个条目，打断正在播放的内容；最多 1000 |
| `channels.time_zone` | `UTC` | 排期所用的 IANA 时区 |
| `channels.rendition` | 无 | 每个条目播放的码流，按名称（如 `720p`）；条目没有它时播放最佳码流 |
| `channels.read_ahead` | 3s | 在媒体库的文件池上提前读取的量 |
| `channels.max_buffer_bytes` | 16 MiB | 已读入但尚未发送的字节上限，优先于 `read_ahead`；更大的采样会结束该条目 |
| `channels.push` | 无 | 该频道自己的转推目标 |
| `channels.encrypt` | 关闭 | 以媒体库 `secret` 派生的密钥，用 AES-128 加密频道的 HLS 分段 |
| `channels.key_rotation` | 10 | 与 `encrypt` 一起用：一个密钥加密多少个分段后轮换 |
| `push.stream` | — | 转推的流：hub 中的流名或频道名（`streaming.push` 中必填） |
| `push.url` | — | 必填。`rtmp://`、`rtmps://` 或 `srt://`；含流密钥，接口与日志只显示其 scheme、host 和 app |
| `push.retry_min`、`push.retry_max` | 1s、30s | 断线重连的退避 |
| `push.connect_timeout` | 10s | 握手与 publish 的时间 |

### 路由上的处理器

HTTP 一侧是路由上的处理器，不在这一页：完整字段表见[配置参考：Handlers](../reference/config.md#handlers)，这里列出
关键的几个：

| 处理器 | 关键字段 | 作用 |
| --- | --- | --- |
| `hls` | `prefix`（`/`）、`auth`、`format`（`fmp4` 或 `ts`）、`target_duration`（2s）、`part_duration`（低延迟 HLS）、`playlist_size`（6）、`max_bytes`（32 MiB）、`play_wait`、`idle_timeout`（30s）、`cors` | `/hls/live/cam/index.m3u8` 播放 `live/cam` |
| `httpflv` | `prefix`（`/`）、`auth`、`wait`、`cors` | `/live/cam.flv` 播放 `live/cam`，可走 HTTP-FLV 或 WebSocket-FLV |
| `stream_whip`、`stream_whep` | `stream`、`prefix`（`/whip/`、`/whep/`）、`auth` | 在 `streaming.webrtc` 的端口上做 WHIP 推流与 WHEP 播放；应答的 `Location` 是会话 URL，不需要凭据 |
| `vod` | `library`、`prefix`（`/vod/<library>/`）、`auth`、各分段参数 | 把媒体库的条目以 HLS 提供，而不是 hub 的流（[媒体库](22-media-library.zh-CN.md)） |
| `whip`、`whep` | `auth`、`stream` | [WebRTC](23-webrtc.zh-CN.md) 自己的媒体服务器 `webrtc.media`，与 hub 无关 |

## .conf 配置

```fit
listener web :8080

media_auth live {
	public play
	tokens studio {
		token_sha256 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
		publish live/*
	}
}

media_library films {
	roots /srv/media/films
}

streaming {
	max_streams 64
	max_players 256
	gop_cache_bytes 8388608

	rtmp {
		listen :1935
		auth live
		max_conns 1024
		handshake_timeout 10s
		idle_timeout 30s
		play_wait 5s
	}
	rtsp {
		listen :8554
		auth live
		auth_methods digest basic
		session_timeout 60s
	}
	srt {
		listen :8890
		auth live
		latency 200ms
		passphrase a-passphrase-for-srt
		peer_idle_timeout 5s
	}
	webrtc {
		listen :8188
		max_sessions 256
		connect_timeout 15s
	}

	access {
		allow 10.0.0.0/8
	}

	channels {
		name tv/films
		library films
		items films/bbb films/sintel
		order shuffle
		loop true
		time_zone Asia/Shanghai
		schedule {
			at 20:00
			item films/sintel
		}
	}

	push {
		name backup
		stream live/cam
		url "srt://backup.example.com:9000?streamid=publish:live/cam&latency=200"
	}
}

route flv { match { path /live/* }; handler httpflv { auth live } }
route hls { match { path /hls/* }; handler hls { prefix /hls/; auth live; target_duration 2s } }
route whip { match { path /whip/* }; handler stream_whip { auth live } }
route whep { match { path /whep/* }; handler stream_whep { auth live } }
```

OBS 或 ffmpeg 用 `rtmp://host/live/cam?token=<token>` 推流，策略的 `public play` 使任何人都可以播放
`http://host:8080/live/cam.flv`、`http://host:8080/hls/live/cam/index.m3u8`、`rtsp://host:8554/live/cam`
或 `srt://host:8890?streamid=live/cam`。用 `?token=` 取得的 HLS 播放列表，其中列出的内容也带上该 token，使不发送
请求头的播放器（Safari、ffmpeg）能继续播放；查询参数中的 token 不会被记录：访问日志把它写成 `REDACTED`。

## 相关文档

- [流媒体](../streaming.zh-CN.md)：hub、各协议、认证与拒绝的方式、bcrypt 密码的检查、重载、指标。
- [媒体认证](21-media-auth.zh-CN.md)：策略的 token、用户、密钥环与 `url_signing`。
- [媒体库](22-media-library.zh-CN.md)：条目、频道、播放 URL 与各配置页。
- [频道与推流目标](../channels.zh-CN.md)：排期、时间戳、退避与[重新加载](../channels.zh-CN.md#重新加载)。
- [反向代理](15-reverse-proxy.zh-CN.md)：路由上的 `hls`、`httpflv`、`vod` 与 `stream_whip`、`stream_whep`。
- [WebRTC](23-webrtc.zh-CN.md)：`webrtc.media` 的 SFU 与 `webrtc.proxy`。
- [配置参考：Streaming](../reference/config.md#streaming)与[Handlers](../reference/config.md#handlers)
  （[`hls`](../reference/config.md#http-handlers-hls)、[`httpflv`](../reference/config.md#http-handlers-httpflv)、
  [`stream_whip`](../reference/config.md#http-handlers-stream_whip)、
  [`stream_whep`](../reference/config.md#http-handlers-stream_whep)、
  [`vod`](../reference/config.md#http-handlers-vod)）。
