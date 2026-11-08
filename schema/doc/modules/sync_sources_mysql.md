Follows MySQL (5.7 and later) or MariaDB (10.11 and later) as a replica, through fit's own client of
their protocol, on connections of the pipeline's own, never on a fib worker: the source registers
with its `server_id` and reads the binlog as a replica does. The server needs the binlog on
(`log_bin`), `binlog_format = ROW` and `binlog_row_image = FULL`, which the source checks when it
starts; the user needs `REPLICATION SLAVE`, `REPLICATION CLIENT` and `SELECT` on the tables read.

Each row inserted, updated or deleted in a table `tables` names (and `exclude` does not) becomes a
record of namespace `db.table`, its `after` (or, for a delete, its `before`) the row as a map of its
columns, its `key` the primary key's columns (or those `keys` names; none for a table with
neither). An update carries the row before and after; one that changed the key comes as a delete
of the old key and an insert of the new row, so that a sink writing by key does not keep both.
Values are typed by their columns: integers (unsigned ones as such), floats, decimals as their
text, dates, `DATETIME` in UTC and `TIMESTAMP` as the instant, `TIME` and zero dates as the
server's text, character strings as text, binary strings and `GEOMETRY` as bytes, `BIT` as an
unsigned integer, `ENUM` and `SET` as their names, `JSON` as JSON. Column names come from the binlog
where the server writes them (`binlog_row_metadata = FULL`), else from `information_schema`, read
again after each DDL: a table changed since the events read were written is then named as it is
now, its columns past the old count `@n` by position, which `binlog_row_metadata = FULL` avoids. A
DDL of a table read (`CREATE`, `ALTER`, `DROP`, `RENAME`, `TRUNCATE`) is a `ddl` record whose
`after` holds the `statement` and the `database`.

Records come a transaction at a time, in commit order, each with the transaction's GTID (or its
binlog file and offset on a server without GTIDs) and its commit time. The position, at each
transaction's end, is `{"gtids": "...", "file": "mysql-bin.000042", "pos": 1234}`: the server's
GTID set executed up to it (MySQL's, or MariaDB's `gtid_binlog_pos` form), and the file and offset
after it. A pipeline resumes from its checkpoint by GTIDs where the server has them
(`COM_BINLOG_DUMP_GTID`, or MariaDB's `@slave_connect_state`), else by file and offset; without a
checkpoint it starts from the server's binlog now. A stream cut, as by a server restarting, is read
again from the last transaction handed on, a transaction cut in the middle without handing on its
records twice. A position the server no longer has, its binlogs purged, is an error saying a new
snapshot is needed.

`heartbeat` (10s) is how long the server waits, with nothing to send, before it says so: the source
then reports no lag. Otherwise it reports, every 10 seconds, the binlog bytes the server holds past
its position (`SHOW BINARY LOGS`), as the pipeline's lag. In a job (`mode: "once"`) the source reads
to the binlog's end as it is when it asks, and stops.

TLS, when `tls` is set, is required and verified; MySQL 5.7's image needs `tls_rsa`. **Test
connection** logs in and checks the binlog's settings.

```json
{
  "sync": {
    "state_dir": "/var/lib/fit/sync",
    "pipelines": [
      {
        "name": "orders-to-search",
        "source": {"module": "mysql", "params": {
          "address": "db.internal:3306", "username": "repl", "password_env": "MYSQL_REPL_PASSWORD",
          "tls": true, "ca_file": "/etc/fit/mysql-ca.pem", "server_id": 4101,
          "tables": ["shop.orders", "shop.order_items"]}},
        "sinks": [{"name": "search", "sink": {"module": "elasticsearch", "params": {"urls": ["https://es:9200"]}}}]
      }
    ]
  }
}
```
