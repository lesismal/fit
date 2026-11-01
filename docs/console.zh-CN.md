# 控制台与 store

[English](console.md) | [简体中文](console.zh-CN.md)

配置示例，每个都有 .conf、YAML 和 JSON 三种格式：[console](../examples/console/fit.conf), [admin](../examples/admin/fit.conf)。

fit 可以把配置加密保存在它自己格式的文件里，即 **store**，并提供 **控制台**：用户名和密码登录后的网页，从中读取和修改
配置的每一部分。两者是 `admin` 中与 [Admin API](reference/config.md#admin-api) 并列的 `admin.store` 和
`admin.console`；各自都可以单独使用，合起来时，在控制台中修改的配置立即运行，封存进 store，下次启动也从它开始。设计见
[决定 027](decisions/027-console-and-store.zh-CN.md)。

- [两个文件](#两个文件)
- [store](#store)
- [fit store export 与 import](#fit-store-export-与-import)
- [控制台](#控制台)
- [用户与 fit hash-password](#用户与-fit-hash-password)
- [登录与会话](#登录与会话)
- [页面](#页面)
- [上限与超时](#上限与超时)
- [角色的页面](#角色的页面)

## 两个文件

文件无法保存打开它自身的密码，所以有 store 的配置分在两个文件里：

- **引导文件**，即 `fit run -c` 指定的文件，明文，可以是 fit 的任一格式。它包含 `admin`：Admin API、
  `admin.store`（store 在哪里、它的密码）和 `admin.console`（控制台监听在哪里、谁可以登录）；
- **store**，用密码封存。它包含配置的其余部分，即除 `admin` 外的一切：fit 运行所依据的 **基础配置**，provider 尚未合并
  进来。

```json
{
  "admin": {
    "address": "127.0.0.1:2019",
    "store": {"path": "/var/lib/fit/fit.store", "password_env": "FIT_STORE_PASSWORD"},
    "console": {
      "address": "127.0.0.1:2020",
      "users": [{"username": "admin", "password_hash": "$2a$10$sKGBAOOCI4p4WWJcVCuAH.ivT8pNy2DFbs6pd8xmtNEZ/5L1SeLvm"}]
    }
  },
  "listeners": [{"name": "web", "address": ":8080"}],
  "routes": [{"match": {"host": ["example.com"]}, "handler": {"module": "respond", "params": {"body": "hello"}}}]
}
```

fit 第一次以这个文件启动时还没有 store：`listeners` 和 `routes` 是它的初始内容，store 立即写入，fit 输出
`the config of fit.json is kept in the store /var/lib/fit/fit.store from now on`。此后以 store 为准：引导文件中
`admin` 之外的设置被忽略，并给出警告，列出被忽略的成员，引导文件只读取 `admin`。要重新从文件开始，删除 store。

`admin` 的设置，包括 store 和控制台的，在 fit 启动时读取；修改后在 fit 重启时生效。

## store

| 字段 | 默认值 | |
| --- | --- | --- |
| `path` | | 必填。store 文件。 |
| `password` | | 密码，写在引导文件中。 |
| `password_file` | | 存放密码的文件，去掉首尾空白。 |
| `password_env` | | 存放密码的环境变量。 |

`password`、`password_file` 和 `password_env` 恰好设置一个；文件为空、环境变量未设置或为空时 fit 不会启动。写在引导文件中的
密码保护的是被复制走的 store（备份、磁盘镜像），而不是同一台机器上读取的 store；另外两种方式让密码不出现在文件中，正如
`token_file` 让 Admin API 的令牌不出现在文件中。

**保存什么。** 每个应用的配置，无论来自控制台、Admin API（`PUT`、`PATCH`、`DELETE`、`POST /load`）、回滚还是角色的
页面，运行后都写入 store。写入失败的配置仍然运行，其结果带有警告 `the config runs, but was not saved to the store`。
SIGHUP 重新读取 store 并应用，例如在 `fit store import` 之后；有 store 时拒绝监视配置文件的 `-watch`。

**`admin` 从不进入 store，也从不显示。** 引导文件的 `admin` 在每个配置加载时、provider 合并之后放入，但不放入基础配置，
因此 `GET /config`、`/versions` 和 `/config/effective` 中没有密码、哈希或密钥路径。有 store 时，带 `admin` 的配置被拒绝：
`admin` 只来自引导文件。

**格式** 是 fit 自己的 `fitstore` 第 1 版：59 字节的头部（魔数 `FITSTORE`、版本、Argon2id 参数、salt 和 nonce），之后是
用 XChaCha20-Poly1305 封存的 JSON 配置，头部作为附加数据。密钥由密码经 Argon2id（time 3、64 MiB、4 线程）派生，只在 fit
启动时派生一次；每次写入使用新的 nonce，所以写入不再派生密钥。写入先写到 store 旁的临时文件，权限 0600，同步后重命名覆盖
store，因此崩溃只会留下旧的或新的 store，不会留下半个。

**打不开的 store**，无论是密码错误还是某个字节被改，都会让 fit 无法启动，且不会被覆盖。丢失密码就丢失了 store：用
`fit store export` 做一份副本，存放在安全的地方。

## fit store export 与 import

```text
fit store export -c <bootstrap file>
fit store import -c <bootstrap file> <config file or directory>...
```

两者都从引导文件的 `admin.store` 读取 store 及其密码；不给 `-c` 时为 `fit.json`，与 `fit run` 相同。

- **`export`** 以 JSON 输出 store 中的配置，不含 `admin`。store 还没写入时报错。
- **`import`** 读取给出的文件，可以是 fit 的任一格式，个数与 `fit run` 接受的相同，像 `fit validate` 一样检查它们组成的
  配置（放入引导文件的 `admin`），再封存为 store，替换原有内容，使用新的 salt。设置了 `admin` 的配置被拒绝：`admin` 留在
  引导文件中。旧 store 不必能打开，所以修改密码也靠 import。

运行中的 fit 在重新读取 store 之前看不到 import 的结果：向它发送 SIGHUP。

修改密码：

```sh
fit store export -c /etc/fit/fit.json > /root/fit-config.json
# 修改 admin.store 的 password、password_file 或 password_env，或它们指向的内容
fit store import -c /etc/fit/fit.json /root/fit-config.json
rm /root/fit-config.json
kill -HUP "$(pidof fit)"    # 或重启 fit
```

## 控制台

| 字段 | 默认值 | |
| --- | --- | --- |
| `address` | `127.0.0.1:2020` | 控制台监听的主机和端口。 |
| `cert_file`、`key_file` | | 控制台证书（及中间证书）和私钥的 PEM 文件；设置后控制台以 TLS 1.2 或更高版本提供服务。两者同时设置或都不设置。 |
| `users` | | 必填。可以登录的用户：至少一个，每个有 `username` 和 `password_hash`。 |
| `session_timeout` | `30m` | 没有请求时登录保持多久。 |
| `session_lifetime` | `12h` | 无论多活跃，登录最长保持多久。 |
| `max_sessions` | 64 | 同时保留的登录数上限；再多一个时结束最早的。 |
| `max_conns` | 32 | 同时打开的连接数上限；超出的连接在接受后立即关闭。 |

**非环回地址需要 `cert_file` 和 `key_file`**，使密码从不以明文穿过网络；`fit validate` 拒绝没有它们的配置。在环回地址上
TLS 可选，例如经 SSH 隧道访问的控制台。

控制台是独立的服务器，与 Admin API 一样运行在 net/http 和它自己的 goroutine 上，而不是监听器的 fib 引擎上，所以弄坏监听器
的配置不会把你挡在修复它的门外。它在进程内调用 Admin API 的处理函数，外加自己的登录检查，因此两者经同一路径、以同样的检查
应用配置；Admin API 保留自己的地址、`token_file` 和规则，也同时监听。控制台不需要 store：没有 store 时，它像 Admin API
一样在内存中修改运行中的配置，重启后从配置文件重新开始。

## 用户与 fit hash-password

用户是一个名字和一个 bcrypt 哈希；密码从不以明文写入。`fit hash-password` 从标准输入的第一行读取密码，从不从参数读取
（机器上的其他用户能看到参数），并输出它的哈希：

```sh
$ fit hash-password
correct horse battery staple
$2a$10$...
```

`-cost` 设置 bcrypt 的代价（默认 10；每加一，检查耗时翻倍）。密码最多 72 字节（bcrypt 的上限），且不能为空。同样的哈希也
用于 `basic_auth`。`fit validate` 拒绝不是 bcrypt 哈希的 `password_hash`、空用户名和重复列出的用户名。

## 登录与会话

- **检查密码。** 不存在的用户名用一个虚设的哈希检查，使耗时无法区分用户名；同时最多进行两次检查，其余等待。
- **锁定。** 一个客户端地址在 15 分钟内失败 5 次后被拒绝 15 分钟，返回 `429` 和 `Retry-After`。地址表最多 4,096 个地址。
- **会话。** 登录得到一个会话：cookie `fit_console` 中的 32 个随机字节，`HttpOnly`、`SameSite=Strict`，TLS 下还有
  `Secure`。控制台只保存它的 SHA-256，并在 `session_timeout` 内没有请求、总时长超过 `session_lifetime`、登出，或超过
  `max_sessions` 且它是最早的会话时结束它。会话在内存中：重启后所有人都需重新登录。
- **其他网页。** 除读取外的每个请求都在 `X-Fit-Csrf` 中携带会话的 CSRF 令牌，否则返回 `403`；`Origin` 不是控制台自身的
  请求返回 `403`。页面带有只允许自身文件、`frame-ancestors 'none'` 的 Content-Security-Policy，以及
  `X-Frame-Options: DENY`、`Referrer-Policy: no-referrer`，TLS 下还有 `Strict-Transport-Security`；API 的响应带
  `Cache-Control: no-store`。

`admin` 日志器记录谁从哪里登录、每次登录失败、每次登出，以及每个用户做的每次修改及其应用的配置版本。

## 页面

登录后有三个标签页：

- **Config**：配置每一部分的表单，由嵌入二进制的 fit JSON Schema 生成：每个对象、列表和模块按 schema 定类型，带有每个
  字段的说明。fit 新增的字段无需修改控制台即可编辑。表单中没有 `admin`。
- **JSON**：整个配置的文本，直接编辑。
- **Versions**：应用过的配置（`admin.history` 个）、每个的来源、每个的文本，以及回滚到它的按钮。

**Save** 带 `If-Match` 发送整个配置；结果给出应用后的版本号及任何警告。有错误的配置被拒绝，错误显示在各自的路径处。若页面
读取后配置被别处修改（`412`），页面会提示：放弃修改以重新读取，或先从 JSON 标签页复制修改。

供脚本使用的控制台自身端点有 `POST /api/login`（JSON 请求体 `{"username", "password"}`，最多 4 KiB，返回用户名和 CSRF
令牌）、`POST /api/logout`、`GET /api/session` 和 `GET /api/schema`；`/api/admin/` 下提供 Admin API 的端点，用会话代替
令牌。

## 上限与超时

| | |
| --- | --- |
| 连接 | `max_conns`，32 |
| 会话 | `max_sessions`，64，超出时结束最早的 |
| 请求头 | 64 KiB，10s 内读完 |
| 请求体 | `admin.max_config_bytes`，4 MiB，1m 内读完；登录请求体 4 KiB |
| 写响应 | 1m |
| 空闲连接 | 1m |
| 同时进行的密码检查 | 2 |
| 登录失败 | 每个地址 15 分钟内 5 次，之后拒绝 15 分钟；保留 4,096 个地址 |

## 角色的页面

[llm](llm.zh-CN.md) 和 [tunnel](tunnel.zh-CN.md) 的角色有自己的页面，各由角色的 `console` 设置：字段与
`admin.console` 相同，但 `address` 必填，并运行在监听器的 fib 引擎上。它们的用户、登录、锁定、会话和 TLS 规则与控制台的
相同，只是各有各的表。角色页面所做的修改写回配置中该角色的成员，与控制台的修改经同一路径应用，所以会出现在控制台的
Versions 标签页中，有 store 时也封存进 store。控制台把这些成员作为整体编辑：角色的页面用来操作角色，控制台用来管理配置。
