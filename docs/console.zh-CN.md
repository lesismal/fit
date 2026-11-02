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
- [workbench 的模块](#workbench-的模块)

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

控制台提供 **workbench（工作台）**：一个页面，左侧是菜单树（网关、隧道、LLM、系统四组，组下是模块，模块下是页面），右侧是
所选的页面。其中的配置模块编辑配置；同一个 fit 中运行的每个 [LLM](llm.zh-CN.md) 与 [tunnel](tunnel.zh-CN.md) 角色也作为
各自的模块显示在旁边，带有该角色自己控制台上的全部页面，只需登录一次。

控制台是监听器的 fib 引擎上它自己的一个绑定，与角色的页面一样，而不是配置中的监听器：配置中的监听器绑定失败时，控制台仍可
访问，以便修复。它的请求在 fib 的 worker 上处理，其中没有任何阻塞：密码检查和每次 Admin API 调用都在控制台自己的有界池上
进行。它在进程内调用 Admin API 的处理函数，外加自己的登录检查，因此两者经同一路径、以同样的检查应用配置；Admin API 保留自己
的地址、`token_file` 和规则，也同时监听。控制台不需要 store：没有 store 时，它像 Admin API 一样在内存中修改运行中的配置，
重启后从配置文件重新开始。

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
- **会话。** 登录得到一个会话：cookie `fit_console_<端口>` 中的 32 个随机字节（带上控制台的端口，同一主机上
  几个 fit 的控制台登录互不影响），`HttpOnly`、`SameSite=Strict`，TLS 下还有
  `Secure`。控制台只保存它的 SHA-256，并在 `session_timeout` 内没有请求、总时长超过 `session_lifetime`、登出，或超过
  `max_sessions` 且它是最早的会话时结束它。会话在内存中：重启后所有人都需重新登录。
- **其他网页。** 除读取外的每个请求都在 `X-Fit-Csrf` 中携带会话的 CSRF 令牌，否则返回 `403`；`Origin` 不是控制台自身的
  请求返回 `403`。页面带有只允许自身文件、`frame-ancestors 'none'` 的 Content-Security-Policy，以及
  `X-Frame-Options: DENY`、`Referrer-Policy: no-referrer`，TLS 下还有 `Strict-Transport-Security`；API 的响应带
  `Cache-Control: no-store`。

`admin` 日志器记录谁从哪里登录、每次登录失败、每次登出，以及每个用户做的每次修改及其应用的配置版本。

## 页面

登录后即为 workbench。菜单可以筛选、收起为图标，组与模块可以展开和折叠；右上角选择主题（浅色、深色及另外三种）与语言（英文或
中文）。每个页面有自己的 URL：`#/<模块>/<页面>`。

- **网关**：运行中配置的概览（状态、路由、四层服务、上游及其健康状况、证书、版本），以及组成网关的各部分配置，各占一页：
  监听器、反向代理的路由与上游、正向代理的路由、四层服务、TLS 与证书。
- **隧道**与 **LLM**：每个运行中的角色一个模块，如 `tunnel.hub`、`llm.node`，带有该角色的页面。
- **系统**：其余各部分配置；整个配置的 JSON；以及应用过的版本（`admin.history` 个）、每个的来源、每个的文本和回滚到它的按钮。

配置的页面是由嵌入二进制的 fit JSON Schema 生成的表单：每个对象、列表和模块按 schema 定类型，带有每个字段的说明。fit 新增的
字段无需修改控制台即可编辑。表单中没有 `admin`。所有页面编辑同一份配置草稿：**保存并应用** 带 `If-Match` 发送整个配置；结果
给出应用后的版本号及任何警告。有错误的配置被拒绝，错误显示在各自的路径处。若页面读取后配置被别处修改（`412`），页面会提示：
放弃修改以重新读取，或先从 JSON 页面复制修改。

供脚本使用的控制台自身端点有 `POST /api/login`（JSON 请求体 `{"username", "password"}`，返回用户名和 CSRF 令牌）、
`POST /api/logout`、`GET /api/session` 和 `GET /api/workbench`（用户名与所提供模块的 ID）。每个模块的端点在
`/api/m/<模块>/` 下：配置模块 `admin` 提供 `GET /api/m/admin/schema`，并在 `/api/m/admin/` 下提供 Admin API 的端点，用会话
代替令牌，如 `GET /api/m/admin/config`；角色的模块提供其页面的 API，如 `POST /api/m/llm.hub/rpc/hub/nodes`。

## 上限与超时

| | |
| --- | --- |
| 连接 | `max_conns`，32 |
| 会话 | `max_sessions`，64，超出时结束最早的 |
| 请求头 | 16 KiB，10s 内读完 |
| 请求体 | `admin.max_config_bytes` 加 64 KiB，默认 4 MiB 加 64 KiB，30s 内读完 |
| 写响应 | 30s；角色的流改为以每次等待浏览器的时间为界，30s |
| 空闲连接 | 2m |
| 同时进行的密码检查 | 2 |
| 同时进行的 Admin API 调用与角色方法 | 4 个运行、64 个等待，每个最多等待 30s；超出时 `503` |
| 登录失败 | 每个地址 15 分钟内 5 次，之后拒绝 15 分钟；保留 4,096 个地址 |

## 角色的页面

[llm](llm.zh-CN.md) 和 [tunnel](tunnel.zh-CN.md) 的角色也可以有自己的页面，各由角色的 `console` 设置：字段与
`admin.console` 相同，但 `address` 必填，并运行在监听器的 fib 引擎上，供没有 `admin.console` 的机器上的角色使用。每个都提供
同样的 workbench，菜单中只有该角色的模块。它们的用户、登录、锁定、会话和 TLS 规则与控制台的相同，只是各有各的表。角色页面
所做的修改（无论在角色自己的控制台上，还是作为 `admin.console` 的 workbench 中的模块）写回配置中该角色的成员，与控制台的修改
经同一路径应用，所以会出现在“版本”页面中，有 store 时也封存进 store。配置本身的页面把这些成员作为整体编辑：角色的页面用来
操作角色，配置的页面用来管理配置。

## workbench 的模块

页面是 `app/workbench/web` 中的纯 HTML、CSS 和 JavaScript，嵌入二进制、从内存提供，不从别处获取任何东西。`core/` 中是各模块
共用的库（`ui.js`、`ui.css`）与 workbench 本身（`workbench.js`）；每个模块是一个脚本，用 `WB.module` 注册自己，写明它调用
哪个服务端模块的 API、放在哪个组下以及它的页面。提供页面的控制台提供某模块的服务端模块时，该模块才显示。
