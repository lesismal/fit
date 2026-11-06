#!/bin/sh
# The vod handler's interop checks in a Linux container: handler/vod's
# TestInterop (ffmpeg plays, seeks and probes every segment of every item),
# and hls.js in headless Chromium playing and seeking every item (player.html).
# They are ordinary tests, not load tests: nothing is measured.
#
#   test/docker/vod/e2e.sh [out-dir]
#
# It needs Docker and Go here. With HOST=user@host (and SSH, its options
# in SSH_OPTS) it builds here and runs on that host's Docker, as on the
# benchmark host, whose Linux is the reference. The results, fit's log and
# Chromium's go to out-dir (a temporary directory when left out). IMAGE
# names an image to use instead of building this directory's Dockerfile.
#
# Safari's own player (AVFoundation) cannot be driven from here; by hand, on
# a Mac with ffmpeg (libx264, libx265, libsvtav1, libopus) and Go:
#
#   test/docker/vod/genlib.sh /tmp/vodlib
#   sed 's|/w/lib|/tmp/vodlib|; s|127.0.0.1:9401|0.0.0.0:9401|' test/docker/vod/fit.json >/tmp/vod.json
#   go run ./cmd/fit run -c /tmp/vod.json
#
# then open in Safari, on the Mac or an iPhone on the same network, each of
# http://<mac>:9401/vod/films/<item>/index.m3u8 and .../index.ts.m3u8 for the
# items films/bbb, clips/hevc, clips/opus, clips/gop3s and music/song
# (clips/av1 needs an Apple chip with AV1 decoding, M3 or A17 Pro on): each
# should play with sound from the start, seek to the middle and play on there,
# and films/bbb offer two audio tracks and English subtitles in the player's
# menu, the subtitles shown from 0:01 to 0:03 and 0:05 to 0:08.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
out=${1:-$(mktemp -d)}
mkdir -p "$out"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
image=${IMAGE:-fit-vod-e2e}
ssh_cmd="ssh ${SSH_OPTS:-}"

(cd "$root" && GOOS=linux GOARCH=${GOARCH:-amd64} CGO_ENABLED=0 go build -o "$work/fit" ./cmd/fit)
(cd "$root" && GOOS=linux GOARCH=${GOARCH:-amd64} CGO_ENABLED=0 go test -c -o "$work/vod.test" ./handler/vod)
cp "$here/run.sh" "$here/genlib.sh" "$here/fit.json" "$here/player.html" "$here/collector.py" "$here/Dockerfile" "$work/"
chmod +x "$work/run.sh" "$work/genlib.sh"

if [ -z "${HOST:-}" ]; then
	[ -n "${IMAGE:-}" ] || docker build -q -t "$image" "$here" >/dev/null
	status=0
	docker run --rm -v "$work:/w" -e ITEMS="${ITEMS:-}" "$image" || status=$?
	cp -r "$work/out/." "$out/"
	echo "results and logs: $out"
	exit $status
fi

remote=/root/fit-vod-e2e
$ssh_cmd "$HOST" "rm -rf $remote && mkdir -p $remote"
COPYFILE_DISABLE=1 tar -C "$work" -cf - . | $ssh_cmd "$HOST" "tar -C $remote -xf -"
[ -n "${IMAGE:-}" ] || $ssh_cmd "$HOST" "docker build -q -t $image $remote >/dev/null"
status=0
$ssh_cmd "$HOST" "docker run --rm -v $remote:/w -e ITEMS='${ITEMS:-}' $image" || status=$?
$ssh_cmd "$HOST" "tar -C $remote/out -cf - ." | tar -C "$out" -xf -
echo "results and logs: $out"
exit $status
