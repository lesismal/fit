# 日志

[English](11-logs.md) | [简体中文](11-logs.zh-CN.md)

## 功能

**日志** 这一页看的是文件：fit 自己的日志与访问日志各自写出的文件、每个文件按什么切割、保留多少、磁盘上现在
有哪些文件、各自多大、什么时候改过，以及两个由人立即执行的动作——**立即切割**与**立即清理**。

设置本身在这一页下方的表单里编辑：`log` 与 `access_log` 的表单（含 `time_zone` 与 `rotate`），走配置的共享
草稿，保存栏与原始文本标签页与其他页面相同。`rotate`、`time_zone` 与级别随重载生效；`log` 与 `access_log`
的 `output` 与 `format` 都要重启，`access_log` 的 `flush_interval` 与 `buffer_bytes` 也一样（重启之前，
[一致性检查](10-consistency-check.zh-CN.md)会把这类修改列为不一致）。

文件写在它自己的写入协程上：磁盘慢时超出的行被丢弃并计数，不会拖住记录它的请求。切割、压缩与删除旧文件也在那条
协程与 fit 的文件池上完成。同一个路径可能既是 fit 自己日志的文件，又是访问日志的文件：那是一个 sink，所以两者的
`rotate` 或 `time_zone` 不同时，`fit validate` 会拒绝。

## 页面与操作

### 日志

页面顶部的一行说明：这些是 fit 自身日志与访问日志写出的文件、各自的切割与保留；没有 `rotate` 的文件输出会像
引入切割之前一样无限增长，fit 启动时会说明这一点。没有日志写文件时，页面说：没有日志写文件，请在下方表单设置
`log.output` 或 `access_log.output`。

下面每个文件输出一个卡片（`GET /logs` 读出的就是这些）：

- 访问日志的文件，标题是 **访问日志**；`log` 各 logger 的文件，标题是 **fit 自身的日志**。卡片第一行是路径、
  写这里的各 logger 的徽标（`server`、`admin`、`engine` 等，访问日志为 `access_log`）、格式徽标与时区徽标。
  格式在多个 logger 之间不一致时显示为 `mixed`。两者都写的路径会列两次，两个标题下各一张卡片，文件相同。
- 一行说这个输出的切割与保留：达到 `max_bytes` 时切割、每 `interval` 切割、保留 `max_age`、最多 `max_files`
  个文件、最多 `max_total_bytes`、gzip。没有 `rotate` 时这里是一条警告：这个文件会无限增长。
- 有被丢弃的行（"已丢弃 N 行"）或最近一次写入失败时，在下面以红色提示显示。
- 一张文件表：**文件**、**大小**、**修改时间**；正在写的那个带**当前**徽标。切割过的文件按切割时的 UTC 时间
  命名，如 `fit.log.2026-11-05T04-00-00.000`（该名字已被占用时加 `-1`、`-2` 等），`compress` 开着时加
  `.gz`。表下面是一行"共 N 个文件，大小"。
- 两个按钮：**立即切割** 与 **立即清理**。

**立即切割** 让那个文件现在就转一个新文件（`POST /logs/rotate`，请求体 `{"path"}`）：写入队列先被写完，然后
在写入协程上切割，页面提示"已切割 <路径>。"。只有设置了 `rotate` 的输出、且当前文件不为空时才会真的切割；
否则请求照样成功，但什么都不变。

**立即清理** 先预览：`POST /logs/cleanup` 带 `dry_run`，列出超过保留的文件。没有时页面提示"没有超过保留的切割
文件。"；有时询问"删除 N 个切割文件（大小）？"，然后不带 `dry_run` 再发一次，并提示删了几个文件、多少字节。
清理只删切割产生的文件，从不删正在写的那个；超过 `max_age`、`max_files` 或 `max_total_bytes` 的最老的被删。

只有运行中配置写出的那些路径能被列出、切割或清理：别的路径得到 `400`（"is not a log file of the running
config"），别的文件不在这一页上，也不会被删。在集群设置了角色的管理端上（[集群](12-cluster.zh-CN.md#用户与审计)），
只有集群范围的管理员才能在这里切割、清理或保存。

页面下方是 **设置**：`log` 与 `access_log` 的表单，与任何其他页面一样有 `.conf`、`.yaml`、`.json` 标签页和
共用的保存栏。这一页用到的接口（Admin API 的接口在控制台上都在 `/api/m/admin/` 下）：

| 接口 | |
| --- | --- |
| `GET /logs` | 每个文件输出：路径、logger、格式、时区、切割与磁盘上的文件 |
| `POST /logs/rotate` | 立即切割一个输出：`{"path"}` |
| `POST /logs/cleanup` | 清理超过保留的文件，或先报告：`{"path", "dry_run"}` |

## 参数

`rotate` 的字段（`log.rotate`、`log.loggers.<name>.rotate` 与 `access_log.rotate` 是同一组；logger 自己的
`rotate` 只在它的 `output` 是自己单独的文件时才用，否则与 `log.rotate` 共用）：

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `max_bytes` | 1 GiB | 文件长到多大就切割：一次写入会超过它就开新文件；0 不按大小切割 |
| `interval` | 关 | 写多久后切割，按 `time_zone` 的下一个周期边界：`24h` 在本地午夜，`1h` 在整点；不足一秒或超过 24h 且不是整天数的周期被拒绝 |
| `max_age` | `720h`（30 天） | 一个切割文件自最后写入后保留多久；0 表示只按数量或大小删 |
| `max_files` | 无 | 最多保留多少个切割文件，最新的在前；0 或不写表示不限个数 |
| `max_total_bytes` | 1 GiB | 切割文件最多共多少，超出的最老被删；0 表示不限总量 |
| `compress` | `true` | 切割时 gzip |

表单里与切割一起决定显示的两个字段：

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `time_zone` | `local` | 行时间与按 `interval` 切割所用的时区：`local`、`utc` 或 IANA 名（如 `Asia/Shanghai`）；随重载生效 |
| `output` | `log`：无（info 与 debug 到 stdout，warn 与 error 到 stderr）；`access_log`：`stdout` | 只有写成文件路径的输出才出现在这一页上；两者的修改都要重启 |

完整表：[log](../reference/config.md#log)、[access_log](../reference/config.md#access-log) 与
[log.rotate](../reference/config.md#config-rotate)。

## .conf 配置

一份完整、能通过 `fit validate` 的配置：`log` 与 `access_log` 都写文件、都带 `rotate`，加一个最小的监听与
路由。

```fit
schema_version 1

log {
	level info
	output /var/log/fit/fit.log
	format line
	time_zone Asia/Shanghai
	rotate {
		max_bytes 100MiB
		interval 24h
		max_age 720h
		max_files 30
		max_total_bytes 1GiB
		compress true
	}
	loggers {
		engine {
			level warn
			output /var/log/fit/engine.log
			format json
			rotate { max_bytes 50MiB; max_total_bytes 500MiB }
		}
	}
}

access_log {
	output /var/log/fit/access.log
	format json
	level info
	time_zone Asia/Shanghai
	rotate {
		max_bytes 1GiB
		interval 24h
		max_age 720h
		max_total_bytes 5GiB
		compress true
	}
}

listener web :8080

route { handler respond { body "hello from fit" } }
```

## 常见情形

- **文件在无限增长**：卡片上那条警告说的就是这个。设 `rotate`：按大小（`max_bytes`）或按时间（`interval`）
  切割，再设保留（`max_age`、`max_files`、`max_total_bytes`）。保存并应用后随重载生效。
- **只想留 30 天**：`max_age 720h` 是默认，配合 `interval 24h` 就是每天一个文件、留 30 个。
- **只想限量**：`max_total_bytes` 会删最老的直到装得下；`max_files` 按个数删。
- **要立刻腾出空间**：**立即清理** 先预览（`dry_run`），确认窗口给出要删的文件个数与总大小，确认后才删。正在写的
  那个文件不会被删。
- **要换文件路径**：改 `output`、保存，再重启；重启之前页面列出的是新路径（还没有文件），fit 仍在写旧路径，
  一致性检查会把它报为不一致。重启之后旧路径不再被列出，它的文件留在磁盘上。
- **让某个 logger 单独写一个文件**：在 `log.loggers.<name>` 下给它 `output` 与 `rotate`（如上例中的 `engine`），
  它会有自己的一张卡片。

日志文件按 0640 创建（fit 的用户可写、其组可读），切割后的新文件沿用被它替换的那个文件的权限；一次切割失败时一分钟后重试，
保留的检查每 10 分钟跑一次。

## 相关文档

- [配置参考](../reference/config.zh-CN.md)：[Log](../reference/config.md#log) 与
  [Access log](../reference/config.md#access-log)。
- [控制台与 store](../console.zh-CN.md#页面)：日志页在菜单中的位置。
- [全局设置](07-global-settings.zh-CN.md)：`log` 与 `access_log` 的其余字段。
- [模板](../../templates/README.zh-CN.md)：
  [`30-observability.conf`](../../templates/conf/conf.d/30-observability.conf)。
- 相邻章：[功能导航栏](03-navigation.zh-CN.md)、[一致性检查](10-consistency-check.zh-CN.md)、
  [集群](12-cluster.zh-CN.md)（跨成员的日志）。
