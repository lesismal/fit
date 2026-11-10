# 助手

[English](14-assistant.md) | [简体中文](14-assistant.zh-CN.md)

本章是教程的第 14 章，讲工作台的**助手**菜单：`admin.assistant` 开启的助手。它的设置页编辑
`admin.assistant`（保存后改由 store 保存），其余页面读取助手做过的事。深入说明在
[控制台与 store：助手](../console.zh-CN.md#助手)，完整字段表在
[配置参考：Admin API](../reference/config.md#admin-api)。

## 功能

`admin.assistant`（不设置时关闭）开启 workbench 的助手：此时控制台提供 `ai` 模块，菜单中显示**助手**。
它需要 `admin.console`，并与整个 `admin` 一样在 fit 启动时读取。

助手由**提供方**（`anthropic`、`openai`、`openai_responses`、`gemini`、`azure_openai`、`ollama`、
`openai_compatible`，以及 `llm_proxy`——本 fit 自己的 [LLM 角色](25-llm.zh-CN.md)）与一个默认模型组成，
每个提供方都有自己的超时与重试。它的**密钥**只写不读：页面只显示某个提供方是否有密钥、来自哪里（store 或
环境变量）与末四位；`api_key_env` 指定的环境变量在每次调用时读取，store 中的密钥优先。密钥只经 https 发送，
或发往回环地址的主机。

助手只读取：它的工具在控制台的线程池上、经进程内的 Admin API 读取本 fit 的运行情况，每个结果都脱敏并截断。
它自己从不修改任何东西——提案工具只向提案加入步骤，由级别为 `apply` 的人在**提案**页应用。

每个控制台用户有一个**级别**：`off`（没有面板）、`read`（只读工具）、`edit`（另可在打开的页面上起草，从不
提交）、`propose`（另可提出提案）、`apply`（另可应用提案），每一级都包含之前的各级。级别限制的是助手替用户
做什么，而不是用户本人：每个控制台用户都是管理员，可以在页面上修改配置，也可以保存设置页（包括各用户的级别）。

## 上手步骤

1. 在引导文件中加入带至少一个提供方的 `admin.assistant`（见 [.conf 配置](#conf-配置)），然后重启 fit；配置了
   `admin.store` 时，此后可在控制台上编辑这些设置。
2. 在**助手 → 设置**中编辑该提供方，输入密钥（或指定 `api_key_env` 环境变量），点**测试**，再点**读取提供方的
   模型列表**添加模型。
3. 选好默认模型，设置各用户的级别（或 `default_level`），点**保存设置**。
4. 在任一页面的顶栏打开面板提问；级别为 `propose` 时，到**提案**页审阅它提出的提案。

## 页面与操作

菜单中的页面按此顺序：**功能**、**技能**、**提案**、**审计日志**、**MCP**、**设置**；对话面板从每个页面的
顶栏打开。

### 功能

注册表中的每个工具，按组列出（配置、运行时、角色、媒体、数据同步、BitTorrent、集群、日志、页面、变更、
转换、代理）：它所需的级别、当前设置下本用户能否调用及原因、它做什么，以及参数的 JSON Schema。页首显示本
用户的级别与正在使用的模型（是否调用工具）。没有任何工具会写入配置、版本或 store：页面工具只在打开的页面上
起草，提案工具只向提案加入步骤。较新模块加入的两个组见下文[工具分组](#工具分组)。

### 技能

所有技能：22 个内置的与用户添加的。集群的三个（`fleet-health`、`rollout-planner` 与 `cluster-drift`）只在
管理端上提供，所以不管理其他 fit 的 fit 列出 19 个；最后一个是同步角色的 `sync-pipeline`。每个技能以工作台的
语言显示标题与用途、它所列的工具（本用户不可调用的会标出原因）以及是否启用。**查看**显示技能全文，**导出**
保存其 `.md`，**试用**运行它的某个示例，**在对话中使用**在面板中选中它。级别为
`apply` 的用户才能添加（从模板或导入 `.md` 文件）、编辑与删除已添加的技能，并启用或停用任何技能；内置技能不能
修改或删除，但可以导出、改名后作为自己的技能添加，再停用内置的那个。所有用户都能查看，并在面板中用 `/name` 或**技能**按钮运行
它。添加的技能最多 64 个，保存在 store 中；没有 store 时只保留到 fit 重启。

### 提案

待处理的提案：每个提案的**步骤**、它改动的成员、是否通过检查（在运行中的配置上），以及 `.conf`、`.yaml`、
`.json` 三种格式的**差异**。级别为 `apply` 的用户看到所有用户的提案，其他人只看到自己的。

**审阅并应用…**先打开含完整差异的对话框，只有其中的**应用**才会发送请求，带上提案的修订号与所见配置的 ETag：
生成一个新版本（来源为 console），保存到 store 并列入**版本**，然后执行缓存清除。期间配置已变时应用返回
`412`，提案在新版本上重新提供，需要重新审阅与确认。**丢弃**删除提案，配置不变。只有级别为 `apply` 的用户能
应用。

### 审计日志

助手为每个用户做过的事，最新的在前：每次回答调用的工具（参数已脱敏并截断）、每个提案的应用、拒绝、重新提供或
丢弃，在工作台页面上对媒体库文件的修改，以及每个 MCP 令牌发起的调用，连同对话、技能与结果。可按用户、类型与对话筛选，分页读取；在
`limits.max_audit_entries`（1000）的限度内保存在 store 中，没有 store 时只保留到 fit 重启。

### MCP

控制台地址上 `/mcp` 处的 MCP 服务器：是否开启、其设置（`admin.assistant.mcp`：**在 /mcp 提供 MCP**、允许的
地址——为空时仅限回环地址——以及每个令牌的限制，用**保存设置**与助手的设置一起保存，因此需要 store）、如何连接
客户端（Claude Code 的命令与 JSON 配置，使用控制台自己的地址），以及用户的**令牌**。**创建令牌**需填名称、级别
（`read`，级别为 `propose` 或 `apply` 的用户还可选 `propose`）、有效天数（最多 `max_token_days`，90）以及可选的
来源地址。令牌只显示一次——fit 只保存其哈希；在同一页上吊销；没有 store 时令牌只保留到 fit 重启。级别为 `apply` 的用户可查看并吊销所有人的令牌。
外部代理调用面板所用的工具、以资源形式读取配置、schema、版本与参考，并把已启用的技能用作提示词；`ask_user`
与页面工具不对外提供。令牌的提案在**提案**页等待，只在那里应用。

### 设置

**提供方**：名称、类型、基础 URL（留空为厂商的默认值）、API 版本（`azure_openai`）、密钥（只写，或**从环境
变量读取密钥**）、请求头（每行一个 `名称: 值`）、模型；每个提供方在**超时与重试**
下有自己的 `connect_timeout`、`first_byte_timeout`、`idle_timeout`、`timeout` 与 `retries`。**测试**按编辑器
中的提供方调用（无论是否已保存）：先取模型列表，再对某个模型做一次简短调用，各自给出用时；**读取提供方的模型
列表**会把它列出的模型添加进来。下方是**模型与参数**（默认模型、最多输出 token、温度、思考）、**限制**、
**发送给模型的数据**（是否发送 fit 自身日志与访问日志的行、播放会话的主体、同步试运行与死信中的记录，以及
额外脱敏的正则），以及**用户**的级别。**保存设置**把它们与输入的密钥一起保存到 store，此后取代配置文件中的
`admin.assistant`；没有 store 时无法在此保存，密钥来自 `api_key_env`。

### 对话面板

每个页面的顶栏都有助手按钮（级别为 `off` 时没有），点开后面板显示在页面旁。消息连同**当前页面作为上下文**
（其模块、页面与标题、页面声明的选中项，以及页面上选中的文字；取消勾选**将本页作为上下文发送**即可不带）发往
默认模型；回答以流的形式到来，Markdown 随
到随画，每次工具调用是一行可展开的记录（参数与结果），`ask_user` 的问题显示为一排按钮，所选即该调用的结果。
**停止**结束回答并保留已生成的部分；**重试**重新回答最后一条消息；面板头部可选择、新建或删除对话。待处理的
提案显示在输入框上方，带**应用**与**丢弃**。页面上有**问 AI** 按钮：它选好技能、填好输入，把所问的对象作为
选中项发送，并以用户的语言写好问题、立即发送。

## 工具分组

注册表的组按功能页的顺序为：配置、运行时、角色、媒体、数据同步、BitTorrent、集群、日志、页面、变更、转换、
代理。各组工具读什么见[控制台文档的助手一节](../console.zh-CN.md#助手)；较新模块加入的两个组是下面这些。

### 数据同步

同步角色的四个只读工具，都不修改管道：暂停、继续、新建快照、检查点与死信仍由人在[数据同步页面](26-sync.zh-CN.md)
上操作。

| 工具 | 级别 | 做什么 |
| --- | --- | --- |
| `sync_pipelines` | `read` | 各管道的状态、最近的错误、计数、吞吐、延迟与保留的死信；或单个管道的完整状态及其运行中的配置 |
| `sync_test` | `read` | 测试运行中管道的源与 sink 连接，不写入任何记录 |
| `sync_dry_run` | `read` | 读取运行中管道的前几条记录（5 条，最多 20 条）经过其各转换，从检查点或从头开始，不写入任何内容 |
| `sync_dead_letters` | `read` | 管道的死信（20 条，最多 100 条，可在某个 ID 之后）：错误、失败位置、尝试次数与记录 |

试运行与死信的记录是用户自己的数据：除非设置了 `admin.assistant.data.send_records`（设置页的**原样发送
同步试运行与死信中的记录（而不只是类型）**），每条记录键与行里的值只以类型（`(string)`、`(int)`、map 的
字段名）到达模型；命名空间、操作、位置与错误始终发送。在管理端上，`sync_pipelines` 与 `sync_dead_letters`
接受 `member`。技能 `sync-pipeline` 从一句话搭出一条管道，并找出管道停滞、延迟、停止或不断产生死信的原因；
管道的最近错误与死信带运行它的**问 AI** 按钮。

### BitTorrent

[BitTorrent 页面](27-bittorrent.zh-CN.md)的工具：四个只读工具与三个提案工具，它们经 Admin API 读取
`bittorrent.client` 与 `bittorrent.tracker` 角色，并像媒体工具那样向对话的提案加入步骤。

| 工具 | 级别 | 做什么 |
| --- | --- | --- |
| `bt_torrents` | `read` | 种子的简况（状态、被保持的原因、错误、进度、大小、速率、对端、分享率、剩余时间），按状态、分类与名称筛选（50 个，最多 200 个），以及备选速率 |
| `bt_torrent` | `read` | 单个种子的状态、前 100 个文件及优先级、前 50 个对端，以及它的 tracker 与 Web 种子 |
| `bt_tracker_stats` | `read` | tracker 的总计与按原因统计的拒绝，以及前 50 个 swarm（白名单中的，或按 info hash 查的公共 tracker） |
| `bt_rss` | `read` | RSS 自动下载的订阅源（上次轮询、结果、计数）与规则，以及最近见过的条目；订阅源 URL 只给 scheme、host 与最后一段路径 |
| `bt_add_propose` | `propose` | 按磁力链接或 `.torrent` URL 添加种子，可指定分类、暂停或顺序下载 |
| `bt_pause_propose` | `propose` | 暂停种子，按 ID（最多 100 个） |
| `bt_resume_propose` | `propose` | 恢复种子，按 ID（最多 100 个） |

没有任何 passkey 到达模型：种子的磁力链接被略去，tracker 的 URL 只保留 scheme、host 与最后一段路径
（`https://tracker.example/[redacted]/announce`），Web 种子去掉用户名与查询串，对端去掉 ID；没有工具读取
用户的 passkey——那由 Tracker 页展示给人看。BitTorrent 的步骤与提案的媒体步骤合在一起，随提案在任何配置
变更之后按顺序经 `/bittorrent/torrents` 以应用提案的人的身份执行；其中提到一个已被删除的种子时，提案不再
通过检查。在管理端上，只读工具接受 `member`，`cluster_bittorrent` 读取集群自己的视图
（[第 12 章](12-cluster.zh-CN.md#bittorrent)）。BitTorrent 页面没有**问 AI** 按钮：这些工具从面板中使用。

## 参数

只列关键参数；完整字段表见[配置参考：Admin API](../reference/config.md#admin-api)（[中文说明](../reference/config.zh-CN.md)）。

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `providers` | 无 | 助手可调用的模型 API，每个按名称；密钥不写在这里 |
| `default_model` | 第一个列有模型的提供方的第一个模型 | 对话用的提供方与模型 |
| `params` | 默认值 | `max_output_tokens`（4096）、`temperature`、`thinking`（`off`） |
| `limits` | 默认值 | 见下表 |
| `data` | 默认值 | `send_logs`、`send_access_log`、`send_subjects` 与 `send_records`（都默认关闭）以及 `redact` |
| `users` | 无 | 每个控制台用户的级别 |
| `default_level` | `read` | 未列出用户的级别 |
| `mcp` | 关闭 | 控制台地址上 `/mcp` 处的 MCP 服务器，设置后（即使为空）即开启：`allow`（回环地址）、`max_tokens_per_user`（10）、`max_token_days`（90）、`max_calls_per_minute`（120）、`max_concurrent_calls`（4）、`max_request_bytes`（1 MiB）、`max_sessions`（4）、`session_idle_timeout`（`1h`） |

### `limits`

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `max_tool_calls` | 16 | 一次回答可调用的工具数 |
| `max_turns` | 40 | 一个对话保留的轮数 |
| `max_concurrent_chats` | 4 | 同时生成的回答数（跨用户） |
| `max_tokens_per_day` | 1000000 | 每个用户每天的 token 数 |
| `max_tool_result_bytes` | 32768（32 KiB） | 交给模型的每个工具结果的字节数 |
| `max_stream_bytes` | 8388608（8 MiB） | 每次模型回答的字节数 |
| `max_conversations` | 50 | 每个用户保留的对话数 |
| `max_audit_entries` | 1000 | 保留的审计条目数 |

### 每个提供方

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `name` | 必填 | 名称，`default_model` 与设置页按它称呼 |
| `kind` | 必填 | `anthropic`、`openai`、`openai_responses`、`gemini`、`azure_openai`、`ollama`、`openai_compatible` 或 `llm_proxy` |
| `base_url` | 厂商的默认值 | `openai_compatible` 与 `azure_openai` 必填；`llm_proxy` 没有它 |
| `api_version` | 无 | `azure_openai` 提供方调用的 api-version，如 `2024-10-21`；只有它需要 |
| `api_key_env` | 无 | 持有密钥的环境变量，每次调用时读取；store 中的密钥优先 |
| `headers` | 无 | 每次调用都发送的请求头（最多 16 个），如组织标识；不能是密钥的请求头、`Host` 或正文的分帧字段 |
| `connect_timeout` | `10s` | 连接（含 TLS）最长耗时 |
| `first_byte_timeout` | `2m` | 等待回答开始最长多久 |
| `idle_timeout` | `1m` | 流式回答最多可以沉默多久 |
| `timeout` | `10m` | 一次调用到回答结束最长多久 |
| `retries` | 2 | 未取得任何回答前重试几次（0 到 5） |
| `models` | 无 | 该提供方提供的模型；`context` 与是否调用工具可选 |

## .conf 配置

引导文件（`admin` 只在 fit 启动时生效，控制台的页面从不编辑它；密钥不在配置里，由设置页保存到 store 或来自
`api_key_env`）：

```fit
# bootstrap file: admin is read when fit starts; the console's pages never edit it
admin {
	console {
		address 127.0.0.1:2020
		users {
			username ada
			password_hash "$2a$10$sKGBAOOCI4p4WWJcVCuAH.ivT8pNy2DFbs6pd8xmtNEZ/5L1SeLvm"
		}
	}
	assistant {
		providers {
			name claude
			kind anthropic
			api_key_env ANTHROPIC_API_KEY
			models claude-sonnet-5-5 {
				context 200000
			}
		}
		providers {
			name local
			kind ollama
			base_url http://127.0.0.1:11434
			models qwen3:8b
		}
		default_model {
			provider claude
			model claude-sonnet-5-5
		}
		limits {
			max_tool_calls 16
			max_turns 40
		}
		users {
			username ada
			level propose
		}
		mcp {
			allow 127.0.0.1
			max_calls_per_minute 120
		}
	}
}
```

## 相关文档

- [控制台与 store：助手](../console.zh-CN.md#助手)：提供方与密钥、对话面板、提案、级别、技能、页面上的
  “问 AI”，以及[MCP 服务器](../console.zh-CN.md#mcp-服务器)。
- [配置参考：Admin API](../reference/config.md#admin-api)：`admin.assistant` 的全部字段。
- [登录](02-login.zh-CN.md)与[账户](13-account.zh-CN.md)：助手用的正是控制台的登录与用户。
- [集群](12-cluster.zh-CN.md)：管理端上的助手与其集群工具（含 `cluster_bittorrent`）。
- [数据同步](26-sync.zh-CN.md)与 [BitTorrent](27-bittorrent.zh-CN.md)：较新工具组读取的模块；
  功能本身见 [sync](../sync.md) 与 [BitTorrent](../bittorrent.md)。
- [LLM 角色](25-llm.zh-CN.md)：`llm_proxy` 提供方。
- [决定 031](../decisions/031-assistant.zh-CN.md)。
- 前端代码：[app/workbench/web/ai/](../../app/workbench/web/ai/)。
