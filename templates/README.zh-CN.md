# 配置模板

[English](README.md) | [简体中文](README.zh-CN.md)

fit 三种配置格式各有一份完整的配置模板，可复制后修改：

| 模板 | 内容 |
| --- | --- |
| [caddyfile/Caddyfile](caddyfile/Caddyfile) | fit 能从 Caddyfile 转换的全部全局选项、站点地址形式、匹配器、指令和子指令，每项的可选值写在注释中。 |
| [yaml/fit.yaml](yaml/fit.yaml) | 配置和每个模块参数的全部字段，每个字段上方的注释给出说明、类型、可选值和默认值。 |
| [json/fit.json](json/fit.json) | 与 YAML 模板相同，格式为带注释和末尾逗号的 JSON，fit 可以直接读取。 |

每份模板中，一个小配置是启用的，其余内容都被注释掉，并写好默认值或示例值，按需取消注释即可：

- YAML 和 JSON 模板可以直接运行（`fit run -c templates/json/fit.json`）：一个监听 :8080 的 listener、一个 upstream 和
  一条应答 "hello from fit" 的路由。每个字段的说明在它上方；在被注释掉的块中，字段说明是注释中的注释（`// //` 或 `# #`），
  取消注释整个块后它仍是注释。路由部分为每个处理器各列一条路由，并在一条路由中列出所有中间件模块，各带全部参数。
- Caddyfile 模板可以直接转换（`fit adapt -c templates/caddyfile/Caddyfile`）：一个使用了全部指令的站点，以及被注释掉的
  HTTPS、按需 TLS 和双向 TLS 站点。运行前请把其中的目录、upstream 和 PHP socket 改成本机实际的。以 `## ` 开头的行是说明，
  以 `# ` 开头的行是代码。

Caddyfile 无法表达的内容（四层服务、缓存、限流、JWT、WASM、provider、telemetry 和 fit 的 limits）只在 YAML 和 JSON
模板中。

YAML 和 JSON 模板与 [配置参考](../docs/reference/config.zh-CN.md) 一样由代码生成，落后时测试会失败：用
`go test ./schema -update` 重新生成，或用 `fit schema -template yaml`、`fit schema -template json` 打印当前二进制所含模块的模板。
三种格式的比较见 [配置](../docs/configuration.zh-CN.md)。
