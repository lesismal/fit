# The agent

[English](agent.md) | [简体中文](agent.zh-CN.md)

Config examples, each in .conf, YAML and JSON: [agent-hub](../examples/agent-hub/fit.conf), [agent-node](../examples/agent-node/fit.conf), [agent-client](../examples/agent-client/fit.conf).

The agent shares AI models among machines. It is three roles of fit, each set by a member of the
config:

- **`agent.node`** serves models. A model is an upstream endpoint of an OpenAI- or
  Anthropic-compatible API: a local server such as Ollama or vLLM, or a hosted API with its key.
  The node also reports its hardware and runs a fixed set of management actions.
- **`agent.hub`** is where nodes and clients meet. It keeps a table of its nodes and their models,
  and relays each call to the least loaded node that serves the model.
- **`agent.client`** runs beside the agents (Claude Code, Codex, an SDK). It serves one API,
  `/v1/chat/completions`, `/v1/messages` and the rest, and calls the models of the hubs and nodes it
  reaches.

Requests and responses are forwarded byte for byte: the agent never converts one API into another,
so tool calls, thinking and images work as the upstream supports them.

A process runs every role whose member is set, any combination of them, beside the gateway's
listeners and routes or without any; a config with roles and no routes needs no listener. Every
role adds its bindings (its pages, its mesh listener, a client's API) to the listeners' fib engine,
and dials its peers on it. The node's HTTP client dials its upstreams on the upstream engine that
fit's own upstream pools use. The roles are the port of agenthub into fit; why and how is in
[decision 028](decisions/028-agent-and-tunnel.md). The [tunnel](tunnel.md) and [WebRTC](webrtc.md)
roles run the same way, beside these.

Each role logs to fit's `agent` logger, whose level `log.levels` sets, its lines prefixed with the
role's member, as `agent.node: `.

## Configuration

The members are `agent.hub`, `agent.node` and `agent.client`. They are decoded as strictly as the
rest of the config: an unknown field is an error, and `fit validate` checks every bound below. The
examples are JSON; the same fields work in a .conf or YAML config. The
[config reference](reference/config.md#agent) lists the same fields.

### What every role has

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `id` | string | made at first start | The role's stable ID, which its peers know it by: up to 128 characters, no spaces. When left out, one is made (`<role>-<16 hex digits>`) and written back into the member. |
| `name` | string | the host's name | The name the role shows its peers: up to 64 characters, no control characters, no spaces around it. |
| `console` | object | none | The role's web pages. See [The pages](#the-pages). |
| `listen` | object | none | Where the role takes links from its peers. |
| `peers` | array | none | The peers the role dials and keeps linked to; at most 64. |
| `limits` | object | the defaults | Bounds on the role's links. |

`listen`:

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `address` | string | required | The host and port the role takes links on, as a binding of its own on the listeners' engine. The WebSocket path is `/ws`. An empty host listens on every address. |
| `password` | string | required | The password peers prove they know. It never crosses the network. |

`peers[]`:

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `address` | string | required | The peer's host and port, as its `listen.address` says. The host is required and the port is a number. Each address appears once. |
| `password` | string | required | The peer's `listen.password`. |
| `role` | string | required | The role the peer must announce: `hub`, `node` or `client`, never the dialing role's own. A hub dials nodes and clients, a node dials hubs and clients, a client dials hubs and nodes. |

`limits`:

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `max_conns` | int | 1024 | The most links the role takes from its peers at once. One past it is refused, and its dialer backs off. |
| `max_inflight` | int | 1024 | The most calls and streams a peer may run here on one link at once. One past it fails at once. |
| `ping_interval` | duration | `15s` | How long a link may hear nothing before it is pinged. A link that hears nothing for three intervals is closed. At least 100ms. |
| `slow_timeout` | duration | `30s` | How long a ping may wait for its pong before the link is taken for too slow and closed. |
| `retry_interval` | duration | `5s` | The longest wait between dials of a peer that cannot be reached. At least 100ms. |
| `stream_idle` | duration | `2m` | How long a stream may carry nothing either way before it ends. At least 1s. |

### `agent.hub`

The fields every role has, and:

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `max_nodes` | int | 1024 | The most nodes the hub keeps in its table, offline ones included. Past it, the offline node seen longest ago is forgotten. |

### `agent.node`

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
| `name` | string | required | The name agents ask for: 1 to 128 characters, no spaces. A name and `api` are unique among the node's models, so one name may have an OpenAI and an Anthropic model. |
| `api` | string | required | `openai` or `anthropic`: the API the upstream speaks, and the agent paths the model is reached through. |
| `base_url` | string | required | The upstream's base URL, http or https, without a query, a fragment or a trailing slash, as `http://127.0.0.1:11434/v1`. A call goes to it with its path appended, as `base_url/chat/completions`. |
| `upstream` | string | `name` | The model ID sent upstream in place of `name`; up to 256 characters, no spaces. |
| `api_key` | string | none | The key sent upstream: as `Authorization: Bearer` for `openai`, as `x-api-key` for `anthropic`. Up to 4096 characters, no spaces. The pages never show it whole. |
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
| `gpu_data_dir` | string | the working directory | Where the install scripts of vendors whose drivers are downloaded by hand look for the packages, under the vendor's name. Scripts see it as `FIT_AGENT_GPU_DATA_DIR`. |

`proxy`:

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `connect_timeout` | duration | `10s` | How long a connection to an upstream may take. |
| `first_byte_timeout` | duration | `5m` | How long an upstream may take to begin its response, from the start of the call, its connect included. |
| `idle_timeout` | duration | `5m` | How long an upstream may go silent in the middle of its response. |
| `timeout` | duration | `30m` | The most one call may take, to the end of its response. |
| `max_inflight` | int | 256 | The most calls the node forwards at once, across its models. A call past it goes to another node. |
| `max_body_bytes` | int | 33554432 (32 MiB) | The largest request body a call may carry. |

### `agent.client`

The fields every role has, and:

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `api` | object | none | The API agents call. Without it the client links to its peers but serves no API. |
| `api_keys` | array | none | The keys agents call the API with; at most 256. The pages make and delete them. |

`api`:

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `address` | string | required | The host and port the API listens on. A host that is not a loopback address needs `cert_file` and `key_file`, so that keys never cross a network in the clear. |
| `cert_file` | string | none | A PEM file with the API's certificate and any intermediates. With `key_file`, the API is served over TLS. |
| `key_file` | string | none | A PEM file with the certificate's private key. |
| `max_body_bytes` | int | 33554432 (32 MiB) | The largest request body. It is held whole, to read the model it names; one past it gets 413. |
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
  "agent": {
    "hub": {
      "name": "hub-1",
      "console": {
        "address": "127.0.0.1:9200",
        "users": [{"username": "admin", "password_hash": "$2a$10$sKGBAOOCI4p4WWJcVCuAH.ivT8pNy2DFbs6pd8xmtNEZ/5L1SeLvm"}]
      },
      "listen": {"address": ":9210", "password": "mesh-secret"},
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
  "agent": {
    "node": {
      "name": "gpu-1",
      "description": "Two RTX 4090s",
      "console": {
        "address": "127.0.0.1:9100",
        "users": [{"username": "admin", "password_hash": "$2a$10$sKGBAOOCI4p4WWJcVCuAH.ivT8pNy2DFbs6pd8xmtNEZ/5L1SeLvm"}]
      },
      "peers": [{"address": "hub.example.net:9210", "password": "mesh-secret", "role": "hub"}],
      "models": [
        {"name": "qwen3-32b", "api": "openai", "base_url": "http://127.0.0.1:11434/v1", "upstream": "qwen3:32b",
         "max_inflight": 4},
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

### A client

```json
{
  "agent": {
    "client": {
      "name": "laptop",
      "console": {
        "address": "127.0.0.1:9300",
        "users": [{"username": "admin", "password_hash": "$2a$10$sKGBAOOCI4p4WWJcVCuAH.ivT8pNy2DFbs6pd8xmtNEZ/5L1SeLvm"}]
      },
      "peers": [{"address": "hub.example.net:9210", "password": "mesh-secret", "role": "hub"}],
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
are made on the client's pages, not written by hand.

### Three machines

A hub on a server at 10.0.0.1, a node on a GPU machine, and a client on a laptop. The node and the
client dial the hub; neither needs a listener of its own.

The server:

```json
{"agent": {"hub": {"listen": {"address": ":9210", "password": "mesh-secret"}}}}
```

The GPU machine:

```json
{"agent": {"node": {
  "peers": [{"address": "10.0.0.1:9210", "password": "mesh-secret", "role": "hub"}],
  "models": [{"name": "qwen3-32b", "api": "openai", "base_url": "http://127.0.0.1:11434/v1", "upstream": "qwen3:32b"}]
}}}
```

The laptop:

```json
{"agent": {"client": {
  "peers": [{"address": "10.0.0.1:9210", "password": "mesh-secret", "role": "hub"}],
  "api": {"address": "127.0.0.1:9301"}
}}}
```

Each machine adds a `console` to manage its role from a browser. A client may also dial a node
directly, and one process may run a hub and a node together.

### The config is the one source

A role keeps nothing in a file of its own. What its pages change is written back into its member:
the listener, the peers, the name and description, a node's models and settings, a client's API
keys, and the ID made at first start. The change goes through `admin.Configs` as a config edit
whose source is the member, as `agent.node`: the running config with the member replaced is applied
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

The members hold secrets: mesh passwords and model API keys, in the clear. A plain config file is
as safe as the file; `admin.store` keeps them encrypted. A client's API keys are kept as their
SHA-256 only, so neither the config nor the store holds a key that can be used.

## The mesh

The roles link over a mesh of WebSocket connections. The protocol is agenthub's, unchanged, so
fit's roles and agenthub's programs link to each other.

### Transport and authentication

A role with `listen` adds a binding on the listeners' engine at `listen.address`. It upgrades only
`/ws`; any other path is refused with 403. A dialer connects to `ws://<address>/ws`; the whole
dial, authentication included, is bounded by 10 seconds.

After the upgrade, before any frame:

1. The listener sends a 32-byte random challenge.
2. The dialer answers with the HMAC-SHA256 of the challenge, keyed with the password.
3. The listener checks it and acknowledges, or closes with code 4001.

The handshake takes at most 5 seconds. The password never crosses the network, and a recorded
proof cannot be replayed. A wrong password fails the dial, and the dialer backs off. The mesh is
plain `ws://`: what crosses it after the handshake, prompts and responses included, is not
encrypted, so links between machines belong on a private network or a tunnel.

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

Each link has a timer. A link that hears nothing for `ping_interval` is pinged; one that hears
nothing for three intervals is closed (code 4002). A pong is sent behind everything sent before
its ping, so a ping waiting `slow_timeout` for its pong means the link is that far behind; it is
closed (code 4004). A listener past `max_conns` refuses a link with code 4003.

A role that stops sends drain on every link: its peers start no new call on it, the calls running
finish, for up to 10 seconds, and then the links close. Dialers link again with backoff.

### Hello and roles

The dialer's first call is `/mesh/hello`, with its identity: role, ID, name, host name, version,
process ID, start time, its mesh address and its pages' URL when it has them. The listener answers
with its own. A link that has not said hello in 15 seconds is
closed. A peer is refused when:

- its role is the role's own (hubs never link to hubs, nodes to nodes, clients to clients);
- its role is not one the role links with: a hub links nodes and clients, a node links hubs and
  clients, a client links hubs and nodes;
- on a dialed link, its role is not the one `peers[].role` names;
- its ID is the role's own.

A refused dialer waits at least 30 seconds, or `retry_interval` when longer, before trying again.
What a peer may call depends on its role alone, as hello said.

### Pools and backoff

The links with one peer, by its ID, whichever side dialed them, form a pool. Calls go round the
pool, skipping draining links; a peer with only draining links is offline, and a call to it fails
at once so that the caller tries another peer.

A dialer never gives up on a peer in `peers`. After the nth failure in a row it waits a random time
up to `min(retry_interval, 1s × 2ⁿ)`, at least 100ms. A link that lived 10 seconds or more starts
the steps again from 1 second.

### Bodies over 1 MiB

agenthub put the request body inside the proxy stream's opening request (`ProxyReq.body`). fit adds
two fields to `ProxyReq`: `body_stream` and `body_size`. A client sends a body of up to 1 MiB
inline, as before, and a larger one as `body_stream: true` with its size, followed by the body in
frames of 64 KiB under the stream's credit. A hub relays the frames as they come; a node passes
them upstream as they arrive.

agenthub's nodes and hubs read inline bodies only. A fit role and an agenthub program link and
work together for bodies up to 1 MiB; a larger body needs fit at every role on its way.

## The node

### Models

Each model is a `models[]` entry. Its state is `disabled`, `error` (the last check or call found
the upstream down) or `ready`; only a ready model below its `max_inflight` serves calls.

The pages, and a hub when `allow_hub_manage` is on, list, save, delete and check models. A model's
key is never shown whole: the list shows its first four and last four characters, or `****` for a
key of 12 characters or fewer. A save whose key is empty or masked keeps the key the member has.

### Checks

A check is `GET <base_url>/models` with the call's authentication headers, bounded by 15 seconds. A
failed connection, 401, 403 or a 5xx answer puts the model in `error`; any other answer, 404 and
405 included, means the upstream serves. Enabled models are checked every 60 seconds, and every 15
seconds while in `error`, so a recovered upstream comes back soon. A call that cannot reach the
upstream also puts the model in `error`; an answer below 500 clears it.

### The proxy

`/node/proxy` serves one agent call, on fib's asynchronous HTTP client:

1. The path must be one of the model's API: `chat/completions`, `responses`, `completions` or
   `embeddings` for `openai`; `messages` or `messages/count_tokens` for `anthropic`.
2. The model (name and API) must be ready and below its `max_inflight`, and the node below
   `proxy.max_inflight`. Otherwise the call fails as unavailable, and a hub or client tries another
   node.
3. The body goes upstream as it arrives. It is held whole only when the model's `upstream` differs
   from the name the agent asked for, to set the body's top-level `model`; the other members keep
   their bytes. A body past `proxy.max_body_bytes` fails.
4. The upstream gets `Content-Type: application/json` and the model's key. For `anthropic`,
   `anthropic-version` (2023-06-01 when the agent sends none) and `anthropic-beta` are passed on;
   for `openai`, `OpenAI-Beta`. No other header of the agent's is, its own key included.
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
and jobs running) to every linked hub and client when it changes, at most once per 200ms, and once
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
client, at most once per 200ms, and once to each client as it links.

### Picking a node

`/hub/proxy` takes the same request as `/node/proxy`. The hub picks, among the online nodes serving
the model ready and not excluded by the client, the one with the least load: the model's calls the
node reported, plus the calls the hub relays to it now. Ties go round robin. It relays the frames
both ways as they come, under each stream's credit.

Before the node's head, a node that cannot serve the model, goes offline or loses its link is left
for the next one, up to 3 nodes; a streamed body is retried only while none of it has been taken
from the client. After the head, bytes have reached the client, and any failure ends the call.
Other errors, as a bad request, end the call at once.

### Managed calls

The hub's pages manage a node through two methods, open to the pages only: `/hub/node/call` for a
management method (bounded by 90 seconds) and `/hub/node/stream` for following a job. The node
decides what is allowed.

## The client

### The API

| Method and path | API | Forwarded as |
| --- | --- | --- |
| `POST /v1/chat/completions`, `/v1/responses`, `/v1/completions`, `/v1/embeddings` | openai | the path after `/v1/` |
| `POST /v1/messages`, `/v1/messages/count_tokens` | anthropic | the path after `/v1/` |
| `GET /v1/models`, `GET /v1/models/{id}` | both | answered by the client |

The API is a binding of its own at `api.address`, apart from the pages. Any other path or method
gets 404. `GET /v1/models` needs a key too; it lists every model name the client's hubs and nodes
list, with the fields of both OpenAI's and Anthropic's model lists, so either SDK reads it.

A response has the upstream's status and content type, `Cache-Control: no-cache`,
`X-Accel-Buffering: no` and `X-Agenthub-Node` naming the node that served it; its body is written as
the frames come.

### API keys

A key is `ah-` followed by 32 random bytes in unpadded base64url, 46 characters. The pages show it
once, when they make it; the member keeps its ID, name, first 10 characters, SHA-256 and creation
time. An agent sends it as `Authorization: Bearer <key>` or `x-api-key: <key>`; its SHA-256 is
compared with every hash kept, in constant time.

The keys' use (calls and last use) and the per-model counters (calls, failures, bytes, calls
running) are kept in memory since the role started; they are not written anywhere. At most 1024
models are counted apart; calls to more are counted together, as `other`.

### Routing

The client reads the body whole, within `api.max_body_bytes`, to find its `model`. It then tries
routes in order:

1. Nodes it links to directly, serving the model ready, the least loaded first: the load the node
   reported plus the calls this client runs on it.
2. Hubs listing the model with a ready node, round robin.

A route that fails before the head because it cannot serve the model now, is offline, or loses its
link, gives way to the next, up to 4 routes. The nodes that failed are excluded when a hub is tried.
After the head there is no retry.

### Errors

Errors take the shape of the API the agent called: OpenAI's `{"error": {"message", "type", "code"}}`
or Anthropic's `{"type": "error", "error": {"type", "message"}}`. On `GET /v1/models`, an agent that
sends `anthropic-version` or `x-api-key` gets Anthropic's shape. A body past `max_body_bytes` is
the exception: the HTTP server refuses it with a plain-text 413 before the API reads it.

| Case | Status |
| --- | --- |
| An unknown path or method | 404 |
| A missing or wrong key | 401 |
| A body that is not JSON, or has no `model` | 400 |
| A body past `max_body_bytes` | 413 |
| A model no peer serves, or served only with the other API | 404 |
| More than `max_inflight` calls | 429 |
| The model has no ready route, every route was unavailable, or no response began within `first_byte_timeout` | 503 |
| A node or hub answered a bad request (as a body past the node's `proxy.max_body_bytes`), not found or rate limited | 400, 404, 429 |
| `timeout` passed before the response began | 504 |
| Any other failure before the head | 502 |

An upstream's own error, once its head has come, is passed on as it is. A failure after the head
cuts the response short.

### Using it

In these examples, `127.0.0.1:9301` is the client's `api.address` and `<key>` an API key made on
its pages.

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
model_provider = "fit_agent"

[model_providers.fit_agent]
name = "fit agent"
base_url = "http://127.0.0.1:9301/v1"
env_key = "FIT_AGENT_API_KEY"
wire_api = "chat"   # or "responses" when the upstream serves /responses
```

then `export FIT_AGENT_API_KEY=<key>` and run `codex`.

curl:

```sh
curl http://127.0.0.1:9301/v1/chat/completions -H "Authorization: Bearer <key>" \
  -H "Content-Type: application/json" \
  -d '{"model": "qwen3-32b", "stream": true, "messages": [{"role": "user", "content": "Hello"}]}'
```

The client's overview page shows these with its own address and models.

## The pages

A role's `console` member serves its web pages, as a binding of its own on the listeners' engine:

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
users, sessions and lockouts. Passwords are checked with bcrypt on the console's own pool (4
running, 64 waiting, at most 2 checks at once); a task that waits past 30 seconds gets 504, and
one past the queue 503. After 5 failed logins from an address within 15 minutes, the address is
refused for 15 minutes (429, with `Retry-After`). A session is a cookie (`fit_agent_hub`,
`fit_agent_node` or `fit_agent_client`; HttpOnly, SameSite=Strict, Secure over TLS). Every API
request but a GET or HEAD carries the session's CSRF token in `X-Fit-Csrf`, and a request whose
`Origin` is another site is refused with 403. A request body is at most 1 MiB and its header 16 KiB.

What each role's pages show, in English or Chinese:

- **A hub**: an overview; its nodes, and for each one that allows hub management, its models, GPUs,
  actions, jobs and name; the models of its nodes; the clients linked to it; its links; its name.
- **A node**: an overview, with its name and description; its models, with a check and a test call
  each; its GPUs and their settings; its actions; its jobs; its links; its settings.
- **A client**: an overview, with the setups of [Using it](#using-it) filled in with its own address
  and models; the models it reaches and their routes; its API keys; its links; its name.

The links page sets the listener and the peers, and kicks a link.

The API under `/api/`:

| Endpoint | Answer |
| --- | --- |
| `POST /api/login` | Starts a session from `{username, password}`; answers `{user, csrf}`. |
| `POST /api/logout` | Ends the session; answers 204. |
| `GET /api/session` | The user and the CSRF token. |
| `GET /api/me` | `{user, identity}`, the role's identity; a client adds `api_url`, its API's URL. |
| `POST /api/rpc<method>` | Runs a method, as `/api/rpc/mesh/peers`, and answers its result as JSON, or `{"error", "code"}` with the code's HTTP status; 504 when the method's time ran out. |
| `POST /api/stream<method>` | Opens a stream method and answers Server-Sent Events: a data event per frame, `end` with the result, `error` with `{code, message}`. Closing the request cancels the stream. |

The pages call the same methods peers call, as a local caller allowed everything, and the methods
only the pages may call: the mesh's (`/mesh/peers`, `/mesh/dial`, `/mesh/undial`, `/mesh/kick`,
`/mesh/listen`); a hub's node table (`/hub/nodes`), managed calls (`/hub/node/call`,
`/hub/node/stream`) and name (`/hub/profile/set`); a node's settings (`/node/settings/get`,
`/node/settings/set`); a client's models, counters, keys and name (`/client/models`,
`/client/stats`, `/client/keys/list`, `/client/keys/create`, `/client/keys/delete`,
`/client/profile/set`).

A method has 10 seconds, the mesh's included; a node's or a client's method that changes the
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
| Method timeout | 10s; 30s for a node's or client's config change, 20s for a model check, 30s for GPU status and settings, 90s for a hub's managed call | fixed |
| Peers, models, API keys | 64, 256, 256 | fixed |
| Nodes in a hub's table | 1024 | `max_nodes` |
| Node calls at once | 256, and per model `max_inflight` | `proxy.max_inflight` |
| Node request body | 32 MiB | `proxy.max_body_bytes` |
| Node connect, first byte, idle, total | 10s, 5m, 5m, 30m | `proxy` |
| Upstream connections per host | 64, with 256 waiting | fixed |
| Upstream response body | 1 GiB | fixed |
| Model checks | 15s each; every 60s, 15s in error; 4 connections per host, 16 waiting | fixed |
| Jobs | 8 running, 200 kept, 1 MiB output each | fixed |
| Job run time | 1h | `settings.job_timeout` |
| Node's hardware and GPU reads | 2 running, 16 waiting; GPU status 25s, kept 5s | fixed |
| Client API body | 32 MiB, held whole | `api.max_body_bytes` |
| Client API connections, calls | 256, 256 | `api.max_conns`, `api.max_inflight` |
| Client first byte, total | 5m, 30m | `api` |
| Client API header size, header read, read, idle | 64 KiB, 10s, 2m, 2m | fixed |
| Client wait for a slow agent | 60s per write | fixed |
| Routes per call: client, hub | 4, 3 | fixed |
| Client counters | 1024 models, the rest as `other` | fixed |
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
  bodies as they come, with backpressure both ways through the streams' credit. Only the client's
  API holds a body whole, within `api.max_body_bytes`, to read the model, and a node holds one only
  to rewrite the model, within `proxy.max_body_bytes`.
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

## Migrating from agenthub

There is no import of agenthub's encrypted state file. Open each program's pages, or decrypt its
state, and write the members by hand, part by part:

| agenthub | fit |
| --- | --- |
| `id` | `id`: keep it, so that peers know the role as before |
| `name` | `name` |
| `listen` `{addr, password}` | `listen` `{address, password}`; leave it out when `addr` was empty |
| `peers[]` `{addr, password, role}` | `peers[]` `{address, password, role}` |
| `console_hash` | a user in `console.users`, as `{"username": "admin", "password_hash": "<the hash>"}`, or a new hash from `fit hash-password` |
| node `role_data.models` | `agent.node.models`, field for field |
| node `role_data.description` | `agent.node.description` |
| node `role_data.settings` | `agent.node.settings`: `refresh_interval_seconds` becomes `refresh_interval` (`"30s"`), `job_timeout_seconds` becomes `job_timeout` (`"1h"`); write `"gpu_sudo": false` when it was false, since fit's default is true |
| client `role_data.keys[]` `{id, name, prefix, hash, created_at, last_used_at, calls}` | `agent.client.api_keys[]` with `id`, `name`, `prefix`, `hash` and `created_at` as they are; drop `last_used_at` and `calls`, which the member does not take. Old keys keep working. |
| hub `role_data.nodes` (the remembered nodes) | dropped: the hub learns its nodes as they link |

The flags and variables:

| agenthub | fit |
| --- | --- |
| `-addr` | `console.address`; for a client, also `api.address`, since the API has its own address |
| `-data` and the state file | `admin.store` |
| `-log` | fit's `log`; the roles write to its `agent` logger |
| `AGENTHUB_PASSWORD` | gone; the store's password is `admin.store`'s |
| `AGENTHUB_CONSOLE_PASSWORD` | gone; console users |
| `AGENTHUB_NODE_DATA_DIR` | `settings.gpu_data_dir`; scripts see `FIT_AGENT_GPU_DATA_DIR` |

During the move, fit's roles and agenthub's programs link to each other: the wire protocol, the
path `/ws` and the authentication are the same. A body over 1 MiB needs fit at every role on its
way.

Once the config is written, `fit validate` checks it, and `fit store import -c <bootstrap file>
<config>` seals it into the store, replacing what the store held, so that what the pages change
from then on is kept. `admin` stays in the bootstrap file and is left out of what is imported; [the
console guide](console.md) has the rest.

## Changes from agenthub

- The three roles are one binary and one config; any of them run in one process, beside the
  gateway.
- The encrypted state file and its start password are gone; the config, kept in `admin.store`, holds
  everything.
- The console password is gone; the pages use console users with bcrypt hashes, sessions, CSRF
  tokens and lockouts, as `admin.console`.
- The mesh listener is a binding of fit's engine; a path other than `/ws` is refused with 403.
- The client's API has an address of its own, apart from the pages, and needs TLS off loopback.
- A hub forgets offline nodes at restart.
- API key use is not kept across restarts.
- Node settings are durations (`"30s"`, `"1h"`), not seconds.
- Upstream calls are made on fib's client, not `net/http`: `HTTP_PROXY` and `HTTPS_PROXY` are not
  honoured for model upstreams.
- New proxy timeouts (connect, first byte, idle, total) and node-wide limits (`proxy.max_inflight`,
  `proxy.max_body_bytes`).
- At most 8 jobs run at once.
- `limits.max_conns` defaults to 1024 (agenthub took 10000).
- Ports in addresses are numbers; a service name, as `:http`, is refused.
- A link whose pushes pile past 1024 is closed.
- At most 16 streams are open on a role's pages at once.
- Request bodies over 1 MiB stream between fit's roles, with `body_stream`.
