Plays the [`streaming`](#streaming) role's streams as HTTP-FLV, an FLV file that never ends as the
body of a GET, and as WebSocket-FLV, the same bytes in binary messages, to a WebSocket upgrade:
what flv.js, mpegts.js and ffmpeg play with low latency. The stream's path is the request's after
`prefix` and before `.flv`: `/live/cam.flv` plays `live/cam`. A player starts at the stream's
latest key frame; one that falls behind has its packets dropped, video until the next key frame,
and one whose output does not move is closed. A stream without a publisher is `404`, unless
`wait` lets the player wait. `auth` names a [`media_auth`](#media-auth) policy whose credentials
must allow `play` on the stream's path: a bearer token, Basic, the `token` query parameter or the
`fit_media_token` cookie; `401` with a `Bearer` challenge without them, `403` when they do not
allow it. See [Streaming](../streaming.md).

```json route
{
  "match": {"path": ["/live/*"]},
  "handler": {"module": "httpflv", "params": {"auth": "live", "cors": "*"}}
}
```
