An upstream is a named set of servers, which the [proxy](#http-handlers-proxy) handler,
[`forward_auth`](#http-middleware-forward_auth) and [services](#services) send to.

- **Load balancing** (`load_balancing`): `round_robin` in turn; `weighted_round_robin` in turn by
  `weights`; `least_conn` to the address with the fewest requests under way; `random`; `p2c`, the
  better of two picked at random; `ip_hash`, `uri_hash`, `header_hash` and `cookie_hash`, the same
  address for the same client address, URI, header or cookie (`hash_key` names the last two).
  `sticky_cookie` keeps each client on one address with a cookie of fit's.
- **Connections**: at most `max_conns` to each address, `max_pending` requests waiting past them,
  `max_idle_conns` kept idle for `idle_timeout`.
- **Timeouts**: `connect_timeout`, `response_header_timeout` (until the response begins),
  `response_idle_timeout` (silence within a body) and `response_timeout` (the whole response).
- **Health**: passively, an address that fails `max_fails` requests within `fail_timeout` is out
  for `fail_timeout`; actively, `health_check` probes each address over HTTP or TCP.
- **Protocol**: `http/1.1`, `h2c`, or `h2` over TLS, with `tls_server_name`, `tls_ca_file` and a
  client certificate (`tls_cert_file`, `tls_key_file`).

```caddyfile
:8080 {
	reverse_proxy 10.0.0.1:8080 10.0.0.2:8080 {
		lb_policy least_conn
		health_uri /healthz
		health_interval 5s
		fail_duration 30s
		max_fails 3
		transport http {
			dial_timeout 3s
		}
	}
}
```

```json
{
  "listeners": [{"name": "web", "address": ":8080"}],
  "upstreams": [
    {
      "name": "app",
      "addresses": ["10.0.0.1:8080", "10.0.0.2:8080"],
      "load_balancing": "least_conn",
      "connect_timeout": "3s",
      "max_fails": 3,
      "fail_timeout": "30s",
      "health_check": {"path": "/healthz", "interval": "5s"}
    }
  ],
  "routes": [{"handler": {"module": "proxy", "params": {"upstream": "app"}}}]
}
```
