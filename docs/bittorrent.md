# BitTorrent

[English](bittorrent.md) | [简体中文](bittorrent.zh-CN.md)

fit runs a BitTorrent client beside its gateway: the `bittorrent.client` role downloads and seeds
torrents on fit's own fib engine, on one port for TCP and UDP, and keeps them across restarts
([decision 037](decisions/037-bittorrent.md)). It is fib's client
([fib's BitTorrent docs](https://github.com/lesismal/fib/blob/main/docs/bittorrent.md)): v1, v2 and
hybrid torrents, magnet links, TCP and uTP, MSE encryption, the DHT, peer exchange, local service
discovery, HTTP and UDP trackers and web seeds. This page covers the role: its config, where it
keeps its state, what a reload changes, and its bounds, and the `bt_stream` handler that plays a
torrent's file while it downloads. Adding torrents by URL and `watch_dir`, categories and queueing,
schedules, the tracker, publishing, the Admin API and the pages come with later items of the
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
