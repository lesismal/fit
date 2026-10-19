# 001: A strict config decoder of fit's own

[English](001-strict-config-decoding.md) | [简体中文](001-strict-config-decoding.zh-CN.md)

Status: accepted, in I02 (2026-10-19).

## Context

fit's config must be strict: an unknown field, a value of the wrong type, a missing required field
or a duplicate key is an error, and every error says where it is, by path
(`routes[0].handler.params.status`) and by line and column. Module params are decoded later than
the config itself, by the module, and their errors need the same paths and positions.

`encoding/json` cannot do this. `DisallowUnknownFields` stops at the first problem and does not
say where it is; type errors carry a partial path without array indices; duplicate keys are
silently accepted, the last one winning; and a `json.RawMessage` holding a module's params loses
its position in the document. `encoding/json/v2` would help, but is not available in the Go
toolchain fit builds with.

## Decision

The `config` package has its own decoder, built on `encoding/json`'s tokenizer:

- the document is parsed into a tree that keeps each value's offset and each object's member
  order, refusing duplicate keys;
- a reflection-based decoder fills Go values from the tree by their `json` tags, collecting every
  error rather than stopping at the first, and reads extra tags: `fit:"required"`, `fit:"self"`,
  `enum`, `min`, `max`, `doc` and `module`;
- `config.Value` keeps a part of the tree with its path, for module params and for later checks
  to report positions in.

The `schema` package reads the same tags through `config.Fields`, so the JSON Schema and the
decoder cannot disagree.

## Consequences

- About 700 lines of parser, decoder and Value to maintain, covered by table tests and a fuzz
  target.
- Field names match exactly, unlike `encoding/json`'s case-insensitive matching; a near miss in
  case gets a "did you mean" hint.
- Every type a config or module params can use must be one the decoder supports: structs,
  pointers, slices, maps with string keys, strings, booleans, numbers and `config.Value`. A new
  kind of value (a duration, for instance) is added to the decoder and the schema generator
  together.
