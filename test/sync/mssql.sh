#!/bin/sh
# Starts the SQL Server servers of sync's interop tests in Docker, and prints
# the environment the tests read, as `export` lines:
#
#   - SQL Server 2022 and 2019 (Linux images, the Developer edition), each
#     with SQL Server Agent on (CDC's capture and cleanup jobs run in it), a
#     certificate of the script's own CA (TLS is offered, not forced), and
#     the databases fit_sync and fit_sync_cp936 (of the collation
#     Chinese_PRC_CI_AS), each with snapshot isolation allowed, CDC and
#     change tracking on.
#   - SQL Server 2025, set up the same, for TDS 8's strict encryption
#     (MSSQL_STRICT=0 leaves it out): SQL Server 2019 has no TDS 8, and
#     2022's Linux image closes a strict connection after its ServerHello
#     whatever the certificate (error 17821, "A valid TLS certificate is
#     not configured to accept strict connections"; microsoft/mssql-docker
#     issue 878), which 2025's does not. It allows TLS 1.2 and 1.3.
#
# The certificate is made as SQL Server on Linux wants it (Microsoft's
# "Encrypt connections to SQL Server on Linux"): its CN the server's host
# name (each container's is mssql.fit.test), with the names the tests dial
# (localhost, 127.0.0.1) in its subjectAltName, server authentication in
# its extended key usage, signed with SHA-256 by the CA, and installed in a
# volume of its own, owned by the mssql user, the directory 700 and the
# files 600; mssql.conf names it (network.tlscert and tlskey), with
# network.tlsprotocols and network.forceencryption 0.
#
#   eval "$(test/sync/mssql.sh up)"     start (or reuse) them, print the env
#   test/sync/mssql.sh down             remove them, and their volumes
#
# PREFIX names the containers and volumes (fit-sync-); MSSQL_PORT is the
# first of the host ports (29430 for 2022, 29431 for 2019, 29432 for 2025).
# MSSQL_VERSIONS picks the servers the tests run on ("2022 2019"; 2025 may
# be among them). The tests read FIT_SYNC_MSSQL (version=address, comma
# separated), FIT_SYNC_MSSQL_PASSWORD (sa's), FIT_SYNC_MSSQL_CA, and
# FIT_SYNC_MSSQL_STRICT (version=address of the servers taking strict).
# On an arm64 host the amd64 images run emulated, slowly: the first start
# takes a minute or more.
set -eu
cd "$(dirname "$0")/../.."

PREFIX=${PREFIX:-fit-sync-}
MSSQL_PORT=${MSSQL_PORT:-29430}
MSSQL_VERSIONS=${MSSQL_VERSIONS:-2022 2019}
MSSQL_STRICT=${MSSQL_STRICT:-1}
PASSWORD='Fit-sync-1nterop!'
STATE=${TMPDIR:-/tmp}/${PREFIX}mssql
IMAGE=mcr.microsoft.com/mssql/server

if [ "${1:-}" = down ]; then
	for v in 2025 2022 2019; do
		docker rm -f "${PREFIX}mssql$v" >/dev/null 2>&1 || true
		docker volume rm "${PREFIX}mssql$v-certs" >/dev/null 2>&1 || true
	done
	rm -rf "$STATE"
	exit 0
fi

# A CA, and the servers' certificate.
if [ ! -f "$STATE/server.pem" ]; then
	mkdir -p "$STATE"
	(
		cd "$STATE"
		openssl req -x509 -newkey rsa:2048 -sha256 -nodes -days 30 -subj /CN=fit-sync-ca \
			-keyout ca.key -out ca.pem >/dev/null 2>&1
		openssl req -newkey rsa:2048 -nodes -subj /CN=mssql.fit.test \
			-keyout server.key -out server.csr >/dev/null 2>&1
		printf '%s\n' 'subjectAltName=DNS:mssql.fit.test,DNS:localhost,IP:127.0.0.1' \
			'extendedKeyUsage=serverAuth' 'keyUsage=critical,digitalSignature,keyEncipherment' >ext.cnf
		openssl x509 -req -sha256 -in server.csr -CA ca.pem -CAkey ca.key -CAcreateserial -days 30 \
			-extfile ext.cnf -out server.pem >/dev/null 2>&1
		chmod 755 . && chmod 644 ./*
	)
fi

# conf writes mssql.conf for a version: 2025 allows TLS 1.3 too (TDS 8's;
# 2022's TDS 7 logins hang with it on), the others 1.2, as Microsoft's
# guide sets for SQL Server 2022 and before.
conf() {
	protocols=1.2
	if [ "$1" = 2025 ]; then protocols=1.2,1.3; fi
	printf '%s\n' '[network]' 'tlscert = /var/opt/mssql/certs/server.pem' 'tlskey = /var/opt/mssql/certs/server.key' \
		"tlsprotocols = $protocols" 'forceencryption = 0' '' '[sqlagent]' 'enabled = true' >"$STATE/mssql$1.conf"
	chmod 644 "$STATE/mssql$1.conf"
}

SERVERS=$MSSQL_VERSIONS
case " $SERVERS " in
*" 2025 "*) ;;
*) if [ "$MSSQL_STRICT" = 1 ]; then SERVERS="$SERVERS 2025"; fi ;;
esac

ADDRS=
STRICT=
for v in $SERVERS; do
	name=${PREFIX}mssql$v
	case $v in
	2022) p=$MSSQL_PORT ;;
	2019) p=$((MSSQL_PORT + 1)) ;;
	2025) p=$((MSSQL_PORT + 2)) ;;
	*)
		echo "MSSQL_VERSIONS: no version $v" >&2
		exit 1
		;;
	esac
	if ! docker inspect "$name" >/dev/null 2>&1; then
		conf "$v"
		# The certificate and key in a volume, as the mssql user's.
		docker volume create "$name-certs" >/dev/null
		docker run --rm --platform linux/amd64 --user root --entrypoint sh \
			-v "$name-certs:/certs" -v "$STATE:/src:ro" "$IMAGE:$v-latest" \
			-c 'cp /src/server.pem /src/server.key /certs/ && chown -R mssql /certs && chmod 700 /certs && chmod 600 /certs/*'
		docker run -d --name "$name" --hostname mssql.fit.test --platform linux/amd64 -p "127.0.0.1:$p:1433" \
			-e ACCEPT_EULA=Y -e MSSQL_SA_PASSWORD="$PASSWORD" -e MSSQL_AGENT_ENABLED=true -e MSSQL_PID=Developer \
			-v "$name-certs:/var/opt/mssql/certs" -v "$STATE/mssql$v.conf:/var/opt/mssql/mssql.conf:ro" \
			"$IMAGE:$v-latest" >/dev/null
	fi
	case " $MSSQL_VERSIONS " in *" $v "*) ADDRS="$ADDRS,$v=127.0.0.1:$p" ;; esac
	if [ "$v" = 2025 ]; then STRICT="$STRICT,$v=127.0.0.1:$p"; fi
done

sqlcmd() {
	docker exec "$1" sh -c 'c=/opt/mssql-tools18/bin/sqlcmd; [ -x $c ] || c=/opt/mssql-tools/bin/sqlcmd; $c -C -b -S localhost -U sa -P "$0" -Q "$1"' "$PASSWORD" "$2"
}

for v in $SERVERS; do
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
	for db in fit_sync fit_sync_cp936; do
		coll=
		if [ $db = fit_sync_cp936 ]; then coll=" COLLATE Chinese_PRC_CI_AS"; fi
		sqlcmd "$name" "IF DB_ID('$db') IS NULL BEGIN CREATE DATABASE $db$coll; ALTER DATABASE $db SET ALLOW_SNAPSHOT_ISOLATION ON; END" >/dev/null
		sqlcmd "$name" "USE $db; IF (SELECT is_cdc_enabled FROM sys.databases WHERE name = '$db') = 0 EXEC sys.sp_cdc_enable_db" >/dev/null
		sqlcmd "$name" "IF NOT EXISTS (SELECT 1 FROM sys.change_tracking_databases WHERE database_id = DB_ID('$db')) ALTER DATABASE $db SET CHANGE_TRACKING = ON (CHANGE_RETENTION = 2 DAYS, AUTO_CLEANUP = ON)" >/dev/null
	done
done

echo "export FIT_SYNC_MSSQL='${ADDRS#,}' FIT_SYNC_MSSQL_PASSWORD='$PASSWORD' FIT_SYNC_MSSQL_CA='$STATE/ca.pem' FIT_SYNC_MSSQL_STRICT='${STRICT#,}'"
