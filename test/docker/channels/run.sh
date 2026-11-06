#!/bin/sh
# On the Docker host, in the directory e2e.sh filled: the outside server,
# MediaMTX, taking RTMP on :19350 and SRT on :18890, and the test in the
# image of this directory's Dockerfile, on the host's network. When the test
# writes out/signal/restart, MediaMTX is restarted, and out/signal/restarted
# tells the test so.
set -eu
work=$(cd "$(dirname "$0")" && pwd)
image=${IMAGE:-fit-channels-e2e}
mtx=${MTX_IMAGE:-bluenviron/mediamtx:1.15.3}
name=fit-channels-mtx
docker build -q -t "$image" "$work" >/dev/null
docker rm -f "$name" >/dev/null 2>&1 || true
docker run -d --name "$name" --network host \
	-e MTX_RTSP=no -e MTX_HLS=no -e MTX_WEBRTC=no -e MTX_API=no -e MTX_METRICS=no -e MTX_PPROF=no -e MTX_PLAYBACK=no \
	-e MTX_RTMPADDRESS=:19350 -e MTX_SRTADDRESS=:18890 "$mtx" >/dev/null
trap 'docker rm -f "$name" >/dev/null 2>&1 || true; kill "$watch" 2>/dev/null || true' EXIT

sig=$work/out/signal
rm -rf "$sig"
mkdir -p "$sig"
(
	while [ ! -e "$sig/restart" ]; do sleep 0.2; done
	docker restart -t 1 "$name" >/dev/null
	touch "$sig/restarted"
) &
watch=$!

docker run --rm --network host -v "$work:/w" -w /w \
	-e FIT_MEDIA_INTEROP=1 -e FIT_CHROMIUM=/usr/bin/chromium -e FIT_HLSJS=/opt/hls.min.js \
	-e FIT_PUSH_RTMP=rtmp://127.0.0.1:19350/relay/tv -e FIT_PUSH_RTMP_PLAY=rtmp://127.0.0.1:19350/relay/tv \
	-e 'FIT_PUSH_SRT=srt://127.0.0.1:18890?streamid=publish:relay/srt&latency=200' \
	-e 'FIT_PUSH_SRT_PLAY=srt://127.0.0.1:18890?streamid=read:relay/srt' \
	-e FIT_PUSH_SIGNAL=/w/out/signal \
	"$image" /w/streaming.test -test.run 'TestChannelInterop' -test.v -test.count 1 -test.timeout 10m
