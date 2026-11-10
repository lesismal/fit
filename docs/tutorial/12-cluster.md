# Cluster

[English](12-cluster.md) | [简体中文](12-cluster.zh-CN.md)

This is chapter 12 of the tutorial: the workbench's **Cluster** menu, for a fit that manages other
fits through `admin.cluster`. Its pages edit no config (the members' credentials live in the store), so
the chapter covers only `admin.cluster`'s parameters; the full field table is in
[the config reference: Admin API](../reference/config.md#admin-api), and the feature is described in
[cluster management](../cluster.md).

## What it does

A fit with `admin.cluster` is the **manager**: it keeps the addresses and administrator credentials of
the other fits (the **members**) in the `cluster` section of its store, reads each member's
`GET /node`, `/status` and `/config/check` every `poll_interval` through the member's console or Admin
API, and shows what it reads in the **Cluster** menu. The manager is not on the data path: whatever it
does or does not run, the members serve by their own configs; losing the manager loses only management.

A manager needs a console (which serves the pages) and a store: `admin.store`, or the one `fit run
-admin` keeps beside the config. `fit validate` refuses `admin.cluster` without `admin.console`, and `fit run`
refuses it without a store. Credentials are
written once, kept encrypted, and never appear in an answer, a log line or any page.

The menu shows when the console serves the module `cluster`; every page draws only what the user's role
allows (see [Users and audit](#users-and-audit)).

The menu's pages, in order: **Overview**, **Nodes**, **Groups**, **Templates**, **Rollouts**, **Drift and
alerts**, **Users and audit**, **Metrics**, **Logs**, **Certificates** and **BitTorrent**; a member's own page
(`#/cluster/node/<name>`), a group's, a template's and a rollout's pages and a distribution's page open from
these, and the node switcher opens a member's own workbench.

## Pages

### Overview

Every member's state as the manager's poller last read it, as cards or a table: the coloured state (up,
error, down, not read yet) with how long it has been so and why, fit's version, the running config's
version and source, the differences its consistency check counts, the certificate that expires first
and its days left, the connections and the upstream backends down, and the latency of its
`GET /node`. An offline member keeps its last answer, with the time it was read at. The tiles count by
state and filter by it when clicked; the filter also takes text (name or URL), a label selector
(`env=prod, region, tier!=db, !canary`), a group and a state, and sorts by state, name, latency,
certificate expiry, drift, connections or fit version; the filter lives in the URL. The page reads again
as often as the poller polls (2 to 10 seconds). A member in trouble says what is wrong and what to do
about it, and has **Ask AI** when the assistant is on. While a member has a BitTorrent client, or a
distribution is under way, a card above the filter sums the fleet's BitTorrent state up and links to the
[BitTorrent](#bittorrent) page.

### Nodes

Each member's labels, URL, access, credentials (only whether one is saved), how its certificate is
trusted, and its state, with the manager's recent changes at the end of the page. **Add a node** opens
the form: name (letters, digits, `_`, `.` and `-`), labels (`env=prod, region=eu`), URL — the member's console,
or its Admin API for a token; `https`, or `http` only to a loopback address — access (a console user's name and
password, or the Admin API's token), and for `https` how to trust its certificate (the system CAs, a CA's PEM,
or a pinned SHA-256 fingerprint). The secrets are written once and never shown again; leaving them empty on
**Edit** keeps them.
**Test** reaches a member as the form has it without saving: it shows the fit version, host and node ID,
or why it failed, and the certificate the member presented. An untrusted certificate (a self-signed one)
is trusted by its fingerprint: **Trust this certificate** shows the certificate whole, asks you to
compare it with the member's own, then pins the fingerprint into the form and tests again. Each row has
**Test**, **Trust**, **Edit** and **Remove** (with a confirm first). Every change carries the ETag the
page read; if something changed elsewhere meanwhile it is refused (`412`) and the page reads again.

**Add by enrollment** (in the bar beside Add a node) is for a member behind NAT or with no port open; it needs
`admin.cluster.link_address` (see [Members behind NAT](#members-behind-nat)): the name, labels, the token's
lifetime (as `24h` or `7d`; 24h when left empty, 30 days at most) and the URL the member reaches the manager at,
with the token and the config line to paste into the member's `admin` shown once.

### Groups

A group is a set of members: those its label selector matches, those named, or both. The page lists each
group's selector, its named members (a missing one in amber), the members it has now (counted by state)
and its variables; **Add a group** previews what it selects as you type. A group's page shows its
selector, members, variables and their states, a checkbox per member (all picked at first) and the
**operations bar**: **Reload** (each member applies its running config again), **Roll back** (each
applies again the latest version it keeps whose content differs from the running one), **Check** (its
consistency check), **Purge cache** (a zone or every zone: keys, prefixes, tags, or everything) and **Test**
(reads its `GET /node`), each listing the members it will act on and confirming first, then following each
member's result (waiting, running, done with what it did, or failed with the reason); at most
`max_concurrency` members are called at once. **Operations on any members** (on the Groups page) is the same
page over every member.

### Templates

A template is the config the manager owns on the members of the groups it is attached to: whole
top-level members, and the named elements of `listeners`, `routes`, `services` and `upstreams` one by
one; `admin` and `schema_version` stay the member's own. A member's **desired config** is its own config
with the parts the templates own replaced. **Variables** in strings render per member
(`${node.name}`, `${node.id}`, `${node.hostname}`, `${node.labels.<key>}` and the group's `${<name>}`). The
list shows each template's groups, order, the parts it owns and its members counted by sync state; a
template's page shows its form and its `.conf`, `.yaml` and `.json` text, its variables and warnings, and
each member's sync state with its value and the rendered one side by side. Below it are the **Rendered result** of a member (its config and its desired config side by side);
the list has **Compare two nodes** and **Import from a node**. Nothing is applied here:
rollouts apply.

### Rollouts

A rollout applies one change across members, batch by batch, each batch only after its check passes. It
can come from a template's change or a draft of one, a sync of members with their templates, parts of one
member's config copied to others, or a patch of members' configs (a cluster-wide admin only). **Planning**
reads every member, builds the desired config and its per-part diff, and has each member check it with its
own dry run; a member is ready, unchanged or not ready with the reason, and a member changed since the plan is
skipped. A plan not started within an hour expires. The **strategy** is chosen at start: **All at once**, **Rolling, in
batches** (members or a percentage of them per batch, a pause in seconds between batches) or **A canary first,
then the rest** (the first members by name, then the rollout waits for a person to go on). After each batch, and
the wait the form gives (**Check after**, 5 s by default), a health gate checks each of its members: reachable,
running the config the rollout applied, no more drift than before, and, when **At most 5xx (%)** is set, the
share of 5xx answers over a window (30 s by default) once at least 20 requests came (this needs metrics in the
members' configs). A failed gate stops the rollout and rolls back its batch, or every member applied, as the form
says. Each member is applied by its own `PUT /config` with `If-Match`, so a member changed since the plan is
skipped, never overwritten. At most 4 rollouts are active at once, and the last 50 are kept. The page lists the
history and **New rollout**; a rollout's page shows each member's state and diff, the strategy form and **Start
the rollout**, then the progress batch by batch with **Pause**, **Resume**, **Go on**, **Abort** (what was
applied stays) and **Roll back** (each member to the version it ran before).

### Drift and alerts

**Drift** is a member not matching what the manager expects: against its templates (out of sync or
conflict), or against its own config (the differences its consistency check found). The **Drift** tab
lists the members, can show only those with drift, and gives each its **diff**, **Fix** (plans a sync
rollout, which a person starts on the Rollouts page), **Fix all out of sync** and **Reload**. The
**Alerts** tab shows the counts, the alerts firing (with **Silence**), the pending and the past ones;
**Rules** (`member_down`, `drift`, `rollout_failed`, `cert_expiry`, `backends_down`, `reload_failed`),
**Webhooks** (their sending, **Test**, edit and delete; fit's own JSON, Slack/Mattermost/Rocket.Chat, Discord,
Feishu, DingTalk, WeCom or a body of your own; an `https` URL, or `http` to a loopback address, kept encrypted)
and **Silences** each have their own tab; with no webhook, alerts are shown here and sent nowhere. While
alerts fire that are not silenced, the Cluster menu's row shows their count, red when one is critical.

### Users and audit

The console users of a manager have **roles** on the cluster: `viewer` (reads), `operator` (also runs
operations and the rollouts of existing templates) and `admin` (also adds, changes and removes members
and their credentials and templates, makes enrollment tokens, writes on a member, and changes groups and
the roles), cluster-wide and per group, the highest of them counting. Until a role is saved or
`admin.cluster.admins` is set, every console user is a cluster-wide admin. Once roles are set, the manager's own
pages (its config, versions, consistency fixes, logs actions and the account pages' **Users**) are changed only by
a cluster-wide admin; the others read them, and still change their own password. **Users and roles** lists
every user with its cluster-wide role, its roles on groups, its assistant level, and the role of **Users not
listed**, and **Save roles** (only a cluster-wide admin may; the roles take effect at once); others read only.
The users named in `admin.cluster.admins` are marked and stay admins whatever the roles say. **Audit log** filters by user, member, operation and
time, each entry opening its diff, digest, versions and per-member results, and **Export JSON lines**; it keeps
the latest 1,000 entries, across restarts. A user
with no role is told so.

### Metrics

The series the manager keeps from each poll (the last 360): requests a second, 4xx and 5xx, the
connections and the bytes queued for clients, and the configs applied successfully or failing. One chart
with a line per member (the first 12 are drawn, and the page says so when there are more — filter by name
to see others), one with a line per group; a table lists each member's trend and its counts since it
started. The window is 30, 90, 180 or 360 polls — about five minutes to an hour at the default 10-second
poll, shorter with a shorter `poll_interval`.

### Logs

The logs of the chosen members (16 at most), tracked at once and merged by time, from each member's
`GET /logs/recent` (capability `logs.recent`); each member keeps its latest 1,000 messages in memory and the page
its latest 2,000 lines, and **Follow** reads again with the cursor
every 2 seconds. Filter by the lowest level, the logger and text; picking a group picks its members.

### Certificates

The certificates each member's last poll reported: name, issuer, serial, dates and days left, the one
expiring first at the top, filtered by text and days left; the members with no certificates or not read
yet are listed below.

### BitTorrent

The members' BitTorrent roles as the manager reads them now, and the distributions of a torrent to members
over BitTorrent. The manager reads each member's `GET /bittorrent/summary` (capability `bittorrent.summary`)
when the page, the assistant or the Admin API asks, at most `max_concurrency` members at once, each call
bounded by `timeout`; one read serves every request for 2 seconds, and a read under way is joined, never
started twice. The page reads again every 5 seconds; a member down, or one older than this view (its
capabilities lack `bittorrent.summary`), is not asked and its row says so. Any role may read the view.

Four tiles above the table: **Clients running** (the members whose client runs, of all the members),
**Torrents** (with how many are in error), **Rates** (the download and the upload rate of all the clients at
once) and **Trackers** (how many, with their torrents and peers). Then a row per member:

| Column | What it shows |
| --- | --- |
| Member | The name, opening its page, and its state when it is not up |
| Client | **running** or **not running**, and the address it takes peers at; **no client role** when the member runs no `bittorrent.client`; the problem when its summary could not be read |
| Torrents by state | How many torrents, the count of each state, and how many are in error |
| Rates | Its download and upload rate |
| Down / up | The bytes it downloaded and uploaded |
| Peers | The peers it has now |
| Published | Each `publish` entry with its torrents, and the entry's error |
| Tracker | Its tracker's torrents and peers |
| | **Open**, its own BitTorrent pages through the node switcher, when it has a client role |

Below the table is the **Distributions** card. A distribution spreads a torrent that one member, the
**source**, publishes privately — a `publish` entry with `private` set, so that the torrent is never
announced to the DHT, exchanged with other peers or announced to a tracker it does not name — to other
members: an LLM node's model, a release. Each target adds the torrent with the source and the other
targets as its only peers, into a category of its own (that category's directory), and the manager follows
it until every target has it whole. At most 64 targets and 2 distributions at once; the last 16 are kept,
the latest first, each with its state, its source with the publish entry and the torrent, and how many
targets are done, under way, failed, cancelled or waiting. Any role reads them; **New distribution** needs
`operator` on the source and on every target.

**New distribution** names the source (a member whose client publishes something), its publish entry and
the torrent (its ID, or its file's path in a `per_file` entry; empty for an entry of one torrent), the
targets — a group's members or members picked, or both — a category each target's client has (whose
directory it downloads into; its `download_dir` when empty), whether they keep seeding once done, a stall
timeout in minutes (10 when empty) and a description. The manager reads the source's summary (its client
must run), its publish entry and the torrent's `.torrent` file, which must be private; it reads each
target's summary — one without a running client fails alone (**No client**) — tells each target to add the
torrent, and reads each target's progress every 2 seconds: done once it has the torrent whole, failed when
its client reports an error, the torrent is removed, or nothing grows for the stall timeout (**Stalled**). A
target that had the torrent already is followed and never removed (**had it already**). At the end the
torrent leaves the targets that were given it — kept, and seeding by their limits, on those that have it
whole with **Keep seeding** — and what was written on those that failed or were cancelled is removed; the
audit log gets a `bt.distribute` entry.

A distribution's page follows it target by target — status, progress, rate, peers, the address given to the
others, the error — with **Cancel**, which asks first and stops the targets still downloading, removing the
torrent and what they downloaded from them. A distribution changes no config, so it holds no member against
rollouts and operations; a manager restarted forgets the distributions under way, and the members keep what
they had. The [cluster management](../cluster.md#bittorrent-the-members-torrents-and-distributions) page has
the bounds and the API, and the torrents themselves are described in the
[BitTorrent chapter](27-bittorrent.md) and [docs/bittorrent.md](../bittorrent.md). The assistant's
`cluster_bittorrent` tool reads the same view ([chapter 14](14-assistant.md)).

### A member's page and the node switcher

Clicking a member's name anywhere opens its page: the member as the manager keeps it and its state, then
what the member itself answers, read through the manager (its identity, the running config with its
reloads and connections, the listeners, the upstreams and their backends, the certificates, the saved
versions and the consistency check). The **node** box in the header switches the workbench to a member:
its pages are served through the manager, which never lets the browser hold its credentials; the member's
name appears in the title and in a bar, and its pages carry its own colour. A member whose schema differs
from the manager's, or one not read yet, opens **read only**.

### Members behind NAT

A member that links out to the manager (`admin.cluster_member`: the manager's URL, its certificate's
fingerprint and an enrollment token used once) is managed through that link exactly as the others are
over HTTP — the same polling, tests, member page, states and API. Its row says **Linked** (with the
source address) or **Waiting** (for it to enroll, until the token expires, or to link again), and **New token**
makes it a new enrollment token; it keeps linking with its credential until it enrolls with the new one. The
manager opens its link listener with `admin.cluster.link_address`; without it **Add by enrollment** says the
manager takes no links. A config a manager applies through the link shows on the member's Versions page with
the source `cluster`.

The steps: set `link_address` (and `link_url` when members reach the manager at another name or port) on the
manager and restart it; **Add by enrollment**, copy the config line shown; paste it into the member's `admin`
(as `cluster_member` below, the token in `token` or in a file named by `token_file`); start the member. After it
enrolls, its credential is kept in its store, or without one in `fit.cluster-member` beside its config, and the
token can be left out.

## Key parameters

Only the key parameters; the full table is in [the config reference: Admin API](../reference/config.md#admin-api)
([the Chinese chapter](../reference/config.zh-CN.md)).

| field | default | description |
| --- | --- | --- |
| `poll_interval` | `10s` (1s at least) | how often each member's `GET /node`, `/status` and `/config/check` are read |
| `timeout` | `10s` (1s at least) | the longest one call to a member may take: connect, the answer's header and the whole call |
| `max_nodes` | 256 (10000 at most) | the most members the manager keeps; one more is refused |
| `max_concurrency` | 8 (256 at most) | the most members polled at once, and called at once by one operation on many members; the pages' tests run beside them, 4 at most |
| `max_response_bytes` | 4194304 (4 MiB; 16 KiB to 64 MiB) | the largest answer read from a member; a larger one fails its call |
| `link_address` | off | where members that link out reach the manager: a WebSocket listener over TLS whose self-signed certificate is made once and kept in the store; without it no member can enroll |
| `link_url` | the host the page was opened at plus `link_address`'s port | the manager's URL in the enrollment config line, as `wss://manager.example.com:7444/cluster/link`; only a `wss` URL, and only with `link_address` |
| `admins` | none | console users (of `admin.console.users`) who are cluster-wide admins whatever the roles say: the way back in when no admin is left; 64 at most |

The member's side, `admin.cluster_member`, read when the member starts:

| field | default | description |
| --- | --- | --- |
| `manager` | required | the manager's link URL, `wss://host:port/cluster/link`, as **Add by enrollment** gives it |
| `fingerprint` | required | the SHA-256 of the manager's link certificate, 64 hex digits, with or without colons: the only certificate the member links to |
| `token` | none | the enrollment token, `fite1.<ID>.<secret>`, used once; left out once the member has its credential |
| `token_file` | none | a file holding the token, in place of `token` (not both) |

## In .conf

The bootstrap file (`admin` is read when fit starts and the console's pages never edit it):

```fit
# bootstrap file: admin is read when fit starts; the console's pages never edit it
admin {
	store {
		path /var/lib/fit/manager.store
		password_env FIT_STORE_PASSWORD
	}
	console {
		address 127.0.0.1:2020
		users {
			username admin
			password_hash "$2a$10$sKGBAOOCI4p4WWJcVCuAH.ivT8pNy2DFbs6pd8xmtNEZ/5L1SeLvm"
		}
	}
	cluster {
		poll_interval 10s
		timeout 10s
		max_nodes 256
		max_concurrency 8
		max_response_bytes 4194304
		link_address :7444
		link_url wss://manager.example.com:7444/cluster/link
		admins admin
	}
}
```

The simplest manager is a config holding only `admin.cluster`, run with `fit run -admin`, which adds the
console and the store with their defaults. A member reached over HTTP needs nothing in its own config: its
address, credentials and role are in the manager's store; only a member behind NAT needs
`admin.cluster_member`, as in this member's bootstrap file:

```fit
# a member behind NAT: it links out to the manager
admin {
	store {
		path /var/lib/fit/member.store
		password_env FIT_STORE_PASSWORD
	}
	cluster_member {
		manager wss://manager.example.com:7444/cluster/link
		fingerprint 3f9a1c0e5b7d2468ace13579bdf02468ace13579bdf02468ace13579bdf02468
		token_file /etc/fit/enroll.token
	}
}

listener web :8080

route { handler respond { body "hello from a member" } }
```

## See also

- [Cluster management](../cluster.md): running a manager, the pages, groups and operations, templates,
  rollouts, drift and alerts, metrics, logs and certificates,
  [the members' torrents and distributions](../cluster.md#bittorrent-the-members-torrents-and-distributions),
  how members are reached, the API, and [members behind NAT](../cluster.md#members-behind-nat).
- [The config reference: Admin API](../reference/config.md#admin-api): every field of `admin.cluster`
  and `admin.cluster_member`.
- [The console and the store](../console.md#managing-other-fits): where the Cluster menu sits.
- [Account](13-account.md) and [the assistant](14-assistant.md): the roles on a manager and the
  assistant's cluster tools, `cluster_bittorrent` among them.
- [BitTorrent](27-bittorrent.md) (the members' own pages) and [docs/bittorrent.md](../bittorrent.md); the
  [Sync](26-sync.md) chapter for the pipelines a manager's assistant reads across members.
- [Decision 033](../decisions/033-cluster-management.md).
- The front end: [app/workbench/web/cluster/](../../app/workbench/web/cluster/).
