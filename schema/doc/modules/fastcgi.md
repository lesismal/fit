For PHP applications behind PHP-FPM, or any FastCGI application. `try_files` sends paths that are
no script of their own to a front controller, as most PHP frameworks want. A script whose path has
a segment that starts with a dot, as `/.git/x.php` or `/.env`, is not run but answered `404`, and
try_files passes over it, but `/.well-known/`; `dotfiles: allow` runs it. On Windows, where NTFS
also names `.git` `GIT~1`, a script with a segment shaped as an 8.3 short name (up to eight
characters ending in `~` and digits, an extension of up to three) is refused the same way, one
whose own name has that shape as well. On Windows, too, a script with a segment Windows opens as
another file or as a device is not run but answered `400`, whatever `dotfiles` says, and
try_files passes over it: one with a colon, which names a stream (`x.php::$DATA` opens `x.php`),
or with another character no Windows name has; one ending in a dot or a space, which Windows
strips (`x.php.`); and a reserved device name, with or without an extension (`CON`, `PRN`, `AUX`,
`NUL`, `COM0` to `COM9`, `LPT0` to `LPT9`, `CONIN$`, `CONOUT$`, as `nul.php`). The path info after
a script is not looked at. An `index` or a `try_files` entry of that kind is refused when the
config is loaded there. On every system, a script whose path has a backslash, which Windows takes
as a separator (`/a%5C.git%5Cx.php` would run `a\.git\x.php` there), or a NUL, is answered `400`,
with try_files or without, and a `try_files` entry with one is refused when the config is loaded.
A `static` handler on a file system that folds case serves no file with a `split_path` extension,
in any case, so a route matching `(?i)\.php$` is all a shared root needs.

```json route
{
  "match": {"host": ["example.com"]},
  "handler": {"module": "fastcgi", "params": {
    "address": "unix//run/php/php-fpm.sock",
    "root": "/var/www/app/public",
    "try_files": ["{path}", "{path}/index.php", "/index.php"]
  }}
}
```
