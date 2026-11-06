#!/bin/sh
# The interop test of the streaming role's channels (TestChannelInterop):
# ffmpeg makes the items, fit plays them as channels, ffprobe reads every
# egress (RTMP, RTSP, SRT, HTTP-FLV, HLS fMP4 and MPEG-TS) and checks that
# their timestamps run on across items; a headless Chromium plays them with
# hls.js; and a channel is pushed over RTMP and SRT to MediaMTX, an outside
# server in a container, which is restarted, and gets the stream back.
#
#   test/docker/channels/e2e.sh
#
# It builds streaming's test binary for Linux here, and runs run.sh on a
# Docker host on Linux: this machine, or, with SSH set to the command that
# reaches one, as SSH="ssh -S ~/.ssh/fitbench.sock root@192.168.100.51",
# that one, in /root/fit-channels-e2e, under flock /root/bench.lock. The
# host needs Docker and Docker Hub. It is not a load test: nothing is
# measured.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
arch=${ARCH:-amd64}
(cd "$root" && GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go test -c -o "$work/streaming.test" ./streaming)
cp "$here/Dockerfile" "$here/run.sh" "$work/"

if [ -z "${SSH:-}" ]; then
	exec "$work/run.sh"
fi
remote=/root/fit-channels-e2e
$SSH "rm -rf $remote && mkdir -p $remote"
tar -C "$work" -cf - . | $SSH "tar -C $remote -xf -"
$SSH "flock /root/bench.lock $remote/run.sh"
