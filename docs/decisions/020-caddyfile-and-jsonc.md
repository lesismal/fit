# 020: A Caddyfile converter, and JSON with comments

[English](020-caddyfile-and-jsonc.md) | [简体中文](020-caddyfile-and-jsonc.zh-CN.md)

Status: accepted, in I79 (2026-10-23). Its first point, the Caddyfile as fit's block syntax, is
replaced by [decision 021](021-fit-format-and-config-files.md): fit's own `.fit` format, with the
Caddyfile converted as a migration tool.

## Context

[Decision 019](019-yaml-config.md) made YAML fit's friendlier format and left a Caddyfile-like
syntax to a later adapter, if users asked for one. They did, and asked too that JSON configs take
C and C++ comments and a comma after the last element of an object or array.

## Decision

- **Caddy's own Caddyfile, not a syntax of fit's.** `adapter/caddyfile` reads Caddyfiles as Caddy
  writes them, with Caddy's directive names, and converts a common subset to native JSON, as
  `adapter/nginx` does nginx configs: a Caddyfile people already have works, the syntax and its
  docs are Caddy's, and fit's fields stay documented once. What it does not convert is listed by
  line, never dropped silently. No new dependency: the lexer and parser are fit's, bounded in
  tokens, nesting and imports.
- **Routes by coverage.** Caddy runs a chain of handlers picked by directive order and matchers;
  fit picks one route. Each handler becomes a route, and each middleware directive goes on the
  routes its matcher covers, splitting a route it covers in part, with what fit's match fields
  cannot hold of both matchers put in `match.expr`.
- **JSON with comments is still JSON.** `config.Standardize` blanks out `//` and `/* */` comments
  and trailing commas, byte for byte, so that every position in the result is the same line and
  column in what was written. fit's JSON parser runs it, and so do the entry points that keep a
  config, `fit run`'s file and the Admin API, which therefore keep standard JSON: what is merged
  with providers, edited by JSON pointer and read back with `GET /config` is plain JSON, its
  comments gone.

## Consequences

- `fit run`, `fit validate` and `fit adapt` take a file named `Caddyfile`, or ending in
  `.caddyfile`; adapters may register whole file names as well as extensions.
- A config's comments do not survive the Admin API: `GET /config` returns the JSON with them
  blanked out.
- Caddy's internal CA, CEL expressions, `templates`, `map` and file imports are not converted.
