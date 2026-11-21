#!/bin/bash
# The ceiling of the 20 KiB sendfile path: fit-exp with each experiment toggled by the environment.
set -u
D=/root/sbench; SRV=0-3,8-11; CLI=4-7,12-15; OUT=$D/expab; mkdir -p $OUT
ulimit -n 1000000
exec 9>/root/bench.lock; flock -x 9
exec 8>/root/wsfn/bench.lock; flock -x 8
echo "locked $(date +%T)"
cleanup() { pkill -f "$D/bin/[f]it-"; umount /mnt/fitrand 2>/dev/null; }
trap cleanup EXIT
cpusnap() { awk -v list="$1" 'BEGIN{n=split(list,a,",");for(i=1;i<=n;i++){split(a[i],r,"-");for(c=r[1];c<=(r[2]==""?r[1]:r[2]);c++)want["cpu"c]=1}} want[$1]{idle+=$5+$6; tot=0; for(i=2;i<=9;i++)tot+=$i; all+=tot} END{print all, idle}' /proc/stat; }
busy() { awk -v a="$1" -v b="$2" -v n=8 'BEGIN{split(a,x," ");split(b,y," "); printf "%.2f", (y[1]-x[1]-(y[2]-x[2]))/(y[1]-x[1])*n}'; }
mkdir -p /mnt/fitrand; mountpoint -q /mnt/fitrand || mount -t tmpfs -o size=4g,nr_inodes=1m tmpfs /mnt/fitrand
head -c 8192 /dev/urandom > /mnt/fitrand/hot.bin
$D/bin/gen -root /mnt/fitrand/z -dirs 104 -files 1000 -size 20480 -workers 16
declare -A ENV=([base]="" [nosniff]="FIT_EXP_NOSNIFF=1" [nofstat]="FIT_EXP_NOFSTAT=1" [block]="FIT_EXP_BLOCK=1" [inline]="FIB_EXP_INLINE=1" [all]="FIT_EXP_NOSNIFF=1 FIT_EXP_NOFSTAT=1 FIT_EXP_BLOCK=1 FIB_EXP_INLINE=1")
for r in 1 2; do for v in base nosniff nofstat block inline all; do
	env ${ENV[$v]} taskset -c $SRV $D/bin/fit-exp run -c $D/fit-prof2.conf > $OUT/$v.log 2>&1 &
	for _ in $(seq 100); do ss -ltn | grep -q ":8082 " && break; sleep 0.1; done
	[ $r = 1 ] && { echo "--- $v"; curl -s -D - -o /dev/null http://127.0.0.1:8082/z/d0000/f0000 | tr -d "\r" | grep -v "^Date\|^$"; }
	docker run --rm --network host --cpuset-cpus $CLI -v $D:/s -e ND=104 -e PREFIX=/z --ulimit nofile=1000000:1000000 wrk:local -t8 -c256 -d5s -s /s/sweep.lua http://127.0.0.1:8082/ >/dev/null
	s0=$(cpusnap $SRV)
	docker run --rm --network host --cpuset-cpus $CLI -v $D:/s -e ND=104 -e PREFIX=/z --ulimit nofile=1000000:1000000 wrk:local -t8 -c256 -d15s --latency -s /s/sweep.lua http://127.0.0.1:8082/ > $OUT/$v.r$r.out
	s1=$(cpusnap $SRV)
	echo "r$r $v rps=$(awk "/Requests\/sec/{print \$2}" $OUT/$v.r$r.out) p99=$(awk "/ 99%/{print \$2}" $OUT/$v.r$r.out) errs=$(grep -c "Socket errors\|Non-2xx" $OUT/$v.r$r.out) srvcpu=$(busy "$s0" "$s1")" | tee -a $OUT/summary.txt
	pkill -INT -f "$D/bin/[f]it-"; sleep 1; pkill -9 -f "$D/bin/[f]it-" 2>/dev/null
	while ss -ltn | grep -q ":8082 "; do sleep 0.2; done
done; done
echo "done $(date +%T)"
