# Logs

[English](11-logs.md) | [简体中文](11-logs.zh-CN.md)

## What it does

The **Logs** page is about files: the files fit's own log and the access log write, what each rotates and
keeps, which files are on disk now with their sizes and times, and two actions a person runs now —
**Rotate now** and **Clean up now**.

The settings themselves are edited in the forms at the bottom of the page: the forms of `log` and
`access_log` (with `time_zone` and `rotate`), over the config's shared draft, with the same save bar and raw
text tabs as any other page. `rotate`, `time_zone` and the levels take effect on reload; `output` and `format`
take a restart, of `log` and of `access_log` alike.

A file is written by its own writer goroutine: when the disk cannot keep up, the lines past the buffer are
dropped and counted, so a slow disk never holds up the work that logged them. Rotation, compression and the
removal of old files happen on that goroutine and on fit's file pool. One path may be both fit's own log's
file and the access log's: that is one sink, sharing one `rotate` and one `time_zone`.

## Pages

### Logs

The line at the top says it: these are the files fit's own log and the access log write, their rotation and
retention; a file output with no `rotate` grows without bound, as it did before rotation, and fit says so at
startup. When no log writes to a file, the page says so and points at the forms below for `log.output` or
`access_log.output`.

Then one card per file output (what `GET /logs` reads):

- The card is titled **Access log** when the access log is the only logger writing there, and **fit's own log**
  otherwise; its first line is the path, a badge for each logger that writes there, the format badge and the time
  zone badge. The format shows as mixed when the loggers writing one file do not share it.
- One line says what this output's rotation does: rotates at `max_bytes`, every `interval`, keeps `max_age`,
  at most `max_files` files, at most `max_total_bytes`, gzip. With no `rotate` it is a warning that this file
  grows without bound.
- Dropped lines and the last write failure, when there are any, are shown beside it.
- A table of the files: **File**, **Size**, **Modified**, with a **current** badge on the one being written. A
  rotated file is named after the time of the rotation in UTC, as in `fit.log.2026-11-05T04-00-00.000` (`-1`,
  `-2` and so on when that name is taken), and gets `.gz` when `compress` is on. Under the table is the line
  "N files, size".
- Two buttons: **Rotate now** and **Clean up now**.

**Rotate now** turns that file into a new one at once (`POST /logs/rotate`, body `{"path"}`): the queue is
written out first, then the rotation runs on the writer's goroutine. Only an output that has a `rotate`
actually rotates; one without it does nothing.

**Clean up now** previews first: `POST /logs/cleanup` with `dry_run` lists the files past its retention, and
only when there are any does it confirm "Remove N rotated files (size)?" and then send the same request without
`dry_run`. A cleanup removes only the files the rotation made, never the file being written; the oldest past
`max_age`, `max_files` or `max_total_bytes` go.

Only the paths the running config writes can be listed, rotated or cleaned: no other file is shown here, and
none is removed.

Below the cards is **Settings**: the forms of `log` and `access_log`, with the `.conf`, `.yaml` and `.json`
tabs and the shared save bar like any other page. The endpoints this page uses (the Admin API's endpoints sit
under `/api/m/admin/` on the console):

| endpoint | |
| --- | --- |
| `GET /logs` | every file output: its path, loggers, format, time zone, rotation and the files on disk |
| `POST /logs/rotate` | rotate one output now: `{"path"}` |
| `POST /logs/cleanup` | remove what retention says, or report it first: `{"path", "dry_run"}` |

## Key parameters

The fields of `rotate` (the same set at `log.rotate`, `log.loggers.*.rotate` and `access_log.rotate`):

| field | default | description |
| --- | --- | --- |
| `max_bytes` | 1 GiB | How large a file may grow before it is rotated: a write that would pass this starts a new file; 0 does not rotate by size |
| `interval` | off | How long a file is written before it is rotated, at the next period boundary of the log's `time_zone`: `24h` turns at midnight, `1h` on the hour; a fraction of a second, or a period over 24h that is not a whole number of days, is refused |
| `max_age` | `720h` (30 days) | How long a rotated file is kept after it was last written; 0 keeps it until a count or size limit removes it |
| `max_files` | none | The most rotated files kept, newest first |
| `max_total_bytes` | 1 GiB | The most the rotated files may total, the oldest removed until they fit |
| `compress` | `true` | Gzip a file when it is rotated |

The two fields of the forms that decide what this page shows:

| field | default | description |
| --- | --- | --- |
| `time_zone` | `local` | The zone the line's time is written in and the zone the `interval` turns at: `local`, `utc` or an IANA name such as `Asia/Shanghai`; takes effect on reload |
| `output` | `log`: none (debug and info to stdout, warn and error to stderr); `access_log`: `stdout` | Only an output that names a file path appears on this page; `log`'s `output` takes a restart |

Full tables: [log](../reference/config.md#log), [access_log](../reference/config.md#access-log) and
[log.rotate](../reference/config.md#config-rotate).

## In .conf

A complete config that passes `fit validate`: `log` and `access_log` both write files and both have a
`rotate`, with a minimal listener and route.

```fit
schema_version 1

log {
	level info
	output /var/log/fit/fit.log
	format line
	time_zone Asia/Shanghai
	rotate {
		max_bytes 100MiB
		interval 24h
		max_age 720h
		max_files 30
		max_total_bytes 1GiB
		compress true
	}
	loggers {
		engine {
			level warn
			output /var/log/fit/engine.log
			format json
			rotate { max_bytes 50MiB; max_total_bytes 500MiB }
		}
	}
}

access_log {
	output /var/log/fit/access.log
	format json
	level info
	time_zone Asia/Shanghai
	rotate {
		max_bytes 1GiB
		interval 24h
		max_age 720h
		max_total_bytes 5GiB
		compress true
	}
}

listener web :8080

route { handler respond { body "hello from fit" } }
```

## Common cases

- **A file is growing without bound**: that is what the card's warning says. Set a `rotate`: by size
  (`max_bytes`) or by time (`interval`), then the retention (`max_age`, `max_files`, `max_total_bytes`). After
  Save and apply it takes effect on reload.
- **Keep 30 days only**: `max_age 720h` is the default, and with `interval 24h` that is one file a day kept for
  30 of them.
- **Cap the bytes only**: `max_total_bytes` removes the oldest until the total fits; `max_files` removes by
  count.
- **Free space now**: **Clean up now** previews first (`dry_run`), the confirmation window lists the files and
  their total size, and only after you confirm are they removed. The file being written is never removed.
- **Change where the file is**: set `output` and restart; the old path is no longer listed on this page.

Log files are created 0640 (fit's user writes them, its group may read), and a rotated file keeps the mode of
the file it came from; a rotation that fails is tried again a minute later, and retention is checked every
10 minutes.

## See also

- [The config reference](../reference/config.md): [Log](../reference/config.md#log) and
  [Access log](../reference/config.md#access-log).
- [The console and the store](../console.md#the-pages): where the Logs page sits in the menu.
- [Global settings](07-global-settings.md): the rest of `log` and `access_log`.
- The [templates](../../templates/README.md):
  [`30-observability.conf`](../../templates/conf/conf.d/30-observability.conf).
- Neighbouring chapters: [the function navigation bar](03-navigation.md),
  [Consistency check](10-consistency-check.md), [Cluster](12-cluster.md) (the logs across members).
