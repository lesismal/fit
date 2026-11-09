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
#
# SYNC_FAMILIES picks the servers started, and the tests run, by family
# (all of them when unset, with every test), so that CI runs the families
# as jobs of their own:
#
#   search    Elasticsearch, OpenSearch, ClickHouse, Redis, Valkey, MinIO,
#             SeaweedFS: the tests TestElasticsearch*, TestClickHouse*,
#             TestRedis*, TestS3*
#   mysql     MySQL, MariaDB and TiDB: TestMySQL*, and sync/mysql/binlog's
#   postgres  PostgreSQL 13 to 17: TestPostgres*
#   mongo     MongoDB 6, 7 and 8: TestMongo*
#   kafka     Kafka and Redpanda: TestKafka*
#   tidb      TiDB with TiCDC (test/sync/tidb.sh), on Kafka (without
#             Redpanda), and PostgreSQL 17, which the test writes into:
#             TestTiDB*
#   mq        RabbitMQ 3.13 and 4.1, NATS 2.10 and 2.11 (clusters of
#             three), Pulsar 3 and 4, Mosquitto 2 and EMQX 5
#             (test/sync/rabbitmq.sh, nats.sh, pulsar.sh, mqtt.sh):
#             TestRabbitMQ*, TestNATS*, TestPulsar*, TestMQTT*
#   mssql     SQL Server 2022 and 2019 (test/sync/mssql.sh): TestMSSQL*
#   olap      Doris 2.1 and StarRocks 3.4 (test/sync/olap.sh): TestDoris*
#   obs       Loki, the OpenTelemetry Collector, InfluxDB 2 and 3,
#             TDengine 3, Qdrant and Milvus (test/sync/obs.sh), with
#             PostgreSQL 17 and Mosquitto, which two of its tests read
#             from: TestLoki*, TestOTLP*, TestInfluxDB*, TestTDengine*,
#             TestQdrant*, TestMilvus*, TestEmbed*
#   matrix    the interoperability and recovery matrix (I192, TestMatrix,
#             test/sync/matrix_test.go): with no other family named, one
#             server of each kind (PostgreSQL 17, MySQL 8.4, MongoDB 8,
#             Elasticsearch, ClickHouse, Redis, Kafka, TiDB with TiCDC),
#             without OpenSearch, Valkey, S3, Redpanda (REDPANDA=1 adds
#             it) or TiDB alone; MATRIX_SERVERS names the families whose
#             servers start ("search mysql postgres mongo kafka tidb";
#             the sinks' four are always there), PG_VERSIONS, MYSQL_SERVERS
#             and MONGO_VERSIONS their versions, and the sources run are
#             those of its families (FIT_SYNC_MATRIX_SOURCES, when unset:
#             mq is rabbitmq, nats, pulsar and mqtt, mssql is mssql, olap
#             is doris; with one of those named, the first round's
#             sources are left out), each on one server: RabbitMQ 4.1, NATS 2.11,
#             Pulsar 4, Mosquitto, SQL Server 2022, Doris. CI runs it
#             three times: the first round's sources, mq's, and mssql's
#             and olap's. MQ_SYSTEMS (rabbitmq nats pulsar mqtt) picks mq's
#             servers, the matrix's those its sources name. Named with other families, their servers and
#             tests, whole.
#
#   SYNC_FAMILIES=postgres test/sync/run.sh -v
#   SYNC_FAMILIES=matrix test/sync/run.sh -v
#   SYNC_FAMILIES=matrix MATRIX_SERVERS="search mysql postgres mongo mq" test/sync/run.sh -v
#   SYNC_FAMILIES=matrix MATRIX_SERVERS="search mysql postgres mongo" \
#     PG_VERSIONS="13 14 15 16 17" FIT_SYNC_MATRIX=full \
#     FIT_SYNC_MATRIX_SOURCES=postgres test/sync/run.sh -v
#
# SYNC_PREFIX (fit-sync) names the containers, so that a second set can
# run beside the first with ports of its own (ES_PORT, OS_PORT, CH_PORT,
# REDIS_PORT, REDIS_TLS_PORT, VALKEY_PORT, MINIO_PORT, SEAWEED_PORT,
# MYSQL_PORT, TIDB_PORT, PG_PORT, MONGO_PORT, KAFKA_PORT, REDPANDA_PORT,
# TIDB_CDC_PORT, RABBITMQ_PORT, NATS_PORT, PULSAR_PORT, MQTT_PORT,
# MSSQL_PORT, and obs.sh's; olap's Doris takes fixed ports, so one set at a
# time).
set -eu
cd "$(dirname "$0")/../.."

SYNC_PREFIX=${SYNC_PREFIX:-fit-sync}
SYNC_FAMILIES=${SYNC_FAMILIES:-}
RUN=
LIGHT=0
if [ -z "$SYNC_FAMILIES" ]; then
	SYNC_FAMILIES="search mysql postgres mongo kafka tidb mq mssql olap obs"
else
	for f in $SYNC_FAMILIES; do
		case $f in
		search) RUN="$RUN|Elasticsearch|ClickHouse|Redis|S3" ;;
		mysql) RUN="$RUN|MySQL" ;;
		postgres) RUN="$RUN|Postgres" ;;
		mongo) RUN="$RUN|Mongo" ;;
		kafka) RUN="$RUN|Kafka" ;;
		tidb) RUN="$RUN|TiDB" ;;
		mq) RUN="$RUN|RabbitMQ|NATS|Pulsar|MQTT" ;;
		mssql) RUN="$RUN|MSSQL" ;;
		olap) RUN="$RUN|Doris" ;;
		obs) RUN="$RUN|Loki|OTLP|InfluxDB|TDengine|Qdrant|Milvus|Embed" ;;
		matrix) RUN="$RUN|Matrix" ;;
		*)
			echo "SYNC_FAMILIES: no family $f" >&2
			exit 2
			;;
		esac
	done
	RUN="^Test(${RUN#|})"
	if [ "$SYNC_FAMILIES" = matrix ]; then
		SYNC_FAMILIES=${MATRIX_SERVERS:-search mysql postgres mongo kafka tidb}
		LIGHT=1
		PG_VERSIONS=${PG_VERSIONS:-17}
		MYSQL_SERVERS=${MYSQL_SERVERS:-mysql84}
		MONGO_VERSIONS=${MONGO_VERSIONS:-8}
		TIDB=${TIDB:-0}
		# The sinks' servers are always there; one server of each source.
		for f in mongo postgres mysql search; do
			case " $SYNC_FAMILIES " in *" $f "*) ;; *) SYNC_FAMILIES="$f $SYNC_FAMILIES" ;; esac
		done
		RABBITMQ_IMAGES=${RABBITMQ_IMAGES:-4.1=rabbitmq:4.1}
		NATS_VERSIONS=${NATS_VERSIONS:-211}
		PULSAR_VERSIONS=${PULSAR_VERSIONS:-4}
		EMQX=${EMQX:-0}
		MSSQL_VERSIONS=${MSSQL_VERSIONS:-2022}
		OLAP=${OLAP:-doris}
		if [ -z "${FIT_SYNC_MATRIX_SOURCES:-}" ]; then
			# With a second round's family named, its sources alone: the
			# first round's run in the matrix of their own.
			round2=0
			for f in $SYNC_FAMILIES; do
				case $f in mq | mssql | olap) round2=1 ;; esac
			done
			for f in $SYNC_FAMILIES; do
				case $round2:$f in
				1:mysql | 1:postgres | 1:mongo | 1:kafka | 1:tidb) ;;
				*:mysql | *:postgres | *:mongo | *:kafka | *:tidb | *:mssql) FIT_SYNC_MATRIX_SOURCES="${FIT_SYNC_MATRIX_SOURCES:-} ${f#*:}" ;;
				*:mq) FIT_SYNC_MATRIX_SOURCES="${FIT_SYNC_MATRIX_SOURCES:-} rabbitmq nats pulsar mqtt" ;;
				*:olap) FIT_SYNC_MATRIX_SOURCES="${FIT_SYNC_MATRIX_SOURCES:-} doris" ;;
				esac
			done
			export FIT_SYNC_MATRIX_SOURCES
		fi
		# The queues the sources name, of mq's four.
		MQ_SYSTEMS=
		for q in rabbitmq nats pulsar mqtt; do
			case " $(echo "$FIT_SYNC_MATRIX_SOURCES" | tr ',' ' ') " in *" $q "*) MQ_SYSTEMS="$MQ_SYSTEMS $q" ;; esac
		done
	fi
fi
MQ_SYSTEMS=${MQ_SYSTEMS:-rabbitmq nats pulsar mqtt}
mq_has() {
	case " $MQ_SYSTEMS " in *" $1 "*) return 0 ;; esac
	return 1
}
export RABBITMQ_IMAGES NATS_VERSIONS PULSAR_VERSIONS EMQX MSSQL_VERSIONS OLAP
has() {
	case " $SYNC_FAMILIES " in *" $1 "*) return 0 ;; esac
	return 1
}
# The tidb family's test writes into a PostgreSQL, and two of obs's read
# from one (and from Mosquitto, below).
if { has tidb || has obs; } && ! has postgres; then
	SYNC_FAMILIES="$SYNC_FAMILIES postgres"
	PG_VERSIONS=${PG_VERSIONS:-17}
fi
MYSQL_PREFIX=${MYSQL_PREFIX:-$SYNC_PREFIX-my}
PG_PREFIX=${PG_PREFIX:-$SYNC_PREFIX-pg}
PREFIX=${PREFIX:-$SYNC_PREFIX-}
export PREFIX
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
	if has mysql; then mysql_cleanup; fi
	rm -rf "$CERTS"
	if [ "${KEEP:-}" != 1 ]; then
		for c in es os ch redis valkey minio seaweed; do
			docker rm -f "$SYNC_PREFIX-$c" >/dev/null 2>&1 || true
		done
		if has postgres; then docker rm -f $(pg_names) >/dev/null 2>&1 || true; fi
		rm_mongos "$SYNC_PREFIX"
		if has kafka || has tidb; then
			test/sync/tidb.sh down
			test/sync/kafka.sh down
		fi
		if has mq; then
			test/sync/rabbitmq.sh down
			test/sync/nats.sh down
			test/sync/pulsar.sh down
		fi
		if has mq || has obs; then test/sync/mqtt.sh down; fi
		if has mssql; then test/sync/mssql.sh down; fi
		if has olap; then test/sync/olap.sh down; fi
		if has obs; then test/sync/obs.sh down; fi
	fi
}
trap cleanup EXIT

# search_start starts the search family's servers; search_wait waits for
# them and exports their environment. With LIGHT=1 (the matrix's servers),
# only Elasticsearch, ClickHouse and Redis.
search_start() {
	if ! docker inspect "$SYNC_PREFIX-es" >/dev/null 2>&1; then
		docker run -d --name "$SYNC_PREFIX-es" -p "127.0.0.1:$ES_PORT:9200" \
			-e discovery.type=single-node -e ELASTIC_PASSWORD="$PASSWORD" \
			-e ES_JAVA_OPTS="-Xms512m -Xmx512m" -e xpack.ml.enabled=false \
			-e thread_pool.write.size=1 -e thread_pool.write.queue_size=1 \
			-e cluster.routing.allocation.disk.threshold_enabled=false \
			"$ES_IMAGE" >/dev/null
	fi
	if [ "$LIGHT" != 1 ] && ! docker inspect "$SYNC_PREFIX-os" >/dev/null 2>&1; then
		docker run -d --name "$SYNC_PREFIX-os" -p "127.0.0.1:$OS_PORT:9200" \
			-e discovery.type=single-node -e OPENSEARCH_INITIAL_ADMIN_PASSWORD="$PASSWORD" \
			-e OPENSEARCH_JAVA_OPTS="-Xms512m -Xmx512m" \
			-e thread_pool.write.size=1 -e thread_pool.write.queue_size=1 \
			-e cluster.routing.allocation.disk.threshold_enabled=false \
			"$OS_IMAGE" >/dev/null
	fi

	if ! docker inspect "$SYNC_PREFIX-ch" >/dev/null 2>&1; then
		docker run -d --name "$SYNC_PREFIX-ch" -p "127.0.0.1:$CH_PORT:8123" \
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
	docker rm -f "$SYNC_PREFIX-redis" >/dev/null 2>&1 || true
	docker run -d --name "$SYNC_PREFIX-redis" -p "127.0.0.1:$REDIS_PORT:6379" -p "127.0.0.1:$REDIS_TLS_PORT:6380" \
		-v "$CERTS:/certs:ro" "$REDIS_IMAGE" redis-server --port 6379 --tls-port 6380 \
		--tls-cert-file /certs/redis.pem --tls-key-file /certs/redis.key --tls-ca-cert-file /certs/redis-ca.pem \
		--tls-auth-clients no --user fit on ">$PASSWORD" '~*' '&*' '+@all' >/dev/null
	[ "$LIGHT" = 1 ] && return 0
	if ! docker inspect "$SYNC_PREFIX-valkey" >/dev/null 2>&1; then
		docker run -d --name "$SYNC_PREFIX-valkey" -p "127.0.0.1:$VALKEY_PORT:6379" "$VALKEY_IMAGE" \
			valkey-server --user fit on ">$PASSWORD" '~*' '&*' '+@all' >/dev/null
	fi

	if ! docker inspect "$SYNC_PREFIX-minio" >/dev/null 2>&1; then
		docker run -d --name "$SYNC_PREFIX-minio" -p "127.0.0.1:$MINIO_PORT:9000" \
			-e MINIO_ROOT_USER=fitminio -e MINIO_ROOT_PASSWORD="$PASSWORD" \
			"$MINIO_IMAGE" server /data >/dev/null
	fi
	# SeaweedFS reads its S3 identities from a file, copied in before it starts.
	if ! docker inspect "$SYNC_PREFIX-seaweed" >/dev/null 2>&1; then
		printf '{"identities":[{"name":"fit","credentials":[{"accessKey":"fitseaweed","secretKey":"%s"}],"actions":["Admin","Read","Write","List","Tagging"]}]}' "$PASSWORD" >"$CERTS/s3.json"
		docker create --name "$SYNC_PREFIX-seaweed" -p "127.0.0.1:$SEAWEED_PORT:8333" \
			"$SEAWEED_IMAGE" server -s3 -s3.config=/etc/fit-s3.json -dir=/data >/dev/null
		docker cp "$CERTS/s3.json" "$SYNC_PREFIX-seaweed:/etc/fit-s3.json"
		docker start "$SYNC_PREFIX-seaweed" >/dev/null
	fi
}

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

search_wait() {
	wait_for "$SYNC_PREFIX-es" elastic "https://localhost:$ES_PORT" "$CERTS/es-ca.pem" /usr/share/elasticsearch/config/certs/http_ca.crt
	export FIT_SYNC_ES_URL="https://localhost:$ES_PORT" FIT_SYNC_ES_CA="$CERTS/es-ca.pem" FIT_SYNC_ES_PASSWORD="$PASSWORD"
	if [ "$LIGHT" != 1 ]; then
		wait_for "$SYNC_PREFIX-os" admin "https://localhost:$OS_PORT" "$CERTS/os-ca.pem" /usr/share/opensearch/config/root-ca.pem
		export FIT_SYNC_OS_URL="https://localhost:$OS_PORT" FIT_SYNC_OS_CA="$CERTS/os-ca.pem" FIT_SYNC_OS_PASSWORD="$PASSWORD"
	fi
	i=0
	until curl -fsS -u "fit:$PASSWORD" "http://localhost:$CH_PORT/?query=SELECT%201" >/dev/null 2>&1; do
		i=$((i + 1))
		if [ $i -gt 120 ]; then
			echo "$SYNC_PREFIX-ch did not start:" >&2
			docker logs --tail 50 "$SYNC_PREFIX-ch" >&2
			exit 1
		fi
		sleep 1
	done
	export FIT_SYNC_CH_URL="http://localhost:$CH_PORT" FIT_SYNC_CH_USER=fit FIT_SYNC_CH_PASSWORD="$PASSWORD"
	redises="$SYNC_PREFIX-redis"
	[ "$LIGHT" = 1 ] || redises="$redises $SYNC_PREFIX-valkey"
	for c in $redises; do
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
	export FIT_SYNC_REDIS_ADDR="127.0.0.1:$REDIS_PORT" FIT_SYNC_REDIS_TLS_ADDR="127.0.0.1:$REDIS_TLS_PORT" \
		FIT_SYNC_REDIS_CA="$CERTS/redis-ca.pem" FIT_SYNC_REDIS_USER=fit FIT_SYNC_REDIS_PASSWORD="$PASSWORD"
	[ "$LIGHT" = 1 ] && return 0
	export FIT_SYNC_VALKEY_ADDR="127.0.0.1:$VALKEY_PORT"
	until_up "$SYNC_PREFIX-minio" "http://localhost:$MINIO_PORT/minio/health/ready"
	until_up "$SYNC_PREFIX-seaweed" "http://localhost:$SEAWEED_PORT/"
	export FIT_SYNC_MINIO_URL="http://localhost:$MINIO_PORT" FIT_SYNC_MINIO_ACCESS_KEY=fitminio FIT_SYNC_MINIO_SECRET_KEY="$PASSWORD" \
		FIT_SYNC_SEAWEED_URL="http://localhost:$SEAWEED_PORT" FIT_SYNC_SEAWEED_ACCESS_KEY=fitseaweed FIT_SYNC_SEAWEED_SECRET_KEY="$PASSWORD"
}

if has search; then search_start; fi
if has search; then search_wait; fi
if has mysql; then mysql_start; fi
if has postgres; then
	pg_start
	# shellcheck disable=SC2046 # one variable a word, no spaces in them
	export $(pg_env)
fi

if has mongo; then
	# The sets' certificates are made afresh for each run; a kept container
	# holds the ones it was started with, so it is made again too.
	MONGO_PORT=${MONGO_PORT:-29600}
	mkdir -p "$CERTS/mongo"
	make_mongo_certs "$CERTS/mongo"
	rm_mongos "$SYNC_PREFIX"
	MONGO_ENV=$(start_mongos "$SYNC_PREFIX" "$MONGO_PORT" "$CERTS/mongo")
	set -f
	# shellcheck disable=SC2086 # one variable a line, no spaces in them
	export $MONGO_ENV
	set +f
fi

if has kafka || has tidb; then
	if [ "$LIGHT" = 1 ] || ! has kafka; then export REDPANDA=${REDPANDA:-0}; fi
	KAFKA_ENV=$(test/sync/kafka.sh up)
	eval "$KAFKA_ENV"
fi
if has tidb; then
	# PD, TiKV, TiDB and TiCDC on Kafka's network, for the tidb source
	# (test/sync/tidb.sh); its failure ends the run.
	TIDB_ENV=$(test/sync/tidb.sh up)
	eval "$TIDB_ENV"
fi

# The second round's servers (I222), each family's script printing the
# environment its tests read; a script that fails ends the run.
if has mq; then
	if mq_has rabbitmq; then eval "$(test/sync/rabbitmq.sh up)"; fi
	if mq_has nats; then eval "$(test/sync/nats.sh up)"; fi
	if mq_has pulsar; then eval "$(test/sync/pulsar.sh up)"; fi
fi
if { has mq && mq_has mqtt; } || has obs; then
	# obs's MQTT to InfluxDB test needs Mosquitto alone.
	if ! has mq; then EMQX=0; fi
	eval "$(EMQX=${EMQX:-1} test/sync/mqtt.sh up)"
fi
if has mssql; then eval "$(test/sync/mssql.sh up)"; fi
if has olap; then eval "$(test/sync/olap.sh up)"; fi
if has obs; then
	export OBS_PREFIX=${OBS_PREFIX:-$SYNC_PREFIX-obs}
	eval "$(test/sync/obs.sh up)"
	export FIT_SYNC_OBS=1
fi

PKGS=./test/sync
if has mysql; then PKGS="$PKGS ./sync/mysql/binlog"; fi
# A -run in the arguments comes after the families' and replaces it.
if [ -n "$RUN" ]; then set -- -run "$RUN" "$@"; fi
# SYNC_TIMEOUT is go test's -timeout: 90m for every family at once on one
# host, each CI job its own (ci.yml).
# shellcheck disable=SC2086 # the packages are words
FIT_SYNC_INTEROP=required go test -race -count=1 -timeout "${SYNC_TIMEOUT:-90m}" "$@" $PKGS
