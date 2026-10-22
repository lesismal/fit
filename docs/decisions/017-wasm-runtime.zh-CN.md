# 017：WASM 插件基于 wazero，使用 fit 自己的 ABI

[English](017-wasm-runtime.md) | [简体中文](017-wasm-runtime.zh-CN.md)

状态：已采纳，见 I71（2026-10-23）。

## 背景

I71 让路由为每个请求运行一个 WebAssembly 插件：运行时加载、在沙箱中、在自己的池上运行，读取并修改请求和响应的
字段。这需要一个 WebAssembly 运行时（fit 不宜自己编写）以及 fit 与插件之间的 ABI。路线图要求运行时是纯 Go 的，
以便 fit 仍是不依赖 cgo 构建的单一静态二进制文件，并要求考虑 Proxy-Wasm ABI，以便复用已有的过滤器。

## 决定

- **运行时用 wazero**（`github.com/tetratelabs/wazero`，v1.12.0，Apache 2.0）：纯 Go 的 WebAssembly 运行时，除
  `golang.org/x/sys` 外没有依赖；在 amd64 和 arm64 上编译为机器码，其他平台解释执行；能限制实例的内存
  （`WithMemoryLimitPages`），在调用的 context 结束时停止调用（`WithCloseOnContextDone`），并缓存编译结果。其他
  选择都要通过 cgo 绑定 C 或 Rust 运行时（wasmtime-go、已不再维护的 wasmer-go、wasm3），静态二进制文件排除了它们。
- **ABI 是 fit 自己的、精简的**（I71）：插件导出 `fit_on_request`，并从模块 `fit` 导入函数，用于读取请求的方法、
  路径、查询串、主机、客户端地址和插件配置，读取、设置、添加和删除请求与响应的字段，自己回答请求，以及写日志。
  Proxy-Wasm 的 ABI 大得多（context、缓冲区、属性、定时器、外部调用），由 I72 视测量结果尽量实现。
- **插件是 WASI reactor**，Go 自 1.24 起可以构建（`GOOS=wasip1 GOARCH=wasm`、`-buildmode=c-shared`、
  `//go:wasmexport`）：`plugin/wasm/guest` 包是 ABI 的 Go 端。任何面向 WASI、能导出函数并导入宿主函数的语言都同样适用。
- **调用在中间件自己的池上运行**：每个实例一个 goroutine（默认与 fit 的 CPU 数相同，最多 16 个），每个实例有自己的
  内存；队列有上限；看门狗通过取消实例的 context 停止超时的调用，之后重建该实例。从不在 fib worker 上运行：插件的
  代码在被停止之前可以运行任意长的时间。

## 影响

- 链接 wazero 后，fit 的二进制文件增大约 3.9 MB（linux/amd64 上从 21.1 MB 到 24.9 MB）。
- 经过插件的请求要付出交给池再交回的开销，以及调用本身的开销，测量结果见 I71 的进度记录；每一项都有上限：实例数和
  队列、调用的超时、等待的超时，以及实例的内存。
- Go 插件的实例带有 Go 的运行时，每个几 MB；运行时的启动（`_initialize`）在创建实例时进行，而不是每个请求一次。
