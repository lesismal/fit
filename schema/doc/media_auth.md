Named policies of who may publish and play which streams, for the handlers and roles that take
media: [`whip`](#whip), [`whep`](#whep) and [`webrtc_proxy`](#webrtc_proxy) refer to one by name
with their `auth` param. A policy lets in static `tokens`, sent as `Authorization: Bearer`, a
`token` query parameter or the `fit_media_token` cookie; `users` with a password, sent as
`Authorization: Basic` (or, where a protocol has no header, as `user` and `pass` in the query); and
tokens fit signs with its `secret`, the file `secret_file` names, or one key of its keyring `keys`,
which `fit media-token` prints and carry their subject, expiry and streams. Each token and user
lists the stream patterns it may `publish` and `play`, with `*` matching any run of characters, as
`live/*` or `room1-*`. `public` lists the actions anyone may take without credentials, as `["play"]`
for streams anyone may watch. Missing or bad credentials are answered `401` with a `Bearer`
challenge, and credentials that do not allow the action `403`. A handler without `auth` lets
everyone in, as the route's middleware allow.

The checks never hold up a worker: a token is looked up by its SHA-256 or its signature checked, a
plaintext password compared in constant time; a `password_hash` is checked with bcrypt on a small
pool of its own, and remembered once found right. A reload changes a policy in place, for the
requests and sessions that follow. See [WebRTC](../webrtc.md#auth) and
[decision 030](../decisions/030-media-auth-and-pages.md).

`secret`, `secret_file` and `keys` are the ways to give a policy what its tokens are signed with,
one of them: `secret` writes the key in the config; `secret_file` names a file that holds it, read
each time the config is applied, so that rewriting the file and applying the config revokes every
token signed before; and `keys` is a keyring, each key a `key` name with its own `secret` or
`secret_file`, in place of the other two. A token is signed with one key of the ring — the one a
`media_tokens` request names, or the first, the ring's default — and checked against every key, so
the token carries no key id and one signed before a key rotation stays acceptable. Rotating a key,
as the handler [`media_tokens`](#media_tokens) does, replaces only that key's material: the tokens
it signed are refused at once, by every handler that shares the policy — `whip`, `whep`, the media
pages, the streaming role and `media_tokens` — while the tokens of the other keys stay good until
their expiry, so a revocation does not take the whole policy down. Two keys sharing a secret are
refused by the config, so a token never verifies under two keys and rotating one cannot leave a twin
good; a ring holds at most 256 keys. `media_tokens` needs a `secret_file` for every key, and
`fit media-token` signs with the ring's first key or the one its `-key` names.

A revoke writes 32 fresh random bytes to the secret's file, atomically, and then replaces that key
in memory, so the tokens it signed are refused at once; with a single `secret` or `secret_file`
there is one key, and every token of the policy is refused. The file keeps the new secret, so the
revocation outlives a restart, which reads it again, as a config apply after a rotation does. A
`secret_file` must hold at least 16 bytes, its line breaks at the end aside; a missing or unreadable
file, a shorter one, or a key with neither a `secret` nor a `secret_file`, fails the config. A
policy with no `secret`, `secret_file` or `keys` signs with a random key of the process's, which a
restart forgets, as `fit media-token` does. Revoking is per key, or all keys, but never per subject:
the tokens a key signed cannot be listed or counted, and one subject's tokens cannot be revoked
alone.

`url_signing` turns on playback URLs that expire, for the `vod`, `hls` and `httpflv` handlers whose
`auth` names the policy: the site's API, or `media_tokens`' `POST <prefix>/urls` for it, signs a URL
for one viewer (`sub`), one resource (an item's path prefix, `/vod/<library>/<id>/`, or a stream's,
`/hls/live/cam/`), an expiry (`exp`) and, if asked, the viewer's address (`ip`, or its network with
`bind_address: subnet`) and a playback session (`sid`), with the secret or a key of the ring (`kid`).
The handlers check it on every request, at once and without a callback; a playlist fetched with one
hands what it names a grant of its own that lives `play_grace` (30m) past the later of the URL's
expiry and the item's end, in their query or, with `cookie`, as the cookie `fit_media_grant` on the
resource's path. `max_sessions` caps a subject's sessions, `on_limit` refusing a new one or ending
the oldest; `referers` and `origins` list the pages and scripts that may play; rotating a key
refuses the URLs it signed. Refusals are 403, and the access log shows the signature as `REDACTED`.
See [playback URLs that expire](../media-library.md#playback-urls-that-expire).

```json
{
  "listeners": [{"name": "web", "address": ":8080"}],
  "media_auth": [
    {
      "name": "live",
      "keys": [
        {"key": "k1", "secret": "a-secret-of-at-least-16-bytes"},
        {"key": "k2", "secret_file": "/etc/fit/media-secret-k2"}
      ],
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
