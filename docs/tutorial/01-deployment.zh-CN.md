# 部署

[English](01-deployment.md) | [简体中文](01-deployment.zh-CN.md)

## 功能

fit 是一个静态二进制加一个配置文件：装好它，写一份配置，`fit run` 就跑起来。有 store 时配置分在
两个文件里：一个明文的**引导文件**，只写 `admin`；一个用密码封存的 **store**，写其余的配置。控制台是
登录后看到的那个网页（[功能导航栏](03-navigation.zh-CN.md)）；有 store 时，从它做出的每次修改都会写入
store，重启后 fit 仍从 store 运行；没有 store 时，它像 Admin API 一样在内存中修改运行中的配置。

本章按顺序走一遍：安装；两个文件各装什么；`fit run -admin` 的首次启动；创建用户；从哪里访问控制台；
最后是 Docker、Helm 与 systemd 的打包要点。字段表不重复罗列，以链接指向
[配置参考](../reference/config.zh-CN.md)。

## 页面与操作

### 安装

有 Go 1.27.3 或更高版本时，`go install` 把 fit 构建到 `$(go env GOPATH)/bin`：

```bash
go install github.com/lesismal/fit/cmd/fit@latest
```

在源码目录中 `make build` 把产物放在 `bin/fit`；`fit version` 打印 fit、fib 与 Go 的版本，以及操作系统
与架构。

没有 Go 时，从 [GitHub Releases](https://github.com/lesismal/fit/releases) 下载预编译的二进制：
Linux（amd64、arm64、armv7、386）、macOS（amd64、arm64）、Windows（amd64、arm64）和 FreeBSD
（amd64、arm64），每个是一个 `.tar.gz`（Windows 为 `.zip`），SHA-256 校验和在 `checksums.txt` 里。
另外发布容器镜像 `ghcr.io/lesismal/fit`（打 `v<semver>` 标签时）；Helm chart 与 systemd unit 也在仓库
里。三种打包方式的细节（端口、volume、sysctl、values）都在[部署](../../deploy/README.zh-CN.md)里；本章只
记下与控制台有关的部分。

### 两个文件

文件保存不了打开它自身的密码，所以有 store 的配置分在两个文件里：

- **引导文件**，即 `fit run -c` 指定的文件，明文，可以是 fit 的任一格式。它包含 `admin`：Admin API、
  `admin.store`（store 在哪里、它的密码）和 `admin.console`（控制台监听在哪里、谁可以登录）。
- **store**，用密码封存。它包含除 `admin` 外的一切，即 fit 运行所依据的**基础配置**，provider 尚未
  合并进来。

第一次以这份配置启动时还没有 store：引导文件里的 `listeners` 和 `routes` 是它的初始内容，store 立即
写入。此后每次启动，store 被合并到引导文件之上，store 的值优先：对象逐个成员合并，store 中的列表或值
整体替换文件中的。只有文件设置的成员会保留，合并后的配置写回 store。要重新从文件开始，删除 store。写入先
写到 store 旁的临时文件（权限 0600），同步后重命名覆盖，所以崩溃只会留下旧的或新的 store，不会留下半个。

有 store 时，`admin` 从不进入 store，也从不显示：它在每个配置加载时放入，但不放入基础配置，所以 Admin
API 的 `GET /config` 和 JSON 页面里没有密码、哈希或密钥路径。带 `admin` 的配置被拒绝：`admin` 只来自
引导文件。`admin` 的设置（包括 store 和控制台的）在 fit 启动时读取，改它要重启。

store 打不开（密码错或某个字节被改）时 fit 不会启动，且不会被覆盖。所以做一份副本：`fit store export -c
<引导文件>` 以 JSON 输出 store 中的配置，`fit store import -c <引导文件> <文件>` 检查一份配置并重新封存
它，用新的 salt 替换原有内容——改 store 的密码也靠它；运行中的 fit 要到 SIGHUP 才看到 import 的结果。

### 首次启动：fit run -admin

`fit run -admin` 在没有相关配置时开启 Admin API 和控制台，用于首次启动，或从控制台配置一台机器：

```bash
fit run -admin                       # 有 fit.json 时使用它，否则不需要任何配置
fit run -admin -c site.yaml -timeout 30m
```

- 配置未设置的 `admin` 与 `admin.console` 使用默认值：Admin API 在 `127.0.0.1:2019`，控制台在
  `127.0.0.1:2020`。没有 `admin.console.users` 时，用户 `admin` 以密码 `admin` 登录。
- 该密码是**临时的**：必须在 `-timeout`（默认 `10m`，自启动时起算）内修改。修改之前，用它登录只能打开
  “修改默认密码”页面（见[登录](02-login.zh-CN.md)）。超过 `-timeout` 后它无法再登录，须以 `-admin` 重启
  fit 并及时修改。新密码不能是 `admin`，并保存在 store 中。
- 没有 `admin.store` 时，store 是配置文件旁扩展名为 `.store` 的文件（`site.yaml` 对应 `site.store`，
  `fit.json` 或无配置对应 `fit.store`），密码为 `$FIT_STORE_PASSWORD`，未设置时为 store 旁的**密钥文件**
  （store 路径加 `.key`，如 `site.store.key`），两者都不存在时以随机密钥创建，权限 0600。密钥文件应与
  store 一起保存：复制到别处的 store 用它打开。
- 只有 `admin.console`、没有监听和路由的配置是合法的，fit 可以在尚无服务内容时启动。
- `-c` 也接受一个 `.store` 文件（最多一个），单独给出或与配置文件一起：`fit run -c site.store` 运行 store
  中的配置，`fit run -c base.yaml -c site.store` 则按上面的规则把它合并到 `base.yaml` 上。配置文件旁的
  `.store` 只要存在就会使用，不论是否给出 `-admin`。

### 创建用户

用户是一个名字和一个 bcrypt 哈希；密码从不以明文写入。`fit hash-password` 输出密码的哈希：在终端中它
提示输入两次密码（输入显示为 `*`），否则从标准输入的第一行读取；它从不从参数读取密码，因为机器上的
其他用户能看到参数。`-cost` 设置 bcrypt 的代价（默认 10，每加一，检查耗时翻倍）。密码最多 72 字节，且
不能为空。同样的哈希也用于 `basic_auth` 的用户。

```bash
echo 'correct horse battery staple' | fit hash-password
```

把输出的哈希写进引导文件的 `admin.console.users`。`fit validate` 拒绝不是 bcrypt 哈希的
`password_hash`、空用户名和重复列出的用户名。登录后也可以在“账户”页改密码（见[账户](13-account.zh-CN.md)），
但改过的密码保存在 store 里，而 `admin` 只来自引导文件：用户本身及其初始哈希仍在文件中。没有 store 时
没有地方保存改动：页面会说明，接口答 `409`。

### 访问控制台

控制台默认在环回地址上，所以最常见的做法是从本机访问，或经 SSH 隧道把端口转过去：

```bash
ssh -L 2020:127.0.0.1:2020 host
```

在环回地址上 TLS 可选。**非环回地址需要 `cert_file` 和 `key_file`**，使密码从不以明文穿过网络；
`fit validate` 拒绝没有它们的配置。`access` 决定哪些客户端地址能连上控制台，它随每次应用的配置生效，
无需重启；被拒绝的连接一被接受就关闭。在其中写 `allow 127.0.0.1`（和 `::1`）可无论全局 `access` 如何
都保留本机访问。

### Docker、Helm 与 systemd

- **Docker**：镜像是 distroless 的 static 镜像，以 `nonroot`（65532）运行，只有 `/usr/local/bin/fit`
  和 `/etc/fit/fit.json` 处的占位配置（它在 8080 端口应答所有请求）。store 所在目录要放在 volume 里
  （写入方式是写一个新文件再改名覆盖），密码放在环境变量（`password_env`）或挂载的文件
  （`password_file`）中，而不是写在配置里。要从容器外访问控制台，给它非环回的地址加 `cert_file` 和
  `key_file`。1024 以下的端口由 sysctl `net.ipv4.ip_unprivileged_port_start=0` 放开，Helm chart 就是
  这样做的。
- **Helm**：chart 把 fit 作为 Gateway API 和 Ingress 的控制器运行，配置放在 ConfigMap 里；它的 values
  只管服务部分，不管控制台与 store。要一并运行它们，在 `config` 值中给出整份配置（保留 chart 的健康检查
  监听、路由与 Kubernetes provider），并自己为 store 提供 volume 与密码——chart 的文件系统是只读的，也没
  挂载其他 volume。
- **systemd**：`fit.service` 用于配置在 `/etc/fit/fit.json` 的 fit，`fit@<name>` 用于
  `/etc/fit/<name>.json`。fit 以系统用户 `fit` 运行，只带 `CAP_NET_BIND_SERVICE`，文件系统只读，例外是
  `/var/lib/fit`（`StateDirectory`，store 的 `path` 放这里）和 `/run/fit`（`RuntimeDirectory`）。启动前
  先用 `fit validate` 检查配置；`systemctl reload fit` 检查新配置后发 SIGHUP，校验不过的重载会失败、运行
  中的配置保持不变；SIGHUP 会重新读取 store，例如在 `fit store import` 之后。`systemctl stop fit` 发
  SIGTERM，fit 最多等 30s（`-shutdown-timeout`）让进行中的请求完成。

## 参数

`admin.store` 的字段（完整字段表见
[配置参考](../reference/config.zh-CN.md)的 [Admin API](../reference/config.md#admin-api) 章）：

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `path` | | 必填。store 的文件，写入时权限 0600。已存在且打不开时 fit 不会启动。 |
| `password` | | 密码，写在引导文件中。它保护的是被复制走的 store，而不是同一台机器上读取的 store。 |
| `password_file` | | 存放密码的文件，去掉首尾空白。 |
| `password_env` | | 存放密码的环境变量。 |

三者恰好设置一个；文件为空、环境变量未设置或为空时 fit 不会启动。store 的格式是 fit 自己的
`fitstore`：59 字节的头部加用 XChaCha20-Poly1305 封存的 JSON 载荷，密钥由密码经 Argon2id 派生，只在
启动时派生一次。载荷包含基础配置和从页面改过密码的控制台用户；若 store 还保存具名 section（如助手的设置与
模型密钥），它写成更新的版本，而导出从不包含 section。打不开的 store 不会被覆盖；丢失密码就丢失了 store，
所以用 `fit store export` 做一份副本。

`admin.console` 的字段在[登录](02-login.zh-CN.md)中列出。

## .conf 配置

一份完整的引导文件：`admin`（store 与控制台）加一个最小的监听和路由，作为 store 的初始内容。

```fit
# 引导文件：只有 admin 写在明文中，其余配置在 store 里。
admin {
	# Admin API：控制台在进程内调用它，两者走同一条应用路径。
	address 127.0.0.1:2019

	store {
		path /var/lib/fit/fit.store
		# password、password_file、password_env 恰好设置一个。
		password_env FIT_STORE_PASSWORD
	}

	console {
		address 127.0.0.1:2020
		users {
			username admin
			password_hash "$2a$10$sKGBAOOCI4p4WWJcVCuAH.ivT8pNy2DFbs6pd8xmtNEZ/5L1SeLvm"
		}
	}
}

# store 的第一份内容：这些是文件里的部分，store 写入后由它说了算。
listener web {
	address :8080
	protocols http/1.1 h2c
}

route hello {
	match {
		path /hello
	}
	handler respond {
		body "hello from fit\n"
	}
}
```

## 相关文档

- [部署](../../deploy/README.zh-CN.md)：容器镜像、Helm chart 与 systemd unit 的细节。
- [快速上手](../getting-started.zh-CN.md)：从零开始搭起一个站点。
- [控制台与 store](../console.zh-CN.md)：[两个文件](../console.zh-CN.md#两个文件)、
  [store](../console.zh-CN.md#store)、[fit run -admin](../console.zh-CN.md#fit-run--admin)、
  [用户与 fit hash-password](../console.zh-CN.md#用户与-fit-hash-password)。
- [配置](../configuration.zh-CN.md)与[.conf 格式](../reference/fit.zh-CN.md)。
- [登录](02-login.zh-CN.md)：下一章；[账户](13-account.zh-CN.md)：改密码；
  [功能导航栏](03-navigation.zh-CN.md)：登录后的页面。
- [决定 027](../decisions/027-console-and-store.zh-CN.md)：控制台与 store 的设计。
