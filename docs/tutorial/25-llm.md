# LLM roles

[English](25-llm.md) | [简体中文](25-llm.zh-CN.md)

This is chapter 25 of the tutorial: the pages of the three roles of the `llm` config member in the
workbench — `llm.hub`, `llm.node` and `llm.proxy`. The full field tables are in
[the config reference](../reference/config.md#llm), and the feature itself is described in
[the LLM feature](../llm.md).

## What it does

`llm` runs fit's three LLM roles, each started by its own member, in any combination, beside the
gateway's listeners and routes or without them:

- **`llm.node`** serves models. Each model is an upstream endpoint of one of the major APIs
  (`openai`, `anthropic`, `gemini`, `azure_openai`, `ollama`, `cohere`): a service on the machine
  (Ollama, vLLM, llama.cpp) or a hosted API with a key; the node also reports its hardware and runs a
  fixed list of actions.
- **`llm.hub`** is where nodes and proxies meet: it keeps the node table and the model catalog, and
  passes each call to the least loaded node that serves that model.
- **`llm.proxy`** serves those APIs to agents (`/v1/chat/completions`, `/v1/messages`,
  `/v1beta/models/{model}:generateContent`, `/api/chat`, `/v2/chat`), across the path from the
  machine or the LAN to the public hub to the nodes on other machines.

The roles talk over a mesh of WebSocket links: each role takes links at `listen` and dials its
`peers`; off loopback the links go over TLS, and the dialer checks the peer's certificate by its
`pin` or its `ca_file`. The credential is a **token** the listener issues: the role's `tokens` keep
only its hash, and the token itself is shown once, when it is issued
([decision 035](../decisions/035-link-tokens.md)).

Once a role runs, its module appears in the menu on the left (see
[the function navigation bar](03-navigation.md)): `llm.hub`, `llm.node` or `llm.proxy`. A role
can also have its own `console`; when fit runs `admin.console` at the same time, the role's pages show
there too, as modules under LLM, behind its login, with no `console` member needed. What the pages
change is written back into the role's member through the same path a config change takes.

## Pages

### llm.hub

#### Overview

Four stats: nodes online (`n of N`), proxies, models (`N ready`), in flight; below them the model
table: name, API, ready (`ready of the nodes that serve it`) and the nodes serving it, each linking to
the node's page. It reads again every 5 seconds.

#### Nodes

One row per node: its name and ID, host, state (online, or offline with when it was last seen), its
model badges, in flight, GPUs (a summary such as `2× RTX 4090`), and whether it is **managed**. A row
opens the node's page: **Details**, and for a node that allows hub management the **Models**, **GPU**,
**Actions**, **Jobs** and **Profile** tabs. When it does not, the page says why; management needs the
node online. The hub cannot read a model's key and can only run the fixed actions, like the pages.

#### Models

The models of the online nodes, by name and API: which nodes serve each one and how many of them are
ready. Disabled models are not listed.

#### Proxies

The proxies linked to it: name and ID, host, version, direction (incoming or outgoing), remote address,
when it connected, and whether it is draining.

#### Traffic

The mesh drawn as a map of columns, as the tunnel's pages do: the proxies → this hub → its nodes →
their models. Each line carries its bytes each way and its rate (worked out between two reads of
`/mesh/status` three seconds apart), its latency coloured good (under 50ms), fair (under 200ms) or
poor, its width by traffic, dashed while idle; a table below lists the same lines with their calls.
Clicking a box or a line shows its details.

#### Settings

The display name (peers see it at their next connection), the ID, version and host; the **Node table**
card writes `max_nodes`; the **Link limits** card writes `limits`.

### llm.node

#### Overview

Four stats: GPUs, ready models, calls in flight, running jobs; then **Profile** (name, description, ID,
version, web console, RPC address, when it started, and whether hub management is allowed), **System**
(host name, OS, kernel, arch, CPU, load, uptime, IPs), the memory and disk meters, and a card per GPU
(utilization and memory). It reads again every 10 seconds.

#### Models

One row per model: name, API, base URL and `upstream`, the API key (masked, only that one is kept),
state (ready, disabled or error, with its error and when it was checked), and in flight
(`n / its limit`). The actions: **Check** (a GET of the upstream's model list, 15 seconds at most),
**Enable** or **Disable**, **Edit** and **Delete**. An empty key keeps the stored one. Below the table
is **Test call**: pick a ready model, write a prompt, and make one streamed call through
`/node/proxy`, the same call an agent makes; the raw response is in the foldable **Raw response**, and
**Stop** ends it.

#### GPUs

A card per device: utilization, memory, temperature, power, the GPU and memory clocks, and the fan,
with that vendor's settings (by kind: a number, a `min,max` range, an enum or an action), for this
device alone or for **All GPUs of this vendor**. Above them the vendors and their management tools:
whether detected, whether installed, **Install tool** (run as a job, it may need root), and the query
command and install script. The page says what identity things run as (root, `sudo -n` or the current
user).

#### Actions

A node only runs the fixed actions defined in the code (install Ollama; list, pull, show and delete
Ollama models; show what Ollama has loaded; Ollama's status, restart and logs; `nvidia-smi`; install
the NVIDIA Container Toolkit), each offered only on the systems it applies to. Parameters are
validated one by one and never pass through a shell. **Run** starts an action as a job and then
follows its output.

#### Jobs

The job list: kind, title, status, created and finished, and the exit code; a row follows its output
(as it is written), or **Cancel job**. At most 8 jobs run at once, 200 are kept, and each keeps its
last 1 MiB of output.

#### Traffic

The link map: the hubs and proxies that call it → this node → its models → their upstreams. The same
as the hub's traffic page.

#### Settings

**Profile** (name and description); **Node settings** (allow hubs to manage this node, `gpu_sudo`, the
hardware refresh interval, the job timeout, the GPU driver package directory); **Upstream calls**
(`proxy`: the connect, first byte, idle and total timeouts, the calls at once, the request body bound);
and **Link limits** (`limits`). Calls already in flight keep the bounds they started with.

### llm.proxy

#### Overview

The **Agent API** card gives the three base URLs (OpenAI, Anthropic, the other APIs), each copyable;
the stats: models and how many are ready, API keys, calls and errors, in flight and bytes relayed;
**Quick start** holds the setups filled in with its own address and models (OpenAI SDK, Claude Code,
Codex, curl, the Gemini SDK, Ollama); **Calls per model** lists each model's calls, errors, in flight,
bytes and last call.

#### Models

The models it can reach: name, API, state (ready or not) and its **Routes** badges — a directly linked
node that has the model ready (`direct · name`) and the hubs (`hub · name`, its hover shows how many
of its nodes are ready). Calls prefer a ready direct node (least in flight), then the hubs (round
robin).

#### Nodes

A section per hub, listing the nodes the hub reports, and the nodes this proxy links to directly:
state, hardware, models, load and its link. **To see only**: each machine is managed on its own pages.
A row opens its details.

#### Traffic

The link map: agents → this proxy → its hubs and direct nodes → the hubs' nodes → their models. The
lines from this proxy are its own links and calls; the hub to node lines are what the hub reports.

#### API keys

Each key: name, prefix (`ah-…`), when it was created, last used and its calls. **New API key** takes
only a name; the key is shown only when it is created, and the member keeps its SHA-256. **Delete**
stops the agents using it working. Usage is saved at most once a minute.

#### Settings

**Profile** (the display name); the **Agent API** card (address, certificate and key files, the request
body bound, the connections and calls at once, the first byte and total timeouts, or **Stop the API**);
and **Link limits**. The API is a binding of its own on the listeners' engine, separate from this
console.

### Connections (the same page on all three roles)

Every role's menu has a **Connections** page, which sets the listener and the peers:

- **This process**: role, name, ID, host, version, PID, when it started, its RPC address and its web
  console.
- **RPC listener**: its state, address, whether it has a password, TLS (self-signed, a certificate
  file, or plain ws on loopback only) and its **fingerprint (SHA-256)** — what the dialers pin.
  **Change listener** writes the address, the password, `self_signed` and the certificate files, and can
  **remove every password: only tokens link**.
- **Connections**: each link's role, name, direction, remote address, when it connected, and the
  credential it proved (a token ID, or the shared password — with a warning while any link still proves
  one); **Kick** closes it, and outgoing links reconnect. This is the only change on the pages that does
  not write the config.
- **Dial targets**: each `peers` address with its role, state and peer; **Connect to…** takes the token
  the peer's listener issued (it replaces the password), the role the peer must announce, `pin` or
  `ca_file`, and `server_name`.
- **Link tokens**: issue one (shown once), list them with their expiry, last use, bound members and
  links, change an expiry and revoke one. They come from the page, the Admin API or `fit token`; do not
  write them by hand.

## Key parameters

Only the key parameters; the full tables are in [the config reference: LLM](../reference/config.md#llm).

### Fields every role has

| field | default | description |
| --- | --- | --- |
| `id` | made at the first start | the role's stable ID, which its peers know it by; made and written back when left out |
| `name` | the host name | the name the role shows its peers; the page's display name |
| `console` | none | the role's own pages; not needed when `admin.console` runs |
| `listen` | none | where it takes links; off loopback it needs `cert_file` and `key_file`, or `self_signed` |
| `access` | follows the global `access` | which client addresses `listen` and the proxy's `api` take |
| `tokens` | none | the link tokens the listener issued, at most 1,024; only their hashes are kept |
| `peers` | none | the peers it dials and keeps linked to, at most 64; `token` or `password`, one of the two |
| `limits` | the defaults | the bounds on its links, in the table below |

### `llm.hub`

| field | default | description |
| --- | --- | --- |
| `max_nodes` | 1024 | the nodes kept in the table, offline ones included; past it the offline node seen longest ago is forgotten |

### `llm.node`

| field | default | description |
| --- | --- | --- |
| `description` | none | the note about the node its peers show |
| `models` | none | the models it serves, at most 256; each needs `name`, `api` and `base_url` |
| `settings` | the defaults | `allow_hub_manage` (off by default), `gpu_sudo`, `refresh_interval` (`30s`), `job_timeout` (`1h`), `gpu_data_dir` |
| `proxy` | the defaults | the bounds on the calls it forwards to its upstreams, in the table below |

### `llm.proxy`

| field | default | description |
| --- | --- | --- |
| `api` | none | the API the agents call; without it the proxy still links but serves nothing |
| `api_keys` | none | the keys the agents use, at most 256; only their SHA-256 is kept, and the page makes and deletes them |

### Link limits (`limits`, the same on all three roles)

| field | default | description |
| --- | --- | --- |
| `max_conns` | 1024 | the links from peers taken at once |
| `max_inflight` | 1024 | the calls and streams one peer may run on one link at once |
| `ping_interval` | `15s` | how long a link may hear nothing before it is pinged (100ms at least) |
| `slow_timeout` | `30s` | how long a ping may wait for its pong before the link is taken for too slow |
| `retry_interval` | `5s` | the most a dialer waits between attempts at a peer it cannot reach |
| `stream_idle` | `2m` | how long a stream with no data either way before it ends |

### The node's upstream calls (`proxy`)

| field | default | description |
| --- | --- | --- |
| `connect_timeout` | `10s` | how long a connect to an upstream may take |
| `first_byte_timeout` | `5m` | from the call's start to the answer beginning |
| `idle_timeout` | `5m` | how long an upstream may go silent in the middle of an answer |
| `timeout` | `30m` | the whole call, to the end of its answer |
| `max_inflight` | 256 | the calls forwarded across all models at once; past it a call goes to another node |
| `max_body_bytes` | 33554432 (32 MiB) | the request body bound of one call |

### The proxy's API (`api`)

| field | default | description |
| --- | --- | --- |
| `address` | required | a loopback address serves this machine; a LAN address needs `cert_file` and `key_file` |
| `max_body_bytes` | 33554432 (32 MiB) | the request body bound; a larger one gets 413 |
| `max_conns` | 256 | the connections the API keeps at once |
| `max_inflight` | 256 | the calls it forwards at once; past it a call gets 429 |
| `first_byte_timeout` | `5m` | how long a call waits for its answer to begin |
| `timeout` | `30m` | the whole call, to the end of its answer |

## In .conf

A hub and a node (the hub's `tokens` and the node's `token` come from the pages, the Admin API or
`fit token`; this is only an example, as is the `password_hash`, which `fit hash-password` makes):

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

A config with only roles and no routes needs no listener. What the pages change in these members is
applied as one config edit through `admin.Configs`, and goes into the store, encrypted, when there is
one.

## See also

- [The LLM feature](../llm.md): the models and APIs, the mesh and the tokens, how the node, the hub and
  the proxy work, and the bounds and rules.
- [The config reference: LLM](../reference/config.md#llm): every field ([in Chinese](../reference/config.zh-CN.md)).
- [The console and the store](../console.md#the-pages): logins, sessions and the rules the pages share.
- [Tunnel roles](24-tunnel.md): the same three roles, and the same kind of traffic page.
- [The assistant](14-assistant.md): `llm.proxy` as the assistant's `llm_proxy` provider.
- The front end: [app/workbench/web/llm/](../../app/workbench/web/llm/).
- [Decision 028](../decisions/028-llm-and-tunnel.md) and [decision 035](../decisions/035-link-tokens.md).
