# APIs fit needs from fib

[English](fib-api-requests.md) | [简体中文](fib-api-requests.zh-CN.md)

A review of M0 and M1 against the [development rules](rules.md) found places where fit cannot
follow them with fib v1.3.0's API alone. This document lists what fit asks of fib, why, and how fit
would use each API. The names and signatures are suggestions; fib decides the final shape.

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
| A8 | Documentation fixes | partly, v1.4.0 | — | — |
| A9 | Refuse a header field name that is not a token | done, v1.4.0 | 13 | — |
| A10 | Stream a client response of unknown length from its header | done, v1.3.1 | 5 | — |
| A11 | The body of a client response that arrived whole, without a copy | done, v1.4.0 | 12 | R07c |
| A12 | Fewer timers and contexts per client request | done, v1.5.0 | 12 | R07c |
| A13 | No header copy for a response that only `OnFinish` hooks | done, v1.4.0 | 12 | R07c |
| A14 | `Release` on a Context that has no connection | done, v1.5.1 | 13 | R05 |
| A15 | `MaxPendingPerHost` not to count requests whose connection is being dialed | done, v1.5.1 | 11 | R06 |
| A16 | A file queued by `SendFile` counted in a connection's unsent output | partly, v1.5.2 | 10, 11 | R05 |

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

**Status.** Partly done in fib v1.4.0 (`75e7005`): `StreamResponseBody` and `OnBody` now say that an
`OnBody` function that waits holds up the connection's other streams. `WriteStallTimeout` was not
added: with `WriteNoWait` (A2) fit's proxy no longer needs it.

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
plain HTTP/1 too. Not done: a stall bound inside `SendFile`; fit keeps sending ranges in pieces.

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
