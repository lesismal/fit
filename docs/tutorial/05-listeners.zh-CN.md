# 监听

[English](05-listeners.md) | [简体中文](05-listeners.zh-CN.md)

## 功能

一个监听是**一个地址，加上这个地址上提供的协议**。路由和四层服务按 `name` 引用监听：一条路由不写
`listeners` 时服务所有监听。

监听决定三件事：在哪个地址上监听（`address`）、用哪些协议（`protocols`：`http/1.1`、`h2c`、`h2`、`h3`、
`tcp`、`udp`、`socks`），以及 TLS（`tls`：证书来自文件、ACME，或在握手中按需申请）。`client_auth` 要求并
校验客户端证书（mTLS）；`proxy_protocol` 从信任的代理读取 PROXY 协议头；`socks` 让 `socks` 协议成为
SOCKS5 正向代理；`mptcp` 在内核支持时以多路径 TCP 监听。

整个配置不写任何监听时，[HTTPS by default](../reference/config.md#https-by-default) 会按路由的 host 名生成
443 与用于跳转的 80；`tls` 写空的监听（`{}`）会为它服务的路由的 host 名取得证书。

监听与别的部分这样分工：路由用 `listeners` 说出它服务哪些监听，不写的路由服务所有监听；四层服务列出它
代理哪个 `tcp` 或 `udp` 监听的连接；`socks` 协议按 `socks` 的 `clients` 决定谁可以用。本页只编辑监听本身，
运行中的每个端口在[引擎](06-engine.zh-CN.md)里展开成它提供的功能。

## 页面与操作

### 监听（Listeners）

菜单**监听**下只有同名的一页，URL 是 `#/gw.listeners/listeners`。本页编辑配置的 `listeners` 成员，
也就是监听本身；每个端口的功能——处理器、路由、缓存、流媒体、WebRTC、四层、SOCKS、TLS——按端口分组列在
[引擎](06-engine.zh-CN.md)菜单中。页面上方的提示写着：各端口的功能都在侧边栏的“引擎”菜单中分组列出；
本页编辑监听本身。

- **每一行**显示监听的名字、`address`，以及 `protocols` 中每个协议的标记；有 `tls` 的行加一个 `tls` 标记，
  有 `socks` 的行加一个 `socks` 标记。点开一行就是它的表单。
- **添加监听**新建一行：默认 `name` 为 `web-N`、`address` 为 `:8080`、`protocols` 为 `http/1.1`。
  每行可**删除**、**上移**、**下移**。
- 表单的字段是 `name`、`address`、`protocols`、`mptcp`、`tls`（含 `certificates`、`acme`、`on_demand`、
  `client_auth`）、`socks`、`access`、`proxy_protocol`。
- 与所有编辑配置的页面一样，本页共用同一份草稿：顶栏的**保存并应用**整体保存（`PUT /config`），
  **检查**走完整的应用流程但不应用，并说出哪些监听会被新增或移除；**界面**之外还有 `.conf`、`.yaml`、
  `.json` 三个标签，编辑本页所管的 `listeners` 成员在对应格式下的文本。
- 地址写的是配置里的地址；内核实际绑定的地址，以及每个端口提供的功能，在[引擎](06-engine.zh-CN.md)的端口行
  上列出。

## 参数

只列关键参数；完整字段表见[配置参考：Listeners](../reference/config.md#listeners)，各对象另有其表：
[`listeners[]`](../reference/config.md#config-listener)、
[`listeners[].tls`](../reference/config.md#config-tls)、
[`listeners[].tls.certificates[]`](../reference/config.md#config-certificate)、
[`listeners[].tls.client_auth`](../reference/config.md#config-clientauth)、
[`listeners[].socks`](../reference/config.md#config-forwardproxy)、
[`listeners[].access`](../reference/config.md#config-access)、
[`listeners[].proxy_protocol`](../reference/config.md#config-proxyprotocol)。

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `name` | 必填 | 监听之间唯一的名字，路由与四层服务用它引用这个监听。 |
| `address` | 必填 | 监听的地址与端口，如 `:8080` 或 `127.0.0.1:8080`。 |
| `protocols` | `http/1.1`，有 `tls` 时 `http/1.1` 与 `h2` | 提供的协议：`http/1.1`、`h2c`（明文的 HTTP/2）、有 `tls` 时的 `h2`（经 ALPN 选择）与 `h3`（QUIC，同一端口的 UDP，向 HTTP/1 和 HTTP/2 客户端以 Alt-Svc 宣告）；`tcp` 原始 TCP，其连接由列出该监听的[四层服务](17-l4-proxy.zh-CN.md)转发到上游；`udp` 数据报；`socks` 是 SOCKS5 与 SOCKS4 正向代理。一个端口上同时提供 HTTP、`tcp` 和 `socks` 时，fit 按客户端先送来的是什么区分：TLS ClientHello 交给 `server_names` 含其名的服务，或（有 `tls` 时）按 HTTPS 应答；SOCKS 问候交给 `socks`；明文的 HTTP 请求交给 HTTP；其余交给没有 `server_names` 的服务，没有则关闭。等服务器先开口的客户端 10s 后被关闭。 |
| `mptcp` | 关闭 | 内核支持时（Linux 5.6+、`net.mptcp.enabled`）以多路径 TCP 监听该监听的 TCP 连接，不支持时回退到普通 TCP，没有多路径 TCP 的客户端也回退。`udp` 监听与 HTTP/3 没有该功能。 |
| `tls` | 无 | 有 `tls` 时该监听的 HTTP 就是 HTTPS。`certificates` 是证书文件；`acme` 是要为哪些名字申请证书；`on_demand` 在握手中为客户端请求的名字现场申请（需要顶层 `acme.on_demand`）；写空的 `tls`（`{}`）为路由的 host 名取得证书。 |
| `tls.client_auth` | `mode` 为 `require` | 要求并校验客户端证书（mTLS）：`ca_files` 是客户端证书必须链到的 CA 文件，会像证书文件一样被监视。`verify_if_given` 允许客户端不送证书，但送来的必须有效。要求证书的监听无法应答 ACME 的 TLS-ALPN-01 挑战。 |
| `socks` | 无（`socks` 协议必需） | 谁可以用这个 SOCKS 代理（`clients`），以及它能连到哪里（`allow`、`deny`、`ports`）：只服务 CONNECT，无认证，拒绝 BIND 与 UDP ASSOCIATE。 |
| `access` | 全部地址 | 该监听服务的客户端地址（`allow`/`deny`，也可用 `allow_files`/`deny_files`），先于全局 `access` 决定；被拒绝的连接一被接受就关闭。有 `proxy_protocol` 时地址是头部写出的那个。 |
| `proxy_protocol` | 无 | 从 `trusted` 地址读取 PROXY 协议 v1 或 v2 头，并按头中写出的客户端地址服务这条连接；来自这些地址的连接必须在 10s 内以头部开头，否则关闭。用于 `tcp` 与 HTTP over TCP；有 `tls` 时头部在 TLS 之前。HTTP/3 不读取。 |

表中的默认值是字段留空时 fit 补上的值。证书文件与 `client_auth` 的 `ca_files` 会被监视：文件一变，配置就
重新加载；`access` 的 `allow_files` 与 `deny_files` 在应用配置时读取，变化时再应用，每个至多 64 MiB。

### 一个端口上的多个协议

一个端口同时提供 HTTP、`tcp` 与 `socks` 时，fit 按客户端先送来的是什么区分：

- TLS ClientHello：交给 `server_names` 含其名的服务；没有那样的服务时，有 `tls` 就按 HTTPS 应答，否则交给
  没有 `server_names` 的服务。
- SOCKS 问候：交给 `socks`。
- 明文的 HTTP 请求：交给 HTTP。
- 其余：交给没有 `server_names` 的服务，没有则关闭。

等服务器先开口的客户端 10s 后被关闭，这样的协议需要自己的监听。同一号码的 TCP 与 UDP 端口不冲突，所以
HTTP 与 HTTP/3 可共用一个端口；由内核挑选的 0 端口从不冲突。

### 一个地址，一个服务器

两个监听不能在同一地址和同一传输上监听，监听也不能与配置中其他监听的部分（Admin API、控制台、角色的
链路监听器或 API、隧道转发、流媒体或 WebRTC 端口）冲突；监听也无法绑定别的进程占着的地址。`:443`、
`0.0.0.0:443` 和 `[::]:443` 是同一个地址，它也包含 `127.0.0.1:443`。同一号码的 TCP 与 UDP 端口不冲突，
所以 HTTP 与 HTTP/3 可以共用一个端口。详见[一个地址，一个服务器](../configuration.zh-CN.md#一个地址一个服务器)。

## .conf 配置

```fit
# The listeners: the addresses fit listens on and the protocols served on each.
listener web :8080 http/1.1 h2c {
	mptcp true
}

listener secure :443 http/1.1 h2 h3 {
	tls {
		# Certificates from files, and the CA files of client_auth, must exist for
		# fit validate to read them, so they are commented out here.
		# certificates {
		# 	cert_file /etc/fit/example.com.pem
		# 	key_file /etc/fit/example.com.key
		# }
		acme example.com www.example.com
		# on_demand true needs the top-level acme.on_demand, which says whom to ask.
		# client_auth {
		# 	ca_files /etc/fit/clients-ca.pem
		# 	mode verify_if_given
		# }
	}
	access {
		allow 10.0.0.0/8
		deny 203.0.113.0/24
	}
}

# A tcp listener needs a service that lists it, so it is commented out here:
# listener internal 127.0.0.1:8081 tcp { proxy_protocol { trusted 10.0.0.0/8 } }

listener proxy :1080 socks {
	socks {
		clients 10.0.0.0/8
		allow *.example.com
		deny 169.254.169.254
		ports 443
	}
}

route { handler respond { body "hello from fit\n" } }
```

这份配置用 `./bin/fit validate -c` 校验通过。证书与 CA 文件必须存在，`fit validate` 才会读取它们，所以
示例中注释掉；`tcp` 监听需要一条列出它的服务，也注释掉了。

## 相关文档

- [配置参考：Listeners](../reference/config.md#listeners)（全部字段）与[中文的章节说明](../reference/config.zh-CN.md#各章内容)
- [一个地址，一个服务器](../configuration.zh-CN.md#一个地址一个服务器)
- [.conf 格式](../reference/fit.zh-CN.md)
- [引擎](06-engine.zh-CN.md)：按端口看运行中的功能
- [TLS 证书](18-tls.zh-CN.md)、[正向代理](16-forward-proxy.zh-CN.md)、[四层代理](17-l4-proxy.zh-CN.md)
- 带全部字段的注释模板：[templates/conf/conf.d/10-listeners.conf](../../templates/conf/conf.d/10-listeners.conf)
