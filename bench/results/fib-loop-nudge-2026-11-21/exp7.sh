#!/bin/bash
# fib A99 (loops nudged by workers): fit main against fit on fib A99, and a
# fib hello server on fib main against fib A99.
set -u
D=/root/sbench
SRV=0-3,8-11
CLI=4-7,12-15
OUT=$D/exp7
ROUNDS=${ROUNDS:-3}
mkdir -p $OUT
ulimit -n 1000000
exec 9>/root/bench.lock; flock -x 9
exec 8>/root/wsfn/bench.lock; flock -x 8
echo "locked $(date +%T)"
cleanup() { pkill -f "$D/bin/[f]it-"; pkill -f "$D/bin/[h]ello-"; umount /mnt/fitrand 2>/dev/null; }
trap cleanup EXIT
mkdir -p /mnt/fitrand
mountpoint -q /mnt/fitrand || mount -t tmpfs -o size=20g,nr_inodes=4m tmpfs /mnt/fitrand
[ -f /mnt/fitrand/hot.bin ] || $D/bin/gen -root /mnt/fitrand -workers 16 || exit 1
[ -d /mnt/fitrand/m ] || $D/bin/gen -root /mnt/fitrand/m -dirs 200 -files 100 -size 65536 -workers 16 || exit 1
cpusnap() { awk -v list="$1" 'BEGIN{n=split(list,a,",");for(i=1;i<=n;i++){split(a[i],r,"-");for(c=r[1];c<=(r[2]==""?r[1]:r[2]);c++)want["cpu"c]=1}} want[$1]{idle+=$5+$6; tot=0; for(i=2;i<=9;i++)tot+=$i; all+=tot} END{print all, idle}' /proc/stat; }
busy() { awk -v a="$1" -v b="$2" 'BEGIN{split(a,x," ");split(b,y," "); printf "%.2f", (y[1]-x[1]-(y[2]-x[2]))/(y[1]-x[1])*8}'; }
wrk() { docker run --rm --network host --cpuset-cpus $CLI -v $D:/s --ulimit nofile=1000000:1000000 wrk:local "$@"; }
one() { # server scenario round
	local srv=$1 sc=$2 r=$3 tag=$1.$2.r$3 c args
	case $sc in
	rand-*) c=${sc#rand-}; args="-s /s/rand.lua http://127.0.0.1:8082/" ;;
	paced-*) c=${sc#paced-}; args="-s /s/paced.lua http://127.0.0.1:8082/" ;;
	hot-*) c=${sc#hot-}; args="http://127.0.0.1:8082/hot.bin" ;;
	mid-*) c=${sc#mid-}; args="-s /s/mid.lua http://127.0.0.1:8082/" ;;
	ka-*) c=${sc#ka-}; args="http://127.0.0.1:8082/" ;;
	pipe-*) c=${sc#pipe-}; args="-s /s/pipeline.lua http://127.0.0.1:8082/" ;;
	esac
	case $srv in
	fit-*) taskset -c $SRV $D/bin/$srv run -c $D/fit.conf > $OUT/$tag.log 2>&1 & ;;
	hello-*) FIB_LOG_SETTINGS=0 taskset -c $SRV $D/bin/$srv > $OUT/$tag.log 2>&1 & ;;
	esac
	for _ in $(seq 200); do ss -ltn | grep -q ':8082 ' && break; sleep 0.05; done
	wrk -t8 -c$c -d5s $args >/dev/null
	local s0 s1 c0 c1
	s0=$(cpusnap $SRV); c0=$(cpusnap $CLI)
	wrk -t8 -c$c -d20s --latency $args > $OUT/$tag.wrk
	s1=$(cpusnap $SRV); c1=$(cpusnap $CLI)
	echo "$tag rps=$(awk '/Requests\/sec/{print $2}' $OUT/$tag.wrk) srvcpu=$(busy "$s0" "$s1") clicpu=$(busy "$c0" "$c1") $(awk '/ 50%| 90%| 99%/{printf "%s=%s ", $1, $2}' $OUT/$tag.wrk) max=$(awk '/Latency  /{print $4}' $OUT/$tag.wrk) errs=$(grep -c 'Socket errors\|Non-2xx' $OUT/$tag.wrk)" | tee -a $OUT/summary.txt
	pkill -INT -f "$D/bin/$srv\$|$D/bin/$srv "; sleep 1; pkill -9 -f "$D/bin/$srv\$|$D/bin/$srv " 2>/dev/null
	while ss -ltn | grep -q ':8082 '; do sleep 0.2; done
}
for r in $(seq $ROUNDS); do
	for sc in rand-128 rand-256 rand-512 paced-360 hot-256 mid-256; do
		for srv in fit-main fit-a99; do one $srv $sc $r; done
	done
	for sc in ka-64 ka-256 ka-4096 pipe-256; do
		for srv in hello-main hello-a99; do one $srv $sc $r; done
	done
done
echo "done $(date +%T)" | tee -a $OUT/summary.txt
