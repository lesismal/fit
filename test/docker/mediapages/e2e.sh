#!/bin/sh
# The browser test of fit's media pages (handler media_pages): fit and two
# headless Chromiums, one with fake devices and one with none, in a Linux
# container; pages-test.mjs drives them over DevTools from the host.
#
#   test/docker/mediapages/e2e.sh [out-dir]
#
# It needs Docker and Node 22 or later. The screenshots, fit's log and the
# browsers' logs go to out-dir (a temporary directory when left out).
# IMAGE names an image of Chromium and Python to use instead of building
# this directory's Dockerfile; BASE_IMAGE the Dockerfile's base.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
out=${1:-$(mktemp -d)}
mkdir -p "$out"
work=$(mktemp -d)
name=fit-mediapages-e2e
cleanup() { docker rm -f "$name" >/dev/null 2>&1 || true; rm -rf "$work"; }
trap cleanup EXIT

image=${IMAGE:-fit-mediapages-e2e}
if [ -z "${IMAGE:-}" ]; then
	docker build -q -t "$image" --build-arg "BASE_IMAGE=${BASE_IMAGE:-debian:stable-slim}" "$here" >/dev/null
fi
arch=$(docker image inspect "$image" --format '{{.Architecture}}')
(cd "$root" && GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go build -o "$work/fit" ./cmd/fit)
cp "$here/run.sh" "$here/fwd.py" "$here/fit.json" "$work/"
mkdir -p "$work/out"

docker rm -f "$name" >/dev/null 2>&1 || true
docker run -d --name "$name" -p 127.0.0.1:29301:9301 -p 127.0.0.1:29302:9302 \
	-v "$work:/w" --entrypoint /w/run.sh "$image" >/dev/null
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
	curl -sf http://127.0.0.1:29301/json/version >/dev/null 2>&1 &&
		curl -sf http://127.0.0.1:29302/json/version >/dev/null 2>&1 && break
	sleep 0.5
done
status=0
node "$here/pages-test.mjs" "$out" || status=$?
cp "$work"/out/*.log "$out/" 2>/dev/null || true
echo "screenshots and logs: $out"
exit $status
