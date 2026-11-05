Redirects requests, every one or those whose path matches `regexp`, to a URL with placeholders.
A location that starts with more than one slash or backslash once expanded, as `/$1` does for
`/old/%2F%2Fevil.com`, starts with one: it would read as another host. Only a `to` that itself
starts with `//` redirects to a host so.

```json route
{
  "middleware": [{"module": "redirect", "params": {"regexp": "^/old/(.*)$", "to": "/new/$1", "status": 301}}],
  "handler": {"module": "respond", "params": {"body": "hello"}}
}
```
