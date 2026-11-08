#!/bin/sh
# Helpers for MongoDB's interop servers, sourced by test/sync/run.sh, or
# run alone to start (or, with "rm", remove) one replica set per version:
#
#   test/sync/mongo.sh PREFIX BASE_PORT CERT_DIR     # prints the env to export
#   test/sync/mongo.sh rm PREFIX
#
# Each version is a replica set of three members in one container, each
# member on a port that is the same inside and out (a replica set's
# members are known by their host:port, which the client must reach),
# with keyfile authentication, a root user fit, and TLS preferred (with a
# CA of its own; connections without TLS are taken too).

MONGO_VERSIONS="6 7 8"
MONGO_PASSWORD=${MONGO_PASSWORD:-'Fit-sync-1nterop!'}

# make_mongo_certs dir: a CA and a server certificate for localhost and
# 127.0.0.1.
make_mongo_certs() {
	d=$1
	openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj /CN=fit-test-ca \
		-keyout "$d/ca.key" -out "$d/ca.pem" 2>/dev/null
	openssl req -newkey rsa:2048 -nodes -subj /CN=localhost \
		-keyout "$d/server.key" -out "$d/server.csr" 2>/dev/null
	printf 'subjectAltName=DNS:localhost,IP:127.0.0.1\n' >"$d/ext"
	openssl x509 -req -in "$d/server.csr" -CA "$d/ca.pem" -CAkey "$d/ca.key" -CAcreateserial \
		-days 2 -extfile "$d/ext" -out "$d/server.crt" 2>/dev/null
	cat "$d/server.crt" "$d/server.key" >"$d/server.pem"
	chmod 644 "$d"/*
}

# start_mongo name image set port1 port2 port3 certdir
start_mongo() {
	name=$1 image=$2 rs=$3 p1=$4 p2=$5 p3=$6 certs=$7
	if docker inspect "$name" >/dev/null 2>&1; then
		return
	fi
	docker run -d --name "$name" -p "127.0.0.1:$p1:$p1" -p "127.0.0.1:$p2:$p2" -p "127.0.0.1:$p3:$p3" \
		--entrypoint sh "$image" -c "
mkdir -p /certs && while [ ! -s /certs/server.pem ]; do sleep 0.2; done &&
head -c 500 /dev/urandom | base64 > /tmp/kf && chmod 400 /tmp/kf &&
for p in $p1 $p2 $p3; do
	mkdir -p /data/db/\$p &&
	mongod --replSet $rs --port \$p --bind_ip_all --keyFile /tmp/kf --dbpath /data/db/\$p \
		--wiredTigerCacheSizeGB 0.25 --tlsMode preferTLS --tlsCertificateKeyFile /certs/server.pem \
		--tlsCAFile /certs/ca.pem --tlsAllowConnectionsWithoutCertificates \
		--fork --logpath /data/db/\$p.log || exit 1
done
exec tail -f /dev/null" >/dev/null
	# Copied in, not mounted: Docker Desktop need not share the directory.
	docker cp "$certs/ca.pem" "$name:/certs/ca.pem" >/dev/null 2>&1 ||
		{ sleep 1; docker cp "$certs/ca.pem" "$name:/certs/ca.pem" >/dev/null; }
	docker cp "$certs/server.pem" "$name:/certs/server.pem" >/dev/null
	i=0
	until docker exec "$name" mongosh --quiet --port "$p1" --eval 1 >/dev/null 2>&1; do
		i=$((i + 1))
		if [ $i -gt 90 ]; then
			echo "$name did not start:" >&2
			docker logs --tail 50 "$name" >&2
			return 1
		fi
		sleep 1
	done
	docker exec "$name" mongosh --quiet --port "$p1" --eval "rs.initiate({_id: '$rs', members: [
		{_id: 0, host: 'localhost:$p1'}, {_id: 1, host: 'localhost:$p2'}, {_id: 2, host: 'localhost:$p3'}]})" >/dev/null
	i=0
	until docker exec "$name" mongosh --quiet "mongodb://localhost:$p1,localhost:$p2,localhost:$p3/?replicaSet=$rs" \
		--eval "db.getSiblingDB('admin').createUser({user: 'fit', pwd: '$MONGO_PASSWORD', roles: ['root']})" >/dev/null 2>&1; do
		i=$((i + 1))
		if [ $i -gt 90 ]; then
			echo "$name: no primary to make the user on" >&2
			return 1
		fi
		sleep 1
	done
}

# start_mongos prefix base_port certdir: one set per version, version v on
# ports base+10*v+1..3 (base 29600: 29661-29663, 29671-29673, 29681-29683);
# prints the FIT_SYNC_MONGO_* variables.
start_mongos() {
	for v in $MONGO_VERSIONS; do
		b=$(($2 + 10 * v))
		start_mongo "$1-mongo$v" "mongo:$v.0" "rs$v" $((b + 1)) $((b + 2)) $((b + 3)) "$3" || return 1
		echo "FIT_SYNC_MONGO_$v=mongodb://fit:$(printf %s "$MONGO_PASSWORD" | sed 's/!/%21/g')@localhost:$((b + 1)),localhost:$((b + 2)),localhost:$((b + 3))/?replicaSet=rs$v"
	done
	echo "FIT_SYNC_MONGO_CA=$3/ca.pem"
}

rm_mongos() {
	for v in $MONGO_VERSIONS; do
		docker rm -f "$1-mongo$v" >/dev/null 2>&1 || true
	done
}

case "${0##*/}" in
mongo.sh)
	set -eu
	if [ "${1:-}" = rm ]; then
		rm_mongos "$2"
	else
		mkdir -p "$3"
		[ -s "$3/server.pem" ] || make_mongo_certs "$3"
		start_mongos "$1" "$2" "$3"
	fi
	;;
esac
