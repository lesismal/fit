#!/bin/sh
# The video site end to end (I137), in a Linux container: fit-examples'
# video-site as its fit.conf runs it (a media library, vod and a channel, its
# own API logging viewers in and asking media_tokens for playback URLs, and
# fit's media pages), its films made by its make-media.sh, and headless
# Chromium driven over DevTools (drive.py): a viewer logs in to the site's
# API, plays a film and the channel through the URLs it issues, seeks, and
# is refused once a URL and its grant expire; a member of the policy does the
# same on media_pages' library and player pages. They are ordinary tests,
# not load tests: nothing is measured.
#
#   test/docker/videosite/e2e.sh [out-dir]
#
# It needs Docker and Go here, and a checkout of fit-examples in EXAMPLES
# (../fit-examples beside this repository by default). With HOST=user@host
# (and SSH, its options in SSH_OPTS) it builds here and runs on that host's
# Docker, as on the benchmark host. The results, fit's log, the API's and
# Chromium's go to out-dir (a temporary directory when left out). IMAGE names
# an image to use instead of building this directory's Dockerfile; LOCK, a
# file on HOST the run holds with flock, as /root/bench.lock on a host shared
# with benchmarks.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
examples=$(cd "${EXAMPLES:-$root/../fit-examples}" && pwd)
[ -f "$examples/video-site/fit.conf" ] || { echo "no video-site example in $examples: set EXAMPLES" >&2; exit 2; }
out=${1:-$(mktemp -d)}
mkdir -p "$out"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
image=${IMAGE:-fit-videosite-e2e}
ssh_cmd="ssh ${SSH_OPTS:-}"
arch=${GOARCH:-amd64}

(cd "$root" && GOOS=linux GOARCH=$arch CGO_ENABLED=0 go build -o "$work/fit" ./cmd/fit)
(cd "$examples" && GOOS=linux GOARCH=$arch CGO_ENABLED=0 go build -o "$work/site-api" ./video-site/api)
mkdir -p "$work/ex/video-site"
for f in fit.conf secret catalog.json make-media.sh web; do cp -R "$examples/video-site/$f" "$work/ex/video-site/"; done
cp "$here/run.sh" "$here/drive.py" "$here/Dockerfile" "$work/"
chmod +x "$work/run.sh"

if [ -z "${HOST:-}" ]; then
	[ -n "${IMAGE:-}" ] || docker build -q -t "$image" "$here" >/dev/null
	status=0
	docker run --rm -v "$work:/w" "$image" || status=$?
	cp -r "$work/out/." "$out/"
	echo "results and logs: $out"
	exit $status
fi

remote=/root/fit-videosite-e2e
$ssh_cmd "$HOST" "rm -rf $remote && mkdir -p $remote"
COPYFILE_DISABLE=1 tar -C "$work" -cf - . | $ssh_cmd "$HOST" "tar -C $remote -xf -"
[ -n "${IMAGE:-}" ] || $ssh_cmd "$HOST" "docker build -q -t $image $remote >/dev/null"
status=0
$ssh_cmd "$HOST" "${LOCK:+flock $LOCK }docker run --rm -v $remote:/w $image" || status=$?
$ssh_cmd "$HOST" "tar -C $remote/out -cf - ." | tar -C "$out" -xf -
echo "results and logs: $out"
exit $status
