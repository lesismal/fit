Reads an Apache Doris or StarRocks table, or a query's rows, over MySQL's protocol (a frontend's
`query_port`, through fit's own client, `sync/mysql/wire`, on the pipeline's goroutine), as the
clickhouse source reads ClickHouse. It reads in pages of `page_rows`, ordered by `cursor` and `key`,
each page the rows after the last one read: `(cursor > x) OR (cursor = x AND key > y)`, spelled out,
since neither system compares row values. Its position is the literals of the last row's cursor and
key, so a restart resumes after that row, rows with the same cursor value included. With a cursor, it
polls for rows past the position every `poll_interval`; without one it reads the rows once and ends.

Every row is an insert, or a delete when `deleted_column` is true or non-zero. A row deleted from the
table is not seen, nor a row written later with a cursor value at or below one already read: the
cursor must grow with each write (an update time the writer sets, a version). `snapshot: "never"`
starts past the greatest cursor value there is. Values are typed by their columns as the mysql
source types them.
