# 数据同步

[English](26-sync.md) | [简体中文](26-sync.zh-CN.md)

## 功能

数据同步是 fit 在网关旁运行的一个角色：它用同一个引擎，但不占用处理请求的 worker。一条**管道**（pipeline）从一个
**源**（source）读取**记录**（record），让记录经过**转换**（transform），再把每一条写入一个或多个**目标**（sink），
每个目标各有自己的批次。它相当于 fit 版的 Filebeat（对文件）与 Canal、Debezium、DataX（对数据库）
（[决定 036](../decisions/036-sync.zh-CN.md)）：各协议的收发都写在 fit 里，不引入驱动。默认关闭：配置中没有 `sync`
成员时不建立任何连接，也不在磁盘上留下任何状态。

记录与来源无关，形式始终相同：产生它的连接器、它的**命名空间**（`db.table`、索引、集合、文件路径、主题）、操作
（`insert`、`update`、`delete`、`ddl` 或 `message`）、**键**、变更前后的行（按类型保存的值，decimal 以文本保存、
从不舍入）、提交时间、它所属的事务以及源给出的**位置**（position）。转换可以修改、丢弃或拆分一条记录；编解码器
（codec）把源读到的字节或目标要写的字节与记录互相转换（`line`、`json`、`csv`、`raw`、`canal_json`、`debezium`、
`ticdc_simple`、`wasm`）。

一条管道只有一个**检查点**（checkpoint），即 `<state_dir>/<name>.json`：它保存**所有目标都已确认**的、且恰好结束
一个源事务的最后一条记录的位置，绝不会落在事务中间。检查点最多每 `checkpoint_interval`（5s）写一次，管道停止时也写
一次——先写临时文件，sync 后改名覆盖——管道从它启动。因此投递保证是**至少一次**：崩溃或 `kill -9` 之后，最后一个检查点
之后的记录会被再次读取并写入，不会丢失；目标则幂等写入（按键 upsert 和删除、按 `_id` 写文档、去重令牌、用一批的第一条
记录生成对象名），所以重放后状态一致。`mysql`、`postgres` 与 `mssql` 目标用 `checkpoint sink` 可以做到**恰好一次**：
把位置与其覆盖的记录写在同一个事务、同一张表里（[投递保证](../sync.zh-CN.md#投递保证)）。检查点还保存一个**谱系**
（lineage），一个为一段位置随机生成的 ID：重置、手工设置位置和新的作业都会开启新的谱系；按位置给所写内容命名的目标
（`doris` 的 label、`http` 的 `Idempotency-Key`）会把它放进名字里，这样重置后重读的记录会被再写一次，而不是当成重复
被丢弃。

一切都有限界：`queue` 限制已读取但尚未被所有目标确认的记录（10000 条、64 MiB），目标慢下来只会拖慢源，而不会让内存
增长；每个目标的 `batch` 在 `max_records`（500）、`max_bytes`（4 MiB）或首条记录之后 `max_wait`（1s）时关闭。写入
失败会重试 `retry.max_attempts`（5）次，每次等待翻倍；仍然失败的记录，或转换失败的记录，按 `on_error` 处理：
`stop` 停止管道、`skip` 跳过该记录，或 `dead_letter` 写入管道的 `dead_letter` 目标。用 `dead_letters` 目标时，这些
记录会保存在一个文件里，**死信**页可以列出并重放它们。源失败时，管道会从检查点重新开始，一直重试到源恢复。

`mode once` 的管道是**批处理作业**：它把源读到末尾，写入检查点后停止。能拆分快照的源会分块读取，一次 `job.max_parallel`
（4）块，同一个键总是交给同一个写入者；检查点保存已完成的分块，所以被中断的作业从这些分块继续，不必重读。已完成的作业在
fit 重启后不会再跑；`job.schedule`（cron 的五个字段，时区由 `job.time_zone` 给出）会让它再次启动，每次都是新作业，
且绝不会与仍在运行的作业重叠。

一条管道只在一台 fit 上运行：检查点是本地文件，因此把管道迁到另一台 fit 意味着停止它、复制 `<state_dir>/<name>.json`
与它的死信、再在新机器上添加——绝不能让一条管道在两台 fit 上同时运行，因为 PostgreSQL 的 slot 或 MySQL 的 `server_id`
属于这条管道。密码和令牌从不写进配置：连接器写出保存它的环境变量名（`password_env`、`secret_access_key_env`、
`bearer_token_env`、`headers_file`），在管道加载时读取；TLS 始终校验，不会被悄悄跳过。

配置成员是 `sync`：只有 `state_dir` 和 `pipelines` 两项——上限由每条管道的 `queue` 和 `batch` 决定，管道上报的
内容进入 fit 的指标与日志。这个模块的所有页面都在控制台的**共享草稿**中编辑该成员——所有编辑配置的页面共用同一份草稿——
在顶栏的**保存并应用**把整份草稿作为新的配置版本应用之前，修改不会生效（**检查**让修改走完整条应用流程但不应用，**放弃修改**
丢弃它们）。应用配置时只会重启设置发生变化的管道：启动新增的，停止被移除的（各自先写完手中内容与检查点），其余保持
运行、不受影响。被手工暂停的管道，在保留该管道的重载之后仍然暂停，fit 重启后则重新运行。保存发现的问题会链接到该管道的
**编辑**页，成员本身的问题则链接到**同步配置**页。

除特别说明外，连接器既可作源也可作目标，因此大多数组合都能连起来。文件（`file`）；数据库（MySQL、MariaDB 与 TiDB 用
`mysql`，TiDB 经 TiCDC 用 `tidb`——只能作源，TiDB 作目标时用 `mysql` 目标——`postgres`，经 CDC 或变更跟踪的 `mssql`，
`mongo`）；搜索与分析存储（Elasticsearch 与 OpenSearch 用 `elasticsearch`，`clickhouse`，Doris 与 StarRocks 用
`doris`）；队列与日志（Kafka 与 Redpanda 用 `kafka`、`mqtt`、`rabbitmq`、NATS JetStream 用 `nats`、`pulsar`、Redis 与
Valkey 用 `redis`、S3 及兼容存储用 `s3`）；`http`，作为路由上 `sync_ingest` 处理器背后的源，以及按批 POST 的目标；以下
只能作目标：可观测性存储（`loki`、`otlp_logs`、`influxdb`、`tdengine`）、向量数据库（`qdrant`、`milvus`，由 `embed`
转换算向量）与 `dead_letters`。用于试用的有源 `generate` 与目标 `memory`、`stdout`。源与目标分别是 `sync.sources` 与
`sync.sinks` 命名空间的模块，转换属于 `sync.transforms`（`filter`、`route`、`fields`、`key`、`cast`、`mask`、
`envelope`、`embed`、`wasm`），编解码器属于 `sync.codecs`；内置模块覆盖不到的部分，在这四处中任何一处都可以用
WebAssembly 插件实现。

`fit validate` 会像检查其余配置一样检查每条管道：所写的模块在其命名空间中存在，`state_dir` 已设置，每条管道和每个目标的
（包括 `dead_letter` 目标）名称合法且不重复，至少有一个目标，`on_error dead_letter` 有其 `dead_letter` 目标而其他策略
没有，`job` 只与 `mode once` 同时出现、其 `schedule` 与 `time_zone` 能被解析，并且 `retry.max_backoff` 不短于 `backoff`。

同样的管道也可以从页面之外观察和操作：Admin API 的 `/sync/…` 接口（状态、暂停与恢复、重新快照、检查点、连接测试、试运行、
死信），其上的 `fit sync`（`status`、`pause`、`resume`、`snapshot`、`checkpoint`、`test`、`dry-run`、`dead-letters`，
以及单独运行一条管道的 `run`），`fit_sync_*` 指标与 `sync` 日志器，还有助手的四个只读工具 `sync_pipelines`、
`sync_test`、`sync_dry_run` 与 `sync_dead_letters`，以及“问 AI”按钮所用的 `sync-pipeline` 技能
（[控制台的数据同步](../console.zh-CN.md#数据同步)、[功能指南](../sync.zh-CN.md#admin-apifit-sync-与工作台)）。

管道的运行情况与 fit 的其他角色一样可见。`sync` 日志器会记录管道何时启动（从检查点还是从源的起点）、等待排期、被暂停、
恢复、终止、完成或停止，以及它的错误——每条错误最多每 10 秒记录一次，并计数被压下的条数。`fit_sync_*` 指标携带页面显示的
内容——已读取、已确认与失败的记录数及字节数，错误数，队列，延迟与落后源端的量——按 `pipeline` 标注，各 sink 自己的再按
`sink` 标注。

## 页面与操作

数据同步在菜单中位于 LLM 角色之后、BitTorrent 之前，标签为“管道”，有两个页面：**管道**与**同步配置**。每条管道有四个
自己的标签页：**概览**、**编辑**、**检查点**与**死信**。每个页面都有自己的地址——`#/sync/pipelines`、`#/sync/config`、
`#/sync/pipeline/<name>`、`#/sync/edit/<name>`、`#/sync/pipeline/<name>/checkpoint` 与
`#/sync/pipeline/<name>/dead_letters`——因此可以直接链接到一条管道。该模块属于控制台，只要控制台
运行就出现在菜单中，即使其角色尚未配置；此时页面会说明应如何设置，配置应用后管道即启动。

### 管道

所有管道的列表，读取 `GET /sync/pipelines`，页面打开期间每两秒刷新一次，并保留最近 60 次轮询用于迷你走势图。每行一条管道：

- **管道**：名称，链接到它的**概览**；
- **状态**：`starting`、`running`、`paused`、`retrying`、`finished`、`stopped`、`scheduled` 或 `killed`；只要
  “暂停”按住了源，无论状态是什么都显示 `paused`；
- **吞吐**：迷你走势图，以及最近十秒内每秒已确认的记录数；
- **延迟**：迷你走势图，以及从记录的提交时间到它被确认之间的时长，随后是源报告的落后其服务器多远（源会报告时）；
- **已读取 / 已确认**：源已读取的记录数与所有目标都已确认的记录数；
- **错误**：管道启动以来的错误计数，以及最近的错误；
- **队列**：已读取但尚未被所有目标确认的记录相对 `queue.max_records` 的占用条；
- **死信**：`dead_letters` 目标保留的死信数，链接到该标签页。

草稿中存在但未运行的管道（尚未保存，或角色未开启）也会列出，带**未应用**标记；角色完全没有运行时，表格上方会说明这一点，
并提示**保存并应用**后即启动。草稿中也没有任何管道时，页面提示先用**新建管道**创建，然后保存并应用。

顶栏的**新建管道**要求填写名称（字母、数字、`_`、`.` 与 `-`，且不重复；建议值为 `pipeline-1`）、**源**（`sync.sources`
中所有模块，默认选中 `generate`）、**目标（sink）**（`sync.sinks` 中除 `dead_letters` 外的所有模块，默认选中 `memory`），
以及草稿中还没有 `sync` 成员时的**状态目录**（`sync.state_dir`；留空时为 `/var/lib/fit/sync`）。**创建**会把管道加入
草稿——包含所选的源和一个以其模块命名的目标，尚无参数——并打开它的**编辑**页：只有在**保存并应用**启动它之后，它才是真正
运行的管道。对话框按与 `fit validate` 相同的规则检查名称：由字母、数字、`_`、`.` 与 `-` 组成，以字母或数字开头，且未被占用。

### 同步配置

由 schema 生成的整个 `sync` 成员的表单——`state_dir` 以及每条管道的源、转换、目标、批次、重试、作业与死信——带有每个配置页
都有的 **UI**、**.conf**、**.yaml** 与 **.json** 标签页。四个标签页编辑同一份共享草稿：原始文本标签页的内容会解析进草稿，
**保存并应用**整体检查并应用。在还没有任何管道前设置 `state_dir`，或一次修改多条管道，都用这一页；保存时某条管道的问题
链接到该管道的**编辑**页，成员本身的问题链接到这一页。

### 概览

一条管道的主页，读取 `GET /sync/pipelines/{name}`，每两秒刷新，带它的四个标签页。页面显示状态标记、定时作业下次启动的时间，
以及最近的错误及其时间，并带一个**问 AI** 按钮，对这条错误运行 `sync-pipeline` 技能。接着是各项计数：**吞吐**、**延迟**、
**已读取**（含已读取字节）、**已确认**（含最近提交时间）、**错误**（含**失败**的记录数），以及源会报告时的**落后于源端**。
然后是相对上限的**队列**（记录与字节）；`mode once` 管道的**作业**卡片（开始与完成时间、已读取与已写入、失败数、已完成
分块与计划总数、剩余时间）；**Sink** 表格（每个目标的记录、字节、批次、重试、错误与失败记录数）；以及**死信**（已写入、未能
写入、已重放，以及相对上限保留的数量，链接到该标签页）。

页面上的按钮用来操作运行中的管道：

- **暂停**按住源，同时已读取的记录仍会写入、检查点仍会保存。管道处于暂停、`stopped` 或 `killed` 时，这个按钮变为
  **恢复**：让它继续，并重新启动因错误而停止的管道。
- **重新快照**会先确认：从头重新读取源，删除检查点并重启管道，于是目标会再次收到所有记录，覆盖已有内容。
- **测试连接**测试源、各目标与 `dead_letter` 目标的连接，不写入任何记录。结果卡片列出各个部分、其模块、结果（**通过**、
  **失败**、**无测试：请试运行**）与耗时；通过的测试若会丢数据，还会给出**警告**。
- **试运行**把前几条记录读出来经过各转换，显示每条读到的记录及其结果——被转换丢弃，或交给 sink 的记录——不写入任何内容，
  也不保存位置。它的表单设置记录条数（10，最多 1000）、等待时长（5s，最多 1m），在这一页还可以选择**从检查点**或**从起点**开始；
  页面会说明源已读完、等待先结束、或没有读到记录。试运行读取的记录与死信中的记录只显示给登录者，从不保存。

不运行的管道（不在运行中的配置里）会说明这一点，并给出到其**编辑**页的链接和保存并应用的提示。

### 编辑

管道的各个字段，由 schema 在其草稿位置生成：名称与 `mode`、源自己的字段与参数、按顺序的转换、每个目标自己的字段及其
`batch`，以及 `queue`、`retry`、`checkpoint_interval`、`on_error` 与 `dead_letter` 目标。与每个配置页一样，这一页
也有该管道在草稿中那部分的 **UI**、**.conf**、**.yaml** 与 **.json** 标签页。

页面上的提示说明修改进入共享草稿，**测试连接**与**试运行**检查的是当前草稿、无需先保存并应用（它们把管道的配置分别发送到
`POST /sync/test` 与 `POST /sync/dry-run`），因此一条管道在真正应用之前就可以试。被要求时，助手的页面工具会在同一张表单
里起草（page_get 读取，page_edit 写入，page_revert 撤回草稿），而**保存并应用**始终由人来点击。**移除管道**会先确认，然后
把它从草稿中移除：**保存并应用**后它将停止，其检查点文件保留，同名管道以后添加时可继续使用。

### 检查点

管道的检查点文件：**保存时间**、**谱系**（重置、手工设置位置或新作业都会开启另一个谱系）、作业的进度，以及文本框中的
**位置** JSON——格式与源所写的相同。页面在打开或点击刷新时读取它，而不是定时刷新，因为管道运行时检查点会移动。

**设置位置**在确认后保存编辑好的 JSON；确认框会说明它之前的记录会被跳过、或它之后的记录会被再次写入；管道停止并从该位置
重新开始。**重置**在确认后删除检查点；确认框会说明记录可能被再次读取并写入，管道按其源的参数重新开始。在第一个检查点保存
之前，页面会说明这一点，并且不提供重置。这就是 `fit sync checkpoint -c fit.conf <name>` 显示的那个检查点，`-set … -confirm`
与 `-reset -confirm` 会写入它，确认内容相同。

### 死信

`dead_letters` 目标保留的记录，最旧的在前，每次 50 条，用**更多**加载下一页；上方显示保留的条数、其字节数相对
`max_bytes` 的占用，以及文件路径。该文件默认是 `<state_dir>/<name>.dead_letters.jsonl`，目标的 `path` 可另行指定
（相对路径位于 `state_dir` 之下），每条死信一行 JSON，写入并同步之后才算已写入。每行有复选框、**ID**、**保存时间**、
失败的**位置**（某个 sink 或某个转换）、**尝试次数**、**错误**，以及**记录**：折叠时显示其命名空间，展开后是规范 JSON。
第一条错误旁有运行 `sync-pipeline` 技能的**问 AI** 按钮。该页在打开时读取，不定时刷新。

**重放所选**与**全部重放**会先确认（未勾选任何行时，“所选”按钮不起作用）；重放会停止管道，把每条记录按原样写入它失败的那个 sink——若失败者是转换，则先经过转换、
交给所有 sink——并覆盖该键之后的任何修改，然后丢弃写入成功的、保留并报告再次失败的，最后重新启动管道。**丢弃所选**与
**全部丢弃**同样会先确认，这些记录将丢失。`dead_letter` 目标不是 `dead_letters` 的管道没有可在这里重放的死信，页面会说明
应如何配置：`on_error dead_letter`，外加一个模块为 `dead_letters` 的 `dead_letter` 目标。命令行用
`fit sync dead-letters` 列出它们，其 `-replay -all` 与 `-drop` 与页面按钮作用相同。

## 一步步建立第一条管道

1. **数据同步 → 管道 → 新建管道**：初次试用保留 `generate` 与 `memory`，或选真正的源与目标；如有提示，填写状态目录。
   **创建**后打开**编辑**页。
2. 在**编辑**页填写源与目标的参数（地址、用户、`password_env` 环境变量名、表或路径），按需添加转换和更多目标，并设置
   `on_error`。
3. **测试连接**检查草稿中的源与目标；**试运行**显示前几条记录会变成什么。两者都不需要先保存草稿。
4. 点击顶栏的**保存并应用**（也可先**检查**）。管道启动后，其**概览**显示 `running`、吞吐与延迟。
5. 要从头再来，用**检查点 → 重置**，或概览上的**重新快照**；要暂时停下，用**暂停**。

连接器所写的环境变量在管道加载时从 fit 自身的环境中读取，因此要在启动 fit 的地方（其服务单元或容器）设置，而不是在控制台里：
变量未设置或为空时，`fit validate`、**检查**与**保存并应用**都会拒绝这条管道。

## 参数

这里只列关键字段；每个字段及其默认值见[配置参考：Sync](../reference/config.md#sync)，各模块的说明见 schema 的
[sync](../../schema/doc/sync.md)、[sources](../../schema/doc/sync_sources.md)、
[sinks](../../schema/doc/sync_sinks.md)、[transforms](../../schema/doc/sync_transforms.md) 与
[codecs](../../schema/doc/sync_codecs.md) 各章。

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `sync.state_dir` | — | 必填。`<name>.json` 检查点所在的目录，不存在时创建；把管道迁到另一台 fit 就是移动它的文件 |
| `pipelines[].name` | — | 必填。唯一；由字母、数字、`_`、`.` 与 `-` 组成，以字母或数字开头；检查点文件与死信文件都以它命名 |
| `pipelines[].source` | — | 必填。`sync.sources` 的一个模块及其 `params` |
| `pipelines[].transforms` | 无 | 按顺序排列的 `sync.transforms` 模块；每个都可以修改、丢弃或拆分记录 |
| `pipelines[].sinks` | — | 必填，至少一个。每个含名称、`sync.sinks` 的一个模块及其 `params`；每个 sink 都接收每一条记录 |
| `source.codec`、`sink.codec` | `line` | 读写字节的连接器上，字节与记录如何互相转换：`line`、`json`、`csv`、`raw`、`canal_json`、`debezium`、`ticdc_simple`、`wasm` |
| `pipelines[].mode` | `stream` | `stream` 长期跟随源；`once` 把源读到末尾，即批处理作业 |
| `pipelines[].on_error` | `stop` | 记录最终失败后的处理：`stop` 停止管道、`skip` 跳过，或 `dead_letter` 写入 `dead_letter` 目标 |
| `pipelines[].dead_letter` | 无 | 死信写入的目标；与 `on_error dead_letter` 搭配，且只在这种情况下使用 |
| `pipelines[].queue.max_records`、`.max_bytes` | 10000、64 MiB | 已读取但尚未被所有目标确认的记录上限 |
| `pipelines[].sinks[].batch.max_records`、`.max_bytes`、`.max_wait` | 500、4 MiB、1s | 批次何时关闭：达到记录数、字节数，或首条记录之后经过的时间 |
| `pipelines[].checkpoint_interval` | 5s | 检查点移动时最多每隔多久写一次；管道停止时也会写 |
| `pipelines[].retry.max_attempts`、`.backoff`、`.max_backoff` | 5、200ms、30s | 失败写入的尝试次数，以及每次之前的等待，逐次翻倍到最大值 |
| `pipelines[].job.max_parallel`、`.schedule`、`.time_zone` | 4、无、`UTC` | 作业同时读取的分块数，以及排期：cron 的五个字段（`0 3 * * *`）或 `@hourly`、`@daily`、`@weekly`、`@monthly`、`@yearly`，时区为 IANA 名称或 `Local`；仅用于 `mode once` |
| `dead_letters` 目标的 `path`、`max_records`、`max_bytes`、`when_full` | `<state_dir>/<name>.dead_letters.jsonl`、10000、64 MiB、`refuse` | 文件、保留的死信数量，以及文件满时是拒绝一条（`refuse`，管道停止，不丢数据）还是丢弃最旧的（`drop_oldest`） |

## .conf 配置

成员是一个 `sync` 块：先是 `state_dir`，然后每条管道一个 `pipelines` 块。下面这条管道跟随一个目录中的 JSON 日志行，把它
写入一个会轮转的文件；重试后仍然失败的记录进入死信。其中的路径与 `state_dir` 就是读者会照着写的值，fit 用户必须有写权限。

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines orders-log {
		source file {
			paths /var/log/shop/*.log
			codec json
			multiline { start "^\\{" }
		}
		sinks archive {
			sink file {
				path /var/lib/fit/orders.jsonl
				codec json
				rotate { max_bytes 104857600; max_files 10 }
			}
			batch { max_records 1000; max_wait 1s }
		}
		on_error dead_letter
		dead_letter { name errors; sink dead_letters { max_records 50000 } }
		retry { max_attempts 5; backoff 200ms; max_backoff 30s }
	}
}
```

`fit run -c fit.conf` 让它在网关旁运行。`fit sync run -c fit.conf orders-log` 单独运行这一条管道，不提供其他任何服务，
直到被中断，退出时保存检查点；加 `--once` 则把它当作作业读到末尾：所有记录都写入时退出码为 0，有记录失败（`skip` 或
`dead_letter`）时为 3，管道停止时为 1。第一次试用、没有文件可跟随时，下面的配置打印一千条记录后停止：

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines trial {
		mode once
		source generate { records 1000; tx_size 10 }
		sinks out { sink stdout }
	}
}
```

把一个数据库恰好一次地同步到另一个：先对 `shop.*` 做一次快照，再跟随 MySQL 的 binlog，用 `filter` 去掉删除操作，
upsert 到 PostgreSQL；其 `checkpoint sink` 把位置与这些行写在同一个事务里。密码来自 fit 环境中的 `MYSQL_PASSWORD` 与
`PG_PASSWORD`，必须设置，配置才能加载。

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines shop-to-pg {
		source mysql {
			address mysql.internal:3306
			username fit_repl
			password_env MYSQL_PASSWORD
			tls true
			server_id 4101
			tables shop.*
		}
		transforms filter { ops delete; action drop }
		sinks warehouse {
			sink postgres {
				address pg.internal:5432
				user fit_sync
				password_env PG_PASSWORD
				database warehouse
				checkpoint sink
			}
			batch { max_records 1000; max_wait 200ms }
		}
	}
}
```

MySQL 用户需要 `REPLICATION SLAVE`、`REPLICATION CLIENT` 与 `SELECT` 权限，`server_id` 在该服务器的副本中必须唯一；
目标把 `shop.orders` 写入 schema `shop` 下的表 `orders`。若改为每晚跑一次的复制，只需在同一条管道上加 `mode once` 与
`job { schedule "0 3 * * *"; time_zone Asia/Shanghai }`。

## 相关文档

- [同步](../sync.zh-CN.md)：记录模型、每个连接器及其对服务器的要求、系统之间的类型、检查点、
  [投递保证](../sync.zh-CN.md#投递保证)、插件、作业、指标、死信与运行维护。
- [控制台的数据同步](../console.zh-CN.md#数据同步)：控制台中的页面说明、助手的工具与技能，以及它可以发送给模型的内容。
- [配置参考：Sync](../reference/config.md#sync)：每个字段及其默认值。
- [决定 036](../decisions/036-sync.zh-CN.md)：这个角色的设计。
- [BitTorrent](27-bittorrent.zh-CN.md)：本模块之后的模块。
- [助手](14-assistant.zh-CN.md#技能)：工具分组、技能与 `sync-pipeline` 技能。
- [全局设置](07-global-settings.zh-CN.md)：共享草稿，以及包含 `sync` 的整份配置的导入与导出。
