A JSON line for each request, written to standard output, standard error or a file, through a
bounded buffer: when the output cannot keep up, lines past the buffer are dropped and counted
rather than holding requests up. Read when fit starts, but for `level`, which a reload changes.

A request's level is its response's status: info below 400, warn for 4xx and error for 5xx.
`level` writes the requests at that level and above: `warn` writes those answered with an error,
`error` those the server failed. A request below the level costs a comparison, and no line.

nginx's `access_log` and Caddy's `log` in a site convert to it (`fit adapt`).

```json
{
  "access_log": {"output": "/var/log/fit/access.log", "flush_interval": "1s", "level": "warn"},
  "routes": [{"match": {"host": ["example.com"]}, "handler": {"module": "respond", "params": {"body": "hello"}}}]
}
```
