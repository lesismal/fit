# The rest of the 16-64 KiB gap: open and fstat, the user-space cost, inline_disk

2026-11-22, benchmark host 192.168.100.51 (Ryzen 7 7800X3D, 8 cores / 16 threads), over loopback,
the setup of [static-default-type-2026-11-22](../static-default-type-2026-11-22/README.md):
server on CPUs 0-3,8-11, wrk on 4-7,12-15, 256 connections, random files of one size, more than
fit's caches hold, `default_type application/octet-stream` in fit as in nginx's config. Three
rounds of 15 s after 5 s of warm-up, a fresh server for each, unless said otherwise; medians in
each directory's `table.txt` (`cliff-sum.py summary.txt ...`), every run in `*.out`.

## Where the time went (`prof3.sh`, `prof/`)

After I233, at 20 KiB, per request at 8 CPUs:

| | fit (I233) | nginx |
| --- | ---: | ---: |
| Requests a second | 287,000 | 335,000 |
| CPU a request | 27.9 µs | 23.6 µs |
| of which in the kernel | 19.5 µs (70%) | 20.5 µs (87%) |
| of which in user space | 8.1 µs | 3.0 µs |
| System calls a request (`perf stat`) | 7.33 | 7.37 |

So the kernel was not the gap: fit spends less there than nginx, which makes two `setsockopt` a
request for `tcp_nopush`. The gap was user space, 5 µs a request, and one system call fit made that
nginx does not: an `lstat` on every lookup the cache does not hold (0.82 a request; the benchmark's
104,000 files are more than the 16,384 lookups it keeps), a path walk before the `open` walks it
again.

## What was done

1. **Open and fstat in place of lstat** (`open/`): a lookup the cache does not hold opens the file
   (`O_NOFOLLOW|O_NONBLOCK`) and stats it through the descriptor, as nginx does, and the request
   reads or sends from that descriptor: open, fstat, close, where it was lstat, open, fstat,
   close. A symbolic link, a permission it lacks to read, or another error falls back to the
   lstat as before; a named pipe or a device is closed at once. Linux only.

   | Size | I233 | open+fstat | Change | nginx | of nginx |
   | --- | ---: | ---: | ---: | ---: | ---: |
   | 8 KiB | 334,560 | 346,860 | +3.7% | 332,624 | 104.3% |
   | 16 KiB | 309,967 | 319,092 | +2.9% | 338,032 | 94.4% |
   | 20 KiB | 286,221 | 302,384 | +5.6% | 335,233 | 90.2% |
   | 32 KiB | 282,465 | 295,147 | +4.5% | 329,855 | 89.5% |

2. **User space** (`micro/`, against 1): an allocation profile (`alloc20.sh`,
   `prof/fit-open.allocs.pprof`) found 25.7 allocations and 1,767 bytes a request, 68 GCs in 10 s.
   - fib `cc74d2c`: SendFile on HTTP/1, which builds and sends the header before it returns, sends
     the handler's header map as it is, without WriteHeader's copy (435 bytes, 1.75% of the CPU).
   - The header sendWhole sets: Content-Length, Last-Modified and Etag written into one string,
     the four values in one array: two allocations where there were seven; fib then sets no
     Content-Length of its own.
   - `appendHTTPTime`: Last-Modified written field by field, 16 ns against AppendFormat's 69.
   - `joinRoot`: the root and a request path already clean joined without cleaning both again.

   | Size | open+fstat | + user space | Change |
   | --- | ---: | ---: | ---: |
   | 16 KiB | 318,569 | 322,769 | +1.3% |
   | 20 KiB | 300,826 | 312,954 | +4.0% |
   | 32 KiB | 285,496 | 290,513 | +1.8% |

3. **No timer a request** (`exp9/` measured the ceiling, 2 rounds at 20 KiB: +1.8%, p99 the same):
   a request queued for the file pool joined a `time.AfterFunc` of the pool's timeout, stopped once
   the pool's work claimed it. It now joins a list, sharded under eight locks, and a sweep every
   eighth of the timeout, scheduled only while requests wait, answers 503 to those past it: within
   the timeout and an eighth of it, where the timer was exact.

**All three** (`all/`, `fit-all`; `fit-micro` is 1 and 2):

| Size | I233 | fit-micro | fit-all | Change | nginx | of nginx |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 8 KiB | 334,529 | 351,660 | 356,584 | +6.6% | 331,948 | 107.4% |
| 16 KiB | 309,033 | 324,268 | 326,659 | +5.7% | 330,349 | 98.9% |
| 20 KiB | 285,949 | 314,361 | 317,507 | **+11.0%** | 335,182 | 94.7% |
| 32 KiB | 282,160 | 305,765 | 308,746 | **+9.4%** | 328,645 | 93.9% |
| 64 KiB | 217,668 | 225,642 | 226,350 | +4.0% | 241,906 | 93.6% |

p99 is level with I233's at every size (2.1-4.0 ms; nginx's 1.1-3.6). System calls a request at
20 KiB: 6.53, nginx 7.36.

4. **`inline_disk`, opt-in** (`inl/`, 2 rounds, a slower hour of the host, nginx's numbers too):
   the static handler looks at the disk on fib's worker that serves the request, as nginx does in
   its event loop, rather than on the file pool. The hand-over to the pool, a goroutine woken and
   the event loops parked more often (0.6 more `epoll_pwait`/`epoll_ctl` a request), was what
   was left of the 20 KiB gap.

   | Size | fit-all | inline_disk | Change | nginx | of nginx |
   | --- | ---: | ---: | ---: | ---: | ---: |
   | 20 KiB | 313,271 | 325,764 | +4.0% | 331,272 | 98.3% |
   | 32 KiB | 294,295 | 299,604 | +1.8% | 309,371 | 96.8% |

   p99 a little worse (3.50 ms against 3.12 at 20 KiB). Off by default: a disk that hangs would
   then hold fib's workers, and nothing answers 503 for it.

## Tried and not done

- **The disk work on fib's worker by default**: what `inline_disk` does; see above.
- The fib header-block API of I233 was not tried again: with WriteHeader's copy gone, the map
  costs fit two allocations and fib's iteration and sort of five fields (1.9% of the CPU).

## What is left

At 20-32 KiB, 5-6% below nginx by default, 2-3% with `inline_disk`; 16 KiB 1%. The rest is spread
thin: lookups in the caches' maps (1.7%), fib's header serialization (1.9%), allocation (1.8%),
and SendFile's completion on another goroutine, which fib's contract asks for.

## Files

- `prof3.sh` (`fit-prof3.conf`): syscalls by name (bpftrace, whose counts came out inflated and
  were not used), perf and a Go profile, fit I233 and nginx; `prof/fit-final.*`, `prof/nginx.*`.
  `prof4.sh`: the same for fit-all, `prof/fit-all.cpu.pprof`.
- `open.sh`, `micro.sh`, `all.sh`, `exp9.sh`, `inl.sh`: the A/Bs; the first round at 20 KiB also
  counts system calls by name with `perf stat` (`*.syscalls.csv`, the `sys` lines of `summary.txt`).
- `alloc20.sh`: the allocation profile, `prof/fit-open.allocs.pprof`.
