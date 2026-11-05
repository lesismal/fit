# Streaming

[English](streaming.md) | [简体中文](streaming.zh-CN.md)

Config example, in .conf, in [fit-examples](https://github.com/lesismal/fit-examples): [streaming](https://github.com/lesismal/fit-examples/blob/main/streaming/fit.conf).

fit's streaming role runs fib's media hub beside the gateway, on its engine
([decision 030](decisions/030-media-auth-and-pages.md)): a stream is published once, by its path
as `live/cam`, over RTMP, RTSP, SRT or WHIP, and played over any of them, and over HTTP-FLV, HLS
and WHEP. Nothing is transcoded: each protocol carries the codecs it can of the stream.

| Protocol | Publish | Play | Where |
| --- | --- | --- | --- |
| RTMP | `rtmp://host:1935/live/cam` | the same | `streaming.rtmp` |
| RTSP | `rtsp://host:8554/live/cam`, ANNOUNCE and RECORD | DESCRIBE, SETUP and PLAY | `streaming.rtsp` |
| SRT | stream ID `#!::r=live/cam,m=publish` | stream ID `live/cam` | `streaming.srt` |
| WHIP and WHEP | `POST /whip/live/cam` | `POST /whep/live/cam` | the `stream_whip` and `stream_whep` handlers, on `streaming.webrtc`'s UDP port |
| HTTP-FLV, WebSocket-FLV | — | `GET /live/cam.flv` | the `httpflv` handler |
| HLS | — | `GET /hls/live/cam/index.m3u8` | the `hls` handler |

```fit
listener web :8080

media_auth live {
	public play
	tokens studio {
		token_sha256 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
		publish live/*
	}
}

streaming {
	rtmp { listen :1935; auth live }
	rtsp { listen :8554; auth live }
	srt { listen :8890; auth live }
	webrtc { listen :8188 }
}

route flv { match { path /live/* }; handler httpflv { auth live } }
route hls { match { path /hls/* }; handler hls { prefix /hls/; auth live } }
route whip { match { path /whip/* }; handler stream_whip { auth live } }
route whep { match { path /whep/* }; handler stream_whep { auth live } }
```

OBS or ffmpeg publishes with `rtmp://host/live/cam?token=<token>`, and anyone plays
`http://host:8080/live/cam.flv`, `http://host:8080/hls/live/cam/index.m3u8`,
`rtsp://host:8554/live/cam` or `srt://host:8890?streamid=live/cam`.

## The hub

Every stream lives in one hub, by its path. A publisher claims its path, and a second publisher of
the same path is refused until the first leaves. A stream keeps its latest group of pictures,
from its last key frame on, up to `gop_cache_bytes`, so that a new player starts at once with a
picture. Packets are handed to every player on the worker that received them: nothing waits on a
player, and one that falls behind has its packets dropped, video until the next key frame, and is
closed when it falls further.

| Field | Default | |
| --- | --- | --- |
| `max_streams` | 64 | streams at once; one more publisher is refused |
| `max_players` | 256 | players of one stream, over every protocol; HLS counts once a stream and format |
| `gop_cache_bytes` | 8 MiB | each stream's cached group of pictures |

## Auth

Every listener and handler names a [`media_auth`](reference/config.md#media-auth) policy with
`auth`, and is checked by the stream's path: `publish` to publish, `play` to play. Without `auth`
everyone may publish and play. Each protocol carries credentials its own way:

| Protocol | Credentials |
| --- | --- |
| RTMP | the query of the stream name or the app: `rtmp://host/live/cam?token=T`, or `?user=U&pass=P` |
| RTSP | `Authorization`: Digest, for users with a plaintext `password`, or Basic when `auth_methods` lists `basic`; or `?token=T` (or `user` and `pass`) in the URL |
| SRT | the stream ID's `s=` as a token (`#!::r=live/cam,m=publish,s=T`), or `u=` and `s=` as a user and password; or `?token=T` in the resource (`live/cam?token=T`) |
| HTTP-FLV, HLS, WHIP, WHEP | `Authorization: Bearer`, `Authorization: Basic`, the `token` query parameter or the `fit_media_token` cookie |

A refusal is each protocol's: RTMP's `NetStream.Publish.Unauthorized` or `NetStream.Play.Failed`,
the connection then closed; RTSP's `401` with the challenges, or `403`; SRT's handshake rejected
with `1401` (unauthorized) or `1403` (forbidden); HTTP's `401` with a `Bearer` challenge, or `403`.

An HLS playlist fetched with `?token=` names its media playlist, segments and parts with the token
too, so that a player that sends no header, as Safari and ffmpeg, plays on; every request of HLS is
checked. A token in a query is never logged: the access log writes it as `REDACTED`.

### Passwords checked with bcrypt

RTMP, RTSP and SRT decide in a hook that must answer before it returns, on the worker the request
came on, and nothing fit runs holds up a worker. A user with a `password_hash`, whose password the
policy has not found right since it was loaded, is therefore refused once, as a try again (RTMP's
refusal says so, RTSP answers `503` with `Retry-After: 1`, SRT rejects with `1402`), while the hash
is checked on mediaauth's own pool; once found right the password is remembered, and the client's
next attempt passes at once. Tokens and plaintext passwords pass the first time. Use tokens, signed
by `fit media-token` or static ones, for encoders that do not retry; the HTTP handlers wait for the
check, the request held without a worker.

## RTSP

RTP goes interleaved on the RTSP connection (RTP/AVP/TCP). A SETUP that asks for UDP is answered
`461 Unsupported Transport`; ffmpeg and most players then set up over TCP, and
`-rtsp_transport tcp` skips the first try. RTP over UDP waits on fib: its RTSP server cannot yet
remove the UDP bindings it adds, which a reload would need (fib request A61).

## SRT

A publisher's path is claimed in the handshake, so that a second publisher is rejected with `1409`
while the caller can still be told. A player of a stream without a publisher is rejected with
`1404`, unless `play_wait` lets it wait. `passphrase` encrypts every session, and callers must have
it too. A player starts at the next key frame, not the cached group of pictures, which a receiver's
UDP buffer could not take at once.

## WebRTC

`stream_whip` and `stream_whep` serve WHIP (RFC 9725) and WHEP on `streaming.webrtc`'s UDP port,
one for all their PeerConnections, fit an ICE lite agent on it. A POST of an offer is answered `201`
with the answer, its `Location` the POST's path followed by the session's ID; a DELETE of that URL
ends the session, and needs no credentials, the ID being the capability. H.264, H.265, AV1, Opus
and G.711 go both ways; AAC, which WebRTC cannot carry, is left out of what WHEP sends, so that an
RTMP stream of H.264 and AAC plays as video alone; nor is Opus turned into AAC for RTMP. The port is
the role's own, not `webrtc.media`'s, whose SFU serves the `whip` and `whep` handlers apart from the
hub.

## Limits and timeouts

| Field | Default | |
| --- | --- | --- |
| `rtmp.max_conns`, `rtsp.max_conns`, `srt.max_conns` | 1024 | connections or sessions of the listener |
| `rtmp.handshake_timeout` | 10s | from connecting to publishing or playing |
| `rtmp.idle_timeout` | 30s | a publisher that sends nothing, a player whose output does not move |
| `rtmp.max_message_bytes` | 8 MiB | the largest RTMP message |
| `rtsp.session_timeout`, `rtsp.idle_timeout` | 60s | a session, and a connection without one, that sends nothing |
| `srt.peer_idle_timeout` | 5s | a peer that sends nothing, keep-alives included |
| `webrtc.max_sessions` | 256 | WHIP and WHEP sessions |
| `webrtc.connect_timeout` | 15s | from the answer to a connected PeerConnection |
| `webrtc.max_offer_bytes` | 32 KiB | an offer, at most 64 KiB |
| `hls`'s `max_bytes` | 32 MiB | the segments a stream and format holds |

`play_wait` on each listener, `wait` on `httpflv` and `play_wait` on `hls` let a player wait for a
stream that has no publisher yet; without them such a play is refused at once.

## Reloading

A reload changes a listener's `auth` in place: the clients connected stay, and the next publish and
play are checked against the policy as it is now. A change of any other field of a listener starts
that listener again, closing its connections. A change of `max_streams`, `max_players` or
`gop_cache_bytes` starts the hub again, ending every stream and connection. `hls` keeps its
segments across a reload that keeps its params. Leaving `streaming` out stops the role, and its
handlers answer `503`.

## Metrics and logs

| Metric | Type | |
| --- | --- | --- |
| `fit_streaming_streams{state}` | gauge | streams, `publishing` or `waiting` for a publisher |
| `fit_streaming_players` | gauge | players of every stream |
| `fit_streaming_published_total` | counter | streams published |
| `fit_streaming_auth_refused_total{protocol,reason}` | counter | RTMP, RTSP and SRT clients refused: `unauthorized`, `forbidden`, `unavailable` (the policy not loaded, or a password being checked) |
| `fit_streaming_restarts_total` | counter | the hub started again for a change of its limits |

The role logs on the `webrtc` logger, prefixed `streaming:`: each listener's address as it starts,
and each stream published and unpublished.

## Interop

`scripts/streaming-interop.sh` runs the role against ffmpeg in a Linux container: ffmpeg publishes
over RTMP, RTSP (Digest and a query token) and SRT, and with missing or wrong credentials, which
are refused; every stream is played over RTMP, RTSP (TCP, Digest, and UDP falling back to TCP), SRT,
HTTP-FLV and HLS (fMP4 and MPEG-TS) with credentials, and refused without.
