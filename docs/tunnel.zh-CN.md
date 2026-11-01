# 隧道

[English](tunnel.md) | [简体中文](tunnel.zh-CN.md)

配置示例，每个都有 .conf、YAML 和 JSON 三种格式：[tunnel-server](../examples/tunnel-server/fit.conf), [tunnel-node](../examples/tunnel-node/fit.conf), [tunnel-client](../examples/tunnel-client/fit.conf)。

fit 的隧道让人经由一台谁都能连上的服务端，访问只有旁边那台主机才连得到的机器上的 TCP 服务。它原本是
一个独立程序 fibhub，现在是 fit 配置里的三个角色，运行在监听器的 fib 引擎上，可以和网关的路由并存，
也可以单独运行（[决策 028](decisions/028-agent-and-tunnel.zh-CN.md)）。

```text
 本地应用 ──TCP──▶ tunnel.client ══链路══▶ tunnel.server ══链路══▶ tunnel.node ──TCP──▶ 服务
 (ssh、浏览器)     每个转发一个本地端口    路由隧道，转发隧道帧     位于服务旁边        10.0.0.5:22
```

| 角色 | 成员 | 运行在 | 作用 |
|------|------|--------|------|
| 服务端 | `tunnel.server` | 节点和客户端都能连上的主机 | 注册节点和客户端，把客户端打开的每条隧道路由到它指名的节点，并在两端之间转发隧道的帧 |
| 节点 | `tunnel.node` | 服务所在网络里的主机 | 连接一台或多台服务端，带着它能访问的服务注册，并为路由给它的每条隧道连接对应的服务，不连接其他地址 |
| 客户端 | `tunnel.client` | 用户的机器 | 连接服务端，得知它们的节点和服务，为每个转发监听一个本地地址，那里的每个连接都是一条隧道 |

fibhub 把节点叫作 *pod*，把服务叫作 *node*。节点和客户端都是**主动向外**连接服务端，所以都不需要开放
端口。一个进程可以运行其中任意几个角色，三个都运行也可以，有网关或没有都行。

隧道的每个字节都经服务端中继，只要节点和客户端能连上一台服务端，现在就能用。计划中的
[P2P 里程碑](decisions/025-p2p.zh-CN.md)走另一条路：ICE 在两个 NAT 上打洞，让 NAT 后的两台主机直接相连，
承载 TCP 和 UDP 服务，只在打不通时才回退到 TURN 中继。

## 运行角色

成员设置了，角色就运行。只有角色、没有路由的配置不需要监听器：

```json
{
  "tunnel": {
    "server": {
      "name": "hub",
      "password": "a-registration-password",
      "body_password": "a-body-password",
      "listeners": [
        {"protocol": "tcp", "address": ":7000"},
        {"protocol": "ws", "address": ":7001"},
        {"protocol": "quic", "address": ":7443"}
      ],
      "console": {
        "address": "127.0.0.1:7080",
        "users": [{"username": "admin", "password_hash": "$2a$10$..."}]
      }
    }
  }
}
```

```json
{
  "tunnel": {
    "node": {
      "name": "lab",
      "servers": [{
        "name": "hub",
        "endpoints": [
          {"address": "quic://hub.example.com:7443"},
          {"address": "ws://hub.example.com:7001/fibhub"},
          {"address": "hub.example.com:7000"}
        ],
        "password": "a-registration-password",
        "body_password": "a-body-password",
        "quic_pin": "服务端控制台上显示的指纹"
      }],
      "services": [
        {"name": "ssh", "address": "10.0.0.5:22", "description": "实验室的网关"}
      ]
    }
  }
}
```

```json
{
  "tunnel": {
    "client": {
      "name": "alice",
      "servers": [{
        "name": "hub",
        "endpoints": [{"address": "hub.example.com:7000"}],
        "password": "a-registration-password",
        "body_password": "a-body-password"
      }],
      "forwards": [
        {"listen": "127.0.0.1:2222", "node": "lab", "service": "ssh"}
      ]
    }
  }
}
```

之后 `ssh -p 2222 127.0.0.1` 就连到 10.0.0.5:22。`fit validate` 检查这些成员；
[配置参考](reference/config.zh-CN.md)列出每个字段、默认值和取值范围。

### tunnel.server

| 字段 | 默认 | 含义 |
|------|------|------|
| `name` | 主机名 | 告知节点和客户端的名字 |
| `password` | 无：不接受任何注册 | 节点和客户端注册用的密码，以 HMAC 证明知道它，从不发送 |
| `body_password` | 无 | 对每个帧的消息体做 AES-256-GCM 加密；节点和客户端需要相同的密码 |
| `listeners` | 无：由页面添加 | `{protocol: tcp\|ws\|quic, address, path}`；ws 监听的路径默认为 `/fibhub`，只有 ws 监听有路径 |
| `console_socket` | 无 | `{address, password, max_conns}`：经加密 WebSocket 的控制台，供客户端的 `web_proxies` 使用，任何路径都应答；需要 `console`，不设密码时用服务端的密码，`max_conns` 默认 32 |
| `quic` | | `{cert, key, idle_timeout, keep_alive, handshake_timeout, max_handshakes}`，不设时为 30s、10s、10s 和 64；有 quic 监听而没有证书时，服务端生成自签名证书并写入这里；`cert` 和 `key` 须同时设置 |
| `register_timeout` | 10s | 一条链路注册的时限 |
| `call_timeout` | 15s | 节点应答的时限：打开隧道、列出或设置服务 |
| `max_links`、`max_links_per_peer`、`max_peers`、`max_tunnels` | 1024、128、1024、16384 | 服务端保有资源的上限 |
| `console` | 无 | 服务端的页面（见[控制台](#控制台)） |

### tunnel.node

| 字段 | 默认 | 含义 |
|------|------|------|
| `name` | 主机名 | 客户端用来找到这个节点的名字 |
| `meta` | 无 | 告知服务端的其他信息 |
| `servers` | 无 | 要连接的服务端（见下） |
| `services` | 无 | `{name, address, description}`：节点能访问的服务，也是它唯一会连接的地址 |
| `dial_timeout` | 5s | 连接一个服务的时限 |
| `idle_timeout` | 2h | 两个方向都没有数据超过这么久的隧道会被关闭 |
| `max_tunnels` | 4096 | 同时打开的隧道数，跨所有服务端 |
| `console` | 无 | 节点的页面 |

### tunnel.client

| 字段 | 默认 | 含义 |
|------|------|------|
| `name`、`meta`、`servers` | | 同节点 |
| `forwards` | 无 | `{listen, node, service, server}`：不指定 `server` 时，隧道分散到所有有这个节点的服务端，到节点路径更快的服务端分得更多 |
| `web_proxies` | 无 | `{name, listen, url, password, max_conns}`：经服务端的 `console_socket` 在本地提供它的控制台，`url` 是它的 `ws://host:port/`；`max_conns` 默认 32 |
| `open_timeout` | 20s | 打开一条隧道（经服务端到服务）的时限 |
| `idle_timeout` | 2h | 同节点 |
| `max_tunnels` | 4096 | 同时打开的隧道数；超出时新接受的连接立即关闭 |
| `console` | 无 | 客户端的页面 |

### 节点或客户端连接的服务端

| 字段 | 默认 | 含义 |
|------|------|------|
| `name` | 第一个端点的地址 | 转发和页面用来指称它的名字 |
| `endpoints` | 必填 | `{address, disabled}`，按顺序尝试：`host:port` 或 `tcp://` 为 TCP，`ws://host:port/path`，`quic://host:port` |
| `password` | 必填 | 服务端的密码 |
| `body_password` | 无 | 服务端的消息体密码（如果有） |
| `quic_pin`、`quic_insecure` | | quic 端点如何检查服务端证书：按服务端页面上显示的 SHA-256 指纹，或不检查（仅用于测试）；两者都不设时用系统根证书 |
| `pool_size` | 10，最多 100 | 承载隧道的链路数 |
| `dial_timeout` | 5s | 链路拨号的时限 |
| `call_timeout` | 10s | 服务端应答注册或调用的时限 |
| `retry_interval` | 3s | 拨号失败或失去服务端后，链路再次拨号前等待的时间 |
| `ping_interval`、`ping_timeout` | 5s、5s | 多久测一次到服务端的往返时间，以及 ping 多久没有应答算作丢失 |

## 链路

节点或客户端与每台服务端保持 `pool_size` 条链路。每条链路拨号第一个未禁用的端点，失败时回退到下一个，
随后注册；拨号失败或失去服务端后隔 `retry_interval` 再拨号。同名同类型的链路，无论来自多少个进程，在服务端看来
都是同一个对端。每条隧道始终走一条链路，使它的帧保持顺序；新隧道走承载隧道最少的链路，每条链路最多
承载 64 条，需要更多隧道的对端用更大的连接池。

- **TCP** 和 **WebSocket** 链路是引擎上的连接。WebSocket 链路用于只放行 HTTP 的网络，例如经 nginx 的
  `proxy_pass` 加 `Upgrade`；没有 `wss://`，所以请设置 `body_password`。
- **QUIC** 链路运行在同一个引擎的 UDP 绑定上，使用 fib 的 QUIC、TLS 1.3 和 ALPN `fibhub`。有 quic 监听
  需要证书时，服务端生成自签名证书并保存在它的成员里，所以节点和客户端固定的指纹在重启后不变。
- **注册**是以密码为密钥、对对端类型、名字、时间和随机数做的 HMAC-SHA256；时间与服务端时钟相差不得超过
  5 分钟，同一个随机数只接受一次。
- **消息体加密**：各方都设置 `body_password` 时，每个帧的消息体用 AES-256-GCM 加密，帧头也参与认证；
  未加密或解不开的帧会关闭链路。

线上协议是 fibhub 的，名字也一样：节点以 `pod` 注册，它的服务是它的 `nodes`，这样迁移期间 fit 的角色
和 fibhub 的程序可以互相连接。

### 流控、上限与超时

隧道每一端最多发送 4 MiB 对端尚未确认的数据，达到后暂停读取自己的 TCP 连接；对端在自己的连接接收了数据
之后才确认。慢的服务会拖住向它写数据的客户端，慢的客户端也会拖住服务，按隧道各自进行，中间最多只有一个
窗口和套接字缓冲区的数据。等待一条链路发送的数据，以它所承载隧道的窗口为界。控制台 WebSocket 和 Web 代理
在一侧的积压超过 256 KiB 时暂停读取另一侧。

每次拨号、注册和调用都有时限；链路的 TCP 连接空闲 30s 后开始探测，对端不再应答时关闭；QUIC 连接每隔
`keep_alive` 发送 ping，`idle_timeout` 后关闭；空闲超过 `idle_timeout` 的隧道会被关闭。服务端限制链路、
对端和隧道的数量、每个对端的链路数、进行中的 QUIC 握手数，以及未注册链路的帧大小（64 KiB）。

## 控制台

每个角色的 `console` 提供它的页面，登录方式与 `admin.console` 相同（见[控制台](console.zh-CN.md)）：
`{address, cert_file, key_file, users, session_timeout, session_lifetime, max_sessions, max_conns}`，
用户的 bcrypt 哈希由 `fit hash-password` 生成，会话 cookie 加 CSRF token；除非设置 `cert_file` 和
`key_file`，只能监听回环地址。

- **服务端**：节点（含服务和延迟）与客户端；隧道（含两个方向的字节数和整条路径的延迟）。查询节点、设置
  它的服务、踢出对端、关闭隧道。增删改监听；设置密码和控制台 WebSocket；复制 QUIC 证书指纹。在这里设置
  的服务会写入节点自己的成员。
- **节点**：它的服务端，以及每条链路使用的协议，可增删改；它的服务，保存后通知所有服务端；它的隧道，
  可关闭。
- **客户端**：它的服务端（编辑方式同节点）、它们的节点和服务，以及每台服务端分担某个节点隧道的比例；
  查询节点，经服务端设置节点的服务；增删转发；它的隧道，可关闭；告知服务端的 meta；以及经 Web 代理访问
  每台服务端的控制台。

页面所做的修改通过 `app.Host.Edit` 写入角色在配置中的成员，再像其他配置一样从那里生效：它进入配置历史；
配置了 `admin.store` 时会加密保存到存储中，下次启动时读回（存储见[控制台](console.zh-CN.md)）。没有存储时，修改只在 fit 重启或重新读取配置
文件之前有效，页面会提示这一点。每个页面都有隧道全景图（**详情**）以及主题和语言选择。

服务端的控制台在无法直接访问的地方，可以经加密 WebSocket 访问：设置服务端的 `console_socket`，客户端的
`web_proxies` 条目填上它的 URL 和密码，就在本地地址上提供这个控制台。WebSocket 的消息用由密码派生的密钥
做 AES-256-GCM 加密；控制台自己的登录照样需要。

## 从 fibhub 迁移

| fibhub | fit |
|--------|-----|
| `fibhub -mode=server\|pod\|client` | 成员 `tunnel.server`、`tunnel.node` 或 `tunnel.client`，可任意组合 |
| pod，它的 nodes | 节点，它的服务 |
| 只含 web 页面设置的配置文件，其余在 `FIBSTORE` 存储中 | 所有设置都在成员里；配置 `admin.store` 时在 fit 的存储中 |
| `webAddr`、`webAuth`、`/login` | `console`，fit 的登录；转发和 Web 代理的 `webAuth` 不再存在 |
| `listeners[].proto`、`addr`、`path`；`addr`、`linkWsAddr`、`listen` | `listeners[].protocol`、`address`、`path` |
| `wsAddr`、`wsPassword` | `console_socket.address`、`password` |
| `quicCert`、`quicKey` | `quic.cert`、`quic.key` |
| 服务端的 `endpoints[].proto`、`addr`、`enabled`；`addr` | 带协议前缀的 `endpoints[].address`，`disabled` |
| `quicPin`、`quicInsecure`、`bodyPassword`、`poolSize` | `quic_pin`、`quic_insecure`、`body_password`、`pool_size` |
| `forwards[].pod`、`node` | `forwards[].node`、`service` |
| `webProxies` | `web_proxies` |
| `-print-config`、命令行参数、`FIBHUB_*` | 无：配置和 fit 的其他配置一样书写 |

迁移时，按上表由 fibhub 的设置写出每个角色的成员并加载，或用 `fit store import` 放进存储。把服务端的
`quicCert` 和 `quicKey` 复制到 `quic` 中，指纹不变，固定了它的节点和客户端可以继续连接。由于线上协议
是 fibhub 的，迁移期间 fit 的服务端可以服务 fibhub 的 pod 和客户端，fit 的节点或客户端也可以连接 fibhub
的服务端；fit 的测试只覆盖 fit 角色之间的互连。

行为上的不同：

- 一切都运行在 fit 的引擎上：QUIC 不再有自己的引擎、套接字或 goroutine，网关的 `limits` 也作用于角色的
  连接。
- 角色切换到新成员时保留没有变化的部分：服务端的链路，设置未变的服务端对应的节点或客户端链路，同一地址上
  的转发。
- 页面写入配置；无法监听的监听器或转发仍保留在配置中，显示错误，并在下一份配置时重试。
- 新增的上限和超时：服务端的 `max_links`、`max_links_per_peer`、`max_peers` 和 `max_tunnels`，每条链路
  64 条隧道，各角色的 `max_tunnels` 和 `idle_timeout`，链路和隧道上的 TCP keep-alive，握手完成后在握手
  时限内没有承载链路的 QUIC 连接会被关闭。
- 页面在 fit 的登录和 Content-Security-Policy 之后，不再显示或保存其他页面的登录信息。

## 测试

`go test -race ./tunnel/...` 像 `fit run` 那样通过 fit 的配置、在真实引擎上运行这些角色：经 TCP、
WebSocket 和 QUIC 的隧道（加密与不加密消息体）、大数据量传输、慢服务和慢客户端、中途断开的连接、页面修改
写回配置，以及从存储读回设置的重启。`test/docker/tunnel/fit-tunnel-docker.sh test` 让服务端、节点和
客户端各自运行在独立容器中，设备位于只有其节点可达的网络上，通过控制台和隧道测试全部功能，然后重启
每个实例，检查它们从各自的存储中恢复。
