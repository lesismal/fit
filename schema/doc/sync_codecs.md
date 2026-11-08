A codec turns the bytes a source reads (a file's lines, a Kafka message, an HTTP body, an S3
object) into records, and records into the bytes a sink writes. The sources and sinks that read
and write bytes (the `file` source and sink, then Kafka, HTTP and S3) take one, named by `module`,
with its `params`; `line` when they name none. A record a codec makes has no position: the source
gives it one. A line a codec refuses still becomes a record: `after` holds the `line` and the
`error`, so that nothing read is lost.

| Module | What it does |
| --- | --- |
| [`csv`](#sync-codecs-csv) | A row of comma-separated values, named by `columns`, read and written |
| [`json`](#sync-codecs-json) | A JSON object as a row (`plain`), or a whole record in its canonical JSON (`record`) |
| [`line`](#sync-codecs-line) | A line as a message record's `after.line`, and back |
| [`wasm`](#sync-codecs-wasm) | A WebAssembly plugin's `fit_sync_decode` and `fit_sync_encode`, for a format of your own |
