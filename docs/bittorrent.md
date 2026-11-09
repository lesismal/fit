# BitTorrent

[English](bittorrent.md) | [简体中文](bittorrent.zh-CN.md)

fit runs a BitTorrent client beside its gateway: the `bittorrent.client` role downloads and seeds
torrents on fit's own fib engine, on one port for TCP and UDP, and keeps them across restarts
([decision 037](decisions/037-bittorrent.md)). It is fib's client
([fib's BitTorrent docs](https://github.com/lesismal/fib/blob/main/docs/bittorrent.md)): v1, v2 and
hybrid torrents, magnet links, TCP and uTP, MSE encryption, the DHT, peer exchange, local service
discovery, HTTP and UDP trackers and web seeds. This page covers the role: its config, where it
keeps its state, adding torrents (magnet links, .torrent files and URLs, `watch_dir`), categories,
the queue, the disk checks, the alternative rates, each torrent's limits, the blocklists, what a
reload changes, and its bounds. Playing a file while it downloads, the tracker, publishing, the
Admin API and the pages come with later items of the
[development plan](dev-plan.md#bittorrent-downloading-and-seeding-a-tracker-publishing).

Off by default: without `bittorrent.client`, fit opens no BitTorrent port and keeps no state.

```fit
media_library films { roots /srv/downloads/films }

bittorrent {
	client {
		listen :6881
		data_dir /var/lib/fit/bittorrent
		download_dir /srv/downloads
		watch_dir /srv/torrents/watch
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
	}
}
```

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
| `torrents.json` | The list, in queue order: each torrent's ID (its v1 info hash, or its v2 hash truncated), name, the magnet link it came from, its download directory, its category, its own limits, file priorities, whether it is paused or sequential, whether it completed, and when it was added; and the API's choice for the alternative rates |
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
`Get` (with the files), `Pause`, `Resume`, `Remove` with or without the data, `SetFilePriority`,
`SetSequential`, `SetCategory`, `SetTorrentLimits`, `SetQueuePosition`, `AltSpeed` and
`SetAltSpeed`, `Save`, and `Torrent` for fib's torrent. A status says a torrent's category, its own
and effective limits, its queue position, whether it is complete, and why the role holds it
(`queued`, `disk`). The
role's console, when set, serves `GET /api/torrents` and `GET /api/torrents/<id>` read-only for
now, and any console that shows the role's module serves them as
`/api/m/bittorrent.client/torrents` and `/api/m/bittorrent.client/torrents/<id>`.
