A pipeline's sink, and its `dead_letter`, writes records: one of these, named by `module` in its
`sink`, with its `params`. A sink acknowledges a batch once it is written, and writes idempotently,
since what came after the checkpoint is written again after a crash.

| Module | What it does |
| --- | --- |
| [`elasticsearch`](#sync-sinks-elasticsearch) | Elasticsearch and OpenSearch, with `_bulk`: documents by `_id` from the key, per-item errors, 429s waited out |
| [`file`](#sync-sinks-file) | Lines by a codec or a template, through fit's log file sink, rotated and kept as a log is |
| [`memory`](#sync-sinks-memory) | Keeps the records in memory, bounded, for tests and trials |
| [`stdout`](#sync-sinks-stdout) | Writes each record to standard output as a line of its canonical JSON |
| [`wasm`](#sync-sinks-wasm) | A WebAssembly plugin that writes each batch through fit's HTTP client to the hosts it is allowed |
