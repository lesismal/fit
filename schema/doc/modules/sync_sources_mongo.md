Reads MongoDB 6.0 and later, a replica set or a sharded cluster (through a `mongos`), as a
snapshot of the collections by `_id`, then their change stream from the resume token. It speaks
MongoDB's wire protocol itself (OP_MSG, SCRAM-SHA-256, TLS), to the primary it finds from the hosts
of `uri` and finds again after a failover, on connections of the pipeline's own goroutine, never
a fib worker. What it reads: every collection of `database`, or `collections` of it, or, with
neither, every database's but `admin`, `config` and `local` (views, time series and `system.`
collections left out).

A record's namespace is `db.collection`, its key `{"_id": ...}`, its `after` the document. Types a
record value has no kind for keep their BSON type as MongoDB's extended JSON names it, as
`{"$oid": "..."}`, so that the `mongo` sink writes them back as they were; int32s read as ints.

**The snapshot** (`snapshot: "initial"`, the default) first takes the change stream's token as of
now, then reads each collection in `_id` order (`find` with the `_id` index's bounds, read concern
`majority`), as inserts, in batches of `batch_size`. What changes while it reads is read again from
the stream, which starts at that token: the two overlap, and the sinks' upserts converge. The
checkpoint during the snapshot is the token, the collection and the last `_id` read, so a restart
goes on after it; the snapshot's last record carries the token itself. `snapshot: "never"` starts
with the changes from now.

**The change stream** has `fullDocument: updateLookup` by default: an update's `after` is the
document as it is when the change is read (an update of a document deleted since is passed over:
its delete follows). `full_document: "whenAvailable"` or `"required"` takes its post-image instead,
and `full_document_before_change` (`whenAvailable` or `required`) adds the pre-image as `before`
to updates and deletes; both need the collection to record them (`changeStreamPreAndPostImages`).
Inserts, updates, replaces (an `update`) and deletes are records of their kind; a collection
dropped or renamed, or a database dropped, is a `ddl` record whose `after` holds `operationType`,
`ns` and, for a rename, `to`. The changes of a transaction carry its ID (the session's and the
transaction's number) with its first and last marked, the last known once the next change, or a
wait of `max_await` with none, shows it: the checkpoint never falls inside a transaction. The
commit time is the change's wall time.

The checkpoint is the change stream's resume token, `{"token": ...}`. A cursor cut by a failover,
a lost connection or a cursor the server timed out is opened again from the last token, up to five
times in a row, then the pipeline restarts from its checkpoint. **A new snapshot is needed** when
the stream cannot resume: a token the oplog no longer holds (the pipeline stopped longer than the
oplog's window), or a stream invalidated (its single collection dropped or renamed, its database
dropped); the error says so, and the pipeline's **New snapshot** (or a reset of its checkpoint)
starts it again.

**Jobs.** In a job (`mode: "once"`) the snapshot is split into ranges of `_id`, one per
`chunk_documents` documents of each collection, bounded by a sorted sample of its `_id`s, read
`job.max_parallel` at a time, each resumed after its last `_id`. With `snapshot: "never"`, a job
reads the change stream from its checkpoint until it is caught up.

The connection: `uri` is a `mongodb://` connection string with the hosts, `replicaSet`,
`authSource`, `tls=true` and `tlsCAFile` (the server's certificate is always checked),
`tlsCertificateKeyFile` and the timeouts; the user is in `uri`, its password in the environment
variable `password_env` (a password in `uri` is refused). `timeout` bounds each command.

```json
{
  "sync": {
    "state_dir": "/var/lib/fit/sync",
    "pipelines": [
      {
        "name": "shop-to-search",
        "source": {"module": "mongo", "params": {
          "uri": "mongodb://sync@db1:27017,db2:27017,db3:27017/?replicaSet=rs0&tls=true&tlsCAFile=/etc/fit/mongo-ca.pem",
          "password_env": "MONGO_SYNC_PASSWORD", "database": "shop", "collections": ["orders", "customers"]}},
        "sinks": [{"name": "search", "sink": {"module": "elasticsearch", "params": {"urls": ["https://es:9200"], "index": "shop"}}}]
      }
    ]
  }
}
```
