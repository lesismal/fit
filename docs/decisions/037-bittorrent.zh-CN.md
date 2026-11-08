# 037：BitTorrent：下载与做种、tracker、发布

[English](037-bittorrent.md) | [简体中文](037-bittorrent.zh-CN.md)

状态：已采纳（2026-11-08，用户审定了下面"审定的问题"）。规划见
[开发计划的 BitTorrent 路线](../dev-plan.zh-CN.md#bittorrent下载与做种tracker发布)（I196–I208）。

## 背景

fib 已有完整的 BitTorrent 协议栈（`fib/bittorrent/...`）：bencode、metainfo（v1、v2、hybrid 与磁力链）、HTTP 与 UDP tracker
的客户端和服务端、peer 协议与 MSE、uTP、DHT，以及下载和做种的客户端，与 libtorrent、anacrolix 互通。fit 的 `go.mod`
已在含这些包的 fib `f957983` 上。用户希望 fit 支持 BT 相关功能：在服务器上下载和做种、边下边播、自建 tracker、
把目录发布成种子。

读 fib 代码时发现，fib 的客户端在 fib worker 上做磁盘读写和整块哈希：`peer.Server.OnData` 跑在 worker 上，同步调用
客户端写块、读上传数据、校验 piece 的代码，违反[规则 1](../rules.zh-CN.md)。它也没有快速续传、运行时可改的限制、
按地址拒绝 peer，并且总是自建引擎。按规则 15 和 19，这些先在 fib 补齐（A75–A79），fit 不绕过。

## 决定

- **先补 fib**（I196）：A75 存储读写与哈希放到客户端有界 pool，pool 满时 `HoldReads` 暂停读（背压）；A76 快速续传数据；
  A77 运行时可改的全局限制和单 torrent 限制；A78 `Config.Filter` 按地址拒绝 peer（入站、出站、uTP、DHT）；A79 客户端
  跑在调用者给的引擎上，关闭时只移除自己的绑定。
- **`bittorrent.client` 角色**：默认关闭，一个进程一个客户端（TCP 和 UDP 同端口），跑在 **fit 的共享 fib 引擎**上（A79）。
  任务从磁力链、.torrent 文件、.torrent URL、`watch_dir` 添加；分类有自己的目录、限制，并可绑定 `media_library`，
  下载完成后触发重扫；同时活跃的下载数、做种数有上限，其余排队；`alt_speed` 按时段换速率；磁盘空间不足时暂停。
- **任务状态存在角色的 `data_dir` 下的文件里**，不放 fit 的加密 store：任务列表、分类、优先级、.torrent 和续传数据
  （A76），写临时文件再 rename；重启后恢复，不重新哈希。reload 时限制、分类、调度当场生效（A77），只有 `listen` 和
  DHT/uTP 的开关会重建客户端，任务和续传数据保留。
- **默认值**：DHT、PEX、uTP 默认开，LSD 默认关，加密默认 `prefer`；peer ID 前缀用 fib 的 `-FB0001-`（尚未在社区登记）。
- **`bt_stream` 处理器**：HTTP Range 读取任务中的文件，未下完的区段优先下载，等数据到达再回应；`Reader` 的阻塞读在
  有界 pool 上执行；鉴权用 `media_auth`。**允许按磁力链临时添加任务**，临时任务有数量上限，在最后一个流结束后过期删除。
- **`bittorrent.tracker` 角色**：HTTP 的 announce 和 scrape 是路由处理器 `bt_tracker`，UDP 是角色自己的 `listen`；
  公开或私有（passkey 来自 `media_auth` 的用户和 token），info hash 白名单（内联或文件，文件变化时重读）；
  统计供 Admin API 与页面读取。Store 是 fib 有界的 `MemoryStore`。
- **发布**：用 fib 的 `metainfo.Builder` 把目录生成 hybrid 种子并做种，`.torrent` 和磁力链经路由给出，web seed 指向
  fit 自己的静态文件服务；跟随磁盘变化重新生成，哈希在有界 pool 上。
- **黑名单格式让所有模块都能读**：`access` 的 `deny_files` 和 `allow_files` 也读 eMule 的 `.p2p`（`名称:起始IP-结束IP`）
  和 `.dat`（`起始 - 结束 , 级别 , 名称`），按内容识别格式；BT 角色经 A78 用同一个 access。
- **不做搜索插件和索引站。**
- **第二阶段**：集群汇总与集群内经 BT 分发大文件（I206），tracker 的集群共享 Store（I207），RSS 自动下载（I208）。

## 审定的问题（2026-11-08）

| 问题 | 用户的决定 |
| --- | --- |
| 第一版范围 | 集群放第二阶段 |
| 引擎 | 共享 fit 的 fib 引擎（因此加了 A79） |
| 默认值 | DHT、PEX、uTP 开，LSD 关，加密 prefer，前缀 `-FB0001-` |
| `bt_stream` 按磁力链临时添加 | 允许 |
| RSS 自动下载、搜索插件 | RSS 进计划，第二阶段；搜索不做 |
| tracker 集群共享 Store | 第二阶段 |
| 黑名单 `.p2p` / `.dat` | 扩展 access，所有模块都能读 |
| 任务状态 | `data_dir` 下的文件 |

## 影响

- fib 先改，fit 按 commit hash 跟进（规则 19）。
- BT 的 TCP/UDP 端口进入 fit 共享引擎，受 `limits` 与 `access` 约束；`bt_stream` 让 fit 的 HTTP 路由能触发外网下载，
  所以临时添加默认受 `media_auth` 保护，并有数量和磁盘上限。
- 互通测试沿用 fib 的方式（libtorrent、anacrolix，在容器里）；吞吐与 CPU 基准只在 .51 上跑。
