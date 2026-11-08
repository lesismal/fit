# 隧道角色

[English](24-tunnel.md) | [简体中文](24-tunnel.zh-CN.md)

## 功能

隧道让人经由一台谁都能连上的 hub，访问只有旁边那台主机才连得到的机器上的 TCP 服务。它是配置里的三个角色，运行在监听器的
fib 引擎上，可以和网关的路由并存，也可以单独运行：

```text
 本地应用 ──TCP──▶ tunnel.proxy ══链路══▶ tunnel.hub ══链路══▶ tunnel.node ──TCP──▶ 服务
 (ssh、浏览器)     每个转发一个本地端口    路由隧道，转发隧道帧     位于服务旁边        10.0.0.5:22
```

**hub** 注册节点和 proxy，把 proxy 打开的每条隧道路由到它指名的节点，并在两端之间转发隧道的帧；**节点**连接一台或多台 hub，
带着它能访问的服务注册，并为路由给它的每条隧道连接对应的服务，不连接其他地址；**proxy** 连接 hub，得知它们的节点和服务，为每个
转发监听一个本地地址，那里的每个连接都是一条隧道。节点和 proxy 都主动向外连接 hub，所以都不需要开放端口。隧道的每个字节都经 hub
中继。

链路是 TLS、WSS 或 QUIC；明文的 TCP 与 WebSocket 只用于回环地址，或必须配合 `body_password`。每个节点和 proxy 用 hub 签发的
`token` 注册：hub 的 `tokens` 只保存哈希，令牌本身只在签发时显示一次（`fit token create -role tunnel.hub -name lab`，或 hub 的
**令牌**页）。共享 `password` 这一版本仍接受，但已弃用，页面在仍有对端使用它时给出警告。

## 页面与操作

角色的页面在角色运行后出现在菜单中：在**隧道角色**页设置成员，配置应用后角色启动，其模块出现在菜单的隧道组下。这些页面的修改
写回角色在配置中的成员并立即生效，无需重启；fit 把配置保存在 `admin.store` 时以加密方式保存，没有 store 时修改只在重启或重新读取
配置文件之前有效，页面会提示这一点。每个角色页面每 3 秒刷新一次，都有隧道全景图（**详情**）。

### 隧道角色（Tunnel roles）

编辑 `tunnel` 成员：`hub`、`node` 与 `proxy` 各一张表单，原始文本标签页（UI / .conf / .yaml / .json）为整个成员。页面说明：
在这里设置的角色在配置应用后启动，移除后停止，其页面在角色运行后出现在菜单中。

### tunnel.hub（Hub）

#### 概览

四个统计：节点（及其服务数）、proxy（其中有多少正在转发）、隧道（上行与下行的字节）、监听（其中失败的个数，或“全部已打开”）。
**本 hub** 卡片给出名称、版本、对端连接地址、控制台 WebSocket、消息体加密、已注册链路与运行时长。**监听**表给出每个监听的协议与
对端连接地址（失败时给出错误），点击行到**监听**页。下方是最近的十条隧道。仍有对端用共享密码注册时，页面顶部给出警告并链接到**令牌**。

#### 节点

每个已注册的节点一行：ID、名称、地址（及其链路数）、延迟、主机（含系统与版本）、它的服务（名称 → 地址、描述与建连时间）、隧道数与
已连接时间。操作：**查询**（让节点重新报回它的服务）、**编辑服务**（仅在节点的 `allow_hub_services` 允许时写入节点自己的成员）与
**踢出**。

#### proxy

每个已注册的 proxy 一行：ID、名称、地址与链路数、延迟、主机、它告知的 meta、隧道数与已连接时间；操作是**踢出**。

#### 隧道

每条隧道：ID、proxy、节点、服务、地址、整条路径的延迟（proxy 到 hub、hub 到节点，加上节点到服务的建连，各段在提示中）、上行与
下行的字节、时长，以及**详情**（这条隧道的完整链路）与**关闭**。

#### 监听

添加、编辑与移除监听：协议（`wss`、`tls`、`quic`、`ws`、`tcp`）、地址，以及 ws 或 wss 的路径（默认 `/tunnel`）。修改立即生效，
无需重启；移除 QUIC 监听会关闭其上的链路，其他监听上的链路保持到自行关闭。每行显示该监听的证书指纹（SHA-256），hub 自有证书标出
“自有证书”。页面说明：TLS、WSS 与 QUIC 监听向固定了本 hub 证书指纹的节点和 proxy 证明 hub 的身份；TCP 与 WS 明文传输链路，用于
非回环地址时须设置消息体密码。

#### 令牌

本 hub 签发的令牌：签发（只显示一次，供复制，填到节点或 proxy 的 `token`）、列出每个令牌的过期时间、自启动以来的最近使用及其来源
地址、它的成员和当前使用它的链路，修改过期时间，吊销（立即关闭它的链路）。只要还有对端用共享密码注册，这里给出警告。

#### 设置

**密码与控制台 WebSocket**：共享注册密码（已弃用，可**移除共享密码**，从此只接受令牌注册）、消息体 AES 密码（可移除）、控制台
WebSocket 的密码与地址（默认用注册密码）。已设置的密码不会返回：留空则保持不变。已注册的对端保持连接；新的消息体密码只用于此后
新建的链路。**Hub 证书**：指纹（SHA-256）与复制，节点和 proxy 把此指纹填作 hub 的证书指纹；添加 TLS、WSS 或 QUIC 监听时生成，
并写入配置。

### tunnel.node（节点）

#### 概览

四个统计：hub（已注册 / 总数）、服务、隧道（入与出的字节）、打开的隧道（当前 / 上限）。**本节点**卡片给出名称、版本、隧道数与
meta；hub 表给出每个 hub 的名称、状态、延迟与隧道数，点击行到 **hub** 页。

#### hub

每个 hub 一行：名称、各端点（按尝试顺序，停用的划掉）、状态（其下为错误）、各链路所用的协议、消息体是否已加密、延迟、hub 的名字、
Peer ID 与连接池（当前 / 上限）、隧道数；操作是**编辑**与**移除**。编辑器填写名称、连接池大小、hub 签发的令牌（令牌会替换密码，
密码也会替换令牌）、消息体密码、hub 证书指纹（SHA-256），按尝试顺序排列各协议的地址与启用的开关，以及仅用于测试的“接受任意 QUIC
证书”。

#### 服务

本节点可访问的服务：名称、地址与描述，原地编辑；每行显示建连时间。**保存并通知 hub** 写入节点自己的成员并告知所有 hub，**还原**
丢弃未保存的修改；修改后轮询不会覆盖。这些地址也是节点唯一会连接的服务地址。

#### 隧道

每条隧道：hub、ID、proxy、服务、地址、建连时间、本地地址、入与出的字节、时长，以及**详情**与**关闭**。

### tunnel.proxy（proxy）

#### 概览

四个统计：hub（已注册 / 总数）、节点（及其服务数）、转发（其中失败的个数，或“全部监听中”）、隧道（发送与接收的字节）。**本 proxy**
卡片给出名称、版本、隧道数与告知 hub 的信息；**转发**表列出每个转发。

#### hub

每个 hub 一张卡片：各端点、状态与所用协议、延迟、hub 的名字、Peer ID、连接池、节点数、隧道数与消息体加密，以及它的 **控制台**——
该 hub 的 `web_proxies` 在本地提供的地址、经由的 URL、连接数与**打开**。不属于任何 hub 的 Web 代理单独一张卡片。

#### 节点与服务

每个 hub 上的每个节点一行：hub、节点、经该 hub 到它的延迟、该节点隧道的分担比例、它的服务（名称 → 地址、描述与建连时间），每个服务
旁是**转发…**（预填表单），以及**编辑服务**——仅在 hub 的 `allow_proxy_services` 与节点的 `allow_hub_services` 都允许时可用。

#### 转发

每个转发：本地地址、节点、服务、hub（未指定时为任意，隧道分散到所有有这个节点的 hub，到节点路径更快的 hub 分得更多）与状态；可
**添加转发**、**打开**其本地地址与**移除**。

#### 隧道

每条隧道：hub、ID、本地地址、节点、服务、地址、延迟、本地 proxy（发起连接的地址）、发送与接收的字节、时长，以及**详情**与**关闭**。

#### proxy 信息

告知 hub 的信息，以键值行编辑；**更新到 hub** 后，hub 在其 **proxy** 页上显示这些键值。

## 参数

关键参数；完整字段表见[配置参考](../reference/config.md#tunnel)。

### tunnel.hub

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `name` | 主机名 | 告知节点和 proxy 的名字 |
| `tokens` | 无 | hub 签发的令牌，只保存哈希（`{id, name, hash, created_at, expires_at, note, members}`）；由页面、Admin API 或 `fit token` 生成，不要手写 |
| `password` | 无 | 已弃用的共享注册密码：本版本仍接受，有对端使用时页面给出警告，下一版本拒绝；不设置时只接受令牌注册 |
| `body_password` | 无 | 对每个帧的消息体做 AES-256-GCM 加密并认证该帧；非回环地址上的 `tcp` 或 `ws` 监听需要它；用令牌注册的链路由令牌与它经 HKDF 派生密钥 |
| `listeners` | 无 | `{protocol, address, path, cert_file, key_file}`：`tls`、`wss`、`quic`，或明文的 `tcp`、`ws`；ws 与 wss 的路径默认 `/tunnel`；没有 `cert_file` 时出示 hub 自己的证书 |
| `access` | 由全局 `access` 决定 | 监听与 `console_socket` 接受哪些客户端地址 |
| `console_socket` | 无 | `{address, password, max_conns}`：经加密 WebSocket 的控制台，供 proxy 的 `web_proxies` 使用；需要 `console` |
| `quic` | 见参考 | hub 自己的证书（`cert`、`key`）与 QUIC 的上限和超时；需要证书时 hub 生成自签名证书并保存，重启后指纹不变 |
| `register_timeout`、`call_timeout` | 10s、15s | 一条链路注册的时限，以及节点应答的时限 |
| `max_links`、`max_links_per_peer`、`max_peers`、`max_tunnels` | 1024、128、1024、16384 | hub 保有资源的上限 |
| `max_unregistered`、`max_unregistered_per_ip` | 256、32 | 尚未注册的已打开链路，总数与每个 IP 地址的数量 |
| `allow_proxy_services` | false | 允许已注册的 proxy 经 hub 设置节点的服务；节点仍须开启自己的 `allow_hub_services` |
| `console` | 无 | hub 的页面 |

### tunnel.node

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `name` | 主机名 | proxy 用来找到这个节点的名字 |
| `meta` | 无 | 告知 hub 的其他信息 |
| `hubs` | 无 | 要连接的 hub（见下） |
| `services` | 无 | `{name, address, description}`：节点能访问的服务，也是它唯一会连接的地址 |
| `allow_hub_services` | false | 允许它的 hub 设置 `services` 并写入本成员 |
| `dial_timeout`、`idle_timeout`、`max_tunnels` | 5s、2h、4096 | 连接一个服务的时限、空闲隧道关闭前的时长、同时打开的隧道数 |
| `console` | 无 | 节点的页面 |

### tunnel.proxy

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `name`、`meta`、`hubs` | | 同节点 |
| `forwards` | 无 | `{listen, node, service, hub}`：不写 `hub` 时隧道分散到所有有这个节点的 hub |
| `web_proxies` | 无 | `{name, listen, url, password, max_conns}`：经 hub 的 `console_socket` 在本地提供它的控制台 |
| `access` | 由全局 `access` 决定 | 作用于 `forwards` 与 `web_proxies` 接受的连接 |
| `open_timeout`、`idle_timeout`、`max_tunnels` | 20s、2h、4096 | 打开一条隧道的时限、空闲隧道关闭前的时长、同时打开的隧道数 |
| `console` | 无 | proxy 的页面 |

### 节点或 proxy 连接的 hub（`hubs[]`）

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `name` | 第一个端点的地址 | 转发和页面用来指称它的名字 |
| `endpoints` | 必填 | `{address, disabled}`，按顺序尝试：`tls://`、`wss://`（路径默认 `/tunnel`）、`quic://`；明文的 `host:port`、`tcp://` 与 `ws://` 连接非回环主机时需要 `body_password` |
| `token` | 必填其一 | hub 为该成员签发的令牌 `fitt1.<id>.<secret>`；`token` 与 `password` 必须设置其一，不能都设 |
| `body_password` | 无 | hub 的消息体密码（如果有） |
| `pin` | 无 | hub 证书的 SHA-256 指纹（与 hub 页面所示一致），按它校验证书；不能与 `ca_file` 同时设置 |
| `ca_file` | 无 | 为 hub 证书签名的 CA 的 PEM 文件，按它校验并校验名称 |
| `quic_insecure` | false | 接受任意 QUIC 证书：仅用于测试；非回环主机需要 `body_password` |
| `pool_size` | 10（最多 100） | 承载隧道的链路数；每条隧道始终走一条链路 |
| `dial_timeout`、`call_timeout`、`retry_interval`、`ping_interval`、`ping_timeout` | 5s、10s、3s、5s、5s | 拨号、应答、重连前的等待，以及测延迟的间隔与超时 |

## .conf 配置

```fit
tunnel {
	hub {
		listeners { protocol wss; address :7443 }
		tokens lab {
			id fdd44d906b8e6ce2
			hash 1c994a095de7b566dcce0f229b829af96d85911b6d3e786f217c77340bc36aa4
			created_at 1793900000
		}
	}
	node {
		name lab
		hubs hub {
			endpoints { address wss://hub.example.com:7443/tunnel }
			token fitt1.fdd44d906b8e6ce2.Hm2vJvGyTEJVh2_zzu0ZJlENz1A3IYXS2JydjHyJShQ
			pin 02e5420d38831f019c363d82b2d7ee2ff7db1a143401587e3b9a92f0011e8261
		}
		services ssh { address 10.0.0.5:22 }
	}
}
```

`tokens` 与 `pin` 不手写：hub 的**令牌**页签发每个令牌（只显示一次），hub 的**设置**页显示它的证书指纹；把两者填到节点的 `token`
与 `pin`。只有角色、没有路由的配置不需要监听器。proxy 的写法见[配置参考](../reference/config.md#config-tunnelproxy)：它的
`forwards { listen 127.0.0.1:2222; node lab; service ssh }` 让 `ssh -p 2222 127.0.0.1` 连到 10.0.0.5:22。

## 相关文档

- [隧道](../tunnel.zh-CN.md)：运行角色、链路（TLS、WSS、QUIC 与明文）、[令牌](../tunnel.zh-CN.md#令牌)、[链路靠什么保护](../tunnel.zh-CN.md#链路靠什么保护)、[控制台](../tunnel.zh-CN.md#控制台)、流控与上限。
- [配置参考：Tunnel](../reference/config.md#tunnel)与[控制台与 store](../console.zh-CN.md#页面)。
- [功能导航栏](03-navigation.zh-CN.md)：角色的动态模块。
- 页面代码：[app/workbench/web/tunnel/hub.js](../../app/workbench/web/tunnel/hub.js)、
  [node.js](../../app/workbench/web/tunnel/node.js)、[proxy.js](../../app/workbench/web/tunnel/proxy.js)与
  [shared.js](../../app/workbench/web/tunnel/shared.js)。
