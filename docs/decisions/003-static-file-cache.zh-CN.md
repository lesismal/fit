# 003：static handler 使用自己的文件缓存

[English](003-static-file-cache.md) | [简体中文](003-static-file-cache.zh-CN.md)

状态：已采纳，见 I12（2026-10-19）。

## 背景

计划让 `static` handler 基于 fib 的 `http.FileCache`。在 fit 使用的 fib v1.2.0 中，`FileCache.ServeFile`
自己完成整个请求：不发送 `ETag`，不知道索引文件和目录，凡是不能服务的都自己写 404，缓存文件的响应头也
不对外公开。而 handler 需要这些：`ETag` 和索引文件属于 I12，I13 的 `try_files` 回退和目录列表需要在写任何
东西之前就知道文件不存在。

## 决定

- `handler/static` 仿照 `FileCache` 的设计，自己缓存小文件：从路径到内容的映射，以及只构建一次、所有响应
  共享的响应头；单个文件大小和总量都有上限。超过任一上限的文件通过 `net/http.ServeContent` 从磁盘读取。
- 每个请求对文件做一次 stat（一次系统调用），只要大小和修改时间不变，缓存副本就有效。这与 nginx 的默认
  行为相同（`open_file_cache` 默认关闭），因此不会让 fit 落后于 nginx 的标准；fib 在 Linux 上用 inotify
  省掉 stat 的做法是 fib 内部实现。
- `ETag` 仿照 nginx：修改时间（纳秒）和大小的十六进制。

## 影响

- 每个请求多一次 stat，而 `FileCache` 在 Linux 上能省掉。如果 I13 或 I23 的基准显示这有影响，应在 fib 中
  修复：让 `FileCache` 提供一个只查询（内容、响应头、不存在或是目录）而不应答请求的接口，并加上 `ETag`。
  之后 fit 通过 fib 的新版本迁移过去，而不是用 `replace`。
- 预压缩副本和 sendfile（I13）也由 fit 自己实现。
