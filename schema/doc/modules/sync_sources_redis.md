Reads Redis or Valkey streams (Redis 6 and later; RESP2 alone for older servers), `streams` together
in one `XREAD` of up to `count` (1000) entries of each, waiting `block` (5s) for more. Each entry is
a record: with `form: "fields"` a `message` whose `after` holds the entry's fields (UTF-8 values as
strings, others as bytes) and whose key is `{"id": "<entry ID>"}`; with `form: "record"` the record
the [`redis` sink](#sync-sinks-redis) wrote with `form: "record"`, its operation, key, namespace and
rows kept (an entry without a `record` field, or one that is not a record, is read as fields). The
commit time is the entry ID's milliseconds, unless the record has its own; `namespace` is the
stream's key, or the record's own.

The position is the last entry ID read of each stream, `{"streams": {"orders": "1731000000000-3"}}`,
so a restart reads on after it. No consumer group is made or acknowledged: the checkpoint is the
only cursor, and entries are not removed. A pipeline without a checkpoint starts at each stream's
first entry (`snapshot: "initial"`), or after its last entry now (`snapshot: "never"`); a stream
added to `streams` later starts the same way. Entries trimmed away (`MAXLEN`, `XTRIM`, `XDEL`) before
the source read them are not seen. In a job (`mode: "once"`) the source reads what the streams hold,
without waiting, and stops.

The connection is TCP, or TLS with `tls` (checked against the system's roots or `ca_file`'s,
`cert_file` and `key_file` for a client certificate); `username` and `password_env` authenticate as
an ACL user, `password_env` alone as the default user. `protocol` is the RESP version; left out it
is 3 when the server knows `HELLO`, 2 otherwise. A standalone server or a primary: not Redis
Cluster.

```json
{
  "sync": {
    "state_dir": "/var/lib/fit/sync",
    "pipelines": [
      {
        "name": "events-out",
        "source": {"module": "redis", "params": {
          "address": "redis.internal:6379", "username": "sync", "password_env": "REDIS_SYNC_PASSWORD",
          "tls": true, "streams": ["events:orders", "events:payments"]}},
        "sinks": [{"name": "search", "sink": {"module": "elasticsearch", "params": {"urls": ["https://es:9200"]}}}]
      }
    ]
  }
}
```
