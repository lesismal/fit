Serves fit's own media pages under `prefix` (`/media`), embedded in the binary, with no script or
style from elsewhere: an index, a room call (`call`) with a chat of text messages and small files,
a publish page (`publish`) and a play page (`play`), over the WHIP and WHEP endpoints `whip` and
`whep` name, fit's [`whip`](#whip) and [`whep`](#whep) or the streaming role's, on this server or
another. In a call each member publishes `<room>-<name>` and plays the others; the switches of the
microphone, the camera and the screen replace the tracks the PeerConnection sends and never
renegotiate it, a page without the device sends a silent track or a frame with its name, and each
member announces its switches through the room API (`api/rooms/<room>/join`, `members/<name>`,
`leave`, and `events`, Server-Sent Events), so that the others show "microphone off" or the
member's avatar rather than wait on media that does not come, and play a member's stream only once
it is announced as published. The publish and play pages show a stream's URLs: WHIP, WHEP, `hls`,
and `links` for tools outside the browser. `auth` names the [`media_auth`](#media-auth) policy the
room API checks: joining and following a room needs `play` on `<room>-*`, announcing that one
publishes `publish` on `<room>-<name>`. With `login`, the pages log the policy's users in: a token
the policy signs comes back as the `HttpOnly`, `SameSite=Strict` cookie `fit_media_token`, which the
endpoints take; the pages also take a token pasted in, as `fit media-token` prints. With `pages`
false, none of the pages is served: every path under the prefix that is not the API, the redirect
from the prefix included, answers 404, for a deployment whose page is its own, and the login and
the room API stay for that page. The chat, on unless `chat` is false, carries the members' text
messages and small files through the room API's `chat` and `files` routes and starts each `events`
watcher with the room's `history` event; each file is at most `max_file_bytes` (5 MiB when left
out), a room keeps 64 messages, and the handler's rooms 64 MiB of file bytes together, a watcher
that falls behind being ended. A member joins with the microphone and the camera on only where
`mic_default` and `cam_default` say so, both off when left out, and the URL's `mic=0|1` and
`cam=0|1` override them. The room API is bounded by `max_rooms`, `max_members`, twice as many
watchers a room, `max_message_bytes`, `member_ttl` and `stall_timeout`. The pages themselves can
sit behind the route's middleware as well, as `basic_auth` or `forward_auth`. The route must match
the prefix and what is below it. See [WebRTC](../webrtc.md#the-media-pages).

```json route
{
  "match": {"path": ["/media", "/media/*"]},
  "handler": {"module": "media_pages", "params": {
    "auth": "live", "login": true, "title": "Our media",
    "pages": true, "mic_default": false, "cam_default": false,
    "chat": true, "max_file_bytes": 5242880,
    "hls": "/hls/{stream}/index.m3u8",
    "links": [{"name": "RTMP", "url": "rtmp://{host}/live/{stream}?token={token}"}]
  }}
}
```
