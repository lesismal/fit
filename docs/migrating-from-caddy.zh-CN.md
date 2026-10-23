# 从 Caddy 迁移

[English](migrating-from-caddy.md) | [简体中文](migrating-from-caddy.zh-CN.md)

fit 与 Caddy 理念相同：默认 HTTPS、严格的结构化配置，以及原子替换配置的 Admin API。不同在于内置了什么（缓存、限流、
四层代理，Caddy 把它们留给插件），以及每个连接的开销更小。没有从 Caddyfile 或 Caddy JSON 的转换器；本指南给出两者的
对应，每个字段见 [配置参考](reference/config.zh-CN.md)。

## 一个站点

```caddyfile
example.com {
	encode zstd gzip
	handle_path /api/* {
		reverse_proxy 127.0.0.1:9000 127.0.0.1:9001 {
			lb_policy least_conn
			health_uri /healthz
		}
	}
	root * /srv/www
	file_server
}
```

在 fit 中是：

```json
{
  "upstreams": [
    {"name": "app", "addresses": ["127.0.0.1:9000", "127.0.0.1:9001"], "load_balancing": "least_conn",
     "health_check": {"path": "/healthz"}}
  ],
  "routes": [
    {"match": {"host": ["example.com"], "path": ["/api/*"]},
     "middleware": [{"module": "compress"}, {"module": "rewrite", "params": {"strip_prefix": ["/api"]}}],
     "handler": {"module": "proxy", "params": {"upstream": "app"}}},
    {"match": {"host": ["example.com"], "path": ["/*"]},
     "middleware": [{"module": "compress"}],
     "handler": {"module": "static", "params": {"root": "/srv/www"}}}
  ]
}
```

和 Caddy 一样，不写 listener 的配置会在 443 上提供 HTTPS，证书由 Let's Encrypt 为路由的域名签发，80 端口跳转过去。

## 各部分的对应

| Caddy | fit |
| --- | --- |
| 站点地址 `example.com` | 路由的 `match.host`；listener 由默认 HTTPS 生成，或手写 |
| `:8080` | 一个 listener：`{"name": "web", "address": ":8080", "protocols": ["http/1.1", "h2c"]}` |
| 匹配器 `path`、`host`、`method`、`header`、`query`、`remote_ip`、`path_regexp`、`expression` | `match` 中同名的字段，以及 `expr` |
| `handle`、`route` 及其顺序 | 路由，按 `priority`、再按最具体的 host 和 path 选取 |
| `handle_path /api/*` | `match.path` 加上 `rewrite` 中间件的 `strip_prefix` |
| `reverse_proxy` | `proxy` 处理器和一个 upstream |
| `lb_policy` | upstream 的 `load_balancing`：`round_robin`、`weighted_round_robin`、`least_conn`、`random`、`p2c`、`ip_hash`、`uri_hash`、`header_hash`、`cookie_hash` |
| `health_uri`、`health_interval`、`fail_duration`、`max_fails` | `health_check`、`fail_timeout`、`max_fails` |
| `transport http { versions h2c }` | upstream 的 `protocol`：`h2c`，或基于 TLS 的 `h2` |
| `file_server`、`root`、`browse`、`try_files`、`precompressed` | `static` 处理器的 `root`、`browse`、`try_files`、`precompressed` |
| `encode` | `compress` 中间件 |
| `header`、`request_header` | `headers` 中间件 |
| `redir` | `redirect` 中间件 |
| `rewrite`、`uri strip_prefix` | `rewrite` 中间件 |
| `respond` | `respond` 处理器 |
| `basicauth` | `basic_auth` 中间件，和 Caddy 一样使用 bcrypt 哈希 |
| `forward_auth` | `forward_auth` 中间件 |
| `php_fastcgi` | `fastcgi` 处理器，带 `split_path`、`index` 和 `try_files` |
| `tls cert.pem key.pem` | listener 的 `tls.certificates` |
| `tls { on_demand }` 与 `on_demand_tls { ask ... }` | listener 的 `tls.on_demand` 与 `acme.on_demand` |
| `email`、`acme_ca`、`storage`（Redis、Consul） | `acme` 的 `email`、`directory`、`storage`，以及用于 Redis、etcd 或 Consul 的 `shared_storage` |
| `log` | `access_log` |
| `metrics` | `metrics`，由 Admin API 在 `/metrics` 提供 |
| `tracing` | `telemetry`，通过 OTLP |
| 插件：限流、缓存、四层 | 内置：`rate_limit`、`cache`、带 service 的 `tcp` 与 `udp` listener |
| `xcaddy build --with ...` | `fit build --with ...`（见 [模块](modules.zh-CN.md)） |
| `caddy run --watch`、`caddy reload` | `fit run -watch`、SIGHUP 或 Admin API 的 `POST /load` |
| `caddy validate`、`caddy hash-password` | `fit validate`、`fit hash-password` |

## Admin API

fit 的 Admin API 在形式上与 Caddy 相同：对 `/config/` 及其下路径的 `GET`、`PUT`、`PATCH`、`DELETE`，`POST /load`，
以及防止丢失更新的 ETag 与 `If-Match`。区别在于：

- 路径是 JSON pointer 的各段（`/config/routes/0/handler`），没有 `@id` 快捷方式。
- `PATCH` 是 JSON merge patch（RFC 7396）；没有 Caddy 用 `POST` 追加数组元素的做法：请 `PUT` 整个数组。
- fit 保留已应用的配置（`GET /versions`），`POST /rollback/{n}` 可以再次应用其中一份。
- 它同样监听 127.0.0.1:2019，监听其他地址时需要 bearer token（`admin.token_file`）。

## 行为上的不同

- **配置的结构。** fit 的配置顶层是 listener、路由、upstream 和 service，而不是 app 和 server；一个路由是一串扁平的中间件
  加一个处理器，而不是嵌套的 handler。
- **没有的功能**：Caddy 的内部 CA（`tls internal`）和 PKI app、DNS 质询（fit 支持 HTTP-01 和 TLS-ALPN-01）、`templates`
  和 `map` 指令，以及 Caddy 的事件。用 `fit build` 编入的模块或 WASM 插件取代插件。
