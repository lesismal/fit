# 033: Cluster management: one fit manages many, through the APIs they already have

[English](033-cluster-management.md) | [简体中文](033-cluster-management.zh-CN.md)

Status: accepted (2026-11-06) for its principles and for I140; the choices marked *pending review*
stand until the user rules on them. The track is I140–I151 of the
[development plan](../dev-plan.md#cluster-management).

## Context

A site that runs several fits, at the edge of a few regions, beside each service, or one per
customer, changes their configs one at a time today: through each one's console, its Admin API, or
its files. A route to add everywhere, a certificate to rotate, an upstream to drain, means as many
edits, with no view of which fits run what, which drifted, and which a change reached. The track
lets one fit, the manager, hold the address and an administrator's credentials of the others, its
members, and show them, group them and change their configs together, previewed, checked by each
member and rolled out in stages.

What a manager needs from a member decides how much the member must change. Each fit already has an
Admin API (a bearer token), and a console that passes the Admin API on behind a user name and a
password; every config change goes through one apply path, with ETags and `If-Match`, a history at
`/versions` and rollback. What it lacked: a way to tell who a fit is and what it offers, a check of a
config that changes nothing, a login a program can drive, and a console that withstands guessing
once it is reachable on a network.

## Decisions

The track's principles, in the order they bind:

- **The manager is not in the data path.** Members serve from their own configs whether or not the
  manager runs. No consensus among members and no runtime state shared between them.
- **Members change as little as possible.** The manager speaks a member's console (user name and
  password, then the session's cookie and CSRF token) or its Admin API (a bearer token). Every
  change on a member goes through that member's own apply path with `If-Match`, lands in its
  `/versions` and can be rolled back there.
- **Credentials stay in the manager's store**, encrypted, written once in the pages and shown
  masked; members' certificates are verified, by a CA or a pinned SHA-256 fingerprint.
- **A person applies.** A change to many members is diffed per member, checked by each member's
  dry run, confirmed, and applied in batches with health gates and rollback. Drift is reported, and
  fixed by a click; no unattended sync.
- **Nothing blocks a worker** ([rule 1](../rules.md#1-never-block-a-worker)): the manager's calls
  go out on fib's asynchronous client, bounded in concurrency, each with timeouts, every answer,
  cache, history and audit entry bounded ([rule 11](../rules.md#4-timeouts-and-limits)).
- **Off by default**: no `admin.cluster`, no Cluster menu, no outgoing calls.

What I140 adds to every fit, so that a manager can manage it:

- **`GET /node`** in the Admin API, and `GET /api/node` (also `/api/m/admin/node`) through the
  console: `id`, `id_kept`, `hostname`, `fit_version`, `fib_version`, `go_version`, `os`, `arch`,
  `started`, `uptime_seconds`, `config_version`, `config_etag`, `config_applied`, `config_source`,
  `roles` (the roles running), `schema_version`, `schema_digest` (`sha256:` of the JSON Schema the
  binary generates, so that two fits with the same digest take the same configs) and
  `capabilities`. The answer carries the running config's `ETag` and `Fit-Config-Version`.
  - **The node ID** is `admin.node_id` when the config sets it; otherwise a random UUID made once
    and kept in the store's section `node`, or, without a store, in a file beside the config
    (`fit.node-id` beside `fit.json`, `fit.node-id` in a directory of config files), written
    through a temporary file renamed over it. A fit given a store takes the ID its file had. With
    nowhere to keep it (a config on standard input, a directory that cannot be written), the ID
    lasts until fit stops, `id_kept` says `memory`, and fit warns at start. `id_kept` is `config`,
    `store`, `file` or `memory`. Only a fit with `admin` makes an ID: one without serves no
    `/node`, and writes nothing beside its config.
  - **Capabilities** are names a manager checks before it calls what they name; a name keeps its
    meaning, and a change to what an endpoint answers takes a new name: `node`,
    `config.if_match`, `config.dry_run`, `config.text`, `config.effective`, `config.check`,
    `config.versions`, `config.transfer`, `status`, `metrics`, `cache`, `logs`, `tokens`,
    `media`, always; `store` with `admin.store`; `console` while `admin.console` runs.
- **A dry run.** `PUT`, `PATCH` and `DELETE /config[/<pointer>]`, `POST /load` and
  `PUT /config/text` take `?dry_run=1` (or `true`; `0`, `false` or none apply; anything else is
  `400`). The request takes the whole path an apply takes, under the same lock: `If-Match` (`412`
  when stale), the edit, standardizing, the store's refusal of `admin`, merging what providers
  provide, loading with the bootstrap file's `admin`, compiling with every module provisioned, and
  the new listeners' addresses tried as the apply's bind tries them, a plain bind closed at once
  (`listener.Server.Plan`). It answers as the apply would: the same errors, word for word, at the
  same status; or `200` with `"dry_run": true`, the running `version` and `etag`, `new_etag` (the
  ETag the config would have, which an apply of the same body then gets), `unchanged` when it is
  the running config, the listeners it would add (their addresses as written) and remove, and the
  warnings an apply would give (changes that wait for a restart, renamed names), with
  `Fit-Dry-Run: 1`. Nothing changes: the running config, its version, the history, the store and
  the counts of reloads. The console logs it as a check, not as an apply.
  - Compiling takes no engine, as `fit validate`'s does: upstream pools dial and probe nothing.
    What a dry run cannot tell: a role's own failure when it applies (the agent's and the tunnel's,
    which an apply reports as warnings), and a race for an address between the check and the
    apply.
- **Logins a program can use.** `POST /api/login` answers `{"user", "csrf"}` with the session's
  cookie, as `GET /api/session` does; a program keeps the cookie and sends `csrf` in `X-Fit-Csrf`
  on every request but a GET or a HEAD. A refused login is `429` with `Retry-After` and
  `{"error", "retry_after"}` in seconds.
- **Failed logins slowed, per address and per user.** An address that fails 5 times in 15 minutes
  is refused for 15 minutes, as before; each further lockout it earns before it has been quiet for
  15 minutes doubles, up to an hour. A user whose password fails 10 times in a row, from any
  addresses, has its checks refused for 2 seconds, doubling with each further failure up to 5
  minutes; the right password once the wait ends works and ends the count, and so do 15 quiet
  minutes. Names that are no user's share one count, so the table stays as small as the users. A
  password change from the Account page is counted the same way.
- **Pages.** The Account menu has **This node**: the ID (copied with a click), where it is kept, the
  host, the versions, the start, the running config and its roles, the schema's digest and the
  capabilities. The config pages have **Check** beside **Save and apply**: the same request with
  `?dry_run=1`, its errors shown where a save's are.

## Pending review

Decided while building I140 (2026-11-06); each stands until the user rules on it.

1. **Where the node ID lives without a store.** A file beside the config, as the roles' kept files
   are; `admin.node_id` for where nothing lasts. Alternatives: the user's state directory
   (`$XDG_STATE_HOME`), or no ID without a store.
2. **The ID's form.** A random UUID; `admin.node_id` takes 1 to 64 letters, digits, `.`, `_`, `-`.
   Alternative: the host name, which changes with the host and collides in containers.
3. **The dry run's reach.** Every config write that takes a body or a pointer; not rollback,
   reconcile or import, whose configs were applied before or are checked by their own flows.
   Alternative: all of them.
4. **The dry run tries addresses.** A plain bind closed at once, so that a taken address is refused
   as the apply refuses it. Alternative: no bind, and a taken address found only on apply.
5. **Per-user slowing refuses, rather than delays.** A `429` with the wait, as the address lockout
   answers; holding a request open for a delay would hold the login pool. A user is kept out for
   5 minutes at most after a guess; an attacker with many addresses can keep it that way, which
   the right password, once the wait ends, gets through.
6. **Address lockouts double.** The first stays 15 minutes, so a typo or two costs no more than
   before; a guesser that comes back is slowed to 5 guesses an hour.
7. **`/api/node` at the console's top level**, beside `/api/m/admin/node`, as the plan names it.

Open questions of the plan, answered as proposed until reviewed:

1. Members are reached by the console's user name and password, or by the Admin API's token; both.
2. Credentials need `admin.store` on the manager; no plain file.
3. I143 opens a member's pages with the manager's bundle, read-only for a version it does not know.
4. Templates own whole members and named elements; an unnamed route cannot be shared.
5. No unattended sync; drift is fixed by a person's rollout.
6. Members linking out (I149) after templates and rollouts (I145–I146); the tunnel reaches members
   behind NAT until then.
7. **Upgrading members' binaries (I151) is not taken up** in this track: it needs a graceful
   restart that hands listening sockets on, which fit does not have; packages, containers and
   orchestrators upgrade fits, and the overview's version column shows where each stands.

## Rejected

- **An agent on each member, or a protocol of its own.** It would be one more thing to run, secure
  and upgrade on every host; the console and the Admin API already carry everything a manager
  needs.
- **Consensus or shared runtime state among members** (rate-limit counters, caches, sessions): a
  data-plane track of its own, with failure modes the manager's design avoids.
- **The manager in the data path**, as a front for its members: a manager down would cost traffic.
- **A dry run that skips compiling**, or one through a second Configs: modules' params are checked
  only when provisioned, and the apply's lock, `If-Match` and providers' parts would not be the
  apply's.
- **Locking a user out outright after failures**: one guesser could keep every administrator out.
