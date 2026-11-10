#!/bin/sh
# Starts Apache Doris and StarRocks in Docker for sync's doris interop tests
# (test/sync/olap_test.go, TestDoris*), and prints the environment the tests
# read, as `export` lines:
#
#   eval "$(test/sync/olap.sh up)"     start (or reuse) them, print the env
#   test/sync/olap.sh down             remove them
#
# Doris (DORIS_IMAGE_TAG, 2.1.10) is a frontend and a backend sharing one
# network namespace, the backend registered as 127.0.0.1, so that the
# frontend's Stream Load redirect (to 127.0.0.1:8040) reaches the backend
# from the host too: the ports are published as they are, 8030 (HTTP),
# 9030 (MySQL) and 8040 (the backend's HTTP), and must be free. StarRocks
# (STARROCKS_IMAGE, allin1 3.4.8) registers its backend as 127.0.0.1:8040
# as well, so its loads go to its backend directly, on STARROCKS_BE_PORT
# (28040), its MySQL port on STARROCKS_PORT (29030). Both have root without
# a password. The tests read FIT_SYNC_DORIS_URL, FIT_SYNC_DORIS_ADDR,
# FIT_SYNC_STARROCKS_URL and FIT_SYNC_STARROCKS_ADDR. OLAP picks the
# systems started: "doris starrocks" (both) by default.
#
# The backends ask for vm.max_map_count of 2000000 on Linux; Doris's checks
# are skipped here (SKIP_CHECK_ULIMIT), which is enough for the tests.
set -eu

PREFIX=${PREFIX:-fit-sync-}
OLAP=${OLAP:-doris starrocks}
DORIS_TAG=${DORIS_IMAGE_TAG:-2.1.10}
STARROCKS_IMAGE=${STARROCKS_IMAGE:-starrocks/allin1-ubuntu:3.4.8}
STARROCKS_PORT=${STARROCKS_PORT:-29030}
STARROCKS_BE_PORT=${STARROCKS_BE_PORT:-28040}

if [ "${1:-}" = down ]; then
	docker rm -f "${PREFIX}doris-be" "${PREFIX}doris-fe" "${PREFIX}starrocks" >/dev/null 2>&1 || true
	exit 0
fi

up() {
	name=$1
	shift
	docker inspect "$name" >/dev/null 2>&1 && return 0
	docker run -d --name "$name" "$@" >/dev/null
}

# wait_sql waits until the container's mysql client sees a live backend.
wait_sql() {
	name=$1
	i=0
	until docker exec "$name" mysql -h127.0.0.1 -P9030 -uroot -N -e "SHOW BACKENDS" 2>/dev/null | grep -q true; do
		i=$((i + 1))
		if [ $i -gt 180 ]; then
			echo "olap.sh: $name has no live backend after 6 minutes" >&2
			docker logs --tail 50 "$name" >&2
			exit 1
		fi
		sleep 2
	done
	# A backend reported alive may not take tablets yet ("Create failed
	# replications" on CI): wait until a table of one replica can be made.
	probe="CREATE DATABASE IF NOT EXISTS fit_olap_probe; DROP TABLE IF EXISTS fit_olap_probe.t;
CREATE TABLE fit_olap_probe.t (id BIGINT) DUPLICATE KEY(id) DISTRIBUTED BY HASH(id) BUCKETS 1 PROPERTIES (\"replication_num\" = \"1\");
DROP DATABASE fit_olap_probe"
	until docker exec "$name" mysql -h127.0.0.1 -P9030 -uroot -e "$probe" >/dev/null 2>&1; do
		i=$((i + 1))
		if [ $i -gt 180 ]; then
			echo "olap.sh: $name cannot make a table after 6 minutes" >&2
			docker logs --tail 50 "$name" >&2
			exit 1
		fi
		sleep 2
	done
}

for s in $OLAP; do
	case $s in
	doris)
		up "${PREFIX}doris-fe" -p 8030:8030 -p 9030:9030 -p 8040:8040 \
			-e FE_SERVERS=fe1:127.0.0.1:9010 -e FE_ID=1 "apache/doris:fe-$DORIS_TAG"
		up "${PREFIX}doris-be" --network "container:${PREFIX}doris-fe" -e SKIP_CHECK_ULIMIT=true \
			-e FE_SERVERS=fe1:127.0.0.1:9010 -e BE_ADDR=127.0.0.1:9050 "apache/doris:be-$DORIS_TAG"
		;;
	starrocks)
		up "${PREFIX}starrocks" -p "$STARROCKS_PORT:9030" -p "$STARROCKS_BE_PORT:8040" "$STARROCKS_IMAGE"
		;;
	*)
		echo "olap.sh: unknown system $s: doris or starrocks" >&2
		exit 2
		;;
	esac
done
for s in $OLAP; do
	case $s in
	doris)
		wait_sql "${PREFIX}doris-fe"
		echo "export FIT_SYNC_DORIS_URL=http://127.0.0.1:8030 FIT_SYNC_DORIS_ADDR=127.0.0.1:9030"
		;;
	starrocks)
		wait_sql "${PREFIX}starrocks"
		echo "export FIT_SYNC_STARROCKS_URL=http://127.0.0.1:$STARROCKS_BE_PORT FIT_SYNC_STARROCKS_ADDR=127.0.0.1:$STARROCKS_PORT"
		;;
	esac
done
