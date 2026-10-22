# 018：Proxy-Wasm 的请求阶段，暂不含响应回调

[English](018-proxy-wasm-subset.md) | [简体中文](018-proxy-wasm-subset.zh-CN.md)

状态：已采纳，见 I72（2026-10-23）。

## 背景

I72 的任务是：如果 I71 的测量结果允许，就实现常见过滤器需要的 Proxy-Wasm HTTP 过滤器调用，使已有的过滤器无需修改就能
运行。结果是允许的：经过插件的请求约耗 5 µs（I71），经过用 Proxy-Wasm Go SDK 构建的过滤器约 8 µs，两者都有上限。过滤器
能做的事分两个阶段：请求头到达时（`proxy_on_request_headers`），以及响应头到达时（`proxy_on_response_headers`），每个阶段
之后还有响应体和 trailer 的回调。

fit 在插件自己的池上运行插件，从不在 fib worker 上运行（规则 4）。请求阶段的回调符合这一点：请求被持有、交给池，从那里
继续。响应阶段的回调则不然：响应头在写出它的那个 goroutine 上（通常是 fib worker）由当场运行的钩子（`OnHeader`、
`OnBodyFilter`）确定。在那里调用插件，要么让插件在 worker 上运行，要么让响应头在池上调用期间等待，而 fib 做不到后者：
无论响应整体写出还是流式写出，经 HTTP/1、HTTP/2 还是 HTTP/3，经 sendfile 还是经过滤器，它的头都是立即确定的。

## 决定

- **fit 实现 Proxy-Wasm v0.2.x 的请求阶段**：导出 `proxy_abi_version_0_2_0` 或 `_0_2_1` 的插件按 Proxy-Wasm 启动（其 root
  context、`vm_config` 和插件的 `config`），每个请求是一个独立的 context：创建，把请求头交给插件，然后 done、log、delete。
  header map（请求和响应的，含伪头 `:method`、`:path`、`:authority`、`:scheme`）、本地响应、VM 和插件配置缓冲区、属性
  （请求自身的，以及配置设定的）、日志和每个实例的指标都可用。过滤器在此时对响应头所做的修改，在响应头确定时生效。
- **不调用响应阶段的回调**，也不调用响应体和 trailer 的回调；外部调用（`proxy_http_call`）、共享数据和队列、定时器、外部
  函数，以及设置属性或缓冲区，都返回 `Unimplemented`。暂停请求却不回答、等待被恢复的过滤器，按 `on_error` 处理为失败。
- **响应阶段需要 fib 提供的能力记为 A41**：一个钩子，在异步调用表示继续之前压住响应头，期间响应体在响应自身的上限内被
  暂存。有了它，`proxy_on_response_headers` 就可以像请求阶段的回调一样在池上运行。

## 影响

- 作用于请求的过滤器无需修改即可运行：I72 的测试原样运行 SDK 的 `http_routing` 和 `properties` 示例。在
  `on_request_headers` 中添加响应头的过滤器也可用；像 SDK 的 `http_headers` 示例那样在 `on_response_headers` 中添加的，
  过滤器能运行，但那部分不会发生。
- fit 自己的 ABI（I71）有同样的限制，出路也相同。
