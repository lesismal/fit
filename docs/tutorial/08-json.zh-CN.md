# JSON

[English](08-json.md) | [简体中文](08-json.zh-CN.md)

## 功能

菜单中的 **JSON**（在**全局配置**之后）只有一页 **JSON 配置**，URL 为 `#/sys.json/json`，把整份配置当作一个
JSON 对象看与改，有两个标签：**草稿**与**生效配置**。草稿是各表单正在编辑的那一份，生效配置是 fit 实际跑的
那一份：基础配置加上各配置来源（provider）提供的内容。两者本来就会不一样——生效配置含 provider 提供的
元素而草稿没有，草稿可能还有未保存的修改；[一致性检查](10-consistency-check.zh-CN.md)管的是运行中的 fit
与生效配置之间的差别。

这一页是看与改整份配置的地方：某成员没有自己的页面（或页面只编辑它的一部分），就在这里改；从别处拿来的 JSON
文件可以在这里打开并应用；草稿也可以从这里下载。任何页面都没有 `admin` 的表单。fit 用 `admin.store` 保存配置
时，`admin` 根本不在草稿里，因为它只来自引导文件，带 `admin` 发送的配置会被拒绝。没有 store 时，草稿就是配置
文件所写的配置，文件写了 `admin` 时也包含它，对 `admin` 的修改要到重启才生效。

这一页与其他编辑配置的页面共用同一份草稿和同一条保存路径，所以从这里保存与从表单保存是一回事：先校验，再应用
为一个新配置版本，有 store 时写入 store。没有 store 时，保存的修改只保留到 fit 从配置文件重新启动为止。

## 页面与操作

### 草稿

一个文本框，整份配置的 JSON；本页只有这两个标签，没有编辑部分配置的那些页面的 `.conf`、`.yaml`、`.json`
标签，也没有**导出**与**导入**按钮。这里的修改与各表单编辑的是同一份草稿：在表单里改过的字段在这里看得到，
在这里输入的 JSON 随输入即时解析成表单的树。文本必须是一个 JSON 对象。解析不了时，文本按原样保留，并指出错误
（“无法解析：…”），表单显示的仍是最后一次解析成功的值；在修正或放弃之前，所有页面都无法保存——其他页面会
提示“JSON 页面中的文本无法解析”。

**保存并应用**发送整份草稿（带 `If-Match` 的 `PUT /config`）：先加载与编译，再应用为一个新配置版本；发现的
问题带路径列出，每条链接到编辑它的页面。**检查**是同一请求带 `?dry_run=1`，只检查不应用，并说出哪些监听会被
新增或移除。读取之后配置被别处修改（`412`）时，页面会提示，并给出**放弃并重新读取**按钮：想保留这里的修改，
就先复制出来。保存栏显示配置版本，或**有未保存的修改**标记，旁边是**放弃修改**、**检查**与**保存并应用**，
没有未保存的修改时都不可用。

这一页的工具：

| 工具 | 作用 |
| --- | --- |
| **格式化** | 把文本重排成草稿的 JSON，缩进两个空格；文本无法解析时什么也不做 |
| **复制** | 复制文本 |
| **打开文件** | 把一个 `.json` 文件读进文本框，像手工输入一样解析；点**保存并应用**之前不会保存 |
| **下载** | 按当前文本原样保存（含未保存的修改），文件名为 `fit-config-<版本>.json`，版本是读取草稿时的版本 |

要导入 `.conf` 或 `.yaml` 文件，请改用[全局设置](07-global-settings.zh-CN.md)章导入 / 导出页的**导入**：
它会转换格式并立即应用。

### 生效配置

只读：基础配置加上各 provider 提供的内容，也就是 fit 实际跑的那一份；每次打开这个标签都从
`GET /config/effective` 读取。三块：

- **优先级**：从最先到最后，先是基础配置的来源，再是各 provider 按写的顺序。前面的赢。
- **来源**：生效配置里每个成员、每个数组元素来自哪里——路径、来源，以及它在 provider 提供的内容中的位置
  （基础配置的行这一列为空）。
- **生效配置 · 版本 N**：格式化后的 JSON，带**复制**按钮。

基础配置的来源是它最近一次修改的来处：`file`（配置文件）、`store`、`admin`（Admin API）、`console`（控制台
页面）或 `cluster`（管理多台 fit 的管理端）。回滚，以及证书文件变化后的重新加载，沿用被重新应用的那份配置的
来源；[版本](09-versions.zh-CN.md)页把这些版本的来源显示为 `rollback` 与 `certificates`。

被遮蔽的元素——某个 provider 提供了、但同名元素已被前面的来源取走——不在生效配置里，在概览页与一致性检查里
作为 info 出现。

生效配置显示的样子，一小段，其中有一个名为 `kv` 的 `etcd` provider：

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

优先级显示为 `console › kv`，旁边的来源表：

| 路径 | 来源 | |
| --- | --- | --- |
| `/listeners/0` | `console` | |
| `/listeners/1` | `kv` | `/listeners/0` |
| `/routes/0` | `console` | |
| `/upstreams/0` | `kv` | `/upstreams/0` |

`shop` 监听与 `backend` 上游不在草稿里：它们是 provider 提供的。在草稿里写一个名为 `shop` 的监听，它就从
“由 `kv` 提供”变成“由配置写出”，provider 那个同名元素随之被遮蔽。

## 参数

这一页编辑的是整份配置而不是某个成员，所以没有自己的字段表；各成员的表在配置参考的对应章里：
[The config](../reference/config.md#the-config)、[Listeners](../reference/config.md#listeners)、
[Routes](../reference/config.md#routes)、[Upstreams](../reference/config.md#upstreams)、
[Services](../reference/config.md#services)、[Providers](../reference/config.md#providers)。

优先级与合并规则：

| | |
| --- | --- |
| 基础配置 | 胜过每个 provider |
| provider | 胜过排在它后面的 provider；名字已被前面来源取走的元素被遮蔽 |
| 数组 | `listeners`、`routes`、`upstreams`、`services` 在各文件与各 provider 之间合并——provider 只提供这四类——`providers` 本身在各文件之间拼接；其余成员只能写在一处 |

这一页用到的接口（在控制台上位于 `/api/m/admin/` 之下，如 `/api/m/admin/config`）：

| 接口 | |
| --- | --- |
| `GET /config` | 草稿，即基础配置的 JSON，带 `ETag` 与 `Fit-Config-Version` |
| `GET /schema` | 生成表单所用的 JSON Schema |
| `PUT /config` | 保存整份配置，带 `If-Match`；带 `?dry_run=1` 时即**检查** |
| `GET /config/effective` | 生效配置，见下表 |

`GET /config/effective` 的应答：

| 字段 | 是什么 |
| --- | --- |
| `version` | 运行中配置的版本，显示在生效配置块的标题中 |
| `etag` | 基础配置的 ETag，而不是合并后的 |
| `precedence` | 各来源的顺序，基础配置的来源在前 |
| `config` | 生效配置的 JSON |
| `sources` | 每个成员与数组元素来自哪里：`path`（指向合并后配置的 JSON 指针）、`source`、`from`（指向 provider 所提供内容的 JSON 指针，基础配置没有） |
| `shadowed` | 被略去的元素：`source`、`from`、`name`，以及 `by`——保留下来的同名元素所属的来源 |

页面显示优先级、来源与 JSON；`shadowed` 显示在概览页与一致性检查中。

## 常见场景

- **一次改几个成员**：在这里改，或在几个页面上改，然后只保存一次；只产生一个版本。
- **从别处拿来的文件**：**打开文件**之后，文本必须能解析才能保存；解析不了时页面显示错误并保留原文。
  **保存并应用**之前先**检查**，可以看到它会做什么。
- **看 fit 实际跑的是什么**：生效配置标签包含 provider 提供的元素，这些不在草稿里；运行中的 fit 与生效配置的
  差别由[一致性检查](10-consistency-check.zh-CN.md)列出。
- **配置被别处改了**：遇到 `412` 时，想要的修改先复制出来，再点**放弃并重新读取**。

## .conf 配置

这是 JSON 页，这里没有 .conf 示例；整份配置的 .conf 写法见 [.conf 格式](../reference/fit.zh-CN.md)，
三种格式的对照见[三种格式](../configuration.zh-CN.md#三种格式)。上面的 JSON 就是本页显示的样子。

## 相关文档

- [配置](../configuration.zh-CN.md)：[三种格式](../configuration.zh-CN.md#三种格式)、
  [多个文件和目录](../configuration.zh-CN.md#多个文件和目录)、
  [修改运行中的配置](../configuration.zh-CN.md#修改运行中的配置)。
- [配置参考](../reference/config.zh-CN.md) 与 [.conf 格式](../reference/fit.zh-CN.md)；
  [Admin API](../reference/config.md#admin-api) 章有这些接口应答的完整说明。
- [控制台与 store](../console.zh-CN.md#页面)：这一页在菜单中的位置，以及草稿写入的
  [store](../console.zh-CN.md#store)。
- [全局设置](07-global-settings.zh-CN.md)章的导入 / 导出页：整份配置的 `.conf`、`.yaml`、`.json` 与 `.store`。
- 相邻章：[功能导航栏](03-navigation.zh-CN.md)、[版本](09-versions.zh-CN.md)、
  [一致性检查](10-consistency-check.zh-CN.md)。
