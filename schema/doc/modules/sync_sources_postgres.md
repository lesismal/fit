Follows a PostgreSQL database (13 to 17, `wal_level = logical`) as a logical replica: a
`publication` names the tables and operations, a `slot` keeps the server's WAL until the source has
confirmed it, and the stream is read with the built-in `pgoutput` plugin (protocol 1 on 13, 2 with
streaming from 14). With `create`, a missing publication is made `FOR TABLE` the `tables`, and a
missing slot is made; neither is changed or dropped later. Without `create`, both must exist: the
slot of plugin `pgoutput` on `database`.

With `snapshot: "initial"` (the default), a pipeline without a checkpoint first reads every row of
the published tables, then the changes after them, and the hand-over is exact: the rows come from a
snapshot exported at a point of the WAL, and the stream starts at that very point, so no change is
both in the snapshot and in the stream, and none is in neither. A slot made by `create` exports the
snapshot itself; when the slot exists already, a temporary slot is made beside it for the snapshot
and dropped with its connection. Each table with a primary key (or a `REPLICA IDENTITY USING INDEX`
index) is split into chunks of `chunk_rows` (100,000) rows by that key, read `snapshot_parallel`
(4) at a time, each in a `REPEATABLE READ` transaction that takes the exported snapshot
(`SET TRANSACTION SNAPSHOT`); a table without one is read whole. Snapshot records are inserts with
no transaction, keyed as the stream keys the table's changes, and the publication's column lists
and row filters (PostgreSQL 15 and later) apply to them. The position records the chunks done; a
run that fails keeps the exported snapshot for five minutes, and the next resumes in it at its next
chunk. Once the snapshot is gone (fit restarted), the tables not finished are read again whole from
a new snapshot, which a warning says, and the stream starts at the first snapshot's point, so the
changes between the two are replayed over them: sinks writing by key converge on the tables. The
snapshot holds back vacuum on the server while it is read. `snapshot: "never"` reads the changes
alone, from the slot's position. A job (`mode: "once"`) reads the snapshot in chunks, as `job`
says, each chunk in a transaction of its own and not one snapshot, and needs no slot; with `never`
it reads the changes up to the server's end at its start.

Each insert, update and delete becomes a record of namespace `schema.table`; a truncate becomes a
`ddl` record of each table whose `after` holds the `statement` and `truncate: true`. Values are typed
by the columns' types: integers and `oid` as ints, `float4` and `float8` as floats, `numeric` as a
decimal, `bool`, `bytea` as bytes, `json` and `jsonb` as JSON, `date` as a date, `timestamp` (UTC)
and `timestamptz` as times, arrays of those as arrays; every other type (`time`, `interval`, `uuid`,
`inet`, enums, ranges, ...) as the server's text. The key is the table's primary key, or, with
`REPLICA IDENTITY USING INDEX` or without a primary key, the replica identity's columns. An update
whose key changed comes as a delete of the old key and an insert of the new row. `before` holds the
old row with `REPLICA IDENTITY FULL`, the old key's columns when the key changed, and nothing
otherwise; a TOASTed value an update did not change is not sent by the server and is left out of
`after` (taken from `before` with `REPLICA IDENTITY FULL`). A table of a publication that publishes
updates or deletes must have a primary key or a replica identity, or the source refuses to start.

Records come a transaction at a time, in commit order, with the transaction's ID (its xid), and
the commit time. The position is the LSN at the end of a transaction's commit,
`{"lsn": "16/B374D848"}`: the slot is told it, as its `confirmed_flush_lsn`, only once every sink
has acknowledged the transaction and the checkpoint is saved, so a crash or `kill -9` replays from
the checkpoint and loses nothing. While every transaction read is confirmed, the slot is told the
server's latest position too, so that writes to tables not published do not hold the WAL. A
checkpoint older than the slot's `confirmed_flush_lsn` starts at the slot's position: the server
no longer has what is before it. Large transactions streamed by the server (PostgreSQL 14 and later,
`streaming: "auto"`) are kept in a file under `spool_dir` until they commit, and only then become
records; an aborted one, or an aborted subtransaction's changes, never do.

`status_interval` (10s) is how often the server is told the position, which also keeps the
connection alive. The slot's lag, the WAL between the server's end and the position confirmed, is
logged as a warning past `lag_warning` (1 GiB): a stopped or slow pipeline makes the server keep
WAL. Drop the slot (`SELECT pg_drop_replication_slot('...')`) when a pipeline is removed for good.

The connection uses TLS with `sslmode` `verify-full` by default (`verify-ca`, `require`,
`disable`; no `prefer`), checked against the system's roots or `ca_file`; `cert_file` and
`key_file` for a client certificate; SCRAM-SHA-256 with channel binding when the server offers it
(`channel_binding`); MD5 and cleartext passwords are refused. The user needs the `REPLICATION`
attribute, `SELECT` on the tables, and, with `create`, the right to create the publication.

```json
{
  "sync": {
    "state_dir": "/var/lib/fit/sync",
    "pipelines": [
      {
        "name": "orders-out",
        "source": {"module": "postgres", "params": {
          "address": "db.internal:5432", "user": "repl", "password_env": "PG_REPL_PASSWORD",
          "database": "shop", "ca_file": "/etc/fit/pg-ca.pem",
          "publication": "fit_orders", "slot": "fit_orders", "create": true,
          "tables": ["public.orders", "public.order_items"]}},
        "sinks": [{"name": "search", "sink": {"module": "elasticsearch", "params": {"urls": ["https://es:9200"]}}}]
      }
    ]
  }
}
```
