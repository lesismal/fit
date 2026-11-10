# BitTorrent

[English](27-bittorrent.md) | [简体中文](27-bittorrent.zh-CN.md)

## What it does

**BitTorrent** is the last module in the menu, after [Sync](26-sync.md). It is one module for two
roles of one config member, `bittorrent` (tag *client / tracker*):

- **`bittorrent.client`** is a BitTorrent client that downloads and seeds on fit's own fib engine,
  beside the gateway: one `listen` port for TCP and UDP, which uTP, the DHT and the UDP trackers
  share. It takes torrents from magnet links, .torrent files, .torrent URLs and a `watch_dir`, and
  keeps them across restarts in `data_dir`: the list in `torrents.json`, each torrent's .torrent file
  under `torrents/`, and its resume data under `resume/`, so a restart adds every torrent back and
  does not hash what is on disk again (unless a file's size or modification time changed).
- **`bittorrent.tracker`** is a tracker: HTTP and HTTPS announces and scrapes through any route whose
  handler is `bt_tracker`, on fit's listeners with their TLS and middleware, and UDP ones (BEP 15) on
  its own `listen`. It is public, or private with passkeys of a `media_auth` policy, and may keep an
  allow-list of info hashes. Swarms live in memory, bounded; several instances may share them through
  Redis so that they answer as one tracker.

Both roles are off unless the config sets them: without `bittorrent.client` fit opens no BitTorrent
port and keeps no state, and without `bittorrent.tracker` it tracks nothing. The client's torrents
download below `download_dir` (`<data_dir>/downloads` when left out), with no torrent writing outside
it. [Categories](#categories-and-their-directories) give torrents a directory, their own limits and a
media library to rescan; [the queue](#the-queue) bounds how many download and seed at once;
[`min_free_bytes`](#disk-space) keeps a download from filling the disk;
[`alt_speed`](#alternative-rates) swaps the rates on a schedule; [blocklists](#seeding-ratio-and-limits)
decide which peers the client deals with. Beyond downloading, the client can
[publish a directory](#publishing-a-directory) as hybrid torrents, seeded in place with a `static` route
as their web seed, and [download from RSS feeds](#rss-auto-download) with rules. The routes' handlers
complete
the picture: `bt_publish` serves published .torrent files and magnet links, `bt_stream` plays a
torrent's file over HTTP ranges while it downloads, and `bt_tracker` serves HTTP announces and
scrapes (see [Reverse proxy](15-reverse-proxy.md) and
[the config reference: Handlers](../reference/config.md#handlers)).

The module is in the menu whenever the console runs, and it reads and changes both roles through the
Admin API's `/bittorrent/` endpoints, passed on by the console. The pages edit the config's **shared
draft**: **Save and apply** in the top bar applies the whole draft; **Check** takes the changes
through the whole apply path without applying them; **Discard** drops them. A save's problems in
`bittorrent` link to the **BitTorrent config** page, whichever page saved. What a change restarts:

| Change | What happens |
| --- | --- |
| `limits`, `access`, `categories`, `alt_speed`, `watch_dir`, `save_interval`, `url_timeout`, the queue and disk bounds, `rss`, `publish` | applied in place, keeping the connected peers (except those a lowered connection limit or the new `access` refuses) |
| `listen`, `encryption`, `dht`, `pex`, `utp`, `lsd`, `disk_workers`, `disk_queue_bytes` | a new client: every torrent's resume data is saved and the torrents are added to the new client without a check; when the new one cannot be made (its port taken), the one before is made again and the apply fails |
| `data_dir` | a new client on the new directory's state, as a restart would; the old directory keeps what was saved there |
| tracker `allow`, `auth`, `access`, `console`, `interval`, `min_interval`, the bounds, `peer_ttl` | applied in place, keeping the swarms |
| tracker `listen` | the UDP port moves, keeping the swarms |
| tracker `store` | a new store; this instance's swarms start anew in memory, the shared ones stay in Redis |

## Concepts

### A torrent, its metadata and its pieces

A torrent's data is described by **metadata** (its file tree and piece hashes), identified by its **info
hash**. fit handles v1, v2 and hybrid torrents: a torrent's **ID** is always 40 hex digits — its v1 info
hash, or for a v2-only torrent the first 20 bytes of its v2 hash; the API takes the whole 64-digit v2
hash too. Metadata arrives with a .torrent file, or with the peers when the torrent is added by a
**magnet link** (`magnet:?xt=urn:btih:…`); until then a torrent has no files to show. Files are cut into
**pieces** whose hashes are checked, so a piece is either had or not; fit reports how many of a
torrent's pieces it has.

### States

A torrent moves through these states: `metadata` (fetching the magnet link's metadata), `checking`
(checking its files against the hashes), `downloading`, `seeding`, `queued` (waiting for its turn in
the queue), `paused`, `stopped`, and `error` (a storage error does not stop the torrent: it stays in
the status until a piece passes its check again). The role also says why it **holds** a torrent:
starting, queued by the role, or short of disk space.

### Seeding, ratio and limits

Seeding is uploading what a torrent has; a torrent seeds once it has every piece it wants.
`limits.seed_ratio` (uploaded bytes over the torrent's size) and `limits.seed_time`, both counted since
the torrent was added or fit started, pause a torrent when reached — when left out, it seeds for ever —
and the status and the events say so (`seed_limit`). `limits` holds the client's rates, connections,
upload slots and seeding bounds; a category's `limits` are each of its torrents', and a torrent's own
limits take the place of those they set (zero takes the client's; a negative count, ratio or time is no
limit for that torrent). **Blocklists** are the role's `access` and the global one: `deny`/`allow`
addresses and `deny_files`/`allow_files`, which read eMule `ipfilter.dat` and PeerGuardian `.p2p` lines
beside addresses, prefixes and ranges; a refused peer is closed at once, never dialed and given no DHT
answers, and a change of the policy or a list file applies at once, closing the peers now refused.

### Categories and their directories

A category names a directory and limits (and optionally a `media_library`). A relative `dir` is below
`download_dir` and may not lead out of it (`<download_dir>/<name>` when left out); an absolute one is
taken as it is. When a torrent of the category completes, the named media library is scanned again,
so a download into its roots appears in its catalog; a torrent counts as completing once, kept across
restarts, and again only after a file priority changes. Moving a torrent to another category moves it
to that category's limits and library, but its files stay in the directory it was added with.

### The queue

`limits.max_active_downloads` and `max_active_seeds` bound the torrents downloading and seeding at
once; past them torrents queue, paused by the role, in the order of the list, which a torrent's
**queue position** changes. A torrent fetching a magnet link's metadata, or checking its data, counts
as downloading. A torrent paused through the API or by its seed limit is not counted and is never
started by the queue. No queue when the bounds are left out.

### Disk space

`limits.min_free_bytes` (1 GiB) is the space a download leaves free on its directory's disk: a torrent
starts only when the disk has what it still has to download and that much more free, and a running
download pauses with an error in its status when less is left; the role looks again every second and
starts it by itself once the space is there. Seeds need no space and are not checked.

### Alternative rates

`alt_speed` sets an upload and a download rate (0 is unlimited) that take the place of
`limits.upload_rate` and `limits.download_rate` while one of its weekly `schedules` holds (`days`,
`from`, `to` as `HH:MM`, `to` not included; a range running past midnight holds into the next day), in
`alt_speed.time_zone` (local when left out). Without `schedules` they hold only while turned on. The
Torrents page's and a torrent's top bar turns them `on` or `off` whatever the schedules say, or back to
`auto`; that choice is kept in the state across restarts.

### Adding from `watch_dir`

`watch_dir` is looked at every `watch_interval` (10s) for `.torrent` and `.magnet` files (a magnet
link on the first line that is not blank). Those directly in it are added with no category; those in
a subdirectory named after a category are added in that category. A file is taken once it has kept
its size and modification time across two looks, so one still being written is not read half way,
then moved into `.done` (added, or held already) or `.failed` with a `.error` file beside it.

### Publishing a directory

A `publish` entry builds hybrid torrents of a directory's files with fib's metainfo builder — one
torrent per file (mode `per_file`, the default), or one of the whole directory (mode `whole_dir`) — and
seeds them where the files are, through storage that refuses every write, so a published file is never
downloaded over. A published torrent seeds for ever, whatever `seed_ratio` and `seed_time` say; it is
not in the client's list of torrents nor counted by `max_torrents`. `web_seed` is the URL at which the
directory is served, as by a `static` route, put in every torrent as its web seed, so a client can
download from fit's seed, the web seed, or both. `announce` lists tracker URLs, `private` marks the
torrents private (BEP 27: peers only through the announced trackers), and `files`/`exclude` pick what is
published (names starting with `.` and symbolic links are always left out). In mode `whole_dir` the web
seed's URL must end in `dir`'s last element. An entry walks the directory every `check_interval` and
builds a file once it has stopped changing; a file changed or removed stops its torrent, and a changed
one gets a new torrent of a new info hash. `bt_publish` serves an index in JSON, each torrent's
`.torrent` file and its magnet link.

### RSS auto-download

`rss` polls RSS 2.0 (and 1.0) and Atom feeds every `interval` (15m, at least 1m), sending back the
`ETag` and `Last-Modified` so that an unchanged feed answers `304` and costs nothing more. Each new
item goes to the first rule whose `feeds`, `patterns` and `exclude` (case-insensitive globs over the
whole title, or `re:` and a regular expression), and `min_size` and `max_size` it meets; the rule adds
the item's magnet link or .torrent URL in its `category`, `paused` if it says, at most `max_per_poll`
a poll. With `episodes`, each episode (`S01E02`, `1x02`, or a date) of a show is added once. Every
item read is remembered with its outcome in `data_dir/rss.json`. A feed or a rule can be turned on or
off from the RSS page (or `fit bt rss enable|disable`), whatever the config's `enabled` says; the choice
is kept in `data_dir` until *back to the config* (`fit bt rss reset`) hands it back. A rule's `category`
must be one of `categories`.

### Playing a file while it downloads

A route whose handler is `bt_stream` serves a torrent's file with `Range`, fetching the pieces asked
for (and `readahead_bytes` past them) before all others: `/bt/<id>/<file path>`, `/bt/<id>/<n>` (the
file of index `n`) or `/bt/<id>/` (its largest file), after `prefix` (`/bt/`). With `auth`, a request
must `play` `bt/<id>` under that `media_auth` policy (a bearer token, Basic, `?token=` or the
`fit_media_token` cookie). With `magnet true` (which needs `auth`), a request carrying
`?magnet=<the torrent's magnet link>` adds a torrent the client does not have, if it may also
`publish` `bt/<id>`: such a torrent is *on the fly* — it downloads only the files streamed and is
removed with its data `temporary_ttl` after its last stream ended. Ticking **Download its pieces in
order** when adding a torrent is the other way to play one as it comes.

### Private trackers and passkeys

With `auth`, a `media_auth` policy, the tracker is private: every announce and scrape needs a
**passkey**, in the path (`/<passkey>/announce`) or the query (`?passkey=`), whose grant must play
`bittorrent/<info hash in hex>` — `play: ["bittorrent/*"]` for every torrent. A passkey is one of the
policy's tokens, or a user's passkey: 32 hex digits derived from the policy's signing key and the
user's name, which the pages look up for a user. Rotating the policy's `secret` changes every user's
passkey; without a secret, passkeys change when fit restarts. A policy whose `public` includes `play`
lets everyone in. The allow-list (`allow` and `allow_files`) keeps the tracker to the listed info
hashes; others are refused as unregistered. The access log never holds a passkey: it is written as
`REDACTED`.

### Distributions

On a manager, **Cluster → BitTorrent** shows the members' BitTorrent roles and spreads a file one
member publishes to others over BitTorrent: a **distribution**. The source publishes it with a
private `publish` entry; the manager gives each target the `.torrent` file with the source and the
other targets as its peers, into a category of the target's, follows the targets' progress, and
removes the torrent from them at the end (keeping their files) or leaves it seeding. See
[cluster management](../cluster.md#bittorrent-the-members-torrents-and-distributions) and
[the cluster chapter](12-cluster.md).

## Pages

The menu entry has five pages — **Torrents** (`#/bittorrent/torrents`), **Tracker**
(`#/bittorrent/tracker`), **RSS** (`#/bittorrent/rss`), **Publishing** (`#/bittorrent/publish`) and
**BitTorrent config** (`#/bittorrent/config`) — and a torrent has its own page,
`#/bittorrent/torrent/<id>`. A page whose role does not run here says so, with a link to the config
page.

### Torrents

The list of every torrent in queue order: name (with its error, and *on the fly* for a torrent
`bt_stream` added on request), wanted size, a progress bar, state and why the role holds it,
download and upload rate, time left at the present rate, ratio (uploaded over size, as `seed_ratio`
counts it), peers and seeders, and category. It is filtered by state (every state, or one of the
states above), by category (every category, no category, or one of the running client's
`categories`), and by a part of the name; the count says *{n} of {total} torrents*. Torrents are
picked one by one or with **Pick every torrent shown**, then paused, resumed or removed — a removal
asks first, with a checkbox to delete the downloaded files too (*Without it, the files stay on disk*)
— and each action is run on the picked torrents one after another, a toast saying how many and which
failed.

**Add torrent** opens **Add a torrent**: a **Magnet link**, a **.torrent file** (at most 32 MiB) or a
**URL** that fit downloads, a category, **Add it paused**, and **Download its pieces in order (to play
while it downloads)** — sequential download. The alternative rates are in the top bar of this page
and of a torrent's pages: a select of **auto (schedule)**, **on** or **off**, and whether they hold
now (with the rates in force, or *The config has no alt_speed: the alternative rates never hold*).

The **Recent events** card keeps the client's last 30 events since the page opened: torrents added
and removed (with or without their data), state changes, completions, storage errors, peers banned,
seed limits, the alternative rates and RSS additions, plus *client events dropped* and *events
missed: the list is read again*; a completion or a storage error is also toasted. The page reads the
list every two seconds for the rates and the progress, which no event carries; beside it, it reads
the client's events from the console's stream (below) so that a torrent added, removed or changing
state shows at once. A break reconnects with `Last-Event-ID`; without the stream (a member opened
through a manager, the bound of streams, a fit without the role) the page polls only, and the badge
says **live** or **polling**. A role that does not run here is said, with a link to the config page.

### A torrent: Overview, Files, Peers and Trackers

`#/bittorrent/torrent/<id>` shows one torrent, in four tabs (Overview, Files, Peers, Trackers; the
last three at `.../files`, `.../peers`, `.../trackers`), its head carrying its name, state, why the
role holds it, and its category, and the toolbar **Pause** or **Resume**, **Recheck** (after a confirm:
the files are checked piece by piece, the peers kept, and pieces that fail downloaded again) and
**Remove** (with the same *Delete its downloaded files too* checkbox).

- **Overview**: progress, rates, downloaded and uploaded bytes, peers and seeders, ratio and time
  left, and the pieces it has of all; a card with the ID, the v1 and v2 info hashes, the size (and
  *Wanted* when different), the directory, when it was added, why the role holds it, and its magnet
  link to copy. Its settings are edited in place: the category, the **queue position** (from 0, the
  top: the queue starts torrents in this order), and **sequential download**. **Its own limits** —
  upload rate, download rate, connections, upload slots, seed ratio and seed time (a duration as
  `72h`; `-1s` seeds for ever) — are under the client's and in place of its category's, shown with
  the limits in force now; **Save limits** saves them together, **Clear limits** removes them.
- **Files**: each file's path, size, progress and **priority** — skip, low, normal, high — set one by
  one or for the files picked (*Set the picked files to*). A magnet link whose metadata has not
  arrived has no files yet.
- **Peers**: each connected peer's address, client, connection (outgoing or incoming, uTP or TCP,
  encrypted, seed, choked, snubbed), pieces, rates and bytes.
- **Trackers**: the trackers in tier order (a later tier is asked only when those before it do not
  answer), each with its status (working, announcing, refused, not answering or not contacted, with
  its error or warning and its failures in a row), last and next announce, interval, peers, and the
  seeders, leechers and downloads of its last answer or scrape; a hybrid's v2 hash in a table of its
  own. Below them are the **peer sources** — trackers, DHT, peer exchange, local discovery, added by
  hand, other sources — each on or off with the peers found and connected, and the **web seeds** with
  their state, failures, next try and pieces being fetched.

The Files and Trackers tabs are read every four seconds, the others every two.

### Tracker

The tracker's page: its totals — torrents and peers against their bounds, announces and scrapes,
refusals by reason (*not in the allow-list*, *bad passkey*, *refused by access*) — and **Who may
announce** (*Private* or *Public*, the allow-list's count or *none: every torrent*, and the UDP
address). **Swarms** are paged 100 at a time: with an allow-list, its torrents in its order, tracked
or not; a public tracker, the torrents it has peers of, by info hash; each with its seeders, leechers,
completed count and whether it is tracked. For a private tracker, **A user's passkey** looks up a
`media_auth` user's passkey for the user's announce URL (`https://<host>/tracker/<passkey>/announce`
with the example's route) — shown to the person signed in and never kept; a public tracker has no
passkeys and says so. The totals are read every four seconds.

### RSS

The RSS auto-download made visible. **Feeds** lists each feed's name and title, URL, an **On** switch
(marked *set here, whatever the config says*, with a link *back to the config* once it was set on
this page), interval, last poll with its result (ok, not modified, error) and error, next poll, items
seen, torrents added and items failed, and **Poll now**, which says what the poll did (items, new,
added, failed, left for the next poll). **Rules** lists each rule with its switch, its feeds (or
*every feed*), category, episodes remembered, torrents added and when it last added one. **Recent
items** lists the items seen (200), filtered by feed and outcome — added, held already, episode had,
no rule, no torrent link, failed — each with the rule and a link to the torrent it added. The page is
read every ten seconds. With no `rss` (or no feed) in the config the page says where to add it, and a
`rss.json` that cannot be saved is shown above the feeds.

### Publishing

One card per `publish` entry: its directory, mode, web seed, when it was last scanned, the files
waiting to settle or being built, and the entry's **problems** (what was left out and why, expanded
from the count). Its torrents are listed with their path and ID, size, file count, state, when they
were built, and their links: the **.torrent** file to download, the magnet link, and its copy button.
A published torrent is seeded beside the client's list, so there is no link to a torrent's page; it
says where to add a publish entry when there is none.

### BitTorrent config

The whole `bittorrent` member on one form: `bittorrent.client` (its `listen`, `data_dir`,
`download_dir`, encryption and peer discovery, the disk pool, `save_interval`, `limits`,
`categories`, `watch_dir` with `watch_interval` and `watch_max_files`, `url_timeout`, `alt_speed`,
`access`, `console`, `rss` and `publish`) and `bittorrent.tracker` (its `listen`, `interval`,
`min_interval`, `peer_ttl`, bounds, `auth`, `allow` and `allow_files`, `access`, `console` and
`store`), with the UI, `.conf`, `.yaml` and `.json` tabs. It is why
`bittorrent` has its own page rather than sitting on Global settings' **Other** page; a save's
problems under this member link here.

## The API, `fit bt` and events

The Admin API serves both roles under `/bittorrent/`, behind its token and `admin.access`; the console
passes them on as `/api/m/admin/bittorrent/...`, and `/node` lists the capability `bittorrent`. There
are the torrents (`GET`/`POST /bittorrent/torrents`, and `GET`/`POST`/`PATCH`/`DELETE` on
`/bittorrent/torrents/<id>`, whose `POST` runs `{"action": "pause"}`, `{"action": "resume"}` or
`{"action": "recheck"}`, and whose `PATCH` changes the category, limits, queue position, sequential
mode or file priorities), the files,
peers and trackers of a torrent, `alt_speed`, `publish` (and a published torrent's `.torrent` file),
`rss` (its feeds, rules, items and a poll now), the tracker's `stats`, `torrents` and `passkey`, and
`summary` for a manager. A torrent that does not exist or a role that does not run answers `404`; a
bad request `400`; a torrent held already `409`; `max_torrents` reached `429`; four .torrent URL
downloads or 32 event streams running, or a client that does not run, `503` with `Retry-After`; a
.torrent URL that could not be downloaded `502`.

`fit bt` steers a running fit's client (and its RSS auto-download) through that API, found and
authenticated as `fit check` and `fit sync` find it (`-c` the bootstrap config, `-timeout`, `-json`,
and the TLS flags): `fit bt ls`, `add`, `info`, `pause`, `resume`, `rm`, `limit`, `alt` and `rss`
(its `items`, `poll`, `enable`, `disable` and `reset` subcommands, `-rule` for rules). A torrent is
named by its ID, a unique start of it, or its name. `add` takes magnet links, http and https URLs and
.torrent files, with `-category`, `-paused`, `-sequential`, `-up` and `-down`, the rates in bytes per
second with `K`, `M` or `G`; `rm -delete-data` deletes the files too; `limit` sets a torrent's own
`-up`, `-down`, `-conns`, `-slots`, `-ratio` and `-seed-time`, or `-clear`s them. It exits 1 when the
Admin API refuses one, after trying the rest.

`GET /bittorrent/events` streams the client's events as server-sent events; the console serves them on a
route of its own, `GET /api/m/admin/bittorrent/events`, behind the session, which is the stream the
pages read. Events are torrents added and removed, state changes (`previous` to `state`), the
alternative rates starting or stopping, completions, storage errors, peers banned, seed limits and RSS
additions, and the stream reconnects with `Last-Event-ID`, going on from the last 1024 events kept (a
`reset` event means the missed ones are gone, so the pages read the list again); past 256 events waiting
for a slow client it is disconnected with a `lagged` event, and at most 32 streams run at once. The
events also go to the log under the logger `bittorrent`.

The metrics are at the Admin API's `/metrics` and in OpenTelemetry's export: the client's bytes,
rates, peers, torrents by state and category, hash failures, bans, the alternative rates and RSS
polls (`fit_bittorrent_*`), and the tracker's torrents, peers, announces, scrapes and refusals by
reason (`fit_bittorrent_tracker_*`).

The [assistant](14-assistant.md) reads the roles and proposes changes to the torrents: `bt_torrents`,
`bt_torrent`, `bt_tracker_stats` and `bt_rss` at level `read`, and `bt_add_propose`,
`bt_pause_propose` and `bt_resume_propose` at level `propose`; pause and resume name the
torrents by ID, at most 100.
No passkey reaches a model: a torrent's magnet link is left out, a tracker's URL keeps its scheme,
host and last path element, a web seed's URL loses its user and query, peers their IDs, and no tool
reads a user's passkey. The propose tools add steps to the conversation's one pending proposal, which
a person applies through the Admin API after a second confirm. On a manager the read tools take
`member`.

## Key parameters

Only the key parameters; the full field tables are in
[the config reference: BitTorrent](../reference/config.md#bittorrent) and the module's own
[schema/doc](../../schema/doc/bittorrent.md).

### `bittorrent.client`

| Field | Default | Description |
| --- | --- | --- |
| `listen` | — (required) | The host and port peers reach the client at, the same for TCP and UDP (uTP, the DHT and UDP trackers share it); `:0` picks one free for both |
| `data_dir` | — (required) | The state: `torrents.json`, `torrents/<id>.torrent` and `resume/<id>.resume`; a restart resumes without hashing what is on disk again |
| `download_dir` | `<data_dir>/downloads` | Where torrents added from now on download; a torrent keeps the directory it was added with |
| `encryption` | `prefer` | `allow`, `prefer` or `require`: how connections use Message Stream Encryption (MSE) |
| `dht`, `pex`, `utp` | on | The mainline DHT, peer exchange and uTP |
| `lsd` | off | Local service discovery |
| `disk_workers` | CPUs, at most 8 | The goroutines that read, write and hash the files, never a fib worker |
| `disk_queue_bytes` | 64 MiB | The received blocks waiting to be written; past it the peers sending them are not read from |
| `save_interval` | 5s | How often, at most, resume data is saved when something changed |
| `watch_dir` | — | A directory looked at every `watch_interval` (10s) for `.torrent` and `.magnet` files; files in a subdirectory named after a category are added in that category; taken files move to `.done` or `.failed` |
| `watch_max_files` | 100 | The most files a look takes |
| `url_timeout` | 30s | The whole download of a .torrent URL (at most 32 MiB, 5 redirects, 4 at a time) |
| `categories` | — (at most 1000) | Each: `name`, `dir` (`<download_dir>/<name>`), `media_library`, `limits` |
| `alt_speed` | — | `upload_rate` and `download_rate` replacing the client's while a weekly `schedules` entry holds (at most 100); the API turns them on, off or back to auto |
| `access` | every address | The client's peer blocklists: `deny`/`allow` and `deny_files`/`allow_files` (eMule `ipfilter.dat`, PeerGuardian `.p2p`), applying at once |
| `rss` | — | The feeds and rules below |
| `publish` | — (at most 64) | The directories published as hybrid torrents |

### `bittorrent.client.limits`

| Field | Default | Description |
| --- | --- | --- |
| `upload_rate`, `download_rate` | unlimited | Bytes per second, payload of all torrents together |
| `max_conns` | 200 | The client's peer connections |
| `max_conns_per_torrent` | 50 | One torrent's peer connections |
| `max_half_open` | 32 | The dials in flight |
| `upload_slots` | 4 | The peers a torrent uploads to at once, beside one optimistic unchoke |
| `seed_ratio`, `seed_time` | — (for ever) | Pause a torrent when it has uploaded this many times its size, or seeded this long |
| `max_torrents` | 1000 | The torrents held, running or paused; one more is refused |
| `max_active_downloads`, `max_active_seeds` | — (no queue) | The torrents downloading and seeding at once; the rest queue in list order |
| `min_free_bytes` | 1 GiB | The disk space a download leaves free |

### `bittorrent.client.rss`

| Field | Default | Description |
| --- | --- | --- |
| `feeds[]` | — (at most 100) | `name`, `url` (required), `interval` 15m (at least 1m), `enabled` on, `max_items` 200, `headers`/`headers_env` |
| `rules[]` | — (at most 200) | `name`, `feeds`, `patterns`/`exclude` (globs over the whole title, or `re:` and a regular expression), `category`, `paused`, `min_size`/`max_size`, `episodes`, `max_per_poll` 5, `enabled` on |
| `history_size` | 1000 | The items of each feed remembered in `data_dir/rss.json` |
| `max_feed_bytes`, `timeout` | 4 MiB, 30s | A larger feed fails its poll; the whole download |

### `bittorrent.client.publish[]`

| Field | Default | Description |
| --- | --- | --- |
| `name`, `dir` | — (required) | The entry's name, and the directory published |
| `mode` | `per_file` | One torrent per file, or `whole_dir`: one torrent of the whole directory, named as its last element |
| `web_seed` | — | The URL at which `dir` is served, as by a `static` route, put in every torrent as its web seed (BEP 19) |
| `announce` | — | Tracker URLs (http, https or udp), each a tier of its own |
| `private` | off | Mark the torrents private (BEP 27) |
| `files`, `exclude` | — | Patterns: a name without `/`, a path under `dir` with `/`, `**` any run of directories |
| `piece_length` | picked | A power of two from 16 KiB to 64 MiB; picked per torrent's size when left out |
| `check_interval` | 10s | How often `dir` is walked again; a changed file gets a new torrent |
| `max_files`, `max_total_bytes` | 1000, 1 TiB | The bounds of an entry |
| `max_hashing` | 1 | The torrents built at once |

### `bittorrent.tracker`

| Field | Default | Description |
| --- | --- | --- |
| `listen` | — (HTTP only) | The host and port UDP announces and scrapes come to (BEP 15) |
| `interval`, `min_interval` | 30m, — | How often clients are told to announce, and how soon they may announce again |
| `peer_ttl` | 1h | How long a peer is kept after its last announce; longer than `interval` |
| `max_torrents` | 100000 | The torrents tracked at once; one more is refused as *tracker is full* |
| `max_peers_per_torrent` | 10000 | The peers kept of one torrent |
| `max_peers` | 500000 | The peers of all torrents together, about 150 bytes each |
| `max_numwant`, `max_scrape` | 200, 64 | The peers one announce gets, and the torrents one HTTP scrape may name |
| `auth` | — (public) | The `media_auth` policy whose passkeys the tracker takes: private with passkeys; grants must play `bittorrent/<info hash in hex>` |
| `allow`, `allow_files` | — (every torrent) | The info hashes tracked: 40 digits for v1, 64 for v2; files hold one a line, `#` comments |
| `access` | every address | The client addresses the tracker serves |
| `store` | — (this instance's memory) | Redis 6.2+ (or Valkey) sharing the swarms of several instances: `redis`, `prefix` `fit/bittorrent/tracker/`, `request_timeout` 2s, `refresh` 5s, `queue` 65536, `cache_torrents` 4096, `cache_peers` 100 |

## In .conf

A minimal but complete config: a client downloading and seeding, a private tracker whose passkeys
come from the `media_auth` policy, a category and an RSS rule, a published directory, and routes for
the tracker's HTTP announces, for published .torrent files and for playing a torrent's files. It passes
`fit validate -c`.

```fit
listener web :8080 http/1.1 h2c

# The tracker's passkeys: a static token, or a user's passkey, which the
# Admin API derives from the secret and the user's name.
media_auth members {
	secret replace-with-a-long-random-secret
	tokens seeder {
		token replace-with-a-long-random-token
		play bittorrent/*
	}
	users alice {
		password replace-with-a-password
		play bittorrent/* bt/*
	}
}

bittorrent {
	client {
		listen :6881
		data_dir /var/lib/fit/bittorrent
		download_dir /srv/downloads
		watch_dir /srv/torrents/watch
		limits {
			upload_rate 1048576
			max_active_downloads 3
			max_active_seeds 5
			min_free_bytes 10GiB
		}
		categories iso {
			dir iso
			limits { seed_ratio 1 }
		}
		alt_speed {
			upload_rate 131072
			download_rate 524288
			schedules { days weekdays; from 08:00; to 18:00 }
		}
		access {
			deny_files /etc/fit/level1.p2p
		}
		rss {
			feeds shows { url https://example.com/rss; interval 15m }
			rules show {
				patterns "*Show Name*1080p*"
				exclude *hevc*
				category iso
				episodes true
			}
		}
		publish releases {
			dir /srv/releases
			web_seed https://example.com/releases/
		}
	}
	tracker {
		listen :6969
		auth members
		allow_files /etc/fit/torrents.txt
	}
}

route tracker { match { path /tracker/* }; handler bt_tracker }
route torrents { match { path /torrents/* }; handler bt_publish }
route stream { match { path /bt/* }; handler bt_stream { auth members } }
```

The torrents of category `iso` download into `/srv/downloads/iso`; downloads anywhere stop when the disk
would keep less than 10 GiB free; the alternative rates hold on weekdays from 08:00 to 18:00; a
`.torrent` or `.magnet` file left in `/srv/torrents/watch` is added; the episode rule adds what it picks
into `iso`; `/srv/releases` is published with `https://example.com/releases/` as the web seed; the
tracker is private, and its HTTP announces and scrapes are served at `/tracker/announce` (and
`/tracker/<passkey>/announce`, and scrapes at `.../scrape`), its UDP ones on `:6969`; `/torrents/`
serves the index of what is published in JSON, `/torrents/<id>.torrent` a .torrent file and
`/torrents/<id>.magnet` its magnet link; `/bt/<id>/` plays a torrent's largest file to a request
carrying `members` credentials that may play `bt/*`, as alice's user and password do over Basic. The
`media_auth` users and tokens must play `bittorrent/<info hash>` to announce; `allow_files` limits what
is tracked.

Two details of the example: the `static` route the web seed's URL would need
(`route { match { path /releases/* }; handler static { root /srv/releases; strip_prefix /releases } }`)
is left out because `fit validate` reads a static root from disk; add it to serve the web seed. And
`allow_files` and `deny_files` name files read when the config is applied — a change of a list file
applies the config again.

## See also

- [BitTorrent](../bittorrent.md): the client, the tracker, [publishing](../bittorrent.md#publishing),
  [the Admin API](../bittorrent.md#the-admin-api), [the events](../bittorrent.md#events),
  [`fit bt`](../bittorrent.md#fit-bt), the metrics, the shared store, interoperability and
  [limits and security](../bittorrent.md#limits-and-security).
- [The console and the store](../console.md#bittorrent): where the BitTorrent menu sits and the
  client's event stream.
- [The config reference: BitTorrent](../reference/config.md#bittorrent) and
  [the handlers](../reference/config.md#handlers): [`bt_stream`](../reference/config.md#http-handlers-bt_stream),
  [`bt_publish`](../reference/config.md#http-handlers-bt_publish) and
  [`bt_tracker`](../reference/config.md#http-handlers-bt_tracker).
- The module's own doc and examples: [schema/doc/bittorrent.md](../../schema/doc/bittorrent.md).
- [Decision 037](../decisions/037-bittorrent.md).
- [Cluster management](../cluster.md#bittorrent-the-members-torrents-and-distributions) and
  [the cluster chapter](12-cluster.md): the members' BitTorrent page and distributions.
- [Reverse proxy](15-reverse-proxy.md): the `bt_stream`, `bt_publish` and `bt_tracker` handlers, and
  the `static` routes that serve published files as web seeds.
- [The assistant](14-assistant.md): the BitTorrent tools and their levels.
- [Sync](26-sync.md): the module before this one.
