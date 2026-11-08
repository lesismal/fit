Writes records to MongoDB 6, 7 or 8 (4.2 and later are spoken) as unordered bulk writes, on the
primary of a replica set, a standalone server or a `mongos`, found from `uri` or `hosts` and found
again when it changes.

Each record writes the document whose `_id` its key makes: a key of one field is that field's value
(`{"id": 7}` gives `_id: 7`, a MongoDB source's `{"_id": ObjectId}` its ObjectId), a key of several
fields a document of them in their order, any other key itself; a record without a key takes its
row's `_id`. An insert, an update or a message replaces that document with its row (`after`), `_id`
first, inserting it when there is none (an upsert); a delete deletes it. DDL records are skipped. A
record goes to the database and collection its namespace names, `db.collection`; `database` and
`collection` replace either. Values are written as I181's client writes them: ints as 64-bit
integers, decimals as `decimal128` (exactly, or the record fails), times and dates as UTC datetimes
to the millisecond, JSON as documents, and the tagged values a MongoDB source gives (`{"$oid": ...}`
and the like) as their BSON types, so a collection copied to another is the same documents.

Within a batch only each document's last record is sent, since it covers the earlier ones: the
upserts of a collection go in one `update` and its deletes in one `delete`, each `ordered: false`,
cut at `max_request_bytes`. Writing the same records again leaves the same documents, so a replay
after a crash converges. A document the server refuses (a validator, a unique index, a document over
16 MiB, an `_id` that cannot be one) fails its record, which `on_error` takes, the records before it
written; the batch is then written again in order to find that record. A write conflict, a primary
stepping down, a lost connection, a failed authentication or a write concern not met (`wtimeout`)
is tried again by the pipeline's `retry`. Transactions of the source are not kept: each document's
write is atomic, a batch is not.

`write_concern`, `journal` and `write_timeout` set the write concern sent with every write;
unacknowledged writes (`w: 0`) are refused, as nothing could then be checkpointed. A job's writers
(`job.max_parallel`) share the connections, at most `max_conns`. **Test connection** finds the
primary, authenticates and sends `ping`.

The password is never in `uri`: `password_env` names the variable holding it. `tls` with `ca_file`
(and `cert_file` for a client certificate, its key in it or in `key_file`) replaces the connection
string's TLS options; the servers' certificates are always checked.

```json
{
  "sync": {
    "state_dir": "/var/lib/fit/sync",
    "pipelines": [
      {
        "name": "orders-to-mongo",
        "source": {"module": "generate", "params": {"records": 1000}},
        "sinks": [{"name": "mongo", "sink": {"module": "mongo", "params": {
          "uri": "mongodb://db1:27017,db2:27017,db3:27017/?replicaSet=rs0",
          "username": "fit", "password_env": "MONGO_PASSWORD",
          "tls": true, "ca_file": "/etc/fit/mongo-ca.pem",
          "database": "shop", "write_concern": "majority"}}}]
      }
    ]
  }
}
```
