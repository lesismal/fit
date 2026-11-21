#!/bin/bash
# A/B of fit's static handler: main 35b37690 (I230) against I231 (header block, WriteResponseCopy, fib filewatch lock-free Watch).
# raw open, virtual Seek, Lstat first) against nginx: 16 GiB of random 8 KiB
# files, and 1.25 GiB of random 64 KiB files under /m.
set -u
D=/root/sbench
SRV=0-3,8-11
CLI=4-7,12-15
OUT=$D/results4
ROUNDS=${ROUNDS:-3}
DUR=${DUR:-20}
mkdir -p $OUT
ulimit -n 1000000
echo "waiting for the bench locks $(date +%T)"
exec 9>/root/bench.lock; flock -x 9
exec 8>/root/wsfn/bench.lock; flock -x 8
echo "locked $(date +%T)"

cleanup() {
	pkill -f "$D/bin/fit-" 2>/dev/null
	docker rm -f sbench-nginx >/dev/null 2>&1
}
trap cleanup EXIT

if [ ! -f /mnt/fitrand/hot.bin ]; then
	mkdir -p /mnt/fitrand
	mountpoint -q /mnt/fitrand || mount -t tmpfs -o size=20g,nr_inodes=4m tmpfs /mnt/fitrand
	t0=$(date +%s)
	$D/bin/gen -root /mnt/fitrand -workers 16 || exit 1
	echo "generated $(find /mnt/fitrand -type f | wc -l) files in $(( $(date +%s) - t0 ))s, $(du -sh /mnt/fitrand | cut -f1)"
fi
[ -d /mnt/fitrand/m ] || $D/bin/gen -root /mnt/fitrand/m -dirs 200 -files 100 -size 65536 -workers 16 || exit 1

# busy CPUs of a list over an interval, from /proc/stat: prints busy core
# equivalents.
cpusnap() { awk -v list="$1" 'BEGIN{n=split(list,a,",");for(i=1;i<=n;i++){split(a[i],r,"-");for(c=r[1];c<=(r[2]==""?r[1]:r[2]);c++)want["cpu"c]=1}} want[$1]{idle+=$5+$6; tot=0; for(i=2;i<=9;i++)tot+=$i; all+=tot} END{print all, idle}' /proc/stat; }
busy() { awk -v a="$1" -v b="$2" 'BEGIN{split(a,x," ");split(b,y," "); printf "%.2f", (y[1]-x[1]-(y[2]-x[2]))/(y[1]-x[1])*'"$3"'}'; }

start() {
	case $1 in
	nginx)
		docker run -d --name sbench-nginx --network host --cpuset-cpus $SRV --ulimit nofile=1000000:1000000 \
			-v $D/nginx.conf:/etc/nginx/nginx.conf:ro -v /mnt/fitrand:/mnt/fitrand:ro nginx:1.31.4 >/dev/null ;;
	*)
		taskset -c $SRV $D/bin/$1 run -c $D/fit.conf >$OUT/$2.server.log 2>&1 & ;;
	esac
	for _ in $(seq 100); do curl -sf -o /dev/null http://127.0.0.1:8090/hot.bin && return 0; sleep 0.1; done
	echo "$1 did not start"; return 1
}
stop() {
	case $1 in
	nginx) docker rm -f sbench-nginx >/dev/null ;;
	*) pkill -INT -f "$D/bin/$1 " ; sleep 1; pkill -9 -f "$D/bin/$1 " 2>/dev/null ;;
	esac
	while ss -ltn | grep -q ':8090 '; do sleep 0.2; done
}
wrk() { # conns duration target-args...
	local c=$1 d=$2; shift 2
	docker run --rm --network host --cpuset-cpus $CLI -v $D:/s --ulimit nofile=1000000:1000000 wrk:local -t8 -c$c -d${d}s --latency "$@"
}
one() { # server scenario round
	local srv=$1 sc=$2 r=$3 tag=$1.$2.r$3 c args
	case $sc in
	rand-*) c=${sc#rand-}; args="-s /s/rand.lua http://127.0.0.1:8090/" ;;
	hot-*) c=${sc#hot-}; args="http://127.0.0.1:8090/hot.bin" ;;
	mid-*) c=${sc#mid-}; args="-s /s/mid.lua http://127.0.0.1:8090/" ;;
	esac
	start $srv $tag || return
	wrk $c 5 $args >/dev/null
	local s0 c0 s1 c1 rss
	s0=$(cpusnap $SRV); c0=$(cpusnap $CLI)
	wrk $c $DUR $args >$OUT/$tag.wrk
	s1=$(cpusnap $SRV); c1=$(cpusnap $CLI)
	case $srv in nginx) rss=$(ps -o rss= -C nginx | awk '{s+=$1}END{print s}') ;; *) rss=$(awk '/VmHWM/{print $2}' /proc/$(pgrep -f "$D/bin/$srv " | head -1)/status) ;; esac
	echo "$tag rps=$(awk '/Requests\/sec/{print $2}' $OUT/$tag.wrk) srvcpu=$(busy "$s0" "$s1" 8) clicpu=$(busy "$c0" "$c1" 8) rss_kb=$rss $(awk '/ 50%| 90%| 99%/{printf "%s=%s ", $1, $2}' $OUT/$tag.wrk) errs=$(grep -c 'Socket errors\|Non-2xx' $OUT/$tag.wrk)" | tee -a $OUT/summary.txt
	stop $srv
}
for r in $(seq $ROUNDS); do
	for sc in rand-256 rand-512 hot-256 mid-256; do
		for srv in fit-main fit-i231; do
			one $srv $sc $r
		done
	done
done
# System calls per request at rand-512, for each server.
SC="raw_syscalls:sys_enter"
for s in openat read pread64 close newfstat newfstatat fcntl lseek epoll_ctl sendto writev sendfile64 recvfrom; do
	[ -d /sys/kernel/tracing/events/syscalls/sys_enter_$s ] && SC="$SC,syscalls:sys_enter_$s"
done
for srv in fit-main fit-i231; do
	start $srv $srv.sys || continue
	wrk 512 5 -s /s/rand.lua http://127.0.0.1:8090/ >/dev/null
	wrk 512 20 -s /s/rand.lua http://127.0.0.1:8090/ > $OUT/$srv.sys.wrk &
	W=$!
	sleep 5
	perf stat -e "$SC" -C $SRV -x, -o $OUT/$srv.syscalls.csv -- sleep 10
	wait $W
	echo "$srv syscalls: $(awk -v rps=$(awk '/Requests\/sec/{print $2}' $OUT/$srv.sys.wrk) -F, '$1+0>0{printf "%s=%.2f ", $3, $1/10/rps}' $OUT/$srv.syscalls.csv | sed 's/syscalls:sys_enter_//g; s/raw_syscalls:sys_enter/all/')" | tee -a $OUT/summary.txt
	stop $srv
done
echo "done $(date +%T)" | tee -a $OUT/summary.txt
touch $OUT/done
