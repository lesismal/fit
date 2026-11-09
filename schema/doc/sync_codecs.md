A codec turns the bytes a source reads (a file's lines, a Kafka message, an HTTP body, an S3
object) into records, and records into the bytes a sink writes. The sources and sinks that read
and write bytes (the `file` source and sink, then Kafka, HTTP and S3) take one, named by `module`,
with its `params`; `line` when they name none. A record a codec makes has no position: the source
gives it one. A line a codec refuses still becomes a record: `after` holds the `line` and the
`error`, so that nothing read is lost.

| Module | What it does |
| --- | --- |
| [`canal_json`](#sync-codecs-canal_json) | Canal's JSON, as Canal and TiCDC write it: row changes of db.table typed by `mysqlType`, keyed by `pkNames`, with TiCDC's commit TS; written too |
| [`csv`](#sync-codecs-csv) | A row of comma-separated values, named by `columns`, read and written |
| [`debezium`](#sync-codecs-debezium) | Debezium's change events, typed by their schema when they have one, keyed by Debezium's key events; written without a schema |
| [`json`](#sync-codecs-json) | A JSON object as a row (`plain`), or a whole record in its canonical JSON (`record`) |
| [`line`](#sync-codecs-line) | A line as a message record's `after.line`, and back |
| [`raw`](#sync-codecs-raw) | A message's bytes as they are, in `after.value`, and back, for a byte-for-byte copy |
| [`ticdc_simple`](#sync-codecs-ticdc_simple) | TiCDC's simple protocol: schemas kept from its bootstrap and DDL messages type the rows and give their keys; decoded only |
| [`wasm`](#sync-codecs-wasm) | A WebAssembly plugin's `fit_sync_decode` and `fit_sync_encode`, for a format of your own |
