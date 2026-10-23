# .fit 参考

[English](fit.md) | [简体中文](fit.zh-CN.md)

`.fit` 文件是 fit 自己的配置格式：像 nginx 和 Caddy 的配置那样由指令块组成，逐字段表达 fit 原生 JSON 所表达的内容。
[配置参考](config.zh-CN.md) 列出的每个字段、每个模块的参数都可以用它来写；`site` 块把一组域名的路由写在一起。以 `.fit`
结尾的文件，`fit run`、`fit validate` 和 `fit adapt` 都按这种格式读取；一份配置还可以分散在多个文件和目录中
（[见下文](#多个文件和目录)）。

- [初识](#初识)
- [语法](#语法)
- [字段](#字段)
- [模块](#模块)
- [站点](#站点)
- [多个文件和目录](#多个文件和目录)
- [错误](#错误)
- [转换](#转换)

## 初识

```fit
listener web {
	address :8080
	protocols http/1.1 h2c
}

upstream app {
	addresses 127.0.0.1:9000 127.0.0.1:9001
	health_check {
		path /healthz
		interval 5s
	}
}

site example.com www.example.com {
	listeners web
	middleware compress

	route /api/* {
		middleware rate_limit { rate 100; burst 200 }
		handler proxy { upstream app }
	}
	route {
		handler static { root /srv/www }
	}
}
```

用 JSON 写是：一个 listener、一个 upstream 和两条路由，每条都匹配站点的域名，第一条还匹配 `/api/*`，两条都先经过
`compress`：

```json
{
  "listeners": [{"name": "web", "address": ":8080", "protocols": ["http/1.1", "h2c"]}],
  "upstreams": [{"name": "app", "addresses": ["127.0.0.1:9000", "127.0.0.1:9001"],
                 "health_check": {"path": "/healthz", "interval": "5s"}}],
  "routes": [
    {"match": {"host": ["example.com", "www.example.com"], "path": ["/api/*"]},
     "listeners": ["web"],
     "middleware": [{"module": "compress"}, {"module": "rate_limit", "params": {"rate": 100, "burst": 200}}],
     "handler": {"module": "proxy", "params": {"upstream": "app"}}},
    {"match": {"host": ["example.com", "www.example.com"]},
     "listeners": ["web"],
     "middleware": [{"module": "compress"}],
     "handler": {"module": "static", "params": {"root": "/srv/www"}}}
  ]
}
```

## 语法

- **指令**是一行中的名字和参数，以空格或 tab 分隔；后面可以跟一个花括号块。换行结束一条指令，`;` 也可以，如
  `rate 100; burst 200`。
- **块**以参数之后、同一行上的 `{` 开始，以结束这条指令的 `}` 结束。块可以写在一行里，如 `{ rate 100 }`；`{}` 是空块。
- **注释**从一个词开头的 `#` 到行尾。
- **引号**：`"双引号"` 中可以有空格和 `;`，支持转义 `\"`、`\\`、`\n`、`\t`、`\r`，以及表示不开始 `{$` 的 `{` 的
  `\{`。`` `反引号` `` 中的内容原样保留，也可以跨行。加引号的参数总是字符串。
- **环境变量**：读取文件时，`{$NAME}` 替换为变量的值，`{$NAME:default}` 在变量未设置时替换为默认值；反引号中不替换。
- **词**是其余到空格或换行为止的内容：`{path}`、`/api/*`、`https://{host}{uri}` 和 `text/html;charset=utf-8` 都各是
  一个词。以 `;` 结尾的词结束它所在的指令。

## 字段

每条指令都是它所在块的一个字段，名字与 JSON 和 YAML 中相同；一个块就是一个对象。字段名之后写什么，取决于字段的类型：

| 字段类型 | 写法 | JSON |
| --- | --- | --- |
| 字符串、时长 | 一个参数 | `address :8080` → `"address": ":8080"` |
| 整数、数字 | 一个参数 | `max_conns 256` → `"max_conns": 256` |
| 布尔 | `true` 或 `false`，不写表示 true | `secure` → `"secure": true` |
| 标量列表 | 各个参数，写在一行或重复多行 | `protocols h2 h3` → `"protocols": ["h2", "h3"]` |
| 对象 | 由其字段组成的块 | `health_check { path /healthz }` |
| map | 由键及其值组成的块 | `set { X-Env prod }` → `"set": {"X-Env": "prod"}` |
| 对象列表 | 每个元素一个块，重复书写 | 写两次 `certificates { ... }` → 两个证书 |
| 模块 | 模块名，然后是它的参数块（[模块](#模块)） | `handler proxy { upstream app }` |

- 在配置顶层，对象列表用单数书写，参数是元素的 `name`：`listener`、`route`、`upstream`、`service` 和 `provider`。
  因此 `listener web { address :8080 }` 就是 `listeners` 中的 `{"name": "web", "address": ":8080"}`。名字也可以写在块里，
  路由的名字可以省略。在块内，对象列表用自己的字段名重复书写：`certificates { ... }`、`users { ... }`。
- 不带参数的列表是空列表：`match { query { debug } }` 中的 `debug` 是一个必须出现、值不限的查询参数。
- 值按字段的类型读取：在数字字段中 `level 5` 是数字，而 `body 5` 是字符串 `"5"`。类型不对的值会在它所写的位置报错，
  例如给数字字段写了带引号的 `"100"`。
- 改名前的旧字段名仍然可用，并给出警告，与 JSON 相同。

## 模块

路由的 `handler` 和每个 `middleware` 先写模块名，再用块给出参数，块由模块自己的字段组成；没有参数的模块不需要块。
`middleware` 每个写一次，按执行顺序重复：

```fit
listener web {
	address :8080
}

upstream app {
	addresses 127.0.0.1:9000
}

route api {
	match {
		path /api/*
	}
	middleware compress
	middleware headers {
		response {
			set {
				Strict-Transport-Security "max-age=31536000"
			}
			delete Server
		}
	}
	handler proxy {
		upstream app
		retry {
			attempts 2
			statuses 502 503 504
		}
	}
}
```

用 `fit build` 编入的自定义模块写法相同：参数按模块自己的字段读取。

## 站点

`site` 把一组域名的路由写在一起。它包含这些路由共有的内容，以及路由本身：

```text
site <hosts...> {
	listeners <names...>    路由的 listener，路由自己没写时使用
	priority <n>            路由的优先级，路由自己没写时使用
	match { ... }           每条路由还要匹配的条件，host 和 path 除外；
	                        路由自己的 match 写了同一字段时以路由的为准
	middleware <module> ... 每条路由都执行的中间件，在路由自己的之前
	route [<paths...>] { ... }
}
```

站点中的每个 `route` 都成为配置中的一条路由：它的 `match.host` 是站点的域名，`match.path` 是 route 的参数（有参数时），
其余与顶层路由相同：`name`、`match`、`middleware`、`handler`、`priority`、`listeners`。这些路由在配置的路由中处于站点
所写的位置。站点的路由不能设置 `host`；以参数给出路径时，也不能在 `match` 中再写 `path`。

```fit
listener web {
	address :8080
}

upstream app {
	addresses 127.0.0.1:9000
}

site api.example.com {
	listeners web
	match {
		remote_ip 10.0.0.0/8
	}
	middleware jwt {
		jwks_url https://auth.example.com/.well-known/jwks.json
		audience api
	}

	route /v1/* /v2/* {
		name api
		match {
			method GET POST
		}
		handler proxy { upstream app }
	}
	route /healthz {
		handler respond { body ok }
	}
}
```

得到两条路由：`api`，匹配域名、两个路径、GET 和 POST 以及客户端地址段，带 `jwt`；另一条匹配域名、`/healthz` 和客户端
地址段，同样带 `jwt`。

## 多个文件和目录

`-c` 可以给出多次，也可以指定目录；配置是所有文件合并的结果：

```bash
fit run -c /etc/fit/base.fit -c /etc/fit/sites
```

- **目录**代表其中（包括子目录中）以 `.fit`、`.yaml`、`.yml` 或 `.json` 结尾的文件，按字典序；名字以 `.` 开头的文件和
  目录被跳过。用 `-c` 直接指定的文件不论名字都会读取，nginx 配置和 Caddyfile 也一样；同一文件指定两次只读一次。
- **合并**：配置顶层的列表 `listeners`、`routes`、`upstreams`、`services` 和 `providers` 按读取文件的顺序拼接。其他字段，
  如 `admin` 或 `acme`，只能在一个文件中给出；`schema_version` 可以在多个文件中给出，但必须一致。每个文件可以是任一种格式。
- **错误**会给出文件、文件中的位置，以及该文件内的路径，如
  `sites/api.fit:12:3: routes[0].handler.params.status: ...`。
- **重新加载**：SIGHUP 会重新读取每个文件。使用 `-watch` 时，文件被修改、被加入目录或从目录中移除，都会重新应用配置。
  每次都重新读取全部文件；上限为 10,000 个文件，共 64 MiB。

## 错误

`.fit` 文件按严格的规则读取。语法错误、写法不对的指令（给块写了值、给单值写了两个值）或重复给出的字段，都会在其行列
报错；配置自身检查发现的问题，如未知字段、超出范围或缺少的值，也会在它所写的位置报告：

```text
fit.fit:7:10: routes[0].handler.params.status: 99 is less than the minimum 100
```

## 转换

`fit adapt` 可以把 fit 任一种格式的配置写成 `.fit` 文件，也可以反向转换：

```bash
fit adapt -c fit.json -to fit -o fit.fit
```

```bash
fit adapt -c fit.fit -o fit.json
```

路由逐条写出，不会归并成站点。nginx 配置和 Caddyfile 也可以这样转换成 `.fit` 文件，见
[从 nginx 迁移](../migrating-from-nginx.zh-CN.md) 和 [从 Caddy 迁移](../migrating-from-caddy.zh-CN.md)。
[templates/fit/fit.fit](../../templates/fit/fit.fit) 包含全部字段，说明和可选值写在注释中。
