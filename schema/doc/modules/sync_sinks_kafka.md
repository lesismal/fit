Produces records to Kafka (3.x and later) and Redpanda through fit's own client of Kafka's
protocol, on connections of the sink's own, never on a fib worker. A record goes to the topic
`topic` names, `{namespace}` by default (a `route` transform can map namespaces to topics first);
with `create_topics`, a topic that does not exist is created when a record first goes to it.

The message's key is the record's key: a string or bytes as they are, any other key (a map of
columns) as plain JSON, `{"id":1}`; its value is what `codec` makes of the record, `raw` by default
(after's `value` field as it is, so a Kafka-to-Kafka copy is byte for byte, or the record's
canonical JSON when it has none), and a delete with a key is a tombstone, a null value
(`deletes: "tombstone"`), which a compacted topic takes as the key gone. A keyed message goes to the
partition the Java client's partitioner (murmur2) gives its key, so a key's messages stay in order in
one partition and land where Java producers would put them; messages without a key go to one
partition per batch, round the partitions batch by batch. Its timestamp is the record's commit time.

A batch's messages are sent together, a record batch per partition of at most `max_batch_bytes`,
compressed (`lz4` by default; `none`, `gzip`, `snappy`, `zstd`), acknowledged by every in-sync replica
(`acks: "all"`). Leaders that move are followed. A partition the brokers refuse fails the batch from
its first record not written, which the pipeline's `retry` writes again; a message too large for the
topic fails its record for good, and `on_error` takes it. Delivery is at least once: the producer is
not idempotent, so a batch written again after a crash, or a produce whose answer was lost, writes
its messages again. **Test connection** reads the cluster's metadata, and the topic's when `topic` is
fixed and none is to be created.

```json
{
  "sync": {
    "state_dir": "/var/lib/fit/sync",
    "pipelines": [
      {
        "name": "orders-to-kafka",
        "source": {"module": "postgres", "params": {"address": "db:5432", "user": "repl", "password_env": "PG_PASSWORD",
          "slot": "fit", "publication": "fit_pub", "database": "shop"}},
        "sinks": [{"name": "kafka", "sink": {"module": "kafka", "params": {
          "brokers": ["kafka-1:9093"], "tls": true, "sasl": "SCRAM-SHA-512", "username": "fit", "password_env": "KAFKA_PASSWORD",
          "topic": "cdc.{namespace}", "codec": {"module": "debezium"}, "compression": "zstd",
          "create_topics": {"partitions": 6, "replication_factor": 3}}}}]
      }
    ]
  }
}
```
