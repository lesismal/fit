# 同步基准测试

[English](README.md) | [简体中文](README.zh-CN.md)

同步基准测试（dev-plan I193）将 fit 的 [sync](../../docs/sync.zh-CN.md) 与它所替代的工具对比：文件对比
Filebeat，MySQL 与 PostgreSQL 的变更对比 Canal 和 Debezium，批量复制对比 DataX。每个搬运者（mover）都在
Docker 中运行，面对使用相同固定镜像与设置的全新服务器，绑定到相同的 CPU，并以相同方式测量：在它写入的地方
清点它搬运的记录。

| 场景 | 源 → 目标 | 搬运者 |
| --- | --- | --- |
| `files` | 文件中的行（`LINE_BYTES`、`FILES`）→ 一个文件 | fit（`file` → `file`）、Filebeat 8.17.4（`filestream` → `output.file`） |
| `mysql-es` | MySQL 8.0 的 binlog → Elasticsearch 7.17 | fit、Canal 1.1.8（canal-server + client adapter `es7`）、Debezium 3.0.8（MySQL 连接器 → Kafka → Confluent Elasticsearch sink 14.1.2，同一个 Kafka Connect） |
| `mysql-kafka` | MySQL 8.0 的 binlog → Kafka 3.9 | fit（Debezium 信封）、Canal 1.1.8（Kafka 模式，flat message）、Debezium 3.0.8（无 schema 的 JSON） |
| `pg-kafka` | PostgreSQL 17 的逻辑复制 → Kafka 3.9 | fit、Debezium 3.0.8（`pgoutput`） |
| `mysql-batch` | 一张 MySQL 表 → 另一台 MySQL | fit（作业：`mysql` 快照 → `mysql`）、DataX 202309（`mysqlreader` → `mysqlwriter`） |

行为 sysbench 的表结构（`id`、`k`、`c CHAR(120)`、`pad CHAR(60)`，约 200 字节），以每个事务 `TX_ROWS` 条单行
INSERT 写入。

## 运行

在装有 Docker（或 Docker Desktop）的主机上，于仓库根目录：

```sh
bench/sync/run.sh files                 # 一个场景，全部搬运者，一轮
bench/sync/run.sh -n 3 mysql-kafka      # 三轮，搬运者顺序交替
SIZE=smoke bench/sync/run.sh pg-kafka   # 极小规模，只检查能否跑通（数字没有意义）
bench/sync/plan.sh 2026-11-09           # 整个计划：每个场景 3 轮，并生成报告
```

`run.sh [-n ROUNDS] [-movers LIST] [-o DIR] [-name NAME] SCENARIO` 写出 `DIR/NAME.json`，并在 `DIR/NAME/`
中保存每轮结果、fit 的 CPU 与堆 profile、各搬运者的日志。规模与 CPU 由环境变量设置，完整列表见
[`run.sh`](run.sh) 开头与英文版表格（`SIZE`、`RECORDS`、`LINE_BYTES`、`FILES`、`TX_ROWS`、`STEADY_RATE`、
`STEADY_FOR`、`PAUSE`、`MOVER_CORES`、`AGENT_CORES`、`MOVER_CPUS`、`AGENT_CPUS`、`SERVER_CPUS`、`BIN`、
`CACHE`、`PREFIX`、`KEEP`）。

不绑定任何主机端口：服务器、搬运者与 syncbench 共用一个专属 Docker 网络（`fsb-net`），fit 的 Admin API 是
卷中的 Unix 套接字；运行结束或被中断时，删除它创建的一切（带 `fit-syncbench=fsb` 标签的容器、卷与网络）。
同时运行两次需要不同的 `PREFIX`。

### 在基准测试主机上

主机（192.168.100.51）有 Docker 而没有 Go：在本地构建、复制，再在主机上持基准锁运行。命令见英文版
“On the benchmark host”一节（构建 `fit`、`syncbench`、`benchreport` 为 linux/amd64，复制 `bench/sync`、
`bench/cmd` 与 `bin`，以 `FIT_REV`、`BIN`、`BENCHREPORT` 在 `flock /root/bench.lock` 下 `nohup` 运行
`plan.sh`）。运行前按 fit 计划的方式清理主机。首次运行会把 Canal adapter（291 MB）、DataX（1.7 GB，只保留引擎
与 MySQL 插件）和 Confluent Elasticsearch sink（43 MB）下载到 `CACHE`，按固定的 sha256 校验，并据此构建三个
镜像（[`images/`](images)）。完整规模 3 轮约需两小时。

## 测量内容

- `prepare` 建表（以及批量作业的数据与目标表）、索引（一个分片、无副本、每秒刷新、所有搬运者相同映射）和文件。
- `measure`：对**流**，先写预热事务直到目标计数上升（搬运者已跟上源），然后在搬运者容器**暂停**时写入
  `RECORDS` 行；恢复后它以自身速度读取积压。对**文件**与**批量**，记录在搬运者启动前就已存在：`run.sh` 只创建
  容器，syncbench 清点目标后再启动它们。
- **吞吐**：从目标中第一条被测记录到最后一条，计数的增长除以所用时间；**CPU**：同一时段内搬运者容器的 CPU；
  **峰值 RSS**：所有容器同时持有的匿名内存（cgroup v2 的 `anon`）的最大值，每 100ms 采样一次。
- **延迟**：随后的稳定阶段以 `STEADY_RATE` 条/秒写入 `STEADY_FOR`，每次写入的延迟为从提交到目标计数达到它
  （p50、p99、最大值）。
- 结束后再观察几秒计数：被计入两次的记录显示为 **extra**。
- 通过 Admin API 保存 fit 的 CPU profile（从排空开始 10 秒）与堆 profile（结束时）。
- `merge` 将一个项目的多轮合并，取每个搬运者按吞吐的中位轮，附带离散度与失败。

对齐的设置（Filebeat 的轮询与刷新等待、Kafka 信封与分区、批量的并行度与批大小、无 TLS、放宽的持久化、JVM 堆
保持镜像默认）以及 arm64（Apple M4）上 Java 的 SVE 问题，见英文版说明。

## 报告

`benchreport` 将同步结果渲染为每个场景一张表；`-plan` 给出权重与得分，`-scores` 给出总排名（规则 24）。
权重：S1 文件 25%，S2 MySQL→Elasticsearch 20%，S3 MySQL→Kafka 20%，S4 PostgreSQL→Kafka 15%，
S5 MySQL 批量复制 20%（无延迟项）。
