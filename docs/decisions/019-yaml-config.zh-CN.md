# 019：以 YAML 作为 fit 更友好的配置格式

[English](019-yaml-config.md) | [简体中文](019-yaml-config.zh-CN.md)

状态：已采纳，见 I73（2026-10-23）。

## 背景

fit 的原生配置是 JSON，严格解码，错误会指出路径和位置。I73 增加一种更便于手写的格式，并用 `fit adapt` 把它转换成
原生 JSON。计划列出了候选：YAML、类似 Caddy 的 Caddyfile 的自有语法，或其他。

## 决定

- **格式是 YAML**，逐键映射到原生 JSON：mapping 是对象，sequence 是数组，标量是其 YAML 1.2 标签所表示的值。它带来
  注释、大多数值无需引号和括号、块字符串，以及用于重复内容的锚点、别名和合并键，这些正是 JSON 手写起来最累的地方；
  配置的名称、结构、默认值、schema 和检查都沿用原生配置，没有新东西要学，也不必写两份文档。
- **不采用自有语法。** 类似 Caddyfile 的格式对小站点读起来不错，但它是第二种语言，有自己的语法、简写和文档，每个
  配置字段都要在其中找到位置并保持同步；它与 JSON 的往返转换会丢失 JSON 有而它没有的东西。如果用户需要，那是以后
  另一个适配器的工作；而用户带来的旧配置，已由 nginx 转换器（I74）负责。
- **解析器是 go.yaml.in/yaml/v3**，即 YAML 组织对 gopkg.in/yaml.v3 的延续，fit 已因 kubeconfig 文件依赖它
  （[决策 011](011-kubernetes-client.zh-CN.md)）：fit 遍历它的节点树而不是解码成 Go 值，这样键保持原有顺序，每个值都
  有行列号，fit 也能限制别名展开的规模。
- **错误指向 YAML。** 适配器记录每个配置路径的值写在哪里，配置的错误（以路径命名）在打印前被移到那里。

## 影响

- `fit run` 和 `fit validate` 像接受 JSON 一样接受 `.yaml` 或 `.yml` 文件，包括重载和 `-watch`；Admin API 仍使用
  JSON，`GET /config` 显示 YAML 文件转换成的原生 JSON。
- YAML 自身的陷阱按 YAML 1.2 处理：`yes` 和 `on` 是字符串，`030` 是整数，`:8080` 这样的值可以不加引号。`fit adapt
  -to yaml` 把原生 JSON 写成 YAML，再转换回来得到同样的 JSON。
