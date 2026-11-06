# 集群管理

[English](cluster.md) | [简体中文](cluster.zh-CN.md)

一个 fit 可以管理多个 fit。设置了 `admin.cluster` 的 fit 是**管理端**：它保存其他 fit（**成员**）的地址和管理员凭据，读取每个
成员的状态，并在工作台的**集群**菜单中集中显示。管理端不在数据路径上：无论它是否运行，成员都按各自的配置提供服务；管理端宕机
或被移除，只会失去管理能力，不影响其他。设计与所做的选择见[决定 033](decisions/033-cluster-management.zh-CN.md)。

- [运行管理端](#运行管理端)
- [集群页面](#集群页面)
- [分组与操作](#分组与操作)
- [成员自己的页面：节点切换器](#成员自己的页面节点切换器)
- [如何访问成员](#如何访问成员)
- [管理端读取什么，以及可能出现的问题](#管理端读取什么以及可能出现的问题)
- [API](#api)
- [NAT 后面的成员](#nat-后面的成员)

## 运行管理端

管理端需要控制台（提供页面）和 store（加密保存成员及其密码、令牌）。最简单的做法是只含 `admin.cluster` 的配置，以 `-admin`
运行：它以默认值提供 Admin API 和控制台，并在配置旁保存一个 store：

```json
{"admin": {"cluster": {}}}
```

```sh
fit run -admin -c manager.json
```

控制台随后位于 `127.0.0.1:2020`，用户 `admin` 的密码为 `admin`，直到被修改；控制台会先要求修改（见
[`fit run -admin`](console.zh-CN.md#fit-run--admin)）。store 是配置旁的 `manager.store`，密钥为 `manager.store.key`。自行指定的配置：

```json
{"admin": {
  "store": {"path": "/var/lib/fit/manager.store", "password_env": "FIT_STORE_PASSWORD"},
  "console": {"address": "127.0.0.1:2020", "users": [{"username": "admin", "password_hash": "$2a$10$..."}]},
  "cluster": {"poll_interval": "10s", "timeout": "10s", "max_nodes": 256, "max_concurrency": 8, "max_response_bytes": 4194304}
}}
```

管理端和其他 fit 一样：同一份配置也可以提供路由。没有 store（既没有 `admin.store`，配置旁也没有）时，`fit run` 拒绝
`admin.cluster`。

| 设置 | 默认值 | 含义 |
| --- | --- | --- |
| `poll_interval` | 10s（至少 1s） | 每个成员的读取间隔 |
| `timeout` | 10s（至少 1s） | 对成员的一次调用最长耗时：连接、应答头与整个调用 |
| `max_nodes` | 256 | 最多保存的成员数，再多则拒绝 |
| `max_concurrency` | 8 | 同时读取的成员数；页面发起的测试另计，最多 4 个，成员页的读取最多 8 个 |
| `max_response_bytes` | 4 MiB（至少 16 KiB） | 从成员读取的最大应答 |

成员保存在 store 的 `cluster` 节中，与 store 的其余内容一起加密，且不进入 store 的导出，因此任何导出都不含成员的凭据。由更新
版本的 fit 写入的节只显示、从不覆盖：此时管理端为只读，页面会说明原因。

## 集群页面

设置了 `admin.cluster` 时，工作台左侧菜单显示**集群**，含**概览**、**节点**与**分组**（[分组与操作](#分组与操作)），以及每个成员
和每个分组的页面，提供中英文。

**概览**以卡片或表格显示管理端最后一次读到的各成员状态：着色的状态（绿色正常、琥珀色错误、红色离线、灰色尚未读取）及其持续
时长与原因、fit 版本、运行配置的版本、一致性检查统计的不一致数、最早到期的证书及剩余天数、连接数与不可用的上游后端，以及其
`GET /node` 的延迟。离线的成员保留其最后一次的应答，并注明时间。磁贴按状态计数，点击即按该状态筛选；筛选还可按文本（名称或 URL）、
标签选择器（`env=prod, region, tier!=db, !canary`：每一项都须满足，单独的 `key` 表示存在该标签，`!key` 表示没有该标签）、分组
（管理端当前确定的其成员）与状态，排序可按状态（严重的在前）、
名称、延迟、证书到期、不一致数、连接数或 fit 版本。筛选条件保存在页面 URL 中，因此在任何地方点击标签都会打开按其筛选的概览。
页面按管理端轮询的频率重新读取，间隔 2 到 10 秒。出问题的成员会说明问题及处理办法；助手开启时带**问 AI**（技能 `diagnose`，
提供成员的状态，从不含凭据）。

**节点**列出各成员的标签、URL、访问方式、凭据（只显示是否已保存）、证书的信任方式与状态，以及管理端最近的修改。**添加节点**
打开表单：名称、标签（`key=value`，以逗号分隔）、URL、访问方式（控制台的用户名和密码，或 Admin API 的令牌），以及 `https` 时
证书的信任方式（系统 CA、某个 CA 的 PEM，或固定的指纹）。**测试**按表单中的内容访问成员但不保存：显示成员的 fit 版本、主机与
节点 ID，或出错的原因，并显示成员出示的证书。不受信任的证书（例如自签名证书）按指纹信任：**信任此证书**完整显示证书，并提示
与成员自身的证书比对（其证书文件，即 `openssl x509 -noout -fingerprint -sha256 -in cert.pem` 输出的指纹），然后把指纹固定到表单
并再次测试。每个成员的行有**测试**、**信任**（证书不受信任，或不是已固定的那张时：有意更新的证书用同样方式再次信任，并带警告）、
**编辑**与**删除**（先确认）。每次修改都带页面读到的成员 ETag；其间别处做过修改时会被拒绝，页面随即重新读取成员。

凭据只写入一次。表单中的密码和令牌从不回填：已保存的只提示其存在，字段留空即保留。它们加密保存在管理端的 store 中，从不出现在
应答、日志或任何页面中。

**成员页**（任何地方点击成员名称）显示管理端保存的成员及其状态，可测试、信任、编辑与删除；随后是经管理端读取的成员自身应答：
成员身份（节点 ID、主机、fit/fib/Go 版本、平台、启动时间、运行时长、角色、schema 与能力）、运行中的配置及其重载与连接、监听、
上游及各后端的状态与计数、所提供的证书及其到期时间、保存的配置版本与一致性检查。成员无法读取时各部分显示各自的问题；成员不可用
时不读取。**打开**经管理端打开成员自己的页面（[节点切换器](#成员自己的页面节点切换器)）。

## 分组与操作

**分组**（资源分组）是一组成员：其**标签选择器**匹配的成员、按名称指定的成员，或两者之和。一个成员可属于任意多个分组，分组随成员
而变：成员的标签改变后，立即进入或离开其选择器此时匹配或不再匹配的分组；按名称指定的成员改名后仍在分组中，删除后即离开；尚未
添加的成员名称会保留并显示为缺失，直到添加了该成员。选择器即 Kubernetes 基于等值的选择器：以逗号分隔的条件，`key=value`（或
`key==value`）、`key!=value`（没有该标签的成员也满足）、`key`（有该标签）或 `!key`（没有该标签），每一项都须满足；空选择器不选中
任何成员，用于只按名称的分组。分组还有说明和**变量**：挂到该分组的模板（I145）将使用的名称与值；它们不是机密，按原样显示。

**分组**页列出各分组的选择器、按名称指定的成员（缺失的以琥珀色显示）、当前成员数（按状态统计）以及变量数。**添加分组**打开表单，
输入时预览其选择器与名称当前选中的成员，以及不是成员的名称；被拒绝的选择器或变量在其字段下说明。**编辑**与**删除**（先确认，成员
保持不变）修改分组，与修改成员一样带文档的 ETag。

**分组页**显示其选择器、按名称的成员、变量与成员状态，每个成员前有一个复选框（起初全部选中），以及**操作栏**：

| 操作 | 对每个选中的成员 | 需要（能力） |
| --- | --- | --- |
| **重新加载** | 把运行中的配置作为新版本再次应用，重新加载其从文件读取的内容（证书、密钥、令牌文件）：`POST /reload` | `config.reload` |
| **回滚** | 再次应用其上一个配置：其保存的、ETag 与运行中配置不同的最新版本（因此重新加载——同一配置的再次应用——不算一步），并以运行中配置的 ETag 作为 `If-Match`，其间配置已变化则不会被覆盖：`GET /versions`，然后 `POST /rollback/{n}` | `config.versions` |
| **一致性检查** | 运行其一致性检查，说明发现的差异数：`GET /config/check` | `config.check` |
| **清除缓存** | 删除其缓存在某个 zone（或全部 zone）中保存的内容：按键（请求的绝对 URL）、按前缀（以其开头的全部 URL，如 `https://example.com/img/`）、按标签，或全部：`POST /cache/purge` | `cache`，按前缀还需 `cache.prefix` |
| **测试** | 按管理端访问它的方式读取其 `GET /node`，说明耗时 | — |

每次操作都先在对话框中列出将作用的成员并确认（清除缓存时连同其 zone、键、前缀与标签）。之后操作在管理端执行，同时最多
`max_concurrency` 个成员，每次调用受 `timeout` 限制；页面逐个成员跟踪：等待中、执行中、完成（及其所做的：应用的版本、发现的差异、
清除的键数），或失败及原因：成员离线、凭据被拒、成员的能力列表中没有所需能力（早于该操作的 fit）、配置被拒绝、没有该 zone、没有
可回滚的版本。页面末尾为最近的操作，每个都可再次打开其结果。分组页上的**对任意成员操作**是覆盖全部成员的同一页面，用于不属于任何
分组的选择。

操作结束后，操作本身与每个成员的结果（状态、所做的事、错误）写入管理端的审计日志。管理端在内存中保存最近 32 次操作及其结果；
同时最多运行 4 次操作，已在某次操作中的成员在其结束前不会接受另一次操作。只有能写入其文档的管理端才运行操作，否则无法审计。

## 成员自己的页面：节点切换器

在管理端上，工作台页眉有一个**节点**选择框：*本管理端*，然后是每个成员及其状态。选择某个成员，即经管理端针对该成员打开工作台
自己的页面：概览、监听、引擎菜单、每个配置页面及其 `.conf`、`.yaml`、`.json` 标签页、检查与保存、配置版本及其回滚、一致性检查、
日志、缓存区、媒体页面，以及经控制台访问的成员的隧道角色页面。每个成员的卡片、行与成员页上的**打开**作用相同。此时页面 URL
带有 `?node=<成员>`，因此成员的页面可以收藏，也可以在单独的标签页中打开；选择**本管理端**或点击**返回管理端**会重新加载管理端
自己的页面，不留下成员的任何数据。

成员的页面有明显标记，以免误改其他 fit：页面标题前的药丸形标签与浏览器标签页标题中的成员名（`[edge-7] Routes · fit`）、页面
上方说明成员是谁、其主机、fit 版本及管理端如何访问它的横条，以及顶栏上方的线条，都使用该成员专属的颜色。菜单是成员的：其监听与
角色，不含管理端的集群菜单、账户页面与助手。

页面是管理端自己的：从不从成员加载任何标记或脚本。页面的请求不再发往 `/api/m/<模块>/<路径>`，而是发往管理端的
`/api/n/<成员>/m/<模块>/<路径>`，需要管理端的登录与 CSRF 令牌，由管理端按访问该成员的方式转发：经其控制台使用管理端保持的
会话、经其 Admin API 使用其令牌，或经其连接。浏览器从不持有成员的凭据，浏览器自己的 cookie 与 CSRF 令牌止于管理端。只转发
页面发送的请求头（`Content-Type`、`If-Match`、`If-None-Match`、`Upload-Offset`、`Upload-Length`、`Fit-Store-Password`），
只传回页面读取的应答头（`ETag`、`Fit-Config-Version`、`Fit-Dry-Run`、`Retry-After`、`Allow`、上传偏移、附件的
`Content-Disposition`，以及 JSON、文本、YAML 或字节的 `Content-Type`；其他类型一律按字节应答）：成员不能在管理端的源上设置
任何 cookie 或应答头。成员拒绝管理端的凭据、或要求修改其控制台用户的临时密码时，应答 `502` 及其问题，与其状态中的说明相同。

保存与在成员上直接保存相同：配置带上页面读取时的 ETag 发送，成员上此后已被修改的配置返回 `412`，保存的错误链接到对应字段。
**每次修改**（GET、HEAD 之外的请求，文本转换与“检查”除外，它们不改变任何东西）都连同其用户、成员、方法与路径以及成员的应答
（`PUT /config: 200, config version 8`）记入管理端的审计日志与日志。配置 schema 与管理端不同的成员（其 `GET /node` 的
`schema_digest`：运行的是另一版本的 fit）以**只读**方式打开：横条说明原因，“保存”变暗，管理端也像页面一样拒绝其修改（`409`）；
读取、检查与转换照常。尚未读取的成员，以及文档由更新的 fit 写入、无法记录修改的管理端的所有成员，同样只读。成员的页面以其自己的
schema 绘制其配置：经控制台为 `/api/m/admin/schema`，经令牌或连接为其 Admin API 的 `GET /schema`（能力 `schema`）；早于节点
切换器、不提供 schema 的成员以管理端的 schema 绘制，应答带 `Fit-Schema-From: manager`，其页面本就只读。

上限：同时发往一个成员的调用 3 个，另有 16 个等待，每个至多等待 `timeout`（之后返回 `503`）；同时发往所有成员的调用共 64 个
（再多返回 `429`）；请求体与应答各至多 `max_response_bytes`（分别返回 `413` 与带 `too_large` 的 `502`）；等待写入审计条目的修改
64 个。不转发：成员的控制台用户与密码（在成员上修改）、其 pprof 与仪表盘、其自己的集群管理与助手、LLM 角色的页面，以及流（`501`）；
隧道角色的页面需要经成员的控制台访问。

## 如何访问成员

成员是管理端能访问到的 fit，访问方式有三种：

- **`console`**：成员控制台的用户名和密码。管理端以 `POST /api/login` 登录，保存会话的 cookie 和 CSRF 令牌，经控制台读取 Admin
  API（`/api/node`、`/api/m/admin/status`、`/api/m/admin/config/check`），调用返回 401 时（例如成员重启或会话到期）重新登录。
  登录被拒后，除非修改或测试该成员，一分钟内不再发送，之后每次加倍，最长 15 分钟，因此错误的密码不会把成员的用户锁在外面；
  被成员限速的登录按其 `Retry-After` 等待。要求修改临时密码的控制台会被报告，其会话随即登出：管理端从不修改成员的密码。
- **`admin_api`**：成员 Admin API 的 bearer 令牌，直接访问 Admin API 的地址（`/node`、`/status`、`/config/check`）。
- **`link`**：主动连接管理端的成员（见 [NAT 后面的成员](#nat-后面的成员)），通过注册而非 URL 添加，管理端经其连接访问其 Admin API。

两者都提供时宜选择控制台：控制台用户是人所持有的，其登录减速机制也在网络上保护它。成员的 URL 必须是 `https`，或只指向回环
地址的 `http`，凭据不会以明文跨越网络；URL 可以带路径，用于代理后面的成员。成员证书由系统 CA、`tls.ca`（要信任的 CA 的 PEM）
或 `tls.fingerprint`（叶证书的 SHA-256，经人确认后固定）验证；没有信任任意证书的设置。

## 管理端读取什么，以及可能出现的问题

轮询器每隔 `poll_interval`、同时最多 `max_concurrency` 个成员，读取每个成员的 `GET /node`，其能力列表包含时再读 `/status` 和
`/config/check`。每次调用都经 fib 的异步 HTTP 客户端发出，从不阻塞 worker；`timeout` 限制连接、等待应答头和整个调用，
`max_response_bytes` 限制每个应答。成员状态记录最近一次轮询的结果（`status`：`up`、`down`、`error`，首次轮询前为
`unknown`；`error` 带 `kind` 和消息；`reachable`、连续失败次数 `failures`、`polled`、`seen`、`since`），以及最近一次成功读取
到的内容，成员下线时仍保留：节点信息（ID、主机、fit/fib/Go 版本、运行时长、角色、schema 摘要、能力）、运行中的配置（版本、
ETag、应用时间、来源）、重载次数、连接、上游及其下线的后端、证书及最近的到期时间、不一致数、`/node` 的延迟，以及它出示的证书。

每种失败有各自的类型，页面会说明其含义及处理办法：

| 类型 | 状态 | 含义 |
| --- | --- | --- |
| `unreachable` | down | 无法连接：被拒绝、无路由或主机不存在 |
| `timeout` | down | 未在 `timeout` 内应答 |
| `tls_untrusted` | error | 系统 CA 与 `tls.ca` 都不信任其证书；附带该证书，其指纹可以固定 |
| `tls_pin_mismatch` | error | 证书不是已固定的那张 |
| `tls` | error | TLS 握手因其他原因失败，例如以 `https` 访问不提供 TLS 的端口 |
| `auth_failed` | error | 用户名和密码或令牌被拒绝 |
| `rate_limited` | error | 失败的登录过多，成员暂时拒绝登录 |
| `password_change_required` | error | 成员的控制台要求修改该用户的临时密码：在成员上修改，然后在“节点”页更新 |
| `forbidden` | error | 成员返回 403 |
| `unsupported` | error | 成员不提供 `GET /node`：不是 fit，或是早于集群管理的 fit |
| `http_status` | error | 管理端未预期的其他状态码 |
| `bad_answer` | error | 应答不是 fit 应答的 JSON |
| `too_large` | error | 应答大于 `max_response_bytes` |
| `not_linked` | down | 主动连接的成员当前没有连接：尚未注册，或正在重新连接 |

问题解决后（成员重启、证书被信任、凭据被修改），成员会在下一次轮询时自行恢复。

## API

模块的端点位于 `/api/m/cluster/` 下，需要控制台登录和 CSRF 令牌；上述页面只使用这些端点：

| 端点 | 作用 |
| --- | --- |
| `GET nodes` | 成员列表 `{"version", "max_nodes", "read_only", "nodes"}`，带 `ETag`；成员的凭据只显示为 `password_set` 和 `token_set` |
| `POST nodes` | 添加成员：`{"name", "labels", "url", "access", "username", "password", "token", "tls": {"ca", "fingerprint"}}`；返回 `201`；字段错误返回 `400` 和 `{"field", "error"}`，名称已占用或达到 `max_nodes` 返回 `409` |
| `GET nodes/<name>` | 成员及其 `state` |
| `PUT nodes/<name>` | 替换成员，新的 `name` 即改名；留空的密码或令牌保留原值 |
| `DELETE nodes/<name>` | 删除成员，并登出其会话 |
| `POST nodes/<name>/test` | 测试已保存的成员，或按请求体修改后的成员（不保存）：`{"ok", "error", "latency_ms", "certificate", "node", "must_change_password"}`；`certificate` 含有供人确认后固定的指纹 |
| `POST test` | 按请求体测试尚未添加的成员 |
| `GET nodes/<name>/read/<what>` | 成员自身对 Admin API 的 `GET /node`、`/status`、`/versions` 或 `/config/check` 的应答（`what` 为 `node`、`status`、`versions` 或 `check`），经管理端以轮询器保持的会话读取；成员无法读取时返回 `502` 和 `{"error", "problem"}`，`problem` 与状态中的 `error` 相同 |
| `GET link` | 成员连接的位置：`{"enabled", "address", "url", "fingerprint", "links", "default_ttl", "max_ttl"}` |
| `POST enroll` | 添加主动连接的成员：`{"name", "labels", "ttl", "url"}`；返回 `201` 和 `{"node", "token", "expires", "config", "config_line"}`，令牌只显示这一次；没有 `link_address` 时返回 `409` |
| `POST nodes/<name>/enroll` | 为连接型成员生成新的注册令牌：`{"ttl", "url"}`，应答相同 |
| `GET state` | 所有成员的状态：`{"interval_ms", "timeout_ms", "read_only", "nodes"}` |
| `GET audit` | 管理端的修改记录：谁、何时、哪个成员或分组、改了什么（凭据只记字段名）；操作带其 `op` ID 与每个成员的 `outcomes` |
| `GET groups` | 分组列表 `{"version", "max_groups", "read_only", "groups"}`，带文档的 `ETag`；每个分组为 `{"name", "description", "selector", "members", "variables", "created", "updated", "nodes", "missing"}`，`nodes` 为当前属于它的成员，`missing` 为不是成员的名称 |
| `POST groups` | 添加分组：`{"name", "description", "selector", "members", "variables"}`；返回 `201`；字段错误返回 `400` 和 `{"field", "error"}`，名称已占用或已有 256 个分组返回 `409` |
| `GET groups/<name>` | 分组及其成员的 `states` |
| `PUT groups/<name>` | 替换分组，新的 `name` 即改名 |
| `DELETE groups/<name>` | 删除分组；其成员保持不变 |
| `POST select` | 选择器与名称当前选中的成员（无需分组）：`{"selector", "members"}`，应答 `{"nodes", "missing"}` |
| `POST ops` | 开始一次操作：`{"op", "group", "members", "purge"}`，`op` 为 `reload`、`rollback`、`check`、`purge` 或 `test` 之一；作用于分组的成员、`members`，或分组中的 `members`；`purge` 为 `{"zone", "keys", "prefixes", "tags", "all"}`；返回 `202` 与该操作；成员已在另一操作中返回 `409`，已有 4 次操作在运行返回 `429` |
| `GET ops` | 保存的操作，最新的在前 |
| `GET ops/<id>` | 一次操作：`{"id", "op", "group", "user", "purge", "started", "ended", "finished", "counts", "outcomes"}`，每个结果为 `{"node", "status", "detail", "error", "result", "started", "ended", "took_ms"}`，`status` 为 `pending`、`running`、`done` 或 `failed`，`error` 与状态中的问题相同 |

在模块之外、控制台的顶层，是节点切换器的转发：

| 端点 | 作用 |
| --- | --- |
| `GET /api/n/<成员>/workbench` | 该成员可打开页面的模块：`{"user", "modules", "member"}`，`member` 为 `{"name", "access", "url", "id", "hostname", "fit_version", "schema_digest", "manager_schema_digest", "roles", "read_only", "reason_kind", "reason"}`，`reason_kind` 为 `version`、`not_read` 或 `manager`；此时读取其 `GET /node` |
| `GET /api/n/<成员>/node` | 成员的 `GET /node` |
| `<方法> /api/n/<成员>/m/<模块>/<路径>` | 成员的 `/api/m/<模块>/<路径>`（模块 `admin` 为其 Admin API 的 `/<路径>`），带查询参数；按成员的应答返回，应答头如上所述；无法访问成员时返回 `502` 与 `{"error", "problem"}`，不转发的接口返回 `403`，对只读成员的修改返回 `409` |

带 `If-Match` 且其 ETag 已不是文档（成员与分组）当前 ETag 的修改返回 `412`。同时最多进行 4 个测试和 8 个读取，再多返回 `429`。读取只转发
成员的这四个 GET，不转发其他接口，从不写入；成员的能力列表中没有某个读取所需的能力（`status`、`config.versions`、
`config.check`）时不会向它请求。任何应答、页面或日志都不含密码、令牌或成员的会话 cookie。

## NAT 后面的成员

管理端无法直接访问的成员（位于 NAT 或防火墙之后，没有开放端口）**主动连接管理端**（I149），并通过这条连接被管理，如同其他
成员通过 HTTP 被管理：轮询、测试、在成员页读取，状态、失败类型与 API 都相同。

管理端以 `admin.cluster.link_address` 开启连接监听，即成员主动拨入的基于 TLS 的 WebSocket。其证书为自签名，只生成一次并保存在
store 的 `cluster` 节中，重启后不变，也不需要 CA：每个成员注册时固定（pin）它。

```json
{"admin": {"cluster": {"link_address": ":7444", "link_url": "wss://manager.example.com:7444/cluster/link"}}}
```

| 设置 | 默认值 | 含义 |
| --- | --- | --- |
| `link_address` | 关闭 | 成员连接的地址，如 `:7444`；不设置则任何成员都不能注册 |
| `link_url` | 打开页面所用的主机名加 `link_address` 的端口 | 配置行中管理端的 URL：`wss://host:port/cluster/link` |

在“节点”页，**通过注册添加**填写成员名称、标签、令牌有效期（留空为 24h，最长 30 天）以及成员访问管理端所用的 URL，并只显示
一次要粘贴到成员 `admin` 中的配置行：

```json
{"admin": {"cluster_member": {"manager": "wss://manager.example.com:7444/cluster/link",
  "fingerprint": "3b:1e:…:73:76", "token": "fite1.<id>.<secret>"}}}
```

成员不需要控制台，也不需要开放端口；其 Admin API 可以只监听回环地址。启动时它拨号连接管理端，按 `fingerprint` 检查管理端的
证书（不信任其他任何证书，令牌也绝不会发给别人），并证明持有令牌。令牌**只能使用一次**：管理端回以成员自己的凭据，只保存其
哈希（此前也只保存令牌的哈希）；成员把凭据保存在其 store 的 `cluster_member` 节中，没有 store 时保存在配置旁的文件里
（`fit.json` 旁的 `fit.cluster-member`，权限 0600）。此后成员用凭据连接，可以省略 `token`；配置中换上新令牌会让它重新注册。
双方都用[签发的连接令牌](decisions/035-link-tokens.zh-CN.md)的握手证明令牌或凭据，每个证明都绑定双方的随机数及方向。

连接由双向的 ping 保活，任一方 45 秒内收不到任何消息即关闭；连接结束后成员会重新连接，等待时间从 1 秒起加倍，最长 1 分钟。
连接可以经受管理端重启和网络中断。管理端经这条连接把请求发给成员的 Admin API，成员在进程内处理，使用连接自己的 2 个 worker，
最多 16 个排队（再多立即回答忙）；每个请求受 `timeout` 限制，其请求体和应答受 `max_response_bytes` 限制；管理端放弃的请求在
成员上被取消。一条连接上同时最多 16 个调用。成员把经连接做出的每个修改记为管理端所为，以此应用的配置来源为 `cluster`。

每个连接型成员的行显示**已连接**（及来源地址）或**等待中**：等待注册，直到令牌过期；或等待重新连接。其状态的 `link` 包含相同
信息（`linked`、`since`、`remote`、`host`、`enrolled`、`enroll_by`，以及最近被拒的 hello `refused` 及原因），没有连接的成员为
`down`，类型 `not_linked`。**新令牌**为连接型成员生成新的注册令牌，用于丢失凭据或迁移的成员：在它用新令牌注册之前，继续用原
凭据连接，注册后新凭据取代原凭据。其名称与标签可像其他成员一样编辑。

管理端拒绝的情况及成员随后的行为：

| 拒绝 | 原因 | 成员 |
| --- | --- | --- |
| `used` | 注册令牌已被使用 | 停止连接，直到配置了新令牌并重启 |
| `expired` | 令牌有效期已过 | 同上 |
| `unknown` | 没有成员持有该令牌或凭据：已被替换，或成员已删除 | 忘记其凭据并停止 |
| `proof` | 证明校验失败 | 同上 |
| `revoked` | 成员在管理端被删除，或已重新注册 | 忘记其凭据并停止 |
| `busy` | 管理端暂时无法保存凭据 | 重试 |
| `replaced` | 另一条使用同一凭据的连接取代了它 | 重试 |

在管理端删除的成员会失去其凭据：其连接以 `revoked` 关闭，成员忘记凭据，管理端也不再认识它。

### 经隧道

在 I149 之前，或成员已经运行隧道 node 时，也可以改为经[隧道](tunnel.zh-CN.md)访问成员。成员运行 `tunnel.node`，主动连接 hub，并把其控制台作为服务提供；管理端运行 `tunnel.proxy`，把一个
本地端口转发到该服务，并以该本地地址添加成员。hub 也可以就是管理端自身。

管理端在 `admin.cluster` 之外再运行 hub（带成员能访问的 TLS 监听）和 proxy（每个成员一个转发，各转发到一个回环端口）：

```json
{"admin": {"cluster": {}},
 "tunnel": {
   "hub": {"name": "hub", "listeners": [{"protocol": "tls", "address": ":7000"}]},
   "proxy": {"name": "manager",
     "hubs": [{"name": "hub", "endpoints": [{"address": "tls://127.0.0.1:7000"}], "token": "fitt1.<id>.<secret>", "pin": "<hub 的指纹>"}],
     "forwards": [{"listen": "127.0.0.1:12021", "node": "edge-7", "service": "console"}]}
 }}
```

成员的控制台监听回环地址，并由 node 提供：

```json
{"admin": {"console": {"address": "127.0.0.1:2020", "users": [{"username": "ops", "password_hash": "$2a$10$..."}]}},
 "tunnel": {"node": {"name": "edge-7",
   "hubs": [{"name": "hub", "endpoints": [{"address": "tls://manager.example.com:7000"}], "token": "fitt1.<id>.<secret>", "pin": "<hub 的指纹>"}],
   "services": [{"name": "console", "address": "127.0.0.1:2020"}]}}}
```

node 和 proxy 所用的令牌在 hub 的“令牌”页或以 `fit token create -role tunnel.hub -name edge-7` 签发，hub 的指纹显示在其页面上
（见[隧道的令牌](tunnel.zh-CN.md#令牌)）。然后在“节点”页以 URL `http://127.0.0.1:12021` 及其控制台的用户名和密码添加该成员：
允许 `http` 是因为管理端访问的是回环地址，从那里到成员的链路由隧道承载，为固定了 hub 证书的 TLS。控制台提供 TLS 的成员以
`https` 添加并同样固定其证书，这样 TLS 一直到成员本身。也可以改为转发成员的 Admin API，使用 `admin_api` 访问方式及其令牌。
