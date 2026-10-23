Providers add listeners, routes, upstreams and services from outside the config, following them as
they change: Docker containers' labels, a Consul catalog's tags, keys in Consul's KV store or
etcd, and a Kubernetes cluster's Gateway API resources and Ingresses. What the config writes wins
over every provider, and a provider over those after it; the Admin API's `/config/effective`
shows the result and where each part came from. Read when fit starts. A Caddyfile has no
providers.

```json
{
  "listeners": [{"name": "web", "address": ":80"}],
  "providers": [
    {"docker": {"endpoint": "unix:///var/run/docker.sock"}},
    {"name": "kv", "etcd": {"endpoints": ["http://etcd:2379"], "prefix": "fit/"}}
  ]
}
```
