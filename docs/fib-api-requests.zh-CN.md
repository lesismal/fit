# fit 需要 fib 提供的 API

[English](fib-api-requests.md) | [简体中文](fib-api-requests.zh-CN.md)

按 [开发规则](rules.zh-CN.md) 审查 M0、M1 时发现，有些地方只靠 fib v1.3.0 现有的 API，fit 无法遵守规则。本文列出 fit
需要 fib 提供的 API、原因，以及 fit 打算怎么用。文中的名字和签名只是建议，最终形式由 fib 决定。

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
| A8 | 文档修正 | 部分完成，v1.4.0 | — | — |
| A9 | 拒绝不是 token 的请求头字段名 | 已完成，v1.4.0 | 13 | — |
| A10 | 长度未知的客户端响应从响应头起就以流方式交付 | 已完成，v1.3.1 | 5 | — |
| A11 | 不复制即取得整体到达的客户端响应体 | 已完成，v1.4.0 | 12 | R07c |
| A12 | 减少每个客户端请求的定时器和 context | 已完成，v1.5.0 | 12 | R07c |
| A13 | 只有 `OnFinish` 钩子的响应不复制响应头 | 已完成，v1.4.0 | 12 | R07c |
| A14 | 在没有连接的 Context 上 `Release` | 已完成，v1.5.1 | 13 | R05 |
| A15 | `MaxPendingPerHost` 不计入正在为其拨号的请求 | 已完成，v1.5.1 | 11 | R06 |

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

**状态。** 已在 fib v1.4.0 中部分完成（`75e7005`）：`StreamResponseBody` 和 `OnBody` 的文档说明了会等待的 `OnBody` 函数会拖住连接上的其他流。没有增加 `WriteStallTimeout`：有了 `WriteNoWait`（A2），fit 的代理不再需要它。

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
