#!/bin/sh
# microbench.sh runs go test micro-benchmarks of two builds on the benchmark
# host and compares them with benchstat here (rules 16-18, 21): seconds where
# a load test takes minutes, and a verdict on the noise instead of guessing at
# rounds.
#
#   bench/microbench.sh [options] -pkg PKG -bench REGEX A B
#
# A and B are git refs of the repository (main, HEAD, a commit), or "work" for
# its working tree. Options:
#
#   -C DIR          the repository, as ../fib            (default: this one)
#   -pkg PKG        the package, as ./http               (required)
#   -bench REGEX    the benchmarks                       (required)
#   -count N        runs of each, alternating A and B    (default: 10)
#   -benchtime D    as go test's                         (default: 1s)
#
# The host has no Go: the test binaries are built here for linux/amd64, run
# there one after the other, and their output fetched; benchstat runs here.
set -eu

BENCH_SSH=${BENCH_SSH:-"ssh -S $HOME/.ssh/fitbench.sock -p 10013 root@127.0.0.1"}
BENCH_DIR=${BENCH_DIR:-/root/fitbench/micro}

repo=.
pkg=
pattern=
count=10
benchtime=1s
while [ $# -gt 2 ]; do
	case "$1" in
	-C) repo=$2 ;;
	-pkg) pkg=$2 ;;
	-bench) pattern=$2 ;;
	-count) count=$2 ;;
	-benchtime) benchtime=$2 ;;
	*) echo "microbench: unknown option $1" >&2; exit 2 ;;
	esac
	shift 2
done
[ $# -eq 2 ] && [ -n "$pkg" ] && [ -n "$pattern" ] || {
	echo "usage: bench/microbench.sh [-C DIR] -pkg PKG -bench REGEX [-count N] [-benchtime D] A B" >&2
	exit 2
}
root=$(git -C "$repo" rev-parse --show-toplevel)

$BENCH_SSH -O check 2>/dev/null || { echo "microbench: the ssh master to the benchmark host is not running; ask the user to unlock it" >&2; exit 1; }

id=$(date +%Y%m%d-%H%M%S)
stage=$(mktemp -d "${TMPDIR:-/tmp}/fit-micro.XXXXXX")
trap 'rm -rf "$stage"' EXIT
mkdir -p "$stage/$id"

linux() { env GOOS=linux GOARCH=amd64 CGO_ENABLED=0 GOWORK=off "$@"; }

# build REF NAME builds the test binary of pkg at REF.
build() {
	if [ "$1" = work ]; then
		(cd "$root" && linux go test -c -o "$stage/$id/$2.test" "$pkg")
		return
	fi
	wt="$stage/wt-$2"
	git -C "$root" worktree add -q --detach "$wt" "$1"
	(cd "$wt" && linux go test -c -o "$stage/$id/$2.test" "$pkg")
	git -C "$root" worktree remove --force "$wt"
}
echo "microbench: building $pkg at A ($1) and B ($2)"
build "$1" a
build "$2" b

cat > "$stage/$id/run.sh" <<EOF
#!/bin/sh
cd $BENCH_DIR/$id
for i in \$(seq 1 $count); do
  for v in a b; do
    ./\$v.test -test.run '^\$' -test.bench '$pattern' -test.benchmem -test.benchtime $benchtime -test.count 1 >> \$v.txt 2>&1
  done
done
EOF
chmod +x "$stage/$id/run.sh"

echo "microbench: running $count rounds on the benchmark host"
COPYFILE_DISABLE=1 tar --no-xattrs -czf - -C "$stage" "$id" | $BENCH_SSH "mkdir -p $BENCH_DIR && tar xzf - -C $BENCH_DIR && $BENCH_DIR/$id/run.sh"
$BENCH_SSH "cd $BENCH_DIR/$id && tar cf - a.txt b.txt" | tar xf - -C "$stage/$id"

out=${MICRO_LOCAL_DIR:-${TMPDIR:-/tmp}/fit-micro}/$id
mkdir -p "$out"
cp "$stage/$id/a.txt" "$stage/$id/b.txt" "$out/"
echo "microbench: A = $1, B = $2; raw output in $out"
if command -v benchstat >/dev/null 2>&1; then
	(cd "$out" && benchstat a.txt b.txt)
else
	(cd "$out" && GOWORK=off GOFLAGS= go run golang.org/x/perf/cmd/benchstat@latest a.txt b.txt)
fi
