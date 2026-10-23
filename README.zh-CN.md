# fit

[English](README.md) | [简体中文](README.zh-CN.md)

fit 是基于 [fib](https://github.com/lesismal/fib) 的反向代理、负载均衡、TLS 终结、静态文件服务和 API 网关，
定位与 nginx、Caddy、Traefik 相同。

它支持 HTTP/1.1、HTTP/2 和 HTTP/3，默认启用 HTTPS；代理 HTTP、WebSocket、gRPC、TCP 和 UDP，带健康检查和负载均衡；
提供静态文件；缓存响应；路由来自严格的 JSON 或 YAML 配置（可通过 Admin API 原地修改），或来自 Docker、Consul、etcd 和
Kubernetes。所有里程碑的迭代（包括 1.0 的）都已完成，1.0 要求的外部安全审计尚待进行；已完成的内容见 [开发进度](docs/progress.zh-CN.md)，与 nginx、Caddy、Traefik 的
对比测量方法见 [bench](bench/README.zh-CN.md)。

## 文档

- [快速上手](docs/getting-started.zh-CN.md)：从零开始搭起一个 HTTPS 站点。
- [配置](docs/configuration.zh-CN.md)：配置可用的三种格式 [Caddyfile](docs/reference/caddyfile.zh-CN.md)、
  [YAML](docs/reference/yaml.zh-CN.md) 和 [JSON](docs/reference/json.zh-CN.md)，以及配置如何运行、检查、转换和修改。
- [配置模板](templates/README.zh-CN.md)：Caddyfile、YAML 和 JSON 各一份完整配置，包含全部字段，可选值写在注释中。
- [配置参考](docs/reference/config.zh-CN.md)：逐章列出全部字段和每个模块的参数，附三种格式的示例；由代码生成。
- 从 [nginx](docs/migrating-from-nginx.zh-CN.md)、[Caddy](docs/migrating-from-caddy.zh-CN.md) 和
  [Traefik](docs/migrating-from-traefik.zh-CN.md) 迁移。
- [编写模块](docs/modules.zh-CN.md)：用 Go 扩展 fit，通过 `fit build` 编入。
- [nginx、Caddy、Traefik 对比](docs/comparison.zh-CN.md)：fit 从三者各借鉴什么。
- [路线图](docs/roadmap.zh-CN.md)：目标、设计原则和里程碑。
- [开发计划](docs/dev-plan.zh-CN.md)：拆分成小迭代的里程碑。
- [开发进度](docs/progress.zh-CN.md)：已完成的内容，以及从哪里继续。
- [兼容性](docs/compatibility.zh-CN.md)：从 1.0 起保持稳定的内容，以及重命名字段的处理。
- [部署](deploy/README.zh-CN.md)：容器镜像、Helm chart 和 systemd unit；以及 Docker Compose 和 Kubernetes 的
  [示例](examples/README.zh-CN.md)。

## 构建

fit 需要 Go 1.27.3 或更高版本。

```bash
make build
```

```bash
./bin/fit validate -c examples/hello.json
./bin/fit run -c examples/hello.json
```

配置可以是 Caddyfile（`examples/hello.caddyfile`）、YAML（`examples/hello.yaml`）或 JSON（`examples/hello.json`，可以写
`//` 和 `/* */` 注释，最后一个元素后可以带逗号）；`fit run` 直接接受其中任何一种，`fit adapt` 在它们之间转换，也能转换
nginx 配置，并列出未转换的部分。详见 [配置](docs/configuration.zh-CN.md)。

`fit run` 收到 SIGHUP 时重新加载配置文件，未变化的 listener 保持不动，新配置有错时继续使用正在运行的配置；收到
SIGINT 或 SIGTERM 时优雅退出。

`make check` 会运行 gofmt、go vet、staticcheck，以及开启 race 检测的测试。
