#!/bin/sh
# run.sh runs one scenario of the sync benchmark (bench/sync/README.md):
# fit and the movers it is compared with, one at a time, each against fresh
# servers of the same images and settings, in Docker on a private network
# (no port is published), each pinned to the same CPUs; syncbench writes
# the records, counts them at the sink and samples the mover. Everything it
# starts is removed at the end.
#
#   bench/sync/run.sh [-n ROUNDS] [-movers LIST] [-o DIR] [-name NAME] SCENARIO
#
# SCENARIO is files, files-loki, mysql-es, mysql-kafka, pg-kafka,
# mysql-batch, rabbitmq-kafka, mqtt-kafka, mssql-kafka or mqtt-influxdb. The
# results go in DIR (bench/results/sync) as NAME.json (NAME is the scenario
# and its variant), with NAME/: each round's result, fit's profiles, and the
# movers' logs. The environment sets the rest (defaults in brackets):
#
#   SIZE          full or smoke, the defaults below for each      [full]
#   RECORDS       records measured: lines, or rows                [files 2,000,000; streams 500,000; batch 2,000,000]
#   LINE_BYTES    a line's size, its newline included             [128]
#   FILES         the files the lines are spread over             [1]
#   TX_ROWS       rows a source transaction                       [10]
#   STEADY_RATE   records a second of the steady phase (lag)      [2000]
#   STEADY_FOR    the steady phase's length                       [20s]
#   PAUSE         1 pauses a stream's mover while its rows are    [1]
#                 written, 0 writes them while it runs
#   MOVER_CORES, AGENT_CORES  physical cores for the mover and    [4, 2]
#                 syncbench; the servers get the rest
#   MOVER_CPUS, AGENT_CPUS, SERVER_CPUS  CPU lists in place of the split
#   BIN           a directory with fit and syncbench built for the Docker
#                 host's Linux; built here with Go when left out
#   CACHE         where downloads and their extracts are kept     [~/.cache/fit-syncbench]
#   PREFIX        names the containers, volumes and network       [fsb]
#   KEEP          1 leaves the last run's containers
set -eu

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)

rounds=1 movers="" outdir=$root/bench/results/sync name=""
while [ $# -gt 1 ]; do
	case $1 in
	-n) rounds=$2 ;;
	-movers) movers=$2 ;;
	-o) outdir=$2 ;;
	-name) name=$2 ;;
	*) break ;;
	esac
	shift 2
done
[ $# -eq 1 ] || {
	sed -n '2,/^set -eu/p' "$0" | sed '$d; s/^# \{0,1\}//' >&2
	exit 2
}
sc=$1
case $sc in
files) all=fit,filebeat ;;
files-loki) all=fit,promtail,alloy ;;
mysql-es | mysql-kafka) all=fit,canal,debezium ;;
pg-kafka) all=fit,debezium ;;
mysql-batch) all=fit,datax ;;
rabbitmq-kafka | mqtt-kafka) all=fit,kafka-connect ;;
mssql-kafka) all=fit,debezium ;;
mqtt-influxdb) all=fit,telegraf ;;
*)
	echo "run.sh: unknown scenario $sc: files, files-loki, mysql-es, mysql-kafka, pg-kafka, mysql-batch, rabbitmq-kafka, mqtt-kafka, mssql-kafka or mqtt-influxdb" >&2
	exit 2
	;;
esac
movers=${movers:-$all}

# Pinned images: the servers', the movers', and the base of fit and syncbench.
BASE_IMAGE=${BASE_IMAGE:-ubuntu:24.04}
MYSQL_IMAGE=${MYSQL_IMAGE:-mysql:8.0.42}
PG_IMAGE=${PG_IMAGE:-postgres:17.5-bookworm}
ES_IMAGE=${ES_IMAGE:-docker.elastic.co/elasticsearch/elasticsearch:7.17.28}
KAFKA_IMAGE=${KAFKA_IMAGE:-apache/kafka:3.9.1}
FILEBEAT_IMAGE=${FILEBEAT_IMAGE:-docker.elastic.co/beats/filebeat:8.17.4}
LOKI_IMAGE=${LOKI_IMAGE:-grafana/loki:3.5.5}
PROMTAIL_IMAGE=${PROMTAIL_IMAGE:-grafana/promtail:3.5.5}
ALLOY_IMAGE=${ALLOY_IMAGE:-grafana/alloy:v1.21.3}
CANAL_IMAGE=${CANAL_IMAGE:-canal/canal-server:v1.1.8}
DEBEZIUM_IMAGE=${DEBEZIUM_IMAGE:-quay.io/debezium/connect:3.0.8.Final}
JRE_IMAGE=${JRE_IMAGE:-eclipse-temurin:8u452-b09-jre-jammy}
RABBITMQ_IMAGE=${RABBITMQ_IMAGE:-rabbitmq:4.1.4}
MOSQUITTO_IMAGE=${MOSQUITTO_IMAGE:-eclipse-mosquitto:2.0.22}
MSSQL_IMAGE=${MSSQL_IMAGE:-mcr.microsoft.com/mssql/server:2022-CU20-ubuntu-22.04}
INFLUXDB_IMAGE=${INFLUXDB_IMAGE:-influxdb:2.7.12}
TELEGRAF_IMAGE=${TELEGRAF_IMAGE:-telegraf:1.40.3}
# Built here from the pinned images and the pinned downloads below.
ADAPTER_IMAGE=fit-syncbench/canal-adapter:1.1.8-2
DATAX_IMAGE=fit-syncbench/datax:202309
CONNECT_ES_IMAGE=fit-syncbench/connect-es:3.0.8-14.1.2
CONNECT_CAMEL_IMAGE=fit-syncbench/connect-camel:3.0.8-4.14.5

ADAPTER_URL=https://github.com/alibaba/canal/releases/download/canal-1.1.8/canal.adapter-1.1.8.tar.gz
ADAPTER_SHA=e9366226860b6939ace0eb6d46a1a365d71ab45b4292c66e73bba8d9f067a340
DATAX_URL=http://datax-opensource.oss-cn-hangzhou.aliyuncs.com/202309/datax.tar.gz
DATAX_SHA=5f3d8cbdf25fd8476c8124f6ca3c892a7f4c2286e55da629771848adff944788
ES_SINK_URL=https://hub-downloads.confluent.io/api/plugins/confluentinc/kafka-connect-elasticsearch/versions/14.1.2/confluentinc-kafka-connect-elasticsearch-14.1.2.zip
ES_SINK_SHA=4d806e31764b5a0e5b3d9ab9fbf047bf35aa3a2470ba28ae92bc81991b251aa0
CAMEL_URL=https://repo1.maven.org/maven2/org/apache/camel/kafkaconnector
CAMEL_RABBITMQ_URL=$CAMEL_URL/camel-spring-rabbitmq-source-kafka-connector/4.14.5/camel-spring-rabbitmq-source-kafka-connector-4.14.5-package.tar.gz
CAMEL_RABBITMQ_SHA=d973fe3e4fc068f2c6e6253d92ebfb36e57bfd6a40f241a3eabe96e255326188
CAMEL_MQTT_URL=$CAMEL_URL/camel-mqtt5-source-kafka-connector/4.14.5/camel-mqtt5-source-kafka-connector-4.14.5-package.tar.gz
CAMEL_MQTT_SHA=cd62d3b88079d999e0023a812158c061cb3fab409b0388ae0bc90e738013171a

SIZE=${SIZE:-full}
case $SIZE in
smoke) d_files=20000 d_stream=5000 d_batch=20000 d_rate=200 d_for=3s ;;
*) d_files=2000000 d_stream=500000 d_batch=2000000 d_rate=2000 d_for=20s ;;
esac
case $sc in
files | files-loki) RECORDS=${RECORDS:-$d_files} ;;
mysql-batch) RECORDS=${RECORDS:-$d_batch} ;;
*) RECORDS=${RECORDS:-$d_stream} ;;
esac
LINE_BYTES=${LINE_BYTES:-128}
FILES=${FILES:-1}
TX_ROWS=${TX_ROWS:-10}
STEADY_RATE=${STEADY_RATE:-$d_rate}
STEADY_FOR=${STEADY_FOR:-$d_for}
STARTUP_TIMEOUT=${STARTUP_TIMEOUT:-10m}
PAUSE=${PAUSE:-1}
PREFIX=${PREFIX:-fsb}
CACHE=${CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/fit-syncbench}
NET=$PREFIX-net
LABEL=fit-syncbench=$PREFIX
# The servers' password: a throwaway, on a network nothing outside reaches.
PW=fit-syncbench-1
DOCKER_SOCK=${DOCKER_SOCK:-/var/run/docker.sock}

if [ -z "$name" ]; then
	name=$sc
	case $sc in files | files-loki) name=$sc-${LINE_BYTES}b-${FILES}f ;; esac
fi
# A run of the same name before is replaced.
rm -rf "${outdir:?}/$name" "$outdir/$name.json"
mkdir -p "$outdir/$name"
outdir=$(cd "$outdir" && pwd)
res=$outdir/$name
log() { echo "$(date -u +%H:%M:%S) $*" >&2; }

# cpu_split sets MOVER_CPUS, AGENT_CPUS and SERVER_CPUS from the physical
# cores (lscpu's, a core's threads together; each CPU a core where there is
# no lscpu, as in Docker Desktop's VM): the servers the first, then
# syncbench's, then the mover's at the end.
cpu_split() {
	if command -v lscpu >/dev/null 2>&1 && [ "$(uname -s)" = Linux ]; then
		cores=$(lscpu -p=CPU,CORE | grep -v '^#' | awk -F, '{c[$2] = c[$2] (c[$2] == "" ? "" : ",") $1} END {for (k in c) print k, c[k]}' | sort -n | cut -d' ' -f2)
	else
		n=$(docker info --format '{{.NCPU}}')
		cores=$(seq 0 $((n - 1)))
	fi
	total=$(echo "$cores" | wc -l | tr -d ' ')
	mc=${MOVER_CORES:-4} ac=${AGENT_CORES:-2}
	if [ $((mc + ac + 2)) -gt "$total" ]; then
		echo "run.sh: $total cores, fewer than the mover's $mc, syncbench's $ac and two for the servers" >&2
		exit 1
	fi
	sc_n=$((total - mc - ac))
	SERVER_CPUS=${SERVER_CPUS:-$(echo "$cores" | sed -n "1,${sc_n}p" | paste -sd, -)}
	AGENT_CPUS=${AGENT_CPUS:-$(echo "$cores" | sed -n "$((sc_n + 1)),$((sc_n + ac))p" | paste -sd, -)}
	MOVER_CPUS=${MOVER_CPUS:-$(echo "$cores" | sed -n "$((sc_n + ac + 1)),${total}p" | paste -sd, -)}
}

cleanup() {
	[ "${KEEP:-}" = 1 ] && return
	ids=$(docker ps -aq --filter "label=$LABEL")
	# -v: with the anonymous volumes their images declare (Kafka's,
	# RabbitMQ's, SQL Server's, ...), which would be left behind.
	[ -n "$ids" ] && docker rm -f -v $ids >/dev/null 2>&1
	vols=$(docker volume ls -q --filter "label=$LABEL")
	[ -n "$vols" ] && docker volume rm $vols >/dev/null 2>&1
	docker network rm "$NET" >/dev/null 2>&1 || true
}

# fetch URL SHA FILE downloads URL into the cache once, checked.
fetch() {
	mkdir -p "$CACHE"
	if [ ! -f "$CACHE/$3" ]; then
		log "downloading $1"
		curl -fSL --retry 3 -o "$CACHE/$3.part" "$1"
		mv "$CACHE/$3.part" "$CACHE/$3"
	fi
	got=$( (sha256sum "$CACHE/$3" 2>/dev/null || shasum -a 256 "$CACHE/$3") | cut -d' ' -f1)
	if [ "$got" != "$2" ]; then
		echo "run.sh: $CACHE/$3 has sha256 $got, not $2; remove it to download again" >&2
		exit 1
	fi
}

# The images built here: from the pinned base images, with the pinned
# downloads.
build_images() {
	case ,$movers, in *,canal,*) [ "$sc" = mysql-es ] && need_adapter=1 ;; esac
	case ,$movers, in *,debezium,*) [ "$sc" = mysql-es ] && need_es_sink=1 ;; esac
	case ,$movers, in *,datax,*) need_datax=1 ;; esac
	case ,$movers, in *,kafka-connect,*) need_camel=1 ;; esac
	if [ "${need_adapter:-}" = 1 ] && ! docker image inspect "$ADAPTER_IMAGE" >/dev/null 2>&1; then
		fetch "$ADAPTER_URL" "$ADAPTER_SHA" canal.adapter-1.1.8.tar.gz
		ctx=$CACHE/canal-adapter-ctx
		rm -rf "$ctx" && mkdir -p "$ctx/canal-adapter"
		tar xzf "$CACHE/canal.adapter-1.1.8.tar.gz" -C "$ctx/canal-adapter"
		cp "$here/images/canal-adapter.Dockerfile" "$ctx/Dockerfile"
		log "building $ADAPTER_IMAGE"
		docker build -q --build-arg "JRE_IMAGE=$JRE_IMAGE" -t "$ADAPTER_IMAGE" "$ctx" >/dev/null
		rm -rf "$ctx"
	fi
	if [ "${need_es_sink:-}" = 1 ] && ! docker image inspect "$CONNECT_ES_IMAGE" >/dev/null 2>&1; then
		fetch "$ES_SINK_URL" "$ES_SINK_SHA" confluentinc-kafka-connect-elasticsearch-14.1.2.zip
		ctx=$CACHE/connect-es-ctx
		rm -rf "$ctx" && mkdir -p "$ctx"
		cp "$CACHE/confluentinc-kafka-connect-elasticsearch-14.1.2.zip" "$ctx/es-sink.zip"
		cp "$here/images/connect-es.Dockerfile" "$ctx/Dockerfile"
		log "building $CONNECT_ES_IMAGE"
		docker build -q --build-arg "DEBEZIUM_IMAGE=$DEBEZIUM_IMAGE" -t "$CONNECT_ES_IMAGE" "$ctx" >/dev/null
		rm -rf "$ctx"
	fi
	if [ "${need_camel:-}" = 1 ] && ! docker image inspect "$CONNECT_CAMEL_IMAGE" >/dev/null 2>&1; then
		fetch "$CAMEL_RABBITMQ_URL" "$CAMEL_RABBITMQ_SHA" camel-spring-rabbitmq-source-4.14.5.tar.gz
		fetch "$CAMEL_MQTT_URL" "$CAMEL_MQTT_SHA" camel-mqtt5-source-4.14.5.tar.gz
		ctx=$CACHE/connect-camel-ctx
		rm -rf "$ctx" && mkdir -p "$ctx"
		cp "$CACHE/camel-spring-rabbitmq-source-4.14.5.tar.gz" "$ctx/rabbitmq.tar.gz"
		cp "$CACHE/camel-mqtt5-source-4.14.5.tar.gz" "$ctx/mqtt.tar.gz"
		cp "$here/images/connect-camel.Dockerfile" "$ctx/Dockerfile"
		log "building $CONNECT_CAMEL_IMAGE"
		docker build -q --build-arg "DEBEZIUM_IMAGE=$DEBEZIUM_IMAGE" -t "$CONNECT_CAMEL_IMAGE" "$ctx" >/dev/null
		rm -rf "$ctx"
	fi
	if [ "${need_datax:-}" = 1 ] && ! docker image inspect "$DATAX_IMAGE" >/dev/null 2>&1; then
		fetch "$DATAX_URL" "$DATAX_SHA" datax-202309.tar.gz
		ctx=$CACHE/datax-ctx
		rm -rf "$ctx" && mkdir -p "$ctx"
		# Only the engine and the MySQL reader and writer, of 1.7 GB.
		tar xzf "$CACHE/datax-202309.tar.gz" -C "$ctx" datax/bin datax/conf datax/lib \
			datax/plugin/reader/mysqlreader datax/plugin/writer/mysqlwriter
		cp "$here/images/datax.Dockerfile" "$ctx/Dockerfile"
		log "building $DATAX_IMAGE"
		docker build -q --build-arg "JRE_IMAGE=$JRE_IMAGE" -t "$DATAX_IMAGE" "$ctx" >/dev/null
		rm -rf "$ctx"
	fi
}

pull() {
	for img in "$@"; do
		docker image inspect "$img" >/dev/null 2>&1 || {
			log "pulling $img"
			docker pull -q "$img" >/dev/null
		}
	done
}

# The binaries, for the Docker host's Linux.
binaries() {
	if [ -n "${BIN:-}" ]; then
		bindir=$(cd "$BIN" && pwd)
		return
	fi
	bindir=$CACHE/bin-$arch
	mkdir -p "$bindir"
	log "building fit and syncbench for linux/$arch"
	(cd "$root" && CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -o "$bindir/fit" ./cmd/fit &&
		CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -o "$bindir/syncbench" ./bench/sync/syncbench)
}

# run_d NAME [docker run flags] IMAGE [ARGS]: a container of this run,
# started, or only made with CREATE=1, for syncbench to start.
run_d() {
	n=$1
	shift
	verb="run -d"
	[ "${CREATE:-}" = 1 ] && verb=create
	# shellcheck disable=SC2086
	docker $verb --name "$PREFIX-$n" --label "$LABEL" --network "$NET" --network-alias "$n" \
		--ulimit nofile=1048576:1048576 "$@" >/dev/null
}

# wait_for WHAT SECONDS CMD...: until CMD succeeds.
wait_for() {
	what=$1 secs=$2
	shift 2
	wi=0
	until "$@" >/dev/null 2>&1; do
		wi=$((wi + 1))
		if [ $wi -gt "$secs" ]; then
			echo "run.sh: $what did not start:" >&2
			docker logs --tail 40 "$PREFIX-$what" >&2 || true
			return 1
		fi
		sleep 1
	done
}

mysql_up() {
	run_d "$1" --cpuset-cpus "$SERVER_CPUS" -e MYSQL_ROOT_PASSWORD=$PW "$MYSQL_IMAGE" \
		--server-id=1 --log-bin=mysql-bin --binlog-format=ROW --gtid-mode=ON --enforce-gtid-consistency=ON \
		--default-authentication-plugin=mysql_native_password --innodb-buffer-pool-size=1G \
		--innodb-flush-log-at-trx-commit=2 --sync-binlog=0 --max-connections=2000 --skip-name-resolve
}

mysql_wait() {
	# The entrypoint's first server has no TCP: an answer over TCP is the last.
	wait_for "$1" 180 docker exec -e MYSQL_PWD=$PW "$PREFIX-$1" mysql -h127.0.0.1 -uroot -e 'SELECT 1'
}

servers_up() {
	case $sc in
	files) return ;;
	files-loki)
		run_d loki --cpuset-cpus "$SERVER_CPUS" -v "$here/configs/loki/loki.yaml:/etc/loki/loki.yaml:ro" \
			"$LOKI_IMAGE" -config.file=/etc/loki/loki.yaml
		wait_for loki 120 docker exec "$PREFIX-loki" /busybox/wget -q -O /dev/null http://127.0.0.1:3100/ready
		return
		;;
	mysql-es | mysql-kafka | mysql-batch) mysql_up mysql ;;
	rabbitmq-kafka)
		run_d rabbitmq --cpuset-cpus "$SERVER_CPUS" -e RABBITMQ_DEFAULT_USER=bench -e RABBITMQ_DEFAULT_PASS=$PW "$RABBITMQ_IMAGE"
		;;
	mqtt-kafka | mqtt-influxdb)
		run_d mosquitto --cpuset-cpus "$SERVER_CPUS" -v "$here/configs/mosquitto/mosquitto.conf:/mosquitto/config/mosquitto.conf:ro" "$MOSQUITTO_IMAGE"
		;;
	mssql-kafka)
		# SQL Server Agent runs CDC's capture job.
		run_d mssql --cpuset-cpus "$SERVER_CPUS" -e ACCEPT_EULA=Y -e MSSQL_SA_PASSWORD=$PW -e MSSQL_AGENT_ENABLED=true \
			-e MSSQL_MEMORY_LIMIT_MB=2048 "$MSSQL_IMAGE"
		;;
	pg-kafka)
		run_d postgres --cpuset-cpus "$SERVER_CPUS" -e POSTGRES_PASSWORD=$PW -e POSTGRES_DB=shop "$PG_IMAGE" \
			-c wal_level=logical -c max_wal_senders=10 -c max_replication_slots=10 -c shared_buffers=1GB \
			-c synchronous_commit=off -c max_connections=200
		;;
	esac
	[ "$sc" = mysql-batch ] && mysql_up mysql2
	if [ "$sc" = mqtt-influxdb ]; then
		# Its setup makes the org and bucket bench, kept for good, and
		# the token (the password, a throwaway) the movers write with.
		run_d influxdb --cpuset-cpus "$SERVER_CPUS" -e DOCKER_INFLUXDB_INIT_MODE=setup \
			-e DOCKER_INFLUXDB_INIT_USERNAME=bench -e DOCKER_INFLUXDB_INIT_PASSWORD=$PW \
			-e DOCKER_INFLUXDB_INIT_ORG=bench -e DOCKER_INFLUXDB_INIT_BUCKET=bench \
			-e DOCKER_INFLUXDB_INIT_ADMIN_TOKEN=$PW "$INFLUXDB_IMAGE"
	fi
	if [ "$sc" = mysql-es ]; then
		# shellcheck disable=SC2086
		run_d es --cpuset-cpus "$SERVER_CPUS" $ES_JAVA -e discovery.type=single-node -e xpack.security.enabled=false -e ingest.geoip.downloader.enabled=false \
			-e ES_JAVA_OPTS="-Xms1g -Xmx1g" "$ES_IMAGE"
	fi
	if [ "$sc" = mysql-kafka ] || [ "$sc" = pg-kafka ] || [ "${sc%-kafka}" != "$sc" ] || { [ "$sc" = mysql-es ] && [ "$1" = debezium ]; }; then
		run_d kafka --cpuset-cpus "$SERVER_CPUS" -e KAFKA_NODE_ID=1 -e KAFKA_PROCESS_ROLES=broker,controller \
			-e KAFKA_LISTENERS=PLAINTEXT://:9092,CONTROLLER://:9093 -e KAFKA_ADVERTISED_LISTENERS=PLAINTEXT://kafka:9092 \
			-e KAFKA_CONTROLLER_LISTENER_NAMES=CONTROLLER \
			-e KAFKA_LISTENER_SECURITY_PROTOCOL_MAP=CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT \
			-e KAFKA_CONTROLLER_QUORUM_VOTERS=1@localhost:9093 -e KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR=1 \
			-e KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR=1 -e KAFKA_TRANSACTION_STATE_LOG_MIN_ISR=1 \
			-e KAFKA_NUM_PARTITIONS=1 -e KAFKA_GROUP_INITIAL_REBALANCE_DELAY_MS=0 -e KAFKA_HEAP_OPTS="-Xms1g -Xmx1g" \
			"$KAFKA_IMAGE"
	fi
	case $sc in
	mysql-es | mysql-kafka) mysql_wait mysql || return 1 ;;
	mysql-batch) mysql_wait mysql && mysql_wait mysql2 || return 1 ;;
	pg-kafka) wait_for postgres 120 docker exec "$PREFIX-postgres" pg_isready -q -h 127.0.0.1 -U postgres || return 1 ;;
	rabbitmq-kafka)
		# As the server's user: rabbitmqctl as root, before the server
		# has made its Erlang cookie, makes it root's, and the server dies.
		wait_for rabbitmq 180 docker exec -u rabbitmq "$PREFIX-rabbitmq" rabbitmqctl await_startup || return 1
		;;
	mqtt-kafka | mqtt-influxdb) wait_for mosquitto 60 sh -c "docker logs '$PREFIX-mosquitto' 2>&1 | grep -q running" || return 1 ;;
	mssql-kafka)
		wait_for mssql 300 docker exec "$PREFIX-mssql" /opt/mssql-tools18/bin/sqlcmd -C -b -S localhost -U sa -P "$PW" -Q 'SELECT 1' || return 1
		;;
	esac
	if [ "$sc" = mqtt-influxdb ]; then
		# The bucket is listed once the setup is done and the server
		# started again on its port.
		wait_for influxdb 120 docker exec "$PREFIX-influxdb" influx bucket list --host http://localhost:8086 -t "$PW" -o bench -n bench || return 1
	fi
	if [ "$sc" = mysql-es ]; then
		wait_for es 180 docker exec "$PREFIX-es" curl -fs 'localhost:9200/_cluster/health?wait_for_status=yellow&timeout=1s' || return 1
	fi
	if docker inspect "$PREFIX-kafka" >/dev/null 2>&1; then
		wait_for kafka 180 docker exec "$PREFIX-kafka" /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 --list || return 1
	fi
}

# agent [docker run flags] -- SYNCBENCH ARGS: syncbench, beside the servers.
agent() {
	flags=""
	while [ "$1" != -- ]; do
		flags="$flags $1"
		shift
	done
	shift
	# In the background, waited for, so that an interrupt ends the run
	# now (the shell runs a trap only once its foreground command ends).
	# shellcheck disable=SC2086 # the flags are words
	docker run --rm --label "$LABEL" --network "$NET" --cpuset-cpus "$AGENT_CPUS" \
		-v "$bindir/syncbench:/usr/local/bin/syncbench:ro" -v "$outdir:/out" $flags \
		"$BASE_IMAGE" syncbench "$@" &
	wait $!
}

# The connectors Kafka Connect runs, from configs: debezium/ (Debezium's)
# and connect/ (Camel's).
register() {
	wait_for connect 300 docker exec "$PREFIX-connect" curl -fs localhost:8083/connectors || return 1
	for f in "$@"; do
		docker exec -i "$PREFIX-connect" curl -fsS -X POST -H 'Content-Type: application/json' \
			--data-binary @- localhost:8083/connectors <"$here/configs/$f" >/dev/null || return 1
	done
}

# mover_up MOVER: starts it, and sets containers, topic and pprof.
mover_up() {
	pprof="" topic=bench kformat=""
	case $1 in
	fit)
		vol=""
		case $sc in files | files-loki) vol="-v $PREFIX-data:/data" ;; esac
		# shellcheck disable=SC2086
		run_d fit --cpuset-cpus "$MOVER_CPUS" $vol -v "$PREFIX-run:/run/fit" \
			-v "$bindir/fit:/usr/local/bin/fit:ro" -v "$here/configs/fit/$sc.conf:/etc/fit/fit.conf:ro" \
			-e MYSQL_PASSWORD=$PW -e PG_PASSWORD=$PW -e RABBITMQ_PASSWORD=$PW -e MSSQL_PASSWORD=$PW -e INFLUX_TOKEN=$PW \
			"$BASE_IMAGE" fit run -c /etc/fit/fit.conf
		containers=$PREFIX-fit pprof=unix:///run/fit/admin.sock
		;;
	filebeat)
		run_d filebeat --cpuset-cpus "$MOVER_CPUS" -v "$PREFIX-data:/data" \
			-v "$here/configs/filebeat/filebeat.yml:/usr/share/filebeat/filebeat.yml:ro" \
			"$FILEBEAT_IMAGE" -e --strict.perms=false
		containers=$PREFIX-filebeat
		;;
	promtail)
		run_d promtail --cpuset-cpus "$MOVER_CPUS" -v "$PREFIX-data:/data" \
			-v "$here/configs/promtail/promtail.yml:/etc/promtail/config.yml:ro" \
			"$PROMTAIL_IMAGE" -config.file=/etc/promtail/config.yml
		containers=$PREFIX-promtail
		;;
	alloy)
		# Its positions under the storage path, in the container; no usage
		# report sent out.
		run_d alloy --cpuset-cpus "$MOVER_CPUS" -v "$PREFIX-data:/data" \
			-v "$here/configs/alloy/config.alloy:/etc/alloy/config.alloy:ro" \
			"$ALLOY_IMAGE" run /etc/alloy/config.alloy --storage.path=/var/lib/alloy/data --disable-reporting
		containers=$PREFIX-alloy
		;;
	telegraf)
		run_d telegraf --cpuset-cpus "$MOVER_CPUS" -e INFLUX_TOKEN=$PW \
			-v "$here/configs/telegraf/$sc.conf:/etc/telegraf/telegraf.conf:ro" "$TELEGRAF_IMAGE"
		containers=$PREFIX-telegraf
		;;
	canal)
		mode=tcp
		[ "$sc" = mysql-kafka ] && mode=kafka
		# canal-server's own start, without the image's node_exporter.
		# shellcheck disable=SC2086
		run_d canal --cpuset-cpus "$MOVER_CPUS" -e canal.auto.scan=false -e canal.destinations=example \
			-e canal.instance.master.address=mysql:3306 -e canal.instance.dbUsername=root \
			-e canal.instance.dbPassword=$PW -e canal.instance.connectionCharset=UTF-8 \
			-e 'canal.instance.filter.regex=shop\.items' -e canal.serverMode=$mode \
			-e kafka.bootstrap.servers=kafka:9092 -e canal.mq.topic=bench -e canal.mq.flatMessage=true \
			--entrypoint bash "$CANAL_IMAGE" -c 'chown -R admin: /home/admin/canal-server &&
				su admin -c "cd /home/admin/canal-server/bin && sh startup.sh" && exec tail -F /home/admin/canal-server/logs/canal/canal.log /home/admin/canal-server/logs/example/example.log'
		containers=$PREFIX-canal kformat=canal
		if [ "$sc" = mysql-es ]; then
			# shellcheck disable=SC2086
			run_d canal-adapter --cpuset-cpus "$MOVER_CPUS" \
				-v "$here/configs/canal/application.yml:/opt/canal-adapter/conf/application.yml:ro" \
				-v "$here/configs/canal/es7/items.yml:/opt/canal-adapter/conf/es7/items.yml:ro" "$ADAPTER_IMAGE"
			containers=$containers,$PREFIX-canal-adapter
		fi
		;;
	debezium | kafka-connect)
		img=$DEBEZIUM_IMAGE
		[ "$sc" = mysql-es ] && img=$CONNECT_ES_IMAGE
		[ "$1" = kafka-connect ] && img=$CONNECT_CAMEL_IMAGE
		# shellcheck disable=SC2086
		run_d connect --cpuset-cpus "$MOVER_CPUS" $JVM_ENV -e BOOTSTRAP_SERVERS=kafka:9092 -e GROUP_ID=bench \
			-e CONFIG_STORAGE_TOPIC=connect-configs -e OFFSET_STORAGE_TOPIC=connect-offsets \
			-e STATUS_STORAGE_TOPIC=connect-status \
			-e KEY_CONVERTER=org.apache.kafka.connect.json.JsonConverter \
			-e VALUE_CONVERTER=org.apache.kafka.connect.json.JsonConverter \
			-e CONNECT_KEY_CONVERTER_SCHEMAS_ENABLE=false -e CONNECT_VALUE_CONVERTER_SCHEMAS_ENABLE=false "$img"
		containers=$PREFIX-connect
		case $sc in
		mysql-es) register debezium/mysql-es.json debezium/es-sink.json || return 1 ;;
		mysql-kafka) register debezium/mysql.json && topic=bench.shop.items || return 1 ;;
		pg-kafka) register debezium/postgres.json && topic=bench.public.items || return 1 ;;
		mssql-kafka) register debezium/mssql.json && topic=bench.shop.dbo.items || return 1 ;;
		rabbitmq-kafka) register connect/rabbitmq.json || return 1 ;;
		mqtt-kafka) register connect/mqtt.json || return 1 ;;
		esac
		;;
	datax)
		# shellcheck disable=SC2086
		run_d datax --cpuset-cpus "$MOVER_CPUS" -v "$here/configs/datax/job.json:/etc/datax/job.json:ro" "$DATAX_IMAGE"
		containers=$PREFIX-datax
		;;
	*)
		echo "run.sh: $sc has no mover $1" >&2
		exit 2
		;;
	esac
}

# run_one ROUND MOVER: fresh servers, the mover, the measurement.
run_one() {
	r=$1 m=$2
	log "$name round $r: $m"
	docker network create --label "$LABEL" "$NET" >/dev/null
	docker volume create --label "$LABEL" "$PREFIX-data" >/dev/null
	docker volume create --label "$LABEL" "$PREFIX-run" >/dev/null
	if ! servers_up "$m"; then
		log "$name round $r: $m: the servers did not start"
		cleanup
		return 1
	fi
	common="-scenario $sc -records $RECORDS -files $FILES -line-bytes $LINE_BYTES -tx-rows $TX_ROWS
		-mysql-password $PW -pg-password $PW -rabbitmq-password $PW -mssql-password $PW -influxdb-token $PW"
	# shellcheck disable=SC2086
	agent -v "$PREFIX-data:/data" -- prepare $common || {
		log "$name round $r: $m: prepare failed"
		cleanup
		return 1
	}
	# A mover of records there before it starts is made here and started
	# by syncbench, once it counts the sink.
	start=false
	case $sc in files | files-loki | mysql-batch) start=true ;; esac
	CREATE=0
	$start && CREATE=1
	if ! mover_up "$m"; then
		log "$name round $r: $m did not start"
		CREATE=0
		cleanup
		return 1
	fi
	CREATE=0
	interval=100ms
	[ "$sc" = mysql-batch ] && interval=500ms
	pause=false
	[ "$PAUSE" = 1 ] && pause=true
	mkdir -p "$res/profiles-r$r"
	status=0
	# shellcheck disable=SC2086
	agent -v "$PREFIX-data:/data" -v "$PREFIX-run:/run/fit" -v "$DOCKER_SOCK:/var/run/docker.sock" -- \
		measure $common -mover "$m" -variant "$name" -containers "$containers" -kafka-topic "$topic" -kafka-format "$kformat" \
		-pause=$pause -start=$start -steady-rate "$STEADY_RATE" -steady-for "$STEADY_FOR" -interval $interval \
		-startup-timeout "$STARTUP_TIMEOUT" \
		-pprof "$pprof" -profile-dir "/out/$name/profiles-r$r" -out "/out/$name/r$r-$m.json" || status=$?
	for c in $(echo "$containers" | tr , ' '); do
		docker logs --tail 2000 "$c" >"$res/r$r-${c#"$PREFIX"-}.log" 2>&1 || true
	done
	rmdir "$res/profiles-r$r" 2>/dev/null || true
	if [ -f "$res/r$r-$m.json" ]; then
		grep -E '"(records_per_sec|cpu_cores|rss_peak_bytes|error)"' "$res/r$r-$m.json" | tr -s ' \n' ' ' >&2
		echo >&2
	fi
	cleanup
	return $status
}

trap cleanup EXIT
trap 'exit 130' INT TERM
cleanup
KEEP_SAVED=${KEEP:-}
KEEP=
cpu_split
log "$name: movers $movers, $rounds rounds, $RECORDS records; CPUs: mover $MOVER_CPUS, syncbench $AGENT_CPUS, servers $SERVER_CPUS"
arch=$(docker version --format '{{.Server.Arch}}')
# Java 21 dies of SIGILL at its start on arm64 hosts whose CPU (or VM) has
# SVE without saying how long its vectors are, as Apple's M4 under Docker
# Desktop: Kafka Connect (Java 21) and Elasticsearch (Java 22, through
# _JAVA_OPTIONS, since it drops JAVA_TOOL_OPTIONS) are told not to use it
# there; Canal's and DataX's older Javas neither crash nor take the option.
JVM_ENV="" ES_JAVA=""
if [ "$arch" = arm64 ]; then
	JAVA_TOOL_OPTIONS="-XX:UseSVE=0"
	export JAVA_TOOL_OPTIONS
	JVM_ENV="-e JAVA_TOOL_OPTIONS"
	ES_JAVA="-e _JAVA_OPTIONS=-XX:UseSVE=0"
fi
binaries
pull "$BASE_IMAGE"
case $sc in
files) pull "$FILEBEAT_IMAGE" ;;
files-loki) pull "$LOKI_IMAGE" "$PROMTAIL_IMAGE" "$ALLOY_IMAGE" ;;
mysql-es) pull "$MYSQL_IMAGE" "$ES_IMAGE" "$KAFKA_IMAGE" "$CANAL_IMAGE" "$DEBEZIUM_IMAGE" "$JRE_IMAGE" ;;
mysql-kafka) pull "$MYSQL_IMAGE" "$KAFKA_IMAGE" "$CANAL_IMAGE" "$DEBEZIUM_IMAGE" ;;
pg-kafka) pull "$PG_IMAGE" "$KAFKA_IMAGE" "$DEBEZIUM_IMAGE" ;;
mysql-batch) pull "$MYSQL_IMAGE" "$JRE_IMAGE" ;;
rabbitmq-kafka) pull "$RABBITMQ_IMAGE" "$KAFKA_IMAGE" "$DEBEZIUM_IMAGE" ;;
mqtt-kafka) pull "$MOSQUITTO_IMAGE" "$KAFKA_IMAGE" "$DEBEZIUM_IMAGE" ;;
mssql-kafka) pull "$MSSQL_IMAGE" "$KAFKA_IMAGE" "$DEBEZIUM_IMAGE" ;;
mqtt-influxdb) pull "$MOSQUITTO_IMAGE" "$INFLUXDB_IMAGE" "$TELEGRAF_IMAGE" ;;
esac
build_images

failed=0
i=1
while [ $i -le "$rounds" ]; do
	# Alternate the movers' order from round to round.
	list=$(echo "$movers" | tr , '\n')
	[ $((i % 2)) -eq 0 ] && list=$(echo "$list" | sed -n '1!G;h;$p')
	for m in $list; do
		[ $i -eq "$rounds" ] && [ "$KEEP_SAVED" = 1 ] && KEEP=1
		run_one "$i" "$m" || failed=$((failed + 1))
		KEEP=
	done
	i=$((i + 1))
done
[ "$KEEP_SAVED" = 1 ] && KEEP=1

images="-image base=$BASE_IMAGE"
case $sc in
files) images="$images -image filebeat=$FILEBEAT_IMAGE" ;;
files-loki) images="$images -image loki=$LOKI_IMAGE -image promtail=$PROMTAIL_IMAGE -image alloy=$ALLOY_IMAGE" ;;
mysql-es) images="$images -image mysql=$MYSQL_IMAGE -image es=$ES_IMAGE -image kafka=$KAFKA_IMAGE -image canal=$CANAL_IMAGE -image canal-adapter=$ADAPTER_IMAGE -image debezium=$CONNECT_ES_IMAGE" ;;
mysql-kafka) images="$images -image mysql=$MYSQL_IMAGE -image kafka=$KAFKA_IMAGE -image canal=$CANAL_IMAGE -image debezium=$DEBEZIUM_IMAGE" ;;
pg-kafka) images="$images -image postgres=$PG_IMAGE -image kafka=$KAFKA_IMAGE -image debezium=$DEBEZIUM_IMAGE" ;;
mysql-batch) images="$images -image mysql=$MYSQL_IMAGE -image datax=$DATAX_IMAGE" ;;
rabbitmq-kafka) images="$images -image rabbitmq=$RABBITMQ_IMAGE -image kafka=$KAFKA_IMAGE -image kafka-connect=$CONNECT_CAMEL_IMAGE" ;;
mqtt-kafka) images="$images -image mosquitto=$MOSQUITTO_IMAGE -image kafka=$KAFKA_IMAGE -image kafka-connect=$CONNECT_CAMEL_IMAGE" ;;
mssql-kafka) images="$images -image mssql=$MSSQL_IMAGE -image kafka=$KAFKA_IMAGE -image debezium=$DEBEZIUM_IMAGE" ;;
mqtt-influxdb) images="$images -image mosquitto=$MOSQUITTO_IMAGE -image influxdb=$INFLUXDB_IMAGE -image telegraf=$TELEGRAF_IMAGE" ;;
esac
cpu=$(grep -m1 'model name' /proc/cpuinfo 2>/dev/null | cut -d: -f2 | sed 's/^ //' || true)
fit_rev=$(git -C "$root" rev-parse --short HEAD 2>/dev/null || echo "${FIT_REV:-unknown}")
rounds_files=$(cd "$res" && ls r*-*.json 2>/dev/null | sed 's|^|/out/'"$name"'/|' | tr '\n' ' ')
docker network create --label "$LABEL" "$NET" >/dev/null 2>&1 || true
# shellcheck disable=SC2086
agent -- merge -out "/out/$name.json" -name "$name" \
	-host "cpu=${cpu:-$(uname -m)}" -host "cpus=$(docker info --format '{{.NCPU}}')" -host "kernel=$(uname -sr)" \
	-host "docker=$(docker version --format '{{.Server.Version}}')" \
	-param "records=$RECORDS" -param "line_bytes=$LINE_BYTES" -param "files=$FILES" -param "tx_rows=$TX_ROWS" \
	-param "steady_rate=$STEADY_RATE" -param "steady_for=$STEADY_FOR" -param "pause=$PAUSE" -param "rounds=$rounds" \
	-param "mover_cpus=$MOVER_CPUS" -param "agent_cpus=$AGENT_CPUS" -param "server_cpus=$SERVER_CPUS" \
	-param "fit=$fit_rev" -param "size=$SIZE" $images $rounds_files
log "$name: $outdir/$name.json ($failed runs failed)"
