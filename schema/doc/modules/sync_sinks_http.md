Posts each batch to `url` through fib's asynchronous HTTP client: as NDJSON (`format: "ndjson"`,
`application/x-ndjson`), a line per record, or as a JSON array (`format: "json"`,
`application/json`); each record its row as a plain JSON object (`form: "plain"`), or the whole
record in its canonical JSON (`form: "record"`), with its operation, key, namespace and position.
`codec` writes the body in a codec's format instead, sent as `content_type`. A batch larger than
`max_request_bytes` (8 MiB) goes in several requests, in order; a record larger alone fails for
good.

Every request carries `headers`, the authentication the config gives (`username` with
`password_env` for basic authentication, or `bearer_token_env`, read from the environment when the
pipeline loads) and an idempotency key in `idempotency_header` (`Idempotency-Key`): 32 hex digits of
a SHA-256 of the request's records' positions and its body. A request sent again carries the same
key, so a receiver that keeps the keys it has taken can drop a request it already has; the same
records at other positions, or other records, get another.

Any `2xx` acknowledges the request's records. A `5xx`, `408` or `429`, a timeout (`timeout`, 30s)
or a connection's error fails the write, and the pipeline's `retry` sends it again with its backoff,
from the request that failed: those before it are acknowledged. Any other answer, a `3xx` among
them (redirects are not followed), fails the records for good: `on_error` takes them at once,
without the retries. TLS is checked against the system's roots, or `ca_file`'s; `cert_file` and
`key_file` present a client certificate. In a job, a sink has `job.max_parallel` writers, each key
going to one of them.

```json
{
  "sync": {
    "state_dir": "/var/lib/fit/sync",
    "pipelines": [
      {
        "name": "logs-out",
        "source": {"module": "file", "params": {"paths": ["/var/log/app/*.log"], "codec": {"module": "json"}}},
        "sinks": [{"name": "collector", "sink": {"module": "http", "params": {
          "url": "https://collector.example.com/v1/logs", "bearer_token_env": "COLLECTOR_TOKEN",
          "headers": {"X-Tenant": "shop"}, "format": "json"}},
          "batch": {"max_records": 500, "max_wait": "1s"}}],
        "retry": {"max_attempts": 10, "backoff": "500ms", "max_backoff": "30s"}
      }
    ]
  }
}
```
