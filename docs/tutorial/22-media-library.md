# Media library

[English](22-media-library.md) | [简体中文](22-media-library.zh-CN.md)

## What it does

A media library is the directories fit catalogs: the films and shows it holds, scanned with their
subdirectories into a catalog of items, each item a work — its renditions, its audio tracks, its
subtitles and its poster. Items are served as video on demand by the `vod` routes
([Video on demand](../media-library.md#video-on-demand)) and played as live streams by the
[channels](../channels.md). fit never transcodes: files are served as they are stored.

In the menu, the **Media library** module (`gw.medialib`, tagged **VOD / channels**) sits beside
Streaming and Media auth with eight pages: **Libraries**, **Channels**, **Playback URLs** and
**Metrics**, and the four config pages **Library config**, **VOD routes**, **Channels config** and
**URL signing**. The first four pages read the Admin API; the last four edit the config's shared
draft.

Off by default: without a `media_library` nothing is scanned or served, and the page says so and
links to the config page. No page keeps a URL, a token or a signature: a preview's URL lives only in
its player, a test URL only in its form until the page changes, and secrets never reach the browser.

The library also reaches the audience: the `media_pages` handler on a route serves fit's own pages —
a room call with a chat over WHIP and WHEP, publish and play pages, and, with this module's
`libraries` (and `channels`), a library page and a player page through which a logged-in viewer plays
the items with HLS, on a playback URL the pages sign for that viewer. The handler is described below,
and in [WebRTC](../webrtc.md#the-media-pages).

## Pages

### Libraries

One card per running library: its items and how many of them have errors, its problems, the last scan
and the number of scans, the catalog memory, and its roots; under them the scan state — a progress bar
while a scan runs (files probed of those needing a probe, files looked at), a note when a scan waits to
start, otherwise idle — and the reason when the catalog is cut short. Several libraries switch by tabs.

**Rescan** asks first, then requests a scan: it says whether one was queued or already waiting, and the
progress is refreshed every second while a scan runs or waits, after which the item list is drawn again.

The item list: search titles and IDs (queried 250 ms after typing), filter by **Codec** (`h264`, `h265`,
`av1`, `aac`, `opus`, `mp3`), by **Resolution** (under 720p, 720p, 1080p, 4K and up, by the height of an
item's largest rendition) and by **Problems** (any, with, without); 50 items a page, with paging. Each
row shows the title, ID, duration, resolution, codecs, the file counts (video, audio, subtitles), the
size and its problem; below the list the files the last scan reported can be opened.

A library with an `upload_dir` gets an **Upload** card: pick the files and the folder, and each file is
sent in pieces of 8 MiB, one PATCH at the upload's offset; a piece that fails for the connection is sent
again from the offset the server has, and can be paused, resumed and cancelled. The uploads under way —
from this page, another page, or from before a restart — are resumed by picking their file again. Once a
file is whole, the scan that follows probes it.

### Item

Clicking a row of the list opens the item page. At the top are the probe errors, one per file, each with
an **Ask AI** button (skill `library-health`). The card gives the ID, the root, the path, the duration,
the size, the modified time, the poster and the sidecar.

**Preview** plays with hls.js, loaded when first needed: the URL is signed for the signed-in user by
`POST /media/preview`, good for two minutes; the page shows whether it is a signed URL, a short-lived
token or public, with the subject and the time it expires. When the route sets no `cors` the page warns
and links to **VOD routes**. The player is muted and stops at the first refusal (401, 403, 404, 410),
saying why: the URL and its grant expired, the key was rotated, or the session ended.

**Manage files**: rename, move and delete, each of the item's files going with it. The page first asks
what plays the item — the channels that name it, the channels playing it, the sessions playing it —
warns about it in the confirm, and only sets `force` when going on. The **sidecar** is edited in a form:
ID, title, poster, renditions, audio files and subtitles; before writing it checks the files it names, a
sidecar changed since the form read it is refused (409), and changing the ID is confirmed.

The page also lists the **Renditions** and the **Audio files**: name, codec (RFC 6381), detail
(resolution, or sample rate and channels), language, duration, samples and the bytes of its sample table;
and the **Subtitles** with their name, language, format, file, size and problem.

### Channels

The Channels page edits `streaming.channels` as a timeline: the list and the schedule are the config's,
and **Save and apply** makes the channel play them from its next item on. Each channel has its own tab,
with **Add channel** and **Remove channel** (a channel renamed is another channel: the old one ends).

The settings row: the stream name, the library, the time zone, the rendition, **Shuffle** (`order`
`shuffle`) and **Loop** (`loop`).

The **Playlist** draws the items as a bar of segments by their length and as a list: drag items in from
the library or add them with `+`, drag to reorder, remove row by row; it shows the number of items and the
total duration, and marks the item playing now. The **Schedule** draws a day in the channel's time zone:
a tick every three hours, a marker for now, and each slot drawn from its time with a width by its item's
duration; dropping an item on the day starts a slot there. The slots list edits the time (`HH:MM` every
day; `mon,wed,fri 20:00` or `sat-sun 09:30` on those days; `2026-12-31 23:00` once) and the item, or
**Add slot**. On the right, **Library items** can be searched and dragged into the playlist or the day, or
added with `+`.

The state is refreshed every 2 s: the state, what plays now (with its position), what is next, the next
slot, the read ahead, and the counters for played, skipped and underruns, with **Start**, **Stop**,
**Skip** and **Play now**; the preview card is kept across the page's redraws, so a draft's edit does not
stop it.

**Push targets** list each target: its server, its state, its last error (with an **Ask AI** button, skill
`channel-scheduler`), the bytes sent, the bit rate, the connects and failures, and start, stop and
**Restart**; below the channel the page shows every push target.

### Playback URLs

One tab per `media_auth` policy. A policy with no `url_signing` is named, with the note that its URLs do
not expire, and a link to the **URL signing** page.

- The **URL signing** card shows the settings running now: `ttl`, `play_grace`, `session_idle`,
  `max_sessions`, `on_limit`, `cookie`, `bind_address`, `referers` and `origins`; **Edit** links to the
  URL signing page.
- The **Test URL**: a subject, an item (which fills in its path), the path, the address, the lifetime, and
  the route to go through (a `media_tokens` route of this policy, whose `url_prefixes`, `max_ttl` and
  `url_base` then apply). **Issue and play** shows the URL here and plays it below; it is not kept
  anywhere and is gone when you leave the page, and its expiry and session are shown.
- The **Keys**, by name (or the policy's secret when there is a single one), with where each is kept — its
  `secret_file` or the config (inline) — and its age, each with **Rotate**. The key itself is never shown.
  **Rotate every key** is only available when the ring has more than one key and all of them are kept in
  files; an inline secret is rotated by editing the config. The confirm says that every URL and token the
  key signed is refused at once and that players using them stop.
- **Refused URLs by reason**: `expired`, `signature`, `address`, `referer`, `origin`, `session_limit` and
  `session_ended`; when there are refusals an **Ask AI** button (skill `playback-troubleshoot`) is offered.
- **Playback sessions**, paged 50 and sorted by subject and start time, filtered by subject and refreshed
  every 5 s: the subject, its count against `max_sessions`, the session, the resource, the address, when it
  started and its last request. Each session can be **Ended**, after which its player is refused from its
  next request on (403).

### Metrics

Read from `GET /media/stats` every 2 s; the counts are since fit started and the rates are over the last
refresh.

- **Libraries**: each library's items and the ones with errors, its file pool (busy, queued of the queue),
  the pool's refusals (refused and expired), and its sample tables (bytes of the max, and the hit rate).
- **Video on demand**: each `vod` handler's viewers now (the sessions whose resource is under one of its
  routes' prefixes), its requests a second, the bytes it sends a second, and its segment cache (bytes of the
  max, and the hit rate).
- **Playback URLs**: each policy's viewers and its 403s by reason, with what changed since the last refresh.
- **Channels**: the state, the players, and each push target's bit rate and bytes.

### Library config

Edits the `media_library` member: **Add library**, each row showing its roots; the raw tabs (UI, .conf,
.yaml, .json) hold the whole member. A save's problems in these members link back here.

### VOD routes

The routes whose handler is `vod`: **Add VOD route**, each row showing its library, its path, its `auth`,
and a note when it sets no `cors`. The raw tabs hold the `routes` member: every route, not only these.

### Channels config

`streaming.channels` and `streaming.push`: add a channel and a push target; the raw tabs hold the whole
`streaming` member. The Channels page edits the same lists as a timeline.

### URL signing

Each `media_auth` policy's `url_signing`; the raw tabs hold the whole `media_auth` member, its secrets left
as the draft holds them. With no policy the page links to the [Media auth](21-media-auth.md) page.

### Media pages

Not a menu page: the `media_pages` handler, on a route, serves fit's own pages to the audience, embedded in
the binary. They are an index with a login, a room call with a chat of text messages and small files, a
publish page and a play page, all over the WHIP and WHEP endpoints of `whip` and `whep`, or of the
streaming role's `stream_whip` and `stream_whep`, on this server or another. With `libraries` they gain a
library page (`<prefix>/library`), listing this module's items the viewer may play, and a player page
(`<prefix>/watch`) that plays one with hls.js, vendored in the binary; with `channels` the streaming role's
channels are listed there too.

With `login`, a policy's user logs in once on the pages: the token the policy signs for its `token_ttl`
comes back as the `HttpOnly`, `SameSite=Strict` cookie `fit_media_token`, which the WHIP and WHEP requests
carry; a token pasted in, as `fit media-token` prints, works too. The player asks `api/play` for its URL:
with the policy's `url_signing`, and playing not public, a playback URL signed for that viewer, its address
and a playback session of its own, whose playlists hand the player the grant it plays on; otherwise the
plain URL, which the login's cookie lets in. A refused URL stops the player.

The room call is the other half: each member publishes `<room>-<name>` and plays the others, and the
microphone, camera and screen switches never renegotiate the PeerConnection; the room API announces them,
so the others show "microphone off" or the member's avatar rather than wait on media. The chat carries text
and files through the same API. The page fields and their bounds are below, and the pages themselves are in
[WebRTC](../webrtc.md#the-media-pages).

## Key parameters

The key fields of `media_library` (the full table is in
[the config reference](../reference/config.md#media-library)):

| field | default | description |
| --- | --- | --- |
| `name` | required | The library's name, which handlers and the Admin API's `/media/library/<name>` refer to it by: letters, digits, `_`, `.` and `-` |
| `roots` | required | The directories scanned with their subdirectories; an item's ID is its path under its root; a root may not lie inside another |
| `files` | every file | The files looked at, by pattern: one without `/` matches a name (`*.mp4`), one with `/` its path under the root (`films/**/*.mp4`); names starting with `.` are never looked at |
| `exclude` | none | Files and directories left out, by the same patterns (`**/extras/**`); a directory left out is not walked |
| `follow_symlinks` | false | Follow symbolic links that lead out of the roots; one that stays inside is followed either way, one that leads out is refused and reported |
| `scan_interval` | `10m` | How often the roots are scanned again (at least 10s); a scan also runs when the library is made, when the config changes its roots, patterns or bounds, and on the page's rescan; a file whose size and time are unchanged is not probed again |
| `max_items` | 20000 | The items the catalog holds; past it the rest are left out and the page says so |
| `max_files` | 200000 | The files and directories a scan looks at |
| `max_catalog_bytes` | 64 MiB | The memory the catalog takes, as reckoned from its items' paths, titles, tracks and codec configurations |
| `max_probes` | 4 | The files probed at once on the file pool, so that a scan leaves the pool's other workers to the files being served |
| `max_sidecar_bytes` | 64 KiB | The largest sidecar (`<name>.json`) read; a larger one is reported and left out |
| `max_subtitle_bytes` | 2 MiB | The largest subtitle served (`.vtt`, or `.srt` turned into WebVTT) |
| `max_table_bytes` | 128 MiB | The memory of the sample tables kept for the files being served; past it the least recently used are dropped |
| `file_workers` | 4 a CPU, at least 16 | The goroutines of the file pool, which probe and read the files' bytes, so that no fib worker waits on the disk |
| `file_queue`, `file_timeout` | 4096, 10s | The jobs that wait for the file pool and how long one may wait; past either the request is answered 503 |
| `secret` / `secret_file` | none | The secret (at least 16 bytes) the AES-128 keys of encrypted segments are derived from; without it nothing of the library can be encrypted |
| `upload_dir` | none | The directory inside a root that uploads are written into; left out, the library takes no uploads, and its items are still renamed, moved and deleted |
| `max_upload_bytes`, `max_uploads`, `min_free_bytes`, `upload_expiry` | 32 GiB, 4, 1 GiB, 24h | A file's size (413), the uploads under way at once (429), the disk space left free (507), and how long an upload not written to is kept before the next scan gives it up |

The key fields of the `vod` handler (the full table is in
[the config reference](../reference/config.md#http-handlers-vod)): `library` (required), `prefix`
(`/vod/<library>/` when left out), `auth` (a [Media auth](21-media-auth.md) policy), `container` (`fmp4` or
`ts`), `segment_duration` (4s), `max_cache_bytes` (256 MiB), `max_segment_bytes` (32 MiB), `cache_control`,
`cors`, `timeout` (30s) and `encrypt`.

The channel's fields (`streaming.channels[]`, its `schedule[]` and its `push[]`) are in
[the config reference](../reference/config.md#config-channelslot) and in
[What a channel plays](../channels.md#what-a-channel-plays); the fields of `url_signing` are in
[the config reference](../reference/config.md#config-mediaauth).

The key fields of the `media_pages` handler (the full table is in
[the config reference](../reference/config.md#http-handlers-media_pages)):

| field | default | description |
| --- | --- | --- |
| `prefix` | `/media` | The path the pages are served under; the route must match it and what is below it |
| `auth` | none | The policy the room API checks, and the policy whose users log in and whose tokens the pages use |
| `login` | off | A login form for the policy's users; a token signed for `token_ttl` comes back as the cookie `fit_media_token` |
| `pages` | on | Serves the index, call, publish and play pages; off serves none of them, for a deployment whose page is its own |
| `title` | fit media | The pages' title |
| `whip`, `whep` | `/whip/{stream}`, `/whep/{stream}` | The endpoints the pages publish to and play from, with `{stream}` filled in |
| `hls` | none | A stream's HLS playlist URL, for the play page where the browser plays HLS itself |
| `links` | none | Further URLs the publish and play pages show for tools outside the browser; `{stream}`, `{token}` and `{host}` are filled in |
| `ice_servers` | none | STUN and TURN servers the pages' PeerConnections use, as a browser's `iceServers` |
| `libraries` | none | The libraries whose items the library page lists, at most 16; the items the viewer may `play` |
| `vod` | `/vod/{library}/{id}/index.m3u8` | The URL of an item's multivariant playlist, with `{library}` and `{id}` filled in |
| `channels` | off | Lists the streaming role's channels on the library page too; needs `hls` |
| `chat` | on | The room call's chat of text messages and small files |
| `max_file_bytes` | 5 MiB | The largest file a chat message may carry |
| `max_rooms`, `max_members` | 64, 16 | The rooms at once, and the members of one room, with twice as many watchers |
| `mic_default`, `cam_default` | off | Whether a member joins with the microphone or the camera on; `mic=0\|1` and `cam=0\|1` in the URL override it |
| `member_ttl`, `stall_timeout` | 30s, 30s | How long a member stays without a heartbeat, and how long an event stream may wait for its browser |

## In .conf

```fit
listener web {
	address :8080
}

media_library films {
	roots /srv/media/films /srv/media/shows
	files *.mp4 *.m4a *.srt *.vtt *.jpg *.json
	exclude **/extras/**
	scan_interval 30m
	max_items 50000
	max_probes 4
	secret_file /etc/fit/films-secret
	upload_dir /srv/media/films/incoming
}

media_auth staff {
	secret a-secret-of-at-least-16-bytes
	users operator {
		password_hash $2a$14$Zkx19XLiW6VYouLHR5NmfOFU0z2GTNmpkT/5qqR7hx4IjWJPDhjvG
		play *
	}
	url_signing {
		ttl 2m
		play_grace 30m
		max_sessions 2
	}
}

route films {
	match { path /vod/films/* }
	handler vod {
		library films
		auth staff
		segment_duration 4s
		cache_control "public, max-age=86400"
	}
}

route media {
	match { path /media /media/* }
	handler media_pages {
		auth staff
		login true
		libraries films
	}
}
```

So `http://host:8080/vod/films/films/bbb/index.m3u8` plays the item `films/bbb` for a viewer the policy
lets in: the console's preview signs a short-lived URL for the console user, and a player of the pages' logs
in as `operator`. The item page's preview loads its media through this route's listener, so the route should
set `cors`; otherwise the browser may refuse the preview from the console's origin. The staff open
`http://host:8080/media/`, log in as `operator`, and play the library from the pages' own player, on a
playback URL the pages sign for them, good for two minutes.

## See also

- [The media library](../media-library.md): items and their naming, IDs, scans, paths out of the roots, the
  bounds, managing files, video on demand, the playback URLs that expire and the encrypted segments.
- [The channels](../channels.md): what a channel plays, the schedules, the timestamps, reading files, the push
  targets, the Admin API and the metrics.
- [Media auth](21-media-auth.md): the `media_auth` policies and their `url_signing`.
- [Streaming](20-streaming.md): the `streaming` role and the `hls` and `httpflv` handlers.
- [The config reference: Media library](../reference/config.md#media-library),
  [Handlers](../reference/config.md#handlers) and [the assistant's media library](../console.md#the-media-library).
- [WebRTC](../webrtc.md#the-media-pages): the media pages, the room call and its chat, and the WHIP and WHEP
  endpoints they use.
- The page code: [app/workbench/web/gateway/media.js](../../app/workbench/web/gateway/media.js).
