# 008：zstd 和 gzip 用 klauspost/compress，brotli 用 andybalholm/brotli

[English](008-compression-libraries.md) | [简体中文](008-compression-libraries.zh-CN.md)

状态：已接受，见 I46（2026-10-21）。

## 背景

I46 用 zstd、brotli 和 gzip 压缩响应。标准库有 gzip 和 flate，但没有 zstd 和 brotli；fit 在 fib 的 worker 上随响应体
写出而压缩，因此每个编码器的速度和内存对每个压缩的响应都有影响。候选（2026-10-21 时的版本）：

| | 是什么 | 给构建带来什么 |
| --- | --- | --- |
| `github.com/klauspost/compress` v1.20.3 | 纯 Go 的 zstd，以及比标准库更快、API 相同的 gzip 和 flate | 除标准库外没有依赖 |
| `github.com/andybalholm/brotli` v1.2.6 | Google C 版 brotli 编解码器的纯 Go 翻译 | 除标准库外没有依赖 |
| `github.com/google/brotli/go/cbrotli`、`github.com/DataDog/zstd` | C 库的绑定 | cgo，fit 的构建不用 |
| `compress/gzip` | 标准库 | 无 |

## 决定

- **zstd 和 gzip 用 `klauspost/compress`**：编码器可以流式写、flush 和 reset，gzip 可直接替换标准库且更快。zstd 使用
  `WithEncoderConcurrency(1)`，在调用者的 goroutine 上编码，不启动自己的 goroutine，窗口 256 KB，并开启
  `WithLowerEncoderMem`。
- **brotli 用纯 Go 的 `andybalholm/brotli`**，质量 1、4 或 6，窗口 256 KB（`LGWin` 18），同样可以 flush 和 reset。
- 不用 cgo：fit 只用 Go 工具链就能为 fib 支持的所有目标构建。

## 后果

- 多了两个模块，它们自己都没有依赖。
- 每个正在压缩的响应占用一个几百 KB 的编码器，响应结束时经池回收；`compress` 的 `max_in_flight`（默认 1024）限制同时
  使用的数量，超出时响应不压缩。
- 在基准机上，整体发送的 16 KB JSON 用默认级别时 zstd 6.3 µs、gzip 9.0 µs、brotli 16.6 µs（最快级别 10.6 µs），
  不压缩为 2.3 µs（`BenchmarkCompress`）。
