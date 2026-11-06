# Cluster management

[English](cluster.md) | [简体中文](cluster.zh-CN.md)

One fit can manage many. A fit with `admin.cluster` is a **manager**: it keeps the address and an
administrator's credentials of other fits, its **members**, reads each one's state, and shows them
together in the workbench's **Cluster** menu. The manager is not in the data path: members serve
from their own configs whether it runs or not, and a manager down or gone costs management and
nothing else. The design and the choices made are [decision 033](decisions/033-cluster-management.md).

- [Running a manager](#running-a-manager)
- [The Cluster pages](#the-cluster-pages)
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

The workbench shows **Cluster** in its left menu when `admin.cluster` is set, with two pages and a
page per member, in English and Chinese.

**Overview** is every member's state as the manager last read it, as cards or as a table: its state
coloured (green up, amber error, red down, grey not read yet) with how long it has been so and why,
fit's version, the running config's version, the drift its consistency check counts, the
certificate that expires first and in how many days, its connections and upstream backends down,
and the latency of its `GET /node`. A member that is down keeps what it last answered, said to be
from then. The tiles count the members by state and filter by it when clicked; the filter takes a
text (a name or URL), a label selector (`env=prod, region, tier!=db`: every term must hold, `key`
alone for a label that is there) and a state, and sorts by state (the worst first), name, latency,
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
member cannot be read, and none is asked of a member that is not up. Opening the member's own
config pages from the manager is the node switcher's, planned next (I143).

## How members are reached

A member is a fit the manager can reach, one of two ways:

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
| `GET state` | every member's state: `{"interval_ms", "timeout_ms", "read_only", "nodes"}` |
| `GET audit` | the manager's changes: who, when, which member, what changed (credentials by name only) |

A change sent with `If-Match` and an ETag that is no longer the members' is refused `412`. At most
4 tests and 8 reads run at once; one more is `429`. The reads pass on those four GETs of a member
and nothing else, never a write; a member that does not list a read's capability (`status`,
`config.versions`, `config.check`) is not asked for it. No answer, page or log line holds a
password, a token or a member's session cookie.

## Members behind NAT

A member the manager cannot reach, behind NAT or a firewall with no port open, is reached through
[the tunnel](tunnel.md) until members link out to the manager themselves (planned, I149). The
member runs a `tunnel.node` that
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
