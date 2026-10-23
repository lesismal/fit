Runs a WebAssembly plugin on each request, on a pool of its own: one of fit's interface, or a
Proxy-Wasm filter.

```json route
{
  "middleware": [{"module": "wasm", "params": {"path": "/etc/fit/plugins/auth.wasm", "config": "{\"mode\": \"strict\"}", "timeout": "50ms", "on_error": "fail"}}],
  "handler": {"module": "proxy", "params": {"upstream": "app"}}
}
```
