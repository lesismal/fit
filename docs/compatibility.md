# Compatibility

[English](compatibility.md) | [简体中文](compatibility.zh-CN.md)

From 1.0, fit keeps what it promises here: a config that loads keeps loading, and a client of the
Admin API keeps working, across every 1.x release. What changes in a way that could break them waits
for a new version of the config schema or of the Admin API, and comes with a way over.

## What is covered

| Surface | Version | What it is |
| --- | --- | --- |
| The native config | schema version 1 | Every field in [`schema/fit.schema.json`](../schema/fit.schema.json), the fields of the modules in `modules/standard` included: its name, its type, what it means, and its default. Configs in YAML (`.yaml`, `.yml`) are the same config written another way. |
| The Admin API | v1 | Its paths and methods, the bodies they take and give, their status codes, and the fields `ETag`, `If-Match` and `Fit-Config-Version` (below). |
| The command line | 1 | The commands `run`, `validate`, `adapt`, `schema`, `version`, `hash-password` and `build`, their flags, and their exit codes. |
| Metrics | 1 | The names, types and labels of the metrics `/metrics` serves and OTLP exports, all starting `fit_`. |
| The access log | 1 | The fields of its JSON lines. |
| Modules in Go | Go's semantic import versioning | The `module` and `plugin/build` packages' exported API, which modules built with `fit build` use. fit's other packages may change in any release. |

Not covered:

- What fit writes for people rather than programs: messages, warnings, the dashboard, `fit adapt`'s
  output beyond its being a valid config, and the text of errors (their JSON shape is covered).
- The host functions of WASM plugins (the `fit` import module) and fit's subset of Proxy-Wasm
  (I71, I72, I81), which are experimental in 1.0; the `wasm` module's config is covered.
- The nginx converter's coverage of nginx's directives, which only grows.
- Performance, beyond the [roadmap's targets](roadmap.md#performance-targets).

## What a 1.x release may do

- Add config fields, modules, providers and enum values; a field added is optional, and leaving it
  out keeps the behavior of the release before.
- Add Admin API paths, optional request fields, response fields and response fields' values. A
  client should ignore fields it does not know.
- Add metrics, and labels to metrics that had none of that name.
- Add commands and flags.
- Tighten a default limit, or change a default, only to close a security hole or a resource that
  was unbounded; the release notes say so.
- Rename a field (below).
- Fix behavior that contradicts the docs or an RFC fit claims to follow.

A config that a 1.x release refuses, because it was wrong before and went unnoticed (two fields that
cannot go together, a value out of range), is a bug fix; the release notes list each.

## The schema's version

A config may say which version of the schema it is written for:

```json
{"schema_version": 1, "listeners": [...], "routes": [...]}
```

It is optional, and a config that leaves it out is read as version 1. A fit that reads version 1
refuses a config that says 2, rather than read it wrongly, and the schema says it reads 1: its title
is "fit config, schema version 1" and its `schema_version` takes only 1.

## Renamed fields

A field renamed within a version keeps its old name until the next one. A config that uses the old
name loads as before, and fit says where:

```text
fit.json:12:40: routes[0].middleware[0].params.max_size: deprecated: renamed "max_bytes", which takes the same value; the old name goes in config version 2
```

`fit validate` and `fit run` print it, at the old name's line and column in the file (YAML too),
both at start and on each reload, and the Admin API returns it in the `warnings` of a load's
response. The schema keeps the old name, marked `deprecated`, so that editors strike it through. A
config may not give both names.

These were renamed for 1.0, so that every size in bytes ends in `_bytes` and every period in
`_interval`:

| Module | Old name | Name |
| --- | --- | --- |
| `cache` | `max_size` | `max_bytes` |
| `cache` | `max_entry_size` | `max_entry_bytes` |
| `cache` | `disk.max_size` | `disk.max_bytes` |
| `compress` | `min_length` | `min_bytes` |
| `jwt` | `jwks_refresh` | `jwks_refresh_interval` |
| `wasm` | `max_memory` | `max_memory_bytes` |

## The Admin API, v1

| Method and path | What it does |
| --- | --- |
| `GET /config`, `GET /config/{path...}` | The running config as it was written, or the part at a path |
| `PUT`, `PATCH`, `DELETE` on `/config` and `/config/{path...}` | Replace, merge patch (RFC 7396) or delete the config or the part at a path, and apply the result |
| `POST /load` | Apply a whole config |
| `GET /config/effective` | The config merged with what providers provide, and where each part came from |
| `GET /versions`, `GET /versions/{n}` | The configs applied and kept, and one of them |
| `POST /rollback/{n}` | Apply a kept config again, as a new version |
| `GET /status` | The listeners, connections, upstreams and their health, and certificates |
| `GET /metrics` | Metrics in Prometheus's text format |
| `GET /cache`, `POST /cache/purge` | The cache's zones, and purges by URL, tag or zone |
| `GET /dashboard/` | The dashboard, for people (not covered) |

A path in the config is a JSON pointer's tokens. A change answers with the version applied, in the
body (`version`) and in `Fit-Config-Version`, and with the config's `ETag`; a change sent with
`If-Match` is applied only to the config that ETag names, and is answered 412 otherwise. A config
that does not load is answered 400 with `{"error": ..., "errors": [{"path", "line", "column",
"message"}]}`.

A v2 of the Admin API, if one is needed, would be served under `/v2/` beside v1's paths, which would
stay for the rest of fit's major version.

## A new version

A change that cannot keep old configs or clients working waits for a new schema or Admin API
version, which comes with:

- a list of every change, and why;
- for the config, `fit adapt`, which rewrites a config of the version before into the new one, and
  the new fit reading the version before, with warnings, for at least one release;
- for the Admin API, the version before served beside the new one for the rest of the major
  version.
