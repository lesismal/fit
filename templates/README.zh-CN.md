# 配置模板

[English](README.md) | [简体中文](README.zh-CN.md)

fit 三种配置格式各有一份完整的配置模板，可复制后修改。每份都包含配置和每个模块参数的全部字段，每个字段上方的注释给出说明、
类型、可选值和默认值：

| 模板 | 格式 |
| --- | --- |
| [fit/fit.conf](fit/fit.conf) | fit 自己的 [.conf 格式](../docs/reference/fit.zh-CN.md) |
| [yaml/fit.yaml](yaml/fit.yaml) | YAML |
| [json/fit.json](json/fit.json) | 带注释和末尾逗号的 JSON，fit 可以直接读取 |

每份模板中，一个小配置是启用的，其余内容都被注释掉，并写好默认值或示例值，按需取消注释即可。每份都可以直接运行
（`fit run -c templates/fit/fit.conf`）：一个监听 :8080 的 listener、一个 upstream 和一条应答 "hello from fit" 的路由。在被
注释掉的块中，字段说明是注释中的注释（`# #` 或 `// //`），取消注释整个块后它仍是注释。路由部分为每个处理器各列一条路由，
并在一条路由中列出所有中间件模块，各带全部参数。

模板与 [配置参考](../docs/reference/config.zh-CN.md) 一样由代码生成，落后时测试会失败：用 `go test ./schema -update` 重新生成，
或用 `fit schema -template fit`、`yaml` 或 `json` 打印当前二进制所含模块的模板。三种格式的比较见 [配置](../docs/configuration.zh-CN.md)。
