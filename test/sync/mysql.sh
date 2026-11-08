#!/bin/sh
# Starts the MySQL and MariaDB servers sync's MySQL tests run against, in
# Docker: MySQL 5.7, 8.0 and 8.4, MariaDB 10.11 and 11, each with TLS from a
# CA made here, a root password, mysql_native_password enabled (8.4), a
# max_allowed_packet of 64 MiB, the binlog on in row format (GTIDs on for
# MySQL; MariaDB has its own always) and secure_file_priv off, so that a
# test can read a binlog's file with LOAD_FILE. test/sync/run.sh sources
# it; run alone, it starts them, runs the MySQL tests (those of test/sync
# and sync/mysql/binlog) and removes them (KEEP=1 keeps them, and a second
# run reuses them):
#
#   test/sync/mysql.sh [go test flags...]
#
# The tests read FIT_SYNC_MYSQL, a list of name=host:port, with
# FIT_SYNC_MYSQL_PASSWORD (root's) and FIT_SYNC_MYSQL_CA (the CA's file);
# without them they skip; FIT_SYNC_MYSQL_PREFIX is MYSQL_PREFIX, by which the
# source's test restarts a server. MYSQL_PREFIX names the containers (fit-sync-my),
# MYSQL_PORT is the first of five host ports (29210), MYSQL_SERVERS picks
# some of them ("mysql57 mysql80 mysql84 mariadb1011 mariadb11").
#
# TiDB (TIDB_IMAGE, pingcap/tidb:v8.5.3, standalone on its own storage) runs
# beside them as $MYSQL_PREFIX-tidb on TIDB_PORT (29217), root without a
# password and without TLS, for the mysql sink's tests, which read
# FIT_SYNC_TIDB (host:port); TIDB=0 leaves it out.

MYSQL_PREFIX=${MYSQL_PREFIX:-fit-sync-my}
MYSQL_PORT=${MYSQL_PORT:-29210}
MYSQL_SERVERS=${MYSQL_SERVERS:-mysql57 mysql80 mysql84 mariadb1011 mariadb11}
MYSQL_PASSWORD='Fit-sync-1nterop!'
MYSQL_CERTS=${MYSQL_CERTS:-$(mktemp -d)}
TIDB=${TIDB:-1}
TIDB_IMAGE=${TIDB_IMAGE:-pingcap/tidb:v8.5.3}
TIDB_PORT=${TIDB_PORT:-29217}

# mysql_image name: the server's image and its options.
mysql_image() {
	case $1 in
	mysql57) echo "--platform linux/amd64 mysql:5.7" ;;
	mysql80) echo "mysql:8.0" ;;
	mysql84) echo "mysql:8.4" ;;
	mariadb1011) echo "mariadb:10.11" ;;
	mariadb11) echo "mariadb:11" ;;
	esac
}

mysql_certs() {
	# The servers run as their image's mysql user, whose ID is not the
	# host user's: on a Linux host the bind-mounted directory (mktemp makes
	# it 0700) and the files must be readable by others. Docker Desktop's
	# file sharing hides this on macOS.
	mkdir -p "$MYSQL_CERTS"
	chmod 755 "$MYSQL_CERTS"
	[ -f "$MYSQL_CERTS/ca.pem" ] || (
		cd "$MYSQL_CERTS"
		openssl req -x509 -newkey rsa:2048 -nodes -days 30 -subj "/CN=fit-sync-test-ca" \
			-keyout ca-key.pem -out ca.pem 2>/dev/null
		openssl req -newkey rsa:2048 -nodes -subj "/CN=localhost" \
			-keyout server-key.pem -out server.csr 2>/dev/null
		printf 'subjectAltName=DNS:localhost,IP:127.0.0.1\n' >ext.cnf
		openssl x509 -req -in server.csr -CA ca.pem -CAkey ca-key.pem -CAcreateserial -days 30 \
			-extfile ext.cnf -out server.pem 2>/dev/null
	)
	chmod 644 "$MYSQL_CERTS"/*.pem
}

mysql_start() {
	mysql_certs
	FIT_SYNC_MYSQL=
	port=$MYSQL_PORT
	for s in mysql57 mysql80 mysql84 mariadb1011 mariadb11; do
		case " $MYSQL_SERVERS " in *" $s "*) ;; *)
			port=$((port + 1))
			continue
			;;
		esac
		name=$MYSQL_PREFIX-$s
		# MariaDB's and MySQL 5.7's LOAD_FILE read only files readable by
		# all; UMASK is the mode the servers create their files with.
		if ! docker inspect "$name" >/dev/null 2>&1; then
			case $s in
			mysql57) extra="--server-id=1 --log-bin=mysql-bin --gtid-mode=ON --enforce-gtid-consistency=ON" ;;
			mysql80) extra="--gtid-mode=ON --enforce-gtid-consistency=ON" ;;
			mysql84) extra="--gtid-mode=ON --enforce-gtid-consistency=ON --mysql-native-password=ON" ;;
			*) extra="--server-id=1 --log-bin=mysql-bin" ;;
			esac
			# shellcheck disable=SC2046
			docker run -d --name "$name" -p "127.0.0.1:$port:3306" \
				-e MYSQL_ROOT_PASSWORD="$MYSQL_PASSWORD" -e UMASK=0644 -v "$MYSQL_CERTS:/certs:ro" \
				$(mysql_image $s) --ssl-ca=/certs/ca.pem --ssl-cert=/certs/server.pem \
				--ssl-key=/certs/server-key.pem --max-allowed-packet=64M \
				--binlog-format=ROW --secure-file-priv= $extra >/dev/null
		fi
		FIT_SYNC_MYSQL="$FIT_SYNC_MYSQL${FIT_SYNC_MYSQL:+,}$s=127.0.0.1:$port"
		port=$((port + 1))
	done
	for s in $MYSQL_SERVERS; do
		name=$MYSQL_PREFIX-$s
		i=0
		# The image's first start runs a server without networking, then the
		# real one: wait for TCP inside the container.
		until docker exec "$name" sh -c 'c=mysql; command -v mysql >/dev/null || c=mariadb;
			$c -h127.0.0.1 -uroot -p"$MYSQL_ROOT_PASSWORD" -e "SELECT 1"' >/dev/null 2>&1; do
			i=$((i + 1))
			if [ $i -gt 180 ]; then
				echo "$name did not start:" >&2
				docker logs --tail 50 "$name" >&2
				exit 1
			fi
			sleep 1
		done
	done
	FIT_SYNC_MYSQL_PASSWORD=$MYSQL_PASSWORD
	FIT_SYNC_MYSQL_CA=$MYSQL_CERTS/ca.pem
	# The source's tests restart a server under a pipeline by its name.
	FIT_SYNC_MYSQL_PREFIX=$MYSQL_PREFIX
	export FIT_SYNC_MYSQL FIT_SYNC_MYSQL_PASSWORD FIT_SYNC_MYSQL_CA FIT_SYNC_MYSQL_PREFIX
	[ "$TIDB" = 1 ] && tidb_start
	return 0
}

tidb_start() {
	name=$MYSQL_PREFIX-tidb
	if ! docker inspect "$name" >/dev/null 2>&1; then
		docker run -d --name "$name" -p "127.0.0.1:$TIDB_PORT:4000" "$TIDB_IMAGE" >/dev/null
	fi
	i=0
	# The image has no client: its log says when it listens.
	until docker logs "$name" 2>&1 | grep -q 'server is running MySQL protocol'; do
		i=$((i + 1))
		if [ $i -gt 180 ]; then
			echo "$name did not start:" >&2
			docker logs --tail 50 "$name" >&2
			exit 1
		fi
		sleep 1
	done
	FIT_SYNC_TIDB=127.0.0.1:$TIDB_PORT
	export FIT_SYNC_TIDB
}

mysql_cleanup() {
	if [ "${KEEP:-}" != 1 ]; then
		for s in $MYSQL_SERVERS tidb; do
			docker rm -f "$MYSQL_PREFIX-$s" >/dev/null 2>&1 || true
		done
		rm -rf "$MYSQL_CERTS"
	else
		echo "kept $MYSQL_PREFIX-*; its CA is in MYSQL_CERTS=$MYSQL_CERTS, to pass again" >&2
	fi
}

case $0 in
*mysql.sh)
	set -eu
	cd "$(dirname "$0")/../.."
	trap mysql_cleanup EXIT
	mysql_start
	FIT_SYNC_INTEROP=required go test -count=1 -run 'MySQL' "$@" ./test/sync ./sync/mysql/binlog
	;;
esac
