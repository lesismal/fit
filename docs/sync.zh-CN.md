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
[PostgreSQL 到 Kafka](https://github.com/lesismal/fit-examples/tree/main/sync-postgres-to-kafka) 和
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

### 投递保证

**至少一次。** 崩溃、重启或 `kill -9` 之后，最后一个检查点之后的记录会被重新读取、重新写入；检查点之前的不会丢失，因为
检查点从不越过所有目标都已拿到的位置。因此目标以幂等方式写入，重放后收敛到同一状态：

| 目标 | 记录再写一次的效果 |
| --- | --- |
| `mysql`、`postgres` | 按键 upsert 和 delete：行相同 |
| `mongo` | 按 `_id` upsert 和 delete：文档相同 |
| `elasticsearch` | 按由键生成的 `_id` 索引和删除：文档相同（没有键的记录由集群生成 `_id`，重放会重复） |
| `clickhouse` | 相同记录得到相同的去重令牌：服务器丢弃这次插入；否则由 `ReplacingMergeTree` 按版本合并 |
| `redis` | 键相同；`entry_id key` 时已存在的流条目被拒绝 |
| `s3` | 同一批次生成同一个对象，覆盖自身；重放若被切成不同的批次，会生成别的对象 |
| `kafka`、`http`、`file`、`stdout` | 再写一次：Kafka 的生产者不是幂等的；`http` 对相同记录发送相同的 `Idempotency-Key` |

**事务保持完整。** 检查点从不落在源事务内部；一个事务的记录带有其 ID，`mysql` 和 `postgres` 目标把每个源事务写成自己的
一个事务（`transactions source`），所以目标的读者永远不会看到半个事务。

**按键保持顺序**，从源到每个目标：目标可以并行写不同的键（作业的多个写入者），但同一个键从不乱序。

**恰好一次**，写入 MySQL、MariaDB、TiDB 和 PostgreSQL：`checkpoint sink` 时位置还会写入目标的一张表
（`_fit_sync_checkpoints`），与它所覆盖的记录在同一个事务中，流水线启动时优先读回它而不是文件。这样目标提交与文件下一次
检查点之间的崩溃不会让任何记录写两次。前提是流水线的每个目标都保存检查点，且位置相同：旁边有其他类型目标的流水线回退到
文件，即至少一次。仍可能写两次的情况：大于 MySQL 目标 `max_transaction_bytes` 的源事务（分段提交），以及按块读取的作业
（它只用自己的文件）。重置、手动设置位置和新的作业会开始新的谱系（lineage），所以目标中较旧的位置不会撤销它们；删除
流水线在表中的行和它的文件，就从头开始。

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
| [HTTP](#http) | `http`：POST 到 `sync_ingest` 路由的记录 | `http`：以 NDJSON 或 JSON 批量 POST |
| [MySQL、MariaDB、TiDB](#mysqlmariadb-与-tidb) | `mysql`：以副本身份读 binlog，无锁快照 | `mysql`：upsert 和 delete，保持源的事务 |
| [经由 TiCDC 读取 TiDB](#经由-ticdc-读取-tidb) | `tidb`：在一个时间戳上读快照，再从 Kafka 读 changefeed | （用 `mysql` 目标） |
| [PostgreSQL](#postgresql) | `postgres`：用 `pgoutput` 做逻辑复制，精确的快照 | `postgres`：`ON CONFLICT` upsert，`COPY` |
| [MongoDB](#mongodb) | `mongo`：按 `_id` 读快照，再读 change stream | `mongo`：按 `_id` 批量 upsert 和 delete |
| [Kafka、Redpanda](#kafka-与-redpanda) | `kafka`：每个分区从检查点中的 offset 读 | `kafka`：带键消息，批量、压缩 |
| [Redis、Valkey](#redis-与-valkey) | `redis`：用 `XREAD` 读流 | `redis`：`XADD`，或每条记录一个键 |
| [S3 及兼容存储](#s3-及兼容存储) | `s3`：前缀下的对象，按键的顺序，逐行 | `s3`：由行组成的对象，按大小和时间滚动 |
| [插件](#插件) | `wasm`：经 fit 的 HTTP 客户端轮询的插件 | `wasm`：经它写出的插件 |
| [试用](#试用generatememory-和-stdout) | `generate` | `memory`、`stdout` |

密钥从不写在配置里：每个连接器都给出保存其密码或令牌的环境变量名（`password_env`、`secret_access_key_env`、
`bearer_token_env`），在流水线加载时读取，所以 `fit validate` 也需要设置它。TLS 按系统根证书或 `ca_file` 校验，且不能
关闭；客户端证书为 `cert_file` 和 `key_file`。除 `wasm` 连接器（其可达范围是插件自己的）外，每个连接器的连接都可以在不写
任何记录的情况下测试（[测试连接](#admin-apifit-sync-与工作台)）。

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

### HTTP

`http` 源接收客户端 POST 到处理器为 [`sync_ingest`](reference/config.md#sync_ingest) 的路由的记录：JSON、NDJSON，或经
编解码器的任何格式。请求在所有目标都拿到其记录后答 `200`；流水线未运行或待处理请求已满时答 `503` 并带 `Retry-After`；
无法读取的请求体答 `400`，其中任何内容都不会写入。客户端只要在得到 `200` 之前一直重发，就不会丢失任何东西。处理器从不在
fib 的工作协程上等待。能访问这条路由的人都能向流水线写入，所以请在它前面放认证中间件。

`http` 目标把每个批次以 NDJSON 或 JSON 数组 POST 出去，带 `Idempotency-Key`，相同记录重发时它也相同；`2xx` 即确认，
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
会话的时区为 UTC。

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
| `kafka` | 保留时间长于流水线可能停止的时间 | 主题的 `Describe` 和 `Read`（不需要消费者组的权限：不加入组） |
| `redis` | 流的裁剪速度不快于流水线的读取 | 流的键上的 `+xread`（或 `+@stream`） |
| `elasticsearch` | 每次写入都让其值比上次更晚的游标字段（更新时间），以及表示删除的软删除字段 | 索引的 `read` 和 `view_index_metadata` |
| `clickhouse` | 随每次写入增长的游标列 | 表的 `SELECT` |
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

目标只需要写权限：表的 `INSERT`、`UPDATE` 和 `DELETE`（MySQL、PostgreSQL），`checkpoint sink` 的表还需 `CREATE`；
数据库的 `readWrite`（MongoDB）；`Write`，以及 `create_topics` 时的 `Create`（Kafka）。SQL 和 ClickHouse 目标中的表和
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
HMAC-SHA256、SHA-256 或 null 替换字段；`envelope` 把变更做成文档（带元数据的扁平文档、前后两份，或 Debezium 的信封）。
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
记录以规范 JSON 跨越边界。源和目标唯一的出口是 fit 的异步 HTTP 客户端（只能访问 `allow_hosts` 列出的主机）和 sleep：没有
套接字。每个插件运行在 `instances` 个各自独立内存的实例上，受 `max_memory_bytes` 和 `timeout` 限制；陷入 trap 或超出
限制的调用会让其记录失败，交给 `on_error`。

`plugin/wasm/guest` 包是这些调用的 Go 端（`OnTransform`、`OnDecode`、`OnEncode`、`OnPoll`、`OnWrite`、`Emit`、`Fail`、
`Row`），可用 Go 或 TinyGo（`tinygo build -target=wasip1 -buildmode=c-shared`）构建；crate
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
| `POST .../test` | 测试源和目标的连接，不写任何记录 |
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
| `fit_sync_source_lag` | gauge | 落后于源服务器的量，以源自己的单位：PostgreSQL 复制槽的 WAL 字节数、MySQL 的 binlog 字节数、Kafka 的 offset 数 |
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
PostgreSQL 13 到 17、MongoDB 6、7 和 8（副本集）、Kafka 3.9 和 Redpanda、Redis 7 和 Valkey 8、MinIO 和 SeaweedFS。每个测试
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
| `matrix` | 每种一个：PostgreSQL 17、MySQL 8.4、MongoDB 8、Elasticsearch、ClickHouse、Redis、Kafka、带 TiCDC 的 TiDB | `TestMatrix`，每个源一次 |

工作流 `sync-matrix.yml` 每晚完整运行矩阵：每个源一个任务，在同类的每个服务器上运行（PostgreSQL 13 到 17、MySQL 5.7
到 MariaDB 11、MongoDB 6 到 8、TiDB、Kafka 和 Redpanda），负载更大、杀得更多（`FIT_SYNC_MATRIX=full`）。在本地运行：

```sh
test/sync/run.sh -v                          # 所有族、所有测试，包括矩阵
SYNC_FAMILIES=postgres test/sync/run.sh -v   # 一族
SYNC_FAMILIES=matrix test/sync/run.sh -v     # 每种服务器一个的矩阵
SYNC_FAMILIES=matrix MATRIX_SERVERS="search mysql postgres mongo" PG_VERSIONS="13 14 15 16 17" \
  FIT_SYNC_MATRIX=full FIT_SYNC_MATRIX_SOURCES=postgres test/sync/run.sh -v   # 每晚的一个任务
```

`SYNC_PREFIX` 和脚本列出的 `*_PORT` 变量可以在第一套旁边再运行一套；`KEEP=1` 保留容器供下次运行。
