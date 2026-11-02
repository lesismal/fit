# The console and the store

[English](console.md) | [简体中文](console.zh-CN.md)

Config examples, in .conf, in [fit-examples](https://github.com/lesismal/fit-examples): [console](https://github.com/lesismal/fit-examples/blob/main/console/fit.conf), [admin](https://github.com/lesismal/fit-examples/blob/main/admin/fit.conf).

fit can keep its config encrypted in a file of its own, the **store**, and serve a **console**: web
pages behind a user name and password from which every part of the config is read and changed. The
two are `admin.store` and `admin.console`, beside the [Admin API](reference/config.md#admin-api) in
`admin`; either works without the other, and together a config changed in the console runs at
once, is sealed in the store, and is what fit starts from next time. The design is
[decision 027](decisions/027-console-and-store.md).

- [Two files](#two-files)
- [The store](#the-store)
- [fit store export and import](#fit-store-export-and-import)
- [The console](#the-console)
- [Users and fit hash-password](#users-and-fit-hash-password)
- [Logins and sessions](#logins-and-sessions)
- [The pages](#the-pages)
- [Limits and timeouts](#limits-and-timeouts)
- [Roles' pages](#roles-pages)
- [The workbench's modules](#the-workbenchs-modules)

## Two files

A file cannot hold the password that opens it, so a config with a store is in two files:

- the **bootstrap file**, the one `fit run -c` names, in clear text and in any of fit's formats. It
  holds `admin`: the Admin API, `admin.store` (where the store is and its password) and
  `admin.console` (where the console listens and who may log in);
- the **store**, sealed with the password. It holds the rest of the config, everything but
  `admin`: the **base config** fit runs from, before providers are merged into it.

```json
{
  "admin": {
    "address": "127.0.0.1:2019",
    "store": {"path": "/var/lib/fit/fit.store", "password_env": "FIT_STORE_PASSWORD"},
    "console": {
      "address": "127.0.0.1:2020",
      "users": [{"username": "admin", "password_hash": "$2a$10$sKGBAOOCI4p4WWJcVCuAH.ivT8pNy2DFbs6pd8xmtNEZ/5L1SeLvm"}]
    }
  },
  "listeners": [{"name": "web", "address": ":8080"}],
  "routes": [{"match": {"host": ["example.com"]}, "handler": {"module": "respond", "params": {"body": "hello"}}}]
}
```

The first time fit starts with this file there is no store yet: `listeners` and `routes` are its
first content, the store is written at once, and fit says
`the config of fit.json is kept in the store /var/lib/fit/fit.store from now on`. From then on the
store wins: what the bootstrap file sets beside `admin` is left out, with a warning naming the
members left out, and only `admin` is read from it. To start again from the file, remove the store.

`admin`'s settings, the store's and the console's included, are read when fit starts; a change to
them takes effect when fit restarts.

## The store

| Field | Default | |
| --- | --- | --- |
| `path` | | Required. The store's file. |
| `password` | | The password, written in the bootstrap file. |
| `password_file` | | A file holding the password, its surrounding spaces trimmed. |
| `password_env` | | An environment variable holding the password. |

Exactly one of `password`, `password_file` and `password_env` is set; an empty file or an unset or
empty variable stops fit from starting. A password written in the bootstrap file protects a store
copied away from it, in a backup or a disk image, not one read on the same machine; the other two
keep it out of the file, as `token_file` keeps the Admin API's token out.

**What is kept.** Every config applied, from the console, the Admin API (`PUT`, `PATCH`, `DELETE`,
`POST /load`), a rollback or a role's pages, is written to the store once it runs. A config that
cannot be written still runs, and its result carries the warning
`the config runs, but was not saved to the store`. SIGHUP reads the store again and applies it, as
after `fit store import`; `-watch`, which watches config files, is refused with a store.

**`admin` is never in the store, and never shown.** The bootstrap file's `admin` is put into every
config as it loads, after providers are merged, but not into the base config, so `GET /config`,
`/versions` and `/config/effective` hold no password, hash or key path. A config sent with `admin`
is refused while there is a store: `admin` comes from the bootstrap file alone.

**The format** is fit's own, `fitstore` version 1: a 59-byte header (the magic `FITSTORE`, the
version, Argon2id's settings, a salt and a nonce), then the config as JSON sealed with
XChaCha20-Poly1305, the header as its additional data. The key is derived from the password with
Argon2id (time 3, 64 MiB, 4 threads) once, when fit starts; each write takes a new nonce, so writes
cost no derivation. A write goes to a temporary file beside the store, made 0600, synced and renamed
over it, so a crash leaves the old store or the new one, never half of one.

**A store that does not open**, by a wrong password or a changed byte, stops fit from starting, and
is not overwritten. Losing the password loses the store: keep a copy made with `fit store export`
somewhere safe.

## fit store export and import

```text
fit store export -c <bootstrap file>
fit store import -c <bootstrap file> <config file or directory>...
```

Both read the bootstrap file's `admin.store` for the store and its password; `-c` is `fit.json`
when left out, as for `fit run`.

- **`export`** prints the config the store holds, as JSON, `admin` aside. A store not written yet is
  an error.
- **`import`** reads the files given, in any of fit's formats and as many as `fit run` takes,
  checks the config they make as `fit validate` does, with the bootstrap file's `admin` put in, and
  seals it as the store, replacing what it held, with a new salt. A config that sets `admin` is
  refused: `admin` stays in the bootstrap file. The old store need not open, so import is also how
  the password changes.

A running fit does not see an import until it reads the store again: send it SIGHUP.

To change the password:

```sh
fit store export -c /etc/fit/fit.json > /root/fit-config.json
# change admin.store's password, password_file or password_env, or what it holds
fit store import -c /etc/fit/fit.json /root/fit-config.json
rm /root/fit-config.json
kill -HUP "$(pidof fit)"    # or restart fit
```

## The console

| Field | Default | |
| --- | --- | --- |
| `address` | `127.0.0.1:2020` | The host and port the console listens on. |
| `cert_file`, `key_file` | | PEM files with the console's certificate (and intermediates) and its key; with them the console is served over TLS 1.2 or later. Set both or neither. |
| `users` | | Required. Who may log in: at least one, each a `username` and a `password_hash`. |
| `session_timeout` | `30m` | How long a login lasts without a request. |
| `session_lifetime` | `12h` | How long a login lasts at most, however busy. |
| `max_sessions` | 64 | The most logins kept at once; one more ends the oldest. |
| `max_conns` | 32 | The most connections open at once; one past it is closed as soon as it is accepted. |

**An address that is not a loopback one needs `cert_file` and `key_file`**, so that passwords never
cross a network in the clear; `fit validate` refuses one without. On a loopback address TLS is
optional, as for a console reached through an SSH tunnel.

The console serves the **workbench**: one page with a menu on the left, a tree of groups (Gateway,
Tunnel, LLM, System), their modules and each module's pages, and the page picked on the right. Its
config module edits the config; and every [LLM](llm.md) and [tunnel](tunnel.md) role running in the
same fit shows beside it as a module of its own, with every page the role's own console has, behind
the one login.

The console is a binding of its own on the listeners' fib engine, as the roles' pages are, not one of
the config's listeners: a config whose listeners fail to bind leaves it reachable to fix them. Its
requests are served on fib's workers and nothing there blocks: a password is checked, and each Admin
API call made, on the console's own bounded pool. It calls the Admin API's handlers in process,
behind its own logins, so both apply configs by the one path and with the same checks; the Admin API
keeps its own address, `token_file` and rules, and listens too. The console needs no store: without
one, it changes the running config as the Admin API does, in memory, and a restart starts from the
config files again.

## Users and fit hash-password

A user is a name and a bcrypt hash; a password is never written in clear text. `fit hash-password`
prints a password's hash. From a terminal it asks for the password twice, showing a `*` for each
character typed, and stops if the two differ; otherwise it reads the first line of standard input.
It never takes the password from its arguments, which other users of the machine could see:

```sh
$ fit hash-password
Password: ****************************
Confirm password: ****************************
$2a$10$...
$ echo 'correct horse battery staple' | fit hash-password
$2a$10$...
```

`-cost` sets bcrypt's cost (10 by default; each one more doubles a check's time). A password is at
most 72 bytes, bcrypt's limit, and not empty. The same hashes serve `basic_auth`. `fit validate`
refuses a `password_hash` that is not a bcrypt hash, an empty user name and a name listed twice.

## Logins and sessions

- **Checking a password.** A name that is not a user is checked against a dummy hash, so the time
  taken does not tell names apart; at most two checks run at once, the rest wait.
- **Lockouts.** A client address that fails 5 times in 15 minutes is refused for 15 minutes, with
  `429` and `Retry-After`. The table of addresses holds 4,096 at most.
- **Sessions.** A login gives a session: 32 random bytes in the cookie `fit_console_<port>` (the console's port, so that consoles
  of several fits on one host keep their logins apart), `HttpOnly`,
  `SameSite=Strict`, and `Secure` over TLS. The console keeps only its SHA-256, and ends it after
  `session_timeout` without a request, after `session_lifetime` in all, at logout, or when
  `max_sessions` is passed and it is the oldest. Sessions are in memory: a restart logs everyone
  out.
- **Other web pages.** Every request but a read carries the session's CSRF token in `X-Fit-Csrf`,
  or gets `403`; a request whose `Origin` is not the console's own gets `403`. Pages are sent with a
  Content-Security-Policy that allows their own files only and `frame-ancestors 'none'`,
  `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, and over TLS
  `Strict-Transport-Security`; API answers carry `Cache-Control: no-store`.

The `admin` logger records who logged in and from where, each failed login, each logout, and each
change a user made with the config version it applied.

## The pages

After logging in, the workbench. Its menu can be filtered, folded to icons, and its groups and
modules opened and closed; the theme (light, dark and three more) and the language (English or
Chinese) are picked at the top right. Each page has its own URL, `#/<module>/<page>`.

- **Gateway**: an overview of the running config (status, routes, services, upstreams and their
  health, certificates, the version), then the parts of the config the gateway is made of, each a
  page: listeners, the reverse proxy's routes and upstreams, the forward proxy's routes, layer 4
  services, TLS and certificates.
- **Tunnel** and **LLM**: a module for each role running, as `tunnel.hub` or `llm.node`, with the
  role's pages.
- **System**: the rest of the config, part by part; the whole config as JSON; and the versions
  applied (`admin.history` of them), where each came from, each one's text, and a button to roll
  back to it.

The config's pages are forms made from fit's JSON Schema, embedded in the binary: every object,
list and module typed by its schema, with each field's doc. A field fit adds is edited with no change
to the console. `admin` is not in the forms. Every page edits the same draft of the config: **Save
and apply** sends it whole, with `If-Match`; the result says which version it was applied as, with
any warnings. A config with errors is refused and its errors are shown at their paths. When the
config was changed elsewhere since the page read it (`412`), the page says so: discard the changes to
read it again, or copy them from the JSON page first.

For scripts, the console's own endpoints are `POST /api/login` (a JSON body
`{"username", "password"}`, answered with the user and the CSRF token), `POST /api/logout`,
`GET /api/session` and `GET /api/workbench` (the user and the IDs of the modules served). Each
module's endpoints are under `/api/m/<module>/`: the config's module, `admin`, serves
`GET /api/m/admin/schema` and, under `/api/m/admin/`, the Admin API's endpoints, behind a session
instead of the token, as `GET /api/m/admin/config`; a role's module serves its pages' API, as
`POST /api/m/llm.hub/rpc/hub/nodes`.

## Limits and timeouts

| | |
| --- | --- |
| Connections | `max_conns`, 32 |
| Sessions | `max_sessions`, 64, the oldest ended past it |
| A request's header | 16 KiB, read within 10s |
| A request's body | `admin.max_config_bytes` and 64 KiB, 4 MiB and 64 KiB by default, read within 30s |
| Writing a response | 30s; a role's streams are bounded by each wait for the browser instead, 30s |
| An idle connection | 2m |
| Password checks at once | 2 |
| Admin API calls and role methods at once | 4 running, 64 waiting, each waiting at most 30s; past them `503` |
| Failed logins | 5 an address in 15 minutes, then refused for 15 minutes; 4,096 addresses kept |

## Roles' pages

The [LLM feature](llm.md)'s and the [tunnel](tunnel.md)'s roles can also have pages of their own,
each set by the role's `console`: the same fields as `admin.console`, but `address` is required, and
served on the listeners' fib engine, as a role on a machine without `admin.console` needs. Each
serves the same workbench, with the role's module alone in its menu. Their users, logins, lockouts,
sessions and the TLS rule work as the console's do, with tables of their own. What a role's pages
change, on its own console or as a module of `admin.console`'s workbench, is written back into its
member of the config, applied by the same path as the console's changes, so it shows in the
Versions page and, with a store, is sealed in the store. The config's own pages edit the same
members as a whole: a role's pages are for working with the role, the config's for the config.

## The workbench's modules

The pages are plain HTML, CSS and JavaScript in `app/workbench/web`, embedded in the binary and
served from memory, with nothing fetched from elsewhere. `core/` holds the library every module uses
(`ui.js`, `ui.css`) and the workbench (`workbench.js`); each module is a script that registers itself
with `WB.module`, naming the server module whose API it calls, the group it goes under and its
pages. A module shows when the console serving the page serves its server module.
