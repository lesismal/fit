# 兼容性

[English](compatibility.md) | [简体中文](compatibility.zh-CN.md)

从 1.0 起，fit 遵守这里的承诺：在所有 1.x 版本中，能加载的配置继续能加载，Admin API 的客户端继续能工作。可能破坏它们的
改动要等到配置 schema 或 Admin API 的新版本，并附带迁移的办法。

## 覆盖范围

| 对象 | 版本 | 内容 |
| --- | --- | --- |
| 原生配置 | schema 版本 1 | [`schema/fit.schema.json`](../schema/fit.schema.json) 中的每个字段，包括 `modules/standard` 中各模块的字段：名字、类型、含义和默认值。YAML 配置（`.yaml`、`.yml`）是同一份配置的另一种写法。 |
| Admin API | v1 | 路径和方法、接收和返回的 body、状态码，以及 `ETag`、`If-Match` 和 `Fit-Config-Version` 字段（见下文）。 |
| 命令行 | 1 | 命令 `run`、`validate`、`adapt`、`schema`、`version`、`hash-password`、`store`（`export` 和 `import`）和 `build`，它们的参数和退出码。 |
| 指标 | 1 | `/metrics` 提供、OTLP 导出的指标的名字、类型和标签，都以 `fit_` 开头。 |
| 访问日志 | 1 | 其 JSON 行的字段。 |
| Go 模块 | Go 的语义化导入版本 | `module` 和 `plugin/build` 包导出的 API，供 `fit build` 构建的模块使用。fit 的其他包可能在任何版本中改变。 |

不覆盖：

- fit 写给人看而不是给程序的内容：消息、警告、仪表盘、控制台以及 llm 和 tunnel 各角色的页面、`fit adapt` 的输出
  （除了它是有效配置这一点），以及错误的文本（错误的 JSON 结构是覆盖的）。
- WASM 插件的宿主函数（`fit` 导入模块）以及 fit 支持的 Proxy-Wasm 子集（I71、I72、I81），它们在 1.0 中是实验性的；`wasm`
  模块的配置是覆盖的。
- nginx 转换器对 nginx 指令的覆盖面，它只会增加。
- 性能，[路线图的目标](roadmap.zh-CN.md#性能目标) 之外的部分。

## 1.x 版本可以做的

- 增加配置字段、模块、provider 和枚举值；新增字段都是可选的，不写时保持上一版本的行为。
- 增加 Admin API 路径、可选的请求字段、响应字段和响应字段的取值。客户端应忽略不认识的字段。
- 增加指标，以及给原本没有该名字标签的指标增加标签。
- 增加命令和参数。
- 收紧默认上限或改变默认值，只为了堵住安全漏洞或无上限的资源；发布说明会写明。
- 重命名字段（见下文）。
- 修正与文档或 fit 声称遵循的 RFC 相矛盾的行为。

1.x 版本拒绝某个配置，原因是它此前就有错而未被发现（两个不能同时出现的字段、超出范围的值），属于缺陷修复；发布说明会逐一列出。

## schema 的版本

配置可以写明它针对的 schema 版本：

```json
{"schema_version": 1, "listeners": [...], "routes": [...]}
```

它是可选的，不写时按版本 1 读取。读取版本 1 的 fit 会拒绝写着 2 的配置，而不是错误地读取它；schema 也写明它是版本 1：
标题为 "fit config, schema version 1"，`schema_version` 只接受 1。

## 重命名的字段

在一个版本内重命名的字段，旧名字保留到下一个版本。使用旧名字的配置照常加载，fit 会指出位置：

```text
fit.json:12:40: routes[0].middleware[0].params.max_size: deprecated: renamed "max_bytes", which takes the same value; the old name goes in config version 2
```

`fit validate` 和 `fit run` 在启动时和每次重载时打印它，给出旧名字在文件中的行和列（YAML 也一样）；Admin API 在加载的
响应的 `warnings` 中返回它。schema 保留旧名字并标为 `deprecated`，编辑器会把它划掉。配置不能同时给出两个名字。

为 1.0 重命名了以下字段，使所有以字节计的大小都以 `_bytes` 结尾，所有周期都以 `_interval` 结尾：

| 模块 | 旧名字 | 名字 |
| --- | --- | --- |
| `cache` | `max_size` | `max_bytes` |
| `cache` | `max_entry_size` | `max_entry_bytes` |
| `cache` | `disk.max_size` | `disk.max_bytes` |
| `compress` | `min_length` | `min_bytes` |
| `jwt` | `jwks_refresh` | `jwks_refresh_interval` |
| `wasm` | `max_memory` | `max_memory_bytes` |

## Admin API v1

| 方法和路径 | 作用 |
| --- | --- |
| `GET /config`、`GET /config/{path...}` | 按原样返回运行中的配置，或某个路径处的部分 |
| `/config` 和 `/config/{path...}` 上的 `PUT`、`PATCH`、`DELETE` | 替换、merge patch（RFC 7396）或删除整个配置或某个路径处的部分，并应用结果 |
| `POST /load` | 应用一份完整配置 |
| `GET /config/effective` | 与 provider 提供的内容合并后的配置，以及每部分的来源 |
| `GET /versions`、`GET /versions/{n}` | 已应用并保留的配置，以及其中一份 |
| `POST /rollback/{n}` | 把保留的配置作为新版本再次应用 |
| `GET /status` | listener、连接、上游及其健康状态、证书 |
| `GET /metrics` | Prometheus 文本格式的指标 |
| `GET /cache`、`POST /cache/purge` | 缓存的 zone，以及按 URL、标签或 zone 清除 |
| `GET /tokens`，`/tokens/{role}` 上的 `GET`、`POST`，`/tokens/{role}/{id}` 上的 `PATCH`、`DELETE` | 隧道 hub 与 LLM 各角色的监听签发的连接令牌：列出、签发、修改过期时间、吊销 |
| `GET /media/library`、`GET /media/library/{name}`、`GET /media/library/{name}/items/{id}`、`POST /media/library/{name}/rescan` | 媒体库：其扫描、一个媒体库的一页条目、一个条目，以及重新扫描 |
| `POST /media/preview`、`GET /media/auth`、`GET /media/auth/{policy}/sessions`、`POST /media/auth/{policy}/sessions/end`、`POST /media/auth/{policy}/rotate`、`POST /media/auth/{policy}/urls`、`GET /media/stats` | 工作台的媒体页面：预览的短时地址，media_auth 策略及其密钥和播放会话，结束会话，轮换密钥，测试地址，以及媒体库、vod 处理器、策略和频道的统计 |
| `GET /streaming/channels`，`/streaming/channels/{name}` 上的 `GET`、`POST`，`GET /streaming/push`，`POST /streaming/push/{name}` | 流媒体角色的频道与推流目标：各自在播放或做什么，以及启动、停止、跳过或重启 |
| `GET /dashboard/` | 给人看的仪表盘（不覆盖） |

配置中的路径是 JSON pointer 的各段。修改的响应在 body（`version`）和 `Fit-Config-Version` 中给出应用的版本号，并给出配置的
`ETag`；带 `If-Match` 的修改只应用于该 ETag 所指的配置，否则回答 412。无法加载的配置回答 400，body 为
`{"error": ..., "errors": [{"path", "line", "column", "message"}]}`。

如果需要 Admin API v2，它会在 `/v2/` 下提供，与 v1 的路径并存，v1 的路径在 fit 的整个主版本内保留。

## 新版本

无法让旧配置或旧客户端继续工作的改动，要等到新的 schema 或 Admin API 版本，并附带：

- 每项改动及其原因的清单；
- 对配置：`fit adapt` 把上一版本的配置改写为新版本，新的 fit 至少在一个版本内仍读取上一版本的配置并给出警告；
- 对 Admin API：上一版本与新版本并存，直到该主版本结束。
