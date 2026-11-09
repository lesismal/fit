Serves the `bittorrent.tracker` role's announces and scrapes over HTTP, and HTTPS on a TLS
listener, through fib's tracker: a request whose path's last segment begins with `announce` is an
announce, one beginning with `scrape` a scrape, whatever comes before: a route matching
`/tracker/*` serves `/tracker/announce` and a private tracker's `/tracker/<passkey>/announce`
alike. Refusals (an info hash the allow-list lacks, a passkey refused, an address the role's `access` refuses, a full store) are answered `200` with a
bencoded failure reason, as BEP 3 has them; another path is `404`, and `503` while the role does
not run. It answers on the worker that read the request, never waiting. The passkey is replaced by
`REDACTED` in the request URI the access log writes.

```json route
{
  "match": {"path": ["/announce", "/scrape", "/tracker/*"]},
  "handler": {"module": "bt_tracker"}
}
```
