A route says which requests it takes (`match`), the [middleware](#middleware) they pass through,
in order, and the [handler](#handlers) that answers. Each request goes to one route:

1. the routes of the listener it came on whose `match` it matches: every field of `match` must
   hold, and within a field, one of the values listed;
2. of those, the one with the highest `priority`;
3. among equal priorities, the most specific host (exact, then `*.`, then `**.`, then none), then
   the most specific path (exact, then the longest prefix), then the one with the most other
   matchers set, then the one written first.

A request no route takes is answered 404. A `match` field left out matches everything; `path`
takes exact paths and prefixes ending in `*`; `expr` combines matchers with `&&`, `||`, `!` and
parentheses, as in `Host("a.example") && (PathPrefix("/api/") || Method("POST"))`.

The path is matched once it is normalized, on every protocol: dot segments are resolved as
RFC 3986 resolves them, `%2e` counting as a dot, and repeated slashes merged, so that
`/./admin`, `/x/../admin`, `//admin` and `/x/%2e%2e/admin` all go to the route of `/admin`; `..`
at the root stays there. The normalized path is what the handler and the upstream get, other
escapes kept as the client wrote them. A path whose encoded slashes (`%2F`) would make dot or
empty segments once decoded, as `/x/..%2Fadmin` and `/%2Fadmin` do, is answered 400, as is an
HTTP/2 or HTTP/3 `:path` with a space or a control byte.

The middleware and handler of a route are modules, named by `module`, with their `params`; the
[Handlers](#handlers) and [Middleware](#middleware) chapters list every one. In a `.conf` file,
a `site` writes the routes of its host names together: each `route` in it takes the site's hosts
and its own paths, and the site's middleware before its own.

```fit
upstream api {
	addresses 127.0.0.1:9000
}

site example.com {
	route /api/* {
		name api-writes
		match {
			method POST PUT DELETE
		}
		middleware basic_auth {
			users {
				username admin
				password_hash $2a$14$Zkx19XLiW6VYouLHR5NmfOFU0z2GTNmpkT/5qqR7hx4IjWJPDhjvG
			}
		}
		handler proxy { upstream api }
	}
	route /api/* {
		name api
		handler proxy { upstream api }
	}
	route {
		name home
		handler respond { body home }
	}
}
```

```json
{
  "upstreams": [{"name": "api", "addresses": ["127.0.0.1:9000"]}],
  "routes": [
    {
      "name": "api-writes",
      "match": {"host": ["example.com"], "path": ["/api/*"], "method": ["POST", "PUT", "DELETE"]},
      "middleware": [
        {"module": "basic_auth", "params": {"users": [{"username": "admin", "password_hash": "$2a$14$Zkx19XLiW6VYouLHR5NmfOFU0z2GTNmpkT/5qqR7hx4IjWJPDhjvG"}]}}
      ],
      "handler": {"module": "proxy", "params": {"upstream": "api"}}
    },
    {
      "name": "api",
      "match": {"host": ["example.com"], "path": ["/api/*"]},
      "handler": {"module": "proxy", "params": {"upstream": "api"}}
    },
    {
      "name": "home",
      "match": {"host": ["example.com"]},
      "handler": {"module": "respond", "params": {"body": "home"}}
    }
  ]
}
```
