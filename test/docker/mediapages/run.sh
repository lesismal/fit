#!/bin/sh
# Inside the container: fit with fit.json, and two headless Chromiums, one
# with fake devices (DevTools on 9301) and one with none at all (9302).
/w/fit run -c /w/fit.json > /w/out/fit.log 2>&1 &
CR="--headless=new --no-sandbox --disable-gpu --disable-dev-shm-usage --no-first-run --use-fake-ui-for-media-stream --autoplay-policy=no-user-gesture-required --remote-allow-origins=*"
chromium $CR --remote-debugging-port=9222 --user-data-dir=/tmp/fake --use-fake-device-for-media-stream about:blank > /w/out/chrome-fake.log 2>&1 &
chromium $CR --remote-debugging-port=9223 --user-data-dir=/tmp/none about:blank > /w/out/chrome-none.log 2>&1 &
python3 /w/fwd.py 9301 9222 &
python3 /w/fwd.py 9302 9223 &
wait
