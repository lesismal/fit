# 012: The Gateway API's conformance suite, in a module of its own

[English](012-gateway-conformance.md) | [简体中文](012-gateway-conformance.zh-CN.md)

Status: accepted, in I61 (2026-10-22).

## Context

I61 runs the Gateway API's conformance suite against fit's Kubernetes provider. The suite is the
Go package `sigs.k8s.io/gateway-api/conformance`: a test that applies its manifests to a cluster,
waits for the implementation to accept and program them, and sends requests to the Gateways'
addresses. It needs client-go, controller-runtime and the Gateway API's own types, the forty-odd
modules decision [011](011-kubernetes-client.md) kept out of fit.

## Decision

- **`test/conformance` is a Go module of its own**, `github.com/lesismal/fit/test/conformance`,
  which requires `sigs.k8s.io/gateway-api` v1.3.0, the version of the CRDs CI installs. Its one
  test calls the suite with its flags. fit's own module, its `go.mod`, its binary and its
  `go test ./...` are untouched: a directory with a `go.mod` is not part of the module above it.
  The module needs nothing of fit's, so it has no `replace` and no `go.work`.
- **fit runs outside the cluster**, as in I58's e2e test: `test/conformance/run.sh` makes fit's
  GatewayClass, starts the `fit` binary with the Kubernetes provider, Gateways bound on
  127.0.0.1 and that address written into their status (the provider's `addresses`, which I61
  adds), and runs the suite's test binary with the profile fit claims, `GATEWAY-HTTP`, writing
  its report. The pods are reached through a route to the kind node, as in I58.
- **CI runs it in a job of its own**, "Gateway API conformance (kind)", and keeps the report and
  fit's log as an artifact. The benchmark host runs the same script against a kind cluster of its
  own, for the work that fixes failures.

## Consequences

- The suite's modules are downloaded and built only by that job, and by whoever runs it.
- Moving to a newer Gateway API is the module's `require`, the CRDs CI installs, and the
  failures the newer suite finds.
- The profile claimed is written in `run.sh`; a feature claimed later is a flag there.
