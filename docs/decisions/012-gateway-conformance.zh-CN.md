# 012：Gateway API 一致性测试，放在单独的模块中

[English](012-gateway-conformance.md) | [简体中文](012-gateway-conformance.zh-CN.md)

状态：已采纳，见 I61（2026-10-22）。

## 背景

I61 针对 fit 的 Kubernetes provider 运行 Gateway API 的一致性测试。测试套件是 Go 包
`sigs.k8s.io/gateway-api/conformance`：它把自己的清单应用到集群，等待实现接受并配置它们，再向 Gateway 的地址发请求。
它需要 client-go、controller-runtime 和 Gateway API 自己的类型，也就是决策 [011](011-kubernetes-client.zh-CN.md)
没有引入 fit 的那四十多个模块。

## 决定

- **`test/conformance` 是单独的 Go 模块** `github.com/lesismal/fit/test/conformance`，依赖
  `sigs.k8s.io/gateway-api` v1.3.0，与 CI 安装的 CRD 版本相同。它唯一的测试带着自己的 flag 调用测试套件。fit 自己的
  模块、`go.mod`、二进制和 `go test ./...` 都不受影响：带 `go.mod` 的目录不属于上层模块。该模块不需要 fit 的任何东西，
  所以没有 `replace`，也没有 `go.work`。
- **fit 在集群外运行**，与 I58 的 e2e 测试一样：`test/conformance/run.sh` 创建 fit 的 GatewayClass，启动带
  Kubernetes provider 的 `fit` 二进制，Gateway 绑定在 127.0.0.1 上并把这个地址写进它们的状态（provider 的
  `addresses`，由 I61 新增），然后以 fit 声明的 profile `GATEWAY-HTTP` 运行测试套件的测试二进制并写出报告。与 I58 一样，
  经到 kind 节点的路由访问 pod。
- **CI 在单独的任务中运行它**，即 "Gateway API conformance (kind)"，并把报告和 fit 的日志作为 artifact 保留。基准主机
  在自己的 kind 集群上运行同一个脚本，用于修复失败项。

## 影响

- 测试套件的模块只由该任务以及手动运行它的人下载和构建。
- 升级 Gateway API 版本，就是改该模块的 `require`、CI 安装的 CRD，并修复新测试套件发现的失败项。
- 声明的 profile 写在 `run.sh` 中；以后声明的特性是那里的一个 flag。
