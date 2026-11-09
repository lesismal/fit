#!/bin/bash
# Runs on the benchmark host, from bench/bt.sh: fit's bittorrent.client
# against anacrolix/torrent and fib's client alone, downloading one torrent from PEERS local fib
# seeders and seeding it to PEERS local fib leechers, plaintext and with
# MSE (RC4), ROUNDS alternating rounds each; every run is a JSON line in
# results/<scenario>-<plain|mse>-<impl>.jsonl. The data and every download
# live in /dev/shm, so that the disk is not what is measured.
#
# Settings: SIZE (bytes, 1 GiB), PEERS (4), ROUNDS (3), MSES ("0 1"),
# SERVER_CPUS (the client measured, 0-3), CLIENT_CPUS (the fib peers, 8-15).
set -euo pipefail
remote=$1
cd "$remote"
SIZE=${SIZE:-1073741824}
PEERS=${PEERS:-4}
ROUNDS=${ROUNDS:-3}
MSES=${MSES:-"0 1"}
SERVER_CPUS=${SERVER_CPUS:-0-3}
CLIENT_CPUS=${CLIENT_CPUS:-8-15}
data=/dev/shm/fit-bt-bench
trap 'rm -rf "$data"' EXIT
rm -rf "$data" results
mkdir -p results "$data"
./btbench gen -dir "$data" -size "$SIZE"
{
	echo "host: $(hostname), $(nproc) CPUs, $(uname -r)"
	echo "size=$SIZE peers=$PEERS rounds=$ROUNDS server_cpus=$SERVER_CPUS client_cpus=$CLIENT_CPUS"
	echo "fit: $(./fit version 2>/dev/null | head -1)"
	date -u +%FT%TZ
} >results/settings.txt
for mse in $MSES; do
	tag=plain
	flag=
	if [ "$mse" = 1 ]; then
		tag=mse
		flag=-mse
	fi
	for scenario in download seed; do
		for round in $(seq 1 "$ROUNDS"); do
			order="fit anacrolix fib"
			if [ $((round % 2)) = 0 ]; then
				order="fib anacrolix fit"
			fi
			for impl in $order; do
				echo "== $scenario $tag $impl round $round" >&2
				taskset -c "$CLIENT_CPUS" ./btbench run -scenario "$scenario" -impl "$impl" -dir "$data" \
					-work "$data/work" -fit ./fit -peers "$PEERS" $flag -server-cpus "$SERVER_CPUS" \
					>>"results/$scenario-$tag-$impl.jsonl"
			done
		done
	done
done
