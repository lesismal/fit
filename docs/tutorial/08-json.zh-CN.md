# JSON

[English](08-json.md) | [简体中文](08-json.zh-CN.md)

## 功能

**JSON** 这一页把整份配置当作一个 JSON 对象看与改，有两个标签：**草稿（Draft）** 与**生效配置
（Effective）**。草稿是各表单正在编辑的那一份，生效配置是 fit 实际跑的那一份：基础配置加上各配置来源
（provider）提供的内容。两者不一样时，差别在[一致性检查](10-consistency-check.zh-CN.md)页列出。

这一页是看与改整份配置的地方：某成员没有自己的页面（或页面只编辑它的一部分），就在这里改；从别处拿来的 JSON
文件可以在这里打开并应用；一份配置要交给别的机器时，从这里下载。它不编辑 `admin`：fit 用 `admin.store` 保存
配置时，草稿里没有 `admin`。

这一页与其他编辑配置的页面共用同一份草稿和同一条保存路径，所以从这里保存与从表单保存是一回事：先校验，再应用
为一个新配置版本，并写入 store。

## 页面与操作

### 草稿（Draft）

一个文本框，整份配置的 JSON。这里的修改与各表单编辑的是同一份草稿：在表单里改过的字段在这里看得到，在这里输入
的 JSON 解析后就是表单的树；解析不了时文本按原样保留、阻止保存，并指出错误（"无法解析：…"），表单显示的仍是最
后一次解析成功的值。离开这个标签页前会先解析一次；解析不过，页面就留在这里。

**保存并应用** 校验并整体应用这份 JSON，与其他页面走同一条路径：先加载与编译，再应用为一个新配置版本并写入
store；错误显示在各自的路径处，带路径、行号与列号。**检查** 是同一请求带 `?dry_run=1`，只检查不应用。读取之后
配置被别处修改（`412`）时，页面会提示：放弃修改以重新读取，或先从这里复制修改。保存栏显示配置版本与"有未保存
的修改"，三个动作是**保存并应用**、**检查**与**放弃修改**。

这一页的工具：**格式化**（把文本重排成草稿的值）、**复制**、**打开文件**（读入一个 `.json` 文件）、**下载**
（存为 `fit-config-<版本>.json`）。未编辑的文本跟随草稿；编辑过之后它就以你写的为准。

### 生效配置（Effective）

只读：基础配置加上各 provider 提供的内容，也就是 fit 实际跑的那一份。三块：

- **优先级**：从最先到最后，先是基础配置的来源，再是各 provider 按写的顺序。前面的赢。
- **来源**：生效配置里每个成员、每个数组元素来自哪里——路径、来源（`file`、`admin`、`rollback` 或某个
  provider 的名字），以及它在 provider 提供的内容中的位置（基础配置的行没有这一列）。
- **生效配置 · 版本 N**：格式化后的 JSON，带**复制**。

被遮蔽的元素——某个 provider 提供了、但同名元素已被前面的来源取走——不在生效配置里，在概览页与一致性检查里
作为 info 出现。

生效配置显示的样子，一小段：

```json
{
  "listeners": [
    { "name": "web", "address": ":8080", "protocols": ["http/1.1"] },
    { "name": "shop", "address": ":8081", "protocols": ["http/1.1"] }
  ],
  "routes": [
    {
      "name": "api",
      "match": { "host": ["api.example.com"] },
      "handler": { "module": "proxy", "params": { "upstream": "backend" } }
    }
  ],
  "upstreams": [
    { "name": "backend", "addresses": ["127.0.0.1:9000"] }
  ]
}
```

旁边的来源表：

| 路径 | 来源 | |
| --- | --- | --- |
| `/listeners/0` | `file` | |
| `/listeners/1` | `docker` | `/listeners/0` |
| `/routes/0` | `file` | |
| `/upstreams/0` | `docker` | `/upstreams/0` |

草稿与生效配置常常不同：`shop` 那条监听不在草稿里，它是 provider 提供的；把它写进草稿，就是把它从"由 provider
管理"变成"由配置写"，此后 provider 的同名元素会被跳过。

## 参数

这一页编辑整份配置，不只有一个成员，所以没有自己的字段表；各成员的字段表在配置参考对应章：
[The config](../reference/config.md#the-config)、[Listeners](../reference/config.md#listeners)、
[Routes](../reference/config.md#routes)、[Upstreams](../reference/config.md#upstreams)、
[Services](../reference/config.md#services)、[Providers](../reference/config.md#providers)。

优先级与合并的规则：

| | |
| --- | --- |
| 基础配置 | 赢过每个 provider；它的来源是最后一次改它的地方：`file`、`admin` 或 `rollback` |
| provider | 赢过排在它后面的 provider；它的元素若与前面来源里同名的元素撞名，就被跳过 |
| 数组 | `listeners`、`routes`、`upstreams`、`services` 与 `providers` 跨文件与跨来源合并；其他成员只写在一处 |

这一页用到的接口：

这一页用到的接口（Admin API 的接口在控制台上都在 `/api/m/admin/` 下）：

| 接口 | |
| --- | --- |
| `GET /config/effective` | 生效配置：版本、ETag、优先级、来源与 JSON |
| `POST /config/render?format=json` | 把草稿渲染成文本 |
| `POST /config/parse?format=json` | 把文本解析回草稿 |
| `PUT /config` | 保存整份配置，带 `If-Match` |

来源表的三列：

| 列 | 说明 |
| --- | --- |
| 路径 | 它在合并后的配置中的 JSON pointer |
| 来源 | 它来自哪个 provider，或基础配置的来源 |
| | 它在 provider 提供的内容中的 JSON pointer；基础配置的行没有这一列 |

生效配置那块的标题带运行中的版本号；它的 ETag 是基础配置的 ETag，不是合并后的。

`GET /config/effective` 的应答：

| 字段 | 说明 |
| --- | --- |
| `version` | 运行中的版本号 |
| `etag` | 基础配置的 ETag |
| `precedence` | 来源的顺序，基础配置在前 |
| `config` | 生效配置的 JSON |
| `sources` | 每个成员、每个数组元素来自哪里 |
| `shadowed` | 被遮蔽的项 |

页面上显示的是优先级、来源与 JSON；`shadowed` 在概览页与一致性检查页显示。

## 常见情形

- **一次改一整份**：草稿就是各表单编辑的那一份，所以从这里保存与从表单保存没有区别；改多个成员时，这里比逐个
  页面点保存更快，而且只产生一个版本。
- **从别处拿来的文件**：**打开文件**读入后，文本要先解析得过才能保存；解析不了时页面停在草稿标签，并指出错误。
- **看 fit 实际跑的那份**：生效标签读的是 `GET /config/effective`，它包含 provider 提供的元素，这些常常不在
  草稿里。
- **没有 store 时**：控制台像 Admin API 一样在内存中改运行中的配置，重启后从配置文件重新开始；有 store 时，
  每次应用都写进 store，而草稿不含 `admin`。
- **配置被别处改过**：`412` 时页面提示重新读取；要先保住这里的修改，就从这里复制出来再重新读取。

## .conf 配置

这一页是 JSON 页面，没有 `.conf` 示例；`.conf` 的写法与整份配置的三种格式见
[.conf 格式](../reference/fit.zh-CN.md)与[三种格式](../configuration.zh-CN.md#三种格式)。上面生效配置的
那段 JSON 就是这一页显示的内容。

## 相关文档

- [配置](../configuration.zh-CN.md)：[三种格式](../configuration.zh-CN.md#三种格式)、
  [多个文件和目录](../configuration.zh-CN.md#多个文件和目录)、
  [修改运行中的配置](../configuration.zh-CN.md#修改运行中的配置)。
- [配置参考](../reference/config.zh-CN.md)与[.conf 格式](../reference/fit.zh-CN.md)。
- [控制台与 store](../console.zh-CN.md#页面)：这一页在菜单中的位置。
- [全局设置](07-global-settings.zh-CN.md)的导入 / 导出页：整份配置的 `.conf`、`.yaml`、`.json` 与 `.store`。
- 相邻章：[功能导航栏](03-navigation.zh-CN.md)、[版本](09-versions.zh-CN.md)、
  [一致性检查](10-consistency-check.zh-CN.md)。
