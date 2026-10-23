Counts and times requests by listener and route, for the Admin API's `/metrics` in Prometheus's
text format, which serves the rest of fit's metrics without it. It costs every request a little,
so it is off unless written, and needs `admin`.

```json
{
  "admin": {},
  "metrics": {},
  "listeners": [{"name": "web", "address": ":8080"}],
  "routes": [{"handler": {"module": "respond"}}]
}
```
