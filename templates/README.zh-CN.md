# 配置模板

[English](README.md) | [简体中文](README.zh-CN.md)

fit 三种配置格式各有一份完整的配置模板，可复制后修改。每份都包含配置和每个模块参数的全部字段，每个字段上方的注释给出说明、
类型、可选值和默认值：

| 模板 | 格式 |
| --- | --- |
| [conf/fit.conf](conf/fit.conf) | fit 自己的 [.conf 格式](../docs/reference/fit.zh-CN.md) |
| [yaml/fit.yaml](yaml/fit.yaml) | YAML |
| [json/fit.json](json/fit.json) | 带注释和末尾逗号的 JSON，fit 可以直接读取 |

每份模板中，一个小配置是启用的，其余内容都被注释掉，并写好默认值或示例值，按需取消注释即可。每份都可以直接运行
（`fit run -c templates/conf/fit.conf`）：一个监听 :8080 的 listener、一个 upstream 和一条应答 "hello from fit" 的路由。在被
注释掉的块中，字段说明是注释中的注释（`# #` 或 `// //`），取消注释整个块后它仍是注释。路由部分为每个处理器各列一条路由，
并在一条路由中列出所有中间件模块，各带全部参数。

模板与 [配置参考](../docs/reference/config.zh-CN.md) 一样由代码生成，落后时测试会失败：用 `go test ./schema -update` 重新生成，
或用 `fit schema -template fit`、`yaml` 或 `json` 打印当前二进制所含模块的模板。三种格式的比较见 [配置](../docs/configuration.zh-CN.md)。

## 目录形式

同一份模板也有目录形式，按种类分成多个文件，供以目录维护的配置使用，`fit run -c <目录>` 即可读取
（[多个文件和目录](../docs/reference/fit.zh-CN.md#多个文件和目录)）：[conf/conf.d](conf/conf.d)、[yaml/conf.d](yaml/conf.d)
和 [json/conf.d](json/conf.d)。

| 文件 | 内容 |
| --- | --- |
| `00-runtime` | 运行规格：`limits`（等待慢客户端的字节即总背压、连接数、头部和请求体大小、写停顿超时）和 `engine`（fib 的 event loop 数量、worker 数、任务队列、缓冲、单连接背压、backlog） |
| `10-listeners` | 所有站点共用的 listener：地址、协议、TLS、客户端证书、PROXY 协议 |
| `20-tls` | `auto_https` 和 `acme` |
| `30-observability` | `access_log`、`telemetry`、`metrics` 和 `admin` |
| `40-services` | 四层 `services` |
| `50-providers` | `providers` |
| `sites/<主机名>` | 每个站点一个文件：它的路由和它代理到的 upstream。`sites/api.example.com` 列出 upstream 的全部字段，`sites/example.com` 列出每个处理器和中间件 |

目录模板可以直接运行：一个监听 :8080 的 listener，example.com 应答 "hello from fit"，api.example.com 代理到 127.0.0.1:9000。
listeners、routes、upstreams、services 和 providers 可以分写在多个文件中并被合并，其他字段只能写在一个文件里。用
`fit schema -template fit -dir /etc/fit/conf.d`（或 `yaml`、`json`）生成一份，目录中已有同名文件时拒绝覆盖。
