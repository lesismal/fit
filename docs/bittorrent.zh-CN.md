# BitTorrent

[English](bittorrent.md) | [简体中文](bittorrent.zh-CN.md)

fit 在网关旁运行一个 BitTorrent 客户端：`bittorrent.client` 角色在 fit 自己的 fib 引擎上、以 TCP 和 UDP 共用的一个端口
下载和做种，并在重启后保留种子（[决定 037](decisions/037-bittorrent.zh-CN.md)）。它就是 fib 的客户端
（[fib 的 BitTorrent 文档](https://github.com/lesismal/fib/blob/main/docs/bittorrent.md)）：v1、v2 和混合种子，磁力链接，
TCP 和 uTP，MSE 加密，DHT，对端交换，本地服务发现，HTTP 和 UDP tracker，以及 web seed。本页讲这个角色：配置、状态保存
在哪里、添加种子（磁力链接、.torrent 文件和 URL、`watch_dir`）、分类、队列、磁盘检查、备用速率、每个种子的限制、屏蔽
列表、重载改变什么，以及它的上限。边下边播、tracker、发布、Admin API 和页面，随[开发计划](dev-plan.zh-CN.md)中之后的
条目而来。

默认关闭：不写 `bittorrent.client` 时，fit 不打开 BitTorrent 端口，也不保存状态。

```fit
media_library films { roots /srv/downloads/films }

bittorrent {
	client {
		listen :6881
		data_dir /var/lib/fit/bittorrent
		download_dir /srv/downloads
		watch_dir /srv/torrents/watch
		limits {
			upload_rate 1048576
			seed_ratio 2
			max_active_downloads 3
			max_active_seeds 5
			min_free_bytes 10GiB
		}
		categories { name films; dir films; media_library films; limits { seed_ratio 1 } }
		categories { name iso; dir /srv/iso }
		alt_speed {
			upload_rate 131072
			download_rate 524288
			schedules { days weekdays; from 08:00; to 18:00 }
		}
		access {
			deny_files [/etc/fit/level1.p2p]
		}
	}
}
```

## 客户端

`listen` 是 TCP 和 UDP 共用的一个主机和端口：对端经 TCP 连接，uTP、DHT 和 UDP tracker 的应答共用该 UDP 端口。客户端
与其他角色一样运行在监听器的引擎上（fib 的 `Config.SharedEngine`），共享 fit 的事件循环和 worker；停止角色会移除它的两个
绑定并关闭其连接，fit 的其余部分照常运行。

默认值来自决定 037：加密 `prefer`（先用 MSE，对端拒绝时用明文），DHT、对端交换和 uTP 开启，本地服务发现关闭，peer ID
前缀为 fib 的 `-FB0001-`。在测试或私有网络中，`dht: false` 使客户端不连接公共 DHT 路由节点。

不阻塞 fib 的 worker：收到的块在客户端由 `disk_workers` 个 goroutine 组成的磁盘池（CPU 数，最多 8）上复制并写入，
上传读取的分片和分片哈希也在那里进行；等待写入的块受 `disk_queue_bytes`（64 MiB）约束，超过后停止读取发送它们的对端，
直到写完一半。状态文件由角色自己的 goroutine 读写，从不在 worker 上。

## data_dir 中的状态

| 文件 | 内容 |
| --- | --- |
| `torrents.json` | 列表，按队列顺序：每个种子的 ID（v1 info hash，或截断的 v2 hash）、名称、来源磁力链接、下载目录、分类、自己的限制、文件优先级、是否暂停或顺序下载、是否已完成，以及添加时间；还有 API 对备用速率的选择 |
| `torrents/<id>.torrent` | 其 .torrent 文件：添加时的字节，或磁力链接的种子从对端收到 info 后的元数据 |
| `resume/<id>.resume` | 其续传数据（fib 的 `Torrent.ResumeData`）：已有的分片，以及每个文件的大小和修改时间 |

每个文件都先写入临时文件、同步后再改名，崩溃后留下的是完整的旧文件或新文件；启动时删除遗留的临时文件。列表的变化
（添加、移除、暂停、恢复种子，改变其优先级或顺序模式）立即保存；续传数据在上次保存后有分片校验通过时，至多每
`save_interval`（5s）保存一次；角色停止或新建客户端时全部保存。

启动时每个种子都带着续传数据加回：其中列出的、所在文件大小和修改时间未变的分片直接视为已有而不读取，因此重启不会重新
哈希磁盘上的数据。因做种上限而暂停的种子保持暂停。

无法读取的内容有界并会报告：

- `torrents.json` 无法解析、版本更新、列出超过 100000 个种子、同一种子列出两次或大于 64 MiB 时，角色不启动，错误中给出
  该文件，而不是在下次保存时被空列表覆盖。修复或移走它后再次应用配置。
- .torrent 文件无法读取、或既无 .torrent 文件也无磁力链接的种子，带着错误保留在列表中并照常保存，直到被移除；其他种子
  照常运行。
- 无法读取的续传数据被忽略，该种子的数据全部重新校验。

## 下载

添加的种子下载到 `download_dir`（不写时为 `<data_dir>/downloads`），每个种子的文件按各客户端通用的布局放在其下。fib 的
metainfo 拒绝含 `..`、`.`、空或含 `/` 的路径段，其文件存储把每个名称处理为系统可用的名称，因此种子不会写到目录之外。
`download_dir` 更改时，已有种子保留添加时的目录。存储错误（如文件无法写入）不会停止种子；它出现在种子的状态中，直到
再有分片校验通过，并写入日志。

## 添加种子

- **磁力链接或 .torrent 文件**，经角色的 API（`Add`），可指定分类、是否暂停及自己的限制。
- **.torrent 的 URL**（`AddURL`），http 或 https：用 fib 的异步 HTTP 客户端在 fit 的引擎上下载，回调只把应答交出去，
  因此没有 fib 的 worker 等待。最多跟随 5 次重定向（包括重定向到磁力链接）；整个下载（连接与响应体）受 `url_timeout`
  （30s）约束，响应体最多 32 MiB，同时最多 4 个下载（再多一个立即拒绝），因此持有的 .torrent 响应体最多 128 MiB。
- **`watch_dir`**：每 `watch_interval`（10s）查看一次其中的 `.torrent` 文件和 `.magnet` 文件（第一个非空行是磁力链接）。
  直接在其中的文件不带分类添加，在以分类命名的子目录中的文件归入该分类；其他文件、子目录和以 `.` 开头的名称不动。
  文件在连续两次查看中大小和修改时间都不变后才被取走，正在写入的文件不会被读到一半。添加成功或其种子已存在的文件移入
  旁边的 `.done` 子目录；无法添加的（不是种子、不是链接、超过大小上限）移入 `.failed`，旁边写一个说明原因的
  `<name>.error` 文件；之后可能加得上的（已达 `max_torrents`、状态写不进去）留在原处，每次查看再试。每次查看最多取
  `watch_max_files`（100）个文件，并只读取有限数量的目录项。

## 分类

`categories` 为种子指定目录和限制。分类的 `dir` 为相对路径时位于 `download_dir` 之下（不写时为
`<download_dir>/<name>`），且不能跑出它；绝对路径的 `dir` 按原样使用。其 `limits` 是每个种子的限制（fib 的
`TorrentLimits`：受客户端速率约束的速率、连接数、上传槽位、做种比例和时间），种子自己的限制取代它所设置的那些；
0 采用客户端的限制，负的连接数、做种比例或做种时间表示该种子不受限。`media_library` 指定一个媒体库，该分类的种子
完成时重新扫描它（`Library.Rescan`），使下载到其根目录中的内容出现在目录（catalog）中；一个种子只算完成一次，完成
状态跨重启保存在状态中，文件优先级改变后才会再算一次。`SetCategory` 把种子移到另一个分类的限制和媒体库下；其文件仍
留在添加时的目录中。

## 队列

`limits.max_active_downloads` 和 `limits.max_active_seeds` 限制同时下载和做种的种子数；超出的种子排队，由角色暂停
（状态 `queued`），按列表顺序排列，`SetQueuePosition` 改变顺序，状态文件保存顺序。角色每秒一次、并在添加、暂停、恢复、
移除或移动后立即，启动列表中排得下的前几个种子并暂停其余的，因此种子完成、暂停或移除都会给下一个让出位置；被移到正在
运行的种子之前的种子会取代它。获取磁力链接元数据或仍在校验数据的种子算作下载中。经 API 暂停或因做种上限暂停的种子不计数，
队列也从不启动它们。不写时不排队。

## 磁盘空间

`limits.min_free_bytes`（1 GiB，与媒体库上传保留的相同）是下载在其目录所在磁盘上留出的空间：只有当磁盘上空闲的空间
够它还要下载的量再加这么多时，种子才开始下载（同一次查看中先启动的种子所需的量也计入），正在下载的种子在剩余空间少于
`min_free_bytes` 时暂停，状态中的错误说明还有多少空闲。角色每秒再看一次，空间够了就自行启动它。做种不需要空间，不检查。
无法得知空闲空间的系统（Linux、macOS、FreeBSD、Windows 以外）不检查。

## 备用速率

`alt_speed` 设置上传和下载速率（0 表示不限），在它的某个 `schedules` 生效期间取代 `limits` 的速率：每个是一个每周的
时间段，`days` 是开始的日子（`mon` 到 `sun`、`weekdays`、`weekend`；不写时每天），`from` 和 `to` 为 `time_zone`
（默认本地）中的 `HH:MM`，`to` 早于 `from` 的时间段跨过午夜进入第二天。角色每秒检查一次，更改就地生效
（`Client.SetLimits`）。API 的 `SetAltSpeed` 可不顾时间表地打开（`on`）或关闭（`off`）它们，或回到 `auto`；该选择跨
重启保存在状态中。

## 屏蔽列表

角色的 `access` 和全局的 `access` 决定它与哪些对端打交道（fib 的 `Config.Filter`）：被拒绝的对端经 TCP 连接后立即
关闭，其 uTP 连接被忽略，从不被拨号，也得不到 DHT 应答。它们的 `deny_files` 和 `allow_files`（与每个模块的相同）除地址、
前缀和范围外，还读取 eMule 的 `ipfilter.dat` 行（`first - last , level , name`，level 大于 127 的行不列入）和
PeerGuardian 的 `.p2p` 行（`name:first-last`），逐行按内容区分（[access](reference/config.md#access)）。策略或列表文件
的更改立即生效：角色重新过滤（`Client.Refilter`），关闭现在被拒绝的已连接对端。

## 重载

| 更改 | 结果 |
| --- | --- |
| `limits` | 就地生效（fib 的 `Client.SetLimits`）：新速率在四分之一秒内生效，对端保持连接，超出调低后连接上限的除外 |
| `access` | 就地生效：客户端的过滤从下一个对端起按新策略判断，现在被拒绝的已连接对端被关闭（`Client.Refilter`） |
| `save_interval`、`console`、`download_dir`、`watch_dir`、`watch_interval`、`watch_max_files`、`url_timeout` | 就地生效 |
| `categories` | 就地生效：限制立即作用于该分类的种子，新的 `dir` 作用于此后添加的种子；分类被删除的种子保留其目录、失去其限制 |
| `max_active_downloads`、`max_active_seeds`、`min_free_bytes`、`alt_speed` | 就地生效，种子按它们立即启动或暂停 |
| `listen`、`encryption`、`dht`、`pex`、`utp`、`lsd`、`disk_workers`、`disk_queue_bytes` | 新建客户端：关闭旧客户端、保存每个种子的续传数据，再把种子无需校验地加入新客户端。新客户端无法创建（端口被占）时，重新创建旧客户端，重载报告错误 |
| `data_dir` | 像重启一样，在新目录的状态上新建客户端；旧目录保留已保存的内容 |

## 上限

| 上限 | 默认值 |
| --- | --- |
| `limits.max_torrents`，持有的种子数（运行或暂停） | 1000（最多 100000） |
| `limits.max_conns`，对端连接数 | 200 |
| `limits.max_conns_per_torrent` | 50 |
| `limits.max_half_open`，进行中的拨号 | 32 |
| `limits.upload_slots`，每个种子 | 4 |
| `limits.upload_rate`、`limits.download_rate` | 不限 |
| `limits.seed_ratio`、`limits.seed_time` | 一直做种 |
| `limits.max_active_downloads`、`limits.max_active_seeds` | 不排队 |
| `limits.min_free_bytes`，下载留出的磁盘空间 | 1 GiB |
| `categories`、`alt_speed.schedules` | 最多 1000、100 |
| 经 URL 下载的 .torrent 文件；同时进行的 URL 下载；重定向 | 32 MiB；4；5 |
| `url_timeout` | 30s |
| `watch_max_files`，每次查看 `watch_dir` 取走的文件数 | 100（最多 10000） |
| `disk_queue_bytes`，等待写盘的已接收块 | 64 MiB |
| .torrent 文件 | 128 MiB（fib 的 metainfo 上限） |
| 磁力链接 | 64 KiB |
| `torrents.json`、续传文件 | 各 64 MiB |

## 角色的 API

角色有一组 Go API，供之后的条目（Admin API、`fit bt`、页面）使用：`Add` 磁力链接或 .torrent 字节（可带分类和限制），
`AddURL`，`List`（按队列顺序），`Get`（含文件），`Pause`，`Resume`，`Remove`（可同时删除数据），`SetFilePriority`，
`SetSequential`，`SetCategory`，`SetTorrentLimits`，`SetQueuePosition`，`AltSpeed` 和 `SetAltSpeed`，`Save`，以及取得 fib
种子的 `Torrent`。状态给出种子的分类、自己的和实际生效的限制、队列位置、是否已完成，以及角色暂停它的原因（`queued`、
`disk`）。
设置了角色的控制台时，它目前只读地提供 `GET /api/torrents` 和 `GET /api/torrents/<id>`；显示该角色模块的任何控制台以
`/api/m/bittorrent.client/torrents` 和 `/api/m/bittorrent.client/torrents/<id>` 提供它们。
