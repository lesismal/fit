# 集群管理

[English](cluster.md) | [简体中文](cluster.zh-CN.md)

一个 fit 可以管理多个 fit。设置了 `admin.cluster` 的 fit 是**管理端**：它保存其他 fit（**成员**）的地址和管理员凭据，读取每个
成员的状态，并在工作台的**集群**菜单中集中显示。管理端不在数据路径上：无论它是否运行，成员都按各自的配置提供服务；管理端宕机
或被移除，只会失去管理能力，不影响其他。设计与所做的选择见[决定 033](decisions/033-cluster-management.zh-CN.md)。

- [运行管理端](#运行管理端)
- [集群页面](#集群页面)
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

设置了 `admin.cluster` 时，工作台左侧菜单显示**集群**，含两个页面和每个成员的页面，提供中英文。

**概览**以卡片或表格显示管理端最后一次读到的各成员状态：着色的状态（绿色正常、琥珀色错误、红色离线、灰色尚未读取）及其持续
时长与原因、fit 版本、运行配置的版本、一致性检查统计的不一致数、最早到期的证书及剩余天数、连接数与不可用的上游后端，以及其
`GET /node` 的延迟。离线的成员保留其最后一次的应答，并注明时间。磁贴按状态计数，点击即按该状态筛选；筛选还可按文本（名称或 URL）、
标签选择器（`env=prod, region, tier!=db`：每一项都须满足，单独的 `key` 表示存在该标签）与状态，排序可按状态（严重的在前）、
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
时不读取。从管理端打开成员自己的配置页面是接下来计划中的节点切换器（I143）。

## 如何访问成员

成员是管理端能访问到的 fit，访问方式有两种：

- **`console`**：成员控制台的用户名和密码。管理端以 `POST /api/login` 登录，保存会话的 cookie 和 CSRF 令牌，经控制台读取 Admin
  API（`/api/node`、`/api/m/admin/status`、`/api/m/admin/config/check`），调用返回 401 时（例如成员重启或会话到期）重新登录。
  登录被拒后，除非修改或测试该成员，一分钟内不再发送，之后每次加倍，最长 15 分钟，因此错误的密码不会把成员的用户锁在外面；
  被成员限速的登录按其 `Retry-After` 等待。要求修改临时密码的控制台会被报告，其会话随即登出：管理端从不修改成员的密码。
- **`admin_api`**：成员 Admin API 的 bearer 令牌，直接访问 Admin API 的地址（`/node`、`/status`、`/config/check`）。

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
| `GET state` | 所有成员的状态：`{"interval_ms", "timeout_ms", "read_only", "nodes"}` |
| `GET audit` | 管理端的修改记录：谁、何时、哪个成员、改了什么（凭据只记字段名） |

带 `If-Match` 且其 ETag 已不是成员列表当前 ETag 的修改返回 `412`。同时最多进行 4 个测试和 8 个读取，再多返回 `429`。读取只转发
成员的这四个 GET，不转发其他接口，从不写入；成员的能力列表中没有某个读取所需的能力（`status`、`config.versions`、
`config.check`）时不会向它请求。任何应答、页面或日志都不含密码、令牌或成员的会话 cookie。

## NAT 后面的成员

管理端无法直接访问的成员（位于 NAT 或防火墙之后，没有开放端口）在成员能主动连接管理端之前（计划中，I149）经
[隧道](tunnel.zh-CN.md)访问。成员运行 `tunnel.node`，主动连接 hub，并把其控制台作为服务提供；管理端运行 `tunnel.proxy`，把一个
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
