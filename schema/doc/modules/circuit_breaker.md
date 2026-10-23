Answers 503 at once while too many of a route's requests fail or are slow, then lets a few
through to try again.

```json route
{
  "middleware": [{"module": "circuit_breaker", "params": {
    "window": "10s", "error_ratio": 0.5, "slow_threshold": "2s", "open_duration": "30s"
  }}],
  "handler": {"module": "proxy", "params": {"upstream": "app"}}
}
```
