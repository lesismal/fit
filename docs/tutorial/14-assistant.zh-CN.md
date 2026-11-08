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
环境变量）与末四位；`api_key_env` 指定的环境变量在每次调用时读取，store 中的密钥优先。

助手只读取：它的工具在控制台的线程池上、经进程内的 Admin API 读取本 fit 的运行情况，每个结果都脱敏并截断。
它自己从不修改任何东西——提案工具只向提案加入步骤，由级别为 `apply` 的人在**提案**页应用。

每个控制台用户有一个**级别**：`off`（没有面板）、`read`（只读工具）、`edit`（另可在打开的页面上起草，从不
提交）、`propose`（另可提出提案）、`apply`（另可应用提案），每一级都包含之前的各级。

## 页面与操作

菜单中的页面按此顺序：**功能**、**技能**、**提案**、**审计日志**、**MCP**、**设置**；对话面板从每个页面的
顶栏打开。

### 功能

注册表中的每个工具，按组列出（配置、运行时、角色、媒体、集群、日志、页面、变更、转换、代理）：它所需的级别、
当前设置下本用户能否调用及原因、它做什么，以及参数的 JSON Schema。页首显示本用户的级别与正在使用的模型
（是否调用工具）。没有任何工具会写入配置、版本或 store：页面工具只在打开的页面上起草，提案工具只向提案加入
步骤。

### 技能

所有技能：18 个内置的与用户添加的。每个技能以工作台的语言显示标题与用途、它所列的工具（本用户不可调用的置灰）
以及是否启用。级别为 `apply` 的用户才能添加（从模板或导入 `.md` 文件）、编辑与删除已添加的技能，并启用或停用
任何技能；内置技能不能修改或删除，但可以导出、改名后作为自己的技能添加。所有用户都能查看，并在面板中用
`/name` 或**技能**按钮运行它。添加的技能最多 64 个，保存在 store 中；没有 store 时只保留到 fit 重启。

### 提案

待处理的提案：每个提案的**步骤**、它改动的成员、是否通过检查（在运行中的配置上），以及 `.conf`、`.yaml`、
`.json` 三种格式的**差异**。级别为 `apply` 的用户看到所有用户的提案，其他人只看到自己的。

**审阅并应用…**先打开含完整差异的对话框，只有其中的**应用**才会发送请求，带上提案的修订号与所见配置的 ETag：
生成一个新版本（来源为 console），保存到 store 并列入**版本**，然后执行缓存清除。期间配置已变时应用返回
`412`，提案在新版本上重新提供，需要重新审阅与确认。**丢弃**删除提案，配置不变。只有级别为 `apply` 的用户能
应用。

### 审计日志

助手为每个用户做过的事，最新的在前：每次回答调用的工具（参数已脱敏并截断）、每个提案的应用、拒绝、重新提供或
丢弃，以及在工作台页面上对媒体库文件的修改，连同对话、技能与结果。可按用户、类型与对话筛选，分页读取；在
`limits.max_audit_entries`（1000）的限度内保存在 store 中，没有 store 时只保留到 fit 重启。

### MCP

控制台地址上 `/mcp` 处的 MCP 服务器：是否开启、其设置（`admin.assistant.mcp`，与助手的设置一起保存）、如何
连接客户端（Claude Code 的命令与 JSON 配置），以及用户的**令牌**：名称、级别（最高 `propose`）、天数与可选的
来源地址。令牌只显示一次——fit 只保存其哈希；在同一页上吊销。级别为 `apply` 的用户可查看并吊销所有人的令牌。
外部代理调用面板所用的工具、以资源形式读取配置、schema、版本与参考，并把已启用的技能用作提示词；`ask_user`
与页面工具不对外提供。令牌的提案在**提案**页等待，只在那里应用。

### 设置

**提供方**：名称、类型、基础 URL、密钥（只写）、模型；每个提供方在**超时与重试**下有自己的
`connect_timeout`、`first_byte_timeout`、`idle_timeout`、`timeout` 与 `retries`。**测试**按编辑器中的提供方
调用（无论是否已保存）：先取模型列表，再对某个模型做一次简短调用，各自给出用时；**读取提供方的模型列表**会把
它列出的模型添加进来。下方是**模型与参数**（默认模型、最多输出 token、温度、思考）、**限制**、**发送给模型
的数据**（是否发送 fit 自身日志与访问日志的行、播放会话的主体，以及额外脱敏的正则），以及**用户**的级别。
**保存设置**把它们与输入的密钥一起保存到 store，此后取代配置文件中的 `admin.assistant`；没有 store 时无法在
此保存，密钥来自 `api_key_env`。

### 对话面板

每个页面的顶栏都有助手按钮（级别为 `off` 时没有），点开后面板显示在页面旁。消息连同**当前页面作为上下文**
（其模块、页面与标题、页面声明的选中项，以及页面上选中的文字）发往默认模型；回答以流的形式到来，Markdown 随
到随画，每次工具调用是一行可展开的记录（参数与结果），`ask_user` 的问题显示为一排按钮，所选即该调用的结果。
**停止**结束回答并保留已生成的部分；**重试**重新回答最后一条消息；面板头部可选择、新建或删除对话。待处理的
提案显示在输入框上方，带**应用**与**丢弃**。页面上有**问 AI** 按钮：它选好技能、填好输入，把所问的对象作为
选中项发送，并以用户的语言写好问题、立即发送。

## 参数

只列关键参数；完整字段表见[配置参考：Admin API](../reference/config.md#admin-api)（[中文说明](../reference/config.zh-CN.md)）。

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `providers` | 无 | 助手可调用的模型 API，每个按名称；密钥不写在这里 |
| `default_model` | 第一个提供方的第一个模型 | 对话用的提供方与模型 |
| `params` | 默认值 | `max_output_tokens`（4096）、`temperature`、`thinking`（`off`） |
| `limits` | 默认值 | 见下表 |
| `data` | 默认值 | `send_logs`、`send_access_log`、`send_subjects`（都默认关闭）与 `redact` |
| `users` | 无 | 每个控制台用户的级别 |
| `default_level` | `read` | 未列出用户的级别 |
| `mcp` | 关闭 | 控制台地址上 `/mcp` 处的 MCP 服务器 |

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
| `api_key_env` | 无 | 持有密钥的环境变量，每次调用时读取；store 中的密钥优先 |
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
- [集群](12-cluster.zh-CN.md)：管理端上的助手与其集群工具。
- [LLM 角色](25-llm.zh-CN.md)：`llm_proxy` 提供方。
- [决定 031](../decisions/031-assistant.zh-CN.md)。
- 前端代码：[app/workbench/web/ai/](../../app/workbench/web/ai/)。
