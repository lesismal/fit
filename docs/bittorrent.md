# BitTorrent

[English](bittorrent.md) | [简体中文](bittorrent.zh-CN.md)

fit runs a BitTorrent client beside its gateway: the `bittorrent.client` role downloads and seeds
torrents on fit's own fib engine, on one port for TCP and UDP, and keeps them across restarts
([decision 037](decisions/037-bittorrent.md)). It is fib's client
([fib's BitTorrent docs](https://github.com/lesismal/fib/blob/main/docs/bittorrent.md)): v1, v2 and
hybrid torrents, magnet links, TCP and uTP, MSE encryption, the DHT, peer exchange, local service
discovery, HTTP and UDP trackers and web seeds. This page covers the role: its config, where it
keeps its state, what a reload changes, publishing a directory, and its bounds, and the `bt_stream`
handler that plays a torrent's file while it downloads. Adding torrents by URL and `watch_dir`,
categories and queueing, schedules, the tracker, the Admin API and the pages come with later items
of the
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
| `torrents.json` | The list: each torrent's ID (its v1 info hash, or its v2 hash truncated), name, the magnet link it came from, its download directory, file priorities, whether it is paused or sequential, whether `bt_stream` added it on the fly (`temporary`), and when it was added |
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

The API has hooks for `bt_stream`'s temporary torrents too: `AddTemporary` (bounded by the
caller's `max_temporary`), `SetTemporary`, `RemoveTemporary` (only while still temporary, with its
data), `TemporaryBytes` and `SetFilePriorities` (every file's priority, saved once).

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
tracks: fib's `MemoryStore` answers only for a torrent named. The Admin API's
`/bittorrent/tracker/stats` comes with I203.
