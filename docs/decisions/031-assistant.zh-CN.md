# 031：工作台中的助手

[English](031-assistant.md) | [简体中文](031-assistant.zh-CN.md)

状态：已采纳（2026-11-06）。2026-11-04 作为开发计划中的 [AI 助手一章](../dev-plan.zh-CN.md) 提出并评审；本文记录其中的选择，
以及 I124 如何实现了第一部分：设置、密钥与模型客户端。基于 [决定 027](027-console-and-store.zh-CN.md)（控制台与 store）
和 [决定 028](028-llm-and-tunnel.zh-CN.md)（llm.proxy）。

## 背景

workbench 编辑的配置中，各种概念（监听、路由、上游、角色）需要时间学习，各种问题（502、配置漂移、即将过期的证书）
要把几个页面一起读才能找到。页面旁的对话可以读取 fit 的运行情况、解释它并起草所要的修改，省去这些阅读。它不能成为绕过
配置所有者的途径，不能让密钥和密码离开本机，也不能给 fib 的 worker 带来新的负担。

## 决定

- **是模型 API 的客户端，而不是模型。** fit 调用用户设置的 API：带密钥的托管 API、本地服务器（Ollama、vLLM、llama.cpp），
  或本 fit 自己的 `llm.proxy`，让网格中的模型为助手服务。共八种：`anthropic`（Messages）、`openai`（Chat Completions）、
  `openai_responses`（Responses）、`gemini`（generateContent）、`azure_openai`（某个部署的 Chat Completions，带
  `api_version`）、`ollama`（`/api/chat`）、`openai_compatible`（任意 `base_url` 上的 Chat Completions）以及 `llm_proxy`。
- **模型起草，人来提交。** 没有任何工具会写配置、版本或 store；页面上的编辑是未保存的草稿，提案要等点击 Apply 并在对话框中
  再次确认。没有无人值守模式（由 I125、I126、I154 实现）。
- **默认关闭。** 没有 `admin.assistant` 时，控制台不提供 `ai` 模块，workbench 不显示助手，也不向任何地方发送任何内容。
  `admin.assistant` 需要 `admin.console`，并与整个 `admin` 一样在 fit 启动时生效。
- **设置先在配置中，然后在 store 中。** `admin.assistant` 包含除密钥外的全部设置：提供方、默认模型、参数、上限、可发送的数据、
  各用户的级别。有 `admin.store` 时，设置页把设置整体保存到 store，此后使用 store 中的设置，正如 store 存在后使用其中的配置：
  文件中的只是最初的设置。若 store 中的设置无法加载（如升级之后），则保留它们不动，使用文件中的设置，并在日志中说明。
  没有 store 时，设置页可以读取和测试，但不能保存，因为密钥无处可存。
- **密钥保存在 store 中，只写不读。** 在设置页输入的密钥进入 store，与 store 的其余内容一起加密，不会再返回：页面只能看到
  提供方是否有密钥、来自哪里（store 或 `api_key_env`），以及 16 位及以上密钥的末四位。密钥从不出现在响应、日志行或错误中：
  即使 API 把它回显，错误中的密钥也会被替换为 `***`，客户端的 `Secret` 类型打印时也是掩码。`api_key_env` 指定在调用时读取的
  环境变量，供没有 store 的 fit 使用。密钥只经 https 发送，或发往回环主机；提供方的 `headers` 不能设置密钥所用的请求头。
- **store 的分区，fitstore 版本 3。** store 在配置与控制台用户旁保存命名的分区；助手的分区为 `assistant`：`{"settings", "keys"}`。
  有分区的 store 写为版本 3；没有分区的仍为版本 2，使不支持分区的旧版 fit 仍能打开；版本 1 和 2 照常读取。分区不会导出：
  `fit store export` 与 Admin API 的导出只包含配置与用户，不包含密钥。
- **一个模型客户端，基于 fib 的异步 HTTP 客户端**（`assistant/model`）。调用只发起、从不等待：按类型由系统提示、消息与工具
  构造请求；回答（SSE 或 Ollama 的 NDJSON）边到达边解析为文本、思考与工具调用，以及 API 统计的 token。每次调用都有上限：
  连接、首字节、片段之间的停顿以及整个调用由提供方的超时限制（10s、2m、1m、10m）；一行或一个事件不超过 1 MiB，整个回答不超过
  `limits.max_stream_bytes`（8 MiB），工具调用不超过 64 个，失败回答的消息只读 8 KiB。只有在尚未取得任何回答时才重试
  （`retries`，2 次）：连接失败、在回答前连接被关闭，或返回 408、429、500、502、503、504、529，按 API 要求的 Retry-After
  等待，最多 10s。调用方取消时（如浏览器离开），调用的连接会被关闭，API 也就停止处理它。
- **思考与工具调用按各 API 的要求送回。** Anthropic 的思考块及其签名、Gemini 函数调用上的思考签名，以及各类型自己的工具结果
  形式（`tool` 消息、`tool_result` 块、`function_call_output` 项、`functionResponse`）都保存在消息中，使工具调用能够往返。
  模型写出的不是 JSON 的参数会作为字符串保存并加以标记，供代理循环作为错误交回。
- **`llm_proxy` 提供方**经回环地址调用 `llm.proxy.api`（无论它监听在哪个地址），使用代理的某个 API 密钥。经 TLS 时，客户端
  固定代理自己的证书（从其 `cert_file` 读取），而不是为自签名证书信任某个 CA。
- **工具注册表、技能、级别、对话与审计日志** 按开发计划该章的设定，在 I125–I130 与 I152–I154 中基于本文记录的设置实现：
  `limits` 限制对话、工具调用与结果，`data` 限制可发送的日志，`users` 与 `default_level`（off、read、edit、propose、apply）
  决定各控制台用户可做的事。

- **代理循环（I125）** 在 fit 中运行，而不在浏览器中：一次对话是一个请求 `POST /api/m/ai/chat`，以 SSE 应答；期间 fit
  调用模型、运行它要求的工具、再调用模型，直到得到回答、向用户提问或达到上限。每次调用都重新组装上下文：系统提示（角色、
  概念、规则、回答的语言）、浏览器所在的页面、技能目录（I127 之前为空），以及模型窗口内的历史：先截断早期的工具结果，再由
  模型把早期轮次总结为摘要，摘要随对话保存。用户的话按原样发送。
- **工具注册表**（`assistant.Registry`）：每个工具有名称、分组、JSON Schema、所需的最低级别、可选的低于
  `max_tool_result_bytes` 的上限，以及运行的位置。读取类工具在控制台的线程池（控制台自己的四个工作线程）上运行，以进程内调用
  Admin API 处理函数的方式读取（与 workbench 页面的调用方式相同），每次最多 30 秒；`ask_user` 以其问题结束本次流，所选随下一个
  请求作为该调用的结果返回。每个结果都会脱敏（密钥、哈希、令牌、私钥、各提供方的密钥、`data.redact`），不读取的成员被略去，
  截断到上限并注明，并作为数据加以标示。
- **fit 自身的日志保留一个环形缓冲**，存放最近 1000 条消息（每条最多 2 KiB），供 `log_tail` 使用；`access_log_sample` 读取访问
  日志文件的末尾，而不在热路径上为每个请求的日志行另存一份。
- **对话保存在 store 中**，每个用户一个分区（`assistant.chats.<哈希>`），由助手自己的一个保存协程写入，因此对话从不等待 store
  的磁盘；没有 store 时保存在内存中，直到 fit 重启。
- **提案（I126）** 随对话保存：提案工具（`config_propose`、`reconcile_propose`、`rollback_propose`、`cache_purge_propose`）
  只加入步骤，每个步骤都与其他步骤一起在运行中的配置上检查，并记录检查时的 ETag 与版本。配置步骤只能是一种，各经自己的
  端点作为一次变更应用：补丁合并为一次 `PUT /config/text?members=`，一致性修复为一次 `POST /config/reconcile`，回滚为一次
  `POST /rollback/{n}`；缓存清除在其后执行。提案不能触及 `admin`，也不能写入密钥（`[redacted]` 只能写回原位，并恢复为原值）。
  只有级别为 `apply` 的用户在点击“应用”并在显示完整差异的对话框中再次确认后才会应用，带 `If-Match` 与所确认的修订号；过期的
  提案被拒绝并在新版本上重新提供。每次工具调用与每次应用都按当时的设置检查用户级别。
- **审计日志（I126）** 记录每次调用了工具的回答，以及每次应用、拒绝、重新提供与丢弃：用户、时间、来源、对话、技能、调用（已
  脱敏并截断）、提案的步骤、结果与版本；最多 `limits.max_audit_entries` 条，保存在 store 的 `assistant.audit` 段中。

## 影响

- 设置了 `admin.assistant` 时，workbench 的菜单中有“助手”模块及其功能页与设置页，每个页面的顶栏都有对话面板的按钮。
- `GET /api/m/ai/settings`、`PUT /api/m/ai/settings`、`POST /api/m/ai/providers/test`、`POST /api/m/ai/chat`、
  `POST /api/m/ai/chat/stop`、`GET /api/m/ai/tools`、`GET /api/m/ai/conversations` 与 `GET`/`DELETE /api/m/ai/conversations/{id}`
  属于控制台，与其他模块的 API 一样需要登录与 CSRF 令牌；I126 加入 `GET /api/m/ai/proposals`、
  `GET /api/m/ai/proposals/{conversation}`、其 `POST .../apply` 与 `POST .../discard`，以及 `GET /api/m/ai/audit`，菜单中加入
  提案页与审计日志页。
- 一次对话在流式输出期间占用控制台的一个连接，工具在运行期间占用控制台线程池的工作线程；`max_concurrent_chats`（4）使两者
  都保持很少。
- 保存了助手设置的 fit 写出的 store 为版本 3，旧版 fit 无法打开。这是把密钥与配置一同加密保存的代价。
- 模型客户端在监听的引擎上打开自己的连接，每个主机最多 8 个，最多 16 种传输（按连接超时与固定的证书区分），满足控制台用户
  同时发起的少量调用。
