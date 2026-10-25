Runs a WebAssembly plugin on each request, on a pool of its own: one of fit's interface, or a
Proxy-Wasm filter. A Proxy-Wasm filter that exports response callbacks is given the response's
header, body and trailer too, its callbacks running on the same pool while the response is held
for them ([decision 023](../decisions/023-proxy-wasm-response.md)); `response` narrows or turns
them off.

```json route
{
  "middleware": [{"module": "wasm", "params": {"path": "/etc/fit/plugins/auth.wasm", "config": "{\"mode\": \"strict\"}", "timeout": "50ms", "on_error": "fail"}}],
  "handler": {"module": "proxy", "params": {"upstream": "app"}}
}
```
