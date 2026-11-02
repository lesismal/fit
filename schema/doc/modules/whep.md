Plays the streams of fit's media server, [`webrtc.media`](#webrtc), with WHEP
(draft-ietf-wish-whep): a POST of a viewer's `application/sdp` offer is answered `201` with fit's
answer, which sends the stream's audio and video, its `Location` the session's URL; a DELETE of
that URL ends the viewer's session. The stream's name is `stream`, or the last segment of the
path, as `cam1` in `/whep/cam1`. A stream not published is answered `404`, one with `max_viewers`
already `503`. Viewers' key frame requests reach the publisher at most once a
`key_frame_interval`. `auth` names a [`media_auth`](#media-auth) policy whose credentials must
allow `play` on the stream's name, or that makes `play` public, as `whip`'s does for `publish`. See
[WebRTC](../webrtc.md#whip-and-whep).

```json route
{
  "match": {"path": ["/whep/*"]},
  "handler": {"module": "whep", "params": {"auth": "live"}}
}
```
