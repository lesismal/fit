# BitTorrent

[English](27-bittorrent.md) | [简体中文](27-bittorrent.zh-CN.md)

## 功能

**BitTorrent** 是菜单中的最后一个模块，位于[数据同步](26-sync.zh-CN.md)之后。它是同一个配置成员 `bittorrent` 的两个
角色共用的一个模块（标签“客户端 / tracker”）：

- **`bittorrent.client`**：在 fit 自己的 fib 引擎上、网关旁下载和做种的 BitTorrent 客户端：一个 `listen` 端口同时
  用于 TCP 和 UDP（uTP、DHT 与 UDP tracker 共用它）。它从磁力链接、.torrent 文件、.torrent URL 与 `watch_dir`
  接收种子，并把种子跨重启保存在 `data_dir` 中：列表在 `torrents.json`，各种子的 .torrent 文件在 `torrents/` 下，
  续传数据在 `resume/` 下，因此重启会把每个种子重新加入，且不会重新哈希磁盘上的内容（除非文件的大小或修改时间变了）。
- **`bittorrent.tracker`**：一个 tracker：HTTP 与 HTTPS 的 announce 与 scrape 经任何以 `bt_tracker` 为处理器的
  路由，落在 fit 的监听器上（带它们的 TLS 与中间件）；UDP 的（BEP 15）在它自己的 `listen` 上。它可以公开，也可以
  用 `media_auth` 策略的 passkey 做成私有，并可保留一份 info hash 白名单。swarm 保存在内存中且有上限；多个实例可以
  经 Redis 共享，作为一个 tracker 应答。

两个角色都只在配置设置时才运行：没有 `bittorrent.client`，fit 不开放任何 BitTorrent 端口、不保存状态；没有
`bittorrent.tracker`，它不做任何跟踪。客户端的种子下载到 `download_dir` 之下（不设置时为 `<data_dir>/downloads`），
没有种子会写到该目录之外。[分类](#分类与目录)给种子一个目录、自己的限制和一个要重新扫描的媒体库；[队列](#队列)限制同时
下载和做种的数量；[`min_free_bytes`](#磁盘空间)让下载不把磁盘占满；[`alt_speed`](#备用速率)按计划替换速率；
[屏蔽列表](#做种分享率与限制)决定客户端与哪些 peer 打交道。除下载之外，客户端还能[把目录发布出去](#发布目录)为 hybrid
种子（在原处做种，以 `static` 路由为 Web 种子），并能[从 RSS 订阅源下载](#rss-自动下载)并按规则挑选。路由上的处理器
补全整幅图景：`bt_publish` 提供已发布种子的 .torrent 文件与磁力链接，`bt_stream` 在种子下载的同时经 HTTP range 播放
其文件，`bt_tracker` 提供 HTTP 的 announce 与 scrape（见[反向代理](15-reverse-proxy.zh-CN.md)与
[配置参考：Handlers](../reference/config.md#handlers)）。

控制台运行时模块就在菜单里；它经控制台转发的 Admin API `/bittorrent/` 接口读取并修改两个角色。各页面编辑配置的
**共享草稿**：顶栏的**保存并应用**应用整份草稿；**检查**走完整的应用流程只检查不应用；**放弃修改**丢弃草稿。
`bittorrent` 下的保存问题会链接到 **BitTorrent 配置**页，无论问题是哪个页面保存的。更改后的重载：

| 更改 | 发生什么 |
| --- | --- |
| `limits`、`access`、`categories`、`alt_speed`、`watch_dir`、`save_interval`、队列与磁盘的上限、`rss` | 就地应用，已连接的 peer 保留（降低的连接上限所超出者、新 `access` 拒绝者除外） |
| `listen`、`encryption`、`dht`、`pex`、`utp`、`lsd`、`disk_workers`、`disk_queue_bytes` | 新建客户端：保存每个种子的续传数据，把它们加入新客户端且不重新校验 |
| `data_dir` | 在新目录的状态上新建客户端，如同重启；旧目录保留已保存的内容 |
| tracker 的 `allow`、`auth`、`access`、`console`、各上限、`peer_ttl` | 就地应用，swarm 保留 |
| tracker 的 `listen` | UDP 端口移动，swarm 保留 |
| tracker 的 `store` | 新建 store；本实例的 swarm 在内存中重新开始，共享的留在 Redis |

## 概念

### 种子、元数据与分片

种子的数据由**元数据**（文件树与分片哈希）描述，并由它的 **info hash** 标识。fit 处理 v1、v2 与 hybrid 种子：
种子的 **ID** 是它的 v1 info hash（十六进制），或它的 v2 哈希（64 位数字）的前 20 字节。元数据随 .torrent 文件到达；
以**磁力链接**（`magnet:?xt=urn:btih:…`）添加的种子则要从 peer 处取得元数据，在那之前它没有可显示的文件。文件被切成
**分片**，其哈希会被校验，因此一个分片要么已有、要么没有；fit 报告一个种子共有多少分片、已有多少。

### 状态

种子经过这些状态：`metadata`（获取元数据）、`checking`（校验中）、`downloading`（下载中）、`seeding`（做种中）、
`queued`（排队中：在队列中等待轮到自己）、`paused`（已暂停）、`stopped`（已停止）与 `error`（错误：存储错误不会让
种子停止，它留在状态中，直到某个分片再次通过校验）。角色还会说明它为什么**保持**一个种子：启动中、由角色排队或
磁盘空间不足。

### 做种、分享率与限制

做种就是上传种子已有的数据；种子拥有它需要的全部分片后即在做种。`limits.seed_ratio`（已上传字节除以种子大小）与
`limits.seed_time` 达到时暂停种子——不设置时永久做种——状态与事件（`seed_limit`）会说明。`limits` 保存客户端的速率、
连接数、上传槽位与做种界限；分类的 `limits` 是其中每个种子的，种子自己的限制取代它们所设置的那些（0 取客户端的；
负数连接数、分享率或时长为该种子不设限）。**屏蔽列表**是角色的 `access` 与全局 `access`：`deny`/`allow` 地址与
`deny_files`/`allow_files`，后者除地址、前缀与范围外还读取 eMule `ipfilter.dat` 与 PeerGuardian `.p2p` 行；
被拒绝的 peer 一连接就被关闭、从不被拨号、也得不到 DHT 应答，策略或列表文件一改变立即生效，关闭现在被拒绝的 peer。

### 分类与目录

分类给种子一个目录与限制（可选一个 `media_library`）。相对的 `dir` 位于 `download_dir` 之下且不得越出它
（不设置时为 `<download_dir>/<name>`）；绝对的 `dir` 原样使用。分类中的某个种子完成时，指定的媒体库会被重新扫描，
因此下载到其根目录中的内容会出现在它的目录中；一个种子只算完成一次，跨重启保留，且只有在文件优先级改变后才会再次
算完成。把种子移到另一个分类会换成该分类的限制与媒体库，但它的文件留在添加时所在的目录。

### 队列

`limits.max_active_downloads` 与 `max_active_seeds` 限制同时下载和做种的种子数；超过后种子由角色暂停排队，顺序即
列表顺序，种子的**队列位置**可以改变它。正在获取磁力链接元数据或仍在校验数据的种子算作下载中。经 API 暂停或因做种
上限暂停的种子不计入，队列也从不启动它们。不设置上限时没有队列。

### 磁盘空间

`limits.min_free_bytes`（1 GiB）是下载在其目录所在磁盘上要留出的空间：只有在磁盘拥有它仍需下载的量再加上这么多
空闲时种子才开始，正在下载的种子在空闲不足时暂停并在状态中写明错误；角色每秒再看一次，空间出现后自行启动它。
做种不需要空间，也不检查。

### 备用速率

`alt_speed` 设置一个上传和一个下载速率（0 为不限），在它的某个每周 `schedules`（`days`、`from`、`to`；`to` 早于
`from` 的时段跨过午夜延续到第二天）生效期间取代 `limits.upload_rate` 与 `limits.download_rate`。客户端各页面的
顶栏可把它们设为 `on` 或 `off`（不论时间表如何），或恢复为 `auto`；这个选择跨重启保存在状态中。

### 从 watch_dir 添加

`watch_dir` 每 `watch_interval`（10s）查看一次 `.torrent` 与 `.magnet` 文件（磁力链接写在第一行非空内容）。直接放在
其中的以无分类添加；放在以分类命名的子目录中的归入该分类。文件在两次查看之间保持大小与修改时间不变后才被取走，因此
正在写入的文件不会被读到一半；处理后的文件移入 `.done`（已添加或已存在）或 `.failed`（旁边有一个说明原因的
`.error` 文件）。

### 发布目录

`publish` 条目用 fib 的元数据构建器把目录中的文件做成 hybrid 种子——每个文件一个（模式 `per_file`，默认），或整个
目录一个（模式 `whole_dir`）——并在文件所在处做种，用的存储拒绝一切写入，因此已发布的文件绝不会被 peer 的下载覆盖。
已发布的种子永久做种，不论 `seed_ratio` 与 `seed_time` 如何；它不在客户端的种子列表中，也不计入 `max_torrents`。
`web_seed` 是目录对外提供服务的 URL（如同 `static` 路由），写进每个种子作为其 Web 种子，客户端因此可以从 fit 的
做种、Web 种子或两者同时下载。`announce` 列出 tracker URL，`private` 把种子标为私有（BEP 27：peer 只经所声明的
tracker 获得），`files`/`exclude` 挑选发布哪些文件。条目每 `check_interval` 遍历目录，在文件停止变化后构建它；
文件改变或被删除会停止其种子，改变的文件会得到一个新 info hash 的新种子。`bt_publish` 提供 JSON 索引、各种子的
`.torrent` 文件与磁力链接。

### RSS 自动下载

`rss` 每 `interval`（15m，至少 1m）轮询 RSS 2.0（及 1.0）与 Atom 订阅源，回送 `ETag` 与 `Last-Modified`，
未变化的订阅源答 `304`，不再有额外开销。每个新条目交给第一条满足其 `feeds`、`patterns`、`exclude`（对整条标题
大小写不敏感的通配符，或 `re:` 加正则）以及 `min_size`、`max_size` 的规则；规则把该条目的磁力链接或 .torrent URL
按其 `category`、`paused` 添加，每次轮询至多 `max_per_poll` 个。设置 `episodes` 后，一部剧的每一集（`S01E02`、
`1x02` 或日期）只添加一次。读过的每个条目连同结果记在 `data_dir/rss.json`。订阅源或规则可以在 RSS 页启用或停用，
不论配置中的 `enabled` 如何。

### 私有 tracker 与 passkey

设置 `auth`（一个 `media_auth` 策略）后 tracker 是私有的：每次 announce 与 scrape 都需要 **passkey**，放在路径
（`/<passkey>/announce`）或查询参数（`?passkey=`）中，其授权须能播放 `bittorrent/<十六进制 info hash>`——
`play: ["bittorrent/*"]` 表示所有种子。passkey 可以是策略的令牌之一，也可以是用户的 passkey：由策略的签名密钥与
用户名派生的 32 位十六进制数字，页面可为某个用户查询它。轮换策略的 `secret` 会改变每个用户的 passkey；没有 secret
时 passkey 在 fit 重启后改变。策略的 `public` 含 `play` 时允许所有人。白名单（`allow` 与 `allow_files`）把
tracker 限制在列出的 info hash 上；其他以未注册被拒绝。访问日志从不保存 passkey：它被写成 `REDACTED`。

### 分发

在管理端，**集群 → BitTorrent** 显示各成员的 BitTorrent 角色，并通过 BitTorrent 把一个成员发布的文件分发给其他
成员：一次**分发**。源用一个私有的 `publish` 条目发布它；管理端把 `.torrent` 文件发给每个目标，以源和其他目标作为
它的 peer，放进目标的一个分类，跟进各目标的进度，最后从它们上移除该种子（保留其文件）或让它继续做种。见
[集群管理](../cluster.zh-CN.md#bittorrent成员的种子与分发)与[集群一章](12-cluster.zh-CN.md)。

## 页面与操作

### 种子

按队列顺序列出全部种子：名称（连同其错误；对 `bt_stream` 按请求临时添加的种子标“临时”）、需要的大小、进度条、状态与
角色保持它的原因、下载与上传速率、按当前速率的剩余时间、分享率（已上传除以大小，与 `seed_ratio` 的计算一致）、peer
与做种数、分类。可按状态（全部状态或上述某一状态）、分类（全部分类、无分类或运行中客户端的某个 `categories`）与
名称的一部分筛选；计数显示“共 {total} 个种子，显示 {n} 个”。种子可逐个勾选或用**选择所显示的全部种子**全选，然后暂停、
恢复或删除——删除先确认，带一个“同时删除已下载的文件”复选框（不勾选则文件保留在磁盘上）——每个操作依次作用于所选
种子，并用提示说明数量与失败者。

**添加种子**打开**添加种子**对话框：**磁力链接**、**.torrent 文件**（最大 32 MiB）或由 fit 下载的 **URL**，一个
分类，**添加后暂停**，以及**按顺序下载分片（便于边下边播）**——顺序下载。备用速率在本页及种子各页的顶栏：一个选择框，
**自动（按计划）**、**开**或**关**，并显示现在是否生效（连同生效的速率，或“配置中没有 alt_speed：备用速率不会
生效”）。

**最近事件**卡片保留页面打开以来客户端的最近 30 个事件：种子已添加、已删除（删除时是否连同数据）、状态变化、已完成、
存储错误、peer 被封禁、做种上限、备用速率与 RSS 添加，另有“客户端事件被丢弃”与“错过了事件：已重新读取列表”；
完成与存储错误还会弹出提示。列表页每两秒读取一次列表以更新速率与进度（事件不携带这些）；同时经控制台的事件流（见下）
接收客户端事件，使添加、删除或状态变化即时显示。断线后带 `Last-Event-ID` 重连；没有事件流时（经管理端打开的成员、
流数已达上限、没有该角色的 fit）页面只轮询，并用徽标显示**实时**或**轮询**。角色未在此运行时页面会说明，并给出到
配置页的链接。

### 单个种子：概览、文件、Peer 与 Tracker

`#/bittorrent/torrent/<id>` 显示一个种子，分四个标签页（概览、文件、Peer、Tracker；后三个的地址是 `.../files`、
`.../peers`、`.../trackers`），页头是名称、状态、保持原因与分类，工具栏是**暂停**或**恢复**、**重新校验**与**删除**。

- **概览**：进度、速率、已下载与已上传字节、peer 与做种数、分享率与剩余时间、已有分片数；一张卡片列出 ID、v1 与 v2
  info hash、大小（不同时还有需要的大小）、目录、添加时间、保持原因，以及可复制的磁力链接。设置就地编辑：分类、
  **队列位置**（从 0（最前）开始：队列按此顺序启动种子）与**顺序下载**。**自身限制**——上传速率、下载速率、连接数、
  上传槽位、做种分享率与做种时长（时长如 `72h`；`-1s` 表示永久做种）——在客户端限制之下、取代其分类的限制，并显示
  当前生效的限制；**保存限制**一起保存，**清除限制**移除它们。
- **文件**：每个文件的路径、大小、进度与**优先级**——不下载、低、普通、高——可逐个设置，或对所选文件设置
  （将所选文件设为）。磁力链接的元数据尚未获取时还没有文件。
- **Peer**：每个已连接 peer 的地址、客户端、连接（主动或被动、uTP 或 TCP、加密、做种者、阻塞、冷落）、分片、速率与
  字节。
- **Tracker**：按层级排列的 tracker（只有前面的层级都没有应答时才会询问后面的层级），各带状态（正常、正在 announce、
  被拒绝、无应答或尚未联系，连同错误或警告与连续失败次数）、上次与下次 announce、间隔、peer，以及上次应答或 scrape 的
  做种数、下载数与完成数；hybrid 种子的 v2 哈希另有一张表。下面还有 **Peer 来源**——Tracker、DHT、Peer 交换、
  本地发现、手动添加、其他来源——各自启用或未启用，并给出找到与已连接的 peer；以及 **Web 种子**的状态、失败次数、
  下次重试与正在获取的分片。这个标签页每四秒读取一次。

### Tracker

tracker 页：总计——种子数与 peer 数相对其上限、announce 与 scrape 数、按原因分类的拒绝（不在白名单中、passkey
无效、被访问控制拒绝）——以及**谁可以 announce**（**私有**或**公开**、白名单数量或“无：接受所有种子”，以及 UDP
地址）。**Swarm** 每页 100 个：有白名单时，是白名单中的种子（按白名单顺序，无论是否正在跟踪）；公开 tracker 则是其
当前有 peer 的种子（按 info hash 排序）；每个显示做种者、下载者、完成数与是否已跟踪；可用**上一页**与**下一页**
翻页。私有 tracker 还有**用户的 passkey**：按用户查询 `media_auth` 用户的 passkey，用于该用户的 announce URL——
只在此显示给登录的人，从不保存；公开 tracker 没有 passkey，页面会说明。

### RSS

RSS 自动下载的可视页面。**订阅源**列出每个订阅源的名称与标题、URL、**启用**开关（在此设置过后标“在此设置，不论配置
如何”，并给出“恢复为配置”的链接）、间隔、上次轮询及其结果（成功、未变化、错误）与错误、下次轮询、已见条目数、
已添加与失败数，以及**立即轮询**（说明本次轮询做了什么：条目、新条目、添加、失败、留到下次）。**规则**列出每条规则
的开关、订阅源（或“所有订阅源”）、分类、记住的剧集数、已添加数与上次添加时间。**最近条目**列出已见条目（200 个），
可按订阅源与结果筛选——已添加、已存在、剧集重复、无规则匹配、无种子链接、失败——每条带其规则与到所添加种子的链接。
配置中没有 `rss` 时，页面说明到哪里添加。

### 发布

每个 `publish` 条目一张卡片：目录、模式、Web 种子、上次扫描时间、等待稳定或正在构建的文件数，以及该条目的**问题**
（被排除的内容与原因，从计数展开）。其种子按路径与 ID、大小、文件数、状态、构建时间与链接列出：可下载的 **.torrent**
文件、磁力链接及其复制按钮。发布的种子在客户端列表之外做种，因此不链接到单个种子页；没有发布条目时页面说明到哪里添加。

### BitTorrent 配置

整个 `bittorrent` 成员在一个表单里：`bittorrent.client`（`listen`、`data_dir`、`download_dir`、加密与 peer 发现、
磁盘池、`save_interval`、`limits`、`categories`、`watch_dir`、`url_timeout`、`alt_speed`、`access`、`rss` 与
`publish`）与 `bittorrent.tracker`（`listen`、`interval`、`peer_ttl`、各上限、`auth`、`allow` 与 `allow_files`、
`access` 与 `store`），带 UI、`.conf`、`.yaml`、`.json` 标签页。`bittorrent` 因此有自己的配置页，而不在“全局设置”
的“其他”页上；该成员下的保存问题链接到这里。

## API、fit bt 与事件

Admin API 在 `/bittorrent/` 下服务两个角色，位于其令牌与 `admin.access` 之后；控制台把它们转发为
`/api/m/admin/bittorrent/...`，`/node` 列出能力 `bittorrent`。其中有种子（`GET`/`POST /bittorrent/torrents`，以及
`GET`/`POST`/`PATCH`/`DELETE` `/bittorrent/torrents/<id>`，其 `POST` 执行 `{"action": "pause"}`、
`{"action": "resume"}` 或 `{"action": "recheck"}`，`PATCH` 更改分类、限制、队列位置、顺序下载或文件优先级）、一个
种子的文件、peer 与 tracker、`alt_speed`、`publish`（以及已发布种子的 `.torrent` 文件）、`rss`（订阅源、规则、
条目与立即轮询）、tracker 的 `stats`、`torrents` 与 `passkey`，以及给管理端用的 `summary`。种子不存在或角色不运行
答 `404`；请求有误 `400`；种子已存在 `409`；达到 `max_torrents` 为 `429`；同时有四个 .torrent URL 下载或 32 个
事件流在运行、或客户端不运行，为带 `Retry-After` 的 `503`；.torrent URL 下载失败为 `502`。

`fit bt` 经该 API 操作运行中 fit 的客户端（及其 RSS 自动下载），它像 `fit check` 与 `fit sync` 一样找到并
认证（`-c` 引导配置、`-timeout`、`-json`，以及 TLS 相关参数）：`fit bt ls`、`add`、`info`、`pause`、`resume`、`rm`、`limit`、
`alt` 与 `rss`（其 `items`、`poll`、`enable`、`disable` 与 `reset` 子命令）；`add` 接受磁力链接、http 与 https
URL 和 .torrent 文件，可带 `-paused`、`-sequential`、`-up` 与 `-down`，速率为字节/秒，可带 `K`、`M` 或 `G`。
Admin API 拒绝某一个时它继续尝试其余，最后以 1 退出。

`GET /bittorrent/events` 以 server-sent events 推送客户端事件；控制台用自己的一条路由
`GET /api/m/admin/bittorrent/events` 在会话之后提供它们，这正是各页面读取的流。事件包括种子已添加与已删除、状态变化
（`previous` 到 `state`）、备用速率开始或停止生效、已完成、存储错误、peer 被封禁、做种上限与 RSS 添加；流带
`Last-Event-ID` 重连（`reset` 事件表示错过的事件已不存在，页面会重新读取列表）；慢客户端等待超过 256 个事件后收到
`lagged` 并被断开；同时最多 32 个流。事件也写入日志，logger 为 `bittorrent`。

指标在 Admin API 的 `/metrics` 与 OpenTelemetry 导出中：客户端的字节、速率、peer、按状态与分类的种子、哈希失败、
封禁、备用速率与 RSS 轮询（`fit_bittorrent_*`），以及 tracker 的种子、peer、announce、scrape 与按原因的拒绝
（`fit_bittorrent_tracker_*`）。

[助手](14-assistant.zh-CN.md)读取这两个角色并提议对种子的修改：`bt_torrents`、`bt_torrent`、`bt_tracker_stats` 与
`bt_rss` 为 `read` 级，`bt_add_propose`、`bt_pause_propose` 与 `bt_resume_propose` 为 `propose` 级；暂停与恢复
按 ID 指名种子，最多 100 个。没有 passkey 会到达模型：种子的磁力链接被略去，tracker 的 URL 只保留 scheme、host 与
最后一段路径，Web 种子的 URL 去掉用户与查询，peer 去掉 ID，也没有工具读取用户的 passkey。propose 工具把步骤加入
对话中唯一待审的提案，由人在第二次确认后经 Admin API 应用。在管理端，读取类工具接受 `member`。

## 参数

只列关键参数；完整字段表见[配置参考：BitTorrent](../reference/config.md#bittorrent)与模块自己的
[schema/doc](../../schema/doc/bittorrent.md)。

### `bittorrent.client`

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `listen` | —（必填） | peer 访问客户端的地址，TCP 与 UDP 用同一个（uTP、DHT 与 UDP tracker 共用）；`:0` 为两者选同一个空闲端口 |
| `data_dir` | —（必填） | 状态：`torrents.json`、`torrents/<id>.torrent` 与 `resume/<id>.resume`；重启后无需重新哈希磁盘上的内容即可续传 |
| `download_dir` | `<data_dir>/downloads` | 此后添加的种子下载到哪里；种子保留添加时所在的目录 |
| `encryption` | `prefer` | `allow`、`prefer` 或 `require`：连接如何使用消息流加密（MSE） |
| `dht`、`pex`、`utp` | 开 | 主网 DHT、peer 交换与 uTP |
| `lsd` | 关 | 本地服务发现 |
| `disk_workers` | CPU 数，至多 8 | 读写文件并计算分片哈希的 goroutine，从不占用 fib 的 worker |
| `disk_queue_bytes` | 64 MiB | 等待写入的已接收数据块；超过后不再从发送它们的 peer 读取 |
| `save_interval` | 5s | 有变化时至多多久保存一次续传数据 |
| `watch_dir` | — | 每 `watch_interval`（10s）查看一次 `.torrent` 与 `.magnet` 文件；以分类命名的子目录归入该分类；取走的文件移入 `.done` 或 `.failed` |
| `watch_max_files` | 100 | 一次查看最多取走的文件数 |
| `url_timeout` | 30s | 下载一个 .torrent URL 的全部时间（最多 32 MiB、5 次重定向、同时 4 个） |
| `categories` | —（至多 1000） | 每个：`name`、`dir`（`<download_dir>/<name>`）、`media_library`、`limits` |
| `alt_speed` | — | 在某个每周 `schedules` 生效时取代客户端速率的 `upload_rate` 与 `download_rate`（至多 100 项）；API 可设为开、关或自动 |
| `access` | 所有地址 | 客户端的 peer 屏蔽列表：`deny`/`allow` 与 `deny_files`/`allow_files`（eMule `ipfilter.dat`、PeerGuardian `.p2p`），立即生效 |
| `rss` | — | 下面所述的订阅源与规则 |
| `publish` | —（至多 64） | 发布为 hybrid 种子的目录 |

### `bittorrent.client.limits`

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `upload_rate`、`download_rate` | 不限 | 每秒字节数，所有种子合计的载荷 |
| `max_conns` | 200 | 客户端的 peer 连接数 |
| `max_conns_per_torrent` | 50 | 一个种子的 peer 连接数 |
| `max_half_open` | 32 | 正在进行的拨号数 |
| `upload_slots` | 4 | 一个种子同时上传给几个 peer，另有 1 个乐观 unchoke |
| `seed_ratio`、`seed_time` | —（永久） | 种子已上传达到大小的多少倍、或做种多久后暂停 |
| `max_torrents` | 1000 | 保存的种子数（运行或暂停）；再多一个被拒绝 |
| `max_active_downloads`、`max_active_seeds` | —（无队列） | 同时下载与做种的种子数；其余按列表顺序排队 |
| `min_free_bytes` | 1 GiB | 下载要留出的磁盘空间 |

### `bittorrent.client.rss`

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `feeds[]` | —（至多 100） | `name`、`url`（必填）、`interval` 15m（至少 1m）、`enabled` 开、`max_items` 200、`headers`/`headers_env` |
| `rules[]` | —（至多 200） | `name`、`feeds`、`patterns`/`exclude`（对整条标题的通配符，或 `re:` 加正则）、`category`、`paused`、`min_size`/`max_size`、`episodes`、`max_per_poll` 5、`enabled` 开 |
| `history_size` | 1000 | 每个订阅源在 `data_dir/rss.json` 中记住的条目数 |
| `max_feed_bytes`、`timeout` | 4 MiB、30s | 更大的订阅源使本次轮询失败；整个下载的时间 |

### `bittorrent.client.publish[]`

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `name`、`dir` | —（必填） | 条目的名称，以及发布的目录 |
| `mode` | `per_file` | 每个文件一个种子，或 `whole_dir`：整个目录一个种子，以其最后一段命名 |
| `web_seed` | — | `dir` 对外提供服务的 URL（如同 `static` 路由），写进每个种子作为其 Web 种子（BEP 19） |
| `announce` | — | tracker URL（http、https 或 udp），每个自成一个层级 |
| `private` | 关 | 把种子标为私有（BEP 27） |
| `files`、`exclude` | — | 模式：不含 `/` 匹配文件名，含 `/` 匹配 `dir` 下的路径，`**` 匹配任意多级目录 |
| `piece_length` | 自动挑选 | 16 KiB 到 64 MiB 的 2 的幂；不设置时按每个种子的大小挑选 |
| `check_interval` | 10s | 多久重新遍历一次 `dir`；改变的文件得到新种子 |
| `max_files`、`max_total_bytes` | 1000、1 TiB | 一个条目的上限 |
| `max_hashing` | 1 | 同时构建的种子数 |

### `bittorrent.tracker`

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `listen` | —（仅 HTTP） | UDP announce 与 scrape 到达的地址（BEP 15） |
| `interval`、`min_interval` | 30m、— | 告知客户端多久 announce 一次，以及它们最早多久后可自行再次 announce |
| `peer_ttl` | 1h | peer 最后一次 announce 后保留多久；长于 `interval` |
| `max_torrents` | 100000 | 同时跟踪的种子数；再多一个以“tracker 已满”拒绝 |
| `max_peers_per_torrent` | 10000 | 一个种子保留的 peer 数 |
| `max_peers` | 500000 | 所有种子合计的 peer 数，每个约 150 字节 |
| `max_numwant`、`max_scrape` | 200、64 | 一次 announce 得到的 peer 数，以及一次 HTTP scrape 最多可指名的种子数 |
| `auth` | —（公开） | tracker 接受其 passkey 的 `media_auth` 策略：设置后为带 passkey 的私有 tracker；授权须能播放 `bittorrent/<十六进制 info hash>` |
| `allow`、`allow_files` | —（所有种子） | 跟踪的 info hash：v1 为 40 位，v2 为 64 位；文件中每行一个，`#` 为注释 |
| `access` | 所有地址 | tracker 服务的客户端地址 |
| `store` | —（本实例内存） | Redis 6.2+（或 Valkey）让多个实例共享 swarm：`redis`、`prefix` `fit/bittorrent/tracker/`、`request_timeout` 2s、`refresh` 5s、`queue` 65536、`cache_torrents` 4096、`cache_peers` 100 |

## .conf 配置

一份最小但完整的配置：一个下载并做种的客户端，一个 passkey 来自 `media_auth` 策略的私有 tracker，一个分类与一条 RSS
规则，一个发布的目录，以及 tracker 的 HTTP announce 与已发布 .torrent 文件的路由。它能通过
`./bin/fit validate -c` 校验。

```fit
listener web :8080 http/1.1 h2c

# tracker 的 passkey：静态令牌，或用户的 passkey，后者由 Admin API
# 从 secret 和用户名派生。
media_auth members {
	secret replace-with-a-long-random-secret
	tokens seeder {
		token replace-with-a-long-random-token
		play bittorrent/*
	}
	users alice {
		password replace-with-a-password
		play bittorrent/*
	}
}

bittorrent {
	client {
		listen :6881
		data_dir /var/lib/fit/bittorrent
		download_dir /srv/downloads
		watch_dir /srv/torrents/watch
		limits {
			upload_rate 1048576
			max_active_downloads 3
			max_active_seeds 5
			min_free_bytes 10GiB
		}
		categories iso {
			dir iso
			limits { seed_ratio 1 }
		}
		alt_speed {
			upload_rate 131072
			download_rate 524288
			schedules { days weekdays; from 08:00; to 18:00 }
		}
		access {
			deny_files /etc/fit/level1.p2p
		}
		rss {
			feeds shows { url https://example.com/rss; interval 15m }
			rules show {
				patterns "*Show Name*1080p*"
				exclude *hevc*
				category iso
				episodes true
			}
		}
		publish releases {
			dir /srv/releases
			web_seed https://example.com/releases/
		}
	}
	tracker {
		listen :6969
		auth members
		allow_files /etc/fit/torrents.txt
	}
}

route tracker { match { path /tracker/* }; handler bt_tracker }
route torrents { match { path /torrents/* }; handler bt_publish }
```

分类 `iso` 的种子下载到 `/srv/downloads/iso`；任何下载在磁盘将只剩不足 10 GiB 时暂停；备用速率在工作日 08:00 到
18:00 生效；放进 `/srv/torrents/watch` 的 `.torrent` 或 `.magnet` 文件会被添加；剧集规则把它挑中的内容添加到 `iso`；
`/srv/releases` 以 `https://example.com/releases/` 为 Web 种子发布；tracker 是私有的，其 HTTP announce 与 scrape
在 `/tracker/announce`（以及 `/tracker/<passkey>/announce`）提供，UDP 的在 `:6969`；`/torrents/` 提供已发布种子的
.torrent 文件与磁力链接。`media_auth` 的用户与令牌须能播放 `bittorrent/<info hash>` 才能 announce；`allow_files`
限制跟踪哪些种子。

示例的两点细节：Web 种子的 URL 所需的 `static` 路由
（`route { match { path /releases/* }; handler static { root /srv/releases; strip_prefix /releases } }`）被略去，因为
`fit validate` 会从磁盘读取 `static` 的 root；要提供 Web 种子请加上它。`allow_files` 与 `deny_files` 指名的是在
应用配置时读取的文件——列表文件改变会重新应用配置。

## 相关文档

- [BitTorrent](../bittorrent.zh-CN.md)：客户端、tracker、[发布](../bittorrent.zh-CN.md#发布)、
  [Admin API](../bittorrent.zh-CN.md#admin-api)、[事件](../bittorrent.zh-CN.md#事件)、
  [`fit bt`](../bittorrent.zh-CN.md#fit-bt)、指标、共享 store、互通与[限制与安全](../bittorrent.zh-CN.md#限制与安全)。
- [控制台与 store](../console.zh-CN.md#bittorrent)：BitTorrent 菜单在其中的位置与客户端的事件流。
- [配置参考：BitTorrent](../reference/config.md#bittorrent)与[处理器](../reference/config.md#handlers)：
  [`bt_stream`](../reference/config.md#http-handlers-bt_stream)、
  [`bt_publish`](../reference/config.md#http-handlers-bt_publish)与
  [`bt_tracker`](../reference/config.md#http-handlers-bt_tracker)。
- 模块自己的说明与示例：[schema/doc/bittorrent.md](../../schema/doc/bittorrent.md)。
- [决定 037](../decisions/037-bittorrent.zh-CN.md)。
- [集群管理](../cluster.zh-CN.md#bittorrent成员的种子与分发)与[集群一章](12-cluster.zh-CN.md)：成员的 BitTorrent
  页与分发。
- [反向代理](15-reverse-proxy.zh-CN.md)：`bt_stream`、`bt_publish` 与 `bt_tracker` 处理器，以及把已发布文件作为
  Web 种子提供的 `static` 路由。
- [助手](14-assistant.zh-CN.md)：BitTorrent 工具及其级别。
- [数据同步](26-sync.zh-CN.md)：本模块之前的模块。
