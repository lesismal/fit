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
# host ports (29540: 29540 and 29541 the brokers, 29550 and 29551 their web
# services); PULSAR_VERSIONS picks the servers ("3 4"). The tests read FIT_SYNC_PULSAR_3_URL, FIT_SYNC_PULSAR_3_ADMIN,
# FIT_SYNC_PULSAR_4_URL and FIT_SYNC_PULSAR_4_ADMIN.
set -eu
cd "$(dirname "$0")/../.."

PREFIX=${PREFIX:-fit-sync-}
PULSAR3_IMAGE=${PULSAR3_IMAGE:-apachepulsar/pulsar:3.0.11}
PULSAR4_IMAGE=${PULSAR4_IMAGE:-apachepulsar/pulsar:4.0.5}
PULSAR_PORT=${PULSAR_PORT:-29540}
PULSAR_VERSIONS=${PULSAR_VERSIONS:-3 4}

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

for v in $PULSAR_VERSIONS; do
	o=$((v - 3))
	eval "image=\$PULSAR${v}_IMAGE"
	start "${PREFIX}pulsar$v" "$image" $((PULSAR_PORT + o)) $((PULSAR_PORT + 10 + o))
done

# Ready once the admin API lists public's namespaces (default among them).
for v in $PULSAR_VERSIONS; do
	web=$((PULSAR_PORT + 7 + v))
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

for v in $PULSAR_VERSIONS; do
	echo "export FIT_SYNC_PULSAR_${v}_URL=pulsar://127.0.0.1:$((PULSAR_PORT + v - 3))"
	echo "export FIT_SYNC_PULSAR_${v}_ADMIN=http://127.0.0.1:$((PULSAR_PORT + 7 + v))"
done
