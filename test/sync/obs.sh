#!/bin/sh
# Starts the time series and vector databases sync's sinks write to, in
# Docker, and runs their interop tests (test/sync/tsdb_test.go and
# vector_test.go): InfluxDB 2 (an org and bucket fit, a token), InfluxDB 3
# Core (no authentication, objects in memory), TDengine 3 (taosAdapter's
# REST port), Qdrant 1.x, and Milvus 2.5 standalone (its etcd embedded,
# local storage, authentication off: any token is taken). The containers
# are removed at the end; KEEP=1 keeps them, and a second run reuses them.
#
#   test/sync/obs.sh [go test flags...]
#
# The tests read FIT_SYNC_INFLUX2_URL, FIT_SYNC_INFLUX2_TOKEN,
# FIT_SYNC_INFLUX3_URL, FIT_SYNC_TDENGINE_URL, FIT_SYNC_TDENGINE_PASSWORD,
# FIT_SYNC_QDRANT_URL, FIT_SYNC_MILVUS_URL and FIT_SYNC_MILVUS_TOKEN; without
# them they skip. FIT_SYNC_OBS, which this script sets, with
# FIT_SYNC_INTEROP=required makes a missing one fail its test instead.
#
# OBS_PREFIX (fit-sync-obs) names the containers, and INFLUX2_PORT (28086),
# INFLUX3_PORT (28181), TDENGINE_PORT (26041), QDRANT_PORT (26333) and
# MILVUS_PORT (29530) are their ports on the host.
set -eu
cd "$(dirname "$0")/../.."

P=${OBS_PREFIX:-fit-sync-obs}
INFLUX2_PORT=${INFLUX2_PORT:-28086}
INFLUX3_PORT=${INFLUX3_PORT:-28181}
TDENGINE_PORT=${TDENGINE_PORT:-26041}
QDRANT_PORT=${QDRANT_PORT:-26333}
MILVUS_PORT=${MILVUS_PORT:-29530}
INFLUX2_IMAGE=${INFLUX2_IMAGE:-influxdb:2.7}
INFLUX3_IMAGE=${INFLUX3_IMAGE:-influxdb:3-core}
TDENGINE_IMAGE=${TDENGINE_IMAGE:-tdengine/tdengine:3.3.6.0}
QDRANT_IMAGE=${QDRANT_IMAGE:-qdrant/qdrant:v1.15.4}
MILVUS_IMAGE=${MILVUS_IMAGE:-milvusdb/milvus:v2.5.17}

cleanup() {
	if [ "${KEEP:-0}" != 1 ]; then
		docker rm -f "$P-influx2" "$P-influx3" "$P-tdengine" "$P-qdrant" "$P-milvus" >/dev/null 2>&1 || true
	fi
}
trap cleanup EXIT

running() { [ "$(docker inspect -f '{{.State.Running}}' "$1" 2>/dev/null)" = true ]; }

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
wait_for influx2 curl -sf "http://127.0.0.1:$INFLUX2_PORT/health"
wait_for influx3 curl -sf "http://127.0.0.1:$INFLUX3_PORT/health"
wait_for tdengine curl -sf -u root:taosdata -d 'SELECT SERVER_VERSION()' "http://127.0.0.1:$TDENGINE_PORT/rest/sql"
wait_for qdrant curl -sf "http://127.0.0.1:$QDRANT_PORT/readyz"
wait_for milvus curl -sf -X POST -H 'Content-Type: application/json' -d '{}' "http://127.0.0.1:$MILVUS_PORT/v2/vectordb/collections/list"

export FIT_SYNC_OBS=1 FIT_SYNC_INTEROP=required
export FIT_SYNC_INFLUX2_URL="http://127.0.0.1:$INFLUX2_PORT" FIT_SYNC_INFLUX2_TOKEN=fit-sync-token
export FIT_SYNC_INFLUX3_URL="http://127.0.0.1:$INFLUX3_PORT"
export FIT_SYNC_TDENGINE_URL="http://127.0.0.1:$TDENGINE_PORT" FIT_SYNC_TDENGINE_PASSWORD=taosdata
export FIT_SYNC_QDRANT_URL="http://127.0.0.1:$QDRANT_PORT"
export FIT_SYNC_MILVUS_URL="http://127.0.0.1:$MILVUS_PORT" FIT_SYNC_MILVUS_TOKEN=root:Milvus
go test -count=1 -run '^Test(InfluxDB|TDengine|Qdrant|Milvus|Embed)' "$@" ./test/sync
