# 007：证书由 fit 自己管理，ACME 协议用 x/crypto/acme

[English](007-acme-client.md) | [简体中文](007-acme-client.zh-CN.md)

状态：已采纳，见 I28（2026-10-20）；依赖在 I29 引入。

## 背景

M2 用 ACME 申请和续期证书（I29–I31）：HTTP-01 和 TLS-ALPN-01 验证、到期前续期、多个实例共享的存储，以及在握手
时为事先不知道的名字签发证书。路线图允许 ACME 使用第三方库，但要放在 fit 自己的接口后面（原则 6）。候选如下，
版本为 2026-10-20 时的版本：

| | 是什么 | 给构建带来什么 |
| --- | --- | --- |
| CertMagic（`caddyserver/certmagic` v0.25） | Caddy 的证书管理器：缓存、存储、续期、按需 TLS、OCSP，基于 acmez | zap、miekg/dns、libdns、zerossl、blake3、cpuid、x/crypto、x/net |
| lego（`go-acme/lego/v4` v4.35） | ACME 客户端和命令行工具，同一个模块里带约 150 个 DNS provider | `go.mod` 中 57 个直接依赖，大多是云厂商 SDK |
| acmez（`mholt/acmez/v3` v3.1） | 精简的 ACME 客户端，支持 ACME 续期信息（ARI） | x/crypto、x/net；`go.mod` 中有测试用的 pebble |
| `golang.org/x/crypto/acme`（v0.58） | Go 团队的 RFC 8555 客户端：账户、订单、授权、HTTP-01 与 TLS-ALPN-01 辅助函数、finalize、吊销 | 只有它自己的包，只引用标准库 |

## 决定

- **证书由 fit 自己管理**，在 `tlsmgr` 中：哪些名字需要证书、何时续期、存储（`tlsmgr.Storage`，带锁的文件系统实现）、
  按需签发及其限制，以及握手如何找到它的证书（`tlsmgr.Manager`，在 `GetCertificate` 后面）。
- **ACME 协议用 `golang.org/x/crypto/acme`**：JWS 签名、nonce、目录、账户、订单、验证和 finalize。它是 fit 除 fib
  之外的第一个依赖，而它的包除标准库外什么都不需要。
- **它缺的部分由 fit 自己补上**：ARI（RFC 9773，一次无需认证的 GET，取得证书建议的续期时间窗）以及围绕它的调度。
- **不用 CertMagic**：它本身就是一个管理器，有自己的缓存、goroutine、存储接口和日志（zap）；fit 等于把一个管理器
  包在另一个里面，而且证书如何跟随配置重载，仍要 fit 自己管。**不用 lego**：它的模块带着所有 DNS provider 的 SDK，
  而 fit 的 HTTP-01 和 TLS-ALPN-01 用不到。**不用 `autocert`**：它为 net/http 设计，缓存是自己的，多个实例之间没有锁，
  而且在 `GetCertificate` 里申请证书。**暂不用 acmez**，尽管它最接近需求：ARI 小到可以自己写，而 x/crypto/acme 让构建
  只包含 Go 团队维护的模块。以后如果需要 DNS-01 或更多 ARI 细节，首先重新考虑 acmez。

## 影响

- ACME 客户端的调用会阻塞（它用 `net/http`），所以在管理器自己的有界池上执行，带超时，绝不在 fib 的 worker 上
  （规则 4）。握手的 `GetCertificate` 在 fib 的握手池上，只读内存；按需签发（I31）在那里等待，有自己的限制。
- HTTP-01 由 fit 的 HTTP 监听器从内存应答，TLS-ALPN-01 由 `GetCertificate` 为通过 ALPN 请求 `acme-tls/1` 的客户端应答
  （I30），这需要 fib 的 TLS 在 ALPN 中提供它。
- Let's Encrypt 已不再发布 OCSP 响应，所以 I30 的 OCSP stapling 只对其他 CA 有用。
- fit 的 `go.mod` 在 I29 引入 `golang.org/x/crypto`，`x/net`、`x/sys` 和 `x/term` 作为该模块的依赖一并出现；只构建
  `acme` 包。
