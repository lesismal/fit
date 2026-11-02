Named policies of who may publish and play which streams, for the handlers and roles that take
media: [`whip`](#whip), [`whep`](#whep) and [`webrtc_proxy`](#webrtc_proxy) refer to one by name
with their `auth` param. A policy lets in static `tokens`, sent as `Authorization: Bearer`, a
`token` query parameter or the `fit_media_token` cookie; `users` with a password, sent as
`Authorization: Basic` (or, where a protocol has no header, as `user` and `pass` in the query); and
tokens fit signs with its `secret`, which `fit media-token` prints and carry their subject, expiry
and streams. Each token and user lists the stream patterns it may `publish` and `play`, with `*`
matching any run of characters, as `live/*` or `room1-*`. `public` lists the actions anyone may take
without credentials, as `["play"]` for streams anyone may watch. Missing or bad credentials are
answered `401` with a `Bearer` challenge, and credentials that do not allow the action `403`. A
handler without `auth` lets everyone in, as the route's middleware allow.

The checks never hold up a worker: a token is looked up by its SHA-256 or its signature checked, a
plaintext password compared in constant time; a `password_hash` is checked with bcrypt on a small
pool of its own, and remembered once found right. A reload changes a policy in place, for the
requests and sessions that follow. See [WebRTC](../webrtc.md#auth) and
[decision 030](../decisions/030-media-auth-and-pages.md).

```json
{
  "listeners": [{"name": "web", "address": ":8080"}],
  "media_auth": [
    {
      "name": "live",
      "secret": "a-secret-of-at-least-16-bytes",
      "public": ["play"],
      "tokens": [{"name": "studio", "token_sha256": "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", "publish": ["live/*"]}],
      "users": [{"name": "alice", "password": "alice-password", "publish": ["alice-*"], "play": ["*"]}],
      "token_ttl": "12h"
    }
  ],
  "routes": [
    {"match": {"path": ["/whip/*"]}, "handler": {"module": "whip", "params": {"auth": "live"}}},
    {"match": {"path": ["/whep/*"]}, "handler": {"module": "whep", "params": {"auth": "live"}}}
  ],
  "webrtc": {"media": {"listen": ":8189"}}
}
```
