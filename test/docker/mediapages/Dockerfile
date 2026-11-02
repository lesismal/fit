# The browser test of fit's media pages: Chromium and Python, and fit,
# which e2e.sh builds for Linux and mounts at /w with the test's files.
ARG BASE_IMAGE=debian:stable-slim
FROM ${BASE_IMAGE}
RUN apt-get update \
 && apt-get install -y --no-install-recommends chromium python3 ca-certificates fonts-noto-cjk \
 && rm -rf /var/lib/apt/lists/*
ENTRYPOINT ["/w/run.sh"]
