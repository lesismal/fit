A pipeline's `transforms` change records on their way from the source to the sinks, in order: a
transform may change a record, drop it or split it into several. Each is one of these, named by
`module`, with its `params`; WASM transforms come in I165. A transform reads a row's fields by
name at its top level; the row is `after`, or `before` when `after` is null, as for a delete. A
chain copies a record once, at the first transform that changes it, and the source's record stays
as it was read, for a dead letter. A record a transform fails on goes as `on_error` says.

A delete keeps its key: `fields` and `envelope` never touch the key (but for `fields`' renames,
which rename its fields too), `mask` blanks no key field, `key` keeps a delete's key when its row
lacks the fields, and a `key` built from a field an earlier transform removes is refused when the
config is checked.

| Module | What it does |
| --- | --- |
| [`filter`](#sync-transforms-filter) | Keeps or drops records by namespace globs, operation and fields' values |
| [`route`](#sync-transforms-route) | Renames namespaces, by an exact map and glob rules with templates |
| [`fields`](#sync-transforms-fields) | Unnests, nests, renames, drops, keeps and sets fields of the rows |
| [`key`](#sync-transforms-key) | Builds the key from fields: a map, a single value or joined text |
| [`cast`](#sync-transforms-cast) | Changes fields' types, by defined conversions |
| [`mask`](#sync-transforms-mask) | Replaces fields by a keyed HMAC-SHA256, a SHA-256, or null |
| [`envelope`](#sync-transforms-envelope) | Makes a document of a change: flat with metadata, a before/after pair, or Debezium's envelope |

For example, tables of `shop` to indices named after the table, with a string document ID, a
masked name, and a deleted flag for a sink that keeps deleted rows:

```json
{
  "sync": {
    "state_dir": "/var/lib/fit/sync",
    "pipelines": [
      {
        "name": "shop",
        "source": {"module": "generate", "params": {"namespace": "shop.orders"}},
        "transforms": [
          {"module": "filter", "params": {"namespaces": ["shop.*"], "exclude_namespaces": ["shop.tmp_*"]}},
          {"module": "route", "params": {"rules": [{"match": "shop.*", "to": "shop-{rest}"}]}},
          {"module": "key", "params": {"fields": ["id"], "format": "join"}},
          {"module": "mask", "params": {"fields": ["name"], "key_env": "SYNC_MASK_KEY"}},
          {"module": "envelope", "params": {"meta": {"deleted": "__deleted"}, "deletes": "rewrite"}}
        ],
        "sinks": [{"name": "out", "sink": {"module": "stdout"}}]
      }
    ]
  }
}
```
