# 缓存

[English](19-cache.md) | [简体中文](19-cache.zh-CN.md)

## 功能

缓存按 RFC 9111 的规则保存响应：在内存中，设置了 `disk` 时也在磁盘上，按"缓存区"（zone）分组——同一个区
可被多条路由共享，其大小与键也共享，只要还有缓存在使用它，区就在重载之间保留；磁盘上的内容在 fit 启动时
被重新找到。缓存本身由路由上的 `cache` 中间件启用：这一页显示正在使用的区及其规模，并按 URL、标签或全部
清除。

一个键是一条响应的 scheme + host + target，也就是请求的 URL；`Vary` 点名的每个变体各有一个键。所以清除
卡片里的 URL 要写成绝对 URL，与请求写它的方式一致。响应按它自己的生命周期算新鲜（`s-maxage`、`max-age`、
`Expires`），没有生命周期时才用 `default_ttl`；写了 `must-revalidate`、`proxy-revalidate` 或 `s-maxage` 的
响应从不以过期状态应答。条件请求（`If-Modified-Since`、`If-None-Match`）从缓存直接应答。

标签来自 `Cache-Tag` 或 `Surrogate-Key` 头，写在响应上，清除时按标签整批移除。一个会改变目标的请求（不是
GET、HEAD、OPTIONS、TRACE、CONNECT）成功时，会让它目标的缓存失效。响应在全部送出之后才被保存：它经过一个
fib 的 body filter 复制，所以响应不会被为了存储而扣下。磁盘的读写在自己的有界协程池上，从不占用 fib worker。

## 页面与操作

### 缓存区（`#/gw.cache/zones`）

"缓存"模块在菜单里只有一个页面：缓存区。它不编辑配置，它读运行中的状态（Admin API 的 `GET /cache`）。

- **区表**（每 5 秒刷新）：每个区的名字、键数、内存字节、磁盘条目与字节、磁盘路径；每行一个**全部清除**
  按钮（确认后才执行）。没有缓存区时，页上说"没有使用中的缓存区：在路由中添加 cache 中间件"。
- **清除卡片**：选区（或"所有缓存区"），输入 URL（每行一个，绝对 URL，与请求一致）、标签（Cache-Tag /
  Surrogate-Key，每行一个），或勾选"清除缓存区中的全部内容"；三者至少要有一个，否则页上提示。全部清除需
  确认。结果给出清除的键数。
- 清除经 Admin API 的 `POST /cache/purge`（请求体 `{"zone", "urls", "prefixes", "tags", "all"}`），立即
  生效，不经过配置草稿，也不产生新版本。卡片上只有 URL、标签与全部；`prefixes` 是 API 才有的。

缓存是在路由上启用的：路由的表单里加 `cache` 中间件，在[反向代理](15-reverse-proxy.zh-CN.md)页编辑。引擎页
在这个端口下列出"缓存区"（链回本页）与"缓存的路由"（链到路由页）。

## 参数

路由的 `cache` 中间件（关键项）；完整字段表见
[配置参考：Middleware](../reference/config.md#http-middleware-cache)。

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `zone` | `default` | 响应保存在哪个区；同名的缓存共享区、大小与键，它在使用中时跨重载保留。 |
| `max_bytes` | `128 MiB` | 区在内存中最多保存的字节（体、字段与键都算）；超出时最久未用的响应被移出。同区的缓存取最后加载的那个大小。 |
| `max_entry_bytes` | `1 MiB` | 保存的最大响应体；更大的直接给客户端，不保存。最多为 `max_bytes` 的十六分之一。 |
| `default_ttl` | Last-Modified 的十分之一，最多一天 | 响应自身没有生命周期时（无 `s-maxage`、`max-age`、`Expires`）且状态允许假设（200、203、300、301、308、404、405、410、414、501）时认为新鲜的时长。 |
| `stale_while_revalidate` | 无 | 过期后仍可在后台重新获取的同时应答旧响应的时长（RFC 5861）。 |
| `stale_if_error` | 无 | 重新获取失败（无响应或 500、502、503、504）时仍可应答旧响应的时长（RFC 5861）。 |
| `disable_lock` | 关 | 默认时，未命中而另一请求正在获取同一响应的请求会等待其获取（最多 `lock_timeout`）并从缓存锁中应答，所以许多请求对同一响应只获取一次；打开后每个未命中的请求各自获取。 |
| `lock_timeout` | `5s` | 等待他人获取的最长时间，之后自己获取。 |
| `disk` | 无 | 区的磁盘层：内存中保存的响应同时写到磁盘，内存不再保存的从磁盘读回并重新放入内存；同区的缓存取最后加载的那个磁盘。 |

`disk` 里：`path` 必填（区独有，不存在时创建，启动时重新找到）、`max_bytes`（默认 `1 GiB`）、
`max_entries`（默认 `100000`，它界定索引占的内存，每个几百字节）。

## .conf 配置

一个监听、一个上游、一条使用缓存的路由。这是自包含的完整配置，能通过 `fit validate`：

```fit
listener web :8080 http/1.1 h2c

upstream app 127.0.0.1:9000

route {
	match { path /assets/* }
	middleware cache {
		zone assets
		max_bytes 256MiB
		default_ttl 10m
		stale_while_revalidate 1m
		stale_if_error 5m
		disk { path /tmp/fit-cache/assets; max_bytes 10GiB }
	}
	handler proxy { upstream app }
}
```

`fit validate` 会创建 `disk.path`，所以它得写在进程能建目录的地方；目录不存在时校验会报错。去掉 `disk`
就是纯内存的缓存。`max_bytes 256MiB` 与 `10GiB` 是 .conf 里允许的写法，JSON 里是字节数。

## 相关文档

- [反向代理](15-reverse-proxy.zh-CN.md)：路由与中间件，`cache` 在路由上的位置。
- [引擎](06-engine.zh-CN.md)：端口下把缓存区与缓存的路由列在何处。
- [一致性检查](10-consistency-check.zh-CN.md)：路由用了某个区而运行中没有对应的区属于 drift。
- [配置参考：Middleware](../reference/config.md#http-middleware-cache)：`cache` 的全部字段。
- [Admin API](../reference/config.md#admin-api)：`GET /cache` 与 `POST /cache/purge`。
- [模块文档 cache](../../schema/doc/modules/cache.md)：这一中间件的说明与示例。
- [完整配置模板](../../templates/conf/conf.d/60-apps.conf)：每个字段带注释。
