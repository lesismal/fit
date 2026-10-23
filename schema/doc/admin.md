The Admin API reads and replaces the running config: `GET`, `PUT`, `PATCH` and `DELETE` on
`/config/` and a JSON pointer into it, `POST /load` for a whole config, ETags against lost updates,
the configs applied (`/versions`) and rollback to one. It also serves metrics at `/metrics`, the
effective config with providers merged, and a read-only dashboard. It listens on 127.0.0.1:2019
unless told otherwise, and needs `token_file` to listen anywhere but a loopback address. It is
off when `admin` is left out. Read when fit starts.

```json
{
  "admin": {"address": "127.0.0.1:2019", "history": 20},
  "routes": [{"match": {"host": ["example.com"]}, "handler": {"module": "respond", "params": {"body": "hello"}}}]
}
```
