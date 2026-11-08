A pipeline's `source` reads its records: one of these, named by `module`, with its `params`. A
source resumes from the pipeline's checkpoint, and gives each record the position it resumes
after.

| Module | What it does |
| --- | --- |
| [`clickhouse`](#sync-sources-clickhouse) | A ClickHouse table or query: a snapshot in pages by the key, then polling by a cursor column; chunked in a job |
| [`file`](#sync-sources-file) | Tails files and globs, following rotation by rename and by copy and truncation, with multiline events |
| [`generate`](#sync-sources-generate) | Numbered records of every value type, for trials and tests |
| [`wasm`](#sync-sources-wasm) | A WebAssembly plugin that polls, through fit's HTTP client to the hosts it is allowed, from its checkpoint |
