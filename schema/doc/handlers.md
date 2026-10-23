A route's `handler` answers its requests: one of these, named by `module`, with its `params`.

| Module | What it does | Caddyfile |
| --- | --- | --- |
| [`fastcgi`](#http-handlers-fastcgi) | A FastCGI application, as PHP-FPM | `php_fastcgi` |
| [`forward_proxy`](#http-handlers-forward_proxy) | A forward proxy for HTTP CONNECT | none |
| [`proxy`](#http-handlers-proxy) | A reverse proxy to an upstream | `reverse_proxy` |
| [`respond`](#http-handlers-respond) | A fixed status, fields and body | `respond`, `error` |
| [`static`](#http-handlers-static) | Files from a directory | `file_server`, `root`, `try_files` |
