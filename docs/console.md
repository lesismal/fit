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
- [fit run -admin](#fit-run--admin)
- [Importing and exporting the config](#importing-and-exporting-the-config)
- [The console](#the-console)
- [Users and fit hash-password](#users-and-fit-hash-password)
- [Logins and sessions](#logins-and-sessions)
- [The pages](#the-pages)
- [Checking the running config](#checking-the-running-config)
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
store is merged onto the bootstrap file at each start, the store's values winning: objects are
merged member by member, and a list or a value the store holds replaces the file's whole (so the
store's `listeners` and `routes` are what runs). A member only the file sets is kept, and the merged
config is written back to the store, with `the store ... merged onto ...` said. To start again from
the file, remove the store. *(Changed 2026-11-04: the file's members were left out before.)*

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

**The format** is fit's own, `fitstore`. A 59-byte header (the magic `FITSTORE`, the version,
Argon2id's settings, a salt and a nonce), then a JSON payload sealed with XChaCha20-Poly1305, the
header as its additional data. Version 2, which fit writes, seals
`{"config": <the base config>, "users": [{"username", "password_hash"}]}`: the console users whose
passwords changed from the pages, beside the config. A version 1 store, which seals the config alone,
is still read, and is written as version 2 from then on. The key is derived from the password with
Argon2id (time 3, 64 MiB, 4 threads) once, when fit starts; each write takes a new nonce, so writes
cost no derivation. A write goes to a temporary file beside the store, made 0600, synced and renamed
over it, so a crash leaves the old store or the new one, never half of one. A file imported through
the Admin API or the pages may ask for at most twice those settings (time 6, 128 MiB, 8 threads),
and one is opened at a time; another import of a store file meanwhile gets `503`.

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

## fit run -admin

`fit run -admin` turns the Admin API and the console on with no config for them, for a first start
or a machine set up from the console:

```bash
fit run -admin                       # fit.json when there is one, or no config at all
fit run -admin -c site.yaml -timeout 30m
```

- `admin` and `admin.console` get their defaults where the config leaves them out (the Admin API on
  127.0.0.1:2019, the console on 127.0.0.1:2020). With no `admin.console.users`, the user `admin`
  logs in with the password `admin`.
- That password is **temporary**: it must be changed within `-timeout` (10m when left out),
  counted from the start. Until it is changed, a login with it opens only the "change the default
  password" page: every other console endpoint answers 403 with `"must_change_password": true`,
  and the Admin API answers every request 403. After `-timeout` the password no longer logs in
  (401, `"password_expired": true`), and fit must be restarted with `-admin` to set it in time.
  The new password must differ from `admin`, and is kept in the store, so a restart logs in with it
  and asks for nothing.
- The config is kept in a store, which every change from the pages is written to. Without
  `admin.store`, the store is the file beside the config with `.store` for its extension
  (`site.yaml` gives `site.store`, `fit.json` or no config gives `fit.store`), and its password is
  `$FIT_STORE_PASSWORD` or, without it, the **key file** beside the store, the store's path with
  `.key` added (`site.store.key`), made 0600 with a random key when neither exists yet. Keep the key
  file with the store: a store copied elsewhere opens with it, or with `$FIT_STORE_PASSWORD` set to
  its contents.
- A config with only `admin.console` and no listener or route is valid, so that fit can start with
  nothing to serve yet.

`-c` takes a `.store` file too, at most one, beside config files or alone: `fit run -c site.store`
runs what the store holds, and `fit run -c base.yaml -c site.store` merges it onto `base.yaml` as
above; the store is then the one written to (`admin.store`'s password still applies when the files
set it). A `.store` beside the config files is used whether or not `-admin` is given, once it
exists. `fit store export` and `fit store import` take the same `-c`.

## Importing and exporting the config

The console's Global menu has an **Import / Export** page for the whole config, and every config
page that edits part of it has **Export** and **Import** buttons beside its format tabs, for that
part only. The formats are `.conf`, `.yaml`, `.json` and `.store`, the fitstore format: a `.store`
export is sealed with a password given in the dialog, or with the running store's key when none is
given (it then opens with the key file or `$FIT_STORE_PASSWORD`). A whole-config `.store` export
holds the console users whose passwords changed too, so that `fit run -c backup.store -admin`
elsewhere restores both. An import is applied at once, through the path every save takes, and saved
to the store; with a page's members, those are taken from the file and the rest of it is ignored.
`admin` is never exported or imported.

Both are Admin API endpoints, which the console calls in process:

| Endpoint | |
| --- | --- |
| `POST /config/export` | Body `{"format": "conf"\|"yaml"\|"json"\|"store", "members": [...], "password": "..."}`; answers the file, with `Content-Disposition`. |
| `POST /config/import?format=...&members=a,b` | Body the file (`text/plain`, or `application/octet-stream` for `store`), `Fit-Store-Password` for a store with a password of its own, `If-Match` optional; answers as `PUT /config/text`. |

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

The console serves the **workbench**: one page with a flat menu on the left, the config's modules and
each running role's as peers in it (the global pages first, then the protocols), each module's pages
under it, and the page picked on the right. Its config module edits the config; and every
[LLM](llm.md) and [tunnel](tunnel.md) role running in the same fit shows beside it as a module of its
own, with every page the role's own console has, behind the one login.

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

**Changing a password.** The pages' Account module changes the signed-in user's password, with the
current one, and lets any console user set another user's password (every console user is an
administrator). A new password is non-empty and at most 72 bytes; it is hashed with bcrypt on the
console's pool and written beside the config in the store, replacing that user's password from the
bootstrap file. The change ends that user's other sessions (the caller's own stays) and is recorded
in the `admin` log. Without `admin.store` there is nowhere to keep it: the pages say so and the
endpoints answer `409`. The users and their first hashes stay in the bootstrap file's
`admin.console.users`; the users of a role's pages change with the config.

## Logins and sessions

- **Checking a password.** A name that is not a user is checked against a dummy hash, so the time
  taken does not tell names apart; at most two checks run at once, the rest wait. Logins are
  checked on a pool of their own (1 running, 16 waiting, then `503`), so logins cannot hold up
  logged-in users' requests.
- **Lockouts.** A client address that fails 5 times in 15 minutes is refused for 15 minutes, with
  `429` and `Retry-After`. An IPv6 address counts by its /64. One check from an address runs at a
  time, so guesses sent at once cannot slip past the count; another sent meanwhile gets `429`. The
  table of addresses holds 4,096 at most; filling it never ends a lockout early, and while it is
  full of addresses locked out, new ones are refused until the first lockout ends.
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

After logging in, the workbench. Its menu is one flat list of the modules the console serves: the
config's global pages first (overview, listeners, the engine, the global settings, the whole config as
JSON, the versions, the consistency check, the logs and the account pages), then the protocol and feature pages,
then a module for each role running. A module's pages open under it; a module with one page is a link.
The **Engine** entry is the running listeners seen from their ports: each port opens to the features it
serves — its handlers, reverse and forward proxy routes, cache, streaming and WebRTC handlers, layer 4
services, SOCKS, and TLS/ACME/HTTPS by default — each a link to the page that edits it, with the tags
that port carries (PROXY protocol, HTTP/3, on-demand and client certificates) on its line. The menu can
be filtered, folded to icons, and its modules opened and closed; the theme (light, dark and three more)
and the language (English or Chinese) are picked at the top right. Each page has its own URL,
`#/<module>/<page>`.

- **Global**: an overview of the running config (status, routes, services, upstreams and their
  health, certificates, the version); the listeners; the rest of the config, part by part (log,
  access log, telemetry, metrics, limits, engine, providers, and whatever else the schema holds); the
  whole config as JSON; and the versions applied (`admin.history` of them), where each came from, each
  one's text, and a button to roll back to it.
- **Logs**: the files fit's own log and the access log write, what each rotates and keeps, the files
  on disk with their sizes and ages, **Rotate now** and **Clean up now** (the second previewing what
  it removes), and the forms of `log` and `access_log`, `time_zone` and `rotate` among them; Save and
  apply is the same bar as any page's.
- **HTTP**, **TCP/UDP**, **TLS**: the protocol pages, each with a small tag in the menu: the reverse
  proxy's routes and upstreams and the cache (HTTP), the forward proxy's routes (HTTP CONNECT), the
  layer 4 services (TCP and UDP), and TLS's certificates, HTTPS by default and ACME.
- **Streaming**, **Media auth**, **WebRTC**, **Tunnel roles**, **LLM roles**: the config's `streaming`,
  `media_auth`, `webrtc`, `tunnel` and `llm` members, each on a page of its own instead of under Global,
  with a tag naming what it is. The streaming page points at the routes where its handlers (HLS,
  HTTP-FLV, WHIP/WHEP) are set; the tunnel and LLM pages say their roles start when the config applies.
- **Account**: the signed-in user's password, and the console users' (below).
- **Tunnel** and **LLM**: a module for each role running, as `tunnel.hub` or `llm.node`, with the
  role's pages.

The **Engine** menu is that same view in the menu, one submenu per listener port, in the order the
config and HTTPS by default make them: a port opens to its handlers (static, respond, FastCGI, media
tokens, media pages — each linking to the routes page filtered to it), its reverse-proxy and
forward-proxy routes, its cache (the zones page and the routes page) and other middleware, the
streaming handlers (the streaming page and each handler's filtered routes), the WebRTC handlers (the
WebRTC page and their routes), its layer 4 services, SOCKS, TLS certificates, ACME, HTTPS by default and
the redirect to it, and finally a link to the listener form. The bound address is shown beside the
written one (and the ports HTTPS by default made when the config writes none), and the port's line
carries its tags: PROXY protocol, HTTP/3, on-demand and client certificates. The Listeners page edits
the listeners themselves and says the features are in the Engine menu. A link to a handler's routes
opens the routes page filtered to that handler.

The config's pages are forms made from fit's JSON Schema, embedded in the binary: every object,
list and module typed by its schema, with each field's doc, and a checkbox for each member that may
be left out, so that the form is the tree the config is. A field fit adds is edited with no change to
the console. `admin` is not in the forms. Each config page also has `.conf`, `.yaml` and `.json`
tabs that edit the members that page is about as raw text, in that format: the text is read from the
draft, adapted to fit's JSON as it changes (and marked when it does not parse, which stops a save),
and **Save and apply** sends it, with `If-Match`, for the server to merge those members into the
config and apply it through the one path — so it is checked as `fit validate` and the apply check it,
and a text that fails is answered with its path, line and column and applies nothing.

Every page edits the same draft of the config: **Save and apply** sends it whole, with `If-Match`,
or, from a raw tab, sends that page's text, which is merged into the config first. The result says
which version it was applied as, with any warnings. A config with errors is refused and its errors
are shown at their paths. When the config was changed elsewhere since the page read it (`412`), the
page says so: discard the changes to read it again, or copy them from the JSON page first.

For scripts, the console's own endpoints are `POST /api/login` (a JSON body
`{"username", "password"}`, answered with the user and the CSRF token), `POST /api/logout`,
`GET /api/session` and `GET /api/workbench` (the user and the IDs of the modules served). Each
module's endpoints are under `/api/m/<module>/`: the config's module, `admin`, serves
`GET /api/m/admin/schema`, the Admin API's endpoints under `/api/m/admin/`, behind a session instead
of the token, as `GET /api/m/admin/config`, and the pages' own: `GET /api/m/admin/users` (the
signed-in user, the users, and whether a store keeps them), `POST /api/m/admin/account/password`
(one's own password, with the current one), `POST /api/m/admin/users/password` (another user's), and
the raw editors' `POST /api/m/admin/config/render?format=`, `POST /api/m/admin/config/parse?format=`
and `PUT /api/m/admin/config/text?format=&members=`. A role's module serves its pages' API, as
`POST /api/m/llm.hub/rpc/hub/nodes`.

## Checking the running config

A running fit knows what it actually runs; the config it runs from says what it should. The two can
differ, and the console's **Consistency** page and `fit check` list where, so that each can be settled
deliberately: every difference says whether it is **drift**, which a config change settles, or
**info**, a runtime fact that is not a disagreement.

What is compared: the runtime — the listeners bound, the roles running, the certificates served, the
cache zones, the engine limits in force and the provider parts — with the effective config, the base
config with providers merged in. The differences found:

| | |
| --- | --- |
| Settings that take effect at start | `log`, `access_log`, `telemetry`, `engine`, `acme` and `providers` changed since fit started: the config says one thing and the running service uses what it started with. The fix writes the in-force value back into the config (or keeps the config for the next restart). `limits.max_conns`, `max_pending_bytes` and `max_write_queue_bytes` are the same, read from the running engine. `admin` is info only: it comes from the bootstrap file. |
| Roles | A role the config sets that is not running (it failed to start), and one running that the config no longer sets: remove it from the config, or apply the config again to retry or stop it. |
| Provider parts | Elements a provider (`docker`, `consul`, `consul_kv`, `etcd`, `kubernetes`) added to the effective config but not to the base config: add them to the config (`listeners`, `routes`, `upstreams`, `services`), or leave them provider-owned. Shadowed provider elements are info. |
| Listeners | A config listener with nothing bound, or a bound listener the config no longer has, is drift; a listener at port `0` whose chosen port can be written back is info. |
| TLS | A `tls.acme` name no certificate served by its listener covers is drift (remove the name, or keep it and wait for ACME); a name HTTPS by default added, or a certificate obtained on demand, is info. |
| Upstreams | A config upstream with no pool, or a pool the config does not have, is drift; an upstream all of whose backends are down is info. |
| Cache | A cache zone the config uses but nothing serves is drift; a zone kept from an earlier config is info. |

The pages show each item with its config and runtime values, its category and kind, and one select of
the fixes offered (`add`, `delete`, `replace` or `reapply`); nothing is applied until the user has
chosen, item by item, and confirmed the whole selection. The selected fixes then go through the one
save path: they are checked, applied as a new config version and written to the store, so a fix that
would not load refuses everything and a config changed in the meantime (`412`) is reported. The
`admin` logger records the applied version.

`fit check` does the same from a terminal, through the Admin API:

```sh
fit check -c /etc/fit/fit.json          # list the differences; exit 1 when there is drift
fit check -c /etc/fit/fit.json -json    # the check as JSON
fit check -c /etc/fit/fit.json -fix     # choose one fix per item, then apply them together
```

The bootstrap file's `admin.address` (and `token_file`) say where the Admin API is. The exit code is
`0` when nothing drifted, `1` when drift remains (including after `-fix`), and `2` for a usage,
config-load, connection or decoding error.

For scripts and other pages, the endpoints are `GET /config/check` (the running version, its ETag, the
differences and their counts) and `POST /config/reconcile` (a JSON body of the chosen
`{"fixes": [{"id", "action"}]}` and `If-Match`); on the console they are
`GET /api/m/admin/config/check` and `POST /api/m/admin/config/reconcile`.

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
| A password change | bcrypt's time on the console's pool, two checks at once, then one store write |
| Failed logins | 5 an address (an IPv6 /64) in 15 minutes, then refused for 15 minutes, one check at a time; a wrong current password in a password change counts too; 4,096 addresses kept |

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
with `WB.module`, naming the server module whose API it calls, its place in the menu and its pages.
A module shows when the console serving the page serves its server module.
