An HTTP CONNECT proxy for the clients `clients` lists, to the destinations `allow` and `deny`
permit; a listener's `socks` does the same for SOCKS5.

```json route
{
  "match": {"method": ["CONNECT"]},
  "handler": {"module": "forward_proxy", "params": {
    "clients": ["10.0.0.0/8"],
    "allow": ["*.example.com"],
    "ports": [443]
  }}
}
```
