# 媒体库

[English](media-library.md) | [简体中文](media-library.zh-CN.md)

媒体库是 fit 为之编目的电影和剧集文件所在的目录，供以点播方式提供它们的处理器、以及把它们当作直播流播放的频道使用
（[决定 032](decisions/032-media-library.zh-CN.md)）。本页讲目录（catalog）：媒体库包含什么，文件如何归入条目，扫描如何
进行、受哪些上限约束，以及读取它的 Admin API；还有[点播](#点播)，即以 HLS 提供条目的 `vod` 处理器。把条目当作直播流播放随流媒体角色的频道而来。

默认关闭：没有 `media_library` 时，不扫描也不提供任何文件。

```fit
media_library films {
	roots /srv/media/films /srv/media/shows
	exclude **/extras/**
	scan_interval 30m
}
```

各字段见[配置参考](reference/config.md#media-library)。

## 文件事先准备，不转码

fit 按文件原样提供其中的编解码器：H.264、H.265 和 AV1 视频，AAC、Opus 和 MP3 音频，装在非分片的 MP4 文件里（`.mp4`、
`.m4v`、`.mov`，纯音频为 `.m4a`），`moov` 在文件开头或末尾均可。各个版本（rendition），比如一部电影的 1080p 和 720p，
是各自独立、事先用 ffmpeg 做好的文件；fit 从不转码。其他编解码器的文件、或无法解析的文件，会连同其错误一起列出，且不提供。

## 条目

条目是一部作品：它的各个版本、音轨、字幕和海报。同一目录下的文件按名称分组：

| 文件 | 对条目 `bbb` 而言是什么 |
| --- | --- |
| `bbb.mp4` | 一个没有标签的版本 |
| `bbb.720p.mp4`、`bbb.1080p60.mp4`、`bbb.4k.mp4`、`bbb.800k.mp4` | 标签为 `720p`、`1080p60`、`4k`、`800k` 的版本 |
| `bbb.audio-fr.m4a` | 纯音频，法语：`audio`，后接 `-` 和语言 |
| `bbb.srt`、`bbb.en.srt`、`bbb.fr.forced.vtt`、`bbb.en.default.sdh.vtt` | 字幕：语言、`forced`、`default`，其他词作为其名称 |
| `bbb.jpg`、`bbb.poster.png`、`bbb-poster.webp` | 海报 |
| `bbb.json` | sidecar |

含视频的文件是版本；只有音频、或标签为 `audio` 的文件是条目的音轨。字幕或海报归入名称以之开头的最长的那个条目，所以有
`bbb.2.mp4` 时 `bbb.2.srt` 属于它，否则属于 `bbb`。

sidecar 即文件旁的 `<name>.json`，用来指明命名规则无法表达的内容，路径相对于 sidecar 所在目录，可以进入其子目录，但不能越出：

```json
{
  "id": "sintel",
  "title": "Sintel",
  "poster": "art/sintel.jpg",
  "renditions": [{"file": "sintel-1080.mp4", "name": "1080p"}, {"file": "sintel-720.mp4", "name": "720p"}],
  "audio": [{"file": "sintel.fr.m4a", "name": "Français", "language": "fr", "default": false}],
  "subtitles": [{"file": "subs/sintel.zh.srt", "name": "中文", "language": "zh", "default": true, "forced": false}]
}
```

每个字段都可以不写。sidecar 列出的文件无论名称如何都属于它的条目；以 sidecar 命名的文件也属于该条目。sidecar 严格读取，
未知字段即为错误；无法解析的 sidecar、或列出了媒体库不查看的文件的 sidecar，会记入目录的问题（problems）中。

## ID

条目的 `id` 取自其 sidecar，否则为其在根目录下的路径，去掉版本标签和扩展名：`films/bbb.720p.mp4` 的 ID 为 `films/bbb`。
ID 由字母、数字、`.`、`_`、`~` 和 `-` 组成的若干段以 `/` 连接而成，因此原样就是 URL 的路径，也是 `media_auth` 模式能匹配的
流名，如 `vod/films/films/*`。路径中含其他字符（空格、非 ASCII 字母）时，每一串这样的字符替换为 `-`，并在其后加上路径哈希的
8 位十六进制：`clips/Big Buck Bunny.mp4` 的 ID 为 `clips/Big-Buck-Bunny-1f0c…`。同一路径每次扫描得到同一 ID，两个路径也
绝不会得到同一 ID。两个条目（来自两个根目录或两个 sidecar）要用同一 ID 时，按根目录和路径的顺序归第一个，另一个被报告。

## 扫描

媒体库在创建时、每隔 `scan_interval`（不设置时为 10 分钟）、重载修改其根目录、模式或上限时，以及
`POST /media/library/<name>/rescan` 时扫描。扫描在媒体库自己的 goroutine 上遍历根目录，一次只有一个扫描；扫描期间请求的
重新扫描会在其后执行一次。每个媒体文件在媒体库的文件池上探测：fib 的 `media/mp4` 读取到 `moov` 为止的 box 头和除样本表
之外的 `moov`，无论文件多长都只读几 KB。大小和时间都与上次扫描相同的文件不会再次探测，因此扫描未变的媒体库只需遍历目录。
扫描生成的新目录完整之后才替换旧目录；正在读旧目录的仍继续读它。

探测时仍在变化的文件（比如还在复制中）会再探测一次，若仍在变化，则连同该错误一起列出，并在下次扫描时再次探测。之后为提供
而打开时发现已变化或已消失的文件会被拒绝，其样本表被丢弃，直到下次扫描按其现状列出。

`files` 和 `exclude` 按模式选择要查看的内容：不含 `/` 的模式匹配名称（`*.mp4`、`trailers`），含 `/` 的匹配根目录下的路径
（`films/**/*.mp4`、`**/extras/**`），`*` 匹配名称之内的字符，`**` 匹配任意层目录。被排除的目录不会被遍历。以 `.` 开头的
名称从不查看。

## 根目录之外的路径

根目录之外的内容一概不读。sidecar 中越出其目录的路径（含 `..` 或以 `/` 开头）被拒绝。留在其根目录之内的符号链接会被跟随；
指向根目录之外的会被拒绝并报告，除非设置 `follow_symlinks`。每个目录只遍历一次，因此指回上层的链接不会造成循环，且深度不超过
32 层。打开文件以提供时会再次解析链接，因此扫描之后被替换成指向根目录之外链接的文件同样会被拒绝。

## 上限

| 上限 | 默认值 | 约束什么 |
| --- | --- | --- |
| `max_items` | 20000 | 目录的条目数 |
| `max_catalog_bytes` | 64 MiB | 目录的内存，按其路径、标题、轨道和编解码器配置估算 |
| `max_files` | 200000 | 一次扫描查看的文件和目录，每次读 256 个，无论目录多大 |
| `max_probes` | 4 | 同时探测的文件数，使扫描把文件池的其他 worker 留给正在提供的文件 |
| `max_sidecar_bytes` | 64 KiB | 一个 sidecar |
| `max_subtitle_bytes` | 2 MiB | 提供的一个字幕 |
| `max_table_bytes` | 128 MiB | 为正在提供的文件保留的样本表，丢弃最久未用的 |
| `file_workers` | 每个 CPU 4 个，至少 16 个 | 文件池的 goroutine：同时向磁盘发出的请求 |
| `file_queue`、`file_timeout` | 4096、10s | 等待文件池的作业数，以及每个作业可等待的时长 |

超过 `max_items`、`max_catalog_bytes` 或 `max_files` 时，目录缺少其余部分，并在 `truncated` 中说明原因；问题最多列出 256 个，
其余只计数。fib 的 worker 上从不访问磁盘：访问磁盘的工作在文件池上进行，请求在 retain 状态下等待它。

## 重载

重载时媒体库原地修改：持有它的处理器和频道看到新的配置。修改其根目录、模式或上限会重新扫描；修改文件池的上限会启动新的文件池，
旧池执行完已排队的作业；修改 `max_table_bytes` 会丢弃超出的样本表。重载时不再出现的媒体库被移除：其扫描停止，持有它的一方
被拒绝。

## Admin API

| 方法和路径 | 返回什么 |
| --- | --- |
| `GET /media/library` | 所有媒体库：根目录、扫描状态、条目数和有错误的条目数、问题、目录字节数、文件池和样本表 |
| `GET /media/library/<name>` | 一个媒体库的上述内容、其问题，以及一页条目：`offset`、`limit`（100，最多 1000）、`q`（在 ID 或标题中查找）、`codec`（`h264`、`aac` 等）、`error`（`true` 表示有文件出错的条目） |
| `GET /media/library/<name>/items/<id>` | 完整的条目：各版本及其轨道、编解码器（RFC 6381）、大小、时长和码率，音轨、字幕、海报和 sidecar，以及错误 |
| `POST /media/library/<name>/rescan` | `202`，立即请求一次扫描：已有一次在等待时 `queued` 为 false |

状态说明是否有扫描在运行或等待、上次扫描的开始和结束时间、查看的文件数、需要探测和已探测的媒体文件数，以及复用的文件数。

## 点播

`vod` 处理器以 HLS（RFC 8216）提供媒体库的条目，按文件存储的原样切分：采样原样放进分片，从不重新解码或编码。

```fit
route films {
	match { path /vod/films/* }
	handler vod {
		library films
		segment_duration 4s
		cache_control "public, max-age=86400"
	}
}
```

条目的 URL 是 `prefix`（不设置时为 `/vod/<library>/`）之后的条目 ID 路径，再加一个名称：

| 名称 | 是什么 |
| --- | --- |
| `index.m3u8` | 多变体播放列表，分片格式由 `container` 指定，不设置时为 `fmp4` |
| `index.fmp4.m3u8`、`index.ts.m3u8` | 同上，分别为分片 MP4 或 MPEG-TS 分片 |
| `v0.fmp4.m3u8`、`a0.ts.m3u8` | 版本 0 或音频文件 0 的媒体播放列表 |
| `v0.init.mp4` | 版本 0 的 fMP4 初始化段 |
| `v0.12.m4s`、`v0.12.ts` | 它的第 12 个分片 |
| `s0.m3u8`、`s0.vtt` | 字幕 0 的播放列表，以及 WebVTT 形式的字幕 |
| `v0.mp4`、`a0.m4a` | 文件本身，扩展名不变，支持 `Range`，供下载和普通播放器使用 |
| `poster.jpg` | 条目的海报，扩展名不变 |

所以 `/vod/films/films/bbb/index.m3u8` 播放条目 `films/bbb`。多变体播放列表把各版本列为变体，带 `BANDWIDTH` 与
`AVERAGE-BANDWIDTH`（文件字节数除以时长）、`CODECS` 和 `RESOLUTION`；条目的音频文件列为一个音频组，各版本自带的音频
在每个版本都有音频时作为其默认项；字幕列为字幕组。纯音频的条目以其音频文件为变体。fMP4 承载 H.264、H.265、AV1、AAC 和
Opus；MPEG-TS 承载除 AV1 外的同样编码，因此 AV1 的版本不出现在 `index.ts.m3u8` 中。其他轨道不提供：播放的是文件中
容器能承载的第一条视频轨和第一条音频轨。

**分片。** 有视频的文件在关键帧处切分，一个分片在距其起点最接近 `segment_duration` 的关键帧处结束：每隔
`segment_duration`（或其几分之一）有一个关键帧的文件（ffmpeg 的 `-g`，配合 `-sc_threshold 0` 或 `-force_key_frames`）
每隔 `segment_duration` 切一次，关键帧位置相同的各版本切法也相同，这最便于播放器在它们之间切换。分片包含从其第一帧
到下一分片之间呈现的音频，因此从任一分片开始播放（如拖动进度）的播放器立刻就有声音。会超过 `max_segment_bytes` 的分片
在更早的关键帧处切分；文件在那么长的时间里没有关键帧时，不按关键帧切分，此时其播放列表不含
`EXT-X-INDEPENDENT-SEGMENTS`。纯音频的文件每隔 `segment_duration` 切一次。

两种容器使用同一时钟：文件的时间 0 在分片时钟上是 1.4s，与 ffmpeg 的 MPEG-TS 一致，因此文件在呈现第一帧之前就要解码
的帧（B 帧、AAC 的预滚）保留各自的时间；字幕带有说明这一点的 `X-TIMESTAMP-MAP`。

**不占用 worker。** 媒体播放列表由文件的采样表切出，采样表由媒体库在其文件池上读取并保留（`max_table_bytes`）；
分片的采样也在那里读取（文件中连续的一段采样只读一次）并放进分片。请求以 retain 的方式等待，最多 `timeout`（30s），
超过则回答 503，文件池的队列满时立即回答 503。许多播放器同时请求的同一分片只读取一次。

**保留什么。** 文件的切分方式、其播放列表、分片和字幕为后续请求保留，最多 `max_cache_bytes`（256 MiB），最久未用的
先淘汰；正在构建的内容从其工作在文件池上开始起就占住其字节数，因此上限在构建期间同样成立，而在文件池队列中等待的请求不占用。参数相同的处理器共享所保留的内容，并在参数
不变的重载之后继续保留。

**变化的文件。** 自媒体库上次扫描以来被修改或删除的文件，在下次读取其内容时回答 404，处理器和媒体库为它保留的内容
随之丢弃；下次扫描按其现状列出，之后又可播放。扫描中不再列出的文件，为它保留的内容也会丢弃。

**缓存与 CDN。** 每个播放列表、分片、字幕和文件都带有取自其文件的 `Last-Modified`，每个分片和文件都带有由文件时间和
大小生成的 `ETag`，因此条件请求回答 304，分片的范围请求回答 206。`cache_control` 随每个响应发送，如
`public, max-age=86400`，以便前面的 CDN 缓存它们；不设置时不发送。

**谁可以播放。** `auth` 指定一个 [`media_auth`](reference/config.md#media-auth) 策略，每个请求都必须满足它对流
`vod/<library>/<id>`（如 `vod/films/films/*`）的 `play`：bearer token、Basic、`token` 查询参数或 `fit_media_token`
cookie。以 `?token=` 取得的播放列表，其列出的内容也带上该 token，使不发送请求头的 Safari 和 ffmpeg 能继续播放。会过期的
播放地址和加密分片在[开发计划](dev-plan.zh-CN.md)的后续迭代中加入。

## 指标

`fit_media_library_items`（按 `state`，ok 或 error）、`fit_media_library_catalog_bytes`、`fit_media_library_scans_total`、
`fit_media_library_probes_total`（按 `result`）、`fit_media_library_table_bytes`、`fit_media_library_pool_jobs`（queued、busy）
和 `fit_media_library_pool_refused_total`（refused、expired），均按 `library` 区分，见 Admin API 的 `/metrics`。
