# WebRTC

[English](webrtc.md) | [简体中文](webrtc.zh-CN.md)

配置示例（.conf 格式，在 [fit-examples](https://github.com/lesismal/fit-examples) 中）：[webrtc-turn](https://github.com/lesismal/fit-examples/blob/main/webrtc-turn/fit.conf), [whip-whep](https://github.com/lesismal/fit-examples/blob/main/whip-whep/fit.conf), [webrtc-proxy](https://github.com/lesismal/fit-examples/blob/main/webrtc-proxy/fit.conf)。

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
		listen_tcp :3478
		listen_tls :5349
		tls {
			certificates {
				cert_file /etc/fit/turn.example.com.pem
				key_file /etc/fit/turn.example.com.key
			}
		}
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
      "listen_tcp": [":3478"],
      "listen_tls": [":5349"],
      "tls": {"certificates": [{"cert_file": "/etc/fit/turn.example.com.pem", "key_file": "/etc/fit/turn.example.com.key"}]},
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

浏览器把它作为 ICE 服务器使用：`{urls: ["turn:turn.example.com:3478",
"turn:turn.example.com:3478?transport=tcp", "turns:turn.example.com:5349?transport=tcp"], username, credential}`。
服务器还在其 UDP 端口上不需凭据地应答 STUN Binding 请求，所以同一地址也可作为 `stun:` 服务器。每个字段见
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

### TCP 和 TLS

WebRTC 服务之所以运行 TURN，正是为了封锁 UDP 的网络，以及只放行 443 端口上 TLS 的网络。`listen_tcp` 经 TCP 提供 TURN
（`turn:host:port?transport=tcp`），`listen_tls` 经 TLS 提供（`turns:host:port?transport=tcp`，常用 5349 或 443），证书取自
`tls.certificates` 中的文件，并像 listener 的证书一样被监视：续期后的文件用于下一次握手，什么都不必重启。无论客户端用哪种
传输，到对端的中继都是 UDP。一条连接就是一个客户端：连接关闭时它的 allocation 结束，没有 allocation 的连接在
`stream_idle_timeout` 后关闭，下一条消息声称超过 `max_stream_message` 的连接立即关闭。TCP 地址和 TLS 地址不能相同；UDP
地址和 TCP 地址可以相同，如上面的 `:3478`。

### 地址与防火墙

每个 allocation 的中继 socket 绑定在 `relay_ip` 上，它必须是本机的地址之一。在 1:1 NAT 后面（大多数云主机如此），把
`public_ip` 设为映射到它的地址：告诉客户端的是这个地址。`port_min` 和 `port_max` 把中继端口限制在一个范围内，便于防火墙
开放；两者都不设时由内核选择端口。每个 allocation 在 fit 引擎的每个事件循环（`engine.event_loops`）上各占一个 socket。
这些 socket 在 fib 服务器自己的 goroutine 上打开，绝不在服务请求的 worker 上。

### fit 不会中继到的对端

客户端要求发到哪里就发到哪里的中继，是进入其所在网络的通道。对回环、私有（RFC 1918 和唯一本地地址）、链路本地、组播、
共享（RFC 6598）以及其他内部或保留地址段上的对端，permission 和 channel 都会被拒绝，范围与 fib 的 `DeniedByDefault`
相同。`allowed_peers` 可放行其中一部分，用于私有网络上的对端；`denied_peers` 可拒绝更多地址；两者都接受带端口的地址，
如 `10.0.0.9:8189`，只针对该端口。

服务器自己的地址 `relay_ip` 和 `public_ip` 另作处理。同一服务器的两个客户端会中继到对方的中继地址，它们就在这些地址上，
所以服务器会中继到自己地址上它持有的中继地址的端口，不论 `allowed_peers` 是否覆盖它们，也会中继到 `allowed_peers` 带端口
列出的端口，如同一地址上的 `webrtc.media` 写作 `10.0.0.5:8189`。它绝不中继到这些地址的其他端口，包括 fit 自己的 listener，
不论 `allowed_peers` 中的前缀怎么写。检查能看到每个数据报的端口：permission 只针对地址，它放行的数据报逐个按端口检查。

### 上限

| 字段 | 默认值 | 限制 |
| --- | --- | --- |
| `max_allocations` | 1024 | 同时持有的 allocation 数；再多一个以 508 拒绝 |
| `max_allocations_per_user` | 16 | 一个用户名的 allocation 数；再多一个以 486 拒绝 |
| `max_permissions` | 64 | 一个 allocation 许可的对端地址数，channel 的对端也计入；再多一个以 508 拒绝 |
| `max_channels` | 64 | 一个 allocation 绑定的 channel 数；再多一个以 508 拒绝 |
| `user_bandwidth` | 2 MiB | 一个用户名每个方向每秒中继的字节数，允许一秒的突发；超出的数据报被丢弃 |
| `default_lifetime`、`max_lifetime` | 10m、1h | allocation 的生存期，由客户端刷新 |
| `nonce_lifetime` | 10m | nonce 的有效期，过期后回 438 |
| `max_stream_message` | 8 KiB | TCP 或 TLS 客户端最大的消息，也是其连接缓冲的上限 |
| `stream_idle_timeout` | 30s | 没有 allocation 的 TCP 或 TLS 连接 |

allocation 的 permission 不刷新时存活五分钟，channel 存活十分钟。fit 的 `limits.max_conns` 把 TCP 和 TLS 连接与
listener 接受的其他连接一起计数；客户端和对端的 UDP 地址不计入其中，由上面的 allocation 及其 permission 限制。

### 重载

重载原地修改 `users`、`secret`、`allowed_peers`、`denied_peers`、`user_bandwidth`、配额（`max_allocations`、
`max_allocations_per_user`、`max_permissions`、`max_channels`）、生存期和证书，从下一个请求或握手起生效：已有的
allocation 继续存在，超过调低后配额的保留到结束。

修改 `listen`、`listen_tcp` 或 `listen_tls` 时，在新地址上启动新服务器，旧服务器排空：不再接受新的 allocation（508），
已有的继续中继和刷新，直到它们结束或 fit 的排空超时（`fit run -shutdown-timeout`，30s）到期，然后关闭。修改 `realm`、
`relay_ip`、`public_ip`、端口范围、`nonce_secret` 或字节流上限也会启动新服务器：地址同时改变时在旧服务器旁启动、旧的排空；
地址不变时取而代之、旧的 allocation 结束，因为两个服务器不能共用一个地址；浏览器随后重启 ICE。去掉 `webrtc.turn` 或停止
fit 会立即关闭它。

### 指标与日志

Admin API 的 `/metrics` 和 OpenTelemetry 导出中有：

- gauge：`fit_turn_allocations`、`fit_turn_pending_allocations`（等待中继 socket 的）、`fit_turn_permissions`、
  `fit_turn_channels` 和 `fit_turn_draining_servers`；
- `fit_turn_relayed_bytes_total{direction="to_peer"|"from_peer"}`；
- `fit_turn_allocated_total`、`fit_turn_refreshed_total` 和 `fit_turn_freed_total{reason="deleted"|"expired"|"closed"}`；
- `fit_turn_refused_total{code}`，凭据通过后被拒绝的请求，按错误码（403 对端被拒，486 和 508 配额、上限和排空；
  400、420、437、441、442、443 以及 `other`，格式错误或不合时宜的请求），以及 `fit_turn_peers_refused_total`，即其中的
  403；`fit_turn_auth_failures_total`，未通过的凭据；
- `fit_turn_dropped_total{reason="bandwidth"|"unsent"}`，超过 `user_bandwidth` 的数据报和没有连接可发的数据报，以及
  `fit_turn_unrelayed_total`，服务器没有处理的所有数据报：格式错误、没有 allocation、permission 或 channel、发往对端
  规则拒绝的端口，或超过 `user_bandwidth`（前者也计入这些）；
- `fit_turn_restarts_total`，重载启动的新服务器数。

服务器的消息写入 `webrtc` 日志，前缀 `webrtc.turn: `：监听和中继的地址以 `info` 级别记录，结束其 allocation 的重启以
`warn` 级别记录，每次 allocation 的建立、刷新和释放、每次拒绝和每次凭据失败以 `debug` 级别记录。

### TURN、P2P 与 tunnel

`webrtc.turn` 是 fit 唯一的 TURN 服务器：[决定 025](decisions/025-p2p.zh-CN.md) 中的 TURN 服务器就是它。计划中的 P2P
隧道（[P2P 与 NAT 穿透](dev-plan.zh-CN.md#p2p-与-nat-穿透)）以 fib 的 `webrtc/stun`、`webrtc/turn` 和 `webrtc/ice`
作为 STUN 客户端、TURN 客户端和 ICE agent，在 NAT 后的两台主机之间打不通洞时，会像其他 ICE agent 一样用它中继。
[tunnel](tunnel.zh-CN.md) 则是另一回事：它的角色经由 node 和 proxy 主动连出的 `tunnel.hub` 转发 TCP 服务，每个字节都经
fit 自己的链路中继；TURN 服务器中继的是标准 ICE agent（包括浏览器）的 UDP 数据报，只在它们找不到直连路径时使用，也不
知道它们背后有什么服务。

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
流的名字是路径的最后一段，或者 handler 的 `stream` 参数（固定名字）；名字由 1 到 128 个字母、数字、`.`、`_`、`~`
和 `-` 组成，最后一段不是合法名字的路径应答 `404`。会话的 URL 是其 POST 的路径加上会话 ID，所以路由也必须
匹配它：`/whip/*` 可以，只有 `/live` 的路由则需要在旁边加上 `/live/*`。所有字段见[配置参考](reference/config.md#webrtc)。

### HTTP API

| 请求 | 应答 |
| --- | --- |
| 向端点 `POST` 一个 `application/sdp` 的 offer | `201 Created`，fit 的 answer（`application/sdp`），`Location` 为会话的 URL（端点路径加会话 ID），每个 ICE 服务器一个 `Link` 头 |
| `DELETE` 会话的 URL | `200`；推流者的流结束，其观看者的会话随之结束；已结束的会话为 `404` |
| `OPTIONS` 端点 | `204`，带 `Allow: POST, OPTIONS` 和 `Accept-Post: application/sdp` |
| `OPTIONS` 会话的 URL | `204`，带 `Allow: DELETE, OPTIONS` |
| `PATCH` 会话的 URL | `405`：见下文 |

拒绝：offer 不是 `application/sdp` 时 `415`；超过 `max_sdp_bytes` 时 `413`；无法解析或无法应答时 `400`；没有 fit
转发的编解码器的音视频，或观看者的 offer 收不了流的任何轨道时 `422`；流已有推流者时 `409`；观看未推送的流，或应答期间
流已结束时 `404`；超过 `max_peer_connections`、`max_streams` 或 `max_viewers` 时 `503` 并带 `Retry-After: 5`，
`webrtc.media` 未运行时 `503`。端点对其他方法应答 `405`。

认证由处理器的 `auth`（一条 [`media_auth`](reference/config.md#media-auth) 策略）或路由的中间件完成，也可两者
同时使用；见下文 [认证](#认证)。

fit 不用 trickle ICE 应答，也不支持 PATCH：fit 在 Mux 上是 ICE lite agent（RFC 8445 第 2.5 节），它的候选就是端口自己
的地址，全部在 answer 中；它从对端的连通性检查得知对端地址，所以对端 trickle 的候选没有用处。ICE 重启就是一个新会话：
客户端 DELETE 旧会话，再 POST 新的 offer。按 RFC 9725，收到 405 的客户端不再 trickle。

### 认证

`auth` 指定配置 `media_auth` 中的一条策略，规定谁可以推送、播放哪些流；处理器在读取 offer 的 body 之前用它检查 POST：

```fit
media_auth live {
	secret a-secret-of-at-least-16-bytes
	public play
	tokens studio {
		token_sha256 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
		publish live/*
	}
	users alice { password alice-password; publish alice-*; play * }
}

route {
	match { path /whip/* }
	handler whip { auth live }
}

route {
	match { path /whep/* }
	handler whep { auth live }
}
```

- **凭据。** `tokens` 中的静态 token，写明文或写其 `token_sha256`；fit 用策略的 `secret` 签发的 token，由
  `fit media-token -policy live -publish 'live/*' -ttl 1h` 打印，带有主体、过期时间和流；或 `users` 中的用户及其密码。
  token 以 RFC 9725 的 `Authorization: Bearer` 发送，或作为查询参数 `token`（供无法设置请求头的客户端；访问日志写出的
  URI 中为 `token=REDACTED`），或作为 cookie `fit_media_token`；用户以 `Authorization: Basic` 发送。
- **权限。** 每个 token 和用户列出可以 `publish` 和 `play` 的流的模式：`*` 匹配任意一串字符，所以 `live/*`、
  `room1-*` 和 `*` 都可用，空列表不授予任何权限。`public: ["play"]` 让任何人都能观看，推流仍需凭据。
- **应答。** 没有凭据时 `401` 并带 `WWW-Authenticate: Bearer realm="fit"`；token 未知、过期或被篡改，或密码错误时
  同样 `401`，并带 `error="invalid_token"`；凭据不允许对该流做该动作时 `403`；bcrypt 检查排队超出上限时 `503` 并带
  `Retry-After: 1`。
- **会话的 URL。** 它的 DELETE 和 OPTIONS 不需要凭据：会话 ID 是 128 位随机数，只有创建会话的一方知道，正是
  RFC 9725 所依赖的凭证；长时间推流期间过期的 token 也不应妨碍推流者挂断。
- **不占用 worker。** token 按其 SHA-256 查找或校验签名、明文密码以常数时间比较，都在 worker 上完成；`password_hash`
  在自己的小线程池上用 bcrypt 检查（CPU 数的四分之一，最多 256 个等待），期间请求等待；检查通过的密码以带密钥的
  SHA-256 记住，下次检查立即完成。
- **重载。** 重载在原处修改策略，作用于下一个 offer；已建立的会话保留。处理器的 `auth` 指向配置中没有的策略时，配置
  无法通过。

路由的中间件仍然先执行，所以策略可以放在 `jwt`、`forward_auth` 或 IP 白名单之后。所有字段见
[配置参考](reference/config.md#media-auth)，设计见 [决定 030](decisions/030-media-auth-and-pages.zh-CN.md)。

#### 从一个路由签发 token

`fit media-token` 为运维人员打印一个 token；业务后端要为许多用户、分组和并发会议签发大量 token 时，改为经它自己的
路由调用处理器 `media_tokens`。策略的 `keys` 密钥环给每个分片——一个会议、一个分组、一个区域——自己的密钥：一批
token 用一个密钥签发，校验时依次尝试环中的每个密钥，而吊销只轮换一个密钥的 secret 文件，所以只有该密钥签发的 token
被拒绝，其他分片照常运行：一次吊销是一个分片的故障，而不是整个策略的。路由的中间件认证该后端——这里是 `jwt`——而
模块自身不检查任何调用方凭据：它不是 Admin API，所以后端从不持有 Admin API 的全权 token。

```fit
media_auth live {
	keys {
		key k1
		secret_file /etc/fit/media-secrets/k1
	}
	keys {
		key k2
		secret_file /etc/fit/media-secrets/k2
	}
	keys {
		key k3
		secret_file /etc/fit/media-secrets/k3
	}
	public play
}

route media-tokens {
	match {
		path /media-tokens /media-tokens/*
	}
	middleware jwt {
		jwks_url https://auth.example.com/.well-known/jwks.json
		audience media-tokens
	}
	handler media_tokens {
		auth live
		prefixes mtg-
		max_ttl 24h
		max_tokens 500
	}
}
```

密钥环中的每个密钥是一个分片，按会议或分组的需要取 k1 到 k8，各有一个 `secret_file`；密钥环及其密钥都来自配置，所以
新增一个分片是在配置里加一个密钥再重载，而不是调用 API。一批 token 用 `"key": "k2"` 指明签发的密钥，整批都用它；
不指明时用环中的第一个密钥，这里是 k1。token 不携带密钥 id：它对环中的每个密钥都校验，所以轮换之前签发的其他密钥的 token
仍然可用，只有吊销它自己的密钥才会拒绝它。每个 `secret_file` 在 fit 启动时必须至少包含 16 字节（末尾换行除外），进程还
必须能写入它所在的目录：吊销通过一次原子改名替换该文件，写入失败则返回 500 且不轮换。

业务后端先在自己的身份提供方登录，然后用得到的 JWT 调用该路由。路由返回的 token 与 WHIP、WHEP、页面、流媒体和
`webrtc_proxy` 端点接受的媒体凭据相同，以 `Authorization: Bearer` 发送。

**为一个分片签发一批 token**，curl：

```sh
curl -X POST https://gateway.example.com/media-tokens \
	-H "Authorization: Bearer $BACKEND_JWT" \
	-H "Content-Type: application/json" \
	-d '{
		"key": "k2",
		"tokens": [
			{"subject": "alice@mtg-42", "publish": ["mtg-42-*"], "play": ["mtg-42-*"], "ttl": "2h"},
			{"subject": "bob@mtg-43", "publish": ["mtg-43-*"], "play": ["mtg-43-*"]}
		]
	}'
```

以及 Python：

```python
import json, urllib.request

backend_jwt = "eyJ..."  # 来自 jwt 中间件所信任的身份提供方
req = urllib.request.Request(
    "https://gateway.example.com/media-tokens",
    data=json.dumps({"key": "k2", "tokens": [
        {"subject": "alice@mtg-42", "publish": ["mtg-42-*"], "play": ["mtg-42-*"], "ttl": "2h"},
        {"subject": "bob@mtg-43", "publish": ["mtg-43-*"], "play": ["mtg-43-*"]},
    ]}).encode(),
    headers={"Authorization": "Bearer " + backend_jwt, "Content-Type": "application/json"},
    method="POST")
with urllib.request.urlopen(req) as resp:
    for t in json.load(resp)["tokens"]:
        print(t["subject"], t["key"], t["token"], t["expires"])
```

应答为 `{"tokens": [{"token", "subject", "expires", "key"}]}`，顺序与请求一致，单密钥策略下 `key` 不出现；
一批 token 中只要有一个不合法，就一个都不签发。

**轮换一个分片的密钥**，curl：

```sh
curl -X POST https://gateway.example.com/media-tokens/revoke \
	-H "Authorization: Bearer $BACKEND_JWT" \
	-H "Content-Type: application/json" \
	-d '{"key": "k2"}'
```

以及 Python：

```python
import json, urllib.request

backend_jwt = "eyJ..."  # 来自 jwt 中间件所信任的身份提供方
req = urllib.request.Request(
    "https://gateway.example.com/media-tokens/revoke",
    data=json.dumps({"key": "k2"}).encode(),
    headers={"Authorization": "Bearer " + backend_jwt, "Content-Type": "application/json"},
    method="POST")
with urllib.request.urlopen(req) as resp:
    print(json.load(resp))  # {"revoked": true, "since": 1793780000, "keys": ["k2"]}
```

**轮换所有密钥**（整个策略都需要吊销时），curl 用 `-d '{"all": true}'`，Python 用
`json.dumps({"all": True})` 作为请求的 data；应答会列出被轮换的每个密钥。

- **吊销一个密钥会同时轮换它的 secret 文件和运行中的密钥。** 处理器把 32 个随机字节原子地写入该密钥的
  `secret_file`，然后替换该密钥：它签发的每个 token 立刻被拒绝，凡共享该策略的处理器都如此——WHIP、WHEP、媒体
  页面、流媒体角色和 `media_tokens`——无论其过期时间如何；而其他密钥的 token 保持有效，所以一个会议或一个分片的
  吊销不会中断其余部分。用 `all` 时每个密钥都被轮换；只有一个 `secret_file` 的策略只有一个密钥：空请求体或
  `{"all": true}` 会拒绝它的每个 token，而请求体中的 `key` 返回 400。
- **在它之后签发的 token 在过期前一直有效。** 它们用新密钥签发，与其他 token 一样校验；策略的 `token_ttl` 和处理器的
  `max_ttl` 仍然限制它们。
- **文件保留新 secret，因此重启后吊销仍然有效**，重启会重新读取它们；应用配置也会重新读取，所以只轮换某个文件再重载
  也能吊销那个分片。
- **路由的中间件认证业务后端**——上面的 `jwt`，或 `basic_auth`、`forward_auth` 或 `rate_limit`；处理器不检查任何调用方
  凭据，也不涉及 Admin API。路由必须匹配处理器的 `prefix` 及其下路径：该前缀之外的请求返回 404。

其参数和应答见 [`media_tokens` 一章](reference/config.md#http-handlers-media_tokens)。

### 媒体

每个推流者的 offer 按每种媒体只应答一个编解码器：它自己列出的、`codecs` 中也有的第一个（未设置时为 opus、VP8、VP9、
H264 和 AV1）；观看者也以同一个应答，它的 offer 必须接受。观看者在其 offer 的各段上收到流的音视频，来源是对应推流者每段
的一个 TrackLocal：包在观看者自己的 SSRC 和密钥下重新保护，序号和时间戳保持推流者发出时的样子。

- **关键帧。** 新加入或丢包的观看者会请求关键帧（PLI 或 FIR）；无论观看者多少，fit 每个 `key_frame_interval`
  （500ms）至多向推流者发一次 PLI。观看者连上时、推流者的视频刚到达时，fit 也会主动请求，同样受这个间隔限制。
- **码率。** fit 每秒用 REMB 告诉每个推流者 `max_bitrate`（8 Mbit/s），浏览器会遵守；超过的包被丢弃，允许两秒的突发。
- **离开。** 推流者 DELETE、它的 PeerConnection 失败，或它沉默超过 `ice_timeout`（30s），流和观看者的会话都结束；
  观看者离开只结束它自己的会话。应答后 `connect_timeout`（30s）内未连上的会话也会结束。断开或缓慢的观看者不会拖住
  推流者和其他观看者：fit 发的是 UDP，从不等待。
- **不转发的。** 数据通道、第一层之后的 simulcast 层（转发 simulcast 推流者最先到达的那一层）、RTX 和 FEC，以及
  fib 的 PeerConnection 不支持的 transport-wide 拥塞控制。每个观看者都按到达顺序收到推流者的包：没有按观看者的带宽
  估计或分层选择。推流者的前 8 个音视频段被转发，它最多可发 16 路 RTP 流，simulcast 层也计入。

### ICE 服务器

fit 自己不需要 STUN 或 TURN 服务器：它的端口在自己的地址上可达，1:1 NAT 后则是 `public_ips`。防火墙封锁到它的 UDP
的对端需要在自己一侧使用 TURN 服务器；answer 的 `Link` 头告诉它 `ice_servers`，以及作为 `turn_urls` 的 fit 自己的
`webrtc.turn`，凭据由其 `secret` 生成（此时必须设置 `secret`），有效期 `turn_credential_ttl`（1h）：

```
Link: <stun:stun.example.com:3478>; rel="ice-server"
Link: <turn:turn.example.com:3478>; rel="ice-server"; username="1767225600:fit-5f1c..."; credential="..."; credential-type="password"
```

fit 的 TURN 服务器像对待任何对端一样中继到媒体端口：端口在它自己的 `relay_ip` 上时，`allowed_peers` 要带端口列出它，如 `10.0.0.5:8189`。

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
不计对端的 UDP 地址：它们由 `max_peer_connections` 限制。

### 重载

会话属于 `webrtc.media`，不属于路由：保持 `listen` 和 `public_ips` 不变的重载会保留所有会话，其他字段对下一个 offer
生效（`max_bitrate` 和 `key_frame_interval` 立即生效）。修改 `listen` 或 `public_ips` 会让媒体服务器重新启动：会话
结束，每个对端都收到 DTLS close，推流者和观看者重新 POST offer。去掉 `webrtc.media` 会结束所有会话，路由应答 503。

### 指标与日志

Admin API 的 `/metrics` 和 OpenTelemetry 导出中有 `fit_webrtc_media_peer_connections{role="publisher"|"viewer"}`、
`fit_webrtc_media_streams`、`fit_webrtc_media_sessions_total{role}`、
`fit_webrtc_media_sessions_ended_total{reason="delete"|"publisher_left"|"failed"|"connect_timeout"|"reload"}`、
`fit_webrtc_media_refused_total{reason="peer_connections"|"streams"|"viewers"|"conflict"|"no_stream"|"offer"}`、
`fit_webrtc_media_received_packets_total`、`fit_webrtc_media_received_bytes_total`、
`fit_webrtc_media_dropped_total{reason="bitrate"}`、
`fit_webrtc_media_key_frame_requests_total{result="sent"|"held"}` 和 `fit_webrtc_media_restarts_total`。
媒体服务器的消息写入 `webrtc` 日志，前缀 `webrtc.media: `：其地址以 `info` 级别记录，结束会话的重启以 `warn` 级别记录。

## 媒体页面

fit 自带一套媒体页面，嵌在二进制里：处理器 `media_pages` 在它的 `prefix` 下提供带登录的首页、带聊天功能的房间通话、
推流页和播放页，走上一节的 WHIP 和 WHEP 端点（或用 `whip` 和 `whep` 指向流媒体角色的端点，可以在本机，
也可以在别的服务器）。示例 [media-pages](https://github.com/lesismal/fit-examples/blob/main/media-pages/fit.conf) 把这些都配好了：

```fit
media_auth media {
	secret a-long-random-secret-for-the-logins
	public play
	users alice { password_hash $2a$10$Fq1ONPcWafh7L8Rqw/wBlOX87AVAV7hZrin/rWwd7vUZv6MlW.v.G; publish *; play * }
	users guest { password guest-password; publish lobby-*; play lobby-* }
}

route pages {
	match { path /media /media/* }
	handler media_pages {
		auth media
		login true
		title "fit media"
		pages true
		mic_default false
		cam_default false
		chat true
		max_file_bytes 5242880
	}
}

route publish { match { path /whip/* }; handler whip { auth media } }
route play { match { path /whep/* }; handler whep { auth media } }

webrtc { media { listen :8189 } }
```

浏览器只在安全的页面上打开摄像头和麦克风：HTTPS，或者 `localhost`。

- **页面。** `/media/` 是首页：端点、是否需要凭据、已登录用户能做什么。`/media/call` 是房间通话：每个成员用
  WHIP 推送 `<room>-<name>`，用 WHEP 播放其他每个成员。`/media/publish` 把摄像头、麦克风或屏幕推成一路流，
  并给出它的地址：WHIP、WHEP、`hls`，以及给浏览器之外的工具用的 `links`（填好 `{stream}`、`{token}` 和
  `{host}`）；`/media/play` 用 WHEP 播放一路流，浏览器自己能播 HLS 时也可以用 HLS。页面不从别处加载任何脚本、
  样式或字体，应答带内容安全策略（CSP），只允许访问本服务器和端点所在的源。`pages` 为 false 时一个页面也不提供：
  前缀下所有不是 API 的路径，包括从 `<prefix>` 到 `<prefix>/` 的重定向，都应答 404，供页面由自己实现的部署使用；
  登录和房间 API 保留，供那个页面使用。
- **登录。** 设了 `auth` 和 `login` 时，页面让策略的用户登录：`POST /media/api/login` 核对密码
  （`password_hash` 在 mediaauth 的 bcrypt 池上核对），应答一个策略签名、有效期为 `token_ttl` 的令牌，设为
  cookie `fit_media_token`（`HttpOnly`、`SameSite=Strict`、TLS 上加 `Secure`、`Path=/`），`whip`、`whep`
  和房间 API 都认；令牌也交给页面，页面对别的服务器上的端点用 `Authorization: Bearer` 发送。粘贴进来的令牌
  （如 `fit media-token` 打印的）用法相同。没有 `secret` 时用进程自己的密钥签名，重启后失效。页面的 HTML
  也可以再放在路由自己的中间件后面，如 `basic_auth`、`forward_auth` 或 IP 白名单；路由上的 `rate_limit`
  可以限制登录尝试。
- **开关。** 麦克风、摄像头和屏幕随时可以打开和关闭，有没有设备都可以。推流方总是提供一个音频和一个视频
  收发器；开关只替换发送器发送的轨道，所以从不重新协商，也不重建 PeerConnection。打开时发送设备的轨道，
  没有设备时发送合成轨道：静音，或每秒 2 帧、写着成员名字的画面。关闭时什么都不发送，并释放设备，指示灯熄灭。成员加入时
  麦克风和摄像头是否打开由 `mic_default` 和 `cam_default` 决定：不写时都是关闭的，由成员自己打开；URL 中的 `mic=0|1`
  和 `cam=0|1` 对这次访问覆盖配置。
- **放大。** 通话中每个成员的画面（包括自己的）都有一个放大按钮，双击画面也可以：放大的画面占据通话区域上方中央，
  在给下方留出一排其他成员画面的前提下尽可能大，其他成员仍按原来的网格排列在下方。再按一次按钮或再双击一次即还原；
  放大另一个画面时，原来放大的那个自动还原。视频始终继续播放；被放大的成员离开时，布局恢复为网格。
- **房间 API。** 每个成员通过处理器的房间 API 告知自己是否在推流、各开关的状态以及有没有设备：
  `POST /media/api/rooms/<room>/join`、`POST .../members/<name>`（一次变化，以及每 `member_ttl`/3 一次心跳）、
  `POST .../leave`，以及 `GET .../events`：Server-Sent Events，每次变化推送房间的成员。其他人在成员的摄像头
  关闭时显示其头像，显示“麦克风已关”“没有麦克风”或“没有摄像头”，并且只在成员宣告推流之后才播放它的流，
  所以没有人去请求一路不存在的流；即使请求仍然失败，观看方最多再试 6 次，依次等待 1、2、4、8 和 15 秒。
  加入需要 `<room>-*` 上的 `play`，宣告推流需要 `<room>-<name>` 上的 `publish`；成员的变更需要它加入时得到的 key。
- **房间聊天。** 房间通话有聊天，除非 `chat` 为 false：文字消息和小文件，在房间存在期间保存在内存里，页面显示它们，
  房间 API 在成员之间传递它们；为 false 时不显示聊天窗口，聊天的 API 应答 404。消息经房间 API 的
  `POST .../chat`、`POST .../files?file=<name>`、`GET .../files/<id>` 以及 `events` 流收发，格式为
  `{"id", "from", "at", "text"}`，文件消息为 `{"id", "from", "at", "file": {"name", "size", "type"}}`；`id` 是
  128 位随机数的十六进制形式，也是文件在 `files/<id>` 中的 id，`at` 是 Unix 毫秒。请求用 `X-Fit-Member: <name>`
  和 `X-Fit-Room-Key: <key>`（加入时得到的 key）标明成员，所以 key 从不放在会被访问日志写入的 URL 里；key 不对时
  应答 404“no such member”，加入或关注和 API 的其他部分一样需要 `<room>-*` 上的 `play`（否则 `401` 或 `403`）。
  聊天开启时，`events` 先发房间的 `history` 事件 `{"messages": [...]}`（最旧的在前），之后和以前一样推送成员，
  并且每条新消息发一个 `event: chat`，绝不早于流的响应头；页面用 POST 的应答立即显示自己发出的消息，收到相同 id
  的回声时去重。文字必须是 UTF-8 且非空白（否则 400）；请求体最多 `max_message_bytes`，超过即 413，因此文字也受它
  限制；文件不超过 `max_file_bytes`（不写时为 5 MiB），更大的请求体同样应答 413，按 `Content-Length` 或随请求体到达
  判定，页面则在发送前就拒绝。下载同样需要 `<room>-*` 上的 `play`，以附件形式应答保存的字节：`Content-Disposition`、
  保存的 `Content-Type`、`X-Content-Type-Options: nosniff` 和 `Cache-Control: no-store`；未知或已被淘汰的 id 为
  404。`api/config` 告诉页面 `call.mic`、`call.cam`、`chat.enabled` 和 `chat.maxFileBytes`。
- **上限。** `max_rooms` 个房间（64），每个房间 `max_members` 个成员（16）和两倍数量的事件流，请求体最多
  `max_message_bytes` 字节（1024），没有心跳最多 `member_ttl`（30s），浏览器取走事件最多等 `stall_timeout`
  （30s）：跟不上的浏览器在取走上一份之后只收到房间的最新状态，超时则被断开。聊天同样有上限：每个房间最多 64 条消息，
  文字和文件合计，最旧的被丢弃；所有房间的文件字节合计最多 64 MiB，超过时最旧的文件消息被丢弃，其下载随后为 404；
  无法一次写完消息的关注者最多排队 256 个事件或 1 MiB，超过则结束它的流，EventSource 会重连并由历史补齐。消息和文件
  数据一旦保存就不再改变，并随房间一起消失。房间属于引擎上处理器的前缀，重载后仍在；被换下的配置上的事件流在 drain
  超时后结束，浏览器会重新关注房间。
- **静默。** 媒体服务器会保留所有开关都关闭的推流方及其观看方：维持会话的是 ICE consent，而不是媒体，所以
  推流方可以任意长时间什么都不发。只推音频或只推视频的推流方，可以被同时请求两种媒体的观看方播放，另一种的
  媒体段被应答为 inactive；只请求推流方没有发送的那种媒体的观看方得到 `422`。

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
		relay_ip 10.0.0.1
		port_min 40000
		port_max 40999
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
  "webrtc": {"proxy": {"listen": ":8190", "public_ips": ["203.0.113.5"],
                       "relay_ip": "10.0.0.1", "port_min": 40000, "port_max": 40999}}
}
```

### 一个会话的经过

1. 推流端把 offer POST 给 fit。fit 收下整个 offer（不超过 `max_sdp_bytes`），去掉其中的候选地址，发给上游选中的
   媒体服务器：上游的负载均衡、健康检查照常生效，请求自带的字段（包括 `Authorization`）照传，`X-Forwarded-For`
   设为客户端的地址。
2. 媒体服务器应答 `201` 并带 `Location`。fit 读出其中的 `a=ice-ufrag`、`a=ice-pwd`，以及要把对端中继到的地址：
   `media_addrs` 中该媒体服务器的条目（IP 加端口，或只有 IP，此时取 answer 中候选的端口）；否则是 answer 中第一个 host
   候选，再否则是 server-reflexive 候选，IPv4 优先，跳过链路本地地址。fit 把
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
- **会话的 URL。** DELETE、PATCH、OPTIONS 以及发往会话 URL 之下至多三段的请求，都转给应答的媒体服务器，靠 fit
  的 ID 而非 cookie 保持粘滞。上游已不再列出其媒体服务器的会话应答 `502`；它的 DELETE 仍会释放会话。端点自己的
  OPTIONS 转给上游选中的媒体服务器。
- **PATCH。** 只 trickle 候选的 PATCH 由 fit 直接应答 `204`：无论对端的候选是什么，媒体服务器都经由 fit 到达对端。
  ICE 重启（带 `a=ice-ufrag` 的 PATCH）去掉候选后转给媒体服务器，其应答中的新 ufrag 与第一个 ufrag 一样被路由，
  候选同样被替换。
- **状态码。** offer 不是 `application/sdp` 时 `415`；请求超过 `max_sdp_bytes` 时 `413`；超过 `max_sessions` 时
  `503` 并带 `Retry-After: 1`，`webrtc.proxy` 未运行时 `503`；媒体服务器无法连接、answer 超过 `max_sdp_bytes`、
  `201` 没有 `Location`、answer 没有 ufrag 或没有 fit 能中继到的候选时 `502`（这种会话 fit 会到媒体服务器上 DELETE
  掉）；在 `timeout` 内没有应答时 `504`；fit 已没有的会话的 URL 为 `404`；端点上 POST 和 OPTIONS 以外的方法为 `405`。
  其他应答，包括媒体服务器对 offer 的拒绝，去掉逐跳字段后原样传递。

设置了 `auth`（一条 [`media_auth`](#认证) 策略）和 `action`（WHIP 路由为 `publish`，WHEP 路由为 `play`）时，
fit 在任何媒体服务器看到 offer 之前，按其路径的最后一段检查它，并自己应答 `401` 或 `403`；会话的请求不需要凭据，
请求的 `Authorization` 原样发往媒体服务器，供也做检查的媒体服务器使用。

整个过程不等待：offer 在最后一次 body 回调里经 fib 的异步客户端发往上游，answer 在客户端的回调里写回；客户端离开
会取消请求。

### 上限

| 字段 | 默认值 | 限制 |
| --- | --- | --- |
| `max_sessions` | 4096 | 同时存在的会话，从 offer 起计；再多一个应答 `503` |
| `max_relays` | 4096 | 同时中继的对端地址，各占一个 socket；超过时新地址被丢弃 |
| `session_timeout` | 30s | 没有任何中继的会话，从应答或最后一个中继结束起，超时后被遗忘，其 URL 应答 `404` |
| `idle_timeout` | 30s | 不发送任何数据的对端地址失去其中继，不论媒体服务器是否还在发送 |
| `webrtc_proxy` 的 `max_sdp_bytes` | 32 KiB | 请求和 answer 的大小 |
| `webrtc_proxy` 的 `timeout` | 10s | 从请求的最后一个字节到完整读完 answer |

通往媒体服务器的 socket 从本机地址之一 `relay_ip`、从 `port_min` 到 `port_max` 中的一个端口发出，以适应媒体服务器前面
只放行一个源地址和一段端口的防火墙，或媒体服务器在另一块网卡所连网络上的主机；不设时由路由和内核选择。被占用的端口会
跳过，范围内所有端口都被占用时丢弃该对端地址（`no_port`），它的下一个请求再试：除 `max_relays` 外，范围也限制同时存在的
中继数。`relay_ip` 与媒体服务器同一地址族：另一地址族的媒体地址不被中继。两者在重载后对下一个中继生效。

一个会话最多中继对端的 8 个地址：对端每个发起检查的候选一个，迁移后还会更多。一个会话最多保留 4 个 ufrag，含 ICE
重启产生的。中继在其 socket 打开期间最多暂存 8 个数据报，打开最多等 5 秒，否则关闭中继。fit 的 `limits.max_conns`
既不计对端的 UDP 地址，也不计通往媒体服务器的 socket：两者都由 `max_relays` 限制。

### ICE 重启、consent 与迁移的对端

ICE 的 consent 检查大约每 5 秒一次，像其他数据报一样被中继，使中继保持打开。对端迁移到别的网络或别的 NAT 映射后，
会从新地址再次检查，从而打开一个通往同一媒体服务器的新中继；旧的在 `idle_timeout` 后结束。经 PATCH 的 ICE 重启
保留原会话；像 fit 自己的 whip 那样以新会话方式重启，则是一次新的 POST。

fit 做不到的：

- 只在 answer 之后用 trickle 给出候选的媒体服务器应答 `502`，除非 `media_addrs` 给出它带端口的地址。
- answer 中的候选分布在多个地址时，只中继到其中一个；TCP 上的 ICE、以及只能经 TURN 到达的媒体服务器不被中继。
- 会话 URL 之下的 URL 按整个 body 和整个应答代理，不超过 `max_sdp_bytes`：流式扩展，如 WHEP 的 server-sent
  events，经过 fit 无法工作。
- 媒体服务器看到的是 fit 的地址，而不是对端的。

### 重载

会话属于 `webrtc.proxy` 而不是路由：保留 `listen` 的重载保留会话及其中继，其他字段从下一次起生效，`public_ips`
从下一个 answer 起生效。`listen` 改变会结束所有中继和会话，对端需重新 POST offer。被重载移出上游的媒体服务器，
其中继保留，而其会话的请求应答 `502`。

### 指标与日志

Admin API 的 `/metrics` 和 OpenTelemetry 导出中有 `fit_webrtc_proxy_sessions`、`fit_webrtc_proxy_relays`、`fit_webrtc_proxy_sessions_total`、
`fit_webrtc_proxy_refused_total`、`fit_webrtc_proxy_sessions_ended_total{reason="delete"|"expired"|"reload"}`、
`fit_webrtc_proxy_relays_total`、
`fit_webrtc_proxy_dropped_total{reason="not_stun"|"unknown_ufrag"|"integrity"|"limit"|"no_port"}`、
`fit_webrtc_proxy_relayed_bytes_total{direction="to_backend"|"to_client"}` 和
`fit_webrtc_proxy_restarts_total`。信令请求计入上游自己的指标。代理的消息写入 `webrtc` 日志，前缀
`webrtc.proxy: `：其端口以 `info` 级别记录，结束会话的 `listen` 修改以 `warn` 级别记录。
