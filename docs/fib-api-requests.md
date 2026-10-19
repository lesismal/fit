# APIs fit needs from fib

[English](fib-api-requests.md) | [简体中文](fib-api-requests.zh-CN.md)

A review of M0 and M1 against the [development rules](rules.md) found places where fit cannot
follow them with fib v1.3.0's API alone. This document lists what fit asks of fib, why, and how fit
would use each API. The names and signatures are suggestions; fib decides the final shape.

The fit iterations that depend on these are R03–R06 and R07c in the [development plan](dev-plan.md#m1-review-fixes).

## Summary

| ID | API | Priority | Rules it unblocks | fit iterations |
| --- | --- | --- | --- | --- |
| A1 | `ClientResponse.HoldBody(hold bool)` | P0 | 1, 8 | R04 |
| A2 | A response write that never waits, and a writable notification | P0 | 1, 8, 9 | R04, R05 |
| A3 | Asynchronous file sending | P1 | 1, 4 | R05 |
| A4 | Engine connection limit | P1 | 11 | R06 |
| A5 | Client limit on requests waiting for a connection | P1 | 11 | R06 |
| A6 | Trailers set on a `BodyWriter` before it closes | P2 | 1, 4 | R04 |
| A7 | A per-request value slot on `Context` | P2 | 12 | R07c |
| A8 | Documentation fixes | P2 | — | — |
| A9 | Refuse a header field name that is not a token | P1 | 13 | — |
| A10 | Stream a client response of unknown length from its header | done, v1.3.1 | 5 | — |
| A11 | The body of a client response that arrived whole, without a copy | P2 | 12 | R07c |
| A12 | Fewer timers and contexts per client request | P2 | 12 | R07c |
| A13 | No header copy for a response that only `OnFinish` hooks | P1 | 12 | R07c |

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

**Problem.** `fib.Config` has no limit on open connections. Rule 11 requires one, with a safe
default.

**Request.** `Config.MaxConns int` (and optionally per `Binding`): past it, accepted connections are
closed at once (or accepting pauses), counted in a statistic. Zero keeps today's behaviour.

## A5: Client limit on requests waiting for a connection

**Problem.** With `MaxConnsPerHost` reached, requests wait for a connection with no limit on how
many wait; only their context bounds how long.

**Request.** `ClientConfig.MaxPendingPerHost int`: a request beyond it fails at once with a
distinct error (for example `ErrTooManyPending`), which fit maps to 503.

## A6: Trailers set on a `BodyWriter` before it closes

**Problem.** fib's client reads a request's `Trailer` when it sends the header. A body whose trailer
values are known only when it ends (a client upload with trailers) cannot use `BodyWriter`, so fit
uses a blocking pipe read on the client's own goroutine, one goroutine per such upload.

**Request.** `BodyWriter.CloseWithTrailer(trailer http.Header) error`, or the client reading the
request's `Trailer` after the body's EOF rather than with the header.

## A7: A per-request value slot on `Context`

**Problem.** fit attaches its access log entry to a request with `r.WithContext`, which copies the
whole `http.Request` and allocates a context value on every request.

**Request.** `Context.SetValue(v any)` and `Context.Value() any` (one slot, cleared when the
`Context` is recycled), or a small keyed map with the same lifetime.

## A8: Documentation fixes

- `StreamResponseBody` says a stalled reader "never holds up the other streams on the connection".
  That holds for a reader of `Body`, but not for an `OnBody` function that waits, which runs on the
  connection's reader. Say so.
- `Config.WriteTimeout` defaults to 0, so a streaming `Write` to a client that stops reading waits
  forever. Consider a per-wait bound (`WriteStallTimeout`) with a non-zero default, separate from the
  whole-response `WriteTimeout`, which long downloads cannot use.

## A9: Refuse a header field name that is not a token

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
