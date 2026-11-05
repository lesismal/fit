fit's streaming role: one media hub, fib's, where a stream is published once, by its path as
`live/cam`, and played over every protocol. `rtmp` takes and plays streams as
`rtmp://host/live/cam`; `rtsp` as `rtsp://host/live/cam`, RTP interleaved on the connection;
`srt` by the caller's stream ID, as `#!::r=live/cam,m=publish`; and `webrtc` is the UDP port of
the [`stream_whip`](#stream_whip) and [`stream_whep`](#stream_whep) handlers. The
[`httpflv`](#httpflv) and [`hls`](#hls) handlers play the hub's streams on routes. The hub holds
at most `max_streams` streams and `max_players` players a stream, each stream keeping its latest
group of pictures, up to `gop_cache_bytes`, so that a new player starts at once with a picture;
each listener bounds its connections and the time a client may be idle. `rtmp` and `rtsp` may
take `mptcp`, to carry their TCP connections over Multipath TCP where the kernel offers it
(Linux 5.6+, with `net.mptcp.enabled`), falling back to plain TCP; off by default. SRT is UDP,
so it takes none.

Each listener names a [`media_auth`](#media-auth) policy with `auth`: RTMP takes a token, or a
user and password, in the query of the stream name (`rtmp://host/live/cam?token=T`); RTSP takes
Digest from users with a plaintext password, Basic when `auth_methods` offers it, or a token in
the URL's query; SRT takes a token as the stream ID's `s=`, a user and password as `u=` and `s=`,
or either in the resource's query. These protocols decide in a hook that answers before it
returns, and fit never holds up a worker: a `password_hash` user whose password the policy has
not found right before is refused once while it is checked off the worker, and passes at once
the next time; tokens and plaintext passwords pass the first time. A reload changes `auth` in
place; a change of another field of a listener starts that listener again, closing its
connections, and a change of the hub's limits ends every stream. See
[Streaming](../streaming.md) and [decision 030](../decisions/030-media-auth-and-pages.md).

```json
{
  "listeners": [{"name": "web", "address": ":8080"}],
  "media_auth": [
    {
      "name": "live",
      "public": ["play"],
      "tokens": [{"name": "studio", "token_sha256": "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", "publish": ["live/*"]}],
      "users": [{"name": "alice", "password": "alice-password", "publish": ["alice/*"], "play": ["*"]}]
    }
  ],
  "streaming": {
    "max_streams": 64,
    "max_players": 256,
    "rtmp": {"listen": ":1935", "auth": "live"},
    "rtsp": {"listen": ":8554", "auth": "live"},
    "srt": {"listen": ":8890", "auth": "live", "latency": "200ms"},
    "webrtc": {"listen": ":8188"}
  },
  "routes": [
    {"match": {"path": ["/live/*"]}, "handler": {"module": "httpflv", "params": {"auth": "live"}}},
    {"match": {"path": ["/hls/*"]}, "handler": {"module": "hls", "params": {"prefix": "/hls/", "auth": "live"}}},
    {"match": {"path": ["/whip/*"]}, "handler": {"module": "stream_whip", "params": {"auth": "live"}}},
    {"match": {"path": ["/whep/*"]}, "handler": {"module": "stream_whep", "params": {"auth": "live"}}}
  ]
}
```
