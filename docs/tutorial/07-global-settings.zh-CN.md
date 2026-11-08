# 全局设置

[English](07-global-settings.md) | [简体中文](07-global-settings.zh-CN.md)

## 功能

**全局配置**（菜单中的 Global）编辑配置里不属于某条路由、某个监听或某个角色的那些顶层成员：fit 自己的
日志、访问日志、遥测、指标、性能剖析、限制、引擎与配置来源。它是功能导航栏系统组的第一个模块，页面按这个
顺序排列，每个页面编辑一个成员，页面名就是成员名；最后两页是**其他**与**导入 / 导出**。

这些页面与其他编辑配置的页面一样（[功能导航栏](03-navigation.zh-CN.md)）：表单由嵌入的 fit JSON Schema
生成，每个字段带说明，可省略的成员带一个复选框，页面还有 `.conf`、`.yaml`、`.json` 三个标签页编辑同一份
草稿。**保存并应用** 带 `If-Match` 发送整份草稿，先校验再应用，所以与 `fit validate` 走同一组检查；旁边的
**检查** 是同一请求带 `?dry_run=1`，只检查不应用。

这一章的要点是哪些改动立刻生效、哪些要重启：`log` 的 `level`、`levels`、`time_zone` 与 `rotate` 随重载
生效，`output` 与 `format` 要重启；`access_log` 的 `level`、`time_zone` 与 `rotate` 随重载生效，其余要
重启；`telemetry`、`engine` 与 `providers` 在启动时读取，改了要重启；`pprof` 随每次应用生效，无需重启。

## 页面与操作

### 日志（log）

一个表单，编辑 `log`：`level` 与按 logger 的 `levels`、`output`、`format`、`time_zone`、`rotate`，以及
按 logger 的 `loggers`（某些 logger 自己的级别、输出、格式与切割）。fit 自己的日志说的是启动、加载与重载
配置、证书、provider 与引擎，不为每个请求写一行；各 logger 是谁、说什么，见
[配置参考的 Log 章](../reference/config.md#log)。保存并应用后级别、时区与切割立即生效，输出与格式要到下次
重启才变。磁盘上的文件与"已丢弃 N 行"在[日志](11-logs.zh-CN.md)页看。

### 访问日志（access_log）

编辑 `access_log`：`output`、`flush_interval`、`buffer_bytes`、`level`、`format`、`time_zone`、`rotate`。
一行一个请求，格式为 `json`（默认）、`text`（logfmt）或 `line`。`level` 按状态码过滤：400 以下是 info，
4xx 是 warn，5xx 是 error，所以 `warn` 只写被 4xx 与 5xx 应答的请求，`error` 只写 5xx；低于级别的请求
只付一次比较，没有行。缓冲满时超出的行被丢弃并计数，不会拖住请求。

### 遥测（telemetry）

编辑 `telemetry`：OTLP/HTTP 的 `endpoint`、`headers_file`、`ca_file`、`cert_file`、`key_file`、
`service_name`、`timeout`，以及 `traces`（`sample_ratio`、`max_queue_bytes`、`interval`）与
`metrics`（`interval`）。不写 `traces` 就没有链路，不写 `metrics` 就不经 OTLP 导出指标；`sample_ratio`
为 0 时只追踪调用方已抽样的请求。这一页的设置只在启动时读取：保存并应用后要到重启才生效。

### 指标（metrics）

`metrics` 没有字段：勾上这个成员（或写 `metrics {}`）就开启，按监听与路由统计请求，供 Admin API 的
`/metrics` 在 Prometheus 的文本格式下提供。它需要 `admin`，并且每个请求都要付出一点，所以默认关闭。页面
上就是那个复选框与三个文本标签页。

### 性能剖析（pprof）

编辑 `pprof`：`block_profile_rate`、`mutex_profile_fraction`、`max_duration`。这一页的**保存并应用** 立即
开启或关闭剖析，无需重启。剖析数据不在控制台上提供：它由 Admin API 在自己的地址（`admin.address`，默认
`127.0.0.1:2019`）的 `/debug/pprof/` 提供，在它的令牌之后；不写 `pprof` 时那些路径答 404。例如：

```sh
go tool pprof http://127.0.0.1:2019/debug/pprof/profile?seconds=30
```

`seconds` 不能超过 `max_duration`（默认 1m）。用完后把 `pprof` 从配置中移除：阻塞与互斥锁的采样率只在查看
时设置，因为采样对每个这样的事件都有代价。

### 限制（limits）

编辑 `limits`：`max_conns`、`max_header_bytes`、`max_body_bytes`、`max_pending_bytes`、
`max_write_queue_bytes`、`write_stall_timeout`。这些上限对每个监听都有效，不写的也有默认值，所以一份什么都不
写的配置仍然是有界的。`max_pending_bytes` 可以是字节数，也可以是 fit 可用内存的份额（如 `25%`）。
`max_conns`、`max_pending_bytes` 与 `max_write_queue_bytes` 只在启动时生效，重载不改；启动时 fit 在日志里
打印实际生效的上限与两个引擎的设置。单条路由的请求体上限由 `body_limit` 中间件另设。

### 引擎（engine）

编辑 `engine`：`event_loops`、`workers`、`min_workers`、`task_queue`、`poll_batch`、`read_buffer_size`、
`write_watermark`、`loop_wait`、`backlog`。fit 跑两个引擎：监听的那个服务客户端，上游连接走另一个，这样等
慢客户端的响应不会占住读上游的 worker。不写的字段取 fib 的默认，所以多数配置不写 `engine`；它是为跑在一台
忙机器上的服务调参用的。引擎在启动时建立，改了要重启（保存时会警告）。按端口看运行中的引擎与它提供的功能在
[引擎](06-engine.zh-CN.md)章。

### 配置来源（providers）

编辑 `providers`：一个列表，每项一个来源，用 `name` 命名（不写时就是它的 kind）：`docker`、`consul`、
`consul_kv`、`etcd`、`kubernetes`。合并规则：配置写的胜过每个 provider，一个 provider 胜过排在它后面的
那些；一个 provider 的元素若与前面的同名，就被前面的取走（被遮蔽的属于 info）。生效配置与每部分来自哪里，在
[JSON](08-json.zh-CN.md)章的生效配置标签看。这一页的设置只在启动时读取。

### 其他（other）

schema 里还没有专门页面的成员在这一页：`access`、`media_library`、`schema_version`、`sync`。每个成员一个
小节，与别的页面一样有原始文本标签页。`access` 是全局的允许与拒绝列表，每个监听、路由、服务、角色、Admin
API 与控制台都按它走，各自更具体的 `access` 先决定（[谁可以连接](../configuration.zh-CN.md#谁可以连接)）。
`media_library` 在媒体库模块也有自己的页面（[媒体库](22-media-library.zh-CN.md)章）。当每个成员都有了页面，
这一页会说"配置的每个成员都有自己的页面。"

### 导入 / 导出（transfer）

最后一页管整个配置：导出为 `.conf`、`.yaml`、`.json` 或 `.store`（加密的 fitstore），或导入一个文件替换
整个配置。导出的是已保存并在运行的配置；未保存的修改不在文件里。导入立即生效，走与保存相同的路径并写入
store；带页面成员的导入只取文件中的这些成员。`admin` 从不导出或导入。各页自己的**导出**与**导入**按钮只
处理该页的成员。详见[配置的导入与导出](../console.zh-CN.md#配置的导入与导出)。

## 参数

只列最常用的字段；完整字段表在配置参考对应章。

### log

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `level` | `info` | 每个 logger 的最低级别：`debug`、`info`、`warn`、`error`；随重载生效 |
| `levels` | | 按 logger 名的最低级别，覆盖 `level`；随重载生效 |
| `output` | | `stdout`、`stderr` 或文件路径；不写时 debug 与 info 到 stdout，warn 与 error 到 stderr；要重启 |
| `format` | `line` | `line`、`text`（logfmt）或 `json`；每条带 logger 名；要重启 |
| `time_zone` | `local` | 行时间与按 `interval` 切割所用的时区：`local`、`utc` 或 IANA 名；随重载生效 |
| `rotate` | | 文件切割与保留；不写则文件无限增长 |
| `loggers` | | 某些 logger 自己的 `level`、`output`、`format`、`rotate` |

完整表：[log](../reference/config.md#log)、[log.rotate](../reference/config.md#config-rotate)、
[log.loggers.*](../reference/config.md#config-logger)。

### access_log

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `output` | `stdout` | `stdout`、`stderr` 或文件路径 |
| `flush_interval` | `1s` | 一行在缓冲里最多等多久 |
| `buffer_bytes` | 4 MiB | 缓冲里最多多少字节；超出的行被丢弃并计数 |
| `level` | `info` | 按状态码的级别：4xx 是 warn、5xx 是 error；随重载生效 |
| `format` | `json` | `json`、`text` 或 `line`；要重启 |
| `time_zone` | `local` | 行时间与切割时区；随重载生效 |
| `rotate` | | 与 `log` 的 `rotate` 相同 |

完整表：[access_log](../reference/config.md#access-log)。

### telemetry

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `endpoint` | `http://127.0.0.1:4318` | OTLP/HTTP 接收方；traces 到 `/v1/traces`，metrics 到 `/v1/metrics` |
| `service_name` | `fit` | 资源的服务名 |
| `timeout` | `10s` | 一次导出最多多久；超时的批次被丢弃并计数 |
| `traces.sample_ratio` | 0 | 调用方没决定时抽样的份额，0 到 1 |
| `traces.interval` | `5s` | 多久导出一次，256 KiB 等着时更早 |
| `metrics.interval` | `30s` | 多久导出一次指标 |

完整表：[telemetry](../reference/config.md#telemetry)。

### metrics

没有字段：`{}` 就开启。它需要 `admin`，并让每个请求多付一点，所以不写时关闭。完整表：
[metrics](../reference/config.md#metrics)。

### pprof

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `block_profile_rate` | 0 | 阻塞剖析：平均每花这么多纳秒被阻塞记一个事件；1 为每个都记 |
| `mutex_profile_fraction` | 0 | 互斥锁剖析：每这么多有竞争的锁记一个；1 为每个都记 |
| `max_duration` | `1m` | 一次剖析最多要求多少秒；更长被 400 拒绝 |

完整表：[pprof](../reference/config.md#pprof)。

### limits

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `max_conns` | 100000 | 所有监听共用的客户端连接上限；超出的在接受时即关闭；只在启动时生效 |
| `max_header_bytes` | 1 MiB | 最大的请求头；更大的答 431 |
| `max_body_bytes` | 16 MiB | 最大的请求体；更大的答 413，已开始流式则结束连接 |
| `max_pending_bytes` | `50%` | 等慢客户端取的响应字节，跨所有连接；可以是字节数或内存份额；只在启动时生效 |
| `max_write_queue_bytes` | 32 MiB | 一个连接为客户端排队的字节上限；只在启动时生效 |
| `write_stall_timeout` | `30s` | 一次响应等客户端取数据最多等多久 |

完整表：[limits](../reference/config.md#limits)。

### engine

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `event_loops` | fib 的默认 | 每个引擎等事件用的 event loop 数；Windows 上总是 1 |
| `workers` | 每个 CPU 100 | 每个引擎的 worker 池同时最多跑多少 |
| `min_workers` | 0 | 空闲时保留的 worker 数 |
| `task_queue` | fib 的默认 | 每个池等 worker 的任务数 |
| `read_buffer_size` | 64 KiB | 一次从 socket 读进缓冲的字节，也是转发体一次写出的最多 |
| `write_watermark` | fib 按读缓冲与机器算 | 单连接的背压水位 |
| `backlog` | 系统的 | 每个监听 socket 未接受连接的队列长度 |

完整表：[engine](../reference/config.md#engine)。

### providers

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `name` | 该 kind 的名字 | 在生效配置与日志中用的名字 |
| `docker` | | 运行中容器标签里的路由、上游与服务；`endpoint` 默认 `unix:///var/run/docker.sock` |
| `consul` | | Consul 目录里服务标签；`endpoint` 默认 `http://127.0.0.1:8500` |
| `consul_kv` | | Consul KV 中 `prefix` 下的键；默认 `fit/` |
| `etcd` | | etcd 中 `prefix` 下的键；`endpoints` 默认 `http://127.0.0.1:2379` |
| `kubernetes` | | Gateway API 与 Ingress；`controller_name` 默认 `lesismal.github.io/fit` |

完整表：[providers](../reference/config.md#providers)。

## .conf 配置

一份完整、能通过 `fit validate` 的配置：`log`、`access_log`、`limits` 与 `engine`，加一个最小的监听与
路由。

```fit
schema_version 1

log {
	level info
	levels {
		engine warn
		tls info
	}
	output /var/log/fit/fit.log
	format line
	time_zone Asia/Shanghai
}

access_log {
	output /var/log/fit/access.log
	format json
	level warn
	time_zone Asia/Shanghai
}

limits {
	max_conns 50000
	max_header_bytes 64KiB
	max_body_bytes 16MiB
	max_pending_bytes 25%
	max_write_queue_bytes 32MiB
	write_stall_timeout 30s
}

engine {
	event_loops 4
	workers 512
	min_workers 8
	task_queue 2048
	read_buffer_size 64KiB
	write_watermark 262144
	backlog 4096
}

listener web :8080

route { handler respond { body "hello from fit" } }
```

## 相关文档

- [配置参考](../reference/config.zh-CN.md)：[Log](../reference/config.md#log)、
  [Access log](../reference/config.md#access-log)、[Telemetry](../reference/config.md#telemetry)、
  [Metrics](../reference/config.md#metrics)、[Profiling](../reference/config.md#pprof)、
  [Limits](../reference/config.md#limits)、[Engine](../reference/config.md#engine)、
  [Providers](../reference/config.md#providers)、[Access](../reference/config.md#access)。
- [控制台与 store](../console.zh-CN.md)：[配置的导入与导出](../console.zh-CN.md#配置的导入与导出)。
- [配置](../configuration.zh-CN.md)与[.conf 格式](../reference/fit.zh-CN.md)。
- [模板](../../templates/README.zh-CN.md)：
  [`00-runtime.conf`](../../templates/conf/conf.d/00-runtime.conf)、
  [`30-observability.conf`](../../templates/conf/conf.d/30-observability.conf)、
  [`50-providers.conf`](../../templates/conf/conf.d/50-providers.conf)。
- 相邻章：[功能导航栏](03-navigation.zh-CN.md)、[概览](04-overview.zh-CN.md)、
  [引擎](06-engine.zh-CN.md)、[JSON](08-json.zh-CN.md)、[日志](11-logs.zh-CN.md)。
