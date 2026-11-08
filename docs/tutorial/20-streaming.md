# Streaming

[English](20-streaming.md) | [简体中文](20-streaming.zh-CN.md)

## What it does

Streaming is a role fit runs beside the gateway, on the same engine: it runs fib's media hub. A stream is
published once, by its path (`live/cam`), and can be published over RTMP, RTSP, SRT or WHIP and played over
any of those protocols, as well as over HTTP-FLV, HLS and WHEP. Nothing is transcoded: each protocol carries
the codecs it can carry.

In the hub every stream keeps its latest group of pictures, from its last key frame on, up to
`gop_cache_bytes`, so that a new player starts at once with a picture. A publisher holds its path: a second
publisher of the same path is refused until the first one leaves. Packets are handed to every player on the
worker that received them, never waiting for any player; a player that falls behind has packets dropped
(video until the next key frame), and one that falls further is closed.

Four listeners belong to the role: `rtmp`, `rtsp`, `srt`, and `webrtc` (the UDP port of the `stream_whip` and
`stream_whep` handlers). Together with the `hls`, `httpflv` and `vod` handlers on routes they make the whole
line: the listeners take and play streams, the handlers play them over HTTP. Every listener and handler can
reference a [media auth](21-media-auth.md) policy with `auth`, checked by the stream's path: publishing needs
`publish`, playing needs `play`; without `auth` everyone may publish and play.

`channels` play the items of a [media library](22-media-library.md) into the hub as live streams, in order,
shuffled, looped or on a schedule, each under its name as the stream name; `push` relays a channel, or any
stream, to outside RTMP or SRT servers, reconnecting with backoff when it drops. A streaming role with
channels needs no listener that takes publishes.

| Protocol | Publish | Play | Where it is configured |
| --- | --- | --- | --- |
| RTMP | `rtmp://host:1935/live/cam` | same | `streaming.rtmp` |
| RTSP | `rtsp://host:8554/live/cam`, ANNOUNCE and RECORD | DESCRIBE, SETUP and PLAY | `streaming.rtsp` |
| SRT | stream ID `#!::r=live/cam,m=publish` | stream ID `live/cam` | `streaming.srt` |
| WHIP and WHEP | `POST /whip/live/cam` | `POST /whep/live/cam` | the `stream_whip` and `stream_whep` handlers, on `streaming.webrtc`'s port |
| HTTP-FLV, WebSocket-FLV | — | `GET /live/cam.flv` | the `httpflv` handler |
| HLS | — | `GET /hls/live/cam/index.m3u8` | the `hls` handler |

## Pages

### Streaming

The Streaming module in the menu has one page, which edits the `streaming` member of the config: the hub's
three limits, each listener, `access`, `channels` and `push`. The page has the UI, `.conf`, `.yaml` and
`.json` tabs, and edits the part of the shared draft that belongs to it; **Save and apply** in the top bar
saves the whole draft, **Check** takes the changes through the whole apply path (loading, compiling, trying
the new listener addresses) without applying them, and **Discard** drops the draft. A problem of a save links
back to this page, whichever page the save came from.

Above the form the page carries the note: **The streaming handlers themselves — HLS, HTTP-FLV and WHIP/WHEP
pushing — are set on routes**, with a link to the routes page ([Reverse proxy](15-reverse-proxy.md)). This
page writes the listeners and the role's own members; the handlers are on routes, where they reference the
same policy with their `auth`.

What can be done on the page:

- Write each listener's `listen` and `auth`, with its connection limit and timeouts; `auth` names the policy,
  which is created on the [Media auth](21-media-auth.md) page.
- Write `access`: the client addresses this role serves, deciding before the global `access`; its list files
  are watched and the config is applied again when one changes.
- Write `channels` and `push`: a channel plays the library's items as a live stream, a push target relays a
  stream to an outside server. These two are also edited on the **Channels config** page of the Media library
  module ([Media library](22-media-library.md)); both edit the same draft.

On reload `auth` changes in place: the clients already connected are kept, and the publishes and plays that
follow are checked against the current policy. A change of any other field of a listener starts that listener
again, closing its connections; a change of `max_streams`, `max_players` or `gop_cache_bytes` starts the hub
again, ending every stream and connection. Channels and push targets change in place: a channel's new list or
schedule applies from its next item, and a push target whose stream and URL are unchanged stays connected.
Leaving `streaming` out stops the role, and its handlers answer `503`.

## Key parameters

Only the key parameters are listed here; the full field tables are in
[the config reference: Streaming](../reference/config.md#streaming), and the handlers in
[the config reference: Handlers](../reference/config.md#handlers).

### The hub

| Field | Default | Description |
| --- | --- | --- |
| `max_streams` | 64 | The streams held at once; one more publisher is refused |
| `max_players` | 256 | The players of one stream, over every protocol; HLS counts once a stream and format |
| `gop_cache_bytes` | 8 MiB | The GOP each stream keeps |

### rtmp

| Field | Default | Description |
| --- | --- | --- |
| `listen` | — | Required. The TCP address RTMP clients connect to, as `:1935` |
| `auth` | none | The media_auth policy a publish and a play must satisfy; credentials go in the query of the stream name or the app: `?token=T` or `?user=U&pass=P` |
| `max_conns` | 1024 | The connections at once; one more is closed as it opens |
| `handshake_timeout` | 10s | From connecting to publishing or playing |
| `idle_timeout` | 30s | A publisher that sends nothing, and a player whose output does not move |
| `play_wait` | none | How long a player may wait for a stream with no publisher yet; without it such a play fails at once |
| `max_message_bytes` | 8 MiB | The largest RTMP message; a larger one closes the connection |
| `mptcp` | off | Carries the RTMP connections over Multipath TCP where the kernel offers it (Linux 5.6+) |

A refusal is answered with `NetStream.Publish.Unauthorized` or `NetStream.Play.Failed`, then the connection
is closed.

### rtsp

| Field | Default | Description |
| --- | --- | --- |
| `listen` | — | Required. The TCP address RTSP clients connect to, as `:8554`; RTP goes interleaved on the same connection (RTP/AVP/TCP) |
| `auth` | none | The policy the requests must satisfy; credentials are a user with a plaintext password by Digest, Basic when `auth_methods` offers it, or a token in the URL's query (`?token=T`) |
| `auth_methods` | `digest` | The schemes a 401 offers; a `password_hash` user can only use `basic` |
| `realm` | `fit` | The realm of the challenges, which Digest's response is computed with |
| `max_conns` | 1024 | The connections at once; one more is closed as it opens |
| `session_timeout` | 60s | A session that sends nothing |
| `idle_timeout` | 60s | A connection without a session |
| `play_wait` | none | How long a player may outlive its publisher, waiting for the next one |

A SETUP asking for UDP is answered `461 Unsupported Transport`; ffmpeg and most players then build over TCP.
A refusal is `401` with the challenges, or `403`.

### srt

| Field | Default | Description |
| --- | --- | --- |
| `listen` | — | Required. The UDP address SRT callers reach, as `:8890` |
| `auth` | none | The policy a caller must satisfy, `publish` for `m=publish` and `play` otherwise, by the stream ID's resource: a token as `s=` (`#!::r=live/cam,m=publish,s=T`), a user and password as `u=` and `s=`, or either in the resource's query |
| `max_conns` | 1024 | The sessions at once; one more caller is rejected with `1402` |
| `latency` | 120ms | The receive latency asked for; the caller's wins when larger |
| `passphrase` | none | Encrypts every session (10–79 characters), which callers must have too |
| `peer_idle_timeout` | 5s | A peer that sends nothing, keep-alives included |
| `play_wait` | none | How long a player may wait for a stream with no publisher yet; without it such a caller is rejected with `1404` |

A refused handshake is rejected with `1401` (unauthorized) or `1403` (forbidden). A player starts at the next
key frame, not at the cached GOP.

### webrtc

The UDP port the `stream_whip` and `stream_whep` handlers share, where fit is an ICE lite agent; it is not
[WebRTC](23-webrtc.md)'s `webrtc.media`.

| Field | Default | Description |
| --- | --- | --- |
| `listen` | — | Required. The UDP port every PeerConnection shares, as `:8188` |
| `public_ips` | none | The addresses peers are told to reach the port at (behind a 1:1 NAT) |
| `ice_servers` | none | The STUN and TURN servers peers are told of in `Link` headers |
| `max_sessions` | 256 | The WHIP and WHEP sessions at once; one more offer is answered `503` |
| `connect_timeout` | 15s | From the answer to a connected PeerConnection |
| `play_wait` | none | How long a WHEP request may wait for a stream with no publisher yet; without it it is answered `404` at once |
| `max_offer_bytes` | 32 KiB | An offer, at most 64 KiB; a larger one is answered `400` |

### channels and push

| Field | Default | Description |
| --- | --- | --- |
| `channels.name` | — | Required. The stream the channel publishes into the hub, as `tv/films`; a channel renamed is another channel |
| `channels.library` | — | Required. The `media_library` the channel's items are of |
| `channels.items` | none | The IDs of the items to play, in this order; at most 10000 |
| `channels.order` | `sequence` | Or `shuffle`: every item once in a random order each time round |
| `channels.loop` | on | Plays the items again from the first once the last has played |
| `channels.schedule` | none | The slots: an item started at a set time, cutting what plays; at most 1000 |
| `channels.time_zone` | `UTC` | The IANA time zone the schedule's times are in |
| `channels.read_ahead` | 3s | How much of the items is read ahead on the library's file pool |
| `channels.push` | none | The channel's own push targets |
| `push.stream` | — | The stream relayed: a stream of the hub or a channel's name (required in `streaming.push`) |
| `push.url` | — | Required. `rtmp://`, `rtmps://` or `srt://`; it holds a stream key, so the API and the logs show its scheme, host and app alone |
| `push.retry_min`, `push.retry_max` | 1s, 30s | The backoff between reconnects |
| `push.connect_timeout` | 10s | The handshake and publish time |

## In .conf

```fit
listener web :8080

media_auth live {
	public play
	tokens studio {
		token_sha256 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
		publish live/*
	}
}

media_library films {
	roots /srv/media/films
}

streaming {
	max_streams 64
	max_players 256
	gop_cache_bytes 8388608

	rtmp {
		listen :1935
		auth live
		max_conns 1024
		handshake_timeout 10s
		idle_timeout 30s
		play_wait 5s
	}
	rtsp {
		listen :8554
		auth live
		auth_methods digest basic
		session_timeout 60s
	}
	srt {
		listen :8890
		auth live
		latency 200ms
		passphrase a-passphrase-for-srt
		peer_idle_timeout 5s
	}
	webrtc {
		listen :8188
		max_sessions 256
		connect_timeout 15s
	}

	access {
		allow 10.0.0.0/8
	}

	channels {
		name tv/films
		library films
		items films/bbb films/sintel
		order shuffle
		loop true
		time_zone Asia/Shanghai
		schedule {
			at 20:00
			item films/sintel
		}
	}

	push {
		name backup
		stream live/cam
		url "srt://backup.example.com:9000?streamid=publish:live/cam&latency=200"
	}
}

route flv { match { path /live/* }; handler httpflv { auth live } }
route hls { match { path /hls/* }; handler hls { prefix /hls/; auth live; target_duration 2s } }
route whip { match { path /whip/* }; handler stream_whip { auth live } }
route whep { match { path /whep/* }; handler stream_whep { auth live } }
```

OBS or ffmpeg publish with `rtmp://host/live/cam?token=<token>`, and the policy's `public play` lets anyone
play `http://host:8080/live/cam.flv`, `http://host:8080/hls/live/cam/index.m3u8`, `rtsp://host:8554/live/cam`
or `srt://host:8890?streamid=live/cam`. A playlist fetched with `?token=` names what it lists with that token
too, so that players that send no header (Safari, ffmpeg) play on; a token in a query is not logged: the access
log writes it as `REDACTED`.

## See also

- [Streaming](../streaming.md): the hub, the protocols, the auth and the refusals, the bcrypt passwords, the
  reloads and the metrics.
- [Media auth](21-media-auth.md): the policy's tokens, users, key ring and `url_signing`.
- [Media library](22-media-library.md): the items, the channels, the playback URLs and the config pages.
- [Channels](../channels.md): the schedules, the timestamps, the backoff and the [reloads](../channels.md#reloads).
- [Reverse proxy](15-reverse-proxy.md): the `hls`, `httpflv`, `vod`, `stream_whip` and `stream_whep` handlers on routes.
- [WebRTC](23-webrtc.md): `webrtc.media`'s SFU and `webrtc.proxy`.
- [The config reference: Streaming](../reference/config.md#streaming) and
  [Handlers](../reference/config.md#handlers)
  ([`hls`](../reference/config.md#http-handlers-hls), [`httpflv`](../reference/config.md#http-handlers-httpflv),
  [`stream_whip`](../reference/config.md#http-handlers-stream_whip),
  [`stream_whep`](../reference/config.md#http-handlers-stream_whep),
  [`vod`](../reference/config.md#http-handlers-vod)).
