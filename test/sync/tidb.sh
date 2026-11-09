#!/bin/sh
# Starts a TiDB cluster with TiCDC in Docker for sync's TiDB interop tests,
# and prints the environment the tests read, as `export` lines: PD, TiKV,
# TiDB and a TiCDC server, on the network of test/sync/kafka.sh's Kafka
# (run it first, with the same PREFIX), so that changefeeds write to that
# Kafka through its brokers' internal listener.
#
#   eval "$(test/sync/tidb.sh up)"     start (or reuse) them, print the env
#   test/sync/tidb.sh down             remove them
#
# PREFIX names the containers (fit-sync-), and the Kafka network
# (${PREFIX}kafka-net); TIDB_CDC_PORT is TiDB's MySQL port on the host
# (29690). TiKV is held to TIKV_MEMORY_MB (3072), its block cache to an eighth of
# it, so that it lives beside run.sh's other servers on a CI runner. The
# script returns only once a DDL and a write go through TiDB (with the
# client of MYSQL_CLIENT_IMAGE, mysql:8.4, which mysql.sh pulls too) and a
# changefeed TiCDC is given turns normal. The tests read FIT_SYNC_TICDC_TIDB
# (TiDB's address, root without a password), FIT_SYNC_TICDC_CONTAINER (the
# TiCDC container, where the tests make changefeeds with its cli) and
# FIT_SYNC_TICDC_KAFKA (the brokers as TiCDC reaches them).
set -eu

PREFIX=${PREFIX:-fit-sync-}
VERSION=${TIDB_VERSION:-v8.5.3}
TIDB_CDC_PORT=${TIDB_CDC_PORT:-29690}
TIKV_MEMORY_MB=${TIKV_MEMORY_MB:-3072}
MYSQL_CLIENT_IMAGE=${MYSQL_CLIENT_IMAGE:-mysql:8.4}
NET=${PREFIX}kafka-net
STATE=${TMPDIR:-/tmp}/${PREFIX}tidb

if [ "${1:-}" = down ]; then
	docker rm -f "${PREFIX}pd" "${PREFIX}tikv" "${PREFIX}cdc-tidb" "${PREFIX}ticdc" >/dev/null 2>&1 || true
	rm -rf "$STATE"
	exit 0
fi

if ! docker network inspect "$NET" >/dev/null 2>&1; then
	echo "no network $NET: run test/sync/kafka.sh up with PREFIX=$PREFIX first" >&2
	exit 1
fi

run() {
	name=$1
	shift
	docker inspect "$name" >/dev/null 2>&1 && return 0
	docker run -d --name "$name" --hostname "$name" --network "$NET" "$@" >/dev/null
}

# TiKV sizes its caches by the memory it sees: the container's limit and a
# block cache of an eighth of it (its page cache counts too) keep it from
# starving the other servers.
mkdir -p "$STATE"
chmod 755 "$STATE"
cat >"$STATE/tikv.toml" <<EOF
memory-usage-limit = "$((TIKV_MEMORY_MB / 2))MiB"

[storage.block-cache]
capacity = "$((TIKV_MEMORY_MB / 8))MiB"
EOF
chmod 644 "$STATE/tikv.toml"

run "${PREFIX}pd" "pingcap/pd:$VERSION" --name=pd --data-dir=/data/pd \
	--client-urls=http://0.0.0.0:2379 --advertise-client-urls="http://${PREFIX}pd:2379" \
	--peer-urls=http://0.0.0.0:2380 --advertise-peer-urls="http://${PREFIX}pd:2380" \
	--initial-cluster="pd=http://${PREFIX}pd:2380"
run "${PREFIX}tikv" --memory "${TIKV_MEMORY_MB}m" -v "$STATE:/etc/tikv-fit:ro" "pingcap/tikv:$VERSION" \
	--config=/etc/tikv-fit/tikv.toml --addr=0.0.0.0:20160 --advertise-addr="${PREFIX}tikv:20160" \
	--status-addr=0.0.0.0:20180 --data-dir=/data/tikv --pd="${PREFIX}pd:2379"
run "${PREFIX}cdc-tidb" -p "127.0.0.1:$TIDB_CDC_PORT:4000" "pingcap/tidb:$VERSION" \
	--store=tikv --path="${PREFIX}pd:2379" --advertise-address="${PREFIX}cdc-tidb"
run "${PREFIX}ticdc" "pingcap/ticdc:$VERSION" /cdc server --pd="http://${PREFIX}pd:2379" \
	--addr=0.0.0.0:8300 --advertise-addr="${PREFIX}ticdc:8300"

fail() {
	echo "$1" >&2
	for c in pd tikv cdc-tidb ticdc; do
		echo "--- ${PREFIX}$c:" >&2
		docker logs --tail 20 "${PREFIX}$c" >&2 2>&1 || true
	done
	exit 1
}

# TiDB's MySQL port, then TiCDC's owner.
i=0
until docker logs "${PREFIX}cdc-tidb" 2>&1 | grep -q 'server is running MySQL protocol' &&
	docker exec "${PREFIX}ticdc" /cdc cli capture list --server=http://127.0.0.1:8300 2>/dev/null | grep -q '"is-owner": true'; do
	i=$((i + 1))
	[ $i -le 150 ] || fail "TiDB or TiCDC did not start"
	sleep 2
done

# A DDL and a write through TiDB: under load TiDB answers before it can load
# a schema change ("Information schema is out of date", 8027).
sql() {
	docker run --rm -i --network "$NET" "$MYSQL_CLIENT_IMAGE" \
		mysql -h "${PREFIX}cdc-tidb" -P 4000 -u root --connect-timeout=10 "$@"
}
probe='DROP DATABASE IF EXISTS fit_cdc_ready;
CREATE DATABASE fit_cdc_ready;
CREATE TABLE fit_cdc_ready.t (id INT PRIMARY KEY);
INSERT INTO fit_cdc_ready.t VALUES (1);
DROP DATABASE fit_cdc_ready;'
i=0
until echo "$probe" | sql >/dev/null 2>&1; do
	i=$((i + 1))
	if [ $i -gt 60 ]; then
		echo "$probe" | sql >&2 || true
		fail "TiDB takes no DDL"
	fi
	sleep 5
done

# A changefeed to nowhere, until TiCDC says it is normal.
cdc() {
	docker exec "${PREFIX}ticdc" /cdc cli changefeed "$@" --server=http://127.0.0.1:8300
}
cdc remove --changefeed-id=fit-ready >/dev/null 2>&1 || true
i=0
until cdc create --changefeed-id=fit-ready --sink-uri=blackhole:// >/dev/null 2>&1; do
	i=$((i + 1))
	if [ $i -gt 30 ]; then
		cdc create --changefeed-id=fit-ready --sink-uri=blackhole:// >&2 || true
		fail "TiCDC makes no changefeed"
	fi
	sleep 5
done
i=0
until cdc query -s --changefeed-id=fit-ready 2>/dev/null | grep -q '"state": "normal"'; do
	i=$((i + 1))
	if [ $i -gt 90 ]; then
		cdc query -s --changefeed-id=fit-ready >&2 || true
		fail "TiCDC's changefeed is not normal"
	fi
	sleep 2
done
cdc remove --changefeed-id=fit-ready >/dev/null 2>&1 || true

echo "export FIT_SYNC_TICDC_TIDB='127.0.0.1:$TIDB_CDC_PORT' FIT_SYNC_TICDC_CONTAINER='${PREFIX}ticdc' FIT_SYNC_TICDC_KAFKA='${PREFIX}kafka1:19092,${PREFIX}kafka2:19092,${PREFIX}kafka3:19092'"
