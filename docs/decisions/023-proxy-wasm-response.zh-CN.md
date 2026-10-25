# 023：Proxy-Wasm 的响应回调，在插件自己的池上运行

[English](023-proxy-wasm-response.md) | [简体中文](023-proxy-wasm-response.zh-CN.md)

状态：已采纳，见 I81（2026-10-25）。它补全了[决定 018](018-proxy-wasm-subset.zh-CN.md) 留下的响应回调。

## 背景

I72 为 Proxy-Wasm 过滤器实现了请求阶段，没有实现响应回调（决定 018）：fit 在插件自己的池上运行插件（规则 4），而响应的
头和 body 在写它们的 goroutine 上定下并写出（常常是 fib 的 worker），由必须当场作出决定的钩子处理。在那里调用插件，就是在
worker 上运行插件。

fib 的 A41（`OnHeaderAsync`）可以压住响应头，直到异步调用说继续，这正是 `proxy_on_response_headers` 需要的。body 仍然做不到：
`OnBodyFilter` 的过滤器在写 body 的 goroutine 上运行且不能阻塞，而 `proxy_on_response_body` 要在其中运行，body 结束时的 trailer
回调也是一样。为此 fit 需要 fib 提供 A46，`OnBodyFilterAsync`：过滤器的调用在别处完成，通过 `done` 回调返回，一次一个；期间
写入的部分暂存在响应 64 KB 的上限之内，写入方像遇到慢客户端那样被限速；最后一次调用带上 trailer，可以修改。它已按 fib 的
标准加入 fib（规则 15）。

## 决定

- **导出了响应回调的插件跟随响应**：插件放行的每个请求（插件自己应答或暂停的请求不算），其上下文在创建它的实例上保持打开，
  因为 Proxy-Wasm 的上下文属于它的实例；响应的回调是排在该实例 goroutine 上的调用，先于新请求运行，从不在写响应的
  goroutine 上运行。`CONNECT` 或升级的响应会立即发出，不跟随。
- **响应头**在 `OnHeaderAsync` 压住时交给 `proxy_on_response_headers`：插件通过 `:status` 读取状态码，可以修改字段；请求
  阶段对响应字段所做的修改先生效。状态码不能改，响应头发出之后也不能再改。
- **body** 在写入时经 `OnBodyFilterAsync` 交给 `proxy_on_response_body`：每次调用拿到上次之后写入的部分，插件用
  `proxy_get_buffer_bytes`、`proxy_set_buffer_bytes` 读取和替换，放行的部分被发出。暂停的插件持有 body，之后有更多数据时
  再用它持有的全部内容调用，与 Envoy 一样，最多 `max_body_bytes`（默认 1 MiB）。结束时，body 的最后一次调用带上 trailer，
  有 trailer 时再交给 `proxy_on_response_trailers`；原本没有 trailer 的 body 在结束时也可以由插件加上。经过拿到 body 的插件
  的响应不带 `Content-Length`。
- **上限**：每次调用受插件的 `timeout` 限制；一个实例同时最多跟随 `max_streams` 个响应（默认 1024），超过时请求得到 503；
  每个实例的响应调用队列也以此为界。调用进行时 fib 暂存的部分最多 64 KB，插件的输出按客户端的速度发送。
- **失败**：回调失败、trap 或超时，持有的 body 超过 `max_body_bytes`，以及在响应已开始后发送本地响应（响应头已定，fib 无法替换
  这个响应），都会让响应失败，按 `on_error: fail` 关闭 HTTP/1 连接或重置流；`on_error: skip` 则让响应的其余部分绕过插件
  继续，连同插件持有的内容。
- **上下文的关闭**：body 结束时（没有 body 的响应在响应头之后），或请求先结束时，在它的实例上 done、log 和 delete。服务该请求的
  配置一直被持有到那时，这样重新加载不会在响应进行中关闭池。
- **`response`** 决定插件拿到哪些回调：`all`（默认）、`headers`（body 原样通过，保留长度）或 `none`（只有请求阶段，同以前）。
- 仍未实现：请求 body 和 trailer 的回调、外部调用（`proxy_http_call`）、共享数据和队列、定时器、外部函数，以及恢复暂停的流。

## 影响

- Proxy-Wasm Go SDK 的 `http_headers` 示例（在 `OnHttpResponseHeaders` 中添加响应字段）和 `http_body` 示例（持有响应 body
  直到结束，再追加、前置或替换）在 I81 的测试中原样运行；另有 fit 自己的过滤器改写流式 body 的每一部分和 trailer，覆盖
  HTTP/1.1、h2c、h2 和 HTTP/3。
- 跟随的响应比只有请求阶段代价更高：响应头一次池上调用，body 在没有调用进行时到达的每一部分一次，关闭上下文一次。只处理
  请求的过滤器可以配置 `response: none`。
- fit 自己的 ABI（I71）仍然只处理请求；它的插件在响应头定下时修改响应字段。
