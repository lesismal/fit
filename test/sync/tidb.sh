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
# (29690). The tests read FIT_SYNC_TICDC_TIDB (TiDB's address, root without a
# password), FIT_SYNC_TICDC_CONTAINER (the TiCDC container, where the tests
# make changefeeds with its cli) and FIT_SYNC_TICDC_KAFKA (the brokers as
# TiCDC reaches them).
set -eu

PREFIX=${PREFIX:-fit-sync-}
VERSION=${TIDB_VERSION:-v8.5.3}
TIDB_CDC_PORT=${TIDB_CDC_PORT:-29690}
NET=${PREFIX}kafka-net

if [ "${1:-}" = down ]; then
	docker rm -f "${PREFIX}pd" "${PREFIX}tikv" "${PREFIX}cdc-tidb" "${PREFIX}ticdc" >/dev/null 2>&1 || true
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

run "${PREFIX}pd" "pingcap/pd:$VERSION" --name=pd --data-dir=/data/pd \
	--client-urls=http://0.0.0.0:2379 --advertise-client-urls="http://${PREFIX}pd:2379" \
	--peer-urls=http://0.0.0.0:2380 --advertise-peer-urls="http://${PREFIX}pd:2380" \
	--initial-cluster="pd=http://${PREFIX}pd:2380"
run "${PREFIX}tikv" "pingcap/tikv:$VERSION" --addr=0.0.0.0:20160 --advertise-addr="${PREFIX}tikv:20160" \
	--status-addr=0.0.0.0:20180 --data-dir=/data/tikv --pd="${PREFIX}pd:2379"
run "${PREFIX}cdc-tidb" -p "127.0.0.1:$TIDB_CDC_PORT:4000" "pingcap/tidb:$VERSION" \
	--store=tikv --path="${PREFIX}pd:2379" --advertise-address="${PREFIX}cdc-tidb"
run "${PREFIX}ticdc" "pingcap/ticdc:$VERSION" /cdc server --pd="http://${PREFIX}pd:2379" \
	--addr=0.0.0.0:8300 --advertise-addr="${PREFIX}ticdc:8300"

# Wait for TiDB to answer, then for TiCDC's owner.
i=0
until docker logs "${PREFIX}cdc-tidb" 2>&1 | grep -q 'server is running MySQL protocol' &&
	docker exec "${PREFIX}ticdc" /cdc cli capture list --server=http://127.0.0.1:8300 2>/dev/null | grep -q '"is-owner": true'; do
	i=$((i + 1))
	if [ $i -gt 120 ]; then
		echo "TiDB or TiCDC did not start:" >&2
		docker logs --tail 30 "${PREFIX}cdc-tidb" >&2
		docker logs --tail 30 "${PREFIX}ticdc" >&2
		exit 1
	fi
	sleep 2
done

echo "export FIT_SYNC_TICDC_TIDB='127.0.0.1:$TIDB_CDC_PORT' FIT_SYNC_TICDC_CONTAINER='${PREFIX}ticdc' FIT_SYNC_TICDC_KAFKA='${PREFIX}kafka1:19092,${PREFIX}kafka2:19092,${PREFIX}kafka3:19092'"
