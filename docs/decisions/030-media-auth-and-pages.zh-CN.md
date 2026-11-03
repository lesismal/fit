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

- **一个 hub，一个角色。** 顶层的 `streaming` 在监听器的引擎上运行 fib 的 `media/hub`，就像 `webrtc.media` 运行它的 SFU：
  `rtmp`、`rtsp` 和 `srt` 是角色自己的监听器，即添加到引擎上的绑定；`webrtc` 是给 fib `media/rtc` 服务器用的 UDP 端口（fib 的
  `webrtc.Mux`）。路由经处理器访问 hub：`httpflv`（fib 的 `media/httpflv`）、`hls`（`media/hls`，fMP4 或 MPEG-TS，可选
  低延迟 HLS），以及 `stream_whip` 和 `stream_whep`，它们把路由的路径映射到 fib 的 WHIP 和 WHEP 服务器，再把其 `Location`
  映射回路由。处理器按引擎找到角色，就像 `whip` 找到 `webrtc.media`；hub 属于角色，所以流在路由重载后仍然存在。
  `webrtc.media` 仍是 `whip` 和 `whep` 的 SFU，与 hub 无关：两者既不共用端口也不共用流。
- **在各协议自己的钩子中认证。** RTMP：`OnPublish` 和 `OnPlay`，凭据在流名或 app 的查询参数中（`token`，或 `user` 和
  `pass`）。RTSP：`OnRequest`，DESCRIBE 和 PLAY 需要 `play`，ANNOUNCE 和 RECORD 需要 `publish`，其余方法属于前者开始的会话；
  凭据为 Digest（fit 用用户的明文密码 `Policy.Password` 检查）、`auth_methods` 提供时的 Basic，或 URL 查询参数中的 token
  （截至其后第一个 `/`，客户端在那里写轨道的 control）。fit 自己发出质询，而不用 fib 的 `Password`，因为后者要求每个请求都带
  `Authorization`，会拒绝查询参数中的 token。SRT：握手中的 `OnConnect`，`s=` 是 token，与 `u=` 一起时是密码；fit 自己的
  stream ID 解析器把资源的查询参数分出，使 `r=live/cam?token=T` 指 `live/cam`。HTTP：`mediaauth.Guard`，与 `whip` 相同。
- **同步钩子中的 bcrypt 密码。** 这三个钩子都在返回前作答，运行在 fib 的 worker 上。使用 `password_hash`、密码此前未被确认
  正确（`ErrPending`）的用户在这里被拒绝，表示请重试（RTMP 的消息如此说明，RTSP 返回带 `Retry-After: 1` 的 503，SRT 返回
  1402），同时 `Policy.Check` 在 mediaauth 的池中检查哈希；确认正确的密码被记住，下一次尝试立即通过。在检查期间保持握手需要
  fib 服务器所没有的异步钩子；拒绝一个会重试的客户端才能让 worker 保持空闲。token 和明文密码从不等待。
- **HLS 携带查询参数中的 token。** 用 `?token=` 获取的播放列表，其中的播放列表、分片、part、map 和预加载提示都带上该 token
  （一个改写播放列表 URI 的 `OnResponse` 钩子），因为不发送请求头的播放器（Safari、ffmpeg）按播放列表自己的相对 URI 获取它们；
  每个请求（包括分片）都会检查。
- **上限。** hub 的 `max_streams`、`max_players`（跨所有协议）和 `gop_cache_bytes`；每个监听器的连接数（`max_conns`，超出时
  在打开时关闭）、握手、空闲和会话超时；WebRTC 的会话数、连接超时和 offer 大小（最多 64 KiB，使 offer 如 fib 服务器读取的
  那样整体到达）；HLS 每路流每种格式保留的字节数。
- **重载。** 监听器的 `auth` 原地生效，其连接保留。监听器其他字段的修改会在其地址上重启该监听器并关闭其连接（两者共用端口时
  先关旧的；新的无法绑定时重新启动旧的）；hub 上限的修改会在一个新 hub 上重启一切。`hls` 的分片器按参数在配置之间共享，
  因此参数不变的重载保留播放列表的编号。
- **目前 RTSP 只走 TCP。** fib 的 RTSP 服务器用 `ListenUDP` 绑定 RTP 和 RTCP 的 UDP 端口，却无法移除它们，而在共享引擎上
  重启或停止都需要这一点；fit 向 fib 提出这一请求（A61），在此期间使用交织的 RTP。要求 UDP 的 SETUP 得到 461，ffmpeg 和
  多数播放器随后改用 TCP 重新建立。

## 媒体页面（I117）

- **一个处理器，资源嵌入。** `media_pages` 在它的 `prefix` 下提供首页、房间通话、推流页和播放页，HTML、脚本和
  样式编进二进制，应答带只允许本服务器和端点所在源的内容安全策略（CSP）：不用 CDN，离线或内网都不受影响。端点是
  URL 模板（`whip`、`whep`、`hls`、`links`，含 `{stream}`），所以页面可以用 fit 的 `whip` 和 `whep`、流媒体
  角色的处理器或别的服务器，而不依赖它们的代码。
- **登录。** `login` 用 `Policy.Login` 核对 `auth` 策略的用户（bcrypt 在 mediaauth 的池上，请求保持住），
  应答 `Policy.Issue` 按该用户自己的权限和策略的 `token_ttl` 签发的令牌：作为 cookie `fit_media_token`
  （`HttpOnly`、`SameSite=Strict`、TLS 上 `Secure`、`Path=/`，让其他路径上的端点也能拿到），也放在应答体中，
  供别的源上的端点使用，页面以 bearer 令牌发送。粘贴进页面的令牌用法相同。HTML 本身保持公开，因为登录就在
  其中；路由的中间件可以再加一层。
- **开关不重新协商。** 推流方总是提供一个音频和一个视频收发器（`sendonly`），不论有没有设备；开关就是
  `RTCRtpSender.replaceTrack`：设备的轨道，没有设备时的合成轨道（WebAudio 的静音，每秒 2 帧、写着名字的
  canvas 画面），或者关闭时什么都不发并释放设备。不重新协商，不重建 PeerConnection，也不必告诉 SFU：它转发
  收到的，收不到就不转发。
- **状态走房间 API，不靠媒体。** 每个成员通过处理器的房间 API 宣告 `{audio, video, audioDevice, videoDevice,
  screen, publishing}`，其他成员在每次变化时通过 Server-Sent Events 得到房间的成员。观看方按宣告显示头像、
  “麦克风已关”或“没有摄像头”，而不是从收不到的帧去猜；并且只在成员宣告推流后才播放它的流——推流方在 WHIP
  POST 得到应答之后才宣告；失败的 WHEP 请求最多重试 6 次，退避到 15 秒。用 SSE 而不是 WebSocket：只需要
  一个方向，它是普通的 HTTP，能穿过所有代理和中间件，`EventSource` 会自己重连，少量的写走带同样凭据的 `POST`。
- **权限。** 加入和关注房间需要 `<room>-*` 上的 `play`，这个名字按模式的方式核对（`room1-*` 或 `*` 的授权都
  匹配）；宣告推流需要 `<room>-<name>` 上的 `publish`，即该成员的 WHIP POST 需要的那路流。加入的应答带一个
  key（128 位随机数），成员的变更、心跳和离开都需要它，就像 WHIP 会话的 URL 是它的凭证；另一个 key 的成员占着
  的名字返回 `409`，直到那个成员离开。
- **有界，从不阻塞。** 房间在内存中，由一把只在操作 map 时持有的锁保护；房间数、每个房间的成员数、每个房间的
  事件流（成员数的两倍）、请求体以及没有心跳时成员的存活时间都有上限，各有默认值。事件流用 `WriteNoWait`
  写出；超过应答的上限时只为它保留最新状态，等 `OnWritable` 表示浏览器取走了上一份再发送，超过
  `stall_timeout` 就断开。房间按引擎和前缀保存，由服务它们的配置共享，所以重载会保留它们；被换下的配置的
  事件流在 drain 时结束，浏览器会重新关注房间。
- **SFU 保持原样。** 检查了 `webrtc.media` 面对静默和单一媒体类型的推流方：维持会话的是 ICE consent 而不是
  媒体，所以全部开关都关闭的推流方会保留，它的观看方也会；同时请求两种媒体的观看方，对只推音频的推流方，其
  视频被应答为 inactive；只请求一种未发送媒体的观看方得到 `422`。无需改动，现在有测试保证。

## 影响

- 一套策略按流覆盖 fit 接收媒体的每个协议；路由的中间件仍先执行，所以策略可以放在 `jwt` 或 `forward_auth` 之后。
- 带明文密码的用户以明文保存在配置中，这是 RTSP Digest 所需要的；其他协议都可以用 `password_hash` 避免。
- token 在过期前无法单独吊销，只能更换策略的 `secret` 一并吊销；`token_ttl` 使其寿命保持较短。
- 没有新依赖：bcrypt 来自 fit 已有的 `golang.org/x/crypto`。
