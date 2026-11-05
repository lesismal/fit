Serves files from `root`, small ones from memory, with listings (`browse`), precompressed twins,
and `try_files` for single-page apps. A path with a segment that starts with a dot, as
`/.git/config`, `/.env` or `/.htpasswd`, is answered `404`, as a file not there is, and so are
try_files candidates of that kind, but `/.well-known/` under the root (ACME's HTTP-01 challenge,
security.txt); `dotfiles: allow` serves them. Listings leave dotfiles out either way. On Windows,
where NTFS also names `.git` `GIT~1`, a segment shaped as an 8.3 short name (up to eight
characters ending in `~` and digits, an extension of up to three) is answered `404` too, a file
whose own name has that shape, as `file~1.txt`, as well. On Windows, too, a path with a segment
Windows opens as another file or as a device is answered `400`, whatever `dotfiles` says, and a
try_files candidate of that kind is passed over: one with a colon, which names a stream
(`x.php::$DATA` opens `x.php`), or with another character no Windows name has (`"*<>?|` and
control characters); one ending in a dot or a space, which Windows strips (`x.php.`, `x.php%20`);
and a reserved device name, with or without an extension (`CON`, `PRN`, `AUX`, `NUL`, `COM0` to
`COM9`, `LPT0` to `LPT9`, `CONIN$`, `CONOUT$`, as `nul.txt`). So a route matching `*.php`, a
content type or a precompressed twin chosen by the extension cannot be sidestepped by naming a
file another way. An `index` or a `try_files` entry of that kind is refused when the config is
loaded there.

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
