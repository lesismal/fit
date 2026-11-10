# Global settings

[English](07-global-settings.md) | [简体中文](07-global-settings.zh-CN.md)

## What it does

**Global** is the menu entry, right after **Engine**, that edits the config's top-level members
that belong to no route, no listener and no role: fit's own log, the access log, telemetry, metrics,
profiling, limits, the engine and the providers. Its pages come in that order — **Log**, **Access
log**, **Telemetry**, **Metrics**, **Profiling**, **Limits**, **Engine**, **Providers**, then
**Other** and **Import / Export** — at `#/sys.config/<member>` (`#/sys.config/log`,
`#/sys.config/pprof`, …, `#/sys.config/other` and `#/sys.config/transfer`). Each of the first eight
edits the member it is named for (the `pprof` page is **Profiling**).

These pages work like every other page that edits the config ([the function navigation bar](03-navigation.md)):
the forms are made from the embedded fit JSON Schema, each field carries its description, an optional
member has a checkbox that sets it or leaves it out, and each page has `.conf`, `.yaml` and `.json`
tabs that edit its member as text, with **Export** and **Import** for that member beside them. All
pages edit one draft. **Save and apply** in the top bar sends it with `If-Match` — the whole draft,
or, from a text tab when nothing else is unsaved, only this page's member — and the server checks it
as `fit validate` does and applies it as a new config version; **Check** beside it is the same
request with `?dry_run=1`, which checks without applying.

What this chapter turns on is which changes take effect at once and which need a restart:

| member | takes effect |
| --- | --- |
| `log` | `level`, `levels`, the loggers' levels, `time_zone` and `rotate` on save; `output` and `format` at the next restart |
| `access_log` | `level`, `time_zone` and `rotate` on save; `output`, `flush_interval`, `buffer_bytes` and `format` at the next restart |
| `telemetry`, `engine`, `providers` | at the next restart |
| `limits` | `max_header_bytes`, `max_body_bytes` and `write_stall_timeout` on save; `max_conns`, `max_pending_bytes` and `max_write_queue_bytes` at the next restart |
| `metrics`, `pprof` | on save |

A save that changes something read only at start is applied, and its result lists a warning, such as
"engine changed; the change takes effect when fit restarts". The three `limits` that wait for a restart
warn the same way ("limits.max_conns changed; …"), and the [consistency check](10-consistency-check.md)
lists each one whose value differs from what the engine runs with, with a fix that sets the config back
to it.

## Pages

### Log (`log`)

One form, editing `log`: `level` and the per-logger `levels`, `output`, `format`, `time_zone` and
`rotate`, plus `loggers` (some loggers' own level, output, format and rotation); a logger may not have its
level both in `levels` and in `loggers`. fit's own log says what it thinks about starting, loading and
reloading configs, certificates, providers and the engine, and writes nothing for each request; the
loggers are `server`, `admin`, `tls`, `provider`, `telemetry`, `access_log`, `plugin`, `fastcgi`,
`engine` and the roles' own (`llm`, `tunnel`, `webrtc`, `sync`, `bittorrent`), and which is which, and
what each says, is in [the config reference's Log chapter](../reference/config.md#log). To see more
about one area, set its logger to `debug` in `levels` (as `tls debug` for certificates) and leave
`level` at `info`. The files on disk, rotating them now and "N lines dropped" are on the
[Logs](11-logs.md) page.

### Access log (`access_log`)

Edits `access_log`: `output`, `flush_interval`, `buffer_bytes`, `level`, `format`, `time_zone` and
`rotate`. Without `access_log` there is none. One line per request, as `json` (the default), `text`
(logfmt) or `line`. `level` filters by status: a response below 400 is info, a 4xx is warn and a 5xx
is error, so `warn` writes only the requests answered with 4xx and 5xx, and `error` only those
answered with 5xx; a request below the level costs one comparison and no line. When the buffer is
full the lines past it are dropped and counted, so a slow output never holds requests up. A file
output with no `rotate` grows without bound.

### Telemetry (`telemetry`)

Edits `telemetry`: the OTLP/HTTP `endpoint`, `headers_file`, `ca_file`, `cert_file`, `key_file`,
`service_name` and `timeout`, then `traces` (`sample_ratio`, `max_queue_bytes`, `interval`) and
`metrics` (`interval`). Without `traces` there are no traces, and without `metrics` nothing is exported
over OTLP; with `sample_ratio` 0 only the requests whose caller already sampled them are traced. This
page's settings are read when fit starts: after Save and apply they take effect at the next restart.

### Metrics (`metrics`)

`metrics` has no fields: ticking the member (or writing `metrics {}`) turns it on, counting and timing
requests by listener and route for the Admin API's `/metrics` in Prometheus's text format, which
serves fit's other metrics without it. It costs every request a little, so it is off unless written.
On the page there is that checkbox and the three text tabs.

### Profiling (`pprof`)

Edits `pprof`: `block_profile_rate`, `mutex_profile_fraction` and `max_duration`. Ticking the member
with no field set serves the profiles without sampling blocking or mutexes. Save and apply turns the
profiles on or off at once, with no restart. The console does not serve them: the note on the page
says they are served by the Admin API at its `/debug/pprof/` on its own address (`admin.address`,
`127.0.0.1:2019` by default), behind its token; without `pprof` those paths answer 404. For example:

```sh
go tool pprof http://127.0.0.1:2019/debug/pprof/profile?seconds=30
go tool pprof http://127.0.0.1:2019/debug/pprof/heap
```

`seconds` may ask for at most `max_duration` (1m by default). With `admin.token_file`, send the token
first, then open the file with `go tool pprof`:

```sh
curl -H "Authorization: Bearer $TOKEN" -o cpu.pprof \
  "http://127.0.0.1:2019/debug/pprof/profile?seconds=30"
go tool pprof cpu.pprof
```

Leave `pprof` out when done: the blocking and mutex rates are worth setting only while looking, since
sampling costs every such event.

### Limits (`limits`)

Edits `limits`: `max_conns`, `max_header_bytes`, `max_body_bytes`, `max_pending_bytes`,
`max_write_queue_bytes` and `write_stall_timeout`. These bounds apply to every listener, and each has a
default, so a config that writes none is still bounded. `max_pending_bytes` may be a number of bytes or
a share of the memory fit may use, such as `25%` (of the machine's, or of the container's limit where
that is lower). `max_conns`, `max_pending_bytes` and `max_write_queue_bytes` take effect at start, not
on a reload; at startup fit prints the limits it actually runs with and the settings of both engines.
A single route's body limit is set by the `body_limit` middleware.

### Engine (`engine`)

Edits `engine`: `event_loops`, `workers`, `min_workers`, `task_queue`, `poll_batch`,
`read_buffer_size`, `write_watermark`, `loop_wait` and `backlog`. fit runs two engines: the listeners'
serves clients, and upstream connections are dialed through the other, so that a response waiting for a
slow client does not hold up the workers that read upstreams; each takes these settings. What is left
out keeps fib's own default, so most configs write no `engine` at all; it is for tuning a busy server on
the host it runs on. The engines are made when fit starts, so a change needs a restart (saving warns
that it waits for one). This page is not the [Engine](06-engine.md) menu, which lists each listener port
and the features it serves.

### Providers (`providers`)

Edits `providers`: a list, one source each, named by `name` (its kind when left out): `docker`,
`consul`, `consul_kv`, `etcd` and `kubernetes`. Docker and Consul's catalog provide routes, upstreams
and services; Consul's KV store, etcd and Kubernetes listeners too — never other members such as `log`
or `limits`. The merge rules: what the config writes wins over every provider, and a provider over those
listed after it; a provider's element named as one before it is left out (the shadowed ones are info on
the Overview page and the consistency check). The effective config and where each part came from are on
the [JSON](08-json.md) chapter's Effective tab. This page's settings are read when fit starts.

### Other (`other`)

The members no other page edits are on this page, each one section of the form under its name:
`schema_version`, `access` and `media_library`. The page has one set of **UI**, `.conf`, `.yaml` and
`.json` tabs for all of them.

- `schema_version` is the version of the config the file is written for, `1`; a config that leaves it
  out is read as version 1, so it is rarely edited.
- `access` is the global allow and deny lists: every listener, route, service, role, the Admin API and
  the console go by it, and a more specific `access` decides first
  ([who may connect](../configuration.md#who-may-connect)). Take care not to deny the address you use
  the console from.
- `media_library` is the list of named libraries; it also has its own **Library config** page in the
  Media library module ([the media library](22-media-library.md) chapter).

`sync` and `bittorrent` have their own config pages, **Sync config** and **BitTorrent config**, so they are
not on this page: see [Sync](26-sync.md) and [BitTorrent](27-bittorrent.md). When every member has a page of
its own, this page says "Every member of the config has its own page."

### Import / Export (`transfer`)

The last page moves the whole config in and out, as two cards: **Export the whole config** and **Import
the whole config**.

**Export** opens a dialog with the format — `.conf`, `.yaml` (the default), `.json` or `.store` (fit's
encrypted fitstore) — and, for `.store`, a password: given, the file is sealed with it; left empty, it is
sealed with the running store's key and opens elsewhere with that store's key file or
`$FIT_STORE_PASSWORD` (without a running store a password is required). The file is named
`fit-config-v<version>.<format>`. The export is the config as it is saved and running, so changes not
saved yet are not in the file, and the dialog says so. A whole-config `.store` export also holds the
console users whose passwords changed, so that `fit run -c backup.store -admin` elsewhere restores both.

**Import** opens a dialog with the file, its format (taken from the extension) and, for `.store`, the
password it was sealed with (printable ASCII; empty for a file sealed with the running store's key).
**Import and apply** replaces the whole config (but `admin`) and applies it at once, through the save
path, as a new config version, written to the store when there is one; it discards unsaved changes,
after asking. The console users a `.store` file holds are not imported. A page that edits part of the
config has **Export** and **Import** buttons beside its format tabs for that part only: such an import
takes those members from the file, ignores the rest of it, and removes from the config a member the
file lacks. `admin` is never exported or imported. See
[importing and exporting the config](../console.md#importing-and-exporting-the-config).

## Key parameters

Only the most-used fields; the full tables are in the matching chapter of the config reference.

### log

| field | default | description |
| --- | --- | --- |
| `level` | `info` | The least level written, for every logger levels and loggers do not set one for: `debug`, `info`, `warn`, `error`; takes effect on reload |
| `levels` | | The least level of some loggers, by name, over `level`; takes effect on reload |
| `output` | | `stdout`, `stderr`, or the path of a file messages are appended to; left out, debug and info go to stdout and warn and error to stderr; takes a restart |
| `format` | `line` | `line`, `text` (logfmt) or `json`; each line carries the logger's name; takes a restart |
| `time_zone` | `local` | The zone the time of every line is written in, and the zone a file's `interval` turns at: `local`, `utc` or an IANA name; takes effect on reload |
| `rotate` | | Rotation and retention of the file: `max_bytes` (1 GiB), `interval` (off), `max_age` (720h), `max_files`, `max_total_bytes` (1 GiB), `compress` (on); left out, the file grows without bound |
| `loggers` | | Some loggers' own `level`, `output`, `format` and `rotate` |

Full table: [log](../reference/config.md#log), [log.rotate](../reference/config.md#config-rotate),
[log.loggers.*](../reference/config.md#config-logger).

### access_log

| field | default | description |
| --- | --- | --- |
| `output` | `stdout` | `stdout`, `stderr`, or the path of a file lines are appended to; takes a restart |
| `flush_interval` | `1s` | How long a line may wait in the buffer before it is written; takes a restart |
| `buffer_bytes` | 4 MiB | How many bytes of lines may wait; lines past it are dropped and counted; takes a restart |
| `level` | `info` | The least level of the requests written, by status: a 4xx is warn and a 5xx is error; takes effect on reload |
| `format` | `json` | `json`, `text` or `line`; takes a restart |
| `time_zone` | `local` | The zone a line's time is written in, and the zone the file's `interval` turns at; takes effect on reload |
| `rotate` | | The same as `log`'s `rotate`; takes effect on reload |

Full table: [access_log](../reference/config.md#access-log).

### telemetry

| field | default | description |
| --- | --- | --- |
| `endpoint` | `http://127.0.0.1:4318` | The OTLP/HTTP receiver; traces go to its `/v1/traces` and metrics to `/v1/metrics` |
| `headers_file` | | A file of header fields sent with each export, one `Name: value` a line, as an API key |
| `service_name` | `fit` | The service.name of the resource traces and metrics are about |
| `timeout` | `10s` | How long an export may take; a batch not sent by then is dropped and counted |
| `traces.sample_ratio` | 0 | The share of requests traced among those whose caller did not decide, 0 to 1 |
| `traces.max_queue_bytes` | 4 MiB | The most bytes of spans waiting to be exported; past it, spans are dropped and counted |
| `traces.interval` | `5s` | How often spans are exported, and sooner when 256 KiB of them wait |
| `metrics.interval` | `30s` | How often the metrics are exported |

Full table: [telemetry](../reference/config.md#telemetry).

### metrics

It has no fields: `{}` turns it on. It costs every request a little, so it is off when left out.
Full table: [metrics](../reference/config.md#metrics).

### pprof

| field | default | description |
| --- | --- | --- |
| `block_profile_rate` | 0 | The block profile: one event per this many nanoseconds spent blocked, on average; 1 for every one |
| `mutex_profile_fraction` | 0 | The mutex profile: one in this many contended mutexes; 1 for every one |
| `max_duration` | `1m` | The longest a profile's `seconds` may ask for; a longer request is refused with 400 |

Full table: [pprof](../reference/config.md#pprof).

### limits

| field | default | description |
| --- | --- | --- |
| `max_conns` | 100000 | The most client connections the listeners keep open at once, across all of them; one past it is closed as it is accepted; takes effect at start |
| `max_header_bytes` | 1 MiB | The largest request header a listener takes; a larger one gets 431 |
| `max_body_bytes` | 16 MiB | The largest request body a listener takes; a larger one gets 413, or ends the connection once it has begun to stream |
| `max_pending_bytes` | `50%` | The bytes of responses waiting for slow clients across every connection; bytes or a share of memory; takes effect at start |
| `max_write_queue_bytes` | 32 MiB | The bytes one connection may have queued for its client before fit closes it; takes effect at start |
| `write_stall_timeout` | `30s` | How long a response may wait for its client to take what it was sent, each time it waits |

Full table: [limits](../reference/config.md#limits).

### engine

| field | default | description |
| --- | --- | --- |
| `event_loops` | fib's: 1 on up to 4 CPUs, 2 on up to 16, the CPUs divided by 8 above that | How many event loops each engine waits on; Windows has one whatever this says |
| `workers` | 100 per CPU | The most workers each engine's pool runs on at once |
| `min_workers` | 0 | How many workers an idle pool keeps |
| `task_queue` | fib's default | How many tasks each pool holds waiting for a worker |
| `poll_batch` | 1024 | The most events one wait of an event loop collects |
| `read_buffer_size` | 64 KiB | The bytes a connection reads its socket into at a time, also the most a proxied body is passed on in one write |
| `write_watermark` | four read buffers, 64 KiB to 256 KiB | The bytes a connection may have waiting to be written before it stops reading: the backpressure on one connection |
| `loop_wait` | `park` | How an event loop waits with no event ready: `park` (Go's poller) or `block` (epoll_wait), Linux only; `block` shortens the latency tail near saturation over HTTP/1.1 at the cost of the median |
| `backlog` | the system's | The length of each listening socket's queue of connections not yet accepted |

Full table: [engine](../reference/config.md#engine).

### providers

| field | default | description |
| --- | --- | --- |
| `name` | its kind | The name the provider goes by in the effective config and in logs: lower-case letters, digits, `_`, `.` and `-`, unique |
| `docker` | | Routes, upstreams and services from running containers' labels; `endpoint` is `unix:///var/run/docker.sock` |
| `consul` | | Routes, upstreams and services from the tags of the services in a Consul catalog; `endpoint` is `http://127.0.0.1:8500` |
| `consul_kv` | | Listeners, routes, upstreams and services from the keys under `prefix` (`fit/`) in Consul's KV store |
| `etcd` | | Listeners, routes, upstreams and services from the keys under `prefix` (`fit/`) in etcd; `endpoints` are `http://127.0.0.1:2379` |
| `kubernetes` | | Listeners, routes, upstreams and services from the cluster's Gateway API resources and Ingresses; `controller_name` is `lesismal.github.io/fit` |

Full table: [providers](../reference/config.md#providers).

## In .conf

A complete config that passes `fit validate`, with every member this chapter's pages edit and a minimal
listener and route. `admin` is written only so that the file runs on its own: it belongs to the file fit
starts with, and the console never sets it.

```fit
schema_version 1

admin { address 127.0.0.1:2019 }

log {
	level info
	levels {
		engine warn
		tls debug
	}
	output /var/log/fit/fit.log
	format line
	time_zone Asia/Shanghai
	rotate {
		max_bytes 100MiB
		interval 24h
		max_age 720h
		max_files 30
	}
}

access_log {
	output /var/log/fit/access.log
	format json
	level warn
	time_zone Asia/Shanghai
	rotate { interval 24h; max_age 168h }
}

telemetry {
	endpoint http://otel-collector:4318
	service_name edge
	traces { sample_ratio 0.05 }
	metrics { interval 30s }
}

metrics {}

pprof { max_duration 2m }

limits {
	max_conns 50000
	max_header_bytes 64KiB
	max_body_bytes 16MiB
	max_pending_bytes 25%
	max_write_queue_bytes 32MiB
	write_stall_timeout 30s
}

engine {
	event_loops 4
	workers 512
	min_workers 8
	read_buffer_size 64KiB
	backlog 4096
}

provider { docker { endpoint unix:///var/run/docker.sock } }

access { deny 203.0.113.0/24 }

listener web :8080

route { handler respond { body "hello from fit" } }
```

## See also

- [The config reference](../reference/config.md): [Log](../reference/config.md#log),
  [Access log](../reference/config.md#access-log), [Telemetry](../reference/config.md#telemetry),
  [Metrics](../reference/config.md#metrics), [Profiling](../reference/config.md#pprof),
  [Limits](../reference/config.md#limits), [Engine](../reference/config.md#engine),
  [Providers](../reference/config.md#providers), [Access](../reference/config.md#access).
- [The console and the store](../console.md):
  [importing and exporting the config](../console.md#importing-and-exporting-the-config).
- [The config](../configuration.md): [who may connect](../configuration.md#who-may-connect); and the
  [.conf format](../reference/fit.md).
- The [templates](../../templates/README.md):
  [`00-runtime.conf`](../../templates/conf/conf.d/00-runtime.conf),
  [`30-observability.conf`](../../templates/conf/conf.d/30-observability.conf),
  [`50-providers.conf`](../../templates/conf/conf.d/50-providers.conf).
- The members with config pages of their own: [Sync](26-sync.md), [BitTorrent](27-bittorrent.md),
  [Media library](22-media-library.md), [Media auth](21-media-auth.md), [Streaming](20-streaming.md),
  [WebRTC](23-webrtc.md), [Tunnel roles](24-tunnel.md) and [LLM roles](25-llm.md).
- Neighbouring chapters: [the function navigation bar](03-navigation.md), [Overview](04-overview.md),
  [Engine](06-engine.md), [JSON](08-json.md), [Consistency check](10-consistency-check.md),
  [Logs](11-logs.md).
