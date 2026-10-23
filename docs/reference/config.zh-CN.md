# 配置参考

[English](config.md) | [简体中文](config.zh-CN.md)

配置参考由 `schema.Reference` 从 fit 的配置类型和模块直接生成，与解码器和 JSON Schema 读取的是同一组类型和标签，因而不会与
fit 实际接受的配置脱节。字段的说明写在代码的 `doc` 标签里，是英文的，所以参考只有 [英文版](config.md)，不另行翻译。

参考中的每一节是一个对象：

- **The config** 是配置的顶层：`listeners`、`routes`、`upstreams`、`services` 等，其后是它们各自的对象，标题是它们在配置中
  的路径，如 `listeners[].tls`。
- **Handlers** 和 **Middleware** 列出 `modules/standard` 中的每个模块：路由的 `handler` 或 `middleware` 用 `module` 指定其
  名字，用 `params` 给出参数。

每张表的列是：字段名、类型（`duration` 是如 `"1.5s"`、`"2m"` 的字符串；整数会注明范围；`object` 链接到它自己的一节）、
默认值（不写时取的值），以及说明。标为 `Required` 的字段必须写出。标为 `Deprecated` 的是重命名前的旧名字，仍可使用，
但会得到警告，见 [兼容性](../compatibility.zh-CN.md)。

用 `go test ./schema -update` 重新生成；内容过期或有字段缺少说明时，`schema` 包的测试会失败。`fit schema -reference` 打印
当前二进制（包括 `fit build` 编入的模块）的参考。
