#!/bin/sh
# Runs sync's interop tests against real servers in Docker: Elasticsearch 8
# and OpenSearch 2, each a single node with security on (TLS with a CA of its
# own, basic authentication) and a write thread pool of one thread and a
# queue of one, so that parallel bulks get 429s. The containers are removed
# at the end; KEEP=1 keeps them, and a second run reuses them.
#
#   test/sync/run.sh [go test flags...]
#
# The tests read FIT_SYNC_ES_URL, FIT_SYNC_ES_CA, FIT_SYNC_ES_PASSWORD and
# FIT_SYNC_OS_URL, FIT_SYNC_OS_CA, FIT_SYNC_OS_PASSWORD; without them they
# skip, unless FIT_SYNC_INTEROP=required, which this script sets.
set -eu
cd "$(dirname "$0")/../.."

ES_IMAGE=${ES_IMAGE:-docker.elastic.co/elasticsearch/elasticsearch:8.19.3}
OS_IMAGE=${OS_IMAGE:-opensearchproject/opensearch:2.19.3}
ES_PORT=${ES_PORT:-29200}
OS_PORT=${OS_PORT:-29201}
PASSWORD='Fit-sync-1nterop!'
CERTS=$(mktemp -d)

cleanup() {
	rm -rf "$CERTS"
	if [ "${KEEP:-}" != 1 ]; then
		docker rm -f fit-sync-es fit-sync-os >/dev/null 2>&1 || true
	fi
}
trap cleanup EXIT

if ! docker inspect fit-sync-es >/dev/null 2>&1; then
	docker run -d --name fit-sync-es -p "127.0.0.1:$ES_PORT:9200" \
		-e discovery.type=single-node -e ELASTIC_PASSWORD="$PASSWORD" \
		-e ES_JAVA_OPTS="-Xms512m -Xmx512m" -e xpack.ml.enabled=false \
		-e thread_pool.write.size=1 -e thread_pool.write.queue_size=1 \
		-e cluster.routing.allocation.disk.threshold_enabled=false \
		"$ES_IMAGE" >/dev/null
fi
if ! docker inspect fit-sync-os >/dev/null 2>&1; then
	docker run -d --name fit-sync-os -p "127.0.0.1:$OS_PORT:9200" \
		-e discovery.type=single-node -e OPENSEARCH_INITIAL_ADMIN_PASSWORD="$PASSWORD" \
		-e OPENSEARCH_JAVA_OPTS="-Xms512m -Xmx512m" \
		-e thread_pool.write.size=1 -e thread_pool.write.queue_size=1 \
		-e cluster.routing.allocation.disk.threshold_enabled=false \
		"$OS_IMAGE" >/dev/null
fi

# wait_for name user url ca: until the cluster answers its root.
wait_for() {
	i=0
	until curl -fsS --cacert "$4" -u "$2:$PASSWORD" "$3/" >/dev/null 2>&1; do
		i=$((i + 1))
		if [ $i -gt 120 ]; then
			echo "$1 did not start:" >&2
			docker logs --tail 50 "$1" >&2
			exit 1
		fi
		sleep 2
		# The CA is written once the node has configured its security.
		docker cp "$1:$5" "$4" >/dev/null 2>&1 || true
	done
}
wait_for fit-sync-es elastic "https://localhost:$ES_PORT" "$CERTS/es-ca.pem" /usr/share/elasticsearch/config/certs/http_ca.crt
wait_for fit-sync-os admin "https://localhost:$OS_PORT" "$CERTS/os-ca.pem" /usr/share/opensearch/config/root-ca.pem

FIT_SYNC_INTEROP=required \
	FIT_SYNC_ES_URL="https://localhost:$ES_PORT" FIT_SYNC_ES_CA="$CERTS/es-ca.pem" FIT_SYNC_ES_PASSWORD="$PASSWORD" \
	FIT_SYNC_OS_URL="https://localhost:$OS_PORT" FIT_SYNC_OS_CA="$CERTS/os-ca.pem" FIT_SYNC_OS_PASSWORD="$PASSWORD" \
	go test -race -count=1 "$@" ./test/sync
