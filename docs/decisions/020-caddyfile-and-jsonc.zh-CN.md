# 020：Caddyfile 转换器，以及带注释的 JSON

[English](020-caddyfile-and-jsonc.md) | [简体中文](020-caddyfile-and-jsonc.zh-CN.md)

状态：已采纳，见 I79（2026-10-23）。其第一点（以 Caddyfile 作为 fit 的块语法）已被
[决定 021](021-fit-format-and-config-files.zh-CN.md) 取代：fit 有了自己的 `.konf` 格式，Caddyfile 作为迁移工具被转换。

## 背景

[决定 019](019-yaml-config.zh-CN.md) 选了 YAML 作为 fit 更友好的格式，并把类似 Caddyfile 的语法留给以后的适配器，待用户
提出时再做。现在用户提出了，同时要求 JSON 配置支持 C/C++ 风格的注释，以及对象和数组最后一个元素后的逗号。

## 决定

- **采用 Caddy 自己的 Caddyfile，而不是 fit 自有的语法。** `adapter/caddyfile` 按 Caddy 的写法读取 Caddyfile，使用 Caddy
  的指令名，把常用子集转换为原生 JSON，与 `adapter/nginx` 转换 nginx 配置一样：已有的 Caddyfile 可以直接用，语法和文档
  是 Caddy 的，fit 的字段只需记录一次。未转换的部分按行列出，从不静默丢弃。没有新依赖：词法和语法分析器是 fit 自己的，
  对 token 数、嵌套和导入都有上限。
- **按覆盖范围生成路由。** Caddy 按指令顺序和匹配器运行一串处理器；fit 只选一条路由。每个处理器成为一条路由，每个中间件
  指令放到其匹配器覆盖的路由上，只覆盖一部分的路由会被拆开，fit 的匹配字段装不下的两者交集写入 `match.expr`。
- **带注释的 JSON 仍是 JSON。** `config.Standardize` 把 `//` 和 `/* */` 注释以及末尾逗号逐字节替换为空白，结果中的每个位置
  在原文里都是同一行同一列。fit 的 JSON 解析器会调用它，保存配置的入口（`fit run` 的文件和 Admin API）也会调用，因此保存
  的是标准 JSON：与 provider 合并、按 JSON pointer 编辑以及 `GET /config` 读回的都是普通 JSON，注释已去掉。

## 后果

- `fit run`、`fit validate` 和 `fit adapt` 接受名为 `Caddyfile` 或以 `.caddyfile` 结尾的文件；适配器除扩展名外也可以注册
  完整的文件名。
- 配置中的注释经过 Admin API 后不保留：`GET /config` 返回的 JSON 中注释已被替换为空白。
- Caddy 的内部 CA、CEL 表达式、`templates`、`map` 和导入文件不转换。
