# 013: The Consul and etcd providers speak their HTTP APIs with net/http

[English](013-consul-and-etcd-over-http.md) | [简体中文](013-consul-and-etcd-over-http.zh-CN.md)

Status: accepted, in I62 (2026-10-22).

## Context

I62 takes services from a Consul catalog, and config from Consul's KV store and from etcd. Consul
has an official client, `github.com/hashicorp/consul/api`; etcd has `go.etcd.io/etcd/client/v3`,
which speaks gRPC. Both servers also serve what the providers need over HTTP and JSON: Consul's API
is HTTP only, and etcd serves a JSON gateway to its v3 API on its client port, on by default.

## Decision

- **`net/http`, no client libraries.** The Consul providers need `GET /v1/catalog/services`,
  `GET /v1/health/service/<name>?passing`, `GET /v1/health/state/any` and
  `GET /v1/kv/<prefix>?recurse`, each also as a blocking query (`index` and `wait`, the index from
  `X-Consul-Index`). The etcd provider needs `POST /v3/kv/range`, `POST /v3/watch`, whose answer is
  a stream of JSON objects, a line each, and `POST /v3/auth/authenticate`. That is a few hundred
  lines with the standard library. etcd's client brings gRPC, protobuf and etcd's API modules into
  the binary for three calls; Consul's brings HashiCorp's helper modules and a client whose
  blocking queries still have to be driven by hand.
- **Blocking queries as Consul's guidance has them**: an index that goes back starts over from 0,
  one below 1 is taken as 1, and queries of one watch start at least 1s apart, so that an index
  that changes all the time does not make a provider spin. The catalog provider polls the services
  and the health checks, and reads the catalog whole, debounced, after either changes, as Traefik's
  provider does, rather than holding a blocking query per service.
- **etcd's watch before its read**: a watch from the current revision is in place once etcd says
  it is created, which must come within `request_timeout`; the keys are read only then, so no
  change between the two is missed. A canceled watch, as after a compaction, starts over.
- **Secrets in files**: Consul's ACL token and etcd's password are read from files, each time a
  watch starts, so they are not written in the config, which the Admin API shows, and can be
  rotated. TLS to either takes a CA, and a client certificate and key, read the same way.
- **Bounded and in the control plane**: the providers run on goroutines of their own; an answer is
  read whole, at most 32 MiB, and a watch message is at most 32 MiB; at most `max_services` or
  `max_keys` are taken; a request takes at most `request_timeout`, beyond a blocking query's five
  minutes of waiting; a lost connection is retried 1s to 30s apart, on etcd's endpoints in turn.

## Consequences

- No new dependency in `go.mod`.
- etcd's gateway must be left on (`--enable-grpc-gateway`, its default); a cluster that turns it
  off cannot be read. etcd's v2 API and the `v3beta` paths are not supported.
- Consul Enterprise's namespaces and admin partitions are not asked for; they can be added as
  settings that become query parameters.
