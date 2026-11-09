#!/bin/sh
# Starts the NATS servers of sync's interop tests in Docker, and prints the
# environment the tests read, as `export` lines: nats-server 2.10 and 2.11,
# each a cluster of three nodes with JetStream, on a network of its own,
# clients authenticated as a user with a password; the clusters do not
# advertise their nodes' inside addresses, so that a client on the host
# reconnects only to the ports it was given.
#
#   eval "$(test/sync/nats.sh up)"     start (or reuse) them, print the env
#   test/sync/nats.sh down             remove them
#
# PREFIX names the containers and the networks (fit-sync-); NATS_PORT is
# the first of the host ports (29420: 2.10's nodes on 29420-29422, 2.11's on
# 29423-29425). The tests read FIT_SYNC_NATS_210 and FIT_SYNC_NATS_211 (each
# cluster's servers, host:port, comma separated), FIT_SYNC_NATS_USER,
# FIT_SYNC_NATS_PASSWORD, and FIT_SYNC_NATS_CONTAINERS_210 and _211 (the
# nodes' containers, for restarting one).
set -eu
cd "$(dirname "$0")/../.."

PREFIX=${PREFIX:-fit-sync-}
NATS_PORT=${NATS_PORT:-29420}
PASSWORD='Fit-sync-1nterop!'
STATE=${TMPDIR:-/tmp}/${PREFIX}nats
VERSIONS="210:nats:2.10-alpine 211:nats:2.11-alpine"

if [ "${1:-}" = down ]; then
	for v in 210 211; do
		for i in 1 2 3; do
			docker rm -f "${PREFIX}nats$v-$i" >/dev/null 2>&1 || true
		done
		docker network rm "${PREFIX}nats$v-net" >/dev/null 2>&1 || true
	done
	rm -rf "$STATE"
	exit 0
fi

mkdir -p "$STATE"
k=0
for entry in $VERSIONS; do
	v=${entry%%:*}
	image=${entry#*:}
	net=${PREFIX}nats$v-net
	docker network inspect "$net" >/dev/null 2>&1 || docker network create "$net" >/dev/null
	routes=""
	for i in 1 2 3; do
		routes="$routes${routes:+, }nats://${PREFIX}nats$v-$i:6222"
	done
	servers="" containers=""
	for i in 1 2 3; do
		name=${PREFIX}nats$v-$i
		p=$((NATS_PORT + k))
		k=$((k + 1))
		servers="$servers${servers:+,}localhost:$p"
		containers="$containers${containers:+,}$name"
		cat >"$STATE/$name.conf" <<EOF
server_name: n$i
port: 4222
http_port: 8222
max_payload: 1MB
jetstream { store_dir: /data }
authorization { user: fit, password: "$PASSWORD" }
cluster {
  name: c$v
  port: 6222
  no_advertise: true
  routes: [ $routes ]
}
EOF
		docker inspect "$name" >/dev/null 2>&1 && continue
		docker run -d --name "$name" --hostname "$name" --network "$net" -p "127.0.0.1:$p:4222" \
			-v "$STATE/$name.conf:/etc/nats/fit.conf:ro" "$image" -c /etc/nats/fit.conf >/dev/null
	done
	eval "SERVERS_$v='$servers' CONTAINERS_$v='$containers'"
done

# Wait until each cluster's JetStream has a meta leader: the server's
# healthz says so once JetStream is ready.
for v in 210 211; do
	i=0
	until docker exec "${PREFIX}nats$v-1" wget -qO- 'http://127.0.0.1:8222/healthz?js-enabled-only=true' >/dev/null 2>&1 ||
		docker logs "${PREFIX}nats$v-1" 2>&1 | grep -q 'JetStream cluster new metadata leader\|is the new metadata leader\|Self is new JetStream cluster metadata leader'; do
		i=$((i + 1))
		if [ $i -gt 60 ]; then
			echo "NATS $v did not start:" >&2
			docker logs --tail 50 "${PREFIX}nats$v-1" >&2
			exit 1
		fi
		sleep 1
	done
done
echo "export FIT_SYNC_NATS_210='$SERVERS_210' FIT_SYNC_NATS_211='$SERVERS_211' FIT_SYNC_NATS_USER=fit FIT_SYNC_NATS_PASSWORD='$PASSWORD'"
echo "export FIT_SYNC_NATS_CONTAINERS_210='$CONTAINERS_210' FIT_SYNC_NATS_CONTAINERS_211='$CONTAINERS_211'"
