A JSON line for each request, written to standard output, standard error or a file, through a
bounded buffer: when the output cannot keep up, lines past the buffer are dropped and counted
rather than holding requests up. Read when fit starts.

```json
{
  "access_log": {"output": "/var/log/fit/access.log", "flush_interval": "1s"},
  "routes": [{"match": {"host": ["example.com"]}, "handler": {"module": "respond", "params": {"body": "hello"}}}]
}
```
