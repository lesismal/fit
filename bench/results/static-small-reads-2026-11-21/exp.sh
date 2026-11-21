#!/bin/bash
# Why I230 has a longer tail: variants of one binary, at rand-256 and rand-512.
set -u
D=/root/sbench
SRV=0-3,8-11
CLI=4-7,12-15
OUT=$D/exp
mkdir -p $OUT
ulimit -n 1000000
exec 9>/root/bench.lock; flock -x 9
exec 8>/root/wsfn/bench.lock; flock -x 8
echo "locked $(date +%T)"
cleanup() { pkill -f "$D/bin/[f]it-x" 2>/dev/null; umount /mnt/fitrand 2>/dev/null; }
trap cleanup EXIT
mkdir -p /mnt/fitrand
mountpoint -q /mnt/fitrand || mount -t tmpfs -o size=20g,nr_inodes=4m tmpfs /mnt/fitrand
[ -f /mnt/fitrand/hot.bin ] || $D/bin/gen -root /mnt/fitrand -workers 16 || exit 1
wrk() { docker run --rm --network host --cpuset-cpus $CLI -v $D:/s --ulimit nofile=1000000:1000000 wrk:local -t8 -c$1 -d$2s --latency -s /s/rand.lua http://127.0.0.1:8090/; }
run() { # tag conns env...
	local tag=$1 c=$2; shift 2
	env "$@" taskset -c $SRV $D/bin/fit-x run -c $D/fit-prof.conf > $OUT/$tag.log 2>&1 &
	for _ in $(seq 100); do curl -sf -o /dev/null http://127.0.0.1:8090/hot.bin && break; sleep 0.1; done
	wrk $c 5 >/dev/null
	if [ $tag = trace ]; then
		wrk $c 15 > $OUT/$tag.wrk & W=$!
		sleep 5; curl -s -o $OUT/trace.out "http://127.0.0.1:9015/debug/pprof/trace?seconds=3"
		wait $W
	else
		wrk $c 20 > $OUT/$tag.wrk
	fi
	echo "$tag c=$c rps=$(awk '/Requests\/sec/{print $2}' $OUT/$tag.wrk) $(awk '/ 50%| 90%| 99%/{printf "%s=%s ", $1, $2}' $OUT/$tag.wrk) max=$(awk '/Latency  /{print $4}' $OUT/$tag.wrk) gcs=$(grep -c '^gc ' $OUT/$tag.log)"
	pkill -INT -f "$D/bin/[f]it-x"; sleep 1; pkill -9 -f "$D/bin/[f]it-x" 2>/dev/null
	while ss -ltn | grep -q ':8090 '; do sleep 0.2; done
}
for c in 256 512; do
	run base-$c $c X=1
	run gosched-$c $c FITX_GOSCHED=1
	run w8-$c $c FITX_POOL_WORKERS=8
	run w16-$c $c FITX_POOL_WORKERS=16
done
run gctrace 256 GODEBUG=gctrace=1
run trace 256 X=1
echo "done $(date +%T)"
