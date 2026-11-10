# Account

[English](13-account.md) | [简体中文](13-account.zh-CN.md)

This is chapter 13 of the tutorial: the workbench's **Account** menu, its three pages — **My password**,
**Users** and **This node**. It edits no config (the passwords are kept in the store, not in the config), so
the chapter has no `.conf` example; the fields involved are in
[the config reference: Admin API](../reference/config.md#admin-api) and the feature is described in
[the console and the store](../console.md#users-and-fit-hash-password).

## What it does

**Account** is the console's own module (server module `admin`), which the menu puts after
[Cluster](12-cluster.md) (see [the function navigation bar](03-navigation.md)): it uses the console's login
and its users, and edits no config member. Every console user is an administrator, so a reset on the
**Users** page changes that account's password at once, with no role of its own asked for. The one exception
is a manager whose cluster has roles ([Cluster](12-cluster.md#users-and-audit)): there only a cluster-wide admin
may reset another user's password (the others get `403`, and the page says it is read-only), while every user
still changes its own.

A user is a name and a bcrypt hash; a password is never written in the config in the clear.
`fit hash-password` prints the hash, which goes into the bootstrap file's `admin.console.users` — that is the
user's first hash. `fit validate` refuses a `password_hash` that is not a bcrypt hash, an empty user name and
a name listed twice; `-cost` is 10 by default, and every step up doubles the time a check takes (a password
changed on these pages is always hashed at cost 10). A password changed on these pages is saved in the store — `admin.store`, or the one `fit run -admin` makes beside the
config — encrypted with the rest of it, surviving a restart, and it takes the place of the user's password in
the bootstrap file. The browser never sees a hash: the page sends the plaintext, and fit hashes it before
saving, so the changed password applies to logins.

Account is only in the console's own menu: the roles' pages (`llm.*` and `tunnel.*` with their `console`) have
their own users, and a member's menu opened through the node switcher has no account pages.

A new password cannot be empty and is at most 72 bytes (bcrypt's limit). The first two pages read
`GET /api/m/admin/users` first: it says who the session belongs to and whether a store keeps the users
(`store: true`); **This node** reads `GET /api/node`. Without a store there is nowhere to save: the page says so
("Password changes need a store") and disables its forms and buttons, and the endpoints return `409`. Each page
has **Refresh** in its tools.

## Pages

### My password

Changes your own password: the **current password** is required, the new one is at most 72 bytes, and the two
new entries must match. Changing it ends the account's other sessions — the caller's own session is kept. The
hash is made with bcrypt on the console's bounded pool and written to the store.

The page sends `POST /api/m/admin/account/password` (`{current_password, password}`); on success it says
"Password changed for <user>." What it checks:

| case | answer |
| --- | --- |
| no current or no new password, or a new password past 72 bytes or empty | `400` with the reason |
| a user still on the temporary password of `fit run -admin` gives it again as the new one | `400` |
| the temporary password expired without being changed | `403`: restart fit with `-admin` |
| no store keeps the console users | `409` |
| the current password is wrong | `403`, and the failure is counted for the address and the user |
| 5 failures from this address within 15 minutes | `429` with `Retry-After`: refused for 15 minutes, twice as long each time it happens again, up to an hour |
| another check from this address is under way, or the user's checks are slowed (10 failures in a row from anywhere: 2 s, doubling up to 5 minutes) | `429` with `Retry-After` |
| the store cannot be written | `500`, and the password is not changed |

A password check is admitted as a login's is, sharing its counts (see [Login](02-login.md)): one at a time from
an address (an IPv6 address counts by its /64), every failure counted against the address and the user, at most
2 checks at once. Every change is logged by the `admin` logger, as `console: "alice" changed the password of
"alice"`.

The sessions themselves are in memory, keyed by a hash of their token, so everyone must log in again after fit
restarts. Changing a password writes the new hash to the store and ends that account's sessions; a restart
changes neither.

### Users

Lists the console users (the signed-in one marked **you**), each with **Reset password**: the new password and
its confirmation, in a dialog that says every console user is an administrator, so a reset changes that
account's password at once and ends all its sessions (the caller's own too, when it resets itself). It sends
`POST /api/m/admin/users/password` (`{username, password}`); a user that does not exist gets `400`. No current
password is asked for, so use it for a user who forgot theirs; it checks no password, but is refused (`429`) while the caller's
address is locked out. This needs a
store too, and is logged as `console: "admin" set the password of "alice"`. Nothing here changes the config: the user and
its first hash stay in the bootstrap file's `admin.console.users`, and the changed password is in the store.

On a manager, the member pages opened through the node switcher do not forward these endpoints: a member's
console users and password are changed only on the member itself, and the manager never changes a member's
password (see [Cluster](12-cluster.md)).

### This node

Reads `GET /api/node`, four cards that say who this fit is:

| card | what it shows |
| --- | --- |
| **Node** | the **node ID**, with a button that copies it, and where it is **kept** — the config (`admin.node_id`), the store (`admin.store`), a file beside the config (`fit.node-id`), or memory only (a new ID after each restart, shown as a warning badge); the host name, the start time and the uptime |
| **Versions** | the fit, fib and Go versions, and the platform (`os/arch`) |
| **Running config** | its version, its ETag, when it was applied, its source, and the **roles running** |
| **For a manager** | the config schema's version and its digest (`schema_digest`), and the **capabilities** |

The node ID is made once (a random UUID) and kept in the store or in a file beside the config (`fit.node-id`
beside `fit.json`), and a manager knows this node by it: a new ID makes the manager take it for another node.
Where neither lasts (a container whose config is read-only and has no store), set `admin.node_id` in the
bootstrap file: 1 to 64 letters, digits, `.`, `_` and `-`, starting with a letter or digit.

The capabilities decide what a manager may read from this node and which operations it can run on it: without
`config.check` it is not asked for its consistency check, without `config.versions` it cannot be rolled back,
and without `logs.recent` the logs page says it does not offer it. A member whose `schema_digest` differs from
the manager's (running another version of fit) opens **read only** in the node switcher. `GET /node` is what
the manager reads every `poll_interval`, so this page and the manager's **Overview** show the same facts.

### The endpoints the three pages use

| endpoint | what it does |
| --- | --- |
| `GET /api/m/admin/users` | who the session belongs to (`user`), whether a store keeps the users (`store`), and the names (`users`) |
| `POST /api/m/admin/account/password` | changes your own password (the current one required) |
| `POST /api/m/admin/users/password` | sets another console user's password |
| `GET /api/node` | this node (also at `/api/m/admin/node`, and at the Admin API's `GET /node`): its ID and where it is kept, the host, the versions, the start time and uptime, the running config, the roles, the schema digest and the capabilities |

The first three are the console's own: the Admin API has no endpoint for console users.

## Key parameters

This page edits no config member; only these fields of the bootstrap file are involved:

| field | default | description |
| --- | --- | --- |
| `admin.console.users` | required, at least one (`fit run -admin` adds `admin` when none is set) | each a `username` and a `password_hash` (bcrypt, from `fit hash-password`): who may log in, and the first password of each |
| `admin.store` | none | where a changed password is kept; without it these pages change nothing |
| `admin.node_id` | made once | the node ID **This node** shows; set it where neither the store nor a file beside the config lasts |

To add or remove a console user, edit `admin.console.users` in the bootstrap file and restart fit: these pages
only change passwords. The full tables are in
[the config reference: Admin API](../reference/config.md#admin-api) ([the Chinese chapter](../reference/config.zh-CN.md)).

## See also

- [The console and the store](../console.md#users-and-fit-hash-password): the users, `fit hash-password` and
  the default-password flow.
- [Login](02-login.md): the sessions, the lockout and the slowdown; changing a password is this page.
- [Deployment](01-deployment.md): the store and `fit run -admin`.
- [Cluster](12-cluster.md): how a manager checks a member by its capabilities and its `schema_digest`.
- [The config reference: Admin API](../reference/config.md#admin-api): `admin.console`, `admin.store` and
  `GET /node`.
- The front end: [app/workbench/web/account/account.js](../../app/workbench/web/account/account.js).
