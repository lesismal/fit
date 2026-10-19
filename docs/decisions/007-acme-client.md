# 007: Certificates managed by fit, with x/crypto/acme for the protocol

[English](007-acme-client.md) | [简体中文](007-acme-client.zh-CN.md)

Status: accepted, in I28 (2026-10-20); the dependency comes in with I29.

## Context

M2 obtains and renews certificates with ACME (I29–I31): HTTP-01 and TLS-ALPN-01 challenges,
renewal ahead of expiry, storage shared between instances, and certificates issued during the
handshake for names not known before. The roadmap allows a third-party library for ACME, behind an
interface of fit's own (principle 6). The candidates, at their versions of 2026-10-20:

| | What it is | What it brings into the build |
| --- | --- | --- |
| CertMagic (`caddyserver/certmagic` v0.25) | Caddy's certificate manager: cache, storage, renewal, on-demand TLS, OCSP, on top of acmez | zap, miekg/dns, libdns, zerossl, blake3, cpuid, x/crypto, x/net |
| lego (`go-acme/lego/v4` v4.35) | an ACME client and CLI, with about 150 DNS providers in the same module | 57 direct requirements in its `go.mod`, mostly cloud SDKs |
| acmez (`mholt/acmez/v3` v3.1) | a lean ACME client, with ACME Renewal Information (ARI) | x/crypto, x/net; pebble in its `go.mod` for tests |
| `golang.org/x/crypto/acme` (v0.58) | the Go team's RFC 8555 client: accounts, orders, authorizations, HTTP-01 and TLS-ALPN-01 helpers, finalize, revoke | its own package, which imports the standard library only |

## Decision

- **fit manages its certificates itself**, in `tlsmgr`: which names need certificates, when to
  renew, the storage (`tlsmgr.Storage`, with a file system implementation and locks), on-demand
  issuance and its limits, and how a handshake finds its certificate (`tlsmgr.Manager`, behind
  `GetCertificate`).
- **The ACME protocol is `golang.org/x/crypto/acme`**: JWS signing, nonces, the directory,
  accounts, orders, challenges and finalization. It is fit's first dependency besides fib, and its
  package needs nothing beyond the standard library.
- **What it lacks, fit adds itself**: ARI (RFC 9773, one unauthenticated GET for a certificate's
  suggested renewal window) and the scheduling around it.
- **Not CertMagic**: it is a manager in its own right, with its own cache, goroutines, storage
  interface and logging (zap); fit would wrap one manager in another, and still need its own for
  how certificates follow config reloads. **Not lego**: its module carries every DNS provider's
  SDK, which fit does not need for HTTP-01 and TLS-ALPN-01. **Not `autocert`**: it is made for
  net/http, keeps its own cache with no locks between instances, and obtains certificates inside
  `GetCertificate`. **Not acmez** for now, though it is the closest match: ARI is small enough to
  write, and x/crypto/acme keeps the build to modules the Go team maintains. If DNS-01 or more of
  ARI's details are wanted later, acmez is the first to look at again.

## Consequences

- The ACME client's calls block (they use `net/http`), so they run on the manager's own bounded
  pool with timeouts, never on a fib worker (rule 4). A handshake's `GetCertificate`, on fib's
  handshake pool, only reads memory; on-demand issuance (I31) waits there, with its own limits.
- HTTP-01 is answered by fit's HTTP listeners from memory, and TLS-ALPN-01 by `GetCertificate` for
  a client that asks for `acme-tls/1` (I30), which fib's TLS has to offer through ALPN.
- Let's Encrypt has stopped publishing OCSP responses, so I30's stapling helps with other CAs only.
- fit's `go.mod` gains `golang.org/x/crypto` in I29, with `x/net`, `x/sys` and `x/term` as
  requirements of that module; only the `acme` package is built.
