#!/bin/bash
# Execution traces and goroutine counts at rand-256, I231 against main.
set -u
D=/root/sbench
SRV=0-3,8-11
CLI=4-7,12-15
OUT=$D/trace2
mkdir -p $OUT
ulimit -n 1000000
exec 9>/root/bench.lock; flock -x 9
exec 8>/root/wsfn/bench.lock; flock -x 8
echo "locked $(date +%T)"
cleanup() { pkill -f "$D/bin/[f]it-"; umount /mnt/fitrand 2>/dev/null; }
trap cleanup EXIT
mkdir -p /mnt/fitrand
mountpoint -q /mnt/fitrand || mount -t tmpfs -o size=20g,nr_inodes=4m tmpfs /mnt/fitrand
[ -f /mnt/fitrand/hot.bin ] || $D/bin/gen -root /mnt/fitrand -workers 16 || exit 1
wrk() { docker run --rm --network host --cpuset-cpus $CLI -v $D:/s --ulimit nofile=1000000:1000000 wrk:local -t8 -c256 -d$1s --latency -s /s/rand.lua http://127.0.0.1:8090/; }
for BIN in fit-i231 fit-main; do
	taskset -c $SRV $D/bin/$BIN run -c $D/fit-prof.conf > $OUT/$BIN.log 2>&1 &
	for _ in $(seq 100); do curl -sf -o /dev/null http://127.0.0.1:8090/hot.bin && break; sleep 0.1; done
	wrk 5 >/dev/null
	wrk 20 > $OUT/$BIN.wrk & W=$!
	sleep 5
	curl -s -o $OUT/$BIN.trace "http://127.0.0.1:9015/debug/pprof/trace?seconds=3"
	for k in 1 2 3; do curl -s "http://127.0.0.1:9015/debug/pprof/goroutine?debug=1" | head -1; sleep 1; done > $OUT/$BIN.goroutines
	curl -s -o $OUT/$BIN.goroutine.txt "http://127.0.0.1:9015/debug/pprof/goroutine?debug=1"
	wait $W
	echo "$BIN rps=$(awk '/Requests\/sec/{print $2}' $OUT/$BIN.wrk) $(awk '/ 50%| 90%| 99%/{printf "%s=%s ", $1, $2}' $OUT/$BIN.wrk) $(tr '\n' ' ' < $OUT/$BIN.goroutines)"
	pkill -INT -f "$D/bin/[f]it-"; sleep 1; pkill -9 -f "$D/bin/[f]it-" 2>/dev/null
	while ss -ltn | grep -q ':8090 '; do sleep 0.2; done
done
echo "done $(date +%T)"
