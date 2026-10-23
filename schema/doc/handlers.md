A route's `handler` answers its requests: one of these, named by `module`, with its `params`.

| Module | What it does |
| --- | --- |
| [`fastcgi`](#http-handlers-fastcgi) | A FastCGI application, as PHP-FPM |
| [`forward_proxy`](#http-handlers-forward_proxy) | A forward proxy for HTTP CONNECT |
| [`proxy`](#http-handlers-proxy) | A reverse proxy to an upstream |
| [`respond`](#http-handlers-respond) | A fixed status, fields and body |
| [`static`](#http-handlers-static) | Files from a directory |
