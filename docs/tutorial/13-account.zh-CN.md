# 账户

[English](13-account.md) | [简体中文](13-account.zh-CN.md)

本章是教程的第 13 章，讲工作台的**账户**菜单：三个页面——**我的密码**、**用户**与**本节点**。它不编辑
配置（密码保存在 store 中，不在配置里），所以本章没有 `.conf` 示例；所涉及的字段在
[配置参考：Admin API](../reference/config.md#admin-api)，深入说明在
[控制台与 store](../console.zh-CN.md#用户与-fit-hash-password)。

## 功能

**账户**是控制台自己的模块（服务端模块 `admin`），在菜单中排在[集群](12-cluster.zh-CN.md)之后（见
[功能导航栏](03-navigation.zh-CN.md)）：它用控制台的登录与用户，不编辑任何配置成员。每个控制台用户都是
管理员，所以**用户**页上的重置会立即修改对方账户的密码，不需要任何额外的角色。

用户是一个名字和一个 bcrypt 哈希；密码从不以明文写入配置。`fit hash-password` 输出密码的哈希，它写入引导
文件的 `admin.console.users`，那是用户的初始哈希。`fit validate` 拒绝不是 bcrypt 哈希的 `password_hash`、
空用户名与重复列出的用户名；`-cost` 默认 10，每加一，检查耗时翻倍。页面上改出的密码保存在 store 中——
`admin.store`，或 `fit run -admin` 在配置旁生成的那个——与 store 的其余内容一起加密，重启后仍在，并取代该
用户在引导文件中的密码。浏览器从不看到哈希：页面提交的是明文，fit 哈希后才保存，所以修改后的密码生效于
登录。

账户只在控制台自己的菜单中：角色的页面（`llm.*` 与 `tunnel.*` 的 `console`）有各自的用户，经节点切换器打开
的成员菜单也不含账户页面。

新密码不能为空、最多 72 字节（bcrypt 的上限）。前两个页面先读取 `GET /api/users`：它给出会话属于哪个用户，
以及用户是否由 store 保存（`store: true`）；**本节点**读取 `GET /api/node`。没有 store 时无处保存：页面会
说明这一点并禁用表单与按钮，端点返回 `409`。

## 页面与操作

### 我的密码

修改自己的密码：**当前密码**必填，新密码最多 72 字节，并需确认两次输入一致。修改后，该账户的其他会话会
结束——调用者自己的会话保留。哈希在控制台的有界池上用 bcrypt 生成，写入 store。

页面提交 `POST /api/account/password`（`{current_password, password}`）。它检查什么：

| 情况 | 应答 |
| --- | --- |
| 缺当前密码或新密码，或新密码超过 72 字节、为空 | `400` 与原因 |
| 新密码与当前密码相同（临时密码的用户） | `400` |
| 临时密码已过期而未被修改 | `403`：需以 `-admin` 重启 |
| 没有 store 保存用户 | `409` |
| 当前密码错误 | `403`，失败按地址计数 |
| 该地址 15 分钟内失败 5 次 | `429` 带 `Retry-After`，此后 15 分钟拒绝 |

密码检查像登录一样被接纳：同一地址同时只检查一次，每次失败都计入地址与用户，同时最多进行两次检查；不存在的
用户名用一个虚设的哈希检查，使耗时无法区分用户名。每次修改都记录到 `admin` 日志。

会话本身在内存中，以其令牌的哈希为键，所以 fit 重启后所有人都要重新登录。修改密码会把新哈希写入 store 并
结束该账户的会话；重启则两者都不改。

### 用户

列出各控制台用户（当前登录的用户标为**当前**），每个可**重置密码**：新密码与其确认，弹窗说明每个控制台用户
都是管理员，重置会立即修改该账户的密码并结束其会话。提交 `POST /api/users/password`（`{username, password}`）；
用户名不存在时 `400`。同样需要 store。这里不修改任何配置：用户与其初始哈希仍在引导文件的
`admin.console.users` 中，改出的密码在 store 里。

在管理端上，经节点切换器打开的成员页面不转发这些接口：成员的用户与密码只能在成员自己那里修改，管理端从不
修改成员的密码（见[集群](12-cluster.zh-CN.md)）。

### 本节点

读取 `GET /api/node`，四张卡片说明本 fit 是谁：

| 卡片 | 内容 |
| --- | --- |
| **节点** | **节点 ID**（旁边有复制按钮）与其**保存位置**：配置（`admin.node_id`）、store（`admin.store`）、配置旁的文件（`fit.node-id`），或仅内存（每次重启后会生成新的 ID，以警告徽标显示）；主机名、启动时间与运行时长 |
| **版本** | fit、fib 与 Go 的版本，以及平台（`os/arch`） |
| **运行中的配置** | 版本、ETag、应用时间、来源，以及**运行中的角色** |
| **供管理端使用** | 配置 schema 的版本与摘要（`schema_digest`），以及**能力**列表 |

节点 ID 只生成一次（一个随机 UUID）并保存在 store 或配置旁的文件中，管理端按它认识这个节点：换一个新的 ID，
管理端就把它当作另一个节点。

能力列表决定管理端能向它读什么、能在它上运行哪些操作：没有 `config.check` 时不会请求它的一致性检查，没有
`config.versions` 时不能回滚，没有 `logs.recent` 时日志页会说明它不提供。`schema_digest` 与管理端不同的成员
（运行的是另一版本的 fit）在节点切换器下以**只读**方式打开。`GET /node` 正是管理端每隔 `poll_interval` 读取
的东西，所以本节点页与管理端的**概览**给出的是同一份事实。

### 三个页面用到的接口

| 接口 | 作用 |
| --- | --- |
| `GET /api/users` | 会话属于哪个用户、是否有 store 保存用户，以及用户名列表 |
| `POST /api/account/password` | 修改自己的密码（需当前密码） |
| `POST /api/users/password` | 为另一个控制台用户设置密码 |
| `GET /api/node` | 本节点：ID 与保存位置、主机、版本、启动时间与运行时长、运行中配置、角色、schema 摘要与能力 |

## 参数

本页不编辑任何配置成员；所涉及的只有保存用户与密码的 `admin.console.users` 与 `admin.store`，其字段表在
[配置参考：Admin API](../reference/config.md#admin-api)（[中文说明](../reference/config.zh-CN.md)）。

## 相关文档

- [控制台与 store](../console.zh-CN.md#用户与-fit-hash-password)：用户、`fit hash-password` 与默认密码的流程。
- [登录](02-login.zh-CN.md)：会话、锁定与减速；修改密码属于本页。
- [部署](01-deployment.zh-CN.md)：store 与 `fit run -admin`。
- [集群](12-cluster.zh-CN.md)：管理端如何按能力与 `schema_digest` 检查成员。
- [配置参考：Admin API](../reference/config.md#admin-api)：`admin.console`、`admin.store` 与 `GET /node`。
- 前端代码：[app/workbench/web/account/account.js](../../app/workbench/web/account/account.js)。
