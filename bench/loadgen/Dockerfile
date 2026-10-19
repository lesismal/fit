# The load generators fitbench runs: wrk for HTTP/1.1 and h2load for h2c,
# with curl to check each server's responses first. Ubuntu's glibc builds,
# since musl's allocator holds a load generator back at high request rates.
ARG BASE=ubuntu:24.04
FROM ${BASE}
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
    && apt-get install -y --no-install-recommends wrk nghttp2-client curl ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY report.lua /fitbench/report.lua
