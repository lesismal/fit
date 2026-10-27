# fit

[![CI](https://github.com/lesismal/fit/actions/workflows/ci.yml/badge.svg)](https://github.com/lesismal/fit/actions/workflows/ci.yml) [![Coverage](https://img.shields.io/endpoint?url=https%3A%2F%2Fraw.githubusercontent.com%2Flesismal%2Ffit%2Fbadges%2Fcoverage.json)](https://github.com/lesismal/fit/actions/workflows/ci.yml) [![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

[English](README.md) | [简体中文](README.zh-CN.md)

fit 是基于 [fib](https://github.com/lesismal/fib) 的反向代理、负载均衡、TLS 终结、静态文件服务和 API 网关，
定位与 nginx、Caddy、Traefik 相同。

它支持 HTTP/1.1、HTTP/2 和 HTTP/3，默认启用 HTTPS；代理 HTTP、WebSocket、gRPC、TCP 和 UDP，带健康检查和负载均衡；
提供静态文件；缓存响应；路由来自严格的配置，用 fit 自己的 .conf 格式、YAML 或 JSON 编写（可通过 Admin API 原地修改），或来自 Docker、Consul、etcd 和
Kubernetes。所有里程碑的迭代（包括 1.0 的）都已完成，1.0 要求的外部安全审计尚待进行；已完成的内容见 [开发进度](docs/progress.zh-CN.md)，与 nginx、Caddy、Traefik 的
对比测量方法见 [bench](bench/README.zh-CN.md)。

## 文档

- [快速上手](docs/getting-started.zh-CN.md)：从零开始搭起一个 HTTPS 站点。
- [配置](docs/configuration.zh-CN.md)：配置可用的三种格式 [.conf](docs/reference/fit.zh-CN.md)、
  [YAML](docs/reference/yaml.zh-CN.md) 和 [JSON](docs/reference/json.zh-CN.md)，配置如何分散在多个文件中，以及如何运行、检查、
  转换和修改。
- [配置模板](templates/README.zh-CN.md)：每种格式各一份完整配置，包含全部字段，可选值写在注释中。
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

## 安装

有 Go 1.27.3 或更高版本时，`go install` 把 fit 构建到 `$(go env GOPATH)/bin`，可以是最新发布的版本，
也可以是指定的版本、标签或提交：

```bash
go install github.com/lesismal/fit/cmd/fit@latest
```

```bash
go install github.com/lesismal/fit/cmd/fit@v1.0.0
```

`go get github.com/lesismal/fit@latest`（或 `@v1.0.0`）把 fit 加入你自己的模块，用作库，或者构建带插件的
fit（`fit build`）；它不安装命令。

没有 Go 时，从 [GitHub Releases](https://github.com/lesismal/fit/releases) 下载预编译的二进制：
Linux（amd64、arm64、armv7、386）、macOS（amd64、arm64）、Windows（amd64、arm64）和 FreeBSD
（amd64、arm64），每个是一个 `.tar.gz`（Windows 为 `.zip`），名为 `fit_<版本>_<系统>_<架构>`，
SHA-256 校验和在 `checksums.txt` 里。下载最新版本，以 amd64 上的 Linux 为例：

```bash
VERSION=$(curl -fsSLI -o /dev/null -w '%{url_effective}' https://github.com/lesismal/fit/releases/latest | sed 's|.*/v||')
curl -fsSLO "https://github.com/lesismal/fit/releases/download/v${VERSION}/fit_${VERSION}_linux_amd64.tar.gz"
tar -xzf "fit_${VERSION}_linux_amd64.tar.gz"
sudo install "fit_${VERSION}_linux_amd64/fit" /usr/local/bin/fit
```

要指定版本，改为设置 `VERSION=1.0.0`。另外也发布了容器镜像：`ghcr.io/lesismal/fit`
（[deploy/README.zh-CN.md](deploy/README.zh-CN.md)）。

## 构建

fit 需要 Go 1.27.3 或更高版本。

```bash
make build
```

```bash
./bin/fit validate -c examples/hello.conf
./bin/fit run -c examples/hello.conf
```

`examples/hello.conf` 在 8080 端口上回答 `/hello`：

```fit
listener web {
	address :8080
	protocols http/1.1 h2c
}

route hello {
	match {
		path /hello
	}
	handler respond {
		body "hello from fit\n"
	}
}

route not-found {
	priority -1
	handler respond {
		status 404
		body "not found\n"
	}
}
```

配置可以是 fit 自己的 [.conf 文件](docs/reference/fit.zh-CN.md)（如上）、YAML（`examples/hello.yaml`）或 JSON
（`examples/hello.json`，可以写 `//` 和 `/* */` 注释，最后一个元素后可以带逗号），三者字段相同，并可以分散在多个文件和目录中
（`fit run -c base.conf -c conf.d`）。`fit adapt` 在这些格式之间转换，也能转换 nginx 配置和 Caddyfile，并列出未转换的部分。
详见 [配置](docs/configuration.zh-CN.md)。

`fit run` 收到 SIGHUP 时重新加载配置文件，未变化的 listener 保持不动，新配置有错时继续使用正在运行的配置；收到
SIGINT 或 SIGTERM 时优雅退出。

`make check` 会运行 gofmt、go vet、staticcheck，以及开启 race 检测的测试。

## 许可证

Apache License 2.0，详见 [LICENSE](LICENSE)。
