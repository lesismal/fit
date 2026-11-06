# 媒体库

[English](media-library.md) | [简体中文](media-library.zh-CN.md)

媒体库是 fit 为之编目的电影和剧集文件所在的目录，供以点播方式提供它们的处理器、以及把它们当作直播流播放的频道使用
（[决定 032](decisions/032-media-library.zh-CN.md)）。本页讲目录（catalog）：媒体库包含什么，文件如何归入条目，扫描如何
进行、受哪些上限约束，以及读取它的 Admin API。条目的播放随 `vod` 处理器和流媒体角色的[频道](channels.zh-CN.md)而来，后者把它们当作直播流播放。

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

## 指标

`fit_media_library_items`（按 `state`，ok 或 error）、`fit_media_library_catalog_bytes`、`fit_media_library_scans_total`、
`fit_media_library_probes_total`（按 `result`）、`fit_media_library_table_bytes`、`fit_media_library_pool_jobs`（queued、busy）
和 `fit_media_library_pool_refused_total`（refused、expired），均按 `library` 区分，见 Admin API 的 `/metrics`。
