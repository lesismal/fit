# 019: YAML as fit's friendlier config format

[English](019-yaml-config.md) | [简体中文](019-yaml-config.zh-CN.md)

Status: accepted, in I73 (2026-10-23).

## Context

fit's native config is JSON, strictly decoded, with errors that name a path and a position. I73
adds a format that is easier to write by hand, and `fit adapt` to turn it into native JSON. The
plan named the candidates: YAML, a syntax of fit's own like Caddy's Caddyfile, or another.

## Decision

- **The format is YAML**, mapped key for key onto native JSON: a mapping is an object, a sequence
  an array, a scalar the value its YAML 1.2 tag says. It adds comments, no quotes or braces for
  most values, block strings, and anchors, aliases and merge keys for what repeats, which is most
  of what makes JSON tiresome to write; the config's names, shapes, defaults, schema and checks stay
  the native config's, with nothing new to learn or document twice.
- **Not a syntax of fit's own.** A Caddyfile-like format reads well for small sites, but it is a
  second language with its own grammar, shorthands and docs, which every config field would have to
  be given a place in and kept in step with; its round trip to JSON would lose what JSON has and it
  does not. That is the work of a separate, later adapter if users ask for it, and the nginx
  converter (I74) already covers the configs people bring with them.
- **The parser is go.yaml.in/yaml/v3**, the YAML organisation's continuation of gopkg.in/yaml.v3,
  already fit's dependency for kubeconfig files ([decision 011](011-kubernetes-client.md)): fit
  walks its node tree rather than decoding into Go values, which keeps keys in order, gives every
  value its line and column, and lets fit bound what aliases expand to.
- **Errors point at the YAML.** The adapter records where each config path's value was written,
  and the config's errors, which name paths, are moved there before they are printed.

## Consequences

- `fit run` and `fit validate` take a `.yaml` or `.yml` file as they take JSON, reloads and `-watch`
  included; the Admin API still speaks JSON, and `GET /config` shows the native JSON a YAML file
  became.
- YAML's own pitfalls are YAML 1.2's: `yes` and `on` are strings, `030` an integer, and a value such
  as `:8080` may be written bare. `fit adapt -to yaml` writes native JSON as YAML that adapts back to
  the same JSON.
