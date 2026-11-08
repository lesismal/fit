A pipeline's `source` reads its records: one of these, named by `module`, with its `params`. A
source resumes from the pipeline's checkpoint, and gives each record the position it resumes
after.

| Module | What it does |
| --- | --- |
| [`clickhouse`](#sync-sources-clickhouse) | A ClickHouse table or query: a snapshot in pages by the key, then polling by a cursor column; chunked in a job |
| [`elasticsearch`](#sync-sources-elasticsearch) | Elasticsearch and OpenSearch: a snapshot through a point in time, then polled by a cursor field; a job's snapshot in slices |
| [`file`](#sync-sources-file) | Tails files and globs, following rotation by rename and by copy and truncation, with multiline events |
| [`generate`](#sync-sources-generate) | Numbered records of every value type, for trials and tests |
| [`http`](#sync-sources-http) | Records posted to a `sync_ingest` route, each request answered once every sink has them |
| [`redis`](#sync-sources-redis) | Redis and Valkey streams read with `XREAD` from the last entry IDs read, no consumer group |
| [`s3`](#sync-sources-s3) | S3 and compatibles: the objects under a prefix in key order, read as lines in ranges, gunzipped by their `.gz`, resumed inside an object |
| [`wasm`](#sync-sources-wasm) | A WebAssembly plugin that polls, through fit's HTTP client to the hosts it is allowed, from its checkpoint |
