# 编写模块

[English](modules.md) | [简体中文](modules.zh-CN.md)

模块是为 fit 增加处理器或中间件的 Go 包。它用 `fit build` 编译进二进制文件，就像 xcaddy 把插件编进 Caddy：
对速度要紧的代码，它像 fit 自带的模块一样在 fib 的 worker 上运行，与请求之间没有任何中间层。
[`examples/module/hello`](../examples/module/hello) 是一个模板：一个给每个响应加上字段的中间件，带有自己的
`go.mod`。

## 模块是什么

模块是在 `init` 函数中以带命名空间的 ID 注册的 Go 类型：

```go
func init() {
	module.Register(module.Info{ID: module.Middlewares + ".hello", New: func() any { return new(Hello) }})
}
```

命名空间说明它是什么、必须实现什么：

| 命名空间 | 配置在哪里引用它 | 它实现 |
| --- | --- | --- |
| `http.handlers`（`module.Handlers`） | 路由的 `handler` | `module.Handler`，即 fib 的 `http.Handler` |
| `http.middleware`（`module.Middlewares`） | 路由的 `middleware` | `module.Middleware`：`Wrap(next Handler) Handler`，在编译配置时调用一次 |

配置用 ID 的最后一段（`hello`）引用模块，它在命名空间内必须唯一。导入包时它就会注册，所以二进制文件只需导入
这个包。

## 参数

模块的导出字段就是它的参数，像配置本身一样被严格解码：未知字段或类型错误都会报错，并带上路径和位置。其余的由
struct tag 说明，并成为该模块在 JSON Schema（`fit schema`）中的部分：

| Tag | |
| --- | --- |
| `json:"name,omitempty"` | 参数名 |
| `doc:"..."` | 它的作用，以及用文字说明的默认值 |
| `default:"..."` | 默认值，用 JSON 表示（`"\"world\""`、`"5"`） |
| `min:"0"`、`max:"599"` | 数字的上下界 |
| `enum:"a,b,c"` | 字符串可取的值 |
| `fit:"required"` | 必须设置该参数 |

非导出字段保存模块根据参数构建的内容。

## 生命周期

- **`Provision(ctx *module.Context) error`**（如果有）在加载使用该模块的配置时运行一次：检查解码器无法检查的
  参数（`ctx.Errorf("field", ...)` 指向该参数），并构建服务所需的一切，使请求只需读取。`ctx.Upstream(name)`
  给出配置的上游池；`ctx.Graph()` 给出在处理器返回后仍在进行的请求对配置的持有。
- **`Cleanup()`** 在配置不再被使用、最后一个使用该模块的请求结束后运行一次：用于模块持有的 goroutine、文件、
  池等。
- **`Drain()`** 用于请求在处理器返回后仍在进行的模块：当配置被替换已超过 drain 超时而仍有请求在用时运行，
  结束正在进行的工作。

reload 时，新配置的模块先完成 provision，旧配置的模块才被清理，因此模块可以把它持有的东西交给后继者，缓存的
zone 就是这样做的。

## 规则

模块在 fib 的 worker 上运行，遵守 [fit 的开发规则](rules.zh-CN.md)，首要的一条是：**fib worker 上的任何东西都
不阻塞**。不做同步的网络或磁盘 I/O，不 `time.Sleep`，不等待 channel，也不长时间持锁。

- 在返回之后才回答的处理器调用 `c.Retain()`，回答后调用 `c.Release()`，并通过 `c.OnCancel` 得知客户端离开。
- 只能阻塞的工作在模块自己的有界池上运行，带超时，并在那里回答。
- 请求体用 `c.OnBody` 随到随取，不整体读入；响应用 `c.WriteNoWait` 和 `c.OnWritable` 分段写出，或用
  `c.WriteResponse` 整体写出。
- 每个缓冲区、队列和 map 都有上限，并有安全的默认值。
- 中间件通过 `c.OnHeader`、`c.OnBodyFilter` 和 `c.OnFinish` 修改响应，无论其后的处理器如何写都有效。

## 测试

模块的测试方式与 fit 自带模块相同：处理器可以不经网络，通过 `fibhttp.NewStreamContext(nil, request, stream)` 和
`fibhttp.Serve` 运行，由测试自己的 `fibhttp.Stream` 记录响应；或者用 `runtime.Compile` 和 `listener.Start` 在
回环端口上运行使用它的配置。

## 编译进 fit

```bash
fit build -with github.com/you/fit-hello@v1.0.0
```

会构建 `./fit`，包含 fit 自带的所有模块和 provider，以及 `github.com/you/fit-hello`：生成一个 main 包，导入 fit
的命令行（`cli` 包）、`modules/standard`、`provider/standard` 和该模块的根包，并用 `PATH` 上的 go 命令构建。
参数：

| 参数 | |
| --- | --- |
| `-with path[@version]`、`-with path=dir` | 要编译进来的模块，按版本（省略时为 `latest`）或按 `dir` 中的源码；每个模块一次 |
| `-without pkg` | 去掉 `github.com/lesismal/fit/modules/standard`（所有处理器和中间件）或 `github.com/lesismal/fit/provider/standard`（所有 provider）；引用了被去掉部分的配置会被拒绝 |
| `-fit v`、`-fit dir` | 基于哪个 fit 构建：一个版本，或 fit 源码所在的目录；省略时为正在运行的 fit 自己的版本 |
| `-o file` | 输出的二进制文件；省略时为 `fit` |
| `-keep dir` | 把生成的 main 模块保留在 `dir` 中，以便查看或再次构建 |

`GOOS` 和 `GOARCH` 可以为其他系统构建。构建出的二进制文件的 `fit version` 带有 `+build`，`go version -m fit` 会列出
其中的模块。
