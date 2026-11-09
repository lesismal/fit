#!/bin/sh
# Starts the Pulsar servers of sync's interop tests in Docker, and prints the
# environment the tests read, as `export` lines: Pulsar 3.0 and 4.0, each a
# standalone (a broker, a bookie and its metadata in one container) with
# broker deduplication on, advertised on 127.0.0.1 at the host's own port,
# so that a lookup's answer is a host port the tests reach.
#
#   eval "$(test/sync/pulsar.sh up)"     start (or reuse) them, print the env
#   test/sync/pulsar.sh down             remove them
#
# PREFIX names the containers (fit-sync-); PULSAR_PORT is the first of the
# host ports (29500: 29500 and 29501 the brokers, 29510 and 29511 their web
# services). The tests read FIT_SYNC_PULSAR_3_URL, FIT_SYNC_PULSAR_3_ADMIN,
# FIT_SYNC_PULSAR_4_URL and FIT_SYNC_PULSAR_4_ADMIN.
set -eu
cd "$(dirname "$0")/../.."

PREFIX=${PREFIX:-fit-sync-}
PULSAR3_IMAGE=${PULSAR3_IMAGE:-apachepulsar/pulsar:3.0.11}
PULSAR4_IMAGE=${PULSAR4_IMAGE:-apachepulsar/pulsar:4.0.5}
PULSAR_PORT=${PULSAR_PORT:-29500}

if [ "${1:-}" = down ]; then
	docker rm -f "${PREFIX}pulsar3" "${PREFIX}pulsar4" >/dev/null 2>&1 || true
	exit 0
fi

# start name image broker-port web-port
start() {
	if [ -z "$(docker ps -q -f "name=^$1\$")" ]; then
		docker rm -f "$1" >/dev/null 2>&1 || true
		docker run -d --name "$1" -p "$3:$3" -p "$4:$4" \
			-e PULSAR_PREFIX_brokerDeduplicationEnabled=true \
			-e PULSAR_PREFIX_advertisedAddress=127.0.0.1 \
			-e PULSAR_PREFIX_brokerServicePort="$3" \
			-e PULSAR_PREFIX_webServicePort="$4" \
			-e PULSAR_MEM="-Xms512m -Xmx512m -XX:MaxDirectMemorySize=512m" \
			"$2" sh -c 'bin/apply-config-from-env.py conf/standalone.conf && exec bin/pulsar standalone -nss -nfw' >/dev/null
	fi
}

start "${PREFIX}pulsar3" "$PULSAR3_IMAGE" "$PULSAR_PORT" $((PULSAR_PORT + 10))
start "${PREFIX}pulsar4" "$PULSAR4_IMAGE" $((PULSAR_PORT + 1)) $((PULSAR_PORT + 11))

# Ready once the admin API lists public's namespaces (default among them).
for web in $((PULSAR_PORT + 10)) $((PULSAR_PORT + 11)); do
	i=0
	until curl -fsS "http://127.0.0.1:$web/admin/v2/namespaces/public" 2>/dev/null | grep -q default; do
		i=$((i + 1))
		if [ "$i" -gt 180 ]; then
			echo "pulsar on $web did not start" >&2
			exit 1
		fi
		sleep 1
	done
done

echo "export FIT_SYNC_PULSAR_3_URL=pulsar://127.0.0.1:$PULSAR_PORT"
echo "export FIT_SYNC_PULSAR_3_ADMIN=http://127.0.0.1:$((PULSAR_PORT + 10))"
echo "export FIT_SYNC_PULSAR_4_URL=pulsar://127.0.0.1:$((PULSAR_PORT + 1))"
echo "export FIT_SYNC_PULSAR_4_ADMIN=http://127.0.0.1:$((PULSAR_PORT + 11))"
