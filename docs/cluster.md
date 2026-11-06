# Cluster management

[English](cluster.md) | [简体中文](cluster.zh-CN.md)

One fit can manage many. A fit with `admin.cluster` is a **manager**: it keeps the address and an
administrator's credentials of other fits, its **members**, reads each one's state, and shows them
together in the workbench's **Cluster** menu. The manager is not in the data path: members serve
from their own configs whether it runs or not, and a manager down or gone costs management and
nothing else. The design and the choices made are [decision 033](decisions/033-cluster-management.md).

- [Running a manager](#running-a-manager)
- [The Cluster pages](#the-cluster-pages)
- [Groups and operations](#groups-and-operations)
- [A member's own pages: the node switcher](#a-members-own-pages-the-node-switcher)
- [How members are reached](#how-members-are-reached)
- [What the manager reads, and what can go wrong](#what-the-manager-reads-and-what-can-go-wrong)
- [The API](#the-api)
- [Members behind NAT](#members-behind-nat)

## Running a manager

A manager needs the console, which serves the pages, and a store, which keeps the members with
their passwords and tokens, encrypted. The shortest is a config of `admin.cluster` alone, run with
`-admin`, which serves the Admin API and the console with their defaults and keeps a store beside
the config:

```json
{"admin": {"cluster": {}}}
```

```sh
fit run -admin -c manager.json
```

The console is then on `127.0.0.1:2020` and its user `admin` has the password `admin` until it is
changed, which the console asks for first (see [`fit run -admin`](console.md#fit-run--admin)). The
store is `manager.store` beside the file, its key `manager.store.key`. A config that names its own:

```json
{"admin": {
  "store": {"path": "/var/lib/fit/manager.store", "password_env": "FIT_STORE_PASSWORD"},
  "console": {"address": "127.0.0.1:2020", "users": [{"username": "admin", "password_hash": "$2a$10$..."}]},
  "cluster": {"poll_interval": "10s", "timeout": "10s", "max_nodes": 256, "max_concurrency": 8, "max_response_bytes": 4194304}
}}
```

A manager is a fit like any other: the same config may serve routes too. Without a store, neither
`admin.store` nor one beside the config, `fit run` refuses `admin.cluster`.

| Setting | Default | What it is |
| --- | --- | --- |
| `poll_interval` | 10s (1s at least) | how often each member is read |
| `timeout` | 10s (1s at least) | the longest one call to a member may take: its connect, its answer's header and its whole |
| `max_nodes` | 256 | the most members kept; one more is refused |
| `max_concurrency` | 8 | the most members read at once; tests from the pages run beside them, 4 at most, and reads of members' pages, 8 at most |
| `max_response_bytes` | 4 MiB (16 KiB at least) | the largest answer read from a member |

The members are kept in the store's section `cluster`, encrypted with the rest of the store and
left out of its exports, so that no export carries a member's credentials. A section a newer fit
wrote is shown and never written over: the manager is then read-only, and its pages say why.

## The Cluster pages

The workbench shows **Cluster** in its left menu when `admin.cluster` is set, with **Overview**,
**Nodes** and **Groups** ([groups and operations](#groups-and-operations)), a page per member and a
page per group, in English and Chinese.

**Overview** is every member's state as the manager last read it, as cards or as a table: its state
coloured (green up, amber error, red down, grey not read yet) with how long it has been so and why,
fit's version, the running config's version, the drift its consistency check counts, the
certificate that expires first and in how many days, its connections and upstream backends down,
and the latency of its `GET /node`. A member that is down keeps what it last answered, said to be
from then. The tiles count the members by state and filter by it when clicked; the filter takes a
text (a name or URL), a label selector (`env=prod, region, tier!=db, !canary`: every term must
hold, `key` alone for a label that is there, `!key` for one that is not), a group (its members as
the manager has them now) and a state, and sorts by state (the worst first), name, latency,
certificate expiry, drift, connections or fit version. The filter is kept in the page's URL, so a
label clicked anywhere opens the overview filtered by it. The page is read again as often as the
manager polls, 2 to 10 seconds apart. A member in trouble says what is wrong and what to do, with
**Ask AI** when the assistant is on (the skill `diagnose`, given the member's state, never a
credential).

**Nodes** lists the members with their labels, URLs, access, credentials (only whether one is
saved), how their certificates are trusted, and their states, and the manager's recent changes.
**Add a node** opens a form: name, labels (`key=value`, by commas), URL, access (the console's user
name and password, or the Admin API's token), and for `https` how the certificate is trusted (the
system's CAs, a CA's PEM, or a pinned fingerprint). **Test** reaches the member as the form has it,
without saving: it says the member's fit version, host and node ID, or what went wrong, and shows
the certificate the member presented. A certificate not trusted, a self-signed one for instance,
is trusted by its fingerprint: **Trust this certificate** shows it in full beside a note to compare
it with the member's own (its certificate file, as
`openssl x509 -noout -fingerprint -sha256 -in cert.pem` prints it), pins it in the form and tests
again. Each member's row has **Test**, **Trust** (when its certificate is not trusted, or not the
one pinned: a certificate renewed on purpose is trusted again this way, with a warning), **Edit**
and **Delete**, which asks first. Every change is sent with the members' ETag the page read; a
change made elsewhere since is refused, and the page reads the members again.

Credentials are written once. The form's password and token are never filled in: a saved one is
only said to be there, and kept when its field is left empty. They are kept encrypted in the
manager's store and never answered, logged, or shown again, on any page.

**A member's page** (a member's name, anywhere) shows the member as the manager keeps it and its
state, with Test, Trust, Edit and Delete, then what the member itself answers, read through the
manager: who it is (node ID, host, the versions of fit, fib and Go, platform, start, uptime, roles,
schema and capabilities), its running config with its reloads and connections, its listeners, its
upstreams and each backend's state and counts, the certificates it serves with their expiry, the
configs it keeps (versions) and its consistency check. Each part says its own problem when the
member cannot be read, and none is asked of a member that is not up. **Open** opens the member's
own pages through the manager ([the node switcher](#a-members-own-pages-the-node-switcher)).

## Groups and operations

A **group** (a resource group) is a set of members: those whose labels its **label selector**
matches, those it names, or both. A member is in any number of groups, and a group follows its
members: a member whose labels change moves into or out of the groups whose selectors it then
matches, at once; a member a group names keeps its place when renamed and leaves when removed; a
name kept for a member not added yet is listed as missing until one is. The selector is
Kubernetes' equality-based one: terms separated by commas, `key=value` (or `key==value`),
`key!=value` (which a member without the label meets), `key` (the label is there) or `!key` (it is
not), every one of which must hold; an empty selector selects no one, for a group of names alone.
A group also has a description and **variables**, names and values the templates attached to it
will use (I145); they are not secrets, and are shown as written.

**Groups** lists the groups, with their selectors, the members they name (those missing in amber),
how many members they have now, counted by state, and how many variables. **Add a group** opens a
form whose preview shows the members its selector and names select as they are typed, and the names
that are no member's; a selector or a variable refused is said under its field. **Edit** and
**Delete** (which asks first, and leaves the members as they are) change it, sent with the
document's ETag as a member's change is.

**A group's page** shows its selector, members by name, variables and members' states, a box
before each member to select it (every member at first), and the **operations bar**:

| Operation | On each member selected | Needs (capability) |
| --- | --- | --- |
| **Reload** | applies its running config again, as a new version, so that what it loads from files (certificates, keys, token files) is loaded again: `POST /reload` | `config.reload` |
| **Roll back** | applies again its previous config: the latest version it keeps whose ETag is not the running one's (so a reload, the same config again, is not a step back), with `If-Match` on the running one, so that a config changed meanwhile is not rolled over: `GET /versions`, then `POST /rollback/{n}` | `config.versions` |
| **Check** | runs its consistency check, saying how many differences it found: `GET /config/check` | `config.check` |
| **Purge cache** | removes what its cache keeps in a zone (or every zone): by key (the absolute URLs requests name), by prefix (every URL starting with one, `https://example.com/img/`), by tag, or all: `POST /cache/purge` | `cache`, and `cache.prefix` for prefixes |
| **Test** | reads its `GET /node` as the manager reaches it, saying how long it took | — |

Each operation is confirmed in a dialog that names the members it runs on (a purge's with its
zone, keys, prefixes and tags). It then runs on the manager, at most `max_concurrency` members at
once, each call bounded by `timeout`; the page follows it member by member, waiting, running, done
with what it did (the version applied, the differences found, the keys purged) or failed with why:
the member down, its credentials refused, a capability it does not list (a fit older than the
operation), a config it refused, a zone it has not, nothing earlier to roll back to. The recent
operations close the page, each opening its results again. **Operations on any members** on the
Groups page is the same page over every member, for a selection that is no group.

An operation and each member's outcome (its status, what it did, its error) go to the manager's
audit log once it ends. The manager keeps the last 32 operations in memory, with their outcomes;
at most 4 run at once, and a member already in one is not given another until it ends. Only a
manager that can write its document runs operations, since it could not audit them otherwise.

## A member's own pages: the node switcher

On a manager, the workbench's header has a **Node** select: *This manager*, then every member with
its state. Choosing a member opens the workbench's own pages against that member, through the
manager: the overview, listeners, the Engine menu, every config page with its `.conf`, `.yaml` and
`.json` tabs, Check and Save, the versions with their rollback, the consistency check, logs,
cache zones, the media pages, and the tunnel roles' pages of a member reached by its console.
**Open** on every member's card, row and page does the same. The page's URL then carries
`?node=<member>`, so that a member's page can be bookmarked or opened in a tab of its own, and
choosing **This manager**, or **Back to the manager**, loads the manager's own pages again, with
nothing of the member's left on them.

A member's pages are marked so that no one edits the wrong fit: the member's name in a pill before
the page's title and in the tab's title (`[edge-7] Routes · fit`), a band above the page saying
who it is, its host, its fit version and how the manager reaches it, and the top bar's line, all
in a colour of the member's own. The menu is the member's: its listeners and roles, without the
manager's Cluster menu, account pages and assistant.

The pages are the manager's own: no markup or script is ever loaded from a member. What they ask
for goes to `/api/n/<member>/m/<module>/<path>` on the manager in place of
`/api/m/<module>/<path>`, behind the manager's login and CSRF token, and the manager passes it on
to the member as it reaches it: through its console with the session it keeps, its Admin API with
its token, or its link. The browser never holds a member's credentials, and the browser's own
cookie and CSRF token stop at the manager. Only what the pages send goes on (`Content-Type`,
`If-Match`, `If-None-Match`, `Upload-Offset`, `Upload-Length`, `Fit-Store-Password`), and only
what they read comes back (`ETag`, `Fit-Config-Version`, `Fit-Dry-Run`, `Retry-After`, `Allow`,
the upload's offsets, an attachment's `Content-Disposition`, and a `Content-Type` of JSON, text,
YAML or bytes; anything else is answered as bytes): a member sets no cookie and no header on the
manager's origin. A member that refuses the manager's credentials, or asks for its console user's
temporary password to be changed, is answered `502` with the problem, as its state would say it.

Saving works as it does on the member itself: the config is sent with the ETag the page read, a
config changed on the member since is refused `412`, and a save's errors link to their fields.
**Every change** (a request that is not a GET or a HEAD, but for the text conversions and Check,
which change nothing) is recorded in the manager's audit log with its user, the member, the
method and path and how the member answered (`PUT /config: 200, config version 8`), and in its
log. A member whose config schema is not the manager's (`schema_digest` in its `GET /node`: it
runs another version of fit) opens **read-only**: the band says why, Save is dimmed, and the
manager refuses its changes (`409`) as the pages do; it reads, checks and converts as any. So is
a member not read yet, and every member of a manager whose document a newer fit wrote, which could
not record the change. A member's pages draw its config with its own schema: `/api/m/admin/schema`
through its console, its Admin API's `GET /schema` (the capability `schema`) by token or link; a
member older than the switcher, which serves none, is drawn with the manager's, answered with
`Fit-Schema-From: manager`, its pages being read-only anyway.

The bounds: 3 calls to a member at once and 16 more waiting, each waiting at most `timeout`
(`503` after); 64 calls to members at once in all (`429`); a request's body and an answer at most
`max_response_bytes` (`413`, and `502` with `too_large`); 64 changes waiting for their audit entries.
Not passed on: the member's console users and passwords (changed on the member), its pprof and
dashboard, its own cluster management and assistant, the LLM roles' pages, and streams (`501`);
the tunnel roles' pages need the member's console.

## How members are reached

A member is a fit the manager can reach, one of three ways:

- **`console`**: its console's user name and password. The manager logs in with
  `POST /api/login`, keeps the session's cookie and CSRF token, reads the Admin API through the
  console (`/api/node`, `/api/m/admin/status`, `/api/m/admin/config/check`), and logs in again
  when a call answers 401, as after the member restarted or the session's lifetime ended. A
  refused login is not sent again for a minute, doubling to 15 minutes, unless the member is
  changed or tested, so that a wrong password never locks the member's users out; a login the
  member rate-limits waits what its `Retry-After` asks. A console that asks for its temporary
  password to be changed is reported, and its session logged out: the manager never changes a
  member's password.
- **`admin_api`**: its Admin API's bearer token, at the Admin API's own address (`/node`,
  `/status`, `/config/check`).
- **`link`**: a member that links out to the manager (see [members behind NAT](#members-behind-nat)),
  added by enrollment rather than by its URL, whose Admin API the manager reaches over its link.

The console is the one to choose where both are served: a console user is what a person has, and
the console's slowed logins guard it on a network. A member's URL is `https`, or `http` to a
loopback address only, so that no credential crosses a network in the clear; it may carry a path,
for a member behind a proxy. Its certificate is verified by the system's CAs, by `tls.ca` (a PEM of
the CAs to trust), or by `tls.fingerprint` (the SHA-256 of its leaf certificate, pinned once a
person confirmed it); there is no setting that trusts any certificate.

## What the manager reads, and what can go wrong

Every `poll_interval`, at most `max_concurrency` members at once, the poller reads each member's
`GET /node`, then `/status` and `/config/check` when its capabilities list them. Every call goes
out on fib's asynchronous HTTP client, never blocking a worker, with `timeout` bounding the
connect, the wait for the answer's header and the whole call, and `max_response_bytes` bounding
each answer. A member's state keeps how its last poll went (`status`: `up`, `down`, `error` or
`unknown` before the first; `error` with its `kind` and message; `reachable`, `failures` in a row,
`polled`, `seen`, `since`) and what the last poll that read it found, kept while it is down: its
node (ID, host, fit, fib and Go versions, uptime, roles, schema digest, capabilities), its
running config (version, ETag, applied, source), reloads, connections, upstreams with their
backends down, its certificates with the nearest expiry, its drift count, the latency of its
`/node`, and the certificate it presented.

Each failure has a kind of its own, which the pages word and say what to do about:

| Kind | State | What it means |
| --- | --- | --- |
| `unreachable` | down | no connection: refused, no route or no such host |
| `timeout` | down | no answer within `timeout` |
| `tls_untrusted` | error | the certificate is trusted by neither the system's CAs nor `tls.ca`; it comes with the certificate, whose fingerprint may be pinned |
| `tls_pin_mismatch` | error | the certificate is not the one pinned |
| `tls` | error | the TLS handshake failed otherwise, as `https` to a port without TLS |
| `auth_failed` | error | the user name and password, or the token, were refused |
| `rate_limited` | error | the member refuses logins for a while, after too many failed |
| `password_change_required` | error | the member's console asks for the user's temporary password to be changed: change it on the member, then on the Nodes page |
| `forbidden` | error | the member answered 403 |
| `unsupported` | error | the member serves no `GET /node`: not a fit, or one older than cluster management |
| `http_status` | error | another status the manager did not expect |
| `bad_answer` | error | an answer that is not the JSON a fit answers |
| `too_large` | error | an answer larger than `max_response_bytes` |
| `not_linked` | down | a member that links out has no link now: not enrolled yet, or linking again |

A member recovers by itself at the next poll once what was wrong is fixed: a member restarted, a
certificate trusted, credentials edited.

## The API

The module's endpoints, under `/api/m/cluster/`, behind the console's login and CSRF token; the
pages above use nothing else:

| Endpoint | What it does |
| --- | --- |
| `GET nodes` | the members, `{"version", "max_nodes", "read_only", "nodes"}`, with an `ETag`; a member's credentials only as `password_set` and `token_set` |
| `POST nodes` | adds a member: `{"name", "labels", "url", "access", "username", "password", "token", "tls": {"ca", "fingerprint"}}`; `201`; `400` with `{"field", "error"}`, `409` for a name taken or `max_nodes` reached |
| `GET nodes/<name>` | the member and its `state` |
| `PUT nodes/<name>` | replaces it, a new `name` renaming it; a password or token left empty keeps the one kept |
| `DELETE nodes/<name>` | removes it, logging its session out |
| `POST nodes/<name>/test` | tests the member as kept, or as the body would change it, without saving it: `{"ok", "error", "latency_ms", "certificate", "node", "must_change_password"}`; `certificate` holds the fingerprint a person confirms before pinning it |
| `POST test` | tests a member not added yet, as the body has it |
| `GET nodes/<name>/read/<what>` | the member's own answer to the Admin API's `GET /node`, `/status`, `/versions` or `/config/check` (`what` is `node`, `status`, `versions` or `check`), read through the manager with the session the poller keeps; `502` with `{"error", "problem"}` when the member cannot be read, `problem` as a state's `error` |
| `GET link` | where members link to: `{"enabled", "address", "url", "fingerprint", "links", "default_ttl", "max_ttl"}` |
| `POST enroll` | adds a member that links out: `{"name", "labels", "ttl", "url"}`; `201` with `{"node", "token", "expires", "config", "config_line"}`, the token shown this once; `409` without `link_address` |
| `POST nodes/<name>/enroll` | a new enrollment token for a linked member: `{"ttl", "url"}`, the same answer |
| `GET state` | every member's state: `{"interval_ms", "timeout_ms", "read_only", "nodes"}` |
| `GET audit` | the manager's changes: who, when, which member or group, what changed (credentials by name only); an operation with its `op` ID and each member's `outcomes` |
| `GET groups` | the groups, `{"version", "max_groups", "read_only", "groups"}`, with the document's `ETag`; each group `{"name", "description", "selector", "members", "variables", "created", "updated", "nodes", "missing"}`, `nodes` the members in it now and `missing` the names no member has |
| `POST groups` | adds a group: `{"name", "description", "selector", "members", "variables"}`; `201`; `400` with `{"field", "error"}`, `409` for a name taken or 256 groups |
| `GET groups/<name>` | the group and its members' `states` |
| `PUT groups/<name>` | replaces it, a new `name` renaming it |
| `DELETE groups/<name>` | removes it; its members stay |
| `POST select` | the members a selector and names select now, without a group: `{"selector", "members"}` answered `{"nodes", "missing"}` |
| `POST ops` | starts an operation: `{"op", "group", "members", "purge"}`, `op` one of `reload`, `rollback`, `check`, `purge`, `test`; on the group's members, on `members`, or on `members` of the group; `purge` is `{"zone", "keys", "prefixes", "tags", "all"}`; `202` with the operation; `409` when a member is in another operation, `429` when 4 run |
| `GET ops` | the operations kept, the latest first |
| `GET ops/<id>` | an operation: `{"id", "op", "group", "user", "purge", "started", "ended", "finished", "counts", "outcomes"}`, each outcome `{"node", "status", "detail", "error", "result", "started", "ended", "took_ms"}`, `status` `pending`, `running`, `done` or `failed`, `error` a problem as a state's |

Beside the module, at the console's top level, the node switcher's pass-through:

| Endpoint | What it does |
| --- | --- |
| `GET /api/n/<member>/workbench` | the modules of the member its pages open, `{"user", "modules", "member"}`, `member` being `{"name", "access", "url", "id", "hostname", "fit_version", "schema_digest", "manager_schema_digest", "roles", "read_only", "reason_kind", "reason"}`, `reason_kind` `version`, `not_read` or `manager`; its `GET /node` is read now |
| `GET /api/n/<member>/node` | the member's `GET /node` |
| `<method> /api/n/<member>/m/<module>/<path>` | the member's `/api/m/<module>/<path>`, its Admin API's `/<path>` for the module `admin`, with the query; answered as the member answers, but for the headers above; `502` with `{"error", "problem"}` when the member cannot be reached, `403` for what is not passed on, `409` for a change to a read-only member |

A change sent with `If-Match` and an ETag that is no longer the document's (its members and groups)
is refused `412`. At most
4 tests and 8 reads run at once; one more is `429`. The reads pass on those four GETs of a member
and nothing else, never a write; a member that does not list a read's capability (`status`,
`config.versions`, `config.check`) is not asked for it. No answer, page or log line holds a
password, a token or a member's session cookie.

## Members behind NAT

A member the manager cannot reach, behind NAT or a firewall with no port open, **links out to the
manager** itself (I149) and is managed through its link as the others are over HTTP: polled, tested,
and read on its page, the same state, the same kinds of failure, the same API.

On the manager, `admin.cluster.link_address` turns on the link listener, a WebSocket over TLS that
members dial out to. Its certificate is self-signed, made once and kept in the store's `cluster`
section, so that it outlives restarts and no CA is needed: each member pins it when it enrolls.

```json
{"admin": {"cluster": {"link_address": ":7444", "link_url": "wss://manager.example.com:7444/cluster/link"}}}
```

| Setting | Default | What it is |
| --- | --- | --- |
| `link_address` | off | where members link to, as `:7444`; without it no member can enroll |
| `link_url` | the host the page was opened at, and `link_address`'s port | the URL the config line names the manager by: `wss://host:port/cluster/link` |

On the Nodes page, **Add by enrollment** names the member, gives its labels, the token's lifetime
(24h when empty, 30 days at most) and the manager's URL as the member reaches it, and shows, once,
the line to paste into the member's `admin`:

```json
{"admin": {"cluster_member": {"manager": "wss://manager.example.com:7444/cluster/link",
  "fingerprint": "3b:1e:…:73:76", "token": "fite1.<id>.<secret>"}}}
```

The member needs no console and no open port; its Admin API may stay on loopback. When it starts
it dials the manager, checks the manager's certificate against `fingerprint` (no other certificate
is trusted, and the token is never sent to another), and proves the token. The token is used
**once**: the manager answers with a credential of the member's own, keeps only its hash (and only
the token's hash before it), and the member keeps the credential in its store's section
`cluster_member`, or without a store in a file beside its config (`fit.cluster-member` beside
`fit.json`, mode 0600). From then on it links with the credential, and `token` may be left out; a
new token in its config enrolls it again. Both sides prove the token or credential with the
handshake of [issued link tokens](decisions/035-link-tokens.md), each proof bound to both sides'
nonces and to its direction.

The link is kept alive by pings both ways, and closed by either side after 45 seconds without a
word; the member links again whenever it ends, after a wait that doubles from a second to a minute.
It survives the manager's restart and a network cut. Over the link the manager sends the member's
Admin API its requests, which the member serves in process, on two workers of the link's own with
16 waiting at most (one more is answered busy at once); a request is bounded by `timeout`, and its
body and its answer by `max_response_bytes`; one the manager gives up on is canceled on the member.
At most 16 calls go over one link at once. The member logs each change made through the link as
the manager's, and a config so applied has the source `cluster`.

Each linked member's row says whether it is **Linked** (and from where) or **Waiting**: for its
enrollment, until its token expires; or for it to link again. Its state's `link` holds the same
(`linked`, `since`, `remote`, `host`, `enrolled`, `enroll_by`, and the last hello `refused` with its
reason), and a member with no link is `down` with the kind `not_linked`. **New token** makes a
linked member a new enrollment token, for one that lost its credential or moved: it keeps linking
with its credential until it enrolls with the new one, which then replaces it. Its name and labels
are edited as any member's.

What the manager refuses, and what the member does then:

| Refused | Why | The member |
| --- | --- | --- |
| `used` | the enrollment token was used already | stops linking until it is given a new token and restarted |
| `expired` | the token's lifetime ended | the same |
| `unknown` | no member has the token or credential: replaced, or the member removed | forgets its credential, and stops |
| `proof` | the proof does not check | the same |
| `revoked` | the member was removed on the manager, or enrolled again | forgets its credential, and stops |
| `busy` | the manager could not keep the credential now | tries again |
| `replaced` | another link with the same credential took this one's place | tries again |

A member removed on the manager loses its credential: its link is closed with `revoked`, it
forgets the credential, and the manager no longer knows it.

### Through the tunnel

Before I149, or where a member already runs a tunnel node, a member is reached through
[the tunnel](tunnel.md) instead. The member runs a `tunnel.node` that
links out to a hub and offers its console as a service; the manager runs a `tunnel.proxy` that
forwards a local port to it, and adds the member at that local address. The hub may be the manager
itself.

On the manager, beside `admin.cluster`: the hub, with a TLS listener the members can reach, and a
proxy with a forward per member, each to a loopback port:

```json
{"admin": {"cluster": {}},
 "tunnel": {
   "hub": {"name": "hub", "listeners": [{"protocol": "tls", "address": ":7000"}]},
   "proxy": {"name": "manager",
     "hubs": [{"name": "hub", "endpoints": [{"address": "tls://127.0.0.1:7000"}], "token": "fitt1.<id>.<secret>", "pin": "<the hub's fingerprint>"}],
     "forwards": [{"listen": "127.0.0.1:12021", "node": "edge-7", "service": "console"}]}
 }}
```

On the member, its console on loopback and a node that offers it:

```json
{"admin": {"console": {"address": "127.0.0.1:2020", "users": [{"username": "ops", "password_hash": "$2a$10$..."}]}},
 "tunnel": {"node": {"name": "edge-7",
   "hubs": [{"name": "hub", "endpoints": [{"address": "tls://manager.example.com:7000"}], "token": "fitt1.<id>.<secret>", "pin": "<the hub's fingerprint>"}],
   "services": [{"name": "console", "address": "127.0.0.1:2020"}]}}}
```

The hub's tokens for the node and the proxy are issued on the hub's Tokens page or with
`fit token create -role tunnel.hub -name edge-7`, and its fingerprint is on its page (see
[the tunnel's tokens](tunnel.md#tokens)). The member is then added on the Nodes page with the URL
`http://127.0.0.1:12021`, its console's user name and password: `http` is allowed because the
manager reaches a loopback address, and the link from there to the member is the tunnel's, TLS
with the hub's certificate pinned. A member whose console serves TLS is added with `https` and its
certificate pinned the same way, for TLS to the member itself. A member's Admin API can be
forwarded instead, with the `admin_api` access and its token.
