# 017: WASM plugins on wazero, with an ABI of fit's own

[English](017-wasm-runtime.md) | [简体中文](017-wasm-runtime.zh-CN.md)

Status: accepted, in I71 (2026-10-23).

## Context

I71 lets a route run a WebAssembly plugin for each request: loaded at run time, sandboxed, on a
pool of its own, reading and changing the request's and the response's fields. It needs a
WebAssembly runtime, which fit cannot reasonably write itself, and an ABI between fit and its
plugins. The roadmap asks for a runtime in pure Go, so that fit stays one static binary built
without cgo, and for the Proxy-Wasm ABI to be considered, so that existing filters can be reused.

## Decision

- **The runtime is wazero** (`github.com/tetratelabs/wazero`, v1.12.0, Apache 2.0), a WebAssembly
  runtime in pure Go with no dependency but `golang.org/x/sys`: it compiles to machine code on
  amd64 and arm64 and interprets elsewhere, bounds an instance's memory (`WithMemoryLimitPages`),
  stops a call whose context ends (`WithCloseOnContextDone`), and caches what it compiles. The
  alternatives bind C or Rust runtimes through cgo (wasmtime-go, wasmer-go, which is no longer
  maintained, and wasm3), which the static binary rules out.
- **The ABI is fit's own and small**, in I71: the plugin exports `fit_on_request`, and imports
  functions from the module `fit` to read the request's method, path, query, host, client address
  and its config, to read, set, add and delete the request's and the response's fields, to answer
  the request itself, and to log. Proxy-Wasm's is much larger (contexts, buffers, properties,
  timers, calls out), and is I72's to take up as far as the numbers allow.
- **Plugins are WASI reactors**, which Go builds since 1.24 (`GOOS=wasip1 GOARCH=wasm`,
  `-buildmode=c-shared`, `//go:wasmexport`): package `plugin/wasm/guest` is the Go side of the ABI.
  Any language that targets WASI and can export a function and import the host's works the same.
- **Calls run on a pool of the middleware's own**: one goroutine per instance (as many as fit has
  CPUs, at most 16, by default), each instance with its own memory; a bounded queue; a watchdog that
  stops a call past its timeout by cancelling its instance's context, after which the instance is
  made again. Never on a fib worker: a plugin's code can run for as long as it likes until it is
  stopped.

## Consequences

- fit's binary grows by about 3.9 MB with wazero linked in (21.1 to 24.9 MB on linux/amd64).
- A request through a plugin costs a hand-off to the pool and back, and the call itself, measured in
  I71's progress entry; each is bounded: by the instances and the queue, the call's timeout, the
  wait's, and the instances' memory.
- A Go plugin's instance carries Go's runtime, a few MB each, and its first call pays for the
  runtime's start (`_initialize`), when the instance is made, not per request.
