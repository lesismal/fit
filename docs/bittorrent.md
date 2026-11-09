# BitTorrent

[English](bittorrent.md) | [简体中文](bittorrent.zh-CN.md)

fit runs BitTorrent beside its gateway ([decision 037](decisions/037-bittorrent.md)), on fit's own
fib engine, with fib's BitTorrent packages
([fib's BitTorrent docs](https://github.com/lesismal/fib/blob/main/docs/bittorrent.md)): v1, v2 and
hybrid torrents, magnet links, TCP and uTP, MSE encryption, the DHT, peer exchange, local service
discovery, HTTP and UDP trackers and web seeds. It has four parts:

- **The `bittorrent.client` role** downloads and seeds torrents on one port for TCP and UDP, and
  keeps them across restarts: torrents added from magnet links, .torrent files and URLs, or a
  `watch_dir`; categories with their own directories, limits and media library; a queue; disk
  checks; alternative rates by schedule; each torrent's limits; blocklists.
- **Publishing**: the client's `publish` entries build a directory's files into hybrid torrents and
  seed them, with fit's own static files as their web seed, following the disk; the `bt_publish`
  handler serves their .torrent files and magnet links.
- **The `bt_stream` handler** plays a torrent's file over HTTP ranges while it downloads, and may add
  a torrent by magnet link on the fly.
- **The `bittorrent.tracker` role** is an HTTP (through the `bt_tracker` handler) and UDP tracker,
  public or private with passkeys, with an allow-list.

They are steered through the [Admin API](#the-admin-api), its [event stream](#events) and
[`fit bt`](#fit-bt), and measured by [metrics](#metrics); the workbench's pages are the development
plan's I204. They are checked against libtorrent and anacrolix ([interoperability](#interoperability))
and measured on the benchmark host ([performance](#performance)); [limits and security](#limits-and-security)
says what to watch when running them on the internet.

Off by default: without `bittorrent.client`, fit opens no BitTorrent port and keeps no state; without
`bittorrent.tracker`, it tracks nothing.

A config with every part (fit's .conf format; the same in JSON and YAML, as every config;
[fit-examples' `bittorrent`](https://github.com/lesismal/fit-examples/tree/main/bittorrent) is a
runnable version of it):

```fit
listener web :8080 http/1.1 h2c
admin { address 127.0.0.1:2019 }

media_library films { roots /srv/downloads/films }

# Passkeys of the private tracker: a static token, or a user's passkey, which
# the Admin API derives from the secret and the user's name.
media_auth members {
	secret replace-with-a-long-random-secret
	tokens seeder { token replace-with-a-long-random-token; play bittorrent/* }
	users alice { password replace-with-a-password; play bittorrent/* }
}
# Who may play a torrent's file at /bt/, and add one there by magnet link.
media_auth viewers {
	tokens player { token replace-with-another-long-random-token; play bt/*; publish bt/* }
}

bittorrent {
	client {
		listen :6881
		data_dir /var/lib/fit/bittorrent
		download_dir /srv/downloads
		watch_dir /srv/torrents/watch
		encryption prefer
		limits {
			upload_rate 1048576
			seed_ratio 2
			max_active_downloads 3
			max_active_seeds 5
			min_free_bytes 10GiB
		}
		categories { name films; dir films; media_library films; limits { seed_ratio 1 } }
		categories { name iso; dir /srv/iso }
		alt_speed {
			upload_rate 131072
			download_rate 524288
			schedules { days weekdays; from 08:00; to 18:00 }
		}
		access {
			deny_files [/etc/fit/level1.p2p]
		}
		publish {
			name releases
			dir /srv/releases
			web_seed https://example.com/releases/
			announce https://example.com/tracker/announce
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
route releases {
	match { path /releases/* }
	handler static { root /srv/releases; strip_prefix /releases }
}
route stream { match { path /bt/* }; handler bt_stream { auth viewers; magnet true } }
```

Here the client seeds `/srv/releases` with `https://example.com/releases/` as the web seed and the
tracker's public announce URL; a private tracker (`auth`) needs a passkey in that URL, which every
downloader of the published torrents then shares (see [limits and security](#limits-and-security)).

## The client

`listen` is one host and port for TCP and UDP: peers connect over TCP, and uTP, the DHT and the
UDP trackers' replies share the UDP port. The client serves on the listeners' engine, as the other
roles do (fib's `Config.SharedEngine`), so it shares fit's event loops and workers; stopping the
role removes its two bindings and closes its connections, and leaves the rest of fit running.

The defaults are those of decision 037: encryption `prefer` (MSE first, plaintext when a peer
refuses), the DHT, peer exchange and uTP on, local service discovery off, and fib's peer ID prefix
`-FB0001-`. In a test or a private swarm, `dht: false` keeps the client from the public DHT
routers.

Nothing blocks a fib worker. Blocks received are copied and written, and pieces read for uploads
and hashed, on the client's disk pool of `disk_workers` goroutines (the number of CPUs, at most 8);
the blocks waiting for it are bounded by `disk_queue_bytes` (64 MiB), past which the peers sending
them are not read from until half is written. The state files are read and written by the role's
own goroutines, never on a worker.

## State in data_dir

| File | What it holds |
| --- | --- |
| `torrents.json` | The list, in queue order: each torrent's ID (its v1 info hash, or its v2 hash truncated), name, the magnet link it came from, its download directory, its category, its own limits, file priorities, whether it is paused or sequential, whether it completed, whether `bt_stream` added it on the fly (`temporary`), and when it was added; and the API's choice for the alternative rates |
| `torrents/<id>.torrent` | Its .torrent file: the bytes it was added with, or, for a magnet link, the metainfo once its info has arrived from peers |
| `resume/<id>.resume` | Its resume data (fib's `Torrent.ResumeData`): the pieces it has, and each file's size and modification time |

Every file is written to a temporary file, synced and renamed, so that a crash leaves the old file
or the new one whole; leftover temporary files are removed at start. Changes to the list (a torrent
added, removed, paused, resumed, its priorities or sequential mode) are saved at once; resume data at
most every `save_interval` (5s) when pieces were verified since the last save; everything when the
role stops or makes a new client.

At start, every torrent is added back with its resume data: the pieces it lists, in files whose
size and modification time have not changed, are had without reading them, so a restart does not
hash what is on disk again. A torrent paused by its seed limit stays paused.

What cannot be read is bounded and reported:

- `torrents.json` that does not parse, is of a later version, lists more than 100000 torrents, names
  a torrent twice, or is larger than 64 MiB keeps the role from starting, with the file in the
  error, rather than being overwritten by an empty list at the next save. Fix it or move it away
  and apply the config again.
- A torrent whose .torrent file does not read, or that has neither a .torrent file nor a magnet
  link, stays listed with its error, and saved, until it is removed; the other torrents run.
- Resume data that does not read is left out, and that torrent's data is checked in full.

## Downloads

Torrents added download into `download_dir` (`<data_dir>/downloads` when left out), each torrent's
files below it as every client lays them out. fib's metainfo refuses paths with `..`, `.`, empty or
`/`-holding components, and its file storage makes each name safe for the system, so no torrent
writes outside the directory. A torrent keeps the directory it was added with when `download_dir`
changes. A storage error, as a file that cannot be written, does not stop the torrent; it is in the
torrent's status until a piece passes its check again, and in the log.

## Adding torrents

- **A magnet link or a .torrent file** through the role's API (`Add`), in a category or none, paused
  or not, with its own limits.
- **A .torrent URL** (`AddURL`), http or https: downloaded with fib's asynchronous HTTP client on
  fit's engine, its callback only handing the answer over, so no fib worker waits. At most 5
  redirects are followed, one to a magnet link included; the whole download, connecting and the
  body, is bounded by `url_timeout` (30s), the body by 32 MiB, and at most 4 downloads run at once
  (one more is refused at once), so at most 128 MiB of .torrent bodies are held.
- **`watch_dir`**: looked at every `watch_interval` (10s) for `.torrent` files and `.magnet` files
  (a magnet link on the first line that is not blank). Those directly in it are added with no
  category, those in a subdirectory named after a category in that category; other files,
  subdirectories and names starting with `.` are left alone. A file is taken once it has kept its
  size and modification time across two looks, so that one still being written is not read half
  way. One added, or whose torrent is held already, moves into the `.done` subdirectory beside it;
  one that cannot be added (not a torrent, not a link, past its size bound) into `.failed`, beside a
  `<name>.error` file saying why; one that may be added later (`max_torrents` reached, the state not
  written) stays and is tried at each look. A look takes at most `watch_max_files` (100) files and
  reads a bounded number of directory entries for them.

## Categories

`categories` name directories and limits for torrents. A category's `dir` is below `download_dir`
when relative (`<download_dir>/<name>` when left out) and may not lead out of it; an absolute `dir`
is taken as it is. Its `limits` are each torrent's (fib's `TorrentLimits`: rates under the client's,
connections, upload slots, seed ratio and time), and a torrent's own limits take the place of those
they set; zero takes the client's, and a negative count, seed ratio or seed time is no limit for the
torrent. `media_library` names a library that is scanned again (`Library.Rescan`) when a torrent
of the category completes, so that a download into one of its roots appears in its catalog; a
torrent counts as completing once, its completion kept in the state across restarts, and again only
after a file priority changes. `SetCategory` moves a torrent to another category's limits and
library; its files stay in the directory it was added with.

## The queue

`limits.max_active_downloads` and `limits.max_active_seeds` bound the torrents downloading and
seeding at once; past them torrents queue, paused by the role (status `queued`), in the order of
the list, which `SetQueuePosition` changes and the state file keeps. Every second, and at once after
an add, a pause, a resume, a removal or a move, the role starts the first torrents of the list that
fit and pauses the rest, so a finished, paused or removed torrent makes room for the next; a
torrent moved ahead of a running one takes its place. A torrent fetching a magnet link's metadata,
or still checking its data, counts as downloading. A torrent paused through the API, or by its seed
limit, is not counted and is never started by the queue. No queue when left out.

## Disk space

`limits.min_free_bytes` (1 GiB, as a media library's uploads leave) is the space a download leaves
free on its directory's disk: a torrent starts downloading only when the disk has what it still has
to download and that much more free (torrents started in the same look counting what the earlier
ones need), and a running download pauses, with an error in its status saying how much is free,
when less than `min_free_bytes` is left. Every second the role looks again, and starts it by itself
once the space is there. Seeds need no space and are not checked. Where the free space cannot be
told (systems other than Linux, macOS, FreeBSD and Windows), it is not checked.

## Alternative rates

`alt_speed` sets an upload and a download rate (0 is unlimited) that take the place of `limits`'
while one of its `schedules` holds: each a weekly range, the `days` it begins on (`mon` to `sun`,
`weekdays`, `weekend`; every day when left out), `from` and `to` as `HH:MM` in `time_zone` (local by
default), a range whose `to` is earlier than its `from` running past midnight into the next day. The
role looks every second, and the change applies in place (`Client.SetLimits`). The API's
`SetAltSpeed` turns them `on` or `off` whatever the schedules say, or back to `auto`; that choice is
kept in the state across restarts.

## Blocklists

The role's `access`, and the global one, decide which peers it deals with (fib's `Config.Filter`):
a refused peer is closed as soon as it connects over TCP, its uTP connection ignored, never dialed,
and given no DHT answers. Their `deny_files` and `allow_files`, as every module's, read eMule
`ipfilter.dat` lines (`first - last , level , name`, where a level above 127 is not listed) and
PeerGuardian `.p2p` lines (`name:first-last`) beside addresses, prefixes and ranges, told apart line
by line ([access](reference/config.md#access)). A change of the policy, or of a list file, applies
at once: the role refilters (`Client.Refilter`), closing the connected peers it now refuses.

## Reloads

| Change | What happens |
| --- | --- |
| `limits` | Applied in place (fib's `Client.SetLimits`): new rates within a quarter second, peers kept, but those over a lowered connection limit |
| `access` | Applied in place: the client's filter asks the new policy from the next peer on, and the connected peers it now refuses are closed (`Client.Refilter`) |
| `save_interval`, `console`, `download_dir`, `watch_dir`, `watch_interval`, `watch_max_files`, `url_timeout` | Applied in place |
| `categories` | Applied in place: limits to the category's torrents at once, a new `dir` to the torrents added from then on; a torrent whose category is gone keeps its directory and loses its limits |
| `max_active_downloads`, `max_active_seeds`, `min_free_bytes`, `alt_speed` | Applied in place, the torrents started and paused by them at once |
| `listen`, `encryption`, `dht`, `pex`, `utp`, `lsd`, `disk_workers`, `disk_queue_bytes` | A new client: the old one is closed, every torrent's resume data saved, and the torrents added to the new one without a check. When the new one cannot be made (its port taken), the old one is made again and the reload reports the error |
| `data_dir` | A new client on the new directory's state, as a restart would; the old directory keeps what was saved there |

## Publishing

`publish` entries publish directories as torrents (I202). Each entry builds hybrid torrents of its
`dir` with fib's `metainfo.Builder`, a single-file torrent of each file (mode `per_file`, the
default) or one torrent of the whole directory, named as its last element (mode `whole_dir`), and
seeds them where the files are: the torrents' storage reads the files in place and refuses every
write, so that a piece that fails its check is never downloaded over a published file, and
removing a torrent never deletes its files. A published torrent seeds for ever, whatever
`limits.seed_ratio` and `seed_time` say; it is not in the role's list of torrents (`List`) nor
counted by `max_torrents`, and the `bt_publish` handler serves it.

```fit
bittorrent {
	client {
		listen :6881
		data_dir /var/lib/fit/bittorrent
		publish {
			name releases
			dir /srv/releases
			web_seed https://example.com/releases/
			announce https://example.com/announce
			files *.iso *.tar.gz
		}
	}
}

route {
	match { path /releases/* }
	handler static { root /srv/releases; strip_prefix /releases }
}

route { match { path /torrents/* }; handler bt_publish }
```

- **`web_seed`** is the URL at which `dir` is served, as by fit's own `static` route above, put in
  every torrent as its web seed (BEP 19): in mode `per_file` a file's own URL
  (`https://example.com/releases/sub/a.iso`), in mode `whole_dir` the URL's parent, since clients ask
  `<url>/<name>/<path>` and the torrent is named as the directory; the config refuses a `whole_dir`
  entry whose URL does not end with the directory's name. A client can then download a published
  file from fit's seed, from the web seed, or both.
- **`announce`** are tracker URLs (http, https, udp), each a tier of its own, as fit's own
  `bt_tracker` route; **`private`** marks the torrents private (BEP 27). Without trackers, peers
  find the torrents by the DHT.
- **`piece_length`** is a power of two from 16 KiB to 64 MiB, or picked for each torrent's size
  (fewer than 2048 pieces, up to 16 MiB).
- **`files`** and **`exclude`** are patterns, as a media library's: without `/` a name, with `/` a
  path under `dir`, `**` any run of directories. Names starting with a dot and symbolic links are
  always left out, the links because one could publish what lies outside `dir`; so is a name the
  client's storage would write differently (on Windows, `<>:"\|?*`), and, in mode `per_file`, an
  empty file.

**Following the disk.** An entry walks `dir` every `check_interval` (10s) on a goroutine of its
own. A file is told by its size, modification and status change times and inode, as static files
are, so a file replaced by another of the same size and time is seen too. A file new or changed is
built once it has not changed for a `check_interval` (its status change time is that old, or the
walk before saw it the same), so that a file still being copied is not hashed; it is hashed on one
of the entry's `max_hashing` goroutines (1), never on a fib worker, and kept only if its stamp is
still the same afterwards. A file changed or removed stops its torrent at once, and a changed one
gets a new torrent, of a new info hash, once it settles; in mode `whole_dir` any change replaces
the torrent. A build that fails (a file that cannot be read) is reported and not tried again until
the file changes. A directory that cannot be read is reported and its torrents are left as they
are; so are those under a part of it that cannot be read.

**State.** The torrents are kept in `data_dir/publish/<name>/`: `index.json` (each torrent's files
and their stamps), `<id>.torrent`, and `<id>.resume`, its resume data, saved once it seeds. A restart
takes from there the torrents whose files kept their stamps, with their resume data, building and
checking none of them; a reload that changes only an entry's bounds or `check_interval` does the
same. A change of `dir`, `mode`, `web_seed`, `announce`, `private` or `piece_length` builds the
torrents again (their bytes change), and an entry taken out of the config has its torrents stopped
and its state removed, never its files. A torrent built for the first time is still checked once by
the client when added, as fib has no way yet to add a torrent as complete (see the open items of
I202 in the development plan).

**The `bt_publish` handler** answers at a path ending in `/` or `index.json` with an index in JSON:
for each entry (`publish` names those served; all when left out) its torrents, each with its path,
name, size, info hashes, magnet link, state (`seeding`, `checking`, `stopped` while no client runs)
and `torrent_url` and `magnet_url` relative to the index, the files waiting to be built
(`pending`), and what was left out and why (`problems`, at most 20). `<id>.torrent` is a .torrent
file, `<id>.magnet` a magnet link as text, an ID being the v1 info hash in hex. It answers `503`
while no `bittorrent.client` runs, so the route is safe to keep in a config without one. It reads
a snapshot of the entries and takes no lock of the role's.

| Bound | Default |
| --- | --- |
| `publish`, entries | at most 64 |
| `max_files`, files an entry publishes | 1000 (at most 100000) |
| `max_total_bytes`, their sizes together | 1 TiB |
| `max_hashing`, torrents built at once per entry | 1 (at most 16) |
| A walk | 10 × `max_files` + 10000 entries |
| `index.json` | 64 MiB |

Past `max_files` or `max_total_bytes`, mode `per_file` publishes the first files by path and
reports the rest; mode `whole_dir` publishes nothing and reports it. A file two entries (or a
file and a torrent added by hand) share is seeded once; the second is listed with its error.

## Bounds

| Bound | Default |
| --- | --- |
| `limits.max_torrents`, torrents held, running or paused | 1000 (at most 100000) |
| `limits.max_conns`, peer connections | 200 |
| `limits.max_conns_per_torrent` | 50 |
| `limits.max_half_open`, dials in flight | 32 |
| `limits.upload_slots`, per torrent | 4 |
| `limits.upload_rate`, `limits.download_rate` | unlimited |
| `limits.seed_ratio`, `limits.seed_time` | seed for ever |
| `limits.max_active_downloads`, `limits.max_active_seeds` | no queue |
| `limits.min_free_bytes`, disk left free by downloads | 1 GiB |
| `categories`, `alt_speed.schedules` | at most 1000, 100 |
| A .torrent file by URL; downloads by URL at once; redirects | 32 MiB; 4; 5 |
| `url_timeout` | 30s |
| `watch_max_files`, files a look at `watch_dir` takes | 100 (at most 10000) |
| `disk_queue_bytes`, received blocks waiting for the disk | 64 MiB |
| A .torrent file | 128 MiB (fib's metainfo bound) |
| A magnet link | 64 KiB |
| `torrents.json`, a resume file | 64 MiB each |

## The role's API

The role has a Go API that the later items build on (the Admin API, `fit bt`, the pages): `Add` a
magnet link or .torrent bytes (with a category and limits), `AddURL`, `List` (in queue order),
`Get` (with the files), `Pause`, `Resume`, `Recheck`, `Remove` with or without the data,
`SetFilePriority`, `SetSequential`, `SetCategory`, `SetTorrentLimits`, `SetQueuePosition`,
`AltSpeed` and `SetAltSpeed`, `Peers`, `Trackers`, `Subscribe` (the events), `Save`, and `Torrent`
for fib's torrent. A status says a torrent's category, its own
and effective limits, its queue position, whether it is complete, and why the role holds it
(`queued`, `disk`). The
role's console, when set, serves `GET /api/torrents` and `GET /api/torrents/<id>` read-only for
now, and any console that shows the role's module serves them as
`/api/m/bittorrent.client/torrents` and `/api/m/bittorrent.client/torrents/<id>`.

The API has hooks for `bt_stream`'s temporary torrents too: `AddTemporary` (bounded by the
caller's `max_temporary`), `SetTemporary`, `RemoveTemporary` (only while still temporary, with its
data), `TemporaryBytes` and `SetFilePriorities` (every file's priority, saved once).

## The Admin API

The Admin API serves the client and the tracker under `/bittorrent/`, behind its token and
`admin.access` as every endpoint, and the console passes them on as `/api/m/admin/bittorrent/...`
(the event stream excepted). `/node` lists the capability `bittorrent`. Each call of the role's
API runs on the request's goroutine (net/http's on the Admin API, the console's pool behind the
console), never on a fib worker.

| Endpoint | What it does |
| --- | --- |
| `GET /bittorrent/torrents` | the torrents in queue order: `{"torrents", "total", "offset"}`; `state` (states with commas), `category` (`category=` for those of none), `q` (in the name, any case), `offset`, `limit` (1000, at most 10000) |
| `POST /bittorrent/torrents` | add one, answered `201` with its status and `Location`: JSON `{"magnet"` or `"url"` or `"torrent"` (base64), `"category"`, `"paused"`, `"sequential"`, `"priorities"`, `"peers"`, `"limits"}`; a .torrent file as `application/x-bittorrent` with `category`, `paused` and `sequential` in the query; or a form (`multipart/form-data`) with the file in the part `torrent` and the same fields |
| `GET /bittorrent/torrents/<id>` | a torrent's status, with its files |
| `POST /bittorrent/torrents/<id>` | `{"action": "pause"}`, `"resume"` or `"recheck"`; answers the status |
| `PATCH /bittorrent/torrents/<id>` | `{"paused", "sequential", "category", "limits" (null clears them), "queue_position", "priorities": [{"index", "priority"}]}`, what is left out unchanged; answers the status |
| `DELETE /bittorrent/torrents/<id>` | remove it; `?delete_data=1` with its files |
| `GET /bittorrent/torrents/<id>/files` | `{"files"}`: index, path, length, completed, priority |
| `GET /bittorrent/torrents/<id>/peers` | `{"peers"}`: address, ID, client, direction, uTP, encryption, seed, pieces, bytes and rates, choking |
| `GET /bittorrent/torrents/<id>/trackers` | `{"trackers": [{"url", "tier"}], "web_seeds"}`, as its .torrent file or magnet link lists them |
| `GET /bittorrent/alt_speed` | the alternative rates: mode, configured, active, the rates in force; `PUT` `{"mode": "auto"}`, `"on"` or `"off"` |
| `GET /bittorrent/publish` | the publish entries and their torrents; `/bittorrent/publish/<name>` one |
| `GET /bittorrent/events` | the events, as server-sent events (below) |
| `GET /bittorrent/tracker/stats` | the tracker's totals |
| `GET /bittorrent/tracker/torrents` | the allow-list's swarms (`offset`, `limit` at most 1000); `/bittorrent/tracker/torrents/<info hash>` one torrent's |
| `GET /bittorrent/tracker/passkey?user=` | a user of the private tracker's policy, and its passkey |

A torrent's `<id>` is its ID, the v1 info hash in hex, in either case, or its v2 hash (64 digits),
whose first 20 bytes are the ID. `limits` take `upload_rate`, `download_rate`, `max_conns`,
`upload_slots`, `seed_ratio` and `seed_time`, a duration (`"12h"`, `"-1s"` to seed for ever) or
nanoseconds as a status gives it. A `PATCH` makes its changes in that order, each saved; the first
that fails is answered and those before it stay made.

Adding a torrent that `bt_stream` added on the fly keeps it as any other torrent, as it is, rather
than answering `409`. A .torrent file sent is at most 32 MiB, as one downloaded by URL, read as it
arrives and refused with `413` past the bound (a form's other fields 64 KiB each, 16 parts at most);
a URL is downloaded by the role, within `url_timeout`.

| The role's error | Status |
| --- | --- |
| the role does not run here | 404 |
| no such torrent | 404 |
| a bad request: a link, a file, a category, a priority, a mode | 400 |
| added already; a torrent that could not be restored, which may only be removed | 409 |
| `max_torrents` held | 429 |
| four .torrent files being downloaded already; 32 event streams | 503 with `Retry-After` |
| the client does not run, as after a port it could not take | 503 with `Retry-After` |
| a .torrent file that could not be downloaded | 502 |

## Events

`GET /bittorrent/events` streams the client's events as server-sent events (`text/event-stream`),
each with its `id`, its type as `event`, and as `data` the event in JSON: `id`, `time`, `type`, and
as they apply `torrent`, `name`, `category`, `state`, `previous`, `error`, `peer`, `data_deleted`.

| Type | When |
| --- | --- |
| `added` | a torrent was added (`state` its state then) |
| `removed` | a torrent was removed (`data_deleted` with its files) |
| `state` | a torrent's state changed, `previous` to `state` (`error` why, for `error`); the states are a status's: `metadata`, `checking`, `downloading`, `seeding`, `queued`, `paused`, `stopped`, `error` |
| `completed` | a torrent has every piece it wants |
| `error` | its storage failed, which does not stop it |
| `peer_banned` | a peer, or every peer of an address, was banned, `error` why |
| `seed_limit` | a torrent reached its seed ratio or time and paused |
| `alt_speed` | the alternative rates started (`state` `on`) or stopped (`off`) holding |

State changes are noticed by the role's manager every second, and at once after the API's
changes. With no event to send, the stream writes a comment every 15s. A client that reconnects with
`Last-Event-ID` (or `?after=<id>`) first gets the events it missed, of the last 1024 the role keeps;
when some are no longer kept, or fit started again since (the IDs count from 1 at each start), the
stream begins with an event `reset`, after which the client reads the torrents again. Each stream
has a buffer of 256 events: a client that does not take them fast enough is not skipped silently
but sent an event `lagged` and disconnected, to reconnect from the last ID it had. At most 32
streams run at once (`503` past them). When the role stops, an event `end` closes the stream; when
the Admin API shuts down, too. The stream needs a connection of its own, so it is served on the
Admin API's address and not through the console (`501` there).

The events go to fit's log too, under the logger `bittorrent`: torrents added and removed, bans and
seed limits at info, completions and the alternative rates at info as before, storage errors at
error, state changes at debug.

## fit bt

`fit bt` steers a running fit's client through its Admin API, found and authenticated as `fit check`
and `fit sync` find it (`-c` the bootstrap config, `-timeout`, `-json`, and the TLS flags):

```sh
fit bt ls -state downloading,queued -category films
fit bt add -category films 'magnet:?xt=urn:btih:...' https://example.com/x.torrent ./y.torrent
fit bt info 3f2a91                      # a unique start of the ID, or the name
fit bt pause 3f2a91 sintel
fit bt resume 3f2a91
fit bt rm -delete-data 3f2a91
fit bt limit -down 2M -ratio 2 -seed-time 72h 3f2a91
fit bt limit -clear 3f2a91
fit bt alt on                           # auto, on, off; alone it shows them
```

`add` takes magnet links, http and https URLs and .torrent files, `-paused`, `-sequential`, and
`-up` and `-down` for the torrents' own rates; rates are bytes per second with `K`, `M` or `G`
(binary) after them. It exits 1 when the Admin API refuses one, after trying the rest, and 2 when
used wrongly.

## Metrics

The roles' metrics go with fit's at the Admin API's `/metrics` and in OpenTelemetry's export. The
client's are taken every second by its manager and read by a scrape without a lock of the role's;
fib's counts of a torrent's bytes and failed pieces are added to its category's totals as they grow,
so the counters keep what removed torrents counted and never go back while fit runs. `category` is
`""` for the torrents of none; sum over it for the client's totals.

| Metric | Type | Labels |
| --- | --- | --- |
| `fit_bittorrent_client_running` | gauge | |
| `fit_bittorrent_downloaded_bytes_total`, `fit_bittorrent_uploaded_bytes_total` (payload) | counter | `category` |
| `fit_bittorrent_download_rate_bytes`, `fit_bittorrent_upload_rate_bytes` | gauge | `category` |
| `fit_bittorrent_peers`, `fit_bittorrent_seeders` (connections) | gauge | `category` |
| `fit_bittorrent_torrents` | gauge | `category`, `state` |
| `fit_bittorrent_hash_failures_total` | counter | `category` |
| `fit_bittorrent_peers_banned_total` | counter | |
| `fit_bittorrent_alt_speed_active` | gauge | |
| `fit_bittorrent_event_subscribers`, `fit_bittorrent_event_subscribers_dropped_total` | gauge, counter | |
| `fit_bittorrent_published_torrents` | gauge | `entry` |
| `fit_bittorrent_tracker_torrents`, `fit_bittorrent_tracker_peers`, `fit_bittorrent_tracker_max_torrents`, `fit_bittorrent_tracker_max_peers`, `fit_bittorrent_tracker_allowed` | gauge | |
| `fit_bittorrent_tracker_announces_total`, `fit_bittorrent_tracker_scrapes_total` | counter | |
| `fit_bittorrent_tracker_refused_total` | counter | `reason`: `unregistered`, `passkey`, `access` |

## Playing a file while it downloads

The route handler `bt_stream` serves a file of a torrent of the role over HTTP while the torrent
downloads, so that a browser's `<video>` or a player plays it as it streams in, seeking as it
likes:

```json
{
  "media_auth": [{"name": "site", "tokens": [{"name": "player", "token": "a-long-random-token", "play": ["bt/*"], "publish": ["bt/*"]}]}],
  "routes": [{"match": {"path": ["/bt/*"]}, "handler": {"module": "bt_stream", "params": {"auth": "site", "magnet": true}}}]
}
```

| URL | What it serves |
| --- | --- |
| `/bt/<info hash>/<file path>` | The file of that path as the torrent lists it, its name first: `/bt/<hash>/Show/S01E01.mkv` |
| `/bt/<info hash>/<n>` | Its file of index `n` in the info dictionary's file list (a path wins over an index when a file is named with digits alone) |
| `/bt/<info hash>/` | Its largest file, the film of a torrent with a sample and a few pictures |

The info hash is the role's torrent ID (its v1 info hash, or its v2 hash truncated), 40 hex
digits; `prefix` changes `/bt/`. A request may be `GET` or `HEAD`, with a single `Range`
(`206 Partial Content`, or `416` with `Content-Range: bytes */<size>` past the end; several
ranges, or a malformed one, are answered with the whole file, as RFC 9110 allows), and
`If-None-Match` and `If-Range` against the `ETag`, which is the torrent and the file's index,
since a torrent's data never changes. `Content-Type` comes from the extension (video, audio,
subtitles, pictures, `application/octet-stream` otherwise); `Accept-Ranges: bytes` is always sent;
`cache_control` and `cors` add their fields.

**How a range is read.** Each response reads through fib's `Reader` of the file: the pieces from
its position to `readahead_bytes` past it (8 MiB) are fetched before every other, nearest first,
so the range asked for downloads first, and a read waits for its piece at most `read_timeout`
(60s). A response begins with its first chunk, so a range whose first piece never comes is
answered `504`; one cut short later is closed short of its `Content-Length`, which the client
sees. A torrent still waiting for its info (a magnet link) is waited for `info_timeout` (60s),
then `504`.

**Nothing blocks a fib worker.** The role's API, which reads and writes the state files, and the
`Reader`, whose reads wait for pieces and read the disk, run on a goroutine of the stream's own:
at most `max_streams` (32) of them, the handler's bounded pool, past which a request is answered
`503` with `Retry-After`. A stream reads a 64 KiB chunk, hands it to the connection without
waiting, and when the client has more than fib's watermark queued, its goroutine ends, started
again by `OnWritable` once the client has taken half: a slow client costs a slot and about two
chunks, never a goroutine waiting on it, and each such wait is bounded by `limits`'
`write_stall_timeout`. A client that leaves ends its stream at once, even one waiting for a
piece: its reader is closed and its slot given back. A config swapped out ends its streams once
the drain timeout passes.

**Authorization.** With `auth`, every request must have `play` on `bt/<info hash>` from the
`media_auth` policy: a bearer token, Basic, the `token` query parameter, the `fit_media_token`
cookie, or, with `url_signing`, a URL signed for `<prefix><info hash>/`.

**Adding by magnet link on the fly.** With `magnet` (which needs `auth`), a request whose `magnet`
query parameter is a magnet link of the torrent its path names adds that torrent when the role
does not have it, provided the request also has `publish` on `bt/<info hash>`:
`/bt/<hash>/?magnet=magnet%3A%3Fxt%3Durn%3Abtih%3A...`. The link must name the path's torrent
(`400` otherwise); a request for a torrent the role has plays it as any other. Such a torrent is
**temporary**:

- it downloads only the files streamed: the others are set to priority 0, and raised as another
  file is asked for;
- at most `max_temporary` (4) are held at once (`503` past it), and they may want at most
  `max_temporary_bytes` (16 GiB) of the disk together: a torrent whose files would take them past
  it is answered `507`, and removed when it was just added; they count against the role's
  `max_torrents` too;
- `temporary_ttl` (10m) after its last stream ended it is removed with its data, unless a stream
  came back meanwhile: a player's next range within that time finds it still there;
- the role removes, with their data, the temporary torrents a stop or a crash left, when it
  starts.

| Param | Default |
| --- | --- |
| `prefix` | `/bt/` |
| `max_streams` | 32 (at most 4096) |
| `read_timeout` | 60s |
| `readahead_bytes` | 8 MiB (64 KiB to 256 MiB) |
| `info_timeout` | 60s |
| `max_temporary` | 4 (at most 1000) |
| `max_temporary_bytes` | 16 GiB |
| `temporary_ttl` | 10m (at most 7 days) |

## The tracker

The `bittorrent.tracker` role is a BitTorrent tracker, fib's
([`fib/bittorrent/tracker`](https://github.com/lesismal/fib/blob/main/docs/bittorrent.md)), one a
process, off unless the config sets it. HTTP and HTTPS announces and scrapes (BEP 3, 23, 7 and 48)
come through any route whose handler is `bt_tracker`, on fit's listeners, with their TLS, access
and middleware; UDP ones (BEP 15, with BEP 41's URL data) come to the role's own `listen`, which
may be left out for an HTTP-only tracker. A request is an announce when the last segment of its
path begins with `announce`, and a scrape when it begins with `scrape`, so a route matching
`/tracker/*` serves `/tracker/announce` and `/tracker/<passkey>/announce` alike. Every announce
and scrape is served on the worker that read it: the store takes a short lock of one of its 64
shards, and the hooks below are lookups in what the config compiled.

```fit
bittorrent {
	tracker {
		listen :6969
		auth members
		allow_files /etc/fit/torrents.txt
	}
}
routes {
	route { match { path /tracker/* }; handler bt_tracker }
}
```

**Swarms** live in memory, in fib's `MemoryStore`, and do not survive a restart: clients announce
again within their interval. A peer is forgotten `peer_ttl` (1h) after its last announce, which
must be longer than `interval` (30m), and a torrent with no peers left is forgotten with its
completed count.

| Bound | Default | At most |
| --- | --- | --- |
| `max_torrents`, torrents tracked at once; one more is refused "tracker is full" | 100000 | 1048576 |
| `max_peers_per_torrent`; a peer past it gets peers but is not handed out | 10000 | 65536 |
| `max_peers`, of all torrents, about 150 bytes each | 500000 | 4194304 |
| `max_numwant`, peers in one answer (50 when the client asks for none; UDP answers within 1400 bytes) | 200 | 1000 |
| `max_scrape`, torrents in one HTTP scrape (a UDP one holds 74 at most) | 64 | 1000 |
| The allow-list, inline and from files | | 1048576 info hashes, 64 MiB a file |

**Private.** With `auth`, the name of a `media_auth` policy, every announce and scrape needs a
passkey, in the path (`/<passkey>/announce`) or the query (`?passkey=`), and the passkey's grant
must play the stream `bittorrent/<info hash in hex>`: `play: ["bittorrent/*"]` for every torrent,
or one hash for one torrent. A passkey is one of the policy's tokens, static or signed by fit
(`fit media-token`, the media pages), or a user's passkey: 32 hex digits derived from the
policy's signing key and the user's name, which the role's console hands out at
`/api/passkey?user=<name>`. Rotating the policy's secret, or its keyring's default key, changes
every user's passkey; without a secret, passkeys change when fit restarts. Over UDP the passkey
comes in the URL data clients send from the tracker URL, `udp://host:6969/<passkey>/announce`.
Refusals are failure reasons: "a passkey is required", "unknown passkey", "the passkey may not use
this torrent". The passkey is replaced by `REDACTED` in the access log. A policy whose `public`
includes `play` lets everyone in. While the policy is not loaded the tracker refuses every
request.

**The allow-list.** With `allow` (hex info hashes: 40 digits for v1, 64 for v2, tracked by their
first 20 bytes) or `allow_files` (one a line, `#` comments), only those torrents are tracked:
others are refused "unregistered torrent" and scrapes of them get nothing. The files are watched
as certificate and access list files are: a change applies the config again and they are read
anew. A file with a line that is not an info hash, or one that does not read, refuses the
reload, and the list before stays in force.

**Access.** The role's `access`, then the global one, drops UDP datagrams from refused addresses
before fib makes a connection for them, and answers HTTP announces and scrapes from them with the
failure reason "refused" (the route's listener has the global access too).

**Reloads.** The allow-list, `auth`, `access` and the console change in place. `interval`,
`min_interval`, `max_numwant` and `max_scrape` make a new fib tracker on the same store, so the
swarms stay, and UDP connection IDs stay good (the role keeps one secret). `max_torrents`,
`max_peers_per_torrent`, `max_peers` and `peer_ttl` make a new store, since fib's `MemoryStore`
takes them only when it is made: the swarms start empty and fill as clients announce again. A
`listen` change moves the UDP port; one that cannot be bound fails the reload and leaves the
tracker as it was.

**Statistics.** The role's Go API has `Stats` (torrents and peers kept, announces and scrapes
served, refusals by reason, the bounds), `Torrent(hash)` (seeders, leechers, completed) and
`Torrents`, the swarms of the allow-list's torrents; its console serves them read-only as
`GET /api/stats`, `/api/torrents` (`offset`, `limit`, at most 1000) and `/api/torrents/<hash>`,
and as `/api/m/bittorrent.tracker/...` as a module. A public tracker cannot list the torrents it
tracks: fib's `MemoryStore` answers only for a torrent named. The Admin API serves them as
`/bittorrent/tracker/stats`, `/bittorrent/tracker/torrents[/<hash>]` and
`/bittorrent/tracker/passkey?user=`, with [metrics](#metrics).

## Interoperability

fit's roles are checked against two other clients by `test/btinterop`, a module of its own (so
that anacrolix is never fit's dependency), in CI's `bittorrent-interop` job and locally with
`test/btinterop/run.sh` (docker; a container with libtorrent's Python binding and cgo). Its tests
build fit from the checkout and run it as a user does, steering it through the Admin API:
[anacrolix/torrent](https://github.com/anacrolix/torrent) 1.61 in the test's process (uTP through
go-libutp, so cgo) and [libtorrent-rasterbar](https://www.libtorrent.org/) 2.0.11 through
`libtorrent_peer.py`. Every case passed on 2026-11-09 (fit 12f4795e with I205's tests, fib
c82b427).

| Case | anacrolix | libtorrent |
| --- | --- | --- |
| fit downloads from its seed: v1 and hybrid, .torrent file and magnet link, TCP and uTP, MSE off, preferred and RC4 required (8 cases) | passes | passes |
| the same for v2 torrents (`btmh` magnet links, piece layers by BEP 52 hash requests; 3 cases) | (no v2-only torrents) | passes |
| it downloads from fit's seed, the same cases | passes | passes |
| a private torrent found only through fit's tracker: a seeder over HTTP with a token's passkey, a leecher over UDP (BEP 41 URL data) with a user's passkey from the Admin API; an unknown passkey refused | passes (v1) | passes (v1, hybrid, v2; fit's own client as the UDP leecher beside libtorrent over HTTP) |
| `bt_stream` plays a 24 MiB file while it downloads from anacrolix: a range in the middle answered at 8% of the torrent, then the whole file; a magnet link added on the fly (temporary) | passes | |
| a publish entry's web seed (fit's `static` route) alone, no BitTorrent peer: `per_file` and `whole_dir` torrents | `whole_dir` passes | both pass |

The peers each case saw are checked through the Admin API: uTP or TCP as the case says, encrypted
when required, plaintext when off. Quirks of the other clients found on the way, not fit's, so the
tests go around them:

- anacrolix takes the single file of a v2 or hybrid torrent for a directory (its file tree holds the
  file's name), so it looks for the data below the name and refuses a single-file web seed URL not
  ending in `/`: fit's `per_file` published torrents cannot be fetched from their web seed by
  anacrolix. Nor does anacrolix's web seed ask for a torrent's last piece when it is short.
- anacrolix 1.61 panics in its announcer (`panicif.False` in `updateTorrentInput`) while checking a
  hybrid torrent it announces under both hashes; the tracker case uses a v1 torrent with it.

## Performance

`bench/bt.sh` runs `test/btinterop/btbench` on the benchmark host (rules 16-18; never locally): one
1 GiB v1 torrent (1 MiB pieces) in `/dev/shm`, the client measured pinned to 4 CPUs (0-3), four fib
clients on the other side pinned to CPUs 8-15, over loopback TCP, plaintext and with MSE (RC4). The
client measured is fit, anacrolix 1.61, or fib's client alone in a process (fit's client is fib's,
so the difference is fit's). CPU time and peak resident memory are the measured process's, from
`/proc`. Results of 2026-11-09 are in
[bench/results/bt-2026-11-09](../bench/results/bt-2026-11-09/summary.md), medians of 3 alternating
rounds:

| Scenario | Client | MiB/s | CPU % (of 400) | CPU s per GiB | Peak RSS MiB |
| --- | --- | --- | --- | --- | --- |
| download from 4 seeders, plaintext | fit | 705 | 158 | 2.28 | 179 |
| | fib alone | 478 | 106 | 2.28 | 163 |
| | anacrolix | 1039 | 225 | 2.23 | 1050 |
| download from 4 seeders, RC4 | fit | 553 | 199 | 3.67 | 176 |
| | fib alone | 363 | 137 | 3.85 | 152 |
| | anacrolix | 759 | 283 | 3.81 | 1052 |
| seed to 4 leechers, plaintext (MiB/s of all four) | fit | 1688 | 98 | 0.60 | 46 |
| | fib alone | 1670 | 93 | 0.57 | 34 |
| | anacrolix | 1110 | 133 | 1.23 | 1058 |
| seed to 4 leechers, RC4 | fit | 933 | 215 | 2.35 | 56 |
| | fib alone | 937 | 217 | 2.36 | 43 |
| | anacrolix | 744 | 199 | 2.74 | 1060 |

Seeding, fit is fib: 1.5 times anacrolix's rate plaintext at half its CPU per byte, 1.25 times with
RC4, in a twentieth of its memory (anacrolix maps the whole file). Downloading, fit takes the same
CPU per byte as anacrolix but reaches 0.68 (plaintext) and 0.73 (RC4) of its rate, using under two
of its four CPUs: the download is bound by fib's torrent lock, under which every block received
picks the next request by scanning the pieces (`pickPiece`, a fifth of the CPU in a profile of fib
alone), and each 16 KiB block is its own `pwrite`. fib alone is slower still (0.68 of fit's rate),
likely its own engine's defaults against fit's shared engine (not profiled further). Both are fib requests (see
I205's open items in the development plan); the rounds spread under 2%, except anacrolix's
plaintext download (7%).

## Limits and security

- **Ports.** The client's `listen` is open to every peer on the internet unless `access` says
  otherwise; it accepts TCP and uTP, the DHT and UDP trackers' replies on the same UDP port.
  `encryption require` refuses plaintext peers (some ISPs throttle plain BitTorrent), at the cost of
  RC4 on every byte (see [performance](#performance)). `dht: false` and no trackers keep a
  client off the public swarms entirely: it then talks only to the peers it is given.
- **What may add torrents.** The Admin API (behind its token and `admin.access`), `fit bt`, a
  `watch_dir` (whoever may write to it) and `bt_stream` with `magnet` (who has `publish` on
  `bt/<info hash>`). A torrent added downloads from the internet into the server's disk, bounded by
  `max_torrents`, `min_free_bytes` and, for `bt_stream`'s, `max_temporary` and
  `max_temporary_bytes`. A .torrent URL is fetched from wherever it points, loopback and private
  addresses included: the Admin API is the operator's (I198's decision 3).
- **Paths.** No torrent writes outside its directory: fib refuses `..` and absolute components, and
  the storage makes names safe; a category's relative `dir` stays below `download_dir`. Published
  torrents' storage refuses writes, so a published file is never overwritten by a peer.
- **Private trackers.** A passkey rides in the announce URL of every .torrent file that carries it:
  whoever has such a file can announce as its owner. Give each user their own passkey (a user's
  passkey, derived and revoked by rotating the policy's secret, or a token per user) rather than a
  shared token; a `publish` entry's `announce` with a passkey hands that passkey to every
  downloader of the published torrents, so publish to a public tracker (no `auth`, with `allow` or
  `allow_files` restricting what it tracks) or with no tracker (web seed and DHT). The access log
  never holds a passkey.
- **Streams.** `bt_stream` without `auth` serves every torrent the client holds to whoever reaches the
  route; with `auth`, a token's `play` grant names the torrents (`bt/*` for all).
- **Not bounded here.** fib's client queues its events without a bound (fit's handler drains them
  at once); fib's tracker store cannot change its bounds without a new store (swarms start again).
  Both are fib requests in the development plan's open items.
