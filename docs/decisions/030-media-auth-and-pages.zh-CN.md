# 030：媒体认证、流媒体角色与媒体页面

[English](030-media-auth-and-pages.md) | [简体中文](030-media-auth-and-pages.zh-CN.md)

状态：已接受（2026-11-03）。基于 [决定 029](029-webrtc.zh-CN.md)。

## 背景

用户要求 fit 的 WebRTC、流媒体以及其他接收媒体的协议都支持可配置的认证；fit 要自带基础页面，页面本身也受可配置的
认证保护；页面能开关麦克风和摄像头等基本功能，让对方看到开关状态，而不是因为收不到媒体而反复报错。

此前 `whip` 和 `whep` 只有路由的中间件可用：由 `jwt` 校验的 bearer token，或 `basic_auth`。这只覆盖 HTTP，无法说明
一个凭据能操作哪条流，也够不到没有路由的 RTMP、RTSP、SRT，以及页面的登录。一套按名称引用、所有接收媒体的处理器和
角色共用的策略，是这项工作三个部分的共同基础：I115（媒体认证，本决定的第一部分）、I116（流媒体角色）和 I117（媒体页面）。

## 决定：媒体认证（I115）

- **配置中的策略。** 顶层 `media_auth` 列出命名的策略。一条策略包括静态 `tokens`（token 本身，或其 `token_sha256`，
  使配置中不保存 token）、带 `password` 或 bcrypt `password_hash` 的 `users`、签发 token 用的 `secret`、无需凭据
  任何人都能进行的动作 `public`，以及签发的 token 的有效期 `token_ttl`。每个 token 和用户列出它可以 `publish` 和
  `play` 的流的模式；`*` 匹配任意一串字符（包括 `/`），空列表不授予任何权限。处理器和角色用 `auth` 指定策略；没有
  `auth` 的处理器像以前一样对所有人开放。
- **`mediaauth` 包。** `Policy.Allow(action, stream, credentials)` 返回主体，或一个错误，由 `Status` 映射为 401
  （没有或错误的凭据）、403（不允许）或 503（策略未加载，或 bcrypt 池已满）；`Check` 和 `Login` 增加 bcrypt 检查；
  `Password` 为 RTSP Digest 提供明文密码；`Issue` 签发 token；`Guard` 为 HTTP 请求完成以上全部，并自行应答拒绝。
  `FromHTTP` 依次读取 `Authorization: Bearer`、`Authorization: Basic`、查询参数 `token` 和 cookie `fit_media_token`；
  `FromQuery` 从没有请求头的协议的 URL 中读取 `token`，或 `user` 和 `pass`。
- **不阻塞 worker（规则 1）。** 静态 token 按其 SHA-256 查找，签名 token 校验 HMAC，明文密码以常数时间比较：都在
  worker 上立即完成。bcrypt 需要几十毫秒，所以 `password_hash` 在包自己的池上检查：CPU 数的四分之一，最多 256 个
  等待，最长等待 5 秒（规则 11）；请求被 retain 等待，由池继续处理。检查通过的密码以进程自己密钥下的 HMAC 记住，下次
  检查立即完成；策略中有哈希用户时，未知用户的密码也与一个诱饵哈希比较，使拒绝耗时相同。
- **签名 token。** 形如 `fm1.<payload>.<mac>`：payload 是主体、Unix 秒表示的过期时间以及 publish 和 play 模式的
  JSON 的 base64url，MAC 是对前缀和 payload 的 HMAC-SHA256，密钥由策略的 `secret` 和名称派生，所以 token 只对一条
  策略有效。没有 `secret` 时，用进程的随机密钥签名（供页面登录使用），重启后失效。`fit media-token` 打印 token，并拒绝
  没有 `secret` 的策略，因为运行中的 fit 无法校验它签发的 token。token 最长 4 KiB。
- **按 engine 和名称查找，原地修改。** 策略按 engine 保存，与 `webrtc.media` 的服务器相同，处理器在 `Provision` 中、
  角色在 `Apply` 中找到它们。`*Policy` 在重载之间保持不变：随每个配置应用的角色 `media_auth` 原地修改它，配置中已不
  存在的策略以 503 拒绝。处理器的 `Provision` 拒绝指向配置中不存在的策略的 `auth`；engine 上新出现的策略由正在编译的
  配置生成，使处理器在角色应用它之前即可服务；哈希未变的用户已记住的密码保留。
- **WHIP 和 WHEP。** `whip`、`whep` 和 `webrtc_proxy`（配合 `action`：`publish` 或 `play`）在读取 offer 的 body 之前
  按流名检查 POST，代理则在任何媒体服务器看到它之前检查：`401` 带 `WWW-Authenticate: Bearer realm="fit"`，凭据错误时
  带 `error="invalid_token"`（RFC 6750），或 `403`。会话的 URL（包括它的 DELETE）不需要凭据：RFC 9725 允许会话 URL
  作为凭证，其 ID 是只有创建者知道的 128 位随机数，而长时间推流期间过期的 token 也不应妨碍对端挂断。`webrtc.turn`
  保留自己的用户和 secret，`webrtc.media` 仍由它签发 TURN 凭据。
- **日志中没有 token。** token 从不写入日志；来自查询参数的 token 在请求 URI（访问日志写出的就是它）中被替换为
  `REDACTED`，主体写入访问日志的 `user`。

## 流媒体角色（I116）

由 I116 编写：fib 的 `media/hub`，带 RTMP、RTSP、SRT 监听和 HTTP 出口（HTTP-FLV、HLS、基于 hub 的 WHIP 和 WHEP），
每个都有 `auth`。

## 媒体页面（I117）

由 I117 编写：处理器 `media_pages`、它的登录、房间 API，以及麦克风和摄像头的开关。

## 影响

- 一套策略按流覆盖 fit 接收媒体的每个协议；路由的中间件仍先执行，所以策略可以放在 `jwt` 或 `forward_auth` 之后。
- 带明文密码的用户以明文保存在配置中，这是 RTSP Digest 所需要的；其他协议都可以用 `password_hash` 避免。
- token 在过期前无法单独吊销，只能更换策略的 `secret` 一并吊销；`token_ttl` 使其寿命保持较短。
- 没有新依赖：bcrypt 来自 fit 已有的 `golang.org/x/crypto`。
