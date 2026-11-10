# 全局设置

[English](07-global-settings.md) | [简体中文](07-global-settings.zh-CN.md)

## 功能

**全局配置**（英文界面中的 Global）是紧跟**引擎**之后的菜单条目，编辑配置里不属于某条路由、某个监听或某个
角色的那些顶层成员：fit 自己的日志、访问日志、遥测、指标、性能剖析、限制、引擎与配置来源。页面按这个顺序
排列——**日志**、**访问日志**、**遥测**、**指标**、**性能剖析**、**限制**、**引擎**、**配置来源**，然后是
**其他**与**导入 / 导出**——地址为 `#/sys.config/<成员>`（`#/sys.config/log`、`#/sys.config/pprof`……
`#/sys.config/other` 与 `#/sys.config/transfer`）。前八页各编辑与其同名的成员（`pprof` 那页叫**性能剖析**）。

这些页面与其他编辑配置的页面一样（[功能导航栏](03-navigation.zh-CN.md)）：表单由嵌入的 fit JSON Schema
生成，每个字段带说明，可省略的成员带一个复选框（勾选即设置，取消即不写），每页还有 `.conf`、`.yaml`、`.json`
三个标签以文本编辑本页成员，旁边是只针对该成员的**导出**与**导入**。所有页面编辑同一份草稿。顶栏的
**保存并应用**带 `If-Match` 发送——整份草稿，或在文本标签中且没有其他未保存修改时只发本页成员——服务端按
`fit validate` 的同一组检查校验后作为新的配置版本应用；旁边的**检查**是同一请求带 `?dry_run=1`，只检查不应用。

这一章的要点是哪些改动立刻生效、哪些要重启：

| 成员 | 何时生效 |
| --- | --- |
| `log` | `level`、`levels`、各 logger 的级别、`time_zone` 与 `rotate` 保存即生效；`output` 与 `format` 下次重启 |
| `access_log` | `level`、`time_zone` 与 `rotate` 保存即生效；`output`、`flush_interval`、`buffer_bytes` 与 `format` 下次重启 |
| `telemetry`、`engine`、`providers` | 下次重启 |
| `limits` | `max_header_bytes`、`max_body_bytes` 与 `write_stall_timeout` 保存即生效；`max_conns`、`max_pending_bytes` 与 `max_write_queue_bytes` 下次重启 |
| `metrics`、`pprof` | 保存即生效 |

改动了只在启动时读取的设置时，保存照常应用，结果中列出警告，例如“engine changed; the change takes effect
when fit restarts”。`limits` 中要重启的三项同样会警告（“limits.max_conns changed; …”）；[一致性检查](10-consistency-check.zh-CN.md)
会列出值与引擎实际运行值不同的每一项，并提供把配置改回运行值的修复。

## 页面与操作

### 日志（log）

一个表单，编辑 `log`：`level` 与按 logger 的 `levels`、`output`、`format`、`time_zone`、`rotate`，以及
按 logger 的 `loggers`（某些 logger 自己的级别、输出、格式与切割）；同一个 logger 的级别不能既写在
`levels` 又写在 `loggers`。fit 自己的日志说的是启动、加载与重载配置、证书、provider 与引擎，不为每个请求
写一行；logger 有 `server`、`admin`、`tls`、`provider`、`telemetry`、`access_log`、`plugin`、`fastcgi`、
`engine`，以及各角色自己的（`llm`、`tunnel`、`webrtc`、`sync`、`bittorrent`），各是谁、说什么，见
[配置参考的 Log 章](../reference/config.md#log)。想多看某一方面时，在 `levels` 里把对应 logger 设为 `debug`
（如证书用 `tls debug`），`level` 保持 `info`。磁盘上的文件、立即切割与“已丢弃 N 行”在[日志](11-logs.zh-CN.md)页。

### 访问日志（access_log）

编辑 `access_log`：`output`、`flush_interval`、`buffer_bytes`、`level`、`format`、`time_zone`、`rotate`。
不写 `access_log` 就没有访问日志。一行一个请求，格式为 `json`（默认）、`text`（logfmt）或 `line`。`level`
按状态码过滤：400 以下是 info，4xx 是 warn，5xx 是 error，所以 `warn` 只写被 4xx 与 5xx 应答的请求，`error`
只写 5xx；低于级别的请求只付一次比较，没有行。缓冲满时超出的行被丢弃并计数，慢的输出不会拖住请求。文件输出
不写 `rotate` 时会无限增长。

### 遥测（telemetry）

编辑 `telemetry`：OTLP/HTTP 的 `endpoint`、`headers_file`、`ca_file`、`cert_file`、`key_file`、
`service_name`、`timeout`，以及 `traces`（`sample_ratio`、`max_queue_bytes`、`interval`）与
`metrics`（`interval`）。不写 `traces` 就没有链路，不写 `metrics` 就不经 OTLP 导出指标；`sample_ratio`
为 0 时只追踪调用方已抽样的请求。这一页的设置只在启动时读取：保存并应用后要到重启才生效。

### 指标（metrics）

`metrics` 没有字段：勾上这个成员（或写 `metrics {}`）就开启，按监听与路由统计请求并计时，供 Admin API 的
`/metrics` 以 Prometheus 文本格式提供；不开启时 `/metrics` 照样提供 fit 的其他指标。它让每个请求多付一点，
所以默认关闭。页面上就是那个复选框与三个文本标签。

### 性能剖析（pprof）

编辑 `pprof`：`block_profile_rate`、`mutex_profile_fraction`、`max_duration`。只勾上成员、不填字段时提供
剖析，但不采样阻塞与互斥锁。**保存并应用**立即开启或关闭剖析，无需重启。剖析数据不在控制台上提供：页面上的
提示说明，它由 Admin API 在自己的地址（`admin.address`，默认 `127.0.0.1:2019`）的 `/debug/pprof/` 提供，
需其令牌访问；不写 `pprof` 时那些路径答 404。例如：

```sh
go tool pprof http://127.0.0.1:2019/debug/pprof/profile?seconds=30
go tool pprof http://127.0.0.1:2019/debug/pprof/heap
```

`seconds` 不能超过 `max_duration`（默认 1m）。设置了 `admin.token_file` 时先带上令牌取回，再用
`go tool pprof` 打开该文件：

```sh
curl -H "Authorization: Bearer $TOKEN" -o cpu.pprof \
  "http://127.0.0.1:2019/debug/pprof/profile?seconds=30"
go tool pprof cpu.pprof
```

用完后把 `pprof` 从配置中移除：阻塞与互斥锁的采样率只在查看时设置，因为采样对每个这样的事件都有代价。

### 限制（limits）

编辑 `limits`：`max_conns`、`max_header_bytes`、`max_body_bytes`、`max_pending_bytes`、
`max_write_queue_bytes`、`write_stall_timeout`。这些上限对每个监听都有效，不写的也有默认值，所以一份什么都不
写的配置仍然是有界的。`max_pending_bytes` 可以是字节数，也可以是 fit 可用内存的份额（如 `25%`：本机内存，
或容器内存上限更低时取后者）。`max_conns`、`max_pending_bytes` 与 `max_write_queue_bytes` 只在启动时生效，
重载不改；启动时 fit 在日志里打印实际生效的上限与两个引擎的设置。单条路由的请求体上限由 `body_limit` 中间件另设。

### 引擎（engine）

编辑 `engine`：`event_loops`、`workers`、`min_workers`、`task_queue`、`poll_batch`、`read_buffer_size`、
`write_watermark`、`loop_wait`、`backlog`。fit 跑两个引擎：监听的那个服务客户端，上游连接走另一个，这样等
慢客户端的响应不会占住读上游的 worker；两个引擎都用这些设置。不写的字段取 fib 的默认，所以多数配置不写
`engine`；它是为跑在一台忙机器上的服务调参用的。引擎在启动时建立，改了要重启（保存时会警告）。这一页不是
[引擎](06-engine.zh-CN.md)菜单；后者按监听端口列出各端口提供的功能。

### 配置来源（providers）

编辑 `providers`：一个列表，每项一个来源，用 `name` 命名（不写时就是它的 kind）：`docker`、`consul`、
`consul_kv`、`etcd`、`kubernetes`。Docker 与 Consul 目录提供路由、上游与服务；Consul KV、etcd 与
Kubernetes 还提供监听——从不提供 `log`、`limits` 之类的其他成员。合并规则：配置写的胜过每个 provider，
一个 provider 胜过排在它后面的那些；一个 provider 的元素若与前面的同名，就被略去（被遮蔽的元素在概览页与
一致性检查中作为 info 列出）。生效配置与每部分来自哪里，在 [JSON](08-json.zh-CN.md) 章的生效配置标签看。
这一页的设置只在启动时读取。

### 其他（other）

没有其他页面编辑的成员在这一页，每个成员是表单中以其名字为标题的一节：`schema_version`、`access`、
`media_library`。整页共用一套**界面**、`.conf`、`.yaml`、`.json` 标签。

- `schema_version` 是这份配置所写的版本号 `1`；不写时按版本 1 读取，所以很少改它。
- `access` 是全局的允许与拒绝列表：每个监听、路由、服务、角色、Admin API 与控制台都按它走，各自更具体的
  `access` 先决定（[谁可以连接](../configuration.zh-CN.md#谁可以连接)）。注意别把自己访问控制台所用的地址拒绝掉。
- `media_library` 是具名媒体库的列表；媒体库模块里另有它自己的**媒体库配置**页（[媒体库](22-media-library.zh-CN.md)
  章）。

`sync` 与 `bittorrent` 各有自己的配置页（**同步配置**与 **BitTorrent 配置**），所以不在这一页：见
[数据同步](26-sync.zh-CN.md) 与 [BitTorrent](27-bittorrent.zh-CN.md)。当每个成员都有自己的页面时，这一页会说
“配置的每个成员都有自己的页面。”

### 导入 / 导出（transfer）

最后一页用两张卡片把整个配置导出与导入：**导出整个配置**与**导入整个配置**。

**导出**打开一个对话框选格式——`.conf`、`.yaml`（默认）、`.json` 或 `.store`（fit 的加密 fitstore）——选
`.store` 时还可填密码：填了就用它加密文件；留空则用运行中 store 的密钥加密，在别处打开需要该 store 的密钥文件
或 `$FIT_STORE_PASSWORD`（没有运行中的 store 时必须填密码）。文件名为 `fit-config-v<版本>.<格式>`。导出的是
已保存并在运行的配置，所以未保存的修改不在文件里，对话框会提示这一点。整份配置的 `.store` 导出还包含从页面
改过密码的控制台用户，因此在别处 `fit run -c backup.store -admin` 能一并恢复。

**导入**打开一个对话框，选文件、格式（按扩展名推断）与 `.store` 的密码（只能是可打印 ASCII；用运行中 store
的密钥加密的文件留空）。**导入并应用**替换整个配置（`admin` 除外），立即走保存的路径应用为新的配置版本，有
store 时写入 store；它会在确认后放弃未保存的修改。`.store` 文件中的控制台用户不会被导入。编辑部分配置的页面
在格式标签旁另有**导出**与**导入**按钮，只针对该部分：这样的导入只取文件中的这些成员、忽略文件中的其他成员，
并会把配置中文件里没有的成员移除。`admin` 从不导出或导入。详见
[配置的导入与导出](../console.zh-CN.md#配置的导入与导出)。

## 参数

只列最常用的字段；完整字段表在配置参考对应章。

### log

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `level` | `info` | 没有被 `levels` 与 `loggers` 单独设置的 logger 的最低级别：`debug`、`info`、`warn`、`error`；随重载生效 |
| `levels` | | 按 logger 名的最低级别，覆盖 `level`；随重载生效 |
| `output` | | `stdout`、`stderr` 或追加写入的文件路径；不写时 debug 与 info 到 stdout，warn 与 error 到 stderr；要重启 |
| `format` | `line` | `line`、`text`（logfmt）或 `json`；每条带 logger 名；要重启 |
| `time_zone` | `local` | 行时间与按 `interval` 切割所用的时区：`local`、`utc` 或 IANA 名；随重载生效 |
| `rotate` | | 文件切割与保留：`max_bytes`（1 GiB）、`interval`（关闭）、`max_age`（720h）、`max_files`、`max_total_bytes`（1 GiB）、`compress`（开启）；不写则文件无限增长 |
| `loggers` | | 某些 logger 自己的 `level`、`output`、`format`、`rotate` |

完整表：[log](../reference/config.md#log)、[log.rotate](../reference/config.md#config-rotate)、
[log.loggers.*](../reference/config.md#config-logger)。

### access_log

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `output` | `stdout` | `stdout`、`stderr` 或追加写入的文件路径；要重启 |
| `flush_interval` | `1s` | 一行在缓冲里最多等多久才写出；要重启 |
| `buffer_bytes` | 4 MiB | 缓冲里最多多少字节；超出的行被丢弃并计数；要重启 |
| `level` | `info` | 按状态码的级别：4xx 是 warn、5xx 是 error；随重载生效 |
| `format` | `json` | `json`、`text` 或 `line`；要重启 |
| `time_zone` | `local` | 行时间与按 `interval` 切割所用的时区；随重载生效 |
| `rotate` | | 与 `log` 的 `rotate` 相同；随重载生效 |

完整表：[access_log](../reference/config.md#access-log)。

### telemetry

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `endpoint` | `http://127.0.0.1:4318` | OTLP/HTTP 接收方；traces 到 `/v1/traces`，metrics 到 `/v1/metrics` |
| `headers_file` | | 每次导出附带的请求头文件，每行一个 `Name: value`，如接收方要求的 API key |
| `service_name` | `fit` | traces 与 metrics 所属资源的 service.name |
| `timeout` | `10s` | 一次导出最多多久；超时的批次被丢弃并计数 |
| `traces.sample_ratio` | 0 | 调用方没决定时抽样的份额，0 到 1 |
| `traces.max_queue_bytes` | 4 MiB | 等待导出的 span 最多多少字节；超出的被丢弃并计数 |
| `traces.interval` | `5s` | 多久导出一次，等待的 span 达 256 KiB 时更早 |
| `metrics.interval` | `30s` | 多久导出一次指标 |

完整表：[telemetry](../reference/config.md#telemetry)。

### metrics

没有字段：`{}` 就开启。它让每个请求多付一点，所以不写时关闭。完整表：
[metrics](../reference/config.md#metrics)。

### pprof

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `block_profile_rate` | 0 | 阻塞剖析：平均每被阻塞这么多纳秒记一个事件；1 为每个都记 |
| `mutex_profile_fraction` | 0 | 互斥锁剖析：每这么多次有竞争的锁记一次；1 为每次都记 |
| `max_duration` | `1m` | 一次剖析的 `seconds` 最多能要求多久；更长的请求被 400 拒绝 |

完整表：[pprof](../reference/config.md#pprof)。

### limits

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `max_conns` | 100000 | 所有监听合计同时保持的客户端连接上限；超出的在接受时即关闭；只在启动时生效 |
| `max_header_bytes` | 1 MiB | 监听接受的最大请求头；更大的答 431 |
| `max_body_bytes` | 16 MiB | 监听接受的最大请求体；更大的答 413，已开始流式传输的则结束连接 |
| `max_pending_bytes` | `50%` | 跨所有连接等慢客户端取走的响应字节；字节数或内存份额；只在启动时生效 |
| `max_write_queue_bytes` | 32 MiB | 一个连接为客户端排队的字节上限，超出则关闭连接；只在启动时生效 |
| `write_stall_timeout` | `30s` | 响应每次等待客户端取走数据时最多等多久 |

完整表：[limits](../reference/config.md#limits)。

### engine

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `event_loops` | fib 的：4 个 CPU 及以下 1、16 个及以下 2、更多时为 CPU 数除以 8 | 每个引擎等待事件的 event loop 数；Windows 上总是 1 |
| `workers` | 每个 CPU 100 | 每个引擎的 worker 池同时最多跑多少 |
| `min_workers` | 0 | 空闲时保留的 worker 数 |
| `task_queue` | fib 的默认 | 每个池中等待 worker 的任务数 |
| `poll_batch` | 1024 | event loop 一次等待最多收集的事件数 |
| `read_buffer_size` | 64 KiB | 连接一次从 socket 读入的字节，也是转发请求体一次写出的最多字节 |
| `write_watermark` | 四个读缓冲，64 KiB 到 256 KiB | 连接待写字节达到这么多时停止读取：单连接的背压 |
| `loop_wait` | `park` | 没有就绪事件时 event loop 的等待方式：`park`（Go 的 poller）或 `block`（epoll_wait），仅 Linux；`block` 在接近饱和时缩短 HTTP/1.1 的延迟尾部，代价是中位数 |
| `backlog` | 系统的 | 每个监听 socket 尚未接受的连接队列长度 |

完整表：[engine](../reference/config.md#engine)。

### providers

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `name` | 它的 kind | 在生效配置与日志中用的名字：小写字母、数字、`_`、`.`、`-`，不可重复 |
| `docker` | | 运行中容器标签里的路由、上游与服务；`endpoint` 默认 `unix:///var/run/docker.sock` |
| `consul` | | Consul 目录中服务标签里的路由、上游与服务；`endpoint` 默认 `http://127.0.0.1:8500` |
| `consul_kv` | | Consul KV 中 `prefix`（默认 `fit/`）下各键的监听、路由、上游与服务 |
| `etcd` | | etcd 中 `prefix`（默认 `fit/`）下各键的监听、路由、上游与服务；`endpoints` 默认 `http://127.0.0.1:2379` |
| `kubernetes` | | 集群 Gateway API 资源与 Ingress 中的监听、路由、上游与服务；`controller_name` 默认 `lesismal.github.io/fit` |

完整表：[providers](../reference/config.md#providers)。

## .conf 配置

一份完整、能通过 `fit validate` 的配置，包含本章各页编辑的所有成员，加一个最小的监听与路由。写 `admin`
只是为了让这个文件能单独运行：它属于 fit 启动时用的文件，控制台从不设置它。

```fit
schema_version 1

admin { address 127.0.0.1:2019 }

log {
	level info
	levels {
		engine warn
		tls debug
	}
	output /var/log/fit/fit.log
	format line
	time_zone Asia/Shanghai
	rotate {
		max_bytes 100MiB
		interval 24h
		max_age 720h
		max_files 30
	}
}

access_log {
	output /var/log/fit/access.log
	format json
	level warn
	time_zone Asia/Shanghai
	rotate { interval 24h; max_age 168h }
}

telemetry {
	endpoint http://otel-collector:4318
	service_name edge
	traces { sample_ratio 0.05 }
	metrics { interval 30s }
}

metrics {}

pprof { max_duration 2m }

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
	read_buffer_size 64KiB
	backlog 4096
}

provider { docker { endpoint unix:///var/run/docker.sock } }

access { deny 203.0.113.0/24 }

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
- [配置](../configuration.zh-CN.md)：[谁可以连接](../configuration.zh-CN.md#谁可以连接)；以及
  [.conf 格式](../reference/fit.zh-CN.md)。
- [模板](../../templates/README.zh-CN.md)：
  [`00-runtime.conf`](../../templates/conf/conf.d/00-runtime.conf)、
  [`30-observability.conf`](../../templates/conf/conf.d/30-observability.conf)、
  [`50-providers.conf`](../../templates/conf/conf.d/50-providers.conf)。
- 各有自己配置页的成员：[数据同步](26-sync.zh-CN.md)、[BitTorrent](27-bittorrent.zh-CN.md)、
  [媒体库](22-media-library.zh-CN.md)、[媒体认证](21-media-auth.zh-CN.md)、[流媒体](20-streaming.zh-CN.md)、
  [WebRTC](23-webrtc.zh-CN.md)、[隧道角色](24-tunnel.zh-CN.md)、[LLM 角色](25-llm.zh-CN.md)。
- 相邻章：[功能导航栏](03-navigation.zh-CN.md)、[概览](04-overview.zh-CN.md)、
  [引擎](06-engine.zh-CN.md)、[JSON](08-json.zh-CN.md)、[一致性检查](10-consistency-check.zh-CN.md)、
  [日志](11-logs.zh-CN.md)。
