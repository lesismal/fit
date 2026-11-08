Reads the objects of an S3 or compatible bucket whose keys begin with `prefix`, in key order, as
lines, through fib's asynchronous HTTP client, each request signed with AWS Signature Version 4.
Objects are listed with `ListObjectsV2` from the last key read (`start-after`), and each is read in
ranges of `range_bytes`, so none is held whole, each only while the
object's ETag is the one the listing gave. Keys ending in `.gz` are gunzipped (`decompress`). Each
line goes through `codec` (`line` when left out): a line the codec fails on is a message record of
the line and the error, and a line longer than `max_line_bytes` is cut there.

A record's namespace is the object's key (or `namespace`), its key the object and the line's offset,
and its commit time the object's last modification. Its position is the object's key and the offset
past the line, so a restart resumes inside the object, right after the last line checkpointed; a
gzipped object is read again from its start and the lines before the offset skipped. The last
line's position says the object is read to its end, and a restart goes on with the keys after it.

Once every object listed is read, a stream lists again every `poll_interval` for keys past the
last; a job (`mode: "once"`) ends. So keys must grow as objects are added, as the `s3` sink's do
with a date and a timestamp in them: an object added under a key before the last read is not seen,
and an object written over after it was read is not read again. An object gone between its listing
and its reading is skipped with a warning; one changed while it is being read stops the run, which
the pipeline starts again from its checkpoint.

```json
{
  "sync": {
    "state_dir": "/var/lib/fit/sync",
    "pipelines": [
      {
        "name": "archive-to-search",
        "source": {"module": "s3", "params": {
          "endpoint": "https://minio.internal:9000", "bucket": "logs", "prefix": "app/2026/",
          "access_key_id": "fit-reader", "secret_access_key_env": "S3_SECRET",
          "codec": {"module": "json"}}},
        "sinks": [{"name": "es", "sink": {"module": "elasticsearch", "params": {
          "urls": ["https://es:9200"], "index": "app-logs"}}}]
      }
    ]
  }
}
```
