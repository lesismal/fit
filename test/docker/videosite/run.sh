#!/bin/sh
# Inside the container: the example video site (fit-examples' video-site) as
# its fit.conf runs it, its films made by its make-media.sh, its API beside
# fit, and drive.py driving headless Chromium through it. The lifetimes are
# shortened so that URLs and grants expire within the test: URLs of 8s, and
# grants that live 4s past them. What it finds goes to /w/out.
set -u
mkdir -p /w/out
cd /w/ex
chromium --version | tee /w/out/versions.txt
ffmpeg -hide_banner -version | head -1 | tee -a /w/out/versions.txt
sh video-site/make-media.sh "${SECONDS_FILM:-20}" >/w/out/media.log 2>&1 || { cat /w/out/media.log; exit 1; }
sed -e 's/^\(\t*\)ttl 2m$/\1ttl 8s/' -e 's/^\(\t*\)play_grace 30m$/\1play_grace 4s/' video-site/fit.conf >/w/fit-test.conf
if ! grep -q 'ttl 8s' /w/fit-test.conf || ! grep -q 'play_grace 4s' /w/fit-test.conf; then
	echo "FAIL the example's url_signing changed: ttl 2m and play_grace 30m were not found to shorten"
	exit 1
fi
/w/site-api -ttl 8s >/w/out/api.log 2>&1 &
api=$!
/w/fit run -c /w/fit-test.conf >/w/out/fit.log 2>&1 &
fit=$!
status=0
python3 -I /w/drive.py /w/out || status=$?
kill "$fit" "$api" 2>/dev/null
exit $status
