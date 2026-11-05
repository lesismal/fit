A line for each request, written to standard output, standard error or a file, through a
bounded buffer: when the output cannot keep up, lines past the buffer are dropped and counted
rather than holding requests up. A file output is queued to a file sink of fit's own, which writes
on a goroutine of its own, off the workers. Read when fit starts, but for `level`, `time_zone` and
`rotate`, which a reload changes.

`format` is how a line is written, with the same fields in each: `json`, the default, an object a
line; or, as fib writes its logs, `text`, every part as key=value (logfmt), or `line`, the time and
level, then the fields as key=value. A value with a space, a quote, an equals sign, a control
character or a byte that is not UTF-8 is quoted, so that a request cannot forge a line:

```text
json  {"time":"2026-10-19T07:39:56.123Z","listener":"web","route":"api","client":"192.0.2.1","proto":"HTTP/1.1","method":"GET",...}
text  time=2026-10-19T07:39:56.123Z level=INFO msg=access listener=web route=api client=192.0.2.1 proto=HTTP/1.1 method=GET ...
line  2026-10-19 15:39:56.123 INFO  access listener=web route=api client=192.0.2.1 proto=HTTP/1.1 method=GET ...
```

`time_zone` is the zone a line's time is written in: `local`, the default, `utc`, or an IANA zone
such as `Asia/Shanghai`; it is also the zone the file's `interval` turns in, so `24h` turns at local
midnight by default. Before this setting `json` and `text` wrote UTC and `line` local; now every
format writes this zone.

`rotate` rotates the file and removes its old rotated files, as `log`'s does: `max_bytes` (1 GiB)
and `interval` (off) decide when it turns, a rotated file is named after the rotation in UTC and
gzipped when `compress` is on, and `max_age` (720h), `max_files` (none) and `max_total_bytes` (1 GiB)
remove the oldest rotated files. Left out, the file grows without bound, and fit says so at startup.
The file at one path may be fit's own log's too, with the same `rotate` and `time_zone`.

A request's level is its response's status: info below 400, warn for 4xx and error for 5xx.
`level` writes the requests at that level and above: `warn` writes those answered with an error,
`error` those the server failed. A request below the level costs a comparison, and no line.

nginx's `access_log` and Caddy's `log` in a site convert to it (`fit adapt`).

```json
{
  "access_log": {"output": "/var/log/fit/access.log", "flush_interval": "1s", "level": "warn", "format": "line",
    "time_zone": "local", "rotate": {"max_bytes": 1073741824, "interval": "24h", "max_age": "720h", "compress": true}},
  "routes": [{"match": {"host": ["example.com"]}, "handler": {"module": "respond", "params": {"body": "hello"}}}]
}
```
