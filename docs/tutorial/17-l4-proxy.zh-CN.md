# 四层代理

[English](17-l4-proxy.md) | [简体中文](17-l4-proxy.zh-CN.md)

## 功能

四层代理不看应用层：`services` 把 `tcp` 监听的连接逐字节转给上游，把 `udp` 监听的数据报一对一地转给
上游。一条 `service` 说三件事——哪些监听（`listeners`）、送到哪个上游（`upstream`），以及可选的
`server_names`、`proxy_protocol`、`access`、`idle_timeout` 与 `max_conns`。它不解密任何 TLS。

上游的地址按上游自己的 `load_balancing` 选：`ip_hash` 按客户端的地址；`header`、`cookie` 与 `uri_hash`
对一条连接没有可看的字段，只能依次尝试。被动与主动健康检查、`max_fails` 与 `connect_timeout` 照 HTTP 的
规则用；一次连接失败，换下一个选中的地址再试，最多三个。上游的 `protocol` 与 HTTP 设置用不上。

`server_names` 让同一个端口分出几条服务：只有 TLS 连接、且 ClientHello 里问的名字（SNI）落在某条服务的
`server_names` 里，才归那条服务；`*.example.com` 含 `example.com` 下面一层的那个名字。一个 `tcp` 监听可以
被几条服务列出，连接归名字对得上的那条；对不上归没有 `server_names` 的那条（不是 TLS 或没问名字的连接也
归它），都没有就合上连接。ClientHello 必须在 10s 内到。这些 TLS 连接原样转发：fit 不解密，所以应答它们的证书是
上游那端的事。

`proxy_protocol` 让上游知道客户端是谁：每条到上游的连接先送一个 v1（文本）或 v2（二进制）头，写着客户端
的地址与它连上的地址。只用于 `tcp` 监听。监听端的 `proxy_protocol` 是收头，服务端的是发头，两者可以叠加：
从可信地址收一个头，再给上游发一个头。

服务与路由互不干涉：同一个端口上，HTTP 请求归路由，TCP 连接归服务。服务不看方法、路径与头部，所以路由上
的中间件对四层的连接不起作用；反过来，服务也没有 `match`，它只认监听与上游。

什么时候用服务：后端不是 HTTP——Postgres、DNS、游戏或 IM 的 TCP 端口；或者要把 TLS 原样透传，不在 fit 上
解密（MQTT over TLS、按 SNI 分流到不同的后端）。服务不解析任何协议，所以它对内容一概不懂：不能按路径分流，
不能改写头部，也不能缓存。

## 页面与操作

### 四层服务

"四层代理"模块在菜单里只有一个页面：四层服务。它编辑配置的 `services` 成员。

- 每条服务一行：名字，加上小标签——`@` 后是它接的监听，`→` 后是它的上游，`proxy_protocol` 有设置时是
  一个徽章。
- "添加服务"新建一条服务：`listeners` 取草稿里第一个 `tcp` 或 `udp` 监听，`upstream` 取第一个上游，名字
  取 `service-1`、`service-2`……
- 行点开是它的表单：`name`、`listeners`、`upstream`、`idle_timeout`、`max_conns`、`server_names`、`access`
  与 `proxy_protocol` 逐项编辑。
- 这里编辑的是共享草稿：表单与 `.conf`、`.yaml`、`.json` 标签是同一份草稿，"保存并应用"校验并应用。
- 监听本身在[监听](05-listeners.zh-CN.md)页编辑，上游在[反向代理](15-reverse-proxy.zh-CN.md)页的上游部分
  编辑；这一页只把两者连起来。

### 在引擎页中

[引擎](06-engine.zh-CN.md)页按端口列出正在提供的功能：这个端口下列出"四层服务"，带条数，链回本页。端口行
的小标签写着协议与 `proxy_protocol`；一个端口同时被 HTTP、tcp 与 socks 用到时，页上把用到的几条都列出。

### 页上不能做什么

- 这一页不监听，也不选地址：`listeners` 只是名字，指向别处编辑的监听；把监听的 `protocols` 写成 `tcp`
  或 `udp` 在[监听](05-listeners.zh-CN.md)页做。
- 页上不显示运行中的计数：fit 持有的连接数在[概览](04-overview.zh-CN.md)页，服务本身在
  [概览](04-overview.zh-CN.md)与[引擎](06-engine.zh-CN.md)页看。

## 参数

只列关键参数；完整字段表见[配置参考：Services](../reference/config.md#services)
（[`services[]`](../reference/config.md#config-service)）。

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `name` | 必填 | 服务间唯一的名字。 |
| `listeners` | 必填 | 它接的监听的名字：服务 `tcp` 的监听（单独或与其他协议一起）与 `udp` 监听，每个 `udp` 监听只归一条服务。 |
| `upstream` | 必填 | 连接送去的上游的名字：按其 `load_balancing` 选一个地址，按 `max_fails` 与健康检查轮出，在其 `connect_timeout` 内拨号；失败最多换三个地址。 |
| `server_names` | 无 | 归这条服务的 TLS 连接的 SNI 名字；`*.example.com` 含下面一层的名字。原样转发，不解密。 |
| `proxy_protocol` | 无 | 发给上游的 PROXY 协议头的版本：`v1`（文本）或 `v2`（二进制）。只用于 `tcp` 监听。 |
| `idle_timeout` | 连接 `10m`，`udp` 会话 `1m` | 一条连接或会话两边都没有字节多久后合上。 |
| `max_conns` | `10000` | 同时最多代理多少条连接与会话；超出的连接一被接受就合上，超出的会话的数据报被丢掉。 |
| `access` | 继承 | 这条服务接的客户端地址，先于监听的 `access` 与全局的 `access` 决定：被它拒绝的连接在监听需要时读完 PROXY 头与 ClientHello 后合上，被它拒绝的 `udp` 会话的数据报被丢掉。 |

### 计数与上限

`max_conns` 数的是一条服务同时代理的连接与会话：超出的连接一被接受就合上，超出的会话的数据报被丢掉。
`idle_timeout` 计的是两个方向都没有字节的时间：只要任一侧还在发送，连接就不算空闲。页上不显示这些数，fit 持有的连接数在
[概览](04-overview.zh-CN.md)页看。

### 校验会拒绝的写法

- 没有任何服务列出的 `tcp` 或 `udp` 监听，以及服务列出的不是这两种的监听：服务接的监听，其 `protocols` 要么
  包含 `tcp`，要么只有 `udp`；
- 一个 `udp` 监听被两条服务列出，或被带 `server_names` 或 `proxy_protocol` 的服务列出；
- 同一个 `tcp` 监听上有两条没有 `server_names` 的服务，或同一个名字出现在它的两条服务里；
- `server_names` 里写 IP 地址：ClientHello 里只有主机名；
- 不存在的 `upstream` 或监听。

### 用不上的那些

上游的 `protocol`、头部、HTTP 超时与 `sticky_cookie` 都不参与：一条连接没有可看的 HTTP 字段。用上的只有
选地址的策略、`max_fails`、健康检查与 `connect_timeout`。

### 健康检查与四层

健康检查照 HTTP 的规则用，但它的 `type` 可以是 `tcp`：连上就算通过。对只有 TCP 后端的上游（Postgres、
DNS），`type: tcp` 才是有意义的探针；`http` 探针发 GET，对这样的后端没有意义。被动的 `max_fails` 数的是
拨号失败。

### udp 的会话

一个 `udp` 监听归一条服务。会话就是一个客户端的地址：它送的数据报去同一个上游地址，从一个属于这个会话
自己的 socket 发出，回到那个 socket 的字节再送回给客户端。所以 `idle_timeout` 对会话比对连接短得多，
`max_conns` 数的也是会话，超出的数据报被丢掉而不是合上连接。

### 一个端口，几条服务

一个 `protocols` 里有 `tcp` 的端口，按客户端先送的内容分辨：TLS 的 ClientHello 给 `server_names` 对得上的
服务，对不上时如果监听有 `tls` 就走 HTTPS，否则给没有 `server_names` 的服务；SOCKS 的问候给 socks，明文的
HTTP 请求给 HTTP；其他字节给没有 `server_names` 的服务，没有就合上连接。等服务器先说话的客户端 10s 后被
合上——这样的协议需要自己的监听。

## .conf 配置

三个监听、四个上游、四条服务：带 TCP 健康检查的 Postgres，走 `udp` 的 DNS，以及按 SNI 拆分的一个端口
`:8443`——`mail.example.com` 的 TLS 透传，加一条发 PROXY 头的兜底服务。这是自包含的完整配置，能通过
`fit validate`：

```fit
listener pg :5432 tcp
listener dns :53 udp
listener tls :8443 tcp

upstream db 10.0.0.5:5432 10.0.0.6:5432 {
	health_check {
		type tcp
		interval 10s
	}
}
upstream resolvers 10.0.0.53:53 10.0.0.54:53
upstream mail 10.0.0.25:8443
upstream web 10.0.0.30:8443 10.0.0.31:8443 {
	load_balancing ip_hash
}

service postgres { listeners pg; upstream db; idle_timeout 30m }
service dns { listeners dns; upstream resolvers }
service mail { listeners tls; upstream mail; server_names mail.example.com }
service web { listeners tls; upstream web; proxy_protocol v2; max_conns 20000 }
```

不写任何监听——也没有匹配主机名的路由、HTTPS by default 因此会生成监听——的配置通不过校验：上游自己不听。

在 `:8443` 上，ClientHello 问 `mail.example.com` 的连接原样送到 `10.0.0.25:8443`；其他连接——问别的名字、
没问名字，或根本不是 TLS——都归 `web`，即这个监听上唯一没有 `server_names` 的服务：它按客户端 IP 把客户端固定在
两个地址之一，并给每条连接先发一个 PROXY v2 头。上游必须能接这个头（nginx 的 `listen ... proxy_protocol`、
HAProxy 的 `accept-proxy`）；不认它的服务器（如 Postgres）会把它当成乱码，所以 `postgres` 不发。

写法上，`listener pg :5432 tcp` 是简写，与 `listener { name pg; address :5432; protocols tcp }` 等价；
`upstream db 10.0.0.5:5432 10.0.0.6:5432 { ... }` 把 `addresses` 作为参数；`service` 只把名字作为参数，
其余字段写在块里。

## 相关文档

- [监听](05-listeners.zh-CN.md)：`tcp` 与 `udp` 协议，监听的 `proxy_protocol` 与 `access`。
- [反向代理](15-reverse-proxy.zh-CN.md)：上游的 `load_balancing`、`max_fails` 与健康检查。
- [引擎](06-engine.zh-CN.md)：端口上把服务列在何处。
- [配置参考：Services](../reference/config.md#services)：`services` 的全部字段。
- [配置参考：Upstreams](../reference/config.md#upstreams)：上游的全部字段。
- [配置参考：Upstreams 的健康检查](../reference/config.md#config-healthcheck)：`type: tcp` 的探针。
- [概览](04-overview.zh-CN.md)：连接与四层服务的计数。
- [模块文档 services](../../schema/doc/services.md)：这一成员的说明与示例。
- [完整配置模板](../../templates/conf/conf.d/40-services.conf)：每个字段带注释。
