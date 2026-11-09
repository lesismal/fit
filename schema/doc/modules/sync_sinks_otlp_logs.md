Exports records as OpenTelemetry log records over OTLP/HTTP (`POST /v1/logs`) through fib's
asynchronous HTTP client: protobuf (`ExportLogsServiceRequest`, encoded by hand) or OTLP's JSON,
gzipped unless `compression` is `none`. The resource is the one fit's own telemetry export sends,
`service.name` (`service_name`, fit) and `service.version` (fit's), with `resource`'s attributes,
templates of the record's fields; records whose resources come out the same are exported under one,
in the scope `fit`. A log record's body is the line its `codec` makes, or, with `body: "row"`, the
record's row as a map of typed values; its time is `timestamp_field`'s or the commit time, its
observed time the export's, its severity `severity_field`'s (text, numbered as OTLP numbers it, or a
number), its trace and span IDs `trace_id_field`'s and `span_id_field`'s, and its attributes
`attributes`' templates (`fit.namespace` and `fit.op` by default).

A batch is exported in requests of up to `max_request_bytes` (before compression). A 429 or a 5xx is
sent again with a backoff, as `Retry-After` asks; a request the receiver refuses (400) is split
until the record it refuses is found, which fails for good (`on_error` takes it), the records before
it exported. A partial success (records the receiver dropped) is logged as a warning. OTLP has no
deduplication: a batch written again after a crash is exported again (at least once).
