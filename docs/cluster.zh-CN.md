# 集群管理

[English](cluster.md) | [简体中文](cluster.zh-CN.md)

一个 fit 可以管理多个 fit。设置了 `admin.cluster` 的 fit 是**管理端**：它保存其他 fit（**成员**）的地址和管理员凭据，读取每个
成员的状态，并在工作台的**集群**菜单中集中显示。管理端不在数据路径上：无论它是否运行，成员都按各自的配置提供服务；管理端宕机
或被移除，只会失去管理能力，不影响其他。设计与所做的选择见[决定 033](decisions/033-cluster-management.zh-CN.md)。

- [运行管理端](#运行管理端)
- [集群页面](#集群页面)
- [分组与操作](#分组与操作)
- [成员自己的页面：节点切换器](#成员自己的页面节点切换器)
- [模板：分组共享的配置](#模板分组共享的配置)
- [发布：分阶段应用的修改](#发布分阶段应用的修改)
- [漂移与告警](#漂移与告警)
- [指标、日志与证书](#指标日志与证书)
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
| `admins` | 无 | 无论角色如何都是集群管理员的控制台用户（[用户、角色与审计日志](#用户角色与审计日志)） |

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

## 模板：分组共享的配置

**模板**是管理端在其关联分组的成员上拥有的配置。它的写法与 fit 配置文件中的一部分相同，拥有它所写的内容：整个顶层成员
（`log`、`media_auth`、`access_log` 等），以及 `listeners`、`routes`、`services`、`upstreams` 中按名称逐个拥有的元素；模板中
这些列表的元素必须有名称，且名称不能使用变量，因为模板靠它在每个成员上找到该元素。`admin` 与 `schema_version` 属于成员自己。
成员的**期望配置**是其自身配置，再用其模板拥有的部分替换：顶层成员整体设置；元素在成员已有处替换，否则追加到列表末尾；其他
部分保持成员原样。

```json
{"upstreams": [{"name": "api", "addresses": ["${backend}"]}],
 "routes": [{"name": "api", "match": {"path": ["/api/*"]},
             "handler": {"module": "proxy", "params": {"upstream": "api"}},
             "middleware": [{"module": "headers", "params": {"response": {"set": {"X-Served-By": "${node.name} in ${node.labels.region}"}}}}]}]}
```

字符串中的**变量**按成员渲染：`${node.name}`（成员在管理端的名称）、`${node.id}` 与 `${node.hostname}`（按其 `GET /node`）、
`${node.labels.<key>}`，以及 `${<name>}`——模板所关联且包含该成员的分组的变量，模板分组列表中靠后的分组优先；`$${` 表示字面的
`${`。成员缺少某个变量时，其同步状态为错误并指出该变量，而不是渲染为空字符串。变量都是字符串：数字或布尔值在模板中直接书写。

**顺序。** 一个模板可关联任意多个分组，并有 `order`（-1000 到 1000）。成员的模板按 order、再按名称依次应用；两个模板拥有成员
的同一部分时，后者胜出，该部分会注明它覆盖了哪个模板。

**密钥**（名称为密码、secret、令牌、密钥等的成员，以及推流 URL）随模板保存在管理端的 store 中，应答时显示为 `[secret]`；以
`[secret]` 原样发回时，保留该位置（按成员名、元素名称或下标）已保存的密钥，其他值则替换它。从成员配置创建的模板保留其密钥，
且密钥不会到达浏览器。**文件路径**（`cert_file`、`token_file`、`..._dir`）会给出警告：每个成员从自己的磁盘读取它们。

**同步状态。** 读取成员时，逐部分比较其配置（`GET /config`）与其模板渲染的结果，并借助成员保存的管理端记录（见下文）判断
是谁修改了什么：

| 状态 | 原因 | 含义 |
| --- | --- | --- |
| `in_sync` | | 成员上的该部分与渲染结果相同 |
| `out_of_sync` | `missing` | 成员上还没有该部分 |
| `out_of_sync` | `template_changed` | 成员上的值仍是管理端上次看到同步时的值，而模板现在渲染出不同的值 |
| `conflict` | `changed_here` | 成员上的值既不是管理端上次看到同步时的值，也不是渲染结果：有人在成员上修改了它 |
| `conflict` | `removed_here` | 管理端曾看到同步的部分已不在成员上 |
| `conflict` | `own_value` | 成员上有自己的值，早于模板拥有该部分 |

成员的状态取其各部分中最差的；无法读取或模板无法为其渲染时为 `error`，没有关联模板时为 `unmanaged`。这里不会向成员应用任何
配置：期望配置由发布（I146）应用，由漂移（I147）持续检查。

**告知成员。** 管理端读取成员时，以及模板、分组或成员变化后不久，会通过成员自己的 `PUT /node/managed`（带 `If-Match`）告知
每个成员管理端在其上拥有什么：管理端的节点 ID 与主机，以及每个部分的模板和管理端上次看到其同步时的值的哈希。成员把它保存在
自己的 store 中（没有 store 时保存在内存中，直到管理端再次告知），并在 `GET /node/managed` 应答，每个部分附带此后是否在成员上
被修改（能力 `node.managed`）。成员自己的配置页面会标出这些部分（左侧色条与徽标，在成员上修改过的为琥珀色），修改前先询问：
聚焦其字段、点击其按钮、编辑原始文本标签页，或“保存并应用”提交未询问过的修改。不会拒绝修改：成员仍归自己，冲突由管理端报告。
`DELETE /node/managed` 让成员忘记管理端。

**页面**（**模板**之下）：

- **列表**：各模板的分组（不存在的分组为琥珀色）、顺序、拥有的部分，以及按同步状态计数的成员；**立即读取**读取全部成员；
  **添加模板**、**从节点导入**与**对比两个节点**。
- **模板页**：名称、说明、顺序与分组（附成员数）；配置的表单（由 schema 生成：每个顶层成员一张卡片，每个元素一个框，字段从
  列表中添加、点叉号移除；没有普通控件的值，如处理器，以 JSON 编辑），以及 `.conf`、`.yaml`、`.json` 文本（输入时即时转换）；
  其拥有的部分、变量及其来源、警告；以及其成员，每个成员的同步状态，每个部分成员上的值与渲染结果并排显示。
- **渲染结果**（`#/cluster/rendered/<member>`）：成员的同步状态，以及其配置与期望配置逐行并排。
- **对比**（`#/cluster/compare`）：两个成员的配置逐部分对比（相同、不同或只在一方），各自并排，以及整个配置。
- **从节点导入**：逐部分读取成员的配置（列表中未命名的元素、`admin` 与 `schema_version` 不能选择），把选中的部分创建为模板，
  并关联到选中的分组。关联到其来源成员时，该成员即为已同步。

## 发布：分阶段应用的修改

**发布**把一次修改应用到多个成员，每次一批，每批检查通过后才进行下一批。它可以来自：

- **模板的修改**：模板所在分组的每个成员（或所选成员）获得其期望配置，即自身配置中由模板拥有的部分替换为模板给出的值；
- **同步**：所选成员、某个分组的成员或全部成员，各自与其所有模板同步；
- **复制**：把一个成员当前配置的若干部分（顶层成员整体，`routes[api]` 等命名列表的元素按名称）连同密钥复制到其他成员。

**规划。** 规划会读取每个成员（`GET /node` 获取其能力，`GET /config` 及其 ETag），生成期望配置及其逐部分的差异（密钥已隐藏），
并由成员通过自己的试运行检查（带 `If-Match` 的 `PUT /config?dry_run=1`）：完整走一遍应用路径，编译并加载，但不应用。成员为
**就绪**、**无需修改**或**未就绪**并说明原因：成员的试运行拒绝了该配置（每个错误附其 JSON 指针）、无法读取、其模板无法为其渲染、
它是早于发布功能的 fit（必须列出 `config.if_match`、`config.dry_run` 与 `config.versions`）、它被另一个发布或操作占用，或规划保存
的配置与差异会超过 8 MiB。规划在管理端的 store 中（加密）逐字节保存每个就绪成员的期望配置、其所基于的 ETag、当时运行的版本，以及
成员试运行给出的新配置 ETag。规划后一小时内未启动即过期；同时最多 4 个发布处于规划或进行中。

**策略**在启动时选择：

| 策略 | 作用 |
| --- | --- |
| `all` | 全部成员为一批 |
| `rolling` | 每批 `batch_size` 个成员，或成员的 `batch_percent`，批次之间暂停 `pause_ms` |
| `canary` | 第一批为 `canary` 个成员（按名称排在最前）；其检查通过后发布**等待**人确认继续；之后其余成员按滚动方式分批，或一次全部 |

启动时可以排除成员。每个成员通过自己带 `If-Match`（规划时读到的 ETag）的 `PUT /config` 获得期望配置：规划后被修改的成员应答
`412`，被**跳过**，绝不覆盖。应答丢失的应用由成员的 `GET /node` 判断：运行的 ETag 正是试运行给出的，即已应用。

**健康检查**在每批应用后 `wait_ms`（默认 5 秒）进行，检查每个已应用的成员：可达；运行的是发布所应用的配置（其配置的 ETag），即重新
加载成功并保持；其一致性检查（`GET /config/check`）发现的不一致不多于发布前；设置了 `max_5xx_ratio` 时，批次之后 `window_ms` 内
5xx 应答的比例不超过它，自应答达到 `min_requests` 起判断（取自成员 `GET /status` 的 `requests`，成员配置设置了 `metrics` 时才统计；
批次中有不统计请求的成员时，在应用任何内容之前即失败）。应用被拒绝同样视为检查失败。检查失败会**停止发布**并回滚该批，或所有已应用
的成员（`on_failure: all`），每个成员通过自己带 `If-Match`（发布所应用配置的 ETag）的 `POST /rollback/{n}` 回到之前运行的版本：之后
被修改过的成员保持不变，并说明原因。

**操控。** **暂停**在当前批次之后生效；**继续**从该处继续；**继续发布**用于金丝雀之后；**中止**在正在应用的成员之后停止（缩短检查的
等待），保留已应用的内容；**回滚**在发布暂停、等待或结束后，回滚此发布应用过的每个成员。每个成员同时只有一个发布或操作：发布从启动
到结束占用其成员，期间对这些成员的操作与其他发布都会被拒绝。发布进行中管理端重启后，发布显示为**在停止处暂停**；继续后，正在应用的
成员由其 ETag 判断是否已应用，然后进行检查。

**保存。** 发布保存在管理端的 store 中，保留最近 50 个，每个都含规划、策略、批次以及每个成员的结果、版本（之前、应用、回滚）、所得
配置的哈希与检查结果；发布结束后丢弃配置与差异的值。审计日志记录每次规划、启动、暂停、继续、金丝雀后继续、中止、回滚与结束，结束时
附每个成员的结果。

**页面**位于**发布**下：历史（最新在前）与**新建发布**；新发布的表单（内容、模板、分组或成员、复制的部分）；发布页：规划完成时显示
每个成员的状态、并排的修改、试运行错误（链接到成员页、其自身页面与渲染结果）、排除成员的复选框、策略表单，以及**启动发布**（其对话框
列出各批次与每个成员的差异）；启动后逐批显示进度，每个成员的状态、版本与检查结果（发现的 5xx 比例与不一致，以及失败原因），并提供
**暂停**、**继续**、**继续发布**、**中止**与**回滚**。模板页有**发布**按钮，成员的渲染结果页有**同步此节点**。

## 漂移与告警

**漂移**指成员与管理端的预期不符，分两种：

- **相对于其模板**：某个部分未同步或冲突，或模板无法为其渲染，如[同步状态](#模板分组共享的配置)所述；
- **相对于其自身配置**：其一致性检查发现的、所依据的配置与实际运行之间的不一致（`GET /config/check`）。

一致性检查随每次轮询获得。关联了模板的成员在可能发生变化时于轮询之后与模板比对读取：还没有同步状态、其配置的 ETag（其
`GET /node` 给出的）不是同步状态所基于的那个，或同步状态已超过 10 分钟；模板、分组或成员的变化无论如何都会立即读取。这些读取
不通知成员任何内容，同时至多 `max_concurrency` 个、每个成员一次一个，且从不等待：没有空位的成员在之后的轮询中读取。

管理端从不自行应用任何内容。与模板未同步的成员上的**修复**为其规划一次同步发布（[发布](#发布分阶段应用的修改)）并打开其规划，
其中有每个部分的差异与成员的试运行；有人在该页启动后才会应用。一致性不一致通过对该成员的**重新加载**修复，这是一次需先确认的
操作，成员自己的一致性页面对此有说明。

**告警**依据**规则**。一条规则监视一件事，作用于全部成员或某个分组的成员，条件持续达到规则的时长（`for_ms`）后触发：

| 类型 | 触发条件 | 设置 |
| --- | --- | --- |
| `member_down` | 成员为 `down` 或 `error`（凭据被拒、证书不受信任），从其状态变化时起算 | |
| `drift` | 成员存在上述漂移 | `source`：`templates`、`consistency`，留空为两者 |
| `rollout_failed` | 发布以 `failed` 结束，每个发布一个告警，直到被回滚或结束后经过 `resolve_after_ms` | `resolve_after_ms`，0 为永不 |
| `cert_expiry` | 成员最早到期的证书在 `days` 天内到期 | `days`，默认 14 |
| `backends_down` | 成员的上游后端至少有 `threshold` 个不可用 | `threshold`，默认 1 |
| `reload_failed` | 成员最近一次重新加载失败（依据管理端首次读到之后的重新加载计数），直到一次成功、成员重启或经过 `resolve_after_ms` | `resolve_after_ms` |

管理端起初每种类型各有一条规则：成员离线 1 分钟后、漂移 5 分钟后、后端不可用 2 分钟后、证书 14 天内到期、发布失败与重新加载失败
立即触发；触发期间每 4 小时再次发送（`repeat_ms`，0 为不重复）。管理端启动后尚未读到的成员保持其告警不变。条件消失、成员被删除，
或规则被删除、关闭或修改时，告警**恢复**；保留最近 500 个。

**Webhook** 是告警的去处：规则发送到其列出的 Webhook，未列出时发送到全部 Webhook。每次评估中，每个 Webhook 收到一条新触发（或
再次发送）告警的通知与一条已恢复告警的通知；已发送过触发通知的告警恢复时会发送恢复通知。Webhook 有**格式**：

| 格式 | 消息体 |
| --- | --- |
| `json` | fit 自己的格式：`{"version", "source", "manager", "manager_host", "status", "test", "sent", "text", "alerts", "more"}`，每个告警与 API 应答的相同，至多 50 个，其余计入 `more` |
| `slack` | `{"text"}`：Slack 的 incoming webhook、Mattermost 与 Rocket.Chat |
| `discord` | `{"content"}` |
| `feishu` | 飞书机器人：文本消息，按飞书的方式以 Webhook 的密钥签名 |
| `dingtalk` | 钉钉机器人：文本消息，按钉钉的方式以密钥签名，签名在 URL 的查询参数中 |
| `wecom` | 企业微信群机器人：文本消息 |
| `custom` | Webhook 的模板，即 JSON：字符串中的 `${text}`、`${status}` 与 `${manager}`，以及作为值的 `${count}` 与 `${alerts}` |

聊天服务的消息为一行说明加上每个告警一行；Webhook 有模板时，模板即这一行，其中 `${severity}`、`${rule}`、`${kind}`、`${node}`、
`${subject}`、`${on}`、`${status}`、`${summary}`、`${since}` 与 `${id}` 会被替换。有密钥时，`json`、`slack`、`discord`、`wecom` 与
`custom` 带有 `X-Fit-Timestamp` 与 `X-Fit-Signature: sha256=<hex>`，即以密钥为键、对时间戳、点号与消息体计算的 HMAC-SHA256；
接收方重新计算并比较。Webhook 的 URL 与其密钥一样是机密，因为聊天服务的令牌就在 URL 中：两者都只写入一次，加密保存在管理端的 store
中，应答中只给出 URL 的协议与主机，从不记入日志；URL 须为 `https`，或仅限回环地址的 `http`。

Webhook 一次发送一条通知，每条在其超时内完成（默认 10 秒，1 至 60 秒），队列容纳 64 条，超出时丢弃最旧的；失败的通知不重发，
Webhook 的状态说明其发送情况。不应答的 Webhook 只阻塞它自己的队列：轮询器与评估器照常运行。聊天服务应答 200 但带错误码（飞书的
`code`、钉钉与企业微信的 `errcode`）视为失败。

**静默**使某条规则、某个成员或某个成员上的某条规则的告警在静默结束前（至多 30 天后）不被发送；告警仍会显示并标为已静默，不计入
菜单的徽标。从未发送过的告警在其静默结束后发送。

所有内容保存在 store 的 `cluster.alerts` 分区中，与成员的文档分开：规则、Webhook 及其 URL 与密钥、静默、正在触发与过去的告警。
审计日志记录规则、Webhook 与静默的每次修改，以及每次 Webhook 测试。

**页面**位于**漂移与告警**下（`#/cluster/alerts/<tab>`）：**漂移**，各成员的模板与一致性检查，可只显示有漂移的成员，**差异**（现读
成员，并排显示每个未同步部分在成员上与模板中的值）、**修复**、**修复全部未同步**、**手动规划**（预填的新建发布表单）与**重新加载**；
**告警**，计数、正在触发的告警（带**静默**）、等待中的与过去的告警；**规则**，添加、编辑与删除；**Webhook**，其发送情况、**测试**、
编辑与删除；**静默**，当前与过去的静默，添加与结束。有未静默的告警触发时，集群菜单上显示其数量，有严重告警时为红色。
## 用户、角色与审计日志

管理端的控制台用户在集群上有**角色**，分为集群范围和按分组：

| 角色 | 可以 |
| --- | --- |
| `viewer`（查看者） | 读取：成员及其状态、分组、模板、发布、操作、角色与审计日志；经节点切换器以只读方式打开成员自己的页面 |
| `operator`（操作员） | 还可运行操作（重新加载、回滚、一致性检查、清除缓存、测试）与已有模板的发布（模板的修改、同步），并操控它们；测试已保存的成员 |
| `admin`（管理员） | 还可添加、修改与删除成员及其凭据、生成注册令牌、测试草稿、经节点切换器写入、把一个成员配置的部分复制到其他成员；在**集群范围**内还可修改分组、模板以及角色本身 |

分组上的角色即其成员上的角色：用户在某成员上的角色，取其集群范围角色与该成员所在各分组上角色中最高的；一次修改只有在用户于其涉及的
**每个**成员上都具备所需角色时才被允许，按成员当前的样子，以及修改标签或名称时修改后的样子判断。分组、模板与角色决定谁在何处有何角色，
因此只有集群范围的管理员才能修改它们。列表（概览、节点、分组、模板、发布、审计日志）对在任意处有角色的用户可读；成员自己的页面、
对其的读取、渲染后的配置、对比与导入需要在该成员上有角色。

**漂移与告警**遵循同样的角色：漂移、告警、规则、Webhook（不含其密钥）与静默对任何角色可读；漂移的**修复**规划同步发布，需要在
每个成员上是操作员；静默需要在其成员上是操作员，规则作用于分组时为该分组上的操作员，否则为集群范围的操作员；规则、Webhook 及
Webhook 的**测试**需要集群范围的管理员。

**角色保存在哪里。** 角色与成员一起保存在管理端的 store 中，由集群范围的管理员在**用户与审计**页修改，立即对每个页面与请求生效。
`admin.cluster.admins` 列出无论角色如何都是集群范围管理员的控制台用户：在没有管理员时用来恢复访问，只能通过修改配置并重启来修改，
与 `admin` 的其他部分一样。角色未列出的控制台用户具有**默认角色**（未设置时为无）；列出但没有角色的用户仍然没有角色。

**设置角色之前**（store 中没有角色，也没有 `admins`），每个控制台用户都是集群范围的管理员，与引入角色之前相同，因此升级后的管理端
照常可用。首次保存的角色必须至少留下一个集群范围的管理员（或由 `admins` 指定）。

**管理端自身。** 设置角色后，管理端自己的页面，即其配置、控制台用户、store 与其运行的服务，只能由集群范围的管理员修改：其他人只能读取，
页面上方的横条会说明。修改自己的密码、文本转换与试运行仍对所有人开放。经节点切换器打开的成员，对不是其管理员的用户为只读
（`reason_kind` 为 `role`）。

**助手**（I126）为用户做的事从不超过用户本人可做的：在设置了角色的管理端，集群范围的管理员保留 `admin.assistant` 给出的级别；在任意处
是操作员的用户最多为 `propose`，由集群范围的管理员应用；其他人最多为 `read`。MCP 令牌也受同样的限制。用户页显示每个用户的级别以及受
角色限制后的级别。

**审计日志**记录每个集群操作：成员的添加、修改、删除、注册与测试；分组、模板与角色的修改；操作；发布的每一步；告警规则、Webhook 与静默的修改及 Webhook 的测试；以及经节点切换器的每次
写入。每个条目含用户、时间、成员、操作、结果（`ok`、`failed`、`partial`、已发送但未应答的 `unknown`、`not_sent`，或发布结束时的状态）、
前后版本以及修改内容：

| 条目 | 版本 | 差异 | 摘要 |
| --- | --- | --- | --- |
| 成员、分组、模板或角色 | 管理端文档的版本，`r12` → `r13` | API 所显示内容的差异：成员的凭据只显示是否设置（修改了密码会在详情中注明），模板的密钥被遮盖 | 完整差异的 SHA-256 |
| 操作 | 每个成员前后的配置版本，在其结果中 | 无 | 无 |
| 告警规则、Webhook 与静默 | 告警自己的版本，`a3` → `a4` | 规则、Webhook（按 API 所显示）与静默的差异 | 完整差异的 SHA-256 |
| 发布启动 | 每个成员的版本，以及其所得配置的哈希 | 每个成员的各部分，现有的与将得到的，密钥被遮盖 | 完整差异的 SHA-256 |
| 发布结束 | 每个成员之前的版本，以及应用或回滚后的版本 | 无 | 每个成员的配置哈希 |
| 经切换器的写入 | 最近一次读到的成员配置版本，以及其应答的版本 | 无：所发送的内容包含成员的配置与密钥 | 所发送请求体的 SHA-256 |

差异最多保留 16 KiB，超出部分截断，摘要针对完整差异。日志在 store 中保留最近 1,000 条、最多约 2 MiB，最旧的先删除，因此重启后仍在。

**页面**位于**用户与审计**下：**用户与角色**，列出每个控制台用户与角色中列出的每个用户、其集群范围角色、分组上的角色、助手级别、
默认角色以及**保存角色**（集群范围管理员可用；其他人只读）；**审计日志**，可按用户、成员、操作与时间筛选（筛选条件在 URL 中，成员页的
**此成员的修改**即链接到其条目），每个条目可打开其差异、摘要、版本与各成员结果，并链接到其发布，经切换器的写入则链接到成员自己的
“版本”页；**导出 JSON Lines** 保存所选条目。每个集群页面只绘制用户角色允许的控件，没有角色的用户会看到提示。

## 指标、日志与证书

集群菜单另有三个页面（I150）展示成员在做什么，数据来自管理端本来就读取的内容以及每个成员自己的日志：

- **指标**：管理端从对每个成员的每次轮询中保存一组序列，最近 360 次轮询（默认 `poll_interval` 下为一小时）：每秒的请求、4xx 与
  5xx（为与上次轮询之间的平均，来自成员 `/status` 的 `requests`，成员配置设置了 `metrics` 时才统计）；轮询读到的**连接数**与
  为客户端**排队的字节数**（`pending_bytes`，即 `limits.max_pending_bytes` 所限制的量）；以及与上次轮询之间应用成功与失败的
  配置数。一张图每个成员一条线，一张图每个分组一条线（其成员按轮询间隔相加），表格列出每个成员的走势及其启动以来的计数，
  这些计数就是成员自己的。成员重启之后、相隔超过三个间隔、或成员不统计请求时，该点没有速率。时间范围为 3 分钟到一小时；
  可按名称或 `a,b` 筛选成员；页面按管理端的轮询频率重新读取。
- **日志**：同时追踪所选成员（最多 16 个）的日志，按时间合并。每个成员的日志在内存中保留最新的 1,000 条消息（即助手的
  `log_tail` 读取的环），其 Admin API 现在通过 `GET /logs/recent?logger=&level=&contains=&limit=&after=`（能力
  `logs.recent`）提供，每条消息自 fit 启动起编号。管理端向每个成员请求其上次给出之后的消息，合并后连同记录每个成员最后编号的
  游标一起应答；**跟随**每 2 秒带着游标再次读取。重启的成员重新从 1 编号，会从头读取；离线、被拒绝或早于此版本的成员单独
  说明，其余成员照常读取。筛选：最低级别、日志器、文本；选择分组即选中其成员。页面保留最新的 2,000 行。
- **证书**：列出各成员最近一次轮询读到的证书：名称、签发者、序列号、日期与剩余天数，最早到期的在前，可按文本与剩余天数
  筛选；未提供证书或尚未读取的成员列在下方。
- **概览**：在每个成员的状态旁显示每秒请求的迷你走势图（成员不统计请求时为连接数）。

序列、证书清单与成员的最新计数保存在管理端内存中、成员的节点上：删除或重命名的成员会将其一并带走，管理端重启后重新开始。
除日志外，不向成员多要任何轮询之外的内容。序列与证书清单任何角色都可读取，与成员状态相同；成员的日志与其自身的读取一样，
需要在该成员上有角色（查看者）。

### 命令行：fit cluster

`fit cluster` 通过管理端的 **Admin API** 访问管理端，与 `fit check` 访问 fit 的方式相同：`-c` 指定引导配置，其 `admin` 说明
Admin API 的监听地址及其 `token_file`；`-ca`、`-server-name`、`-cert` 与 `-key` 用于基于 TLS 的 Admin API。管理端在其下的
`/cluster/` 提供端点。持有 Admin API 令牌的人可以修改管理端自己的配置，因此 Admin API 不论角色如何都作为集群范围的管理员
行事，审计日志将其记录为用户 `Admin API`（同名的控制台用户没有角色）。

```
fit cluster nodes                    成员：URL、访问方式与标签
fit cluster state                    各成员的状态；有成员不在线时退出码为 1
fit cluster groups                   分组及其成员
fit cluster diff [-node a,b | -template T]
                                     各成员与其模板不同的部分；有不同时退出码为 1
fit cluster certs [-within 30d]      证书清单；带 -within 时，有证书在此期间内到期则退出码为 1
fit cluster metrics [-node a,b] [-group g]
fit cluster logs -node a,b [-level warn] [-logger tls] [-contains x] [-limit N] [-f]
fit cluster rollout list
fit cluster rollout start -template T [-group G] [-node a,b] [策略] [-yes] [-wait]
fit cluster rollout start -sync [-group G | -node a,b] [策略] [-yes] [-wait]
fit cluster rollout start -id ID [策略] [-yes] [-wait]
fit cluster rollout status [-wait] ID
fit cluster rollout pause|resume|go|abort|rollback ID
```

`rollout start` 规划发布（或取用已规划的发布），打印每个成员的规划（其修改与试运行的错误），除非给出 `-yes`，启动前先询问；
带 `-wait` 时逐批跟随直到结束，或直到金丝雀等待 `fit cluster rollout go`。策略为 `-mode all|rolling|canary`、`-batch-size`、
`-batch-percent`、`-pause 30s`、`-canary N`、`-on-failure batch|all`、`-gate-wait 5s`、`-max-5xx 0.01`、`-window 30s`、
`-min-requests 20` 与 `-exclude a,b`，与发布页面的表单相同。发布失败、中止、回滚或过期时 `status` 的退出码为 1。每个命令都
接受 `-json`，输出 Admin API 的应答。

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
| `GET audit` | 审计日志，最旧在前，可按 `user`、`node`（所涉及的成员）、`action`（完整的 `op.reload` 或其类别 `op`）与 `since`/`until`（RFC 3339）筛选：`{"entries", "total", "max_entries", "max_bytes", "users", "actions"}`，每个条目为 `{"at", "user", "action", "node", "nodes", "group", "template", "rollout", "op", "detail", "outcome", "before", "after", "diff", "clipped", "digest", "outcomes"}`，每个结果为 `{"node", "status", "detail", "error", "before", "after", "digest"}`；从不含凭据；带 `format=jsonl` 时以 JSON Lines 文件返回条目 |
| `GET me` | 用户的角色，页面据此绘制：`{"user", "open", "fixed", "role", "top", "groups", "nodes", "manager_read_only", "assistant"}`，`role` 为集群范围的，`top` 为任意处最高的，`nodes` 与 `groups` 为在各成员与分组上的角色；任何控制台用户均可读取，无论有无角色 |
| `GET access` | 角色：`{"version", "open", "read_only", "admins", "default_role", "users", "groups", "roles", "max_users"}`，每个用户为 `{"user", "role", "groups", "console", "fixed", "assistant", "assistant_cap"}`，带 `ETag` |
| `PUT access` | 替换角色，需集群范围的管理员：`{"default_role", "users": [{"user", "role", "groups": {"<分组>": "<角色>"}}]}`；角色或分组不存在、用户重复，或不再有集群范围的管理员时返回 `400` 与 `{"field", "error"}` |
| `GET groups` | 分组列表 `{"version", "max_groups", "read_only", "groups"}`，带文档的 `ETag`；每个分组为 `{"name", "description", "selector", "members", "variables", "created", "updated", "nodes", "missing"}`，`nodes` 为当前属于它的成员，`missing` 为不是成员的名称 |
| `POST groups` | 添加分组：`{"name", "description", "selector", "members", "variables"}`；返回 `201`；字段错误返回 `400` 和 `{"field", "error"}`，名称已占用或已有 256 个分组返回 `409` |
| `GET groups/<name>` | 分组及其成员的 `states` |
| `PUT groups/<name>` | 替换分组，新的 `name` 即改名 |
| `DELETE groups/<name>` | 删除分组；其成员保持不变 |
| `POST select` | 选择器与名称当前选中的成员（无需分组）：`{"selector", "members"}`，应答 `{"nodes", "missing"}` |
| `POST ops` | 开始一次操作：`{"op", "group", "members", "purge"}`，`op` 为 `reload`、`rollback`、`check`、`purge` 或 `test` 之一；作用于分组的成员、`members`，或分组中的 `members`；`purge` 为 `{"zone", "keys", "prefixes", "tags", "all"}`；返回 `202` 与该操作；成员已在另一操作中返回 `409`，已有 4 次操作在运行返回 `429` |
| `GET ops` | 保存的操作，最新的在前 |
| `GET ops/<id>` | 一次操作：`{"id", "op", "group", "user", "purge", "started", "ended", "finished", "counts", "outcomes"}`，每个结果为 `{"node", "status", "detail", "error", "result", "started", "ended", "took_ms"}`，`status` 为 `pending`、`running`、`done` 或 `failed`，`error` 与状态中的问题相同 |
| `GET templates` | 模板列表 `{"version", "max_templates", "read_only", "templates"}`，带文档的 `ETag`；每个模板为 `{"name", "description", "groups", "order", "config", "created", "updated", "parts", "variables", "warnings", "nodes", "missing_groups"}`，`config` 中的密钥显示为 `[secret]`，`parts` 每项为 `{"path", "member", "name"}`，`nodes` 为其分组当前的成员 |
| `POST templates` | 添加模板：`{"name", "description", "groups", "order", "config"}`，或以 `"from": {"node", "paths"}` 代替 `config`，即成员配置中当前的若干部分（`"log"`、`"routes[api]"`）；返回 `201`；`400` 附 `{"field", "error"}`；名称已被占用或已有 128 个模板时 `409`；无法读取成员时 `502` |
| `GET templates/<name>` | 该模板 |
| `PUT templates/<name>` | 替换该模板，新的 `name` 即重命名；`[secret]` 保留该位置已保存的密钥 |
| `DELETE templates/<name>` | 删除该模板；其成员保留模板给出的配置，并被告知管理端不再拥有这些部分 |
| `POST sync` | 立即读取成员：`{"nodes"}`、`{"template"}`（其成员）或 `{}`（全部成员），并告知每个成员管理端拥有什么：`{"nodes", "pending"}`，每项为 `{"node", "state", "error", "errors", "checked", "config_etag", "templates", "parts", "counts", "released", "managed", "managed_error", "note"}`，每个部分为 `{"path", "member", "name", "template", "overrides", "state", "reason", "have", "want"}`，密钥已隐藏；30 秒内未读完的成员列为 `pending`；同时最多 4 个，再多返回 `429` |
| `GET sync` | 每个成员最近的同步状态，不含各部分的值 |
| `GET desired/<member>` | 立即读取的成员：其同步状态、`config` 与 `desired`，密钥已隐藏 |
| `POST compare` | `{"a", "b"}`：两个成员的配置，`{"a", "b", "parts"}`，每一方为 `{"node", "config_etag", "config"}`，每个部分为 `{"path", "state", "a", "b"}`，`state` 为 `same`、`differs`、`only_a` 或 `only_b` |
| `POST import` | `{"node"}`：成员配置的各部分，`{"node", "config_etag", "parts"}`，每项为 `{"path", "member", "name", "ownable", "why", "secret", "value"}` |
| `POST rollouts` | 规划发布：`{"kind", "template", "group", "nodes", "from", "paths", "description"}`，`kind` 为 `template`、`sync` 或 `copy`；返回 `202` 与规划中的发布；字段错误 `400` 附 `{"field", "error"}`，模板、分组或成员不存在 `404`，规划或进行中的超过 4 个 `429` |
| `GET rollouts` | 保存的发布，最新在前，不含差异的值：`{"max_active", "plan_ttl_ms", "read_only", "rollouts"}` |
| `GET rollouts/<id>` | 一次发布：`{"id", "kind", "template", "group", "from", "paths", "description", "planned_by", "started_by", "state", "reason", "strategy", "created", "planned", "started", "ended", "batch", "next_at", "batches", "members", "counts"}`；`state` 为 `planning`、`planned`、`running`、`paused`、`waiting`、`rolling_back`、`done`、`failed`、`aborted`、`rolled_back` 或 `expired`；成员为 `{"node", "status", "config_etag", "version", "new_etag", "digest", "applied", "rolled_back", "unsure", "changes", "changed", "errors", "warnings", "note", "error", "gate"}`，修改为 `{"path", "state", "sync", "reason", "template", "have", "want"}`（密钥已隐藏），检查结果为 `{"passed", "checked", "reasons", "notes", "version", "drift_before", "drift_after", "requests", "errors_5xx", "ratio"}`；从不包含配置 |
| `POST rollouts/<id>/start` | 启动规划：`{"strategy": {"mode", "batch_size", "batch_percent", "pause_ms", "canary", "on_failure", "gate": {"wait_ms", "max_5xx_ratio", "window_ms", "min_requests"}}, "exclude"}`；成员被其他发布或操作占用时 `409` |
| `POST rollouts/<id>/pause`、`resume`、`go`、`abort`、`rollback` | 操控发布；状态不允许时 `409` |
| `DELETE rollouts/<id>` | 放弃规划 |
| `GET drift` | 各成员的漂移：`{"interval_ms", "read_only", "nodes"}`，每个为 `{"node", "status", "groups", "managed", "templates", "consistency", "drifted", "fixable"}`，`templates` 为其不含部分值的同步状态，`consistency` 为其检查的计数 |
| `POST drift/fix` | `{"nodes"}`：为它们规划一次同步发布，与 `kind` 为 `sync` 的 `POST rollouts` 相同；`202` 附发布 |
| `GET alerts` | `{"read_only", "counts", "active", "pending", "history"}`，`counts` 为 `{"firing", "silenced", "pending", "critical", "warning", "info"}`（未静默的触发告警，按严重程度），告警为 `{"id", "rule", "kind", "severity", "node", "subject", "state", "summary", "since", "fired", "resolved", "resolved_why", "last_sent", "sends", "silenced_by"}`，`history` 最新在前 |
| `GET alerts/summary` | 只有计数，供菜单徽标使用 |
| `GET alerts/config` | `{"version", "read_only", "rules", "webhooks", "silences", "kinds", "formats", "severities", "bounds"}`，附告警的 `ETag`；Webhook 为 `{"name", "url_host", "secret_set", "format", "template", "timeout_ms", "disabled", "send_resolved", "status"}`，`status` 为 `{"sent", "failed", "dropped", "queued", "last_at", "last_ok", "last_status", "last_error", "last_took_ms"}` |
| `POST alerts/rules` | 添加规则：`{"name", "kind", "disabled", "group", "severity", "for_ms", "repeat_ms", "resolve_after_ms", "days", "threshold", "source", "webhooks"}`；`201`；`400` 附 `{"field", "error"}`，名称已占用或已有 64 条规则时 `409` |
| `PUT alerts/rules/<name>`、`DELETE alerts/rules/<name>` | 替换（新的 `name` 即改名）或删除规则；其告警恢复 |
| `POST alerts/webhooks` | 添加 Webhook：`{"name", "url", "secret", "format", "template", "timeout_ms", "send_resolved", "disabled"}`；`201`；名称已占用或已有 16 个时 `409` |
| `PUT alerts/webhooks/<name>` | 替换它；留空的 `url` 或 `secret` 保留已保存的，`clear_secret` 删除密钥；改名时其规则随之改名 |
| `DELETE alerts/webhooks/<name>` | 删除它；仍有规则引用时 `409` |
| `POST alerts/webhooks/<name>/test` | 向其发送测试，完成后应答：`{"ok", "status", "error", "took_ms"}` |
| `POST alerts/silences` | 添加静默：`{"rule", "node", "comment", "starts", "ends", "duration_ms"}`，规则、成员或两者；`201` |
| `DELETE alerts/silences/<id>` | 立即结束它；作为过去的静默保留 |
| `POST text/render?format=` | 把模板配置（JSON）写成 `conf`、`yaml` 或 `json` 文本 |
| `POST text/parse?format=` | 把这种文本读成 JSON，出错时 `400` 附错误 |
| `GET metrics` | 序列：`?node=a,b&group=g&points=90`（1 到 360；两者都未给出时为全部成员与分组）：`{"interval_ms", "points_max", "now", "nodes", "groups"}`，成员为 `{"name", "status", "groups", "counters", "series"}`，`counters` 为成员最近给出的 `{"at", "requests": {"total", "classes"}, "connections": {"open", "refused", "pending_bytes"}, "reloads": {"succeeded", "failed"}}`，分组为 `{"name", "nodes", "series"}`；序列按列给出 `{"t", "requests", "errors_4xx", "errors_5xx", "connections", "pending_bytes", "reloads", "reloads_failed", "members"}`，`t` 为 Unix 毫秒，速率为每秒，无值处为 `null`，分组的 `members` 为每点相加的成员数；任何角色 |
| `GET certificates` | 证书清单：`{"now", "certificates", "nodes"}`，证书为 `{"node", "listener", "names", "issuer", "serial", "not_before", "not_after", "days_left", "status", "read"}`，最早到期的在前，成员为 `{"name", "status", "count", "read"}`；任何角色 |
| `GET logs` | 追踪日志：`?node=a,b`（1 到 16 个，必填）、`level`、`logger`、`contains`、`limit`（每个成员最新的条数，默认 200，最多 1,000）与 `cursor`（上次应答的游标）：`{"records", "cursor", "nodes"}`，记录为 `{"node", "seq", "time", "level", "logger", "message"}`，按时间合并，成员为 `{"name", "count", "last", "error"}`；需要在所列每个成员上有角色；运行期间占用 8 个读取名额之一 |

在模块之外、控制台的顶层，是节点切换器的转发：

| 端点 | 作用 |
| --- | --- |
| `GET /api/n/<成员>/workbench` | 该成员可打开页面的模块：`{"user", "modules", "member"}`，`member` 为 `{"name", "access", "url", "id", "hostname", "fit_version", "schema_digest", "manager_schema_digest", "roles", "read_only", "reason_kind", "reason"}`，`reason_kind` 为 `version`、`not_read`、`manager` 或 `role`；此时读取其 `GET /node` |
| `GET /api/n/<成员>/node` | 成员的 `GET /node` |
| `<方法> /api/n/<成员>/m/<模块>/<路径>` | 成员的 `/api/m/<模块>/<路径>`（模块 `admin` 为其 Admin API 的 `/<路径>`），带查询参数；按成员的应答返回，应答头如上所述；无法访问成员时返回 `502` 与 `{"error", "problem"}`，不转发的接口返回 `403`，对只读成员的修改返回 `409` |

用户角色不允许的请求返回 `403` 与 `{"error", "kind": "forbidden", "user", "role", "nodes", "groups", "cluster"}`：所需的角色，以及缺少该角色的
成员或分组。带 `If-Match` 且其 ETag 已不是文档（成员、分组与模板）当前 ETag 的修改，以及告警的修改中已不是告警自身当前 ETag 的，返回 `412`。同时最多进行 4 个测试和 8 个读取，再多返回 `429`。读取只转发
成员的这四个 GET，不转发其他接口，从不写入；成员的能力列表中没有某个读取所需的能力（`status`、`config.versions`、
`config.check`）时不会向它请求。任何应答、页面或日志都不含密码、令牌或成员的会话 cookie。

管理端的 Admin API 在 `/cluster/` 下以其令牌提供相同的读取与发布操作，供 `fit cluster` 使用：`GET nodes`、`state`、`groups`、
`drift`、`metrics`、`certificates`、`logs`；`POST sync`（与模块的相同，但不通知成员）；`GET`/`POST rollouts`、
`GET`/`DELETE rollouts/<id>`、`POST rollouts/<id>/start`、`pause`、`resume`、`go`、`abort`、`rollback`，应答与模块相同。
不管理成员的 fit 上 `/cluster/` 返回 `404`。成员自己的 Admin API 新增 `GET /logs/recent?logger=&level=&contains=&limit=&after=`：
`{"records", "last", "ring_size"}`，即序号 `after` 之后的消息（`after` 超过最后一条时从头开始），最多 `limit` 条（默认 100，
最多 1,000），取最新的。

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
