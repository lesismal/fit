# 功能导航栏

[English](03-navigation.md) | [简体中文](03-navigation.zh-CN.md)

## 功能

登录后的整页就是 **workbench（工作台）**：左侧是功能导航栏，右侧是所选页面。导航栏的第一级是
控制台所提供各模块的平铺列表，第二级是模块的页面，第三级是引擎菜单中端口的功能。只有一个页面
（或一个外链）的模块直接是链接。

导航栏只显示控制台实际提供的模块（`GET /api/workbench` 列出服务端模块）：`admin.console` 提供配置
本身的模块 `admin`，下表中除集群与助手外的每个模块都调用它；有 `admin.assistant` 时提供助手的 `ai`；
有 `admin.cluster` 时提供管理端的 `cluster`；以及同进程运行的每个角色的模块。角色自己的控制台只提供
该角色的模块。每个页面有自己的 URL：`#/<模块>/<页面>`，刷新或分享链接都能直达；URL 未指定页面时转到
该模块的第一页，未指定模块时转到概览。

## 布局

- **左侧导航栏**：品牌区（fit 与“工作台”）及把导航栏收起为图标的按钮、**筛选菜单**框、模块树、底部
  （当前用户与**退出登录**）。
- **顶栏**：菜单按钮（窄屏）、标题与面包屑（模块，多页模块再加页面）、页面工具（如概览的**自动刷新**与
  **刷新**）、控制台提供 `ai` 时助手的对话按钮，以及主题与语言选择。
- **右侧内容区**：所选页面。

筛选递归匹配标签：任一后代命中时其模块或端口仍显示，模块本身命中时显示其全部页面。模块默认展开，可
折叠；端口默认折叠，可展开。展开状态与收起状态保存在浏览器（`localStorage`）中，主题（跟随系统、浅色、
深色、深海、日落、石墨）与语言（中文或 English，初始取浏览器的语言）也一样；切换语言会重新加载页面。

## 模块与页面清单

按菜单顺序（与 `core/workbench.js` 的 RANK 一致），名称为菜单所显示的标签。括号内是模块在菜单下的
小标签。只有一页的模块本身就是一个链接，其页面不单独成行。

| 模块 | 页面 |
| --- | --- |
| [概览](04-overview.zh-CN.md) | （一页） |
| [监听](05-listeners.zh-CN.md) | （一页） |
| [引擎](06-engine.zh-CN.md)（端口） | 每个监听端口一个子菜单，端口下是其功能 |
| [全局配置](07-global-settings.zh-CN.md) | 日志、访问日志、遥测、指标、性能剖析、限制、引擎、配置来源、其他、导入 / 导出 |
| [JSON](08-json.zh-CN.md) | （一页，有草稿 / 生效配置两个标签） |
| [版本](09-versions.zh-CN.md) | （一页） |
| [一致性检查](10-consistency-check.zh-CN.md) | （一页） |
| [日志](11-logs.zh-CN.md) | （一页） |
| [集群](12-cluster.zh-CN.md)，仅在有 `admin.cluster` 时 | 概览、节点、分组、模板、发布、漂移与告警、用户与审计、指标、日志、证书、BitTorrent |
| [账户](13-account.zh-CN.md) | 我的密码、用户、本节点 |
| [助手](14-assistant.zh-CN.md)，仅在有 `admin.assistant` 时 | 功能、技能、提案、审计日志、MCP、设置 |
| [反向代理](15-reverse-proxy.zh-CN.md)（HTTP） | 路由、上游 |
| [正向代理](16-forward-proxy.zh-CN.md)（HTTP CONNECT） | （一页） |
| [四层代理](17-l4-proxy.zh-CN.md)（TCP/UDP） | （一页：四层服务） |
| [TLS 证书](18-tls.zh-CN.md)（TLS） | 证书、自动 HTTPS、ACME |
| [缓存](19-cache.zh-CN.md)（HTTP） | （一页：缓存区） |
| [流媒体](20-streaming.zh-CN.md)（RTMP / RTSP / SRT） | （一页） |
| [媒体认证](21-media-auth.zh-CN.md)（媒体令牌） | （一页） |
| [媒体库](22-media-library.zh-CN.md)（点播 / 频道） | 媒体库、频道、播放 URL、指标、媒体库配置、点播路由、频道配置、URL 签名 |
| [WebRTC](23-webrtc.zh-CN.md)（WebRTC） | （一页） |
| [隧道角色](24-tunnel.zh-CN.md)（隧道） | （一页） |
| [LLM 角色](25-llm.zh-CN.md)（LLM） | （一页） |
| [数据同步](26-sync.zh-CN.md)（管道） | 管道、同步配置 |
| [BitTorrent](27-bittorrent.zh-CN.md)（客户端 / tracker） | 种子、Tracker、RSS、发布、BitTorrent 配置 |

除集群与助手外，每个模块都调用配置本身的模块，因此只要 `admin.console` 运行，它们就都出现在清单里，
即使它们编辑的那部分配置尚未设置：此时流媒体、媒体库、数据同步与 BitTorrent 的页面会说明应如何设置，
配置应用后管道、客户端或 tracker 即启动。见[数据同步](26-sync.zh-CN.md)与
[BitTorrent](27-bittorrent.zh-CN.md)。

角色运行后，它自己的模块也会出现在清单最后：先是隧道的 **Hub**、**节点**、**proxy**（tunnel.hub、
tunnel.node、tunnel.proxy），再是 LLM 的 **Hub**、**节点**、**代理**（llm.hub、llm.node、llm.proxy），
各带该角色的页面；从控制台保存的配置启动或停止角色时菜单随之重建（在别处修改后请刷新页面）。见
[隧道角色](24-tunnel.zh-CN.md)与 [LLM 角色](25-llm.zh-CN.md)。

## 引擎菜单

引擎菜单把同一份视图放进导航栏：每个监听端口一个子菜单，按配置与默认 HTTPS 生成的顺序排列，标签为
名称加端口（`web :8080`）。端口展开后是它提供的各项功能，有数量的带数量——处理器（静态文件、直接应答、
FastCGI、媒体令牌、媒体页面，以及其路由上按模块 id 列出的其他处理器，如 `sync_ingest`、`bt_stream`、
`bt_publish`、`bt_tracker`）、反向与正向代理路由、缓存（缓存区与缓存的路由）、其他中间件、流媒体配置及其
HLS、HTTP-FLV、WHIP、WHEP 路由、点播路由与它播放的频道、WebRTC 配置及其路由、四层服务、SOCKS、TLS 证书、
ACME、自动 HTTPS 与到它的重定向——最后是监听表单**端口配置**。协议、自动 HTTPS、PROXY 协议、HTTP/3、
按需证书、客户端证书、MPTCP、点播以小标签显示在端口行下。详见[引擎](06-engine.zh-CN.md)。

## 问 AI

当助手可用时（`admin.assistant` 且用户级别不是 `off`），导航栏的端口与列出路由的条目、页面上的
每条路由、有后端不可用的上游等都有**问 AI**按钮（在导航栏与概览中是一个图标，提示文字写明技能）：点击后
以对应技能（如 `diagnose`）在对话面板中新建对话，把所问对象作为页面的选中项，并立即提问。没有助手时不显示
这些按钮。见[助手](14-assistant.zh-CN.md)。

## 相关文档

- [控制台与 store](../console.zh-CN.md#页面) — 工作台与页面的完整说明
- [概览](04-overview.zh-CN.md) · [引擎](06-engine.zh-CN.md) — 两个只读视图
