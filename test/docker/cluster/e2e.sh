#!/bin/sh
# The docker test of cluster management (I141): a manager and three members,
# each a fit in a container of its own, driven through the manager's console
# by e2e/cluster_test.go, which starts and stops the containers itself.
#
#   test/docker/cluster/e2e.sh            build fit for Linux and run the test here
#   test/docker/cluster/e2e.sh build DIR  only build fit and the test binary into DIR,
#                                         for a Linux host with Docker and no Go
#
# On such a host: FIT_BIN=DIR/fit DIR/cluster-e2e.test -test.v. Environment:
# FIT_IMAGE (the containers' image, default debian:trixie), FIT_PORT (the host
# port of the manager's console, default 29441), GOARCH (the containers'
# architecture, default this machine's). This is an interop test, not a load
# test: it makes a few calls a second.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
arch=${GOARCH:-$(go env GOARCH)}

build() {
	(cd "$root" && GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go build -o "$1/fit" ./cmd/fit)
	(cd "$root" && GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go test -c -tags e2e -o "$1/cluster-e2e.test" ./test/docker/cluster/e2e)
}

case "${1:-run}" in
build)
	mkdir -p "$2"
	build "$2"
	;;
run)
	work=$(mktemp -d)
	trap 'rm -rf "$work"' EXIT
	build "$work"
	FIT_BIN="$work/fit" "$work/cluster-e2e.test" -test.v -test.timeout 15m
	;;
*)
	sed -n '2,14p' "$0"
	exit 2
	;;
esac
