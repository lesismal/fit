For PHP applications behind PHP-FPM, or any FastCGI application. `try_files` sends paths that are
no script of their own to a front controller, as most PHP frameworks want.

```caddyfile
example.com {
	php_fastcgi unix//run/php/php-fpm.sock {
		root /var/www/app/public
	}
}
```

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
