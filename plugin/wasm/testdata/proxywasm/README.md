# Proxy-Wasm filters for plugin/wasm's tests

`http_routing` and `properties` are the examples of the same names in the
[Proxy-Wasm Go SDK](https://github.com/proxy-wasm/proxy-wasm-go-sdk), at commit `44c7d5847745`,
copied unchanged: filters written for Envoy that fit's tests run as they are. They are © Tetrate,
under the Apache License 2.0, as their headers say. The tests build them with:

```bash
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o http_routing.wasm ./http_routing
```
