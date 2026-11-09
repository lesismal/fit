#!/bin/sh
# Starts the SQL Server servers of sync's interop tests in Docker, and prints
# the environment the tests read, as `export` lines:
#
#   - SQL Server 2022 and 2019 (Linux images, the Developer edition), each
#     with SQL Server Agent on (CDC's capture and cleanup jobs run in it), a
#     certificate of the script's own CA for localhost (TLS is offered, not
#     forced), and a database fit_sync with snapshot isolation allowed and
#     CDC enabled on it.
#
#   eval "$(test/sync/mssql.sh up)"     start (or reuse) them, print the env
#   test/sync/mssql.sh down             remove them
#
# PREFIX names the containers (fit-sync-); MSSQL_PORT is the first of the
# host ports (29430 for 2022, 29431 for 2019). MSSQL_VERSIONS picks the
# servers ("2022 2019"). The tests read FIT_SYNC_MSSQL (version=address,
# comma separated), FIT_SYNC_MSSQL_PASSWORD (sa's) and FIT_SYNC_MSSQL_CA.
# On an arm64 host the amd64 images run emulated, slowly: the first start
# takes a minute or more.
set -eu
cd "$(dirname "$0")/../.."

PREFIX=${PREFIX:-fit-sync-}
MSSQL_PORT=${MSSQL_PORT:-29430}
MSSQL_VERSIONS=${MSSQL_VERSIONS:-2022 2019}
PASSWORD='Fit-sync-1nterop!'
STATE=${TMPDIR:-/tmp}/${PREFIX}mssql

if [ "${1:-}" = down ]; then
	for v in 2022 2019; do
		docker rm -f "${PREFIX}mssql$v" >/dev/null 2>&1 || true
	done
	rm -rf "$STATE"
	exit 0
fi

# A CA and the servers' certificate for localhost, and mssql.conf naming it.
if [ ! -f "$STATE/mssql.conf" ]; then
	mkdir -p "$STATE"
	(
		cd "$STATE"
		openssl req -x509 -newkey rsa:2048 -nodes -days 30 -subj /CN=fit-sync-ca \
			-keyout ca.key -out ca.pem >/dev/null 2>&1
		openssl req -newkey rsa:2048 -nodes -subj /CN=localhost \
			-keyout server.key -out server.csr >/dev/null 2>&1
		printf 'subjectAltName=DNS:localhost,IP:127.0.0.1\nextendedKeyUsage=serverAuth\n' >ext.cnf
		openssl x509 -req -in server.csr -CA ca.pem -CAkey ca.key -CAcreateserial -days 30 \
			-extfile ext.cnf -out server.pem >/dev/null 2>&1
		cat >mssql.conf <<EOF
[network]
tlscert = /fit/server.pem
tlskey = /fit/server.key
tlsprotocols = 1.2
forceencryption = 0

[sqlagent]
enabled = true
EOF
		chmod 755 . && chmod 644 ./*
	)
fi

ADDRS=
for v in $MSSQL_VERSIONS; do
	name=${PREFIX}mssql$v
	case $v in
	2022) p=$MSSQL_PORT ;;
	2019) p=$((MSSQL_PORT + 1)) ;;
	*)
		echo "MSSQL_VERSIONS: no version $v" >&2
		exit 1
		;;
	esac
	if ! docker inspect "$name" >/dev/null 2>&1; then
		docker run -d --name "$name" --platform linux/amd64 -p "127.0.0.1:$p:1433" \
			-e ACCEPT_EULA=Y -e MSSQL_SA_PASSWORD="$PASSWORD" -e MSSQL_AGENT_ENABLED=true -e MSSQL_PID=Developer \
			-v "$STATE/server.pem:/fit/server.pem:ro" -v "$STATE/server.key:/fit/server.key:ro" \
			-v "$STATE/mssql.conf:/var/opt/mssql/mssql.conf:ro" \
			"mcr.microsoft.com/mssql/server:$v-latest" >/dev/null
	fi
	ADDRS="$ADDRS,$v=127.0.0.1:$p"
done

sqlcmd() {
	docker exec "$1" sh -c 'c=/opt/mssql-tools18/bin/sqlcmd; [ -x $c ] || c=/opt/mssql-tools/bin/sqlcmd; $c -C -b -S localhost -U sa -P "$0" -Q "$1"' "$PASSWORD" "$2"
}

for v in $MSSQL_VERSIONS; do
	name=${PREFIX}mssql$v
	i=0
	until sqlcmd "$name" "SELECT 1" >/dev/null 2>&1; do
		i=$((i + 1))
		if [ $i -gt 180 ]; then
			echo "SQL Server $v did not start:" >&2
			docker logs --tail 50 "$name" >&2
			exit 1
		fi
		sleep 2
	done
	sqlcmd "$name" "IF DB_ID('fit_sync') IS NULL BEGIN CREATE DATABASE fit_sync; ALTER DATABASE fit_sync SET ALLOW_SNAPSHOT_ISOLATION ON; END" >/dev/null
	sqlcmd "$name" "USE fit_sync; IF (SELECT is_cdc_enabled FROM sys.databases WHERE name = 'fit_sync') = 0 EXEC sys.sp_cdc_enable_db" >/dev/null
	sqlcmd "$name" "IF NOT EXISTS (SELECT 1 FROM sys.change_tracking_databases WHERE database_id = DB_ID('fit_sync')) ALTER DATABASE fit_sync SET CHANGE_TRACKING = ON (CHANGE_RETENTION = 2 DAYS, AUTO_CLEANUP = ON)" >/dev/null
done

echo "export FIT_SYNC_MSSQL='${ADDRS#,}' FIT_SYNC_MSSQL_PASSWORD='$PASSWORD' FIT_SYNC_MSSQL_CA='$STATE/ca.pem'"
