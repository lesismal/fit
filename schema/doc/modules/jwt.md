Verifies a JSON Web Token, from the Authorization field, another field, a cookie or the query,
with keys from a JWK set, files or a secret, and passes claims on as fields.

```json route
{
  "match": {"path": ["/api/*"]},
  "middleware": [{"module": "jwt", "params": {
    "jwks_url": "https://auth.example.com/.well-known/jwks.json",
    "issuer": "https://auth.example.com/",
    "audience": ["api"],
    "claims_headers": {"X-User": "sub"}
  }}],
  "handler": {"module": "proxy", "params": {"upstream": "app"}}
}
```
