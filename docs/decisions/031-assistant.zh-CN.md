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

## 影响

- 设置了 `admin.assistant` 时，workbench 的菜单中有“助手”模块及其设置页；对话面板随 I125 提供。
- `GET /api/m/ai/settings`、`PUT /api/m/ai/settings` 与 `POST /api/m/ai/providers/test` 属于控制台，与其他模块的 API 一样
  需要登录与 CSRF 令牌。
- 保存了助手设置的 fit 写出的 store 为版本 3，旧版 fit 无法打开。这是把密钥与配置一同加密保存的代价。
- 模型客户端在监听的引擎上打开自己的连接，每个主机最多 8 个，最多 16 种传输（按连接超时与固定的证书区分），满足控制台用户
  同时发起的少量调用。
