Plays the [`streaming`](#streaming) role's streams over HLS (RFC 8216): `/live/cam/index.m3u8`
is the playlist of `live/cam`, after `prefix`, in fragmented MP4 segments, or MPEG-TS ones with
`format` `ts`; the other format is played with `?format=ts` or `?format=fmp4`. A stream is cut
into segments of `target_duration` from the first playlist request on, held in memory up to
`max_bytes`, and stops after `idle_timeout` without requests; `part_duration` turns on
Low-Latency HLS. A stream without a publisher is `404`, unless `play_wait` lets the playlist
request wait. `auth` names a [`media_auth`](#media-auth) policy every request, playlist and
segment, must satisfy with `play` on the stream's path; a playlist fetched with a `token` query
parameter names its playlists and segments with the token too, so that Safari and ffmpeg, which
send no header, play on. A reload that keeps the params keeps the segments. See
[Streaming](../streaming.md).

```json route
{
  "match": {"path": ["/hls/*"]},
  "handler": {"module": "hls", "params": {"prefix": "/hls/", "auth": "live", "target_duration": "2s"}}
}
```
