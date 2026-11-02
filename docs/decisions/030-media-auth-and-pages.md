# 030: Media auth, the streaming role and the media pages

[English](030-media-auth-and-pages.md) | [简体中文](030-media-auth-and-pages.zh-CN.md)

Status: accepted (2026-11-03). Builds on [decision 029](029-webrtc.md).

## Context

Users asked that fit's WebRTC, its streaming media and the other protocols that take media have
auth they can configure; that fit ship basic pages for them, behind configurable auth too; and
that those pages switch the microphone and the camera on and off, so that the other side sees the
switch rather than errors about media that does not come.

Until now, `whip` and `whep` had only the route's middleware: a bearer token checked by `jwt`, or
`basic_auth`. That covers HTTP alone, says nothing about which stream a credential may touch, and
cannot reach RTMP, RTSP or SRT, which have no route, nor a page's login. One policy, by name,
that every handler and role taking media refers to, is what the three parts of this work share:
I115 (media auth, this decision's first part), I116 (the streaming role) and I117 (the media
pages).

## Decision: media auth (I115)

- **Policies in the config.** A top-level `media_auth` lists named policies. A policy has static
  `tokens` (the token, or its `token_sha256` so that the config does not hold it), `users` with a
  `password` or a bcrypt `password_hash`, a `secret` for tokens fit signs, `public` actions anyone
  may take without credentials, and `token_ttl` for the tokens it issues. Each token and user lists
  the stream patterns it may `publish` and `play`; `*` matches any run of characters, `/` among
  them, and an empty list grants nothing. Handlers and roles name a policy with `auth`; a handler
  without it lets everyone in, as before.
- **Package `mediaauth`.** `Policy.Allow(action, stream, credentials)` returns the subject or an
  error that `Status` maps to 401 (none, or bad), 403 (not allowed) or 503 (not loaded, or the
  bcrypt pool full); `Check` and `Login` add a bcrypt check; `Password` gives RTSP Digest a
  plaintext password; `Issue` signs a token; `Guard` does all of it for an HTTP request, answering
  the refusals itself. `FromHTTP` reads `Authorization: Bearer`, `Authorization: Basic`, the
  `token` query parameter and the `fit_media_token` cookie, in that order; `FromQuery` reads
  `token`, or `user` and `pass`, from URLs of protocols without headers.
- **Nothing blocks a worker (rule 1).** A static token is looked up by its SHA-256, a signed one's
  HMAC checked, a plaintext password compared in constant time: on the worker, at once. bcrypt
  takes tens of milliseconds, so a `password_hash` is checked on a pool of the package's own, a
  quarter of the CPUs, at most 256 waiting and for 5 seconds (rule 11); the request waits retained
  and goes on from the pool. A password found right is remembered as an HMAC under a key of the
  process's, so its next check is at once; an unknown user's password is checked against a decoy
  hash when the policy has hashed users, so that a refusal takes as long either way.
- **Signed tokens.** `fm1.<payload>.<mac>`: the payload is base64url JSON of the subject, the
  expiry in Unix seconds and the publish and play patterns, the MAC HMAC-SHA256 over the prefix
  and the payload, under a key derived from the policy's `secret` and its name, so that a token is
  good for one policy alone. Without a `secret`, a random key of the process signs them, for the
  pages' logins, and a restart forgets them. `fit media-token` prints one, and refuses a policy
  without a `secret`, whose tokens a running fit could not check. Tokens are at most 4 KiB.
- **Found by engine and name, changed in place.** Policies are kept per engine, as `webrtc.media`'s
  server is, so that handlers find them in `Provision` and roles in their `Apply`. A `*Policy`
  stays the same across reloads: the role `media_auth`, applied with every config, changes it in
  place, and one the config no longer has refuses with 503. A handler's `Provision` refuses an
  `auth` that names no policy of its config, and makes a policy new to the engine from the config
  being compiled, so that it serves before the role applies it; the remembered passwords of users
  whose hash did not change are kept.
- **WHIP and WHEP.** `whip`, `whep` and `webrtc_proxy` (with `action`, `publish` or `play`) check
  a POST of an offer on its stream's name before reading its body, and a proxy before any media
  server sees it: `401` with `WWW-Authenticate: Bearer realm="fit"`, and `error="invalid_token"`
  for bad credentials (RFC 6750), or `403`. A session's URL, its DELETE among its requests, needs
  no credentials: RFC 9725 lets the session's URL be the capability, its ID is 128 random bits
  known only to whoever made it, and a token that expires during a long stream must not keep its
  peer from hanging up. `webrtc.turn` keeps its own users and secret, and `webrtc.media` still
  hands out TURN credentials from it.
- **No token in logs.** Tokens are never logged; a token taken from the query is replaced by
  `REDACTED` in the request's URI, which the access log writes, and the subject goes into the
  access log's `user`.

## The streaming role (I116)

- **One hub, a role.** A top-level `streaming` runs fib's `media/hub` on the listeners' engine, as
  `webrtc.media` runs its SFU: `rtmp`, `rtsp` and `srt` are listeners of the role's own, bindings
  added to the engine, and `webrtc` a UDP port (a fib `webrtc.Mux`) for fib's `media/rtc` server.
  Routes reach the hub through handlers: `httpflv` (fib's `media/httpflv`), `hls` (`media/hls`,
  fMP4 or MPEG-TS, Low-Latency HLS optional), and `stream_whip` and `stream_whep`, which map the
  route's paths onto fib's WHIP and WHEP server and its `Location` back onto the route. Handlers
  find the role by engine, as `whip` finds `webrtc.media`; the hub is the role's, so a stream
  outlives a reload of the routes. `webrtc.media` stays the SFU of `whip` and `whep`, apart from the
  hub: they share neither a port nor streams.
- **Auth in each protocol's own hook.** RTMP: `OnPublish` and `OnPlay`, the credentials in the
  query of the stream name or the app (`token`, or `user` and `pass`). RTSP: `OnRequest`, DESCRIBE
  and PLAY needing `play` and ANNOUNCE and RECORD `publish`, the other methods belonging to a
  session one of those began; credentials are Digest, checked by fit against a user's plaintext
  password (`Policy.Password`), Basic when `auth_methods` offers it, or a token in the URL's query,
  up to the first `/` after it, where clients write the track's control. fit answers the
  challenges itself rather than through fib's `Password`, which would demand an `Authorization` of
  every request and so refuse a token in the query. SRT: `OnConnect` in the handshake, `s=` a
  token or, with `u=`, a password, and the resource's query split off by fit's own stream ID
  parser so that `r=live/cam?token=T` names `live/cam`. HTTP: `mediaauth.Guard`, as `whip`.
- **A bcrypt password in a synchronous hook.** The three hooks answer before they return, on a
  fib worker. A `password_hash` user whose password the policy has not found right before
  (`ErrPending`) is refused there, as a try again (RTMP's message says so, RTSP 503 with
  `Retry-After: 1`, SRT 1402), while `Policy.Check` runs the hash on mediaauth's pool; a password
  found right is remembered, and the next attempt passes at once. Holding the handshake open for
  the check would need an asynchronous hook fib's servers do not have; refusing a client that
  retries is what keeps the worker free. Tokens and plaintext passwords never wait.
- **HLS carries a query token.** A playlist fetched with `?token=` names its playlists, segments,
  parts, maps and preload hints with the token (an `OnResponse` hook that rewrites the playlist's
  URIs), since players that send no header (Safari, ffmpeg) fetch them by the playlist's own
  relative URIs; every request, segment included, is checked.
- **Limits.** The hub's `max_streams`, `max_players` (over every protocol) and `gop_cache_bytes`;
  each listener's connections (`max_conns`, closed as they open past it), handshake, idle and
  session timeouts; WebRTC's sessions, connect timeout and offer size, at most 64 KiB so that the
  offer arrives whole as fib's server reads it; HLS's bytes a stream and format holds.
- **Reloads.** A listener's `auth` changes in place, its connections kept. A change of any other
  field of a listener starts that listener again, closing its connections, on its address (the old
  one first when they share a port; the old one started again if the new one cannot bind); a change
  of the hub's limits starts everything again on a new hub. `hls`'s segmenters are shared between
  configs by their params, so a reload that keeps them keeps the playlists' numbering.
- **RTSP over TCP only, for now.** fib's RTSP server binds its RTP and RTCP UDP ports with
  `ListenUDP` and has no way to remove them, which a restart or a stop would need on the shared
  engine; fit asks fib for it (A60) and takes interleaved RTP meanwhile. A SETUP asking for UDP is
  answered 461, and ffmpeg and most players set up again over TCP.

## The media pages (I117)

To be written with I117: the handler `media_pages`, its login, its room API, and the switches of
the microphone and the camera.

## Consequences

- One policy covers every protocol fit takes media over, by stream; the route's middleware still
  run first, so a policy can sit behind `jwt` or `forward_auth`.
- Users with a plaintext password are kept in the config in the clear, which RTSP's Digest needs;
  `password_hash` avoids it for every other protocol.
- A token cannot be revoked before it expires except by changing the policy's `secret`, which
  revokes them all; `token_ttl` keeps their life short.
- No new dependency: bcrypt is `golang.org/x/crypto`, which fit has already.
