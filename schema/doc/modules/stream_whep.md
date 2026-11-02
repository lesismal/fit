Plays the [`streaming`](#streaming) role's streams to WebRTC viewers with WHEP, whatever they were
published over, on the role's `webrtc` port: a POST of a viewer's `application/sdp` offer is
answered `201` with fit's answer, its `Location` the session's URL; a DELETE of that URL ends the
viewer's session. The stream's path is `stream`, or the path after `prefix`, as `live/cam` in
`/whep/live/cam`. H.264, H.265, AV1, Opus and G.711 are sent; AAC, which WebRTC cannot carry, is
left out, so that an RTMP stream with AAC plays as video alone. A stream without a publisher is
`404`, unless `streaming.webrtc.play_wait` lets the offer wait. `auth` names a
[`media_auth`](#media-auth) policy whose credentials must allow `play` on the stream's path. See
[Streaming](../streaming.md#webrtc).

```json route
{
  "match": {"path": ["/whep/*"]},
  "handler": {"module": "stream_whep", "params": {"auth": "live"}}
}
```
