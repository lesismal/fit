A config is one object. Its parts, each with a chapter below:

| Field | What it is |
| --- | --- |
| [`listeners`](#listeners) | The addresses fit listens on, and the protocols each serves. Left out, [HTTPS by default](#https-by-default) makes them from the routes' host names. |
| [`routes`](#routes) | Which requests go where: a match, middleware, and the handler that answers. |
| [`upstreams`](#upstreams) | The servers requests are proxied to, by name, with load balancing, limits, timeouts and health checks. |
| [`services`](#services) | Layer 4: connections of tcp listeners and datagrams of udp listeners proxied to an upstream. |
| [`access_log`](#access-log) | A JSON line for each request. |
| [`telemetry`](#telemetry) | OpenTelemetry traces and metrics, over OTLP. |
| [`limits`](#limits) | Limits on every listener: connections, header and body sizes, memory for responses. |
| [`admin`](#admin-api) | The Admin API, which reads and replaces the running config. |
| [`auto_https`](#https-by-default) | HTTPS by default, and its ports. |
| [`metrics`](#metrics) | Requests counted and timed by listener and route. |
| [`acme`](#acme) | How certificates are obtained: the CA, its account, storage, on-demand TLS. |
| [`providers`](#providers) | Routes, upstreams, services and listeners from Docker, Consul, etcd and Kubernetes. |

Nothing else is required: a config of a route alone, whose match names a host, is served over
HTTPS with a certificate obtained for that host. Names are matched exactly, an unknown field or a
value of the wrong type is an error, and so is a key written twice.

```json
{
  "routes": [
    {"match": {"host": ["example.com"]}, "handler": {"module": "respond", "params": {"body": "hello"}}}
  ]
}
```
