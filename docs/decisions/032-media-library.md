# 032: The media library: video on demand, channels and playback URLs

[English](032-media-library.md) | [简体中文](032-media-library.zh-CN.md)

Status: accepted (2026-11-06). Builds on [decision 030](030-media-auth-and-pages.md) (media auth
and the streaming role). The track is I131–I139 of the
[development plan](../dev-plan.md#media-library-video-on-demand-and-channels).

## Context

A video site keeps its films and episodes as files and wants them played on the web: on demand,
and as channels that play files on a schedule as if live, pushed on to other servers if asked;
and only by the viewers its own API lets in, and only for a while. fit's streaming role (I116)
takes and serves live streams; nothing in fit serves a file's media as HLS, and fib had no reader
of MP4 files that are not fragmented, the form ffmpeg, encoders and cameras write. A file of a
two-hour film holds hundreds of thousands of samples: what is read of it, and when, and how much
of it is kept, decides whether fit can serve a library without blocking its workers or growing
without bound.

## Decisions

The track's principles, in the order they bind:

- **Files are prepared, not transcoded.** fit serves the codecs a file holds (H.264, H.265, AV1;
  AAC, Opus) as they are stored. Renditions are files of their own, made beforehand; a script in
  `scripts/` shows how with ffmpeg. Transcoding in process stays out, as in the streaming role.
- **Nothing blocks a worker** ([rule 1](../rules.md#1-never-block-a-worker)): probing a file,
  reading its sample table and reading a segment's bytes run on a bounded file pool of the
  library's own; a request waits retained and goes on from the pool. The catalog, the sample
  tables and the built segments are each bounded in bytes ([rule 11](../rules.md#4-timeouts-and-limits)).
- **The site's API decides who watches; fit checks.** fit keeps none of the site's accounts. The
  API signs a URL for a viewer, a resource and a lifetime, itself or through `media_tokens`, and
  fit checks it on the worker, at once, without calling back.
- **A leaked URL dies soon and opens nothing else.** URLs are short-lived, bound to one resource
  (a title or a channel) and, if asked, to the viewer's address or session; every playlist,
  segment, key and subtitle a player fetches carries the grant; playback sessions per subject are
  capped.
- **Off by default**: no `media_library`, no files served and no scans.

What each iteration decides:

- **I131, the MP4 reader is fib's** (A64 in [fib-api-requests.md](../fib-api-requests.md#a64-a-reader-of-mp4-files-that-are-not-fragmented)),
  in a new package `media/mp4` rather than in `media/fmp4`. `fmp4` reads and writes fragments
  held whole in memory, where `mp4` reads a file through an `io.ReaderAt` and keeps sample
  tables; they share the sample entry reader (`fmp4.ParseSampleEntry`), so a track's codec is
  read one way. The reader is synchronous over the `io.ReaderAt`: fit runs it on the file pool.
  - `mp4.Open` reads box headers to the `moov`, at the front or the back of the file, and reads
    the `moov` but for its sample tables, of which it takes the entry counts alone. So probing a
    file for the catalog (I132) reads some kilobytes, whatever its length.
  - `Track.ReadTable` reads a track's tables in one pass through a 16 KiB buffer into a compact
    `Table`: 4 bytes a sample for sizes (2 for `stz2`, none when constant), 12 a chunk, 16 a run
    of `stts`, 4 a sync sample, and the smaller of 8 a `ctts` run and 4 a sample for composition
    offsets; about 8.4 bytes a sample for H.264 with B-frames, under 2 MB for an hour at 60 fps.
    `Track.TableBytes` gives that size from the counts before the table is read, so the library's
    table cache admits or evicts by it.
  - Edit lists are applied as players apply them: the empty edits before the first that plays
    delay the track, and that edit's media time is presented at its start (`Track.Shift`,
    `Table.Time`); so ffmpeg's files with B-frames and AAC priming start at 0, as ffprobe shows
    them. Later edits are kept for whoever needs them, not applied.
  - Segments (I133) are cut from the table: `SeekKeyframe` and `KeyframeBefore`/`After` find the
    sync samples near a segment's start, `Samples` walks the samples in decode order at a constant
    cost each, and `MinCTS` gives the decode delay MPEG-TS needs where composition offsets are
    negative.
  - The reader trusts nothing in a file: sizes, counts and offsets are checked against the file
    and the limits (`MaxTracks`, `MaxBoxes`, `MaxSampleEntries`, `MaxEdits`, `MaxSamples`), every
    chunk is checked to lie within the file, and the table never takes more than four times the
    bytes of the file's own tables. It is fuzzed in fib's CI.
- **I132, the catalog** (`media_library`): roots, patterns and scans, items with stable IDs,
  renditions grouped by a naming rule or a sidecar, subtitles turned into WebVTT; probing on the
  file pool with bounded items, concurrent probes and sidecar size; paths out of the roots refused;
  a file that fails to probe listed with its error and not served.
- **I133, video on demand** (`vod`): HLS with fMP4 segments, or MPEG-TS for older players, built
  from the sample tables on request, cut at keyframes near `segment_duration`, and kept in a
  bounded cache; the file itself with `Range` for downloads; `ETag` and `Last-Modified` from the
  file, so that a CDN can keep segments; `media_auth`'s action `play` on `vod/<library>/<id>`.
- **I134, channels** (`streaming.channels`): items published into the hub, paced by the samples'
  timestamps on fit's timers, with timestamps kept continuous across items, served by every egress
  of the streaming role; `push` targets relay a channel or any stream to outside RTMP or SRT
  servers, reconnecting with backoff.
- **I135, playback URLs that expire** (`media_auth`'s `url_signing`): an HMAC over the resource's
  path prefix, the expiry, the key's ID and the bound fields (address, session), checked on the
  worker; the playlist hands what a player fetches next a grant of its own that lives `play_grace`
  past the URL's expiry or the item's length, whichever is longer; `media_tokens` issues URLs;
  sessions per subject capped; revocation by rotating a key.
- **I136, encrypted segments**: AES-128 per HLS, a key per item or channel derived from a library
  secret, served only to a grant for that resource; DRM with license servers stays out.
- **I137–I139**: an example video site, the workbench's pages for all of it, and files managed from
  the workbench (resumable uploads written to a temporary file on the file pool and renamed into
  place).
- **I137, a video site**: the example (fit-examples' `video-site`) is a library, `vod` and a
  channel, `media_tokens` on the loopback behind `basic_auth`, and an API of some 300 lines of Go
  that logs the site's own viewers in and asks for a URL signed for each. `media_pages` gains a
  library and a player page behind the policy's login, which sign their URLs for the logged-in user
  (`api/play`). hls.js 1.5.20 (Apache-2.0) is vendored with its license, taken from its npm package
  and checked against its shasum, served at `<prefix>/vendor/hls.min.js` for the site's page as
  well; it is no Go dependency, and is recorded here by rule 14.
- **I138, hls.js in the workbench**: the workbench's preview player plays with the same hls.js,
  loaded only when a player first needs it. fit keeps one copy, in the workbench's `web/vendor`
  with its license: `workbench.HLSJS()` hands it to `media_pages`, which serves it at its own
  `vendor/` path. The console's Content-Security-Policy opens `connect-src`, `img-src` and
  `media-src` (with `blob:`) to the listeners serving `vod` and `hls` routes alone, and no
  `worker-src`, so the player runs without a worker.
- **I139, files managed from the workbench**: uploads are resumable as tus makes them (`POST` to
  begin, `PATCH` at `Upload-Offset`, `HEAD` to resume), into the library's `upload_dir`, bounded by
  `max_upload_bytes`, `max_uploads` and `min_free_bytes`; the console streams a `PATCH`'s body to the
  file pool as it arrives (fib's `StreamRequestBodyAtHeader`, `OnBody`, `HoldBody` and `LimitBody`),
  into a temporary file kept across restarts and renamed into place once whole. Rename, move, delete
  and sidecar edits act on an item and all its files, on the pool, one at a time, never writing out of
  a root; a change to an item a channel names or plays, or a viewer plays, is refused unless forced,
  so that the workbench warns first; each is written to the assistant's audit log. A full tus server
  (its `Upload-Metadata`, `Tus-Resumable` and extensions) was not taken: the workbench and scripts need
  the three calls alone.

## Rejected

- **Transcoding in process.** It costs a CPU core or more per stream and needs codec libraries fit
  does not take; renditions made beforehand with ffmpeg serve the same players.
- **Probing with ffprobe, or a cgo demuxer.** A process per file, or a C dependency, for what a
  reader of a few hundred lines does over a `ReaderAt`; and neither runs in fit's own pools.
- **Reading the `moov` whole into memory, or a sample table of an entry a sample.** A long film's
  `moov` is megabytes, mostly sample tables; an entry a sample (offset, size, times, flags) takes
  24 bytes or more where the compact form takes about 8, and its size would not be known before it
  is read.
- **The reader in `media/fmp4`.** Fragments and whole files are read in different shapes (bytes in
  memory, against a `ReaderAt` and tables kept); one package for both would mix the two APIs.
- **DRM with license servers, watermarks per viewer, and libraries in object storage.** Not in this
  track; the file pool keeps reads behind one interface so that object storage can be added later.
