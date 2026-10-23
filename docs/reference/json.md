# JSON reference

[English](json.md) | [简体中文](json.zh-CN.md)

JSON is fit's native config format: what the [config reference](config.md) describes field by
field, what the Admin API takes and returns, and what a .fit or YAML file becomes. A file ending
in `.json`, or named with `-c` and of no other format by its name, is read as JSON; `fit run`
reads `fit.json` when given no `-c`. [Configuration](../configuration.md) compares the
formats.

## Strict reading

```json
{
  "listeners": [{"name": "web", "address": ":8080"}],
  "routes": [{"handler": {"module": "respond", "params": {"body": "hello"}}}]
}
```

fit reads a config strictly, so that a mistake is an error rather than a setting quietly ignored:

- a field fit does not know is an error; names are matched exactly, case included;
- a value of the wrong type is an error, and so is a number out of a field's bounds or a string
  not among its values;
- a required field left out is an error;
- an object with the same key twice is an error, where most JSON readers keep the last;
- a module's `params` are checked by the module, as strictly, when the config is compiled.

Every error says where: `fit validate` prints `fit.json:4:27: listeners[0].protocols[1]: h3
needs tls`, the file, line and column, then the field's path.

## Comments and trailing commas

`//` comments to the end of a line, `/* */` comments over any lines, and a comma after the last
element of an object or array are allowed, so that a config can say why it is what it is:

```json
// Production edge.
{
  /* Plain HTTP for the health checker, inside the network only. */
  "listeners": [
    {"name": "health", "address": "10.0.0.10:8080",},  // a trailing comma
  ],
  "routes": [
    {"handler": {"module": "respond", "params": {"body": "ok", /* "status": 503, */},},},
  ],
}
```

Inside strings, `//` and `/*` are text: `"http://example.com"` is a URL. Comments and trailing
commas are blanked out as the config is read, every other byte kept in its place, so errors keep
their lines and columns. The Admin API keeps a config as standard JSON: what `GET /config` returns
has no comments.

## Values

| Kind | Written as |
| --- | --- |
| duration | a string of Go's form: `"500ms"`, `"30s"`, `"1m30s"`, `"24h"` |
| size | an integer of bytes: `16777216` for 16 MiB |
| address | a string: `":8080"`, `"127.0.0.1:8080"`, `"[::1]:8080"` |
| host pattern | a string: `"example.com"`, `"*.example.com"` (one label), `"**.example.com"` (one or more) |
| path pattern | a string: `"/exact"`, `"/prefix/*"` |
| module | an object: `{"module": "<name>", "params": {...}}` |

## Editors

`fit schema` prints the JSON Schema of the config and of every module's params, also kept as
[schema/fit.schema.json](../../schema/fit.schema.json); an editor given it completes field names
and checks values as they are typed. Standard JSON editors flag comments, which fit takes: name
the file `.jsonc` in the editor, or turn comments on for it.

## Converting

`fit adapt -c fit.json -to fit -o fit.fit` writes the config as a .fit file, and `-to yaml` as
YAML; `fit adapt` of a .fit or YAML file, or of several, writes this JSON, indented.
