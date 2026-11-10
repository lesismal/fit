#!/bin/sh
# plan.sh runs the sync benchmark's plan (bench/sync/README.md, "The plan"):
# every scenario, and the files scenario at two line sizes and two file
# counts, each ROUNDS times (3), fit and its rivals alternating, through
# run.sh, and renders the scored report:
#
#   bench/sync/plan.sh [-n ROUNDS] DATE
#
# The results go in bench/results/sync-DATE: a results file for each item
# (s1-files-128b-1f.json ... s10-mqtt-influxdb.json) with its folder of
# rounds, profiles and logs; tables.md, every item's table with its weight
# and scores (benchreport -plan); scores.md, the ranking (benchreport
# -scores). Progress is in plan.log there, each item's output in
# <item>.log; done is touched at the end. run.sh's environment (BIN, CACHE,
# MOVER_CORES, SIZE, ...) passes through; with BENCHREPORT, a benchreport
# binary, the report is rendered without Go.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
rounds=3
if [ "${1:-}" = -n ]; then
	rounds=$2
	shift 2
fi
[ $# -eq 1 ] || {
	echo "usage: bench/sync/plan.sh [-n ROUNDS] DATE" >&2
	exit 2
}
results=$root/bench/results/sync-$1
if [ -e "$results" ]; then
	echo "plan.sh: $results is there already; give another DATE" >&2
	exit 1
fi
mkdir -p "$results"
echo "start $(date -u +%FT%TZ) rounds=$rounds" >"$results/plan.log"

# item NAME SCENARIO [VAR=VALUE...]: one item of the plan.
item() {
	n=$1 s=$2
	shift 2
	echo "$n start $(date -u +%FT%TZ)" >>"$results/plan.log"
	status=0
	env "$@" sh "$here/run.sh" -n "$rounds" -o "$results" -name "$n" "$s" >"$results/$n.log" 2>&1 || status=$?
	echo "$n exit $status $(date -u +%FT%TZ)" >>"$results/plan.log"
}

# S1, files: short and long lines, one file and sixteen.
item s1-files-128b-1f files LINE_BYTES=128 FILES=1
item s1-files-128b-16f files LINE_BYTES=128 FILES=16
item s1-files-1k-1f files LINE_BYTES=1024 FILES=1 RECORDS="${FILES_1K_RECORDS:-1000000}"
item s1-files-1k-16f files LINE_BYTES=1024 FILES=16 RECORDS="${FILES_1K_RECORDS:-1000000}"
# S2-S4, streams; S5, a batch copy.
item s2-mysql-es mysql-es
item s3-mysql-kafka mysql-kafka
item s4-pg-kafka pg-kafka
item s5-mysql-batch mysql-batch
# S6, files to Loki; S7-S9, the second round's sources (I222) into Kafka.
item s6-files-loki files-loki
item s7-rabbitmq-kafka rabbitmq-kafka
item s8-mqtt-kafka mqtt-kafka
item s9-mssql-kafka mssql-kafka
# S10, MQTT's readings into InfluxDB, against Telegraf.
item s10-mqtt-influxdb mqtt-influxdb

report() {
	if [ -n "${BENCHREPORT:-}" ]; then
		"$BENCHREPORT" "$@"
	else
		(cd "$root" && go run ./bench/cmd/benchreport "$@")
	fi
}
: >"$results/tables.md"
# In the plan's order: s10 after s9, not after s1.
for f in $(cd "$results" && ls s*.json | sort -V); do
	report -in "$results/$f" -plan "$results" >>"$results/tables.md" || true
done
report -scores "$results" >"$results/scores.md" || true
echo "end $(date -u +%FT%TZ)" >>"$results/plan.log"
touch "$results/done"
