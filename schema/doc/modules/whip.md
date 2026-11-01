Takes WebRTC streams published with WHIP (RFC 9725) into fit's media server,
[`webrtc.media`](#webrtc): a POST of an `application/sdp` offer is answered `201` with fit's answer,
its `Location` the session's URL and its ICE servers in `Link` headers; a DELETE of that URL ends
the stream and its viewers. The stream's name is `stream`, or the last segment of the path, as
`cam1` in `/whip/cam1`. A name published already is refused with `409`, the limits with `503`, an
offer with no codec fit forwards with `422`. PATCH (trickle ICE) is answered `405`: fit is an ICE
lite agent whose answer holds all of its candidates. Auth is the route's middleware, as a bearer
token checked by `jwt`. See [WebRTC](../webrtc.md#whip-and-whep).

```json route
{
  "match": {"path": ["/whip/*"]},
  "middleware": [{"module": "jwt", "params": {"jwks_url": "https://auth.example.com/.well-known/jwks.json", "audience": ["whip"]}}],
  "handler": {"module": "whip"}
}
```
