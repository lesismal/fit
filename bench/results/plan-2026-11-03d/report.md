# Load test plan, 2026-11-03 (fourth run): the report

[English](report.md) | [简体中文](report.zh-CN.md)

The plan of [the third 2026-11-03 run](../plan-2026-11-03c/report.md) run again with fit at 8e4cd91 on fib
36acb0b: one change, the HTTP/2 server's answers queued past a connection lock another holds. 232 runs on
the benchmark host, 2026-11-03 11:34 to 13:07 UTC (19:34 to 21:07 in the host's Asia/Hong_Kong): none
failed, none hung, no run had a single error. Tables in [tables.md](tables.md), the plan in
[plan.txt](plan.txt), the log in [plan.log](plan.log); the host as before, `strata.service` stopped for
the run. fit was built against fib 36acb0b before it was pushed; its go.mod requires fib c492d7e, which
adds a test fix, from ca97f13.

## In short

- **fit now leads nginx in the API gateway over h2.** W1 over h2 +14.4% at 500 in flight (269,405 a
  second, 22.13 µs a request from 25.34), +9.6% at 2,000, +8.8% at 100, +4.8% at 5,000, +2.3% at 10,000:
  106%, 104%, 101% and 100% of nginx at 500 to 10,000 (91-99% before), 96% at 100. W5, the same with
  40,000 idle TLS connections, +15.3% over h2 (266,649), **109% of nginx** at a third of its memory.
- **Plain responses over h2c and h2 +19% at 500 in flight** (3.05M and 2.89M a second, 6.0 and 4.0 times
  nginx) and +14% to +16% at 100; from 2,000 up the load generators were the limit before and are still.
- **Less memory:** fit's median peak memory -4.9% against the third run; W1 over h2 at 500 in flight 47
  MiB from 67, at 5,000 189 from 246, at 10,000 296 from 388.
- **Nothing slower.** The one run of fit's past -3%, proxied uploads over HTTP/1.1 (-3.9%), is +1.0% in
  a 5-round A/B (627.6 against 624.8 µs a request); an HTTP/2 change cannot reach it. The other servers'
  medians moved +0.4% to +1.1%.
- **GOMAXPROCS one below the CPUs: measured, not taken.** At full load fit served 3-20% fewer requests a
  second with 5 Ps than with 6 (W1 and W5 -9% to -10%, h2c at 5,000 -19.5%), though CPU a request fell
  7-11%; at 200,000 a second the maximum latency fell 20-25% and the h2 p99 rose (4.82 → 5.29 ms). The
  `GOMAXPROCS` environment variable already sets it for whoever wants that trade.

## The change: HTTP/2 answers past a taken lock

At W1 over h2 with 5,000 in flight a mutex profile of fit showed 1,202 goroutine-seconds in 10 of waiting
in `h2ServerStream.respond` on the HTTP/2 server connection's lock, which it held for about 3 µs of
work: HPACK, framing, window accounting and handing the frames to the connection. The work was under 1%
of CPU; the waiting was convoying on a saturated scheduler, where `sync.Mutex` parks rather than spins,
and the goroutines parked were the backend connections' read loops, each stalling its connection.

fib 36acb0b has a response that finds the lock taken (`TryLock` fails) pushed onto a lock-free queue of
the connection's and left there, its header flattened and a body it does not own copied; whoever holds
the lock sends the queued answers, in the order they came, before letting it go, and takes it again for
any queued while it did. HPACK encoding stays under the lock, so header blocks leave in the encoder's
order; windows are charged under it as before; a reset stream or a closed connection is met when the
answer is sent. At most one answer a stream is queued. A queued response returns no error of a stream
reset or a connection closed meanwhile, which only one that is not queued reports.

| W1 api h2, `bench/ab.sh` 3 rounds | req/s | µs a request | peak MiB | lock wait (goroutine-s / 10 s) |
| --- | --- | --- | --- | --- |
| 500 in flight | 235k → 266k (+13.6%) | 25.38 → 22.34 | 68 → 46 | 467 → 57.5 |
| 5,000 in flight | 227k → 240k (+5.7%) | 26.22 → 24.77 | 245 → 191 | 1,258 → 256 |

What waits now is mostly fib's HTTP client's per-host lock in `enqueue` and the connection's own lock in
`Cork`. Skipping `Cork` while a flush was pending was tried and lost 5% (sends found the connection
uncorked and sealed TLS a response at a time).

## Against the third 2026-11-03 run, server by server

| Server | Runs | Median throughput | Median CPU a request | Median peak memory | > +3% | < -3% |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| fit | 58 | +0.7% | -0.9% | -4.9% | 10 | 1 |
| nginx | 58 | +1.1% | -0.9% | +0.0% | 8 | 2 |
| Caddy | 52 | +0.6% | -0.7% | -0.7% | 0 | 0 |
| Envoy | 44 | +0.6% | -0.4% | +0.0% | 9 | 0 |
| Traefik | 20 | +0.4% | -0.6% | +0.4% | 0 | 0 |

## fit, where it changed

| Run | Scenario | Protocol | 11-03c | 11-03d | Change | µs a request | Peak MiB |
| --- | --- | --- | --: | --: | --: | --: | --: |
| W0 100 | fixed | h2c | 1,502,528 | 1,741,993 | +15.9% | 3.83→3.16 | 13→11 |
| W0 100 | fixed | h2 | 1,352,625 | 1,543,150 | +14.1% | 4.17→3.34 | 13→10 |
| W0 500 | fixed | h2c | 2,562,452 | 3,047,675 | +18.9% | 2.33→1.96 | 20→13 |
| W0 500 | fixed | h2 | 2,421,567 | 2,888,607 | +19.3% | 2.47→2.06 | 18→12 |
| W1 100 | api | h2 | 215,597 | 234,533 | +8.8% | 27.63→25.35 | 24→21 |
| W1 500 | api | h2 | 235,399 | 269,405 | +14.4% | 25.34→22.13 | 67→47 |
| W1 2,000 | api | h2 | 237,400 | 260,173 | +9.6% | 25.00→22.94 | 138→126 |
| W1 5,000 | api | h2 | 228,134 | 239,157 | +4.8% | 26.16→24.93 | 246→189 |
| W1 10,000 | api | h2 | 205,094 | 209,787 | +2.3% | 29.02→28.40 | 388→296 |
| W5 500 | api + idle | h2 | 231,208 | 266,649 | +15.3% | 25.78→22.36 | 454→433 |
| W4 100 | download | HTTP/1.1 | 18,041 | 18,899 | +4.8% | 139.78→133.04 | 12→12 |
| W4 100 | proxy-upload | HTTP/1.1 | 9,601 | 9,230 | -3.9% (A/B +1.0%) | 607.24→644.22 | 137→119 |

## GOMAXPROCS one below the CPUs

`bench/ab.sh`, fit 8e4cd91 with 6 Ps (A) and with 5 (B, by `runtime.GOMAXPROCS` at start), 3 rounds (W6 5):

| Workload | Protocol | req/s A → B | µs a request A → B |
| --- | --- | --- | --- |
| W0 fixed 500 | HTTP/1.1, h2c, HTTP/3 | -3.1%, -6.3%, -0.7% | 8.63 → 7.47, 2.33 → 2.07, 2.22 → 2.02 |
| W0 fixed 5,000 | HTTP/1.1, h2c, HTTP/3 | -0.4%, **-19.5%**, -0.7% | 8.32 → 7.50, 1.75 → 1.81, 2.16 → 1.95 |
| W1 api 500 | HTTP/1.1, h2 | -9.1%, -8.8% | 22.43 → 20.60, 25.37 → 23.18 |
| W1 api 5,000 | HTTP/1.1, h2 | -9.7%, -9.1% | 26.95 → 24.91, 26.27 → 24.13 |
| W2 site 500 | HTTPS/1.1, h2 | -7.8%, -6.8% | 56.59 → 52.18, 51.81 → 46.32 |
| W5 500 | HTTP/1.1, h2 | -10.2%, -9.7% | 22.43 → 20.82, 25.62 → 23.65 |
| W6 200,000 | HTTP/1.1, h2 | rate | 23.20 → 21.59, 25.89 → 23.65; p99 4.99 → 5.04, 4.82 → 5.29 ms; max 15.1 → 11.6, 17.4 → 13.4 ms |
| W6 30,000 | HTTP/1.1, h2 | rate | 27.67 → 26.88, 32.75 → 29.73; p99 2.74 → 2.40, 1.41 → 1.45 ms |

The 6th P is a sixth of the server's CPUs; giving it to the kernel's network work costs more requests than
the cheaper requests win back.

## Fixed in passing

- **fib `media/rtsp` TestAV1Session** (c492d7e) failed on Linux in several of 40 runs: AV1's configuration
  comes in band with the first keyframe, and the test asked for the SDP as soon as the stream was
  published, before the server had read it. It now waits for the track's configuration.
- **fit's listener at port 0 with HTTP/3** (5d0b45d) failed to start when the port TCP got was held by
  another socket for UDP (`middleware/auth` TestClientAuthReload); it now tries another port, up to 8.
- **fit `upstream` TestLeaveAndRejoin** failed twice under load with two errors where one should take a
  backend out of rotation; the cause is not found yet, and the test now reports the errors themselves
  (09bf1f7).

## Where fit stands

First over h2c, h2 and HTTP/3 at every level; **first over h2 in the API gateway from 500 to 5,000 in
flight (101-106% of nginx), level at 10,000, and first with idle connections (109%)**; level with nginx over HTTP/1.1 for plain
responses; second to nginx in the API gateway over HTTP/1.1 (83-91%); first for the website over HTTPS/1.1
and h2, second over HTTP/3.

## What is left

- **W1 over HTTP/1.1, 83-91% of nginx,** spread over the request path.
- **The tail at 200,000 a second** without `loop_wait block`.
- **The website over HTTP/3,** per byte in Go's AES-GCM.
