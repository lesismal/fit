# 频道与推流目标

[English](channels.md) | [简体中文](channels.zh-CN.md)

频道把[媒体库](media-library.zh-CN.md)的条目作为直播流播放进[流媒体角色](streaming.zh-CN.md)的 hub：按顺序、随机、
循环，或按开始时间的排期（[决定 032](decisions/032-media-library.zh-CN.md)）。角色的每种出口都按频道的名称、在其
`auth` 之下播放它：RTMP、RTSP、SRT、HTTP-FLV、HLS 和 WHEP。推流目标把一个频道或 hub 中的任意流转推到外部 RTMP 或
SRT 服务器（例如 CDN 的接入点），断开后重连。不做任何转码：条目中的编码按存储的样子发送。

```fit
media_library films {
	roots /srv/media/films
}

streaming {
	rtmp { listen :1935 }
	srt { listen :8890 }

	channels {
		name tv/films
		library films
		items films/bbb films/sintel films/tears
		order shuffle
		time_zone Asia/Shanghai
		schedule {
			at 20:00
			item films/sintel
		}
		schedule {
			at "sat,sun 09:30"
			item films/bbb
		}
		push {
			url rtmp://live.example.com/app/stream-key
		}
	}

	push {
		name backup
		stream tv/films
		url "srt://backup.example.com:9000?streamid=publish:tv/films&latency=200"
	}
}

route hls { match { path /hls/* }; handler hls { prefix /hls/ } }
```

于是 `rtmp://host/tv/films`、`http://host/hls/tv/films/index.m3u8` 和 `srt://host:8890?streamid=tv/films` 都能播放
该频道；`live.example.com` 和 `backup.example.com` 会收到它。

## 频道播放什么

| 字段 | 默认值 | |
| --- | --- | --- |
| `name` | — | 流在 hub 中的路径，例如 `tv/films`；改名会结束旧频道 |
| `library` | — | 条目所属的 `media_library` |
| `items` | 无 | 条目的 ID，按顺序；媒体库没有或无法提供的条目会被跳过 |
| `order` | `sequence` | `sequence`，或 `shuffle`：每轮以随机顺序把每个条目播放一次，跨轮也不会连续播放同一个条目 |
| `loop` | 打开 | 列表播完后从第一个重新播放 |
| `schedule` | 无 | 时段：在某个时间开始某个条目，打断正在播放的内容 |
| `time_zone` | `UTC` | 时段时间所用的 IANA 时区，或 `Local` |
| `rendition` | 最好的版本 | 按名称选择播放的版本；缺少该版本的条目播放其最好的版本，即像素最多、像素相同时码率最高者 |

频道播放所选版本的第一条视频轨与第一条音频轨；没有音频的版本使用条目的默认音频文件（`bbb.audio-fr.m4a`，或旁置
文件指定的）。视频支持 H.264、H.265 和 AV1，音频支持 AAC、Opus 和 MP3，按每种出口的能力发送：WHEP 不发送 AAC，与任何
流相同。

既没有 `items`、此刻也没有时段在播的频道保持安静：它仍处于发布状态，播放器不会断开，到下一个时段再播放。关闭 `loop`
时，频道在最后一个条目之后保持安静，直到其下一个时段，或直到 Admin API 停止并再次启动它，这会从第一个条目重新播放列表。

## 排期

时段的 `at` 使用频道的 `time_zone`：

| `at` | 何时 |
| --- | --- |
| `20:00`、`20:00:30` | 每天 |
| `mon,wed,fri 20:00`、`sat-sun 09:30`、`fri-mon 23:00` | 每周的这几天 |
| `2026-12-31 23:00`、`2026-12-31T23:00:00` | 仅一次 |

到时间时，时段的条目开始：在频道的时间线上，它的第一帧画面在时段开始时显示。之前播放的条目在该处被截断，只读到那里；
恰好在时段开始时结束的条目，后面紧接时段的条目，没有间隙。频道启动时，或重新加载使安静的频道开始播放时，若某时段正在
进行，就从它应在的位置加入，从该处或之前的关键帧开始。条目本应已播完的时段会被跳过；重新加载时已过开始时间的时段也会
被跳过。时段的条目结束后，频道回到其 `items`，从上次播放的条目之后继续。

跨越时钟调整时，时间按写下的本地时间理解：`01:30` 就是当地 01:30，时钟回拨那一夜取两个 01:30 中的第一个。

## 时间戳

频道以采样的解码时间、在 fit 的定时器上控制节奏：每个包在到期时才发送进 hub，从不提前，因此播放器和切片器看到的是
直播流。条目之间按显示时间衔接：下一个条目的第一帧在上一个条目结束时显示，解码时间延续上一个条目，每条轨道都不会回退。
完全在条目开始之前显示的采样（例如 AAC 的预滚）会被略去。

轨道与上一个条目相同的条目（相同的编码与相同的配置：以相同方式编码的影片）会无缝延续，播放器看不出变化。轨道不同的
条目会重新设置轨道：HLS 标记不连续（`#EXT-X-DISCONTINUITY`），fMP4 还会有新的初始化分片；RTMP 和 HTTP-FLV 播放器收到
新的序列头；编码改变时 RTSP 播放器会结束，与任何流相同。时间戳在切换前后连续：fib 的出口只有在某个包不晚于同类的最后一个包
时才把新轨道当作重新开始的时钟（[fib 请求 A68](fib-api-requests.zh-CN.md#a68新轨道沿用同一时钟)），而频道的下一个条目从不如此。

## 读取文件

频道所做的一切都不会阻塞 worker（[规则 1](rules.zh-CN.md#一不阻塞-worker)）。条目的采样表由媒体库读取，保留在媒体库的
`max_table_bytes` 之内；采样在媒体库的文件池上读取，每次一秒的媒体，最多领先已发送内容 `read_ahead`（3s）和
`max_buffer_bytes`（16 MiB）。上一个条目播放时，下一个条目就已加载并读取。文件池或磁盘太慢时，频道会暂停，而不是把迟到
的内容一次性突发发送，并计一次欠载。播放中被修改或删除的文件，其条目在已读到的位置结束；下次扫描会按其现状列出。

## 加密的 HLS

设置 `encrypt` 后，频道的 HLS 与 `vod` 的分片一样加密（[加密分片](media-library.zh-CN.md#加密分片)）：AES-128，密钥由
媒体库的 `secret` 或 `secret_file` 派生，每 `key_rotation` 个分片（10）换一把，使交出的一把密钥只能打开少数分片。

```json
{"name": "tv/films", "library": "films", "items": ["films/bbb", "films/sintel"], "encrypt": true, "key_rotation": 10}
```

由 fib 的 HLS muxer 完成（fib 请求 [A69](fib-api-requests.zh-CN.md#a69加密的-hls-分片)），`hls` 处理器对任何设置了
`encrypt` 的频道的流都让它这样做：

- 媒体播放列表在每次换密钥处带有 `#EXT-X-KEY`，其 URI 为分片旁的 `key-<session>-<n>.key`，不带 IV，使分片的 IV 就是它的
  媒体序号。密钥之后（在不连续处）出现的 `#EXT-X-MAP` 之前先加 `METHOD=NONE`，使初始化段保持明文。
- 低延迟 HLS（`part_duration`）与明文频道一样可用：每个部分分片用其所属分片的密钥与 IV 单独加密，密钥的标签位于该分片的
  第一个部分分片之前。
- 每个分片和部分分片在切出时于媒体库的文件池上加密一次，加密之后才列出和提供，比切出时稍晚；没有请求等待加密，加密落后
  超过 4 个分片的流会结束，而不是无限积压媒体。
- 密钥只提供给处理器的 `auth` 允许播放该频道的请求，带 `Cache-Control: private, no-store`，且只提供已切出的分片与正在切的分片的密钥，
  使观众无法取得未来内容的密钥；没有 `auth` 的 `hls` 处理器不提供任何密钥（403）。
- 媒体库未加载或没有 secret 时，该频道的播放列表和密钥回答 503：绝不会以明文代替。

每当流的切片重新开始（如 `idle_timeout` 之后），`<session>` 就会改变，所有密钥随之改变，因此同一密钥与 IV 不会用两次。
重新加载配置时若打开或关闭 `encrypt`，或改变媒体库、其 secret 或 `key_rotation`，切片会在下一个播放列表请求时以新密钥
重新开始：播放器随后加载新会话的播放列表。RTMP、RTSP、SRT、HTTP-FLV、WHEP 和推流目标照旧在各自的 `auth` 下传送该频道。

## 推流目标

| 字段 | 默认值 | |
| --- | --- | --- |
| `name` | 流名加序号，例如 `tv/films#1` | 目标在 Admin API 与指标中的名称 |
| `stream` | 频道本身 | 被转推的流，用于 `streaming.push` |
| `url` | — | `rtmp://host[:port]/app/stream`、`rtmps://…`，或 `srt://host:port?streamid=…&passphrase=…&pbkeylen=…&latency=ms` |
| `retry_min`、`retry_max` | 1s、30s | 两次尝试之间的退避，从 `retry_min` 翻倍到 `retry_max` |
| `connect_timeout` | 10s | RTMP 握手与发布，或 SRT 握手 |

目标先等待其流被发布，每 `retry_min` 检查一次，然后从角色的引擎拨号。服务器接受后，它收到 hub 中已有的内容：轨道、缓存的
图像组，以及之后的每个包；跟不上的目标会被丢包（视频丢到下一个关键帧），与任何播放器相同。拒绝、断开或无法连接的目标在
退避后重试；连接持续了 `retry_max` 的目标再次从 `retry_min` 开始。流没有发布者时（例如频道停止后再启动），目标保持连接 5s。

推流 URL 中含有推流密钥：Admin API、指标与日志只显示其协议、主机与 RTMP 的 app，从不显示密钥，也不显示 SRT 的 stream ID
与 passphrase。

## 重新加载

重新加载就地修改频道与推流目标。频道新的 `items`、`order`、`schedule`、`library` 或 `rendition` 从其下一个条目起生效：
正在播放的条目继续播放（重新加载时已过开始时间的时段不会因此开始）。改名后的频道是另一个频道：旧频道的流结束。流、URL
与 `connect_timeout` 都未变的推流目标保持连接；其他的重新连接。hub 的限制改变时，所有流结束，频道与推流也一样，然后重新
开始。

## Admin API

| 端点 | |
| --- | --- |
| `GET /streaming/channels` | 所有频道：状态（`playing`、`loading`、`idle`、等待其流路径的 `waiting`、`stopped`），正在播放的条目（`now`：条目、标题、版本、来源、所属时段、位置），读好跟在后面的条目（`next`），下一个时段，预读的量，计数器，以及推流目标 |
| `GET /streaming/channels/{name}` | 单个频道 |
| `POST /streaming/channels/{name}` | `{"action": "stop"}`、`{"action": "start"}`、跳到下一个条目的 `{"action": "skip"}`、跳到指定条目的 `{"action": "skip", "item": "<id>"}` |
| `GET /streaming/push` | 所有推流目标：状态（`connected`、`connecting`、`retrying`、等待其流的 `waiting`、`stopped`），最近的错误，连接与失败次数，字节数与码率 |
| `POST /streaming/push/{name}` | `{"action": "stop"}`、`{"action": "start"}`、`{"action": "restart"}` |

含 `/` 的名称原样放在路径中（`/streaming/channels/tv/films`）；`#` 写成 `%23`。停止会取消频道的发布；启动从原来的位置继续
播放列表。这些操作不写入配置：被停止的频道在保留它的重新加载之后仍保持停止，在 fit 或 hub 重新启动后再次运行。

## 指标

| 指标 | 类型 | |
| --- | --- | --- |
| `fit_streaming_channels{state}` | gauge | 各状态的频道数 |
| `fit_streaming_channel_items_total{channel,result}` | counter | 开始播放（`played`）或被跳过（`failed`）的条目 |
| `fit_streaming_channel_underruns_total{channel}` | counter | 因读取太慢而暂停的次数 |
| `fit_streaming_push_targets{state}` | gauge | 各状态的推流目标数 |
| `fit_streaming_push_connects_total{target,result}` | counter | 连接成功（`ok`）或失败、断开（`failed`）的次数 |
| `fit_streaming_push_bytes_total{target}` | counter | 发送的字节：交给 RTMP 的媒体，SRT 发出的负载 |

角色会记录频道播放的每个条目、每个被跳过的条目及原因，以及推流目标的每次失败。

## 互通测试

`test/docker/channels/e2e.sh` 在 Linux 容器中运行 `streaming` 的 `TestChannelInterop`：ffmpeg 生成条目，ffprobe 经 RTMP、
RTSP、SRT、HTTP-FLV 与 HLS（fMP4 和 MPEG-TS）读取频道，确认解码时间跨条目连续；无头 Chromium 用 hls.js 播放频道，穿过
不连续点；一个频道经 RTMP 和 SRT 推到容器中的 MediaMTX，MediaMTX 重启后又重新收到流。
