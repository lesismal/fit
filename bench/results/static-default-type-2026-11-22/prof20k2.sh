#!/bin/bash
# fit after I232 (fit-cliff): 20 KiB by sendfile against 16 KiB read whole: syscalls a request, perf and a Go CPU profile.
set -u
D=/root/sbench
SRV=0-3,8-11
CLI=4-7,12-15
OUT=$D/prof20k2
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
$D/bin/gen -root /mnt/fitrand/y -dirs 104 -files 1000 -size 16384 -workers 16
SC="raw_syscalls:sys_enter"
for s in openat read pread64 close newfstat newfstatat fcntl lseek epoll_ctl sendto writev write sendfile64 recvfrom dup dup3 ppoll setsockopt; do
	[ -d /sys/kernel/tracing/events/syscalls/sys_enter_$s ] && SC="$SC,syscalls:sys_enter_$s"
done
wrkbg() { docker run --rm --network host --cpuset-cpus $CLI -v $D:/s -e ND=104 -e PREFIX=$PFX --ulimit nofile=1000000:1000000 wrk:local -t8 -c256 -d${1}s --latency -s /s/sweep.lua http://127.0.0.1:8082/ > $2 & WRK=$!; }
measure() {
	local n=$1
	wrkbg 5 /dev/null; wait $WRK
	wrkbg 45 $OUT/$n.wrk
	sleep 3
	[ $n != nginx ] && curl -s -o $OUT/$n.cpu.pprof "http://127.0.0.1:9015/debug/pprof/profile?seconds=15" || sleep 15
	perf stat -e "$SC" -C $SRV -x, -o $OUT/$n.syscalls.csv -- sleep 10
	perf record -F 999 -g -C $SRV -o $OUT/$n.perf.data -- sleep 8 2>/dev/null
	wait $WRK
	perf report -i $OUT/$n.perf.data --no-children --sort sym --stdio -g none 2>/dev/null | grep -v "^#" | grep -v "^$" | head -60 > $OUT/$n.perf.sym.txt
	echo "$n rps=$(awk '/Requests\/sec/{print $2}' $OUT/$n.wrk) syscalls/req: $(awk -v rps=$(awk '/Requests\/sec/{print $2}' $OUT/$n.wrk) -F, '$1+0>0{printf "%s=%.2f ", $3, $1/10/rps}' $OUT/$n.syscalls.csv | sed 's/syscalls:sys_enter_//g; s/raw_syscalls:sys_enter/all/')"
}
for n in 20k 16k; do
	[ $n = 20k ] && PFX=/z || PFX=/y
	taskset -c $SRV $D/bin/fit-cliff run -c $D/fit-prof2.conf > $OUT/$n.log 2>&1 &
	for _ in $(seq 100); do ss -ltn | grep -q ":8082 " && break; sleep 0.1; done
	measure $n
	pkill -INT -f "$D/bin/[f]it-cliff"; sleep 1; pkill -9 -f "$D/bin/[f]it-cliff" 2>/dev/null
	while ss -ltn | grep -q ":8082 "; do sleep 0.2; done
done
echo "done $(date +%T)"
