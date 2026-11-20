Compresses responses as they are written, with the encoding the client prefers.

How hard each encoding works is `level`, one name for all three, or a number for one encoding,
which takes its place in `level`:

| `level` | `gzip_level` (1-9) | `brotli_level` (1-11) | `zstd_level` (1-22) |
| --- | --- | --- | --- |
| `fastest`, the default | 1 | 1 | 1 |
| `default` | 5 | 4 | 3 |
| `better` | 7 | 6 | 6 |
| the smallest | 9 | 11 | 10 |

zstd's encoder runs at four speeds, which its levels fall into: 1-2 fastest, 3-5 default, 6-9
better and 10-22 best. A response compressed as it goes costs its CPU on every request: on the
benchmark host, a 16 KB JSON body that takes 2.7 µs to send as it is takes 37 µs at gzip 1, 59 µs
at gzip 5 for a body 6% smaller, 44 µs at zstd 1, 81 µs at brotli 1, larger than gzip 1's, and
241 µs at brotli 4. That is why `fastest` is the default, as nginx's `gzip_comp_level` is 1, and
why gzip goes ahead of brotli when a client accepts both equally.

Every request is compressed anew. Where the same response goes out many times, compress it once:

- **Files**: the `static` handler's `compress` keeps a compressed copy of each file it holds in
  memory, made once at a level that makes it small, and its `precompressed` serves `.br`, `.zst`
  and `.gz` files made beforehand. Either answers without this middleware compressing anything;
  with both, this middleware compresses only what they do not.
- **Responses that may be kept**: put the [`cache`](#http-middleware-cache) middleware before
  `compress`. The cache then keeps each response as `compress` made it, one for each
  `Accept-Encoding` the clients send, and a request it answers is not compressed again.

```json route
{
  "match": {"path": ["/api/catalog/*"]},
  "middleware": [
    {"module": "cache", "params": {"zone": "catalog", "default_ttl": "1m"}},
    {"module": "compress", "params": {"encodings": ["zstd", "gzip"], "gzip_level": 6, "min_bytes": 512}}
  ],
  "handler": {"module": "proxy", "params": {"upstream": "app"}}
}
```
