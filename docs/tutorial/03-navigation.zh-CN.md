# 功能导航栏

[English](03-navigation.md) | [简体中文](03-navigation.zh-CN.md)

## 功能

登录后的整页就是 **workbench（工作台）**：左侧是功能导航栏，右侧是所选页面。导航栏的第一级是
控制台所提供各模块的平铺列表，第二级是模块的页面，第三级是引擎菜单中端口的功能。只有一个页面
（或一个外链）的模块直接是链接。

导航栏只显示控制台实际提供的模块：`admin.console` 提供配置本身的模块与同进程运行的每个角色的
模块；角色自己的控制台只提供该角色的模块。每个页面有自己的 URL：`#/<模块>/<页面>`，刷新或分享
链接都能直达。

## 布局

- **左侧导航栏**：品牌区（fit 与"工作台"）、筛选框、模块树、底部（当前用户与登出）。
- **顶栏**：菜单按钮（窄屏）、标题与面包屑、页面工具、主题与语言选择。
- **右侧内容区**：所选页面。

导航栏可以筛选（递归匹配：任一后代命中时其模块或端口仍显示）、收起为图标（`is-mini`），模块与
端口可以展开和折叠，展开状态保存在浏览器本地。主题有浅色、深色及另外三种；语言为英文或中文。

## 模块与页面清单

按菜单顺序（与 `core/workbench.js` 的 RANK 一致）。括号内是模块在菜单下的小标签。

| 模块 | 页面 |
| --- | --- |
| [概览](04-overview.zh-CN.md) | 概览 |
| [监听](05-listeners.zh-CN.md) | 监听 |
| [引擎](06-engine.zh-CN.md)（端口） | 每个监听端口一个子菜单，端口下是其功能 |
| [全局配置](07-global-settings.zh-CN.md) | 日志、访问日志、遥测、指标、性能剖析、限制、引擎、配置来源、其他、导入 / 导出 |
| [JSON](08-json.zh-CN.md) | JSON 配置（草稿 / 生效配置两个标签） |
| [版本](09-versions.zh-CN.md) | 配置版本 |
| [一致性检查](10-consistency-check.zh-CN.md) | 一致性检查 |
| [日志](11-logs.zh-CN.md) | 日志 |
| [集群](12-cluster.zh-CN.md) | 概览、节点、分组、模板、发布、漂移与告警、用户与审计、指标、日志、证书、BitTorrent |
| [账户](13-account.zh-CN.md) | 我的密码、用户、本节点 |
| [助手](14-assistant.zh-CN.md) | 功能、技能、提案、审计日志、MCP、设置 |
| [反向代理](15-reverse-proxy.zh-CN.md)（HTTP） | 路由、上游 |
| [正向代理](16-forward-proxy.zh-CN.md)（HTTP CONNECT） | 正向代理 |
| [四层代理](17-l4-proxy.zh-CN.md)（TCP/UDP） | 四层服务 |
| [TLS](18-tls.zh-CN.md)（TLS） | 证书、自动 HTTPS、ACME |
| [缓存](19-cache.zh-CN.md)（HTTP） | 缓存区 |
| [流媒体](20-streaming.zh-CN.md)（RTMP / RTSP / SRT） | 流媒体 |
| [媒体认证](21-media-auth.zh-CN.md)（媒体令牌） | 媒体认证 |
| [媒体库](22-media-library.zh-CN.md)（点播 / 频道） | 媒体库、频道、播放 URL、指标、媒体库配置、点播路由、频道配置、URL 签名 |
| [WebRTC](23-webrtc.zh-CN.md)（WebRTC） | WebRTC |
| [隧道角色](24-tunnel.zh-CN.md)（隧道） | 隧道角色 |
| [LLM 角色](25-llm.zh-CN.md)（LLM） | LLM 角色 |
| [数据同步](26-sync.zh-CN.md)（管道） | 管道、同步配置 |
| [BitTorrent](27-bittorrent.zh-CN.md)（客户端 / tracker） | 种子、Tracker、RSS、发布、BitTorrent 配置 |

最后两个模块属于控制台本身，因此只要控制台运行就出现在清单里，即使其角色尚未配置；此时页面会说明
应如何设置，配置应用后管道与客户端或 tracker 即启动。见[数据同步](26-sync.zh-CN.md)与
[BitTorrent](27-bittorrent.zh-CN.md)。

角色运行后，它自己的模块也会出现在清单最后：隧道为 **Hub**、**节点**、**proxy**（tunnel.hub、
tunnel.node、tunnel.proxy），LLM 为 **Hub**、**节点**、**proxy**（llm.hub、llm.node、llm.proxy），
各带该角色的页面。见[隧道角色](24-tunnel.zh-CN.md)与 [LLM 角色](25-llm.zh-CN.md)。

## 引擎菜单

引擎菜单把同一份视图放进导航栏：每个监听端口一个子菜单，按配置与默认 HTTPS 生成的顺序排列。
端口展开后是其处理器（静态文件、直接应答、FastCGI、媒体令牌、媒体页面，以及其路由上按模块 id
列出的其他处理器：`vod`、`sync_ingest`、`bt_stream`、`bt_publish`、`bt_tracker`）、反向与正向
代理路由、缓存（缓存区与缓存的路由）、其他中间件、流媒体与 WebRTC 处理器、四层服务、SOCKS、
TLS 证书、ACME、自动 HTTPS 与到它的重定向，最后是监听表单。协议、PROXY 协议、HTTP/3、按需证书、
客户端证书、MPTCP、点播以小标签显示在端口行下。详见[引擎](06-engine.zh-CN.md)。

## 问 AI

当助手可用时（`admin.assistant` 且用户级别不是 `off`），导航栏的端口与列出路由的条目、页面上的
每条路由、有后端不可用的上游等都有"问 AI"按钮，点击后以对应技能（如 `diagnose`）提问并把所问
对象作为选中项发送。见[助手](14-assistant.zh-CN.md)。

## 相关文档

- [控制台与 store](../console.zh-CN.md#页面) — 工作台与页面的完整说明
- [概览](04-overview.zh-CN.md) · [引擎](06-engine.zh-CN.md) — 两个只读视图
