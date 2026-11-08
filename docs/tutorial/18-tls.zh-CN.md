# TLS 证书

[English](18-tls.md) | [简体中文](18-tls.zh-CN.md)

## 功能

TLS 模块有三个页面：**证书**显示各监听当前提供的证书（文件证书与 ACME 证书），只读；**自动 HTTPS**与
**ACME**编辑配置的 `auto_https` 与 `acme` 成员。监听自身的 `tls`（证书文件、`tls.acme` 的名字、按需申请、
客户端证书）在[监听](05-listeners.zh-CN.md)页设置，这一页只把它当前的结果列出来。

自动 HTTPS 是默认行为：一个不写监听的配置在 443 上以 HTTPS 提供服务，证书按 `acme` 的规则为路由匹配的
主机名申请，并在 80 上以 HTTP 提供服务——那个监听只用来重定向到 HTTPS 和应答 ACME 的 HTTP-01 挑战。写了
监听但 `tls` 是空的（`{}`），它得到它所服务的路由的主机名的证书。没有公共 CA 会认证的名字（`localhost`、
IP 地址、`*.internal`、通配符）得不到证书。

ACME 说证书从哪来、怎么续：CA 的目录（默认 Let's Encrypt）、账户邮箱、存储（一个目录，或 Redis、etcd、
Consul 的 `shared_storage`，供多台机器共享）、应答的挑战（`http-01` 与 `tls-alpn-01`），以及 `on_demand`——
握手期间为一个名字申请证书之前，先问一个服务这个名字可不可以。`acme` 的设置只在 fit 启动时读取，改动在
重启后生效；`tls.acme` 里的名字随每次重载生效。

## 页面与操作

### 证书（`#/gw.tls/certs`）

只读的证书表，每 30 秒刷新一次。列：监听、名字、签发者、生效时间、过期时间与剩余天数（14 天内标黄、已
过期标红）、序列号。页顶的提示说：监听的 `tls` 在监听页设置，ACME 与自动 HTTPS 各有自己的页面。没有监听
提供证书时，页上说"没有监听在提供证书"。

这一页不编辑配置：它读的是运行中的状态（Admin API 的 `GET /status`）。证书来自监听的 `tls.certificates`、
`tls.acme` 与 `tls.on_demand`，或来自自动 HTTPS。

### 自动 HTTPS（`#/gw.tls/auto_https`）

编辑 `auto_https` 成员：表单加 `.conf`、`.yaml`、`.json` 标签，"保存并应用"校验并应用。四个字段——
`disable`、`disable_redirects`、`http_port`、`https_port`——都在[参数](#参数)里。这一页不生成监听：它改的是
规则，监听由 fit 按规则生成，在引擎页上以"自动 HTTPS"列出，链回本页。

### ACME（`#/gw.tls/acme`）

编辑 `acme` 成员：CA 的目录、邮箱、`ca_file`、`storage` 或 `shared_storage`（`redis`、`etcd`、`consul` 三选
一）、`challenges`、`on_demand`、`max_concurrent` 与两个超时。这一页改的设置要重启才生效；页上改的是配置，
运行中的申请与续期按启动时读到的规则继续。

## 参数

`auto_https`：

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `disable` | 开 | 关闭默认 HTTPS：不为无监听的配置生成监听，空的 `tls` 也不得证书。 |
| `disable_redirects` | 关 | 不建明文的重定向监听；HTTP-01 无人应答，证书走 TLS-ALPN-01。 |
| `http_port` | `80` | 重定向监听（及 HTTP-01）的端口。 |
| `https_port` | `443` | HTTPS 监听的端口，也是重定向所写的端口。 |

`acme`（关键项）：

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `directory` | Let's Encrypt | CA 的 ACME 目录 URL；`acme-staging-v02` 是测试用的 staging CA。 |
| `email` | 无 | CA 可写信通知证书的账户邮箱。 |
| `ca_file` | 系统根 | 校验 CA 目录证书用的 PEM 文件，用于自建的 CA。 |
| `storage` | `~/.local/share/fit` | 账户、密钥与证书的存放目录，多个 fit 实例可共享。 |
| `shared_storage` | 无 | 改用 Redis、etcd 或 Consul 存放（与 `storage` 互斥）：一个实例持锁申请，其余从那里取；每个挑战的应答也存那里，谁被 CA 访问到谁应答。 |
| `challenges` | `http-01`, `tls-alpn-01` | 应答的挑战与尝试顺序：HTTP-01 由任何明文监听在 80 上答，TLS-ALPN-01 由列出这个名字的 TLS 监听在 443 上答。 |
| `on_demand` | 无 | 为 `tls.on_demand` 的监听在握手中申请证书：先问 `ask` 指定的 URL 这个名字可不可以（2xx 才算可以）。 |
| `max_concurrent` | `2` | 同时申请的证书数，其余排队。 |
| `timeout` | `10m` | 单个证书申请（含挑战）的最长时间，超时后放弃、稍后再试。 |
| `request_timeout` | `30s` | 每个到 CA 的请求的最长时间。 |

完整字段表见[配置参考：HTTPS by default](../reference/config.md#https-by-default)与
[配置参考：ACME](../reference/config.md#acme)。

## .conf 配置

一个 HTTPS 监听、`tls.acme` 的名字、`auto_https` 与顶层 `acme`。这是自包含的完整配置，能通过
`fit validate`：

```fit
auto_https { http_port 80; https_port 443 }

acme {
	email ops@example.com
	storage /var/lib/fit
	challenges http-01 tls-alpn-01
	on_demand { ask http://127.0.0.1:5555/allowed }
}

listener https :443 {
	tls {
		acme example.com www.example.com
		on_demand true
	}
}

route { handler respond { body hello } }
```

两点要注意：`tls.on_demand` 需要顶层 `acme.on_demand` 的 `ask`，少了它校验报错；`tls.certificates` 里的
`cert_file` 与 `key_file` 在校验时会被读取，文件不存在就报错，所以示例里只写 `acme` 的名字。多台机器共享
证书时改用 `shared_storage`（与 `storage` 互斥），并把 `ask` 与 `email` 留着。

## 相关文档

- [监听](05-listeners.zh-CN.md)：监听的 `tls`、`client_auth` 与按需证书。
- [引擎](06-engine.zh-CN.md)：端口下把证书、ACME 与自动 HTTPS 列在何处。
- [概览](04-overview.zh-CN.md)：14 天内到期的证书与证书表。
- [一致性检查](10-consistency-check.zh-CN.md)：`acme` 中的名字没有证书覆盖属于 drift。
- [配置参考：HTTPS by default](../reference/config.md#https-by-default)：`auto_https` 的全部字段。
- [配置参考：ACME](../reference/config.md#acme)：`acme` 的全部字段。
- [模块文档 auto_https](../../schema/doc/auto_https.md)与[acme](../../schema/doc/acme.md)。
- [完整配置模板](../../templates/conf/conf.d/20-tls.conf)：每个字段带注释。
