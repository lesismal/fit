# 媒体库

[English](22-media-library.md) | [简体中文](22-media-library.zh-CN.md)

## 功能

媒体库是 fit 为之编目的电影与剧集文件所在的目录：扫描根目录与其子目录，把文件按名称或 sidecar 归入条目，每个条目是一部
作品——它的各个视频码流、音轨、字幕和海报。条目由[点播](../media-library.zh-CN.md#点播)的 `vod` 路由以 HLS 提供，也被
[频道](../channels.zh-CN.md)当作直播流播放。fit 从不转码：文件按存储时的样子提供。

菜单中的**媒体库**模块（`gw.medialib`，标签**点播 / 频道**）位于流媒体与媒体认证旁，有八个页面：**媒体库**、**频道**、
**播放 URL**、**指标**，以及四个配置页——**媒体库配置**、**点播路由**、**频道配置**与 **URL 签名**。前四个页面读取 Admin
API，后四个编辑配置的共享草稿。

默认关闭：没有 `media_library` 时不扫描也不提供任何文件，页面会说明这一点并链接到配置页。页面从不保存任何 URL、令牌或
签名：预览的 URL 只存在于它的播放器里，测试 URL 只留在表单中，离开页面即消失；密钥从不到达浏览器。

## 页面与操作

### 媒体库（Libraries）

每个运行中的媒体库一张卡片：条目数与其中出错的条目数、问题、上次扫描（及其扫描次数）、目录内存，以及它的根目录；卡片下方
是扫描状态——扫描中显示进度条（已探测 / 需要探测的文件数，共查看的文件数），有扫描等待时说明，否则为空闲；目录被截断时给出
原因。多个媒体库时以标签页切换。

**重新扫描**先确认，再请求一次扫描：已有扫描在等待时说明，进度在扫描期间每秒刷新一次，扫描结束后条目列表重新绘制。

条目列表：搜索标题与 ID（输入后 250 毫秒再查询），按**编码**（`h264`、`h265`、`av1`、`aac`、`opus`、`mp3`）、**分辨率**
（低于 720p、720p、1080p、4K 及以上，按条目最大版本的高度）与**问题**（全部、有问题、无问题）过滤，每页 50 项，可翻页。
每行显示标题、ID、时长、分辨率、编码、文件数（视频 / 音频 / 字幕）、大小与问题；列表下方可展开上次扫描报告的文件。

设置了 `upload_dir` 的媒体库多出一张**上传**卡片：选择文件与文件夹，按 8 MiB 一块发送，每块是一次带偏移量的 PATCH；连接
中断后从服务端记录的偏移量继续，可暂停、继续与取消；进行中的上传（来自本页、其他页面或重启之前）重新选择其文件即可继续。
文件完整后由随后的扫描探测。

### 条目（Item）

点击条目列表中的一行进入条目页。页首是探测错误（每个文件一条），旁边是**问 AI**（技能 `library-health`）。卡片给出 ID、
根目录、路径、时长、大小、修改时间、海报与 sidecar。

**预览**用 hls.js（首次需要时加载）播放：URL 由 `POST /media/preview` 为登录用户签发，两分钟有效；页面标出它是签名 URL、
短期令牌还是公开，以及主体与过期时间；路由未设置 `cors` 时给出警告并链接到**点播路由**。播放器静音，遇到第一次拒绝
（401、403、404、410）即停止并说明原因：URL 与其授权已过期、密钥已轮换，或会话已结束。

**管理文件**：改名、移动与删除，条目全部文件随之变动；先查询谁在播放它（引用它的频道、正在播放它的频道、正在播放它的会话），
使用中时先在确认框中说明，继续时才带 `force`。**Sidecar** 在表单中编辑：ID、标题、海报、视频码流、音频文件与字幕；写入前
检查引用的文件，编辑所依据的 sidecar 此后被改写过的以 409 拒绝；改变 ID 会先确认。

条目页还列出各**视频码流**与**音频文件**：名称、编码（RFC 6381）、详情（分辨率或采样率与声道数）、语言、时长、采样数与采样表
的字节数；以及**字幕**的名称、语言、格式、文件、大小与问题。

### 频道（Channels）

频道页以时间线编辑 `streaming.channels`：列表与日程属于配置，**保存并应用**后，频道从下一个条目起按其播放。每个频道一个标签
页，可**添加频道**与**删除频道**（改名即另一个频道，旧的结束）。

设置行：流名称、媒体库、时区、码流（`rendition`）、**随机播放**（`order` 为 `shuffle`）与**循环**（`loop`）。

**播放列表**把条目画成按时长比例的条形与可拖动的列表：从媒体库把条目拖入，或用 `+` 添加，拖动以调整顺序，逐行删除；显示条目数
与总时长，正在播放的条目被标出。**日程**按频道的时区画出一天：每 3 小时一个刻度，当前时间一个标记，每个时段按其开始时间与条目
时长画出；把条目拖到当天的时间轴上即在该时间开始一个时段；时段列表可编辑时间（`HH:MM` 每天、`mon,wed,fri 20:00` 或 `sat-sun
09:30` 这些日子、`2026-12-31 23:00` 一次）与条目，或**添加时段**。右侧的**媒体库条目**面板可搜索，拖入播放列表或当天，或用 `+`
添加。

状态每 2 秒刷新一次：状态、正在播放（含位置）、下一个、下一个时段、预读、已播放 / 跳过 / 欠载，以及**开始**、**停止**、**跳过**
和**立即播放**；预览卡片在页面重绘之间保留，编辑草稿不会使其停止。

**推流目标**列出每个目标：服务器、状态、最近错误（旁边是**问 AI**，技能 `channel-scheduler`）、已发送、码率、连接与失败，以及
开始、停止与**重启**；页面下方是全部推流目标。

### 播放 URL（Playback URLs）

每个 `media_auth` 策略一个标签页。策略没有 `url_signing` 时页面说明其 URL 不会过期，并链接到 **URL 签名**页。

- **URL 签名**卡片列出运行中的设置：`ttl`、`play_grace`、`session_idle`、`max_sessions`、`on_limit`、`cookie`、
  `bind_address`、`referers`、`origins`；**编辑**链接到 URL 签名页。
- **测试 URL**：主体、条目（选择后填入其路径）、路径、地址、有效期，以及经由的路由（该策略的 `media_tokens` 路由，套用它的
  `url_prefixes`、`max_ttl` 与 `url_base`）。**签发并播放**后，URL 显示在这里并在下方播放；它不会被保存，离开页面即消失；
  过期时间与会话 ID 一并显示。
- **密钥**按名称列出（单一密钥时为“策略的密钥”），显示保存于 `secret_file` 文件还是配置中（内联）、其时长，以及**轮换**；
  密钥本身从不显示。**轮换全部密钥**只在密钥环有多个密钥且都由文件保存时可用；内联密钥需通过编辑配置来轮换。确认框说明：
  轮换后该密钥签发的每个 URL 与令牌立即被拒绝，正在使用它们的播放器会停止。
- **按原因统计的被拒 URL**：`expired`、`signature`、`address`、`referer`、`origin`、`session_limit`、`session_ended`；
  有拒绝时可**问 AI**（技能 `playback-troubleshoot`）。
- **播放会话**分页列出（每页 50，按主体与开始时间排序），可按主体筛选，每 5 秒刷新：主体、该主体的会话数与 `max_sessions`、
  会话、资源、地址、开始、最近请求；每个会话可**结束**，其播放器从下一次请求起被拒绝（403）。

### 指标（Metrics）

每 2 秒读取一次 `GET /media/stats`；计数自 fit 启动以来，速率为最近一次刷新之间的变化。

- **媒体库**：每个媒体库的条目数与出错的条目数、文件池（忙 / 排队 / 队列上限）、文件池拒绝（被拒与超时）、采样表（字节 / 上限
  与命中率）。
- **点播**：每个 `vod` 处理器的当前观看（资源位于其路由前缀下的会话数）、每秒请求、每秒发送、分段缓存（字节 / 上限与命中率）。
- **播放 URL**：每个策略的观看数与按原因统计的 403（含与上次刷新之间的变化）。
- **频道**：状态、播放者，以及各推流目标的码率与字节数。

### 媒体库配置（Library config）

编辑 `media_library` 成员：**添加媒体库**，每行显示其根目录；原始文本标签页（UI / .conf / .yaml / .json）为整个成员。保存时
这些成员的问题链接回这里。

### 点播路由（VOD routes）

处理器为 `vod` 的路由：**添加点播路由**，每行显示媒体库、路径、`auth`，未设置 `cors` 时说明。原始文本标签页为 `routes`
成员：全部路由，而不只是这些。

### 频道配置（Channels config）

`streaming.channels` 与 `streaming.push`：添加频道与推流目标；原始文本标签页为整个 `streaming` 成员。“频道”页以时间线编辑
同样的列表。

### URL 签名（URL signing）

各 `media_auth` 策略的 `url_signing`；原始文本标签页为整个 `media_auth` 成员，其密钥按草稿中的样子保留。没有策略时链接到
[媒体认证](21-media-auth.zh-CN.md)页。

## 参数

`media_library` 的关键字段（完整字段表见[配置参考](../reference/config.md#media-library)）：

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `name` | 必填 | 媒体库的名字，处理器与 Admin API 的 `/media/library/<name>` 用它指称；字母、数字、`_`、`.` 与 `-` |
| `roots` | 必填 | 扫描的目录（含子目录）；条目的 ID 是其在此根目录下的路径；一个根目录不能位于另一个之内 |
| `files` | 全部 | 要查看的文件，按模式：不含 `/` 的匹配名称（`*.mp4`），含 `/` 的匹配根目录下的路径（`films/**/*.mp4`）；以 `.` 开头的名称从不查看 |
| `exclude` | 无 | 排除的文件与目录，按同样的模式（`**/extras/**`）；被排除的目录不会被遍历 |
| `follow_symlinks` | false | 跟随指向根目录之外的符号链接；留在根目录之内的无论如何都跟随，指向之外的被拒绝并报告 |
| `scan_interval` | `10m` | 重新扫描的间隔（至少 10s）；创建时、修改其根目录 / 模式 / 上限时、以及页面上的重新扫描也会扫描；大小与时间未变的文件不再探测 |
| `max_items` | 20000 | 目录的条目数；超出时其余部分缺失，并在页面上说明 |
| `max_files` | 200000 | 一次扫描查看的文件与目录数 |
| `max_catalog_bytes` | 64 MiB | 目录的内存，按其路径、标题、轨道和编解码器配置估算 |
| `max_probes` | 4 | 同时在文件池上探测的文件数，把其余 worker 留给正在提供的文件 |
| `max_sidecar_bytes` | 64 KiB | 一个 sidecar（`<name>.json`）的大小；更大者被报告并排除 |
| `max_subtitle_bytes` | 2 MiB | 提供的一个字幕（`.vtt`，或转为 WebVTT 的 `.srt`）的大小 |
| `max_table_bytes` | 128 MiB | 为正在提供的文件保留的样本表；超出时淘汰最久未用的 |
| `file_workers` | 每个 CPU 4 个，至少 16 | 文件池的 goroutine：同时向磁盘发出的请求；fib 的 worker 上从不访问磁盘 |
| `file_queue`、`file_timeout` | 4096、10s | 等待文件池的作业数与每个作业的时限；超出答 503 |
| `secret` / `secret_file` | 无 | 加密分片的 AES-128 密钥由此派生（至少 16 字节）；没有时该媒体库的内容无法加密 |
| `upload_dir` | 无 | 上传写入的目录（根目录之内或根目录本身）；不设置时此媒体库不接受上传，条目仍可改名、移动与删除 |
| `max_upload_bytes`、`max_uploads`、`min_free_bytes`、`upload_expiry` | 32 GiB、4、1 GiB、24h | 单个上传的大小（413）、进行中的上传数（429）、磁盘剩余空间（507）、久未写入的上传在下次扫描时放弃 |

`vod` 处理器的关键字段（完整表见[配置参考](../reference/config.md#http-handlers-vod)）：`library`（必填）、`prefix`
（不设置时为 `/vod/<library>/`）、`auth`（一个 [媒体认证](21-media-auth.zh-CN.md) 策略）、`container`（`fmp4` 或 `ts`）、
`segment_duration`（4s）、`max_cache_bytes`（256 MiB）、`max_segment_bytes`（32 MiB）、`cache_control`、`cors`、`timeout`
（30s）、`encrypt`。

频道的字段（`streaming.channels[]`、其 `schedule[]` 与 `push[]`）见[配置参考](../reference/config.md#config-channelslot)与
[频道](../channels.zh-CN.md#频道播放什么)；`url_signing` 的字段见[配置参考](../reference/config.md#config-mediaauth)。

## .conf 配置

```fit
listener web {
	address :8080
}

media_library films {
	roots /srv/media/films /srv/media/shows
	files *.mp4 *.m4a *.srt *.vtt *.jpg *.json
	exclude **/extras/**
	scan_interval 30m
	max_items 50000
	max_probes 4
	secret_file /etc/fit/films-secret
	upload_dir /srv/media/films/incoming
}

route films {
	match { path /vod/films/* }
	handler vod {
		library films
		segment_duration 4s
		cache_control "public, max-age=86400"
	}
}
```

于是 `http://host:8080/vod/films/films/bbb/index.m3u8` 播放条目 `films/bbb`。条目页的预览经这条路由的监听器加载媒体，所以
路由上最好设置 `cors`，否则浏览器可能拒绝从控制台的源播放。

## 相关文档

- [媒体库](../media-library.zh-CN.md)：条目与命名规则、ID、扫描、根目录之外的路径、上限、管理文件、点播与会过期的播放地址、加密分片。
- [频道](../channels.zh-CN.md)：频道播放什么、排期、时间戳、读取文件、推流目标、Admin API 与指标。
- [媒体认证](21-media-auth.zh-CN.md)：`media_auth` 策略与 `url_signing`。
- [流媒体](20-streaming.zh-CN.md)：`streaming` 角色与 `hls`、`httpflv` 处理器。
- [配置参考：Media library](../reference/config.md#media-library)与[控制台与 store](../console.zh-CN.md#媒体库)。
- 页面代码：[app/workbench/web/gateway/media.js](../../app/workbench/web/gateway/media.js)。
