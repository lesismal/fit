# fit development plan

[English](dev-plan.md) | [简体中文](dev-plan.zh-CN.md)

This plan breaks the [roadmap](roadmap.md) into small iterations. Each iteration is one piece of
work that fits in one working session and one commit or pull request: small enough that the code it
touches, the docs it needs and its tests fit in one context, and done before the session runs out
of room.

- [How to work an iteration](#how-to-work-an-iteration)
- [Package layout](#package-layout)
- [Index](#index)
- [M0, foundations](#m0-foundations)
- [M1, the proxy core](#m1-the-proxy-core)
- [M1 review fixes](#m1-review-fixes)
- [M2, dynamic config and TLS](#m2-dynamic-config-and-tls)
- [M3, gateway features](#m3-gateway-features)
- [M4, platform integration](#m4-platform-integration)
- [M5, caching and extensions](#m5-caching-and-extensions)
- [1.0](#10)
- [P2P, NAT traversal](#p2p-nat-traversal)
- [VPN, a network of fit nodes](#vpn-a-network-of-fit-nodes)
- [Console, logins and an encrypted store](#console-logins-and-an-encrypted-store)
- [Agent and tunnel](#agent-and-tunnel)
- [WebRTC](#webrtc)
- [Media: auth, streaming and pages](#media-auth-streaming-and-pages)
- [AI assistant in the workbench](#ai-assistant-in-the-workbench)
- [Media library: video on demand and channels](#media-library-video-on-demand-and-channels)
- [Cluster management](#cluster-management)
- [Logging: files, rotation and retention](#logging-files-rotation-and-retention)

## How to work an iteration

**Pick one.** [Progress](progress.md) says where the work stands and which iteration is next.
Otherwise, take the first iteration in the [index](#index) whose status is `todo` and whose
dependencies are all `done`. Iterations with no dependency between them may run in parallel, in
separate branches.

**Read only what it needs.** A session starts from:

1. [progress](progress.md), this file's [index](#index) and the iteration's own section;
2. the [design principles](roadmap.md#design-principles) in the roadmap;
3. the packages the iteration lists under **Touches**, and the fib docs or packages under **Read**.

There is no need to read the whole repository, the comparison, or other iterations' sections.

**Stay small.** Aim for at most about 1,500 changed lines, tests included. If an iteration turns
out bigger, stop at a working, tested point, split the rest into a new iteration (`I17b` and so
on) in this file, and finish the current one.

**Finish properly.** An iteration is done when:

- its **Done when** items hold;
- `go vet ./...` and `go test -race ./...` pass, and new code has tests;
- nothing on the request path blocks a fib worker (see principle 1);
- the change follows every rule in [the development rules](rules.md);
- its status in the index is set to `done`, in both the English and the Chinese file, in the same
  commit;
- [progress](progress.md) and its Chinese twin record what was built, how it was checked, where it
  differs from the plan, and which iteration is next;
- any decision it made (a library choice, a format) is recorded as a short note in
  `docs/decisions/NNN-title.md`.

**Session prompt.** A session can be started with:

> Implement iteration IXX from docs/dev-plan.md. Read docs/progress.md, the index and that
> iteration's section in docs/dev-plan.md, and the design principles in docs/roadmap.md; then only
> the packages it lists. Keep the change within the iteration's scope, add tests, and when done
> mark it done in both dev-plan files and record it in both progress files.

**fib.** fit depends on `github.com/lesismal/fib`, at a released version. A gap found in fib goes
into [fib-api-requests](fib-api-requests.md) for the maintainer, who changes fib and releases it;
fit then moves to that version (rule 15 of the [development rules](rules.md)). Until then the
iteration does what does not depend on the gap, and records any stopgap in the progress files.

## Package layout

The layout set up in I01 and filled in by later iterations. Names may change, but each iteration
should touch as few of these as it can.

| Package | Contents |
| --- | --- |
| `cmd/fit` | the binary; its commands are in `cli`: `run`, `validate`, `adapt`, `schema`, `build`, `version`, `hash-password`, `store` |
| `config` | native config types, strict JSON loading |
| `schema` | the JSON Schema, generated from the config types and the registered modules |
| `module` | the module registry and the core interfaces |
| `modules/standard` | imports every module fit ships with |
| `runtime` | compiling config into an immutable graph, atomic swaps, graph reference counts |
| `listener` | listeners on fib bindings, protocol entry points, listener diffing |
| `router` | routers and matchers |
| `handler/...` | terminal handlers: `respond`, `static`, `proxy`, `fastcgi` |
| `middleware/...` | rewrite, headers, limits, auth, compression, cache, retry, breaker |
| `upstream` | upstream pools, load balancing, passive and active health |
| `l4` | TCP and UDP proxying, SNI routing, PROXY protocol |
| `p2p/stun` | the STUN server on listeners, over UDP and TCP; its messages move to fib's `webrtc/stun` with I86 (decision 029) |
| `webrtc/turn` | the TURN server role, `webrtc.turn`, on fib's `webrtc/turn/turnserver` (decision 029); P2P's relay |
| `webrtc/media` | the roles `webrtc.media` and `webrtc.proxy`, and the handlers `whip`, `whep` and `webrtc_proxy` (decision 029) |
| `p2p` | tunnels over a connected pair, signaling, the config's P2P apps |
| `vpn/packet` | IP headers and the overlay route table |
| `vpn/tun` | TUN devices on Linux, macOS, FreeBSD and Windows |
| `vpn/relay` | the relay protocol, server, and the client that picks a transport |
| `vpn` | the node, the coordinator, the config's VPN apps |
| `tlsmgr` | the certificate manager, storage, ACME, OCSP |
| `admin` | the Admin API and config versions; the console (decision 027) |
| `admin/store` | the encrypted store, fitstore (decision 027) |
| `login` | logins, sessions, CSRF tokens and lockouts, for the console and the roles' pages |
| `app`, `app/console` | roles in the config: their registry and manager, write-back through `Host.Edit`, and their pages (decision 028) |
| `llm/...` | the LLM feature's roles: `hub`, `node`, `proxy` |
| `tunnel/...` | the tunnel's roles: `hub`, `node`, `proxy` |
| `provider/...` | file, docker, kubernetes, consul, etcd |
| `observe` | access logs, metrics, tracing |
| `logs` | fit's own log: loggers, levels, and the files the log and the access log write through, rotated and kept (I155–I156) |
| `plugin/...` | compile-time modules and WASM |
| `adapter/...` | config adapters: friendly format, nginx |
| `bench` | the benchmark harness, configs for nginx, Caddy, Traefik and fit, results |
| `test/e2e` | end-to-end and conformance suites |
| `internal/fibtest` | running fib handlers in tests without a network |
| `examples` | example configs; moved to [fit-examples](https://github.com/lesismal/fit-examples) |

## Index

Status: `todo`, `doing` or `done`. The next free iteration number is I163.

| ID | Iteration | Depends on | Status |
| --- | --- | --- | --- |
| **M0** | | | |
| I01 | Repository skeleton and CI | — | done |
| I02 | Config types v0 and strict loading | I01 | done |
| I03 | JSON Schema and `fit validate` | I02 | done |
| I04 | Module registry and core interfaces | I02 | done |
| I05 | Runtime graph and atomic swap | I04 | done |
| I06 | HTTP entry points and `fit run` | I05 | done |
| I07 | Benchmark harness: throughput | I06 | done |
| I08 | Benchmark harness: idle connection memory and baselines | I07 | done |
| **M1** | | | |
| I09 | TLS entry from certificate files | I06 | done |
| I10 | HTTP/3 entry | I09 | done |
| I11 | Router v1: host and path | I06 | done |
| I12 | Static files I | I11 | done |
| I13 | Static files II | I12 | done |
| I14 | Upstream pool v1 (HTTP/1.1) | I05 | done |
| I15 | Reverse proxy I: forwarding and headers | I11, I14 | done |
| I16 | Reverse proxy II: streaming and backpressure | I15 | done |
| I17 | Reverse proxy III: timeouts, cancellation, errors | I16 | done |
| I18 | HTTP/2 upstreams | I17 | done |
| I19 | Load balancing v1 | I14 | done |
| I20 | Passive health checks | I19 | done |
| I21 | Access logs | I06 | done |
| I22 | Reload on SIGHUP | I05, I06 | done |
| I23 | M1 conformance and benchmarks | I13, I18, I20, I21, I22 | done |
| **M1 review fixes** | | | |
| R01 | Bounded waits on slow clients | I13, I17 | done |
| R02 | Safe default limits | I13, I20 | done |
| R03 | fib APIs for forwarding without waiting (in fib) | — | done |
| R04 | Proxy downloads without waiting | R01, R03 | done |
| R05 | Static files without blocking | R01, R03 | done |
| R05b | Files sent by sendfile paced and bounded | R05, A16 | done |
| R06 | Connection and queue limits through fib | R02, R03 | done |
| R07a | Hot path costs in fit | I23 | done |
| R07b | Proxy allocations | I23 | done |
| R07c | Hot path costs through fib | R03, R07a, R07b | done |
| R07d | Access log cost | R07c | done |
| R07e | Hooks and files through fib (A16–A18) | R05b, R07d, I34 | done |
| R07f | The proxy's CPU per request, profiled (A20–A22) | R07e, I35 | done |
| **M2** | | | |
| I24 | Config versions and Admin API skeleton | I22 | done |
| I25 | Admin API: patch, ETag, rollback | I24 | done |
| I26 | Listener diffing and graph draining | I24 | done |
| I27 | File provider | I24 | done |
| I28 | Certificate manager and storage; ACME decision | I09 | done |
| I29 | ACME HTTP-01 and renewal | I28 | done |
| I30 | TLS-ALPN-01, OCSP stapling | I29 | done |
| I30b | HTTPS by default | I30 | done |
| I30c | ACME Renewal Information (ARI) | I29 | done |
| I31 | On-demand TLS | I30 | done |
| I32 | Active health checks | I20 | done |
| I33 | Load balancing v2 | I19 | done |
| I34 | Prometheus metrics | I23 | done |
| I35 | M2 exit tests | I25, I26, I27, I30b, I31, I32, I33, I34 | done |
| I35b | Certificate files watched | I27, I28, I34 | done |
| I35c | On-demand names dropped when idle | I31, I35b | done |
| **M3** | | | |
| I36 | Matchers v2 and priorities | I11 | done |
| I37 | Expression matcher | I36 | done |
| I38 | Rewrites, redirects and headers | I36 | done |
| I39 | Rate, concurrency and size limits | I36 | done |
| I40 | Basic auth and client mTLS | I36 | done |
| I41 | ForwardAuth | I40 | done |
| I42 | JWT and JWKS | I40 | done |
| I43 | Retries and per-route timeouts | I17 | done |
| I44 | Circuit breakers | I43 | done |
| I45 | Traffic splitting: weights, mirroring, failover | I19 | done |
| I46 | Compression | I36 | done |
| I47 | WebSocket proxying | I17 | done |
| I48 | WebSocket over HTTP/2 and HTTP/3 entries | I47, I10 | done |
| I49 | gRPC proxying | I18 | done |
| I50 | TCP proxying | I05 | done |
| I51 | UDP proxying | I50 | done |
| I52 | SNI routing and PROXY protocol | I50 | done |
| I53 | One port, many protocols; SOCKS and CONNECT | I52 | done |
| I53b | The CONNECT forward proxy | I53 | done |
| I54 | Upstream TLS | I18 | done |
| I55 | M3 exit tests | I37–I54 | done |
| **M4** | | | |
| I56 | Provider framework and effective config | I27 | done |
| I57 | Docker provider | I56 | done |
| I58 | Kubernetes Gateway API I: HTTPRoute | I56 | done |
| I59 | Kubernetes Gateway API II: other routes and status | I58 | done |
| I60 | Kubernetes Ingress | I58 | done |
| I61 | Gateway API conformance | I59 | done |
| I62 | Consul and etcd providers | I56 | done |
| I63 | Shared certificate storage | I30 | done |
| I64 | Dashboard | I56 | done |
| I65 | OpenTelemetry | I34 | done |
| I66 | Packaging and examples | I57 | done |
| **M5** | | | |
| I67 | Cache I: HTTP semantics and memory tier | I23 | done |
| I68 | Cache II: disk tier | I67 | done |
| I69 | Cache III: locks, stale, purge | I68, I25 | done |
| I70 | `fit build` and compile-time modules | I04 | done |
| I71 | WASM I: runtime and plugin pool | I36 | done |
| I72 | WASM II: Proxy-Wasm subset | I71 | done |
| I73 | Friendly config format | I03 | done |
| I74 | nginx config converter | I73 | done |
| I75 | FastCGI | I17 | done |
| **1.0** | | | |
| I76 | Continuous fuzzing | I55 | done |
| I77 | Config and Admin API v1 freeze | I69, I74 | done |
| I78 | Reference docs and migration guides | I77 | done |
| I79 | Caddyfile converter, JSON with comments | I74 | done |
| I80 | fit's own .conf format; configs in several files | I79 | done |
| I81 | WASM III: Proxy-Wasm's response callbacks | I72 | done |
| I82 | fit's own log: loggers and levels; nginx's and Caddy's logs converted | I21, I79 | done |
| I83 | fit's log in fib's formats; loggers of their own; the access log's formats | I82 | done |
| **P2P** | | | |
| I84 | STUN messages and the STUN server | I51 | done |
| I85 | A UDP peer opened toward an address (A49, in fib) | — | done |
| I86 | The STUN client and NAT behaviour, on fib's `webrtc/stun` | I84, I85, I108 | todo |
| I87 | The TURN server, `webrtc.turn`, on fib's `turnserver` | I85, I108 | done |
| I88 | The TURN client, fib's `turn.Client` | I86, I87 | todo |
| I89 | The ICE agent, fib's `webrtc/ice` | I86, I88 | todo |
| I90 | QUIC DATAGRAM frames (A50, in fib) | — | todo |
| I91 | Tunnels over a connected pair | I89, I90 | todo |
| I92 | Signaling and the config's P2P apps | I87, I91 | todo |
| I93 | P2P interop and exit tests | I92 | todo |
| **VPN** | | | |
| I94 | IP packets and the overlay route table | — | done |
| I95 | A packet device's descriptor on the poller (A51, in fib) | — | todo |
| I96 | TUN devices on Linux, macOS and FreeBSD | I94, I95 | todo |
| I97 | TUN devices on Windows with Wintun | I94 | todo |
| I98 | Relay frames and the relay server, over WebSocket | I94 | todo |
| I99 | Relay over QUIC and HTTP/2; transports in order, with fallback | I90, I98, S04 | todo |
| I100 | The coordinator and the netmap | I92, I98 | todo |
| I101 | The node: TUN to peers, direct or relayed | I91, I96, I99, I100 | todo |
| I102 | VPN config, Admin API and the dashboard's pages | I101 | todo |
| I103 | VPN end-to-end and exit tests | I97, I102 | todo |
| **Console** | | | |
| I104 | The config kept encrypted (`admin.store`); the console, with logins and an editor made from the schema | I77 | done |
| **Agent and tunnel** | | | |
| I105 | Roles in fit's config: `app`, `app/console`, `login`, write-back through `EditMember` | I104 | done |
| I106 | The tunnel's roles, from fibhub: `tunnel/server`, `tunnel/node`, `tunnel/client` | I105 | done |
| I107 | The agent's roles, from agenthub, on fib throughout: `agent/hub`, `agent/node`, `agent/client` | I105 | done |
| **WebRTC** | | | |
| I108 | Decision 029: fit's WebRTC on fib's; fib `80c7f76`; A49 as built; A55–A58 | I85 | done |
| I109 | WHIP and WHEP: an SFU on a shared `webrtc.Mux` (`webrtc.media`) | I108 | done |
| I110 | WHIP and WHEP proxied to media servers; UDP routed by ICE ufrag | I109 | done |
| I111 | WebRTC on fib's A55–A59: TURN over TCP and TLS, caps, events, changes in place and drains; the proxy's source address and ports | I87, I110 | done |
| I112 | The agent's model APIs: every stateless inference endpoint of OpenAI and its compatibles, Anthropic, Gemini, Azure OpenAI, Ollama and Cohere | I107 | done |
| I113 | The LLM roles' node list, links and traffic: a hub's nodes synced to its proxies, every link's bytes and latency, every route's calls, a link map on each role's pages, and the roles' remaining settings set there | I112 | done |
| **Media** | | | |
| I115 | Media auth: named `media_auth` policies (tokens, users, signed tokens, public actions, stream patterns), package `mediaauth`, `auth` on `whip`, `whep` and `webrtc_proxy`, `fit media-token` | I109, I110 | done |
| I116 | The streaming role: fib's `media/hub` with RTMP, RTSP and SRT listeners, and `httpflv`, `hls`, `stream_whip` and `stream_whep` handlers, each with `auth` | I115 | done |
| I117 | The media pages: `media_pages`, login, publish, play and a room call with the microphone and camera switched on and off, and a bounded room API | I115 | done |
| I118 | The media pages' room chat: text messages and small files over the room API, a page switch, and the microphone and camera a member joins with | I117 | done |
| I119 | Media tokens from a route: the handler `media_tokens`, batch issuance signed with one key of the policy's `keys` ring, prefix bounds on the stream names, and revocation by rotating one key or all of them | I115 | done |
| I120 | The workbench's config pages: a flat module menu, per-page `.conf`/`.yaml`/`.json` tabs checked through the apply path, and console passwords changed in the pages and kept in the store | I114 | done |
| I121 | Consistency between what the running service does and the config it runs from: the drift and info differences with the fixes offered, the Consistency page, `fit check`, and `GET /config/check`/`POST /config/reconcile` | I120 | done |
| I122 | The workbench's feature tabs and listener ports: streaming, media auth, WebRTC, tunnel and LLM config on pages of their own, and each listener port listing the features it serves with links to their pages | I121 | done |
| I123 | The workbench's Engine menu: one first-level entry, each listener port a submenu of its features in the order asked (handlers, reverse/forward proxy, cache and middleware, streaming, WebRTC, layer 4/SOCKS/TLS/ACME/HTTPS by default), with the port's feature tags on its line | I122 | done |
| **AI assistant** | | | |
| I124 | Decision 031; `admin.assistant` and its settings page; the model client for each API, streamed, with tool calls, on fib's asynchronous HTTP client; keys in the store | I120 | done |
| I125 | The workbench's chat panel and the read tools: tool registry, SSE chat, page context, redaction, fit's log ring | I124 | done |
| I126 | Proposals: the change tools, the diff and apply flow, per-user levels and the audit log | I125 | done |
| I127 | Skills: the format, the built-in set, the Skills page, import and export, `/name` | I126 | done |
| I128 | Ask AI where the work is: entry points on save errors, Consistency, the Engine menu, form fields, metrics and role pages | I127 | done |
| I129 | Migration and review: `migrate` and `security-review` end to end, evaluated on fixed sets of configs | I127 | done |
| I130 | An MCP server on the tool registry: tools, skills as prompts, config and schema as resources; per-user tokens capped at `propose`; proposals applied only in the workbench | I126, I127 | done |
| **Media library** | | | |
| I131 | Decision 032; fib A64, a reader of MP4 files that are not fragmented: sample tables through an `io.ReaderAt`, samples by time and keyframe, bounded memory | I116 | done |
| I132 | Media libraries: `media_library` roots scanned into a catalog of items (tracks, codecs, renditions, subtitles, posters), probed on a bounded file pool, the Admin API | I131 | done |
| I133 | Video on demand: the handler `vod`, HLS (fMP4 or MPEG-TS) cut at keyframes from the sample tables, renditions, audio and subtitle groups, ranges, `auth` | I132 | done |
| I134 | Channels: library items played into the hub as live streams, in order, looped or on a schedule; `push` to outside RTMP and SRT servers | I132 | done |
| I135 | Playback URLs that expire: `url_signing` bound to a resource, address or session, grants carried to segments, `POST <prefix>/urls` in `media_tokens`, `max_sessions` | I119, I133 | done |
| I136 | Encrypted segments: AES-128 HLS, keys served only to a grant for the resource | I135 | done |
| I137 | A video site end to end: the example, `media_pages`' library and player pages, docs | I134, I135 | done |
| I138 | The workbench's media library pages: menu entries, config pages, a library browser with previews, channels on a timeline, push targets, test URLs, keys and live sessions, metrics | I135, I123 | done |
| I139 | Media files managed from the workbench: resumable uploads, rename, move, delete and sidecars, bounded and confirmed | I138 | done |
| **Cluster management** | | | |
| I140 | Decision 033; what a member needs: `GET /node`, a dry run of `PUT`/`PATCH /config`, logins a program can use, failed logins slowed; a Check button on the config pages | I121 | done |
| I141 | The manager: `admin.cluster`, members and their credentials in the store (console password or Admin API token, TLS pinned), a bounded poller of their state | I140 | done |
| I142 | The workbench's Cluster menu: Overview, Nodes and a node's page; `docs/cluster.md` | I141 | done |
| I143 | A member's own pages opened from the manager: the node switcher, its API passed on with the stored session | I142 | done |
| I144 | Resource groups by label or name, and operations on many members (reload, rollback, check, purge) with per-member results; the Groups pages | I142 | done |
| I145 | Templates: config paths a group's members share, with variables, sync state, conflicts marked on members; the Templates pages | I144 | done |
| I146 | Rollouts: per-member diffs and dry runs, all at once, rolling or canary, health gates, rollback on failure; the Rollouts pages | I145 | done |
| I147 | Drift and alerts: drift fixed by a rollout, alert rules sent to webhooks; the Drift and alerts pages | I146 | done |
| I148 | The manager's users and roles per group, and the audit log; the Users and audit pages | I146 | todo |
| I149 | Members behind NAT linking out to the manager, enrolled by a one-time token; Add by enrollment | I141 | done |
| I150 | Metrics, logs and certificates across members, `fit cluster` in the CLI, the assistant's cluster tools; their pages | I147, I125 | todo |
| **AI assistant, continued** | | | |
| I152 | The assistant's media library tools and skills: the catalog, channels, sessions, rescans, key rotation and file changes proposed; `vod-setup`, `channel-scheduler`, `library-health`, `playback-troubleshoot` | I127, I138 | done |
| I153 | The assistant across a cluster: members' state, templates and rollouts read, changes proposed as rollout plans, the manager's roles checked; `fleet-health`, `rollout-planner`, `cluster-drift` | I127, I146, I148 | todo |
| I154 | The assistant's page edits: the `edit` level, and the page tools that draft a change into the open page's editor and forms as an unsaved draft, reverted on request, never submitted — **Save and apply** stays the person's | I125, I126, I120 | done |
| **Logging** | | | |
| I155 | Decision 034; the file sink: a bounded queue and a writer of its own, the log's `time_zone` (local by default, UTC or an IANA zone), rotation by size and by time, retention by age, number and total size, compression, and `log`'s file outputs and their `rotate` | I21, I82, I83 | done |
| I156 | The access log's file through the same sink: `access_log.rotate` and `time_zone`, one sink per path, shared with `log`'s, a rotation only after the buffers are written, and the config's refusal of two settings for one path | I155 | done |
| I157 | The workbench's Logs page: each output, its loggers and format, the files on disk with sizes and ages, the rotation and retention form, rotate now and clean up now with a preview and a confirmation, and warnings | I155, I156, I120 | done |
| **Performance** | | | |
| I158 | fib v1.7.1's HTTP/2 request checks made as cheap as they can be: fit's `fixed` and `static` over h2c back within noise of fib `5c0e5b9` (−2.4% and −1.7% on 2026-11-05), the checks kept | — | done |
| **Agent and tunnel, continued** | | | |
| I159 | Issued tokens for the tunnel and the LLM feature's mesh: a listener issues, lists, expires and revokes tokens that any role may present in place of the shared password, each token its own credential, with an optional lifetime; a peer that knows no long-lived password, no table of guesses that works everywhere, and no role's password that lets one member pose as another | I106, I113 | done |
| I160 | The tunnel's and the LLM mesh's TLS, from the review of 2026-11-06: a link's certificate options passed apart from its address, so nothing in the address can override the pin; `quic_insecure` refused off loopback; a self-signed certificate kept across restarts without the store; an LLM listener's certificate reloaded when its file changes | I159 | done |
| I161 | The Admin API's and consoles' unauthenticated connections, and Windows and case-insensitive file names, from the review of 2026-11-06: writes bounded as reads are, slots kept for authenticated clients; fastcgi refusing a backslash as static does; an extension in another case not served as source | — | done |
| I162 | One address, one server: config validation refuses two parts of the config on one address and transport, across listeners, the Admin API, consoles and every role, with an error naming both; every binding checks at bind time that no other socket holds its address; fib's TURN relays never share a port of the range (A70) | — | done |

## M0, foundations

### I01 Repository skeleton and CI

- **Do**: `go.mod` for `github.com/lesismal/fit` on the Go version fib uses; the empty packages from
  the [layout](#package-layout) that M0 needs; `cmd/fit version`; a Makefile; CI running
  `go vet`, staticcheck and `go test -race`; a short README pointing at the docs.
- **Done when**: CI is green on `main`; `fit version` prints the version and the fib version.
- **Touches**: repository root, `cmd/fit`.

### I02 Config types v0 and strict loading

- **Do**: Go types for the native config: `listeners` (address, protocols), `routes` (match, a
  handler chain), `handlers` by module name with raw JSON params. Loading rejects unknown fields
  and reports errors with their JSON path. Only a `respond` handler (status, headers, body) for now.
- **Done when**: table tests cover valid configs, unknown fields, wrong types and missing fields,
  each with a precise error path.
- **Touches**: `config`.

### I03 JSON Schema and `fit validate`

- **Do**: a JSON Schema for the native config, published under `schema`; a test that keeps
  the schema and the Go types in sync; `fit validate -c file` that exits non-zero with readable
  errors.
- **Done when**: the sync test fails if a field is added to the types but not the schema, and the
  other way round.
- **Touches**: `config`, `cmd/fit`.

### I04 Module registry and core interfaces

- **Do**: interfaces for handler, middleware, matcher, service and upstream; a registry keyed by
  namespaced names (`http.handlers.respond`) with a provision step that decodes params and
  validates them; register `respond`.
- **Done when**: unknown module names and bad params fail at provision time with the config path.
- **Touches**: `module`, `handler/respond`.
- **Read**: Caddy's module model as summarised in [the comparison](comparison.md#caddy).

### I05 Runtime graph and atomic swap

- **Do**: compile a config into an immutable graph (listeners → routes → provisioned modules);
  hold the current graph in an atomic pointer; reference-count graphs so connections pin the graph
  they started on and modules get a cleanup call when the last reference goes.
- **Done when**: tests swap graphs while goroutines hold references and check that cleanup runs
  exactly once per graph, under the race detector.
- **Touches**: `runtime`.

### I06 HTTP entry points and `fit run`

- **Do**: start a fib engine, bind listeners from the graph, serve HTTP/1.1 and h2c, dispatch to the
  route's handler chain; `fit run -c file`; graceful shutdown on SIGINT and SIGTERM.
- **Done when**: an e2e test starts `fit run` with a `respond` route and gets the response over
  HTTP/1.1 and h2c; shutdown waits for in-flight requests.
- **Touches**: `listener`, `cmd/fit`, `test/e2e`.
- **Read**: fib `docs/http1.md`, `docs/http2.md`, `engine.go`, `binding.go`.

### I07 Benchmark harness: throughput

- **Do**: `bench/` with equivalent configs for nginx, Caddy, Traefik and fit (fixed response, small
  static file, proxy to a fixed backend); scripts that run wrk and h2load and write results as
  JSON with the hardware and versions.
- **Done when**: one command runs all four servers through all scenarios on a Linux host.
- **Touches**: `bench`.

### I08 Benchmark harness: idle connection memory and baselines

- **Do**: a Go tool that opens N idle connections (HTTP/1.1 keep-alive, TLS, WebSocket) and reads the
  server's RSS; add it to the harness; commit baseline results for nginx, Caddy and Traefik.
- **Done when**: `bench/results/baseline-*.json` exists and the README in `bench/` says how to
  reproduce it.
- **Touches**: `bench`.

## M1, the proxy core

### I09 TLS entry from certificate files

- **Do**: TLS listeners on `fib/tls` with certificates from files, chosen by SNI; ALPN for h2 and
  http/1.1; sensible TLS defaults (TLS 1.2+, modern cipher suites).
- **Done when**: e2e tests cover two host names with different certificates over HTTP/1.1 and h2.
- **Touches**: `listener`, `tlsmgr` (static certificates only).
- **Read**: fib `tls`, `GetCertificate` and the handshake pool.

### I10 HTTP/3 entry

- **Do**: HTTP/3 listeners on fib `http3`, sharing the certificate source with TLS; `Alt-Svc`
  advertised on the TCP listeners for the same host.
- **Done when**: an e2e test reaches the same route over h3 and sees `Alt-Svc` on h2.
- **Touches**: `listener`.
- **Read**: fib `docs/http3.md`.

### I11 Router v1: host and path

- **Do**: routers with host (exact and wildcard) and path (exact and prefix) matchers; explicit
  priority, then most specific first; 404 when nothing matches.
- **Done when**: table tests cover ordering and ties; lookups do not allocate on the hot path.
- **Touches**: `router`.

### I12 Static files I

- **Do**: a `static` handler on `http.FileCache`: root, index files, ETag, `Last-Modified`,
  conditional requests, range requests, MIME types; path traversal rejected.
- **Done when**: tests cover conditional and range requests and traversal attempts.
- **Touches**: `handler/static`.
- **Read**: fib `http/file_cache*.go`.

### I13 Static files II

- **Do**: precompressed twins (`.br`, `.zst`, `.gz`) chosen by `Accept-Encoding`; `try_files`-style
  fallbacks (for single-page apps); sendfile for large files; optional directory listing off by
  default.
- **Done when**: tests cover encoding negotiation and fallbacks; a benchmark scenario uses it.
- **Touches**: `handler/static`, `bench`.

### I14 Upstream pool v1 (HTTP/1.1)

- **Do**: upstream config (addresses, max connections, idle timeout, connect timeout); a pool per
  upstream on fib's asynchronous HTTP/1.x client; connection reuse and idle eviction.
- **Done when**: tests show reuse, limits and eviction against a test backend.
- **Touches**: `upstream`.
- **Read**: fib `http/client.go`.

### I15 Reverse proxy I: forwarding and headers

- **Do**: a `proxy` handler that forwards method, path, query and headers to one upstream and
  writes the response back; removes hop-by-hop headers; sets `X-Forwarded-For/Proto/Host` and
  `Forwarded`; a trusted-proxy list decides whether incoming forwarded headers are kept.
- **Done when**: header tests pass; small bodies round-trip.
- **Touches**: `handler/proxy`.

### I16 Reverse proxy II: streaming and backpressure

- **Do**: stream request and response bodies in both directions without buffering whole bodies;
  link backpressure (`HoldReads` on the side that is ahead when the other side's pending bytes pass
  a watermark).
- **Done when**: tests with a slow client and a slow upstream keep memory bounded while moving
  hundreds of megabytes; chunked and content-length bodies both work.
- **Touches**: `handler/proxy`.
- **Read**: fib `Connection.HoldReads`, `backpressure_test.go`, `http/body*.go`.

### I17 Reverse proxy III: timeouts, cancellation, errors

- **Do**: connect, response-header and idle timeouts; cancel the upstream request through
  `c.Context()` when the client goes away; map failures to 502 and 504; forward trailers.
- **Done when**: tests cover each timeout, a client abort mid-body and trailers.
- **Touches**: `handler/proxy`.
- **Read**: fib `http/context.go`.

### I18 HTTP/2 upstreams

- **Do**: upstreams over h2 (TLS) and h2c on fib's HTTP/2 client, with stream multiplexing over
  pooled connections and limits per connection.
- **Done when**: proxy tests from I15–I17 also pass against h2 and h2c backends.
- **Touches**: `upstream`, `handler/proxy`.
- **Read**: fib `http/h2_client.go`.

### I19 Load balancing v1

- **Do**: a selection interface and round robin, weighted round robin (smooth) and least
  connections; several addresses per upstream.
- **Done when**: distribution tests hold within tolerance; selection does not allocate.
- **Touches**: `upstream`.

### I20 Passive health checks

- **Do**: mark an address down after N failures within a window, skip it while down, retry it after
  a cooldown; like nginx's `max_fails` / `fail_timeout`.
- **Done when**: tests with a failing backend show it leave and rejoin rotation.
- **Touches**: `upstream`.

### I21 Access logs

- **Do**: structured JSON access logs with the usual fields (time, client, host, method, path,
  status, sizes, durations, upstream); buffered and written off the event loop; stdout or file.
- **Done when**: a benchmark shows logging does not block workers; fields are tested.
- **Touches**: `observe`.

### I22 Reload on SIGHUP

- **Do**: on SIGHUP, load and compile the config file; on success swap the graph; keep listeners
  whose address is unchanged, add and remove the rest; on failure keep the running graph and log
  the error.
- **Done when**: an e2e test reloads under load with no failed requests; a broken config leaves the
  old one serving.
- **Touches**: `cmd/fit`, `runtime`, `listener`.
- **Read**: fib `AddBinding`, `RemoveBinding`, `ShutdownBinding`.

### I23 M1 conformance and benchmarks

- **Do**: run h2spec on the h2 entry; a proxy conformance suite in `test/e2e`; run the harness for
  static and proxy scenarios and idle memory at 10k and 100k; fix what they find or split fixes into
  new iterations.
- **Done when**: results are committed under `bench/results/` and compared with the baselines in a
  short report.
- **Touches**: `test/e2e`, `bench`.

## M1 review fixes

A review of M0 and M1 against the [development rules](rules.md) on 2026-10-19 found code that breaks
them. These iterations fix it in three steps: R01 and R02 change fit alone and come before M2; R03
is work in fib, specified in [APIs fit needs from fib](fib-api-requests.md); R04–R06 and R07c use those
APIs. R07a and R07b are the part of R07, the hot path, that needs nothing from fib.

What the review found, and where each finding is fixed:

| Finding | Rules | Where | Fixed in |
| --- | --- | --- | --- |
| A proxied download waits in `Context.Write` on the worker that reads the upstream connection, with no time limit. One client that stops reading stalls every request sharing its h2c upstream connection, and a few exhaust `max_conns`; both were reproduced | 1, 8, 10 | `handler/proxy/exchange.go`, `onBody` | R01 bounds it, R04 fixes it |
| Static files are stat'ed, read and opened on the worker, and sent through writes that wait without a time limit; a directory listing reads the whole directory | 1, 4, 10, 11 | `handler/static` | R01 and R02 bound it, R05 fixes it |
| Listeners set no `WriteTimeout` or stall timeout | 10 | `listener/listener.go`, `httpConfig` and `http3Config` | R01 |
| An upstream without `max_conns` has no connection limit; requests waiting for a connection are not limited in number; a response has no total timeout | 10, 11 | `upstream/upstream.go`, `config` | R02, R06 |
| The engine limits neither total queued output nor connections | 11 | `listener/listener.go`, `Start` | R02, R06 |
| An upload with trailers holds a goroutine blocked in a pipe read | 1, 4 | `handler/proxy/exchange.go`, `trailerPipe` | R04 |
| A lock per access log line; a copy of the request and a context per request for the log entry; a scan of the graph's listeners per request; a lock per weighted round robin pick | 12 | `observe`, `listener`, `upstream/balance.go` | R07a, R07c |
| No test that a client that stops reading leaves other requests alone; no slow-client tests for static files | 13 | tests | R01, R05 |

The rest of M0 and M1 follows the rules: config, the module registry, the runtime graph, the router,
`respond`, the proxy's upload direction and the access log's writer.

### R01 Bounded waits on slow clients

- **Do**: a listener setting, `write_stall_timeout` (30s when left out), that bounds each wait for a
  client to take output. The proxy's download path and the static handler call
  `Context.SetWriteDeadline(now + stall)` before writing, refreshed at most once a second to spare
  system calls, so a client that stops reading loses its response (its HTTP/1 connection closed, its
  HTTP/2 or HTTP/3 stream reset) and the goroutine waiting on it is let go; the proxy then abandons
  the upstream response. Regression tests that reproduce the review: a non-reading client on a
  shared h2c upstream connection, and non-reading clients against `max_conns`, over HTTP/1.1, h2c
  and h3.
- **Done when**: in those tests other requests are served while the stuck clients are there, and
  the stuck ones end within the stall timeout; the streaming tests still pass.
- **Touches**: `config`, `schema`, `listener`, `handler/proxy`, `handler/static`, `test/e2e`.
- **Read**: fib `Context.SetWriteDeadline`, `Config.WriteTimeout`; the evidence in
  [fib-api-requests](fib-api-requests.md#evidence).

### R02 Safe default limits

- **Do**: a default for every limit the review found open, each settable in the config:
  `max_conns` per upstream address (256 when left out); `max_pending` per upstream, the requests
  waiting for a connection (1024 when left out; past it, 503 at once, counted by fit until R06);
  `response_timeout` per upstream, the total time a response may take (1h when left out); a
  top-level `limits` object with the engine's `max_pending_bytes` (256MB) and the listeners'
  `max_header_bytes` and `max_body_bytes` (fib's 1MB and 16MB, now visible); and
  `browse_max_entries` for directory listings (10000). Every default is in the schema.
- **Done when**: each limit has a test that reaches it; the schema and the examples show the
  defaults.
- **Touches**: `config`, `schema`, `upstream`, `handler/proxy`, `handler/static`, `listener`.

### R03 fib APIs for forwarding without waiting

- **Do**: the maintainer adds A1, A2 and A3 from [fib-api-requests](fib-api-requests.md) to fib,
  then A4–A7 and A11–A13 as fib schedules them, and releases a new fib version; fit moves to it
  with no change in behaviour.
- **Done when**: fit builds and passes its tests on the new fib release, and fib-api-requests says
  of each API whether it was delivered, changed or declined.
- **Touches**: fib; in fit, `go.mod` and the docs.

### R04 Proxy downloads without waiting

- **Do**: rewrite the download path with `WriteNoWait`, `OnWritable` and `ClientResponse.HoldBody`,
  as the upload path already uses `HoldBody` and `OnDrain`; replace `trailerPipe` with trailers on
  `BodyWriter` (A6) once fib has them.
- **Done when**: no proxy code calls a write that waits; R01's tests pass with the stall timeout
  raised to an hour, so nothing relies on it; a slow client holds back only its own upstream stream;
  memory stays bounded in the 256MB streaming tests.
- **Touches**: `handler/proxy`.

### R05 Static files without blocking

- **Do**: serve from fib's `http.FileCache`, with no stat per request; open, stat, read and list the
  files it misses on a bounded file I/O pool of fit's own, with a timeout; send large files with A3.
  Range and conditional requests keep working.
- **Done when**: no static code calls a file operation or a waiting write on a worker; tests with
  slow and non-reading clients over HTTP/1.1, h2 and h3, and with a slow disk (a test hook on the
  pool); benchmarks before and after.
- **Touches**: `handler/static`.

### R05b Files sent by sendfile paced and bounded

- **Do**: once fib counts a file queued by `SendFile` in a connection's unsent output (A16), move
  to that fib release, and run `TestStuckClient/http/1.1`; if fib's `SendFile` bounds its own
  waits, hand it whole ranges instead of pieces.
- **Done when**: a client that stops reading a file sent by sendfile over plain HTTP/1 is cut off
  after the stall timeout, and the skipped test runs.
- **Touches**: `handler/static`, `go.mod`.

### R06 Connection and queue limits through fib

- **Do**: the engine's connection limit (A4), as `limits.max_conns`; the client's pending limit (A5)
  in place of R02's count in fit.
- **Done when**: tests reach both limits, and R02's interim count is gone.
- **Touches**: `listener`, `upstream`, `config`.

### R07a Hot path costs in fit

R07 split in three: what fit can do alone (R07a, R07b) and what needs fib (R07c).

- **Do**, guided by I23's numbers: per-shard access log buffers, each with its own lock; the log
  entry and its context in one allocation; each listener's routes found without scanning the graph;
  smooth weighted round robin without a lock, as a precomputed schedule and an atomic index;
  benchmarks of the dispatcher and the log from several cores.
- **Done when**: nanoseconds and allocations per request, before and after, are in the progress
  record, and throughput does not regress on the benchmark host.
- **Touches**: `observe`, `listener`, `upstream`.

### R07b Proxy allocations

- **Do**: fewer allocations per proxied request in fit's own code: the outgoing request, its URL
  and the forwarding fields' values in the exchange's allocation; the client's IPv4 address
  without formatting it; a response that arrived whole read in one allocation; a benchmark of the
  proxy with a raw client. What fib's client allocates goes to fib-api-requests.
- **Done when**: as R07a, for the proxy scenario.
- **Touches**: `handler/proxy`.

### R07c Hot path costs through fib

- **Do**: the log entry in the Context's value slot (A7) instead of `r.WithContext`; a response
  that arrived whole written without a copy (A11); the client's per-request timers and contexts
  (A12), with the proxy's response header timer handed to fib's client; no header copy for a
  logged response (A13); then, on the benchmark host, the proxy's 83% of nginx over HTTP/1.1 and
  its p99 tail, as I23 left them.
- **Done when**: as R07a; the proxy's allocations per request, before and after, are in the
  progress record.
- **Touches**: `observe`, `listener`, `handler/proxy`, `upstream`.

### R07d Access log cost

- **Do**: R07c left fixed h2c about 30% slower with the access log on than off. Profile it on the
  benchmark host and take out what fit pays for each line: header lookups that canonicalize a
  key already canonical, durations formatted as floats, the date formatted on every line, a JSON
  string scanned a byte at a time, and contention on the log's shards; what is fib's goes to
  fib-api-requests.
- **Done when**: as R07a, for the fixed response with the access log on, over HTTP/1.1 and h2c.
- **Touches**: `observe`.

### R07e Hooks and files through fib (A16–A18)

Done in fib by fit's own session, with the maintainer's leave, after the M2 report.

- **Do**: in fib, a Context that keeps its response writer for the next request and a value slot
  without a lock (A17); `Context.Status` (A18); `SendFileStall` (A16). In fit, count the requests
  of a handler that answers before it returns without an OnFinish hook, and send a file's range
  whole through `SendFileStall`; then move to the fib release.
- **Done when**: fixed and static over h2c, with the access log and with metrics, before and after,
  on the benchmark host; `TestStuckClient` passes over every protocol with whole ranges.
- **Touches**: fib's `http`; fit's `module`, `runtime`, `listener`, `handler/respond`,
  `handler/static`.

### R07f The proxy's CPU per request, profiled (A20–A22)

The M2 report put fit's proxy at 86% of nginx over HTTP/1.1, with 1.5 times nginx's CPU per
request.

- **Do**: profile the proxy against nginx on the benchmark host; take out what is fit's, and write
  up what is fib's in fib-api-requests with the evidence.
- **Done when**: the CPU per request is split between the kernel, fib, fit and the runtime; fit's
  part is measured before and after with a micro-benchmark and the harness; fib's requests are
  written.
- **Touches**: `handler/proxy`; `docs/fib-api-requests`.

## M2, dynamic config and TLS

### I24 Config versions and Admin API skeleton

- **Do**: keep a history of applied configs with version numbers; an Admin API on localhost or a
  Unix socket by default, optional bearer token: `GET /config`, `POST /load`; apply through the
  same compile-and-swap path as SIGHUP.
- **Done when**: e2e tests load a config through the API and read it back.
- **Touches**: `admin`, `runtime`.

### I25 Admin API: patch, ETag, rollback

- **Do**: `GET/PUT/PATCH/DELETE /config/<path>` by JSON pointer; ETags and `If-Match` against lost
  updates; `POST /rollback/<version>`.
- **Done when**: tests cover concurrent writers, patches that fail validation and rollbacks.
- **Touches**: `admin`.

### I26 Listener diffing and graph draining

- **Do**: diff listeners between graphs: add with `AddBinding`, drain removed ones with
  `ShutdownBinding`; long-lived connections stay on the old graph until they end or a drain
  timeout passes.
- **Done when**: a churn test (thousands of swaps while traffic runs, including WebSocket-like
  long connections) drops nothing.
- **Touches**: `listener`, `runtime`.

### I27 File provider

- **Do**: watch the config file, debounce changes, apply through the Admin API's apply path; report
  errors without stopping.
- **Done when**: tests edit the file and see the change applied; a broken edit is reported and
  ignored.
- **Touches**: `provider/file`.

### I28 Certificate manager and storage; ACME decision

- **Do**: a certificate manager interface behind `GetCertificate` (static files move under it); a
  storage interface with a file system implementation and locks; a decision note choosing CertMagic,
  lego or our own ACME client.
- **Done when**: static certificates work through the manager; the ACME decision note exists in
  `docs/decisions`.
- **Touches**: `tlsmgr`, `docs/decisions`.

### I29 ACME HTTP-01 and renewal

- **Do**: obtain certificates with the HTTP-01 challenge; answer challenges on the HTTP listener;
  renew ahead of expiry with jitter and backoff; store account keys and certificates.
- **Done when**: tests against Pebble (the ACME test server) issue and renew certificates.
- **Touches**: `tlsmgr`, `listener`.

### I30 TLS-ALPN-01, OCSP stapling

- **Do**: the TLS-ALPN-01 challenge, and a choice of challenges; OCSP stapling with refresh, for
  certificates from files and obtained ones.
- **Done when**: tests obtain a certificate from Pebble through TLS-ALPN-01 alone, answered by fib's
  TLS; responses are stapled over TLS 1.3 and 1.2, refreshed, and dropped once they expire.
- **Touches**: `tlsmgr`, `runtime`.

I30 was split in three, to keep each part within the size an iteration aims for: I30b and I30c
are the rest of it.

### I30b HTTPS by default

- **Do**: every host name in the config gets a managed certificate and an HTTP→HTTPS redirect
  unless turned off.
- **Done when**: a config with only a host name serves HTTPS against Pebble; a test against Let's
  Encrypt staging is documented and can be run by hand.
- **Touches**: `config`, `runtime`, `listener`.

### I30c ACME Renewal Information (ARI)

- **Do**: renew when the CA's suggested window (RFC 9773) says, checked again as its Retry-After
  says, and by lifetime when the CA has none; decision 007 leaves ARI to fit.
- **Done when**: tests against Pebble and a CA that suggests a window in the past renew in it.
- **Touches**: `tlsmgr`.

### I31 On-demand TLS

- **Do**: issue certificates during the handshake for unknown names, guarded by an "ask" URL and
  rate limits; the handshake waits on the handshake pool, not a worker.
- **Done when**: tests cover allowed, refused and rate-limited names.
- **Touches**: `tlsmgr`.

### I32 Active health checks

- **Do**: HTTP and TCP probes run by the control plane with interval, timeout, thresholds and
  jitter; results update upstream state that the data plane reads.
- **Done when**: tests show a backend leaving and rejoining rotation within the configured window.
- **Touches**: `upstream`.

### I33 Load balancing v2

- **Do**: random, P2C, consistent hashing on IP, header, cookie or URI, sticky sessions by cookie.
- **Done when**: distribution and stickiness tests pass; hashing stays stable when an address is
  added.
- **Touches**: `upstream`.

### I34 Prometheus metrics

- **Do**: the Prometheus text format written by fit (no heavy client library unless a decision note
  says otherwise); counters and histograms per router, service and upstream; connection and pool
  gauges; reload and certificate expiry metrics; served on the Admin listener.
- **Done when**: tests scrape the endpoint; a benchmark shows the cost per request.
- **Touches**: `observe`, `admin`.

### I35 M2 exit tests

- **Do**: the churn test, the auto-HTTPS test and the health check test from the roadmap's M2 exit
  criteria, together in `test/e2e`, run in CI.
- **Done when**: all three pass in CI.
- **Touches**: `test/e2e`.

### I35b Certificate files watched

Left by I09 to I28 and by I28 to I29–I30, and done by none of them; found when M2 was reviewed.

- **Do**: apply the running config again when the certificate or key files it loaded change, as a
  renewal tool outside fit changes them, polling as the file provider does; a pair that does not
  load leaves the running certificates. List the certificates taken on demand in the certificate
  expiry metric too.
- **Done when**: a renewed pair is served without a reload by hand, and a certificate written
  before its key is reported and changes nothing, in an end-to-end test.
- **Touches**: `tlsmgr`, `runtime`, `admin`, `cmd/fit`.

### I35c On-demand names dropped when idle

Left by I31, which keeps every name taken on demand until a restart; found when M2 was reviewed.

- **Do**: drop a name taken on demand that no handshake has asked for in `acme.on_demand`'s
  `idle_timeout`, with a safe default, so that `max_names` is not used up for good; its
  certificate stays in the storage, and the certificate expiry metric stops listing it.
- **Done when**: time-driven tests show an idle name dropped, no longer served, listed or
  counted, and taken on again from the storage, while a name asked for meanwhile is kept.
- **Touches**: `config`, `tlsmgr`.

## M3, gateway features

### I36 Matchers v2 and priorities

- **Do**: method, header, query, remote IP and CIDR, and regexp path matchers; explicit route
  priorities with a documented tie-break.
- **Done when**: table tests cover each matcher and ordering.
- **Touches**: `router`.

### I37 Expression matcher

- **Do**: a small expression language combining matchers with `&&`, `||`, `!` and parentheses
  (`Host("a.com") && PathPrefix("/api")`); compiled at provision time.
- **Done when**: parser tests and fuzzing of the parser; evaluation does not allocate.
- **Touches**: `router`.

### I38 Rewrites, redirects and headers

- **Do**: middlewares for path rewrite (prefix strip, regexp replace), redirects, and request and
  response header set, add and delete.
- **Done when**: table tests per middleware.
- **Touches**: `middleware/rewrite`, `middleware/headers`.

### I39 Rate, concurrency and size limits

- **Do**: token bucket rate limits keyed by client IP, header or route; concurrency (in-flight)
  limits; request body size limits; 429 and 413 responses.
- **Done when**: tests and benchmarks; the limiter's memory is bounded under many keys.
- **Touches**: `middleware/limit`.
- **Read**: fib `middleware/limiter`.

### I40 Basic auth and client mTLS

- **Do**: Basic auth with hashed passwords; client certificate verification on TLS listeners, with
  the certificate subject available to later middleware.
- **Done when**: tests cover good and bad credentials and certificates.
- **Touches**: `middleware/auth`, `listener`.

### I41 ForwardAuth

- **Do**: send a subrequest to an auth service and continue, deny or copy headers based on its
  answer; the request waits for the answer without holding a worker.
- **Done when**: tests cover allow, deny and a slow auth service under load with no worker
  starvation.
- **Touches**: `middleware/auth`.

### I42 JWT and JWKS

- **Do**: verify JWTs (signature, expiry, audience, issuer); JWKS fetched and refreshed by the
  control plane; claims available to later middleware.
- **Done when**: tests cover key rotation and invalid tokens.
- **Touches**: `middleware/auth`.

### I43 Retries and per-route timeouts

- **Do**: retries for idempotent requests and connection failures, with a retry budget; per-route
  overall timeouts.
- **Done when**: tests show retries stop at the budget and never resend a non-idempotent request
  whose body was sent.
- **Touches**: `handler/proxy`, `middleware/retry`.

### I44 Circuit breakers

- **Do**: breakers per upstream on error ratio and latency, with closed, open and half-open states.
- **Done when**: state transition tests.
- **Touches**: `middleware/breaker`.

### I45 Traffic splitting: weights, mirroring, failover

- **Do**: weighted services, mirroring a share of traffic to a second service (responses dropped),
  failover to a backup service.
- **Done when**: tests for each; mirroring does not slow the main request.
- **Touches**: `upstream`.

### I46 Compression

- **Do**: response compression with gzip, zstd and brotli, negotiated by `Accept-Encoding`, skipping
  small and already compressed responses; a decision note on the zstd and brotli libraries.
- **Done when**: tests and a benchmark.
- **Touches**: `middleware/compress`, `docs/decisions`.
- **Read**: fib `middleware/compress`.

### I47 WebSocket proxying

- **Do**: detect upgrades in the proxy, upgrade the upstream connection, then join both sides with
  `Context.Upgrade` and `Tunnel`, with backpressure both ways.
- **Done when**: echo and large-message tests through fit; idle WebSocket memory measured.
- **Touches**: `handler/proxy`.
- **Read**: fib `http/tunnel.go`, `websocket`.

### I48 WebSocket over HTTP/2 and HTTP/3 entries

- **Do**: accept extended CONNECT WebSockets on h2 and h3 entries and proxy them to HTTP/1.1
  upstreams.
- **Done when**: e2e tests over h2 and h3.
- **Touches**: `handler/proxy`.
- **Read**: fib `http/h2_tunnel.go`, `http3/tunnel.go`.

### I49 gRPC proxying

- **Do**: proxy gRPC over h2 and h2c upstreams: trailers, `grpc-timeout` carried as a deadline,
  streaming in both directions, gRPC status on proxy errors.
- **Done when**: unary, server, client and bidirectional streaming tests pass.
- **Touches**: `handler/proxy`.
- **Read**: fib `grpc`.

### I50 TCP proxying

- **Do**: TCP listeners and TCP services in config; proxy bytes both ways with backpressure;
  connect and idle timeouts; load balancing reused from `upstream`.
- **Done when**: throughput and slow-peer tests.
- **Touches**: `l4`, `config`.

### I51 UDP proxying

- **Do**: UDP listeners with sessions keyed by client address, idle timeouts and limits.
- **Done when**: tests with many clients; sessions expire.
- **Touches**: `l4`.

### I52 SNI routing and PROXY protocol

- **Do**: read the ClientHello and route TLS by SNI without terminating it; PROXY protocol v1/v2
  accepted on listeners and sent to upstreams.
- **Done when**: tests cover passthrough by SNI and both PROXY protocol versions.
- **Touches**: `l4`, `listener`.

### I53 One port, many protocols; SOCKS and CONNECT

- **Do**: a listener option that uses fib `mux` to split HTTP, TLS (terminated or passthrough), SOCKS
  and custom protocols on one port; SOCKS and CONNECT forward proxy handlers with access rules.
- **Done when**: one port serves HTTP/1.1, h2, TLS passthrough and SOCKS in an e2e test.
- **Touches**: `listener`, `l4`.
- **Read**: fib `mux`, `multiproto`, `socks`, `http/connect_proxy.go`.
- **Split**: the CONNECT handler went to I53b, since fib's `ConnectProxy` first had to stop waiting
  for its dial on the worker.

### I53b The CONNECT forward proxy

- **Do**: a `forward_proxy` route handler that serves CONNECT over HTTP/1.1, HTTP/2 and HTTP/3 with
  the access rules SOCKS has, on fib's `ConnectProxy`, made not to wait for its dial (A37) and given
  an idle timeout (A36).
- **Done when**: tests tunnel through it over HTTP/1.1 and h2c, refuse clients and destinations the
  rules do not allow, a host name by the address it resolves to too, and close idle tunnels.
- **Touches**: `handler/forward`, `l4`.

### I54 Upstream TLS

- **Do**: TLS to upstreams with custom CAs, client certificates (mTLS) and SNI overrides.
- **Done when**: tests against backends that require client certificates.
- **Touches**: `upstream`.

### I55 M3 exit tests

- **Do**: the M3 exit criteria in `test/e2e` and the harness; a check (benchmark or test hook) that
  no middleware blocks a worker.
- **Done when**: all pass in CI; results committed.
- **Touches**: `test/e2e`, `bench`.

## M4, platform integration

### I56 Provider framework and effective config

- **Do**: a provider interface; merging configs from several providers with written precedence
  rules; record where each part came from; `GET /config/effective` on the Admin API.
- **Done when**: tests merge file and fake providers and show sources.
- **Touches**: `provider`, `admin`.

### I57 Docker provider

- **Do**: watch the Docker API (events and containers) over its Unix socket and turn labels into
  routes and services; a decision note on using the Docker SDK or plain HTTP.
- **Done when**: an e2e test with Docker Compose follows containers as they start and stop.
- **Touches**: `provider/docker`.

### I58 Kubernetes Gateway API I: HTTPRoute

- **Do**: a Kubernetes client (decision note on client-go versus something lighter); watch
  GatewayClass, Gateway and HTTPRoute, and translate them into config.
- **Done when**: tests with a fake API server and a kind-based e2e test.
- **Touches**: `provider/kubernetes`.

### I59 Kubernetes Gateway API II: other routes and status

- **Do**: GRPCRoute, TLSRoute, TCPRoute and UDPRoute; HTTPS listeners, with certificates from
  Secrets (left from I58); write status conditions back to resources.
- **Done when**: tests per route type; status reflects accepted and refused routes.
- **Touches**: `provider/kubernetes`.

### I60 Kubernetes Ingress

- **Do**: translate Ingress resources and a small set of annotations.
- **Done when**: tests per Ingress feature supported.
- **Touches**: `provider/kubernetes`.

### I61 Gateway API conformance

- **Do**: run the Gateway API conformance suite for the core profile; fix failures or split them
  into iterations.
- **Done when**: the claimed profile passes in CI.
- **Touches**: `test/e2e`, `provider/kubernetes`.

### I62 Consul and etcd providers

- **Do**: services from the Consul catalog; config from Consul KV and etcd.
- **Done when**: tests against local Consul and etcd.
- **Touches**: `provider/consul`, `provider/etcd`.

### I63 Shared certificate storage

- **Do**: storage implementations on Redis, etcd and Consul, with distributed locks so one instance
  issues and the others reuse.
- **Done when**: two instances share issuance in a test against Pebble.
- **Touches**: `tlsmgr`.

### I64 Dashboard

- **Do**: a read-only web UI embedded in the binary, reading the Admin API: routers, services,
  upstream health, certificates, config versions.
- **Done when**: the pages render against a running fit; no write actions exist.
- **Touches**: `admin`.

### I65 OpenTelemetry

- **Do**: tracing with W3C trace context propagated to upstreams; spans exported over OTLP; metrics
  over OTLP as well; a decision note on the OTel SDK and its dependency weight.
- **Done when**: tests against an OTLP collector; a benchmark shows the cost with sampling off and
  on.
- **Touches**: `observe`.

### I66 Packaging and examples

- **Do**: a container image, a Helm chart, systemd units, and examples for Docker Compose and
  Kubernetes.
- **Done when**: the examples run as documented.
- **Touches**: `deploy`, `examples`.

## M5, caching and extensions

### I67 Cache I: HTTP semantics and memory tier

- **Do**: a cache middleware with RFC 9111 rules (what is storable, freshness, `Vary`,
  revalidation with validators, `Age`); a memory tier with a size bound and LRU-style eviction.
- **Done when**: tests per caching rule; a benchmark of hits.
- **Touches**: `middleware/cache`.

### I68 Cache II: disk tier

- **Do**: a disk tier with an index in memory; disk reads and writes on a pool of their own; entries
  promoted from disk to memory.
- **Done when**: tests survive restarts with the index rebuilt; workers never wait on disk.
- **Touches**: `middleware/cache`.

### I69 Cache III: locks, stale, purge

- **Do**: cache locks so a miss fetches once; stale-while-revalidate and stale-if-error; purge by key
  and tag through the Admin API.
- **Done when**: the stampede test makes one upstream request; hit-ratio scenarios compared with
  nginx `proxy_cache` in the harness.
- **Touches**: `middleware/cache`, `admin`, `bench`.

### I70 `fit build` and compile-time modules

- **Do**: `fit build --with module@version` that generates a main package importing extra modules
  and builds a binary, like xcaddy; a documented module template.
- **Done when**: a sample external module builds into fit and runs.
- **Touches**: `cmd/fit`, `plugin/build`.

### I71 WASM I: runtime and plugin pool

- **Do**: load WASM modules with a pure-Go runtime (decision note, wazero by default); run calls on a
  pool of their own; a minimal ABI for reading and changing headers.
- **Done when**: a header-editing plugin works; its cost per request is measured and bounded.
- **Touches**: `plugin/wasm`, `docs/decisions`.

### I72 WASM II: Proxy-Wasm subset

- **Do**: implement the Proxy-Wasm HTTP filter calls needed by common filters, if I71's numbers
  allow; otherwise record why not.
- **Done when**: an existing Proxy-Wasm filter runs unchanged, or the decision note explains the
  alternative.
- **Touches**: `plugin/wasm`.

### I73 Friendly config format

- **Do**: an adapter from a human-friendly format (decision note: YAML, a Caddyfile-like syntax or
  other) to native JSON; `fit adapt`.
- **Done when**: round-trip tests on the example configs.
- **Touches**: `adapter`, `cmd/fit`.

### I74 nginx config converter

- **Do**: parse nginx configs and convert a common subset (server, location, proxy_pass, root,
  return, rewrite, upstream, ssl_certificate, limit_req) to native JSON; list everything skipped.
- **Done when**: sample configs convert, and each skipped directive is reported.
- **Touches**: `adapter/nginx`.

### I75 FastCGI

- **Do**: a FastCGI client over fib connections, pooled; a `fastcgi` handler with the usual PHP
  parameters and split path info.
- **Done when**: e2e tests against PHP-FPM.
- **Touches**: `handler/fastcgi`.

## 1.0

### I76 Continuous fuzzing

- **Do**: fuzz targets for config loading, the expression parser, the nginx parser, header
  handling in the proxy and the PROXY protocol parser; run them in CI on a schedule.
- **Done when**: the targets run in CI and their corpora are committed.
- **Touches**: fuzz tests across packages.

### I77 Config and Admin API v1 freeze

- **Do**: review the schema and the API for names and shapes worth keeping; mark them v1; write the
  compatibility policy; add deprecation warnings for anything renamed.
- **Done when**: `docs/compatibility.md` exists and the schema carries its version.
- **Touches**: `config`, `admin`, `docs`.

### I78 Reference docs and migration guides

- **Do**: a getting started guide; a config reference generated from the schema; migration guides
  from nginx, Caddy and Traefik, in English and Chinese.
- **Done when**: the docs build and every config field has a description.
- **Touches**: `docs`.

### I79 Caddyfile converter, JSON with comments

- **Do**: convert a common subset of Caddy's Caddyfile to native JSON, as I74 does nginx's, listing
  everything skipped by line; let native JSON have `//` and `/* */` comments and trailing commas,
  with errors still at their lines and columns ([decision 020](decisions/020-caddyfile-and-jsonc.md)).
- **Done when**: sample Caddyfiles convert, each skipped directive reported, and a converted one is
  served end to end; JSON with comments loads from a file and through the Admin API.
- **Touches**: `adapter/caddyfile`, `adapter`, `config`, `admin`, `cli`.

### I80 fit's own .conf format; configs in several files

- **Do**: a block format of fit's own, `.conf`, read field for field from the config's types and
  each module's params, so that it says everything JSON and YAML do, with `site` to write the
  routes of a set of host names together, and written back from JSON; configs read from several
  files and directories, merged, with errors at their files; the Caddyfile and nginx converters
  kept as migration tools, out of the config docs
  ([decision 021](decisions/021-fit-format-and-config-files.md)).
- **Done when**: every template, example and reference example has a `.conf` form that loads as its
  JSON does; a directory of files in the three formats runs, and is reloaded when a file is added,
  changed or taken out; the docs and templates speak of `.conf`, YAML and JSON.
- **Touches**: `adapter/fitconf`, `adapter`, `config`, `provider/file`, `cli`, `schema`, `docs`,
  `templates`, `examples`.

### I81 WASM III: Proxy-Wasm's response callbacks

- **Do**: make Proxy-Wasm's response callbacks, `proxy_on_response_headers`, `_body` and
  `_trailers`, on the plugin's pool, with the header held by fib's A41 and the body streamed through
  an asynchronous body filter fib adds as A46; bounded in responses followed and body held, with the
  plugin's timeout ([decision 023](decisions/023-proxy-wasm-response.md)).
- **Done when**: Proxy-Wasm filters read and change a response's header, body and trailer, the
  SDK's `http_headers` and `http_body` examples running unchanged, over HTTP/1.1, h2c, h2 and
  HTTP/3, and a client that does not read or a slow plugin holds up no one else.
- **Touches**: `plugin/wasm`, fib's `http`.

### I82 fit's own log: loggers and levels

- **Do**: a top-level `log` for what fit says about itself, divided into loggers (server, admin,
  tls, provider, telemetry, access_log, plugin, fastcgi, and engine for fib and Go's `log` and
  `log/slog`), each with a least level, debug, info, warn or error, that a reload changes; written
  as text, as before, or JSON, to stdout and stderr by level, or one output. The access log's
  `level` writes only the requests answered with 4xx and 5xx, or 5xx. `fit adapt` converts nginx's
  `error_log` and `access_log`, and Caddy's global `log`, `debug` and a site's `log { level }`.
- **Done when**: every message fit writes has a logger and a level; a config without `log` writes
  the lines fit wrote before, each to stdout or stderr by its level; levels change on reload; the converters' tests cover each
  directive; the access log's micro-benchmark shows no cost.
- **Touches**: `logs` (new), `config`, `cli`, `admin`, `runtime`, `observe`, `tlsmgr`, `provider`,
  `plugin/wasm`, `handler/fastcgi`, `adapter/nginx`, `adapter/caddyfile`, `schema`, `docs`.

### I83 fit's log in fib's formats; loggers of their own; the access log's formats

- **Do**: write fit's log with fib's package `log` (fib's `log/log.go`), whose handlers write
  fib's own lines, so that fit and fib look alike: `log.format` is `line` (the default), `text`
  (logfmt) or `json`, and fib's messages go to the `engine` logger through `fiblog.SetLogger`.
  `log.loggers` gives a logger a `level`, an `output` and a `format` of its own over the log's.
  `access_log.format` writes a request's line as `json` (the default, fields unchanged), `text` or
  `line`, with the same fields. Caddy's `format console` converts to `line`.
- **Done when**: each logger writes in its own format to its own output; the engine's messages,
  fib's among them, are the engine logger's; the access log's three formats carry the same fields,
  quoting what a client sends; a reload warns when a logger's output or format changed; the JSON
  access log's benchmarks show no cost.
- **Touches**: `logs`, `config`, `cli`, `admin`, `observe`, `adapter/caddyfile`, `adapter/nginx`,
  `schema`, `test/e2e`, `docs`; fib's `log` (fib `185658f1`, `33a039f1`).

The external security review in the roadmap's 1.0 section is not an iteration: it is arranged
outside the code once I77 is done.

## P2P, NAT traversal

Two hosts behind NAT connect directly through holes punched with ICE, or through a TURN relay where
no hole can be punched, and carry TCP and UDP services between them. The design is
[decision 025](decisions/025-p2p.md): STUN, TURN and ICE on fib, interoperable with other agents,
and QUIC over the selected pair; [decision 029](decisions/029-webrtc.md) amends it, so that I86–I89
use fib's `webrtc/stun`, `webrtc/turn` and `webrtc/ice` rather than packages of fit's own.

### I84 STUN messages and the STUN server

- **Do**: package `p2p/stun`: STUN messages (RFC 8489) parsed without allocating and built by
  appending; the attributes ICE and TURN need, with (XOR-)MAPPED-ADDRESS, USERNAME, REALM, NONCE,
  ERROR-CODE, SOFTWARE, MESSAGE-INTEGRITY and MESSAGE-INTEGRITY-SHA256 (short- and long-term keys),
  FINGERPRINT and UNKNOWN-ATTRIBUTES; RFC 7983's test for a STUN datagram; a STUN server as a fib
  handler that answers Binding requests over UDP and TCP.
- **Done when**: RFC 5769's test vectors parse and check, messages round-trip, malformed ones are
  refused (fuzzed), a Binding request over UDP and TCP is answered with the address it came from,
  and an unknown comprehension-required attribute gets a 420.
- **Touches**: `p2p/stun`, `docs`.

### I85 A UDP peer opened toward an address (A49, in fib)

- **Do**: [A49](fib-api-requests.md#a49-a-udp-peer-opened-toward-an-address-on-a-listeners-socket)
  in fib, by fib's standards; fit moves to that commit.
- **Done when**: a peer opened toward an address sends first from the listener's port and hears the
  answer; it times out, closes and is counted as other peers are, on every backend.
- **Touches**: fib's engine; `go.mod`.
- **Done by I108**: fib had built A49 as `96a2331`, synchronous, and fit took it with fib `80c7f76`.

### I86 The STUN client and NAT behaviour

- **Do**: Binding transactions on a fib UDP peer with `webrtc/stun`'s transactions (RFC 8489 section
  6.2.1's RTO doubling, Rc and Rm), over UDP and TCP; `p2p/stun`'s messages replaced by
  `webrtc/stun`'s, its server kept; the server-reflexive address; RFC 5780's mapping and
  filtering tests, for the logs and for ICE's choice of candidates; the server answers
  OTHER-ADDRESS and CHANGE-REQUEST when it has a second address.
- **Done when**: transactions time out and retransmit on schedule with a fake clock, a lost answer
  is retried, and the NAT tests tell endpoint-independent from address-dependent mapping in tests.
- **Touches**: `p2p/stun`.

### I87 The TURN server

- **Do**: the role `webrtc.turn` ([decision 029](decisions/029-webrtc.md)) on fib's
  `webrtc/turn/turnserver`, on the listeners' engine: UDP addresses, the realm, static users (a
  password or a long-term key) and coturn's `use-auth-secret`; the relay and public addresses and a
  port range; allocations in all and per user, relayed bytes a second per user and direction,
  lifetimes; peers allowed and denied over fib's denial of internal addresses; users, secret, peers
  and bandwidth changed on reload without a restart; metrics in `/metrics` through `app`; the
  `webrtc` logger; the schema's chapter. TCP and TLS to the server, permission and channel caps,
  events and drains are fib's (A55–A58), taken up in I111.
- **Done when**: fib's TURN client allocates through fit, relays both ways to a peer, by indication
  and channel; a wrong password and an expired ephemeral credential are refused; quotas refuse with
  486 and 508; bandwidth past its rate drops; a private peer is refused unless allowed; a reload of
  users keeps the allocation, and one of the address moves the server; a client cut midway frees its allocation
  at its lifetime's end; under the race detector.
- **Touches**: `webrtc/turn`, `config`, `app`, `admin`, `schema`, `docs`.

### I88 The TURN client

- **Do**: fib's `turn.Client` (allocate, refresh, permissions and channels, the 401/438 nonce
  dance) driven from a fib UDP peer, and over TCP and TLS (A55, in fib since `84a67f7`); data to and from
  peers through the allocation, by channel once bound.
- **Done when**: the client works against fit's server and keeps an allocation alive past its
  lifetime; a stale nonce is retried.
- **Touches**: `p2p`.

### I89 The ICE agent

- **Do**: fib's `webrtc/ice` agent in `p2p`: host, server-reflexive and relayed candidates on one
  socket per component; priorities, pairs and the checklist (RFC 8445), with the controlling and controlled
  roles and their conflicts; regular nomination; trickle (RFC 8838); consent freshness (RFC 7675);
  candidates in RFC 8839's syntax; a pair's path as a `quic.PacketConn`. Limits on candidates and
  pairs; timers on fib's.
- **Done when**: two agents connect over loopback, through a simulated NAT of each kind and through
  a TURN relay when the NATs are symmetric; a lost consent fails the pair; nothing blocks a worker.
- **Touches**: `p2p`.

### I90 QUIC DATAGRAM frames (A50, in fib)

- **Do**: [A50](fib-api-requests.md#a50-quic-datagram-frames) in fib; fit moves to that commit.
- **Done when**: datagrams go both ways, are never retransmitted, and are refused past the peer's
  limit.
- **Touches**: fib's `http3/quic`; `go.mod`.

### I91 Tunnels over a connected pair

- **Do**: QUIC over an ICE pair, both peers authenticated by certificate fingerprints; a TCP
  connection to a service as a bidirectional stream, a UDP flow as DATAGRAM frames with a flow ID;
  backpressure both ways (rule 8); limits on streams and flows; a path change from relayed to
  direct carried by QUIC's migration.
- **Done when**: TCP and UDP services are reached through the tunnel with slow peers, large
  transfers and cuts midway (rule 13), direct and relayed.
- **Touches**: `p2p`.

### I92 Signaling and the config's P2P apps

- **Do**: a signaling endpoint on the rendezvous fit that pairs peers by name and trades their
  credentials, fingerprints and candidates (trickled), with each peer authenticated; the config's
  `stun` server on listeners, and `p2p` peers that expose or reach services, relayed by
  `webrtc.turn` (I87) or another TURN server; schema,
  Admin API, metrics; the friendly formats' forms.
- **Done when**: two fit processes behind simulated NATs reach each other's services through a
  third, set up from config alone, and reload without dropping open tunnels.
- **Touches**: `p2p`, `config`, `schema`, `listener`, `runtime`, `admin`, `observe`, `test/e2e`,
  `docs`.

### I93 P2P interop and exit tests

- **Do**: fit's STUN and TURN servers against coturn's and pion's clients, fit's agent against
  pion and libjuice; throughput and latency through a direct and a relayed tunnel on the benchmark
  host.
- **Done when**: the interop runs pass in CI or a recorded run, and the measurements are committed.
- **Touches**: `test/e2e`, `bench`, `docs`.

## VPN, a network of fit nodes

Nodes join one overlay network, each with an address and a TUN interface; IP packets go between
them directly through holes ICE punched, or through the server node's relay, reached over QUIC,
WebSocket or HTTP/2 in a configured order with fallback, or over the one transport a node is set
to on the web page. The design is [decision 026](decisions/026-vpn.md): built on the P2P milestone,
IP packets as QUIC DATAGRAM frames on an end-to-end connection, and the relay as one more path of
that connection.

### I94 IP packets and the overlay route table

- **Do**: package `vpn/packet`: IPv4 and IPv6 headers read without allocating (version, length,
  protocol, source and destination), refusing what a TUN should not hand over; the overlay route
  table, built once from prefixes and the node each belongs to, with a longest-prefix lookup that
  neither allocates nor locks, and the check that a peer's packet comes from one of its prefixes.
- **Done when**: well-formed and malformed packets of both versions are told apart (fuzzed), the
  longest prefix wins, duplicate and invalid routes are refused when the table is built, and
  lookups allocate nothing.
- **Touches**: `vpn/packet`, `docs`.

### I95 A packet device's descriptor on the poller (A51, in fib)

- **Do**: [A51](fib-api-requests.md#a51-a-packet-devices-descriptor-on-the-poller) in fib, by fib's
  standards, on epoll and kqueue; fit moves to that commit.
- **Done when**: a descriptor attached to an engine delivers one packet per `OnData` and sends one
  per `Send`, a full device drops and counts rather than queueing without bound, and it closes with
  the engine; tested with a pipe-like packet descriptor (a `SOCK_DGRAM` socketpair) on every Unix
  backend.
- **Touches**: fib's engine; `go.mod`.

### I96 TUN devices on Linux, macOS and FreeBSD

- **Do**: package `vpn/tun`: open a device (`/dev/net/tun`, `utun`, `/dev/tun`), with its name,
  MTU, IPv4 and IPv6 addresses and routes set, and removed on close; packets in and out through
  A51, the 4-byte family header of macOS and FreeBSD taken off and put on; setup on the control
  plane's bounded pool.
- **Done when**: on each system, as root, a device comes up with its address, a ping to an address
  in its prefix is read from it and an answer written to it reaches the pinger; without the
  privilege, opening fails with a clear error. CI runs the Linux test as root; macOS and FreeBSD
  are run by hand and recorded.
- **Touches**: `vpn/tun`.

### I97 TUN devices on Windows with Wintun

- **Do**: `vpn/tun` on Windows: `wintun.dll` loaded at run time, an adapter and session, the
  receive ring waited on by one goroutine per device that hands packets to a fib worker, sends from
  the worker; addresses and routes by the IP Helper API; the DLL in the Windows release archives.
- **Done when**: as Administrator, the same ping test as I96 passes, a missing DLL fails with a
  clear error, and closing the device ends its goroutine.
- **Touches**: `vpn/tun`, the release packaging.

### I98 Relay frames and the relay server, over WebSocket

- **Do**: package `vpn/relay`: the frames (`DATA`, `GONE`, `PING`, `PONG`) encoded and decoded
  without allocating; the server, a WebSocket handler that authenticates a session by its ticket,
  forwards `DATA` by node ID with the source set from the session, answers `GONE` for a node with no
  session and drops past a session's queue; the WebSocket client; limits and timeouts of decision
  026.
- **Done when**: two clients exchange frames through the server, a forged source is impossible, a
  slow session drops and counts instead of queueing, a cut session frees its node, and expired or
  bad tickets are refused.
- **Touches**: `vpn/relay`.

### I99 Relay over QUIC and HTTP/2; transports in order, with fallback

- **Do**: the relay server and client over QUIC (`DATA` as DATAGRAM frames, the rest on a stream)
  and over an HTTP/2 extended CONNECT stream; the client that dials `relay.transports` in order
  with a connect timeout, falls back, starts again from the top with backoff, probes the
  transports ahead of the current one and moves to the first that answers, make before break; a
  fixed `relay.transport`; its state (transport, tries, last error) for the dashboard.
- **Done when**: with each transport blocked in turn the client lands on the next, moves back when
  the better one returns without losing the session's frames in flight, a fixed transport never
  falls back, and all three carry the same frames; timers on a fake clock.
- **Touches**: `vpn/relay`.

### I100 The coordinator and the netmap

- **Do**: on the server node, joining with a token, node IDs and overlay addresses (IPv4 and IPv6)
  from the configured network, kept across restarts; the netmap (peers, addresses, routes,
  fingerprints, per-node `p2p` and `relay.transport`) pushed over I92's signaling endpoint when it
  changes; relay tickets signed and renewed; approved subnet routes; limits on nodes and routes.
- **Done when**: nodes join, get stable addresses, see each other appear and leave, a change of a
  node's settings reaches it, and a node with a bad token or past the limit is refused.
- **Touches**: `vpn`, `p2p`.

### I101 The node: TUN to peers, direct or relayed

- **Do**: the node: packets read from the TUN routed by `vpn/packet` to a peer's end-to-end QUIC
  connection as DATAGRAM frames, received ones checked against the peer's prefixes and written to
  the TUN; each peer's connection opened over the relay and moved to ICE's direct pair by path
  migration, and back when consent fails; ICE skipped when `p2p` is off for either side; packets
  waiting while a connection opens bounded; ICMP too-big for oversized packets.
- **Done when**: in network namespaces behind simulated NATs, two nodes ping and carry TCP and UDP
  directly, through the relay when the NATs are symmetric or `p2p` is off, and keep a TCP
  connection open across a move from relay to direct (rule 13: slow peers, large transfers, cuts).
- **Touches**: `vpn`, `test/e2e`.

### I102 VPN config, Admin API and the dashboard's pages

- **Do**: the config's `vpn` apps (server and node), schema and the friendly formats' forms; the
  Admin API's node list and per-node settings; dashboard pages: on a node, its peers, each path
  (direct or relayed, and over which transport), round trip and bytes, and its own
  `relay.transport` and `p2p`; on the server, every node with the same settings, delivered in the
  netmap, showing which setting is in force; metrics.
- **Done when**: a VPN is set up from config alone, changing a node's transport on either page
  moves its relay session without dropping its peers' connections, and a reload keeps tunnels open.
- **Touches**: `vpn`, `config`, `schema`, `admin`, `observe`, `runtime`, `docs`.

### I103 VPN end-to-end and exit tests

- **Do**: end-to-end runs on Linux in CI (namespaces, NATs, each relay transport blocked in turn),
  macOS, FreeBSD and Windows nodes joined by hand and recorded; throughput and latency direct and
  through each relay transport on the benchmark host.
- **Done when**: the runs pass, and the measurements are committed.
- **Touches**: `test/e2e`, `bench`, `docs`.

## Console, logins and an encrypted store

fit serves, when configured, a console behind a user name and password from which every part of
the config is changed, and keeps the config encrypted in a file of its own format, with its
password in the config file fit starts with. The design is
[decision 027](decisions/027-console-and-store.md).

### I104 The config kept encrypted; the console

- **Do**: `admin.store`: the fitstore format (Argon2id, XChaCha20-Poly1305), the store as the base
  config fit runs from, seeded from the bootstrap file, written on every change, read again on
  SIGHUP, and `admin` put in from the bootstrap file, never kept or shown; `fit store export` and
  `fit store import`. `admin.console`: a server of its own with TLS, bcrypt logins limited per
  address, sessions in cookies with idle and absolute ends, CSRF tokens and origin checks, the
  Admin API's endpoints called in process; pages with a form for every object, list and module of
  the schema, a JSON editor, and the versions with rollback.
- **Done when**: a config changed in the console runs, is in the store and survives a restart; a
  wrong password or a changed byte stops fit from starting; logins, lockouts, sessions, CSRF and
  other origins are tested; every page works in a browser.
- **Touches**: `admin`, `admin/store`, `config`, `cli`, `schema`, `templates`, `docs`.

## Agent and tunnel

agenthub and fibhub, two programs on fib, brought into fit as roles of its config, on its engine,
their settings kept in its store ([decision 028](decisions/028-llm-and-tunnel.md)).

### I105 Roles in fit's config

- **Do**: `agent` and `tunnel` in the config, a member per role, with `RoleConsole`; `app`: a
  registry of roles, a `Manager` moved to every config applied after the listeners, `app.Check`
  when a config is compiled, and `Host.Edit`, which applies a role's member changed, through
  `admin.Configs.EditMember`, on a goroutine of its own; `login`, the console's logins made a
  package of their own; `app/console`, a role's pages on the listeners' engine; `fit run`, `fit
  build` and `cmd/fit` running the roles.
- **Done when**: a config starts, moves and stops roles; a role's failure is a warning; an edit
  is applied, kept in the store, and refused when it does not load; the console's logins pass
  their tests on both servers.
- **Touches**: `app`, `login`, `admin`, `config`, `cli`, `listener`, `runtime`, `plugin/build`.

### I106 The tunnel

- **Do**: fibhub's server, pod and client as `tunnel.server`, `tunnel.node` and `tunnel.client`:
  links over TCP, WebSocket and QUIC (on a UDP binding of the engine), tunnels with backpressure,
  forwards and web proxies, pages on `app/console`, every change written back; fibhub's store and
  CLI dropped.
- **Done when**: the e2e tests pass on fib engines, with a restart from the store; the Docker
  environment's checks pass.
- **Touches**: `tunnel`, `config`, `test/docker/tunnel`, `docs`.

### I107 The agent

- **Do**: agenthub's hub, node and client as `agent.hub`, `agent.node` and `agent.client`, ported
  to today's fib: the mesh on fib's WebSocket with credits on its streams, the node's proxy on
  fib's asynchronous client, streaming both ways, blocking work on bounded pools, pages on
  `app/console`, every change written back; agenthub's state file, lock and passwords dropped.
- **Done when**: the e2e tests, with slow, large and cut bodies, pass on fib engines, with a
  restart from the store; the Docker environment's checks pass.
- **Touches**: `agent`, `config`, `test/docker/agent`, `docs`.

## WebRTC

fit serves WebRTC on fib's `webrtc` packages: a TURN server, WHIP and WHEP with fit as the media
server, and fit in front of other media servers, for their signaling and their media
([decision 029](decisions/029-webrtc.md)). The TURN server is I87, in the P2P milestone.

### I108 WebRTC on fib's packages

- **Do**: decision 029, amending 025; this file's and progress's rows; fit on fib `80c7f76`, the
  first with `webrtc`; A49's entry made to say what fib built (`96a2331`); fib requests for what
  the TURN server lacks (A55–A58).
- **Done when**: the full test suite passes on fib `80c7f76`, and the docs' links resolve.
- **Touches**: `go.mod`, `docs`.

### I109 WHIP and WHEP: an SFU on a shared Mux

- **Do**: the role `webrtc.media`, a `webrtc.Mux` on a UDP binding of the listeners' engine, with
  its public address and ICE servers; handler modules `whip` and `whep`: streams by name from the
  route, a publisher's RTP forwarded to its viewers with key frame requests relayed, the route's
  middleware for auth, limits on streams, viewers a stream, PeerConnections in all and bitrate;
  DELETE and PATCH (trickle) per RFC 9725 and the WHEP draft, ICE servers in `Link` headers (TURN
  credentials made from `webrtc.turn`'s secret when it has one); everything closed on reload and
  stop.
- **Done when**: fib's `PeerConnection` publishes and plays through fit; a publisher gone ends its
  viewers; slow and cut viewers do not hold the publisher; the limits refuse; under the race
  detector.
- **Touches**: `webrtc`, `modules`, `config`, `schema`, `docs`.

### I110 WHIP and WHEP proxied; UDP routed by ICE ufrag

- **Do**: WHIP and WHEP signaling proxied to upstream media servers through fit's upstream pools,
  the answer's candidates rewritten to fit's public UDP port and its ufrag remembered against the
  backend's media address; an L4 UDP service that routes a new peer by the STUN USERNAME of its
  first datagram, then relays its 5-tuple, sticky, with limits and idle timeouts. Built as the
  handler `webrtc_proxy` and the role `webrtc.proxy` beside `webrtc.media`, not an `l4` service
  mode: the sessions outlast reloads, which drain an L4 service with its graph, and the handler
  and the relay share their routes through the role ([decision 029](decisions/029-webrtc.md)).
- **Done when**: two media servers behind one fit port each get their own peers; a peer's
  datagrams before its Binding request are dropped; a backend gone frees its routes; under the race
  detector.
- **Touches**: `webrtc`, `upstream`, `config`, `schema`, `docs`.

### I111 WebRTC on fib's A55–A59

- **Do**: fit on fib `84a67f7`, with A55–A59. `webrtc.turn`: the rule-1 breach I87 recorded goes
  (A56); `max_permissions` and `max_channels` (A57); a peer check that sees the port, so that
  `relay_ip` is reached on the relayed ports alone and on ports `allowed_peers` names, never on
  fit's other ports (A57); `OnEvent` into the `webrtc` logger and `/metrics`, `Stats()` into gauges
  (A57); a reload that changes the quotas, caps, lifetimes and credentials in place, and a change
  of the addresses that drains the old server for the listeners' drain timeout (A58);
  `listen_tcp` and `listen_tls`, with certificates from files as listeners have them,
  `max_stream_message` and `stream_idle_timeout`, the free-address probe over TCP too (A55).
  `webrtc.proxy`: `relay_ip`, `port_min` and `port_max`, checked when loaded, its relays dialed
  with `DialWith`, an exhausted range counted as a drop (A59). fib requests A55–A59 marked done.
- **Done when**: TCP and TLS allocations through fit with fib's client relay both ways; caps
  refuse with 508; the port-aware check refuses fit's other ports and lets two clients relay to
  each other; events and metrics count; a reload keeps allocations with changed limits; an address
  change drains; the relay leaves from the configured port range, and a full range is dropped and
  counted; under the race detector.
- **Touches**: `webrtc`, `config`, `app`, `runtime`, `tlsmgr`, `cli`, `schema`, `docs`, `go.mod`.

### I112 The agent's model APIs

- **Do**: model kinds `gemini`, `azure_openai`, `ollama` and `cohere` beside `openai` and
  `anthropic`, with `api_version` for Azure; every stateless inference call of each as a table
  entry: OpenAI's moderations, images, audio and rerank beside chat, responses, completions and
  embeddings, Gemini's generate, stream, count and embed methods, Ollama's chat, generate and embed,
  Cohere's v2 chat, embed and rerank. The model read from a JSON body, a multipart form's field or
  Gemini's path, and `upstream` set where it was read; each kind's URL, key header and check on the
  node; the vendors' SDK paths, key headers, error shapes and model lists on the client;
  `ProxyReq.content_type` and `query`. agent.client described as the path from a machine or LAN
  through a public hub to other machines' nodes; the agent's docs and code name no earlier program.
- **Done when**: each kind is called end to end through a client, directly and through a hub, its
  upstream seeing its own URL, key header and model; uploads stream, or are held within
  `max_body_bytes` only to set the model; binary, SSE and NDJSON answers pass through; slow agents,
  slow upstreams, large bodies and cut connections hold; under the race detector.
- **Touches**: `agent`, `config`, `schema`, `docs`, `examples`, `test/docker/agent`.

### I113 The LLM roles' node list, links and traffic

- **Do**: a hub's list of its nodes (details, hardware, models, load, its links with them and their
  traffic) pulled by its proxies as they link and pushed when it changes, coalesced and bounded;
  the proxy's read-only nodes page. Every mesh link's bytes each way and the round trips of its
  pings; every role's model calls counted per route, their bytes read from the streams as they
  cross; `/mesh/status`; a link map and a table of the links and their traffic on each role's
  pages, after the tunnel's. The pages set what they could not: every role's `limits`, a node's
  upstream bounds, a hub's `max_nodes`, a proxy's `api`, through the write-back, checked as the
  config is.
- **Done when**: a node that joins, changes or leaves shows on the proxy, within the list's bounds;
  the bytes of a known call agree across proxy, hub and node, for an inline and a streamed body; a
  link has a latency on both ends; a call cut midway is a failure on every role that saw it; the
  settings' edits are written back and a bad value is refused with 400; under the race detector.
- **Touches**: `llm`, `docs`.

## Media: auth, streaming and pages

Users asked for auth they can configure on every protocol fit takes media over, a streaming
server, and pages to use them with ([decision 030](decisions/030-media-auth-and-pages.md)). One
set of named policies comes first; the streaming role and the pages build on it, side by side.

### I115 Media auth policies

- **Do**: top-level `media_auth`: named policies of static tokens (or their SHA-256), users (a
  password, or a bcrypt hash), a secret for tokens fit signs, public actions and a token lifetime,
  each token and user with the stream patterns it may publish and play. Package `mediaauth`: the
  checks, never blocking a worker (bcrypt on a bounded pool of its own), credentials from HTTP
  (Bearer, Basic, `?token=`, a cookie) and from URLs (`token`, `user` and `pass`), signed tokens,
  policies found by engine and name and changed in place on reload by the role `media_auth`.
  `auth` on `whip`, `whep` and `webrtc_proxy` (with `action`); `fit media-token`.
- **Done when**: a POST without credentials is answered 401 with a Bearer challenge, one with
  credentials that do not allow it 403, one that does 201, for each handler, for static, signed
  and query tokens, Basic and a bcrypt user; a session's DELETE needs no credentials; a reload
  changes a policy in place; a token from `fit media-token` is taken by fit; the config refuses an
  unknown policy, an empty one, a bad pattern, token with token_sha256; under the race detector.
- **Touches**: `mediaauth`, `config`, `module`, `runtime`, `app`, `webrtc/media`, `cli`, `schema`,
  `docs`.

### I116 The streaming role

- **Do**: fib's `media/hub` run by fit: a top-level `streaming` with RTMP, RTSP and SRT listeners,
  each with `auth`, limits (streams, players a stream, timeouts) and reloads by fit's conventions;
  HTTP egress as handlers, `httpflv`, `hls`, `stream_whip` and `stream_whep` on fib's `media/rtc`,
  each with `auth`; credentials in each protocol's way (RTMP and SRT query or streamid, RTSP Basic
  and Digest, HTTP through `mediaauth.FromHTTP`, HLS carrying a query token to its segments).
- **Done when**: ffmpeg in a Linux container pushes over RTMP, RTSP and SRT with and without valid
  credentials and plays over each egress; limits and reloads hold; under the race detector.
- **Touches**: `streaming` (new), `config`, `app`, `schema`, `docs`, `examples`, `scripts`,
  `test/workers`.

### I117 The media pages

- **Do**: a handler `media_pages`, its assets embedded, serving an index, a room call over WHIP and
  WHEP, publish and play pages; a login against the policy's users that issues a signed token as
  an HttpOnly cookie; a room API, bounded, through which members announce their microphone and
  camera state; every publisher always offers an audio and a video transceiver, with a synthetic
  track when there is no device, and the switches never renegotiate; webrtc.media checked against
  silent publishers and viewers asking for a kind not sent.
- **Done when**: the pages work in headless Chromium with fake devices and with none; the room
  API's limits, auth and cut connections hold under Go tests; an example config wires it all.
- **Touches**: `handler/mediapages` (new), `webrtc/media`, `schema`, `docs`, `examples`.

### I118 The media pages' room chat

- **Do**: a chat in the room call: text messages and small files (at most `max_file_bytes`, 5 MiB
  by default) through the room API (`POST .../chat`, `POST .../files`, `GET .../files/<id>`, a
  `history` event and one `chat` event a message on `events`), a member named with the key its
  join returned and never in a URL; `pages` to serve the pages or none, the login and the room API
  staying; `mic_default` and `cam_default` for the microphone and camera a member joins with, the
  URL's `mic=0|1` and `cam=0|1` overriding them; the chat bounded to 64 messages a room and 64 MiB
  of file bytes across the handler's rooms, a watcher that falls behind cut off.
- **Done when**: two members exchange a text message and a file through the page, and a late
  watcher gets the history; a file past `max_file_bytes` is answered 413 and a body past
  `max_message_bytes` 413; `pages: false` answers 404 for the pages while the login and the room
  API stay; the bounds, eviction and slow watchers hold under the race detector; the pages' browser
  test joins with the microphone and camera off, switches them on and sends a message and a file.
- **Touches**: `handler/mediapages`, `schema`, `docs`, `test/docker/mediapages`.

### I119 Media tokens from a route

- **Do**: a handler `media_tokens`, a route a business backend calls to sign many `media_auth`
  tokens at once and revoke them: batch issuance (`POST <prefix>`, per-token subject, publish and
  play patterns, ttl), the route's `prefix` and the `prefixes` bounds on the stream names, plus
  `max_ttl`, `max_tokens`, `max_body_bytes` and `revoke_timeout`; a `keys` keyring in `media_auth`,
  each key a name with its own `secret` or `secret_file`, in place of the single `secret` or
  `secret_file`: a token is signed with one key, the one a batch's `key` names or the ring's first,
  and checked against every key, so rotating one key (`POST <prefix>/revoke` with `{"key": ...}`)
  refuses only the tokens that key signed while the others stay good, and `{"all": true}` rotates
  every key; the revoke writes 32 random bytes to each rotated key's file, atomically, on a bounded
  pool of its own, then replaces the key, the tokens signed after it living until their expiry; the
  files keep the new secrets across restarts, and a config apply re-reads them; two keys sharing a
  secret are refused; the route's middleware authenticates the caller, so the handler is not the
  Admin API; `media_auth`'s `secret`, `secret_file` and `keys` are one of them.
- **Done when**: a backend's route issues a batch whose tokens a policy accepts for the streams
  asked and refuses for others, the batch signed with the `key` it names; a bad subject, pattern,
  ttl, key or a path outside the prefix is refused and the batch issues none; a revoke of one key
  leaves its file holding a new secret of at least 16 bytes, its old tokens refused and a new one
  accepted by a policy compiled from the file, while the other keys' tokens still verify; `all`
  rotates every key; a restart keeps the revocation; the route's middleware, the bounds and the
  revoke pool's 503, 504 and 500 hold under the race detector; `TestChapterExamples` and the
  workbench's schema descriptions cover the new fields and params.
- **Touches**: `handler/mediatokens` (new), `config`, `mediaauth`, `cli`, `modules/standard`,
  `schema`, `docs`, `test/workers`.

### I120 The workbench's config pages and console passwords

- **Do**: every admin console account changes its own password and a logged-in user may reset another's,
  kept in the store file; the config menu with no Gateway/System tree, its global modules and its
  protocol modules peers in one flat sidebar; every config page edited either as a form (a tree with an
  include checkbox for each optional member and inputs) or as raw `.conf`, `.yaml` or `.json` text of the
  members that page is about, checked as `fit validate` checks a config before anything is applied.
  fitstore's payload becomes version 2: the base config under `config`, and the console users whose
  passwords changed under `users`, a version 1 store still read; `admin.WithConsoleUsers`, `Configs`'
  console-user overrides, `login`'s `Users`, `HasUser`, `SetPassword` and `EndUserSessions`; the endpoints
  `GET /api/users`, `POST /api/account/password` and `POST /api/users/password`; `POST /config/render`,
  `POST /config/parse` and `PUT /config/text?format=&members=`, which merges a page's members into the
  config and applies it through the one path, with errors mapped to the text's line and column;
  `app/workbench/web`'s flat menu, account module and per-page tabs, and its forms' include checkboxes.
- **Done when**: every console user changes its own password (another user may reset it) in the pages and
  it holds across a restart, kept in the store, `409` without one; the menu lists modules flat in the
  browser, English and Chinese; a page's `.conf`, `.yaml` and `.json` tabs render its part, accept an
  edit and apply it, and refuse text that does not parse or does not load with its errors and line and
  column, applying nothing; the bundle's files are all loaded and the workbench's schema words stay
  whole; under the race detector.
- **Touches**: `admin`, `admin/store`, `login`, `cli`, `app/workbench`, `docs`.

### I121 Consistency between the running service and its config

- **Do**: a checker of what a running fit does against the config it runs from: the restart-only
  settings in force (`log`, `access_log`, `telemetry`, `engine`, `acme`, `providers`) and the engine
  limits read from the running engine; the roles configured against those running; the elements
  providers added to the effective config but not the base config; the listeners bound and their
  addresses; the certificates served against the listeners' `tls.acme` names; the upstream pools and
  their health; and the cache zones. Each difference has a stable ID, a category, a kind — `drift`,
  actionable, or `info`, a runtime fact that is not a disagreement — the config and runtime values, and
  the fixes offered: `add`, `delete` or `replace` in the base config, or `reapply`. `GET /config/check`
  answers with the running version, its ETag and the differences; `POST /config/reconcile` takes the
  chosen `{"fixes": [{"id", "action"}]}` with `If-Match`, rechecks under the apply lock, applies the
  patches through the one config path and answers as `PUT /config` does. The workbench's Consistency
  page shows each difference with its values and lets the user choose and confirm one fix per item,
  then applies the selection together and checks again; `fit check` does the same from a terminal
  through the Admin API, with `-json` and `-fix`, and the bootstrap file's `admin` addresses the API.
- **Done when**: a restart-only member or an engine limit changed after start is listed as drift with
  the in-force value and a revert fix, and applying it leaves the check clean; a role configured but not
  running, a provider element missing from the base config, a missing or extra listener or upstream
  pool, an uncovered `tls.acme` name and a missing cache zone are detected with their fixes; nothing is
  applied when a fix or the merged config fails to load; a stale `If-Match` is refused; the page works in
  a browser in English and Chinese and the CLI lists the differences and fixes them one by one, exiting
  0/1/2; under the race detector.
- **Touches**: `admin`, `cli`, `app/workbench`, `docs`.

### I122 The workbench's feature tabs and listener ports

- **Do**: put the config's `streaming`, `media_auth`, `webrtc`, `tunnel` and `llm` members on pages of
  their own in the flat menu (with a small tag naming each), out of Global and the "other" page, with the
  same UI/`.conf`/`.yaml`/`.json` editor and save-error links; and, on the Listeners page, list for every
  port the features that port serves — HTTP routes grouped by handler (reverse proxy, forward proxy,
  static, respond, FastCGI, media tokens, the streaming and WebRTC handlers), route middleware, layer 4
  services, SOCKS, and TLS with certificates/ACME or HTTPS by default — each linking to the page that
  edits it, with the address the kernel actually bound, and the ports HTTPS by default makes when the
  config writes none; a handler link opens the routes page filtered to that handler.
- **Done when**: the sidebar shows the feature entries after the protocol pages, in English and Chinese;
  the five pages edit their members (raw tabs included) and their save errors link to them; the Listeners
  page shows a card per configured (or generated) port with its features, counts and links, the bound
  addresses from `/api/status` (degrading when it fails), and the deep link lands on the filtered routes;
  no duplicate config pages remain under Global/Other; the bundle's tests pass and the pages work in a
  browser, and under the race detector where Go is touched (none here).
- **Touches**: `app/workbench`, `docs`.

### I123 The workbench's Engine menu

- **Do**: a first-level **Engine** entry in the workbench menu whose submenus are the listener ports
  (the ones the config writes, and those HTTPS by default makes when it writes none), each opening to
  the features it serves, in the user's order: its handlers (static, respond, FastCGI, media tokens,
  media pages — each linking to the routes page filtered to it, then any other handler module), its
  reverse-proxy and forward-proxy routes, its cache (zone page and routes page) and other middleware
  grouped, the streaming handlers (streaming page and each handler's filtered routes), the WebRTC
  handlers (WebRTC page and filtered routes), its layer 4 services, SOCKS, TLS certificates, ACME,
  HTTPS by default and the HTTP redirect, and last a link to the listener form; the port's line shows
  its address and its tags (PROXY protocol, HTTP/3, on-demand and client certificates), and the menu
  follows config changes. The workbench menu gains dynamic per-module navigation, third-level entries
  that link to other pages, a menu-only rebuild and a recursive filter; the Listeners page keeps the
  listener form and points at the Engine menu.
- **Done when**: Engine shows after Listeners in the menu, in English and Chinese; each port opens to
  its entries in the order above with the expected hrefs and counts, the port's protocols and feature
  tags on its line, bound addresses from `/api/status`, the HTTPS-by-default and no-listener cases, and
  active link highlighting; clicking an entry lands on the right page (filtered routes where the order
  says); the menu updates after a config save without losing the page; the Listeners page no longer
  draws the port cards; the bundle's tests pass and the mock/real-browser checks hold.
- **Touches**: `app/workbench`, `docs`.

## AI assistant in the workbench

Proposed and reviewed 2026-11-04. The workbench gains an assistant: a chat panel beside every page
that reads what the running fit does, explains it, and drafts config changes — into the page's own
editor and forms, or as a checked diff the user applies with a click — while submitting and saving
stay the person's. It is a client of model APIs the user sets (a hosted API with its key, a local
server, or this fit's own `llm.proxy`), never a model of its own. Decision 031 records the choices
below.

Principles, in the order they bind:

- **The model drafts; a person submits, applies and saves.** No tool writes the config, a version or the
  store: what the assistant makes is always a draft a person finishes. On a page it writes the change
  the user asked for into that page's editor or form fields as an unsaved draft (I154), and the person
  reviews it there and submits and saves it as any other page change. Elsewhere a tool that changes
  something adds to a proposal (a diff checked through the apply path); nothing is ever applied without
  a person: a logged-in user with the right level clicks Apply and confirms again in a dialog that
  shows the diff, and only then is the change applied, with `If-Match`, saved, and listed in
  `/versions` and in the audit log like any other change. There is no unattended mode.
- **The page is edited, never submitted.** The page tools change only what the open page holds in
  its editor and forms: they never call the config API (no `PUT`/`PATCH /config`, reconcile, rollback
  or purge), never save, and never change the version, the ETag or `/versions`, nor enter the audit
  log as a change; the page's **Save and apply** stays the person's click and goes through the page's
  ordinary apply path, recorded as that person's change. A change no open page can express is
  proposed instead (I126).
- **Nothing blocks a fib worker** (rule 1): model calls go out on fib's asynchronous HTTP client,
  streamed answers are parsed as they arrive, tools run on the console pool; every conversation, tool
  result, stream buffer and audit entry is bounded.
- **Secrets never leave**: model API keys are kept in the store, written once and shown masked, never
  sent to the browser; password hashes, keys, tokens and certificates' private parts are redacted from
  everything a tool hands the model; logs go only if the settings allow it.
- **Tool output is data, not instructions**: config text, logs and headers a tool returns are fenced
  as data in the prompt; nothing in them widens what the assistant may do.
- **Off by default**: no `admin.assistant`, no panel, no outgoing calls.

### Functions (the tool registry)

Every function is a tool with a name, a JSON schema, a level (`read`, `edit` or `propose`) and a cost
bound. `GET /api/m/ai/tools` lists them; the panel's Functions page shows them, each enabled or
disabled by the settings.

| Group | Tool | Level | What it does |
| --- | --- | --- | --- |
| Config | `config_get` | read | base or effective config, whole or by members, in `.conf`/`.yaml`/`.json`, redacted |
| Config | `schema_describe` | read | a member's schema, description, defaults and examples (English or Chinese) |
| Config | `config_validate` | read | text or a patch checked as `fit validate` checks it, errors with line and column |
| Config | `versions_diff` | read | the kept versions, and the diff between two of them |
| Runtime | `status_get` | read | listeners bound, roles running, engine limits, uptime |
| Runtime | `metrics_query` | read | counters and histograms by route, upstream, listener, over a bounded window |
| Runtime | `upstream_health` | read | pools, their members, health and recent failures |
| Runtime | `consistency_check` | read | `GET /config/check`'s differences and fixes |
| Runtime | `certs_list` | read | certificates served, names, issuers, expiry, ACME state |
| Runtime | `cache_zones` | read | zones, sizes, hit rates |
| Roles | `llm_nodes`, `tunnel_nodes` | read | the roles' node lists, links, latency and traffic |
| Logs | `log_tail` | read | fit's own log, from a new bounded in-memory ring, by logger and level, redacted |
| Logs | `access_log_sample` | read | recent access log lines matching a route, status or client, bounded |
| Page | `page_get` | read | the open page, the members and form fields it can edit, and its unsaved text |
| Page | `page_edit` | edit | a draft written into the open page's editor or form fields: a member set, added or removed; marked as unsaved, never submitted |
| Page | `page_revert` | edit | the assistant's drafts removed, restoring the page's text as it was before them |
| Change | `config_propose` | propose | a patch by members, rendered as a diff in the three formats, validated, waiting for a click |
| Change | `reconcile_propose` | propose | a chosen set of Consistency fixes |
| Change | `rollback_propose` | propose | a rollback to a kept version |
| Change | `cache_purge_propose` | propose | a purge by zone, key or prefix |
| Convert | `adapt_convert` | read | nginx or Caddyfile text through `fit adapt`, with what it could not convert |
| Agent | `skill_load` | read | an enabled skill's whole prompt, within the user's level |
| Agent | `ask_user` | read | a question with options, shown as buttons in the panel; the choice is the result |
| Media (I152) | `media_library_search` | read | a library's items searched and filtered (codec, resolution, probe errors), paged |
| Media (I152) | `media_item_get` | read | an item's tracks, codecs, renditions, subtitles, sidecar and probe error |
| Media (I152) | `channels_status` | read | each channel's now and next with its position, and its push targets' state, bytes and last error |
| Media (I152) | `playback_sessions` | read | live sessions by resource and address, counts per subject against `max_sessions`, 403s by reason; subjects shown hashed unless the settings allow them |
| Media (I152) | `media_scan_propose` | propose | a rescan of a library |
| Media (I152) | `media_session_end_propose` | propose | live playback sessions ended |
| Media (I152) | `media_key_rotate_propose` | propose | a rotation of one `media_auth` key or all, by name |
| Media (I152) | `media_file_propose` | propose | a rename, move, delete or sidecar edit of library files, with what plays them now |
| Cluster (I153) | `cluster_members` | read | the manager's members: health, versions, config version, drift, labels, groups |
| Cluster (I153) | `cluster_templates` | read | templates, their owned paths and variables, sync state per member |
| Cluster (I153) | `cluster_rollouts` | read | rollouts in progress and their history, batch by batch, with each member's result |
| Cluster (I153) | `cluster_metrics`, `cluster_log_tail`, `cluster_certs` | read | the manager's series per member and group, logs tailed across members, the certificate inventory |
| Cluster (I153) | `member` argument | read | every read tool above, as `config_get` or `consistency_check`, run on a chosen member through the manager's session |
| Cluster (I153) | `rollout_propose` | propose | a change to a template or to a group's members as a rollout plan: per-member diffs, dry runs, strategy and health gates |
| Cluster (I153) | `cluster_op_propose` | propose | an operation on many members (reload, rollback, check, purge) |
| Cluster (I153) | `drift_fix_propose` | propose | members' drift against their templates fixed by a rollout |

Out of reach of every tool: `admin.console` users and passwords, the store's key, `admin.assistant`
itself, the Admin API's own addresses and tokens, and any file outside the config; `media_auth`'s
secrets, playback URLs and their signatures (a test URL is issued only in the form of I138), and
uploads; a cluster's member credentials, enrollment tokens, and adding or removing members. The page
tools reach only the open page's editor and the members and fields that page edits, and cannot write
into its secret fields; a draft is redacted and bounded as any other tool result.

### Skills

A skill is a named task: a description saying when to use it, a prompt, the subset of tools it may
call (never wider than the user's level), optional inputs and an optional model. Built-in skills are
embedded in the bundle; users add their own on the Skills page (stored in the store), import and export
them as Markdown with front matter, and enable or disable any. In the chat, `/name` runs one; without a
name the assistant picks from the enabled skills' descriptions.

| Skill | What it does | Tools |
| --- | --- | --- |
| `explain` | explains the current page, a member or a value, with links to the docs | config, schema |
| `route-builder` | a route, its matcher, handler and middleware from a sentence | config, change |
| `reverse-proxy` | an upstream pool with health checks, load balancing, timeouts and retries | config, runtime, change |
| `tls-acme` | certificates by ACME or files, HTTPS by default, redirects, client certificates | config, certs, change |
| `rate-limit-auth` | rate limits, IP lists, basic, JWT or forward auth on routes | config, change |
| `cache-tuning` | cache zones and rules, from hit rates and the routes' traffic | cache, metrics, change |
| `diagnose` | why a route answers 502/504 or is slow: health, metrics and logs read together | runtime, logs |
| `drift-explain` | each Consistency difference explained, a fix recommended | consistency, change |
| `log-analysis` | errors and anomalies in fit's log and the access log, grouped and explained | logs |
| `security-review` | admin exposure, TLS settings, missing limits and bodies without bounds, as findings with fixes | config, status, change |
| `migrate` | an nginx, Caddy or Traefik config to fit: the converter first, the model for what it left | convert, config, change |
| `media-setup` | streaming, media auth, WebRTC and media pages from a description of the service | config, change |
| `llm-mesh-setup` | LLM hub, node and proxy roles, and their models, from the machines described | config, roles, change |
| `tunnel-setup` | tunnel hub, node and proxy for services behind NAT | config, roles, change |
| `vod-setup` (I152) | a library, its `vod` routes and `url_signing` for a site's titles, from a description of the site | config, media, change |
| `channel-scheduler` (I152) | a channel's list or schedule from items described in words, and its push targets | media, config, change |
| `library-health` (I152) | probe errors, missing renditions or subtitles, codecs players cannot play, and the ffmpeg steps of `scripts/` to prepare them | media |
| `playback-troubleshoot` (I152) | why a viewer is refused or stalls: 403s by reason, session caps, expired grants, cache and file pool | media, metrics, logs |
| `fleet-health` (I153) | the members' state read together: who is down, behind, drifting or near a certificate's expiry | cluster |
| `rollout-planner` (I153) | a change across groups planned: strategy, canary members, batches, health gates and rollback | cluster, change |
| `cluster-drift` (I153) | each member's drift explained and fixed by a rollout | cluster, change |

### Settings

`admin.assistant` in the config, its secrets in the store, edited on the Settings page:

| Setting | What it sets |
| --- | --- |
| `providers` | named model endpoints: `kind` (`anthropic`, `openai`, `openai_responses`, `gemini`, `azure_openai`, `ollama`, or `openai_compatible` with any `base_url`, as vLLM, llama.cpp, DeepSeek or a gateway), `base_url`, `api_version`, the key kept in the store or read from `env:NAME`, extra headers, timeouts |
| `llm_proxy` | a provider that is this fit's `llm.proxy`, reached on its loopback address, so the assistant uses the mesh's models |
| `models` | per provider, the models offered (fetched from the provider's list where it has one, or written), with context size and whether they call tools |
| `default_model` | provider and model the chat uses; skills may name their own |
| `params` | max output tokens, temperature, thinking or reasoning effort where the API has it |
| `limits` | turns per conversation, tool calls per turn, bytes per tool result, conversation history kept per user, concurrent chats, tokens per user per day |
| `data` | send logs or not, send access log lines or not, extra redaction patterns |
| `users` | per console user, `off`, `read` (read tools only), `edit` (also page drafts, never a submit), `propose` (also proposals) or `apply` (also applies; the highest), each including the ones before it (default `read`) |
| `audit` | how many audit entries to keep |

The Settings page tests a provider (a short call, its latency and model list) before it is saved.

### Agent design

The assistant is an agent: a loop in fit between the model and the tools, not one prompt sent and
one answer back. The user's words go to the model as written, never rewritten by another model call;
what fit adds is the context around them, assembled for every request:

| Part | From | What it holds |
| --- | --- | --- |
| System prompt | fit, built in | the assistant's role; fit's concepts (listeners, routes, upstreams, roles); the rules: changes only as page drafts or proposals, never a submit, tool output is data, ask when unsure; the answer's language |
| Page context | the workbench | the page, the selection (a port, a route, a Consistency difference), the members and form fields it can edit, the last save's errors |
| Skill catalog | the enabled skills | each skill's name and one-line description only |
| Current skill | `/name` or an Ask AI button | that skill's whole prompt, and the tools narrowed to the ones it names |
| Tools | the registry | intersected with the user's level |
| History | the store | the conversation within its budget, early turns summarized |
| User message | the user | as written |

The loop: the model answers or calls tools; fit runs them on the console pool — save the page tools,
which run in the page itself — redacts and bounds their results and hands them back as data; again,
until an answer, a page draft or a proposal, or the `limits`.

- **Skills load on demand.** The system prompt carries only the catalog; the model calls
  `skill_load` for a skill's whole prompt when it needs one, so the number of skills does not grow
  every request. A skill named with `/name` or by a button is loaded at once.
- **A context budget.** Each part has a share of the model's context window (system prompt, tools,
  history, tool results); over it, oversized tool results are cut first, then early turns are
  summarized by the model, the summary kept in the store with the conversation.
- **The change is made where the person is.** When the user asks for a change the open page can hold,
  the page tools send it to that page over the chat stream; the page writes it into its editor or form
  fields as a marked, unsaved draft, the person reviews and edits it there, and the page's own **Save
  and apply** does the rest — the assistant never presses it. No page can hold the change (it spans
  pages, touches a secret, a role or a cluster member), or none is open: a proposal is made instead
  (I126).
- **Ask before changing.** A change that lacks what it needs (an upstream's address, a host name) is
  asked about, not guessed: the `ask_user` tool shows the question with options as buttons in the
  panel, and the user's choice comes back as the tool's result.

The iterations, I124–I130 and I152–I154, are in the [index](#index).

### I124 The assistant's settings and model client

- **Do**: decision 031. `admin.assistant` in the schema and the config, its keys in the store (fitstore
  payload version 3, version 2 still read), shown masked and written only. A model client in a new
  package `assistant/model`: for each `kind`, the request with system prompt, messages and tools, the
  streamed answer (SSE, or NDJSON for Ollama) parsed with bounded buffers into text, thinking and tool
  calls, the usage counted; timeouts, a cancel when the browser goes away, retries only before the first
  byte; the `llm_proxy` provider. The workbench's `ai` module with the Settings page, a provider test and
  the model list, English and Chinese. `GET/PUT /api/m/ai/settings`, `POST /api/m/ai/providers/test`.
- **Done when**: each kind is called against a recorded fake of its API and, in `test/docker`, against
  Ollama and an OpenAI-compatible server, a tool call round-tripping; a key never appears in a response,
  the page or a log; a slow, cut or oversized stream is bounded and failed; under the race detector.
- **Touches**: `assistant` (new), `admin`, `admin/store`, `config`, `schema`, `app/workbench`, `docs`.

**Decisions pending review** (made while building it, 2026-11-06; each stands until the user rules on it):

1. **Where the settings live.** `admin.assistant` gives the first settings; once the Settings page
   saves, the store's section `assistant` is used, as the store's config is. All of `admin` changes
   only at a restart and stays out of the store, so the page cannot save into it. Alternatives: an
   override layer of its own; no editing in the page.
2. **Without a store nothing is saved**: `PUT` answers 409; the page still reads and tests, and keys
   come from `api_key_env`, so a key typed in the page never lands in a plain file.
3. **Version 3 only with a section**: a store without one is still written as version 2, so older
   fits open it. Alternative: always version 3.
4. **Keys are not exported**: an export carries the config and the users only. Alternative: include
   them, as exports are encrypted.
5. **A masked key shows its last four characters**, only for keys of 16 or more. Alternative: "set".
6. **No key over plain http off loopback**, as for the console and the Admin API. Alternative: allow
   LAN addresses (a vLLM on the LAN).
7. **`llm_proxy` is a provider kind** rather than a member of its own as the plan's table has it:
   reached on loopback with one of the proxy's API keys, its certificate pinned, not checked
   against CAs.
8. **Retries** on transport errors and retryable statuses (408, 429, 500, 502–504, 529, with
   Retry-After up to 10s) before any answer; a first-byte timeout is never retried.
9. **Each API's details**: Gemini `parametersJsonSchema`; Chat Completions
   `stream_options.include_usage`; `max_completion_tokens` and `reasoning_effort` for openai and
   azure, `max_tokens` for the rest; Anthropic thinking budgets 2048/8192/24576 added to
   `max_tokens`, no temperature with thinking; Responses with `store: false`.
10. **Every limit of the plan's table is in the schema now**, with defaults; only `max_stream_bytes`
    is enforced in I124, the rest by the iterations that use them.
11. **`users` is a list** of `{username, level}`, each a console user.
12. **Lost updates**: a counter of saves in this process answers 412 to a stale page. Alternative: an
    ETag from a hash.
13. **Provider tests**: at most 4 at once, 15s for the model list and 30s for the call, at most 32
    tokens, no retries; `models_only` reads the list alone.
14. **Model connections**: on the listeners' engine, at most 16 fib clients (one per connect timeout
    and pinned certificate); a 17th retires the oldest, closed once its calls end.

### I125 The chat panel and the read tools

- **Do**: the agent loop and its context as in [Agent design](#agent-design): the system prompt, the page
  context, the skill catalog, the budget with cut tool results and summarized turns, and `ask_user`;
  the tool registry with the read tools above, each with its schema, level and bounds, run on the
  console pool against the Admin API in process; redaction of secrets from every result; a bounded ring of
  fit's own log for `log_tail`. `POST /api/m/ai/chat` streaming events (text, tool call, tool result,
  usage, done) as SSE, the loop bounded by `limits`; conversations kept per user in the store (encrypted,
  across restarts), bounded in number and size, listed and deleted. The panel: open from every page, the current page and selection sent as context, streamed
  Markdown, tool calls shown folded, stop, retry, new conversation; the Functions page.
- **Done when**: a question about a route, an upstream's health or a log error is answered from the tools,
  in English and Chinese; no secret reaches a recorded request to the model; a tool result over its bound
  is cut and says so; a long conversation stays within its model's window, its early turns summarized;
  an `ask_user` question shows as buttons and its choice reaches the model; the user's words reach the
  model unchanged; the loop stops at its limits; two users' conversations stay apart and hold across a
  restart within their bounds; under the race
  detector, and the panel works in a browser.
- **Touches**: `assistant`, `admin`, `admin/store`, `observe`, `app/workbench`, `docs`.

**Decisions pending review** (made while building it, 2026-11-06; each stands until the user rules on it):

1. **Where tools run.** The console pool's request helper answers 504 and would break a stream, so
   `console.Request.Submit` runs work on the pool without holding the request, 30s a tool.
   Alternative: a pool of the assistant's own.
2. **Redaction** happens once, on every result in the loop, plus by structure inside the JSON tools:
   members named like secrets (password, secret, token, key, hash; not `*_file`, `*_env`, `max_*`)
   and secrets in text by pattern. `admin.assistant`, `admin.console.users`, `admin.store`,
   `admin.address` and `admin.token_file` are left out entirely. Alternative: listed paths only.
3. **`access_log_sample`** reads the last 512 KiB of the access log's file, not a ring, which would
   add a copy on the request path.
4. **At `max_turns`** a new message is refused (409, start a new conversation), not folded silently.
5. **At `max_tool_calls`** further calls get error results and the answer ends with reason
   `max_tool_calls`; no closing call without tools, which Anthropic refuses after tool calls.
6. **Tokens a day** are counted per user in their conversations' section, reset at UTC midnight.
7. **A conversation's size**: constants, 512 KiB a conversation and 8 MiB a user; past them,
   summarized messages go first, then the earliest turns. Alternative: settings of their own.
8. **Without a store** conversations are kept in memory until a restart, and the panel says so.
9. **The context budget**: about 3 bytes a token; 32,768 tokens when a model's `context` is unset; the
   answer's share is `max_output_tokens`, at most a quarter; the summary is tried once a chat, and if
   it fails the history is cut harder.
10. **The page context** goes in the system prompt, redacted and at most 16 KiB, so the user's words
    reach the model unchanged.
11. **The last event** is written after the conversation and the chat's slot are freed, so a message
    sent at once is not refused.
12. **A slow browser**: events are written without waiting, within the limits; one that takes nothing
    for 60s has its stream cut, which stops the chat. Alternative: pause the model's stream.
13. **Stop**: the browser aborts its fetch and calls `POST /api/m/ai/chat/stop`; the partial answer is
    kept, marked stopped, and calls left without results get error results.
14. **Languages**: the model gets English tool descriptions; the Functions page shows them in the
    workbench's language.

### I126 Proposals, levels and the audit log

- **Do**: the propose tools: the model may call several in a row, and each adds a step to one pending
  proposal per conversation, which holds the steps, the base version's ETag, the combined diff in
  `.conf`, `.yaml` and `.json`, and the validation of the whole; the panel shows it with each step,
  Apply and Discard. Apply opens a confirmation dialog with the whole diff, and only its confirm runs
  the steps as the user with `If-Match` — the config steps merged and applied as one change through
  `PUT /config/text` (a reconcile and a rollback as their own endpoints), then saved, a cache purge
  after them — all or nothing for the config; a stale proposal is refused and offered again on the
  new version. Per-user levels
  from `admin.assistant.users`, checked on every tool and every apply. The audit log in the store: user,
  time, conversation, skill, tool calls, proposal, outcome and version; `GET /api/m/ai/audit` and its page.
- **Done when**: a change asked in words becomes a valid proposal and, applied, a new version that
  `/versions` lists; several propose calls make one proposal applied as one version, and one failing
  step applies none of them; nothing is applied without the click and the dialog's confirm, and
  cancelling the dialog leaves the config and the store unchanged; a `read` or `edit` user gets no
  propose tools, a `propose` user cannot apply; a proposal that fails validation cannot be applied; the
  forbidden members cannot be proposed; the audit log holds across a restart within its bound; under
  the race detector.
- **Touches**: `assistant`, `admin`, `admin/store`, `app/workbench`, `docs`.

**Decisions pending review** (made while building it, 2026-11-06; each stands until the user rules on it):

1. **Who applies.** Only level `apply`; an `apply` user lists and applies or discards any user's
   pending proposals, the proposer recorded as `owner` in the audit log, since a `propose` user's
   could otherwise never be applied. Alternative: each user's proposals private.
2. **One kind of config step a proposal**: patches, fixes or one rollback, each through its own
   endpoint as exactly one version, since mixing them takes two calls and cannot be all or nothing;
   purges mix with any. Alternative: fold fixes and rollbacks into one merged `PUT /config/text`.
3. **No proposal touches `admin`**: it holds the forbidden members, `PUT /config/text?members`
   refuses it, and it takes effect only at a restart; a rollback to a version whose `admin` differs
   is refused too.
4. **The assistant never writes a secret**: a value under a secret-named member must equal the
   config's at that path (a `[redacted]` copied back is restored) or be empty; the person sets new
   secrets on the page. Alternative: allow it, flagged in the diff.
5. **Patches go by top-level member** (`PUT /config/text?members=` with only the changed ones), the
   rest of the config kept as written, member order kept with an order-preserving JSON tree.
6. **A step that leaves the whole invalid is kept** for the next to mend; one wrong in itself is
   refused.
7. **A proposal of purges only is never stale**; its zones are checked again at the apply.
8. **Diffs are made again at each view**, 256 KiB a format; a proposal holds at most 32 steps and
   256 KiB of arguments.
9. **Diffs are redacted everywhere**, in the panel and the dialog as for the model.
10. **No apply while the conversation answers** (409); apply and discard commit only if the revision
    is unchanged.
11. **The audit log** records answers that called tools, not plain questions, so the bound is not
    spent on chat; every console user may read it, as the console's log; a cancelled dialog sends
    nothing and is not logged.
12. **Discard** needs any level but `off`; deleting a conversation with a pending proposal is logged
    as a discard.
13. **An apply runs on a context of its own**, so a browser that leaves cannot half apply it.

### I127 Skills

- **Do**: the skill format (name, title, description, inputs, tools, model, prompt, examples) as Markdown
  with front matter; the built-in skills above, embedded; user skills kept in the store, bounded in number
  and size; the Skills page to list, view, add, edit, enable, disable, import and export them; `/name` and
  a skill picker in the panel; the catalog in the system prompt and `skill_load` for a skill's whole
  prompt; a skill's tools intersected with the user's level.
- **Done when**: each built-in skill runs its task on a fixed example config against a recorded model; a
  user skill added on the page runs and survives a restart; a skill not named is picked from the catalog and
  loaded with `skill_load`, and the prompt sent without it carries only the catalog; a skill naming a
  tool above the user's level cannot call it; bad front matter is refused with its line; English and Chinese; under the race detector.
- **Touches**: `assistant`, `admin/store`, `app/workbench`, `docs`.

**Decisions pending review** (made while building it, 2026-11-06; each stands until the user rules on it):

1. **Who changes skills**: level `apply`; everyone else reads and runs them, as skills change the
   assistant for every user. Alternatives: `propose`; skills private to each user.
2. **One set for the console**, shared by all users, as team skills and I130's MCP prompts want.
   Alternative: a section a user.
3. **Bounds are constants**: 64 user skills of 32 KiB each, as I125's conversation sizes.
   Alternative: `limits.max_skills`.
4. **A built-in's name is refused** to a user skill; to change a built-in, export it, rename it, add
   it and disable the original. Alternative: a user skill overrides a built-in.
5. **`skill_load` makes the skill the conversation's**: the tools narrow and `skill_load` leaves
   them; later turns keep the skill until it is cleared. Alternative: load the prompt only.
6. **The prompt is sent once**: while a skill is the conversation's, its prompt is in the system
   prompt, marked as fit's own instructions that never override the rules, and earlier `skill_load`
   results are replaced in the request by a short note (stored and shown whole).
7. **The catalog** goes only when `skill_load` is offered and no skill is the conversation's, at most a
   tenth of the window, names only past it.
8. **The catalog lists every enabled skill** whatever the user's level; the tools narrow when it
   runs. Alternative: hide skills whose tools are all above the level.
9. **`ask_user`** is always a skill's, whatever it names.
10. **A tool a skill names that this fit lacks** is accepted and shown as such, so skills written for
    later tools load; built-ins are tested to name only registered ones.
11. **A skill's model**: `provider/model` when the provider exists, else a model of the default
    provider; it applies from the message that starts the chat.
12. **`/name`** is a skill only when it names an enabled one (`/etc/hosts …` goes as written); the API
    refuses `/name` alone (400), the panel just sets the chip.
13. **Inputs** go into the system prompt as redacted `<skill_inputs>` data, never into the user's
    words, 2 KiB each, kept with the conversation; missing ones are listed for the model.
14. **A skill disabled or deleted** leaves its conversation at the next message.
15. **The panel's Markdown** joins wrapped list items and nests indented ones, as the skills' prompts
    need.

### I128 Ask AI where the work is

- **Do**: an Ask AI button that opens the panel with a skill and context filled in, on a save's errors
  (`explain` with the error and the text), each Consistency difference (`drift-explain`), each Engine port
  and route (`diagnose`), each form field (`explain` with its schema path), a metrics spike or an
  upstream down (`diagnose`), and the LLM and tunnel link maps.
- **Done when**: each entry opens the panel with the right skill and context and its first answer uses
  them; the buttons are absent when the assistant is off or the user's level is `off`; English and
  Chinese; the bundle's tests and a browser check pass.
- **Touches**: `app/workbench`, `docs`.

**Decisions pending review** (made while building it, 2026-11-06; each stands until the user rules on it):

1. **Sent at once**: a click sends the question, shown as the user's message, so one click gives an
   answer about the thing. Alternative: prefill and wait for Enter, a model call fewer a click.
2. **The link maps** use `diagnose`, on the selected box or line or else the whole map, the map as
   drawn sent as the selection. Alternatives: `explain`; `llm-mesh-setup` or `tunnel-setup`, which
   propose config.
3. **A metrics spike** is an error counter rising between two Overview refreshes (failed requests
   an upstream, refused connections, failed reloads), listed for 60s, as fit keeps no metric
   history. Alternatives: thresholds; a metrics page with history.
4. **An Engine route** is each port entry that lists routes, and each route on the Routes page, as
   the Engine menu has no rows of single routes.
5. **Consistency info rows** get a button too, with a question of their own, still `drift-explain`.
6. **A difference's values are not sent**: `drift-explain` reads them, redacted, with
   `consistency_check`.
7. **A save error's text**: about four numbered lines each side of each problem's line, lines that set
   secrets redacted on the page as by the server, or the draft's values at the problems' paths for a
   forms save; at most 20 problems. Alternative: the whole text, cut.
8. **A form field's input `path`** is its schema path (indexes and keys dropped); the indexed path goes
   in the selection.
9. **The context checkbox**: an Ask AI click turns the panel's "send this page" back on, so the
   selection goes with the question.
10. **The tunnel's path dialog** closes on Ask AI, so the panel shows.
11. **Icons or labels**: icons on form fields, route and upstream chips and menu rows (named on hover
    or focus); a labelled "Ask AI" on callouts, Consistency rows and map cards.
12. **The page's bounds**: strings 1,500 characters, lists 40 items, the selection 8 KiB, under the
    server's 16 KiB page context.

### I129 Migration and review

- **Do**: `migrate`: nginx and Caddyfile text through `fit adapt` first, the model converting what the
  converter reported it could not, Traefik's YAML or labels by the model alone, each result a proposal
  with the items it could not map listed; `security-review`: findings with severity, member and a
  proposed fix. A fixed set of input configs with the expected outcome, run against a chosen model on
  demand, reporting how many convert to a config that validates and keeps the routes.
- **Done when**: the set's configs convert and validate, their unconverted items named; the review
  finds the planted issues of its set; the set runs from `make assistant-eval` with a provider set in the
  environment, never in CI by default.
- **Touches**: `assistant`, `adapter`, `cli`, `docs`.

**Decisions pending review** (made while building it, 2026-11-06; each stands until the user rules on it):

1. **The evaluator** is test code in package `assistant` (`eval_test.go` and its helpers), run by
   `make assistant-eval` as `go test -run '^TestAssistantEval$'`, reusing the console's and the Admin
   API's fixtures and adding no untested code. Alternatives: a CLI command; a package of its own.
2. **No CLI command**, though the plan's Touches name `cli`: the plan names only `make
   assistant-eval`, and `fit adapt` already prints its warnings with their lines. Alternative: `fit
   assistant eval`, for users to test their own model.
3. **Unconverted items** are an optional `unconverted` list on `config_propose` (100 items of 500
   bytes), kept with the step and shown with the proposal. Alternative: in the answer's text only.
4. **Findings** are lines of a fixed format (severity, member, fix), parsed leniently (numbered,
   bracketed, Chinese). Alternative: a findings tool or event.
5. **"Keeps the routes"** means: apply the proposal, compile it, send each case's requests through the
   real `Router.Match`, and check the upstream, handler, redirect, refusal or text. Alternative:
   compare JSON shapes.
6. **Files the config names that are not here**: in the eval only, a missing `*_file` stands for a
   generated certificate and a static `root` for a temporary directory.
7. **`ask_user` in a run** is answered with its first option, at most three times.
8. **A live run exits non-zero** when a case fails, the report still printed.
9. **`config_get` of an unset member** answers `null` (a comment in `.conf` and YAML).
10. **A selector is not a secret**: only a member named `key` whose value is `ip`, `route` or
    `header:<token>`; `hash_key` and every other `key` stay secret.
11. **nginx's `auth_basic` fails closed**: its routes answer 401 until a `basic_auth` with users
    replaces it; `return` locations still answer first, and `auth_basic off` reopens one.
    Alternative: warn only, leaving the content public as before.
12. **Lua in nginx**: `*_by_lua_block` blocks are skipped and reported with their line; braces in Lua
    strings, comments and long brackets are skipped. Alternative: refuse configs with Lua.
13. **The live run's settings**: level `apply`, 40 tool calls, 64 KiB a tool result, 16,384 output
    tokens, so a whole config fits one proposal.
14. **`llm_proxy` is refused as `FIT_EVAL_KIND`**, pointing to `openai_compatible` at the proxy.

### I130 An MCP server on the tool registry

- **Do**: `admin.assistant.mcp`, off by default: an MCP server (Streamable HTTP, POST and SSE) at
  `/mcp` on the console's binding, so that outside agents (Claude Code, Cursor, an SDK of one's own) use
  what the panel uses. The registry's tools are its tools, the enabled skills its prompts, and the base
  and effective config, the schema, the versions and the docs its resources (`fit://config/effective`,
  `fit://schema/{path}`, `fit://versions/{n}`), all redacted as for the model. Per-user MCP tokens made on
  the workbench's MCP page: a name, an expiry and an optional address list, shown once, kept hashed in
  the store, revoked there; a token carries its user's level, capped at `propose`. A propose tool adds to
  the token's pending proposal and answers with its ID and the workbench link; there is no apply tool —
  the proposal is applied only in the workbench, with the click and the dialog's confirm. Sessions,
  request bytes, concurrent calls and calls per minute bounded per token; the allowed addresses
  (loopback by default); every call in the audit log with source `mcp` and the token's name. fit's own
  assistant does not call outside MCP servers.
- **Done when**: an MCP client (the official SDK's in a test, and Claude Code by hand) lists the tools,
  prompts and resources, reads the config and status, and makes a proposal that shows in the workbench
  and applies only after the confirm; a `read` token gets no propose tools; an expired, revoked or wrong
  token, or a caller off the address list, is refused; no secret appears in a tool result or resource;
  the bounds hold against a slow client, a large request and a cut stream; English and Chinese; under the
  race detector.
- **Touches**: `assistant`, `admin`, `admin/store`, `app/console`, `app/workbench`, `schema`, `docs`.

**Decisions pending review** (made while building it, 2026-11-06; each stands until the user rules on it):

1. **Protocol revisions**: 2026-07-28 and 2025-03-26 to 2025-11-25 on one endpoint, as the newest
   revision changed the transport and clients such as Claude Code may still open sessions.
   Alternatives: the newest only; the older ones only.
2. **Where the settings live**: `mcp` is a member of the assistant's settings, in the store once the
   Settings page saves, edited through `PUT /api/m/ai/settings`. Alternative: a member read at a
   restart only.
3. **Token levels**: `read` or `propose`; `edit` adds only page tools, which are not served. A
   `propose` token needs a user at `propose` or `apply`, the user's level applied again at each call.
4. **Token storage**: a 256-bit secret kept as a SHA-256 (no bcrypt, as the secret has full entropy),
   found by ID, compared in constant time; without a store, until a restart; tokens expired over 7
   days are dropped at the next write. Alternatives: bcrypt; no tokens without a store.
5. **Who sees whose tokens**: one's own; an `apply` user sees and revokes everyone's. Tokens may be
   made while MCP is off.
6. **Addresses**: a token's list narrows the server's `allow`, never widens it; a caller off the list
   gets 403 before its token is checked.
7. **A session past the limit** ends the one used least recently, as clients rarely send DELETE; idle
   ones end after `session_idle_timeout`.
8. **Requests at once** past the limit answer 429, not a queue; a token's propose calls run one at a
   time, its reads do not.
9. **The proposal's conversation**: one a token, `MCP: <name>`, made again if dropped; one busy in the
   panel refuses MCP propose calls.
10. **Audit**: a new kind `tool` for each `tools/call`, `resources/read` and `prompts/get`, outcome `ok`
    or `error`; list calls are not logged; one bound with the rest.
11. **Answers**: SSE for `tools/call` only, JSON for the rest; batches refused (400); unknown methods
    404 in the new revision and 200 in the old, as each says; GET 405.
12. **Caching**: lists carry `ttlMs: 0` and `cacheScope: "private"`; no `listChanged`.
13. **A resource** is at most 1 MiB, cut with a note.
14. **Defaults**: 10 tokens a user, 90 days at most (3650 the ceiling), 4 sessions, 1h idle, 1 MiB a
    request, 4 at once, 120 a minute.
15. **Docs as resources**: the generated config reference, whole and by chapter (English only); other
    docs are not in the binary.
16. **The SDK's test** is a module of its own (`test/mcpsdk`) that builds fit with `go build -C ../..`
    and never imports it, so fit's dependencies do not grow; its own CI job runs it.
17. **A clock stepping back** costs the per-minute counter no budget.

### I152 The assistant's media library tools and skills

- **Do**: the Media tools and skills above, on the library, `vod`, channels and `url_signing` of
  I132–I136, the propose tools applied as every proposal is (I126), with the Apply click and the
  dialog's confirm; a file proposal warns when a channel uses the file or a session plays it.
  Ask AI on the media library pages (I138): an item's probe error (`library-health`), a channel's
  push target in error (`channel-scheduler`), the 403s by reason (`playback-troubleshoot`).
- **Done when**: each skill does its task on a fixed library against a recorded model; no key,
  signature or playback URL reaches a recorded request; subjects reach it hashed unless allowed;
  a delete proposal for a file a channel plays names the channel; under the race detector.
- **Touches**: `assistant`, `admin`, `medialib`, `app/workbench`, `docs`.

**Decisions pending review** (made while building it, 2026-11-06; each stands until the user rules on it):

1. **Media steps are actions** that join a config change of any kind, as purges do, applied after the
   config in order and before the purges, a failing one making the outcome `partial`. Alternative: a
   proposal of media steps only, which would block "rename the file and change the channel's list" in
   one change.
2. **What was shown is confirmed** by `media_tag`, a digest of in use, the channels listing or playing
   the item, and whether viewers play it; a mismatch answers 412 and offers the proposal again, and
   `force` goes only for what was shown in use. The tag leaves out a channel's now and next and the
   viewers' count, which change every second. Alternative: always force.
3. **`admin.assistant.data.send_subjects`**, off by default, beside `send_logs` and `send_access_log`;
   otherwise subjects go as `subject-` and 16 hex digits of an HMAC under a key made at each start,
   stable while fit runs; the propose tools take either form. Alternative: a key kept in the store, so
   that hashes outlive a restart (sessions do not).
4. **Addresses and session IDs go as they are**, as the plan lists sessions by resource and address.
   Alternative: hash addresses too.
5. **A push target's `url` is a secret** in `config_get`, proposals and page drafts: `config_get` passed
   the stream key to the model before.
6. **MCP serves the media tools** within a token's level; proposals are applied only in the workbench.
7. **Sessions ended or gone** before an apply are dropped from a session-end step with a warning; the
   step is refused only when none still plays.
8. **Key rotation** only for keys in a `secret_file`; a key written in the config is refused, for the
   person to change; no skill rotates keys.
9. **The skills' tools**: `library-health` may propose rescans and file changes but is told never to
   delete unless asked; `playback-troubleshoot` may propose ending sessions; `vod-setup` and
   `channel-scheduler` have the page tools too.
10. **`library-health` carries the ffmpeg commands itself**, as `scripts/` has no preparation script.
11. **`TestChatSummarizes`'s window** grew from 3,600 to 5,600 tokens with the tools and prompt.

### I153 The assistant across a cluster

- **Do**: on a manager (I141), the Cluster tools and skills above; a read tool's `member` argument
  goes through the manager's session to that member's API; a propose tool becomes a rollout plan
  or an operation, shown on the Rollouts page and started only with its own confirm (I146). The
  assistant's level and the manager's role per group (I148) are both checked, the narrower wins.
  Ask AI on the Cluster pages: an Overview card (`fleet-health`), a failed batch (`rollout-planner`),
  a drift item (`cluster-drift`). The MCP server (I130) serves these tools on a manager too.
- **Done when**: a change asked in words for a group becomes a rollout plan with each member's
  diff and dry run, and runs only after the confirm; an `operator` cannot propose a change to a
  template it may not edit; no member credential reaches a recorded request; a read across
  members stays within its bounds when a member is slow or down; under the race detector.
- **Touches**: `assistant`, `cluster`, `app/workbench`, `docs`.

### I154 Edits on the page, submitted by a person

- **Do**: the `edit` level in `admin.assistant.users` (I124's settings page offers it), and the page
  tools above. The panel sends each page tool call to the open page over the chat stream, and the
  page's answer comes back as the next event, the wait bounded by a timeout and ended when the
  browser goes away. The page applies the call to its text editor (the `.conf`/`.yaml`/`.json` tabs
  of I120) or to the form field of a schema path (the feature pages of I122 and I123), marks what the
  assistant drafted, and returns the result; a page declares the members and fields it can edit, so
  that the assistant knows which changes the page can hold, and the page's own check of the draft
  (parse, the tab's format) runs on it as on a manual edit. The assistant has no submit or save
  path: the page tools never call `PUT`/`PATCH /config`, `/config/reconcile`, a rollback or a purge,
  and the version, the ETag and `/versions` stay as they were until the person clicks the page's
  **Save and apply**, which goes through the page's ordinary apply path and is recorded as that
  person's change. Drafts are redacted and bounded as tool results are; a page-edit call is in the
  audit log as a tool call, never as a change. The page tools are not served over MCP (I130), which
  has no open page to act on. A change no page can express, or no open page, means a proposal (I126).
- **Done when**: asked on a page to change a value, the assistant writes it into that page's editor
  or form, marked as its unsaved draft; no config request is made, and the version, the ETag and
  `/versions` are unchanged until the person saves; the person's **Save and apply** then submits and
  saves a version as any other page change, and the assistant made no request for it; a draft can be
  reverted and the page returns to the text it held before it; a change no page can express becomes a
  proposal; a `read` user gets no page tools, an `edit` user does; a page edit that touches a secret
  field or a forbidden member is refused; a draft over its bound is refused and says so; English and
  Chinese; a browser check passes; under the race detector.
- **Touches**: `assistant`, `app/workbench`, `admin`, `docs`.

**Decisions pending review** (made while building it, 2026-11-06; each stands until the user rules on it):

1. **How the page answers**: `POST /api/m/ai/chat/page` while the SSE stream stays open, so the
   answer comes back as the call's result and ends when the browser leaves. Alternative: end the
   stream and resume with a new request, as `ask_user` does.
2. **When page tools are offered**: only while the page context declares `editable.members`, so
   unticking "send this page as context" removes them too. Alternative: always, the page refusing.
3. **`page_get` is level `read`**, as the plan's table says; a read user is told it cannot draft.
4. **The draft's format** is `config_propose`'s (`members`, JSON-pointer `ops`), or `text` with its
   `format`, so the model learns one. Alternative: dotted schema paths.
5. **Checked twice**: by fit (members, no `admin`, no secret, parses, 256 KiB) and by the page
   against its draft and its `/config/parse`; a text that fails fit's parse never reaches the editor.
6. **Secrets**: under a secret-named member only a copied-back `[redacted]`, which the page restores;
   dropping a secret field from an object that remains is refused; any `[redacted]` is put back as
   the draft holds it, or refused. Alternative: allow empty values or removal, as I126 allows empty.
7. **`add` creates missing parents** on the page, unlike `config_propose`'s ops, since drafts often add
   into a member the page lacks.
8. **Revert** restores a snapshot from before the assistant's first draft, raw texts included; after
   a person's edit the assistant's `page_revert` is refused and the page's Revert asks first.
9. **Text drafts** stay in their tab as written, unless they held `[redacted]`, when the tab is
   rendered again from the draft so the saved text carries the real secret.
10. **One draft for all pages**: the note, badge and Revert show on every editor page while an
    assistant draft exists, as the config pages share one draft.
11. **Skills**: five change skills name the page tools, `explain` names `page_get`; `migrate` and
    `security-review` are I129's.
12. **The page's wait** is 30s, a constant as `toolTimeout`. Alternative: a `limits` setting.

### Decisions from review (2026-11-04)

- Batch proposals: allowed; the model's propose calls in a turn make one proposal, applied as one change.
- No unattended apply: every change needs the Apply click and a second confirmation in a dialog before
  it takes effect and is saved.
- Conversations are kept in the store, across restarts, within the bounds of `limits`.
- No CLI: the assistant lives in the workbench only.
- MCP (2026-11-05): fit serves MCP to outside agents on the same registry, levels and proposals (I130);
  apply stays in the workbench; fit's assistant does not call outside MCP servers.
- Page edits (2026-11-05): the assistant may draft the change the user asks for into the open page's
  editor or form fields; it never submits or saves — **Save and apply** stays the person's — and no
  version, ETag or audit change happens until then; a change no page can express is proposed (I126);
  the page tools are not served over MCP (I130).

## Media library: video on demand and channels

Proposed 2026-11-05. A video site keeps its films and episodes as files and wants them played on
the web, on demand and as scheduled channels, by the viewers its own API lets in, and only for a
while. fit gains a media library: directories of files catalogued, served as HLS on demand without
transcoding, and played into the streaming hub as live channels pushed on to other servers if
asked; and playback URLs that expire, signed for one viewer and one title by the site's API and
checked by fit on every playlist, segment and key. It builds on the streaming role (I116), media
auth (I115) and media tokens (I119); decision 032 records the choices below.

Principles, in the order they bind:

- **Files are prepared, not transcoded.** fit serves the codecs a file holds (H.264, H.265, AV1;
  AAC, Opus) as they are stored. Renditions are files of their own, made beforehand; a script in
  `scripts/` shows how with ffmpeg. Transcoding in process stays out, as in the streaming role.
- **Nothing blocks a worker** (rule 1): probing a file, reading its sample table and reading a
  segment's bytes run on a bounded file pool of the library's own; a request waits retained and
  goes on from the pool. The catalog, the sample tables and the built segments are each bounded in
  bytes (rule 11).
- **The site's API decides who watches; fit checks.** fit keeps none of the site's accounts. The
  API signs a URL for a viewer, a resource and a lifetime, itself or through `media_tokens`, and
  fit checks it on the worker, at once, without calling back.
- **A leaked URL dies soon and opens nothing else.** URLs are short-lived, bound to one resource (a
  title or a channel) and, if asked, to the viewer's address or session; every playlist, segment,
  key and subtitle a player fetches carries the grant; playback sessions per subject are capped.
- **Off by default**: no `media_library`, no files served and no scans.

Not in this track: transcoding, DRM with license servers (Widevine, FairPlay, PlayReady),
watermarks per viewer, and libraries in object storage (a later iteration if asked; the file pool
keeps the reads behind one interface so that it can be added).

### I131 Decision 032; an MP4 reader in fib

- **Do**: decision 032. Ask fib for A64 in [fib-api-requests.md](fib-api-requests.md): a reader of
  MP4 files that are not fragmented (ISO BMFF), in fib's `media/fmp4` or a new `media/mp4`, which
  parses `moov` (`stts`, `ctts`, `stss`, `stsc`, `stsz`/`stz2`, `stco`/`co64`, edit lists) through
  an `io.ReaderAt` without reading the whole file, finds a `moov` at the end as well as at the
  front, gives each track's codec config as `media/av` does, finds samples by time and by
  keyframe, and keeps its sample table in a compact form whose size is known before it is built;
  fuzzed. fib adds it and fit moves to that commit.
- **Done when**: fib's tests read ffmpeg-made files of each codec pair, `moov` at the front and at
  the back, with B-frames and edit lists, and every sample's times, offset and size match
  ffprobe's; a sample table's memory is within its bound; fuzzing the reader finds no panic.
- **Touches**: `docs/decisions` (032), `docs/fib-api-requests.md`, fib's `media`, `go.mod`.

**Decisions pending review** (made while building it, 2026-11-06; each stands until the user rules on it):

1. **Where the reader lives**: a new fib package `media/mp4`, using an exported
   `fmp4.ParseSampleEntry`, as `fmp4` reads fragments held as bytes and this reads a file through a
   `ReaderAt` and keeps tables; codecs are read in one place. Alternatives: inside `media/fmp4`; an
   internal package shared by both.
2. **The reader holds no file**: `Open` keeps no reference; `ReadTable(r)` and `ReadSample(r, …)` take
   it, so fit's file pool may close and open files again. Alternative: `File` holds the reader.
3. **The compact table**: `stts` and `ctts` run-length coded (`ctts` a sample when that is smaller),
   16-bit sizes for `stz2`, an offset and first sample a chunk; about 8.4 bytes a sample with
   B-frames. Alternative: an entry of 24 bytes or more a sample, faster and three times the memory.
4. **Edit lists**: the shift is the leading empty edits less the first playing edit's media time,
   which matches ffprobe on every fixture; the end trim and later edits are kept, not applied.
   Alternative: every edit applied.
5. **Times are the file's ticks**: `DTS` and `PTS` as ffprobe gives them with `-ignore_editlist`;
   `Table.Time` applies the edit list; `MinCTS` is given rather than shifting DTS.
6. **Strict and lenient**: tables that miss samples, chunks outside the file and fragmented files are
   refused; a short `ctts`, an unsorted or out-of-range `stss` and signed `ctts` v0 are read as
   players read them.
7. **`SeekKeyframe` searches by PTS**, keyframes presented in decode order, as for every codec fib
   carries.
8. **Limits are constants** (`MaxTracks`, `MaxBoxes` and the rest), not options.
9. **Fixtures**: seven small checked-in files (129 KB with their ffprobe output, made by
   `media/mp4/testdata/gen.sh`), and longer files in `media/interop`'s `TestMP4` in CI.
10. **fib CI's fuzz job** gains `media/mp4:FuzzOpen` only; the other media fuzz targets were never in
    it, and were left so.

### I132 Media libraries

- **Do**: a top-level `media_library`: named libraries, each with `roots` (directories), include
  and exclude patterns, and `scan_interval`; a scan on start, on the interval and on
  `POST /media/library/<name>/rescan`; a catalog of items, each with a stable `id` (from its path
  under the root, or a sidecar's), a title, its duration, tracks and codecs, size, renditions (files
  grouped by a naming rule or a sidecar `<name>.json`), audio tracks, subtitles (`.vtt`, `.srt`
  turned into WebVTT) and a poster; probing on the file pool, bounded by items, concurrent probes
  and sidecar size; a file that fails to probe listed with its error, not served; paths outside the
  roots refused (`..`, and symlinks leading out unless `follow_symlinks`); the catalog kept per
  engine and changed in place on reload, as `media_auth`'s policies are; the Admin API's
  `GET /media/library/<name>` and `GET /media/library/<name>/items/<id>`.
- **Done when**: a library of ffmpeg-made files lists every item with the durations and codecs
  ffprobe gives, its renditions and subtitles grouped; a file added, changed or removed shows after
  a rescan; a broken file is listed with its error; a path or symlink out of the roots is refused;
  the bounds and a reload hold under the race detector.
- **Touches**: `medialib` (new), `config`, `app`, `admin`, `schema`, `docs`, `test/workers`.

**Decisions pending review** (made while building it, 2026-11-06; each stands until the user rules on it):

1. **The patterns' names** are `files` and `exclude`, not the plan's "include and exclude", as
   `include` is a directive of the `.conf` format. Alternatives: `match` and `ignore`; a block.
2. **Pattern syntax**: one without `/` matches the name, one with `/` the path under the root, `**`
   spans directories; `exclude` prunes directories; dot-names are always skipped. Alternatives:
   gitignore's rules; regular expressions.
3. **IDs** are the path under the root without the rendition label and extension when that is safe
   in a URL; otherwise unsafe runs become `-` and 8 hex digits of the path's hash follow, so IDs stay
   stable and unique. Alternatives: reversible escaping (unreadable for CJK names); a hash only on a
   collision (an ID would change when a colliding file appears).
4. **An ID taken twice** (across roots or sidecars): the first in root and path order wins, the
   other is reported. Alternative: the root in the ID.
5. **Naming**: rendition labels `NNNp[fps]`, `Nk`, `Nkbps` and `audio[-lang…]`; subtitles and posters
   go to the item with the longest matching base; a sidecar's lists merge with the files named after
   it, and may name subdirectories but not above its own.
6. **A pool a library**, made at once, with the static handler's defaults (4 a CPU, at least 16; queue
   4096; 10s); `max_probes` keeps scans from crowding out serving. Alternative: one pool an engine.
7. **The sample-table cache lives in `medialib`**, shared by I133 and I134 under one bound
   (`max_table_bytes`), with `Table` and `Subtitle`; this adds `max_table_bytes`,
   `max_subtitle_bytes`, `file_workers`, `file_queue` and `file_timeout`.
8. **Sidecars** are read again at each scan, their bytes in a scan bounded by `max_catalog_bytes`.
9. **Defaults**: `scan_interval` 10m (at least 10s; it cannot be turned off), `max_items` 20,000,
   `max_files` 200,000, `max_catalog_bytes` 64 MiB, `max_probes` 4, sidecars 64 KiB (at most 1 MiB),
   subtitles 2 MiB, tables 128 MiB.
10. **The Admin API finds a library** with `medialib.Lookup` on the upstream engine, not through
    `app.Manager`, whose lock is held for a whole apply.
11. **`HasApps` does not count `media_library`**, as for `media_auth`: a config still needs a listener
    or routes.
12. **Fixtures' line endings**: `medialib/testdata/.gitattributes` sets `* -text`, so that Windows CI
    does not change the subtitles compared byte for byte.

### I133 Video on demand

- **Do**: a handler `vod` on a library: for each item a master playlist (renditions as variants
  with `BANDWIDTH`, `RESOLUTION` and `CODECS`; audio and subtitle groups), media playlists of fMP4
  segments, or MPEG-TS for older players (`container`), cut at keyframes near `segment_duration`,
  built from the sample tables on request and kept in a bounded cache; init segments; the file
  itself with `Range` (206, 416) for downloads and plain players, by sendfile where fib allows;
  `ETag` and `Last-Modified` from the file, `Cache-Control` set by the route, so that a CDN can
  keep segments; `auth` with a `media_auth` policy, action `play` on the stream `vod/<library>/<id>`.
- **Done when**: hls.js in headless Chromium and ffmpeg play every item and seek, each segment
  starting on a keyframe and audio within a frame of video, and Safari's own player plays them by
  hand; ffprobe finds every segment valid; a file changed or removed while it plays is answered 404
  and its cache entries dropped; the bounds hold under the race detector; on Linux, segments a
  second and RSS measured against nginx serving the same files cut beforehand.
- **Touches**: `handler/vod` (new), `medialib`, `schema`, `docs`, `test/workers`, `bench`.

**Decisions pending review** (made while building it, 2026-11-06; each stands until the user rules on it):

1. **URL names** are flat in the item's directory (`index[.c].m3u8`, `v0.c.m3u8`, `v0.N.m4s`,
   `v0.mp4` …), both containers always there, `container` picking only what `index.m3u8` is.
   Alternatives: a directory a container; a `?format=` query as the `hls` handler has, which a CDN's
   key and I135's path signing would not like.
2. **One clock for both containers**, the file's time 0 at 1.4s as in ffmpeg's MPEG-TS, keeping
   B-frame and AAC priming times without edit lists in fMP4, and one `X-TIMESTAMP-MAP` for subtitles.
   Alternatives: fMP4 from 0 with an edit list in the init, which players honour unevenly; an offset
   an item.
3. **Where a segment is cut**: the keyframe nearest its start plus `segment_duration`, never nearer
   than half of it; the file's end counts as a keyframe; `max_segment_bytes` counts the audio.
   Alternative: ffmpeg's first keyframe at or after the duration, with longer, less even segments.
4. **A segment's audio** is what plays under its video, so a seek has sound at once; alternate audio
   files are cut on a grid of their own.
5. **The main audio**: the renditions' own audio is the audio group's default, without a URI, only when
   every rendition has audio; otherwise the sidecar's default audio file, or the first.
6. **BANDWIDTH** is the catalog's average bitrate (with the largest alternate audio), so the
   multivariant playlist needs no tables. Alternative: the peak, read from every rendition's tables.
7. **The cache**: one LRU a store for plans, media playlists (only without a query), init sections,
   segments and WebVTT; a build holds its bytes only once its pool work starts; a segment larger than
   the cache is served, not kept; a build that finds no room answers 503.
8. **Caches are shared** by handlers of the same params across reloads, as the `hls` handler shares
   its muxers.
9. **`cache_control` is a param of the handler**, unset by default, where the plan said the route sets
   it, as a headers middleware cannot tell a segment from a 404; ETags are the file's time and size
   with the kind and a hash of the cutting params.
10. **Bounds**: `max_segment_bytes` 32 MiB (64 KiB to the smaller of 1 GiB and a quarter of the cache),
    `segment_duration` 1s to 60s, `timeout` 30s.
11. **Statuses**: a library not applied yet, or before its first scan, answers 503 with Retry-After 1;
    a segment too large or without a keyframe 500.
12. **Range sending** is copied from `handler/static/send.go`, not moved to a shared package, as
    static's tests use its internals.
13. **Only a file's first video and audio tracks** are played; MP3 is not served (neither muxer carries
    it).
14. **`test/workers`** lists `vod` in `notCovered`, pointing to `handler/vod`'s `TestNoWorkerBlocks`, as
    it needs the library role and a scan.
15. **ffmpeg's seek check** runs only where ffmpeg seeks in its own cut of the same file, as 6.1 and 7.1
    fail on their own fMP4 HLS.
16. **hls.js 1.5.20** is downloaded when the test image is built, not vendored (I137 may vendor it).
17. **The benchmark**: fit in `ubuntu:24.04` and nginx in its image, host networking, on CPUs 0–3, wrk on
    8–15; a synthetic library; nginx with `open_file_cache`.
18. **No fib request**: `fmp4.Fragment`, `AppendInit`, `mpegts.Muxer` and `SendFileStall` sufficed. What
    would close the gap with nginx for cached segments is sending them without a copy in user space
    (a memory-backed SendFile, or sendfile from a tmpfs cache): not asked yet.

### I134 Channels: files played as live streams, and pushed out

- **Do**: `streaming.channels`: a channel publishes into the hub, under its stream name, a list of
  library items, in order, shuffled, looped, or on a schedule of start times in a time zone, paced
  by the samples' timestamps on fit's timers, with timestamps kept continuous from one item to the
  next (a discontinuity in HLS); every egress of the streaming role serves it (HLS, HTTP-FLV,
  WHEP, and RTMP, RTSP and SRT play) under its `auth`; `push` targets that relay a channel, or any
  stream of the hub, to outside RTMP or SRT servers (a CDN's ingest), reconnecting with backoff;
  channels changed in place on reload, a new list or schedule taking effect at the next item; the
  Admin API to list channels and push targets, and to start, stop and skip.
- **Done when**: a channel plays two items back to back through each egress, with ffmpeg and
  hls.js, without a gap or a jump in the timestamps; a scheduled item starts at its time; a push
  target (fit itself and an outside server in a container) receives the stream and gets it back
  after the target restarts; under the race detector.
- **Touches**: `streaming`, `medialib`, `config`, `schema`, `docs`, `test/workers`.

**Decisions pending review** (made while building it, 2026-11-06; each stands until the user rules on it):

1. **A package of its own**, `streaming/channels`, which the Admin API imports, since `streaming`'s
   tests import `admin` and the code in `streaming` would make a cycle. Alternative:
   `app.Manager.Role(id)` behind an interface.
2. **`SetTracks` only when the tracks differ**, so films encoded alike play on seamlessly; fib's
   egresses shift timestamps at every `SetTracks` (A68). Alternative: at every item, a discontinuity
   an item.
3. **Items join by presentation time**; audio wholly before the start (AAC priming) is dropped; an
   item ends at its latest track's start plus duration. Alternative: by decode time, leaving gaps of
   about 120 ms.
4. **"Starts at its time"** means the slot's first picture is shown at the slot's time on the
   channel's timeline, the video sent up to the B-frame delay earlier.
5. **A slot under way** is joined at a start, or when a reload wakes an idle channel, from the
   keyframe before its position; slots a reload has passed are skipped.
6. **Nothing to play**: the channel stays published and silent; stop then start plays a finished list
   that does not loop from its first item again.
7. **Defaults**: the rendition with the most pixels, then the highest bitrate; audio falls back to the
   item's default audio file; `time_zone` UTC; `loop` on.
8. **Bounds**: `read_ahead` 3s (0.5s to 1 minute), `max_buffer_bytes` 16 MiB, reads of a second; a
   packet more than a second late pauses the channel, counted as an underrun.
9. **Push**: a target polls every `retry_min` while its stream is not published; its subscription waits
   5s; the backoff starts over after `retry_max` up; a reload keeps the connection when the stream,
   URL and `connect_timeout` are unchanged.
10. **Names**: push targets without a name are `stream#n`, the top-level ones first; a channel's status
    lists every target of its stream; actions through the Admin API are not saved in the config.
11. **Interop** pushes to MediaMTX 1.15.3; a Linux test binary runs in a Debian trixie image with
    ffmpeg, Chromium and hls.js 1.5.20.
12. **fib A68, compared per kind of track** rather than a `hub.TracksChanger` the hub would call: no
    caller changes, and "the same publisher, the same clock" would be wrong for SRT encoders that start
    again mid-connection. WHEP now moves on any step back of a kind (it let up to a second back
    through); FLV and RTMP sequence headers carry their own track's last time; HLS decides at the new
    writer's first packet; the kinds are video and the rest (two audio tracks share one).

### I135 Playback URLs that expire

- **Do**: signed playback URLs in `media_auth`: `url_signing`, signed with a key of the policy's
  `keys` ring (I119), each URL carrying `exp`, `kid`, if asked the viewer's address (`ip`, or its
  /24 or /64) and a session `sid`, and `sig`, an HMAC over the resource's path prefix
  (`/vod/<library>/<id>/` or a channel's), the expiry and the bound fields; checked on the worker
  without a callback. The playlist hands its segments, init segments, keys and subtitles a grant
  of its own for that resource, in their URIs or as a cookie on the resource's path (`cookie`,
  for players that drop query parameters), that lives `play_grace` beyond the URL's expiry or the
  item's duration, whichever is longer, so that a film does not stop halfway while a leaked URL
  still dies. `media_tokens` gains `POST <prefix>/urls`: the site's API names the viewer (subject,
  address), the resources and a ttl up to `max_ttl`, and gets the URLs back whole. `max_sessions`
  per subject counts playback sessions by `sid`, refusing a new one or ending the oldest
  (`on_limit`); `referers` and `origins` allow lists; revocation by rotating a key, as in I119.
  Tokens and signatures never reach a log.
- **Done when**: a URL from `/urls` plays in hls.js; once it expires its playlist is refused 403,
  while a player already playing goes on to the end of its grant; a URL for one item is refused for
  another and for a path changed in any byte; a URL bound to an address is refused from another;
  a third session past `max_sessions: 2` is refused, or ends the oldest; rotating a key refuses
  the URLs it signed and only those; the access log shows `REDACTED`; under the race detector.
- **Touches**: `mediaauth`, `handler/vod`, `handler/mediatokens`, `streaming`, `config`, `schema`,
  `docs`, `test/workers`.

**Decisions pending review** (made while building it, 2026-11-06; each stands until the user rules on it):

1. **The resource** is a path up to its last `/` (`/x/cam.flv` is `/x/cam/`), each handler checking
   against the resource it computes. Alternative: the stream's name signed, which would not bind a URL
   to a route.
2. **`sub` in every URL**, to count sessions by subject and give the access log its user; the docs
   advise an opaque ID. Alternative: a hashed subject, which I138's session list could not show.
3. **A child grant lives** the later of the URL's expiry and now plus the item's length, plus
   `play_grace`, and is passed on unchanged by the playlists fetched with it, so a leaked grant cannot
   be renewed; a live stream's lives the expiry plus `play_grace`.
4. **The signature** is 16 bytes of HMAC-SHA256 (about 160 bytes of query), under a key derived apart
   from the token key, so that a token's MAC never checks as a URL's; `kid` is left out for a single
   secret.
5. **Defaults**: `ttl` 10m, `play_grace` 30m, `session_idle` 2m, `max_tracked_sessions` 100,000 swept at
   most once a second; past the cap a new session answers 503.
6. **One viewer a `/urls` request**, up to `max_tokens` paths, a random `sid` when none is given,
   `url_base` before the paths. Alternative: viewers in a batch.
7. **Cookies**: cookie mode puts the grant only in the cookie, so playlists stay cacheable; the cookie
   is `Secure; SameSite=None` over TLS and `SameSite=Lax` otherwise; a playlist answering a URL grant is
   sent `private, no-store`.
8. **`referers` and `origins`** are origin patterns; a request without the header passes.
9. **A URL's refusal** is 403 with its reason in the body, counted by reason; a bad cookie falls back
   to the token or Basic check when the request carries those too.
10. **Redaction** at the top of `vod`, `hls` and `httpflv`, which also stops the logging of `?token=`
    on early answers found on the way; refusals now carry the handler's `cors`.
11. **I133's `TestPoolGivesUp`**, failing about once in ten under load, waits for the pool to drain
    first.
12. **The interop harness** (`test/docker/vod`) serves through `url_signing` with `max_sessions` 1 and
    `end_oldest`; `LOCK` is a new option of `e2e.sh`, and `collector.py` stands for the site's API.

### I136 Encrypted segments

- **Do**: `encrypt` on `vod` and on a channel's HLS: segments encrypted with AES-128 (`METHOD=AES-128`,
  for MPEG-TS and fMP4), a key per item or channel derived from a library secret and rotated every
  `key_rotation` segments on channels; the key URI served by fit only to a grant for that resource,
  so that segments copied without a grant cannot be played; encryption on the file pool, its
  buffers bounded.
- **Done when**: hls.js and ffmpeg play encrypted items and channels; a key request without a grant,
  or with one for another resource, is refused; segments fetched alone do not decode; the cost a
  segment measured on Linux.
- **Touches**: `handler/vod`, `streaming`, `medialib`, `schema`, `docs`.

**Decisions pending review** (made while building it, 2026-11-06; each stands until the user rules on it):

1. **Encryption needs `auth`**: `vod`'s `encrypt` requires it, and an `hls` handler without `auth`
   serves no channel keys (403), as an open key protects nothing.
2. **A key an item, the IV the segment's number**, none written: an item's renditions share the key
   and a segment number's IV, which tells only whether their first blocks match. Alternatives: a key a
   rendition; an IV written a segment, which needs a key tag a segment.
3. **Init sections stay clear**: the key comes after `#EXT-X-MAP` in a title's playlist, and
   `METHOD=NONE` before a later MAP in a channel's. Alternative: init sections encrypted too, with IVs
   written.
4. **Key names**: `aes.key` for an item, `key-<session>-<epoch>.key` for a channel.
5. **Keys are derived, never stored**: the first 16 bytes of HMAC-SHA256 under a key the library
   derives from its `secret`, over the kind, the resource and the period; fits with one secret serve the
   same segments; a new secret changes every key and ETag, and copies a CDN holds must be purged
   (documented).
6. **A channel's key names the Muxer's session**, so a restart never uses a key and IV again; a key is
   served only up to the next segment's period, so no viewer takes keys ahead.
7. **An encrypting `vod` serves no clear file** (404), as a download would undo it; subtitles and
   posters stay clear.
8. **Channels encrypt HLS only**; RTMP, RTSP, SRT, HTTP-FLV, WHEP and push stay clear under their own
   `auth`.
9. **No Low-Latency HLS for an encrypted channel** (its tags removed, parts 404), as parts cannot be
   encrypted outside fib.
10. **The channels' encryption is a stopgap in fit** until fib request A69 lets fib's Muxer encrypt:
    each segment copied and encrypted once on the library's pool, cached within the handler's
    `max_bytes`, playlists rewritten. Alternative: no encrypted channels until A69.
11. **`key_rotation`** defaults to 10 segments (at most 100,000) and means nothing without `encrypt`.

**Low-Latency HLS across new tracks** (fib A71, after I136, 2026-11-06), decisions pending review:

12. **Fixed in fib's playlist and segment ends, not worked around in hls.js or fit**: the preload hint
    of a segment's first part follows that segment's discontinuity and `EXT-X-MAP` (alternative: a
    `TYPE=MAP` preload hint, which hls.js ignores); hls.js's keeping the old init section for a
    fragment hint (`mergeDetails`) and placing the next discontinuity by the `EXTINF` of a segment read
    by its parts are taken as players' behaviour to meet, not reported upstream.
13. **The hint names the key in force again after a new `EXT-X-MAP`**, not the next segment's, which
    no playlist names before that segment is cut (`KeyEpoch`); hls.js reads the next segment's key
    from the playlist that lists its first part.
14. **A segment ended by new tracks, the stream's end or a jump in the clock lasts until the earliest
    of its tracks ends** (each track's last packet and the step before it; a track that ended before
    the last part began does not count): when audio and video end apart, the next item overlaps the
    longer track's tail rather than leaving a hole, as a player replaces an overlap but stalls at a
    hole. Alternative: the video's end alone, which left an audio-only hole when the audio ends first.
15. **Left as it is**: tracks changing twice before the new tracks' first keyframe leave a playlist
    that hinted the first change's init section; hls.js would keep that one. Rare (a publisher's
    tracks set again within one keyframe interval) and not handled.
16. **The interop test**: `live/secll` plays items whose tracks differ again (a and c), a clear
    `live/mixll` beside it, both required to load a discontinuity by their parts; ffprobe reads
    `live/mixll`'s Low-Latency playlist; the page reports its load and append log on a fatal error.

### I137 A video site, end to end

- **Do**: an example in [fit-examples](https://github.com/lesismal/fit-examples): a library, `vod`
  and channel routes, `media_tokens` behind the route's middleware for the site's API, a small Go
  API that logs a viewer in and hands out URLs, and a page with hls.js. `media_pages` gains a
  library page and a player page behind the policy's login (hls.js vendored, Apache-2.0).
  `docs/media-library.md`, in English and Chinese.
- **Done when**: in Docker on Linux, headless Chromium logs in to the example's API, plays a film
  and a channel through the URLs it issues, seeks, and is refused once a URL and its grant expire;
  `TestChapterExamples` and the schema's descriptions cover the new fields.
- **Touches**: `handler/mediapages`, `schema`, `docs`, `test/docker`, fit-examples.

**Decisions pending review** (made while building it, 2026-11-06; each stands until the user rules on it):

1. **The new fields**: `libraries` a list, `vod` a URL template with `{library}` and `{id}`, `channels`
   a switch that needs `hls`. Alternatives: one library a handler; `vod` read from the config's routes.
2. **The player pages sign** through the policy's `url_signing` themselves, not `media_tokens`, so no
   credential more is needed; the subject is the user's name, as these users are fit's own staff;
   each URL bound to the requester's address, a new session a play, `url_signing`'s `ttl`; without
   `url_signing`, the plain URL and the login's cookie. Alternatives: no address bound; a hashed subject.
3. **Listings are filtered** to what the viewer may play, hiding the rest. Alternative: list all,
   refuse at play.
4. **Bounds**: 16 libraries, 60 items a page (500 at most), 1,000 channels listed, a library not yet
   scanned retried 30 times by the page.
5. **hls.js 1.5.20**, the version the harnesses use, vendored untouched (its `sourceMappingURL` line
   too) and served while the pages are on, so a page of the same site loads it, as the example's does.
6. **The dependency** is recorded as an I137 entry in decision 032, not a decision of its own.
7. **The example's API** lives in fit-examples' root module, so CI's `go vet ./...` covers it;
   `golang.org/x/crypto` becomes a direct requirement there. Alternative: a nested module.
8. **A placeholder secret is committed** (`video-site/secret`, "replace-me"), as `media_tokens` needs a
   `secret_file`; the comments say to replace it.
9. **The example's lifetimes**: URLs 2 minutes, `play_grace` 30 minutes, `max_sessions` 2 with
   `end_oldest`; viewers alice and bob, staff `staff`, with the other examples' `example-password`.
10. **The e2e** lives in fit (`test/docker/videosite`), takes the fit-examples checkout as `EXAMPLES`,
    drives Chromium from Python's standard library over `--remote-debugging-pipe`, and shortens the
    lifetimes by editing the example's `fit.conf`, failing loudly if those lines change.
11. **`mediaauth.Whois` and `ViewerAddr`** are exported rather than the token and address logic copied
    into `mediapages`.

### I138 The workbench's media library pages

- **Do**: the workbench manages all of this track, in English and Chinese, behind its logins.
  - **Menu**: a `media_library` page in the flat menu beside streaming and media auth (I122); in the
    Engine menu (I123), each port's streaming entry lists its `vod` routes and channels, and the
    port's line gains a `VOD` tag.
  - **Config pages**: `media_library` (roots, patterns, scan interval, bounds, `follow_symlinks`),
    `vod` routes (library, container, segment duration, cache, `encrypt`), `streaming.channels`
    with `push` targets, and `media_auth`'s `url_signing` (bound fields, `play_grace`, `cookie`,
    `max_sessions`, `on_limit`, `referers`, `origins`), each with the UI, `.conf`, `.yaml` and
    `.json` tabs checked through the apply path, as every config page is (I120).
  - **Library browser**: each library's items in a table, searched, filtered (codec, resolution,
    probe errors) and paged from the Admin API; an item's page with its tracks, codecs, renditions,
    subtitles, poster and sidecar, its probe error if any, and a preview player (hls.js) on a
    short-lived URL the workbench signs for the logged-in user; rescan a library, with its progress.
  - **Channels**: each channel's list or schedule edited as a timeline (items dragged in from the
    library, start times in its time zone, loop and shuffle), what plays now and next with its
    position, start, stop and skip, and a preview; each push target's state (connected, retrying,
    the last error), bytes and bitrate.
  - **Playback URLs and sessions**: a form that issues a test URL through `media_tokens` (subject,
    resource, address, ttl) and shows it with a player, never storing it; the policy's keys with
    their names and ages (never the secrets), rotated one or all with a confirm, as I119 does; live
    playback sessions by subject, resource, address and start, each endable, with the counts per
    subject against `max_sessions`.
  - **Metrics**: per library and channel the viewers now, requests and bytes a second, the segment
    and sample-table caches' hit rates and bytes, the file pool's queue and refusals, and 403s by
    reason (expired, wrong resource, address, session cap), from fit's metrics.
- **Done when**: every page works in a real browser against a running fit and in the bundle's mock
  tests, in English and Chinese; config saved from each page is checked and applied, its errors
  linked back to the field; the preview plays through a signed URL and stops once it is refused;
  a channel's schedule edited on its timeline takes effect at the next item; a rotated key and an
  ended session take effect at once; no secret, token or signature reaches the browser beyond the
  preview's own short-lived URL; the Admin API's new read endpoints are bounded and paged under
  the race detector.
- **Touches**: `app/workbench`, `admin`, `medialib`, `streaming`, `mediaauth`, `docs`.

**Decisions pending review** (made while building it, 2026-11-06; each stands until the user rules on it):

1. **One module**, `gw.medialib`, of eight pages, not pages added to Streaming and Media auth, so the
   track's pages stay together and the others unchanged.
2. **Test URLs** are issued by the Admin API (`POST /media/auth/{policy}/urls`) within an optional
   `media_tokens` route's bounds, as the workbench holds no credentials for that route's middleware.
   Alternative: a proxy carrying the site API's credentials.
3. **The preview signs** for subject `workbench:<user>`, a new session each time, 2 minutes; a route
   without `url_signing` gets a short-lived token; a public route plays unsigned.
4. **The preview plays from the listener itself**: the console's CSP admits the `vod` and `hls`
   listeners, and the page warns when the route sets no `cors`; proxying the media through the console,
   which buffers whole bodies, was rejected.
5. **A short player buffer** (8–16s), so an ended session or a rotated key stops the preview within
   seconds; with hls.js's defaults a short film is fetched whole and never refused.
6. **Rotation from the workbench** only for keys kept in a secret file, written then rotated as
   `media_tokens`' revoke does; 409 for keys written in the config.
7. **A key's age** is its file's modification time, never the secret; keys in the config show none.
8. **Channel edits go through the shared draft** and Save and apply, checked as any change; the
   channels page's raw tabs hold the whole `streaming` member.
9. **The resolution filter** is `min_height` and `max_height` in the API, grouped in the page as under
   720p, 720p, 1080p, 4K and up.
10. **A library's viewers** are the live sessions under its `vod` route's prefix; without `url_signing`
    viewers are not known.
11. **One hls.js copy**, in the workbench, which `media_pages` serves (I137's identical copy removed).
12. **Ask AI** uses `explain` and `diagnose` until I152's media skills.
13. **Secrets written in the config** still show in the URL signing page's raw tabs, as the config
    editor shows them on the Media auth page; redacting `GET /config` would change the whole editor.

### I139 Media files managed from the workbench

- **Do**: files of a library managed in the workbench: uploads into a root's `upload_dir`, resumable
  in chunks (`POST` to create, `PATCH` at an offset, `HEAD` to resume), bounded by `max_upload_bytes`,
  concurrent uploads and disk free, written to a temporary file on the file pool and renamed into
  place once whole, then probed; rename, move between folders of a root, and delete, each confirmed;
  a sidecar's title, poster, renditions and subtitles edited in a form; a file in use by a channel
  or still playing warned about before it is moved or deleted; every change by a logged-in user,
  CSRF-checked, and written to the audit log once it exists (I126).
- **Done when**: a multi-GB upload resumes after a cut connection and plays once it is probed; an
  upload past a bound is refused 413 and leaves no partial file; a path out of the roots is refused;
  a rename keeps the item's `id` when its sidecar names one; a deleted item leaves the catalog and
  its caches; under the race detector.
- **Touches**: `medialib`, `admin`, `app/workbench`, `schema`, `docs`.

**Decisions pending review** (made while building it, 2026-11-06; each stands until the user rules on it):

1. **One `upload_dir` a library**, inside one of its roots (or a root), as roots are plain strings in
   the config and one target keeps the bounds simple. Alternatives: a field a root; a path relative to
   the first root.
2. **The protocol is shaped as tus's** (`Upload-Offset`, `Upload-Length`, POST, PATCH, HEAD, DELETE),
   without a whole tus server (`Tus-Resumable`, `Upload-Metadata`, extensions).
3. **A body past the upload, or a full disk, gives the whole upload up** and removes its file, so no
   partial file is left. Alternative: trim back to the last good offset.
4. **A rename** renames only the files in the item's directory named after it (`base.*`,
   `base-poster.*`) and rewrites a sidecar that names one; a new name that would read as a rendition
   label or a poster is refused. Alternative: every file renamed and the sidecar rewritten whole.
5. **A move** takes every file, keeping its path under the item's directory, so the sidecar's
   references hold; missing folders are made.
6. **An item in use** (named or played by a channel, or played in a session) is refused 409 for a
   rename, move, delete or a sidecar edit that changes its ID, unless `force`; the page shows the usage
   in its confirm dialog. Alternative: a warning in the page only.
7. **"Still playing"** is a live session whose resource is the item's `vod` prefix, under a policy with
   `url_signing`; the answer says when sessions cannot be counted.
8. **The catalog after a change**: the item leaves it at once and stays out of a scan begun before the
   change; a rescan lists it as it now is.
9. **Uploads outlive a restart** by a `.json` beside each `.part`; expiry and orphans are cleaned at the
   start of each scan, so their timing follows `scan_interval`; uploads past `max_uploads` at a restart
   are removed. Alternatives: memory only; a timer of their own.
10. **Writes never follow links out**: every file written lies inside its root after resolving, whatever
    `follow_symlinks` (which is for reading) says.
11. **Names valid on every OS**: no `\`, `:`, `*?"<>|`, controls, leading `.`, trailing `.` or space, or
    Windows device names; 255 bytes a name, 1,024 a path, 32 levels; uploads only of kinds the library
    reads.
12. **Audit**: kind `media`, source `workbench`, one entry a change (`media.upload.begin`,
    `media.upload`, `media.upload.cancel`, `media.rename`, `media.move`, `media.delete`,
    `media.sidecar`), written when the console runs the assistant; changes made with the Admin API's
    token are logged, not audited.
13. **A streamed PATCH through the console** has 10 minutes for its body (in place of the console's 30s
    read timeout), a ceiling of 1 TiB on any streamed body, and `LimitBody` set to what the upload still
    takes; the workbench sends 8 MiB pieces.
14. **The Admin API's own server takes PATCH too**, read 64 KiB at a time, so scripts upload with the
    token, within its one-minute read timeout.
15. **The page uploads one file at a time**, retrying a piece up to 30 times, up to 15s apart;
    `max_uploads` bounds other clients.
16. **A sidecar edit** carries the sidecar's time and is refused 409 if the file changed; references must
    exist, lie in the root and be of the right kind; another item's ID is refused; a new sidecar is not
    written over a file already there.
17. **Defaults**: 32 GiB, 4 uploads, 1 GiB free, 24h; at most 1,024 uploads and 2,000 folders listed.

## Cluster management

Proposed 2026-11-05. One fit manages many: an instance set aside as the manager (or any instance
with the feature on) holds the address and an administrator's credentials of other fit instances,
its members, connects to them, shows them together, groups them, and changes their configs
together, previewed, checked on every member and rolled out in stages. Every feature has its pages
in the workbench under a first-level **Cluster** entry in the left menu; nothing here is an API
alone. Decision 033 records the choices below.

Principles, in the order they bind:

- **The manager is not in the data path.** Members serve traffic from their own configs whether or
  not the manager runs; a manager down, or gone, costs management and nothing else. There is no
  consensus among members and no runtime state shared between them (rate-limit counters, caches,
  sessions): that would be a data-plane track of its own.
- **Members change as little as possible.** The manager speaks the APIs a member already has: its
  console, logged in with a user name and password, through which the console passes on the Admin
  API; or the Admin API itself with its bearer token. Every change on a member goes through that
  member's own apply path with `If-Match`, lands in its `/versions` and can be rolled back there.
- **Credentials stay in the manager's store.** Cluster management needs `admin.store`; members'
  passwords and tokens are kept encrypted there, written once in the pages and shown masked, never
  sent to a browser or written to a log. Members' certificates are verified (a CA, or a SHA-256
  fingerprint pinned once a person confirms it); there is no silent `insecure`.
- **A person applies.** A change to many members is shown as a diff per member, checked by each
  member without applying it, and applied only after a confirm dialog, in batches with health gates
  and rolled back on failure. Drift is reported and fixed by a click; there is no unattended sync in
  this track.
- **Nothing blocks a worker** (rule 1): the manager's calls to members go out on fib's asynchronous
  HTTP client; fan-out is bounded in concurrency, every call has timeouts (rule 10), and every
  answer, cache, history and audit entry is bounded (rule 11).
- **Off by default**: no `admin.cluster`, no Cluster menu, no outgoing calls.

Words used below: the **manager**, the fit that manages; a **member** (node), a fit it manages; a
**group** (resource group), a set of members by labels or by name; a **template**, config members
the manager owns on the members of the groups it is attached to; a **rollout**, one change applied
to many members in stages.

The workbench's Cluster menu, built up by the iterations below:

| Entry | Page | Iteration |
| --- | --- | --- |
| Overview | every member's health, version, config version, drift, nearest certificate expiry and connections, as cards or a table, filtered by group and label | I142 |
| Nodes | add, edit, remove and test members; credentials written once; a certificate's fingerprint confirmed on first contact; a member's detail page | I142 |
| Node switcher | in the header: open any member's own pages (config, Engine, Consistency, versions) from the manager | I143 |
| Groups | groups by label selector or by name, their members, and bulk operations with each member's result | I144 |
| Templates | templates, their owned paths and variables, attached to groups; sync state per member; import from a member; compare two members | I145 |
| Rollouts | plan (diffs and checks per member), strategy, progress batch by batch, pause, resume, abort, roll back; history | I146 |
| Drift and alerts | drift per member against the templates, one-click fixes as rollouts; alert rules and webhooks | I147 |
| Users and audit | the manager's users and their roles per group; the audit log of every cluster action | I148 |
| Metrics, logs, certificates | requests, errors and connections per member and group over time; logs tailed across members; the certificate inventory | I150 |

Not in this track: consensus or shared runtime state among members; a replacement for Kubernetes
or GitOps (the Kubernetes provider stays the way to drive fit from a cluster's resources); a
manager with a standby (its store is exported and imported as any store is); upgrading members'
binaries (I151 records the question, not a plan).

### I140 Decision 033; what a member needs to be managed

- **Do**: decision 033. On every fit, so that a manager can manage it: `GET /node` in the Admin
  API (and `/api/node` through the console): a node ID made once and kept in the store (or beside
  the config file without one), the host name, fit's and fib's versions, the start time, the
  running config's version and ETag, the roles running, a digest of the schema, and a capability
  list a manager checks before using an endpoint; a dry run, `PUT`/`PATCH /config?dry_run=1` and
  `PUT /config/text?dry_run=1`, which checks a config through the whole apply path, compiled and
  loaded, without applying it, answering as an apply does; login answers that a program can use
  (the CSRF token in the login's JSON as well as from `/api/session`); failed logins slowed per user
  and per address, with backoff and a cap, so that a member's console on a network withstands
  guessing.
- **Done when**: `/node` answers the same ID across restarts, with and without a store; a dry run
  refuses what an apply refuses with the same errors and leaves the version, the config and the
  store as they were; a program logs in, reads and applies with the CSRF token; the tenth wrong
  password in a row is slowed and the right one still works once the backoff ends; under the race
  detector.
- **Pages**: the workbench's Account menu shows the node ID and versions; the config pages gain a
  **Check** button beside Save that runs the dry run and shows its errors where Save would.
- **Touches**: `docs/decisions` (033), `admin`, `admin/store`, `login`, `app/console`,
  `app/workbench`, `docs`.

**Decisions pending review** (made while building it, 2026-11-06; each stands until the user rules on it):

1. **The node ID without a store** is kept in a file beside the config (`fit.node-id`), where the
   roles keep theirs; a fit that gains a store takes it over. Alternatives: `$XDG_STATE_HOME`; no ID
   without a store.
2. **A random UUIDv4**; `admin.node_id` takes 1–64 of `[A-Za-z0-9._-]`. Alternative: the host name,
   which changes with the host and collides in containers.
3. **`admin.node_id`**, not in the plan, for containers with a read-only config and no store, which
   would otherwise get a new ID at each start.
4. **Dry runs** on `PUT`, `PATCH` and `DELETE /config`, `/load` and `/config/text`; not rollback,
   reconcile or import.
5. **A dry run tries new addresses with a brief bind**, so a port taken is refused as an apply refuses
   it.
6. **A dry run compiles without an engine**, as `fit validate` does, so upstream pools neither dial nor
   probe. Alternative: the real environment, which would also find differences in shared pools.
7. **A slowed user is refused (429), not delayed**, as holding the request would tie up the login pool;
   at most 5 minutes after each guess. Alternatives: a delay; loopback exempt.
8. **Names that are not users share one failure count**, keeping the table bounded; unknown names slow
   down together.
9. **Address lockouts double** from 15 minutes to an hour, starting over after 15 quiet minutes.
   Alternative: a flat 15 minutes.
10. **A user is slowed after 10 failures in a row**, above an address's 5, so that a guesser from one
    address cannot slow the real user.
11. **A failed password check on the Account page counts** as a failed login.
12. **`/api/node` at the console's top level** as well as `/api/m/admin/node`, as the plan names it.
13. **A dry run's answer** gives the running config's `version` and `etag` (for the next `If-Match`),
    `new_etag` and `unchanged`, with `Fit-Dry-Run: 1`; it is not counted as a reload.
14. **Capability names** are dotted, sorted and never change meaning; `store` and `console` appear only
    when on.
15. **Check is enabled only with unsaved changes**, as Save is.
16. **Decision 033 also writes down the plan's seven open questions as proposed**, such as binary
    upgrades (I151) not taken up, pending review.
17. **`Retry-After` keeps its rounding** (whole seconds plus one), and the body's `retry_after` the same
    number.

### I141 The manager: members, their credentials and their state

- **Do**: `admin.cluster` (`poll_interval`, `timeout`, `max_nodes`, `max_concurrency`,
  `max_response_bytes`), refused without `admin.store`; the store's payload gains `cluster`:
  members, groups, templates, rollouts and the audit log, the payload's version bumped and the old
  one still read. A member has a name, labels, a URL and its access: `console` (user name and
  password; logged in again on 401 or when the session ends; a member asking for a password change
  reported, not changed) or `admin_api` (a bearer token); TLS by the system's CAs, a `ca` PEM, or a
  pinned `fingerprint`. A poller on the console pool reads each member's `/node`, `/status` and
  `/config/check` every `poll_interval`, keeping its reachability, latency, last error, versions,
  uptime, config version and ETag, reload counts, connections, upstream backends down, nearest
  certificate expiry and drift count. The manager's endpoints, as console module `cluster`:
  `GET/POST/PUT/DELETE /api/m/cluster/nodes[/<name>]`, `POST .../nodes/<name>/test` (which answers
  the certificate's fingerprint for a person to confirm), and `GET /api/m/cluster/state`.
- **Done when**: in Docker, a manager and three members: members added by password and by token
  show their state; a wrong password, a certificate not trusted, a pin that does not match and a
  member down are each reported apart; a member's restart and a manager's restart are recovered
  from; no password, token or session cookie appears in any answer, page or log; the poller's
  concurrency and response caps hold; under the race detector.
- **Pages**: none yet beyond what I142 draws on these endpoints.
- **Touches**: `cluster` (new), `config`, `admin`, `admin/store`, `app`, `schema`, `docs`,
  `test/docker`.

**Decisions pending review** (made while building it, 2026-11-06; each stands until the user rules on it;
also in decision 033 as its items 8–19):

1. **A store section, not a payload field**: members live in the store's section `cluster`, a document
   of its own version, which an older fit opens and writes back untouched and which exports leave
   out, so no credential leaves in an export; a payload field and format 4 would have made every older
   fit refuse the whole store. Alternative: the plan's payload field and format 4.
2. **`admin.cluster` needs `admin.console` too**, which serves the endpoints. Alternative: also in the
   Admin API, which I150's CLI may want.
3. **The poller is a goroutine of its own**, only starting asynchronous calls, not tasks on the
   console's pool, whose tasks belong to requests.
4. **One fib client a member**, two connections at most, so each member's TLS is checked by its own
   settings and its certificate kept.
5. **`ca` and `fingerprint` exclude each other, and there is no `insecure`**; a pin trusts the leaf
   alone, whatever its names and dates.
6. **Login backoff**: a minute doubling to 15, reset by an edit or a test that works; a member's 429 is
   waited out; a console that asks for a new password is checked again on the same backoff, its session
   logged out each time.
7. **Sessions are logged out** on removal or change, after each test and when the manager stops (2s at
   most), so the manager never fills a member's `max_sessions`.
8. **Tests have a bound of their own** (4), apart from `max_concurrency`; `POST /test` tests a member not
   yet added, and a saved member's test may take a draft.
9. **The audit log** records adds, updates and removals: who, when, which member and the names of the
   fields changed, never their values, 1,000 entries; tests and polls are not recorded.
10. **Credentials are never shown**, not even their last characters: `password_set` and `token_set`.
11. **Defaults**: `poll_interval` 10s, `timeout` 10s, `max_nodes` 256, `max_concurrency` 8,
    `max_response_bytes` 4 MiB.
12. **`http` member URLs only to loopback**, so credentials never cross a network in the clear.
13. **Labels** as Kubernetes' (32 at most, keys and values of 63 characters).
14. **A member whose entry in the store is invalid** is shown with its error and not polled, not dropped.
15. **A member's state keeps what was last read** while it is down.
16. **A rename is a `PUT` with a new `name`**; the renamed member gets a new poller node and session.

### I142 The Cluster menu: overview, nodes and a node's page

- **Do**: the first-level **Cluster** entry in the workbench's left menu, shown when `admin.cluster`
  is set, with **Overview** and **Nodes**. Overview: a card or a row per member, its state colored,
  with health, fit version, config version, drift, nearest certificate expiry, connections and
  latency; filtered by group, label and state, sorted, refreshed as the poller reads. Nodes: add and
  edit a member in a form (name, labels, URL, access, credentials written once and shown masked,
  TLS), **Test** showing the certificate's fingerprint for a person to trust, remove with a confirm.
  A member's page: its `/node`, listeners, upstreams and their backends, certificates, versions and
  consistency, read through the manager. The menu's labels and every page in English and Chinese;
  `docs/cluster.md` in both, with a manager-only config (`fit run -admin` with `admin.cluster`) and
  the recipe for members behind NAT through the tunnel until I149.
- **Done when**: in a browser, against the Docker set of I141, a member is added, tested, trusted,
  edited and removed; the overview shows each failure mode of I141 and recovers; a credential
  field never shows what was written; the bundle's tests and the browser checks pass in both
  languages.
- **Pages**: Cluster → Overview, Nodes, a node's page.
- **Touches**: `app/workbench`, `cluster`, `docs`.

**Decisions pending review** (made while building it, 2026-11-06; each stands until the user rules on it;
also in decision 033 as its items 20–27):

1. **A member's page reads the member** through a narrow pass-through of four GETs (`node`, `status`,
   `versions`, `check`) on the poller's session, 8 at once at most, rather than I143's general one now
   or the poller's last answers kept (memory a member, and stale).
2. **`fit run`, not the config, checks a manager's store**, so the plan's `fit run -admin` with
   `admin.cluster` alone runs: a store kept beside the config cannot be seen by validation; `fit
   validate` no longer reports a manager without a store. Alternative: `-admin` writing `admin.store`
   into the config it validates.
3. **The menu's place**: after the global pages (after Logs), before Account.
4. **No group filter yet**: a label selector (`k=v`, `k!=v`, `k`) until I144; up green, error amber,
   down red, unknown grey.
5. **The overview reads `/state`** every poll interval, between 2s and 10s, without a push from the
   server. Alternative: a stream of changes.
6. **Trusting a certificate**: confirming its whole fingerprint pins it in the form and tests again; a
   saved member is saved at once; a certificate replaced is trusted behind a red warning.
7. **Extension points** (`FitCluster.addAction`, `rowExtra`) so I149 adds to the pages without editing
   them.
8. **Nodes shows the last 20 audit entries**; the whole log is I148's.
9. **Members behind NAT until I149**: the tunnel's hub and proxy in the manager, the member added at a
   loopback `http` URL, the hop to the member being the tunnel's pinned TLS. Alternative: a hub host
   of its own.

### I143 A member's own pages, opened from the manager

- **Do**: a node switcher in the workbench's header: choosing a member opens the workbench's own
  pages (config with its text tabs, Engine, Consistency, versions, the role pages) against that
  member, the manager passing `/api/n/<member>/...` on to the member's API with the stored session;
  the member's schema read from the member; the page's title bar names the member in a color of its
  own so that no one edits the wrong one; a member whose version the bundle does not know opens
  read-only. Every write through the switcher in the manager's audit log with its user.
- **Done when**: every config page reads and saves a member's config, the save's errors linking to
  the right field; a stale ETag on the member is refused as it would be locally; a member of an
  older or newer version opens read-only; switching back to the manager leaves no member's data on
  the page; the body caps hold; browser checks in both languages.
- **Pages**: the node switcher; every existing page, against a member.
- **Touches**: `cluster`, `app/console`, `app/workbench`, `docs`.

**Decisions pending review** (made while building it, 2026-11-06; each stands until the user rules on it;
also in decision 033 as its items 56–67):

1. **The path is the plan's `/api/n/<member>/...`** at the console's top level, by a small generic
   `console.Module.Prefixes`; `/api/m/cluster/n/...` is refused, so there is one way.
2. **The pass-through calls the member on its own connection**, not through `Manager.Call`, so it sees
   the console session (a 403 asking for a new password ends it) and reaches role modules at their
   console paths. Alternative: a wider `Manager.Call`.
3. **The member shown lives in the URL's `?node=`**, and switching loads the page again, so nothing of
   one fit stays on another's, each member's page has its own link and tab, and the browser's own
   prompt guards unsaved changes.
4. **"A version the bundle knows"** is the same config schema digest as the manager's; writes are also
   refused on the manager by the last poll's digest. Alternatives: the same fit version, or minor
   version; read-only in the page alone.
5. **A write** is anything but GET and HEAD, render and parse, and dry runs; every write is audited,
   whatever the member answered ("may or may not have been applied" when unanswered, "not sent" when its
   login failed).
6. **Audit entries are written to the store asynchronously**, without changing the members' ETag; 64
   writes may wait for theirs, one more is refused (429) before it is sent; a manager whose member list
   a newer fit wrote refuses switcher writes, as it could not record them.
7. **Bounds**: 3 calls a member and 16 waiting, 503 after waiting past `timeout`, 64 in all.
8. **Headers and statuses**: fixed lists both ways; content types a browser renders become
   `application/octet-stream`; the member's 401 and its 403 for a new password become 502; streams 501.
9. **Not passed on**: the member's console users and passwords, pprof, the dashboard, `cluster`, `ai`,
   and the LLM roles, whose pages read by POST and streams, so that every poll would be an audited write.
10. **`GET /schema`** on every fit's Admin API (`schema`); an older member is drawn with the manager's
    schema, read-only in any case. Alternative: no pages for such members.
11. **The marks**: a pill before the title, the tab's title, a band, and the top bar's line in a color
    made from the member's name; read-only dims Save and refuses writes with a toast.
12. **Two fixes on the way**: queries pass on calls to `http` members, and a data race on a refused
    login's problem.

### I144 Resource groups and operations on many

- **Do**: groups: a label selector, members by name, or both; a member in any number of groups;
  group variables for I145. Operations on a group or a selection of members, run with bounded
  concurrency and answered per member: reload, rollback to the previous version, run the
  consistency check, purge cache zones by key or prefix, test connectivity. `/api/m/cluster/groups`
  and `POST /api/m/cluster/ops`.
- **Done when**: a selector's members follow a label's change; an operation on five members where
  one is down reports four done and one failed, with the error; a purge reaches every member's
  zone; under the race detector.
- **Pages**: Cluster → Groups: the list, a group's form (selector with its members previewed,
  variables), a group's page with its members' states and an operations bar; Overview's filter by
  group; each operation confirmed in a dialog naming the members, and its results per member.
- **Touches**: `cluster`, `app/workbench`, `docs`.

**Decisions pending review** (made while building it, 2026-11-06; each stands until the user rules on it;
also in decision 033 as its items 43–55):

1. **Membership is computed, never stored**: a selector's matches and the members named, from the
   labels as they are.
2. **Selectors** are Kubernetes' equality terms and `!k`, without `in` or `notin`; an empty selector
   selects no one.
3. **A name without a member is kept and shown missing**, not refused; renames follow, a removal takes
   the name out, operations skip it.
4. **Variables are plain strings**: 64 at most, names of letters, digits and `_`, values of 4 KiB, not
   secret; typed or secret values are I145's templates'.
5. **No new document version**: groups stay in version 2, whose `groups` an I149 fit keeps; members
   and groups share one ETag.
6. **"Reload" applies the running config again** (a new `POST /reload`, `config.reload`), not SIGHUP's
   reading of files or the store, which would drop what the manager changed by the API; no fallback
   for older members.
7. **Rollback goes to the latest kept version whose ETag differs** from the running one's, with
   `If-Match`, so a reload between does not count as a step back; no choice of version.
8. **Purge by prefix is new on the member** (`prefixes` in `POST /cache/purge`, `cache.prefix`):
   absolute URLs, the host in lower case, keys scanned under the shard and disk-index locks as a full
   clear scans them; operations also purge by tag and all.
9. **Operations are asynchronous** (202, followed by the page), with bounds of their own: 4 at once,
   one a member, 32 kept.
10. **Audit entries** hold each member's outcome (errors cut to 256 bytes) within a log of about 2 MiB;
    a read-only manager refuses operations (409), as it could not audit them.
11. **The test operation goes through `Manager.Call`**, on the node's session.
12. **New problem kinds** `refused` and `no_earlier_config`, so pages show no misleading hint.
13. **The pages**: Groups in the menu, a group's page that is its operations page, `#/cluster/ops` for
    any selection, every member selected at first, no cancel.

### I145 Templates: config shared by a group

- **Do**: a template owns paths in the config: whole members (`media_auth`, `tls`, `log`), and
  named elements (`routes` by name, `upstreams`, `listeners` and `services` by name; a route in a
  template must be named); its values may use variables (`${node.name}`, `${node.labels.region}`,
  a group's variables). Templates attached to groups, in an order that settles overlaps; a member's
  desired config is its own config with the owned paths taken from its templates, every other path
  left to the member. Each member's sync state: in sync, out of sync with its diff, or conflict where
  someone changed an owned path on the member. A template made from a member's config (pick paths
  from it); two members' configs compared side by side; secret values in a template kept in the
  store and masked; paths to files (`cert_file`, `token_file`) warned as the member's own. The
  manager tells each member what it owns (`PUT /node/managed`, kept in the member's store), and the
  member's own workbench marks those fields managed and asks before a local change to them.
- **Done when**: a template of routes and an upstream attached to a group renders per member with
  its variables; a member's other routes are untouched; an owned route changed on a member shows as
  a conflict on the manager and is marked on the member's page; a template from a member's config
  reproduces it; under the race detector.
- **Pages**: Cluster → Templates: the list; a template's editor (form and `.conf`/`.yaml`/`.json`
  text tabs as on the config pages, its owned paths listed, its variables), its groups, and each
  member's rendered result and sync state; **Compare** for two members; **Import from a node**.
- **Touches**: `cluster`, `admin`, `app/workbench`, `docs`.

**Decisions pending review** (made while building it, 2026-11-06; each stands until the user rules on it;
also in decision 033 as its items 68–84):

1. **What a template owns is read from its fragment**, not a list of paths: top-level members whole
   (`media_auth` whole), elements of `listeners`, `routes`, `services` and `upstreams` by name; never
   `admin` or `schema_version`. The plan's `tls` is no top-level member, so TLS goes with its listener.
   Alternatives: JSON pointers; `media_auth` by policy.
2. **Element names take no variables**, as the name is how the template finds the element.
3. **Overlaps go by `order`** (−1,000 to 1,000), then name, the later winning; each part says which
   template it overrides.
4. **Variables are strings**; one a member lacks is that member's error, never an empty string; group
   variables come only from the template's groups the member is in, a later group winning; no defaults.
5. **The in-sync hash is kept on the member**, in its record, not in the manager's document, which would
   grow with members times parts; a value the member had before the template owned it is a conflict
   (`own_value`), as applying would overwrite it.
6. **Members are told** when a page reads them and after any change to templates, groups or members;
   to the log, not the audit log.
7. **Values compare by the SHA-256 of their canonical JSON** (keys sorted, numbers as written), so `1.0`
   and `1` differ.
8. **Secrets are known by name**, the assistant's rule and push URLs (`rate_limit`'s `key` excepted),
   and are masked in every answer.
9. **New elements go at the end of a member's list**; a template removes nothing from members, and a
   part no longer owned is left as it is and listed as released.
10. **The store's document stays version 2**, as older fits keep templates as they find them.
11. **Reading sync states answers within the request**: 4 at once, 30 seconds, then members not read
    are answered pending; only summaries without values are kept.
12. **Text tabs convert through the cluster module's own endpoints**, a fragment converting without the
    config's checks; the form is the Templates page's own, as the config pages share the running
    config's draft.
13. **A member's pages ask but never refuse**, once a part until the config is read again.
14. **A member of another schema** gets a note on its sync state (I143's `compatible`), not an error.

### I146 Rollouts

- **Do**: a rollout from a template's change, a sync of out-of-sync members, or a section copied
  from one member to others. Its plan: each member's diff and the member's dry run (I140), its
  ETag taken. Its strategy: all at once, rolling by batch size or percentage with a pause, or a
  canary of the first members waiting for a person to go on. After each batch a health gate: the
  member reachable, its reload succeeded, no new drift, and if set its 5xx ratio under a threshold
  over a window. On a failed gate the rollout stops and rolls back the batch (each member to the
  version it had, by `/rollback/{n}` with `If-Match`), or all of it if asked. A member whose ETag
  changed since the plan is skipped and flagged, not overwritten. Pause, resume, abort; one rollout
  at a time per member; rollouts kept in the store, with each member's outcome and versions.
- **Done when**: in Docker with five members: a rolling rollout applies in batches; a member
  given a config that loads but fails its gate stops the rollout and is rolled back; a canary waits
  for its go; a member edited locally after the plan is skipped; a manager restarted in the middle
  shows the rollout paused where it stopped and resumes it; under the race detector.
- **Pages**: Cluster → Rollouts: a new rollout's plan (members, diffs, dry run errors linked to the
  member's page), the strategy form, the confirm dialog with the diff; a running rollout's progress
  batch by batch with each member's gate, and pause, resume, abort and roll back; the history.
- **Touches**: `cluster`, `app/workbench`, `docs`, `test/docker`.

**Decisions pending review** (made while building it, 2026-11-06; each stands until the user rules on it;
also in decision 033 as its items 85–102):

1. **Rollouts live typed in the document's `rollouts`** (still version 2); a ready member's desired
   config is kept byte for byte (encrypted with the store) until the rollout ends, then only its hash
   and the paths changed; the last 50 kept. The exact bytes keep the dry run's `new_etag` valid, after a
   restart too. Alternatives: a section of their own; the desired config made again at apply.
2. **A plan is frozen**: members get what was planned; plans expire after an hour; 4 rollouts planned or
   under way at once. Alternative: templates rendered again at each batch.
3. **Members not ready are left out with the reason**, the start going on with the ready ones; a person
   may leave out more. Alternative: no start with any member not ready.
4. **A conflict is applied over** when its member is in the plan; the diff shows each part's sync state
   and reason. Alternative: conflicting parts or members left out by themselves.
5. **Batches follow members' names**: the canary is the first ones, then batches by size, by percentage
   rounded up, or the rest at once. Alternatives: a canary chosen by hand or label.
6. **Gate defaults**: a wait of 5s; with a 5xx threshold, a window of 30s judged from 20 requests, fewer
   passing with a note; a member that counts no requests fails its batch before anything is applied.
7. **The 5xx share is a member-side change**: `/status`'s `requests`, totals that never go back
   (`status.requests`, counted when the member's config sets `metrics`), read twice by the manager.
   Alternative: a windowed endpoint on the member.
8. **An apply refused fails its batch's gate**, which is rolled back; a member skipped with 412 does not.
9. **Rollback goes to the version the member ran at plan time**, never forced; a member changed since,
   or no longer keeping that version, is `rollback_failed`; a failed gate rolls back its batch, or with
   `on_failure: all` everything applied.
10. **An apply whose answer was lost is settled by ETag** (the member's `config_etag` against the dry
    run's `new_etag`), or marked `unsure`.
11. **Pause** takes effect between batches; **abort** stops after the members being applied, cuts a
    gate's wait short and keeps what was applied (rolling back is asked apart).
12. **After a manager's restart** a running rollout is paused, a plan being made expires, a rollback under
    way fails so it can be asked again; only a person resumes.
13. **A started rollout holds its members** through paused and waiting until it ends; plans hold nothing.
14. **Checkpoints are written without changing the members' ETag** (`appendAudit`); a failed write is
    logged and the rollout goes on.
15. **A goroutine a rollout**, `max_concurrency` members of a batch at once; tests use millisecond
    timings, not an injected clock.
16. **API times are milliseconds** (`pause_ms`, `wait_ms`, `window_ms`).
17. **The pages**: Rollouts after Templates; New rollout filled from its URL's query; dry-run errors link
    to the member's page, its own pages and its rendered result.
18. **`admin` on a member without a store** is accepted back unchanged by `PUT /config`; templates and
    copies never own `admin`, so a rollout never changes it.

### I147 Drift and alerts

- **Do**: drift checked on every poll: each member against its templates and its own consistency
  check; a fix is a rollout of that member (I146), never applied unasked. Alert rules: member down
  for a while, drift, a rollout failed, a certificate expiring within days, upstream backends down,
  reloads failing; sent to webhooks (generic JSON, with a template for chat services), with a
  repeat interval and a silence, the webhook's secret in the store; alerts kept, bounded.
- **Done when**: each rule fires once its condition has lasted its time and resolves after;
  silences hold; a webhook that times out does not delay the poller; under the race detector.
- **Pages**: Cluster → Drift and alerts: members out of sync with their diffs and a **Fix** that
  opens a rollout's plan; the alert rules' form, webhooks, active and past alerts, silences; a
  badge on the Cluster menu while alerts are active.
- **Touches**: `cluster`, `app/workbench`, `docs`.

**Decisions pending review** (made while building it, 2026-11-06; each stands until the user rules on it;
also in decision 033 as its items 103–117):

1. **Alerts live in a store section of their own**, `cluster.alerts`, not in the document, which an
   older fit would write without them; version 3 would make older fits read-only.
2. **Drift is read again only when it can have changed**: the config's ETag differs, no state yet, or
   the state is older than 10 minutes.
3. **Drift reads tell members nothing** of what they own; rollouts and page reads do.
4. **Consistency drift is fixed by a confirmed Reload** of the member, not a rollout; Fix plans a
   rollout and never starts it.
5. **`member_down` covers down and error**; `rollout_failed` is one alert a failed rollout, until rolled
   back or 24 hours; `reload_failed` counts from the manager's first read, until a success, a restart or
   an hour.
6. **Default rules exist** while none is saved; without webhooks they show only on the pages and the
   badge.
7. **A rule's webhooks**: an empty list is every webhook; a webhook a rule names cannot be deleted
   (409); a rename follows into rules.
8. **Resolved notifications are sent** whenever the firing one was, while silenced too, and to every
   webhook when the rule was deleted, so receivers close what they opened.
9. **Failed sends are not retried**: a queue of 64 dropping the oldest, a timeout of 10s (1s to 60s), a
   chat service's 200 with an error code counted as failed.
10. **Templates are plain `${...}` replacement**, not Go templates, so nothing user-written runs;
    custom bodies are checked as JSON when saved.
11. **Webhook URLs are secrets** (chat tokens live in them), https or loopback http only.
12. **Bounds**: silences up to 30 days ahead, kept 7 days after they end, 256 at most; 2,048 alerts
    firing or pending and 500 resolved; pending alerts are not kept across a restart.
13. **Firing and resolving go to the log, not the audit log**, as no person acted; every method takes
    the user, for I148's roles.
14. **Alerts have an ETag of their own**, apart from the members'.
15. **The badge** is a small addition to the core (`badge()` on a module), as no extension point drew
    one.

### I148 Users, roles and the audit log

- **Do**: the manager's console users get roles per group: `viewer` (read), `operator` (operations
  and rollouts of existing templates), `admin` (members, credentials, templates, groups), checked on
  every endpoint and every switcher write; consistent with the assistant's levels (I126) when both
  are on. The audit log of every cluster action: user, time, members, operation, the diff's digest,
  outcome and the versions before and after, in the store, bounded; exported as JSON lines.
- **Done when**: each role is refused what it may not do, in the API and with the controls hidden in
  the pages; every action of I141–I147 leaves its entry; the log holds across a restart within its
  bound; under the race detector.
- **Pages**: Cluster → Users and audit: users' roles per group; the audit log filtered by user,
  member, operation and time, each entry opening its diff.
- **Touches**: `cluster`, `login`, `app/workbench`, `docs`.

### I149 Members behind NAT, linking out

- **Do**: `admin.cluster_member` on a member: the manager's URL, and an enrollment token made on
  the manager, used once for a credential of its own; the member links out to the manager over a
  WebSocket on fib's client, reconnecting with backoff, and the manager reaches that member's API
  over the link, as it reaches others over HTTP. The manager's certificate pinned by the member at
  enrollment; a member removed on the manager loses its credential.
- **Done when**: a member with no open port is managed like the others through everything above;
  the link survives the manager's restart and a network cut; a used or expired enrollment token is
  refused; under the race detector.
- **Pages**: Nodes gains **Add by enrollment**: a token made with its lifetime and the config line
  to paste on the member, and the member shown as linked or waiting.
- **Touches**: `cluster`, `config`, `admin`, `app/workbench`, `schema`, `docs`.

**Decisions pending review** (made while building it, 2026-11-06; each stands until the user rules on it;
also in decision 033 as its items 28–42):

1. **Links land on a listener of their own** (`link_address`), not on the console, whose 32
   connections, a quarter a client address, would cap the members behind one NAT.
2. **The manager's link certificate** is self-signed, kept in the store, never renewed, and pinned by
   the config line, so no CA is needed.
3. **Tokens and credentials** are `internal/linktoken` secrets of their own prefixes (`fite1.`,
   `fitc1.`), kept as hashes and proved both ways under the label `fit cluster link v1`.
4. **A member is added when its token is made**, shown as waiting; the token is spent before the
   credential reaches the member. Alternative: an enrollment in two steps the member acknowledges.
5. **The link carries the Admin API in process**, with its token's authority, without the dashboard or
   pprof, and refused while a console password is temporary; the member needs no console.
   Alternative: the member forwarding to its own console or Admin API over loopback.
6. **The member keeps its credential** in its store or beside its config (`fit.cluster-member`, 0600),
   used only for the same fingerprint; another token in its config enrolls it again.
7. **Final refusals stop the member** (used, expired, unknown, a bad proof, revoked; the last three also
   forget the credential) until a new token and a restart; `busy` and `replaced` retry.
8. **`admin.cluster_member` is read at start only**, as the rest of `admin`.
9. **Bounds**: pings every 15s, dead after 45s, backoff from 1s to a minute with jitter, a handshake of
   10s, 16 calls a link, 2 workers and 16 queued on the member, request bodies within
   `max_response_bytes`.
10. **The document is version 2**, so an older manager cannot drop links by writing over it.
11. **Tokens live 24 hours** by default, from a minute to 30 days.
12. **One link a credential**: a newer link replaces the older, so two fits sharing one take turns.
13. **`Manager.Call`** is the one way to call a member (HTTP or link), for I143 on.
14. **I142's pages** changed in a few lines beside its extension points, which did not cover access
    `link` in the columns and the edit form.
15. **fib's WebSocket gaps** (A52, a forced close; A53, send backpressure) have stopgaps as the LLM mesh's:
    connections found by remote address, queues bounded by calls in flight times their size.

### I150 Metrics, logs and certificates across the cluster

- **Do**: series kept by the manager from each poll, bounded per member: requests per second, 4xx
  and 5xx, connections, bytes, reloads; summed per group. Logs tailed from several members at once
  through fit's log ring (I125), merged by time. The certificate inventory: every member's
  certificates with names, issuer and expiry. `fit cluster` in the CLI (nodes, state, groups, diff,
  rollout start/status/abort) through the manager's Admin API. The assistant's cluster tools are
  I153.
- **Done when**: the series match the members' own metrics; a merged tail follows three members;
  a certificate about to expire on one member is listed first; the CLI drives a rollout end to end;
  under the race detector.
- **Pages**: Cluster → Metrics (charts per member and group), Logs (members chosen, filters,
  follow), Certificates (the inventory, sorted by expiry); Overview's sparklines.
- **Touches**: `cluster`, `cli`, `app/workbench`, `docs`.

### I151 Upgrading members' binaries (a question, not a plan)

Upgrading fit itself across members (a release downloaded, its signature checked against a pinned
key, the binary replaced and fit restarted with its listening sockets handed on, rolled out as in
I146) needs a graceful restart fit does not have yet. Decision 033 records whether it is taken up,
or left to packages, containers and orchestrators, which the overview's version column already
serves.

### Open questions for review (2026-11-05)

1. Access to members: the console's user name and password (as asked) and the Admin API's token,
   both offered, password first. Keep both?
2. Credentials need `admin.store` on the manager. Acceptable, or also allow a plain file?
3. I143 opens a member's pages with the manager's bundle (one UI, read-only on unknown versions)
   rather than the member's own bundle proxied whole. Agreed?
4. Templates own whole members and named elements; unnamed routes cannot be shared. Enough?
5. No unattended sync: drift is reported, a person rolls out the fix. Agreed, or an opt-in
   `auto_sync` per group?
6. I149 (members linking out) before or after I145–I146? Until then the tunnel reaches them.
7. Binary upgrades (I151): in scope or not?

## Logging: files, rotation and retention

fit's own log and the access log both write to files, and today a file is opened once and appended to
for as long as fit runs: nothing rotates it, nothing removes it, and changing its path or format takes
a restart. A file therefore grows until the disk fills, operators rotate it outside fit, and the
workbench shows neither the files nor their cost. This track gives a file output a rotation schedule
and a retention policy, shared by the two logs: rotate by size, by time at the boundary of the log's
time zone, or whichever comes first, compress what is rotated, and remove old files past a maximum
age, a maximum number and a maximum total size. Each log writes the time of its lines in its own
zone — local by default, `utc` or an IANA zone when the config says — and its files turn at that
zone's boundaries. The workbench's Logs page shows the files, edits the settings in a form, and
offers a rotate and a cleanup that a person runs. Decision 034 records the choices below.

Principles, in the order they bind:

- **One writer per file, off fib's workers.** One file sink for each output path, with a bounded queue
  and a writer goroutine of its own, shared by every logger that writes there and by the access log.
  A write copies its bytes and returns: a full queue refuses them and counts them, and no logger,
  handler or request path touches the disk (rule 1). Rotation, retention and compression run on the
  writer's goroutine and `internal/filepool`, within a timeout (rule 4).
- **No cost on the request path.** Everything this adds runs on the writer's goroutine and its pool:
  the enqueue, the size and time checks, a rotation, a sweep, a compression. The access log's line is
  formatted where it is today, allocation-free, and the queue copies into pooled buffers, so a write
  allocates nothing and takes no lock a worker waits on; the packages' benchmarks, before and after on
  the benchmark host (rule 21), show no regression.
- **A rotation loses nothing.** Queued lines are written to a file before it is renamed and a new one
  opened; a line is in exactly one file, never split, dropped or written twice, however the settings
  combine. Compression writes a temporary file and renames it, and a failure keeps the plain file.
- **Local time by default, one clock per file.** A log's lines and its files' time rotation use its
  `time_zone`: `local` when left out, `utc`, or an IANA zone such as `Asia/Shanghai`, which fit
  carries (`time/tzdata`) so that a container without zoneinfo works. So `24h` turns at local midnight
  by default and at UTC midnight with `time_zone: "utc"`, and every format of a log writes the same
  zone. Two logs sharing a file must write the same zone and turn at the same boundary, or the config
  is refused.
- **Only the sink's own files.** A sweep removes only the names the sink makes from its path, and
  never the file being written; another file in the directory is never touched.
- **Applied where it can be.** A reload changes `time_zone`, rotation, retention and compression in
  place, the sink keeping the file it has; a file's path or a logger's format still takes a restart,
  and the workbench says which is which. The Consistency check (I121) stops calling `time_zone` and
  the rotation, retention and compression that now apply live a restart-only difference.
- **Bounded everything.** The queue's bytes, the files a sweep reads and the compressions at once have
  limits; a slow disk costs memory up to the queue and no more, and what is dropped is counted and
  reported.

### I155 Decision 034; a file sink: rotation by size and time, retention, compression

- **Do**: decision 034. Package `logs`: a file sink on a path, a queue of 1 MiB and a writer goroutine;
  `Write` copies into a pooled buffer and enqueues, refuses and counts past the queue. Rotation, before
  a write that would take the file past `max_bytes` — a line larger than that gets a file of its own —
  or on the first write after a period later than the file's, periods in the log's `time_zone`,
  whichever comes first; so `24h` turns at local midnight by default and at UTC midnight with
  `time_zone: "utc"`. A rotated file is named `<path>.<yyyy-mm-ddThh-mm-ss.mmm>` in UTC, the time of
  the rotation, whatever the log's zone, with `-1`, `-2` when that name is taken and `.gz` when
  compressed by `internal/filepool` (a temporary file, renamed; a failure keeps the plain file, and a
  sweep skips what is being compressed). Retention, swept at open, after every rotation and every ten
  minutes: the files older than `max_age`, then those beyond `max_files`, then the oldest until the
  rotated files total at most `max_total_bytes`; only names the sink makes, never the open file.
  `config.Log` gains `time_zone` — `local` when left out, `utc`, or an IANA zone — and every logger's
  lines in every format are written in it, the engine's included, through a wrapper of fib's handler
  that puts the record's time in the zone. `config.Log` and `config.Logger` gain `rotate`: `log.rotate`
  for `log.output`, a logger's own for an output of its own; `max_bytes` 1 GiB, `max_age` 720h and
  `max_total_bytes` 1 GiB when left out, `compress` on, `interval` and `max_files` off, and a limit set
  to 0 off — so `"rotate": {}` is a bounded policy and no `rotate` keeps today's single file, with a
  line at startup saying it grows without bound. One manager (`logs.Files`, made by `fit run` and
  handed to `logs.New`) opens one sink per path and shares it among the loggers writing there, and is
  where a reload changes `time_zone`, rotation, retention and compression in place; a logger's output
  path is still fixed at start.
- **Done when**: a logger's file rotates at `max_bytes`, at each `interval` boundary and at whichever
  comes first; with the default `time_zone` a `24h` period turns at local midnight, with
  `time_zone: "utc"` at UTC midnight and with an IANA zone at that zone's midnight, `1h` at the top of
  the hour in the zone, a fake clock proving each; lines of every format carry the chosen zone, the
  engine's included; across a hundred rotations every line is in exactly one file, none split, lost or
  duplicated, and a line larger than `max_bytes` is in a file of its own; files are removed by age, by
  number and by total size, oldest first, the open file never, a file the sink did not write never;
  with `compress` a rotated file is a gzip whose text is the lines, its plain file gone, its compressed
  size what `max_total_bytes` counts, and a failed compression keeps the plain file and logs a line; a
  full queue refuses and counts, a slow disk (filepool's hook) holds up no logger call, and drops are
  reported once the disk catches up; a reload changes `time_zone` and every rotation, retention and
  compression field in place, while a path or format change still warns of a restart; a restart sweeps
  before its first line; `fit validate` refuses a negative limit, an `interval` under 1s, an unknown
  `time_zone`, and two loggers naming one path with different `rotate`; the schema's descriptions and
  the docs cover every field; the log's benchmark and the sink's write, before and after with benchstat
  on the benchmark host (rule 21), show no regression and no allocation a line while the queue has
  room, the zone costing one `Time.In` a message or a second, never a line; under the race detector.
- **Touches**: `logs`, `internal/filepool`, `config`, `cli`, `admin`, `schema`, `docs`.

### I156 The access log's file through the same sink; one file, one sink

- **Do**: `access_log.rotate`, the same `config.Rotate`, and `access_log.time_zone`; `observe.AccessLog`
  writes its batches to the sink the manager keeps for its path instead of its own file, and its
  `Close` writes out its buffers before the sink's. Its lines are written in its `time_zone` — `local`
  when left out, `utc`, or an IANA zone — and its file's time rotation turns at that zone's boundaries:
  `json` and `text` used to write UTC and `line` local, and now every format follows the setting,
  local by default. Loggers of `log` and the access log naming one path share the one sink, and a
  config that gives a shared path two different `rotate` settings or two `time_zone`s is refused, so
  two writers cannot rotate one file under each other. A reload updates the access log's `time_zone`,
  rotation, retention and compression in place; its path and format still take a restart, as they do
  now. The batches the sink refuses are counted as the lines the access log drops, as its own buffer's
  drops are. The per-second time stamp cache stays: a chosen zone costs one conversion a second, not
  one a line.
- **Done when**: the access log's file rotates by size and by time and is cleaned up as fit's own
  log's is, and no line is lost or duplicated across a rotation; with the default `time_zone` a `24h`
  period turns at local midnight, with `utc` at UTC midnight, with an IANA zone at that zone's, a fake
  clock proving each; `json`, `text` and `line` all write local time by default and the zone that is
  set — `json` and `text` as RFC 3339 with its offset, `line` its wall clock — one conversion a second
  at most; the two logs on one path write through one sink and each line is whole; `fit validate`
  refuses one path with two different `rotate` settings or two `time_zone`s; a slow disk drops and
  counts lines as the buffer does; a reload changes the access log's `time_zone`, rotation, retention
  and compression without a restart; `Close` writes out what both logs buffered; the access log's
  benchmark, before and after with benchstat on the benchmark host (rule 21), shows no significant
  change and no more allocation a line on the request path; under the race detector.
- **Touches**: `observe`, `logs`, `config`, `admin`, `schema`, `docs`.

### I157 The workbench's Logs page

- **Do**: a first-level **Logs** page: for each file output its path, the loggers writing there and the
  format, the log's `time_zone` and the rotation and retention settings, the current and rotated files
  with their sizes and ages, the totals, the lines dropped and the last write error; the settings
  edited in the page's form and its `.conf`, `.yaml` and `.json` tabs, writing `log.time_zone`,
  `log.rotate`, `log.loggers.<name>.rotate` or `access_log.time_zone` and `access_log.rotate`, and
  applied through the one apply path as any page's edit is (I120), `time_zone`, rotation, retention and
  compression taking effect without a restart; a warning for an output with no `rotate`, one past its
  retention and one whose last write failed. **Rotate now** and **Clean up now**, the second previewing
  the files it would remove and confirmed in a dialog that names them; both through the console's own
  endpoints, behind its login and CSRF, and recorded in the `admin` logger. `GET /api/logs` lists the
  outputs and their files, bounded; `POST /api/logs/rotate` and `POST /api/logs/cleanup` take an
  output's `path`, and the second `dry_run`; a `path` that is not one fit opened is refused; the
  listing is read on `internal/filepool`, at most 1,000 files an output, and its answer is bounded;
  nothing outside the configured outputs' own names is listed or removed. Links to the `log` and
  `access_log` pages and to Consistency (I121).
- **Done when**: the menu shows Logs, and the page lists each output's files, sizes, ages, totals and
  its effective zone in English and Chinese; a form edit of `max_age`, `max_files`, `max_total_bytes`,
  `max_bytes`, `interval`, `compress` or `time_zone` applies through the page's apply path and takes
  effect without a restart, while a path or format change says it needs one; **Rotate now** rotates and
  the page shows the new file and the rotated one; **Clean up now** previews the files and, confirmed,
  removes exactly them, cancelling removes nothing, and a name the sink did not make is never listed or
  removed; a failed write, a compression failure and dropped lines are shown; an answer past the
  listing bounds is truncated and says so; only a logged-in user with the page may act, CSRF is
  checked, and each action is in the `admin` logger; a browser test passes in English and Chinese;
  under the race detector where Go changes.
- **Touches**: `app/workbench`, `admin`, `logs`, `observe`, `docs`.

## Agent and tunnel, continued

### I159 Issued tokens for the tunnel and the LLM mesh

- **Why**: the security review of 2026-11-06 found that both features authenticate their links with a
  password shared by every member: the tunnel's hub `password` (and `body_password`), and the LLM
  mesh's listener `password` or the role passwords added in f1628565. So any member that holds a
  role's password can pose as the listener to another member of that role, or say hello under another
  member's ID and receive its share of calls, prompts included; and a peer that answers a dial, or
  records a link, gets what it needs to guess the password offline: an HMAC over a challenge it chose,
  or PBKDF2 at 10,000 rounds under a salt every install shares. The links themselves are now TLS or
  QUIC off loopback (decided the same day), which keeps out an attacker on the path; what is left is
  that the secret is shared, long-lived and guessable.
- **Do**: decision 035. Tokens a listener issues, in place of the shared password:
  - **Not bound to a role.** A valid token is accepted from a node, a proxy or a hub alike; what a link
    may do still follows from the role it says hello as and the listener's rules for that role
    (`allow_hub_manage`, `allow_hub_services`, `allow_proxy_services`).
  - **Each token its own credential.** A token has an ID, a name, a creation time, an optional expiry
    and an optional note; the listener keeps only a hash of its secret. A member's ID is bound to the
    token it first registered with, so another token cannot take it over.
  - **Lifetimes.** A token may expire at a set time or after a duration; an expired token is refused
    at the next handshake, and the links it holds are closed within a bound.
  - **Management.** The listener's pages, its Admin API module and the CLI generate a token (shown
    once), list tokens with their last use and the links using them, change a token's expiry, and
    revoke one, which closes its links at once. Every change is in the `admin` logger.
  - **The handshake.** A token's secret is high-entropy (at least 128 bits, generated), so an HMAC
    over a challenge leaks nothing worth guessing. Both sides add a nonce of their own, the listener
    proves the token back, and the proofs bind both nonces and the direction. `body_password` keys
    come from the token by HKDF, not PBKDF2 over a shared salt.
  - **The shared password.** It stays accepted for one release, for an upgrade, and is then refused;
    the pages warn while any member still uses it.
- **Done when**: a node, a proxy and a hub link to a listener with tokens over TLS or QUIC; a token
  is accepted from any role and the role's rules still apply; one member's token cannot register as
  another member's ID; an expired token is refused and its links close within the bound; a revoked
  token's links close at once; generate, list, change expiry and revoke work from the pages, the Admin
  API and the CLI, and each is logged; a token's secret is shown once and stored only as a hash; the
  handshake's proofs bind both nonces and the direction, and a reflected or replayed proof is refused;
  the shared password still links, with a warning; the docs (English and Chinese) explain tokens and
  the upgrade; under the race detector.
- **Touches**: `tunnel/...`, `llm/...`, `config`, `admin`, `app`, `cli`, `schema`, `docs`.

**Decisions pending review** (made while building it, 2026-11-06; each stands until the user rules on it):

1. **The proof.** SCRAM-like: the listener stores SHA-256(HMAC(secret)), so a copy of the config
   cannot link; the listener's proof key and the body key come from that hash by HKDF, so whoever
   holds the hash can pose as the listener or read sealed tcp links. Alternatives: a plain HMAC under
   the stored key (the config becomes a credential); a key pair a member.
2. **A member's binding is kept** in the token's `members`, by a config edit on first use, so it
   survives restarts; moving a member to another token means revoking the old one first. Alternatives:
   in memory only; an explicit rebind.
3. **A member's identity** is `node/<name>` or `proxy/<name>` in the tunnel and the role's ID in the
   LLM mesh, as each already keys its peers.
4. **The tunnel's `challenge` round trip** before registration, so the hub adds a nonce whether or
   not bodies are sealed. Alternative: reuse the sealed hello's random bytes, which exist only with
   `body_password`.
5. **Sealing.** `body_password` still turns it on; keys are HKDF of the token's body key and
   `body_password`, and the hello names the token's ID in clear. Alternative: a token alone turns it on.
6. **Where tokens sit.** At the role (`tunnel.hub.tokens`, `llm.<role>.tokens`), not under `listen`,
   so editing the listener from the pages keeps them.
7. **A listener with no password** is allowed and takes only tokens, with a warning while it has
   none, so a role can be set up before its first token.
8. **Expiry** is checked by a timer at the next expiry, at least once a minute; the docs give that
   minute as the bound.
9. **Last use and live links** are kept at run time only, not written to the config on each handshake.
10. **Bounds**: 1,024 tokens a listener, 256 members a token; lifetimes as Go durations or `Nd`.
11. **Removing the password**: `clear_password` (tunnel hub settings) and `clear_passwords` (an LLM
    listener's edit) in the pages' APIs.

### I160 The tunnel's and the LLM mesh's TLS, after review

- **Why**: an independent review of the TLS added on 2026-11-06 (fit `3622aee1`, `6bd2c39c`,
  `fc685c08`) found the pinning and handshakes sound, but:
  - **A fragment in an endpoint's address overrides its pin (Medium).** `checkTunnelEndpoint`
    (`config/tunnel.go`) refuses a query in the address but not a `#fragment`; `uplink.Dials` appends
    `?pin=…` to the address as a string, and `TLSClientConfig`, quic's and ws's dials cut it at the
    first `?`, which may be inside the fragment. So `tls://hub:7000#?pin=<other>&`, with the hub's real
    pin configured, opens to the other certificate; `quic://hub:7000#?insecure=1&` turns quic's check
    off; `#?ca=/path&` swaps the CAs; and `tls://hub:443#?x=` drops the pin for the system roots. The
    hub's and node's pages pass an endpoint's address through (`EndpointReq.address()`), so whoever
    writes an endpoint, or hands an operator one to paste, can use it. Proven on tls, wss and quic.
  - **`quic_insecure` is accepted off loopback (Low)**, the unauthenticated-hub case the new rule
    refuses for tcp and ws.
  - **Not flaws, but to fix with them:** without `admin.store` a hub's self-signed certificate is
    made anew at each start, which breaks its peers' pins; an LLM listener's `cert_file` is read only
    when it starts, so a key rotated on disk needs a restart.
- **Do**: pass a link's certificate options (pin, CAs, insecure) to the transports' Dial as
  `ClientOptions`, never in the address; build and parse dial URLs with `net/url`; refuse a fragment,
  userinfo, `?` or `#` in an endpoint's address in validation and in the pages' API; refuse
  `quic_insecure` off loopback unless `body_password` is set; keep a self-signed certificate in the
  config's directory (0600) when there is no store; reload an LLM listener's certificate when its
  files change, as the tunnel hub's listeners do.
- **Done when**: the review's fragment attacks on tls, wss and quic fail validation and, given to Dial
  directly, cannot change how the hub's certificate is checked; `quic_insecure` off loopback is refused
  with a clear error; a hub without a store keeps its fingerprint across restarts; an LLM listener picks
  up a rotated certificate without a restart; the docs (English and Chinese) say so; under the race
  detector.
- **Touches**: `config`, `tunnel/internal/transport/...`, `tunnel/internal/uplink`, `tunnel/hub`,
  `tunnel/node`, `llm/...`, `docs`.

**Decisions pending review** (made while building it, 2026-11-06; each stands until the user rules on it):

1. **`quic_insecure` beside `pin` or `ca_file`** loads off loopback: the pin or the CAs decide the
   check, so it does nothing there. Alternative: refuse it with any quic endpoint off loopback.
2. **Disabled endpoints count** for the `quic_insecure` check, as for plain tcp and ws endpoints.
3. **The kept certificate's file**: `<config path without extension>.<role id>.pem` beside the
   first `-c` path, or `<dir>/fit.<role id>.pem` for a config directory; nothing for stdin or with a
   store. Alternatives: a fixed name; a state directory of its own.
4. **Without a store, only the file** holds the certificate, not the running config, which then
   matches the file on disk. Alternative: both.
5. **A kept file with no certificate in it** fails the Apply and is left alone, so the hub does
   not start, rather than make a new certificate and change the pin silently.
6. **A kept file that cannot be written** (a read-only mount) falls back to the old behaviour, with
   a warning that the certificate changes at the next start. Alternative: refuse to start.
7. **LLM `self_signed` listeners too** keep their certificate the same way (`fit.llm.<role>.pem`),
   though the plan named the hub only.
8. **An LLM listener's rotated certificate is swapped in place**: open links and calls survive, at
   the cost of fib's handshake-pool hop for `GetCertificate` on listeners with `cert_file`.
   Alternative: reopen the listener, as the tunnel hub does, which cuts every inbound link.
9. **A reload whose files fail to load** is logged, not returned, and `-watch-certs` tries again
   when they change.
10. **Files are watched through `-watch-certs`** (`app.Info.CertFiles` into the runtime graph), not
    a watcher a role; the tunnel hub's listener files, read only on Apply until now, are watched too.
11. **The pages' API runs the whole `config.CheckTunnelEndpoint`**, answering 400 with the config's
    message.

### I161 Unauthenticated connections and file names, after review

- **Why**: the same review found:
  - **The Admin API's bound covers reads only (Medium).** `limitConn` caps read deadlines at
    accept + `unauthTimeout` (`admin/server.go`), but net/http's write deadline stays `writeTimeout` and
    is set again for each pipelined request; a tokenless client that pipelines ~70 dashboard requests
    and reads a few bytes a minute holds a slot about 70 minutes (proven at 49 times the bound).
  - **The bound limits a connection's life, not an attacker's share (Medium, design).** A client that
    reconnects every 4.9s, from a few addresses or from loopback, which is not counted, still holds
    nearly every slot of the Admin API or a console.
  - **fastcgi does not refuse a backslash (Medium, Windows).** It checks scripts with `Hidden`,
    `HasShortName` and `Alias`, which split on `/` only, and not with `dotfile.Scan`; so
    `/a%5C.git%5Cx.php` runs `root\a\.git\x.php`, and `a\GIT~1\` and `a\CON.php` pass too. Proven
    over HTTP.
  - **An extension in another case is served as source (Medium, Windows NTFS and macOS APFS).** A
    case-sensitive `\.php$` route misses `/x.PHP`, which falls through to `static` on a shared root and
    opens `x.php`: the PHP source, as text. The nginx migration doc maps `location ~ \.php$` that way.
- **Do**: close an unauthenticated Admin API connection at accept + `unauthTimeout` whatever it is
  doing, as the console's timer does, and cap its write deadline too; send `Connection: close` on the
  dashboard's answers to an unauthenticated connection; when the slots are full, close the oldest
  unauthenticated connection rather than refuse the new one, and keep a share of `max_conns` for
  authenticated ones, for the Admin API and the consoles. fastcgi uses `dotfile.Scan` and answers 400
  on a backslash, in its plain path and in `try_files`. On a case-insensitive file system, `static`
  refuses a request whose extension matches fastcgi's `split_path` extension in any case, or whose name
  differs from the name on disk; the migration docs use `(?i)\.php$`.
- **Done when**: the review's pipelining client is closed at the bound; a reconnecting attacker cannot
  keep an authenticated admin out; the backslash paths are refused by fastcgi on every OS; `/x.PHP` on
  a shared root does not serve source on Windows or macOS; under the race detector.
- **Touches**: `admin`, `app/console`, `handler/fastcgi`, `handler/static`, `internal/dotfile`, `docs`.

**Decisions pending review** (made while building it, 2026-11-06; each stands until the user rules on it):

1. **Which unauthenticated connection a new one closes.** The oldest of the client that holds the
   most unauthenticated connections, loopback and Unix-socket clients counting as one client; the
   plan said the oldest. So an attacker reconnecting from other addresses closes its own
   connections, not a new admin's. Alternatives: plain oldest first; a minimum age before one may be
   closed.
2. **The authenticated share.** A quarter of `max_conns` (16 → 12 for unauthenticated, 32 → 24),
   with at least one slot always open to unauthenticated connections, as the existing per-client
   quarter. Alternative: a config field of its own.
3. **How `static` learns fastcgi's `split_path`.** A process-wide list fastcgi adds its extensions
   to when set up (`dotfile.AddScripts`), never shrunk, so a reload can only widen the refusal.
   Alternative: a `script_extensions` param on `static`.
4. **How `static` reads a name as it is on disk.** `FindFirstFile` on Windows, a directory read
   elsewhere: O(entries) a path segment, on cache misses on case-insensitive roots only. A name in
   another Unicode normalization than the disk's (NFC against NFD on macOS) is refused.
5. **The answer to a case variant.** 404, as for a hidden dotfile. Alternative: 400.
6. **fastcgi's `try_files`.** Only a backslash, NUL or `..` answers 400; Windows aliases and
   dotfiles are still skipped over as before.
7. **The nginx converter.** It writes a `~` regexp containing `\.php` as `(?i)` and warns, beside
   the migration docs. Alternative: the docs only.

### I162 One address, one server

- **Why**: fib's engines listen with SO_REUSEPORT, so that each poller accepts on a socket of its
  own, and the kernel lets any other socket of the same user that sets it too listen on the same
  address, without an error, splitting the connections between the two. The TestWebProxy flake of
  2026-11-06 showed it: a web proxy and a console forced onto one port both listened, and the wrong
  server answered 13 of 20 requests. Config validation refused some clashes within one role (two
  listeners, a forward and a web proxy) but not across roles, and the consoles, the LLM roles'
  listeners and API, and the tunnel hub's listeners, console socket, forwards and web proxies bound
  without the plain-bind check `listener`, `streaming`, `webrtc/turn` and `webrtc/media` made.
- **Do**: one check in `config` over every address the config has fit listen on (listeners, the
  ones HTTPS by default makes, `admin`, `admin.console`, the `llm` roles' consoles, listeners and
  API, the tunnel roles' consoles, the hub's listeners and console socket, the proxy's forwards and
  web proxies, streaming's four ports, TURN's three lists, `webrtc.media` and `webrtc.proxy`),
  addresses normalized, reported at the later of two with the earlier named; the per-role
  duplicate checks it replaces go. One package, `internal/bindcheck`, for the plain-bind check at
  bind time, used by every binding fit adds. Decide what to do about another process.
- **Done when**: every two kinds of address clash in validation, with tests; each role refuses an
  address another fib engine holds with SO_REUSEPORT, with tests that fail without the check; docs
  in English and Chinese; the race tests and CI pass.
- **Touches**: `config`, `internal/bindcheck`, `internal/bindtest`, `listener`, `app/console`,
  `llm`, `tunnel`, `streaming`, `webrtc`, `docs`; fib's `webrtc/turn/turnserver` (A70).

**Decisions pending review** (made while building it, 2026-11-06; each stands until the user rules on it):

1. **Another process: a plain bind at bind time, fib's ReusePort kept.** fib's own answer for an
   engine without `ReusePort` is the same probe (`claimAddress` in its `internal/listener` binds
   without SO_REUSEPORT and closes before the pollers bind), so neither is atomic: a socket that
   takes the address in the microseconds between is not caught, and a socket of the same user that
   sets SO_REUSEPORT itself can join afterwards, which is that socket's choice. Turning `ReusePort`
   off is engine-wide and would put every UDP address (HTTP/3, the tunnel's QUIC, TURN, WebRTC, SRT)
   on one poller, and end the zero-gap reload of a listener replaced on its own address. So no fib
   API was requested for fit's bindings. Alternative: a fib `Binding.Exclusive` that keeps the
   claiming socket (SO_REUSEPORT set after its bind), atomic, if the window ever matters.
2. **Wildcard and specific addresses on one port clash.** `:8080` and `127.0.0.1:8080` are refused,
   though the kernel would route the specific address's connections to its own socket rather than
   split them: on Linux the bind-time check refuses such a pair anyway (a plain bind of one
   conflicts with a listening socket on the other), so validation says so first and on every OS
   alike. Two addresses of one TURN `listen` list that overlap are refused too. Alternative: exact
   matches only.
3. **Normalization.** An empty host, `0.0.0.0`, `::` and `::ffff:0.0.0.0` are one wildcard, so
   `0.0.0.0:80` and `[::1]:80` clash though Linux would let them both bind (an over-refusal);
   `localhost` is `127.0.0.1`, as Go resolves it to listen; IPs compare in canonical form, zones
   included; other host names compare in lower case and are not resolved at load, so two names of
   one IP are caught only when they bind.
4. **One check, new messages.** The per-role checks (listeners, streaming's ports, the tunnel hub's
   listeners, the tunnel proxy's forwards and web proxies, TURN's lists, `webrtc.proxy` against
   `webrtc.media`, streaming's WebRTC port against the WebRTC roles) are replaced; the messages now
   name the earlier part by its config path or listener name, and the transport. Parts are
   ordered listeners, `admin`, `llm`, `tunnel`, `streaming`, `webrtc`; the error is at the later.
5. **What validation does not see.** Listeners that providers (Docker, Kubernetes Gateways) make at
   run time, and a role's address its pages change, are checked at bind time only. TURN's relay
   range and `webrtc.proxy`'s source port range are not listen addresses and are not checked against
   the listeners; a relay now skips a port of the range another socket holds (A70).
6. **A move between kinds in one reload.** A reload that moves an address from one kind of binding
   to another (a web proxy's to a forward, a role console's to another role's) can fail the new
   binding's check if the old one is still open when it binds; it is reported, the rest runs, and
   the next apply binds it. Each kind closes its own old bindings before binding new ones, as
   before.
7. **fib's TURN relays (A70).** Found here: with `port_min`/`port_max`, two allocations could share
   one relayed port. Fixed in fib (`856ad5b`) with a set of reserved ports and the same plain bind;
   fit moved to that commit, which also brings `925f292` and `0ddaba3` (media/hls) from another
   session.

## Performance

Work on throughput and latency that no feature track owns.

### I158 The cost of fib v1.7.1's HTTP/2 request checks

- **Why**: on the benchmark host on 2026-11-05, fib v1.7.1, the security release, against `5c0e5b9`,
  the commit before it, cost fit's `fixed` and `static` scenarios over h2c 2.4% and 1.7% of their
  requests a second (`bench/ab.sh`, 5 rounds, verdict SLOWER), and the same again in the A/B of fit's
  security round (`fixed` −2.5%, `static` −1.8%); HTTP/1.1 and `proxy` were within noise. fib's
  `BenchmarkH2ServerStatic/1KB` was 0.99% slower (p=0.004) and `BenchmarkH2ServerGET` 1% (p=0.05).
  fit's own changes in that round were within noise. The likely costs are fib's `dabe569`, which checks
  each request's `:method`, `:scheme`, `:authority` and `:path` byte by byte, and `46af102`, which counts
  the bytes of bodies collected whole on each DATA frame.
- **Do**: in fib, profile `BenchmarkH2ServerGET` and `BenchmarkH2ServerStatic` on the benchmark host
  at `5c0e5b9` and at the current commit, and compare the profiles. Make the checks cost what they must
  and no more: check the pseudo-header fields in the pass that already decodes them, with one table
  where several lookups run now, and skip the body accounting for a request that ends with its HEADERS
  frame. Keep every check. Then move fit to that fib commit (rule 19).
- **Done when**: `bench/ab.sh -scenarios fixed,static -protocols h2c` on the benchmark host shows the
  new fib within noise of `5c0e5b9`; fib's micro-benchmarks show no significant regression against
  `5c0e5b9`; fib's security tests for these checks still pass; fib's CI passes, and fit is on that
  commit.
- **Touches**: fib `http` (`h2_server.go`, `h2_pseudo.go`), fit `go.mod`.

**Decisions pending review** (made while building it, 2026-11-06; each stands until the user rules on it):

1. **The `:authority` remembered.** After the first two commits `BenchmarkH2ServerGET` was still
   +0.7%, so a connection keeps the last valid `:authority` and checks it again only when it changes
   (`cb9bbbe`): clients send the same string each time, and a decoded string is a copy that cannot
   change. Alternative: stop at `089a5b0`, GET at +0.7%, simpler code.
2. **The response side too.** `089a5b0` checks a known key of a response with the same one lookup,
   though the plan named request checks only; it helps `static` and can be dropped on its own.
3. **Hop-by-hop fields by comparison.** The map is a comparison everywhere, the HTTP/2 client
   included; behaviour is the same.
4. **What the A/B built.** fit's main calls `Context.SetIdleTimeout`, which `5c0e5b9` lacks, so both
   sides were built from fit `35270ef4` (the v1.7.1 pin, as in the A/B that found the cost) with a
   `replace` in a scratch modfile. The harness's h2c port was moved to 8091 for the run, uncommitted,
   since the user's `qwen27b.service` holds 8081.
