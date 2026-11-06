# Media library

[English](media-library.md) | [简体中文](media-library.zh-CN.md)

A media library is directories of film and episode files that fit catalogues, for the handlers
that serve them as video on demand and the channels that play them as live streams
([decision 032](decisions/032-media-library.md)). This page covers the catalog: what a library
holds, how files are grouped into items, how scans run and what bounds them, and the Admin API that
reads it, and [video on demand](#video-on-demand), the `vod` handler that serves the items over
HLS. Playing them as live streams comes with the streaming role's [channels](channels.md).

Off by default: without `media_library`, no files are scanned or served.

```fit
media_library films {
	roots /srv/media/films /srv/media/shows
	exclude **/extras/**
	scan_interval 30m
}
```

The [config reference](reference/config.md#media-library) lists every field.

## Files are prepared, not transcoded

fit serves the codecs a file holds as they are stored: H.264, H.265 and AV1 video, AAC, Opus and MP3
audio, in MP4 files that are not fragmented (`.mp4`, `.m4v`, `.mov`, and `.m4a` for audio alone),
with the `moov` at the front or the back. Renditions, a film at 1080p and at 720p say, are files of
their own, made beforehand with ffmpeg; fit never transcodes. A file of other codecs, or one that
does not parse, is listed with its error and not served.

## Items

An item is a title: its renditions, its audio, its subtitles and its poster. Files are grouped by
their names, in one directory:

| File | What it is to the item `bbb` |
| --- | --- |
| `bbb.mp4` | a rendition, with no label |
| `bbb.720p.mp4`, `bbb.1080p60.mp4`, `bbb.4k.mp4`, `bbb.800k.mp4` | renditions labelled `720p`, `1080p60`, `4k`, `800k` |
| `bbb.audio-fr.m4a` | audio alone, French: `audio`, then `-` and a language |
| `bbb.srt`, `bbb.en.srt`, `bbb.fr.forced.vtt`, `bbb.en.default.sdh.vtt` | subtitles: a language, `forced`, `default`, and any other word as its name |
| `bbb.jpg`, `bbb.poster.png`, `bbb-poster.webp` | its poster |
| `bbb.json` | its sidecar |

A rendition is a file with video; a file with audio alone, or labelled `audio`, is the item's
audio. A subtitle or poster goes to the item of the longest name it starts with, so `bbb.2.srt`
belongs to `bbb.2.mp4` when there is one, and to `bbb` otherwise.

A sidecar, `<name>.json` beside the files, names what the naming rule cannot, by paths from the
sidecar's directory, which may lead into its subdirectories but not out of it:

```json
{
  "id": "sintel",
  "title": "Sintel",
  "poster": "art/sintel.jpg",
  "renditions": [{"file": "sintel-1080.mp4", "name": "1080p"}, {"file": "sintel-720.mp4", "name": "720p"}],
  "audio": [{"file": "sintel.fr.m4a", "name": "Français", "language": "fr", "default": false}],
  "subtitles": [{"file": "subs/sintel.zh.srt", "name": "中文", "language": "zh", "default": true, "forced": false}]
}
```

Every field may be left out. The files a sidecar names are its item's, whatever their names; the
files named after the sidecar are its item's too. A sidecar is read strictly, an unknown field
being an error, and one that does not parse, or names a file the library does not look at, is
reported among the catalog's problems.

## IDs

An item's `id` is its sidecar's, or its path under its root without its rendition label and
extension: `films/bbb` for `films/bbb.720p.mp4`. An ID is parts of letters, digits, `.`, `_`, `~`
and `-` joined by `/`, so that it is a URL's path as it is and a stream name a `media_auth` pattern
matches, as `vod/films/films/*`. A path with anything else, spaces or letters outside ASCII, has
each run of it made `-` and eight hex digits of a hash of the path after it: `clips/Big Buck
Bunny.mp4` is `clips/Big-Buck-Bunny-1f0c…`. The same path gives the same ID every scan, and two
paths never one. An ID two items would take, from two roots or two sidecars, goes to the first in
the order of the roots and the paths, and the other is reported.

## Scans

A library is scanned when it is made, every `scan_interval` (10 minutes unless set), when a reload
changes its roots, patterns or bounds, and on `POST /media/library/<name>/rescan`. A scan walks the
roots on a goroutine of the library's own, one scan at a time; a rescan asked for during a scan runs
once after it. Each media file is probed on the library's file pool: fib's `media/mp4` reads the
boxes to the `moov` and the `moov` but for its sample tables, a few kilobytes whatever the file's
length. A file whose size and time are those of the last scan is not probed again, so a scan of an
unchanged library costs a walk. The catalog a scan makes replaces the last one once it is whole;
whoever reads the old one keeps reading it.

A file that changes while it is probed, as one still being copied, is probed again once, and if it
changes again is listed with that error and probed again at the next scan. A file found changed or
gone when it is opened later is refused, and its sample tables dropped, until the next scan lists
it as it is.

`files` and `exclude` choose what is looked at, by pattern: a pattern without `/` matches a name
(`*.mp4`, `trailers`), one with `/` a path under the root (`films/**/*.mp4`, `**/extras/**`), `*`
matching within a name and `**` any run of directories. A directory excluded is not walked. Names
starting with `.` are never looked at.

## Paths out of the roots

Nothing outside the roots is read. A sidecar's path that leads out of its directory, with `..` or
from `/`, is refused. A symbolic link within its root is followed; one that leads out of its root is
refused, and reported, unless `follow_symlinks`. Each directory is walked once, so a link back up
does not loop, and no deeper than 32 directories. When a file is opened to be served, the link is
resolved again, so a file swapped for a link out of the root after the scan is refused too.

## Bounds

| Bound | Default | What it bounds |
| --- | --- | --- |
| `max_items` | 20000 | the items of the catalog |
| `max_catalog_bytes` | 64 MiB | the catalog's memory, as reckoned from its paths, titles, tracks and codec configurations |
| `max_files` | 200000 | the files and directories a scan looks at, read 256 at a time, however large a directory |
| `max_probes` | 4 | the files probed at once, so that a scan leaves the pool's other workers to the files served |
| `max_sidecar_bytes` | 64 KiB | a sidecar |
| `max_subtitle_bytes` | 2 MiB | a subtitle served |
| `max_table_bytes` | 128 MiB | the sample tables kept for the files being served, the least recently used dropped |
| `file_workers` | 4 a CPU, at least 16 | the file pool's goroutines: what the disk is asked to do at once |
| `file_queue`, `file_timeout` | 4096, 10s | the jobs waiting for the pool, and how long each may wait |

Past `max_items`, `max_catalog_bytes` or `max_files` the catalog lacks the rest and says why in
`truncated`; at most 256 problems are listed and the rest counted. Nothing touches the disk on a
fib worker: what does runs on the file pool, and a request waits for it retained.

## Reloads

A library is changed in place on reload: the handlers and channels that hold it see the new config.
A change of its roots, patterns or bounds scans it again; of its file pool's bounds starts a new
pool, the old one running what it has queued; of `max_table_bytes` drops tables past it. A library a
reload leaves out is removed: its scan stops, and what holds it is refused.

## The Admin API

| Method and path | What it answers |
| --- | --- |
| `GET /media/library` | every library: its roots, its scans' status, items and items with errors, problems, catalog bytes, file pool and sample tables |
| `GET /media/library/<name>` | the same of one library, its problems, and a page of its items: `offset`, `limit` (100, at most 1000), `q` (in the ID or title), `codec` (`h264`, `aac`, ...), `error` (`true` for items with a file in error), `min_height` and `max_height` (the height of the item's largest rendition) |
| `GET /media/library/<name>/items/<id>` | the item whole: its renditions with their tracks, codecs (RFC 6381), sizes, durations and bitrates, its audio, subtitles, poster and sidecar, and its errors |
| `POST /media/library/<name>/rescan` | `202`, a scan asked for at once: `queued` is false when one was waiting already |
| `POST /media/preview` | `{"library", "item"}` or `{"channel"}`: a URL of the item's (channel's) playlist, and of its poster, on the listener of the first `vod` (`hls`) route that serves it, good for two minutes: signed for the subject `workbench:<console user>` in a new session with the route's `url_signing` (`signed: "url"`), with a token of its `auth` (`"token"`), or as it is (`"public"`); with `expires`, `subject`, `session`, `route` and `cors`. 404 for a library, item, channel or route not found, 503 for a library not loaded yet |
| `GET /media/auth` | every `media_auth` policy: its `url_signing` as it runs, its keys (their names, which is the default, whether a file keeps the secret and when it was written; never a secret nor a path), its playback sessions and their subjects, the URLs refused by reason, and the `media_tokens` routes that sign with it |
| `GET /media/auth/<policy>/sessions` | a page of the policy's playback sessions, by subject and start: `offset`, `limit` (100, at most 1000), `subject` and `resource` (a prefix); with `total`, `matched`, `max_sessions` and the sessions playing of each subject on the page |
| `POST /media/auth/<policy>/sessions/end` | `{"subject", "id"}`: ends the session, whose next request is refused 403; 404 when it is not playing |
| `POST /media/auth/<policy>/rotate` | `{"key": "k1"}`, `{"all": true}`, or `{}` for a single secret: a new secret written to each one's `secret_file` and used at once, as `media_tokens`' revoke does; answers the keys rotated (`[""]` for the single secret) and `since`. 409 for a secret written in the config |
| `POST /media/auth/<policy>/urls` | `{"subject", "path", "address", "ttl", "route"}`: a test URL in a new session, as `media_tokens`' `/urls` signs it; with `route`, that `media_tokens` route's `url_prefixes`, `max_ttl` and `url_base` apply, and without, a `ttl` of at most an hour and the listener of the `vod` or `hls` route that serves the path |
| `GET /media/stats` | the libraries' items, file pools and sample tables; what the `vod` handlers served and their caches; each policy's sessions, refusals and the 50 resources with most sessions; each channel's state, its hub's subscribers and push targets |

The status says whether a scan runs or waits, when the last one started and ended, the files it
looked at, the media files it probes and has probed, and those it reused.

The workbench's console passes the user logged in and the host the browser addressed to these
endpoints, as `Fit-Console-User` and `Fit-Console-Host`; its pages may load images and media from
the listeners of the `vod` and `hls` routes. The URLs these endpoints answer are sent with
`Cache-Control: no-store` and never logged.

## Video on demand

The `vod` handler serves a library's items over HLS (RFC 8216), cut from the files as they are
stored: the samples are put in segments as they are, never decoded or encoded again.

```fit
route films {
	match { path /vod/films/* }
	handler vod {
		library films
		segment_duration 4s
		cache_control "public, max-age=86400"
	}
}
```

An item's URLs are its ID's path after `prefix`, `/vod/<library>/` unless set, and a name:

| Name | What it is |
| --- | --- |
| `index.m3u8` | the multivariant playlist, of the segments `container` names, `fmp4` unless set |
| `index.fmp4.m3u8`, `index.ts.m3u8` | the same, of fragmented MP4 or of MPEG-TS segments |
| `v0.fmp4.m3u8`, `a0.ts.m3u8` | the media playlist of rendition 0, or of audio file 0 |
| `v0.init.mp4` | the fMP4 initialization section of rendition 0 |
| `v0.12.m4s`, `v0.12.ts` | its segment 12 |
| `s0.m3u8`, `s0.vtt` | subtitle 0's playlist, and the subtitle as WebVTT |
| `v0.mp4`, `a0.m4a` | the file itself, of its own extension, with `Range`, for downloads and plain players |
| `poster.jpg` | the item's poster, of its own extension |

So `/vod/films/films/bbb/index.m3u8` plays the item `films/bbb`. The multivariant playlist lists
the renditions as variants, with `BANDWIDTH` and `AVERAGE-BANDWIDTH` (the file's bytes over its
length), `CODECS` and `RESOLUTION`; the item's audio files as an audio group, with the renditions'
own audio as its default where every rendition has some; and its subtitles as a subtitle group. An
item of audio alone has its audio files as variants. fMP4 carries H.264, H.265, AV1, AAC and Opus;
MPEG-TS the same but AV1, so a rendition of AV1 is left out of `index.ts.m3u8`. Other tracks are
left out: a file's first video and first audio track that a container carries are played.

**Segments.** A file with video is cut at keyframes, a segment ending at the keyframe nearest
`segment_duration` after its start: files made with a keyframe every `segment_duration`, or every
fraction of it (ffmpeg's `-g`, with `-sc_threshold 0` or `-force_key_frames`), are cut every
`segment_duration`, and renditions with their keyframes in the same places are cut alike, which
players switching between them like best. A segment holds the audio presented from its first frame
to the next segment's, so that a player starting at any segment, as a seek does, has its sound at
once. A segment that would pass `max_segment_bytes` is cut at an earlier keyframe, or, a file with
none for that long, without one, and its playlist then lacks `EXT-X-INDEPENDENT-SEGMENTS`. A file
of audio alone is cut every `segment_duration`.

Both containers carry one clock: a file's time 0 is 1.4s on the segments' clock, as ffmpeg's
MPEG-TS has it, so that the frames a file decodes before it presents its first (B-frames, AAC's
priming) keep their times; a subtitle has an `X-TIMESTAMP-MAP` saying so.

**Nothing waits on a worker.** A media playlist is cut from the file's sample tables, which the
library reads on its file pool and keeps (`max_table_bytes`); a segment's samples are read there
too, a run of them that lie together in one read, and put in a segment. A request waits for
them retained, up to `timeout` (30s), and is answered 503 past it, or at once when the pool's queue
is full. A segment asked for by many players at once is read once.

**What is kept.** The cuts of the files, their playlists, segments and subtitles are kept for the
next request, up to `max_cache_bytes` (256 MiB), the least recently used dropped first; what is
being built holds its bytes from when its work starts on the file pool, so the bound holds while it
is, and requests queued for the pool hold none. Handlers of the
same params share what they keep, and keep it across reloads that keep the params.

**Files that change.** A file changed or removed since the library's last scan is answered 404
when what is asked of it is read next, and what the handler and the library keep of it is dropped;
the next scan lists it as it is, and it plays again. A scan that drops a file drops what was kept
of it.

**Caches and CDNs.** Every playlist, segment, subtitle and file has `Last-Modified` from its file,
and every segment and file an `ETag` from the file's time and size, so conditional requests are
answered 304 and ranges of a segment 206. `cache_control` is sent with each, as
`public, max-age=86400`, so that a CDN in front keeps them; it is not sent unless set.

**Who plays.** `auth` names a [`media_auth`](reference/config.md#media-auth) policy every request must satisfy, with
`play` on the stream `vod/<library>/<id>`, as `vod/films/films/*`: a bearer token, Basic, the
`token` query parameter or the `fit_media_token` cookie. A playlist fetched with `?token=` names
what it lists with the token too, so that Safari and ffmpeg, which send no header, play on.
With the policy's `url_signing`, a [signed URL](#playback-urls-that-expire) of the item plays too.
Encrypted segments come later in the
[development plan](dev-plan.md#media-library-video-on-demand-and-channels).

## Playback URLs that expire

The site's API decides who watches; fit checks. A policy's `url_signing` turns on URLs signed for
one viewer and one resource, for a while, which the `vod` handler, and the streaming role's `hls`
and `httpflv` handlers (a channel's streams as any other), check on every request whose `auth`
names the policy: at once, on the worker, without calling anyone back. A URL that leaks dies soon
and opens nothing else.

```json
{
  "media_auth": [{
    "name": "site",
    "keys": [{"key": "k1", "secret_file": "/etc/fit/site-k1"}, {"key": "k2", "secret_file": "/etc/fit/site-k2"}],
    "url_signing": {"ttl": "10m", "play_grace": "30m", "max_sessions": 2, "on_limit": "end_oldest",
                    "referers": ["https://www.example.com"]}
  }],
  "routes": [
    {"match": {"path": ["/vod/films/*"]}, "handler": {"module": "vod", "params": {"library": "films", "auth": "site"}}},
    {"match": {"path": ["/media-tokens", "/media-tokens/*"]},
     "middleware": [{"module": "jwt", "params": {"jwks_url": "https://auth.example.com/.well-known/jwks.json"}}],
     "handler": {"module": "media_tokens", "params": {"auth": "site", "url_base": "https://media.example.com",
                                                      "url_prefixes": ["/vod/films/"], "max_ttl": "1h"}}}
  ]
}
```

**What a URL holds.** The site's API asks `media_tokens` for URLs, `POST /media-tokens/urls` with
`{"subject": "user-42", "address": "203.0.113.7", "ttl": "10m", "paths": ["/vod/films/films/bbb/index.m3u8"]}`,
and gets them back whole, for its page to hand hls.js or the player:

```
https://media.example.com/vod/films/films/bbb/index.m3u8?exp=1767225600&kid=k1&ip=203.0.113.7&sid=Qk9Hq3n2c8lYv1aF&sub=user-42&sig=0Wm2Yt7pV4lH8oXcQ1sZ9g
```

`exp` is the expiry, in Unix seconds; `kid` the key of the ring that signed it (none with a single
secret); `ip` the viewer's address, or its network (`/24`, or `/64` for IPv6) with
`bind_address: subnet`, when the request names one; `sid` the playback session, a new random one
unless the request names one; `sub` the subject, which the access log shows as the user, so make it
an opaque ID rather than a name or an e-mail; and `sig` the first 16 bytes of an HMAC-SHA256, in
base64url, over the resource, the expiry and these fields, with a key derived from the ring's key
or the policy's secret. The resource is the path up to the name: `/vod/films/films/bbb/` for an
item, `/hls/live/cam/` for a stream's playlists, and `/live/cam/` for `/live/cam.flv`. A site may
sign URLs itself, with the same key and the same message as `mediaauth.SignURL`; going through
`media_tokens` keeps the keys in fit.

**What is checked.** Every playlist, segment, init section, subtitle and file of the item: the key
and the signature, against the resource the request is for, so that a URL of one item is refused for
another and for its path changed in any byte; the expiry; the address, from the connection's peer or,
behind `trusted_proxies`, from `X-Forwarded-For`; a `Referer` and an `Origin`, when sent, against
`referers` and `origins`; and the session. A refused URL is answered 403, saying why: `expired`,
`signature`, `address`, `referer`, `origin`, `session_limit` or `session_ended`. A request without a
signature is checked as before, by a token, Basic or the cookie; one with a signature is let in by it
alone. The check allocates nothing; the one cost is the access log's redaction.

**What a playlist hands on.** A URL dies in minutes, a film runs for hours: a playlist fetched with
the URL names its playlists, segments, init sections, keys and subtitles with a grant of its own,
the same fields and `g=1`, which lives `play_grace` (30m) past the later of the URL's expiry and the
item's end, played from when the playlist is fetched. So a player that started goes on to the end,
through a pause or a seek, while the URL itself is refused once it expires. A playlist fetched with
the grant hands the same grant on, so it is never renewed; a live stream's grant lives `play_grace`
past the URL's expiry, after which the player needs a new URL from the site. A playlist with a grant
is the viewer's own: it goes out with `Cache-Control: private, no-store`, whatever the route's
`cache_control`. With `cookie`, for players that drop a playlist's query parameters, the grant is set
as the cookie `fit_media_grant` on the resource's path instead, `HttpOnly`, `SameSite=Lax`, or
`Secure; SameSite=None` over TLS, and the playlists name their URIs plainly, as the cache keeps them.

**Sessions.** `max_sessions` caps the playback sessions of a subject: those whose requests come
within `session_idle` (2m). Past it, `on_limit: refuse` answers a new session 403, and
`on_limit: end_oldest` ends the subject's session that started first, whose requests, and those of
every grant it was handed, are refused from then on until those grants would have expired. The
sessions are kept per policy, across reloads, in memory bounded by `max_tracked_sessions` (100,000),
a new session past it answered 503; on more than one fit, each counts the sessions it sees.

**Revoking.** Rotating a key of the ring, `POST /media-tokens/revoke` with `{"key": "k1"}`, refuses
at once the URLs and grants that key signed, and only those; rotating a single secret refuses all.

**Never in a log.** A request's `token` and `sig` are replaced by `REDACTED` in the URI the access
log writes, however it is answered, and a grant in a cookie is never logged.

## Metrics

`fit_media_library_items` (by `state`, ok or error), `fit_media_library_catalog_bytes`,
`fit_media_library_scans_total`, `fit_media_library_probes_total` (by `result`),
`fit_media_library_table_bytes`, `fit_media_library_table_lookups_total` (hit, miss),
`fit_media_library_pool_jobs` (queued, busy) and `fit_media_library_pool_refused_total` (refused,
expired), each by `library`, on the Admin API's `/metrics`. The `vod` handlers add
`fit_vod_requests_total`, `fit_vod_sent_bytes_total`, `fit_vod_cache_lookups_total` (hit, miss)
and `fit_vod_cache_bytes`, by `library`; `media_auth` adds `fit_media_auth_url_refused_total` (by
`policy` and `reason`) and `fit_media_auth_sessions` (by `policy`).
