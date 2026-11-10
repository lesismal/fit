# WebRTC

[English](23-webrtc.md) | [简体中文](23-webrtc.zh-CN.md)

## 功能

fit 用 fib 的 WebRTC 各包提供 WebRTC 服务，与网关一起运行在其引擎上。它做三件事，各自是 `webrtc` 下的一个成员：

- **`turn`**：一个 TURN 服务器（RFC 8656）。两个 WebRTC 对端在不让打洞的 NAT 后面，或所在网络封锁了它们之间的
  UDP，就经由 TURN 中继相连：各自申请一个 *allocation*，服务器在这个地址与它许可的对端之间转发数据报。凭据是
  `users` 中的静态用户，或 `secret` 这串 coturn 的限时凭据密钥；两者可以同时设置。UDP 在 `listen` 上，TCP 在
  `listen_tcp` 上，TLS 在 `listen_tls` 上。
- **`media`**：fit 自己是一个小型媒体服务器（SFU），`whip` 和 `whep` 处理器在路由上提供 HTTP 一侧，所有
  PeerConnection 共用这一个 UDP 端口，fit 在其上是 ICE lite agent。
- **`proxy`**：把其他媒体服务器（别处的 `whip` 和 `whep`，或 MediaMTX、Janus、LiveKit）放在 fit 后面：处理器
  `webrtc_proxy` 把信令转给上游，`webrtc.proxy` 按每个对端第一个 STUN 请求中的 ICE ufrag 把它中继到应答它的那台
  媒体服务器。

`webrtc.turn` 是 fit 唯一的 TURN 服务器；计划中的 P2P 隧道在打不通洞时也会像其他 ICE agent 一样用它中继。
[tunnel](24-tunnel.zh-CN.md) 是另一回事：它转发的是主动连出的链路，而不是标准 ICE agent 的 UDP 数据报。

## 页面与操作

### WebRTC

菜单里的"WebRTC"模块只有一个页面，编辑配置的 `webrtc` 成员：`turn`、`media` 与 `proxy`。页面有 UI、`.conf`、
`.yaml`、`.json` 四个标签页，编辑的是共享草稿中属于这一页的部分；顶栏的**保存并应用**保存整份草稿，**检查**走完整的
应用流程（加载、编译、试绑新监听地址）只检查不应用，**放弃修改**丢弃草稿。保存的问题链接回这一页。

HTTP 一侧不在这一页：`whip`、`whep` 与 `webrtc_proxy` 是路由上的处理器，在[反向代理](15-reverse-proxy.zh-CN.md)页
设置，并按 `auth` 引用[媒体认证](21-media-auth.zh-CN.md)的策略。没有 `webrtc.media` 时带 `whip` 或 `whep` 的路由
应答 `503`；没有 `webrtc.proxy` 时带 `webrtc_proxy` 的路由同样应答 `503`。

在这一页能做的事：

- 写 `turn` 的地址（`listen`、`listen_tcp`、`listen_tls`）、`realm`、`relay_ip` 与 `public_ip`、中继端口范围，
  以及凭据：`users` 各带 `password` 或 `key`（coturn `turnadmin -k` 打印的值），或 `secret`。
- 写 `turn` 的配额与带宽：`max_allocations`、`max_allocations_per_user`、`max_permissions`、`max_channels`、
  `user_bandwidth`、生存期，以及 `allowed_peers` 与 `denied_peers`。
- 写 `media` 的端口、`public_ips`、`ice_servers` 与 `turn_urls`（answer 的 `Link` 头把它们告知对端）、它的 `access`，
  以及 PeerConnection、流、观看者、码率的上限和超时。
- 写 `proxy` 的端口、`public_ips`、会话与中继的上限、空闲超时，以及通往媒体服务器的源地址 `relay_ip` 与端口范围。

重载时 `turn` 的 `users`、`secret`、对端列表、配额、生存期和证书在原处生效，已有的 allocation 保留；修改地址会
启动新服务器并排空旧的。`media` 保持 `listen` 和 `public_ips` 不变的重载保留所有会话；修改它们会重新启动，会话
结束，每个对端收到 DTLS close。`proxy` 保持 `listen` 不变的重载保留会话与其中继。

## 参数

关键参数如下；完整字段表见[配置参考：WebRTC](../reference/config.md#webrtc)
（[`webrtc.turn`](../reference/config.md#config-turnserver)、
[`webrtc.media`](../reference/config.md#config-webrtcmedia)、
[`webrtc.proxy`](../reference/config.md#config-webrtcproxy)），处理器各节见
[Handlers](../reference/config.md#handlers)。

### turn

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `listen` | — | 必填。客户端到达的 UDP 地址，如 `:3478`（`turn:` URI 的默认端口）；同一地址也可作为 `stun:` 服务器，它不需凭据地应答 STUN Binding |
| `listen_tcp`、`listen_tls` | 无 | 经 TCP 与 TLS 提供 TURN（`turn:host:port?transport=tcp`、`turns:`）；TLS 的证书取自 `tls.certificates` 中的文件，并被监视。TCP 与 TLS 地址不能相同；UDP 与 TCP 可以 |
| `mptcp` | 关闭 | 内核支持时（Linux 5.6+）以多路径 TCP 承载 `listen_tcp` 与 `listen_tls`；`listen` 的 UDP socket 没有 |
| `realm` | — | 必填。凭据的 realm，写在每个质询里 |
| `users` | 无 | 静态用户，各带 `password` 或它导出的 `key`；与 `secret` 同时设置时先在 `users` 中查找 |
| `secret` | 无 | coturn `use-auth-secret` 的共享密钥：用户名 `<过期时间>:<任意内容>`，密码是 `base64(HMAC-SHA1(secret, username))`，过期前有效 |
| `relay_ip` | — | 必填。中继 socket 绑定的地址，必须是本机的地址之一，不能是通配 |
| `public_ip` | 无 | 1:1 NAT 之后告知客户端的地址 |
| `port_min`、`port_max` | 无 | 中继端口范围，便于防火墙开放；都不设时由内核选择 |
| `max_allocations` | 1024 | 同时持有的 allocation 数；再多一个以 `508` 拒绝 |
| `max_allocations_per_user` | 16 | 一个用户名的 allocation 数；再多一个以 `486` 拒绝 |
| `max_permissions` | 64 | 一个 allocation 许可的对端地址数；再多一个以 `508` 拒绝 |
| `max_channels` | 64 | 一个 allocation 绑定的 channel 数；再多一个以 `508` 拒绝 |
| `user_bandwidth` | 2 MiB | 一个用户名每个方向每秒中继的字节数，允许一秒的突发；超出的数据报被丢弃 |
| `default_lifetime`、`max_lifetime` | 10m、1h | allocation 的生存期，由客户端刷新 |
| `nonce_lifetime`、`nonce_secret` | 10m、随机 | nonce 的有效时间（更旧的应答 `438`），以及生成 nonce 用的密钥——同一个负载均衡器后的各服务器共享它，因为客户端的请求可能落到任何一台 |
| `max_stream_message` | 8 KiB | TCP 或 TLS 客户端最大的消息；声称更大的连接立即关闭 |
| `stream_idle_timeout` | 30s | 没有 allocation 的 TCP 或 TLS 连接 |
| `allowed_peers`、`denied_peers` | 无 | 环回、私有、链路本地、组播等内部地址默认被拒绝；`allowed_peers` 可放行其中一部分。两者都接受带端口的地址，如 `10.0.0.5:8189` |
| `access` | 无 | 客户端地址的允许与拒绝列表，先于全局 `access` 决定；被拒绝的连接在接受时立即关闭，数据报被丢弃 |

每个 allocation 在引擎的每个事件循环上各占一个 socket；这些 socket 在 fib 服务器自己的 goroutine 上打开，绝不
在服务请求的 worker 上。

### media

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `listen` | — | 必填。所有 PeerConnection 共用的 UDP 端口，如 `:8189`；通配 host 告知每个在用接口的地址，除非设了 `public_ips` |
| `public_ips` | 无 | 告知对端到达该端口的地址（1:1 NAT 之后，或原样转发该端口的负载均衡器） |
| `access` | 跟随全局 `access` | 该端口服务的对端地址，先于全局 `access` 决定；被拒绝地址的数据报被丢弃，其 ICE 检查得不到应答 |
| `ice_servers` | 无 | 在 `Link` 头中告知对端的 STUN 与 TURN 服务器；fit 自己不需要，它是 ICE lite agent |
| `turn_urls` | 无 | fit 自己的 `webrtc.turn`，作为 `turn_urls` 告知对端；此时必须设置其 `secret` |
| `turn_credential_ttl` | 1h | answer 携带的 TURN 凭据的有效时间 |
| `codecs` | opus、VP8、VP9、H264、AV1 | 推流者可以发送的编解码器；每个推流者的 offer 按其中列出的第一个应答 |
| `max_peer_connections` | 1024 | 推流者和观看者的 PeerConnection 总数；再多一个 offer 应答 `503` |
| `max_streams` | 64 | 推送中的流；再多一个推流者应答 `503` |
| `max_viewers` | 256 | 一路流的观看者；再多一个应答 `503` |
| `max_bitrate` | 8000000 | 推流者每秒可发送的比特数，用 REMB 告知并强制执行，允许两秒的突发 |
| `max_sdp_bytes` | 32 KiB | offer 的大小；更大的应答 `413` |
| `connect_timeout` | 30s | 从应答到 PeerConnection 连上 |
| `ice_timeout` | 30s | 已连上的对端可以沉默多久 |
| `key_frame_interval` | 500ms | 转给推流者的两次关键帧请求之间的间隔 |

数据通道、第一层之后的 simulcast 层、RTX 与 FEC，以及 transport-wide 拥塞控制不被转发。会话属于 `webrtc.media`，
不属于路由。

### proxy

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `listen` | — | 必填。对端到达的 UDP 端口，如 `:8190`：被代理的 answer 把它写成唯一的候选 |
| `public_ips` | 无 | 告知对端到达该端口的地址 |
| `max_sessions` | 4096 | 同时存在的会话，从 offer 起计；再多一个应答 `503` |
| `max_relays` | 4096 | 同时中继的对端地址，各占一个 socket；超过时新地址被丢弃 |
| `session_timeout` | 30s | 没有任何中继的会话，超时后被遗忘，其 URL 应答 `404` |
| `idle_timeout` | 30s | 不发送任何数据的对端地址失去其中继 |
| `relay_ip` | 无 | 通往媒体服务器的 socket 从本机哪个地址发出；与媒体服务器同一地址族 |
| `port_min`、`port_max` | 无 | 通往媒体服务器的 socket 的本地端口范围；被占用的端口会跳过，范围占满时丢弃该对端地址 |
| `access` | 无 | 对端地址的允许与拒绝列表，先于全局 `access` 决定 |

一个新的对端地址必须先发 STUN Binding 请求，其 USERNAME 以某个会话的 ufrag 开头，MESSAGE-INTEGRITY 由该会话的
ice-pwd 签名；先到的其他数据一律丢弃。一个会话最多中继对端的 8 个地址，最多保留 4 个 ufrag。

## .conf 配置

```fit
listener web :8080

webrtc {
	turn {
		listen :3478
		listen_tcp :3478
		realm example.com
		secret a-secret-shared-with-the-web-service
		users alice { password a-password }
		relay_ip 10.0.0.5
		public_ip 203.0.113.5
		port_min 49152
		port_max 49999
		max_allocations 1024
		max_permissions 64
		max_channels 64
		user_bandwidth 2097152
		allowed_peers 10.0.0.5:8189
	}

	media {
		listen :8189
		public_ips 203.0.113.5
		ice_servers { urls stun:stun.example.com:3478 }
		turn_urls turn:203.0.113.5:3478
		max_peer_connections 1024
		max_streams 16
		max_viewers 100
		max_bitrate 4000000
		connect_timeout 30s
		ice_timeout 30s
		key_frame_interval 500ms
	}
}

route whip { match { path /whip/* }; handler whip }
route whep { match { path /whep/* }; handler whep }
```

`listen_tls :5349` 与它的 `tls.certificates` 在应用配置时读取证书文件，所以示例里没有写；被监视的文件续期后用于
下一次握手，什么都不必重启。浏览器把 TURN 作为 ICE 服务器使用：
`{urls: ["turn:turn.example.com:3478", "turn:turn.example.com:3478?transport=tcp",
"turns:turn.example.com:5349?transport=tcp"], username, credential}`，其中限时凭据由知道 `secret` 的 Web 服务
生成。

## 相关文档

- [WebRTC](../webrtc.zh-CN.md)：[TURN 服务器](../webrtc.zh-CN.md#turn-服务器)与它的
  [凭据](../webrtc.zh-CN.md#凭据)、[WHIP 和 WHEP](../webrtc.zh-CN.md#whip-和-whep)与它的
  [认证](../webrtc.zh-CN.md#认证)、[代理媒体服务器](../webrtc.zh-CN.md#代理媒体服务器)。
- [配置参考：WebRTC](../reference/config.md#webrtc)：全部字段；
  [`webrtc_proxy`](../reference/config.md#http-handlers-webrtc_proxy)、
  [`whip`](../reference/config.md#http-handlers-whip)、
  [`whep`](../reference/config.md#http-handlers-whep)。
- [媒体认证](21-media-auth.zh-CN.md)：`auth` 引用的策略与密钥环。
- [流媒体](20-streaming.zh-CN.md)：`stream_whip` 与 `stream_whep` 用的另一个端口（`streaming.webrtc`）。
- [反向代理](15-reverse-proxy.zh-CN.md)：这些处理器的路由。
- [决定 029](../decisions/029-webrtc.zh-CN.md)：WebRTC 角色的设计。
