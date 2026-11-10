# 媒体库

[English](22-media-library.md) | [简体中文](22-media-library.zh-CN.md)

## 功能

媒体库是 fit 为之编目的电影与剧集文件所在的目录：扫描根目录与其子目录，把文件按名称或 sidecar 归入条目，每个条目是一部
作品——它的各个视频码流、音轨、字幕和海报。条目由[点播](../media-library.zh-CN.md#点播)的 `vod` 路由以 HLS 提供，也被
[频道](../channels.zh-CN.md)当作直播流播放。fit 从不转码：文件按存储时的样子提供。

菜单中的**媒体库**模块（`gw.medialib`，标签**点播 / 频道**）位于流媒体与媒体认证旁，有八个页面：**媒体库**、**频道**、
**播放 URL**、**指标**，以及四个配置页——**媒体库配置**、**点播路由**、**频道配置**与 **URL 签名**。前四个页面读取 Admin
API，后四个编辑配置的共享草稿。它们的 URL 依次是 `#/gw.medialib/library`、`channels`、`playback`、`metrics`、`config`、
`vod`、`channels_config` 与 `url_signing`。无论从哪个页面保存，`media_library` 的问题链接到媒体库配置，`vod` 路由的问题
链接到点播路由，`streaming.channels` 或 `streaming.push` 的问题链接到频道配置，策略 `url_signing` 的问题链接到 URL 签名。

默认关闭：没有 `media_library` 时不扫描也不提供任何文件，页面会说明这一点并链接到配置页。页面从不保存任何 URL、令牌或
签名：预览的 URL 只存在于它的播放器里，测试 URL 只留在表单中，离开页面即消失；密钥从不到达浏览器。

媒体库也面向观众：路由上的 `media_pages` 处理器提供 fit 自带的页面——经 WHIP 与 WHEP 的多人房间通话（带聊天）、推流页与
播放页；设置本模块的 `libraries`（以及 `channels`）后，还多出媒体库页与播放器页：登录的观众从那里用 HLS 播放条目，所用的
播放 URL 由页面为该观众签发。该处理器见下文与 [WebRTC](../webrtc.zh-CN.md#媒体页面)。

## 页面与操作

### 媒体库（Libraries）

每个运行中的媒体库一张卡片：条目数与其中出错的条目数、问题、上次扫描（及其扫描次数）、目录内存，以及它的根目录；卡片下方
是扫描状态——扫描中显示进度条（已探测 / 需要探测的文件数，共查看的文件数），有扫描等待时说明，否则为空闲；目录被截断时给出
原因。多个媒体库时以标签页切换。

**重新扫描**先确认，再请求一次扫描：已有扫描在等待时说明，进度在扫描期间每秒刷新一次，扫描结束后条目列表重新绘制。

条目列表：搜索标题与 ID（输入后 250 毫秒再查询），按**编码**（`h264`、`h265`、`av1`、`aac`、`opus`、`mp3`）、**分辨率**
（低于 720p、720p、1080p、4K 及以上，按条目最大版本的高度）与**问题**（全部、有问题、无问题）过滤，每页 50 项，可翻页。
每行显示标题、ID、时长、分辨率、编码、文件数（视频 / 音频 / 字幕）、大小与问题；列表下方可展开上次扫描报告的文件。

**上传**卡片用于设置了 `upload_dir` 的媒体库；没有设置时卡片说明此媒体库不接受上传，并链接到媒体库配置。选择文件（MP4、
M4V、M4A、MOV、字幕、图片或 sidecar）与上传目录下的文件夹，文件逐个发送，每个按 8 MiB 一块，每块是一次带偏移量的 PATCH；
连接中断后从服务端记录的偏移量继续，可暂停、继续与取消（取消会删除已发送的部分）；进行中的上传（来自本页、其他页面或重启之前）重新选择其文件即可继续。
文件完整后由随后的扫描探测。

### 条目（Item）

点击条目列表中的一行进入条目页。页首是探测错误（每个文件一条），旁边是**问 AI**（技能 `library-health`）。卡片给出 ID、
根目录、路径、时长、大小、修改时间、海报与 sidecar。

**预览**（只对没有探测错误的条目提供）用 hls.js（首次需要时加载）播放：URL 由 `POST /media/preview` 为登录用户签发，两分钟有效；页面标出它是签名 URL、
短期令牌还是公开，以及主体与过期时间；路由未设置 `cors` 时给出警告并链接到**点播路由**。播放器静音，遇到第一次拒绝
（401、403、404、410）即停止并说明原因：URL 与其授权已过期、密钥已轮换，或会话已结束。

**管理文件**：改名、移动与删除，条目全部文件随之变动；先查询谁在播放它（引用它的频道、正在播放它的频道、正在播放它的会话），
使用中时先在确认框中说明，继续时才带 `force`。**Sidecar** 在表单中编辑：ID、标题、海报、视频码流、音频文件与字幕；写入前
检查引用的文件，编辑所依据的 sidecar 此后被改写过的以 409 拒绝；改变 ID 会先确认。

条目页还列出各**视频码流**与**音频文件**：每个文件的名称、分辨率、码率、时长、编码（RFC 6381）、路径与大小，以及它的轨道表：
类型、编码、详情（分辨率或采样率与声道数）、语言、时长、采样数与采样表的字节数；以及**字幕**的名称、语言、格式、文件、大小与问题。

### 频道（Channels）

频道页以时间线编辑 `streaming.channels`：列表与日程属于配置，**保存并应用**后，频道从下一个条目起按其播放。每个频道一个标签
页，可**添加频道**与**删除频道**（改名即另一个频道，旧的结束）。

设置行：流名称、媒体库、时区、码流（`rendition`）、**随机播放**（`order` 为 `shuffle`）与**循环**（`loop`）。

**播放列表**把条目画成按时长比例的条形与可拖动的列表：从媒体库把条目拖入，或用 `+` 添加，拖动以调整顺序，逐行删除；显示条目数
与总时长，正在播放的条目被标出。**日程**按频道的时区画出一天：每 3 小时一个刻度，当前时间一个标记，每个时段按其开始时间与条目
时长画出；把条目拖到当天的时间轴上即在该时间开始一个时段；时段列表可编辑时间（`HH:MM` 每天、`mon,wed,fri 20:00` 或 `sat-sun
09:30` 这些日子、`2026-12-31 23:00` 一次）与条目，或**添加时段**。右侧的**媒体库条目**面板（媒体库的前 200 个条目，不含出错的）
可搜索，拖入播放列表或当天，或用 `+` 添加。这些编辑都进入草稿：**保存并应用**之前不会播放。

状态每 2 秒刷新一次：状态（`playing`、`loading`、`idle`、`waiting`、`stopped`）、正在播放（含位置）、下一个、下一个时段、预读、
已播放 / 跳过 / 欠载。尚未进入运行配置的频道会说明这一点：保存并应用后才启动。**开始**、**停止**（先确认）与**跳过**经 Admin API
的 `POST /streaming/channels/<name>` 立即作用于运行中的频道，下一个条目旁的**立即播放**跳到该条目；这些操作不写进配置：停止的
频道在重载后仍保持停止，fit 重启后再次播放。频道的**预览**经第一条 `hls` 路由播放，与条目的预览相同；预览卡片在页面重绘之间
保留，编辑草稿不会使其停止。

**推流目标**列出频道的每个目标：服务器（只显示 scheme、host 与 app，从不显示流密钥）、状态与距下次重试的时间、最近错误（旁边是
**问 AI**，技能 `channel-scheduler`）、已发送、码率、连接与失败，以及立即生效的**开始**、**停止**与**重启**；页面底部的**全部
推流目标**卡片也列出 `streaming.push` 的目标。

### 播放 URL（Playback URLs）

每个 `media_auth` 策略一个标签页。策略没有 `url_signing` 时页面说明其 URL 不会过期，并链接到 **URL 签名**页。

- **URL 签名**卡片列出运行中的设置：`ttl`、`play_grace`、`session_idle`、`max_sessions`、`on_limit`、`cookie`、
  `bind_address`、`referers`、`origins`；**编辑**链接到 URL 签名页。
- **测试 URL**：主体（初始为 `test-viewer`）、条目（`auth` 为该策略的 `vod` 路由的条目，选择后填入其路径）、路径、地址、
  有效期（初始为 `2m`），以及经由的路由（该策略的 `media_tokens` 路由，套用它的 `url_prefixes`、`max_ttl` 与 `url_base`；
  不选路由时有效期最长一小时）。**签发并播放**后，URL 显示在这里（可**复制**）并在下方播放；它不会被保存，离开页面即消失；
  过期时间与会话 ID 一并显示。
- **密钥**（无论有没有 `url_signing` 都显示）按名称列出（单一密钥时为“策略的密钥”），显示保存于 `secret_file` 文件还是配置中
  （内联）、其时长，以及把新密钥写入文件的**轮换**；密钥本身从不显示。内联密钥的**轮换**不可用，需通过编辑配置来轮换；
  **轮换全部密钥**在密钥环有多个密钥时显示，且只在全部由文件保存时可用。确认框说明：
  轮换后该密钥签发的每个 URL 与令牌立即被拒绝，正在使用它们的播放器会停止。
- **按原因统计的被拒 URL**：`expired`、`signature`、`address`、`referer`、`origin`、`session_limit`、`session_ended`；
  有拒绝时可**问 AI**（技能 `playback-troubleshoot`）。
- **播放会话**分页列出（每页 50，按主体与开始时间排序），可按主体筛选，每 5 秒刷新：主体、该主体的会话数与 `max_sessions`、
  会话、资源、地址、开始、最近请求；**结束**（先确认）一个会话后，其播放器从下一次请求起被拒绝（403）。

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

### 媒体页面（Media pages）

不是菜单页：路由上的 `media_pages` 处理器向观众提供 fit 自带的页面，它们嵌在二进制里。页面包括带登录的索引、带文字消息
与小文件聊天的多人房间通话、推流页与播放页，全部走 `whip`、`whep`（或流媒体角色的 `stream_whip`、`stream_whep`）的
WHIP 与 WHEP 端点，可在本服务器或另一台服务器上。设置 `libraries` 后，它们多出媒体库页（`<prefix>/library`，列出本模块
中观众有权播放的条目）与播放器页（`<prefix>/watch`，用随二进制提供的 hls.js 播放其中一个）；设置 `channels` 时，流媒体
角色的频道也列在那里。

设置 `login` 后，策略的用户在页面上登录一次：策略为其 `token_ttl` 签发的令牌以 `HttpOnly`、`SameSite=Strict` 的 cookie
`fit_media_token` 返回（TLS 上加 `Secure`），WHIP、WHEP 与 HLS 请求带着它；粘贴 `fit media-token` 打印的令牌同样有效。播放器向 `api/play` 请求 URL：
策略有 `url_signing` 且播放不是公开的，就得到为该观众、其地址与一个独立播放会话签发的播放 URL，其播放列表把授权交给播放器；
否则得到普通 URL，由登录的 cookie 放行。URL 被拒时播放器停止。

房间通话是另一半：每个成员发布 `<room>-<name>` 并播放其他人；麦克风、摄像头与屏幕的开关从不重新协商 PeerConnection，房间
API 会公布这些开关，于是其他人显示“麦克风已关”或该成员的头像，而不是等待不会到来的媒体。聊天经同一 API 传送文字与小文件。
相关字段与上限见下文，页面本身见 [WebRTC](../webrtc.zh-CN.md#媒体页面)。

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

频道的字段（`streaming.channels[]`、其 `schedule[]` 与 `push[]`）见[配置参考](../reference/config.md#config-streamchannel)与
[频道](../channels.zh-CN.md#频道播放什么)；`url_signing` 的字段见[配置参考](../reference/config.md#config-urlsigning)与
[媒体认证](21-media-auth.zh-CN.md#url_signing)。

`media_pages` 处理器的关键字段（完整字段表见[配置参考](../reference/config.md#http-handlers-media_pages)）：

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `prefix` | `/media` | 页面所在的路径前缀；路由必须匹配它及其下一层 |
| `auth` | 无 | 房间 API 校验的策略，也是页面登录与令牌所属的策略 |
| `login` | 关闭 | 为策略的用户显示登录表单；按 `token_ttl` 签发的令牌以 cookie `fit_media_token` 返回（`HttpOnly`、`SameSite=Strict`，TLS 上加 `Secure`）；不设置时，`auth` 需要凭据的页面会要求粘贴令牌 |
| `pages` | 开启 | 提供索引、通话、推流与播放页；关闭时一个页面都不提供，供自带页面的部署使用 |
| `title` | fit media | 页面标题 |
| `whip`、`whep` | `/whip/{stream}`、`/whep/{stream}` | 页面推流与播放所用的端点，`{stream}` 会被替换 |
| `hls` | 无 | 一路流的 HLS 播放列表 URL，供浏览器自身能播放 HLS 时的播放页使用 |
| `links` | 无 | 推流页与播放页为浏览器之外的工具显示的更多 URL；`{stream}`、`{token}` 与 `{host}` 会被替换 |
| `ice_servers` | 无 | 页面的 PeerConnection 所用的 STUN 与 TURN 服务器，即浏览器的 `iceServers` |
| `libraries` | 无 | 媒体库页列出其条目的媒体库，最多 16 个；只列观众有权 `play` 的条目 |
| `vod` | `/vod/{library}/{id}/index.m3u8` | 条目的多码率播放列表 URL，`{library}` 与 `{id}` 会被替换 |
| `channels` | 关闭 | 媒体库页也列出流媒体角色的频道；需要 `hls` |
| `chat` | 开启 | 房间通话的文字与小文件聊天 |
| `max_file_bytes` | 5 MiB | 一条聊天消息可携带的最大文件 |
| `max_rooms`、`max_members` | 64、16 | 同时在线的房间数，与一个房间的成员数（旁观者为两倍） |
| `mic_default`、`cam_default` | 关闭 | 成员加入时麦克风或摄像头是否已开；URL 中的 `mic=0\|1` 与 `cam=0\|1` 优先 |
| `member_ttl`、`stall_timeout` | 30s、30s | 成员多久没有心跳就不再算在房间内，以及事件流等待其浏览器多久 |

## .conf 配置

```fit
listener web {
	address :8080
}

media_library films {
	roots /srv/media
	files *.mp4 *.m4a *.srt *.vtt *.jpg *.json
	exclude **/extras/**
	scan_interval 30m
	max_items 50000
	max_probes 4
	upload_dir /srv/media/incoming
}

media_auth staff {
	secret a-secret-of-at-least-16-bytes
	users operator {
		password_hash $2a$14$Zkx19XLiW6VYouLHR5NmfOFU0z2GTNmpkT/5qqR7hx4IjWJPDhjvG
		play *
	}
	url_signing {
		ttl 2m
		play_grace 30m
		max_sessions 2
	}
}

route films {
	match { path /vod/films/* }
	handler vod {
		library films
		auth staff
		segment_duration 4s
		cache_control "public, max-age=86400"
	}
}

route media {
	match { path /media /media/* }
	handler media_pages {
		auth staff
		login true
		libraries films
	}
}
```

文件 `/srv/media/films/bbb.mp4` 就是条目 `films/bbb`——ID 是它在根目录下去掉扩展名的路径；控制台上传的文件写入
`/srv/media/incoming`。于是 `http://host:8080/vod/films/films/bbb/index.m3u8` 为策略放行的观众播放条目 `films/bbb`：控制台的预览为控制台用户签发
短期 URL，页面播放器的用户则以 `operator` 登录。条目页的预览经这条路由的监听器加载媒体，所以路由上最好设置 `cors`，否则
浏览器可能拒绝从控制台的源播放。工作人员打开 `http://host:8080/media/`，以 `operator` 登录，即可在页面自带的播放器里播放
媒体库——播放 URL 由页面为他们签发，有效期为 `url_signing` 的 `ttl`（两分钟），而其播放列表交出的授权在影片结束后
还能维持 `play_grace`。要加密分段，给媒体库加上 `secret_file`（至少 16 字节），并在 `vod` 处理器上写 `encrypt true`。

## 相关文档

- [媒体库](../media-library.zh-CN.md)：条目与命名规则、ID、扫描、根目录之外的路径、上限、管理文件、点播与会过期的播放地址、加密分片。
- [频道](../channels.zh-CN.md)：频道播放什么、排期、时间戳、读取文件、推流目标、Admin API 与指标。
- [媒体认证](21-media-auth.zh-CN.md)：`media_auth` 策略与 `url_signing`。
- [流媒体](20-streaming.zh-CN.md)：`streaming` 角色与 `hls`、`httpflv` 处理器。
- [配置参考：Media library](../reference/config.md#media-library)、[Handlers](../reference/config.md#handlers)与
  [控制台与 store](../console.zh-CN.md#媒体库)。
- [WebRTC](../webrtc.zh-CN.md#媒体页面)：媒体页面、多人房间通话与其聊天，以及它们所用的 WHIP 与 WHEP 端点。
- 页面代码：[app/workbench/web/gateway/media.js](../../app/workbench/web/gateway/media.js)。
