#!/bin/sh
# Runs fit's BitTorrent interop tests in a container that has
# libtorrent-rasterbar (Debian's python3-libtorrent, 2.0) and cgo, for
# anacrolix's uTP (go-libutp): every case, anacrolix's and libtorrent's,
# against the fit built from this checkout. It needs docker; the module and
# build caches are kept in volumes. Functional tests, not load: they may run
# on any machine with docker.
#
#   test/btinterop/run.sh                       # every case
#   test/btinterop/run.sh -run 'TestClientLibtorrent/.*v2'
set -eu
cd "$(dirname "$0")/../.."
IMAGE=fit-btinterop

docker build -q -t "$IMAGE" - <<'EOF' >/dev/null
FROM golang:1.27.3
RUN apt-get update && apt-get install -y --no-install-recommends python3-libtorrent g++ && rm -rf /var/lib/apt/lists/*
EOF

ARGS="-run ."
if [ $# -gt 0 ]; then
	ARGS="$*"
fi
# -buildvcs=false: a git worktree's .git points outside the mount.
docker run --rm --cpus 4 -v "$PWD":/fit -v fit-gomod:/go/pkg/mod -v fit-gocache:/root/.cache/go-build \
	-w /fit/test/btinterop -e FIT_LIBTORRENT=1 -e CGO_ENABLED=1 -e GOFLAGS=-buildvcs=false "$IMAGE" \
	sh -c "python3 -c 'import libtorrent; print(\"libtorrent\", libtorrent.__version__)' && go test -count=1 -timeout 1200s $ARGS -v ."
