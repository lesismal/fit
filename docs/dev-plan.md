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
- [Sync: moving data from producers to consumers](#sync-moving-data-from-producers-to-consumers)

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
| `sync/...` | sync: records, pipelines, checkpoints, transforms, and each connector with its wire protocol (`sync/mysql`, `sync/postgres`, `sync/mongo`, `sync/kafka`, ...; decision 036) |
| `adapter/...` | config adapters: friendly format, nginx |
| `bench` | the benchmark harness, configs for nginx, Caddy, Traefik and fit, results |
| `test/e2e` | end-to-end and conformance suites |
| `internal/fibtest` | running fib handlers in tests without a network |
| `examples` | example configs; moved to [fit-examples](https://github.com/lesismal/fit-examples) |

## Index

Status: `todo`, `doing` or `done`. The next free iteration number is I210.

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
| I148 | The manager's users and roles per group, and the audit log; the Users and audit pages | I146 | done |
| I149 | Members behind NAT linking out to the manager, enrolled by a one-time token; Add by enrollment | I141 | done |
| I150 | Metrics, logs and certificates across members, `fit cluster` in the CLI, the assistant's cluster tools; their pages | I147, I125 | done |
| **AI assistant, continued** | | | |
| I152 | The assistant's media library tools and skills: the catalog, channels, sessions, rescans, key rotation and file changes proposed; `vod-setup`, `channel-scheduler`, `library-health`, `playback-troubleshoot` | I127, I138 | done |
| I153 | The assistant across a cluster: members' state, templates and rollouts read, changes proposed as rollout plans, the manager's roles checked; `fleet-health`, `rollout-planner`, `cluster-drift` | I127, I146, I148 | done |
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
| **Sync** | | | |
| I163 | Decision 036; `sync`: the record model, pipelines (source, transforms, sinks, `stream` or `once`), a bounded queue, batches, acknowledgements to a watermark that never splits a transaction, the checkpoint file, retries and `on_error` | I04 | done |
| I164 | Built-in transforms: `filter`, `route`, `fields`, `key`, `cast`, `mask`, `envelope` | I163 | done |
| I165 | WASM plugins for sync: transforms, codecs, and source and sink connectors on fit's HTTP client and timers; the guest SDKs | I163 | done |
| I165b | WASM sync plugins, the rest: the benchmark host's run and a binary record form if it asks; TinyGo and Rust plugins built and run in the tests; examples in fit-examples | I165 | todo |
| I166 | Files as a source (tail and globs, rotation and truncation followed, multiline) and a sink (through I155's file sink) | I163, I155 | done |
| I167 | Batch jobs as DataX runs them: chunked parallel snapshots resumed by chunk, `fit sync run --once`, schedules | I163 | done |
| I168 | Elasticsearch and OpenSearch as a sink: `_bulk` by `_id`, per-item errors, 429 backed off | I163 | done |
| I169 | Elasticsearch and OpenSearch as a source: a point-in-time snapshot, then a cursor polled | I168 | done |
| I170 | ClickHouse as a sink (deduplication tokens, `ReplacingMergeTree`) and a source (a cursor polled) | I163 | done |
| I171 | HTTP as a source (`sync_ingest`, answered once the sinks have the records) and a sink | I163 | done |
| I172 | A MySQL client: handshake, TLS, both password plugins, text and prepared statements | I163 | done |
| I173 | The MySQL binlog: events, checksums and every column type, MySQL's and MariaDB's | I172 | done |
| I174 | MySQL and MariaDB as a source: a replica from the checkpoint's GTID set, column names, DDL, a checkpoint at each XID | I173 | done |
| I175 | The MySQL snapshot without locks, chunked by key, and its hand-over to the binlog | I174 | done |
| I176 | MySQL and TiDB as a sink: upserts and deletes by key, the source's transactions kept | I172 | done |
| I177 | A PostgreSQL client: startup, TLS, SCRAM-SHA-256, both query protocols, `COPY`, replication commands | I163 | done |
| I178 | PostgreSQL as a source: `pgoutput` from a slot, the LSN confirmed only after the sinks | I177 | done |
| I179 | The PostgreSQL snapshot from the slot's exported snapshot, an exact hand-over | I178 | done |
| I180 | PostgreSQL as a sink: `ON CONFLICT` upserts, deletes, `COPY` for snapshots | I177 | done |
| I181 | A MongoDB client: BSON, `OP_MSG`, the primary found, SCRAM-SHA-256, TLS | I163 | done |
| I182 | MongoDB as a source: change streams from a resume token, a snapshot by `_id` | I181 | done |
| I183 | MongoDB as a sink: bulk upserts and deletes by `_id` | I181 | done |
| I183b | One set of MongoDB connection params for the source and the sink | I182, I183 | done |
| I184 | A Kafka client: metadata, produce, fetch, offsets, every compression, SASL, TLS | I163 | done |
| I185 | Kafka as a source and a sink, offsets in fit's checkpoint; TiDB through TiCDC | I184, I176 | done |
| I186 | Redis as a source (streams) and a sink (streams or keys) | I163 | done |
| I187 | S3 and compatibles as a sink (rolled objects) and a source (objects in key order) | I163 | done |
| I188 | The Admin API and `fit sync`: state, pause, resume, checkpoints, connection tests, dry runs | I163 | done |
| I188b | Connection tests for the earlier connectors | I188 | done |
| I189 | Metrics, logs and dead letters, replayed from the Admin API | I188 | done |
| I190 | The workbench's Sync pages, and the assistant's tools for sync | I188, I189, I123 | done |
| I191 | Exactly once into MySQL, TiDB and PostgreSQL: the checkpoint written in the sink's transaction | I176, I180 | done |
| I192 | The interoperability and recovery matrix in CI | I176, I180, I183, I185 | done |
| I193 | Benchmarks against Filebeat, Canal, Debezium and DataX | I192 | todo |
| I194 | Sync's documentation and examples | I190 | done |
| **Access** | | | |
| I195 | Allow and deny lists of client addresses (whitelists and blacklists): a global `access`, and one on each listener, route, service and role that decides first; refused at accept through fib's `Binding.Filter` (A73) where it can be, per request or once a service is picked where it cannot; list files watched | — | done |
| **BitTorrent** | | | |
| I196 | fib A75–A79 for the BitTorrent client: storage and hashing off fib's workers, fast resume, limits that change at run time, refusing peers by address, the client on the caller's engine | — | done |
| I197 | The `bittorrent.client` role: config, the client on the shared engine, the state file in `data_dir`, restore and reload (decision 037) | I196 | done |
| I198 | Adding torrents (magnet, file, URL, `watch_dir`), categories with a media library rescan, queueing, disk checks | I197 | done |
| I199 | `alt_speed` schedules, per-torrent limits, the role's `access`, `.p2p` and `.dat` blocklists in `access` for every module | I197 | done |
| I200 | `bt_stream`: playing a torrent's file through HTTP ranges while it downloads, adding by magnet link on the fly | I197 | done |
| I201 | The `bittorrent.tracker` role: `bt_tracker` over HTTP, UDP, private passkeys, an allow-list, statistics | I196 | done |
| I202 | Publishing a directory as hybrid torrents with web seeds, following the disk | I197 | done |
| I203 | The Admin API, an SSE stream of events, `fit bt`, metrics | I198 | done |
| I204 | The workbench's BitTorrent pages, `schema-zh.js`, the AI assistant's functions | I203 | done |
| I205 | Interop with libtorrent and anacrolix, failure tests, benchmarks on the benchmark host, docs and an example | I198–I204 | done |
| I206 | (second phase) The cluster's view of its members' torrents; spreading large files to members over BitTorrent | I203 | todo |
| I207 | (second phase) A tracker store the cluster shares | I201 | todo |
| I208 | (second phase) RSS auto-download | I198 | todo |
| I209 | The roles use fib A80–A86: publishing hashes once and builds stop on a context, a public tracker's torrents listed and its bounds changed in place, the client's event queue bounded | I201, I202, I203 | done |

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

**Decisions pending review** (made while building it, 2026-11-07; each stands until the user rules on it;
also in decision 031 and decision 033 as its items 146–156):

1. **`assistant` and `cluster` meet through an interface of plain types** (`assistant.Cluster`,
   implemented by `cluster.Manager.Assistant()`), so neither imports the other. Alternative: the tools
   in `cluster`, or `assistant` importing `cluster`.
2. **A change asked in words becomes one of two new rollout kinds**: a patch (parts written in the
   request, for config no template owns; admin on each member), or a template draft (members planned
   against it, saved only when a cluster-wide admin starts the rollout; refused if the template changed
   since). Alternative: templates only, or saving the template when the plan is made.
3. **Secrets**: the assistant's `[redacted]` goes out as `[secret]`, which keeps the existing secret; a
   member with none there is not ready; a secret the model writes out is refused.
4. **No tool starts anything**: `rollout_propose` returns the plan and a link to its page, whose own
   confirm starts it; `cluster_op_propose` checks the operation as `StartOp` would and links to the ops
   page with the confirm filled in; none is a step of the conversation's proposal, so Apply never runs
   them. Alternative: proposed operations stored in the manager.
5. **The proposal card** is drawn from the tool result's `link`, no new stream event; over MCP the answer
   carries the workbench's absolute URL.
6. **Plans record `source` and `suggested`** (the strategy), and the start form prefills from it.
7. **Waits**: 20s for a plan, a read across members, a member read and the log tail, inside the chat's
   30s tool limit; a plan not ready by then says so and keeps going.
8. **`member` reaches reads only**: config, effective config, check, versions, status, metrics, cache,
   node, schema, the media and streaming reads, and the text conversions; never writes, tokens or log
   files. A member of another fit version adds a note.
9. **I150 reused**: certificates from its inventory (no member calls), metrics a live read plus 12
   points of its series, `cluster_log_tail` its `TailLogs`, behind `data.send_logs`.
10. **Cluster skills are hidden on a fit that is not a manager.**
11. **Role needs match the endpoints'**: any role reads the lists; metrics, logs and `member` reads need
    a role on each member read; the narrower of role and assistant level wins.
12. **Ask AI**: the Overview's toolbar and each member card (`fleet-health`), a failed or rolled-back
    batch (`rollout-planner`), each drifting member (`cluster-drift`); the `diagnose` button stays.
13. **The New rollout form** gains "Parts written here" (a patch) and a template draft, for admins only.

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

**Decisions pending review** (made while building it, 2026-11-06; each stands until the user rules on it;
also in decision 033 as its items 118–133):

1. **Roles live in the store's document**, changed live by a cluster-wide admin, with
   `admin.cluster.admins` in the config as the way back in. Alternative: the config, which changes only at
   a restart and which any console user can edit.
2. **Before any role is set**, every console user is a cluster-wide admin, as before; the first save must
   leave an admin. Alternative: everyone a viewer after the upgrade, which would lock people out.
3. **What each role may do**: a viewer reads; an operator runs operations, tests saved members, and
   plans and steers template and sync rollouts; an admin also edits members and credentials, enrolls,
   tests drafts, writes through the switcher and plans copies; groups, templates and roles need a
   cluster-wide admin, as they decide who has which role where.
4. **A change to a member is checked as the member is and as it will be**, so a label change cannot move
   it out of the editor's reach; group roles follow a rename and leave with the group.
5. **Lists are not filtered by role**; a member's own reads need a role on it. Alternative: filter every
   list.
6. **The default role** is none unless set; a user listed without a role stays at none.
7. **The router fails closed**: an endpoint added with `Handle` lets only cluster-wide admins write;
   `Checked` endpoints decide for themselves.
8. **The manager's own pages belong to cluster-wide admins** once roles are set, or a viewer could edit
   the config and become admin at the next restart.
9. **The assistant's cap**: a cluster-wide admin unbounded, an operator at most `propose`, others `read`;
   for cluster tools the narrower of role and level wins.
10. **Version 3** is written only when roles exist; older fits then open the document read-only.
11. **A diff holds what the API shows**, secrets masked; a switcher write keeps only its body's digest;
    revisions are kept, as the ETag starts over at a restart.
12. **Tests of members are audited**, reversing I141's ninth decision; polls and reads are not.
13. **Both bounds on every write path**, where some only counted entries.
14. **Audit filters**: user, member, operation (whole or by prefix) and RFC 3339 times; JSON lines by
    `?format=jsonl`.
15. **The pages hide** controls a role may not use, not merely disable them; a template's editor is
    read-only for non-admins.
16. **I147's endpoints**: reading drift and alerts any role; Fix and silences an operator on the members
    (or group, or the cluster for a rule of no group); rules, webhooks and a webhook's Test a cluster-wide
    admin. Alternative: silences for admins only, as rules.

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

**Decisions pending review** (made while building it, 2026-11-07; each stands until the user rules on it;
also in decision 033 as its items 134–145):

1. **The CLI reaches the manager through its Admin API** (token and `-c`, as `fit check`), served
   under `/cluster/...` by handlers calling the Manager's methods. Alternative: the console with a
   login, at the cost of a password in scripts.
2. **The Admin API acts as a cluster-wide admin**, the user `Admin API`, whatever the roles say; a
   console user of that name has no role. Alternative: a role for the token in the config.
3. **"Bytes" in the series are bytes queued for clients** (`pending_bytes`), not throughput: a byte
   counter would sit on the request path. Alternative: bytes counted per route, measured on the
   benchmark host (a follow-up).
4. **Series live in the manager's memory**, 360 polls a member (about 30 KiB), lost at a restart.
   Alternative: kept in the store, written at every poll.
5. **Rates are computed when read**, empty across a member's restart, a gap of more than three
   intervals, or a member counting no requests; groups summed a poll interval at a time.
6. **Series and inventory are any role's**, as states (item 122); logs need a role on each member.
7. **The tail reads members' log rings** through a new member endpoint `GET /logs/recent`
   (capability `logs.recent`, ring messages numbered), not files: 16 members, 1,000 messages a
   member, one of the 8 member-read slots; the page keeps 2,000 lines.
8. **The inventory is the poller's `/status` read**, 256 certificates a member, not a fresh read.
9. **Overview sparklines are a `rowExtra`** (I142's extension point); `overview.js` untouched.
10. **`fit cluster rollout start`** plans, prints, asks unless `-yes`, follows with `-wait`; exit 1
    for failed, aborted, rolled back or expired; `state`, `diff`, `certs -within` exit 1 when
    something needs attention.
11. **`diff` tells members nothing** of what the manager owns there, as drift reads (item 105).
12. **Charts are the workbench's own inline SVG**, colours by member name set through the CSSOM
    (the console's CSP refuses inline styles); no chart library.

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

## Access

### I195 Allow and deny lists of client addresses

- **Why**: asked for on 2026-11-08: fit should take allow lists (whitelists) and deny lists
  (blacklists) of client addresses, set for the whole config and for each service module on its
  own, the module's own taking precedence over the global one.
- **Do**: `config.Access` (`allow`, `deny`, `allow_files`, `deny_files`) at the top level and on
  listeners, routes, services and the roles; package `access` compiling a list into sorted ranges
  looked up by binary search, a `Policy` of the lists that apply, most specific first, and a `Gate`
  swapped when a config is applied; fib's `Binding.Filter` (A73) so that a refused peer costs no
  Connection; the Admin API, the console and every role's listeners gated too.
- **Done when**: a refused peer is closed at accept, or its datagrams dropped, where the policy is
  known then; a route's, service's or role's own access overrides the global one both ways, with
  tests; a reload changes the policy without binding anew; list files are watched; docs in English
  and Chinese; the race tests and CI pass.
- **Touches**: `access`, `config`, `runtime`, `listener`, `l4`, `app`, `admin`, `app/console`,
  `streaming`, `webrtc`, `tunnel`, `llm`, `schema`, `docs`; fib's `core` (A73).

**Decisions pending review** (made while building it, 2026-11-08; each stands until the user rules on it):

1. **How the layers combine.** Each list may decide: its `deny` refuses, else its `allow` serves,
   else an `allow` with entries refuses whatever it does not hold; otherwise it has no say and the
   next one out decides (route, then listener, then global; service, then listener, then global;
   role, then global). An address none has a say on is served. So a more specific list overrides
   both ways, and within one list deny beats allow. Alternatives: a local list replacing the global
   one wholesale, or lists only narrowing (a local allow could not serve what the global deny
   refuses).
2. **Where it is decided.** At accept, through `Binding.Filter`, when the listener's policy is
   all there is: a refused TCP connection is closed before anything is read, a refused UDP or
   HTTP/3 peer's datagrams dropped. A listener whose routes or services set an access of their own,
   or that takes PROXY headers, decides later instead: an HTTP request refused is answered 403
   (plain text), a tcp connection closed once its PROXY header and ClientHello say which service
   takes it, a socks client refused at its request. A tcp or udp listener with one service and no
   server names decides at accept with that service's policy.
3. **Which address.** The connection's, or the one its PROXY header names; never
   `X-Forwarded-For`, which a client writes itself. Behind a CDN, the PROXY protocol or the CDN's
   own lists are the way.
4. **Changes apply to what starts after them.** Open connections, HTTP/2 and HTTP/3 connections'
   later requests on a listener decided at accept, and known UDP peers are not checked again. A
   reload keeps the bindings and swaps their gates.
5. **List files.** One entry a line, `#` and `;` comments, at most 64 MiB a file; read when the
   config is applied and watched as certificate files are, so a change applies the config again.
   A file that cannot be read fails the config, as a bad certificate file does.
6. **ACME.** An allow list on a listener that answers HTTP-01 or TLS-ALPN-01 challenges must serve
   the CA's validation servers; fit does not open a hole for them, since their addresses are not
   published. Documented in the reference.
7. **fib's Filter (A73).** Not counted in `Stats.ConnsRefused`, which stays `MaxConns`'s; Unix
   sockets and `OpenUDPPeer` (ICE's own peers) are not asked; a refused UDP peer is asked again for
   each datagram rather than remembered, so that a policy change takes effect at once and nothing
   grows with the peers refused.
8. **The roles.** One `access` per role covers every socket it listens on: `streaming` (RTMP,
   RTSP, SRT and its WebRTC port), `webrtc.turn`, `webrtc.media`, `webrtc.proxy`, `tunnel.hub`
   (listeners and `console_socket`), `tunnel.proxy` (forwards and web proxies), `llm.hub`,
   `llm.node` and `llm.proxy` (`listen`, and the proxy's `api`); not one per sub-listener.
   `tunnel.node` has none, listening only for its pages. Each role sets its gate on every Apply,
   since the global list may change while its member does not, and access is in no restart key.
   The WebRTC ports shared through fib's `webrtc.Mux` needed `MuxConfig.Filter` (A74).
9. **The Admin API, the console and the roles' pages.** `admin.access`, `admin.console.access`
   and each role's `console.access`, before the global list, which applies to them too: a global
   `allow` that leaves out 127.0.0.1 shuts local clients out of the Admin API, and the docs say to
   set `admin.access`'s `allow` to `127.0.0.1` and `::1` to keep it. They follow every config
   applied, though the rest of `admin` is read at start. An Admin API on a Unix socket is never
   checked, and `admin.access` with one is a validation error. Alternative: the global list not
   applying to the Admin API at all.

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

## Sync: moving data from producers to consumers

Proposed 2026-11-08. fit gains `sync`: pipelines that read changes or records from a producer (a
**source**) and write them to consumers (**sinks**), as Filebeat does for files and DataX, Canal or
Debezium do for databases. Sources and sinks are set in the config like any role; every kind of
infrastructure below is both a source and a sink, so any pair can be joined; and what the built-in
connectors and transforms do not cover is added by WASM plugins. A pipeline can run for good (a
stream: tail a file, follow a binlog) or once (a batch job: copy a table and stop). Decision 036
records the choices below.

Principles, in the order they bind:

- **Nothing is lost, and the checkpoint proves it.** A source's position (a binlog GTID set or file
  and offset at a transaction's end, a PostgreSQL LSN, a MongoDB resume token, a file's identity and
  offset, a Kafka offset, a cursor's last value) is saved only after every sink of the pipeline has
  acknowledged every record up to it, and a restart, crash or `kill -9` resumes from the last saved
  position. Delivery is at least once; sinks write idempotently (upserts by key, document IDs,
  deduplication tokens), so a replay converges to the same state. A source whose server keeps data
  for its readers (a PostgreSQL slot, a Kafka offset) is told the position only then, never earlier.
- **Transactions stay whole.** A checkpoint never falls inside a source transaction; records of one
  transaction carry its ID and boundaries, and a SQL sink can write them in one transaction of its own.
  Order is kept per key from source to sink; sinks may run keys in parallel, never one key out of order.
- **Bounded everything** (rule 11): the queue between stages in bytes and records, a batch in bytes,
  records and time, a record's size, a plugin's memory and time, the dead letters kept. A slow sink
  slows the source through the queue, and the lag is reported; nothing grows without limit in fit.
  What a source's server keeps meanwhile (a slot's WAL, binlogs) is reported with a warning threshold.
- **Off fib's workers.** Sync is not on the request path: a pipeline's source, transforms and sinks run
  on goroutines of their own, bounded per pipeline; HTTP-based systems (Elasticsearch, ClickHouse,
  HTTP, S3) go out through fib's asynchronous HTTP client; connections to databases are few and long,
  each on a goroutine. A listener that takes records in (the HTTP source) hands them over without
  blocking a worker (rule 1).
- **Lean dependencies** (design principle 6): the wire protocols (MySQL with its replication stream,
  PostgreSQL with logical replication, MongoDB's OP_MSG and BSON, Kafka, RESP, SigV4) are written in
  fit, as fib's own protocols are, each checked against the real server in Docker; no driver is
  imported. The canonical record is fit's own type, so no connector's types leak into another.
- **One record model.** Every source produces, and every sink takes, the same record: the source and
  its namespace (`db.table`, an index, a collection, a file, a topic), an operation (`insert`,
  `update`, `delete`, `ddl`, `message`), the key, the row before and after as typed values (null,
  bool, int, uint, float, decimal as text, string, bytes, time, date, JSON, array, map), the commit
  time, the transaction, and the opaque position. Transforms and plugins see only this.
- **Credentials as everywhere in fit**: in `admin.store` or named by an environment variable (`password_env`, as the assistant's `api_key_env`), written once in the pages and
  shown masked; TLS to every server that offers it, verified, with no silent `insecure`.
- **Off by default**: no `sync`, no connections out, no state on disk.

Words used below: a **pipeline**, one source, its transforms and one or more sinks, with one
checkpoint; a **snapshot**, the existing rows of a table read once, before or instead of its
changes; a **cursor**, a monotonic column or field (an update time, a version) a source polls by when
the system has no change stream.

What each kind of infrastructure is as a source and as a sink:

| Infrastructure | As a source | Its checkpoint | As a sink | Iterations |
| --- | --- | --- | --- | --- |
| File lines | tail paths and globs as Filebeat does: new files found, rotation by rename and by truncation followed, partial last lines held, multiline joins | the file's device, inode and a fingerprint of its head, and the offset | lines (raw, JSON, CSV or a template) through fit's file sink, rotated and kept (I155) | I166 |
| MySQL, MariaDB | the binlog in row format, as a replica (`COM_BINLOG_DUMP_GTID`); a snapshot chunked by primary key | GTID set, or file and offset, at a transaction's end | upserts and deletes by primary key, batched, optionally in the source's transactions | I172–I176 |
| TiDB | changes through TiCDC (canal-json or TiCDC's simple protocol, from Kafka); a snapshot over the MySQL protocol | Kafka offsets, the commit TS in the record | the MySQL sink: TiDB speaks MySQL's protocol | I185 |
| PostgreSQL | logical replication with `pgoutput`, a slot and a publication; a snapshot from the slot's exported snapshot | the LSN, confirmed to the slot only after the sinks | `INSERT ... ON CONFLICT DO UPDATE` and deletes, batched; `COPY` for snapshots | I177–I180 |
| ClickHouse | no change stream: polled by a cursor over the HTTP interface, a snapshot by query | the cursor's last value and the keys at it | inserts over HTTP (`JSONEachRow`), with deduplication tokens; deletes as a `ReplacingMergeTree` version and a deleted flag | I170 |
| Elasticsearch, OpenSearch | a snapshot with a point in time and `search_after`; then polled by a cursor field | the cursor's last sort values | `_bulk` index and delete by `_id` from the key, per-item errors, 429 backed off | I168, I169 |
| MongoDB | change streams with `fullDocument`, a snapshot by `_id` ranges | the resume token | bulk writes: upserts and deletes by `_id` | I181–I183 |
| Kafka (and Redpanda) | topics and partitions read from fit's own offsets | offsets per partition | produce with the key, batched, compressed | I184, I185 |
| Redis | streams (`XREAD` from an ID) | the last ID | `XADD`, or a key per record (`SET`/`HSET`/`DEL`) | I186 |
| HTTP | records posted to a fit route, answered once the sinks have them | none: the client retries until answered | batches posted as JSON or NDJSON | I171 |
| S3 and compatibles | new objects under a prefix, in key order, read as lines | the last key and its offset | objects of batched lines, rolled by size and time | I187 |
| WASM connectors | a plugin that reads through fit's HTTP client and timers | opaque bytes the plugin returns | a plugin that writes through the same | I165 |

How a pipeline looks in the config (the names may change in I163):

```json
"sync": {
  "state_dir": "/var/lib/fit/sync",
  "pipelines": [{
    "name": "orders-to-search",
    "source": {"type": "mysql", "addr": "db:3306", "user": "repl", "password_env": "MYSQL_REPL_PASSWORD",
               "server_id": 4101, "tables": ["shop.orders", "shop.order_items"], "snapshot": "initial"},
    "transforms": [{"type": "route", "map": {"shop.orders": "orders"}},
                   {"type": "wasm", "module": "plugins/mask.wasm"}],
    "sinks": [{"name": "es", "type": "elasticsearch", "urls": ["https://es:9200"],
               "batch": {"max_records": 1000, "max_bytes": "5MiB", "max_wait": "1s"}}],
    "on_error": "stop"
  }]
}
```

The workbench's Sync menu, built up by the iterations below: pipelines with their state, lag and
throughput; a pipeline's editor made from the schema, with **Test connection** and a dry run that
shows the first records without writing or saving a position; the checkpoint, shown and reset with a
confirmation; errors and dead letters; pause, resume and a new snapshot.

Not in this track: SQL joins or aggregations across sources (a stream processor's work; a WASM
transform sees one batch at a time); DDL replayed into SQL sinks (I163 decides; reported, not
applied); one pipeline run on several fit instances at once (a pipeline runs on one fit; the cluster
track can place it); exactly-once into sinks with no transactions.

### I163 Decision 036; records, pipelines and checkpoints

- **Do**: decision 036 (the principles above, the record model, at-least-once with idempotent sinks,
  protocols written in fit, and the pending choices below). Package `sync`: the record and its typed
  values, with a canonical JSON form; `config.Sync` with `state_dir` and `pipelines` (source,
  transforms, sinks, `mode` `stream` or `once`, `on_error` `stop`, `skip` or `dead_letter`, queue and
  batch limits), registered in the module registry so connectors are modules (`sync.sources.*`,
  `sync.sinks.*`, `sync.transforms.*`), checked by `fit validate` and described in the schema; the
  runtime: a source feeds a bounded queue, transforms run in order, each sink batches by records,
  bytes and time and acknowledges; acknowledgements advance a watermark of contiguous positions that
  never falls inside a transaction; the checkpoint store writes the watermark to
  `<state_dir>/<pipeline>.json` (temporary file, `fsync`, rename) at most each `checkpoint_interval`
  and at shutdown, and a pipeline starts from it; retries with backoff, then `on_error`; a `generate`
  source and `memory` and `stdout` sinks for tests; a reload adds, removes and restarts pipelines whose
  config changed, keeping the others running.
- **Done when**: records flow from `generate` to two sinks and the checkpoint is the minimum both have
  acknowledged; a sink failing mid-batch, a crash between a write and a checkpoint, and `kill -9` at
  random points (a test helper that stops the runtime anywhere) lose nothing and replay only after
  the checkpoint; a checkpoint never lands inside a transaction; a full queue stops the source, and
  memory stays within the limits under a stalled sink; `once` stops at the source's end with the
  checkpoint written; `dead_letter` writes the record and its error to the named sink; a reload
  restarts only changed pipelines; under the race detector.
- **Touches**: `docs/decisions` (036), `sync`, `config`, `module`, `schema`, `cli`, `docs`.

**Decisions pending review** (made while building it, 2026-11-08; each stands until the user rules on it):

1. **Package path.** The directory is `sync`, the package name `fitsync`: the plan's paths
   (`sync/file`, `sync/mysql/wire`) and the config's `sync` stay, and the name does not shadow the
   standard library's `sync`, which the runtime imports. Alternative: a directory `datasync`.
2. **A sink's shape.** `{"name", "sink": {"module", "params"}, "batch"}`: the module in a reference
   of its own, so the schema, the decoder and `module.Load` treat it as every module reference.
   Alternative: the plan sketch's flat `{"name", "type", ...params}`, which needs a decoder and
   schema of its own.
3. **Dead letters.** `dead_letter` is a sink of its own, required with `on_error: dead_letter` and
   refused without it, rather than one of `sinks` (which would take every record too). A dead
   letter is a `message` record whose `after` holds `error`, `at`, `attempts` and `record` (the
   canonical JSON). Alternative: `dead_letter` naming one of `sinks`.
4. **`state_dir` is required** when `sync` is set: where checkpoints live is worth saying.
   Alternative: a default under `$XDG_DATA_HOME/fit/sync`, as ACME's storage.
5. **Retry settings are the pipeline's**, shared by its sinks, the dead letter sink and the source's
   restarts. Alternative: each sink its own.
6. **A batch error that names no record** is retried whole; once its attempts are spent its first
   record is the failed one, `on_error` takes it, and the rest is retried with fresh attempts, so a
   sink that is down costs each skipped record its attempts. Alternative: write the batch record by
   record once to find the bad ones.
7. **Transforms are per record** (`Apply(r, emit)`), on the dispatcher goroutine; a batch interface
   for WASM is I165's to add. Alternative: a batch interface now.
8. **A source's error restarts the pipeline from its checkpoint for as long as it fails**, after the
   backoff; only sink and transform errors follow `on_error`. Alternative: a source's error past
   its attempts follows `on_error` too.
9. **Byte sizes are integers** (`max_bytes: 4194304`), as `rotate`'s and the media library's are, not
   `"5MiB"` as the plan's sketch had. Alternative: a size type taking units, for every byte field.
10. **The queue bound counts records from `Emit` until the watermark passes them**, so one bound
    covers the queue, the transforms and every sink's pending batch; a record larger than
    `max_bytes` goes through alone once the queue is empty. Alternative: separate bounds per stage.
11. **Sources are told the checkpoint after it is saved** (`Confirmer`), not when the watermark
    moves, so a PostgreSQL slot never releases what the file does not hold yet.
12. **Crash tests use a hook** (`Options.Crash` at six named points, killing the pipeline as
    `kill -9` would), not a subprocess killed for real. Alternative: a helper binary killed with
    SIGKILL, slower and not portable to Windows.
13. **No metrics, Admin API or status endpoint yet**: `Runtime.Status` is there for I188 and I189;
    a pipeline's state is in its log meanwhile, under a logger of its own, `sync`.

### I164 Built-in transforms

- **Do**: `filter` (by namespace with globs, by operation, by a field's value), `route` (namespace
  renamed or mapped, e.g. tables to indices or topics), `fields` (keep, drop, rename, set a constant,
  move into or out of a nested map), `key` (choose or build the key from fields), `cast` (a field's
  type), `mask` (hash or blank fields), and `envelope` (a record turned into a flat document, a
  before/after pair or Debezium's shape for sinks that take one); each a module, chained in order.
- **Done when**: each transform has table tests over every value type and operation; a chain of them
  allocates no more than one record copy per record; a delete keeps its key through every transform
  that would drop the key's fields, or the config is refused.
- **Touches**: `sync/transform`, `schema`, `docs`.

**Decisions pending review** (made while building it, 2026-11-08; each stands until the user rules on it):

1. **Where they live.** In package `fitsync` itself (`sync/transform*.go`), not a package
   `sync/transform`: they read values' internals and the record's copy flag. Alternative: a
   subpackage, on exported accessors alone.
2. **One copy per chain.** `Record.Own` returns the record when a transform made it with `Clone`,
   and a clone otherwise; `Emit` clears the flag, so the source's record stays as read (a dead letter
   holds it), and a chain copies a record once, however many transforms change it. A transform emits
   each record it owns once. The dispatcher's `emit` closure is now made once rather than per record
   and transform, which allocated. Changed values still allocate their new field lists. Alternative:
   a copy per changing transform, or changing the source's record in place.
3. **Fields are named at the row's top level**, no dotted paths; `fields`' `unnest` and `nest` move
   them in and out of maps. The row is `after`, or `before` when `after` is null. Alternative: dotted
   paths or JSON pointers everywhere.
4. **`filter` compares fields as text**: `equals` and `in` are strings, matched against the value's
   text (`"42"`, `"true"`, RFC 3339 times), and `present` tests missing or null. Alternative: typed
   values in the canonical JSON form.
5. **`route`**: `map` exactly first, then glob `rules` whose `to` is a template of `{namespace}`,
   `{first}`, `{rest}` and `{last}`; `unmatched` `keep` or `drop`. No lowercasing or regular
   expressions. Alternative: regular expressions with capture groups.
6. **`fields`** applies `unnest`, `nest`, `rename`, `drop` or `keep` (not both), then `set`; a
   collision fails the record; renames apply to the key's fields too, so the key names the sink's
   columns; nothing else touches the key; `set` takes values in the canonical JSON form.
7. **`key`**: a delete whose `before` lacks the fields keeps the key it has; any other record
   missing one fails; DDL passes untouched; formats `map`, `value` and `join` (`:`).
8. **A key built after a field is removed is refused at validation**: a `key` after `fields`
   dropping, not keeping, renaming away, nesting or unnesting one of its fields, after `mask` with
   `null`, or after `envelope` `pair` or `debezium`. Alternative: allow it and fail records at run
   time.
9. **`cast`** applies to the key too, by the table in its doc: ints and uints as Unix seconds for
   times, a time's date in its own offset, any value to `json` as plain JSON, bytes to string only
   when UTF-8; null stays null. Alternative: a `layout` or unit option for times.
10. **`mask`** defaults to `hmac_sha256`, with the key only from `key_env` (no inline key), read when
    the pipeline loads, so `fit validate` needs the variable too; `sha256` is allowed and documented
    as reversible for small sets; `null`. A value hashes as its text, to a lowercase hex string; hashes
    apply to the key's fields, `null` does not. Alternative: the key in `admin.store`, or the key read
    lazily at the first record.
11. **`envelope`** reshapes inserts, updates and deletes only (DDL and messages pass); `before`
    becomes null and the document is `after`; a flat delete is `before`, or the key when it has none;
    `deletes` `keep`, `drop`, or `rewrite` to an update. Debezium's shape takes `ts_ms` from the
    commit time (top level and `source`), `op` `c`, `u` or `d` (no `r` until snapshots exist), and
    `db` and `table` split at the namespace's first `.`.
12. **Size**: about 1,170 lines of code and 730 of tests, past the plan's 1,500, since the seven
    transforms only make sense together (the chain check, the copy count); no I164b.

### I165 WASM plugins for sync: transforms, codecs and connectors

- **Do**: a host module `fit_sync` beside the HTTP ABI of `plugin/wasm`, sharing its runtime, pool of
  instances and bounds. A **transform** plugin exports `fit_sync_transform(batch)`: it reads the batch
  (the records' canonical JSON, a binary form later if the benchmark asks), and emits zero or more
  records for each through `record_emit`, so it filters, changes, splits and routes. A **codec**
  plugin turns bytes into records (`fit_sync_decode`, for file lines, Kafka messages, HTTP bodies and
  S3 objects) and records into bytes (`fit_sync_encode`). A **connector** plugin is a source
  (`fit_sync_poll(checkpoint)` returning records and the next checkpoint) or a sink
  (`fit_sync_write(batch)`), doing its I/O through host functions: an HTTP request on fib's
  asynchronous client to the hosts the config allows, a timer, the plugin's config and a log; no raw
  sockets. Every call has a time and memory limit; a plugin that traps or overruns fails the batch,
  which then follows `on_error`. The guest SDK (`plugin/wasm/guest`, Go and TinyGo) and a Rust crate
  gain the sync calls, with examples.
- **Done when**: a TinyGo transform drops, changes and splits records; a codec reads a custom line
  format from files; a connector plugin reads a paged HTTP API into a sink and resumes from its
  checkpoint after a restart; a plugin past its time or memory, or calling a host not allowed, fails
  the batch and leaves the checkpoint where it was; a pool of instances keeps a pipeline's throughput
  within 20% of a built-in transform doing the same work, measured on the benchmark host.
- **Touches**: `plugin/wasm`, `plugin/wasm/guest`, `sync`, `docs`, fit-examples.

**Decisions pending review** (made while building it, 2026-11-08; each stands until the user rules on it):

1. **A batch interface beside the per-record one** (I163's decision 7). `BatchTransform` adds
   `ApplyBatch(in, emit(i, o), fail(i, err)) error` to `Transform`; the dispatcher puts the records
   it holds through the transforms one transform at a time (`sync/stages.go`), a batch transform in
   one call, and still takes failures in the source's order, so `on_error` and the checkpoint
   behave as before. Alternative: per record only, a WASM call for each record.
2. **What fails is a call, not the dispatcher's whole batch.** The `wasm` transform splits the
   records into calls of `batch_records` (256) and runs them on up to `instances` (the CPUs, at most
   4) at once, handing on what they make in order; a call that fails, traps, runs past `timeout` or
   asks for more than `max_memory_bytes` fails each of its records, which `on_error` takes; a plugin
   may fail one record with `fail(i)`. Alternative: fail everything the dispatcher held.
3. **The ABI's shape.** Each export takes its input's length, and the plugin pulls the input with
   `input(buf, len)`, as the request ABI's getters write into the plugin's buffers; results go out
   through `record_emit(index, json)` (indexes never go back), `fail(index, msg)`, `output(bytes)`
   and `checkpoint(json)`; an export returns 0, 1 (a poll caught up) or 2 (failed). A plugin's config
   and log are the request ABI's own `request_get(5)` and `log`, which answer in a sync call too.
   Alternative: the host writing into memory the plugin allocates through an exported `malloc`.
4. **What is shared with the request plugins, and what is not.** The compiled plugin, its runtime,
   the memory bound, the compilation cache and the host modules are shared (`loadPlugin`, which no
   longer checks for `fit_on_request`: the middleware does); the pool is the sync modules' own, idle
   instances in a channel that a pipeline's goroutines wait on, made when first needed and again
   after a failed call, each call bounded by a deadline on its context, rather than the request
   pool's queue and watchdog. A Go plugin exports every sync call, since package `guest` declares
   them all; one it registered nothing for fails the call. Alternative: one pool type for both.
5. **HTTP and sleep are a connector's alone.** A transform's or a codec's plugin calling
   `http_request` or `sleep` fails its call. `allow_hosts` entries are a host (any port),
   `host:port` or `*.domain`; none allows nothing. Requests go through fib's asynchronous client on
   the role's engine (`Options.Engine`, the upstream engine), the plugin's call waiting on the
   pipeline's goroutine within its `timeout` (30s for connectors); bodies are text in the request's
   JSON, a response's up to `max_response_bytes` (4 MiB). Alternative: base64 bodies for binary
   data; URL prefixes rather than hosts.
6. **A poll is a unit.** The source clears the transactions and positions the plugin's records
   carry and sets the poll's next checkpoint on its last record, so the checkpoint never falls
   inside a poll; a poll that moves the checkpoint with no records moves only the one in memory, and
   is polled again after a restart. A failed poll restarts the pipeline from its checkpoint (I163's
   decision 8). Alternative: positions the plugin sets on each record.
7. **A sink's plugin names the record that failed** with `fail(i)`; the earliest it names is the
   `BatchError`'s index, those before it written.
8. **Codecs are a namespace now**, `sync.codecs`, with the interface `Codec` (`Decode(data, emit)`,
   `Encode(b, recs)`) and the module `wasm` (each of its two exports optional, one required); no
   source or sink takes one until I166, so the tests prove it by reading a file of a line format
   of the plugin's own and writing it back. The reference has a part *Sync codecs*. Alternative:
   leave the interface to I166.
9. **Go rather than TinyGo in the tests.** The tests build Go `wasip1` plugins when they run, as
   the request plugins' tests do; TinyGo is not on the machine, so the plan's TinyGo transform is a
   Go one, and a TinyGo build is left to I165b. The guest SDK reads a batch and writes records by
   hand rather than with `encoding/json`, whose reflection is slow in WebAssembly (the benchmark
   below went from 160 µs a record to 46.5 µs), which also spares TinyGo's reflection.
10. **The Rust crate** (`plugin/wasm/guest/rust`, `fit-guest`, no dependencies) wraps the imports
    and splits a batch into its records; its unit test runs on the host, but no plugin built from it
    for `wasm32-wasip1` has run, since the machine lacks the target: untested as a plugin, left to
    I165b with the examples, which go to fit-examples.
11. **The benchmark misses 20% by far**, locally: `BenchmarkSyncTransform` on an Apple M4 Pro (not
    the benchmark host), the built-in `route` 81 ns a record; the `wasm` transform doing the same in
    a Go plugin 46.5 µs a record on one instance and 12.6 µs on four. JSON in and out of a Go plugin
    dominates; planning decision 8's binary form is therefore asked for, with the benchmark host's
    run, in I165b. Alternative: hold I165 until the binary form is in.
12. **The templates list every module of a single reference.** A pipeline's `source` and a sink's
    `sink` were written with their namespace's first module only; now the others follow it
    commented out with all their params, so the templates name every field, `allow_hosts` too. A
    module chapter outside handlers and middleware is `schema/doc/modules/<namespace>_<name>.md`
    (`sync_transforms_wasm.md`), since `wasm` names modules of several namespaces.
13. **Size**: about 2,000 lines of code (the guest SDK's 640 and the Rust crate's 180 included) and
    860 of tests, past the plan's 1,500; what was built is not split out, and I165b takes what is
    left.

### I165b WASM sync plugins, the rest

- **Do**: run `BenchmarkSyncTransform` and a pipeline through a `wasm` transform on the benchmark
  host; if a pool still misses 20% of the built-in, add the binary record form planning decision 8
  allows (and a host decoder that allocates less); build and run a TinyGo transform and a Rust
  plugin in the tests (and in CI); examples of a transform, a codec, a source and a sink in
  fit-examples.
- **Done when**: the benchmark host's numbers are recorded and the 20% holds, or the gap is
  explained and accepted; TinyGo and Rust plugins pass the same tests as the Go one.
- **Touches**: `plugin/wasm`, `plugin/wasm/guest`, `sync`, fit-examples.

**Decisions pending review** (made while building all but the benchmark host's run, 2026-11-09; each
stands until the user rules on it; the item stays todo until that run):

1. **The same tests, one subtest a guest.** `TestSyncTransform`, `TestSyncTransformFailures`,
   `TestSyncCodec`, `TestSyncConnectors`, `TestSyncTransformInAPipeline` and
   `TestSyncFailuresKeepTheCheckpoint` run as `/go`, `/tinygo` and `/rust`
   (`plugin/wasm/guests_test.go`): TinyGo builds `testdata/sync`, the Go plugin's own source,
   unchanged (`tinygo build -target=wasip1 -buildmode=c-shared`, default flags, as a user would);
   Rust builds `testdata/rust`, a plugin of the same behaviour on the `fit-guest` crate. All pass,
   locally in a container (TinyGo 0.44.0 with Go 1.27.3, Rust 1.99.0). Alternative: a smaller
   TinyGo transform and Rust transform of their own, as the plan's wording allows.
2. **Missing toolchains skip, unless required.** Without `tinygo`, or `cargo`/`rustc` with the
   `wasm32-wasip1` target, a guest's subtests skip, so the Test and Coverage jobs (no toolchains) are
   unaffected and no production code changed; `FIT_WASM_GUESTS=tinygo,rust` turns a missing one into a
   failure. CI's new job **WASM guests (TinyGo, Rust)** installs TinyGo 0.44.0 (its .deb) and Rust
   1.99.0 (`rustup`, `--target wasm32-wasip1`), runs the crate's `cargo test` and the `TestSync*`
   tests with `-race` and that variable set. Alternative: a build tag; or prebuilt plugins in
   testdata.
3. **The Rust crate reads records by hand, as the Go guest does.** `fit-guest` gains `Record`,
   `parse_batch`, `Row`, the value helpers (`int`, `string`, `boolean`, `as_int`, `as_str`, ...),
   `members`/`elements`, `emit_record`, `request` (an HTTP request from its parts) and
   `response_header`, still with no dependencies; `records` stays. Alternative: serde_json in the
   test plugin and the examples, the crate left as I165 had it.
4. **BenchmarkSyncTransform covers every guest**: `builtin`, then `wasm-<go|tinygo|rust>-<1|4>`, each
   guest skipped where it cannot be built, so the benchmark host needs TinyGo and Rust installed for
   all the numbers (the CI job's versions). Locally, in a container on an Apple M4 Pro (not the
   benchmark host), per record: built-in 206 ns; Go 49.9 µs on one instance, 15.3 µs on four;
   TinyGo 45.8 / 14.3 µs; Rust 30.2 / 11.3 µs. Every guest makes about 209 host allocations a
   record (53.5k a batch of 256), so the host's JSON decoding of what plugins emit dominates, not
   the guest's language: the binary form, with a host decoder that allocates less, is what the
   host run will most likely ask for. Left to the main session with the host run.
5. **Examples**: fit-examples branch `wasm-sync-examples` adds `sync-wasm-codec` (a line format of
   its own read from a file and written back, its bad line dead-lettered), `sync-wasm-source` (a
   paged API that fit itself serves, the checkpoint a page and how far into it) and
   `sync-wasm-sink` (NDJSON posts to a webhook fit itself answers), beside I194's
   `sync-wasm-transform`; each runs with fit alone and was run; their plugins are Go, and build by
   TinyGo 0.44 too (checked), which the comments show. No Rust example there: its CI builds only Go.
   Alternative: a Rust example, with a Rust toolchain in fit-examples' test.

**The binary record form** (planning decision 8, built 2026-11-09 before the benchmark host's run,
since the local numbers already asked for it; pending review as the rest):

6. **The form.** `fitsync` gains a binary form of records beside the canonical JSON
   (`sync/binary.go`): strings are a uvarint length and bytes; a value is a tag, `0x10` plus its
   kind (null, bool as a byte, int as a zigzag varint, uint as a uvarint, float as its 8 bytes,
   decimal, string, bytes and json as strings, time as Unix seconds, nanoseconds and offset, date
   as year, month, day), arrays and maps with their length in 4 bytes before their count so that a
   plugin passes over any value without reading into it, and `0x1F` a value in canonical JSON, so
   that one written by hand still goes; the tags are control characters no JSON text starts with,
   so a value tells its form by its first byte. A record is its op and transaction flags as bytes,
   source and namespace, key, before and after, the commit time as RFC 3339 text, the
   transaction id and the position's JSON. A batch is a count and records; what a plugin hands
   back is any number of an index and a record. Alternative: JSON for the records' frame and
   binary values only; an off-the-shelf format (CBOR, protobuf, FlatBuffers), which every guest
   would need a library for.
7. **Negotiated per plugin, both directions.** A plugin that exports `fit_sync_form(offered)`
   gets offered form 1 when an instance is made and returns the form it takes, at most the one
   offered (more fails the load, as a trap does); one that takes it reads its batches (transform,
   encode, write) in it and hands records back through the new import `records_emit(buf, len)`,
   many at once. A plugin that exports none, every one built before, reads JSON and calls
   `record_emit` as before, unchanged; `record_emit` stays JSON in either form. Every instance
   must take what the first took. A plugin built on the new SDKs imports `records_emit`, so it
   needs a fit that has it; the other way round holds. Alternative: a version in a custom section;
   a second set of exports.
8. **A host decoder that allocates little.** `fitsync.BinaryReader` copies what a plugin hands
   back once, as a string its records' strings, positions and bytes are slices of, and carves
   records, map fields and array values out of blocks; `fitsync.BinaryBatch` writes a batch and
   notes where each record's values, commit time and position are, and a record handed back for
   input record i whose part is byte for byte what record i had takes record i's part (values are
   not changed once made) without reading it: a transform that changes the namespace decodes no
   value at all. The plugin's calls keep their buffers in a pool of the plugin's, and the caller
   of `ApplyBatch` is one of its workers. Host allocations a record: about 209 before, 0.18 to
   0.21 now. Alternative: records over the plugin's memory without a copy, unsafe once the
   instance runs again.
9. **The Go SDK speaks it, and keeps a call's memory.** `guest.Value` (a `[]byte` of either form)
   replaces `json.RawMessage` for `Record.Key`, `Before`, `After` and a `Row`'s fields; code that
   uses the helpers and `[]byte` literals builds as it was, code naming `json.RawMessage` for a
   value does not. The helpers read either form and make values in the one taken; `Decimal` and
   `AsDecimal` are new, `Value.MarshalJSON` (and `Record.MarshalJSON`) writes canonical JSON;
   `JSONForm()` in an init keeps a plugin on JSON. `Emit` gathers records in the binary form and
   hands them over as the export returns (or every 64 KiB). The input and the batch's records
   are reused by the next call, which spared TinyGo's allocator most (a batch of 64 went from
   6 µs a record to 0.45 µs): a handler's batch, and what its records hold, are the call's, to be
   copied if kept. fit-examples' `sync-wasm-codec` reads a price by scanning the value's JSON,
   which finds nothing in the binary form: it should use `guest.AsDecimal` (or call
   `guest.JSONForm()`), left to fit-examples. Alternative: fresh memory for each call; values
   converted to JSON for the plugin.
10. **TinyGo plugins built with `-scheduler=none`.** TinyGo's default scheduler starts a
    goroutine for each exported call, which cost about 430 µs a call here whatever the form; a
    sync plugin needs no goroutines, so the tests build with `-scheduler=none` and the module's
    chapter says so. This replaces decision 1's "default flags". Alternative: default flags, and
    a TinyGo plugin ten to a hundred times slower.
11. **The Rust crate speaks it by a default feature.** `fit-guest` exports `fit_sync_form` with its
    default feature `binary`; `default-features = false` keeps JSON, for a plugin that splits a
    batch with `records` and parses it with a JSON library. `parse_batch`, `Row` and the helpers
    read either form and make the one taken (`decimal`, `as_decimal`, `value_json` are new);
    `emit_record` hands a record over at once, `Emitter` gathers them and hands them over when it
    is dropped at the export's end. Alternative: binary only when a plugin opts in.
12. **The tests run each guest in both forms**: every `TestSync*` of decision 1 runs as
    `<guest>/binary` and `<guest>/json`, the latter offering no form (`offerForm`), as fit did
    before, which is what an old plugin sees; `TestSyncBinaryEmit` and `TestSyncFormNegotiation`
    use hand-built modules for what the SDKs would not do; `sync/binary_test.go` covers
    `binary.go` whole. `BenchmarkSyncTransform` is `wasm-<guest>-<form>-<instances>`, with
    `allocs/record`.
13. **Measured, and the gap left.** `BenchmarkSyncTransform`, per record, batches of 64, on an
    Apple M4 Pro (not the benchmark host). Before, in a container (Docker Desktop, 8 CPUs):
    built-in 279 ns; Go 49.3 µs on one instance, 15.2 µs on four; TinyGo 44.9 / 14.7 µs; Rust
    30.8 / 11.4 µs; 209 host allocations a record. After, binary form, the same container:
    built-in 255 ns; Go 3.1 / 2.6 µs; TinyGo 1.5 / 1.4 µs; Rust 1.1 / 1.06 µs; 0.18 to 0.21
    allocations. Four instances barely help in the container: its VM wakes an idle thread so
    late that the other workers start when the first call ends (seen by timing each call), so
    the same build was run natively too (TinyGo and Rust plugins built in the container): built-in
    89 ns; Go 3.2 µs / 1.03 µs; TinyGo 1.5 µs / 502 ns; Rust 1.08 µs / 437 ns. The JSON form is
    as before (Go 51.8 / 12.8 µs natively). So four instances are 4.9 (Rust) to 11.6 (Go) times
    the built-in, not within 20%: what is left is the plugin's own work, reading a batch and
    writing each record back in its memory (Go's WebAssembly code most), and the host's writing
    of the batch (about 110 ns a record); the host's reading is mostly comparisons now. More
    instances, larger `batch_records`, a guest that reads a record's parts only when asked, or a
    mark for "this value as it came" so a guest copies nothing it did not change, would narrow it;
    a plugin that sees every record is unlikely to come within 20% of a built-in that clones a
    record and looks up a map. The benchmark host's run decides.

### I166 Files as a source and a sink

- **Do**: source `file`: paths and globs, scanned each `scan_interval`; each file known by its device,
  inode and a fingerprint of its first bytes, so a rename is followed and a reused inode is a new file;
  truncation starts the file again; a file deleted while read is finished first; lines up to
  `max_line_bytes`, a partial last line held until it ends or `close_after` passes; multiline joins by
  a start pattern; files read at once limited; the checkpoint is every file's offset; a codec
  (`line`, `json`, `csv`, a WASM codec) makes the record. Sink `file`: records written as lines (raw,
  JSON, CSV or a template) through the `logs` file sink, so rotation, retention and compression are
  I155's.
- **Done when**: a tail across rename rotation, copy-and-truncate and deletion loses and repeats no
  line, checked against a writer of numbered lines with a crash at random points; ten thousand files
  stay within the open-file limit; a line past the limit is cut and counted; the sink rotates as the
  log does.
- **Touches**: `sync/file`, `logs`, `docs`.

**Decisions pending review** (made while building it, 2026-11-08; each stands until the user rules on it):

1. **Where it lives.** The source and the sink are package `sync/file` (`syncfile`); the codecs
   `line`, `json` and `csv` are modules of `sync.codecs` in `fitsync` itself (`sync/codec.go`), so
   that Kafka, HTTP and S3 take them too. A module's `codec` is a module reference inside its
   params, loaded with the module's own config by the new `module.Context.Load`; `line` when left
   out. Alternative: a `format` enum per connector.
2. **The checkpoint** is every file's device, inode, fingerprint (hash and length), path, offset,
   a skip flag and its records' key part, and the heads of files truncated lately (below), as JSON.
   Files not read yet, and copies held, are left out; a closed file scans no longer find is
   forgotten once they have missed it for `close_after` (decision 15), so the checkpoint holds at most the files the globs match (`max_files`,
   100000). A position costs the whole list, so the source sets one only between lines, on the
   last record of each pass over the files or of every 1,000 records (it holds one record back to
   put it on), not on every record. Alternative: a position on every record, or a state file of the
   source's own.
3. **A file's identity** is its device and inode and a fingerprint of its first
   `fingerprint_bytes` (1024), taken as the file grows to it; a known inode whose head no longer
   matches, or whose size fell below the offset, is read again from 0. **Windows** has no inode:
   the volume's serial number and the file index from `GetFileInformationByHandle`, files opened
   sharing read, write and delete so the writer may still rename or remove them; a scan opens each
   matched file briefly to learn it. A system with neither (not one fit ships for) uses a hash of
   the path, so a rename is a new file there. Alternative on Windows: path and fingerprint only.
4. **Copy and truncation is followed, not only truncation.** A truncated file's head, offset and
   key are kept for `close_after` (also in the checkpoint); a new file whose head matches one is
   its copy and is read on from that offset under the original's key. A new file whose head
   matches a file not yet truncated is held until it is, then the same; if `close_after` passes
   first, it was a backup and is read whole (repeats rather than losses). Without this, copytruncate
   loses the original's unread tail or reads the copy again. A copy found while it is shorter than
   the original's fingerprint is a new file. Alternative: ignore copies, as Filebeat's inode mode.
5. **Records' keys.** A record its codec gave no key gets `{"file", "offset"}` (and `n` for a
   line's second record on): the file is `<device>:<inode>:<hash of its first line>`, a copy taking
   its original's, so a line read again after a crash has the key it had and an idempotent sink
   folds it. The first line, not a count of truncations, tells a file's contents before and after
   a truncation apart, since a count differs on a replay after two truncations. Caveat: an inode's
   successive contents with the same first line share keys. Alternative: no key; a count.
6. **"Loses and repeats no line"** is tested as: every line arrives, and no line arrives under two
   keys unless the earlier came past the checkpoint its run saved (killed or stopped); repeats under
   one key are at-least-once's, which an idempotent sink folds. A copy found after a crash cannot
   know what its original's run wrote unsaved, so that one case may repeat under a second key.
7. **A line the codec refuses** becomes a message record whose `after` holds `line` and `error`,
   rather than being dropped, or failing the source, which would restart at the same line for ever.
   Alternative: skip and count it; `on_error`.
8. **Partial lines cost no memory**: the source reads from the last whole line's end each pass,
   so a partial line is read again rather than kept; a multiline event is kept (up to
   `max_line_bytes`, per open file), its start being the checkpoint's offset. `close_after` (1m) is
   both how long a partial last line or event waits and when an idle file is closed; a partial line
   growing counts as activity. In `once`, a file's last partial line is taken as it is.
9. **Long lines** are cut at `max_line_bytes` (1 MiB), the rest skipped (the skip survives a
   restart), counted (`Source.LinesCut`) and logged for the first and every thousandth; an event
   over it is cut too. The count is not a metric yet (I188, I189).
10. **Open files.** `max_open_files` (256): past it an idle file (read to its end) is closed to
    open another, a deleted one first, its partial last line taken first since it cannot be opened
    again; a closed file is opened again when a scan finds it grown. A file deleted or renamed out
    of the globs while open is read to its end, and closed once `close_after` passes without growth.
    A file renamed out of the globs while closed with lines unread is lost to the source, as with
    Filebeat. `poll_interval` (250ms) is the wait once every open file is read to its end.
11. **The sink writes through the process's log files** (`logs.DefaultFiles`, a new
    `logs.File.WriteSync`): a batch is acknowledged once its lines are written and the file synced
    on the sink's writer goroutine, in writes of about 64 KiB of whole lines so rotation falls
    between lines; `WriteSync` waits for the disk, which is fine on sync's own goroutines. The file
    opens at the first write, so `fit validate` opens none, and closes at the pipeline's stop. One
    sink per path: another file sink, or fit's log or access log, on the path is refused at the first
    write (the sink then retries), not when the config is checked. `rotate` and `time_zone` are the
    logs' own. Lines come from a codec, or from `template` (`{field}`, `{$namespace}`, `{$op}`,
    `{$key}`, `{$commit_time}`, `{$record}`, `{{`). A record a codec cannot encode is the batch's
    failed record, those before it written. Alternative: a writer of the sink's own; refusing the
    shared path in `fit validate`.
12. **`json` decoding**: an integer is an int, or a uint past int64; a number with a fraction or
    exponent a float; one past a float's range a decimal; members keep their order. `csv` reads a
    row's values as strings, named by `columns` and `c<n>` past them.
13. **Windows' tests**: the test that renames, truncates and deletes files held open skips on
    Windows, whose rules for that differ; the others run there. Vet passes for Windows, FreeBSD and
    Linux; the tests ran on macOS only.
14. **Size**: about 1,500 lines of code and 920 of tests (the ten-thousand-file and crash tests
    included), past the plan's 1,500; nothing is left, so there is no I166b.
15. **Linux reuses inodes at once, and files change between a stat and a read** (found on Linux,
    where the crash test failed 7 runs in 40, after passing on macOS's APFS): four rules follow.
    A closed file whose inode now holds other contents (truncated while closed, or freed and taken
    by a new file, a copy among them) is taken as a file found anew: its old head kept for a copy,
    its new contents matched against the heads kept and the files known. It is checked when a scan
    finds it moved, resized or with another modification time (fixed-width lines make a new file of
    the same size common), and whenever it is opened again. A closed file a scan misses is
    forgotten only once missing for `close_after` (`missing` in the checkpoint), since a rename
    between the directory's listing and its stat hides a file for one scan, and forgetting it at
    once lost its copy's match. Each read is checked against the file's head after it is made: a
    truncation between taking the size and reading discards the read and starts the file again,
    so new contents are never taken at the old offset. And the key's first line comes from the same
    read as the unit's records when the unit is the file's first. Verified: the crash test 100 runs
    in a row on Linux (Docker, 4 CPUs, under the race detector), and 500 in a row at 4 and at 6
    CPUs; 40 on macOS. Alternative: forget at once, and check heads only on a size change.

### I167 Batch jobs, as DataX runs them

- **Do**: `mode: "once"` made a job: the source's snapshot read in parallel chunks (by key ranges where
  the source has them), written with `max_parallel` writers, a progress record (rows read, written,
  failed, ETA) in the checkpoint so a job interrupted resumes from its finished chunks; `fit sync run
  <pipeline> --once` runs one from the command line without a server and exits with its result; a
  schedule (`schedule`, cron syntax) starts a job again.
- **Done when**: a job copying a million rows between two `generate`/`memory` connectors with chunked
  sources is killed half way and finishes on restart without repeating finished chunks; the command
  exits non-zero with the failed records' count; a schedule runs and never overlaps a run still going.
- **Touches**: `sync`, `cli`, `docs`.

**Decisions pending review** (made while building it, 2026-11-08; each stands until the user rules on it):

1. **The chunk interface.** A source that can split its snapshot implements `Chunker` beside
   `Source`: `Chunks(ctx)` plans the whole snapshot when a job starts, and `ReadChunk(ctx, chunk,
   from, emit)` reads one chunk from its start or after a position of the chunk's own. A `Chunk` is
   a namespace, `lo` and `hi` key bounds in the source's own JSON (`lo` included, `hi` not, nil
   unbounded) and the rows it is reckoned to hold. The plan is kept in the checkpoint
   (`job.chunks`), so a job resumed reads the chunks it planned even if the data has changed; at
   most 100,000. A plan of no chunks means the source reads whole (`Run`). I175, I179, I182 and
   I169 implement it. Alternative: chunks found as the job goes (the next chunk's upper bound
   queried when the last ends, as Debezium's incremental snapshots), which keeps the checkpoint
   small for huge tables but plans one chunk at a time.
2. **A chunk is done only once every sink has all its records, through I163's watermark.** The
   job's position is `{"done": "0-41,43", "at": [{"chunk": 42, "from": <the chunk's position>}]}`,
   the state of every chunk as of the record that carries it. The chunks' readers emit through one
   lock, each holding its last record back until the next is read, so a chunk's last record carries
   the position that marks it done; a position goes on that record and on one in every 100. Since
   the watermark passes a record only once every record emitted before it is acknowledged, a saved
   position never claims a chunk, or a record within one, that a sink lacks; the runtime's
   watermark, checkpoint file and crash points are unchanged. Alternative: a watermark per chunk.
3. **Parallel writers only for a chunked snapshot, and only for sinks that say so.** `max_parallel`
   is both how many chunks are read at once and how many writers each `ParallelSink` (a new marker
   interface; `memory` is one, the database sinks will be) gets; a record goes to the writer its
   key's hash (FNV-1a of the key's canonical JSON; by sequence for a record without a key) picks,
   so one key is never written out of order. A job whose source does not split, and every stream,
   keep one writer per sink, since their records carry transactions and a key's successive changes.
   Alternative: a module instance per writer, which the file sink's one sink per path refuses, or
   parallel writes for every job.
4. **Snapshot records lose their transactions**: chunks read at once interleave, so `ReadChunk`'s
   records are each a transaction of their own (`Tx` cleared), and every one ends one.
5. **The progress is the checkpoint's `job` part**: `started`, `finished`, `read`, `written`,
   `failed`, `rows`, `eta`, `chunks_done` and the plan. `written` counts the source records every
   sink acknowledged (one a transform dropped among them), `failed` those `on_error` `skip` or
   `dead_letter` took, both as of the saved position; `read` counts records emitted, those past the
   checkpoint counting again after a restart. The ETA is the plan's rows left at the rate of the
   run under way; none when the source does not reckon rows. `Status` gains `Job` (live) and
   `Next`. The checkpoint's version stays 1: the part is optional. Alternative: a progress file of
   its own.
6. **A finished job does not run again by itself.** A checkpoint whose job finished keeps fit,
   restarted or reloaded, from running it again (its state is `finished`); before I167, `once`
   ran again from its checkpoint, so a file source read what was new. A schedule's next time, or
   `fit sync run`, starts a new job: a chunked source from its start, with a new plan, any other
   source from where the last job ended, so a file or a cursor copies what is new. A new job's
   checkpoint, with its plan, is written as it starts, so that one interrupted before its first
   checkpoint is known to be. I165's connector test, which ran a finished job again by restarting
   the runtime, now does so with `RunPipeline`. Alternative: every new job from the start; or `once` running again at
   every start, as before.
7. **The schedule's syntax**: `job.schedule` in cron's five fields (minute, hour, day of the month,
   month, day of the week; `*`, values, ranges, lists, steps, month and day names, 7 for Sunday, and
   Vixie cron's rule that a day matching either day field runs when both are restricted) or
   `@yearly`, `@annually`, `@monthly`, `@weekly`, `@daily`, `@midnight`, `@hourly`; no seconds
   field, `@reboot` or `@every`. `job.time_zone` is an IANA name or `Local`, `UTC` when left out,
   as the streaming channels' schedules. Times a daylight saving change skips do not run; times it
   repeats run once. The parser is fit's own (`internal/cron`, about 190 lines), no dependency.
   Alternative: robfig/cron, with a seconds field.
8. **No overlap, no catching up.** A pipeline's goroutine runs a job and then waits for the next
   time, so runs never overlap; times passing during a run are skipped (the next run is at the
   first time after it ends), and times passing while fit is down are not made up, except that a
   job interrupted (its checkpoint not finished) goes on at once when fit starts. Alternative: one
   run at once for times missed, as anacron.
9. **`job` is refused unless `mode` is `once`**; `max_parallel` is 4 when left out, at most 256.
10. **`fit sync run [-c config]... [--once] <pipeline>`**, flags before or after the name: it loads
    the config as `fit validate` does (without compiling the rest of it), runs that one pipeline
    and nothing else, and exits. `--once` makes a stream pipeline a job; without it the pipeline
    runs in its mode until it ends or SIGINT or SIGTERM. A finished job starts anew, the schedule is
    not waited for, and a source failing `retry.max_attempts` times in a row stops it
    (`Options.SourceAttempts`), rather than restarting for ever as in a server. The log goes to
    stderr at info, not to the config's log outputs, so as not to write into a running fit's log
    files; the progress to stdout. Exit codes: 0 everything written; 3 records failed, with their
    count on stderr; 1 the pipeline stopped, or a job was interrupted; 2 used wrongly.
    Alternative: exit 1 for failed records too.
11. **`fit sync run` has no fib engine**, so a WASM connector's `http_request` fails under it, and
    it does not check that a running fit is not running the same pipeline, whose checkpoint both
    would write. Alternative: an engine of its own; a lock file in `state_dir`.
12. **The million-row test** kills the job at one of the six crash points, picked at random, once
    half the records are written, and checks that the chunks the checkpoint had done are written
    exactly once, every record at least once, and at most the four chunks under way twice. It copies
    a million records (100 chunks of 10,000) without the race detector, and 200,000 (100 chunks of
    2,000) under it, where a million take some 20 seconds a run; it counts ids in a test-only sink,
    `counter`, since `memory` would keep a million records (about a gigabyte). The schedule's test
    drives a fake `Clock` (`Options.Clock`).
13. **A fix on the way: the `memory` sink and a `BatchError` past the batch.** A `BatchError` whose
    index is the batch's length kept the whole batch in the store while the runtime, taking the
    index as out of range, wrote it all again: I163's `TestSinkFailsMidBatch` failed 14 times in
    1,000 on Linux on `origin/main` (203 records for 200). The store now keeps nothing for an index
    outside the batch, as the runtime reads it; 1,000 runs in a row pass.
14. **Size**: about 1,160 lines of code (the cron parser and `fit sync run` included) and 540 of
    tests, past the plan's 1,500; nothing is left, so there is no I167b.

### I168 Elasticsearch and OpenSearch as a sink

- **Do**: sink `elasticsearch` (OpenSearch through the same, its differences detected): `_bulk` over
  fib's asynchronous HTTP client, `index` or `create` for inserts and updates, `delete` for deletes,
  `_id` from the record's key, the index from the namespace or a template with a date, routing and
  pipeline options; per-item results read, 429 and 5xx retried with backoff, a mapping error sent to
  `on_error`; basic auth, API keys, TLS with a CA.
- **Done when**: interop with Elasticsearch 8 and OpenSearch 2 in Docker: inserts, updates and deletes
  converge after replays; a bulk with one bad document fails only that one; backpressure from 429 slows
  the pipeline without losing records.
- **Touches**: `sync/elasticsearch`, `test/sync`, `docs`.

**Decisions pending review** (made while building it, 2026-11-08; each stands until the user rules on it):

1. **Where it lives.** Package `sync/elasticsearch` (`syncelastic`): the sink and a `Conn` (URLs,
   credentials, TLS, failover, the cluster's `Info`) that I169's source will share. The connection's
   params are declared by the sink itself, since fit's params decoder and schema take no embedded
   structs; the source will declare the same ones. Alternative: a nested `connection` object.
2. **What is written.** An insert, update or message is the row (`after`, or `before` when `after`
   is null) as a plain JSON document (`fitsync.AppendPlain`, now exported: numbers bare, decimals as
   numbers, bytes in base64, times in RFC 3339); a delete is a `delete` by `_id`; DDL is acknowledged
   and not written. Another shape is the `envelope` transform's to make, and keeping deleted rows as
   documents is its `deletes: "rewrite"`. Alternative: an option for the document's shape in the sink.
3. **The `_id`** is the key: a single value's text, a map of one field its value's, a map of several
   their texts joined by `:`, each with `\` and `:` escaped by `\`; bytes in base64, arrays and maps
   as plain JSON. An insert or message without a key gets an `_id` from the cluster (not idempotent,
   documented); an update or delete without one fails for good, and so does an `_id` past 512
   bytes, checked before sending since the cluster would refuse the whole bulk. Alternative: JSON of
   the key's values, unambiguous but awkward to look up.
4. **The index template**: `{namespace}` and dates in `yyyy`, `yy`, `MM`, `dd`, `HH` with `.`, `-`
   and `_`, as Logstash and Beats write them, from the commit time (the write's time when the source
   gives none) in `time_zone`, UTC by default; `{{` for a brace; names not lowercased (a refused name
   fails the record). Alternative: Go layouts, or the namespace lowercased.
5. **`action: "create"`** treats a 409 as written, so a replay into a data stream converges, at the
   cost of updates to documents already there; a delete's 404 is written too. **`routing`** names a
   field of the row (or the key's, for a delete without `before`), and a record without it fails;
   **`pipeline`** is sent on the bulk's URL.
6. **A permanent error, a new runtime notion.** `fitsync.Permanent(err)` marks an error that trying
   again cannot cure; the runtime sends a record failing with one straight to `on_error`, without
   the pipeline's retries. The sink marks so every item refusal other than 429 and 5xx (a mapping
   error, a bad index name), and records it cannot make an item of. Before this, a bad document
   cost each batch `retry.max_attempts` sends with their backoff. Alternative: leave every error to
   the pipeline's retries.
7. **What a failed item means for the others.** Items succeed independently. The sink sends items
   the cluster answered 429 or 5xx again within the same `Write`, alone, with a backoff from 200ms
   doubling to 30s, taking with each the later items of the same document (index, routing and
   `_id`) that were in the batch, so a document's last change stays last even though one before it
   is written again. A 429 is sent again for as long as it lasts (backpressure); a 5xx five times
   (`DefaultItemAttempts`), then the write fails at it. When the write fails, the `BatchError` names
   the first record not written: every record before it is written; records after it that the
   cluster took are sent again by the runtime, which writes the batch again from that record, in
   order, and acknowledges each once. A permanent refusal before every item to retry ends the write
   at once. Alternative: return at the first failure without sending anything again, leaving every
   retry to the pipeline.
8. **A 429 to the whole bulk** waits with the same backoff for as long as the cluster answers so,
   holding the sink's goroutine, so the queue fills and the source stops, as the plan asks; only the
   pipeline's stop ends it. So does a 429 per item. A cluster that answers 429 for good therefore
   stalls the pipeline rather than failing records, which the lag will show (I189). Alternative:
   count 429s against `retry.max_attempts`, which would let `on_error: skip` drop records under load.
9. **Failover**: `urls` in turn, from the one that answered last; a connection's error, a timeout,
   a 502, 503 or 504 moves on to the next; when all fail the write fails whole and the pipeline's
   retry takes it. No node sniffing. Alternative: sniff the cluster's nodes, as the official clients
   can.
10. **Requests bounded by `max_request_bytes`** (8 MiB): a batch larger is sent in several bulks, in
    order; a document past it fails for good. The response is read with `filter_path` to the items'
    status and error, and bounded at 64 MiB.
11. **OpenSearch's differences**: told by the root endpoint's `version.distribution`, asked at the
    first write (the root needs the credentials, so it is asked again after a failure). The only one
    acted on is API keys, which OpenSearch lacks: a root's 401 with an API key says so, and a
    distribution of `opensearch` refuses one. Elasticsearch 7 and 8 and OpenSearch 1 and 2 take the
    same `_bulk`. Alternative: a `distribution` param.
12. **Secrets from the environment only** (`password_env`, `api_key_env`), read when the pipeline
    loads, as the `mask` transform's `key_env`; `admin.store` waits for the pages (I190) to write
    them. TLS with `ca_file`, `cert_file` and `key_file`, `server_name`; no way to skip the check.
13. **The engine.** The sink's client runs on the role's engine (`Options.Engine`), or, when there is
    none (`fit sync run`, tests), on a fib engine of its own made at the first write and stopped at
    `Cleanup`, so `fit sync run` can write to Elasticsearch, unlike a WASM connector's HTTP (I167's
    decision 11). Alternative: refuse to run without an engine.
14. **`ParallelSink`.** The sink takes writes at once (one client, safe for several goroutines),
    so a job gives it `job.max_parallel` writers; the runtime's key hash keeps a document's changes
    on one writer, in order.
15. **The interop tests** are `test/sync` (package `synctest`), skipped without `FIT_SYNC_ES_URL` and
    `FIT_SYNC_OS_URL`, and failing instead with `FIT_SYNC_INTEROP=required`; `test/sync/run.sh`
    starts `docker.elastic.co/elasticsearch/elasticsearch:8.19.3` and
    `opensearchproject/opensearch:2.19.3`, each a single node with security on (TLS from its own
    CA, basic authentication) and a write thread pool of one thread and a queue of one, so a job of
    eight writers gets real 429s (the test checks the cluster's rejected count grew). CI runs it in
    the job `sync-interop` on every push. Alternative: GitHub service containers, which cannot
    hand the tests the CA the node generates.
16. **Size**: about 900 lines of code and 750 of tests (unit tests against a fake cluster, the
    interop tests and their script), past the plan's 1,500 with the docs; nothing is left, so there
    is no I168b.

### I169 Elasticsearch and OpenSearch as a source

- **Do**: source `elasticsearch`: a snapshot of an index pattern with a point in time and
  `search_after` on `_shard_doc`, then, if `cursor` names a field (an update time, a sequence),
  polling by that field with `search_after` on it and the `_id`; the checkpoint is the last sort
  values. Deletes are not seen by polling; the docs say so and suggest a soft-delete field.
- **Done when**: a snapshot of a million documents is complete and in order; polling picks up updates
  written during it, including several with the same cursor value; a restart resumes without gaps.
- **Touches**: `sync/elasticsearch`, `test/sync`, `docs`.

**Decisions pending review** (made while building it, 2026-11-08; each stands until the user rules on it):

1. **What a record is.** The source is `elasticsearch` in the sink's package, declaring the same
   connection params and sharing `Conn`. A record's namespace is the document's own index (`_index`,
   not the pattern), its key the map `{"_id": "<id>"}`, which the sink's `DocID` turns back into the
   same `_id` and a SQL sink takes as a column named `_id`, and its `after` the `_source` read by the
   `json` codec's rules (`fitsync.ParsePlain`, now exported): a number without a fraction an int, a
   uint past int64, otherwise a float, or a decimal past float64; dates stay strings. Alternative:
   decoding the `_id` with the inverse of `DocID` into the key's fields, which would split `_id`s fit
   did not make; or typing values by the mapping.
2. **Operations.** Snapshot records are inserts (there is no `read` op as Debezium's `r`); polled
   documents are inserts when their `_version` is 1 and updates otherwise. A date cursor's value is
   the commit time of polled records; snapshot records have none. Alternative: a new op, or a mark,
   for snapshot reads.
3. **The snapshot** reads through a point in time with `search_after`, on `_shard_doc` in
   Elasticsearch and on `_id` in OpenSearch, which has no `_shard_doc` ("No mapping found for
   [_shard_doc]") and opens points in time at `/_search/point_in_time`, answering `pit_id`. Clusters
   without points in time are refused (Elasticsearch before 7.12, OpenSearch before 2.4); there is
   no scroll fallback for a stream. OpenSearch refuses a point in time of a pattern matching no
   index, which the source reads as empty once `_count` says so. Alternative: scroll for older
   clusters.
4. **A stream's snapshot interrupted starts over.** Its records carry no position but the last,
   which holds where polling starts (or `done` without a cursor): a new point in time's sort values
   are not the old one's, and the source closes its point in time when it stops. `snapshot:
   "cursor"` reads the existing documents by the cursor instead, resumable, for an index too large
   to read again. Alternative: keep the point in time's id in the position and try it after a
   restart, which works only within `keep_alive` and only when the source did not close it.
5. **Polling without sorting on `_id`.** Elasticsearch 8 refuses to sort on `_id` ("Fielddata
   access on the _id field is disallowed"), so polls sort by the cursor alone, `gte` the last value
   with the `_id`s already read at it excluded (`must_not` `ids`), each page a fresh search. The
   position is `{"cursor": <the last sort value>, "ids": [...], "past": bool}`, on each page's last
   record; past 1,000 `_id`s at one value, the rest of that value is read at once through a point in
   time (`past`, then `gt`). Alternative: require `indices.id_field_data.enabled`, or a unique
   tiebreak field in the params.
6. **Late writes are not caught.** There is no overlap window: a document whose cursor is written at
   or below the checkpoint after polling passed it is missed, as the docs say. For a date or
   date_nanos cursor, polls stay `delay` (2s, in whole seconds) behind the cluster's own `now` (date
   math in the query, so fit's clock does not matter), covering the refresh interval; numbers and
   keywords have no delay. Alternative: re-read a window below the checkpoint and drop what was read
   by `_id` and `_version`, at the memory of the window.
7. **Where polling starts**: the cursor's highest value (within `delay`), asked before the snapshot
   opens its point in time, so what changes during the snapshot is polled after it; documents at
   that value are read again (an idempotent sink folds them). `snapshot: "none"` starts there
   without a snapshot. The cursor's type comes from `_mapping/field`, one type across the indices
   (date, date_nanos, numbers, keyword), asked again while no index maps it; date_nanos values are
   sent as epoch milliseconds with a six-digit fraction.
8. **Deletes**: `deleted_field`, a top-level field whose value (not null or false) turns a document
   into a delete of its `_id`, with the document as `before`, in snapshots and polls. Alternative:
   leave it to a transform, which today cannot make a delete from a field.
9. **Chunks for jobs**: only without a cursor (a cursor's job reads as a stream does, so the next
   job reads only what changed), one per `chunk_documents` (100,000) documents by `_count`, at most
   1,024 (`index.max_slices_per_scroll`). Each is a slice of the `_id`'s hash read through a scroll,
   not a point in time: Elasticsearch refuses to slice a point in time by `_id` ("cannot slice on
   [_id] when using [point-in-time]" on a one-shard index), and its default slices, ranges of
   Lucene's document numbers, are not the same in another point in time, so a resumed job would
   read other documents. A chunk interrupted is read again from its start (no position within it);
   each chunk is as its scroll found it, not the whole job at one time. Alternative: chunks by
   ranges of a cursor, which need its percentiles.
10. **Bounds.** Pages of `page_size` (1,000, at most 10,000) documents, halved while answers pass
    `max_page_bytes` (8 MiB) and doubled back under a quarter of it; an answer past the client's
    64 MiB is asked again for half (a scroll's next page cannot change size: it fails, and the
    searches after are halved). A 429 is waited out as the sink does; an answer that timed out or
    failed on a shard fails the run, which restarts from the checkpoint, rather than leave a gap.
    Points in time and scrolls are closed when they end or fail, with a 10s timeout of their own;
    at most one is open for a stream, `job.max_parallel` for a job, `keep_alive` 5m.
11. **No `query` param**: every document of the indices is read; a `filter` transform drops what is
    not wanted. Alternative: a query in the params, applied to snapshots and polls.
12. **The interop tests** read `FIT_SYNC_SOURCE_DOCS` documents (100,000 by default, in CI): a
    snapshot bulk loaded and read whole, each once (OpenSearch's in `_id` order); a stream whose
    snapshot has 125 documents changed and 5 written at one time during it, polled in pages of 50,
    each read once, then a restart from the checkpoint reading only the four written after it (three
    at the same value); a job copying 20,000 documents to another index in seven slices, four at
    once, through the sink. No point in time or scroll is left open. The million, run locally on
    2026-11-08 (`FIT_SYNC_SOURCE_DOCS=1000000`, ES 8.19.3 and OpenSearch 2.19.3, two shards, 512 MiB
    heaps), read whole and once each in 26s and 31s.
13. **Size**: about 710 lines of code and 1,230 of tests (unit tests with a fake cluster, the
    interop tests), past the plan's 1,500; nothing is left, so there is no I169b.

### I170 ClickHouse as a sink and a source

- **Do**: over the HTTP interface on fib's asynchronous client: sink `clickhouse`, batches inserted as
  `JSONEachRow` with `insert_deduplication_token` set from the batch's first and last positions, so a
  replayed batch is dropped by the server; deletes and updates written as rows of a
  `ReplacingMergeTree` with a version column and a deleted flag (or lightweight `DELETE` when
  configured); source `clickhouse`, a snapshot by query and then polling by a cursor column.
- **Done when**: interop with ClickHouse in Docker; a batch sent twice is stored once; the final state
  after replays is right with `FINAL`; the source resumes from its cursor.
- **Touches**: `sync/clickhouse`, `test/sync`, `docs`.

**Decisions pending review** (made while building it, 2026-11-08; each stands until the user rules on it):

1. **Where it lives.** Package `sync/clickhouse` (`syncclickhouse`): the sink, the source and a
   `Conn` of their own (URLs with failover, basic authentication, TLS, `database`, the role's engine
   or one of its own, as I168's), which reads ClickHouse's error code from
   `X-ClickHouse-Exception-Code`. The connection's params are declared by each connector, flat, as
   I168's. Alternative: one HTTP connection package shared with `sync/elasticsearch`.
2. **What a row is.** An insert, update or message is its row (`after`, or `before` when `after` is
   null) as plain JSON (`fitsync.AppendPlain`: numbers bare, decimals as numbers, bytes in base64,
   times in RFC 3339, read with `date_time_input_format=best_effort`, JSON values as objects, which
   ClickHouse reads into a `String` column), plus `_version` (`version_column`) and `_deleted`
   (`deleted_column`, 0). A delete with `deletes: "flag"` is the row as it was (`before`), or the
   key's fields when there is none, with `_deleted` 1. DDL is acknowledged and not written. The
   sink never makes or alters a table: the docs show the `ReplacingMergeTree(_version, _deleted)`
   to make. `plain: true` writes rows without the two columns, for append-only tables.
   Alternative: make the table from the first record's types.
3. **The version** is the commit time in Unix nanoseconds, or `version_field`'s value (a uint, a
   non-negative int, or a time in nanoseconds); the time of the write only when the source gives no
   commit time. It must come from the record, not from the clock: with deduplication, a replay can
   write an older change of a key again under a new token while a later batch it had written is
   dropped as a duplicate, and a clock version would make the older change win. Two changes of one
   key with the same version leave the one inserted last (a binlog's commit times are seconds), which
   is what `version_field` is for. Alternative: a version from the position, which is not a number
   for every source.
4. **The deduplication token** is `fit-` and 40 hex digits of a SHA-256 of the table, the first and
   last records' positions and the count; a record without a position is placed by the nearest one
   before it (`+n`), or after it (`-n`), and a run with no position at all is sent without a token.
   A batch is one `INSERT` per run of consecutive records for one table, bounded by
   `max_request_bytes` (8 MiB), each with its own token. The token works on a replicated table and
   on a plain `MergeTree` only with `non_replicated_deduplication_window` set, which the docs and the
   `table` param's description say; the sink does not check. Alternative: a hash of every record's
   position, which a batch cut differently would also change.
5. **Lightweight deletes turn deduplication off.** With `deletes: "delete"` each run of deletes is
   one `DELETE FROM t WHERE (k1 = v1 AND k2 = v2) OR ...` (at most 128 KiB, below `max_query_size`),
   in order between the inserts, and the inserts are sent with `insert_deduplicate=0`: a `DELETE`
   written again by a replay removes the row a later insert had written, and a deduplicated insert
   would not bring it back. The interop test found it (162 rows for 180). A replay therefore stores
   rows again, which a `ReplacingMergeTree` folds. Keys are compared with `=` to literals (numbers
   bare, strings quoted, times through `parseDateTime64BestEffort`, dates through `toDate`, null by
   `IS NULL`), since ClickHouse refuses an `IN` of tuples whose literal types differ from the
   columns'. Alternative: `IN` with typed query parameters, which needs the key's column types.
6. **Errors.** A data error that names a row (`(at row N)`) fails that record for good
   (`fitsync.Permanent`), and the rows before it in its insert are sent again alone, with a token of
   their own, so only it fails, as I168's bad document; a data error that names no row (a parse,
   conversion or type error code) fails the run's first record for good. Every other error (an
   unknown table or column, authentication, too many parts, a 5xx, the connection) is retried by the
   pipeline, so records are not skipped while a table is being made. The sink does not retry within
   a write: ClickHouse has no per-row 429. Alternative: an unknown table or column as permanent.
7. **`settings`**, a map sent with every query, for `async_insert` and the like; the connector's
   own names (`query`, `database`, `insert_deduplication_token`, `insert_deduplicate`, `param_*`)
   are refused. Alternative: no settings, or named params for the common ones.
8. **The source reads by keyset.** It reads `SELECT * FROM (<table or query>) WHERE (cursor, key)
   > (...) ORDER BY cursor, key LIMIT page_rows`, with the bounds as query parameters of the
   columns' own types and their values as `toString` writes them, so that any type round-trips
   exactly (no float or time formatting of fit's own). The position is `{"after": [{"t": "<type>",
   "v": "<text>"}, ...]}`, the order's columns of the last row read. `key` is required, not read
   from `system.tables`; a query must have the key's and the cursor's columns and no `ORDER BY`,
   `LIMIT` or `FORMAT` of its own. Alternative: the table's sorting key by default.
9. **Rows are inserts** (polling cannot tell an insert from an update; sinks write both as
   upserts), or deletes when `deleted_column` is true or non-zero, so a table the sink wrote reads
   back with `deleted_column: "_deleted"`; a row deleted from ClickHouse is not seen. A `DateTime`
   cursor is the records' commit time. Values are typed by the column's type (`ToValue`): 128- and
   256-bit integers as decimals, `Map`, `Tuple` and `JSON` as JSON. Alternative: `update` for rows
   read after the snapshot.
10. **Late rows are missed.** A row written later with a cursor value at or below one already read
    (two inserts whose parts become visible out of cursor order, a clock behind) is not seen; the
    docs say the cursor must grow, as `DEFAULT now64()`. Alternative: a `poll_lag`, reading only
    rows older than a delay.
11. **`snapshot: "never"`** asks for the greatest cursor value when a pipeline without a checkpoint
    starts; a restart before the first record is checkpointed asks again, missing rows written in
    between. Alternative: a way for a source to save its starting position at once.
12. **Without a cursor** the source reads once by the key and ends, in a stream too, which stops the
    pipeline. Alternative: refuse a stream without a cursor.
13. **I167's `Chunker`.** With `chunk_rows`, a job plans chunks of the key: `count()`, then each
    chunk's `lo` found by `ORDER BY key LIMIT 1 OFFSET chunk_rows` from the one before's, a query a
    chunk, whose sort needs memory for `chunk_rows` keys at most and reads in order when the key is
    the table's sorting key; `lo` and `hi` are the same bounds as the position's. A chunk is read by
    the key in pages; the next job plans again from the start (I167's decision 6). Without
    `chunk_rows` a job reads by the cursor and the next one goes on after it. Alternative: one
    query with `row_number()` over the whole key, which sorts every key at once.
14. **Interop.** `test/sync/run.sh` also starts `clickhouse/clickhouse-server:25.8` (`fit-sync-ch`,
    port 29202, a user `fit` with a password, over HTTP), and the tests (`test/sync/clickhouse_test.go`)
    read `FIT_SYNC_CH_URL`, `FIT_SYNC_CH_USER` and `FIT_SYNC_CH_PASSWORD`, skipping without them;
    CI's `sync-interop` job runs them with the others. TLS is not tried against ClickHouse (its TLS
    needs a config file of its own in the container); the code is I168's. Alternative: a container
    with TLS.
15. **`ParallelSink`**, as I168's: a job gives the sink `job.max_parallel` writers, a key to one.
16. **Size**: about 1,000 lines of code and 500 of tests, within the plan's size with the docs;
    nothing is left, so there is no I170b.

### I171 HTTP as a source and a sink

- **Do**: source `http`, a handler `sync_ingest` on a fit route: a POST of JSON, NDJSON or a codec's
  format becomes records, and is answered 200 only once the pipeline's sinks have acknowledged them,
  or 503 with `Retry-After` when the queue is full or the wait passes `timeout`; the route's
  middleware (auth, limits) applies. Sink `http`: batches posted as JSON or NDJSON with headers and
  auth from the config, retried on 5xx and timeouts, idempotency key from the batch's positions.
- **Done when**: a client that retries until answered loses nothing across a fit restart; a full queue
  answers 503 without blocking a worker; the sink's retries carry the same idempotency key.
- **Touches**: `sync/httpsync`, `handler`, `docs`.

**Decisions pending review** (made while building it, 2026-11-08; each stands until the user rules on it):

1. **Where it lives, and how the handler finds the source.** The source and the sink are package
   `sync/httpsync` (`httpsync`); the handler `sync_ingest` is package `handler/syncingest`. The
   handler names the pipeline (`pipeline`), and finds its source through a registry of the process
   (`httpsync.Lookup`), where the source is while its `Run` runs; `fitsync.Run` gains `Pipeline`, the
   name, for it. While no source is registered (the pipeline not in the config, restarting after a
   failure, stopped) the handler answers 503. Alternative: a name of the source's own (`endpoint`)
   the handler names, or the handler inside `httpsync`.
2. **Answered at the watermark, not at the checkpoint.** A new optional interface,
   `fitsync.Acknowledger` (`Acknowledged(pos)`), is told each position the watermark moves to, as it
   moves, outside the run's lock; the source answers 200 every request up to it. `Confirmer` would
   answer only once the checkpoint is saved, up to `checkpoint_interval` later, and the HTTP source
   has nothing to resume from: once every sink has a request's records they are safe. Alternative:
   `Confirmer`.
3. **A request is a transaction.** Its records, when more than one, carry `Tx.ID`
   `http-<run>-<n>` with `First` and `Last`, and its last record the position
   `{"run": "<random>", "request": <n>}`, so the watermark never stops inside a request and a SQL
   sink may write one in a transaction of its own. The position is saved in the checkpoint like any
   other and ignored at a restart. Records get `source` `http`, `namespace` (`http`) when they have
   none, and the time the request came as their commit time when they have none.
4. **What `on_error` took counts as acknowledged.** A record a transform or sink failed, skipped or
   sent to `dead_letter`, lets its request be answered 200: the pipeline's policy decides what a
   failed record is, as for every source. With `on_error: stop` the pipeline stops, and the requests
   waiting are answered 503 at its stop (the module's `Cleanup`) or at their `timeout`.
   Alternative: answer such a request 422 or 500.
5. **Bounds and backpressure.** The handler reads the body on the worker as `OnBody` delivers it,
   into a buffer bounded by its `max_body_bytes` (8 MiB, at most 1 GiB; 413 past it), and hands it to
   the source without waiting (`Offer`); the source decodes on the pipeline's goroutine, so a WASM
   codec never runs on a worker. The source bounds what it holds: `max_pending_requests` (1024)
   counts a request from `Offer` until it is answered for good, a request whose handler has answered
   503 on its timeout still counting until its records are acknowledged; `max_pending_bytes`
   (64 MiB) counts bodies not yet decoded (one body always fits when none waits). A full queue holds
   the source in `Emit`, its pending requests fill, and the next request is answered 503 at once.
   Alternative: the pipeline's `queue` alone, with a non-blocking `Emit` the runtime would add.
6. **The body's formats.** Without `codec`, a body is a sequence of JSON values, each an object or an
   array of objects, so one parser reads JSON (one object or an array) and NDJSON alike; its
   `Content-Type` is `application/json`, `application/x-ndjson`, `application/ndjson`,
   `application/jsonl`, `application/x-jsonlines`, a `+json` type, `text/plain` or none, any other
   415. `form` is the `json` codec's (`plain` rows or whole `record`s). With `codec`, any
   `Content-Type`, and `framing` `lines` (each line, empty ones skipped, `\r` dropped) or `body`.
   A body is decoded whole before any record is emitted: one bad value answers 400 and writes
   nothing. Alternative: one format per `Content-Type`, strictly.
7. **At least once, no deduplication in the source.** A request answered 503 on its timeout may
   still be written, and a client that sends it again writes it twice; an idempotent sink folds it
   when the records have keys (`form: "record"`, or a `key` transform). The source does not keep the
   clients' `Idempotency-Key`s. Alternative: keep the keys seen for a while and answer a repeat 200
   without writing it.
8. **The answers**: JSON, `{"records": n}` with 200 and `{"records": 0, "error": "..."}` otherwise;
   405 with `Allow: POST` for another method; `Retry-After` from `retry_after` (1s) in whole seconds,
   at least 1; `Cache-Control: no-store`. An empty body is 200 with no records, at once.
9. **No cross-check at validation.** A `sync_ingest` naming a pipeline that is not there, or whose
   source is not `http`, is not refused by `fit validate`: a module's `Provision` does not see the
   config's `sync`. It answers 503 at run time. Alternative: a check in the config's validation.
10. **Restarts and stops.** A pipeline restarting after its source failed answers 503 every request
    its earlier run emitted and did not have acknowledged (that run's records are gone with it); a
    pipeline stopped by a reload or fit's stop stops taking requests (those not yet decoded get 503),
    lets the sinks write what they hold, which answers their requests 200, and answers the rest 503.
    In a job (`mode: "once"`) the source never ends.
11. **The sink's retries are the pipeline's.** A 5xx, 408, 429, a timeout or a connection's error
    fails the write, and the pipeline's `retry` sends it again from the request that failed (those
    before it acknowledged through a `BatchError`); any other answer that is not a 2xx, a 3xx among
    them since fib's client does not follow redirects, is `Permanent`, to `on_error` at once. A 429
    is not waited out as the `elasticsearch` sink's is, nor `Retry-After` read: it counts against
    `retry.max_attempts`. Alternative: wait out 429 and honour `Retry-After`, as I168's decision 8.
12. **The idempotency key** is 32 hex digits (128 bits) of a SHA-256 of each record's position,
    length first, and the request's body, in `idempotency_header` (`Idempotency-Key`). Positions alone
    do not tell batches apart, since most records carry none (the file source puts one on every
    thousandth, the HTTP source on a request's last); the body alone would make two batches of the
    same lines at different places one. A request sent again has the same records and body, so the
    same key; a batch split by `max_request_bytes` is split the same way again from the record that
    failed, so the request that failed comes back with its key. A replay after a crash batched
    otherwise gets other keys: the receiver still needs idempotent writes. Alternative: the first and
    last positions only, as I170's deduplication token.
13. **The sink's shape.** `format` `ndjson` (default) or `json` (an array), `form` `plain` or
    `record`, or a `codec` with `content_type`; `method` `POST` or `PUT`; `headers`, which may not
    set `Authorization`, `Content-Type`, `Content-Length`, `Host` or the idempotency header; basic
    authentication (`username`, `password_env`) or a bearer token (`bearer_token_env`), from the
    environment only as I168's; TLS with `ca_file`, `cert_file`, `key_file`, `server_name`, never
    skipped; one `url`, no failover; responses read up to 1 MiB, their first 300 bytes in errors; the
    role's engine, or one of its own (`fit sync run`); a `ParallelSink`. Alternative: several URLs
    with failover, as the `elasticsearch` sink's.
14. **The tests.** The restart test serves the route and runs sync in the test's process, stops the
    listener, the config and the sync runtime while eight clients post 400 records (each sent again
    until answered 200), and starts them again on another address: every record is in the sink. The
    `memory` sink's store, which outlives the runtime in the process, stands in for a durable sink.
    The full-queue test holds the sink, answers the second request 503 with `Retry-After` at once,
    and answers twenty requests to another route meanwhile. The sink's test fails a request with a
    503 and then a timeout, and the three sends carry one key. The tests ran on macOS under the race
    detector; by the scope the user set for this round, not on Linux, and not the `./sync/...` and
    `./handler/...` sweeps, which the merge runs.
15. **Size**: about 1,000 lines of code (with the runtime's hook) and 940 of tests; nothing is left, so there is no I171b.

### I172 A MySQL client: connections, authentication and queries

- **Do**: package `sync/mysql/wire`: the handshake, TLS, `mysql_native_password` and
  `caching_sha2_password` (fast path, full authentication over TLS or with the server's RSA key),
  text queries and result sets, prepared statements with the binary protocol, multi-row inserts with
  parameters, transactions, timeouts on every call; MySQL 5.7, 8.0, 8.4, MariaDB 10.11 and 11.
- **Done when**: interop in Docker with each version: every column type round-trips through a
  prepared statement; authentication with each plugin; a server closing the connection mid-result is
  an error, not a hang.
- **Touches**: `sync/mysql/wire`, `test/sync`.

**Decisions pending review** (made while building it, 2026-11-08; each stands until the user rules on it):

1. **Where it lives.** Package `sync/mysql/wire`, named `mysqlwire`: a library with no module, params
   or schema of its own; I174–I176 declare the connector's params and hand them to `mysqlwire.Config`.
   Alternative: the name `wire`, which I177's PostgreSQL package would share.
2. **A blocking connection on a goroutine, not fib's.** A `Conn` is a `net.Conn` (with `crypto/tls`)
   used by one goroutine at a time, as decision 036 says databases' connections are: few, long, each
   on a goroutine of its own, off fib's workers. MySQL's protocol is a request and its answer, a
   result set streamed in order, then (I174) the binlog read in order; fib's asynchronous conns
   would need the packets reassembled on fib's loop and handed to the pipeline's goroutine anyway,
   with backpressure of their own. Alternative: a parser on fib's loop feeding a channel.
3. **Timeouts.** `Config.Timeout` (60s) bounds every wait for the server, the dial and each read and
   write of a call, not the whole call, so a long result read steadily is not cut; a call's context
   bounds the call (its deadline and its cancellation). A call ended by either closes the connection,
   as does any error of the stream, since what the server sends next can no longer be told apart; the
   server's own errors (`*Error`) leave it usable. A cancelled query is not killed from a second
   connection (`KILL QUERY`): the server stops it when it finds the connection gone. Alternative: a
   second connection that kills the query.
4. **TLS.** A `Config.TLS` is required, never "preferred": a server that does not offer it is refused,
   and the name verified is the address's host unless the config names one. MySQL 5.7's official
   image offers only RSA and DHE key exchange, of which Go has only RSA's, off by default since Go
   1.22: a 5.7 client must list the `TLS_RSA_*` suites in its `tls.Config`, as the interop test does;
   the connector (I174) decides whether to offer a param for it. Alternative: the RSA suites turned on
   by the package for every server.
5. **Password plugins**: `mysql_native_password`; `caching_sha2_password` with its fast path and its
   full authentication, sent in clear over TLS or a Unix socket, else encrypted with
   `ServerPublicKey`, else with the key asked of the server when `AllowPublicKeyRetrieval` is set
   (off by default: a man in the middle can answer with its own); `sha256_password` the same way;
   `mysql_clear_password` only over TLS or a Unix socket, or with `AllowCleartextPassword`. A greeting
   naming a plugin the client does not have is answered with `mysql_native_password`, and the server
   switches to the user's. Not supported: MariaDB's `client_ed25519` and `parsec`, GSSAPI and the
   pre-4.1 hash, each an error naming it. Alternative: `client_ed25519`, which needs MariaDB's own
   Ed25519 variant written in fit.
6. **Values are typed by their column**, the same from the text protocol and the binary one:
   `int64` or `uint64` (by `UNSIGNED`) for the integers and `YEAR`, `float32` for `FLOAT`, `float64`
   for `DOUBLE`, `mysqlwire.DateTime` for `DATE`, `DATETIME` and `TIMESTAMP` (no zone; the zero date
   and zero parts kept, which `time.Time` cannot hold), `time.Duration` for `TIME`, `nil` for NULL,
   and `[]byte` for the rest (strings, `DECIMAL` as its text, `BIT`, `ENUM`, `SET`, `JSON`,
   `GEOMETRY`), whose meaning the column's type and collation give. A row's `[]byte` point into the
   connection's buffer until the next `Next`, so a snapshot is read without a copy a value. Mapping to
   `fitsync.Value` is I174's and I175's. Alternative: `fitsync.Value` straight from the wire.
7. **Parameters** are Go values: `nil`, `bool`, the integer kinds (as `BIGINT`, signed or not),
   `float32`, `float64`, `string`, `[]byte` and `json.RawMessage` (all as `MYSQL_TYPE_STRING`, so a
   JSON column takes them on every server), `time.Time` (a `DATETIME` in `Config.Location`, UTC by
   default), `DateTime` and `time.Duration`; anything else is refused before sending.
8. **`Exec` and `Query` with arguments prepare, execute and close** the statement each time, without a
   cache of statements; without arguments they send text. `Prepare` gives a statement to run many
   times. Alternative: a cache, or client-side interpolation.
9. **Multi-row inserts** (`InsertRows`): `head`, a `(?,...)` per row and `tail` (`ON DUPLICATE KEY
   UPDATE ...`), in as few statements as 65,535 placeholders and the server's `max_allowed_packet`
   (asked once per connection, with 1 KiB of slack) allow; a statement is prepared once per count of
   rows in a call and closed at its end. The statements are not one unit unless run in a
   transaction (`Begin`, `Commit`, `Rollback`, `InTransaction` from the server's status).
10. **One call at a time.** A result's rows are read or closed before the next call (`ErrBusy`
    otherwise); `Rows.Close` reads what is left rather than breaking the connection. Several result
    sets (a procedure's) are walked with `NextResultSet`.
11. **Bounds**: a packet read at most `MaxPacketSize` (64 MiB) with its row, buffers grown past 1 MiB
    let go after use, 4,096 columns at most.
12. **Left out**: compression, `LOAD DATA LOCAL` (answered with an empty file and refused),
    `CLIENT_MULTI_STATEMENTS`, `COM_STMT_SEND_LONG_DATA` (a value must fit `max_allowed_packet`),
    cursors, connection attributes and session tracking. The replica's commands
    (`COM_REGISTER_SLAVE`, `COM_BINLOG_DUMP_GTID`) and a raw packet API for them are I174's.
13. **Interop.** `test/sync/mysql.sh`, sourced by `test/sync/run.sh` and runnable alone (`-run MySQL`),
    starts `mysql:5.7` (as `linux/amd64`), `mysql:8.0`, `mysql:8.4` (with
    `--mysql-native-password=ON`), `mariadb:10.11` and `mariadb:11` (`fit-sync-my-*`, ports
    29210–29214), each with TLS from a CA made by `openssl`, a root password and a
    `max_allowed_packet` of 64 MiB; the tests (`test/sync/mysql_test.go`) read `FIT_SYNC_MYSQL`
    (`name=host:port,...`), `FIT_SYNC_MYSQL_PASSWORD` and `FIT_SYNC_MYSQL_CA` and skip without them.
    The CI job's name still lists the systems before MySQL; `.github` was not touched. Alternative: a
    job of MySQL's own.
14. **Size**: about 1,630 lines of code, 1,890 of unit tests against an in-process fake server (96.8%
    of statements, no containers) and 700 of interop; nothing is left, so there is no I172b.

### I173 The MySQL binlog: events and values

- **Do**: decoding of the binlog: format description, rotate, GTID (MySQL's and MariaDB's), query,
  table map with optional metadata, `WRITE`/`UPDATE`/`DELETE_ROWS` v1 and v2, partial JSON updates,
  XID, heartbeat; CRC32 checksums checked; every column type to the record's values (decimal, temporal
  types with fractions, `BIT`, `SET`, `ENUM`, `GEOMETRY` as bytes, JSON's binary form).
- **Done when**: binlogs captured from each server version in testdata decode to the rows the tests
  wrote; a fuzz test over the decoder neither panics nor allocates past its bounds.
- **Touches**: `sync/mysql/binlog`.

**Decisions pending review** (made while building it, 2026-11-09; each stands until the user rules on it):

1. **Where it lives.** Package `sync/mysql/binlog` (`binlog`), a library like `mysqlwire`: a `Decoder`
   that takes one whole event at a time (header, body and checksum) and keeps the format description
   and the table maps, and a `Reader` of binlog files over it. The replica's commands
   (`COM_REGISTER_SLAVE`, `COM_BINLOG_DUMP_GTID`) stay I174's. Alternative: a decoder reading the
   stream itself.
2. **Values are `fitsync.Value`s**, as the plan says, not `mysqlwire`'s types: integers `Int`, or `Uint`
   when the column is unsigned (`YEAR` an `Int`); `FLOAT` its shortest decimal as a `Float`; `DECIMAL`
   a `Decimal`; `DATE` a `Date`; `DATETIME` a `Time` in `Decoder.Location` (UTC by default);
   `TIMESTAMP` a `Time` in UTC; a zero date, or one with zero parts, a `String` as MySQL writes it;
   `TIME` a `String` (`[-]HH:MM:SS[.frac]`, fitsync having no duration); `BIT` a `Uint`; `ENUM` and
   `SET` the `String` of their names when known, else a `Uint` of the index or bits; character strings
   `String`s of their bytes in the column's charset (not converted), binary ones (charset 63) `Bytes`,
   with the charset unknown `CHAR`/`VARCHAR` `String` and `BLOB`/`TEXT` `Bytes`; JSON (MySQL's binary
   form) `JSON` text as MySQL prints it; `GEOMETRY` and `VECTOR` `Bytes`. Alternative: `mysqlwire`'s
   types, mapped by I174.
3. **The source completes a table map.** A `TableMap`'s `Columns` are exported; what the optional
   metadata lacks (5.7, MariaDB's default `NO_LOG`, MySQL's `MINIMAL` without names) I174 fills in
   from `information_schema` (name, `Unsigned`, `Charset`, `EnumValues`, `SetValues`) before the rows
   events after it are decoded, in `Decoder.OnTableMap`, called with each table map before the rows
   after it (also inside a transaction payload, whose rows are decoded in the same call);
   `TableMap.Metadata` says which pieces the server sent. The tests do the
   same from the workload's description. Observed and followed: MySQL counts `YEAR` among the numeric
   columns of the signedness bitmap, and MariaDB counts `GEOMETRY` among the character columns of the
   charset metadata.
4. **Checksums.** CRC32 checked on every event when the format description says so; the description
   itself checked; `SetChecksum` for the events a replica gets before it (the artificial rotate).
5. **Partial JSON updates** (`binlog_row_value_options=PARTIAL_JSON`) are applied: the after value is
   the before value with the diffs (replace, insert, remove) applied, so a sink always gets the whole
   document; without the before value (`binlog_row_image` not `FULL`) the event is refused
   (`ErrUnsupported`). Alternative: the diffs passed on.
6. **MySQL 8.3's tagged GTIDs and 8.0.26's heartbeat v2** are decoded from MySQL's serialization
   format (version, size, last required field, then field IDs and variable-length values; a UUID as 16
   integers; GNO and the logical clock zigzag-signed), worked out from MySQL's sources and checked
   against 8.4's binlog with a tagged transaction; the tagged previous-GTIDs format too.
7. **Compression.** MariaDB's compressed query and rows events (zlib, standard library) and MySQL's
   `Transaction_payload` (`binlog_transaction_compression`, ZSTD through
   `github.com/klauspost/compress/zstd`, which fit already depends on for Kafka) are decoded; a
   payload's events come out in `Payload.Events`, its uncompressed size bounded by `MaxEventSize`
   (read as the stream gives it, the decoder's window and memory bounded the same). The interop
   workload turns compression on for a transaction on MySQL 8.0. Alternative: payloads refused, with
   `binlog_transaction_compression=OFF` required by I174.
8. **Not decoded further** (`*Unknown`, the body's bytes): `Intvar`, `Rand`, `User_var`, `Stop`,
   `Incident`, `Ignorable`, `XA_prepare`, view changes, the v0 rows events. Refused: pre-5.0
   `DECIMAL`, MariaDB's pre-10.3 temporal format (`mysql56_temporal_format=OFF`), and an encrypted
   MariaDB file (`Start_encryption`; a replica's stream comes decrypted).
9. **Bounds**: an event at most `MaxEventSize` (64 MiB); the values of one rows event at most
   `MaxRowsSize` (64 MiB, or twice the event if more), reckoned as fitsync does (32 bytes a value and its
   text); 4,096 columns; `MaxTables` (4,096) table maps, past which all are forgotten (each transaction
   carries its own); JSON 100 levels deep and its text at most 16 times its bytes (offsets may share a
   value); a row that takes no bytes refused. The `Reader`'s buffer and a decompressed event grow with
   the bytes actually read, never with the length a header claims. A fuzz test (`FuzzReader`) checks
   no panic and allocation within 2,048 times the input plus 4 MiB; `TestMutations` decodes every
   captured event with each byte changed and cut at each length.
10. **Interop and testdata.** `test/sync/mysql.sh` now starts every server with the binlog on in row
    format, GTIDs on for MySQL, `secure_file_priv` off and `UMASK=0644` (MariaDB's and 5.7's
    `LOAD_FILE` read only files readable by all); `TestMySQLBinlogInterop` (in `sync/mysql/binlog`,
    which `mysql.sh` and `run.sh` now also run) runs `testdata/workload.json` in a binlog file of its
    own, reads the file with `LOAD_FILE` and checks it decodes to the workload's rows (those of its own database: `go test` runs
    `./test/sync` beside it, writing to the same servers); with
    `FIT_SYNC_MYSQL_BINLOG_TESTDATA` it writes the file, as `testdata/*.binlog` were captured (8.4 and
    MariaDB 11 with `binlog_row_metadata=FULL`, set and restored by the test; 8.0 `MINIMAL`; 5.7 and
    MariaDB 10.11 none). `TestCapturedBinlogs` checks them without containers. Alternative: the replica
    protocol, which is I174's.
11. **Size**: about 2,870 lines of code (with comments), 1,710 of unit tests (99.6% of statements, no
    containers) and 260 of interop and workload; nothing left, so no I173b.

### I174 MySQL and MariaDB as a source

- **Do**: source `mysql`: connects as a replica with its `server_id`, checks `binlog_format=ROW` and
  `binlog_row_image=FULL`, starts from the checkpoint's GTID set (or file and offset), else from now;
  column names from `binlog_row_metadata=FULL` where the server has it, else from
  `information_schema` read at start and again after each DDL in the stream; `tables` include and
  exclude by glob; a checkpoint at each XID; DDL as `ddl` records; heartbeats for lag; a reconnect
  resumes from the last delivered transaction.
- **Done when**: interop with each version: inserts, updates and deletes across many tables, a DDL
  mid-stream, a server restart and a binlog purge (an error saying a new snapshot is needed, not
  silence); `kill -9` of fit at random points loses no transaction.
- **Touches**: `sync/mysql`, `test/sync`, `docs`.

**Decisions pending review** (made while building it, 2026-11-09; each stands until the user rules on it):

1. **Where it lives.** The source `mysql` (`sync.sources.mysql`) in package `sync/mysql` beside I176's
   sink, in `source*.go`; it takes the sink's connection params but `database` (through the same
   `wireConfig`, so `tls_rsa` serves MySQL 5.7 here too), and `server_id` (required, not 0),
   `tables` and `exclude` (`db.table` globs; every table but those of `mysql`, `sys`,
   `information_schema` and `performance_schema` when left out), `keys` and `heartbeat`.
2. **The replica's commands in `mysqlwire`**, additive: `Conn.Command` (a command answered by OK, as
   `COM_REGISTER_SLAVE`), `Conn.StartBinlog` and `Conn.ReadEvent` (a binlog packet's event; `io.EOF`
   at a non-blocking dump's end; the server's `*Error`), the commands' constants exported. A
   connection that streams is the stream's alone: any other call is `ErrBusy`, and `Close` sends no
   `COM_QUIT`.
3. **Two connections**: the stream, and a second, made when first needed, for `information_schema`
   and `SHOW BINARY LOGS` while the first streams. Both on the pipeline's goroutine.
4. **By GTIDs where the server has them.** MySQL with `gtid_mode = ON`: `COM_BINLOG_DUMP_GTID` with
   `BINLOG_THROUGH_GTID` and the checkpoint's set; MariaDB (always): `@slave_connect_state` (strict
   mode off) and `COM_BINLOG_DUMP`; otherwise the file and offset. The position always holds both,
   `{"gtids", "file", "pos"}`, the GTID set as the server writes it (MariaDB's `gtid_binlog_pos`
   form, the last GTID of each domain). A set with MySQL 8.3's tagged GTIDs resumes by file and
   offset: their encoding in the dump command is not written yet. Alternative: tagged GTIDs encoded
   too, from MySQL's sources and checked against 8.4.
5. **From now, and no checkpoint before the first record.** Without a checkpoint the stream starts at
   the server's binlog position now (`SHOW BINARY LOG STATUS` from 8.2, else `SHOW MASTER STATUS`,
   and MariaDB's `gtid_binlog_pos`); a transaction of tables not read moves no checkpoint, as
   I182's decision 11 has it. So a pipeline on a quiet table reads again, after a restart, what the
   server wrote since its last record, and the server purging those binlogs makes it ask for a new
   snapshot though nothing of its tables was lost. Alternative: a record without a row that carries
   the position (the record model has none), or the position kept by the source outside the
   checkpoint.
6. **Column names from the binlog, else `information_schema`.** A table map with names
   (`binlog_row_metadata = FULL`) is used as it is, its primary key with it; otherwise the columns
   (name, `unsigned`, binary or not, `ENUM` and `SET` values) and the primary key
   (`STATISTICS`) are read once per table and kept until the next DDL in the stream, which clears
   them all. Events written before a change of the table are named as the table is now: when the
   counts differ, the columns past the shorter are named `@n` by position, with a warning. A
   column's character set is told only as binary or not (strings keep their bytes, I173's decision
   2). Alternative: a history of the schema replayed from the DDL, as Debezium keeps.
7. **Keys and key changes.** The key is the primary key, or `keys`' columns for a table; a table with
   neither has records without a key, which a sink writing by key refuses (for the snapshot, I175
   refuses such a table). An update whose key changed is a delete of the old key and an insert of the
   new row, in its transaction, as I178's decision 5. Alternative: an update whose `before` holds the
   old key, which the `mysql` sink handles but others do not.
8. **Transactions.** A record's `Tx.ID` is the GTID (`uuid:gno`, `uuid:tag:gno`, MariaDB's
   `domain-server-sequence`), or `file:offset` of the transaction's first event without GTIDs; the
   source holds one record back so the last is flagged, and only the last carries the position, the
   file and offset after its `XID` (or `COMMIT`); a transaction compressed in a payload is read as
   any. A DDL (`CREATE`, `ALTER`, `DROP`, `RENAME`, `TRUNCATE`) is a transaction of its own and a
   `ddl` record of `db.table` when its table is told (from the statement, its database the
   statement's default), else of `db` when the source reads that database, `after` the
   `statement` and the `database`; other statements (`GRANT`, `FLUSH`, ...) are passed over.
9. **Refusals stop rather than lose.** `log_bin`, `binlog_format = ROW` and `binlog_row_image = FULL`
   are checked at each connection (and by **Test connection**); a rows event without every column
   (a session's `binlog_row_image`) and a statement-based change of a database read (a session's
   `binlog_format`) are errors, which restart the pipeline from its checkpoint until fixed by hand.
   Alternative: such events passed over with a warning.
10. **A stream cut is read again where it was.** An error of the connection (not the server's)
    reconnects up to five times in a row (100 ms, doubling) from the last transaction handed on; a
    transaction cut inside is read again whole and its records already handed on are passed over by
    count, the binlog being the same. The server's errors go to the pipeline: 1236 (a position it no
    longer has, binlogs purged) as an error saying a new snapshot is needed.
11. **Lag in bytes** (I189's `Run.ReportLag`): 0 at each heartbeat outside a transaction (the server
    had nothing to send), and at most every 10 seconds at a transaction's end the bytes of the
    server's binlogs past the position (`SHOW BINARY LOGS` on the second connection: the rest of the
    file read and the files after it; nothing reported when it is refused, which needs `REPLICATION
    CLIENT`). Not counted while a long transaction is read. Alternative: GTIDs behind, which a server
    without GTIDs and MariaDB's domains make uneven.
12. **Heartbeats**: the session sets `@master_heartbeat_period` and `@source_heartbeat_period` (8.0.26
    renamed it; both names, harmless where unknown) to `heartbeat`, 10 s, which must be below
    `timeout` since every read waits at most that; the checksum likewise, `@master_binlog_checksum`
    and `@source_binlog_checksum`; MariaDB's `@mariadb_slave_capability = 4` for its GTID events.
13. **A job** (`mode: "once"`) asks for a non-blocking dump: the server ends the stream at its binlog's
    end and the source stops there (I175 makes a job the snapshot).
14. **Interop** (`test/sync/mysql_source_test.go`, on `test/sync/mysql.sh`'s five servers): changes
    across two tables in one transaction, an update, a key changed, a DDL mid-stream and rows after
    it, deletes, compared record by record; a restart from a checkpoint; the binlogs purged for real
    under a checkpoint (an error asking for a snapshot); the connection test; and a MySQL-to-MySQL
    pipeline through I176's sink under a mixed write load, killed at random points (`Options.Crash`)
    and, with `FIT_SYNC_MYSQL_PREFIX` (which `mysql.sh` now exports), the server restarted with
    `docker restart` under it, ending equal by checksum. The purge takes a lock file
    (`$TMPDIR/fit-sync-mysql-binlog.lock`) that `TestMySQLBinlogInterop` now takes too, since `go
    test` runs that package beside `./test/sync` on the same servers and it reads a binlog file of its
    own.
15. **Size**: about 1,430 lines of code (`mysqlwire` 74 of them), 1,240 of unit tests against a fake
    connection with built events (the package at 96.5% of statements without containers) and 420 of
    interop; nothing left, no I174b.

### I175 The MySQL snapshot and its hand-over to the binlog

- **Do**: `snapshot: "initial"` (when there is no checkpoint), `"always"` or `"never"`: the binlog
  position read first (`SHOW BINARY LOG STATUS` or `SHOW MASTER STATUS`, GTID executed), then each
  table read in chunks by primary key in a `REPEATABLE READ` transaction with a consistent snapshot,
  without global locks; the stream then starts from the position read first, so changes during the
  snapshot are applied again and converge in idempotent sinks; chunk progress in the checkpoint;
  tables without a primary key refused unless a key is named.
- **Done when**: a snapshot of tables written to during it ends with the sink equal to the source
  (checked by a checksum of both); an interrupted snapshot resumes at its next chunk.
- **Touches**: `sync/mysql`, `test/sync`.

**Decisions pending review** (made while building it, 2026-11-09; each stands until the user rules on it):

1. **Params.** `snapshot`: `initial` (the default, as the plan's sketch and the `mongo` source have
   it), `always` or `never`; `batch_size`, the rows a query reads (1,000, at most 100,000); and
   `chunk_rows`, a job's chunk (100,000).
2. **The hand-over without locks.** The binlog's position (`SHOW BINARY LOG STATUS` from 8.2, else
   `SHOW MASTER STATUS`, with MySQL's executed GTIDs or MariaDB's `gtid_binlog_pos`) is read first, on
   the snapshot's connection, then the tables; the stream then starts from that position, so what
   changed while the tables were read comes again and converges in idempotent sinks. No `FLUSH TABLES
   WITH READ LOCK`, no table locks.
3. **A transaction a page.** Each query of `batch_size` rows runs in a `START TRANSACTION WITH
   CONSISTENT SNAPSHOT` of its own, in `REPEATABLE READ`, rather than one transaction for the whole
   snapshot: a long transaction holds back the server's purge of old row versions on a busy server,
   and the hand-over needs no single view, since the stream replays everything after the position.
   Alternative: one transaction per table, or for the whole snapshot (one view, at that cost).
4. **Keyset pages.** Tables are read in the order of their `db.table` names, each by its key, `WHERE
   (k1, k2) > (?, ?) ORDER BY k1, k2 LIMIT n`, the last key read as parameters (times as UTC text: the
   session's `time_zone` is `+00:00`).
5. **One record for one row, whichever reads it.** The client's values are typed as the binlog's
   (`binlog.Column`): integers (unsigned ones `Uint`, `YEAR` an `Int`), `FLOAT` as its shortest
   decimal, decimals, dates, `DATETIME` in UTC and `TIMESTAMP` the instant, zero dates and `TIME` as
   the server's text with the column's digits, `BIT` a `Uint`, `JSON`, binary strings and `GEOMETRY`
   as bytes, the rest as text; so a row's snapshot record and its stream record are the same.
6. **Progress in every record.** A snapshot record has no transaction and carries the stream's start
   with `{"snapshot": {"table": "db.table", "key": <the key's map in canonical JSON>}}`; the
   snapshot's last record carries the start alone, so a restart after it reads the stream. A snapshot
   interrupted resumes after the last key saved, finer than a chunk. A snapshot with no rows saves
   nothing, and is read again on a restart (as I182's decision 11).
7. **`always`** reads a snapshot at every start of the source, also after an error restarts it, except
   when its checkpoint is inside one, which it resumes; `never` starts from the checkpoint or from now.
   Alternative: `always` only at fit's start or a reload.
8. **Keys.** A table without a primary key is refused unless `keys` names its key's columns: an error
   naming the table, so the pipeline restarts until `keys` or `tables` change. `keys` also chooses a
   unique key over the primary one.
9. **Jobs.** `Chunks` splits each table into ranges of its key, `chunk_rows` rows each, at the keys
   found by `LIMIT 1 OFFSET chunk_rows` walking the key (an index scan, exact, no sample);
   `information_schema.TABLES.TABLE_ROWS` (an estimate) reckons the last chunk's rows; each chunk is
   read on a connection of its own. With `snapshot: "never"` a job is not split and reads the binlog
   to its end (I174). Alternative: bounds from a sample, as the `mongo` source's.
10. **Interop** (`test/sync/mysql_source_test.go`, the five servers): a table of 3,000 rows written to
    by a mixed load while its snapshot is read 50 rows a page, through the sink `mysql`, the pipeline
    killed at random points (inside the snapshot too) and started again, ends equal to the table by
    checksum; a job of 2,000 rows in chunks of 300 read four at a time, killed and finished on its
    restart, ends equal.
11. **Size**: about 530 lines of code, 380 of unit tests (the package at 96.6% of statements without
    containers) and 150 of interop; nothing left, no I175b.

### I176 MySQL and TiDB as a sink

- **Do**: sink `mysql`: inserts and updates as `INSERT ... ON DUPLICATE KEY UPDATE` and deletes by key,
  batched per table into multi-row statements, `transactions: "source"` writing each source
  transaction as one, or `"batch"` grouping several; `ddl: "report"` (the default) or `"stop"`; works
  against TiDB, whose differences (the size of a transaction, `AUTO_RANDOM`) are handled or refused.
- **Done when**: interop with MySQL 8.4 and TiDB in Docker: a MySQL-to-MySQL pipeline under a mixed
  write load ends equal by checksum, across replays and a kill.
- **Touches**: `sync/mysql`, `test/sync`, `docs`.

**Decisions pending review** (made while building it, 2026-11-08; each stands until the user rules on it):

1. **Where it lives.** Package `sync/mysql`, named `syncmysql`, which I174's source joins; the
   connection's params (`address`, `username`, `password_env`, `database`, `tls`, `ca_file`,
   `cert_file`, `key_file`, `server_name`, `tls_rsa`, `server_public_key_file`, `timeout`) are made
   into `mysqlwire.Config` by one function both will share. `tls_rsa` answers I172's question 4: a
   param that adds the `TLS_RSA_*` suites, off by default, for MySQL 5.7's image.
2. **Upserts with `VALUES()`.** `INSERT ... ON DUPLICATE KEY UPDATE c=VALUES(c), ...` for every column
   of the row: MySQL 8.0.20 and later deprecate `VALUES()` in favour of a row alias, which MySQL 5.7,
   MariaDB and TiDB do not have, and the one form serves them all. Alternative: the alias on MySQL
   8.0.19 and later.
3. **Deletes by the record's key**, a map of columns without nulls (a key of one value, or with a
   null, fails the record for good): `DELETE ... WHERE k IN (?, ...)`, or `(a, b) IN ((?, ?), ...)`
   for a key of several columns, up to 1,000 keys a statement. An update whose key changed (its
   `before` holding other values for the key's columns) deletes the row of the old key first.
4. **A source transaction may be held open across batches.** With `transactions: "source"` a batch
   that ends inside a source transaction leaves the server's transaction open and acknowledges its
   records: the checkpoint never passes a transaction's end before every sink has its last record,
   so a crash replays the whole transaction. The sink keeps what the open transaction holds, and a
   new connection, after the old one fails, writes it again before the batch. `transactions:
   "batch"` commits at a batch's last record that ends a source transaction, so that what it holds
   open is a part of one source transaction only; otherwise the watermark could pass a source
   transaction the server could still roll back. Alternative: commit at each batch's end, a source
   transaction split across batches committed in parts.
5. **Records without a transaction** (a snapshot's, or a source that has none) go in one of the
   server's transactions per batch, or per run between source transactions, with `transactions:
   "source"`.
6. **Refusals.** A server's error whose SQLSTATE is of class 22 (data), 23 (constraints) or 42
   (a table or column missing, a statement or access refused), or one of 1300, 1364, 1366, 1411 and
   3140, fails a record for good; a multi-row statement refused (InnoDB and TiDB undo the statement
   alone) is written again a row at a time to find the record at fault. What was written before it is
   committed, even a part of a source transaction, and `on_error` takes the record; with `on_error:
   "stop"` the pipeline replays the transaction from its checkpoint. Deadlocks (1213), lock waits
   (1205), TiDB's conflicts (8002, 8022, 9007) and a connection's errors close the connection and the
   batch is written again. Alternative: roll back the whole source transaction and fail its first
   record.
7. **`ddl: "stop"` fails the DDL record for good**, which `on_error` takes: a sink cannot stop a
   pipeline whatever `on_error` says, so with `skip` or `dead_letter` the pipeline goes on past it.
   `report` logs the namespace and the statement (its first 200 bytes) at info. Alternative: a way
   for a sink to stop its pipeline, in `fitsync`.
8. **TiDB** is told by its version (`-TiDB-`). The session sets `allow_auto_random_explicit_insert`,
   so `AUTO_RANDOM` keys take the source's values; `max_transaction_bytes` is 16 MiB when left out
   (TiDB's `txn-total-size-limit` is 100 MiB and counts indexes too), and a source transaction past
   it is committed in parts with a warning rather than refused. Alternative: refuse such a
   transaction, stopping the pipeline.
9. **The session** sets `time_zone = '+00:00'` (times are written in UTC: a `TIMESTAMP` holds the
   instant, a `DATETIME` the UTC wall clock) and adds `NO_AUTO_VALUE_ON_ZERO` to `sql_mode`, which
   the interop test found: a key of 0 written to an `AUTO_INCREMENT` or `AUTO_RANDOM` column was
   given a new value. Alternative: a `time_zone` param.
10. **One connection, not a `ParallelSink`**: a job's snapshot goes through one writer. Alternative:
    a connection per writer, for jobs.
11. **Test connection** logs in on a connection of its own and runs `SELECT 1`.
12. **Interop.** The plan's MySQL-to-MySQL pipeline needs I174's source; meanwhile a test source of
    a mixed load (transactions of one to six inserts, updates, key changes and deletes over 300 keys,
    0 among them) runs into each server through the runtime, killed at random points by the crash
    hook and started again from its checkpoint, then replayed whole from no checkpoint: the table
    (an `AUTO_INCREMENT` key, `AUTO_RANDOM` on TiDB, with `DECIMAL`, `DATETIME(6)`, `JSON` and
    `VARBINARY` columns) ends equal, by a server-side checksum, to one the expected rows were
    written to directly, with both `transactions` modes, on MySQL 5.7, 8.0 and 8.4, MariaDB 10.11
    and 11, and TiDB v8.5.3. `test/sync/mysql.sh` starts TiDB beside them (`$MYSQL_PREFIX-tidb`,
    `pingcap/tidb:v8.5.3`, `TIDB_PORT` 29217, root without a password or TLS; `TIDB=0` leaves it
    out), so `test/sync/run.sh` and CI start it too; the tests read `FIT_SYNC_TIDB`. I174 adds the
    MySQL-to-MySQL run.
13. **Size**: about 790 lines of code, 1,000 of unit tests against a fake connection and a fake
    server (98.1% of statements, no containers) and 300 of interop; nothing left, so no I176b.

### I177 A PostgreSQL client: connections, authentication and queries

- **Do**: package `sync/postgres/wire`: startup, TLS (`sslmode` with `verify-full` the default where
  TLS is used), SCRAM-SHA-256 (with channel binding), MD5 refused unless enabled, the simple and the
  extended query protocol, `COPY FROM STDIN`, the replication connection's commands; PostgreSQL 13 to
  17.
- **Done when**: interop with each version in Docker: every common type through the extended
  protocol, `COPY` of a million rows, SCRAM with and without channel binding.
- **Touches**: `sync/postgres/wire`, `test/sync`.

**Decisions pending review** (made while building it, 2026-11-08; each stands until the user rules on it):

1. **Where it lives.** Package `sync/postgres/wire`, named `pgwire`, so that it does not clash with
   `sync/mysql/wire` where both are imported. Alternative: the name `wire`.
2. **A blocking connection on `net.Conn` and `crypto/tls`, not fib.** A `Conn` is meant for one
   goroutine of its own, as decision 036 has database connections (few, long, off fib's workers):
   the protocol is a request and its answer, COPY and the replication stream want the backpressure
   a blocking write gives, and channel binding needs the server's certificate from `crypto/tls`.
   `SendStandbyStatus` alone may run beside a goroutine waiting in `ReceiveReplication`.
   Alternative: fib's asynchronous connection with callbacks.
3. **`sslmode`**: `disable`, `require`, `verify-ca` and `verify-full`, the default; no `prefer` or
   `allow`, whose silent fall back to plain text decision 036 rules out. TLS is asked for with
   `SSLRequest`; PostgreSQL 17's direct TLS (`sslnegotiation=direct`) is not used. `Config.TLS`
   carries the roots, client certificates and server name; the connectors will build it from
   `ca_file` and the like. Alternative: `prefer` as libpq's default.
4. **Channel binding** (`tls-server-end-point`) is `prefer` by default: SCRAM-SHA-256-PLUS whenever
   TLS is used and the server offers it, `y,,` when TLS is used and it does not (so a stripped
   offer is caught by the server), `n,,` without TLS. `require` fails unless the server
   authenticated with -PLUS, also when it asked for no password at all. A certificate signed with
   Ed25519 has no hash for it: unbound under `prefer`, an error under `require`. Alternative:
   `require` by default over TLS.
5. **SCRAM without SASLprep**: the password is used as its bytes, as PostgreSQL does with a password
   SASLprep refuses; ASCII passwords are the same either way, and a non-ASCII one whose
   normalisation changes it fails to authenticate. A server asking for more than 4,194,304
   iterations is refused. Alternative: SASLprep (RFC 4013) in fit.
6. **MD5 and cleartext passwords are refused** unless `AllowMD5` or `AllowCleartext` is set;
   Kerberos, GSSAPI and SSPI are not supported.
7. **A context that ends breaks the connection.** A `CancelRequest` is sent on a connection of its
   own (best effort, plain TCP) and the connection is closed with the context's error; the caller
   makes a new one, since the protocol cannot be resumed at an arbitrary point. There is no timeout
   per call of the wire's own: callers bound calls with their contexts; `ConnectTimeout` (10s)
   bounds the connect when the context has no deadline. Server errors (`*Error`, with SQLSTATE and
   the fields) leave it usable. Alternative: wait for the cancelled query's error and keep the
   connection.
8. **The extended protocol takes two round trips per `Query`**: Parse and Describe, then Bind and
   Execute, so the parameters' types are known and the results asked in binary for the types
   decoded here, in text for the others. `Prepare` names a statement, run with one round trip
   each time. Parameters go as text, except `[]byte` for a `bytea` parameter, which goes in binary.
   Alternative: binary parameters, or a statement cache.
9. **Values.** Integers and `oid` as `int64`, `float4`/`float8` as `float64`, `numeric` as
   `Numeric` (its exact decimal text, `NaN` and the infinities included), `json`/`jsonb` as `JSON`,
   `uuid` as its string, `bytea` as `[]byte`, `date` as midnight UTC, `timestamp` as its wall clock
   in UTC, `timestamptz` as UTC, the infinities of dates and timestamps as the strings
   `infinity`/`-infinity`, arrays of those as `[]any` (nested for more dimensions); every other type
   (`time`, `timetz`, `interval`, `money`, `inet`, enums, ranges, composites) as the server's text.
   Parameters: Go's numbers, bools, strings, `time.Time` written by the parameter's type (the wall
   clock in its location for `timestamp`, with the offset for `timestamptz`; years 1 to 9999),
   `time.Duration` as an interval, `Numeric`, `JSON`, pointers, and slices as array literals.
   Alternative: a type of fit's own for time and interval.
10. **Bounds**: one message from the server at most `MaxMessageBytes` (64 MiB); `SimpleQuery`
    keeps a result's rows up to the same in all and is meant for commands and small answers;
    `Query` streams rows. Alternative: no bound beyond the server's 1 GB.
11. **COPY**: `CopyIn` gathers 64 KiB per CopyData; `WriteRow` and `AppendCopyRow` write COPY's
    text format; a server error during the data is seen at `Close`; `Abort` sends CopyFail.
    `CopyTo` (COPY TO STDOUT) is there too, beyond the plan, for checksums in I179's and I180's
    tests. Binary COPY is not. Alternative: watch for the server's error while writing.
12. **Replication commands**: `IDENTIFY_SYSTEM`; `CREATE_REPLICATION_SLOT` for logical slots only,
    with the new syntax from 15 and the old one before, and its snapshot action (`export`, `use`,
    `nothing`); `DROP_REPLICATION_SLOT [WAIT]`; `START_REPLICATION ... LOGICAL` with the plugin's
    options quoted; the stream read as `XLogData` and `Keepalive`; standby status updates;
    `EndReplication`. Not here: physical replication, `BASE_BACKUP`, `TIMELINE_HISTORY`,
    `READ_REPLICATION_SLOT`, `ALTER_REPLICATION_SLOT`, and decoding `pgoutput`'s messages, which is
    I178's (pgoutput sends values as text, so I178 also turns text into values by type OID).
13. **Interop.** `test/sync/postgres.sh` starts `postgres:13`…`17-bookworm` (`fit-sync-pg13`…, ports
    29513–29517) with TLS from a CA made in the container by its `openssl` and copied out, SCRAM
    for a user `fit`, `wal_level=logical`; `test/sync/run.sh` sources it, and CI's `sync-interop`
    job runs the tests (`test/sync/postgres_test.go`, reading `FIT_SYNC_PG`, `FIT_SYNC_PG_CA_DIR`,
    `FIT_SYNC_PG_USER`, `FIT_SYNC_PG_PASSWORD`, skipping without them). PostgreSQL 13's initdb
    stores the superuser's password as MD5 unless given `--auth-host=scram-sha-256`, which the
    script sets. MD5's refusal is tried against the fake server only. Alternative: a pg_hba line
    for an MD5 user.
14. **Unit tests against a scripted backend in the process** (TLS with a certificate made in the
    test, SCRAM's server side, cancel requests counted), as `sync/elasticsearch`'s fake cluster:
    96.9% of the package's statements without containers. Alternative: replayed captures.
15. **Size**: about 2,600 lines of code with comments, 2,000 of unit tests and 500 of interop, over
    the plan's size for a client, since the replication commands and COPY TO came with it; nothing
    is left, so there is no I177b.

### I178 PostgreSQL as a source

- **Do**: source `postgres`: the publication and the slot created if missing (`create: true`) or
  required; `START_REPLICATION` with `pgoutput` (protocol 2, streaming of large transactions when the
  server has it); relation, insert, update, delete, truncate, begin and commit messages to records,
  typed by the relation's type OIDs; `REPLICA IDENTITY` checked so deletes and updates carry their
  key; standby status updates sent with the flushed LSN only once the sinks have acknowledged it, and
  keepalives answered; slot lag reported with a warning threshold.
- **Done when**: interop with each version: changes across many tables, a large transaction, a
  truncate, a server restart; the slot's `confirmed_flush_lsn` never passes what the sinks have; `kill
  -9` of fit loses no transaction.
- **Touches**: `sync/postgres`, `test/sync`, `docs`.

**Decisions pending review** (made while building it, 2026-11-08; each stands until the user rules on it):

1. **Where it lives.** Package `sync/postgres`, named `syncpostgres` as the other connectors are,
   the source in `source*.go` beside I180's sink in the same package; it registers
   `sync.sources.postgres`. Alternative: a package per direction.
2. **One replication connection does everything.** The source connects with `replication=database`
   and runs its catalog queries (`SHOW wal_level`, `pg_publication`, `pg_publication_tables`,
   `pg_class`, `pg_replication_slots`) and `CREATE PUBLICATION` on it, through the simple protocol,
   before `CREATE_REPLICATION_SLOT` and `START_REPLICATION`; a run is one connection on the
   pipeline's goroutine, made anew after any error. Alternative: a second, normal connection for the
   catalog and the slot's lag.
3. **`create`** makes a missing publication `FOR TABLE` the `tables` (not `FOR ALL TABLES`, which
   needs a superuser) and a missing slot with no snapshot; neither is ever altered or dropped, so a
   removed pipeline's slot is the operator's to drop (the docs say so). Without `create` both must
   exist; an existing publication must hold every table `tables` names. Alternative: drop the slot
   when the pipeline leaves the config, or alter the publication to match `tables`.
4. **Replica identities are checked at the start**: when the publication publishes updates or
   deletes, a table with `REPLICA IDENTITY NOTHING`, or the default without a primary key, is an
   error (so the pipeline restarts and says why, as any source error). A publication of inserts
   alone needs no keys. Alternative: a warning, and records without keys.
5. **The key** is the primary key (read from the catalog), or, with `REPLICA IDENTITY USING
   INDEX` or no primary key, the identity's columns (every column for `FULL` without a key). An
   update whose key changed is a delete of the old key and an insert of the new row, both in its
   transaction, so a sink writing by key does not keep both. Alternative: an update whose `before`
   holds the old key, left to sinks.
6. **An unchanged TOASTed value** (pgoutput's `u`) is left out of `after`, taken from `before` with
   `REPLICA IDENTITY FULL`; sinks that update the fields given keep the old value. Alternative: read
   it with a query (racy against later writes) or fail the record.
7. **A truncate** is a `ddl` record of each table, `after` `{"statement": "TRUNCATE schema.table
   [RESTART IDENTITY] [CASCADE]", "truncate": true}`, reported, not applied, as decision 036 has DDL.
   Alternative: an operation of its own that sinks apply.
8. **Values from pgoutput's text, by type OID**: the session sets `DateStyle ISO`, `TimeZone UTC`,
   `extra_float_digits 3`, `bytea_output hex` and `IntervalStyle postgres` at startup, so the forms
   are known. Integers and `oid` are ints, floats floats (`NaN`, `Infinity`), `numeric` a decimal,
   `bytea` bytes, `json`/`jsonb` JSON, `date` a date, `timestamp` a UTC time, `timestamptz` a time
   with the server's offset, arrays of those (bounds, nesting, quoting) arrays; `uuid` and every
   other type (`time`, `interval`, `inet`, enums, ranges, composites), BC dates, years past 9999 and
   infinities the server's text, as I177's binary values have them. pgoutput's `binary` option
   (14+) is not used. Alternative: binary, where the server has it.
9. **Transactions**: a record's `Tx.ID` is the transaction's xid in decimal; the source holds back
   one record so the last is flagged `Last`; only the last carries the position, `{"lsn":
   "X/Y"}`, the end LSN of the commit, which `START_REPLICATION` resumes from. A transaction whose
   commit is before the checkpoint (the slot started earlier) is skipped. A transaction with no
   record for the pipeline emits nothing.
10. **What the slot is told**: write, flush and apply are the last saved checkpoint, sent at each
    `Confirm`, every `status_interval` (10s) whether it moved or not (a keepalive against
    `wal_sender_timeout`), and at once when the server asks. **While every transaction handed on is
    in the checkpoint**, the slot is told the latest position before which nothing is owed: the end
    of a transaction with no records, a keepalive's position outside a transaction; so a quiet
    table does not hold the WAL of busy unpublished ones. This tells the slot a position beyond the
    checkpoint file's, safe because nothing between them is the pipeline's; a checkpoint set back
    by hand cannot go before the slot's `confirmed_flush_lsn`, which the docs say. Alternative: the
    checkpoint alone, with the slot holding WAL until the next published change (Debezium's
    default without heartbeats).
11. **Streaming** (`streaming: "auto"`): protocol 2 with `streaming on` from PostgreSQL 14,
    protocol 1 on 13 (`off` keeps protocol 1). A streamed transaction's messages are kept in a file
    of its own under `spool_dir` (the system's temporary directory by default) until its stream
    commit, then read back as one transaction; a stream abort removes the file, a subtransaction's
    abort drops its changes by xid; files left by a run are removed when it ends. Their size is
    bounded by the server's transaction alone. Protocols 3 and 4 (two-phase, parallel apply) are not
    used. Alternative: streaming off, the server spilling large transactions and sending them at
    their commit.
12. **The slot's lag** is the WAL between the server's end (from `XLogData` and keepalives) and the
    position the slot was last told; a warning is logged when it passes `lag_warning` (1 GiB) and
    an info line when it falls back; `Source.Lag` is there for I189's metrics. Alternative: query
    `pg_replication_slots` on a second connection.
13. **A job** (`mode: "once"`) reads up to the server's WAL end at its start (`IDENTIFY_SYSTEM`)
    and stops, asking for a keepalive with each status; with I179 it will be the snapshot's mode.
14. **`Test`** connects and checks `wal_level`, the publication, its tables' identities and the
    slot, creating nothing.
15. **I179's seam**: `prepare` returns the slot it made (`setup.slot`, with its consistent point),
    created with `SnapshotNothing` where I179 asks for the exported snapshot, and `Run` has the
    place where the snapshot runs before `START_REPLICATION`; the position is a struct I179 adds
    its progress to.
16. **Interop** (`test/sync/postgres_source_test.go`, PostgreSQL 13 to 17 from
    `test/sync/postgres.sh`): changes across two tables in one transaction, a key changed, a
    TOASTed value unchanged, `REPLICA IDENTITY FULL`, a truncate, a 30,000-row transaction with a
    savepoint rolled back (`logical_decoding_work_mem` set to 64kB with `ALTER SYSTEM` on the test
    servers, so it streams on 14+, checked by `pg_stat_replication_slots.stream_txns`, and spills on
    13); `confirmed_flush_lsn` unmoved before any checkpoint and exactly at each one; a restart from
    a checkpoint; writes to an unpublished table moving the slot on; the walsender terminated and,
    with `FIT_SYNC_PG_PREFIX` (now exported by `postgres.sh`), the server restarted with `docker
    restart`, losing nothing; and a pipeline to a memory sink killed at random points as `kill -9`
    would, while 300 transactions are written, ending with every row. Alternative: a container of
    the test's own to restart.
17. **Size**: about 1,360 lines of code, 970 of unit tests (a fake replication connection, 98.6% of
    the package's statements without containers) and 510 of interop; nothing left, no I178b.

### I179 The PostgreSQL snapshot

- **Do**: the slot created with `EXPORT_SNAPSHOT` (or a new slot for a new snapshot), the tables read
  in chunks by key in transactions that `SET TRANSACTION SNAPSHOT` to it, in parallel; the stream then
  starts at the slot's consistent point, so the hand-over is exact; chunk progress in the checkpoint.
- **Done when**: a snapshot taken under writes ends with the sink equal to the source by checksum; a
  resumed snapshot whose exported snapshot is gone starts the table again and says so.
- **Touches**: `sync/postgres`, `test/sync`.

**Decisions pending review** (made while building it, 2026-11-09; each stands until the user rules on it):

1. **`snapshot`: `initial` (the default) or `never`.** `initial` reads the tables when there is no
   checkpoint; I178's behaviour (changes alone) is `never`, which its tests now set. No `always`:
   a new snapshot is the Admin API's (I188's `Snapshot`, which removes the checkpoint).
   Alternative: `never` the default, keeping I178's.
2. **Where the snapshot comes from.** A slot `create` makes is made with `EXPORT_SNAPSHOT`, the
   run's replication connection staying idle while the tables are read, then `START_REPLICATION`
   at its consistent point. When the slot exists already (made by hand, or the checkpoint removed
   for a new snapshot), a **temporary slot** is made on a second replication connection with
   `EXPORT_SNAPSHOT`, and the stream starts at *its* consistent point, which the server takes as
   the start of decoding (transactions committed before it are skipped), so the hand-over is exact
   there too; the temporary slot goes with its connection when the snapshot ends. The permanent
   slot is never dropped or made again (I178 decision 3). Alternative: drop and make the slot
   again, or a snapshot of the moment without a slot, replayed over from the slot's position.
3. **Chunks by key, read in parallel.** A table is split by its primary key, or its `REPLICA
   IDENTITY USING INDEX` index (as the stream keys it), into ranges of `chunk_rows` (100,000) rows:
   `count(*)`, then the key of every `chunk_rows`-th row from `row_number()` over the key, both in
   the exported snapshot, so the plan is the same whenever it is made in that snapshot. A table with
   neither (`FULL` without a key, or inserts-only publications) is one chunk read whole.
   `snapshot_parallel` (4, at most 64) chunks are read at once, each reader on a normal connection
   of its own, each chunk in `BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY; SET TRANSACTION
   SNAPSHOT` through a cursor (`FETCH` of up to 1,000 rows, fewer when rows are large, about
   16 MiB a fetch). Bounds are the key's text, compared as a row with untyped literals, so every
   key type the server can read back works. Alternative: `ctid` ranges (PostgreSQL 14+), or
   sampling for the bounds.
4. **Snapshot records** are inserts with no transaction (`Tx` empty), no commit time, keyed and
   valued exactly as the stream would key and value the same row (text values by type OID, the
   same columns: generated ones left out, the publication's column list kept), and the
   publication's row filter (15+) is applied. Alternative: a transaction per chunk.
5. **The position during the snapshot** is `{"lsn": <the stream's start>, "snapshot": {"id",
   "tables": [{"name", "chunks"}], "done": "0-41,43", "at": [{"chunk", "key"}]}}`, put as I167's
   job does it on each chunk's last record and on one in every 100, the records emitted through one
   lock, each chunk's held back by one; the snapshot's very last record carries `{"lsn": ...}`
   alone. So a checkpoint never claims a chunk or a row a sink lacks. Alternative: the whole plan
   in the position.
6. **Resuming.** A run that fails mid-snapshot keeps its temporary slot's connection, and so the
   exported snapshot, for five minutes (`holdFor`), with its plan; the next run whose checkpoint
   names that snapshot resumes in it exactly, at each chunk's last key. Otherwise (fit restarted,
   the five minutes passed, the slot made by the run itself, whose connection the run closes, or
   `SET TRANSACTION SNAPSHOT` says 22023) the snapshot is gone: the tables whose chunks were all
   done stay done, the others are read **again whole** from a new temporary slot's snapshot, with
   a warning naming them, and the stream still starts at the first snapshot's point, so the changes
   between the two are replayed over the tables read again and converge in sinks writing by key
   (as I175's MySQL snapshot converges). Alternative: the whole snapshot again from a new
   temporary slot, exact, whatever was done.
7. **Jobs** (`mode: "once"`) implement I167's `Chunker`: `Chunks` plans the publication's tables
   (or `tables`, when the publication does not exist; no slot is needed or made), and `ReadChunk`
   reads a chunk in a `REPEATABLE READ` transaction of its own on a connection of its own, resumed
   after `{"key": [...]}`; a keyless table's chunk is read whole again. A job's chunks are not in
   one exported snapshot (nothing follows it to hand over to, and a snapshot would hold vacuum back
   for the whole job): each is consistent in itself, as MySQL's and MongoDB's. With `never` a job
   reads the changes up to the server's end, as I178's did. Alternative: one exported snapshot for
   the job, held by a connection from `Chunks` to the job's end.
8. **The catalog query** reads, beside the primary key, the replica identity index's columns and the
   non-generated columns; on 15+ `pg_publication_tables`' `attnames` and `rowfilter`. Alternative:
   `SELECT *`, generated columns included, column lists and row filters ignored.
9. **Interop** (`test/sync/postgres_snapshot_test.go`, PostgreSQL 13 to 17): a 3,000-row table
   read while rows are inserted, through a slot `create` makes and through a temporary slot beside
   a slot that exists, every row exactly once (none in both the snapshot and the stream); three
   tables (an int key, a composite text and int key, none with `REPLICA IDENTITY FULL`) written to
   by updates, deletes, inserts and key changes while a pipeline into the memory sink is killed at
   its fifth saved checkpoint, usually mid-snapshot, and run again, the snapshot then gone and the
   tables read again, ending equal to the tables row for row; a job's chunks read once each, and a
   chunk resumed after a key. Three runs in a row pass on every version.
10. **Size**: about 790 lines of code, 580 of unit tests (fakes of the server's SQL and slots,
    98.1% of the package's statements without containers) and 300 of interop; nothing left, no
    I179b.

### I180 PostgreSQL as a sink

- **Do**: sink `postgres`: `INSERT ... ON CONFLICT (key) DO UPDATE` and deletes by key, batched per
  table, `COPY` into a temporary table and a merge for snapshots, the source's transactions kept when
  asked; types mapped from the record's values, with the mapping in the docs.
- **Done when**: PostgreSQL-to-PostgreSQL and MySQL-to-PostgreSQL pipelines end equal by checksum
  under a mixed load, across replays and a kill.
- **Touches**: `sync/postgres`, `test/sync`, `docs`.

**Decisions pending review** (made while building it, 2026-11-08; each stands until the user rules on it):

1. **Where it lives.** Package `sync/postgres/sink` (`syncpgsink`), not `sync/postgres`, which
   I178's source takes (built at the same time); the connection's params (`address`, `user`,
   `password_env`, `database`, `sslmode`, `ca_file`, `cert_file`, `key_file`, `server_name`,
   `channel_binding`, `timeout`) are the sink's own, flat, as I168's and I186's, and may be shared
   with the source once both are in. `user` is required; MD5 and cleartext passwords are not offered.
   Alternative: one package with the source.
2. **Connections**: a small pool of blocking `pgwire` connections, one per writer (a stream has one,
   a job `job.max_parallel`, as `ParallelSink`), each on the pipeline's goroutine; one that broke,
   or is left in a transaction, is closed. `Test` (I188's `Tester`) connects and runs `SELECT 1`.
3. **Statements.** Upserts are `INSERT ... VALUES (...),(...) ON CONFLICT (key) DO UPDATE SET` every
   other column of the row `= EXCLUDED.col` (`DO NOTHING` for a row of the key alone); deletes
   `DELETE ... WHERE key IN (...)` (a row of columns for a key of several). A statement is a run of
   consecutive records of one table, operation and set of columns, at most `max_statement_rows`
   (1000) rows and 65535 parameters, a key held once in it (an upsert's last row, so the server's
   "cannot affect row a second time" is not met; one met anyway, as `1.0` and `1.00`, writes the run
   one row at a time). Parameters go as text, typed by the server from the columns. Alternative:
   prepared statements of fixed sizes.
4. **COPY for snapshots by size, not by kind**: a run of `copy_min_rows` (100) upserts or more is
   copied into `CREATE TEMPORARY TABLE ... ON COMMIT DROP AS SELECT cols FROM t WITH NO DATA`, then
   merged with one `INSERT ... SELECT ... ON CONFLICT`; `copy: "always"` or `"never"` override. A
   snapshot's records are inserts like others, so the size of a run is what tells. Alternative: COPY
   only in jobs.
5. **Type mapping**: values as the text PostgreSQL reads (the table in the docs): times with their
   own offset (so a `timestamp` column takes the wall clock, a `timestamptz` the instant), dates as
   `YYYY-MM-DD`, bytes as bytea's hex text (`\x..`) also in statements, so a row is the same copied
   or inserted, arrays as array literals, maps as JSON; a NUL in a string fails the record.
   Alternative: binary parameters by the column's type.
6. **The table** is `table` (`{namespace}`) split at its first dot into schema and table, so a
   MySQL source's `db.table` is a schema and a table; not made by the sink. The key's columns are
   the key's fields or `key_columns` (from the key or the row); an upsert's row gets the key's
   columns it lacks.
7. **Transactions.** `batch` (default): each batch one transaction. `source`: each source
   transaction one; an open transaction's records are held in the sink across batches, acknowledged
   (the checkpoint cannot pass them, since it never falls inside a transaction) and written with its
   last record; a held transaction whose next record is another's, or its own first again (a
   replay), is dropped. Alternative: a transaction left open across writes, which a lost connection
   would lose after its records were acknowledged.
8. **Errors.** SQLSTATE classes 22 and 23 are permanent; every other error (a table or column
   missing, `42P10` with no unique index, a lost connection, `40001`) rolls the batch back and is
   retried. On a permanent error the batch is written again one record at a time after savepoints
   to find the record: with `batch`, the records before it are committed and it fails; with `source`,
   its whole source transaction is left out (the records after it acknowledged unwritten), and a
   held record's refusal fails the batch's first record, the error naming the transaction.
   Alternative: in `source`, write the transaction without the refused record.
9. **DDL is acknowledged and not applied** (decision 036). Alternative: `ddl: "stop"`.
10. **Interop.** `test/sync/postgres_sink_test.go` against `test/sync/postgres.sh`'s servers (run here
    against 13 and 17, TLS with SCRAM): generate's every type inserted and copied, equal by checksum
    and read back by the mapping; a refused value and a missing table; a mixed load of upserts and
    deletes of 37 two-column keys in transactions of three, through pipelines killed at every crash
    point, with each transactions mode, ending equal by checksum to the load applied in order. The
    plan's PostgreSQL-to-PostgreSQL and MySQL-to-PostgreSQL pipelines need I178's and I174's sources,
    neither built yet: they are I192's matrix. Alternative: wait for the sources.
11. **Size**: about 900 lines of code and 1,400 of tests (98.8% of the package from unit tests
    against an in-process fake server that runs the sink's statements); nothing is left, so there is
    no I180b.

### I181 A MongoDB client: BSON and OP_MSG

- **Do**: package `sync/mongo/wire`: BSON encoding and decoding to the record's values (with types
  that have no match kept as tagged values), `OP_MSG` with sections, `hello` and replica set discovery
  of the primary, SCRAM-SHA-256, TLS, cursors with `getMore`, timeouts; MongoDB 6, 7 and 8.
- **Done when**: interop with a replica set in Docker; a BSON fuzz test; a failover finds the new
  primary.
- **Touches**: `sync/mongo/wire`, `test/sync`.

**Decisions pending review** (made while building it, 2026-11-08; each stands until the user rules on it):

1. **Where it lives.** Package `sync/mongo/wire` (`mongowire`): a client only, with no connector, no
   params and no schema; I182's source and I183's sink declare the connection's params (a `uri`, or
   hosts, credentials and TLS) and build `Options` from them, or from `ParseURI`. Alternative: the
   connection's params declared here, shared by both.
2. **A blocking client, off fib.** A connection is a `net.Conn` (with `crypto/tls`) read and written
   in lockstep, one command at a time, by the goroutine that calls it; sync's connectors call it from
   goroutines of their own, never from a fib worker (rule 1). MongoDB answers each request on its
   connection in order, connections to a database are few and long, and a change stream's `getMore`
   waits on the server; callbacks on fib's connections would buy nothing here and cost a state machine
   for SCRAM, cursors and discovery. Alternative: the client on fib's connections with callbacks.
3. **Types with no match are tagged as canonical extended JSON.** An ObjectId reads as
   `{"$oid": "<hex>"}`, a timestamp as `{"$timestamp": {"t": .., "i": ..}}`, binary other than subtype
   0 as `{"$binary": {"base64": .., "subType": "04"}}`, and so `$regularExpression`, `$numberDecimal`
   (NaN and infinities only), `$minKey`, `$maxKey`, `$undefined`, `$symbol`, `$code` (with `$scope`)
   and `$dbPointer`; a map of exactly that shape is written back as its type, so a document read and
   written again is the same document. A real document whose only field is `$oid` with 24 hex digits
   would be written as an ObjectId (MongoDB keeps `$` names out of stored documents). Alternative:
   ObjectIds as plain hex strings, which a MongoDB sink could not tell from strings.
4. **Integers are int64.** int32 and int64 read as ints; an int is written as an int64 whatever its
   value, so one field keeps one BSON type (what `$type` queries and validators see); a uint past
   int64's range is a decimal128. MongoDB to MongoDB, an int32 field so becomes a long.
   Alternative: an int32 when it fits.
5. **Decimals are exact or refused.** A decimal is a decimal128; one that 34 digits or the exponent's
   range cannot hold exactly is an error, not rounded. A decimal128 reads as the BSON specification
   writes it: plain for small exponents (`-12.3400`), scientific otherwise (`1E+3`), which JSON
   takes as a number. Alternative: rounding to 34 digits.
6. **Times are UTC milliseconds.** A time or a date is written as a datetime, rounded down to the
   millisecond, its offset dropped; a datetime reads as a time in UTC. A JSON value is written as
   what it holds: objects as documents, integers as int64 (past it a decimal128 when 34 digits hold
   it, else a double), other numbers as doubles. Alternative: JSON as a string.
7. **The primary only, found when needed.** A `Client` sends every command to a writable primary (a
   replica set's, a standalone server, a mongos): the seeds, then the members a server names, the
   primary a secondary names tried first, round after round (100 ms growing to 1 s) for
   `ServerSelectionTimeout` (30 s); with `ReplicaSet` set, a server of another set is not taken. There
   is no monitoring goroutine: the primary is looked for again only when a command meets a
   connection's error or a not-primary answer (codes 10107, 13435, 13436, 189, 91, 11600, 11602,
   10058). No reads from secondaries, no stale-primary check by election ID. Alternative: the
   drivers' server discovery and monitoring, with heartbeats.
8. **A command is sent at most twice.** `Run` and `Cursor` send a command once more to the primary
   found after such an error; a write may so be applied twice, which sync's writes (upserts and
   deletes by `_id`) allow. No sessions or retryable writes (`lsid`, `txnNumber`); a `getMore` is
   never sent again (its cursor lives on one server). Alternative: retryable writes with sessions.
9. **A bounded pool.** `MaxConns` (4) connections to the primary; a cursor holds its connection
   until it is exhausted or closed (its `getMore`s must go to the same server), so a cursor left open
   holds a slot. Idle connections are dropped when the primary changes.
10. **Timeouts.** A round trip is bounded by its context's deadline, or by `Timeout` (60 s) when it has
    none; one cut by either closes the connection, since its answer may still come. `ConnectTimeout`
    (10 s) bounds the dial, TLS, `hello` and authentication. An answer larger than
    `MaxMessageBytes` (48,000,000, or less when the server says so) is refused before it is read.
    The interop test found that the socket's deadline can pass before the context's own timer fires;
    such an error is reported as the context's `DeadlineExceeded`.
11. **SCRAM-SHA-256 only, without SASLprep.** The password is used as given, which is what SASLprep
    gives for printable ASCII; SCRAM-SHA-1, X.509, LDAP and AWS authentication are not spoken; a
    server that offers the user only SCRAM-SHA-1 is an error that says so. The salted password is
    kept per client (at most eight), so the pool's connections do not each run PBKDF2; 4,096 to
    10,000,000 iterations are accepted. Alternative: SASLprep's tables.
12. **What is not spoken.** Servers older than 4.2 (wire version 8) are refused (6.0, 7.0 and 8.0 are
    tested); no compression; checksums are checked when a reply has one and never sent; `hello` goes
    over `OP_MSG`, never the old `OP_QUERY`.
13. **Connection strings.** `ParseURI` reads `mongodb://` with `replicaSet`, `authSource`,
    `authMechanism` (SCRAM-SHA-256), `tls`/`ssl`, `tlsCAFile`, `tlsCertificateKeyFile`,
    `connectTimeoutMS`, `socketTimeoutMS`, `serverSelectionTimeoutMS`, `maxPoolSize` and `appName`;
    any other option is refused, `tlsInsecure` among them, so nothing is silently left out;
    `mongodb+srv://` is not read (it needs DNS SRV and TXT lookups). Alternative: SRV through the
    resolver.
14. **`Doc(k, v, ...)`** builds a command from other packages, where vet wants `E`'s fields named.
15. **The interop servers.** Each version is a replica set of three members in one container
    (`test/sync/mongo.sh`), each member on the same port inside and out, since the client reaches
    members by the names the set gives them (`localhost:<port>`); keyfile authentication, a root user,
    `preferTLS` with a CA of the run's own. `test/sync/run.sh` starts them on 29661–29683 and makes
    them afresh on every run, `KEEP=1` too, as their certificates are the run's. CI's
    `sync-interop` job runs them through `run.sh`; its comment in `ci.yml` is not updated here.

### I182 MongoDB as a source

- **Do**: source `mongo`: change streams on databases or collections with
  `fullDocument: updateLookup` (or `whenAvailable` with pre-images), the resume token as the
  checkpoint, a snapshot by `_id` ranges with the stream's start time taken first so the hand-over
  overlaps; an invalidate or a token too old for the oplog is an error saying a snapshot is needed.
- **Done when**: interop with a replica set: inserts, updates, replaces and deletes, a failover, a
  restart of fit; nothing lost across `kill -9`.
- **Touches**: `sync/mongo`, `test/sync`, `docs`.

**Decisions pending review** (made while building it, 2026-11-08; each stands until the user rules on it):

1. **The connection's params, shared with the sink.** Package `sync/mongo` (`syncmongo`); its
   `conn.go` holds what I183's sink shares: `ConnParams`, `NewClient` and `Ping`, for the params
   `uri`, `password_env` and `timeout`, which each connector declares as its own with the same
   names and docs. `uri` is a `mongodb://` connection string as `mongowire.ParseURI` reads it
   (hosts, `replicaSet`, `authSource`, `tls`, `tlsCAFile`, `tlsCertificateKeyFile`, timeouts):
   MongoDB's users know it, and TLS needs no params of fit's own. Alternative: `hosts`,
   `username`, `ca_file` and the like, as the other connectors have.
2. **No password in `uri`.** The user is in `uri`, its password only in `password_env`; a password
   in `uri` is refused, as is a user without `password_env`. Alternative: a password in `uri` taken.
3. **The key is `{"_id": ...}`**, in the snapshot and the stream alike, not the event's whole
   `documentKey` (which holds a sharded collection's shard key too), so that a snapshot's record and
   a change's of one document have one key. Alternative: `documentKey` as it is.
4. **Positions in the record's canonical JSON.** The checkpoint is `{"token": <the resume token>}`;
   during the snapshot `{"start": <the stream's start token>, "ns": "db.coll", "id": <the last _id>}`;
   a job's chunk `{"id": ...}`; tokens and `_id`s in canonical JSON (`{"map": {"_data": "82..."}}`,
   `{"int": 5}`), so that every BSON type comes back as it was. Alternative: the token's `_data`
   alone, and `_id`s as extended JSON.
5. **The snapshot's hand-over.** The stream's start is the post-batch resume token of a change
   stream opened (and closed at once) before the snapshot; the snapshot then reads each collection,
   in the order of their names, by `_id` with `find`'s `min` and `max` on the `_id` index (`hint`):
   a `$gt` filter compares only within one BSON type, so `_id`s of several types would be passed
   over; read concern `majority`, so that nothing read is rolled back. The snapshot's last record
   carries the start token, so that a restart after it reads the stream. Alternative:
   `startAtOperationTime` from the answer's `operationTime`.
6. **An update without its document is passed over**: with `updateLookup`, one whose document was
   deleted since (its delete follows); with `whenAvailable`, one without a post-image. Alternative:
   an update with a null `after`, or its `updateDescription` as the row.
7. **Transactions.** A change with `lsid` and `txnNumber` is of the transaction
   `<the session's ID in hex>:<the number>`; its first record is marked at once, its last once the
   next change is of another, or a request for changes comes back empty (after `max_await`, 1s): the
   record is held until then. Alternative: each change a transaction of its own, the checkpoint then
   free to fall inside one (MongoDB can resume there).
8. **What the stream covers.** One collection: that collection's stream; several: the database's,
   with a `$match` on `ns.coll` (keeping `invalidate` and `dropDatabase`); no `database`: the
   deployment's (`allChangesForCluster`), its snapshot every database but `admin`, `config` and
   `local`, every collection but views, time series and `system.` ones. `drop`, `rename` and
   `dropDatabase` are `ddl` records (`after`: `operationType`, `ns`, `to`); no expanded events are
   asked for. Alternative: every database's stream of its own.
9. **Cursors resumed in place.** A cursor cut by a lost connection, a failover (not primary), a
   cursor the server timed out (while the sinks held the queue) or an error labelled
   `ResumableChangeStreamError` is opened again from the last token or `_id` read, up to five times
   in a row (100 ms, doubling), before the error is the pipeline's, which restarts it from its
   checkpoint. Alternative: every error to the pipeline's restart.
10. **A new snapshot is the user's.** A token the oplog no longer holds (codes 286, 280, 260) or an
    invalidate is an error saying a new snapshot is needed (the pipeline's snapshot control, or a
    reset of its checkpoint); the source does not start one by itself. Alternative: a new snapshot
    at once, with a warning.
11. **No checkpoint before the first record.** A pipeline that has read nothing yet has no
    position to save: with `snapshot: "never"` a restart before the first change starts from now
    again, and an empty snapshot is read again with a new start. Alternative: a record with no row
    carrying the post-batch token, which the record model does not have.
12. **Jobs.** A job's snapshot is split by each collection's `count`, `chunk_documents` (100,000) a
    chunk, at bounds from a sorted `$sample` of four `_id`s per chunk; with `snapshot: "never"` a job
    is not split and reads the stream from its checkpoint until a request for changes comes back
    empty. Alternative: `splitVector` (it needs privileges a sync user rarely has) or `$bucketAuto`
    (a scan of the whole collection).
13. **The interop tests** (`test/sync/mongo_source_test.go`, on `test/sync/mongo.sh`'s replica sets of
    MongoDB 6, 7 and 8): the snapshot then every kind of change and a transaction over TLS, a restart
    from a position, a drop and an invalidate; the primary stepped down mid-stream; a copy into the
    memory sink across random kills (`Options.Crash`) while the collection changes, converging; a
    job's chunks.

### I183 MongoDB as a sink

- **Do**: sink `mongo`: unordered bulk writes of upserts (replace by `_id` from the key) and deletes,
  per-document errors read, write concern from the config.
- **Done when**: MySQL-to-MongoDB and MongoDB-to-MongoDB pipelines converge across replays.
- **Touches**: `sync/mongo`, `test/sync`, `docs`.

**Decisions pending review** (made while building it, 2026-11-08; each stands until the user rules on it):

1. **Where it lives.** Package `sync/mongo` (`syncmongo`), beside I182's source: the sink in
   `sink.go` and its connection params in `sinkconn.go` (`uri` or `hosts`, `replica_set`,
   `username`, `password_env`, `auth_source`, `tls`, `ca_file`, `cert_file`, `key_file`,
   `server_name`, `connect_timeout`, `timeout`, `max_conns`), declared flat as I168's and I186's;
   built while I182 was built beside it, so the two sets of connection params are to be made one
   at the merge. Alternative: the params declared once in `mongowire` (I181's decision 1).
2. **No password in `uri`.** A connection string holding one is refused; `password_env` holds it, as
   every credential in fit. `tls` and its files, when set, replace the string's TLS options; a
   `cert_file` without `key_file` holds both, as MongoDB's `tlsCertificateKeyFile`. Alternative: a
   `uri_env` holding the whole string.
3. **The `_id` from the key.** A key of one field is that field's value (`{"id": 7}` gives `_id: 7`,
   a MongoDB source's `{"_id": ...}` its value), a key of several fields an embedded document of them
   in their order, a scalar key itself, a tagged value (`{"$oid": ...}`) its BSON type; a record
   without a key takes its row's `_id`, and one with neither fails for good, as does a key with a
   null field. The row's own `_id`, if any, is replaced by the key's. Alternative: an `id_field`
   param, or a compound key joined into a string as I168's `_id`.
4. **Upserts replace.** An insert, update or message is `update` with `{q: {_id}, u: <row, _id
   first>, upsert: true}`, the whole document replaced; a delete is `delete` with `limit: 1`. No
   `$set` of the changed fields: every source gives whole rows (a MongoDB source with
   `updateLookup`). DDL is acknowledged and not written; a row that is not a map fails for good.
   Alternative: a `$set` mode for partial rows.
5. **Unordered, yet in order per key.** Within a batch only each document's last record is sent (it
   covers the earlier ones, whose effect it replaces), so the statements of one `update` and one
   `delete` per collection touch distinct documents and `ordered: false` cannot reorder a key;
   commands are cut at `max_request_bytes` (8 MiB) and 100,000 statements. When a document fails,
   the batch is written again from that document's first record in runs of distinct documents, in
   order, so the `BatchError` names the exact record that fails and every record before it is
   written. A command's error (or its write concern's) names the first record of the earliest
   document not written. Alternative: ordered bulk writes, record by record, slower on every batch.
6. **Errors.** A write error of a document is permanent (a validator's 121, a unique index's 11000,
   a document too large, a bad `_id`), unless its code says it may pass (a write conflict, a primary
   stepping down, interruptions, lock and time limits); a command's error is retried by the pipeline
   (authentication, a lost primary, a `writeConcernError` with `wtimeout`) unless it is the
   statements' or the collection's fault (codes 2, 9, 14, 72, 73, 10334). A document over 16 MiB,
   or a decimal that a `decimal128` cannot hold exactly, fails before anything is sent. Alternative:
   an unknown code as transient.
7. **Write concern.** `write_concern` is `w` (`majority`, a number, a tag set), `journal` is `j`,
   `write_timeout` is `wtimeout`; left out, the server's default (majority on a replica set since
   5.0). `w: 0` is refused: an unacknowledged write cannot be checkpointed.
8. **Where documents go.** The namespace `db.collection` names both (split at its first `.`);
   `database` replaces the database (a namespace without `.` is then the collection), `collection`
   replaces the collection. Names are not checked beyond what the server refuses (code 73,
   permanent). The sink never creates collections or indexes: MongoDB creates a collection at its
   first write. Alternative: templates as I186's `{namespace}`.
9. **No transactions.** The source's transactions are not kept: each document's write is atomic, a
   batch is not. A transaction would need sessions, which I181 does not speak (its decision 8).
10. **A `ParallelSink`** (a job's writers share the client's pool, `max_conns`, 4), and a `Tester`
    whose test finds the primary, authenticates and pings it.
11. **Interop.** `test/sync/mongo_sink_test.go`, on I181's replica sets (`test/sync/mongo.sh`,
    MongoDB 6, 7 and 8): 3,000 changes of 100 keys written in random batches, each sometimes written
    again from an earlier record, ending as the changes leave them; `generate`'s records of every
    type through a pipeline killed at random crash points, over TLS, ending equal to a run without
    kills; a collection of every BSON type (ObjectId, string and int `_id`s, `decimal128`,
    timestamps, UUIDs, regular expressions, min and max keys, nested arrays) read as records with
    the key `_id` and written through the sink twice, the copy byte for byte the same; a validator's
    refusal permanent; the connection test. MySQL-to-MongoDB and MongoDB-to-MongoDB *pipelines*
    wait on I174's and I182's sources; the collection copy stands in for the second until then, and
    I192's matrix is to run both. Alternative: hold I183 until those sources are merged.
12. **Size**: about 620 lines of code and 950 of tests; nothing is left, no I183b.

### I183b One set of MongoDB connection params

I182's source and I183's sink were built at once and took different connection params: the source
`uri`, `password_env` and `timeout`, the sink `uri` or `hosts` and a dozen more. Made one set, so
that a MongoDB is configured the same way for either. Done on 2026-11-09. Decisions made, pending
review:

1. **The sink's set, for both.** `ConnParams` in `sync/mongo/conn.go`: `uri` or `hosts`,
   `replica_set`, `username`, `password_env`, `auth_source`, `tls`, `ca_file`, `cert_file`,
   `key_file`, `server_name`, `connect_timeout`, `timeout`, `max_conns`; the source gains all but
   its three, none renamed, so neither side's configs change. `sinkconn.go` is gone; `NewClient`
   checks the params and makes the client for both. Alternative: the source's three only, TLS and
   users in `uri`.
2. **Declared flat in both, kept alike by a test.** `config` reads no embedded structs, so `Source`
   and `Sink` each declare `ConnParams`' fields first, with its names, types and tags (the docs
   now worded for either), and copy them with `conn()`; `TestConnParamsShared` fails on any
   difference. Alternative: a nested `connection` object (a new config shape), or embedded structs
   in `config` (shared code, left alone).
3. **The source's `uri` no longer required**: `uri` or `hosts`, as the sink's, checked in
   `Provision`.
4. **The params replace the connection string's settings, and nothing else.** The string's
   `appName` now stands (the sink set `fit-sync` over it), and so does its `socketTimeoutMS` when
   `timeout` is left out (the source set 60s over it); `fit-sync` and 60s when neither sets them. The checks are the sink's: a password in `uri` refused, `password_env` needs
   a user (`username` or the one in `uri`), a user needs `password_env`, `username` and a user in
   `uri` refused together, the TLS files need `tls`.
5. **The sink's connection test is `Ping`**, as the source's, so its errors start with `mongo:`.
6. **Interop**: the source's TLS subtest of `TestMongoSourceStream` now connects with `username`,
   `tls` and `ca_file` instead of the user and TLS in `uri`; the others keep `uri`.

### I184 A Kafka client

- **Do**: package `sync/kafka/wire`: the protocol versions needed for metadata, produce, fetch,
  list offsets and API versions; record batches v2 with gzip, snappy, lz4 and zstd; SASL PLAIN and
  SCRAM, TLS; leader changes followed; Kafka 3.x and Redpanda.
- **Done when**: interop in Docker with both; every compression both ways; a leader change mid-fetch
  and mid-produce loses nothing.
- **Touches**: `sync/kafka/wire`, `test/sync`.

**Decisions pending review** (made while building it, 2026-11-08; each stands until the user rules on it):

1. **Where it lives.** Package `sync/kafka/wire`, named `kafkawire` (not `wire`, which the MySQL,
   PostgreSQL and MongoDB clients' `sync/*/wire` would share). A `Client` of `Config` (bootstrap
   brokers, client ID, TLS, SASL, timeouts, retry time, size limits) with `Metadata`, `Partitions`,
   `Produce`/`ProduceRecords`, `Fetch`, `ListOffsets`/`Offset`/`OffsetAt`, and, beyond the plan,
   `CreateTopics` and `DeleteTopics` (for the tests, and for I185 to make a sink's topic), plus
   `PartitionFor`, the Java client's murmur2 partitioner, so that keys land where Java producers put
   them. Alternative: no admin APIs, topics made by the tests with the brokers' tools.
2. **Blocking connections, not fib.** Each broker connection is a `net.Conn` (TLS through
   `crypto/tls`), written under a mutex with many requests in flight and read by a goroutine of its
   own that hands each response to its caller in order; a broker works one request of a
   connection at a time, so fetches, which wait on the broker, have a connection per broker of
   their own beside the one for produce, metadata and offsets. This is decision 036's "connections
   to databases are few and long, each on a goroutine": no fib worker is held, and Kafka's
   long-polls gain nothing from an event loop. Alternative: fib's engine with a parser per
   connection.
3. **Non-flexible API versions only.** Produce 3–8, Fetch 4–11, ListOffsets 1–5, Metadata 1–8,
   CreateTopics 2–4, DeleteTopics 1–3, SaslHandshake 1, SaslAuthenticate 1, ApiVersions 0; the
   highest both sides speak is used, from ApiVersions at connect. These are all inside Kafka 4.0's
   baseline (KIP-896) and Redpanda's, and need no tagged fields; zstd needs Produce 7 and is refused
   on an older broker. Alternative: the flexible versions with compact encodings, needed only when
   a broker drops these.
4. **Codecs.** gzip from the standard library; snappy and zstd from `klauspost/compress`, which fit
   already requires for HTTP compression (no new dependency); LZ4 written in the package (the frame
   format and xxHash32, about 300 lines), since no LZ4 is among fit's dependencies. Snappy is
   written in xerial's framing as the Java client writes it and read raw or framed; LZ4 is written
   as independent 64 KiB blocks and read with any block size, linked blocks and checksums. Every
   decompression is bounded by `MaxBatchBytes` (64 MiB) before it allocates, and a response by
   `MaxResponseBytes` (128 MiB). Levels: gzip 1–9 and zstd 1–22, 0 the default. Alternative:
   pierrec/lz4 as a new dependency.
5. **Not idempotent, at least once.** Batches go with no producer ID: a produce whose answer is
   lost (a connection failing, a leader stopping) is sent again and may be written twice; the
   leader-change interop saw 50 records written twice out of 260,000, and none lost. Order per
   partition holds while a caller has one produce per partition in flight. Alternative:
   `InitProducerId` and sequence numbers (the idempotent producer), left to I185 if its sink needs
   duplicates gone.
6. **Leaders followed by retrying.** A partition refused for a retriable reason (not the leader,
   no leader, an unknown topic, too few replicas, a timeout, a failed connection) is sent again
   after a backoff of 100 ms doubling to 1 s, with the metadata read again when the error says the
   leader moved, until the call's context ends or `RetryFor` (30 s) has passed; the leader epoch is
   not sent (-1), so the client does not fence itself against stale leaders by epoch. A broker
   silent past `RequestTimeout` (30 s, beyond the wait it was asked for) fails its connection and
   every request on it. Alternative: a count of retries.
7. **One call, many partitions.** `Produce`, `Fetch` and `ListOffsets` take many partitions,
   grouped by leader, one request per leader, the leaders in parallel, and return a result per
   partition in order; at most one batch per partition per call. A fetch returns when every leader
   has answered, so a partition with records waits for an idle broker's `MaxWait`; a partition
   retried after another brought records is asked without waiting. Alternative: a fetcher per
   broker streaming into a channel, which I185's source may add on top.
8. **Not a consumer group.** No group, commit or heartbeat APIs: offsets are the caller's, kept in
   sync's checkpoint (I185). `ReadCommitted` drops aborted transactions' records and control
   batches; message sets of magic 0 and 1 (written by brokers older than 0.11, or kept from then)
   are refused with an error rather than read. Alternative: reading old message sets.
9. **SASL.** PLAIN, SCRAM-SHA-256 and SCRAM-SHA-512 (the server's signature checked, at most 2^20
   iterations); a session with a lifetime is connected again at 90 % of it. No OAUTHBEARER or
   GSSAPI. PLAIN without TLS is allowed here; the config of I185 can warn. Alternative: refuse PLAIN
   without TLS.
10. **Interop.** `test/sync/kafka.sh` starts Kafka 3.9.1 (three KRaft brokers that are also the
    controllers, each with PLAINTEXT, SASL_PLAINTEXT and SASL_SSL listeners and a CA of its own)
    and Redpanda v25.2.9 (one node, PLAINTEXT and SASL listeners), and prints the environment;
    `test/sync/run.sh` runs it, so CI's `sync-interop` job runs the tests too. The leader-change
    test stops and kills a partition's leader with `docker` from the test (the containers named in
    `FIT_SYNC_KAFKA_CONTAINERS`) while records are produced and fetched; Redpanda, one node, has no
    leader change. On Kafka every codec is also a topic's `compression.type`, so the broker writes
    it from another codec and the client reads it; Redpanda does not recompress, so there each codec
    goes one way and back. Kafka 4.x is not tried. Alternative: a Redpanda cluster for leader
    changes too.
11. **Size**: about 3,000 lines of code and 2,800 of tests (unit tests on an in-process fake
    cluster, 98.7 % of statements without containers); no I184b.

### I185 Kafka as a source and a sink; TiDB through TiCDC

- **Do**: source `kafka`: topics and partitions read from offsets in fit's checkpoint (not a consumer
  group, so the checkpoint stays with the rest; a group commit optional for visibility), a codec per
  topic (`json`, raw, canal-json, Debezium's JSON, TiCDC's simple protocol, a WASM codec); sink
  `kafka`: the record's key as the message key, the topic from the namespace, batched and compressed,
  acknowledged by all in-sync replicas by default. TiDB as a source: a TiCDC changefeed to Kafka with
  canal-json or the simple protocol, decoded into records with the commit TS, and the TiDB snapshot
  over the MySQL protocol at a TS (`tidb_snapshot`) before it.
- **Done when**: interop with Kafka and Redpanda; Kafka-to-Kafka copies keep per-key order across a
  kill; a TiDB cluster with TiCDC in Docker syncs into PostgreSQL and ends equal by checksum.
- **Touches**: `sync/kafka`, `sync/tidb`, `test/sync`, `docs`.

**Decisions pending review** (made while building it, 2026-11-09; each stands until the user rules on it):

1. **Where it lives.** Package `sync/kafka` (`synckafka`): the source and sink `kafka`, and the
   codecs `raw`, `canal_json`, `debezium` and `ticdc_simple`, which are Kafka's change formats but,
   as modules of `sync.codecs`, also read files, HTTP bodies and S3 objects. Package `sync/tidb`
   (`synctidb`): the source `tidb`. `syncmysql` exports `ConnParams` and `WireConfig` (two lines) so
   that the tidb source's connection takes the mysql connectors' params and checks. Alternative: a
   package `sync/cdc` for the codecs.
2. **Offsets in the checkpoint alone; no group commit.** The plan's optional commit to a consumer
   group for visibility is not built (the I184 client has no group APIs); the source reports its
   lag instead, `Run.ReportLag` in `offsets` (I189's `LagOffsets`): the sum over its partitions of
   the high watermark less the next offset, after each fetch. Offsets count control batches and
   compacted gaps, so it is about the records behind, not exactly. Alternative: `OffsetCommit` to a
   group named in the params.
3. **The position** is `{"offsets": {topic: {partition: next offset}}}`, every partition's, set on
   the last record of each message. The records one message becomes are a transaction (the codec's
   ID, TiCDC's commit TS, or `topic/partition@offset`), so a checkpoint never falls inside a
   message. A message that becomes no record (TiCDC's watermark every second, its bootstraps) does
   not move the checkpoint by itself: the next record's position covers it, and after a long quiet
   time a restart reads those watermarks again. Alternative: a positional no-op record the runtime
   would acknowledge without the sinks.
4. **One codec per source**, not one per topic as the plan has it: a pipeline per format. `raw`
   both ways by default, so a Kafka-to-Kafka copy is byte for byte; a null value is a delete of the
   message's key (`tombstones: "delete"`, `skip` for Debezium's tombstones after its deletes), and
   the sink writes a keyed delete as a tombstone (`deletes: "tombstone"`, or `encode`). A codec may
   decode keys too (`KeyDecoder`, new): `debezium` reads Debezium's key events into the key's
   columns. A value a codec refuses is a message record of the value and the error, as the file
   source's lines.
5. **Keys and partitions.** A record's key goes out as its text when it is a string, bytes,
   decimal or JSON, as plain JSON otherwise (`{"id":1}`), and to the partition the Java client's
   murmur2 gives those bytes; keyless records of a batch go to one partition, the next batch's to
   the next. Partition counts are read again after a minute.
6. **The sink is at least once, not idempotent** (I184's decision 5 stands): the Kafka-to-Kafka
   interop copied 3,000 messages as 3,076 across 12 kills, each key's first copies in order.
   Alternative: the idempotent producer (`InitProducerId` and sequences), which only dedups within
   one producer session anyway.
7. **Batches.** Each partition's messages go in record batches of at most `max_batch_bytes`
   (1 MiB, before compression), the partitions' first batches in one produce, their second in the
   next; a partition refused stops there and the `BatchError` names the batch's earliest record not
   written. `MESSAGE_TOO_LARGE` of a batch of several sends them one at a time; of one message it
   fails that record for good (`Permanent`). Defaults: `acks: "all"`, `compression: "lz4"`; topics
   are created only with `create_topics`.
8. **Where reading starts.** Without a checkpoint, at `start` (`earliest`); a partition the
   checkpoint lacks, or one added later (looked for each minute), at its earliest. An offset
   retention removed stops the pipeline (`offset_reset: "error"`) rather than skipping silently.
   Committed messages only by default (`read_uncommitted` to change). A job stops at each
   partition's end as it was when it started.
9. **The tidb source** takes the kafka source's params nested (`kafka`), its codec picked by
   `protocol` (`canal-json`, needing `enable-tidb-extension=true` for the commit TS, or `simple`).
   The snapshot reads at `@@tidb_current_ts` of a transaction begun for it, with `tidb_snapshot`, in
   keyset pages by primary key (`page_rows`), each row's position the table and its key; changes
   committed at or before that TS are dropped. The changefeed must predate the snapshot and the
   snapshot must end within `tidb_gc_life_time`; neither is checked. Only `tables`' changes are
   kept; without `tables` there is no snapshot. Alternative: start the changefeed from the
   snapshot's TS through TiCDC's API, which needs TiCDC's address and rights.
10. **Values.** TiCDC's canal-json writes ENUM and SET as their numbers and binary columns as
    ISO-8859-1 text; `canal_json` reads binary back to bytes and keeps ENUM and SET as text of the
    number, and the tidb source turns them into labels from the schema it reads at start, as its
    snapshot has them. BIT is a uint, TIME text, DATETIME and TIMESTAMP times in `time_zone` (UTC),
    a bool encodes as tinyint `1`/`0`. The simple protocol is typed by the schema of its bootstrap
    and DDL messages; a row before any (a source started between bootstraps) stays text without a
    key, unless the tidb source seeded the schema from `information_schema`. TiCDC's Debezium
    writes binary columns as base64 strings without a type that says so (they stay strings) and
    decimals as doubles (floats); the `debezium` codec decodes Kafka Connect's `Decimal` and
    Debezium's time types when a schema has them, and writes events without a schema.
11. **Interop.** `test/sync/tidb.sh` starts PD, TiKV, TiDB and TiCDC v8.5.3 on `kafka.sh`'s network
    (TiDB on `TIDB_CDC_PORT`, 29690) and `run.sh` starts it after Kafka, so CI's `sync-interop` runs
    the tests: a Kafka-to-Kafka copy of 3,000 keyed messages killed at random points, on Kafka and
    Redpanda; `generate`'s records through the sink and back through the source in `json`,
    `canal_json` and `debezium`; and TiDB into PostgreSQL 17 through each protocol, a snapshot of
    300 rows then 600 mixed writes (inserts, updates, deletes, key changes in transactions), killed
    at random points, ending equal by checksum. TiCDC in Docker was feasible; its messages are kept
    in `sync/kafka/testdata` for the codecs' unit tests.
12. **Size**: about 2,300 lines of code (the codecs 1,040), 1,600 of unit tests against a fake
    cluster and a fake TiDB (97.5% and 96.7% of statements without containers), 520 of interop; no
    I185b.

### I186 Redis as a source and a sink

- **Do**: a RESP2 and RESP3 client (`sync/redis/wire`) with TLS and ACL authentication; source
  `redis`, streams read with `XREAD` from the checkpoint's ID; sink `redis`, `XADD` to a stream from
  the namespace, or one key per record (`SET` of the document, `HSET` of its fields, `DEL` on delete)
  with an optional TTL, pipelined.
- **Done when**: interop with Redis 7 and Valkey in Docker; a stream copied across a kill loses
  nothing.
- **Touches**: `sync/redis`, `test/sync`, `docs`.

**Decisions pending review** (made while building it, 2026-11-08; each stands until the user rules on it):

1. **A RESP client of sync's own, not shared storage's.** `tlsmgr/sharedstore` already speaks to
   Redis, but its client is unexported, RESP2 only, reads replies two deep, has no pipelining and is
   tied to `config.RedisStorage`; `sync/redis/wire` (package `wire`) is the plan's: RESP2 and RESP3
   (every RESP3 type, attributes skipped, pushes dropped), `HELLO 3 AUTH user pass SETNAME`, falling
   back to RESP2 with `AUTH` when the server does not know `HELLO`, `SELECT`, TLS, pipelining, a
   context's end or deadline interrupting a blocked read (the connection is then broken and dialled
   again), replies bounded (a string 64 MiB, 1<<20 elements and 256 MiB a reply, 8 deep).
   `sharedstore` is left as it is, outside this item's package. Alternative: move `sharedstore` onto
   `wire` in a follow-up, so that fit has one Redis client.
2. **One connection each, on the pipeline's goroutine.** The source and the sink each hold one
   blocking connection (Redis is a database: few long connections, not fib's engine), dialled at the
   first use and again after it breaks; the sink is not a `ParallelSink`, so a job writes it through
   one writer. Alternative: a small pool and `ParallelWrites`.
3. **No consumer group.** The source reads with `XREAD` after the checkpoint's entry IDs, which are
   the only cursor; nothing is acknowledged or removed on the server, and no group is made.
   Alternative: `XREADGROUP` with `XACK` from `Confirmer`, for servers whose operators expect a
   group's pending list.
4. **The position is every stream's last ID**, `{"streams": {...}}`, on every record, so a restart
   reads each stream on after the last entry the sinks have. A stream added to `streams` later
   starts by `snapshot`; one removed is dropped from the position. Entries trimmed away before they
   were read are not seen, and not detected. Stream keys must be UTF-8 (they are JSON in the
   checkpoint). `snapshot: "never"` reads the last ID at each start without a checkpoint, as the
   ClickHouse source reads its greatest cursor.
5. **Exactly once into a stream by IDs, at least once otherwise.** `entry_id: "auto"` (the default)
   lets the server pick IDs, so a replay adds entries again; `entry_id: "key"` takes the record's key
   (its `id` field, or the key itself) as the ID, and the server's refusal of an ID at or below the
   stream's last (`equal or smaller than the target stream top item`) counts as written: a stream
   copied from the `redis` source holds every entry once, with its ID, across kills. It needs IDs
   that grow per stream, as a stream's own do. Alternative: deduplication keys written with each
   entry, or a Lua script.
6. **The entry forms.** `form: "fields"`: the row's fields as text, maps and arrays as plain JSON,
   nulls left out, a row of no field refused; the source reads values that are not UTF-8 as bytes,
   and gives each entry the key `{"id": "<entry ID>"}` and the ID's milliseconds as its commit time.
   `form: "record"`: one field named `record` (fixed, not a param) holding the canonical JSON; read
   back, the record keeps its operation, key, namespace (unless the source's `namespace` is set),
   rows and commit time, its transaction is dropped (the entries of one may not all be there), and
   an entry that is not a record is read as fields rather than failing the source, which would
   restart it for ever. `op_field` puts the operation in a field for `form: "fields"`; without it a
   delete is written as its row, as an insert would be.
7. **Keys.** `key` is a template of `{namespace}` and `{key}`, a key of several fields their values
   joined by `:`, a record without a key refused; `value` `json` (the row's plain JSON, `SET`),
   `record` (canonical JSON, `SET`) or `hash` (`MULTI`, `DEL`, `HSET`, `PEXPIRE`, `EXEC`, so that no
   field of an earlier row stays and readers see no key half written); a delete is `DEL`; `ttl` is
   `SET ... PX` or `PEXPIRE`. Stream params with `mode: "keys"` and key params with
   `mode: "stream"` are refused.
8. **Which server errors are retried.** `LOADING`, `OOM`, `READONLY`, `BUSY`, `TRYAGAIN`,
   `MASTERDOWN`, `NOREPLICAS`, `CLUSTERDOWN`, `MOVED`, `ASK`, `NOAUTH`, `NOPERM`, `WRONGPASS`,
   `MISCONF`, a connection's error and a timeout go to the pipeline's `retry`; any other error the
   server answers a record's command with (`WRONGTYPE`, an ID refused) is the record's, for good, and
   `on_error` takes it. A connection lost mid-pipeline fails the round trip's first record, those of
   earlier round trips acknowledged.
9. **Defaults and names.** `address` defaults to `127.0.0.1:6379`, as `acme.shared_storage.redis`
   does; `password_env` without `username` authenticates as the default user; `protocol` 0 (auto),
   2 or 3; the connection is named `fit-sync`; `count` 1000, `block` 5s (at least 1ms, never
   `BLOCK 0`), `max_pipeline` 1000, `timeout` 10s; `snapshot` `initial` or `never`, as the
   ClickHouse source's. A standalone server or a primary: no Redis Cluster or Sentinel.
10. **The configuration table's `sync` row.** `docs/configuration*.md` had three `sync` rows, one
    left by each merge of I169, I170 and I171; they are one row again, with Redis added.

### I187 S3 and compatibles as a source and a sink

- **Do**: SigV4 signing over fib's asynchronous HTTP client; sink `s3`, objects of batched lines
  (JSON, CSV or a codec) rolled by size and time, named by a template with the date and the batch's
  first position, gzip optional, written only once complete; source `s3`, objects under a prefix
  listed in key order and read as lines, the last key and offset as the checkpoint.
- **Done when**: interop with MinIO in Docker; an object written twice in a replay has the same name
  and content; the source resumes inside an object.
- **Touches**: `sync/s3`, `test/sync`, `docs`.

**Decisions pending review** (made while building it, 2026-11-08; each stands until the user rules on it):

1. **Where it lives, and SigV4 written here.** Package `sync/s3` (`syncs3`): the sink, the source,
   a `Conn` of their own (the role's engine or one of its own, as I168's) and `Signer`, AWS
   Signature Version 4, checked against AWS's four published examples. Nothing in fit or fib signed
   SigV4 or spoke S3 (the media library and backups write to disk), so there was nothing to reuse;
   `Signer` is exported for whatever speaks S3 next. Alternative: an `internal/sigv4` package now.
2. **Credentials.** `access_key_id` in the config, its secret named by `secret_access_key_env`, and
   `session_token_env` for temporary credentials; requests are not signed when neither is set (a
   public bucket). They are read once when the pipeline loads: no instance profile, web identity or
   `~/.aws` files, and temporary credentials are not renewed. Alternative: AWS's default chain.
3. **Addressing.** `addressing` is `virtual` (the bucket in the host) when `endpoint` is left out,
   which is AWS, and `path` otherwise, which every compatible takes; a bucket with a dot is refused
   with `virtual` over HTTPS, since no certificate covers it. Bucket names are checked (3 to 63 of
   lower-case letters, digits, `.`, `-`, `_`). Alternative: `path` everywhere.
4. **What is signed.** The whole body's SHA-256 (no `UNSIGNED-PAYLOAD`, no chunked signing): the
   object is in memory anyway, and the server then refuses a body that did not arrive whole. The
   signed headers are `host`, `content-type`, `content-md5` and `x-amz-*`; requests go over HTTP/1.1
   (ALPN), as S3 speaks no HTTP/2. Alternative: unsigned payloads, to spare the hash.
5. **An object is a batch.** The sink puts each batch it is given as one object, or several in
   order past `max_object_bytes` (64 MiB, at most 5 GiB, one `PUT`'s limit); so objects roll by the
   sink's `batch` (`max_records`, `max_bytes`, `max_wait`), which the docs tell users to raise, with
   the `queue`. It keeps nothing across batches: acknowledging records not yet in an object breaks
   the checkpoint's rule, and multipart parts are not objects until completed, so no multipart
   upload. Alternative: the sink holding lines across batches and acknowledging them once their
   object is put, which the runtime cannot express (a `Write` that waits holds the pipeline).
6. **Names.** `key` is a template (`{namespace}/{date}/{timestamp}-{position}{ext}` by default)
   of the object's first record: its commit time in UTC (the time of the write when it has none,
   which a replay does not repeat) and `{position}`, 20 hex digits of a SHA-256 of its namespace and
   position, or of the nearest position before it and how far (`+n`), after it (`-n`), or, with no
   position in the batch, of the object's lines. `{position}` is required. A batch written again
   makes the same names and bytes (gzip's header has no time or name), written over; a replay cut
   into other batches makes other names, so records between the checkpoint and a crash can be in
   two objects: at least once, as every sink. A leading `/` of a namespace and of a key is dropped;
   a key past 1024 bytes fails its record for good. Alternative: Kafka Connect's objects aligned
   on offsets, which needs numeric positions.
7. **Namespaces.** When `key` names the namespace, a new object starts at each change of it, in
   order; interleaved namespaces make small objects. Alternative: grouping a batch by namespace,
   which reorders records across namespaces.
8. **Lines.** `codec` as the `file` sink's (`line` by default, `json`, `csv`, a WASM codec), `{ext}`
   `.txt`, `.jsonl` or `.csv` by the codec's name and `.gz` with `gzip`, `Content-Type` to match
   (`application/gzip` gzipped, no `Content-Encoding`, so the object downloads as it is). No
   `template` as the `file` sink has. Alternative: `json` by default.
9. **Errors.** A failed `PUT`, of any status (a missing bucket, denied access, 5xx), is retried by
   the pipeline; a record the codec cannot encode, or a key too long, fails for good, the objects
   before it written. A failure past a batch's first object is a `BatchError` at the object's first
   record. Alternative: 403 and 404 as permanent.
10. **The source lists by key.** `ListObjectsV2` under `prefix` from the last key (`start-after`,
    or `start_after` at first), in pages of 1,000; keys ending in `/` are skipped. Keys must grow:
    an object added under a key before the last is not seen, and one written over after it was read
    is not read again (the `s3` sink's names grow with time). A stream lists again every
    `poll_interval` (30s); a job ends after one listing. No suffix or glob filter. Alternative:
    remembering every key seen, which grows without bound, or S3's event notifications.
11. **Reading.** An object is read in ranges of `range_bytes` (8 MiB, at most 64 MiB), each with
    `If-Match` on the listing's ETag: an object changed while being read fails the run, and the
    pipeline starts again from its checkpoint; one gone (404) is skipped with a warning; a range at
    the end (416) is the end. Objects whose keys end in `.gz` are gunzipped (`decompress`), and one
    resumed is read again from its start, the lines before the offset skipped. Lines as the `file`
    source's: a `\r` before the newline dropped, a last line without a newline read,
    `max_line_bytes` (1 MiB) cut, a line the codec fails on a message record with the error.
    Alternative: whole objects, one `GET` each, bounded by the client's answer limit.
12. **Records and positions.** The namespace is the object's key (or `namespace`), the key
    `{object, offset}` (and `n` for a codec's second record of a line), the commit time the
    object's last modification. Each line's last record has the position `{"key", "offset"}` past
    the line, and an object's last line `"end": true`, so a restart goes on inside the object or
    after it. The source is not a `Chunker`: a job reads the objects in order. Alternative: chunks
    of objects for jobs.
13. **Interop.** `test/sync/run.sh` also starts MinIO and SeaweedFS's S3 gateway (`fit-sync-minio`
    on 29215, `fit-sync-seaweed` on 29216, each with an access key; `FIT_SYNC_MINIO_*`,
    `FIT_SYNC_SEAWEED_*`). MinIO's own images are no longer pulled from Docker Hub or Quay (pull
    access denied), so the script runs `pgsty/minio:RELEASE.2026-08-04T00-00-00Z`, a community
    build of it, and `chrislusf/seaweedfs:4.50`. Over HTTP only; TLS is tried against an `httptest`
    server in the unit tests. Alternative: Garage, or MinIO built from source.
14. **`ParallelSink`**: a job's writers each put objects of their own names.
15. **Size**: about 1,300 lines of code and 1,350 of tests (97.9% covered without containers);
    nothing is left, so there is no I187b.

### I188 The Admin API and `fit sync`

- **Do**: `/sync/pipelines` in the Admin API (and through the console): each pipeline's state,
  position, lag, throughput, queue, errors and last error; pause, resume, a new snapshot; the
  checkpoint shown, set and reset (a reset that would skip or replay data needs `confirm`); a test of
  a source's or sink's connection; a dry run that reads the first records through the transforms and
  returns them, writing nothing and saving no position. `fit sync status|pause|resume|checkpoint|
  test|dry-run|run` on top.
- **Done when**: every endpoint has tests, through the console too; a dry run leaves the checkpoint
  and the sinks as they were; pause holds the source and keeps the checkpoint.
- **Touches**: `sync`, `admin`, `cli`, `docs`.

**Decisions pending review** (made while building it, 2026-11-08; each stands until the user rules on it):

1. **The Admin API finds the runtime by engine**, as it finds the streaming role's channels:
   `fitsync.Register(engine, rt)` when the role starts, `Lookup(s.configs.env.Engine)` in the
   handlers; `app.Manager` is unchanged. Alternative: a `Role(id)` accessor on `app.Manager`.
2. **The endpoints.** `GET /sync/pipelines` and `/sync/pipelines/{name}`; actions as the channels'
   are, `POST /sync/pipelines/{name}` with `{"action": "pause" | "resume" | "snapshot", "confirm"}`;
   `GET`, `PUT` (`{"position", "confirm"}`) and `DELETE` (`?confirm=1`, a query since a DELETE has
   no body) `/sync/pipelines/{name}/checkpoint`; `POST .../test` and `.../dry-run`; and `POST
   /sync/test` and `/sync/dry-run` with `{"pipeline": {...}}`, a pipeline's config not applied, for
   I190's editor, which need no running role. The console serves them as every endpoint, under
   `/api/m/admin/`, each change logged with its user. `/node` lists the capability `sync`.
3. **What needs `confirm`.** Positions are the sources' own and opaque, so whether a move skips or
   replays cannot be told: any change of the checkpoint needs `confirm`, a set to another position
   than the saved one, and a reset or a snapshot while there is a checkpoint; without it the answer
   is 409 with the checkpoint as it is. Setting the same position, and a reset or snapshot with no
   checkpoint, need none. Alternative: a source interface comparing positions, to ask only for skips.
4. **A new snapshot is the checkpoint removed and the pipeline restarted at once**: the source
   starts as its params say (a database's snapshot, then its changes), and a job starts anew,
   whatever its schedule. A **reset** is the same, except that a scheduled job waits for its next
   time. There is no incremental snapshot alongside a running stream, as Debezium's signals do;
   a `Snapshotter` interface for the database sources can add one. Alternative: snapshot only for
   jobs.
5. **Setting the checkpoint** stops the pipeline first (it writes what it holds and saves its
   checkpoint), writes the position, compacted, through the same temporary-file-and-rename save,
   and restarts it. The position is in the form `GET .../checkpoint` shows, a chunked job's in the
   job's form; a job's progress is kept, its `finished` cleared, so the job goes on from the
   position at once. A `null` or empty position is refused: that is a reset.
6. **Pause holds `Emit`**: the source waits at its next record, what it read is still written and
   the checkpoint still saved, so a paused pipeline's checkpoint comes to cover all it read. The
   state shows `paused` in place of `running`, and `paused: true` in any state. A pause is the
   runtime's, not the config's: it survives the pipeline's own restarts (its source failing, a
   control) and a reload that leaves the pipeline as it is, not a reload that changes it or fit
   restarting. An `http` source's requests wait while paused, until `sync_ingest`'s timeout answers
   503. Alternative: pause kept in the config, or in the checkpoint file.
7. **Resume also starts again a pipeline that stopped** (on_error `stop`, a dead letter not written)
   or was killed, from its checkpoint, so that a fixed sink needs no reload.
8. **Connection tests are an optional interface**, `fitsync.Tester` (`Test(ctx) error`), on a
   source or sink; `generate`, `memory` and `stdout` pass at once; a connector without it is
   reported `tested: false` with a pointer to the dry run, and does not fail the test. The running
   pipeline's own modules are tested, since loading a second instance of some (the file sink, one per
   path) is refused, with the runtime's lock held so that a reload does not let them go meanwhile;
   bounded by 10s. `file`, `elasticsearch`, `clickhouse`, `http` and `wasm` do not implement it yet
   (left for their owners; see below). Alternative: a test that loads every module anew.
9. **A dry run loads the source and transforms anew** (no sink), runs the source with `Once` from
   the checkpoint (or from its start with `"from": "start"`; a chunked job's checkpoint, which is
   the job's, reads from the start), takes up to `limit` records (10, at most 1,000), 4 MiB, or what
   comes within `timeout` (5s, at most 1m), puts them through the transforms, and answers each
   source record's canonical JSON with what it became or the transform's error, and whether the
   source ended, timed out or failed. Nothing is acknowledged, confirmed or saved. The source runs
   as pipeline `<name>#dry-run`, so that one found by its pipeline's name (the `http` source) does not
   take the running one's place; one that does not return within 5s of being stopped is answered
   without, and let go when it returns.
10. **Status's counters** are the pipeline's since the runtime started it: records read,
    acknowledged and failed, errors (failed write attempts, records a transform failed, dead letters
    not written, source failures) with the last and its time, the queue against its bounds, the
    throughput over the last ten seconds, and the lag as how long after its commit time the last
    acknowledged record was acknowledged. The lag behind the source's head (a slot's bytes, a
    topic's offsets) is I189's. `Status` gains JSON names; its checkpoint is written as the JSON it
    is.
11. **`fit sync status|pause|resume|snapshot|checkpoint|test|dry-run`** go through the Admin API
    the bootstrap config names, with `-c`, `-timeout` (90s), `-json` and the TLS flags, as `fit token`
    does; `snapshot` is added to the plan's list. The dry run's wait is `-wait`, since `-timeout` is
    the request's. `fit sync test` exits 1 when a test failed, every command 1 when the API refuses
    (a 409 with a hint to give `-confirm`), 2 when used wrongly.
12. **Size**: about 1,600 lines of code with their comments (sync 930, admin 360, cli 340) and 910
    of tests; nothing of the plan is left out, but the connectors' `Test` methods (point 8) are
    left to I188b or to the connectors' own iterations.

### I188b Connection tests for the earlier connectors

- **Do**: `fitsync.Tester` (I188) for the connectors built before it: `file`, `elasticsearch`,
  `clickhouse`, `http`, `redis` and `s3`, source and sink, and `wasm` if it has a sensible test; a
  cheap check that writes no record, against each package's fakes.
- **Done when**: each tests what it reaches as it will reach it, passing and failing in unit tests.
- **Touches**: `sync/file`, `sync/elasticsearch`, `sync/clickhouse`, `sync/httpsync`, `sync/redis`,
  `sync/s3`, `docs`.

**Decisions pending review** (made while building it, 2026-11-09; each stands until the user rules on it):

1. **Elasticsearch and OpenSearch.** `Conn.Test` asks the root endpoint again each time (what `Info`
   kept is not trusted), so the cluster answers, the credentials are taken and it is one of the two
   (an API key against OpenSearch fails, as at a write). The sink stops there: whether it may write
   the indices is not known without writing, and `_has_privileges` is Elasticsearch's only. The
   source also counts `index` (`/_count`), which needs the right to read it; a pattern or index that
   matches nothing (404) passes, since the source reads it as empty. Alternative: a 404 failing.
2. **ClickHouse.** `SELECT 1` in `database` (an unknown database fails), its answer checked to be
   `1`. The sink stops there (an insert right cannot be checked without inserting); the source also
   runs `SELECT * FROM (<table or query>) LIMIT 0` with its settings, which reads no row but needs the
   table and the right to select from it. Alternative: `EXISTS TABLE` for a sink whose `table` has no
   `{namespace}`, which needs a right a write-only user may lack.
3. **Redis and Valkey.** A connection of the test's own (the module's belongs to its pipeline's
   goroutine, used by one at a time): the dial says HELLO, authenticates and selects `db`, then
   `PING`, then it is closed. The streams or keys are not looked at (`TYPE` needs `@keyspace`).
4. **S3.** A `ListObjectsV2` of one key: under `prefix` for the source, which lists as it reads; at
   the bucket's root for the sink, where `AccessDenied` passes, since S3 answers it only to a request
   it authenticated (a bad key is `InvalidAccessKeyId`, a bad secret `SignatureDoesNotMatch`, a
   missing bucket `NoSuchBucket`) and a sink's user may put objects without listing them. No `HEAD`
   of the bucket: its answer has no body, so the code that tells those apart is lost. Alternative:
   a put of a marker object, which writes.
5. **file.** The source: the directory each path or glob starts from (its longest leading part
   without `*`, `?` or `[`) is there and lists, and each regular file the globs match now opens for
   reading, the first 1,000 at most; no file matching passes, since the source waits for its files;
   only regular files are opened (a FIFO would wait for a writer). The sink: none of fit's logs
   writes the path; a file there is a regular file (not a link, as the log files refuse) that opens
   for appending, without creating; and when there is none, or `rotate` makes files beside it, its
   directory takes a new file, `.<name>.fit-test-*`, made and removed at once: the only write of any
   test, of no record. Another file sink writing the path is not reported: a config tested before
   it is applied (`POST /sync/test`) replaces the running one's sink, and could not be told from a
   real clash. Alternative: `access(2)` on Unix, which Windows lacks.
6. **http sink: a `HEAD` of `url`** with the sink's headers and credentials, no body and no
   idempotency key: safe by HTTP's rules, and not a batch to any receiver. It passes on 2xx and on
   4xx other than 401, 403 and 407, since an ingest endpoint routing only its method may answer HEAD
   404 or 405, which still says the host, its TLS and its server are there; 401, 403 and 407 fail
   (credentials refused), as do a redirect (the sink does not follow one), 5xx, and a connection's
   or TLS's error. Alternatives: only a TCP and TLS handshake (tests no credentials), or a
   `test_method`/`test_url` param.
7. **http source: passes at once**, as `generate` does: it reaches nothing, its clients reach fit;
   whether it takes requests is the pipeline's state, not a connection.
8. **wasm source and sink: no test.** What they reach is the plugin's own: fit knows only
   `allow_hosts`, patterns with no scheme, port or path, so probing them would test nothing the
   plugin uses, and a test that passed would mislead. They stay `tested: false`, pointing at the dry
   run. A guest export, `fit_sync_test`, called by an optional `Tester` when the plugin has it, is the
   way; it changes the guest ABI and `plugin/wasm/guest`, so it is left to a later item.
9. **Size**: about 300 lines of code with their comments and 400 of tests; the Redis fake answers `PING`. Nothing is
   left of the plan but decision 8.

### I189 Metrics, logs and dead letters

- **Do**: metrics per pipeline and sink: records and bytes in and out, batches, retries, errors, dead
  letters, queue fill, lag in time (commit time to acknowledgement) and in position (a slot's or
  binlog's bytes behind, Kafka's offsets behind); log lines for state changes, rate-limited for errors;
  dead letters kept in a bounded file sink with the record, the error and the attempt count, and
  replayed from the Admin API.
- **Done when**: each metric is checked in a test; a dead letter replayed after the cause is fixed
  reaches the sink once.
- **Touches**: `sync`, `observe`, `admin`, `docs`.

**Decisions pending review** (made while building it, 2026-11-09; each stands until the user rules on it):

1. **Dead letters are a sink module, `dead_letters`**, named as the `dead_letter` sink (I163's
   decision 3 stands): it keeps each letter as a JSON line of `<state_dir>/<pipeline>.dead_letters.jsonl`
   (`path` to name another, relative to `state_dir`), synced before the write is acknowledged, and
   removals as lines of their own, the file compacted once the removed take more than the kept plus
   1 MiB. The runtime tells it the state directory and the pipeline when the pipeline is made; two
   sinks of one file (a reload overlapping) share it. It is refused among a pipeline's `sinks`.
   Other sinks stay usable as `dead_letter`; only `dead_letters` can be listed and replayed.
   Alternative: a store kept by the runtime itself whenever `on_error` is `dead_letter`.
2. **Bounds**: `max_records` 10,000 and `max_bytes` 64 MiB; `when_full` `refuse` (the default: the
   write fails, so after its attempts the pipeline stops before the record, nothing lost) or
   `drop_oldest` (counted in `fit_sync_dead_letters_dropped_total`). A letter larger than
   `max_bytes` fails for good. Alternative: drop the oldest by default.
3. **A replay stops the pipeline** (writing what it holds and its checkpoint), writes the letters
   itself on the request's goroutine, then starts the pipeline again, as `SetCheckpoint` does,
   so that the replay is each sink's only writer and nothing in the runtime's queue or watermark
   changes; one stopped starts again, as `Resume` would. A record a sink failed is written to that
   sink alone, as the transforms made it (the letter's `at`, `sink <name>`); one a transform
   failed goes through the transforms again and to every sink. Writes are batched by the sink's
   `batch` bounds and tried as `retry` says; a letter still failing, whose sink is gone, or that
   holds no record, is kept and reported; those written are removed. A finished job's letters
   replay too. Alternative: replayed records put into the running pipeline's queue, with no stop,
   which needs per-record routing in the dispatcher.
4. **A replay writes the record as it was**: a later change of the same key that reached the sink is
   overwritten; the docs say so. Alternative: refuse a replay unless the key has no later record,
   which fit cannot know.
5. **Metrics** come from `Runtime.Status` through the role's `app.Metered`, as the other roles'
   (`fit_sync_*`, labels `pipeline`, and `sink` for `fit_sync_sink_*`); counters are the pipeline's
   since it was started (a reload that changes it starts them at 0, as Prometheus expects of a
   restart). Bytes are records' sizes as the queue reckons them, not bytes on the wire. A sink's
   `batches` are the writes tried, `errors` those that failed, `retries` those tried again,
   `failed` records it still failed. No throughput gauge: `rate()` of the counters gives it. The
   status (`GET /sync/pipelines/{name}`) gains `read_bytes`, `sinks`, `source_lag` and
   `dead_letters`. Alternative: histograms of batch write times.
6. **Lag behind the source's server** is the source's to tell: `Run.ReportLag(behind, unit)` as it
   learns it, or a `Lagger` (`Lag() int64`, bytes unless a `LagUnit()` says) asked at each status
   or scrape; the postgres source's `Lag` (its slot's WAL) is so read already, with no change to it.
   MySQL's binlog bytes and Kafka's offsets behind are left to those sources' owners. Alternative:
   the runtime querying servers itself.
7. **Logs**: a pipeline's start of each run (from its checkpoint or its start), its schedule's next
   time, a kill, pause, resume, finishing and stopping are logged; sink attempts, transform
   failures, skipped records, dead letters written and not written are warnings at most one each
   10s per pipeline, the next line saying how many were held back (sink attempts were debug lines
   before). Alternative: a limit per error message.
8. **Endpoints**: `GET /sync/pipelines/{name}/dead-letters?after=&limit=` (100, at most 1,000, oldest
   first, `more` when there are more), `POST .../dead-letters/replay` with `{"ids"}` or
   `{"all": true}`, `DELETE .../dead-letters?id=..&id=..` or `?all=1`; 404 when the
   `dead_letter` sink is not `dead_letters`. `fit sync dead-letters [-after] [-limit]`,
   `-replay` and `-drop` with ids or `-all`; a replay with letters kept exits 1.
9. **Size**: about 1,000 lines of code (sync 760, admin 110, cli 150) and 900 of tests.

### I190 The workbench's Sync pages

- **Do**: a first-level **Sync** entry: pipelines as a table with state, lag and throughput sparklines;
  a pipeline's page with its editor made from the schema (each connector's fields), **Test
  connection**, **Dry run** with the records shown, the checkpoint with reset behind a confirmation,
  errors and dead letters with replay, pause, resume and a new snapshot; the assistant's read tools
  and a skill for sync (I124's registry).
- **Done when**: a pipeline is made, tested, dry-run, saved and watched entirely from the pages, in
  both languages, in the workbench's tests.
- **Touches**: `app/workbench`, `assistant`, `docs`.

**Decisions pending review** (made while building it, 2026-11-09; each stands until the user rules on it):

1. **One module, `sync`**, on the Admin API's server module, ranked after the gateway's features in
   the menu, with two pages in the menu (**Pipelines**, **Sync config**) and a pipeline's four as
   tabs that are links (`#/sync/pipeline/<name>`, `/edit/<name>`, `.../checkpoint`,
   `.../dead_letters`). It shows wherever `admin.console` does, with or without the sync role: with
   none running, the list says so and lists the draft's pipelines, so the first one is made from the
   pages too. `sync` leaves the "other" config page. Alternative: a module only while the role runs.
2. **Sparklines from the page's own polls**: `GET /sync/pipelines` every 2s, the last 60 kept while
   the workbench is open (two minutes), throughput as `throughput` and lag as `lag_seconds`; no
   history before the page opened. Alternative: series from `/metrics`, as the cluster's charts.
3. **The editor is the config's**: `C.subsection('sync.pipelines[i]')` in the shared draft, with the
   UI/.conf/.yaml/.json tabs of `sync` and the config bar's Check and Save and apply; **Test
   connection** and **Dry run** send the draft's pipeline as it stands to `POST /sync/test` and
   `/sync/dry-run` (from the source's start), so a pipeline is checked before it is saved; a running
   pipeline's own buttons test and dry-run what runs. **New pipeline** asks a name, a source and a
   sink from the schema's modules (generate and memory picked), and `state_dir` when the config has
   no `sync` yet, then opens the editor. **Remove the pipeline** takes it out of the draft; its
   checkpoint file stays.
4. **Confirms**: a new snapshot, a checkpoint set or reset, a replay and a drop each ask first, in
   words that say what is skipped, written again or lost, and then send `confirm`, so the API's 409
   is never shown; pause and resume ask nothing. The checkpoint page is read again only by its
   Refresh, not under an edit. Dead letters come 50 a page, with More.
5. **The assistant's sync tools are four read tools**, group `sync`, level read: `sync_pipelines`
   (all in short, or one whole with its running config), `sync_test`, `sync_dry_run` (5 records, at
   most 20, 5s) and `sync_dead_letters` (20, at most 100). The test and the dry run reach the
   source's and sinks' servers, as the pages' buttons do, and write nothing, so read is their level.
   None changes a pipeline: pause, resume, snapshot, checkpoint, replay and drop stay the person's,
   on the pages, and the skill says so. Alternative: propose tools whose steps are those actions, as
   the media tools' are.
6. **Records' values are not sent by default**: the records of a dry run and of dead letters reach
   the model with each value of their key, before and after replaced by its kind (`(string)`,
   `(int)`, a map's fields by name), unless the new setting `admin.assistant.data.send_records`
   (the settings page's checkbox) allows them; namespaces, operations, positions and errors always
   go. Alternative: always as they are, redacted as other results.
7. **On a manager**, `sync_pipelines` and `sync_dead_letters` take `member` (GETs, which the
   manager passes on); the test and the dry run do not, since the manager passes reads only.
8. **One skill, `sync-pipeline`**: building a pipeline (on the page with the page tools, or as a
   proposal) and finding why one stalls, lags, stops or keeps dead letters. The pipeline's last error
   and its dead letters have Ask AI buttons that run it.
9. **Secrets stay environment variables**: the pages edit `password_env` and its kin as any field;
   writing a secret into `admin.store` from the pages (I168's decision 12 waited on I190) needs the
   connectors to read a store reference, which is not a small change to package `sync` or its
   connectors, so it is left to a later item.
10. **The pages' tests**: the workbench has no JavaScript runner, so its tests read the pages as
    text, as `TestAskSkills` does: `TestSyncWords` checks that every word the Sync pages show is in
    English and Chinese with the same placeholders, every state a pipeline can be in is worded and
    the pages call each endpoint they offer; `TestAskSkills` checks the Ask AI specs. The flow (make,
    test, dry-run, save, watch, pause, resume, snapshot, checkpoint set and reset, dead letters
    replayed and dropped) was checked by hand in a browser, in both languages, against fit with
    `generate`→`memory` and `generate`→`http` (refused)→`dead_letters`. Alternative: a headless
    browser in CI.
11. **Size**: about 650 lines of pages and 20 of CSS, 330 of Go in `assistant` and 50 of skill;
    330 lines of tests.

### I191 Exactly once into transactional sinks

- **Do**: for the MySQL, TiDB and PostgreSQL sinks, `checkpoint: "sink"`: the position written to a
  table of the sink's (`_fit_sync_checkpoints`) in the same transaction as the records, and read back
  at start in preference to the local file, so a replay writes nothing twice.
- **Done when**: with a crash injected between the sink's commit and the local checkpoint, no row is
  written twice (checked with a sink table that counts writes); the local file still serves other
  sinks.
- **Touches**: `sync/mysql`, `sync/postgres`, `sync`.

**Decisions pending review** (made while building it, 2026-11-09; each stands until the user rules on it):

1. **A small interface in `fitsync`**: `CheckpointSink`, a `Sink` with `KeepsCheckpoint()` (its params
   say `checkpoint: "sink"`) and `ReadCheckpoint(ctx, pipeline, lineage)`, called on the pipeline's
   goroutine before each run, which returns the position the sink keeps and its lineage and tells the
   sink what to write from then on (`sync/sinkcheckpoint.go`; hooks in `start`, the reset and
   `SetCheckpoint`, and a `lineage` field in the checkpoint file). Each commit writes the position of
   its last record that ends a source transaction. Alternative: the position handed to every `Write`.
2. **When the sinks' position is used**: only when every sink of the pipeline keeps the checkpoint,
   all at the same position, of the file's lineage; otherwise the file serves, at least once as
   before. So exactly once holds in a pipeline of transactional sinks only, and a slower sink beside
   them keeps their replays. Alternative: each keeping sink skips, by matching positions, the records
   up to its own, which gives exactly once beside other sinks but loses everything after a position
   a source does not give again byte for byte.
3. **Lineage**: a random ID in the checkpoint file, new at a reset, a position set by hand and a new
   job from the source's start, saved to the file before the sinks write it; a sink's row of another
   lineage is ignored, so those are not undone by an older position in the sink. A reset of a
   pipeline with a keeping sink leaves a file of no position rather than none, so that it is not taken
   for a lost file; a stream whose file is gone (a `state_dir` lost) takes the sinks' shared lineage and
   position; a job does not, its plan being in its file. Alternative: the reset clears the sinks' rows,
   which needs every sink reachable at the time.
4. **Jobs read in chunks keep to their file**: their positions are the job's, written by several writers
   at once, whose commits come in any order. Alternative: a row per writer.
5. **The sink's row may lag the file** (a DDL record alone commits nothing), which is safe: a run starts
   at what the sink committed, never past it. What a commit holds past its last position is written
   again after a crash: a source transaction larger than `max_transaction_bytes` committed in parts,
   records without positions, and, with `on_error: "stop"`, what the MySQL sink commits before a
   refused record.
6. **The table is the sink's own and made by it** (`CREATE TABLE IF NOT EXISTS`, unlike the data tables of
   decision 036's ninth decision): `checkpoint_table`, `_fit_sync_checkpoints` by default, in
   `database` for MySQL (one is then required) and by the `search_path` for PostgreSQL; a row per
   pipeline name (`pipeline`, `lineage`, `position` as text, not JSON, which `jsonb` would reorder,
   `updated`). Two fits writing pipelines of one name to one table would share a row; deleting the row
   and the file starts over. Alternative: a key of the fit's node and the pipeline.
7. **What an earlier run left held is dropped** at `ReadCheckpoint` (MySQL's open transaction, PostgreSQL's
   held records): the run starts before it. Without `checkpoint: "sink"` the MySQL sink still writes it
   again on a new connection, as I176 does.
8. **PostgreSQL needs `transactions: "source"`**, which `checkpoint: "sink"` makes the default; `batch` is
   refused, since its transaction may end inside a source transaction. MySQL keeps both modes: `batch`
   commits at a batch's last record that ends one already. TiDB is as MySQL.
9. **Interop**: `test/sync/exactly_once_test.go`, a test source of 3,000 inserts of distinct keys in
   transactions of one to four, into a table whose `BEFORE INSERT` trigger counts every row written
   (an upsert written again fires it too), through pipelines killed twelve times at random points
   (after the commit and before the file's checkpoint among them) and run to the end: with
   `checkpoint: "sink"` as many writes as rows, every record once; with `checkpoint: "file"` none lost
   and some written twice, reported. MySQL 5.7, 8.0 and 8.4 and MariaDB 10.11 and 11 with both
   `transactions` modes as jobs; PostgreSQL 13 to 17 as streams, runs copied with `COPY` too. TiDB has
   no triggers: `TestMySQLSinkAcrossKills` gains a mode with `checkpoint: "sink"`, run on every server
   and TiDB, ending equal by checksum across kills and a replay.
10. **Size**: about 330 lines of code, 900 of unit tests (the runtime against a sink of the tests'
    that keeps its checkpoint, killed at every point, and each sink against its fake server) and 230 of
    interop; nothing left, so no I191b.

### I192 The interoperability and recovery matrix

- **Do**: `test/sync`: a Docker Compose matrix of every connector's servers and versions, and a test
  that, for each source against a fixed set of sinks, writes a mixed load, kills fit at random points
  several times and compares the sink with the source at the end; in CI on every push that touches
  `sync/`, the full matrix nightly, never skipped (the CI rule of v1.7.1).
- **Done when**: the matrix runs in CI and passes; a broken connector fails it.
- **Touches**: `test/sync`, CI config.

**Decisions pending review** (made while building it, 2026-11-09; each stands until the user rules on it):

1. **One pipeline per source into every sink at once** (`TestMatrix`, `test/sync/matrix_test.go`):
   PostgreSQL 17, MySQL 8.4, MongoDB 8, Elasticsearch, ClickHouse and Redis (keys), the checkpoint
   saved once all six have a record, each sink compared row for row with the source (and the source
   with the load's own model) at the end. One load per source, and the sinks judged against the same
   truth. Alternative: a pipeline per source and sink pair, six times the load and the time.
2. **The sources are those of changes**: PostgreSQL, MySQL and MariaDB, MongoDB, TiDB through TiCDC,
   and Debezium's events in Kafka; each one entry in `mxSources` (a family, whether its load has
   transactions and key changes, a setup that fills its table and applies the load). The sources
   polled by a cursor or reading appends (Elasticsearch, ClickHouse, S3, Redis streams, files, HTTP)
   have no updates and deletes to mirror, and keep their own across-kills tests. Alternative: an
   insert-only load for them in the matrix.
3. **The load**: a model of rows `id`, `v` (text with quotes, a backslash and non-ASCII), `n` (a
   nullable bigint, NULL one row in five) and `ver` (the load's count): inserts, updates, deletes, key
   changes, and transactions of two to four changes; 200 rows before the pipeline (its snapshot),
   then 500 transactions under 4 kills (1,000 rows, 2,500 transactions, 10 kills nightly), two to ten
   milliseconds apart. MongoDB's has no transactions (the client speaks no sessions) and changes a key
   by a delete and an insert, as Debezium's events do in Kafka. Types are the per-connector tests'.
4. **Kills** as the per-connector tests have them: the crash hook at a random point and count, the
   first run killed for sure, then a run to the end.
5. **Found by the matrix: a source of a snapshot and a stream killed before its first checkpoint
   lost changes.** PostgreSQL, MySQL and MongoDB (and TiDB, of the same shape), started again without
   a checkpoint, took a new snapshot and streamed from it, so the changes between the two runs were
   never read; a row the first run wrote to the sinks and that was deleted meanwhile stayed in them
   for good (seen in every sink, a few rows a run). **Fixed** (2026-11-09) by the runtime saving the
   source's start: `fitsync.Run.SaveStart(p)`, small and additive in package sync (a `Run.Start` func
   the runtime fills for pipelines that are not jobs, none in a dry run), by which a source with no
   checkpoint gives, before its first record, the position a run started again resumes from; the
   runtime saves it as the checkpoint at once (through the crash points `checkpoint_written` and
   `checkpoint_saved`, without telling a `Confirmer`). PostgreSQL gives its snapshot's state with no
   chunk done at the exported point, so a restart reads every table again from a new snapshot and
   streams from the first point (the path a checkpoint inside a snapshot already took, its slot never
   confirmed past that point); MySQL its binlog start with an empty snapshot position; MongoDB its
   start token with the snapshot not begun; TiDB its snapshot's TSO, read again at the same TSO
   (within `tidb_gc_life_time`, or the run fails with the existing "older than the GC safe point").
   The stream replayed over a newer snapshot converges in sinks writing by key, each key's last
   change coming last. The matrix kills every source's first run before its first checkpoint (that
   run's `checkpoint_interval` an hour, killed at a random write), `settle` and
   `FIT_SYNC_MATRIX_EARLY` gone; with `SaveStart` disabled it fails, with it it passes.
   Alternatives: each source streaming from the earliest position its server kept (PostgreSQL's
   slot's confirmed LSN holds only for a slot that outlives the run, and MySQL and MongoDB keep no
   such mark: the oldest binlog or oplog entry would replay far too much, or be gone); the source
   saving nothing until a record is acknowledged (the bug).
6. **Found by the matrix: ClickHouse's defaults lost to a snapshot.** Versions by commit time (the
   default) were the write time for a record without one, a snapshot's row, so a row read again
   after a kill outranked the earlier-committed changes replayed after it. **Fixed** (2026-11-09):
   such a record is version 0, below any change; rows of one version keep the one written last, as
   `ReplacingMergeTree` does, so a source with no commit times at all is still written as it comes.
   Flagged deletes already carried their commit time's version: the "no version" seen was
   `version_field`'s, which a delete of only the key lacks. The matrix's ClickHouse sink is the
   default now (`ReplacingMergeTree(_version, _deleted)`, flagged deletes, read with `FINAL`). Left:
   ties at one version (a transaction changing a key twice, MariaDB's commit seconds) are ordered by
   write, which a deduplicated replay can get wrong; `version_field` stays for those. Alternatives:
   versions from positions (not comparable across a source's kinds of position), snapshot records
   carrying the snapshot's time (only TiDB's have one; PostgreSQL's exported point has none).
7. **No Docker Compose file**: the servers are started by `test/sync/run.sh` and its helpers, as every
   interop test's already are, now by family (`SYNC_FAMILIES`: search, mysql, postgres, mongo, kafka,
   tidb, matrix), each family a CI job of its own on its own runner, in parallel, with `-timeout` and
   `timeout-minutes` sized to it; `matrix` starts one server of each kind without OpenSearch, Valkey,
   S3, Redpanda or the standalone TiDB. `SYNC_PREFIX` and the ports let a second set run beside the
   first. Alternative: a Compose file, a second description of the same servers.
8. **On every push, not only those touching `sync/`**: the v1.7.1 rule (no skipped jobs) and one
   workflow; the full matrix (each source on every server of its kind, Redpanda too) in
   `sync-matrix.yml` at 02:41 UTC and by hand, a job per source family. Alternative: path filters,
   which skip jobs.
9. **TiKV needs 123,880 open files**: CI run 42504979157 failed in `tidb.sh` because TiKV refused the
   runner's 65,536 and TiDB gave up on it; TiKV gets `--ulimit nofile=1048576`, TiDB and TiCDC
   `--restart on-failure`.
10. **Size**: about 1,100 lines of test, run.sh reorganized by family (about 150 lines changed), small
    additions to `kafka.sh` (`REDPANDA=0`), `mongo.sh` (`MONGO_VERSIONS`) and `tidb.sh`; decisions 5 and 6
    fixed after, so no I192b.

### I193 Benchmarks

- **Do**: on the benchmark host: files against Filebeat (lines a second, CPU, memory), MySQL to
  Elasticsearch and MySQL to Kafka against Canal and Debezium, PostgreSQL to Kafka against Debezium,
  a MySQL batch copy against DataX; with profiles, and the costs found fixed or recorded.
- **Done when**: the results are in `bench` and the scored report (rule 24), with where fit stands.
- **Touches**: `bench`, `docs`.

**The harness** (built 2026-11-09; the host's run, the results and the report are still to come, so
I193 stays todo): [`bench/sync`](../bench/sync/README.md). `bench/sync/run.sh SCENARIO` runs fit and
its rivals one at a time against fresh servers (scenarios `files`, `mysql-es`, `mysql-kafka`,
`pg-kafka`, `mysql-batch`), in Docker on a private network, no host port, CPU-pinned (mover, driver,
servers on separate physical cores), everything removed at the end; `bench/sync/plan.sh DATE` runs
every item 3 times into `bench/results/sync-DATE` and renders `tables.md` and `scores.md`.
`bench/sync/syncbench` (Go, fit's own wire clients) writes the load, counts at the sink, samples the
movers through the Docker API and saves fit's pprof CPU and heap profiles; `bench/cmd/benchreport`
renders `"kind": "sync"` results and scores them (workloads S1-S5). Smoke-tested end to end on
macOS Docker (arm64) at `SIZE=smoke`; not yet run on Linux.

**Decisions pending review** (made while building the harness, 2026-11-09; each stands until the user rules on it):

1. **Throughput from a backlog.** A stream's mover is `docker pause`d while its rows are written,
   then unpaused, so it drains a backlog at its own pace and the writer is never the bound; files
   and the batch's rows exist before the mover starts (syncbench starts its containers). Lag comes
   from a separate steady phase at a fixed rate (2,000 records/s for 20s), by matching each write's
   commit to the sink's count. Alternative: writing while the mover runs (`PAUSE=0`, kept as an
   option), which measures the writer as much as the mover.
2. **Counted at the sink, never from the mover**: lines in the output files, Elasticsearch `_count`
   (so its 1s refresh is in every mover's lag), Kafka offsets (a message a row), Canal's flat
   messages by their `data` rows (Canal puts a transaction's rows of a table in one message), the
   target's `COUNT(*)`. Extra records (counted twice) are reported, not scored.
3. **A stream's start by warm-up transactions until the sink's count rises**, then the count once
   settled is the base: rows written before a mover attached are not read by Canal or Debezium
   (`no_data`), nor fit (`snapshot never`), so a fixed warm-up count would hang.
4. **Each mover's defaults, except where the comparison needs alignment**: Filebeat's harvester
   backoff to 250ms and queue flush to 1s (fit's `poll_interval` and batch `max_wait`); fit writes
   Debezium's envelope to Kafka; one partition per topic; the batch copy at 4 readers/writers and
   1,000-row batches on both sides; no TLS; servers' durability relaxed alike. JVM heaps stay at what
   each image ships (Canal 2-3 GiB, Connect up to 2 GiB, DataX 1 GiB), which their RSS shows.
   Alternative: equal heaps.
5. **Debezium to Elasticsearch** is Debezium's MySQL connector plus Confluent's Elasticsearch sink
   14.1.2 in one Kafka Connect, the Kafka broker counted as a server, not the mover; topics renamed
   at the source (`RegexRouter`), the sink allowing no topic-changing SMT. **Canal to Elasticsearch**
   is canal-server plus its client adapter (built from the 1.1.8 release), both containers the mover.
6. **Images**: pinned tags (MySQL 8.0.42, PostgreSQL 17.5, Elasticsearch 7.17.28, Kafka 3.9.1,
   Filebeat 8.17.4, canal-server v1.1.8, Debezium connect 3.0.8.Final, Temurin 8u452); the Canal
   adapter, DataX 202309 (engine and MySQL plugins only, of a 1.7 GB tarball) and the Confluent
   sink downloaded with pinned sha256 and built into local images. MySQL 8.0 rather than 8.4, for
   `mysql_native_password`, which DataX's and Canal's drivers need.
7. **Scores**: S1 files 25% (four items: 128 B and 1 KiB lines, 1 and 16 files), S2 MySQL→ES 20%,
   S3 MySQL→Kafka 20%, S4 PostgreSQL→Kafka 15%, S5 batch 20%; metrics as the HTTP plan's (throughput
   50%, CPU 25%, memory 15%, lag 10%), the batch without lag (55/25/20). In benchreport beside the
   HTTP plan's, a plan folder being one or the other.
8. **Seen in the smoke runs (sizes too small to trust)**: fit's lag at low rates is its sink batch
   `max_wait` (1s); fit PostgreSQL→Kafka drained with little CPU, and Canal's and Debezium's
   Elasticsearch paths ran at under 1,000 rows/s with little CPU, each worth a look at the host's
   sizes before conclusions.

**The host's first run** (2026-11-09, `bench/results/sync-2026-11-09`; Ryzen 7 7800X3D, 16 threads,
Docker 29.1.3) and what came of it:

- **Canal's mysql-es failed every round on Linux**: its client adapter refused the whole `es7`
  adapter ("unacceptable code point", SnakeYAML), so it wrote nothing. The harness mounted the
  `configs/canal/es7` folder, and the adapter loads every file there: the copy to the host, made with
  macOS `tar`, carried an AppleDouble `._items.yml` beside `items.yml`. Fixed: the image (now
  `fit-syncbench/canal-adapter:1.1.8-2`) drops the release's example mappings and run.sh mounts
  `items.yml` alone; the README's copy is `COPYFILE_DISABLE=1 tar`. Checked again on Docker Desktop.
- **fit's batch copy (S5) cost 0.84 cores for 139.8k rows/s against DataX's 0.47 for 173k** (fit at
  46 MiB, DataX 432). Its CPU profile (`s5-mysql-batch/profiles-r*`): `encoding/json.Marshal` 14.5%
  (a snapshot position marshalled by reflection for every record, 7.5%, and `appendString`, the
  canonical JSON's string writer, going through `json.Marshal` for every string, 4%); the mysql
  sink's `InsertRows` preparing each batch's statement 12.9%, of which parsing the server's 4,000
  parameter definitions (1,000 rows of four columns), which nothing reads, was 11%; GC about 24%,
  much of it from those. Fixed, with tests and benchmarks: the wire client's `Prepare` reads the
  parameter definitions without parsing them (`skipColumns`; 2.18 MB to 0.42 MB allocated a
  4,000-parameter prepare, the rest the fake server's); the snapshot position is written by hand
  (`appendSnapPos`, 239 ns and 6 allocations to 31 ns and 1, byte for byte what `json.Marshal`
  wrote); `appendString` writes plain ASCII as it is (8 ns, no allocation) and falls back to
  `json.Marshal` otherwise, its output unchanged (`fitsync.AppendJSONString` exports it). Not
  done, for review: caching InsertRows' prepared statements across batches on a connection, which
  would save the prepare's round trip and the server's work too, but changes the client's promise
  that it leaves no statement open.
- **Seen, not fixed**: in S3 (MySQL to Kafka) the Debezium codec's envelope is built as maps and
  marshalled by `encoding/json`, 35% of fit's CPU (`kafka.(*Debezium).Encode`); a hand-written
  encoder there is the next cost worth taking, for the kafka connector's owner.
- To re-run on the host: `s2-mysql-es` (Canal) and `s5-mysql-batch` (fit's fixes); the other items
  stand. The lag columns are filled for every stream and files item, and the batch has none, as
  designed.

### I194 Sync's documentation and examples

- **Do**: `docs/sync.md` and its Chinese twin: concepts, every connector's settings, the guarantees
  and what each source needs on its server (binlog settings, a replication user's grants, a
  publication, a replica set), the type mapping between systems, plugins, operations; examples in
  fit-examples: files to Elasticsearch, MySQL to PostgreSQL, PostgreSQL to Kafka, a WASM transform.
- **Done when**: links checked; each example runs in fit-examples' CI.
- **Touches**: `docs`, fit-examples.

**Decisions pending review** (made while building it, 2026-11-09; each stands until the user rules on it):

1. **One guide, `docs/sync.md` and its Chinese twin**, linked from the README and the configuration
   page's `sync` row: concepts, every connector with a .conf config that loads, what each source needs
   on its server, the type mapping, transforms and codecs, plugins, jobs, the Admin API, `fit sync`
   and the workbench, metrics, dead letters, operations and interop. Each field's description stays in
   the generated reference, which the guide links to rather than repeats. Alternative: a page per
   connector.
2. **Four examples in fit-examples** (`sync-files-to-elasticsearch`, `sync-mysql-to-postgres` with
   `checkpoint: "sink"`, `sync-postgres-to-kafka`, `sync-wasm-transform`), on a branch
   `sync-examples` there, not pushed. "Runs in fit-examples' CI" is taken as `fit validate` in its
   test, as every example there; only the WASM one runs without servers (file to stdout, checked by
   hand with `fit sync run --once`). Running the others against servers is I192's matrix's, or a
   Compose example later. Alternative: a Compose file per example, run in fit-examples' CI.
3. **fit-examples' test sets the examples' password variables** (`ES_PASSWORD`, `KAFKA_PASSWORD`,
   `MYSQL_PASSWORD`, `PG_PASSWORD`) to stand-ins, since `fit validate` reads `password_env` as `fit
   run` does, and **builds the WASM plugin from its Go source** (`GOOS=wasip1`) before validating; the
   4.5 MB `.wasm` is not committed (`.gitignore`). Alternative: a committed `.wasm`, or TinyGo's
   smaller one (I165b).
4. **`sync-mysql-to-postgres` is validated** like the others since I174 merged (it left
   `noValidate`); fit-examples' go.mod moves to a pushed fit with I174, I179 and I185, and the
   sync-examples branch is pushed with that bump.
5. **The MySQL source's parts were written from I174's and I175's specs** before they merged, and
   I192's matrix is not described yet: both are marked in the guide with `MERGE CHECK` comments
   (hidden on GitHub) for the merge to check against the merged code. Alternative: leave them out
   until merged.
6. **The guide's configs are loaded by `TestConfigExamples`**, which now takes `sync*.md` too (since
   I174 merged, every block loads; each was also checked with `fit validate` while writing). The
   record's canonical JSON example is fenced `json record`, so it is not taken for a config. The
   MySQL source's parts were checked against the merged I174/I175 source and its schema chapter.
7. **Size**: about 1,000 lines of guide in English and 890 in Chinese; four examples and 30 lines of test in
   fit-examples. Nothing of the plan is left out, but decision 2's runs against servers.

### Decisions pending review (2026-11-08)

Made while planning; each stands until the user rules on it, and I163 writes them into decision 036.

1. **Protocols written in fit, no drivers.** MySQL, PostgreSQL, MongoDB, Kafka, Redis and SigV4 are
   implemented in `sync/*/wire`, as fib does its protocols, each tested against real servers.
   Alternative: go-mysql, pgx, the MongoDB driver and franz-go behind fit's interfaces — far less work
   now, against design principle 6 and with hundreds of transitive packages.
2. **At least once, idempotent sinks; exactly once only where the sink has transactions (I191).**
   Alternative: a log of written positions in every sink, which most sinks cannot hold atomically.
3. **The checkpoint in a local file per pipeline**, not in `admin.store` or a sink, by default. A
   pipeline therefore runs on one fit; moving it means moving its file. Alternative: the store, so it
   travels with an exported store.
4. **One checkpoint per pipeline, at the slowest sink.** A slow sink holds back the others' position
   (not their writes, which the queue bounds). Alternative: one pipeline per sink, each with its own
   connection to the source.
5. **MySQL snapshots without locks, converging through replays** (I175), rather than the watermark
   algorithm of DBLog and Flink CDC; PostgreSQL's exported snapshot makes its hand-over exact anyway.
6. **ClickHouse and Elasticsearch as sources by cursor polling**, deletes not seen; they have no change
   stream to follow.
7. **TiDB's changes through TiCDC via Kafka**, not TiDB's old binlog (Pump/Drainer, deprecated).
8. **The WASM ABI passes records as JSON first**, a binary form only if I165's benchmark shows JSON
   costs more than 20%; connector plugins get HTTP and timers, not raw sockets.
9. **DDL is reported, not replayed into SQL sinks**; schemas in sinks are created by people (a
   `fit sync schema` command printing suggested DDL can come later). Elasticsearch, MongoDB and Kafka
   need no schema.
10. **Order:** core, transforms, WASM and files first (I163–I167), then the HTTP-based systems
    (I168–I171, cheap with fib's client), then the databases (I172–I183), then Kafka, Redis and S3,
    then operations. Agreed, or databases first?

## BitTorrent: downloading and seeding, a tracker, publishing

Planned 2026-11-08, [decision 037](decisions/037-bittorrent.md). fit gains BitTorrent on fib's
`bittorrent` packages: a client role that downloads and seeds, a handler that plays a torrent's file
while it downloads, a tracker role, and publishing a directory as torrents. fib's client first needs
A75–A79 (I196): its disk I/O and hashing run on a fib worker today. The first phase is I196–I205;
the second phase (I206–I208) is the cluster, a tracker store the cluster shares, and RSS. I198–I202
can run in parallel once I197 is done, one worktree agent each.

Principles, in the order they bind:

- **Nothing blocks a worker** (rule 1): storage, hashing, `Reader` reads, building torrents and the
  state file all run on bounded pools; a full pool holds the peer's reads.
- **Bounded everything** (rule 11): torrents, connections (all, a torrent's, half-open), metadata
  and .torrent sizes, URL downloads, a `watch_dir` scan, the event queue, `bt_stream` streams and
  torrents added on the fly, and the disk a download may take.
- **State in `data_dir`, restarts without hashing again** (A76); a reload changes limits in place
  (A77) and makes a new client only when `listen` or the DHT and uTP switches change.
- **One port on fit's shared engine** (A79), under the config's `access` (A78) and `limits`.

### I196 fib A75–A79 for the BitTorrent client

- **Why**: fib's client writes blocks, reads upload data and hashes pieces on the fib worker that
  read the peer's message; it re-hashes everything at every start; its limits are fixed at
  `NewClient`; it cannot refuse peers by address; it always makes an engine of its own.
- **Do**: in fib, A75 (storage and hashing on a bounded pool, `HoldReads` while it is full), A76
  (`Torrent.ResumeData`, `AddOptions.Resume`), A77 (`Client.SetLimits`, `Torrent.SetLimits`), A78
  (`Config.Filter`, `Client.Refilter`), A79 (the client on the caller's engine); docs and decisions
  pending review in fib's `docs/bittorrent*.md`; fit moves to the commit.
- **Done when**: a test shows a slow storage never stalls another connection of the engine; a
  restart with resume data completes without reading for a check; limits change without dropping
  peers; a refused address cannot connect over TCP or uTP and is never dialed; two clients share an
  engine and closing one leaves the other; fib's CI, race and Linux runs pass.
- **Touches**: fib `bittorrent`, `bittorrent/peer`, `bittorrent/utp`, `bittorrent/dht`, maybe
  `core`; fit `go.mod`, `docs/fib-api-requests*.md`.
- **Done** 2026-11-08 at fib `1f1d2e7` (A75–A79 as built in
  [fib-api-requests](fib-api-requests.md#a75-the-bittorrent-client-never-touches-storage-or-hashes-pieces-on-a-worker));
  fit is on it. Four worktree agents, one per request (A78 and A79 together), merged by the main
  session; merging A75 with A76 needed the resume plan and `ResumeData` to hold the storage through
  A75's guard, and `TestResumeRejected` raced on Linux (fixed in the test). Verified: `go test
  -race ./...` of fib on macOS, `./bittorrent/...` and `./core` on Linux (golang:1.27.3), vet for
  FreeBSD and Windows.

**Decisions pending review** (made while building it, 2026-11-08; the details, with alternatives, are in
fib's [docs/bittorrent.md](https://github.com/lesismal/fib/blob/main/docs/bittorrent.md#decisions-pending-review)):

1. **A75**: one disk pool and one FIFO queue per client; `DiskWorkers` min(GOMAXPROCS, 8); only
   received blocks count against the 64 MiB bound, which is soft (a block is never refused on a
   worker; held connections resume at half); a write that finds its storage closed is dropped;
   closing waits only for I/O under way; the initial check, web seed writes, `Reader` reads and the
   `StorageOpener` stay off the pool (the opener runs on a worker when a magnet's info arrives).
2. **A76**: the caller stores the blob; size and mtime are trusted; pieces not in the blob and
   unfinished pieces are downloaded again; unusable data is an `EventError` wrapping
   `ErrResumeData`; `ResumeGen` is the "save now" signal; `ResumeData` errs during a check.
3. **A77**: a separate `Limits` type; zero keeps `Config`'s meaning; in `TorrentLimits` zero
   inherits and negative is no limit; download tokens are spent at request time and not refunded on
   cancel (about 1.5x overshoot for a couple of seconds); peers are closed least useful first.
4. **A78**: one `netip.AddrPort` predicate; the UDP binding is not filtered (UDP trackers share
   it), uTP and the DHT filter instead; `Refilter` is separate from setting a policy, so one fit
   change can refilter many clients.
5. **A79**: `Config.SharedEngine` rather than a second constructor; the engine must be running.

Open from it: fib's docs say the client computes v2 piece layers after a hybrid magnet download,
but `Torrent.computeLayers` is never called; a hybrid torrent from a magnet link cannot answer v2
hash requests as a seed until it is wired in and tested.

### I197 The `bittorrent.client` role

- **Why**: the core every other item builds on.
- **Do**: package `bittorrent` (fit's), the role `bittorrent.client` in the registry; config
  (`listen`, `data_dir`, `download_dir`, `encryption`, `dht`, `pex`, `utp`, `lsd`, `limits`,
  `access`, `console`) with the defaults of decision 037; the client on the shared engine; the state
  file (torrents, categories, priorities, .torrent files, resume data; temporary file, fsync,
  rename, at most every few seconds and at stop); restore at start; reload in place; schema,
  reference, `schema-zh.js`.
- **Done when**: a torrent added survives a restart and resumes without a check; a reload of limits
  keeps peers; a `listen` change keeps torrents; slow-peer, cut-connection and disk-error tests;
  docs in both languages.
- **Touches**: `bittorrent`, `app`, `config`, `schema`, `docs`.
- **Done** 2026-11-09: package `bittorrent` (`role.go`, `state.go`, `api.go`), the role registered in
  `app/standard`; config `bittorrent.client` (`config/bittorrent.go`) with its checks, its port in
  the address clash check (TCP and UDP), the `bittorrent` logger; schema, reference, templates,
  `schema-zh.js`; [BitTorrent](bittorrent.md) in both languages. The role consoles' code moved from
  `tunnel/internal/common` to `app/roleconsole`, which the tunnel's roles and this one share. Tests:
  the role through the app Manager, a download between two roles on loopback, a restart that has
  every piece again with no read of the data (storage reads counted), a magnet link's .torrent saved
  and restored from, a reload of limits keeping the peer and the client, a `listen` change keeping
  the torrents (and a taken port bringing the old client back), a `data_dir` change, a cut seeder
  replaced by another mid-download over a rate-limited (slow) link, access closing a connected peer,
  a storage write error, a state directory that cannot be written, bad and oversized state files,
  broken torrents kept listed, the API's refusals (bounds, bad input, a name leading out of
  `download_dir`), the console's routes.

**Decisions pending review** (made while building it, 2026-11-09; in force until the user rules):

1. **The state is three kinds of files, not one**: `torrents.json` (the list), `torrents/<id>.torrent`
   and `resume/<id>.resume`, each written to a temporary file, synced and renamed. Why: .torrent
   files reach MiBs (v2 piece layers, many files) and resume data grows with pieces and files, so one
   file would rewrite every torrent's bytes at each save; per-torrent files rewrite only what
   changed. Alternatives: one bencoded or JSON file with everything; an embedded database.
2. **A state file that does not read keeps the role from starting** (`ErrState`, the file in the
   error; the rest of the config runs): JSON that does not parse, a later version, more than
   100000 torrents, an ID twice or malformed, a relative directory, or past 64 MiB. Why: starting
   empty would overwrite the list at the next save and lose every torrent. Alternative: rename the
   bad file aside (`torrents.json.bad-<time>`) and start empty.
3. **A torrent whose own files do not read stays listed** with its error (state `error`), saved,
   until removed, while the others run; resume data that does not read is dropped and that torrent
   checked in full. Alternative: drop such torrents with a log line.
4. **`listen` and `data_dir` are required; `download_dir` defaults to `<data_dir>/downloads`.** Why:
   a BitTorrent port is opened only where the config says; fib's `:6881` default is not taken
   implicitly. Alternatives: `listen` defaulting to `:6881`; `download_dir` required.
5. **`disk_queue_bytes`, not `disk_queue`**, after fit's `*_bytes` names; **`save_interval`**
   (default 5s) is exposed. Changes to the list (add, remove, pause, resume, priorities,
   sequential) are saved at once, in the API call; resume data by the saver when `ResumeGen` moved.
6. **What makes a new client**: `listen`, `encryption`, `dht`, `pex`, `utp`, `lsd`, `disk_workers`,
   `disk_queue_bytes` (all read only by fib's `NewClient`), and `data_dir`. **A new `data_dir`
   starts from that directory's state**, as a restart would, and the old one keeps what was saved.
   Alternatives: carry the torrents over into the new directory; refuse the change.
7. **A new client that cannot be made** (its port taken) **brings the old one back** on the old
   config and the same torrents, and the reload reports the error; between the two the client is
   down briefly. Alternative: probe the new port before closing the old client (the same port
   cannot be probed while the old one holds it).
8. **A torrent's ID** is its v1 info hash in hex, or its v2 hash truncated to 20 bytes, what fib's
   `Client.Torrent` looks up.
9. **A torrent paused by its seed limit is saved as paused**, so that a restart, where fib's upload
   counters start again from zero, does not seed it again. Seed ratio and time count from when it
   was added or fit started (fib's counters are not persisted). Alternative: keep upload and
   download totals in the state (for I203's statistics).
10. **Storage errors** (fib's `EventError`, which leaves the torrent running) are kept in the
    torrent's status until a piece passes its check or it changes state, and logged once each;
    `ErrResumeData` is only logged.
11. **`max_torrents`** defaults to 1000 (at most 100000); lowering it removes none and refuses
    adds; a state listing more than it is restored whole.
12. **The role console's code moved to `app/roleconsole`**, shared with the tunnel's roles, rather
    than copied; the console serves a read-only `GET /api/torrents` and `/api/torrents/<id>` until
    I203/I204.
13. **The role's `access` (A78) is in I197**, as the brief asked: the gate is set on every Apply and
    `Client.Refilter` runs after it; I199 keeps the `.p2p`/`.dat` list formats.
14. **Not configurable**: the peer ID prefix (fib's `-FB0001-`), trackers on or off, dials per
    second, the DHT routers (public ones), preallocation.

Open from it: fib's client queues events for `OnEvent` in a slice with no bound; fit's handler only
takes a short lock, so the queue drains, but a bound belongs in fib (a fib request when it matters).

### I198 Adding torrents, categories and queueing

- **Do**: magnet links, .torrent uploads, .torrent URLs (fit's HTTP client, size bound), a
  `watch_dir` (.torrent and .magnet files, moved aside once added); categories with a directory,
  rates, seed limits and an optional `media_library` rescanned on completion; `max_active_downloads`
  and `max_active_seeds` with a queue; free space checked before and while downloading (medialib's
  diskfree), pausing with an error when short; paths kept inside the category's directory.
- **Done when**: each way of adding is tested, including bad and oversized input; a completed
  download into a library's root appears in its catalog; the queue starts the next torrent when one
  finishes.
- **Touches**: `bittorrent`, `medialib`.
- **Done** 2026-11-09, with I199: `bittorrent/manage.go` (the manager goroutine: queue, disk checks,
  alternative rates, each torrent's limits, completions), `fetch.go` (`AddURL` on fib's asynchronous
  HTTP client), `watch.go` (`watch_dir`); config `categories`, `watch_dir`, `watch_interval`,
  `watch_max_files`, `url_timeout`, `limits.max_active_downloads`, `max_active_seeds`,
  `min_free_bytes`, with their checks; `medialib.DiskFree`; the state keeps each torrent's category,
  own limits and completion, and the list's order is the queue's. API: `Add` with `Category` and
  `Limits`, `AddURL`, `SetCategory`, `SetTorrentLimits`, `SetQueuePosition`, `AltSpeed`,
  `SetAltSpeed`; a status gives the category, own and effective limits, queue position, completion
  and why the role holds the torrent. Tests: categories (directories, limits merged, a reload of a
  category's limits in place, kept across a restart, unknown and escaping ones refused), the
  download queue (the next starting as one finishes, a queue position moving one ahead, the order
  across a restart), the seed queue (pause and remove free a slot), the disk (not started short of
  space, started once it is there, paused when it runs out mid-download over a slow peer, seeds not
  held), a download into a library's root appearing in its catalog with one scan and none after a
  restart, `watch_dir` (.torrent, .magnet, a category's subdirectory, bad, empty, hidden, other
  files, one already held), its bounds (files a look, a file still being written, max_torrents left
  for later, one too large, a missing directory), URLs (redirects, to a magnet link, a bad scheme, a
  bad answer, a redirect loop, a server that never answers, a connection cut mid-body, a body past
  the bound, past MaxURLFetches at once).

**Decisions pending review** (made while building it, 2026-11-09; in force until the user rules):

1. **URL downloads**: the body is bounded by 32 MiB, not fib's 128 MiB metainfo bound, and at most
   4 run at once, one more refused (`ErrBusy`) rather than queued, so that at most 128 MiB are held;
   5 redirects, one to a magnet link included; `url_timeout` (30s) bounds the whole download. Why: a
   .torrent body is held whole until added, and real ones are far below 32 MiB. Alternatives: fib's
   bound; a bounded queue of URL adds.
2. **`AddURL` waits for the download** on the caller's goroutine (never a fib worker; fib's callback
   only hands the answer over) and returns the torrent's status. Alternative: return at once with a
   job the Admin API reports on (I203).
3. **No internal-address guard on URLs**: the API is the operator's (Admin API, CLI, pages behind
   their auth). Alternative: refuse loopback, link-local and private addresses as an upstream's
   `deny_internal` does.
4. **`watch_dir`**: a category's files are those in a subdirectory of its name, the others have
   none; a file is taken once its size and modification time held across two looks (rather than by
   its age, which a fake clock or a slow copy would fool); handled files move into `.done` or
   `.failed` beside them (a `<name>.error` file says why), a torrent held already counts as done,
   and errors that may pass (`max_torrents`, the state not written, the client down) leave the file
   to be tried at each look. Defaults: every 10s, at most 100 files a look (at most 10000), reading
   at most 16 directory entries for each. Alternatives: delete handled files; one `.done` for all;
   options for a watch directory's category or a paused add.
5. **A category's limits are each torrent's** (fib's `TorrentLimits`), not one budget the
   category's torrents share: fib has buckets for the client and for each torrent only. A torrent's
   own limits take the place of the category's, field by field. Alternative: a shared bucket per
   category (a fib request, when wanted).
6. **A category's relative `dir` stays below `download_dir`**; an absolute one is taken as it is
   (the config is the operator's). **`SetCategory` does not move files**: fib has no way to move a
   running torrent's storage; the torrent takes the new category's limits and library only.
   A category removed from the config stays recorded on its torrents, which lose its limits, and
   regain them if it comes back. Alternatives: move the files (stop, rename, add again with resume
   data, or a fib `Torrent.Move`); refuse a category change.
7. **No queue by default** (`max_active_downloads` and `max_active_seeds` 0), not qBittorrent's
   3 and 5: a server's torrents are bounded already by `max_torrents` and the connection limits.
   The queue goes by the list's order, which `SetQueuePosition` changes and the state file keeps; a
   torrent moved ahead of a running one pauses it; stalled torrents count as active; a magnet
   fetching its metadata, and a torrent checking its data, count as downloading; which torrents are
   held is not saved but worked out again at start. Alternatives: qBittorrent's defaults; leave
   slow torrents out of the count; save the held state.
8. **Every torrent is added paused and started by the role** (restored, added, resumed), in the
   same hold of the role's lock, so that the queue and the disk check are one place. The manager
   looks every second (not configurable).
9. **Disk checks**: `min_free_bytes` defaults to 1 GiB, as a media library's uploads; a held
   download starts when the disk has what it still has to download plus `min_free_bytes` (torrents
   started in the same look reserving theirs, per directory, not per device), a running one pauses
   below `min_free_bytes`, and starts again by itself once the space is there; seeds are never
   held; what running downloads will still write is not reserved. Alternatives: no default (off);
   reserve for running downloads too; never restart by itself.
10. **A completion is counted once**: polled every second (the torrent's `Done`), saved in the state
    (`completed`), counted again only after `SetFilePriority`; then the category's library, looked
    up by name on the role's engine, is asked for a scan (`Library.Rescan`, which coalesces). A
    library not running is logged. Alternative: fib's `EventCompleted`, which a restart's check
    would send again.
11. **medialib gains `DiskFree`**, a thin export of its diskfree helpers; the rescan API existed.

Open from it: fib has no per-category (group) rate bucket and no way to move a torrent's storage; the
role's new API is not on its console's HTTP routes yet (I203).

**Decided when merging I198 with I200 (pending review):** a torrent `bt_stream` added on the fly is
not queued and takes no slot of `max_active_downloads` or `max_active_seeds`, since a viewer waits
for it and `max_temporary` and `max_temporary_bytes` bound such torrents; the disk check holds it
as any other (`TestTemporaryNotQueued`). Alternative: queue it like the rest, which leaves the
viewer waiting until `read_timeout`.

### I199 Schedules, per-torrent limits and blocklists

- **Do**: `alt_speed` with weekly schedules; per-category and per-torrent limits (A77); `access` on
  the role through A78 with `Client.Refilter` after a policy change; `access`'s list files read
  eMule `.p2p` and `.dat` as well, told apart by content, for every module.
- **Done when**: a schedule switching rates is tested with a fake clock; a deny list closes a
  connected peer; `.p2p` and `.dat` files parse (ranges, comments, levels, bad lines) in `access`'s
  tests.
- **Touches**: `bittorrent`, `access`, `config`, `docs`.
- **Done** 2026-11-09, with I198: `access` reads list files line by line, each line an address or
  prefix, a range, an eMule `ipfilter.dat` line or a PeerGuardian `.p2p` line, for every module
  (`access/lists.go`; its sets are built from ranges); `alt_speed` (rates, weekly `schedules`,
  `time_zone`) applied by the manager on the role's clock, with the API's `on`/`off`/`auto` kept in
  the state; per-category and per-torrent limits through fib's `TorrentLimits`, in the API and the
  state; the role's access already refilters at every Apply (I197), and a list file's change
  applies the config again. Tests: `access` (ranges, `.dat` levels and leading zeros, `.p2p` names
  with colons, commas, dashes and `#`, comments, bad lines naming the file and line, a line too
  long, mixed files through a policy, an allow list of ranges, ranges against a linear check);
  schedules (overnight ranges, weekends to 24:00, zones) and the role switching rates on a fake
  clock, the API's choice across a restart, a reload without `alt_speed`; a `.p2p` deny file
  closing a connected peer, and a `.dat` line above level 127 not denying.

**Decisions pending review** (made while building it, 2026-11-09; in force until the user rules):

1. **An `ipfilter.dat` line of a level above 127 is not listed**, wherever the file is used (so a
   deny file blocks levels 0 to 127): qBittorrent's reading. eMule at its default filter level 127
   blocks levels below 127 only, so a line of level exactly 127 is blocked here and not by eMule.
   Alternatives: eMule's `< 127`; a configurable level.
2. **Formats are told apart line by line**, not by file name or a header: an address or prefix
   first, then a bare range (`first-last`, also accepted on its own), then `.dat`
   (`first - last , level , name`), then `.p2p` (the range after the last `:`). A trailing `#` or
   `;` comment is cut only from the plain format, since `.p2p` names may hold `#`; lines starting
   with `#`, `;` or `//` are comments. IPv4 addresses in ranges may have leading zeros; IPv6 ranges
   are taken too.
3. **A bad line still fails the whole file**, naming the file and the line, as before (the config
   is not applied, the old one stays), rather than being skipped as eMule and qBittorrent do. Why:
   a list that silently lists less is worse for an allow list. Alternative: skip and count bad
   lines in a log line.
4. **`access.ReadFile` returns ranges** (`[]access.Range`) instead of prefixes, and a line may be
   16 KiB (was 4 KiB); ranges are not split into prefixes, so a published list costs one range a
   line.
5. **Alternative rates**: 0 is unlimited (qBittorrent's); only the two rates switch, not the
   connection limits; a schedule's `days` are the days it begins on, one whose `to` is earlier than
   its `from` runs into the next day, `to` may be `24:00` (and `00:00` means midnight at the end);
   `time_zone` is local by default, as fit's logs; the API's `on`/`off` override the schedules
   until `auto`, and is saved in `torrents.json` (`alt_speed`); without `alt_speed` in the config
   the rates never switch. Alternatives: a separate alternative connection limit; not saving the
   override.
6. **Per-torrent limits are fit's `BitTorrentTorrentLimits`**, fib's `TorrentLimits` meaning (zero
   takes the client's, negative is no limit for the torrent), the state keeping only the torrent's
   own, merged over its category's when applied.

### I200 `bt_stream`: playing a file while it downloads

- **Do**: a route handler serving a torrent's file by info hash and file index or path, with ranges;
  the ranges asked for are read through fib's `Reader` on a bounded pool, so they download first;
  `max_streams`, read timeouts, readahead bound; `media_auth`; adding by magnet link on the fly,
  bounded in number and disk, expiring after their last stream.
- **Done when**: a browser-style range request of a file still downloading is answered once its
  pieces arrive; slow readers, cut connections and a full stream bound are tested.
- **Touches**: `bittorrent`, `handler`, `mediaauth`.
- **Done** 2026-11-09: the route handler `bt_stream` (`handler/btstream`, in `modules/standard`):
  `<prefix><info hash>/<file path>`, `/<n>` (file index) and `/` (the largest file), `GET` and
  `HEAD`, a single `Range` (206, 416), `If-None-Match`/`If-Range` against an `ETag` of the torrent
  and the file, `Content-Type` by extension, `Accept-Ranges`; read through fib's `Reader` on a
  goroutine of the stream's own, at most `max_streams`, written with `WriteNoWait` and resumed by
  `OnWritable`; `read_timeout`, `readahead_bytes`, `info_timeout`; `auth` (play on
  `bt/<info hash>`); with `magnet`, adding by the `magnet` query parameter (publish needed),
  temporary torrents bounded by `max_temporary` and `max_temporary_bytes`, downloading only the
  files streamed, removed with their data `temporary_ttl` after their last stream and at the
  role's start. Role hooks: `AddTemporary`, `SetTemporary`, `RemoveTemporary`, `TemporaryBytes`,
  `SetFilePriorities`, `Status.Temporary`, `temporary` in `torrents.json`. Schema, reference,
  templates, `schema-zh.js`, [BitTorrent](bittorrent.md#playing-a-file-while-it-downloads) in both
  languages; `test/workers` names the handler's own `TestNoWorkerBlocks`. Tests (two roles on
  loopback, DHT and LSD off, no trackers): a range of a file with no piece yet, answered once a
  seeder is added; whole files by path, index and as the largest; suffix and open ranges, 416,
  HEAD, 304, `If-Range`, several ranges; a 24 MiB file read by a client that stops (what is handed
  to the connection settles far below the file, 768 KiB in a run on macOS, then the file arrives
  whole); connections cut while
  waiting for a piece and while streaming (the stream and its hold on the torrent end);
  `max_streams` full; `read_timeout`; the role not running; the temporary lifecycle (401/403 for
  adding without publish, a magnet link of another torrent, priorities, `max_temporary`, removal
  with the data after the linger, added again), kept while another stream reads it,
  `max_temporary_bytes`; play auth by header and query; `Drain`; ServeHTTP returning while its
  stream waits; `parseRange`; the params' checks; the role's hooks, and temporary torrents removed
  at the next start.

**Decisions pending review** (made while building it, 2026-11-09; in force until the user rules):

1. **URLs**: `<prefix><info hash>/<file path>` (the path as fib's `File.Path` gives it, the
   torrent's name first), `<prefix><info hash>/<n>` for the file of index `n`, a path winning over
   an index when a file is named with digits alone, and `<prefix><info hash>/` for the largest
   file. Why: a player sees the file's name and extension, the index serves scripts, the largest
   file is what a magnet link usually means. Alternatives: the index in a query (`?file=n`) or
   under its own segment (`/i/<n>`); no largest-file form.
2. **Adding on the fly is the `magnet` query parameter of a file's URL**, which must be a link of
   the path's torrent (400 otherwise); the request then needs `publish` on `bt/<info hash>`
   besides `play`, and `magnet` needs `auth` (a config without it is refused). Why: the URL a
   player keeps works for every range whether or not the torrent is there yet; downloading from
   the internet is a stronger right than playing. Alternatives: a path form
   (`<prefix>magnet/<link>`); a separate add request, then plain URLs; play being enough; `magnet`
   allowed without `auth`.
3. **The bounded pool is a goroutine per stream, at most `max_streams`**, with no queue: past it
   503. A goroutine waits for pieces (bounded by `read_timeout`) but never for the client: when
   `WriteNoWait` reports the watermark passed it ends, and `OnWritable` starts another. Why: a
   read can only block, and one at a time per stream is all a stream needs; a shared pool with a
   queue would let one torrent's missing pieces delay other streams. Alternatives: a shared
   `filepool` of fixed workers and a queue; a goroutine that also waits for the client.
4. **`max_streams` counts a handler's own streams**, a route's, not shared across a reload: the
   old config's streams end at its drain. Alternative: a count shared by engine and prefix.
5. **A response begins with its first chunk**: a first read past `read_timeout` is a 504, a later
   one cuts the response short of its `Content-Length`. Alternative: send the header at once, as
   soon as the range is known.
6. **Temporary torrents download only the files streamed** (the others priority 0, raised as
   another is asked for); `max_temporary_bytes` counts what they want (fib's `Stats.Wanted`), and
   past it the answer is **507** and a torrent just added is removed. Two torrents added at once
   may both be refused near the bound. Alternatives: download whole torrents; count their total
   length; check the disk's free space (I198's diskfree, when it lands).
7. **Temporary torrents are marked in the role's state** (`temporary` in `torrents.json`) and
   removed with their data when the role reads a state (start, a new `data_dir`). Why: otherwise a
   restart would turn them into ordinary torrents that are never removed. Alternatives: not
   persisted; kept across a restart with a new linger.
8. **The linger is shared** by every `bt_stream` handler and across reloads, by role and torrent;
   the `temporary_ttl` is that of the handler whose stream ended last; `RemoveTemporary` removes a
   torrent only while it is still temporary, so one made ordinary (`SetTemporary(id, false)`)
   stays; a stream that comes while its torrent is being removed waits, then adds it again or gets
   404.
9. **Several ranges are answered with the whole file** (200), not `multipart/byteranges`: players
   ask for one range at a time. The `ETag` is strong, `"<info hash>-<index>"`; no
   `Last-Modified`.
10. **Defaults**: `max_streams` 32 (at most 4096), `read_timeout` 60s, `readahead_bytes` 8 MiB
    (64 KiB to 256 MiB), `info_timeout` 60s, `max_temporary` 4 (at most 1000),
    `max_temporary_bytes` 16 GiB, `temporary_ttl` 10m (at most 7 days); a chunk is 64 KiB, fib's
    watermark.
11. **Answers**: the role not running 503 (`Retry-After: 5`); `max_temporary` or `max_torrents`
    reached 503 (`Retry-After: 30`); `magnet` off 403; a torrent the role has not got, without a
    magnet link, 404; the torrent removed under a stream not begun 503.
12. **`Content-Type` from a table of the handler's own** (video, audio, subtitles, pictures),
    not Go's `mime`, which reads the system's files on first use.

Open from it: adding a torrent already held as temporary through the API answers `ErrExists`;
making it an ordinary torrent (`SetTemporary(id, false)`) belongs to I198/I203's add paths. fib's
`Reader.Close` waits for a `Read` under way (both take its lock), so the handler cancels the read's
context first; an interrupting `Close` would be a small fib request if another user needs it.
Not run: the Linux race run, and the full suite (left to the main session).

### I201 The `bittorrent.tracker` role

- **Do**: `bt_tracker` (HTTP announce and scrape) and the role's UDP `listen` on fib's
  `tracker.Server`; public or private (passkeys from `media_auth`), an allow-list of info hashes
  (inline or files, watched); statistics.
- **Done when**: libtorrent and anacrolix announce to it over HTTP and UDP; a private tracker refuses
  unknown passkeys and info hashes; bounds tested.
- **Touches**: `bittorrent`, `handler`, `mediaauth`.
- **Done** 2026-11-09: package `bittorrent/tracker` (`tracker.go`, `hooks.go`, `handler.go`,
  `api.go`), the role `bittorrent.tracker` and the handler `bt_tracker` registered by
  `app/standard` and `modules/standard`; config `bittorrent.tracker` (`config/bittorrent_tracker.go`:
  `listen`, `interval`, `min_interval`, `peer_ttl`, `max_torrents`, `max_peers_per_torrent`,
  `max_peers`, `max_numwant`, `max_scrape`, `auth`, `allow`, `allow_files`, `access`, `console`)
  with its checks, its UDP port in the address clash check, its files watched (`app.Info.CertFiles`);
  users' passkeys and `AllowPasskey` in `mediaauth` (`passkey.go`); schema, reference, templates,
  `schema-zh.js`; a tracker section in [BitTorrent](bittorrent.md) in both languages. Tests: the
  role through the app Manager; announces and scrapes over HTTP (through the `bt_tracker` module)
  and UDP with fib's tracker client, the role's statistics; two fib clients downloading a private
  torrent that find each other only through the tracker (the seeder over HTTP with a token, the
  leecher over UDP with a user's passkey); passkeys of users and tokens in the path and the query,
  over HTTP and UDP, refused when missing, unknown or without the grant, scrapes too, and a policy not
  loaded failing closed; the allow-list inline and in a file, refused hashes, the file changed and
  applied again, a bad line and a missing file refusing the reload with the list before kept; the
  store's bounds and `max_numwant`; access refusing UDP before a connection and HTTP with a failure,
  lifted by a reload; reloads in place (the interval keeping the swarms, a taken port leaving all as
  it was, `listen` moved and removed, new bounds starting a new store); a client sending its
  announce a byte at a time, clients cut midway, garbage datagrams; the console's routes; the
  passkey redaction; `mediaauth`'s passkeys (keyrings, other secrets, public play); the config's
  checks and clashes. libtorrent and anacrolix interop is I205.

**Decisions pending review** (made while building it, 2026-11-09; in force until the user rules):

1. **A user's passkey is derived, not stored**: the first 32 hex digits of HMAC-SHA256(the policy's
   signing key, "fit bittorrent passkey\0" + name); tokens, static or signed, are passkeys as they
   are; a passkey's grant must play `bittorrent/<info hash in hex>`. Why: `media_auth` already has
   users, tokens, grants by pattern and key rotation, and a password cannot ride in a URL a .torrent
   file keeps for ever; deriving needs no new state and rotating the secret revokes every passkey.
   With a keyring every key's passkey is taken and the default key's handed out. Alternatives: a
   `passkey` field per user in `media_auth`; passkeys kept in a file of the role; long-lived signed
   tokens only.
2. **A private tracker fails closed** while its policy is not loaded ("the tracker's passkeys are not
   available"); a policy whose `public` has `play` lets everyone in, as it does for streams.
3. **The store's default bounds are fit's, below fib's**: `max_torrents` 100000 (fib 1048576),
   `max_peers_per_torrent` 10000 (65536), `max_peers` 500000 (2097152), about 80 MiB at most; the
   maxima are fib's defaults (`max_peers` up to 4194304). Alternative: fib's defaults.
4. **A change of the store's bounds or `peer_ttl` makes a new store**, the swarms starting empty
   (clients announce again within their interval), since fib's `MemoryStore` takes them only when
   made; the interval, `min_interval`, `max_numwant` and `max_scrape` make a new fib `Server` on the
   same store, with the role's one UDP secret, so swarms and UDP connection IDs survive.
   Alternatives: refuse such a change until a restart; wait for fib to set bounds at run time.
   **Changed by I209**: the bounds and `peer_ttl` now change in place (fib's `MemoryStore.SetConfig`,
   A85), keeping the swarms; a change of both them and the interval sets them, then makes a new
   `Server` on the same store.
5. **An allow-list file with a bad line, or one that does not read, refuses the reload**, the list
   before staying in force. Why: a typo would otherwise silently refuse a torrent, or with a missing
   file, every torrent. Alternative: skip bad lines with a warning.
6. **The role's `access` is checked in the `bt_tracker` handler for HTTP** (a failure "refused", BEP 3
   style), and by the UDP binding's `Filter` before a connection is made; HTTP is accepted by fit's
   listeners, whose access is the global one and the listener's. Alternative: the role's access for
   UDP only.
7. **The access log never holds a passkey**: `?passkey=` is always replaced by `REDACTED` in the
   request URI; the path segment before `announce`/`scrape` only when the tracker is private, so that
   a public tracker's `/tracker/announce` stays readable.
8. **Per-torrent statistics are for a torrent named, and listed for the allow-list's torrents**;
   a public tracker cannot list what it tracks, because fib's `MemoryStore` has no way to enumerate
   its torrents. Keeping our own index beside the store would work around fib (rule 15), so it is a
   fib request instead (below). Counters: announces and scrapes served, refusals by reason
   (`unregistered`, counted per refused hash, scrapes included; `passkey`; `access`).
   **Changed by I209**: a public tracker lists the torrents its store has peers of, by info hash,
   through fib's `MemoryStore.Range` (A85).
9. **A user's passkey is read on the role's console**, `GET /api/passkey?user=`, behind its login,
   until I203's Admin API and I204's pages; the console's other routes are `GET /api/stats`,
   `/api/torrents` (`offset`, `limit` ≤ 1000) and `/api/torrents/<hash>`.
10. **A private scrape needs a passkey whose grant plays every torrent it names**; a scrape naming
    none gets nothing, as fib serves it, whoever asks.
11. **`bt_tracker` has no params**: fib's dispatch by the path's last segment, so the route's match
    decides where the tracker lives (`/announce`, `/tracker/*`).
12. **Not configurable**: fib's `TrustIPParam` (off: a peer is recorded at the address its request
    came from), the default numwant (fib's 50), the UDP secret (random per role, kept across reloads,
    not restarts: UDP clients connect again).

Open from it: fib requests to file — `MemoryStore` cannot list its torrents (a `Range(func(InfoHash,
Stats) bool)` would give a public tracker's per-torrent statistics) nor change its bounds while
it runs (a `SetConfig(MemoryStoreConfig)` would keep the swarms across such a reload); fib's client
announces `started` while a seeder checks its existing data, then `completed` when the check ends,
so a seeder counts as a completed download. No metrics of the tracker yet (I203's metrics).

### I202 Publishing a directory as torrents

- **Do**: `publish` blocks: a directory built into hybrid torrents (each file, or the directory
  whole) and seeded; `.torrent` and magnet links through a route; web seeds at fit's static files;
  rebuilt when the disk changes, as static files follow it; hashing on a bounded pool.
- **Done when**: a client downloads a published file from fit's seed alone and from the web seed
  alone; a changed file gets a new torrent and the old one stops.
- **Touches**: `bittorrent`, `handler`.
- **Done** 2026-11-09: `bittorrent.client.publish` entries (`config/bittorrent_publish.go`: `name`,
  `dir`, `mode` `per_file`|`whole_dir`, `web_seed`, `announce`, `private`, `piece_length`, `files`,
  `exclude`, `check_interval`, `max_files`, `max_total_bytes`, `max_hashing`, with their checks);
  the role's publisher (`bittorrent/publish.go`, `publish_walk.go`): a walker goroutine and
  `max_hashing` builder goroutines per entry, hybrid torrents by fib's `metainfo.Builder`, seeded in
  place through read-only storage, kept in `data_dir/publish/<name>/` and restored by file stamp;
  the role's code touched only by six hook lines (`role.go`: the field, `New`, `Apply`,
  `restoreAll`, `closeClient`, `Stop`). The handler `bt_publish` (`handler/btpublish`, in
  `modules/standard`): the JSON index, `<id>.torrent`, `<id>.magnet`, `503` without a client.
  Schema, reference, templates, `schema-zh.js`, the module's chapter, a publishing section in
  [BitTorrent](bittorrent.md) in both languages. Tests: each file of a directory downloaded by a
  fib client from the role's seed alone, a `whole_dir` torrent downloaded (dotfiles and symbolic
  links left out) and replaced when a file is added, a file downloaded from fit's own `static` route
  as the web seed alone with the publishing role stopped, a restart that builds nothing and reads no
  data (storage reads counted), a reload of bounds that builds nothing, an entry removed (torrents
  stopped, state gone, files kept), a changed file getting a new torrent while the old one leaves
  the client and is no longer served, a removed and an added file, `max_files`, `max_total_bytes`,
  `files`/`exclude`, empty files, a `whole_dir` entry past its bounds, a duplicate file, a missing
  directory, a failed build not retried until the file changes, web seed URLs of both modes,
  trackers and the private flag, read-only storage, the routes (index, .torrent, magnet, HEAD,
  404, 405, named entries), and the config's checks.

**Decisions pending review** (made while building it, 2026-11-09; in force until the user rules):

1. **Where the entries are: `bittorrent.client.publish`**, not a top-level `bittorrent.publish`.
   Why: publishing runs on the client and keeps its state in its `data_dir`, so nesting makes the
   dependency structural and the role's member stays one struct (no shared-code change). The
   handler answers `503` while no client runs. Alternatives: `bittorrent.publish` read by the client
   role (its member type changes); a role of its own looking the client up.
2. **Names**: `mode` is `per_file` (default) or `whole_dir`; the include patterns are **`files`**,
   as a media library's, because `include` is a directive of fit's .conf format. Alternatives:
   `each`/`whole`; `match`.
3. **Published torrents are the publisher's, not the role's list**: not in `torrents.json` or
   `List`, not counted by `max_torrents`, seeding for ever whatever the seed limits say
   (`TorrentLimits` −1). Why: their life follows the config and the disk, not the API.
   Alternatives: records in `torrents.json` tagged with their entry, under `max_torrents`.
4. **State and its key**: per entry `data_dir/publish/<name>/` with `index.json` (each torrent's
   files and their fib `filewatch.Stamp`: size, modification and status change times, inode,
   device) and `<id>.torrent`/`<id>.resume`; the index is kept only under the same build key
   (`dir`, `mode`, `web_seed`, `announce`, `private`, `piece_length`). The index is written at the
   walk after a change and when the client closes or the role stops, not after every build (that
   would rewrite it once per file, quadratic for many); a crash in between builds those again.
   Resume data is saved once a torrent has every piece. Alternatives: size and modification time
   only, as fib's resume data (misses a replacement of the same size and time); writing the index
   after every build.
5. **Settling**: a new or changed file is built once its stamp has held for a `check_interval`
   (its status change time is that old, or the walk before saw it the same), so that a file being
   copied is not hashed again and again. Alternatives: build at once; a `settle` option of its own.
6. **Walks, no watch**: the disk is followed by walking `dir` every `check_interval` (10s), not by
   fib's `filewatch` watch, since a walk must stat every file anyway to compare stamps and builds
   wait to settle. Alternative: on Linux, a watch that brings the next walk forward.
7. **Read-only storage**: writes are refused (empty ones, creating an empty file, are a no-op), so
   a piece that fails its check is never downloaded over a published file; such a torrent stays,
   its state showing it, until its file changes. Alternative: remove a torrent whose check fails.
8. **What is left out**: names starting with a dot, symbolic links (never followed: a link could
   publish what lies outside `dir`), names fib's storage would write differently, and in
   `per_file` empty files. Alternative: `follow_symlinks` within `dir`, as a media library's.
9. **Bounds**: `max_files` 1000 (≤100000), `max_total_bytes` 1 TiB, `max_hashing` 1 (≤16) per
   entry, at most 64 entries, a walk at most 10×`max_files`+10000 entries; past a bound `per_file`
   publishes the first files by path, `whole_dir` publishes nothing; every problem reported, at most
   20. No pool shared by the entries. Alternative: one hashing pool for the role.
10. **Walk errors**: a `dir` that cannot be read leaves the torrents as they are; a walk that could
    not read part of `dir` stops no torrent (and `whole_dir` is not rebuilt from it). Alternative:
    stop the torrents of files not seen.
11. **Failures and duplicates**: a build that fails is remembered with its files' stamps and not
    tried again until they change; a torrent of an info hash the client already has (the same file
    twice, two entries, a torrent added by hand) is listed with its error and seeded once.
12. **`bt_publish`**: requests told apart by their last path element (`/` or `index.json`,
    `<id>.torrent`, `<id>.magnet` as text), the index's URLs relative to it, `.torrent` files held
    in memory in the entries' snapshot (bounded by `max_files` × fib's metainfo bound), no lock of
    the role's on a worker (`ClientRunning` is an atomic). Alternatives: a `strip_prefix` param; the
    magnet link as a redirect; .torrent files read from disk on a pool.
13. **Torrents' fields**: trackers each a tier of their own, `created by` `fit`, the creation date
    the build's; the web seed of `whole_dir` is the URL's parent, so its last element must be the
    directory's name (checked in the config).
14. **An entry renamed or removed** drops its state (a renamed one builds again under its new
    name); a change of only its bounds or `check_interval` restarts it from its state, building
    nothing.

Open from it (fib, reported, not worked around):

- **A torrent built is checked again when first added**: fib has no way to add a torrent as
  complete, so a newly built torrent is hashed twice (by the Builder, then by the client's initial
  check); restarts are not, through resume data. A fib request: `AddOptions.Seed` (trust the storage
  as complete, as libtorrent's seed mode) or resume data from a Builder's result.
- **`metainfo.Builder` holds every 16 KiB block's v2 leaf hash of a file until the file ends**,
  1/512 of its size (a 1 TiB file takes 2 GiB while hashed, per `max_hashing`); a fib request to
  fold leaves into piece roots as it goes.
- **`metainfo.Builder` cannot be cancelled**: fit stops a build through the reads of the `fs.FS` it
  gives it; a `Builder.BuildContext` would be cleaner.
- Not run: Windows (name sanitizing), Linux CI, libtorrent or anacrolix fetching from fit's static
  web seed; the Admin API and pages for publishing are I203/I204's.

### I203 Admin API, events, `fit bt`, metrics

- **Do**: `/bittorrent/torrents` (list, add, upload, pause, resume, remove, recheck, priorities,
  limits), its files, peers and trackers, `/bittorrent/tracker/stats`, an SSE stream of events;
  `fit bt add|ls|info|pause|resume|rm|limit`; metrics by category and state; events in fit's log.
- **Touches**: `bittorrent`, `admin`, `cli`, `observe`.
- **Done** 2026-11-09: the Admin API's `/bittorrent/...` (`admin/bittorrent.go`): torrents listed
  (filters `state`, `category`, `q`, paged), added from a magnet link, a URL or a .torrent file
  (JSON with base64, `application/x-bittorrent`, or a form read part by part, 32 MiB), one with its
  files, actions (`pause`, `resume`, `recheck`), `PATCH` (paused, sequential, category, limits,
  queue position, priorities), `DELETE` with `delete_data`, `.../files`, `.../peers`,
  `.../trackers`, `/bittorrent/alt_speed` (GET, PUT), `/bittorrent/publish[/<name>]`, the
  tracker's `stats`, `torrents[/<hash>]` and `passkey`; the role's errors mapped to statuses; the
  capability `bittorrent` in `/node`. The event stream `GET /bittorrent/events`
  (`admin/btevents.go`) on the role's events (`bittorrent/events.go`: added, removed, state,
  completed, error, peer_banned, seed_limit, alt_speed; a hub of 32 subscribers with 256-event
  buffers, the last 1024 kept for `Last-Event-ID`, `reset`, `lagged`, `end`, heartbeats), the
  events in the `bittorrent` log. The role's API gains `Peers`, `Trackers`, `Recheck` and
  `Subscribe` (`bittorrent/inspect.go`), and `Add` keeps a torrent `bt_stream` added on the fly.
  Metrics (`bittorrent/metrics.go`, `bittorrent/tracker/metrics.go`, both roles `app.Metered`):
  bytes, rates, peers, seeders and hash failures by category, torrents by category and state,
  bans, the alternative rates, the event subscribers, published torrents; the tracker's torrents,
  peers, bounds, announces, scrapes and refusals. `fit bt ls|add|info|pause|resume|rm|limit|alt`
  (`cli/bt.go`). Docs: [BitTorrent](bittorrent.md#the-admin-api) (Admin API, events, `fit bt`,
  metrics) in both languages, the reference's chapter, fib requests A87 (a torrent's trackers and
  their announces) and A88 (an in-place recheck). Tests: every endpoint without the roles (404),
  behind the token (401), each way of adding and its errors (duplicate, bad link, none or two
  sources, bad JSON, a bad scheme, a URL that fails, an unknown category, bad limits, not a
  torrent, unsupported types, a bad form, a form field and a file past their bounds with and
  without `Content-Length`, too many parts, `max_torrents`), the list's filters and pages and their
  errors, a torrent by ID in either case, files, peers, trackers (from a .torrent file and a magnet
  link), actions and their errors, `PATCH` and its errors, removal with and without the data, the
  alternative rates, publish, the tracker's endpoints, the error-to-status table, the console
  (login, an add logged, the stream's 501); the stream: an add's events, heartbeats, a replay from
  `Last-Event-ID`, `reset`, a bad ID, 32 subscribers and a 503, a closed stream freeing its slot, a
  client reading nothing while 40000 events come dropped with `lagged`, `end` when the role stops;
  the hub's bounds; the role's events through a download, a pause, the alternative rates, a ban, a
  seed limit, a storage error and a removal, in the log, with a replay and the end at stop; peers
  between two roles, trackers and web seeds of a magnet link, a recheck reading the data again;
  metrics by category outliving a removal and a stop, bans and the alternative rates; the
  tracker's metrics; `fit bt`'s commands, requests and output, ID prefixes and names, refusals and
  usage errors.

**Decisions pending review** (made while building it, 2026-11-09; in force until the user rules):

1. **No pool for the role's API in the Admin API**: each call runs on the request's goroutine,
   net/http's on the Admin API and the console's bounded pool behind the console, as the sync and
   media endpoints do; neither is a fib worker, and `admin.max_conns` bounds them. Alternative: a
   pool of the role's own for its API.
2. **Statuses**: no such torrent 404, the role not running here 404, bad input 400, added already
   or broken 409, `max_torrents` (and `max_temporary`) **429**, too many URL downloads or event
   streams and the client not running **503** with `Retry-After: 5`, a URL that fails 502. Why:
   429 says the caller asked for more than it may hold; 503 is a state that passes. Alternatives:
   `max_torrents` 409 or 507; the role not running here 503.
3. **Adding takes three bodies**: JSON (the file in base64), the raw file as
   `application/x-bittorrent` with the options in the query, and `multipart/form-data`, read part by
   part; a .torrent file is bounded by **32 MiB**, as one by URL (not fib's 128 MiB), a form's other
   fields by 64 KiB, 16 parts. Answered `201` with `Location`. Alternatives: fib's bound;
   `max_config_bytes`; JSON only.
4. **`PATCH` is not atomic**: its changes are made in a fixed order (paused, sequential, category,
   limits, queue position, priorities), each saved, and the first that fails is answered with those
   before it kept; the limits are parsed before any change. Why: the role's API has no
   transaction. Alternative: check every field first against a status, then apply.
5. **`recheck` removes the torrent from fib's client, keeping its files, and adds it again without
   resume data** (whose file is removed, so that a crash during the check checks again), since fib
   has no recheck of a running torrent; its connections and fib's counters start again, its place,
   priorities, limits and pause stay. Filed as fib request A88. Alternative: answer 501 until fib
   has `Torrent.Recheck`.
6. **`/trackers` lists what the .torrent file or magnet link names**, with tiers, and the web
   seeds, no announce state: fib exposes none (fib request A87).
7. **Slow event subscribers are disconnected, not skipped**: a buffer of 256 events per stream; one
   that fills gets `event: lagged` and is closed, to reconnect with `Last-Event-ID`, replayed from
   the last 1024 events the role keeps; a gap, or an ID past the last (fit restarted: IDs count
   from 1 at each start), begins the stream with `event: reset`. At most 32 streams (503), a
   comment every 15s, `end` when the role stops or the Admin API shuts down. **Not configurable**,
   so no schema change. Why: a stream that silently misses events shows a wrong state; a reconnect
   with replay loses nothing in the common case. Alternatives: drop the oldest events and mark the
   gap; config for the bounds.
8. **The event stream is served on the Admin API's own address only**: the console's proxy answers
   whole responses (its recorder), so it answers 501 there. I204's pages need a streaming console
   route (on fib, with `OnWritable`), or poll. Alternative: build that route now.
9. **State changes are noticed by the manager's look every second**, and at once after the API's
   changes, by comparing each torrent's status state with the last one published, since fib's
   events do not say when a torrent starts downloading, seeding or checking. Events are published
   on the API's, the manager's and fib's event goroutine, never waiting.
10. **Adding a torrent `bt_stream` added on the fly keeps it** as an ordinary torrent as it is
    (category, limits and pause of the request not applied), answering its status, rather than 409
    (I200's open point). Alternative: apply the request's options too.
11. **Metrics**: the role adds fib's per-torrent counts to totals by category each look (and before
    a removal, a recheck or a client closing), so counters outlive torrents but start at zero when
    fit starts (not persisted, as I197's decision 9); `category=""` for none and no separate
    total; a scrape reads a snapshot without the role's lock; bans are client-wide; the tracker's
    are its `Stats`. Alternative: keep totals in `torrents.json`.
12. **Logs**: added, removed, bans and seed limits at info, state changes at debug; completions,
    storage errors and the alternative rates keep their earlier lines (info, error, info).
13. **`fit bt`**: a torrent is named by its info hash, a unique start of its ID of at least 4
    digits, or its name, resolved through the list; rates take `K`, `M`, `G` (binary); a file is
    sent raw unless `-up`/`-down` ask for JSON; a refusal of one torrent exits 1 after the rest are
    tried. No `peers` or `events` commands. Alternatives: IDs only; `fit bt events`.
14. **A user's passkey is on the Admin API** (`/bittorrent/tracker/passkey?user=`), behind its
    token, beside the role's console. The list's page is 1000 torrents by default, 10000 at most.

Open from it: fib requests A87 and A88; the
console's streaming route for the pages (I204); event and metric counters are not persisted. Not
run: the Linux race run and the full suite (left to the main session).

### I204 The workbench's BitTorrent pages

- **Do**: torrents (rate, progress, ETA, ratio), a torrent's files, peers, trackers and pieces, the
  add dialog, the tracker's page, publishing's page; schema descriptions in `schema-zh.js`; the AI
  assistant's functions (list, add, pause) under its approval rules.
- **Touches**: `app/workbench`, `assistant`.
- **Done** 2026-11-09: a **BitTorrent** menu (`app/workbench/web/bittorrent/`, after Sync):
  Torrents (state, why held, size, a progress bar, rates, time left, ratio, peers, category;
  filtered by state, category and name through the Admin API; picked torrents paused, resumed or
  removed with or without their data; Add torrent by magnet link, .torrent file sent raw, or URL,
  with category, paused, sequential; the client's recent events), a torrent's Overview (numbers,
  pieces had, hashes, directory, magnet link; category, queue position, sequential and its own
  limits edited in place beside those in force; pause, resume, recheck, remove), Files (priorities
  one by one or for the files picked), Peers and Trackers; the alternative rates (auto, on, off) in
  the client pages' top bar; Tracker (totals against bounds, refusals by reason, the allow-list's
  swarms paged, a user's passkey looked up for a private tracker); Publishing (entries, problems,
  torrents with a .torrent download and the magnet link); BitTorrent config. English and Chinese.
  Live updates: the console's own route `GET /api/m/admin/bittorrent/events` (`admin/btconsole.go`),
  the role's stream written on fib's connection (the worker subscribes and writes the start, a
  goroutine of the stream's own writes with `WriteNoWait` and waits for `OnWritable` past the
  watermark, at most 30s), with `Last-Event-ID`, the role's 32 subscribers bounding it; the pages
  read it beside a poll every 2s for rates, and poll only where it cannot stream. A new Admin API
  endpoint `GET /bittorrent/publish/<name>/torrents/<id>` serves a published .torrent file.
  `schema-zh.js` already had every BitTorrent description (I197–I203 added them; the test passes).
  The assistant (`assistant/tools_bittorrent.go`, `proposals_bt.go`): read tools `bt_torrents`,
  `bt_torrent`, `bt_tracker_stats` (no magnet link, trackers by scheme, host and last element, web
  seeds without user or query, no peer IDs, no passkey), and propose tools `bt_add_propose`,
  `bt_pause_propose`, `bt_resume_propose`, whose steps join the proposal's media steps, checked on
  what runs now and applied after the config by the person who confirms; a group `bittorrent` on
  the Functions page, words for the proposals' cards, the system prompt, `member` on a manager.
  Docs: [BitTorrent](bittorrent.md#the-pages) (the pages, the assistant, the console's stream, the
  .torrent endpoint), [the console](console.md#bittorrent) (the pages and the tools), the reference's
  chapter, in both languages. Tests: `admin` (the console's stream behind a session, an add's
  events, heartbeats, replay by `Last-Event-ID` and `?after`, reset, a bad ID, 404 without the role,
  the bound of 32 and 503 with `Retry-After`, a slot freed, a client reading nothing dropped with
  `lagged`, `end` when the role stops; the .torrent endpoint and its 404s), `assistant` (each read
  tool, its arguments and errors, the role not running, nothing secret in the results; the propose
  tools in a chat and over MCP, applied in order as the person, with a config change, a torrent gone,
  a failure making the outcome `partial`; every check refusing a step; the levels and groups),
  `app/workbench` (every word in both languages and used, every state, hold, event and priority
  worded, the endpoints the pages call, the assistant's tools worded). Checked in the browser
  pane against a local fit: list, add, live events, resume, remove with data, a torrent's overview
  and limits, alternative rates, tracker, publishing and its .torrent download, config, in English
  and Chinese.

**Decisions pending review** (made while building it, 2026-11-09; in force until the user rules):

1. **Live updates by a streaming console route, and a poll**: the console serves the event stream on
   a route of its own on fib's connection, rather than the pages polling alone, since events say at
   once what a poll says late (a completion, an error, a ban); the list is still read every 2s for
   rates and progress, which no event carries. Alternatives: poll only; a streaming mode of the
   console's proxy for every endpoint.
2. **The console's stream waits at most 30s for a client past the watermark** (`btStall`), then is
   cut; it shares the role's 32 subscribers with the Admin API's stream. Not configurable.
   Alternatives: a bound of the console's own; the console's `write_stall_timeout`.
3. **Through a manager's node switcher the pages poll only**: its pass-through answers whole
   responses. Alternative: a streaming pass-through in `cluster`.
4. **One module, `bittorrent`, on the server module `admin`**, shown wherever `admin.console` is (as
   Sync), each page saying when its role does not run, with a link to BitTorrent config; the role's
   own `console` serves only its read-only routes, without these pages. Alternatives: a module per
   role, shown only while it runs; pages on the role's own console.
5. **The alternative rates are in the top bar of the client's pages** (Torrents and a torrent), not
   of every page. Alternative: a `topbar` on every page.
6. **The assistant's writes are propose tools** (add, pause, resume), whose steps join the
   proposal's media steps (`MediaAction`, `media` in the view and the apply's answer): checked on
   what runs now, applied by a person whose level is apply after the second confirm, as the media
   steps are. No tool removes, rechecks or limits a torrent: those stay the person's, on the pages.
   Alternatives: a `bittorrent` member of its own in the view; `bt_remove_propose` and the like.
7. **What reaches the model**: no magnet link, trackers as scheme, host and last path element (a
   private tracker's passkey sits in the path), web seeds without user or query, peers without IDs,
   no passkey (the Tracker page reads them for the person); peers' addresses go as they are, as the
   media tools send addresses. Alternative: hash the peers' addresses as playback subjects are.
8. **Ratio is uploaded over size**, as `seed_ratio` counts it, and the time left is what it wants at
   its present rate while it downloads, both computed by the pages and the tools, not the role.
   Alternative: uploaded over downloaded (qBittorrent's).
9. **Pieces are shown as had of all, with a bar**, not as a map: the role's status gives the counts
   only. Alternative: a piece map from fib's `Torrent.HavePiece` in the role's API (open).
10. **A published torrent's .torrent file comes from a new Admin API endpoint**,
    `GET /bittorrent/publish/<name>/torrents/<id>`, read from the role's snapshot, rather than a link
    to a `bt_publish` route, which the config may not have and which is served on a listener's origin.
    Published torrents are seeded beside the client's list, so they link to no torrent page.
11. **Categories offered come from the running config** (`GET /config`), not the draft: a category
    only in the draft is refused by the role. Alternative: the draft's.
12. **The page adds a .torrent file raw** (`application/x-bittorrent`, options in the query),
    checked against 32 MiB before it is sent. Alternative: a form.
13. **Files take four priorities**: skip, low, normal, high (0, 1, 4, 7), fib's named ones, though the
    role takes 0–7. Alternative: all eight.
14. **`bt_add_propose` of a torrent `bt_stream` added on the fly is allowed, with a warning** that it
    is kept as an ordinary torrent as it is (I203's decision 10).
15. `TestChatSummarizes`'s window grew from 5600 to 6200 tokens for the new tools' descriptions.

Open from it: a piece map (the role's API, from fib's `Torrent.HavePiece`); pages on the role's own
console; a streaming pass-through for the node switcher. Not run: the Linux race run and the full
suite (left to the main session).

### I205 Interop, failure tests, benchmarks and docs

- **Do**: fit's client and tracker against libtorrent and anacrolix in containers (fib's interop
  way); disk full, restarts and reloads under load; throughput and CPU on the benchmark host only;
  `docs/bittorrent*.md`, the reference, an example in fit-examples.
- **Touches**: `test`, `bench`, `docs`, fit-examples.
- **Done** 2026-11-09 (branch `bt-i205`; fit-examples branch `bittorrent-example`):
  - **Interop**: `test/btinterop`, a module of its own (anacrolix/torrent 1.61 and fib's metainfo
    builder; decision 037 covers the dependency), whose tests build fit from the checkout and run it,
    steered through the Admin API, against anacrolix in the test's process and libtorrent-rasterbar
    2.0.11 through `libtorrent_peer.py` (`FIT_LIBTORRENT=1`); `run.sh` runs it all in docker, CI's
    `bittorrent-interop` job on the runner with Ubuntu's python3-libtorrent and cgo. 46 cases, all
    passing: fit downloading from and seeding to both (v1, hybrid and, with libtorrent, v2; .torrent
    files and magnet links; TCP and uTP; MSE off, preferred and RC4 required; the peers' transport and
    encryption checked through the Admin API); the private tracker over HTTP (a token's passkey) and
    UDP (a user's passkey from the Admin API, BEP 41) for anacrolix and libtorrent swarms, fit's own
    client among them, an unknown passkey refused; `bt_stream` playing a 24 MiB file while it
    downloads from anacrolix (a middle range answered at 8% of the torrent) and a magnet link added on
    the fly; a publish entry's web seed (fit's `static` route) alone, `per_file` and `whole_dir`, by
    libtorrent, `whole_dir` by anacrolix.
  - **Failure tests** (`bittorrent/failure_test.go`), beside I197–I203's: the disk filling during a
    download (ENOSPC writes while the free-space check still finds room: the error in the status, the
    blocks asked for again; then the check pausing it; then completing, no hash failure); a stop and
    restart in the middle of three downloads (every piece had again with no read, then completing); a
    `listen` change while downloading (the pieces kept, no read, completing); the only seeder cut in the
    middle of a piece's block, then back; one of two seeders stalled mid-piece (the download done from
    the other in about 2s, far before fib's 60s snub).
  - **Benchmarks** on the benchmark host: `bench/bt.sh`, `bench/bt/host.sh`, `bench/bt/summary.py`
    and `test/btinterop/btbench`; one 1 GiB torrent in `/dev/shm`, the client measured on 4 CPUs, four
    fib peers on 8 others, fit beside anacrolix and fib's client alone, plaintext and RC4, 3 alternating
    rounds; results in `bench/results/bt-2026-11-09` (medians). Seeding to 4 leechers fit does 1688
    MiB/s plaintext (anacrolix 1110) at 0.60 CPU s/GiB (1.23), 933 with RC4 (744), peak RSS 46–56 MiB
    (anacrolix about 1050). Downloading from 4 seeders fit does 705 MiB/s plaintext (anacrolix 1039,
    fib alone 478) and 553 with RC4 (759, 363), at anacrolix's CPU per byte, under 2 of its 4 CPUs:
    bound by fib's torrent lock (below).
  - **Docs**: [BitTorrent](bittorrent.md) in both languages: an overview of the four parts, a config
    with every part (validated with `fit validate`), the duplicated state row merged, and new
    sections Interoperability, Performance and Limits and security; the configuration overview's
    rows and the README's docs list, both languages. **Example**: fit-examples' `bittorrent/`
    (client with categories, `alt_speed`, `watch_dir` and a publish entry, the private tracker,
    `bt_tracker`, `bt_publish`, `static`, `bt_stream` with `magnet`, the Admin API), listed in its
    READMEs; fit-examples' pin already had the roles.

**Decisions pending review** (made while building it, 2026-11-09; in force until the user rules):

1. **The interop tests drive the fit binary through its Admin API**, as test/mcpsdk does, rather
   than the roles in-process: they check what a user runs, need no `replace` of fit in the module,
   and the same harness serves the benchmark. The module requires fib at fit's version for the test
   torrents only. Alternative: in-process roles with `replace github.com/lesismal/fit => ../..`.
2. **The anacrolix cases run in every `go test` of the module; the libtorrent cases need
   `FIT_LIBTORRENT=1`**, as fib's; CI runs both, with `-race`, within 25 minutes. The module is not in
   fit's main `go test ./...` (a module of its own).
3. **Interop transfers run at 2 MiB/s** (fit's own `limits`), so that a case lasts the seconds it
   takes to see its peers through the Admin API, which lists only connected peers. Alternative: an
   event for each peer connected.
4. **The other clients' bugs are gone around in the tests and recorded, not fixed upstream yet**:
   anacrolix takes a v2/hybrid single-file torrent for a directory (its web seed refuses a URL not
   ending in `/`, so `per_file` web seeds are not fetched by anacrolix, and it looks for the file below
   its name), its web seed skips a short last piece, and 1.61 panics announcing a hybrid torrent it
   checks; the cases use v1 or `whole_dir` with it. Alternative: report them to anacrolix and skip
   the cases until fixed.
5. **The benchmark's baseline is anacrolix, beside fib's client alone**; libtorrent is not installed
   on the host, and through its Python binding the harness would measure Python too. The other side is
   fib's client (its seeders take the first one's resume data, checking nothing); data in `/dev/shm`
   so the disk is not measured; 1 GiB, v1, 1 MiB pieces, TCP; CPU and peak RSS from `/proc`. Not a
   plan report: no scores or ranking (rule 24 is for the full plan). The host's LLM service was left
   running (idle; 13 GiB free), as the user prefers.
6. **btbench lives in test/btinterop** (`btbench/`), the module that already has anacrolix, not in
   `bench/` (whose Go is fit's module). It waits for fit's `pieces_have` rather than `complete`, which
   the role sets at its manager's look every second (see open items).
7. **The failure tests fake a full disk with a storage wrapper and the free-space function**, not a
   tmpfs or loop device (root, Linux only); cut and stalled peers through a TCP relay of the test's.
8. **The docs' config example is .conf with placeholders** (no `${...}`), the tracker public in it
   so that the published torrents carry no passkey; the security notes say why.

Open from it:

- **fib: a torrent's download is bound by its lock** (a fib request to file): every block received
  takes `Torrent.mu` and refills the peer's requests, `nextRequest`/`pickPiece` scanning pieces with
  `math/rand` (24% of fib alone's CPU in a profile of a 1 GiB download from 4 seeders, `pickPiece`
  21%), and each 16 KiB block is a `pwrite` of its own (30%). Suggested: a peer reserves a piece and
  requests its blocks without picking again, refilling when its pipeline falls below half its reqq;
  rarest-first kept in buckets updated on have/bitfield rather than scanned; blocks written per piece
  or per run (`pwritev`) on the disk pool. fit would gain the download rate with no API change.
- **fib alone downloads at 0.68 of fit's rate** on its own engine: its engine defaults for a client
  made without `SharedEngine` are worth a look in fib.
- **fit: `Status.Complete` lags up to a second** (the manager's look, I198's decision 10): an API
  client polling for completion waits for it; computing it from fib's torrent when asked would not
  count a completion twice if the count stays the manager's.
- Report the anacrolix bugs above upstream. fit-examples' `bittorrent/` announces published torrents
  to its private tracker with a static token, which every downloader then shares (its comments call
  the token a placeholder; the docs' security notes say to publish to a public tracker).
- Not run: the full `go test -race ./...` and the Linux race run (left to the main session); the
  interop tests ran in docker (Linux) and anacrolix's on macOS.

### I206 (second phase) The cluster and BitTorrent distribution

- **Do**: the cluster's view of its members' torrents and rates; spreading a large file (an LLM
  node's model, a release) to a group of members over a private torrent with direct peers, beside
  rollouts.
- **Touches**: `cluster`, `bittorrent`, `app/workbench`.

### I207 (second phase) A tracker store the cluster shares

- **Do**: a `tracker.Store` on fit's shared storage (redis, etcd), so trackers on several members
  answer as one.
- **Touches**: `bittorrent`, fit's shared storage.

### I208 (second phase) RSS auto-download

- **Do**: feeds polled on a schedule, rules (patterns, categories, size bounds, episode
  deduplication) adding torrents; bounded feeds, items and history.
- **Touches**: `bittorrent`.

### I209 The roles use what fib A80–A86 added

- **Why**: I197, I200, I201 and I202 left fib requests (A80–A86, done at fib `ecdcec0`, fit on
  `e633fc2`): a published file hashed twice, builds stopped only by failing their reads, a public
  tracker unable to list its torrents, reloads of the tracker's bounds starting the swarms empty,
  the client's event queue unbounded, `Reader.Close` waiting for a read.
- **Do**: publishing adds a newly built torrent with `AddOptions.Seed` (resume data afterwards,
  never both: `ErrSeedResume`) and builds with `BuildContext`; the tracker lists a public tracker's
  torrents through `MemoryStore.Range` (Admin API, console, pages; paged, bounded) and applies new
  bounds and `peer_ttl` with `SetConfig`; the client sets `Config.EventQueue` and surfaces
  `EventDropped` (event stream, `fit_bittorrent_events_dropped_total`); `bt_stream` drops what A83
  makes unnecessary, where safe.
- **Done when**: a new publish reads no data at add; a build stops promptly on a stop; a public
  tracker's torrents are listed and paged; a reload of bounds keeps the swarms; dropped events are
  counted and published; docs in both languages.
- **Touches**: `bittorrent`, `bittorrent/tracker`, `handler/btstream`, `admin`, `assistant`,
  `config`, `app/workbench`, `docs`.
- **Done** 2026-11-09 (branch `bt-i209`): **Publishing** (`bittorrent/publish.go`,
  `publish_walk.go`): an item built here and without resume data is added with `AddOptions.Seed`,
  later with its resume data alone (saved once it has every piece, which with seed mode is at once,
  its unchecked pieces kept as such); an entry has a context, cancelled when it halts, which ends
  its walks and its builds (`Builder.BuildContext`), replacing the atomic flag and the `fs.File`
  wrapper that failed reads (a test hook on reads remains). **Tracker** (`bittorrent/tracker`):
  `ListTorrents(offset, limit, after)` with `Source` (`allow_list` or `store`), `Total` and `Next`;
  a public tracker's list ordered by info hash through `Range`, keeping the `offset`+`limit`
  smallest past `after` in a heap; `Torrents` keeps its signature; the console's `/api/torrents`
  and the Admin API's `/bittorrent/tracker/torrents` take `after` (400 for a bad one, and with an
  allow-list) and answer `source` and `next`; a reload of the bounds or `peer_ttl` calls
  `SetConfig` on the store it has, and a change of the interval too makes a new `Server` on it; the
  four fields' descriptions (schema, reference, templates, `schema-zh.js`) say so; the Tracker page
  words the list by its source; the assistant's `bt_tracker_stats` description. **Client**
  (`bittorrent/role.go`, `events.go`, `metrics.go`): `Config.EventQueue` set to `EventQueue`
  (fib's default 4096); `EventDropped` counted (`fit_bittorrent_events_dropped_total`) and
  published by the manager's next look as one `events_dropped` event with `dropped`; the pages word
  it. **`bt_stream`**: nothing removed (below); its comment says why. Docs:
  [BitTorrent](bittorrent.md) (publishing hashing once and builds stopping, the event type, the
  client's queue, the metric, the tracker's reloads and listing, the Admin API row, limits) in both
  languages; A80–A86 marked used in [fib-api-requests](fib-api-requests.md#a80-the-bittorrent-clients-event-queue-bounded).
  Tests: a new publish reading no storage at add with every piece trusted, then served (reads, and
  the trusted count reaching 0); a build of a 24 MiB file at 100ms a megabyte read cut short by a
  stop within 3 reads; a public tracker's 25 torrents listed by offset and by `after` (every one
  once, in order), the console's route, `after` refused with an allow-list; a reload of the bounds
  and `peer_ttl` keeping the store and its swarm, lowered bounds refusing a new torrent and peer,
  bounds with the interval (same store, new server), a shorter `peer_ttl` expiring the peers; the
  Admin API's `source`, `after` and a bad `after`; dropped events counted and published once for two
  drops; the pages' words. Run: `go test -race` of `bittorrent/...`, `handler/btstream`, `admin`,
  `assistant`, `config`; `go test ./schema/ ./app/workbench/ ./docs/ ./test/workers/`; vet, gofmt.

**Decisions pending review** (made while building it, 2026-11-09; in force until the user rules):

1. **No `event_queue` config field**: the role sets fib's default (4096) as a constant. Why: the
   role's handler takes a short lock and returns, and the events fib drops (pieces verified, hash
   failures, peers connected and disconnected) are ones the role never publishes, so no operator
   gains by tuning it. Alternative: `event_queue` in `bittorrent.client` (reload makes a new client,
   as fib reads it at `NewClient`).
2. **Dropped events are one `events_dropped` event a second at most**, published by the manager's
   look with the count since the last, not one per fib `EventDropped`, so that a burst cannot push
   the stream's real events out of the 1024 kept for replay nor lag its subscribers; logged at
   debug. A dropped piece event may leave a storage error shown until the next piece passes or the
   state changes (as before, those clear it too). Alternatives: forward each `EventDropped`; only
   the metric.
3. **A public tracker's list is ordered by info hash and paged by `offset` or `after`**: `Range`
   gives no order, so a page keeps the `offset`+`limit` smallest hashes past `after` in a heap, time
   in the torrents tracked (at most `max_torrents`) and memory in the page; `total` counts the
   torrents with peers (past `after`, when given). The allow-list keeps its own order and refuses
   `after` (400). Off the worker: the console's route runs it on its pool, the Admin API on the
   request's goroutine. Alternatives: a snapshot of the store kept by the role and refreshed
   periodically; `Range`'s order with a cursor of shard and position (unstable as torrents come and
   go); ordering by swarm size.
4. **Seed mode only for a torrent built in this process with no resume data yet**: a torrent
   restored from `index.json` without its resume data (a crash before it was saved) is still
   checked in full, as its files were not just hashed. Alternative: seed mode for those too, since
   the walk compares their stamps (but only after they are added).
5. **The build's cancellation is the entry's context alone**: the `fs.File` wrapper that failed
   reads once the entry stopped is gone, so a build stops within a megabyte read
   (`BuildContext`'s look), and a read that blocks is not interrupted (as before).
6. **`bt_stream` is unchanged in behaviour**: it never closed a reader while a `Read` ran; the
   stream's context, which the client leaving and `Drain` cancel, also ends the waits for the
   torrent's hold and info and is the parent of each read's `read_timeout`, so A83 removes nothing
   there. Alternative: close the reader from `OnCancel` (it would need the reader shared across
   goroutines, for no gain).
7. **Lowered tracker bounds drop nothing** (fib's `SetConfig`): what is past them leaves as peers
   stop or expire, within `peer_ttl`. Alternative: trim at once (fib offers no way).

Open from it: A84 (piece layers of a hybrid added by magnet link) and A86 (no `completed` for seeds)
work through the client with no change in fit and are not tested in fit itself; an interop case of
a v2-only peer fetching hashes from a fit seed of a magnet-added hybrid would cover A84. Not run: the
Linux race run and the full suite (left to the main session).
