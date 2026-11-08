Writes records to MySQL (5.7 and later), MariaDB (10.11 and later) or TiDB, through fit's own client
of their protocol, on a connection of the pipeline's own, never on a fib worker. The tables are the
user's: each needs a primary or unique key on the record's key columns.

An insert, an update or a message is the row (`after`) written with `INSERT ... ON DUPLICATE KEY
UPDATE`, every column set to the row's value; a delete is a `DELETE` by the key's columns (the
record's `key`, a map of columns), and an update whose key changed deletes the row of the key it
had (from `before`) first. Each run of a batch's records for one table and one set of columns is
one statement of many rows, as many as the server's `max_allowed_packet` and 65,535 placeholders
allow, and up to 1,000 keys a `DELETE`. Writing the same records again leaves the same rows, so a
replay after a crash converges.

`transactions: "source"` (the default) writes each source transaction as one transaction of the
server's, committed at its last record, even when a batch ends inside it (the transaction is held
open into the next batch), and the records without one, a snapshot's, of a batch together; so the
table never holds half of a source transaction. `transactions: "batch"` groups several source
transactions in one, committed at a batch's last record that ends one. Should the connection fail
while a transaction is held open, what it held is written again on the next connection.
`max_transaction_bytes` bounds a transaction: past it, it is committed at the next record that ends
a source transaction, and a source transaction larger than that alone is committed in parts, with
a warning. Against TiDB, whose transactions are bounded by `txn-total-size-limit`, it is 16 MiB when
left out; the sink also lets the source's values into `AUTO_RANDOM` columns
(`allow_auto_random_explicit_insert`).

Values: numbers as they are, decimals as their text, strings and bytes, times in UTC (the session's
`time_zone` is `+00:00`, so a `TIMESTAMP` holds the instant and a `DATETIME` the UTC wall clock),
dates, and JSON, arrays and maps as JSON text. A value the server refuses for good (a constraint, a
value too long, a table or column missing) fails its record, found by writing the statement's rows
one at a time, and `on_error` takes it, those before it committed; a deadlock, a lock wait timed
out, a conflict at commit or a connection lost is tried again by the pipeline's `retry`. `ddl`
records are logged and not applied (`ddl: "report"`), or fail (`ddl: "stop"`), so that `on_error:
"stop"` stops the pipeline before them, for a person to change the table.

TLS, when `tls` is set, is required and verified. **Test connection** logs in and runs `SELECT 1`.

```json
{
  "sync": {
    "state_dir": "/var/lib/fit/sync",
    "pipelines": [
      {
        "name": "orders-copy",
        "source": {"module": "file", "params": {"paths": ["/var/log/shop/orders.ndjson"], "codec": {"module": "json"}}},
        "transforms": [{"module": "key", "params": {"fields": ["order_id"]}}],
        "sinks": [{"name": "replica", "batch": {"max_records": 500, "max_wait": "200ms"},
          "sink": {"module": "mysql", "params": {
            "address": "db.internal:3306", "username": "fit_sync", "password_env": "MYSQL_SYNC_PASSWORD",
            "tls": true, "ca_file": "/etc/fit/mysql-ca.pem", "table": "shop.orders"}}}]
      }
    ]
  }
}
```
