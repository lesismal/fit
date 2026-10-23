# YAML reference

[English](yaml.md) | [简体中文](yaml.zh-CN.md)

A config in YAML has exactly the fields of fit's native config, key for key: every field in the
[config reference](config.md) is written the same way, and the reference shows each chapter's
example in YAML. A file ending in `.yaml` or `.yml` is read as YAML by `fit run`,
`fit validate` and `fit adapt`; [Configuration](../configuration.md) compares the formats.

## How YAML maps onto the config

| YAML | Config |
| --- | --- |
| a mapping | an object; its keys are the fields' names, exactly |
| a sequence | an array |
| a string, plain or quoted, or a block string (`\|`, `>`) | a string |
| an integer or a float | a number |
| `true`, `false` | a boolean |
| `null`, `~` | null, as a field left out |

```yaml
# A listener, an upstream and two routes.
listeners:
  - name: web
    address: ":8080"
    protocols: [http/1.1, h2c]
upstreams:
  - name: app
    addresses: [10.0.0.1:8080, 10.0.0.2:8080]
    load_balancing: least_conn
    health_check:
      path: /healthz
      interval: 5s
routes:
  - name: api
    match:
      path: ["/api/*"]
    handler:
      module: proxy
      params:
        upstream: app
  - name: page
    handler:
      module: respond
      params:
        body: |
          <h1>Hello</h1>
        headers:
          Content-Type: text/html
```

## What repeats

Anchors (`&name`), aliases (`*name`) and merge keys (`<<:`) write a value once and use it again.
What aliases expand to is bounded, so that a small file cannot become a huge config.

```yaml
upstreams:
  - name: users
    addresses: [10.0.1.1:8080]
    <<: &limits {max_conns: 512, connect_timeout: 3s, max_fails: 2}
  - name: orders
    addresses: [10.0.2.1:8080]
    <<: *limits
listeners:
  - name: web
    address: ":8080"
routes:
  - handler: {module: proxy, params: {upstream: users}}
```

Every key must be a field of the config, as in JSON: an anchor is defined where its value is
first used.

## Pitfalls

YAML 1.2 is read, not 1.1:

- `yes`, `no`, `on` and `off` are strings, not booleans: write `true` and `false`.
- Quote a value that starts with a character YAML gives a meaning: `*name` is an alias, `&name`
  an anchor, `{` and `[` start a mapping and a sequence, `!` a tag, `@` and a backquote are
  reserved. `":8080"` reads the same plain, but quoting addresses is clearest.
- Durations are strings: `5s` and `1m30s` need no quotes, `"5"` is not a duration.
- `030` is the integer 30; octal is `0o30`.

## Errors

Errors are given at the YAML's lines and columns: `fit validate -c fit.yaml` says
`fit.yaml:12:9: routes[0].handler.params.upstream: ...` for the value written there.

## Converting

`fit adapt -c fit.yaml -o fit.json` writes the native JSON; `fit adapt -c fit.json -to yaml`
writes JSON (or a .konf file) as YAML that adapts back to the same JSON. The Admin API speaks JSON: `GET /config` shows the native JSON a YAML file became.

## Editors

`fit schema` prints the config's JSON Schema, which YAML editors take too: with the YAML language
server, a first line `# yaml-language-server: $schema=<path to fit.schema.json>` completes and
checks fields as they are typed.
