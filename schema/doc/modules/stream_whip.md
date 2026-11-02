Takes WebRTC streams published with WHIP (RFC 9725) into the [`streaming`](#streaming) role's hub,
which plays them over every protocol, on the role's `webrtc` port: a POST of an
`application/sdp` offer is answered `201` with fit's answer, its `Location` the session's URL,
the POST's path followed by the session's ID; a DELETE of that URL ends the stream. The stream's
path is `stream`, or the path after `prefix`, as `live/cam` in `/whip/live/cam`. A path published
already is `409`, the limits `503`. H.264, H.265, AV1, Opus and G.711 are taken as they come, and
played to the other protocols in what each carries. `auth` names a [`media_auth`](#media-auth)
policy whose credentials must allow `publish` on the stream's path; the session's URL needs none,
its random ID being the capability. Without `streaming.webrtc` the route answers `503`. See
[Streaming](../streaming.md#webrtc).

```json route
{
  "match": {"path": ["/whip/*"]},
  "handler": {"module": "stream_whip", "params": {"auth": "live"}}
}
```
