#!/bin/sh
# Starts Grafana Loki and the OpenTelemetry Collector in Docker for sync's
# loki and otlp_logs interop tests (test/sync/obs_test.go, TestLoki* and
# TestOTLP*), and prints the environment the tests read, as `export` lines:
#
#   eval "$(test/sync/obs.sh up)"     start (or reuse) them, print the env
#   test/sync/obs.sh down             remove them
#
# Loki (LOKI_IMAGE, 3.5.5) runs its own single-binary config, without
# authentication, on LOKI_PORT (23100). The Collector (OTELCOL_IMAGE,
# contrib 0.137.0) takes OTLP/HTTP on OTLP_PORT (24318) and writes what it
# takes, as OTLP JSON, to a file in OBS_DIR (${TMPDIR}/${PREFIX}obs), which
# the tests read (test/sync/otelcol/config.yaml). The tests read
# FIT_SYNC_LOKI_URL, FIT_SYNC_OTLP_URL and FIT_SYNC_OTLP_FILE.
set -eu
cd "$(dirname "$0")/../.."

PREFIX=${PREFIX:-fit-sync-}
LOKI_IMAGE=${LOKI_IMAGE:-grafana/loki:3.5.5}
OTELCOL_IMAGE=${OTELCOL_IMAGE:-otel/opentelemetry-collector-contrib:0.137.0}
LOKI_PORT=${LOKI_PORT:-23100}
OTLP_PORT=${OTLP_PORT:-24318}
OBS_DIR=${OBS_DIR:-${TMPDIR:-/tmp}/${PREFIX}obs}

if [ "${1:-}" = down ]; then
	docker rm -f "${PREFIX}loki" "${PREFIX}otelcol" >/dev/null 2>&1 || true
	rm -rf "$OBS_DIR"
	exit 0
fi

mkdir -p "$OBS_DIR"
chmod 777 "$OBS_DIR"
up() {
	name=$1
	shift
	docker inspect "$name" >/dev/null 2>&1 && return 0
	docker run -d --name "$name" "$@" >/dev/null
}
up "${PREFIX}loki" -p "$LOKI_PORT:3100" "$LOKI_IMAGE"
up "${PREFIX}otelcol" -p "$OTLP_PORT:4318" -v "$PWD/test/sync/otelcol/config.yaml:/etc/otelcol/config.yaml:ro" \
	-v "$OBS_DIR:/out" "$OTELCOL_IMAGE" --config /etc/otelcol/config.yaml

# Loki answers /ready once its ingester has joined the ring; the Collector
# takes an empty export once its receiver listens.
i=0
until curl -fs "http://127.0.0.1:$LOKI_PORT/ready" >/dev/null 2>&1 &&
	curl -fs -X POST -H 'Content-Type: application/json' -d '{}' "http://127.0.0.1:$OTLP_PORT/v1/logs" >/dev/null 2>&1; do
	i=$((i + 1))
	if [ $i -gt 120 ]; then
		echo "obs.sh: Loki or the Collector is not ready after 2 minutes" >&2
		docker logs --tail 30 "${PREFIX}loki" >&2
		docker logs --tail 30 "${PREFIX}otelcol" >&2
		exit 1
	fi
	sleep 1
done
echo "export FIT_SYNC_LOKI_URL=http://127.0.0.1:$LOKI_PORT FIT_SYNC_OTLP_URL=http://127.0.0.1:$OTLP_PORT FIT_SYNC_OTLP_FILE=$OBS_DIR/logs.json"
