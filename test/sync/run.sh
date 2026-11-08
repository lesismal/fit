#!/bin/sh
# Runs sync's interop tests against real servers in Docker: Elasticsearch 8
# and OpenSearch 2, each a single node with security on (TLS with a CA of its
# own, basic authentication) and a write thread pool of one thread and a
# queue of one, so that parallel bulks get 429s; ClickHouse, with a user
# and password, over HTTP; MySQL 5.7, 8.0 and 8.4 and MariaDB 10.11 and 11
# (test/sync/mysql.sh, which says what they read); and PostgreSQL 13 to 17
# with TLS, SCRAM and logical replication (test/sync/postgres.sh); Redis 7,
# with an ACL user and a TLS port of a CA made here, and Valkey 8, with the
# same user; and MinIO and SeaweedFS's S3 gateway, each with an access key
# of its own. Kafka (three KRaft brokers, with SASL and TLS listeners) and
# Redpanda are started by test/sync/kafka.sh, which describes them. The
# containers are removed at the end; KEEP=1 keeps them, and a second run
# reuses them.
#
#   test/sync/run.sh [go test flags...]
#
# The tests read FIT_SYNC_ES_URL, FIT_SYNC_ES_CA, FIT_SYNC_ES_PASSWORD and
# FIT_SYNC_OS_URL, FIT_SYNC_OS_CA, FIT_SYNC_OS_PASSWORD, and FIT_SYNC_CH_URL,
# FIT_SYNC_CH_USER, FIT_SYNC_CH_PASSWORD, FIT_SYNC_PG, FIT_SYNC_PG_CA_DIR,
# FIT_SYNC_PG_USER, FIT_SYNC_PG_PASSWORD, and FIT_SYNC_REDIS_ADDR,
# FIT_SYNC_REDIS_TLS_ADDR, FIT_SYNC_REDIS_CA, FIT_SYNC_VALKEY_ADDR,
# FIT_SYNC_REDIS_USER, FIT_SYNC_REDIS_PASSWORD, and FIT_SYNC_MINIO_URL,
# FIT_SYNC_MINIO_ACCESS_KEY, FIT_SYNC_MINIO_SECRET_KEY and the same of
# FIT_SYNC_SEAWEED_, and the FIT_SYNC_KAFKA_* and FIT_SYNC_REDPANDA_* that
# test/sync/kafka.sh prints; without them they
# skip, unless FIT_SYNC_INTEROP=required, which this script sets. The
# source's snapshot test reads FIT_SYNC_SOURCE_DOCS documents, 100,000 when
# unset (CI); the plan's million is
#
#   FIT_SYNC_SOURCE_DOCS=1000000 test/sync/run.sh -run SourceSnapshot -v
#
# MongoDB 6, 7 and 8 run as replica sets of three members each, with
# authentication and TLS preferred (test/sync/mongo.sh), on ports
# MONGO_PORT+61..63, +71..73 and +81..83 (29661-29683 by default); the
# tests read FIT_SYNC_MONGO_6, _7, _8 (connection strings) and
# FIT_SYNC_MONGO_CA.
set -eu
cd "$(dirname "$0")/../.."
. test/sync/mongo.sh

ES_IMAGE=${ES_IMAGE:-docker.elastic.co/elasticsearch/elasticsearch:8.19.3}
OS_IMAGE=${OS_IMAGE:-opensearchproject/opensearch:2.19.3}
ES_PORT=${ES_PORT:-29200}
OS_PORT=${OS_PORT:-29201}
CH_IMAGE=${CH_IMAGE:-clickhouse/clickhouse-server:25.8}
CH_PORT=${CH_PORT:-29202}
REDIS_IMAGE=${REDIS_IMAGE:-redis:7-alpine}
VALKEY_IMAGE=${VALKEY_IMAGE:-valkey/valkey:8-alpine}
REDIS_PORT=${REDIS_PORT:-29386}
REDIS_TLS_PORT=${REDIS_TLS_PORT:-29387}
VALKEY_PORT=${VALKEY_PORT:-29388}
MINIO_IMAGE=${MINIO_IMAGE:-pgsty/minio:RELEASE.2026-08-04T00-00-00Z}
MINIO_PORT=${MINIO_PORT:-29215}
SEAWEED_IMAGE=${SEAWEED_IMAGE:-chrislusf/seaweedfs:4.50}
SEAWEED_PORT=${SEAWEED_PORT:-29216}
PASSWORD='Fit-sync-1nterop!'
CERTS=$(mktemp -d)
# mktemp makes the directory 0700: a server in a container, running as its
# own user, could not read the certificates bind-mounted from it on Linux.
chmod 755 "$CERTS"
. test/sync/postgres.sh
PG_CA_DIR=$CERTS

. test/sync/mysql.sh

cleanup() {
	mysql_cleanup
	rm -rf "$CERTS"
	if [ "${KEEP:-}" != 1 ]; then
		docker rm -f fit-sync-es fit-sync-os fit-sync-ch fit-sync-redis fit-sync-valkey fit-sync-minio fit-sync-seaweed $(pg_names) >/dev/null 2>&1 || true
		rm_mongos fit-sync
		test/sync/kafka.sh down
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

# Redis's TLS port has a certificate for localhost of a CA made here; a
# second run with KEEP=1 makes them again, and the container anew.
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 2 \
	-subj /CN=fit-sync-test-ca -keyout "$CERTS/redis-ca.key" -out "$CERTS/redis-ca.pem" 2>/dev/null
openssl req -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -subj /CN=localhost \
	-keyout "$CERTS/redis.key" -out "$CERTS/redis.csr" 2>/dev/null
printf 'subjectAltName=DNS:localhost,IP:127.0.0.1\n' >"$CERTS/redis.ext"
openssl x509 -req -in "$CERTS/redis.csr" -CA "$CERTS/redis-ca.pem" -CAkey "$CERTS/redis-ca.key" -CAcreateserial \
	-days 2 -extfile "$CERTS/redis.ext" -out "$CERTS/redis.pem" 2>/dev/null
chmod 644 "$CERTS"/redis*
docker rm -f fit-sync-redis >/dev/null 2>&1 || true
docker run -d --name fit-sync-redis -p "127.0.0.1:$REDIS_PORT:6379" -p "127.0.0.1:$REDIS_TLS_PORT:6380" \
	-v "$CERTS:/certs:ro" "$REDIS_IMAGE" redis-server --port 6379 --tls-port 6380 \
	--tls-cert-file /certs/redis.pem --tls-key-file /certs/redis.key --tls-ca-cert-file /certs/redis-ca.pem \
	--tls-auth-clients no --user fit on ">$PASSWORD" '~*' '&*' '+@all' >/dev/null
if ! docker inspect fit-sync-valkey >/dev/null 2>&1; then
	docker run -d --name fit-sync-valkey -p "127.0.0.1:$VALKEY_PORT:6379" "$VALKEY_IMAGE" \
		valkey-server --user fit on ">$PASSWORD" '~*' '&*' '+@all' >/dev/null
fi

if ! docker inspect fit-sync-minio >/dev/null 2>&1; then
	docker run -d --name fit-sync-minio -p "127.0.0.1:$MINIO_PORT:9000" \
		-e MINIO_ROOT_USER=fitminio -e MINIO_ROOT_PASSWORD="$PASSWORD" \
		"$MINIO_IMAGE" server /data >/dev/null
fi
# SeaweedFS reads its S3 identities from a file, copied in before it starts.
if ! docker inspect fit-sync-seaweed >/dev/null 2>&1; then
	printf '{"identities":[{"name":"fit","credentials":[{"accessKey":"fitseaweed","secretKey":"%s"}],"actions":["Admin","Read","Write","List","Tagging"]}]}' "$PASSWORD" >"$CERTS/s3.json"
	docker create --name fit-sync-seaweed -p "127.0.0.1:$SEAWEED_PORT:8333" \
		"$SEAWEED_IMAGE" server -s3 -s3.config=/etc/fit-s3.json -dir=/data >/dev/null
	docker cp "$CERTS/s3.json" fit-sync-seaweed:/etc/fit-s3.json
	docker start fit-sync-seaweed >/dev/null
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
mysql_start

pg_start

# The sets' certificates are made afresh for each run; a kept container
# holds the ones it was started with, so it is made again too.
MONGO_PORT=${MONGO_PORT:-29600}
mkdir -p "$CERTS/mongo"
make_mongo_certs "$CERTS/mongo"
rm_mongos fit-sync
MONGO_ENV=$(start_mongos fit-sync "$MONGO_PORT" "$CERTS/mongo")
set -f
# shellcheck disable=SC2086 # one variable a line, no spaces in them
export $MONGO_ENV
set +f

for c in fit-sync-redis fit-sync-valkey; do
	i=0
	until docker exec "$c" sh -c 'redis-cli ping 2>/dev/null || valkey-cli ping' 2>/dev/null | grep -q PONG; do
		i=$((i + 1))
		if [ $i -gt 60 ]; then
			echo "$c did not start:" >&2
			docker logs --tail 50 "$c" >&2
			exit 1
		fi
		sleep 1
	done
done

# until_up name url: until url answers at all (an S3 server answers 403 to
# a request with no signature).
until_up() {
	i=0
	until curl -sS -o /dev/null "$2" 2>/dev/null; do
		i=$((i + 1))
		if [ $i -gt 120 ]; then
			echo "$1 did not start:" >&2
			docker logs --tail 50 "$1" >&2
			exit 1
		fi
		sleep 1
	done
}
until_up fit-sync-minio "http://localhost:$MINIO_PORT/minio/health/ready"
until_up fit-sync-seaweed "http://localhost:$SEAWEED_PORT/"

eval "$(test/sync/kafka.sh up)"

FIT_SYNC_INTEROP=required \
	FIT_SYNC_ES_URL="https://localhost:$ES_PORT" FIT_SYNC_ES_CA="$CERTS/es-ca.pem" FIT_SYNC_ES_PASSWORD="$PASSWORD" \
	FIT_SYNC_OS_URL="https://localhost:$OS_PORT" FIT_SYNC_OS_CA="$CERTS/os-ca.pem" FIT_SYNC_OS_PASSWORD="$PASSWORD" \
	FIT_SYNC_CH_URL="http://localhost:$CH_PORT" FIT_SYNC_CH_USER=fit FIT_SYNC_CH_PASSWORD="$PASSWORD" \
	FIT_SYNC_REDIS_ADDR="127.0.0.1:$REDIS_PORT" FIT_SYNC_REDIS_TLS_ADDR="127.0.0.1:$REDIS_TLS_PORT" \
	FIT_SYNC_REDIS_CA="$CERTS/redis-ca.pem" FIT_SYNC_VALKEY_ADDR="127.0.0.1:$VALKEY_PORT" \
	FIT_SYNC_REDIS_USER=fit FIT_SYNC_REDIS_PASSWORD="$PASSWORD" \
	FIT_SYNC_MINIO_URL="http://localhost:$MINIO_PORT" FIT_SYNC_MINIO_ACCESS_KEY=fitminio FIT_SYNC_MINIO_SECRET_KEY="$PASSWORD" \
	FIT_SYNC_SEAWEED_URL="http://localhost:$SEAWEED_PORT" FIT_SYNC_SEAWEED_ACCESS_KEY=fitseaweed FIT_SYNC_SEAWEED_SECRET_KEY="$PASSWORD" \
	env $(pg_env) go test -race -count=1 "$@" ./test/sync ./sync/mysql/binlog
