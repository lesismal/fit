#!/bin/bash
# Allocation profile and GC trace of fit at rand-256.
set -u
D=/root/sbench
SRV=0-3,8-11
CLI=4-7,12-15
OUT=$D/alloc20
BIN=${BIN:-fit-open}
mkdir -p $OUT
ulimit -n 1000000
exec 9>/root/bench.lock; flock -x 9
exec 8>/root/wsfn/bench.lock; flock -x 8
echo "locked $(date +%T)"
cleanup() { pkill -f "$D/bin/[f]it-"; umount /mnt/fitrand 2>/dev/null; }
trap cleanup EXIT
mkdir -p /mnt/fitrand
mountpoint -q /mnt/fitrand || mount -t tmpfs -o size=6g,nr_inodes=1m tmpfs /mnt/fitrand
$D/bin/gen -root /mnt/fitrand/z -dirs 104 -files 1000 -size 20480 -workers 16
GODEBUG=gctrace=1 taskset -c $SRV $D/bin/$BIN run -c $D/fit-prof3.conf > $OUT/$BIN.log 2>&1 &
for _ in $(seq 100); do ss -ltn | grep -q :8082 && break; sleep 0.1; done
wrk() { docker run --rm --network host --cpuset-cpus $CLI -v $D:/s --ulimit nofile=1000000:1000000 -e ND=104 -e PREFIX=/z wrk:local -t8 -c256 -d$1s --latency -s /s/sweep.lua http://127.0.0.1:8082/; }
wrk 5 >/dev/null
wrk 30 > $OUT/$BIN.wrk & W=$!
sleep 5
g0=$(grep -c '^gc ' $OUT/$BIN.log)
curl -s -o $OUT/$BIN.allocs.pprof "http://127.0.0.1:9015/debug/pprof/allocs?seconds=10"
g1=$(grep -c '^gc ' $OUT/$BIN.log)
curl -s -o $OUT/$BIN.heap.txt "http://127.0.0.1:9015/debug/pprof/heap?debug=1"
wait $W
echo "$BIN rps=$(awk '/Requests\/sec/{print $2}' $OUT/$BIN.wrk) gcs_in_10s=$((g1-g0)) $(awk '/ 99%/{print "p99="$2}' $OUT/$BIN.wrk)"
grep '^gc ' $OUT/$BIN.log | tail -3
echo "done $(date +%T)"
