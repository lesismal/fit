Requires a client certificate on a route of a listener whose `tls.client_auth` mode is
`verify_if_given`, and passes its subject on.

```json route
{
  "match": {"path": ["/internal/*"]},
  "middleware": [{"module": "client_cert", "params": {"subject_header": "X-Client-Subject"}}],
  "handler": {"module": "proxy", "params": {"upstream": "app"}}
}
```
