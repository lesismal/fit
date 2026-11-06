# The channels' interop test (streaming's TestChannelInterop): ffmpeg with
# libx264 and libsrt, Chromium, and hls.js, beside the test binary e2e.sh
# builds for Linux and mounts at /w.
ARG BASE_IMAGE=debian:trixie
FROM ${BASE_IMAGE}
ARG HLSJS_VERSION=1.5.20
RUN apt-get update \
 && apt-get install -y --no-install-recommends ffmpeg chromium ca-certificates curl \
 && rm -rf /var/lib/apt/lists/* \
 && curl -fsSL "https://cdn.jsdelivr.net/npm/hls.js@${HLSJS_VERSION}/dist/hls.min.js" -o /opt/hls.min.js
