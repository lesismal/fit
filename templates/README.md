# Config templates

[English](README.md) | [简体中文](README.zh-CN.md)

A complete config template in each of fit's three formats, to copy and edit. Each holds every
field of the config and of every module's params, each with its description, type, choices and
default in a comment above it:

| Template | Format |
| --- | --- |
| [fit/fit.conf](fit/fit.conf) | fit's own [.conf format](../docs/reference/fit.md) |
| [yaml/fit.yaml](yaml/fit.yaml) | YAML |
| [json/fit.json](json/fit.json) | JSON with comments and trailing commas, which fit reads |

In each, a small config is written out and the rest is commented out, with its default or an
example value, to be uncommented as needed. Each runs as it is (`fit run -c templates/fit/fit.conf`):
a listener on :8080, an upstream and a route answering "hello from fit". In a commented-out
block, a field's comment is a comment within the comment (`# #` or `// //`), so that uncommenting
the block leaves it a comment. The routes show every handler, one route each, and every
middleware module, with all their params.

The templates are generated from the code, as the [config reference](../docs/reference/config.md)
is, and a test fails when they fall behind: regenerate them with `go test ./schema -update`, or
print them for the modules a binary has with `fit schema -template fit`, `yaml` or `json`. The
formats are compared in [Configuration](../docs/configuration.md).
