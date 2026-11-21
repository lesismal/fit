#!/bin/bash
# Profiles fit (new) and nginx at rand-512: Go CPU profile, perf, syscall counts.
set -u
D=/root/sbench
SRV=0-3,8-11
CLI=4-7,12-15
OUT=$D/prof
mkdir -p $OUT
ulimit -n 1000000
exec 9>/root/bench.lock; flock -x 9
exec 8>/root/wsfn/bench.lock; flock -x 8
echo "locked $(date +%T)"
cleanup() { pkill -f "$D/bin/fit-prof" 2>/dev/null; docker rm -f sbench-nginx >/dev/null 2>&1; umount /mnt/fitrand 2>/dev/null; }
trap cleanup EXIT
mkdir -p /mnt/fitrand
mountpoint -q /mnt/fitrand || mount -t tmpfs -o size=20g,nr_inodes=4m tmpfs /mnt/fitrand
[ -f /mnt/fitrand/hot.bin ] || $D/bin/gen -root /mnt/fitrand -workers 16 || exit 1
echo "files ready $(date +%T)"

SC="raw_syscalls:sys_enter"
for s in openat read pread64 close newfstat newfstatat statx write writev sendto recvfrom sendfile64 epoll_wait epoll_pwait epoll_ctl futex nanosleep sched_yield getdents64 lseek tgkill madvise mmap munmap getpid rt_sigreturn; do
	[ -d /sys/kernel/tracing/events/syscalls/sys_enter_$s ] && SC="$SC,syscalls:sys_enter_$s"
done

wrkbg() { docker run --rm --network host --cpuset-cpus $CLI -v $D:/s --ulimit nofile=1000000:1000000 wrk:local -t8 -c512 -d${1}s --latency -s /s/rand.lua http://127.0.0.1:8090/ > $2 & WRK=$!; }
measure() { # name
	local n=$1
	wrkbg 5 /dev/null; wait $WRK
	wrkbg 60 $OUT/$n.wrk
	sleep 3
	if [ $n = fit ]; then
		curl -s -o $OUT/fit.cpu.pprof "http://127.0.0.1:9015/debug/pprof/profile?seconds=20"
	else
		sleep 20
	fi
	perf stat -e "$SC" -C $SRV -x, -o $OUT/$n.syscalls.csv -- sleep 10
	perf stat -e task-clock,cycles,instructions,context-switches,cpu-migrations -C $SRV -x, -o $OUT/$n.stat.csv -- sleep 5
	perf record -F 999 -g -C $SRV -o $OUT/$n.perf.data -- sleep 10 2>/dev/null
	wait $WRK
	perf report -i $OUT/$n.perf.data --no-children --sort comm,dso --stdio 2>/dev/null | grep -v "^#" | grep -v "^$" | head -40 > $OUT/$n.perf.dso.txt
	perf report -i $OUT/$n.perf.data --no-children --sort sym --stdio -g none 2>/dev/null | grep -v "^#" | grep -v "^$" | head -80 > $OUT/$n.perf.sym.txt
	echo "$n rps=$(awk '/Requests\/sec/{print $2}' $OUT/$n.wrk) $(date +%T)"
}

taskset -c $SRV $D/bin/fit-prof run -c $D/fit-prof.conf > $OUT/fit.server.log 2>&1 &
for _ in $(seq 100); do curl -sf -o /dev/null http://127.0.0.1:8090/hot.bin && break; sleep 0.1; done
measure fit
pkill -INT -f "$D/bin/fit-prof"; sleep 1; pkill -9 -f "$D/bin/fit-prof" 2>/dev/null
while ss -ltn | grep -q ':8090 '; do sleep 0.2; done

docker run -d --name sbench-nginx --network host --cpuset-cpus $SRV --ulimit nofile=1000000:1000000 \
	-v $D/nginx.conf:/etc/nginx/nginx.conf:ro -v /mnt/fitrand:/mnt/fitrand:ro nginx:1.31.4 >/dev/null
for _ in $(seq 100); do curl -sf -o /dev/null http://127.0.0.1:8090/hot.bin && break; sleep 0.1; done
measure nginx
docker rm -f sbench-nginx >/dev/null
echo "done $(date +%T)"
touch $OUT/done
