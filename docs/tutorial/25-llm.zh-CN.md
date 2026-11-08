# LLM 角色

[English](25-llm.md) | [简体中文](25-llm.zh-CN.md)

本章是教程的第 25 章，讲配置成员 `llm` 的三个角色在工作台中的页面：`llm.hub`、`llm.node` 与
`llm.proxy`。字段的完整表在[配置参考](../reference/config.md#llm)，功能的深入说明在
[LLM 功能](../llm.zh-CN.md)。

## 功能

`llm` 开启 fit 的三个 LLM 角色，每个角色由它自己的成员开启，任意组合，可以与网关的监听和路由并存：

- **`llm.node`** 提供模型。每个模型是某个主流接口的一个上游端点（`openai`、`anthropic`、`gemini`、
  `azure_openai`、`ollama`、`cohere`），可以是本机的服务（Ollama、vLLM、llama.cpp），也可以是带 Key 的
  托管接口；node 还上报本机硬件，并执行一组固定的操作。
- **`llm.hub`** 是 node 与 proxy 汇合之处：它维护节点表与模型目录，把每次调用转给提供该模型、负载最小的节点。
- **`llm.proxy`** 为 agent 提供这些接口（`/v1/chat/completions`、`/v1/messages`、
  `/v1beta/models/{model}:generateContent`、`/api/chat`、`/v2/chat`），打通本机或局域网——公网 hub——其他
  机器上的 node 这条链路。

角色之间通过 WebSocket 连接组成的互联（mesh）通信：每个角色 `listen` 接受连接、`peers` 拨号连接对端；回环
之外的连接走 TLS，拨号方按 `pin` 或 `ca_file` 校验对端的证书。连接凭据是监听方签发的**令牌**：角色的
`tokens` 只保存哈希，令牌本身只在签发时显示一次（[决定 035](../decisions/035-link-tokens.zh-CN.md)）。

角色运行后，它的模块出现在左侧菜单（见[功能导航栏](03-navigation.zh-CN.md)）：`llm.hub`、`llm.node` 或
`llm.proxy`。角色也可以有自己的 `console`；fit 同时运行 `admin.console` 时，角色的页面显示在 LLM 组下，用
控制台的登录，无需 `console` 成员。页面上的修改写回该角色的成员，经与配置相同的流程应用。

## 页面与操作

### llm.hub

#### 概览

四个统计：在线节点（`n / 共 N 个`）、代理、模型（`N 个就绪`）、在途；下面是模型表：名称、接口、就绪数
（`就绪 / 提供它的节点数`）与提供它的节点（链接到节点页）。每 5 秒重新读取。

#### 节点

每个节点一行：名称与 ID、主机、状态（在线，或离线与最后在线时间）、模型徽标、在途、显卡（`2× RTX 4090`
这样的汇总）、以及是否**可代管**。点击一行打开该节点的页面：详情（系统、模型、显卡、磁盘与连接），以及节点
允许 hub 代管时的**模型管理**、**显卡**、**操作**、**作业**与**资料**标签页。不允许时页面说明原因；节点离线
时代管不可用。hub 读不到模型的 Key，也只能运行固定操作。

#### 模型

hub 按名称与接口列出在线节点的模型：每个模型由哪些节点提供、其中多少个就绪。停用的模型不列出。

#### 代理

连到它的 proxy：名称与 ID、主机、版本、方向（入站或出站）、远端地址、连接时间，以及是否在排空。

#### 流量

把互联画成分列的链路图：代理 → 本 hub → 节点 → 模型的上游。每条线带双向字节数与速率（由间隔三秒的两次
`/mesh/status` 计算）、延迟（好、中、差着色），线宽随流量变化，空闲时为虚线；下方的表格列出同样的线及其
调用。点击方框或连线显示其详情。

#### 设置

显示名称（对端在下次连接时看到新名称）、ID、版本与主机；**节点表**卡片写 `max_nodes`；**连接上限**卡片写
`limits`。

### llm.node

#### 概览

四个统计：显卡、就绪模型、进行中的调用、运行中作业；然后是**资料**（名称、描述、ID、版本、Web 控制台、RPC
地址、启动时间、hub 代管是否允许）、**系统**（主机名、操作系统、内核、架构、CPU、负载、运行时长、IP）、
**内存与磁盘**的条形，以及每块显卡的卡片（利用率与显存）。每 10 秒重新读取。

#### 模型

每个模型一行：名称、接口、Base URL 与 `upstream`、API Key（打码，只看到已保存）、状态（就绪、已停用、错误
及其原因与检查时间）、在途（`n / 上限`）。操作：**检查**（对上游模型列表的 GET，限时 15 秒）、**启用**或
**停用**、**编辑**与**删除**。Key 留空表示保留当前的。页面下方是**测试调用**：选一个就绪的模型，写提示词，
经 `/node/proxy` 发起一次流式调用，与 agent 的调用相同；原始应答在可展开的**原始应答**中，**停止**结束它。

#### 显卡

每块显卡一张卡片：利用率、显存、温度、功耗、核心与显存频率、风扇，以及该厂商的设置项（按类型：数值、
`min,max` 范围、枚举或动作），可只作用于这块卡或**该厂商的所有显卡**。上方列出厂商与管理软件：是否检测到、
是否已安装、**安装管理软件**（以作业执行，可能需要 root），以及查询命令与安装脚本。页面说明执行身份
（root、`sudo -n` 或当前用户）。

#### 操作

节点只执行代码中定义的固定操作（安装 Ollama；列出、拉取、查看与删除 Ollama 模型；Ollama 服务的状态、重启
与日志；`nvidia-smi`；安装 NVIDIA Container Toolkit），每个操作只在适用的系统上提供。参数逐项校验，从不
经过 shell。**执行**把操作作为作业运行，随后跟随其输出。

#### 作业

作业列表：类型、标题、状态、创建与结束时间、退出码；点击一行跟随其输出（随到随写），或**取消作业**。最多
同时运行 8 个，保留 200 个，每个保留最后 1 MiB 输出。

#### 流量

链路图：调用它的 hub 与代理 → 本节点 → 模型 → 模型的上游。与 hub 的流量页相同。

#### 设置

**资料**（名称与描述）；**节点设置**（允许 hub 代管本节点、`gpu_sudo`、硬件刷新间隔、作业超时、显卡驱动包
目录）；**上游调用**（`proxy`：连接、首字节、空闲与总超时，并发调用上限，请求体上限）；**连接上限**
（`limits`）。进行中的调用保持开始时的上限。

### llm.proxy

#### 概览

**Agent 接口**卡片给出三个 base URL（OpenAI、Anthropic、其他接口），可复制；统计：模型与就绪数、API Key、
调用与失败、进行中与已转发字节；**接入示例**按它自己的地址与模型填好（OpenAI SDK、Claude Code、Codex、
curl、Gemini SDK、Ollama）；**各模型调用**列出每个模型的调用、失败、进行中、字节与最近调用。

#### 模型

它能访问的模型：名称、接口、状态（就绪或未就绪）与**路由**徽标——优先的直连节点（`直连 · 名称`）与 hub
（`hub · 名称`，悬停显示其就绪节点数）。调用优先选择已就绪该模型的直连节点（在途最少），其次是 hub（轮询）。

#### 节点

每个 hub 一节，列出 hub 上报的节点，以及本 proxy 直连的节点：状态、硬件、模型、负载与连接。**仅供查看**：
每台机器在其自身页面上管理。点击一行显示其详情。

#### 流量

链路图：agent → 本 proxy → 其 hub 与直连节点 → hub 的节点 → 模型。从本 proxy 出发的线是它自己的连接与
调用；hub 到节点的线来自 hub 的上报。

#### API Key

列出每个 Key：名称、前缀（`ah-…`）、创建时间、最近使用与调用数。**新建 API Key**只输入名称；Key 只在创建
时显示一次，成员保存的是它的 SHA-256。**删除**会使使用它的 agent 无法调用。使用情况最多每分钟保存一次。

#### 设置

**基本信息**（显示名称）；**Agent 接口**（地址、证书与私钥文件、请求体上限、连接与并发上限、首字节与总
超时，或**停止接口**）；**连接上限**。接口与本控制台分开，是监听引擎上一个独立的绑定。

### 连接（三个角色的同一页面）

三个角色的菜单里都有**连接**页，它设置监听与对端：

- **本进程**：角色、名称、ID、主机、版本、PID、启动时间、RPC 地址与 Web 控制台。
- **RPC 监听**：状态、地址、是否有密码、TLS（自签名、证书文件或明文仅限回环）与**指纹（SHA-256）**——拨号
  方固定的就是它。**修改监听**写入地址、密码、自签名与证书文件，并可**移除全部密码：只接受令牌连接**。
- **连接**：每条连接的角色、名称、方向、远端地址、连接时间，以及它证明的凭据（令牌 ID，或共享密码——仍有
  这样的连接时给出警告）；**踢下线**关闭它，出站连接会自动重连。这是页面上唯一不写入配置的修改。
- **主动连接目标**：`peers` 中的每个地址及其角色、状态与对端；**连接到…**填写对端监听签发的令牌（它替换
  密码）、对端必须声明的角色、`pin` 或 `ca_file` 与 `server_name`。
- **连接令牌**：签发令牌（只显示一次）、列出令牌及其过期时间、最近使用、绑定成员与连接，修改过期时间与
  吊销。由页面、Admin API 或 `fit token` 生成，不要手写。

## 参数

只列关键参数；完整字段表见[配置参考：LLM](../reference/config.md#llm)。

### 每个角色都有的字段

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `id` | 首次启动时生成 | 角色的稳定 ID，对端以它识别角色；省略时生成并写回成员 |
| `name` | 主机名 | 角色向对端显示的名称，页面上的**显示名称** |
| `console` | 无 | 角色自己的页面；`admin.console` 运行时不需要它 |
| `listen` | 无 | 接受对端连接的地方；回环之外的地址需要 `cert_file` 与 `key_file`，或 `self_signed` |
| `access` | 由全局 `access` 决定 | `listen` 与 proxy 的 `api` 接受哪些客户端 |
| `tokens` | 无 | 监听签发的连接令牌，最多 1,024 个；只保存哈希 |
| `peers` | 无 | 角色拨号并保持连接的对端，最多 64 个；`token` 与 `password` 二选一 |
| `limits` | 默认值 | 连接的上限，见下表 |

### llm.hub

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `max_nodes` | 1024 | 节点表保留的节点数（含离线）；超出时遗忘最久未见的离线节点 |

### llm.node

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `description` | 无 | 对端显示的节点说明 |
| `models` | 无 | 节点提供的模型，最多 256 个；每个 `name`、`api`、`base_url` 必填 |
| `settings` | 默认值 | `allow_hub_manage`（默认关闭）、`gpu_sudo`、`refresh_interval`（`30s`）、`job_timeout`（`1h`）、`gpu_data_dir` |
| `proxy` | 默认值 | 转发给上游的调用的上限，见下表 |

### llm.proxy

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `api` | 无 | agent 调用的接口；没有它时 proxy 仍连接对端，但不提供接口 |
| `api_keys` | 无 | agent 用的 Key，最多 256 个；只保存其 SHA-256，由页面创建与删除 |

### 连接上限（`limits`，三个角色相同）

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `max_conns` | 1024 | 同时接受的对端连接数 |
| `max_inflight` | 1024 | 一个对端在一条连接上同时运行的调用与流 |
| `ping_interval` | `15s` | 多久没收到任何东西就发 ping（至少 100ms） |
| `slow_timeout` | `30s` | ping 等待 pong 多久后连接被视为过慢 |
| `retry_interval` | `5s` | 连不上的对端两次拨号之间的最长等待 |
| `stream_idle` | `2m` | 流两个方向都没有数据多久后结束 |

### node 的上游调用（`proxy`）

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `connect_timeout` | `10s` | 连接上游最多用多久 |
| `first_byte_timeout` | `5m` | 从调用开始到上游开始应答最多多久 |
| `idle_timeout` | `5m` | 上游在应答中途最多可以沉默多久 |
| `timeout` | `30m` | 一次调用到应答结束最多多久 |
| `max_inflight` | 256 | 所有模型上同时转发的调用数；超出的调用转到其他节点 |
| `max_body_bytes` | 33554432（32 MiB） | 一次调用的请求体上限 |

### proxy 的接口（`api`）

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `address` | 必填 | 回环地址服务本机的 agent；局域网地址需要 `cert_file` 与 `key_file` |
| `max_body_bytes` | 33554432（32 MiB） | 请求体上限；超出的得到 413 |
| `max_conns` | 256 | 接口同时保持的连接数 |
| `max_inflight` | 256 | 接口同时转发的调用数；超出的得到 429 |
| `first_byte_timeout` | `5m` | 调用等待应答开始最多多久 |
| `timeout` | `30m` | 一次调用到应答结束最多多久 |

## .conf 配置

一个 hub 与一个 node（hub 的 `tokens` 与 node 的 `token` 由页面、Admin API 或 `fit token` 生成，这里只是
示例；`password_hash` 用 `fit hash-password` 生成）：

```fit
llm {
	hub {
		name hub-1
		console {
			address 127.0.0.1:9200
			users {
				username admin
				password_hash "$2a$10$sKGBAOOCI4p4WWJcVCuAH.ivT8pNy2DFbs6pd8xmtNEZ/5L1SeLvm"
			}
		}
		listen {
			address :9210
			self_signed true
		}
		tokens gpu-1 {
			id fdd44d906b8e6ce2
			hash 1c994a095de7b566dcce0f229b829af96d85911b6d3e786f217c77340bc36aa4
			created_at 1793900000
			members node-8c1f6e2a90b34d57
		}
		limits {
			max_conns 1024
			max_inflight 1024
			ping_interval 15s
			slow_timeout 30s
			retry_interval 5s
			stream_idle 2m
		}
		max_nodes 1024
	}
	node {
		name gpu-1
		description "Two RTX 4090s"
		peers {
			address hub.example.net:9210
			token fitt1.fdd44d906b8e6ce2.Hm2vJvGyTEJVh2_zzu0ZJlENz1A3IYXS2JydjHyJShQ
			role hub
			pin 134daba9eaf7f4b2f15a8ca3788273bd52d0b7f6f34581a4561458814d95b0c9
		}
		models qwen3-32b {
			api openai
			base_url http://127.0.0.1:8000/v1
			upstream Qwen/Qwen3-32B
			max_inflight 4
		}
		models llama3.2 {
			api ollama
			base_url http://127.0.0.1:11434
			upstream llama3.2:3b
		}
		settings {
			allow_hub_manage true
			refresh_interval 30s
			job_timeout 1h
		}
		proxy {
			connect_timeout 10s
			first_byte_timeout 5m
			idle_timeout 5m
			timeout 30m
			max_inflight 256
			max_body_bytes 33554432
		}
	}
}
```

只有角色、没有路由的配置不需要监听。页面对这些成员的修改经 `admin.Configs` 作为一次配置编辑生效，有
store 时加密写入 store。

## 相关文档

- [LLM 功能](../llm.zh-CN.md)：模型与接口、互联与令牌、node、hub 与 proxy 的行为、上限与规则。
- [配置参考：LLM](../reference/config.md#llm)：全部字段（[中文说明](../reference/config.zh-CN.md)）。
- [控制台与 store](../console.zh-CN.md#页面)：登录、会话与页面的通用规则。
- [隧道角色](24-tunnel.zh-CN.md)：同样的三个角色，流量页的链路图与之相同。
- [助手](14-assistant.zh-CN.md)：`llm.proxy` 作为助手的 `llm_proxy` 提供方。
- 前端代码：[app/workbench/web/llm/](../../app/workbench/web/llm/)。
- [决定 028](../decisions/028-llm-and-tunnel.zh-CN.md) 与[决定 035](../decisions/035-link-tokens.zh-CN.md)。
