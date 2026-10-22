# Writing modules

[English](modules.md) | [简体中文](modules.zh-CN.md)

A module is a Go package that adds a handler or a middleware to fit. It is compiled into the
binary with `fit build`, as xcaddy builds Caddy's plugins in: for code where speed matters, it
runs as fit's own modules do, on fib's workers, with nothing between it and the request.
[`examples/module/hello`](../examples/module/hello) is a template: a middleware that adds a field
to every response, with its own `go.mod`.

## What a module is

A module is a Go type registered under a namespaced ID in an `init` function:

```go
func init() {
	module.Register(module.Info{ID: module.Middlewares + ".hello", New: func() any { return new(Hello) }})
}
```

The namespace says what it is, and what it must implement:

| Namespace | A config names it in | It implements |
| --- | --- | --- |
| `http.handlers` (`module.Handlers`) | a route's `handler` | `module.Handler`, fib's `http.Handler` |
| `http.middleware` (`module.Middlewares`) | a route's `middleware` | `module.Middleware`: `Wrap(next Handler) Handler`, called once when the config is compiled |

A config names the module by the last part of its ID, `hello`, which must be unique in its
namespace. Its package registers it when it is imported, so importing the package is all a binary
needs.

## Params

The module's exported fields are its params, decoded from the config as strictly as the config
itself: an unknown field or a wrong type is an error with its path and position. Struct tags say
the rest, and become the module's part of the JSON Schema (`fit schema`):

| Tag | |
| --- | --- |
| `json:"name,omitempty"` | the param's name |
| `doc:"..."` | what it does, and its default in words |
| `default:"..."` | its default, as JSON (`"\"world\""`, `"5"`) |
| `min:"0"`, `max:"599"` | bounds of a number |
| `enum:"a,b,c"` | the values a string may take |
| `fit:"required"` | the param must be set |

Unexported fields hold what the module builds from its params.

## Its life

- **`Provision(ctx *module.Context) error`**, when it has one, runs once, when a config using the
  module is loaded: it checks the params beyond what the decoder can (`ctx.Errorf("field", ...)`
  points at the param), and builds what serving takes, so that requests only read it.
  `ctx.Upstream(name)` gives the config's upstream pools; `ctx.Graph()` the hold a request that
  outlives its handler's return takes on the config.
- **`Cleanup()`** runs once the config is no longer used, after the last request using the module
  is done: for a goroutine, a file, a pool the module holds.
- **`Drain()`**, for a module whose requests outlive its handler's return, runs when its config was
  swapped out the drain timeout ago and requests still use it: end what is under way.

A reload provisions the new config's modules before the old config's are cleaned up, so a module
can hand what it holds on to its successor, as the cache's zones are.

## The rules

A module runs on fib's workers, and follows [fit's development rules](rules.md), the first above
all: **nothing on a fib worker blocks**. No synchronous network or disk I/O, no `time.Sleep`, no
waiting on a channel or a lock held for long.

- A handler that answers after it returns takes `c.Retain()` and calls `c.Release()` once it has
  answered, and hears through `c.OnCancel` that the client left.
- Work that can only block runs on a bounded pool of the module's own, with a timeout, and answers
  from there.
- Request bodies are taken with `c.OnBody` as they arrive, not read whole; a response is written
  piece by piece with `c.WriteNoWait` and `c.OnWritable`, or whole with `c.WriteResponse`.
- Every buffer, queue and map has a bound, with a safe default.
- A middleware changes a response through `c.OnHeader`, `c.OnBodyFilter` and `c.OnFinish`, which
  work however the handler after it writes.

## Testing

A module is tested as fit's own are: a handler runs without a network through
`fibhttp.NewStreamContext(nil, request, stream)` and `fibhttp.Serve`, with a `fibhttp.Stream` of
the test's own recording the response; or a config using it is served by `runtime.Compile` and
`listener.Start` on a loopback port.

## Building it in

```bash
fit build -with github.com/you/fit-hello@v1.0.0
```

builds `./fit` with every module and provider fit ships with and `github.com/you/fit-hello`: a main
package that imports fit's command line (package `cli`), `modules/standard`, `provider/standard`
and the module's root package, built with the go command on `PATH`. The flags:

| Flag | |
| --- | --- |
| `-with path[@version]`, `-with path=dir` | a module to build in, by version (`latest` when left out) or from its source in `dir`; repeated for each |
| `-without pkg` | leave out `github.com/lesismal/fit/modules/standard` (every handler and middleware) or `github.com/lesismal/fit/provider/standard` (every provider); a config that names one left out is refused |
| `-fit v`, `-fit dir` | the fit to build on: a version, or a directory of fit's source; the running fit's own version when left out |
| `-o file` | the binary; `fit` when left out |
| `-keep dir` | keep the generated main module in `dir`, to read or build again |

`GOOS` and `GOARCH` build for another system. The binary's `fit version` says `+build`, and
`go version -m fit` lists the modules in it.
