# 014: Certificates shared in Redis, etcd or Consul, with leased locks and shared challenges

[English](014-shared-certificate-storage.md) | [简体中文](014-shared-certificate-storage.zh-CN.md)

Status: accepted, in I63 (2026-10-22).

## Context

I28 gave certificate management a `Storage` with locks, and I29 made ACME take a lock in it before
obtaining a certificate and look in it again once it holds the lock, so that instances sharing the
storage obtain each certificate once. `FileStorage` shares only between instances that see one
directory. Instances on several hosts need a store they all reach. And there, the CA's check of a
challenge may reach any instance behind the load balancer, not the one that ordered the certificate.

## Decision

- **Three stores, in `tlsmgr/sharedstore`, chosen by `acme.shared_storage`**: Redis (a server or
  a primary; not Redis Cluster), etcd and Consul's KV store, each under a key prefix (`fit/`).
  Like decision 013, none brings a client library: Redis is spoken in RESP2 over a few pooled
  connections, with optional TLS, and etcd and Consul over HTTP with `internal/apiclient`, the
  helpers I62's providers now share.
- **Leased locks.** A lock lasts `lock_ttl` (30s, at least 10s) and its holder renews it every
  third of that: a Redis key set with `SET NX PX` and a random token, renewed and deleted by
  scripts that check the token; an etcd lease, the key put in a transaction only if it was never
  made, renewed with a keepalive and revoked to give it back; a Consul session with that TTL,
  which deletes the key when it is destroyed or expires. An instance that stops without giving a
  lock back loses it once the TTL passes (Consul's sessions take up to twice it). A waiter tries
  again every second, within its context.
- **Challenges shared.** With a shared storage, ACME keeps each challenge's answer in the store
  while it is under way, as `challenges/<CA>/http-01/<token>` and
  `challenges/<CA>/tls-alpn-01/<name>`, and removes it afterwards. An instance asked for one it
  does not have in memory looks it up there: an HTTP-01 request takes `Retain` and the lookup runs
  on ACME's goroutines, at most 16 at once and each within 5s, never on a fib worker (rule 1); a
  TLS-ALPN-01 handshake looks it up in `GetCertificate`, which fib runs on its handshake pool, as
  on-demand TLS does. Tokens and names that could make other keys are refused before any lookup.
- **Everything else is ACME's as it was**: the lock around issuance, the second look once it is
  held, renewal times that every instance computes alike from the certificate, and the account's
  key shared under a lock of its own.

## Consequences

- No new dependency in `go.mod`.
- With a shared storage, a request under `/.well-known/acme-challenge/` whose token is in neither
  memory nor the store gets a 404, rather than going to the routes as it does without one: the
  answer comes after the handler returned, when the request can no longer be routed.
- Redis Cluster, Redis Sentinel, Consul Enterprise's namespaces and etcd's v2 API are not
  supported; any of them can be added as another kind or setting.
- A holder that cannot renew its lock reports it and goes on. If the lock expires meanwhile, another
  instance may take it and, finding no newer certificate yet, order one too: two certificates for
  the name, the later one stored, rather than none.
