#!/bin/sh
# Starts the MQTT brokers of sync's interop tests in Docker, and prints the
# environment the tests read, as `export` lines:
#
#   - Mosquitto 2, persistence on, with three listeners, a password file
#     (user fit) checked on each of them and anonymous clients let in, the
#     third over TLS (a CA of the script's own). Not per_listener_settings:
#     with it, Mosquitto 2 drops the messages of a persistent session while
#     its client is away;
#   - EMQX 5, one node, anonymous.
#
#   eval "$(test/sync/mqtt.sh up)"     start (or reuse) them, print the env
#   test/sync/mqtt.sh down             remove them
#
# PREFIX names the containers (fit-sync-); MQTT_PORT is the first of the
# host ports (29450: Mosquitto's three, then EMQX's at 29455). The tests
# read FIT_SYNC_MQTT_MOSQUITTO, FIT_SYNC_MQTT_MOSQUITTO_AUTH,
# FIT_SYNC_MQTT_MOSQUITTO_TLS, FIT_SYNC_MQTT_CA, FIT_SYNC_MQTT_USER,
# FIT_SYNC_MQTT_PASSWORD and FIT_SYNC_MQTT_EMQX. EMQX=0 leaves EMQX out, and
# its variable.
set -eu
cd "$(dirname "$0")/../.."

PREFIX=${PREFIX:-fit-sync-}
MOSQUITTO_IMAGE=${MOSQUITTO_IMAGE:-eclipse-mosquitto:2.0.22}
EMQX_IMAGE=${EMQX_IMAGE:-emqx/emqx:5.8.6}
MQTT_PORT=${MQTT_PORT:-29450}
PASSWORD='Fit-sync-1nterop!'
STATE=${TMPDIR:-/tmp}/${PREFIX}mqtt

if [ "${1:-}" = down ]; then
	docker rm -f "${PREFIX}mosquitto" "${PREFIX}emqx" >/dev/null 2>&1 || true
	rm -rf "$STATE"
	exit 0
fi

# A CA and the broker's certificate for localhost, Mosquitto's config and
# its password file.
if [ ! -f "$STATE/passwd" ]; then
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
		cat >mosquitto.conf <<EOF
persistence true
persistence_location /mosquitto/data/
max_queued_messages 100000
max_inflight_messages 1000
allow_anonymous true
password_file /fit/passwd

listener 1883

listener 1884

listener 8883
cafile /fit/ca.pem
certfile /fit/server.pem
keyfile /fit/server.key
EOF
		: >passwd
		chmod 755 . && chmod 644 ./*
	)
	docker run --rm -v "$STATE:/fit" "$MOSQUITTO_IMAGE" \
		sh -c "mosquitto_passwd -b /fit/passwd fit '$PASSWORD' && chmod 644 /fit/passwd" >/dev/null 2>&1
fi

M=${PREFIX}mosquitto
if ! docker inspect "$M" >/dev/null 2>&1; then
	docker run -d --name "$M" -v "$STATE:/fit:ro" \
		-p "127.0.0.1:$MQTT_PORT:1883" -p "127.0.0.1:$((MQTT_PORT + 1)):1884" -p "127.0.0.1:$((MQTT_PORT + 2)):8883" \
		"$MOSQUITTO_IMAGE" mosquitto -c /fit/mosquitto.conf >/dev/null
fi

E=${PREFIX}emqx
EMQX=${EMQX:-1}
if [ "$EMQX" = 1 ] && ! docker inspect "$E" >/dev/null 2>&1; then
	docker run -d --name "$E" -p "127.0.0.1:$((MQTT_PORT + 5)):1883" \
		-e EMQX_MQTT__MAX_INFLIGHT=1000 -e EMQX_MQTT__MAX_MQUEUE_LEN=100000 \
		"$EMQX_IMAGE" >/dev/null
fi

i=0
until docker logs "$M" 2>&1 | grep -q 'running'; do
	i=$((i + 1))
	if [ $i -gt 60 ]; then
		echo "Mosquitto did not start:" >&2
		docker logs --tail 50 "$M" >&2
		exit 1
	fi
	sleep 1
done
echo "export FIT_SYNC_MQTT_MOSQUITTO='localhost:$MQTT_PORT' FIT_SYNC_MQTT_MOSQUITTO_AUTH='localhost:$((MQTT_PORT + 1))' FIT_SYNC_MQTT_MOSQUITTO_TLS='localhost:$((MQTT_PORT + 2))'"
echo "export FIT_SYNC_MQTT_CA='$STATE/ca.pem' FIT_SYNC_MQTT_USER=fit FIT_SYNC_MQTT_PASSWORD='$PASSWORD'"
[ "$EMQX" = 1 ] || exit 0
i=0
until docker exec "$E" emqx ctl status 2>/dev/null | grep -q 'is started'; do
	i=$((i + 1))
	if [ $i -gt 120 ]; then
		echo "EMQX did not start:" >&2
		docker logs --tail 50 "$E" >&2
		exit 1
	fi
	sleep 1
done
echo "export FIT_SYNC_MQTT_EMQX='localhost:$((MQTT_PORT + 5))'"
