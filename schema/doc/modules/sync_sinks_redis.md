Writes records to Redis or Valkey, each batch's commands pipelined, at most `max_pipeline` (1000) in
one round trip, in order.

With `mode: "stream"` (the default) each record is an entry added with `XADD` to the stream
`stream` names (`{namespace}`; `{key}` is the record's key as text). With `form: "fields"` the entry's
fields are the row's (`after`, or `before` for a delete, or the key when both are null), each value
as text, maps and arrays as JSON, nulls left out, and `op_field` adds the operation first; with
`form: "record"` one field, `record`, holds the whole record in its canonical JSON, which the
[`redis` source](#sync-sources-redis) reads back with every type. `max_len` trims each stream to
about that many entries (`MAXLEN ~`). The entry's ID is the server's (`entry_id: "auto"`), so a
batch written again after a crash adds its entries again; with `entry_id: "key"` it is the record's
key (its `id` field, or the key itself), an ID as `ms-seq`, as the `redis` source gives: an entry
the stream already has, at or below its last, is refused by the server and counted as written, so a
stream copied from another loses nothing and holds nothing twice across restarts.

With `mode: "keys"` each record is the value of a key, `key` (`{namespace}:{key}`), a key of several
fields named by their values joined by `:`: `value: "json"` sets the row as a plain JSON object,
`value: "record"` the whole record in its canonical JSON, and `value: "hash"` sets the row's fields
as a hash, the key deleted and set again in one `MULTI` so that no field of an earlier row stays. A
delete deletes the key. `ttl` sets each key's expiry as it is written. Writing the same records again
leaves the same keys.

A record the server refuses for good (`WRONGTYPE`, a bad ID) fails, and `on_error` takes it, the
records before it written; an error that may pass (`LOADING`, `OOM`, `READONLY`, `BUSY`, `NOAUTH`,
`NOPERM`, a connection lost, a `timeout`) is tried again by the pipeline's `retry`. DDL records are
skipped. The connection is as the source's.

```json
{
  "sync": {
    "state_dir": "/var/lib/fit/sync",
    "pipelines": [
      {
        "name": "orders-cache",
        "source": {"module": "file", "params": {"paths": ["/var/log/shop/orders.ndjson"], "codec": {"module": "json"}}},
        "transforms": [{"module": "key", "params": {"fields": ["order_id"]}}],
        "sinks": [{"name": "cache", "sink": {"module": "redis", "params": {
          "address": "redis.internal:6379", "password_env": "REDIS_PASSWORD",
          "mode": "keys", "key": "order:{key}", "value": "hash", "ttl": "24h"}}}]
      }
    ]
  }
}
```
