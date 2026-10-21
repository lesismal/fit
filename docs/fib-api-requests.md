# APIs fit needs from fib

[English](fib-api-requests.md) | [简体中文](fib-api-requests.zh-CN.md)

A review of M0 and M1 against the [development rules](rules.md) found places where fit cannot
follow them with fib v1.3.0's API alone. This document lists what fit asks of fib, why, and how fit
would use each API. The names and signatures are suggestions; fib decides the final shape. Since
R07e, fit's sessions may make the change in fib themselves ([rule 15](rules.md#7-when-fib-falls-short)).

The fit iterations that depend on these are R03–R06 and R07c in the [development plan](dev-plan.md#m1-review-fixes).

## Summary

| ID | API | Status | Rules it unblocks | fit iterations |
| --- | --- | --- | --- | --- |
| A1 | `ClientResponse.HoldBody(hold bool)` | done, v1.4.0 | 1, 8 | R04 |
| A2 | A response write that never waits, and a writable notification | done, v1.4.0 | 1, 8, 9 | R04, R05 |
| A3 | Asynchronous file sending | done, v1.4.0 | 1, 4 | R05 |
| A4 | Engine connection limit | done, v1.4.0 | 11 | R06 |
| A5 | Client limit on requests waiting for a connection | done, v1.4.0 | 11 | R06 |
| A6 | Trailers set on a `BodyWriter` before it closes | done, v1.4.0 | 1, 4 | R04 |
| A7 | A per-request value slot on `Context` | done, v1.4.0 | 12 | R07c |
| A8 | Documentation fixes | done, v1.4.0 (the rest not needed) | — | — |
| A9 | Refuse a header field name that is not a token | done, v1.4.0 | 13 | — |
| A10 | Stream a client response of unknown length from its header | done, v1.3.1 | 5 | — |
| A11 | The body of a client response that arrived whole, without a copy | done, v1.4.0 | 12 | R07c |
| A12 | Fewer timers and contexts per client request | done, v1.5.0 | 12 | R07c |
| A13 | No header copy for a response that only `OnFinish` hooks | done, v1.4.0 | 12 | R07c |
| A14 | `Release` on a Context that has no connection | done, v1.5.1 | 13 | R05 |
| A15 | `MaxPendingPerHost` not to count requests whose connection is being dialed | done, v1.5.1 | 11 | R06 |
| A16 | A file queued by `SendFile` counted in a connection's unsent output | done, v1.5.4 | 10, 11 | R05, R07e |
| A17 | Cheaper `OnFinish` and `SetValue` on every request | done, v1.5.4 | 12 | R07d, R07e |
| A18 | A response's status, and when its request arrived, without a hook | partly, v1.5.4 | 12 | I34, R07e |
| A19 | A streamed request's `Trailer` set where its end is handed over | done, v1.5.3 | 13 | I35 |
| A20 | Client requests written and response heads read without net/http | done, v1.5.6 | 12 | R07f |
| A21 | One `epoll_ctl` per park of an event loop, not two | not done: `EPOLLONESHOT` cannot save the call | 12 | R07f |
| A22 | A client connection's idle clock without a timer reset per request | done, `7f632cb` (main after v1.5.6) | 12 | R07f |
| A23 | `OnClose` for the connections `Engine.Close` cuts | done, v1.5.6 | 11, 13 | — |
| A24 | A new taskpool ring that writes none of its cells | done, `a6d78cd` (main after v1.5.6) | 11 | — |
| A25 | A per-request limit on a request body, streamed or not | done, `cfb4f09` (main after v1.5.6) | 7, 11 | I39 |
| A26 | More than one `OnCancel` callback on a Context | done, `cfb4f09` (main after v1.5.6) | 1, 12 | I39 |
| A27 | A hook that transforms a response body as it is written | done, `fc7ec5e` (main after v1.5.6) | 5, 7, 11 | I46 |
| A28 | Upgrade a request the handler retained, after it returned | done, `929924e` (main after v1.5.6) | 1, 9 | I47, I48 |
| A29 | A client request whose 101 hands over its connection as a tunnel | done, `929924e` (with A28) | 1, 9 | I47 |
| A30 | Extended CONNECT from the HTTP/3 client | done, `929924e` (with A28) | 13 | I48 tests |
| A31 | A callback when an HTTP/2 or HTTP/3 tunnel's unsent output drains | open | 1, 8, 9 | I48 |

## Evidence

The download direction of fit's proxy calls `Context.Write` from `ClientResponse.OnBody`'s function
(`handler/proxy/exchange.go`, `exchange.onBody`). For HTTP/1 and HTTP/2 upstreams that function runs
on the worker that reads the upstream connection, and `Write` waits once 64 KB are queued for the
client. fit leaves `Config.WriteTimeout` at its default of 0, so the wait has no limit.
Measured against fit at commit `511f64b`, with fib v1.3.0:

| Setup | One or more clients ask for a 200 MB download and never read | Other requests to the same upstream |
| --- | --- | --- |
| h2c upstream, `max_conns: 1` | 1 client | time out (5 s), still after 7 s; served again only once the stuck client disconnects |
| HTTP/1.1 upstream, `max_conns: 4` | 8 clients | time out (5 s), still after 7 s; served again only once they disconnect |
| HTTP/1.1 upstream, no `max_conns` | 512 clients | served (fib's adaptive pool adds workers), at the cost of a blocked goroutine and an upstream connection per stuck client, without limit |

`response_idle_timeout` (2 s in these runs) does not help: it cancels the upstream request, but the
goroutine stays in `Write`.

## A1: `ClientResponse.HoldBody(hold bool)`

**Status.** Done in fib v1.4.0 (`b520601`): `ClientResponse.HoldBody(hold bool)` as asked, on
HTTP/1, HTTP/2 and HTTP/3. Delivery resumes on fib's side pool. While held, the end of the body
waits too, but an error is delivered at once; a whole response is held before its one call.

**Problem.** A client response body can only be paced by its `OnBody` function blocking ("what holds
the connection back is fn itself"). On HTTP/2 the function runs on the worker that reads the shared
connection, so one slow consumer stalls every stream on it.

**Request.** The mirror of `Context.HoldBody` for client responses:

```go
// HoldBody pauses, with hold set, the delivery of the body to OnBody's
// function, and resumes it without. While held, what arrives waits in the
// client's buffer: HTTP/1 stops reading the connection once
// StreamResponseBodyBuffer waits; HTTP/2 and HTTP/3 stop returning window for
// this stream only, so other streams on the connection go on. Resuming
// delivers on the engine's handler pool, never on the caller's goroutine, so
// HoldBody may be called from inside the function and from anywhere else.
func (c *ClientResponse) HoldBody(hold bool)
```

**fit's use.** In `onBody`, after a write that leaves the client's queue above its limit:
`resp.HoldBody(true)`, then resume from the A2 notification.

## A2: A response write that never waits, and a writable notification

**Status.** Done in fib v1.4.0 (`24da9bb`): `Context.WriteNoWait(p) (above bool, err error)` and
`Context.OnWritable(fn func(error)) bool`, as asked, one fn at a time. HTTP/1 uses
`Connection.NotifyOutput`, HTTP/2 its stream windows, and HTTP/3 a new `http.ResponseNotifier`
interface; an external stream without it falls back to `Write`. `OnWritable` reports false before
the response streams, since small writes are held back and never above the watermark.

**Problem.** `Context.Write` waits when 64 KB are queued (HTTP/1) or held back by flow control
(HTTP/2, HTTP/3). There is no way to write without waiting and learn when there is room again.
`Connection.NotifyOutput` covers an HTTP/1 connection only, and not HTTP/2 or HTTP/3 streams.

**Request.**

```go
// WriteNoWait adds p to the response body as Write does, but never waits: it
// takes all of p, and reports above when more than the response's limit
// (64 KB) is now queued or held back by flow control. A caller that gets
// above stops writing until OnWritable's function runs.
func (c *Context) WriteNoWait(p []byte) (above bool, err error)

// OnWritable arranges for fn to run, once, on another goroutine, when the
// response's queued output falls to half its limit, or with an error when the
// connection or stream ends or the write deadline passes. It reports false,
// and never runs fn, when the output is already at or below half.
func (c *Context) OnWritable(fn func(error)) bool
```

**fit's use.** Together with A1:

```go
above, err := c.WriteNoWait(data)
if err != nil { /* client gone: resp.Close(), release */ }
if above {
    resp.HoldBody(true)
    if !c.OnWritable(func(err error) { if err == nil { resp.HoldBody(false) } else { /* abort */ } }) {
        resp.HoldBody(false)
    }
}
```

This is the same shape as the upload direction, which already uses `Context.HoldBody` and
`BodyWriter.OnDrain`. WebSocket, gRPC and tunnel forwarding (M3) need the same pair.

## A3: Asynchronous file sending

**Status.** Done in fib v1.4.0 (`ecd62b0`), as the first option: `Context.SendFile(f *os.File,
offset, n int64, done func(error))`. Plain HTTP/1 sends 16 KB or more with `Connection.SendFile`;
TLS, HTTP/2 and HTTP/3 read 64 KB pieces on a file pool of fib's (`sidepool.File()`, 256 workers)
and write them with `WriteNoWait` and `OnWritable`. `done` runs on another goroutine, and the
response is retained until then. It sets `Content-Length` when unset; the handler sets 206 and
`Content-Range` itself. Not tied to `FileCache`.

**Problem.** fit's static handler serves files with `http.ServeContent`, which reads the file on the
worker and writes through `Context.Write` or `ReadFrom` (sendfile), both of which wait for the
client. Disk reads and the waits both block the worker.

**Request.** One of:

- `Context.SendFile(f *os.File, offset, n int64, done func(error))`, which sends a range of a file
  without waiting: sendfile paced by A2's notification on HTTP/1, and chunks read on a bounded file
  I/O pool and sent with A2 on HTTP/2 and HTTP/3; or
- an `http.FileCache` method that serves a cached file, or a range of it, through A2, with misses
  read on a bounded pool.

**fit's use.** The static handler serves from `FileCache` and sends large files with this API; it
calls no blocking file operation on a worker.

## A4: Engine connection limit

**Status.** Done in fib v1.4.0 (`eb0fe89`): `fib.Config.MaxConns int`, engine-wide (no per-binding
setting), 0 for no cap. Connections accepted past it are closed at once, before `OnOpen`, and
counted in `Stats.ConnsRefused`; `Stats.OpenConns` is the count. UDP peers and dialed connections
are not counted.

**Problem.** `fib.Config` has no limit on open connections. Rule 11 requires one, with a safe
default.

**Request.** `Config.MaxConns int` (and optionally per `Binding`): past it, accepted connections are
closed at once (or accepting pauses), counted in a statistic. Zero keeps today's behaviour.

## A5: Client limit on requests waiting for a connection

**Status.** Done in fib v1.4.0 (`7179521`): `ClientConfig.MaxPendingPerHost int` and
`ErrTooManyPending`, as asked, in `http` only (`http3` has no per-host cap). Retried requests bypass
it.

**Problem.** With `MaxConnsPerHost` reached, requests wait for a connection with no limit on how
many wait; only their context bounds how long.

**Request.** `ClientConfig.MaxPendingPerHost int`: a request beyond it fails at once with a
distinct error (for example `ErrTooManyPending`), which fit maps to 503.

## A6: Trailers set on a `BodyWriter` before it closes

**Status.** Done in fib v1.4.0 (`b51db73`), narrower than asked: `Close` already sends `req.Trailer`
as it is at `Close`, so values set before it go out, provided the names were in `req.Trailer` before
`Do`. `BodyWriter.CloseWithTrailer(trailer http.Header) error` hands the values over from a producer
that does not hold the request. HTTP/1 sends a trailer only with a chunked body.

**Problem.** fib's client reads a request's `Trailer` when it sends the header. A body whose trailer
values are known only when it ends (a client upload with trailers) cannot use `BodyWriter`, so fit
uses a blocking pipe read on the client's own goroutine, one goroutine per such upload.

**Request.** `BodyWriter.CloseWithTrailer(trailer http.Header) error`, or the client reading the
request's `Trailer` after the body's EOF rather than with the header.

## A7: A per-request value slot on `Context`

**Status.** Done in fib v1.4.0 (`21eeb62`, `dbb6ef2`): `Context.SetValue(v any)` and
`Context.Value() any`, cleared when the Context is recycled. The slot lives with the Context's
cancellation hooks, so the first `SetValue` on a fresh Context allocates them (about 48 bytes); a
recycled one keeps them.

**Problem.** fit attaches its access log entry to a request with `r.WithContext`, which copies the
whole `http.Request` and allocates a context value on every request.

**Request.** `Context.SetValue(v any)` and `Context.Value() any` (one slot, cleared when the
`Context` is recycled), or a small keyed map with the same lifetime.

## A8: Documentation fixes

**Status.** Done in fib v1.4.0 (`75e7005`): `StreamResponseBody` and `OnBody` now say that an
`OnBody` function that waits holds up the connection's other streams. `WriteStallTimeout` was not
added and is not needed: with `WriteNoWait` (A2) fit's proxy no longer waits on a client, and
`SendFileStall` (A16) bounds the static handler's waits. Checked again on 2026-10-21: nothing is left
to ask.

- `StreamResponseBody` says a stalled reader "never holds up the other streams on the connection".
  That holds for a reader of `Body`, but not for an `OnBody` function that waits, which runs on the
  connection's reader. Say so.
- `Config.WriteTimeout` defaults to 0, so a streaming `Write` to a client that stops reading waits
  forever. Consider a per-wait bound (`WriteStallTimeout`) with a non-zero default, separate from the
  whole-response `WriteTimeout`, which long downloads cannot use.

## A9: Refuse a header field name that is not a token

**Status.** Done in fib v1.4.0 (`25064b3`), the patch of `f02a0bc` rebased onto `main`.

**Problem.** fib v1.3.0's HTTP/1 server accepts `X-A : one` as a field named `X-A ` (with the
space), where RFC 9112 section 5.1 asks a server to refuse whitespace between a field name and its
colon with 400: a server downstream may read it as another field, which is a way to smuggle one.
The fast parser (`simpleField`) refuses such a name and hands the head to net/http's
`ReadRequest`, which keeps it as sent; net/http's own server checks names after reading, and fib's
does not. A tab before the colon is refused already. `Host : x` was refused only because the
request then has no `Host`.

**Evidence.** I23's proxy conformance suite (`test/e2e/conformance_test.go`), and a request written
by hand to a fib handler: `X-A : one` reaches it as `[X-A ]=[one]`.

**Suggested change.** In the HTTP/1 server, after `parseRequestHead` falls back to net/http's
reader, refuse with `ErrMalformed` (so 400) a request whose header has a name that is not a token,
as net/http's server does; the fast path takes only tokens. `parseRequestHead` itself can keep
matching `ReadRequest`, as its tests ask. A patch that does this, with conformance cases for a
space before another field's colon, a tab, and a space inside a name, is on a local branch of the
fib checkout, `fix-field-name-whitespace` (commit `f02a0bc`, not pushed), for the maintainer to take
or redo.

**How fit uses it.** It needs nothing new: the request never reaches a handler. Until then fit's
proxy refuses with 400 a request with a field name that is not a token (I23, in the loop that
copies the header anyway), so such a field never reaches an upstream; other handlers do not pass
headers on. The check stays as defence in depth once fib refuses them.

## A10: Stream a client response of unknown length from its header

**Status.** Done in fib v1.3.1 (`ClientConfig.StreamUnknownLengthAtHeader`, in `http` and
`http3`); fit uses it from then on.

**Problem.** `ClientConfig.StreamResponseBodyThreshold` does two jobs. For a body with a
`Content-Length`, it decides whether the body arrives whole (up to the threshold) or streams. For a
body of no declared length (chunked, ended by the server's close, or an HTTP/2 body that declares
none), it is how much of the body must arrive before the callback runs. fit sets it to 64 KB, so
that small responses arrive whole and are written to the client in one go; but then a response
that trickles, as server-sent events and long polls do, reaches fit's callback, and so the
client, only once 64 KB of it have arrived, or the stream ends, or fit's
`response_idle_timeout` (60s) cuts it off. A threshold of 0 streams those at once, but it also
streams every body with a length, however small: none of them is complete when the callback runs,
so each takes the streaming path, with a write for the header and more for the body, instead of one.

**Evidence.** `handler/proxy/sse_test.go` (skipped until this lands): a backend that sends
`data: 0`, then two more events 200 ms apart, through fit's proxy over HTTP/1.1, h2c and h2
upstreams. With fib v1.3.0 and fit's threshold of 64 KB the first event reaches the client after
606–617 ms, when the stream ends; a stream that does not end shows nothing for 60 s. With the
threshold at 0 it arrives at once, but of 20 small responses with a `Content-Length` through
`upstream.Pool`, none was complete when its callback ran, against all 20 at 64 KB.

**Request.** A setting that runs the callback of a body of unknown length as soon as its header
has arrived, with whatever of the body came with it, and leaves bodies with a length to the
threshold as now:

```go
// StreamUnknownLengthAtHeader runs the callback of a response whose body
// has no declared length as soon as its header has arrived, rather than
// once StreamResponseBodyThreshold of the body has, so that a body that
// trickles is passed on as it comes. Bodies with a Content-Length are kept
// whole or streamed by StreamResponseBodyThreshold as before. It has no
// effect without StreamResponseBody, and applies to HTTP/1 and HTTP/2.
StreamUnknownLengthAtHeader bool
```

A patch that adds `StreamUnknownLengthAtHeader` to `http.ClientConfig` and `http3.ClientConfig`,
with tests over HTTP/1, HTTP/2 and HTTP/3, is on a local branch of the fib checkout,
`stream-unknown-length` (commit `5fd41c1`, from `main`), merged and released as v1.3.1. fib's tests pass under the race
detector, and with the branch put in place of fib v1.3.0 and the setting turned on, fit's
`sse_test.go` passes and the rest of its tests still do.

An alternative that keeps small chunked bodies whole too would be a wait,
`StreamResponseBodyMaxWait time.Duration`: the callback runs once the threshold has arrived, the
body has ended, or the wait has passed since the header, whichever is first.

**How fit uses it.** `upstream.New` sets it beside `StreamResponseBody`; the proxy's streaming
path already writes each piece as it arrives and flushes it unless more is coming at once, so
events reach the client as the upstream sends them. `sse_test.go` is then run instead of skipped.
Until then there is no stopgap that keeps both: fit keeps the 64 KB threshold.

## A11: The body of a client response that arrived whole, without a copy

**Status.** Done in fib v1.4.0 (`3661093`): `ClientResponse.BodyBytes() ([]byte, bool)`, what `Body`
has yet to read, false for a streamed body; fib never reuses the bytes. HTTP/3 too.

**Problem.** A response that arrived whole holds its body in a `bufferedBody`, which `Body` reads
from. The proxy passes such a body on with `Context.WriteResponse`, whose `Body` is a `[]byte`, so
it reads the body into a slice of its own first: an allocation and a copy of every small proxied
response. `OnBody` delivers the same bytes without a copy, but they are valid only during the call,
and `WriteResponse` reads `Body` until it is sent, which over HTTP/2 is after it returns.

**Evidence.** `BenchmarkProxy` (`handler/proxy/bench_test.go`): since R07b the read is one
allocation of the body's length (`readBody`), about 1 of fit's 12 allocations per proxied request.

**Request.** `ClientResponse.BodyBytes() ([]byte, bool)`: the body of a response that arrived
whole, which stays valid for as long as the response is referenced, and false for one that
streams. Or `Response.Body` taking the bytes `NewBufferedBody` was given, under the same terms.

**How fit uses it.** `exchange.onResponse` hands the bytes to `WriteResponse` without reading them.

## A12: Fewer timers and contexts per client request

**Status.** Done in fib v1.5.0 (`b580ea7`, `f4afaa6`, `c58eb4b`, `783e89c`, `2f68795`): the
connection's deadline timer and the client's idle timer are reset rather than made anew;
`ClientConfig.ResponseHeaderTimeout`, failing with `ErrResponseHeaderTimeout` (which matches
`os.ErrDeadlineExceeded`), shares the request's one timer with `Timeout`, and a request requeued for
a retry restarts it. It is a config setting, not per request, which suits fit, with a client per
upstream pool. `Client.Start(req, cb) Call` and `Call.Cancel(err)` cancel a request without a
cancellable context: 45 allocations a request against `Do`'s 48 with one.

**Problem.** Each request through `Client.Do` makes several timers and contexts that could be
reused or left out, and the proxy adds one of its own for its response header timeout, which the
client has no setting for.

**Evidence.** A memory profile of `BenchmarkProxy` (HTTP/1.1 upstream, keep-alive, fib v1.3.1),
per request: four `time.AfterFunc` timers, each a timer and a closure: `Client.do`'s for
`ClientConfig.Timeout` (`client.go:284`); `Client.release`'s idle timer, made anew each time a
connection goes back to the pool (`client.go:716` and `774`) rather than reset; the connection's
read deadline (`Connection.armDeadlineLocked`); and the proxy's `response_header_timeout`. Besides
those, `context.AfterFunc` for the request's context (`client.go:287`, four allocations with
`propagateCancel`), and a `context.WithCancel` in the client. The whole exchange, net/http backend
included, makes 81 allocations a request.

**Request.**

- The idle timer kept with the connection and `Reset` when the connection goes idle, and stopped
  when it is taken, rather than made each time.
- The connection's deadline timer reused across deadlines.
- `ClientConfig.ResponseHeaderTimeout` (and a per-request way to set it, as fit's upstreams each
  have their own): the time from the request's end to its response's header, failing the request
  with an error fit can tell apart from `Timeout`'s.
- If the timeouts can share one timer per request (or a wheel per worker), so much the better.

**How fit uses it.** `upstream.New` sets `ResponseHeaderTimeout` from `response_header_timeout`,
and the proxy drops its own timer for it; the idle timeout stays a timer of the proxy's, reset as
the body arrives.

## A13: No header copy for a response that only `OnFinish` hooks

**Status.** Done in fib v1.4.0 (`67bc31d`), as asked: only `OnHeader` and `OnResponse` hooks make
`WriteResponse`, `Upgrade` and `Connect` copy the header. No API change.

**Problem.** `Context.WriteResponse` clones the response's header whenever any hook is registered
(`handler.go`, `WriteResponse`: `c.hooked() != nil`), since a hook may change it. An `OnFinish`
hook cannot: its header "must not be changed". fit's access log registers one on every request, so
with the log on, every response written whole, as `respond` and the proxy's small responses are,
pays for a copy of its header that nothing needs.

**Evidence.** `BenchmarkDispatch` (`listener/dispatch_test.go`, a `respond` route with the access
log on): `net/http.Header.Clone` is 3 of the 12 allocations per request. On the benchmark host,
fit's fixed response over h2c serves 3.78M requests a second without the access log and 2.28M with
it (written to `/dev/null`), and 783k and 752k over HTTP/1.1.

**Request.** Clone the header only when an `OnHeader` or `OnResponse` hook is registered: keep a
flag beside `whole` set by those two, and test it in `WriteResponse` instead of `hooked() != nil`.

**How fit uses it.** Nothing changes in fit; logged responses stop paying for the copy.

## A14: `Release` on a Context that has no connection

**Status.** Done in fib v1.5.1 (`c9dd792`), as asked: the last `Release` skips the flush when
`c.Conn` is nil, and `NewStreamContext`'s doc allows a nil connection.

**Problem.** `fibhttp.NewStreamContext(nil, req, stream)` makes a Context for a stream served
outside fib, with no `fib.Connection`; fit's `internal/fibtest` uses one to run handlers in tests
without a network. `fibhttp.Serve` runs a handler on it, and a handler that answers from the
handler's goroutine works. One that retains the request and answers later, from another
goroutine, panics at its last `Release`. `Context.giveBack` calls `c.Conn.Flush()` after
`Finish` on that path, and `Conn` is nil.

**Evidence.** With fib v1.4.0, a handler that calls `c.Retain()`, then from a goroutine
`c.WriteResponse(...)` and `c.Release()`, served through `fibhttp.Serve` on a
`NewStreamContext(nil, ...)`: nil pointer dereference in `(*Connection).Flush`, from
`(*Context).giveBack` (`http/context.go:895`). R05 moves the static handler's file work to a pool
that answers from its own goroutines, so every static handler test takes that path.

**Request.** Skip the flush when `c.Conn` is nil (an external stream sends what it is given
itself), and say in `NewStreamContext`'s doc that a nil connection is allowed.

**How fit uses it.** `fibtest.ServeRequest` serves through `fibhttp.Serve` and waits for a
retained response to be written, so the static handler's tests run unchanged against its
asynchronous paths.

## A15: `MaxPendingPerHost` not to count requests whose connection is being dialed

**Status.** Done in fib v1.5.1 (`f627cd4`): a request is refused when the requests waiting, less
those covered by dials under way and by the room `MaxConnsPerHost` leaves, reach the limit. For a
host that may be HTTP/2, the room counts as the dials it would become. The bound while dials hang is
`MaxConnsPerHost + MaxPendingPerHost`. Without `MaxConnsPerHost` the cap no longer applies; fit
always sets one.

**Problem.** `ClientConfig.MaxPendingPerHost` counts every request in a host's queue, those
waiting for a connection that `MaxConnsPerHost` lets the client dial, and is dialing for them,
included. So a burst of new requests to a host with no idle connections is refused past
`MaxPendingPerHost` while the connections it is allowed are still opening. fit's `max_pending` is
meant as nginx's `queue`: the requests waiting once `max_conns` are busy. R02 counted it that way,
in fit, and `upstream.TestDefaultMaxConns` tested it.

**Evidence.** With fib v1.4.0, an upstream with `max_conns` 256 (the default) and `max_pending`
8, and 256 requests at once to a backend that holds them: 179 connections open and the rest of
the requests get `ErrTooManyPending` at once, while R02's count let all 256 open. With
`max_conns` 1 and `max_pending` 2, three requests at once to a cold host: the third is refused
while the first's connection is dialing.

**Request.** Leave out of the count the requests the dials under way are for: refuse when
`pendingLocked(h) - h.dialing >= limit` (for HTTP/1, where each dial carries one request; for a
host that may be HTTP/2, the one dial a time stands for all of them until it has answered). The
queue stays bounded, at `MaxConnsPerHost + MaxPendingPerHost` for a host whose dials hang, and a
burst within `MaxConnsPerHost` is not turned away.

**How fit uses it.** No change: `upstream.TestDefaultMaxConns` goes back to sending its 256
requests at once.

## A16: A file queued by `SendFile` counted in a connection's unsent output

**Status.** Partly done in fib v1.5.2 (`6f66bcc`): a file segment queued by `SendFile` counts in the
backlog that `NotifyOutput`, `WriteNoWait`, the write deadline and `MaxPendingBytes` see, so the
static handler's transfer is paced and its stall timeout cuts off a client that stops reading, over
plain HTTP/1 too. The stall bound came in fib v1.5.4 (`fe60763`), done by fit's session with the
maintainer's leave: `Context.SendFileStall(f, offset, n, stall, done)` sends a range a piece at a
time, queued for sendfile or read, and waits for room between pieces under a write deadline of
`stall`; `SendFile` is it without a bound. fit's static handler hands it whole ranges (R07e).

**Problem.** On a plain HTTP/1 connection, `Context.SendFile` queues the range as a file segment
with `Connection.SendFile`, which `pushLocked` adds to the send queue without counting it in
`pendingBytes`. So nothing that paces a response sees it: `WriteNoWait` does not report it as
above the limit, `OnWritable` (`Connection.NotifyOutput`) reports room at once, and
`outputwait.Unsent` leaves it out. A client that stops reading keeps its file queued, and its
descriptor duplicated, for as long as it keeps the connection, with no write deadline cutting it
off, since nothing waits. `SendFile` itself bounds no wait on any protocol either: where it copies
pieces, it waits for `OnWritable` without a limit.

**Evidence.** R05's static handler sends a range through `SendFile` in pieces of 256 KB on plain
HTTP/1 (64 KB elsewhere), and between pieces waits on `OnWritable` under a write deadline of the
stall timeout. Over h2c and h3, a client that stops reading a 64 MB file is cut off after the
1 s stall timeout. Over plain HTTP/1, `OnWritable` reports room after every piece: the whole file
is queued, the deadline never set, and the client, once it reads, gets all 64 MB after 2.5 s.
`handler/static/stall_test.go`, `TestStuckClient/http/1.1`, is skipped until this lands.

**Request.** Count a queued file segment's remaining bytes in `pendingBytes` (adding them when it
is queued, taking them off as `sendfile` sends them), so that `NotifyOutput`, `WriteNoWait` and
the server-wide `MaxPendingBytes` budget see a file queued for a client as they see bytes. And,
better still, a stall bound in `SendFile` itself (a `stall time.Duration` argument or a field set
before it), so that it can send a whole range with every wait for the client bounded, and fit
need not split it.

**How fit uses it.** With the first, nothing changes in fit: the transfer's waits work on HTTP/1
too, and the test runs. With the second, `send` hands `SendFile` the whole range.

## A17: Cheaper `OnFinish` and `SetValue` on every request

**Status.** Done in fib v1.5.4 (`b39373d`), by fit's session with the maintainer's leave, as the
first option of each: a recycled Context keeps its response writer, cleared, for its next request,
and `SetValue` and `Value` take no lock (the value is set before the request goes to another
goroutine and read after). On the benchmark host, fixed and static over h2c with the access log on
gained 2.2% and 2.1% (R07e); a single-threaded micro-benchmark of `SetValue`, `OnFinish` and
`Respond` went from 105.6 to 94.2 ns.

**Problem.** fit's access log calls `SetValue` and `OnFinish` on every request it logs. Each costs
more in fib than the work it stands for:

- `OnFinish` calls `addHook`, which calls `Context.writer()`. For a response written whole, as
  `respond`'s is, the Context has no `responseWriter` yet, so the hook takes one from the global
  `writerPool` (`sync.Pool.Get`), and `recycle` resets it and puts it back. That is a pool round
  trip per request for a slot that holds one function.
- `SetValue` takes `c.mu`, which the connection's goroutine also takes, so the unlock often finds
  the lock's cache line on another core.

**Evidence.** `perf` on the benchmark host, fit's fixed response over h2c (h2load, 64 connections,
32 streams; fit on 7 cores) with the access log to `/dev/null`, at R07d's build: of the
0.51 µs a request the log costs, about 54 ns is `addHook` and the `writerPool.Get` under it, and
about 31 ns `SetValue`, mostly in `Mutex.Unlock`; `Context.recycle` goes from 2.1% of the CPU
without the log to 3.3% with it. Together about a sixth of the log's cost, on a path that serves
3.78M requests a second without the log and 2.83M with it.

**Request.** Either or both:

- Keep a Context's `responseWriter`, reset, when it is recycled, as its `cancelHooks` are kept,
  instead of returning it to `writerPool`; or keep `OnFinish` hooks in storage of the Context's
  own, so that registering one needs no writer.
- A value slot that is not behind `c.mu`: say an atomic pointer, set and read without the lock,
  since the slot is set once, before the handler runs, and read after.

**How fit uses it.** Nothing changes in fit: `observe.Track` and the dispatcher's `OnFinish` cost
less.

## A18: A response's status, and when its request arrived, without a hook

**Status.** Partly done in fib v1.5.4 (`b39373d`): `Context.Status()`, with `Retained`. Not done:
`Received`. fib reads no clock for a request unless a timeout needs it, so a read for every
request, or every read round, would cost every server for one of fit's two reads; fit keeps
reading the clock itself. Status alone is read safely only where nothing else answers, so fit counts
without a hook only a handler that declares it answers before returning (`module.Inline`:
`respond`, and the dispatcher's 404 and redirects); fixed over h2c with metrics on gained 8.5%
(R07e).

**Problem.** fit's request metrics (I34) need, for each request, the status it was answered with
and how long it took. fib gives the status only to an `OnFinish` hook, so fit registers one on
every request, with a closure that holds the time the dispatcher read when the request came in,
and reads the clock again in the hook. For a handler that has answered by the time it returns, as
`respond` and `static` have, the hook, the closure and one of the clock reads stand for nothing
fit could not read after the handler returns.

**Evidence.** On the benchmark host, fit's fixed response over h2c (h2load, 64 connections, 32
streams; fit on 7 cores), at I34's build, with the Admin API on: 3.86M requests a second without
`metrics`, 3.28M with it, 0.32 µs more CPU a request. `perf` puts about 110 ns of it in fib's hook
path (`addHook`, `responseHooks.before` and `finished`, `writeResponse`, `recycle`, and the
`sync.Pool` traffic for the writer, as A17 describes), about 60 ns in the two clock reads, about 30
to 45 ns in allocating the closure and collecting it, and about 15 ns in fit's counting. fitbench,
two rounds: fixed h2c 3.79M → 3.29M (−13%), static h2c 2.46M → 2.25M (−9%); HTTP/1.1, held by
the load generator at about 775k, and the proxy, at about 230k, do not move.

**Request.**

- `Context.Status() int`: the status of the response sent, or begun, so far; 0 before any. With
  `Retained`, a dispatcher can tell after the handler returns that the response is settled, and
  count it then, keeping `OnFinish` for handlers that answer later.
- `Context.Received() time.Time`, or a monotonic nanosecond count: when the request's head was
  read, from a clock fib reads anyway for the read round or its deadlines, however coarse, so that
  a duration takes one clock read rather than two.

**How fit uses it.** The dispatcher reads `Received` instead of the clock when a request comes in,
and after the handler returns counts a response that `Status` says is sent and is not `Retained`,
with one clock read and no hook; the proxy's responses keep the hook, which A17 makes cheaper.

## A19: A streamed request's Trailer set where its end is handed over

**Status.** Done in fib v1.5.3 (`aa7c1b0`); fit uses it from then on.

**Problem.** `BodyStream.finish` set the request's `Trailer` on the goroutine that took the body's
end off the connection. On HTTP/2 and HTTP/3 that is the connection's reader, not the handler's
goroutine, so a handler that reads the `Trailer` its request came with, as net/http lets it before
it reads the body, races with the body ending. fit's proxy reads the trailer names its client
announced as it starts, to declare them upstream.

**Evidence.** CI has failed since the M1 review; with three CPUs, as a CI runner has,
`handler/proxy` `TestTrailers/h2c` fails under the race detector: `trailerNames` reads
`Request.Trailer` while `BodyStream.finish`, called from `h2ServerConn.handleTrailers`, writes it.
Five runs in a row fail one in five with fib v1.5.2, and none with the branch.

**Change.** The trailer is kept in the `BodyStream` and becomes the request's `Trailer` only
where the body's end is handed to the handler, on the goroutine taking it: by `Read` as it returns
`io.EOF`, and before `OnBody`'s function is called with `fin`. Until then the request keeps the
`Trailer` it came with. Tests over HTTP/2 and HTTP/3, a handler reading its `Trailer` while the
body and trailer arrive, fail under the race detector without the change and pass with it.

**How fit uses it.** Nothing changes in fit: the proxy's `trailerNames` stops racing.

## A20: Client requests written and response heads read without net/http

**Status.** Done in fib v1.5.6 (`0a9339f`), by fit's session with the maintainer's leave; fit uses
it. The client writes request heads and reads response heads itself, and decodes a whole
chunked body without a trailer, producing net/http's output exactly, which tables and fuzzing
against net/http check; anything outside the common shape still goes through net/http. Writing
fit's proxied request: 1 allocation and 300 ns, against 8 and 645 ns; reading a plain response
head: 5 and 400 ns, against 13 and 1.1 µs. On the benchmark host, fit's proxy (3f25679) over
HTTP/1.1 went from 226.6k to 245.8k requests a second (+8.4%, three rounds each), at 26.0 µs of
CPU a request instead of 29.7; over h2c 235.5k to 238.1k. fib's race suite and fit's, against the
branch, pass.

**Problem.** fib's client frames every HTTP/1 request with `net/http`'s `Request.Write`
(`marshalRequest`, `marshalStreamedHead`) and reads every response head with
`net/http.ReadResponse` over a new `bufio.Reader` (`responseParser.parseHead`, and
`completeChunked` again for a chunked body). Both do much more than a proxy's request needs:
`Request.Write` sorts the header's keys, formats with `fmt.Fprintf` and grows a `bytes.Buffer`;
`ReadResponse` allocates a 4 KiB reader for a head of 100 bytes, a `textproto.Reader`, a `Header`
map and a string per field, and a `Response`. fib's server side does neither: it parses request
heads and writes response heads itself (`parseSimpleRequestHead`, `appendResponseHead`,
`appendHeaderLines`).

**Evidence.** `perf` on the benchmark host (R07f), fit's proxy over HTTP/1.1 at 2b37a8e on fib
v1.5.4: wrk, 256 connections, fit on 7 CPUs, its backend another fit on 2. fit serves 226.8k
requests a second at 29.7 µs of CPU each; nginx, the same way, 265.3k at 20.7 µs. The kernel's share
is about the same (fit 15.2 µs, nginx 17.0 µs, with 5.9 and 6.3 system calls a request); the
difference is user time, 14.5 µs against 3.6. Of fit's, fib's code and what it calls take 10.3
µs and fit's own 1.8 µs. `parseHead` is 2.23 µs a request (7.5% of all CPU), 1.38 µs of it in
`ReadResponse`, and `marshalRequest` 1.49 µs (5.0%), 1.43 µs of it in `Request.write`; between them
they make over half of the request's allocations (0.9 and 0.24 µs of the 2.1 µs in `mallocgc` and
GC assists), which the GC's background marking, another 1.3 µs, follows. Alone, on the development
Mac: `ReadResponse` as `parseHead` calls it, for a head of four lines, takes 1.0 µs, 5,015 bytes and
13 allocations; `Request.Write` for the request fit's proxy sends, 0.63 µs, 704 bytes and 8
allocations.

**Request.**

- Write an HTTP/1 request head with fib's own appender, into a buffer from `bufferpool`: request
  line, `Host`, the header's fields in map order (net/http sorts them, which no peer needs),
  framing; the checks `Request.Write` makes on names and values kept, as `appendHeaderLines` makes
  them for responses.
- Parse a response head with fib's own parser, as the server parses requests: status line, fields
  into a `Header` whose keys are canonical (one string for the whole head, sliced, as
  `parseSimpleRequestHead` does, or interned common names), `Content-Length`, `Transfer-Encoding`
  and `Connection` read as it goes; no `bufio.Reader`. `ClientResponse` keeps its shape.
- Chunked bodies decoded without a second `ReadResponse`.

Together about 3.7 µs a request, an eighth of the proxy's CPU, and most of what puts its user time
above nginx's; fib's HTTP/1 client and every caller of `Client.Do` gain, not only fit.

**How fit uses it.** Nothing changes in fit; the proxy's requests cost less.

## A21: One `epoll_ctl` per park of an event loop, not two

**Status.** Not done: the change asked for cannot save the call. `EPOLLONESHOT` disarms an item only
when `epoll_wait` on the relay delivers it, and nothing calls `epoll_wait` on the relay: Go's
runtime watches it from its own edge-triggered epoll, which learns only that it is ready. On the
benchmark host (Linux 7.0), a relay holding an instance with `EPOLLIN | EPOLLONESHOT`, watched as the
runtime watches it, reported the instance ready to the runtime's epoll for every new event, three
in a row and again after an `epoll_wait` on the relay that found the instance drained. A one-shot
relay would stay armed while the loop runs, and the runtime's poller would look at a busy loop's
instance, which is what the relay is there to stop (4.3% of the CPU in fib's own measurement, in
`poller_epoll.go`). Leaving the relay armed after a wake-up until the loop next finds work is the
other way to save calls; fib's notes measured a variant of it at 1.6% less at 20k and 50k
connections, so it would need fib's WebSocket benchmarks too, not only fit's proxy. Tried on
2026-10-21: the relay left armed after a wake-up and disarmed only when the look before parking
finds events, so that a loop that parks again with nothing to do makes neither call. On the
benchmark host, fit at cbdca26 against the same fit on that fib, three rounds each, alternating:
the proxy over HTTP/1.1 248k → 246k requests a second (−0.8%), over h2c 240k → 240k (−0.1%), at
the same CPU a request (23.15 µs → 23.12 over HTTP/1.1); fixed over HTTP/1.1 +0.9% and h2c −0.3%;
all within the noise. The calls it saves do not show in fit's throughput, so it was not kept, and
fib's WebSocket benchmarks were not needed. Left as it is.

**Problem.** An event loop with nothing to do parks in Go's netpoller through a relay epoll
instance (`epollWaiter.wait`): it polls once, arms the relay with `EPOLL_CTL_MOD` (`EPOLLIN`),
parks, and disarms it with another `EPOLL_CTL_MOD` once woken. Under a proxy's load the loops park
often, so these add system calls nginx does not make.

**Evidence.** The run of A20, with `perf stat` on fit's process: 0.48 `epoll_ctl` and 1.25
`epoll_pwait` a request (nginx: no `epoll_ctl`, 1.36 `epoll_wait`). `epollWaiter.wait` is 1.25
µs a request with its kernel time, `arm` 0.31 µs, and the polls that find nothing, in
`pollEvents`, 0.82 µs.

**Request.** Register the relay's interest with `EPOLLIN | EPOLLONESHOT`: an event disarms it, so
only arming takes a call, and the disarm after waking goes. If the look after arming that the
comment in `wait` explains is still needed, it stays. Or another way to park that costs one call.

**How fit uses it.** Nothing changes in fit.

## A22: A client connection's idle clock without a timer reset per request

**Status.** Done in fib's `main` at `7f632cb`, after v1.5.6; fit uses it from `a6d78cd` on, by
commit (rule 19). The connection's clock is `idleUntil` alone, and its timer is set only when none
is pending; one that fires finds the connection taken, idle until later (and is set again for what
is left), or idle long enough. A connection still closes `IdleConnTimeout` after it last went idle.
On the benchmark host, fit's proxy over HTTP/1.1 (ca450dd on v1.5.6, against the same with the
change): 24.85 and 24.86 µs of CPU a request against 24.64 and 24.80, about 0.13 µs less;
throughput within noise, 245.8k → 246.1k over HTTP/1.1 and 240.2k → 238.8k over h2c (three rounds
each). fib's CI on `7f632cb` (run 215) failed on two macOS tests in packages the change does not
touch (`TestRemoveUDPBindingClosesPeers/pollers-no-reuseport`, http3's `TestStreamTimeoutMidBody`);
both pass 20 times in a row alone.

**Problem.** Each time a request's response ends and its connection goes back to the pool,
`Client.idleLocked` reads the clock and calls `Timer.Reset` on the connection's idle timer, which
takes the timer's lock and moves it in the runtime's timer heap, and taking the connection stops
it again; with keep-alive that is every request.

**Evidence.** The run of A20: `idleLocked` is 0.23 µs a request, mostly `Timer.Reset`.

**Request.** Keep the idle deadline as a time on the connection, written when it goes idle, and
have one timer per client (or per host) sweep idle connections every `IdleConnTimeout` / 4 or so,
closing those past their deadline; or reset the timer only when the deadline moves by more than a
fraction of the timeout. A connection closes up to that fraction late, which `IdleConnTimeout`
allows.

**How fit uses it.** Nothing changes in fit.

## A23: `OnClose` for the connections `Engine.Close` cuts

**Status.** Done in fib v1.5.6 (`6fb6479`); fit uses it from then on.

**Problem.** `Engine.Close` on the epoll, kqueue and IOCP backends cut every connection at once
without running its `OnClose`, which the portable backend's `Close` did run. What a protocol lets
go of in `OnClose` stayed held: an HTTP/2 connection's idle timer, set for `IdleTimeout` (two
minutes by default) once its last stream ended, kept the connection, and through it the closed
engine, its task pool and the 20000-slot queues of its stream pool, until it fired. Every fit a
test starts and closes, after one h2c request, stayed in memory for two minutes.

**Evidence.** fit's CI failed on macOS in `handler/proxy` `TestStreamedBodyLimit`, h2c case
`{150000 false 200}`: the client timed out awaiting headers. Under load the same hang came back
locally in every GOMAXPROCS=2 copy of the test run 16 at a time; the goroutine dump showed the
client waiting for flow control with its first 65535 bytes sent, and fit's connection reader still
building the engine's stream pool in `newH2ServerConn`, before it had sent its SETTINGS. The
machine was swapping: one test process had grown to 4.2GB. Each start and close of fit with one
h2c request kept about 1MB (56MB under the race detector); with fib's HTTP timer sites tagged,
every timer still pending was the idle timer of a connection `OnClose` never reached. The whole
`handler/proxy` package under the race detector peaked at 5.3GB, which on a 7GB runner running
several packages at once is swapping, and stalls of seconds.

**Change.** `Close` records the connections it cuts, and runs their `OnClose` once no round
runs, before the task pools go, as their last round would have. fib's
`TestCloseRunsOnClose` (an idle connection and one whose handler is running at `Close`) and
`TestCloseLetsGoOfAnIdleHTTP2Connection` (a closed engine with an idle HTTP/2 connection is
collected) fail without the change and pass with it.

**How fit uses it.** Nothing changes in fit's code. `handler/proxy` under the race detector now
peaks at 1.8GB rather than 5.3GB and runs in 104s rather than 142s, and 16 copies of
`TestStreamedBodyLimit` at once, 400 times each, all pass, each under 510MB.

## A24: A new taskpool ring that writes none of its cells

**Status.** Done on fib's `main` as `a6d78cd`, after v1.5.6, whose CI passed on every job; fit uses
it by commit (`v1.5.7-0.20261020131351-a6d78cd9d925`, rule 19).

**Problem.** `taskpool.newTaskRing` stored every cell's `seq` with an atomic when it made the
ring: 100,000 cells for a pool at the default sizing on a machine of ten cores or more, 40,000 on
four. Under the race detector each address an atomic touches costs a sync object of a few hundred
bytes in the detector's own memory, which the Go heap does not count, so each engine's pool cost
tens of megabytes before it ran a task. Outside the detector the stores touched every page of the
queue.

**Evidence.** After A23, `handler/proxy` under `go test -race` still peaked at 1.27GB resident
(1.42GB peak footprint) on a 14-core Mac, and at 1.25GB footprint with GOMAXPROCS=4, though the
Go heap of a test such as `TestHostHeader` never passed 18MB, nearly all of it the rings. One fit
started with no traffic had a footprint of 179MB, 155MB of it the region `vmmap` names
`Sanitizer`, against 9MB of Go heap; without the race detector the same test peaked at 25MB.
`runtime.AddCleanup` probes on twelve fits started, used over HTTP/1.1, h2c and h2 and stopped,
found every closed engine collected and the heap in use flat at 3MB, so nothing was leaking.

**Change.** A cell keeps its `seq` less its index, so that a zeroed cell is already free for its
first position and a new ring writes none of its cells. fib's `TestTaskRingStartsUntouched`
checks a new ring's cells are zero and that a ring of four keeps its order and limit over 1000
laps; fib's tests pass with and without the race detector, vet passes for Linux, macOS, Windows
and FreeBSD and staticcheck for the first three. A push and a pop cost the same as before (5.0
against 5.3ns, and 142 against 142ns with four CPUs), and a ring of 100,000 is made in 82µs
rather than 263µs.

**How fit uses it.** Nothing changes in fit's code. With it, one idle fit under the race detector
has a footprint of 67MB rather than 179MB, and `handler/proxy` peaks at 540–720MB rather than
1.27–1.42GB (runs differ by about 100MB), and at 518MB rather than 1.25GB with GOMAXPROCS=4, in
the same time. What is left is what the heaviest tests hold on purpose: in
`TestStuckClientsPastMaxConns` and `TestStuckClientWithoutStallTimeout` eight downloads that are
not read fill their flow-control windows in the test's own h2c client and in fib's HTTP/3
streams, and the stream tests move 64MB each way, with the race detector's shadow of that memory
on top. Closing the idle connections of the test helper `client`'s transports at the end of each
test was tried, and changed nothing beyond the noise: fit's listener cuts them when it closes.

## A25: A per-request limit on a request body, streamed or not

**Status.** Done on fib's `main` in `ad0d175`, with its test made reliable in `cfb4f09`, whose CI
passed on every job (run 39569138260); fit uses it by commit (`v1.5.7-0.20261021080904-cfb4f09f0283`, rule 19).

**As built.** `func (c *Context) LimitBody(n int64) error`, called on the goroutine serving the
request, before the body is taken and before anything answers it; a negative `n` does nothing, and
`n` of zero allows no body. The lower of `n` and `MaxBodyBytes` (a body read whole) or
`MaxStreamedBodyBytes` (one that streams) applies. A body known to be past `n` already — a
`Content-Length` past it, a body that arrived whole and is longer, or a streamed one that has
brought more than `n` bytes — is refused at once: the body ends with `ErrBodyTooLarge`, fib answers
413 unless a response has been begun (closing an HTTP/1 connection with a lingering close; HTTP/2
and HTTP/3 refuse only the stream), and `LimitBody` returns `ErrBodyTooLarge`. Otherwise it returns
nil, and a body of no declared length (HTTP/1 chunked, HTTP/2 and HTTP/3 without content-length) is
ended past `n` with `ErrBodyTooLarge`, which `Read` and the `OnBody` callback see, as past
`MaxStreamedBodyBytes`. A request whose streamed body was ended for its size and that is let go
unanswered is answered with 413 rather than the empty 200 (this holds for `MaxStreamedBodyBytes`
too). fib's `TestLimitBody` covers HTTP/1.1, h2c and h2 over TLS, and HTTP/3, for servers that read
bodies whole, stream them, and stream them past a threshold, with declared and undeclared lengths.

**Problem.** fit's `body_limit` middleware bounds a route's request bodies below the listener's
`max_body_bytes`. It can refuse a body whose `Content-Length` declares too much, but not one of no
declared length (chunked on HTTP/1.1, or HTTP/2 and HTTP/3 without the field): the bytes reach the
handler's `OnBody` callback, which the middleware does not see, and a `Context` holds one. Such a
body is bounded only by the listener's `MaxBodyBytes` and `MaxStreamedBodyBytes`, so a route that
should take 1MB takes what the whole listener allows.

**Suggested API.** `Context.LimitBody(n int64)`, callable before the body is taken: past `n` bytes
fib ends the body with an error the handler's `OnBody` sees (as it does past
`MaxStreamedBodyBytes`), and answers 413 itself when no response has begun; a `Content-Length`
past `n` is refused at once. The lower of `n` and the connection's limits applies.

**How fit would use it.** `body_limit` calls `c.LimitBody(max_bytes)`, returns without calling the
next handler when it reports an error (fib has answered), and drops its own `Content-Length` check.

## A26: More than one `OnCancel` callback on a Context

**Status.** Done on fib's `main` in `ad0d175` (with A25; CI passed on `cfb4f09`); fit uses it by
commit (`v1.5.7-0.20261021080904-cfb4f09f0283`, rule 19).

**As built.** `Context.OnCancel(fn func(error))`, its signature unchanged, keeps every callback and
runs them in the reverse of the order they were registered. Each still runs once, on the engine's
handler pool, never after the response is written, and at once on a request already ended;
registering one once the response is written does nothing. The first is a field, the rest a slice
behind a pointer that a recycled Context keeps, so neither one callback nor several allocate. fib's
`BenchmarkOnCancel` (a registration and a recycle, on the benchmark host, 10 alternating runs): one
callback 7.00 → 7.48ns, the residual being the hooks' extra word cleared on recycle; two 10.4 →
14.4ns, where before the second replaced the first; no allocations before or after. fib's tests
cover several callbacks on a request whose connection is cut, and a recycled Context.

**Problem.** `Context.OnCancel` keeps one callback: a second replaces the first. A middleware that
must hear of a client leaving, as fit's `concurrency_limit` must to give a request's place back,
loses its callback when the handler registers its own, as fit's proxy does for every request.
fit's test `TestConcurrencyClientGone` failed that way: the place stayed taken after the client
hung up.

**Stopgap in fit.** The middleware waits on `Context.Context()` with `context.AfterFunc`, asked for
only once the handler has returned with the request retained, and stopped when the response is
sent. On fit's micro-benchmark that path costs a context and its cancel machinery per retained
request; a cancelled one costs a goroutine too.

**Suggested API.** `OnCancel` keeps every callback registered, and runs them in the reverse of the
order they were registered, as the response hooks do; one callback stays as cheap as today (a
field, with a slice only for a second).

**How fit would use it.** `concurrency_limit` registers `OnCancel` beside `OnFinish` before the
handler runs, and no longer asks for the request's context.

## A27: A hook that transforms a response body as it is written

**Status.** Done on fib's `main` in `fc7ec5e`, made by fit's session with the maintainer's leave;
fit uses it by commit (`v1.5.7-0.20261021111325-fc7ec5e327d4`, rule 19). fib's `http`, `http3` and middleware race tests pass on the benchmark host, `go vet`
and staticcheck pass for linux, darwin, windows and freebsd, and fib's root package fails there
only in its UDP and socket-buffer tests, as it does on `main` in the same container.

**Problem.** fit's `compress` middleware (I46) must compress responses whichever way the handler
writes them. fib's only hook that sees a body is `OnResponse`, and registering it holds a response
written through the ResponseWriter methods whole until the handler is done: without a bound, and
without streaming. The proxy streams every response past 64 KB, and server-sent events through
it; holding them would break rules 5, 7 and 11.

**Evidence.** `holdsWhole` in `http/response_writer.go` holds every write once an `OnResponse`
hook is registered (`hooks.whole`); nothing limits `w.buf`, and `Flush` does nothing for such a
response.

**As built.** `Context.OnBodyFilter(fn func(status int, header http.Header) BodyFilter)`. `fn` runs
as the header is settled, in turn with the OnHeader hooks, may change the header, and returns a
`BodyFilter` (`Filter(p) ([]byte, error)`, `Close() ([]byte, error)`) or nil. Every part of the
body goes through it as it is written: `Write`, `WriteNoWait`, `ReadFrom` (which then copies rather
than sendfiles); `Flush` and `FlushError` hand over what a filter that is also a `BodyFlusher`
holds back; `Finish` sends what `Close` leaves. A response sent whole (`WriteResponse`, `Respond`)
is filtered whole, and its hook sees the body's length as its `Content-Length`. A filtered response
goes without `Content-Length`; HEAD and statuses without a body are not filtered. Filters
registered on one response run in turn. fib's `TestBodyFilter`, `TestBodyFilterFlush` and
`TestBodyFilterChainAndSkips` cover HTTP/1.1 and h2c. `bench/microbench.sh` on the benchmark host,
10 alternating runs, `cfb4f09` against it: `BenchmarkServerKeepAlive`, `ServerKeepAliveHooked`,
`StreamRespond` and `StreamAccessLog`, no slowdown and the same allocations.

**How fit uses it.** `compress` registers an `OnBodyFilter` that decides from the status and the
header, sets `Content-Encoding`, `Vary` and a weak `ETag`, and returns a streaming zstd, brotli or
gzip encoder.

## A28: Upgrade a request the handler retained, after it returned

**Status.** Done on fib's `main` in `929924e`, made by fit's session with the maintainer's leave;
fit uses it by commit (`v1.5.7-0.20261021130553-929924e17392`, rule 19).

**Problem.** A proxy can switch its client's request to WebSocket only once the upstream has
answered 101, with the subprotocol and extensions it chose, which the proxy learns in the fib
client's callback, after its handler has returned. `Context.Upgrade` refused a request whose
handler was no longer running; waiting for the upstream in the handler would block a worker.

**As built.** `Upgrade` and `Connect` also take a request the handler retained, before anything
answered it, from any goroutine. On HTTP/1 the switch takes the connection's parser lock: what the
client sent while the request waited (the parser keeps it while the request is busy) is the
tunnel's first data, a round that read meanwhile hands its bytes to the tunnel, and the last
`Release` no longer carries the connection on to another request. HTTP/2 and HTTP/3 streams switch
under their own locks, as before; the DATA that came first is queued, bounded, in the stream's
tunnel. fib's `TestLateUpgradeHTTP1`, `TestLateUpgradeHTTP2`, `TestCcTunnelLateUpgrade` and
`TestLateUpgradeAfterAnswer`.

**How fit uses it.** The proxy retains a request that asks to switch, sends the handshake
upstream, and calls `Upgrade` in the callback, with the upstream's answer.

## A29: A client request whose 101 hands over its connection as a tunnel

**Status.** Done with A28 (`929924e`).

**Problem.** fib's client closed a connection on a 101 before the callback ran: a proxy had no way
to keep the connection the upstream switched.

**As built.** `Client.StartUpgrade(req, handler, callback)`. On a 101 the connection leaves the
client, no longer counted against `MaxConnsPerHost`, and becomes a tunnel,
`ClientResponse.Tunnel()`, whose handler opens once the callback returns, with what followed the
101 queued. Any other answer is an ordinary response. A request that would go out on an HTTP/2
connection fails with `ErrNotUpgradable`. fib's `TestClientStartUpgrade` and
`TestClientStartUpgradeHTTP2`.

**How fit uses it.** `upstream.Pool.SendUpgrade` sends the handshake through the upstream's pool,
with its dial, TLS and limits.

## A30: Extended CONNECT from the HTTP/3 client

**Status.** Done with A28 (`929924e`).

**Problem.** I48's tests open WebSockets over h3 entries, and no client fit could use sent an
extended CONNECT over HTTP/3.

**As built.** The HTTP/3 client sends a CONNECT whose header has `:protocol` as an extended CONNECT
(RFC 9220), as net/http's HTTP/2 transport does. fib's `TestClientExtendedConnect`.

## A31: A callback when an HTTP/2 or HTTP/3 tunnel's unsent output drains

**Status.** Open.

**Problem.** A relay holds one side's reads while the other has too much unsent, and resumes them
once it drains. Over HTTP/1 `Connection.NotifyOutput` says when, with nothing waiting; over HTTP/2
and HTTP/3 a tunnel only has `WaitSendable`, which blocks a goroutine for the while.

**Evidence.** fit's proxy relays a WebSocket that came in over h2 or h3 to an HTTP/1.1 upstream;
when its client reads slowly, one goroutine per held tunnel waits in `WaitSendable` (fib's own
`ConnectProxy` does the same).

**Suggested API.** `Tunnel.NotifySendable(limit int, fn func(error)) bool`, as
`Connection.NotifyOutput`: when more than limit is unsent, run fn once no more than half is, or
with an error once the tunnel ends, and report true.

**How fit would use it.** The relay registers it in place of the goroutine, which goes.

