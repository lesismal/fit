Proxies WHIP (RFC 9725) and WHEP (draft-ietf-wish-whep) to the media servers of `upstream`, fit's
own [`whip`](#whip) and [`whep`](#whep) or any other, with their peers' media through one UDP port,
[`webrtc.proxy`](#webrtc). A POST of an offer goes, without its candidates, to the media server the
upstream picks; its `201` answer comes back with fit's port as its only candidate, its `Location`
a URL of fit's, and fit relays each peer to that media server by the ICE ufrag of its first STUN
request. DELETE, PATCH and the rest of a session's requests go to the media server that answered
it; a PATCH that only trickles candidates is answered `204` by fit. A request or answer past
`max_sdp_bytes` is refused, and a media server that does not answer within `timeout` is answered
`504`. `media_addrs` names the address fit relays to, for a media server whose answer's
candidates fit cannot reach. See [WebRTC](../webrtc.md#proxying-media-servers).

```json route
{
  "match": {"path": ["/whip/*", "/whep/*"]},
  "handler": {"module": "webrtc_proxy", "params": {"upstream": "media-servers", "media_addrs": {"10.0.0.2:8080": "10.0.0.2"}}}
}
```
