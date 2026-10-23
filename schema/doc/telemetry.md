OpenTelemetry over OTLP/HTTP: a span for each request sampled, with W3C trace context sent on to
upstreams, and fit's metrics exported on a schedule, to a collector. Read when fit starts. A
Caddyfile's `tracing` is not converted.

```json
{
  "listeners": [{"name": "web", "address": ":8080"}],
  "routes": [{"handler": {"module": "respond"}}],
  "telemetry": {
    "endpoint": "http://otel-collector:4318",
    "service_name": "edge",
    "traces": {"sample_ratio": 0.1},
    "metrics": {"interval": "30s"}
  }
}
```
