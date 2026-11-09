#!/bin/sh
# Starts the observability, time series and vector databases sync's sinks
# write to, in Docker, and runs their interop tests (test/sync/obs_test.go,
# tsdb_test.go and vector_test.go): Grafana Loki 3.5 (its single-binary
# config, no authentication), the OpenTelemetry Collector (contrib, OTLP/HTTP
# in, every log record written as OTLP JSON to a file the tests read back,
# test/sync/otelcol/config.yaml), InfluxDB 2 (an org and bucket fit, a
# token), InfluxDB 3 Core (no authentication, objects in memory), TDengine 3
# (taosAdapter's REST port), Qdrant 1.x, and Milvus 2.5 standalone (its etcd
# embedded, local storage, authentication off: any token is taken).
#
#   test/sync/obs.sh [go test flags...]   start (or reuse) them, run the
#                                         tests, remove them (KEEP=1 keeps)
#   eval "$(test/sync/obs.sh up)"         start (or reuse) them, print the env
#   test/sync/obs.sh down                 remove them
#
# The tests read FIT_SYNC_LOKI_URL, FIT_SYNC_OTLP_URL, FIT_SYNC_OTLP_FILE,
# FIT_SYNC_INFLUX2_URL, FIT_SYNC_INFLUX2_TOKEN, FIT_SYNC_INFLUX3_URL,
# FIT_SYNC_TDENGINE_URL, FIT_SYNC_TDENGINE_PASSWORD, FIT_SYNC_QDRANT_URL,
# FIT_SYNC_MILVUS_URL and FIT_SYNC_MILVUS_TOKEN; without them they skip.
# FIT_SYNC_OBS, which this script sets, with FIT_SYNC_INTEROP=required makes
# a missing one fail its test instead.
#
# OBS_PREFIX (fit-sync-obs) names the containers, and LOKI_PORT (23100),
# OTLP_PORT (24318), INFLUX2_PORT (28086), INFLUX3_PORT (28181),
# TDENGINE_PORT (26041), QDRANT_PORT (26333) and MILVUS_PORT (29530) are
# their ports on the host; OBS_DIR (${TMPDIR}/${OBS_PREFIX}) holds the
# Collector's file.
set -eu
cd "$(dirname "$0")/../.."

P=${OBS_PREFIX:-fit-sync-obs}
LOKI_PORT=${LOKI_PORT:-23100}
OTLP_PORT=${OTLP_PORT:-24318}
INFLUX2_PORT=${INFLUX2_PORT:-28086}
INFLUX3_PORT=${INFLUX3_PORT:-28181}
TDENGINE_PORT=${TDENGINE_PORT:-26041}
QDRANT_PORT=${QDRANT_PORT:-26333}
MILVUS_PORT=${MILVUS_PORT:-29530}
LOKI_IMAGE=${LOKI_IMAGE:-grafana/loki:3.5.5}
OTELCOL_IMAGE=${OTELCOL_IMAGE:-otel/opentelemetry-collector-contrib:0.137.0}
INFLUX2_IMAGE=${INFLUX2_IMAGE:-influxdb:2.7}
INFLUX3_IMAGE=${INFLUX3_IMAGE:-influxdb:3-core}
TDENGINE_IMAGE=${TDENGINE_IMAGE:-tdengine/tdengine:3.3.6.0}
QDRANT_IMAGE=${QDRANT_IMAGE:-qdrant/qdrant:v1.15.4}
MILVUS_IMAGE=${MILVUS_IMAGE:-milvusdb/milvus:v2.5.17}
OBS_DIR=${OBS_DIR:-${TMPDIR:-/tmp}/$P}
OBS_DIR=${OBS_DIR%/}

remove() {
	docker rm -f "$P-loki" "$P-otelcol" "$P-influx2" "$P-influx3" "$P-tdengine" "$P-qdrant" "$P-milvus" >/dev/null 2>&1 || true
	rm -rf "$OBS_DIR"
}

mode=test
case ${1:-} in
down)
	remove
	exit 0
	;;
up)
	mode=up
	shift
	;;
esac
if [ $mode = test ]; then
	cleanup() { [ "${KEEP:-0}" = 1 ] || remove; }
	trap cleanup EXIT
fi

running() { [ "$(docker inspect -f '{{.State.Running}}' "$1" 2>/dev/null)" = true ]; }

mkdir -p "$OBS_DIR"
chmod 777 "$OBS_DIR"
running "$P-loki" || docker run -d --name "$P-loki" -p "$LOKI_PORT:3100" "$LOKI_IMAGE" >/dev/null
running "$P-otelcol" || docker run -d --name "$P-otelcol" -p "$OTLP_PORT:4318" \
	-v "$PWD/test/sync/otelcol/config.yaml:/etc/otelcol/config.yaml:ro" -v "$OBS_DIR:/out" \
	"$OTELCOL_IMAGE" --config /etc/otelcol/config.yaml >/dev/null
running "$P-influx2" || docker run -d --name "$P-influx2" -p "$INFLUX2_PORT:8086" \
	-e DOCKER_INFLUXDB_INIT_MODE=setup -e DOCKER_INFLUXDB_INIT_USERNAME=fit \
	-e DOCKER_INFLUXDB_INIT_PASSWORD=fit-sync-password -e DOCKER_INFLUXDB_INIT_ORG=fit \
	-e DOCKER_INFLUXDB_INIT_BUCKET=fit -e DOCKER_INFLUXDB_INIT_ADMIN_TOKEN=fit-sync-token \
	"$INFLUX2_IMAGE" >/dev/null
running "$P-influx3" || docker run -d --name "$P-influx3" -p "$INFLUX3_PORT:8181" "$INFLUX3_IMAGE" \
	influxdb3 serve --node-id n1 --object-store memory --without-auth >/dev/null
running "$P-tdengine" || docker run -d --name "$P-tdengine" -p "$TDENGINE_PORT:6041" "$TDENGINE_IMAGE" >/dev/null
running "$P-qdrant" || docker run -d --name "$P-qdrant" -p "$QDRANT_PORT:6333" "$QDRANT_IMAGE" >/dev/null
if ! running "$P-milvus"; then
	docker rm -f "$P-milvus" >/dev/null 2>&1 || true
	# Milvus's standalone_embed.sh, as a container: etcd in the process,
	# segments on its disk.
	docker run -d --name "$P-milvus" --security-opt seccomp:unconfined -p "$MILVUS_PORT:19530" \
		-e ETCD_USE_EMBED=true -e ETCD_DATA_DIR=/var/lib/milvus/etcd -e COMMON_STORAGETYPE=local \
		"$MILVUS_IMAGE" sh -c 'printf "listen-client-urls: http://0.0.0.0:2379\nadvertise-client-urls: http://0.0.0.0:2379\n" > /milvus/configs/embedEtcd.yaml && ETCD_CONFIG_PATH=/milvus/configs/embedEtcd.yaml exec milvus run standalone' >/dev/null
fi

wait_for() {
	name=$1
	shift
	i=0
	until "$@" >/dev/null 2>&1; do
		i=$((i + 1))
		if [ $i -gt 180 ]; then
			echo "$name did not start" >&2
			docker logs --tail 50 "$P-$name" >&2 || true
			exit 1
		fi
		sleep 1
	done
}
# Loki answers /ready once its ingester has joined the ring; the Collector
# takes an empty export once its receiver listens.
wait_for loki curl -sf "http://127.0.0.1:$LOKI_PORT/ready"
wait_for otelcol curl -sf -X POST -H 'Content-Type: application/json' -d '{}' "http://127.0.0.1:$OTLP_PORT/v1/logs"
wait_for influx2 curl -sf "http://127.0.0.1:$INFLUX2_PORT/health"
wait_for influx3 curl -sf "http://127.0.0.1:$INFLUX3_PORT/health"
wait_for tdengine curl -sf -u root:taosdata -d 'SELECT SERVER_VERSION()' "http://127.0.0.1:$TDENGINE_PORT/rest/sql"
wait_for qdrant curl -sf "http://127.0.0.1:$QDRANT_PORT/readyz"
wait_for milvus curl -sf -X POST -H 'Content-Type: application/json' -d '{}' "http://127.0.0.1:$MILVUS_PORT/v2/vectordb/collections/list"

env="FIT_SYNC_LOKI_URL=http://127.0.0.1:$LOKI_PORT FIT_SYNC_OTLP_URL=http://127.0.0.1:$OTLP_PORT FIT_SYNC_OTLP_FILE=$OBS_DIR/logs.json
FIT_SYNC_INFLUX2_URL=http://127.0.0.1:$INFLUX2_PORT FIT_SYNC_INFLUX2_TOKEN=fit-sync-token
FIT_SYNC_INFLUX3_URL=http://127.0.0.1:$INFLUX3_PORT
FIT_SYNC_TDENGINE_URL=http://127.0.0.1:$TDENGINE_PORT FIT_SYNC_TDENGINE_PASSWORD=taosdata
FIT_SYNC_QDRANT_URL=http://127.0.0.1:$QDRANT_PORT
FIT_SYNC_MILVUS_URL=http://127.0.0.1:$MILVUS_PORT FIT_SYNC_MILVUS_TOKEN=root:Milvus"
if [ $mode = up ]; then
	echo "$env" | sed 's/^/export /'
	exit 0
fi
# shellcheck disable=SC2046 # the words are the variables
export FIT_SYNC_OBS=1 FIT_SYNC_INTEROP=required $(echo "$env" | tr '\n' ' ')
go test -count=1 -run '^Test(Loki|OTLP|InfluxDB|TDengine|Qdrant|Milvus|Embed)' "$@" ./test/sync
