# 005：Admin API 用 net/http，默认关闭，配置按原样保存

[English](005-admin-api.md) | [简体中文](005-admin-api.zh-CN.md)

状态：已采纳，见 I24（2026-10-19）。

## 背景

I24 加入 Admin API，用来读取和替换运行中的配置，以及已应用配置的历史。需要确定：由什么来服务这个 API；配置没有
要求时它是否开启；如何防止网页使用它；以及历史保存什么。

## 决定

- **Admin API 由 net/http 在它自己的 goroutine 上服务。** API 属于控制面：应用配置要读取证书文件、绑定
  listener，这些都会阻塞，所以不能运行在 fib worker 上（规则 4）。net/http 属于标准库，核心的依赖不变；fib 只
  会多出一个 API 反正要离开的 worker 池。它的资源都有上限：`admin.max_conns` 个连接（16；超过的连接在 accept
  后立即关闭）、`max_config_bytes` 的请求体（4 MiB；超过则 413）、64 KiB 的头部，头部 10s、请求体和响应 60s、
  空闲 60s 的超时。同一时间只应用一个配置。
- **配置里没有 `admin` 就不开启。** `"admin": {}` 在 127.0.0.1:2019 开启它。Caddy 默认开启；对 fit 来说，那样
  同一主机上的第二个进程就会启动失败，而且一个没人要求的 listener 出现在代理的配置里会让人意外。地址必须是回环
  地址或 Unix 套接字（`unix/<路径>`，权限 0600），除非设置了 `token_file`，这样远程可达的 API 总是要求令牌。
- **拒绝网页的请求。** 带 `Origin` 的请求得到 403，网页因此无法向 localhost 提交配置；`POST /load` 只接受
  `application/json`，网页发送它需要一次预检，而 API 不应答预检；在没有令牌的 TCP 地址上，`Host` 字段必须是
  回环主机名，通过 DNS 重绑定到达 127.0.0.1 的网页不满足这一点。
- **令牌从文件读取**，即 `token_file`，从不写在配置里：`GET /config` 返回配置，令牌写在里面也会被返回。
- **配置按原样保存**在内存中：`GET /config` 返回被应用的字节，来自文件或 API，而不是重新编码的结果。保留最近
  `admin.history` 个配置（10 个），编号为 `runtime.Runtime.Swap` 给它们的图的版本号。它们不写入磁盘；重启从文件
  开始。

更新，I25（2026-10-19）：

- **配置中的路径是 `/config` 之后的 JSON pointer**（RFC 6901），如 `/config/upstreams/0/addresses`，键中的 `/`
  写作 `~1`。`PUT` 设置那里的值（替换、新增成员，或在 `-` 处追加到数组），`PATCH` 把请求体作为 JSON merge patch
  （RFC 7396）合并进去，这样改 listener 的几个字段时无需重写整个 listener，`DELETE` 删除它。每次修改都应用一个完整的
  新配置，像其他配置一样检查，所以让配置失效的修改会连同错误一起被拒绝。
- **修改后的配置会重新写出**，每层缩进两个空格，成员保持原有顺序，数字保持原样。只有整体发送的配置（`POST /load`、
  `PUT /config`）逐字节保存。
- **ETag 是配置原文的哈希**（SHA-256 的前 96 位），而不是版本号：fit 重启后版本号从 1 重新开始，回滚也会给旧配置一个
  新版本号。`If-Match` 的检查和基于运行中配置的修改都在给应用排序的那把锁下进行，所以并发写入不会丢失修改；ETag
  过期的写入得到 412。
- **`POST /rollback/{n}`** 在版本 `n` 仍在历史中时，把它的配置作为新版本再次应用。

更新，I64（2026-10-22）：

- **仪表盘由 API 自己提供**，位于 `/dashboard/`，除非设置了 `admin.disable_dashboard`：嵌入二进制的 HTML、CSS 和
  JavaScript，其 Content-Security-Policy 只允许自身的文件和对自身源的请求。它读取 `GET /status`（新增：listener、
  连接、上游及其后端的健康状态、证书）、`/config/effective` 和 `/versions`，不发送其他请求；有一个测试读取这些文件，
  出现其他方法即失败。
- **guard 的两个有限例外。** 仪表盘的文件无需 token 即可获取，因为它们不含数据，页面会自己要求输入 token，保存在该
  标签页的 `sessionStorage` 中并作为 `Authorization` 发送；所有端点仍然要求 token。另外，`Origin` 为 API 自身源
  （协议加请求的 `Host`）的 `GET` 或 `HEAD` 会被放行：浏览器在某些同源读取时也会发送 `Origin`；其他源的页面仍得到
  403，任何带 `Origin` 的写入也是如此。回环 `Host` 检查不变，所以仪表盘要用回环名称访问，或在设置 token 后从任何地方
  访问。

## 影响

- 通过 API 加载的配置持续到下一次 SIGHUP（它会重新应用文件）或重启。把 API 加载的配置写回磁盘（像 Caddy 的
  autosave 那样）可以随文件 provider（I27）一起做。
- 对 `admin` 和 `access_log` 的修改在重启后生效；应用时会提示。
- 历史中 API 配置最多占 `history × max_config_bytes`（默认 40 MiB），文件的配置按其大小计。
