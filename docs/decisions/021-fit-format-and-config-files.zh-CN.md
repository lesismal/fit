# 021：fit 自己的 .fit 格式，以及分散在多个文件中的配置

[English](021-fit-format-and-config-files.md) | [简体中文](021-fit-format-and-config-files.zh-CN.md)

状态：已采纳，见 I80（2026-10-23）。取代 [决定 020](020-caddyfile-and-jsonc.zh-CN.md) 的第一点，即以 Caddy 的 Caddyfile
作为 fit 的块语法。

## 背景

决定 020 通过读取 Caddy 自己的 Caddyfile 为 fit 提供了块语法。Caddyfile 只能表达 Caddy 能表达的内容，用的是 Caddy 的
指令：fit 的很多功能在其中没有指令（四层服务、缓存、限流、JWT、WASM、provider、telemetry、fit 的 limits），有的那些也要按
Caddy 的语义（一串处理器）转换成 fit 的路由，并列出存疑之处。用户要求的是一种外形像 Caddyfile 或 nginx 配置、内容却是 fit
自己的格式：JSON 和 YAML 有的字段一个不多、一个不少；其他服务器的配置只是文档和转换的事，不属于 fit 的格式。用户还要求
一份配置可以像 nginx 的 `include` 和 Caddy 的 `import` 那样分散在多个文件和目录中。

## 决定

- **fit 自己的格式 `.fit`，由配置的类型推导。** `adapter/fitconf` 读取由指令组成的块，每条指令是一个字段：标量取一个参数，
  列表取各个参数，对象或 map 取一个块，对象列表重复写块，模块写名字再跟参数块。有哪些字段、各是什么类型，来自配置解码器
  读取的 Go 类型和每个模块的参数，因此 fit 新增的字段或模块无需修改适配器就能用 `.fit` 书写，格式也不会与 JSON 和 YAML
  脱节。值按字段的类型读取，类型不对的值保留下来交给配置检查，在它所写的位置报错。
- **站点是唯一的简写。** `site <hosts...> { route <paths...> { ... } }` 把一组域名的路由及其共有的内容（listener、优先级、
  match、中间件）写在一起。它在转换前展开为普通路由，不带任何自己的语义；路由总可以逐条写出。顶层列表用单数书写，参数是
  元素的名字（`listener web { ... }`），使配置读起来像 nginx 和 Caddy 的配置。
- **写回。** `fitconf.FromJSON` 把原生 JSON 写成 `.fit` 文件，转换回来得到同样的配置，用于 `fit adapt -to fit`、`.fit`
  模板，以及配置参考中由 JSON 生成的示例，使它们不会不一致。
- **Caddy 和 nginx 的配置是被转换的，不按 fit 的配置读取。** `adapter/caddyfile` 和 `adapter/nginx` 保留，作为迁移工具：
  `fit adapt` 转换它们，最好转换成 `.fit` 文件；`fit run` 仍接受用 `-c` 指定的这类文件。配置文档和模板只讲 fit 的三种
  格式；Caddy 和 nginx 出现在迁移指南和转换 Caddyfile 的页面中。
- **多个文件和目录。** `-c` 可以重复，也可以指定目录；目录代表其中（递归）的 `.fit`、`.yaml`、`.yml` 和 `.json` 文件，按
  字典序，隐藏文件除外。每个文件单独转换，结果按 JSON 合并：顶层列表按顺序拼接，其他字段只能由一个文件给出（`schema_version`
  可以由多个文件给出，但必须一致），合并因此无需决定两个值中哪个胜出。合并后配置中的错误映射回文件、该文件内的路径和位置。
  文件监视器监视整组文件，包括加入和移除的文件，每次变化都重新读取全部文件，并有上限（10,000 个文件，64 MiB）。
- **没有新依赖。** 词法和语法分析器是 fit 自己的，对词数和嵌套有上限。

## 影响

- `fit run`、`fit validate` 和 `fit adapt` 按扩展名接受 `.fit` 文件，并接受多个 `-c`；`fit adapt -to fit` 写出 `.fit`
  文件；`fit schema -template fit` 打印模板。
- `templates/caddyfile` 已删除；`templates/fit/fit.fit` 与 YAML、JSON 模板一起生成。`examples/*.caddyfile` 改为
  `examples/*.fit`。
- 配置错误通过 `config.Error` 的 `File` 指出所在文件。
- Admin API 仍收发一份 JSON 配置；`-c` 读取的内容先合并，再交给它。

## 后续：扩展名改为 .konf

2026-10-24，该格式的扩展名先改为 `.conf`：按文件本身的特点（与 nginx、Caddy 一样由指令块组成）来取，不再与 fit 的名字绑定；
`.fit` 也是 Garmin 运动数据和 FITS 图像的扩展名。同一天又改为 `.konf`，以免 fit 的文件与 nginx 的或其他众多 `.conf` 文件混淆。
项目尚未发布，`.fit` 和 `.conf` 都不再读取。`fit run` 和 `fit validate` 只加载 fit 自己的格式（`.konf`、YAML、JSON）：nginx 和
Caddyfile 适配器不认扩展名和文件名，这两种配置用 `fit adapt -from nginx` 或 `-from caddyfile` 转换成 fit 的格式之一。该格式仍叫
fit 格式，`-to fit` 仍写出它，包仍是 `adapter/fitconf`。上面各节保留当时的名字。
