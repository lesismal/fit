# 015：不用 SDK 的 OpenTelemetry：fit 自己实现 W3C trace context，用 OTLP/HTTP JSON 导出

[English](015-opentelemetry-without-the-sdk.md) | [简体中文](015-opentelemetry-without-the-sdk.zh-CN.md)

状态：已采纳，见 I65（2026-10-22）。

## 背景

I65 要追踪请求，把 W3C trace context 传给上游，并通过 OTLP 导出 span 和指标。常见做法是 OpenTelemetry 的 Go SDK：
`go.opentelemetry.io/otel`、它的 `sdk/trace` 和 `sdk/metric`，以及用 gRPC 或 HTTP 加 protobuf 的 OTLP exporter。

## 决定

- **不用 SDK。** SDK 及其 OTLP exporter 会把 gRPC、protobuf 和 genproto 带进二进制，十几个模块、数 MB，而 fit 需要的
  只是：解析和写出一个 55 字节的头、ID、每个请求一个 span，以及两种 POST 请求。它们还会在每个被追踪的请求上放
  `context.Context`、span 对象和属性切片，而 fit 的请求路径是按分配次数计算的。fit 在 `observe` 中自己实现：
  - **按 W3C 的 trace context**：解析 `traceparent`（版本 00，以及更高版本中与 00 相同的部分；无效的不跟随），并以
    fit 的 span 为父写给上游。`tracestate` 原样转发；fit 不添加自己的条目。
  - **采样**与 SDK 默认采样器一样基于父级：调用方已采样的请求被追踪，调用方未采样的不追踪，没有有效调用方的按
    `sample_ratio` 追踪（默认 0：只追踪调用方采样的）。ID 来自 `math/rand/v2`，对 trace ID 足够随机且不加锁。
  - **每个请求一个 server span**，名为 `<method> <route>`，带稳定版 HTTP 语义约定的属性（`http.request.method`、
    `url.path`、`url.scheme`、`server.address`、`client.address`、`network.protocol.version`、
    `http.response.status_code`、`http.response.body.size`）和 fit 自己的（`fit.listener`、`fit.route`、
    `fit.upstream.address`），5xx 时状态为错误。不为上游另建 client span：上游自己的 span 是这个 server span 的子
    span，上游地址和耗时记在 fit 的 span 上。
- **OTLP/HTTP 使用 JSON**，发往 `<endpoint>/v1/traces` 和 `/v1/metrics`，所有 collector 和大多数后端都接受，且不需要
  protobuf。span 在响应结束时写成 JSON，放入受 `max_queue_bytes` 约束的分片缓冲区，与访问日志的行相同；一个独立的
  goroutine 每隔 `interval`，或积累 256 KiB 时发送，每次一个请求。放不下的 span 被丢弃并计数，失败的导出被丢弃、计数，
  并在持续期间只报告一次：追踪从不拖住请求。
- **通过 OTLP 导出的指标就是 Admin API 的指标。** `WriteMetrics` 写入 `MetricWriter`，Prometheus 文本写入器和 OTLP
  写入器都实现了它：计数器成为累积的单调 sum，gauge 仍是 gauge，耗时直方图成为以秒为单位的显式分桶直方图。
  `fit_trace_spans_total` 统计导出、失败和丢弃的 span。
- **开销**（规则 21）：没有 `telemetry` 时只多一次 nil 检查；开启追踪时，未采样的请求多一次头查找；采样的请求多一个
  `Entry`（除非访问日志已创建），结束时写出约 850 字节 JSON 的 span，以及给上游的一个 `traceparent` 字符串。基准测试
  结果见进度文件中 I65 的说明。

## 影响

- `go.mod` 不增加依赖。
- 不支持 gRPC 的 OTLP（4317 端口）和 HTTP 上的 protobuf；只接受这些的接收方需要在前面放一个 collector。如有需要，
  可以把同样的 span 加一种 protobuf 编码。
- baggage 像其他头一样透传，不读取；不产生 span link、event 和日志。
