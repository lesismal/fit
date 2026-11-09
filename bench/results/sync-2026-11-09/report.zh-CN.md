# 同步基准测试，2026-11-09：报告

[English](report.md) | [简体中文](report.zh-CN.md)

I193 的第一份计划：在压测主机上（Ryzen 7 7800X3D，8 核 16 线程，Linux 7.0，Docker 29.1.3），fit 的同步与 Filebeat、
Canal、Debezium 和 DataX 在同样的服务器之间搬运同样的记录，一次一个搬运者。每个搬运者都绑定同样的四个物理核（cpuset），
服务器和驱动程序用其余的核。每项运行 3 轮；[tables.md](tables.md) 给出每张表的权重和每行的得分，[scores.md](scores.md)
给出排名，每项的 JSON 与日志都在旁边，并有 fit 每一轮的 CPU 与堆剖析。测试工具见 [bench/sync](../../sync/README.md)。
主机的 LLM 服务（strata）在每次运行时停止，运行后再启动。

S2 到 S5 在第一次运行发现问题并修复后重新运行（见下）；这里的数字都取自各项最后一次运行，fit 分别为 `a0425fcd`（S2）、
`18c4d8de`（S3、S5）和 `6afaa355`（S4）。S1 取第一次运行（fit `0b94708d`）：之后没有改动触及文件路径。

## 简而言之

- **fit 排第一，100 分中 98.1 分**，其后是 DataX（78.7，只参加批量复制）、Filebeat（42.4）、Debezium（40.2）和
  Canal（29.9）。fit 在每一项都得分最高。
- **fit 用更少的 CPU 和一小部分内存搬运更多记录。** 与各工作负载中其他搬运者最好的相比：
  - 文件（S1）：每秒行数是 Filebeat 的 1.3 到 3.1 倍，CPU 只用其四分之一到一半。
  - MySQL 到 Elasticsearch（S2）：每秒 5.04 万行，是 Debezium 的 1.9 倍、Canal 的 3 倍，用 0.44 核和 44 MiB（Debezium
    1.62 核、1.26 GB，Canal 2.4 GB）。
  - MySQL 到 Kafka（S3）：每秒 29.8 万行，是 Debezium 的 2.8 倍、Canal 的 3.7 倍。
  - PostgreSQL 到 Kafka（S4）：每秒 13 万行，比 Debezium 多 9%，CPU 只用其 45%，内存 20 MiB。
  - MySQL 批量复制（S5）：每秒 22.5 万行，比 DataX 多 6%，内存 44 MiB 对其 429 MiB。
- **fit 落后之处：**
  - S5 的 CPU：DataX 每 CPU 秒复制 38 万行，fit 32.8 万行（86%）。
  - S3 的延迟：Canal 的 p99 为 121 ms，fit 为 300 ms。fit 把记录攒成批（`batch.max_records` 条或第一条到达后
    `max_wait` 1s，先到者为准）再写出，Canal 按事务发送；每秒 2,000 行时 fit 的延迟主要是这段攒批。
  - S4 的吞吐几乎没有随 fit 更便宜的编码器提高（13 万，与之前相同）：其瓶颈不是 fit 的 CPU（1.76 核）；下一个要看的是
    源读取 pgoutput。

## 排名

| 名次 | 搬运者 | 得分 | S1 | S2 | S3 | S4 | S5 | 覆盖 |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | fit | **98.1** | 100.0 | 100.0 | 94.0 | 100.0 | 96.5 | 100% |
| 2 | DataX | 78.7 | — | — | — | — | 78.7 | 20% |
| 3 | Filebeat | 42.4 | 42.4 | — | — | — | — | 25% |
| 4 | Debezium | 40.2 | — | 38.5 | 26.7 | 60.6 | — | 55% |
| 5 | Canal | 29.9 | — | 31.0 | 28.8 | — | — | 40% |

这是第一份同步计划，没有更早的排名可比。权重：S1 25%、S2 20%、S3 20%、S4 15%、S5 20%；每项按吞吐 50%、CPU 效率 25%、
内存效率 15%、p99 延迟 10% 计分（S5 为 55%、25%、20%，无延迟），各为该项最佳值的比例（[scores.md](scores.md)）。

## fit 所处的位置

| 工作负载 | fit 的名次 | 对手 |
| --- | --- | --- |
| S1，文件（4 项：128 B 与 1 KiB 的行，1 与 16 个文件） | 每项第一 | Filebeat 28.7 到 56.3 |
| S2，MySQL 到 Elasticsearch | 第一 | Debezium 38.5，Canal 31.0 |
| S3，MySQL 到 Kafka | 第一（94.0：Canal 延迟更低） | Canal 28.8，Debezium 26.7 |
| S4，PostgreSQL 到 Kafka | 第一 | Debezium 60.6 |
| S5，MySQL 批量复制 | 第一（96.5：DataX 每行 CPU 更少） | DataX 78.7 |

## 运行中发现并修复的问题

计划要求发现的开销要么修复，要么记录；[dev-plan](../../../docs/dev-plan.zh-CN.md) 的 I193 一节给出每项及其数字。

- **测试工具**：syncbench 使用 Docker API 1.41，Docker 29 拒绝（`279aa357`）；Canal 的 Elasticsearch 适配器解析了复制时
  macOS 加入的 `._items.yml`，什么也没写（`f16b3b56`）。
- **fit 的批量复制（S5）**，第一次运行为每秒 14 万行、0.84 核，DataX 为 17.3 万：
  - 约四分之一的 CPU 用在无人需要的工作上：解析每个预处理插入的 4,000 个参数定义、快照位置经 `json.Marshal`、普通字符串
    经 `json.Marshal`（`f16b3b56`）；
  - 然后它在等待：mysql 目标只有一个写者；现在作业的写者跟随其分块读者，每批的 prepare、execute、close 换成一条文本语句，
    队列满时持有最早记录的写者立即写出（`18c4d8de`）。每秒 12.2 万行提高到 22.5 万行。
- **Debezium 编解码器（S3、S4）**把每个事件构造成 map 再经 `encoding/json`，占 S3 中 fit CPU 的 35%；现在手写编码，输出
  逐字节相同（`6afaa355`）：S3 从每秒 12.9 万行提高到 29.8 万行。
- **磁盘写满**：S2 第二次运行时主机磁盘被历次运行的匿名卷占满；Elasticsearch 变为只读并回答 429，fit 的目标默默等待。
  现在 429 持续 30 秒时会告警（`a0425fcd`）；匿名卷已删除，S2 重新运行。S3 和 S5 最后一次运行时磁盘正在写满（MySQL 和
  Kafka 仍正常工作；其数字有效）。
- **已记录、未做**：用缓存的预处理语句替代文本插入（未做，以保持客户端不留下打开语句的承诺）；canal-json 的编码器与
  Debezium 的模式相同，但不在任何基准的路径上；S4 的源。
</content>
</invoke>
