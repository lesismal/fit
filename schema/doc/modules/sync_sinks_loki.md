Pushes records to Grafana Loki's push API (`POST /loki/api/v1/push`) through fib's asynchronous HTTP
client: protobuf (logproto's `PushRequest`, encoded by hand) compressed with snappy, as Promtail and
Alloy send it, or Loki's JSON. Each record is a line its `codec` makes (the line codec by default,
which writes a file source's lines as they are), in the stream its `labels` make: each label a
template of the record's fields, so that the streams are only as many as the values of the fields
named, never one per record. `structured_metadata` (Loki 3) carries values of many kinds beside the
line, out of the labels. A line's time is `timestamp_field`'s, or the record's commit time, or the
time of the write, a nanosecond apart for each record of a batch.

A batch is pushed in requests of up to `max_request_bytes` (before compression), a request for each
tenant (`tenant`, `tenant_field`, sent as `X-Scope-OrgID`), each stream's entries in time order. A
429 or a 5xx is sent again with a backoff, as `Retry-After` asks; a request Loki refuses (400: an
entry too far behind, a line too long, too many labels) is split until the record it refuses is
found, which fails for good (`on_error` takes it), the records before it pushed. Loki keeps one copy
of an entry pushed again with the same stream, time and line, so a batch written again after a crash
is stored once when its records have times; lines without one get the time of the write, and a
replay stores them again.
