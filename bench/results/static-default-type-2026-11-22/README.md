# What is left of the 17-64 KiB gap, and default_type

2026-11-22, benchmark host 192.168.100.51 (Ryzen 7 7800X3D, 8 cores / 16 threads), over loopback,
the setup of [static-sendfile-path-2026-11-22](../static-sendfile-path-2026-11-22/README.md):
server on CPUs 0-3,8-11, wrk on 4-7,12-15, 256 connections, random files of one size, more than
fit's caches hold. Two or three rounds of 15 s after 5 s of warm-up, a fresh server for each;
medians in `table.txt` (`cliff-sum.py summary.txt`), every run in `*.out`.

## The profile

After I232, 20 KiB files went at 80% of nginx's rate at the same CPU: 29.7 µs a request against
24. `prof20k2.sh` (results in `prof/`) took a Go CPU profile and counted system calls at 20 KiB,
and at 16 KiB, read whole, beside it:

- System calls are 69% of the CPU at 20 KiB, 74% at 16 KiB: on this kernel each costs about a
  microsecond (`entry_SYSRETQ_unsafe_stack`, the SRSO mitigation, is the top symbol). 20 KiB
  makes 8.06 a request, nginx 7.13. The header's `sendto` and the `sendfile` each go once through
  TCP: lo's MTU is 1500 here, so a 20 KiB reply is 17 segments, nginx's too.
- fit makes three nginx does not: a `pread` of 512 bytes to sniff the type of a file whose name
  has no extension, as the benchmark's have (nginx answers with its `default_type`); an `fstat`
  after the open (nginx does one too, but needs no `lstat`, from its open_file_cache); and an
  `epoll_ctl` for a quarter of the requests, a loop parking.
- In user space, the header map (`Header.Clone`, map assignments, formatting) is about 4%, and
  the hop of SendFile's completion to another goroutine about 2%.

## The ceiling (`expab.sh`, `expab/`)

One build, `fit-exp`, with each candidate switched on by the environment, at 20 KiB:

| Variant | QPS (2 rounds) | Change | Server CPUs |
| --- | ---: | ---: | --- |
| base (fit main) | 269,241 | | 7.86 |
| no sniff | 287,640 | +6.8% | 7.84 |
| no fstat | 275,505 | +2.3% | 7.82 |
| header as a block (unchecked) | 275,322 | +2.3% | 7.84 |
| completion on the same goroutine | 267,163 | −0.8% | 7.72 |
| all four | 304,150 | +13.0% | 7.69 |

## What was done

**`default_type`** for the static handler, as nginx's: the type of a file whose extension gives
none, unsniffed. Left out, files are sniffed as before. It types files on every path, in memory,
read whole, by SendFile and through ServeContent.

`dtab.sh` (`table.txt`): fit main (`fit-cliff`), the change without `default_type` (`fit-dt`, which
also sent the header as a checked block through a fib API tried here) and with
`default_type application/octet-stream` (`fit-dt-dt`), as nginx's config has:

| Size | fit main | fit-dt | fit-dt with default_type | nginx | default_type gain | of nginx |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 8 KiB | 332,882 | 329,907 | 335,026 | 336,609 | +0.6% | 99.5% |
| 16 KiB | 304,736 | 302,676 | 307,401 | 333,540 | +0.9% | 92.2% |
| 20 KiB | 268,921 | 267,689 | 285,292 | 332,827 | **+6.1%** | 85.7% |
| 32 KiB | 262,355 | 262,917 | 276,184 | 316,490 | +5.3% | 87.3% |
| 64 KiB | 218,871 | 218,089 | 225,812 | 242,334 | +3.2% | 93.2% |

**As committed** (`final.sh`, `final/`, three rounds): fit main and the change, both on fib main
(`fit-iso`, `fit-final`), and the change with `default_type`:

| Size | fit main | the change | with default_type | default_type gain |
| --- | ---: | ---: | ---: | ---: |
| 8 KiB | 333,682 | 331,892 (−0.5%) | 338,066 | +1.3% |
| 20 KiB | 270,900 | 271,062 (+0.1%) | 288,384 | **+6.5%** |

Without `default_type` the change is level: −0.5% at 8 KiB is about what rounds of one build vary
by (0.4% here).

## Tried and dropped

- **The header as a block** (a fib `Context.AddHeaderBlock`, checked and copied, written by HTTP/1
  as it is): fit-dt above, and `fit-dt2` in `iso/` (built on the stack, checked without a copy):
  +0.1% at 20 KiB against the same code on the same fib (`fit-iso`), where the unchecked hack
  gave +2.3%. Not worth an API; fib does not have it.
- **No fstat** (+2.3%): it keeps a file replaced between the lookup and the open, or a directory,
  from going out with the lookup's length and validators. Kept.
- **Completion on the same goroutine**: SendFile's done runs on another goroutine by contract;
  the CPU it saved bought no throughput.
- **The header in the file's first segment** (I232, `MSG_MORE`): `segs.sh` counted 17.97 → 17.00
  segments a request, and it was slower.

## Notes

`iso/`: fit main on fib main (`fit-iso`, with fib's de24a5f, the Upgrade race fix) is 0.3-0.6%
below the same on fib cec2122 (`fit-cliff`) at 8 KiB, within what rounds vary by.
