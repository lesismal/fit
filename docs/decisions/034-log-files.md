# 034: Log files, rotation and retention, and the logs' time zone

[English](034-log-files.md) | [简体中文](034-log-files.zh-CN.md)

Status: accepted (2026-11-05). Builds on [decision 027](027-console-and-store.md) (the workbench)
and the log formats of I82–I83.

## Context

fit's own log and the access log can both write to a file, and until now a file was opened once
and appended to for as long as fit runs: nothing rotated it, nothing removed it, and changing its
path or format took a restart. A file therefore grew until the disk filled, and operators rotated
it outside fit with a restart or a signal. The logs also disagreed on time: fib's `line` format,
which fit's own log writes, used local time, while the access log's `json` and `text` wrote UTC,
and only its `line` format was local; nothing let a user choose.

Users asked for file logs rotated by time and size, cleaned up past a maximum age and a maximum
size, configurable in the workbench, and for every log's time to be local by default while a user
may choose UTC or another zone. The iterations are I155 (fit's own log's files), I156 (the access
log's files, through the same sink) and I157 (the workbench's Logs page).

## Decisions

- **A file sink of fit's own, not a third-party roller.** Package `logs` grows a bounded file
  sink per output path: a queue of 1 MiB, a writer goroutine of its own, and rotation, retention
  and compression on that goroutine and on `internal/filepool`. A write copies into a pooled
  buffer and returns; a full queue refuses the write and counts it, so that no logger, handler or
  fib worker waits for the disk (rule 1), and a slow disk costs memory up to the queue and no more
  (rule 11). The standard library is enough (`compress/gzip`, `time/tzdata`), so no dependency is
  added and no decision about a library is needed (rule 14).
- **One sink per path, shared by the two logs.** `logs.Files` is made by `fit run` and handed to
  `logs.New` and the access log: `log` and `access_log` naming the same path write through one
  sink. Config validation refuses two different `rotate` settings, or two different `time_zone`s,
  for one path: a file turns at one boundary and has one policy.
- **`rotate` is an object that turns rotation on.** `log.rotate`, `log.loggers.<name>.rotate` and
  `access_log.rotate` hold `max_bytes` (1 GiB), `interval` (off), `max_age` (720h), `max_files`
  (off), `max_total_bytes` (1 GiB) and `compress` (on). A field left out takes its default and 0
  turns that limit off, so the fields are pointers: an absent `rotate` keeps today's single file,
  which fit says at startup grows without bound; `"rotate": {}` is a bounded policy. A file's
  settings are its logger's own when the logger writes to a file of its own, and the log's
  otherwise.
- **Rotation and naming.** A write that would take the file past `max_bytes` starts a new file
  before it, and so does the first write in a period later than the file's, whichever comes first;
  a line larger than `max_bytes` gets a file of its own. A period is a whole number of intervals
  from local midnight of the log's zone, so `24h` turns at midnight and `1h` on the hour; config
  validation refuses an interval under a second, and one over 24h that is not a whole number of
  days. A rotated file is named `<path>.<yyyy-mm-ddThh-mm-ss.mmm>`, the rotation's time in UTC, a
  `-1`, `-2` and so on when the name is taken, and `.gz` when compressed. Naming in UTC keeps
  names sortable whatever zone the log writes in; the lines inside carry the log's zone.
- **Retention.** A sweep at open, after every rotation and every ten minutes removes, oldest
  first, the rotated files older than `max_age`, then those past `max_files`, then those the total
  of `max_total_bytes` no longer fits. It removes only names the rotation made from that path, and
  never the open file. Compression is gzip to a temporary file and a rename, on the file pool; a
  failure keeps the plain file, and a sweep skips a file being compressed.
- **`time_zone`: local by default, UTC or an IANA zone.** `log.time_zone` and
  `access_log.time_zone` name `local` (left out), `utc`, or an IANA zone such as `Asia/Shanghai`.
  The zone is where every format of the log writes its times and where its files' intervals turn,
  so `24h` is local midnight by default and UTC midnight with `utc`. Fit embeds `time/tzdata` so
  an IANA name works in a container without zoneinfo; that is a standard-library package, not a
  dependency (rule 14). The access log's `json` and `text` lines, which were UTC, now write the
  setting too, local by default: a deliberate change, as asked, and its `line` format was already
  local.
- **What a reload takes up.** `time_zone`, `rotate` and `compress` change on reload, in place:
  the sink keeps the file it has and the handler writes each record in the new zone (one
  conversion a message, or a second for the access log's cached stamp, never a line). A file's
  path and a logger's format still take a restart, as before, and the workbench and the
  consistency check say so; the consistency check does not report the settings that now apply
  live. The access log's per-second time stamp cache stays, so a chosen zone costs one conversion
  a second, not one a line.
- **Nothing gets slower.** The work added is on the writer's goroutine and its pool; the access
  log's line is still formatted where it is today, allocation-free, and the request path takes no
  new lock, syscall or allocation. The iteration's done-when requires the packages' benchmarks,
  before and after with benchstat on the benchmark host, to show no regression (rule 21).

## Rejected

- **lumberjack or another rolling writer.** A dependency for what the standard library does, with
  its own queueing and naming rules to wrap, and no time-boundary rotation in the shape fit's logs
  need.
- **Rotating by an interval measured from when the file opened.** The boundaries would drift with
  the process's start time; midnight and the top of the hour are what operators read logs by.
- **One time zone for every log, fixed to UTC.** Users asked for local by default and a choice;
  the zone that writes a line's time is the zone its file's day turns in, or the two could
  disagree.
- **A global log-file path for rotation settings.** The settings belong to the output they
  rotate, so that a logger's own file has its own policy, and a shared path has one.
