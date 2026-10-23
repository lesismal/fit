# 快速上手

[English](getting-started.md) | [简体中文](getting-started.zh-CN.md)

本指南带你从零开始，用 fit 搭起一个通过 HTTPS 提供服务的站点：静态文件、被代理的应用，以及在运行中修改配置。每一步都在
同一个配置文件上添加内容；每个字段见 [配置参考](reference/config.zh-CN.md)。

## 安装

用 Go 1.27.3 或更高版本构建：

```bash
go install github.com/lesismal/fit/cmd/fit@latest
```

也可以在源码目录中运行 `make build`，产物在 `bin/fit`。打了标签的版本还会发布容器镜像 `ghcr.io/lesismal/fit`
（linux/amd64 和 arm64）；镜像、Helm chart 和 systemd unit 见 [部署](../deploy/README.zh-CN.md)。`fit build` 可以构建
带有你自己模块的 fit（见 [模块](modules.zh-CN.md)）。

```bash
fit version
```

## 第一个配置

配置可以是 Caddyfile、YAML 或 JSON（[配置](configuration.zh-CN.md) 比较了三者）；本指南使用三者中最简短的 Caddyfile，
fit 读取时把它转换为自己的原生 JSON。把下面的内容保存为 `Caddyfile`：

```caddyfile
:8080 {
	respond /hello "hello from fit"
}
```

站点地址 `:8080` 说明 fit 在哪里监听：8080 端口上的 HTTP，任何 host。块中的指令说明由什么回答请求：`respond` 用固定的
响应体回答它的匹配器 `/hello` 接收的请求。先检查配置，再运行：

```bash
fit validate -c Caddyfile
fit run -c Caddyfile
```

```bash
curl localhost:8080/hello
```

fit 严格检查配置：不转换的指令会连同行号列出，转换得到的配置像其他配置一样受检查，取值错误就是错误，fit 不会启动。
`fit adapt -c Caddyfile` 打印它转换成的原生配置。没有指令接收的请求得到 200 和空响应体，与 Caddy 相同。

## 提供静态文件

在第一条指令下面加上要提供的目录：

```caddyfile
:8080 {
	respond /hello "hello from fit"
	root * ./public
	file_server
}
```

`root` 为所有请求（`*`）指明文件所在的目录，`file_server` 提供这些文件。以 `*` 结尾的路径匹配前缀，没有匹配器的指令
接收所有请求；多条指令都接收一个请求时，最具体的胜出：不论顺序，`/hello` 先于文件。`file_server` 为目录提供
`index.html`；`file_server browse` 会列出没有 index 的目录，`try_files {path} /index.html` 可以服务单页应用。

## 代理到应用

`reverse_proxy` 把请求发给一组后端，即一个 upstream：

```caddyfile
:8080 {
	reverse_proxy /api/* 127.0.0.1:9000 127.0.0.1:9001 {
		health_uri /healthz
		health_interval 5s
	}
	root * ./public
	file_server
}
```

发往 `/api/` 的请求轮流交给两个后端，健康检查失败的后端被移出，直到它再次通过。WebSocket 和 gRPC 也以同样方式通过。
每个连接、队列和超时都有安全的默认值，可以用 `reverse_proxy` 的子指令修改。

## HTTPS

用域名作站点地址：

```caddyfile
example.com {
	root * ./public
	file_server
}
```

fit 会在 443 上提供 HTTPS，证书由 Let's Encrypt 为 `example.com` 签发并自动续期；80 端口只用来跳转到 HTTPS。域名的 DNS
必须指向这台主机，且 80 和 443 端口可达。全局选项 `email` 和 `acme_ca` 设置 ACME 账号和 CA，`auto_https` 可以关闭这一
行为。要使用自己的证书，用 `tls` 给出：

```caddyfile
example.com {
	tls example.com.pem example.com.key
	root * ./public
	file_server
}
```

文件变化时 fit 会重新加载它们。

## 中间件

中间件在处理器之前运行。下面的配置压缩响应，并给每个响应加一个字段：

```caddyfile
example.com {
	encode
	header Strict-Transport-Security "max-age=31536000"
	root * ./public
	file_server
}
```

[Caddyfile 参考](reference/caddyfile.zh-CN.md#指令) 列出了可以转换的指令，[配置参考](reference/config.md#middleware)
列出了 fit 的全部中间件：重写和跳转、速率与并发限制、Basic 认证、JWT、转发认证、客户端证书、熔断器、缓存、WASM 插件。
没有 Caddyfile 指令的中间件用 YAML 或 JSON 编写，可以从 `fit adapt` 的输出开始。

## 运行中修改

`fit run` 收到 SIGHUP 时重新应用配置文件，加 `-watch` 时文件每次变化都会重新应用。无法加载的配置不会替换正在运行的配置，
并会说明原因。没有变化的 listener 保留其连接，进行中的请求按开始时的配置完成。

Caddyfile 默认开启 Admin API（在 127.0.0.1:2019），除非全局选项写了 `admin off`。它以 fit 的原生 JSON 读取和修改运行中的
配置：下面依次读取整个配置、按 JSON pointer 修改第一个路由的参数、加载一份用 `fit adapt` 从 Caddyfile 转换来的新配置，
再回滚到第一个版本：

```bash
curl localhost:2019/config
curl -X PATCH -H 'Content-Type: application/merge-patch+json' \
  -d '{"body": "hello again"}' localhost:2019/config/routes/0/handler/params
fit adapt -c Caddyfile -o fit.json
curl -X POST -H 'Content-Type: application/json' -d @fit.json localhost:2019/load
curl -X POST localhost:2019/rollback/1
```

它保留已应用的配置，供读取和回滚，并在 `/metrics` 提供指标、在 `/dashboard/` 提供仪表盘。

## 接下来

- [配置](configuration.zh-CN.md)：同一个配置写成 [Caddyfile](reference/caddyfile.zh-CN.md)、[YAML](reference/yaml.zh-CN.md)
  或 [JSON](reference/json.zh-CN.md)。
- [配置参考](reference/config.zh-CN.md)：每个字段及其类型和默认值，附示例。
- 从其他服务器迁移：[nginx](migrating-from-nginx.zh-CN.md)、[Caddy](migrating-from-caddy.zh-CN.md)、
  [Traefik](migrating-from-traefik.zh-CN.md)。
- [兼容性](compatibility.zh-CN.md)：在各 1.x 版本间保持稳定的内容。
- [部署](../deploy/README.zh-CN.md)：容器镜像、Kubernetes 和 systemd。
