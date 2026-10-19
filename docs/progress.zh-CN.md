# fit 开发进度

[English](progress.md) | [简体中文](progress.zh-CN.md)

本文件按迭代记录实际完成的内容，以及下一次会话从哪里继续。[开发计划](dev-plan.zh-CN.md) 说明每个迭代应该做
什么；本文件说明实际做了什么，以及与计划不同的地方。

## 当前状态

| | |
| --- | --- |
| 最后更新 | 2026-10-19 |
| 里程碑 | M0 基础：I01–I05 已完成；I06–I08 待做 |
| fib | `github.com/lesismal/fib v1.2.0`，来自模块代理 |
| 可用的功能 | `fit validate` 加载配置、检查并 provision 其中的模块；`fit schema` 输出 JSON Schema；`fit version` |
| 尚不可用 | fit 还不能处理流量：从 I06 开始 |

**下一个迭代：I06，HTTP 入口与 `fit run`。** 它的依赖都已完成。之后的 I07、I08（基准框架）完成 M0。
I06 完成后，I09、I11、I14、I21、I22 也可以开始。

可以用下面的话开始下一次会话：

> 实现 docs/dev-plan.zh-CN.md 中的迭代 I06。先读 docs/progress.zh-CN.md、docs/dev-plan.zh-CN.md 的索引和该迭代的
> 小节，以及 docs/roadmap.zh-CN.md 中的设计原则，然后只读它列出的包。改动限制在该迭代范围内，补上测试，完成后在
> 两个 dev-plan 文件中标记为 done，并记录到两个 progress 文件中。

## 已有的代码

| 包 | 内容 |
| --- | --- |
| `cmd/fit` | 命令行：`validate`、`schema`、`version`、`help` |
| `config` | 原生配置类型；带错误路径和位置的严格 JSON 解析器与解码器；`Value`；语义检查 |
| `module` | 模块注册表；`Handler`、`Middleware`、`Matcher` 接口，以及暂定的 `Service`、`Upstream` 接口；`Provisioner`、`Cleaner` |
| `handler/respond` | `respond` handler |
| `modules/standard` | 引入 fit 自带的所有模块 |
| `runtime` | 把配置 `Compile` 成不可变的 `Graph`；带原子替换和图引用计数的 `Runtime` |
| `schema` | JSON Schema 生成；`fit.schema.json`，由测试保证最新 |
| `internal/fibtest` | 在测试中不经网络运行 fib handler |
| `examples` | `hello.json` |

目前的配置形如：

```json
{
  "listeners": [
    {"name": "web", "address": ":8080", "protocols": ["http/1.1", "h2c"]}
  ],
  "routes": [
    {
      "name": "hello",
      "listeners": ["web"],
      "priority": 10,
      "match": {"host": ["example.com"], "path": ["/hello", "/api/*"]},
      "middleware": [],
      "handler": {"module": "respond", "params": {"status": 200, "body": "hello\n"}}
    }
  ]
}
```

## 迭代记录

### I01 仓库骨架与 CI — 已完成

- `github.com/lesismal/fit` 的 `go.mod`，Go 1.27（与 fib 一致），依赖 fib v1.2.0。
- `cmd/fit` 的 `version` 命令：打印 fit 的版本（可用 `-ldflags "-X main.version=..."` 设置，否则使用 Go 记录的
  版本）、从构建信息读取的 fib 版本，以及 Go 版本和平台。
- `Makefile`：`build`、`test`、`race`、`vet`、`lint`、`fmt`、`fmt-check`、`schema`、`fuzz`，以及 `check`
  （fmt-check、vet、staticcheck、race 测试）。
- `.github/workflows/ci.yml`：gofmt；针对 linux、darwin、windows 的 go vet 和 staticcheck；在 ubuntu 和 macOS
  上开启 race 检测的测试；`FuzzLoad` 运行 30 秒。
- `README.md` 和 `README.zh-CN.md`。

与计划的差异：包在迭代填充时才创建，而不是预先建好，因为 Go 中空包没有意义。CI 还没有运行过：首次推送时运行。

### I02 配置类型 v0 与严格加载 — 已完成

- 类型：`Config`、`Listener`（name、address、protocols）、`Route`（name、listeners、priority、match、
  middleware、handler）、`Match`（host、path）、`ModuleRef`（module、params）。
- fit 自己的解码器，见 [决定 001](decisions/001-strict-config-decoding.zh-CN.md)：未知字段、类型错误、缺少必填
  字段、超出范围的数字、不在枚举中的值、重复的键都是错误，全部报告（不止第一个），并带路径和 行:列。只差大小写的
  字段名会提示 "did you mean"。
- `Config.Validate`：listener 和 route 名称唯一、地址合法（允许端口 0，供测试使用）、两个 listener 不能用同一地址、
  协议不能重复、route 引用的 listener 存在、host 合法（精确、`*.` 通配或 IP）、path 合法（以 `/` 开头，`*` 只能在
  末尾）。
- `Config` 保存原样写入的内容，不填默认值，可以序列化回同样的 JSON；params 保持成员顺序。
- 测试：覆盖每种错误的表驱动测试、往返测试，以及 `FuzzLoad`（运行了四百万个输入，没有失败）。

与计划的差异：route 有 `middleware`（列表）和 `handler`（一个模块），而不是单一的 handler 链；这符合路线图中
router → middleware → handler 的模型，也让每个模块所属的命名空间一目了然。协议目前接受 `http/1.1` 和 `h2c`；
I09 和 I10 会加入 `h2` 和 `h3`。

### I03 JSON Schema 与 `fit validate` — 已完成

- `schema.Generate` 根据配置类型的标签（通过 `config.Fields`，与解码器使用同一份描述）和已注册的模块生成
  draft 2020-12 的 JSON Schema：模块引用的 `module` 是该命名空间中模块的枚举，`params` 遵循对应模块的 schema。
- `schema/fit.schema.json` 已提交；它过期时 `TestSchemaUpToDate` 会失败，`make schema`（或
  `go test ./schema -update`）会重新生成。示例配置已用 Python 的 `jsonschema` 对照它校验通过，一份错误配置中的
  三处错误也都被它拒绝。
- `fit validate -c file` 加载配置后再编译，所以模块参数也会被检查；错误输出为 `file:line:col: path: message`，
  退出码为 1。`fit schema` 输出 schema。

与计划的差异：schema 放在顶层的 `schema` 包，而不是 `config/schema`，因为它需要模块注册表，而模块注册表本身依赖
`config`。

### I04 模块注册表与核心接口 — 已完成

- `module.Register`、`Lookup`、`Namespace`、`Load`、`Cleanup`。ID 带命名空间（`http.handlers.respond`）；配置中
  写最后一段，命名空间由名字出现的位置决定（`handler` → `http.handlers`，`middleware` → `http.middleware`）。
- `Load` 把参数严格解码到模块的结构体，再调用 `Provision`，其上下文的 `Errorf` 可以指向某个参数；普通错误会被
  加上参数的路径和位置。未知模块的错误会列出该命名空间中已有的模块。
- `module.Handler` 就是 fib 的 `http.Handler`，所以可以复用 fib 的 handler 和中间件。`Middleware.Wrap` 在编译时
  调用一次。`Service` 和 `Upstream` 标记为暂定，在 I14 和 I19 中确定。
- `respond`：状态码（默认 200，拒绝 1xx）、头（检查名称和值）、body（在 204 和 304 上拒绝）；响应只构建一次并
  共享，fib 允许这样做。

### I05 运行时图与原子替换 — 已完成

- `runtime.Compile` provision 所有模块，把中间件包在 handler 外面，给每个 listener 分配路由（全部路由，或指名它的
  路由），按优先级排序、同优先级保持书写顺序，并应用默认协议。它报告每一个失败的模块，任何失败时都会清理已
  provision 的模块。
- `Runtime.Current` 无锁：用 CAS 引用当前图，并拒绝计数已归零的图，所以与替换竞争的读者不会让正在清理的图
  "复活"。`Runtime.Swap` 给图编号并释放旧图；最后一次 `Release` 按逆序清理其模块。
- 测试（race 检测下）：8 个读者通过当前图处理请求的同时替换图 300 次；没有读者看到已清理的模块，每张图都恰好
  清理一次。

与计划的差异：没有实现路由匹配；`Route` 原样保存 host 和 path，供 I11 的路由器使用。

## 给后续迭代的说明

- **fib 版本。** fit 使用模块代理上的 fib v1.2.0。本地 fib 仓库的 `main` 比这个 tag 多约 30 个提交。如果 I06 需要
  更新的 fib，应该发布一个 fib 版本，而不是提交 `replace` 指令。
- **staticcheck。** 用较旧 Go 构建的 staticcheck 读不了 Go 1.27 的导出数据。按 CI 的方式（基于最新的
  `golang.org/x/tools`）构建，再传给 make：`make check STATICCHECK=/path/to/staticcheck`。
- **测试 handler。** `internal/fibtest.Serve` 通过 `fibhttp.NewStreamContext` 和一个记录用的 stream 运行 fib
  handler，不经过网络。
- **错误位置。** 错误测试比较带 行:列 的完整消息。期望的列号应该从文档计算，而不是手数。
- **许可证。** 仓库还没有 LICENSE 文件（fib 是 MIT），由维护者决定。
