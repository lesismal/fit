# 004：具名上游、客户端 engine，以及会排空的连接池

[English](004-upstreams-and-async-handlers.md) | [简体中文](004-upstreams-and-async-handlers.zh-CN.md)

状态：已采纳，见 I15（2026-10-19）。

## 背景

I15 的代理 handler 需要三件代码里尚未确定的事：上游写在配置的哪里；它的连接通过哪个 fib engine 拨号；以及
一个在返回之后才应答的 handler 如何让它用到的东西保持可用——因为 listener 在 handler 返回时就释放图。

## 决定

- **上游是顶层的具名列表** `upstreams`，代理用 `"upstream": "app"` 引用，与 nginx 的 `upstream` 块和 Traefik
  的 service 一样。健康检查、负载均衡和连接上限（I19、I20、I32）属于上游，多个路由可以共享一个连接池。类型是
  `config.Upstream`，由 `Config.Validate` 检查。
- **`runtime.Compile(c, Env{Engine})`** 在提供模块之前为每个上游创建一个 `upstream.Pool`，并通过 `module.Env`
  交给模块：`Provision` 用 `ctx.Upstream(name)` 找到连接池。engine 为 nil 时创建的连接池让每个请求失败，供
  `fit validate` 使用。
- **上游连接使用单独的 engine**，由 `fit run` 用 `upstream.StartEngine` 启动，在图释放后停止。fib v1.2.0 在
  图编译之后才用 `fib.Bind` 一次性根据 binding 创建 listener 的 engine，不能给运行中的 engine 添加 binding。单独
  的 engine 只多几个空闲 goroutine，代价是代理请求会从一个事件循环转到另一个。等 fit 升级到有 `fib.New`、
  binding 可以动态增减的 fib 后，值得测量共用一个 engine 的效果。
- **连接池先排空再关闭。** 图的清理在模块之后关闭连接池，而连接池的 `Close` 会等它上面的在途请求：最后一个
  回调返回时才关闭客户端。分发器持有图直到 handler 返回，此时 handler 已经调用了 `Pool.Do`，所以图替换之后才
  到达的响应仍能找到打开的连接池。代理自己用 `Context.Retain` 持有请求直到写完响应，除此之外不保留图里的任何
  东西。

更新，I22（2026-10-19）：fit 升级到了 fib v1.3.0，其 engine 可以在运行中添加 binding，因此一个 engine 就能同时
服务两者。但上游 engine 仍保持独立：自 I16 起，代理下载在上游 engine 的 worker 上于 `Context.Write` 中等待慢客户
端，独立的 engine 让这些等待不占用 listener 的池。

更新，I26（2026-10-19）：在返回后才应答的 handler 现在通过 `module.Context.Graph()` 自己持有图，直到请求结束：
代理在一次转发开始时持有，结束时释放，所以跨越重载的下载留在它开始时的图上。被换下的图在 runtime 的排空超时后被
排空：实现 `module.Drainer` 的模块结束它们仍在处理的请求，代理的做法是取消这些转发的 context。连接池可以共享：
上游配置和 CA 文件都未变时，新图接管前一个图的连接池，每个连接池记录持有它的图的数量。

## 影响

- 在返回后才应答的 handler 必须自己保证返回后用到的东西可用，就像代理对连接池那样；图不会等它。若要按请求持有
  图，需要 fib 在结束一个被 retain 的请求时提供钩子，而 fib v1.2.0 只能通过 `Context.Context()` 得知，监听它
  需要每请求一个 goroutine。
- 重载（I22）会创建新的连接池，连接不会从一个图带到下一个图。I26 可以让上游未变的图之间共享连接池。
