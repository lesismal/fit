# 011: A Kubernetes client of fit's own, on net/http, and a YAML parser for kubeconfig

[English](011-kubernetes-client.md) | [简体中文](011-kubernetes-client.zh-CN.md)

Status: accepted, in I58 (2026-10-22).

## Context

I58's Kubernetes provider watches GatewayClasses, Gateways and HTTPRoutes, and the Services,
EndpointSlices, Namespaces and ReferenceGrants they refer to. The usual way is client-go with
informers, and the Gateway API's Go types from `sigs.k8s.io/gateway-api`. The alternative is a
client of fit's own: the API server's REST API is JSON over HTTPS, and a watch is a stream of JSON
events.

## Decision

- **No client-go.** client-go, apimachinery and k8s.io/api, with the Gateway API's types, add some
  forty modules (protobuf, gnostic, klog, structured-merge-diff, OpenAPI, the Gateway API's own
  generated clients) and tens of megabytes to a binary that today has four dependencies, for a
  provider many deployments never turn on. What fit needs of them is small: list a resource with
  paging, watch it from the list's resourceVersion, list again when the watch says 410 Gone, and
  decode a few fields of seven kinds. `provider/kubernetes` does that on `net/http`: a list, then a
  watch with bookmarks, kept per kind in a store, with types of its own that hold only the fields
  read. Tests run it against a fake API server.
- **Credentials as kubectl finds them, without exec plugins.** In a pod: the service account's
  token (read again as it rotates), CA and `KUBERNETES_SERVICE_HOST`. Outside: a kubeconfig file,
  its current or a named context, with a CA, client certificate and key (files or data), a token or
  token file, or `insecure-skip-tls-verify`. `exec` and `auth-provider` plugins, which cloud
  clusters use for short-lived credentials, are refused with a message; run fit in the cluster,
  or with a token, there.
- **`go.yaml.in/yaml/v3` reads kubeconfig files**, which are YAML. It is the YAML organisation's
  maintained continuation of `gopkg.in/yaml.v3`, pure Go, with no dependencies of its own; kind,
  kubeadm and every cloud write kubeconfigs that need a real YAML parser (block and flow styles,
  quoted and plain scalars). fit's config adapters for YAML (roadmap) can use it later.
- **Bounded and in the control plane.** The provider runs on goroutines of its own. Each kind holds
  at most `max_objects` objects (10000); requests other than watches have `request_timeout`; watches
  end every five minutes and are made again; a failing list is tried again 1s to 30s apart. A kind
  whose resource is not installed (404, as when the Gateway API's CRDs are missing) counts as empty
  and is looked for again.

## Consequences

- One new dependency, `go.yaml.in/yaml/v3`.
- Status written back to resources (I59) uses the same client: a JSON merge patch of the status
  subresource that names the resource version it was made from, so that a write from an older
  object is refused (409) rather than overwriting a newer one.
- A kind the provider needs later is a type of its own and a line in the list of what it watches.
  A kind may name older versions to fall back to, listed in turn while the server answers 404
  (I59's TLSRoutes: v1, v1alpha3, v1alpha2), and a field selector (I59's Secrets, of type
  `kubernetes.io/tls` only, so that other Secrets are neither read nor kept).
