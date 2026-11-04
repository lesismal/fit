# 027: A web console with logins, and the config kept encrypted on disk

[English](027-console-and-store.md) | [简体中文](027-console-and-store.zh-CN.md)

Status: accepted (2026-11-01).

## Context

Users asked for fit to serve, when configured, a web console behind a user name and password from
which every feature fit has can be configured, with the config it edits saved to a local file in a
format of fit's own, encrypted, and the password it is encrypted with written in the config.

The Admin API ([decision 005](005-admin-api.md)) can already change everything, but it asks for a
bearer token or a loopback address, refuses requests from web pages, and keeps what it applied in
memory only: a restart starts from the file again. Its dashboard is read-only.

A file cannot hold the password that opens it, so two files are involved: the config file fit is
started with, in clear text, and the encrypted store.

## Decision

- **Two files.** The file `-c` names (the bootstrap file) holds `admin`, with `admin.store`, where
  the store is and its password, and `admin.console`, where the console listens and its users. The
  store holds the rest of the config: everything but `admin`. A password in the bootstrap file
  protects a store copied away from it (a backup, a disk image), not one read on the same machine;
  `password_file` or `password_env` keep the password out of the file, as `token_file` does for the
  Admin API's token. Exactly one of the three is set.
- **The store wins.** When the store exists, it is the base config, and what the bootstrap file
  sets beside `admin` is left out, with a warning. When it does not, the bootstrap file's other
  fields are its seed, and the store is written at once. Every config applied after that, from the
  console, the Admin API or a rollback, is written to the store once it runs; one that cannot be
  written stays applied, and the result says it is not saved. SIGHUP applies the store again (as
  `fit store import` may have changed it); `-watch` is refused with a store.
- **`admin` is never in the store, and never shown.** The bootstrap file's `admin` is put into
  every config as it is loaded, after providers are merged, and not into the base config: `GET
  /config`, `/versions` and `/config/effective` hold no password, hash or key path, as decision 005
  asked of the token. A config sent with `admin` is refused while there is a store: `admin` comes
  from the bootstrap file and changes when fit restarts.
- **The store's format, `fitstore`.** A 59-byte header, then the payload as JSON, sealed
  with XChaCha20-Poly1305 with the header as its additional data:

  | Offset | Bytes | |
  | --- | --- | --- |
  | 0 | 8 | `FITSTORE` |
  | 8 | 1 | format version: 1 (read) or 2 (written, since 2026-11-04) |
  | 9 | 1 | key derivation, 1 = Argon2id |
  | 10 | 4 | Argon2id time, big endian |
  | 14 | 4 | Argon2id memory in KiB, big endian |
  | 18 | 1 | Argon2id threads |
  | 19 | 16 | salt |
  | 35 | 24 | nonce |
  | 59 | | ciphertext and its 16-byte tag |

  The key is derived from the password with Argon2id (time 3, 64 MiB, 4 threads, RFC 9106's second
  recommended setting) once, when fit starts, and kept; each write takes a new random nonce and
  keeps the salt, so writes cost no derivation. A store that does not open, by a wrong password or
  a changed byte, stops fit from starting, and is not overwritten. Writes go to a temporary file in
  the same directory, made 0600, synced and renamed over the store. Both parts come from
  `golang.org/x/crypto`, already a dependency.
  *Changed 2026-11-04:* the payload is `fitstore` version 2: the sealed JSON is an object with the
  base config under `config` and the console users whose passwords changed under `users`, each a
  `username` and a `password_hash`, so that the pages can change a password and it survives a
  restart. A version 1 store, which holds the config alone, is still read, and is written as
  version 2 from then on. `fit store export` still prints the config only; `fit store import`
  keeps the users the store held.
- **`fit store export` and `fit store import`**, given the bootstrap file, print the store as JSON
  and seal a config file (in any of fit's formats, checked first) as the store. Changing the
  password is an export, a change to the bootstrap file and an import.
- **The console is a server of its own**, on net/http and goroutines of its own like the Admin
  API (rule 4), at `admin.console.address` (127.0.0.1:2020), and with TLS from `cert_file` and
  `key_file`, which an address that is not a loopback one must have. The Admin API and its rules
  are unchanged; the console calls its handlers in process, behind its own checks, for the config,
  versions, rollback, status and caches, so that both apply configs by the one path.
  *Changed 2026-11-02:* the console is now a binding of its own on the listeners' fib engine,
  served by `app/console` as the roles' pages are, and serves the workbench (`app/workbench`): the
  config's module and every running role's, each under `/api/m/<module>/`, behind one login. The
  Admin API's handlers run on the console's bounded pool, never on a fib worker. Its binding is not
  one of the config's listeners, so a config whose listeners fail still leaves it reachable.
- **Logins.** `users` are names and bcrypt hashes, as `basic_auth` takes them and `fit
  hash-password` makes them; a password is never written in clear text. A login is checked against
  a dummy hash when the name is unknown, two checks run at once at most, and a client address that
  fails 5 times in 15 minutes is refused for 15 minutes, in a table of 4,096 addresses at most.
  A session is 32 random bytes in a cookie that is `HttpOnly`, `SameSite=Strict` and, over TLS,
  `Secure`; the server keeps its SHA-256, ends it after `session_timeout` idle (30m) or
  `session_lifetime` (12h), and keeps `max_sessions` (64), dropping the oldest.
  *Changed 2026-11-04:* a user changes its own password from the pages, with the old one; any
  logged-in user may also set another user's password. A changed hash is kept in the store beside
  the config, never in the bootstrap file, so the users there are the first set and the store's
  overrides them by user name; changing another user's password ends that user's sessions.
- **Against other web pages.** Every request but a read carries the session's CSRF token in
  `X-Fit-Csrf`, and an `Origin`, when sent, must be the console's own. Pages are sent with a
  Content-Security-Policy of their own files only, `frame-ancestors 'none'`, and API answers with
  `Cache-Control: no-store`.
- **Every feature, from the schema.** The editor is made from `fit.schema.json`, embedded in the
  binary: a form for each object, list and module, typed by its schema, with the field's doc, so
  that a field fit adds is edited with no change to the console. A raw JSON editor sits beside it.
  Saving sends the whole config with `If-Match`, and the config's errors are shown at their paths.
  *Changed 2026-11-04:* each config page also edits its part of the config as `.conf`, `.yaml` or
  `.json` text: the text is adapted to fit's native JSON and applied through the one path a save
  takes, so it is checked as `fit validate` and the apply check it, and a config that fails is
  answered with its errors and is not applied. The menu is no longer a tree of groups: the modules,
  the config's own and each role's, are one flat list.

## Consequences

- A store's config lasts across restarts; the bootstrap file's routes and the like are only its
  seed. To start again from the file, remove the store.
- Losing the password loses the store; `fit store export` makes a clear-text copy to keep.
- The console is off unless `admin.console` is set, and needs `admin`, so the Admin API listens
  too, on its own address and terms.
