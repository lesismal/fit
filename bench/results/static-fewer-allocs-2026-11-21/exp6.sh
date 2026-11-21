#!/bin/bash
# Which of I231's changes makes the tail: a = old fib, b = Owned, c = no pool growth.
set -u
D=/root/sbench
SRV=0-3,8-11
CLI=4-7,12-15
OUT=$D/exp6
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
wrk() { docker run --rm --network host --cpuset-cpus $CLI -v $D:/s --ulimit nofile=1000000:1000000 wrk:local -t8 -c$1 -d$2s --latency -s /s/paced.lua http://127.0.0.1:8082/; }
run() { # tag bin conns env...
	local tag=$1 bin=$2 c=$3; shift 3
	env "$@" taskset -c $SRV $D/bin/$bin run -c $D/fit.conf > $OUT/$tag.log 2>&1 &
	for _ in $(seq 100); do curl -sf -o /dev/null http://127.0.0.1:8082/hot.bin && break; sleep 0.1; done
	wrk $c 5 >/dev/null
	wrk $c 20 > $OUT/$tag.wrk
	echo "$tag rps=$(awk '/Requests\/sec/{print $2}' $OUT/$tag.wrk) $(awk '/ 50%| 75%| 90%| 99%/{printf "%s=%s ", $1, $2}' $OUT/$tag.wrk) max=$(awk '/Latency  /{print $4}' $OUT/$tag.wrk)"
	pkill -INT -f "$D/bin/[f]it-"; sleep 1; pkill -9 -f "$D/bin/[f]it-" 2>/dev/null
	while ss -ltn | grep -q ':8082 '; do sleep 0.2; done
}
for r in 1 2; do
	for c in 360; do
		run main-paced-$c.r$r fit-main $c X=1
		run i231-paced-$c.r$r fit-i231 $c X=1
		run va-oldfib-paced-$c.r$r fit-va $c X=1
	done
done
echo "done $(date +%T)"
