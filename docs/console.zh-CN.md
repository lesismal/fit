# 控制台与 store

[English](console.md) | [简体中文](console.zh-CN.md)

配置示例（.conf 格式，在 [fit-examples](https://github.com/lesismal/fit-examples) 中）：[console](https://github.com/lesismal/fit-examples/blob/main/console/fit.conf), [admin](https://github.com/lesismal/fit-examples/blob/main/admin/fit.conf)。

fit 可以把配置加密保存在它自己格式的文件里，即 **store**，并提供 **控制台**：用户名和密码登录后的网页，从中读取和修改
配置的每一部分。两者是 `admin` 中与 [Admin API](reference/config.md#admin-api) 并列的 `admin.store` 和
`admin.console`；各自都可以单独使用，合起来时，在控制台中修改的配置立即运行，封存进 store，下次启动也从它开始。设计见
[决定 027](decisions/027-console-and-store.zh-CN.md)。

- [两个文件](#两个文件)
- [store](#store)
- [fit store export 与 import](#fit-store-export-与-import)
- [fit run -admin](#fit-run--admin)
- [配置的导入与导出](#配置的导入与导出)
- [控制台](#控制台)
- [用户与 fit hash-password](#用户与-fit-hash-password)
- [登录与会话](#登录与会话)
- [页面](#页面)
- [检查运行中的配置](#检查运行中的配置)
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
`the config of fit.json is kept in the store /var/lib/fit/fit.store from now on`。此后每次启动时把 store 合并到引导文件之上，
store 的值优先：对象逐个成员合并，store 中的列表或值整体替换文件中的（因此运行的是 store 的 `listeners` 和
`routes`）。只有文件设置的成员会保留，合并后的配置写回 store，并输出 `the store ... merged onto ...`。要重新从文件开始，
删除 store。*（2026-11-04 修改：此前文件中的这些成员被忽略。）*

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

**格式** 是 fit 自己的 `fitstore`：59 字节的头部（魔数 `FITSTORE`、版本、Argon2id 参数、salt 和 nonce），之后是用
XChaCha20-Poly1305 封存的 JSON 载荷，头部作为附加数据。fit 写入的第 2 版封存的是
`{"config": <基础配置>, "users": [{"username", "password_hash"}]}`：改过密码的控制台用户与配置放在一起。第 3 版增加
`"sections"`，即 fit 其他部分保存在其中的命名 JSON 值，如[助手](#助手)的设置与模型 API 密钥；只有保存了分区时才写为第 3 版，
没有分区的 store 仍能被不支持分区的旧版 fit 读取，导出的文件也从不包含分区。第 1 版的 store 只封存配置，仍可读取，之后按第 2
或第 3 版写入。密钥由密码经 Argon2id（time 3、64 MiB、4 线程）派生，只在 fit 启动时派生一次；每次
写入使用新的 nonce，所以写入不再派生密钥。写入先写到 store 旁的临时文件，权限 0600，同步后重命名覆盖 store，因此崩溃只会留下
旧的或新的 store，不会留下半个。经 Admin API 或页面导入的文件，最多可要求上述参数的两倍（time 6、128 MiB、8 线程），
且同时只打开一个；其间再导入 store 文件得到 `503`。

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

## fit run -admin

`fit run -admin` 在没有相关配置时开启 Admin API 和控制台，用于首次启动或从控制台配置一台机器：

```bash
fit run -admin                       # 有 fit.json 时使用它，否则不需要任何配置
fit run -admin -c site.yaml -timeout 30m
```

- 配置未设置的 `admin` 与 `admin.console` 使用默认值（Admin API 在 127.0.0.1:2019，控制台在 127.0.0.1:2020）。
  没有 `admin.console.users` 时，用户 `admin` 以密码 `admin` 登录。
- 该密码是**临时的**：必须在 `-timeout`（默认 10m，自启动时起算）内修改。修改之前，用它登录只能打开“修改默认密码”
  页面：控制台的其他接口一律返回 403 及 `"must_change_password": true`，Admin API 对所有请求返回 403。超过
  `-timeout` 后该密码无法再登录（401，`"password_expired": true`），须以 `-admin` 重启 fit 并及时修改。新密码不能是
  `admin`，并保存在 store 中，重启后用新密码登录，不再要求修改。
- 配置保存在 store 中，页面上的每次修改都写入它。没有 `admin.store` 时，store 是配置文件旁扩展名为 `.store` 的文件
  （`site.yaml` 对应 `site.store`，`fit.json` 或无配置对应 `fit.store`），密码为 `$FIT_STORE_PASSWORD`，未设置时为
  store 旁的**密钥文件**，即 store 路径加 `.key`（`site.store.key`），两者都不存在时以随机密钥创建，权限 0600。密钥文件
  应与 store 一起保存：复制到别处的 store 用它打开，或把 `$FIT_STORE_PASSWORD` 设为其内容。
- 只有 `admin.console`、没有监听和路由的配置是合法的，fit 可以在尚无服务内容时启动。

`-c` 也接受 `.store` 文件（最多一个），可与配置文件一起或单独给出：`fit run -c site.store` 运行 store 中的配置，
`fit run -c base.yaml -c site.store` 按上文把它合并到 `base.yaml` 之上；此后写入的就是这个 store（文件设置了
`admin.store` 时仍使用其密码）。配置文件旁的 `.store` 只要存在就会使用，不论是否给出 `-admin`。`fit store export`
和 `fit store import` 接受同样的 `-c`。

## 配置的导入与导出

控制台 Global 菜单中的 **Import / Export** 页面导入或导出整个配置；每个编辑部分配置的页面在其格式标签旁有
**Export** 和 **Import** 按钮，只处理该部分。格式为 `.conf`、`.yaml`、`.json` 和 fitstore 格式的 `.store`：导出
`.store` 时用对话框中给出的密码封存，未给出时用运行中 store 的密钥（之后用密钥文件或 `$FIT_STORE_PASSWORD` 打开）。
整个配置的 `.store` 导出还包含修改过密码的控制台用户，因此在别处 `fit run -c backup.store -admin` 可以同时恢复两者。
导入立即生效，经过与保存相同的路径，并写入 store；带页面成员时只取文件中的这些成员，其余忽略。`admin` 从不导出或导入。

两者都是 Admin API 的接口，控制台在进程内调用：

| 接口 | |
| --- | --- |
| `POST /config/export` | 请求体 `{"format": "conf"\|"yaml"\|"json"\|"store", "members": [...], "password": "..."}`；返回文件，带 `Content-Disposition`。 |
| `POST /config/import?format=...&members=a,b` | 请求体为文件（`text/plain`，`store` 为 `application/octet-stream`），自带密码的 store 用 `Fit-Store-Password`，`If-Match` 可选；返回同 `PUT /config/text`。 |

## 控制台

| 字段 | 默认值 | |
| --- | --- | --- |
| `address` | `127.0.0.1:2020` | 控制台监听的主机和端口。 |
| `cert_file`、`key_file` | | 控制台证书（及中间证书）和私钥的 PEM 文件；设置后控制台以 TLS 1.2 或更高版本提供服务。两者同时设置或都不设置。 |
| `users` | | 必填。可以登录的用户：至少一个，每个有 `username` 和 `password_hash`。 |
| `session_timeout` | `30m` | 没有请求时登录保持多久。 |
| `session_lifetime` | `12h` | 无论多活跃，登录最长保持多久。 |
| `max_sessions` | 64 | 同时保留的登录数上限；再多一个时结束最早的。 |
| `max_conns` | 32 | 同时打开的连接数上限，其中四分之一留给已登录的连接。连接占满时，新连接不被拒绝，而是关闭未登录连接中、持有最多此类连接的客户端（本机的客户端算作一个）最早的那个；只有每个连接都已登录，或其客户端已占满份额（四分之一，IPv6 客户端按 /64 计，回环客户端除外）时，新连接才在接受后立即关闭。 |

**非环回地址需要 `cert_file` 和 `key_file`**，使密码从不以明文穿过网络；`fit validate` 拒绝没有它们的配置。在环回地址上
TLS 可选，例如经 SSH 隧道访问的控制台。

控制台提供 **workbench（工作台）**：一个页面，左侧是控制台所提供各模块的平铺菜单（先是配置的全局页面，再是各协议页面，
最后是每个运行中的角色），右侧是所选的页面。其中的配置模块编辑配置；同一个 fit 中运行的每个 [LLM](llm.zh-CN.md) 与
[tunnel](tunnel.zh-CN.md) 角色也作为各自的模块显示在旁边，带有该角色自己控制台上的全部页面，只需登录一次。

控制台是监听器的 fib 引擎上它自己的一个绑定，与角色的页面一样，而不是配置中的监听器：配置中的监听器绑定失败时，控制台仍可
访问，以便修复。它的请求在 fib 的 worker 上处理，其中没有任何阻塞：密码检查和每次 Admin API 调用都在控制台自己的有界池上
进行。它在进程内调用 Admin API 的处理函数，外加自己的登录检查，因此两者经同一路径、以同样的检查应用配置；Admin API 保留自己
的地址、`token_file` 和规则，也同时监听。控制台不需要 store：没有 store 时，它像 Admin API 一样在内存中修改运行中的配置，
重启后从配置文件重新开始。

## 用户与 fit hash-password

用户是一个名字和一个 bcrypt 哈希；密码从不以明文写入。`fit hash-password` 输出密码的哈希。在终端中，它提示输入两次密码，
每输入一个字符显示一个 `*`，两次不一致则退出；否则从标准输入的第一行读取密码。它从不从参数读取密码（机器上的其他用户能看到参数）：

```sh
$ fit hash-password
Password: ****************************
Confirm password: ****************************
$2a$10$...
$ echo 'correct horse battery staple' | fit hash-password
$2a$10$...
```

`-cost` 设置 bcrypt 的代价（默认 10；每加一，检查耗时翻倍）。密码最多 72 字节（bcrypt 的上限），且不能为空。同样的哈希也
用于 `basic_auth`。`fit validate` 拒绝不是 bcrypt 哈希的 `password_hash`、空用户名和重复列出的用户名。

**修改密码。** 页面的“账户”模块让已登录用户凭当前密码修改自己的密码，也允许任何控制台用户设置另一个用户的密码（每个控制台用户
都是管理员）。新密码不能为空、最多 72 字节；它在控制台的有界池上用 bcrypt 生成哈希，写入 store 中配置的旁边，取代该用户在
引导文件中的密码。修改会结束该用户的其他会话（调用者自己的会话保留），并记录到 `admin` 日志。没有 `admin.store` 时无处保存：
页面会说明这一点，端点返回 `409`。用户本身及其初始哈希仍在引导文件的 `admin.console.users` 中；角色的页面所用的用户随配置
修改。

## 登录与会话

- **检查密码。** 不存在的用户名用一个虚设的哈希检查，使耗时无法区分用户名；同时最多进行两次检查，其余等待。
  登录在自己的池上检查（运行 1 个，等待 16 个，超出时 `503`），不会拖住已登录用户的请求。
- **锁定。** 一个客户端地址在 15 分钟内失败 5 次后被拒绝 15 分钟，返回 `429` 和 `Retry-After`。IPv6 地址按其 /64
  计数。同一地址同时只检查一次，同时发出的猜测绕不过计数；其间再发的得到 `429`。地址表最多 4,096 个地址；
  填满地址表不会提前结束锁定，地址表被锁定的地址占满时，新地址被拒绝，直到最早的锁定结束。
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

登录后即为 workbench。菜单是控制台所提供模块的一个平铺列表：先是配置的全局页面（概览、监听器、引擎、全局设置、整个配置的
JSON、版本、一致性检查、日志，以及账户页面），再是各协议与功能页面，最后是每个运行中的角色。模块的页面在其下展开；只有一个页面的模块
就是链接。**引擎** 条目是按端口查看运行中的监听：每个端口展开为该端口提供的功能——各处理器、反向与正向代理路由、缓存、流媒体与
WebRTC 处理器、四层服务、SOCKS，以及 TLS/ACME/默认 HTTPS——每项链接到编辑它的页面，端口所在行带上该端口的标签（PROXY 协议、
HTTP/3、按需证书与客户端证书）。菜单可以筛选、收起为图标，模块可以展开和折叠；右上角选择主题（浅色、深色及另外三种）与语言
（英文或中文）。每个页面有自己的 URL：`#/<模块>/<页面>`。

- **全局**：运行中配置的概览（状态、路由、四层服务、上游及其健康状况、证书、版本）；监听器；其余各部分配置（日志、访问日志、
  遥测、指标、限制、引擎、配置来源，以及 schema 中其他内容）；整个配置的 JSON；以及应用过的版本（`admin.history` 个）、
  每个的来源、每个的文本和回滚到它的按钮。
- **日志**：fit 自身日志与访问日志写出的文件、各自的切割与保留、磁盘上的文件及其大小与时间，**立即切割** 与
  **立即清理**（后者先预览要删除的文件），以及 `log` 与 `access_log` 的表单（其中含 `time_zone` 与 `rotate`）；保存并应用与其他
  页面相同。
- **HTTP**、**TCP/UDP**、**TLS**：各协议页面，菜单中有小标签：反向代理的路由与上游以及缓存（HTTP）、正向代理的路由
  （HTTP CONNECT）、四层服务（TCP 与 UDP）、TLS 的证书、默认 HTTPS 与 ACME。
- **流媒体**、**媒体认证**、**WebRTC**、**隧道角色**、**LLM 角色**：配置的 `streaming`、`media_auth`、`webrtc`、`tunnel`、
  `llm` 成员，各自独立成页而不再收在全局配置下，菜单中带标明其类别的小标签。流媒体页面指向设置其处理器（HLS、HTTP-FLV、
  WHIP/WHEP）的路由页面；隧道与 LLM 页面说明其角色在配置应用后启动。
- **账户**：已登录用户的密码，以及控制台用户的密码（见下）。
- **隧道**与 **LLM**：每个运行中的角色一个模块，如 `tunnel.hub`、`llm.node`，带有该角色的页面；hub 的令牌页和 LLM
  各角色的连接页签发、吊销其对端用来连接的连接令牌（见[隧道](tunnel.zh-CN.md#令牌)、[LLM 功能](llm.zh-CN.md#令牌)），
  Admin API 的 `/tokens` 端点和 `fit token` 也可以管理它们。

**引擎** 菜单把同一份视图放进菜单：每个监听端口一个子菜单，按配置与默认 HTTPS 生成的顺序排列。端口展开后是其处理器（静态文件、
直接应答、FastCGI、媒体令牌、媒体页面——各自链接到按该处理器筛选的路由页面）、反向代理与正向代理路由、缓存（缓存区页面与路由页面）
及其他中间件、流媒体处理器（流媒体页面及每个处理器的筛选路由）、WebRTC 处理器（WebRTC 页面及其路由）、四层服务、SOCKS、TLS 证书、
ACME、默认 HTTPS 与到它的重定向，最后是监听表单的链接。写入的地址旁给出内核实际绑定的地址（配置未写监听时，给出默认 HTTPS 生成的
端口），端口所在行带上其标签：PROXY 协议、HTTP/3、按需证书与客户端证书。监听页面本身编辑监听，并说明各端口功能在引擎菜单中。
指向某处理器路由的链接会打开按该处理器筛选的路由页面。

配置的页面是由嵌入二进制的 fit JSON Schema 生成的表单：每个对象、列表和模块按 schema 定类型，带有每个字段的说明，可省略的成员
带一个复选框，使表单就是配置的那棵树。fit 新增的字段无需修改控制台即可编辑。表单中没有 `admin`。每个配置页面还有 `.conf`、
`.yaml`、`.json` 三个标签页，用对应格式的原文编辑该页面所管的成员：文本从草稿读入，随输入适配成 fit 的 JSON（无法解析时标出，
并阻止保存），**保存并应用** 带 `If-Match` 发送它，由服务端把这些成员合并进配置，再走同一条路径应用——因此会经过与
`fit validate` 及应用相同的检查；校验失败的文本会返回其路径、行号和列号，且不会被应用。

所有页面编辑同一份配置草稿：**保存并应用** 带 `If-Match` 发送整个配置，或在原始文本标签页中发送该页面的文本，先合并进配置。
结果给出应用后的版本号及任何警告。有错误的配置被拒绝，错误显示在各自的路径处。若页面读取后配置被别处修改（`412`），页面会提示：
放弃修改以重新读取，或先从 JSON 页面复制修改。

供脚本使用的控制台自身端点有 `POST /api/login`（JSON 请求体 `{"username", "password"}`，返回用户名和 CSRF 令牌）、
`POST /api/logout`、`GET /api/session` 和 `GET /api/workbench`（用户名与所提供模块的 ID）。每个模块的端点在
`/api/m/<模块>/` 下：配置模块 `admin` 提供 `GET /api/m/admin/schema`，在 `/api/m/admin/` 下提供 Admin API 的端点（用会话
代替令牌，如 `GET /api/m/admin/config`），以及页面自己的端点：`GET /api/m/admin/users`（当前用户、各用户，以及是否有 store
保存它们）、`POST /api/m/admin/account/password`（自己的密码，需当前密码）、`POST /api/m/admin/users/password`（另一个用户的
密码），以及原始文本编辑器的 `POST /api/m/admin/config/render?format=`、`POST /api/m/admin/config/parse?format=` 和
`PUT /api/m/admin/config/text?format=&members=`。角色的模块提供其页面的 API，如 `POST /api/m/llm.hub/rpc/hub/nodes`。

## 检查运行中的配置

运行中的 fit 知道自己实际在运行什么；它运行所依据的配置说它应该运行什么。两者可能不一致，控制台的**一致性检查**页面与
`fit check` 会列出差异，让每一处都被有意识地处理：每处差异都标明它是 **drift**（可通过修改配置解决），还是 **info**（某个
运行事实，并非不一致）。

比较的两边是：运行时——已绑定的监听、运行中的角色、已提供的证书、缓存区、实际生效的引擎上限，以及 provider 提供的部分——
与生效配置，即 provider 合并后的基础配置。可能发现：

| | |
| --- | --- |
| 启动时才生效的设置 | fit 启动后改过 `log`、`access_log`、`telemetry`、`engine`、`acme` 或 `providers`：配置说一套，运行中的服务仍用启动时的值。修正方式是把生效值写回配置（或保留配置、等待重启）。`limits.max_conns`、`max_pending_bytes`、`max_write_queue_bytes` 同理，取自运行中的引擎。`admin` 只作为 info：它来自引导文件。 |
| 角色 | 配置设置了但没在运行的角色（启动失败），以及仍在运行但配置已不再设置的角色：可以从配置中删除，或重新应用配置以重试/停止它。 |
| provider 的部分 | provider（`docker`、`consul`、`consul_kv`、`etcd`、`kubernetes`）加入生效配置、但不在基础配置中的元素：把它们写入配置（`listeners`、`routes`、`upstreams`、`services`），或继续由 provider 管理。被遮蔽的 provider 元素属于 info。 |
| 监听 | 配置里的监听没有绑定，或绑定了配置中已没有的监听，属于 drift；监听地址为端口 `0` 时可把实际端口写回，属于 info。 |
| TLS | 某个监听提供的证书没有覆盖 `tls.acme` 中的某个名字，属于 drift（删除该名字，或保留并等待 ACME）；由默认 HTTPS 自动加入的名字、按需签发的证书属于 info。 |
| 上游 | 配置中的上游没有对应的池，或有池但配置中已没有该上游，属于 drift；某个上游的全部后端都不可用属于 info。 |
| 缓存 | 配置使用了某个缓存区但运行时没有对应的区，属于 drift；从更早的配置保留下来的缓存区属于 info。 |

页面会为每一处差异显示其配置值与运行值、类别与种类，以及一个可选的修正（`add`、`delete`、`replace` 或 `reapply`）；在用户逐项
选择并确认整个选择之前，不会应用任何修改。选中的修正随后走同一条保存路径：先校验，再作为新配置版本应用并写入 store，所以任何
一条无法加载的修正都会让全部修改被拒绝；期间配置被别处修改（`412`）也会被报告。`admin` 日志记录应用的版本。

`fit check` 在终端通过 Admin API 完成同样的事：

```sh
fit check -c /etc/fit/fit.json          # 列出差异；有 drift 时退出码 1
fit check -c /etc/fit/fit.json -json    # 以 JSON 输出检查结果
fit check -c /etc/fit/fit.json -fix     # 逐项选择修正，然后一起应用
```

引导文件中的 `admin.address`（以及 `token_file`）指明 Admin API 的位置。设置了 `admin.cert_file` 时，fit check 使用
https 并校验 API 的证书：能读到 `cert_file` 时（如在 fit 所在主机上）用其中的证书和系统 CA 校验，否则用 `-ca` 中的 CA；
校验的名称为地址中的主机，主机未指定时为 localhost，也可用 `-server-name` 指定。设置了 `admin.client_ca_file` 时，它出示
`-cert` 和 `-key` 给出的证书。

```sh
fit check -c /etc/fit/fit.json -ca /etc/fit/admin-ca.pem -server-name admin.example.com
```

退出码：没有 drift 为 `0`；仍有 drift（包括 `-fix` 之后）为 `1`；用法、配置加载、连接或解码错误为 `2`。

供脚本与其他页面使用的端点是 `GET /config/check`（运行中的版本、ETag、差异及其计数）与 `POST /config/reconcile`（JSON 请求体
为选中的 `{"fixes": [{"id", "action"}]}`，带 `If-Match`）；在控制台上对应
`GET /api/m/admin/config/check` 与 `POST /api/m/admin/config/reconcile`。

## 上限与超时

| | |
| --- | --- |
| 连接 | `max_conns`，32；单个客户端最多四分之一，回环客户端除外；四分之一留给已登录的连接，占满后新连接关闭最早的未登录连接 |
| 会话 | `max_sessions`，64，超出时结束最早的 |
| 请求头 | 16 KiB，10s 内读完 |
| 请求体 | `admin.max_config_bytes` 加 64 KiB，默认 4 MiB 加 64 KiB，30s 内读完 |
| 写响应 | 30s；角色的流改为以每次等待浏览器的时间为界，30s |
| 连接 | 发出带有效会话的请求之前（登录成功也算），打开 5s 后关闭，无论其间发送什么，与 Admin API 关闭未认证连接一样；之后空闲 2m，直到登出，或某个 API 请求发现其会话已结束，此时 5s 后关闭，除非其间又带来有效会话 |
| 同时进行的密码检查 | 2 |
| 同时进行的 Admin API 调用与角色方法 | 4 个运行、64 个等待，每个最多等待 30s；超出时 `503` |
| 修改密码 | 在控制台的有界池上用 bcrypt，同时最多 2 次检查，之后一次 store 写入 |
| 登录失败 | 每个地址（IPv6 按 /64）15 分钟内 5 次，之后拒绝 15 分钟，同一地址同时只检查一次；修改密码时当前密码错误也计入；保留 4,096 个地址 |

## 助手

`admin.assistant` 开启 workbench 的助手（见 [决定 031](decisions/031-assistant.zh-CN.md)）：此时控制台提供 `ai` 模块，
workbench 的菜单中显示 **助手**。不设置时没有该模块，也不向任何地方发送任何内容。它需要 `admin.console`，并与整个 `admin`
一样在 fit 启动时读取。

```json
"assistant": {
  "providers": [
    {"name": "claude", "kind": "anthropic", "models": [{"name": "claude-sonnet-4-5", "context": 200000}]},
    {"name": "local", "kind": "ollama", "base_url": "http://127.0.0.1:11434", "models": [{"name": "qwen3:8b"}]},
    {"name": "mesh", "kind": "llm_proxy"}
  ],
  "default_model": {"provider": "claude", "model": "claude-sonnet-4-5"},
  "users": [{"username": "ops", "level": "propose"}]
}
```

**提供方。** `kind` 为 `anthropic`、`openai`、`openai_responses`、`gemini`、`azure_openai`（带 `api_version`）、`ollama`、
`openai_compatible`（任何支持 Chat Completions 的服务器，需 `base_url`）或 `llm_proxy`，即本 fit 自己的
[llm.proxy](llm.zh-CN.md)，经回环地址调用其 `api`，使用它的某个 API 密钥；有 TLS 时固定其证书。每个提供方都有
`connect_timeout`（10s）、`first_byte_timeout`（2m）、`idle_timeout`（1m）、`timeout`（10m）与 `retries`（2，仅在尚未取得
任何回答时重试）。

**设置页** 列出各提供方及其密钥状态，在对话框中编辑，并可在保存前 **测试**：先取提供方的模型列表，再对某个模型做一次简短
调用，各自给出用时；**读取提供方的模型列表** 会添加它列出的模型。下方设置默认模型及其参数、上限、可发送的日志以及各控制台
用户的级别。**保存设置** 把它们保存到 store，此后取代 `admin.assistant` 中的设置；没有 store 时无法在此保存，密钥来自
`api_key_env`。

**密钥** 只写不读。在页面输入的密钥随设置进入 store，与 store 的其余内容一起加密；页面只显示提供方是否有密钥、来自哪里以及
末四位。`api_key_env` 则指定一个环境变量，每次调用时读取，store 中的密钥优先。密钥从不出现在响应、日志行或错误中（即使 API
把它回显），且只经 https 发送，或发往回环主机。

端点为 `GET /api/m/ai/settings`（正在使用的设置、来源、是否有 store 保存、版本，以及页面所见的各提供方密钥信息）、
`PUT /api/m/ai/settings`（`{"settings", "keys": {"<提供方>": "<密钥>"，或 "" 表示删除}, "version"}`；设置无效时 `400` 并给出
各路径的错误，没有 store 时 `409`，版本过期时 `412`）和 `POST /api/m/ai/providers/test`（`{"provider", "key", "model",
"models_only"}`；同时最多四个，超出时 `429`；浏览器离开时取消测试中的调用）。

### 对话面板

每个页面的顶栏都有助手按钮（用户级别为 `off` 时没有），点开后对话面板显示在页面旁。消息发往默认模型，并以当前页面作为上下文
（其模块、页面与标题、页面声明的选中项，以及页面上选中的文字；取消勾选 **将本页作为上下文发送** 则不发送）。回答以流的形式到来：
Markdown 随到随画，每次工具调用为一行可展开的记录（参数与结果），`ask_user` 的问题显示为一排按钮，所选会回传给模型。
**停止** 结束回答并保留已生成的部分；**重试** 重新回答最后一条消息；面板头部可选择对话、新建或删除对话。

助手只读取，自己从不修改任何东西：它调用的工具（**功能** 页列出它们、各自所需级别以及是否启用）读取配置、schema、版本、
状态、指标、上游健康、一致性检查、证书、缓存、fit 自身日志与访问日志，像 `fit validate` 一样检查配置，转换 nginx 或
Caddyfile 文本，并向用户提问。它们在控制台的线程池上、经进程内的 Admin API 运行，每次最多 30 秒。每个结果在交给模型前都会
脱敏（密钥、密码哈希、令牌、私钥、各提供方的密钥、`data.redact` 的模式，以及任何工具都不读取的成员：`admin.assistant`、
`admin.console.users`、`admin.store`、`admin.address` 与 `admin.token_file`），截断到 `limits.max_tool_result_bytes` 并注明，
且作为数据加以标示，系统提示要求模型绝不把它当作指令。`log_tail` 需要 `data.send_logs`，读取 fit 日志在内存中保留的最近
1000 条消息；`access_log_sample` 需要 `data.send_access_log`，读取访问日志文件的末尾。用户的话按原样发给模型。

**上限。** `max_tool_calls` 限制一条消息的回答可调用的工具数（超出时调用失败、回答结束），`max_turns` 限制一个对话的消息数
（超出后请新建对话），`max_concurrent_chats` 限制同时生成的回答数（超出时 `429`），`max_tokens_per_day` 限制每个用户每天的
token（超出时 `429`，或正在进行的回答结束），`max_tool_result_bytes` 限制工具结果，`max_stream_bytes` 限制模型的一次回答，
`max_conversations` 限制每个用户保留的对话数（再多一个会删除最旧的）。对话超出模型的上下文窗口（模型的 `context`，未设置时为
32768 个 token）时，先截断早期的工具结果，再由模型把早期轮次总结为摘要；摘要随对话保存，并代替这些轮次发送。

**对话** 按用户保存：有 admin.store 时保存在其 `assistant.chats.<用户名的哈希>` 段中，与 store 的其余内容一起加密，重启后仍在；
每个对话最多 512 KiB，每个用户合计最多 8 MiB；没有 store 时只保留到 fit 重启。用户只能看到自己的对话。

端点为 `POST /api/m/ai/chat`（`{"conversation", "message" | "retry": true | "answer": {"id", "choice"}, "page", "lang"}`；
以 SSE 应答：类型为 `start`、`text`、`thinking`、`tool_call`、`tool_result`、`ask`、`usage`、`summary`、`proposal` 与 `page_call` 的 `data:` 事件，
最后是带 `{"type": "done", "reason"}`（`answer`、`ask`、`max_tool_calls`、`max_tokens_per_day`、`stopped`）的 `event: end`，
或带 `{"message"}` 的 `event: error`；级别为 `off` 的用户 `403`，对话不存在 `404`，没有模型、对话正在回答或已达轮数时 `409`，
超出同时回答数或当天 token 时 `429`）、`POST /api/m/ai/chat/stop`（`{"conversation"}`）、`GET /api/m/ai/conversations`、
`GET` 与 `DELETE /api/m/ai/conversations/{id}`，以及 `GET /api/m/ai/tools`（用户的级别、模型，以及每个工具的级别、分组、
schema 及本用户能否调用）。

### 提案、级别与审计日志

**级别。** `admin.assistant.users` 为每个控制台用户设定级别，`default_level` 为其余用户的级别：`off`（没有面板）、`read`
（只读工具）、`edit`（另可起草打开页面上的草稿，I154）、`propose`（另可使用提案工具）与 `apply`（另可应用提案），每一级都包含
之前的各级。只有用户级别达到工具所需级别时，该工具才提供给模型；每次调用在运行时还会按当时使用的设置再检查一次，因此在设置页
降低某用户的级别后，正在进行的对话的下一次调用就会被拒绝。

**提案。** 即使级别为 `propose` 或 `apply`，助手自己也不修改任何东西：它的提案工具只向对话唯一的待处理提案加入步骤。
`config_propose` 整体替换成员（`members`，`null` 表示删除）或按 JSON 指针操作（`ops`：`add`、`replace`、`remove`）；
`reconcile_propose` 按差异与操作选择一致性修复；`rollback_propose` 重新应用某个保存的版本；`cache_purge_propose` 按 URL、标签
或全部清除某个缓存区。每个步骤都会与其他步骤一起在运行中的配置上检查（与 `fit validate` 相同），提案记录检查时的 ETag 与版本；
本身有误的步骤会被拒绝、不加入提案，而使整体无效的步骤会连同错误保留，等待下一个步骤修正。一个提案的配置步骤只能是一种：
补丁，合并后以一次 `PUT /config/text?members=` 应用其改动的顶层成员；一致性修复，以一次 `POST /config/reconcile` 一起应用；
或一次回滚，`POST /rollback/{n}`。缓存清除可与任何一种同在，在配置之后执行。任何步骤都不能触及 `admin`（其成员不在任何工具
的范围内，且只在 fit 重启时生效），也不能写入密钥：模型看到的 `[redacted]` 值只能写回配置中原有的位置，届时恢复为配置原值。
最多 32 个步骤、256 KiB 参数。`config_propose` 的步骤可以列出从其他服务器迁来的配置中没有带过来的项（`unconverted`，
最多 100 项，每项 500 字节），随提案一起显示。

面板在输入框上方显示待处理的提案：各步骤、改动的成员、是否通过检查、`.conf`、`.yaml` 与 `.json` 三种格式的差异（已脱敏），
以及 **审阅并应用…** 与 **丢弃**。“应用”会打开含完整差异的对话框，只有其中的 **应用** 才会以该用户的身份应用，带上所见的 ETag
作为 `If-Match` 以及所确认的修订号：生成一个新版本（来源为 console），保存到 store 并列入 **版本**，然后执行缓存清除。取消对话框
不会发送任何请求。若提案检查后配置已变，应用会被拒绝，提案移到新版本上重新提供，需要重新审阅与确认；Admin API 拒绝的变更不会
应用任何内容，提案连同原因保留，模型也会得知。只有级别为 `apply` 的用户能应用；`propose` 用户的提案等待这样的用户，他们在
**提案** 页可看到所有用户的待处理提案。

**审计日志** 记录助手做过的事：每次调用了工具的回答（其调用，已脱敏并截断；留下的提案；结果），以及每次应用、拒绝、重新提供
与丢弃，含用户、时间、对话、技能、提案的步骤与生成的版本。最多保留 `limits.max_audit_entries`（1000）条，保存在 store 的
`assistant.audit` 段中，重启后仍在，在 **审计日志** 页查看。

端点为 `GET /api/m/ai/proposals`（待处理的提案：用户自己的；级别为 `apply` 时为所有用户的）、
`GET /api/m/ai/proposals/{conversation}`（`?user=` 读取他人的：在运行中的配置上检查后的提案，含 `revision`、`etag`、`stale`、
`steps` 及各步骤的错误、`changed`、按格式的 `diff`、`valid`、`errors`、`unconverted`、`can_apply`）、
`POST /api/m/ai/proposals/{conversation}/apply`（`{"id", "revision", "etag", "user"}`；`200` 带 `outcome` 为 `applied` 或
`partial`、`version` 与清除结果；级别低于 `apply` 时 `403`；修订号不符或对话正在回答时 `409`；过期时 `412` 并重新提供提案；
检查未通过或 Admin API 拒绝时 `422`）、`POST /api/m/ai/proposals/{conversation}/discard`（`{"id", "user"}`）以及
`GET /api/m/ai/audit`（`?user=`、`?conversation=`、`?kind=` 为 `chat`、`apply` 或 `discard`、`?before=` 某条记录的 `seq`、
`?limit=` 最多 500；最新的在前）。

### 页面上的草稿

级别为 `edit` 及以上的用户所要求的修改，会在其所在之处起草：在编辑配置的页面上（网关与系统中带表单及 `.conf`、`.yaml`、
`.json` 标签页的页面，以及各功能页面），助手把修改写入页面的表单或文本标签页，作为未保存的草稿；由人在页面上检查并点击
**保存并应用**，经页面通常的应用流程提交，记为此人的修改。助手从不点击它。

页面工具：`page_get`（级别 `read`）读取页面当前的内容（含未保存的修改）：其所编辑成员在草稿中的值（已脱敏）、当前标签页、
无法解析的文本以及上次保存的问题。`page_edit`（级别 `edit`）为页面所编辑的成员起草修改：整体设置成员（`members`，`null`
表示删除）、按 JSON 指针的操作（`ops`：`add`、`replace`、`remove`），或以页面某个标签页格式（`format` 为 `conf`、`yaml` 或
`json`）书写的本页部分（`text`），并附页面上显示的 `summary`。`page_revert`（级别 `edit`）撤回助手的草稿，回到首次起草之前
的内容；若人在此之后改过页面则被拒绝，此时由页面自己的 **撤回助手的草稿**（会先确认）或 **放弃修改** 完成。

页面声明它所编辑的内容，面板将其作为 `editable`（`{"members", "formats", "tab", "unsaved", "assistant_draft"}`）随页面发送；
只有当前页面声明了编辑器时才提供页面工具，MCP 没有打开的页面，从不提供。草稿交给页面之前，fit 先在控制台线程池上检查：
它设置的每个成员都是页面所编辑的，都不在 `admin` 中，不写入任何密钥字段（模型看到的 `[redacted]` 值可以抄回，页面会放回
该处原有的值），文本按其格式可以解析，参数最多 256 KiB；未通过的草稿会被拒绝并说明原因，不会到达页面。页面无法容纳的修改
（其他成员、密钥、`admin`、角色自己的设置）改为提案；级别低于 `propose` 时，则加以描述，由人在编辑它的页面上完成。

页面随后像应用人工修改一样把草稿写入共享草稿，对文本运行它自己的检查（`POST /config/parse`，与手工输入的文本相同），标出
修改的字段，在表单上方显示带 **撤回助手的草稿** 的提示，在顶栏 **有未保存的修改** 旁显示 **助手草稿** 徽标，并以其修改的
内容应答。页面工具从不调用 `PUT` 或 `PATCH /config`、`/config/text`、reconcile、回滚或缓存清除，也不保存：在人保存之前，
版本、ETag 与 **版本** 列表保持不变，审计日志把页面工具的调用记为对话中的工具调用，从不记为一次修改。保存、放弃或重新读取
配置都会结束草稿的标记。

页面工具的调用以流中的 `page_call` 事件到达面板（`{"id", "name", "arguments"}`，参数为 fit 检查后的内容）；面板在当前页面上
执行，并以 `POST /api/m/ai/chat/page`（`{"conversation", "id", "result" | "error"}`；对话未在等待该调用时 `409`）应答，其结果
与其他工具的结果一样脱敏并受限。对话最多等待应答 30 秒，浏览器离开时即停止等待。

### 技能

技能是一个命名的任务：何时使用、说明做法的提示词、可调用的工具、所需的输入，以及（如需要）它自己的模型。它以 Markdown 书写：
两行 `---` 之间为 YAML front matter，其后为提示词：

```markdown
---
name: ops-check
title: Ops check
title_zh: 运维检查
description: Checks what the ops team checks every morning.
description_zh: 检查运维团队每天早上检查的内容。
tools: [status_get, upstream_health, config_propose]
inputs:
  - name: upstream
    description: The pool to look at.
    required: false
model: anthropic/claude-sonnet-4-5
examples:
  - Is everything up this morning?
---
Read the status and each upstream's health, and say what is down and why.
```

`name`（小写字母、数字与 `-`，最多 40 个字符）、`title` 与 `description` 为必填；`title_zh` 与 `description_zh` 是工作台
中文界面显示的内容；`tools` 列出可调用的工具，还会再按用户级别与设置收窄（`ask_user` 始终可用）；`model` 为设置中某个提供方
的 `provider/model`，或默认提供方的某个模型；`inputs`（最多 8 个）与 `examples`（最多 8 个）显示在技能页与面板中。一个技能
最多 32 KiB。不符合上述格式的 front matter（未列出的键、类型不对的值、已被占用的名称、无法解析的 YAML）会被拒绝，并给出出错
的行号。

fit 内置 14 个技能，嵌入在二进制中：`explain`、`route-builder`、`reverse-proxy`、`tls-acme`、`rate-limit-auth`、
`cache-tuning`、`diagnose`、`drift-explain`、`log-analysis`、`security-review`、`migrate`、`media-setup`、
`llm-mesh-setup` 与 `tunnel-setup`。**技能** 页把它们与用户添加的技能一起列出：每个技能的标题与用途、所列的工具（本用户不可
调用的以删除线显示）以及是否启用；可查看全文、导出为 `.md`，并用 **试用** 以其示例运行。级别为 `apply` 的用户可以添加技能
（从模板或导入 `.md` 文件）、编辑与删除已添加的技能，并启用或停用任意技能；内置技能不能修改或删除，但可以导出、改名后作为自己的
技能添加，再停用内置的那个。添加的技能由所有控制台用户共享，最多 64 个，与停用了哪些内置技能一起保存在 store 的
`assistant.skills` 段中；没有 store 时只保留到 fit 重启。

在面板中，**技能** 按钮用于选择技能，在消息开头输入 `/` 会列出匹配的已启用技能；`/name 其余内容` 以其余内容运行该技能。输入框
上方的标签显示对话运行的技能，点 × 停止使用。没有技能时，每次调用的系统提示会列出已启用的技能（每个只有名称与一句英文描述），
当用户的请求符合某个技能时，模型用 `skill_load` 加载它：该工具的结果是技能的完整提示词，此后对话运行该技能。技能运行期间，
系统提示带上它的提示词而不带目录，提供给模型的工具是技能所列、且用户级别与设置允许的那些，外加 `ask_user`；技能所列但超出
级别的工具不会提供，提示中会说明。目录最多占模型窗口的十分之一：超出时只列名称。审计日志记录每次回答运行的技能。

端点为 `GET /api/m/ai/skills`（所有技能，不含提示词，各带 `builtin`、`enabled` 与 `tool_state`，以及本用户能否修改）、
`POST /api/m/ai/skills`（`{"source"}`：添加；`400` 带 `{"error", "line"}`，名称已被占用或超过 64 个时 `409`）、
`GET /api/m/ai/skills/{name}`（全文，含 `prompt` 与 `source`）、`PUT`（替换已添加的技能，名称可变）、`DELETE`，以及
`POST /api/m/ai/skills/{name}/enable` 与 `.../disable`；修改需要 `apply` 级别（低于它时 `403`）。`POST /api/m/ai/chat`
可随消息带 `"skill"`（技能名，或 `""` 停止对话的技能）与 `"inputs"`（技能的输入，每个最多 2 KiB，作为数据发给模型）；以某个
已启用技能的 `/name ...` 开头的消息等同于 `"skill": "name"`。其 `start` 事件带上对话运行的 `skill`，`skill` 事件表示
`skill_load` 设定了技能。

### 迁移与安全审查

内置技能中有两个完成整件任务，fit 在固定的配置集上检验它们。

**`migrate`** 把 nginx、Caddy 或 Traefik 的配置迁移到 fit：把配置粘贴在 `/migrate` 之后，来源不明显时给出输入 `from`
（`nginx`、`caddyfile` 或 `traefik`）。nginx 与 Caddyfile 的文本先经过 `adapt_convert`，即 `fit adapt` 的转换器；它的
警告各带源行号，是留给模型的部分：模型手工转换（nginx 的 `allow` 与 `deny` 写成 `remote_ip` 匹配，旁边加一条回答 403 的
路由；`expires` 写成 `Cache-Control` 头），或列为未转换。Traefik 的 YAML（entry point、router、service、middleware）与
Docker 标签没有转换器，由模型按[从 Traefik 迁移](migrating-from-traefik.zh-CN.md)中的对应自行转换。结果合并进运行中的配置：
保留 fit 已有的 listener、路由与上游，在 fit 已监听的地址上复用那个 listener，重名的改名，不写 `admin` 也不写任何密钥，
因此配置无法带上的密码所保护的路由会回答 401，直到有人在页面上添加它们。结果用 `config_validate` 检查后成为一个提案，其
`unconverted` 列出每个未迁移的项，带源行号、原因以及 fit 中最接近的做法。面板与提案页把这份清单与差异一起显示；提案的应用与
其他提案相同。

**`security-review`** 读取配置、状态与证书，每行写一条发现，形如 ``- **high** `routes[2]` (admin-panel): what is wrong. Fix: the change.``，
最严重的在前，最后一行列出检查过且没有问题的内容。它至少检查：控制台可从任何地方访问、与 HTTPS 并存却不重定向的明文 HTTP、
即将到期的证书、要求客户端证书却不强制、任何人都能访问的指向内网或元数据地址的路由、没有认证的管理或内部路由、没有限制的
上传与登录、信任所有地址的 `trusted_proxies`、目录列表与点文件、过长的超时，以及写在配置里的密钥。用户要求时，它为能修复的
每条发现提出一个步骤；对 `admin` 或密钥的修改则说明如何在文件中修改。

**评估。** `assistant/testdata/eval` 中有两组配置：五个待迁移的配置（两个 nginx、一个 Caddyfile、一个 Traefik YAML 与一组
Docker 标签），每个都带一组请求及其提案应用到一个小型运行配置后这些请求应到达的地方，以及必须列为未转换的项；还有两个埋了问题
的 fit 配置，每个都带审查必须给出的发现（按成员）。`make assistant-eval` 用某个模型运行它们：

```bash
FIT_EVAL_KIND=anthropic FIT_EVAL_MODEL=claude-sonnet-4-5 FIT_EVAL_API_KEY=... make assistant-eval
```

它逐个报告：提案是否通过检查（与 `fit validate` 相同：加载并编译）、是否已应用并保留了路由、列出了多少未转换的项、审查找到了
多少埋下的问题，以及各自的工具调用与 token 数；最后给出每组通过的数量。`FIT_EVAL_BASE_URL`、`FIT_EVAL_API_VERSION` 与
`FIT_EVAL_CONTEXT` 像 `admin.assistant` 那样设定提供方，`FIT_EVAL_CASES` 用正则表达式挑选用例，`FIT_EVAL_REPORT` 把报告写成
JSON，`FIT_EVAL_RECORD=1` 把模型的回答写回各用例的 `recorded.json`。配置中提到、而本机没有的文件（证书、静态根目录）代表来源
机器上的那些文件。CI 不调用任何模型：`go test ./assistant` 用记录的回答运行同样的用例，每个都必须通过。

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
哪个服务端模块的 API、在菜单中的位置以及它的页面。提供页面的控制台提供某模块的服务端模块时，该模块才显示。模块还可以在每个页面的
顶栏放一个工具（`topbar`，如助手的按钮）；页面可以用 `WB.select` 声明其选中项，`WB.page` 会把它与当前页面一起交给助手面板。
