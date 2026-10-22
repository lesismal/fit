# fit

[English](README.md) | [简体中文](README.zh-CN.md)

fit 是基于 [fib](https://github.com/lesismal/fib) 的反向代理、负载均衡、TLS 终结、静态文件服务和 API 网关，
定位与 nginx、Caddy、Traefik 相同。

fit 处于早期开发阶段。目前它可以按 JSON 配置通过 HTTP/1.1 和 h2c 返回固定响应；已完成的内容见
[开发进度](docs/progress.zh-CN.md)，与 nginx、Caddy、Traefik 的对比测量方法见 [bench](bench/README.zh-CN.md)。

## 文档

- [nginx、Caddy、Traefik 对比](docs/comparison.zh-CN.md)：fit 从三者各借鉴什么。
- [路线图](docs/roadmap.zh-CN.md)：目标、设计原则和里程碑。
- [开发计划](docs/dev-plan.zh-CN.md)：拆分成小迭代的里程碑。
- [开发进度](docs/progress.zh-CN.md)：已完成的内容，以及从哪里继续。
- [部署](deploy/README.zh-CN.md)：容器镜像、Helm chart 和 systemd unit；以及 Docker Compose 和 Kubernetes 的
  [示例](examples/README.zh-CN.md)。

## 构建

fit 需要 Go 1.27 或更高版本。

```bash
make build
```

```bash
./bin/fit validate -c examples/hello.json
./bin/fit run -c examples/hello.json
```

`fit run` 收到 SIGHUP 时重新加载配置文件，未变化的 listener 保持不动，新配置有错时继续使用正在运行的配置；收到
SIGINT 或 SIGTERM 时优雅退出。

`make check` 会运行 gofmt、go vet、staticcheck，以及开启 race 检测的测试。
