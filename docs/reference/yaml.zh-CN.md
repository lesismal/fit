# YAML 参考

[English](yaml.md) | [简体中文](yaml.zh-CN.md)

YAML 配置的字段与 fit 原生配置完全相同，逐键对应：[配置参考](config.zh-CN.md) 中的每个字段在 YAML 中写法相同，参考中每一章
的示例也给出了 YAML 写法。以 `.yaml` 或 `.yml` 结尾的文件，`fit run`、`fit validate` 和 `fit adapt` 都按 YAML 读取；
三种格式的比较见 [配置](../configuration.zh-CN.md)。

## YAML 如何对应到配置

| YAML | 配置 |
| --- | --- |
| 映射（mapping） | 对象；键是字段名，必须完全一致 |
| 序列（sequence） | 数组 |
| 字符串，普通或带引号，或块字符串（`\|`、`>`） | 字符串 |
| 整数或浮点数 | 数字 |
| `true`、`false` | 布尔值 |
| `null`、`~` | null，等同于不写该字段 |

```yaml
# 一个 listener、一个 upstream 和两条路由。
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

## 复用重复内容

锚点（`&name`）、别名（`*name`）和合并键（`<<:`）让一个值写一次、多处使用。别名展开后的大小有上限，小文件不会膨胀成巨大
的配置。

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

和 JSON 一样，每个键都必须是配置的字段：锚点定义在它的值第一次出现的地方。

## 需要注意的地方

读取的是 YAML 1.2，不是 1.1：

- `yes`、`no`、`on` 和 `off` 是字符串，不是布尔值：请写 `true` 和 `false`。
- 以 YAML 赋予含义的字符开头的值要加引号：`*name` 是别名，`&name` 是锚点，`{` 和 `[` 开始映射和序列，`!` 是标签，`@`
  和反引号是保留字符。`":8080"` 不加引号也一样，但给地址加引号最清楚。
- 时长是字符串：`5s`、`1m30s` 无需引号，`"5"` 不是时长。
- `030` 是整数 30；八进制写作 `0o30`。

## 错误

错误指向 YAML 的行号和列号：`fit validate -c fit.yaml` 对写在那里的值给出 `fit.yaml:12:9: routes[0].handler.params.upstream: ...`。

## 转换

`fit adapt -c fit.yaml -o fit.json` 写出原生 JSON；`fit adapt -c fit.json -to yaml` 把 JSON（或转换后的 Caddyfile、nginx
配置）写成 YAML，再转换回来得到同样的 JSON。Admin API 使用 JSON：`GET /config` 显示 YAML 文件转换成的原生 JSON。

## 编辑器

`fit schema` 打印配置的 JSON Schema，YAML 编辑器也能使用：在 YAML language server 中，第一行写
`# yaml-language-server: $schema=<fit.schema.json 的路径>`，即可在输入时补全和检查字段。
