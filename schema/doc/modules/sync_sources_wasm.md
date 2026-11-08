Runs a WebAssembly plugin that exports `fit_sync_poll`: given the checkpoint, empty at the start,
it emits records and sets the next checkpoint, JSON of its own, and says whether it has caught
up. Its only way out is the host module's `http_request`, on fit's asynchronous HTTP client, to
the hosts `allow_hosts` names, and `sleep`; it has no sockets. A request to any other host fails
the poll, as a trap or a poll past `timeout` does, and the pipeline starts again from its
checkpoint. A poll's records go out as a whole: their transactions and positions are cleared, and
the last carries the poll's checkpoint, so the checkpoint never falls inside a poll. Once the
plugin has caught up, the source waits `poll_interval`, or ends in mode `once`.

```json
{
  "sync": {
    "state_dir": "/var/lib/fit/sync",
    "pipelines": [
      {
        "name": "tickets",
        "source": {"module": "wasm", "params": {"path": "/etc/fit/plugins/tickets.wasm",
          "config": "{\"url\": \"https://api.example.com/tickets\"}", "allow_hosts": ["api.example.com"]}},
        "sinks": [{"name": "out", "sink": {"module": "stdout"}}]
      }
    ]
  }
}
```
