# 028：agent 与 tunnel 作为 fit 配置中的角色

[English](028-agent-and-tunnel.md) | [简体中文](028-agent-and-tunnel.zh-CN.md)

状态：已采纳（2026-11-01）。

## 背景

用户要求 fit 在网关之外提供两项功能，每项都由运行在多台机器上、彼此互联的角色组成：

- **agent** 在多台机器间共享 AI 模型。*node* 用本地或远端上游（OpenAI、Anthropic 兼容 API）提供
  模型，*hub* 是 node 与 client 汇合的地方，*client* 提供一个 API，调用它能到达的 node 上的模型。
- **tunnel** 用来访问 NAT 后面的 TCP 服务。服务旁边的 *node* 通过 TCP、WebSocket 或 QUIC 连到
  *server*，*client* 把本地端口和 Web 代理经 server 转发到某个 node 的服务。

用户要求两者都共用 fit 的 fib 引擎，使用 fit 的配置格式，进程运行哪些角色由 fit 的配置决定，存储的
内容放进 fit 的 store；并要求完全遵守 fit 的开发规则，不留到以后。

## 决定

- **包。** agent 的角色是 `agent/hub`、`agent/node`、`agent/client`；tunnel 的角色是
  `tunnel/server`、`tunnel/node` 和 `tunnel/client`。同一家族角色共用的代码在
  `agent/internal` 和 `tunnel/internal` 下；两个家族共用的放在 fit 自己的包中：`login`（用户、会话、
  CSRF 令牌与锁定，与 admin.console 共用）、`app`（角色的注册表与管理器）以及 `app/console`（角色在
  fib 上的网页）。`app/standard` 导入所有角色，`cmd/fit` 与 `fit build` 导入它。
- **配置。** 两个顶层成员 `agent` 和 `tunnel`，每个角色一个成员：`agent.hub`、`agent.node`、
  `agent.client`、`tunnel.server`、`tunnel.node`、`tunnel.client`。设置了成员，角色就运行；一个进程可
  运行任意多个，可与网关的 listener 和路由并存，也可以都没有。成员与配置其余部分一样严格解码，使用
  snake_case，带 `doc` 标签、默认值和边界，因此 `fit schema`、`fit validate`、console 的表单和参考
  文档都覆盖它们。只有角色而没有路由的配置不需要 listener。
- **一个引擎。** 每个角色都把自己的 binding（它的网页、链路监听、client 转发的端口）加到 listener 的
  引擎 `listener.Server.Engine()` 上，并在其上用自己的 handler 拨号，与网关自己的连接一样。
  `limits.max_conns` 和 `max_pending_bytes` 与其余连接一起约束它们。HTTP 客户端（如 agent node 到上游
  的客户端）通过 fit 上游池使用的上游引擎拨号。角色的 QUIC 是同一引擎 UDP binding 上的 fib
  `http3/quic`。
- **应用配置。** 每个配置应用时，在 listener 之后、在控制面上推进 `app.Manager`：配置设置了但未运行的
  角色被启动，已运行的角色以其成员调用 `Apply`，配置中去掉的角色被停止。角色迁移到新成员时不丢弃未变
  的部分：它的链路、隧道和会话。应用失败的角色保持原样运行，错误作为该配置的警告报告，就像需要重启才
  生效的改动一样；在启动时，它让 fit 退出。由各自独立绑定的部分组成的角色，如 tunnel 的 listener 和端口转发，
  应用能绑定的部分，并在该警告中报告其余部分，而不因一个被占用的端口保留整个旧成员。编译配置时运行 `app.Check`，因此 `fit validate` 和
  `fit store import` 会做解码之外的检查。
- **网页上的改动写回配置。** 角色的网页通过 `Host.Edit` 修改自己的成员：经由
  `admin.Configs.EditMember`，按 JSON 指针替换成员后，应用正在运行的基础配置，来源为角色的 ID。改动像
  其他配置一样运行，进入历史，可以回滚；有 `admin.store` 时写入 store。没有 store 时，它运行到重启或
  重新读取文件为止，并以警告说明。角色生成且必须保留的东西，如 QUIC 证书、ID 或 API key 的哈希，也同样
  写入它的成员。Edit 在管理器自己的 goroutine 上逐个执行，从不在 fib worker 上，至多 `app.MaxEdits` 个
  等待。
- **一个 store。** 角色没有自己的状态文件或 store：角色要保存的内容在它的成员中，因而在 fit 的 store
  中。`fit store import` 导入以这些成员写成的配置。
- **登录用 fit 的。** 角色的网页受 `console`（一个 `RoleConsole`：地址、TLS 文件、用户及
  `fit hash-password` 生成的 bcrypt 哈希、会话限制）保护，通过 `login` 包，与 admin.console 一样检查；
  角色没有自己的密码或登录方式。
- **规则完全适用。** 角色做的任何事都不阻塞 fib worker：bcrypt、命令执行、读文件和配置修改在各自有界的
  池上执行，带超时；agent node 到上游的请求体与响应体用 fib 的异步客户端和 `OnBody` 流式转发，双向背压；
  每个 map、队列和缓冲都有带安全默认值的上限；与外部的每次交互都有超时。没有角色用 `net/http`
  handler 提供服务、用 `net/http.Client` 代理，或运行自己的引擎。

## 后果

- 一个二进制和一份配置就能以任意组合运行网关、tunnel 和 agent；一个 store 保存它们的设置；一份日志、
  一个 Admin API 和一个 console 展示它们。
- 角色成员中有密码和密钥。存在明文文件中时，安全程度与文件相同；`admin.store` 把它们加密保存，这也是
  网页的改动应保存在那里的原因。
- 角色与 listener 共用限制。繁忙的 tunnel 可能用尽 `limits.max_conns`。
