fit's own log: what it says about itself, as opposed to the [access log](#access-log)'s line for
each request. Each message comes from a logger and has a level, debug, info, warn or error; a
message below its logger's level is not written. The loggers are:

| Logger | What it says |
| --- | --- |
| `server` | listeners opened and closed, configs loaded and reloaded, and shutting down |
| `admin` | the Admin API's address, and configs it loaded |
| `tls` | certificates: obtained and renewed with ACME, OCSP responses, certificate files reloaded |
| `provider` | what each provider watches, and what it could not read or convert |
| `telemetry` | exports over OTLP that fail, and that work again |
| `access_log` | lines the access log dropped because its output could not keep up |
| `plugin` | what wasm plugins log, at the level each line asks for |
| `fastcgi` | what FastCGI applications write to their stderr |
| `engine` | fib, the network engine, and anything else that writes to Go's `log` and `log/slog`; fib's own startup lines are off, since fit prints its own |

`level` sets every logger's level, info when left out, and `levels` sets some loggers' own. A
change to either takes effect on reload; `output` and `format` take a restart.

Without `log`, messages at info and above are written as lines of text, `fit: ` and the message:
debug and info ones to standard output, warn and error ones to standard error, as fit has always
written them. `output` sends them all to one place. The `json` format writes an object a line,
with `time`, `level`, `logger` and `msg`, and the attributes the engine's messages carry. A
message is written when it is logged: fit says little about itself, and nothing for each request.

nginx's `error_log` and Caddy's global `log` convert to it (`fit adapt`).

```json
{
  "log": {"level": "warn", "levels": {"tls": "info", "engine": "error"}, "output": "/var/log/fit/fit.log", "format": "json"},
  "routes": [{"match": {"host": ["example.com"]}, "handler": {"module": "respond", "params": {"body": "hello"}}}]
}
```
