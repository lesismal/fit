#!/bin/bash
# Runs the streaming role's ffmpeg interop checks (streaming/interop_test.go)
# in a Linux container: golang:1.27, Debian trixie, with trixie's ffmpeg
# (libx264, libsrt) installed at start; an image that has Go and ffmpeg
# already, as IMAGE=fibmedia:trixie, skips the install. They are ordinary
# tests, not load tests: nothing is measured.
#
# Usage: scripts/streaming-interop.sh [test pattern], TestInterop by default
# Environment: IMAGE (default golang:1.27).
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
image=${IMAGE:-golang:1.27}

# The module cache volume is shared with the other container runs.
docker run --rm \
    -e GOTOOLCHAIN=auto -e FIT_MEDIA_INTEROP=1 \
    -v "$repo":/fit -v fitgomod:/go/pkg/mod -w /fit \
    "$image" bash -c '
set -e
if ! command -v ffmpeg >/dev/null; then
    apt-get update -qq
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends ffmpeg >/dev/null
fi
ffmpeg -hide_banner -version | head -1
exec go test -count=1 -v -timeout 15m -run "${1:-TestInterop}" ./streaming/
' run "$@"
