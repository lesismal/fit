# 从 nginx 迁移

[English](migrating-from-nginx.md) | [简体中文](migrating-from-nginx.zh-CN.md)

fit 保留了 nginx 擅长的东西——事件循环和每个连接很小的开销——改变的是配置方式：一份结构化的配置，运行前先检查，通过
Admin API 原地修改而无需 reload。它自己的格式 [.conf 文件](reference/fit.zh-CN.md) 像 nginx 的配置一样由指令块组成，但
指令就是 fit 自己的字段；一份配置可以像 nginx 用 `include` 那样分散在多个文件和目录中。本指南把 nginx 的指令对应到 fit 的配置；每个字段见 [配置参考](reference/config.zh-CN.md)。

## 先转换

`fit adapt` 可以转换 nginx 配置的常用子集，并列出它略过的指令及其行号：

```bash
fit adapt -from nginx -c nginx.conf -to fit -o fit.conf
fit validate -c fit.conf
```

它转换 `http` 块中的 server（`listen`、`server_name`、`ssl_certificate`）、其中的 location（精确、前缀、`^~` 和正则，
可嵌套），以及其中的 `proxy_pass` 和 `upstream` 块、`root`、`alias`、`index`、`try_files`、`return`、`rewrite`、
`limit_req` 与 `limit_req_zone`、`client_max_body_size`、`add_header`、取字面值的 `proxy_set_header`，以及 `gzip`。
请阅读它报告的内容，再参照下面的表格手工补全配置。

## 各部分的对应

| nginx | fit |
| --- | --- |
| `server { listen 8080; }` | 一个 listener：`listener web { address :8080; protocols http/1.1 h2c }` |
| 带 `ssl_certificate` 和 `ssl_certificate_key` 的 `listen 443 ssl http2;` | `protocols http/1.1 h2` 且带 `tls { certificates { cert_file ...; key_file ... } }` 的 listener；加上 `h3` 即启用 HTTP/3 |
| `server_name a.example *.example;` | `site a.example *.example { ... }`，或路由的 `match.host`；`*.` 代表一个标签，`**.` 代表一个或多个 |
| `location = /x` | `match.path: ["/x"]` |
| `location /x/` | `match.path: ["/x/*"]` |
| `location ~ \.php$` | `match.path_regexp: ["\\.php$"]` |
| `proxy_pass http://app;` 与 `upstream app { server ...; }` | `handler proxy { upstream app }`，以及 `upstream app { addresses ... }` |
| `least_conn;`、`ip_hash;`、`hash $cookie_x;` | upstream 的 `load_balancing`：`least_conn`、`ip_hash`、带 `hash_key` 的 `cookie_hash` |
| `server ... weight=3 max_fails=2 fail_timeout=10s;` | `weights`、`max_fails`、`fail_timeout` |
| `server ... backup;` | proxy 的 `failover`：另一个 upstream，在第一个没有可用后端时使用 |
| `proxy_connect_timeout`、`proxy_read_timeout` | `connect_timeout`、`response_header_timeout` 和 `response_idle_timeout` |
| `keepalive 32;` | `max_idle_conns`（默认 32） |
| `proxy_set_header`、`add_header` | `headers` 中间件的 `request` 和 `response` |
| `root`、`alias`、`index`、`try_files`、`autoindex on;` | `static` 处理器的 `root`、`strip_prefix`、`index`、`try_files`、`browse` |
| `return 301 https://$host$request_uri;` | `redirect` 中间件：`{"status": 301, "to": "https://{host}{uri}"}` |
| `rewrite ^/old/(.*)$ /new/$1;` | `rewrite` 中间件的 `regexp` 和 `replacement` |
| `return 200 "ok";` | `respond` 处理器 |
| `gzip on;` | `compress` 中间件，还支持 zstd 和 brotli |
| `limit_req_zone` 与 `limit_req` | `rate_limit` 中间件：`rate`、`burst`、`key` |
| `limit_conn` | `concurrency_limit` 中间件 |
| `client_max_body_size 10m;` | `body_limit` 中间件，或对所有 listener 生效的 `limits.max_body_bytes` |
| `auth_basic` 与 `auth_basic_user_file` | `basic_auth` 中间件，使用 bcrypt 哈希（`fit hash-password`） |
| `auth_request` | `forward_auth` 中间件 |
| `proxy_cache_path` 与 `proxy_cache` | `cache` 中间件：`zone`、`max_bytes`，以及磁盘层 `disk` |
| 带 `fastcgi_param` 的 `fastcgi_pass` | `fastcgi` 处理器，PHP 的参数已为你设好 |
| `stream { server { listen 5432; proxy_pass db; } }` | 一个 `tcp`（或 `udp`）listener，以及指向它和某个 upstream 的 service |
| `ssl_preread` 与 `map $ssl_preread_server_name` | service 的 `server_names`，按 SNI 透传 TLS |
| `access_log` | `access_log`，JSON 格式 |
| `nginx -s reload` | SIGHUP、`fit run -watch`，或 Admin API 的 `POST /load` |

## 行为上的不同

- **路由如何匹配。** nginx 先试精确 location，再找最长前缀，然后按顺序试正则。fit 先看 `priority`，再看最具体的 host，然后
  是 path，再然后是设置了最多其他匹配器的路由；正则和其他匹配器一样只是匹配器之一。要让某个路由优先，给它设 `priority`。
- **没有变量。** fit 没有 `$变量` 和 `if`。匹配用 `match`（host、path、方法、请求头、查询参数、远端地址，或组合它们的
  `expr`）；跳转和重写使用各自的占位符（`{host}`、`{uri}`、`$1`）。
- **默认 HTTPS。** 没有 listener、路由带域名的配置会在 443 上以 Let's Encrypt 的证书提供 HTTPS，80 上的 HTTP 跳转过去。
  写出 listener，或设置 `auto_https.disable`，即可保持 nginx 的行为。
- **健康检查与可变的 upstream。** 内置主动健康检查（`health_check`），upstream 还可以来自 Docker、Consul、etcd 或
  Kubernetes（`providers`）。
- **上限默认开启。** 连接数、请求头和请求体大小、待发送字节数以及每个 upstream 的超时都有安全的默认值；用 `limits` 和
  upstream 的字段修改。
- **不转换**：Lua、`njs`、`map`、`geo`、`split_clients`、`mirror`（fit 的 proxy 有自己的 `mirror`）、`sub_filter`、
  邮件代理以及 nginx 的各种模块。WebAssembly 插件（`wasm`）和用 `fit build` 编入的模块取代这些扩展。
