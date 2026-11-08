# 037: BitTorrent: downloading and seeding, a tracker, publishing

[English](037-bittorrent.md) | [简体中文](037-bittorrent.zh-CN.md)

Status: accepted (2026-11-08; the user ruled on the questions under "Questions ruled on"). The plan
is the [BitTorrent track of the development plan](../dev-plan.md#bittorrent-downloading-and-seeding-a-tracker-publishing)
(I196–I208).

## Context

fib has the BitTorrent protocols (`fib/bittorrent/...`): bencoding, metainfo (v1, v2, hybrid and
magnet links), HTTP and UDP trackers in both roles, the peer wire protocol with MSE, uTP, the DHT,
and a client that downloads and seeds, interoperable with libtorrent and anacrolix. fit's `go.mod`
is already on fib `f957983`, which has them. The user wants BitTorrent in fit: downloading and
seeding on a server, playing a file while it downloads, running a tracker, and publishing a
directory as torrents.

Reading fib showed that its client does disk I/O and whole-piece hashing on a fib worker:
`peer.Server.OnData` runs on the worker and calls the client's code that writes blocks, reads
upload data and verifies pieces, which [rule 1](../rules.md) forbids. It also has no fast resume,
no limits that change at run time, no way to refuse peers by address, and always makes an engine of
its own. By rules 15 and 19 these are added to fib first (A75–A79), not worked around in fit.

## Decisions

- **fib first** (I196): A75, storage I/O and hashing on a bounded pool of the client's, reads held
  with `HoldReads` while it is full; A76, fast-resume data; A77, client and per-torrent limits that
  change at run time; A78, `Config.Filter`, refusing peers by address (incoming, outgoing, uTP, the
  DHT); A79, the client on an engine the caller gives, removing only its own bindings when closed.
- **The `bittorrent.client` role**: off by default, one client a process (TCP and UDP on one
  port), on **fit's shared fib engine** (A79). Torrents are added from magnet links, .torrent
  files, .torrent URLs and a `watch_dir`; categories have their own directories and limits and may
  name a `media_library`, rescanned when a download completes; the active downloads and seeds are
  bounded and the rest queue; `alt_speed` changes the rates by schedule; a torrent pauses when the
  disk runs short.
- **Torrent state lives in files under the role's `data_dir`**, not in fit's encrypted store: the
  torrent list, categories, priorities, .torrent files and resume data (A76), written to a temporary
  file and renamed; a restart restores them without hashing again. A reload applies limits,
  categories and schedules in place (A77); only `listen` and the DHT and uTP switches make a new
  client, keeping the torrents and their resume data.
- **Defaults**: DHT, PEX and uTP on, LSD off, encryption `prefer`; fib's peer ID prefix
  `-FB0001-` (not yet registered with the community).
- **The `bt_stream` handler** reads a torrent's file through HTTP ranges, downloads the ranges asked
  for first, and answers as the data arrives; the `Reader`'s blocking reads run on a bounded pool;
  `media_auth` authorizes. **Adding a torrent by magnet link on the fly is allowed**, such torrents
  bounded in number and removed once their last stream has ended and they expire.
- **The `bittorrent.tracker` role**: HTTP announce and scrape through the route handler
  `bt_tracker`, UDP on the role's own `listen`; public or private (passkeys from `media_auth`'s
  users and tokens), an allow-list of info hashes (inline or in files, read again when they
  change), statistics for the Admin API and the pages. The store is fib's bounded `MemoryStore`.
- **Publishing** builds hybrid torrents of a directory with fib's `metainfo.Builder` and seeds them;
  routes give the `.torrent` files and magnet links, web seeds point at fit's own static files;
  they follow the disk, hashing on a bounded pool.
- **Blocklist formats every module reads**: `access`'s `deny_files` and `allow_files` also read
  eMule's `.p2p` (`name:first-last`) and `.dat` (`first - last , level , name`), told apart by
  content; the BitTorrent roles use the same access through A78.
- **No search plugins or indexers.**
- **Second phase**: the cluster's view of its members' torrents and spreading large files to them
  over BitTorrent (I206), a tracker store the cluster shares (I207), RSS auto-download (I208).

## Questions ruled on (2026-11-08)

| Question | The user's ruling |
| --- | --- |
| The first phase's scope | the cluster in the second phase |
| The engine | fit's shared fib engine (hence A79) |
| Defaults | DHT, PEX, uTP on, LSD off, encryption prefer, prefix `-FB0001-` |
| `bt_stream` adding by magnet link on the fly | allowed |
| RSS auto-download, search plugins | RSS planned for the second phase; no search |
| A tracker store the cluster shares | second phase |
| `.p2p` / `.dat` blocklists | in `access`, for every module |
| Torrent state | files under `data_dir` |

## Consequences

- fib changes first, and fit moves to the commit by its hash (rule 19).
- BitTorrent's TCP and UDP port joins fit's shared engine, under `limits` and `access`;
  `bt_stream` lets an HTTP route start downloads from the internet, so adding on the fly is behind
  `media_auth` by default and bounded in number and disk.
- Interop tests follow fib's (libtorrent and anacrolix in containers); throughput and CPU
  benchmarks run on the benchmark host only.
