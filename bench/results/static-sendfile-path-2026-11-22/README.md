# Files above 16 KiB: sendfile without ServeContent or a duplicate descriptor

2026-11-22, benchmark host 192.168.100.51 (Ryzen 7 7800X3D, 8 cores / 16 threads), over loopback,
the setup of [sweep-2026-11-21](../sweep-2026-11-21/README.md): server on CPUs 0-3,8-11, wrk on
4-7,12-15, 256 connections, random files of one size, more than fit's caches hold (200,000 files,
or 2 GiB). `cliff.sh`; two rounds of 15 s after 5 s of warm-up, a fresh server for each; medians in
`table.txt` (`cliff-sum.py summary.txt`), every run in `*.out`.

**The cliff.** The sweep found fit at 90.7% of nginx for 16 KiB files and 70.0% for 17 KiB: files
up to 16 KiB are read whole and written from memory, larger ones went through `ServeContent` with
an `*os.File`, which fib's `SendFile` duplicated (`dup`, `fcntl F_SETFD`, a second `close`), and
`NewFile` had set non-blocking (`fcntl` twice). An `strace -c` at 20 KiB counted 11.5 system calls a
request against nginx's 7.1, and `ServeContent` took 10.3% of the CPU.

**The change.** A plain GET or HEAD of a file above 16 KiB, without conditions or a range, now opens
the file raw (`open` only), checks it with one `fstat` against what the lookup found (a regular file
of the same size, else the old path), sets the header from the lookup's validators, and hands the
descriptor to `SendFile`, which sends from it and closes it (fib `cec2122`, `FileHandover`).
Conditional and range requests keep `ServeContent`.

| Size | fit before | fit after | after/before | nginx | after/nginx | p99 before / after / nginx (ms) |
| --- | ---: | ---: | ---: | ---: | ---: | --- |
| 8 KiB | 330,281 | 331,655 | 100.4% | 336,043 | 98.7% | 2.20 / 2.63 / 1.17 |
| 12 KiB | 314,523 | 315,883 | 100.4% | 332,934 | 94.9% | 3.15 / 2.67 / 1.22 |
| 16 KiB | 302,437 | 304,265 | 100.6% | 332,708 | 91.5% | 3.08 / 2.89 / 1.27 |
| **17 KiB** | 231,751 | **267,914** | **115.6%** | 334,637 | 80.1% | 2.88 / 2.55 / 1.20 |
| 20 KiB | 230,911 | 269,010 | 116.5% | 330,054 | 81.5% | 2.89 / 2.58 / 1.61 |
| 32 KiB | 228,677 | 263,315 | 115.1% | 317,990 | 82.8% | 2.91 / 2.87 / 2.27 |
| 64 KiB | 202,537 | 228,446 | 112.8% | 242,961 | 94.0% | 3.34 / 3.13 / 1.33 |
| 128 KiB | 150,108 | 150,821 | 100.5% | 154,595 | 97.6% | 3.39 / 3.08 / 1.71 |

"before" is fit `e1611dbb` (`fit-sweep`), "after" this change (`fit-cliff`). Rounds agree within 1%.
Up to 16 KiB the read-whole path is unchanged; at 128 KiB both are bound by copying, not calls.

**The threshold.** `fit-cliff4k` reads only up to 4 KiB whole and sends the rest by the new path:
8-16 KiB lose 11-15% (281k against 332k at 8 KiB), so files up to 16 KiB stay read whole. Above 16
KiB the two are the same build.

**Left.** 17-32 KiB are still 17-20% below nginx, at the same CPU (7.85 of 8 against 7.9): about 29
µs a request against nginx's 24. The read-whole path does 16 KiB in 26 µs, so the sendfile path
costs some 3-4 µs more than reading and copying the same bytes.

**Tried and dropped: the header in one segment with the file** (`cork/`, `cliff2.sh`). fib sent the
header in a write of its own ahead of the sendfile, so every reply took two TCP segments; a build
that corked the connection over the header and the file, and sent the header with `MSG_MORE`
(`fit-cliff2`), was 1.5-1.9% slower in both rounds at 17 and 20 KiB, and level at 8-16 KiB. The run
was stopped after 20 KiB. The separate segment is not the cost; fib keeps sending the header as it
did.
