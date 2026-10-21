# fit service mesh data plane development plan

[English](mesh-dev-plan.md) | [简体中文](mesh-dev-plan.zh-CN.md)

This plan breaks the [service mesh data plane roadmap](mesh-roadmap.md) into small iterations,
numbered from `S01`, apart from the gateway's `I` and `R` iterations. Iterations are sized, finished
and worked in sessions as in the [gateway development plan](dev-plan.md#how-to-work-an-iteration).

- [How to work an iteration](#how-to-work-an-iteration)
- [Package layout](#package-layout)
- [Index](#index)
- [S0 Groundwork and baseline](#s0-groundwork-and-baseline)
- [S1 Identity and HBONE](#s1-identity-and-hbone)
- [S2 Node proxy](#s2-node-proxy)
- [S3 Waypoint](#s3-waypoint)
- [S4 Operations and resilience](#s4-operations-and-resilience)
- [S5 Standalone mode and multiple networks](#s5-standalone-mode-and-multiple-networks)
- [Mesh 1.0](#mesh-10)

## How to work an iteration

As in the [gateway development plan](dev-plan.md#how-to-work-an-iteration), and also:

- **Pick one.** Take the first iteration in the [index](#index) whose status is `todo` and whose
  dependencies are all `done`; an `I` number among the dependencies is an iteration of the gateway
  development plan.
- **Read only what you need.** [Progress](progress.md), this file's index and the iteration's own
  section, the roadmap's [design principles](mesh-roadmap.md#design-principles), and the packages
  and references the iteration lists.
- **Linux only.** The packages under `mesh/` build for Linux only, and their tests run on Linux (CI
  or the benchmark host). End-to-end tests that need kind and Istio live in `test/mesh` and run only
  for changes that touch them.
- **Check against upstream.** An iteration that touches Istio's protocols (ZDS, WDS, the CA, HBONE)
  goes by the source and protos of an Istio version in the supported range, and records that version
  in progress.
- **Finish.** Mark the status in both of these files, and record the iteration in both
  [progress](progress.md) files, beside the gateway's iterations.

**Session prompt:**

> Implement iteration SXX in docs/mesh-dev-plan.md. First read docs/progress.md, the index and the
> iteration's section of docs/mesh-dev-plan.md, and the design principles in docs/mesh-roadmap.md,
> then only the packages it lists. Keep the change within the iteration, add tests, and when it is
> done mark it done in both mesh-dev-plan files and record it in both progress files.

## Package layout

Set up by S01 and filled in by later iterations. Names may change.

| Package | Contents |
| --- | --- |
| `mesh/identity` | SPIFFE IDs, the certificate cache and rotation, CA clients |
| `mesh/hbone` | The HBONE server, client and connection pool |
| `mesh/xds` | The delta ADS client |
| `mesh/wds` | Workload discovery: compiling resources into an immutable snapshot |
| `mesh/zds` | The ZDS protocol with istio-cni |
| `mesh/netns` | The dedicated pool that enters network namespaces to create sockets |
| `mesh/node` | The node proxy: outbound, inbound, pod lifecycle |
| `mesh/authz` | Layer 4 and layer 7 authorization policy |
| `mesh/telemetry` | Mesh metrics and logs |
| `mesh/waypoint` | The waypoint's HBONE entry and exit |
| `mesh/meshd` | The light control plane of standalone mode (S5) |
| `test/mesh` | End-to-end tests with kind and Istio; the network namespace simulation |

## Index

Status: `todo`, `doing` or `done`.

| ID | Iteration | Depends on | Status |
| --- | --- | --- | --- |
| **S0** | | | |
| S01 | Decision notes and package skeleton | — | todo |
| S02 | Mesh test environments | S01 | todo |
| S03 | ztunnel baseline | S02 | todo |
| S04 | fib: HTTP/2 client CONNECT tunnels (F1) | S01 | todo |
| S05 | fib: descriptors, socket options and original destinations (F2–F4) | S01 | todo |
| **S1** | | | |
| S06 | SPIFFE identities and the certificate cache | S01 | todo |
| S07 | Istio CA client | S06, I49 | todo |
| S08 | HBONE server | S06 | todo |
| S09 | HBONE client and connection pool | S04, S06 | todo |
| S10 | HBONE interoperability with ztunnel | S08, S09, S02 | todo |
| **S2** | | | |
| S11 | xDS client | I49 | todo |
| S12 | Workload discovery snapshot | S11 | todo |
| S13 | Network namespace pool | S05 | todo |
| S14 | ZDS and pod lifecycle | S13 | todo |
| S15 | Outbound path | S09, S12, S14 | todo |
| S16 | Inbound path | S08, S14 | todo |
| S17 | Layer 4 authorization | S12, S16 | todo |
| S18 | Layer 4 telemetry | S15, S16 | todo |
| S19 | Node proxy exit tests | S07, S10, S17, S18, S03 | todo |
| **S3** | | | |
| S20 | Waypoint GatewayClass and pointing services at it | S19, I58 | todo |
| S21 | Waypoint data path | S20, S09, S08 | todo |
| S22 | GAMMA routes | S21, I59 | todo |
| S23 | Layer 7 authorization and authentication | S21, I42 | todo |
| S24 | Layer 7 telemetry and tracing | S21, I65 | todo |
| S25 | Waypoint exit tests | S22, S23, S24 | todo |
| **S4** | | | |
| S26 | Upgrading the node proxy without interruption | S19 | todo |
| S27 | Overload protection and per-pod limits | S19 | todo |
| S28 | DNS proxy | S19 | todo |
| S29 | Debug endpoints and `fit mesh` | S19 | todo |
| S30 | Packaging and installation | S26 | todo |
| **S5** | | | |
| S31 | fit-meshd workload discovery | S19 | todo |
| S32 | SPIRE identities | S06 | todo |
| S33 | Double HBONE across networks | S25 | todo |
| S34 | Evaluating sidecar mode | S25 | todo |
| **Mesh 1.0** | | | |
| S35 | Istio compatibility matrix | S25, S30 | todo |
| S36 | Mesh documentation | S35 | todo |

## S0 Groundwork and baseline

### S01 Decision notes and package skeleton

- **Do**: decision notes on ambient first, the range of Istio versions supported, the protobuf and
  gRPC dependencies and keeping them inside `mesh/`, the waypoint through GAMMA rather than Envoy's
  xDS, and how istiod supports third-party waypoints (checked against Istio's source); a `mesh/`
  package skeleton that builds on Linux only; a `fit node` subcommand that reads the node proxy's
  config and refuses to run off Linux.
- **Done when**: the decision notes are committed; `fit node --help` works; builds for other systems
  leave `mesh/` out.
- **Touches**: `docs/decisions`, `mesh`, `cmd/fit`.

### S02 Mesh test environments

- **Do**: two environments in `test/mesh`: kind with Istio ambient in CI, where the ztunnel image can
  be swapped for fit-node; and a script that simulates several pods with `ip netns` and veth pairs,
  for the benchmark host, with no Kubernetes.
- **Done when**: CI brings up a kind cluster and runs bookinfo with the stock ztunnel; the script
  builds and tears down a two-pod environment on the host.
- **Touches**: `test/mesh`, CI config.

### S03 ztunnel baseline

- **Do**: in the simulated environment on the benchmark host, run ztunnel in its local config mode
  and measure throughput, p50/p99 latency, memory per connection and CPU, against direct
  connections.
- **Done when**: the results are committed to `bench/results` with the ztunnel version and config
  used.
- **Touches**: `bench`, `test/mesh`.

### S04 fib: HTTP/2 client CONNECT tunnels (F1)

- **Do**: in fib: HTTP/2 clients open CONNECT and extended CONNECT streams, each a `Tunnel` with
  flow control and backpressure, many tunnels to a connection within the peer's limit on concurrent
  streams. Write it into both fib-api-requests files first, then implement and push it by fib's
  standards.
- **Done when**: fib's tests, the race detector, vet and staticcheck pass, and fib's CI passes; fit
  moves to the commit by its hash.
- **Touches**: fib `http`; fit `docs/fib-api-requests*.md`, `go.mod`.
- **Read**: fib `http/h2_tunnel.go`, `http/h2_client.go`.

### S05 fib: descriptors, socket options and original destinations (F2–F4)

- **Do**: in fib: adding an existing listening descriptor as a binding, and removing and draining
  it; dialing with a socket the caller created, and a hook to set socket options before the connect;
  the original destination of an accepted connection on Linux. Write them into both fib-api-requests
  files first.
- **Done when**: fib's tests and static checks pass on linux, darwin, windows and freebsd (with a
  clear unsupported error off Linux where it applies); fit moves to the commit by its hash.
- **Touches**: fib's engine, `binding.go`, `dial.go`; fit `docs/fib-api-requests*.md`, `go.mod`.

## S1 Identity and HBONE

### S06 SPIFFE identities and the certificate cache

- **Do**: parse and check SPIFFE IDs and trust domains; cache certificates and keys by identity,
  with a limit on their number, asking for a new one at half the lifetime; an interface for asking,
  for the Istio CA and SPIRE to implement; the peer's identity from a certificate's URI SAN.
- **Done when**: tests cover rotation, the limit, expiry and a wrong trust domain.
- **Touches**: `mesh/identity`.

### S07 Istio CA client

- **Do**: ask istiod's certificate service for certificates with fib's gRPC client, on behalf of the
  service accounts on the node; requests run in the control plane, with a concurrency limit and a
  timeout, backing off and retrying on failure.
- **Done when**: tests against a fake CA, and a certificate from a real istiod in kind.
- **Touches**: `mesh/identity`.
- **Read**: fib `grpc`; the proto of Istio's certificate service.

### S08 HBONE server

- **Do**: a TLS listener on 15008 that requires a client certificate and takes the peer's identity
  from it; accept HTTP/2 CONNECT, connect to the local target it names, and forward both ways with a
  `Tunnel` and backpressure; limits on streams per connection and on tunnels per identity.
- **Done when**: tests cover a failed identity check, slow peers, heavy traffic and connections cut
  midway, under the race detector.
- **Touches**: `mesh/hbone`, `listener`.
- **Read**: fib `http/h2_tunnel.go`.

### S09 HBONE client and connection pool

- **Do**: a pool of HTTP/2 mTLS connections reused by (source identity, destination address,
  destination identity); CONNECT tunnels opened on them; a new connection once one is at its stream
  limit; idle connections reclaimed; a destination with the wrong identity refused.
- **Done when**: tests cover reuse, the limits, a wrong identity, the peer's GOAWAY and cut
  connections; a benchmark of opening a tunnel (on the benchmark host).
- **Touches**: `mesh/hbone`.

### S10 HBONE interoperability with ztunnel

- **Do**: in the simulated environment, fit's HBONE client connects to ztunnel's inbound side, and
  ztunnel's outbound side to fit's HBONE server; then the same in kind.
- **Done when**: data flows both ways, and identities and headers such as `Forwarded` are what
  ztunnel expects.
- **Touches**: `test/mesh`, `mesh/hbone`.

## S2 Node proxy

### S11 xDS client

- **Do**: a delta ADS client over fib's bidirectional gRPC streams: subscriptions, ACKs and NACKs,
  reconnecting with backoff, limits on the number of resources and on message size; updates handed
  to a compiler by resource type.
- **Done when**: tests against a fake xDS server cover incremental updates, removals, NACKs and
  reconnects.
- **Touches**: `mesh/xds`.

### S12 Workload discovery snapshot

- **Do**: compile WDS's `Address` (Workload, Service) and `Authorization` resources into an immutable
  snapshot indexed by IP, service VIP and port, swapped in atomically; record each service's
  waypoint.
- **Done when**: tests cover adds, changes and removals, services and their endpoints, and waypoints;
  lookups don't allocate.
- **Touches**: `mesh/wds`.

### S13 Network namespace pool

- **Do**: a bounded, dedicated pool that enters a given network namespace on a locked thread,
  creates a socket for listening or dialing, and goes back to its own namespace, discarding the
  thread when it cannot.
- **Done when**: tests show that goroutines outside the pool never run in another namespace; when the
  pool is busy, requests wait in a bounded queue.
- **Touches**: `mesh/netns`.

### S14 ZDS and pod lifecycle

- **Do**: the ZDS protocol with istio-cni on a Unix socket, receiving pods' network namespace
  descriptors; for each pod, three listeners on 15001, 15006 and 15008 created through S13 and handed
  to fib (F2); drained and closed when the pod goes; restored from the snapshot when the node proxy
  restarts.
- **Done when**: tests cover adds, removals, repeated messages and recovery after a restart.
- **Touches**: `mesh/zds`, `mesh/node`.

### S15 Outbound path

- **Do**: on 15001, take the original destination (F4), look up the service and endpoints in the
  snapshot, and choose an endpoint with `upstream`'s load balancing; inside the mesh, go over HBONE
  to the destination node or a waypoint, outside it, pass through; backpressure both ways, connect
  and idle timeouts.
- **Done when**: two simulated pods reach each other over HBONE; destinations outside the mesh are
  passed through; slow-peer and cut-connection tests.
- **Touches**: `mesh/node`, `upstream`.

### S16 Inbound path

- **Do**: on 15008, terminate HBONE and connect to the application in the pod from the original
  source address (F3's `IP_TRANSPARENT`); on 15006, handle inbound plaintext.
- **Done when**: the application sees the calling pod's address as the source; tests cover an
  application not listening, a slow application and cut connections.
- **Touches**: `mesh/node`.

### S17 Layer 4 authorization

- **Do**: enforce the authorization policies WDS sends: ALLOW and DENY by namespace, identity, port
  and IP, DENY first; the matching structures built when the snapshot is compiled.
- **Done when**: table-driven tests cover the combinations of Istio AuthorizationPolicy's layer 4
  semantics.
- **Touches**: `mesh/authz`.

### S18 Layer 4 telemetry

- **Do**: `istio_tcp_connections_opened_total`, `istio_tcp_sent_bytes_total` and the other metrics,
  with Istio's standard labels; a connection log; a limit on label cardinality.
- **Done when**: tests that metric names and labels match ztunnel's; a test of the cardinality limit.
- **Touches**: `mesh/telemetry`, `observe`.

### S19 Node proxy exit tests

- **Do**: in kind, replace ztunnel with fit-node and run bookinfo and the layer 4 part of Istio's
  ambient integration tests; on the benchmark host, compare with S03's baseline.
- **Done when**: the tests pass in CI; the comparison is committed, and large gaps are split into new
  iterations.
- **Touches**: `test/mesh`, `bench`.

## S3 Waypoint

### S20 Waypoint GatewayClass and pointing services at it

- **Do**: a `fit-waypoint` GatewayClass and its Gateway controller (reusing M4's Kubernetes
  provider); the Gateway declares an HBONE listener; confirm that istiod points the services that use
  it at the fit waypoint in WDS.
- **Done when**: in kind, once a service is labeled with the waypoint, ztunnel or fit-node sends its
  traffic to the fit waypoint.
- **Touches**: `mesh/waypoint`, `provider/kubernetes`.

### S21 Waypoint data path

- **Do**: terminate HBONE on 15008 and hand the inner traffic to fit's HTTP and gRPC chain; go out
  over the HBONE client to the endpoints, keeping the source identity; forward traffic that is not
  HTTP at layer 4.
- **Done when**: HTTP/1.1, h2, gRPC and TCP tests through the waypoint; slow-peer and
  cut-connection tests.
- **Touches**: `mesh/waypoint`, `handler/proxy`.

### S22 GAMMA routes

- **Do**: turn HTTPRoutes and GRPCRoutes with a Service as parentRef into the waypoint's routes, and
  write their status back.
- **Done when**: the mesh profile of GAMMA in the Gateway API conformance tests passes in CI.
- **Touches**: `provider/kubernetes`, `mesh/waypoint`.

### S23 Layer 7 authorization and authentication

- **Do**: turn Istio AuthorizationPolicy's layer 7 rules (paths, methods, headers, JWT claims) and
  RequestAuthentication into M3's authentication and authorization middleware, with the source
  identity as a principal to match.
- **Done when**: table-driven tests cover the combinations apart from CUSTOM; policy tests in kind.
- **Touches**: `mesh/authz`, `middleware/auth`.

### S24 Layer 7 telemetry and tracing

- **Do**: `istio_requests_total`, `istio_request_duration_milliseconds` and the other metrics, with
  Istio's standard labels; trace context propagation.
- **Done when**: tests that metrics match the Envoy waypoint's; spans show up in a collector.
- **Touches**: `mesh/telemetry`, `observe`.

### S25 Waypoint exit tests

- **Do**: a chosen subset of Istio's waypoint integration tests; a comparison with the Envoy waypoint
  on the benchmark host.
- **Done when**: the tests pass in CI; the comparison is committed.
- **Touches**: `test/mesh`, `bench`.

## S4 Operations and resilience

### S26 Upgrading the node proxy without interruption

- **Do**: a new instance gets every pod's namespace again over ZDS and creates its listeners there
  with `SO_REUSEPORT`; the old one stops accepting, drains, and closes when its time is up.
- **Done when**: under steady traffic, an upgrade cuts no open connection and fails no new one.
- **Touches**: `mesh/node`, `mesh/zds`.

### S27 Overload protection and per-pod limits

- **Do**: per-pod limits on connections, tunnels and pending bytes; a node-wide memory watermark;
  actions in stages past the limits (no new tunnels, then no new connections).
- **Done when**: while one pod opens a flood of connections, the latency of other pods on its node
  stays within bounds (measured in the simulated environment on the benchmark host).
- **Touches**: `mesh/node`.

### S28 DNS proxy

- **Do**: capture DNS in the pod, answer the names of services in WDS directly, and forward the rest
  through an asynchronous client; a bounded cache.
- **Done when**: tests cover names in the mesh, external names and upstream timeouts.
- **Touches**: `mesh/node`.

### S29 Debug endpoints and `fit mesh`

- **Do**: the node proxy's admin port: the config snapshot, and listings of workloads, certificates
  and tunnels; a `fit mesh` command that reads them.
- **Done when**: every endpoint has tests; the endpoints are on localhost by default.
- **Touches**: `mesh/node`, `admin`, `cmd/fit`.

### S30 Packaging and installation

- **Do**: images for fit-node and the fit waypoint; how to swap the image in Istio's ztunnel chart;
  manifests for deploying the waypoint.
- **Done when**: following the docs installs it in a fresh kind cluster and runs bookinfo.
- **Touches**: `deploy`, `examples`.

## S5 Standalone mode and multiple networks

### S31 fit-meshd workload discovery

- **Do**: build WDS resources from the Kubernetes API and serve them over xDS, with no istiod;
  authorization policy from Istio's CRDs or a format of fit's own (a decision note).
- **Done when**: with no istiod installed, fit-node gets its workloads from fit-meshd and forwards
  traffic.
- **Touches**: `mesh/meshd`.

### S32 SPIRE identities

- **Do**: X.509 SVIDs for the node's workloads through the SPIFFE Workload API (Delegated
  Identity), as another implementation of S06's interface.
- **Done when**: in kind, with identities issued by SPIRE, bookinfo works over mTLS.
- **Touches**: `mesh/identity`.

### S33 Double HBONE across networks

- **Do**: double HBONE through an east-west gateway: the outer tunnel to the gateway, the inner one
  to the destination; chosen automatically when the destination is on another network.
- **Done when**: two kind clusters stand in for two networks, and services reach each other.
- **Touches**: `mesh/hbone`, `mesh/node`.

### S34 Evaluating sidecar mode

- **Do**: weigh what fit as a sidecar would give and cost (capture, the config source, living beside
  ambient), and write a decision note; if the decision is to do it, split it into new iterations.
- **Done when**: the decision note is committed.
- **Touches**: `docs/decisions`.

## Mesh 1.0

### S35 Istio compatibility matrix

- **Do**: settle the list of Istio versions supported; a scheduled CI job runs the interoperability
  tests against the matrix; protocol definitions generated per version.
- **Done when**: the matrix is published in the docs and the CI job passes.
- **Touches**: `test/mesh`, CI config, `docs`.

### S36 Mesh documentation

- **Do**: guides to installing, migrating from ztunnel and the Envoy waypoint, and troubleshooting,
  in English and Chinese.
- **Done when**: following the docs completes an installation and a migration.
- **Touches**: `docs`.

The external security audit in the roadmap's mesh 1.0 is not an iteration: it is arranged outside
the code once S35 is done.
