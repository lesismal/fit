For PHP applications behind PHP-FPM, or any FastCGI application. `try_files` sends paths that are
no script of their own to a front controller, as most PHP frameworks want. Request fields whose
names hold an underscore are not passed on, as nginx leaves them out: `X_User` would be
`HTTP_X_USER` as `X-User` is, and stand in for one a middleware set; `underscores_in_headers`
passes them on.

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
