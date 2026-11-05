Serves files from `root`, small ones from memory, with listings (`browse`), precompressed twins,
and `try_files` for single-page apps. A path with a segment that starts with a dot, as
`/.git/config`, `/.env` or `/.htpasswd`, is answered `404`, as a file not there is, and so are
try_files candidates of that kind, but `/.well-known/` under the root (ACME's HTTP-01 challenge,
security.txt); `dotfiles: allow` serves them. Listings leave dotfiles out either way.

```json route
{
  "match": {"host": ["example.com"]},
  "handler": {"module": "static", "params": {
    "root": "/srv/www",
    "try_files": ["{path}", "{path}/", "/index.html"],
    "precompressed": ["br", "gzip"]
  }}
}
```
