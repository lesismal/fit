# 快速上手

[English](getting-started.md) | [简体中文](getting-started.zh-CN.md)

本指南带你从零开始，用 fit 搭起一个通过 HTTPS 提供服务的站点：静态文件、被代理的应用，以及在运行中修改配置。每一步都在
同一个配置文件上添加内容；每个字段见 [配置参考](reference/config.zh-CN.md)。

## 安装

用 Go 1.27 或更高版本构建：

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

fit 的配置是 JSON，也可以用字段相同的 YAML。把下面的内容保存为 `fit.json`：

```json
{
  "listeners": [{"name": "web", "address": ":8080", "protocols": ["http/1.1", "h2c"]}],
  "routes": [
    {"match": {"path": ["/hello"]}, "handler": {"module": "respond", "params": {"body": "hello from fit\n"}}}
  ]
}
```

listener 说明 fit 在哪里监听、说什么协议。route 说明它接收哪些请求（`match`）、请求经过什么（`middleware`）、由什么回答
（`handler`），每一个都是带 `params` 的模块。先检查配置，再运行：

```bash
fit validate -c fit.json
fit run -c fit.json
```

```bash
curl localhost:8080/hello
```

fit 严格检查配置：未知字段、类型错误或缺少必填项都是错误，会给出行号和列号，fit 不会启动。没有路由匹配的请求得到 404。

## 提供静态文件

在第一个路由之后加一个提供目录的路由：

```json
{"match": {"path": ["/*"]}, "handler": {"module": "static", "params": {"root": "./public"}}}
```

以 `*` 结尾的路径匹配前缀。多个路由都匹配时，最具体的胜出：不论顺序，`/hello` 先于 `/*`。`static` 为目录提供
`index.html`；`browse` 会列出没有 index 的目录，`try_files` 可以服务单页应用。

## 代理到应用

upstream 是一组按名字引用的后端；`proxy` 处理器把请求发给其中之一：

```json
{
  "listeners": [{"name": "web", "address": ":8080", "protocols": ["http/1.1", "h2c"]}],
  "upstreams": [
    {"name": "app", "addresses": ["127.0.0.1:9000", "127.0.0.1:9001"],
     "health_check": {"path": "/healthz", "interval": "5s"}}
  ],
  "routes": [
    {"match": {"path": ["/api/*"]}, "handler": {"module": "proxy", "params": {"upstream": "app"}}},
    {"match": {"path": ["/*"]}, "handler": {"module": "static", "params": {"root": "./public"}}}
  ]
}
```

发往 `/api/` 的请求轮流交给两个后端，健康检查失败的后端被移出，直到它再次通过。WebSocket 和 gRPC 也以同样方式通过。
每个连接、队列和超时都有安全的默认值，可以用 upstream 的字段修改。

## HTTPS

给路由写上域名，并且不写 listener：

```json
{
  "routes": [
    {"match": {"host": ["example.com"], "path": ["/*"]},
     "handler": {"module": "static", "params": {"root": "./public"}}}
  ]
}
```

fit 会在 443 上提供 HTTPS，证书由 Let's Encrypt 为 `example.com` 签发并自动续期；80 端口只用来跳转到 HTTPS。域名的 DNS
必须指向这台主机，且 80 和 443 端口可达。`acme` 设置 CA 和证书的存放位置，`auto_https` 设置端口或关闭这一行为。要使用
自己的证书，就写出 listener：

```json
{"name": "web", "address": ":443", "protocols": ["http/1.1", "h2", "h3"],
 "tls": {"certificates": [{"cert_file": "example.com.pem", "key_file": "example.com.key"}]}}
```

文件变化时 fit 会重新加载它们。

## 中间件

中间件在处理器之前按顺序运行。下面的配置压缩响应，并给每个响应加一个字段：

```json
{"match": {"path": ["/*"]},
 "middleware": [
   {"module": "compress"},
   {"module": "headers", "params": {"response": {"set": {"Strict-Transport-Security": "max-age=31536000"}}}}
 ],
 "handler": {"module": "static", "params": {"root": "./public"}}}
```

[配置参考](reference/config.md#middleware) 列出了全部中间件：重写和跳转、速率与并发限制、Basic 认证、JWT、转发认证、
客户端证书、熔断器、缓存、WASM 插件。

## 运行中修改

`fit run` 收到 SIGHUP 时重新应用配置文件，加 `-watch` 时文件每次变化都会重新应用。无法加载的配置不会替换正在运行的配置，
并会说明原因。没有变化的 listener 保留其连接，进行中的请求按开始时的配置完成。

配置中写上 `"admin": {}`，Admin API（默认在 127.0.0.1:2019）就可以读取和修改运行中的配置：下面依次读取整个配置、按 JSON
pointer 修改第一个路由的参数、加载一份新配置，再回滚到第一个版本：

```bash
curl localhost:2019/config
curl -X PATCH -H 'Content-Type: application/merge-patch+json' \
  -d '{"body": "hello again\n"}' localhost:2019/config/routes/0/handler/params
curl -X POST -H 'Content-Type: application/json' -d @fit.json localhost:2019/load
curl -X POST localhost:2019/rollback/1
```

它保留已应用的配置，供读取和回滚，并在 `/metrics` 提供指标、在 `/dashboard/` 提供仪表盘。

## 接下来

- [配置参考](reference/config.zh-CN.md)：每个字段及其类型和默认值。
- 从其他服务器迁移：[nginx](migrating-from-nginx.zh-CN.md)、[Caddy](migrating-from-caddy.zh-CN.md)、
  [Traefik](migrating-from-traefik.zh-CN.md)。
- [兼容性](compatibility.zh-CN.md)：在各 1.x 版本间保持稳定的内容。
- [部署](../deploy/README.zh-CN.md)：容器镜像、Kubernetes 和 systemd。
