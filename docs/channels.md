# Channels and push targets

[English](channels.md) | [简体中文](channels.zh-CN.md)

A channel plays the items of a [media library](media-library.md) into the
[streaming role](streaming.md)'s hub as a live stream: in order, shuffled, looped, or on a schedule
of start times ([decision 032](decisions/032-media-library.md)). Every egress of the role plays it by
the channel's name, under its `auth`: RTMP, RTSP, SRT, HTTP-FLV, HLS and WHEP. A push target relays a
channel, or any stream of the hub, to an outside RTMP or SRT server, a CDN's ingest say, and
reconnects when it drops. Nothing is transcoded: the items' codecs are sent as they are stored.

```fit
media_library films {
	roots /srv/media/films
}

streaming {
	rtmp { listen :1935 }
	srt { listen :8890 }

	channels {
		name tv/films
		library films
		items films/bbb films/sintel films/tears
		order shuffle
		time_zone Asia/Shanghai
		schedule {
			at 20:00
			item films/sintel
		}
		schedule {
			at "sat,sun 09:30"
			item films/bbb
		}
		push {
			url rtmp://live.example.com/app/stream-key
		}
	}

	push {
		name backup
		stream tv/films
		url "srt://backup.example.com:9000?streamid=publish:tv/films&latency=200"
	}
}

route hls { match { path /hls/* }; handler hls { prefix /hls/ } }
```

`rtmp://host/tv/films`, `http://host/hls/tv/films/index.m3u8` and `srt://host:8890?streamid=tv/films`
then play the channel; `live.example.com` and `backup.example.com` receive it.

## What a channel plays

| Field | Default | |
| --- | --- | --- |
| `name` | — | the stream's path in the hub, as `tv/films`; renaming a channel ends the old one |
| `library` | — | the `media_library` the items are of |
| `items` | none | the items' IDs, in order; an item the library does not hold, or cannot serve, is skipped |
| `order` | `sequence` | `sequence`, or `shuffle`: every item once in a random order each time round, never the same item twice in a row across rounds |
| `loop` | on | play the list again from the first once it has played |
| `schedule` | none | slots: an item started at a time, cutting what plays |
| `time_zone` | `UTC` | the IANA zone the slots' times are in, or `Local` |
| `rendition` | the best | the rendition played, by name; one an item lacks plays its best, the one with the most pixels, the highest bit rate among equals |

A channel plays its rendition's first video track and first audio track; a rendition without audio
takes the item's default audio file (`bbb.audio-fr.m4a`, or the sidecar's). H.264, H.265 and AV1
video and AAC, Opus and MP3 audio are carried, as each egress can: WHEP leaves AAC out, as it does
for any stream.

A channel with neither `items` nor a slot playing now is quiet: it stays published, so players stay,
and plays again at its next slot. With `loop` off, it goes quiet after the last item, until its next
slot, or until the Admin API stops and starts it, which plays the list from the first again.

## Schedules

A slot's `at` is in the channel's `time_zone`:

| `at` | When |
| --- | --- |
| `20:00`, `20:00:30` | every day |
| `mon,wed,fri 20:00`, `sat-sun 09:30`, `fri-mon 23:00` | on those days of the week |
| `2026-12-31 23:00`, `2026-12-31T23:00:00` | once |

At its time a slot's item starts: its first picture is shown at the slot's start, on the channel's
timeline. The item that plays before it is cut there, read only up to it; an item that ends as the
slot starts is followed by it without a gap. A slot under way when the channel starts, or when a
reload makes a quiet channel play, is joined where it would be, from the keyframe at or before that
point. A slot whose item would be over is passed over; so is a slot whose time a reload passes. When
the slot's item ends, the channel goes back to its `items`, after the last it played.

Times of day are as written across changes of the clocks: `01:30` is 01:30 local time, the first of
the two on the night the clocks go back.

## Timestamps

A channel is paced by its samples' decode times, on fit's timers: each packet is sent into the hub
when it is due, never ahead, so that players and segmenters see a live stream. One item follows the
last by their presentation: the next item's first picture is shown as the last one's ends, its
decode times carrying on from the last item's, each track's never going back. A track's samples
shown wholly before an item's start, as AAC's priming, are left out.

An item whose tracks are the last one's (the same codecs, with the same configurations: films
encoded the same way) carries on with no change a player can see. An item whose tracks differ sets
them anew: HLS marks a discontinuity (`#EXT-X-DISCONTINUITY`) and, with fMP4, a new initialization
section, RTMP and HTTP-FLV players get new sequence headers, and RTSP players end when the codecs
change, as for any stream. The timestamps run on across the change: fib's egresses take new tracks
for a clock that started again only when a packet comes at or before the last of its own kind
([fib request A68](fib-api-requests.md#a68-new-tracks-on-the-same-clock)), which a channel's next
item never does.

## Reading files

Nothing a channel does blocks a worker ([rule 1](rules.md#1-never-block-a-worker)). Its items'
sample tables are read by the library, kept within the library's `max_table_bytes`; their samples
are read on the library's file pool, a second of media at a time, up to `read_ahead` (3s) and
`max_buffer_bytes` (16 MiB) ahead of what is sent. The next item is loaded and read while the last
one plays. When the pool or the disk is too slow, the channel pauses rather than sending what it
read late in a burst, and counts an underrun. A file changed or removed while it plays ends its item
where it was read to; the next scan lists it as it is.

## Push targets

| Field | Default | |
| --- | --- | --- |
| `name` | the stream and its place, as `tv/films#1` | the target's name in the Admin API and the metrics |
| `stream` | the channel's | the stream relayed, in `streaming.push` |
| `url` | — | `rtmp://host[:port]/app/stream`, `rtmps://…`, or `srt://host:port?streamid=…&passphrase=…&pbkeylen=…&latency=ms` |
| `retry_min`, `retry_max` | 1s, 30s | the backoff between attempts, doubling from `retry_min` to `retry_max` |
| `connect_timeout` | 10s | the RTMP handshake and publish, or the SRT handshake |

A target waits for its stream to be published, checking every `retry_min`, then dials from the
role's engine. Once the server takes the stream, it is sent what the hub has: the tracks, the cached
group of pictures, and every packet; a target too slow to take them has packets dropped, video until
the next keyframe, as any player. A target that refuses, drops or cannot be reached is tried again
after the backoff; one that stayed up `retry_max` starts again from `retry_min`. A target keeps its
connection for 5s while its stream has no publisher, as a channel stopped and started.

A push URL holds a stream key: the Admin API, the metrics and the logs show its scheme, host and RTMP
app alone, never the key, nor SRT's stream ID and passphrase.

## Reloads

A reload changes channels and push targets in place. A channel's new `items`, `order`, `schedule`,
`library` or `rendition` takes effect from its next item: the item playing plays on (a slot whose
start the reload passes is not started by it). A channel renamed is another: the old one's stream
ends. A push target whose stream, URL and `connect_timeout` are unchanged stays connected; another
connects again. A change of the hub's limits ends every stream, channels and pushes too, and starts
them again.

## The Admin API

| Endpoint | |
| --- | --- |
| `GET /streaming/channels` | every channel: its state (`playing`, `loading`, `idle`, `waiting` for its stream's path, `stopped`), the item playing (`now`: item, title, rendition, where it came from, its slot, position), the item read to follow it (`next`), the next slot, what is read ahead, its counters and its push targets |
| `GET /streaming/channels/{name}` | one channel |
| `POST /streaming/channels/{name}` | `{"action": "stop"}`, `{"action": "start"}`, `{"action": "skip"}` to the next item, `{"action": "skip", "item": "<id>"}` to that item |
| `GET /streaming/push` | every push target: its state (`connected`, `connecting`, `retrying`, `waiting` for its stream, `stopped`), the last error, connections and failures, bytes and bit rate |
| `POST /streaming/push/{name}` | `{"action": "stop"}`, `{"action": "start"}`, `{"action": "restart"}` |

A name with `/` goes in the path as it is (`/streaming/channels/tv/films`); `#` is `%23`. A stop
unpublishes the channel; a start plays its list from where it was. Actions are not kept in the
config: a stopped channel stays stopped across reloads that keep it, and runs again when fit or the
hub starts again.

## Metrics

| Metric | Type | |
| --- | --- | --- |
| `fit_streaming_channels{state}` | gauge | channels by state |
| `fit_streaming_channel_items_total{channel,result}` | counter | items started (`played`) or skipped (`failed`) |
| `fit_streaming_channel_underruns_total{channel}` | counter | pauses for reads too slow |
| `fit_streaming_push_targets{state}` | gauge | push targets by state |
| `fit_streaming_push_connects_total{target,result}` | counter | connections taken (`ok`) or failed and dropped (`failed`) |
| `fit_streaming_push_bytes_total{target}` | counter | bytes sent: the media handed to RTMP, the payload SRT sent |

The role logs each item a channel plays, each item skipped and why, and each push target's failure.

## Interop

`test/docker/channels/e2e.sh` runs `streaming`'s `TestChannelInterop` in a Linux container: ffmpeg
makes the items, ffprobe reads a channel over RTMP, RTSP, SRT, HTTP-FLV and HLS (fMP4 and MPEG-TS)
and finds its decode times continuous across items, a headless Chromium plays channels with hls.js,
through discontinuities, and a channel is pushed over RTMP and SRT to MediaMTX in a container, which
is restarted and gets the stream back.
