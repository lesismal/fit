# 005: The Admin API on net/http, off by default, with configs kept as written

[English](005-admin-api.md) | [简体中文](005-admin-api.zh-CN.md)

Status: accepted, in I24 (2026-10-19).

## Context

I24 adds the Admin API, which reads the running config and replaces it, and a history of the
configs applied. It has to settle what serves the API, whether it is on when the config does not
ask for it, how it keeps a web page from using it, and what the history keeps.

## Decision

- **net/http serves the Admin API, on goroutines of its own.** The API is control plane: applying
  a config reads certificate files and binds listeners, which block, so it must not run on a fib
  worker (rule 4). net/http is in the standard library, so the core's dependencies do not change;
  fib would add nothing but a worker pool the API would have to leave anyway. Its resources are
  bounded: `admin.max_conns` connections (16; one past it is closed when accepted), a
  `max_config_bytes` body (4 MiB; past it, 413), 64 KiB of header, timeouts of 10s for the header,
  60s for the body and the response, and 60s idle. One config is applied at a time.
- **Off unless the config has `admin`.** `"admin": {}` turns it on at 127.0.0.1:2019. Caddy has it
  on by default; for fit, a second process on one host would then fail to start, and a listener
  nobody asked for is a surprise in a proxy's config. The address must be a loopback one or a Unix
  socket (`unix/<path>`, made 0600) unless `token_file` is set, so that a remote API always asks
  for a token.
- **Web pages are refused.** A request with `Origin` gets 403, so that a page cannot post a config
  to localhost; `POST /load` takes only `application/json`, which a page cannot send without a
  preflight the API does not answer; and on a TCP address without a token, the `Host` field must
  name a loopback host, which a page that reaches 127.0.0.1 through DNS rebinding does not.
- **The token is read from a file**, `token_file`, never written in the config: `GET /config`
  returns the config, and a token in it would be returned too.
- **Configs are kept as written**, in memory: `GET /config` returns the bytes that were applied,
  from the file or the API, not a re-encoding. The last `admin.history` configs (10) are kept,
  numbered with the versions `runtime.Runtime.Swap` gives their graphs. They are not written to
  disk; a restart starts from the file.

Update, I25 (2026-10-19):

- **A path into the config is a JSON pointer** (RFC 6901) after `/config`, as in
  `/config/upstreams/0/addresses`, written with `~1` for a `/` in a key. `PUT` sets the value there
  (replacing it, adding a member, or appending to an array at `-`), `PATCH` merges the body into
  it as a JSON merge patch (RFC 7396), which can change a few fields of a listener without
  restating it, and `DELETE` removes it. Every change applies a whole new config, checked as any
  other, so one that leaves the config invalid is refused with its errors.
- **An edited config is written out again**, two spaces to a level, its members in their order and
  its numbers as written. Only a config sent whole (`POST /load`, `PUT /config`) is kept byte for
  byte.
- **The ETag is a hash of the config as written** (the first 96 bits of its SHA-256), not its
  version: versions start again at 1 when fit restarts, and a rollback gives an old config a new
  version. `If-Match` is checked, and the change made from the running config, under the lock
  that orders applies, so concurrent writers lose nothing; one whose ETag is stale gets 412.
- **`POST /rollback/{n}`** applies version `n`'s config again as a new version, while `n` is in
  the history.

Update, I64 (2026-10-22):

- **The dashboard is served by the API itself**, at `/dashboard/` unless `admin.disable_dashboard`:
  HTML, CSS and JavaScript embedded in the binary, with a Content-Security-Policy that allows only
  its own files and requests to its own origin. It reads `GET /status` (new: listeners,
  connections, upstreams and their backends' health, certificates), `/config/effective` and
  `/versions`, and sends nothing else; a test reads its files and fails on any other method.
- **Two narrow exceptions to the guard.** The dashboard's files are served without the token,
  since they hold no data and the page asks for the token, keeping it in the tab's
  `sessionStorage` and sending it as `Authorization`; every endpoint still asks for it. And a
  `GET` or `HEAD` whose `Origin` is the API's own (scheme and the request's `Host`) is let through:
  a browser sends `Origin` on some same-origin reads, a page on another origin still gets 403, as
  every write with any `Origin` does. The loopback `Host` check is unchanged, so the dashboard is
  reached at a loopback name, or anywhere with a token.

## Consequences

- A config loaded through the API lasts until the next SIGHUP, which applies the file again, or a
  restart. Writing what the API loaded back to disk (as Caddy's autosave does) can come with the
  file provider (I27).
- Changes to `admin` and `access_log` take effect on a restart; applying one says so.
- The history holds at most `history × max_config_bytes` of API configs (40 MiB at the defaults),
  and the file's configs at their size.
