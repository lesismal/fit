Named libraries of media files: each library's `roots` are directories, scanned with their
subdirectories into a catalog of items. An item is a title: its renditions, MP4 files (`.mp4`,
`.m4v`, `.mov`, and `.m4a` for audio alone) of the codecs fit serves as they are stored (H.264,
H.265, AV1; AAC, Opus), made beforehand, since fit does not transcode; its subtitles, `.vtt`, and
`.srt` served as WebVTT; and its poster, a `.jpg`, `.png` or `.webp`. Files are grouped by their
names: `bbb.mp4` and `bbb.720p.mp4` are renditions of the item `bbb`, `bbb.audio-fr.m4a` its
French audio, `bbb.en.srt` and `bbb.fr.forced.vtt` its subtitles, and `bbb.jpg` or
`bbb.poster.jpg` its poster; or by a sidecar, `bbb.json`, which gives the item its `id`, `title`
and `poster` and lists its `renditions`, `audio` and `subtitles` by their paths from the sidecar's
directory, which they may not lead out of. An item's `id` is the sidecar's, or its path under
its root, as `films/bbb`; a path with what an ID may not hold, spaces among them, gets those made
`-` and a hash of the path after it, so that every scan gives it the same ID.

A library is scanned when it is made, every `scan_interval`, when a reload changes its roots,
patterns or bounds, and on the Admin API's `POST /media/library/<name>/rescan`. A scan walks the
roots on a goroutine of the library's own and probes the media files on the library's file pool,
`max_probes` at once, reading a few kilobytes of each whatever its length; a file whose size and
time are unchanged since the last scan is not probed again, and the catalog a scan makes replaces
the last one only once it is whole. A file that fails to probe is listed with its error and not
served. `files` and `exclude` choose the files looked at, by pattern, and names starting with `.` are
never looked at. A path out of the roots is refused: a sidecar's path with `..` or from `/`, and a
symbolic link that leads out of its root, unless `follow_symlinks`; links within the root are
followed, and each directory is walked once. What a scan cannot list, as such a link, is in the
catalog's problems.

Every part is bounded: the catalog by `max_items` and `max_catalog_bytes`, a scan by `max_files`
(files and directories, read a few hundred at a time) and 32 directories of depth, a sidecar by
`max_sidecar_bytes`, a subtitle by `max_subtitle_bytes`, the sample tables kept for the files being
served by `max_table_bytes`, and the file pool by `file_workers`, `file_queue` and `file_timeout`.
Nothing touches the disk on a fib worker. A reload changes a library in place. The Admin API's
`GET /media/library`, `GET /media/library/<name>` (a page of its items, with `offset`, `limit`,
`q`, `codec` and `error`) and `GET /media/library/<name>/items/<id>` read the catalogs. See
[decision 032](../decisions/032-media-library.md) and [Media library](../media-library.md).

```json
{
  "listeners": [{"name": "web", "address": ":8080"}],
  "media_library": [
    {
      "name": "films",
      "roots": ["/srv/media/films", "/srv/media/shows"],
      "files": ["*.mp4", "*.m4a", "*.srt", "*.vtt", "*.jpg", "*.json"],
      "exclude": ["**/extras/**"],
      "scan_interval": "30m",
      "max_items": 50000,
      "max_probes": 4
    }
  ],
  "routes": [{"handler": {"module": "respond", "params": {"body": "the library is read by the Admin API for now"}}}]
}
```
