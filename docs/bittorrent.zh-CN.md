# BitTorrent

[English](bittorrent.md) | [简体中文](bittorrent.zh-CN.md)

fit 在网关旁运行 BitTorrent（[决定 037](decisions/037-bittorrent.zh-CN.md)），运行在 fit 自己的 fib 引擎上，使用 fib 的
BitTorrent 包（[fib 的 BitTorrent 文档](https://github.com/lesismal/fib/blob/main/docs/bittorrent.md)）：v1、v2 和混合种子，
磁力链接，TCP 和 uTP，MSE 加密，DHT，对端交换，本地服务发现，HTTP 和 UDP tracker，以及 web seed。它分四部分：

- **`bittorrent.client` 角色**以 TCP 和 UDP 共用的一个端口下载和做种，并在重启后保留种子：种子可从磁力链接、.torrent
  文件和 URL 或 `watch_dir` 添加；分类有自己的目录、限制和媒体库；队列；磁盘检查；按时间表的备用速率；每个种子的限制；
  屏蔽列表。
- **发布**：客户端的 `publish` 条目把目录中的文件生成 hybrid 种子并做种，以 fit 自己的静态文件作为 web seed，跟随磁盘
  变化；`bt_publish` 处理器提供它们的 .torrent 文件和磁力链接。
- **`bt_stream` 处理器**在种子下载的同时经 HTTP Range 播放其中的文件，并可按磁力链接即时添加种子。
- **`bittorrent.tracker` 角色**是 HTTP（经 `bt_tracker` 处理器）和 UDP 的 tracker，可公开，也可用 passkey 设为私有，并带
  允许列表。

它们经 [Admin API](#admin-api)、其[事件流](#事件)、[`fit bt`](#fit-bt)、工作台的[页面](#页面)和[助手](#助手)操控，由[指标](#指标)度量。它们与 libtorrent 和 anacrolix 做过互通检查（[互通](#互通)），并在基准主机上测量过（[性能](#性能)）；
[限制与安全](#限制与安全)说明在互联网上运行时要注意什么。

默认关闭：不写 `bittorrent.client` 时，fit 不打开 BitTorrent 端口，也不保存状态；不写 `bittorrent.tracker` 时，它不跟踪任何种子。

包含所有部分的配置（fit 的 .conf 格式；JSON 和 YAML 中相同，与所有配置一样；
[fit-examples 的 `bittorrent`](https://github.com/lesismal/fit-examples/tree/main/bittorrent) 是它可运行的版本）：

```fit
listener web :8080 http/1.1 h2c
admin { address 127.0.0.1:2019 }

media_library films { roots /srv/downloads/films }

# 私有 tracker 的 passkey：静态令牌，或用户的 passkey，
# 后者由 Admin API 从 secret 和用户名派生。
media_auth members {
	secret replace-with-a-long-random-secret
	tokens seeder { token replace-with-a-long-random-token; play bittorrent/* }
	users alice { password replace-with-a-password; play bittorrent/* }
}
# 谁可以在 /bt/ 播放种子的文件，并在那里按磁力链接添加种子。
media_auth viewers {
	tokens player { token replace-with-another-long-random-token; play bt/*; publish bt/* }
}

bittorrent {
	client {
		listen :6881
		data_dir /var/lib/fit/bittorrent
		download_dir /srv/downloads
		watch_dir /srv/torrents/watch
		encryption prefer
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
		publish {
			name releases
			dir /srv/releases
			web_seed https://example.com/releases/
			announce https://example.com/tracker/announce
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
route releases {
	match { path /releases/* }
	handler static { root /srv/releases; strip_prefix /releases }
}
route stream { match { path /bt/* }; handler bt_stream { auth viewers; magnet true } }
```

这里客户端为 `/srv/releases` 做种，以 `https://example.com/releases/` 作为 web seed，并使用 tracker 的公开 announce URL；
私有 tracker（`auth`）需要在该 URL 中放 passkey，发布种子的每个下载者便会共用它（见[限制与安全](#限制与安全)）。

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
| `torrents.json` | 列表，按队列顺序：每个种子的 ID（v1 info hash，或截断的 v2 hash）、名称、来源磁力链接、下载目录、分类、自己的限制、文件优先级、是否暂停或顺序下载、是否已完成、是否由 `bt_stream` 即时添加（`temporary`），以及添加时间；还有 API 对备用速率的选择 |
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

## 发布

`publish` 条目把目录发布成种子（I202）。每个条目用 fib 的 `metainfo.Builder` 为其 `dir` 生成 hybrid 种子：每个文件一个
单文件种子（`per_file` 模式，默认），或整个目录一个种子、以目录的最后一段命名（`whole_dir` 模式），并在文件所在处做种：
种子的存储在原处读取文件、拒绝一切写入，因此校验失败的分片绝不会被下载并覆盖发布的文件，移除种子也绝不删除其文件。
发布的种子一直做种，不受 `limits.seed_ratio` 和 `seed_time` 影响；它们不在角色的种子列表（`List`）中，也不计入
`max_torrents`，由 `bt_publish` 处理器提供。

```fit
bittorrent {
	client {
		listen :6881
		data_dir /var/lib/fit/bittorrent
		publish {
			name releases
			dir /srv/releases
			web_seed https://example.com/releases/
			announce https://example.com/announce
			files *.iso *.tar.gz
		}
	}
}

route {
	match { path /releases/* }
	handler static { root /srv/releases; strip_prefix /releases }
}

route { match { path /torrents/* }; handler bt_publish }
```

- **`web_seed`** 是提供 `dir` 的 URL（如上面 fit 自己的 `static` 路由），作为 web seed（BEP 19）写入每个种子：`per_file`
  模式下是文件自己的 URL（`https://example.com/releases/sub/a.iso`），`whole_dir` 模式下是该 URL 的上一级，因为客户端请求
  `<url>/<name>/<path>`，而种子以目录命名；URL 不以目录名结尾的 `whole_dir` 条目会被配置检查拒绝。这样客户端可以从 fit 的
  做种、从 web seed、或两者一起下载发布的文件。
- **`announce`** 是 tracker URL（http、https、udp），每个自成一层，例如 fit 自己的 `bt_tracker` 路由；**`private`** 把种子
  标为私有（BEP 27）。没有 tracker 时，peer 经 DHT 找到这些种子。
- **`piece_length`** 是 16 KiB 到 64 MiB 之间的 2 的幂，或按每个种子的大小选取（少于 2048 个分片，最大 16 MiB）。
- **`files`** 和 **`exclude`** 是模式，与媒体库的相同：不含 `/` 匹配名称，含 `/` 匹配在 `dir` 下的路径，`**` 匹配任意层
  目录。名称以点开头的文件和符号链接总是不发布，后者是因为链接可能发布 `dir` 之外的内容；客户端存储会改写的名称（Windows
  上的 `<>:"\|?*`）以及 `per_file` 模式下的空文件也不发布。

**跟随磁盘。** 每个条目在自己的 goroutine 上每隔 `check_interval`（10s）遍历一次 `dir`。文件以大小、修改时间、状态改变时间
和 inode 识别，与静态文件相同，因此被另一个大小和时间都相同的文件替换也能发现。新增或改变的文件在一个 `check_interval`
内不再变化后才生成种子（其状态改变时间已经这么久，或上一次遍历看到的相同），以免对仍在复制的文件做哈希；哈希在条目的
`max_hashing`（1）个 goroutine 之一上进行，从不在 fib worker 上，且只有在哈希之后文件的标记仍未改变时才保留结果。条目停止或
改变时，生成在读完一兆字节之内停止（fib 的 `Builder.BuildContext`），且每次只保留一个分片的 v2 叶子哈希，而不是整个文件的。改变或
删除的文件立即停止其种子，改变的文件在稳定后得到 info hash 不同的新种子；`whole_dir` 模式下任何变化都会取代种子。生成
失败的文件（无法读取）会被报告，在文件改变前不再重试。无法读取的目录会被报告，其种子保持原样；目录中无法读取的部分下的
种子也一样。

**状态。** 种子保存在 `data_dir/publish/<name>/` 中：`index.json`（每个种子的文件及其标记）、`<id>.torrent`，以及做种后保存
一次的续传数据 `<id>.resume`。重启时从这里取回文件标记未变的种子及其续传数据，既不重新生成也不重新校验；只改变条目的上限
或 `check_interval` 的 reload 也一样。改变 `dir`、`mode`、`web_seed`、`announce`、`private` 或 `piece_length` 会重新生成种子
（种子的字节变了）；从配置中移除的条目会停止其种子并删除其状态，从不删除其文件。**文件只哈希一次**：刚生成的种子以"已完整"
方式加入客户端（fib 的 seed 模式，`AddOptions.Seed`），不读取任何数据，每个分片在 peer 第一次请求时才校验；校验失败的分片
报告为哈希失败，并不再提供。之后保存的续传数据把尚未校验的分片标为未校验，因此重启后同样在第一次提供时校验。

**`bt_publish` 处理器** 在以 `/` 或 `index.json` 结尾的路径上返回 JSON 索引：每个条目（`publish` 指定提供哪些，不设置时为
全部）的种子，各带路径、名称、大小、info hash、磁力链接、状态（`seeding`、`checking`，没有客户端运行时为 `stopped`）以及
相对于索引的 `torrent_url` 和 `magnet_url`，等待生成的文件数（`pending`），以及未发布的内容和原因（`problems`，最多 20 条）。
`<id>.torrent` 是 .torrent 文件，`<id>.magnet` 是文本形式的磁力链接，ID 是十六进制的 v1 info hash。没有 `bittorrent.client`
运行时它返回 `503`，因此在没有客户端的配置中保留这条路由也是安全的。它读取条目的快照，不取角色的任何锁。

| 上限 | 默认值 |
| --- | --- |
| `publish`，条目数 | 最多 64 |
| `max_files`，一个条目发布的文件数 | 1000（最多 100000） |
| `max_total_bytes`，这些文件的大小之和 | 1 TiB |
| `max_hashing`，每个条目同时生成的种子数 | 1（最多 16） |
| 一次遍历 | 10 × `max_files` + 10000 个条目 |
| `index.json` | 64 MiB |

超过 `max_files` 或 `max_total_bytes` 时，`per_file` 模式按路径发布前面的文件并报告其余的；`whole_dir` 模式什么也不发布并
报告。两个条目共有的文件（或一个文件与手动添加的种子相同）只做种一次；第二个连同错误一起列出。

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
`AddURL`，`List`（按队列顺序），`Get`（含文件），`Pause`，`Resume`，`Recheck`，`Remove`（可同时删除数据），
`SetFilePriority`，`SetSequential`，`SetCategory`，`SetTorrentLimits`，`SetQueuePosition`，`AltSpeed` 和 `SetAltSpeed`，
`Peers`，`Trackers`，`Subscribe`（事件），`Save`，以及取得 fib 种子的 `Torrent`。状态给出种子的分类、自己的和实际生效的限制、队列位置、是否已完成，以及角色暂停它的原因（`queued`、
`disk`）。
设置了角色的控制台时，它目前只读地提供 `GET /api/torrents` 和 `GET /api/torrents/<id>`；显示该角色模块的任何控制台以
`/api/m/bittorrent.client/torrents` 和 `/api/m/bittorrent.client/torrents/<id>` 提供它们。

API 还为 `bt_stream` 的临时种子提供了钩子：`AddTemporary`（受调用者的 `max_temporary` 限制）、`SetTemporary`、
`RemoveTemporary`（仅当仍是临时种子时，连同数据一起移除）、`TemporaryBytes` 和 `SetFilePriorities`（一次设置每个文件的
优先级，只保存一次）。

## Admin API

Admin API 在 `/bittorrent/` 下提供客户端和 tracker 的端点，与所有端点一样受其令牌和 `admin.access` 保护；控制台以
`/api/m/admin/bittorrent/...` 转发它们（事件流由控制台自己的路由提供，见下）。`/node` 列出能力 `bittorrent`。角色 API 的每次调用都运行在请求自己的
goroutine 上（Admin API 上是 net/http 的，控制台后面是控制台的池），从不在 fib worker 上。

| 端点 | 作用 |
| --- | --- |
| `GET /bittorrent/torrents` | 按队列顺序的种子：`{"torrents", "total", "offset"}`；`state`（逗号分隔的状态）、`category`（`category=` 表示无分类的）、`q`（名称包含，不分大小写）、`offset`、`limit`（1000，最多 10000） |
| `POST /bittorrent/torrents` | 添加一个，以 `201`、其状态和 `Location` 应答：JSON `{"magnet"` 或 `"url"` 或 `"torrent"`（base64）、`"category"`、`"paused"`、`"sequential"`、`"priorities"`、`"peers"`、`"limits"}`；以 `application/x-bittorrent` 发送的 .torrent 文件，`category`、`paused`、`sequential` 放在查询中；或表单（`multipart/form-data`），文件放在 `torrent` 部分，字段同上 |
| `GET /bittorrent/torrents/<id>` | 种子的状态，含文件 |
| `POST /bittorrent/torrents/<id>` | `{"action": "pause"}`、`"resume"` 或 `"recheck"`；应答状态 |
| `PATCH /bittorrent/torrents/<id>` | `{"paused", "sequential", "category", "limits"（null 清除）, "queue_position", "priorities": [{"index", "priority"}]}`，省略的不变；应答状态 |
| `DELETE /bittorrent/torrents/<id>` | 移除它；`?delete_data=1` 连同文件 |
| `GET /bittorrent/torrents/<id>/files` | `{"files"}`：索引、路径、长度、已完成、优先级 |
| `GET /bittorrent/torrents/<id>/peers` | `{"peers"}`：地址、ID、客户端、方向、uTP、加密、是否做种、分块数、字节数与速率、choke 状态 |
| `GET /bittorrent/torrents/<id>/trackers` | `{"trackers": [{"url", "tier"}], "web_seeds"}`，即其 .torrent 文件或磁力链接所列 |
| `GET /bittorrent/alt_speed` | 备用速率：模式、是否配置、是否生效、当前生效的速率；`PUT` `{"mode": "auto"}`、`"on"` 或 `"off"` |
| `GET /bittorrent/publish` | 发布条目及其种子；`/bittorrent/publish/<name>` 一个 |
| `GET /bittorrent/publish/<name>/torrents/<id>` | 已发布种子的 .torrent 文件（`application/x-bittorrent`，作为附件），页面提供下载 |
| `GET /bittorrent/events` | 事件，以 server-sent events 推送（见下） |
| `GET /bittorrent/tracker/stats` | tracker 的总计 |
| `GET /bittorrent/tracker/torrents` | `{"torrents", "total", "offset", "source", "next"}`：允许列表中种子的 swarm，或公开 tracker 按 info hash 排序的种子（`offset`，`limit` 最多 1000，`after`）；`/bittorrent/tracker/torrents/<info hash>` 一个种子的 |
| `GET /bittorrent/tracker/passkey?user=` | 私有 tracker 策略中的一个用户及其 passkey |

种子的 `<id>` 是它的 ID，即十六进制的 v1 info hash，大小写均可，或其 v2 hash（64 位），取前 20 字节为 ID。`limits` 接受
`upload_rate`、`download_rate`、`max_conns`、`upload_slots`、`seed_ratio` 和 `seed_time`，后者为时长（`"12h"`，`"-1s"`
表示永远做种）或状态给出的纳秒数。`PATCH` 按上述顺序逐项修改并各自保存；第一项失败时应答其错误，之前的修改保留。

添加一个 `bt_stream` 即时添加的种子时，它会原样转为普通种子保留，而不是应答 `409`。发送的 .torrent 文件最多 32 MiB，与按
URL 下载的相同，边到达边读取，超出时以 `413` 拒绝（表单的其他字段每个 64 KiB，最多 16 部分）；URL 由角色在 `url_timeout`
内下载。

| 角色的错误 | 状态码 |
| --- | --- |
| 该角色不在此运行 | 404 |
| 没有该种子 | 404 |
| 错误的请求：链接、文件、分类、优先级、模式 | 400 |
| 已添加；无法恢复、只能移除的种子 | 409 |
| 已达 `max_torrents` | 429 |
| 已有 4 个 .torrent 文件在下载；已有 32 个事件流 | 503，带 `Retry-After` |
| 客户端未运行，例如端口无法占用之后 | 503，带 `Retry-After` |
| .torrent 文件无法下载 | 502 |

## 事件

`GET /bittorrent/events` 以 server-sent events（`text/event-stream`）推送客户端的事件，每个事件带 `id`、作为 `event` 的类型，
以及作为 `data` 的 JSON：`id`、`time`、`type`，视情况还有 `torrent`、`name`、`category`、`state`、`previous`、`error`、
`peer`、`data_deleted`、`dropped`。

| 类型 | 何时 |
| --- | --- |
| `added` | 添加了种子（`state` 为当时的状态） |
| `removed` | 移除了种子（`data_deleted` 表示连同文件） |
| `state` | 种子的状态从 `previous` 变为 `state`（`error` 状态时 `error` 说明原因）；状态即状态信息中的：`metadata`、`checking`、`downloading`、`seeding`、`queued`、`paused`、`stopped`、`error` |
| `completed` | 种子已拥有想要的每个分块 |
| `error` | 其存储出错，但不会因此停止 |
| `peer_banned` | 某个 peer 或某地址的所有 peer 被封禁，`error` 说明原因 |
| `seed_limit` | 种子达到做种比率或时间而暂停 |
| `alt_speed` | 备用速率开始（`state` 为 `on`）或停止（`off`）生效 |
| `events_dropped` | 自管理器上次查看以来，fib 的客户端因交给角色的队列已满丢弃了 `dropped` 个分块和 peer 事件；每秒最多一个 |

状态变化由角色的管理器每秒察觉一次，API 修改之后则立即察觉。没有事件可发时，事件流每 15s 写一行注释。带
`Last-Event-ID`（或 `?after=<id>`）重连的客户端会先收到它错过的事件，取自角色保留的最近 1024 个；其中有些已不再保留，或
fit 此后重启过（ID 每次启动从 1 开始）时，事件流以事件 `reset` 开头，客户端此后应重新读取种子。每个事件流有 256 个事件的
缓冲：取得不够快的客户端不会被悄悄跳过事件，而是收到事件 `lagged` 后被断开，从它拿到的最后一个 ID 重连。最多同时运行
32 个事件流（超出为 `503`）。角色停止时以事件 `end` 关闭事件流；Admin API 关闭时也是。事件流需要独占一个连接：Admin API
在其地址上提供，控制台则以自己的路由 `GET /api/m/admin/bittorrent/events` 在会话之后提供，因为它的代理只回答完整的应答。
控制台在 fib 的连接上写出事件流：worker 订阅并写出流的开头，之后由该流自己的一个 goroutine 不等待地写出每个事件，超过连接
的水位时等待其排空，最多 30s，超时即断开；两者共用 32 个事件流的上限。管理端到成员的转发（节点切换器）同样只回答完整的
应答，因此页面在那里只轮询。

**客户端自己的队列。** fib 在自己的 goroutine 上把客户端的事件交给角色，经过一个 4096 的队列（fib 的
`Config.EventQueue`，取其默认值，不可配置）：超出时 fib 丢弃分块和 peer 事件，从不丢弃其他事件。角色不发布这两类事件，因此
丢弃不会让事件流缺少什么；丢弃会被计数（`fit_bittorrent_events_dropped_total`），并每秒一次以 `events_dropped` 告知，页面
收到后重新读取列表。角色的处理器只取一个短锁就返回，所以只有突发时队列才会满。

事件也写入 fit 的日志，logger 为 `bittorrent`：添加、移除种子，封禁和做种限制为 info，完成和备用速率照旧为 info，存储错误
为 error，状态变化为 debug。

## fit bt

`fit bt` 通过运行中 fit 的 Admin API 操作其客户端，查找和认证方式与 `fit check`、`fit sync` 相同（`-c` 引导配置、
`-timeout`、`-json` 以及 TLS 参数）：

```sh
fit bt ls -state downloading,queued -category films
fit bt add -category films 'magnet:?xt=urn:btih:...' https://example.com/x.torrent ./y.torrent
fit bt info 3f2a91                      # ID 的唯一前缀，或名称
fit bt pause 3f2a91 sintel
fit bt resume 3f2a91
fit bt rm -delete-data 3f2a91
fit bt limit -down 2M -ratio 2 -seed-time 72h 3f2a91
fit bt limit -clear 3f2a91
fit bt alt on                           # auto、on、off；不带参数时显示
```

`add` 接受磁力链接、http 和 https URL 以及 .torrent 文件，还有 `-paused`、`-sequential`，以及设置种子自身速率的 `-up` 和
`-down`；速率为每秒字节数，可加 `K`、`M` 或 `G`（二进制）。有一个被 Admin API 拒绝时，它会试完其余的再以 1 退出，用法错误
时以 2 退出。

## 指标

两个角色的指标与 fit 的指标一起出现在 Admin API 的 `/metrics` 和 OpenTelemetry 导出中。客户端的指标由其管理器每秒采集一次，
抓取时不取角色的锁；fib 对种子字节数和校验失败分块的计数随增长累加到其分类的总计中，因此计数器保留已移除种子的计数，
在 fit 运行期间从不回退。无分类的种子 `category` 为 `""`；对它求和即客户端的总计。

| 指标 | 类型 | 标签 |
| --- | --- | --- |
| `fit_bittorrent_client_running` | gauge | |
| `fit_bittorrent_downloaded_bytes_total`、`fit_bittorrent_uploaded_bytes_total`（有效载荷） | counter | `category` |
| `fit_bittorrent_download_rate_bytes`、`fit_bittorrent_upload_rate_bytes` | gauge | `category` |
| `fit_bittorrent_peers`、`fit_bittorrent_seeders`（连接） | gauge | `category` |
| `fit_bittorrent_torrents` | gauge | `category`、`state` |
| `fit_bittorrent_hash_failures_total` | counter | `category` |
| `fit_bittorrent_peers_banned_total` | counter | |
| `fit_bittorrent_alt_speed_active` | gauge | |
| `fit_bittorrent_event_subscribers`、`fit_bittorrent_event_subscribers_dropped_total` | gauge、counter | |
| `fit_bittorrent_events_dropped_total`（fib 丢弃的客户端分块和 peer 事件） | counter | |
| `fit_bittorrent_published_torrents` | gauge | `entry` |
| `fit_bittorrent_tracker_torrents`、`fit_bittorrent_tracker_peers`、`fit_bittorrent_tracker_max_torrents`、`fit_bittorrent_tracker_max_peers`、`fit_bittorrent_tracker_allowed` | gauge | |
| `fit_bittorrent_tracker_announces_total`、`fit_bittorrent_tracker_scrapes_total` | counter | |
| `fit_bittorrent_tracker_refused_total` | counter | `reason`：`unregistered`、`passkey`、`access` |
| `fit_bittorrent_tracker_store_reachable`、`fit_bittorrent_tracker_store_queued`、`fit_bittorrent_tracker_store_cached`（有共享存储时） | gauge | |
| `fit_bittorrent_tracker_store_written_total`、`_dropped_total`、`_refused_total`、`_failed_total`（有共享存储时） | counter | |

## 页面

工作台有一个 **BitTorrent** 菜单（I204），位于“数据同步”之后，供配置本身的控制台使用：它经控制台调用 Admin API 的
`/bittorrent/` 端点，并编辑共享草稿。

- **种子**：按队列顺序列出每个种子：名称（及错误）、大小、进度条、状态及角色保持它的原因（排队、磁盘空间不足）、下载与上传
  速率、按当前速率的剩余时间、分享率（已上传量除以大小，与 `seed_ratio` 的计算一致）、peer 与做种者数、分类；可按状态、分类
  （或无分类）和名称的一部分筛选，筛选由 Admin API 完成。所选种子可暂停、恢复或删除，删除前询问是否同时删除已下载的文件。
  **添加种子**接受磁力链接、.torrent 文件（以 `application/x-bittorrent` 原样发送，最多 32 MiB）或由 fit 下载的 URL，可指定
  分类、暂停与顺序下载。**最近事件**列出页面打开以来客户端最近的 30 个事件。
- **单个种子**（`#/bittorrent/torrent/<id>`）：**概览**显示进度、速率、总量、peer、分享率与剩余时间、已有分片数与总分片数、
  ID 与 hash、目录、添加时间以及可复制的磁力链接；分类、队列位置与顺序下载即改即生效，自身限制（速率、连接数、上传槽位、
  做种分享率与时长）一起保存或清除，旁边显示当前生效的限制；暂停或恢复、重新校验（需确认：校验文件期间它会停止）与删除。
  **文件**显示各文件的进度与优先级（不下载、低、普通、高），可逐个或对所选文件设置。**Peer**显示各 peer 的地址、客户端、
  连接（方向、uTP、加密、做种、阻塞）与速率。**Tracker**按层级列出其 tracker 及其 Web 种子，即其 .torrent 文件或磁力链接所列。
- **备用速率**位于客户端各页面的顶栏：自动（按计划）、开或关，以及当前是否生效及其速率。
- **Tracker**：tracker 的种子与 peer 数及其上限、announce 与 scrape 数、按原因统计的拒绝、私有或公开及其白名单与 UDP 地址；
  种子的 swarm，每页 100 个（白名单中的，或公开 tracker 按 info hash 排序的）；私有 tracker 还可按用户查询其 passkey，用于该用户的 announce URL，只显示给登录者，
  从不保存。
- **发布**：各发布条目的目录、模式、Web 种子、上次遍历时间、待处理文件与问题，以及其种子的大小、文件数、状态与构建时间，
  提供 **.torrent** 下载（Admin API 的 `/bittorrent/publish/<name>/torrents/<id>`）与磁力链接（打开或复制）。
- **BitTorrent 配置**：整个 `bittorrent` 成员，由 schema 生成：客户端及其分类、限制、`alt_speed` 与 `publish`，以及 tracker；
  保存时 `bittorrent` 下的问题链接到此页。

页面每两秒读取一次列表，以更新事件不携带的速率与进度；同时从控制台的事件流读取客户端的事件，使添加、删除或状态变化的种子
即时显示，并提示完成与存储错误。断开后带 `Last-Event-ID` 重连；收到 `reset` 时重新读取列表；没有事件流时（经管理端打开的
成员、事件流已达上限、没有该角色的 fit）只轮询，列表上标出是哪一种（**实时**或**轮询**）。角色未在此运行时会说明，并链接到
配置页。角色自己的 `console` 只提供其只读模块；这些页面需要 `admin.console`。

### 助手

助手读取客户端与 tracker，并提议对种子的修改，与所有工具一样受其级别约束（[控制台](console.zh-CN.md#bittorrent)）：

| 工具 | 级别 | 作用 |
| --- | --- | --- |
| `bt_torrents` | `read` | 种子的简要信息（状态、保持原因、错误、进度、大小、速率、peer、分享率、剩余时间），可按状态、分类与名称筛选（50 个，最多 200 个），并附备用速率 |
| `bt_torrent` | `read` | 种子的状态、前 100 个文件及其优先级、前 50 个 peer，以及其 tracker 与 Web 种子 |
| `bt_tracker_stats` | `read` | tracker 的总计与拒绝，以及白名单中前 50 个种子的 swarm |
| `bt_add_propose` | `propose` | 按磁力链接或 .torrent URL 添加种子，可指定分类、暂停或顺序下载 |
| `bt_pause_propose` | `propose` | 按 ID 暂停种子（最多 100 个） |
| `bt_resume_propose` | `propose` | 按 ID 恢复种子（最多 100 个） |

passkey 不会到达模型：种子的磁力链接被省略（其 tracker URL 可能带有私有 tracker 的 passkey），tracker 的 URL 只给出协议、
主机与路径的最后一段，Web 种子的 URL 去掉用户与查询，peer 不带 ID，工具也从不读取用户的 passkey。提议工具与媒体工具一样
向对话中唯一待处理的提案加入步骤：按当前运行状态检查（角色及其客户端在运行、分类存在、种子尚未持有、所列种子存在且尚未
暂停或尚未恢复，已处于该状态的会附说明后略去），在提案的配置修改之后按顺序、经 Admin API 以二次确认后应用提案的人的身份
执行。某一步失败时其余步骤仍会完成，结果为 `partial`。

## 边下载边播放

路由处理器 `bt_stream` 在种子下载期间通过 HTTP 提供该角色某个种子的文件，使浏览器的 `<video>` 或播放器能随数据到来而
播放，并任意拖动：

```json
{
  "media_auth": [{"name": "site", "tokens": [{"name": "player", "token": "a-long-random-token", "play": ["bt/*"], "publish": ["bt/*"]}]}],
  "routes": [{"match": {"path": ["/bt/*"]}, "handler": {"module": "bt_stream", "params": {"auth": "site", "magnet": true}}}]
}
```

| URL | 提供什么 |
| --- | --- |
| `/bt/<info hash>/<file path>` | 种子中按其列出的路径（以种子名称开头）指定的文件：`/bt/<hash>/Show/S01E01.mkv` |
| `/bt/<info hash>/<n>` | info 字典文件列表中索引为 `n` 的文件（文件名只由数字组成时，路径优先于索引） |
| `/bt/<info hash>/` | 其中最大的文件，即带一段样片和几张图片的种子里的影片 |

info hash 即角色的种子 ID（v1 info hash，或截断的 v2 hash），40 位十六进制数字；`prefix` 可改变 `/bt/`。请求可以是
`GET` 或 `HEAD`，带单个 `Range`（`206 Partial Content`，超出末尾时为 `416` 并带 `Content-Range: bytes */<size>`；多个范围
或格式错误的范围按 RFC 9110 的许可以整个文件回答），以及针对 `ETag` 的 `If-None-Match` 和 `If-Range`；`ETag` 由种子和
文件索引组成，因为种子的数据从不改变。`Content-Type` 按扩展名确定（视频、音频、字幕、图片，其他为
`application/octet-stream`）；总是发送 `Accept-Ranges: bytes`；`cache_control` 和 `cors` 添加相应字段。

**范围如何读取。** 每个响应通过 fib 的文件 `Reader` 读取：从其位置到其后 `readahead_bytes`（8 MiB）内的分块优先于其他
所有分块下载，近者优先，因此所请求的范围最先下载；一次读取最多等待其分块 `read_timeout`（60s）。响应随第一个数据块
开始，因此第一个分块始终不到的范围得到 `504`；之后中断的响应在达到 `Content-Length` 之前被关闭，客户端能察觉。仍在
等待 info 的种子（磁力链接）最多等待 `info_timeout`（60s），之后为 `504`。

**不阻塞任何 fib worker。** 读写状态文件的角色 API，以及等待分块并读盘的 `Reader`，都运行在流自己的 goroutine 上：最多
`max_streams`（32）个，即处理器的有界池，超过时请求得到带 `Retry-After` 的 `503`。流每次读取 64 KiB，不等待地交给连接；
当客户端排队的数据超过 fib 的水位线时，其 goroutine 结束，待客户端取走一半后由 `OnWritable` 重新启动：慢客户端只占用一个
名额和大约两个数据块，从不占用一个等待它的 goroutine，每次这样的等待受 `limits` 的 `write_stall_timeout` 限制。客户端
离开时其流立即结束，即使正在等待分块：其 reader 被关闭，名额被归还。被换下的配置在排空超时后结束其流。

**授权。** 设置 `auth` 时，每个请求都必须在该 `media_auth` 策略下对 `bt/<info hash>` 有 `play` 权限：bearer token、
Basic、`token` 查询参数、`fit_media_token` cookie，或在设置了 `url_signing` 时为 `<prefix><info hash>/` 签名的 URL。

**即时通过磁力链接添加。** 设置 `magnet`（需要 `auth`）时，若请求的 `magnet` 查询参数是其路径所指种子的磁力链接，而角色
没有该种子，且请求还对 `bt/<info hash>` 有 `publish` 权限，则添加该种子：
`/bt/<hash>/?magnet=magnet%3A%3Fxt%3Durn%3Abtih%3A...`。链接必须属于路径中的种子（否则为 `400`）；对角色已有种子的请求
照常播放。这样的种子是**临时的**：

- 只下载被流式播放的文件：其他文件的优先级设为 0，请求到别的文件时再提高；
- 同时最多持有 `max_temporary`（4）个（超过时为 `503`），合计最多占用 `max_temporary_bytes`（16 GiB）磁盘：若某个种子的
  文件会使总量超过它，请求得到 `507`，刚添加的该种子会被移除；它们也计入角色的 `max_torrents`；
- 最后一个流结束 `temporary_ttl`（10m）之后，除非期间又有流到来，它连同数据一起被移除：播放器在这段时间内请求下一个范围
  时它仍在；
- 角色启动时，会连同数据移除停止或崩溃时遗留的临时种子。

| 参数 | 默认值 |
| --- | --- |
| `prefix` | `/bt/` |
| `max_streams` | 32（最多 4096） |
| `read_timeout` | 60s |
| `readahead_bytes` | 8 MiB（64 KiB 到 256 MiB） |
| `info_timeout` | 60s |
| `max_temporary` | 4（最多 1000） |
| `max_temporary_bytes` | 16 GiB |
| `temporary_ttl` | 10m（最多 7 天） |

## Tracker

`bittorrent.tracker` 角色是一个 BitTorrent tracker，即 fib 的
（[`fib/bittorrent/tracker`](https://github.com/lesismal/fib/blob/main/docs/bittorrent.md)），每个进程一个，配置不设置时关闭。
HTTP 和 HTTPS 的 announce 与 scrape（BEP 3、23、7 和 48）经由处理器为 `bt_tracker` 的任何路由到来，走 fit 的监听器及其
TLS、access 和中间件；UDP 的（BEP 15，带 BEP 41 的 URL 数据）到角色自己的 `listen`，只用 HTTP 的 tracker 可以不设它。
路径最后一段以 `announce` 开头的请求是 announce，以 `scrape` 开头的是 scrape，所以匹配 `/tracker/*` 的路由同样服务
`/tracker/announce` 和 `/tracker/<passkey>/announce`。每个 announce 和 scrape 都在读到它的 worker 上服务：存储只短暂
锁住 64 个分片之一，下面的钩子都是对配置编译结果的查找。

```fit
bittorrent {
	tracker {
		listen :6969
		auth members
		allow_files /etc/fit/torrents.txt
	}
}
routes {
	route { match { path /tracker/* }; handler bt_tracker }
}
```

**Swarm** 保存在内存中，即 fib 的 `MemoryStore`，重启后不保留：客户端会在其间隔内再次 announce。peer 在最后一次 announce
之后 `peer_ttl`（1h）被遗忘，它必须长于 `interval`（30m）；没有 peer 的种子连同其完成次数一起被遗忘。

**多个实例共享的存储**（I207）。设置 `store` 块后，多个 fit 实例的 tracker（在同一个名字之后，或列为同一层）经由 Redis 6.2
或更高版本（或 Valkey）作为一个 tracker 应答：单机服务器或主节点，其 `maxmemory-policy` 为 `noeviction`。fib 的 tracker
在读到请求的 worker 上询问存储并等待应答，所以共享存储在请求上从不等待 Redis：每个实例仍像没有它时一样在 fib 的
`MemoryStore` 中保留自己的 peer，由存储自己的一个 goroutine 完成每次往返，每次不超过 `request_timeout`（2s）。announce
进入队列（每个 peer 与种子一条，同一 peer 后来的 announce 取代之前的，最多 `queue` 条，65536），以 Lua 脚本流水线批量写入，
每个 peer 一次脚本，以 Redis 的时钟为准（各实例的时钟不必一致）：每个种子的做种者和下载者分别存于按过期时间排序的有序集合，
地址存于哈希，另有完成次数、种子索引和 peer 总数，每个脚本都使它们保持准确。对一个实例上 announce 或 scrape 过的每个种子，
存储每 `refresh`（5s）读取一次共享 swarm 的随机样本：`cache_peers`（100）个做种者和同样多的下载者及其计数，最多
`cache_torrents`（4096）个种子。announce 的应答先取样本中的 peer，再补上样本没有的本实例 peer，做种者只得到下载者，
从不包括自己；announce 和 scrape 的计数取本实例与共享计数中较大者。因此一个实例在一个 refresh 内得知另一个实例的 peer，
而**一个实例尚未服务过的种子，其第一次 announce 只得到本实例的 peer**（下一次才得到整个集群的）：fib 的 `Store` 同步应答，
异步的 Store 是对 fib 的请求。

```fit
bittorrent {
	tracker {
		listen :6969
		store {
			redis { address redis.internal:6379; password_file /etc/fit/redis.pass }
		}
	}
}
```

上限对共享存储同样适用，即所有实例的 peer 合计：索引中最多 `max_torrents` 个种子，一个 swarm 最多 `max_peers_per_torrent`
个，总共最多 `max_peers` 个；超出上限的 announce 仍由实例内存应答，但不共享（计为被拒）。键从不自行过期：种子中过期的 peer
由该种子的 announce 移除，每个实例每分钟移除至多 4096 个 peer 已全部过期的种子，所以每个种子四个键，另加两个。**Redis
无法访问时**，tracker 继续依靠内存：announce 以本实例的 peer 和不超过三个 refresh 的样本应答，进入队列，Redis 恢复后写入
（超出队列或早于 `peer_ttl` 的丢弃并计数）；退避 100ms 起、加倍到 `refresh`（最多 10s）后重试。请求从不因此被拒。它增加的
内存是队列（每条约 100 字节）和样本（每个 peer 约 60 字节）。

| 上限 | 默认 | 最多 |
| --- | --- | --- |
| `max_torrents`，同时跟踪的种子；再多一个以 "tracker is full" 拒绝 | 100000 | 1048576 |
| `max_peers_per_torrent`；超出的 peer 能获得 peer，但不会被分发 | 10000 | 65536 |
| `max_peers`，所有种子合计，每个约 150 字节 | 500000 | 4194304 |
| `max_numwant`，一次应答中的 peer（客户端未指定时 50；UDP 应答限制在 1400 字节内） | 200 | 1000 |
| `max_scrape`，一次 HTTP scrape 的种子（UDP 的最多 74 个） | 64 | 1000 |
| 允许列表，内联和文件合计 | | 1048576 个 info hash，每个文件 64 MiB |

**私有。** 设置 `auth`（一个 `media_auth` 策略的名字）后，每个 announce 和 scrape 都需要 passkey，放在路径中
（`/<passkey>/announce`）或查询中（`?passkey=`），且 passkey 的授权须能播放流 `bittorrent/<十六进制 info hash>`：
`play: ["bittorrent/*"]` 表示所有种子，或写一个哈希只表示一个种子。passkey 是策略的令牌之一（静态的，或 fit 签发的：
`fit media-token`、媒体页面），或用户的 passkey：由策略的签名密钥和用户名派生的 32 位十六进制数，角色的控制台在
`/api/passkey?user=<name>` 给出。轮换策略的 secret 或其 keyring 的默认密钥会改变每个用户的 passkey；没有 secret 时，
passkey 在 fit 重启后改变。UDP 上 passkey 在客户端依 tracker URL 发送的 URL 数据中：`udp://host:6969/<passkey>/announce`。
拒绝以失败原因应答："a passkey is required"、"unknown passkey"、"the passkey may not use this torrent"。访问日志中的
passkey 被替换为 `REDACTED`。`public` 含 `play` 的策略让所有人进入。策略未加载时 tracker 拒绝所有请求。

**允许列表。** 设置 `allow`（十六进制 info hash：v1 为 40 位，v2 为 64 位，按前 20 字节跟踪）或 `allow_files`（每行一个，
`#` 注释）后，只跟踪这些种子：其他的以 "unregistered torrent" 拒绝，对它们的 scrape 得不到任何内容。文件像证书和 access
列表文件一样被监视：变化时重新应用配置并重新读取。有一行不是 info hash 或无法读取的文件会使重载失败，之前的列表继续生效。

**Access。** 角色的 `access`，然后是全局的，在 fib 为被拒地址的 UDP 数据报建立连接之前将其丢弃，并以失败原因 "refused"
应答来自被拒地址的 HTTP announce 和 scrape（路由的监听器也有全局 access）。

**重载。** 允许列表、`auth`、`access` 和控制台原地改变，`max_torrents`、`max_peers_per_torrent`、`max_peers` 和
`peer_ttl` 也一样（fib 的 `MemoryStore.SetConfig`），swarm 保留：调低的上限不丢弃任何东西，在存储回到上限以下之前拒绝新的
种子或 peer；改短的 `peer_ttl` 使已保留超过新时长的 peer 过期。`interval`、`min_interval`、`max_numwant` 和 `max_scrape`
在同一存储上新建 fib tracker，swarm 保留，UDP 连接 ID 仍然有效（角色保持同一个密钥）。`listen` 的更改会移动 UDP 端口；无法
绑定时重载失败，tracker 保持原样。有共享存储时，上限和 `peer_ttl` 从此作用于它之后的写入（已共享的 peer 保留原过期时间）；
更改、添加或移除 `store` 块会新建存储，本实例的 swarm 在内存中重新开始，共享的仍留在 Redis 中；无法建立（TLS 文件读不出）
时重载失败。

**统计。** 角色的 Go API 有 `Stats`（保存的种子和 peer、已服务的 announce 和 scrape、按原因的拒绝次数、上限）、
`Torrent(hash)`（做种者、下载者、完成次数）以及 `Torrents`/`ListTorrents`：有允许列表时，按列表顺序给出其中种子的
swarm，无论是否正在跟踪（`source` 为 `allow_list`）；公开 tracker 给出存储中有 peer 的种子，按 info hash 排序（`source` 为
`store`），经 fib 的 `MemoryStore.Range` 逐个分片读取。每页最多 1000 个种子，按 `offset` 翻页，公开 tracker 还可按
`after=<info hash>` 翻页（应答的 `next` 是下一页的起点，最后一页为 `""`）；一次调用在内存中只保留 `offset`+`limit` 个种子，
耗时与跟踪的种子数成正比，因此要翻到大存储的深处应使用 `after`；每个种子的数字取自同一时刻。有共享存储时，`Stats`
的种子和 peer 是共享存储中所有实例的，每个 refresh 读取一次（无法读取时为本实例的），其 `store` 说明存储的状况：`kind`、
`reachable`、`error`、共享的与 `local_` 开头的本实例种子和 peer、`queued`、`cached`，以及 announce 的 `written`、
`dropped`、`refused` 和 `failed`；列表是所有实例的种子，从 Redis 读取（`ZSCAN` 加计数，每次往返 512 个），无法读取时为
本实例的；单个种子的数字取本实例与其样本中较大者。其控制台只读地提供
`GET /api/stats`、`/api/torrents`（`offset`、`limit`、`after`）和 `/api/torrents/<hash>`，作为模块时为
`/api/m/bittorrent.tracker/...`。Admin API 以 `/bittorrent/tracker/stats`、`/bittorrent/tracker/torrents[/<hash>]` 和 `/bittorrent/tracker/passkey?user=`
提供它们，另有[指标](#指标)。

## 互通

fit 的角色由 `test/btinterop` 与另外两个客户端做互通检查。它是独立的模块（因此 anacrolix 永远不会成为 fit 的依赖），在 CI 的
`bittorrent-interop` 任务中运行，本地用 `test/btinterop/run.sh` 运行（docker；容器中有 libtorrent 的 Python 绑定和 cgo）。
它的测试从检出的代码构建 fit，像用户那样运行它，经 Admin API 操控：[anacrolix/torrent](https://github.com/anacrolix/torrent)
1.61 在测试进程中（uTP 经 go-libutp，因此需要 cgo），[libtorrent-rasterbar](https://www.libtorrent.org/) 2.0.11 经
`libtorrent_peer.py`。2026-11-09 所有用例均通过（fit 12f4795e 加 I205 的测试，fib c82b427）。

| 用例 | anacrolix | libtorrent |
| --- | --- | --- |
| fit 从对方的做种下载：v1 和混合种子，.torrent 文件和磁力链接，TCP 和 uTP，MSE 关闭、优先和要求 RC4（8 个用例） | 通过 | 通过 |
| v2 种子的同样用例（`btmh` 磁力链接，按 BEP 52 哈希请求取得 piece layers；3 个用例） | （不支持纯 v2 种子） | 通过 |
| 对方从 fit 的做种下载，同样的用例 | 通过 | 通过 |
| 只能经 fit 的 tracker 找到的私有种子：做种方经 HTTP 用令牌的 passkey，下载方经 UDP（BEP 41 URL 数据）用 Admin API 给出的用户 passkey；未知 passkey 被拒绝 | 通过（v1） | 通过（v1、混合、v2；fit 自己的客户端作为 UDP 下载方，libtorrent 经 HTTP） |
| `bt_stream` 在从 anacrolix 下载 24 MiB 文件的同时播放它：中间的一段 Range 在种子下载到 8% 时得到应答，然后是整个文件；按磁力链接即时添加（临时） | 通过 | |
| 只用发布条目的 web seed（fit 的 `static` 路由），没有 BitTorrent 对端：`per_file` 和 `whole_dir` 种子 | `whole_dir` 通过 | 都通过 |

每个用例见到的对端经 Admin API 检查：uTP 或 TCP 与用例相符，要求加密时为加密，关闭时为明文。途中发现的其他客户端的怪癖
（不是 fit 的），测试绕开了它们：

- anacrolix 把 v2 或混合种子的单个文件当作目录（其文件树中包含文件名），因此在名称下面寻找数据，并拒绝不以 `/` 结尾的
  单文件 web seed URL：anacrolix 无法从 web seed 获取 fit 以 `per_file` 发布的种子。anacrolix 的 web seed 也不会请求较短的
  最后一个分片。
- anacrolix 1.61 在校验一个它以两个哈希 announce 的混合种子时，会在其 announcer 中 panic（`updateTorrentInput` 中的
  `panicif.False`）；tracker 用例与它一起时使用 v1 种子。

## 性能

`bench/bt.sh` 在基准主机上运行 `test/btinterop/btbench`（规则 16-18；从不在本地运行）：一个 1 GiB 的 v1 种子（1 MiB 分片）
放在 `/dev/shm` 中，被测客户端绑定在 4 个 CPU（0-3）上，另一端是绑定在 CPU 8-15 上的四个 fib 客户端，经回环 TCP，明文和 MSE
（RC4）各一组。被测客户端是 fit、anacrolix 1.61，或单独在一个进程中的 fib 客户端（fit 的客户端就是 fib 的，所以差异来自
fit）。CPU 时间和峰值常驻内存取自 `/proc`，属于被测进程。2026-11-09 的结果在
[bench/results/bt-2026-11-09](../bench/results/bt-2026-11-09/summary.md)，为 3 轮交替运行的中位数：

| 场景 | 客户端 | MiB/s | CPU %（共 400） | 每 GiB CPU 秒 | 峰值 RSS MiB |
| --- | --- | --- | --- | --- | --- |
| 从 4 个做种方下载，明文 | fit | 705 | 158 | 2.28 | 179 |
| | 单独的 fib | 478 | 106 | 2.28 | 163 |
| | anacrolix | 1039 | 225 | 2.23 | 1050 |
| 从 4 个做种方下载，RC4 | fit | 553 | 199 | 3.67 | 176 |
| | 单独的 fib | 363 | 137 | 3.85 | 152 |
| | anacrolix | 759 | 283 | 3.81 | 1052 |
| 向 4 个下载方做种，明文（四者合计 MiB/s） | fit | 1688 | 98 | 0.60 | 46 |
| | 单独的 fib | 1670 | 93 | 0.57 | 34 |
| | anacrolix | 1110 | 133 | 1.23 | 1058 |
| 向 4 个下载方做种，RC4 | fit | 933 | 215 | 2.35 | 56 |
| | 单独的 fib | 937 | 217 | 2.36 | 43 |
| | anacrolix | 744 | 199 | 2.74 | 1060 |

做种时 fit 就是 fib：明文速率是 anacrolix 的 1.5 倍，每字节 CPU 只有其一半，RC4 时为 1.25 倍，内存只有其二十分之一
（anacrolix 映射整个文件）。下载时 fit 每字节 CPU 与 anacrolix 相同，但速率只达到其 0.68（明文）和 0.73（RC4），四个 CPU
用了不到两个：下载受 fib 的种子锁限制，在这把锁下每收到一个块都要扫描分片来挑选下一个请求（`pickPiece`，在单独 fib 的
profile 中占 CPU 的五分之一），而且每个 16 KiB 块各是一次 `pwrite`。单独的 fib 更慢（fit 速率的 0.68），可能是它自己引擎的
默认值不如 fit 的共享引擎（未进一步 profile）。两者都是 fib 请求（见开发计划中 I205 的遗留项）；各轮之间的差异在 2% 以内，
只有 anacrolix 的明文下载为 7%。

## 限制与安全

- **端口。** 除非 `access` 另有规定，客户端的 `listen` 对互联网上的每个对端开放；它在同一个 UDP 端口上接受 TCP 和 uTP、
  DHT 以及 UDP tracker 的应答。`encryption require` 拒绝明文对端（有些 ISP 会限速明文 BitTorrent），代价是每个字节都要
  RC4（见[性能](#性能)）。`dht: false` 且不设 tracker 可使客户端完全不进入公共 swarm：它只与给定的对端通信。
- **谁可以添加种子。** Admin API（在其令牌和 `admin.access` 之后）、`fit bt`、`watch_dir`（谁能写入它）以及带 `magnet` 的
  `bt_stream`（谁对 `bt/<info hash>` 有 `publish`）。添加的种子从互联网下载到服务器的磁盘，受 `max_torrents`、
  `min_free_bytes` 约束，`bt_stream` 添加的还受 `max_temporary` 和 `max_temporary_bytes` 约束。.torrent URL 从它指向的任何
  地方获取，包括回环和私有地址：Admin API 属于运维者（I198 的决定 3）。
- **路径。** 种子不会写到其目录之外：fib 拒绝 `..` 和绝对路径段，存储把名称处理为安全的名称；分类的相对 `dir` 留在
  `download_dir` 之下。发布的种子的存储拒绝写入，因此发布的文件绝不会被对端覆盖。
- **私有 tracker。** passkey 位于每个携带它的 .torrent 文件的 announce URL 中：持有这样文件的人都能以其所有者的身份 announce。
  给每个用户自己的 passkey（用户的 passkey，由策略的 secret 派生，轮换 secret 即吊销；或每个用户一个令牌），而不是共享的
  令牌；`publish` 条目的 `announce` 若带 passkey，会把该 passkey 交给发布种子的每个下载者，因此应发布到公开的 tracker（不设
  `auth`，用 `allow` 或 `allow_files` 限制它跟踪什么）或不用 tracker（web seed 和 DHT）。访问日志中从不出现 passkey。
- **流。** 不设 `auth` 的 `bt_stream` 把客户端持有的每个种子提供给能到达该路由的任何人；设了 `auth` 时，令牌的 `play` 授权
  指定哪些种子（`bt/*` 表示全部）。
- **事件队列。** fib 的客户端为角色最多排队 4096 个事件（超出时丢弃分块和 peer 事件并计数）；每个事件流 256 个，角色保留
  1024 个。
