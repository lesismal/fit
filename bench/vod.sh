#!/bin/bash
# The vod handler against nginx serving the same files cut beforehand, on the
# benchmark host: segments a second and resident memory. It builds fit here
# for linux/amd64, copies it with bench/vod/ to the host and runs
# bench/vod/host.sh there under flock /root/bench.lock, then prints a summary.
# See host.sh for what is run and the settings it takes from the environment
# (FILMS, SECS, DUR, ROUNDS, CONNS, SERVER_CPUS, CLIENT_CPUS).
#
#   bench/vod.sh [out-dir]
#   bench/vod.sh -report out-dir    the summary of a run again
#
# HOST (root@192.168.100.51) and SSH_OPTS (its ssh master) name the host.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/.." && pwd)
if [ "${1:-}" = -report ]; then
	exec python3 "$here/vod/summary.py" "$2"
fi
out=${1:-$root/bench/results/run-vod-$(date +%Y%m%d-%H%M%S)}
HOST=${HOST:-root@192.168.100.51}
SSH_OPTS=${SSH_OPTS:--S $HOME/.ssh/fitbench.sock -o ControlMaster=no}
remote=/root/fit-vod-bench
mkdir -p "$out"

(cd "$root" && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$out/fit" ./cmd/fit)
ssh $SSH_OPTS "$HOST" "mkdir -p $remote"
COPYFILE_DISABLE=1 tar -C "$here/vod" -cf - host.sh segments.lua nginx.conf | ssh $SSH_OPTS "$HOST" "tar -C $remote -xf -"
ssh $SSH_OPTS "$HOST" "cat >$remote/fit && chmod +x $remote/fit" <"$out/fit"
rm -f "$out/fit"
envs="COLD_CACHE=${COLD_CACHE:-67108864} FILMS=${FILMS:-4} SECS=${SECS:-120} DUR=${DUR:-15} ROUNDS=${ROUNDS:-3} CONNS=${CONNS:-64} SERVER_CPUS=${SERVER_CPUS:-0-3} CLIENT_CPUS=${CLIENT_CPUS:-8-15}"
ssh $SSH_OPTS "$HOST" "cd $remote && rm -rf results && $envs flock /root/bench.lock bash host.sh $remote"
ssh $SSH_OPTS "$HOST" "tar -C $remote/results -cf - ." | tar -C "$out" -xf -


echo
python3 "$here/vod/summary.py" "$out"
echo "results: $out"
