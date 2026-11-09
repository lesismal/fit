Reads TiDB: a snapshot of `tables` over MySQL's protocol, then the changes a TiCDC changefeed writes
to Kafka after it, so that a pipeline starts with every row and goes on with every change. TiDB as a
sink is the `mysql` sink.

When the pipeline has no checkpoint (`snapshot: "initial"`), the source takes TiDB's current
timestamp (TSO) and reads each table at it (`tidb_snapshot`), in pages of `page_rows` by primary key,
each row an insert whose position is the table and its key, so that a snapshot interrupted reads on
after its last key. Then the kafka source (`kafka`, its params as the `kafka` source's, without
`codec`) reads the changefeed's topic from its `start`, or from the checkpoint's offsets, and the
changes committed at or before the snapshot's timestamp are left out. The changefeed must exist
before the snapshot is taken (its start-ts at or before it) and the topic must keep its messages
since; the snapshot must finish within TiDB's GC life time (`tidb_gc_life_time`), or it fails and
starts again.

`protocol` is the changefeed's: `canal-json`, created with `enable-tidb-extension=true` so that each
row carries its commit TS, or `simple`. A change's transaction is its commit TS and its commit time
the TS's physical time; its key the primary key (canal-json's `pkNames`, the simple protocol's
schema). The tables' schemas are read when the source starts: canal-json's ENUM and SET numbers
become their labels, as the snapshot reads them, and the simple protocol's rows decode before the
changefeed's next bootstrap message. Only the changes of `tables` are kept, every table's when it is
left out (with no snapshot). Values: integers, decimals, floats, strings, bytes, dates, DATETIME and
TIMESTAMP as times (in UTC, and `time_zone` for the changefeed's), TIME as text, JSON, BIT as a uint.

**Test connection** logs in to TiDB with `SELECT 1` and reads the Kafka topics' metadata.

```json
{
  "sync": {
    "state_dir": "/var/lib/fit/sync",
    "pipelines": [
      {
        "name": "tidb-to-pg",
        "source": {"module": "tidb", "params": {
          "address": "tidb:4000", "username": "fit_sync", "password_env": "TIDB_PASSWORD",
          "tables": ["shop.orders", "shop.order_items"], "protocol": "canal-json",
          "kafka": {"brokers": ["kafka-1:9092"], "topics": ["ticdc-shop"]}}},
        "sinks": [{"name": "pg", "sink": {"module": "postgres", "params": {
          "address": "pg:5432", "user": "fit", "password_env": "PG_PASSWORD", "database": "replica"}}}]
      }
    ]
  }
}
```
