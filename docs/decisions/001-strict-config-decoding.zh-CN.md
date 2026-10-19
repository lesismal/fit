# 001：fit 自己的严格配置解码器

[English](001-strict-config-decoding.md) | [简体中文](001-strict-config-decoding.zh-CN.md)

状态：已采纳，于 I02（2026-10-19）。

## 背景

fit 的配置必须严格：未知字段、类型错误、缺少必填字段、重复的键都是错误，而且每个错误都要说明位置，
既有路径（`routes[0].handler.params.status`），也有行号和列号。模块参数比配置本身晚解码，由模块自己完成，
其错误也需要同样的路径和位置。

`encoding/json` 做不到这些。`DisallowUnknownFields` 遇到第一个问题就停止，而且不说明位置；类型错误只带
不含数组下标的部分路径；重复的键被静默接受，以最后一个为准；用 `json.RawMessage` 保存的模块参数会丢失它在
文档中的位置。`encoding/json/v2` 会有帮助，但 fit 使用的 Go 工具链中没有它。

## 决定

`config` 包有自己的解码器，基于 `encoding/json` 的词法分析器：

- 把文档解析成一棵树，记录每个值的偏移和每个对象成员的顺序，并拒绝重复的键；
- 一个基于反射的解码器按 `json` 标签从树中填充 Go 值，收集所有错误而不是在第一个处停止，并读取额外的标签：
  `fit:"required"`、`fit:"self"`、`enum`、`min`、`max`、`doc` 和 `module`；
- `config.Value` 连同路径保存树的一部分，供模块参数使用，也供后续检查报告位置。

`schema` 包通过 `config.Fields` 读取同样的标签，所以 JSON Schema 和解码器不会不一致。

## 影响

- 需要维护约 700 行解析器、解码器和 Value 代码，由表驱动测试和一个 fuzz 目标覆盖。
- 字段名精确匹配，不同于 `encoding/json` 的大小写不敏感匹配；只差大小写时会提示 "did you mean"。
- 配置和模块参数能使用的类型必须是解码器支持的：结构体、指针、切片、键为字符串的 map、字符串、布尔值、数字和
  `config.Value`。新的值类型（例如时长）要在解码器和 schema 生成器中一起加入。
