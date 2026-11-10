#!/bin/sh
# Starts the Pulsar servers of sync's interop tests in Docker, and prints the
# environment the tests read, as `export` lines: Pulsar 3.0 and 4.0, each a
# standalone (a broker, a bookie and its metadata in one container) with
# broker deduplication on, advertised on 127.0.0.1 at the host's own port,
# so that a lookup's answer is a host port the tests reach. With
# PULSAR_AUTH=1 (the default) a third, pulsar-auth (Pulsar 4.0), takes only
# TLS clients authenticated by a token: a CA, a server certificate, an
# HS256 secret key and a token of the role fit, all made by the script.
#
#   eval "$(test/sync/pulsar.sh up)"     start (or reuse) them, print the env
#   test/sync/pulsar.sh down             remove them
#
# PREFIX names the containers (fit-sync-); PULSAR_PORT is the first of the
# host ports (29540: 29540 and 29541 the brokers, 29550 and 29551 their web
# services; 29542 and 29552 pulsar-auth's TLS broker and web service);
# PULSAR_VERSIONS picks the servers ("3 4"). The tests read
# FIT_SYNC_PULSAR_3_URL, FIT_SYNC_PULSAR_3_ADMIN, FIT_SYNC_PULSAR_4_URL and
# FIT_SYNC_PULSAR_4_ADMIN, and pulsar-auth's FIT_SYNC_PULSAR_AUTH_URL,
# FIT_SYNC_PULSAR_AUTH_ADMIN, FIT_SYNC_PULSAR_AUTH_CA and
# FIT_SYNC_PULSAR_AUTH_TOKEN.
set -eu
cd "$(dirname "$0")/../.."

PREFIX=${PREFIX:-fit-sync-}
PULSAR3_IMAGE=${PULSAR3_IMAGE:-apachepulsar/pulsar:3.0.11}
PULSAR4_IMAGE=${PULSAR4_IMAGE:-apachepulsar/pulsar:4.0.5}
PULSAR_PORT=${PULSAR_PORT:-29540}
PULSAR_VERSIONS=${PULSAR_VERSIONS:-3 4}
PULSAR_AUTH=${PULSAR_AUTH:-1}
STATE=${TMPDIR:-/tmp}/${PREFIX}pulsar

if [ "${1:-}" = down ]; then
	docker rm -fv "${PREFIX}pulsar3" "${PREFIX}pulsar4" "${PREFIX}pulsar-auth" >/dev/null 2>&1 || true
	rm -rf "$STATE"
	exit 0
fi

b64url() { openssl base64 -A | tr '+/' '-_' | tr -d '='; }

# start name image broker-port web-port
start() {
	if [ -z "$(docker ps -q -f "name=^$1\$")" ]; then
		docker rm -fv "$1" >/dev/null 2>&1 || true
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

AUTH_TLS=$((PULSAR_PORT + 2)) AUTH_WEB=$((PULSAR_PORT + 12)) TOKEN=""
if [ "$PULSAR_AUTH" = 1 ]; then
	if [ ! -f "$STATE/token" ]; then
		mkdir -p "$STATE"
		(
			cd "$STATE"
			openssl req -x509 -newkey rsa:2048 -nodes -days 30 -subj /CN=fit-sync-ca \
				-keyout ca.key -out ca.pem >/dev/null 2>&1
			openssl req -newkey rsa:2048 -nodes -subj /CN=localhost \
				-keyout server.rsa -out server.csr >/dev/null 2>&1
			# The broker (Netty) takes a PKCS#8 key.
			openssl pkcs8 -topk8 -nocrypt -in server.rsa -out server.key
			printf 'subjectAltName=DNS:localhost,IP:127.0.0.1\n' >ext.cnf
			openssl x509 -req -in server.csr -CA ca.pem -CAkey ca.key -CAcreateserial -days 30 \
				-extfile ext.cnf -out server.pem >/dev/null 2>&1
			# An HS256 key of 64 printable bytes, and a token of the role fit
			# signed with it.
			openssl rand -hex 32 | tr -d '\n' >secret.key
			jwt="$(printf '{"alg":"HS256"}' | b64url).$(printf '{"sub":"fit"}' | b64url)"
			printf '%s.%s' "$jwt" "$(printf '%s' "$jwt" | openssl dgst -sha256 -hmac "$(cat secret.key)" -binary | b64url)" >token
			chmod 755 . && chmod 644 ./*
		)
	fi
	TOKEN=$(cat "$STATE/token")
	name=${PREFIX}pulsar-auth
	if [ -z "$(docker ps -q -f "name=^$name\$")" ]; then
		docker rm -fv "$name" >/dev/null 2>&1 || true
		docker run -d --name "$name" -p "$AUTH_TLS:$AUTH_TLS" -p "$AUTH_WEB:$AUTH_WEB" \
			-v "$STATE:/pulsar/fit:ro" \
			-e PULSAR_PREFIX_brokerDeduplicationEnabled=true \
			-e PULSAR_PREFIX_advertisedAddress=127.0.0.1 \
			-e PULSAR_PREFIX_brokerServicePortTls="$AUTH_TLS" \
			-e PULSAR_PREFIX_webServicePort="$AUTH_WEB" \
			-e PULSAR_PREFIX_tlsCertificateFilePath=/pulsar/fit/server.pem \
			-e PULSAR_PREFIX_tlsKeyFilePath=/pulsar/fit/server.key \
			-e PULSAR_PREFIX_tlsTrustCertsFilePath=/pulsar/fit/ca.pem \
			-e PULSAR_PREFIX_authenticationEnabled=true \
			-e PULSAR_PREFIX_authenticationProviders=org.apache.pulsar.broker.authentication.AuthenticationProviderToken \
			-e PULSAR_PREFIX_tokenSecretKey=file:///pulsar/fit/secret.key \
			-e PULSAR_PREFIX_superUserRoles=fit \
			-e PULSAR_PREFIX_brokerClientAuthenticationPlugin=org.apache.pulsar.client.impl.auth.AuthenticationToken \
			-e PULSAR_PREFIX_brokerClientAuthenticationParameters="token:$TOKEN" \
			-e PULSAR_MEM="-Xms512m -Xmx512m -XX:MaxDirectMemorySize=512m" \
			"$PULSAR4_IMAGE" sh -c 'bin/apply-config-from-env.py conf/standalone.conf && exec bin/pulsar standalone -nss -nfw' >/dev/null
	fi
fi

# ready web [token]: once the admin API lists public's namespaces (default
# among them).
ready() {
	i=0
	until curl -fsS ${2:+-H "Authorization: Bearer $2"} "http://127.0.0.1:$1/admin/v2/namespaces/public" 2>/dev/null | grep -q default; do
		i=$((i + 1))
		if [ "$i" -gt 180 ]; then
			echo "pulsar on $1 did not start" >&2
			exit 1
		fi
		sleep 1
	done
}
for v in $PULSAR_VERSIONS; do
	ready $((PULSAR_PORT + 7 + v))
done
if [ "$PULSAR_AUTH" = 1 ]; then
	ready "$AUTH_WEB" "$TOKEN"
fi

for v in $PULSAR_VERSIONS; do
	echo "export FIT_SYNC_PULSAR_${v}_URL=pulsar://127.0.0.1:$((PULSAR_PORT + v - 3))"
	echo "export FIT_SYNC_PULSAR_${v}_ADMIN=http://127.0.0.1:$((PULSAR_PORT + 7 + v))"
done
if [ "$PULSAR_AUTH" = 1 ]; then
	echo "export FIT_SYNC_PULSAR_AUTH_URL=pulsar+ssl://127.0.0.1:$AUTH_TLS FIT_SYNC_PULSAR_AUTH_ADMIN=http://127.0.0.1:$AUTH_WEB"
	echo "export FIT_SYNC_PULSAR_AUTH_CA='$STATE/ca.pem' FIT_SYNC_PULSAR_AUTH_TOKEN='$TOKEN'"
fi
