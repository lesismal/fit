# BitTorrent

[English](bittorrent.md) | [简体中文](bittorrent.zh-CN.md)

fit 在网关旁运行一个 BitTorrent 客户端：`bittorrent.client` 角色在 fit 自己的 fib 引擎上、以 TCP 和 UDP 共用的一个端口
下载和做种，并在重启后保留种子（[决定 037](decisions/037-bittorrent.zh-CN.md)）。它就是 fib 的客户端
（[fib 的 BitTorrent 文档](https://github.com/lesismal/fib/blob/main/docs/bittorrent.md)）：v1、v2 和混合种子，磁力链接，
TCP 和 uTP，MSE 加密，DHT，对端交换，本地服务发现，HTTP 和 UDP tracker，以及 web seed。本页讲这个角色：配置、状态保存
在哪里、重载改变什么、它的上限，以及边下载边播放种子文件的 `bt_stream` 处理器。按 URL 和 `watch_dir` 添加种子、分类与
排队、时间表、tracker、发布、Admin API 和页面，随[开发计划](dev-plan.zh-CN.md)中之后的条目而来。

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
| `torrents.json` | 列表：每个种子的 ID（v1 info hash，或截断的 v2 hash）、名称、来源磁力链接、下载目录、文件优先级、是否暂停或顺序下载、是否由 `bt_stream` 即时添加（`temporary`），以及添加时间 |
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

API 还为 `bt_stream` 的临时种子提供了钩子：`AddTemporary`（受调用者的 `max_temporary` 限制）、`SetTemporary`、
`RemoveTemporary`（仅当仍是临时种子时，连同数据一起移除）、`TemporaryBytes` 和 `SetFilePriorities`（一次设置每个文件的
优先级，只保存一次）。

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
