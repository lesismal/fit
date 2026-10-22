# 009：provider 配置如何合并

[English](009-provider-merging.md) | [简体中文](009-provider-merging.zh-CN.md)

状态：已采纳，见 I56（2026-10-22）。

## 背景

从 I57 起，provider（Docker、Kubernetes、Consul、etcd）会在配置文件和 Admin API 写入的内容之外添加路由和上游。
路线图要求合并规则写成文字、能看得到：哪个来源优先，以及一个 Admin API 接口，展示生效配置和其中每一部分的来源。
I56 要决定 provider 能设置什么、两个来源用了同一个名字怎么办、错误如何报告，以及配置来自多处之后，Admin API 现有的
接口是什么含义。

## 决定

- **基础配置与片段。** 基础配置是文件、SIGHUP 和 Admin API 写入的那份；`GET /config`、其中的 JSON pointer、ETag、
  版本和回滚都仍然针对它。provider 发送的是片段：一个只含 `listeners`、`routes`、`upstreams` 和 `services` 的
  JSON 对象。其他设置（`access_log`、`limits`、`admin`、`acme`、`auto_https`、`metrics`）只来自基础配置，因此
  容器标签无法关掉 Admin API 的 token，也无法改变证书存放的位置。
- **优先级。** 基础配置优先于所有 provider，先添加的 provider 优先于后添加的（从 I57 起即配置中列出的顺序）。
  片段的元素加在基础配置的元素之后，按这个顺序逐个 provider 添加，每个片段内保持自己的顺序；基础配置的元素下标
  不变，所以无论有没有 provider，它的错误路径和 JSON pointer 含义都相同。
- **名字。** 有名字的元素（listener、upstream、service，或带名字的路由）如果名字已被排在前面的来源使用，就被略去，
  并列为被遮蔽，注明名字被谁占用。没有名字的路由不会冲突。同一来源中两个同名元素不做处理：配置自身的检查会拒绝它们，
  和写在文件里一样。名字不按 provider 加命名空间（Traefik 的 `name@docker`）：那需要改写模块参数中对它们的引用，
  而 fit 并不解读模块参数。
- **整体生效或不生效。** 合并后的配置作为一个整体检查、编译和应用，走所有配置都走的路径（`admin.Configs`，一次
  一个）。会让它无效的片段被拒绝，该 provider 之前的片段保留；会破坏 provider 所提供内容的基础配置同样被拒绝，
  例如删掉了某个 provider 的路由所引用的 listener。
- **错误指向写下它的地方。** 合并后的配置是基础配置的原文，把片段的元素按原样插入其中，因此每个错误都能映射回去：
  基础配置中的错误给出它在基础配置中的行列，provider 元素中的错误给出它在该 provider 片段中的路径和位置，并注明
  `(provided by <name>)`。
- **可见。** `GET /config/effective` 返回实际运行的配置、版本号和基础配置的 ETag、按优先级排列的来源、每个顶层成员
  和每个数组元素的来源（在合并配置中的 JSON pointer、它的来源，以及在 provider 片段中的 pointer），以及被遮蔽的
  内容。基础配置的来源是最后一次整体设置它的来源：`file` 或 `admin`，回滚之后则是被回滚到的那个版本的来源。
- **有界。** 片段最大 16 MiB。`provider.Run` 每个 provider 最多保留一个待应用的片段：等待期间又发来的片段替换它，
  所以变化快于配置应用速度的 provider 会跳过过时的内容。与该 provider 上一个片段相同的片段不会再次应用。provider
  和合并都在控制面自己的 goroutine 上运行。

## 影响

- provider 不能让基础配置中写明的东西停止服务，也不能接管运维写下的名字；想让 provider 拥有某个名字，就不要在
  基础配置中写它。
- 片段被拒绝的 provider 会继续提供它之前的片段，直到发来能加载的片段；错误在运行 provider 的地方报告。消失的
  provider 发送空片段。
- 每次 provider 更新都是一个新的配置版本，来源为该 provider；`GET /versions` 能看到它们，回滚恢复基础配置，
  并带上 provider 当前提供的内容。
