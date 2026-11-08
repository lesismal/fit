# BitTorrent

[English](bittorrent.md) | [简体中文](bittorrent.zh-CN.md)

fit 在网关旁运行一个 BitTorrent 客户端：`bittorrent.client` 角色在 fit 自己的 fib 引擎上、以 TCP 和 UDP 共用的一个端口
下载和做种，并在重启后保留种子（[决定 037](decisions/037-bittorrent.zh-CN.md)）。它就是 fib 的客户端
（[fib 的 BitTorrent 文档](https://github.com/lesismal/fib/blob/main/docs/bittorrent.md)）：v1、v2 和混合种子，磁力链接，
TCP 和 uTP，MSE 加密，DHT，对端交换，本地服务发现，HTTP 和 UDP tracker，以及 web seed。本页讲这个角色：配置、状态保存
在哪里、重载改变什么，以及它的上限。按 URL 和 `watch_dir` 添加种子、分类与排队、时间表、边下边播、tracker、发布、Admin API
和页面，随[开发计划](dev-plan.zh-CN.md)中之后的条目而来。

默认关闭：不写 `bittorrent.client` 时，fit 不打开 BitTorrent 端口，也不保存状态。

```fit
bittorrent {
	client {
		listen :6881
		data_dir /var/lib/fit/bittorrent
		download_dir /srv/downloads
		limits {
			upload_rate 1048576
			seed_ratio 2
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
| `torrents.json` | 列表：每个种子的 ID（v1 info hash，或截断的 v2 hash）、名称、来源磁力链接、下载目录、文件优先级、是否暂停或顺序下载，以及添加时间 |
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

## 重载

| 更改 | 结果 |
| --- | --- |
| `limits` | 就地生效（fib 的 `Client.SetLimits`）：新速率在四分之一秒内生效，对端保持连接，超出调低后连接上限的除外 |
| `access` | 就地生效：客户端的过滤从下一个对端起按新策略判断，现在被拒绝的已连接对端被关闭（`Client.Refilter`） |
| `save_interval`、`console`、`download_dir` | 就地生效 |
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
| `disk_queue_bytes`，等待写盘的已接收块 | 64 MiB |
| .torrent 文件 | 128 MiB（fib 的 metainfo 上限） |
| 磁力链接 | 64 KiB |
| `torrents.json`、续传文件 | 各 64 MiB |

## 角色的 API

角色有一组 Go API，供之后的条目（Admin API、`fit bt`、页面）使用：`Add` 磁力链接或 .torrent 字节，`List`，`Get`（含文件），
`Pause`，`Resume`，`Remove`（可同时删除数据），`SetFilePriority`，`SetSequential`，`Save`，以及取得 fib 种子的 `Torrent`。
设置了角色的控制台时，它目前只读地提供 `GET /api/torrents` 和 `GET /api/torrents/<id>`；显示该角色模块的任何控制台以
`/api/m/bittorrent.client/torrents` 和 `/api/m/bittorrent.client/torrents/<id>` 提供它们。

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

**重载。** 允许列表、`auth`、`access` 和控制台原地改变。`interval`、`min_interval`、`max_numwant` 和 `max_scrape` 在同一
存储上新建 fib tracker，swarm 保留，UDP 连接 ID 仍然有效（角色保持同一个密钥）。`max_torrents`、`max_peers_per_torrent`、
`max_peers` 和 `peer_ttl` 会新建存储，因为 fib 的 `MemoryStore` 只在创建时接受它们：swarm 从空开始，随客户端再次 announce
而填充。`listen` 的更改会移动 UDP 端口；无法绑定时重载失败，tracker 保持原样。

**统计。** 角色的 Go API 有 `Stats`（保存的种子和 peer、已服务的 announce 和 scrape、按原因的拒绝次数、上限）、
`Torrent(hash)`（做种者、下载者、完成次数）以及 `Torrents`（允许列表中种子的 swarm）；其控制台只读地提供
`GET /api/stats`、`/api/torrents`（`offset`、`limit`，最多 1000）和 `/api/torrents/<hash>`，作为模块时为
`/api/m/bittorrent.tracker/...`。公开的 tracker 无法列出它跟踪的种子：fib 的 `MemoryStore` 只对指名的种子作答。
Admin API 的 `/bittorrent/tracker/stats` 随 I203 而来。
