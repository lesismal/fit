# WebRTC

[English](webrtc.md) | [简体中文](webrtc.zh-CN.md)

fit 用 fib 的 WebRTC 各包提供 WebRTC 服务，与网关一起运行在其引擎上（[决定 029](decisions/029-webrtc.zh-CN.md)）：
TURN 服务器；以 fit 为媒体服务器的 WHIP 和 WHEP；以及 fit 作为其他媒体服务器的前端，代理它们的 WHIP 和 WHEP，
经一个 UDP 端口中继它们的媒体。

## TURN 服务器

两个 WebRTC 对端如果在不让打洞的 NAT 后面，或者所在网络封锁了它们之间的 UDP，就经由 TURN 中继（RFC 8656）相连：
各自向服务器申请一个中继地址，即 *allocation*，服务器在这个地址与客户端许可的对端之间转发数据报。`webrtc.turn`
把 fib 的 TURN 服务器作为 fit 配置中的一个角色运行在 listener 的引擎上，可以与网关的路由一起运行，也可以单独运行：

```fit
webrtc {
	turn {
		listen :3478
		realm example.com
		secret a-secret-shared-with-the-web-service
		users alice { password a-password }
		relay_ip 10.0.0.5
		public_ip 203.0.113.5
		port_min 49152
		port_max 49999
	}
}
```

同样的配置写成 JSON：

```json
{
  "webrtc": {
    "turn": {
      "listen": [":3478"],
      "realm": "example.com",
      "secret": "a-secret-shared-with-the-web-service",
      "users": [{"name": "alice", "password": "a-password"}],
      "relay_ip": "10.0.0.5",
      "public_ip": "203.0.113.5",
      "port_min": 49152,
      "port_max": 49999
    }
  }
}
```

浏览器把它作为 ICE 服务器使用：`{urls: "turn:turn.example.com:3478", username, credential}`。服务器还在同一端口上
不需凭据地应答 STUN Binding 请求，所以同一地址也可作为 `stun:` 服务器。每个字段见
[配置参考](reference/config.md#webrtc)。

### 凭据

- **`users`**：固定的用户名，各带一个 `password`，或者由它导出的 `key`，即 coturn 的 `turnadmin -k` 打印的值
  （`name:realm:password` 的 MD5，十六进制），这样不必保存密码本身。使用 `admin.store` 时，配置以及其中的用户都加密保存。
- **`secret`**：coturn `use-auth-secret` 的限时凭据，大多数 WebRTC 服务都这样发放凭据。知道 secret 的 Web 服务为每个
  会话生成用户名 `<以 Unix 秒计的过期时间>:<任意内容>` 和密码 `base64(HMAC-SHA1(secret, username))`；服务器在过期前接受
  它们，不需要用户列表。在 Go 中可用 fib 的 `turn.EphemeralCredentials(secret, user, expiry)` 生成；在 shell 中：

  ```sh
  user="$(($(date +%s) + 3600)):alice"
  pass=$(printf %s "$user" | openssl dgst -sha1 -hmac "$secret" -binary | base64)
  ```

两者可以同时设置；先在 `users` 中查找用户名。密码按字节处理：非 ASCII 的密码必须先转换成 RFC 8265 OpaqueString
规定的形式再填写。

### 地址与防火墙

每个 allocation 的中继 socket 绑定在 `relay_ip` 上，它必须是本机的地址之一。在 1:1 NAT 后面（大多数云主机如此），把
`public_ip` 设为映射到它的地址：告诉客户端的是这个地址。`port_min` 和 `port_max` 把中继端口限制在一个范围内，便于防火墙
开放；两者都不设时由内核选择端口。每个 allocation 在 fit 引擎的每个事件循环（`engine.event_loops`）上各占一个 socket。

### fit 不会中继到的对端

客户端要求发到哪里就发到哪里的中继，是进入其所在网络的通道。对回环、私有（RFC 1918 和唯一本地地址）、链路本地、组播、
共享（RFC 6598）以及其他内部或保留地址段上的对端，permission 和 channel 都会被拒绝，范围与 fib 的 `DeniedByDefault`
相同。`allowed_peers` 可放行其中一部分，用于私有网络上的对端；`denied_peers` 可拒绝更多地址。同一服务器的两个客户端
会中继到对方的中继地址，也就是服务器自己的地址：`relay_ip` 是私有地址时，要把它列入 `allowed_peers`。这样中继就能到达
该地址的每个端口，包括 fit 自己的 listener：在这个地址上只对客户端的对端开放中继端口范围，或者让 listener 监听在别的
地址上。fib 目前还不能只放行某个地址的中继端口（[A57](fib-api-requests.zh-CN.md#a57turn-服务器的限制与钩子)）。

### 上限

| 字段 | 默认值 | 限制 |
| --- | --- | --- |
| `max_allocations` | 1024 | 同时持有的 allocation 数；再多一个以 508 拒绝 |
| `max_allocations_per_user` | 16 | 一个用户名的 allocation 数；再多一个以 486 拒绝 |
| `user_bandwidth` | 2 MiB | 一个用户名每个方向每秒中继的字节数，允许一秒的突发；超出的数据报被丢弃 |
| `default_lifetime`、`max_lifetime` | 10m、1h | allocation 的生存期，由客户端刷新 |
| `nonce_lifetime` | 10m | nonce 的有效期，过期后回 438 |

allocation 的 permission 不刷新时存活五分钟，channel 存活十分钟；fib 目前还不限制一个 allocation 持有的数量（A57）。
fit 的 `limits.max_conns` 把客户端和对端的 UDP 地址与其他连接一起计数。

### 重载

重载修改 `users`、`secret`、`allowed_peers`、`denied_peers` 或 `user_bandwidth` 时，从下一个请求起生效，已有的
allocation 继续存在。修改其他字段会让服务器重新启动：它的 allocation 结束，浏览器会重启 ICE。修改 `listen` 时，先绑定
新地址再关闭旧地址，除非两者有相同的地址。

### 指标与日志

Admin API 的 `/metrics` 和 OpenTelemetry 导出中有 `fit_turn_allocations`、
`fit_turn_relayed_bytes_total{direction="to_peer"|"from_peer"}`、`fit_turn_dropped_total{reason="bandwidth"}`、
`fit_turn_peers_refused_total` 和 `fit_turn_restarts_total`。服务器的消息写入 `webrtc` 日志。

### 尚未支持

以下有待 fib 提供（[fib API 请求](fib-api-requests.zh-CN.md)）：

- 经 TCP 和 TLS 的 TURN，用于封锁 UDP 的网络中的客户端（[A55](fib-api-requests.zh-CN.md#a55经-tcp-和-tls-的-turn)）。
- 绑定中继地址和打开对端时不让 fib worker 等待（[A56](fib-api-requests.zh-CN.md#a56从不让-worker-等待的-turn-服务器)）。
- allocation 的 permission 和 channel 上限、带端口的对端规则，以及用于日志和指标的事件
  （[A57](fib-api-requests.zh-CN.md#a57turn-服务器的限制与钩子)）。
- 不重启就修改生存期和配额，以及排空 allocation 的重启（[A58](fib-api-requests.zh-CN.md#a58原地修改和排空-turn-服务器)）。

## WHIP 和 WHEP

fit 是一个小型媒体服务器（SFU）：推流者用 WHIP（RFC 9725）发送一路流，如 OBS、浏览器或 GStreamer，观看者用 WHEP
（draft-ietf-wish-whep）播放；fit 把推流者发来的每个 RTP 包转发给每个观看者，不做解码。`webrtc.media` 是所有
PeerConnection 共用的 UDP 端口，handler 模块 `whip` 和 `whep` 在任意路由上提供 HTTP 一侧：

```fit
route {
	match { path /whip/* }
	middleware jwt {
		jwks_url https://auth.example.com/.well-known/jwks.json
		audience whip
	}
	handler whip
}

route {
	match { path /whep/* }
	handler whep
}

webrtc {
	media {
		listen :8189
		public_ips 203.0.113.5
		ice_servers { urls stun:stun.example.com:3478 }
		max_streams 16
		max_viewers 100
		max_bitrate 4000000
	}
}
```

JSON 写法：

```json
{
  "routes": [
    {"match": {"path": ["/whip/*"]},
     "middleware": [{"module": "jwt", "params": {"jwks_url": "https://auth.example.com/.well-known/jwks.json", "audience": ["whip"]}}],
     "handler": {"module": "whip"}},
    {"match": {"path": ["/whep/*"]}, "handler": {"module": "whep"}}
  ],
  "webrtc": {
    "media": {
      "listen": ":8189",
      "public_ips": ["203.0.113.5"],
      "ice_servers": [{"urls": ["stun:stun.example.com:3478"]}],
      "max_streams": 16,
      "max_viewers": 100,
      "max_bitrate": 4000000
    }
  }
}
```

推流者把 offer POST 到 `https://example.com/whip/cam1`，观看者把自己的 offer POST 到 `https://example.com/whep/cam1`：
流的名字是路径的最后一段，或者 handler 的 `stream` 参数（固定名字）。会话的 URL 是其 POST 的路径加上会话 ID，所以路由也必须
匹配它：`/whip/*` 可以，只有 `/live` 的路由则需要在旁边加上 `/live/*`。所有字段见[配置参考](reference/config.md#webrtc)。

### HTTP API

| 请求 | 应答 |
| --- | --- |
| 向端点 `POST` 一个 `application/sdp` 的 offer | `201 Created`，fit 的 answer（`application/sdp`），`Location` 为会话的 URL（端点路径加会话 ID），每个 ICE 服务器一个 `Link` 头 |
| `DELETE` 会话的 URL | `200`；推流者的流结束，其观看者的会话随之结束；已结束的会话为 `404` |
| `OPTIONS` | `204`，带 `Allow` 和 `Accept-Post: application/sdp` |
| `PATCH` 会话的 URL | `405`：见下文 |

拒绝：offer 不是 `application/sdp` 时 `415`；超过 `max_sdp_bytes` 时 `413`；无法解析或无法应答时 `400`；没有 fit
转发的编解码器的音视频，或观看者的 offer 收不了流的任何轨道时 `422`；流已有推流者时 `409`；观看未推送的流时 `404`；
超过 `max_peer_connections`、`max_streams` 或 `max_viewers`，以及 `webrtc.media` 未运行时 `503` 并带 `Retry-After`。

认证由路由的中间件完成：RFC 9725 的 bearer token 就是 `Authorization` 字段，由 `jwt` 校验，也可用 `basic_auth`、
`forward_auth` 等。DELETE 走同一条路由，会话 ID 是 128 位随机数，只有创建会话的一方知道。

fit 不用 trickle ICE 应答，也不支持 PATCH：fit 在 Mux 上是 ICE lite agent（RFC 8445 第 2.5 节），它的候选就是端口自己
的地址，全部在 answer 中；它从对端的连通性检查得知对端地址，所以对端 trickle 的候选没有用处。ICE 重启就是一个新会话：
客户端 DELETE 旧会话，再 POST 新的 offer。按 RFC 9725，收到 405 的客户端不再 trickle。

### 媒体

每个推流者的 offer 按每种媒体只应答一个编解码器：它自己列出的、`codecs` 中也有的第一个（未设置时为 opus、VP8、VP9、
H264 和 AV1）；观看者也以同一个应答，它的 offer 必须接受。观看者在其 offer 的各段上收到流的音视频，来源是对应推流者每段
的一个 TrackLocal：包在观看者自己的 SSRC 和密钥下重新保护，序号和时间戳保持推流者发出时的样子。

- **关键帧。** 新加入或丢包的观看者会请求关键帧（PLI 或 FIR）；无论观看者多少，fit 每个 `key_frame_interval`
  （500ms）至多向推流者发一次 PLI，观看者连上时也发一次。
- **码率。** fit 每秒用 REMB 告诉每个推流者 `max_bitrate`（8 Mbit/s），浏览器会遵守；超过的包被丢弃，允许两秒的突发。
- **离开。** 推流者 DELETE、它的 PeerConnection 失败，或它沉默超过 `ice_timeout`（30s），流和观看者的会话都结束；
  观看者离开只结束它自己的会话。应答后 `connect_timeout`（30s）内未连上的会话也会结束。断开或缓慢的观看者不会拖住
  推流者和其他观看者：fit 发的是 UDP，从不等待。
- **不转发的。** 数据通道、第一层之后的 simulcast 层（转发 simulcast 推流者最先到达的那一层）、RTX 和 FEC，以及
  fib 的 PeerConnection 不支持的 transport-wide 拥塞控制。每个观看者都按到达顺序收到推流者的包：没有按观看者的带宽
  估计或分层选择。

### ICE 服务器

fit 自己不需要 STUN 或 TURN 服务器：它的端口在自己的地址上可达，1:1 NAT 后则是 `public_ips`。防火墙封锁到它的 UDP
的对端需要在自己一侧使用 TURN 服务器；answer 的 `Link` 头告诉它 `ice_servers`，以及作为 `turn_urls` 的 fit 自己的
`webrtc.turn`，凭据由其 `secret` 生成，有效期 `turn_credential_ttl`（1h）：

```
Link: <stun:stun.example.com:3478>; rel="ice-server"
Link: <turn:turn.example.com:3478>; rel="ice-server"; username="1767225600:fit-5f1c..."; credential="..."; credential-type="password"
```

只有 `allowed_peers` 允许它到达媒体端口的地址时（`relay_ip` 是私有地址时需要），fit 的 TURN 服务器才会中继到那里。

### 上限

| 字段 | 默认值 | 限制 |
| --- | --- | --- |
| `max_peer_connections` | 1024 | 推流者和观看者的 PeerConnection 总数；再多一个 offer 应答 503 |
| `max_streams` | 64 | 推送中的流；再多一个推流者应答 503 |
| `max_viewers` | 256 | 一路流的观看者；再多一个应答 503 |
| `max_bitrate` | 8000000 | 推流者每秒可发送的比特数，用 REMB 告知并强制执行 |
| `max_sdp_bytes` | 32 KiB | offer 的大小；更大的应答 413 |
| `connect_timeout` | 30s | 从应答到 PeerConnection 连上 |
| `ice_timeout` | 30s | 已连上的对端可以沉默多久 |
| `key_frame_interval` | 500ms | 转给推流者的两次关键帧请求之间的间隔 |

推流者的包在它到达的 fib worker 上发给每个观看者，所以 `max_viewers` 也限制了这部分工作。fit 的 `limits.max_conns`
把每个对端的 UDP 地址和其他连接一起计数。

### 重载

会话属于 `webrtc.media`，不属于路由：保持 `listen` 和 `public_ips` 不变的重载会保留所有会话，其他字段对下一个 offer
生效（`max_bitrate` 和 `key_frame_interval` 立即生效）。修改 `listen` 或 `public_ips` 会让媒体服务器重新启动：会话
结束，每个对端都收到 DTLS close，推流者和观看者重新 POST offer。去掉 `webrtc.media` 会结束所有会话，路由应答 503。

### 指标与日志

Admin API 的 `/metrics` 中有 `fit_webrtc_media_peer_connections{role}`、`fit_webrtc_media_streams`、
`fit_webrtc_media_sessions_total{role}`、`fit_webrtc_media_sessions_ended_total{reason}`、
`fit_webrtc_media_refused_total{reason}`、`fit_webrtc_media_received_packets_total`、
`fit_webrtc_media_received_bytes_total`、`fit_webrtc_media_dropped_total{reason="bitrate"}`、
`fit_webrtc_media_key_frame_requests_total{result="sent"|"held"}` 和 `fit_webrtc_media_restarts_total`。
媒体服务器的消息写入 `webrtc` 日志。

## 代理媒体服务器

许多媒体服务器可以放在一个 fit 后面：其他主机上 fit 自己的 `whip` 和 `whep`，或任何 WHIP、WHEP 服务器，如 MediaMTX、
Janus、LiveKit 的 WHIP ingress。推流端和观看端只知道 fit 的一个 HTTP 端点和一个 UDP 端口。处理器 `webrtc_proxy`
把它们的信令转给由媒体服务器组成的上游，`webrtc.proxy` 把每个对端的媒体中继到应答它的那台媒体服务器：

```fit
upstream {
	name media-servers
	addresses 10.0.0.2:8080 10.0.0.3:8080
	health_check {
		path /healthz
	}
}

route {
	match { path /whip/* /whep/* }
	handler webrtc_proxy {
		upstream media-servers
		media_addrs {
			10.0.0.3:8080 10.0.0.3:8189
		}
	}
}

webrtc {
	proxy {
		listen :8190
		public_ips 203.0.113.5
	}
}
```

JSON 写法相同：

```json
{
  "upstreams": [{"name": "media-servers", "addresses": ["10.0.0.2:8080", "10.0.0.3:8080"],
                 "health_check": {"path": "/healthz"}}],
  "routes": [
    {"match": {"path": ["/whip/*", "/whep/*"]},
     "handler": {"module": "webrtc_proxy",
                 "params": {"upstream": "media-servers", "media_addrs": {"10.0.0.3:8080": "10.0.0.3:8189"}}}}
  ],
  "webrtc": {"proxy": {"listen": ":8190", "public_ips": ["203.0.113.5"]}}
}
```

### 一个会话的经过

1. 推流端把 offer POST 给 fit。fit 收下整个 offer（不超过 `max_sdp_bytes`），去掉其中的候选地址，发给上游选中的
   媒体服务器：上游的负载均衡、健康检查，以及请求自带的字段（包括 `Authorization`）都照常生效。
2. 媒体服务器应答 `201`。fit 读出其中的 `a=ice-ufrag`、`a=ice-pwd`，以及要把对端中继到的地址：`media_addrs`
   中该媒体服务器的条目，否则是 answer 中第一个 UDP host 候选，再否则是 server-reflexive 候选，IPv4 优先。fit 把
   answer 的候选地址和 `c=` 行换成 `webrtc.proxy` 的地址，以自己的 ID 记下会话，应答的 `Location` 是 fit 的 URL：
   POST 的路径后接这个 ID。
3. 推流端的 ICE 检查到达 fit 的端口。一个新的对端地址必须先发 STUN Binding 请求，其 USERNAME 以某个会话的 ufrag
   开头（`<ufrag>:<对端的 ufrag>`），MESSAGE-INTEGRITY 由该会话的 ice-pwd 签名；fit 随即为这个地址单独打开一个
   通往媒体服务器的 UDP socket，双向中继每个数据报，STUN、DTLS、SRTP 一视同仁。先到的其他数据一律丢弃。
4. 媒体服务器把 fit 的 socket 看作它的对端，即 peer-reflexive 候选，这是 ICE 允许的。它没有拿到对端的任何候选，
   所以从不绕过 fit 直接发送。
5. 对 fit URL 的 DELETE 转给应答的媒体服务器，无论媒体服务器如何回应，fit 都释放该会话。

每个对端地址都有自己通往媒体服务器的 socket，因为单端口的媒体服务器（如 fit 的 `webrtc.media`）按地址区分对端：
所有中继共用一个 socket，对它来说就成了同一个对端。从媒体服务器发回某个 socket 的数据报，只回到打开它的那个对端地址。

### 信令

- **Location 和 Link。** 媒体服务器的 `Location`（相对或绝对）记为会话在那边的路径；绝对地址中的主机不使用，
  因为请求总是发往应答的媒体服务器。ICE 服务器的 `Link`（`rel="ice-server"`）原样传递；指向会话 URL 之下的 Link
  改写到 fit 中对应 URL 之下，其余的丢弃。
- **会话的 URL。** DELETE、PATCH、OPTIONS 以及发往会话 URL 之下的请求，都转给应答的媒体服务器，靠 fit 的 ID
  而非 cookie 保持粘滞。上游已不再列出其媒体服务器的会话应答 `502`；它的 DELETE 仍会释放会话。
- **PATCH。** 只 trickle 候选的 PATCH 由 fit 直接应答 `204`：无论对端的候选是什么，媒体服务器都经由 fit 到达对端。
  ICE 重启（带 `a=ice-ufrag` 的 PATCH）去掉候选后转给媒体服务器，其应答中的新 ufrag 与第一个 ufrag 一样被路由，
  候选同样被替换。
- **状态码。** offer 不是 `application/sdp` 时 `415`；请求超过 `max_sdp_bytes` 时 `413`；超过 `max_sessions`，或
  `webrtc.proxy` 未运行时 `503`；媒体服务器无法连接、answer 超过 `max_sdp_bytes`、answer 没有 ufrag 或没有 fit
  能中继到的候选时 `502`（这种会话 fit 会到媒体服务器上 DELETE 掉）；在 `timeout` 内没有应答时 `504`。其他应答原样传递。

整个过程不等待：offer 在最后一次 body 回调里经 fib 的异步客户端发往上游，answer 在客户端的回调里写回；客户端离开
会取消请求。

### 上限

| 字段 | 默认值 | 限制 |
| --- | --- | --- |
| `max_sessions` | 4096 | 同时存在的会话，从 offer 起计；再多一个应答 `503` |
| `max_relays` | 4096 | 同时中继的对端地址，各占一个 socket；超过时新地址被丢弃 |
| `session_timeout` | 30s | 没有任何中继的会话，从应答或最后一个中继结束起，超时后被遗忘 |
| `idle_timeout` | 30s | 不发送任何数据的对端地址失去其中继，不论媒体服务器是否还在发送 |
| `webrtc_proxy` 的 `max_sdp_bytes` | 32 KiB | 请求和 answer 的大小 |
| `webrtc_proxy` 的 `timeout` | 10s | 从请求的最后一个字节到完整读完 answer |

一个会话最多中继对端的 8 个地址：对端每个发起检查的候选一个，迁移后还会更多。一个会话最多保留 4 个 ufrag，含 ICE
重启产生的。中继在其 socket 打开期间最多暂存 8 个数据报。

### ICE 重启、consent 与迁移的对端

ICE 的 consent 检查大约每 5 秒一次，像其他数据报一样被中继，使中继保持打开。对端迁移到别的网络或别的 NAT 映射后，
会从新地址再次检查，从而打开一个通往同一媒体服务器的新中继；旧的在 `idle_timeout` 后结束。经 PATCH 的 ICE 重启
保留原会话；像 fit 自己的 whip 那样以新会话方式重启，则是一次新的 POST。

fit 做不到的：

- 只在 answer 之后用 trickle 给出候选的媒体服务器应答 `502`，除非 `media_addrs` 给出它带端口的地址。
- answer 中的候选分布在多个地址时，只中继到其中一个；TCP 上的 ICE、以及只能经 TURN 到达的媒体服务器不被中继。
- 会话 URL 之下的 URL 按整个 body 和整个应答代理，不超过 `max_sdp_bytes`：流式扩展，如 WHEP 的 server-sent
  events，经过 fit 无法工作。
- 通往媒体服务器的 socket 使用内核选的地址和端口，fit 与它们之间的防火墙必须放行；fib 目前还不能把它们绑定到
  指定地址和端口范围（[A59](fib-api-requests.zh-CN.md#a59从指定的本地地址和端口范围发起拨号)）。
- 媒体服务器看到的是 fit 的地址，而不是对端的。

### 重载

会话属于 `webrtc.proxy` 而不是路由：保留 `listen` 的重载保留会话及其中继，其他字段从下一次起生效，`public_ips`
从下一个 answer 起生效。`listen` 改变会结束所有中继和会话，对端需重新 POST offer。被重载移出上游的媒体服务器，
其中继保留，而其会话的请求应答 `502`。

### 指标

`fit_webrtc_proxy_sessions`、`fit_webrtc_proxy_relays`、`fit_webrtc_proxy_sessions_total`、
`fit_webrtc_proxy_refused_total`、`fit_webrtc_proxy_sessions_ended_total{reason="delete"|"expired"|"reload"}`、
`fit_webrtc_proxy_relays_total`、
`fit_webrtc_proxy_dropped_total{reason="not_stun"|"unknown_ufrag"|"integrity"|"limit"}`、
`fit_webrtc_proxy_relayed_bytes_total{direction="to_backend"|"to_client"}` 和
`fit_webrtc_proxy_restarts_total`。信令请求计入上游自己的指标。
