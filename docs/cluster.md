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
- [Templates: config shared by a group](#templates-config-shared-by-a-group)
- [Rollouts: a change applied in stages](#rollouts-a-change-applied-in-stages)
- [Drift and alerts](#drift-and-alerts)
- [Metrics, logs and certificates](#metrics-logs-and-certificates)
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
| `admins` | none | console users who are the cluster's admins whatever the roles say ([Users, roles and the audit log](#users-roles-and-the-audit-log)) |

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

## Templates: config shared by a group

A **template** is config the manager owns on the members of the groups it is attached to. It is
written as a part of fit's config, as in a config file, and owns what it writes: a top-level member
whole (`log`, `media_auth`, `access_log`, ...), and the elements of `listeners`, `routes`,
`services` and `upstreams` one by one, by their names; a template's element of those lists must be
named, and its name takes no variables, since it is how the template finds it on every member.
`admin` and `schema_version` are the member's own. A member's **desired config** is its own config
with the parts its templates own taken from them: a top-level member set whole, an element
replaced where the member has it or added at the end of its list; every other part is left as the
member has it.

```json
{"upstreams": [{"name": "api", "addresses": ["${backend}"]}],
 "routes": [{"name": "api", "match": {"path": ["/api/*"]},
             "handler": {"module": "proxy", "params": {"upstream": "api"}},
             "middleware": [{"module": "headers", "params": {"response": {"set": {"X-Served-By": "${node.name} in ${node.labels.region}"}}}}]}]}
```

**Variables** in strings are rendered per member: `${node.name}` (the member's name on the
manager), `${node.id}` and `${node.hostname}` (as its `GET /node` says), `${node.labels.<key>}`, and
`${<name>}`, a variable of the groups the template is attached to that the member is in, a later
group in the template's list winning; `$${` writes `${`. A variable a member lacks makes that
member's sync state an error, naming it, rather than rendering an empty string. Variables are
strings: a number or a boolean is written as such in the template.

**Order.** A template is attached to any number of groups, and has an `order`, from -1000 to 1000.
A member's templates are applied by order, then by name; when two own the same part of a member,
the later one wins, and the part says which it overrides.

**Secrets** (members named as passwords, secrets, tokens and keys are, and push URLs) are kept in
the manager's store with the template and answered as `[secret]`; sent back as `[secret]`, the
secret kept at that place (by member, by an element's name, or by index) stays, and anything else
replaces it. A template made from a member's config keeps its secrets without them reaching a
browser. **Paths to files** (`cert_file`, `token_file`, `..._dir`) are warned about: each member
reads them from its own disk.

**Sync state.** Reading a member compares its config (`GET /config`) with what its templates
render, part by part, using what the member keeps of the manager (below) to tell who changed what:

| State | Reason | What it means |
| --- | --- | --- |
| `in_sync` | | the member holds the part as rendered |
| `out_of_sync` | `missing` | the member has no such part yet |
| `out_of_sync` | `template_changed` | the member holds what the manager last saw there in sync, and the template now renders something else |
| `conflict` | `changed_here` | the member's value is neither what the manager last saw in sync nor what is rendered: someone changed it on the member |
| `conflict` | `removed_here` | the member no longer has a part the manager saw in sync |
| `conflict` | `own_value` | the member has its own value, from before the template owned the part |

A member's state is the worst of its parts', `error` when it cannot be read or a template does not
render for it, and `unmanaged` when no template is attached to it. Nothing is applied to a member
here: a rollout (I146) applies the desired config, and drift (I147) watches it.

**What a member is told.** When the manager reads a member, and a little after a template, a group
or a member changes, it tells each member what it owns there, through the member's own
`PUT /node/managed` (with `If-Match`): its node ID and host, and each part with its template and the
hash of the value it last saw there in sync. The member keeps it in its store (in memory without
one, until the manager tells it again) and answers it at `GET /node/managed`, each part with whether
it changed there since (capability `node.managed`). Its own config pages mark those parts (a bar
and a badge, amber once changed there) and ask before a change to one: a field focused, a button
clicked, a raw tab edited, or **Save and apply** sending a change not asked about. Nothing is
refused: the member stays its own, and the manager reports the conflict. `DELETE /node/managed`
forgets the manager.

**The pages**, under **Templates**:

- **The list**: each template's groups (those that are no group's in amber), order, parts and
  members, counted by sync state; **Read now** reads every member; **Add a template**, **Import
  from a node** and **Compare two nodes**.
- **A template's page**: its name, description, order and groups (each with its member count); its
  config as a form drawn from the schema (a card per top-level member, a box per element, fields
  added from a list and left out by a cross; what has no plain control, as a handler, as JSON),
  and as `.conf`, `.yaml` and `.json` text, converted as typed; the parts it owns, its variables
  with where each comes from, its warnings; and its members, each one's sync state with every part's
  value on the member beside the rendered one.
- **Rendered result** (`#/cluster/rendered/<member>`): a member's sync state, and its config beside
  its desired config, line by line.
- **Compare** (`#/cluster/compare`): two members' configs part by part, the same, different or in
  one only, each side by side, and the whole configs.
- **Import from a node**: a member's config read part by part (unnamed list elements, `admin` and
  `schema_version` cannot be picked), the parts picked made into a template, attached to the
  groups picked. Attached to its own member, it is in sync there.

## Rollouts: a change applied in stages

A **rollout** applies one change to many members, a batch at a time, each batch checked before the
next goes. It is made from:

- **a template's change**: every member of the template's groups (or those picked) gets its desired
  config, its own with the parts its templates own taken from them;
- **a sync**: the members picked, a group's, or every member, each brought in sync with all its
  templates;
- **a copy**: parts of one member's config (top-level members whole, `routes[api]` and the other
  named lists' elements by name) copied to others, as it has them now, secrets and all;
- **a patch** (I153): parts written in the request (`config`, the same kind of parts as a template's,
  without variables) put on the members picked or a group's as written: config no template owns,
  so an admin's of each member, as a copy is; a secret written as `[secret]` keeps each member's
  own, and a member that has none there is not ready;
- **a template's draft** (I153): a template's rollout with `config`, the template's whole config as
  it would be. Each member is rendered with the draft in the template's place, and nothing is saved:
  **starting** it saves the draft as the template, as the person who starts it, who must be a
  cluster-wide admin (templates are theirs), and then applies it; a template changed or removed
  since the plan refuses the start. A secret written as `[secret]` keeps the template's.

**The plan.** Planning reads each member (`GET /node` for its capabilities, `GET /config` and its
ETag), makes its desired config and its diff, part by part (secrets masked), and has the member
check it by its own dry run (`PUT /config?dry_run=1` with `If-Match`): the whole apply path,
compiled and loaded, without applying it. A member is **ready**, has **nothing to change**, or is
**not ready**, with why: the member's dry run refused the config (each error with its JSON pointer),
it could not be read, its templates do not render for it, it is a fit older than rollouts (it must
list `config.if_match`, `config.dry_run` and `config.versions`), it is held by another rollout or
an operation, or the plan would keep more than 8 MiB of configs and diffs. The plan keeps each ready
member's desired config byte for byte in the manager's store (encrypted), the ETag it was made from,
the version it ran, and the ETag the member's dry run said the new config gets. A plan not started
within an hour expires; at most 4 rollouts are planned or under way at once.

**The strategy**, chosen when it starts:

| Strategy | What it does |
| --- | --- |
| `all` | one batch of every member |
| `rolling` | batches of `batch_size` members, or of `batch_percent` of them, with `pause_ms` between batches |
| `canary` | a first batch of `canary` members (the first by name); once its gate passes the rollout **waits** for a person to go on; then the rest, in batches as rolling has them, or at once |

Members may be left out when it starts. Each member gets its desired config by its own `PUT /config`
with `If-Match` on the ETag the plan read: a member changed since the plan answers `412` and is
**skipped**, never overwritten. An apply whose answer is lost is told by the member's `GET /node`:
running the ETag the dry run gave means it was applied.

**The health gate** after each batch, `wait_ms` after it is applied (5 s by default), checks each
member applied: it is reachable; it runs the config the rollout applied (its config's ETag), so its
reload succeeded and held; its consistency check (`GET /config/check`) finds no more differences
than before the rollout; and, when `max_5xx_ratio` is set, its share of 5xx answers over
`window_ms` after the batch is at most that, judged once it answered `min_requests` (counted from
the member's `GET /status` `requests`, which a member counts when its config sets `metrics`; a batch
with a member that counts none fails before anything of it is applied). An apply refused fails the
gate too. A failed gate **stops the rollout** and rolls back its batch, or every member applied
(`on_failure: all`), each to the version it ran before by its own `POST /rollback/{n}` with
`If-Match` on the rollout's config: a member changed since is left as it is, said so.

**Steering.** **Pause** takes effect after the batch under way; **Resume** goes on from there;
**Go on** follows a canary; **Abort** stops after the members being applied (a gate's wait is cut
short), leaving what was applied; **Roll back** rolls back every member the rollout applied, once it
is paused, waiting or ended. One rollout or operation runs on a member at a time: a rollout holds
its members from its start to its end, and operations and other rollouts are refused on them. A
manager restarted while a rollout runs shows it **paused where it stopped**; resumed, a member whose
apply was under way is found applied or not by its ETag, and gated.

**Kept.** Rollouts are kept in the manager's store, the last 50, each with its plan, its strategy,
its batches and each member's outcome, versions (from, applied, rolled back), the hash of the config
it got and its gate; the configs and diffs' values are dropped once it ends. The audit log has each
plan, start, pause, resume, go, abort, rollback and end, the end with every member's outcome.

**The pages**, under **Rollouts**: the history, the latest first, and **New rollout**; a new
rollout's form (what, the template, group or members, the parts of a copy, a patch's config, a
template's draft); a rollout's page:
planned, each member's state, its changes side by side, its dry run's errors linked to the member's
page, its own pages and its rendered result, a box to leave it out, the strategy form, and **Start**,
whose dialog lists the batches and every member's diff; started, its progress batch by batch with
each member's status, versions and gate (the share of 5xx and the drift it found, and why it
failed), with **Pause**, **Resume**, **Go on**, **Abort** and **Roll back**. A template's page has
**Roll out**, and a member's rendered result **Sync this node**.

## Drift and alerts

**Drift** is a member that is not what the manager expects of it, in two ways:

- **against its templates**: a part out of sync or in conflict, or templates that do not render for
  it, as [the sync state](#templates-config-shared-by-a-group) says;
- **against its own config**: the differences its consistency check finds between the config it
  runs from and what it runs (`GET /config/check`).

The consistency check comes with every poll. A member a template is attached to is read against its
templates after a poll when that can have changed: it has no sync state yet, its config's ETag (as
its `GET /node` says) is not the one its sync state was taken of, or its sync state is older than
10 minutes; a change of the templates, the groups or the members is read at once anyway. These reads
tell the member nothing, run at most `max_concurrency` at once and one at a time a member, and are
never waited for: a member there is no room for is read after a later poll.

Nothing is applied by the manager unasked. **Fix** on a member out of sync with its templates plans
a sync rollout of it ([rollouts](#rollouts-a-change-applied-in-stages)) and opens its plan, with each
part's diff and the member's dry run; it is applied once a person starts it there. A consistency
drift is fixed by a **Reload** of the member, an operation confirmed first, as the member's own
Consistency page explains.

**Alerts** follow **rules**. A rule watches one thing, on every member or a group's, and fires once
its condition has lasted the rule's time (`for_ms`):

| Kind | Fires when | Settings |
| --- | --- | --- |
| `member_down` | the member is `down`, or `error` (refused credentials, a certificate not trusted), from when its state changed | |
| `drift` | the member drifts, as above | `source`: `templates`, `consistency`, or both when empty |
| `rollout_failed` | a rollout ended `failed`, an alert per rollout, until it is rolled back or `resolve_after_ms` after it ended | `resolve_after_ms`, 0 for never |
| `cert_expiry` | the member's certificate that expires first does within `days` | `days`, 14 by default |
| `backends_down` | at least `threshold` of the member's upstream backends are down | `threshold`, 1 by default |
| `reload_failed` | the member's latest reload failed, as its reload counts tell from the manager's first read of them, until one succeeds, the member restarts, or `resolve_after_ms` passes | `resolve_after_ms` |

A manager starts with a rule of each kind: member-down after a minute, drift after 5 minutes,
backends-down after 2 minutes, certificate-expiry within 14 days, rollout-failed and reloads-failing
at once; each sent again every 4 hours (`repeat_ms`, 0 for never) while it fires. A member not read
since the manager started keeps its alerts as they are. An alert **resolves** once its condition is
gone, its member is removed or its rule removed, turned off or changed; the last 500 are kept.

**Webhooks** are where alerts go: a rule sends to those it names, or to every webhook when it names
none. Each evaluation sends a webhook one notification of the alerts that fired (or are sent again)
and one of those that resolved; an alert resolved is sent when it was sent firing. A webhook has a
**format**:

| Format | Body |
| --- | --- |
| `json` | fit's own: `{"version", "source", "manager", "manager_host", "status", "test", "sent", "text", "alerts", "more"}`, each alert as the API answers it, 50 at most and the rest counted in `more` |
| `slack` | `{"text"}`: Slack's incoming webhooks, Mattermost and Rocket.Chat |
| `discord` | `{"content"}` |
| `feishu` | Feishu's (Lark's) bot: a text message, signed by the webhook's secret as Feishu signs |
| `dingtalk` | DingTalk's robot: a text message, signed by the secret as DingTalk signs, in the URL's query |
| `wecom` | WeCom's group robot: a text message |
| `custom` | the webhook's template, JSON with `${text}`, `${status}` and `${manager}` in strings, and `${count}` and `${alerts}` as values |

A chat service's message is a line saying what it is and a line each alert; the webhook's template,
when it has one, is that line, with `${severity}`, `${rule}`, `${kind}`, `${node}`, `${subject}`,
`${on}`, `${status}`, `${summary}`, `${since}` and `${id}` replaced. With a secret, `json`, `slack`,
`discord`, `wecom` and `custom` carry `X-Fit-Timestamp` and `X-Fit-Signature: sha256=<hex>`, the
HMAC-SHA256 keyed by the secret of the timestamp, a dot and the body; a receiver computes it again
and compares. A webhook's URL is a secret as its secret is, since a chat service's token is in it:
both are written once, kept encrypted in the manager's store, and answered as the URL's scheme and
host only, never logged; the URL is `https`, or `http` to a loopback address.

A webhook sends one notification at a time, each within its timeout (10 s by default, 1 to 60 s),
from a queue of 64, the oldest dropped past it; one that fails is not sent again, and the webhook's
status says how its sends went. A webhook that does not answer holds up nothing but its own queue: the
poller and the evaluator go on. A chat service that answers 200 with an error code (Feishu's `code`,
DingTalk's and WeCom's `errcode`) has failed.

**Silences** keep the alerts of a rule, of a member, or of a rule on a member, from being sent until
they end (30 days ahead at most); the alerts still show, marked silenced, and are not counted in the
menu's badge. An alert that was never sent is sent once its silence ends.

Everything is kept in the store's section `cluster.alerts`, apart from the members' document: the
rules, the webhooks with their URLs and secrets, the silences, the alerts firing and the past ones.
The audit log has every change of rules, webhooks and silences, and every webhook test.

**The pages**, under **Drift and alerts** (`#/cluster/alerts/<tab>`): **Drift**, each member's
templates and consistency check, the members with drift only or every one, **Diff** (each part out
of sync, the member's beside its templates', read now), **Fix**, **Fix all out of sync**, **Plan by
hand** (the new rollout's form, filled in) and **Reload**; **Alerts**, the counts, the alerts firing
with **Silence**, those pending, and the past ones; **Rules**, added, edited and removed; **Webhooks**,
with their sends, **Test**, edited and removed; **Silences**, active and past, added and ended. While
alerts fire, not silenced, the Cluster menu shows their count, red when one is critical.
## The assistant on a manager

On a manager whose console runs the [assistant](console.md#the-assistant) (`admin.assistant`), the
assistant reads the cluster and proposes changes across it (I153), as the user who asks:

| Tool | Level | What it does |
| --- | --- | --- |
| `cluster_members` | `read` | every member's state as last polled (status, error, versions, config version, drift, sync with its templates, nearest certificate and days left, connections, reloads, backends down, alerts firing), filtered by group, label selector or status |
| `cluster_templates` | `read` | the templates, their parts, variables and members, and how many are in sync; one whole, with each member's parts |
| `cluster_rollouts` | `read` | the latest 20 rollouts batch by batch with each member's result, and the latest 10 operations; one whole with its changes |
| `cluster_metrics` | `read` | each member's requests by class, connections, reloads and backends down, read now from its `GET /status`, summed by group, with the [series](#metrics-logs-and-certificates) of the last 12 polls |
| `cluster_certs` | `read` | the certificate inventory as the members' polls read it, the soonest to expire first, within some days |
| `cluster_log_tail` | `read` | the latest lines of up to 16 members' own logs, merged by time, by logger, level and text, 50 a member by default; offered when `data.send_logs` is on, as `log_tail` is |
| `member` argument | `read` | `config_get`, `versions_diff`, `status_get`, `metrics_query`, `upstream_health`, `consistency_check`, `certs_list`, `cache_zones` and the media tools read a member instead of the manager, through the manager's session with it |
| `rollout_propose` | `propose` | a rollout's plan: a template (as it is, or a draft), a sync, a patch or a copy, with a strategy suggested |
| `cluster_op_propose` | `propose` | an operation (reload, rollback, check, purge, test) checked, not started |
| `drift_fix_propose` | `propose` | a sync rollout's plan for members out of sync or in conflict, and a reload for those whose consistency check differs |

**Nothing is applied by the assistant.** A propose tool makes a rollout plan, which the Rollouts page
shows (each member's diff and dry run, marked as planned by the assistant for the user, with the
strategy it suggests in the start form), or checks an operation and links the operations page with
it filled in (`#/cluster/ops?op=&members=&purge=` or a group's page), whose confirm dialog opens: the
person starts either there, with that page's own confirm. None is a step of the conversation's
proposal, and the panel's Apply never starts one; the panel draws a card under the tool's result
with the link. A plan made over MCP answers with the workbench's link.

**Roles and levels.** Every call acts as the user and is checked by the manager's roles, as the
endpoints check them: reads need a role anywhere, a member's own reads a role on it; a template's
rollout or a sync an operator of each member, a patch or a copy an admin of each, a template's
draft a cluster-wide admin; an operation an operator of each member. The assistant's level (I126)
is checked too, capped by the roles (a cluster-wide admin keeps it, an operator anywhere at most
`propose`, anyone else `read`): the narrower wins, so an operator never proposes a change to a
template, and a user at `read` gets no propose tool.

**Bounds.** A read across members (`cluster_metrics`, `cluster_log_tail`) calls at most
`max_concurrency` members at once, each within `timeout`, and answers within 20 seconds: a member
slow or down is reported with its problem, and the others read. A member's read through `member`
waits 20 seconds at most; a plan waits for its members' dry runs as long, and says when it is still
planning. Only reads reach a member through `member`: `GET` of the config, versions, status,
metrics, consistency check, cache, node, schema and the media and streaming reads, and the text
conversions; never a write, the tokens or the logs' files. A member of another fit version is noted.

**Secrets.** No member's password, token or session reaches a tool's result: the manager answers
none, a member's errors are redacted of its own, and every result is redacted as any tool's. The
assistant never writes a secret: a value copied as `[redacted]` goes as `[secret]`, which keeps the
one there; one written is refused.

**Skills**: `fleet-health` reads the members together and says who is down, behind, drifting or
near a certificate's expiry; `rollout-planner` plans a change across a group with a canary,
batches, gates and rollback, and explains a failed batch; `cluster-drift` explains each member's
drift and plans its fix. They are offered on a manager only. **Ask AI** on the Overview (the fleet,
and each member's card), on a rollout's failed batch, and on a drifting member of the Drift page.

## Users, roles and the audit log

The manager's console users have **roles** on the cluster, cluster-wide and per group:

| Role | May |
| --- | --- |
| `viewer` | read: the members, their states, groups, templates, rollouts, operations, the roles and the audit log; a member's own pages through the node switcher, read-only |
| `operator` | also run operations (reload, roll back, check, purge, test) and rollouts of the templates there are (a template's change, a sync), and steer them; test a member as it is kept |
| `admin` | also add, change and remove members and their credentials, make enrollment tokens, test a draft, write through the node switcher and copy parts of one member's config to others; **cluster-wide**, change groups, templates and the roles themselves |

A role on a group is a role on its members: a user's role on a member is the highest of its
cluster-wide role and its roles on the groups the member is in, and a change is allowed only when
the user has the role on **every** member it touches, as the member is and, for a change of its
labels or name, as it will be. Groups, templates and roles decide who has which role where, so only
a cluster-wide admin changes them. The lists (Overview, Nodes, Groups, Templates, Rollouts, the
audit log) are read by anyone with a role anywhere; a member's own pages, its reads, its rendered
config, a compare or an import need a role on that member.

**Drift and alerts** follow the same roles: drift, alerts, rules, webhooks (without their secrets)
and silences are read by any role; a drift's **Fix**, which plans a sync rollout, needs an operator
of each member; a silence needs an operator of its member, of its rule's group for a rule on a
group, or cluster-wide otherwise; rules, webhooks and a webhook's **Test** need a cluster-wide admin.

**Where roles are kept.** The roles live in the manager's store, beside the members, and are changed
on **Users and audit** by a cluster-wide admin; they take effect at once, for every page and request.
`admin.cluster.admins` names console users who are cluster-wide admins whatever the roles say: the
way back in when no admin is left, changed only by editing the config and restarting, as all of
`admin` is. A console user the roles do not name has the **default role** (none unless set); one
named with no role stays without one.

**Before roles are set** (no roles in the store and no `admins`), every console user is a
cluster-wide admin, as before roles existed, so a manager keeps working after an upgrade. The first
roles saved must leave at least one cluster-wide admin (or `admins` must name one).

**The manager itself.** Once roles are set, the manager's own pages, its config, its console users,
its store and the services it runs, are changed only by a cluster-wide admin: anyone else reads
them, and its pages say so in a band above them. A user's own password, the text conversions and
dry runs stay everyone's. A member opened through the node switcher is read-only for a user who is
not its admin (`reason_kind` `role`).

**The assistant** (I126) never does more for a user than the user may: on a manager with roles, a
cluster-wide admin keeps the level `admin.assistant` gives; a user who is an operator anywhere has at
most `propose`, which a cluster-wide admin applies; anyone else at most `read`. MCP tokens are bounded
the same way. The Users page shows each user's level and the level the roles bound it to.

**The audit log** records every cluster action: members added, changed, removed, enrolled and tested;
groups, templates and roles changed; operations; each step of a rollout; alert rules, webhooks and
silences changed and webhooks tested; and every write through the node switcher. Each entry has its user, time, members, operation, outcome (`ok`, `failed`,
`partial`, `unknown` for a write sent and not answered, `not_sent`, or the state a rollout ended in),
the versions before and after, and the change:

| Entry | Versions | Diff | Digest |
| --- | --- | --- | --- |
| a member, group, template or the roles | the manager's document, `r12` → `r13` | of what the API shows of it: a member's credentials only as set or not (a password changed is named in the detail), a template's secrets masked | the SHA-256 of the whole diff |
| an operation | each member's config version before and after, in its outcome | none | none |
| alert rules, webhooks and silences | the alerts' own version, `a3` → `a4` | of the rules, webhooks (as the API shows them) and silences | the SHA-256 of the whole diff |
| a rollout's start | each member's version, and the hash of the config it gets | each member's parts, what it has and what it gets, secrets masked | the SHA-256 of the whole diff |
| a rollout's end | each member's version before, and after it was applied or rolled back | none | each member's config hash |
| a write through the switcher | the member's config version as last read, and the one it answered | none: what was sent holds the member's config and secrets | the SHA-256 of the body sent |

A diff is kept up to 16 KiB, cut past it with its digest of the whole. The log keeps the last 1,000
entries and at most about 2 MiB, the oldest going first, in the store, so it holds across restarts.

**The pages**, under **Users and audit**: **Users and roles**, each console user and each user the
roles name, its cluster-wide role, its roles on groups, its assistant's level, the default role and
**Save roles** (a cluster-wide admin's; the others read it); **Audit log**, filtered by user, member,
operation and time (the filters are in the URL, so a member's page links to its own entries with
**Changes of this member**), each entry opening its diff, digest, versions and outcomes, with links to
its rollout and, for a write through the switcher, to the member's own Versions page; **Export JSON
lines** saves the entries picked. Every Cluster page draws only the controls the user's roles allow,
and a user with no role is told so.

## Metrics, logs and certificates

Three more pages of the Cluster menu (I150) show what the members do, from what the manager reads
already and from each member's own log:

- **Metrics** charts a series the manager keeps from each poll of each member, the last
  360 polls (an hour at the default `poll_interval`): requests, 4xx and 5xx **a second** over the
  time since the poll before, from the member's `/status` `requests` (counted when its config sets
  `metrics`); the **connections** open and the **bytes queued** for clients (`pending_bytes`, what
  `limits.max_pending_bytes` bounds) as the poll read them; and the configs applied and failed
  since the poll before. One chart has a line per member, one a line per group (its members
  summed a poll interval at a time), and a table each member's trend and its counters since it
  started, which are the member's own. A point has no rate after a member restarted, across a gap
  of more than three intervals, or when the member counts no requests. The window is 3 minutes to
  an hour; a name or `a,b` filters the members; the page reads again as often as the manager polls.
- **Logs** tails the members chosen (16 at most) at once, merged by time. Each member's log keeps
  its latest 1,000 messages in memory (the ring the assistant's `log_tail` reads), which its Admin
  API now answers at `GET /logs/recent?logger=&level=&contains=&limit=&after=` (capability
  `logs.recent`), each message numbered from the start of fit. The manager asks each member for
  the messages after the last it gave, and answers them merged with a cursor of each member's last
  number; **Follow** sends the cursor back every 2 seconds. A member restarted numbers from 1 again
  and is read from its start; a member down, refused or older than this is said apart, and the
  others are read. Filters: the least level, a logger, a text; a group picks its members. The page
  keeps the latest 2,000 lines.
- **Certificates** lists every member's certificates as its latest poll read them: names, issuer,
  serial, dates and days left, the soonest to expire first, filtered by text and by days left;
  members serving none, or not read yet, are named below.
- **Overview** shows a sparkline of each member's requests a second (or its connections, for a
  member that counts no requests) beside its state.

The series, the inventory and a member's last counters are kept on the manager in memory, on the
member's node: a member removed or renamed takes them with it, and a manager restarted starts
afresh. Nothing more is asked of a member than the poller reads, but its logs. The series and the
inventory are any role's to read, as the members' states are; a member's logs need a role on it
(viewer), as its own reads do.

### The command line: fit cluster

`fit cluster` reaches a manager through **its Admin API**, as `fit check` reaches a fit: `-c` names
the bootstrap config whose `admin` says where the Admin API listens and its `token_file`; `-ca`,
`-server-name`, `-cert` and `-key` are for an Admin API over TLS. The manager serves its endpoints
there under `/cluster/`. Whoever holds the Admin API's token can change the manager's own config,
so the Admin API acts as a cluster-wide admin whatever the roles say, and the audit log records it
as the user `Admin API` (a console user of that name has no role).

```
fit cluster nodes                    the members: URL, access and labels
fit cluster state                    each member's state; exits 1 when one is not up
fit cluster groups                   the groups and their members
fit cluster diff [-node a,b | -template T]
                                     each member's parts that differ from its templates; exits 1 when one does
fit cluster certs [-within 30d]      the inventory; with -within, exits 1 when one expires in it
fit cluster metrics [-node a,b] [-group g]
fit cluster logs -node a,b [-level warn] [-logger tls] [-contains x] [-limit N] [-f]
fit cluster rollout list
fit cluster rollout start -template T [-group G] [-node a,b] [strategy] [-yes] [-wait]
fit cluster rollout start -sync [-group G | -node a,b] [strategy] [-yes] [-wait]
fit cluster rollout start -id ID [strategy] [-yes] [-wait]
fit cluster rollout status [-wait] ID
fit cluster rollout pause|resume|go|abort|rollback ID
```

`rollout start` plans the rollout (or takes one planned), prints each member's plan (its changes,
its dry run's errors), asks before it starts it unless `-yes`, and with `-wait` follows it batch by
batch until it ends, or until a canary waits for `fit cluster rollout go`. Its strategy is `-mode
all|rolling|canary`, `-batch-size`, `-batch-percent`, `-pause 30s`, `-canary N`, `-on-failure
batch|all`, `-gate-wait 5s`, `-max-5xx 0.01`, `-window 30s`, `-min-requests 20` and `-exclude a,b`,
as the Rollouts page's form. `status` exits 1 for a rollout failed, aborted, rolled back or
expired. Every command takes `-json` for the Admin API's answer.

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
| `GET audit` | the audit log, oldest first, filtered by `user`, `node` (a member it acted on), `action` (whole, `op.reload`, or its kind, `op`) and `since`/`until` (RFC 3339): `{"entries", "total", "max_entries", "max_bytes", "users", "actions"}`, each entry `{"at", "user", "action", "node", "nodes", "group", "template", "rollout", "op", "detail", "outcome", "before", "after", "diff", "clipped", "digest", "outcomes"}`, an outcome `{"node", "status", "detail", "error", "before", "after", "digest"}`; credentials never; with `format=jsonl`, the entries as JSON lines, a file to save |
| `GET me` | the user's roles, which the pages draw by: `{"user", "open", "fixed", "role", "top", "groups", "nodes", "manager_read_only", "assistant"}`, `role` cluster-wide, `top` the highest anywhere, `nodes` and `groups` the role on each; any console user, a role or not |
| `GET access` | the roles: `{"version", "open", "read_only", "admins", "default_role", "users", "groups", "roles", "max_users"}`, each user `{"user", "role", "groups", "console", "fixed", "assistant", "assistant_cap"}`, with an `ETag` |
| `PUT access` | replaces the roles, a cluster-wide admin's: `{"default_role", "users": [{"user", "role", "groups": {"<group>": "<role>"}}]}`; `400` with `{"field", "error"}` for a role or group there is none of, a user named twice, or no cluster-wide admin left |
| `GET groups` | the groups, `{"version", "max_groups", "read_only", "groups"}`, with the document's `ETag`; each group `{"name", "description", "selector", "members", "variables", "created", "updated", "nodes", "missing"}`, `nodes` the members in it now and `missing` the names no member has |
| `POST groups` | adds a group: `{"name", "description", "selector", "members", "variables"}`; `201`; `400` with `{"field", "error"}`, `409` for a name taken or 256 groups |
| `GET groups/<name>` | the group and its members' `states` |
| `PUT groups/<name>` | replaces it, a new `name` renaming it |
| `DELETE groups/<name>` | removes it; its members stay |
| `POST select` | the members a selector and names select now, without a group: `{"selector", "members"}` answered `{"nodes", "missing"}` |
| `POST ops` | starts an operation: `{"op", "group", "members", "purge"}`, `op` one of `reload`, `rollback`, `check`, `purge`, `test`; on the group's members, on `members`, or on `members` of the group; `purge` is `{"zone", "keys", "prefixes", "tags", "all"}`; `202` with the operation; `409` when a member is in another operation, `429` when 4 run |
| `GET ops` | the operations kept, the latest first |
| `GET ops/<id>` | an operation: `{"id", "op", "group", "user", "purge", "started", "ended", "finished", "counts", "outcomes"}`, each outcome `{"node", "status", "detail", "error", "result", "started", "ended", "took_ms"}`, `status` `pending`, `running`, `done` or `failed`, `error` a problem as a state's |
| `GET templates` | the templates, `{"version", "max_templates", "read_only", "templates"}`, with the document's `ETag`; each `{"name", "description", "groups", "order", "config", "created", "updated", "parts", "variables", "warnings", "nodes", "missing_groups"}`, secrets in `config` as `[secret]`, `parts` each `{"path", "member", "name"}`, `nodes` the members of its groups now |
| `POST templates` | adds a template: `{"name", "description", "groups", "order", "config"}`, or `"from": {"node", "paths"}` in place of `config`, the parts of a member's config (`"log"`, `"routes[api]"`) as it has them now; `201`; `400` with `{"field", "error"}`; `409` for a name taken or 128 templates; `502` when the member cannot be read |
| `GET templates/<name>` | the template |
| `PUT templates/<name>` | replaces it, a new `name` renaming it; `[secret]` keeps the secret kept at its place |
| `DELETE templates/<name>` | removes it; its members keep what it gave them, and are told the manager no longer owns it |
| `POST sync` | reads members now, `{"nodes"}`, `{"template"}` (its members) or `{}` (every member), and tells each what the manager owns there: `{"nodes", "pending"}`, each `{"node", "state", "error", "errors", "checked", "config_etag", "templates", "parts", "counts", "released", "managed", "managed_error", "note"}`, a part `{"path", "member", "name", "template", "overrides", "state", "reason", "have", "want"}`, secrets masked; members not read within 30s are `pending`; 4 at once, `429` past |
| `GET sync` | every member's latest sync state, without the parts' values |
| `GET desired/<member>` | a member read now: its sync state, `config` and `desired`, secrets masked |
| `POST compare` | `{"a", "b"}`: both members' configs, `{"a", "b", "parts"}`, each side `{"node", "config_etag", "config"}`, each part `{"path", "state", "a", "b"}`, `state` `same`, `differs`, `only_a` or `only_b` |
| `POST import` | `{"node"}`: the member's config's parts, `{"node", "config_etag", "parts"}`, each `{"path", "member", "name", "ownable", "why", "secret", "value"}` |
| `POST rollouts` | plans a rollout: `{"kind", "template", "group", "nodes", "from", "paths", "config", "description"}`, `kind` `template`, `sync`, `copy` or `patch`; `config` a patch's parts, or a template's draft (I153); `202` with the rollout, planning; `400` with `{"field", "error"}`, `404` for a template, group or member there is none of, `429` past 4 planned or under way |
| `GET rollouts` | the rollouts kept, the latest first, without the diffs' values: `{"max_active", "plan_ttl_ms", "read_only", "rollouts"}` |
| `GET rollouts/<id>` | a rollout: `{"id", "kind", "template", "group", "from", "paths", "config", "template_base", "description", "planned_by", "source", "suggested", "started_by", "state", "reason", "strategy", "created", "planned", "started", "ended", "batch", "next_at", "batches", "members", "counts"}`; `state` `planning`, `planned`, `running`, `paused`, `waiting`, `rolling_back`, `done`, `failed`, `aborted`, `rolled_back` or `expired`; a member `{"node", "status", "config_etag", "version", "new_etag", "digest", "applied", "rolled_back", "unsure", "changes", "changed", "errors", "warnings", "note", "error", "gate"}`, a change `{"path", "state", "sync", "reason", "template", "have", "want"}` (secrets masked), a gate `{"passed", "checked", "reasons", "notes", "version", "drift_before", "drift_after", "requests", "errors_5xx", "ratio"}`; never a member's config, and its own `config` with its secrets as `[secret]`, until it ends; `source` `assistant` for a plan the assistant made for `planned_by`, and `suggested` the strategy it suggests |
| `POST rollouts/<id>/start` | starts a plan: `{"strategy": {"mode", "batch_size", "batch_percent", "pause_ms", "canary", "on_failure", "gate": {"wait_ms", "max_5xx_ratio", "window_ms", "min_requests"}}, "exclude"}`; a template's draft is saved as the template first, a cluster-wide admin's; `409` for a member held by another rollout or an operation, or a template changed since the plan |
| `POST rollouts/<id>/pause`, `resume`, `go`, `abort`, `rollback` | steers it; `409` when its state does not allow it |
| `DELETE rollouts/<id>` | discards a plan |
| `GET drift` | every member's drift: `{"interval_ms", "read_only", "nodes"}`, each `{"node", "status", "groups", "managed", "templates", "consistency", "drifted", "fixable"}`, `templates` its sync state without the parts' values, `consistency` its check's counts |
| `POST drift/fix` | `{"nodes"}`: plans a sync rollout of them, as `POST rollouts` with `kind` `sync`; `202` with the rollout |
| `GET alerts` | `{"read_only", "counts", "active", "pending", "history"}`, `counts` `{"firing", "silenced", "pending", "critical", "warning", "info"}` (firing not silenced, by severity), an alert `{"id", "rule", "kind", "severity", "node", "subject", "state", "summary", "since", "fired", "resolved", "resolved_why", "last_sent", "sends", "silenced_by"}`, `history` the latest first |
| `GET alerts/summary` | the counts alone, for the menu's badge |
| `GET alerts/config` | `{"version", "read_only", "rules", "webhooks", "silences", "kinds", "formats", "severities", "bounds"}`, with the alerts' `ETag`; a webhook `{"name", "url_host", "secret_set", "format", "template", "timeout_ms", "disabled", "send_resolved", "status"}`, `status` `{"sent", "failed", "dropped", "queued", "last_at", "last_ok", "last_status", "last_error", "last_took_ms"}` |
| `POST alerts/rules` | adds a rule: `{"name", "kind", "disabled", "group", "severity", "for_ms", "repeat_ms", "resolve_after_ms", "days", "threshold", "source", "webhooks"}`; `201`; `400` with `{"field", "error"}`, `409` for a name taken or 64 rules |
| `PUT alerts/rules/<name>`, `DELETE alerts/rules/<name>` | replaces (a new `name` renames it) or removes a rule; its alerts resolve |
| `POST alerts/webhooks` | adds a webhook: `{"name", "url", "secret", "format", "template", "timeout_ms", "send_resolved", "disabled"}`; `201`; `409` for a name taken or 16 webhooks |
| `PUT alerts/webhooks/<name>` | replaces it; a `url` or `secret` left empty keeps the one kept, `clear_secret` drops the secret; its rules follow a rename |
| `DELETE alerts/webhooks/<name>` | removes it; `409` while a rule names it |
| `POST alerts/webhooks/<name>/test` | sends it a test, answered once it is over: `{"ok", "status", "error", "took_ms"}` |
| `POST alerts/silences` | adds a silence: `{"rule", "node", "comment", "starts", "ends", "duration_ms"}`, a rule, a member or both; `201` |
| `DELETE alerts/silences/<id>` | ends it now; it is kept as past |
| `POST text/render?format=` | a template's config, JSON, as `conf`, `yaml` or `json` text |
| `POST text/parse?format=` | such a text as JSON, or `400` with its error |
| `GET metrics` | the series: `?node=a,b&group=g&points=90` (1 to 360; every member and group when neither is given): `{"interval_ms", "points_max", "now", "nodes", "groups"}`, a member `{"name", "status", "groups", "counters", "series"}`, `counters` `{"at", "requests": {"total", "classes"}, "connections": {"open", "refused", "pending_bytes"}, "reloads": {"succeeded", "failed"}}` as the member last gave them, a group `{"name", "nodes", "series"}`; a series is columns, `{"t", "requests", "errors_4xx", "errors_5xx", "connections", "pending_bytes", "reloads", "reloads_failed", "members"}`, `t` in Unix milliseconds, rates a second, `null` where there is none, a group's `members` the members summed at each point; any role |
| `GET certificates` | the inventory: `{"now", "certificates", "nodes"}`, a certificate `{"node", "listener", "names", "issuer", "serial", "not_before", "not_after", "days_left", "status", "read"}` the soonest to expire first, a member `{"name", "status", "count", "read"}`; any role |
| `GET logs` | a tail: `?node=a,b` (1 to 16, required), `level`, `logger`, `contains`, `limit` (each member's latest, 200 by default, 1,000 at most) and `cursor` (the last answer's): `{"records", "cursor", "nodes"}`, a record `{"node", "seq", "time", "level", "logger", "message"}` merged by time, a member `{"name", "count", "last", "error"}`; a role on each member named; one of the 8 reads while it runs |

Beside the module, at the console's top level, the node switcher's pass-through:

| Endpoint | What it does |
| --- | --- |
| `GET /api/n/<member>/workbench` | the modules of the member its pages open, `{"user", "modules", "member"}`, `member` being `{"name", "access", "url", "id", "hostname", "fit_version", "schema_digest", "manager_schema_digest", "roles", "read_only", "reason_kind", "reason"}`, `reason_kind` `version`, `not_read`, `manager` or `role`; its `GET /node` is read now |
| `GET /api/n/<member>/node` | the member's `GET /node` |
| `<method> /api/n/<member>/m/<module>/<path>` | the member's `/api/m/<module>/<path>`, its Admin API's `/<path>` for the module `admin`, with the query; answered as the member answers, but for the headers above; `502` with `{"error", "problem"}` when the member cannot be reached, `403` for what is not passed on, `409` for a change to a read-only member |

A request the user's roles do not allow is refused `403` with `{"error", "kind": "forbidden", "user",
"role", "nodes", "groups", "cluster"}`: the role it needed, and the members or groups it lacks it on.
A change sent with `If-Match` and an ETag that is no longer the document's (its members, groups
and templates), or for the alerts' changes the alerts' own, is refused `412`. At most
4 tests and 8 reads run at once; one more is `429`. The reads pass on those four GETs of a member
and nothing else, never a write; a member that does not list a read's capability (`status`,
`config.versions`, `config.check`) is not asked for it. No answer, page or log line holds a
password, a token or a member's session cookie.

The manager's Admin API serves the same reads and the rollouts under `/cluster/`, behind its token,
for `fit cluster`: `GET nodes`, `state`, `groups`, `drift`, `metrics`, `certificates`, `logs`; `POST
sync` (as the module's, but the members are told nothing); `GET`/`POST rollouts`, `GET`/`DELETE
rollouts/<id>`, `POST rollouts/<id>/start`, `pause`, `resume`, `go`, `abort`, `rollback`, answered
as the module answers them. On a fit that manages no members, `/cluster/` is `404`. A member's own
Admin API gains `GET /logs/recent?logger=&level=&contains=&limit=&after=`: `{"records", "last",
"ring_size"}`, the messages after the sequence number `after` (from the start when `after` is past
the last), at most `limit` (100 by default, 1,000 at most), the latest.

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
