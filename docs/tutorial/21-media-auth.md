# Media auth

[English](21-media-auth.md) | [简体中文](21-media-auth.zh-CN.md)

## What it does

`media_auth` is a set of named policies in the config that say who may publish and play which streams. The
handlers and roles that take media refer to one by name with their `auth`: `whip`, `whep`, `webrtc_proxy`,
`stream_whip`, `stream_whep`, `hls`, `httpflv`, `vod`, `media_pages`, `media_tokens`, `bt_stream`, the `rtmp`,
`rtsp` and `srt` listeners of [Streaming](20-streaming.md), and the [BitTorrent](27-bittorrent.md) tracker,
which takes the policy's tokens and its users' passkeys. A policy is checked by the stream's path: publishing
needs `publish`, playing needs `play`. A handler without `auth` lets everyone in, as the route's own
middleware allow.

A policy has three kinds of credentials: static `tokens` (written as plaintext `token`, or as its
`token_sha256`, so that the config does not hold the token itself); `users`, each with a plaintext `password`
or a `password_hash` (a bcrypt hash, as `fit hash-password` makes it); and one of `secret`, `secret_file` or
the `keys` ring, which signs the time-limited media tokens that `fit media-token` prints or the
`media_tokens` handler issues, carrying their subject, expiry and streams. Every token and user lists the
stream patterns it may `publish` and `play`: `*` matches any run of characters, so `live/*`, `room1-*` and
`*` all work, and an empty list grants nothing. `public` lists the actions anyone may take without
credentials, as `play`.

`url_signing` turns on the expiring playback URLs for the audience: URLs signed for one viewer, one resource
and a span of time, checked by the `vod`, `hls` and `httpflv` handlers on every request. See
[playback URLs that expire](../media-library.md#playback-urls-that-expire).

Without credentials the answer is `401` with a `Bearer` challenge (`WWW-Authenticate: Bearer realm="fit"`); a
token that is unknown, expired or tampered with, or a wrong password, is the same `401` with
`error="invalid_token"`; credentials that do not allow that action on that stream are `403`; a URL
`url_signing` refuses is `403` with the reason. The checks never hold up a worker: a token is looked up by its
SHA-256 or its signature checked, a plaintext password compared in constant time, a `password_hash` checked on
mediaauth's own small pool and remembered once found right.

## Pages

### Media auth (`#/gw.mediaauth/media_auth`)

The Media auth module in the menu has one page, which edits the `media_auth` member of the config: the
policies with their `tokens`, `users`, `keys`, `public`, `token_ttl` and `url_signing`. The page has the UI,
`.conf`, `.yaml` and `.json` tabs, and edits the part of the shared draft that belongs to it; **Save and
apply** in the top bar saves the whole draft, **Check** takes the changes through the whole apply path without
applying them, and **Discard** drops the draft. A problem of a save links back to this page, whichever page
the save came from — for instance a route whose `auth` names a policy the config does not hold — except one
in a policy's `url_signing`, which links to the Media library's **URL signing** page.

What can be done on the page:

- Make a policy: write its `name` (the name `whip`, `whep`, `webrtc_proxy` and the media roles refer to it
  by), its signing key (`secret`, `secret_file` or `keys`), and its `public` actions.
- Write `tokens`: a static token or its SHA-256, with the streams it may publish and play. Logs show only
  `token:<name>`, never the token itself.
- Write `users`: a plaintext password or a bcrypt hash, with their streams. Secrets are typed by a person:
  the [assistant](14-assistant.md) sees them as `[redacted]`, a `[redacted]` value it copies back is put
  back as the draft holds it, and any other change it makes to a secret field is refused.
- Write the `keys` ring: each key a name with its own `secret` or `secret_file`, for issuing and revoking
  per shard — rotating one key refuses only the tokens that key signed, while the other shards keep running.
  A `secret_file` must exist and hold at least 16 bytes, or the config fails; a rotation (by `media_tokens`,
  or **Rotate** on the Media library's **Playback URLs** page) writes the new secret to a file beside it and
  renames it into place, so the process must be able to write to its directory.

`url_signing` is also edited on the **URL signing** page of the Media library module, and its test URLs are
issued and played, its keys rotated and its playback sessions ended on the **Playback URLs** page
([Media library](22-media-library.md)); the two config pages edit the same draft. A secret written in the
config (`secret`, or a key's `secret`) cannot be rotated from a page: edit it and apply the config. A reload changes a policy in place, for the requests and sessions that follow: the sessions already
established are kept.

## Key parameters

Only the key parameters are listed here; the full field tables are in
[the config reference: Media auth](../reference/config.md#media-auth).

### media_auth

| Field | Default | Description |
| --- | --- | --- |
| `name` | — | Required. The policy's name: letters, digits, `_`, `.` and `-`; the handlers and roles refer to it by this |
| `tokens` | none | Static bearer tokens, each with the streams it may publish and play |
| `users` | none | Users with a password, each with the streams they may publish and play |
| `secret` | none | The key, at least 16 bytes, that fit's tokens are signed with (HMAC-SHA256): `fit media-token`'s and the media pages' logins; left out, a random key of the process signs the logins, which a restart forgets |
| `secret_file` | none | A file holding the key, read each time the config is applied: rewriting it and applying the config refuses every token signed before |
| `keys` | none | The keyring: each key a name with its own `secret` or `secret_file`; a token carries no key id and is checked against every key |
| `public` | none | The actions anyone may take without credentials (`publish`, `play`); without it every request needs credentials |
| `token_ttl` | 12h | How long a token fit issues is good for, unless `fit media-token`'s `-ttl` says otherwise |
| `url_signing` | off | The expiring playback URLs; needs `secret`, `secret_file` or `keys` |

How credentials arrive depends on the protocol: over HTTP they are `Authorization: Bearer`,
`Authorization: Basic`, the `token` query parameter or the `fit_media_token` cookie; RTMP and SRT have no
header, so they go in the query or in the stream ID; RTSP uses Digest (users with a plaintext password) or
Basic. A token and a signature in a query are written as `REDACTED` in the access log.

### tokens

| Field | Default | Description |
| --- | --- | --- |
| `name` | `token` | What the token is called in logs: `token:<name>` |
| `token` | — | The token itself, at least 16 characters; or `token_sha256`, one of the two |
| `token_sha256` | — | The token's SHA-256 in hex, as `printf %s "$TOKEN" \| sha256sum` prints it |
| `publish` | none | The patterns of the streams the token may publish |
| `play` | none | The patterns of the streams the token may play |

### users

| Field | Default | Description |
| --- | --- | --- |
| `name` | — | Required. The user's name; it may not hold a colon or a control character |
| `password` | — | The plaintext password, which RTSP's Digest needs; or `password_hash`, one of the two |
| `password_hash` | — | The bcrypt hash (as `fit hash-password` makes it), usable only with Basic and the query's user and pass, checked off fib's workers; RTMP, RTSP and SRT refuse such a user once, as a try again, until the password is found right |
| `publish` | none | The patterns of the streams the user may publish |
| `play` | none | The patterns of the streams the user may play |

### keys

| Field | Default | Description |
| --- | --- | --- |
| `key` | — | Required. The key's name: 1 to 64 letters, digits, `_`, `.`, `~` and `-`; a `media_tokens` request names it to sign with, and to revoke |
| `secret` | — | The key's secret, at least 16 bytes; or `secret_file`, one of the two |
| `secret_file` | — | A file holding the key's secret; rotating it refuses only the tokens this key signed |

Two keys sharing one secret are refused by the config: a token never carries a key id, so if two keys were
the same, rotating one would leave the other still good. The ring holds at most 256 keys.

### url_signing

| Field | Default | Description |
| --- | --- | --- |
| `ttl` | 10m | How long a URL is good for when the request names no ttl, and at most the route's `max_ttl` |
| `bind_address` | `ip` | How a URL bound to a viewer's address is bound: `ip`, that address alone, or `subnet`, its `/24` for IPv4 and `/64` for IPv6 |
| `play_grace` | 30m | How long the grant a playlist hands its content lives past the later of the URL's expiry and the item's end |
| `cookie` | off | Hands the grant as the `fit_media_grant` cookie on the resource's path, for players that drop a playlist's query parameters |
| `max_sessions` | none | The playback sessions one subject may have at once; no cap when left out |
| `on_limit` | `refuse` | Past it: `refuse` answers `403`, or `end_oldest` ends the subject's session that started first |
| `session_idle` | 2m | How long a session may go without a request and still count as playing |
| `max_tracked_sessions` | 100000 | The sessions the policy keeps of all its subjects; a new one past it is answered `503` |
| `referers`, `origins` | none | The origins of the pages and scripts that may play; a request with another Referer or Origin is `403`, one without any let through |
| `trusted_proxies` | none | The proxies in front of fit whose `X-Forwarded-For` is believed when a URL bound to an address is checked |

### media_tokens

The handler `media_tokens` is how a business backend issues tokens, and playback URLs, in batches over a
route of its own: it checks no caller credential itself — the route's `jwt`, `basic_auth`, `forward_auth`
or `client_cert` without `optional` does, and a route with none of them fails the config (a `rate_limit`
authenticates no one) — and it needs a policy with a `secret_file`, or a keyring whose
every key has one, so that a revoke can rewrite a file and outlive a restart. Its full fields are in
[the config reference: Handlers](../reference/config.md#http-handlers-media_tokens).

| Field | Default | Description |
| --- | --- | --- |
| `prefix` | `/media-tokens` | The path the route serves under; the route must match it and what is below it |
| `auth` | — | Required. The policy the tokens belong to: a single `secret_file`, or a keyring of them |
| `prefixes` | none | The stream-name prefixes a token may be signed for: every `publish` and `play` pattern must start with one |
| `max_ttl` | 24h | The longest a request may ask a token or URL to be good for; longer is `400` |
| `max_tokens` | 100 | The most tokens or URLs one request may ask for |
| `max_body_bytes` | 64 KiB | The largest request body the route takes |
| `revoke_timeout` | 10s | How long a revoke may wait for its secret files' writes before `504` |
| `url_base` | none | What the URLs `/urls` answers start with, before their paths |
| `url_prefixes` | none | The path prefixes `/urls` may sign URLs for |

`POST <prefix>` takes `{"key", "tokens": [{"subject", "publish", "play", "ttl"}]}` and answers
`{"tokens": [{"token", "subject", "expires", "key"}]}`; `key` names the key of the ring to sign with, the
first when left out. `POST <prefix>/revoke` rotates the named key's secret file, `{"key": "k2"}` or
`{"all": true}`, so that the tokens it signed are refused at once while the other keys' stay good until
their expiry. `POST <prefix>/urls` signs playback URLs when the policy has `url_signing`:
`{"subject", "address", "session", "ttl", "key", "paths"}`, at most `max_tokens` paths, each starting with
one of `url_prefixes`.

## In .conf

```fit
listener web :8080

media_auth site {
	secret a-secret-of-at-least-16-bytes
	token_ttl 12h

	tokens studio {
		token_sha256 751213bf907c13a8ffdc4f2bb17f74e8e4567c724395bc4eb3cb5fc661b58ae5
		publish live/*
	}

	users alice {
		password_hash $2a$14$Zkx19XLiW6VYouLHR5NmfOFU0z2GTNmpkT/5qqR7hx4IjWJPDhjvG
		publish live/*
		play *
	}

	public play

	url_signing {
		ttl 10m
		bind_address subnet
		play_grace 30m
		max_sessions 2
		on_limit end_oldest
		referers https://www.example.com
	}
}

route flv { match { path /live/* }; handler httpflv { auth site } }
route hls { match { path /hls/* }; handler hls { prefix /hls/; auth site } }
```

The static token `studio-publish-token` publishes `live/*` (the config holds its SHA-256), `alice` publishes
`live/*` and plays everything with her password, and anyone plays without credentials. So
`fit media-token -c fit.conf -policy site -publish 'live/*' -ttl 1h` prints a time-limited token that OBS
publishes with, and the site's API signs the expiring playback URLs for its viewers, which `hls` and
`httpflv` check on every request. The CLI takes `-c` (the config to read the policy from), `-policy`, one or
more `-publish` and `-play` patterns (repeated, or separated by commas), `-ttl` (the policy's `token_ttl`
when left out), `-subject` (`media-token` by default) and, for a keyring, `-key`; it needs the policy's `secret`, `secret_file` or
`keys`, because a token signed with a random key of the process could not be checked by a running fit. To
issue tokens in batches for many subjects — one key per meeting, group or region — use the `media_tokens`
handler: it needs the policy's `secret_file` or a keyring of `secret_file`s, and must sit behind `jwt`,
`basic_auth`, `forward_auth` or a `client_cert` without `optional`, because it checks no caller credential
itself. See [issuing tokens from a route](../webrtc.md#issuing-tokens-from-a-route).

## See also

- [the config reference: Media auth](../reference/config.md#media-auth): every field and every object.
- [WebRTC](../webrtc.md#auth): the `auth` of `whip`, `whep` and `webrtc_proxy`, the answers, and the session
  URL that needs no credentials.
- [playback URLs that expire](../media-library.md#playback-urls-that-expire): what a URL holds, what is
  checked, what a playlist hands out, the sessions and the revocation.
- [Streaming](20-streaming.md): the `auth` of the listeners and the handlers.
- [Media library](22-media-library.md): the URL signing and the playback URL pages.
- [Reverse proxy](15-reverse-proxy.md): the `media_tokens` and `media_pages` handlers on routes.
- [decision 030](../decisions/030-media-auth-and-pages.md): the design of the policies and the pages.
