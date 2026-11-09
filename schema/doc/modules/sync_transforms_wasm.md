Runs a WebAssembly plugin, a WASI reactor, over the records: the plugin exports
`fit_sync_transform`, reads the batch, and emits zero or more records for each through the host
module `fit_sync`, so it drops, changes, splits and routes them. The records cross in a binary
form of lengths and tags, which plugins built on the guest SDKs take (`fit_sync_form`), or as
their canonical JSON for a plugin that does not, one built before it among them.
The records the pipeline holds are split into calls of `batch_records`, run on up to `instances`
instances at once, each with its own memory, and what they make goes on in order. A call that
fails, traps, runs past `timeout`, or asks for more than `max_memory_bytes`, fails every record of
its call, which `on_error` then takes, and its instance is made again; a plugin may fail a single
record too. A transform plugin makes no HTTP requests and does not sleep: only a source's and a
sink's may. Package `plugin/wasm/guest` is the Go side of the calls (`OnTransform`, `Emit`,
`Fail`, `Row`), `plugin/wasm/guest/rust` the Rust one; [decision 036](../decisions/036-sync.md)
describes the ABI. A TinyGo plugin is best built with `-scheduler=none`: TinyGo's default
scheduler costs each call hundreds of microseconds.

```json
{
  "sync": {
    "state_dir": "/var/lib/fit/sync",
    "pipelines": [
      {
        "name": "orders",
        "source": {"module": "generate", "params": {"namespace": "shop.orders"}},
        "transforms": [
          {"module": "wasm", "params": {"path": "/etc/fit/plugins/orders.wasm", "config": "{\"drop_tests\": true}", "timeout": "500ms"}}
        ],
        "sinks": [{"name": "out", "sink": {"module": "stdout"}}]
      }
    ]
  }
}
```
