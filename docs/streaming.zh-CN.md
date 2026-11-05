# 流媒体

[English](streaming.md) | [简体中文](streaming.zh-CN.md)

配置示例（.conf）在 [fit-examples](https://github.com/lesismal/fit-examples)：[streaming](https://github.com/lesismal/fit-examples/blob/main/streaming/fit.conf)。

fit 的流媒体角色在网关旁、在同一个引擎上运行 fib 的媒体 hub（[决定 030](decisions/030-media-auth-and-pages.zh-CN.md)）：
一路流按其路径（如 `live/cam`）只推送一次，经 RTMP、RTSP、SRT 或 WHIP 推入，即可经其中任何一种协议播放，也可经
HTTP-FLV、HLS 和 WHEP 播放。不做转码：每种协议承载它能承载的那些编码。

| 协议 | 推流 | 播放 | 配置位置 |
| --- | --- | --- | --- |
| RTMP | `rtmp://host:1935/live/cam` | 同左 | `streaming.rtmp` |
| RTSP | `rtsp://host:8554/live/cam`，ANNOUNCE 和 RECORD | DESCRIBE、SETUP 和 PLAY | `streaming.rtsp` |
| SRT | stream ID `#!::r=live/cam,m=publish` | stream ID `live/cam` | `streaming.srt` |
| WHIP 和 WHEP | `POST /whip/live/cam` | `POST /whep/live/cam` | `stream_whip` 和 `stream_whep` 处理器，使用 `streaming.webrtc` 的 UDP 端口 |
| HTTP-FLV、WebSocket-FLV | — | `GET /live/cam.flv` | `httpflv` 处理器 |
| HLS | — | `GET /hls/live/cam/index.m3u8` | `hls` 处理器 |

```fit
listener web :8080

media_auth live {
	public play
	tokens studio {
		token_sha256 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
		publish live/*
	}
}

streaming {
	rtmp { listen :1935; auth live }
	rtsp { listen :8554; auth live }
	srt { listen :8890; auth live }
	webrtc { listen :8188 }
}

route flv { match { path /live/* }; handler httpflv { auth live } }
route hls { match { path /hls/* }; handler hls { prefix /hls/; auth live } }
route whip { match { path /whip/* }; handler stream_whip { auth live } }
route whep { match { path /whep/* }; handler stream_whep { auth live } }
```

OBS 或 ffmpeg 用 `rtmp://host/live/cam?token=<token>` 推流，任何人都可以播放
`http://host:8080/live/cam.flv`、`http://host:8080/hls/live/cam/index.m3u8`、`rtsp://host:8554/live/cam`
或 `srt://host:8890?streamid=live/cam`。

## Hub

所有流都按路径存放在同一个 hub 中。推流者占用它的路径，同一路径的第二个推流者在第一个离开之前会被拒绝。每路流缓存其最新的
GOP（从最后一个关键帧开始），最多 `gop_cache_bytes`，使新的播放者立刻从一幅完整画面开始。包在收到它的 worker 上交给每个
播放者：不会等待任何播放者；落后的播放者会被丢包（视频丢到下一个关键帧为止），落后更多则被关闭。

| 字段 | 默认值 | |
| --- | --- | --- |
| `max_streams` | 64 | 同时存在的流数；再多一个推流者会被拒绝 |
| `max_players` | 256 | 一路流在所有协议上的播放者数；HLS 每路流每种格式计一个 |
| `gop_cache_bytes` | 8 MiB | 每路流缓存的 GOP |

## 认证

每个监听器和处理器都可以用 `auth` 引用一个 [`media_auth`](reference/config.md#media-auth) 策略，按流的路径检查：推流需要
`publish`，播放需要 `play`。不设 `auth` 时任何人都可以推流和播放。各协议携带凭据的方式不同：

| 协议 | 凭据 |
| --- | --- |
| RTMP | 流名或 app 的查询参数：`rtmp://host/live/cam?token=T`，或 `?user=U&pass=P` |
| RTSP | `Authorization`：Digest（用于有明文 `password` 的用户），或 `auth_methods` 列出 `basic` 时的 Basic；或 URL 中的 `?token=T`（或 `user` 和 `pass`） |
| SRT | stream ID 的 `s=` 作为 token（`#!::r=live/cam,m=publish,s=T`），或 `u=` 和 `s=` 作为用户名和密码；或资源中的 `?token=T`（`live/cam?token=T`） |
| HTTP-FLV、HLS、WHIP、WHEP | `Authorization: Bearer`、`Authorization: Basic`、`token` 查询参数或 `fit_media_token` cookie |

拒绝的方式按各协议：RTMP 返回 `NetStream.Publish.Unauthorized` 或 `NetStream.Play.Failed` 后关闭连接；RTSP 返回带质询的
`401`，或 `403`；SRT 以 `1401`（未认证）或 `1403`（禁止）拒绝握手；HTTP 返回带 `Bearer` 质询的 `401`，或 `403`。

用 `?token=` 获取的 HLS 播放列表，其中的媒体播放列表、分片和 part 也都带上该 token，使不发送请求头的播放器（如 Safari
和 ffmpeg）能继续播放；HLS 的每个请求都会检查。查询参数中的 token 不会被记录：访问日志把它写成 `REDACTED`。

### 用 bcrypt 检查的密码

RTMP、RTSP 和 SRT 在必须于返回前作答的钩子中做决定，钩子运行在收到请求的 worker 上，而 fit 运行的任何东西都不会占住
worker。因此，使用 `password_hash` 的用户，若其密码自策略加载以来尚未被确认正确，会先被拒绝一次，表示请重试（RTMP 的拒绝信息
如此说明，RTSP 返回带 `Retry-After: 1` 的 `503`，SRT 以 `1402` 拒绝），同时在 mediaauth 自己的池中检查哈希；一旦确认正确，
密码被记住，客户端的下一次尝试立即通过。token 和明文密码第一次就能通过。对不会重试的编码器，请使用 token（`fit media-token`
签发的或静态的）；HTTP 处理器会等待检查完成，期间请求被保留而不占用 worker。

## RTSP

RTP 交织在 RTSP 连接上传输（RTP/AVP/TCP）。要求 UDP 的 SETUP 得到 `461 Unsupported Transport`；ffmpeg 和多数播放器随后
改用 TCP 建立，用 `-rtsp_transport tcp` 可省去第一次尝试。基于 UDP 的 RTP 有待 fib：它的 RTSP 服务器尚不能移除自己添加的
UDP 绑定，而重载需要这一点（fib 请求 A61）。

## SRT

推流者的路径在握手中占用，因此第二个推流者会在还能告知对方时被以 `1409` 拒绝。播放一路没有推流者的流会被以 `1404` 拒绝，
除非 `play_wait` 允许它等待。`passphrase` 为每个会话加密，调用方也必须持有它。播放者从下一个关键帧开始，而不是从缓存的
GOP 开始，因为接收方的 UDP 缓冲区无法一次接住它。

## WebRTC

`stream_whip` 和 `stream_whep` 在 `streaming.webrtc` 的 UDP 端口上提供 WHIP（RFC 9725）和 WHEP，所有 PeerConnection 共用这
一个端口，fit 在其上作为 ICE lite agent。POST 一个 offer 得到 `201` 和 answer，其 `Location` 是该 POST 的路径加上会话 ID；
DELETE 该 URL 结束会话，无需凭据，因为 ID 本身就是凭证。H.264、H.265、AV1、Opus 和 G.711 双向都可；WebRTC 无法承载的 AAC
不会经 WHEP 发送，因此 H.264 加 AAC 的 RTMP 流只播放视频；Opus 也不会转成 AAC 给 RTMP。该端口是本角色自己的，不是
`webrtc.media` 的；后者的 SFU 独立于 hub，服务 `whip` 和 `whep` 处理器。

## 上限与超时

| 字段 | 默认值 | |
| --- | --- | --- |
| `rtmp.max_conns`、`rtsp.max_conns`、`srt.max_conns` | 1024 | 监听器的连接或会话数 |
| `rtmp.handshake_timeout` | 10s | 从连接到推流或播放 |
| `rtmp.idle_timeout` | 30s | 什么都不发送的推流者、输出不再前进的播放者 |
| `rtmp.max_message_bytes` | 8 MiB | 最大的 RTMP 消息 |
| `rtsp.session_timeout`、`rtsp.idle_timeout` | 60s | 什么都不发送的会话，以及没有会话的连接 |
| `srt.peer_idle_timeout` | 5s | 什么都不发送（包括 keep-alive）的对端 |
| `webrtc.max_sessions` | 256 | WHIP 和 WHEP 会话 |
| `webrtc.connect_timeout` | 15s | 从 answer 到 PeerConnection 连通 |
| `webrtc.max_offer_bytes` | 32 KiB | 一个 offer，最多 64 KiB |
| `hls` 的 `max_bytes` | 32 MiB | 一路流每种格式保留的分片 |

各监听器的 `play_wait`、`httpflv` 的 `wait` 和 `hls` 的 `play_wait` 让播放者等待尚无推流者的流；不设置时这样的播放立即被拒绝。

## 重载

重载原地修改监听器的 `auth`：已连接的客户端保留，之后的推流和播放按当前策略检查。修改监听器的其他字段会重启该监听器并关闭
其连接。修改 `max_streams`、`max_players` 或 `gop_cache_bytes` 会重启 hub，结束所有流和连接。参数不变时，`hls` 在重载后
保留其分片。去掉 `streaming` 会停止该角色，其处理器返回 `503`。

## 指标与日志

| 指标 | 类型 | |
| --- | --- | --- |
| `fit_streaming_streams{state}` | gauge | 流，`publishing`（有推流者）或 `waiting`（等待推流者） |
| `fit_streaming_players` | gauge | 所有流的播放者 |
| `fit_streaming_published_total` | counter | 推送过的流 |
| `fit_streaming_auth_refused_total{protocol,reason}` | counter | 被拒绝的 RTMP、RTSP 和 SRT 客户端：`unauthorized`、`forbidden`、`unavailable`（策略未加载，或密码正在检查） |
| `fit_streaming_restarts_total` | counter | 因 hub 上限改变而重启 hub 的次数 |

该角色记录在 `webrtc` logger 上，前缀为 `streaming:`：每个监听器启动时的地址，以及每路流的推送和结束。

## 互通测试

`scripts/streaming-interop.sh` 在 Linux 容器中让该角色与 ffmpeg 互通：ffmpeg 经 RTMP、RTSP（Digest 和查询参数 token）和
SRT 推流，以及缺少或错误的凭据（被拒绝）；每路流都经 RTMP、RTSP（TCP、Digest，以及从 UDP 回退到 TCP）、SRT、HTTP-FLV 和
HLS（fMP4 和 MPEG-TS）带凭据播放，不带凭据则被拒绝。
