# WebRTC

[English](webrtc.md) | [简体中文](webrtc.zh-CN.md)

fit 用 fib 的 WebRTC 各包提供 WebRTC 服务，与网关一起运行在其引擎上（[决定 029](decisions/029-webrtc.zh-CN.md)）。
目前运行的是 TURN 服务器；以 fit 为媒体服务器、以及作为其他媒体服务器前端的 WHIP 和 WHEP 已列入计划
（[开发计划](dev-plan.zh-CN.md#webrtc) 中的 I109 和 I110）。

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
