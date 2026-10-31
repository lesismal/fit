# 028: The agent and the tunnel, as roles of fit's config

[English](028-agent-and-tunnel.md) | [简体中文](028-agent-and-tunnel.zh-CN.md)

Status: accepted (2026-11-01).

## Context

Two programs built on fib lived in repositories of their own, each with its own engine, config
format and encrypted file:

- **agenthub** shares AI models among machines. A *node* serves models from local or remote
  upstreams (OpenAI- and Anthropic-compatible APIs), a *hub* is where nodes and clients meet, and a
  *client* offers one API that calls the models of the nodes it reaches. They link over a WebSocket
  mesh with a challenge-response on a shared password. Every setting lived in an encrypted state
  file (scrypt, AES-GCM) edited from web pages; it had no config file. It was written against an
  old fib (`fib/go`), served a `net/http` ServeMux on fib, and its node proxied with a blocking
  `net/http.Client`.
- **fibhub** reaches services behind NAT. A *pod* beside the services links to *servers* over TCP,
  WebSocket or QUIC, and a *client* forwards local ports and web proxies through a server to a
  pod's service. Its config was JSON in camelCase with a `mode` naming the role; what its pages
  changed was kept in a store of its own (`FIBSTORE`, Argon2id, XChaCha20-Poly1305).

Users asked for both to become part of fit: to share fit's fib engine, to use fit's config format,
with the roles a process runs chosen by fit's config, and to keep what they store in fit's store.
They also asked for fit's development rules to hold in full, with nothing left for later.

## Decision

- **Packages.** agenthub's roles are `agent/hub`, `agent/node` and `agent/client`; fibhub's are
  `tunnel/server`, `tunnel/node` (fibhub's pod) and `tunnel/client`. What a family's roles share is
  under `agent/internal` and `tunnel/internal`; what both families share is in packages of fit's
  own: `login` (users, sessions, CSRF tokens and lockouts, shared with admin.console),
  `app` (the roles' registry and manager) and `app/console` (a role's web pages on fib). `app/standard`
  imports every role, and `cmd/fit` and `fit build` import it.
- **The config.** Two top-level members, `agent` and `tunnel`, each with a member per role:
  `agent.hub`, `agent.node`, `agent.client`, `tunnel.server`, `tunnel.node`, `tunnel.client`. A role
  runs while its member is set, any number of them in one process, beside the gateway's listeners
  and routes or without any. Members are decoded as strictly as the rest of the config, in
  snake_case, with `doc` tags, defaults and bounds, so that `fit schema`, `fit validate`, the
  console's forms and the reference cover them. A config with roles and no routes needs no
  listener.
- **One engine.** Every role adds its bindings (its pages, its link listeners, a client's forwarded
  ports) to the listeners' engine, `listener.Server.Engine()`, and dials on it with handlers of its
  own, as the gateway's own connections are served. `limits.max_conns` and `max_pending_bytes`
  bound them with the rest. HTTP clients, as the agent node's to its upstreams, dial through the
  upstream engine fit's pools use. A role's QUIC is fib's `http3/quic` on a UDP binding of the same
  engine.
- **Applying a config.** `app.Manager` is moved to every config applied, after the listeners, on
  the control plane: it starts each role a config sets that is not running, calls `Apply` with the
  member on each that is, and stops each one the config leaves out. A role moves to its new member
  without dropping what is unchanged: its links, tunnels and sessions. A role that fails to apply
  runs as it did, and its error is a warning of the config's, as a change that takes effect at a
  restart is; at start, it stops fit. A role made of parts that bind on their own, as the tunnel's
  listeners and forwards, applies the parts that bind and reports the others in that warning,
  rather than keep a whole old member for one taken port. `app.Check` runs when a config is compiled, so `fit validate`
  and `fit store import` check members beyond decoding.
- **What the pages change is written back into the config.** A role's pages change its member
  with `Host.Edit`, which applies the running base config with the member replaced, by JSON
  pointer, through `admin.Configs.EditMember`, its source the role's ID. The change runs as any
  other config does, is in the history and can be rolled back, and with `admin.store` is written to
  the store. Without a store, it runs until a restart or a reload of the files, and says so in a
  warning. What a role generates and must keep, as a QUIC certificate, an ID or an API key's hash,
  is written into its member the same way. Edits run one at a time on the manager's goroutine,
  never on a fib worker, and at most `app.MaxEdits` wait.
- **The stores are merged.** agenthub's state file and fibhub's store are gone: what they held is
  in the roles' members, and so in fit's store. `fit store import` moves an existing setup in, from
  a config written in the new members.
- **Logins are fit's.** A role's pages are behind `console` (a `RoleConsole`: address, TLS files,
  users with bcrypt hashes from `fit hash-password`, session limits), checked as admin.console's
  are, through package `login`. agenthub's console password and fibhub's `webAuth` are gone.
- **The rules hold in full.** Nothing a role does blocks a fib worker: bcrypt, command execution,
  file reads and config edits run on bounded pools of their own, with timeouts; bodies to and from
  the agent node's upstreams stream with fib's asynchronous client and `OnBody`, with backpressure
  both ways; every map, queue and buffer has a bound with a safe default; every exchange with the
  outside has timeouts. agenthub's `net/http` handlers and client, and its own engines, are
  rewritten on fib.

## Consequences

- One binary and one config run the gateway, the tunnel and the agent, in any combination; one
  store keeps their settings; one log, one Admin API and one console show them.
- The wire protocols are kept, so that a role of fit links to the programs it replaces while a
  setup moves over; names on the wire (a WebSocket path, an ALPN) keep their old values.
- The roles' members carry passwords and keys. Kept in a plain file, they are as safe as the file;
  `admin.store` keeps them encrypted, which is why the pages' changes are meant to be kept there.
- Roles share the listeners' limits. A busy tunnel can use up `limits.max_conns`.
