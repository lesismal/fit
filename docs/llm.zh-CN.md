# LLM 功能

[English](llm.md) | [简体中文](llm.zh-CN.md)

配置示例（.conf 格式，在 [fit-examples](https://github.com/lesismal/fit-examples) 中）：[llm-hub](https://github.com/lesismal/fit-examples/blob/main/llm-hub/fit.conf), [llm-node](https://github.com/lesismal/fit-examples/blob/main/llm-node/fit.conf), [llm-proxy](https://github.com/lesismal/fit-examples/blob/main/llm-proxy/fit.conf)。

LLM 功能（配置中的 `llm`）在多台机器之间共享 AI 模型，并提供给 agent 调用。它是 fit 的三个角色，各由配置中的一个成员开启：

- **`llm.node`** 提供模型。模型是某个主流模型接口的一个上游端点：OpenAI 及所有 OpenAI 兼容接口、Anthropic、
  Google Gemini、Azure OpenAI、Ollama 原生接口与 Cohere（见[模型与接口](#模型与接口)）。它可以是本机的服务（如
  Ollama、vLLM、llama.cpp），也可以是带 Key 的托管接口。node 还上报本机硬件，并执行一组固定的管理操作。
- **`llm.hub`** 是 node 与 proxy 汇合之处，通常部署在各台机器都能访问的公网服务器上。它维护节点表及其模型，把每次
  调用转给提供该模型、负载最小的节点。
- **`llm.proxy`** 为 agent（Claude Code、Codex、SDK、自己的应用）提供模型接口：`/v1/chat/completions`、
  `/v1/messages`、`/v1beta/models/{model}:generateContent`、`/api/chat`、`/v2/chat` 等。它打通本机或局域网——公网
  hub——其他算力设备上的 node 这条链路，向本机（监听回环地址）或局域网（监听局域网地址，启用 TLS 与 API Key）内的
  agent 提供接口。它不必运行在 agent 所在的机器上：局域网中一台机器上的 proxy 就能服务整个局域网的 agent。

请求与响应按字节原样转发：fit 从不把一种接口转换成另一种，所以工具调用、思考、图片、音频与流式输出等能力取决于上游本身。

一个进程运行配置中设置了成员的每个角色，任意组合，可以与网关的监听和路由并存，也可以没有它们；只有角色、没有路由的配置
不需要监听。每个角色把自己的绑定（页面、互联监听、proxy 的接口）加到监听所用的 fib 引擎上，也在这个引擎上拨号连接对端。
node 的 HTTP 客户端在 fit 自身上游连接池所用的上游引擎上拨号。这些角色为何属于 fit 见
[决策 028](decisions/028-llm-and-tunnel.zh-CN.md)。[隧道](tunnel.zh-CN.md)与 [WebRTC](webrtc.zh-CN.md) 的角色
以同样的方式与之并存。

每个角色把日志写到 fit 的 `llm` 日志器（级别由 `log.levels` 设置），每行以角色的成员名为前缀，如 `llm.node: `。

## 配置

成员为 `llm.hub`、`llm.node` 与 `llm.proxy`。它们与配置的其余部分一样严格解码：未知字段是错误，`fit validate`
检查下文的每个上限。示例用 JSON；同样的字段也可以写在 .conf 或 YAML 配置中。
[配置参考](reference/config.zh-CN.md)的 LLM 一章列出同样的字段。

### 每个角色都有的字段

| 字段 | 类型 | 默认 | 含义 |
| --- | --- | --- | --- |
| `id` | string | 首次启动时生成 | 角色的稳定 ID，对端以它识别角色：最长 128 个字符，不含空白。省略时生成一个（`<role>-<16 位十六进制>`）并写回成员。 |
| `name` | string | 主机名 | 角色向对端显示的名称：最长 64 个字符，不含控制字符，首尾无空白。 |
| `console` | object | 无 | 角色的 Web 页面。见[页面](#页面)。 |
| `listen` | object | 无 | 角色接受对端连接的地方。 |
| `access` | object | 无：由全局 `access` 决定 | `{allow, deny, allow_files, deny_files}`：`listen` 以及 proxy 的 `api` 接受哪些客户端地址的连接，先于全局 [`access`](reference/config.md#access) 决定；被拒绝的连接在接受时即关闭。每次应用都会重新设置，无论成员是否变化，都不重新监听；已打开的链路保留。它的文件像证书文件一样被监视。不包括角色的页面。 |
| `tokens` | array | 无 | 角色的监听签发的令牌，对端用它连接：`{id, name, hash, created_at, expires_at, note, members}`，最多 1,024 个。由页面、Admin API 和 `fit token` 生成，不要手写。见[令牌](#令牌)。 |
| `peers` | array | 无 | 角色拨号并保持连接的对端；最多 64 个。 |
| `limits` | object | 默认值 | 角色连接的上限。 |

`listen`：

| 字段 | 类型 | 默认 | 含义 |
| --- | --- | --- | --- |
| `address` | string | 必填 | 角色接受连接的主机与端口，作为监听引擎上一个独立的绑定。WebSocket 路径为 `/ws`。主机为空表示监听所有地址。 |
| `password` | string | 无 | 已弃用，请改用[令牌](#令牌)：监听方互联的、没有自己密码的各角色共用的密码。对端需证明自己知道它；它从不在网络上传输。持有它的任何一方都可以以这些角色中的任意一个建立连接。本版本仍接受，有对端使用时连接页会给出警告；下一版本将拒绝。 |
| `node_password` | string | `password` | 已弃用：node 以 node 身份连接时证明的密码，用于 hub 或 proxy 的监听。 |
| `proxy_password` | string | `password` | 已弃用：proxy 以 proxy 身份连接时证明的密码，用于 hub 或 node 的监听。 |
| `hub_password` | string | `password` | 已弃用：hub 以 hub 身份连接时证明的密码，用于 node 或 proxy 的监听。 |
| `cert_file`、`key_file` | string | 无 | PEM 文件，包含监听出示的证书、其后的任何中间证书，以及私钥。两者都设置时以 TLS（`wss://`）接受连接，对端用其 `ca_file` 或 `pin` 校验证书。在监听启动时读取，磁盘上的文件变化时再次读取（`fit run -watch-certs`，默认开启）：此后建立的连接使用新证书，已建立的连接保持不变；文件加载失败时（例如只写了一个、另一个还没写），证书保持不变，文件下次变化时再读取。 |
| `self_signed` | bool | false | 以角色自己生成的自签名证书走 TLS 接受连接，用于没有 `cert_file` 的监听。角色会保存它，使其重启后保持不变：有 `admin.store` 时写入 `cert` 与 `key`；没有时写入配置旁、按配置和角色命名的文件，权限 0600（`fit.json` 旁的 `fit.llm.hub.pem`），每次启动时读取。角色在连接页与日志中显示其 SHA-256 指纹：对端把它设为 `pin`。 |
| `cert`、`key` | string | 无 | `self_signed` 生成的 PEM 证书与私钥，配置有 `admin.store` 时由角色写入。删除两者，或删除配置旁的文件，会生成新证书，所有对端都须重新固定其指纹。 |

监听方接受角色的[令牌](#令牌)，令牌可连接它所互联的任何角色的对端；在下一版本之前，它也接受自己的各个密码。拨号方
证明的密码决定它可以以哪个角色 hello（见[hello 与角色](#hello-与角色)）；既没有自己的密码、也没有 `password` 的角色只能
用令牌连接。没有任何密码的监听方只接受令牌，在签发令牌之前不接受任何连接，它启动时会在日志中说明这一点。在不与某角色
互联的角色的监听上设置该角色的密码（如 hub 的 `hub_password`）是错误。一个密码被多个角色共用的监听方（只设置 `password` 时即如此）在启动时记录一条警告。

监听在回环地址（`127.0.0.0/8`、`::1`、`localhost`）之外的地址上（包括主机为空）时需要 TLS：`cert_file` 与
`key_file`，或 `self_signed`。缺少时配置被拒绝，错误信息说明需要添加什么。原因见[传输与认证](#传输与认证)。

`peers[]`：

| 字段 | 类型 | 默认 | 含义 |
| --- | --- | --- | --- |
| `address` | string | 必填 | 对端的主机与端口，即对端的 `listen.address`。主机必填，端口是数字。每个地址只出现一次。 |
| `token` | string | | 对端角色为本角色签发的令牌，`fitt1.<id>.<secret>`，由其页面、Admin API 或 `fit token create` 显示一次。`token` 与 `password` 必须设置其一，不能都设。 |
| `password` | string | | 已弃用：对端监听上拨号方角色的密码：node 用对端的 `listen.node_password` 拨号，proxy 用其 `listen.proxy_password`，hub 用其 `listen.hub_password`；对端没有该角色的密码时用其 `listen.password`。 |
| `role` | string | 必填 | 对端必须声明的角色：`hub`、`node` 或 `proxy`，不能是拨号方自己的角色。hub 拨 node 与 proxy，node 拨 hub 与 proxy，proxy 拨 hub 与 node。 |
| `pin` | string | 无 | 对端监听所出示证书的 SHA-256 指纹，64 位十六进制，忽略冒号与大小写，即其连接页与日志中显示的值。连接走 TLS（`wss://`），且只连向恰好持有该证书的监听；不校验证书的名称与有效期。 |
| `ca_file` | string | 无 | 或者一个 PEM 文件，包含对端证书须链接到的证书颁发机构的证书：连接走 TLS，证书须包含 `server_name`，或地址中的主机名。`pin` 与 `ca_file` 二选一。在设置对端时读取。 |
| `server_name` | string | 地址中的主机 | 配合 `ca_file`：证书须具有的名称，也是发送的 TLS 服务器名称。 |

回环之外地址上的对端需要 `pin` 或 `ca_file`；两者都没有时配置被拒绝，错误信息说明需要添加什么。回环地址上的对端没有它们时
经明文 `ws://` 拨号；有它们时经 `wss://`，此时其监听也须启用 TLS。

`limits`：

| 字段 | 类型 | 默认 | 含义 |
| --- | --- | --- | --- |
| `max_conns` | int | 1024 | 角色同时接受的对端连接数上限。超出的连接被拒绝，拨号方退避。 |
| `max_inflight` | int | 1024 | 一个对端在一条连接上可以同时在本方运行的调用与流的上限。超出的立即失败。 |
| `ping_interval` | duration | `15s` | 连接多久没收到任何东西就发 ping。三个周期没收到任何东西的连接被关闭。至少 100ms。 |
| `slow_timeout` | duration | `30s` | ping 等待 pong 多久之后，连接被视为过慢而关闭。 |
| `retry_interval` | duration | `5s` | 连不上的对端两次拨号之间的最长等待。至少 100ms。 |
| `stream_idle` | duration | `2m` | 流在两个方向都没有数据多久之后结束。至少 1s。 |

### `llm.hub`

每个角色都有的字段，以及：

| 字段 | 类型 | 默认 | 含义 |
| --- | --- | --- | --- |
| `max_nodes` | int | 1024 | hub 节点表中保留的节点数上限，含离线节点。超出时忘掉最久未见的离线节点。 |

### `llm.node`

每个角色都有的字段，以及：

| 字段 | 类型 | 默认 | 含义 |
| --- | --- | --- | --- |
| `description` | string | 无 | 对端显示的节点说明；最长 1024 字节。 |
| `models` | array | 无 | 节点提供的模型；最多 256 个。 |
| `settings` | object | 默认值 | 节点自己的设置。 |
| `proxy` | object | 默认值 | 节点转发给上游的调用的上限。 |

`models[]`：

| 字段 | 类型 | 默认 | 含义 |
| --- | --- | --- | --- |
| `name` | string | 必填 | agent 请求时使用的名称：1 到 128 个字符，不含空白。名称在节点同一 agent 接口的模型中唯一，因此同一名称可以各有一个 OpenAI、Anthropic 与 Gemini 模型；`azure_openai` 模型算作 OpenAI。 |
| `api` | string | 必填 | 上游使用的接口：`openai`、`anthropic`、`gemini`、`azure_openai`、`ollama` 或 `cohere`。它决定模型经哪些 agent 路径访问、上游 URL 与 Key 所在的请求头；见[模型与接口](#模型与接口)。 |
| `base_url` | string | 必填 | 上游的基础 URL，http 或 https，不带查询串、片段与末尾斜杠，如 `http://127.0.0.1:8000/v1`。调用发往它加上调用路径，各种类方式不同：`base_url/chat/completions`、`base_url/models/{upstream}:generateContent`、`base_url/openai/deployments/{upstream}/chat/completions`、`base_url/api/chat`。 |
| `upstream` | string | `name` | 发给上游的模型 ID，在该种类指明模型的地方替代 `name`：请求体、表单字段、Gemini 的路径、Azure 的部署名。最长 256 个字符，不含空白。 |
| `api_key` | string | 无 | 发给上游的 Key：`openai`、`ollama`、`cohere` 用 `Authorization: Bearer`，`anthropic` 用 `x-api-key`，`gemini` 用 `x-goog-api-key`，`azure_openai` 用 `api-key`。最长 4096 个字符，不含空白。页面从不完整显示它。 |
| `api_version` | string | 无 | `azure_openai` 模型每次调用携带的 `api-version`，如 `2024-10-21`：该种类必填，其他种类不接受。最长 64 个字母、数字、点、短横线与下划线。 |
| `disabled` | bool | false | 保留模型但不提供服务。 |
| `description` | string | 无 | 对端显示的模型说明；最长 1024 字节。 |
| `max_inflight` | int | 0 | 节点同时转发给该模型的调用数上限，0 到 100000。超出的调用转到其他节点。0 表示只受 `proxy.max_inflight` 限制。 |

`settings`：

| 字段 | 类型 | 默认 | 含义 |
| --- | --- | --- | --- |
| `allow_hub_manage` | bool | false | 允许已连接的 hub 代管节点：模型、名称与说明、显卡设置、固定操作及其作业。从不包括节点设置，也从不包括 hub 自己提供的命令。 |
| `gpu_sudo` | bool | true | fit 不以 root 运行时，用 `sudo -n` 执行显卡设置、工具安装以及需要 root 的操作。sudo 不能要求输入密码。 |
| `refresh_interval` | duration | `30s` | 节点重新读取硬件信息的间隔：5s 到 1h。 |
| `job_timeout` | duration | `1h` | 作业被杀掉前最多可以运行多久：10s 到 168h。 |
| `gpu_data_dir` | string | 工作目录 | 驱动需手动下载的厂商，其安装脚本在此目录下按厂商名查找安装包。脚本以 `FIT_LLM_GPU_DATA_DIR` 读到它。 |

`proxy`：

| 字段 | 类型 | 默认 | 含义 |
| --- | --- | --- | --- |
| `connect_timeout` | duration | `10s` | 连接上游最多用多久。 |
| `first_byte_timeout` | duration | `5m` | 从调用开始（含连接）到上游开始应答最多多久。 |
| `idle_timeout` | duration | `5m` | 上游在应答中途最多可以沉默多久。 |
| `timeout` | duration | `30m` | 一次调用到应答结束最多多久。 |
| `max_inflight` | int | 256 | 节点在所有模型上同时转发的调用数上限。超出的调用转到其他节点。 |
| `max_body_bytes` | int | 33554432（32 MiB） | 一次调用的请求体上限。只有请求体中指明了模型、需要把它改为 `upstream` 时，请求体才被完整读入。 |

### `llm.proxy`

每个角色都有的字段，以及：

| 字段 | 类型 | 默认 | 含义 |
| --- | --- | --- | --- |
| `api` | object | 无 | proxy 所在机器或局域网中的 agent 调用的接口。没有它时 proxy 仍连接对端，但不提供接口。 |
| `api_keys` | array | 无 | agent 调用接口所用的 Key；最多 256 个。由页面创建与删除。 |

`api`：

| 字段 | 类型 | 默认 | 含义 |
| --- | --- | --- | --- |
| `address` | string | 必填 | 接口监听的主机与端口。回环地址（如 `127.0.0.1:9301`）服务 proxy 所在机器上的 agent；局域网地址（如 `192.168.1.10:9301`）或空主机也服务其他机器上的 agent，此时需要 `cert_file` 与 `key_file`，Key 才不会以明文经过网络。 |
| `cert_file` | string | 无 | 接口证书的 PEM 文件，后接中间证书。与 `key_file` 一起设置时，接口经 TLS 提供。 |
| `key_file` | string | 无 | 证书私钥的 PEM 文件。 |
| `max_body_bytes` | int | 33554432（32 MiB） | 请求体上限。请求体被完整读入，以取得其中的模型，并在某条路由无法提供服务时再次发送；超出的得到 413。 |
| `max_conns` | int | 256 | 接口同时保持的连接数上限；超出的连接在接受后立即关闭。 |
| `max_inflight` | int | 256 | 接口同时转发的调用数上限；超出的得到 429。 |
| `first_byte_timeout` | duration | `5m` | 调用等待应答开始最多多久。 |
| `timeout` | duration | `30m` | 一次调用到应答结束最多多久。 |

`api_keys[]`：

| 字段 | 类型 | 默认 | 含义 |
| --- | --- | --- | --- |
| `id` | string | 必填 | Key 的 ID：16 位十六进制，唯一。 |
| `name` | string | 必填 | Key 的用途：1 到 64 个字符。 |
| `prefix` | string | 无 | Key 的开头几个字符，在页面上显示；最长 16。 |
| `hash` | string | 必填 | Key 的 SHA-256，64 位十六进制。Key 本身不保存。 |
| `created_at` | int | 无 | Key 的创建时间，自 1970 年起的秒数。 |

### 一个 hub

```json
{
  "llm": {
    "hub": {
      "name": "hub-1",
      "console": {
        "address": "127.0.0.1:9200",
        "users": [{"username": "admin", "password_hash": "$2a$10$sKGBAOOCI4p4WWJcVCuAH.ivT8pNy2DFbs6pd8xmtNEZ/5L1SeLvm"}]
      },
      "listen": {"address": ":9210", "self_signed": true},
      "tokens": [
        {"id": "fdd44d906b8e6ce2", "name": "gpu-1", "hash": "1c994a095de7b566dcce0f229b829af96d85911b6d3e786f217c77340bc36aa4",
         "created_at": 1793900000, "members": ["node-8c1f6e2a90b34d57"]},
        {"id": "5a3716dd56e5e458", "name": "laptop", "hash": "4e6349fdb79b1b48fb57cc61b5aa9593490f288f28f722e0245c04a9df6c66df",
         "created_at": 1793900000, "expires_at": 1825436000}
      ],
      "limits": {"max_conns": 1024, "max_inflight": 1024, "ping_interval": "15s", "slow_timeout": "30s",
                 "retry_interval": "5s", "stream_idle": "2m"},
      "max_nodes": 1024
    }
  }
}
```

### 一个 node

```json
{
  "llm": {
    "node": {
      "name": "gpu-1",
      "description": "Two RTX 4090s",
      "console": {
        "address": "127.0.0.1:9100",
        "users": [{"username": "admin", "password_hash": "$2a$10$sKGBAOOCI4p4WWJcVCuAH.ivT8pNy2DFbs6pd8xmtNEZ/5L1SeLvm"}]
      },
      "peers": [{"address": "hub.example.net:9210", "token": "fitt1.fdd44d906b8e6ce2.Hm2vJvGyTEJVh2_zzu0ZJlENz1A3IYXS2JydjHyJShQ",
                 "role": "hub", "pin": "134daba9eaf7f4b2f15a8ca3788273bd52d0b7f6f34581a4561458814d95b0c9"}],
      "models": [
        {"name": "qwen3-32b", "api": "openai", "base_url": "http://127.0.0.1:8000/v1", "upstream": "Qwen/Qwen3-32B",
         "max_inflight": 4},
        {"name": "llama3.2", "api": "ollama", "base_url": "http://127.0.0.1:11434", "upstream": "llama3.2:3b"},
        {"name": "claude-sonnet", "api": "anthropic", "base_url": "https://api.anthropic.com/v1",
         "upstream": "claude-sonnet-4-5", "api_key": "sk-ant-..."}
      ],
      "settings": {"allow_hub_manage": false, "gpu_sudo": true, "refresh_interval": "30s", "job_timeout": "1h",
                   "gpu_data_dir": "/var/lib/fit/gpu"},
      "proxy": {"connect_timeout": "10s", "first_byte_timeout": "5m", "idle_timeout": "5m", "timeout": "30m",
                "max_inflight": 256, "max_body_bytes": 33554432}
    }
  }
}
```

### 一个 proxy

```json
{
  "llm": {
    "proxy": {
      "name": "laptop",
      "console": {
        "address": "127.0.0.1:9300",
        "users": [{"username": "admin", "password_hash": "$2a$10$sKGBAOOCI4p4WWJcVCuAH.ivT8pNy2DFbs6pd8xmtNEZ/5L1SeLvm"}]
      },
      "peers": [{"address": "hub.example.net:9210", "token": "fitt1.5a3716dd56e5e458.nz51QI_A3avzPo4tagvevbtlJ-dNi-Us-k4-il9gAV4",
                 "role": "hub", "pin": "134daba9eaf7f4b2f15a8ca3788273bd52d0b7f6f34581a4561458814d95b0c9"}],
      "api": {"address": "127.0.0.1:9301", "max_body_bytes": 33554432, "max_conns": 256, "max_inflight": 256,
              "first_byte_timeout": "5m", "timeout": "30m"},
      "api_keys": [
        {"id": "3f2a9c1d7e5b4a60", "name": "claude-code", "prefix": "ah-Xq3v9Lk",
         "hash": "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", "created_at": 1761955200}
      ]
    }
  }
}
```

`password_hash` 用 `fit hash-password` 生成；上面的只是示例。API Key 在 proxy 的页面上创建，不要手写；hub 的
`tokens` 也一样：其连接页、Admin API 或 `fit token create -role llm.hub -name gpu-1` 签发每个令牌，保存其哈希，并只
显示一次令牌，供 node 或 proxy 填作对端的 `token`；`members` 由 hub 自己写入。`pin` 是
hub 证书的指纹，hub 生成证书后在其连接页与日志中显示；上面的只是示例。

### 三个地方

公网服务器 hub.example.net 上的 hub、实验室里一台 GPU 机器上的 node、办公室局域网中一台机器（192.168.1.10）上的
proxy。node 与 proxy 都拨号连接 hub，它们自己都不需要监听，因此也都不需要公网地址。proxy 经 TLS、以其 API Key
服务局域网中每台机器上的 agent。hub 以自己生成的证书走 TLS 接受连接；node 与 proxy 按其连接页显示的指纹固定它，并各自
用 hub 签发的令牌连接。

服务器，只接受令牌：

```json
{"llm": {"hub": {"listen": {"address": ":9210", "self_signed": true}}}}
```

```sh
fit token create -c fit.json -role llm.hub -name gpu-1    # GPU 机器的令牌，只显示一次
fit token create -c fit.json -role llm.hub -name office   # 局域网机器的令牌
```

GPU 机器：

```json
{"llm": {"node": {
  "peers": [{"address": "hub.example.net:9210", "token": "fitt1.fdd44d906b8e6ce2.Hm2vJvGyTEJVh2_zzu0ZJlENz1A3IYXS2JydjHyJShQ",
             "role": "hub", "pin": "134daba9eaf7f4b2f15a8ca3788273bd52d0b7f6f34581a4561458814d95b0c9"}],
  "models": [{"name": "qwen3-32b", "api": "openai", "base_url": "http://127.0.0.1:8000/v1", "upstream": "Qwen/Qwen3-32B"}]
}}}
```

局域网中的机器：

```json
{"llm": {"proxy": {
  "peers": [{"address": "hub.example.net:9210", "token": "fitt1.5a3716dd56e5e458.nz51QI_A3avzPo4tagvevbtlJ-dNi-Us-k4-il9gAV4",
             "role": "hub", "pin": "134daba9eaf7f4b2f15a8ca3788273bd52d0b7f6f34581a4561458814d95b0c9"}],
  "api": {"address": "192.168.1.10:9301", "cert_file": "/etc/fit/lan.crt", "key_file": "/etc/fit/lan.key"}
}}}
```

局域网中的 agent 以 `https://192.168.1.10:9301` 作为 base URL。只服务本机 agent 的 proxy 监听回环地址，如
`127.0.0.1:9301`，不需要证书。

每台机器加上 `console` 即可在浏览器中管理其角色。proxy 也可以直接拨号连接 node，一个进程也可以同时运行 hub 与 node。
持有证书颁发机构为 hub.example.net 签发的证书的 hub 用 `cert_file` 与 `key_file` 代替 `self_signed`，其对端用
`ca_file` 代替 `pin`。

## 模型与接口

模型的 `api` 是其上游所讲的接口。它决定 `llm.proxy` 的哪些端点能访问该模型、node 调用的 URL、node 发送模型 Key
所用的请求头，以及模型在哪里被指明——node 就在那里用 `upstream` 替代名称。

| `api` | 上游 | agent 在 llm.proxy 上调用 | node 调用 | Key 请求头 | 模型位于 |
| --- | --- | --- | --- | --- | --- |
| `openai` | OpenAI 及所有 OpenAI 兼容接口：DeepSeek、通义千问（DashScope 兼容模式）、Mistral、Groq、xAI、Moonshot、智谱、OpenRouter、Together、Fireworks；vLLM、SGLang、llama.cpp server、LM Studio、Ollama 的 `/v1` | `POST /v1/{call}` | `base_url/{call}` | `Authorization: Bearer` | JSON 请求体的 `model`，或表单的 `model` 字段 |
| `azure_openai` | Azure OpenAI 部署 | 与 `openai` 相同：`POST /v1/{call}` | `base_url/openai/deployments/{upstream}/{call}?api-version={api_version}`；`responses` 发往 `base_url/openai/responses?api-version=…` | `api-key` | URL 中的部署名；`responses` 为请求体的 `model` |
| `anthropic` | Anthropic，以及讲其 Messages 接口的服务 | `POST /v1/messages`、`/v1/messages/count_tokens` | `base_url/{call}` | `x-api-key` | JSON 请求体的 `model` |
| `gemini` | Google Gemini API | `POST /v1beta/models/{model}:{method}` 或 `/v1/models/{model}:{method}` | `base_url/models/{upstream}:{method}` | `x-goog-api-key` | 路径 |
| `ollama` | Ollama 原生接口 | `POST /api/chat`、`/api/generate`、`/api/embed` | `base_url/api/…` | 设置了 `api_key` 时为 `Authorization: Bearer` | JSON 请求体的 `model` |
| `cohere` | Cohere v2 接口 | `POST /v2/chat`、`/v2/embed`、`/v2/rerank` | `base_url/{call}` | `Authorization: Bearer` | JSON 请求体的 `model` |

各接口接受的调用：

- **openai**：`chat/completions`、`responses`、`completions`、`embeddings`、`moderations`、`images/generations`、
  `images/edits`、`images/variations`、`audio/speech`、`audio/transcriptions`、`audio/translations`，以及
  `rerank`（vLLM、Jina 等在 `/v1/rerank` 提供的 Cohere 风格重排接口）。`azure_openai` 模型接受除 `moderations`、
  `images/variations` 与 `rerank` 外的全部调用，这三个得到 404。
- **anthropic**：`messages`、`messages/count_tokens`。
- **gemini**：`generateContent`、`streamGenerateContent`、`countTokens`、`embedContent`、`batchEmbedContents`。
- **ollama**：`api/chat`、`api/generate`、`api/embed`。
- **cohere**：`chat`、`embed`、`rerank`。

只转发无状态的调用。在厂商处保存状态的调用，如文件、批处理、存储的 responses、assistants、微调或 Gemini 的缓存内容，
都不提供，得到 404。

请求体与应答：

- 请求体是 JSON，OpenAI 的上传（`images/edits`、`images/variations`、`audio/transcriptions`、
  `audio/translations`）除外：它们是 `multipart/form-data`，模型在 `model` 字段中。请求体的内容类型（含 boundary）
  随它一起发往上游。
- 应答按上游给出的状态码与内容类型逐段返回：SSE 事件、Ollama 的 NDJSON、Gemini 不带 `alt=sse` 流式输出时的 JSON
  数组、`audio/speech` 的音频字节。
- agent 的查询参数中只有 Gemini 的 `alt` 发往上游（`alt=sse` 表示 SSE）；`azure_openai` 模型的 `api-version` 由
  node 自己设置；其他参数一律不发往上游，包括 agent 的 Key。
- agent 的请求头中，只有 `anthropic-version`（agent 未提供时为 2023-06-01）与 `anthropic-beta` 发往 `anthropic`
  上游，`OpenAI-Beta` 发往 `openai` 上游。其他请求头一律不发，包括 agent 的 Key；node 发送模型自己的 Key。

`upstream` 在模型被指明的地方替代名称：

- 在 JSON 请求体顶层的 `model` 中（`openai`、`anthropic`、`ollama`、`cohere`，以及 Azure 的 `responses`）：node 在
  `proxy.max_body_bytes` 之内完整读入请求体，设置 `model`，其他成员保留原始字节。
- 在表单的 `model` 字段中：node 在 `proxy.max_body_bytes` 之内完整读入请求体，替换该字段的值；其他字节（包括文件）
  原样保留。
- 在 Gemini 的路径中：请求体不变，因此流式转发；`batchEmbedContents` 例外，其 `requests[].model` 也被设为
  `models/{upstream}`。
- 作为 Azure 的部署名在 URL 中：请求体原样流式转发。

模型已是上游 ID、或请求体没有指明模型时，请求体随到随发给上游。

各种类的 `base_url`：

| `api` | `base_url` 示例 |
| --- | --- |
| `openai` | `https://api.openai.com/v1`、`https://api.deepseek.com/v1`、`https://dashscope.aliyuncs.com/compatible-mode/v1`、`https://api.mistral.ai/v1`、`https://api.groq.com/openai/v1`、`https://api.x.ai/v1`、`https://api.moonshot.cn/v1`、`https://open.bigmodel.cn/api/paas/v4`、`https://openrouter.ai/api/v1`、`http://127.0.0.1:8000/v1`（vLLM、SGLang）、`http://127.0.0.1:8080/v1`（llama.cpp）、`http://127.0.0.1:1234/v1`（LM Studio）、`http://127.0.0.1:11434/v1`（Ollama） |
| `azure_openai` | `https://{resource}.openai.azure.com` |
| `anthropic` | `https://api.anthropic.com/v1` |
| `gemini` | `https://generativelanguage.googleapis.com/v1beta` |
| `ollama` | `http://127.0.0.1:11434` |
| `cohere` | `https://api.cohere.com/v2` |

每种各提供一个模型的 node：

```json
{"llm": {"node": {
  "peers": [{"address": "hub.example.net:9210", "token": "fitt1.fdd44d906b8e6ce2.Hm2vJvGyTEJVh2_zzu0ZJlENz1A3IYXS2JydjHyJShQ",
             "role": "hub", "pin": "134daba9eaf7f4b2f15a8ca3788273bd52d0b7f6f34581a4561458814d95b0c9"}],
  "models": [
    {"name": "deepseek-chat", "api": "openai", "base_url": "https://api.deepseek.com/v1", "api_key": "sk-..."},
    {"name": "whisper", "api": "openai", "base_url": "http://127.0.0.1:8000/v1", "upstream": "openai/whisper-large-v3"},
    {"name": "claude-sonnet", "api": "anthropic", "base_url": "https://api.anthropic.com/v1",
     "upstream": "claude-sonnet-4-5", "api_key": "sk-ant-..."},
    {"name": "gemini-flash", "api": "gemini", "base_url": "https://generativelanguage.googleapis.com/v1beta",
     "upstream": "gemini-2.5-flash", "api_key": "AIza..."},
    {"name": "gpt-4o", "api": "azure_openai", "base_url": "https://contoso.openai.azure.com",
     "upstream": "gpt-4o-prod", "api_version": "2024-10-21", "api_key": "..."},
    {"name": "llama3.2", "api": "ollama", "base_url": "http://127.0.0.1:11434", "upstream": "llama3.2:3b"},
    {"name": "command-a", "api": "cohere", "base_url": "https://api.cohere.com/v2",
     "upstream": "command-a-03-2025", "api_key": "..."}
  ]
}}}
```

### 配置是唯一的来源

角色不在自己的文件中保存任何东西。页面所做的修改写回其成员：监听、对端、名称与说明、连接的上限（`limits`）、node 的
模型、设置与上游上限（`proxy`）、hub 的 `max_nodes`、proxy 的接口（`api`）与 API Key，以及首次启动时生成的 ID。取默认值的
设置不写入成员。修改经 `admin.Configs` 作为一次配置编辑生效，其来源是该成员，如 `llm.node`：
把成员替换后的运行中配置像任何配置一样应用，先经检查，记入历史，可以回滚。检查不通过的修改不改变任何东西，页面得到
400 与原因。角色随后切换到新成员，不断开未改变的连接。

- 使用 `admin.store` 时，修改加密写入存储，重启后仍在。
- 没有存储时，修改只持续到 fit 重启或重新读取配置文件，并有一条警告说明这一点。依赖页面之前，先把配置放进存储。

[控制台指南](console.zh-CN.md)介绍这些编辑产生的版本、如何回滚其中一个，以及 `admin.store` 如何保存配置。

在页面上踢掉连接是唯一不写入配置的修改：它关闭连接，拨号方随后重新连接。

成员中有机密：对端的令牌、互联密码与模型的 API Key，以明文保存。普通配置文件的安全性等同于该文件本身；`admin.store`
把它们加密保存。proxy 的 API Key 只保存其 SHA-256，监听方的令牌只保存不能用来连接的哈希，因此配置与存储中都没有他人能
直接使用的 Key 或令牌。

## 互联

角色之间通过 WebSocket 连接组成的互联网络通信。

### 传输与认证

设置了 `listen` 的角色在监听引擎上 `listen.address` 处加一个绑定，角色的 `access`（其次是全局的）拒绝的地址发来的
连接在接受时即关闭。它只升级 `/ws`；其他路径一律以 403 拒绝。对端设置了
`pin` 或 `ca_file` 时，拨号方连接 `wss://<address>/ws`，否则连接 `ws://<address>/ws`（只有回环地址允许）；整个拨号
（含 TLS 与认证）限时 10 秒。

回环之外的连接走 TLS 1.3，由 fib 的 TLS 提供，其握手在等待时不占用 worker。拨号方校验监听方的证书：按 `pin`，即证书的
SHA-256，以常量时间比较，代替证书链与名称的校验；或按 `ca_file`，校验到其某个证书颁发机构的证书链以及名称。两端都不恢复
TLS 会话，所以每条连接都在完整握手中校验证书。下面的令牌（或密码）握手仍在 TLS 之内进行，因此连接需要两者：拨号方期望
的证书，以及监听方签发的令牌。

TLS 使连接只属于建立它的两端。握手只在连接建立时校验一次，之后的帧不带自身的证明。在明文 `ws://` 上，占用了监听方
地址的一方即使不知道任何密码，也可以把拨号方的握手转发给真正的监听方，然后以监听方的身份在连接上发送请求，拨号方会执行
它们：开启了 `allow_hub_manage` 的 node 会把它们当作其 hub 的请求执行。在 TLS 上，转发方无法为固定的证书应答，经它转发的
记录以它没有的密钥加密：它注入的内容会使连接结束。

升级完成后、任何帧之前，使用令牌时：

1. 监听方发送 32 字节随机挑战。
2. 拨号方发回其令牌的 ID、自己的 32 字节随机数，以及对令牌 ID、该随机数和挑战组成的记录给出的令牌证明
   （密钥见[决定 035](decisions/035-link-tokens.zh-CN.md)）。
3. 监听方查找该令牌，不是它签发的或已过期的令牌被拒绝，以关闭码 4001 关闭并说明原因，然后校验证明。它的确认中带有它
   自己对同一记录给出的令牌证明，使用另一把密钥和另一方向的标签。令牌可连接监听方接受的任何角色的对端。
4. 拨号方校验监听方的证明，缺失或不符就放弃拨号。

在另一条连接上重放的证明、针对另一个随机数的证明、或发回给发送方的证明都通不过校验；监听方只保存令牌的哈希，它不能
用来连接。使用密码时（已弃用）：

1. 监听方发送 32 字节随机挑战。
2. 拨号方以自己的密码（监听方为拨号方角色设置的密码）为密钥，对挑战计算 HMAC-SHA256，连同自己的 32 字节随机数一起发回。
3. 监听方逐一用自己的各个密码校验，通过后回确认，一个都证明不了时以关闭码 4001 关闭。被证明的密码决定该连接可以以哪些
   角色 hello。确认中带有它自己的证明：以同一密码为密钥，对一个固定标签、挑战与拨号方随机数计算的 HMAC-SHA256。
4. 拨号方校验监听方的证明，缺失或不符就放弃拨号，使不知道密码的监听方得不到连接。仅靠这一步挡不住占用了该地址、把握手
   转发给真正监听方的一方；TLS 加上证书校验才能挡住。

监听方仍接受不发送随机数的早先版本拨号方；本版本的拨号方不接受不做证明的早先版本监听方，所以应先升级监听方。

握手最多 5 秒。监听方至多持有 256 条尚未认证的连接（从 TCP 连接建立算起），来自同一 IP 地址的至多 16 条；再多的连接在
打开时即被关闭，使从不认证的连接无法占据认证连接的位置。密码从不在网络上传输，录下的证明也无法重放。密码错误、证书与指纹或证书颁发机构不符，或明文拨号方
连到 TLS 监听方，都表现为拨号失败，拨号方随之退避；原因显示在其连接页上。在 TLS 上，提示词、应答与管理调用都经过加密；
在回环地址上经明文 `ws://` 传输时，它们不离开本机。

### 迁移到 TLS

从本版本起，回环之外必须使用 TLS，监听或对端在回环之外却没有 TLS 的配置会被拒绝，因此有这类设置的角色在修改之前不会
启动。对每个有这类监听的角色：

1. 在其 `listen` 中加上 `"self_signed": true`（或 `cert_file` 与 `key_file`），然后启动它。其日志与连接页给出证书的指纹。
2. 把该指纹作为 `pin` 加到每个拨号连接它的对端上（证书由证书颁发机构签发时用 `ca_file`）。

监听方与其拨号方要一起迁移：设置了 `pin` 的拨号方连不上明文监听方，明文拨号方也连不上 TLS 监听方。同一台机器上经回环
互联的角色无需修改。

### 令牌

角色的监听为每个拨号连接它的对端签发一个令牌，代替同一角色的对端共享的密码
（[决定 035](decisions/035-link-tokens.zh-CN.md)）：

- **每个令牌都是独立的凭据。** 令牌形如 `fitt1.<id>.<secret>`，即 16 位十六进制的 ID 和 256 位随机数。角色在其成员的
  `tokens` 中保存 ID、名称、生成时间、可选的过期时间、可选的备注和一个哈希，从不保存令牌本身：令牌只在生成时显示一次。
- **不绑定角色。** 有效的令牌可连接监听方接受的任何角色的对端；对端能调用什么取决于它 hello 时声明的角色，
  见[hello 与角色](#hello-与角色)。
- **绑定到成员。** 第一个用某个令牌 hello 的 ID 被绑定到该令牌，并写入令牌的 `members`，因此其他令牌不能再以这个 ID
  hello，重启后也一样。吊销该令牌即释放它：要把对端迁移到新令牌，先签发新令牌，吊销旧令牌，再把新令牌交给该对端。
- **过期。** 令牌可以在指定时间或一段时长（`30d`、`720h`）之后过期；过期的令牌被拒绝，其连接在过期时关闭（如果系统
  时钟跳变，最多晚一分钟）。吊销令牌会立即关闭它的连接。
- **三种管理方式**，每次修改都是对角色成员的一次配置编辑，像页面的修改一样生效并保存，并记录到 `admin` 日志：连接页的
  **连接令牌**卡片（方法 `/mesh/tokens/list`、`/mesh/tokens/create`、`/mesh/tokens/expiry`、`/mesh/tokens/revoke`）；
  Admin API，`GET /tokens/llm.hub`、`POST /tokens/llm.hub` `{name, note, ttl | expires_at}`、
  `PATCH /tokens/llm.hub/{id}` `{ttl | expires_at}`（都不给表示永不过期）和 `DELETE /tokens/llm.hub/{id}`，`llm.node`
  与 `llm.proxy` 同理；以及 `fit token list|create|expiry|revoke -role llm.hub`。列表给出每个令牌自 fit 启动以来的最近
  一次使用及其来源地址、它的成员和当前使用它的连接。

### 迁移到令牌

本版本仍接受监听方的各个密码，与令牌并存，下一版本将拒绝。连接页显示哪些入站连接证明的是共享密码，只要还有，就给出
警告。迁移一个角色：

1. 先升级监听方的 fit：带令牌的拨号方连不上更早版本的监听方。
2. 为每个拨号连接它的对端签发令牌，把它设为该对端的 `token`，代替其 `password`（连接页的**连接到…**表单会同时完成两者）。
3. 警告消失后，编辑监听并勾选**移除全部密码**，从此只接受令牌连接。

### 帧、会话与流

每条 WebSocket 消息是一个帧：类型（request、response、stream-open、stream-data、stream-end、stream-cancel、push、
ping、pong、credit、stream-keepalive、drain）、标志（gzip、error、binary）、32 位流 ID、请求 / 打开流 / 推送的方法名，
以及载荷。1 KiB 及以上的载荷在压缩后更小时用 gzip 压缩。压缩前后的帧都不超过 16 MiB。

每条连接上运行一个会话。拨号方的流 ID 为奇数，监听方为偶数；请求也占用一个流 ID。请求得到一个应答；推送没有应答，
按到达顺序逐个执行。对端在一条连接上最多同时运行 `limits.max_inflight` 个请求与流；再多一个立即以限流错误失败。
等待处理的推送超过 1024 个的连接被关闭。

流在两个方向上传输数据。每个方向有 256 KiB 的接收窗口：发送方在有额度时发送，接收方消费后归还额度。慢读者只阻塞
自己的流。只有响应方能结束流，同时给出结果；打开方可以关闭自己一侧（如流式请求体在结束时那样），或取消流。两个方向在 `stream_idle` 内都没有数据的流结束；仍在工作的一方（例如等待
上游第一个 token 的 node）发送 stream-keepalive 帧，重置空闲计时。

### 心跳、慢连接与下线

每条连接一个定时器。`ping_interval` 内没收到任何东西的连接发 ping，最近一次往返时间已是两个周期之前测得的连接也发 ping，
使连接两端都知道其延迟；三个周期都没收到的连接被关闭（关闭码 4002）。
pong 排在其 ping 之前发送的所有内容之后，所以 ping 等待 pong 达到 `slow_timeout` 说明连接积压到了这个程度，连接被关闭
（关闭码 4004）。超过 `max_conns` 的监听方以关闭码 4003 拒绝连接。

角色停止时在每条连接上发送 drain：对端不再在其上发起新调用，进行中的调用最多再运行 10 秒结束，然后连接关闭。拨号方
随后按退避重新连接。

### 流量与延迟

每条连接统计其双向传输的帧字节数（按传输时的压缩大小，含 ping），以及其 ping 的往返时间：最近一次、近期权重最大的移动
平均（每个新值按 0.3 的比例调整它）与最短的一次。pong 排在其 ping 之前发送的所有内容之后，所以繁忙连接的往返时间包含其
排队时间。

每个角色还按路由统计自启动以来转发的模型调用：proxy 按其调用的 hub 或 node 与模型，hub 按 proxy、node 与模型，node 按
调用方与模型。路由的调用数、失败数（中断的调用；在 node 与 proxy 上，上游 400 及以上的应答也计入）、进行中的调用、
请求字节（请求体，内联或流式）与应答字节（头帧与应答体）随调用的流传输读取，所以长回答在流式传输时即计入。最多单独
统计 1024 条路由；其余路由的调用合并统计为 `other`。这些都不写入任何地方。

`/mesh/status` 只对角色自己的页面开放，应答 `{self, links, routes, at}`：角色的身份；每条连接（与 `/mesh/peers` 所列相同）
及其 `link`，即 `{bytes_in, bytes_out, rtt: {last, avg, min, samples}}`（毫秒）；路由，即 `{from, to, model, calls, errors,
inflight, req_bytes, resp_bytes, last_at}`（proxy 的 agent 的 `from` 为空，node 上游的 `to` 为空）；以及取得的时间 `at`
（毫秒），页面据两次应答计算速率。

### hello 与角色

拨号方的第一个调用是 `/mesh/hello`，带上自己的身份：角色、ID、名称、主机名、版本、进程 ID、启动时间，以及有的话
其互联地址与页面 URL。监听方回复自己的身份。
15 秒内没有完成 hello 的连接被关闭。以下对端被拒绝：

- 角色与本方相同（hub 不与 hub 互联，node 不与 node，proxy 不与 proxy）；
- 角色不是本方互联的角色：hub 连 node 与 proxy，node 连 hub 与 proxy，proxy 连 hub 与 node；
- 用密码拨入的连接上，角色不是该连接所证明密码对应的角色；
- 用令牌拨入的连接上，其 ID 已绑定到另一个令牌（见[令牌](#令牌)）；
- 拨出的连接上，角色不是 `peers[].role` 所声明的；
- ID 与本方相同；
- ID 已经以另一个角色连接着。

被拒绝的拨号方至少等待 30 秒（`retry_interval` 更长时等它）再试。对端可以调用什么取决于它在 hello 中声明的角色。令牌不
绑定角色：持有者可以以监听方接受的任何角色 hello，其 ID 绑定到该令牌，因此持有其他令牌的一方不能以这个 ID hello 并接走它
的调用。使用密码时，角色与对端证明的密码绑定：持有 hub 的 `proxy_password` 的 proxy 不能以 node 身份加入 hub；持有 node 的 `proxy_password` 时也
不能以 hub 身份向该 node hello，因此无法管理开启了 `allow_hub_manage` 的 node。hello 被拒绝的连接没有角色：它调用的任何
方法都不会运行，一秒后连接被关闭。只设置 `listen.password`，或把某个角色的密码设得与其他角色相同，就像角色还没有各自密码
时一样，信任其持有者担任它所对应的每个角色；只把它交给在这些角色上都可信的对端。同一种角色共用其密码：知道 hub 的
`node_password` 的 node，若占用了该 hub 的地址，并且（在回环之外）持有其 pin 所指的 hub 证书，可以对拨号到该地址的其他
node 冒充该 hub。

### 连接池与退避

与同一对端（按 ID）的所有连接，无论哪一方拨出，组成一个池。调用在池中轮流使用连接，跳过正在下线的连接；只剩下线中
连接的对端视为离线，对它的调用立即失败，以便调用方换一个对端。

拨号方对 `peers` 中的对端从不放弃。连续第 n 次失败后，它等待 `min(retry_interval, 1s × 2ⁿ)` 以内的随机时长，至少
100ms。存活 10 秒及以上的连接断开后，步长从 1 秒重新开始。

### 请求体

proxy 把 1 MiB 以内的请求体放在转发流的打开请求中（`ProxyReq.body`），更大的则以 `body_stream: true` 加上长度
（`body_size`）发送，随后在流的额度内以 64 KiB 的帧发送请求体。hub 随到随转这些帧；node 随到随发给上游。打开请求还
携带调用的接口、路径与模型，请求体不是 JSON 时其内容类型（`content_type`），以及发往上游的查询参数（`query`）。

## node

### 模型

每个模型是 `models[]` 中的一项。其状态为 `disabled`、`error`（最近一次检查或调用发现上游不可用）或 `ready`；只有就绪
且未达 `max_inflight` 的模型提供服务。

页面，以及 `allow_hub_manage` 开启时的 hub，可以列出、保存、删除与检查模型。模型的 Key 从不完整显示：列表只显示其前
4 位与后 4 位，不超过 12 个字符的 Key 显示为 `****`。保存时 Key 为空或为打码值，表示保留成员中已有的 Key。

### 检查

检查是带调用认证头、对上游模型列表的 `GET`，限时 15 秒：`base_url/models`（`openai`、`anthropic`、`gemini`、
`cohere`）、`base_url/openai/models?api-version=…`（`azure_openai`）或 `base_url/api/tags`（`ollama`）。连接失败、401、403 或 5xx 应答使模型进入 `error`；其他应答
（包括 404 与 405）表示上游可用。已启用的模型每 60 秒检查一次，处于 `error` 时每 15 秒一次，恢复的上游很快就能重新
提供服务。调用时连不上上游同样使模型进入 `error`；状态码低于 500 的应答清除错误。

### 转发

`/node/proxy` 处理一次 agent 调用，使用 fib 的异步 HTTP 客户端：

1. 调用必须属于其接口（见[模型与接口](#模型与接口)），请求体是 JSON 或带 boundary 的 `multipart/form-data`。
2. 模型（名称，且种类讲该调用的接口）必须就绪且未达其 `max_inflight`，节点未达 `proxy.max_inflight`。否则调用以
   不可用失败，hub 或 proxy 换一个节点。模型种类不提供的调用（如 `azure_openai` 模型的 `images/variations`）以
   未找到失败。
3. URL 按种类构造。请求体随到随发给上游。只有当模型的 `upstream` 与 agent 请求的名称不同、且请求体指明了模型时，
   才完整读入请求体以设置它；其他字节保留。超过 `proxy.max_body_bytes` 的请求体失败。
4. 上游收到请求体的内容类型、放在该种类请求头中的模型 Key，以及[模型与接口](#模型与接口)列出的请求头与查询参数。
   agent 的其他请求头一律不转发，包括它自己的 Key。
5. 在收到上游第一个字节之前，node 保持流存活。返回的第一帧是头帧：上游状态码、内容类型与节点 ID。之后应答体的每一段
   随到随作为一帧发送，所以 SSE 事件不会被攒批。
6. 两个方向的背压相连。请求体在上游连接的写队列中等待，队列排空之前流的额度不归还。调用方不给额度时应答体暂停，
   给出额度后继续。
7. `connect_timeout`、`first_byte_timeout`、`idle_timeout` 与 `timeout` 约束每次调用。调用方取消或离开时，上游请求
   随之取消。

每次调用记录一行日志：模型、接口、路径、状态码、字节数、耗时与调用方。

页面上的“测试调用”打开的流与 agent 的调用相同。

### 操作与作业

node 从不执行调用方提供的脚本或命令。它的管理操作是代码中定义的固定列表：安装 Ollama；列出、拉取、查看与删除 Ollama
模型；查看 Ollama 已加载的模型；Ollama 服务的状态、重启与日志；`nvidia-smi`；安装 NVIDIA Container Toolkit。每个操作
只在适用的系统上提供（Ollama 服务的操作在 Linux 上；安装 Ollama 在 Linux 上，以及装有 Homebrew 的 macOS 上）。每个
操作只接受声明过的参数，按正则
校验，每个参数作为独立的 argv 元素传入，从不经过 shell。显卡设置（按厂商的功耗上限、频率、风扇等）按类型与范围校验，
同样执行；厂商管理软件的安装运行代码中的常量脚本。

操作、显卡设置与安装都以作业运行，有自己的池：最多同时运行 8 个，第 9 个被拒绝；保留 200 个作业，先清理最早结束的；
每个作业保留最后 1 MiB 输出。超过 `job_timeout` 的作业连同其进程组被杀掉。页面随输出写入跟随作业。

硬件信息（每 `refresh_interval` 一次）与显卡状态（页面或 hub 请求时）在节点的一个小池上读取（运行 2 个，等待 16 个）。
显卡状态用厂商的工具读取，一次只读一个，限时 25 秒，保留 5 秒，除非请求的是最新状态。

### hub 代管

`allow_hub_manage` 开启时，已连接的 hub 可以调用节点的管理方法：模型、名称与说明、显卡状态与设置、操作与作业。节点在
每次调用时检查该设置，因此关闭后立即生效。hub 不能修改节点设置，读不到模型的 Key，也和页面一样只能运行固定操作。

### 推送

节点信息（身份、说明、硬件、模型及其状态与负载、进行中的调用与作业）变化时，节点把它推送给所有已连接的 hub 与 proxy，
最多每 200ms 一次；对端连上时也单独推送一次。每次推送带一个从节点启动时间开始计数的序号，接收方据此丢弃旧信息，重启后也是如此。

## hub

### 节点表

node 连上时，hub 取得其信息，之后跟随它的推送。有未下线连接的节点为在线。节点表最多保留 `max_nodes` 个节点；超出时
忘掉最久未见的离线节点。节点表只在内存中：hub 重启后，只知道重新连上的节点。

### 模型

hub 按名称与接口列出在线节点的模型：每个模型由哪些节点提供，其中多少个就绪。停用的模型不列出。列表变化时 hub 把它推送
给所有已连接的 proxy，最多每 200ms 一次；proxy 连上时也单独推送一次。

### 节点列表

hub 让其 proxy 及时得知它的节点：proxy 连上时读取 `/hub/node/list`，列表变化时 hub 推送 `/hub/node/list/update`。每个节点
列出其 ID、名称、说明、是否在线与最后在线时间、不含磁盘与地址且至多 16 块显卡的硬件、模型及其状态与进行中的调用（至多
64 个，另有 `models_total`）、进行中的调用与作业、与 hub 的连接（方向、字节数、延迟），以及 hub 转发给它的调用
（`traffic`）。在线节点在前。列表就是 hub 的节点表，因此受 `max_nodes` 约束，编码后最多 4 MiB：超出时省略列表末尾的节点，
`total` 仍计入它们。变化在 200ms 后推送，且距上次推送不少于一秒；一直在变的流量与延迟，在有 proxy 连接时至多每 5 秒推送
一次。每份列表带一个序号，proxy 据此丢弃旧列表。

proxy 只展示列表。管理节点仍在节点自己的页面与其 hub 的页面上进行。

### 选节点

`/hub/proxy` 的请求与 `/node/proxy` 相同。hub 在提供该模型且就绪、未被 proxy 排除的在线节点中，选负载最小的：节点上报的
该模型调用数，加上 hub 当前转给它的调用数。并列时轮询。之后在各个流的额度内双向随到随转帧。

收到节点的头帧之前，不能提供该模型、离线或连接断开的节点被跳过，换下一个，最多 3 个节点；流式请求体只在尚未从 proxy
取走任何部分时重试。收到头帧之后，字节已到达 proxy，任何失败都结束调用。其他错误（如请求有误）立即结束调用。

### 代管调用

hub 的页面通过两个只对页面开放的方法代管节点：`/hub/node/call` 调用管理方法（限时 90 秒），`/hub/node/stream` 跟随
作业。允许什么由节点决定。

## proxy

### 接口

| 方法与路径 | 接口 | 转发为 |
| --- | --- | --- |
| `POST /v1/chat/completions`、`/v1/responses`、`/v1/completions`、`/v1/embeddings`、`/v1/moderations`、`/v1/images/generations`、`/v1/images/edits`、`/v1/images/variations`、`/v1/audio/speech`、`/v1/audio/transcriptions`、`/v1/audio/translations`、`/v1/rerank` | openai，对应 `openai` 与 `azure_openai` 模型 | `/v1/` 之后的部分 |
| `POST /v1/messages`、`/v1/messages/count_tokens` | anthropic | `/v1/` 之后的部分 |
| `POST /v1beta/models/{model}:{method}`、`/v1/models/{model}:{method}` | gemini | 方法名；模型取自路径 |
| `POST /api/chat`、`/api/generate`、`/api/embed` | ollama | `/` 之后的部分 |
| `POST /v2/chat`、`/v2/embed`、`/v2/rerank` | cohere | `/v2/` 之后的部分 |
| `GET /v1/models`、`GET /v1/models/{id}` | 所有模型 | 由 proxy 应答 |
| `GET /v1beta/models`、`GET /v1beta/models/{model}` | gemini 模型 | 由 proxy 应答 |
| `GET /api/tags` | ollama 模型 | 由 proxy 应答 |

这些路径正是各厂商 SDK 把 base URL 设为 proxy 后调用的地方，所以 agent 只需要 base URL 与 Key。接口是
`api.address` 上一个独立的绑定，与页面分开。其他路径或方法得到 404。模型列表同样需要 Key。`GET /v1/models` 列出
proxy 的 hub 与 node 所列的每个模型名称，同时带 OpenAI 与 Anthropic 模型列表的字段，两种 SDK 都能读取；
`GET /v1beta/models` 按 Gemini `models.list` 的格式列出 Gemini 模型，`GET /api/tags` 按 Ollama 的格式列出 Ollama 模型。

调用在 JSON 请求体的 `model` 中、在 `multipart/form-data` 请求体的 `model` 字段中（OpenAI 的上传，只有 OpenAI 的
路径接受），或在 Gemini 的路径中指明模型。应答带上游的状态码与内容类型、`Cache-Control: no-cache`、
`X-Accel-Buffering: no`，以及指明提供服务节点的 `X-Fit-Node`；应答体随帧到达写出。

### API Key

Key 为 `ah-` 加上 32 字节随机数的无填充 base64url，共 46 个字符。页面只在创建时显示一次；成员保存它的 ID、名称、前 10
个字符、SHA-256 与创建时间。agent 以 `Authorization: Bearer <key>`、`x-api-key: <key>` 或 `x-goog-api-key: <key>` 发送，在
Gemini 的路径上也可以像 Google 的 REST 示例那样以查询参数 `key` 发送；其 SHA-256 与每个已存哈希做常量时间比较。

Key 的使用情况（调用数与最近使用时间）与按模型的计数（调用数、失败数、字节数、进行中的调用）只在内存中统计，自角色启动起，
不写入任何地方。最多分别统计 1024 个模型；更多模型的调用合在一起，记为 `other`。

### 路由

proxy 在 `api.max_body_bytes` 之内完整读入请求体，取得其中的模型（Gemini 调用的模型在路径中）。然后按顺序尝试路由：

1. 直接连接、以讲该调用接口的种类提供该模型且就绪的节点，负载小的优先：节点上报的负载加上本 proxy 在其上进行中的
   调用数。
2. 列出该模型且有就绪节点的 hub，轮询。

在头帧之前因当前无法提供该模型、离线或连接断开而失败的路由，让给下一条，最多 4 条。尝试 hub 时排除已失败的节点。头帧
之后不再重试。

### 节点

proxy 的节点页面显示其连接的每个 hub 列表中的节点，以及它直连的节点，并带上它自己与每个节点的连接与调用：
`/proxy/node/list` 应答 `{hubs: [{hub, online, nodes, total, seq}], direct: [...]}`。它只供查看：页面上没有任何操作会改变
其他机器。

### 错误

错误采用 agent 所调用接口的格式：OpenAI 为 `{"error": {"message", "type", "code"}}`，Anthropic 为
`{"type": "error", "error": {"type", "message"}}`，Gemini 为 `{"error": {"code", "message", "status"}}`，Ollama 为
`{"error": "<消息>"}`，Cohere 为 `{"message"}`。不属于任何接口的路径按其前缀取格式（`/v1beta/` 为 Gemini，`/api/`
为 Ollama，`/v2/` 为 Cohere）；`/v1/` 上，发送 `anthropic-version` 或 `x-api-key` 的 agent 得到 Anthropic 格式。超过 `max_body_bytes` 的请求体是例外：HTTP 服务器在接口读取它之前，以纯文本的 413 拒绝。

| 情况 | 状态码 |
| --- | --- |
| 未知的路径或方法 | 404 |
| 缺少 Key 或 Key 错误 | 401 |
| 请求体不是 JSON，或没有 `model`；表单没有 `model` 字段，或路径不接受表单 | 400 |
| 请求体超过 `max_body_bytes` | 413 |
| 没有对端提供该模型，或只以另一种接口提供；模型种类不提供该调用 | 404 |
| 调用数超过 `max_inflight` | 429 |
| 模型没有就绪路由、所有路由都不可用，或 `first_byte_timeout` 内应答没有开始 | 503 |
| node 或 hub 应答请求有误（如请求体超过 node 的 `proxy.max_body_bytes`）、未找到或限流 | 400、404、429 |
| 应答开始之前超过 `timeout` | 504 |
| 头帧之前的其他失败 | 502 |

上游自己的错误在头帧到达后原样转发。头帧之后的失败使应答被截断。

### 接入

以下示例中，`127.0.0.1:9301` 是 proxy 的 `api.address`，`<key>` 是在其页面上创建的 API Key。局域网中其他机器上的
agent 则经 https 使用 proxy 的局域网地址。

OpenAI SDK（Python）：

```python
from openai import OpenAI
client = OpenAI(base_url="http://127.0.0.1:9301/v1", api_key="<key>")
client.chat.completions.create(model="qwen3-32b", messages=[{"role": "user", "content": "Hello"}])
```

Claude Code，使用以 Anthropic 接口提供的模型。`ANTHROPIC_BASE_URL` 不带 `/v1`：SDK 自己拼上 `/v1/messages`。

```sh
export ANTHROPIC_BASE_URL=http://127.0.0.1:9301
export ANTHROPIC_AUTH_TOKEN=<key>      # 或 ANTHROPIC_API_KEY=<key>，以 x-api-key 发送
export ANTHROPIC_MODEL=claude-sonnet
claude
```

Codex，`~/.codex/config.toml`，使用以 OpenAI 接口提供的模型：

```toml
model = "qwen3-32b"
model_provider = "fit_llm"

[model_providers.fit_llm]
name = "fit llm"
base_url = "http://127.0.0.1:9301/v1"
env_key = "FIT_LLM_API_KEY"
wire_api = "chat"   # 上游支持 /responses 时可改为 "responses"
```

然后 `export FIT_LLM_API_KEY=<key>` 并运行 `codex`。

Gemini SDK（Python，`google-genai`），使用以 Gemini 接口提供的模型。base URL 不带版本：SDK 自己拼上
`/v1beta/models/...`。

```python
from google import genai
from google.genai import types
client = genai.Client(api_key="<key>", http_options=types.HttpOptions(base_url="http://127.0.0.1:9301"))
client.models.generate_content(model="gemini-flash", contents="Hello")
```

Ollama 的 Python 库，使用以 Ollama 接口提供的模型：

```python
from ollama import Client
client = Client(host="http://127.0.0.1:9301", headers={"Authorization": "Bearer <key>"})
client.chat(model="llama3.2", messages=[{"role": "user", "content": "Hello"}])
```

Cohere SDK：`cohere.ClientV2(api_key="<key>", base_url="http://127.0.0.1:9301")`。

curl，一次对话与一次转写（音频文件以表单发送）：

```sh
curl http://127.0.0.1:9301/v1/chat/completions -H "Authorization: Bearer <key>" \
  -H "Content-Type: application/json" \
  -d '{"model": "qwen3-32b", "stream": true, "messages": [{"role": "user", "content": "Hello"}]}'

curl http://127.0.0.1:9301/v1/audio/transcriptions -H "Authorization: Bearer <key>" \
  -F model=whisper -F file=@meeting.wav
```

proxy 的概览页用它自己的地址与模型显示这些示例。

## 页面

角色的 `console` 成员提供其 Web 页面，作为监听引擎上一个独立的绑定：即[控制台指南](console.zh-CN.md#页面)中的 workbench，
菜单中只有该角色的模块。fit 同时运行 `admin.console` 时，角色的页面也显示在那里，作为 LLM 组下的 `llm.hub`、`llm.node` 或
`llm.proxy` 模块，用其登录，无需 `console` 成员。

| 字段 | 类型 | 默认 | 含义 |
| --- | --- | --- | --- |
| `address` | string | 必填 | 主机与端口。不是回环地址的主机需要 `cert_file` 与 `key_file`。 |
| `cert_file`、`key_file` | string | 无 | PEM 文件；两者都设置时页面经 TLS 提供。 |
| `users` | array | 必填 | 可以登录的用户：`username` 与 `password_hash`（`fit hash-password` 生成的 bcrypt 哈希）。至少一个。 |
| `session_timeout` | duration | `30m` | 登录在没有请求时持续多久。 |
| `session_lifetime` | duration | `12h` | 登录最长持续多久。 |
| `max_sessions` | int | 64 | 同时保留的登录数上限；再多一个结束最早的。 |
| `max_conns` | int | 32 | 页面同时保持的连接数上限；超出的连接在接受后立即关闭。 |

登录与 `admin.console` 相同，用的是同一份代码；用户、会话与锁定见[控制台指南](console.zh-CN.md)。登录在自己的池上
以 bcrypt 校验（运行 1 个，等待 16 个），API 的任务在控制台的池上（运行 4 个，等待 64 个），同时最多校验 2 个；
等待超过 30 秒的任务得到 504，超出队列的得到 503。同一地址（IPv6 按 /64）同时只检查一次，
15 分钟内登录失败 5 次后，该地址被拒绝 15 分钟（429，带 `Retry-After`）。会话是一个 cookie（`fit_llm_hub_<端口>`、
`fit_llm_node_<端口>` 或 `fit_llm_proxy_<端口>`，带上控制台的端口：浏览器把一台主机的 cookie 发给它的所有端口，
这样同一主机上的几个控制台登录互不影响；HttpOnly、SameSite=Strict，TLS 下带 Secure）。除 GET 与 HEAD 外，每个接口请求
都在 `X-Fit-Csrf` 中带上会话的 CSRF 令牌，`Origin` 是其他站点的请求以 403 拒绝。请求体最大 1 MiB，请求头最大 16 KiB。

每个角色的页面（英文或中文）显示：

- **hub**：概览；它的节点，允许 hub 代管的节点还有其模型、显卡、操作、作业与名称；节点的模型；连到它的 proxy；它的流量；
  它的连接；它的设置：名称、`max_nodes` 与 `limits`。
- **node**：概览，含名称与说明；它的模型，每个可以检查与测试调用；显卡及其设置；操作；作业；它的流量；它的连接；它的
  设置：名称与说明、`settings`、上游上限（`proxy`）与 `limits`。
- **proxy**：概览，含以它自己的地址与模型填好的[接入](#接入)示例；它能访问的模型及其路由；hub 背后的节点，仅供查看
  （[节点](#节点)）；它的流量；API Key；它的连接；它的设置：名称、接口（`api`：地址、证书与私钥文件、上限，或不提供）与
  `limits`。

连接页设置监听与对端，也可以踢掉一条连接。它显示监听是否启用 TLS 以及其证书的指纹（拨号方固定的就是它），以及每个对端的
监听如何校验：按指纹、按 CA 文件，或在回环地址上不校验。它的**连接令牌**卡片签发令牌（只显示一次，供复制），列出令牌及其
过期时间、最近使用、成员和连接，修改令牌的过期时间并吊销令牌；每条入站连接显示它所证明的令牌，或在它证明的是共享密码时
给出警告，编辑监听时可以移除全部密码。添加对端时填写其监听签发的令牌，令牌会替换密码。

流量页像 tunnel 的页面那样，把角色的连接画成分列的链路图：proxy 的 agent、proxy、它的 hub、hub 的节点与节点的模型；hub 的
proxy、hub、它的节点与节点的模型；node 的 hub 与 proxy、node、它的模型与模型的上游。每条线带双向字节数与速率（由间隔
三秒的两次 `/mesh/status` 计算），线宽随流量变化，空闲时为虚线，延迟按好（50ms 以下）、中（200ms 以下）、差着色；下方的
表格列出同样的线及其调用。点击方框或连线显示其详情。

设置卡片读取其设置，并按配置的规则写入：配置会拒绝的值，如低于 100ms 的 `ping_interval`、负数的上限、没有证书的非回环
接口地址，得到 400 与原因，不改变任何东西。

`/api/` 下的接口：

| 接口 | 应答 |
| --- | --- |
| `POST /api/login` | 用 `{username, password}` 开始会话；应答 `{user, csrf}`。 |
| `POST /api/logout` | 结束会话；应答 204。 |
| `GET /api/session` | 用户与 CSRF 令牌。 |
| `GET /api/me` | `{user, identity}`，即角色的身份；proxy 另加 `api_url`，即其接口的 URL。 |
| `POST /api/rpc<method>` | 执行一个方法，如 `/api/rpc/mesh/peers`，以 JSON 应答其结果，或以错误码对应的 HTTP 状态应答 `{"error", "code"}`；方法超时得到 504。 |
| `POST /api/stream<method>` | 打开一个流方法，以 SSE 应答：每帧一个数据事件，`end` 带结果，`error` 带 `{code, message}`。关闭请求即取消流。 |

页面以拥有全部权限的本地调用方身份，调用与对端相同的方法，以及只有页面能调用的方法：互联的（`/mesh/peers`、
`/mesh/status`、`/mesh/dial`、`/mesh/undial`、`/mesh/kick`、`/mesh/listen`、`/mesh/limits/get`、`/mesh/limits/set`，
以及令牌的 `/mesh/tokens/list`、`/mesh/tokens/create`、`/mesh/tokens/expiry`、`/mesh/tokens/revoke`）；
hub 的节点表（`/hub/nodes`）、代管调用（`/hub/node/call`、`/hub/node/stream`）、名称（`/hub/profile/set`）与设置
（`/hub/settings/get`、`/hub/settings/set`）；node 的设置（`/node/settings/get`、`/node/settings/set`）与上游上限
（`/node/upstream/get`、`/node/upstream/set`）；proxy 的模型、节点、计数、Key、名称与接口（`/proxy/models`、
`/proxy/node/list`、`/proxy/stats`、`/proxy/keys/list`、`/proxy/keys/create`、`/proxy/keys/delete`、`/proxy/profile/set`、
`/proxy/api/get`、`/proxy/api/set`）。

方法限时 10 秒，互联的方法也是；node 或 proxy 修改配置的方法限时 30 秒，模型检查 20 秒，node 的显卡状态与设置 30 秒，
hub 的代管调用 90 秒。方法在角色的池（16 个 goroutine，256 个等待）上运行，从不在 fib worker 上运行；超出队列的方法
得到 503。流的事件以不等待的方式写出，其帧只在浏览器取走后才归还给流，所以慢的浏览器只阻塞它自己的流；一个角色的页面上
最多同时打开 16 个流，第 17 个得到 429，30 秒内什么都没取走的浏览器使其流结束。

## 上限与规则

每个上限都有默认值；什么都不设置的配置也有界。

| 上限 | 默认 | 位置 |
| --- | --- | --- |
| 接受的互联连接 | 1024 | `limits.max_conns` |
| 每条连接上的调用与流 | 1024 | `limits.max_inflight` |
| 心跳周期、慢连接超时 | 15s、30s | `limits` |
| 拨号退避上限 | 5s | `limits.retry_interval` |
| 流空闲 | 2m | `limits.stream_idle` |
| 流窗口 | 每个方向 256 KiB | 固定 |
| 帧 | 16 MiB | 固定 |
| 每条连接等待的推送 | 1024，超出则关闭连接 | 固定 |
| 拨号、握手、hello | 10s、5s、15s | 固定 |
| 尚未认证的连接 | 256，每个 IP 地址 16 | 固定 |
| 方法时限 | 10s；node 或 proxy 修改配置 30s，模型检查 20s，显卡状态与设置 30s，hub 的代管调用 90s | 固定 |
| 对端、模型、API Key | 64、256、256 | 固定 |
| 连接令牌、每个令牌的成员 | 1,024、256 | 固定 |
| hub 节点表中的节点 | 1024 | `max_nodes` |
| hub 的节点列表 | `max_nodes` 个节点，每个 64 个模型、16 块显卡，4 MiB；200ms 后推送，间隔一秒，流量每 5s | 固定 |
| 统计的路由 | 每个角色 1024 条，其余记为 `other` | 固定 |
| node 同时转发的调用 | 256，另有每个模型的 `max_inflight` | `proxy.max_inflight` |
| node 请求体 | 32 MiB | `proxy.max_body_bytes` |
| node 连接、首字节、空闲、总时限 | 10s、5m、5m、30m | `proxy` |
| 每个上游主机的连接 | 64，另有 256 个等待 | 固定 |
| 上游应答体 | 1 GiB | 固定 |
| 模型检查 | 每次 15s；每 60s，`error` 时每 15s；每个主机 4 个连接，16 个等待 | 固定 |
| 作业 | 运行 8 个，保留 200 个，每个输出 1 MiB | 固定 |
| 作业运行时长 | 1h | `settings.job_timeout` |
| node 的硬件与显卡读取 | 运行 2 个，等待 16 个；显卡状态限时 25s，保留 5s | 固定 |
| proxy 接口请求体 | 32 MiB，完整读入 | `api.max_body_bytes` |
| proxy 接口连接、调用 | 256、256 | `api.max_conns`、`api.max_inflight` |
| proxy 首字节、总时限 | 5m、30m | `api` |
| proxy 接口请求头大小、读请求头、读取、空闲 | 64 KiB、10s、2m、2m | 固定 |
| proxy 等待慢 agent | 每次写入 60s | 固定 |
| 每次调用的路由：proxy、hub | 4、3 | 固定 |
| proxy 的计数 | 1024 个模型，其余记为 `other` | 固定 |
| 角色的池 | 运行 16 个，等待 256 个 | 固定 |
| 等待的配置编辑 | fit 所有角色共 64 个；每个最多等其方法的时限 | 固定 |
| 页面上的流 | 16 个；停滞的浏览器 30s | 固定 |
| 页面会话、连接 | 64、32 | `console` |
| 页面请求体、请求头 | 1 MiB、16 KiB | 固定 |
| 控制台的池 | 运行 4 个，等待 64 个，同时校验 2 个密码；每个任务 30s | 固定 |
| 登录失败 | 15 分钟内 5 次，之后锁定 15 分钟 | 固定 |
| 停止时的下线 | 10s | 固定 |

[开发规则](rules.zh-CN.md) 如何成立：

- **不阻塞 fib worker。** 页面与接口的请求在回调中应答；需要等待的工作在别处运行：bcrypt 在控制台的池上，方法在角色
  的池上，硬件与显卡查询在 node 的工作池上，命令在作业的池上，配置编辑在管理器的 goroutine 上。互联的请求与流运行在
  会话的 goroutine 上，每条连接的数量受 `limits.max_inflight` 限制。
- **请求体流式传输。** node 用 fib 的异步客户端转发调用，请求体与应答体随到随转，通过流的额度实现双向背压。只有 proxy
  接口为读取模型、并向下一条路由再次发送而完整读入请求体（在 `api.max_body_bytes` 之内），node 也只在把请求体指明的
  模型设为 `upstream` 时完整读入（在 `proxy.max_body_bytes` 之内）。
- **每个 map、队列与池都有上限**，见上表；在线节点与模型目录的条目受连接数约束。
- **每次对外交互都有超时**：拨号、握手、hello、方法、流、上游调用以及 agent 的连接。

fib 暂时还缺的两项能力写在 [fib API 需求](fib-api-requests.zh-CN.md) 中：

- **A52**，WebSocket 连接的底层传输：跳过关闭握手的关闭，以及拨号一侧的对端地址。在此之前，监听方通过包在 WebSocket
  处理器外的一层按对端地址找到连接的 `fib.Connection`，拨号方通过拨号钩子拿到自己的，从而能立即关闭死掉或过慢的连接。
- **A53**，WebSocket 连接发送的背压。在此之前，连接的队列由其各个流的窗口与慢连接超时约束：读得慢的对端在 ping 等待达到
  `slow_timeout` 时被发现，连接随之关闭。
