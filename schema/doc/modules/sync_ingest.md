Takes records into a sync pipeline over HTTP: the bodies `POST`ed to the route go to the `http`
source of the pipeline `pipeline` names, and each request is answered once every sink of the
pipeline has acknowledged its records, `200` with `{"records": <n>}`. It is answered `503` with
`Retry-After` (`retry_after`, in whole seconds) when the pipeline is not running (not in the config,
starting again after a failure, stopped), when the source's pending requests or bytes are full, and
when its records are not acknowledged within `timeout` (30s); a client that sends a request again
until it is answered `200` therefore loses nothing, across a restart of fit too, though it may write
a record twice, which an idempotent sink folds. A body the source cannot read is `400`, and nothing
of it is written; one larger than `max_body_bytes` (8 MiB) `413`; a `Content-Type` the source does
not read `415`; a method other than `POST` `405`.

The handler never waits on a fib worker: the body is read as it arrives, handed to the source
without waiting, and the answer is written later from the pipeline's goroutine or the timeout's.
The route's middleware applies as to any handler: put an authenticating middleware (`basic_auth`,
`jwt`, `forward_auth`, `client_cert`) and a `rate_limit` or `body_limit` before it, since anyone who
reaches the route can write into the pipeline.

The pipeline is set in `sync` as any other, its source `http`, as `{"name": "events", "source":
{"module": "http", "params": {"namespace": "events"}}, "sinks": [...]}`.

```json route
{
  "match": {"path": ["/ingest/events"]},
  "middleware": [{"module": "jwt", "params": {
    "jwks_url": "https://auth.example.com/.well-known/jwks.json",
    "audience": ["ingest"]
  }}],
  "handler": {"module": "sync_ingest", "params": {"pipeline": "events", "timeout": "10s"}}
}
```
