# 022：读取机器内存，使用 golang.org/x/sys

[English](022-machine-memory.md) | [简体中文](022-machine-memory.zh-CN.md)

状态：已接受（2026-10-24）。

## 背景

`limits.max_pending_bytes` 是 fib 对所有连接上等待客户端读取的字节的总预算，默认取 fit 可用内存的一半，因此需要从系统读取内存大小。

## 决定

- **`internal/sysmem` 在第一次调用时读取一次。** Linux：`sysinfo`，并取 cgroup 内存上限（v2 的 `memory.max`，否则 v1 的
  `memory.limit_in_bytes`）中较小者，以适配容器。Windows：经标准库 `syscall` 调用 `GlobalMemoryStatusEx`。macOS 和 BSD：经
  `golang.org/x/sys/unix` 的 `SysctlUint64` 读取 `hw.memsize`、`hw.physmem` 或 `hw.physmem64`。其他系统或读取失败时为 0，默认值取 256 MiB。
- **`golang.org/x/sys` 成为直接依赖。** 它原本已经作为 fib 和 `golang.org/x/crypto` 的间接依赖在 `go.mod` 中，不会引入新的代码；
  标准库的 `syscall.Sysctl` 返回字符串，读取二进制整数不可靠。
