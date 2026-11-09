Serves a file of a torrent of the [`bittorrent.client`](#bittorrent) role over HTTP while it
downloads, so that a browser or a player plays it as it streams in: `/bt/<info hash>/<file path>`
is a file by its path as the torrent lists it, its name first, `/bt/<info hash>/<n>` its file of
index `n`, and `/bt/<info hash>/` its largest file, after `prefix` (`/bt/` when left out). The
info hash is the role's torrent ID. `Range` asks for a single range (`206`, `416` past the end),
`HEAD` is answered, `Content-Type` comes from the file's extension, and the `ETag` from the
torrent and the file, which never change. The pieces of the range asked for, and `readahead_bytes`
past it, are fetched before all others; a read waits at most `read_timeout` for its piece (`504`
before the response begins). At most `max_streams` responses stream at once (`503` past it), each
read on a goroutine of its own and written only as fast as its client takes it. `auth` names a
[`media_auth`](#media-auth) policy every request must satisfy with `play` on `bt/<info hash>`.
With `magnet`, a request whose `magnet` query parameter is a magnet link of the torrent its path
names adds the torrent when the role does not have it, if the request also has `publish` on
`bt/<info hash>`: such torrents are temporary, download only the files streamed, are bounded by
`max_temporary` and `max_temporary_bytes`, and are removed with their data `temporary_ttl` after
their last stream ended. See [BitTorrent](../bittorrent.md#playing-a-file-while-it-downloads).

```json route
{
  "match": {"path": ["/bt/*"]},
  "handler": {"module": "bt_stream", "params": {"auth": "site", "magnet": true, "max_streams": 16, "temporary_ttl": "15m"}}
}
```
