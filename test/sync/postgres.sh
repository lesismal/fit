#!/bin/sh
# Starts PostgreSQL 13 to 17 in Docker for sync's PostgreSQL tests (I177),
# each with TLS (a CA and a server certificate for localhost and 127.0.0.1
# made in the container), SCRAM-SHA-256 for a user fit, and wal_level=logical;
# then prints the environment the tests read:
#
#   FIT_SYNC_PG         13=127.0.0.1:29513,14=127.0.0.1:29514,...
#   FIT_SYNC_PG_CA_DIR  a directory with each server's CA as <version>.pem
#   FIT_SYNC_PG_USER, FIT_SYNC_PG_PASSWORD
#   FIT_SYNC_PG_PREFIX  the containers' prefix, for a test that restarts a server
#
# test/sync/run.sh sources it; alone it starts the servers and prints the
# exports:
#
#   eval "$(test/sync/postgres.sh)"; go test -run Postgres ./test/sync
#   PG_STOP=1 test/sync/postgres.sh   # removes the containers
#
# PG_VERSIONS (13 14 15 16 17), PG_PREFIX (fit-sync-pg), PG_PORT (29500, a
# version's port is PG_PORT plus the version) and PG_CA_DIR choose.
set -eu

PG_VERSIONS=${PG_VERSIONS:-13 14 15 16 17}
PG_PREFIX=${PG_PREFIX:-fit-sync-pg}
PG_PORT=${PG_PORT:-29500}
PG_PASSWORD=${PG_PASSWORD:-Fit-sync-1nterop!}

# The container makes its certificates, gives them to postgres and starts it.
PG_INIT='set -e
d=/var/lib/postgresql/tls
mkdir -p $d && cd $d
if [ ! -f server.crt ]; then
  openssl req -x509 -newkey rsa:2048 -nodes -days 30 -subj /CN=fit-sync-test-ca -keyout ca.key -out ca.crt 2>/dev/null
  openssl req -newkey rsa:2048 -nodes -subj /CN=localhost -keyout server.key -out server.csr 2>/dev/null
  printf "subjectAltName=DNS:localhost,IP:127.0.0.1\n" > ext
  openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 30 -sha256 -extfile ext -out server.crt 2>/dev/null
  chown -R postgres $d && chmod 600 server.key
fi
exec docker-entrypoint.sh postgres -c ssl=on -c ssl_cert_file=$d/server.crt -c ssl_key_file=$d/server.key \
  -c wal_level=logical -c password_encryption=scram-sha-256 -c max_wal_senders=20 -c max_replication_slots=20'

pg_start() {
	for v in $PG_VERSIONS; do
		name=$PG_PREFIX$v
		if ! docker inspect "$name" >/dev/null 2>&1; then
			docker run -d --name "$name" -p "127.0.0.1:$((PG_PORT + v)):5432" \
				-e POSTGRES_USER=fit -e POSTGRES_PASSWORD="$PG_PASSWORD" -e POSTGRES_DB=fit \
				-e POSTGRES_HOST_AUTH_METHOD=scram-sha-256 -e POSTGRES_INITDB_ARGS=--auth-host=scram-sha-256 \
				"postgres:$v-bookworm" sh -c "$PG_INIT" >/dev/null
		fi
	done
	for v in $PG_VERSIONS; do
		name=$PG_PREFIX$v
		i=0
		# The entrypoint's first server listens on no TCP port, so an answer
		# over TCP is the final one.
		until docker exec "$name" pg_isready -q -h 127.0.0.1 -U fit >/dev/null 2>&1; do
			i=$((i + 1))
			if [ $i -gt 120 ]; then
				echo "$name did not start:" >&2
				docker logs --tail 50 "$name" >&2
				exit 1
			fi
			sleep 1
		done
		docker cp "$name:/var/lib/postgresql/tls/ca.crt" "$PG_CA_DIR/$v.pem" >/dev/null
	done
}

pg_env() {
	addrs=
	for v in $PG_VERSIONS; do
		addrs="$addrs${addrs:+,}$v=127.0.0.1:$((PG_PORT + v))"
	done
	echo "FIT_SYNC_PG=$addrs FIT_SYNC_PG_CA_DIR=$PG_CA_DIR FIT_SYNC_PG_USER=fit FIT_SYNC_PG_PASSWORD=$PG_PASSWORD FIT_SYNC_PG_PREFIX=$PG_PREFIX"
}

pg_names() {
	for v in $PG_VERSIONS; do
		printf '%s ' "$PG_PREFIX$v"
	done
}

case "$0" in
*postgres.sh)
	if [ "${PG_STOP:-}" = 1 ]; then
		docker rm -f $(pg_names) >/dev/null 2>&1 || true
		exit 0
	fi
	PG_CA_DIR=${PG_CA_DIR:-$(mktemp -d)}
	pg_start
	for kv in $(pg_env); do
		echo "export '$kv'"
	done
	;;
esac
