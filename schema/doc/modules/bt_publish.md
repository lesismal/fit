Serves what the `bittorrent.client` role publishes, its `publish` entries: at a path ending in `/`
or in `index.json`, an index in JSON of the entries (`publish` names them; every entry when left
out) and their torrents, each with its path under the entry's `dir`, its info hashes (v1 and v2:
the torrents are hybrid), its magnet link, its state in the client (`seeding`, `checking`, or
`stopped` while no client runs it), and `torrent_url` and `magnet_url`, relative to the index; at
`<id>.torrent`, a torrent's .torrent file (`application/x-bittorrent`, as an attachment named after
the torrent); at `<id>.magnet`, its magnet link as plain text. The index also says what each
entry left out and why (`problems`): files past its bounds, a build that failed, a duplicate.

It answers `503` while no `bittorrent.client` runs, `404` for a torrent not published, or not of the
entries it names, and `405` for a method other than `GET` and `HEAD`. It reads a snapshot of the
role's entries, swapped as they change, and takes no lock of the role's: nothing waits on a fib
worker. A torrent's ID is its v1 info hash in hex; a file changed on disk gets a new torrent, of a
new ID, and the old ID is answered `404` from then on.

The route's middleware applies as to any handler: a `basic_auth` or `jwt` before it keeps a private
entry's .torrent files, which carry its trackers' URLs, to those who may have them.

```json route
{
  "match": {"path": ["/torrents/*"]},
  "handler": {"module": "bt_publish", "params": {"publish": ["releases"]}}
}
```
