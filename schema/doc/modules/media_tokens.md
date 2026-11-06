Issues time-limited [`media_auth`](#media-auth) tokens in batches, over a route a business backend
calls, and revokes the tokens of one key of the policy's keyring, or of every key, by rotating their
secrets: `POST <prefix>` takes the tokens a request wants and answers them, and
`POST <prefix>/revoke` rotates the secret files of the keys it names. The route's middleware
authenticates the caller — a `jwt`, `basic_auth`, `forward_auth`, or `client_cert` without
`optional`, of the business backend's own; a route without one of these is refused when the config
is applied, since anyone reaching it could sign tokens and rotate secrets, and a `rate_limit` beside
it bounds the calls but authenticates no one — and the module checks no caller credentials itself: it is not the Admin API, so a
backend never holds the Admin API's all-powerful token. `auth` names the policy the tokens belong
to: it must set a single `secret_file`, or a `keys` keyring whose every key sets a `secret_file`, so
that a revoke can rotate a file and outlive a restart; a policy with a key that has no
`secret_file` fails the config.

`prefix` is the path the route serves under, `/media-tokens` when left out: the route must match it
and what is below it, as `/media-tokens` and `/media-tokens/*`, and a request outside it is answered
404. `prefixes` bounds the stream names a token may be signed for: every `publish` and `play`
pattern of a request must start with one of them, as `mtg-` for `mtg-42-*`, so that a token cannot
be signed for a stream outside them; any valid pattern when left out. `max_ttl` (24h) is the longest
a request may ask a token to be good for, `max_tokens` (100, 1 to 1000) the most tokens one request
may ask for, `max_body_bytes` (64 KiB, 256 bytes to 1 MiB) the largest body the route takes, and
`revoke_timeout` (10s) how long a revoke may wait for its secret files' writes before it is
answered 504.

`POST <prefix>` takes a batch, `{"key": "<name>", "tokens": [{"subject", "publish", "play",
"ttl"}]}`, and answers `{"tokens": [{"token", "subject", "expires", "key"}]}` in the request's order.
`key` is optional: it signs the whole batch with that key of the policy's keyring, the ring's first
when left out, and a name the policy has no key for is 400; a policy with one secret takes no `key`,
and a request that gives one is 400. Each answer token names the key that signed it, left out for a
policy with one secret. `subject` is required, 1 to 128 bytes with no control character; each token
needs at least one `publish` or `play` pattern, in the config's syntax with `*` matching any run of
characters; and `ttl` is a duration such as `"2h"`, the smaller of the policy's `token_ttl` and
`max_ttl` when left out. A batch with one bad token issues none: a body over `max_body_bytes` is
413, more than `max_tokens` or a bad JSON, `subject`, pattern, `ttl` or `key` 400, a path outside
the prefix 404, and other methods 405. Issuing is pure computation on the policy's state, so many
requests run at once and none waits on a disk.

`POST <prefix>/revoke` writes 32 random bytes to the secret file of each key it rotates,
atomically, and then replaces that key, so that the tokens it signed are refused at once, by every
handler sharing the policy, while the tokens of the keys left alone stay good until their expiry.
With a `keys` keyring the body is `{"key": "<name>"}` to rotate one key, or `{"all": true}` to
rotate every key; a body naming neither, or a key the policy lacks, is 400. A policy with a single
secret keeps the empty body, or `{}`, and `{"all": true}` rotates it too; a `key` there is 400. It
answers `{"revoked": true, "since": <unix seconds>, "keys": ["k2"]}`, the keys rotated, left out
for a single key, 500 when a file cannot be written, which stops the revoke there, so on `all` the
keys already written stay rotated, 503 with `Retry-After: 1` when a revoke waits already, and 504
when the writes take longer than `revoke_timeout`. The writes run on a bounded pool of its own, one
at a time, and the answer is written when it completes, so no worker waits on the disk. See
[WebRTC](../webrtc.md#issuing-tokens-from-a-route).

`POST <prefix>/urls` signs playback URLs that expire, for a policy with `url_signing`, and is 404
for one without: the body is `{"subject", "address", "session", "ttl", "key", "paths"}`, one viewer
and the paths to sign, as `/vod/films/films/bbb/index.m3u8` or `/live/cam.flv`, at most `max_tokens`
of them, each starting with one of `url_prefixes` when set. `address` binds the URLs to the viewer's
address, as `bind_address` says; `session` is the playback session `max_sessions` counts, a new
random one when left out; `ttl` is at most `max_ttl`, url_signing's `ttl` (10m) when left out; `key`
as for tokens. It answers `{"subject", "session", "expires", "key", "urls"}`, the URLs whole,
`url_base` before each path. See
[playback URLs that expire](../media-library.md#playback-urls-that-expire).

```json route
{
  "match": {"path": ["/media-tokens", "/media-tokens/*"]},
  "middleware": [{"module": "jwt", "params": {
    "jwks_url": "https://auth.example.com/.well-known/jwks.json",
    "issuer": "https://auth.example.com/",
    "audience": ["media-tokens"],
    "claims_headers": {"X-User": "sub"}
  }}],
  "handler": {"module": "media_tokens", "params": {
    "prefix": "/media-tokens",
    "auth": "live",
    "prefixes": ["mtg-"],
    "max_ttl": "12h",
    "max_tokens": 200
  }}
}
```
