fit's BitTorrent roles. `bittorrent.client` is a BitTorrent client that downloads and seeds on
fit's own fib engine, beside the gateway: one port, `listen`, for TCP and UDP, which uTP, the DHT
and UDP trackers share; v1, v2 and hybrid torrents from magnet links and .torrent files; MSE
encryption (`prefer` by default), the DHT, peer exchange and uTP on by default, local service
discovery off. The role is off unless the config sets it. Storage is read and written, and pieces
are hashed, on the client's bounded disk pool (`disk_workers`, `disk_queue_bytes`), never on a fib
worker; past `disk_queue_bytes` of received blocks waiting, the peers sending them are not read
from until half is written.

The torrents are kept in files under `data_dir`: `torrents.json`, the list in queue order (each
torrent's source, directory, category, own limits, file priorities, and whether it is paused,
sequential or complete), `torrents/<id>.torrent`, its
.torrent file (a magnet link's once its info has arrived), and `resume/<id>.resume`, its resume
data, each written to a temporary file, synced and renamed. Changes to the list are saved at once,
resume data at most every `save_interval` when pieces were verified, and everything when the role
stops; a restart adds every torrent back with its resume data, so that what is on disk is not
hashed again, unless a file's size or modification time changed. A state file that does not read,
of a later version or larger than its bound keeps the role from starting, with the file named in
the error, rather than being overwritten; a torrent whose own files do not read stays listed, with
its error, until it is removed. Downloads go below `download_dir` (`<data_dir>/downloads` when left
out), each torrent's paths made safe for the system, none leading out of it.

Torrents are added from magnet links and .torrent files through the role's API, from .torrent URLs
(fib's asynchronous HTTP client, at most 5 redirects, 32 MiB, `url_timeout`, 4 at once), and from
`watch_dir`, whose `.torrent` and `.magnet` files, and those of subdirectories named after a
category, are added once they stop changing and moved into `.done` or `.failed`. `categories` give
torrents a directory below `download_dir` (or an absolute one), their own limits (fib's
`TorrentLimits`, a torrent's own taking their place) and a `media_library` scanned again when one of
them completes. `limits.max_active_downloads` and `max_active_seeds` queue the torrents past them,
paused, in the list's order; `limits.min_free_bytes` (1 GiB) keeps a download from starting, or
pauses it, when its disk would be left with less free, and starts it again once the space is there.
`alt_speed` takes the place of the rates on weekly schedules, or while the API turns it on. The
access's list files may be eMule `ipfilter.dat` or PeerGuardian `.p2p` lists, as every module's.

A reload changes `limits`, `access`, `categories`, `alt_speed`, `watch_dir`, `save_interval` and
`console` in place, keeping the peers
connected (but those over a lowered connection limit, and those the access now refuses); a change
of `listen`, `encryption`, `dht`, `pex`, `utp`, `lsd`, `disk_workers` or `disk_queue_bytes` makes
a new client, the torrents saved from the old one and added to the new one without a check. A new
`data_dir` starts from that directory's state, as a restart would.

`publish` entries publish directories: each file of `dir` (mode `per_file`), or the whole directory
(mode `whole_dir`), is built into a hybrid torrent, with `web_seed` (the URL of a static route of `dir`)
as its web seed, `announce` as its trackers and `private` if set, on `max_hashing` goroutines of the
entry's own, and seeded in place, through storage that never writes. The `bt_publish` handler serves
an index of them, their .torrent files and their magnet links. An entry walks `dir` every
`check_interval` (10s): a file added gets a torrent once it has not changed for a `check_interval`,
and a file changed or removed stops its torrent, a changed one getting a new one. Names starting
with a dot and symbolic links are left out, and `files`, `exclude`, `max_files` and
`max_total_bytes` bound what is published. The torrents are kept in `data_dir/publish/<name>/`, so
that a restart takes those whose files kept their size, times and inode without building or checking
them again. See [decision 037](../decisions/037-bittorrent.md) and [BitTorrent](../bittorrent.md).

`bittorrent.tracker` is a BitTorrent tracker on fib's tracker, one a process: HTTP and HTTPS
announces and scrapes come through any route whose handler is `bt_tracker`, on fit's listeners
and TLS, and UDP ones (BEP 15) on the role's own `listen`, which may be left out. The swarms are
kept in memory, bounded by `max_torrents`, `max_peers_per_torrent` and `max_peers`; a peer is
forgotten `peer_ttl` after its last announce. With `auth`, a `media_auth` policy, the tracker is
private: each announce and scrape carries a passkey, as `/<passkey>/announce` or `?passkey=`, one
of the policy's tokens or a user's passkey (derived from the policy's secret and the user's name,
read on the console's `/api/passkey?user=`), whose grant must play `bittorrent/<info hash>`.
With `allow` or `allow_files`, only the info hashes listed are tracked; the files are read again
when they change. A reload changes the allow-list, `auth`, `access` and `console` in place; the
interval, `min_interval`, `max_numwant` and `max_scrape` keep the swarms; the store's bounds and
`peer_ttl` start them empty. See [decision 037](../decisions/037-bittorrent.md) and
[BitTorrent](../bittorrent.md).

```json
{
  "listeners": [{"name": "web", "address": ":8080"}],
  "bittorrent": {
    "client": {
      "listen": ":6881",
      "data_dir": "/var/lib/fit/bittorrent",
      "download_dir": "/srv/downloads",
      "encryption": "prefer",
      "watch_dir": "/srv/torrents/watch",
      "limits": {
        "upload_rate": 1048576,
        "max_conns": 200,
        "seed_ratio": 2,
        "max_torrents": 500,
        "max_active_downloads": 3,
        "min_free_bytes": 10737418240
      },
      "categories": [{"name": "iso", "dir": "/srv/iso", "limits": {"seed_ratio": -1}}],
      "alt_speed": {"upload_rate": 131072, "schedules": [{"days": ["weekdays"], "from": "08:00", "to": "18:00"}]},
      "access": {"deny": ["10.0.0.0/8"], "deny_files": ["/etc/fit/level1.p2p"]},
      "publish": [{
        "name": "releases",
        "dir": "/srv/releases",
        "web_seed": "https://example.com/releases/",
        "files": ["*.iso", "*.tar.gz"]
      }]
    },
    "tracker": {
      "listen": ":6969",
      "interval": "30m",
      "max_torrents": 10000
    }
  },
  "routes": [
    {"match": {"path": ["/releases/*"]}, "handler": {"module": "static", "params": {"root": "/srv/releases", "strip_prefix": "/releases"}}},
    {"match": {"path": ["/torrents/*"]}, "handler": {"module": "bt_publish"}},
    {"match": {"path": ["/announce", "/scrape"]}, "handler": {"module": "bt_tracker"}}
  ]
}
```
