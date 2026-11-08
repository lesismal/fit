#!/bin/sh
# Starts the Kafka servers of sync's interop tests in Docker, and prints the
# environment the tests read, as `export` lines:
#
#   - Kafka (KRaft), three brokers that are also the controllers, on a
#     network of their own, each with three listeners for the host:
#     PLAINTEXT, SASL_PLAINTEXT (PLAIN, SCRAM-SHA-256 and SCRAM-SHA-512) and
#     SASL_SSL (a CA of the script's own), so that a test can stop a
#     partition's leader and see the client follow the new one;
#   - Redpanda, one node, with a PLAINTEXT listener and a SASL one
#     (PLAIN and SCRAM-SHA-256).
#
#   eval "$(test/sync/kafka.sh up)"     start (or reuse) them, print the env
#   test/sync/kafka.sh down             remove them
#
# PREFIX names the containers and the network (fit-sync-); KAFKA_PORT and
# REDPANDA_PORT are the first of the host ports (29210, nine of them, and
# 29220, two). The tests read FIT_SYNC_KAFKA_BROKERS, FIT_SYNC_KAFKA_SASL_BROKERS,
# FIT_SYNC_KAFKA_TLS_BROKERS, FIT_SYNC_KAFKA_CA, FIT_SYNC_KAFKA_USER,
# FIT_SYNC_KAFKA_PASSWORD, FIT_SYNC_KAFKA_CONTAINERS (node=container, for
# stopping a leader) and FIT_SYNC_REDPANDA_BROKERS, FIT_SYNC_REDPANDA_SASL_BROKERS.
set -eu
cd "$(dirname "$0")/../.."

PREFIX=${PREFIX:-fit-sync-}
KAFKA_IMAGE=${KAFKA_IMAGE:-apache/kafka:3.9.1}
REDPANDA_IMAGE=${REDPANDA_IMAGE:-redpandadata/redpanda:v25.2.9}
KAFKA_PORT=${KAFKA_PORT:-29210}
REDPANDA_PORT=${REDPANDA_PORT:-29220}
PASSWORD='Fit-sync-1nterop!'
NET=${PREFIX}kafka-net
STATE=${TMPDIR:-/tmp}/${PREFIX}kafka

if [ "${1:-}" = down ]; then
	docker rm -f "${PREFIX}kafka1" "${PREFIX}kafka2" "${PREFIX}kafka3" "${PREFIX}redpanda" >/dev/null 2>&1 || true
	docker network rm "$NET" >/dev/null 2>&1 || true
	rm -rf "$STATE"
	exit 0
fi

# A CA and the brokers' certificate for localhost, and the JAAS file of
# the SASL listeners: PLAIN's user, and SCRAM's module (its users are in
# the cluster's metadata, added below).
if [ ! -f "$STATE/keystore.pem" ]; then
	mkdir -p "$STATE"
	(
		cd "$STATE"
		openssl req -x509 -newkey rsa:2048 -nodes -days 30 -subj /CN=fit-sync-ca \
			-keyout ca.key -out ca.pem >/dev/null 2>&1
		openssl req -newkey rsa:2048 -nodes -subj /CN=localhost \
			-keyout server.key -out server.csr >/dev/null 2>&1
		printf 'subjectAltName=DNS:localhost,IP:127.0.0.1\n' >ext.cnf
		openssl x509 -req -in server.csr -CA ca.pem -CAkey ca.key -CAcreateserial -days 30 \
			-extfile ext.cnf -out server.pem >/dev/null 2>&1
		openssl pkcs8 -topk8 -nocrypt -in server.key -out server.p8
		cat server.p8 server.pem >keystore.pem
		cat >jaas.conf <<EOF
KafkaServer {
  org.apache.kafka.common.security.plain.PlainLoginModule required user_fit="$PASSWORD";
  org.apache.kafka.common.security.scram.ScramLoginModule required;
};
EOF
		chmod 755 . && chmod 644 ./*
	)
fi

docker network inspect "$NET" >/dev/null 2>&1 || docker network create "$NET" >/dev/null
VOTERS="1@${PREFIX}kafka1:9093,2@${PREFIX}kafka2:9093,3@${PREFIX}kafka3:9093"
BROKERS="" SASL_BROKERS="" TLS_BROKERS="" CONTAINERS=""
for i in 1 2 3; do
	name=${PREFIX}kafka$i
	p=$((KAFKA_PORT + (i - 1) * 3))
	BROKERS="$BROKERS${BROKERS:+,}localhost:$p"
	SASL_BROKERS="$SASL_BROKERS${SASL_BROKERS:+,}localhost:$((p + 1))"
	TLS_BROKERS="$TLS_BROKERS${TLS_BROKERS:+,}localhost:$((p + 2))"
	CONTAINERS="$CONTAINERS${CONTAINERS:+,}$i=$name"
	docker inspect "$name" >/dev/null 2>&1 && continue
	docker run -d --name "$name" --hostname "$name" --network "$NET" \
		-p "127.0.0.1:$p:9092" -p "127.0.0.1:$((p + 1)):9094" -p "127.0.0.1:$((p + 2)):9095" \
		-v "$STATE:/etc/kafka/fit:ro" \
		-e CLUSTER_ID=Zml0LXN5bmMta2Fma2EtMQ \
		-e KAFKA_NODE_ID=$i -e KAFKA_PROCESS_ROLES=broker,controller \
		-e KAFKA_CONTROLLER_QUORUM_VOTERS="$VOTERS" \
		-e KAFKA_LISTENERS=INTERNAL://:19092,CONTROLLER://:9093,PLAIN://:9092,SASL://:9094,TLS://:9095 \
		-e KAFKA_ADVERTISED_LISTENERS="INTERNAL://$name:19092,PLAIN://localhost:$p,SASL://localhost:$((p + 1)),TLS://localhost:$((p + 2))" \
		-e KAFKA_LISTENER_SECURITY_PROTOCOL_MAP=INTERNAL:PLAINTEXT,CONTROLLER:PLAINTEXT,PLAIN:PLAINTEXT,SASL:SASL_PLAINTEXT,TLS:SASL_SSL \
		-e KAFKA_INTER_BROKER_LISTENER_NAME=INTERNAL -e KAFKA_CONTROLLER_LISTENER_NAMES=CONTROLLER \
		-e KAFKA_SASL_ENABLED_MECHANISMS=PLAIN,SCRAM-SHA-256,SCRAM-SHA-512 \
		-e KAFKA_SSL_KEYSTORE_TYPE=PEM -e KAFKA_SSL_KEYSTORE_LOCATION=/etc/kafka/fit/keystore.pem \
		-e KAFKA_OPTS=-Djava.security.auth.login.config=/etc/kafka/fit/jaas.conf \
		-e KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR=3 -e KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR=3 \
		-e KAFKA_TRANSACTION_STATE_LOG_MIN_ISR=2 -e KAFKA_LEADER_IMBALANCE_CHECK_INTERVAL_SECONDS=5 \
		-e KAFKA_AUTO_CREATE_TOPICS_ENABLE=false \
		-e KAFKA_HEAP_OPTS="-Xms256m -Xmx512m" \
		"$KAFKA_IMAGE" >/dev/null
done

RP=${PREFIX}redpanda
if ! docker inspect "$RP" >/dev/null 2>&1; then
	docker run -d --name "$RP" -p "127.0.0.1:$REDPANDA_PORT:9092" -p "127.0.0.1:$((REDPANDA_PORT + 1)):9093" \
		"$REDPANDA_IMAGE" redpanda start --mode dev-container --smp 1 --memory 1G --overprovisioned \
		--kafka-addr 'PLAIN://0.0.0.0:9092,SASL://0.0.0.0:9093|sasl' \
		--advertise-kafka-addr "PLAIN://localhost:$REDPANDA_PORT,SASL://localhost:$((REDPANDA_PORT + 1))" >/dev/null
fi

# Wait for the cluster, adding the SCRAM user until it takes it, then
# Redpanda's.
i=0
# scram_user mechanism: one per request, which is all Kafka 3.9 takes.
scram_user() {
	docker exec "${PREFIX}kafka1" /opt/kafka/bin/kafka-configs.sh --bootstrap-server "${PREFIX}kafka1:19092" \
		--alter --add-config "$1=[password=$PASSWORD]" --entity-type users --entity-name fit >/dev/null 2>&1
}
until scram_user SCRAM-SHA-256 && scram_user SCRAM-SHA-512; do
	i=$((i + 1))
	if [ $i -gt 150 ]; then
		echo "Kafka did not start:" >&2
		docker logs --tail 50 "${PREFIX}kafka1" >&2
		exit 1
	fi
	sleep 2
done
i=0
until docker exec "$RP" rpk cluster health 2>/dev/null | grep -q 'Healthy:.*true'; do
	i=$((i + 1))
	if [ $i -gt 90 ]; then
		echo "Redpanda did not start:" >&2
		docker logs --tail 50 "$RP" >&2
		exit 1
	fi
	sleep 1
done
docker exec "$RP" rpk security user create fit -p "$PASSWORD" --mechanism SCRAM-SHA-256 >/dev/null 2>&1 || true
docker exec "$RP" rpk cluster config set superusers '["fit"]' >/dev/null
docker exec "$RP" rpk cluster config set sasl_mechanisms '["SCRAM","PLAIN"]' >/dev/null

echo "export FIT_SYNC_KAFKA_BROKERS='$BROKERS' FIT_SYNC_KAFKA_SASL_BROKERS='$SASL_BROKERS' FIT_SYNC_KAFKA_TLS_BROKERS='$TLS_BROKERS'"
echo "export FIT_SYNC_KAFKA_CA='$STATE/ca.pem' FIT_SYNC_KAFKA_USER=fit FIT_SYNC_KAFKA_PASSWORD='$PASSWORD' FIT_SYNC_KAFKA_CONTAINERS='$CONTAINERS'"
echo "export FIT_SYNC_REDPANDA_BROKERS='localhost:$REDPANDA_PORT' FIT_SYNC_REDPANDA_SASL_BROKERS='localhost:$((REDPANDA_PORT + 1))'"
