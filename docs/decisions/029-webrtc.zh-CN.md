# 029：fit 中的 WebRTC，基于 fib 的 WebRTC 包

[English](029-webrtc.md) | [简体中文](029-webrtc.zh-CN.md)

状态：已接受（2026-11-01）。修订 [决定 025](025-p2p.zh-CN.md)。

## 背景

fib 现在有了 WebRTC（fib `80c7f76`）：`PeerConnection`，以及让任意多个 `PeerConnection` 共用一个 UDP 端口的 `Mux`；
其下是 sans-IO 的各个协议包：`webrtc/stun`、`webrtc/ice`、`webrtc/turn`（客户端和服务端核心）、`webrtc/sdp`、
`webrtc/sctp`、`webrtc/srtp` 和 `webrtc/rtp`，DTLS 在 fib 的 `tls` 中。`webrtc/turn/turnserver` 在 fib 引擎上运行 TURN
服务端核心：监听地址是一个 UDP binding，每个 allocation 的中继地址是它自己的 binding，用 `Engine.AddBinding` 打开，用
`Engine.OpenUDPPeer`（[A49](../fib-api-requests.zh-CN.md#a49在监听-socket-上朝某地址打开-udp-对端)）向外发送。

用户要求 fit 提供 WebRTC 服务：给浏览器和其他实现用的 TURN 服务器；以 fit 为媒体服务器的 WHIP 推流和 WHEP 播放；以及
fit 作为其他媒体服务器前面的代理，代理它们的信令和媒体。

决定 025 计划把 STUN、TURN 和 ICE 写成 fit 自己的包 `p2p/stun`、`p2p/turn` 和 `p2p/ice`，因为 pion 在自己的
goroutine 上读 socket、跑定时器。fib 的这些包正是 025 想要的：sans-IO，跑在 fib 的引擎上；规则 15 也要求 fib 能承载的
东西放在 fib 里。

## 决定

- **fit 的 WebRTC 就是 fib 的。** fit 使用 fib 的 `webrtc` 各包，不增加依赖；出于 025 的理由，仍不用 pion。
- **修订 025。** 它的中继和 ICE 来自 fib：I86（STUN 客户端）使用 `webrtc/stun` 的事务，I87（TURN 服务器）使用
  `webrtc/turn/turnserver`，I88（TURN 客户端）使用 `turn.Client`，I89（ICE agent）使用 `webrtc/ice`。不再编写 `p2p/turn`
  和 `p2p/ice`。`p2p/stun` 暂时保留，作为 fit 可放在任何 listener 上、走 UDP 和 TCP 的 STUN 服务器，这是 fib 没有的
  （fib 的 TURN 服务器只在自己的 UDP 端口上应答 Binding 请求）。I86 进行时，它的消息改用 `webrtc/stun` 的，只保留服务器
  部分，这样进程里只有一套 STUN 编解码，修复对两边都生效。025 的其他决定不变：选中的候选对上跑 QUIC，信令在汇合点的
  fit 上，一切都有上限。
- **配置中的 `webrtc`。** 一个顶层成员，存放 fit 在网关之外为 WebRTC 运行的东西，与 `llm` 和 `tunnel` 一样
  （[决定 028](028-llm-and-tunnel.zh-CN.md)）：`webrtc.turn`，即 TURN 服务器，是 `app` 包的一个角色，运行在 listener
  的引擎上，随每个配置应用、迁移和停止。WHIP 和 WHEP（I109）是路由上的 handler 模块，它们的对端共用的 UDP 端口是旁边的
  另一个角色 `webrtc.media`。
- **TURN 服务器**（`webrtc/turn`，I87、I111）：监听的 UDP 地址，同一个 binding，以及 TCP 和 TLS 地址（I111，A55），
  TLS 证书像 listener 一样来自文件；realm；带密码或 coturn 长期密钥的用户，以及
  coturn 用于限时凭据的 `use-auth-secret`，二者可选其一或同时使用；中继地址、1:1 NAT 后的公网地址和端口范围；allocation
  的总数和每用户数、每个用户每个方向每秒中继的字节数、生存期；每个 allocation 的 permission 和 channel 上限（A57）；允许
  和拒绝的对端，默认采用 fib 对内部地址的拒绝，服务器自己的地址只在其中继端口上可达（A57）。重载后，用户、secret、对端
  列表、带宽、生存期和配额从下一个请求起生效，不影响已有的 allocation（A58）；地址改变时在旧服务器旁启动新服务器，旧的
  在 listener 的排空超时内排空；同一地址上 realm 或中继地址改变时服务器重新启动，释放它的 allocation。它的指标来自 fib 的
  事件和统计，经 `app` 出现在 Admin API 的 `/metrics` 中。
- **WHIP 和 WHEP**（I109）：`whip` 和 `whep` 模块在 fit 中终结 WebRTC，使用 `webrtc.media` 共享的 `webrtc.Mux`：按路由给出
  的名字区分流，一个推流者的 RTP 转发给多个观看者并转发关键帧请求，用路由的中间件做认证，限制流数、观看者数、
  PeerConnection 数和码率，按 RFC 9725 和 WHEP 草案支持 DELETE 和 PATCH（trickle），在 `Link` 头中给出 ICE 服务器，重载和
  停止时关闭一切。
- **代理媒体服务器**（I110）：处理器模块 `webrtc_proxy` 经 fit 的上游连接池把 WHIP 和 WHEP 信令代理到由媒体服务器
  组成的上游，offer 去掉候选，answer 的候选改写为 fit 的 UDP 端口，并记住其 ufrag 和 ice-pwd 对应的媒体服务器地址；
  角色 `webrtc.proxy` 持有这个端口，按新对端地址第一个数据报中的 STUN USERNAME 选路（用会话的 ice-pwd 校验），之后
  从它自己的一个 socket 中继到媒体服务器，让多个媒体服务器共用 fit 的一个端口。它是 `webrtc.media` 旁边的角色，而不是
  L4 `service` 的一种模式：它的会话长期存在，必须挺过重载，而 L4 服务的连接会随其所属的图被排空；它的路由由处理器在
  请求时写入，只有双方都能找到的进程级角色（按 engine 查找，就像 `whip` 找到 `webrtc.media`）才能共享；它的目的地是
  answer 中给出的地址，不是上游的地址。每个对端地址一个 socket 而不是共用一个，因为单端口的媒体服务器按地址区分对端。

## fib 需要提供的

五项都已完成，在 fib `84a67f7` 中，fit 自 I111 起使用。

- [A55](../fib-api-requests.zh-CN.md#a55经-tcp-和-tls-的-turn)：经 TCP 和 TLS 连接 TURN 服务器，这是 025 要求的，UDP
  被防火墙封住的浏览器会用到。
- [A56](../fib-api-requests.zh-CN.md#a56从不让-worker-等待的-turn-服务器)：TURN 服务器绑定中继地址和打开对端时不让 worker
  等待：目前在一个数据报的 `OnData` 中，`AddBinding` 和 `OpenUDPPeer` 会等待锁和事件循环。
- [A57](../fib-api-requests.zh-CN.md#a57turn-服务器的限制与钩子)：每个 allocation 的 permission 和 channel 上限，能看到端口
  的对端检查，以及用于指标的事件。
- [A58](../fib-api-requests.zh-CN.md#a58原地修改和排空-turn-服务器)：原地修改生存期和配额，以及不再接受新 allocation、
  让已有的自然结束的排空。
- [A59](../fib-api-requests.zh-CN.md#a59从指定的本地地址和端口范围发起拨号)：从指定的本地地址和端口范围发起拨号，
  用于代理通向防火墙后媒体服务器的 socket。

## 影响

- fit 只有一套 WebRTC 协议栈，即 fib 的，由 fib 的测试和模糊测试覆盖；修复进入 fib（规则 15）。
- 在 A56 之前，TURN 服务器绑定中继地址或打开对端时，worker 会短暂等待 fib 的 binding 锁和事件循环：这是对规则 1 的
  已知违反，受 allocation 配额约束，自 I111 起已消除。在 A57 之前，一个 allocation 的 permission 只受它的请求约束，channel
  只受 RFC 8656 的 4096 个编号约束；自 I111 起受 `max_permissions` 和 `max_channels` 约束。
- 在 A55 之前，不能使用 UDP 的客户端无法连到 TURN 服务器；自 I111 起它们经 TCP 和 TLS 连接。
- 自 A59（I111）起，代理通向媒体服务器的 socket 从 `relay_ip` 和一段端口范围发出，以适应它们前面的防火墙。
