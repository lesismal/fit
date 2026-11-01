# The console and the store

[English](console.md) | [简体中文](console.zh-CN.md)

Config examples, each in .conf, YAML and JSON: [console](../examples/console/fit.conf), [admin](../examples/admin/fit.conf).

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

The console is a server of its own, on net/http and goroutines of its own like the Admin API, not on
the listeners' fib engine, so a config that breaks the listeners does not lock you out of fixing it.
It calls the Admin API's handlers in process, behind its own logins, so both apply configs by the
one path and with the same checks; the Admin API keeps its own address, `token_file` and rules, and
listens too. The console needs no store: without one, it changes the running config as the Admin
API does, in memory, and a restart starts from the config files again.

## Users and fit hash-password

A user is a name and a bcrypt hash; a password is never written in clear text. `fit hash-password`
reads a password from the first line of standard input, never from its arguments, which other users
of the machine could see, and prints its hash:

```sh
$ fit hash-password
correct horse battery staple
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

After logging in, three tabs:

- **Config**: a form for each part of the config, made from fit's JSON Schema, embedded in the
  binary: every object, list and module typed by its schema, with each field's doc. A field fit
  adds is edited with no change to the console. `admin` is not in the form.
- **JSON**: the whole config as text, edited as it is.
- **Versions**: the configs applied (`admin.history` of them), where each came from, each one's
  text, and a button to roll back to it.

**Save** sends the whole config with `If-Match`; the result says which version it was applied as,
with any warnings. A config with errors is refused and its errors are shown at their paths. When
the config was changed elsewhere since the page read it (`412`), the page says so: discard the
changes to read it again, or copy them from the JSON tab first.

For scripts, the console's own endpoints are `POST /api/login` (a JSON body
`{"username", "password"}`, at most 4 KiB, answered with the user and the CSRF token),
`POST /api/logout`, `GET /api/session` and `GET /api/schema`; under `/api/admin/` it serves the
Admin API's endpoints, behind a session instead of the token.

## Limits and timeouts

| | |
| --- | --- |
| Connections | `max_conns`, 32 |
| Sessions | `max_sessions`, 64, the oldest ended past it |
| A request's header | 64 KiB, read within 10s |
| A request's body | `admin.max_config_bytes`, 4 MiB, read within 1m; a login's body 4 KiB |
| Writing a response | 1m |
| An idle connection | 1m |
| Password checks at once | 2 |
| Failed logins | 5 an address in 15 minutes, then refused for 15 minutes; 4,096 addresses kept |

## Roles' pages

The [LLM feature](llm.md)'s and the [tunnel](tunnel.md)'s roles have pages of their own, each set by the
role's `console`: the same fields as `admin.console`, but `address` is required, and served on the
listeners' fib engine. Their users, logins, lockouts, sessions and the TLS rule work as the
console's do, with tables of their own. What a role's pages change is written back into its member
of the config, applied by the same path as the console's changes, so it shows in the console's
Versions tab and, with a store, is sealed in the store. The console edits the same members as a
whole: a role's pages are for working with the role, the console for the config.
