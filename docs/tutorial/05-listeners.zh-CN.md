# 监听

[English](05-listeners.md) | [简体中文](05-listeners.zh-CN.md)

## 功能

一个监听是**一个地址，加上这个地址上提供的协议**。路由和四层服务按 `name` 引用监听。

监听决定三件事：在哪个地址上监听（`address`）、用哪些协议（`protocols`：`http/1.1`、`h2c`、`h2`、`h3`、
`tcp`、`udp`、`socks`），以及 TLS（`tls`：证书来自文件、用 ACME 为若干名字申请，或在握手中按需申请）。
`client_auth` 要求并校验客户端证书（mTLS）；`proxy_protocol` 从信任的代理读取 PROXY 协议头；`socks` 让
`socks` 协议成为 SOCKS5 与 SOCKS4 正向代理；`mptcp` 在内核支持时以多路径 TCP 监听；`access` 决定该监听
服务哪些客户端地址。

监听与配置的其他部分这样分工：

- 路由用 `listeners` 说出它服务哪些监听；不写的路由服务所有提供 HTTP 的监听。路由不能指向不提供 HTTP
  的监听（只有 `tcp`、`udp` 或 `socks`）；
- 四层服务列出它代理哪些 `tcp` 或 `udp` 监听的连接；没有任何服务列出的 `tcp` 或 `udp` 监听会被拒绝；
- `socks` 协议按该监听的 `socks.clients` 决定谁可以用。

整个配置不写任何监听时，由 [HTTPS by default](../reference/config.md#https-by-default) 提供服务：443 端口
用 ACME 为路由匹配的 host 名取得证书，80 端口跳转到 443（可在 `auto_https` 中关掉跳转）。这样的配置必须
有一条路由匹配公共 CA 能签发的 host 名，否则被拒绝：“at least one listener is needed, or a route that
matches a host name for HTTPS by default”。`tls` 写空（`{}`）的监听会为它所服务路由的 host 名取得证书。

本页只编辑监听本身；每个端口及其提供的功能在[引擎](06-engine.zh-CN.md)菜单中列出。

## 页面与操作

### 监听（Listeners）

菜单**监听**只有一页，URL 是 `#/gw.listeners/listeners`，编辑配置的 `listeners` 成员。列表上方的提示写着：
各端口的功能都在侧边栏的“引擎”菜单中分组列出；本页编辑监听本身。

- **每一行**是一个监听，按配置书写的顺序：序号与名字（`#0 web`）、`address`、`protocols` 中每个协议的
  标记；有 `tls` 的行加一个 `tls` 标记，有 `socks` 的行加一个 `socks` 标记。点开一行就是它的表单。
- **添加监听**新建一行：`name` 为 `web-1`（第一个未被占用的 `web-N`）、`address` 为 `:8080`、
  `protocols` 为 `http/1.1`。每行有**上移**、**下移**与**删除**按钮。删除不再确认，保存后才生效；删掉
  最后一个监听时 `listeners` 成员被移除，此时由 HTTPS by default 接管。
- 表单的字段是 `name`、`address`、`protocols`、`mptcp`、`tls`（含 `certificates`、`acme`、`on_demand`、
  `client_auth`）、`socks`、`access`、`proxy_protocol`。每个字段带说明；可选成员前有复选框，勾选即设置，
  取消即不写。
- 本页与所有编辑配置的页面共用同一份草稿。顶栏显示配置版本（或**有未保存的修改**），旁边是**放弃修改**、
  **检查**、**保存并应用**。**保存并应用**校验草稿并作为新的配置版本应用；**检查**发送同一请求并带上
  `?dry_run=1`：加载并编译配置、试绑新监听的地址，但不应用任何修改，并说出哪些监听会被新增或移除
  （“将新增监听器 web-2（:8081）。”）。
- **界面**之外的 `.conf`、`.yaml`、`.json` 标签以对应格式的文本编辑本页的 `listeners` 成员，标签旁另有
  只针对该成员的**导出**与**导入**按钮。在文本标签中保存时，如果草稿中没有其他未保存的修改，只发送
  `listeners`，问题会定位到文本的行列；否则发送整份草稿。
- 保存或检查发现的问题链接到它所在的监听与字段。页面上的地址是配置写的地址；内核实际绑定的地址在
  [引擎](06-engine.zh-CN.md)菜单中该端口的悬停提示里。

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
| `name` | 必填 | 监听之间唯一的名字，路由与四层服务用它引用这个监听：字母、数字、`_`、`.`、`-`，以字母或数字开头。 |
| `address` | 必填 | 监听的地址与端口，如 `:8080` 或 `127.0.0.1:8080`。 |
| `protocols` | `http/1.1`，有 `tls` 时 `http/1.1` 与 `h2` | 提供的协议：`http/1.1`；`h2c`，明文的 HTTP/2，只用于没有 `tls` 的监听；有 `tls` 时的 `h2`（经 ALPN 选择）与 `h3`（QUIC 上的 HTTP/3，同一端口的 UDP，向 HTTP/1 和 HTTP/2 客户端以 Alt-Svc 宣告）；`tcp` 原始 TCP，其连接由列出该监听的[四层服务](17-l4-proxy.zh-CN.md)转发到上游；`udp` 数据报，只能单独列出；`socks` 是 SOCKS5 与 SOCKS4 正向代理，需要该监听的 `socks`。每个协议只能列一次。 |
| `mptcp` | 关闭 | 内核支持时（Linux 5.6+、`net.mptcp.enabled`）以多路径 TCP 监听该监听的 TCP 连接，不支持时回退到普通 TCP，没有多路径 TCP 的客户端也回退。默认关闭时监听是普通 TCP，这也是 CVE-2026-98432 的漏洞利用公开后加固的结果：该内核 use-after-free 的利用需要目标上有 MPTCP 监听，所以只在已修复的内核上开启。`udp` 监听与 HTTP/3 没有该功能。 |
| `tls` | 无 | 有 `tls` 时该监听的 HTTP 就是 HTTPS。`certificates` 是证书文件（客户端拿到第一张名字与其请求相符的证书）；`acme` 是要为哪些 host 名申请证书；`on_demand` 在握手中现场申请（需要顶层 `acme.on_demand`）；写空的 `tls`（`{}`）为路由的 host 名取得证书。不提供 HTTP 的监听不能写 `tls`：`tcp` 与 `udp` 连同其中的 TLS 原样转发。 |
| `tls.client_auth` | `mode` 为 `require` | 要求并校验客户端证书（mTLS）：`ca_files` 是客户端证书必须链到的 CA 文件。`require` 在握手中拒绝没有有效证书的客户端；`verify_if_given` 允许客户端不送证书，但送来的必须有效，需要证书的路由用 `client_cert` 中间件声明。要求证书的监听无法应答 ACME 的 TLS-ALPN-01 挑战，所以配合 `acme` 时需要 `verify_if_given`，或允许 HTTP-01 挑战。 |
| `socks` | 无（`socks` 协议必需） | 谁可以用这个 SOCKS 代理（`clients`，必填），以及它能连到哪里（`allow`、`deny`、`ports`、`connect_timeout` 默认 10s、`idle_timeout` 默认 5m）：只服务 CONNECT，无认证，拒绝 BIND 与 UDP ASSOCIATE。环回、链路本地（含元数据服务 169.254.169.254）、未指定、多播地址与主机自身的地址一律拒绝，除非 `allow` 写出了落在其中的网段。 |
| `access` | 全部地址 | 该监听服务的客户端地址（`allow`/`deny`，也可用 `allow_files`/`deny_files`），先于全局 `access` 决定；被拒绝的连接一被接受就关闭，HTTP/3 与 `udp` 则丢弃数据报——除非该监听的路由或服务自己设了 `access`，那由它先决定。有 `proxy_protocol` 时地址是头部写出的那个。 |
| `proxy_protocol` | 无 | 从 `trusted` 地址读取 PROXY 协议 v1 或 v2 头，并按头中写出的客户端地址服务这条连接：来自这些地址的连接必须在 10s 内以头部开头，否则关闭；来自其他地址的连接原样服务，不读头部。用于 `tcp` 与 HTTP over TCP；有 `tls` 时头部在 TLS 之前。HTTP/3 与 `udp` 不读取。 |

表中的默认值是字段留空时 fit 补上的值。证书文件与 `client_auth` 的 `ca_files` 会被监视：文件一变，配置就
重新加载；`access` 的 `allow_files` 与 `deny_files` 在应用配置时读取，变化时再应用，每个至多 64 MiB。

### 一个端口上的多个协议

一个端口同时提供 HTTP、`tcp` 与 `socks` 中的多种时，fit 按客户端先送来的是什么区分：

- TLS ClientHello：交给 `server_names` 含其名的服务；没有那样的服务时，有 `tls` 就按 HTTPS 应答，否则交给
  没有 `server_names` 的服务。
- SOCKS 问候：交给 `socks`。
- 明文的 HTTP 请求：交给 HTTP。
- 其余：交给没有 `server_names` 的服务，没有则关闭。

等服务器先开口的客户端 10s 后被关闭，这样的协议需要自己的监听。

### 一个地址，一个服务器

两个监听不能在同一地址和同一传输上监听，监听也不能与配置中其他监听的部分（Admin API、控制台、角色的
链路监听器或 API、隧道转发、流媒体或 WebRTC 端口）冲突；监听也无法绑定别的进程占着的地址。`:443`、
`0.0.0.0:443` 和 `[::]:443` 是同一个地址，它也包含 `127.0.0.1:443`。同一号码的 TCP 与 UDP 端口不冲突，
所以 HTTP 与 HTTP/3 可以共用一个端口。详见[一个地址，一个服务器](../configuration.zh-CN.md#一个地址一个服务器)。

## .conf 配置

```fit
# Plain HTTP/1.1 and HTTP/2 in the clear, with Multipath TCP where the kernel has it.
listener web :8080 http/1.1 h2c {
	mptcp true
}

# HTTPS with HTTP/2 and HTTP/3, certificates from ACME, open to two networks.
listener secure :443 http/1.1 h2 h3 {
	tls {
		acme example.com www.example.com
		# Files must exist for fit validate to read them:
		# certificates {
		# 	cert_file /etc/fit/example.com.pem
		# 	key_file /etc/fit/example.com.key
		# }
		# client_auth {
		# 	ca_files /etc/fit/clients-ca.pem
		# 	mode verify_if_given
		# }
	}
	access {
		allow 10.0.0.0/8 192.168.0.0/16
		deny 10.9.0.0/16
	}
}

# HTTP behind a load balancer that sends the PROXY protocol header.
listener internal 127.0.0.1:8081 {
	proxy_protocol { trusted 10.0.0.0/8 }
}

# A SOCKS5/SOCKS4 forward proxy for the office network, to port 443 only.
listener proxy :1080 socks {
	socks {
		clients 10.0.0.0/8
		allow *.example.com
		deny 169.254.169.254
		ports 443
	}
}

# Raw TCP, proxied byte for byte by the service that lists the listener.
listener postgres :5432 tcp
upstream db 10.0.0.5:5432
service postgres { listeners postgres; upstream db }

route { match { host example.com www.example.com }; listeners secure; handler respond { body "hello over HTTPS\n" } }
route { listeners web internal; handler respond { body "hello from fit\n" } }
```

这份配置通过 `fit validate -c` 校验。`fit validate` 会读取证书与 CA 文件，它们必须存在，所以上面注释掉了。
`internal` 没写协议，因此提供 `http/1.1`。

## 相关文档

- [配置参考：Listeners](../reference/config.md#listeners)（全部字段）与[中文的章节说明](../reference/config.zh-CN.md#各章内容)
- [一个地址，一个服务器](../configuration.zh-CN.md#一个地址一个服务器)
- [.conf 格式](../reference/fit.zh-CN.md)
- [引擎](06-engine.zh-CN.md)：按端口看运行中的功能
- [TLS 证书](18-tls.zh-CN.md)、[正向代理](16-forward-proxy.zh-CN.md)、[四层代理](17-l4-proxy.zh-CN.md)
- 带全部字段的注释模板：[templates/conf/conf.d/10-listeners.conf](../../templates/conf/conf.d/10-listeners.conf)
