A line for each request, written to standard output, standard error or a file, through a
bounded buffer: when the output cannot keep up, lines past the buffer are dropped and counted
rather than holding requests up. Read when fit starts, but for `level`, which a reload changes.

`format` is how a line is written, with the same fields in each: `json`, the default, an object a
line; or, as fib writes its logs, `text`, every part as key=value (logfmt), or `line`, the time and
level, then the fields as key=value. A value with a space, a quote, an equals sign, a control
character or a byte that is not UTF-8 is quoted, so that a request cannot forge a line:

```text
json  {"time":"2026-10-19T07:39:56.123Z","listener":"web","route":"api","client":"192.0.2.1","proto":"HTTP/1.1","method":"GET",...}
text  time=2026-10-19T07:39:56.123Z level=INFO msg=access listener=web route=api client=192.0.2.1 proto=HTTP/1.1 method=GET ...
line  2026-10-19 15:39:56.123 INFO  access listener=web route=api client=192.0.2.1 proto=HTTP/1.1 method=GET ...
```

`json` and `text` write the time in UTC, `line` in local time.

A request's level is its response's status: info below 400, warn for 4xx and error for 5xx.
`level` writes the requests at that level and above: `warn` writes those answered with an error,
`error` those the server failed. A request below the level costs a comparison, and no line.

nginx's `access_log` and Caddy's `log` in a site convert to it (`fit adapt`).

```json
{
  "access_log": {"output": "/var/log/fit/access.log", "flush_interval": "1s", "level": "warn", "format": "line"},
  "routes": [{"match": {"host": ["example.com"]}, "handler": {"module": "respond", "params": {"body": "hello"}}}]
}
```
