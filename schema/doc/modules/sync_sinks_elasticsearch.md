Writes records to Elasticsearch or OpenSearch with `_bulk`, through fib's asynchronous HTTP client.
An insert, an update or a message is an `index` (or `create`) of its row, `after`, or `before` when
`after` is null, as a plain JSON document; a delete is a `delete`; DDL is not written. The `_id` is
the record's key: a single value's text, or a map's values in order joined by `:`, each with `\` and
`:` escaped by a `\` (`{"order": 7, "line": 2}` is `7:2`); bytes are base64, arrays and maps plain
JSON. A record without a key is indexed with an `_id` the cluster makes, which a replay repeats; an
update or a delete without one fails. Put an `envelope` transform before it for a document of
another shape; its `deletes: "rewrite"` with `meta`'s `deleted` keeps deleted rows as documents.

`index` is `{namespace}` or a template with the commit time's date, as `logs-{yyyy.MM.dd}`; a
`route` transform maps namespaces to other names first. The sink tells Elasticsearch from
OpenSearch by the root endpoint, which it asks at the first write.

What the cluster answers each item decides what follows: written (and a delete of a document that
is not there, and with `action: "create"` one that is) is acknowledged; a 429 is sent again, with a
backoff from 200ms to 30s, for as long as the cluster answers so, and a 429 to the whole bulk
waits the same way, so a busy cluster holds the pipeline and, through its queue, the source; a 5xx
is sent again five times. An item sent again takes the later items of the same document in the
batch with it, so a document's last change stays last. Any other refusal, as a mapping error, fails
its record for good: `on_error` takes it at once, without the pipeline's retries. A write that
fails says at which record: those before it are written, and the runtime sends the batch again
from it, so the later records the cluster had already taken are written again, in order, and
acknowledged once. A connection that fails, or a 502, 503 or 504, moves on to the next of `urls`.
In a job, a sink has `job.max_parallel` writers, each key going to one of them.

```json
{
  "sync": {
    "state_dir": "/var/lib/fit/sync",
    "pipelines": [
      {
        "name": "logs-to-search",
        "source": {"module": "file", "params": {"paths": ["/var/log/app/*.log"], "codec": {"module": "json"}}},
        "sinks": [{"name": "es", "sink": {"module": "elasticsearch", "params": {
          "urls": ["https://es1:9200", "https://es2:9200"], "ca_file": "/etc/fit/es-ca.pem",
          "username": "fit", "password_env": "ES_PASSWORD", "index": "app-logs-{yyyy.MM.dd}"}},
          "batch": {"max_records": 1000, "max_wait": "1s"}}]
      }
    ]
  }
}
```
