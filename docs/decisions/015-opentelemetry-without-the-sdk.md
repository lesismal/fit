# 015: OpenTelemetry without the SDK: W3C trace context in fit, OTLP/HTTP JSON

[English](015-opentelemetry-without-the-sdk.md) | [简体中文](015-opentelemetry-without-the-sdk.zh-CN.md)

Status: accepted, in I65 (2026-10-22).

## Context

I65 traces requests, propagates W3C trace context to upstreams, and exports spans and metrics over
OTLP. The usual way is the OpenTelemetry Go SDK: `go.opentelemetry.io/otel`, its `sdk/trace` and
`sdk/metric`, and the OTLP exporters, which speak gRPC or HTTP with protobuf.

## Decision

- **No SDK.** The SDK and its OTLP exporters bring gRPC, protobuf and genproto into the binary, a
  dozen modules and several megabytes, for what fit needs of them: a 55-byte header parsed and
  written, IDs, one span per request, and two POST requests. They also put a `context.Context`,
  span objects and attribute slices on every traced request, where fit's request path counts
  allocations. fit does it itself, in `observe`:
  - **Trace context** per W3C: `traceparent` parsed (version 00, and a later version as far as 00
    goes; an invalid one is not followed), and written for the upstream with fit's span as the
    parent. `tracestate` goes on as it came; fit adds no entry of its own.
  - **Sampling**, parent-based as the SDK's default sampler: a request whose caller sampled it is
    traced, one whose caller did not is not, and one with no valid caller is traced at
    `sample_ratio` (0 by default: only what callers sample). IDs come from `math/rand/v2`, which is
    random enough for trace IDs and does not lock.
  - **One server span per request**, named `<method> <route>`, with the stable HTTP semantic
    conventions' attributes (`http.request.method`, `url.path`, `url.scheme`, `server.address`,
    `client.address`, `network.protocol.version`, `http.response.status_code`,
    `http.response.body.size`) and fit's (`fit.listener`, `fit.route`, `fit.upstream.address`), and
    an error status for 5xx. No client span for the upstream: the upstream's own span is the
    server span's child, and the upstream's address and time are on fit's.
- **OTLP/HTTP with JSON**, to `<endpoint>/v1/traces` and `/v1/metrics`, which every collector and
  most backends take, and which needs no protobuf. A span is written as JSON when its response
  finishes, into sharded buffers bounded by `max_queue_bytes`, as the access log's lines are; a
  goroutine of its own sends them every `interval`, or once 256 KiB wait, one request at a time.
  Spans that do not fit are dropped and counted, and an export that fails is dropped, counted and
  reported once while it lasts: tracing never holds a request up.
- **Metrics over OTLP are the Admin API's.** `WriteMetrics` writes to a `MetricWriter`, which the
  Prometheus text writer and an OTLP writer both are: counters become cumulative monotonic sums,
  gauges gauges, and duration histograms explicit-bucket histograms in seconds. `fit_trace_spans_total`
  counts spans exported, failed and dropped.
- **Costs** (rule 21): with no `telemetry` nothing is added but a nil check; with tracing on, a
  request not sampled costs a header lookup; a sampled one an `Entry`, unless the access log made
  one, its span written as about 850 bytes of JSON when it finishes, and a `traceparent` string
  for the upstream. The I65 notes in the progress files have the benchmarks.

## Consequences

- No new dependency in `go.mod`.
- gRPC OTLP (port 4317) and protobuf over HTTP are not spoken; a receiver that takes only those
  needs a collector in front. Protobuf can be added as an encoding of the same spans if one is
  wanted.
- Baggage is passed through as any other field and not read; span links, events and logs are not
  produced.
