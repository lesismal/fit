# 引擎

[English](06-engine.md) | [简体中文](06-engine.zh-CN.md)

## 功能

引擎菜单是按端口查看监听的视图：一级条目**引擎**（小标签**端口**），在导航栏中紧跟**监听**之后，其子菜单
为各监听端口，每个端口列出它所提供的功能及编辑各项功能的页面。这个条目没有自己的页面——它展开为各端口，
端口下的每个条目都是指向其他模块页面的链接。

数据来自各编辑页面共用的配置草稿（上次读取时的样子），加上 `GET /api/status` 报告的内核实际绑定的地址，
以及 HTTPS by default 生成的 `https` 与 `http` 监听。每次重新读取草稿（保存、放弃修改、导入或回滚之后）
菜单都会重建，且不丢失当前页面。每个端口，以及每个列出路由的条目，都有**问 AI**按钮，以技能 `diagnose`
诊断它，其路由名称作为技能输入 `route`。见[助手](14-assistant.zh-CN.md)。

这个菜单不是[全局配置](07-global-settings.zh-CN.md#引擎engine)里的**引擎**页；后者编辑 `engine` 成员，
即网络引擎 fib 的事件循环与 worker。

## 页面与操作

### 引擎

**端口**：按配置书写顺序列出已配置的监听；未写监听且 HTTPS by default 开启时，是它生成的 `https` 与
`http` 端口（关掉跳转时只有 `https`），端口号取自 `auto_https`（默认 443 与 80）；一个端口也没有——或配置
无法读取——时，只有一个指向监听表单的**未配置监听**链接。

端口行显示名称与短地址（`127.0.0.1:8080` 记为 `:8080`）。悬停提示中有完整地址、小标签、内核实际绑定的
地址（与写入的不同时），以及 `/api/status` 读取失败时的提示：显示的是配置中写入的地址。

端口名称下的一行小标签是它的协议与没有独立页面的功能，顺序为：协议（`HTTP/1.1`、`h2c`、`HTTP/2`、
`TCP`、`UDP`、`SOCKS`；没写协议的监听显示默认值 `HTTP/1.1`，有 `tls` 时加 `HTTP/2`）、生成端口上的
**自动 HTTPS**、**PROXY**（`proxy_protocol`）、**HTTP/3**（`h3`）、**按需证书**（`tls.on_demand`）、
**客户端证书**（`tls.client_auth`）、**MPTCP**、**点播**（该端口有 `vod` 路由）。

**每个端口的条目**按固定顺序，只显示端口真正具有的功能。计入的路由是 `listeners` 写了该端口、或不写
`listeners` 的路由；不提供 HTTP（`http/1.1`、`h2c`、`h2` 或 `h3`）的端口没有路由。

1. 路由的处理器，按固定顺序：**静态文件**、**直接应答**、**FastCGI**、**媒体令牌**、**媒体页面**，然后
   是按模块 ID 列出的其他处理器——例如 [BitTorrent](27-bittorrent.zh-CN.md) 的 `bt_publish`、
   `bt_stream`、`bt_tracker`，或 [数据同步](26-sync.zh-CN.md) 的 `sync_ingest`；
2. **反向代理路由**（处理器 `proxy`）与**正向代理路由**（处理器 `forward_proxy`）；
3. **缓存区**与**缓存的路由**（该端口有路由使用 `cache` 中间件时）；
4. **其他中间件**：使用其他中间件的路由合计一项，悬停提示列出用到的中间件模块；
5. **流媒体配置**（端口有流媒体或 `vod` 路由时），然后是各流媒体处理器的路由（**HLS 路由**、
   **HTTP-FLV 路由**、**WHIP 推流路由**、**WHEP 播放路由**），然后是**点播路由**与**频道**——
   `streaming.channels` 的数量，悬停提示列出频道名，端口有 HLS、HTTP-FLV 或 WHEP 播放路由时显示；
6. **WebRTC 配置**与各处理器的路由（**WHIP 路由**、**WHEP 路由**、**WebRTC 代理路由**）；
7. **四层服务**（列出该端口的服务）、**SOCKS 正向代理**（其规则在[监听](05-listeners.zh-CN.md)页编辑）、
   **TLS 证书**，以及 `tls.acme` 写了名字时的 **ACME 证书**；端口提供 HTTP、`tls` 为空（如 `tls {}`）
   或是生成的 `https` 端口，且 `auto_https` 未关闭时显示**自动 HTTPS**；生成的 `http` 端口上显示**重定向到 HTTPS**；
8. 最后是**端口配置**，即监听表单。

各条目打开的页面：

| 条目 | 打开 |
| --- | --- |
| 某处理器的路由、**反向代理路由**、流媒体或 WebRTC 处理器的路由 | [反向代理](15-reverse-proxy.zh-CN.md)的路由页，按该处理器筛选（`#/gw.reverse/routes?handler=<module>`），带数量 |
| **正向代理路由** | [正向代理](16-forward-proxy.zh-CN.md)页，带数量 |
| **缓存的路由**、**其他中间件** | 路由页，不筛选，带数量 |
| **缓存区** | [缓存](19-cache.zh-CN.md)的缓存区页 |
| **流媒体配置**、**WebRTC 配置** | [流媒体](20-streaming.zh-CN.md)与 [WebRTC](23-webrtc.zh-CN.md) 的配置页 |
| **点播路由**、**频道** | [媒体库](22-media-library.zh-CN.md)的点播路由页与频道页，带数量 |
| **四层服务** | [四层代理](17-l4-proxy.zh-CN.md)的服务页，带数量 |
| **TLS 证书**、**ACME 证书**、**自动 HTTPS**、**重定向到 HTTPS** | [TLS 证书](18-tls.zh-CN.md)的证书、ACME 与自动 HTTPS 页 |
| **SOCKS 正向代理**、**端口配置** | [监听](05-listeners.zh-CN.md)页 |

端口下的**自动 HTTPS**是指向 `auto_https` 设置的链接，只在自动 HTTPS 起作用的地方列出：`tls` 为空的监听，
或一个监听都没写时它生成的端口。写了却没有 `tls` 的监听仍是明文 HTTP，两项都不列。

## 相关文档

- [控制台与 store](../console.zh-CN.md#页面) — 引擎菜单的说明
- [监听](05-listeners.zh-CN.md) — 编辑监听本身
- [配置参考：Listeners](../reference/config.md#listeners) — 监听的全部字段，以及
  [HTTPS by default](../reference/config.md#https-by-default)
- [反向代理](15-reverse-proxy.zh-CN.md)、[四层代理](17-l4-proxy.zh-CN.md)、[流媒体](20-streaming.zh-CN.md)
  与 [WebRTC](23-webrtc.zh-CN.md) — 端口条目指向的页面
- [数据同步](26-sync.zh-CN.md) 与 [BitTorrent](27-bittorrent.zh-CN.md) — 其处理器按模块 ID 列出的模块
