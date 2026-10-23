# Migrating from Traefik

[English](migrating-from-traefik.md) | [简体中文](migrating-from-traefik.zh-CN.md)

fit takes from Traefik its providers, which turn Docker labels, a Consul catalog, KV stores and
Kubernetes resources into routes, and its middleware; it adds a static file server, a cache, and a
smaller footprint per connection. There is no converter; this guide maps Traefik's parts to fit's,
and the [config reference](reference/config.md) has every field.

## How the parts map

| Traefik | fit |
| --- | --- |
| entryPoints | listeners: `{"name": "web", "address": ":80", "protocols": ["http/1.1", "h2c"]}` |
| an entryPoint's `http.tls` | a listener's `tls` |
| HTTP routers | routes; `entryPoints` is a route's `listeners` |
| a router's `rule` | a route's `match.expr`, or `match`'s fields (below) |
| a router's `priority` | a route's `priority` |
| services, `loadBalancer.servers` | upstreams and their `addresses`, with the `proxy` handler |
| `loadBalancer.healthCheck` | the upstream's `health_check` |
| `loadBalancer.sticky.cookie` | the upstream's `sticky_cookie` |
| weighted services | the proxy's `upstreams`, by `weight` |
| mirroring services | the proxy's `mirror` |
| failover services | the proxy's `failover` |
| serversTransport (`rootCAs`, `serverName`, client certificates) | the upstream's `protocol` `h2` and `tls_ca_file`, `tls_server_name`, `tls_cert_file` |
| TCP routers with `HostSNI` | `tcp` listeners and services with `server_names`, passing TLS through |
| UDP routers | `udp` listeners and services |
| certificatesResolvers (ACME) | `acme`, with HTTP-01 and TLS-ALPN-01, and a listener's `tls.acme` for the names; HTTPS by default for routes with host names |
| `tls.certificates` | a listener's `tls.certificates` |
| `tls.options` with `clientAuth` | a listener's `tls.client_auth` |
| `accessLog` | `access_log` |
| `metrics.prometheus` | `metrics`, at the Admin API's `/metrics` |
| `tracing` (OpenTelemetry) | `telemetry` |
| `api.dashboard` | the Admin API's read-only dashboard, `/dashboard/` |

## Rules

fit's `match.expr` takes Traefik's syntax for the matchers it shares, backticks included:

```json
{"match": {"expr": "Host(`example.com`) && (PathPrefix(`/api`) || Method(`POST`))"}}
```

Its matchers are `Host`, `Path`, `PathPrefix`, `PathRegexp`, `Method`, `Header(name, value...)`,
`Query(key, value...)` and `RemoteIP` (Traefik's `ClientIP`). `HostRegexp` and `HeaderRegexp` are
not there: a host with a leading `*.` (one label) or `**.` (one or more) covers most uses.

## Middleware

| Traefik | fit |
| --- | --- |
| `StripPrefix`, `AddPrefix`, `ReplacePathRegex` | `rewrite`: `strip_prefix`, `add_prefix`, `regexp` and `replacement` |
| `RedirectScheme`, `RedirectRegex` | `redirect` |
| `Headers` | `headers` (CORS fields set as fields) |
| `BasicAuth` | `basic_auth` |
| `ForwardAuth` | `forward_auth` |
| `RateLimit` | `rate_limit` |
| `InFlightReq` | `concurrency_limit` |
| `Buffering` with `maxRequestBodyBytes` | `body_limit` |
| `CircuitBreaker` | `circuit_breaker` |
| `Compress` | `compress` |
| `Retry` | the proxy's `retry` |
| `PassTLSClientCert` | `client_cert` |
| `IPAllowList` | a route's `match.remote_ip`, beside a route that answers 403 |
| plugins | WASM plugins (`wasm`), or modules built in with `fit build` |

## Providers

| Traefik | fit's `providers` |
| --- | --- |
| file provider | the config file itself, applied again on change with `fit run -watch` |
| Docker | `docker`, with labels under `fit.` (below) |
| Consul Catalog | `consul`, from services' tags |
| Consul KV, etcd | `consul_kv`, `etcd`: keys holding JSON objects of listeners, routes, upstreams and services |
| Kubernetes Gateway API | `kubernetes`, which serves the Gateways of its GatewayClasses and their HTTPRoutes, GRPCRoutes, TLSRoutes, TCPRoutes and UDPRoutes |
| Kubernetes Ingress | `kubernetes`'s `ingress`, for fit's IngressClasses |
| Kubernetes CRDs (IngressRoute) | not there: use the Gateway API |

What the config file writes wins over every provider, and a provider over those listed after it;
the Admin API's `/config/effective` shows the result and where each part came from.

Docker labels look alike, under `fit.` rather than `traefik.`:

| Traefik | fit |
| --- | --- |
| `traefik.enable=true` | `fit.enable=true` |
| `traefik.http.routers.web.rule=Host(...)` | `fit.routes.web.host=a.example`, `fit.routes.web.path=/api/*`, or `fit.routes.web.expr=Host(...)` |
| `traefik.http.services.app.loadbalancer.server.port=8080` | `fit.port=8080` |
| `traefik.http.routers.web.middlewares=...` | `fit.routes.web.middleware=[{"module": "compress"}]`, as JSON |
| `traefik.docker.network=net` | `fit.network=net` |

## What works differently

- **One config.** Traefik splits static configuration (entryPoints, providers) from dynamic
  (routers, services, middlewares). fit has one config; its listeners can change on a reload, and
  `providers`, `admin` and `acme` take effect when fit restarts.
- **Routes pick by specificity**: `priority` first, then the most specific host and path, rather
  than the rule's length.
- **Not there**: DNS challenges, and so certificates for wildcard names from ACME; Traefik's own
  CRDs; HTTP/3 to upstreams; Traefik Hub.
