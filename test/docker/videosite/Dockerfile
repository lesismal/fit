# The video site's end-to-end test (I137): ffmpeg makes the example's
# films, and headless Chromium, driven over DevTools by drive.py, plays them
# through the example's page and fit's media pages. fit, the example's API
# and the example's files are built and copied outside (e2e.sh) and mounted
# at /w. hls.js is fit's own copy, which media_pages serves.
ARG BASE_IMAGE=debian:trixie
FROM ${BASE_IMAGE}
RUN apt-get update \
 && apt-get install -y --no-install-recommends chromium ffmpeg python3 ca-certificates \
 && rm -rf /var/lib/apt/lists/*
ENTRYPOINT ["/w/run.sh"]
