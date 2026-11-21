#!/bin/bash
# The change as committed: fit main on fib main (fit-iso) against fit with default_type (fit-final),
# without it set and with default_type application/octet-stream (fit-final-dt).
set -u
D=/root/sbench
CLI=4-7,12-15
OUT=$D/final
ROUNDS=${ROUNDS:-3}
DUR=${DUR:-15}
FIT=${FIT:-fit-sweep}
mkdir -p $OUT
ulimit -n 1000000
exec 9>/root/bench.lock; flock -x 9
exec 8>/root/wsfn/bench.lock; flock -x 8
echo "locked $(date +%T)"
cleanup() { pkill -f "$D/bin/[f]it-"; docker rm -f sbench-nginx >/dev/null 2>&1; umount /mnt/fitrand 2>/dev/null; }
trap cleanup EXIT
mkdir -p $D/certs
[ -f $D/certs/cert.pem ] || openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 30 \
	-subj /CN=localhost -keyout $D/certs/key.pem -out $D/certs/cert.pem 2>/dev/null
mkdir -p /mnt/fitrand
mountpoint -q /mnt/fitrand || mount -t tmpfs -o size=8g,nr_inodes=2m tmpfs /mnt/fitrand
head -c 8192 /dev/urandom > /mnt/fitrand/hot.bin

cpusnap() { awk -v list="$1" 'BEGIN{n=split(list,a,",");for(i=1;i<=n;i++){split(a[i],r,"-");for(c=r[1];c<=(r[2]==""?r[1]:r[2]);c++)want["cpu"c]=1}} want[$1]{idle+=$5+$6; tot=0; for(i=2;i<=9;i++)tot+=$i; all+=tot} END{print all, idle}' /proc/stat; }
busy() { awk -v a="$1" -v b="$2" -v n="$3" 'BEGIN{split(a,x," ");split(b,y," "); printf "%.2f", (y[1]-x[1]-(y[2]-x[2]))/(y[1]-x[1])*n}'; }
ncpu() { awk -v list="$1" 'BEGIN{n=split(list,a,",");t=0;for(i=1;i<=n;i++){split(a[i],r,"-");t+=(r[2]==""?1:r[2]-r[1]+1)}print t}'; }
start() { # server cpus
	local srv=$1 cpus=$2 n
	n=$(ncpu $cpus)
	case $srv in
	nginx)
		sed "s/WORKERS/$n/" $D/nginx-sweep.conf > $OUT/nginx-$n.conf
		docker run -d --name sbench-nginx --network host --cpuset-cpus $cpus --ulimit nofile=1000000:1000000 \
			-v $OUT/nginx-$n.conf:/etc/nginx/nginx.conf:ro -v /mnt/fitrand:/mnt/fitrand:ro -v $D/certs:/certs:ro nginx:1.31.4 >/dev/null ;;
	fit-final-dt) taskset -c $cpus $D/bin/fit-final run -c $D/fit-dt.conf > $OUT/$srv.log 2>&1 & ;;
	*) taskset -c $cpus $D/bin/$srv run -c $D/fit-sweep.conf > $OUT/$srv.log 2>&1 & ;;
	esac
	for _ in $(seq 200); do ss -ltn | grep -q ':8443 ' && ss -ltn | grep -q ':8082 ' && break; sleep 0.05; done
}
stop() {
	case $1 in
	nginx) docker rm -f sbench-nginx >/dev/null ;;
	*) pkill -INT -f "$D/bin/[f]it-"; sleep 1; pkill -9 -f "$D/bin/[f]it-" 2>/dev/null ;;
	esac
	while ss -ltn | grep -qE ':(8082|8080|8443) '; do sleep 0.2; done
}
wrk() { docker run --rm --network host --cpuset-cpus $CLI -v $D:/s --ulimit nofile=1000000:1000000 "${ENVS[@]}" wrk:local "$@"; }
# measure: tag server cpus tool args...
measure() {
	local tag=$1 srv=$2 cpus=$3 tool=$4; shift 4
	local n s0 s1 line
	n=$(ncpu $cpus)
	start $srv $cpus
	if [ $tool = wrk ]; then
		wrk "$@" -d5s >/dev/null
		s0=$(cpusnap $cpus); wrk "$@" -d${DUR}s --latency > $OUT/$tag.$srv.out; s1=$(cpusnap $cpus)
		line="rps=$(awk '/Requests\/sec/{print $2}' $OUT/$tag.$srv.out) $(awk '/ 50%| 90%| 99%/{printf "%s=%s ", $1, $2}' $OUT/$tag.$srv.out) errs=$(grep -c 'Socket errors\|Non-2xx' $OUT/$tag.$srv.out)"
	else
		docker run --rm --network host --cpuset-cpus $CLI -v $D:/s --entrypoint h2load fitbench-loadgen:latest "$@" -D 5 >/dev/null 2>&1
		s0=$(cpusnap $cpus)
		docker run --rm --network host --cpuset-cpus $CLI -v $D:/s --entrypoint h2load fitbench-loadgen:latest "$@" -D $DUR > $OUT/$tag.$srv.out 2>&1
		s1=$(cpusnap $cpus)
		line="rps=$(awk '/^finished in/{print $4}' $OUT/$tag.$srv.out) mean=$(awk '/time for request:/{print $6}' $OUT/$tag.$srv.out) max=$(awk '/time for request:/{print $5}' $OUT/$tag.$srv.out) failed=$(awk '/^requests:/{print $10}' $OUT/$tag.$srv.out)"
	fi
	echo "$tag $srv rps_line: $line srvcpu=$(busy "$s0" "$s1" $n)/$n" | tee -a $OUT/summary.txt
	stop $srv
}
SRV8=0-3,8-11
for size in 8192 20480; do
	count=$(( 2147483648 / size )); [ $count -gt 200000 ] && count=200000
	nd=$(( count / 1000 )); [ $nd -lt 2 ] && nd=2
	rm -rf /mnt/fitrand/z; $D/bin/gen -root /mnt/fitrand/z -dirs $nd -files 1000 -size $size -workers 16
	ENVS=(-e ND=$nd -e PREFIX=/z)
	for r in $(seq $ROUNDS); do
		for srv in fit-iso fit-final fit-final-dt; do measure "size-$size.r$r" $srv $SRV8 wrk -t8 -c256 -s /s/sweep.lua http://127.0.0.1:8082/; done
	done
done
echo "done $(date +%T)" | tee -a $OUT/summary.txt
