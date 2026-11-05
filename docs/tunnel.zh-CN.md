# 隧道

[English](tunnel.md) | [简体中文](tunnel.zh-CN.md)

配置示例（.conf 格式，在 [fit-examples](https://github.com/lesismal/fit-examples) 中）：[tunnel-hub](https://github.com/lesismal/fit-examples/blob/main/tunnel-hub/fit.conf), [tunnel-node](https://github.com/lesismal/fit-examples/blob/main/tunnel-node/fit.conf), [tunnel-proxy](https://github.com/lesismal/fit-examples/blob/main/tunnel-proxy/fit.conf)。

fit 的隧道让人经由一台谁都能连上的 hub，访问只有旁边那台主机才连得到的机器上的 TCP 服务。它是
fit 配置里的三个角色，运行在监听器的 fib 引擎上，可以和网关的路由并存，也可以单独运行（[决策 028](decisions/028-llm-and-tunnel.zh-CN.md)）。

```text
 本地应用 ──TCP──▶ tunnel.proxy ══链路══▶ tunnel.hub ══链路══▶ tunnel.node ──TCP──▶ 服务
 (ssh、浏览器)     每个转发一个本地端口    路由隧道，转发隧道帧     位于服务旁边        10.0.0.5:22
```

| 角色 | 成员 | 运行在 | 作用 |
|------|------|--------|------|
| hub | `tunnel.hub` | 节点和 proxy 都能连上的主机 | 注册节点和 proxy，把 proxy 打开的每条隧道路由到它指名的节点，并在两端之间转发隧道的帧 |
| 节点 | `tunnel.node` | 服务所在网络里的主机 | 连接一台或多台 hub，带着它能访问的服务注册，并为路由给它的每条隧道连接对应的服务，不连接其他地址 |
| proxy | `tunnel.proxy` | 用户的机器 | 连接 hub，得知它们的节点和服务，为每个转发监听一个本地地址，那里的每个连接都是一条隧道 |

节点和 proxy 都是**主动向外**连接 hub，所以都不需要开放端口。一个进程可以运行其中任意几个角色，三个都运行也可以，有网关或没有都行。

隧道的每个字节都经 hub 中继，只要节点和 proxy 能连上一台 hub，现在就能用。计划中的
[P2P 里程碑](decisions/025-p2p.zh-CN.md)走另一条路：ICE 在两个 NAT 上打洞，让 NAT 后的两台主机直接相连，
承载 TCP 和 UDP 服务，只在打不通时才回退到 TURN 中继。

## 运行角色

成员设置了，角色就运行。只有角色、没有路由的配置不需要监听器：

```json
{
  "tunnel": {
    "hub": {
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
      "hubs": [{
        "name": "hub",
        "endpoints": [
          {"address": "quic://hub.example.com:7443"},
          {"address": "ws://hub.example.com:7001/tunnel"},
          {"address": "hub.example.com:7000"}
        ],
        "password": "a-registration-password",
        "body_password": "a-body-password",
        "quic_pin": "hub 控制台上显示的指纹"
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
    "proxy": {
      "name": "alice",
      "hubs": [{
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

### tunnel.hub

| 字段 | 默认 | 含义 |
|------|------|------|
| `name` | 主机名 | 告知节点和 proxy 的名字 |
| `password` | 无：不接受任何注册 | 节点和 proxy 注册用的密码，以 HMAC 证明知道它，从不发送 |
| `body_password` | 无 | 对每个帧的消息体做 AES-256-GCM 加密；节点和 proxy 需要相同的密码 |
| `listeners` | 无：由页面添加 | `{protocol: tcp\|ws\|quic, address, path}`；ws 监听的路径默认为 `/tunnel`，只有 ws 监听有路径 |
| `console_socket` | 无 | `{address, password, max_conns}`：经加密 WebSocket 的控制台，供 proxy 的 `web_proxies` 使用，任何路径都应答；需要 `console`，不设密码时用 hub 的密码，`max_conns` 默认 32。10 秒内没有证明知道密码的 WebSocket 会被关闭，同一 IP 地址至多有 `max_conns` 的四分之一处于这种状态 |
| `quic` | | `{cert, key, idle_timeout, keep_alive, handshake_timeout, max_handshakes}`，不设时为 30s、10s、10s 和 64；进行中的握手超过 `max_handshakes` 的一半后，客户端须先以 QUIC Retry 证明自己的地址才能开始握手；有 quic 监听而没有证书时，hub 生成自签名证书并写入这里；`cert` 和 `key` 须同时设置 |
| `register_timeout` | 10s | 一条链路注册的时限 |
| `call_timeout` | 15s | 节点应答的时限：打开隧道、列出或设置服务 |
| `max_links`、`max_links_per_peer`、`max_peers`、`max_tunnels` | 1024、128、1024、16384 | hub 保有资源的上限 |
| `max_unregistered`、`max_unregistered_per_ip` | 256、32 | 尚未注册的已打开链路，总数与来自同一 IP 地址的数量；超出任一项的链路在打开时即被关闭，其节点或 proxy 在 `retry_interval` 之后重连 |
| `allow_proxy_services` | false | 允许已注册的 proxy 经 hub 设置节点的服务（proxy 的页面这样做）；开启后任何持有密码的 proxy 都可以。节点仍须开启自己的 `allow_hub_services` 才接受 |
| `console` | 无 | hub 的页面（见[控制台](#控制台)） |

### tunnel.node

| 字段 | 默认 | 含义 |
|------|------|------|
| `name` | 主机名 | proxy 用来找到这个节点的名字 |
| `meta` | 无 | 告知 hub 的其他信息 |
| `hubs` | 无 | 要连接的 hub（见下） |
| `services` | 无 | `{name, address, description}`：节点能访问的服务，也是它唯一会连接的地址 |
| `allow_hub_services` | false | 允许它的 hub 设置 `services` 并写入本成员；关闭时只有节点自己的成员和页面能设置 |
| `dial_timeout` | 5s | 连接一个服务的时限 |
| `idle_timeout` | 2h | 两个方向都没有数据超过这么久的隧道会被关闭 |
| `max_tunnels` | 4096 | 同时打开的隧道数，跨所有 hub |
| `console` | 无 | 节点的页面 |

### tunnel.proxy

| 字段 | 默认 | 含义 |
|------|------|------|
| `name`、`meta`、`hubs` | | 同节点 |
| `forwards` | 无 | `{listen, node, service, hub}`：不指定 `hub` 时，隧道分散到所有有这个节点的 hub，到节点路径更快的 hub 分得更多 |
| `web_proxies` | 无 | `{name, listen, url, password, max_conns}`：经 hub 的 `console_socket` 在本地提供它的控制台，`url` 是它的 `ws://host:port/`；`max_conns` 默认 32 |
| `open_timeout` | 20s | 打开一条隧道（经 hub 到服务）的时限 |
| `idle_timeout` | 2h | 同节点 |
| `max_tunnels` | 4096 | 同时打开的隧道数；超出时新接受的连接立即关闭 |
| `console` | 无 | proxy 的页面 |

### 节点或 proxy 连接的 hub

| 字段 | 默认 | 含义 |
|------|------|------|
| `name` | 第一个端点的地址 | 转发和页面用来指称它的名字 |
| `endpoints` | 必填 | `{address, disabled}`，按顺序尝试：`host:port` 或 `tcp://` 为 TCP，`ws://host:port/path`，`quic://host:port` |
| `password` | 必填 | hub 的密码 |
| `body_password` | 无 | hub 的消息体密码（如果有） |
| `quic_pin`、`quic_insecure` | | quic 端点如何检查 hub 证书：按 hub 页面上显示的 SHA-256 指纹，或不检查（仅用于测试）；两者都不设时用系统根证书 |
| `pool_size` | 10，最多 100 | 承载隧道的链路数 |
| `dial_timeout` | 5s | 链路拨号的时限 |
| `call_timeout` | 10s | hub 应答注册或调用的时限 |
| `retry_interval` | 3s | 拨号失败或失去 hub 后，链路再次拨号前等待的时间 |
| `ping_interval`、`ping_timeout` | 5s、5s | 多久测一次到 hub 的往返时间，以及 ping 多久没有应答算作丢失 |

## 链路

节点或 proxy 与每台 hub 保持 `pool_size` 条链路。每条链路拨号第一个未禁用的端点，失败时回退到下一个，
随后注册；拨号失败或失去 hub 后隔 `retry_interval` 再拨号。同名同类型的链路，无论来自多少个进程，在 hub 看来
都是同一个对端。每条隧道始终走一条链路，使它的帧保持顺序；新隧道走承载隧道最少的链路，每条链路最多
承载 64 条，需要更多隧道的对端用更大的连接池。

- **TCP** 和 **WebSocket** 链路是引擎上的连接。WebSocket 链路用于只放行 HTTP 的网络，例如经 nginx 的
  `proxy_pass` 加 `Upgrade`；没有 `wss://`，所以请设置 `body_password`。
- **QUIC** 链路运行在同一个引擎的 UDP 绑定上，使用 fib 的 QUIC、TLS 1.3 和 ALPN `fit-tunnel`。有 quic 监听
  需要证书时，hub 生成自签名证书并保存在它的成员里，所以节点和 proxy 固定的指纹在重启后不变。
- **注册**是以密码为密钥、对对端类型、名字、时间和随机数做的 HMAC-SHA256；时间与 hub 时钟相差不得超过
  5 分钟，同一个随机数只接受一次。
- **消息体加密**：各方都设置 `body_password` 时，每个帧的消息体用 AES-256-GCM 加密，帧头也参与认证。
  每条链路建立时双方各发送一段新的随机字节；链路的每个方向各有自己的密钥，由密码和双方的随机字节经 HKDF
  派生，并为自己的帧编号，编号即 nonce。未加密的帧，或不能作为下一帧解开的帧，都会关闭链路：被重放、
  被调换顺序、被发回发送方或从另一条链路搬过来的帧（包括注册帧）都是如此。所以位于 `tcp://` 或 `ws://`
  链路路径上的攻击者既不能重复链路上传过的内容（发给服务的命令、隧道的关闭、对节点服务的修改），也不能用
  截获的节点注册帧冒充该节点注册。攻击者仍能看到帧的长度和时间，也能切断链路。这是链路协议第 2 版：
  设置了 `body_password` 的 hub、节点或 proxy 会关闭与更早版本对端之间的链路，并在日志中说明对端使用的是
  链路协议第 1 版，所以请一起升级。

### 流控、上限与超时

隧道每一端最多发送 4 MiB 对端尚未确认的数据，达到后暂停读取自己的 TCP 连接；对端在自己的连接接收了数据
之后才确认。慢的服务会拖住向它写数据的 proxy，慢的 proxy 也会拖住服务，按隧道各自进行，中间最多只有一个
窗口和套接字缓冲区的数据。等待一条链路发送的数据，以它所承载隧道的窗口为界。控制台 WebSocket 和 Web 代理
在一侧的积压超过 256 KiB 时暂停读取另一侧。

每次拨号、注册和调用都有时限；链路的 TCP 连接空闲 30s 后开始探测，对端不再应答时关闭；QUIC 连接每隔
`keep_alive` 发送 ping，`idle_timeout` 后关闭；空闲超过 `idle_timeout` 的隧道会被关闭。 hub 限制链路、
对端和隧道的数量、每个对端的链路数、进行中的 QUIC 握手数，以及未注册链路的帧大小（64 KiB）。

## 控制台

每个角色的 `console` 提供它的页面，即[控制台指南](console.zh-CN.md#页面)中的 workbench，菜单中只有该角色的模块；fit 同时
运行 `admin.console` 时，它们也显示在那里，作为隧道组下的 `tunnel.hub`、`tunnel.node` 或 `tunnel.proxy` 模块，无需 `console`
成员。角色自己的控制台登录方式与 `admin.console` 相同（见[控制台](console.zh-CN.md)）：
`{address, cert_file, key_file, users, session_timeout, session_lifetime, max_sessions, max_conns}`，
用户的 bcrypt 哈希由 `fit hash-password` 生成，会话 cookie 加 CSRF token；除非设置 `cert_file` 和
`key_file`，只能监听回环地址。

- **hub**：节点（含服务和延迟）与 proxy；隧道（含两个方向的字节数和整条路径的延迟）。查询节点、设置
  它的服务、踢出对端、关闭隧道。增删改监听；设置密码和控制台 WebSocket；复制 QUIC 证书指纹。在这里设置
  的服务在节点的 `allow_hub_services` 允许时写入节点自己的成员。
- **节点**：它的 hub，以及每条链路使用的协议，可增删改；它的服务，保存后通知所有 hub；它的隧道，
  可关闭。
- **proxy**：它的 hub（编辑方式同节点）、它们的节点和服务，以及每台 hub 分担某个节点隧道的比例；
  查询节点，在 hub 的 `allow_proxy_services` 与节点的 `allow_hub_services` 都允许时经 hub 设置节点的服务；增删转发；它的隧道，可关闭；告知 hub 的 meta；以及经 Web 代理访问
  每台 hub 的控制台。

页面所做的修改通过 `app.Host.Edit` 写入角色在配置中的成员，再像其他配置一样从那里生效：它进入配置历史；
配置了 `admin.store` 时会加密保存到存储中，下次启动时读回（存储见[控制台](console.zh-CN.md)）。没有存储时，修改只在 fit 重启或重新读取配置
文件之前有效，页面会提示这一点。每个页面都有隧道全景图（**详情**）以及主题和语言选择。

hub 的控制台在无法直接访问的地方，可以经加密 WebSocket 访问：设置 hub 的 `console_socket`，proxy 的
`web_proxies` 条目填上它的 URL 和密码，就在本地地址上提供这个控制台。WebSocket 的消息用由密码派生的密钥
做 AES-256-GCM 加密；控制台自己的登录照样需要。

## 重载与上限

- 一切都运行在 fit 的引擎上：隧道没有自己的引擎、套接字或 goroutine，QUIC 也一样，网关的 `limits` 也作用于
  角色的连接。
- 角色切换到新成员时保留没有变化的部分：hub 的链路，设置未变的 hub 对应的节点或 proxy 链路，同一地址上
  的转发。
- 无法监听的监听器或转发仍保留在配置中，在页面上显示错误，并在下一份配置时重试。
- 每种资源都有上限：hub 的 `max_links`、`max_links_per_peer`、`max_peers` 和 `max_tunnels`，每条链路
  64 条隧道，各角色的 `max_tunnels` 和 `idle_timeout`，链路和隧道上的 TCP keep-alive，握手完成后在握手
  时限内没有承载链路的 QUIC 连接会被关闭。
- 每条隧道的流量控制都受检查，由另一端和 hub 共同检查：某一端发送超出其 4 MiB 窗口（另留 1 MiB 给用尽
  窗口的那次读取），或确认了多于发给它的字节，它的隧道就会被关闭，免得它占满其他隧道共用的链路。服务或
  proxy 一侧的连接无法接收到达的数据时，隧道也会关闭。
- 页面在 fit 的登录和 Content-Security-Policy 之后。

## 测试

`go test -race ./tunnel/...` 像 `fit run` 那样通过 fit 的配置、在真实引擎上运行这些角色：经 TCP、
WebSocket 和 QUIC 的隧道（加密与不加密消息体）、大数据量传输、慢服务和慢 proxy 、中途断开的连接、页面修改
写回配置，以及从存储读回设置的重启。`test/docker/tunnel/fit-tunnel-docker.sh test` 让 hub 、节点和
proxy 各自运行在独立容器中，设备位于只有其节点可达的网络上，通过控制台和隧道测试全部功能，然后重启
每个实例，检查它们从各自的存储中恢复。
