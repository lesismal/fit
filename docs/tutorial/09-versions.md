# Versions

[English](09-versions.md) | [简体中文](09-versions.zh-CN.md)

## What it does

**Versions** lists the configs fit has applied: every apply leaves a version, newest first, and as many as fit
keeps are kept, which `admin.history` decides (10 by default, the running one included). The page reads the
Admin API's `GET /versions` (`GET /api/m/admin/versions` on the console), so it shows what was really applied,
not a draft.

The list is something this process of fit remembers: after a restart it begins again with the config fit
started from, so the first rows are of this run. With a store, every apply also writes the config there; this
list is what the process holds in memory, not the store's history. The version number is the one the runtime
gave the config's graph, the applied time is the moment it was applied, and the size is the config's bytes as
written — not the size of the effective config.

The page does two things: it **views** a version's text and it **rolls back** to one. Rolling back does not
erase history: it applies an old config again as a new version, so the rollback itself is in the list and can
be rolled back again. The page's value is undoing and checking: a change that went wrong can be rolled back to
the one before it, and what an apply actually wrote can be read from its text.

## Pages

### Config versions

The line at the top says it: the configs applied, newest first, as many as fit keeps, and rolling back applies
an old one again as a new version. The endpoint returns them oldest first and the page reverses that. Below it
is a table, one row per version, with **View** and **Roll back** at the end of each row (no **Roll back** on the
running one):

| Column | What it shows |
| --- | --- |
| Version | The version number; the running one carries a **running** badge |
| Applied | When the config was applied, with how long ago beside it |
| Source | Where this config came from: `file`, `store`, `admin`, `console`, `rollback`, `certificates`, `cluster`, or a provider's name (each is described below) |
| Size | The config's bytes as written |
| ETag | Its strong entity tag, a hash of the config's content, the one `If-Match` compares |
| | **View**, and **Roll back** on every row but the running one's |

For example:

| Version | Applied | Source | Size | ETag |
| --- | --- | --- | --- | --- |
| 12 · running | 2026-11-08 14:02:11 (3 minutes ago) | `console` | 1.2 KiB | `"9f2c1a7b3e0d4c65a1b8e2f0"` |
| 11 | 2026-11-08 13:47:02 (18 minutes ago) | `docker` | 1.1 KiB | `"3b7e5c2a9d1046f8c3e5a2b1"` |
| 10 | 2026-11-08 11:20:44 (2 hours ago) | `rollback` | 1.0 KiB | `"7a1d4e8b2c3059f6a4d8e1b7"` |

**Refresh** reads the table again; it is read once when the page is opened. This page edits no member, so it has
no format tabs, but it still has the shared save bar: a rollback is sent with `If-Match`, compared against the
running config's ETag. A failed apply leaves no version: only an apply that went through is recorded; the failures
are counted on the **Reloads** tile of the [Overview](04-overview.md) page ("N failed"), which also flags each new
one as a spike. The Admin API's dashboard shows these
versions too (not when `admin.disable_dashboard` turns the dashboard off).

**View** opens a window titled "Version N" with that version's text: formatted when it parses as JSON, and as it
was written otherwise. The window has **Copy**, **Roll back** (only for a version that is not the running one)
and **Close**. A version keeps the config as it wrote it, not the effective config it ran then; for that, look
at the Effective tab of the [JSON](08-json.md) page.

This page and the [Overview](04-overview.md) page: the Overview's **Recent versions** card shows the latest five
(version, applied, source) with an **All versions** link to this page, which lists every version kept.

**Roll back** asks twice: when the draft has unsaved changes it first asks "Rolling back discards the changes
not saved. Go on?", then "Apply version N again, as a new version?". After the confirmations it goes through
the same path as saving: the config is checked, then applied as a new version and written to the store, and the
page says "Version N applied again as version V" and the table refreshes. The new version's source is
`rollback`, and what it wrote is that old config itself. A rollback takes the config as that version wrote it,
not the effective config it ran then: what the providers provide is merged again as it is now. When a version is
no longer kept (past `admin.history`), `GET /versions/{n}` answers 404 and the page has no row for it.

The page does not say who did it: the `admin` logger records it — each change a console user makes is logged
with the version it applied, and each login with its address (see [Login](02-login.md)).

The endpoints this page uses (the Admin API's endpoints sit under `/api/m/admin/` on the console):

| endpoint | |
| --- | --- |
| `GET /versions` | the versions kept, oldest first, with the running version's number |
| `GET /versions/{n}` | the text of version n |
| `POST /rollback/{n}` | applies version n again; the page sends `If-Match`, and a version no longer kept answers 404 |

## Key parameters

This page involves one field of `admin`, and `admin` is never edited on a console page: it comes from the
bootstrap file.

| field | default | description |
| --- | --- | --- |
| `admin.history` | 10 | How many applied configs are kept to be read back, the running one included; 0 or left out takes 10 |

Full table: [admin](../reference/config.md#admin-api).

What is kept is a list this process holds in memory: the oldest past `admin.history` are dropped, and the
running one is always there (at least one is kept).

What a version records (each item of `GET /versions`):

| field | description |
| --- | --- |
| `version` | The version number the runtime gave the config's graph |
| `applied` | When it was applied |
| `source` | Where it came from: `file`, `store`, `admin`, `console`, `rollback`, `certificates`, `cluster` or a provider's name |
| `bytes` | The config's bytes as written |
| `etag` | A hash of its content (the first 12 bytes of its SHA-256, 24 hex digits, quoted), as a strong entity tag |

The source names are the paths a config can take in:

| source | what applied it |
| --- | --- |
| `file` | the config file, at start, on a reload of it (`SIGHUP`) or with `fit run -watch` |
| `store` | the store the config is kept in, at start, or read again on `SIGHUP` |
| `admin` | a change through the Admin API (`PUT /config`, `POST /reload` and the like) |
| `console` | a save, a reload or a consistency fix on a console page, and a manager's change made through the member's console |
| `rollback` | a rollback on this page |
| `certificates` | the running config applied again because a certificate file it loaded changed (`fit run -watch-certs`, on by default) |
| `cluster` | a config a manager applied to this member through its link (`admin.cluster_member`); a manager that reaches the member over HTTP shows as `console` or `admin` |
| a provider's name | what that provider provided changed |

## Versions and the store

A store holds one config: the current one. Every apply writes it to the store (when there is one), so after a
rollback the store holds the rolled-back config; the list of versions is what this process of fit remembers in
memory, and after a restart it begins again with the config fit started from.

`fit store export` and `fit store import` read and write the store file, not this page's list: the export is the
running config, and an import goes through the save path. Without a store the console changes the config in
memory like the Admin API does and starts again from the config file at the next restart, and this page still
shows the versions this run applied.

## How a rollback goes

A rollback is an apply: that config is loaded, compiled, its new listeners' addresses are tried, and only then
is it applied; if any step fails the running config stays. `If-Match` compares the running config's ETag, and
when it does not match (`412`) nothing is applied.

After it succeeds: a new version, written to the store, and the `admin` log records the apply. A rollback
applies that base config again and does not change what the providers provided; it also does not restart the
settings read only at start — `engine`, `telemetry`, `acme`, `providers`, the outputs and formats of `log` and
`access_log`, and `admin` run with their new values only at the next restart, and that kind of difference shows
on the [consistency check](10-consistency-check.md) page.

On a manager whose cluster has roles ([Cluster](12-cluster.md#users-and-audit)), only a cluster-wide admin may roll
the manager's own config back; the page answers the others that it is read-only.

## Common cases

- **Something went wrong, go back to the last one**: find the previous version whose source is `console` or
  `admin`, **View** it to read its text, then **Roll back**. Rolling back discards the changes not saved in the
  draft, so copy them from the [JSON](08-json.md) page first, or save them.
- **See what an apply actually wrote**: a version's text is that config itself, without what the providers
  provided; to see both together, look at the effective config.
- **After a restart the list changes**: only the versions this run applied are on the page; after a restart it
  begins again with the config fit started from. To keep a config on the machine, use `fit store export` or
  the [JSON](08-json.md) page's Download.
- **A row whose source is a provider's name**: that is an apply taken when what the provider provided changed.
  The version still records the base config itself; the provider's part is seen in the effective config. The
  same content is not applied again, so it does not make another version.
- **Roll back and reapply are not the same**: `reapply` on the consistency check applies the config as it is
  now, a rollback applies the config a version wrote; both go through the same path.
- **Look before rolling back**: **View** the text first, then roll back. After the confirmation there is no
  undoing it, because it is an apply.
- **Think about `If-Match` before rolling back**: it compares the running config's ETag, not a version's; if
  the config was changed elsewhere while the rollback was being sent, you get `412` and nothing is applied.
- **Comparing two versions**: this page has no side-by-side comparison; use **View** to copy one text and
  compare it with the other.
- **Rolling back does not change `admin`**: `admin` comes from the bootstrap file and is put into the config at
  each load; the users, the passwords and the store's settings are not in a version, so a rollback does not
  touch them.
- **Reading versions from a script**: there is no command for it; use the Admin API (`127.0.0.1:2019` unless
  `admin.address` says otherwise; the token only with `admin.token_file`):

  ```sh
  curl -H "Authorization: Bearer $TOKEN" http://127.0.0.1:2019/versions      # the list, and "current"
  curl -H "Authorization: Bearer $TOKEN" http://127.0.0.1:2019/versions/11   # the text of version 11
  curl -X POST -H "Authorization: Bearer $TOKEN" -H 'If-Match: "9f2c1a7b3e0d4c65a1b8e2f0"' \
    http://127.0.0.1:2019/rollback/11                                         # roll back to it
  ```

  `If-Match` is optional on the API: without it the rollback is applied whatever changed meanwhile.
- **A manager**: a rollback through `admin.cluster` (a group's **Roll back** operation, or a rollout rolled back)
  calls each member's `POST /rollback/{n}`, so the versions a manager uses are that member's own; a member's own
  Versions page, opened through the node switcher, lists them.

## In .conf

This page edits no member, so there is no .conf example: it reads configs that were already applied. `admin` is
never edited on a console page; it comes from the bootstrap file ([chapter 01](01-deployment.md)).

## See also

- [The config reference: Admin API](../reference/config.md#admin-api): `GET /versions`,
  `GET /versions/{n}` and `POST /rollback/{n}`.
- [The console and the store](../console.md#the-pages): where the Versions page sits in the menu.
- [The config](../configuration.md#changing-a-running-config): how each apply is written to the store.
- [JSON](08-json.md): the draft and the effective config; [Consistency check](10-consistency-check.md): when
  what runs does not match what was applied.
- Neighbouring chapters: [the function navigation bar](03-navigation.md), [Overview](04-overview.md),
  [Cluster](12-cluster.md) (a manager rolls members back across the cluster).
