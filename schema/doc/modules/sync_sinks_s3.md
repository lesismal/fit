Writes records to S3 or a compatible (MinIO, SeaweedFS, Ceph, Cloudflare R2 and the like) as
objects of lines, through fib's asynchronous HTTP client, each request signed with AWS Signature
Version 4. Each batch the sink is given becomes one object, or several in order when its lines pass
`max_object_bytes`, or when its records' namespaces change and `key` names the namespace. An object
is put whole, in one `PUT`, so readers see it only once complete; its SHA-256 goes in the signature,
so the server stores it only if every byte arrived. A line is made by `codec` (`line` when left out,
`json`, `csv` or a WebAssembly codec), and with `gzip` the object is gzipped.

An object's key comes from the `key` template and its first record: `{namespace}`, the date and
time parts of its commit time in UTC (`{date}`, `{yyyy}`, `{mm}`, `{dd}`, `{hh}`, `{timestamp}`),
`{position}` (20 hex digits of a SHA-256 of its namespace and position) and `{ext}`. A batch written
again, after a failed write or a crash before the checkpoint, is the same records with the same
positions, so it makes the same keys and the same bytes and is written over itself. A replay cut
into other batches (the batch's `max_wait` may close it at another record) writes objects of other
names, so records between the checkpoint and the crash can be in two objects: at least once, as
every sink. A record without a commit time is named by the time of the write, which a replay does
not repeat.

Objects roll by the sink's `batch`: an object holds at most `batch.max_records` records and
`batch.max_bytes` bytes, and waits at most `batch.max_wait`. S3 bills each request, so give the sink
larger batches than the defaults, and a `queue` large enough to hold them:

```json
{
  "sync": {
    "state_dir": "/var/lib/fit/sync",
    "pipelines": [
      {
        "name": "logs-to-s3",
        "source": {"module": "file", "params": {"paths": ["/var/log/app/*.log"]}},
        "queue": {"max_records": 200000, "max_bytes": 268435456},
        "sinks": [{"name": "archive", "sink": {"module": "s3", "params": {
          "endpoint": "https://minio.internal:9000", "bucket": "logs",
          "access_key_id": "fit-writer", "secret_access_key_env": "S3_SECRET",
          "codec": {"module": "json"}, "gzip": true,
          "key": "app/{yyyy}/{mm}/{dd}/{timestamp}-{position}{ext}"}},
          "batch": {"max_records": 100000, "max_bytes": 134217728, "max_wait": "5m"}}]
      }
    ]
  }
}
```

Without `endpoint` the sink writes to AWS, at `https://s3.<region>.amazonaws.com`, naming the bucket
in the host (`addressing: "virtual"`); with one, in the path, which every compatible takes. Requests
are signed for `region`. Credentials are an access key's ID and its secret in an environment
variable, and a session token for temporary ones; neither is renewed while the pipeline runs, and
instance profiles are not read. The bucket is not made. A failed write, of any status, is retried by
the pipeline; a record the codec cannot encode, or whose key is longer than 1024 bytes, fails for
good and goes to `on_error`, the objects before it written. In a job, the sink has
`job.max_parallel` writers.
