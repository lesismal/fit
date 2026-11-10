# Assistant

[English](14-assistant.md) | [简体中文](14-assistant.zh-CN.md)

This is chapter 14 of the tutorial: the workbench's **Assistant** menu, the assistant
`admin.assistant` turns on. Its Settings page edits `admin.assistant` (after a save the settings are kept
in the store); the other pages read what the assistant did. The full description is in
[the console and the store: the assistant](../console.md#the-assistant), and the field tables in
[the config reference: Admin API](../reference/config.md#admin-api).

## What it does

`admin.assistant` (off when left out) turns on the workbench's assistant: the console then serves the `ai`
module and the menu shows **Assistant**. It needs `admin.console`, and is read when fit starts, like all
of `admin`.

The assistant is made of its **providers** (`anthropic`, `openai`, `openai_responses`, `gemini`,
`azure_openai`, `ollama`, `openai_compatible`, and `llm_proxy` — this fit's own
[LLM roles](25-llm.md)), with a default model, each provider with its own timeouts and retries. Its
**keys** are write only: a page shows only whether a provider has a key, where it comes from (the store or
an environment variable) and its last four characters; an `api_key_env` variable is read at every call, and
a key kept in the store comes first.

The assistant only reads: its tools read what this fit runs, on the console's pool, through the Admin API
in process, and every result is redacted and cut. It never changes anything itself — the propose tools only
add steps to a proposal, which a user whose level is `apply` applies on the **Proposals** page.

Every console user has a **level**: `off` (no panel), `read` (the read tools), `edit` (also drafts on the
open page, never submitted), `propose` (also proposals) and `apply` (also applies them), each level
including the ones before it.

## Pages

The menu's pages, in order: **Functions**, **Skills**, **Proposals**, **Audit log**, **MCP** and
**Settings**; the chat panel opens from the top bar of every page.

### Functions

Every tool of the registry, by group (Config, Runtime, Roles, Media, Sync, BitTorrent, Cluster, Logs, Page,
Change, Convert, Agent): the level it needs, whether this user may call it under the settings in use and why
not, what it does, and the JSON Schema of its arguments. The top of the page shows the user's level and the
model in use (whether it calls tools). No tool writes the config, a version or the store: the page tools only
draft into the open page, and the propose tools only add steps to a proposal. The two groups the newer modules
added are in [Tools by group](#tools-by-group) below.

### Skills

Every skill: the 22 built-in ones (the last of them `fleet-health`, `rollout-planner`, `cluster-drift` and
`sync-pipeline`) and those users added. Each shows its title and what it is for in the workbench's language,
the tools it names (those this user may not call muted) and whether it is enabled. Only a user whose level is
`apply` adds (from the template or by importing `.md` files), edits and deletes the skills users added, and
enables or disables any skill; the built-in ones cannot be changed or deleted, but can be exported, added under
a new name as your own, and then the built-in one disabled. Every user reads them and runs one in the panel with
`/name` or the **Skills** button. Added skills are at most 64, kept in the store; without one they last until
fit restarts.

### Proposals

The pending proposals: each proposal's **steps**, the members it changes, whether it **checks** (on the
running config), and its **diff** in `.conf`, `.yaml` and `.json`. A user whose level is `apply` sees every
user's here; others see their own.

**Review and apply…** first opens a dialog with the whole diff, and only its **Apply** sends the request,
with the proposal's revision and the ETag of the config as seen: it makes a new version (source console),
saves it to the store and lists it in **Versions**, then runs the cache purges. If the config changed
meanwhile the apply comes back `412` and the proposal is offered again on the new version, to be reviewed
and confirmed again. **Discard** drops the proposal; the config does not change. Only a user whose level is
`apply` applies.

### Audit log

What the assistant did for each user, the latest first: the tools an answer called (arguments redacted and
cut), each proposal applied, refused, offered again or discarded, and the media library files each user
changed on the workbench's pages, with the conversation, the skill and how it ended. Filter by user, kind
and conversation, a page at a time; kept in the store within `limits.max_audit_entries` (1000), or until fit
restarts without one.

### MCP

The MCP server at `/mcp` on the console's address: whether it is on, its settings
(`admin.assistant.mcp`, saved with the assistant's settings), how to connect a client (the Claude Code
command and the JSON config), and the user's **tokens**: name, level (`propose` at most), days and the
optional source addresses. A token is shown once — fit keeps only its hash — and is revoked on the same
page. A user whose level is `apply` sees and revokes everyone's tokens. Outside agents call the tools the
panel calls, read the config, the schema, the versions and the reference as resources, and use the enabled
skills as prompts; `ask_user` and the page tools are not served. A token's proposals wait on the
**Proposals** page and are applied only there.

### Settings

**Providers**: name, kind, base URL, API version (`azure_openai`), key (write only) and models; every provider
has its own **Timeouts and retries** (`connect_timeout`, `first_byte_timeout`, `idle_timeout`, `timeout` and
`retries`). **Test** calls a provider as the editor has it, saved or not: first its model list, then a short
call to one model, each with its time; **Read the provider's list** adds the models it lists. Below are
**Model and parameters** (the default model, its output tokens, temperature and thinking), **Limits**,
**Data sent to models** (whether fit's own log lines and the access log's are sent, the playback sessions'
subjects, the records of sync dry runs and dead letters, and more to redact) and the **Users**' levels.
**Save settings** keeps them with the keys typed in the store, which then takes the place of
`admin.assistant` from the file; without a store nothing can be saved here and keys come from `api_key_env`.

### The chat panel

Every page's top bar has the assistant button (none for a user whose level is `off`), and the panel opens
beside the page. A message goes with **the current page as context** (its module, page and title, the
selection the page declares, and the text selected on it) to the default model; the answer streams back, the
Markdown drawn as it comes, each tool call a folded row with its arguments and its result, and an `ask_user`
question a row of buttons whose choice is the call's result. **Stop** ends the answer and keeps what was
made; **Retry** answers the last message again; the panel's head picks, starts or deletes conversations. A
pending proposal shows above the composer, with **Apply** and **Discard**. On the pages there are **Ask AI**
buttons: they pick the skill, fill its inputs, send the thing asked about as the page's selection, write the
question in the user's language and send it at once.

## Tools by group

The registry's groups, in the Functions page's order: Config, Runtime, Roles, Media, Sync, BitTorrent, Cluster,
Logs, Page, Change, Convert and Agent. What each group's tools read is in
[the assistant section of the console guide](../console.md#the-assistant); the two groups the newer modules
added are these.

### Sync

The sync role's four read tools, which change no pipeline: pausing, resuming, a new snapshot, the checkpoint
and the dead letters stay the person's, on the [Sync pages](26-sync.md).

| tool | level | what it does |
| --- | --- | --- |
| `sync_pipelines` | `read` | every pipeline's state, last error, counts, throughput, lag and the dead letters it keeps; or one pipeline's whole status with its config as it runs |
| `sync_test` | `read` | a running pipeline's source and sink connections tested, writing no record |
| `sync_dry_run` | `read` | a running pipeline's first records (5, at most 20) through its transforms, from its checkpoint or its start, writing nothing |
| `sync_dead_letters` | `read` | a pipeline's dead letters (20, at most 100, after an ID): the error, where it failed, the attempts and the record |

The records of a dry run and of dead letters are the person's data: each value of their key and rows reaches
the model as its kind only (`(string)`, `(int)`, a map's fields by name) unless
`admin.assistant.data.send_records` is set — the Settings page's **Send the records of sync dry runs and dead
letters as they are, not as kinds**; namespaces, operations, positions and errors always go. On a manager,
`sync_pipelines` and `sync_dead_letters` take `member`. The skill `sync-pipeline` builds a pipeline from a
sentence and finds why one stalls, lags, stops or keeps dead letters; a pipeline's last error and its dead
letters have **Ask AI** buttons that run it.

### BitTorrent

The tools of the [BitTorrent pages](27-bittorrent.md): four read tools and three propose tools, which read the
`bittorrent.client` and `bittorrent.tracker` roles through the Admin API and add steps to the conversation's
proposal, as the media tools do.

| tool | level | what it does |
| --- | --- | --- |
| `bt_torrents` | `read` | the torrents in short (state, why held, error, progress, size, rates, peers, ratio, time left), by state, category and name (50, at most 200), with the alternative rates |
| `bt_torrent` | `read` | a torrent's status, its first 100 files with their priorities, its first 50 peers, and its trackers and web seeds |
| `bt_tracker_stats` | `read` | the tracker's totals and refusals by reason, and the first 50 swarms (the allow-list's, or a public tracker's by info hash) |
| `bt_rss` | `read` | the RSS auto-download's feeds (last poll, result, counts) and rules, and the latest items seen; a feed's URL as scheme, host and last path element |
| `bt_add_propose` | `propose` | a torrent added by magnet link or `.torrent` URL, in a category, paused or sequential |
| `bt_pause_propose` | `propose` | torrents paused, by ID (at most 100) |
| `bt_resume_propose` | `propose` | torrents resumed, by ID (at most 100) |

No passkey reaches the model: a torrent's magnet link is left out, a tracker's URL keeps its scheme, host and
last path element (`https://tracker.example/[redacted]/announce`), a web seed's loses its user and query, and
peers their IDs; no tool reads a user's passkey, which the Tracker page shows to the person. The BitTorrent
steps join the proposal's media steps and are applied with the proposal, after any config change, in order,
through `/bittorrent/torrents`, as the person who applies it, and one that names a torrent removed meanwhile
stops checking. On a manager the read tools take `member`, and `cluster_bittorrent` reads the cluster's own view
([chapter 12](12-cluster.md#bittorrent)). The BitTorrent pages have no **Ask AI** buttons: these tools are
reached from the panel.

## Key parameters

Only the key parameters; the full table is in [the config reference: Admin API](../reference/config.md#admin-api)
([the Chinese chapter](../reference/config.zh-CN.md)).

| field | default | description |
| --- | --- | --- |
| `providers` | none | the model APIs the assistant may call, each by name; their keys are not written here |
| `default_model` | the first model of the first provider | the provider and model the chat uses |
| `params` | the defaults | `max_output_tokens` (4096), `temperature` and `thinking` (`off`) |
| `limits` | the defaults | see the table below |
| `data` | the defaults | `send_logs`, `send_access_log`, `send_subjects` and `send_records` (all off by default), and `redact` |
| `users` | none | what each console user may have the assistant do |
| `default_level` | `read` | the level of the users not listed |
| `mcp` | off | the MCP server at `/mcp` on the console's address |

### `limits`

| field | default | description |
| --- | --- | --- |
| `max_tool_calls` | 16 | the tools an answer may call before the assistant stops it |
| `max_turns` | 40 | the turns a conversation keeps |
| `max_concurrent_chats` | 4 | the answers being made at once, across users |
| `max_tokens_per_day` | 1000000 | the tokens one user's calls may take in a day |
| `max_tool_result_bytes` | 32768 (32 KiB) | the bytes of a tool's result handed to the model |
| `max_stream_bytes` | 8388608 (8 MiB) | the bytes of one model answer as it streams in |
| `max_conversations` | 50 | the conversations kept for each user |
| `max_audit_entries` | 1000 | the audit entries kept |

### Each provider

| field | default | description |
| --- | --- | --- |
| `name` | required | the provider's name, which `default_model` and the Settings page name it by |
| `kind` | required | `anthropic`, `openai`, `openai_responses`, `gemini`, `azure_openai`, `ollama`, `openai_compatible` or `llm_proxy` |
| `base_url` | the vendor's | required for `openai_compatible` and `azure_openai`; not for `llm_proxy` |
| `api_version` | none | the api-version of an `azure_openai` provider's calls, as `2024-10-21`; needed for it, and for no other kind |
| `api_key_env` | none | an environment variable holding the key, read at every call; a key in the store comes first |
| `connect_timeout` | `10s` | how long a connect, TLS included, may take |
| `first_byte_timeout` | `2m` | how long a call may wait for its answer to begin |
| `idle_timeout` | `1m` | how long a streamed answer may go silent |
| `timeout` | `10m` | the most one call may take, to the end of its answer |
| `retries` | 2 | how often a call is tried again before any of its answer was taken (0 to 5) |
| `models` | none | the models offered from this provider; `context` and whether it calls tools are optional |

## In .conf

The bootstrap file (`admin` is read when fit starts and the console's pages never edit it; the keys are not
in the config, they go to the store from the Settings page or come from `api_key_env`):

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

## See also

- [The console and the store: the assistant](../console.md#the-assistant): the providers and keys, the chat
  panel, the proposals, the levels, the skills, the Ask AI buttons, and
  [the MCP server](../console.md#the-mcp-server).
- [The config reference: Admin API](../reference/config.md#admin-api): every field of `admin.assistant`.
- [Login](02-login.md) and [Account](13-account.md): the assistant uses the console's logins and users.
- [Cluster](12-cluster.md): the assistant on a manager and its cluster tools, `cluster_bittorrent` among them.
- [Sync](26-sync.md) and [BitTorrent](27-bittorrent.md), the modules the tools of the newer groups read;
  [sync](../sync.md) and [BitTorrent](../bittorrent.md) for the features themselves.
- [LLM roles](25-llm.md): the `llm_proxy` provider.
- [Decision 031](../decisions/031-assistant.md).
- The front end: [app/workbench/web/ai/](../../app/workbench/web/ai/).
