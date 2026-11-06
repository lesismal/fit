#!/bin/sh
# Inside the container: handler/vod's ffmpeg checks (TestInterop), when the
# test binary is there; then fit serving a library genlib.sh makes, and
# headless Chromium playing it with hls.js (player.html), whose results
# collector.py takes. What they find goes to /w/out.
set -u
status=0
mkdir -p /w/out
ffmpeg -hide_banner -version | head -1 | tee /w/out/versions.txt
chromium --version | tee -a /w/out/versions.txt

if [ -x /w/vod.test ]; then
	# The test finds genlib.sh as it does in the repository.
	mkdir -p /w/t/handler/vod /w/t/test/docker/vod
	cp /w/genlib.sh /w/t/test/docker/vod/
	(cd /w/t/handler/vod && FIT_MEDIA_INTEROP=1 /w/vod.test -test.run '^TestInterop$' -test.v -test.timeout 20m) \
		>/w/out/interop.log 2>&1 || status=1
	grep -E '^(---|ok|FAIL|PASS)|segments valid|interop_test' /w/out/interop.log
fi

sh /w/genlib.sh /w/lib "${SECONDS_FILM:-30}"
/w/fit run -c /w/fit.json >/w/out/fit.log 2>&1 &
cp /w/player.html /w/collector.py /opt/hlsjs/
python3 /opt/hlsjs/collector.py 9400 /w/out/result.json 900 &
collector=$!
sleep 1
chromium --headless=new --no-sandbox --disable-gpu --disable-dev-shm-usage --no-first-run \
	--autoplay-policy=no-user-gesture-required --user-data-dir=/tmp/chrome \
	"http://127.0.0.1:9400/player.html?base=http://127.0.0.1:9401/vod/films/&items=${ITEMS:-films/bbb,clips/hevc,clips/av1,clips/opus,clips/gop3s,music/song}" \
	>/w/out/chromium.log 2>&1 &
wait $collector || status=1
exit $status
