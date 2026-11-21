#!/bin/bash
# fib A100 (owned HTTP/1 bodies back to the pool once sent, client bodies pooled up to 1 MiB): A = fit main 99181244 on
# fib 3354267, B = the same fit on fib A100, C = B with the static handler's
# read files sent with WriteResponseOwned. Proxy of 8 and 32 KiB bodies from
# nginx, and random 8 KiB static files.
set -u
D=/root/sbench
SRV=0-3,8-11
CLI=4-7,12-15
OUT=$D/exp8
ROUNDS=${ROUNDS:-3}
mkdir -p $OUT
ulimit -n 1000000
exec 9>/root/bench.lock; flock -x 9
exec 8>/root/wsfn/bench.lock; flock -x 8
echo "locked $(date +%T)"
cleanup() { pkill -f "$D/bin/[f]it-"; docker rm -f sbench-backend >/dev/null 2>&1; umount /mnt/fitrand 2>/dev/null; }
trap cleanup EXIT
mkdir -p /mnt/fitrand
mountpoint -q /mnt/fitrand || mount -t tmpfs -o size=20g,nr_inodes=4m tmpfs /mnt/fitrand
[ -f /mnt/fitrand/hot.bin ] || $D/bin/gen -root /mnt/fitrand -workers 16 || exit 1
head -c 8192 /dev/urandom > /mnt/fitrand/p8k.bin
head -c 32768 /dev/urandom > /mnt/fitrand/p32k.bin
docker run -d --name sbench-backend --network host --cpuset-cpus $CLI --ulimit nofile=1000000:1000000 \
	-v $D/nginx-backend.conf:/etc/nginx/nginx.conf:ro -v /mnt/fitrand:/mnt/fitrand:ro nginx:1.31.4 >/dev/null
for _ in $(seq 100); do curl -sf -o /dev/null http://127.0.0.1:9090/p8k.bin && break; sleep 0.1; done
cpusnap() { awk -v list="$1" 'BEGIN{n=split(list,a,",");for(i=1;i<=n;i++){split(a[i],r,"-");for(c=r[1];c<=(r[2]==""?r[1]:r[2]);c++)want["cpu"c]=1}} want[$1]{idle+=$5+$6; tot=0; for(i=2;i<=9;i++)tot+=$i; all+=tot} END{print all, idle}' /proc/stat; }
busy() { awk -v a="$1" -v b="$2" 'BEGIN{split(a,x," ");split(b,y," "); printf "%.2f", (y[1]-x[1]-(y[2]-x[2]))/(y[1]-x[1])*8}'; }
wrk() { docker run --rm --network host --cpuset-cpus $CLI -v $D:/s --ulimit nofile=1000000:1000000 wrk:local "$@"; }
one() { # server scenario round
	local srv=$1 sc=$2 r=$3 tag=$1.$2.r$3 c args conf=fit.conf
	case $sc in
	proxy-8k) c=256; args="http://127.0.0.1:8082/p8k.bin"; conf=proxy.conf ;;
	proxy-32k) c=256; args="http://127.0.0.1:8082/p32k.bin"; conf=proxy.conf ;;
	rand-*) c=${sc#rand-}; args="-s /s/rand.lua http://127.0.0.1:8082/" ;;
	esac
	GODEBUG=gctrace=1 taskset -c $SRV $D/bin/$srv run -c $D/$conf > $OUT/$tag.log 2>&1 &
	for _ in $(seq 200); do ss -ltn | grep -q ':8082 ' && break; sleep 0.05; done
	wrk -t8 -c$c -d5s $args >/dev/null
	local s0 s1 g0 g1
	s0=$(cpusnap $SRV); g0=$(grep -c '^gc ' $OUT/$tag.log)
	wrk -t8 -c$c -d20s --latency $args > $OUT/$tag.wrk
	s1=$(cpusnap $SRV); g1=$(grep -c '^gc ' $OUT/$tag.log)
	echo "$tag rps=$(awk '/Requests\/sec/{print $2}' $OUT/$tag.wrk) srvcpu=$(busy "$s0" "$s1") gcs=$((g1-g0)) $(awk '/ 50%| 90%| 99%/{printf "%s=%s ", $1, $2}' $OUT/$tag.wrk) errs=$(grep -c 'Socket errors\|Non-2xx' $OUT/$tag.wrk)" | tee -a $OUT/summary.txt
	pkill -INT -f "$D/bin/$srv "; sleep 1; pkill -9 -f "$D/bin/$srv " 2>/dev/null
	while ss -ltn | grep -q ':8082 '; do sleep 0.2; done
}
for r in $(seq $ROUNDS); do
	for sc in proxy-8k proxy-32k rand-256 rand-512; do
		for srv in fit-a fit-b fit-c; do one $srv $sc $r; done
	done
done
echo "done $(date +%T)" | tee -a $OUT/summary.txt
