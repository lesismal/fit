# 003: The static handler keeps its own file cache

[English](003-static-file-cache.md) | [简体中文](003-static-file-cache.zh-CN.md)

Status: accepted, in I12 (2026-10-19).

## Context

The plan built the `static` handler on fib's `http.FileCache`. In fib v1.2.0, the version fit
uses, `FileCache.ServeFile` answers the whole request itself: it sends no `ETag`, knows nothing of
index files or directories, writes its own 404 for anything it cannot serve, and keeps the
headers of a cached file to itself. The handler needs all of those: `ETag` and index files are in
I12, and I13's `try_files` fallbacks and directory listing need to know that a file is missing
before anything is written.

## Decision

- `handler/static` keeps its own cache of small files, after `FileCache`'s design: a map from path
  to contents and a header built once, shared by every response the file goes out in, with limits
  on the size of one file and on the total. A file past either limit is served from disk through
  `net/http.ServeContent`.
- Each request stats the file, one system call, and the cached copy stands while its size and
  modification time do. That is what nginx does by default (`open_file_cache` is off), so it does
  not put fit below nginx's bar; fib's inotify watch, which saves the stat on Linux, is internal to
  fib.
- The `ETag` is the modification time in nanoseconds and the size, in hex, after nginx's.

## Consequences

- A request costs a stat that `FileCache` saves on Linux. If I13's or I23's benchmarks show it
  matters, the fix belongs in fib: a `FileCache` that exposes a lookup (contents, header, missing
  or directory) without answering the request, and adds an `ETag`. fit would then move onto it
  with a fib release, not a `replace`.
- Precompressed twins and sendfile (I13) are fit's to build as well.
