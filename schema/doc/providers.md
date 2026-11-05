Providers add listeners, routes, upstreams and services from outside the config, following them as
they change: Docker containers' labels, a Consul catalog's tags, keys in Consul's KV store or
etcd, and a Kubernetes cluster's Gateway API resources and Ingresses. What the config writes wins
over every provider, and a provider over those after it; the Admin API's `/config/effective`
shows the result and where each part came from. Read when fit starts.

```json
{
  "listeners": [{"name": "web", "address": ":80"}],
  "providers": [
    {"docker": {"endpoint": "unix:///var/run/docker.sock"}},
    {"name": "kv", "etcd": {"endpoints": ["http://etcd:2379"], "prefix": "fit/"}}
  ]
}
```

Whoever writes what a provider reads is trusted no further than the provider's settings say. The
routes Docker labels and Consul tags write may match only the hosts `allowed_hosts` lists (none
until it is set), have a priority of at most `max_priority` (0), and use only the proxy, to their
own upstream, and the middleware headers, rewrite, redirect, compress, rate_limit,
concurrency_limit, body_limit, basic_auth and circuit_breaker, unless `allowed_modules` names more;
containers and instances at loopback, link-local or unspecified addresses are left out unless
`allow_local_addresses` is set. The Kubernetes provider sends to no ExternalName Service unless
`external_name_services` is set, and never to a local name. What a provider leaves out, it reports,
and the rest is served: a route whose values the config would refuse is left out, or answers 500,
rather than have the provider's whole part refused.
