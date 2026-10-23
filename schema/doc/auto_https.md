HTTPS by default: a config that writes no listeners is served over HTTPS on 443, with certificates
obtained with [ACME](#acme) for the host names its routes match, and over HTTP on 80, which
redirects to HTTPS and answers ACME's HTTP-01 challenge. A listener written with an empty `tls`
gets the certificates of the routes it serves. `auto_https` turns it off, turns the redirects off,
or moves the ports. Names that no public CA certifies (`localhost`, IP addresses, `*.internal`,
wildcards) get none.

```caddyfile
{
	auto_https disable_redirects
	https_port 8443
}
example.com {
	respond "hello"
}
```

```json
{
  "auto_https": {"disable_redirects": true, "https_port": 8443},
  "routes": [{"match": {"host": ["example.com"]}, "handler": {"module": "respond", "params": {"body": "hello"}}}]
}
```
