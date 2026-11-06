The Admin API reads and replaces the running config: `GET`, `PUT`, `PATCH` and `DELETE` on
`/config/` and a JSON pointer into it, `POST /load` for a whole config, ETags against lost updates,
the configs applied (`/versions`) and rollback to one. A change sent with `?dry_run=1` is checked
through the whole path an apply takes, loaded, compiled and its new listeners' addresses tried, and
answered as an apply would answer it, with nothing applied. `GET /node` says who this fit is: its
node ID (`node_id`, or one made once and kept in the store or beside the config), host name, fit's
and fib's versions, start time, running config version and ETag, roles, the schema's digest and the
capabilities a manager of many fits checks before it calls an endpoint. It also serves metrics at
`/metrics`, the effective config with providers merged, and a read-only dashboard. It listens on 127.0.0.1:2019
unless told otherwise. To listen anywhere but a loopback address or a Unix socket it needs
`token_file`, and `cert_file` and `key_file` to be served over TLS (1.2 or later), so that the
token never crosses a network in the clear; TLS may be set on loopback too. The certificate files
are read again when they change, so that a renewed certificate is served without a restart, and
`client_ca_file` asks every client for a certificate that CA issued, besides the token.
`fit check` reaches such an API over https, trusting the certificate `cert_file` holds when it
can read it; its `-ca`, `-server-name`, `-cert` and `-key` flags verify it otherwise and present
a client certificate. It is off when `admin` is left out. Read when fit starts. `mptcp` has it listen over Multipath TCP
where the kernel offers it (Linux 5.6+, with `net.mptcp.enabled`), falling back to plain TCP;
off by default, and a Unix socket has no paths.

A connection that has not made a request with the token (with no `token_file`, any request) is
closed 5s after it is accepted, whatever it is doing, reading a request or writing an answer;
the dashboard's files it may fetch are answered with `Connection: close`, so that requests queued
on it do not hold it. `max_conns` keeps a quarter of the connections for those that have; when
they are taken, a new connection closes the oldest unauthenticated one of the client holding the
most of them, rather than being refused, so that clients that only reconnect cannot keep an admin
out.

`admin.store` keeps the config encrypted in a file of its own, the store: the file fit is started
with then holds `admin`, and the store the rest, which fit runs from and writes on every change.
When the store does not exist yet, the rest of the file is its first content. `fit store export`
and `fit store import` read and write it. `admin.console` serves the console, web pages behind a
user name and password from which every part of the config but `admin` is changed, with a form
made from the schema and a JSON editor; off a loopback address it needs `cert_file` and
`key_file`. Its `mptcp` does the same for the console's listener. See
[the console and the store](../console.md) and
[decision 027](../decisions/027-console-and-store.md). `admin.assistant`, off when left out, adds
the workbench's assistant: the model APIs it calls (Anthropic, OpenAI, Gemini, Azure OpenAI, Ollama,
any Chat Completions server, or this fit's `llm.proxy`), its limits and each console user's level;
the Settings page keeps them, with the keys, in the store; its `mcp` serves the assistant to
outside agents over MCP at `/mcp` on the console's address, with tokens users make on the MCP page. See
[the assistant](../console.md#the-assistant) and [decision 031](../decisions/031-assistant.md).
`admin.cluster`, off when left out, makes this fit a manager of others: their addresses and
credentials are kept in the store, each one's `GET /node`, `/status` and `/config/check` read every
`poll_interval` through its console or its Admin API, and their states served in the console as the
module `cluster`; it needs `store` and `console`. See
[managing other fits](../console.md#managing-other-fits) and
[decision 033](../decisions/033-cluster-management.md).

```json
{
  "admin": {
    "address": "127.0.0.1:2019",
    "history": 20,
    "store": {"path": "/var/lib/fit/fit.store", "password_env": "FIT_STORE_PASSWORD"},
    "console": {
      "address": "127.0.0.1:2020",
      "users": [{"username": "admin", "password_hash": "$2a$10$sKGBAOOCI4p4WWJcVCuAH.ivT8pNy2DFbs6pd8xmtNEZ/5L1SeLvm"}]
    }
  },
  "routes": [{"match": {"host": ["example.com"]}, "handler": {"module": "respond", "params": {"body": "hello"}}}]
}
```
