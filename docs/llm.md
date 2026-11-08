# The LLM feature

[English](llm.md) | [简体中文](llm.zh-CN.md)

Config examples, in .conf, in [fit-examples](https://github.com/lesismal/fit-examples): [llm-hub](https://github.com/lesismal/fit-examples/blob/main/llm-hub/fit.conf), [llm-node](https://github.com/lesismal/fit-examples/blob/main/llm-node/fit.conf), [llm-proxy](https://github.com/lesismal/fit-examples/blob/main/llm-proxy/fit.conf).

The LLM feature (`llm` in the config) shares AI models among machines and serves them to agents.
It is three roles of fit, each set by a member of the
config:

- **`llm.node`** serves models. A model is an upstream endpoint of one of the mainstream model
  APIs: OpenAI's and every OpenAI-compatible one, Anthropic's, Google's Gemini, Azure OpenAI,
  Ollama's own and Cohere's ([Models and APIs](#models-and-apis)). It may be a local server, as
  Ollama, vLLM or llama.cpp, or a hosted API with its key. The node also reports its hardware and
  runs a fixed set of management actions.
- **`llm.hub`** is where nodes and proxies meet, usually on a public server that every machine
  reaches. It keeps a table of its nodes and their models, and relays each call to the least loaded
  node that serves the model.
- **`llm.proxy`** gives agents (Claude Code, Codex, an SDK, an app of one's own) the
  models' APIs: `/v1/chat/completions`, `/v1/messages`, `/v1beta/models/{model}:generateContent`,
  `/api/chat`, `/v2/chat` and the rest. It builds the path from the local machine or LAN, through a
  public hub, to the nodes of other compute machines, and serves the API to agents on its own
  machine (on a loopback address) or on its LAN (on a LAN address, with TLS and API keys). It need
  not run on an agent's machine: one proxy on a LAN machine serves every agent of the LAN.

Requests and responses are forwarded byte for byte: fit never converts one API into another,
so tool calls, thinking, images, audio and streaming work as the upstream supports them.

A process runs every role whose member is set, any combination of them, beside the gateway's
listeners and routes or without any; a config with roles and no routes needs no listener. Every
role adds its bindings (its pages, its mesh listener, a proxy's API) to the listeners' fib engine,
and dials its peers on it. The node's HTTP client dials its upstreams on the upstream engine that
fit's own upstream pools use. Why the roles are part of fit is in
[decision 028](decisions/028-llm-and-tunnel.md). The [tunnel](tunnel.md) and [WebRTC](webrtc.md)
roles run the same way, beside these.

Each role logs to fit's `llm` logger, whose level `log.levels` sets, its lines prefixed with the
role's member, as `llm.node: `.

## Configuration

The members are `llm.hub`, `llm.node` and `llm.proxy`. They are decoded as strictly as the
rest of the config: an unknown field is an error, and `fit validate` checks every bound below. The
examples are JSON; the same fields work in a .conf or YAML config. The
[config reference](reference/config.md#llm) lists the same fields.

### What every role has

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `id` | string | made at first start | The role's stable ID, which its peers know it by: up to 128 characters, no spaces. When left out, one is made (`<role>-<16 hex digits>`) and written back into the member. |
| `name` | string | the host's name | The name the role shows its peers: up to 64 characters, no control characters, no spaces around it. |
| `console` | object | none | The role's web pages. See [The pages](#the-pages). |
| `listen` | object | none | Where the role takes links from its peers. |
| `access` | object | none: the global `access` decides | `{allow, deny, allow_files, deny_files}`: the client addresses `listen`, and a proxy's `api`, take connections from, deciding before the global [`access`](reference/config.md#access); a connection refused is closed as it is accepted. It is set again on every apply, the member changed or not, without listening anew; links already open stay. Its files are watched as certificate files are. Not the role's pages. |
| `tokens` | array | none | The tokens the role's listener issued, which its peers link with: `{id, name, hash, created_at, expires_at, note, members}`, at most 1,024. Made by the pages, the Admin API and `fit token`, not by hand. See [Tokens](#tokens). |
| `peers` | array | none | The peers the role dials and keeps linked to; at most 64. |
| `limits` | object | the defaults | Bounds on the role's links. |

`listen`:

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `address` | string | required | The host and port the role takes links on, as a binding of its own on the listeners' engine. The WebSocket path is `/ws`. An empty host listens on every address. |
| `password` | string | none | Deprecated for [tokens](#tokens): the password of every role the listener links with that has no password of its own. Peers prove they know it; it never crosses the network. Whoever holds it may link as any of those roles. Taken in this release, with a warning on the links page while a peer uses it; refused in the next. |
| `node_password` | string | `password` | Deprecated: the password nodes prove to link as a node, on a hub's or a proxy's listener. |
| `proxy_password` | string | `password` | Deprecated: the password proxies prove to link as a proxy, on a hub's or a node's listener. |
| `hub_password` | string | `password` | Deprecated: the password hubs prove to link as a hub, on a node's or a proxy's listener. |
| `cert_file`, `key_file` | string | none | PEM files with the certificate the listener presents, any intermediates after it, and its key. With both, links are taken over TLS (`wss://`), and peers check the certificate with their `ca_file` or `pin`. Read when the listener starts, and again when they change on disk (`fit run -watch-certs`, on by default): the links that open from then on are taken with the new certificate, and those open stay; files that do not load, as when one is written and the other not yet, leave the certificate as it is, and are read again at their next change. |
| `self_signed` | bool | false | Take links over TLS with a self-signed certificate the role makes, for a listener without `cert_file`. The role keeps it, so that it outlives a restart: with `admin.store`, it writes it into `cert` and `key`; without one, into a file beside the config, mode 0600, named after it and the role (`fit.llm.hub.pem` beside `fit.json`), which it reads at each start. It shows its SHA-256 fingerprint on its links page and in its log: peers set that as their `pin`. |
| `cert`, `key` | string | none | The PEM certificate and key `self_signed` made, written by the role when the config has `admin.store`. Delete both, or the file beside the config, for a new certificate, which every peer must pin again. |

A listener takes the role's [tokens](#tokens), which link a peer of any role it links with, and,
until the next release, its passwords. The password a dialer proves decides the role it may say
hello as (see [Hello and roles](#hello-and-roles)); a role with no password of its own and no
`password` is taken with tokens alone. A listener with no password takes tokens alone, and none
until one is issued, which its log says when it starts. A role's own password on a listener of a
role that does not link with it (`hub_password` on a hub's) is an error. A listener that takes one
password from more than one role, as `password` alone does, logs a warning when it starts.

A listener on an address other than a loopback one (`127.0.0.0/8`, `::1`, `localhost`), an empty
host among them, needs TLS: `cert_file` and `key_file`, or `self_signed`. Without it the config is
refused, and the error says what to add. See [Transport and authentication](#transport-and-authentication)
for why.

`peers[]`:

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `address` | string | required | The peer's host and port, as its `listen.address` says. The host is required and the port is a number. Each address appears once. |
| `token` | string | | The token the peer's role issued for this one, `fitt1.<id>.<secret>`, as its pages, its Admin API or `fit token create` show it once. `token` or `password` is required, not both. |
| `password` | string | | Deprecated: the password of the dialing role on the peer's listener: a node dials with the peer's `listen.node_password`, a proxy with its `listen.proxy_password`, a hub with its `listen.hub_password`, or with its `listen.password` when it has none for the role. |
| `role` | string | required | The role the peer must announce: `hub`, `node` or `proxy`, never the dialing role's own. A hub dials nodes and proxies, a node dials hubs and proxies, a proxy dials hubs and nodes. |
| `pin` | string | none | The SHA-256 fingerprint of the certificate the peer's listener presents, 64 hex digits, colons and case ignored, as its links page and its log show it. The link goes over TLS (`wss://`), and only to a listener with exactly that certificate; its name and dates are not checked. |
| `ca_file` | string | none | Or a PEM file with the certificates of the authorities the peer's certificate must chain to: the link goes over TLS, and the certificate must name `server_name`, or the address's host. One of `pin` and `ca_file`. Read when the peer is set. |
| `server_name` | string | the address's host | With `ca_file`, the name the certificate must have, and the TLS server name sent. |

A peer at an address other than a loopback one needs `pin` or `ca_file`; without either the config
is refused, and the error says what to add. A peer on loopback without them is dialed over plain
`ws://`; with them, over `wss://`, so its listener needs TLS too.

`limits`:

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `max_conns` | int | 1024 | The most links the role takes from its peers at once. One past it is refused, and its dialer backs off. |
| `max_inflight` | int | 1024 | The most calls and streams a peer may run here on one link at once. One past it fails at once. |
| `ping_interval` | duration | `15s` | How long a link may hear nothing before it is pinged. A link that hears nothing for three intervals is closed. At least 100ms. |
| `slow_timeout` | duration | `30s` | How long a ping may wait for its pong before the link is taken for too slow and closed. |
| `retry_interval` | duration | `5s` | The longest wait between dials of a peer that cannot be reached. At least 100ms. |
| `stream_idle` | duration | `2m` | How long a stream may carry nothing either way before it ends. At least 1s. |

### `llm.hub`

The fields every role has, and:

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `max_nodes` | int | 1024 | The most nodes the hub keeps in its table, offline ones included. Past it, the offline node seen longest ago is forgotten. |

### `llm.node`

The fields every role has, and:

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `description` | string | none | A note about the node its peers show; up to 1024 bytes. |
| `models` | array | none | The models the node serves; at most 256. |
| `settings` | object | the defaults | The node's own settings. |
| `proxy` | object | the defaults | Bounds on the calls the node forwards upstream. |

`models[]`:

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `name` | string | required | The name agents ask for: 1 to 128 characters, no spaces. A name is unique among the node's models of one agent API, so one name may have an OpenAI, an Anthropic and a Gemini model; an `azure_openai` model counts as OpenAI. |
| `api` | string | required | The API the upstream speaks: `openai`, `anthropic`, `gemini`, `azure_openai`, `ollama` or `cohere`. It sets the agent paths the model is reached through, the upstream URL and the key's header; see [Models and APIs](#models-and-apis). |
| `base_url` | string | required | The upstream's base URL, http or https, without a query, a fragment or a trailing slash, as `http://127.0.0.1:8000/v1`. A call goes to it with its path appended, each kind its own way: `base_url/chat/completions`, `base_url/models/{upstream}:generateContent`, `base_url/openai/deployments/{upstream}/chat/completions`, `base_url/api/chat`. |
| `upstream` | string | `name` | The model ID sent upstream in place of `name`, wherever the kind names the model: the body, a form field, Gemini's path, Azure's deployment. Up to 256 characters, no spaces. |
| `api_key` | string | none | The key sent upstream: as `Authorization: Bearer` for `openai`, `ollama` and `cohere`, as `x-api-key` for `anthropic`, `x-goog-api-key` for `gemini`, `api-key` for `azure_openai`. Up to 4096 characters, no spaces. The pages never show it whole. |
| `api_version` | string | none | The `api-version` every call of an `azure_openai` model carries, as `2024-10-21`: required there, refused for the other kinds. Up to 64 letters, digits, dots, dashes and underscores. |
| `disabled` | bool | false | Keep the model without serving it. |
| `description` | string | none | A note about the model its peers show; up to 1024 bytes. |
| `max_inflight` | int | 0 | The most calls to the model the node forwards at once, 0 to 100000. A call past it goes to another node. 0 leaves only `proxy.max_inflight`. |

`settings`:

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `allow_hub_manage` | bool | false | Let linked hubs manage the node: its models, name and description, GPU settings, its fixed actions and their jobs. Never its settings, and never a command of their own. |
| `gpu_sudo` | bool | true | Run GPU settings, tool installs and actions that need root with `sudo -n` when fit does not run as root. sudo must not ask for a password. |
| `refresh_interval` | duration | `30s` | How often the node reads its hardware again: 5s to 1h. |
| `job_timeout` | duration | `1h` | How long a job may run before it is killed: 10s to 168h. |
| `gpu_data_dir` | string | the working directory | Where the install scripts of vendors whose drivers are downloaded by hand look for the packages, under the vendor's name. Scripts see it as `FIT_LLM_GPU_DATA_DIR`. |

`proxy`:

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `connect_timeout` | duration | `10s` | How long a connection to an upstream may take. |
| `first_byte_timeout` | duration | `5m` | How long an upstream may take to begin its response, from the start of the call, its connect included. |
| `idle_timeout` | duration | `5m` | How long an upstream may go silent in the middle of its response. |
| `timeout` | duration | `30m` | The most one call may take, to the end of its response. |
| `max_inflight` | int | 256 | The most calls the node forwards at once, across its models. A call past it goes to another node. |
| `max_body_bytes` | int | 33554432 (32 MiB) | The largest request body a call may carry. A body is held whole only to set its model to `upstream`, when the body names the model. |

### `llm.proxy`

The fields every role has, and:

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `api` | object | none | The API agents on the proxy's machine or LAN call. Without it the proxy links to its peers but serves no API. |
| `api_keys` | array | none | The keys agents call the API with; at most 256. The pages make and delete them. |

`api`:

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `address` | string | required | The host and port the API listens on. A loopback address, as `127.0.0.1:9301`, serves the agents of the proxy's machine; a LAN address, as `192.168.1.10:9301`, or an empty host, serves the agents of other machines too, and needs `cert_file` and `key_file`, so that keys never cross a network in the clear. |
| `cert_file` | string | none | A PEM file with the API's certificate and any intermediates. With `key_file`, the API is served over TLS. |
| `key_file` | string | none | A PEM file with the certificate's private key. |
| `max_body_bytes` | int | 33554432 (32 MiB) | The largest request body. It is held whole, to read the model it names and to send it again when a route cannot serve it; one past it gets 413. |
| `max_conns` | int | 256 | The most connections the API keeps open; one past it is closed as soon as it is accepted. |
| `max_inflight` | int | 256 | The most calls the API forwards at once; one past it gets 429. |
| `first_byte_timeout` | duration | `5m` | How long a call may wait for its response to begin. |
| `timeout` | duration | `30m` | The most one call may take, to the end of its response. |

`api_keys[]`:

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `id` | string | required | The key's ID: 16 hex digits, unique. |
| `name` | string | required | What the key is for: 1 to 64 characters. |
| `prefix` | string | none | The key's first characters, shown on the pages; up to 16. |
| `hash` | string | required | The SHA-256 of the key, 64 hex digits. The key itself is not kept. |
| `created_at` | int | none | When the key was made, in seconds since 1970. |

### A hub

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

### A node

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

### A proxy

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

The `password_hash` is made with `fit hash-password`; the one above is an example only. API keys
are made on the proxy's pages, not written by hand, and so are the hub's `tokens`: its links page,
its Admin API or `fit token create -role llm.hub -name gpu-1` issues each, keeps its hash and shows
the token once, for the node's or proxy's peer `token`; the hub writes `members` itself. The `pin`
is the hub's fingerprint, as the hub's links page and its log show it once it has made its
certificate; the one above is an example only.

### Three places

A hub on a public server at hub.example.net, a node on a GPU machine in a lab, and a proxy on a
machine of an office LAN, at 192.168.1.10. The node and the proxy dial the hub; neither needs a
listener of its own, so neither needs a public address. The proxy serves the agents of every
machine of the LAN, over TLS, with its API keys. The hub takes links over TLS with a certificate
it makes itself; the node and the proxy pin it by the fingerprint its links page shows, and link
with a token each that the hub issued.

The server, which takes tokens alone:

```json
{"llm": {"hub": {"listen": {"address": ":9210", "self_signed": true}}}}
```

```sh
fit token create -c fit.json -role llm.hub -name gpu-1    # the GPU machine's token, shown once
fit token create -c fit.json -role llm.hub -name office   # the LAN machine's
```

The GPU machine:

```json
{"llm": {"node": {
  "peers": [{"address": "hub.example.net:9210", "token": "fitt1.fdd44d906b8e6ce2.Hm2vJvGyTEJVh2_zzu0ZJlENz1A3IYXS2JydjHyJShQ",
             "role": "hub", "pin": "134daba9eaf7f4b2f15a8ca3788273bd52d0b7f6f34581a4561458814d95b0c9"}],
  "models": [{"name": "qwen3-32b", "api": "openai", "base_url": "http://127.0.0.1:8000/v1", "upstream": "Qwen/Qwen3-32B"}]
}}}
```

The LAN machine:

```json
{"llm": {"proxy": {
  "peers": [{"address": "hub.example.net:9210", "token": "fitt1.5a3716dd56e5e458.nz51QI_A3avzPo4tagvevbtlJ-dNi-Us-k4-il9gAV4",
             "role": "hub", "pin": "134daba9eaf7f4b2f15a8ca3788273bd52d0b7f6f34581a4561458814d95b0c9"}],
  "api": {"address": "192.168.1.10:9301", "cert_file": "/etc/fit/lan.crt", "key_file": "/etc/fit/lan.key"}
}}}
```

Agents on the LAN use `https://192.168.1.10:9301` as their base URL. A proxy that serves only the
agents of its own machine listens on loopback, as `127.0.0.1:9301`, and needs no certificate.

Each machine adds a `console` to manage its role from a browser. A proxy may also dial a node
directly, and one process may run a hub and a node together. A hub with a certificate from an
authority, for hub.example.net, takes `cert_file` and `key_file` in place of `self_signed`, and its
peers `ca_file` in place of `pin`.

## Models and APIs

A model's `api` is the API its upstream speaks. It sets which endpoints of `llm.proxy` reach the
model, the URL the node calls, the header the node sends the model's key in, and where the model is
named, which is where the node puts `upstream` in place of the name.

| `api` | Upstreams | Agents call, on llm.proxy | The node calls | Key header | The model is in |
| --- | --- | --- | --- | --- | --- |
| `openai` | OpenAI, and every OpenAI-compatible API: DeepSeek, Qwen (DashScope's compatible mode), Mistral, Groq, xAI, Moonshot, Zhipu, OpenRouter, Together, Fireworks; vLLM, SGLang, llama.cpp's server, LM Studio, Ollama's `/v1` | `POST /v1/{call}` | `base_url/{call}` | `Authorization: Bearer` | the JSON body's `model`, or a form's `model` field |
| `azure_openai` | Azure OpenAI deployments | as `openai`: `POST /v1/{call}` | `base_url/openai/deployments/{upstream}/{call}?api-version={api_version}`; `responses` goes to `base_url/openai/responses?api-version=…` | `api-key` | the URL's deployment; for `responses`, the body's `model` |
| `anthropic` | Anthropic, and servers that speak its Messages API | `POST /v1/messages`, `/v1/messages/count_tokens` | `base_url/{call}` | `x-api-key` | the JSON body's `model` |
| `gemini` | Google's Gemini API | `POST /v1beta/models/{model}:{method}`, or `/v1/models/{model}:{method}` | `base_url/models/{upstream}:{method}` | `x-goog-api-key` | the path |
| `ollama` | Ollama's own API | `POST /api/chat`, `/api/generate`, `/api/embed` | `base_url/api/…` | `Authorization: Bearer`, when `api_key` is set | the JSON body's `model` |
| `cohere` | Cohere's v2 API | `POST /v2/chat`, `/v2/embed`, `/v2/rerank` | `base_url/{call}` | `Authorization: Bearer` | the JSON body's `model` |

The calls each API takes:

- **openai**: `chat/completions`, `responses`, `completions`, `embeddings`, `moderations`,
  `images/generations`, `images/edits`, `images/variations`, `audio/speech`,
  `audio/transcriptions`, `audio/translations`, and `rerank` (vLLM's, Jina's and the other
  Cohere-style rerank endpoints at `/v1/rerank`). An `azure_openai` model takes them all but
  `moderations`, `images/variations` and `rerank`, which get 404.
- **anthropic**: `messages`, `messages/count_tokens`.
- **gemini**: `generateContent`, `streamGenerateContent`, `countTokens`, `embedContent`,
  `batchEmbedContents`.
- **ollama**: `api/chat`, `api/generate`, `api/embed`.
- **cohere**: `chat`, `embed`, `rerank`.

Only stateless calls go through. Calls that keep state at the vendor, as files, batches, stored
responses, assistants, fine-tuning or Gemini's cached contents, are not served, and get 404.

Bodies and answers:

- A request body is JSON, except OpenAI's uploads (`images/edits`, `images/variations`,
  `audio/transcriptions`, `audio/translations`), which are `multipart/form-data` with the model in
  their `model` field. The body's content type goes upstream with it, its boundary included.
- An answer goes back with its status and content type as the upstream gives them, a piece at a
  time: server-sent events, Ollama's NDJSON, Gemini's JSON array when it streams without `alt=sse`,
  the audio bytes of `audio/speech`.
- Of an agent's query, only Gemini's `alt` goes upstream (`alt=sse` for server-sent events); an
  `azure_openai` model's `api-version` is the node's own; nothing else does, an agent's key
  included.
- Of an agent's headers, only `anthropic-version` (2023-06-01 when the agent sends none) and
  `anthropic-beta` go to an `anthropic` upstream, and `OpenAI-Beta` to an `openai` one. No other
  header goes, the agent's key included; the node sends the model's own.

`upstream` takes the place of the name where the model is named:

- In a JSON body's top-level `model` (`openai`, `anthropic`, `ollama`, `cohere`, and Azure's
  `responses`): the node holds the body whole, within `proxy.max_body_bytes`, sets `model`, and
  keeps every other member's bytes.
- In a form's `model` field: the node holds the body whole, within `proxy.max_body_bytes`, and
  replaces the field's value; every other byte, the files included, stays.
- In Gemini's path: nothing in the body changes, so the body streams, but for
  `batchEmbedContents`, whose `requests[].model` are set to `models/{upstream}` too.
- As Azure's deployment, in the URL: the body streams unchanged.

A body whose model is the upstream ID already, or that does not name it, streams upstream as it
arrives.

The `base_url` of each:

| `api` | `base_url`, for instance |
| --- | --- |
| `openai` | `https://api.openai.com/v1`, `https://api.deepseek.com/v1`, `https://dashscope.aliyuncs.com/compatible-mode/v1`, `https://api.mistral.ai/v1`, `https://api.groq.com/openai/v1`, `https://api.x.ai/v1`, `https://api.moonshot.cn/v1`, `https://open.bigmodel.cn/api/paas/v4`, `https://openrouter.ai/api/v1`, `http://127.0.0.1:8000/v1` (vLLM, SGLang), `http://127.0.0.1:8080/v1` (llama.cpp), `http://127.0.0.1:1234/v1` (LM Studio), `http://127.0.0.1:11434/v1` (Ollama) |
| `azure_openai` | `https://{resource}.openai.azure.com` |
| `anthropic` | `https://api.anthropic.com/v1` |
| `gemini` | `https://generativelanguage.googleapis.com/v1beta` |
| `ollama` | `http://127.0.0.1:11434` |
| `cohere` | `https://api.cohere.com/v2` |

A node serving one model of each:

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

### The config is the one source

A role keeps nothing in a file of its own. What its pages change is written back into its member:
the listener, the peers, the name and description, the bounds on its links (`limits`), a node's
models, settings and upstream bounds (`proxy`), a hub's `max_nodes`, a proxy's API (`api`) and API
keys, and the ID made at first start. A setting left at its default is left out of the member. The change goes through `admin.Configs` as a config edit
whose source is the member, as `llm.node`: the running config with the member replaced is applied
as any config is, checked first, recorded in the history and open to rollback. A change the check
refuses changes nothing, and the pages get 400 with the reason. The role then moves to the new
member without dropping links that did not change.

- With `admin.store`, the change is written to the store, encrypted, and survives restarts.
- Without a store, the change runs until fit restarts or reads its files again, and a warning says
  so. Put the config in a store before relying on the pages.

[The console guide](console.md) covers the versions these edits make, rolling one back, and how
`admin.store` keeps the config.

Kicking a link from the pages is the one change that is not written: it closes the link, and the
dialer links again.

The members hold secrets: a peer's token, mesh passwords and model API keys, in the clear. A plain
config file is as safe as the file; `admin.store` keeps them encrypted. A proxy's API keys are kept
as their SHA-256 only, and a listener's tokens as a hash that cannot be used to link, so neither the
config nor the store holds a key or a token another can use with them.

## The mesh

The roles link over a mesh of WebSocket connections.

### Transport and authentication

A role with `listen` adds a binding on the listeners' engine at `listen.address`, which closes a
connection from an address the role's `access`, then the global one, refuses as it accepts it. It
upgrades only `/ws`; any other path is refused with 403. A dialer connects to `wss://<address>/ws`
when its peer has a `pin` or a `ca_file`, and to `ws://<address>/ws` otherwise, which only a
loopback address allows; the whole dial, TLS and authentication included, is bounded by 10 seconds.

Off loopback, links go over TLS 1.3, served by fib's TLS, whose handshake takes no worker while it
waits. The dialer checks the listener's certificate: by its `pin`, the SHA-256 of the certificate,
compared in constant time, in place of the chain and name checks; or by `ca_file`, the chain to one
of its authorities and the name. No TLS session is resumed, on either side, so every link checks
the certificate in a full handshake. The token's (or password's) handshake below still runs inside
TLS, so a link needs both: the certificate the dialer expects, and a token the listener issued.

TLS is what keeps a link to the two ends that made it. The handshake is checked once, when
the link opens, and the frames after it carry no proof of their own. Over plain `ws://`, one who
took over a listener's address, knowing no password, could pass a dialer's handshake through to
the real listener, then send requests on the link as the listener, which the dialer serves: a node
with `allow_hub_manage` would run them as its hub's. Over TLS, the relay cannot answer for the
pinned certificate, and the records it passes through are sealed with keys it does not have: what
it injects ends the link.

After the upgrade, before any frame, with a token:

1. The listener sends a 32-byte random challenge.
2. The dialer answers with its token's ID, a 32-byte random nonce of its own, and its proof of the
   token over a transcript of the token's ID, its nonce and the challenge
   ([decision 035](decisions/035-link-tokens.md) has the keys).
3. The listener looks the token up, refuses one it did not issue, or one expired, closing with code
   4001 and the reason, and checks the proof. Its acknowledgement carries its own proof of the token
   over the same transcript, under another key and the label of the other direction. The token links
   a peer of any role the listener takes.
4. The dialer checks the listener's proof, and gives up the dial when it is missing or wrong.

A proof replayed on another link, answered to another nonce, or sent back to its sender does not
check, and the listener keeps only a hash of the token, which cannot be used to link. With a
password (deprecated):

1. The listener sends a 32-byte random challenge.
2. The dialer answers with the HMAC-SHA256 of the challenge, keyed with its password (the one the
   listener has for the dialer's role), and a 32-byte random nonce of its own.
3. The listener checks it against each of its passwords and acknowledges, or closes with code 4001
   when it proves none. The password proven decides the roles the link may say hello as. Its
   acknowledgement carries its own proof: the HMAC-SHA256, keyed with that same password, of a
   fixed label, the challenge and the dialer's nonce.
4. The dialer checks the listener's proof, and gives up the dial when it is missing or wrong, so
   that a listener that does not know the password gets no link. On its own this does not stop one
   that took over the address and relays the handshake to the real listener; TLS, with the
   certificate checked, does.

A listener still takes a dialer of an earlier version, which sends no nonce; a dialer of this
version takes no listener of an earlier one, which proves nothing, so listeners are upgraded first.

The handshake takes at most 5 seconds. A listener holds at most 256 connections that have not
authenticated yet, from their TCP connect on, and 16 from one IP address; one more is closed as it
opens, so that connections that never authenticate cannot take the places of those that do. The
password never crosses the network, and a recorded
proof cannot be replayed. A wrong password, a certificate that does not match the pin or the
authorities, or a plain dialer at a TLS listener fails the dial, and the dialer backs off; the
reason shows on its links page. Over TLS, prompts, responses and management calls are encrypted;
on loopback, over plain `ws://`, they stay on the machine.

### Moving to TLS

TLS off loopback is required from this version on, and a config that has a listener or a peer off
loopback without it is refused, so a role that has one does not start until it is changed. For
each role with such a listener:

1. Add `"self_signed": true` to its `listen` (or `cert_file` and `key_file`), and start it. Its
   log, and its links page, give the certificate's fingerprint.
2. Add that fingerprint as `pin` to every peer that dials it (or `ca_file`, for a certificate from
   an authority).

A listener and its dialers move together: a dialer with a pin gets nowhere with a plain listener,
and a plain dialer nowhere with a TLS one. Roles on one machine, linked over loopback, need no
change.

### Tokens

A role's listener issues a token for each peer that dials it, in place of a password a role's
peers share ([decision 035](decisions/035-link-tokens.md)):

- **Each token its own credential.** A token is `fitt1.<id>.<secret>`, a 16-hex-digit ID and 256
  random bits. The role keeps, in its member's `tokens`, the ID, a name, when it was made, an
  optional expiry, an optional note and a hash, never the token: it is shown once, when it is made.
- **Of no role.** A valid token links a peer of any role the listener takes; what the peer may call
  follows from the role it says hello as, as [Hello and roles](#hello-and-roles) says.
- **Bound to its members.** The first ID that says hello with a token is bound to it and written into
  the token's `members`, so that no other token says hello as that ID, across restarts. Revoking the
  token frees it: to move a peer to a new token, issue the new one, revoke the old, and give the peer
  the new one.
- **Expiry.** A token may expire at a set time or after a duration (`30d`, `720h`); an expired token
  is refused, and its links are closed at its expiry (at most a minute late, should the wall clock
  jump). Revoking a token closes its links at once.
- **Managed three ways**, each change a config edit of the role's member, applied and kept as any
  page's edit is, and logged to the `admin` logger: the links page's **Link tokens** card (the
  methods `/mesh/tokens/list`, `/mesh/tokens/create`, `/mesh/tokens/expiry`, `/mesh/tokens/revoke`);
  the Admin API, `GET /tokens/llm.hub`, `POST /tokens/llm.hub` `{name, note, ttl | expires_at}`,
  `PATCH /tokens/llm.hub/{id}` `{ttl | expires_at}` (neither: never) and `DELETE /tokens/llm.hub/{id}`,
  and the same for `llm.node` and `llm.proxy`; and `fit token list|create|expiry|revoke -role llm.hub`.
  The list gives each token's last use and the address it came from since fit started, its members
  and the links that use it now.

### Moving to tokens

The listener's passwords still link in this release, beside tokens, and are refused in the next.
The links page shows which inbound links proved a shared password, and warns while any does. To
move a role:

1. Upgrade the listener's fit first: a dialer with a token gets nowhere with an earlier listener.
2. Issue a token for each peer that dials it, and set it as that peer's `token` in place of its
   `password` (the links page's **Connect to…** form does both).
3. Once the warning is gone, edit the listener and tick **Remove every password**, so that only
   tokens link.

### Frames, sessions and streams

Each WebSocket message is one frame: a type (request, response, stream-open, stream-data,
stream-end, stream-cancel, push, ping, pong, credit, stream-keepalive, drain), flags (gzip, error,
binary), a 32-bit stream ID, a method name for requests, stream opens and pushes, and the payload.
A payload of 1 KiB or more is gzipped when that makes it smaller. A frame, compressed or inflated,
is at most 16 MiB.

A link carries a session. The dialer numbers its streams odd and the listener even; a request uses
a stream ID too. Requests get one response; pushes get none and run one at a time, in order. A
peer may run at most `limits.max_inflight` requests and streams on a link; one more fails at once
with a rate-limited error. A link whose pushes wait past 1024 is closed.

A stream carries data both ways. Each direction has a receive window of 256 KiB: a sender sends
while it has credit, and the receiver grants credit back as it consumes. A slow reader holds back
its own stream and no other. Only the responder ends a stream, with its result; the opener may
close its side of it, as a streamed request body does at its end, or cancel it. A stream that
carries nothing either way for `stream_idle` ends; a side still working, as a node waiting for an
upstream's first token, sends stream-keepalive frames, which reset the idle timer.

### Heartbeat, slow links and drain

Each link has a timer. A link that hears nothing for `ping_interval` is pinged, and so is one whose
last round trip was measured two intervals ago, so that both ends know its latency; one that hears
nothing for three intervals is closed (code 4002). A pong is sent behind everything sent before
its ping, so a ping waiting `slow_timeout` for its pong means the link is that far behind; it is
closed (code 4004). A listener past `max_conns` refuses a link with code 4003.

A role that stops sends drain on every link: its peers start no new call on it, the calls running
finish, for up to 10 seconds, and then the links close. Dialers link again with backoff.

### Traffic and latency

Every link counts the bytes of the frames it carries each way, compressed as they cross, pings
included, and the round trips of its pings: the last, a moving average weighing recent ones most
(each new one moves it by 0.3), and the shortest. A pong waits behind everything sent before its
ping, so a busy link's round trip includes its queue.

Each role also counts the model calls it relays, per route, since it started: a proxy per hub or
node it calls and model, a hub per proxy, node and model, a node per caller and model. A route's
calls, failures (a call that broke; on a node and a proxy, an upstream's answer of 400 or more too),
calls running, request bytes (the body, inline or streamed) and response bytes (the head frame and
the body) are read from the call's stream as it crosses, so a long answer counts as it streams. At
most 1024
routes are counted apart; the calls of the rest are counted together, as `other`. None of it is
written anywhere.

`/mesh/status`, for the role's pages only, answers `{self, links, routes, at}`: the role's identity,
each link (as `/mesh/peers` lists them) with its `link`, `{bytes_in, bytes_out, rtt: {last, avg,
min, samples}}` in milliseconds, the routes, `{from, to, model, calls, errors, inflight, req_bytes,
resp_bytes, last_at}` (`from` empty for a proxy's agents, `to` empty for a node's upstream), and
`at`, when it was taken, in milliseconds, so that the pages work out rates between two answers.

### Hello and roles

The dialer's first call is `/mesh/hello`, with its identity: role, ID, name, host name, version,
process ID, start time, its mesh address and its pages' URL when it has them. The listener answers
with its own. A link that has not said hello in 15 seconds is
closed. A peer is refused when:

- its role is the role's own (hubs never link to hubs, nodes to nodes, proxies to proxies);
- its role is not one the role links with: a hub links nodes and proxies, a node links hubs and
  proxies, a proxy links hubs and nodes;
- on a link it dialed in with a password, its role is not one whose password the link proved;
- on a link it dialed in with a token, its ID is bound to another token (see [Tokens](#tokens));
- on a dialed link, its role is not the one `peers[].role` names;
- its ID is the role's own;
- its ID is linked already under another role.

A refused dialer waits at least 30 seconds, or `retry_interval` when longer, before trying again.
What a peer may call depends on its role, as hello said. A token is of no role: its holder says
hello as any role the listener takes, and its ID is bound to the token, so that another token's
holder cannot say hello as that ID and take its calls. With a password, the role is bound to the
password the peer proved: a proxy that holds a hub's `proxy_password` cannot join it as a node, nor say hello to
a node as a hub when it holds the node's `proxy_password`, so it cannot manage a node that has
`allow_hub_manage` on. A refused hello leaves the link without a role: nothing it calls runs, and it
is closed a second later. `listen.password` alone, or a role's password set the same as
another's, trusts its holder in every role it is the password of, as before roles had passwords of
their own; give it only to peers trusted in all of them. The roles of one kind share their password:
a node that knows a hub's `node_password` could pose as that hub to nodes that dial it if it took
over the hub's address and, off loopback, held the hub's certificate, which its pin names.

### Pools and backoff

The links with one peer, by its ID, whichever side dialed them, form a pool. Calls go round the
pool, skipping draining links; a peer with only draining links is offline, and a call to it fails
at once so that the caller tries another peer.

A dialer never gives up on a peer in `peers`. After the nth failure in a row it waits a random time
up to `min(retry_interval, 1s × 2ⁿ)`, at least 100ms. A link that lived 10 seconds or more starts
the steps again from 1 second.

### Request bodies

A proxy sends a body of up to 1 MiB inside the proxy stream's opening request (`ProxyReq.body`),
and a larger one as `body_stream: true` with its size (`body_size`), followed by the body in frames
of 64 KiB under the stream's credit. A hub relays the frames as they come; a node passes them
upstream as they arrive. The opening request also carries the call's protocol, path and model, the
body's content type when it is not JSON (`content_type`) and the query parameters that go upstream
(`query`).

## The node

### Models

Each model is a `models[]` entry. Its state is `disabled`, `error` (the last check or call found
the upstream down) or `ready`; only a ready model below its `max_inflight` serves calls.

The pages, and a hub when `allow_hub_manage` is on, list, save, delete and check models. A model's
key is never shown whole: the list shows its first four and last four characters, or `****` for a
key of 12 characters or fewer. A save whose key is empty or masked keeps the key the member has.

### Checks

A check is a `GET` of the upstream's model list with the call's authentication headers, bounded by
15 seconds: `base_url/models` (`openai`, `anthropic`, `gemini`, `cohere`),
`base_url/openai/models?api-version=…` (`azure_openai`) or `base_url/api/tags` (`ollama`). A
failed connection, 401, 403 or a 5xx answer puts the model in `error`; any other answer, 404 and
405 included, means the upstream serves. Enabled models are checked every 60 seconds, and every 15
seconds while in `error`, so a recovered upstream comes back soon. A call that cannot reach the
upstream also puts the model in `error`; an answer below 500 clears it.

### The proxy

`/node/proxy` serves one agent call, on fib's asynchronous HTTP client:

1. The call must be one of its API's ([Models and APIs](#models-and-apis)), and its body JSON or
   `multipart/form-data` with a boundary.
2. The model (its name, of a kind that speaks the call's API) must be ready and below its
   `max_inflight`, and the node below `proxy.max_inflight`. Otherwise the call fails as
   unavailable, and a hub or proxy tries another node. A call the model's kind does not serve,
   as `images/variations` of an `azure_openai` model, fails as not found.
3. The URL is the kind's. The body goes upstream as it arrives. It is held whole only when the
   model's `upstream` differs from the name the agent asked for and the body names the model, to
   set it; the other bytes stay. A body past `proxy.max_body_bytes` fails.
4. The upstream gets the body's content type, the model's key in the kind's header, and the
   headers and query parameters [Models and APIs](#models-and-apis) lists. No other header of the
   agent's goes, its own key included.
5. While no byte has come back, the node keeps the stream alive. The first frame back is the head:
   the upstream's status, content type and the node's ID. Then each piece of the response body is
   a frame, sent as it arrives, so server-sent events are not batched.
6. Backpressure links both ways. A request body waits on the upstream connection's write queue, and
   the stream's credit is held back until it drains. A response body is held when the caller grants
   no credit, and resumes when it does.
7. `connect_timeout`, `first_byte_timeout`, `idle_timeout` and `timeout` bound every call. A caller
   that cancels or goes away cancels the upstream request.

Each call is logged with the model, API, path, status, bytes, duration and caller.

The pages' "test call" opens the same stream as an agent's call.

### Actions and jobs

A node never runs a script or command its caller supplies. Its management is a fixed list of
actions defined in code: install Ollama, list, pull, show and remove Ollama models, show the models
Ollama has loaded, the Ollama service's status, restart and logs, `nvidia-smi`, install the NVIDIA
Container Toolkit, each on the systems it applies to (the Ollama service's on Linux; Ollama's
install on Linux, and on macOS with Homebrew). Each takes
only declared parameters, checked against a pattern, each passed as its own argument, never through
a shell. GPU settings (power limits, clocks, fans and the like, per vendor) are checked by type and
range and run the same way; a vendor's tool install runs a script that is a constant of the code.

Actions, GPU settings and installs run as jobs, on a pool of their own: at most 8 run at once and a
ninth is refused; 200 jobs are kept, the oldest finished ones dropped first; each keeps the last
1 MiB of its output. A job past `job_timeout` is killed with its process group. The pages follow a
job's output as it is written.

The hardware, every `refresh_interval`, and the GPUs' status, when the pages or a hub ask, are read
on a small pool of the node's (2 running, 16 waiting). The GPUs' status is read with the vendors'
tools, one read at a time and within 25 seconds, and kept for 5 seconds unless a fresh one is asked
for.

### Hub management

With `allow_hub_manage` on, a linked hub may call the node's management methods: models, name and
description, GPU status and settings, actions and jobs. The node checks the setting on every call,
so turning it off takes effect at once. A hub cannot change the node's settings, cannot read a
model's key, and, like the pages, can only run the fixed actions.

### Pushes

The node pushes its info (identity, description, hardware, models and their state and load, calls
and jobs running) to every linked hub and proxy when it changes, at most once per 200ms, and once
to each peer as it links. Each push carries a sequence that starts from the node's start time, so a
receiver drops what is older, across restarts too.

## The hub

### The node table

When a node links, the hub fetches its info, then follows its pushes. A node is online while it has
a link that is not draining. The table holds at most `max_nodes` nodes; past it, the offline node
seen longest ago is forgotten. The table is in memory only: after a restart, the hub knows the
nodes that link again.

### Models

The hub lists, per name and API, the models of its online nodes: which nodes serve each, and how
many are ready. Disabled models are left out. On a change it pushes the list to every linked
proxy, at most once per 200ms, and once to each proxy as it links.

### The node list

The hub keeps its proxies up to date with its nodes: `/hub/node/list`, which a proxy reads as it
links, and the push `/hub/node/list/update` when it changes. Each node is listed with its ID, name,
description, whether it is online and when it was last seen, its hardware without its disks and
addresses and with at most 16 GPUs, its models with their state and calls running (at most 64, with
`models_total`), its calls and jobs running, its links with the hub (direction, bytes, latency), and
the calls the hub relayed to it (`traffic`). Online nodes come first. The list holds the hub's
table, so `max_nodes` bounds it, and at most 4 MiB encoded: past it, the nodes at its end are left
out and `total` still counts them. A change goes out after 200ms, no sooner than a second after the
last push; the traffic and latency, which change all the time, at most every 5 seconds while
proxies are linked. Each list carries a sequence, so a proxy drops an older one.

Proxies only show the list. Managing a node stays with the node's own pages and its hubs'.

### Picking a node

`/hub/proxy` takes the same request as `/node/proxy`. The hub picks, among the online nodes serving
the model ready and not excluded by the proxy, the one with the least load: the model's calls the
node reported, plus the calls the hub relays to it now. Ties go round robin. It relays the frames
both ways as they come, under each stream's credit.

Before the node's head, a node that cannot serve the model, goes offline or loses its link is left
for the next one, up to 3 nodes; a streamed body is retried only while none of it has been taken
from the proxy. After the head, bytes have reached the proxy, and any failure ends the call.
Other errors, as a bad request, end the call at once.

### Managed calls

The hub's pages manage a node through two methods, open to the pages only: `/hub/node/call` for a
management method (bounded by 90 seconds) and `/hub/node/stream` for following a job. The node
decides what is allowed.

## The proxy

### The API

| Method and path | API | Forwarded as |
| --- | --- | --- |
| `POST /v1/chat/completions`, `/v1/responses`, `/v1/completions`, `/v1/embeddings`, `/v1/moderations`, `/v1/images/generations`, `/v1/images/edits`, `/v1/images/variations`, `/v1/audio/speech`, `/v1/audio/transcriptions`, `/v1/audio/translations`, `/v1/rerank` | openai, for `openai` and `azure_openai` models | the path after `/v1/` |
| `POST /v1/messages`, `/v1/messages/count_tokens` | anthropic | the path after `/v1/` |
| `POST /v1beta/models/{model}:{method}`, `/v1/models/{model}:{method}` | gemini | the method; the model is the path's |
| `POST /api/chat`, `/api/generate`, `/api/embed` | ollama | the path after `/` |
| `POST /v2/chat`, `/v2/embed`, `/v2/rerank` | cohere | the path after `/v2/` |
| `GET /v1/models`, `GET /v1/models/{id}` | every model | answered by the proxy |
| `GET /v1beta/models`, `GET /v1beta/models/{model}` | gemini models | answered by the proxy |
| `GET /api/tags` | ollama models | answered by the proxy |

The paths are where each vendor's SDKs call once their base URL is the proxy's, so an agent needs
nothing but the base URL and a key. The API is a binding of its own at `api.address`, apart from
the pages. Any other path or method gets 404. The model lists need a key too. `GET /v1/models`
lists every model name the proxy's hubs and nodes list, with the fields of both OpenAI's and
Anthropic's model lists, so either SDK reads it; `GET /v1beta/models` lists the Gemini models as
Gemini's `models.list` does, and `GET /api/tags` the Ollama models as Ollama's does.

A call names its model in its JSON body's `model`, in a `multipart/form-data` body's `model` field
(OpenAI's uploads, which only OpenAI's paths take), or, for Gemini, in its path. A response has the
upstream's status and content type, `Cache-Control: no-cache`, `X-Accel-Buffering: no` and
`X-Fit-Node` naming the node that served it; its body is written as the frames come.

### API keys

A key is `ah-` followed by 32 random bytes in unpadded base64url, 46 characters. The pages show it
once, when they make it; the member keeps its ID, name, first 10 characters, SHA-256 and creation
time. An agent sends it as `Authorization: Bearer <key>`, `x-api-key: <key>` or
`x-goog-api-key: <key>`, or on Gemini's paths as the query parameter `key`, as Google's REST
examples do; its SHA-256 is compared with every hash kept, in constant time.

The keys' use (calls and last use) and the per-model counters (calls, failures, bytes, calls
running) are kept in memory since the role started; they are not written anywhere. At most 1024
models are counted apart; calls to more are counted together, as `other`.

### Routing

The proxy reads the body whole, within `api.max_body_bytes`, to find its model (a Gemini call's
is in its path). It then tries routes in order:

1. Nodes it links to directly, serving the model ready with a kind that speaks the call's API, the
   least loaded first: the load the node reported plus the calls this proxy runs on it.
2. Hubs listing the model with a ready node, round robin.

A route that fails before the head because it cannot serve the model now, is offline, or loses its
link, gives way to the next, up to 4 routes. The nodes that failed are excluded when a hub is tried.
After the head there is no retry.

### Nodes

The proxy's nodes page shows, for each hub it links to, the nodes in the hub's list, and the nodes
it links to directly, with its own link and calls to each: `/proxy/node/list` answers `{hubs: [{hub,
online, nodes, total, seq}], direct: [...]}`. It is a view: nothing on it changes another machine.

### Errors

Errors take the shape of the API the agent called: OpenAI's `{"error": {"message", "type", "code"}}`,
Anthropic's `{"type": "error", "error": {"type", "message"}}`, Gemini's
`{"error": {"code", "message", "status"}}`, Ollama's `{"error": "<message>"}` or Cohere's
`{"message"}`. A path that is none of them gets the shape its prefix suggests (`/v1beta/` Gemini's,
`/api/` Ollama's, `/v2/` Cohere's); on `/v1/`, an agent that sends `anthropic-version` or
`x-api-key` gets Anthropic's shape. A body past `max_body_bytes` is
the exception: the HTTP server refuses it with a plain-text 413 before the API reads it.

| Case | Status |
| --- | --- |
| An unknown path or method | 404 |
| A missing or wrong key | 401 |
| A body that is not JSON, or has no `model`; a form without its `model` field, or a form on a path that takes none | 400 |
| A body past `max_body_bytes` | 413 |
| A model no peer serves, or served only with another API; a call the model's kind does not serve | 404 |
| More than `max_inflight` calls | 429 |
| The model has no ready route, every route was unavailable, or no response began within `first_byte_timeout` | 503 |
| A node or hub answered a bad request (as a body past the node's `proxy.max_body_bytes`), not found or rate limited | 400, 404, 429 |
| `timeout` passed before the response began | 504 |
| Any other failure before the head | 502 |

An upstream's own error, once its head has come, is passed on as it is. A failure after the head
cuts the response short.

### Using it

In these examples, `127.0.0.1:9301` is the proxy's `api.address` and `<key>` an API key made on
its pages. An agent on another machine of the LAN uses the proxy's LAN address over https instead.

The OpenAI SDK (Python):

```python
from openai import OpenAI
client = OpenAI(base_url="http://127.0.0.1:9301/v1", api_key="<key>")
client.chat.completions.create(model="qwen3-32b", messages=[{"role": "user", "content": "Hello"}])
```

Claude Code, with a model served with the Anthropic API. `ANTHROPIC_BASE_URL` has no `/v1`: the
SDK appends `/v1/messages`.

```sh
export ANTHROPIC_BASE_URL=http://127.0.0.1:9301
export ANTHROPIC_AUTH_TOKEN=<key>      # or ANTHROPIC_API_KEY=<key>, sent as x-api-key
export ANTHROPIC_MODEL=claude-sonnet
claude
```

Codex, in `~/.codex/config.toml`, with a model served with the OpenAI API:

```toml
model = "qwen3-32b"
model_provider = "fit_llm"

[model_providers.fit_llm]
name = "fit llm"
base_url = "http://127.0.0.1:9301/v1"
env_key = "FIT_LLM_API_KEY"
wire_api = "chat"   # or "responses" when the upstream serves /responses
```

then `export FIT_LLM_API_KEY=<key>` and run `codex`.

The Gemini SDK (Python, `google-genai`), with a model served with the Gemini API. The base URL has
no version: the SDK appends `/v1beta/models/...`.

```python
from google import genai
from google.genai import types
client = genai.Client(api_key="<key>", http_options=types.HttpOptions(base_url="http://127.0.0.1:9301"))
client.models.generate_content(model="gemini-flash", contents="Hello")
```

Ollama's Python library, with a model served with Ollama's API:

```python
from ollama import Client
client = Client(host="http://127.0.0.1:9301", headers={"Authorization": "Bearer <key>"})
client.chat(model="llama3.2", messages=[{"role": "user", "content": "Hello"}])
```

Cohere's SDK: `cohere.ClientV2(api_key="<key>", base_url="http://127.0.0.1:9301")`.

curl, a chat and a transcription, whose audio file goes as a form:

```sh
curl http://127.0.0.1:9301/v1/chat/completions -H "Authorization: Bearer <key>" \
  -H "Content-Type: application/json" \
  -d '{"model": "qwen3-32b", "stream": true, "messages": [{"role": "user", "content": "Hello"}]}'

curl http://127.0.0.1:9301/v1/audio/transcriptions -H "Authorization: Bearer <key>" \
  -F model=whisper -F file=@meeting.wav
```

The proxy's overview page shows these with its own address and models.

## The pages

A role's `console` member serves its web pages, as a binding of its own on the listeners' engine:
the workbench of [the console guide](console.md#the-pages), with the role's module alone in its
menu. When fit also runs `admin.console`, the role's pages show there too, as the module `llm.hub`,
`llm.node` or `llm.proxy` under LLM, behind its login, with no `console` member needed.

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `address` | string | required | The host and port. A host that is not a loopback address needs `cert_file` and `key_file`. |
| `cert_file`, `key_file` | string | none | PEM files; with both, the pages are served over TLS. |
| `users` | array | required | Who may log in: `username` and `password_hash`, a bcrypt hash from `fit hash-password`. At least one. |
| `session_timeout` | duration | `30m` | How long a login lasts without a request. |
| `session_lifetime` | duration | `12h` | How long a login lasts at most. |
| `max_sessions` | int | 64 | The most logins kept; one more ends the oldest. |
| `max_conns` | int | 32 | The most connections the pages keep open; one past it is closed as soon as it is accepted. |

Logins work as `admin.console`'s, through the same code; [the console guide](console.md) describes
users, sessions and lockouts. Logins are checked with bcrypt on a pool of their own (1 running,
16 waiting), the API's tasks on the console's pool (4 running, 64 waiting), at most 2 checks at
once; a task that waits past 30 seconds gets 504, and one past the queue 503. After 5 failed
logins from an address (an IPv6 /64) within 15 minutes, one checked at a time, the address is
refused for 15 minutes (429, with `Retry-After`). A session is a cookie (`fit_llm_hub_<port>`,
`fit_llm_node_<port>` or `fit_llm_proxy_<port>`, with the console's port, since a browser sends a
host's cookies to all its ports, so that consoles on one host keep their logins apart; HttpOnly, SameSite=Strict, Secure over TLS). Every API
request but a GET or HEAD carries the session's CSRF token in `X-Fit-Csrf`, and a request whose
`Origin` is another site is refused with 403. A request body is at most 1 MiB and its header 16 KiB.

What each role's pages show, in English or Chinese:

- **A hub**: an overview; its nodes, and for each one that allows hub management, its models, GPUs,
  actions, jobs and name; the models of its nodes; the proxies linked to it; its traffic; its links;
  its settings: its name, `max_nodes` and `limits`.
- **A node**: an overview, with its name and description; its models, with a check and a test call
  each; its GPUs and their settings; its actions; its jobs; its traffic; its links; its settings:
  its name and description, `settings`, the upstream bounds (`proxy`) and `limits`.
- **A proxy**: an overview, with the setups of [Using it](#using-it) filled in with its own address
  and models; the models it reaches and their routes; the nodes behind its hubs, to see only
  ([Nodes](#nodes)); its traffic; its API keys; its links; its settings: its name, its API (`api`:
  address, certificate and key files, bounds, or none) and `limits`.

The links page sets the listener and the peers, and kicks a link. It shows whether the listener
takes TLS and the fingerprint of its certificate, which its dialers pin, and how each peer's
listener is checked: by a pin, a CA file, or not at all on loopback. Its **Link tokens** card issues
a token (shown once, to copy), lists the tokens with their expiry, last use, members and links,
changes a token's expiry and revokes one; each inbound link shows the token it proved, or a warning
that it proved a shared password, and the listener's edit can remove every password. A peer is
added with the token its listener issued, which replaces a password.

The traffic page draws the role's links as a map of columns, as the tunnel's pages do: a proxy's
agents, the proxy, its hubs, their nodes and their models; a hub's proxies, the hub, its nodes and
their models; a node's hubs and proxies, the node, its models and their upstreams. Each line has its
bytes each way and its rate, worked out between two reads of `/mesh/status` three seconds apart, its
width by traffic, dashed while idle, and its latency coloured good (under 50ms), fair (under 200ms)
or poor; a table below lists the same lines with their calls. Clicking a box or a line shows its
details.

A settings card reads its settings and writes them as the config: a value the config would refuse,
as a `ping_interval` under 100ms, a negative bound or a non-loopback API address without a
certificate, gets 400 with the reason, and changes nothing.

The API under `/api/`:

| Endpoint | Answer |
| --- | --- |
| `POST /api/login` | Starts a session from `{username, password}`; answers `{user, csrf}`. |
| `POST /api/logout` | Ends the session; answers 204. |
| `GET /api/session` | The user and the CSRF token. |
| `GET /api/me` | `{user, identity}`, the role's identity; a proxy adds `api_url`, its API's URL. |
| `POST /api/rpc<method>` | Runs a method, as `/api/rpc/mesh/peers`, and answers its result as JSON, or `{"error", "code"}` with the code's HTTP status; 504 when the method's time ran out. |
| `POST /api/stream<method>` | Opens a stream method and answers Server-Sent Events: a data event per frame, `end` with the result, `error` with `{code, message}`. Closing the request cancels the stream. |

The pages call the same methods peers call, as a local caller allowed everything, and the methods
only the pages may call: the mesh's (`/mesh/peers`, `/mesh/status`, `/mesh/dial`, `/mesh/undial`,
`/mesh/kick`, `/mesh/listen`, `/mesh/limits/get`, `/mesh/limits/set`, and the tokens',
`/mesh/tokens/list`, `/mesh/tokens/create`, `/mesh/tokens/expiry`, `/mesh/tokens/revoke`); a hub's node table
(`/hub/nodes`), managed calls (`/hub/node/call`, `/hub/node/stream`), name (`/hub/profile/set`) and
settings (`/hub/settings/get`, `/hub/settings/set`); a node's settings (`/node/settings/get`,
`/node/settings/set`) and upstream bounds (`/node/upstream/get`, `/node/upstream/set`); a proxy's
models, nodes, counters, keys, name and API (`/proxy/models`, `/proxy/node/list`, `/proxy/stats`,
`/proxy/keys/list`, `/proxy/keys/create`, `/proxy/keys/delete`, `/proxy/profile/set`,
`/proxy/api/get`, `/proxy/api/set`).

A method has 10 seconds, the mesh's included; a node's or a proxy's method that changes the
config has 30, a model check 20, a node's GPU status and settings 30, and a hub's managed call 90. Methods run on the role's pool (16 goroutines, 256
waiting), never on a fib worker; a method past the queue gets 503. A stream's events are written
without waiting, and its frames are released to the stream only as the browser takes them, so a
slow browser holds back its own stream; at most 16 streams are open on a role's pages at once, a
seventeenth gets 429, and a browser that takes nothing for 30 seconds ends its stream.

## Limits and rules

Every bound has a default; a config that sets none is bounded.

| Bound | Default | Where |
| --- | --- | --- |
| Mesh links taken | 1024 | `limits.max_conns` |
| Calls and streams per link | 1024 | `limits.max_inflight` |
| Ping interval, slow timeout | 15s, 30s | `limits` |
| Dial backoff ceiling | 5s | `limits.retry_interval` |
| Stream idle | 2m | `limits.stream_idle` |
| Stream window | 256 KiB per direction | fixed |
| Frame | 16 MiB | fixed |
| Pushes waiting per link | 1024, then the link closes | fixed |
| Dial, handshake, hello | 10s, 5s, 15s | fixed |
| Links not authenticated yet | 256, 16 per IP address | fixed |
| Method timeout | 10s; 30s for a node's or proxy's config change, 20s for a model check, 30s for GPU status and settings, 90s for a hub's managed call | fixed |
| Peers, models, API keys | 64, 256, 256 | fixed |
| Link tokens, members of a token | 1,024, 256 | fixed |
| Nodes in a hub's table | 1024 | `max_nodes` |
| A hub's node list | `max_nodes` nodes, 64 models and 16 GPUs each, 4 MiB; pushed after 200ms, a second apart, its traffic every 5s | fixed |
| Routes counted | 1024 per role, the rest as `other` | fixed |
| Node calls at once | 256, and per model `max_inflight` | `proxy.max_inflight` |
| Node request body | 32 MiB | `proxy.max_body_bytes` |
| Node connect, first byte, idle, total | 10s, 5m, 5m, 30m | `proxy` |
| Upstream connections per host | 64, with 256 waiting | fixed |
| Upstream response body | 1 GiB | fixed |
| Model checks | 15s each; every 60s, 15s in error; 4 connections per host, 16 waiting | fixed |
| Jobs | 8 running, 200 kept, 1 MiB output each | fixed |
| Job run time | 1h | `settings.job_timeout` |
| Node's hardware and GPU reads | 2 running, 16 waiting; GPU status 25s, kept 5s | fixed |
| Proxy API body | 32 MiB, held whole | `api.max_body_bytes` |
| Proxy API connections, calls | 256, 256 | `api.max_conns`, `api.max_inflight` |
| Proxy first byte, total | 5m, 30m | `api` |
| Proxy API header size, header read, read, idle | 64 KiB, 10s, 2m, 2m | fixed |
| Proxy wait for a slow agent | 60s per write | fixed |
| Routes per call: proxy, hub | 4, 3 | fixed |
| Proxy counters | 1024 models, the rest as `other` | fixed |
| Role pool | 16 running, 256 waiting | fixed |
| Config edits waiting | 64, across every role of fit's; each as long as its method's timeout | fixed |
| Page streams | 16; 30s for a stalled browser | fixed |
| Page sessions, connections | 64, 32 | `console` |
| Page request body, header | 1 MiB, 16 KiB | fixed |
| Console pool | 4 running, 64 waiting, 2 password checks at once; 30s a task | fixed |
| Failed logins | 5 in 15 minutes, then 15 minutes out | fixed |
| Draining at stop | 10s | fixed |

How [the rules](rules.md) hold:

- **Nothing blocks a fib worker.** Requests on the pages and the API are answered from callbacks;
  what waits runs elsewhere: bcrypt on the console's pool, methods on the role's pool, hardware and
  GPU queries on the node's work pool, commands on the jobs' pool, config edits on the manager's
  goroutine. Mesh requests and streams run on goroutines of the session, as many as
  `limits.max_inflight` allows per link.
- **Bodies stream.** The node forwards calls on fib's asynchronous client, request and response
  bodies as they come, with backpressure both ways through the streams' credit. Only the proxy's
  API holds a body whole, within `api.max_body_bytes`, to read the model and to send it again to the
  next route, and a node holds one only to set the model the body names to `upstream`, within
  `proxy.max_body_bytes`.
- **Every map, queue and pool is bounded**, as the table says; online nodes and catalog entries are
  bounded by the links.
- **Every exchange has timeouts**: dials, handshakes, hello, methods, streams, upstream calls and
  the agents' connections.

Two things fib does not offer yet are written up in [fib's API requests](fib-api-requests.md):

- **A52**, a WebSocket connection's transport: a close that skips the close handshake, and its
  remote address on the dialing side. Until then a listener finds a link's `fib.Connection` by its
  remote address through a wrapper around the WebSocket handler, and a dialer catches its own
  through the dial hook, so that a dead or slow link is closed at once.
- **A53**, backpressure on a WebSocket connection's sends. Until then a link's queue is bounded by
  its streams' windows and the slow timeout: a peer that reads slowly is found when a ping has
  waited `slow_timeout`, and the link is closed.
