# Converting a Caddyfile

[English](caddyfile.md) | [简体中文](caddyfile.zh-CN.md)

To help move a site from Caddy, `fit adapt` converts a common subset of Caddy's Caddyfile to fit's
config, best written out as a [.conf file](fit.md) to keep and edit from then on. What is not
converted is never dropped silently: each directive, option or subdirective left out is listed
with its line:

```bash
fit adapt -from caddyfile -c Caddyfile -to fit -o fit.conf
```

`-from caddyfile` names the format, whatever the file is called. `fit run` and `fit validate` load
only fit's own formats, so a Caddyfile is converted once and the result kept. This page says
what converts and to what; the fields it converts to are in the [config reference](config.md),
and [migrating from Caddy](../migrating-from-caddy.md) shows a site in both.

- [Syntax](#syntax)
- [Global options](#global-options)
- [Site addresses](#site-addresses)
- [Matchers](#matchers)
- [How directives become routes](#how-directives-become-routes)
- [Directives](#directives)
- [Placeholders](#placeholders)
- [Not converted](#not-converted)

## Syntax

```caddyfile
# A comment runs from # to the end of its line.
{
	# Global options, in a block of their own, first.
	email ops@example.com
}

(common) {
	# A snippet, imported below; {args[0]} is the first argument of the import.
	header X-Served-By {args[0]}
}

example.com, www.example.com {
	import common edge-1
	respond "hello"
}
```

- **Tokens** are separated by spaces; a line holds one directive and its arguments. `"double
  quotes"` hold spaces, with `\"` for a quote; `` `backquotes` `` hold anything as it is; a
  heredoc, `<<END` to a line holding `END`, holds lines, the closing line's indentation taken off
  each. A `\` at the end of a line continues it on the next.
- **Comments**: `#` at the start of a token, to the end of the line. Inside quotes it is text.
- **Blocks** open with `{` at the end of a line and close with `}`. The first block, if it has no
  address, is the global options.
- **Environment**: `{$NAME}` is replaced with the environment variable's value when the file is
  read, `{$NAME:default}` with a default when it is unset.
- **Snippets**: `(name) { ... }` at the top defines one, and `import name args...` puts its lines
  where it is, with `{args[0]}`, `{args[1]}` and `{args[:]}` (all of them) replaced. Importing
  files is not converted.
- **One site** may leave out its braces: the address on the first line, its directives after it.

## Global options

| Option | Converts to |
| --- | --- |
| `email <address>` | `acme.email` |
| `acme_ca <url>` | `acme.directory` |
| `acme_ca_root <file>` | `acme.ca_file` |
| `storage file_system <path>`, or `{ root <path> }` | `acme.storage` |
| `admin off` | no Admin API |
| `admin <address>` | `admin.address` |
| `auto_https off` | `auto_https.disable` |
| `auto_https disable_redirects` | `auto_https.disable_redirects` |
| `auto_https disable_certs` | no names given to ACME |
| `http_port <port>`, `https_port <port>` | `auto_https.http_port`, `auto_https.https_port`, and the ports sites default to |
| `on_demand_tls { ask <url>; interval <d>; burst <n> }` | `acme.on_demand`'s `ask`, `interval` and `rate`; `permission http { endpoint <url> }` too |
| `servers { protocols h1 h2 h2c h3 }` | each listener's `protocols`, those its TLS allows |
| `debug` | `log.level` `debug` |
| `log [default] { output; format; level }` | `log`: `output` (stdout, stderr or `file <path>`; stderr when left out, as Caddy's), `format` (`console` is `text`, `json`), `level` (`DEBUG`, `INFO`, `WARN`, `ERROR`; `PANIC` and `FATAL` are `error`); `include` and `exclude` are not converted, `log.levels` divides fit's log by logger |
| `log <name> { include http.log.access...; output; level }` | `access_log`, as a site's `log` |

The Admin API is on unless `admin off` says otherwise, as Caddy's is. Options for timeouts, PKI,
events and the like are listed as not converted: fit's defaults stand in for them.

## Site addresses

A site's addresses are on its first line, separated by spaces or commas:
`[scheme://][host][:port][/path]`.

| Address | Served |
| --- | --- |
| `example.com`, `*.example.com` | HTTPS on 443, with certificates from ACME, and HTTP on 80 redirected to it |
| `example.com:8443` | HTTPS on 8443 |
| `http://example.com` | HTTP on 80 |
| `https://example.com` | HTTPS on 443 |
| `:8080` | HTTP on 8080, any host |
| `localhost`, `127.0.0.1`, `localhost:8080` | HTTP: Caddy serves them over HTTPS with its internal CA, which fit has not (a warning says so) |
| `example.com/api/*` | the site's routes match the path too |

A host becomes the routes' `match.host`. When every site is served only over HTTPS on its port,
fit writes no listeners and [HTTPS by default](config.md#https-by-default) makes them, as Caddy
does. Otherwise it writes a listener for each address and port (`http_8080`, `https_443`), puts
each site's routes on its listeners, and adds a route on the HTTP port that redirects the HTTPS
sites' names, unless a site serves the name over HTTP itself.

Within a site:

| Directive | Converts to |
| --- | --- |
| `tls <cert> <key>` | the listener's `tls.certificates`; ACME is then not asked for this site |
| `tls <email>` | `acme.email` |
| `tls internal` | a warning: fit has no internal CA |
| `tls { on_demand }` | the listener's `tls.on_demand`; needs `on_demand_tls` |
| `tls { client_auth { mode <m>; trusted_ca_cert_file <f> } }` | the listener's `tls.client_auth`; `trust_pool file <f>` too; `require_and_verify` is `require`, any other mode `verify_if_given` |
| `tls { issuer acme { email; dir } }`, `tls { ca <url> }` | `acme.email`, `acme.directory` |
| `tls { protocols; ciphers; curves; dns }` | not converted: fit picks TLS versions and ciphers, and answers HTTP-01 and TLS-ALPN-01, not DNS-01 |
| `bind <address>` | the host the site's listeners bind |
| `log { output stdout\|stderr\|file <path>; level <level> }` | `access_log.output` and `access_log.level` (`WARN` writes the requests answered with 4xx and 5xx, `ERROR` those with 5xx); fit has one access log, of JSON lines |

## Matchers

A directive's first argument may be a matcher: `*` (every request), a path starting with `/`, or
`@name`, a named matcher defined in the site.

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

A path is exact, or a prefix ending in `*`, which fit's `match.path` takes as it is; a `*`
elsewhere, as in `*.css`, makes it a regular expression in `match.path_regexp`. A named matcher's
lines must all hold; values within a line, and lines of the same kind, are alternatives.

| Matcher | Converts to |
| --- | --- |
| `path <paths...>` | `match.path`, or `match.path_regexp` |
| `path_regexp [name] <regexp>` | `match.path_regexp` |
| `host <names...>` | `match.host` |
| `method <methods...>` | `match.method` |
| `header <field> [<values...>]` | `match.header`; `*` for any value; `!<field>` for its absence, in `match.expr` |
| `query <key>=<value>...` | `match.query`; `<key>=*` for any value |
| `remote_ip`, `client_ip <ranges...>` | `match.remote_ip`; `private_ranges` stands for the private networks |
| `not <matcher>`, `not { ... }` | `!(...)` in `match.expr` |
| `expression`, `file`, `protocol`, `vars`, `header_regexp` | not converted; directives using the matcher are left out |

## How directives become routes

Caddy runs a chain of handlers for each request, in its directives' order; fit picks one route
for each request, by priority, then the most specific host and path. The converter makes:

1. **a route for each handler directive** (`reverse_proxy`, `file_server`, `respond`,
   `php_fastcgi`, `error`), with its matcher in `match`, and for each `handle`, `handle_path`
   and `route` block, the routes of its own directives, with the block's matcher added;
2. **a route answering 200 with no body** for what no handler takes, as Caddy answers;
3. **middleware on the routes it covers**: each middleware directive (`header`, `encode`,
   `basic_auth`, `redir`, `rewrite` and the rest) is added to every route its matcher covers, in
   Caddy's directive order, and a route it covers only in part is split in two, the part it covers
   getting the middleware. `root` and `try_files` set the handler's root and files the same way.

```caddyfile
example.com {
	basic_auth /admin/* {
		alice $2a$14$Zkx19XLiW6VYouLHR5NmfOFU0z2GTNmpkT/5qqR7hx4IjWJPDhjvG
	}
	root * /srv/www
	file_server
}
```

makes two routes: `/admin/*` with `basic_auth` and the static handler, and the rest of
example.com with the static handler alone. Of `root`, `try_files`, `rewrite`, `uri` and `redir`
in one block, only the most specific applies to a request, as in Caddy; in a `route` block,
directives keep the order they are written in.

## Directives

| Directive | Converts to |
| --- | --- |
| `reverse_proxy [<matcher>] <upstreams...> { ... }` | the [`proxy`](config.md#http-handlers-proxy) handler and an [upstream](config.md#upstreams) |
| `file_server [<matcher>] [browse] { ... }` | the [`static`](config.md#http-handlers-static) handler |
| `root [<matcher>] <path>` | the `static` and `fastcgi` handlers' `root` |
| `try_files [<matcher>] <files...>` | their `try_files`; `{path}` stays, a last `=404` is what fit answers anyway |
| `respond [<matcher>] [<body>] [<status>] { body; close }` | the [`respond`](config.md#http-handlers-respond) handler |
| `error [<matcher>] [<message>] <status>` | `respond` with the status |
| `php_fastcgi [<matcher>] <address> { ... }` | the [`fastcgi`](config.md#http-handlers-fastcgi) handler |
| `redir [<matcher>] <to> [<code>]` | the [`redirect`](config.md#http-middleware-redirect) middleware; `permanent` is 301, `temporary` 302 |
| `rewrite [<matcher>] <uri>` | the [`rewrite`](config.md#http-middleware-rewrite) middleware: `/prefix{path}` adds a prefix, a path replaces the path |
| `uri [<matcher>] strip_prefix\|strip_suffix\|replace\|path_regexp ...` | the `rewrite` middleware |
| `handle_path <path> { ... }` | a `handle` block whose routes strip the path's prefix with `rewrite` |
| `handle [<matcher>] { ... }`, `route [<matcher>] { ... }` | the routes of their directives |
| `header [<matcher>] [+\|-\|?\|>]<field> [<value>]`, or a block of them | the [`headers`](config.md#http-middleware-headers) middleware's `response`: a field is set, `+` adds, `-` deletes |
| `request_header [<matcher>] ...` | the `headers` middleware's `request` |
| `encode [<matcher>] [zstd] [gzip] [br] { minimum_length <n> }` | the [`compress`](config.md#http-middleware-compress) middleware; zstd and gzip when none are named |
| `basic_auth`, `basicauth [<matcher>] [bcrypt [<realm>]] { <user> <hash> }` | the [`basic_auth`](config.md#http-middleware-basic_auth) middleware; base64 hashes of older Caddy too |
| `forward_auth [<matcher>] <upstreams...> { uri; copy_headers }` | the [`forward_auth`](config.md#http-middleware-forward_auth) middleware and an upstream |
| `request_body [<matcher>] { max_size <size> }` | the [`body_limit`](config.md#http-middleware-body_limit) middleware; `10MB` is 10,000,000 bytes, `10MiB` 10,485,760 |
| `metrics` | `metrics`, served by the Admin API at `/metrics` |

### `reverse_proxy`

Upstream addresses: `host:port`, `:port` (localhost), `http://`, `https://` (TLS, with HTTP/2
when the server offers it), `h2c://`, and ranges such as `10.0.0.1:8001-8004`. Proxies with the
same addresses and options share one upstream, named for its first address
(`proxy_10_0_0_1_8080`).

| Subdirective | Converts to |
| --- | --- |
| `to <upstreams...>` | more addresses |
| `lb_policy round_robin\|least_conn\|random\|ip_hash\|client_ip_hash\|uri_hash` | `load_balancing` |
| `lb_policy random_choose` | `p2c` |
| `lb_policy header <field>` | `header_hash` with `hash_key` |
| `lb_policy cookie [<name>]` | `sticky_cookie` |
| `lb_policy weighted_round_robin <weights...>` | `weights` |
| `lb_retries <n>` | the proxy's `retry.attempts` |
| `lb_try_duration` | the proxy's `retry`, with its defaults |
| `health_uri`, `health_path`, `health_port`, `health_interval`, `health_timeout`, `health_status <code>`, `health_passes`, `health_fails`, `health_headers { Host }` | `health_check` |
| `fail_duration`, `max_fails` | `fail_timeout`, `max_fails` |
| `header_up Host <value>` | the proxy's `host_header`; `{upstream_hostport}` is the single address |
| `header_up <field> <value>`, `header_up -<field>` | the `headers` middleware's `request` |
| `header_down ...` | the `headers` middleware's `response` |
| `trusted_proxies <ranges...>` | the proxy's `trusted_proxies` |
| `stream_timeout` | the proxy's `tunnel_idle_timeout` |
| `transport http { versions h2c }`, `{ tls }` | `protocol` `h2c`, `h2` |
| `transport http { tls_server_name; tls_trusted_ca_certs; tls_trust_pool file; tls_client_auth }` | `tls_server_name`, `tls_ca_file`, `tls_cert_file` and `tls_key_file` |
| `transport http { dial_timeout; response_header_timeout; read_timeout }` | `connect_timeout`, `response_header_timeout`, `response_idle_timeout` |
| `transport http { keepalive <d>\|off; keepalive_idle_conns; max_conns_per_host }` | `idle_timeout`, `max_idle_conns`, `max_conns` |
| `flush_interval`, `buffer_requests` and the like | nothing: fit streams both ways |

`header_up X-Forwarded-*` and `X-Real-IP {remote_host}` are left out: fit's proxy sets
X-Forwarded-For, -Proto and -Host itself.

### `file_server`

| Subdirective | Converts to |
| --- | --- |
| `root <path>` | `root` |
| `browse` | `browse` |
| `index <files...>` | `index` |
| `precompressed [br] [zstd] [gzip]` | `precompressed`; all three when none are named |

Without a `root`, Caddy serves the directory it runs in; the converter writes `"."` and says so.

### `php_fastcgi`

| Subdirective | Converts to |
| --- | --- |
| `root <path>` | `root`; the site's `root` otherwise |
| `split <exts...>` | `split_path` |
| `index <file>`, `index off` | `index`, and `try_files` with it; none |
| `try_files <files...>` | `try_files` |
| `env <key> <value>` | `env` |
| `dial_timeout`, `read_timeout` | `connect_timeout`, `response_idle_timeout` |

As in Caddy, a path that is no script of its own runs `index.php`. Beside a `file_server` that
takes the same requests, PHP takes the paths of `.php` scripts and directories, and the files the
rest: a path that is neither is answered 404 rather than sent to `index.php`.

## Placeholders

fit expands few placeholders. Where it has them, Caddy's are converted: in `redir`'s URL,
`{uri}`, `{path}`, `{query}`, `{host}`, `{scheme}` and their `{http.request.*}` forms; in
`rewrite` and `try_files`, `{path}`. A directive with another placeholder is left out, but
`respond`, whose body is sent as written, with a warning.

## Not converted

Each of these is listed by line when it appears:

- Caddy's internal CA (`tls internal`, `local_certs`) and DNS challenges.
- CEL expressions, and the `file`, `protocol`, `vars` and `header_regexp` matchers.
- `templates`, `map`, `vars`, `handle_errors`, `abort`, `intercept`, `push`, `tracing`, `invoke`
  and plugins' directives.
- `import` of files: only snippets defined in the Caddyfile.
- `rewrite` and `uri` act after fit has picked the route: a rewritten path does not choose another
  route, as it can in Caddy.
- Layer 4, caching, rate limits, JWT and WASM have no Caddyfile directive here: add them to the
  .conf file `fit adapt` writes.
