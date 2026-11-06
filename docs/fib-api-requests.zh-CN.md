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
| A25 | 按请求限制请求体大小，流式的也包括在内 | 已完成，`cfb4f09`（v1.5.6 之后的 main） | 7、11 | I39 |
| A26 | 一个 Context 上可以有多个 `OnCancel` 回调 | 已完成，`cfb4f09`（v1.5.6 之后的 main） | 1、12 | I39 |
| A27 | 在写出时改写响应体的钩子 | 已完成，`fc7ec5e`（v1.5.6 之后的 main） | 5、7、11 | I46 |
| A28 | 处理器返回后再 Upgrade 被保留的请求 | 已完成，`929924e`（v1.5.6 之后的 main） | 1、9 | I47、I48 |
| A29 | 客户端请求收到 101 时把连接移交为 tunnel | 已完成，`929924e`（与 A28 一起） | 1、9 | I47 |
| A30 | HTTP/3 客户端发送 extended CONNECT | 已完成，`929924e`（与 A28 一起） | 13 | I48 的测试 |
| A31 | HTTP/2、HTTP/3 tunnel 的未发送输出排空时的回调 | 已完成，fib `main` 上的 `70f76c8` | 1、8、9 | I48 |
| A32 | 按请求选择从请求头起流式交给处理器的请求体；HTTP/2 客户端发送 `te: trailers` | 已完成，`0bd2447` | 1、5、9 | I49 |
| A33 | 读取被暂停时看到的对端结束不再丢失 | 已完成，`9ab22e8` | 8、9 | I50 |
| A34 | UDP 对端自己的空闲超时，或不超时 | 已完成，`11de9eb` | 10、11 | I51 |
| A35 | 按 PROXY 头替换连接的对端地址 | 已完成，`530d222` | 13 | I52 |
| A36 | SOCKS 与 CONNECT 中继的空闲超时 | 已完成，`4f1c3e9` | 10 | I53、I53b |
| A37 | `ConnectProxy` 在拨号回调里应答，不在 worker 上等待 | 已完成，`4f1c3e9` | 1 | I53b |
| A38 | 请求体失败时，HTTP/1 响应声明连接将关闭 | 已完成，`86071b8` | 13 | I66 的 CI 修复 |
| A39 | 客户端连接为每个响应运行的读轮次更便宜 | 未完成，已放弃：第 1、2 项没有可测的 CPU 节省 | 12 | — |
| A41 | 异步钩子运行期间压住响应头 | 已完成，fib `main` 上的 `76c0275` | 1, 4 | I72 |
| A40 | 响应未写完响应体就结束时中止其响应体过滤器；过滤器之后不再给出长度 | 已完成，`3998953`，v1.6.0 | 6, 11 | I67 |
| A42 | HTTP/3 服务端把一轮的数据报一次批量发出 | 已完成，`a34c5db` | 12 | — |
| A43 | 减少 TLS 上 HTTP/2 的唤醒和协程切换 | 已完成，`5a02929` | 12 | — |
| A44 | 客户端超过 `MaxPendingPerHost` 拒绝请求时不再遍历队列 | 已完成，`ee1a859` | 1, 11, 12 | — |
| A45 | 读取中途停下的一轮收到对端的结束时，不再当作数据已读完 | 已完成，`9f99ea5` | 5, 13 | — |
| A46 | 在别处完成工作的响应体过滤器 | 已完成，fib `dbe5c274`（`main`） | 1, 4, 5, 8 | I81 |
| A47 | 流式请求体转交时不必要的拷贝 | 已完成，fib `309a6cd4`（`main`） | 1, 5, 8 | W4 |
| A48 | 整份响应的 body 取自缓冲池，已是字节的请求体不再拷贝 | 已完成，fib `46827ea7`，HTTP/2 `d3707ca7`（`main`） | 1, 5, 8 | W1 |
| A49 | 在监听 socket 上朝某地址打开 UDP 对端 | 已完成，`96a2331`（在 `80c7f76` 中） | 1, 9 | I85（P2P）、I87 |
| A50 | QUIC DATAGRAM 帧 | 已提出 | 9 | I90（P2P） |
| A51 | 包设备的描述符在 poller 上读写 | 已提出 | 1, 8, 11 | I95（VPN） |
| A52 | WebSocket 连接的底层传输：强制关闭与其地址 | 已提出 | 10, 13 | llm（028） |
| A53 | WebSocket 连接发送的背压 | 已提出 | 8, 9, 11 | llm、tunnel（028） |
| A54 | 引擎 UDP binding 上的原始 QUIC 端点 | 已提出 | 15 | tunnel（028） |
| A55 | 经 TCP 和 TLS 的 TURN | 已完成，`97201b6`（在 `84a67f7` 中） | — | I111（029） |
| A56 | 从不让 worker 等待的 TURN 服务器 | 已完成，`74100b2`（在 `84a67f7` 中） | 1 | I87、I111（029） |
| A57 | TURN 服务器的限制与钩子 | 已完成，`bf669ce`（在 `84a67f7` 中） | 11 | I111（029） |
| A58 | 原地修改和排空 TURN 服务器 | 已完成，`496301d`（在 `84a67f7` 中） | 10, 11 | I111（029） |
| A59 | 从指定的本地地址和端口范围发起拨号 | 已完成，`3655193`（在 `84a67f7` 中）；Windows 修复 `63b38d4`（在 `9824c68` 中） | — | I111（029） |
| A60 | 代理两个方向都按行转发头部块 | 已完成，fib 分支 `perf/header-blocks`（尚未进入 `main`） | 12 | — |
| A61 | 移除 RTSP 服务器的 UDP 绑定 | 已提出 | 4, 11 | I116（030） |
| A62 | 用 splice(2) 搬运代理的 HTTP/1.1 上传 | 已完成，fib `main` 的 `60d67aa` | 12 | — |
| A63 | 多路径 TCP 作为监听器的选项 | 已完成，fib `5c0e5b9` | — | — |
| A64 | 非分片 MP4 文件的读取器 | 已完成，fib `8998d58` | 1、11 | I131–I134（032） |
| A70 | TURN 中继不共用端口范围内的同一端口 | 已完成，fib `856ad5b` | 11 | I162 |

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
2026-10-21 试过：唤醒后中继保持布防，只在休眠前那次查看发现事件时才撤防，这样无事可做再次休眠的循环两次调用都不用做。在压测机上，
cbdca26 的 fit 与同一 fit 换用该 fib 对比，各三轮交替：代理 HTTP/1.1 每秒 248k → 246k 个请求（−0.8%），h2c 240k → 240k
（−0.1%），每请求 CPU 不变（HTTP/1.1 23.15 µs → 23.12）；fixed HTTP/1.1 +0.9%，h2c −0.3%；全在噪声之内。省下的调用在
fit 的吞吐上看不出来，所以没有保留，也就不需要 fib 的 WebSocket 基准。暂不改动。

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

## A25：按请求限制请求体大小，流式的也包括在内

**状态。** 已在 fib 的 `main` 上完成（`ad0d175`，测试在 `cfb4f09` 中改为稳定），`cfb4f09` 的 CI 全部通过
（run 39569138260）；fit 按提交使用它（`v1.5.7-0.20261021080904-cfb4f09f0283`，规则 19）。

**实际实现。** `func (c *Context) LimitBody(n int64) error`，在服务该请求的 goroutine 上、取请求体之前且回应之前调用；
`n` 为负时什么也不做，`n` 为 0 表示不允许请求体。`n` 与 `MaxBodyBytes`（整体读取的请求体）或 `MaxStreamedBodyBytes`
（流式请求体）取较小者。已知超过 `n` 的请求体——`Content-Length` 超过 `n`、整体到达且更长、或流式且已到达超过 `n`
字节——立即拒绝：请求体以 `ErrBodyTooLarge` 结束，响应尚未开始时 fib 回应 413（HTTP/1 连接在响应后以 lingering close
关闭；HTTP/2 和 HTTP/3 只拒绝该流），`LimitBody` 返回 `ErrBodyTooLarge`。否则返回 nil，没有声明长度的请求体
（HTTP/1 的 chunked，没有 content-length 的 HTTP/2 和 HTTP/3）超过 `n` 时以 `ErrBodyTooLarge` 结束，`Read` 和
`OnBody` 回调都能看到，与超过 `MaxStreamedBodyBytes` 时一样。流式请求体因大小被结束、又没有被回应就放手的请求，
回应 413 而不是空的 200（超过 `MaxStreamedBodyBytes` 时同样如此）。fib 的 `TestLimitBody` 覆盖 HTTP/1.1、h2c、
TLS 上的 h2 和 HTTP/3，服务端整体读取、流式、超过阈值才流式三种配置，以及声明和未声明长度的请求体。

**问题。** fit 的 `body_limit` 中间件把一个路由的请求体限制在低于监听器 `max_body_bytes` 的大小。它可以拒绝
`Content-Length` 声明过大的请求体，但无法拒绝没有声明长度的请求体（HTTP/1.1 的 chunked，或没有该字段的 HTTP/2 和
HTTP/3）：这些字节送到处理器的 `OnBody` 回调，中间件看不到，而一个 `Context` 只有一个回调。这样的请求体只受监听器的
`MaxBodyBytes` 和 `MaxStreamedBodyBytes` 限制，所以本应只接收 1MB 的路由会接收整个监听器允许的大小。

**建议的 API。** `Context.LimitBody(n int64)`，在取请求体之前调用：超过 `n` 字节时，fib 以错误结束请求体，处理器的
`OnBody` 能看到（与超过 `MaxStreamedBodyBytes` 时一样），在响应尚未开始时自行回应 413；`Content-Length` 超过 `n` 时立即
拒绝。`n` 与连接的限制取较小者。

**fit 如何使用。** `body_limit` 调用 `c.LimitBody(max_bytes)`，返回错误时不再调用下一个处理器（fib 已经回应），
并不再自己检查 `Content-Length`。

## A26：一个 Context 上可以有多个 `OnCancel` 回调

**状态。** 已在 fib 的 `main` 上完成（`ad0d175`，与 A25 一起；`cfb4f09` 的 CI 通过）；fit 按提交使用它
（`v1.5.7-0.20261021080904-cfb4f09f0283`，规则 19）。

**实际实现。** `Context.OnCancel(fn func(error))` 签名不变，保留所有注册的回调，按注册的逆序运行。每个回调仍只运行
一次，在 engine 的 handler 池上运行，从不在响应写出之后运行，请求已结束时注册则立即运行；响应写出后注册什么也不做。
第一个回调存在字段里，其余的存在一个指针指向的切片里，Context 回收时保留，所以一个或多个回调都不分配内存。fib 的
`BenchmarkOnCancel`（一次注册加一次回收，在基准测试主机上交替运行 10 轮）：一个回调 7.00 → 7.48ns，多出的部分是
回收时清除 hooks 多出的一个字；两个回调 10.4 → 14.4ns，而之前第二个会替换第一个；前后都没有内存分配。fib 的测试覆盖
连接被切断的请求上的多个回调，以及回收后的 Context。

**问题。** `Context.OnCancel` 只保存一个回调：第二个会替换第一个。必须知道客户端离开的中间件（例如 fit 的
`concurrency_limit` 要归还请求的名额）会在处理器注册自己的回调时失去它的回调，而 fit 的代理对每个请求都会注册。fit 的
测试 `TestConcurrencyClientGone` 就是这样失败的：客户端挂断后名额仍被占用。

**fit 中的权宜做法。** 中间件用 `context.AfterFunc` 等待 `Context.Context()`，只在处理器返回且请求被保留时才获取它，
响应发出后停止。在 fit 的微基准中，这条路径对每个被保留的请求多花一个 context 及其取消机制；被取消的请求还多一个 goroutine。

**建议的 API。** `OnCancel` 保留所有注册的回调，按注册的逆序运行，与响应钩子相同；只有一个回调时开销与现在一样（一个字段，
第二个回调才用切片）。

**fit 如何使用。** `concurrency_limit` 在处理器运行之前与 `OnFinish` 一起注册 `OnCancel`，不再获取请求的 context。

## A27：在写出时改写响应体的钩子

**状态。** 已在 fib 的 `main` 上完成（`fc7ec5e`），由 fit 的会话经维护者同意实现；fit 按 commit 依赖它
（`v1.5.7-0.20261021111325-fc7ec5e327d4`，规则 19）。fib 的 `http`、
`http3` 和中间件的竞态测试在基准机上通过，linux、darwin、windows 和 freebsd 的 `go vet` 与 staticcheck 通过；fib 根包
在那里只有 UDP 和 socket 缓冲区相关的测试失败，在同一容器中 `main` 也一样。

**问题。** fit 的 `compress` 中间件（I46）必须压缩处理器以任何方式写出的响应。fib 唯一能看到响应体的钩子是
`OnResponse`，而注册它会让经 ResponseWriter 方法写出的响应整体缓存到处理器结束：没有上限，也不再流式发送。代理对超过
64 KB 的响应和经它的 server-sent events 都是流式的；缓存它们会违反规则 5、7 和 11。

**证据。** `http/response_writer.go` 中的 `holdsWhole` 在注册了 `OnResponse` 钩子（`hooks.whole`）后缓存每次写入；
`w.buf` 没有上限，`Flush` 对这样的响应不起作用。

**实现。** `Context.OnBodyFilter(fn func(status int, header http.Header) BodyFilter)`。`fn` 在响应头定下来时与
OnHeader 钩子依次运行，可以修改 header，返回一个 `BodyFilter`（`Filter(p) ([]byte, error)`、`Close() ([]byte, error)`）
或 nil。响应体的每一部分在写出时都经过它：`Write`、`WriteNoWait`、`ReadFrom`（此时复制而不是 sendfile）；`Flush` 和
`FlushError` 交出同时实现了 `BodyFlusher` 的过滤器所缓冲的部分；`Finish` 发送 `Close` 剩下的部分。整体发送的响应
（`WriteResponse`、`Respond`）整体过滤，其钩子看到的 `Content-Length` 是 body 的长度。有过滤器的响应不带
`Content-Length`；HEAD 和没有 body 的状态码不过滤。同一响应上注册的多个过滤器依次运行。fib 的 `TestBodyFilter`、
`TestBodyFilterFlush` 和 `TestBodyFilterChainAndSkips` 覆盖 HTTP/1.1 和 h2c。基准机上的 `bench/microbench.sh`，
10 次交替运行，`cfb4f09` 对比本改动：`BenchmarkServerKeepAlive`、`ServerKeepAliveHooked`、`StreamRespond` 和
`StreamAccessLog` 没有变慢，分配次数相同。

**fit 如何使用。** `compress` 注册一个 `OnBodyFilter`，根据状态码和 header 判断，设置 `Content-Encoding`、`Vary` 和
弱 `ETag`，并返回流式的 zstd、brotli 或 gzip 编码器。

## A28：处理器返回后再 Upgrade 被保留的请求

**状态。** 已在 fib 的 `main` 上完成（`929924e`），由 fit 的会话经维护者同意实现；fit 按 commit 依赖它（`v1.5.7-0.20261021130553-929924e17392`，规则 19）。

**问题。** 代理只有在上游回应 101（以及它选定的子协议和扩展）之后，才能把客户端的请求切换为 WebSocket，而代理是在 fib
客户端的回调中、处理器返回之后才知道的。`Context.Upgrade` 拒绝处理器已不在运行的请求；在处理器中等待上游会阻塞 worker。

**实现。** `Upgrade` 和 `Connect` 也接受处理器保留着、尚未回应的请求，可以从任何 goroutine 调用。HTTP/1 上切换时获取连接的
parser 锁：请求等待期间客户端发来的数据（请求忙时由 parser 保存）是 tunnel 的第一批数据，期间读到数据的轮次把它交给
tunnel，最后一次 `Release` 不再让连接继续处理下一个请求。HTTP/2 和 HTTP/3 的流照旧在各自的锁下切换；先到的 DATA 有界地
排在流的 tunnel 中。fib 的 `TestLateUpgradeHTTP1`、`TestLateUpgradeHTTP2`、`TestCcTunnelLateUpgrade` 和
`TestLateUpgradeAfterAnswer`。

**fit 如何使用。** 代理保留要求切换协议的请求，把握手发往上游，在回调中用上游的回应调用 `Upgrade`。

## A29：客户端请求收到 101 时把连接移交为 tunnel

**状态。** 与 A28 一起完成（`929924e`）。

**问题。** fib 的客户端收到 101 时在回调运行之前就关闭了连接：代理无法保留上游切换了的连接。

**实现。** `Client.StartUpgrade(req, handler, callback)`。收到 101 时连接离开客户端，不再计入 `MaxConnsPerHost`，成为
tunnel（`ClientResponse.Tunnel()`），其 handler 在回调返回后打开，101 之后到达的数据排队。其他回应是普通响应。会经
HTTP/2 连接发出的请求以 `ErrNotUpgradable` 失败。fib 的 `TestClientStartUpgrade` 和 `TestClientStartUpgradeHTTP2`。

**fit 如何使用。** `upstream.Pool.SendUpgrade` 经上游的池发送握手，使用池的拨号、TLS 和限制。

## A30：HTTP/3 客户端发送 extended CONNECT

**状态。** 与 A28 一起完成（`929924e`）。

**问题。** I48 的测试要经 h3 入口打开 WebSocket，而 fit 能用的客户端都不能经 HTTP/3 发送 extended CONNECT。

**实现。** HTTP/3 客户端把 header 中带 `:protocol` 的 CONNECT 作为 extended CONNECT（RFC 9220）发送，与 net/http 的
HTTP/2 transport 相同。fib 的 `TestClientExtendedConnect`。

## A31：HTTP/2、HTTP/3 tunnel 的未发送输出排空时的回调

**状态。** 2026-10-24 按建议实现，fib `70f76c8`，自 2026-10-24 起在 fib 的 `main` 上：`Tunnel.NotifySendable` 在 HTTP/1.1 上就是
`Connection.NotifyOutput`，在 HTTP/2 和 HTTP/3 上于流控压住的部分降到 limit 的一半时运行 fn，流结束时带错误运行。
fib 的 `ConnectProxy` 用它代替了原来的 goroutine。HTTP/3 上一个流同时只能有一个这样的回调。

**问题。** 中继在一侧未发送的数据过多时暂停另一侧的读取，排空后再恢复。HTTP/1 上 `Connection.NotifyOutput` 会告知何时
排空，不需要任何等待；HTTP/2 和 HTTP/3 的 tunnel 只有 `WaitSendable`，在此期间阻塞一个 goroutine。

**证据。** fit 的代理把经 h2 或 h3 进来的 WebSocket 中继到 HTTP/1.1 上游；客户端读得慢时，每个被暂停的 tunnel 有一个
goroutine 在 `WaitSendable` 中等待（fib 自己的 `ConnectProxy` 也是如此）。

**建议的 API。** `Tunnel.NotifySendable(limit int, fn func(error)) bool`，与 `Connection.NotifyOutput` 相同：未发送超过
limit 时，在不超过一半时运行 fn 一次，tunnel 结束时带错误运行，并返回 true。

**fit 如何使用。** 中继用它代替那个 goroutine。

## A32：按请求选择从请求头起流式交给处理器的请求体

**状态。** 已完成（fib `main` 的 `0bd2447`），由 fit 的会话实现。

**问题。** fit 只在请求体超过 64 KB（`StreamRequestBodyThreshold`）时才把它流式交给处理器，好让普通请求体完整到达、可以
重试和镜像。gRPC 调用的请求体没有长度，由一个个小消息组成，双向流或客户端流调用的客户端可能要等第一条消息的应答才发下一条：
于是代理的处理器永远不会运行。fib 的 HTTP/2 客户端还会去掉所有 `TE` 字段，而 gRPC 服务器期望收到 `te: trailers`。

**证据。** 把 fit 监听器里下面的钩子去掉后，I49 的 `TestGRPCBidiStreaming` 在第一个应答上一直卡到 10 秒期限，h2c 和
h2 上游都是如此。

**实现。** 包 `http`（HTTP/1 和 HTTP/2）和包 `http3` 的 `Config.StreamRequestBodyAtHeader func(*http.Request) bool`：与
`StreamRequestBody` 一起使用时，对每个还有请求体待到达的请求调用一次，被它选中的请求立即运行处理器，请求体不论阈值都经
`OnBody` 流式到达。HTTP/3 在请求头处无法判断没有声明长度的请求是否还有请求体，所以在那里被选中的请求即使请求体为空也按流式
处理。函数为 nil 时只多一次 nil 判断，且只针对有请求体的请求。HTTP/2 客户端在请求的 `TE` 列出 `trailers` 时发送
`te: trailers`（HTTP/2 允许的唯一值），其他值都去掉。fib 的 `TestStreamRequestBodyAtHeaderH2`、
`TestStreamRequestBodyAtHeaderH1`、`TestH2ClientSendsTETrailers` 以及 http3 的 `TestStreamRequestBodyAtHeader`。

**fit 如何使用。** 监听器选中 gRPC 调用（`application/grpc` 或其子类型的 HTTP/2 或 HTTP/3 POST），代理带 `TE: trailers`
把它们发往上游。

## A33：读取被暂停时看到的对端结束不再丢失

**状态。** 已完成（fib `main` 的 `9ab22e8`），由 fit 的会话修复。

**问题。** 读取被暂停的连接，如果对端发送数据后结束了自己一侧，就永远收不到这个结束：看到结束的那一轮把读取挂起并丢掉了
结束，而它的边沿触发事件已经用掉；解除暂停后交回的读取读走数据，在短读处停下，没有读到结束。连接一直不关闭，`OnClose`
不运行。因还有待发送输出而推迟的读取，也以同样方式丢失结束。

**证据。** I50 的 `TestTCPHangUp`：客户端在 fit 仍在拨号上游（暂停着客户端的读取）时写入并关闭，上游等了 10 秒；fib 的
`TestHoldReadsFromOpenThenHangup` 用在 `OnOpen` 中暂停读取复现了它。

**实现。** 连接把结束（`hupOwed`）与挂起或推迟的读取一起记下，下一轮读取时一并交回。fib 的
`TestHoldReadsFromOpenThenHangup`。

**fit 如何使用。** `l4` 从 `OnOpen` 起暂停客户端的读取，直到上游连接打开，并按另一侧的积压暂停任一侧的读取。

## A34：UDP 对端自己的空闲超时，或不超时

**状态。** 已完成（fib `main` 的 `11de9eb`），由 fit 的会话实现。

**问题。** fib 关闭 UDP 监听器上静默超过 `Config.UDPIdleTimeout` 的对端，这个值整个引擎只有一个（默认 60 秒），它同时限制
HTTP/3 的对端，QUIC 的空闲超时必须低于它。udp 服务的会话有服务自己的 `idle_timeout`，可能更长；而 fit 的监听器在多次重载间共用
一个引擎，引擎的值无法跟随配置。

**证据。** I51 的 `TestUDPSessionsExpire`，以及超过一分钟的 `idle_timeout`：如果无法脱离引擎的清扫，会话不论服务怎么配置都会在
60 秒时结束。

**实现。** UDP 监听器对端上的 `Connection.SetUDPIdleTimeout(d)`：`d > 0` 为该对端替换引擎的超时（检查频率与引擎的清扫相同），
零恢复为引擎的，负值让对端一直保留到被关闭。每次清扫每个对端多一次原子读取。fib 的 `TestUDPPeerIdleTimeout`。

**fit 如何使用。** `l4` 对每个 udp 会话的客户端一侧设置 -1，并像 TCP 中继那样用自己的定时器关闭会话。

## A35：按 PROXY 头替换连接的对端地址

**状态。** 已完成（fib `main` 的 `530d222`），由 fit 的会话实现。

**问题。** 位于发送 PROXY protocol 的负载均衡器之后的监听器，从头部得知客户端地址，但 fib 的 HTTP 处理器从连接的对端取请求的
`RemoteAddr`，即负载均衡器的地址，且无法修改。

**证据。** I52 的 `TestProxyProtocolHTTP`：上游的 `X-Forwarded-For` 必须是头部中的客户端，而只有连接自己的地址能把它带进请求。

**实现。** `Connection.SetRemoteAddrPort(ap)`：此后 `RemoteAddrPort` 返回 `ap`，`RemoteAddr` 返回它的 `*net.TCPAddr`，取代
socket 的地址；由读取头部的处理器在把连接交给真正服务它的处理器之前调用。它复用连接已有的压缩对端地址和一个标志位，连接不变大；
UDP 连接以及无效或带 zone 的地址不受影响。fib 的 `TestSetRemoteAddrPort`。

**fit 如何使用。** `l4.AcceptProxy` 包装配置了 `proxy_protocol` 的监听器的 HTTP（及 TLS）处理器；`l4` 对 tcp 监听器的连接设置它，
使 `ip_hash` 和它向上游发送的 PROXY 头都指向客户端。

## A36：SOCKS 与 CONNECT 中继的空闲超时

**状态。** 已完成（fib `main` 的 `4f1c3e9`），由 fit 的会话实现。

**问题。** 中继中的 SOCKS CONNECT 和 `ConnectProxy` 隧道，只要两端都不关连接，即使什么都不传也一直保持：规则 10 要求每个对外交互都有
空闲超时，fit 自己的 tcp 中继也有。

**证据。** I53 的 `TestSOCKSIdleTimeout` 与 I53b 的 `TestConnectIdleTimeout`：没有它，连上后不再说话的客户端会占住两个 socket，
直到两端自己超时。

**实现。** `socks.Config.IdleTimeout` 与 `ConnectProxyConfig.IdleTimeout`：中继在这么长时间里双向都没有数据，就关闭两端；零或负值
保持原有行为。每个中继记下最后一次传数据的时间（每次回调一次原子写），由一个定时器查看，从应答（或隧道打开）起到任一端关闭为止。
fib 的 `TestRelayIdleTimeout` 与 `TestConnectProxyIdleTimeout`。

**fit 如何使用。** `l4.NewSOCKS` 与 `forward_proxy` 处理器按规则的 `idle_timeout`（默认 5 分钟）设置它。

## A37：`ConnectProxy` 在拨号回调里应答

**状态。** 已完成（fib `main` 的 `4f1c3e9`），由 fit 的会话实现。

**问题。** `ConnectProxy.ServeHTTP` 在服务请求的 goroutine 上用 channel 等拨号结果，而那正是 fib 的 worker：违反规则 1。应答慢的目标
最多会占住一个 worker 直到拨号超时，足够多时整个引擎的请求都会停住。

**证据。** 它自己的文档就这么写（“its handler waits for the dial”）；I53b 在 fit 的监听器上提供 CONNECT，而这些监听器与所有路由共用
一个任务池。

**实现。** `ServeHTTP` retain 请求后立即返回；拨号回调用 `Context.Connect` 打开隧道，或应答 502、504、400，然后 release（处理器
返回后对 retain 的请求调用 `Connect` 由 A28 提供）。fib 的 `TestConnectProxyDoesNotWait` 把拨号压到处理器返回之后。

**fit 如何使用。** `forward_proxy` 处理器是一个 `ConnectProxy`：它的 `Allow` 检查客户端和目标，`Dial` 是 `l4.Rules.Dial`，后者拒绝
解析到规则所拒地址的主机名。

## A38：请求体失败时，HTTP/1 响应声明连接将关闭

**状态。** 已完成（fib `main` 的 `86071b8`），由 fit 的会话实现。

**问题。** 在 HTTP/1 上，流式请求体失败时（超过上限、分块格式错误、暂停过久或被截断），由于剩余的请求体不会再读取，fib 在响应发出后
关闭连接。但响应头没有这样声明：`commit` 和 `writeHTTP1` 只在请求自己带 `Connection: close`、响应带了它或服务器正在 drain 时才设置
`Connection: close`。客户端以为连接会保持，就留下它，并在一条正在关闭的连接上发出下一个请求，结果被重置。

**证据。** fit 的 `TestBodyOverNetwork`（`middleware/limit`）在 CI 的 Linux runner 上失败（run 39777797515）：一个 1 MiB 的
chunked 请求体被 413 拒绝后，Go 的客户端在同一连接上发出下一个 POST，得到 `connection reset by peer`。在基准机上用 1、2、4 个 CPU
运行 1500 次，约失败 10 次。

**实现。** `Context.bodyFailed` 判断流式请求体是否已失败，写 HTTP/1 响应头的两处都据此关闭连接。fib 的
`TestLimitBodyEndedPastTheLimitCloses` 用 net/http 的解析器读取两种 413：处理器不应答时 fib 写的，以及处理器自己写的，要求两者都带
`Connection: close`；不改时前者没有。改后 fit 的测试 1500 次全部通过。

**fit 如何使用。** 无需调用：`body_limit` 的拒绝，以及任何路由对上传失败的请求的应答，现在都会让客户端新开连接。

## A39：客户端连接为每个响应运行的读轮次更便宜

**状态。** 未完成，已放弃：第 1、2 项已经实现，但没有可测的 CPU 节省，所以 fib 两项都不保留。在一个未推送的 fib 分支上，一个
提交让读轮次的 uncork 等到 `process` 下一遍本来就要取的锁时再做，另一个改为读进每个 P 保留的缓冲（用 `linkname` 访问运行时），
不再从 `bufferpool` 借；测试、竞态检测、vet 和 staticcheck 在四个系统上都通过。fib 的 `BenchmarkReadRound` 中客户端读轮次从
1.701 变为 1.684 µs（噪声范围内）；profile 显示 uncork 省下的部分又回到了 `process` 里，每个 P 的缓冲与原来池的借还成本相当：
每个请求约 0.06 µs。第 3 项未尝试。

加上 fit 自己的部分（`fad911f`：open exchanges 改为串在 exchange 上的链表而不是 map；`be8cf0c`：`Forwarded` 元素和完整请求体
的 reader 放进 exchange；两者都保留），2026-10-23 在基准机上对比 fit 的 `main`（4aa8196）与基于上述 fib 改动的这两项，3 轮交替：代理走 h2c 每秒 235.4k → 236.2k 请求，每个请求 CPU 25.88 → 25.64 µs；走 HTTP/1.1 242.6k → 242.1k，
23.72 → 23.63。两者都在噪声范围内。open set 去掉 map 省 0.05 µs，不是 0.14，因为分片锁还在。

所以下文的 1.65 µs 并不是能从读轮次里削掉的开销：阻塞 client 也为同样的工作付了代价——为上游的响应被唤醒并读取它——只是付在
Go 的 netpoller 和调度器里，归因只把其中一部分记到了它头上。阻塞路径少的 2.7 µs 来自哪里，任何单个函数都解释不了；它薄薄地分布在
h2 server、fit 和 GC 上，就像把一个 exchange 从一个核交给另一个核造成的缓存未命中那样。这一点没有测量。

**问题。** fit 的代理从上游收到的每个响应，除了客户端自身的工作，还要在上游连接上走一整轮 fib 引擎的读轮次：poller 为它唤醒并重新
注册（`epollWaiter.wait`、`arm`、`pollEvents`、`runLoop`），把连接交给一个 worker（`submitReady`、taskpool），worker 执行这一轮
（`process`、`readLoop`）：从 `bufferpool` 借一个读缓冲再归还；因为引擎合并写，开始时 cork 连接，结束时在连接的锁下 uncork
（`uncork`、`foldCorkedLocked`、`flushLocked`）。在客户端连接上，cork 没有收益：这一轮里 `OnData` 写的是另一条连接，也就是下游
客户端的连接；上游连接上唯一的发送是下一个请求，`Client.release` 可能在这一轮里把它交给这条连接。每个响应都走同样的一轮，12 字节的
响应也一样，代理在 HTTP/1.1 和 h2c 上都是每个请求付一次。

**证据。** fit 的分支 `exp/blocking-quota`（未合并；报告和数据在 `bench/results/exp-blocking-quota/`，CPU profile 在其中的
`profile/`）上做了一个实验：h2c 代理走 fit 自己写的一个最简阻塞 client，它在 handler 的协程上通过 Go 的 netpoller 读写上游连接，
与 fit 经由 fib client 的异步路径对比。在基准机上（fitbench `proxy` 场景，h2c，server 7 个 CPU，3 轮交替），阻塞路径每秒 250.2k
请求，异步路径 235.0k（+6.3%，达到 nginx 253.5k 的 98.7%），每个请求 CPU 23.2 µs，对比 25.9。这条路走不通：它阻塞 handler 的
协程，放弃了 drain、取消和流式转发；但它显示了异步路径付出的代价。

对 fit server 做 `perf record`（`-F 1999 -g`，在计量区间内采 5 秒，两种各两次），每个采样记到离叶子最近的 fit 或 fib 帧上，只计
用户态采样（内核时间两者相同，每个请求 14.0 对 14.1 µs），单位为每个请求 µs：

| | 异步 | 阻塞 | 异步 − 阻塞 |
| --- | ---: | ---: | ---: |
| 上游 client：fib 的 `Client` / fit 自己的，含 Go 的网络读写 | 2.93 | 2.81 | +0.12 |
| fib 引擎处理连接（poller、读轮次、发送队列、bufferpool、系统调用封装） | 3.21 | 1.57 | **+1.65** |
| fib 的 HTTP/2 server 和 `Context` | 1.95 | 1.57 | +0.38 |
| fit 的 exchange | 0.46 | 0.18 | +0.28 |
| GC，后台 | 0.59 | 0.31 | +0.28 |
| fib 的 taskpool | 0.99 | 0.86 | +0.12 |
| 合计 | 26.11 | 23.40 | +2.71 |

所以差距不在 client 自己的解析和组帧，而在引擎对上游连接的处理。阻塞路径下 fib 引擎只服务客户端连接；异步路径多出的 1.65 µs
中，最大的几部分是 poller 的等待和重新注册（`epollWaiter.wait` +0.14、`pollEvents` +0.12、`runLoop` +0.09、relay 的回调 +0.05、
`arm` +0.04），系统调用封装（`rawSockIO6` +0.29、`sys.Read`/`Write` +0.10，阻塞路径改在 Go 自己的封装里付这部分），读缓冲
（`bufferpool.Get` +0.11、`Put` +0.08），uncork（+0.14，加上 `flushLocked` +0.03），以及读轮次本身（`process` +0.06、
`readLoop` +0.05）。差距的其余部分在 fit（它的 exchange 和分配），由 fit 自己削减。

A21 已经试过唤醒后让 relay 保持注册，省了调用但没有提高吞吐；本请求针对的是每一轮周围的工作，而不是让事件循环挂起的那些调用。

**请求。** 让客户端连接为每个响应运行的读轮次更便宜；以下哪些值得做，由 fib 测量决定：

- **客户端连接的读轮次不 cork**，或者在这一轮里没有发送时 cork 不花代价：`uncork` 可以不加锁先看出这一点，再决定是否合并和 flush。
- **读缓冲不走池的借还**：缓冲不会活过这一轮（`OnData` 的数据只在调用期间有效），所以由 worker 持有一个缓冲，或者每个 poller 一
  组，就足够了，也不会增加空闲连接的内存。
- **短响应少几次交接**：例如 poller 发现多个客户端连接就绪时一起交给一个 worker，或者 worker 结束一个客户端读轮次后，在挂起前先
  看看有没有下一个就绪的客户端连接。

**fit 如何使用。** fit 不需要改动。用 `bench/ab.sh -scenarios proxy` 在 HTTP/1.1 和 h2c 上按每个请求的 CPU 衡量；以上 profile 是
基线，目标是拿回 1.65 µs 中的大部分。

## A40：响应未写完响应体就结束时中止其响应体过滤器；过滤器之后不再给出长度

**状态。** 由 fit 的会话实现，为 `3998953`，自 2026-10-23 起在 fib 的 `main` 上，其后还有一处在 I77 中遇到的修复
`b5d406a`：请求体仍在到达、剩余部分多于服务器愿意丢弃的量时，发出的完整响应声明 Connection: close。
随 v1.6.0 发布，fit 使用该版本。

**问题。** fib 的响应体过滤器（A27）有两处缺口，I67 的缓存遇到了它们：缓存在响应经过一个不改动
内容的过滤器时保存一份副本。

1. 响应在响应体结束之前就结束时，其 `BodyFilter` 被悄无声息地丢弃：客户端中途离开或写入失败的
   响应、被持有后取消的响应、HEAD 的响应，以及在 `Close` 失败的过滤器之后的那些过滤器。持有资源
   的过滤器因此无法归还：缓存的响应体副本计入所有副本的总量上限，compress 则持有在途名额和编码器。
   compress 原先在 `OnCancel` 中归还名额，而写入失败的响应一旦写完，`OnCancel` 就不会再运行：
   这样的响应累计 1024 个（`max_in_flight`）之后，就再也不会压缩任何响应。
2. 在某个已返回过滤器的钩子之后运行的钩子，拿到的 `Content-Length` 是经过该过滤器之前的响应体
   长度；对于整体发送的响应，fib 还会为每个钩子重新加上它。排在 compress 之后的缓存把它当作自己
   看到的响应体的长度，发现压缩后的响应体比它短，于是什么也不保存。

**证据。** I67 的 `TestClientGone`：流式响应中途客户端离开后，缓存的上限永久占用 2 KB，HTTP/1.1
和 h2c 都是如此，因为 `Close` 和 `OnCancel` 都没有到来。`TestCompressed`：在 compress 之后，
从未保存过任何压缩后的变体。

**实现。** `BodyAborter` 是 `BodyFilter` 的可选接口：当 `Context` 结束时仍有未关闭的过滤器
（在 `Context.ended` 中，因此适用于各种流）、HEAD 的响应丢弃其过滤器时，以及 `Close` 失败的
过滤器之后的那些过滤器，都会以原因调用一次 `Abort(err)`，代替 `Close`。一旦某个钩子返回了过滤器，
其后的钩子就拿不到 `Content-Length`。fib 的 `TestBodyFilterAbort` 和
`TestBodyFilterLengthAfterFilter` 在 HTTP/1.1 和 HTTP/2 上覆盖了这两点。

**fit 如何使用。** 缓存的副本和 compress 的过滤器实现 `Abort`，归还其持有的资源；compress 不再
注册 `OnCancel`。缓存把拿到的 `Content-Length` 当作它所复制的响应体的长度。

## A41：异步钩子运行期间压住响应头

**状态。** 2026-10-24 按建议实现，fib `76c0275`，自 2026-10-24 起在 fib 的 `main` 上。一处不同：`Upgrade`、`Connect` 的响应立即发出，
不经过这个钩子。Proxy-Wasm 的响应回调原先有待它实现（[决策 018](decisions/018-proxy-wasm-subset.zh-CN.md)），借助 A46，fit 自 I81 起
按下文实现了它们（[决策 023](decisions/023-proxy-wasm-response.zh-CN.md)），只是响应开始后发送的本地响应会让响应失败，因为状态码已经定下。

**问题。** 有的中间件需要根据无法在确定响应头的 goroutine 上完成的工作来决定响应头，例如 WebAssembly 插件的
`proxy_on_response_headers`（fit 在自己的池上运行它，见规则 4），而它没有办法压住响应头。`OnHeader`、`OnBodyFilter`
和 `OnResponse` 都在确定响应头的地方运行，必须当场决定；`OnResponse` 会暂存响应，但要整体暂存、没有上限，而且只到处理器
完成为止。

**证据。** I72：Proxy-Wasm Go SDK 的 `http_headers` 示例在 `OnHttpResponseHeaders` 中添加响应头，fit 无法在不让插件在
fib worker 上运行的前提下调用它。

**建议的 API。** `Context.OnHeaderAsync(fn func(status int, header http.Header, resume func(err error)))`：fn 在响应头
本应确定的地方运行，在 `resume` 被调用（可以来自任何 goroutine）之前响应头不会发出；以 nil 调用时，响应头按 fn 的调用者
留下的样子发出，以错误调用时响应失败。在此期间处理器写出的内容被暂存：整体响应原样暂存，流式响应体在响应的 64 KB 上限内
暂存，超过时 `WriteNoWait` 报告超限，`OnWritable` 等待恢复和排空，`Write` 像面对慢客户端那样等待。期间客户端离开的响应
按被中断处理，此后 `resume` 不起作用。

**fit 如何使用。** wasm 中间件为 Proxy-Wasm 插件注册它，把 `proxy_on_response_headers` 连同状态码和响应头交给池，在调用
返回、插件的修改完成后恢复；插件发送的本地响应替换原响应。

## A42：HTTP/3 服务端把一轮的数据报一次批量发出

**状态。** 已完成（fib `main` 上的 `a34c5db`），由 fit 的会话实现。压测机上 A、B 交替 3 轮、每轮
10 秒：cache-hot 走 HTTP/3 从每秒 30.8 万提高到 63.9 万请求，每个请求的 CPU 从 22.4 µs 降到 10.8 µs，全部来自 GSO
（关掉 GSO 后提升消失）。fixed 走 HTTP/3 在四次比较中都低 2-4%，原因是压测机的噪声，不是这次改动：fixed 每轮只有一个数据报，批量路径根本不会执行。
带 `a34c5db` 的 fixed perf 剖析中，`SendBatch` 和 sendmmsg 下没有一个采样，`syscall.Sendto` 下有 7,738 个，占 fit CPU 的
7.4%，与没有这次改动时相同。压测机空闲时（2026-10-24，http/1.1 在前），fixed 走 HTTP/3 是每秒 180.0 万对 179.8 万
请求，−0.1%，两边每个请求都是 3.77 µs；同时 cache-hot 提高 107%。压测机繁忙时会掉到 110-160 万、每个请求 4.1-4.5 µs，
而且两个版本都会掉：交换 A、B 后，“回退”就跑到了另一边。fib 不需要修改。fitbench 只用 `-protocols h3` 时，会在 fit 的
UDP 监听就绪之前检查 h3 监听，每次都失败，所以要把 http/1.1 放在前面。

**问题。** `quic.sendDatagrams` 在一轮要发多个数据报、且其 `PacketConn` 是 `quic.BatchSender` 时，用一次调用发出；
`fib.Connection.SendBatch` 用一次系统调用发出一批（sendmmsg，能用时带 UDP GSO）。HTTP/3 客户端交给 quic 的是它的
`fib.Connection`，所以用上了这一点；服务端交给 quic 的是 `http3.peerPath`，只有 `Send`，于是服务端发出的每个数据报
都是一次单独的系统调用，也各自单独走一遍内核的 UDP 和 netfilter 路径。

**证据。** [v1.0.0 报告](https://github.com/lesismal/fit/blob/3658ae9/bench/results/v1.0.0-report-2026-10-23.md)：fit 的 cache-hot 走 HTTP/3（响应 4 KiB）每秒
30.9 万个请求，nginx 49.0 万；每个请求 22.4 µs，nginx 14.1 µs。在压测机上对这一轮做的 perf 剖析里，fit 63.5% 的 CPU
在 `syscall.Sendto` 之下，调用路径是 `quic.sendDatagrams` → `http3.(*peerPath).Send`，大部分在内核里
（`udp_sendmsg` 52%，netfilter 10%）。fixed 走 HTTP/3 时这部分占 7%，proxy 走 HTTP/3 时更多。

**建议的 API。** 不需要新 API：`func (p *peerPath) SendBatch(datagrams [][]byte) error { return
p.peer.c.SendBatch(datagrams) }`，让服务端的路径成为 `quic.BatchSender`。

**fit 如何使用。** 原样使用：每个 HTTP/3 监听都经由它发送。fit 在改动前后测量 HTTP/3 上的 cache-hot、fixed 和 proxy。

## A43：减少 TLS 上 HTTP/2 的唤醒和协程切换

**状态。** 已完成（fib `main` 上的 `5a02929`），由 fit 的会话实现：连接 cork 期间发送的明文由 TLS
layer 留着，cork 解除时合成一个 record 加密（`fib.CorkHolder`），而此前每个 frame 都在连接的锁下单独加密成一个 record。
压测机上 A、B 交替 3 轮、每轮 10 秒：fixed 走 h2 从每秒 164 万提高到 352 万请求（h2c 为 382 万），每个请求的 CPU 从
3.65 µs 降到 1.97 µs；cache-hot 走 h2 提高 6.4%；h2c 不变。

**问题。** 在 TLS 上，一个连接的工作在协程之间转手的次数远多于不加 TLS 时。fit 的 fixed 走 h2 只有 h2c 速率的 43%，
每个请求 3.80 µs，h2c 是 1.83 µs，而且 7 个 CPU 里有 0.8 个闲着。开销不在加解密：AES-GCM 只占约 1% 的 CPU。

**证据。** v1.0.0 报告的结果，以及在压测机上对两者各 5 秒的 perf 统计：

| | h2（TLS） | h2c |
| --- | ---: | ---: |
| 请求数 | 810 万 | 1,920 万 |
| 每个请求的系统调用 | 0.39 | 0.11 |
| 每个请求的 `epoll_pwait` | 0.13 | 0.013 |
| 上下文切换 | 68,453 | 1,826 |
| CPU 迁移 | 1,503 | 2 |

剖析显示写入经由 `http.(*h2Flusher).RunTask`（占 CPU 28%）而不是直接写，`Context.writeResponse` 里还有一些自旋
（`runtime.procyield`）。nginx 的 h2 反而比 h2c 快。

**建议的 API。** 不需要新 API：让 fib 的 TLS 上的 h2 像 h2c 那样写入和刷新——能直接写时由应答的协程直接写，让一轮响应
一次写出，不另起任务，每个请求唤醒 poller 的次数不多于不加 TLS 时。

**fit 如何使用。** 原样使用：每个 h2 监听。fit 在改动前后测量 h2 上的 fixed、static 和 proxy。

## A44：客户端超过 `MaxPendingPerHost` 拒绝请求时不再遍历队列

**状态。** 已完成（fib `main` 上的 `ee1a859`），由 fit 的会话实现；fit 已使用该版本。在压测机上，fit 的代理走 h2c、
256×32（8,192 个请求在途，`max_conns` 4,096，`max_pending` 1,024）从只有 503 提高到每秒 19.4 万个成功响应；在压测
工具的默认负载下，proxy 走 HTTP/1.1 和 h2c 不变（`bench/ab.sh`，在噪声范围内）。

**问题。** 超过 `MaxPendingPerHost` 后，`overPendingLocked` 清点等待中的请求时先剔除已经结束的（`pendingLocked`），
在客户端唯一的锁内遍历整个主机队列（约 `MaxPendingPerHost` 个请求），而且每拒绝一个请求都要做一次。超过上限后几乎
每个请求都被拒绝，而客户端引擎的事件循环处理连接的响应（`release`）和拨号结果（`dialed`）时也要拿这把锁。锁几乎
一直被占着；事件循环排在成千上万个 `enqueue` 中的 goroutine 之后，响应不再返回，拨号超时，在途的上游请求冻结不动。

**证据。** [2026-10-23 的扫描](https://github.com/lesismal/fit/blob/3658ae9/bench/results/sweep-report-2026-10-23.zh-CN.md)：fit 的代理走 h2c 在 256×32 只有每秒
7.7 万个成功响应（nginx 20.6 万），1,024×32 时 1.9 万，4,096×32 起为零。在压测机上重跑并在第 22 秒抓取 goroutine 栈：
256×32 时 3,071 个 goroutine 在 `Client.enqueue` 等 `Client.mu`，一个事件循环在 `Client.dialed` 里也在等它；
4,096×32 时是 13,957 个和两个事件循环。fit 的 `fit_upstream_requests_in_flight` 在 20 秒内停在 8,182 和 19,080，
没有一个响应是 2xx。

**建议的 API。** 不需要新 API：主机记下在等待中结束的请求数（`hostPool.ended`，通过 `clientRequest.waitingIn`，由请求
结束和离开队列两者中先发生的一方结算一次），以常数时间检查上限；只有队列大半已结束时才清扫一次。

**fit 如何使用。** 原样使用：每个配置了 `max_pending` 的 HTTP/1.1 上游。fit 还在连接池已持有 `max_conns` 加
`max_pending` 个请求时，不构造请求直接回答 503（`upstream.Pool.Saturated`），拒绝一个请求只需读一个计数器。

## A45：读取中途停下的一轮收到对端的结束时，不再当作数据已读完

**状态。** 已完成（fib `main` 上的 `9f99ea5`），由 fit 的会话修复。

**问题。** 一轮读取连同对端的结束一起被交付（A33 记下的欠账，或新到的结束）时，可能在 socket 读空之前就停下：处理
函数在 `OnData` 里暂停了读取（中继为另一侧的积压这样做），或连接自己的写预算满了。随后这一轮根据读取*之后*是否仍在
暂停来决定如何处理结束：如果暂停已在另一个 goroutine 上解除，或者停下是因为写预算（并不设置暂停），就以 `io.EOF`
关闭连接，对端已发送、仍在 socket 里的数据随之丢失。

**证据。** fit 的 `TestTCPHangUp/upstream`（后端写 1 MB 后关闭）在 CI 的 macOS 上失败了两次（运行 39776469190 和
40042387673）：客户端读到 606,208 字节后就遇到了结束。macOS 的回环上客户端取走 fit 输出的速度很快，l4 中继对上游
的暂停在同一轮内就被解除。fib 的 `TestStalledRoundThenHangup`（对端发送 64 KiB 后结束，回显超过 16 KiB 的水位）
在 Linux 上不修复时每次都丢 16 KiB。

**实现。** 这一轮记下自己的读取中途停下了（`roundStalled`），然后像暂停时一样把结束记为欠账，不管此时是否仍在暂停：
之后交回的读取先读完剩下的数据，再处理结束。

**fit 如何使用。** 原样使用：`l4` 中继，以及任何暂停读取或有写水位的连接。

## A46：在别处完成工作的响应体过滤器

**状态。** 2026-10-25 由 fit 的会话实现，已在 fib 的 `main`（`dbe5c274`）。Proxy-Wasm 的响应 body 和
trailer 回调（I81，[决策 023](decisions/023-proxy-wasm-response.zh-CN.md)）有待它。

**问题。** 有的中间件需要用不能在写响应的 goroutine 上完成的工作来转换响应体，例如 WebAssembly 插件的 `proxy_on_response_body`
（fit 在自己的池上运行它，见规则 4），却没有可用的过滤器。`OnBodyFilter` 的过滤器在写 body 的 goroutine 上运行，并在那里返回输出，
它的 `Close` 也必须在那里返回 body 的结尾。A41 能在别处调用期间压住响应头，但压不住 body，也压不住它的结束。

**证据。** I81：Proxy-Wasm Go SDK 的 `http_body` 示例持有响应 body 直到结束再替换它；fit 无法在不让插件在 fib worker 上运行的前提下
调用它，也无法在结束时调用 `proxy_on_response_trailers`。

**实现。** `Context.OnBodyFilterAsync(fn func(status int, header http.Header) AsyncBodyFilter)` 注册一个在 `OnBodyFilter` 钩子位置
运行的钩子。过滤器的 `FilterAsync(p []byte, end bool, trailer http.Header, done func(out []byte, err error))` 拿到 body 的每一部分，
从任何 goroutine 调用 `done`，给出替代它发出的内容，或者给出让响应失败的错误；在此之前 `p` 归过滤器所有，`out` 在 `done` 返回前
被发出或复制。一次只有一个调用。期间写入的内容最多暂存 64 KB，整体交给下一次调用：超过时 `Write` 等待，`WriteNoWait` 报告超限，
`OnWritable` 在暂存的部分经过过滤器、客户端取走其输出后回调；过滤器的输出同样按客户端的速度发送，下一次调用等它。`Finish` 把结束
交给最后一次调用（`end` 为 true），它拿到可修改、不为 nil 的 trailer；在它返回前响应被持有，handler 没有 finish 就返回时，最后一个
持有被移交给它。整体发送的响应，或被 `OnHeaderAsync` 整体压住的响应，在一次调用里过滤。注册在它内侧的同步过滤器在写入时运行，
外侧的在它的输出上运行。过滤器实现 `BodyAborter` 时，响应没结束 body 就结束会中止它。测试覆盖 HTTP/1.1 和 HTTP/2 上各种写响应的
方式，调用稍后返回、就地返回或持有 body 到结束，并与 `OnHeaderAsync` 和两侧的同步过滤器一起使用；还有 HEAD、调用失败、`Write`
等待时客户端离开以及过滤器为 nil。竞态检测、linux、darwin、windows 和 freebsd 的 vet 与 staticcheck 都通过。

**fit 如何使用。** wasm 中间件为导出了 `proxy_on_response_body` 或 `proxy_on_response_trailers` 的 Proxy-Wasm 插件注册它：每次调用
排到持有该请求上下文的插件实例上，用插件持有的内容运行 `proxy_on_response_body`，最后一次调用带着 trailer 运行
`proxy_on_response_trailers`，然后关闭上下文。
## A47：流式请求体转交时不必要的拷贝

**状态。** 2026-10-25 由 fit 的会话实现，已在 fib 的 `main`（`309a6cd4`）。

**问题。** 代理上传时，fit 把对端到达的 body 分片交给 fib 的 `BodyStream`，由它交给上游，两步之间有两处拷贝。
回调还忙时的 `BodyStream`（在 HTTP/2 上就是每一片交给 handler 池的时候）用 append 把这段时间到达的数据缓冲到自己的
缓冲区里：从零开始、一次增长一次拷贝，交出去后再留给垃圾回收。`Pusher.Write` 则在发送前把每一片都拷进自己的队列，
即使队列是空的、这一片本来可以直接发出去。

**证据。** 基准机上 fit 的 W4 HTTP/2 代理上传里，`BodyStream.push` 的拷贝占 fit CPU 的 20%。去掉这两处拷贝后，1 MiB
上传从每秒 2748 个请求升到 4742（+73%），最慢响应从 200ms 降到 62ms，HTTP/1.1 上传从 6957 升到 8300（+18%），峰值
内存更低。

**实现。** `BodyStream` 的缓冲区来自 `bufferpool`；交出去的那块作为下一个缓冲区或备用块回来，body 结束后两块都还给池。
`Pusher.Write` 前面没人等的那一片直接进有空间的 sink，只有必须等待时才拷贝。

**fit 如何使用。** 原样使用：`handler/proxy` 的上传路径，它把流式的 body 转交出去而不聚合成整包。

## A48：整份响应的 body 取自缓冲池，已是字节的请求体不再拷贝

**状态。** 2026-10-31 由 fit 的会话实现，已在 fib 的 `main`（`46827ea7`）；HTTP/2 还回缓冲在 `d3707ca7`。

**问题。** fib `861689c9` 让连接的读轮次从 16 KiB 开始，fit 的 W1 内存因此减半，垃圾回收据以定步调的堆也随之减小：
分配速度同为每秒 1.06 GB，回收次数却多了 56%，W1 的 HTTP/1.1 慢了 3%。一个代理请求最大的几处分配中有两处是 fib
客户端只用片刻的拷贝：整份到达的响应把 body 从解析器的缓冲拷进一块同样大小的新缓冲，占分配字节的 18%；已经是字节的
请求体在组帧前先被读进一份副本，占 9%。

**证据。** 基准机上 W1 在 500 在途、HTTP/1.1，改动前后对照 fit 的运行时计数：分配从每秒 1,064 MB 降到 776 MB，回收
次数少 29%，回收占用的 CPU 从 2.63% 降到 1.84%，吞吐 +3.0%（每请求 27.25 降到 26.53 µs），峰值内存从 57-65 降到
55-60 MiB。

**实现。** 整份到达、body 不超过 4 KiB 的响应把 body 拷进 `bufferpool` 的缓冲。`ClientResponse.TakeBody` 连同所有权交出
body，并说明它是否是这样的缓冲；`Context.WriteResponseOwned` 发送来自缓冲池的 body，发送路径用完后还回池里，发送路径
按引用持有 body 时（超过 4 KiB 的 HTTP/1，以及 HTTP/2 被窗口挡住的尾部）则交给垃圾回收。`BodyBytes` 仍保证交出的字节永不复用。`Body` 为
`NewBufferedBody` 的请求直接取用其字节：HTTP/1 当即把它们拷进请求的帧里，HTTP/2 在发送前拷贝一份。

**fit 如何使用。** `handler/proxy` 把客户端整份到达的请求体作为 `Context.Body` 的 `NewBufferedBody` 发往上游，这些字节在
交换结束前保持不变；整份到达的上游响应用 `TakeBody` 取出，body 来自缓冲池时用 `WriteResponseOwned` 转发。

**HTTP/2，`d3707ca7`。** `46827ea7` 把 HTTP/2 响应的池化 body 交给垃圾回收，因为 stream 按引用持有 body。每一块都是从池里取出
却不还回的缓冲，1 KiB 那一档因此被取空，下一次取这个大小的 `Get` 只能新分配：W1 走 h2 时每个请求一次分配，剖析把它记在 fib
给请求组帧的地方。HTTP/2 分帧时会拷贝 body，所以 stream 在 `respond` 返回前就把缓冲还回池里；只有被窗口挡住的尾部仍交给垃圾
回收（stream 正好 1024 字节，没有地方记住这块缓冲）。W1 在 500 在途、走 h2，对照 `6bd106d9`：+1.3%（每请求 29.00 降到
28.63 µs），分配从每秒 850 降到 677 MB，回收次数少 25%，回收占用的 CPU 从 1.65% 降到 1.36%；HTTP/1.1 在噪声内。

## A49：在监听 socket 上朝某地址打开 UDP 对端

**状态。** 已完成，fib `96a2331`，自 `80c7f76` 起在 fib 的 `main` 上，fit 在 I108 中升级到该版本。本条目的早先版本写的是 fib 本地 `main` 上的提交 `4febcdf5` 和下面建议的异步 API；那个提交从未推送，实际实现的是“实现”一段中的同步 API。

**问题。** UDP 监听的对端要先发来一个数据报才成为连接，而拨出的 UDP 连接有自己的 socket，也就有自己的本地端口。打洞需要
的正相反：一个只绑定一次的 socket，它的端口已被 NAT 映射、也被 STUN 服务器看到过，先向一个还没发过任何东西的地址发送，
再在同一端口上收到对方的数据。TURN 服务器对每个 allocation 的中继地址也有同样的需要：它要向客户端许可的对端发送，不论
对端是否先发过。fib 的 API 做不到这两点：第二个 socket 就是第二个映射，它打出的洞不是告诉对端的那个。

**依据。** ICE（RFC 8445 第 7.2 节）从每个候选的 base，也就是收集该候选的那个 socket，发出连通性检查；TURN（RFC 8656
第 11 节）从 allocation 的中继传输地址转发 Send indication 的数据。

**建议的 API。** 在 binding 的某个 UDP socket 上朝一个地址打开对端：

```go
// OpenUDPPeer 返回 binding i 的 UDP socket（绑定在 local 上的那个；local 无效时为该 binding 唯一的那个）上
// addr 的连接；若该地址还没发来过数据，就用 handler 打开它。之后它的数据报像其他对端一样交给它，它的
// Send 从监听 socket 发出。
func (e *Engine) OpenUDPPeer(i int, local, addr netip.AddrPort, handler Handler) (*Connection, error)
```

这个对端与监听的其他对端一样：binding 的空闲超时、`SetUDPIdleTimeout`、`NotifyWritable` 和 `OnClose` 都适用；该地址已有
对端时原样返回。打开时不阻塞，也不发送任何东西。

**实现。** `Engine.OpenUDPPeer(binding int, addr netip.AddrPort) (*Connection, error)`（fib `96a2331`，
`udp_peer.go`）：这个对端得到 binding 的 handler 和它的 `OnOpen`，就像它先发过数据一样，开在该 binding 地址的第一个 socket
上；开启 `ReusePort` 时，它的应答可能落到另一个 socket 上，那个 socket 会为同一地址再开一个连接，所以打开对端的协议按地址
而不是按连接记录对端，fib 的 `webrtc` 和 `turnserver` 都是这样做的。它会等待 socket 的事件循环，所以不能在该循环运行的
回调里调用（UDP 对端的 `OnOpen`、`OnHangup`）；引擎停止或 binding 排空后返回 `net.ErrClosed`，索引不是 UDP binding 或地址
族不对时返回错误。像 `turnserver` 那样在 worker 上的 `OnData` 中调用时，这个等待违反了 fit 的规则 1；
[A56](#a56从不让-worker-等待的-turn-服务器) 要求 TURN 服务器不再等待。

**fit 的用法。** 通过 fib 的 `webrtc` 各包：`webrtc/turn/turnserver` 用它从每个 allocation 的中继地址向被许可的对端发送
（I87，[决定 029](decisions/029-webrtc.zh-CN.md)），`webrtc/ice` 从收集候选的 socket 发出检查。

## A50：QUIC DATAGRAM 帧

**状态。** 2026-11-01 提出，用于 fit 的 P2P 里程碑（[决定 025](decisions/025-p2p.zh-CN.md)）。

**问题。** 经 QUIC 承载的 UDP 流必须保持不可靠、无序：放在 stream 上时，丢一个包会卡住其后所有数据报，而对游戏或语音
通话来说，迟到的数据报比丢掉的更糟。fib 的 `quic` 没有 DATAGRAM 帧（RFC 9221）。

**建议的 API。** `Config.MaxDatagramFrameSize` 通告 `max_datagram_frame_size` 传输参数；`Conn.SendDatagram(b []byte) error`
排入一个 DATAGRAM 帧，它从不重传，超出对端上限或连接窗口时丢弃（并返回错误）；带 `OnDatagram(c *Conn, data []byte)` 的
`DatagramHandler` 接收它们。HTTP/3 datagram（RFC 9297）和 CONNECT-UDP（RFC 9298）以后可以建立在它之上。

**fit 的用法。** `p2p` 把每个 UDP 流的数据报作为带 varint 流 ID 前缀的 DATAGRAM 帧发送。


## A51：包设备的描述符在 poller 上读写

**状态。** 2026-11-01 提出，用于 fit 的 VPN 里程碑（[决定 026](decisions/026-vpn.zh-CN.md)）。

**问题。** TUN 设备是一个既不是 fib 接受的、也不是它拨出的 socket 的描述符：Linux 上是 `/dev/net/tun`，macOS 上是
`PF_SYSTEM` socket，FreeBSD 上是 `/dev/tun`。每次读返回一个 IP 包，每次写接收一个，都可以设为非阻塞。fib 没有办法把这样的
描述符放到它的 poller 上，fit 只能在自己的 goroutine 上阻塞在 `read` 里读它，违反规则 1，每个包还要付一次 goroutine 切换。

**依据。** VPN 节点的 overlay 上每个包都要经过设备；wireguard-go 和 Tailscale 在专门的 goroutine 上读设备，并把这次切换列为
它们要用 `IFF_VNET_HDR` offload 批量摊薄的成本。

**建议的 API。** 把调用者打开并设为非阻塞的描述符挂到 engine 上：

```go
// AttachPacketFD 把 fd 放到 engine 的某个 poller 上，作为一个读写保持包边界
// 的连接：每次 OnData 得到一个包，Send 把 data 作为一个包写出。设备拒绝的写
// （EAGAIN 或 ENOBUFS）丢弃该包并计数，而不是排队。此后 fd 归该连接所有，
// Close 时关闭。
func (e *Engine) AttachPacketFD(fd int, maxPacket int, handler Handler) (*Connection, error)
```

它和其他连接一样：`OnClose`、`HoldReads`、统计、engine 的 `Close` 都适用。它没有远端地址。先做 epoll 和 kqueue；Windows 上
Wintun 没有这样的描述符，不需要。

**fit 的用法。** `vpn/tun` 打开设备、完成设置后挂上去；节点的 handler 路由 `OnData` 交给它的每个包，并用 `Send` 发送对端的包。

## A52：WebSocket 连接的底层传输：强制关闭与其地址

**状态。** 2026-11-01 为 llm 的角色提出（[决定 028](decisions/028-llm-and-tunnel.zh-CN.md)）。

**问题。** llm 的互联链路是 fib 的 WebSocket。对端已死，或积压到 ping 等了 30 秒仍没有 pong 的链路，必须立即断开。
`Connection.Close` 发送关闭帧，它排在所有未发送内容之后，所以一个什么也不读的对端，其套接字要等到引擎的写队列上限或内核放弃才关闭。
`websocket.Connection` 没有暴露其下的 `fib.Connection`，没有跳过关闭握手的关闭，拨号方也拿不到对端地址。

**证据。** `llm/internal/wire/conn.go`：监听方用包装 WebSocket 处理器的方式，按对端地址记录其绑定的 `fib.Connection`，
再按升级请求的 `RemoteAddr` 找到链路的连接；拨号方经 `DialerConfig.Dial` 拿到自己的 `fib.Connection`。两者都是符合规则的临时做法，
由本请求取代。

**建议的 API。**

```go
// Abort 立即关闭连接，不做关闭握手，丢弃仍在排队的内容；随后以 err 调用 OnClose。
func (c *Connection) Abort(err error)

// RemoteAddr 返回对端地址，两侧都可用。
func (c *Connection) RemoteAddr() net.Addr
```

**fit 的用法。** `wire.Conn.forceClose` 在两侧都调用 `Abort`，监听方的映射与拨号方的钩子随之去掉。

## A53：WebSocket 连接发送的背压

**状态。** 2026-11-01 为 llm 的角色提出（[决定 028](decisions/028-llm-and-tunnel.zh-CN.md)）。

**问题。** `websocket.Connection.WriteMessage` 把给它的内容全部排队，从不告诉调用方积压了多少，处理器也无法暂停连接的读取。
llm 的 wire 协议用额度窗口（256 KiB）约束每个流，所以链路的队列以其各流的窗口与应答为界，但链路自身的队列与填充它的生产者之间没有联系：
读得慢的对端只能由排在其队列之后的 ping 发现（默认 30 秒），在此之前队列会增长到引擎的 `MaxWriteQueueBytes`。

**证据。** `llm/internal/wire/conn.go` 与 `session.go`：`ConnConfig.SlowTimeout` 关闭 ping 等待过久的链路；
流以额度控制自己的节奏（`Stream.TrySend`、`OnCredit`、`Release`），而调用的应答与推送不等待任何东西。

**建议的 API。** 与 `fibhttp.Context` 对响应提供的相同（A2）：

```go
// WriteMessageNoWait 与 WriteMessage 一样排队一条消息，并报告排队的内容是否已超过连接的上限。
func (c *Connection) WriteMessageNoWait(op Opcode, payload []byte) (above bool, err error)
// OnWritable 在队列降到上限的一半时运行一次 fn，连接结束时以错误运行；已低于时返回 false。
func (c *Connection) OnWritable(fn func(error)) bool
// HoldReads 暂停、恢复读取该连接。
func (c *Connection) HoldReads(hold bool)
```

**fit 的用法。** 超过上限的链路停止向为其供数据的流归还额度，并暂停读取，直到 `OnWritable`；慢连接超时仍作为最后的手段。tunnel 的 Web 代理和控制台 socket 为了能用 `HoldReads` 和 `NotifyOutput`，在 `fib.Connection` 上自己做 WebSocket
分帧（`tunnel/internal/transport/ws`），届时改用 fib 的 `websocket.Connection`。

## A54：引擎 UDP binding 上的原始 QUIC 端点

**状态。** 2026-11-01 为 tunnel 的角色提出（[决定 028](decisions/028-llm-and-tunnel.zh-CN.md)）。

**问题。** tunnel 的链路可以走 QUIC，每条链路一个双向流，使用自己的 ALPN，而不是 HTTP/3。fib 的
`http3.ServerHandler` 只为 HTTP/3 把 QUIC 放到 UDP binding 上；其他协议使用 `http3/quic` 时，调用者必须自己按
connection ID 把每个数据报路由到连接、限制进行中的握手、回收连接及其 ID，并驱动连接的定时器。

**依据。** `tunnel/internal/transport/quic/listen.go` 和 `conn.go` 在引擎的 UDP binding 上做了这些，重复了
`http3.ServerHandler` 的一部分。它们遵守规则，是本请求将取代的临时方案；迁移地址的客户端不会被跟随（其连接空闲超时）。

**建议的 API。** 一个作为 UDP binding 的 `fib.Handler` 的 `quic.Endpoint`，接受 `*tls.Config` 和 `quic.Config`，
用 `OnConn(func(*quic.Conn))` 接收每个连接，客户端用 `Dial(engine, addr, tlsConfig, config, done)`；分流、握手限制、
迁移和定时器都在其中，与 `http3.ServerHandler` 共用。

**fit 如何使用。** tunnel 的 QUIC 监听和拨号只需在其上写几行，并能跟随迁移的客户端。

## A55：经 TCP 和 TLS 的 TURN

**状态。** 2026-11-01 为 fit 的 TURN 服务器提出（[决定 029](decisions/029-webrtc.zh-CN.md)）。已完成：fib `97201b6`，在 fib `main` 的 `84a67f7` 中，fit 在 I111 中升级到该版本。

**问题。** fib 的 `webrtc/turn` 只在 UDP 上讲 TURN：它的 `FiveTuple` 不含传输协议，`turnserver` 监听一个 UDP binding。
所在网络只放行 TCP、或只放行 443 端口上 TLS 的客户端连不到服务器，而 WebRTC 服务运行 TURN 正是为了这些客户端。决定 025
计划了经 UDP 和 TCP 到服务器的 allocation。

**依据。** RFC 8656 第 3.1 节允许客户端经 UDP、TCP 或 (D)TLS 连到服务器，中继始终是 UDP；浏览器会列出
`turn:host?transport=tcp` 和 `turns:host:443` 服务器，coturn 和 pion 两者都支持。

**建议的 API。** 核心在 `FiveTuple` 中带上传输协议（UDP 或字节流），并从字节流中切分 STUN 和 ChannelData（字节流上的
ChannelData 补齐到四字节，RFC 8656 第 12.5 节）；`turnserver` 用同一个 handler 服务 TCP binding，以及经 `fib/tls` 的 TLS
binding：一条连接就是一个客户端，连接关闭时释放其 allocation。字节流缓冲的字节数有上限，第一个 Allocate 之前有空闲超时。

**fit 的用法。** `webrtc.turn.listen` 接受 `tcp` 和带证书的 `tls` 地址，与 listener 相同。

**实际实现。** `FiveTuple.Transport`（UDP 或 TCP，TLS 对核心而言就是 TCP），`FrameLen` 切分字节流上的 STUN 和补齐的 ChannelData，客户端用 `ClientConfig.Stream`；`turnserver.Server.StreamHandler()` 可挂在 `fib.Binding{Network: "tcp"}` 上，或包在 `fibtls.NewServer(cfg, ...)` 里，另有 `ListenTCP`/`ListenTLS`；`Config.MaxStreamMessage`（8 KiB）和 `Config.StreamIdleTimeout`（30s，每次清扫时检查）。一条连接就是一个客户端，连接关闭时释放其 allocation；监听 binding 由调用方移除。

**fit 的使用方式。** `webrtc.turn.listen_tcp` 和 `listen_tls`，各是 listener 引擎上的一个 TCP binding，服务 `StreamHandler()`；TLS 的经 `fibtls.NewServer`，证书来自 `webrtc.turn.tls.certificates`，由 `tlsmgr` 加载，并像 listener 的证书一样被监视；`max_stream_message` 和 `stream_idle_timeout`（I111）。


## A56：从不让 worker 等待的 TURN 服务器

**状态。** 2026-11-01 为 fit 的 TURN 服务器提出（[决定 029](decisions/029-webrtc.zh-CN.md)）。已完成：fib `74100b2`，在 fib `main` 的 `84a67f7` 中，fit 在 I111 中升级到该版本。

**问题。** `turnserver` 在发来请求的数据报的 `OnData` 中、在 fib worker 上运行核心，并在那里用会等待的调用绑定中继地址和
打开对端：Allocate 调用 `AllocateRelay`，后者调用 `Engine.AddBinding`（fib 的 binding 锁，正在移除的 binding 等待其事件循环
时持有它；然后是一次 bind，以及等待事件循环接手新 socket），端口范围中的端口被占用时还会重试；发给尚未先发过数据的对端的
数据报调用 `Engine.OpenUDPPeer`，它等待 socket 的事件循环。忙碌的事件循环或别处正在移除的 binding 会卡住这个 worker，
以及排在所有 allocation 共用的那一把核心互斥锁后面的其他数据报。

**依据。** `80c7f76` 的 `webrtc/turn/turnserver/turnserver.go`：`allocateRelay`（在 `s.mu` 下 `AddBinding`）、`conn`（由
`OnData` 调用的 `flush` 中的 `OpenUDPPeer`）；`relay.Close` 已经因为同样的原因把 `RemoveBinding` 放到了 goroutine 上。

**建议的 API。** 核心异步地请求中继，`AllocateRelay(t, user, ipv6, done func(Relay, error))`，在 `done` 运行时应答
Allocate（期间的重传丢弃）；`turnserver` 在自己的有界池上绑定，或预先绑定几个中继。`OpenUDPPeer` 增加像 `Dial` 那样的
异步形式，或者 `turnserver` 在中继的 socket 上用 `sendto` 发出对端的第一个数据报，由应答打开对端。核心的互斥锁按
allocation 拆分，让一个 allocation 的中继不必等另一个。

**fit 的用法。** 与现在相同，无需改动：为 I87 记录的对规则 1 的违反随之消除。

**实际实现。** `ServerConfig.AllocateRelay(t, user, ipv6, done)` 是异步的；核心可并发使用，每个 allocation 一把锁，表分成 64 个分片；`turnserver` 在自己的 goroutine 上绑定中继地址、打开对端，由有界队列喂给它们（`Config.SetupWorkers`，4，和 `SetupQueue`，1024），队列满时以 508 拒绝 allocation 或丢弃数据报，由 `Server.Unsent()` 计数。

**fit 的使用方式。** 与之前相同，无需改动：I87 记录的对规则 1 的违反已消除。`Unsent()` 即 `fit_turn_dropped_total{reason="unsent"}`（I111）。


## A57：TURN 服务器的限制与钩子

**状态。** 2026-11-01 为 fit 的 TURN 服务器提出（[决定 029](decisions/029-webrtc.zh-CN.md)）。已完成：fib `bf669ce`，在 fib `main` 的 `84a67f7` 中，fit 在 I111 中升级到该版本。

**问题。** 规则 11 要求每种资源都有上限，核心限制了 allocation，但一个 allocation 的 permission 是一个随每次
CreatePermission 增长的 map（每个存活五分钟），channel 随每次 ChannelBind 增长到 4096 个：持有凭据的客户端能让服务器
无上限地占用内存。`PeerAllowed` 只看到地址而看不到端口，所以服务器无法只允许本机地址上的中继端口，而同一服务器的两个
客户端之间的中继需要这样，同时本机的其他端口仍对中继关闭。宿主也只能看到 `OnRelay` 报告的字节数：allocation 建立或
释放时、请求被拒绝时及其原因都没有事件，而指标和日志需要它们。

**依据。** `80c7f76` 的 `webrtc/turn/server.go`：`createPermission` 和 `channelBind` 不计数地加入 `a.perms` 和
`a.chans`；`ServerConfig.PeerAllowed func(netip.Addr) bool`；`OnRelay` 是唯一的钩子。

**建议的 API。** 每个 allocation 的 `ServerConfig.MaxPermissions` 和 `MaxChannels`（以 508 拒绝，RFC 8656 第 9 节允许）；
`SendAllowed func(peer netip.AddrPort) bool`，在向对端中继数据时检查；`OnEvent func(Event)`，带 allocation 的用户和五元组，
用于建立、刷新、释放（及原因：删除、过期、关闭）和拒绝（带错误码），以及认证失败；`Server.Stats()` 给出持有的
allocation、permission 和 channel 数。

**fit 的用法。** `webrtc.turn` 增加 `max_permissions` 和 `max_channels`，默认只允许服务器本机地址上的中继端口，并为每种
事件在 `/metrics` 中计数、在 `webrtc` 日志中记录。

**实际实现。** 每个 allocation 的 `Limits.MaxPermissions` 和 `MaxChannels`，以 508 拒绝；`PeerAllowed func(netip.AddrPort) bool`，CreatePermission 时以端口 0 询问，ChannelBind 和双向中继的每个数据报带对端端口询问，取代了建议的 `SendAllowed`；`Server.HoldsRelay(addr)`；`ServerConfig.OnEvent`（建立、刷新、释放及原因和流量、拒绝及错误码、认证失败）；`Server.Stats()`。

**fit 的使用方式。** `max_permissions` 和 `max_channels`（各 64）；对端检查只在各服务器持有中继的端口（`HoldsRelay`）以及 `allowed_peers` 带端口列出的端口上允许 `relay_ip` 和 `public_ip`，绝不允许 fit 的其他端口；`OnEvent` 计入 `/metrics`，并由 `webrtc` 日志以 debug 级别记录；`Stats()` 作为 gauge（I111）。


## A58：原地修改和排空 TURN 服务器

**状态。** 2026-11-01 为 fit 的 TURN 服务器提出（[决定 029](decisions/029-webrtc.zh-CN.md)）。已完成：fib `496301d`，在 fib `main` 的 `84a67f7` 中，fit 在 I111 中升级到该版本。

**问题。** 配置重载不能丢掉没有改变的东西（决定 028），被移除的服务器应让它的 allocation 自然结束而不是切断。核心的
生存期、nonce 生存期和 allocation 配额在创建时就固定了，`turnserver.Close` 一次释放所有 allocation。fit 通过函数
（`Auth`、`PeerAllowed`、`OnRelay`）替换核心读取的内容而无需重启，但生存期或配额的改变会创建新的服务器，已有的
allocation 随之丢失：浏览器只好重启 ICE。

**依据。** `NewServer` 把 `turn.ServerConfig` 复制进 `Server`；`turnserver` 只有 `Close`，在它和运行之间没有别的状态。

**建议的 API。** `Server.SetLimits(Limits)` 修改生存期和配额，从下一个请求起生效（超过调低后配额的 allocation 保留到结束）；
`Server.Drain()` 以 508 拒绝新的 allocation，让已有的继续刷新直到 `Close`；`turnserver.Shutdown(ctx)` 先排空，等到没有
allocation 或 ctx 结束，再关闭。

**fit 的用法。** 重载时原地修改生存期和配额；地址或 realm 改变时，在旧服务器旁启动新服务器，旧服务器像重载移除的
listener 一样，在 listener 的排空超时内排空。

**实际实现。** `turn.Limits` 嵌入 `ServerConfig`；`Server.SetLimits`、`Limits` 和 `SetAuth`，从下一个请求起生效；`Server.Drain()`，以及 `turnserver.Server.Shutdown(ctx)`：排空，等到没有 allocation 或 ctx 结束，再关闭。

**fit 的使用方式。** 重载时用 `Core().SetLimits` 修改配额、上限和生存期，用 `SetAuth` 修改用户和 secret；地址改变时在旧服务器旁启动新服务器，旧服务器由 `Shutdown` 在 fit run 的 `-shutdown-timeout`（`app.Host.DrainTimeout`）内排空；同一地址上 realm、中继地址、nonce secret 或字节流上限的改变仍先关闭旧服务器，因为两者不能共用一个地址（I111）。


## A59：从指定的本地地址和端口范围发起拨号

**状态。** 2026-11-01 为 fit 的 WebRTC 代理提出（[决定 029](decisions/029-webrtc.zh-CN.md)）。已完成：fib `3655193`，在 fib `main` 的 `84a67f7` 中，fit 在 I111 中升级到该版本；Windows 上的修复 `63b38d4`（从本地端口拨号时跳过在另一地址上被占用的端口）及其测试 `749189e` 在 `9824c68` 中，fit 当前使用该版本。

**问题。** `webrtc.proxy` 为每个对端地址用一个自己的 UDP socket 中继到它的媒体服务器，这个 socket 由
`Engine.DialWithHandler` 打开。内核按路由选本地地址，从整个临时端口范围里选端口。私有网络上的媒体服务器常在
防火墙后，只放行一个源地址和一段端口；多网卡的主机也可能需要中继从媒体服务器所在网络的网卡发出。TURN 服务器
用 `AddBinding` 自己绑定端口范围，但 fit 不能为每个对端的 socket 各开一个 binding。

**依据。** `dialRequest` 只有网络、地址、超时和 handler，没有本地地址；socket 只在 `connect` 时才被绑定。

**建议的 API。** `Engine.DialWith(DialOptions{Network, Addr, LocalAddr netip.AddrPort, PortMin,
PortMax uint16, Timeout, Handler}, done)`：socket 在 connect 前绑定到 `LocalAddr`，端口为 0 且设了范围时从随机起点
依次尝试 `PortMin`-`PortMax`，都像现在的拨号一样在事件循环上完成；所有端口都 `EADDRINUSE` 则拨号失败。

**fit 的用法。** `webrtc.proxy` 增加 `relay_ip`、`port_min` 和 `port_max`，用于它通向媒体服务器的 socket，
就像 `webrtc.turn` 为它的中继地址所做的那样。

**实际实现。** `fib.DialOptions{Network, Addr, Timeout, Handler, LocalAddr netip.AddrPort, PortMin, PortMax uint16}` 和 `Engine.DialWith(opts, done)`：socket 不带 SO_REUSEADDR 绑定，从随机端口开始搜索范围，没有空闲端口时拨号失败，错误匹配 `fib.ErrNoLocalPort`；本地地址决定地址族。

**fit 的使用方式。** `webrtc.proxy.relay_ip`、`port_min` 和 `port_max`，加载配置时检查；每个中继用 `DialWith` 拨向它的媒体服务器，范围内没有空闲端口时丢弃该对端地址，计为 `fit_webrtc_proxy_dropped_total{reason="no_port"}`（I111）。

## A60：代理两个方向都按行转发头部块

**状态。** 2026-11-03 为负载测试计划中 HTTP/1.1 的 API 负载（W1）提出（`bench/results/plan-2026-11-03/report.md`
的“剩下的问题”）。已在 fib 分支 `perf/header-blocks` 上完成；fit 的代理在其分支 `perf/hdr-passthrough` 上使用。
两者都还没有进入 `main`。

**问题。** 经 HTTP/1 代理的请求，其头部要反复建成 map：fib 的 server 把客户端的请求头解析进 `Request.Header`，
代理编辑这个 map，fib 的客户端再收集、排序、写出；读上游响应头时又建一个 map，代理从中删掉逐跳字段，fib 的
server 再收集、排序、写出。在 6 个 CPU、500 并发下，fit 每个请求的用户态是 61k 周期，nginx 是 26k；头部 map、
排序和两次编组约占 fit CPU 的五分之一。

**依据。** W1 在 500 并发下的 pprof（fit 用 fib `3664a1e`）：`fastRequest` 5.5%（其中 `collectRequestFields`
2.5%、`appendCollectedFields` 1.6%），`parseResponseHead` 3.5%，`Proxy.outgoing` 3.4%，`appendResponseHead`
1.8%，`removeHopByHop` 1.2%，大头是 map 遍历、`sortHeaderFields`、哈希和插入。

**建议的 API。** 在 `Context` 上给出请求到达时的字段行；客户端请求接受一段已成帧的字段行作为头部；客户端响应
以字段行代替 map 交出；server 的 `Response` 原样写出字段行。

**实际实现。** `Context.RequestHeaderBlock() string`：HTTP/1 请求到达时的字段行，每行以 CRLF 结束，含 Host，
字段名保持客户端的大小写（HTTP/2、HTTP/3 上以及 fib 交给 `net/http` 解析的请求头为 `""`）；它不随
`Request.Header` 的修改而变。`Client.StartFramed(req, Framing{HeaderBlock []byte, ResponseHeaderBlock bool}, callback)`：
`HeaderBlock` 原样写在请求行、Host 和 Content-Length 之后，代替 `req.Header`，客户端不再加自己的 User-Agent
（块里不能有 Host、Content-Length、Transfer-Encoding、Trailer）；走 HTTP/2、带流式 body 或交给 `net/http` 的
请求则把这些字段加进 `req.Header`。设置 `ResponseHeaderBlock` 时，fib 自己读的响应头以
`ClientResponse.HeaderBlock() string` 给出，每行保持原样，`Header` 为 nil；经 HTTP/2 或交给 `net/http` 的响应仍是
map。`Response.HeaderBlock []byte` 在 HTTP/1 上写在 `Header` 的字段之后，不排序、不检查（不能含成帧字段），
块里有 `Date` 时 fib 不再加自己的；HTTP/2、HTTP/3 上，以及有 `OnHeader` 或 `OnResponse` hook 可能修改头部时，
其字段加进 `Header` 的副本，`OnFinish` hook 只拿到 `Header` 本身。

**fit 的使用方式。** 代理没有被任何中间件包裹的路由（`module.Unwrapped`，由路由编译时调用）从
`RequestHeaderBlock` 转发 HTTP/1 请求的头部（`outgoingFramed`）：不区分大小写地去掉逐跳字段、Connection 点名的
字段、Content-Length、Host 和客户端的转发字段，再追加 `X-Forwarded-For`、`-Proto`、`-Host`、`Forwarded` 以及跟踪的
`Traceparent`；客户端是受信任代理、请求带 Expect 或 Trailer、gRPC 调用和带镜像的路由仍走 map。除 HEAD 外，每个
HTTP/1 客户端的响应都要求上游的响应头按行给出，去掉逐跳字段、Connection 点名的字段和 Content-Length，再加上粘性
cookie，经 `Response.HeaderBlock` 转发；304 和流式 body 回到 map。这些行保持到达时的顺序和大小写，以及上游的 Date。
W1 在 500 并发下的 A/B，各三轮：请求一侧使 HTTP/1.1 的每秒请求数 +5.6%（每请求 24.69 降到 23.37 µs），响应一侧
再 +4.0%（23.37 降到 22.41）；h2c 在噪声以内，峰值内存不变。


## A61：移除 RTSP 服务器的 UDP 绑定

**状态。** 2026-11-03 为 fit 的流媒体角色提出（[决定 030](decisions/030-media-auth-and-pages.zh-CN.md)）。

**问题。** `media/rtsp` 的 `Server.ListenUDP(engine)` 向引擎添加两个 UDP 绑定，即服务器的 RTP 和 RTCP 端口，使 SETUP 可以
使用基于 UDP 的 RTP/AVP。没有任何东西能移除它们：绑定的索引未导出，服务器也没有 `Close`。重载修改 RTSP 监听器的设置时，fit
的流媒体角色会重启该监听器，配置去掉它时会停止它；在共享的引擎上，每一次都会留下两个在进程生命期内一直绑定的套接字，而
下一次在相同端口上的 `ListenUDP` 会失败。

**依据。** `serverUDP` 保存了 `AddBinding` 返回的索引 `rtp` 和 `rtcp`，却没有方法读出它们；`Server` 只有 `ListenUDP` 和
`UDPPorts`，没有结束这些绑定或 UDP 会话路由的方法。

**建议的 API。** `Server.CloseUDP() error`：移除这两个绑定并清除路由，结束经 UDP 建立的会话；之后可以再次调用 `ListenUDP`。
或者让 `ListenUDP` 返回绑定索引，由调用方移除。

**fit 的使用方式。** `streaming.rtsp` 增加 `udp_rtp` 和 `udp_rtcp`（RTP 和 RTCP 端口）；监听器启动时调用 `ListenUDP`，重载重启
它或角色停止时调用 `CloseUDP`。在此之前该监听器只接受基于 TCP 的 RTP：要求 UDP 的 SETUP 得到 `461`，ffmpeg 和多数播放器
随后改用 TCP 重新建立。


## A62：用 splice(2) 搬运代理的 HTTP/1.1 上传

**状态。** 2026-11-04 为 W4 的代理上传提出（`bench/results/plan-2026-11-04b/report.zh-CN.md`，"并发 10,000 时内存的构成、HTTP/1.1 代理上传"）。
已在 fib 的 `main` 的 `60d67aa`（`v1.6.1-0.20261104101432-60d67aa22f8d`）完成；fit 的代理默认使用它，fit 也已移到该提交。

**问题。** 代理的上传两次经过用户态：fib 的 server 把它从客户端的 socket 读出，fib 的 client 再把它写进上游的 socket，
而上游的发送缓冲要用内核先清零的新页。W4（1 MiB 的 POST 经 fit 发给 nginx，HTTP/1.1，100 并发，fit 用六个 CPU）中 fit
每个请求约耗 620 µs CPU，其中 87% 在内核，Caddy 约 588 µs。

**依据。** W4 期间 fit 所在 CPU 上的 perf（fit `ca704c3`，fib `07ee9e0`）：`_copy_to_iter` 22.5%（接收拷贝），
`kernel_init_pages` 17.1%（为上游发送缓冲的页清零，来自 `tcp_sendmsg` → `sk_page_frag_refill`），`_copy_from_iter` 5.0%。

**建议的 API。** 把请求 body 的剩余部分从客户端连接经管道用 `splice(2)` 搬到 fib 的 client 发送它所用的上游连接：仅
Linux，两侧都是明文 HTTP/1.1，body 以 `Content-Length` 定界，其余情况仍走拷贝。

**实际实现。** `fib.Connection.SpliceTo(dst, fib.Splice{Begin, Read, Written}) error` 经池化的管道（128 KiB，每个 poller
最多 256 个），用 `SPLICE_F_NONBLOCK|SPLICE_F_MOVE` 把连接上接下来到达的 n 个字节搬到 `dst` 的 socket，从不等待：从源连接
的下一轮起，它的读交给 splice 而不是 `OnData`；`Begin` 在那一轮里先把要排在前面的内容发给 `dst`，并说明要搬多少字节；
管道满时像 hold 一样暂停源连接的读，`dst` 排空后恢复；不多不少正好搬 n 个字节，其后的字节照常交给 `OnData`。`Read` 在
源一侧结束时得到通知（在最后的字节交出之前），`Written` 在目标一侧结束时得到通知；源一侧失败会关闭 `dst`，目标一侧失败
则让源连接的读保持暂停。在非 Linux 上、对带 TLS 等 layer 的连接、或没有空闲管道时它会拒绝（`ErrSpliceUnsupported`、
`ErrSpliceBusy`），由调用方改用拷贝。`fibhttp.Context.SpliceBody(w *BodyWriter, fallback func()) bool` 把一个以
`Content-Length` 定界的流式 HTTP/1 body 提供给以 `w` 为 body 的请求；fib 的 client 在能 splice 的 HTTP/1 连接上接手——server
已读到的字节先用普通写发出，剩下的不多不少用 splice 搬运——否则在需要 body 之前调用一次 `fallback`，由调用方像以前一样经
`OnBody` 推送。server 像结束自己读完的 body 一样结束它，并解析 pipeline 在其后的请求；上游失败的 body 交还 server，像
没人接管的 body 一样读掉丢弃，超过 256 KB 则关闭连接。splice 的最后一次写出之后到达的响应，client 视为对完整请求的回答，
上游连接因此得以复用。

**fit 的使用方式。** 代理把每个 128 KB 及以上、以长度定界、没有 trailer 的上传交给 `SpliceBody`（`spliceMinBody`），在
fallback 中经它的 `BodyWriter` 推送；代理的 `splice_uploads`（默认 true）可以关闭它。发往 h2、h2c 或 TLS 上游的上传会被
fib 的 client 拒绝，照旧转发。W4 的 A/B，五轮：HTTP/1.1 上传每秒请求数 9.55k → 10.33k（+8.2%，瓶颈从 fit 的六个 CPU 变为
nginx 的四个 CPU），每请求 CPU 616 → 369 µs（-40%），p99 23.6 → 18.3 ms，峰值内存 46 → 29 MiB；h2 上传与下载在噪声以内。
fit 所在 CPU 上的 perf：每请求 `_copy_to_iter` 665k → 36k cycles，`kernel_init_pages` 505k → 60k，`_copy_from_iter`
148k → 26k；fit 每请求 2.95M → 1.74M cycles。W1（`api`）与 W0（`fixed`）在 HTTP/1.1、500 并发下均在噪声以内（每秒
273k → 273k 与 680k → 681k 请求）。


## A63：多路径 TCP 作为监听器的选项

**状态。** 2026-11-05 为 fit 的监听器提出：客户端地址变化时（如手机在 Wi-Fi 与移动网络之间切换）连接不断。已在 fib 的
`main` 的 `5c0e5b9`（`v1.7.1-0.20261105042253-5c0e5b925fc9`）完成，由 fit 的会话按
[规则 15](rules.zh-CN.md#七fib-满足不了时) 实现；fit 已按[规则 19](rules.zh-CN.md#九fib-版本) 移到该提交。
fib 在 `5c0e5b9` 上的 CI 已通过：所有作业全绿，其中 Windows 作业是重跑后通过的，第一次运行失败于 `webrtc.TestMedia` 的数据竞态（该测试与本次改动无关）。

**问题。** fit 运行的每个 TCP 监听都是普通 TCP：配置的 `listeners`、Admin API、它的控制台与各角色的控制台、流媒体的 RTMP 与
RTSP 监听，以及 TURN 的 TCP 与 TLS 监听。TCP 连接固定在建立它时的地址上：客户端地址一变，连接即断，其上的协议——TLS 与
HTTP、RTMP、RTSP、TURN——都要重新开始。多路径 TCP 让一条连接同时走多条路径，由内核随地址的增减迁移，这正是移动客户端所需；
fit 的用户希望按监听器开启它，其余默认关闭。fit 无法提供：所有监听套接字都由 fib 创建，fit 只给出 binding
（`listener/listener.go` 中的两个 `fib.Binding` 字面量、`admin/server.go`、`app/console/console.go`、
`streaming/listeners.go`、`webrtc/turn/turn.go`），而 `fib.Binding` 说明的是 network、地址与 handler，引擎说明其设置，没有
任何描述监听套接字传输方式的东西。若由 fit 自己打开套接字，监听器就脱离了 fib 的事件循环、poller 与 prefork，还需要 fit 自己的
accept 循环；[规则 1](rules.zh-CN.md#一不阻塞-worker) 两者都不允许。普通 TCP 的默认值不只是一个默认：前一次改动
`24dba02`（"fit 经 net 打开的监听器一律是普通 TCP，绝不使用 MPTCP"）在 CVE-2026-98432 的公开利用出现后确定了它——该漏洞是
内核 IPv4 TCP 握手中的释放后使用，而利用它正需要目标上有 MPTCP 监听；关闭多路径 TCP 从未修复这个内核缺陷，那要由内核来修，而在
未打补丁的内核上设置 `mptcp` 的运维者会承担这一暴露面。

**依据。** 在 Linux 5.6+ 且设置了 `net.mptcp.enabled` 时，`net.ListenConfig.SetMultipathTCP(true)` 得到 MPTCP 监听；自 Go 1.24
起，`net.Listen` 在 `GODEBUG=multipathtcp` 允许处会直接打开这种监听。可见内核的能力只靠 net 包就能用到；而 fib 的 binding 走
fib 自己的监听路径，没有这个选项，`fib.Binding` 也只有 `Network`、`Addr`、`Addrs` 与 `Handler`，其余属于引擎，任何 fit 配置
都无法要求它。在压测机上（Linux 7.0.0-38-generic，16 CPU，`net.mptcp.enabled`=1），要求 MPTCP 的 net 监听与同样拨号的客户端
得到端到端的 MPTCP 连接，即下文测试所用的组合。

**建议的 API。** `Binding.MultipathTCP bool`，位于 `Addrs` 与 `Handler` 之间，默认关闭：设置时，fib 在 Linux 上由内核支持处
以 `IPPROTO_MPTCP` 打开该 binding 的 TCP 监听套接字，不支持处按套接字回退到普通 TCP，与
`net.ListenConfig.SetMultipathTCP(true)` 相同；binding 打开的每个套接字都如此，spread 监听器用来占端口的 claim 套接字、
各 poller 的套接字以及 prefork 子进程的副本也不例外。UDP 与 Unix binding 不受影响，其他平台仍监听普通 TCP，bind、listen 或
`SO_REUSEPORT` 的错误照旧返回。

**实际实现。** 与建议一致：`fib.Binding.MultipathTCP` 位于 `Addrs` 与 `Handler` 之间。设置它的 TCP binding 在 Linux 5.6+ 且
设置了 `net.mptcp.enabled` 处以 `IPPROTO_MPTCP`（262）监听，用于 spread 监听器占端口的 claim 套接字、每个 poller 的套接字
以及 prefork 子进程的副本；任何套接字错误都回退到普通 TCP，与 `SetMultipathTCP(true)` 相同；没有多路径 TCP 的对端在握手时
回退。它默认关闭，因此 binding 就是普通 TCP；UDP 与 Unix binding 不受影响；在 macOS、Windows 与各 BSD 上 binding 监听普通
TCP；bind、listen 与 `SO_REUSEPORT` 的失败照旧返回，绝不吞掉。

**fit 的使用方式。** 七个成员上有 `mptcp` 布尔值：`listeners[]`、`admin`、`admin.console`、角色的 `console`（tunnel 与 LLM
功能的，`config.RoleConsole`，其 `Console()` 把它带给该控制台的 binding，与 `admin.console` 的相同）、`streaming.rtmp`、
`streaming.rtsp` 与 `webrtc.turn`（它的 `listen_tcp` 与 `listen_tls`）。运行图的 `runtime.Listener.MPTCP` 到达
`listener/listener.go` 中的两个 `fib.Binding` 字面量；监听 binding 的身份包含它，所以重载改动它时会建立新的 binding，因为
监听套接字的传输方式一旦建立就固定不变；`udp` 监听器与 HTTP/3 没有该功能，校验会拒绝只在 UDP 监听器上的 `mptcp`，以及没有
`listen_tcp` 或 `listen_tls` 的 TURN 服务器上的 `mptcp`。Admin API 不用 fib 的 binding：它经 `internal/nettcp`（由
`internal/plaintcp` 改名而来）监听，即按配置要求把 `SetMultipathTCP` 打开或关闭的 `net.Listen`，因为 Go 1.24 的 `net.Listen`
会在 `GODEBUG=multipathtcp` 允许处直接打开多路径 TCP 监听，而 fit 只在配置要求时提供它。控制台经 `app/console` 绑定到引擎，
由它传入控制台的 `mptcp`。流媒体 RTMP 与 RTSP 的监听 key 与 TURN 的 static key 包含它，因此改动会重启该监听器，或启动新的
TURN 服务器并排空旧的。tunnel 与 LLM 功能自身的链路监听仍为普通 TCP：它们的 fib binding 有同样的选项，但 fit 的配置在那里
还没有 `mptcp`。没有多路径 TCP 的客户端或对端回退到普通 TCP，也没有要求必须使用它的配置；Unix 套接字与 UDP 没有该功能。
workbench 的引擎菜单会为带它的端口加上标记。


## A64：非分片 MP4 文件的读取器

**状态。** 2026-11-06 为媒体库（I131–I136，[决定 032](decisions/032-media-library.zh-CN.md)）提出。已在 fib 的 `8998d58`
（连同 `ba47477`）完成，由 fit 的会话按 [规则 15](rules.zh-CN.md) 实现，已在 fib 的 `main` 上；fit 已移到该提交
（[规则 19](rules.zh-CN.md)）。

**问题。** 媒体库不转码地把文件作为 HLS 提供（I133），并把它们作为频道播放进 hub（I134）。两者都要求 fit 知道一个文件
中每条轨道的编码，以及每个采样的位置和时间：ffmpeg、编码器和摄像机写出的 MP4 文件是非分片的，采样由 `moov` 中的
采样表描述。一部两小时影片的文件有几十万个采样，`moov` 有几 MB，可能在文件开头也可能在末尾；把文件或其 `moov`
整个读入会违反 [规则 11](rules.zh-CN.md)，而每个采样一个条目会让采样表的内存在读完之前无从得知。

**依据。** fib 的 `media/fmp4` 只读取分片 MP4：`ParseInit` 接收整个放在内存中的 `moov` 并读取每条轨道的采样条目，
`ParseFragment` 读取 `moof` 和 `mdat`；它不读取 `stts`、`ctts`、`stss`、`stsc`、`stsz`/`stz2`、`stco`/`co64` 或
`elst`，并说明它不处理编辑列表。fib 中没有其它代码读取文件的采样表。

**建议的 API。** 在 `media/fmp4` 或新的 `media/mp4` 中提供读取器：通过 `io.ReaderAt` 解析 `moov` 而不读取整个文件，
`moov` 在末尾和开头都能找到，按 `media/av` 的方式给出每条轨道的编码配置（H.264、H.265、AV1；AAC、Opus），按时间和
关键帧查找采样，采样表以紧凑形式保存且其大小在构建之前可知，并做 fuzz。

**实际实现。** 新包 `media/mp4`：`fmp4` 读取内存中的分片，而这里通过 `ReaderAt` 读取文件并保留采样表；两者共用
`fmp4.ParseSampleEntry`，即 `ParseInit` 所用的采样条目读取函数，为此导出。

- `mp4.Open(r io.ReaderAt, size int64) (*File, error)` 读取顶层盒子头直到 `moov`（无论在哪），读取 `moov` 中除采样表
  外的盒子，采样表只读位置和条目数。`File` 有 `Size`、`MajorBrand`、`Timescale`、`Duration`、`MoovOffset`、`MoovSize`
  和 `Tracks []*Track`。`Track` 有 `ID`、`Handler`（`vide`、`soun` 等）、`Language`、`Timescale`、`Duration`、
  `Track *av.Track`（编码、按 `media/av` 形式的 `Config`、画面尺寸、采样率、声道数；其它格式为 `av.CodecUnknown`）、
  `Edits []Edit`、`Shift`（编辑列表加到媒体时间上的量）、`Samples` 和 `Chunks`。`moov` 中有 `mvex` 的文件返回
  `ErrFragmented`；没有 `moov` 或在其之前被截断的返回 `ErrNoMoov`。`File` 不持有读取器的任何东西。
- `Track.TableBytes() int64` 仅凭条目数给出该轨道采样表将占用的内存：`stsz` 大小每个采样 4 字节（`stz2` 为 2 字节，
  所有采样大小相同时为 0），每个 chunk 12 字节，每个 `stts` 条目 16 字节，每个同步采样 4 字节，`ctts` 取每条目 8 字节
  与每采样 4 字节中较小者，再加 `Table` 本身。它绝不超过文件中这些表本身字节数的四倍。
- `Track.ReadTable(r io.ReaderAt) (*Table, error)` 通过 16 KiB 缓冲区一次读完各表，分配 `TableBytes`；它检查 `stts`
  和 `stsc` 覆盖每个采样、每个 chunk 的采样都在文件之内（`ErrSampleData`）。`Table` 不可变，可在多个 goroutine 中使用：
  `Len`、`Sample(i) Sample`（O(log n)）、`Samples(from) iter.Seq2[int, Sample]`（解码顺序，每个采样恒定开销）、
  `IndexAt(d)`、`SeekKeyframe(d)`、`Keyframes`、`Keyframe(k)`、`KeyframeBefore(i)`、`KeyframeAfter(i)`、`Time(ticks)`
  与 `Ticks(d)`（应用编辑列表后的呈现时间线）、`MinCTS`（负的合成偏移所需的解码提前量）、`End`、`DataSize` 和 `Bytes`。
  `Sample` 有 `Offset`、`Size`、`DTS`、`CTS`、`Duration`（轨道时间刻度的 tick）和 `Keyframe`；`ReadSample(r, s, buf)`
  读取其数据。
- 编辑列表：第一个实际播放的编辑之前的空编辑使轨道延后，该编辑的媒体时间在其起点呈现，与 ffmpeg 和播放器一致；之后
  的编辑保留在 `Edits` 中，不应用。
- 上限：`MaxTracks`（256）、`MaxBoxes`（65536 个盒子头）、`MaxSampleEntries`（`stsd` 1 MiB）、`MaxEdits`（1024）和
  `MaxSamples`（2^31-1）；超出时返回 `ErrTooLarge`。文件中的任何内容都不被信任。
- 测试：ffmpeg 7.1 写出的七个文件（`media/mp4/testdata/gen.sh`，连同 ffprobe 的输出共 129 KB）：H.264、H.265 和 AV1
  搭配 AAC 和 Opus，`moov` 在前和在后，B 帧，负的合成偏移（`ctts` 版本 1），以及晚 0.2 秒开始的音频（空编辑）；每条
  轨道的编码、尺寸、采样率、声道数和配置（其 SHA-256 对照 ffprobe 的 `extradata_hash`），以及每个采样的偏移、大小、
  时长、DTS、PTS 和同步标志，在不应用和应用编辑列表时都与 ffprobe 一致；把表改写为 `co64` 和 `stz2` 后同样一致。用每种
  表形式（4、8、16 位的 `stz2`，大小相同，两种版本或没有 `ctts`，`stss` 缺失、为空或乱序，64 位 `mdat`，版本 1 的头和
  编辑列表）构建的文件逐个采样读回，每种查找都对照逐个遍历检查。60 fps 一小时带 B 帧：216,000 个采样，表 1.82 MB
  （每个采样 8.4 字节），`ReadTable` 总共分配 1.86 MB。`FuzzOpen` 在本地运行一分钟无失败，并加入 fib CI 的 fuzz 任务；
  `media/interop` 的 `TestMP4` 让 ffmpeg 写出同样编码组合的十秒文件、两条音轨、44.1 kHz 的 AAC，并在 `media-interop`
  任务中对照 ffprobe 检查。

**fit 的使用方式。** I132 在媒体库的文件池上用 `mp4.Open` 探测每个文件（无论文件多长只读几 KB），得到目录中的时长、
轨道和编码；I133 在文件池上读取某条轨道的采样表之前，先按 `TableBytes` 决定是否放入媒体库有界的表缓存，并在
`SeekKeyframe` 和 `KeyframeAfter` 找到的同步采样处切分 HLS 分片，按 `Samples` 给出的偏移和大小在文件池上读取字节；
`MinCTS` 为负时 MPEG-TS 分片提前 `-MinCTS` 解码。I134 按读取的采样的 `Table.Time` 控制频道节奏。

## A65：被拒绝的协议切换以流式返回

**状态。** 2026-11-05 由 fit 的安全审查提出。已在 fib `main` 的 `4848eeb` 完成；fit 已在 `v1.7.2`（`d6f8809`）上。fit 的临时
做法（upstream 池的客户端把整体缓冲的 body 限制在 64 KiB，超出的被拒升级答 502）随之去掉。

**问题。** 任何客户端都可以向代理路由发送 `Connection: Upgrade` 与 `Upgrade: websocket`。不切换协议的后端照常应答，
body 往往很大，而 fib 的 `Client.StartUpgrade` 在回调前整体等待该应答，上限为 `MaxResponseBodyBytes`：按 fib 默认的
64 MiB，八个这样的请求就占了 fit 1.3 GiB 堆。

**依据。** `StartUpgrade` 调用 `c.do(req, callback, true, handler)`：`whole` 固定为 true，所以 `StreamResponseBody` 及其
阈值对非 101 应答不起作用。

**建议的 API。** `StartUpgrade` 对 101 以外的应答遵从 `StreamResponseBody`（101 没有 body，切换不受影响），或新增这样做的
`StartUpgradeStreamed`。

**实际实现。** 采用第一种建议：`StartUpgrade` 像 `Start` 一样发送请求，101 以外的应答遵从 `StreamResponseBody`、
`StreamResponseBodyThreshold` 与 `StreamUnknownLengthAtHeader`，body 受连接的背压（`StreamResponseBodyBuffer`）读取；101
没有 body，照旧切换；不开流式的客户端仍整体收到所有拒绝应答。没有新增方法：`StartUpgrade` 的文档本来就说这样的应答是
普通响应，设置了 `StreamResponseBody` 的客户端本就要求大 body 流式交付，fib 里另一个调用方
`http3.AltSvcClient.StartUpgrade` 沿用 TCP 客户端的行为。对调用方唯一的变化是：开启 `StreamResponseBody` 时，大的拒绝应答
的 body 要像其他响应一样在回调内取走。

**fit 的使用方式。** 被拒绝的升级交给 relay 构造请求时所用的 exchange：`relay.refused` 把后端、配置与对请求的持有交给它，
把它加入代理的打开中的 exchange（drain 会截断它），再调用普通路径 `exchange.onResponse`：小的拒绝应答整体写出，大的按客户端
积压用 `HoldBody` 带背压流式返回，受响应空闲超时与写停滞超时约束。relay 与它的 exchange 共用一个 `Call` 和一个取消原因，
客户端离开或 drain 会同时放弃握手与拒绝应答的 body。upstream 池的客户端恢复为 fib 默认的 `MaxResponseBodyBytes`：阈值为
64 KiB、未声明长度的 body 从响应头起就流式交付，整体缓冲的 body 本来就不会超过 64 KiB，这个上限只拦下过被拒绝的升级。
临时做法中的池饱和检查与 h2c 处理保留。

## A66：RTSP 的 PLAY 或 RECORD 绑定到会话的流

**状态。** 2026-11-05 由 fit 的安全审查提出。已在 fib `main` 的 `e216884` 完成；fit 已在 `v1.7.2`（`d6f8809`）上。在此之前 fit 的临时做法
（6c4a5937）：`streaming/rtspauth.go` 对 SETUP 授权，按连接记下其 ANNOUNCE 与 SETUP 所指的流（最多 8 个），PLAY 须对其中每一个
都有权限；现已去掉。

**问题。** `media/rtsp` 的服务器播放 SETUP 选定的流（`session.path`），从不与 PLAY 的 URL 比较。按各请求自身 URL 授权的
钩子因此让持有流 `a` 令牌的客户端 SETUP `secret/trackID=0` 后 PLAY `a?token=…`，收到 `secret`。

**依据。** `server.go` 在 `session.afterResponse` 中按 SETUP 的路径订阅；PLAY 与 RECORD 不读取自己的路径。

**建议的 API。** PLAY 与 RECORD 的流路径与会话的不同时答 `455 Method Not Valid in This State`；并提供
`ServerConn.SessionPath() string`，让钩子对会话实际承载的流授权。

**实现情况。** PLAY 与 RECORD 的 URL 指向的流与会话的不同时，在 `OnRequest` 之后答 `455 Method Not Valid in This State`。URL
的流按 SETUP 的方式得出：去掉首尾斜杠的路径，query 末尾的 `/trackID=N` 移到路径上，再去掉末尾的 `trackID=N` 段；主机、端口和
query 不参与比较，路径内部的斜杠参与。因此聚合 URL、带斜杠的 Content-Base、track 的 URL，以及 ffmpeg 带 query 的写法
（`rtsp://h/s?token=T/`、`rtsp://h/s?token=T/trackID=0`）照常播放，录制方 RECORD 其 ANNOUNCE 的 URL；RECORD 某个 track 的 URL
（`rec/streamid=0`）会被拒绝，没有客户端这样发。`ServerConn.SessionPath() (string, bool)` 返回会话的流，没有会话时返回 false
（流的路径可以为空，只靠 `""` 无法区分）；它与 `User()` 一样是原子的。已用 ffmpeg 9.0 经 TCP 和 UDP 推流、播放验证。

**fit 的使用方式。** `rtspAuth.onRequest` 按 `SessionPath()` 检查 PLAY（play）与 RECORD（publish），没有会话时（fib 会答 454）
检查 URL 的流。没有会话时的 SETUP 会建立播放其所指流的会话，需要该流的 play；已有会话时 SETUP 只给会话的流加一个 track，
需要会话的流的 play 或 publish（fib 不说明会话是播放还是录制，其后的 PLAY 或 RECORD 会按其所做的事检查）。按连接记录的流
列表、其 8 个的上限以及为清理它而包装的 `OnClose` 已去掉；Digest 的 `uri` 检查保留。

## A67：单个 HTTP 连接的空闲超时

**状态。** 2026-11-05 由 fit 的安全审查提出。已在 fib `main` 的 `27f5c88` 完成；fit 已在 `v1.7.2`（`d6f8809`）上。

**问题。** 控制台保留空闲连接两分钟。fit 希望尚未发送带会话请求的客户端只被保留几秒，使未认证的连接占不满控制台为数不多
的连接位；而 fib 的 HTTP 处理器自行设置每个连接的读截止时间，处理器拿不到连接去修改它。

**依据。** 基于 `net/http` 的 Admin API 用自己的 `ConnState` 做到了这一点；`fibhttp.Context` 不暴露 `*fib.Connection`，
也没有按连接的空闲超时。

**建议的 API。** `Context.SetIdleTimeout(d time.Duration)`，对请求所在连接生效直到再次设置；或在 `Context` 上暴露连接。

**实际实现。** 与建议一致：`Context.SetIdleTimeout(d time.Duration) error` 在请求所在的连接上代替 `Config.IdleTimeout`
（未设时为 `ReadTimeout`），从本次响应结束起直到某个 handler 再次设置为止，无论 server 是否设了超时都生效；`d` 为 0 或负数时
恢复 server 的设置，响应完成后调用返回 `ErrResponseWritten`。HTTP/1 上它约束等待下一个请求的时间（排在设置它的请求之后的
pipeline 请求照常先处理完）。HTTP/2 和 HTTP/3 上它属于整条连接，任一请求都可以设置：约束连接没有任何流、发出 GOAWAY 之前的时长，
从最后一个流结束算起；h2c 升级保留 HTTP/1 时设的值，QUIC 的 `MaxIdleTimeout` 不变。fib `http` 包之外提供的 `Stream` 通过
`StreamIdleTimeouter` 接收它，`http3` 已实现。不调用它的请求只在选定空闲截止时间处多一次原子读，无分配。

**fit 的使用方式。** 控制台以 5s 的 `IdleTimeout` 提供服务（`console.UnauthIdleTimeout`，与 Admin API 对未认证连接一样），
带有效会话的请求或成功的登录用 `SetIdleTimeout` 把所在连接改为控制台通常的 2 分钟（`console.IdleTimeout`）；登出后改回 5s。

## A68：新轨道沿用同一时钟

**状态。** 2026-11-06 由 I134（频道）提出。已在 fib 的 `826c2c3`（连同 `40b3e2c`、`7fd6218` 与 `a8a475f`）完成，由 fit 的会话按
[规则 15](rules.zh-CN.md) 实现，采用下面第一种方式：每个出口把 `OnTracks` 之后的包与同类（视频，或其余）的最后一个包比较，HLS 在
不连续点前后保持解码时间（`fmp4.Writer.Offset` 与 `Follow`）；fit 已移到该提交。

**问题。** 发布者在流中途更换轨道而时钟继续（`hub.Publisher.SetTracks`，例如频道切到以不同方式编码的条目，或编码改变）时，
各出口会把之后的包挪后，仿佛时钟重新开始了。它们把 `OnTracks` 之后第一个不晚于*任一*轨道最后一个包的包当作新时钟，把之后
的内容移到那个包之后至少 20ms。有 B 帧时，下一个条目最初的画面在上一个条目的音频结束之前解码（早出 B 帧延迟，两个 B 帧为
80ms），于是频道的视频在并未重启的地方跳变约 100ms。

**依据。** `media/flv` 的 `Timeline.Stamp` 在 `Restart` 之后拿包与 `t.last`（两条轨道中最大的时间戳）比较（RTMP 播放与
HTTP-FLV）；`media/srt` 的 `sendPacket` 在 `restart` 之后与 `s.lastDTS`（所有包中最大的）比较；`media/hls` 的 muxer 让下一个
writer 从上一个的 `End()`（最大者）开始；`media/rtsp` 的播放器在 `restart` 时重新定基。在 fit 的 `TestChannelInterop` 中，
由两个 SPS 不同的条目（先 320x240，后 480x270，两个 B 帧）组成的频道，经 RTMP、HTTP-FLV、SRT、RTSP 与 HLS 时视频解码时间
出现 120 到 135ms 的跳变；而轨道相同的条目（不调用 `SetTracks`）没有任何跳变。

**建议的 API。** 二者择一：

1. 出口按轨道检查重启：`OnTracks` 之后的包，只有不晚于其自身轨道的最后时间戳（`lastVideo`、`lastAudio`），或超过上限时，
   才算新时钟。时钟重新开始的重新发布仍能被识别（其视频早于最后的视频），同一时钟上的更换则继续沿用。
2. 或由 hub 区分二者：可选接口 `hub.TracksChanger`（`OnTracksChanged(tracks)`），正在发布的发布者调用 `Publisher.SetTracks`
   时代替 `OnTracks` 调用它，各出口实现它而不 `Restart`；新发布者的 `OnTracks` 保持现有处理。

**fit 的使用方式。** fit 无需改动：`streaming/channels` 在条目的轨道与上一个不同时调用 `SetTracks`，其时间戳本已按轨道连续，
各出口届时原样转发即可。`TestChannelInterop` 与 `TestChannelEgresses` 中对这类条目 0.2s 的容差可收紧到一帧。

## A69：加密的 HLS 分片

**状态。** 2026-11-06 由 I136（加密分片）提出。已在 fib 的 `70a9acc`（连同 `9852eee` 与 `b4a9036`）完成，由 fit 的会话按
[规则 15](rules.zh-CN.md#七fib-满足不了时) 实现，如下文“实际实现”所述；fit 已在该提交上。在此之前，fit 在其 `hls` 处理器中自行加密
频道的分片（`streaming/hlsenc.go`）：复制 Muxer 切出的每个分片，在媒体库的文件池上加密，在 `max_bytes` 之内保留，在媒体播放
列表中改写出密钥标签，并且不提供低延迟 HLS 的部分分片。这一临时方案已移除。

**问题。** `media/hls` 不加密任何内容（其文档写明："Left out: ... and encryption"）。以 RFC 8216 第 4.3.2.4 节的 AES-128
（`METHOD=AES-128`）提供加密的直播流，需要媒体播放列表在开头和每次换密钥处带上 `#EXT-X-KEY` 标签，并避开 `#EXT-X-MAP`
（在它之前的密钥也会加密初始化段），每个分片用当时的密钥、以其媒体序号为 IV 加密。`SegmentWriter` 两者都做不到：它既不知道
是哪路流，也不知道分片的 msn，播放列表又由 Muxer 写出。于是 fit 在 Muxer 之外复制并加密每个分片（每个分片多保留一份），
并在 worker 上为每个请求解析、改写 Muxer 渲染的每个播放列表。部分分片（低延迟 HLS）它根本无法加密，因为部分分片的字节是其
分片的一段切片。

**依据。** fit 的 `streaming/hlsenc.go`：`withKeys` 逐行改写每个媒体播放列表；`serveEncrypted` 自行回答 `seg-*` 名称，取自
`Muxer.File`，在池上加密一份副本并缓存；`keyServed` 在提供密钥前渲染整个播放列表以得知会话和最后的 msn；低延迟 HLS 的标签被
去掉，部分分片被拒绝。

**建议的 API。**

```go
// Encryption encrypts a Muxer's segments with AES-128 (RFC 8216 section 4.3.2.4).
type Encryption struct {
	// Rotation is how many segments one key encrypts; 0 for one key.
	Rotation int
	// Key returns the key of epoch (msn / Rotation) of the Muxer whose
	// session is session, and the URI the playlist names it by, relative
	// to the playlist. Called on the publisher's goroutine, once an epoch.
	Key func(session string, epoch uint64) (key [16]byte, uri string)
}

// in Config:
	// Encryption, when not nil, encrypts every segment as it completes, each
	// part too, with an IV of its msn (a part's, its own explicit IV), and
	// writes #EXT-X-KEY, after any #EXT-X-MAP, wherever the key changes.
	Encryption *Encryption

// in HandlerConfig:
	// Encrypt returns the Encryption of the stream at path, nil for none:
	// asked once a Muxer starts.
	Encrypt func(path string) *Encryption
```

以及 `Muxer.Session()`、`Muxer.Next()`（正在切的分片的 msn），使密钥服务无需渲染播放列表即可检查密钥的会话和周期。

**fit 的使用方式。** `hls` 处理器的 `HandlerConfig.Encrypt` 对设置了 `encrypt` 的频道返回一个按其 `key_rotation` 的 `Encryption`，
其 `Key` 由媒体库的密钥（`medialib.Keys`）派生密钥，并命名为 `key-<session>-<epoch>.key`；密钥 URI 照旧回答，用 `Session` 与
`Next` 检查。`withKeys`、加密分片的缓存和副本都可移除；加密频道的部分分片得以提供。

**实际实现。** 与建议相同，以下几点不同：

- `Key` 还返回错误，错误会结束该流（`EXT-X-ENDLIST`）而不是以明文提供；它在加密运行处调用，不持有 Muxer 的锁，也不在发布者的
  goroutine 上。
- `Encryption.Run func(job func()) bool` 在调用方的池上运行加密；为 nil 或返回 false 时在单独的 goroutine 上运行。分片和部分
  分片加密之后才列出和提供，等待加密的分片超过 4 个时流结束。
- 部分分片用其所属分片的密钥和 IV（媒体序号）单独加密，而不是用自己的 IV：规范没有规定部分分片的 IV，hls.js 取其所属分片的。
  `#EXT-X-KEY` 位于该分片第一个 `#EXT-X-PART` 之前；之后出现的 `#EXT-X-MAP` 之前先加 `METHOD=NONE`，其后再次写出密钥。
- `HandlerConfig.Encrypt` 返回 `(*Encryption, error)`，在持有 Handler 锁时调用；出错时不启动 Muxer，播放列表请求回答 503。
- 密钥服务不用 `Next`，而用 `Handler.SessionMuxer(path, session)` 与 `Muxer.KeyEpoch()`（正在切的分片的周期）检查。

fit 的 `hls` 处理器给 fib 的处理器一个 `Encrypt`：对设置了 `encrypt` 的频道返回按其 `key_rotation` 的 `Encryption`，其 `Key`
与临时方案一样派生 `medialib.Keys.Key(KeyChannel, "<stream>/<session>", epoch)` 并命名为 `key-<session>-<epoch>.key`，其 `Run`
为媒体库的文件池（`Library.Submit`，在队列中等待超过 `file_timeout` 的任务改在单独的 goroutine 上运行）；媒体库未加载时返回
错误（503）。它在 fib 的处理器之前、在自己的 `auth` 下回答密钥 URI，只提供 `SessionMuxer(stream, session).KeyEpoch()` 中
`ok && epoch <= cur` 的密钥；重新加载改变了频道的加密时，它在播放列表请求时结束该频道的 Muxer，让 fib 按新的加密重新启动。
加密频道可以使用低延迟 HLS。

## A70：TURN 中继不共用端口范围内的同一端口

**状态。** 2026-11-06 由 I162（一个地址，一个服务器）提出。已在 fib 的 `856ad5b` 完成，由 fit 的会话按
[规则 15](rules.zh-CN.md#七fib-满足不了时) 实现；fit 已在该提交上。fit 中没有临时方案：fit 的 TURN 角色无法选择中继的端口。

**问题。** 设置 `Config.PortMin` 和 `PortMax` 时，`turnserver.bindRelay` 用 `Engine.AddBinding` 逐个尝试范围内的端口。引擎以
SO_REUSEPORT 绑定（`Config.ReusePort`，fib 的默认），内核允许设置了它的套接字加入另一个同样设置了它的套接字占着的端口：于是中继
可能绑定到另一个分配的中继的端口、范围内 fit 监听器的 UDP 端口，或另一个 fib 进程的端口，内核会把对端的数据报分给两者。

**依据。** 一个只有一个端口的范围的测试：第一个中继由内核选端口绑定，把范围设为该端口，第二次 `bindRelay` 无错误地绑定了同一端口
（两次都是 `127.0.0.1:55826`），而它本应返回 `turn.ErrNoRelay`。

**建议的 API。** 无：在 `bindRelay` 内部修复。范围内的端口先在服务器的中继端口集合中预留，使两个 setup 协程不会选中同一个，并在中继
绑定之前以不带 SO_REUSEPORT 的绑定占位（端口上已有套接字时会被拒绝）；失败的端口像以往被占用的端口一样跳过。中继的绑定移除后归还
预留。

**fit 的使用方式。** `webrtc.turn` 的 `port_min` 和 `port_max` 照旧传给 `Config.PortMin` 和 `PortMax`；各分配现在得到各自的端口。

**实际实现。** 与建议相同（`claimPort`、`releasePort`、`relay.port`），附 `TestRelayPortsNotShared`（另一个中继或另一个以
SO_REUSEPORT 监听的引擎占着的端口被跳过，中继关闭后端口回到范围中）和 `TestRelayPortsConcurrent`（在四个端口的范围上同时绑定的中继
各得一个端口）。
