# Config templates

[English](README.md) | [简体中文](README.zh-CN.md)

A complete config template in each of fit's three formats, to copy and edit:

| Template | What it holds |
| --- | --- |
| [caddyfile/Caddyfile](caddyfile/Caddyfile) | Every global option, site address form, matcher, directive and subdirective fit converts from a Caddyfile, with the choices of each in comments. |
| [yaml/fit.yaml](yaml/fit.yaml) | Every field of the config and of every module's params, each with its description, type, choices and default in a comment. |
| [json/fit.json](json/fit.json) | The same as the YAML, in JSON with comments and trailing commas, which fit reads. |

In each, a small config is written out and the rest is commented out, with its default or an
example value, to be uncommented as needed:

- The YAML and JSON templates run as they are (`fit run -c templates/json/fit.json`): a listener on
  :8080, an upstream and a route answering "hello from fit". Each field's comment is above it; in a
  commented-out block, a field's comment is a comment within the comment (`// //` or `# #`), so
  that uncommenting the block leaves it a comment. The routes show every handler, one route each,
  and every middleware module, with all their params.
- The Caddyfile template converts as it is (`fit adapt -c templates/caddyfile/Caddyfile`): one site
  using every directive, and commented-out sites for HTTPS, on-demand TLS and mutual TLS. Point
  its directories, upstreams and PHP socket at what the host has before running it. Lines starting
  with `## ` explain; lines starting with `# ` are code.

What a Caddyfile cannot say (layer 4 services, caching, rate limits, JWT, WASM, providers,
telemetry and fit's limits) is only in the YAML and JSON templates.

The YAML and JSON templates are generated from the code, as the
[config reference](../docs/reference/config.md) is, and a test fails when they fall behind:
regenerate them with `go test ./schema -update`, or print them for the modules a binary has with
`fit schema -template yaml` and `fit schema -template json`. The formats are compared in
[Configuration](../docs/configuration.md).
