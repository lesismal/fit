#!/bin/sh
# sweep.sh runs the connection sweep on the benchmark host: every server,
# scenario and protocol at seven connection counts, low to high, one JSON and
# one log per level, as the 2026-10-23 sweep did. It runs on the host itself,
# from a directory holding bin/ (fitbench, fit, idleconn for linux/amd64) and
# bench/ (this repository's), and takes hours: start it under nohup with
# < /dev/null.
#
#   bench/sweep.sh DATE       results in bench/results/sweep-DATE/l<N>-c<C>-h<H>.json
#
# | Level | HTTP/1.1 connections | h2c, h2, HTTP/3 connections × 32 streams |
# | 1 | 16     | 8     |
# | 2 | 64     | 16    |
# | 3 | 256    | 64    |
# | 4 | 1,024  | 256   |
# | 5 | 4,096  | 1,024 |
# | 6 | 16,384 | 4,096 |
# | 7 | 32,768 | 8,192 |
#
# The proxy runs at levels 1-5 only. At 6 and 7, 131,072 and 262,144 requests
# in flight against one backend on two CPUs (about 260k requests a second)
# measure the load generator and the backend, not the proxies: in the
# 2026-10-23 sweep every proxy failed there over h2c and h2 (nothing but 5xx,
# or a hung h2load), and fit, past its max_conns and max_pending, spends its
# CPU answering the closed-loop load generator's retries with 503.
set -eu

[ $# -eq 1 ] || { echo "usage: bench/sweep.sh DATE" >&2; exit 2; }
date=$1
here=$(pwd)
# The sweep's results go in a folder of their own, where its report goes too.
results=bench/results/sweep-$date
if [ -e "$results" ]; then
	echo "sweep.sh: $results is there already; give another DATE" >&2
	exit 1
fi
mkdir -p "$results"
echo "start $(date -u +%FT%TZ)" > sweep.log
for lv in 1:16:8 2:64:16 3:256:64 4:1024:256 5:4096:1024 6:16384:4096 7:32768:8192; do
	n=${lv%%:*}
	rest=${lv#*:}
	c=${rest%%:*}
	h=${rest#*:}
	scenarios=fixed,static,proxy,cache-hot,cache-zipf
	if [ "$n" -ge 6 ]; then
		scenarios=fixed,static,cache-hot,cache-zipf
	fi
	echo "level $n h1=$c h2=$h start $(date -u +%FT%TZ)" >> sweep.log
	status=0
	./bin/fitbench -root "$here" -bin "$here/bin" -scenarios $scenarios -idle "" \
		-connections "$c" -h2-connections "$h" -h2-streams 32 \
		-out "$results/l$n-c$c-h$h.json" > "level$n.log" 2>&1 || status=$?
	echo "level $n exit $status $(date -u +%FT%TZ)" >> sweep.log
done
touch done
