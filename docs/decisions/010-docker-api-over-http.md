# 010: The Docker provider speaks the Engine API over plain HTTP

[English](010-docker-api-over-http.md) | [简体中文](010-docker-api-over-http.zh-CN.md)

Status: accepted, in I57 (2026-10-22).

## Context

I57's Docker provider reads containers and follows their events from the Docker daemon, whose
Engine API is HTTP over a Unix socket (or TCP, behind a socket proxy). It can be reached with the
Docker SDK (`github.com/docker/docker/client`, now `github.com/moby/moby/client`) or with
`net/http`.

## Decision

- **Plain `net/http`, no SDK.** The provider needs two calls: `GET /containers/json` (running
  containers, with their labels, networks and ports) and `GET /events` (a stream of JSON lines),
  plus `filters` in their query. That is a few dozen lines with the standard library. The SDK
  brings the moby module and its dependency tree (OpenTelemetry, containerd's types, docker/go-
  connections and more) into the binary, for two calls; its versions move with the daemon's and
  have broken their import paths before.
- **Unversioned paths.** The daemon serves an unversioned path at its own API version. Docker 29
  refuses versions below 1.44, so a fixed old version would fail there, while the fields read
  (`Id`, `Names`, `State`, `Status`, `Labels`, `Ports`, `HostConfig.NetworkMode`,
  `NetworkSettings.Networks.*.IPAddress`) have been in every version since 1.24.
- **Endpoints**: `unix://<path>` (`/var/run/docker.sock` by default), or `tcp://host:port` for a
  socket proxy, in plain HTTP; TLS to a remote daemon can come later if asked for.
- **Bounded and in the control plane**: the provider runs on goroutines of its own; a list is
  read whole, at most 16 MiB, within `request_timeout`; an event line is at most 1 MiB; at most
  `max_containers` containers are translated; a lost connection is retried 1s to 30s apart.

## Consequences

- No new dependency in `go.mod`.
- If a later field is needed that is not in old versions, the provider reads `/version` and keeps
  to what the daemon has, rather than picking a version.
