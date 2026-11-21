#!/bin/bash
# TCP segments and send calls a request at 20 KiB: fit-cliff (header written apart) against fit-cliff2 (corked with the file).
set -u
D=/root/sbench; SRV=0-3,8-11; CLI=4-7,12-15
ulimit -n 1000000
exec 9>/root/bench.lock; flock -x 9
exec 8>/root/wsfn/bench.lock; flock -x 8
echo "locked $(date +%T)"
cleanup() { pkill -f "$D/bin/[f]it-"; umount /mnt/fitrand 2>/dev/null; }
trap cleanup EXIT
mkdir -p /mnt/fitrand; mountpoint -q /mnt/fitrand || mount -t tmpfs -o size=4g,nr_inodes=1m tmpfs /mnt/fitrand
head -c 8192 /dev/urandom > /mnt/fitrand/hot.bin
$D/bin/gen -root /mnt/fitrand/z -dirs 104 -files 1000 -size 20480 -workers 16
for b in fit-cliff fit-cliff2 fit-cliff fit-cliff2; do
	taskset -c $SRV $D/bin/$b run -c $D/fit-prof2.conf > /dev/null 2>&1 &
	for _ in $(seq 100); do ss -ltn | grep -q ":8082 " && break; sleep 0.1; done
	docker run --rm --network host --cpuset-cpus $CLI -v $D:/s -e ND=104 -e PREFIX=/z --ulimit nofile=1000000:1000000 wrk:local -t8 -c256 -d5s -s /s/sweep.lua http://127.0.0.1:8082/ >/dev/null
	s0=$(nstat -az TcpOutSegs | awk "/TcpOutSegs/{print \$2}")
	perf stat -e syscalls:sys_enter_sendto,syscalls:sys_enter_sendmsg,syscalls:sys_enter_sendfile64 -C $SRV -x, -o /tmp/segs.csv -- \
		docker run --rm --network host --cpuset-cpus $CLI -v $D:/s -e ND=104 -e PREFIX=/z --ulimit nofile=1000000:1000000 wrk:local -t8 -c256 -d10s -s /s/sweep.lua http://127.0.0.1:8082/ > /tmp/segs.wrk
	s1=$(nstat -az TcpOutSegs | awk "/TcpOutSegs/{print \$2}")
	req=$(awk "/requests in/{print \$1}" /tmp/segs.wrk)
	echo "$b rps=$(awk "/Requests\/sec/{print \$2}" /tmp/segs.wrk) segs/req=$(awk -v a=$s0 -v b=$s1 -v r=$req "BEGIN{printf \"%.2f\", (b-a)/r}") $(awk -v r=$req -F, "\$1+0>0{printf \"%s=%.2f \", \$3, \$1/r}" /tmp/segs.csv | sed s/syscalls:sys_enter_//g)"
	pkill -INT -f "$D/bin/[f]it-"; sleep 1; pkill -9 -f "$D/bin/[f]it-" 2>/dev/null
	while ss -ltn | grep -q ":8082 "; do sleep 0.2; done
done
echo "done $(date +%T)"
