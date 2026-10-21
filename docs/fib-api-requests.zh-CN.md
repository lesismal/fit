# fit 需要 fib 提供的 API

[English](fib-api-requests.md) | [简体中文](fib-api-requests.zh-CN.md)

按 [开发规则](rules.zh-CN.md) 审查 M0、M1 时发现，有些地方只靠 fib v1.3.0 现有的 API，fit 无法遵守规则。本文列出 fit
需要 fib 提供的 API、原因，以及 fit 打算怎么用。文中的名字和签名只是建议，最终形式由 fib 决定。自 R07e 起，fit 的开发会话可以自己在 fib 中实现（[规则 15](rules.zh-CN.md#七fib-满足不了时)）。

依赖这些 API 的 fit 迭代是 [开发计划](dev-plan.zh-CN.md#m1-审查修复) 中的 R03–R06 和 R07c。

## 概览

| ID | API | 状态 | 解决的规则 | fit 迭代 |
| --- | --- | --- | --- | --- |
| A1 | `ClientResponse.HoldBody(hold bool)` | 已完成，v1.4.0 | 1、8 | R04 |
| A2 | 从不等待的响应写入，以及可写通知 | 已完成，v1.4.0 | 1、8、9 | R04、R05 |
| A3 | 异步发送文件 | 已完成，v1.4.0 | 1、4 | R05 |
| A4 | 引擎连接数上限 | 已完成，v1.4.0 | 11 | R06 |
| A5 | 客户端等待连接的请求数上限 | 已完成，v1.4.0 | 11 | R06 |
| A6 | 在 `BodyWriter` 关闭前设置 trailer | 已完成，v1.4.0 | 1、4 | R04 |
| A7 | `Context` 上的单请求值槽 | 已完成，v1.4.0 | 12 | R07c |
| A8 | 文档修正 | 已完成，v1.4.0（其余不再需要） | — | — |
| A9 | 拒绝不是 token 的请求头字段名 | 已完成，v1.4.0 | 13 | — |
| A10 | 长度未知的客户端响应从响应头起就以流方式交付 | 已完成，v1.3.1 | 5 | — |
| A11 | 不复制即取得整体到达的客户端响应体 | 已完成，v1.4.0 | 12 | R07c |
| A12 | 减少每个客户端请求的定时器和 context | 已完成，v1.5.0 | 12 | R07c |
| A13 | 只有 `OnFinish` 钩子的响应不复制响应头 | 已完成，v1.4.0 | 12 | R07c |
| A14 | 在没有连接的 Context 上 `Release` | 已完成，v1.5.1 | 13 | R05 |
| A15 | `MaxPendingPerHost` 不计入正在为其拨号的请求 | 已完成，v1.5.1 | 11 | R06 |
| A16 | `SendFile` 排队的文件计入连接未发送的输出 | 已完成，v1.5.4 | 10、11 | R05、R07e |
| A17 | 降低每个请求上 `OnFinish` 和 `SetValue` 的开销 | 已完成，v1.5.4 | 12 | R07d、R07e |
| A18 | 不经回调取得响应的状态和请求到达的时间 | 部分完成，v1.5.4 | 12 | I34、R07e |
| A19 | 流式请求的 `Trailer` 在交出请求体结尾的地方设置 | 已完成，v1.5.3 | 13 | I35 |
| A20 | 不经 net/http 写客户端请求、读响应头 | 已完成，v1.5.6 | 12 | R07f |
| A21 | 事件循环每次休眠只调用一次 `epoll_ctl`，而不是两次 | 未完成：`EPOLLONESHOT` 省不掉这次调用 | 12 | R07f |
| A22 | 客户端连接的空闲计时不必每个请求重置定时器 | 已完成，`7f632cb`（v1.5.6 之后的 main） | 12 | R07f |
| A23 | `Engine.Close` 切断的连接也运行 `OnClose` | 已完成，v1.5.6 | 11、13 | — |
| A24 | taskpool 新建的环不写任何单元 | 已完成，`a6d78cd`（v1.5.6 之后的 main） | 11 | — |

## 证据

fit 代理的下载方向在 `ClientResponse.OnBody` 的回调里调用 `Context.Write`（`handler/proxy/exchange.go` 的
`exchange.onBody`）。对 HTTP/1 和 HTTP/2 上游，这个回调运行在读取上游连接的 worker 上；客户端排队超过 64 KB 后，
`Write` 就会等待。fit 没有设置 `Config.WriteTimeout`，其默认值为 0，所以这个等待没有时间上限。
在 fit `511f64b`、fib v1.3.0 上的实测结果：

| 场景 | 请求 200 MB 下载但从不读取的客户端 | 发往同一上游的其他请求 |
| --- | --- | --- |
| h2c 上游，`max_conns: 1` | 1 个 | 超时（5 秒），7 秒后仍然超时；只有卡住的客户端断开后才恢复 |
| HTTP/1.1 上游，`max_conns: 4` | 8 个 | 超时（5 秒），7 秒后仍然超时；只有它们断开后才恢复 |
| HTTP/1.1 上游，不设 `max_conns` | 512 个 | 能正常处理（fib 的自适应池会增加 worker），代价是每个卡住的客户端占用一个阻塞的 goroutine 和一条上游连接，没有上限 |

`response_idle_timeout`（这些测试里设为 2 秒）不起作用：它取消了上游请求，但 goroutine 仍停在 `Write` 里。

## A1：`ClientResponse.HoldBody(hold bool)`

**状态。** 已在 fib v1.4.0 中完成（`b520601`）：按请求提供 `ClientResponse.HoldBody(hold bool)`，支持 HTTP/1、HTTP/2 和 HTTP/3。恢复交付在 fib 的 side pool 上进行。暂停期间响应体的结束也会等待，但错误会立即交付；整体到达的响应在它唯一的一次调用之前暂停。

**问题。** 客户端响应体只能靠 `OnBody` 回调阻塞来控速（"what holds the connection back is fn itself"）。在 HTTP/2 上，
回调运行在读取共享连接的 worker 上，一个慢消费者会卡住这条连接上的所有 stream。

**需求。** 与 `Context.HoldBody` 对称的客户端响应版本：

```go
// HoldBody 在 hold 为 true 时暂停向 OnBody 回调交付 body，为 false 时恢复。暂停期间到达的数据留在客户端缓冲中：
// HTTP/1 在 StreamResponseBodyBuffer 满后停止读取连接；HTTP/2 和 HTTP/3 只停止归还这个 stream 的窗口，同一连接上的
// 其他 stream 照常进行。恢复时在引擎的 handler 池上交付，绝不在调用者的 goroutine 上，所以回调内外都可以调用。
func (c *ClientResponse) HoldBody(hold bool)
```

**fit 的用法。** 在 `onBody` 中，写入后若客户端队列超过上限：先 `resp.HoldBody(true)`，等 A2 的通知再恢复。

## A2：从不等待的响应写入，以及可写通知

**状态。** 已在 fib v1.4.0 中完成（`24da9bb`）：按请求提供 `Context.WriteNoWait(p) (above bool, err error)` 和 `Context.OnWritable(fn func(error)) bool`，同一时间只有一个 fn。HTTP/1 用 `Connection.NotifyOutput`，HTTP/2 用流窗口，HTTP/3 通过新的 `http.ResponseNotifier` 接口；没有实现它的外部 stream 退回到 `Write`。响应开始流式发送之前 `OnWritable` 返回 false，因为小的写入会被暂存，不会超过水位。

**问题。** 排队超过 64 KB（HTTP/1）或被流控挡住（HTTP/2、HTTP/3）时，`Context.Write` 会等待。没有办法不等待地写入，
并在有空间时得到通知。`Connection.NotifyOutput` 只适用于 HTTP/1 连接，不适用于 HTTP/2、HTTP/3 的 stream。

**需求。**

```go
// WriteNoWait 像 Write 一样把 p 加入响应体，但从不等待：它接收全部 p，并在当前排队或被流控挡住的数据超过响应上限
// （64 KB）时返回 above=true。收到 above 的调用者应停止写入，直到 OnWritable 的回调运行。
func (c *Context) WriteNoWait(p []byte) (above bool, err error)

// OnWritable 安排 fn 在另一个 goroutine 上运行一次：当响应排队的输出降到上限的一半时；或者连接、stream 结束，或写截止
// 时间已过时，带上错误运行。如果输出已经不超过一半，它返回 false，fn 不会运行。
func (c *Context) OnWritable(fn func(error)) bool
```

**fit 的用法。** 与 A1 配合：

```go
above, err := c.WriteNoWait(data)
if err != nil { /* 客户端已断开：resp.Close()，释放 */ }
if above {
    resp.HoldBody(true)
    if !c.OnWritable(func(err error) { if err == nil { resp.HoldBody(false) } else { /* 中止 */ } }) {
        resp.HoldBody(false)
    }
}
```

这与上传方向的写法一致：上传方向已经在用 `Context.HoldBody` 和 `BodyWriter.OnDrain`。M3 的 WebSocket、gRPC 和隧道
转发也需要同样的一对 API。

## A3：异步发送文件

**状态。** 已在 fib v1.4.0 中完成（`ecd62b0`），采用第一种方案：`Context.SendFile(f *os.File, offset, n int64, done func(error))`。普通 HTTP/1 上 16 KB 及以上用 `Connection.SendFile`；TLS、HTTP/2 和 HTTP/3 在 fib 的文件池（`sidepool.File()`，256 个 worker）上按 64 KB 读取，用 `WriteNoWait` 和 `OnWritable` 写出。`done` 在另一个 goroutine 上运行，在此之前响应保持 retain。未设置时它会补上 `Content-Length`；206 和 `Content-Range` 由 handler 自己设置。没有接入 `FileCache`。

**问题。** fit 的静态文件 handler 用 `http.ServeContent` 发送文件：它在 worker 上读文件，再通过 `Context.Write` 或
`ReadFrom`（sendfile）写出，两者都会等待客户端。磁盘读取和这些等待都会阻塞 worker。

**需求。** 二选一：

- `Context.SendFile(f *os.File, offset, n int64, done func(error))`：不等待地发送文件的一段。HTTP/1 上用 sendfile，
  由 A2 的通知控速；HTTP/2、HTTP/3 上在有界的文件 I/O 池中分块读取，再用 A2 发送；或者
- `http.FileCache` 提供一个方法，通过 A2 发送已缓存的文件或其中一段，未命中时在有界池中读取。

**fit 的用法。** 静态 handler 从 `FileCache` 提供文件，大文件用这个 API 发送，不在 worker 上调用任何阻塞的文件操作。

## A4：引擎连接数上限

**状态。** 已在 fib v1.4.0 中完成（`eb0fe89`）：`fib.Config.MaxConns int`，作用于整个引擎（不能按 binding 设置），0 表示不限制。超过上限接受的连接会在 `OnOpen` 之前立即关闭，计入 `Stats.ConnsRefused`；`Stats.OpenConns` 是当前连接数。UDP 对端和主动拨出的连接不计入。

**问题。** `fib.Config` 没有限制打开的连接数。规则 11 要求有上限，并且有安全的默认值。

**需求。** `Config.MaxConns int`（可选再加上每个 `Binding` 的上限）：超过时立即关闭新接受的连接（或暂停 accept），
并计入统计。为 0 时保持现在的行为。

## A5：客户端等待连接的请求数上限

**状态。** 已在 fib v1.4.0 中完成（`7179521`）：按请求提供 `ClientConfig.MaxPendingPerHost int` 和 `ErrTooManyPending`，只在 `http` 中（`http3` 没有按主机的上限）。重试的请求不受它限制。

**问题。** 达到 `MaxConnsPerHost` 后，请求排队等待连接，排队数量没有上限，只有各自的 context 限制等待时长。

**需求。** `ClientConfig.MaxPendingPerHost int`：超出的请求立即失败，返回专门的错误（如 `ErrTooManyPending`），
fit 把它映射为 503。

## A6：在 `BodyWriter` 关闭前设置 trailer

**状态。** 已在 fib v1.4.0 中完成（`b51db73`），范围比请求的窄：`Close` 本来就会按 `Close` 时的样子发送 `req.Trailer`，所以在此之前设置的值会发出，前提是这些名字在 `Do` 之前已经在 `req.Trailer` 中。`BodyWriter.CloseWithTrailer(trailer http.Header) error` 让不持有请求的生产者也能交出这些值。HTTP/1 只在分块响应体上发送 trailer。

**问题。** fib 的客户端在发送请求头时读取请求的 `Trailer`。如果 trailer 的值要等 body 结束才知道（带 trailer 的
客户端上传），就无法使用 `BodyWriter`。fit 只好在客户端自己的 goroutine 上用阻塞的管道读取，每个这样的上传占用一个
goroutine。

**需求。** `BodyWriter.CloseWithTrailer(trailer http.Header) error`，或者客户端在 body 读到 EOF 之后再读取请求的
`Trailer`，而不是在发送请求头时读取。

## A7：`Context` 上的单请求值槽

**状态。** 已在 fib v1.4.0 中完成（`21eeb62`、`dbb6ef2`）：`Context.SetValue(v any)` 和 `Context.Value() any`，Context 回收时清空。这个槽和 Context 的取消钩子放在一起，所以在新 Context 上第一次 `SetValue` 会分配它们（约 48 字节）；回收复用的 Context 会保留它们。

**问题。** fit 用 `r.WithContext` 把访问日志条目挂到请求上，每个请求都要复制整个 `http.Request` 并分配一个
context 值。

**需求。** `Context.SetValue(v any)` 和 `Context.Value() any`（一个槽，`Context` 回收时清空），或者一个生命周期相同
的小型键值表。

## A8：文档修正

**状态。** 已在 fib v1.4.0 中完成（`75e7005`）：`StreamResponseBody` 和 `OnBody` 的文档说明了会等待的 `OnBody` 函数会拖住连接上的其他流。没有增加 `WriteStallTimeout`，也不再需要：有了 `WriteNoWait`（A2），fit 的代理不再等待客户端，静态文件处理的等待由 `SendFileStall`（A16）限定。2026-10-21 复查：没有剩下要提的。

- `StreamResponseBody` 的文档说停滞的读者"never holds up the other streams on the connection"。这对读取 `Body` 的读者
  成立，但对会等待的 `OnBody` 回调不成立，因为回调运行在连接的读取者上。建议写明。
- `Config.WriteTimeout` 默认为 0，所以流式 `Write` 遇到不再读取的客户端时会永远等待。建议增加按单次等待计的上限
  （`WriteStallTimeout`），默认非零，与针对整个响应的 `WriteTimeout` 分开，因为长时间下载无法使用后者。

## A9：拒绝不是 token 的请求头字段名

**状态。** 已在 fib v1.4.0 中完成（`25064b3`），即 `f02a0bc` 的补丁变基到 `main` 上。

**问题。** fib v1.3.0 的 HTTP/1 服务器把 `X-A : one` 当作名为 `X-A `（带空格）的字段接受，而 RFC 9112 第 5.1 节要求
服务器以 400 拒绝字段名和冒号之间的空白：下游服务器可能把它读成另一个字段，这是一种走私手段。快速解析器
（`simpleField`）会拒绝这样的名字，并把请求头交给 net/http 的 `ReadRequest`，后者原样保留；net/http 自己的服务器在读取
后会检查字段名，fib 的不会。冒号前的制表符已经会被拒绝。`Host : x` 之所以被拒绝，只是因为请求因此没有 `Host`。

**证据。** I23 的代理一致性测试（`test/e2e/conformance_test.go`），以及手写给 fib handler 的请求：`X-A : one` 到达时是
`[X-A ]=[one]`。

**建议的修改。** 在 HTTP/1 服务器中，当 `parseRequestHead` 回退到 net/http 的读取器之后，像 net/http 的服务器一样，以
`ErrMalformed`（即 400）拒绝字段名不是 token 的请求；快速路径本来就只接受 token。`parseRequestHead` 本身可以继续与
`ReadRequest` 保持一致，如其测试所要求的。实现这一修改的补丁，连同针对其他字段冒号前的空格、制表符和名字中间空格的一致性
测试用例，在 fib 本地仓库的 `fix-field-name-whitespace` 分支上（提交 `f02a0bc`，未推送），供维护者采用或重写。

**fit 如何使用。** 不需要新的 API：这样的请求不会到达 handler。在此之前，fit 的代理以 400 拒绝字段名不是 token 的请求
（I23，在复制请求头的循环中顺带检查），这样的字段不会到达上游；其他 handler 不转发请求头。fib 拒绝之后，这个检查作为纵深
防御保留。

## A10：长度未知的客户端响应从响应头起就以流方式交付

**状态。** 已在 fib v1.3.1 中完成（`http` 和 `http3` 的 `ClientConfig.StreamUnknownLengthAtHeader`）；fit 从此使用它。

**问题。** `ClientConfig.StreamResponseBodyThreshold` 同时做两件事。对带 `Content-Length` 的响应体，它决定响应体是整体
到达（不超过阈值时）还是以流方式到达；对没有声明长度的响应体（分块、以服务器关闭连接结束，或没有声明长度的 HTTP/2
响应体），它是回调运行前必须先到达的数据量。fit 把它设为 64 KB，让小响应整体到达、一次写给客户端；但这样一来，像
server-sent events 和长轮询这样缓慢涓流的响应，要等到达 64 KB、流结束，或被 fit 的 `response_idle_timeout`（60s）切断时，
才会到达 fit 的回调，进而到达客户端。阈值设为 0 能让这类响应立即以流方式交付，但同时也让每个带长度的响应体都以流方式
交付，无论多小：回调运行时它们都还不完整，因此都要走流式路径，响应头一次写、响应体再写若干次，而不是一次写完。

**证据。** `handler/proxy/sse_test.go`（在此之前跳过）：后端先发送 `data: 0`，再间隔 200 ms 发送两个事件，经 fit 的代理，
上游分别为 HTTP/1.1、h2c 和 h2。在 fib v1.3.0、fit 阈值为 64 KB 时，第一个事件要 606–617 ms 后、流结束时才到达客户端；
不结束的流在 60 s 内什么都显示不出来。阈值为 0 时它立即到达，但经 `upstream.Pool` 的 20 个带 `Content-Length` 的小响应中，
回调运行时没有一个是完整的，而阈值为 64 KB 时 20 个都完整。

**请求。** 一个设置：让长度未知的响应体在响应头到达时就运行回调（带上随响应头一起到达的那部分响应体），带长度的响应体
仍按阈值处理：

```go
// StreamUnknownLengthAtHeader runs the callback of a response whose body
// has no declared length as soon as its header has arrived, rather than
// once StreamResponseBodyThreshold of the body has, so that a body that
// trickles is passed on as it comes. Bodies with a Content-Length are kept
// whole or streamed by StreamResponseBodyThreshold as before. It has no
// effect without StreamResponseBody, and applies to HTTP/1 and HTTP/2.
StreamUnknownLengthAtHeader bool
```

为 `http.ClientConfig` 和 `http3.ClientConfig` 增加 `StreamUnknownLengthAtHeader` 的补丁，连同 HTTP/1、HTTP/2 和
HTTP/3 的测试，在 fib 本地仓库的 `stream-unknown-length` 分支上（提交 `5fd41c1`，基于 `main`），已合并并发布为 v1.3.1。fib 的测试在
race 检测下通过；用该分支替换 fib v1.3.0 并打开这个设置后，fit 的 `sse_test.go` 通过，其他测试也都通过。

另一种也能让小的分块响应体保持整体的做法是一个等待时间 `StreamResponseBodyMaxWait time.Duration`：阈值的数据量到达、
响应体结束、或自响应头起等待时间已过，三者先到者触发回调。

**fit 如何使用。** `upstream.New` 在 `StreamResponseBody` 旁边设置它；代理的流式路径本来就会把每一段在到达时写出，并在
后面没有紧接着的数据时刷新，所以事件会按上游发送的节奏到达客户端。届时 `sse_test.go` 改为运行而不是跳过。在此之前没有
能两者兼顾的临时办法：fit 保持 64 KB 的阈值。

## A11：不复制即取得整体到达的客户端响应体

**状态。** 已在 fib v1.4.0 中完成（`3661093`）：`ClientResponse.BodyBytes() ([]byte, bool)`，返回 `Body` 还未读取的部分，以流方式到达的响应返回 false；fib 不会复用这些字节。HTTP/3 也支持。

**问题。** 整体到达的响应把响应体放在 `bufferedBody` 里，`Body` 从中读取。代理用 `Context.WriteResponse` 转发这样的
响应体，而它的 `Body` 是 `[]byte`，所以代理要先把响应体读进自己的切片：每个小的代理响应都多一次分配和一次复制。`OnBody`
交付的是同样的字节且不复制，但它们只在调用期间有效，而 `WriteResponse` 会一直读 `Body` 直到发出，在 HTTP/2 上这发生在
它返回之后。

**证据。** `BenchmarkProxy`（`handler/proxy/bench_test.go`）：R07b 之后这次读取是一次与响应体等长的分配（`readBody`），
约占 fit 每个代理请求 12 次分配中的 1 次。

**请求。** `ClientResponse.BodyBytes() ([]byte, bool)`：返回整体到达的响应的响应体，只要响应还被引用就一直有效；对以流
方式到达的响应返回 false。或者让 `Response.Body` 直接取用 `NewBufferedBody` 收到的字节，条件相同。

**fit 如何使用。** `exchange.onResponse` 把这些字节直接交给 `WriteResponse`，不再读取。

## A12：减少每个客户端请求的定时器和 context

**状态。** 已在 fib v1.5.0 中完成（`b580ea7`、`f4afaa6`、`c58eb4b`、`783e89c`、`2f68795`）：连接的截止时间定时器和客户端的空闲定时器改为重置而不是新建；`ClientConfig.ResponseHeaderTimeout` 超时时以 `ErrResponseHeaderTimeout`（与 `os.ErrDeadlineExceeded` 匹配）失败，与 `Timeout` 共用请求的同一个定时器，为重试重新排队的请求会重新计时。它是配置项而不是按请求设置，这对 fit 正合适，因为每个上游池有自己的客户端。`Client.Start(req, cb) Call` 和 `Call.Cancel(err)` 无需可取消的 context 即可取消请求：每个请求 45 次分配，而用可取消 context 的 `Do` 是 48 次。

**问题。** 每个经 `Client.Do` 的请求都会创建若干本可复用或省去的定时器和 context；代理还要为响应头超时再加一个自己的
定时器，因为客户端没有这个设置。

**证据。** `BenchmarkProxy`（HTTP/1.1 上游、keep-alive、fib v1.3.1）的内存 profile，每个请求：四个 `time.AfterFunc`
定时器，每个都是一个定时器加一个闭包：`Client.do` 为 `ClientConfig.Timeout` 创建的（`client.go:284`）；`Client.release`
的空闲定时器，每次连接回到池中都新建一个（`client.go:716` 和 `774`），而不是重置；连接的读截止时间
（`Connection.armDeadlineLocked`）；以及代理的 `response_header_timeout`。此外还有请求 context 上的 `context.AfterFunc`
（`client.go:287`，连同 `propagateCancel` 共四次分配），以及客户端里的一个 `context.WithCancel`。整个交换（包括 net/http
后端）每个请求 81 次分配。

**请求。**

- 空闲定时器随连接保存，连接空闲时 `Reset`、被取用时停止，而不是每次新建。
- 连接的截止时间定时器在多次设置之间复用。
- `ClientConfig.ResponseHeaderTimeout`（以及按请求设置的方式，因为 fit 的每个上游各有自己的值）：从请求发完到响应头到达
  的时间；超时时请求以一个 fit 能与 `Timeout` 区分开的错误失败。
- 如果几个超时能共用每个请求一个定时器（或每个 worker 一个时间轮），更好。

**fit 如何使用。** `upstream.New` 用 `response_header_timeout` 设置 `ResponseHeaderTimeout`，代理去掉自己的这个定时器；
空闲超时仍是代理自己的定时器，随响应体到达而重置。

## A13：只有 `OnFinish` 钩子的响应不复制响应头

**状态。** 已在 fib v1.4.0 中完成（`67bc31d`），按请求实现：只有 `OnHeader` 和 `OnResponse` 钩子才会让 `WriteResponse`、`Upgrade` 和 `Connect` 复制响应头。没有 API 变化。

**问题。** 只要注册了任何钩子，`Context.WriteResponse` 就会复制响应头（`handler.go` 的 `WriteResponse`：
`c.hooked() != nil`），因为钩子可能修改它。但 `OnFinish` 钩子不能修改：它拿到的响应头“must not be changed”。fit 的访问日志
在每个请求上都注册一个这样的钩子，所以打开日志后，每个整体写出的响应（`respond` 和代理的小响应都是）都要为一次没人需要的
响应头复制付出代价。

**证据。** `BenchmarkDispatch`（`listener/dispatch_test.go`，打开访问日志的 `respond` 路由）：`net/http.Header.Clone` 占每
请求 12 次分配中的 3 次。压测主机上，fit 的固定响应在 h2c 上不开访问日志时每秒 378 万请求，开日志（写到 `/dev/null`）时
228 万；HTTP/1.1 上分别是 78.3 万和 75.2 万。

**请求。** 只有注册了 `OnHeader` 或 `OnResponse` 钩子时才复制响应头：在 `whole` 旁边加一个由这两者设置的标志，
`WriteResponse` 检查它而不是 `hooked() != nil`。

**fit 如何使用。** fit 不需要改动；记日志的响应不再为这次复制付出代价。

## A14：在没有连接的 Context 上 `Release`

**状态。** 已在 fib v1.5.1 中完成（`c9dd792`），按请求实现：`c.Conn` 为 nil 时最后一次 `Release` 跳过 flush，`NewStreamContext` 的文档说明允许 nil 连接。

**问题。** `fibhttp.NewStreamContext(nil, req, stream)` 为 fib 之外服务的流创建一个没有 `fib.Connection` 的 Context；
fit 的 `internal/fibtest` 用它在测试中不经网络运行 handler。`fibhttp.Serve` 在它上面运行 handler，在 handler 自己的
goroutine 上应答没有问题。但 retain 请求、之后从另一个 goroutine 应答的 handler，在最后一次 `Release` 时会 panic：
这条路径上 `Context.giveBack` 在 `Finish` 之后调用 `c.Conn.Flush()`，而 `Conn` 是 nil。

**证据。** 在 fib v1.4.0 上，一个 handler 调用 `c.Retain()`，然后在 goroutine 中调用 `c.WriteResponse(...)` 和
`c.Release()`，经 `fibhttp.Serve` 在 `NewStreamContext(nil, ...)` 上运行：在 `(*Connection).Flush` 中空指针解引用，调用
来自 `(*Context).giveBack`（`http/context.go:895`）。R05 把静态 handler 的文件操作移到一个从自己的 goroutine 应答的池上，
所以静态 handler 的每个测试都会走这条路径。

**请求。** `c.Conn` 为 nil 时跳过 flush（外部的流会自己发送交给它的内容），并在 `NewStreamContext` 的文档中说明允许
nil 连接。

**fit 如何使用。** `fibtest.ServeRequest` 改为经 `fibhttp.Serve` 运行，并等待被 retain 的响应写出，这样静态 handler 的测试
不用改动就能覆盖它的异步路径。

## A15：`MaxPendingPerHost` 不计入正在为其拨号的请求

**状态。** 已在 fib v1.5.1 中完成（`f627cd4`）：等待的请求数减去正在进行的拨号和 `MaxConnsPerHost` 剩余空间所覆盖的请求后达到上限时，才拒绝请求。对可能是 HTTP/2 的主机，剩余空间按它会变成的拨号数计算。拨号挂住时的上限是 `MaxConnsPerHost + MaxPendingPerHost`。没有设置 `MaxConnsPerHost` 时这个上限不再生效；fit 总会设置它。

**问题。** `ClientConfig.MaxPendingPerHost` 计算主机队列中的所有请求，包括那些在等待 `MaxConnsPerHost` 允许、客户端
正在为它们拨号的连接的请求。于是一批发往没有空闲连接的主机的新请求，在允许的连接还在建立时，超过
`MaxPendingPerHost` 的部分就被拒绝。fit 的 `max_pending` 本意同 nginx 的 `queue`：`max_conns` 都忙之后等待的请求。
R02 在 fit 中就是这样计数的，`upstream.TestDefaultMaxConns` 也是这样测试的。

**证据。** 在 fib v1.4.0 上，一个 `max_conns` 为 256（默认值）、`max_pending` 为 8 的上游，同时发出 256 个请求到一个
会挂住请求的后端：只建立了 179 个连接，其余请求立即得到 `ErrTooManyPending`，而 R02 的计数能让 256 个连接都建立。
`max_conns` 为 1、`max_pending` 为 2 时，同时向一个冷主机发出三个请求：第一个请求的连接还在拨号时，第三个就被拒绝了。

**请求。** 计数时不包括正在进行的拨号所对应的请求：当 `pendingLocked(h) - h.dialing >= limit` 时才拒绝（对 HTTP/1
而言每次拨号对应一个请求；对可能是 HTTP/2 的主机，在它应答之前一次拨号代表所有请求）。队列仍有上限：对拨号挂住的主机
是 `MaxConnsPerHost + MaxPendingPerHost`，而 `MaxConnsPerHost` 之内的突发请求不会被拒绝。

**fit 如何使用。** 不需要改动：`upstream.TestDefaultMaxConns` 恢复为同时发出 256 个请求。

## A16：`SendFile` 排队的文件计入连接未发送的输出

**状态。** 已在 fib v1.5.2 中部分完成（`6f66bcc`）：`SendFile` 排队的文件段计入 `NotifyOutput`、`WriteNoWait`、写截止时间和 `MaxPendingBytes` 看到的积压，所以静态 handler 的传输受节奏控制，停滞超时在普通 HTTP/1 上也能切断停止读取的客户端。停滞上限在 fib v1.5.4（`fe60763`）中完成，经维护者同意由 fit 的会话实现：`Context.SendFileStall(f, offset, n, stall, done)` 分段发送（排队给 sendfile 或读出后写入），段与段之间在 `stall` 的写截止时间下等待空间；`SendFile` 即不设上限的它。fit 的静态 handler 把整段范围交给它（R07e）。

**问题。** 在普通 HTTP/1 连接上，`Context.SendFile` 用 `Connection.SendFile` 把范围作为文件段排队，`pushLocked` 把它加入
发送队列，但不计入 `pendingBytes`。所以控制响应节奏的机制都看不到它：`WriteNoWait` 不会报告超过上限，`OnWritable`
（`Connection.NotifyOutput`）立即报告有空间，`outputwait.Unsent` 也不包括它。停止读取的客户端，只要不断开连接，就一直让它
的文件排着队、文件描述符保持复制状态，没有写截止时间能切断它，因为没有任何东西在等待。`SendFile` 本身在任何协议上也
不限制等待：在逐段复制的路径上，它无限期地等待 `OnWritable`。

**证据。** R05 的静态 handler 经 `SendFile` 分段发送一个范围，普通 HTTP/1 上每段 256 KB（其他情况 64 KB），段与段之间在
写截止时间为停滞超时的情况下等待 `OnWritable`。在 h2c 和 h3 上，停止读取 64 MB 文件的客户端在 1 s 停滞超时后被切断。在
普通 HTTP/1 上，每段之后 `OnWritable` 都报告有空间：整个文件都排进队列，截止时间从未设置，客户端 2.5 s 后开始读取时拿到
全部 64 MB。`handler/static/stall_test.go` 的 `TestStuckClient/http/1.1` 在此之前跳过。

**请求。** 把排队文件段剩余的字节计入 `pendingBytes`（排队时加上，`sendfile` 发出时减去），让 `NotifyOutput`、
`WriteNoWait` 以及全局的 `MaxPendingBytes` 预算像看待字节一样看待排给客户端的文件。更好的是在 `SendFile` 本身提供停滞
上限（一个 `stall time.Duration` 参数，或在调用前设置的字段），让它能发送整个范围，并限制每次等待客户端的时间，fit 就
不用自己分段。

**fit 如何使用。** 有了第一项，fit 不需要改动：传输的等待在 HTTP/1 上同样生效，测试恢复运行。有了第二项，`send` 把整个
范围交给 `SendFile`。

## A17：降低每个请求上 `OnFinish` 和 `SetValue` 的开销

**状态。** 已在 fib v1.5.4 中完成（`b39373d`），经维护者同意由 fit 的会话实现，两项都按第一种方案：回收的 Context 为下一个请求保留清空后的 response writer；`SetValue` 和 `Value` 不加锁（在请求交给其他 goroutine 之前设置，之后读取）。基准机上，开启访问日志时 h2c 的 fixed 和 static 提高 2.2% 和 2.1%（R07e）；`SetValue`、`OnFinish` 和 `Respond` 的单线程微基准从 105.6 ns 降到 94.2 ns。

**问题。** fit 的访问日志对每个记录的请求调用 `SetValue` 和 `OnFinish`。两者在 fib 里的开销都比它们代表的工作大：

- `OnFinish` 调用 `addHook`，后者调用 `Context.writer()`。对于整体写出的响应（`respond` 的响应就是），Context 还没有
  `responseWriter`，于是这个钩子从全局 `writerPool` 取一个（`sync.Pool.Get`），`recycle` 再把它重置后放回。为了一个只装一个
  函数的位置，每个请求都要在池里走一个来回。
- `SetValue` 要拿 `c.mu`，连接的 goroutine 也拿这把锁，所以解锁时这把锁所在的缓存行常常在另一个核上。

**证据。** 在压测主机上用 `perf` 测 fit 的 h2c 固定响应（h2load，64 个连接、32 个流；fit 用 7 个核），访问日志写到
`/dev/null`，R07d 的构建：日志每个请求的 0.51 µs 开销中，约 54 ns 是 `addHook` 及其下的 `writerPool.Get`，约 31 ns 是
`SetValue`，主要在 `Mutex.Unlock`；`Context.recycle` 占的 CPU 从不开日志时的 2.1% 升到开日志时的 3.3%。合起来约占日志开销
的六分之一，而这条路径不开日志时每秒 3.78M 请求，开日志时 2.83M。

**请求。** 以下任一项或两项：

- Context 回收时保留它的 `responseWriter`（重置后），像保留 `cancelHooks` 一样，而不是还给 `writerPool`；或者把 `OnFinish`
  钩子放在 Context 自己的存储里，注册时不需要 writer。
- 不在 `c.mu` 之后的值槽：例如一个原子指针，不加锁地设置和读取，因为这个槽只在 handler 运行前设置一次，之后读取。

**fit 如何使用。** fit 不需要改动：`observe.Track` 和 dispatcher 的 `OnFinish` 开销降低。

## A18：不经回调取得响应的状态和请求到达的时间

**状态。** 已在 fib v1.5.4 中部分完成（`b39373d`）：`Context.Status()`，配合 `Retained` 使用。未完成：`Received`。fib 只在超时需要时才为请求读时钟，为每个请求或每轮读取读一次时钟会让所有服务器付出代价，只为省下 fit 的两次读时钟中的一次；fit 继续自己读时钟。`Status` 只有在没有其他 goroutine 应答时才能安全读取，所以 fit 只对声明在返回前应答的 handler（`module.Inline`：`respond`，以及分发器的 404 和跳转）不注册回调地计数；开启指标时 h2c 的 fixed 提高 8.5%（R07e）。

**问题。** fit 的请求指标（I34）需要每个请求的响应状态和耗时。fib 只把状态交给 `OnFinish` 回调，所以 fit 对每个请求都
注册一个回调，用闭包保存 dispatcher 在请求到来时读到的时间，并在回调中再读一次时钟。对于在返回前就已经应答的 handler
（`respond` 和 `static` 都是），回调、闭包和其中一次时钟读取所提供的信息，fit 本可以在 handler 返回之后直接读到。

**证据。** 在压测主机上测 fit 的 h2c 固定响应（h2load，64 个连接、32 个流；fit 用 7 个核），I34 的构建，开启 Admin API：
不开 `metrics` 时每秒 3.86M 请求，开启时 3.28M，每个请求多 0.32 µs CPU。`perf` 显示其中约 110 ns 在 fib 的回调路径上
（`addHook`、`responseHooks.before` 和 `finished`、`writeResponse`、`recycle`，以及 writer 在 `sync.Pool` 上的进出，A17
已有描述），约 60 ns 在两次时钟读取，约 30 到 45 ns 在分配闭包和回收它，约 15 ns 在 fit 的计数。fitbench 两轮：h2c 固定
响应 3.79M → 3.29M（−13%），h2c 静态文件 2.46M → 2.25M（−9%）；HTTP/1.1（受压测工具限制在约 775k）和代理（约 230k）
没有变化。

**请求。**

- `Context.Status() int`：到目前为止已发送或已开始发送的响应的状态；尚无响应时为 0。配合 `Retained`，dispatcher 可以
  在 handler 返回后判断响应已经确定，在那时计数，只为稍后才应答的 handler 保留 `OnFinish`。
- `Context.Received() time.Time`，或一个单调的纳秒计数：读到请求头的时间，取自 fib 为读取轮次或超时本来就会读的时钟，
  粗一些也可以，这样计算耗时只需读一次时钟而不是两次。

**fit 如何使用。** dispatcher 在请求到来时用 `Received` 代替读时钟，在 handler 返回后，若 `Status` 表明响应已发送且没有
`Retained`，就只读一次时钟、不注册回调地计数；代理的响应仍用回调，由 A17 降低其开销。

## A19：流式请求的 Trailer 在交出请求体结尾的地方设置

**状态。** 已在 fib v1.5.3 中完成（`aa7c1b0`）；fit 从此使用它。

**问题。** `BodyStream.finish` 在从连接上读到请求体结尾的 goroutine 上设置请求的 `Trailer`。在 HTTP/2 和 HTTP/3 上，
那是连接的读取者，而不是 handler 的 goroutine，所以读取请求自带 `Trailer` 的 handler（net/http 允许在读请求体之前这样做）
会与请求体结束发生竞争。fit 的代理在开始时读取客户端声明的 trailer 名字，以便向上游声明。

**证据。** 自 M1 审查以来 CI 一直失败；在三个 CPU（与 CI 的 runner 相同）下，`handler/proxy` 的 `TestTrailers/h2c` 在竞态
检测下失败：`trailerNames` 读取 `Request.Trailer`，同时由 `h2ServerConn.handleTrailers` 调用的 `BodyStream.finish` 写它。
用 fib v1.5.2 连续运行五次有一次失败，用这个分支没有失败。

**修改。** trailer 保存在 `BodyStream` 中，只在把请求体结尾交给 handler 的地方、在取走它的 goroutine 上，成为请求的
`Trailer`：`Read` 返回 `io.EOF` 时，以及以 `fin` 调用 `OnBody` 的函数之前。在此之前请求保留它自带的 `Trailer`。HTTP/2 和
HTTP/3 上的测试（handler 在请求体和 trailer 到达时读取 `Trailer`）没有这个修改时在竞态检测下失败，有修改时通过。

**fit 如何使用。** fit 不需要改动：代理的 `trailerNames` 不再竞争。

## A20：不经 net/http 写客户端请求、读响应头

**状态。** 已在 fib v1.5.6 中完成（`0a9339f`），由 fit 的会话经维护者同意实现；fit 已使用。客户端自己写请求头、
读响应头，并解码没有 trailer 的整个分块 body，结果与 net/http 完全相同，由对照 net/http 的用例表和模糊测试检查；常见形式
之外的仍交给 net/http。写 fit 的代理请求：1 次分配、300 ns，原来 8 次、645 ns；读普通响应头：5 次、400 ns，原来 13 次、
1.1 µs。在基准机上，fit 的代理（3f25679）在 HTTP/1.1 上从每秒 226.6k 提高到 245.8k（+8.4%，各三轮），每个请求的 CPU
从 29.7 µs 降到 26.0 µs；h2c 上从 235.5k 到 238.1k。fib 和 fit 的竞态测试在该分支上都通过。

**问题。** fib 的客户端用 `net/http` 的 `Request.Write` 组装每个 HTTP/1 请求（`marshalRequest`、`marshalStreamedHead`），
并在新建的 `bufio.Reader` 上用 `net/http.ReadResponse` 读每个响应头（`responseParser.parseHead`；分块响应体在
`completeChunked` 中再读一次）。两者做的远多于代理请求所需：`Request.Write` 对头部的键排序、用 `fmt.Fprintf` 格式化、
扩容 `bytes.Buffer`；`ReadResponse` 为 100 字节的响应头分配 4 KiB 的 reader，还有一个 `textproto.Reader`、一个 `Header`
map、每个字段一个字符串，以及一个 `Response`。fib 的服务端两者都不用：它自己解析请求头、自己写响应头
（`parseSimpleRequestHead`、`appendResponseHead`、`appendHeaderLines`）。

**证据。** 在基准主机上用 `perf` 测量（R07f）：fit 在 2b37a8e、fib v1.5.4 上通过 HTTP/1.1 代理，wrk 256 个连接，fit 用
7 个 CPU，其后端是另一个 fit，用 2 个 CPU。fit 每秒处理 226.8k 个请求，每个请求 29.7 µs CPU；同样条件下 nginx 为 265.3k、
20.7 µs。内核部分差不多（fit 15.2 µs，nginx 17.0 µs；每个请求分别 5.9 和 6.3 次系统调用）；差别在用户态：14.5 µs 对 3.6 µs。
fit 的用户态时间中，fib 的代码及其调用的部分占 10.3 µs，fit 自己的代码占 1.8 µs。`parseHead` 每个请求 2.23 µs（全部 CPU 的
7.5%），其中 1.38 µs 在 `ReadResponse`；`marshalRequest` 1.49 µs（5.0%），其中 1.43 µs 在 `Request.write`；两者合计产生
请求一半以上的分配（`mallocgc` 和 GC 协助的 2.1 µs 中占 0.9 和 0.24 µs），随之而来的是 GC 后台标记的另外 1.3 µs。在开发用的
Mac 上单独测量：按 `parseHead` 的方式对四行的响应头调用 `ReadResponse`，1.0 µs、5,015 字节、13 次分配；对 fit 代理发出的
请求调用 `Request.Write`，0.63 µs、704 字节、8 次分配。

**请求。**

- 用 fib 自己的写入函数把 HTTP/1 请求头写进 `bufferpool` 的缓冲区：请求行、`Host`、按 map 顺序写头部字段（net/http 会排序，
  对端并不需要）、长度或分块；保留 `Request.Write` 对名字和值的检查，像 `appendHeaderLines` 对响应那样。
- 用 fib 自己的解析器解析响应头，像服务端解析请求那样：状态行；字段放入键为规范形式的 `Header`（整个响应头一个字符串、
  切片引用，如 `parseSimpleRequestHead`，或复用常见名字）；边解析边读出 `Content-Length`、`Transfer-Encoding` 和
  `Connection`；不用 `bufio.Reader`。`ClientResponse` 的形式不变。
- 分块响应体的解码不再调用第二次 `ReadResponse`。

合计每个请求约 3.7 µs，占代理 CPU 的八分之一，也是其用户态时间高于 nginx 的主要原因；受益的是 fib 的 HTTP/1 客户端和所有
`Client.Do` 的调用者，不只是 fit。

**fit 如何使用。** fit 不需要改动；代理的请求开销变小。

## A21：事件循环每次休眠只调用一次 `epoll_ctl`，而不是两次

**状态。** 未实现：所提的修改省不掉这次调用。`EPOLLONESHOT` 只在对中继调用 `epoll_wait` 取走事件时才撤防，而没有谁对中继调用
`epoll_wait`：Go 运行时用自己的边沿触发 epoll 监视它，只知道它就绪。在基准机（Linux 7.0）上，中继以 `EPOLLIN | EPOLLONESHOT`
持有一个实例，并像运行时那样被监视：每个新事件都让运行时的 epoll 看到它就绪，连续三次如此，在对中继调用一次 `epoll_wait`
（此时实例已被取空）之后依然如此。一次性的中继会在事件循环运行时一直处于布防状态，运行时的 poller 就会去查看忙碌循环的实例，
而中继正是为了避免这一点（fib 自己测得占 4.3% 的 CPU，见 `poller_epoll.go`）。另一种省调用的办法是唤醒后保持布防，直到循环
下次找到工作；fib 的记录测得其一个变体在 2 万和 5 万连接时少 1.6%，所以除了 fit 的代理，还需要 fib 的 WebSocket 基准来验证。
暂不改动。

**问题。** 无事可做的事件循环通过一个中继 epoll 实例在 Go 的 netpoller 中休眠（`epollWaiter.wait`）：先轮询一次，用
`EPOLL_CTL_MOD`（`EPOLLIN`）布防中继，休眠，被唤醒后再用一次 `EPOLL_CTL_MOD` 撤防。代理负载下事件循环经常休眠，
这就多出了 nginx 没有的系统调用。

**证据。** A20 的那次测量中，对 fit 进程运行 `perf stat`：每个请求 0.48 次 `epoll_ctl`、1.25 次 `epoll_pwait`（nginx 没有
`epoll_ctl`，1.36 次 `epoll_wait`）。`epollWaiter.wait` 连同内核时间每个请求 1.25 µs，其中 `arm` 0.31 µs，`pollEvents` 中
什么也没取到的轮询 0.82 µs。

**请求。** 以 `EPOLLIN | EPOLLONESHOT` 注册中继：事件发生后自动撤防，只有布防需要一次调用，唤醒后的撤防可以去掉。
如果 `wait` 注释中解释的布防后再查看一次仍然需要，就保留它。或者换一种只需一次调用的休眠方式。

**fit 如何使用。** fit 不需要改动。

## A22：客户端连接的空闲计时不必每个请求重置定时器

**状态。** 已在 fib 的 `main`（`7f632cb`，v1.5.6 之后）完成；fit 从 `a6d78cd` 起按提交使用它（规则 19）。连接的空闲计时只用
`idleUntil`，只在没有待触发的定时器时才设置定时器；定时器触发时，发现连接已被取用、空闲到更晚（按剩余时间重新设置）或已空闲足够久。
连接仍在最后一次空闲后 `IdleConnTimeout` 关闭。在基准机上，fit 的代理走 HTTP/1.1（ca450dd 用 v1.5.6，与加上该修改的对比）：每个
请求 24.85 和 24.86 µs CPU，对比 24.64 和 24.80，约少 0.13 µs；吞吐在噪声范围内，HTTP/1.1 245.8k → 246.1k，h2c 240.2k → 238.8k
（各三轮）。fib 在 `7f632cb` 上的 CI（第 215 次运行）在两个 macOS 测试上失败，所在的包不受该修改影响
（`TestRemoveUDPBindingClosesPeers/pollers-no-reuseport`、http3 的 `TestStreamTimeoutMidBody`），两者单独运行都连续通过 20 次。

**问题。** 每当一个请求的响应结束、连接回到连接池，`Client.idleLocked` 都会读一次时钟，并对连接的空闲定时器调用
`Timer.Reset`，这会获取定时器的锁、在运行时的定时器堆中移动它；连接被取用时又把它停掉。保持连接时，每个请求都如此。

**证据。** A20 的那次测量中，`idleLocked` 每个请求 0.23 µs，主要是 `Timer.Reset`。

**请求。** 把空闲截止时间作为一个时间值记在连接上，连接空闲时写入；每个客户端（或每个主机）用一个定时器，大约每
`IdleConnTimeout` / 4 扫描一次空闲连接，关闭超时的；或者只在截止时间移动超过超时的一定比例时才重置定时器。连接最多晚关闭
这个比例的时间，`IdleConnTimeout` 允许这样。

**fit 如何使用。** fit 不需要改动。

## A23：`Engine.Close` 切断的连接也运行 `OnClose`

**状态。** 已在 fib v1.5.6 中完成（`6fb6479`）；fit 从此使用它。

**问题。** 在 epoll、kqueue 和 IOCP 后端上，`Engine.Close` 一次切断所有连接，却不运行它们的 `OnClose`，而可移植后端的
`Close` 会运行。协议在 `OnClose` 里放开的东西因此一直被持有：HTTP/2 连接在最后一个流结束后设置的 idle 定时器（`IdleTimeout`，
默认两分钟）持有连接，并经由它持有已关闭的引擎、它的任务池以及它的流池那些 20000 槽的队列，直到定时器触发。测试中每启动并
关闭一次 fit（只要有过一个 h2c 请求），它就在内存中多留两分钟。

**证据。** fit 的 CI 在 macOS 上的 `handler/proxy` `TestStreamedBodyLimit` 失败，h2c 用例 `{150000 false 200}`：客户端
等待响应头超时。在负载下同时运行 16 份该测试时，本地每个 GOMAXPROCS=2 的副本都重现了同样的挂起；goroutine 转储显示客户端
已发出最初的 65535 字节、正在等待流量控制，而 fit 的连接读取者仍在 `newH2ServerConn` 中建立引擎的流池，尚未发出 SETTINGS。
当时机器正在使用交换空间：一个测试进程已增长到 4.2GB。每次带一个 h2c 请求启动并关闭 fit 会留下约 1MB（竞态检测下 56MB）；
给 fib 的 HTTP 定时器各个位置打上标记后，所有仍未触发的定时器都是 `OnClose` 从未到达的连接的 idle 定时器。整个 `handler/proxy`
包在竞态检测下峰值达 5.3GB，在同时运行多个包的 7GB runner 上，这意味着交换和数秒的停顿。

**修改。** `Close` 记下它切断的连接，在没有轮次运行后、任务池释放之前运行它们的 `OnClose`，如同它们最后一轮本会做的那样。
fib 的 `TestCloseRunsOnClose`（`Close` 时一个空闲连接和一个 handler 正在运行的连接）和
`TestCloseLetsGoOfAnIdleHTTP2Connection`（带空闲 HTTP/2 连接关闭的引擎能被回收）没有这个修改时失败，有修改时通过。

**fit 如何使用。** fit 的代码不需要改动。`handler/proxy` 在竞态检测下的峰值从 5.3GB 降到 1.8GB，运行时间从 142 秒降到
104 秒；同时运行 16 份 `TestStreamedBodyLimit`、每份 400 次，全部通过，每份都在 510MB 以内。

## A24：taskpool 新建的环不写任何单元

**状态。** 已在 fib 的 `main` 上完成（`a6d78cd`，v1.5.6 之后），CI 全部通过；fit 按提交使用它
（`v1.5.7-0.20261020131351-a6d78cd9d925`，规则 19）。

**问题。** `taskpool.newTaskRing` 建立环时，用原子操作写入每个单元的 `seq`：在十核及以上的机器上，默认规格的池有 100,000 个
单元，四核时有 40,000 个。在竞态检测下，原子操作触及的每个地址都会在检测器自己的内存中占用一个几百字节的同步对象，Go 堆并不
统计这部分内存，因此每个引擎的池在运行第一个任务之前就占用几十 MB。不开竞态检测时，这些写入也会触及队列的每一页。

**证据。** A23 之后，`handler/proxy` 在 `go test -race` 下的常驻内存峰值在 14 核 Mac 上仍为 1.27GB（峰值占用 1.42GB），
GOMAXPROCS=4 时峰值占用为 1.25GB，而 `TestHostHeader` 这类测试的 Go 堆从未超过 18MB，几乎都是这些环。启动一个没有流量的 fit，
占用为 179MB，其中 155MB 是 `vmmap` 称为 `Sanitizer` 的区域，Go 堆只有 9MB；不开竞态检测时同一测试峰值为 25MB。在十二个
经 HTTP/1.1、h2c 和 h2 使用后停止的 fit 上用 `runtime.AddCleanup` 探测，所有关闭的引擎都被回收，使用中的堆一直是 3MB，
所以并没有泄漏。

**修改。** 单元保存的 `seq` 减去了自己的下标，于是清零的单元本身就对它的第一个位置空闲，新建的环不写任何单元。fib 的
`TestTaskRingStartsUntouched` 检查新环的单元都是零，并检查一个四单元的环在 1000 圈中保持顺序和上限；fib 的测试在开与不开竞态
检测时都通过，vet 在 Linux、macOS、Windows 和 FreeBSD 上通过，staticcheck 在前三者上通过。一次入队和出队的开销与之前相同
（5.0 对 5.3ns，四个 CPU 时 142 对 142ns），建立 100,000 单元的环从 263µs 降到 82µs。

**fit 如何使用。** fit 的代码不需要改动。有了它，竞态检测下一个空闲 fit 的占用从 179MB 降到 67MB，`handler/proxy` 的峰值从
1.27–1.42GB 降到 540–720MB（各次运行相差约 100MB），GOMAXPROCS=4 时从 1.25GB 降到 518MB，用时不变。剩下的是最重的测试
有意持有的内存：在 `TestStuckClientsPastMaxConns` 和 `TestStuckClientWithoutStallTimeout` 中，八个不读取的下载填满了测试
自己的 h2c 客户端和 fib 的 HTTP/3 流的流量控制窗口；流测试每个方向传输 64MB；再加上竞态检测器为这些内存保留的影子内存。
曾尝试在每个测试结束时关闭测试辅助函数 `client` 的 transport 的空闲连接，结果在噪声范围内没有变化：fit 的监听器关闭时会切断它们。
