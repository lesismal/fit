# fit development progress

[English](progress.md) | [简体中文](progress.zh-CN.md)

This file records what has actually been built, iteration by iteration, and where the next
session picks up. The [development plan](dev-plan.md) says what each iteration is meant to do;
this file says what was done, and where the work differs from the plan.

## Where things stand

| | |
| --- | --- |
| Last updated | 2026-10-19 |
| Milestone | M0, foundations: I01–I05 done; I06–I08 to do |
| fib | `github.com/lesismal/fib v1.2.0`, from the module proxy |
| What works | `fit validate` loads a config, checks it and provisions its modules; `fit schema` prints the JSON Schema; `fit version` |
| What does not | fit serves no traffic yet: that starts with I06 |

**Next iteration: I06, HTTP entry points and `fit run`.** Its dependencies are done. After it,
I07 and I08 (the benchmark harness) finish M0. I09, I11, I14, I21 and I22 also become possible once
I06 is done.

To continue, start a session with:

> Implement iteration I06 from docs/dev-plan.md. Read docs/progress.md, the index and that
> iteration's section in docs/dev-plan.md, and the design principles in docs/roadmap.md; then only
> the packages it lists. Keep the change within the iteration's scope, add tests, and when done
> mark it done in both dev-plan files and record it in both progress files.

## Code as built

| Package | Contents |
| --- | --- |
| `cmd/fit` | the CLI: `validate`, `schema`, `version`, `help` |
| `config` | the native config types; a strict JSON parser and decoder with error paths and positions; `Value`; semantic checks |
| `module` | the module registry; `Handler`, `Middleware`, `Matcher`, and provisional `Service` and `Upstream` interfaces; `Provisioner`, `Cleaner` |
| `handler/respond` | the `respond` handler |
| `modules/standard` | imports every module fit ships with |
| `runtime` | `Compile` from config to an immutable `Graph`; `Runtime` with atomic swaps and graph reference counts |
| `schema` | JSON Schema generation; `fit.schema.json`, kept up to date by a test |
| `internal/fibtest` | runs fib handlers in tests without a network |
| `examples` | `hello.json` |

A config today looks like this:

```json
{
  "listeners": [
    {"name": "web", "address": ":8080", "protocols": ["http/1.1", "h2c"]}
  ],
  "routes": [
    {
      "name": "hello",
      "listeners": ["web"],
      "priority": 10,
      "match": {"host": ["example.com"], "path": ["/hello", "/api/*"]},
      "middleware": [],
      "handler": {"module": "respond", "params": {"status": 200, "body": "hello\n"}}
    }
  ]
}
```

## Iterations

### I01 Repository skeleton and CI — done

- `go.mod` for `github.com/lesismal/fit`, Go 1.27 (fib's version), requiring fib v1.2.0.
- `cmd/fit` with `version`, which prints fit's version (settable with
  `-ldflags "-X main.version=..."`, otherwise the one Go records), fib's version from the build
  info, and the Go version and platform.
- `Makefile`: `build`, `test`, `race`, `vet`, `lint`, `fmt`, `fmt-check`, `schema`, `fuzz`, and
  `check` (fmt-check, vet, staticcheck, race tests).
- `.github/workflows/ci.yml`: gofmt; go vet and staticcheck for linux, darwin and windows; tests
  with the race detector on ubuntu and macOS; a 30-second run of `FuzzLoad`.
- `README.md` and `README.zh-CN.md`.

Differences from the plan: the packages are created as iterations fill them rather than up front,
since Go has no use for empty packages. CI has not run yet: it runs on the first push.

### I02 Config types v0 and strict loading — done

- Types: `Config`, `Listener` (name, address, protocols), `Route` (name, listeners, priority, match,
  middleware, handler), `Match` (host, path), `ModuleRef` (module, params).
- A decoder of fit's own, recorded in
  [decision 001](decisions/001-strict-config-decoding.md): unknown fields, wrong types, missing
  required fields, out-of-range numbers, values outside an enum and duplicate keys are all errors,
  every one reported (not only the first) with its path and line:column. A field whose name
  differs only in case gets a "did you mean" hint.
- `Config.Validate`: unique listener and route names, valid addresses (port 0 allowed, for tests),
  no two listeners on one address, no protocol listed twice, route listeners that exist, valid
  hosts (exact, `*.` wildcard or an IP) and paths (starting with `/`, `*` only at the end).
- A `Config` keeps what was written, without defaults, and marshals back to the same JSON; params
  keep their member order.
- Tests: table tests for every error kind, round-tripping, and `FuzzLoad`, which ran four million
  inputs without a failure.

Differences from the plan: a route has `middleware` (a list) and `handler` (one module) rather
than a single handler chain, which follows the roadmap's router → middleware → handler model and
makes the namespace of each module plain. Protocols accept `http/1.1` and `h2c` for now; I09 and
I10 add `h2` and `h3`.

### I03 JSON Schema and `fit validate` — done

- `schema.Generate` builds a draft 2020-12 JSON Schema from the config types' tags (through
  `config.Fields`, the same descriptions the decoder uses) and from the registered modules: a
  module ref's `module` is an enum of the namespace's modules, and its `params` follow that
  module's schema.
- `schema/fit.schema.json` is committed; `TestSchemaUpToDate` fails when it is stale, and
  `make schema` (or `go test ./schema -update`) rewrites it. The example config was checked
  against it with Python's `jsonschema`, which also rejected a bad config for each of its three
  errors.
- `fit validate -c file` loads the config, then compiles it, so module params are checked too;
  errors print as `file:line:col: path: message`, exit code 1. `fit schema` prints the schema.

Differences from the plan: the schema lives in a top-level `schema` package, not `config/schema`,
because it needs the module registry, which itself depends on `config`.

### I04 Module registry and core interfaces — done

- `module.Register`, `Lookup`, `Namespace`, `Load`, `Cleanup`. IDs are namespaced
  (`http.handlers.respond`); a config names the last part, and the namespace comes from where the
  name appears (`handler` → `http.handlers`, `middleware` → `http.middleware`).
- `Load` decodes params strictly into the module's struct, then calls `Provision` with a context
  whose `Errorf` points at a param; plain errors are given the params' path and position. An
  unknown module's error lists the modules the namespace has.
- `module.Handler` is fib's `http.Handler`, so fib's handlers and middleware can be reused.
  `Middleware.Wrap` is called once at compile time. `Service` and `Upstream` are marked
  provisional, to be settled in I14 and I19.
- `respond`: status (default 200, 1xx refused), headers (names and values checked) and body
  (refused on 204 and 304); the response is built once and shared, which fib allows.

### I05 Runtime graph and atomic swap — done

- `runtime.Compile` provisions every module, wraps middleware around handlers, gives each listener
  its routes (all routes, or those naming it) ordered by priority with ties in written order, and
  applies default protocols. It reports every failing module, and cleans up the modules it had
  provisioned when anything fails.
- `Runtime.Current` is lock-free: it retains the current graph with a CAS that refuses a graph
  whose count already reached zero, so a reader racing a swap never revives a graph being cleaned
  up. `Runtime.Swap` numbers graphs and releases the old one; the last `Release` cleans up its
  modules in reverse order.
- Tests, under the race detector: graphs swapped 300 times while 8 readers serve through whatever
  is current; no reader ever sees a cleaned-up module, and every graph is cleaned up exactly once.

Differences from the plan: route matching is not implemented; `Route` carries hosts and paths as
written, for I11's router.

## Notes for later iterations

- **fib version.** fit uses fib v1.2.0 from the module proxy. The local fib checkout's `main` is
  about 30 commits ahead of that tag. If I06 needs something newer, tag a fib release rather than
  committing a `replace` directive.
- **staticcheck.** A staticcheck built with an older Go cannot read Go 1.27 export data. Build it
  as CI does (against the latest `golang.org/x/tools`) and pass it to make:
  `make check STATICCHECK=/path/to/staticcheck`.
- **Testing handlers.** `internal/fibtest.Serve` runs a fib handler through
  `fibhttp.NewStreamContext` with a recording stream, with no network.
- **Error positions.** Error tests compare whole messages with line:column. Work out expected
  columns from the document rather than counting by hand.
- **License.** The repository has no LICENSE file yet (fib is MIT); that is the maintainer's call.
