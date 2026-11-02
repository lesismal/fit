Takes WebRTC streams published with WHIP (RFC 9725) into fit's media server,
[`webrtc.media`](#webrtc): a POST of an `application/sdp` offer is answered `201` with fit's answer,
its `Location` the session's URL and its ICE servers in `Link` headers; a DELETE of that URL ends
the stream and its viewers. The stream's name is `stream`, or the last segment of the path, as
`cam1` in `/whip/cam1`. A name published already is refused with `409`, the limits with `503`, an
offer with no codec fit forwards with `422`. PATCH (trickle ICE) is answered `405`: fit is an ICE
lite agent whose answer holds all of its candidates. `auth` names a [`media_auth`](#media-auth)
policy whose credentials must allow `publish` on the stream's name: `401` with a `Bearer`
challenge without them, `403` when they do not allow it; a session's URL takes its DELETE without
them, its random ID being the capability. The route's middleware may check too, as a bearer token
checked by `jwt`. See [WebRTC](../webrtc.md#whip-and-whep).

```json route
{
  "match": {"path": ["/whip/*"]},
  "middleware": [{"module": "jwt", "params": {"jwks_url": "https://auth.example.com/.well-known/jwks.json", "audience": ["whip"]}}],
  "handler": {"module": "whip", "params": {"auth": "live"}}
}
```
