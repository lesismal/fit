# M1 conformance, 2026-10-19

[I23](../../docs/dev-plan.md#i23-m1-conformance-and-benchmarks), first part: conformance runs on a
development Mac (Apple M3 Max, macOS), fit at the commit that adds this file, fib v1.3.0. The
benchmarks, the second part, run on the benchmark host and are not here yet.

## h2spec

h2spec 2.0.0 (built from `github.com/summerwind/h2spec` at its latest commit), every section, with
`-o 3`:

| Entry | What answers | Result |
| --- | --- | --- |
| h2c (prior knowledge), `127.0.0.1:19081` | `respond` | 145 tests, 145 passed |
| h2 over TLS 1.3, `127.0.0.1:19443` | `respond` | 145 tests, 145 passed |
| h2c | `proxy` to an HTTP/1.1 upstream | 145 tests, 145 passed |
| h2 over TLS | `proxy` to an HTTP/1.1 upstream | 145 tests, 145 passed |

## Proxy conformance

`test/e2e/conformance_test.go` sends requests byte by byte to the `fit` binary in front of a
backend that records what reaches it, for what RFC 9110 and RFC 9112 ask of an intermediary:

- refused with 400, with nothing reaching the upstream: conflicting `Content-Length`s, whitespace
  before a colon, no `Host` in HTTP/1.1, two `Host`s, a bad chunk size, a NUL in a field; a request
  with both `Content-Length` and `Transfer-Encoding` never lets a smuggled request reach the
  upstream; a header past the limit gets 431;
- obs-fold is replaced with a space, which RFC 9112 section 5.2 allows;
- `Expect: 100-continue` gets its 100 before the body is sent, and the body arrives whole;
- an HTTP/1.0 client gets a body that is not chunked;
- pipelined requests are answered in order;
- an absolute-form target reaches the upstream in origin form;
- 304, 204 and 1xx from the upstream pass through without a body where there is none, and the
  connection stays usable;
- an upstream that does not speak HTTP gives 502;
- `Connection: close` from the client closes the connection; from the upstream it stays the
  upstream's.

## Found and fixed

- **Whitespace before a colon.** fib v1.3.0 accepted `X-A : one`, keeping a field named `X-A `,
  where RFC 9112 section 5.1 asks a server to refuse it with 400: its fast parser refuses such a
  name and hands the head to net/http's reader, which keeps it, and net/http's server, unlike fib's,
  checks names afterwards. fit's proxy now refuses a request with a field name that is not a token
  (in the loop that copies the header anyway), so such a field never reaches an upstream. The fix
  in fib, with conformance cases for it, is on fib's branch `fix-field-name-whitespace`, for a fib
  release; fit's check stays as defence in depth.
