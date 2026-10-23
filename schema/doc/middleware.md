A route's `middleware` is a list of these, each named by `module`, with its `params`, which a
request passes through in order before the handler; a middleware may answer itself, as
authentication does when it refuses.

| Module | What it does | Caddyfile |
| --- | --- | --- |
| [`basic_auth`](#http-middleware-basic_auth) | A user and password, against bcrypt hashes | `basic_auth` |
| [`body_limit`](#http-middleware-body_limit) | Bounds request bodies | `request_body { max_size }` |
| [`cache`](#http-middleware-cache) | An HTTP cache, in memory and on disk | none |
| [`circuit_breaker`](#http-middleware-circuit_breaker) | Stops sending requests on while they fail | none |
| [`client_cert`](#http-middleware-client_cert) | Requires a client certificate | none (`tls { client_auth }` on the listener) |
| [`compress`](#http-middleware-compress) | zstd, brotli and gzip | `encode` |
| [`concurrency_limit`](#http-middleware-concurrency_limit) | Bounds the requests under way | none |
| [`forward_auth`](#http-middleware-forward_auth) | Asks an auth service | `forward_auth` |
| [`headers`](#http-middleware-headers) | Sets, adds and deletes fields | `header`, `request_header`, `header_up`, `header_down` |
| [`jwt`](#http-middleware-jwt) | Verifies JSON Web Tokens | none |
| [`rate_limit`](#http-middleware-rate_limit) | Limits the rate of requests | none |
| [`redirect`](#http-middleware-redirect) | Redirects | `redir` |
| [`rewrite`](#http-middleware-rewrite) | Rewrites the path | `rewrite`, `uri`, `handle_path` |
| [`wasm`](#http-middleware-wasm) | A WebAssembly plugin | none |
