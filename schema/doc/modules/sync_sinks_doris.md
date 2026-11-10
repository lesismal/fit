Loads records into Apache Doris or StarRocks with Stream Load (`PUT /api/<db>/<table>/_stream_load`),
through fib's asynchronous HTTP client, with `Expect: 100-continue`; a frontend's redirect to a
backend is followed with the credentials, but not one from https to http, which would send them and
the rows in the clear. Each run of a batch's records for one table is one load,
its rows JSON (a row a line on Doris, an array on StarRocks) or CSV, and its `columns` header the union
of the run's columns. The load's `label` is `label_prefix`, `_` and a hash of the database, the table,
the run's first and last records' origins (a Kafka partition and offset, a Pulsar message ID, a CDC
transaction and the record's place in it), or positions for a source without them, and its count: written again after a crash, the same records make
the same label and the server answers `Label Already Exists`, which counts as loaded once the earlier
load has finished (and is asked again while it runs).

An insert, an update or a message is its row (`after`). A delete is the row as it was (`before`), or
its key, with Doris's `__DORIS_DELETE_SIGN__` or StarRocks's `__op` set to 1, which a table of the
unique key model (Doris, merge on write) or the primary key model (StarRocks) takes as deleting the
key; a run without deletes sends no such column, so inserts load into any table, and `deletes:
"ignore"` drops deletes for a duplicate key table. Times are written as `DATETIME` text in
`time_zone`, bytes in base64. `partial_columns` updates only the columns the rows have. The table is
not made.

A load refused for its rows (`too many filtered rows`, a JSON it cannot parse) is split in two, each
half loaded, until the record it cannot take is found: that record fails for good (`on_error` takes
it), the records before it are loaded, and the error names the load's `ErrorURL`. Any other failure
is retried by the pipeline; a 429 or a 5xx is sent again with a backoff first. Labels outlive a
table dropped and made again (Doris keeps them `label_keep_max_second`, three days by default): after
a pipeline is reset, or its tables made afresh, change `label_prefix`, or the records loaded before
are refused as already loaded.

```sql
CREATE TABLE shop.orders (id BIGINT NOT NULL, status VARCHAR(20), total DECIMAL(12, 2))
UNIQUE KEY(id) DISTRIBUTED BY HASH(id) BUCKETS 8
PROPERTIES ("enable_unique_key_merge_on_write" = "true");
```
