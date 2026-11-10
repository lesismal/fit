# 一致性检查

[English](10-consistency-check.md) | [简体中文](10-consistency-check.zh-CN.md)

## 功能

运行中的 fit 知道自己实际在运行什么：哪些监听绑上了、哪些角色在跑、哪些证书在被提供、哪些缓存区存在、引擎实际
用的上限，以及各 provider 提供了什么。它运行所依据的配置——生效配置——说它应该运行什么。两边可能不一样，这一页
把每一处差异都列出来，让每一处都被有意识地处理。

每处差异有一个种类：**不一致（drift）** 是可以通过改配置解决的，服务端为它给出一种或多种修复；**说明（info）**
是一个已知的运行事实，不是不一致，没有修复。

页面读的是 Admin API 的 `GET /config/check`，修复经 `POST /config/reconcile` 应用：选中的修复走的是与保存相同
的路径，所以它同样先校验、再作为一个新配置版本应用并写入 store。

## 页面与操作

### 一致性检查

工具栏显示运行中配置的版本、这次检查是什么时候做的，以及**重新检查**。下面在有不一致时是一个"几处不一致"的徽标，
有说明项时再加一个"几处说明"；没有不一致时，页面说"运行中的服务与其配置一致"，有说明项的话仍列在下面。

差异按类别分组，按这个顺序排：设置、监听、路由、上游、四层服务、TLS 证书、角色、缓存、配置来源；每组的标题带它
的项数。类别内按名字与 ID 排序。provider 提供的元素列在它所属成员自己的类别下（提供的监听列在“监听”下），被
provider 提供而未被采用的元素列在“配置来源”下。比较的两边是运行时——已绑定的监听、运行中的角色、已提供的证书、
缓存区、实际生效的引擎上限，以及 provider 提供的部分——与生效配置，即 provider 合并后的基础配置。

每处差异一行：种类的徽标、名字与说明，然后是它的两边——**配置**的值与**运行中**的值，各自格式化为 JSON。不一致
的行下面还有**修复方式**：一个下拉框选出一种修复，一个复选框决定要不要应用它。

修复的动作有四种：

| 动作 | 页面上的说法 | 做什么 |
| --- | --- | --- |
| `add` | 写入配置 | 把运行中的值写进配置 |
| `delete` | 从配置删除 | 把配置里多出来的那项去掉 |
| `replace` | 改为运行中的值 | 把配置改成实际在跑的值 |
| `reapply` | 重新应用配置 | 不改内容，再应用一次，以重试或停止它 |

下拉框里显示的是页面自己对动作的说法与它要写的路径（`写入配置 · add /listeners/1` 这样）；服务端自己的说法
（`label`）是 `fit check` 打印的那句。一处差异没有可给的修复时，页面会
说：此处没有可用的修复，请在相应页面修改配置，或重新应用配置。

页面底部的栏只应用勾选的项：**应用所选修复（N）** 打开一个确认窗口，列出每处的差异、类别与选中的修复；确认前不会
应用任何修改。确认后所选修复一起走同一条保存路径：先校验，再作为一个新配置版本应用并写入 store，所以任何一条
无法加载的修复都会让全部被拒绝；成功后提示"已应用为配置版本 V"，然后重新检查。如果自这次检查后配置被别处改过
（`412`），页面会提示重新检查再应用；`400`（差异已不存在，或已不再提供所选修复）显示在提示里，并重新检查。

每组的标题带类别名与它的项数（1 项 / N 项）；汇总的两个徽标是"几处不一致"与"几处说明"。下拉框默认第一项，
勾选后底部栏的计数随之变；一项都没勾时按钮是禁用的。

应用后页面会重新检查：把运行的值写回配置，那一处差异就消失；但若是启动时才生效的设置，你改的是配置里的值而
不是运行中的值，差异会留到下次重启——配置说新的值，运行中的服务仍用启动时的值。

助手开启时，每处差异旁边还有一个"问 AI"按钮，用技能 drift-explain 说明它（见[助手](14-assistant.zh-CN.md)）。

在集群设置了角色的管理端上（[集群](12-cluster.zh-CN.md#用户与审计)），只有集群范围的管理员才能对管理端自己的配置
应用修复；其他用户仍可以运行检查。

确认窗口里的三列是：**差异**（名字，名字与 ID 不同时下面附上 ID）、**类别**与**修复**。

检查在打开这一页时跑一次，之后每次点**重新检查**再跑一次；它不是自动刷新的。未知的类别照服务端的写法显示。

一处不一致的样子：

```text
不一致 · 设置 · log
  the config sets log differently from the value fit started with, which is in force until fit restarts
  配置:   {"output": "/var/log/fit/new.log", "level": "warn"}
  运行中: {"output": "/var/log/fit/fit.log", "level": "info"}
  修复方式: [改为运行中的值 · replace /log]   ☐ 应用此修复
```

（说明行是服务端给出的英文原文。）这里启动后改了 `output`（它要重启才生效）；级别由重新加载生效，它也出现在
两边，是因为两边都是整个成员：配置里的，以及 fit 启动时的。`replace` 修复把启动时的值整个写回，所以已在生效的
级别 `warn` 也会被改回去。

这一页用到的接口（Admin API 的接口在控制台上都在 `/api/m/admin/` 下）：

| 接口 | |
| --- | --- |
| `GET /config/check` | 运行中的版本、ETag、检查时间、差异与计数 |
| `POST /config/reconcile` | 选中的修复 `{"fixes": [{"id", "action"}]}`，带 `If-Match`（必须带，不带答 `428`） |

## 参数

差异的字段（`GET /config/check` 的每一项）：

| 字段 | 说明 |
| --- | --- |
| `id` | 稳定的名字，如 `setting:log` 或 `role:llm.hub`，跨检查一致 |
| `kind` | `drift` 或 `info` |
| `category` | 类别：settings、listeners、routes、upstreams、services、tls、roles、cache、providers |
| `name` | 差异的主体，按配置或运行中的写法 |
| `detail` | 一行说明 |
| `config` | 配置里的值（有值时） |
| `running` | 运行中的值（有值时） |
| `fixes` | 可选的修复：每项一个 `label`（服务端自己的说法）、一个 `action`，以及 `add`、`delete`、`replace` 时要写的 `patch`（`op`、`path`、`value`） |

## 差异的种类

| 种类 | 可能发现什么 |
| --- | --- |
| 启动时才生效的设置 | fit 启动后改过只在启动时读取的部分：`log` 任一日志器的 `output` 或 `format`；`access_log` 的 `output`、`format`、`flush_interval` 或 `buffer_bytes`；`telemetry`、`engine`、`providers` 的任何内容；`acme` 的账户、CA、存储、超时、验证方式或按需签发设置。配置说一套，运行中的服务仍用启动时的值。唯一的修复 `replace` 把整个成员写回 fit 启动时的样子（启动时没有该成员则是一个 `delete` 补丁）；也可以保留配置、等待重启。级别、`time_zone` 与 `rotate` 由重新加载生效，从不算不一致。`limits.max_conns`、`max_pending_bytes`、`max_write_queue_bytes` 在配置设置了它们时，同样与运行中的引擎比较。`admin`（地址、令牌、TLS 文件、history、节点 ID 与各上限）只作为 info：它来自引导文件 |
| 角色 | 配置设置了但没在运行的角色（启动失败）：用 `delete` 从配置中删除，或用 `reapply` 重试启动它。仍在运行但配置已不再设置的角色：用 `reapply` 停止它 |
| 配置来源的部分 | provider（`docker`、`consul`、`consul_kv`、`etcd`、`kubernetes`）加入生效配置、但不在基础配置中的元素：把它们写入配置，或继续由 provider 管理；它们列在所属成员自己的类别下。被遮蔽的 provider 元素属于 info，在“配置来源”下 |
| 监听 | 配置里的监听没有绑定属于不一致（基础配置里有它时可 `delete`，或 `reapply` 重试绑定）；绑定了配置中已没有的监听也属于不一致（`reapply` 停掉它）；监听地址为端口 `0` 时会报告内核选中的端口，属于 info（动态端口是有意为之） |
| TLS | 基础配置在某个监听的 `tls.acme` 中写了、但没有已提供的证书覆盖的名字属于不一致，修复（`delete`）是从配置中删除该名字；由默认 HTTPS 在证书签发期间加入的名字，以及没有任何名字要求而提供的证书（按需 TLS，或来自文件证书），属于 info |
| 上游 | 配置中的上游没有对应的池属于不一致（基础配置里有它时可 `delete`，或 `reapply`）；有池但配置中已没有该上游也属于不一致（`reapply` 停掉它）；某个上游的全部后端都已移出轮转属于 info |
| 缓存 | 某条路由使用的缓存区在运行时不存在，属于不一致（`reapply` 打开它）；从更早的配置保留下来、当前没有路由使用的缓存区属于 info |

检查的应答带 `summary`（不一致与说明的计数）与 `checked_at`（检查的时间），页面上的汇总与工具栏用的就是它；
运行中的版本与 ETag 也在应答里，`If-Match` 用的就是那个 ETag。

## 常见情形

- **改过启动时才生效的设置**：比如运行中改了 `log.output`、`access_log.format`、`engine` 或 `providers`。用
  `replace` 把启动时的值写回配置，或保留配置、重启 fit；两者都行，选哪个看你想让哪边为准。`replace` 写回的是
  整个成员，所以此后在该成员里做过的、由重新加载生效的修改（级别、`rotate`）之后要在它的页面上重新做一遍。
- **provider 提供的元素想固化到配置**：用 `add` 把它写进 `listeners`、`routes`、`upstreams` 或 `services`；
  不想固化就什么都不勾，让它继续由 provider 管。
- **角色启动失败**：用 `reapply` 再应用一次以重试；仍在跑但配置已不再设置的角色，用 `reapply` 停掉它——
  这里页面不提供别的修复。
- **只看不动**：不勾选任何项，这一页不会应用任何东西；确认窗口是最后一道。
- **应用后重新检查**：修复走的是与保存相同的路径，所以任何一条加载不了，全部被拒绝；成功的那次是一个新的配置
  版本，写入 store，`admin` 日志记下它。
- **这一页的检查与保存时的检查不同**：这一页比较的是运行中的服务与生效配置；其他页面上的**检查**按钮是让草稿
  走完加载与编译、试绑新监听地址，但不应用。两者互补：前者说现在跑的不一样，后者说草稿能不能应用。

- **在终端里逐项选修复**：`fit check -fix` 与页面上的下拉框加勾选是同一件事——每处选一种修复，最后一起应用；
  两者都走 `POST /config/reconcile`。

## 在终端里：fit check

`fit check` 经 Admin API 做同样的事：

```sh
fit check -c /etc/fit/fit.json          # 列出差异；有不一致时退出码 1
fit check -c /etc/fit/fit.json -json    # 以 JSON 输出检查结果
fit check -c /etc/fit/fit.json -fix     # 逐项选择修正，然后一起应用
```

`-c` 指定 fit 启动时用的引导文件，或一个装着它们的目录（不给时为 `fit.json`；可重复 `-c` 给出多个）。
`-timeout`（默认 30s）限定 `fit check` 等待 Admin API 的时长；`-json` 与 `-fix` 只给其中一个。

`-fix` 先列出差异，再对每处有修复的不一致逐一询问选哪个：输入编号，或 `s`（或直接回车）跳过。选中的修复带着
本次检查的 ETag 一起发送，打印结果（"applied config version V"），然后重新检查：

```text
fix [1] setting:log — choose 1) replace: revert the config to the value in force (replace /log), s) skip [s]: 1
fit check: applied config version 13
```

引导文件中的 `admin.address`（以及 `token_file`）指明 Admin API 的位置（不写时为 `127.0.0.1:2019`）；没有
`admin` 的配置无法检查。设置了 `admin.cert_file` 时，`fit check` 使用 https 并校验 API 的证书：不给 `-ca` 时
信任系统 CA，以及能读到时（如在 fit 所在主机上）`cert_file` 里的证书；给了 `-ca` 时用该文件里的 CA。校验的名称
取地址中的主机，主机未指定时为 localhost，也可用 `-server-name` 指定；设置了 `admin.client_ca_file` 时，必须用
`-cert` 与 `-key` 给出它要出示的客户端证书。

退出码：没有不一致为 `0`；仍有不一致（包括 `-fix` 之后）、`-fix` 一项都没选，或修复未能应用，为 `1`；用法、
配置加载、连接或解码错误为 `2`。

## .conf 配置

这一页不编辑配置成员，所以没有 `.conf` 示例：它读的是运行中的 fit 与生效配置，写的是选中的修复。修复走的是与
保存相同的路径，所以它同样受 `fit validate` 的那组检查——一条加载不了的修复会让全部被拒绝。

## 相关文档

- [控制台与 store](../console.zh-CN.md)：[检查运行中的配置](../console.zh-CN.md#检查运行中的配置)。
- [配置参考](../reference/config.zh-CN.md)：[The config](../reference/config.md#the-config) 与
  [Admin API](../reference/config.md#admin-api)。
- [配置](../configuration.zh-CN.md#修改运行中的配置)：应用一条配置走的路径。
- [JSON](08-json.zh-CN.md)：生效配置与来源；[版本](09-versions.zh-CN.md)：应用过的配置。
- 相邻章：[功能导航栏](03-navigation.zh-CN.md)、[概览](04-overview.zh-CN.md)、[日志](11-logs.zh-CN.md)。
