#!/bin/bash
# fit's bittorrent.client against anacrolix/torrent on the benchmark host:
# throughput, CPU and peak memory downloading one torrent from local
# seeders, and seeding it to local leechers (test/btinterop/btbench). It
# builds fit and btbench here for linux/amd64, copies them with bench/bt/
# to the host and runs bench/bt/host.sh there under flock /root/bench.lock,
# then prints a summary. See host.sh for what is run and the settings it
# takes from the environment (SIZE, PEERS, ROUNDS, MSES, SERVER_CPUS,
# CLIENT_CPUS). Never run locally (docs/rules.md, rules 16-18).
#
#   bench/bt.sh [out-dir]
#   bench/bt.sh -report out-dir    the summary of a run again
#
# HOST (root@192.168.100.51) and SSH_OPTS (its ssh master) name the host.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/.." && pwd)
if [ "${1:-}" = -report ]; then
	exec python3 "$here/bt/summary.py" "$2"
fi
out=${1:-$root/bench/results/run-bt-$(date +%Y%m%d-%H%M%S)}
HOST=${HOST:-root@192.168.100.51}
SSH_OPTS=${SSH_OPTS:--S $HOME/.ssh/fitbench.sock -o ControlMaster=no}
remote=/root/fit-bt-bench
mkdir -p "$out"

(cd "$root" && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$out/fit" ./cmd/fit)
(cd "$root/test/btinterop" && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$out/btbench" ./btbench)
ssh $SSH_OPTS "$HOST" "mkdir -p $remote"
COPYFILE_DISABLE=1 tar -C "$here/bt" -cf - host.sh | ssh $SSH_OPTS "$HOST" "tar -C $remote -xf -"
for bin in fit btbench; do
	ssh $SSH_OPTS "$HOST" "cat >$remote/$bin && chmod +x $remote/$bin" <"$out/$bin"
	rm -f "$out/$bin"
done
envs="SIZE=${SIZE:-1073741824} PEERS=${PEERS:-4} ROUNDS=${ROUNDS:-3} MSES='${MSES:-0 1}' SERVER_CPUS=${SERVER_CPUS:-0-3} CLIENT_CPUS=${CLIENT_CPUS:-8-15}"
ssh $SSH_OPTS "$HOST" "cd $remote && rm -rf results && $envs flock /root/bench.lock bash host.sh $remote"
ssh $SSH_OPTS "$HOST" "tar -C $remote/results -cf - ." | tar -C "$out" -xf -

echo
python3 "$here/bt/summary.py" "$out" | tee "$out/summary.md"
echo "results: $out"
