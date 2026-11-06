# The vod handler's interop checks on Linux: ffmpeg and ffprobe for
# handler/vod's TestInterop, and Chromium with hls.js for player.html. fit
# and the test binary are built outside (e2e.sh) and mounted at /w.
ARG BASE_IMAGE=debian:trixie
FROM ${BASE_IMAGE}
ARG HLSJS_VERSION=1.5.20
RUN apt-get update \
 && apt-get install -y --no-install-recommends chromium ffmpeg python3 ca-certificates curl \
 && rm -rf /var/lib/apt/lists/*
# hls.js (Apache-2.0), at a version pinned here, beside the page.
RUN mkdir -p /opt/hlsjs \
 && curl -fsSL -o /opt/hlsjs/hls.min.js "https://cdn.jsdelivr.net/npm/hls.js@${HLSJS_VERSION}/dist/hls.min.js"
ENTRYPOINT ["/w/run.sh"]
