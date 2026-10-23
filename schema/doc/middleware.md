A route's `middleware` is a list of these, each named by `module`, with its `params`, which a
request passes through in order before the handler; a middleware may answer itself, as
authentication does when it refuses.

| Module | What it does |
| --- | --- |
| [`basic_auth`](#http-middleware-basic_auth) | A user and password, against bcrypt hashes |
| [`body_limit`](#http-middleware-body_limit) | Bounds request bodies |
| [`cache`](#http-middleware-cache) | An HTTP cache, in memory and on disk |
| [`circuit_breaker`](#http-middleware-circuit_breaker) | Stops sending requests on while they fail |
| [`client_cert`](#http-middleware-client_cert) | Requires a client certificate |
| [`compress`](#http-middleware-compress) | zstd, brotli and gzip |
| [`concurrency_limit`](#http-middleware-concurrency_limit) | Bounds the requests under way |
| [`forward_auth`](#http-middleware-forward_auth) | Asks an auth service |
| [`headers`](#http-middleware-headers) | Sets, adds and deletes fields |
| [`jwt`](#http-middleware-jwt) | Verifies JSON Web Tokens |
| [`rate_limit`](#http-middleware-rate_limit) | Limits the rate of requests |
| [`redirect`](#http-middleware-redirect) | Redirects |
| [`rewrite`](#http-middleware-rewrite) | Rewrites the path |
| [`wasm`](#http-middleware-wasm) | A WebAssembly plugin |
