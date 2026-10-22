# 016: The container image on distroless's static image, and whoami in the examples

[English](016-container-image.md) | [简体中文](016-container-image.zh-CN.md)

Status: accepted, in I66 (2026-10-22).

## Context

I66 packages fit as a container image, a Helm chart and systemd units, with examples for Docker
Compose and Kubernetes. The image needs a base, and the examples an app to send to. Neither is a Go
dependency, but both are images fit's users and CI pull.

## Decision

- **The image's base is `gcr.io/distroless/static-debian12:nonroot`.** fit builds without cgo, so
  it needs no libc; what it does need is CA certificates (upstream TLS, ACME, the providers' HTTPS),
  time zones, `/etc/passwd` with an unprivileged user, and a `/tmp`, which this image has in about
  2 MB, with no shell or package manager to patch. `scratch` would need all of those copied in by
  hand; Alpine or Debian add a shell and packages fit never runs. It runs as `nonroot` (65532):
  ports below 1024 come from Docker's default sysctl or the chart's, not from root or a capability.
- **The binary is built on the builder's platform** (`--platform=$BUILDPLATFORM`, `GOOS` and
  `GOARCH` from the target), so linux/amd64 and linux/arm64 build without emulation, with BuildKit's
  module and build caches.
- **The examples send to `traefik/whoami`**, pinned to v1.11.0: a few-MB app that answers with its
  host name, which shows which replica answered, and has `/health` for health checks. fit's own image
  answering fixed responses could stand in, but every replica would answer alike, and balancing would
  not show.

## Consequences

- No shell in the image: `docker exec` cannot open one, and debugging goes through `fit`'s own
  commands, its logs and the Admin API, or an ephemeral debug container.
- The Dockerfile needs BuildKit (`RUN --mount`, `$BUILDPLATFORM`), Docker's default builder since
  23.0 when buildx is installed.
