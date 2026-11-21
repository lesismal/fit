#!/bin/bash
# I231 with loop_wait park (default) against block, at rand-256 and rand-512.
set -u
D=/root/sbench
SRV=0-3,8-11
CLI=4-7,12-15
OUT=$D/exp2
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
wrk() { docker run --rm --network host --cpuset-cpus $CLI -v $D:/s --ulimit nofile=1000000:1000000 wrk:local -t8 -c$1 -d$2s --latency $3 http://127.0.0.1:8090/${4:-}; }
for r in 1 2; do
	for sc in rand-256 rand-512 hot-256; do
		for conf in fit fit-block; do
			c=${sc#*-}; s="-s /s/rand.lua"; p=""
			[ ${sc%-*} = hot ] && s="" && p="hot.bin"
			taskset -c $SRV $D/bin/fit-i231 run -c $D/$conf.conf > $OUT/$conf.$sc.r$r.log 2>&1 &
			for _ in $(seq 100); do curl -sf -o /dev/null http://127.0.0.1:8090/hot.bin && break; sleep 0.1; done
			wrk $c 5 "$s" "$p" >/dev/null
			wrk $c 20 "$s" "$p" > $OUT/$conf.$sc.r$r.wrk
			echo "$conf.$sc.r$r rps=$(awk '/Requests\/sec/{print $2}' $OUT/$conf.$sc.r$r.wrk) $(awk '/ 50%| 90%| 99%/{printf "%s=%s ", $1, $2}' $OUT/$conf.$sc.r$r.wrk)"
			pkill -INT -f "$D/bin/[f]it-"; sleep 1; pkill -9 -f "$D/bin/[f]it-" 2>/dev/null
			while ss -ltn | grep -q ':8090 '; do sleep 0.2; done
		done
	done
done
echo "done $(date +%T)"
