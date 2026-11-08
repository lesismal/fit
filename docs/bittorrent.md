# BitTorrent

[English](bittorrent.md) | [简体中文](bittorrent.zh-CN.md)

fit runs a BitTorrent client beside its gateway: the `bittorrent.client` role downloads and seeds
torrents on fit's own fib engine, on one port for TCP and UDP, and keeps them across restarts
([decision 037](decisions/037-bittorrent.md)). It is fib's client
([fib's BitTorrent docs](https://github.com/lesismal/fib/blob/main/docs/bittorrent.md)): v1, v2 and
hybrid torrents, magnet links, TCP and uTP, MSE encryption, the DHT, peer exchange, local service
discovery, HTTP and UDP trackers and web seeds. This page covers the role: its config, where it
keeps its state, what a reload changes, publishing a directory, and its bounds. Adding torrents by URL and `watch_dir`,
categories and queueing, schedules, playing a file while it downloads, the tracker, the Admin API
and the pages come with later items of the
[development plan](dev-plan.md#bittorrent-downloading-and-seeding-a-tracker-publishing).

Off by default: without `bittorrent.client`, fit opens no BitTorrent port and keeps no state.

```fit
bittorrent {
	client {
		listen :6881
		data_dir /var/lib/fit/bittorrent
		download_dir /srv/downloads
		limits {
			upload_rate 1048576
			seed_ratio 2
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
| `torrents.json` | The list: each torrent's ID (its v1 info hash, or its v2 hash truncated), name, the magnet link it came from, its download directory, file priorities, whether it is paused or sequential, and when it was added |
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

## Reloads

| Change | What happens |
| --- | --- |
| `limits` | Applied in place (fib's `Client.SetLimits`): new rates within a quarter second, peers kept, but those over a lowered connection limit |
| `access` | Applied in place: the client's filter asks the new policy from the next peer on, and the connected peers it now refuses are closed (`Client.Refilter`) |
| `save_interval`, `console`, `download_dir` | Applied in place |
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
| `disk_queue_bytes`, received blocks waiting for the disk | 64 MiB |
| A .torrent file | 128 MiB (fib's metainfo bound) |
| A magnet link | 64 KiB |
| `torrents.json`, a resume file | 64 MiB each |

## The role's API

The role has a Go API that the later items build on (the Admin API, `fit bt`, the pages): `Add` a
magnet link or .torrent bytes, `List`, `Get` (with the files), `Pause`, `Resume`, `Remove` with or
without the data, `SetFilePriority`, `SetSequential`, `Save`, and `Torrent` for fib's torrent. The
role's console, when set, serves `GET /api/torrents` and `GET /api/torrents/<id>` read-only for
now, and any console that shows the role's module serves them as
`/api/m/bittorrent.client/torrents` and `/api/m/bittorrent.client/torrents/<id>`.
