# 配置

[English](configuration.md) | [简体中文](configuration.zh-CN.md)

fit 读取一个配置文件，可以是三种格式之一：**Caddyfile**、**YAML** 或 **JSON**。三者表达的内容相同：fit 监听的地址、
为每个请求选择应答者的路由、请求被代理到的 upstream，以及日志、限制、证书和 Admin API 的设置。JSON 是 fit 的原生格式；
YAML 的字段与它完全相同，Caddyfile 会被转换成这些字段。

| 格式 | 适合 | 参考 |
| --- | --- | --- |
| Caddyfile | 简短易读的站点配置；从 Caddy 带过来的配置 | [Caddyfile 参考](reference/caddyfile.zh-CN.md) |
| YAML | fit 的全部字段，可写注释，可用锚点复用重复内容 | [YAML 参考](reference/yaml.zh-CN.md) |
| JSON | fit 的全部字段；Admin API 收发的格式，工具生成的格式 | [JSON 参考](reference/json.zh-CN.md) |

每个字段的类型、默认值和含义，以及三种格式的示例，见 [配置参考](reference/config.zh-CN.md)。[配置模板](../templates/README.zh-CN.md)
为每种格式提供一份包含全部字段的完整配置，可复制后修改。

## 三种格式

### Caddyfile

Caddy 自己的格式：每个站点一个块，以地址命名，块内是指令。fit 把 Caddy 指令的常用子集转换为自己的配置，并按行列出
未转换的部分。名为 `Caddyfile` 或以 `.caddyfile` 结尾的文件按 Caddyfile 读取。

```caddyfile
# 以 HTTPS 提供，自动为 example.com 申请证书。
example.com {
	encode zstd gzip
	reverse_proxy /api/* 127.0.0.1:9000
	root * /srv/www
	file_server
}
```

逐条指令的转换规则见 [Caddyfile 参考](reference/caddyfile.zh-CN.md)。

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
最后一个元素后的逗号。其他文件都按 JSON 读取，默认是 `fit.json`。

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

## 运行、检查和转换

格式由文件名判断；三种格式的命令相同：

```bash
fit run -c Caddyfile
```

```bash
fit validate -c fit.yaml
```

`fit validate` 像 `fit run` 那样加载配置，并检查每个模块的参数，每个错误都给出文件、行号和列号，指向原始文件中的位置，
Caddyfile 和 YAML 也一样。`fit adapt` 负责转换：

| 从 | 到 | 命令 |
| --- | --- | --- |
| Caddyfile | JSON | `fit adapt -c Caddyfile -o fit.json` |
| nginx | JSON | `fit adapt -c nginx.conf -o fit.json` |
| YAML | JSON | `fit adapt -c fit.yaml -o fit.json` |
| JSON（或以上任何一种） | YAML | `fit adapt -c fit.json -to yaml -o fit.yaml` |

转换 Caddyfile 或 nginx 配置时，未转换的部分会打印到标准错误。`fit schema` 打印配置的 JSON Schema，编辑器可以用它在
编写 JSON 和 YAML 时补全和检查。

## 修改运行中的配置

无论哪种格式，`fit run` 在收到 SIGHUP 时，或使用 `-watch` 时每当文件变化，都会重新应用配置文件。加载或编译失败的配置会
被拒绝，运行中的配置保持不变。[Admin API](reference/config.md#admin-api) 以 JSON 读取和替换运行中的配置，可以整体替换，
也可以按 JSON pointer 修改，并保留版本以便回滚。

## 从其他服务器迁移

[nginx](migrating-from-nginx.zh-CN.md) 和 [Caddy](migrating-from-caddy.zh-CN.md) 的配置可用 `fit adapt` 转换；
[Traefik](migrating-from-traefik.zh-CN.md) 的配置逐字段对应。每篇指南都给出同一套配置在两种服务器中的写法。
