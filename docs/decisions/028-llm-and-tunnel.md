# 028: The LLM feature and the tunnel, as roles of fit's config

[English](028-llm-and-tunnel.md) | [简体中文](028-llm-and-tunnel.zh-CN.md)

Status: accepted (2026-11-01). The feature first named the agent was renamed `llm` the same day,
its client role `llm.proxy`, so that "agent" means only the AI agents that call it.

## Context

Users asked fit for two features beyond the gateway, each made of roles that run on several
machines and link to each other:

- **The LLM feature** shares AI models among machines. A *node* serves models from local or remote
  upstreams (OpenAI- and Anthropic-compatible APIs), a *hub* is where nodes and proxies meet, and a
  *proxy* offers agents one API that calls the models of the nodes it reaches.
- **The tunnel** reaches TCP services behind NAT. A *node* beside the services links to *hubs*
  over TCP, WebSocket or QUIC, and a *proxy* forwards local ports and web proxies through a hub
  to a node's service.

They asked for both to share fit's fib engine, to use fit's config format, with the roles a
process runs chosen by fit's config, and to keep what they store in fit's store; and for fit's
development rules to hold in full, with nothing left for later.

## Decision

- **Packages.** The LLM feature's roles are `llm/hub`, `llm/node` and `llm/proxy`; the tunnel's
  are `tunnel/hub`, `tunnel/node` and `tunnel/proxy`. What a family's roles share is
  under `llm/internal` and `tunnel/internal`; what both families share is in packages of fit's
  own: `login` (users, sessions, CSRF tokens and lockouts, shared with admin.console),
  `app` (the roles' registry and manager) and `app/console` (a role's web pages on fib). `app/standard`
  imports every role, and `cmd/fit` and `fit build` import it.
- **The config.** Two top-level members, `llm` and `tunnel`, each with a member per role:
  `llm.hub`, `llm.node`, `llm.proxy`, `tunnel.hub`, `tunnel.node`, `tunnel.proxy`. A role
  runs while its member is set, any number of them in one process, beside the gateway's listeners
  and routes or without any. Members are decoded as strictly as the rest of the config, in
  snake_case, with `doc` tags, defaults and bounds, so that `fit schema`, `fit validate`, the
  console's forms and the reference cover them. A config with roles and no routes needs no
  listener.
- **One engine.** Every role adds its bindings (its pages, its link listeners, a client's forwarded
  ports) to the listeners' engine, `listener.Server.Engine()`, and dials on it with handlers of its
  own, as the gateway's own connections are served. `limits.max_conns` and `max_pending_bytes`
  bound them with the rest. HTTP clients, as the LLM node's to its upstreams, dial through the
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
- **One store.** No role keeps a state file or store of its own: what a role keeps is in its
  member, and so in fit's store. `fit store import` loads a setup written in the members.
- **Logins are fit's.** A role's pages are behind `console` (a `RoleConsole`: address, TLS files,
  users with bcrypt hashes from `fit hash-password`, session limits), checked as admin.console's
  are, through package `login`; a role has no password or login scheme of its own.
- **The rules hold in full.** Nothing a role does blocks a fib worker: bcrypt, command execution,
  file reads and config edits run on bounded pools of their own, with timeouts; bodies to and from
  the LLM node's upstreams stream with fib's asynchronous client and `OnBody`, with backpressure
  both ways; every map, queue and buffer has a bound with a safe default; every exchange with the
  outside has timeouts. No role serves with `net/http` handlers, proxies with
  `net/http.Client`, or runs an engine of its own.

## Consequences

- One binary and one config run the gateway, the tunnel and the LLM feature, in any combination; one
  store keeps their settings; one log, one Admin API and one console show them.
- The roles' members carry passwords and keys. Kept in a plain file, they are as safe as the file;
  `admin.store` keeps them encrypted, which is why the pages' changes are meant to be kept there.
- Roles share the listeners' limits. A busy tunnel can use up `limits.max_conns`.
