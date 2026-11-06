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
passwords changed from the pages, beside the config. Version 3 adds `"sections"`, named JSON values
other parts of fit keep there, as the [assistant](#the-assistant)'s settings and model API keys; a
store is written as version 3 only while it keeps a section, so one without stays readable by a fit
that predates them, and an export never holds sections. A version 1 store, which seals the config
alone, is still read, and is written as version 2 or 3 from then on. The key is derived from the password with
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
| `max_conns` | 32 | The most connections open at once, a quarter of them kept for those with a login. When they are taken, a new connection closes the oldest of those without one, of the client holding the most of them (the host's own clients counting as one), rather than being refused; it is closed as soon as it is accepted only when every connection has a login, or when its client already holds its share (a quarter of them, an IPv6 client by its /64), but for loopback clients. |

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
  role's pages; a hub's Tokens page, and the LLM roles' links page, issue and revoke the link tokens
  their peers link with ([the tunnel](tunnel.md#tokens), [the LLM feature](llm.md#tokens)), which
  the Admin API's `/tokens` endpoints and `fit token` manage too.

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

The bootstrap file's `admin.address` (and `token_file`) say where the Admin API is. With
`admin.cert_file`, fit check speaks https and verifies the API's certificate: with the
certificate `cert_file` holds when it can read it, as on fit's own host, and the system's CAs, or
with the CAs of `-ca`; for the address's host, or localhost when that is unspecified, or for
`-server-name`. With `admin.client_ca_file`, it presents the certificate of `-cert` and `-key`.

```sh
fit check -c /etc/fit/fit.json -ca /etc/fit/admin-ca.pem -server-name admin.example.com
```

The exit code is `0` when nothing drifted, `1` when drift remains (including after `-fix`), and
`2` for a usage, config-load, connection or decoding error.

For scripts and other pages, the endpoints are `GET /config/check` (the running version, its ETag, the
differences and their counts) and `POST /config/reconcile` (a JSON body of the chosen
`{"fixes": [{"id", "action"}]}` and `If-Match`); on the console they are
`GET /api/m/admin/config/check` and `POST /api/m/admin/config/reconcile`.

## Limits and timeouts

| | |
| --- | --- |
| Connections | `max_conns`, 32; a quarter of them for one client, but for loopback; a quarter kept for logged-in ones, and past them a new one closes the oldest without a login |
| Sessions | `max_sessions`, 64, the oldest ended past it |
| A request's header | 16 KiB, read within 10s |
| A request's body | `admin.max_config_bytes` and 64 KiB, 4 MiB and 64 KiB by default, read within 30s |
| Writing a response | 30s; a role's streams are bounded by each wait for the browser instead, 30s |
| A connection | closed 5s after it opens, whatever it sends, until it sends a request with a valid session (a login that succeeds counts), as the Admin API closes an unauthenticated one; then kept 2m idle, until it logs out or an API request finds its session ended, when it is closed 5s later unless a valid session comes first |
| Password checks at once | 2 |
| Admin API calls and role methods at once | 4 running, 64 waiting, each waiting at most 30s; past them `503` |
| A password change | bcrypt's time on the console's pool, two checks at once, then one store write |
| Failed logins | 5 an address (an IPv6 /64) in 15 minutes, then refused for 15 minutes, one check at a time; a wrong current password in a password change counts too; 4,096 addresses kept |

## The assistant

`admin.assistant` turns on the workbench's assistant (see
[decision 031](decisions/031-assistant.md)): the console then serves the module `ai`, and the
workbench's menu shows **Assistant**. Left out, there is no module, and nothing is sent anywhere.
It needs `admin.console`, and like all of `admin` it is read when fit starts.

```json
"assistant": {
  "providers": [
    {"name": "claude", "kind": "anthropic", "models": [{"name": "claude-sonnet-4-5", "context": 200000}]},
    {"name": "local", "kind": "ollama", "base_url": "http://127.0.0.1:11434", "models": [{"name": "qwen3:8b"}]},
    {"name": "mesh", "kind": "llm_proxy"}
  ],
  "default_model": {"provider": "claude", "model": "claude-sonnet-4-5"},
  "users": [{"username": "ops", "level": "propose"}]
}
```

**Providers.** `kind` is one of `anthropic`, `openai`, `openai_responses`, `gemini`, `azure_openai`
(with `api_version`), `ollama`, `openai_compatible` (any server that speaks Chat Completions, with
`base_url`) and `llm_proxy`, this fit's own [llm.proxy](llm.md), called on its `api` address over
loopback with one of its API keys, its certificate pinned when it has TLS. Each provider has
`connect_timeout` (10s), `first_byte_timeout` (2m), `idle_timeout` (1m), `timeout` (10m) and
`retries` (2, only while none of the answer has been taken).

**The Settings page** lists the providers with their keys' state, edits each in a dialog, and
**Test**s one before it is saved: the provider's model list, then a short call to a model, each with
its time; **Read the provider's list** adds the models it lists. The default model, its parameters,
the limits, what of the logs may be sent and each console user's level are set below. **Save
settings** keeps them in the store, from then on in place of `admin.assistant`'s; without a store
they cannot be saved here, and keys come from `api_key_env`.

**Keys** are written, never read back. A key typed on the page goes to the store with the settings,
encrypted with the rest of it; the page shows only whether a provider has one, where from, and its
last four characters. `api_key_env` names an environment variable instead, read at each call, which
a key in the store overrides. A key is never in an answer, a log line or an error, even when an API
says it back, and is sent only over https or to a loopback host.

The endpoints are `GET /api/m/ai/settings` (the settings in use, where from, whether a store keeps
them, a version, and each provider's key as the page sees it), `PUT /api/m/ai/settings`
(`{"settings", "keys": {"<provider>": "<key>" or "" to remove}, "version"}`; `400` with each error at
its path, `409` without a store, `412` when the version is stale) and
`POST /api/m/ai/providers/test` (`{"provider", "key", "model", "models_only"}`; four at once, past
them `429`; a browser that leaves cancels the test's call).

### The chat panel

Every page's top bar has the assistant's button, unless the user's level is `off`: it opens the
chat panel beside the page. A message goes to the default model with the page shown as context (its
module, page and title, what the page has selected, and the text selected on it, unless **Send this
page as context** is unticked), and the answer streams in: Markdown drawn as it comes, each tool
call a folded row with its arguments and its result, an `ask_user` question a row of buttons whose
choice goes back to the model. **Stop** ends an answer, keeping what it came to; **Retry** answers
the last message again; the head picks a conversation, starts a new one or deletes one.

The assistant reads, and never changes anything itself: the tools it calls (the **Functions** page
lists them, with the level each needs and whether it is enabled) read the config, the schema, the
versions, the status, the metrics, the upstreams' health, the Consistency check, the certificates,
the cache, fit's own log and the access log, check a config as `fit validate` does, convert nginx or
Caddyfile text, and ask the user. They run on the console's pool, through the Admin API in process,
each within 30 seconds. Every result is redacted before the model sees it (keys, password hashes,
tokens, private keys, the providers' keys, `data.redact`'s patterns, and the members no tool reads:
`admin.assistant`, `admin.console.users`, `admin.store`, `admin.address` and `admin.token_file`), cut
to `limits.max_tool_result_bytes` with a note saying so, and fenced as data, which the system prompt
tells the model never to take as instructions. `log_tail` needs `data.send_logs` and reads the last
1000 messages fit's log keeps in memory; `access_log_sample` needs `data.send_access_log` and reads
the end of the access log's file. The user's words go to the model as written.

**Limits.** `max_tool_calls` bounds the tools one message's answer may call (past it the calls fail
and the answer ends), `max_turns` the messages of a conversation (past it, start a new one),
`max_concurrent_chats` the answers made at once (past it `429`), `max_tokens_per_day` one user's
tokens a day (past it `429`, or the answer under way ends), `max_tool_result_bytes` a tool's result,
`max_stream_bytes` a model answer, and `max_conversations` the conversations kept per user (one more
removes the oldest). A conversation that outgrows its model's window (the model's `context`, 32768
tokens when left out) has its tools' early results cut, then its early turns summarized by the
model; the summary is kept with the conversation and sent in their place.

**Conversations** are kept per user, with admin.store in its section `assistant.chats.<hash of the
user>`, encrypted with the rest of the store, across restarts, each at most 512 KiB and a user's at
most 8 MiB together; without a store they last until fit restarts. A user sees only their own.

The endpoints are `POST /api/m/ai/chat` (`{"conversation", "message" | "retry": true | "answer":
{"id", "choice"}, "page", "lang"}`; answered as SSE: `data:` events of type `start`, `text`,
`thinking`, `tool_call`, `tool_result`, `ask`, `usage`, `summary`, `proposal` and `page_call`, then `event: end` with
`{"type": "done", "reason"}` (`answer`, `ask`, `max_tool_calls`, `max_tokens_per_day`, `stopped`)
or `event: error` with `{"message"}`; `403` for a user whose level is `off`, `404` for a
conversation that is not, `409` without a model, for a conversation answering already or at its
turns, `429` past the chats at once or the day's tokens), `POST /api/m/ai/chat/stop`
(`{"conversation"}`), `GET /api/m/ai/conversations`, `GET` and `DELETE
/api/m/ai/conversations/{id}`, and `GET /api/m/ai/tools` (the user's level, the model, and every
tool with its level, group, schema and whether this user may call it).

### Proposals, levels and the audit log

**Levels.** `admin.assistant.users` gives each console user a level, `default_level` the others':
`off` (no panel), `read` (the read tools), `edit` (also the open page's drafts, I154), `propose`
(also the propose tools) and `apply` (also applies proposals), each including the ones before it.
A tool is offered to the model only when the user's level reaches its own, and every call is
checked again against the settings in use when it runs, so that a level lowered on the Settings
page stops the next call of a chat under way.

**Proposals.** With `propose` or `apply`, the assistant changes nothing either: its propose tools add
steps to the conversation's one pending proposal. `config_propose` changes members, whole
(`members`, `null` removing one) or by operations at JSON pointers (`ops`: `add`, `replace`,
`remove`); `reconcile_propose` picks Consistency fixes by difference and action;
`rollback_propose` applies a kept version again; `cache_purge_propose` purges a zone by URL, by tag
or all. Each step is checked with the others on the running config, as `fit validate` checks it,
and the proposal keeps the ETag and version it was checked on; a step that is wrong in itself is
refused and not added, while one that leaves the whole invalid is kept, with the errors, for the
next step to mend. A proposal's config steps are of one kind: patches, merged and applied as one
`PUT /config/text?members=` of the top-level members they change; reconcile fixes, applied
together as one `POST /config/reconcile`; or one rollback, `POST /rollback/{n}`. Purges go with any
of them and run after the config. No step may touch `admin` (its members are out of every tool's
reach, and it changes only when fit restarts), nor write a secret: a value the model saw as
`[redacted]` may be written back only where the config has it, and is then the config's own again.
At most 32 steps and 256 KiB of arguments.

The panel shows the pending proposal above the composer: its steps, the members it changes, whether
it checks, its diff in `.conf`, `.yaml` and `.json` (redacted), **Review and apply…** and
**Discard**. Apply opens a dialog with the whole diff; only its **Apply** applies, as the user, with
`If-Match` the ETag shown and the revision confirmed: one new version, the console's, saved to the
store and listed in **Versions**, then the purges. Cancelling the dialog sends nothing. When the
config changed since the proposal was checked, the apply is refused and the proposal is moved to
the new version and offered again, to be reviewed and confirmed anew; a change the Admin API
refuses applies nothing, and the proposal stays with the reason, which the model is told too. Only
a user whose level is `apply` applies; `propose` users' proposals wait for one, who sees every
user's pending proposals on the **Proposals** page.

**The audit log** keeps what the assistant did: each answer that called tools (its calls, redacted
and cut, the proposal it left and how it ended) and each apply, refusal, offer again and discard,
with the user, the time, the conversation, the skill, the proposal's steps and the version made.
It holds `limits.max_audit_entries` (1000) entries, kept in the store's section `assistant.audit`
across restarts, and is read on the **Audit log** page.

The endpoints are `GET /api/m/ai/proposals` (the pending proposals: the user's, and every user's for
one whose level is `apply`), `GET /api/m/ai/proposals/{conversation}` (`?user=` for another's: the
proposal checked on the running config, with `revision`, `etag`, `stale`, `steps` and each step's
error, `changed`, `diff` by format, `valid`, `errors`, `can_apply`), `POST
/api/m/ai/proposals/{conversation}/apply` (`{"id", "revision", "etag", "user"}`; `200` with
`outcome` `applied` or `partial`, the `version` and the purges; `403` below `apply`; `409` for a
revision not the proposal's or a conversation answering; `412` when stale, with the proposal
offered again; `422` when it does not check or the Admin API refused it), `POST
/api/m/ai/proposals/{conversation}/discard` (`{"id", "user"}`) and `GET /api/m/ai/audit`
(`?user=`, `?conversation=`, `?kind=` `chat`, `apply` or `discard`, `?before=` an entry's `seq`,
`?limit=` up to 500; the latest first).

### Drafts on the page

A user whose level is `edit` or above has the change they ask for drafted where they are: on a page
that edits the config (the gateway's and the system's pages with a form and `.conf`, `.yaml` and
`.json` tabs, and the feature pages), the assistant writes it into the page's form or text tab as an
unsaved draft, and the person reviews it there and clicks **Save and apply**, which goes through the
page's ordinary apply path and is recorded as that person's change. The assistant never presses it.

The page tools: `page_get` (level `read`) reads what the page holds now, unsaved edits included: the
members it edits as its draft has them (redacted), the tab shown, a text that does not parse and the
last save's problems. `page_edit` (level `edit`) drafts a change for the members the page edits:
members set whole (`members`, `null` removing one), operations at JSON pointers (`ops`: `add`,
`replace`, `remove`), or the page's part as `text` in one of its tabs (`format` `conf`, `yaml` or
`json`), with a `summary` the page shows. `page_revert` (level `edit`) takes the assistant's drafts
out again, back to what the page held before the first of them; it is refused once the person has
changed the page since, when the page's own **Revert the assistant's draft** (which asks first) or
**Discard** does it.

A page declares what it edits, and the panel sends it with the page as `editable`
(`{"members", "formats", "tab", "unsaved", "assistant_draft"}`); the page tools are offered only
while the page shown declares an editor, and never over MCP, which has no open page. Before the page
gets a draft, fit checks it on the console's pool: every member it sets is one the page edits, none
is in `admin`, no secret field is written (a value the model saw as `[redacted]` may be copied back,
and the page puts back the value it holds there), its text parses in its format, and its arguments
are at most 256 KiB; a draft that fails is refused with why and never reaches the page. A change the
page cannot hold (another member, a secret, `admin`, a role's own settings) is proposed instead, or,
below `propose`, described for the person to make on the page that edits it.

The page then applies the draft to its shared draft as a person's edit is applied, running its own
check of a text (`POST /config/parse`, as for a typed one), marks the fields it changed, a note above
the forms with **Revert the assistant's draft**, and an **Assistant draft** badge beside **Unsaved
changes** in the top bar, and answers with what it changed. No page tool calls `PUT` or `PATCH
/config`, `/config/text`, a reconcile, a rollback or a purge, nor saves: the version, the ETag and
**Versions** stay as they were until the person saves, and the audit log has the page tools' calls
as tool calls of the chat, never as a change. A save, a discard or a read of the config again ends
the draft's marks.

The page tool's call reaches the panel as the stream's event `page_call` (`{"id", "name",
"arguments"}`, the arguments as fit checked them); the panel runs it on the page shown and answers
`POST /api/m/ai/chat/page` (`{"conversation", "id", "result" | "error"}`; `409` when the
conversation does not wait for that call), whose result is redacted and bounded as any tool's. The
chat waits for the answer at most 30 seconds, and no longer than the browser stays.

### Skills

A skill is a named task: when to use it, a prompt that says how, the tools it may call, the inputs it
takes and, if it wants one, its model. It is written as Markdown, YAML front matter between two lines
`---`, then the prompt:

```markdown
---
name: ops-check
title: Ops check
title_zh: 运维检查
description: Checks what the ops team checks every morning.
description_zh: 检查运维团队每天早上检查的内容。
tools: [status_get, upstream_health, config_propose]
inputs:
  - name: upstream
    description: The pool to look at.
    required: false
model: anthropic/claude-sonnet-4-5
examples:
  - Is everything up this morning?
---
Read the status and each upstream's health, and say what is down and why.
```

`name` (lower case letters, digits and `-`, at most 40), `title` and `description` are needed;
`title_zh` and `description_zh` are what the workbench shows in Chinese; `tools` names the tools it
may call, and is narrowed again by the user's level and the settings (`ask_user` is always there);
`model` is `provider/model` of a provider the settings have, or a model of the default provider;
`inputs` (at most 8) and `examples` (at most 8) are shown on the Skills page and in the panel. A
skill is at most 32 KiB. Front matter that is not this — a key not listed, a value of the wrong
kind, a name taken, YAML that does not parse — is refused with the line it is about.

fit has fourteen built-in skills, embedded in the binary: `explain`, `route-builder`,
`reverse-proxy`, `tls-acme`, `rate-limit-auth`, `cache-tuning`, `diagnose`, `drift-explain`,
`log-analysis`, `security-review`, `migrate`, `media-setup`, `llm-mesh-setup` and `tunnel-setup`.
The **Skills** page lists them with those users added: each one's title and what it is for, the
tools it names (those the user may not call struck through), and whether it is enabled; a skill is
viewed whole, exported as its `.md`, and run from one of its examples with **Try**. A user whose
level is `apply` adds skills (from a template, or by importing `.md` files), edits and deletes those
added, and enables or disables any; a built-in skill is not changed or deleted, but exported,
renamed and added as one's own, and the built-in one disabled. Skills added are shared by every
console user, at most 64, kept in the store's section `assistant.skills` with which built-in ones
are disabled; without a store they last until fit restarts.

In the panel, **Skills** picks one, and typing `/` at the start of a message lists the enabled
skills that match; `/name the rest` runs the skill on the rest. The chip above the composer says
which skill the conversation runs, and its × stops it. Without a skill, each call's system prompt
lists the enabled skills, each by name and its one-line English description, and the model loads
one with `skill_load` when the person's request fits it: the tool's result is the skill's whole
prompt, and from then on the conversation runs that skill. While a skill runs, the system prompt
carries its prompt and no catalog, and the model is offered the tools the skill names that the
user's level and the settings allow, and `ask_user`; a tool it names above the level is not
offered, and the prompt says so. The catalog takes at most a tenth of the model's window: past
that, the skills are listed by name only. The audit log names the skill each answer ran.

The endpoints are `GET /api/m/ai/skills` (every skill, without its prompt, each with `builtin`,
`enabled` and `tool_state`, and whether this user may change them), `POST /api/m/ai/skills`
(`{"source"}`: added; `400` with `{"error", "line"}`, `409` for a name taken or past 64),
`GET /api/m/ai/skills/{name}` (whole, with `prompt` and `source`), `PUT` (an added skill replaced,
its name may change), `DELETE`, and `POST /api/m/ai/skills/{name}/enable` and `.../disable`; a
change needs the level `apply` (`403` below it). `POST /api/m/ai/chat` takes `"skill"` with a
message (a name, or `""` to stop the conversation's) and `"inputs"` (the skill's inputs, at most
2 KiB each, sent to the model as data); a message `/name ...` of an enabled skill is the same as
`"skill": "name"`. Its `start` event carries the `skill` the chat runs, and an event `skill` says
that `skill_load` set one.

### The MCP server

`admin.assistant.mcp`, off when left out, serves the assistant over the
[Model Context Protocol](https://modelcontextprotocol.io) at `/mcp` on the console's address, so that
outside agents — Claude Code, Cursor, a client of an MCP SDK — use what the panel uses:

| MCP | What fit serves |
| --- | --- |
| tools | the registry's tools the token's level allows under the settings, with their schemas, results redacted and cut as for the model; not `ask_user` nor the page tools, which need the person and the open page |
| prompts | the enabled skills, each skill's inputs its arguments; a prompt is the skill's prompt with the tools it uses |
| resources | `fit://config/base`, `fit://config/effective`, `fit://status`, `fit://versions`, `fit://schema`, `fit://docs/reference` and each of its chapters; templates `fit://schema/{path}`, `fit://versions/{n}` and `fit://docs/reference/{chapter}`; redacted as for the model, at most 1 MiB each |

```json
{
  "admin": {
    "console": {"users": [{"username": "ada", "password_hash": "$2a$10$..."}]},
    "assistant": {
      "users": [{"username": "ada", "level": "apply"}],
      "mcp": {"allow": ["127.0.0.1", "::1"], "max_calls_per_minute": 120}
    }
  }
}
```

An agent authenticates with an **MCP token**, made on the workbench's **Assistant → MCP** page: a
name, a level (`read`, or `propose` for a user whose level is `propose` or `apply`), a number of
days (at most `max_token_days`, 90) and, if wanted, the addresses it is used from. The token is
shown once — fit keeps only a SHA-256 of its secret, in the store's section `assistant.mcp`, or in
memory until fit restarts without a store — and is revoked on the same page. A token's calls have
its level within its user's level in the settings in use at each request, so a user lowered to
`read` or `off` takes their tokens with them; a token's user no longer a console user is refused.
A user sees their own tokens; one whose level is `apply` sees and revokes everyone's.

A token whose level is `propose` is offered the propose tools: each adds a step to the token's one
pending proposal, kept in a conversation of its user's named `MCP: <token name>`, and answers with
the proposal's ID and a link to the **Proposals** page. There is no apply tool: the proposal is
applied only there, by a user whose level is `apply`, with the Apply click and the dialog's confirm,
as every proposal is. A token's propose calls run one after another, so that its proposal takes one
step at a time.

The transport is Streamable HTTP, for both of the protocol's eras: revision `2026-07-28`, stateless,
whose every request carries its version in `_meta` and the headers `Mcp-Protocol-Version`,
`Mcp-Method` and `Mcp-Name`, which fit checks against the body (`server/discover` answers what fit
serves); and `2025-11-25`, `2025-06-18` and `2025-03-26`, which open a session with `initialize`
(`Mcp-Session-Id`, ended with `DELETE`). A request is a `POST` of one JSON-RPC message; `tools/call`
is answered as an SSE stream (pings while the tool runs, then the result) to a client that accepts
one, the rest as JSON; a stream the client cuts cancels the call, and `GET` answers `405`, as fit
sends nothing unasked.

Each request is checked, in this order: the caller's address against `allow` (loopback when left
out; `403`), the token in `Authorization: Bearer` (`401` when missing, wrong, revoked or expired),
the token's own addresses (`403`), the user's level (`403` when `off`), and the token's bounds:
`max_calls_per_minute` (120) and `max_concurrent_calls` (4) answer `429` with `Retry-After`,
`max_request_bytes` (1 MiB) `413`, and `max_sessions` (4) ends the session used least recently,
`session_idle_timeout` (1h) an idle one. A tool call runs on the console's pool within 30 seconds;
a client that takes nothing of its stream is cut a minute after its queue passed the watermark.
Every tool call, resource read and prompt is in the audit log with the source `mcp` and the token's
name. The console's `Origin` check refuses browsers of other sites, as for every request.

To add fit to Claude Code, with the token in place of `<token>`:

```sh
claude mcp add --transport http fit http://127.0.0.1:2020/mcp --header "Authorization: Bearer <token>"
```

A client configured by JSON (Cursor, a project's `.mcp.json`) takes
`{"mcpServers": {"fit": {"type": "http", "url": "http://127.0.0.1:2020/mcp", "headers": {"Authorization": "Bearer <token>"}}}}`.
The MCP page shows both with the console's own address. fit's assistant calls no outside MCP
server.

The endpoints of the page are `GET /api/m/ai/mcp` (the server on or off, its settings with their
defaults, the endpoint, and the tokens, never their hashes), `POST /api/m/ai/mcp/tokens`
(`{"name", "level", "days", "allow"}`: `{"token", "view"}`, the token shown this once; `409` for a
name taken or past `max_tokens_per_user`) and `DELETE /api/m/ai/mcp/tokens/{id}`. The settings are
saved with the assistant's through `PUT /api/m/ai/settings`.

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
A module shows when the console serving the page serves its server module. A module may also put a tool in
every page's top bar (`topbar`, as the assistant's button), and a page may say what it has selected
with `WB.select`, which `WB.page` hands the assistant's panel with the page shown.
