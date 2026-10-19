# 002: A benchmark harness in containers, driven by a Go program

[English](002-benchmark-harness.md) | [简体中文](002-benchmark-harness.zh-CN.md)

Status: accepted, in I07 and I08 (2026-10-19).

## Context

The roadmap holds fit to nginx's bar and asks every milestone to publish numbers against nginx,
Caddy and Traefik: throughput, latency percentiles, CPU, and memory per idle connection. The
numbers are only worth something if every server runs the same scenarios with equivalent
configs, on the same CPUs, under the same load generators, and if anyone can run them again from
one command.

## Decision

- **Everything runs in Docker.** Each server comes from its official image at a pinned version
  (fit from the binary built for the run, on `ubuntu:24.04`); the load generators, the proxy
  backend and the WebSocket backend have containers of their own. Each container is pinned to its
  own CPUs with `--cpuset-cpus`. On Linux they share the host's network (`--network host`), which
  costs nothing; elsewhere they use a bridge network, and the numbers are for comparison on that
  machine only.
- **The runner is a Go program**, `bench/cmd/fitbench`, using only the standard library and the
  `docker` CLI. Parsing wrk's and h2load's output is unit-tested, which a shell script's would not
  be. wrk reports through a Lua `done` hook that prints JSON, so its numbers are read exactly
  rather than from its human-readable summary.
- **Load generators**: wrk for HTTP/1.1 and h2load for h2c, Ubuntu's glibc builds, since musl's
  allocator holds a load generator back at high request rates.
- **Equivalence is checked, not assumed**: before each run, the harness fetches the scenario once
  and compares status, protocol and body with the expected ones.
- **CPU and memory come from the server's cgroup** (v2): `cpu.stat`'s `usage_usec` across the
  measured run, and `memory.stat`'s `anon` for memory. Anonymous memory counts every process in
  the container (nginx's workers as well as its master) without the page cache, which is what RSS
  is meant to show here; summing each process's RSS would count shared pages more than once.
- **Idle connection memory** is measured by `bench/cmd/idleconn`, a Go tool that opens N
  connections, makes one request on each, and holds them; each kind of connection runs against a
  freshly started server, and the cost per connection is the change in anonymous memory divided by
  N.
- **Static files are served from a Docker volume**, not a bind mount: on Docker Desktop a bind
  mount is slow enough to be what gets measured.

## Consequences

- The harness needs Docker and Go, nothing else on the host.
- Traefik serves neither fixed responses nor files, so it runs only the proxy scenario; fit runs
  the scenarios its modules support, and gains the others as they land.
- h2load reports no latency percentiles, so the h2c results have mean, standard deviation and
  maximum only. A load generator that does (an HTTP/2 mode of a wrk-like tool, or one of fit's own
  on fib) can replace it later behind the same result format.
- The WebSocket and HTTP/3 load generators the roadmap mentions are not part of M0: the idle
  WebSocket measurement covers WebSocket for now, and HTTP/3 waits for I10.
