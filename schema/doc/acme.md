How certificates are obtained and renewed with ACME, for HTTPS by default and for the names a
listener's `tls.acme` lists: the CA's directory (Let's Encrypt's by default), the account's
email, where accounts and certificates are kept (a directory, or Redis, etcd or Consul for
instances on several hosts, `shared_storage`), the challenges answered (HTTP-01 and TLS-ALPN-01),
and `on_demand`, which says whom to ask before a certificate is obtained during a handshake. Read
when fit starts.

```caddyfile
{
	email ops@example.com
	acme_ca https://acme-staging-v02.api.letsencrypt.org/directory
	storage file_system /var/lib/fit
	on_demand_tls {
		ask http://localhost:5555/allowed
	}
}
https:// {
	tls {
		on_demand
	}
	respond "hello"
}
```

```json
{
  "acme": {
    "email": "ops@example.com",
    "directory": "https://acme-staging-v02.api.letsencrypt.org/directory",
    "storage": "/var/lib/fit",
    "on_demand": {"ask": "http://localhost:5555/allowed"}
  },
  "listeners": [{"name": "https", "address": ":443", "tls": {"on_demand": true}}],
  "routes": [{"handler": {"module": "respond", "params": {"body": "hello"}}}]
}
```
