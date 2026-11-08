Takes the records clients post to a route whose handler is [`sync_ingest`](#http-handlers-sync_ingest)
naming the pipeline. Without `codec` a body is JSON: one object, an array of objects, or several of
them one after another, as NDJSON, its `Content-Type` `application/json`, `application/x-ndjson`,
`application/jsonl`, a `+json` type, `text/plain` or none; each object is a row (`form: "plain"`), a
message record whose `after` holds its members, or a whole record in its canonical JSON
(`form: "record"`). With `codec` each line of the body, or the whole body (`framing: "body"`), goes
to the codec, whatever the `Content-Type`. A body is read whole or not at all: one value or line
the source cannot read answers the request `400` and writes nothing of it. Records without a
namespace get `namespace` (`http`), and the time the request came as their commit time.

The records of one request are one transaction, and its last record carries the source's position,
so a request is answered `200` once every sink has acknowledged all of them (a record `on_error`
skipped or sent to `dead_letter` counts as acknowledged). The source holds at most
`max_pending_requests` (1024) requests taken and not yet answered, and `max_pending_bytes` (64 MiB)
of bodies not yet read; a request past either is answered `503` at once. A slow sink fills the
pipeline's `queue`, which holds the source, which fills its pending requests: backpressure reaches
the clients as `503`s, and nothing waits on a fib worker.

There is nothing to resume from: the checkpoint holds the position of the last request answered,
which a restart ignores. A request not answered `200` is the client's to send again, so delivery is
at least once; give records a key (`key` transform, or `form: "record"`) for an idempotent sink. The
source never ends, in a job (`mode: "once"`) too.

```json
{
  "sync": {
    "state_dir": "/var/lib/fit/sync",
    "pipelines": [
      {
        "name": "metrics-in",
        "source": {"module": "http", "params": {"codec": {"module": "csv", "params": {"columns": ["host", "value"]}},
          "namespace": "metrics", "max_pending_requests": 256}},
        "sinks": [{"name": "file", "sink": {"module": "file", "params": {"path": "/var/log/fit/metrics.log", "codec": {"module": "json"}}}}]
      }
    ]
  }
}
```
