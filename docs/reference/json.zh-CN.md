# JSON 参考

[English](json.md) | [简体中文](json.zh-CN.md)

JSON 是 fit 的原生配置格式：[配置参考](config.zh-CN.md) 逐字段描述的就是它，Admin API 收发的是它，.fit 和 YAML 文件转换后得到的也是它。
以 `.json` 结尾的文件，以及用 `-c` 指定、按名字不属于其他格式的文件，都按 JSON 读取；不带 `-c` 时 `fit run` 读取 `fit.json`。三种格式的比较见 [配置](../configuration.zh-CN.md)。

## 严格读取

```json
{
  "listeners": [{"name": "web", "address": ":8080"}],
  "routes": [{"handler": {"module": "respond", "params": {"body": "hello"}}}]
}
```

fit 严格读取配置，写错的地方会报错，而不是被悄悄忽略：

- fit 不认识的字段是错误；字段名必须完全一致，包括大小写；
- 类型错误的值是错误，超出字段范围的数字、不在可选值中的字符串也是；
- 缺少必填字段是错误；
- 同一个对象中重复的键是错误，而大多数 JSON 解析器会保留最后一个；
- 模块的 `params` 在编译配置时由模块同样严格地检查。

每个错误都指明位置：`fit validate` 打印 `fit.json:4:27: listeners[0].protocols[1]: h3 needs tls`，即文件、行号和列号，
然后是字段路径。

## 注释和末尾逗号

允许 `//` 行注释、可跨行的 `/* */` 注释，以及对象或数组最后一个元素后的逗号，配置可以写明为什么这样写：

```json
// 生产环境的入口。
{
  /* 给健康检查用的明文 HTTP，只在内网。 */
  "listeners": [
    {"name": "health", "address": "10.0.0.10:8080",},  // 末尾逗号
  ],
  "routes": [
    {"handler": {"module": "respond", "params": {"body": "ok", /* "status": 503, */},},},
  ],
}
```

字符串中的 `//` 和 `/*` 是普通文本：`"http://example.com"` 是一个 URL。读取时，注释和末尾逗号被替换为空白，其余字节位置
不变，因此错误的行号和列号保持准确。Admin API 以标准 JSON 保存配置：`GET /config` 返回的内容没有注释。

## 值

| 类型 | 写法 |
| --- | --- |
| 时长 | Go 格式的字符串：`"500ms"`、`"30s"`、`"1m30s"`、`"24h"` |
| 大小 | 字节数，整数：16 MiB 写作 `16777216` |
| 地址 | 字符串：`":8080"`、`"127.0.0.1:8080"`、`"[::1]:8080"` |
| host 模式 | 字符串：`"example.com"`、`"*.example.com"`（一级）、`"**.example.com"`（一级或多级） |
| 路径模式 | 字符串：`"/exact"`、`"/prefix/*"` |
| 模块 | 对象：`{"module": "<name>", "params": {...}}` |

## 编辑器

`fit schema` 打印配置和每个模块参数的 JSON Schema，仓库中也保存了一份 [schema/fit.schema.json](../../schema/fit.schema.json)；
编辑器加载后可以在输入时补全字段名、检查值。标准 JSON 编辑器会把注释标为错误，而 fit 接受注释：在编辑器中把文件当作
`.jsonc`，或为它打开注释支持。

## 转换

`fit adapt -c fit.json -to fit -o fit.fit` 把配置写成 .fit 文件，`-to yaml` 写成 YAML；对 .fit 或 YAML 文件（或多个文件）
执行 `fit adapt`，会写出缩进好的 JSON。
