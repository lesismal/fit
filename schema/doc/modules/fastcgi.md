For PHP applications behind PHP-FPM, or any FastCGI application. `try_files` sends paths that are
no script of their own to a front controller, as most PHP frameworks want. A script whose path has
a segment that starts with a dot, as `/.git/x.php` or `/.env`, is not run but answered `404`, and
try_files passes over it, but `/.well-known/`; `dotfiles: allow` runs it.

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
