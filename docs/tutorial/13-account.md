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
**Users** page changes that account's password at once, with no role of its own asked for.

A user is a name and a bcrypt hash; a password is never written in the config in the clear.
`fit hash-password` prints the hash, which goes into the bootstrap file's `admin.console.users` — that is the
user's first hash. `fit validate` refuses a `password_hash` that is not a bcrypt hash, an empty user name and
a name listed twice; `-cost` is 10 by default, and every step up doubles the time a check takes. A password
changed on these pages is saved in the store — `admin.store`, or the one `fit run -admin` makes beside the
config — encrypted with the rest of it, surviving a restart, and it takes the place of the user's password in
the bootstrap file. The browser never sees a hash: the page sends the plaintext, and fit hashes it before
saving, so the changed password applies to logins.

Account is only in the console's own menu: the roles' pages (`llm.*` and `tunnel.*` with their `console`) have
their own users, and a member's menu opened through the node switcher has no account pages.

A new password cannot be empty and is at most 72 bytes (bcrypt's limit). The first two pages read
`GET /api/users` first: it says who the session belongs to and whether a store keeps the users
(`store: true`); **This node** reads `GET /api/node`. Without a store there is nowhere to save: the page says so
and disables its forms and buttons, and the endpoints return `409`.

## Pages

### My password

Changes your own password: the **current password** is required, the new one is at most 72 bytes, and the two
new entries must match. Changing it ends the account's other sessions — the caller's own session is kept. The
hash is made with bcrypt on the console's bounded pool and written to the store.

The page sends `POST /api/account/password` (`{current_password, password}`). What it checks:

| case | answer |
| --- | --- |
| no current or no new password, or a new password past 72 bytes or empty | `400` with the reason |
| the new password is the same as the current one (a temporary-password user) | `400` |
| the temporary password expired without being changed | `403`: restart fit with `-admin` |
| no store keeps the console users | `409` |
| the current password is wrong | `403`, and the failure is counted for the address |
| 5 failures from this address within 15 minutes | `429` with `Retry-After`, refused for 15 minutes |

A password check is admitted as a login's is: one at a time from an address, every failure counted against the
address and the user, at most 2 checks at once; a user name that does not exist is checked against a dummy
hash, so the time cannot tell the names apart. Every change is logged by `admin`.

The sessions themselves are in memory, keyed by a hash of their token, so everyone must log in again after fit
restarts. Changing a password writes the new hash to the store and ends that account's sessions; a restart
changes neither.

### Users

Lists the console users (the signed-in one marked **you**), each with **Reset password**: the new password and
its confirmation, in a dialog that says every console user is an administrator, so a reset changes that
account's password at once and ends its sessions. It sends `POST /api/users/password` (`{username, password}`);
a user that does not exist gets `400`. This needs a store too. Nothing here changes the config: the user and
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

The node ID is made once (a random UUID) and kept in the store or in a file beside the config, and a manager
knows this node by it: a new ID makes the manager take it for another node.

The capabilities decide what a manager may read from this node and which operations it can run on it: without
`config.check` it is not asked for its consistency check, without `config.versions` it cannot be rolled back,
and without `logs.recent` the logs page says it does not offer it. A member whose `schema_digest` differs from
the manager's (running another version of fit) opens **read only** in the node switcher. `GET /node` is what
the manager reads every `poll_interval`, so this page and the manager's **Overview** show the same facts.

### The endpoints the three pages use

| endpoint | what it does |
| --- | --- |
| `GET /api/users` | who the session belongs to, whether a store keeps the users, and the names |
| `POST /api/account/password` | changes your own password (the current one required) |
| `POST /api/users/password` | sets another console user's password |
| `GET /api/node` | this node: its ID and where it is kept, the host, the versions, the start time and uptime, the running config, the roles, the schema digest and the capabilities |

## Key parameters

This page edits no config member; only `admin.console.users` and `admin.store`, which keep the users and their
passwords, are involved, and their tables are in
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
