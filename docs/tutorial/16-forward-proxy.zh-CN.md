# 正向代理

[English](16-forward-proxy.md) | [简体中文](16-forward-proxy.zh-CN.md)

## 功能

正向代理不回答自己的站点，而是替客户端去连接别处。一条 `handler` 为 `forward_proxy` 的路由为
`clients` 列出的客户端代理 HTTP CONNECT：客户端发来 `CONNECT host:port`，fit 连上目的地，应答
`200 Connection established`，然后把这条连接变成双向的字节流，不再看里面的内容。CONNECT 在
HTTP/1.1、HTTP/2 与 HTTP/3 上都服务。页面把这一页说成 HTTP CONNECT（及普通 HTTP）的代理；处理器
实际只代理 CONNECT：一个不是 CONNECT 的请求（扩展 CONNECT 也算）答 405，`Allow: CONNECT`。所以
客户端按 HTTPS 代理的用法接入——`curl -x`、浏览器的代理设置、`HTTPS_PROXY` 都经 CONNECT 建隧道。

目的地由 `allow` 与 `deny` 决定。`allow` 写主机名（`*.example.com` 含 `example.com` 下面每一个
名字）、IP 地址与 CIDR 段，段也含主机名解析到的地址。无论 `allow` 写了什么，环回、链路本地
（`169.254.169.254`，多数云的元数据服务）、未指定与组播地址，以及 fit 所在主机自己的地址，默认都
被拒绝：否则客户端就能经代理走到 `127.0.0.1:2019` 的 Admin API 和主机上其他只留给自己的服务。要
放行它们，`allow` 得用位于这些段之内的段（`127.0.0.0/8` 算，`0.0.0.0/0` 不算），或单独写出主机
自己的那个地址。`deny` 无论 `allow` 有什么都拒绝。

监听器也可以用 `socks` 协议提供 SOCKS5（与 SOCKS4）代理：`listeners.socks` 说哪些客户端可以用、
连到哪里。它只服务 CONNECT，不认证，拒绝 BIND 与 UDP ASSOCIATE。`forward_proxy` 路由与
`listeners.socks` 是同一个对象，所以 `clients`、`allow`、`deny`、`ports` 与两个超时的写法完全相同；
不同的只是被拒的客户端怎么被拒：路由答 403，SOCKS 在握手后合上连接。一个端口同时服务 HTTP 与 socks
时，fit 按客户端先送的内容分辨：SOCKS 的问候给 socks，明文的 HTTP 请求给 HTTP。SOCKS5 的命令里只有
CONNECT 被服务，SOCKS4 只有连接这一种。两者都不认证，所以 `clients` 是唯一的门槛。

## 页面与操作

### 正向代理

"正向代理"模块在菜单里只有一个页面。它编辑配置的 `routes` 成员，但只显示 `handler` 为
`forward_proxy` 的路由；页顶的提示说明这一点，并提醒监听也能通过 `socks` 协议提供 SOCKS5。

- 如果草稿里有监听设置了 `socks`，页面列出"提供 SOCKS 的监听"：每个徽章写着监听的名字与地址，
  点开是[监听](05-listeners.zh-CN.md)页。这一行只读，`socks` 本身在监听页编辑。
- 每条路由一行：名字与 `match`，加上小标签——`clients`、`:ports`、`@listeners`（这条路由服务哪些
  监听；没写 `listeners` 的路由服务所有监听）。
- "添加正向代理路由"新建一条 `match { method CONNECT }` 的路由，`clients` 与 `ports` 是占位值，
  名字取 `forward-1`、`forward-2`……
- 路由行点开是它的表单：`match` 与 `handler` 的参数逐项编辑，`clients`、`allow`、`deny`、`ports`
  与两个超时都在其中。
- 这里编辑的是共享草稿：表单与 `.conf`、`.yaml`、`.json` 标签是同一份草稿，"保存并应用"校验并
  应用。页面显示的是整个 `routes` 成员，不只是本页过滤出的那些，所以一致性检查与 JSON 页报的问题
  也出自这里。

### 在引擎页中

[引擎](06-engine.zh-CN.md)页按端口列出正在提供的功能：这个端口下列出"正向代理路由"，链回本页；
同一端口若提供 SOCKS，列出"SOCKS 正向代理"，链到监听页。端口行的小标签写着协议与
`proxy_protocol`。

## 参数

只列关键参数；完整字段表见[配置参考：Handlers](../reference/config.md#http-handlers-forward_proxy)，
`listeners.socks` 的表在[配置参考：Listeners](../reference/config.md#listeners)。两处是同一个对象，
所以字段相同。

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `clients` | 必填 | 可以用代理的客户端地址与 CIDR 段，如 `10.0.0.0/8`；其他客户端被拒（路由答 403，SOCKS 合上连接）。 |
| `allow` | 全部，除默认拒绝的那些 | 代理可以连去的目的地：主机名、`*.example.com`、IP 与 CIDR 段；段也含主机名解析到的地址。 |
| `deny` | 无 | 无论 `allow` 有什么都拒绝的目的地，写法同 `allow`。 |
| `ports` | 任意 | 允许的目的端口，如 `443`。 |
| `connect_timeout` | `10s` | 解析并连接一个目的地最多多久；超时答 504。 |
| `idle_timeout` | `5m` | 一条隧道两边都没有字节多久后被关闭。 |

### 默认被拒绝的地址

这些地址默认拒绝，与 `allow` 有什么无关；要放行就得在 `allow` 里点名它们：

- 环回：`127.0.0.0/8` 与 `::1`；
- 链路本地：`169.254.0.0/16`，其中 `169.254.169.254` 是多数云的元数据服务；
- 未指定：`0.0.0.0/0` 与 `::/0`；
- 组播；
- fit 所在主机自己的地址：只有单独写出那个地址才算，写一个更宽的段不算。

### 被拒的几种情形

- 客户端不在 `clients`：路由答 403，SOCKS 在握手后合上连接；
- 目的地不在 `allow`，或属于上面那些默认拒绝的地址：路由答 403；
- 一个主机名解析到被拒的地址：在连上之后、送出任何字节之前被拒，路由答 502；
- 解析或连接超过 `connect_timeout`：答 504。

### 几点常被误读

- `clients` 说的是客户端，不是目的地；目的地是 `allow` 与 `deny`。
- `allow` 里的段也含主机名解析到的地址，所以一个写在 `allow` 里的名字仍可能被拒。
- `deny` 优先于 `allow`：`allow` 含的地址，`deny` 一样拒。
- `ports` 限的是要去的那一端的目的端口，不限监听的端口。

### 两处必填，两种"谁能用"

`clients` 必填：路由少了它，校验报缺字段。监听的 `protocols` 里有 `socks` 时，`listeners.socks` 也
必填：协议只说这个端口提供 SOCKS，谁能用、连到哪里由它说。

`clients` 管的是谁可以用代理；谁能连上监听本身由 `access` 决定——被 `access` 拒掉的连接走不到
路由，也就轮不到 `clients` 说话。

## .conf 配置

一个监听提供 SOCKS5，一条路由提供 HTTP CONNECT，两者用同一套规则。这是自包含的完整配置，能通过
`fit validate`：

```fit
listener proxy :8080 http/1.1 socks {
	socks {
		clients 10.0.0.0/8
		allow *.example.com
		ports 443
	}
}

route {
	match { method CONNECT }
	handler forward_proxy {
		clients 10.0.0.0/8
		allow *.example.com
		ports 443
	}
}
```

简写形式里，参数后面的块是监听自己的字段，所以 `clients` 要写在 `socks` 块里面，不能直接写在监听
块里。`allow` 里没有 `127.0.0.0/8`，所以客户端连不到本机；要连本机，把它写进 `allow`。`ports 443`
只放行目的端口 443——限的是要去的那一端，不是监听的这一端；去掉 `ports` 就是任意端口。

这一页编辑的是 `routes` 成员：在 JSON 与 YAML 里，同样的写法是 `routes[0].handler.params`，所以这里
的监听与路由，和[模块文档](../../schema/doc/modules/forward_proxy.md) 中的 JSON 示例是同一份配置。

## 相关文档

- [监听](05-listeners.zh-CN.md)：`socks` 协议与 `listeners.socks`。
- [引擎](06-engine.zh-CN.md)：端口上把这条路由与 SOCKS 列在何处。
- [反向代理](15-reverse-proxy.zh-CN.md)：路由的 `match`、`handler` 与 `middleware`。
- [配置参考：Handlers](../reference/config.md#http-handlers-forward_proxy)：`forward_proxy` 的全部字段。
- [配置参考：Listeners](../reference/config.md#listeners)：`listeners.socks` 的全部字段。
- [模块文档 forward_proxy](../../schema/doc/modules/forward_proxy.md)：默认拒绝的地址与一个路由示例。
- [谁可以连接](../configuration.zh-CN.md#谁可以连接)：`access` 与客户端地址。
- [完整配置模板](../../templates/conf/conf.d/10-listeners.conf)：每个字段带注释。
