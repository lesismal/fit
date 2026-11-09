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

`rss` downloads from RSS 2.0 (and 1.0) and Atom feeds: each of `feeds` is polled every `interval`
(15m, at least 1m) on a goroutine of the role's, at most 4 at once, with fib's asynchronous HTTP
client, sending back the `ETag` and `Last-Modified` it answered (304 costs nothing more), at most
`max_feed_bytes` (4 MiB), within `timeout` (30s), at most 5 redirects; `headers` and `headers_env`
(values read from environment variables) go with its requests, and its URL comes from the config
alone. At most `max_items` items of a feed are read; each new one goes to the first of `rules` whose
`feeds`, `patterns` and `exclude` (case-insensitive globs over the whole title, or `re:` and
a regular expression) and `min_size` and `max_size` (from `torrent:contentLength` or the enclosure's
length; unknown passes) it meets, which adds its torrent from its magnet link, else its .torrent
enclosure or link (as a .torrent URL is added), in the rule's `category`, `paused` if it says, at
most `max_per_poll` (5) a poll; with `episodes`, an episode (S01E02, 1x02 or a date) of a show the
rule added already is left out. Every item is remembered as seen in `data_dir/rss.json`, at most
`history_size` (1000) a feed, across restarts, with the episodes each rule added and the Admin
API's toggles; items left for later (by `max_per_poll`, or an add that may pass, as `max_torrents`)
are read again at the next poll, and an item whose .torrent file cannot be downloaded is tried at 3
polls. A reload applies `rss` in place.

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

The Admin API serves both roles under `/bittorrent/`, and so does the console under `/api/m/admin/`:
`GET /bittorrent/torrents` lists the torrents (filtered by `state`, `category` and `q`, paged), `POST`
adds one from a magnet link, a URL or a .torrent file (JSON, `application/x-bittorrent` or a form,
at most 32 MiB), `/bittorrent/torrents/{id}` gives one with its files, pauses, resumes or rechecks it
(`POST {"action"}`), changes its priorities, category, limits and queue position (`PATCH`) and
removes it (`DELETE`, `?delete_data=1`); `.../files`, `.../peers` and `.../trackers`;
`/bittorrent/alt_speed` reads and sets the alternative rates; `/bittorrent/publish` the publish
entries, and `.../{name}/torrents/{id}` a published torrent's .torrent file; `/bittorrent/rss` the
RSS feeds (their last poll and result) and rules, `.../items` the items seen, `POST
.../feeds/{name}/poll` polls a feed now, `PUT .../feeds/{name}` and `.../rules/{name}` turn one on
or off; `/bittorrent/tracker/stats`, `.../torrents` and `.../passkey` the tracker's.
`GET /bittorrent/events` streams the client's events as server-sent events (torrents added, removed,
completed, failing or changing state, peers banned, seed limits, the alternative rates, torrents
an RSS rule added), with
`Last-Event-ID` to catch up, 32 streams at most, a slow one disconnected after 256 events waiting;
on the Admin API's address, and on a route of the console's own, `/api/m/admin/bittorrent/events`,
which the workbench's BitTorrent pages read to show torrents as they change. `fit bt ls`, `add`, `info`, `pause`, `resume`, `rm`, `limit`,
`alt` and `rss` call these through the Admin API the bootstrap config names, as `fit sync` does. The
Admin API's `/metrics` carries the client's as `fit_bittorrent_*` (bytes, rates, peers and hash
failures by category, torrents by category and state, bans, the alternative rates, RSS polls and
items added by feed) and the
tracker's as `fit_bittorrent_tracker_*` (torrents, peers, announces, scrapes, refusals by reason).

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
interval, `min_interval`, `max_numwant` and `max_scrape` keep the swarms, and so do the store's
bounds and `peer_ttl`, changed in place. With a `store` block, the trackers of several instances
share their swarms through Redis and answer as one: each answers from its own memory at once, and a
goroutine of the store's own writes its announces to Redis and reads samples of the shared swarms
every `refresh`, so an instance learns of another's peers within a refresh; while Redis cannot be
reached it answers from memory. See [decision 037](../decisions/037-bittorrent.md) and
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
      "rss": {
        "feeds": [{"name": "shows", "url": "https://example.com/rss", "interval": "15m"}],
        "rules": [{"name": "show", "patterns": ["*Show Name*1080p*"], "exclude": ["*hevc*"], "category": "iso", "episodes": true}]
      },
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
