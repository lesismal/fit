Reads Kafka (3.x and later) and Redpanda topics through fit's own client of Kafka's protocol, on
connections of the pipeline's own, never on a fib worker. Every partition of each of `topics` is
read from the offset in the pipeline's checkpoint: no consumer group is joined, and no offset is
committed to the brokers, so the checkpoint, saved once every sink has the records, is the only
cursor, and a restart, a crash or `kill -9` reads on from it (at least once: what came after the
last checkpoint is read again). A partition the checkpoint does not hold starts at `start`
(`earliest` by default) when the pipeline has none, at its earliest otherwise; partitions added to
a topic are found within a minute. A checkpoint's offset that retention has deleted stops the
pipeline (`offset_reset: "error"`) unless `offset_reset` says to go on from the earliest or latest.

Each message goes through `codec` into records, `raw` when none is named (the value as bytes in
`after.value`); `json`, `canal_json` (Canal's and TiCDC's canal-json), `debezium` (Debezium's and
TiCDC's), `ticdc_simple` (TiCDC's simple protocol) or a WASM codec read the formats. A record's key
is the message's (Debezium's key events decoded) unless the codec gives one, its namespace the topic
unless the codec or `namespace` does, and its commit time the message's timestamp unless the codec
has one. Several records of one message are a transaction, so the checkpoint never falls inside a
message. A message with a null value is a delete of its key (`tombstones: "delete"`), or nothing
(`"skip"`). A value the codec refuses becomes a message record holding the `value` and the `error`.
Only committed messages are read, unless `read_uncommitted`. The source reports how many offsets it
is behind its partitions' high watermarks as its lag.

In a job (`mode: "once"`) the source stops at each partition's end as it was when it started.
**Test connection** reads the topics' metadata. TLS, when `tls` is set, is required and verified;
SASL PLAIN, SCRAM-SHA-256 and SCRAM-SHA-512 authenticate.

```json
{
  "sync": {
    "state_dir": "/var/lib/fit/sync",
    "pipelines": [
      {
        "name": "orders-from-kafka",
        "source": {"module": "kafka", "params": {
          "brokers": ["kafka-1:9093", "kafka-2:9093"], "topics": ["shop.orders"],
          "tls": true, "ca_file": "/etc/fit/kafka-ca.pem",
          "sasl": "SCRAM-SHA-512", "username": "fit", "password_env": "KAFKA_PASSWORD",
          "codec": {"module": "debezium"}, "tombstones": "skip"}},
        "sinks": [{"name": "search", "sink": {"module": "elasticsearch", "params": {"urls": ["https://es:9200"]}}}]
      }
    ]
  }
}
```
