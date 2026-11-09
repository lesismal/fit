#!/bin/sh
# Starts the RabbitMQ servers of sync's interop tests in Docker, and prints
# the environment the tests read, as `export` lines: RabbitMQ 3.13 and 4.1,
# one node each, with a user of their own (no guest), a plain listener and a
# TLS one of a CA the script makes. Stream queues need no plugin over AMQP
# 0-9-1.
#
#   eval "$(test/sync/rabbitmq.sh up)"     start (or reuse) them, print the env
#   test/sync/rabbitmq.sh down             remove them
#
# PREFIX names the containers (fit-sync-); RABBITMQ_PORT is the first of the
# host ports (29420, four of them: each version's plain and TLS ports). The
# tests read FIT_SYNC_RABBITMQ_ADDRS and FIT_SYNC_RABBITMQ_TLS_ADDRS
# (version=host:port, comma separated), FIT_SYNC_RABBITMQ_CA,
# FIT_SYNC_RABBITMQ_USER and FIT_SYNC_RABBITMQ_PASSWORD.
set -eu
cd "$(dirname "$0")/../.."

PREFIX=${PREFIX:-fit-sync-}
RABBITMQ_IMAGES=${RABBITMQ_IMAGES:-"3.13=rabbitmq:3.13 4.1=rabbitmq:4.1"}
RABBITMQ_PORT=${RABBITMQ_PORT:-29420}
PASSWORD='Fit-sync-1nterop!'
STATE=${TMPDIR:-/tmp}/${PREFIX}rabbitmq

if [ "${1:-}" = down ]; then
	for v in $RABBITMQ_IMAGES; do
		docker rm -f "${PREFIX}rabbitmq-${v%%=*}" >/dev/null 2>&1 || true
	done
	rm -rf "$STATE"
	exit 0
fi

if [ ! -f "$STATE/server.pem" ]; then
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
		cat >10-fit.conf <<EOF
listeners.tcp.default = 5672
listeners.ssl.default = 5671
ssl_options.cacertfile = /etc/rabbitmq/fit/ca.pem
ssl_options.certfile = /etc/rabbitmq/fit/server.pem
ssl_options.keyfile = /etc/rabbitmq/fit/server.key
ssl_options.verify = verify_none
ssl_options.fail_if_no_peer_cert = false
default_user = fit
default_pass = $PASSWORD
EOF
		chmod 755 . && chmod 644 ./*
	)
fi

ADDRS="" TLS_ADDRS="" p=$RABBITMQ_PORT
for v in $RABBITMQ_IMAGES; do
	version=${v%%=*} image=${v#*=}
	name=${PREFIX}rabbitmq-$version
	ADDRS="$ADDRS${ADDRS:+,}$version=localhost:$p"
	TLS_ADDRS="$TLS_ADDRS${TLS_ADDRS:+,}$version=localhost:$((p + 1))"
	if ! docker inspect "$name" >/dev/null 2>&1; then
		# Copied in, not mounted: the image's entrypoint takes /etc/rabbitmq
		# for its user, and a read-only mount there left the node unable to
		# read its own .erlang.cookie (eacces).
		docker create --name "$name" --hostname "$name" \
			-p "127.0.0.1:$p:5672" -p "127.0.0.1:$((p + 1)):5671" \
			"$image" >/dev/null
		docker cp "$STATE/." "$name:/etc/rabbitmq/fit" >/dev/null
		docker cp "$STATE/10-fit.conf" "$name:/etc/rabbitmq/conf.d/10-fit.conf" >/dev/null
		docker start "$name" >/dev/null
	fi
	p=$((p + 2))
done

for v in $RABBITMQ_IMAGES; do
	name=${PREFIX}rabbitmq-${v%%=*}
	i=0
	until docker exec "$name" rabbitmqctl await_startup >/dev/null 2>&1; do
		i=$((i + 1))
		if [ $i -gt 90 ]; then
			echo "$name did not start:" >&2
			docker logs --tail 50 "$name" >&2
			exit 1
		fi
		sleep 2
	done
done
echo "export FIT_SYNC_RABBITMQ_ADDRS='$ADDRS' FIT_SYNC_RABBITMQ_TLS_ADDRS='$TLS_ADDRS'"
echo "export FIT_SYNC_RABBITMQ_CA='$STATE/ca.pem' FIT_SYNC_RABBITMQ_USER=fit FIT_SYNC_RABBITMQ_PASSWORD='$PASSWORD'"
