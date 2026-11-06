Serves the items of a [`media_library`](#media-library) as video on demand over HLS (RFC 8216),
cut from the files as they are stored, without transcoding: `/vod/films/films/bbb/index.m3u8`
is the multivariant playlist of the item `films/bbb` of the library `films`, after `prefix`
(`/vod/<library>/` when left out). Its renditions are variants, its audio files an audio group
and its subtitles, as WebVTT, a subtitle group; each media playlist is of fragmented MP4
segments, or MPEG-TS ones with `container` `ts`, and `index.fmp4.m3u8` and `index.ts.m3u8` name
either. Segments are cut at the keyframe nearest `segment_duration`, from the files' sample
tables, on the library's file pool, and kept, up to `max_cache_bytes`; the files themselves are
served too, as `v0.mp4`, with `Range`. A file changed or removed since the library's last scan
is answered `404`. `auth` names a [`media_auth`](#media-auth) policy every request must satisfy
with `play` on `vod/<library>/<id>`; a playlist fetched with a `token` query parameter names
what it lists with the token too. `cache_control` lets a CDN keep what is served, whose `ETag`
and `Last-Modified` come from the file. See [Media library](../media-library.md#video-on-demand).

```json route
{
  "match": {"path": ["/vod/films/*"]},
  "handler": {"module": "vod", "params": {"library": "films", "segment_duration": "4s", "cache_control": "public, max-age=86400"}}
}
```
