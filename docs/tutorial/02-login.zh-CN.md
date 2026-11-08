# 登录

[English](02-login.md) | [简体中文](02-login.zh-CN.md)

## 功能

登录是控制台的第一页：一个名字加一个 bcrypt 哈希，哈希写在引导文件的 `admin.console.users` 里，密码从不
以明文写入。登录成功得到一个会话，此后的每个请求都凭它，而不是凭密码。

控制台对登录的保护是：失败的登录按地址锁定、按用户减速；会话只保存其 SHA-256；每个非读取的请求都要带
CSRF 令牌；页面只允许自身的文件。检查密码要花 bcrypt 的几十毫秒，所以它从不发生在 fib 的 worker 上，而
在控制台自己的有界池上，登录因此不会拖住已登录用户的请求。

这一章讲登录页与 `POST /api/login`、会话、默认密码的流程、锁定与减速、CSRF 与响应头，以及 `admin` 日志。
改密码在[账户](13-account.zh-CN.md)一章。

## 页面与操作

### 登录页

打开控制台的地址，未登录时看到的就是登录页：用户名、密码与**登录**。未登录时它是唯一的页面：其他请求
得到 `401`（`log in first`）。它把 JSON 请求体发到
`POST /api/login`（`{"username", "password"}`，最多 4 KiB，必须是 `application/json`），成功时得到
`{"user", "csrf"}` 和会话 cookie；页面保存这两者，此后除 GET 与 HEAD 外的每个请求都在 `X-Fit-Csrf` 中
带上令牌。登录之后，`GET /api/workbench` 列出这台 fit 提供的模块，菜单才画得出页面。

失败的登录显示“登录失败”：`401` 说明用户名或密码不对，`429` 说明该地址或该用户还在等待，应答带
`{"error", "retry_after"}`，即需等待的秒数。给脚本用的也是同一个端点：程序自己保存 cookie，并在每个非
读取的请求的 `X-Fit-Csrf` 中发送 `csrf`；`GET /api/session` 以同一个会话应答 `{"user", "csrf"}`，未登录
时 `401`；`POST /api/logout` 结束会话。

### 修改默认密码页

以 `fit run -admin` 的默认密码登录时，会话是临时的：它只能打开**修改默认密码**这一页，其他接口一律 `403`
及 `"must_change_password": true`，Admin API 对所有请求 `403`。这一页外观同登录页，显示当前（默认）密码、
新密码与确认，以及默认密码失效前的剩余时间；新密码不能与当前密码相同，最多 72 字节。改过之后工作台才
开始可用。超过 `-timeout` 后该密码无法再登录（`401`，`"password_expired": true`），只能以 `-admin` 重启
fit 再及时修改。

### 会话

登录得到一个会话：cookie `fit_console_<端口>` 中的 32 个随机字节（带上控制台的端口，同一主机上几个 fit
的控制台登录互不影响），`HttpOnly`、`SameSite=Strict`，TLS 下还有 `Secure`。控制台只保存它的 SHA-256，
并在下列情况下结束它：`session_timeout` 内没有请求；总时长超过 `session_lifetime`；登出；或超过
`max_sessions` 且它是最早的会话。会话在内存中：重启后所有人都需重新登录。发出带有效会话的请求之前（登录
成功也算），连接在打开 5s 后关闭；之后空闲 2m，直到登出，或某个请求发现会话已结束。

改密码会结束该用户的其他会话，调用者自己的保留（见[账户](13-account.zh-CN.md)）。

### 锁定与减速

- **地址锁定。** 一个客户端地址在 15 分钟内失败 5 次后被拒绝 15 分钟（`429` 与 `Retry-After`）；在安静满
  15 分钟之前，每再被锁定一次，锁定时间加倍，最长一小时。IPv6 地址按其 /64 计数。同一地址同时只检查一次，
  同时发出的猜测绕不过计数；其间再发的得到 `429`。地址表最多 4,096 个地址，填满它不会提前结束锁定；表被
  锁定的地址占满时，新地址被拒绝，直到最早的锁定结束。
- **用户减速。** 一个用户的密码（无论来自哪些地址）连续失败 10 次后，其检查被拒绝 2 秒，此后每再失败一次
  等待时间加倍，最长 5 分钟。等待结束后正确的密码可以登录并结束计数，15 分钟内没有失败也会结束计数。
  不存在的用户名共用一个计数。这样从许多地址猜测也只能每 5 分钟猜一次，而每次猜测最多让该用户 5 分钟
  无法登录。
- **虚设哈希。** 不存在的用户名用一个虚设的哈希检查，使耗时无法区分用户名；同时最多进行两次检查，其余
  等待。登录在自己的池上检查（运行 1 个，等待 16 个，超出时 `503`）。修改密码时当前密码错误也计入锁定
  与减速。

### CSRF、Origin 与响应头

除读取外的每个请求都要在 `X-Fit-Csrf` 中携带会话的 CSRF 令牌，否则 `403`；`Origin` 不是控制台自身的请求
也返回 `403`。页面带有只允许自身文件、`frame-ancestors 'none'` 的 Content-Security-Policy，以及
`X-Frame-Options: DENY`、`Referrer-Policy: no-referrer`，TLS 下还有 `Strict-Transport-Security`；API 的
响应带 `Cache-Control: no-store`。控制台的 Content-Security-Policy 只允许它的页面访问提供 `vod` 与 `hls`
路由的监听。

### admin 日志

`admin` 日志器记录谁从哪里登录、每次登录失败、每次登出，以及每个用户做的每次修改及其应用的配置版本。它
与 fit 自身的日志一起写在 `log` 所设置的输出中。

## 参数

`admin.console` 的字段（完整字段表见
[配置参考](../reference/config.zh-CN.md)的 [Admin API](../reference/config.md#admin-api) 章）：

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `address` | `127.0.0.1:2020` | 控制台监听的主机和端口。非环回地址需要 `cert_file` 与 `key_file`。 |
| `cert_file`、`key_file` | | 控制台的证书与私钥的 PEM 文件；设置后以 TLS 1.2 或更高版本提供服务，两者同时设置或都不设置。 |
| `users` | | 必填。可以登录的用户：至少一个，每个有 `username` 和 `password_hash`。 |
| `session_timeout` | `30m` | 没有请求时登录保持多久。 |
| `session_lifetime` | `12h` | 无论多活跃，登录最长保持多久。 |
| `max_sessions` | 64 | 同时保留的登录数上限；再多一个时结束最早的。 |
| `max_conns` | 32 | 同时打开的连接数上限，其中四分之一留给已登录的连接。 |
| `access` | | 控制台服务的客户端地址，先于全局 `access` 决定，随每次应用的配置生效，无需重启。 |

登录之外，控制台还有几项固定的上限：请求头 16 KiB、10s 内读完；请求体 `admin.max_config_bytes` 加
64 KiB、30s 内读完；写响应 30s；发出带有效会话的请求之前（登录成功也算），连接打开 5s 后关闭，之后空闲
2m；同时进行的密码检查 2 次；Admin API 调用与角色方法 4 个运行、64 个等待，每个最多等待 30s，超出时
`503`。见[上限与超时](../console.zh-CN.md#上限与超时)。

## .conf 配置

引导文件中的登录部分，加一个最小的监听和路由：

```fit
# 引导文件：admin 只在 fit 启动时读取，改它要重启。
admin {
	store {
		path /var/lib/fit/fit.store
		password_file /etc/fit/store-password
	}

	console {
		address 127.0.0.1:2020

		users {
			username admin
			password_hash "$2a$10$sKGBAOOCI4p4WWJcVCuAH.ivT8pNy2DFbs6pd8xmtNEZ/5L1SeLvm"
		}
		users {
			username ops
			password_hash "$2a$10$R3sQ0a3nB0eS0dC1kZmV2uLqXlN5pT7yH8gD6fC.bA1iJ2kL3mN4o"
		}

		session_timeout 30m
		session_lifetime 12h
		max_sessions 64
		max_conns 32
	}
}

listener web {
	address :8080
	protocols http/1.1 h2c
}

route hello {
	match {
		path /hello
	}
	handler respond {
		body "hello from fit\n"
	}
}
```

## 相关文档

- [部署](01-deployment.zh-CN.md)：上一章，引导文件与 store。
- [账户](13-account.zh-CN.md)：登录后改密码。
- [功能导航栏](03-navigation.zh-CN.md)：登录之后的页面。
- [控制台与 store](../console.zh-CN.md)：[登录与会话](../console.zh-CN.md#登录与会话)、
  [用户与 fit hash-password](../console.zh-CN.md#用户与-fit-hash-password)、
  [上限与超时](../console.zh-CN.md#上限与超时)。
- [决定 027](../decisions/027-console-and-store.zh-CN.md)：控制台与 store 的设计。
