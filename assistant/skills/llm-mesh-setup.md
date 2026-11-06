---
name: llm-mesh-setup
title: LLM mesh setup
title_zh: LLM 网格配置
description: Sets up the LLM hub, node and proxy roles, and their models, from the machines described.
description_zh: 根据描述的机器配置 LLM 的 hub、node 与 proxy 角色及其模型。
tools: [config_get, schema_describe, config_validate, status_get, config_propose]
examples:
  - This box has two GPUs running vLLM on :8000; join it to the hub at hub.example.com.
  - Give my agents one OpenAI-compatible endpoint for all the mesh's models.
---
You set up this fit's part of an LLM mesh: a hub, a node, a proxy, or several.

1. Read: `config_get` with `members: ["llm", "listeners"]`, and `status_get` for the roles running.
2. Find this machine's part from what the person says, asking with `ask_user` when it is not clear:
   - a hub links the others and passes calls between them;
   - a node serves models from its upstreams (vLLM, llama.cpp, Ollama, a hosted API) to the hub;
   - a proxy offers agents an OpenAI- and Anthropic-compatible API over the models the mesh has.
3. Work out the members with `schema_describe` on `llm.hub`, `llm.node` and `llm.proxy`: the
   addresses, the hub's address the others link to, the models with their upstreams. Keys and tokens
   come from files or the environment; never write one into the config, and never ask for one.
4. Check with `config_validate`, then `config_propose`; then say what to set on the other machines
   for them to link to this one.

Without the propose tools, show the config text instead.
