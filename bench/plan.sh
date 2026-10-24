#!/bin/sh
# plan.sh runs the load test plan of bench/README.md ("The load test plan")
# on the benchmark host: seven workloads, each at the levels of load it is
# measured at, one fitbench run and one JSON for each. It runs on the host
# itself, from a directory holding bin/ (fitbench, fit, idleconn for
# linux/amd64) and bench/ (this repository's), and takes about 1 h 35 min:
# start it under nohup with < /dev/null.
#
#   bench/plan.sh DATE        results in bench/results/plan-DATE-<workload>-<level>.json
#
# Each run is a 5s warm-up and 15s measured. Progress is in plan.log, each
# fitbench run's output in <workload>-<level>.log; done is touched at the
# end.
set -eu

[ $# -eq 1 ] || { echo "usage: bench/plan.sh DATE" >&2; exit 2; }
date=$1
here=$(pwd)
echo "start $(date -u +%FT%TZ)" > plan.log

# run NAME SERVERS SCENARIOS PROTOCOLS H1-CONNS H2-CONNS H2-STREAMS [FLAGS...]
run() {
	name=$1 servers=$2 scenarios=$3 protocols=$4 c=$5 hc=$6 hm=$7
	shift 7
	# Something on the host sets the UDP buffer limits HTTP/3 needs back to
	# 16 KiB; they are set again before each run, and recorded.
	if [ -f /etc/sysctl.d/90-fitbench-udp-buffers.conf ]; then
		sysctl -q -p /etc/sysctl.d/90-fitbench-udp-buffers.conf || true
	fi
	echo "$name start $(date -u +%FT%TZ) rmem_max=$(sysctl -n net.core.rmem_max 2>/dev/null || echo ?)" >> plan.log
	status=0
	./bin/fitbench -root "$here" -bin "$here/bin" -idle "" -duration 15s -warmup 5s -label "$name" \
		-servers "$servers" -scenarios "$scenarios" -protocols "$protocols" \
		-connections "$c" -h2-connections "$hc" -h2-streams "$hm" "$@" \
		-out "bench/results/plan-$date-$name.json" > "$name.log" 2>&1 || status=$?
	echo "$name exit $status $(date -u +%FT%TZ)" >> plan.log
}

all=nginx,caddy,traefik,envoy,fit

# W7, the raw ceiling: the fixed response, at 28 to 7,168 requests in flight.
for lv in 28:7:4 448:14:32 1792:56:32 7168:224:32; do
	c=${lv%%:*} rest=${lv#*:}
	run "w7-fixed-$c" nginx,caddy,envoy,fit fixed http/1.1,h2c,h2,h3 "$c" "${rest%%:*}" "${rest#*:}"
done

# W1, the API gateway, at 112 to 7,168 requests in flight.
for lv in 112:7:16 448:14:32 1792:56:32 7168:224:32; do
	c=${lv%%:*} rest=${lv#*:}
	run "w1-api-$c" "$all" api http/1.1,h2 "$c" "${rest%%:*}" "${rest#*:}"
done

# W2, the website, served and behind a cache, over HTTPS.
for lv in 448:14:32 1792:56:32; do
	c=${lv%%:*} rest=${lv#*:}
	run "w2-site-$c" nginx,caddy,fit site,edge https/1.1,h2,h3 "$c" "${rest%%:*}" "${rest#*:}"
done

# W3, connection churn: a new TLS connection every request, and every ten.
for c in 112 448; do
	run "w3-churn-$c" nginx,caddy,envoy,fit churn-1,churn-10 https/1.1 "$c" 7 1
done

# W4, 1 MiB downloads and uploads.
run w4-large-112 "$all" download,proxy-download,proxy-upload http/1.1,h2 112 7 16

# W5, the API gateway with 40,000 idle TLS connections held open.
run w5-idle-448 "$all" api http/1.1,h2 448 14 32 -hold-idle 40000

# W6, the API gateway's latency at 50k and 150k requests a second.
for rate in 50000 150000; do
	run "w6-rate-$rate" "$all" api http/1.1,h2 448 14 32 -rate "$rate"
done

touch done
