# 031: An assistant in the workbench

[English](031-assistant.md) | [简体中文](031-assistant.zh-CN.md)

Status: accepted (2026-11-06). Proposed and reviewed 2026-11-04 as the
[AI assistant chapter](../dev-plan.md#ai-assistant-in-the-workbench) of the dev plan; this records
the choices and how I124 built the first of them: the settings, the keys and the model client.
Builds on [decision 027](027-console-and-store.md) (the console and the store) and
[decision 028](028-llm-and-tunnel.md) (llm.proxy).

## Context

The workbench edits a config whose concepts (listeners, routes, upstreams, roles) take a while to
learn, and whose problems (a 502, a drift, a certificate about to expire) are found by reading
several pages together. A chat beside the pages that reads what fit runs, explains it and drafts
the change asked for saves that reading. It must not become a way around the person who owns the
config, nor a way for keys and passwords to leave the machine, nor a new kind of load on fib's
workers.

## Decisions

- **A client of model APIs, never a model.** fit calls the API a user sets: a hosted one with its
  key, a local server (Ollama, vLLM, llama.cpp), or this fit's own `llm.proxy`, so that the mesh's
  models serve the assistant. Eight kinds: `anthropic` (Messages), `openai` (Chat Completions),
  `openai_responses` (Responses), `gemini` (generateContent), `azure_openai` (a deployment's Chat
  Completions with `api_version`), `ollama` (`/api/chat`), `openai_compatible` (Chat Completions at
  any `base_url`) and `llm_proxy`.
- **The model drafts; a person submits.** No tool writes the config, a version or the store; a page
  edit is an unsaved draft, a proposal waits for Apply and a second confirm. There is no unattended
  mode (I125, I126, I154 build this).
- **Off by default.** Without `admin.assistant` the console serves no module `ai`, the workbench
  shows no assistant, and nothing is sent anywhere. `admin.assistant` needs `admin.console`, and,
  as all of `admin`, takes effect when fit starts.
- **Settings in the config first, then in the store.** `admin.assistant` holds every setting but
  the keys: providers, the default model, params, limits, what data may be sent, users' levels.
  With `admin.store`, the Settings page saves the settings whole into the store, and the store's are
  used from then on, as the store's config is once it exists: the file's are the first ones. A
  store whose settings no longer load (after an upgrade) leaves them there, uses the file's, and
  says so in the log. Without a store the page reads and tests but does not save, since a key would
  have nowhere to go.
- **Keys in the store, written once.** A key typed on the Settings page goes to the store, sealed
  with the rest of it, and never comes back: the page sees whether a provider has one, where from
  (the store or `api_key_env`), and its last four characters for a key of 16 or more. A key is
  never in a response, a log line or an error: errors have it replaced by `***`, even when an API
  says it back, and the client's `Secret` type prints masked. `api_key_env` names a variable read
  when a call is made, for a fit without a store. A key is sent only over https or to a loopback
  host; a provider's `headers` cannot set the key's headers.
- **Store sections, fitstore version 3.** The store keeps named sections beside the config and the
  console users; the assistant's is `assistant`: `{"settings", "keys"}`. A store with a section is
  written as version 3; one without stays version 2, so that a fit that predates sections still
  opens it, and versions 1 and 2 are read as before. Sections are not exported: `fit store export`
  and the Admin API's export carry the config and the users, not the keys.
- **One model client, on fib's asynchronous HTTP client** (`assistant/model`). A call is started,
  never waited for: the request is built per kind from a system prompt, messages and tools; the
  answer, SSE or Ollama's NDJSON, is parsed as it arrives into text, thinking and tool calls, and
  the tokens the API counts. Every call is bounded: the connect, the first byte, the silence between
  pieces and the whole call by the provider's timeouts (10s, 2m, 1m, 10m); a line or an event by
  1 MiB, the answer by `limits.max_stream_bytes` (8 MiB), the tool calls by 64, a failed answer's
  message by 8 KiB. A call is tried again (`retries`, 2) only while none of its answer has been
  taken: a connect that failed, a connection closed before an answer, or 408, 429, 500, 502, 503,
  504 and 529, after the Retry-After the API asks for, at most 10s. A caller that cancels, as when
  the browser goes away, closes the call's connection, so the API stops working on it.
- **Thinking and tool calls go back as each API wants them.** Anthropic's thinking block and its
  signature, Gemini's thought signature on a function call, and every kind's own shape of a tool's
  result (a `tool` message, a `tool_result` block, a `function_call_output` item, a
  `functionResponse`) are kept on the messages, so that a tool call round-trips. Arguments a model
  wrote that are not JSON are kept as a string and marked, for the agent loop to hand back as an
  error.
- **The `llm_proxy` provider** calls `llm.proxy.api` on loopback, whatever address it listens on,
  with one of the proxy's API keys. Over TLS, the client pins the proxy's own certificate, read
  from its `cert_file`, rather than trusting a CA for a self-signed one.
- **The tool registry, skills, levels, conversations and the audit log** are as the dev plan's
  chapter sets them, built in I125–I130 and I152–I154 on the settings this records:
  `limits` bound the conversations, tool calls and results, `data` what of the logs may be sent,
  `users` and `default_level` (off, read, edit, propose, apply) what each console user may do.

## Consequences

- The workbench's menu has the module Assistant when `admin.assistant` is set, with its Settings
  page; the chat panel comes with I125.
- `GET /api/m/ai/settings`, `PUT /api/m/ai/settings` and `POST /api/m/ai/providers/test` are the
  console's, behind its login and CSRF token, as every module's API is.
- A store saved by a fit with the assistant's settings is version 3; an older fit cannot open it.
  That is the price of keeping keys encrypted with the config.
- The model client opens its own connections on the listeners' engine, up to 8 per host, at most
  16 kinds of transport (by connect timeout and pinned certificate), for the few calls a console's
  users make at once.
