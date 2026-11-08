A pipeline's sink, and its `dead_letter`, writes records: one of these, named by `module` in its
`sink`, with its `params`. A sink acknowledges a batch once it is written, and writes idempotently,
since what came after the checkpoint is written again after a crash.

| Module | What it does |
| --- | --- |
| [`clickhouse`](#sync-sinks-clickhouse) | ClickHouse, with `INSERT ... FORMAT JSONEachRow`: a deduplication token per insert, updates and deletes as rows of a `ReplacingMergeTree` |
| [`elasticsearch`](#sync-sinks-elasticsearch) | Elasticsearch and OpenSearch, with `_bulk`: documents by `_id` from the key, per-item errors, 429s waited out |
| [`file`](#sync-sinks-file) | Lines by a codec or a template, through fit's log file sink, rotated and kept as a log is |
| [`http`](#sync-sinks-http) | Batches posted as NDJSON or a JSON array, with headers, authentication and an idempotency key, retried on 5xx and timeouts |
| [`memory`](#sync-sinks-memory) | Keeps the records in memory, bounded, for tests and trials |
| [`s3`](#sync-sinks-s3) | S3 and compatibles (MinIO, SeaweedFS, Ceph, R2): each batch as objects of lines, named by its first record so a batch written again is the same object, gzipped if asked |
| [`stdout`](#sync-sinks-stdout) | Writes each record to standard output as a line of its canonical JSON |
| [`wasm`](#sync-sinks-wasm) | A WebAssembly plugin that writes each batch through fit's HTTP client to the hosts it is allowed |
