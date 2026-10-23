# 022: Reading the machine's memory, with golang.org/x/sys

[English](022-machine-memory.md) | [简体中文](022-machine-memory.zh-CN.md)

Status: accepted (2026-10-24).

## Context

`limits.max_pending_bytes`, fib's budget for the bytes waiting for clients across every
connection, defaults to half the memory fit may use, which has to be read from the system.

## Decision

- **`internal/sysmem` reads it once, at the first call.** Linux: `sysinfo`, lowered to the
  cgroup's memory limit (v2's `memory.max`, else v1's `memory.limit_in_bytes`) as in a container.
  Windows: `GlobalMemoryStatusEx`, through the standard library's `syscall`. macOS and the BSDs:
  the `hw.memsize`, `hw.physmem` or `hw.physmem64` sysctl, through `golang.org/x/sys/unix`'s
  `SysctlUint64`. Elsewhere, or when reading fails, nothing, and the default is 256 MiB.
- **`golang.org/x/sys` becomes a direct dependency.** It was already in `go.mod`, indirect through
  fib and `golang.org/x/crypto`, so nothing new is built; the standard library's `syscall.Sysctl`
  returns a string and is unreliable for binary integers.
