#!/bin/sh
# Runs sync's interop tests against real servers in Docker: Elasticsearch 8
# and OpenSearch 2, each a single node with security on (TLS with a CA of its
# own, basic authentication) and a write thread pool of one thread and a
# queue of one, so that parallel bulks get 429s; and ClickHouse, with a user
# and password, over HTTP. The containers are removed
# at the end; KEEP=1 keeps them, and a second run reuses them.
#
#   test/sync/run.sh [go test flags...]
#
# The tests read FIT_SYNC_ES_URL, FIT_SYNC_ES_CA, FIT_SYNC_ES_PASSWORD and
# FIT_SYNC_OS_URL, FIT_SYNC_OS_CA, FIT_SYNC_OS_PASSWORD, and FIT_SYNC_CH_URL,
# FIT_SYNC_CH_USER, FIT_SYNC_CH_PASSWORD; without them they
# skip, unless FIT_SYNC_INTEROP=required, which this script sets. The
# source's snapshot test reads FIT_SYNC_SOURCE_DOCS documents, 100,000 when
# unset (CI); the plan's million is
#
#   FIT_SYNC_SOURCE_DOCS=1000000 test/sync/run.sh -run SourceSnapshot -v
set -eu
cd "$(dirname "$0")/../.."

ES_IMAGE=${ES_IMAGE:-docker.elastic.co/elasticsearch/elasticsearch:8.19.3}
OS_IMAGE=${OS_IMAGE:-opensearchproject/opensearch:2.19.3}
ES_PORT=${ES_PORT:-29200}
OS_PORT=${OS_PORT:-29201}
CH_IMAGE=${CH_IMAGE:-clickhouse/clickhouse-server:25.8}
CH_PORT=${CH_PORT:-29202}
PASSWORD='Fit-sync-1nterop!'
CERTS=$(mktemp -d)

cleanup() {
	rm -rf "$CERTS"
	if [ "${KEEP:-}" != 1 ]; then
		docker rm -f fit-sync-es fit-sync-os fit-sync-ch >/dev/null 2>&1 || true
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

if ! docker inspect fit-sync-ch >/dev/null 2>&1; then
	docker run -d --name fit-sync-ch -p "127.0.0.1:$CH_PORT:8123" \
		-e CLICKHOUSE_USER=fit -e CLICKHOUSE_PASSWORD="$PASSWORD" \
		--ulimit nofile=262144:262144 "$CH_IMAGE" >/dev/null
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
i=0
until curl -fsS -u "fit:$PASSWORD" "http://localhost:$CH_PORT/?query=SELECT%201" >/dev/null 2>&1; do
	i=$((i + 1))
	if [ $i -gt 120 ]; then
		echo "fit-sync-ch did not start:" >&2
		docker logs --tail 50 fit-sync-ch >&2
		exit 1
	fi
	sleep 1
done

FIT_SYNC_INTEROP=required \
	FIT_SYNC_ES_URL="https://localhost:$ES_PORT" FIT_SYNC_ES_CA="$CERTS/es-ca.pem" FIT_SYNC_ES_PASSWORD="$PASSWORD" \
	FIT_SYNC_OS_URL="https://localhost:$OS_PORT" FIT_SYNC_OS_CA="$CERTS/os-ca.pem" FIT_SYNC_OS_PASSWORD="$PASSWORD" \
	FIT_SYNC_CH_URL="http://localhost:$CH_PORT" FIT_SYNC_CH_USER=fit FIT_SYNC_CH_PASSWORD="$PASSWORD" \
	go test -race -count=1 "$@" ./test/sync
