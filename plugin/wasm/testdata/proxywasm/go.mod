// The Proxy-Wasm filters plugin/wasm's tests run, examples of the
// Proxy-Wasm Go SDK copied unchanged; a module of their own, since fit does
// not depend on the SDK.
module example.com/fit-proxywasm-tests

go 1.24

require (
	github.com/proxy-wasm/proxy-wasm-go-sdk v0.0.0-20260105142703-44c7d5847745
	github.com/tidwall/gjson v1.14.3
)

require (
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
)
