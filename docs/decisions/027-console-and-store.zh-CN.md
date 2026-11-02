# 027：带登录的 web 控制台，以及加密保存在磁盘上的配置

[English](027-console-and-store.md) | [简体中文](027-console-and-store.zh-CN.md)

状态：已接受（2026-11-01）。

## 背景

用户希望 fit 在配置开启时提供一个 web 控制台，用用户名和密码登录，在上面可以配置 fit 支持的所有功能；控制台编辑的配置
以 fit 自己的格式加密保存到本地文件，加密用的密码写在配置里。

Admin API（[决定 005](005-admin-api.zh-CN.md)）已经可以修改一切，但它要求 bearer token 或 loopback 地址，拒绝来自网页的
请求，而且应用过的配置只留在内存里，重启后又从文件开始。它的 dashboard 是只读的。

一个文件装不下打开它自己的密码，所以涉及两个文件：fit 启动时用的明文配置文件，和加密的 store。

## 决定

- **两个文件。** `-c` 指定的文件（引导文件）里放 `admin`：`admin.store` 写 store 的位置和密码，`admin.console` 写控制台
  监听的地址和用户。store 里放其余的配置，也就是 `admin` 以外的全部。写在引导文件里的密码，保护的是被拷离它的 store（备份、
  磁盘镜像），而不是在同一台机器上被读取的 store；`password_file` 或 `password_env` 可以让密码不出现在文件里，就像
  `token_file` 之于 Admin API 的 token。三者必须恰好设置一个。
- **以 store 为准。** store 存在时，它就是基础配置，引导文件里 `admin` 以外的字段被忽略并给出警告。store 不存在时，引导文件
  的其余字段作为种子，并立即写出 store。此后每个应用成功的配置，无论来自控制台、Admin API 还是回滚，在开始运行后都写入
  store；写入失败时配置仍保持应用，结果里说明它没有保存。SIGHUP 重新应用 store（`fit store import` 可能改了它）；有 store 时
  拒绝 `-watch`。
- **`admin` 从不进入 store，也从不展示。** 引导文件的 `admin` 在每个配置加载时、合并 provider 之后注入，不进入基础配置：
  `GET /config`、`/versions` 和 `/config/effective` 里没有密码、哈希或密钥路径，这正是决定 005 对 token 的要求。有 store 时，
  带 `admin` 的配置被拒绝：`admin` 来自引导文件，在 fit 重启时生效。
- **store 的格式 `fitstore` 第 1 版。** 59 字节的头，之后是配置的 JSON，用 XChaCha20-Poly1305 加密，头作为附加数据：

  | 偏移 | 字节数 | |
  | --- | --- | --- |
  | 0 | 8 | `FITSTORE` |
  | 8 | 1 | 格式版本，1 |
  | 9 | 1 | 密钥派生，1 = Argon2id |
  | 10 | 4 | Argon2id time，大端 |
  | 14 | 4 | Argon2id 内存（KiB），大端 |
  | 18 | 1 | Argon2id 线程数 |
  | 19 | 16 | salt |
  | 35 | 24 | nonce |
  | 59 | | 密文及其 16 字节 tag |

  密钥由密码经 Argon2id（time 3、64 MiB、4 线程，RFC 9106 推荐的第二组参数）派生，在 fit 启动时派生一次并保留；每次写入
  使用新的随机 nonce、沿用 salt，所以写入时不再派生。打不开的 store（密码错误或字节被改动）会让 fit 无法启动，且不会被覆盖。
  写入先写到同目录下权限 0600 的临时文件，sync 后 rename 覆盖 store。两种算法都来自已经是依赖的 `golang.org/x/crypto`。
- **`fit store export` 和 `fit store import`**：给定引导文件，把 store 打印为 JSON，或把一个配置文件（fit 的任一格式，先检查）
  加密成 store。改密码就是 export、修改引导文件、再 import。
- **控制台是一个独立的服务**，和 Admin API 一样用 net/http、跑在自己的 goroutine 上（规则 4），地址为
  `admin.console.address`（127.0.0.1:2020），TLS 来自 `cert_file` 和 `key_file`，非 loopback 地址必须设置 TLS。Admin API 和它
  的规则不变；控制台在自己的检查之后，在进程内调用 Admin API 的处理函数来处理配置、版本、回滚、状态和缓存，两者经同一条
  路径应用配置。
  *2026-11-02 修改：* 控制台现在是监听器的 fib 引擎上它自己的一个绑定，与角色的页面一样由 `app/console` 提供，并提供
  workbench（`app/workbench`）：配置模块与每个运行中角色的模块，各在 `/api/m/<模块>/` 下，只需登录一次。Admin API 的处理
  函数在控制台的有界池上运行，绝不在 fib worker 上。它的绑定不是配置中的监听器，所以监听器失败的配置仍让它可以访问。
- **登录。** `users` 是用户名和 bcrypt 哈希，与 `basic_auth` 的写法相同，由 `fit hash-password` 生成；密码从不以明文写入。
  用户名不存在时也与一个占位哈希比较；同时最多做两次校验；一个客户端地址 15 分钟内失败 5 次后被拒绝 15 分钟，记录的地址最多
  4,096 个。会话是放在 cookie 里的 32 个随机字节，cookie 为 `HttpOnly`、`SameSite=Strict`，走 TLS 时还有 `Secure`；服务端
  只保存它的 SHA-256，空闲 `session_timeout`（30m）或存在 `session_lifetime`（12h）后结束，最多保留 `max_sessions`（64）
  个，超出时去掉最老的。
- **防范其他网页。** 除读取外的每个请求都要在 `X-Fit-Csrf` 里带上会话的 CSRF token；请求带 `Origin` 时必须是控制台自己的。
  页面带有只允许自身文件的 Content-Security-Policy 和 `frame-ancestors 'none'`，API 的应答带 `Cache-Control: no-store`。
- **所有功能，来自 schema。** 编辑器由嵌入二进制的 `fit.schema.json` 生成：每个对象、列表和模块一个表单，按 schema 定类型，
  附带字段的说明，所以 fit 新增的字段无需修改控制台即可编辑。旁边还有 JSON 原文编辑器。保存时用 `If-Match` 发送整个配置，
  配置的错误显示在对应的路径上。

## 影响

- store 里的配置在重启后依然有效；引导文件里的路由等只是种子。要从文件重新开始，删除 store。
- 丢了密码就丢了 store；`fit store export` 可以导出一份明文副本留存。
- 不设置 `admin.console` 时控制台关闭；它需要 `admin`，所以 Admin API 也会在自己的地址上、按自己的规则监听。
