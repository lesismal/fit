#!/bin/sh
# ab.sh compares two builds of fit on the benchmark host, quickly and without
# waiting for it (rules 16-18, 21-22): fit alone, only the scenarios and
# protocols a change touches, short runs, rounds that alternate A and B.
#
#   bench/ab.sh [options] A B     start a comparison; prints its id and returns
#   bench/ab.sh -report ID        fetch what has finished and summarize it
#   bench/ab.sh -wait ID          wait for it to finish, then summarize
#
# A and B are git refs of this repository (main, HEAD, a commit), "work"
# for the working tree, or bin:PATH for a fit already built for
# linux/amd64, as one fit build made. Options:
#
#   -scenarios LIST   fixed,static,proxy                  (default: all three)
#   -protocols LIST   http/1.1,h2c                        (default: both)
#   -rounds N         rounds of A then B                  (default: 3)
#   -duration D       each measured run                   (default: 10s)
#   -warmup D         unmeasured load before each run     (default: 2s)
#   -extra KONF       appended to bench/configs/fit/fit.konf for both builds,
#                     as 'access_log { output /dev/null }'
#   -args FLAGS       more fitbench flags for every run, as the load test
#                     plan's W1 at 500 in flight: '-connections 500
#                     -h2-connections 20 -h2-streams 25 -server-cpus 0-1,8-9
#                     -backend-cpus 2-4,10-12 -client-cpus 5-7,13-15', and
#                     '-rate 20000' for W6
#
# The host is reached through the ssh master of BENCH_SSH (default below); it
# has no Go, so everything is built here for linux/amd64. Each run waits on
# the host until no other fitbench, go build or test binary runs there.
set -eu

BENCH_SSH=${BENCH_SSH:-"ssh -S $HOME/.ssh/fitbench.sock -p 10013 root@127.0.0.1"}
BENCH_DIR=${BENCH_DIR:-/root/fitbench/ab}
root=$(git rev-parse --show-toplevel)
here=$(cd "$(dirname "$0")" && pwd)
local_dir=${AB_LOCAL_DIR:-${TMPDIR:-/tmp}/fit-ab}

report() {
	id=$1
	mkdir -p "$local_dir/$id"
	$BENCH_SSH "cd $BENCH_DIR/$id && tar cf - status done *.json 2>/dev/null" | tar xf - -C "$local_dir/$id" 2>/dev/null || true
	echo "ab: $id: $(wc -l < "$local_dir/$id/status" 2>/dev/null | tr -d ' ') runs finished$([ -e "$local_dir/$id/done" ] && echo ', done' || echo ', still running')"
	python3 "$here/abreport.py" "$local_dir/$id"
}

case "${1:-}" in
-report)
	report "$2"
	exit 0
	;;
-wait)
	until $BENCH_SSH "test -e $BENCH_DIR/$2/done"; do sleep 30; done
	report "$2"
	exit 0
	;;
esac

scenarios=fixed,static,proxy
protocols=http/1.1,h2c
rounds=3
duration=10s
warmup=2s
extra=
args=
while [ $# -gt 2 ]; do
	case "$1" in
	-scenarios) scenarios=$2 ;;
	-protocols) protocols=$2 ;;
	-rounds) rounds=$2 ;;
	-duration) duration=$2 ;;
	-warmup) warmup=$2 ;;
	-extra) extra=$2 ;;
	-args) args=$2 ;;
	*) echo "ab: unknown option $1" >&2; exit 2 ;;
	esac
	shift 2
done
[ $# -eq 2 ] || { echo "usage: bench/ab.sh [options] A B" >&2; exit 2; }
ref_a=$1
ref_b=$2

$BENCH_SSH -O check 2>/dev/null || { echo "ab: the ssh master to the benchmark host is not running; ask the user to unlock it" >&2; exit 1; }

id=$(date +%Y%m%d-%H%M%S)
stage=$(mktemp -d "${TMPDIR:-/tmp}/fit-ab-stage.XXXXXX")
trap 'rm -rf "$stage"' EXIT
mkdir -p "$stage/$id/a" "$stage/$id/b"

linux() { env GOOS=linux GOARCH=amd64 CGO_ENABLED=0 GOWORK=off "$@"; }

# build REF DIR builds fit at REF into DIR.
build() {
	case "$1" in
	bin:*)
		cp "${1#bin:}" "$2/fit"
		echo "$1" > "$2/ref"
		return
		;;
	esac
	if [ "$1" = work ]; then
		(cd "$root" && linux go build -o "$2/fit" ./cmd/fit)
		echo "work (uncommitted changes of $(git -C "$root" rev-parse --short HEAD))" > "$2/ref"
		return
	fi
	wt="$stage/wt-$(basename "$2")"
	git -C "$root" worktree add -q --detach "$wt" "$1"
	(cd "$wt" && linux go build -o "$2/fit" ./cmd/fit)
	echo "$1 ($(git -C "$root" rev-parse --short "$1"))" > "$2/ref"
	git -C "$root" worktree remove --force "$wt"
}
echo "ab: building A ($ref_a) and B ($ref_b) for linux/amd64"
build "$ref_a" "$stage/$id/a"
build "$ref_b" "$stage/$id/b"
(cd "$root" && linux go build -o "$stage/$id/a/idleconn" ./bench/cmd/idleconn && linux go build -o "$stage/$id/fitbench" ./bench/cmd/fitbench)
cp "$stage/$id/a/idleconn" "$stage/$id/b/idleconn"

# The harness's tree, from the working tree, with -extra appended to fit's
# config.
mkdir -p "$stage/$id/root"
(cd "$root" && COPYFILE_DISABLE=1 tar cf - --exclude bench/.work --exclude bench/results bench) | tar xf - -C "$stage/$id/root"
if [ -n "$extra" ]; then
	printf '\n%s\n' "$extra" >> "$stage/$id/root/bench/configs/fit/fit.konf"
fi

cat > "$stage/$id/run.sh" <<EOF
#!/bin/sh
cd $BENCH_DIR/$id
# Each run waits for a quiet host: no other harness, and no go build or test
# binary, which other sessions run there unpinned and which would take CPUs
# from the run's.
# A pause and a second look keep two comparisons waiting for the same moment
# from both starting at it.
free() { ! pgrep -f '[f]itbench -root' >/dev/null && ! pgrep -x go >/dev/null && ! pgrep -f '[.]test( |\$)' >/dev/null; }
quiet() {
  while :; do
    until free; do sleep 15; done
    sleep \$(shuf -i 2-12 -n 1)
    free && return
  done
}
for r in \$(seq 1 $rounds); do
  for v in a b; do
    quiet
    ./fitbench -root $BENCH_DIR/$id/root -bin $BENCH_DIR/$id/\$v -servers fit -idle "" \\
      -scenarios $scenarios -protocols $protocols -duration $duration -warmup $warmup $args \\
      -out \$v-\$r.json > \$v-\$r.log 2>&1
    echo "\$v-\$r exit \$?" >> status
  done
done
touch done
EOF
chmod +x "$stage/$id/run.sh"
printf 'scenarios=%s protocols=%s rounds=%s duration=%s warmup=%s extra=%s args=%s\n' \
	"$scenarios" "$protocols" "$rounds" "$duration" "$warmup" "$extra" "$args" > "$stage/$id/params"

echo "ab: uploading to the benchmark host"
COPYFILE_DISABLE=1 tar --no-xattrs -czf - -C "$stage" "$id" | $BENCH_SSH "mkdir -p $BENCH_DIR && tar xzf - -C $BENCH_DIR"
$BENCH_SSH "cd $BENCH_DIR/$id && touch status && setsid nohup ./run.sh > run.log 2>&1 < /dev/null &" < /dev/null
mkdir -p "$local_dir/$id"
cp "$stage/$id/a/ref" "$local_dir/$id/a.ref"
cp "$stage/$id/b/ref" "$local_dir/$id/b.ref"
cp "$stage/$id/params" "$local_dir/$id/params"
echo "ab: started $id on the benchmark host; bench/ab.sh -report $id (or -wait $id)"
