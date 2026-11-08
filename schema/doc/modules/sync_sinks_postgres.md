Writes records to PostgreSQL 13 and later, over fit's own client of PostgreSQL's protocol (TLS with
`sslmode`, SCRAM-SHA-256 with channel binding), on connections of its own, never on fib's workers.
Inserts, updates and messages are upserts by key, `INSERT ... ON CONFLICT (key) DO UPDATE SET` the
row's other columns (`DO NOTHING` for a row of the key alone); deletes are `DELETE ... WHERE key IN
(...)`. Each run of a batch's records for one table, one operation and one set of columns is one
statement of at most `max_statement_rows` (1000) rows, a key held once in it (an upsert's last row).
A run of `copy_min_rows` (100) upserts or more, as a snapshot's, is copied instead: `COPY` into a
temporary table of the row's columns, dropped at the end of the transaction, then one `INSERT ...
SELECT ... ON CONFLICT` from it (`copy: "always"` or `"never"` choose). Writing the same records
again, after a crash, leaves the same rows. DDL is not written, and tables are not made: each must
have a primary key or unique index on the key's columns.

The table is `table` (`{namespace}`), split at its first dot into the schema and the table, so that
a MySQL source's `shop.orders` is the table `orders` of the schema `shop`. The key's columns are the
record's key's fields, or `key_columns`, their values taken from the key or the row; an upsert's row
gets the key's columns it lacks.

With `transactions: "batch"` (the default) each batch is one transaction. With `transactions:
"source"`, each of the source's transactions is one: a transaction's records are held across batches
until its last arrives (they are acknowledged, and the checkpoint cannot pass them), so a reader never
sees part of one. A record the server refuses for good (SQLSTATE class 22, a value the column cannot
read, or 23, a constraint) fails, and `on_error` takes it: with `batch` the rows before it are
written, found one at a time after savepoints; with `source` its whole transaction is left out, and
for a held record the batch's first record is the one failed. Every other error, a table or column
missing, a lost connection, a serialization failure, rolls the batch back and is tried again.

Values become the text PostgreSQL reads for the column, whose type the server gives:

| Record value | Written as | Columns it suits |
| --- | --- | --- |
| null | `NULL` | any |
| bool | `t` or `f` | `boolean` |
| int, uint | decimal digits | `smallint`, `integer`, `bigint`, `numeric` |
| float | its shortest text, `NaN`, `Infinity`, `-Infinity` | `real`, `double precision`, `numeric` |
| decimal | its text | `numeric` |
| string | as it is; a NUL byte fails the record | `text`, `varchar`, enums, `uuid`, `inet`, ... |
| bytes | `\x` and hex | `bytea` |
| time | its wall clock to the microsecond and its offset | `timestamptz` (the instant), `timestamp` (the wall clock) |
| date | `YYYY-MM-DD` | `date` |
| JSON | its text | `json`, `jsonb` |
| array | an array literal of its elements | arrays, as `text[]` |
| map | a JSON object | `json`, `jsonb` |

```json
{
  "sync": {
    "state_dir": "/var/lib/fit/sync",
    "pipelines": [
      {
        "name": "orders-to-pg",
        "source": {"module": "generate", "params": {"namespace": "shop.orders", "tx_size": 10}},
        "sinks": [{"name": "pg", "batch": {"max_records": 1000}, "sink": {"module": "postgres", "params": {
          "address": "pg.internal:5432", "user": "sync", "password_env": "PG_PASSWORD", "database": "shop",
          "ca_file": "/etc/fit/pg-ca.pem", "transactions": "source"}}}]
      }
    ]
  }
}
```
