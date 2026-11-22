#!/bin/bash
# 20 KiB, fit with default_type (fit-final, fit-prof3.conf) against nginx: syscalls a request by
# name (bpftrace), perf symbols, a Go CPU profile for fit.
set -u
D=/root/sbench
SRV=0-3,8-11
CLI=4-7,12-15
OUT=$D/prof3
BIN=${BIN:-fit-final}
mkdir -p $OUT
ulimit -n 1000000
exec 9>/root/bench.lock; flock -x 9
exec 8>/root/wsfn/bench.lock; flock -x 8
echo "locked $(date +%T)"
cleanup() { pkill -f "$D/bin/[f]it-"; docker rm -f sbench-nginx >/dev/null 2>&1; umount /mnt/fitrand 2>/dev/null; }
trap cleanup EXIT
mkdir -p /mnt/fitrand
mountpoint -q /mnt/fitrand || mount -t tmpfs -o size=6g,nr_inodes=1m tmpfs /mnt/fitrand
head -c 8192 /dev/urandom > /mnt/fitrand/hot.bin
$D/bin/gen -root /mnt/fitrand/z -dirs 104 -files 1000 -size 20480 -workers 16
wrkbg() { docker run --rm --network host --cpuset-cpus $CLI -v $D:/s -e ND=104 -e PREFIX=/z --ulimit nofile=1000000:1000000 wrk:local -t8 -c256 -d${1}s --latency -s /s/sweep.lua http://127.0.0.1:8082/ > $2 & WRK=$!; }
measure() {
	local n=$1 comm=$2
	wrkbg 5 /dev/null; wait $WRK
	wrkbg 40 $OUT/$n.wrk
	sleep 3
	if [ $n != nginx ]; then curl -s -o $OUT/$n.cpu.pprof "http://127.0.0.1:9015/debug/pprof/profile?seconds=12"; else sleep 12; fi
	timeout -s INT 10 bpftrace -q -e "tracepoint:syscalls:sys_enter_* /comm == \"$comm\"/ { @[probe] = count(); }" > $OUT/$n.sys.txt 2>&1
	perf record -F 999 -g -C $SRV -o $OUT/$n.perf.data -- sleep 6 2>/dev/null
	wait $WRK
	perf report -i $OUT/$n.perf.data --no-children --sort sym --stdio -g none 2>/dev/null | grep -v "^#" | grep -v "^$" | head -80 > $OUT/$n.perf.sym.txt
	local rps=$(awk '/Requests\/sec/{print $2}' $OUT/$n.wrk)
	echo "$n rps=$rps"
	grep '@\[' $OUT/$n.sys.txt | sed 's/@\[tracepoint:syscalls:sys_enter_//; s/\]://' | awk -v rps=$rps '{t+=$2; printf "  %-16s %.3f\n", $1, $2/10/rps} END{printf "  %-16s %.3f\n", "TOTAL", t/10/rps}' | sort -k2 -nr | head -20
}
taskset -c $SRV $D/bin/$BIN run -c $D/fit-prof3.conf > $OUT/fit.log 2>&1 &
for _ in $(seq 100); do ss -ltn | grep -q ":8082 " && break; sleep 0.1; done
measure fit ${BIN:0:15}
pkill -INT -f "$D/bin/[f]it-"; sleep 1; pkill -9 -f "$D/bin/[f]it-" 2>/dev/null
while ss -ltn | grep -q ":8082 "; do sleep 0.2; done
sed "s/WORKERS/8/" $D/nginx-sweep.conf > $OUT/nginx-8.conf
docker run -d --name sbench-nginx --network host --cpuset-cpus $SRV --ulimit nofile=1000000:1000000 \
	-v $OUT/nginx-8.conf:/etc/nginx/nginx.conf:ro -v /mnt/fitrand:/mnt/fitrand:ro -v $D/certs:/certs:ro nginx:1.31.4 >/dev/null
for _ in $(seq 100); do ss -ltn | grep -q ":8082 " && break; sleep 0.1; done
measure nginx nginx
echo "done $(date +%T)"
