# 014：证书共享于 Redis、etcd 或 Consul，使用租约锁和共享的质询

[English](014-shared-certificate-storage.md) | [简体中文](014-shared-certificate-storage.zh-CN.md)

状态：已采纳，见 I63（2026-10-22）。

## 背景

I28 为证书管理提供了带锁的 `Storage`，I29 让 ACME 在获取证书前取得其中的锁，持有锁后再查看一次，使共享存储的实例对
每个证书只获取一次。`FileStorage` 只能在看到同一目录的实例之间共享。分布在多台主机上的实例需要一个都能访问的存储。
而且在那种部署中，CA 对质询的检查可能到达负载均衡器后的任何一个实例，而不是下单的那个。

## 决定

- **三种存储，位于 `tlsmgr/sharedstore`，由 `acme.shared_storage` 选择**：Redis（单机或主节点；不支持 Redis
  Cluster）、etcd 和 Consul 的 KV 存储，都放在一个键前缀（`fit/`）下。与决策 013 一样，都不引入客户端库：Redis 通过
  几个池化连接用 RESP2 访问，可选 TLS；etcd 和 Consul 用 `internal/apiclient` 走 HTTP，即 I62 的 provider 现在共用的
  辅助代码。
- **租约锁。** 锁持续 `lock_ttl`（30s，至少 10s），持有者每隔三分之一的时间续期：Redis 中用 `SET NX PX` 加随机
  token 设置的键，由校验 token 的脚本续期和删除；etcd 的 lease，键只在从未创建过时才在事务中写入，用 keepalive 续期，
  撤销 lease 来释放；Consul 中 TTL 相同的 session，session 被销毁或过期时删除该键。未释放锁就停止的实例，在 TTL 过后
  失去锁（Consul 的 session 最多要两倍 TTL）。等待者在其 context 允许的范围内每秒重试一次。
- **共享质询。** 使用共享存储时，ACME 在质询进行期间把应答保存在存储中，键为 `challenges/<CA>/http-01/<token>` 和
  `challenges/<CA>/tls-alpn-01/<name>`，结束后删除。实例收到内存中没有的质询时到存储中查找：HTTP-01 请求先
  `Retain`，查找在 ACME 自己的 goroutine 上进行，同时最多 16 个，每个限时 5s，绝不在 fib worker 上（规则 1）；
  TLS-ALPN-01 握手在 `GetCertificate` 中查找，fib 在其握手池上运行它，与按需 TLS 相同。可能构成其他键的 token 和名称
  在查找前即被拒绝。
- **只在有质询进行中时才查找。** 请求质询和发起握手对攻击者毫无成本，若每次都查找，就会占住握手 worker 并压垮存储。
  有质询进行中的实例同时在存储中保存一个标记 `acme/<CA>/challenges-in-progress`，其值为失效时间：两分钟之后，质询
  持续期间每 30s 重写一次，结束后任其自行失效。其他实例只在标记有效时才查找质询，且每秒最多读一次标记；没有任何实例
  在下单时（几乎总是如此），质询请求直接得到 404，握手直接得不到证书，都不查找。16 个查找槽位由两种质询共用，超出时
  握手立即得不到证书；查找未果的名称一秒内不再查找（最多记住 1024 个名称）；一个客户端（一个 IPv4 地址或一个 IPv6 /64）
  最多占 4 个 HTTP-01 槽位。下单的实例保存应答后等待 1.25s 再请 CA 验证，以越过各实例在应答写入之前读到的内容。标记中
  的时间与各实例自己的时钟比较，因此时钟相差超过约 90s 时可能错过质询。
- **其余仍是 ACME 原有的做法**：签发时持锁、持锁后再查看一次、每个实例根据证书算出相同的续期时间，以及账户密钥在
  其自己的锁下共享。

## 影响

- `go.mod` 不增加依赖。
- 使用共享存储时，`/.well-known/acme-challenge/` 下的请求如果其 token 既不在内存也不在存储中，会得到 404，而不像没有
  共享存储时那样交给路由：应答在 handler 返回之后才到来，此时请求已无法再路由。
- 不支持 Redis Cluster、Redis Sentinel、Consul Enterprise 的命名空间以及 etcd 的 v2 API；以后都可以作为新的种类或
  设置加入。
- 无法续期锁的持有者会报告并继续。如果锁在此期间过期，另一个实例可能取得它，并因尚无更新的证书而也去下单：结果是该
  名称有两张证书，存储的是后一张，而不是没有证书。
