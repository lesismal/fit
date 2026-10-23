# 配置

[English](configuration.md) | [简体中文](configuration.zh-CN.md)

fit 从一个或多个文件读取配置，可以是三种格式之一：**.konf** 文件、**YAML** 或 **JSON**。三者逐字段表达相同的内容：fit
监听的地址、为每个请求选择应答者的路由、请求被代理到的 upstream，以及日志、限制、证书和 Admin API 的设置。Admin API
收发的是 JSON；三种格式的字段完全相同，彼此之间都可以转换。

| 格式 | 适合 | 参考 |
| --- | --- | --- |
| .konf | 手写的配置：像 nginx 和 Caddy 那样的指令块，支持站点 | [.konf 参考](reference/fit.zh-CN.md) |
| YAML | 手写或工具生成的配置，可用锚点复用重复内容 | [YAML 参考](reference/yaml.zh-CN.md) |
| JSON | Admin API 收发的格式，工具生成的格式 | [JSON 参考](reference/json.zh-CN.md) |

每个字段的类型、默认值和含义，以及三种格式的示例，见 [配置参考](reference/config.zh-CN.md)。[配置模板](../templates/README.zh-CN.md)
为每种格式提供一份包含全部字段的完整配置，可复制后修改。

## 三种格式

### .konf

fit 自己的格式：每个字段一条指令，每个对象一个块，用 `site` 把一组域名的路由写在一起。以 `.konf` 结尾的文件。

```fit
upstream api {
	addresses 127.0.0.1:9000
}

# 以 HTTPS 提供，自动为 example.com 申请证书。
site example.com {
	middleware compress
	route /api/* {
		handler proxy { upstream api }
	}
	route {
		handler static { root /srv/www }
	}
}
```

语法、各类字段的写法、站点和多个文件，见 [.konf 参考](reference/fit.zh-CN.md)。

### YAML

用 YAML 语法写的 fit 配置：可写注释，大多数值无需引号和括号，重复内容可用锚点和别名。以 `.yaml` 或 `.yml` 结尾的文件。

```yaml
# 同一个站点。
upstreams:
  - name: api
    addresses: ["127.0.0.1:9000"]
routes:
  - match: {host: [example.com], path: ["/api/*"]}
    middleware: [{module: compress}]
    handler: {module: proxy, params: {upstream: api}}
  - match: {host: [example.com]}
    middleware: [{module: compress}]
    handler: {module: static, params: {root: /srv/www}}
```

YAML 如何对应到配置，以及需要注意的地方，见 [YAML 参考](reference/yaml.zh-CN.md)。

### JSON

fit 的原生格式，严格读取：未知字段、类型错误的值、重复的键都是错误，并给出行号和列号。允许 `//` 和 `/* */` 注释，以及
最后一个元素后的逗号。以 `.json` 结尾的文件，以及单独指定的其他文件，都按 JSON 读取，默认是 `fit.json`。

```json
{
  // 同一个站点。
  "upstreams": [{"name": "api", "addresses": ["127.0.0.1:9000"]}],
  "routes": [
    {"match": {"host": ["example.com"], "path": ["/api/*"]},
     "middleware": [{"module": "compress"}],
     "handler": {"module": "proxy", "params": {"upstream": "api"}}},
    {"match": {"host": ["example.com"]},
     "middleware": [{"module": "compress"}],
     "handler": {"module": "static", "params": {"root": "/srv/www"}}},
  ],
}
```

更多内容见 [JSON 参考](reference/json.zh-CN.md)。

## 配置包含什么

| 部分 | 是什么 | 参考 |
| --- | --- | --- |
| `listeners` | 地址和协议；不写时由默认 HTTPS 生成 | [Listeners](reference/config.md#listeners) |
| `routes` | 每类请求的匹配条件、中间件和处理器 | [Routes](reference/config.md#routes) |
| `upstreams` | 被代理的服务器，带负载均衡和健康检查 | [Upstreams](reference/config.md#upstreams) |
| `services` | TCP 和 UDP 代理 | [Services](reference/config.md#services) |
| 处理器 | `proxy`、`static`、`respond`、`fastcgi`、`forward_proxy` | [Handlers](reference/config.md#handlers) |
| 中间件 | 认证、限流、缓存、压缩、头部、改写等 | [Middleware](reference/config.md#middleware) |
| `access_log`、`telemetry`、`metrics` | 日志、链路追踪和指标 | [Access log](reference/config.md#access-log)、[Telemetry](reference/config.md#telemetry)、[Metrics](reference/config.md#metrics) |
| `limits` | 连接、头部、请求体和内存的上限 | [Limits](reference/config.md#limits) |
| `auto_https`、`acme` | 默认 HTTPS 和证书 | [HTTPS by default](reference/config.md#https-by-default)、[ACME](reference/config.md#acme) |
| `admin` | Admin API | [Admin API](reference/config.md#admin-api) |
| `providers` | Docker、Consul、etcd 和 Kubernetes | [Providers](reference/config.md#providers) |

## 多个文件和目录

一份配置可以分散在多个文件（任一种格式）和目录中，每个用一个 `-c` 给出：

```bash
fit run -c /etc/fit/base.konf -c /etc/fit/sites
```

目录代表其中（包括子目录中）的 `.konf`、`.yaml`、`.yml` 和 `.json` 文件，按字典序，名字以 `.` 开头的除外。配置顶层的列表
`listeners`、`routes`、`upstreams`、`services` 和 `providers` 按文件顺序拼接；其他字段只能在一个文件中给出。错误会指出
所在的文件。详见 [多个文件和目录](reference/fit.zh-CN.md#多个文件和目录)。

## 运行、检查和转换

格式由每个文件的名字判断；三种格式的命令相同：

```bash
fit run -c fit.konf
```

```bash
fit validate -c fit.yaml -c conf.d
```

`fit validate` 像 `fit run` 那样加载配置，并检查每个模块的参数，每个错误都给出文件、行号和列号，指向原始文件中的位置，
无论哪种格式。`fit adapt` 在格式之间转换，并可把多个文件合并为一个：

| 从 | 到 | 命令 |
| --- | --- | --- |
| JSON 或 YAML | .konf | `fit adapt -c fit.json -to fit -o fit.konf` |
| .konf 或 YAML | JSON | `fit adapt -c fit.konf -o fit.json` |
| .konf 或 JSON | YAML | `fit adapt -c fit.konf -to yaml -o fit.yaml` |
| 一个目录 | 一个文件 | `fit adapt -c conf.d -to fit -o fit.konf` |

`fit schema` 打印配置的 JSON Schema，编辑器可以用它在编写 JSON 和 YAML 时补全和检查；`fit schema -template fit` 打印
.konf 模板。

## 修改运行中的配置

无论哪种格式，`fit run` 在收到 SIGHUP 时，或使用 `-watch` 时每当某个文件变化、目录中加入或移除文件，都会重新应用配置。
加载或编译失败的配置会被拒绝，运行中的配置保持不变。[Admin API](reference/config.md#admin-api) 以 JSON 读取和替换运行中的
配置，可以整体替换，也可以按 JSON pointer 修改，并保留版本以便回滚。

## 从其他服务器迁移

`fit adapt` 可以把 nginx 配置或 Caddyfile 转换为 fit 的配置，作为起点，并列出未转换的部分：
`fit adapt -from nginx -c nginx.conf -to fit -o fit.konf`。[nginx](migrating-from-nginx.zh-CN.md)、[Caddy](migrating-from-caddy.zh-CN.md)
和 [Traefik](migrating-from-traefik.zh-CN.md) 的迁移指南给出同一套配置在各服务器和 fit 中的写法，以及各部分如何对应到 fit。
