A codec turns the bytes a source reads (a file's lines, a Kafka message, an HTTP body, an S3
object) into records, and records into the bytes a sink writes. The sources and sinks that read
and write bytes (files from I166, then Kafka, HTTP and S3) take one, named by `module`, with its
`params`; none does yet. A record a codec makes has no position: the source gives it one.

| Module | What it does |
| --- | --- |
| [`wasm`](#sync-codecs-wasm) | A WebAssembly plugin's `fit_sync_decode` and `fit_sync_encode`, for a format of your own |
