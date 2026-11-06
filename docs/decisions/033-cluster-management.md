# 033: Cluster management: one fit manages many, through the APIs they already have

[English](033-cluster-management.md) | [简体中文](033-cluster-management.zh-CN.md)

Status: accepted (2026-11-06) for its principles, I140, I141 and I142; I144 and I149 built; the choices marked *pending review*
stand until the user rules on them. The track is I140–I151 of the
[development plan](../dev-plan.md#cluster-management).

## Context

A site that runs several fits, at the edge of a few regions, beside each service, or one per
customer, changes their configs one at a time today: through each one's console, its Admin API, or
its files. A route to add everywhere, a certificate to rotate, an upstream to drain, means as many
edits, with no view of which fits run what, which drifted, and which a change reached. The track
lets one fit, the manager, hold the address and an administrator's credentials of the others, its
members, and show them, group them and change their configs together, previewed, checked by each
member and rolled out in stages.

What a manager needs from a member decides how much the member must change. Each fit already has an
Admin API (a bearer token), and a console that passes the Admin API on behind a user name and a
password; every config change goes through one apply path, with ETags and `If-Match`, a history at
`/versions` and rollback. What it lacked: a way to tell who a fit is and what it offers, a check of a
config that changes nothing, a login a program can drive, and a console that withstands guessing
once it is reachable on a network.

## Decisions

The track's principles, in the order they bind:

- **The manager is not in the data path.** Members serve from their own configs whether or not the
  manager runs. No consensus among members and no runtime state shared between them.
- **Members change as little as possible.** The manager speaks a member's console (user name and
  password, then the session's cookie and CSRF token) or its Admin API (a bearer token). Every
  change on a member goes through that member's own apply path with `If-Match`, lands in its
  `/versions` and can be rolled back there.
- **Credentials stay in the manager's store**, encrypted, written once in the pages and shown
  masked; members' certificates are verified, by a CA or a pinned SHA-256 fingerprint.
- **A person applies.** A change to many members is diffed per member, checked by each member's
  dry run, confirmed, and applied in batches with health gates and rollback. Drift is reported, and
  fixed by a click; no unattended sync.
- **Nothing blocks a worker** ([rule 1](../rules.md#1-never-block-a-worker)): the manager's calls
  go out on fib's asynchronous client, bounded in concurrency, each with timeouts, every answer,
  cache, history and audit entry bounded ([rule 11](../rules.md#4-timeouts-and-limits)).
- **Off by default**: no `admin.cluster`, no Cluster menu, no outgoing calls.

What I140 adds to every fit, so that a manager can manage it:

- **`GET /node`** in the Admin API, and `GET /api/node` (also `/api/m/admin/node`) through the
  console: `id`, `id_kept`, `hostname`, `fit_version`, `fib_version`, `go_version`, `os`, `arch`,
  `started`, `uptime_seconds`, `config_version`, `config_etag`, `config_applied`, `config_source`,
  `roles` (the roles running), `schema_version`, `schema_digest` (`sha256:` of the JSON Schema the
  binary generates, so that two fits with the same digest take the same configs) and
  `capabilities`. The answer carries the running config's `ETag` and `Fit-Config-Version`.
  - **The node ID** is `admin.node_id` when the config sets it; otherwise a random UUID made once
    and kept in the store's section `node`, or, without a store, in a file beside the config
    (`fit.node-id` beside `fit.json`, `fit.node-id` in a directory of config files), written
    through a temporary file renamed over it. A fit given a store takes the ID its file had. With
    nowhere to keep it (a config on standard input, a directory that cannot be written), the ID
    lasts until fit stops, `id_kept` says `memory`, and fit warns at start. `id_kept` is `config`,
    `store`, `file` or `memory`. Only a fit with `admin` makes an ID: one without serves no
    `/node`, and writes nothing beside its config.
  - **Capabilities** are names a manager checks before it calls what they name; a name keeps its
    meaning, and a change to what an endpoint answers takes a new name: `node`,
    `config.if_match`, `config.dry_run`, `config.text`, `config.effective`, `config.check`,
    `config.versions`, `config.transfer`, `status`, `metrics`, `cache`, `logs`, `tokens`,
    `media`, always; `store` with `admin.store`; `console` while `admin.console` runs.
- **A dry run.** `PUT`, `PATCH` and `DELETE /config[/<pointer>]`, `POST /load` and
  `PUT /config/text` take `?dry_run=1` (or `true`; `0`, `false` or none apply; anything else is
  `400`). The request takes the whole path an apply takes, under the same lock: `If-Match` (`412`
  when stale), the edit, standardizing, the store's refusal of `admin`, merging what providers
  provide, loading with the bootstrap file's `admin`, compiling with every module provisioned, and
  the new listeners' addresses tried as the apply's bind tries them, a plain bind closed at once
  (`listener.Server.Plan`). It answers as the apply would: the same errors, word for word, at the
  same status; or `200` with `"dry_run": true`, the running `version` and `etag`, `new_etag` (the
  ETag the config would have, which an apply of the same body then gets), `unchanged` when it is
  the running config, the listeners it would add (their addresses as written) and remove, and the
  warnings an apply would give (changes that wait for a restart, renamed names), with
  `Fit-Dry-Run: 1`. Nothing changes: the running config, its version, the history, the store and
  the counts of reloads. The console logs it as a check, not as an apply.
  - Compiling takes no engine, as `fit validate`'s does: upstream pools dial and probe nothing.
    What a dry run cannot tell: a role's own failure when it applies (the agent's and the tunnel's,
    which an apply reports as warnings), and a race for an address between the check and the
    apply.
- **Logins a program can use.** `POST /api/login` answers `{"user", "csrf"}` with the session's
  cookie, as `GET /api/session` does; a program keeps the cookie and sends `csrf` in `X-Fit-Csrf`
  on every request but a GET or a HEAD. A refused login is `429` with `Retry-After` and
  `{"error", "retry_after"}` in seconds.
- **Failed logins slowed, per address and per user.** An address that fails 5 times in 15 minutes
  is refused for 15 minutes, as before; each further lockout it earns before it has been quiet for
  15 minutes doubles, up to an hour. A user whose password fails 10 times in a row, from any
  addresses, has its checks refused for 2 seconds, doubling with each further failure up to 5
  minutes; the right password once the wait ends works and ends the count, and so do 15 quiet
  minutes. Names that are no user's share one count, so the table stays as small as the users. A
  password change from the Account page is counted the same way.
- **Pages.** The Account menu has **This node**: the ID (copied with a click), where it is kept, the
  host, the versions, the start, the running config and its roles, the schema's digest and the
  capabilities. The config pages have **Check** beside **Save and apply**: the same request with
  `?dry_run=1`, its errors shown where a save's are.

What I141 adds, the manager (`admin.cluster`, package `cluster`, the console module `cluster`; see
[cluster management](../cluster.md)):

- **Members in the store's section `cluster`**, a document of its own version (1) holding the
  members with their credentials, and the groups, templates, rollouts and audit log the later
  iterations fill, kept as found until then. A document of a later version is shown and never
  written over.
- **Access** by the console's user name and password (logged in again on 401; a refused login
  backed off from a minute to 15; a console asking for a password change reported, its session
  logged out) or by the Admin API's token. **TLS** by the system's CAs, a `ca` PEM, or a pinned
  `fingerprint`, checked in the handshake's `VerifyConnection` so that a certificate refused is
  still shown with its fingerprint; `http` only to loopback.
- **The poller**, a goroutine of its own, starts at most `max_concurrency` polls; each is a chain
  of calls on fib's asynchronous client, one client per member, each call bounded by `timeout` and
  its answer by `max_response_bytes`. A member's state keeps how its last poll went, with each
  failure a kind of its own, and what the last poll that read it found.

What I142 adds, the workbench's **Cluster** menu ([its pages](../cluster.md#the-cluster-pages)):

- **Overview**: every member's state, as cards or a table, coloured by state, filtered by a text, a
  label selector and a state, kept in the URL, sorted, and read as often as the manager polls.
- **Nodes**: members added, tested, trusted by their certificate's fingerprint, edited and removed,
  every change with the members' ETag; credentials written once and never filled in again.
- **A member's page**: its state, then its `/node`, `/status`, `/versions` and `/config/check` read
  through the manager by `GET /api/m/cluster/nodes/<name>/read/<what>`, a bounded pass-through of
  those four GETs alone, with the poller's session.
- `fit run` checks that a manager has a store, which may be the one `-admin` keeps beside the
  config; the config alone no longer asks for `admin.store`.

What I149 adds, members behind NAT linking out ([members behind NAT](../cluster.md#members-behind-nat)):

- **`admin.cluster.link_address`**: a WebSocket listener over TLS on the listeners' engine, with a
  self-signed certificate made once and kept in the document, bounded to `max_nodes` + 16
  connections, 32 of them before their hello.
- **Add by enrollment**: a member of the access `link` and an enrollment token (`fite1.`), kept as
  a hash, with a lifetime; the config line holds the manager's URL, its certificate's fingerprint and
  the token. **`admin.cluster_member`** on the member dials out on fib's WebSocket client, pins the
  certificate, proves the token once for a credential of its own (`fitc1.`, kept as a hash on the
  manager), keeps it in its store or beside its config, and links with it from then on, again with
  backoff whenever the link ends. Both sides prove the token with decision 035's handshake.
- **Over the link** the manager sends the member's Admin API its requests, served in process
  (`admin.LinkAPI`); the poller, tests, the member's page and `Manager.Call` reach a linked member
  as the others, through one transport abstraction. A member removed, or enrolled again, has its
  link revoked and forgets its credential. The document's version is 2.

What I144 adds, groups and operations on many ([groups and operations](../cluster.md#groups-and-operations)):

- **Groups** in the document's `groups`: a label selector, members by name, or both, with a
  description and variables for I145; who is in a group is decided when asked, from the members'
  labels then, and every member's state says its groups. `GET/POST/PUT/DELETE
  /api/m/cluster/groups[/<name>]` and `POST select`, a selection previewed.
- **Operations** on a group's members or a selection (`POST /api/m/cluster/ops`, followed at
  `GET ops/<id>`): reload, roll back, check, purge, test, each member's answered apart, recorded in
  the audit log with each member's outcome.
- **On every fit, for them**: `POST /reload` (`config.reload`), the running config applied again;
  `POST /cache/purge` takes `prefixes` (`cache.prefix`).

## Pending review

Decided while building I140 (2026-11-06); each stands until the user rules on it.

1. **Where the node ID lives without a store.** A file beside the config, as the roles' kept files
   are; `admin.node_id` for where nothing lasts. Alternatives: the user's state directory
   (`$XDG_STATE_HOME`), or no ID without a store.
2. **The ID's form.** A random UUID; `admin.node_id` takes 1 to 64 letters, digits, `.`, `_`, `-`.
   Alternative: the host name, which changes with the host and collides in containers.
3. **The dry run's reach.** Every config write that takes a body or a pointer; not rollback,
   reconcile or import, whose configs were applied before or are checked by their own flows.
   Alternative: all of them.
4. **The dry run tries addresses.** A plain bind closed at once, so that a taken address is refused
   as the apply refuses it. Alternative: no bind, and a taken address found only on apply.
5. **Per-user slowing refuses, rather than delays.** A `429` with the wait, as the address lockout
   answers; holding a request open for a delay would hold the login pool. A user is kept out for
   5 minutes at most after a guess; an attacker with many addresses can keep it that way, which
   the right password, once the wait ends, gets through.
6. **Address lockouts double.** The first stays 15 minutes, so a typo or two costs no more than
   before; a guesser that comes back is slowed to 5 guesses an hour.
7. **`/api/node` at the console's top level**, beside `/api/m/admin/node`, as the plan names it.

Decided while building I141 (2026-11-06):

8. **The members are a section of the store, not a new field of its payload.** The plan asked for
   a payload field and a new payload version; the store already keeps named sections (format 3),
   which an older fit opens and writes back untouched, and which its exports leave out, so that
   members' credentials never leave in an export. A new format would have made every older fit
   refuse the whole store, its config too. The section carries a version of its own for its own
   changes. Alternative: the payload field and format 4, as planned.
9. **`admin.cluster` needs `admin.console` too**, which serves its endpoints. Alternative: the
   endpoints in the Admin API as well (I150's CLI may want them).
10. **The poller is a goroutine of its own**, not tasks of the console's pool: it only starts
    asynchronous calls and parses bounded answers, and the pool's tasks are bound to requests.
11. **One fib client per member**, two connections at most, so that each member's TLS is checked
    as its own settings say and its certificate kept. Alternative: one client and a dialer per
    member.
12. **`ca` and `fingerprint` exclusive; no `insecure`.** A pin trusts the leaf alone, whatever its
    names and dates.
13. **A refused login waits a minute, doubling to 15 minutes**, unless the member is changed or a
    test of it works; a member's 429 waits its `Retry-After`. A console asking for its temporary
    password to be changed is checked again on the same backoff, its session logged out each time.
14. **Sessions are logged out** when a member is removed or changed, after each test, and when the
    manager stops (waiting 2 seconds at most), so that the manager never fills a member's
    `max_sessions`.
15. **Tests have a bound of their own, 4**, beside `max_concurrency`, so that a busy poller never
    refuses a person's test; `POST /test` tests a member not added yet, and a saved member's test
    may take a draft of its changes.
16. **The audit log records changes** (add, change, remove: who, when, which member, the fields
    changed, never their values), 1000 entries; tests and polls are reads and are not recorded.
17. **Credentials are never answered, not even their last characters**: `password_set` and
    `token_set` only.
18. **The defaults**: `poll_interval` 10s (1s at least), `timeout` 10s (1s at least), `max_nodes`
    256, `max_concurrency` 8, `max_response_bytes` 4 MiB (16 KiB at least).
19. **The API is documented in the console's docs** until I142 writes `docs/cluster.md`; it now
    lives there, and the console's docs link to it.

Decided while building I142 (2026-11-06):

20. **A member's page reads through a narrow pass-through**, `GET nodes/<name>/read/<what>` for
    `node`, `status`, `versions` and `check` alone, answered as the member answers, on the node's
    own transport and session, with a bound of its own (8 at once) and no read of a capability the
    member does not list. I143 generalises it to `/api/n/<member>/...` for every method, with the
    audit of writes. Alternatives: the poller's last answers kept whole (memory per member, and
    stale), or I143's general pass-through now.
21. **`admin.store` is no longer required by the config**: `fit run` refuses `admin.cluster`
    without a store, which may be the one beside the config, so that `fit run -admin` with
    `admin.cluster` alone runs a manager, as the plan's recipe asks. `fit validate` no longer
    reports a manager without a store. Alternative: `-admin` writing an `admin.store` into the
    config it validates.
22. **The Cluster entry sits after the global pages and before Account** in the menu.
    Alternatives: first, for a fit that is mostly a manager; last, with the roles.
23. **Groups have no filter yet**: the overview filters by a label selector (`k=v`, `k!=v`, `k`),
    which a group by labels is; I144 adds groups to the filter. **The colours**: up green, error
    amber, down red, not read yet grey.
24. **The overview reads `state` every poll interval, clamped to 2–10 seconds**, while it is shown;
    no push from the server. Alternative: a stream of state changes.
25. **Trusting a certificate pins it in the form and tests again**, and on a saved member saves the
    pin at once after a confirm that shows the whole fingerprint; a certificate that replaced a
    pinned one is trusted the same way, behind a warning. Alternative: trust only from the form.
26. **Extension points for the next iterations**, so that they add to the pages without editing
    them: `FitCluster.addAction` (I149's Add by enrollment beside Add a node) and
    `FitCluster.rowExtra` (a member's linked or waiting state).
27. **The Nodes page shows the last 20 audit entries**; the full log, filtered, is I148's.

Decided while building I149 (2026-11-06):

28. **A link listener of its own** (`link_address`), not an endpoint of the console: the console's
    32 connections, a quarter per client, would cap the members behind one NAT. Alternative: the
    console's `/cluster/link`, with its connection bounds raised.
29. **Its certificate is self-signed, kept in the store, and pinned through the config line**, so
    that a member trusts it from its first contact and no CA is needed; renewing it would break
    every pin, so it is never renewed. Alternative: the console's certificate files, or a CA.
30. **The token and the credential are linktoken secrets** of prefixes of their own (`fite1.`,
    `fitc1.`), kept as hashes, proved with decision 035's handshake (the label
    `fit cluster link v1`, the mode bound); the welcome proves the token back.
31. **A member is added when its token is made** (the access `link`, waiting), and the token is
    used up on the manager before the credential reaches the member: a member that cannot keep it
    needs a new token. Alternative: a two-step enrollment the member acknowledges.
32. **The link carries the Admin API, served in process** (`admin.LinkAPI`: every endpoint but the
    dashboard and pprof, refused while a console password is temporary), with the authority of the
    Admin API's token; the member needs no console. Alternative: the member passing requests on to
    its own console or Admin API over loopback, with a credential of those.
33. **The member keeps its credential** in its store's section `cluster_member`, or a file beside
    its config (`.cluster-member`, 0600), for the manager of the fingerprint it was given for; a
    token in the config other than the one it came from enrolls again.
34. **Refusals for good stop the member** (`used`, `expired`, `unknown`, `proof`, `revoked`), the
    last three forgetting its credential, until a new token and a restart; `busy` and `replaced`
    are tried again. Alternative: trying again every few minutes.
35. **`admin.cluster_member` is read at start**, as the rest of `admin`.
36. **The bounds**: pings every 15s, a link closed after 45s silent, backoff 1s to a minute with
    jitter, a handshake of 10s; 16 calls per link; 2 workers and 16 waiting on the member; request
    bodies on the link bounded by `max_response_bytes` too.
37. **The document's version is 2**, so that an I141 fit, which would drop the links and the
    certificate, shows it read-only rather than writing it over.
38. **An enrollment token lasts 24h** unless told otherwise, from a minute to 30 days.
39. **One link per credential**: a newer one replaces the older (`replaced`); two fits sharing one
    credential take turns, with backoff.
40. **`Manager.Call(name, method, path, header, body, done)`** is the one way to a member's Admin
    API with its access, session or link, for I143's pass-through.
41. **The Nodes and member pages learned of the access `link`** (its access, credential and
    certificate columns, and an edit of its name and labels alone) in a few lines of their own,
    beside the extension points.
42. **fib's WebSocket connection gives neither its connection nor backpressure** (fib requests A52,
    A53): the listener keeps its connections by remote address, as the LLM mesh does, and the
    link's queues are bounded by its calls in flight and their sizes.

Decided while building I144 (2026-11-06):

43. **Who is in a group is computed, never kept**: the members its selector matches now, and those
    it names, whichever labels they have (a union). A label changed moves a member at once.
44. **The selector is Kubernetes' equality-based one, with `!key`**: `key=value` (`==`),
    `key!=value`, `key`, `!key`; no set-based terms (`in`, `notin`); the empty selector selects no
    one. The overview's label filter takes the same. Alternative: set-based terms too.
45. **A name kept for a member not there is listed as missing**, not refused; a member renamed is
    renamed in the groups that name it, and one removed is taken out of them. Operations skip
    missing names.
46. **Variables are plain strings**, 64 at most, names of letters, digits and `_`, values of 4 KiB
    at most, shown as written; secrets belong to templates (I145). Alternative: typed values.
47. **Groups stay in the document's version 2**, whose `groups` an I149 fit keeps as found; one
    ETag covers members and groups. Bounds: 256 groups, a selector of 1 KiB and 32 terms, members by
    name up to `max_nodes`.
48. **Reload is a new Admin API endpoint, `POST /reload`**, which applies the running config again
    (as the certificate watcher and the reconcile's `reapply` do), not SIGHUP's re-read of the files
    or the store: a member reached through its API may hold changes its files do not, which a
    re-read would drop. Takes `If-Match` and `?dry_run=1`; listed as `config.reload`. A member
    without it is reported unsupported; there is no fallback through `GET /config` and `POST /load`.
49. **Roll back goes to the latest version whose ETag is not the running one's**, so a reload in
    between is not taken for a step; with `If-Match` on the running ETag. No version can be chosen,
    since each member's history differs.
50. **Purge by prefix is a member-side change**: `prefixes` in `POST /cache/purge`, absolute URLs
    whose host is compared in lower case, found by going through the zone's keys under each shard's
    and the disk index's lock, as clearing does; listed as `cache.prefix`. Operations also purge
    by tag and all.
51. **Operations run asynchronously**: `202` and a page that follows them; each on a goroutine of
    its own, as the poller, starting at most `max_concurrency` members' calls at once; 4 at once;
    a member in one operation at a time (`409`); the last 32 kept in memory. Alternatives: a
    synchronous answer (minutes for many members), or the poller's own bound shared.
52. **The audit entry of an operation** has the operation's ID, its group and each member's
    status, detail and error (clipped to 256 bytes); the audit log is bounded by about 2 MiB as
    well as by 1000 entries. A manager whose document is read-only runs no operation (`409`).
53. **Test reaches the member as operations do**, through `Manager.Call` and the node's session,
    not on connections of its own as the Nodes page's Test.
54. **Two new kinds of problem**: `refused` (the member answered the operation with an error) and
    `no_earlier_config`.
55. **The pages**: Groups in the menu; a group's page holds its members with a box each (all
    selected at first), the operations bar, the latest results and the recent operations;
    `#/cluster/ops` is the same over every member, for a selection that is no group. No cancel of
    an operation under way.

Open questions of the plan, answered as proposed until reviewed:

1. Members are reached by the console's user name and password, or by the Admin API's token; both.
2. Credentials need `admin.store` on the manager; no plain file.
3. I143 opens a member's pages with the manager's bundle, read-only for a version it does not know.
4. Templates own whole members and named elements; an unnamed route cannot be shared.
5. No unattended sync; drift is fixed by a person's rollout.
6. Members linking out (I149) after templates and rollouts (I145–I146); the tunnel reaches members
   behind NAT until then. (I149 was built first, 2026-11-06; the tunnel stays a way to them.)
7. **Upgrading members' binaries (I151) is not taken up** in this track: it needs a graceful
   restart that hands listening sockets on, which fit does not have; packages, containers and
   orchestrators upgrade fits, and the overview's version column shows where each stands.

## Rejected

- **An agent on each member, or a protocol of its own.** It would be one more thing to run, secure
  and upgrade on every host; the console and the Admin API already carry everything a manager
  needs.
- **Consensus or shared runtime state among members** (rate-limit counters, caches, sessions): a
  data-plane track of its own, with failure modes the manager's design avoids.
- **The manager in the data path**, as a front for its members: a manager down would cost traffic.
- **A dry run that skips compiling**, or one through a second Configs: modules' params are checked
  only when provisioned, and the apply's lock, `If-Match` and providers' parts would not be the
  apply's.
- **Locking a user out outright after failures**: one guesser could keep every administrator out.
