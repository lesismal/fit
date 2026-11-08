# Cache

[English](19-cache.md) | [简体中文](19-cache.zh-CN.md)

## What it does

The cache keeps responses by RFC 9111's rules, in memory and, with `disk`, on disk, in a zone that
routes may share: caches of the same zone share the zone, its size and its keys, the zone is kept
across reloads while a cache uses it, and what is on disk is found again when fit starts. Caching
itself is enabled by the `cache` middleware on a route; this page shows the zones in use and their
size, and purges by URL, by tag or everything.

A key is a response's scheme + host + target, which is the URL the request named; each variant `Vary`
names has a key of its own. So the URLs in the purge card must be absolute, written as the requests
name them. A response is fresh for the lifetime it gives itself (`s-maxage`, `max-age`, `Expires`), and
only when it gives none is `default_ttl` used; a response that says `must-revalidate`,
`proxy-revalidate` or `s-maxage` is never answered stale. Conditional requests
(`If-Modified-Since`, `If-None-Match`) are answered from the cache itself.

The tags come from the `Cache-Tag` or `Surrogate-Key` headers, written on the response, and purging by
tag removes the whole batch. A request that can change what its target holds — not GET, HEAD, OPTIONS,
TRACE or CONNECT — makes the stored responses for its target out of date when it succeeds. A response is
kept only after all of it has gone: it passes through a fib body filter that copies what passes, so a
response is never held back to be stored. The disk is read and written on a pool of bounded goroutines
of its own, never by a fib worker.

## Pages

### Cache zones (`#/gw.cache/zones`)

The Cache module has one page in the menu: Cache zones. It edits no config; it reads the running state
(the Admin API's `GET /cache`).

- **The zones table** (refreshed every 5 seconds): each zone's name, its keys, the memory bytes, the
  disk entries and bytes, and the disk path; each row has a **Purge all** button (run only after
  confirming). When no zone is in use the page says so: add the `cache` middleware to a route.
- **The purge card**: choose a zone (or "Every zone"), enter URLs (one per line, absolute, as requests
  name them), tags (Cache-Tag / Surrogate-Key, one per line), or check "Everything in the zone"; at
  least one of the three is needed, or the page says so. Purging everything asks for confirmation. The
  result gives the number of keys purged.
- Purging goes through the Admin API's `POST /cache/purge` (the body `{"zone", "urls", "prefixes",
  "tags", "all"}`) and takes effect at once, not through the config draft and with no new version. The
  card has only URLs, tags and everything; `prefixes` is an API-only field.

Caching is enabled on a route, in the route's form, on the
[Reverse proxy](15-reverse-proxy.md) page. The Engine page lists "Cache zones" under the port, linking
back here, and "Cached routes", linking to the routes page.

## Key parameters

The `cache` middleware on a route (the key fields); the full field table is in
[the config reference: Middleware](../reference/config.md#http-middleware-cache).

| field | default | description |
| --- | --- | --- |
| `zone` | `default` | The store responses are kept in; caches of the same zone, in one route or several, share it, its size and its keys, and it is kept across reloads while a cache uses it. |
| `max_bytes` | `128 MiB` | The most bytes the zone keeps in memory, bodies, fields and keys counted; past it the least recently used responses go. Caches of one zone take the size of the last one loaded. |
| `max_entry_bytes` | `1 MiB` | The largest body kept; a larger response goes to its client without being kept. At most a sixteenth of `max_bytes`. |
| `default_ttl` | a tenth of the time since Last-Modified, at most a day | How long a response is fresh for when it gives no lifetime of its own (no `s-maxage`, `max-age` or `Expires`) and its status allows one to be assumed (200, 203, 300, 301, 308, 404, 405, 410, 414, 501). |
| `stale_while_revalidate` | none | How long past its lifetime a response may still be answered while the cache fetches it again in the background (RFC 5861). |
| `stale_if_error` | none | How long past its lifetime a response may still be answered when fetching it again fails, with no response or a 500, 502, 503 or 504 (RFC 5861). |
| `disable_lock` | off | When off, a request that misses while another fetches the same response waits for that fetch (up to `lock_timeout`) and is answered from what the cache keeps, so many requests for one response make one fetch; when on, every request that misses fetches for itself. |
| `lock_timeout` | `5s` | How long a request waits for another's fetch before it goes on to fetch for itself. |
| `disk` | none | A disk tier for the zone: every response kept in memory is written to disk too, and one the memory no longer holds is read back from disk and kept in memory again. Caches of one zone take the disk of the last one loaded. |

Inside `disk`: `path` is required (the zone's alone, made if missing, found again at start),
`max_bytes` (default `1 GiB`) and `max_entries` (default `100000`, which bounds the memory the index
takes, a few hundred bytes each).

## In .conf

One listener, one upstream and one route using the cache. This is a self-contained complete config, and
it passes `fit validate`:

```fit
listener web :8080 http/1.1 h2c

upstream app 127.0.0.1:9000

route {
	match { path /assets/* }
	middleware cache {
		zone assets
		max_bytes 256MiB
		default_ttl 10m
		stale_while_revalidate 1m
		stale_if_error 5m
		disk { path /tmp/fit-cache/assets; max_bytes 10GiB }
	}
	handler proxy { upstream app }
}
```

`fit validate` creates `disk.path`, so it has to be somewhere the process can make a directory; a path
it cannot create is an error. Leave `disk` out for a memory-only cache. `max_bytes 256MiB` and `10GiB`
are the .conf way of writing them; in JSON they are byte counts.

## See also

- [Reverse proxy](15-reverse-proxy.md): routes and middleware, and where `cache` sits on a route.
- [Engine](06-engine.md): where a port lists the cache zones and the cached routes.
- [Consistency check](10-consistency-check.md): a zone a route uses but the runtime does not have is drift.
- [the config reference: Middleware](../reference/config.md#http-middleware-cache): every field of `cache`.
- [the Admin API](../reference/config.md#admin-api): `GET /cache` and `POST /cache/purge`.
- [the cache module doc](../../schema/doc/modules/cache.md): what the middleware does, with an example.
- [the complete config template](../../templates/conf/conf.d/60-apps.conf): every field, commented.
