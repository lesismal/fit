# 转换 Caddyfile

[English](caddyfile.md) | [简体中文](caddyfile.zh-CN.md)

为了帮助把站点从 Caddy 迁移过来，`fit adapt` 把 Caddy 的 Caddyfile 的常用子集转换为 fit 的配置，最好写成
[.konf 文件](fit.zh-CN.md)，此后保留并在其上修改。未转换的部分从不静默丢弃：每个被跳过的指令、选项或子指令都按行列出：

```bash
fit adapt -from caddyfile -c Caddyfile -to fit -o fit.konf
```

`-from caddyfile` 指明格式，与文件名无关。`fit run` 和 `fit validate` 只加载 fit 自己的格式，所以 Caddyfile 先转换一次，
保存转换结果。本页说明哪些内容可以转换、转换成什么；转换后的字段见 [配置参考](config.zh-CN.md)，同一站点在两者中的写法
见 [从 Caddy 迁移](../migrating-from-caddy.zh-CN.md)。

- [语法](#语法)
- [全局选项](#全局选项)
- [站点地址](#站点地址)
- [匹配器](#匹配器)
- [指令如何变成路由](#指令如何变成路由)
- [指令](#指令)
- [占位符](#占位符)
- [不转换的内容](#不转换的内容)

## 语法

```caddyfile
# 注释从 # 开始，到行尾结束。
{
	# 全局选项，单独一个块，放在最前面。
	email ops@example.com
}

(common) {
	# 一个片段（snippet），在下面导入；{args[0]} 是 import 的第一个参数。
	header X-Served-By {args[0]}
}

example.com, www.example.com {
	import common edge-1
	respond "hello"
}
```

- **Token** 以空格分隔；一行是一条指令和它的参数。`"双引号"` 可包含空格，`\"` 表示引号；`` `反引号` `` 原样包含任何内容；
  heredoc 从 `<<END` 到只含 `END` 的一行，包含其间各行，并去掉结束行的缩进。行尾的 `\` 把这一行接到下一行。
- **注释**：位于 token 开头的 `#`，直到行尾。引号内的 `#` 是普通文本。
- **块**以行尾的 `{` 开始，以 `}` 结束。第一个块如果没有地址，就是全局选项。
- **环境变量**：读取文件时，`{$NAME}` 替换为环境变量的值，`{$NAME:default}` 在变量未设置时取默认值。
- **片段**：顶层的 `(name) { ... }` 定义一个片段，`import name args...` 把它的各行放到所在位置，并替换 `{args[0]}`、
  `{args[1]}` 和 `{args[:]}`（全部参数）。导入文件不转换。
- **只有一个站点**时可以省略大括号：第一行是地址，后面是它的指令。

## 全局选项

| 选项 | 转换为 |
| --- | --- |
| `email <address>` | `acme.email` |
| `acme_ca <url>` | `acme.directory` |
| `acme_ca_root <file>` | `acme.ca_file` |
| `storage file_system <path>` 或 `{ root <path> }` | `acme.storage` |
| `admin off` | 不启用 Admin API |
| `admin <address>` | `admin.address` |
| `auto_https off` | `auto_https.disable` |
| `auto_https disable_redirects` | `auto_https.disable_redirects` |
| `auto_https disable_certs` | 不把域名交给 ACME |
| `http_port <port>`、`https_port <port>` | `auto_https.http_port`、`auto_https.https_port`，以及站点的默认端口 |
| `on_demand_tls { ask <url>; interval <d>; burst <n> }` | `acme.on_demand` 的 `ask`、`interval` 和 `rate`；也支持 `permission http { endpoint <url> }` |
| `servers { protocols h1 h2 h2c h3 }` | 每个 listener 的 `protocols`，按其是否有 TLS 取可用的部分 |

与 Caddy 一样，除非写了 `admin off`，Admin API 是开启的。日志、超时、PKI、事件等选项会被列为未转换：由 fit 的默认值代替。

## 站点地址

站点的地址写在第一行，以空格或逗号分隔：`[scheme://][host][:port][/path]`。

| 地址 | 提供方式 |
| --- | --- |
| `example.com`、`*.example.com` | 443 上的 HTTPS，证书来自 ACME；80 上的 HTTP 跳转过去 |
| `example.com:8443` | 8443 上的 HTTPS |
| `http://example.com` | 80 上的 HTTP |
| `https://example.com` | 443 上的 HTTPS |
| `:8080` | 8080 上的 HTTP，任意 host |
| `localhost`、`127.0.0.1`、`localhost:8080` | HTTP：Caddy 用内部 CA 以 HTTPS 提供，fit 没有内部 CA（会给出警告） |
| `example.com/api/*` | 站点的路由同时匹配该路径 |

host 成为路由的 `match.host`。当所有站点都只在 HTTPS 端口上提供时，fit 不写 listener，由
[默认 HTTPS](config.md#https-by-default) 生成，与 Caddy 一致。否则为每个地址和端口写一个 listener（`http_8080`、
`https_443`），把每个站点的路由放到它的 listener 上，并在 HTTP 端口上加一条路由把 HTTPS 站点的域名跳转过去，除非某个站点
自己以 HTTP 提供该域名。

站点内：

| 指令 | 转换为 |
| --- | --- |
| `tls <cert> <key>` | listener 的 `tls.certificates`；该站点不再向 ACME 申请 |
| `tls <email>` | `acme.email` |
| `tls internal` | 警告：fit 没有内部 CA |
| `tls { on_demand }` | listener 的 `tls.on_demand`；需要 `on_demand_tls` |
| `tls { client_auth { mode <m>; trusted_ca_cert_file <f> } }` | listener 的 `tls.client_auth`；也支持 `trust_pool file <f>`；`require_and_verify` 为 `require`，其他模式为 `verify_if_given` |
| `tls { issuer acme { email; dir } }`、`tls { ca <url> }` | `acme.email`、`acme.directory` |
| `tls { protocols; ciphers; curves; dns }` | 不转换：fit 自行选择 TLS 版本和密码套件，应答 HTTP-01 和 TLS-ALPN-01，不支持 DNS-01 |
| `bind <address>` | 站点 listener 绑定的地址 |
| `log { output stdout\|stderr\|file <path> }` | `access_log.output`；fit 只有一个访问日志，每行一个 JSON |

## 匹配器

指令的第一个参数可以是匹配器：`*`（所有请求）、以 `/` 开头的路径，或 `@name`，即站点内定义的命名匹配器。

```caddyfile
example.com {
	@api {
		path /api/*
		method GET POST
		header Authorization *
	}
	@static path *.css *.js /favicon.ico
	@notadmin not path /admin/*

	reverse_proxy @api 127.0.0.1:9000
	root * /srv/www
	file_server @static
	respond @notadmin "public"
}
```

路径可以是精确路径，或以 `*` 结尾的前缀，fit 的 `match.path` 原样接受；`*` 出现在其他位置（如 `*.css`）时转换为
`match.path_regexp` 中的正则表达式。命名匹配器的各行必须同时满足；同一行的多个值、同类的多行之间是"或"的关系。

| 匹配器 | 转换为 |
| --- | --- |
| `path <paths...>` | `match.path` 或 `match.path_regexp` |
| `path_regexp [name] <regexp>` | `match.path_regexp` |
| `host <names...>` | `match.host` |
| `method <methods...>` | `match.method` |
| `header <field> [<values...>]` | `match.header`；`*` 表示任意值；`!<field>` 表示没有该字段，写入 `match.expr` |
| `query <key>=<value>...` | `match.query`；`<key>=*` 表示任意值 |
| `remote_ip`、`client_ip <ranges...>` | `match.remote_ip`；`private_ranges` 表示私有网段 |
| `not <matcher>`、`not { ... }` | `match.expr` 中的 `!(...)` |
| `expression`、`file`、`protocol`、`vars`、`header_regexp` | 不转换；使用该匹配器的指令被跳过 |

## 指令如何变成路由

Caddy 按指令顺序为每个请求运行一串处理器；fit 为每个请求只挑一条路由，依次按优先级、最具体的 host 和 path 选择。
转换器生成：

1. **每个处理器指令一条路由**（`reverse_proxy`、`file_server`、`respond`、`php_fastcgi`、`error`），其匹配器写入
   `match`；每个 `handle`、`handle_path` 和 `route` 块生成它自己指令的路由，并加上块的匹配器；
2. **一条应答 200、无响应体的路由**，用于没有处理器接收的请求，与 Caddy 的应答一致；
3. **中间件放到它覆盖的路由上**：每个中间件指令（`header`、`encode`、`basic_auth`、`redir`、`rewrite` 等）按 Caddy 的
   指令顺序加到其匹配器覆盖的每条路由上，只覆盖一部分的路由会被拆成两条，被覆盖的部分得到该中间件。`root` 和
   `try_files` 以同样方式设置处理器的根目录和文件。

```caddyfile
example.com {
	basic_auth /admin/* {
		alice $2a$14$Zkx19XLiW6VYouLHR5NmfOFU0z2GTNmpkT/5qqR7hx4IjWJPDhjvG
	}
	root * /srv/www
	file_server
}
```

生成两条路由：`/admin/*` 带 `basic_auth` 和静态文件处理器；example.com 的其余请求只有静态文件处理器。同一个块中的
`root`、`try_files`、`rewrite`、`uri` 和 `redir`，对一个请求只有最具体的那条生效，与 Caddy 一致；`route` 块中的指令保持
书写顺序。

## 指令

| 指令 | 转换为 |
| --- | --- |
| `reverse_proxy [<matcher>] <upstreams...> { ... }` | [`proxy`](config.md#http-handlers-proxy) 处理器和一个 [upstream](config.md#upstreams) |
| `file_server [<matcher>] [browse] { ... }` | [`static`](config.md#http-handlers-static) 处理器 |
| `root [<matcher>] <path>` | `static` 和 `fastcgi` 处理器的 `root` |
| `try_files [<matcher>] <files...>` | 它们的 `try_files`；保留 `{path}`，最后的 `=404` 本就是 fit 的应答 |
| `respond [<matcher>] [<body>] [<status>] { body; close }` | [`respond`](config.md#http-handlers-respond) 处理器 |
| `error [<matcher>] [<message>] <status>` | 带该状态码的 `respond` |
| `php_fastcgi [<matcher>] <address> { ... }` | [`fastcgi`](config.md#http-handlers-fastcgi) 处理器 |
| `redir [<matcher>] <to> [<code>]` | [`redirect`](config.md#http-middleware-redirect) 中间件；`permanent` 为 301，`temporary` 为 302 |
| `rewrite [<matcher>] <uri>` | [`rewrite`](config.md#http-middleware-rewrite) 中间件：`/prefix{path}` 加前缀，路径则替换整个路径 |
| `uri [<matcher>] strip_prefix\|strip_suffix\|replace\|path_regexp ...` | `rewrite` 中间件 |
| `handle_path <path> { ... }` | 一个 `handle` 块，其路由用 `rewrite` 去掉路径前缀 |
| `handle [<matcher>] { ... }`、`route [<matcher>] { ... }` | 其中指令的路由 |
| `header [<matcher>] [+\|-\|?\|>]<field> [<value>]`，或其块形式 | [`headers`](config.md#http-middleware-headers) 中间件的 `response`：设置字段，`+` 追加，`-` 删除 |
| `request_header [<matcher>] ...` | `headers` 中间件的 `request` |
| `encode [<matcher>] [zstd] [gzip] [br] { minimum_length <n> }` | [`compress`](config.md#http-middleware-compress) 中间件；未指定时为 zstd 和 gzip |
| `basic_auth`、`basicauth [<matcher>] [bcrypt [<realm>]] { <user> <hash> }` | [`basic_auth`](config.md#http-middleware-basic_auth) 中间件；也支持旧版 Caddy 的 base64 哈希 |
| `forward_auth [<matcher>] <upstreams...> { uri; copy_headers }` | [`forward_auth`](config.md#http-middleware-forward_auth) 中间件和一个 upstream |
| `request_body [<matcher>] { max_size <size> }` | [`body_limit`](config.md#http-middleware-body_limit) 中间件；`10MB` 为 10,000,000 字节，`10MiB` 为 10,485,760 |
| `metrics` | `metrics`，由 Admin API 在 `/metrics` 提供 |

### `reverse_proxy`

upstream 地址：`host:port`、`:port`（localhost）、`http://`、`https://`（TLS，服务器支持时用 HTTP/2）、`h2c://`，以及
`10.0.0.1:8001-8004` 这样的端口范围。地址和选项相同的代理共用一个 upstream，以第一个地址命名（`proxy_10_0_0_1_8080`）。

| 子指令 | 转换为 |
| --- | --- |
| `to <upstreams...>` | 更多地址 |
| `lb_policy round_robin\|least_conn\|random\|ip_hash\|client_ip_hash\|uri_hash` | `load_balancing` |
| `lb_policy random_choose` | `p2c` |
| `lb_policy header <field>` | `header_hash` 加 `hash_key` |
| `lb_policy cookie [<name>]` | `sticky_cookie` |
| `lb_policy weighted_round_robin <weights...>` | `weights` |
| `lb_retries <n>` | 代理的 `retry.attempts` |
| `lb_try_duration` | 代理的 `retry`，取默认值 |
| `health_uri`、`health_path`、`health_port`、`health_interval`、`health_timeout`、`health_status <code>`、`health_passes`、`health_fails`、`health_headers { Host }` | `health_check` |
| `fail_duration`、`max_fails` | `fail_timeout`、`max_fails` |
| `header_up Host <value>` | 代理的 `host_header`；`{upstream_hostport}` 为唯一的那个地址 |
| `header_up <field> <value>`、`header_up -<field>` | `headers` 中间件的 `request` |
| `header_down ...` | `headers` 中间件的 `response` |
| `trusted_proxies <ranges...>` | 代理的 `trusted_proxies` |
| `stream_timeout` | 代理的 `tunnel_idle_timeout` |
| `transport http { versions h2c }`、`{ tls }` | `protocol` 为 `h2c`、`h2` |
| `transport http { tls_server_name; tls_trusted_ca_certs; tls_trust_pool file; tls_client_auth }` | `tls_server_name`、`tls_ca_file`、`tls_cert_file` 和 `tls_key_file` |
| `transport http { dial_timeout; response_header_timeout; read_timeout }` | `connect_timeout`、`response_header_timeout`、`response_idle_timeout` |
| `transport http { keepalive <d>\|off; keepalive_idle_conns; max_conns_per_host }` | `idle_timeout`、`max_idle_conns`、`max_conns` |
| `flush_interval`、`buffer_requests` 等 | 无需转换：fit 双向流式转发 |

`header_up X-Forwarded-*` 和 `X-Real-IP {remote_host}` 被跳过：fit 的代理自己设置 X-Forwarded-For、-Proto 和 -Host。

### `file_server`

| 子指令 | 转换为 |
| --- | --- |
| `root <path>` | `root` |
| `browse` | `browse` |
| `index <files...>` | `index` |
| `precompressed [br] [zstd] [gzip]` | `precompressed`；未指定时为全部三种 |

没有 `root` 时，Caddy 提供它运行所在的目录；转换器写入 `"."` 并给出提示。

### `php_fastcgi`

| 子指令 | 转换为 |
| --- | --- |
| `root <path>` | `root`；否则取站点的 `root` |
| `split <exts...>` | `split_path` |
| `index <file>`、`index off` | `index`，以及相应的 `try_files`；不使用 |
| `try_files <files...>` | `try_files` |
| `env <key> <value>` | `env` |
| `dial_timeout`、`read_timeout` | `connect_timeout`、`response_idle_timeout` |

与 Caddy 一样，不是脚本本身的路径会运行 `index.php`。当同一批请求还有 `file_server` 时，PHP 接收 `.php` 脚本和目录的路径，
其余交给文件：两者都不是的路径应答 404，而不会转给 `index.php`。

## 占位符

fit 只展开少数占位符。fit 有对应物的，Caddy 的占位符会被转换：`redir` 的 URL 中的 `{uri}`、`{path}`、`{query}`、
`{host}`、`{scheme}` 及其 `{http.request.*}` 形式；`rewrite` 和 `try_files` 中的 `{path}`。含其他占位符的指令被跳过，
只有 `respond` 例外：它的响应体按原文发送，并给出警告。

## 不转换的内容

出现时都会按行列出：

- Caddy 的内部 CA（`tls internal`、`local_certs`）和 DNS 验证。
- CEL 表达式，以及 `file`、`protocol`、`vars` 和 `header_regexp` 匹配器。
- `templates`、`map`、`vars`、`handle_errors`、`abort`、`intercept`、`push`、`tracing`、`invoke` 以及插件的指令。
- 导入文件：只支持 Caddyfile 内定义的片段。
- `rewrite` 和 `uri` 在 fit 选定路由之后生效：改写后的路径不会像在 Caddy 中那样选择另一条路由。
- 四层代理、缓存、限流、JWT 和 WASM 在这里没有 Caddyfile 指令：把它们加到 `fit adapt` 写出的 .konf 文件中。
