The Admin API reads and replaces the running config: `GET`, `PUT`, `PATCH` and `DELETE` on
`/config/` and a JSON pointer into it, `POST /load` for a whole config, ETags against lost updates,
the configs applied (`/versions`) and rollback to one. It also serves metrics at `/metrics`, the
effective config with providers merged, and a read-only dashboard. It listens on 127.0.0.1:2019
unless told otherwise, and needs `token_file` to listen anywhere but a loopback address. It is
off when `admin` is left out. Read when fit starts. `mptcp` has it listen over Multipath TCP
where the kernel offers it (Linux 5.6+, with `net.mptcp.enabled`), falling back to plain TCP;
off by default, and a Unix socket has no paths.

`admin.store` keeps the config encrypted in a file of its own, the store: the file fit is started
with then holds `admin`, and the store the rest, which fit runs from and writes on every change.
When the store does not exist yet, the rest of the file is its first content. `fit store export`
and `fit store import` read and write it. `admin.console` serves the console, web pages behind a
user name and password from which every part of the config but `admin` is changed, with a form
made from the schema and a JSON editor; off a loopback address it needs `cert_file` and
`key_file`. Its `mptcp` does the same for the console's listener. See
[the console and the store](../console.md) and
[decision 027](../decisions/027-console-and-store.md).

```json
{
  "admin": {
    "address": "127.0.0.1:2019",
    "history": 20,
    "store": {"path": "/var/lib/fit/fit.store", "password_env": "FIT_STORE_PASSWORD"},
    "console": {
      "address": "127.0.0.1:2020",
      "users": [{"username": "admin", "password_hash": "$2a$10$sKGBAOOCI4p4WWJcVCuAH.ivT8pNy2DFbs6pd8xmtNEZ/5L1SeLvm"}]
    }
  },
  "routes": [{"match": {"host": ["example.com"]}, "handler": {"module": "respond", "params": {"body": "hello"}}}]
}
```
