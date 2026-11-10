# 同步

[English](sync.md) | [简体中文](sync.zh-CN.md)

同步把数据从生产者搬到消费者：流水线从一个**源**（文件、数据库的变更、主题、索引、存储桶、HTTP 端点）读取记录，
经过**变换**，写入一个或多个**目标**，就像 Filebeat 之于文件、Canal、Debezium 或 DataX 之于数据库
（[决定 036](decisions/036-sync.zh-CN.md)）。fit 支持的每种基础设施既可作源也可作目标，任意两者都能连接；流水线可以
一直跟随它的源（流），也可以读完一遍就停（批处理作业）；内置连接器覆盖不到的，用 [WebAssembly 插件](#插件)补上。
各线协议（MySQL 及其复制流、PostgreSQL 及其逻辑复制、MongoDB、Kafka、Redis、S3 的签名）都在 fit 中实现，不引入任何驱动。

默认关闭：没有 `sync` 时，不建立任何连接，也不向磁盘写任何东西。

本页讲解概念、每个连接器及一份可用的配置、每个源对其服务器的要求、类型如何在系统之间转换、插件、作业、Admin API、
`fit sync` 和工作台、指标与死信，以及流水线的运维。[配置参考](reference/config.md#sync)列出所有字段，
[fit-examples](https://github.com/lesismal/fit-examples) 仓库中有
[文件到 Elasticsearch](https://github.com/lesismal/fit-examples/tree/main/sync-files-to-elasticsearch)、
[MySQL 到 PostgreSQL](https://github.com/lesismal/fit-examples/tree/main/sync-mysql-to-postgres)、
[PostgreSQL 到 Kafka](https://github.com/lesismal/fit-examples/tree/main/sync-postgres-to-kafka)、
[RabbitMQ 到 Kafka](https://github.com/lesismal/fit-examples/tree/main/sync-rabbitmq-to-kafka)、
[PostgreSQL 到 NATS](https://github.com/lesismal/fit-examples/tree/main/sync-postgres-to-nats)、
[Pulsar 到 Elasticsearch](https://github.com/lesismal/fit-examples/tree/main/sync-pulsar-to-elasticsearch)、
[MQTT 到 InfluxDB](https://github.com/lesismal/fit-examples/tree/main/sync-mqtt-to-influxdb)、
[MQTT 到 TDengine](https://github.com/lesismal/fit-examples/tree/main/sync-mqtt-to-tdengine)、
[SQL Server 到 Kafka](https://github.com/lesismal/fit-examples/tree/main/sync-mssql-to-kafka)、
[MySQL 到 Doris](https://github.com/lesismal/fit-examples/tree/main/sync-mysql-to-doris)、
[文件到 Loki](https://github.com/lesismal/fit-examples/tree/main/sync-files-to-loki)、
[PostgreSQL 到向量数据库](https://github.com/lesismal/fit-examples/tree/main/sync-postgres-to-vectors) 和
[WASM 变换](https://github.com/lesismal/fit-examples/tree/main/sync-wasm-transform)的示例。

## 第一条流水线

`generate` 生成涵盖所有值类型的编号记录，`stdout` 把它们打印出来，所以这个配置在哪里都能运行：

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

`fit sync run -c fit.conf trial` 单独运行它，并以其结果退出；`fit run` 则在提供配置中其他服务的同时运行它。一条做实际
工作的流水线，把日志文件发送到 Elasticsearch：

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines app-logs {
		source file { paths /var/log/app/*.log; codec json }
		sinks search {
			sink elasticsearch {
				urls https://es1:9200 https://es2:9200
				username fit
				password_env ES_PASSWORD
				index app-logs-{yyyy.MM.dd}
			}
			batch { max_records 1000; max_wait 1s }
		}
	}
}
```

## 概念

### 记录

每个源产生、每个目标接收的都是同一种记录，因此任何连接器的类型都不会泄漏到另一个连接器：

| 部分 | 内容 |
| --- | --- |
| `source` | 读到它的连接器：`mysql`、`postgres`、`file` …… |
| `namespace` | 它来自哪里：`db.table`、`schema.table`、索引、集合、文件路径、主题 |
| `op` | `insert`、`update`、`delete`、`ddl`（模式变更，只报告）或 `message`（一行、一个事件） |
| `key` | 标识这一行的东西：主键各列组成的 map、文档的 `_id`、消息的键 |
| `before`、`after` | 变更前后的行，均为类型化的值；删除只有 `before`（或只有键） |
| `commit_time` | 源提交它的时间，源知道时才有 |
| `tx` | 所属事务的 ID，以及它是否为该事务的第一条或最后一条记录 |
| `position` | 源在它之后继续读的位置：不透明的 JSON，由源自己定义 |

值的类型有 null、bool、int、uint、float、decimal（以文本保存，从不舍入）、string、bytes、time（带时区偏移）、date、
JSON、array 和 map，map 保持字段顺序，就像行保持列的顺序。变换、插件、`stdout` 目标和死信看到的是记录的规范 JSON，
它能无损往返每种类型：null、布尔、字符串和数组用 JSON 自身的形式，其他类型都写成一个以类型命名的单成员对象。

```json record
{"source": "mysql", "namespace": "shop.orders", "op": "update",
 "key": {"map": {"id": {"int": 7}}},
 "before": {"map": {"id": {"int": 7}, "status": "paid", "total": {"decimal": "12.30"}}},
 "after": {"map": {"id": {"int": 7}, "status": "shipped", "total": {"decimal": "12.30"},
   "shipped_at": {"time": "2026-11-09T08:00:00.123456Z"}}},
 "commit_time": "2026-11-09T08:00:00Z", "tx": {"id": "3E11FA47-71CA-11E1-9E33-C80AA9429562:23", "first": true, "last": true}}
```

### 流水线

一条流水线由一个源、按顺序的变换和一个或多个目标组成，有一个检查点。源把记录送入队列；每条记录依次经过变换，变换可以
修改、丢弃或拆分它；每个目标按顺序接收每条记录，按自己的批次写入，写完一批就确认这一批。目标只在同一个键的记录保持顺序
的前提下并行写不同的键。

- **处处有界。** `queue` 以记录数（10,000）和字节数（64 MiB）限制已读取但尚未被所有目标确认的记录：超过时源等待，
  所以慢目标让源变慢，而不是让内存增长。每个目标的 `batch` 在达到 `max_records`（500）、`max_bytes`（4 MiB）或第一条
  记录到达后 `max_wait`（1s）时结束，以先到者为准。
- **不占用 fib 的工作协程。** 每条流水线运行在自己的几个 goroutine 上；基于 HTTP 的系统经 fib 的异步 HTTP 客户端访问，
  数据库连接少而长，每个连接一个 goroutine。同步中没有任何东西在服务请求的工作协程上等待。
- **流或一次。** `mode stream`（默认）一直跟随源；`mode once` 读到源的末尾就停：即[批处理作业](#批处理作业)。
- **重载。** 重载会启动新增的流水线，停止移除的流水线（各自先写完手中的记录和检查点），只重启设置改变了的流水线；其余的
  不受影响地继续运行。

### 检查点

流水线的检查点是 `<state_dir>/<name>.json`：所有目标都已确认、且结束一个源事务的最后一条记录的位置，从不落在事务内部，
也从不越过尚未确认的记录。它至多每 `checkpoint_interval`（5s）写一次，并在流水线停止时写，先写临时文件，同步后改名就位；
流水线从它开始。无法解析的检查点文件会让流水线无法启动，而不是让它从头开始。

服务器为其读者保留数据的源，只在文件已保存位置之后才被告知该位置：PostgreSQL 的复制槽被告知某个 LSN 后，服务器就会
丢弃其之前的 WAL，所以只在检查点保存之后才告知。Kafka 的 offset、Redis 的条目 ID 和 MongoDB 的 resume token 也保存在
检查点中：fit 不加入消费者组，也不向 broker 提交任何东西。

一条流水线只在一个 fit 上运行：它的检查点是本地文件。把流水线移到另一个 fit，就是（在流水线停止时）移动它的文件。

**谱系。** 检查点还保存一个谱系（lineage）：流水线首次启动时生成的随机 ID，重置检查点、手动设置位置和新的作业（从头再跑
的作业）都会生成新的；重启保留它。服务器会丢弃按位置命名、已收到过的内容的目标，把它放进名字里：`doris` 的 label 和
`http` 的 `Idempotency-Key`，这样重置后重新读取的记录会再次写入，而不会被当作重复丢弃（见[投递保证](#投递保证)）。比谱系
更早的 fit 保存的检查点没有谱系，其目标的名字保持原样，直到它被重置。

**安静的表。** 源的位置随它自己的表的记录移动。从繁忙的日志（binlog、WAL、SQL Server 的变更表）读取一张安静的表时，
检查点会停在旧位置而服务器继续向前；服务器丢弃了那段日志之后（binlog 被清理、CDC 的清理保留期已过）重启，就会失败并
要求快照，尽管这张表什么都没丢。所以 `mysql`、`postgres` 和 `mssql` 源在读过一段其他表的变更后，会交出只含位置的记录：
它前面的记录写完后检查点就移到它，转换和目标都看不到它，也不计入读取或写入的记录数。每个 `checkpoint_interval` 至多取
一条，且只在这段时间内没有任何记录时才取，所以繁忙的表靠自己的记录移动检查点。`postgres` 源还会在事务之间把位置告诉
复制槽，使服务器不再为其他表保留 WAL。

### 投递保证

**至少一次。** 崩溃、重启或 `kill -9` 之后，最后一个检查点之后的记录会被重新读取、重新写入；检查点之前的不会丢失，因为
检查点从不越过所有目标都已拿到的位置。因此目标以幂等方式写入，重放后收敛到同一状态：

| 目标 | 记录再写一次的效果 |
| --- | --- |
| `mysql`、`postgres` | 按键 upsert 和 delete：行相同 |
| `mongo` | 按 `_id` upsert 和 delete：文档相同 |
| `elasticsearch` | 按由键生成的 `_id` 索引和删除：文档相同（没有键的记录由集群生成 `_id`，重放会重复） |
| `clickhouse` | 相同记录得到相同的去重令牌：服务器丢弃这次插入；否则由 `ReplacingMergeTree` 按版本合并 |
| `doris` | 相同记录（同一谱系）得到相同的 label：服务器以“已导入”拒绝这次导入；否则由 Unique Key 或主键表按键覆盖 |
| `loki` | 相同的条目（流、时间、行）只保留一份；没有时间的行使用写入时间，会再存一次 |
| `mssql` | 按键 upsert（`MERGE`）和删除：行相同 |
| `redis` | 键相同；`entry_id key` 时已存在的流条目被拒绝 |
| `s3` | 同一批次生成同一个对象，覆盖自身；重放若被切成不同的批次，会生成别的对象 |
| `influxdb`、`tdengine` | 同一个数据点：序列（子表）加时间，覆盖自身；删除不写 |
| `qdrant`、`milvus` | 按由键生成的 ID upsert 和 delete：数据点和实体相同 |
| `nats` | 相同记录得到相同的 `Nats-Msg-Id`：流的去重窗口丢弃它（默认 2 分钟） |
| `pulsar` | 相同记录得到相同的 `fit-id` 属性：`dedup read_back` 时，生产者最近存过的记录被丢弃 |
| `rabbitmq` | 再发布一次，`message_id` 相同，消费者可据此丢弃 |
| `kafka`、`http`、`otlp_logs`、`file`、`stdout` | 再写一次：Kafka 的生产者不是幂等的，OTLP 也不是；`http` 对相同记录（同一谱系）发送相同的 `Idempotency-Key` |

**记录 ID。** `nats`、`pulsar` 和 `rabbitmq` 目标给每条记录一个 ID（`Nats-Msg-Id`、`fit-id`、`message_id`），`http` 的
`Idempotency-Key`、`doris` 的 label 和 `s3` 的对象名也以同样方式生成，以便认出再次写入的记录。源若对每条记录有自己的名字，
且不论运行怎样读到它都不变，就把这个名字作为记录的来源（origin）：`kafka` 是主题、分区和偏移；`pulsar` 是分区和消息 ID
（ledger、entry、批内序号）；`nats` 是流序列号；`rabbitmq` 是 stream 的偏移；`mysql` 是事务的 GTID（或 binlog 文件和偏移）
加行在其中的序号；`postgres` 是提交 LSN 加变更的序号；`mssql`（CDC）是事务的 LSN 加变更的序号；`mongo` 是 resume token。
一条消息的多条记录（编解码器生成的）和转换由一条记录生成的多条记录按序号区分，于是 ID 跟着记录而不是位置：崩溃后的重放
得到原来的 ID，不论运行以什么顺序遇到各分区（`kafka`、`pulsar` 源在每个位置中按这个顺序写入所有分区的进度）。来源的 ID 还
包含流水线的名字，所以两条流水线的源给出相同的来源（两个 Kafka 集群上同名的主题、两台 MySQL 上相同的 binlog 文件和偏移）
并写入同一个流时，不会把对方的记录当作重复。没有来源的
记录（队列的消息、MQTT、文件、快照、变更跟踪）按其处或其后的位置及与它的距离命名，源以相同位置再次读到记录时才重复：
数据库日志、文件、单个分区。`doris` 的 label 和 `http` 的键还包含检查点的谱系，所以重置后重新读取的记录会被再次接受（Doris
的 label 在表删除重建后仍保留 `label_keep_max_second`，即三天）；记录 ID 和 `s3` 的对象名不包含：重置后消息仍是同一条
消息，其消费者可以丢弃，而再次写入的对象会覆盖自身。

**事务保持完整。** 检查点从不落在源事务内部；一个事务的记录带有其 ID，`mysql` 和 `postgres` 目标把每个源事务写成自己的
一个事务（`transactions source`），所以目标的读者永远不会看到半个事务。

**按键保持顺序**，从源到每个目标：目标可以并行写不同的键（作业的多个写入者），但同一个键从不乱序。

**恰好一次**，写入 MySQL、MariaDB、TiDB、PostgreSQL 和 SQL Server：`checkpoint sink` 时位置还会写入目标的一张表
（`_fit_sync_checkpoints`），与它所覆盖的记录在同一个事务中，流水线启动时优先读回它而不是文件。这样目标提交与文件下一次
检查点之间的崩溃不会让任何记录写两次。前提是流水线的每个目标都保存检查点，且位置相同：旁边有其他类型目标的流水线回退到
文件，即至少一次。仍可能写两次的情况：大于 MySQL 目标 `max_transaction_bytes` 的源事务（分段提交），以及按块读取的作业
（它只用自己的文件）。重置、手动设置位置和新的作业会开始新的谱系（lineage），所以目标中较旧的位置不会撤销它们；删除
流水线在表中的行和它的文件，就从头开始。只含位置的记录（见[检查点](#检查点)）会让文件越过目标保存的位置，因为目标不写
它们：文件同时记下目标保存的那个位置（`wrote`），保存该位置的目标会让流水线从文件的位置开始。

### 错误与重试

写入失败会从失败的记录开始重试，共 `retry.max_attempts`（5）次，等待时间从 `retry.backoff`（200ms）翻倍到
`retry.max_backoff`（30s）。目标会区分永久拒绝的记录（映射错误、约束、值过长）和可能恢复的错误（连接断开、429、死锁），
前者立即交给 `on_error`。仍然失败的记录，或变换处理失败的记录，按 `on_error` 处理：

- `stop`（默认）停止流水线，检查点停在该记录之前，直到它被恢复、配置改变或 fit 重启；
- `skip` 在该目标中略过它，记一行日志；
- `dead_letter` 把它连同错误写到 `dead_letter` 目标再继续；用 `dead_letters` 目标时，死信保存在 `state_dir` 中，可以
  列出和[重放](#死信)。

源失败（连接断开、服务器重启）时，流水线在同样的退避后从检查点重启，只要失败就一直如此：源被认为会恢复。

## 连接器

| 系统 | 作源 | 作目标 |
| --- | --- | --- |
| [文件](#文件) | `file`：跨轮转跟踪路径和通配符，多行合并 | `file`：按编解码器或模板写行，像日志一样轮转 |
| [Elasticsearch、OpenSearch](#elasticsearch-与-opensearch) | `elasticsearch`：经时间点读快照，再轮询游标字段 | `elasticsearch`：按 `_id` 用 `_bulk` 写 |
| [ClickHouse](#clickhouse) | `clickhouse`：按键读表或查询，再轮询游标列 | `clickhouse`：`JSONEachRow` 插入，去重，`ReplacingMergeTree` |
| [Doris、StarRocks](#doris-与-starrocks) | `doris`：经 MySQL 协议按键读表或查询，再轮询游标列 | `doris`：Stream Load，按来源或位置生成 label，删除标记 |
| [Loki](#loki) | | `loki`：推送 API，protobuf 或 JSON，流取自配置的字段 |
| [OpenTelemetry 日志](#opentelemetry-日志) | | `otlp_logs`：OTLP/HTTP 日志记录，protobuf 或 JSON |
| [HTTP](#http) | `http`：POST 到 `sync_ingest` 路由的记录 | `http`：以 NDJSON 或 JSON 批量 POST |
| [MySQL、MariaDB、TiDB](#mysqlmariadb-与-tidb) | `mysql`：以副本身份读 binlog，无锁快照 | `mysql`：upsert 和 delete，保持源的事务 |
| [经由 TiCDC 读取 TiDB](#经由-ticdc-读取-tidb) | `tidb`：在一个时间戳上读快照，再从 Kafka 读 changefeed | （用 `mysql` 目标） |
| [PostgreSQL](#postgresql) | `postgres`：用 `pgoutput` 做逻辑复制，精确的快照 | `postgres`：`ON CONFLICT` upsert，`COPY` |
| [SQL Server](#sql-server) | `mssql`：按 LSN 读 CDC 变更表，或 change tracking；无锁快照 | `mssql`：`MERGE` upsert、删除、批量装载，恰好一次 |
| [MongoDB](#mongodb) | `mongo`：按 `_id` 读快照，再读 change stream | `mongo`：按 `_id` 批量 upsert 和 delete |
| [Kafka、Redpanda](#kafka-与-redpanda) | `kafka`：每个分区从检查点中的 offset 读 | `kafka`：带键消息，批量、压缩 |
| [MQTT](#mqtt) | `mqtt`：持久会话，QoS 1 与 2 在目标拿到后确认 | `mqtt`：主题模板、QoS、可选保留 |
| [RabbitMQ](#rabbitmq) | `rabbitmq`：队列在目标拿到后确认，流队列从检查点中的 offset 读 | `rabbitmq`：持久消息，批次等发布确认 |
| [NATS JetStream](#nats-jetstream) | `nats`：持久拉取消费者，在目标拿到后确认；core 主题 | `nats`：JetStream 发布，按 `Nats-Msg-Id` 去重；core |
| [Apache Pulsar](#apache-pulsar) | `pulsar`：每个分区作为读者从检查点中的消息 ID 读，或持久订阅 | `pulsar`：带键消息，批量、压缩、去重 |
| [Redis、Valkey](#redis-与-valkey) | `redis`：用 `XREAD` 读流 | `redis`：`XADD`，或每条记录一个键 |
| [S3 及兼容存储](#s3-及兼容存储) | `s3`：前缀下的对象，按键的顺序，逐行 | `s3`：由行组成的对象，按大小和时间滚动 |
| [InfluxDB](#influxdb) | | `influxdb`：行协议写入 InfluxDB 1、2 和 3，按序列和时间 |
| [TDengine](#tdengine) | | `tdengine`：schemaless 行，或 `INSERT ... USING` 超级表 |
| [Qdrant 与 Milvus](#qdrant-与-milvus) | | `qdrant`、`milvus`：按键 upsert 和 delete，向量来自 `embed` |
| [插件](#插件) | `wasm`：经 fit 的 HTTP 客户端轮询的插件 | `wasm`：经它写出的插件 |
| [试用](#试用generatememory-和-stdout) | `generate` | `memory`、`stdout` |

密钥从不写在配置里：每个连接器都给出保存其密码或令牌的环境变量名（`password_env`、`secret_access_key_env`、
`bearer_token_env`、`headers_file`），在流水线加载时读取，所以 `fit validate` 也需要设置它。TLS 按系统根证书或 `ca_file` 校验，且不能
关闭；客户端证书为 `cert_file` 和 `key_file`。除 `wasm` 连接器（其可达范围是插件自己的）外，每个连接器的连接都可以在不写
任何记录的情况下测试（[测试连接](#admin-apifit-sync-与工作台)）。

模板分两种。名称模板说明记录发往哪里（SQL 目标和 `doris` 的 `table`，`influxdb` 和 `tdengine` 的 `measurement` 与
`subtable`，`qdrant` 和 `milvus` 的 `collection`），记录的命名空间写作 `{namespace}`，行的字段写作 `{name}`。记录模板
说明记录的内容（`loki` 的标签、`otlp_logs` 的属性、`file` 目标的 `template`），字段写作 `{name}`，记录本身的部分写作
`{$namespace}`、`{$op}`、`{$source}`、`{$key}` 和 `{$commit_time}`，使名为 `namespace` 的字段仍可引用。名称模板也接受
`{$namespace}`，含义相同，所以一种写法两处通用；在名称模板中其他 `{$...}` 不指代任何东西。

### 文件

`file` 源像 Filebeat 那样跟踪文件：每隔 `scan_interval`（10s）重新匹配通配符，每个文件以其设备号、inode 和文件头的
指纹识别，所以改名会被跟随，截断后从头重读，截断前拷贝出的副本从原文件读到的位置接着读，被删除的文件先读完。每一行或每个
`multiline` 事件经[编解码器](#编解码器)变成记录；检查点保存每个文件的标识和偏移。上限：`max_line_bytes`（1 MiB，更长
的行被截断并计数）、`max_open_files`（256）、`max_files`（100,000）。`file` 目标经 fit 的日志文件写入器，按编解码器或
`template` 写行，按 `rotate` 轮转、保留和压缩。

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
		}
	}
}
```

fit 的用户需要能读这些文件及其目录。

### Elasticsearch 与 OpenSearch

目标用 `_bulk` 写入：insert、update 和 message 写成该行的 `index`，删除写成 `delete`，`_id` 由记录的键生成
（`{"order": 7, "line": 2}` 为 `7:2`）。`index` 为 `{namespace}`，或带提交时间日期的模板。每一项的应答决定下一步：429 只要
集群这样答就一直重发（让流水线等待，再经其队列让源等待），5xx 重发五次，其他拒绝让该记录永久失败。支持 Elasticsearch
7.12 及以后、OpenSearch 2.4 及以后。

源经时间点（point in time，`snapshot pit`）读取所有文档，有 `cursor` 时再每隔 `poll_interval` 轮询游标字段（更新时间、
序号）超过上次读到的值的文档。轮询看不到删除：用软删除字段保留它们，并在 `deleted_field` 中指明。

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines orders-copy {
		source elasticsearch {
			urls https://old-es:9200
			username reader
			password_env OLD_ES_PASSWORD
			index orders-*
			cursor updated_at
			deleted_field deleted
		}
		sinks new {
			sink elasticsearch {
				urls https://es1:9200 https://es2:9200
				username fit
				password_env ES_PASSWORD
				index "{namespace}"
			}
		}
	}
}
```

目标的用户需要对索引有 `index` 和 `create_index`（或 `write`）权限；源的用户需要 `read` 和 `view_index_metadata`。

### ClickHouse

目标对一批中属于同一张表的每段记录执行一次 `INSERT ... FORMAT JSONEachRow`，带由这段记录生成的
`insert_deduplication_token`，所以相同的记录再写一次会被服务器丢弃。更新和删除写成按键排序的
`ReplacingMergeTree(_version, _deleted)` 的行，用 `FINAL` 读取时得到每个键的最后状态。行的版本是它的提交时间
（Unix 纳秒），或 `version_field` 的值；没有提交时间的记录（快照的行）版本为 0，因此永远不会压过变更，同一版本的行保留最后
写入的。表由你创建：

```sql
CREATE TABLE shop.orders (id UInt64, status String, total Decimal(12, 2),
  _version UInt64, _deleted UInt8)
ENGINE = ReplacingMergeTree(_version, _deleted) ORDER BY id
SETTINGS non_replicated_deduplication_window = 1000
```

源按 `cursor` 和 `key` 排序分页读取表或查询，再轮询上次读到的行之后的行；从 ClickHouse 删除的行看不到，但
`deleted_column` 会被读作删除。

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines events-to-ch {
		source clickhouse {
			urls http://ch-old:8123
			username fit
			password_env CH_OLD_PASSWORD
			table app.events
			key id
			cursor updated_at
			poll_interval 10s
		}
		sinks ch {
			sink clickhouse {
				urls http://ch1:8123 http://ch2:8123
				username fit
				password_env CH_PASSWORD
				table shop.orders
			}
			batch { max_records 10000; max_wait 1s }
		}
	}
}
```

### Doris 与 StarRocks

一个连接器 `doris` 同时服务 Apache Doris（2.1 及以上）和 StarRocks（3 及以上）；`flavor` 指明系统，两者在这里的区别只有
删除列和部分列更新的请求头。目标用 HTTP 上的 Stream Load 导入一个批次中同一张表的每段连续记录，行为 JSON（或 CSV），
`label` 由这段记录的第一条和最后一条记录的来源生成，没有来源的源用位置（见[投递保证](#投递保证)）：崩溃后再次导入的同一段会被以“已导入”拒绝，所以即使是 Duplicate Key 表，
一个批次也只存一份。更新和删除需要 merge on write 的 Unique Key 表（Doris）或主键表（StarRocks）：删除写出行或其键，
并把 `__DORIS_DELETE_SIGN__`（或 `__op`）设为 1。`partial_columns` 只更新记录带有的列。前端到后端的重定向会带着凭据跟随，
所以后端地址必须是 fit 能访问到的，但从 https 到 http 的重定向不跟随，否则凭据和行都会明文发送；`urls` 也可以直接写后端的
HTTP 端口。因行而被拒绝的导入会被拆分，直到找出它不能
接受的那条记录：该记录交给 `on_error`，它之前的记录都已导入。

label 比表活得久：Doris 会保留 `label_keep_max_second`（默认三天）。label 包含流水线检查点的谱系（见[检查点](#检查点)），
重置会改变它，所以为了导入重建的表而重置的流水线会再次导入其记录，无需修改 `label_prefix`。不重置而重建表时，需要重置或
换一个 `label_prefix`，否则之前导入过的记录会被以“已导入”拒绝；比谱系更早的 fit 保存的检查点也一样，其 label 保持原样，
直到它被重置。

源经 MySQL 协议（前端的查询端口）读取表或查询，按 `cursor` 和 `key` 排序分页，再轮询上次读到的行之后的行，与 clickhouse
源相同；从表中删除的行看不到，但 `deleted_column` 会读成删除。

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines orders-to-doris {
		source mysql {
			address mysql:3306
			username fit_repl
			password_env MYSQL_PASSWORD
			server_id 4101
			tables shop.orders
		}
		sinks doris {
			sink doris {
				urls http://fe1:8030 http://fe2:8030
				username fit
				password_env DORIS_PASSWORD
				table dw.{namespace}
			}
			batch { max_records 20000; max_wait 2s }
		}
	}
	pipelines starrocks-events {
		source doris {
			address sr-fe:9030
			username fit
			password_env SR_PASSWORD
			table app.events
			key id
			cursor updated_at
		}
		sinks out { sink stdout }
	}
}
```

`orders-to-doris` 在 Doris 上的表：

```sql
CREATE TABLE dw.orders (id BIGINT NOT NULL, status VARCHAR(20), total DECIMAL(12, 2))
UNIQUE KEY(id) DISTRIBUTED BY HASH(id) BUCKETS 8
PROPERTIES ("enable_unique_key_merge_on_write" = "true");
```

### Loki

目标 `loki` 把记录作为日志行推送到 Loki 的推送 API，格式为 snappy 压缩的 protobuf（与 Promtail 和 Alloy 相同）或 JSON。
记录的行由其 `codec` 生成，默认是 line 编解码器，文件的行原样保留。流的 `labels` 是记录字段的模板，默认为
`{"job": "fit", "namespace": "{$namespace}"}`：只有配置中写出的字段才成为标签，所以流的数量等于这些字段取值的数量，
绝不会每条记录一个流；取值很多的值放进 `structured_metadata`（Loki 3）。行的时间取 `timestamp_field`，否则取记录的提交
时间，再否则取写入时间。`tenant`（或 `tenant_field`）以 `X-Scope-OrgID` 发送。每个流的条目按时间排序；Loki 拒绝的请求
（条目太旧、行太长）会被拆分，直到找出它拒绝的记录，交给 `on_error`。Loki 对流、时间和行都相同的条目只保留一份，所以
记录带时间时，崩溃后再写一次的批次只存一份。连接测试推送一个没有条目的流，Loki 不会存下任何东西，所以只能推送、不能
查询的凭据也能通过。

### OpenTelemetry 日志

目标 `otlp_logs` 把记录作为 OpenTelemetry 日志记录，经 OTLP/HTTP（`/v1/logs`）导出到 OpenTelemetry Collector 或任何
OTLP 接收端，protobuf 或 JSON，gzip 压缩。资源与 fit 自身遥测发送的相同（`service.name`、`service.version`），再加上
由记录字段模板生成的 `resource` 属性；日志记录的 body 是其 `codec` 生成的行，或带类型的行 map（`body row`），时间取
`timestamp_field` 或提交时间，严重级别取 `severity_field`，属性取 `attributes` 的模板（默认 `fit.namespace` 和
`fit.op`）。`headers_file` 让服务商的 key 不出现在配置中。OTLP 没有去重：崩溃后再写一次的批次会再导出一次。

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines app-logs {
		source file { paths /var/log/app/*.log }
		sinks loki {
			sink loki {
				urls http://loki:3100
				labels { job app; host web1 }
			}
			batch { max_wait 1s }
		}
		sinks otel {
			sink otlp_logs {
				urls http://collector:4318
				service_name app
				resource { host.name web1 }
			}
			batch { max_wait 1s }
		}
	}
}
```

### HTTP

`http` 源接收客户端 POST 到处理器为 [`sync_ingest`](reference/config.md#sync_ingest) 的路由的记录：JSON、NDJSON，或经
编解码器的任何格式。请求在所有目标都拿到其记录后答 `200`；流水线未运行或待处理请求已满时答 `503` 并带 `Retry-After`；
无法读取的请求体答 `400`，其中任何内容都不会写入。客户端只要在得到 `200` 之前一直重发，就不会丢失任何东西。处理器从不在
fib 的工作协程上等待。能访问这条路由的人都能向流水线写入，所以请在它前面放认证中间件。

`http` 目标把每个批次以 NDJSON 或 JSON 数组 POST 出去，带 `Idempotency-Key`，相同记录重发时它也相同，检查点重置后（谱系变了）则不同；`2xx` 即确认，
`5xx`、`408`、`429` 和超时会重试，其他应答让记录永久失败。

```fit
listener web :8080 http/1.1 h2c

route ingest {
	match { path /ingest/events }
	middleware basic_auth {
		users {
			username collector
			password_hash $2a$10$gkDJyGagxp1q7R24F0rSjulsgZOFL55Zm6JK2RcxTCLDk23szhZA2
		}
	}
	handler sync_ingest { pipeline events; timeout 10s }
}

sync {
	state_dir /var/lib/fit/sync
	pipelines events {
		source http { namespace events; max_pending_requests 256 }
		transforms key { fields event_id }
		sinks collector {
			sink http {
				url https://collector.example.com/v1/events
				bearer_token_env COLLECTOR_TOKEN
				format ndjson
			}
		}
	}
}
```

### MySQL、MariaDB 与 TiDB

`mysql` 目标写入 MySQL 5.7 及以后、MariaDB 10.11 及以后和 TiDB：insert、update 和 message 写成
`INSERT ... ON DUPLICATE KEY UPDATE`，删除按键的各列执行，一批中属于同一张表的每段记录是一条多行语句。
`transactions source`（默认）把每个源事务写成服务器的一个事务，批次在事务中间结束时保持事务打开到下一批；
`transactions batch` 把多个源事务合在一起。`ddl report`（默认）记录 DDL 记录而不执行；`ddl stop` 让它们失败，使
`on_error stop` 停下流水线，等人去修改表。`checkpoint sink` 提供[恰好一次](#投递保证)。写 TiDB 时，事务受
`max_transaction_bytes`（16 MiB）限制，`AUTO_RANDOM` 列接受源的值。表由你创建，每张表在记录的键列上要有主键或唯一键；
会话的时区为 UTC。行以文本语句写入，值写成按会话 `sql_mode` 转义的字面量（每条语句一次往返）；作业中
`job.max_parallel` 个写入者各有自己的连接。

`mysql` 源作为副本跟随 MySQL 5.7 及以后或 MariaDB 10.11 及以后，以自己的 `server_id` 注册；启动时检查 binlog 已开启
（`log_bin`）、`binlog_format = ROW` 且 `binlog_row_image = FULL`。`tables`（`db.table` 的通配符，省略时为系统库之外的
所有表）列出且 `exclude` 未排除的表中的每个 insert、update 和 delete 成为命名空间为 `db.table` 的记录，键为主键各列（或
`keys` 指定的列）；改变了键的 update 以旧键的删除加新行的插入出现。列名在服务器写入时取自 binlog
（`binlog_row_metadata = FULL`），否则取自 `information_schema`，每次 DDL 之后重新读取。所读表的 DDL（`CREATE`、
`ALTER`、`DROP`、`RENAME`、`TRUNCATE`）是一条 `ddl` 记录，带 `statement` 和 `database`，SQL 目标只报告而不执行。

记录按提交顺序一次一个事务地到来，带事务的 GTID（或 binlog 文件和偏移）和提交时间。每个事务结束处的位置为
`{"gtids": "...", "file": "mysql-bin.000042", "pos": 1234}`：服务器有 GTID 时（MySQL 的 `gtid_mode = ON`、MariaDB 的
GTID）流水线按 GTID 从它续读，否则按文件和偏移。被切断的流（如服务器重启）从最后交出的事务之后重新读取。服务器已清除该
位置的 binlog 时，流水线停止，错误信息说明需要新的快照。

`snapshot initial`（默认）时，没有检查点的流水线先读表中的行，再读 binlog；`always` 在源每次启动时都读；`never` 只读
binlog，从检查点或从现在开始。先不加锁地读 binlog 位置，再按键读每张表，每次查询 `batch_size`（1,000）行，每次查询在
带一致性快照的 `REPEATABLE READ` 事务中；然后流从那个位置开始，所以期间的变更会再来一遍，按键写入的目标会收敛。被中断的
快照从最后保存的键之后继续；没有主键的表会被拒绝，除非 `keys` 指定了键。作业（`mode once`）中只读快照，每张表按键分成
`chunk_rows`（100,000）行的块。源报告的延迟是服务器在其位置之后保存的 binlog 字节数（`SHOW BINARY LOGS`），`heartbeat`
（10s）表明已追上时为零。

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines shop-replica {
		source mysql {
			address mysql.internal:3306
			username fit_repl
			password_env MYSQL_PASSWORD
			tls true
			server_id 4101
			tables shop.*
			snapshot initial
		}
		sinks replica {
			sink mysql {
				address replica.internal:3306
				username fit_sync
				password_env MYSQL_SYNC_PASSWORD
				tls true
				checkpoint sink
				database shop
			}
			batch { max_records 500; max_wait 200ms }
		}
	}
}
```

### 经由 TiCDC 读取 TiDB

`tidb` 源先经 MySQL 协议在一个时间戳上读取 `tables` 的快照，再读 TiCDC changefeed 写到 Kafka 的、此后的变更
（`protocol canal-json`，创建时带 `enable-tidb-extension=true`，或 `simple`），提交于快照时刻及之前的变更被略过。TiDB 作
目标时用 `mysql` 目标。

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines tidb-to-pg {
		source tidb {
			address tidb:4000
			username fit_sync
			password_env TIDB_PASSWORD
			tables shop.orders shop.order_items
			protocol canal-json
			kafka { brokers kafka-1:9092; topics ticdc-shop }
		}
		sinks pg {
			sink postgres {
				address pg:5432
				user fit
				password_env PG_PASSWORD
				database replica
			}
		}
	}
}
```

changefeed 必须在取快照之前就存在，其主题必须保留此后的消息，快照必须在 `tidb_gc_life_time` 之内完成。

### PostgreSQL

`postgres` 源作为逻辑副本跟随 PostgreSQL 13 到 17：`publication` 指定表，`slot` 让服务器保留 WAL 直到 fit 确认，流用内置
的 `pgoutput` 读取。`create true` 时，缺失的 publication（`FOR TABLE` 这些 `tables`）和复制槽会被创建；之后两者都不会被
修改或删除。`snapshot initial`（默认）时，没有检查点的流水线先从复制槽导出的快照读取所有行，按 `chunk_rows` 分块，每次
`snapshot_parallel` 块，流恰好从那一点开始：没有变更同时出现在两者中，也没有变更两者都缺。记录按提交顺序一次一个事务地
到来；只有当每个目标都已拿到、检查点已保存后，才把 LSN 告诉复制槽。服务器流式发送的大事务在提交前暂存到 `spool_dir`。
复制槽的延迟会被报告，超过 `lag_warning`（1 GiB）时记日志：停止的流水线会让服务器保留 WAL。

`postgres` 目标用 `INSERT ... ON CONFLICT (key) DO UPDATE` upsert、按键删除，每段至多 `max_statement_rows` 行一条语句；
达到 `copy_min_rows`（100）条的 upsert 用 `COPY` 写入临时表，再用一条语句合并。`table` 为 `{namespace}`，在第一个点处
分成模式和表名，所以 MySQL 源的 `shop.orders` 即 `shop` 模式下的 `orders` 表。`transactions batch`（默认）或 `source`；
`checkpoint sink` 提供[恰好一次](#投递保证)，并使 `source` 成为默认。

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines orders-out {
		source postgres {
			address db.internal:5432
			user repl
			password_env PG_REPL_PASSWORD
			database shop
			create true
			publication fit_orders
			slot fit_orders
			tables public.orders public.order_items
		}
		sinks copy {
			sink postgres {
				address replica.internal:5432
				user sync
				password_env PG_PASSWORD
				database shop
				checkpoint sink
			}
			batch { max_records 1000 }
		}
	}
}
```

两者默认都用 `sslmode verify-full` 的 TLS（还有 `verify-ca`、`require`、`disable`）和 SCRAM-SHA-256，服务器提供通道绑定时
使用它；MD5 和明文密码被拒绝。流水线永久移除时，请删除复制槽（`SELECT pg_drop_replication_slot('fit_orders')`）。

### SQL Server

`mssql` 源经 SQL Server 的 CDC 变更表跟随 SQL Server 2016 及以后的版本（测试过 2019 与 2022），变更表由 SQL Server Agent
的捕获作业从事务日志填充。`snapshot initial`（默认）时，没有检查点的流水线先读取 CDC 的最高 LSN，再按键读取各表的所有行，
每次 `batch_size`（1000）行，数据库允许时每批在快照隔离的事务中读取（`ALLOW_SNAPSHOT_ISOLATION ON`），否则用读已提交；
之后变更从那个 LSN 开始，因此快照期间的变更会再来一次，与 MySQL 源一样。追上之后每隔 `poll_interval`（1s）轮询变更表，
每次最多 `window_transactions`（1000）个事务，每个事务的变更跨表按服务器产生的顺序一起交出：更新带有变更前后的行（键
也可能改变），删除带有被删的行，CDC 记在 `cdc.ddl_history` 中的 DDL 作为 `ddl` 记录。检查点是所有目标都拿到的最后一个
事务的 LSN。变更行超过 `window_rows`（10000）的窗口会缩小后重读，超过它的单个事务分页读取。记录的命名空间为
`schema.table`，键为主键（或启用 CDC 时指定的唯一索引，或 `keys`）。

`tables` 中完整写出名称的表必须被捕获，否则流水线失败，并给出捕获它的 `sys.sp_cdc_enable_table` 调用；`dbo.*` 这样的
通配模式匹配已被捕获的表，流水线运行期间 CDC 开始捕获的表会先整表读取，再跟随。表的第二个捕获实例（模式变化后建立，
含新列）从其起点开始跟随，旧实例读到那里为止；流水线运行期间旧实例被删除（`sys.sp_cdc_disable_table`），而它到新实例起点
为止的变更尚未读完时，流水线停止并要求新的快照（停止期间的删除不留下可查的痕迹），完全不再被捕获的表记一条日志并不再读取。
比 CDC 清理所保留的（默认三天）更旧的检查点会让流水线停止：需要新的快照。报告的延迟是位置之后 `cdc.lsn_time_mapping` 中的
事务数（`mode change_tracking` 时为版本数）。
`mode change_tracking` 改读 SQL Server 的 change tracking，对服务器更轻：读取检查点中的版本之后变更过的键，每行按当时的
状态读回（行已不存在则为删除），一次轮询中所有表的变更是一个事务；两次轮询之间的变更合并为最后一个，更新没有变更前的行。

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines orders-from-mssql {
		source mssql {
			address mssql.internal:1433
			username fit_cdc
			password_env MSSQL_PASSWORD
			database shop
			tables dbo.orders sales.*
			ca_file /etc/fit/mssql-ca.pem
		}
		sinks search {
			sink elasticsearch { urls https://es:9200; username fit; password_env ES_PASSWORD }
		}
	}
}
```

连接默认使用 TLS（`encrypt on`，在 TDS 的 pre-login 中协商，服务器证书按 `ca_file` 或系统根证书校验），TDS 8 用 `strict`
（先 TLS，服务器允许时用 TLS 1.3），或 `off`。`strict` 需要 Windows 上的 SQL Server 2022 及以后、Linux 上的 2025 及以后
（2022 的 Linux 镜像会关闭每个 strict 连接），以及按微软指南制作的证书（CN 为服务器主机名、用于服务器身份验证、私钥属于
`mssql` 用户）；已对 SQL Server 2025 测试。

排序规则的代码页既不是 1252（拉丁排序规则）也不是 UTF-8 的 varchar、char 与 text 列（如 `Chinese_PRC_CI_AS` 的 936、
`Japanese_CI_AS` 的 932）按 `nvarchar` 读取，其值以文本到达 sink。使用 CDC 时，请让这样的列保持数据库自身的排序规则
（或 UTF-8 排序规则）：排序规则不同于数据库的 varchar、char 与 text 列中的非 ASCII 字符，凡数据库代码页没有的，CDC
会在变更表中写成问号，这是 SQL Server 的限制。对通过 CDC 读取的表中排序规则代码页既不是 UTF-8 也不是数据库代码页的
每个这样的列，源在发现表时以及之后接入新表时给出警告，每次运行每列一次，写明表、列与两种排序规则；连接测试把同样的
内容列为警告，测试仍通过。快照直接读表，会保留这些字符；变更跟踪直接读行，二者都不警告。

登录默认为 SQL 身份验证（`username`、`password_env`），除非 `auth` 指定其他方式，source 与 sink 相同：

- `method entra`，Microsoft Entra ID，用于 Azure SQL Database 与 Managed Instance：在 TDS 的 FEDAUTH 中发送访问令牌。
  令牌来自服务主体（`tenant_id`、`client_id`，以及 `client_secret_env` 或 `client_certificate_file`，走 OAuth 的客户端
  凭据流程；其他云用 `authority`）、主机的托管标识（`managed_identity true`，用户分配的标识加 `client_id`），或在别处
  获取的令牌（`token_env`、`token_file`，每次连接时读取）。不用 `username`；`encrypt` 不能是 `off`。未对 Azure 测试。
- `method kerberos`，用 Kerberos 票据的 Windows 身份验证：`username` 为主体（`user` 或 `user@REALM`），密钥来自
  `password_env` 或 `keytab_file`（仅 AES 密钥）；领域与 KDC 来自 `realm` 与 `kdc`，或 `krb5_conf`（`/etc/krb5.conf`）；
  服务器的 SPN 为 `address` 的 `MSSQLSvc/host:port`，或 `spn`。已对 MIT KDC 与 Linux 上的 SQL Server 2022 测试：它接受
  票据，但没有 Active Directory，无法把它映射到登录名；对域中 SQL Server 的完整登录未测试。
- `method ntlm`，用 NTLMv2 的 Windows 身份验证，用于域外的服务器：`username` 为 `DOMAIN\user`，加 `password_env`。
  比 Kerberos 弱（服务器不被验证，截获的交换可被离线猜测）；未对服务器测试。

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines orders-from-azure-sql {
		source mssql {
			address sql-prod.database.windows.net:1433
			database shop
			tables dbo.orders
			auth { method entra; tenant_id contoso.onmicrosoft.com; client_id 0f1e5a52-1b9e-4c1d-9e2a-6c2f3b7d8a10; client_secret_env AZURE_SQL_SECRET }
		}
		sinks search {
			sink elasticsearch { urls https://es:9200; username fit; password_env ES_PASSWORD }
		}
	}
}
```

目标 `mssql` 用 `MERGE ... WITH (HOLDLOCK)` 按记录的键 upsert 一个批次中同一张表的每段连续行（`upsert update_insert`：先
`UPDATE`，找不到行时再 `INSERT`），按键删除；不属于源事务的连续插入（快照的）达到 `bulk_rows`（1000）条时，先批量装载
（`INSERT BULK`）到临时表再合并进来。每个源事务是服务器的一个事务（`transactions source`），或一个批次的合在一起
（`batch`），从不只提交一部分；服务器永久拒绝的语句只让它的那条记录失败，之前的记录都已提交。`checkpoint sink` 时位置在同一
事务中写入 `_fit_sync_checkpoints`，做到恰好一次。`identity_insert` 把源的值写入 `IDENTITY` 列。表名由命名空间得出，为
`schema.table`（或 `db.schema.table`）：MySQL 源的 `shop.orders` 是 schema `shop` 中的 `orders`，所以要用 `route` 变换改名
（`rules { match shop.*; to dbo.{rest} }`），或直接写一张表的名字（`dbo.{namespace}` 只适用于不含点的命名空间，例如主题名）。
表由你建好，在键列上要有主键或唯一索引。NULL 以其列的类型发送，类型每个连接读一次；语句被拒绝时（例如列的类型在此期间
改了），会先重新读取类型，再让记录失败。

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines orders-to-mssql {
		source mysql {
			address mysql:3306
			username fit_repl
			password_env MYSQL_PASSWORD
			server_id 4102
			tables shop.orders
		}
		sinks mssql {
			sink mssql {
				address mssql.internal:1433
				username fit_writer
				password_env MSSQL_PASSWORD
				database dw
				table dbo.orders
				checkpoint sink
				ca_file /etc/fit/mssql-ca.pem
			}
		}
	}
}
```

### MongoDB

`mongo` 源读取 MongoDB 6.0 及以后的副本集，或经 `mongos` 读取分片集群：先按 `_id` 读集合的快照（`snapshot initial`），
再从快照之前取得的 resume token 读它们的 change stream，两者重叠，由目标的 upsert 收敛。insert、update、replace 和 delete
是各自类型的记录；集合被删除或改名是 `ddl` 记录。事务中的变更带事务 ID，所以检查点从不落在事务内部。oplog 已不再包含的
token 需要新的快照，错误信息会说明。`mongo` 目标按键生成的 `_id` 做无序批量 upsert 和 delete，每批中每个文档只写最后一条
记录，写关注由你设置（从不允许 `w: 0`）。两者使用相同的连接参数：`uri`（其中若含密码会被拒绝）或 `hosts`、
`password_env`、`tls` 和 `ca_file`。

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines shop-copy {
		source mongo {
			uri "mongodb://sync@db1:27017,db2:27017,db3:27017/?replicaSet=rs0&tls=true"
			password_env MONGO_SYNC_PASSWORD
			database shop
			collections orders customers
		}
		sinks archive {
			sink mongo {
				uri "mongodb://archive1:27017,archive2:27017/?replicaSet=rs1"
				username fit
				password_env MONGO_PASSWORD
				database shop_archive
				write_concern majority
			}
		}
	}
}
```

### Kafka 与 Redpanda

`kafka` 源从流水线检查点中的 offset 读取 `topics` 的每个分区（Kafka 3.x 及以后、Redpanda）：不加入消费者组，不提交
offset，所以检查点是唯一的游标。每条消息经 `codec` 处理（默认 `raw`；`json`、`canal_json`、`debezium`、`ticdc_simple`
或插件读取各自的格式）；值为 null 的消息是对其键的删除。检查点中的 offset 已被保留策略删除时，流水线停止，除非
`offset_reset` 另有指定。报告的延迟是落后于各分区高水位的 offset 数。

`kafka` 目标为每条记录向 `topic`（`{namespace}`）生产一条消息，以记录的键为消息键，由 Java 客户端的分区器（murmur2）分配
分区，所以同一个键的消息保持顺序；删除写成墓碑；批次被压缩（`lz4`），并由所有同步副本确认。支持 TLS 以及 SASL PLAIN、
SCRAM-SHA-256 和 SCRAM-SHA-512 认证。

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines orders-to-search {
		source kafka {
			brokers kafka-1:9093 kafka-2:9093
			topics shop.orders
			tls true
			sasl SCRAM-SHA-512
			username fit
			password_env KAFKA_PASSWORD
			codec debezium
		}
		sinks search {
			sink elasticsearch { urls https://es:9200; username fit; password_env ES_PASSWORD }
		}
	}
}
```

### MQTT

`mqtt` 源以持久会话订阅 MQTT broker（3.1.1 或 5：EMQX、Mosquitto、HiveMQ、VerneMQ）上的主题过滤器，会话由 `client_id`
命名，fit 不在时 broker 保留该会话以及发给它的 QoS 1 与 2 消息（版本 5 中为 `session_expiry`，24h）。QoS 1 或 2 的消息只在
所有目标都拿到其记录后才按到达顺序确认（`PUBACK`，或 `PUBREC` 后 `PUBCOMP`），因此在目标写入之前停止或被 kill 的流水线会
从 broker 再次收到它：对幂等目标至少一次，QoS 2 的消息在一个连接内只取一次。检查点不保存可续读的位置（会话就是位置），
但保存已订阅的主题过滤器：从 `topics` 删去的过滤器会在下次运行恢复会话时被取消订阅（`UNSUBSCRIBE`），broker 此前为它排队、
可能仍会到来的消息被确认而不写入，除非某个仍在配置中的过滤器也匹配其主题。过滤器与检查点中不同的运行在订阅后以一条只含
位置的记录交出它们，因此即使没有消息到来，它们也会在约 `checkpoint_interval` 内被保存（流水线的首次运行立即保存），在一次
运行中加上、下一次运行中删去的过滤器同样会被取消订阅。
一条消息是一条记录，命名空间为主题（或 `namespace`），键为主题，除非编解码器设了键；没有 `codec` 时其 after 含 `topic`、
`payload`（字节）、`qos`、`retain`，在版本 5 中还有消息带有的属性（`content_type`、`response_topic`、`correlation_data`、
`user_properties`）。作业中，源一直读到 `idle`（5s）内没有消息到来。

`mqtt` 目标把每条记录以 `qos`（1）发布到 `topic`（`{namespace}`，也可用 `{key}`），`retain` 时保留；一批消息按顺序发出，
未确认的最多 `max_inflight`（64）条，broker 确认了每一条后这批才算写入。负载为编解码器的编码，没有编解码器时为 after 的
`payload`，因此经源与目标复制的主题逐字节不变，版本 5 的属性也保留。`retain` 时的删除是一条空的保留消息，它清除该主题。
版本 5 的 broker 永久拒绝的发布（主题名无效、未授权、包过大）立即交给 `on_error`。

两者都可以设置 `will`：一条遗嘱消息（`topic`、`payload`、`qos`、`retain`，版本 5 中还有 `delay`、`message_expiry`、
`content_type`、`user_properties`），fit 的连接不是由 fit 关闭而结束（连接丢失或保活超时）时由 broker 发布，让其他客户端得知
流水线已不在；重载或停止会关闭连接，不发布遗嘱。在版本 5 中两者都可以用 SCRAM 做增强认证（`AUTH`）：`auth_method`
设为 `SCRAM-SHA-256`（或 `SCRAM-SHA-1`、`SCRAM-SHA-512`），配合 `username` 和 `password_env`，不发送密码即可证明密码，
并校验 broker 知道该密码的证明。EMQX 5 的 SCRAM 认证器支持 `SCRAM-SHA-256` 和 `SCRAM-SHA-512`；Mosquitto 自身没有增强
认证。不提供基于 `AUTH` 的 Kerberos（GSSAPI）。

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines sensors {
		source mqtt {
			servers emqx1:1883 emqx2:1883
			client_id fit-sensors
			topics sensors/+/temperature devices/#
			qos 1
			codec json
		}
		sinks archive {
			sink mqtt {
				servers mosquitto:8883
				tls true
				username fit
				password_env MQTT_PASSWORD
				topic archive/{namespace}
				qos 2
				retain true
			}
		}
	}
}
```

消息只在目标拿到后才确认，因此 broker 发出而未确认的消息数限制了一批的大小：EMQX 的 `mqtt.max_inflight` 默认 32，
Mosquitto 的 `max_inflight_messages` 默认 20；请把它们以及版本 5 中源的 `receive_maximum`（128）调到高于目标的
`batch.max_records`，或把 `batch.max_wait` 设短。每条流水线需要自己的 `client_id`：同一 ID 的第二个客户端会接管会话。

### RabbitMQ

`rabbitmq` 源通过 AMQP 0-9-1 消费 `queues`（经典队列和仲裁队列，RabbitMQ 3.13 和 4.x），带 `prefetch`（`basic.qos`，默认
1000），每条投递只在所有目标都拿到其记录后才确认：队列的位置在 broker 那里，检查点中没有它，fit 停止时未确认的消息会被
再次投递。`streams` 是流队列，从检查点中最后一个 offset 之后读（没有检查点时从 `stream_start`，默认 `first`）。消息体经
`codec` 处理（默认 `raw`）；消息记录在 `value` 旁还有 `exchange`、`routing_key`、`queue`、`redelivered`、`headers` 以及已设置
的属性，`metadata_field` 把它们放进编解码器生成的行的一个字段。记录的键是 `rabbitmq` 目标写入的 `fit-key` 头。在作业
（`mode once`）中，源在 `idle_end`（2s）内没有消息到来、且所有记录都已确认时结束。报告的延迟是各队列就绪的消息数（每 10s
查询一次；stream 队列没有这个计数）。

`rabbitmq` 目标为每条记录向 `exchange`（不写时为默认交换机）以 `routing_key`（`{namespace}`；也可用 `{key}`）发布一条持久
消息，其 `message_id` 在记录再次写入时不变，并在 broker 确认整批之前一直持有该批次（发布确认）。没有队列接收的消息使其
记录失败（`unroutable error`，默认；`drop` 让 broker 丢弃它）。投递至少一次：消费者可以按 `message_id` 丢弃重复。支持 TLS
（`ca_file`、`cert_file`）以及 PLAIN 和 EXTERNAL 认证；遵守心跳，以及 broker 的内存和磁盘告警（发布最多被挡 `timeout`）。

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines orders-to-queue {
		source rabbitmq {
			addresses rabbit-1:5671 rabbit-2:5671
			tls true
			username fit
			password_env RABBITMQ_PASSWORD
			queues orders
			prefetch 500
			codec json
		}
		sinks archive {
			sink rabbitmq {
				addresses archive:5672
				username fit
				password_env RABBITMQ_PASSWORD
				exchange archive
				routing_key orders.{namespace}
				content_type application/json
				codec json
			}
		}
	}
}
```

### NATS JetStream

`nats` 源通过持久拉取 `consumer` 读取 JetStream 的 `stream`（nats-server 2.10 及以后），每条消息只在所有目标都拿到后才确认，
所以崩溃不会丢消息：至少一次。设置 `create_consumer` 时由 fit 创建消费者（ack 策略 all，`subjects` 为其过滤，`start` 为 all
或 new，`ack_wait`、`max_ack_pending`）；流序号在每条记录的位置和检查点中，fit 创建的消费者丢失时，或某次运行发现它持有已投递
未确认的消息时，会从检查点重新创建，使这些消息先来且有序。你自己的消费者从其确认下限读起，且需要 ack 策略 `explicit`。
每条消息经 `codec` 处理（默认 `raw`）；消息记录在 `value` 旁还有主题、头、流、序号和时间，`metadata_field` 把它们放进
编解码器生成的行。报告的延迟是消费者待处理的消息数。`mode core` 改为订阅 core NATS 的 `subjects`（`queue` 组可分担），
至多一次，用于遥测：fit 停机时、或到来得比它处理得快（`buffer`）的消息会丢失。

`nats` 目标为每条记录向 `subject`（`{namespace}`）发布一条消息，数据来自 `codec`，带由记录来源生成的 `Nats-Msg-Id`（见[投递保证](#投递保证)），
所以在流的去重窗口（`duplicate_window`，默认 2 分钟）内再次写入的批次只存一次；最多 `max_in_flight` 个发布同时等待流的
确认，批次在其每条消息都被确认后才确认。`mode core` 发布到 core NATS，只确认到服务器读到为止。
每条消息在 `Fit-Op`、`Fit-Namespace` 和 `Fit-Key` 头中带上记录的操作、命名空间和键，与 RabbitMQ 目标的 `fit-op`、
`fit-namespace` 和 `fit-key` 相同（键为字符串或字节时原样，其他为普通 JSON；头的一行容不下的值，即含换行或两端有空白的，
不写）。`nats` 源在编解码器未设置键时把 `Fit-Key` 读回记录的键，所以 NATS 到 NATS 的复制保留键。

两者都连接 `servers`（集群的其他服务器会被获知，一台失败时客户端换到另一台），`tls` 时走 TLS，用令牌（`token_env`）、
用户名和密码（`username`、`password_env`）、NKey 种子（`nkey_file`）或 JWT 及其种子的 creds 文件（`creds_file`）认证。

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines orders-to-search {
		source nats {
			servers nats-1:4222 nats-2:4222 nats-3:4222
			tls true
			username fit
			password_env NATS_PASSWORD
			stream ORDERS
			consumer fit-orders-to-search
			create_consumer true
			subjects orders.>
			codec json
		}
		transforms key { fields order_id }
		sinks search {
			sink elasticsearch { urls https://es:9200; username fit; password_env ES_PASSWORD }
		}
	}
}
```

### Apache Pulsar

`pulsar` 源读取 `topics` 的每个分区（Pulsar 2.10 及以后；裸名属于 `public/default`）。默认（`mode reader`）每个分区由一个
非持久的读者从流水线检查点中的消息 ID 读起，所以 broker 上不保留任何东西，保留策略在读之前删掉的消息会丢失；
`mode subscription` 通过持久的 `subscription`（`exclusive` 或 `failover`）读取，在所有目标拿到记录后累积确认，消息 ID 仍在
检查点中，以便跳过 broker 再次发送的内容。每条消息经 `codec` 处理（默认 `raw`）；消息记录还带有消息的 `topic`、
`message_id`、`properties`、`producer`、`publish_time` 和 `event_time`，设置 `metadata_field` 时编解码器生成的行把它们放在该
字段下。值为 null 的消息是对其键的删除。加密和分块的消息不读取。

`pulsar` 目标为每条记录向 `topic`（`{namespace}`）发送一条消息，以记录的键为消息键，由 Java 客户端的默认路由（键的 Java
字符串哈希）分配分区，所以同一个键的消息保持顺序；删除的值为 null，供主题压缩。一个分区的消息作为 Pulsar 批次发出
（`max_batch_messages`、`max_batch_bytes`），压缩（`lz4`；`zlib`、`zstd`、`snappy`），生产者名为 `producer_name`，其序列 ID
接着 broker 的继续。每条消息带属性 `fit-id`，由记录的来源生成（见[投递保证](#投递保证)）：`dedup read_back`（默认）时目标在创建生产者时读回每个分区
最后 64 个条目，丢弃其中已存储的记录，所以崩溃后再次写入的批次只存一次（这需要消费该主题的权限；`dedup off` 全部发送）。
`pulsar+ssl://` 的 URL 走 TLS；`token_env` 以令牌
认证。

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines orders-to-pulsar {
		source kafka {
			brokers kafka-1:9092
			topics shop.orders
			codec debezium
		}
		sinks events {
			sink pulsar {
				url pulsar+ssl://pulsar.internal:6651
				token_env PULSAR_TOKEN
				topic shop/orders/{namespace}
				producer_name orders-to-pulsar
				codec json
			}
		}
	}
}
```

### Redis 与 Valkey

`redis` 源用 `XREAD` 从上次读到的条目 ID 之后读取 `streams`（没有消费者组，不确认也不删除任何东西），每个条目是一条由其
字段组成的 `message`，`form record` 时则是 `redis` 目标写入的那条记录。`redis` 目标用 `XADD` 写流条目（`entry_id key`
时 ID 取自键，用于精确复制），或每条记录写一个键（`mode keys`：JSON 值、整条记录或 hash，可带 `ttl`），删除即删除该键。
支持 Redis 6 及以后、Valkey；单机服务器或主节点，不支持 Redis Cluster。

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines orders-cache {
		source redis {
			address redis.internal:6379
			username sync
			password_env REDIS_SYNC_PASSWORD
			streams events:orders
		}
		transforms key { fields order_id }
		sinks cache {
			sink redis {
				address cache.internal:6379
				password_env REDIS_PASSWORD
				mode keys
				key order:{key}
				value hash
				ttl 24h
			}
		}
	}
}
```

### S3 及兼容存储

`s3` 目标把每个批次写成一个由行组成的对象（按编解码器，可选 gzip），按 `key` 模板以其第一条记录命名，所以同一批次再写
一次是同一个对象；对象按目标的 `batch` 滚动，所以请给它比默认值大的批次。`s3` 源按键的顺序、分范围逐行读取 `prefix` 下的
对象，可在对象内部续读，并每隔 `poll_interval` 重新列出上次之后的键：键必须随新增对象而增大，就像该目标生成的键那样。没有
`endpoint` 时写 AWS；MinIO、SeaweedFS、Ceph、R2 等兼容存储要设置它。请求用 Signature Version 4 签名。

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines logs-to-s3 {
		source file { paths /var/log/app/*.log }
		queue { max_records 200000; max_bytes 268435456 }
		sinks archive {
			sink s3 {
				endpoint https://minio.internal:9000
				bucket logs
				access_key_id fit-writer
				secret_access_key_env S3_SECRET
				codec json
				gzip true
				key app/{yyyy}/{mm}/{dd}/{timestamp}-{position}{ext}
			}
			batch { max_records 100000; max_bytes 134217728; max_wait 5m }
		}
	}
}
```

目标的凭据需要对该前缀有 `s3:PutObject` 权限；源的需要 `s3:ListBucket` 和 `s3:GetObject`。

### InfluxDB

该目标把每条插入和更新写成行协议的一个数据点：measurement 来自模板（`{namespace}`，或某个字段的值），`tags` 来自行的
字段，其余字段（或 `fields`）作为数据点的字段，时间来自 `time_field` 或提交时间，并截断到 `precision`。数据点由其序列
（measurement 和标签）和时间确定，所以同一记录再写一次会覆盖同一个数据点，更新就是数据点的新值。行协议没有删除：删除被跳过
（`deletes fail` 则交给 `on_error`）。`api` 选择端点：`v2`（`/api/v2/write`，`org` 和 `bucket`，令牌）、`v3`
（`/api/v3/write_lp`，`database`，bearer 令牌）、`v1`（`/write`，`database` 和 `retention_policy`，用户名和密码或令牌）。
请求经 gzip 压缩，大小受 `max_request_bytes` 限制；429 或 5xx 退避后重发，服务器拒绝的数据点（字段类型与列不同）只让它自己
的记录失败：被拒绝请求中的记录会逐条重发以找出它。支持 InfluxDB 2.7 和 3 Core，以及 InfluxDB 2 的 v1 API。

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines readings {
		source kafka { brokers kafka:9092; topics readings; codec json }
		sinks influx {
			sink influxdb {
				url http://influxdb:8086
				org iot
				bucket readings
				token_env INFLUX_TOKEN
				measurement "{kind}"
				tags device site
				time_field at
				precision ms
			}
			batch { max_records 5000; max_wait 1s }
		}
	}
}
```

标签只用取值有限的字段：每个标签值都是一个单独的序列。没有 `time_field` 也没有提交时间的记录用写入时的时间，重放时不会重复。

### TDengine

该目标经 taosAdapter 的 REST 接口（端口 6041）写入。`mode schemaless`（默认）把同样的行协议发到 `/influxdb/v1/write`：
measurement 是超级表，标签是其子表的标签，TDengine 在首次写入时创建它们及其列。`mode sql` 向你建好的超级表写
`INSERT INTO db.sub USING db.stable (tags) TAGS (...) (ts, columns) VALUES (...)`，子表名由 `subtable`（模板，如
`d_{device}`）或标签的哈希给出。一行由其子表和时间戳确定：再写一次会覆盖自身。`precision` 必须与数据库的一致（默认
`ms`）。TDengine 拒绝的行（类型不符、语法错误）只让它自己的记录失败；服务器忙时重试。

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines meters {
		source generate { namespace meters; records 1000 }
		sinks td {
			sink tdengine {
				url http://tdengine:6041
				username root
				password_env TD_PASSWORD
				database power
				mode sql
				measurement meters
				subtable "m_{id}"
				fields price count
				precision ms
			}
		}
	}
}
```

### Qdrant 与 Milvus

`qdrant` 目标把每条插入和更新 upsert 为 `collection`（模板，集合由你创建）中的一个数据点，向量来自 `vector_field`
（数字数组，默认 `embedding`；命名向量用 `vector_name`），行的其余字段（或 `payload` 指定的字段）作为 payload；删除则删除该
数据点。数据点的 ID 来自记录的键：无符号整数或 UUID 原样使用，其他键用其文本生成一个 UUID，每次都相同。写入等待 Qdrant 应用
（`wait=true`）；API 密钥放在 `api_key_env`。

`milvus` 目标经 RESTful API v2（Milvus 2.4 及以后、Zilliz Cloud）把行 upsert 为 `collection` 的一个实体，其
`primary_key`（`id`）取自键：单个整数为 `Int64`，其他键为其文本的 `VarChar`；删除按它删除。Milvus 会把 upsert 的键转换为
字段的类型，但删除的 filter 必须按该类型书写，所以该目标在第一次删除时读取集合的描述，按主键的类型写键（整数键对 `VarChar`
加引号，数字字符串对 `Int64` 写成数字）。集合由你创建，主键不能是
`auto_id`，行的字段（或 `fields`）必须在其 schema 或动态字段中。令牌放在 `token_env`，或用 `username` 和 `password_env`。

两者都只写一个批次中每个键的最后一条记录（删除和 upsert），所以一个请求中不会有重复的键；429、5xx 或 Milvus 忙时重试，被拒绝的
实体（向量维数不同、schema 缺少的字段）只让它自己的记录失败。

`embed` 变换计算向量：它把 `fields` 的文本连接起来，向兼容 OpenAI 的 embeddings API（`url`，其后加 `/embeddings`；不设置时为
本配置的 [llm.proxy](llm.zh-CN.md)）请求其向量，并写入 `into`（`embedding`）。一个批次的文本按 `batch_records` 分成多个请求，
同时最多 `max_parallel` 个，每个文本只发一次；向量按模型和文本的哈希缓存，上限为 `cache_bytes`，所以重放或只改了其他字段的
更新不会再请求。重试 `attempts` 次后仍失败的请求，以及 API 拒绝或长于 `max_text_bytes` 的文本，会让其记录失败并交给
`on_error`。删除和没有这些字段的记录原样通过。

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines docs-to-vectors {
		source postgres {
			address db.internal:5432
			user repl
			password_env PG_REPL_PASSWORD
			database app
			create true
			publication fit_docs
			slot fit_docs
			tables public.docs
		}
		transforms embed {
			url https://api.openai.com/v1
			model text-embedding-3-small
			api_key_env OPENAI_API_KEY
			fields title body
		}
		sinks qdrant {
			sink qdrant { url http://qdrant:6333; collection docs; payload id title }
			batch { max_records 256 }
		}
		sinks milvus {
			sink milvus { url http://milvus:19530; token_env MILVUS_TOKEN; collection docs; fields title embedding }
			batch { max_records 256 }
		}
	}
}
```

### 试用：generate、memory 和 stdout

`generate` 生成涵盖所有值类型的编号记录，每次都相同，按 `tx_size` 组成事务，生成 `records` 条后结束（或永不结束）；
`chunk_records` 把它拆分，用来试用作业。`memory` 把记录保存在一个有名字、有上限的存储中，供测试使用；`stdout` 把每条记录
打印为一行规范 JSON。它们就是[第一条流水线](#第一条流水线)所用的，也是试用变换或目标的便捷方式。

## 各个源对服务器的要求

| 源 | 服务器设置 | 用户的权限 |
| --- | --- | --- |
| `mysql` | 开启 `log_bin`，`binlog_format = ROW`，`binlog_row_image = FULL`；要按 GTID 续读需 `gtid_mode = ON` 和 `enforce_gtid_consistency = ON`（MySQL）；建议 `binlog_row_metadata = FULL`；binlog 保留时间长于流水线可能停止的时间（`binlog_expire_logs_seconds`） | `REPLICATION SLAVE`、`REPLICATION CLIENT` 以及表的 `SELECT`；一个其他副本都没有用的 `server_id` |
| `postgres` | `wal_level = logical`；每条流水线有空余的 `max_replication_slots` 和 `max_wal_senders`；发布了更新或删除的每张表有主键或 `REPLICA IDENTITY` | `REPLICATION`、表的 `SELECT`，`create` 时还需创建 publication 的权限（表的所有者；`FOR ALL TABLES` 需超级用户） |
| `mongo` | 副本集或分片集群（change stream 需要）；`full_document whenAvailable` 或 `full_document_before_change` 需在集合上开启 `changeStreamPreAndPostImages`；oplog 窗口长于流水线可能停止的时间 | 数据库的 `read`（所有数据库则为 `readAnyDatabase`） |
| `tidb` | 在快照之前已启动的、写到 Kafka 的 TiCDC changefeed（`canal-json` 带 `enable-tidb-extension=true`，或 `simple`）；`tidb_gc_life_time` 长于快照所需时间 | 表的 `SELECT`；Kafka 源在该主题上的权限 |
| `mssql` | 数据库与表开启 CDC（`sys.sp_cdc_enable_db`、`sys.sp_cdc_enable_table`），SQL Server Agent 在运行（其捕获作业填充变更表），清理保留时间长于流水线可能停止的时间（默认三天）；无锁快照需要 `ALLOW_SNAPSHOT_ISOLATION ON`；`mode change_tracking` 时数据库与表开启 change tracking | 表与 `cdc` 架构的 `SELECT`（`db_owner`，或给 `sp_cdc_enable_table` 的门控角色）；用 change tracking 时表的 `VIEW CHANGE TRACKING` |
| `mqtt` | 持久会话保留足够久（EMQX 的 `session_expiry_interval`，Mosquitto 的 `persistence true` 与 `persistent_client_expiration`），fit 不在时排队的消息有足够空间（`max_mqueue_len`、`max_queued_messages`）；不要用 Mosquitto 的 `per_listener_settings true`，它会丢弃这些消息 | 主题的订阅权限 |
| `kafka` | 保留时间长于流水线可能停止的时间 | 主题的 `Describe` 和 `Read`（不需要消费者组的权限：不加入组） |
| `rabbitmq` | 持久队列（经典或仲裁）或流队列，由你创建；流的保留时间长于流水线可能停止的时间 | 队列的 `read`（不需要 `configure`：只做被动声明） |
| `nats` | 一个 JetStream 流，由你创建；`create_consumer` 时消费者由 fit 创建 | 向 `$JS.API.STREAM.INFO.<stream>`、`$JS.API.CONSUMER.*.<stream>.>`、`$JS.API.CONSUMER.MSG.NEXT.<stream>.<consumer>` 和 `$JS.ACK.<stream>.>` 发布，订阅 `_INBOX.>` |
| `pulsar` | 保留时间长于流水线可能停止的时间（`mode reader` 不在 broker 上保留任何东西） | 主题的 `consume` |
| `redis` | 流的裁剪速度不快于流水线的读取 | 流的键上的 `+xread`（或 `+@stream`） |
| `elasticsearch` | 每次写入都让其值比上次更晚的游标字段（更新时间），以及表示删除的软删除字段 | 索引的 `read` 和 `view_index_metadata` |
| `clickhouse` | 随每次写入增长的游标列 | 表的 `SELECT` |
| `doris` | 随每次写入增长的游标列 | 表的 `SELECT` |
| `s3` | 随新增对象而增大的键 | `s3:ListBucket` 和 `s3:GetObject` |
| `file` | 以改名或拷贝后截断的方式轮转 | 文件及其目录的读权限 |

MySQL 的 `my.cnf` 和 SQL：

```ini
[mysqld]
server_id = 1
log_bin = mysql-bin
binlog_format = ROW
binlog_row_image = FULL
binlog_row_metadata = FULL
gtid_mode = ON
enforce_gtid_consistency = ON
```

```sql
CREATE USER 'fit_repl'@'%' IDENTIFIED BY '...';
GRANT REPLICATION SLAVE, REPLICATION CLIENT ON *.* TO 'fit_repl'@'%';
GRANT SELECT ON shop.* TO 'fit_repl'@'%';
```

PostgreSQL 在 `postgresql.conf` 中设置 `wal_level = logical`（需重启），然后：

```sql
CREATE ROLE repl WITH LOGIN REPLICATION PASSWORD '...';
GRANT SELECT ON public.orders, public.order_items TO repl;
CREATE PUBLICATION fit_orders FOR TABLE public.orders, public.order_items;  -- or create true
```

SQL Server 在数据库中执行，且 SQL Server Agent 在运行：

```sql
ALTER DATABASE shop SET ALLOW_SNAPSHOT_ISOLATION ON;
USE shop;
EXEC sys.sp_cdc_enable_db;
EXEC sys.sp_cdc_enable_table @source_schema = N'dbo', @source_name = N'orders', @role_name = N'fit_cdc_reader';
CREATE LOGIN fit_cdc WITH PASSWORD = '...';
CREATE USER fit_cdc FOR LOGIN fit_cdc;
ALTER ROLE fit_cdc_reader ADD MEMBER fit_cdc;
GRANT SELECT ON SCHEMA::dbo TO fit_cdc;
GRANT SELECT ON SCHEMA::cdc TO fit_cdc;
```

目标只需要写权限：表的 `INSERT`、`UPDATE` 和 `DELETE`（MySQL、PostgreSQL），`checkpoint sink` 的表还需 `CREATE`；
数据库的 `readWrite`（MongoDB）；`Write`，以及 `create_topics` 时的 `Create`（Kafka）；表的 `LOAD`（Doris；StarRocks 为 `INSERT`）；交换机的 `write`（RabbitMQ）；主题的发布权限和 `_INBOX.>` 的订阅权限（NATS）；`produce`，`dedup read_back` 时还需 `consume`（Pulsar），其 broker 去重（`brokerDeduplicationEnabled`）让生产者的序列 ID 跨重启延续。SQL 和 ClickHouse 目标中的表和
索引由你创建：DDL 只报告，从不重放。

## 系统之间的类型

源把服务器的类型读成记录的值，目标把值写成其服务器的类型：

| 值 | 来自 MySQL | 来自 PostgreSQL | 来自 MongoDB | 写入 MySQL | 写入 PostgreSQL | 写入 Elasticsearch、ClickHouse、JSON |
| --- | --- | --- | --- | --- | --- | --- |
| null | `NULL` | `NULL` | null | `NULL` | `NULL` | `null` |
| bool | （`BOOL` 即 `TINYINT`：int） | `boolean` | bool | 1 或 0 | `boolean` | `true`、`false` |
| int | 有符号整数、`YEAR` | `smallint`、`integer`、`bigint`、`oid` | int32、int64 | 整数 | 整数、`numeric` | 数字 |
| uint | 无符号整数、`BIT` | | | 整数 | 整数、`numeric` | 数字 |
| float | `FLOAT`、`DOUBLE` | `real`、`double precision` | double | `FLOAT`、`DOUBLE` | `real`、`double precision` | 数字 |
| decimal | `DECIMAL` | `numeric` | decimal128 | `DECIMAL` | `numeric` | 数字 |
| string | `CHAR`、`VARCHAR`、`TEXT`、`ENUM` 和 `SET` 的名字、`TIME` | `text`、`varchar`，以及其他所有类型的服务器文本（`uuid`、`time`、`interval`、`inet`、枚举、范围） | string | 文本列 | `text`、`varchar`、枚举、`uuid`、`inet` …… | 字符串 |
| bytes | `BINARY`、`VARBINARY`、`BLOB`、`GEOMETRY` | `bytea` | 子类型为 0 的 binary | 二进制列 | `bytea` | base64 |
| time | `DATETIME`、`TIMESTAMP`（UTC） | `timestamp`（UTC）、`timestamptz` | datetime（UTC） | UTC 的 `DATETIME`、`TIMESTAMP` | `timestamptz`、`timestamp` | RFC 3339 |
| date | `DATE` | `date` | | `DATE` | `date` | `YYYY-MM-DD` |
| JSON | `JSON` | `json`、`jsonb` | | `JSON`（文本） | `json`、`jsonb` | 该文档 |
| array | | 数组 | array | `JSON`（文本） | 数组 | 数组 |
| map | | | 文档 | `JSON`（文本） | `json`、`jsonb` | 对象 |

MongoDB 中没有对应类型的值（ObjectId、时间戳、正则表达式、其他子类型的 binary 等）保存为带标签的 map，以 MongoDB 扩展
JSON 的名字命名，如 `{"$oid": "..."}`，使 `mongo` 目标能把它们原样写回；MongoDB 目标把 int 写成 int64，decimal 写成
decimal128（须精确，否则该记录失败），time 和 date 写成精确到毫秒的 datetime。ClickHouse 把 decimal 写成数字，按
`best_effort` 读取时间；Kafka、HTTP、S3 和文件目标写出的是其编解码器生成的内容。目标需要别的类型时，用 `cast` 变换在这些
类型之间转换字段。

## 变换

`transforms` 按顺序修改流向目标的记录：`filter` 按命名空间、操作和字段值保留或丢弃记录；`route` 改命名空间（表到索引、
主题）；`fields` 展开、嵌套、改名、删除、保留和设置字段；`key` 由字段构造键；`cast` 转换类型；`mask` 用带密钥的
HMAC-SHA256、SHA-256 或 null 替换字段；`envelope` 把变更做成文档（带元数据的扁平文档、前后两份，或 Debezium 的信封）；`embed` 经 embeddings API 由文本字段计算向量
（[Qdrant 与 Milvus](#qdrant-与-milvus)）。
删除记录经过所有这些变换后仍保留它的键。各变换的参数见[参考](reference/config.md#sync-transforms)。

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines shop {
		source generate { namespace shop.orders; records 100 }
		transforms filter { namespaces shop.*; exclude_namespaces shop.tmp_* }
		transforms route { rules { match shop.*; to shop-{rest} } }
		transforms key { fields id; format join }
		transforms mask { fields name; key_env SYNC_MASK_KEY }
		transforms envelope { meta { deleted __deleted }; deletes rewrite }
		sinks out { sink stdout }
	}
}
```

### 编解码器

编解码器把源读到的字节（文件的行、Kafka 消息、HTTP 请求体、S3 对象）变成记录，把记录变成目标写出的字节：`line`（默认）、
`json`、`csv`、`raw`、`canal_json`（Canal 和 TiCDC 的）、`debezium`（读取时按事件的 schema 定类型，写出时不带 schema）、
`ticdc_simple`（只读）和 `wasm`。编解码器拒绝的行仍会成为一条记录，保存该行和错误，所以读到的东西不会丢失。

## 插件

内置连接器覆盖不到的，用 WebAssembly 插件：在 fit 中由 wazero 运行的 WASI reactor。`wasm` 变换（处理每个批次：丢弃、
修改、拆分、改路由）、`wasm` 编解码器（自己的格式）、`wasm` 源（从检查点开始轮询 API）和 `wasm` 目标（写出每个批次）。
记录以长度加标签的二进制形式跨越边界，基于 guest SDK 构建的插件都采用它；不采用的插件（包括此前构建的）仍用规范
JSON。源和目标唯一的出口是 fit 的异步 HTTP 客户端（只能访问 `allow_hosts` 列出的主机）和 sleep：没有
套接字。每个插件运行在 `instances` 个各自独立内存的实例上，受 `max_memory_bytes` 和 `timeout` 限制；陷入 trap 或超出
限制的调用会让其记录失败，交给 `on_error`。

`plugin/wasm/guest` 包是这些调用的 Go 端（`OnTransform`、`OnDecode`、`OnEncode`、`OnPoll`、`OnWrite`、`Emit`、`Fail`、
`Row`），可用 Go 或 TinyGo（`tinygo build -target=wasip1 -buildmode=c-shared -scheduler=none`：TinyGo 默认的调度器让每次调用
多花数百微秒）构建；crate
`plugin/wasm/guest/rust`（`fit-guest`）是 Rust 端；fit 的测试会各运行一个这样的插件。fit-examples 中有变换
（`sync-wasm-transform`）、编解码器（`sync-wasm-codec`）、源（`sync-wasm-source`）和目标（`sync-wasm-sink`）的示例。
一个丢弃测试账号的变换：

```go
//go:build wasip1

package main

import (
	"strings"

	"github.com/lesismal/fit/plugin/wasm/guest"
)

func init() {
	guest.OnTransform(func(batch []guest.Record) error {
		for i := range batch {
			row, err := guest.ParseRow(batch[i].After)
			if err != nil {
				guest.Fail(i, err.Error())
				continue
			}
			v, _ := row.Get("email")
			if email, _ := guest.AsString(v); strings.HasSuffix(email, "@example.test") {
				continue // dropped: nothing emitted for it
			}
			guest.Emit(i, &batch[i])
		}
		return nil
	})
}

func main() {}
```

用 `GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o users.wasm` 构建后，以
`transforms wasm { path /etc/fit/plugins/users.wasm; config "{}" }` 运行。完整的示例（还会隐藏邮箱）在
[fit-examples](https://github.com/lesismal/fit-examples/tree/main/sync-wasm-transform) 中。

## 批处理作业

`mode once` 的流水线是批处理作业，就像 DataX 运行的那样：把源读到末尾，写好检查点后停止。能拆分快照的源（数据库按键的
范围、Elasticsearch 按 slice、`generate` 按 `chunk_records`）按块读取，每次 `job.max_parallel`（4）块，每个能同时接受
多路写入的目标得到同样多的写入者，同一个键总是交给同一个写入者。检查点保存作业的进度，所以因崩溃或重启而中断的作业从已
完成的块之后继续。完成的作业在 fit 重启时不会再运行；`job.schedule`（cron 的五个字段或 `@daily`，按 `job.time_zone`）
再次启动它，每次都是新的作业，从不与仍在进行的运行重叠。

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines nightly-copy {
		mode once
		source postgres {
			address db.internal:5432
			user reader
			password_env PG_PASSWORD
			database shop
			publication fit_orders
			slot fit_orders
			tables public.orders
		}
		sinks warehouse {
			sink clickhouse { urls http://ch1:8123; username fit; password_env CH_PASSWORD; table shop.orders }
			batch { max_records 10000 }
		}
		job { max_parallel 8; schedule "0 3 * * *"; time_zone Asia/Shanghai }
	}
}
```

`fit sync run -c fit.conf --once nightly-copy` 在命令行运行一个作业，不提供其他任何服务；所有记录都写入时以 0 退出，有记录
失败（`skip` 或 `dead_letter`）时以 3 退出，流水线停止时以 1 退出。

## Admin API、fit sync 与工作台

Admin API，以及控制台中 `/api/m/admin/` 之下的同一组端点，显示和操控运行中的流水线：

| 端点 | 作用 |
| --- | --- |
| `GET /sync/pipelines`、`/sync/pipelines/{name}` | 每条流水线的状态、检查点、队列与其上限、读取/确认/失败的记录数、吞吐、延迟、错误及最后一个错误、各目标的计数、死信 |
| `POST /sync/pipelines/{name}` `{"action": "pause"}` | 暂停源，已读到的仍会写完，检查点照常保存 |
| `{"action": "resume"}` | 让它继续；也会让因错误停止的流水线从检查点重新开始 |
| `{"action": "snapshot", "confirm": true}` | 从头重新读源（删除检查点） |
| `.../checkpoint` 的 `GET`、`PUT {"position", "confirm"}`、`DELETE ?confirm=1` | 显示、设置或重置检查点，流水线从那里重启 |
| `POST .../test` | 测试源和目标的连接，不写任何记录：`{"ok", "results"}`，每个结果为 `{"part", "module", "tested", "ok", "error", "warnings", "took_ms"}`，`warnings` 为能通过但会丢数据之处 |
| `POST .../dry-run` `{"limit", "timeout", "from": "start"}` | 经变换读取最初的记录（5s 内 10 条），返回每条变成了什么，不写任何东西，也不保存位置 |
| `POST /sync/test`、`/sync/dry-run` `{"pipeline": {...}}` | 对尚未应用的流水线配置做同样的事 |
| `.../dead-letters` 的 `GET`、`POST .../dead-letters/replay`、`DELETE` | 列出、重放和丢弃[死信](#死信) |

会跳过或重放记录的修改（把检查点设到另一个位置、在有检查点时重置或重做快照）若不带 `confirm`，答 409。`fit sync` 做同样的
事，它像 `fit token` 那样从引导配置中找到 Admin API：

```sh
fit sync status -c fit.conf
fit sync status -c fit.conf orders-out
fit sync pause -c fit.conf orders-out
fit sync resume -c fit.conf orders-out
fit sync test -c fit.conf orders-out
fit sync dry-run -c fit.conf -limit 5 -from-start orders-out
fit sync checkpoint -c fit.conf orders-out
fit sync checkpoint -c fit.conf -reset -confirm orders-out
fit sync snapshot -c fit.conf -confirm orders-out
fit sync dead-letters -c fit.conf -replay -all orders-out
```

工作台的 **Sync** 菜单提供同样的功能：**Pipelines**，每条流水线的状态、吞吐和延迟火花线，以及 **New pipeline**；流水线的
**Overview**（暂停、恢复、新快照、测试连接、试运行）、**Edit**（由 schema 生成的表单，其测试和试运行在保存之前检查草稿）、
**Checkpoint** 和 **Dead letters**；以及整个 `sync` 的 **Sync config**。每个会跳过、重放或丢失记录的修改都会先询问。
助手可以读取流水线、测试、试运行和列出死信，但不修改任何流水线：见[控制台的数据同步](console.zh-CN.md#数据同步)。

## 指标与日志

Admin API 的 `/metrics` 以 `fit_sync_*` 输出每条流水线的指标，标签为 `pipeline`（以及 `sink`）：

| 指标 | 类型 | |
| --- | --- | --- |
| `fit_sync_pipeline_state{state}` | gauge | 当前所处状态为 1：`starting`、`running`、`paused`、`retrying`、`finished`、`stopped`、`scheduled`、`killed` |
| `fit_sync_records_read_total`、`fit_sync_read_bytes_total` | counter | 源读取的记录数及其大小 |
| `fit_sync_records_acknowledged_total`、`fit_sync_records_failed_total` | counter | 所有目标都已拿到的记录数，以及失败的记录数 |
| `fit_sync_errors_total` | counter | 失败的写入、变换失败、未能写入的死信、源的失败 |
| `fit_sync_queue_records`、`fit_sync_queue_bytes`、`fit_sync_queue_max_records`、`fit_sync_queue_max_bytes` | gauge | 队列与其上限 |
| `fit_sync_lag_seconds` | gauge | 最后确认的记录从提交时间到被确认的时长 |
| `fit_sync_last_commit_timestamp_seconds` | gauge | 最后确认的记录的提交时间 |
| `fit_sync_source_lag` | gauge | 落后于源服务器的量，以源自己的单位：PostgreSQL 复制槽的 WAL 字节数、MySQL 的 binlog 字节数、Kafka 的 offset 数、RabbitMQ 队列就绪的或 JetStream 消费者待处理的消息数、SQL Server 的 CDC 事务数或 change tracking 版本数 |
| `fit_sync_sink_records_total`、`fit_sync_sink_bytes_total`、`fit_sync_sink_batches_total`、`fit_sync_sink_errors_total`、`fit_sync_sink_retries_total`、`fit_sync_sink_failed_total` | counter | 每个目标：写入量、尝试的批次、失败、重试、失败的记录 |
| `fit_sync_dead_letters_total`、`fit_sync_dead_letters_replayed_total`、`fit_sync_dead_letters_dropped_total` | counter | 写入、重放和丢弃的死信 |
| `fit_sync_dead_letters_kept`、`fit_sync_dead_letters_kept_bytes` | gauge | 保存着的死信 |

吞吐即这些计数器的 `rate()`。`sync` 日志记录器记录流水线的启动（从检查点或从源的开头）、等待其定时、被暂停、恢复、杀死、
完成或停止，以及它的错误——每 10s 至多一次，并统计期间被压下的条数。

## 死信

使用 `on_error dead_letter` 和 `dead_letters` 目标时，每条仍然失败的记录连同其错误、失败的位置（目标或变换）和尝试次数
保存在 `<state_dir>/<pipeline>.dead_letters.jsonl` 中，同步到磁盘后才算写入。该文件有上限：`max_records`（10,000）和
`max_bytes`（64 MiB）；满了之后新的死信被拒绝，流水线停止，不丢失任何东西，除非设置 `when_full drop_oldest`。

```fit
sync {
	state_dir /var/lib/fit/sync
	pipelines orders {
		source generate { records 100 }
		sinks out { sink stdout }
		on_error dead_letter
		dead_letter { name errors; sink dead_letters { max_records 50000; when_full refuse } }
	}
}
```

原因修复后（映射、列、约束），`fit sync dead-letters -replay -all orders`（或工作台的 **Dead letters**）会停止流水线，把每条
死信写到它失败的那个目标（若是变换处理失败，则重新经过变换写到每个目标），丢弃写入成功的，保留并报告再次失败的，然后重新
启动流水线。重放按记录原样写入，会覆盖目标中该键此后的变更。`-drop` 丢弃死信。

## 运维

**新快照。** 数据库源用新快照重新读取其表：`fit sync snapshot -confirm` 或工作台的 **New snapshot** 删除检查点并重启
流水线，源按其参数开始（先快照，再变更）；作业则重新开始。在流无法续读时需要它：binlog 被清除或 oplog 滚过了检查点、
PostgreSQL 复制槽被删除、MongoDB 的流失效。幂等的目标会把这些行覆盖写在已有内容上；流水线无法跟随期间在源端被删除的行
会留在目标中。

**重置与位置。** `fit sync checkpoint -reset -confirm` 删除检查点（定时作业则等到下一次）；
`-set '{"lsn": "16/B374D848"}' -confirm` 设置检查点，形式与 `fit sync checkpoint` 显示的相同。两者都会先停止流水线，写入，
再重启它。设到某些记录之后会跳过它们；设到之前会重放它们。

**迁移流水线** 到另一个 fit：在原处停止它（从配置中移除，或停止 fit），把 `<state_dir>/<name>.json`（及其死信）拷贝到新
fit 的 `state_dir`，再在那里添加它。PostgreSQL 复制槽或 MySQL 的 `server_id` 属于这条流水线：不要在两个 fit 上运行同一条
流水线。

**移除流水线** 会留下其检查点文件，以后添加的同名流水线从它开始；要从头开始就删除这个文件。删除它创建的 PostgreSQL 复制槽，
因为没人读的复制槽会无限保留 WAL。

**故障。** 停摆的目标会让其流水线等待：队列填满，源等待，延迟增长（`fit_sync_lag_seconds`、`fit_sync_source_lag`），目标
恢复应答后流水线从原处继续。因 `on_error stop` 而停止的流水线等待 `fit sync resume`（在修复之后）、配置变更或重启；状态和
日志会说明是哪条记录、为什么。源失败时，只要还在失败就从检查点重启，每次失败都会计数并记日志。崩溃或 `kill -9` 不会丢失
任何东西；检查点之后的记录会被再写一次。

**上限。** 流水线持有的一切都有上限：队列（记录数和字节数）、每个批次、一行或一个事件（`max_line_bytes`）、HTTP 源的待处理
请求和字节数、插件的内存和时间、保存的死信。源服务器在此期间保留的东西不归 fit 限制：流水线停止时，PostgreSQL 复制槽会保留
WAL，MySQL 会保留 binlog，所以请关注 `fit_sync_source_lag` 和 `lag_warning` 日志，并删除不再读取的东西。批次大的目标
（S3、ClickHouse）请调大 `queue`，服务器能承受更快速度的作业请调大 `job.max_parallel`。

**安全。** 密码和令牌来自环境变量，从不出现在配置或其历史中；TLS 会被校验；HTTP 源经路由访问，所以请在 `sync_ingest` 前
放认证和限流。流水线登录所用的用户只需要上面表格中的权限。

## 互操作

`test/sync/run.sh` 在 Docker 中启动这些服务器，并针对它们运行连接器的互操作测试：Elasticsearch 8 和 OpenSearch 2、
ClickHouse 25.8、MySQL 5.7、8.0 和 8.4、MariaDB 10.11 和 11、TiDB（单机，以及 PD、TiKV、TiDB 和 TiCDC 组成的集群）、
PostgreSQL 13 到 17、MongoDB 6、7 和 8（副本集）、Kafka 3.9 和 Redpanda、Redis 7 和 Valkey 8、MinIO 和 SeaweedFS；以及第二批的
RabbitMQ 3.13 和 4.1、NATS 2.10 和 2.11（三节点集群）、Pulsar 3 和 4、Mosquitto 2 和 EMQX 5、SQL Server 2019 和 2022、Doris 2.1 和
StarRocks 3.4、Loki 3.5、OpenTelemetry Collector、InfluxDB 2.7 和 3 Core、TDengine 3.3、Qdrant 1.15 和 Milvus 2.5。每个测试
在随机时刻杀掉流水线，并在最后把目标与源比较；`test/sync/exactly_once_test.go` 统计写入的每一行，以检查恰好一次。

**互操作与恢复矩阵**（`TestMatrix`，`test/sync/matrix_test.go`）让每个源同时写入一组固定的目标：PostgreSQL 17、MySQL 8.4、
MongoDB 8、Elasticsearch、ClickHouse（目标的默认值：按提交时间做版本的 `ReplacingMergeTree(_version, _deleted)`，删除用标记）和 Redis（每行一个键）。
源有 PostgreSQL、MySQL 和 MariaDB、MongoDB、经 TiCDC 的 TiDB，以及 Kafka 中的 Debezium 事件。每个源先有一张表的行（它的
快照），然后写入插入、更新、删除、改键、NULL 和多变更事务组成的混合负载，同时流水线在随机的崩溃点被一次次杀掉，第一次在它的第一个检查点之前、目标中已有行时；最后每个
目标都必须逐行与源相同，否则测试列出每个目标中缺少、多出或不同的行。

服务器按族划分，每族是 CI 中一个单独的任务，并行运行（`SYNC_FAMILIES`）：

| 任务 | 服务器 | 测试 |
| --- | --- | --- |
| `search` | Elasticsearch、OpenSearch、ClickHouse、Redis、Valkey、MinIO、SeaweedFS | `TestElasticsearch*`、`TestClickHouse*`、`TestRedis*`、`TestS3*` |
| `mysql` | MySQL 5.7、8.0、8.4，MariaDB 10.11、11，单机 TiDB | `TestMySQL*` |
| `postgres` | PostgreSQL 13 到 17 | `TestPostgres*` |
| `mongo` | MongoDB 6、7 和 8 | `TestMongo*` |
| `kafka` | Kafka（三个 broker）、Redpanda | `TestKafka*` |
| `tidb` | PD、TiKV、TiDB、TiCDC，在 Kafka 上；PostgreSQL 17 | `TestTiDB*` |
| `mq` | RabbitMQ 3.13 和 4.1、NATS 2.10 和 2.11、Pulsar 3 和 4、Mosquitto 2、EMQX 5 | `TestRabbitMQ*`、`TestNATS*`、`TestPulsar*`、`TestMQTT*` |
| `mssql` | SQL Server 2022 和 2019（开启 SQL Server Agent、CDC 与 change tracking） | `TestMSSQL*` |
| `olap` | Doris 2.1（一个前端和一个后端）、StarRocks 3.4 | `TestDoris*` |
| `obs` | Loki、OpenTelemetry Collector、InfluxDB 2 和 3、TDengine、Qdrant、Milvus；PostgreSQL 17 与 Mosquitto | `TestLoki*`、`TestOTLP*`、`TestInfluxDB*`、`TestTDengine*`、`TestQdrant*`、`TestMilvus*`、`TestEmbed*` |
| `matrix` | 每种一个：PostgreSQL 17、MySQL 8.4、MongoDB 8、Elasticsearch、ClickHouse、Redis、Kafka、带 TiCDC 的 TiDB | `TestMatrix`，每个源一次 |
| `matrix-mq` | 矩阵的目标；RabbitMQ 4.1、NATS 2.11、Pulsar 4、Mosquitto | `TestMatrix`，各队列的源 |
| `matrix-db` | 矩阵的目标；SQL Server 2022、Doris | `TestMatrix`，SQL Server 与 Doris 的源 |

`matrix-mq` 与 `matrix-db` 即 `SYNC_FAMILIES=matrix`，`MATRIX_SERVERS` 中写上 `mq`，或 `mssql` 与 `olap`：写上第二批的族时只运行
其源。第二批的源是 RabbitMQ、NATS JetStream、Pulsar 和 MQTT 中的 Debezium 事件（键由 id 构造，因为它们的消息没有键）、经 CDC
读取的 SQL Server，以及按游标列轮询的 Doris（删除用 deleted 标记）。在 `obs` 中，`TestEmbedPostgresAcrossKills` 让矩阵的
PostgreSQL 源经 `embed` 写入 Qdrant 和 Milvus，`TestInfluxDBFromMQTT` 是物联网流水线（MQTT 到 InfluxDB），两者都跨多次 kill。

工作流 `sync-matrix.yml` 每晚完整运行矩阵：每个源一个任务，在同类的每个服务器上运行（PostgreSQL 13 到 17、MySQL 5.7
到 MariaDB 11、MongoDB 6 到 8、TiDB、Kafka 和 Redpanda、RabbitMQ 3.13 和 4.1、NATS 2.10 和 2.11、Pulsar 3 和 4、Mosquitto 与
EMQX、SQL Server 2019 和 2022、Doris），负载更大、杀得更多（`FIT_SYNC_MATRIX=full`）。在本地运行：

```sh
test/sync/run.sh -v                          # 所有族、所有测试，包括矩阵
SYNC_FAMILIES=postgres test/sync/run.sh -v   # 一族
SYNC_FAMILIES=matrix test/sync/run.sh -v     # 每种服务器一个的矩阵
SYNC_FAMILIES=matrix MATRIX_SERVERS="search mysql postgres mongo" PG_VERSIONS="13 14 15 16 17" \
  FIT_SYNC_MATRIX=full FIT_SYNC_MATRIX_SOURCES=postgres test/sync/run.sh -v   # 每晚的一个任务
```

`SYNC_PREFIX` 和脚本列出的 `*_PORT` 变量可以在第一套旁边再运行一套；`KEEP=1` 保留容器供下次运行。

每族的服务器都有自己的脚本，由 `run.sh` 调用，也可单独运行：`eval "$(test/sync/<脚本>.sh up)"` 启动它们并打印测试读取的
变量，`down` 移除它们。`mq` 是 `rabbitmq.sh`、`nats.sh`、`pulsar.sh` 和 `mqtt.sh`（`MQ_SYSTEMS` 可选其中一些），`mssql` 是
`mssql.sh`（amd64 镜像，在 arm64 上模拟运行），`olap` 是 `olap.sh`（Doris 原样占用其端口：一次只能运行一套），`obs` 是
`obs.sh`，它也可以单独运行其测试：

```sh
SYNC_FAMILIES="mq obs" test/sync/run.sh -v
eval "$(test/sync/mssql.sh up)" && go test ./test/sync -run 'TestMSSQL' -v
test/sync/obs.sh -v
```
