# 从 Traefik 迁移

[English](migrating-from-traefik.md) | [简体中文](migrating-from-traefik.zh-CN.md)

fit 借鉴了 Traefik 的 provider（把 Docker 标签、Consul 服务目录、KV 存储和 Kubernetes 资源变成路由）和中间件，并增加了
静态文件服务、缓存，以及更小的每连接开销。没有转换器；本指南给出 Traefik 各部分与 fit 的对应，每个字段见
[配置参考](reference/config.zh-CN.md)。

## 各部分的对应

| Traefik | fit |
| --- | --- |
| entryPoints | listener：`listener web { address :80; protocols http/1.1 h2c }` |
| entryPoint 的 `http.tls` | listener 的 `tls` |
| HTTP router | 路由；`entryPoints` 即路由的 `listeners` |
| router 的 `rule` | 路由的 `match.expr`，或 `match` 的各字段（见下文） |
| router 的 `priority` | 路由的 `priority` |
| service 与 `loadBalancer.servers` | upstream 及其 `addresses`，配合 `proxy` 处理器 |
| `loadBalancer.healthCheck` | upstream 的 `health_check` |
| `loadBalancer.sticky.cookie` | upstream 的 `sticky_cookie` |
| 加权 service | proxy 的 `upstreams`，按 `weight` |
| 镜像 service | proxy 的 `mirror` |
| 故障转移 service | proxy 的 `failover` |
| serversTransport（`rootCAs`、`serverName`、客户端证书） | upstream 的 `protocol` `h2`，以及 `tls_ca_file`、`tls_server_name`、`tls_cert_file` |
| 带 `HostSNI` 的 TCP router | `tcp` listener 和带 `server_names` 的 service，透传 TLS |
| UDP router | `udp` listener 和 service |
| certificatesResolvers（ACME） | `acme`，支持 HTTP-01 和 TLS-ALPN-01，域名写在 listener 的 `tls.acme`；带域名的路由默认使用 HTTPS |
| `tls.certificates` | listener 的 `tls.certificates` |
| 带 `clientAuth` 的 `tls.options` | listener 的 `tls.client_auth` |
| `accessLog` | `access_log` |
| `metrics.prometheus` | `metrics`，在 Admin API 的 `/metrics` |
| `tracing`（OpenTelemetry） | `telemetry` |
| `api.dashboard` | Admin API 的只读仪表盘 `/dashboard/` |

## 规则

fit 的 `match.expr` 对共有的匹配器采用 Traefik 的语法，包括反引号：

```fit
route api {
	match {
		expr "Host(`example.com`) && (PathPrefix(`/api`) || Method(`POST`))"
	}
	handler proxy { upstream app }
}
```

它的匹配器有 `Host`、`Path`、`PathPrefix`、`PathRegexp`、`Method`、`Header(name, value...)`、`Query(key, value...)`
和 `RemoteIP`（即 Traefik 的 `ClientIP`）。没有 `HostRegexp` 和 `HeaderRegexp`：以 `*.`（一个标签）或 `**.`（一个或多个）
开头的 host 能覆盖大多数用法。

## 中间件

| Traefik | fit |
| --- | --- |
| `StripPrefix`、`AddPrefix`、`ReplacePathRegex` | `rewrite`：`strip_prefix`、`add_prefix`、`regexp` 与 `replacement` |
| `RedirectScheme`、`RedirectRegex` | `redirect` |
| `Headers` | `headers`（CORS 字段直接作为字段设置） |
| `BasicAuth` | `basic_auth` |
| `ForwardAuth` | `forward_auth` |
| `RateLimit` | `rate_limit` |
| `InFlightReq` | `concurrency_limit` |
| 带 `maxRequestBodyBytes` 的 `Buffering` | `body_limit` |
| `CircuitBreaker` | `circuit_breaker` |
| `Compress` | `compress` |
| `Retry` | proxy 的 `retry` |
| `PassTLSClientCert` | `client_cert` |
| `IPAllowList` | 路由的 `match.remote_ip`，再加一个回答 403 的路由 |
| 插件 | WASM 插件（`wasm`），或用 `fit build` 编入的模块 |

## Provider

| Traefik | fit 的 `providers` |
| --- | --- |
| file provider | 配置文件本身，用 `fit run -watch` 在变化时重新应用 |
| Docker | `docker`，标签以 `fit.` 开头（见下文） |
| Consul Catalog | `consul`，取自服务的 tag |
| Consul KV、etcd | `consul_kv`、`etcd`：键中存放 listener、路由、upstream 和 service 的 JSON 对象 |
| Kubernetes Gateway API | `kubernetes`，服务其 GatewayClass 下的 Gateway 及其 HTTPRoute、GRPCRoute、TLSRoute、TCPRoute 和 UDPRoute |
| Kubernetes Ingress | `kubernetes` 的 `ingress`，用于 fit 的 IngressClass |
| Kubernetes CRD（IngressRoute） | 不支持：请改用 Gateway API |

配置文件中写的内容优先于所有 provider，列在前面的 provider 优先于后面的；Admin API 的 `/config/effective` 展示合并结果及
每部分的来源。

Docker 标签很相似，前缀是 `fit.` 而不是 `traefik.`：

| Traefik | fit |
| --- | --- |
| `traefik.enable=true` | `fit.enable=true` |
| `traefik.http.routers.web.rule=Host(...)` | `fit.routes.web.host=a.example`、`fit.routes.web.path=/api/*`，或 `fit.routes.web.expr=Host(...)` |
| `traefik.http.services.app.loadbalancer.server.port=8080` | `fit.port=8080` |
| `traefik.http.routers.web.middlewares=...` | `fit.routes.web.middleware=[{"module": "compress"}]`，JSON 格式 |
| `traefik.docker.network=net` | `fit.network=net` |

## 行为上的不同

- **一份配置。** Traefik 把静态配置（entryPoints、providers）和动态配置（router、service、middleware）分开。fit 只有一份
  配置；listener 可以在 reload 时改变，`providers`、`admin` 和 `acme` 在 fit 重启后生效。
- **路由按具体程度选取**：先看 `priority`，再看最具体的 host 和 path，而不是规则的长度。
- **没有的功能**：DNS 质询，因而也无法通过 ACME 获取通配符证书；Traefik 自己的 CRD；到上游的 HTTP/3；Traefik Hub。
