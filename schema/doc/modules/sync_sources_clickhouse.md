Reads a ClickHouse table, or a query's rows, over its HTTP interface, through fib's asynchronous
HTTP client. It reads in pages of `page_rows`, ordered by `cursor` and then `key`, each page after
the last row read: `SELECT * FROM (<table or query>) WHERE (cursor, key) > (...) ORDER BY cursor,
key LIMIT page_rows`. Its position is the cursor's and the key's values of the last row read, so a
restart resumes right after it, and rows that share a cursor value are each read once. Once the
rows there are read, a source with a `cursor` asks again every `poll_interval`; one without a
cursor, or in a job, ends.

The cursor is a column that grows as rows are written or changed, as an update time
(`DEFAULT now64()`) or a version: a row written later with a cursor value at or below one already
read is not seen. Every row is an `insert`, or a `delete` when `deleted_column` is true or non-zero;
a row deleted from ClickHouse is not seen. Values are typed by their column's type: integers as int
or uint (past 64 bits as decimal), decimals, floats, `Bool`, `Date` as a date, `DateTime` as a time
(also the records' commit time when it is the cursor), strings, `UUID`, enums and addresses as
strings, arrays as arrays, `Map`, `Tuple` and `JSON` as JSON.

`snapshot: "never"` starts past the greatest cursor value there is. In a job (`mode: "once"`) with
`chunk_rows`, the snapshot is split into ranges of the key, planned by skipping `chunk_rows` rows
from each chunk's start, and read `job.max_parallel` at a time.

```json
{
  "sync": {
    "state_dir": "/var/lib/fit/sync",
    "pipelines": [
      {
        "name": "events-out",
        "source": {"module": "clickhouse", "params": {
          "urls": ["http://ch1:8123"], "username": "fit", "password_env": "CH_PASSWORD",
          "query": "SELECT * FROM app.events WHERE kind != 'debug'",
          "key": ["id"], "cursor": "updated_at", "poll_interval": "10s"}},
        "sinks": [{"name": "out", "sink": {"module": "stdout"}}]
      }
    ]
  }
}
```
