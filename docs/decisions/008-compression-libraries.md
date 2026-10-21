# 008: zstd and gzip from klauspost/compress, brotli from andybalholm/brotli

[English](008-compression-libraries.md) | [简体中文](008-compression-libraries.zh-CN.md)

Status: accepted, in I46 (2026-10-21).

## Context

I46 compresses responses with zstd, brotli and gzip. The standard library has gzip and flate but
neither zstd nor brotli, and fit compresses on fib's workers, as a body is written, so each
encoder's speed and memory count for every compressed response. The candidates, at their versions
of 2026-10-21:

| | What it is | What it brings into the build |
| --- | --- | --- |
| `github.com/klauspost/compress` v1.20.3 | pure Go zstd, and gzip and flate faster than the standard library's, with the same API | nothing beyond the standard library |
| `github.com/andybalholm/brotli` v1.2.6 | a pure Go translation of Google's C brotli encoder and decoder | nothing beyond the standard library |
| `github.com/google/brotli/go/cbrotli`, `github.com/DataDog/zstd` | bindings to the C libraries | cgo, which fit's builds do not use |
| `compress/gzip` | the standard library's | nothing |

## Decision

- **zstd and gzip come from `klauspost/compress`**, whose encoders stream, flush and reset, and
  whose gzip is a drop-in for the standard library's and faster. zstd runs with
  `WithEncoderConcurrency(1)`, so it encodes on the caller's goroutine and starts none of its own,
  with a 256 KB window and `WithLowerEncoderMem`.
- **brotli comes from `andybalholm/brotli`**, the pure Go one, at qualities 1, 4 or 6 and a 256 KB
  window (`LGWin` 18), whose encoders flush and reset the same way.
- No cgo: fit builds for every target fib does with the Go toolchain alone.

## Consequences

- Two modules more, neither with requirements of its own.
- Each response being compressed holds an encoder of a few hundred kilobytes, recycled through a
  pool when the response ends; `compress`'s `max_in_flight` (1024 by default) bounds how many are
  in use, past which responses go uncompressed.
- On the benchmark host, a 16 KB JSON body sent whole costs 6.3 µs with zstd, 9.0 µs with gzip and
  16.6 µs with brotli at the default levels (10.6 µs at the fastest), against 2.3 µs uncompressed
  (`BenchmarkCompress`).
