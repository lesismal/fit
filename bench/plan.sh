#!/bin/sh
# plan.sh runs the load test plan of bench/README.md ("The load test plan")
# on the benchmark host: seven workloads, each at the levels of load it is
# measured at, one fitbench run and one JSON for each. It runs on the host
# itself, from a directory holding bin/ (fitbench, fit, idleconn for
# linux/amd64) and bench/ (this repository's): start it under nohup with
# < /dev/null.
#
# The plan's sizes are derived from the machine by fitbench plan
# (bench/README.md, "Deriving the plan"): the CPU split by physical core,
# the levels of load, W5's idle connections, the aligned settings; W6's
# rates are calibrated from W1's results at the level near 500. Any flag
# after DATE is fitbench plan's, and sets what it names in place of what
# would be derived (-server-cpus, -backend-cpus, -client-cpus, -levels,
# -top, -w5-idle, -w6-rates, -backend-conns, -backlog, -duration, -warmup).
#
#   bench/plan.sh DATE [FLAGS]     results in bench/results/plan-DATE-<workload>-<level>.json
#   bench/plan.sh -n DATE [FLAGS]  print the derived plan and run nothing
#
# Each run is a 5s warm-up and 15s measured unless set, about 24s a run in
# all. Progress is in plan.log, each fitbench run's output in
# <workload>-<level>.log, the plan derived in plan-derived.json and
# plan.txt; done is touched at the end.
set -eu

dry=false
if [ "${1:-}" = -n ]; then
	dry=true
	shift
fi
[ $# -ge 1 ] || { echo "usage: bench/plan.sh [-n] DATE [fitbench plan flags]" >&2; exit 2; }
date=$1
shift
here=$(pwd)
if $dry; then
	exec ./bin/fitbench plan "$@"
fi
for f in bench/results/plan-"$date"-*.json; do
	if [ -e "$f" ]; then
		echo "plan.sh: $f is there already; give another DATE" >&2
		exit 1
	fi
done

./bin/fitbench plan "$@" > plan.txt
./bin/fitbench plan -format json "$@" > plan-derived.json
eval "$(./bin/fitbench plan -format sh "$@")"
echo "start $(date -u +%FT%TZ) runs=$PLAN_RUNS estimated=$PLAN_ESTIMATED" > plan.log

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
	# shellcheck disable=SC2086 # the derived flags are split on purpose
	./bin/fitbench -root "$here" -bin "$here/bin" -idle "" -duration "$PLAN_DURATION" -warmup "$PLAN_WARMUP" \
		-label "$name" $PLAN_CPUS $PLAN_ALIGNED -derived "$here/plan-derived.json" \
		-servers "$servers" -scenarios "$scenarios" -protocols "$protocols" \
		-connections "$c" -h2-connections "$hc" -h2-streams "$hm" "$@" \
		-out "bench/results/plan-$date-$name.json" > "$name.log" 2>&1 || status=$?
	echo "$name exit $status $(date -u +%FT%TZ)" >> plan.log
}

all=nginx,caddy,traefik,envoy,fit

# A level is IN-FLIGHT:H2-CONNS:H2-STREAMS. Over HTTP/1.1 it is wrk's
# connections, of which it opens a multiple of its threads (100 is 96, 500
# is 498 with 6); over h2c, h2 and HTTP/3 it is h2load's connections times
# streams, 25 streams a connection, or near a square below that, every
# connection count at least the load generators' threads.
c() { echo "${1%%:*}"; }
hc() { rest=${1#*:}; echo "${rest%%:*}"; }
hm() { echo "${1##*:}"; }

# W0, the raw ceiling: the fixed response, at every level.
for lv in $PLAN_LEVELS; do
	run "w0-fixed-$(c "$lv")" nginx,caddy,envoy,fit fixed http/1.1,h2c,h2,h3 "$(c "$lv")" "$(hc "$lv")" "$(hm "$lv")"
done

# W1, the API gateway, at every level.
for lv in $PLAN_LEVELS; do
	run "w1-api-$(c "$lv")" "$all" api http/1.1,h2 "$(c "$lv")" "$(hc "$lv")" "$(hm "$lv")"
done

# W2, the website, served and behind a cache, over HTTPS.
for lv in $PLAN_W2_LEVELS; do
	run "w2-site-$(c "$lv")" nginx,caddy,fit site,edge https/1.1,h2,h3 "$(c "$lv")" "$(hc "$lv")" "$(hm "$lv")"
done

# W3, connection churn: a new TLS connection every request, and every ten.
for n in $PLAN_W3_CONNS; do
	run "w3-churn-$n" nginx,caddy,envoy,fit churn-1,churn-10 https/1.1 "$n" 10 1
done

# W4, 1 MiB downloads and uploads.
lv=$PLAN_W4_LEVEL
run "w4-large-$(c "$lv")" "$all" download,proxy-download,proxy-upload http/1.1,h2 "$(c "$lv")" "$(hc "$lv")" "$(hm "$lv")"

# W5, the API gateway with idle TLS connections held open.
lv=$PLAN_MID_LEVEL
run "w5-idle-$(c "$lv")" "$all" api http/1.1,h2 "$(c "$lv")" "$(hc "$lv")" "$(hm "$lv")" -hold-idle "$PLAN_W5_IDLE"

# W6, the API gateway's latency at two fixed rates, calibrated from W1 at the
# same level: the low rate about half the slowest server's ceiling, so every
# server runs below it; the high one past the slower servers' ceilings and
# within the faster ones'.
if [ "$PLAN_W6_SET" != true ]; then
	calibration=bench/results/plan-$date-w1-api-$PLAN_MID.json
	if [ -f "$calibration" ]; then
		./bin/fitbench plan -calibration "$calibration" "$@" > plan.txt
		./bin/fitbench plan -format json -calibration "$calibration" "$@" > plan-derived.json
		eval "$(./bin/fitbench plan -format sh -calibration "$calibration" "$@")"
	fi
	echo "w6 rates $PLAN_W6_RATES" >> plan.log
fi
for rate in $PLAN_W6_RATES; do
	run "w6-rate-$rate" "$all" api http/1.1,h2 "$(c "$lv")" "$(hc "$lv")" "$(hm "$lv")" -rate "$rate"
done

touch done
