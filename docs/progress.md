# fit development progress

[English](progress.md) | [简体中文](progress.zh-CN.md)

This file records what has actually been built, iteration by iteration, and where the next
session picks up. The [development plan](dev-plan.md) says what each iteration is meant to do;
this file says what was done, and where the work differs from the plan.

## Where things stand

| | |
| --- | --- |
| Last updated | 2026-11-08 |
| Milestone | M0, foundations: done (I01–I08). M1, the proxy core: done (I09–I23); its review fixes R01–R07f are all done. M2, dynamic config and TLS: done (I24–I35, with I30b and I30c). M3, gateway features: done (I36–I55, with I53b). M4, platform integration: done (I56–I66). M5, caching and extensions: done (I67–I75). 1.0: its iterations done (I76–I78); since, I79, a Caddyfile converter and JSON with comments, I80, fit's own .conf format and configs in several files, I81, Proxy-Wasm's response callbacks, I82, fit's own log by logger and level, and I83, fit's log in fib's formats with loggers of their own, and the access log's formats. P2P, NAT traversal: I84, STUN messages and the STUN server, I85, A49 in fib, and I87, the TURN server, done; I86 and I88–I93 to come, on fib's WebRTC packages since decision 029. VPN, a network of fit nodes: I94, IP packets and the overlay route table, done; I95–I103 to come. Console: I104, the config kept encrypted and the console, done. Agent and tunnel: I105, roles in fit's config, I106, the tunnel, and I107, the agent, done; I112, the agent's model APIs (OpenAI and compatible, Anthropic, Gemini, Azure OpenAI, Ollama, Cohere), done; I113, the LLM roles' node list, links and traffic, done; I114, every page in one workbench, done. WebRTC: I108, decision 029 and fib `80c7f76`, I109, WHIP and WHEP with fit as the media server, I110, media servers proxied behind one UDP port, and I111, fib's A55–A59 taken up (TURN over TCP and TLS, per-allocation caps, a port-aware peer check, events, changes in place and drains; the proxy's source address and ports), done. Media: I115, media auth policies (`media_auth`, package `mediaauth`, `auth` on `whip`, `whep` and `webrtc_proxy`, `fit media-token`), done; I116, the streaming role (`streaming`: RTMP, RTSP, SRT, `httpflv`, `hls`, `stream_whip`, `stream_whep`), and I117, the media pages (`media_pages`, a room API, microphone and camera switches), done; I118, the room chat of their calls (text messages and small files, a page switch, and the microphone and camera a member joins with), done (decision 030); I119, media tokens from a route (`media_tokens`: batch issuance for a business backend signed with one key of the policy's `keys` ring, prefix bounds, revocation of one key or of all), done; I120, the workbench's config pages (a flat module menu, per-page `.conf`/`.yaml`/`.json` tabs checked through the apply path, console passwords changed in the pages and kept in the store), done; I121, consistency between what the running service does and the config it runs from (the drift and info differences with the fixes offered, the Consistency page, `fit check`, and `GET /config/check`/`POST /config/reconcile`), done; I122, the workbench's feature tabs (streaming, media auth, WebRTC, tunnel and LLM config on pages of their own) and each listener port's features with links to their config pages, done; I123, the Engine menu (a first-level entry, each listener port a submenu of its features in the asked order, with the port's feature tags on its line), done; Logging: I155, a bounded file sink with the logs' `time_zone` (local by default, UTC or an IANA zone), rotation by size and by time, retention by age, number and total size and gzip compression, I156, the access log's file through the same sink, and I157, the workbench's Logs page with `GET /logs`, Rotate now and Clean up now, done (decision 034) |
| fib | fib's `main` at `f957983` (`v1.7.3-0.20261108041507-f957983ac955`), with A73, `Binding.Filter`, which refuses a peer by address before a Connection is made (`8de0a75`), and A74, `webrtc.MuxConfig.Filter`, for I195's access lists; before it, fib's `main` at `856ad5b` (`v1.7.3-0.20261106115119-856ad5bb2135`), with A70, TURN relays that never share a port of the range (I162), and A71, `925f292` and `0ddaba3`, Low-Latency HLS parts across new tracks; before it, fib's `main` at `826c2c3` (`v1.7.3-0.20261106074945-826c2c3786ae`), with A68, new tracks on the same clock (I134); before it, fib's `main` at `8998d58` (`v1.7.3-0.20261106054245-8998d58120f6`), with `media/mp4` (A64, I131); before it, fib's `main` at `cb9bbbe` (`v1.7.3-0.20261105195654-cb9bbbe30e24`), whose HTTP/2 request checks cost what they must (I158), on v1.7.2 (`d6f8809`, with A65–A67 for the security round); before it, fib's `main` at `60d67aa` (`v1.6.1-0.20261104101432-60d67aa22f8d`): `1262648` adds `Connection.SpliceTo` and `Context.SpliceBody` (A62) and `60d67aa` documents them and their tests' fallbacks, which fit's proxy now uses by default for large HTTP/1.1 uploads (`splice_uploads`); before it, fib's `main` at `9824c68` (`v1.6.1-0.20261031223653-9824c6852e7d`): `63b38d4` has a Windows dial from a local port skip one held on another address (A59, which fit's WebRTC proxy relay dials with), `749189e` keeps A59's tests off ports in TIME_WAIT, and `9824c68` has a connection the HTTP client closes stop counting against MaxConnsPerHost before the request it failed hears why; before it, fib's `main` at `84a67f7` (`v1.6.1-0.20261031213322-84a67f76ecfc`), with A56 (`74100b2`, a TURN server that never makes a worker wait), A57 (`bf669ce`, caps, a port-aware peer check, events and stats), A58 (`496301d`, limits and credentials in place, a drain), A55 (`97201b6`, TURN over TCP and TLS) and A59 (`3655193`, a dial from a chosen local address and port range), for I111; before it, fib's `main` at `80c7f76` (`v1.6.1-0.20261031191422-80c7f7662ab4`), the first with WebRTC (`webrtc` and its packages, `Engine.OpenUDPPeer` for A49), for I108; before it, fib's `main` at `47948dd6` (`v1.6.1-0.20261031164450-47948dd66351`): `47948dd6` has QPACK's decoder remember each string by its place in the section, so that a browser's literals, sent on every request, are decoded once (W2 over h3: site +4.0%, edge +2.8%); before it, fib's `main` at `c20ab7a1` (`v1.6.1-0.20261031151458-c20ab7a1705e`): `c20ab7a1` holds an HTTP/3 request stream until its OnWritable callback has run, a data race fit's CI met in `TestStuckClientsPastMaxConns/h3`, and `e66f4cf6` fixes an intermittent arpc test; before it, fib's `main` at `d7e117c1` (`v1.6.1-0.20261031142854-d7e117c1f901`): `d7e117c1` decodes a long header literal a peer repeats unindexed once, as nghttp2 clients send a bearer token (W1 h2 +6.7%, 28.60 to 26.86 us a request), on `2ea8ddb7` and `da0ac655`, which fix and cover tests; before it, fib's `main` at `a6131b0c` (`v1.6.1-0.20261031112846-a6131b0c0af3`), whose give-back test looks for a pooled body at the handler's Get, on `d3707ca7`: an HTTP/2 stream gives a body from the pool back once it has framed it, where it had left it to the collector and so emptied the pool's size class (A48; W1 h2 +1.3%, allocation 850 to 677 MB/s); before it, fib's `main` at `6bd106d9` (`v1.6.1-0.20261031020358-6bd106d9d44b`), which widens two timing tests' margins for Windows CI, on `46827ea7`: a whole response's body of up to 4 KiB comes from the pool and goes back with `ClientResponse.TakeBody` and `Context.WriteResponseOwned`, and a `NewBufferedBody` request body is not copied (A48; W1 HTTP/1.1 +3.0%, the 3% that `861689c9`'s smaller heap had cost in collections); before it, fib's `main` at `7dcb41da` (`v1.6.1-0.20261030055545-7dcb41da8e7c`): `4f870993` binds a pushed stream's Context before the stream is the connection's, a data race fib's CI met, and `7dcb41da` fixes an early data test's warm-up on Windows; `a4ae3a0c`: a file sealed in Go as it is sent tells the kernel more follows each chunk but its last, so TCP fills its segments (a 1 MiB SendFile download over HTTPS/1.1 without the kernel's TLS +6.4%, W2 site HTTPS/1.1 +1.7%); before it, fib's `main` at `881ff97a` (`v1.6.1-0.20261029202329-881ff97a25f2`): `6c61693d` keeps a streamed response body in pooled 64 KiB chunks (W4 proxy-download h1 +11%, about 55 to 32 MiB; h2 +8%, about 137 to 100 MiB), `8895a6aa` grows a task pool's queue with its backlog instead of allocating it at its limit (W6 h2 50 to 32 MiB), `24beda14` and `881ff97a` make a connection the kernel's TLS seals write whole records with MSG_MORE and switch to the kernel's TLS only for files of 512 KiB or more (a 1 MiB SendFile download over HTTPS/1.1 +29%, which lets fit drop NoKernelTLS); before it, fib's `main` at `c794cf89` (`v1.6.1-0.20261029134914-c794cf8960fe`): `d5b8d477` frames an HTTP/2 response sent whole as the socket takes it, `0eb21d67` hands a streamed Content-Length response body to its sink from the read, `08faed60` fits sealed TLS records to the pool's size classes (W4 download h2 +11.5%, about 167 to 77 MiB; proxy-download h1 +6.6%, h2 +2.7%), and `c794cf89` makes a streamed HTTP/2 body hold the connection's window until it is consumed (W4 proxy-upload h2 about 312 to 252 MiB); before it, fib's `main` at `4ad4ebef` (`v1.6.1-0.20261029100241-4ad4ebefac94`): a streamed HTTP/2 or HTTP/3 request body waits in 64 KiB pooled chunks (W4 proxy-upload h2 +12.7%, 553 to 322 MiB); before it, fib's `main` at `56f08db3` (`v1.6.1-0.20261029003614-56f08db38f88`): `861689c9` starts a connection's read round at 16 KiB and remembers when it grew (W1 at 10,000 in flight 1,076 to 639 MiB, W4 within 2%), `b926ebf5` and `c6c2e10b` cut HTTP/3's per-packet work (W2 site h3 +2.3%); before it, fib's `main` at `f0378649` (`v1.6.1-0.20261028184321-f037864957c0`): `d4319879` recycles HTTP/3 request streams whose body did not stream (W0 h3 +17.6% to +20.0% at 500 to 10,000 in flight), and `f0378649` removes its test's body-size assumption; before it, fib's `main` at `a7b8b456` (`v1.6.1-0.20261028010536-a7b8b456e081`): `7f1fac3a`, whose Close takes the listeners under the lock a concurrent ShutdownBinding changes them under, a data race fit's CI met on macOS, and `8696ef40`, whose TLS server that answers early data and closes waits for the end of its handshake, so that a Windows client does not lose the answer to a reset, and `a7b8b456`, a fix of an HTTP/3 test's race; before it, fib's `main` at `33a039f1` (`v1.6.1-0.20261026175603-33a039f1382f`), with fib's package `log`, made for I83 (`185658f1`, and `33a039f1` for `ServeDuplex`'s panics); before it, fib's `main` at `a88c75d2` (`v1.6.1-0.20261025180753-a88c75d2f19b`): `b0809b4` with A46, a body filter whose work is done elsewhere, made for I81, and the HTTP/2, HTTP/3 and TLS work since; `b0809b4` itself: v1.6.0 with A42, the HTTP/3 server sending a round's datagrams in one batch (`a34c5db`), A43, h2 over TLS sealing a corked round as one record (`5a02929`), and a fix of A43's test; by commit, not tag, rule 19. Before it, `github.com/lesismal/fib v1.6.0` (`55e4140`, the first tag with what fit needs since v1.5.6, and fib's TLS fix that verifies a resumed session's peer again and requires Go 1.27.3; before it, fib's `main` at `fcb1718`: `86071b8` with A40, made for I67, a whole response sent while the body is still arriving saying Connection: close, made for I77, and fixes of two intermittent tests, `benchmark/rpc`'s `TestClientCountsFailures` and `http`'s `TestBodyWriterHoldsNoGoroutine`; before it, fib's `main` at `86071b8`, which is `4f1c3e9` with fib's prefork and eBPF reuseport balancing, `5fc8250` and `aa0ba7e`, and A38; by commit, not tag, rule 19); with A32 and A33 since I49 and I50, A34 and A35 since I51 and I52, A36 and A37 since I53 and I53b, A38 since I66's CI fixes, A42 and A43 since the v1.0.0 report |
| What works | `fit run` serves fixed responses and static files, and proxies to HTTP/1.1, h2c and h2 upstreams (with a CA, a server name and a client certificate of their own), with passive and active (HTTP and TCP) health checks, nine load balancing policies (round robin, weighted, least connections, random, P2C, consistent hashing on IP, URI, header or cookie) and sticky cookies, over HTTP/1.1, h2c, TLS (HTTP/1.1 and h2, certificates from files, loaded again when they change, or obtained and renewed with ACME through HTTP-01 or TLS-ALPN-01, kept in a directory or, for instances on several hosts, in Redis, etcd or Consul, where they share each certificate and challenge, or during the handshake on demand, chosen by SNI, with OCSP responses stapled; and HTTPS by default, with redirects, for a config that names only host names) and HTTP/3, writes access logs as JSON, logfmt or lines, reloads its config on SIGHUP, when the file changes (`-watch`), or through the Admin API (read, load, edit by JSON pointer and merge patch, ETags against lost updates, rollback to a kept version), serves Prometheus metrics on the Admin API, traces requests with W3C trace context sent on to upstreams and exports spans and metrics over OTLP/HTTP, and keeps responses in an HTTP cache by RFC 9111's rules, in memory and on disk, where they are found again after a restart, shared by the routes of a zone and kept across reloads, fetched once for many clients, answered stale while refreshed or when the upstream fails, and purged by URL or tag through the Admin API, serves a read-only dashboard of its routes, services, upstreams and their health, certificates and config versions, merges what providers provide into its config by written precedence rules and shows the effective config and where each part came from (`GET /config/effective`), takes routes, upstreams and services from the labels of Docker containers, following them as they start and stop, takes upstreams, routes and services from the tags of the services in a Consul catalog, following their health checks, and listeners, routes, upstreams and services from keys in Consul's KV store and in etcd, serves a Kubernetes cluster's Gateways of its GatewayClasses (Gateway API), with HTTP, HTTPS (certificates from Secrets), TLS passthrough, TCP and UDP listeners, and their HTTPRoutes, GRPCRoutes, TLSRoutes, TCPRoutes and UDPRoutes, writing their status back, and its Ingresses of fit's IngressClasses, with a few annotations, and shuts down gracefully; routes match by host, path, method, header, query, remote IP and path regexp, or an expression combining them, by priority, then specificity, then how many matchers they set; middleware rewrites paths, redirects, and sets, adds and deletes request and response fields, limits a route's request rate (by client IP, header or route), its requests under way, and the size of its bodies, and asks for a user and password (Basic auth, bcrypt) or a client certificate, which TLS listeners can require or verify when given (mTLS), asks an auth service (ForwardAuth), or verifies JSON Web Tokens with keys from files or a JWK set; the proxy retries within a budget and bounds a route's requests with a timeout; a circuit breaker stops sending a route's requests on while they fail; the proxy splits requests over upstreams by weight, mirrors a share to another and fails over to a backup; responses are compressed with zstd, brotli or gzip as they are written; WebSockets are proxied from HTTP/1.1, h2 and h3 entries to HTTP/1.1 upstreams, and gRPC calls to h2 and h2c upstreams, streaming both ways, with gRPC statuses for what fails at the proxy; tcp listeners proxy their connections to an upstream, byte for byte, and udp listeners their datagrams, a session per client, and TLS connections by their SNI without terminating them, with the PROXY protocol taken from trusted proxies (also on HTTP listeners) and sent to upstreams (services); one port serves HTTP, TLS terminated or passed through by its name, SOCKS and raw TCP, told apart by what a client sends first; SOCKS (CONNECT) and HTTP CONNECT forward proxies with access rules for clients and destinations; `fit validate`, `fit schema`, `fit version`, `fit hash-password`; the config kept encrypted in a store (`admin.store`, `fit store export` and `import`) and a console behind logins from which all of it is changed (`admin.console`); the agent's roles, hubs, nodes and clients that share AI models over a WebSocket mesh, and the tunnel's, which reach TCP services behind NAT through a server over TCP, WebSocket or QUIC, both set up and changed on pages of their own; WebRTC: a TURN server over UDP, TCP and TLS, WHIP and WHEP with fit as the media server, and media servers proxied behind one UDP port, routed by ICE ufrag; fit is packaged as a container image (linux/amd64 and arm64, published on a tag), a Helm chart that runs it as a Gateway API and Ingress controller, and systemd units, with examples for Docker Compose and Kubernetes; the benchmark harness runs nginx, Caddy, Traefik and fit; fit's own log and the access log write their files through one sink a path, rotated by size and by time and cleaned up by age, number and total size, every log's `time_zone` local by default and UTC or an IANA zone as the config says, and the workbench's Logs page lists the files and rotates or cleans them up now |
| What does not | 1.0's external security review, which is arranged outside the code; P2P tunnels between hosts behind NAT (I86, I88–I93: `p2p/stun` and `webrtc.turn` are their only parts so far) and the VPN (I95–I103: `vpn/packet` only) |
| Next | P2P: I86, the STUN client on fib's `webrtc/stun`, then I88 and I89 on fib's TURN client and ICE agent; I90 (A50 in fib) beside them. VPN: I95 (A51 in fib), I97 and I98 wait on nothing new. fib requests open: A50 and A51 for those, and A52–A54, which would replace the agent's and the tunnel's stopgaps. Media: RTSP over UDP waits on fib's A61. The next free iteration number is I158, and the next free fib request A64; the logging track (I155–I157) is done, and the AI assistant's I124 is the first iteration in the index still to do, with I152–I154 after it. Since v1.0.0 (2026-11-06), the review follow-ups I158–I161 are done, and I162 (one address, one server, with fib A70); the next free iteration number is I196, and the next free fib request A75; I195, allow and deny lists of client addresses (global, and per listener, route, service, role, Admin API and console), is done on 2026-11-08 with fib A73 and A74, its decisions pending review in its plan section; each has decisions made while building it, listed in its plan section as pending review. The AI assistant in the workbench (decision 031): I124–I130 and I154 done on 2026-11-06, each with decisions pending review in its plan section; I152 and I153 done since. The media library (decision 032): I131–I139 and I152 done on 2026-11-06, each with decisions pending review in its plan section, with fib A64, A68 and A69; open from it (fib's LL-HLS parts after a discontinuity whose tracks change, fixed since by fib A71): fib's HTTP client now and then opening a second backend connection under load beside its splice tests' flakes, the same free-port helper fix for the llm, streaming, webrtc and MCP SDK tests, and Safari's player by hand. Cluster management (decision 033): I140–I150 and I153 done on 2026-11-06 and 2026-11-07, each with decisions pending review in its plan section, and the open questions for review in the track's section; I151 (upgrading members' binaries) is a question, not a plan. Sync (decision 036, planned 2026-11-08): I163–I194, pipelines from producers to consumers (files, MySQL/MariaDB/TiDB, PostgreSQL, ClickHouse, Elasticsearch, MongoDB, Kafka, Redis, HTTP, S3) with checkpoints saved only after the sinks acknowledge, and WASM plugins; I163 (records, pipelines and checkpoints, package `sync`) done on 2026-11-08, with its planning decisions and those made building it pending review in decision 036 and its plan section; I164 (built-in transforms: `filter`, `route`, `fields`, `key`, `cast`, `mask`, `envelope`) done on 2026-11-08, its decisions pending review in its plan section; I165 (WASM plugins for sync) is next. |
**The 2026-10-27 benchmark pass.** [The plan and its analysis](../bench/results/plan-2026-10-27/analysis.md) ran 232 runs on the benchmark host and kept one optimization: fib `d4319879` recycles HTTP/3 request streams whose body did not stream, measured at +17.6%, +19.2% and +20.0% at 500, 5,000 and 10,000 in flight, with CPU a request down 14-16% and h1/h2 unchanged. The read-buffer start (W4 proxy-download -4.9%), the sendfile download (-13.9%) and the fit-only HTTP/1.1 change were measured and rejected; their numbers are in the analysis.

**M0 is done.** Its exit criteria hold, with one caveat: `fit run -c fit.json` serves a fixed
response on HTTP/1.1 and HTTP/2 (h2c; HTTP/2 over TLS comes with I09), and baselines for nginx,
Caddy and Traefik are checked in, but they were measured on Docker Desktop on a Mac, not on a Linux
host; see I08. R07b measured M0's fit on the Linux host: within 1.3% (HTTP/1.1) and 4.2% (h2c) of
today's fixed throughput.

**M1 is done.** I23's conformance runs and benchmarks are in the
[M1 report](https://github.com/lesismal/fit/blob/3658ae9/bench/results/m1-report-2026-10-19.md): h2spec passes everywhere, and on a Linux host
fit meets every M1 target in the roadmap, with its proxy above Caddy and Traefik but at 83% of
nginx's throughput over HTTP/1.1.

**M2 is done.** Its three exit tests pass in CI, in a job of their own (I35); CI's full race test
jobs pass again since `TestStuckClientWithoutStallTimeout` waits on progress, not a fixed time (below).
Its benchmarks are in the [M2 report](https://github.com/lesismal/fit/blob/3658ae9/bench/results/m2-report-2026-10-20.md): fit still meets every
M1 target, static files are 1.3 to 1.8 times as fast as at M1, and the proxy is at 86% of nginx over
HTTP/1.1 (83% at M1) and 94% over h2c. **M3 is done.** Its exit tests are `make m3-exit` and a CI job of their own (I55): no middleware or
handler waits on a worker (`test/workers`), each has a benchmark, WebSocket, gRPC and TCP/UDP
proxying hold back their fast side for a slow peer, and one port serves HTTP/1.1, HTTP/2, TLS passed
through by SNI and SOCKS. Its benchmarks are in the [M3 report](https://github.com/lesismal/fit/blob/3658ae9/bench/results/m3-report-2026-10-22.md):
fit still meets every M1 target, the proxy is at 94% of nginx over HTTP/1.1 (86% at M2) and 95% over
h2c, and an idle WebSocket costs it 5.5 KiB against nginx's 17.8. **In M4, I56, the provider framework
and the effective config, I57, the Docker provider, I58, the Gateway API's HTTPRoute, I59, its other routes, HTTPS listeners and status, I60, Kubernetes Ingress, I61, the Gateway API's conformance suite, whose core HTTP profile fit passes, I62, the Consul and etcd providers, I63, certificates shared in Redis, etcd or Consul, I64, the read-only dashboard, I65, OpenTelemetry traces and metrics over OTLP, and I66, packaging and examples, are done, and M4's exit criteria hold: fit passes the Gateway API's conformance suite for the core HTTP profile (I61), and the Docker Compose example routes by labels and follows its containers as they start and stop (I66).** **M4 is done.** Its benchmarks are in the [M4 report](https://github.com/lesismal/fit/blob/3658ae9/bench/results/m4-report-2026-10-23.md): fit still meets every M1 target, the data plane measures as at M3 but for h2c, where an A/B of M3's build against M4's finds fit 1.5% to 1.8% slower, built up over I59–I64 with no change to the request path (recorded below), and idle connections cost it 0.1 to 0.5 KiB more at 10k. **In M5, I67, the HTTP cache's semantics and memory tier, I68, its disk tier, and I69, its locks, stale answers and purges, are done: one fetch for a stampede, and on the harness's cache scenarios fit keeps what nginx's `proxy_cache` keeps, at about twice its rate over HTTP/1.1. I70, `fit build`, builds fit with modules of other modules, or without its providers. I71 runs WebAssembly plugins in wazero, on a pool of their own, and I72 Proxy-Wasm filters' request phase. I73 takes configs in YAML, and I74 converts nginx's. I75 serves PHP through FastCGI.** **M5 is done.** Its benchmarks are in the [M5 report](https://github.com/lesismal/fit/blob/3658ae9/bench/results/m5-report-2026-10-23.md): fit still meets every M1 target, an A/B of M4's build against M5's finds every scenario within noise (0.7% at most), the cache answers as nginx's `proxy_cache` does at 2.1 times its rate over HTTP/1.1 and 5.4 times over h2c, and idle connections cost what they did at M4. **1.0's iterations are done**: I76 fuzzes eleven targets in CI, daily too, with their corpora committed; I77 freezes the config and the Admin API as v1, with [the compatibility policy](compatibility.md), the schema's version and deprecated names for six renamed fields; I78 adds [getting started](getting-started.md), [the config reference](reference/config.md), generated from the code, and migration guides from nginx, Caddy and Traefik. What 1.0 still asks for is the external security review, outside the code, and fib's `main` taking the branch fit depends on.
M2 has I24 and I25, the Admin API with config versions, patches, ETags and rollback, I26, graph
draining and shared pools, I27, the file provider, I28, the certificate manager, its storage and the ACME decision, I29, certificates obtained and renewed with ACME through HTTP-01, I30, TLS-ALPN-01 and OCSP stapling, I30b, HTTPS by default, I30c, renewal by ARI, I31, on-demand TLS, I32, active health checks, I33, load
balancing v2, I34, Prometheus metrics, I35, its exit tests, I35b, certificate files watched, and
I35c, on-demand names dropped when idle. R07f profiled the proxy's CPU per request: what is left
to gain is fib's, asked as A20–A22.
R01 and R02, the review fixes that came before M2, are done, and so are R07a and R07b, the part of the
hot path work that needs nothing from fib, and R03–R06: fib has the APIs, and the proxy's downloads, the
static handler and the limits use them, sendfile included since R05b, and R07c took the hot path's
costs to fib. The M1 review fixes are done.

**Review against the rules (2026-10-19).** M0 and M1 were reviewed against the
[development rules](rules.md). The proxy's download path and the static handler wait on fib workers
without a time limit (reproduced: one client that stops reading stalls every request sharing its
h2c upstream connection), and several limits have no safe default. The fixes are iterations
R01–R07 in the [development plan](dev-plan.md#m1-review-fixes); what fit needs from fib is in
[fib-api-requests](fib-api-requests.md). **R01 and R02 change fit alone and come before M2**, so
before I24 and I32.

To continue, start a session with:

> Implement iteration I62 from docs/dev-plan.md. Read docs/progress.md, docs/rules.md, the index
> and that iteration's section in docs/dev-plan.md, and the design principles in docs/roadmap.md;
> then only the packages it lists. Keep the change within the iteration's scope, add tests, and when
> done mark it done in both dev-plan files and record it in both progress files.

## Code as built

| Package | Contents |
| --- | --- |
| `adapter`, `adapter/yaml`, `adapter/nginx`, `adapter/caddyfile` | adapters from other formats to native JSON, by name, extension or file name, with errors mapped to the source; YAML, both ways; common subsets of nginx's configs and Caddy's Caddyfile, what they leave out reported by line |
| `cmd/fit` | fit's main package: `cli.Main` with every module and provider imported |
| `cli` | the CLI: `run` (with reload on SIGHUP, the Admin API and, with `-watch`, a change to the file, and the config's providers, by their registered kinds), `validate`, `adapt`, `schema`, `build`, `version`, `hash-password`, `help` |
| `plugin/build` | `Build`: the main module of a fit with more modules or fewer, generated and built with the go command; `ParseModule`, `Imports`, `MainSource` |
| `admin` | `Configs`, the one path every new config takes to the running server (load, compile, `listener.Server.Reload`), one at a time, with the history of configs applied, each with an ETag; `Update`, a change made from the running config under the same lock, with If-Match; `Rollback`; the Admin API on net/http: `GET/PUT/PATCH/DELETE /config[/<pointer>]`, `POST /load`, `POST /rollback/{n}`, `GET /versions`, `GET /versions/{n}`, `GET /metrics`, which `WriteMetrics` writes; `AddProvider` and `Provide`, which merge providers' parts into the base config on every apply; `GET /config/effective`; `GET /status` (listeners, connections, upstreams and backends' health, certificates); `GET /cache` and `POST /cache/purge`; the dashboard, embedded, at `/dashboard/`; `UseStore`, `SplitAdmin` and `WithAdmin`, for a config kept in a store; the console (`StartConsole`), with its pages embedded; `admin/store`, the fitstore format |
| `config` | the native config types, `providers` among them; a strict JSON parser and decoder with error paths and positions, and renamed fields' old names taken with warnings (`Config.Warnings`); `SchemaVersion`; `Value`; `Duration`; semantic checks |
| `module` | the module registry; `Handler`, `Middleware`, `Matcher`, and provisional `Service` and `Upstream` interfaces; `Provisioner`, `Cleaner`; `Env`, which gives `Provision` the config's upstream pools |
| `sync` (package `fitsync`) | the sync role (decision 036): `Record` and its typed `Value`s with a canonical JSON form; `Source`, `Sink`, `Transform` and `Confirmer`; `Runtime` with a bounded queue, batching sinks, a watermark of acknowledged positions that never splits a transaction, the checkpoint file, retries, `on_error` and reloads; the test connectors `generate`, `memory` and `stdout` |
| `handler/respond` | the `respond` handler |
| `handler/forward` | the `forward_proxy` handler: CONNECT over HTTP/1.1, HTTP/2 and HTTP/3, fib's `ConnectProxy` with `l4.Rules` |
| `handler/proxy` | the `proxy` handler: forwards to a named upstream, drops hop-by-hop fields, sets `X-Forwarded-*` and `Forwarded` with a trusted-proxy list; streams both directions without waiting, paced by `HoldBody` and `OnDrain` up and `WriteNoWait` and `OnWritable` down; WebSockets joined to their upstream's tunnel; gRPC calls with `TE: trailers`, `grpc-timeout` as a deadline and gRPC statuses on failure |
| `middleware/rewrite` | the `rewrite` middleware (strip prefix, regexp, add prefix) and `redirect` |
| `middleware/limit` | `rate_limit` (token buckets by client IP, header or route, keys bounded), `concurrency_limit`, `body_limit` |
| `middleware/auth` | `jwt` (JWS verified with the standard library, keys from PEM files, a secret or a JWK set fetched by the control plane, verified tokens kept); `forward_auth` (a subrequest to an auth service through fib's asynchronous client); `basic_auth` (bcrypt hashes checked on a bounded pool of their own, a password that passed remembered as a keyed SHA-256) and `client_cert` (a verified client certificate required or optional, its subject passed on); `UserOf` |
| `middleware/breaker` | `circuit_breaker`: closed, open and half-open on a route's error and slow-response ratios |
| `middleware/cache` | `cache`: a shared HTTP cache by RFC 9111, responses kept as they go through a body filter, answered with `Age`, variants by `Vary`, 304s, request directives, invalidation, `Cache-Status`; the memory tier, zones of 16 shards with an LRU each, kept across reloads; the disk tier, a file a response and an index in memory, read and written on a pool of its own, read back at start; cache locks, requests of the cache's own through a stream of its own, stale-while-revalidate and stale-if-error, purges by URL, tag or all (`Purge`, `Zones`) |
| `plugin/wasm` | `wasm`: WebAssembly plugins in wazero, on a pool of instances with a queue and a watchdog, an ABI of fit's own for fields and answers, and Proxy-Wasm's request phase; `guest`, the Go side of fit's ABI for wasip1 |
| `middleware/compress` | `compress`: zstd, brotli and gzip through fib's body filters, streaming, encoders pooled and bounded |
| `middleware/headers` | the `headers` middleware: request and response fields deleted, set and added |
| `handler/fastcgi` | `fastcgi`: a FastCGI client of fit's own over fib connections, pooled and bounded, streaming both ways; PHP's parameters, split path info, try_files |
| `handler/static` | the `static` handler: files under a root, with a cache of what paths resolve to and of small files' contents, every file operation on a bounded file pool, large files through `SendFile`; validators, conditional and range requests, precompressed twins, `try_files`, directory listings |
| `modules/standard` | imports every module fit ships with |
| `runtime` | `Compile(config, Env)` to an immutable `Graph` with a router per listener and a pool per upstream, sharing the previous graph's pools for unchanged upstreams; `Runtime` with atomic swaps, graph reference counts, and a drain timeout for graphs swapped out |
| `listener` | binds a graph's listeners on one fib engine, TCP and, for HTTP/3, UDP; a listener of more than one of HTTP, tcp and socks behind fib's `mux`; dispatches each request to a route of the current graph; `Reload` to a new graph, keeping unchanged listeners and draining removed ones; graceful shutdown |
| `l4` | layer 4: `Service`, which proxies a tcp listener's connections to an address of its upstream, dialed through fib's engine, each side's data sent on from its callback, its reads held while the other side has 64 KB unsent; idle timeout, `max_conns`, drained with its graph; `Clients`, the listener handler's callbacks once a service has a connection; `Entry`, a listener's services by SNI, handing a TLS connection no service names to the listener's own TLS when it has one (`HandOff`); `Rules`, who may use a forward proxy and where to, a host name decided by the address it resolves to once connected; `NewSOCKS`, fib's SOCKS server with them |
| `upstream` | `Pool`, which picks a backend by the upstream's sticky cookie and policy (round robin, smooth weighted round robin, least connections, random, P2C, consistent hashing on the client's IP, the URI, a header or a cookie), or the backend a session is kept on (`PickBackend`), and sends requests to it over HTTP/1.1, h2c or h2 through fib's asynchronous client, and drains before it closes; passive health (`max_fails`) and active health checks, HTTP and TCP probes on timers, sent through fib's asynchronous client and dialer; `StartEngine` for the engine pools dial through |
| `provider` | `Provider`, a source of config that sends parts; `RegisterKind` and `LookupKind`, the kinds a binary has, which `provider/standard` imports; `Run`, which runs providers and applies their latest parts one at a time; `Parse` and `Merge`, which merge parts into the base config by decision 009's rules, recording each element's source and what was shadowed, and map errors back to where they were written |
| `provider/docker` | the Docker provider: the Engine API over plain HTTP (events, then the containers listed), container labels turned into upstreams, routes and services, replicas sharing an upstream |
| `provider/labels` | routes, services and an upstream's settings read from labels under a prefix, as Docker's labels and Consul's tags are, and the part they make |
| `provider/consul` | the Consul providers on net/http with blocking queries: `Catalog`, services tagged for fit turned into upstreams of their passing instances, with the routes and services their tags write; `KV`, parts from the keys under a prefix |
| `provider/etcd` | the etcd provider: etcd's v3 JSON gateway, a watch on the prefix then the keys read, parts from them, users authenticated, endpoints in turn |
| `provider/internal/watch`, `provider/internal/kvpart` | `Follower`, the loop that keeps a watch, reads the source after changes and every interval and backs off; the part made from keys holding parts |
| `provider/kubernetes` | the Kubernetes provider: a client of its own on net/http (service account or kubeconfig), list-and-watch informers per kind (at the first version served), the Gateway API's GatewayClasses and Gateways (HTTP, HTTPS with certificates from Secrets written to files, TLS passthrough, TCP, UDP) and their HTTPRoutes, GRPCRoutes, TLSRoutes, TCPRoutes and UDPRoutes turned into listeners, routes, services and upstreams of Services' endpoints; statuses written back as merge patches; with `ingress`, Ingresses of fit's IngressClasses and a few annotations |
| `provider/file` | `Watcher`, which polls the config file and applies it once it has stopped changing and its contents differ from what was applied last |
| `observe` | the access log: one JSON line per request, buffered in shards with a lock each and written by a goroutine of its own, dropping lines rather than blocking; `Track` and `EntryOf` for a request's entry; metrics: `Histogram`, `RequestMetrics` per listener and route, sharded atomic counters, kept in `Metrics` while a graph uses them, and `TextWriter` for Prometheus's text format; `Tracer`, W3C trace context and sampling, server spans written as OTLP JSON into bounded shards and exported over OTLP/HTTP; `MetricWriter`, `OTLPWriter` and `MetricsExporter`, metrics over OTLP |
| `tlsmgr/sharedstore` | `Store`, a `tlsmgr.Storage` in Redis (RESP2), etcd (JSON gateway) or Consul (KV), with leased locks renewed while held |
| `internal/apiclient` | the HTTP client of services' APIs, with TLS files, and bounded reads, for the providers and the shared storage |
| `tlsmgr` | `Manager`, which picks a handshake's certificate behind `GetCertificate`; `ClientAuth`, the authorities a listener verifies client certificates against; `Static`, certificates from files indexed by name; `ServerConfig`, a listener's TLS configuration: TLS 1.2+, AEAD suites, ALPN from its protocols; `Storage` and `FileStorage`, what ACME keeps, with locks between instances; `ACME`, which obtains certificates through HTTP-01 or TLS-ALPN-01 and renews them on goroutines of its own; `OCSP`, which keeps and staples OCSP responses; on-demand TLS (`ACME.OnDemand`), with an ask URL and limits; `Certs`, a listener's Manager of files and names obtained with ACME |
| `router` | `Router[T]`: picks a route by host (exact, `*.` for one label, `**.` for one or more) and path from maps, then by its matchers (`Methods`, `Headers`, `Query`, `RemoteIP`, `PathRegexp`, and expressions of them, `ParseExpr`), by priority, specificity and how many matchers it has, without allocating |
| `schema` | JSON Schema generation, with the schema's version and renamed fields' old names as deprecated; `fit.schema.json`, kept up to date by a test; `Reference`, the config reference in Markdown, `docs/reference/config.md` |
| `docs` | the documentation; `TestLinks`, which checks every relative link in the repository's Markdown |
| `internal/certtest` | self-signed certificates for tests |
| `internal/pebbletest` | runs Pebble, the ACME test server, and its DNS server for tests; skips them without the binaries |
| `internal/filepool` | `Pool`, goroutines of their own for file work, bounded by workers and a queue, jobs expiring past a timeout: the static handler's and the cache's disk tier's |
| `internal/fibtest` | runs fib handlers in tests without a network, as fib's servers run them, waiting for one that answers later: `Serve`, `ServeRequest` |
| `test/e2e` | builds the fit binary and drives `fit run` over the network |
| `bench` | the benchmark harness: `cmd/fitbench`, `cmd/idleconn`, configs for the four servers, results |
| `deploy` | `Dockerfile` (at the root): the container image; `deploy/helm/fit`: the Helm chart; `deploy/systemd`: `fit.service`, `fit@.service`, the sysusers file and their test |
| `examples` | `hello.json`, `proxy.json`; `docker-compose` and `kubernetes`, each with a `test.sh` CI runs; `module/hello`, a module of its own to build in with `fit build` |

A config today looks like this:

```json
{
  "listeners": [
    {"name": "web", "address": ":8080", "protocols": ["http/1.1", "h2c"]}
  ],
  "routes": [
    {
      "name": "hello",
      "listeners": ["web"],
      "priority": 10,
      "match": {"host": ["example.com"], "path": ["/hello", "/api/*"]},
      "middleware": [],
      "handler": {"module": "respond", "params": {"status": 200, "body": "hello\n"}}
    }
  ]
}
```

## Iterations

### I01 Repository skeleton and CI — done

- `go.mod` for `github.com/lesismal/fit`, Go 1.27 (fib's version), requiring fib v1.2.0.
- `cmd/fit` with `version`, which prints fit's version (settable with
  `-ldflags "-X main.version=..."`, otherwise the one Go records), fib's version from the build
  info, and the Go version and platform.
- `Makefile`: `build`, `test`, `race`, `vet`, `lint`, `fmt`, `fmt-check`, `schema`, `fuzz`, and
  `check` (fmt-check, vet, staticcheck, race tests).
- `.github/workflows/ci.yml`: gofmt; go vet and staticcheck for linux, darwin and windows; tests
  with the race detector on ubuntu and macOS; a 30-second run of `FuzzLoad`.
- `README.md` and `README.zh-CN.md`.

Differences from the plan: the packages are created as iterations fill them rather than up front,
since Go has no use for empty packages. CI has not run yet: it runs on the first push.

### I02 Config types v0 and strict loading — done

- Types: `Config`, `Listener` (name, address, protocols), `Route` (name, listeners, priority, match,
  middleware, handler), `Match` (host, path), `ModuleRef` (module, params).
- A decoder of fit's own, recorded in
  [decision 001](decisions/001-strict-config-decoding.md): unknown fields, wrong types, missing
  required fields, out-of-range numbers, values outside an enum and duplicate keys are all errors,
  every one reported (not only the first) with its path and line:column. A field whose name
  differs only in case gets a "did you mean" hint.
- `Config.Validate`: unique listener and route names, valid addresses (port 0 allowed, for tests),
  no two listeners on one address, no protocol listed twice, route listeners that exist, valid
  hosts (exact, `*.` wildcard or an IP) and paths (starting with `/`, `*` only at the end).
- A `Config` keeps what was written, without defaults, and marshals back to the same JSON; params
  keep their member order.
- Tests: table tests for every error kind, round-tripping, and `FuzzLoad`, which ran four million
  inputs without a failure.

Differences from the plan: a route has `middleware` (a list) and `handler` (one module) rather
than a single handler chain, which follows the roadmap's router → middleware → handler model and
makes the namespace of each module plain. Protocols accept `http/1.1` and `h2c` for now; I09 and
I10 add `h2` and `h3`.

### I03 JSON Schema and `fit validate` — done

- `schema.Generate` builds a draft 2020-12 JSON Schema from the config types' tags (through
  `config.Fields`, the same descriptions the decoder uses) and from the registered modules: a
  module ref's `module` is an enum of the namespace's modules, and its `params` follow that
  module's schema.
- `schema/fit.schema.json` is committed; `TestSchemaUpToDate` fails when it is stale, and
  `make schema` (or `go test ./schema -update`) rewrites it. The example config was checked
  against it with Python's `jsonschema`, which also rejected a bad config for each of its three
  errors.
- `fit validate -c file` loads the config, then compiles it, so module params are checked too;
  errors print as `file:line:col: path: message`, exit code 1. `fit schema` prints the schema.

Differences from the plan: the schema lives in a top-level `schema` package, not `config/schema`,
because it needs the module registry, which itself depends on `config`.

### I04 Module registry and core interfaces — done

- `module.Register`, `Lookup`, `Namespace`, `Load`, `Cleanup`. IDs are namespaced
  (`http.handlers.respond`); a config names the last part, and the namespace comes from where the
  name appears (`handler` → `http.handlers`, `middleware` → `http.middleware`).
- `Load` decodes params strictly into the module's struct, then calls `Provision` with a context
  whose `Errorf` points at a param; plain errors are given the params' path and position. An
  unknown module's error lists the modules the namespace has.
- `module.Handler` is fib's `http.Handler`, so fib's handlers and middleware can be reused.
  `Middleware.Wrap` is called once at compile time. `Service` and `Upstream` are marked
  provisional, to be settled in I14 and I19.
- `respond`: status (default 200, 1xx refused), headers (names and values checked) and body
  (refused on 204 and 304); the response is built once and shared, which fib allows.

### I05 Runtime graph and atomic swap — done

- `runtime.Compile` provisions every module, wraps middleware around handlers, gives each listener
  its routes (all routes, or those naming it) ordered by priority with ties in written order, and
  applies default protocols. It reports every failing module, and cleans up the modules it had
  provisioned when anything fails.
- `Runtime.Current` is lock-free: it retains the current graph with a CAS that refuses a graph
  whose count already reached zero, so a reader racing a swap never revives a graph being cleaned
  up. `Runtime.Swap` numbers graphs and releases the old one; the last `Release` cleans up its
  modules in reverse order.
- Tests, under the race detector: graphs swapped 300 times while 8 readers serve through whatever
  is current; no reader ever sees a cleaned-up module, and every graph is cleaned up exactly once.

Differences from the plan: route matching is not implemented; `Route` carries hosts and paths as
written, for I11's router.

### I06 HTTP entry points and `fit run` — done

- `listener.Start` binds every listener of the runtime's current graph as a binding of one fib
  engine, so they share one event loop, task pool and buffer pool. Each binding's handler is a
  `fibhttp.ServerHandler` around a dispatcher that takes the current graph per request, finds its
  listener by name, matches a route and calls the route's handler; it releases the graph when the
  handler returns. No route: 404. No graph: 503.
- Protocols map onto fib's HTTP config: `http/1.1` alone sets `DisableHTTP2` (which also refuses
  `Upgrade: h2c`), `h2c` alone sets `HTTP2Only`, and both serve HTTP/1.1, h2c with prior knowledge
  and `Upgrade: h2c` on the same port.
- `Server.Shutdown` is fib's `Engine.Shutdown`: listeners stop accepting, idle connections close,
  busy ones close after their response (HTTP/2 with GOAWAY); then the engine is closed.
- `fit run -c file [-shutdown-timeout 30s]` loads and compiles the config, prints one line per
  listener with the address it bound, and on SIGINT or SIGTERM shuts down, exiting 0, or 1 if the
  timeout passed first. A second signal kills the process. Config errors print as `fit validate`
  prints them, through a shared helper.
- Tests: `listener` serves two routes over HTTP/1.1 and h2c (host and path matching, 404),
  refuses the wrong protocol on single-protocol listeners, picks up a swapped graph, and fails on
  an address in use; a shutdown test holds a request in a blocking handler, checks that `Shutdown`
  waits for it over both HTTP/1.1 and h2c, that the request completes, and that the graph's module
  is cleaned up once. `test/e2e` builds the binary, runs `fit run`, gets responses over HTTP/1.1
  and h2c, sends SIGTERM with an idle connection open and expects a clean exit; a bad config exits
  1 with its error. Checked by hand with curl: prior knowledge, `Upgrade: h2c`, and SIGTERM.

Differences from the plan:

- Route matching was a linear stand-in in `listener/match.go` (exact and `*.` wildcard hosts,
  exact and prefix paths, first match in priority order, no allocations), so that routes mean what
  they say before I11. I11 replaced it with package `router`.
- The listeners are fixed when the server starts. A swapped graph changes the routes of listeners
  it shares by name; a listener it lacks answers 404, and a new one is not bound. I26 diffs
  listeners.
- A request pins the graph until its handler returns, not until its response is finished. That is
  enough for `respond`; a handler that answers later (the proxy, from I15) needs the graph held
  until the response finishes or is cancelled (`Context.OnFinish` and `OnCancel`, or a `Retain`).
- The e2e test does not cover in-flight requests, since no shipped module can hold one; the
  in-process `listener` test does. The e2e tests do not build on Windows, which has no SIGTERM.

### I07 Benchmark harness: throughput — done

- `bench/cmd/fitbench`, a Go program using the standard library and the `docker` CLI, recorded in
  [decision 002](decisions/002-benchmark-harness.md). `go run ./bench/cmd/fitbench` (or
  `make bench`) builds fit for Linux, builds the load generator image (wrk, h2load and curl on
  Ubuntu), runs each server from a pinned official image (nginx 1.31.4, Caddy 2.10.2, Traefik
  3.5.0; fit on `ubuntu:24.04`), and writes `bench/results/run-<time>.json`.
- Configs for the four servers in `bench/configs`, with the same routes on port 8080 (HTTP/1.1,
  wrk) and 8081 (h2c, h2load): `fixed`, a 1 KiB `static` file, and `proxy` to a fit backend
  answering the same fixed response.
- Before each run, curl checks status, protocol and body; then a warm-up, then the measured run.
  Each result records requests per second, latency (wrk percentiles through a Lua `done` hook that
  prints JSON; h2load mean, standard deviation and maximum), errors by kind, the server's CPU in
  cores and its memory, from its cgroup. The report records the CPU model, kernel, memory, versions,
  image digests and parameters. Containers are pinned to CPUs of their own; nginx gets one worker
  per server CPU.
- Tests: the wrk and h2load parsers on real output shapes, cgroup parsing, the CPU split, and name
  selection. The harness itself was run end to end several times, short and at full length.

Differences from the plan:

- A Go program rather than scripts, so the parsing is tested and the harness runs wherever Docker
  does.
- Traefik serves neither fixed responses nor files, so it runs only `proxy`; fit runs only `fixed`
  until I12 (`static`) and I15 (`proxy`) add theirs to `servers.go` and `configs/fit/fit.json`.
- Static files are served from a Docker volume: Docker Desktop's bind mounts held nginx to 14k
  requests a second against 350k from the volume.
- Every proxy keeps up to 4096 idle backend connections. With their defaults, Caddy (32) and
  Traefik (200) churned backend connections under 256 or 2048 requests in flight until ports ran
  out: Caddy fell to 1,600 requests a second and answered 502.
- It has been run on Docker Desktop (bridge network), not yet on a Linux host with
  `-network host`, which is the path published numbers should take.

### I08 Benchmark harness: idle connection memory and baselines — done

- `bench/cmd/idleconn` opens N connections, 64 at a time, makes one request on each and holds them
  until its standard input closes: `http1` (keep-alive after a 404 for `/idle`), `tls` (the same
  over TLS on 8443) and `ws` (a WebSocket the server proxies to a backend). `idleconn -serve-ws` is
  that backend.
- `fitbench -idle http1,tls,ws` measures each kind on a freshly started server: anonymous memory
  from the server's cgroup before the connections open and after they have sat for
  `-idle-settle`, divided by the number of connections. The TLS listeners use a throwaway ECDSA
  P-256 certificate the harness writes.
- [`bench/results/baseline-2026-10-19-macos-docker.json`](https://github.com/lesismal/fit/blob/3658ae9/bench/results/baseline-2026-10-19-macos-docker.json),
  with the defaults (30-second runs, 256 wrk connections, 64×32 h2load streams, 10,000 idle
  connections). `bench/README.md` says how to reproduce it.

Requests per second, and anonymous memory per idle connection (10,000 of each kind):

| | nginx | Caddy | Traefik | fit |
| --- | ---: | ---: | ---: | ---: |
| fixed, HTTP/1.1 | 811,923 | 249,350 | — | 1,018,822 |
| fixed, h2c | 914,776 | 163,137 | — | 3,651,820 |
| static, HTTP/1.1 | 724,292 | 155,336 | — | — |
| static, h2c | 663,958 | 112,823 | — | — |
| proxy, HTTP/1.1 | 170,101 | 97,915 | 142,490 | — |
| proxy, h2c | 158,202 | 69,446 | 85,954 | — |
| idle HTTP/1.1 | 531 B | 21,733 B | 22,812 B | 956 B |
| idle TLS | 29,943 B | 51,561 B | 51,928 B | — |
| idle WebSocket (proxied) | 22,281 B | 131,578 B | 130,963 B | — |

Run to run on this machine, the HTTP/1.1 numbers move by up to a quarter (nginx's fixed response
gave 1.11M and 0.81M in two full runs, with a few dozen wrk timeouts in the slower one); the h2c
and memory numbers move by a few percent. nginx's memory before any connection is 176 MB, its
preallocated connection tables; Caddy's is 13 MB, Traefik's 22 MB and fit's 9 MB.

Differences from the plan:

- Memory is the cgroup's anonymous memory rather than RSS: it covers every process in the
  container without counting shared pages twice, and leaves the page cache out.
- The baseline was measured on Docker Desktop on an Apple M3 Max (14 CPUs to Docker, bridge
  network), the only machine this session had. The ratios between servers are meaningful; the
  absolute numbers are not what a Linux host would give. Re-run it on a dedicated Linux host and
  check that in as the baseline to publish.
- The roadmap's WebSocket and HTTP/3 load generators are not in M0; see decision 002.

### I11 Router v1: host and path — done

- Package `router`: `New` builds a `Router[T]` from routes (hosts, paths, priority and a value
  of any type), and `Match(host, path)` returns the value of the route that serves a request, or
  false, which the listener answers with 404. Hosts are exact or `*.` wildcards (one label),
  ignoring case, a port, IPv6 brackets and a trailing dot; paths are exact or `*` prefixes; a route
  with no hosts or no paths matches all of them.
- When several routes match, the rule is: the highest priority; then the most specific host (exact,
  then wildcard, then none); then the most specific path (exact, then the longest prefix, then
  none); then the route written first. Host before path follows nginx and Caddy, which pick the
  server or site by name before looking at the path. The rule is in the package doc and in the
  `priority` field's description, so the schema says it too.
- How it is built: a map from exact host and one from wildcard suffix (`.example.com`) to a node,
  plus one node for routes with no hosts. A node keeps exact paths in a map and prefixes in a map
  per prefix length, tried longest first. A lookup is at most three host lookups, each one map
  lookup for the exact path and one per distinct prefix length, stopping as soon as nothing left
  can beat what was found. A mixed-case host is lower-cased into a stack buffer and looked up
  through `m[string(b)]`, which Go does without allocating.
- `runtime.Compile` builds a router for each listener (`Listener.Router`) from its routes;
  `Listener.Routes` keeps the routes in priority order, as before. `listener`'s dispatcher calls
  `Router.Match` with the request's `Host` and `URL.Path`; `listener/match.go` and its tests are
  gone.
- Tests: table tests for hosts and paths, and for ordering and ties (priority over specificity, a
  lower priority still serving what a higher one does not, exact host over wildcard over none, host
  over path, exact path over longer prefix over shorter prefix over none, ties to the route written
  first, a later route with a higher priority replacing an earlier one for the same pattern, and
  each pattern of a route matched on its own); `TestMatchAllocs` checks with `AllocsPerRun` that
  `Match` allocates nothing, for lower- and mixed-case hosts, wildcards, IPv6 and misses, on a
  router of 103 routes. `BenchmarkMatch`: about 60 ns and 0 allocations a lookup with 101 routes
  (Apple M3 Max). `runtime`'s test checks each listener's router picks among its own routes, and
  `listener`'s serve test now has a wildcard route written last that beats a route with no host,
  over HTTP/1.1 and h2c. `go vet ./...` and `go test -race ./...` pass.

Differences from the plan:

- Equal priorities used to go to the route written first; they now go to the most specific route,
  as the plan says. A config whose broader route came first, at the same priority, now has its
  narrower route win.
- `Router` is generic over the route's value, so that `runtime` can build it without `router`
  importing `runtime`. Host and path matching is built into the router rather than made of
  `module.Matcher` modules, which stay unused until I36 adds the matchers that need a request.

### I12 Static files I — done

- Package `handler/static`, module `static`, with params `root` (required), `index` (default
  `["index.html"]`, `[]` for none), `strip_prefix`, `max_file_bytes` (default 1 MiB) and
  `max_cache_bytes` (default 64 MiB). Registered in `modules/standard`; the schema is regenerated.
- A request: GET and HEAD only, else 405 with `Allow`; `strip_prefix` removed (404 when the path
  lacks it); a path with a `..` segment, a NUL or a backslash gets 400; the rest is cleaned and
  joined to the root, and stat'ed. A directory asked for without a trailing slash is redirected
  (301, query kept) to the path with one; with one, the first index file that is a regular file is
  served, else 404. A file asked for with a trailing slash, or anything not a regular file, is 404;
  a stat refused for permissions is 403.
- Files up to `max_file_bytes` are kept in memory while `max_cache_bytes` allows, with a header
  built once (`Content-Type` from the extension or sniffed, `Last-Modified`, `ETag`,
  `Accept-Ranges`) and shared by every response. A request without conditions or a range leaves
  through `WriteResponse` with that header and the cached bytes; one with them goes through
  `net/http.ServeContent`, which answers `If-None-Match`, `If-Modified-Since`, `If-Match`,
  `If-Unmodified-Since`, `Range` (multipart too) and `If-Range`. A larger file, or one past the
  cache's room, is served from disk through `ServeContent`. The cached copy stands while the
  file's size and modification time do; a file that is gone is dropped from the cache.
- `internal/fibtest.ServeRequest` runs a handler for a request built by the test, and finishes a
  response written through `Write` when the handler returns, as the server does.
- Tests: `handler/static` covers MIME types (by extension and sniffed), HEAD, empty files, escaped
  names, index files in order, redirects with and without a query, 404s, 405 with `Allow`; the
  traversal attempts `/../`, `/a/../../`, `%2e%2e`, `..%2f`, `..%5c`, `%00` and `/..` all get 400
  and never the file outside, while a name like `..dots` is served; every conditional and range
  case above, from memory and from disk; a changed file served anew with a new `ETag`, a removed one
  404 and dropped; the cache's limits; `strip_prefix`; `"index": []`; 403; and the params' errors.
  `test/e2e` serves a small and a 1 MiB file from the built binary over HTTP/1.1 and h2c, with
  HEAD, 304, ranges, an index, a redirect and a raw `..` path. `BenchmarkServeCached`: about
  1.8 µs, 672 B and 8 allocations a request for a cached 1 KiB file, the test context included
  (Apple M3 Max). `go vet ./...` and `go test -race ./...` pass.

Differences from the plan:

- The handler does not use fib's `http.FileCache`, which in v1.2.0 sends no `ETag` and answers
  the request itself, so knows nothing of index files and writes its own 404; see
  [decision 003](decisions/003-static-file-cache.md). The handler stats the file on each
  request, as nginx does by default, where `FileCache` on Linux would not.
- Any `..` segment is refused with 400, even one that would stay inside the root, as nginx
  refuses a path that climbs above the root; symbolic links are followed wherever they lead, as
  nginx and Caddy do by default.

### I14 Upstream pool v1 (HTTP/1.1) — done

- `config.Duration`: a length of time written as a string `time.ParseDuration` takes (`"10s"`,
  `"1m30s"`), never negative. The decoder and the schema know it as they know `Value`; the schema
  gives it a pattern, which a test checks against the decoder.
- Package `upstream`: `Config` has `addresses` (required, `host:port` each), `max_conns` (per
  address, no limit by default), `max_idle_conns` (per address, 32 by default, 0 keeps none),
  `idle_timeout` (60s) and `connect_timeout` (10s). `Config.Check` refuses an empty list, a URL
  (with a hint), a missing host or port, a port out of range and a repeated address, each with its
  path and position.
- `New(engine, config)` makes a `Pool` around a `fibhttp.Client` on a running engine, with the
  limits mapped onto `MaxConnsPerHost`, `MaxIdleConnsPerHost`, `IdleConnTimeout` and
  `DialTimeout`, and no overall `Timeout` (I17). fib's client already pools per `host:port`: it
  reuses keep-alive connections, queues requests past the cap, keeps some idle and closes them on
  a timer, so the pool adds the config, the defaults and the choice of address. `Pool.Do` takes the
  addresses in turn (an atomic counter, no lock), sets the request URL's scheme and host, leaves
  its `Host` alone, and hands it to the client, whose callback gets the response or the error.
  `Close` closes the idle connections and fails the waiting requests.
- Tests, against `httptest` backends that count their connections: 20 requests in turn over one
  connection; `max_idle_conns: 0` opens one per request; with `max_conns: 2` and two requests held
  by the backend, six more wait, no third connection opens, and all eight finish with at most two
  connections and two requests in flight; `idle_timeout: "100ms"` closes the idle connection and
  the next request opens a new one; `Close` closes the idle connection and refuses requests; two
  addresses take five of ten requests each over one connection each; a refused connect and a
  connect to TEST-NET-1 with `connect_timeout: "200ms"` fail promptly; and every config error with
  its message and position. `config` and `schema` test `Duration`. Ten runs of the package under
  `-race` pass; `go vet ./...` and `go test -race ./...` pass.

Differences from the plan:

- The upstream config is not in the config file yet: `upstream.Config` is a type the decoder
  takes, ready for I15 to place, either as a top-level `upstreams` list named by routes (as
  nginx's `upstream` blocks and Traefik's services are) or inline in the proxy's params (as
  Caddy's are). The schema is unchanged until then.
- `New` takes the engine to dial through. Where it comes from is I15's to settle: the listener
  creates the engine after the graph is compiled, so either the graph's pools are made once the
  engine exists, or a client engine of its own serves them. Sharing the server's engine keeps a
  proxied request on the event loops it arrived on, as nginx does.
- The pool spreads requests over several addresses in turn already, since one address per
  upstream would make `addresses` a list of one; I19 replaces that with its selection interface.
  `module.Upstream` and `module.Service` stay provisional until then.

### I15 Reverse proxy I: forwarding and headers — done

- Config: a top-level `upstreams` list of `config.Upstream` (`name` plus I14's fields), checked by
  `Config.Validate` (names, and the address checks that were `upstream.Config.Check`). The type
  moved from `upstream` to `config`, which `upstream` imports. See
  [decision 004](decisions/004-upstreams-and-async-handlers.md).
- `runtime.Compile(c, Env)` makes a pool per upstream through `Env.Engine` before provisioning the
  modules, passes them in `module.Env` (`module.Load` takes it; `Context.Upstream(name)` finds
  one), and closes them after cleaning up the modules. `fit run` starts a client engine with
  `upstream.StartEngine` and stops it after the graph goes; `fit validate` compiles with no engine,
  whose pools fail every request. `Pool.Close` now drains: it closes the client once the requests
  in flight have had their callbacks.
- Package `handler/proxy`, module `proxy`, params `upstream` (required), `trusted_proxies` (IPs and
  CIDRs) and `host_header`. It builds the upstream request from the client's method, path (escaped
  as received), query, headers and body (`c.Body()`, which fib reads whole by default), retains the
  request, calls `Pool.Do`, and in the callback writes the status, headers and body back, then
  releases. Hop-by-hop fields (`Connection` and what it names, `Keep-Alive`, `Proxy-Connection`,
  `Proxy-Authenticate`, `Proxy-Authorization`, `TE`, `Trailer`, `Transfer-Encoding`, `Upgrade`) are
  dropped both ways. The client's `Host` goes upstream unless `host_header` is set. No
  `User-Agent` is made up for a client that sent none. A client in `trusted_proxies` has itself
  appended to `X-Forwarded-For` and `Forwarded` and its `X-Forwarded-Proto` and `X-Forwarded-Host`
  kept; anyone else has all four replaced. `Forwarded` quotes IPv6 addresses and hosts with a port.
  A failed upstream request gets 502.
- Tests, through a real listener and client engine: method, an escaped path and query, the `Host`,
  repeated and custom headers and an 8 KB body reach the upstream over HTTP/1.1 and h2c, and the
  status and headers come back; hop-by-hop fields are dropped from a request and, with a raw
  backend, from a response; untrusted and trusted forwarded fields; `host_header`; 502 for a
  refused connect; HEAD keeps the upstream's `Content-Length`; 64 concurrent requests over both
  protocols; `Forwarded` elements for IPv4, IPv6 and unknown; config errors. `config` tests the
  upstream checks, `upstream` the draining and the engineless pool, `cmd/fit` validates
  `examples/proxy.json`, and `test/e2e` proxies through the built binary. Five runs of the proxy
  tests under `-race` pass; `go vet ./...` and `go test -race ./...` pass.

Differences from the plan:

- The plan's "one upstream" is a named upstream from the config's `upstreams`, which may have
  several addresses taken in turn (I14).
- `TE` is dropped like the other hop-by-hop fields, `TE: trailers` included; gRPC (I49) needs it
  kept, with trailers (I17).
- The proxy holds the request with `Retain`, not the graph; decision 004 explains why the pool's
  draining is enough.

### I21 Access logs — done

- Config: a top-level `access_log` with `output` (`"stdout"`, the default, `"stderr"` or a file
  appended to), `flush_interval` (1s) and `buffer_bytes` (4 MiB). `fit run` opens it, hands it to
  the graph through `runtime.Env.AccessLog`, and closes it after the graph, which writes out what is
  buffered. `fit validate` does not open it.
- Package `observe`: `AccessLog.Log` formats one JSON line (time in UTC with milliseconds,
  listener, route, client IP, proto, method, host, URI, status, bytes in and out, duration,
  upstream and upstream duration, user agent, referer) into a stack buffer and appends it to the
  log's buffer under a short lock; it allocates nothing. A goroutine of the log's own swaps the
  buffer out and writes it every `flush_interval`, or as soon as 64 KB wait. When `buffer_bytes`
  are waiting, because the output is slow or blocked, lines are dropped and counted, and the count
  goes to stderr once the output catches up: a request never waits for the disk. Strings are
  escaped by hand, invalid UTF-8 becoming U+FFFD, so every line is valid JSON.
- `listener`'s dispatcher, when the graph has a log, makes an `observe.Entry` (start, listener,
  route, bytes in), attaches it to the request's context with `observe.Attach`, and registers a
  `Context.OnFinish` hook that logs the line with the status and size fib sends. The proxy fills in
  the entry's upstream address and time before writing its response. 404s are logged too.
- Tests: `observe` checks a line's fields and escaping, IPv6 and missing fields, JSON string
  escaping against `encoding/json`, the flush interval, the flush when 64 KB wait, a blocked
  output (10,000 lines logged within a second with the writer stuck, every line either written or
  counted as dropped, the drops reported), `Close` flushing and dropping later lines, and file
  output. `handler/proxy` logs proxied and unmatched requests through a real listener over HTTP/1.1
  and h2c and checks every field; `test/e2e` runs `fit run` with a file log and finds the lines
  after SIGTERM. `BenchmarkLog`: about 416 ns and no allocation a line with 14 goroutines logging
  at once (Apple M3 Max). `go vet ./...` and `go test -race ./...` pass.

Differences from the plan:

- The plan's "a benchmark shows logging does not block workers" is shown by the blocked-output
  test and `BenchmarkLog`. A load test with logging on and off was to run on the benchmark host,
  which refused the session's ssh key; it can be run there with key-based login.
- The log belongs to the process, not the graph, so that a response finished after a graph swap
  still has a log to go to. I22 decides what a reload that changes `access_log` does, and whether
  SIGHUP reopens the file for log rotation.
- One log for every listener; per-listener or per-route logs, sampling and other formats can come
  when someone needs them. Under heavy contention the one lock shows (416 ns a line at 14
  goroutines); per-worker buffers are the fix if I23's numbers call for it.

### I16 Reverse proxy II: streaming and backpressure — done

- fit moved to fib v1.3.0 first (its own commit), which has what forwarding without waiting needs:
  `Context.HoldBody`, `BodyWriter` with `OnDrain`, and a client that sends a pushed body as the
  connection has room. fib's reference proxy, `fib-examples/gateway`, was the model.
- Listeners set `StreamRequestBody` with a threshold of 64KB (`listener.StreamThreshold`), and
  upstream pools `StreamResponseBody` with the same (`upstream.StreamThreshold`): smaller bodies
  still arrive whole and take I15's path, a single `WriteResponse`.
- Upload: a request whose body streams (`c.RequestBody() != nil`) goes upstream with a
  `fibhttp.BodyWriter` as its body and the client's `ContentLength` (-1 for chunked). The body comes
  through `c.OnBody`; each piece is written to the `BodyWriter`, which copies it and returns; past
  its limit (256KB) the callback holds the body with `c.HoldBody(true)`, which stops reading the
  client once 256KB wait there, and `OnDrain` resumes it when the upstream has taken the backlog.
  Nothing waits. `Content-Length` and `Expect` are no longer copied into the upstream request's
  header: the length is the request's own, and fib answers `100 Continue` itself.
- Download: a response whose body streams is taken with `resp.OnBody`; the first piece sends the
  status and header (with the upstream's `Content-Length` when it had one, so the client gets
  chunked only when the upstream sent it so), each piece is written with `c.Write` and flushed, and
  the last one finishes the response. The callback runs on the upstream engine's worker; when the
  client is slower, `Write` waits once 64KB are queued for it (HTTP/1) or the stream window is spent
  (HTTP/2), and while it waits the upstream connection is not read, so the upstream is slowed by
  TCP or by its own stream window. This is fib's intended pacing ("what holds the connection back
  is fn itself"), and it waits on the upstream engine's pool, never the listeners'. An upstream that
  fails mid-body closes an HTTP/1 client connection, so the truncation shows; one that fails before
  the first piece gets 502. A client that goes away mid-body ends the upstream response.
- Tests (`handler/proxy/stream_test.go`): 256MB down and 256MB up, each over HTTP/1.1 and h2c, with
  a `Content-Length` and chunked, through a real listener: downloads to a client that pauses 20ms
  after every 4MB, uploads to an upstream that does the same. Every byte is checked against a
  pattern with a prime period, so a lost, repeated or moved byte shows; lengths and framing are
  checked. The live heap (`/gc/heap/live:bytes`, sampled every 10ms) rose at most 10MB in any case,
  against a bound of 16MB; at 1GB it rose the same, so memory does not grow with the body. Under
  `-race` the tests move 64MB. `go vet ./...` and `go test -race ./...` pass.

Differences from the plan:

- The plan's `HoldReads` link is `HoldBody` and `BodyWriter.OnDrain` for the upload, which hold the
  client's reads through fib, and fib's own pacing of `Context.Write` for the download, which holds
  the upstream's. fit never calls `HoldReads` itself.
- The download direction waits in `Context.Write` on a worker of the upstream engine. fib v1.3.0
  offers no write on a `Context` that reports a full queue instead of waiting; if I23's numbers
  show many slow clients tying up the upstream engine's pool, that is the API to ask fib for.
- An HTTP/2 client whose upstream fails mid-body gets its stream finished rather than reset; I17
  handles failures properly.

### I22 Reload on SIGHUP — done

- `listener.Server` now makes its engine with no bindings and adds each listener with
  `Engine.AddBinding`. `Server.Reload(g)` moves it to a new graph: a listener whose name, address and
  protocols are all unchanged keeps its binding, its port and its connections; the others are bound
  before the swap; then `Runtime.Swap(g)`; then the listeners `g` lacks, or that changed, are drained
  with `Engine.ShutdownBinding` in the background, for at most the drain timeout (`fit run`'s
  `-shutdown-timeout`). If a listener cannot be bound, the bindings made for `g` are removed, `g` is
  released, and the running graph stays. A listener replaced on the same address (new protocols, a
  new name) is bound beside the old one, which fib's SO_REUSEPORT allows, so there is no moment with
  nothing listening; where that fails, the old one stops listening first, its connections left to
  finish. `Addrs` reports the current listeners.
- `fit run` turns SIGHUP into a reload: it loads and compiles the file with the same upstream
  engine and access log, calls `Reload`, prints `fit: reloading`, a line per listener removed and
  added, and `fit: reloaded <file> (config version N)`. A config that fails to load, compile or bind
  prints `fit: reload failed; the running config stays` and its errors to stderr. Signals that arrive
  during a reload fold into one more. A changed `access_log` is reported as taking effect on
  restart.
- Tests: `listener` reloads with a listener kept (same port, and a keep-alive connection open across
  the reload sees the new routes), one changed (protocols), one added and one removed (it stops
  taking connections), and checks the graph version; a reload with an address already taken fails,
  unbinds the listener it had bound, and leaves the old graph serving; a listener on a fixed port
  changes its protocols in place. `test/e2e` runs eight clients against `fit run` — keep-alive and a
  new connection per request, HTTP/1.1 and h2c — through a reload that changes the route and adds a
  listener, and a reload of a broken config, which leaves the running one serving and prints its
  error: about 36,000 requests, none failed, in each of five runs. `go vet ./...` and
  `go test -race ./...` pass.

Differences from the plan:

- Listeners are kept by name, address and protocols together, not by address alone, so that two
  listeners on port 0 stay apart and a listener whose protocols change is served with the new ones.
- The access log is not reopened or replaced on reload; a changed `access_log` waits for a restart.
  Reopening the file for log rotation (nginx does it on USR1) is left for when someone needs it.
- Upstream pools are made anew for each graph, so a reload drops the idle upstream connections of
  the old one once it drains. I26 can carry pools over.

### I17 Reverse proxy III: timeouts, cancellation, errors — done

- `config.Upstream` gains `response_header_timeout` and `response_idle_timeout` (60s each by
  default); `connect_timeout` was there already. `upstream.Pool.Timeouts` hands the first two to the
  proxy, which enforces them, since only it knows when a response begins and a body goes quiet.
- The proxy's request handling moved to `handler/proxy/exchange.go`. Each upstream request carries a
  context the proxy cancels with a cause: the client going away (`Context.OnCancel`), the header
  timer (started at `Do`, stopped at the response), or the idle timer (started at a streamed
  response, reset on each piece of body). Cancelling makes fib's client abandon the request and end
  a body that is streaming.
- Failures before the response begins: a header or idle timeout, a connect timeout
  (`os.ErrDeadlineExceeded` from fib's dial) and a body held past `MaxBodyHold` give 504; a client
  body that failed gives 400, 408 for its read timeout, 413 past `MaxStreamedBodyBytes`; anything
  else 502. A client that went away gets nothing. After the response began: a body with a declared
  length is finished short, which fib turns into a closed HTTP/1 connection or a reset HTTP/2
  stream; a chunked one closes the HTTP/1 connection; on HTTP/2 a body of no declared length ends
  with a `Fit-Error` trailer, as fib's gateway does, since fib has no way to reset such a stream.
- Trailers: a response's trailers are forwarded, whole (in `Response.Trailer`) or streamed (the
  `Trailer` announcement kept through the hop-by-hop filter, the values set with
  `http.TrailerPrefix` before `Finish`). A request's trailers are forwarded too: a body with
  trailers goes through the `BodyWriter`, chunked, even when it arrived whole, and the values are
  copied into the upstream request's `Trailer` once the body ends — on HTTP/2 the names are first
  known then.
- Tests (`handler/proxy/failure_test.go`, not on Windows), over HTTP/1.1 and h2c: a connect to a
  listener whose backlog is full times out to 504; a backend that never answers hits
  `response_header_timeout` (504 after about 150ms); a backend that sends 200KB and goes quiet hits
  `response_idle_timeout`, and the client can tell the body was cut short, with and without a
  length; the same for a backend that drops the connection mid-body; a client that gives up before
  the header, one that closes a download after 256KB, and one whose upload fails all have the
  upstream request cancelled (the backend sees its request context end); trailers go both ways for
  small and streamed bodies; `statusOf` maps each cause. Three runs of the package under `-race`
  pass; `go vet ./...` and `go test -race ./...` pass.

Differences from the plan:

- The timeouts live on the upstream rather than the route; I43 adds per-route ones.
- An HTTP/2 response of no declared length whose upstream fails mid-body ends with a trailer rather
  than a stream reset, which fib v1.3.0 does not let a handler ask for.

### I19 Load balancing v1 — done

- Config: `config.Upstream` gains `weights`, one per address (1 to 1000), and `load_balancing`:
  `round_robin`, `weighted_round_robin` or `least_conn`. Left out, it is `round_robin`, or
  `weighted_round_robin` when weights are given. `Validate` refuses a weight count that does not
  match the addresses, and weights with `round_robin`.
- `upstream.Balancer` is the selection interface, `Pick() *Backend`; a `Backend` is an address, its
  weight and its count of requests under way. Round robin is an atomic counter. Weighted round robin
  is nginx's smooth algorithm under a mutex: weights 5, 1, 1 give `a a b a c a a`. Least connections
  scans every backend for the fewest requests under way for its weight (compared by
  cross-multiplying, without dividing), from a starting point that moves with each pick, so that
  idle backends share the load; it reads the counts without a lock.
- `Pool.Pick` picks and counts the request under way, `Pool.Send` sends it to that backend, and
  `Pool.Done` ends it; `Pool.Do` is the three for callers that are done when the callback returns.
  The proxy picks at the start of an exchange and calls `Done` when it is over, body and all, so a
  long download counts as busy for as long as it lasts. The pool's draining (I15) now counts from
  `Pick` to `Done` too, so a pool waits for streamed bodies before it closes. The access log's
  `upstream` field is the picked backend's address.
- Tests: round robin gives `abcabc` and exactly 1,000 picks each of 3,000; weighted round robin gives
  nginx's sequence and exact shares (5,000/1,000/1,000 of 7,000; 3,000/2,000 of 5,000); least
  connections shares idle backends evenly, skips busy ones until the others catch up, and keeps
  requests under way in proportion to weights 2:1; 8 goroutines pick concurrently under `-race` for
  each policy; `AllocsPerRun` finds no allocation in any `Pick`. Through a pool, weights 3:1 send 30
  and 10 of 40 requests, and with `least_conn` an address holding a slow request gets none of the
  next 20. `config` tests the new checks. `BenchmarkPick`, 14 goroutines (Apple M3 Max): 52 ns round
  robin, 107 ns weighted, 67 ns least connections, no allocations. `go vet ./...` and
  `go test -race ./...` pass.

Differences from the plan:

- Weights are a list beside `addresses`, as Caddy has them, rather than objects in place of the
  address strings, which keeps `addresses` as simple as I14 made it.
- Weighted least connections came for free, so `least_conn` takes weights too, as nginx's does.

### I20 Passive health checks — done

- `config.Upstream` gains `max_fails` (1 by default, 0 for none) and `fail_timeout` (10s), as
  nginx's: `max_fails` failures of an address within `fail_timeout` take it out of rotation for
  `fail_timeout`; after that it takes requests again, and one success clears its record.
- A `Backend` keeps its state beside its counters: `downUntil` (atomic, Unix nanoseconds, 0 when up)
  and, under a mutex only failures and the first success after them take, the failures in the
  current window. A pick reads `downUntil` and only reads the clock when it is set, so picking costs
  nothing more while everything is healthy. Every policy passes over backends that are down; round
  robin and least connections move their turn past them, so the load spreads evenly over the rest.
  If every backend is down, health is ignored rather than failing every request, since an address
  that may answer is better than none (nginx ignores health for a single server for the same reason).
- `Pool.Done(b, failed)` reports how the request ended. The proxy counts a failure when the
  upstream could not be reached, broke off, or timed out (502 and 504, and a response cut short);
  not when the client went away or failed to send its body (400, 408, 413). HTTP statuses are not
  failures. `Pool.Do` counts an error other than cancellation.
- Tests: two failures within the window take a backend down, a success brings it back, failures in
  different windows do not add up, `max_fails: 0` never takes it down; every policy skips a down
  backend and shares its load evenly, without allocating, ignores health when all are down, and
  takes the backends back after the window. Through a pool, an address whose port starts refusing
  costs one failed request and then gets none; the port listening again, the address rejoins after
  `fail_timeout` and takes half the requests. Through the proxy, an upstream with a refused address
  answers 1 of 20 requests with 502 and the rest from the live one. `go vet ./...` and
  `go test -race ./...` pass.

Differences from the plan:

- No retry: the request that finds an address failing still fails. Retrying it on another address
  is I43's.
- Transitions are not logged yet; metrics (I34) will report them.

### I18 HTTP/2 upstreams — done

- `config.Upstream` gains `protocol`: `http/1.1` (the default), `h2c` (HTTP/2 in the clear with
  prior knowledge) or `h2` (TLS, offering h2 and http/1.1 through ALPN, so a server that does not
  choose h2 is still reached), with `tls_server_name` and `tls_ca_file` for `h2`. `Validate` refuses
  the TLS fields without `h2`; `upstream.New` now returns an error, for a CA file that cannot be read
  or holds no certificate, which `Compile` reports with its position (and stops there, rather than
  also reporting every route that names the upstream).
- The pool sets fib's `UnencryptedHTTP2` for h2c, and a `tls.Config` (TLS 1.2 at least, the CA file
  in place of the system roots when given) for h2, whose requests go to `https://`. fib's client
  multiplexes requests over each HTTP/2 connection up to the server's `SETTINGS_MAX_CONCURRENT_STREAMS`
  and opens another past it, within `max_conns`.
- Proxy fixes the h2 tests found: an absent `User-Agent` is now sent as a present, empty field, which
  both fib's HTTP/1 and HTTP/2 clients take as "send none" (`[""]` made the HTTP/2 client send an
  empty one). A request's announced trailers are read from its `Trailer` header too, since an
  HTTP/2 client's arrive only with the end of the body, and Go's servers keep only announced
  trailers. And a race the HTTP/2 tests showed: fib's client reads a request's `Trailer` when it
  sends the header, which may be after the body has ended, so a whole body's trailers are now set
  before the request goes, and a streamed upload with trailers goes through a `trailerPipe` that
  fib's client reads on its own goroutine, filling the values in just before the end.
- Tests: the proxy tests of I15–I17 run as subtests against an HTTP/1.1, an h2c and an h2 (TLS)
  backend each — forwarding (which checks the upstream saw HTTP/2), hop-by-hop request fields,
  forwarded fields, `host_header`, 64 concurrent requests, HEAD, passive health, 256MB streamed each
  way with the heap bounded, the header and idle timeouts, an upstream that breaks mid-body (now an
  `http.ErrAbortHandler` panic, which closes an HTTP/1 connection and resets an HTTP/2 stream), client
  aborts, and trailers both ways. `config` and `upstream` test the TLS settings' errors. The stream
  tests' heap bound went from 16MB to 32MB: the heap is the test's HTTP/2 client's and backends' too.
  Two runs of the proxy package under `-race` pass; `go vet ./...` and `go test -race ./...` pass.

Differences from the plan:

- There is no per-connection stream limit of fit's own: fib's client honours the server's, and
  `max_conns` bounds the connections. fib v1.3.0 has no setting to lower it.
- The rest of upstream TLS (client certificates, skipping verification, cipher choices) is I54's.
- A streamed upload with trailers costs a goroutine, the one exception to forwarding without one; a
  `BodyWriter` that takes the trailer with its close would remove it, and is worth asking fib for.

### I09 TLS entry from certificate files — done

- Config: a listener gains `tls: {"certificates": [{"cert_file", "key_file"}, ...]}`, and protocols
  gain `h2`, HTTP/2 over TLS. A listener with TLS serves `http/1.1` and `h2` when it names no
  protocols. `Validate` refuses `h2` without TLS, `h2c` with it, and TLS without certificates.
- Package `tlsmgr` (static certificates only, as the plan says): `ServerConfig` loads the pairs and
  returns a `tls.Config` with TLS 1.2 at least, the TLS 1.2 suites cut to ECDHE with AES-GCM or
  ChaCha20-Poly1305 (TLS 1.3's are all fine and not configurable), and ALPN from the protocols
  (`fibhttp.ConfigureTLS` for both, `h2` or `http/1.1` alone otherwise); and a fingerprint of the
  certificates and the ALPN list. A certificate that cannot be loaded is a config error at its path.
- `runtime.Compile` builds each TLS listener's config, so `fit validate` checks the files too.
  `listener` wraps the HTTP handler in `fibtls.NewServer`; its HTTP config treats `h2` like `h2c`
  for allowing HTTP/2. The certificates go in `Config.Certificates`, from which fib's handshake takes
  the first that suits the client's hello, SNI first, wildcards included, and the first of all for a
  name none has: fib runs such handshakes as state machines, with no goroutine, which a
  `GetCertificate` would have given up. A listener's reload identity includes the fingerprint, so a
  reload after the certificate files changed binds the listener anew, beside the old one, and new
  connections get the new certificate.
- Tests: `tlsmgr` checks the config for each protocol set, that fingerprints differ by protocols and
  by certificate, and the errors for a missing file, a file with no PEM and a key that does not match
  (with their paths); and the validation rules. `listener` serves two certificates by SNI (exact and
  wildcard) over HTTP/1.1 and HTTP/2, gives an unknown name the first, refuses TLS 1.1, serves only
  the protocols named, and picks up a new certificate on reload. `test/e2e` runs `fit run` with two
  host names, each with its own certificate, over HTTP/1.1 and h2, one answered by `respond` and one
  proxied, whose upstream sees `X-Forwarded-Proto: https`. `internal/certtest` writes self-signed
  certificates for the tests. `go vet ./...` and `go test -race ./...` pass.

Differences from the plan:

- The certificates are chosen by fib's handshake from `Config.Certificates` rather than by a
  `GetCertificate` of fit's own; the certificate manager (I28) will need `GetCertificate` for
  certificates that change while the listener runs, and should keep the static ones on this path.
- A listener whose certificate files change is only re-read on reload; watching the files is
  I28's.

### I13 Static files II — done

- `static` gains `precompressed` (any of `br`, `zstd`, `gzip`; none by default), `try_files` and
  `browse` (off by default).
- Precompressed twins: for a file the handler would serve, it looks for `name.br`, `name.zst` and
  `name.gz`, in that order of preference among the encodings listed, ranked by the client's
  `Accept-Encoding` q values (wildcards and `q=0` refusals honoured, ties to the order listed), and
  serves the best twin on disk with `Content-Encoding`, the original's `Content-Type`, and its own
  `ETag` and `Last-Modified`, so conditions and ranges apply to that representation. With
  `precompressed` set, every file response carries `Vary: Accept-Encoding`. Twins are cached like
  files, by their own path; a twin too large to keep comes from disk with its `Content-Length`. It
  costs a stat per encoding tried, as nginx's `gzip_static` costs an open.
- `try_files` takes paths with `{path}` for the request's path, as nginx's does; one ending in `/`
  is a directory, served by its index file (or listed, with `browse`). The first that resolves is
  served, else 404; a directory asked for without its slash is not redirected under `try_files`.
  `["{path}", "{path}/", "/index.html"]` serves a single-page app. Each candidate passes the same
  traversal check as a request path.
- `browse` lists a directory with no index file: an HTML table, directories first, each group by
  name, dotfiles left out, with size and modification time; names are HTML-escaped and hrefs
  path-escaped, so `?`, `#`, `&` and `<` stay names.
- Sendfile: a file too large for the cache goes through `net/http.ServeContent` to fib's
  `Context.ReadFrom`, which on HTTP/1 sends it, whole or a range, by sendfile(2) without reading it
  into memory (since I12; now documented and in the code's comments). HTTP/2 frames it instead.
- Resolution is now its own step (`resolve`), shared by plain requests and `try_files`, and the
  cache and headers take a `variant` (the name the type comes from, the encoding, whether to vary).
- Benchmarks: fit now serves the harness's `static` (with `precompressed` on, which a 1KiB
  `small.html` with no twins exercises as a stat per encoding the client accepts) and `proxy`
  scenarios, as nginx and Caddy do; `bench/configs/fit/fit.json` has the upstream with 4,096 idle
  connections, as the others keep. The config validates, and a local `fit run` of it serves the
  scenario's body over HTTP/1.1 and h2c. The harness run itself is for I23, on the benchmark host.
- Tests: encoding negotiation (preference, q values, ties, `*`, refusals, a missing twin, no
  `Accept-Encoding`), distinct ETags and a 304 for a twin, a range of a twin, a twin from disk, twins
  as plain files without `precompressed`; `acceptQ`; `try_files` for a single-page app (a file, a
  directory with and without its slash, a client route, a directory without an index, traversal),
  with nothing found, and with `browse`; listings (escaping, order, dotfiles, the parent link, 404
  without `browse`); the new params' errors. `go vet ./...` and `go test -race ./...` pass.

Differences from the plan:

- The benchmark scenario is wired and checked but not run: the benchmark host refused the session's
  ssh key again, so the numbers come with I23.

### I10 HTTP/3 entry — done

- Config: protocols gain `h3`, which needs `tls`. It is opt-in, as nginx has it: a TLS listener that
  names no protocols serves HTTP/1.1 and h2.
- `listener`: a listener's binding is now up to two of the engine's, one TCP for its HTTP/1.1, h2c
  and h2, and one UDP for h3, on the port TCP got (so an address with port 0 gets the same port for
  both), served by `http3.NewHandlerWithConfig` with the listener's TLS config through
  `http3.ConfigureTLS` (TLS 1.3, ALPN `h3`) and request bodies streamed past 64KB as on TCP. The same
  certificates serve both. An `h3`-only listener binds UDP alone. The UDP port is checked with a
  plain bind first, as TCP's is. Reload and retire take both bindings; `Addr` reports the UDP address
  in `UDPAddr`. The fingerprint now hashes the protocols, so adding or dropping `h3` rebinds.
- Alt-Svc: the TCP side's dispatcher registers an `OnHeader` hook on every response that adds
  `Alt-Svc: h3=":port"; ma=86400` (fib's `http3.AltSvc`). The hook and the value are made once per
  listener, so registering it allocates nothing, and listeners without `h3` register none. fib hands
  the hook a copy of a header a handler passed to `WriteResponse`, so the shared headers of `respond`
  and of the static cache are left as they are; a test checks that.
- Tests: `listener` serves a route over h3 (fib's HTTP/3 client, `Proto` HTTP/3.0) on the TCP port,
  sees `Alt-Svc` naming it on HTTP/1.1 and h2 responses, 404s included; an h3-only listener takes no
  TCP; no `Alt-Svc` without `h3`; a reload that drops `h3` lets the UDP port go; and a `respond`
  shared by a listener with `h3` and one without gives `Alt-Svc` only to the first. `test/e2e` runs
  `fit run` with h3, reaches `/hello` over h2 and finds `Alt-Svc`, reaches it over h3 on the port
  Alt-Svc names, and shuts down cleanly. `go vet ./...` (also for Windows) and `go test -race ./...`
  pass.

Differences from the plan:

- A listener replaced on the same address binds a second UDP socket beside the old one for the time
  the old one drains; datagrams then split between the two by the kernel's hash, so a QUIC
  connection caught in that window may break, and its client reconnects. TCP connections are not
  affected that way.

### I23 M1 conformance and benchmarks — done

The [M1 report](https://github.com/lesismal/fit/blob/3658ae9/bench/results/m1-report-2026-10-19.md) has the details.

- Conformance (on the development Mac): h2spec 2.0.0 against the h2c and TLS h2 entries, answering
  with `respond` and through the proxy, 145 of 145 in each of the four runs; and
  `test/e2e/conformance_test.go`, a proxy conformance suite through the `fit` binary with requests
  written byte by byte (request smuggling with both lengths, conflicting `Content-Length`s,
  whitespace before a colon, missing and doubled `Host`, a bad chunk size, a NUL in a field, a header
  past the limit, obs-fold, `Expect: 100-continue`, an HTTP/1.0 client, pipelining, an absolute-form
  target, 304, 204 and 1xx from the upstream, an upstream that does not speak HTTP, and
  `Connection: close` from either side).
- Found and fixed: fib v1.3.0 let `X-A : one` through as a field named `X-A `, where RFC 9112 asks
  for 400. The proxy refuses a request with a field name that is not a token, so it never reaches an
  upstream. The fix in fib is asked for as A9 in
  [fib-api-requests](fib-api-requests.md), and fib v1.4.0 refuses such a request with 400 (`25064b3`);
  the proxy's check stays as defence in depth, in the loop that copies the header anyway.
- Benchmarks (on the benchmark host: Ryzen 7 7800X3D, Ubuntu 24.04, host networking): `fitbench`
  with its defaults for nginx 1.31.4, Caddy 2.10.2, Traefik 3.5.0 and fit, with idle connections at
  10k and 100k; the results are `bench/results/m1-2026-10-19-linux.json` and
  `m1-2026-10-19-linux-idle100k.json`. fit, in thousands of requests a second over HTTP/1.1 and h2c:
  fixed 783 and 3,811 (nginx 812 and 643); static 580 and 1,328 (nginx 440 and 373, Caddy 164 and
  125); proxy 215 and 230 (nginx 258 and 249, Caddy 129 and 94, Traefik 152 and 113). Memory per
  idle connection: HTTP/1.1 1.5 KiB at 10k and 1.1 at 100k (nginx 0.5, Caddy and Traefik 20 to 22);
  TLS 9.1 and 7.4 KiB (nginx 29, Caddy and Traefik 43 to 46). p99 below Caddy's and Traefik's in
  every scenario, above nginx's over HTTP/1.1. Every target of the roadmap for M1 holds.
- To run there, the harness gained `-root` and `-bin` (prebuilt binaries for a host without Go);
  `idleconn` opens connections from several loopback addresses (`-sources`, one per 40k), binds them
  with `IP_BIND_ADDRESS_NO_PORT`, and closes them with a reset, so 100k fit in the ports and leave no
  TIME_WAIT for the next run; fit serves the harness's TLS idle connections.
- `go vet ./...` and `go test -race ./...` pass.

Differences from the plan:

- No fix was split into a new iteration: the review that came in meanwhile planned R01–R07, and the
  report's leads go to them. The proxy's 83% of nginx over HTTP/1.1, with 6.1 cores to nginx's 4.7,
  and fit's HTTP/1.1 p99 tail are R07's to look into, with the download path's waits, which R04
  removes.
- WebSocket idle memory at 100k could not be measured for any server: the proxy's connections to the
  one WebSocket backend address run out of ports at 64,504. The harness needs several backend
  addresses for it by I47, when fit proxies WebSockets. nginx closed about 45k of its 100k idle
  connections by its 75s `keepalive_timeout` while the rest were opening, so its 10k figures are the
  ones to compare.
- Against the M0 baseline (Docker Desktop on an M3 Max), fit's fixed HTTP/1.1 throughput went from
  1.25 times nginx's to 0.96, and its idle HTTP/1.1 memory from 0.9 to 1.5 KiB. The machines differ;
  measuring both commits on this host, for R07, settles whether fit moved.

### R01 Bounded waits on slow clients — done

- Config: `limits.write_stall_timeout` (30s when left out), in a new top-level `limits` object, with
  `Limits.WriteStall()` for the default. `runtime.Compile` hands it to modules through `module.Env`;
  `module.Context.WriteStallTimeout()` reads it at provision time.
- The proxy's download path writes each piece through `exchange.write`: `SetWriteDeadline(now +
  stall)`, `Write`, `FlushError`, then the deadline lifted. A client that stops reading fails the
  write once the deadline passes (fib closes its HTTP/1 connection or resets its HTTP/2 or HTTP/3
  stream), the upstream response is given up with `ClientResponse.Close`, and the worker goes back to
  the upstream connection. `Finish` gets a deadline too.
- The static handler hands `ServeContent` a `stallWriter` over the Context: each `Write` under the
  same set-and-lift deadline; `ReadFrom` keeps fib's `ReadFrom` (sendfile, which does not wait) on a
  plain HTTP/1 connection, and copies through the bounded `Write` over TLS, HTTP/2 and HTTP/3,
  where fib would copy through writes that wait. A Context that takes no deadline
  (`http.ErrNotSupported`) is written to without one.
- Tests (`handler/proxy/stall_test.go`), with a stall timeout of 1s and clients over HTTP/1.1, h2c
  and h3: the review's two cases, one client that stops reading a 200MB download from an h2c
  upstream with `max_conns: 1`, and eight such clients against an HTTP/1.1 upstream with
  `max_conns: 4`; in both, ten other requests are all served (within 1s and 2s of the stall
  respectively) and every stuck client's response is cut short. Run once without the deadlines, the
  other requests time out after 10s, as the review measured. A client that reads slowly and an
  upstream that pauses for twice the stall timeout keep their response. `handler/static`: a client
  that stops reading a 64MB file over h2c and over h3 has its response cut short (and keeps all of
  it without the deadline). `config` tests the new limit. `go vet ./...` and `go test -race ./...`
  pass; the streaming tests still pass.

Differences from the plan:

- `write_stall_timeout` is in a top-level `limits` object rather than on each listener: the handlers
  that bound their writes by it are shared by every listener a route serves, and R02 puts the
  listeners' other limits in `limits` too.
- The deadline is set before each write and lifted after it, rather than refreshed at most once a
  second: fib resets an HTTP/2 or HTTP/3 stream when its deadline passes even if nothing waits, so a
  deadline left in place would cut off a response whose upstream pauses, as a server-sent events
  stream does. As with nginx's `send_timeout`, only the time a write waits for the client counts.
- Neither the listener's `WriteTimeout` nor fib's HTTP/3 one is set: they bound a whole response,
  which would cut off long downloads.
- A file sent by sendfile on a plain HTTP/1 connection gets no deadline: no worker waits for it,
  and the kernel holds what the client has not taken. R05 (asynchronous file sending) gives it one.
- No benchmark before and after: the harness's scenarios take the paths R01 leaves alone (a small
  proxied body answered in one write, a cached file without conditions).

### R02 Safe default limits — done

- Upstreams: `max_conns` defaults to 256 per address (it had no limit); `max_pending` (1024) bounds
  the requests waiting for a connection, and one past it gets 503 at once; `response_timeout` (1h)
  bounds a whole response, through fib's client `Timeout`, so it gives 504 before the response
  begins and cuts it short after. Until fib counts waiting requests (R06), the pool counts requests
  under way from `Pick` to `Done` against `max_conns × addresses × streams + max_pending`, with one
  stream per HTTP/1.1 connection and 100, RFC 9113's floor, per HTTP/2 one; `Pick` returns nil past
  it, `Pool.Do` reports `upstream.ErrBusy`.
- `limits` gains `max_pending_bytes` (256 MiB), set on the listeners' engine as fib's
  `MaxPendingBytes`; and `max_header_bytes` (1 MiB) and `max_body_bytes` (16 MiB), set on every
  listener's HTTP/1, HTTP/2 and HTTP/3 config. `max_body_bytes` bounds streamed bodies too
  (`MaxStreamedBodyBytes`): since I16 every body past 64 KB streams, and fib's `MaxBodyBytes` alone
  left those unbounded. A reload that changes the header or body limit rebinds the listener; the
  engine's budget takes a restart. `listener.Server.Stats` returns the engine's counters.
- `static` gains `browse_max_entries` (10000): a listing reads at most that many entries, sorts
  those, and says when the directory has more, instead of reading it whole.
- Every default is in the schema: a `default` struct tag, checked as JSON when the type is first
  read, which the schema emits as JSON Schema's `default`; 23 fields carry one (the upstream's and
  the access log's fields, `limits`, `respond`'s status, `static`'s). Defaults that depend on other
  fields (a listener's protocols, `load_balancing`) stay in the descriptions. `examples/proxy.json`
  writes the limits and the upstream's settings out at their defaults.
- Tests that reach each limit: 256 connections open to one address and, past `max_pending`, a
  request refused at once (`upstream`); 503 past `max_pending` through the proxy, `response_timeout`
  before and during a response over HTTP/1.1 and h2c, and a streamed upload past `max_body_bytes`
  refused with 413 for a declared length and ended for a chunked one (`handler/proxy`); a header past
  `max_header_bytes` gets 431, and bodies past `max_body_bytes` 413 whole and streamed, over HTTP/1.1
  and h2c (`listener`); `max_pending_bytes` spent by a client that reads nothing, which fib's counters
  show as pending bytes past the budget and reads paused for it, until the client reads
  (`listener`); a listing cut at `browse_max_entries` (`handler/static`); and the schema's defaults
  (`schema`). The proxy tests' shared config raises `max_body_bytes` for the 256 MB streaming tests.
  `go vet ./...` and `go test -race ./...` pass.
- Benchmarks before and after, on the benchmark host, fit alone, two rounds each: static 577k and
  1.33M requests a second over HTTP/1.1 and h2c, proxy 215k and 228k, the same within 0.5% before
  and after. The harness's fit config now sets `max_conns: 4096` for its backend, as the others
  keep 4096 connections: the h2c proxy scenario has 2,048 requests in flight, past the new default.

Differences from the plan:

- The limits test found nothing in fit or fib but a test helper of I14's, `newBackend`, whose
  connection hook blocked once more than 64 connections had closed, stalling the backend: any test
  with a burst of more than about 96 connections hung. Its send no longer blocks.
- `max_pending_bytes` is fib's budget, which fib checks once a read round is done, so a request
  that arrives in one read is still served while the budget is spent; it holds back reading on, as
  the test shows through fib's counters. fib warns that a few peers that do not read can hold it.
  A peer that streams is cut off by R01's stall timeout; one whose responses were queued whole is
  not, and R06's connection limits are what bound how many such peers there can be.
- An explicit 0 for `max_conns`, `max_pending` or a `limits` field means its default, since the
  config cannot tell 0 from left out for these fields; no limit can be turned off.

### I24 Config versions and Admin API skeleton — done

- `admin.Configs` is the one path a new config takes, from SIGHUP or the Admin API (and later the
  file provider): `Apply(data, source)` loads, compiles and moves the server to it with
  `listener.Server.Reload`, under one lock, so that applies follow one another; on any failure the
  running config stays. It keeps the last `admin.history` configs (10) as they were written, with
  the version `runtime.Runtime.Swap` gave each graph, when it was applied and where it came from.
  `fit run`'s SIGHUP reload now goes through it; its output is unchanged.
- The Admin API (`admin.Server`), on net/http and off unless the config has `admin`:
  `GET /config` returns the running config as written, with its version in `Fit-Config-Version`;
  `POST /load` applies its body and answers the new version, the listeners added and removed, and
  warnings (changes to `access_log` or `admin`, which take a restart), or 400 with each config
  error's path, line, column and message, or 500 when a listener cannot be bound; `GET /versions`
  lists the configs kept and `GET /versions/{n}` returns one. `fit run` prints
  `fit: admin API on <address>`, and a line for each config the API applies.
- Config: a top-level `admin` with `address` (127.0.0.1:2019, or `unix/<path>` for a Unix socket,
  made 0600; a socket left by a process that is gone is replaced, one in use is refused),
  `token_file` (a bearer token every request must carry, compared in constant time), `history`,
  `max_config_bytes` (4 MiB) and `max_conns` (16). The address must be loopback or a socket unless
  `token_file` is set. Requests with `Origin` get 403, `POST /load` takes only `application/json`,
  and on a TCP address without a token the `Host` field must name a loopback host, so that neither
  a web page nor DNS rebinding reaches the API. Timeouts: 10s for the header, 60s for the body and
  the response, 60s idle; 64 KiB of header.
- Tests: `admin` loads a config and reads it back, and the listener serves it; refuses a config
  that does not decode, compile or bind (400, 400, 500), one sent as `text/plain` (415) and one past
  `max_config_bytes`, with a length (413 before reading) and chunked (413 at the limit), leaving
  the running config and history alone; the token (missing, wrong, another scheme, right; an empty
  token file); Origin and Host checks; history trimmed to `history`; eight concurrent loads, each
  applied whole with a version of its own; a client that stops sending its config, cut off at the
  read timeout while others are served, and one cut midway, which applies nothing; connections past
  `max_conns` closed at once and served again once others go; the Unix socket, its mode, a stale
  socket replaced and one in use refused; and the warnings. `config` tests the new checks;
  `test/e2e/admin_test.go` runs fit with the API, loads a config that adds a listener, reads it
  back and from `/versions/1`, is refused a broken one, and sees SIGHUP apply the file again as
  version 3. `go vet ./...`, `GOOS=windows go vet ./...` and `go test -race ./...` pass.
- No benchmark: the data plane is unchanged; the API runs beside it on goroutines of its own.
- Decision note [005](decisions/005-admin-api.md): net/http for the API, off by default, the token
  in a file, configs kept as written.

Differences from the plan:

- The Admin API is off unless the config has `admin` (`"admin": {}` turns it on at its default
  address), where Caddy's is on by default; see decision 005.
- `runtime` is unchanged: `Runtime.Swap` already numbers graphs, and the history, being control
  plane, lives in `admin` with the apply path.
- `GET /versions` and `GET /versions/{n}`, not in the plan, show the history; `/versions` rather
  than `/config/versions`, since I25 gives paths under `/config` to JSON pointers.
- The history is in memory only; SIGHUP applies the file again over a config loaded through the
  API, and a restart starts from the file.
- A listener at port 0 is kept by a new config only if it is still written as port 0: listeners
  are matched by the address as written, so the e2e test keeps `127.0.0.1:0` for the kept one.

### I32 Active health checks — done

- Config: an upstream's `health_check`, with `type` (`http`, the default, or `tcp`), `path` (`/`),
  `host`, `port`, `expect_status` (200–399 when left out), `interval` (5s), `timeout` (2s, no
  longer than the interval), `jitter` (a tenth of the interval, less than it), and `fails` and
  `passes` (2 each): the probes in a row that take an address out of rotation and put it back.
  `path`, `host` and `expect_status` on a `tcp` check are refused.
- `upstream`'s prober, made by `upstream.New` for a pool with an engine and stopped by `Pool.Close`
  (so `fit validate` probes nothing): each backend's probes follow one another, each started by a
  `time.AfterFunc` timer and finished in fib's callback, the next scheduled `interval` after the
  last began plus a random jitter, the first within a jitter of the start. No goroutine waits for
  a probe and none runs on a listener's worker. An http probe is a GET through a `fibhttp.Client` of
  the pool's own protocol and TLS settings but its own connections (two per address, none kept
  idle), so that it sees whether the backend takes new connections and never waits behind the
  pool's requests; its callback runs once the header has arrived (`StreamResponseBody`, threshold
  0) and the body is given up with `ClientResponse.Close`. A tcp probe is `Engine.DialWithHandler`,
  closed as soon as it opens.
- Results go to the backend's `downUntil`, the one field `Pick` reads, which is now
  `math.MaxInt64` while probes have the backend down, and otherwise what passive health set:
  probes and `max_fails` take a backend out each on their own account, and a request's success
  does not put back one its probes took out. `Pick` is unchanged; when every backend is down, health
  is still ignored. `Backend.ProbedDown` reports the probes' verdict.
- Tests (`upstream/probe_test.go`): a backend whose health endpoint turns to 503 leaves rotation
  and rejoins it within the configured window (two probes' intervals and jitter and the second's
  timeout, doubled for a loaded host), and requests through the pool avoid it meanwhile; a single
  failure under `fails: 2` keeps it; tcp probes against a port that closes and opens again; slow
  upstreams (answering after the timeout, or taking the connection and never answering) and a cut
  connection (a header broken off) are taken out, and a 1 GiB body is not waited for; `expect_status`,
  `host` and `port`; probes over h2c and h2 with a CA file; probes stop when the pool closes; probe
  connections are not kept; probes and passive health together. `config` tests the new checks and
  defaults. `go vet ./...`, `GOOS=windows go vet ./...` and `go test -race ./...` pass.
- `examples/proxy.json` has a health check and the Admin API.
- No harness benchmark: requests take the same path as before (`Pick` reads the same field), and a
  config without `health_check`, as the harness's, starts no probes. `go test -bench BenchmarkPick`
  (parallel, 14 threads, this Mac) before and after: round robin 54 and 57 ns, weighted round robin
  106 and 107, least connections 66 and 66, within the benchmark's run-to-run spread.

Differences from the plan:

- A reload makes new pools (as since I22), so health starts again with every backend up after a
  reload or an Admin API load, and a backend that was down rejoins until `fails` probes fail
  again. I26, which can share pools between graphs whose upstream did not change, would keep it.
- Each address may have two probe connections, not one: under the race detector, the connection
  of a probe that gave up on a large body took up to 750ms to finish closing in fib's client, and
  the next probe waited for it past its timeout. Nothing else in fit sees this.
- Nothing is logged when a backend leaves or rejoins rotation; I34's metrics will show it.

### I25 Admin API: patch, ETag, rollback — done

- `GET /config/<pointer>` returns the value a JSON pointer (RFC 6901) names in the running config;
  `PUT` sets it to the body (replacing a member or an element, adding a member, appending at `-`),
  `PATCH` merges the body into it as a JSON merge patch (RFC 7396, sent as
  `application/merge-patch+json` or `application/json`), and `DELETE` removes it. `/config` alone
  is the whole config: `PUT /config` replaces it as `POST /load` does, and `DELETE /config` is
  refused. A missing member or index gets 404, a body that is not JSON (or holds a key twice) 400.
- Every change applies a whole config through `Configs.Update`, which takes the running config,
  makes the change and applies the result under the lock that orders applies, so that changes from
  many writers each start from the one before. A change that leaves the config invalid is refused
  with its errors, and nothing changes.
- Each version has an ETag, the first 96 bits of the SHA-256 of the config as written, sent as
  `ETag` on every answer about a config and listed by `GET /versions`. A change with `If-Match`
  (one ETag, a list of them, or `*`) is made only if the running config is still that one, and is
  answered 412 otherwise; `POST /load` and rollbacks take it too. `GET /config` answers 304 to an
  `If-None-Match` that names the running config.
- `POST /rollback/{n}` applies version `n`'s config again, as a new version from the source
  `rollback`; a version no longer in the history gets 404.
- An edited config is written out by fit (`admin/jsonptr.go`: a tree of the document that keeps
  members in order and numbers as written, two spaces to a level); a config sent whole is kept as
  written.
- Tests (`admin/edit_test.go`): pointers and their escapes; getting, putting, appending,
  removing and merging in a document, and errors for missing members, indexes past the end or
  with leading zeros, and scalars; order, numbers and characters kept; reading a value, `PUT`,
  `PATCH`, appending and deleting a route, each served at once, and the config written out;
  edits that fail validation (an address, a module, a required field, an unknown field), that are
  not JSON or point nowhere, refused with nothing changed; ETags, `If-None-Match`, `If-Match` with
  the right, a stale, a listed and `*` ETag, a stale one refused for every kind of change; eight
  writers that read, change and write back with `If-Match`, retrying on 412, beside eight that
  append without it, all sixteen routes in the config once and as many versions; rollbacks, to a
  version still kept, with `If-Match`, and to one gone. `go vet ./...`, `GOOS=windows go vet ./...`
  and `go test -race ./...` pass.
- Decision note [005](decisions/005-admin-api.md) gains the formats: pointers, merge patch, the
  ETag, rollback.

Differences from the plan:

- `PATCH` is a JSON merge patch at the pointer rather than a replacement of the value there, which
  `PUT` already does; a JSON Patch (RFC 6902) document is not taken.
- The ETag is a hash of the config, not its version, since versions start again on a restart.
- An edited config's layout is fit's, not the one it was written in.

### I33 Load balancing v2 — done

- `load_balancing` gains `random` (by weight), `p2c` (of two backends drawn at random, the one
  with fewer requests under way for its weight), and `ip_hash`, `uri_hash`, `header_hash` and
  `cookie_hash`, consistent hashing on the client's IP address (without its port), the path and
  query, or the header or cookie `hash_key` names. A request without the header or cookie goes in
  turn. `sticky_cookie` (`name`, `path`, `max_age`, `secure`; always `HttpOnly` and
  `SameSite=Lax`) keeps a client on one address on top of any policy: a request whose cookie names
  an address that is up goes there, and any other is answered with the cookie of the address it
  went to, after the upstream's own `Set-Cookie` fields. The cookie's value is the first 64 bits of
  the SHA-256 of the address, so it is the same in every fit with the same addresses and says
  nothing more about them.
- The hash ring has 160 points per unit of weight for each backend, placed by its address alone
  (FNV-1a with MurmurHash3's finish), as nginx's `hash ... consistent` has; a key goes to the
  first point at or after its hash, or, when that backend is down, to the next point whose backend
  is up, so the keys of a backend that is down move and come back with it, and no others do.
  Every policy passes over backends that are down, unless all are, as before.
- `Balancer.Pick` and `Pool.Pick` take the request (nil when there is none, as for `Pool.Do`'s
  callers); `Pool.SetCookie` says what the proxy adds to the response. Nothing on the request path
  allocates: keys are read from the request in place, header names canonicalised and the
  `Set-Cookie` fields made when the pool is.
- Tests: distribution of `random` (even and by weight) and `p2c` (never the busiest of three,
  level as requests stay, by weight, one backend up); hash distribution for each key, even and by
  weight; the same key to the same backend; adding a fourth backend moves only keys to it, a
  quarter of them; a backend down moves only its own keys, which come back with it; the port not
  counting for `ip_hash`, the query counting for `uri_hash`; cookie parsing; requests without a key
  going in turn; no allocation a pick for the new policies and the sticky cookie; down backends
  passed over; sticky cookies (set without one, followed with one, replaced when its backend is
  down or it names nothing, attributes) in `upstream`, and through the proxy, with the upstream's
  own cookie kept, and `header_hash` keeping each user on one backend (`handler/proxy`); config
  errors. `go vet ./...`, `GOOS=windows go vet ./...` and `go test -race ./...` pass.
- Benchmarks before and after, on the benchmark host, fit alone, the proxy scenario (round robin,
  as the harness's config has it), two rounds each, alternating: over HTTP/1.1 215k and 216k
  requests a second before, 215k and 213k after; over h2c 228k and 228k before, 228k and 227k after,
  within 1%, about as far apart as two rounds of the same binary. `go test -bench BenchmarkPickNew`
  (parallel, 14 threads, this Mac): 1.5 to 4 ns a pick for the new policies, cookie_hash the most,
  none allocating.

Differences from the plan:

- Sticky sessions are a `sticky_cookie` beside `load_balancing` rather than a policy of their own,
  so that a client without the cookie, or whose address is down, is placed by any policy.
- Consistent hashing is by a ring, as nginx's, rather than rendezvous hashing: lookups take a
  binary search however many addresses there are.

### I26 Listener diffing and graph draining — done

- Listener diffing was I22's: unchanged listeners kept, new ones added with `AddBinding` before the
  swap, removed ones drained with `ShutdownBinding`. I26 adds what keeps long requests on the graph
  they began on, and what ends them.
- A handler that serves a request past its return holds its graph for it: `module.Env` carries the
  graph, `module.Context.Graph()` gives a `module.Holder`, and the proxy takes a hold when an
  exchange starts and gives it back in `exchange.release`. The graph, its modules and its pools stay
  until the last exchange on it is over.
- `Runtime.SetDrainTimeout` (30s by default; `listener.Server.SetDrainTimeout`, so `fit run`'s
  `-shutdown-timeout`, sets it too): `Swap` starts a timer on the graph it swaps out, stopped when
  the graph is cleaned up; when it fires, the modules that implement `module.Drainer` end what they
  have. The proxy derives each exchange's context from one of 32 drain contexts, picked at random so
  that exchanges starting at once do not share one lock, and `Drain` cancels them all: a response
  not yet begun gets 503, one under way is cut short, and neither counts against the backend.
- Pools are shared between graphs: `runtime.Env.Previous` names the graph being replaced, and an
  upstream whose config (as JSON) and CA file contents are unchanged takes over its pool with
  `Pool.Acquire`; `Pool.Close` gives a hold back and the last one closes it. Connections and active
  health (I32) carry over a reload; an upstream that changes gets a pool of its own. `admin.Configs`
  passes the running graph.
- Tests: `runtime` drains a graph a request still holds after the timeout and not before, cleans
  it up once the request lets go, never drains one whose requests ended first or with a negative
  timeout; shares the pool of an unchanged upstream and its CA file, not of a changed one or one
  whose CA file changed. `upstream`: a pool held twice. `handler/proxy`: a download that outlasts a
  reload keeps streaming through the shared pool until the drain timeout and is then cut off, while
  one begun after it goes on, and the backend stays in rotation; a request waiting for its response
  when drained gets 503. `test/e2e/churn_test.go` swaps the config 2,000 times through the Admin
  API, paced over six seconds, while eight clients send short requests (HTTP/1.1 and h2c, kept and
  new connections, a fixed response and the proxy) and four download second-long streams; the
  swaps change the fixed response each time, the upstream every 50 and add or remove a listener
  every 100: in each of three runs, about 337,000 short requests and 32 downloads, none failed.
  `go vet ./...`, `GOOS=windows go vet ./...` and `go test -race ./...` pass.
- Benchmarks before and after, on the benchmark host, fit alone, the proxy scenario: 216k and 214k
  requests a second over HTTP/1.1, 228k and 229k over h2c, within 1%. Only one round counts: in the
  second, another benchmark had started on the host (a WebSocket server at four cores), and both
  binaries fell by 8% to 33%; the rerun was stopped so as not to disturb it. To be measured again
  when the host is free.

Differences from the plan:

- The churn test's long connections are streamed downloads through the proxy, not WebSockets,
  which come with I47.
- Drained requests are those of handlers that answer after returning, the proxy's; a request whose
  handler is still running (a static file being sent) holds its graph through the dispatcher and
  ends as it would.
- Each proxied exchange registers its context with one of the proxy's drain contexts, one lock
  more a request; shared over 32 contexts, the benchmark does not show it.

### I27 File provider — done

- `provider/file.Watcher` looks at the config file's size and modification time every interval
  (1s), on a goroutine of its own; once they have stayed the same for the debounce time (500ms)
  after a change, it reads the file (at most 64 MiB) and, if its SHA-256 differs from what was
  applied last, applies it. An editor's several writes make one reload, a touch none, and a file
  renamed into place is seen as one written in place. A config that fails, a file that is gone or
  too large, is reported once, and the watcher goes on: the next edit is tried, a file that comes
  back is applied. `Applied` records contents applied another way.
- `fit run -watch` (and `-watch-interval`) starts it, printing `fit: watching <file>`; a change
  prints `fit: <file> changed; reloading` and is applied through `admin.Configs.Apply`, as SIGHUP
  is, with the same output; a SIGHUP tells the watcher what it applied. The watcher stops before
  shutdown begins, so that no config is applied meanwhile.
- Tests (`provider/file`): an edit applied, one renamed into place applied, a touch not; four writes
  in a row applied once, as the last; a broken edit reported once, then a fix applied; a file
  removed reported once and applied when it is back; a file past the limit refused; contents
  applied by SIGHUP not applied again. `test/e2e/watch_test.go` runs `fit run -watch`, edits the
  file and sees the new route served, a broken edit reported on stderr with the running config
  kept, and a fix applied. `go vet ./...`, `GOOS=windows go vet ./...` and `go test -race ./...`
  pass.
- Decision note [006](decisions/006-file-provider-polls.md): polling rather than file system
  events.
- No benchmark: nothing on the data plane changed.

Differences from the plan:

- The watcher polls rather than taking file system events; see decision 006.
- It is turned on by a flag of `fit run`, not by the config.

### R07a Hot path costs in fit — done

R07 is split in three ([dev-plan](dev-plan.md#r07a-hot-path-costs-in-fit)): R07a and R07b need
nothing from fib, and R07c waits for A7 and A11–A13.

- Access log (`observe`): the buffer is split into shards, a power of two up to GOMAXPROCS (at most
  64), each with its own lock, padded apart. A line goes to the shard of its client's address
  (`maphash` of `RemoteAddr`), so one connection's lines keep their order. A shard hands its
  buffer to the writer through a bounded channel once it holds 64 KB, under its lock, and the
  writer queues each shard's partial buffer the same way at each flush, so the channel holds every
  shard's buffers in the order they were filled. `buffer_bytes` bounds what is queued for the
  writer plus what the shards hold. A shard checks that against an atomic counter it updates once
  per buffer handed over, not once per line, so each shard can hold up to 64 KB more than the
  bound. Buffers written out are reused through a bounded free list. A line's timestamp is
  formatted by hand rather than by `Time.AppendFormat`, and JSON string escaping checks a byte
  table.
- `observe.Track(r)` replaces `Attach`: the entry and the context that carries it are one
  allocation (`tracked`, a `context.Context` whose `Value` answers for the entry).
- `listener`: each dispatcher keeps where its listener was in the last graph it looked in (graph
  version and index, behind an atomic pointer), so a request scans the graph's listeners only once
  per graph.
- `upstream`: smooth weighted round robin is worked out once per balancer as a schedule of
  `total weight / gcd` entries (at most 65,536; past that it stays under the lock), and a pick takes
  the next entry with one atomic add. A backend that is down is passed over by drawing another
  entry, so its share goes to the others by weight; when all are down, health is ignored as before.
- Tests: `observe` `TestShards` (16 clients logging at once at GOMAXPROCS 8; every line written or
  counted as dropped, each client's in order, nothing left counted), `TestTrack`,
  `TestAppendTime`; `listener` `TestDispatchFollowsReload` (the listener moving between positions
  across reloads, and a graph where it has no route); `upstream` `TestWeightedRoundRobinDown`,
  `TestWeightedRoundRobinSchedule` (gcd, and weights too many to precompute),
  `TestWeightedRoundRobinConcurrent` (exact shares over 8 × 7000 picks). The new
  `BenchmarkDispatch` serves a `respond` route through the dispatcher, with the access log on,
  from each P as a client of its own; `BenchmarkLog` now does the same. `go vet ./...` and `go test
  -race ./...` pass.
- Micro-benchmarks on the development Mac (M3 Max), before and after, median of five, ns/op at
  GOMAXPROCS 1, 8 and 16: `BenchmarkLog` 359, 335, 424 → 223, 49, 34; `BenchmarkDispatch` 1009,
  750, 852 → 969, 485, 387, with 13 → 12 allocations; `BenchmarkPick/weighted_round_robin` 17.2,
  116, 115 → 2.5, 45, 57, as plain round robin, where the shared counter is now the cost.
- On the benchmark host, fit alone, two rounds of each build alternating, thousands of requests a
  second (HTTP/1.1 and h2c): without an access log, fixed 781 and 3,768 → 780 and 3,770, proxy 212
  and 228 → 212 and 229, all within 0.4%; with the access log to `/dev/null`, fixed 732 and 2,108
  → 752 and 2,296 (+2.7% and +8.9%), proxy 207 and 226 → 208 and 228.

Differences from the plan:

- The dispatcher's listener lookup is a cache per graph version rather than an index built at
  compile time: the dispatcher outlives graphs, and a graph's listeners keep their slice, so the
  index is the position, found once.
- The schedule is not rebuilt when health changes: drawing again past a backend that is down gives
  the same shares by weight among those up, without a rebuild on the probe's goroutine.
- The access log still costs fixed h2c about 40% of its throughput (3.77M without, 2.30M with).
  Three of `BenchmarkDispatch`'s 12 allocations are fib cloning the response header because an
  `OnFinish` hook is registered, which cannot change it: asked of fib as
  [A13](fib-api-requests.md#a13-no-header-copy-for-a-response-that-only-onfinish-hooks). The
  request copy that `r.WithContext` makes waits for A7. Both are R07c's.

### R07b Proxy allocations — done

- `handler/proxy`: the exchange is allocated first and holds what the outgoing request is built
  from: the request itself (`base`, of which `out` is the copy `WithContext` makes), its URL and the
  four forwarding fields' values, whose header slices point into it. `clientAddr` returns the
  client's address as a string too, a part of `RemoteAddr` for an IPv4 address (netip parses only
  the canonical form, so it is that already) and formatted only for IPv6, once instead of twice;
  `forwardedElement` builds the element in one sized `strings.Builder`. A response that arrived
  whole is read into a buffer of its length with `io.ReadFull`, rather than through a
  `bytes.Buffer` that grew past it; the 400 and 503 bodies are package variables.
- `BenchmarkProxy` (`handler/proxy/bench_test.go`): small GETs through the proxy to an HTTP/1.1
  backend over keep-alive connections, from a raw client that allocates little. The test helpers
  `startFit` and `serveBackend` take a `testing.TB` for it. `TestForwardedElement` checks
  `clientAddr` on IPv4, mapped, IPv6 and zoned addresses and that an IPv4 one allocates nothing.
  `go vet ./...` and `go test -race ./...` pass.
- On the development Mac: 95 → 81 allocations and 13.0 → 11.3 KB per proxied request, net/http
  backend included; what fit's own code allocates is now the exchange, the header map, the
  `OnCancel`, timer and `onResponse` closures and the response header timer, the context and its
  copy of the request, the Forwarded element and the body read. The rest is fib's client, the
  backend and the client: a memory profile is the evidence for
  [A11](fib-api-requests.md#a11-the-body-of-a-client-response-that-arrived-whole-without-a-copy)
  and [A12](fib-api-requests.md#a12-fewer-timers-and-contexts-per-client-request), four timers a
  request among them.
- On the benchmark host, R07a's build against this one, two rounds each alternating, thousands of
  requests a second: proxy over HTTP/1.1 211.7 → 219.0 (+3.4%), p99 3.91 → 3.51 ms, over h2c
  228.4 → 229.6; with the access log, 204.5 → 211.8 (+3.6%) and 226.3 → 228.1.
- The M0 question from I23, settled on the benchmark host: fit at `f96aa03` (M0, fib v1.2.0) and
  this build, with M0's config, two rounds each: fixed 792 and 3,929 → 782 and 3,764 over
  HTTP/1.1 and h2c (−1.3% and −4.2%), with 19 → 28 MB resident. So fit's fall from 1.25 times
  nginx's fixed throughput to 0.96 between the M0 baseline and I23 is the change of machine
  (Docker Desktop on an M3 Max to the Linux host), not fit; the few percent that are fit's go to
  R07c.

Differences from the plan: none beyond what goes to fib. The proxy's response header timer stays
until fib's client has a setting for it (A12).

### R03 fib APIs for forwarding without waiting — done

- fib v1.4.0 (tag on `ecd62b0`) has A1–A7, A9, A11 and A13, and parts of A8 and A12. fit moves to
  it with no change of its own: `go.mod` and `go.sum` only. `go vet ./...` and `go test -race ./...`
  pass on it.
- [fib-api-requests](fib-api-requests.md) and its twin give each request's status, its commits,
  and how the API as built differs from what was asked. The differences that matter to the next
  iterations: `fib.Config.MaxConns` is engine-wide, not per binding (R06); `MaxPendingPerHost` is
  in `http` only (R06); `ResponseHeaderTimeout` is a client setting, which fits one client per
  upstream pool (R07c); `OnWritable` reports false until a response streams (R04); `SendFile` is
  not tied to `FileCache`, and leaves 206 and `Content-Range` to the handler (R05).
- Not done in fib: `WriteStallTimeout` (A8), which `WriteNoWait` makes unneeded; the
  `context.AfterFunc` per cancellable request (A12), which needs a cancel handle from `Do`. fib
  noted that a request requeued after its reused connection closed does not restart its header
  timeout.

Differences from the plan: none. fib's own session did the work in fib, as rule 15 asks.

### R04 Proxy downloads without waiting — done

- `handler/proxy`: the download path never waits. Each piece of the upstream body goes to the
  client with `Context.WriteNoWait`, flushed unless it is the last; once it reports more than the
  response's limit (64 KB) queued for the client, the upstream body is held with
  `ClientResponse.HoldBody(true)` and `Context.OnWritable` resumes it once half has gone. While it
  is held, an HTTP/1 upstream connection is read no further, and an HTTP/2 or HTTP/3 one gives
  this stream alone no more window, so a client that reads slowly holds back its own download
  and nothing else. `write_stall_timeout` still bounds each wait: the write deadline is set when
  the body is held and lifted when it resumes, and `OnWritable`'s function, told of the deadline
  passing or the client going, gives the response up. A mutex in the exchange orders the body's
  callbacks, which run on the upstream worker or fib's side pool once resumed, with
  `OnWritable`'s.
- An upload with trailers is an ordinary pushed body: a `BodyWriter`, sent chunked with the
  announced names in its `Trailer`, ended with `CloseWithTrailer` and the values the client's body
  ended with. `trailerPipe`, and the goroutine fib's client spent reading it, are gone.
- Tests: `TestStuckClientWithoutStallTimeout`, with the stall timeout at an hour, a client over
  HTTP/1.1, h2c or h3 that stops reading a 200 MB download from an h2c upstream with
  `max_conns: 1`: ten other requests are served within a second, fit's heap grows by less than
  32 MB, and the stuck client gets the whole body once it reads. On the R03 code, the others time
  out after 10 s. `TestStuckClientOnSharedConnection` now expects the others at once and waits
  out the stall timeout from when the client got stuck, not after the others. R01's other stall
  tests, the 256 MB streaming tests, the trailer tests and the SSE test pass unchanged.
  `BenchmarkProxyDownload` streams 64 MB through the proxy: on the development Mac, median of
  four, 1,915 → 2,260 MB/s over HTTP/1.1 and 1,275 → 1,290 MB/s over h2c. `go vet ./...` and
  `go test -race ./...` pass.

Differences from the plan:

- R01's tests do not all pass with the stall timeout at an hour, and cannot: with an HTTP/1.1
  upstream, a stuck download keeps the upstream connection it is read from, as it does in nginx,
  so under `max_conns` (`TestStuckClientsPastMaxConns`) it is the stall timeout that frees the
  connections for others. With an h2c upstream, where a stream can be held alone, nothing relies
  on it, which the new test shows.
- No benchmark host run: the harness's proxy scenario answers with a small body that arrives
  whole, which goes out in one `WriteResponse`, a path R04 leaves alone.

### R06 Connection and queue limits through fib — done

- `limits.max_conns` (100,000 when left out) is fib's `Config.MaxConns` on the listeners' engine: a
  connection past it is closed as it is accepted, and counted in `Stats().ConnsRefused`. Like
  `max_pending_bytes`, it is the engine's, set at start; a reload does not change it.
- An upstream's `max_pending` is fib's `ClientConfig.MaxPendingPerHost`, and `upstream.ErrBusy` is
  fib's `ErrTooManyPending`, which the proxy answers with 503 and which counts against no
  backend. R02's count of requests in flight, and its guess of 100 streams per HTTP/2 connection,
  are gone: `Pool.Pick` always picks.
- The benchmark config sets `max_conns` to 1,000,000 for the 100k idle connection runs;
  `examples/proxy.json` shows the default.
- Tests: `listener` `TestConnLimit` (`max_conns` 3 across two listeners: a fourth connection is
  closed unanswered and counted, and one opened after another closes is served); `upstream`
  `TestDefaultMaxConns` and `handler/proxy` `TestMaxPending` reach fib's cap. `go vet ./...` and
  `go test -race ./...` pass.

Differences from the plan:

- `max_pending` is per address, not per upstream: fib's cap is per host. Its doc says so.
- fib v1.4.0's cap also counted the requests waiting for a connection being dialed for them, so a
  cold burst past `max_pending` was refused while the connections `max_conns` allows were still
  opening: 179 of 256 opened in `TestDefaultMaxConns`'s burst. Asked of fib as
  [A15](fib-api-requests.md#a15-maxpendingperhost-not-to-count-requests-whose-connection-is-being-dialed),
  done in v1.5.1; the test sends its 256 at once again.
- No benchmark: neither limit is reached in the harness's scenarios, and `Pick` lost an atomic
  add and a compare.

### R05 Static files without blocking — done

- `handler/static`: no file operation runs on a fib worker. The cache keeps what a request path
  resolved to, a file with its index and its compressed twins, a directory to list or redirect,
  or nothing to serve, for `check_interval` (a new parameter, 1s when left out), along with the
  contents of small files as before. A request it can answer from that is answered on the worker
  without a system call; a conditional or ranged one goes through `ServeContent` over the bytes in
  memory, gathered and written whole. Anything else, a path not seen within the interval, a file
  to read, open or list, goes to a file pool of the package's own: `max(16, 4 × GOMAXPROCS)`
  goroutines and a queue of 4,096, shared by every static handler. A request the queue cannot
  take gets 503 at once, and one the pool has not answered within 10 s, waiting or on a disk
  that hangs, gets 503 then, its work's own answer dropped (`queued.claim`). The lookups are
  bounded at 16,384 paths, so a client asking for paths that do not exist cannot grow them past
  that.
- A file too large to keep goes out through fib's `SendFile`: `ServeContent` runs against a writer
  that takes the range it would copy (`rangeWriter`, `offsetFile`) instead of copying it, and
  `transfer` hands `SendFile` the range a piece at a time. Each piece is 64 KB where fib copies, so
  that fib never waits within one, and 256 KB on plain HTTP/1, where sendfile only queues it.
  Between pieces it waits for `OnWritable` under a write deadline of the stall timeout, as the
  proxy's downloads do. A request for several ranges of such a file gets the whole file, since
  `ServeContent` would copy a multipart body through a pipe. `stall.go` and its writes that wait
  are gone.
- `internal/fibtest.ServeRequest` serves through `fibhttp.Serve`, as fib's servers do, and waits up
  to `fibtest.Timeout` for a handler that answers later, which fib v1.5.1 allows (A14).
- Tests: `TestNoDiskOnHit` (a known path within `check_interval` is answered without a pool job,
  four requests to one job, and looked at again after it; a file too large to keep takes a job
  each time); `TestSlowDisk` (a pool whose one worker is stuck: a path that needs the disk gets
  503 after the pool's timeout, a cached one is answered at once, and past the full queue 503 at
  once); `TestStuckClient` over h2c and h3 now through `SendFile`; `TestSlowReader` (3 MB read at
  about 1.3 MB/s over HTTP/1.1 and h2c, past the 1s stall timeout, arrives whole);
  `TestConditional`'s several ranges from disk get the whole file; `TestChangedFile` sets the
  interval to 0. `go vet ./...` and `go test -race ./...` pass.
- On the benchmark host, fit alone, R06's build against this one, two rounds each, thousands of
  requests a second: static over HTTP/1.1 587 → 772 (+31%), over h2c 1,327 → 2,457 (+85%), with
  26 MB resident against 31.

Differences from the plan:

- Not fib's `FileCache`: it reads a file it misses on the caller's goroutine, which would be a fib
  worker, stats every request where it has no inotify, and sends large files through
  `ServeContent`'s waiting writes; nor does it have fit's index files, try_files, listings,
  zstd twins or ETags. The cache is fit's, and every file operation goes through the pool.
- A change on disk shows within `check_interval`, not at once, as nginx's `open_file_cache_valid`
  has it.
- Over plain HTTP/1 a file sent by sendfile is neither paced nor cut off by the stall timeout:
  fib v1.5.1 does not count a queued file in the connection's unsent output, so `OnWritable`
  always reports room (as R01 noted). Asked of fib as
  [A16](fib-api-requests.md#a16-a-file-queued-by-sendfile-counted-in-a-connections-unsent-output);
  `TestStuckClient/http/1.1` is skipped until R05b moves to the fib release that has it.

### R05b Files sent by sendfile paced and bounded — done

- fib v1.5.2 counts a file `SendFile` queued in the connection's backlog (A16, `6f66bcc`). fit moves
  to it and changes nothing else: the static handler's transfer, which waits on `OnWritable`
  between pieces under the stall timeout's deadline, now waits on plain HTTP/1 too.
- `TestStuckClient/http/1.1` runs: a client that stops reading a 64 MB file sent by sendfile is cut
  off after the 1 s stall timeout, as over h2c and h3; `TestSlowReader` still gets its 3 MB whole
  over HTTP/1.1 and h2c. `go vet ./...` and `go test -race ./...` pass.

Differences from the plan: fib did not add a stall bound inside `SendFile`, so ranges still go a
piece at a time, 256 KB over plain HTTP/1 and 64 KB elsewhere. No benchmark: the harness's static
scenario serves a cached file, which does not take this path.

### R07c Hot path costs through fib — done

- The access log's entry is the request's in fib's Context value slot (A7): `observe.Track(c)` and
  `EntryOf(c)` take the Context, and no request is copied for it.
- The proxy sends through `Client.Start` and abandons a request with its `Call`: the request has no
  context of its own (no `WithCancelCause`, no `WithContext` copy, nothing for fib to watch with
  `context.AfterFunc`). Why it was abandoned, the client gone, the idle timeout or a drain, is
  recorded in the exchange (`cancel`, `causeErr`), and a small lock orders keeping the `Call`
  against a cancel that comes first. A drain finds the exchanges under way in 32 locked sets
  per proxy instead of cancelling a parent context.
- `response_header_timeout` is fib's `ResponseHeaderTimeout` on each pool's client, which shares
  the request's one timer, and the proxy's own timer for it is gone; fib's
  `ErrResponseHeaderTimeout` is 504.
- A response that arrived whole goes to the client as the bytes fib's client holds
  (`BodyBytes`, A11), without a copy. A13 needed nothing of fit's.
- `fibtest.Recorder.Wait` makes no timer or channel for a response already written, which
  `BenchmarkDispatch` was counting.
- Tests: `TestStatusOf` covers `ErrResponseHeaderTimeout`, `ErrBusy` and the idle timeout's cause;
  `TestTrack` the slot; the drain, timeout, streaming and access log tests pass unchanged.
  `go vet ./...` and `go test -race ./...` pass.
- Allocations on the development Mac: `BenchmarkProxy` 77 → 65 a request, 11.1 → 10.0 KB, net/http
  backend included. `BenchmarkDispatch`, with the same fibtest, 9 → 10 allocations and 636 →
  558 ns single-threaded, 406 → 396 ns at 8: each benchmark request has a fresh Context, on which
  fib makes the hooks the value slot lives in; a server's Contexts are recycled and keep them.
- On the benchmark host, R05b's build against this one, two rounds each, thousands of requests a
  second (HTTP/1.1 and h2c): without an access log, proxy 218.9 and 236.0 → 228.4 and 237.3
  (+4.3% and +0.5%), p99 over HTTP/1.1 3.71 → 3.21 ms, fixed 778 and 3,774 → 781 and 3,774; with
  the access log, proxy 214.7 and 235.1 → 226.2 and 236.9 (+5.4% and +0.8%), fixed 787 and 2,683
  → 788 and 2,691.

Differences from the plan:

- `response_header_timeout` now counts from the end of the request, as its doc says and as fib's
  client measures it; the proxy's timer counted from the start, connect and upload included.
- I23's question, the proxy's 83% of nginx over HTTP/1.1: on this host fit's proxy went from
  212k (I23's build) to 228k requests a second through R07a–R07c, about 88% of nginx's 258k at
  I23. The [M2 report](https://github.com/lesismal/fit/blob/3658ae9/bench/results/m2-report-2026-10-20.md) compares them again in one run:
  226k against nginx's 264k, 86%, with half as much CPU again per request.
- With the access log on, fixed h2c still serves about 30% less than without (2.69M against
  3.77M), where A13 removed the header copy: what is left is the line itself, formatted on the
  worker, and the hooks. It is not a regression, and not M1's.

### I28 Certificate manager and storage; ACME decision — done

- `tlsmgr.Manager`, the interface a handshake's certificate comes from:
  `GetCertificate(*tls.ClientHelloInfo)`. `tlsmgr.Static` is the Manager of certificates from
  files (`LoadStatic`, errors at the certificate's path as before): an index from each name a
  certificate is for (DNS names in lower case, IP addresses) and from the parent of each `*.` name
  to the certificates for it, in the order given. A handshake gets the first for its name that the
  client supports, or the first for its name, or the first of all when none is for it or it names
  none, as fib picked from `Config.Certificates`; a name with one certificate skips the check. A
  lookup among 100 certificates takes 26ns and allocates nothing.
- `tlsmgr.ServerConfig(protocols, get)` builds a listener's `tls.Config` with `GetCertificate` and
  no `Certificates`. `runtime.Listener` carries `Certificates tlsmgr.Manager` in place of the
  `tls.Config` and fingerprint; `listener` builds the configuration once per binding, over HTTP/1,
  HTTP/2 and HTTP/3, with a `GetCertificate` that reads the binding's Manager from an atomic
  pointer, which `Reload` sets to the new graph's once it is current. A reload that changes only
  certificates keeps the binding and its connections; new handshakes get the new certificates.
- `tlsmgr.Storage`, for what ACME keeps (I29): `Load`, `Store` (whole or not at all), `Delete`,
  `List`, and `Lock`/`Unlock` across processes; its methods may block and take a context, and are
  for the manager's own goroutines. `FileStorage` keeps each key as a file under a root (written to
  a temporary file, synced and renamed; 0600 files, 0700 directories; values up to 4 MiB; keys with
  an empty, `.` or `..` part, a leading slash or a backslash refused), and each lock as a file made
  with `O_EXCL`, which its holder touches every 5s and which a waiter removes once it is 30s old.
- Decision note [007](decisions/007-acme-client.md): fit manages certificates itself, with
  `golang.org/x/crypto/acme` for the protocol and ARI written in fit; not CertMagic, lego,
  `autocert` or, for now, acmez.
- Tests: `tlsmgr` picks by exact name, case and trailing dot, wildcard (one label only), IP
  address, order, and key type (an RSA certificate for a client without ECDSA), and the first for
  no name or an unknown one; the configuration per protocol set; load errors with their paths.
  `FileStorage`: store, replace, load, list by prefix (locks not listed), delete, bad keys, the
  value limit, a cancelled context; eight holders in two storages on one directory take a lock in
  turn, never two at once; a lock kept fresh is waited for past the stale time and taken once given
  back; a stale one is taken over. `listener`: a reload with a rewritten certificate keeps the
  listener and a connection open across it, and new connections over HTTP/1.1, h2 and h3 get the new
  certificate and not the old one. `go vet ./...`, `GOOS=windows go vet ./...` and
  `go test -race ./...` pass.
- Benchmark: `listener.BenchmarkTLSHandshake`, full TLS 1.3 handshakes on new connections without
  resumption, before (`Config.Certificates`) and after (`GetCertificate`), five rounds alternating
  on an M3 Max: 68.8µs and 68.8µs a handshake on average, with each round within 70.2–67.4µs and
  70.8–67.6µs. fib keeps a handshake with `GetCertificate` a state machine, and only the callback
  goes to its handshake pool. The harness's scenarios do not handshake, so it was not run.

Differences from the plan:

- Static certificates go through `GetCertificate` rather than staying in `Config.Certificates`, as
  I09's notes suggested: the benchmark shows no cost, and in exchange a certificate changes without
  binding the listener anew.
- Certificate files are not watched; a reload (SIGHUP, `-watch` on the config file, the Admin API)
  reads them again, and no longer rebinds the listener. A watch on them, or a renewal loop that
  picks them up, is left for I29–I30.
- No config yet for the storage's location: it comes with I29, the first user of `Storage`.

### R07d Access log cost — done

R07c left fixed h2c about 30% slower with the access log on than off. `perf` on the benchmark host
(fit on 7 cores, h2load with 64 connections and 32 streams, the log to `/dev/null`) put about 23%
of fit's CPU under the log, 0.6 µs a request, spread thin: escaping JSON strings a byte at a time,
`Header.Get` canonicalizing "User-Agent" and "Referer" on every line, durations formatted as
floats, the date and time formatted in full on every line, the shards' locks, two clock reads, and,
in fib, `SetValue` and `OnFinish`.

- `appendString` scans eight bytes at a time (`plainLen`, `plainWord`) and appends each plain run
  at once.
- The user agent and referer are read from the header map by their canonical keys
  (`headerValue`), without canonicalizing them again.
- Durations are formatted from integer microseconds (`appendSeconds`), as `AppendFloat` with
  six decimals would, without formatting a float.
- The date and time to the second are formatted once a second and shared (`AccessLog.appendTime`,
  an atomic pointer to the last second's text); a line adds its milliseconds.
- Four shards for each of GOMAXPROCS, up to 64, where there was one: a request's shard is its
  client's, and with as many shards as workers two workers picked the same one often.
- Tests: `TestPlainLen` checks every byte at every position against the table; `TestAppendStringLong`
  an escape at every offset of a long string; `TestAppendSeconds` against `strconv`;
  `TestLogTime` against `appendTime` across seconds and zones; `TestLogLine` that `Log` writes
  what `appendLine` formats. `go vet ./...` and `go test -race ./...` pass.
- On the development Mac: `BenchmarkLog` 230 → 181 ns single-threaded, no allocations either way;
  `BenchmarkDispatch` 580 → 550 ns single-threaded, 390 → 350 ns at 8, 10 allocations either way.
- On the benchmark host, clean builds of `ea59148` and of it with this change, two rounds each
  of 20 s, fixed h2c with the access log: 2.69M and 2.69M → 2.86M and 2.87M requests a second
  (+6.6%), against 3.80M without the log; the log's cost goes from 29% to 24.5%. In the harness,
  two rounds alternating, fixed HTTP/1.1 and the proxy did not move (788k, 226k and 236k), the log
  not being what bounds them.

Differences from the plan:

- What is left, about 0.45 µs a request, is the line itself, formatted on the worker, and fib's
  share: `OnFinish` takes a `responseWriter` from fib's global pool for a response written whole,
  and `SetValue` takes the Context's lock, together about 85 ns. That is asked of fib as
  [A17](fib-api-requests.md#a17-cheaper-onfinish-and-setvalue-on-every-request).
- Formatting lines off the worker would move the cost, not remove it: the server's cores are
  already all busy in this benchmark.

### I34 Prometheus metrics — done

- The Admin API serves `GET /metrics` in Prometheus's text format (0.0.4), written by fit
  (`observe.TextWriter`), no client library. Requests are counted only when the config has
  `metrics` (`"metrics": {}`, which needs `admin`, and which a reload may turn on or off): `fit run`
  makes an `observe.Metrics` when the config has `admin`, and a graph takes counters from it when
  its config has `metrics`. The rest of `/metrics` is always served.
- Requests, per listener and route: `fit_http_requests_total{listener,route,code}`, by the class
  of the status (`2xx` and so on), and `fit_http_request_duration_seconds{listener,route}`, a
  histogram from when the handler is called to when the response is handed to the connection, with
  buckets from 5ms to 10s. A route without a name is counted as `#` and its place in the config
  (`#1`), and requests no route takes under `route=""`. Each listener's routes get their counters
  when the graph is compiled (`runtime.Listener.Requests`, by `Route.Index`), so a request finds
  them by index, and counts them in the dispatcher's `OnFinish`, the one the access log already
  registers when it is on. Counters are shared between graphs and kept while a graph has them: a
  route that a reload keeps goes on counting, and one no graph has is no longer scraped.
- Counting is atomic adds alone, spread over eight shards of 128 or 192 bytes. A shard is picked
  by the address of the request's Context, which fib keeps in pools that favour the core that gave
  it back: picked at random, every core touches every shard, and on 4 cores a request costs 37ns
  rather than 6ns. With 14 cores on one route, 15ns.
- Upstreams: `fit_upstream_requests_in_flight{upstream}`;
  `fit_upstream_response_duration_seconds{upstream}`, a histogram of the time to a response's
  header, or to a failure, recorded by the proxy; per backend,
  `fit_upstream_backend_requests_total{upstream,backend,result}` (success or failure, as
  `Pool.Done` is told), `fit_upstream_backend_requests_in_flight` and `fit_upstream_backend_up`.
  A pool a reload replaces starts its counts afresh.
- The listeners' engine: `fit_connections_open`, `fit_connections_refused_total`,
  `fit_pending_bytes`, `fit_reads_paused_total{reason}`, from `listener.Server.Stats`. Config:
  `fit_config_reloads_total{result}`, the configs applied after the first and those that failed
  (`Configs.Reloads`), `fit_config_version`, `fit_config_last_applied_timestamp_seconds`.
  Certificates: `fit_tls_certificate_not_after_seconds{listener,name,serial}`, from the
  certificates of each listener's `tlsmgr.Static`. `fit_access_log_dropped_total`, and
  `go_goroutines` and two of the Go runtime's memory figures, from `runtime/metrics`.
- Tests: `observe` checks the histogram's bounds (inclusive), sums and counts, the status classes,
  counts from eight goroutines at once, the registry's lifetime, the text format with escaping,
  and the shards' sizes. `runtime`: two graphs share a listener's counters, a graph that fails to
  compile after a listener took its counters gives them back, and none are left once every graph
  is released. `admin`: a scrape after requests through two listeners (one with TLS) and an
  upstream with a backend that answers and one that refuses, a reload and a failed one: every
  count, the duration histograms, the backends' results, the certificate's expiry, the reloads and
  the version are as expected, and the output is checked line by line (each family's HELP and TYPE
  once, before its samples; no sample twice); a route renamed by a reload is no longer scraped;
  without metrics, no request metrics are written. `test/e2e`: `fit run` with the Admin API
  counts requests across a reload, and `/metrics` shows them. `go vet ./...`,
  `GOOS=windows go vet ./...` and `go test -race ./...` pass.
- Cost per request: `listener.BenchmarkDispatch`, on 14 cores without a network: 333ns a request
  with neither the access log nor metrics, 350ns with the log, 438ns with metrics alone, 360ns with
  both. Metrics alone pay for the first `OnFinish` hook, which takes a writer from fib's pool and
  more (the test harness gives each request a new Context); with the log on, they add about 10ns
  and no allocation. A17 in [fib-api-requests](fib-api-requests.md) asks fib for a cheaper
  `OnFinish`.
- Benchmarks on the benchmark host, fitbench, fit alone, two rounds alternating, before (R07d) and
  after, both with the Admin API on. Without `metrics`, every scenario is within 1.5%: fixed
  777k → 779k requests a second over HTTP/1.1 and 3.79M → 3.84M over h2c, static 772k → 775k and
  2.46M → 2.45M, proxy 229k → 227k and 237k → 238k. With `metrics`, fixed h2c 3.29M (−13%) and
  static h2c 2.25M (−9%), proxy over HTTP/1.1 −1.9%, the rest unchanged; HTTP/1.1 is held at
  about 775k by the load generator, so it does not show the cost. `perf` on fixed h2c (3.86M
  without, 3.28M with): of the 0.32µs more a request, about 110ns is fib's `OnFinish` path, 60ns
  the two clock reads, 30–45ns the closure's allocation and collection, and 15ns fit's counting.
  [A18](fib-api-requests.md#a18-a-responses-status-and-when-its-request-arrived-without-a-hook)
  asks fib for a response's status and its request's arrival time, so that a request answered
  before its handler returns is counted with one clock read and no hook.

Differences from the plan:

- No metrics per service: fit has no service layer yet (traffic splitting comes in M3); routes and
  upstreams have theirs.
- Status codes are counted by class, so that a request finds its counter by index; exact codes
  would need a map, or a counter for each of 500 codes per route.
- A first version counted requests whenever the Admin API was on; the benchmark showed what that
  costs, and counting became a setting of its own, off by default: on the benchmark host it costs the
  fastest paths 8% to 13% (fixed h2c −13%, static h2c −9%), and A18 asks fib for what would make it cheaper.

### I29 ACME HTTP-01 and renewal — done

- Config: a listener's `tls` takes `acme`, host names to obtain certificates for, besides or instead
  of `certificates` (one or the other is needed); a name may not be a wildcard, an IP address or
  listed twice. A top-level `acme` says where: `directory` (Let's Encrypt's when left out),
  `email`, `ca_file` for a CA of one's own, `storage` (`$XDG_DATA_HOME/fit` or
  `~/.local/share/fit`), `max_concurrent` (2), `timeout` for one certificate (10m) and
  `request_timeout` for each request to the CA (30s). Like `access_log` and `admin`, the `acme`
  settings are read at start, and a reload that changes them gets a warning; the names follow
  every reload.
- `tlsmgr.ACME`, the control plane's certificate manager, on `golang.org/x/crypto/acme` (decision
  [007](decisions/007-acme-client.md)): a scheduler and `max_concurrent` workers of its own, which
  may block on the CA and the storage, each obtaining under the `timeout`. `Manage(names)` sets the
  names; `Certificate(name)` and `HTTPChallenge(token)` read memory only, an atomic map and a
  `sync.Map`, for a handshake and a request. For each name a worker takes the storage's
  certificate if it is not due for renewal, and otherwise takes the storage's lock for the name,
  looks again, and orders one: account (its key made and kept on first use, under a lock of its
  own), order, HTTP-01 answer, finalize with a new P-256 key, and the chain and key stored as one
  PEM value, so that they change together.
- Renewal once a third of the lifetime is left, less up to a fiftieth of it by the serial number,
  so that certificates obtained together spread out, and instances sharing a storage agree on
  when. A failure is tried again after a minute, doubling up to an hour, less up to a tenth at
  random; it is logged on stderr with the wait.
- `tlsmgr.Certs`, a listener's Manager when it lists names: a listed name's obtained certificate,
  and the files' for every other name and for a listed name not obtained yet; with no files, the
  first name's for a client that names none, and an error for a handshake with nothing to give.
- `runtime`: `Env.ACME`, the manager `fit run` makes at start, whether or not the config lists
  names, so a reload can add some; the graph carries it with the names of all its listeners, and
  `listener` tells it the names once the graph is current, at start and on every reload. The
  dispatcher answers `/.well-known/acme-challenge/<token>` from memory on a request in the clear
  when a challenge for the token is under way; any other goes to the routes. Compiled without a
  manager, as `fit validate` does, a listener's listed names are not obtained and only its files'
  certificates are served (fixed after the merge: `fit validate` had refused every config that
  lists names, which `cmd/fit`'s validate test now covers).
- `internal/pebbletest` runs Pebble v2.10.1 and its DNS test server, which answers 127.0.0.1 for
  every name, from `$FIT_PEBBLE_BIN` or `PATH`; tests without them are skipped. CI installs them.
- Tests: `TestPebble` obtains a certificate from Pebble, answering the challenge from
  `HTTPChallenge`, checks it chains to Pebble's root and that the account key is stored, renews it
  (renewal set 3s after issue) and gets a new serial, and has a second manager on the same storage
  serve the renewed certificate without obtaining one. `TestACMEFromPebble` (e2e) runs the fit
  binary with a listener in the clear on Pebble's HTTP port and a TLS listener with only
  `acme`: a client gets the page over a certificate that verifies against Pebble's root, and a
  token with no challenge goes to the routes. Without Pebble: `TestSlowCA`, a CA that never answers,
  five names and `max_concurrent` 2: each name is given up after the timeout and tried again, the CA
  never sees more than two requests at once, a handshake and a challenge are answered meanwhile,
  and `Close` returns at once; `TestCutCA`, a CA that closes every connection, is tried again later
  and later; `TestStoredCertificate`, a stored certificate served without the CA; `TestManage`, a
  name dropped is no longer served or tried; `TestCerts`, `TestRenewAt`, `TestBackoff`, `TestCAKey`;
  the config's checks in `TestValidate`. `go vet ./...`, `GOOS=windows go vet ./...` and
  `go test -race ./...` pass, with Pebble and without it; the ACME tests pass five times in a row.

Differences from the plan:

- ARI, the CA's suggested renewal window, which decision 007 says fit adds itself, is not here:
  renewal is by lifetime. It goes with I30.
- No benchmark: the data plane gains, on a request in the clear, a pointer check and a prefix
  compare of the path, and handshakes for a listed name a map lookup through an atomic pointer.
- The plan touched `tlsmgr` and `listener`; `config`, `schema`, `runtime`, `admin`, `cmd/fit` and CI
  changed too, to configure, wire and test it.

### I30 TLS-ALPN-01, OCSP stapling — done

- TLS-ALPN-01 (RFC 8737): every TLS listener offers `acme-tls/1` last in ALPN, so only a client that
  offers it alone, as a CA checking the challenge does, gets it; `tlsmgr.Certs` gives such a hello
  the challenge's certificate (made with `TLSALPN01ChallengeCert`) while a challenge for a name the
  listener lists is under way, and nothing otherwise. `ACME.ALPNChallenge` reads a `sync.Map`.
  fib's TLS needed nothing new: its server picks ALPN in the server's order and gives
  `GetCertificate` the client's protocols, and a connection that negotiates neither h2 nor
  http/1.1 is read as HTTP/1, which the CA closes after the handshake.
- `acme.challenges` (`["http-01", "tls-alpn-01"]` when left out) says which challenges are
  answered, in order; the attempt after n failures in a row starts with the nth, so that a type the
  CA cannot reach does not fail every attempt.
- OCSP stapling: `tlsmgr.OCSP`, made by `fit run` and closed after the graph, keeps a response for
  every certificate that names a responder and has its issuer in its chain, from files
  (`Static.Staple`, at compile; `Release` at the graph's cleanup) or obtained (`ACME.UseOCSP`). It
  fetches on goroutines of its own, two at once, each within 10s and 64 KiB, with
  `golang.org/x/crypto/ocsp`, checks the response against the issuer, and staples a good one to a
  copy of the certificate, which handshakes read through an atomic pointer. It fetches again
  halfway through a response's validity (hourly without a next update), after a failure a minute
  later doubling up to an hour, and never staples a response past its next update. Certificates
  are kept by their chain with a count of users, so the graphs a reload makes share one response.
  A revoked certificate is not stapled and is reported; one obtained with ACME is renewed at once,
  and the storage's copy of it is not taken again.
- `internal/certtest` gains a CA that issues certificates naming an OCSP responder, and
  `internal/pebbletest` a settable TLS-ALPN-01 port.
- Tests: `TestPebbleTLSALPN` obtains a certificate from Pebble through TLS-ALPN-01 alone, answered by
  a TLS server with a listener's configuration, and the challenge's certificate is gone after;
  `TestChallengeFallback` lists `tls-alpn-01` first with nobody answering it: the first attempt
  fails and the next is answered over HTTP-01; `TestCertsALPN`: the challenge's certificate only
  for a listed name and a hello offering `acme-tls/1` alone. `test/e2e` `TestACMETLSALPNFromPebble`
  runs the fit binary with only a TLS listener on Pebble's TLS-ALPN-01 port: fib's TLS answers the
  challenge and the client gets the page. OCSP, against a responder the tests control: a response
  stapled to a copy; refreshed halfway through a validity of two seconds; kept while the responder
  is down and dropped once expired, then stapled again; a revoked certificate unstapled and
  reported; one chain added twice shares a staple and one fetch, dropped after its last release; a
  responder that holds every request sees at most two at once, and `Close` returns at once;
  `Static` serves the stapled copy and releases it. `listener` `TestOCSPStapled`: clients of TLS 1.3
  and 1.2 get the response stapled by fib's TLS. `go vet ./...`, `GOOS=windows go vet ./...` and
  `go test -race ./...` pass, with Pebble.
- Benchmark: `listener.BenchmarkTLSHandshake`, four rounds alternating before and after: medians
  70.9µs and 70.2µs a handshake, within the noise of a laptop. The request path is unchanged.

Differences from the plan:

- Split: HTTPS by default goes to I30b, and ARI, which I29 had left for I30, to I30c, to keep the
  iteration near the size the plan aims for.
- Let's Encrypt has stopped OCSP, so its certificates get no staple; the stapling is for other CAs
  and for certificates from files.
- No setting turns stapling off: a certificate that names no responder costs nothing.

### I31 On-demand TLS — done

- Config: a listener's `tls` takes `on_demand: true`, alone or with `certificates` and `acme`, and
  needs a top-level `acme.on_demand`: `ask`, a URL asked with `domain=<name>` whether a name may
  have a certificate (any answer but 2xx refuses it), `rate` per `interval` (10 a minute),
  `max_names` (1000) and `timeout`, how long a handshake waits (10s, fib's handshake timeout).
  Like the rest of `acme`, it is read at start; a reload that changes it gets a warning.
- `tlsmgr.Certs` serves a listed name as before, and a name the files have a certificate for
  (`Static.Has`, exact or wildcard) from them; any other name a client asks for goes to
  `ACME.OnDemand`, which `GetCertificate` calls on fib's handshake pool, never on a worker. It
  refuses names that are no host name (an IP address, one label, a wildcard, a bad label) without
  asking; serves a certificate it has from memory; otherwise checks the rate and `max_names`,
  asks the ask URL, counts the name against both, and hands it to the ACME workers as a managed
  name, which loads it from the storage or obtains it as I29 does, and renews it after. Handshakes
  for the same name wait for one certificate; each waits at most `timeout`, and the certificate
  goes on being obtained for the next. A refusal is remembered for a minute (10,000 names at
  most); an ask URL that does not answer refuses without being remembered. A name whose first
  certificate fails is dropped and no longer counted; a reload keeps the names taken on.
- Tests: four handshakes at once for an allowed name get one certificate from Pebble, with one ask
  and one order, and it is then served from memory, also after a reload that lists no names; a
  refused name is asked about once a minute; an ask URL that does not answer refuses each time; with
  a rate of one an hour, the second name is refused without asking, and allowed an hour later; past
  `max_names` a name is refused; against a CA that cuts connections the waiting handshake hears the
  failure, the name is dropped and no longer counted; bad names are refused without asking; a name
  the files have is served from them, as is a hello with no name; without `acme.on_demand` nothing
  is obtained. `test/e2e` `TestOnDemandFromPebble`: the fit binary obtains a certificate during the
  handshake of an allowed name and serves the page, and fails the handshake of a refused one. The
  config's checks in `TestValidate`. `go vet ./...`, `GOOS=windows go vet ./...` and
  `go test -race ./...` pass, with Pebble.
- No benchmark: listeners without `on_demand` take the same path as before, and a request's path is
  unchanged.

Differences from the plan:

- Rate limits are on taking names on, which a certificate loaded from the storage counts towards
  too; a stored certificate still needs the ask URL's yes, as the name may no longer be allowed.
- Names taken on are kept and renewed until a restart; none is dropped for going unused, and
  `max_names` bounds them meanwhile. I35c drops them once idle.
- The certificate expiry metric lists the names a listener lists, not those taken on demand.

### I30b HTTPS by default — done

- `listeners` may be left out. A config that writes none is served, while HTTPS by default is on,
  on an `https` listener (`:443`, HTTP/1.1 and h2) with certificates obtained with ACME for the
  host names its routes match, and an `http` listener (`:80`) that answers ACME's HTTP-01
  challenges and redirects every other request to `https://<host>[:port]<target>` with 308, so
  that a client repeats its method and body. Routes may name either listener; the `http` one
  serves none. A TLS listener written with an empty `tls` gets certificates for the host names of
  the routes it serves. Listeners written out are served as written, so existing configs do not
  change.
- Names: a host name of two labels or more; wildcards, IP addresses and names only a private
  network resolves (`localhost`, `.localhost`, `.local`, `.internal`, `.home.arpa`, `.invalid`)
  are skipped.
- `auto_https`: `disable` (no listeners made, an empty `tls` refused), `disable_redirects` (no
  `http` listener: certificates come through TLS-ALPN-01), `http_port` (80) and `https_port`
  (443). The logic is `config.Config.Effective`, which `Validate` and `runtime.Compile` both use:
  a config with no listeners and no name to obtain a certificate for is refused, as is a route
  naming a listener that does not exist, and the two ports being one. `runtime.Listener`
  gains `RedirectHTTPS`, the port the dispatcher redirects to; it is counted by the access log and
  metrics as a request no route took.
- Let's Encrypt staging: `test/e2e` `TestLetsEncryptStaging` runs fit with a config naming only a
  host, against the staging CA, and checks that a certificate from it is served. It is run by hand
  on a host the name resolves to from the internet and that can listen on ports 80 and 443:
  `FIT_LE_STAGING_NAME=<name> go test ./test/e2e -run TestLetsEncryptStaging -v -timeout 10m`
  (`FIT_LE_STAGING_EMAIL` sets the account's address); it is skipped otherwise.
- Tests: `config` `TestAutoHTTPSName` and `TestEffective` (names per listener, routes on other
  listeners, the config left unchanged, ports, no redirects) and the checks in `TestValidate`;
  `listener` `TestAutoHTTPSRedirect`: a config with no listeners gets both, and requests in the
  clear are redirected with 308, host, port and target kept, for GET and POST, an IPv6 host, and a
  challenge path with no challenge; `test/e2e` `TestAutoHTTPSFromPebble`: the fit binary with a
  config naming only `auto.fit.test` (and Pebble's address and ports) obtains its certificate
  through HTTP-01 on the `http` listener, serves the page over HTTPS, and redirects HTTP; `fit
  validate` takes such a config. `go vet ./...`, `GOOS=windows go vet ./...` and
  `go test -race ./...` pass, with Pebble.
- No benchmark: the dispatcher reads one more field of the listener it already has.

Differences from the plan:

- Listeners written out get no redirect: HTTPS by default fills in what a config leaves out, and
  does not change what it says. A redirect for a written listener comes with I38's redirects.
- The `https` listener serves HTTP/1.1 and h2, not h3, as a written TLS listener does by default.
- The Let's Encrypt staging test needs a public name and ports 80 and 443, which the benchmark
  host does not have. It was run by hand on 2026-10-21, at 1f56c44, on a public Debian 12 host
  with a DuckDNS name: fit obtained a certificate from the staging CA ("(STAGING) Baloney Bulgur
  YE2") through a config naming only the host, and served it, in 10s. The host's Go is older than
  fit's, so the fit binary and `go test -c`'s were cross-built and copied over, with a stand-in
  `go` on the test's `PATH` that copies the prebuilt fit where `TestMain` builds it.

### I30c ACME Renewal Information (ARI) — done

- `tlsmgr.ACME` asks the CA, after each run that leaves a name with a certificate, what ARI
  (RFC 9773) suggests for it: the `renewalInfo` URL from the CA's directory, read once (x/crypto's
  `acme.Directory` does not carry it), and the certificate's identifier, its authority key
  identifier and its serial number's DER in base64url. A window that has begun makes the
  certificate due at once; one to come, at a time picked at random in it. The certificate is due
  at the earliest of that, its lifetime's renewal time, and when to ask again: the CA's
  Retry-After, in seconds or as a date, kept between a minute and a day, six hours when it says
  nothing. What ARI says counts for the certificate it was asked about only, so a certificate
  another instance renewed meanwhile is taken from the storage as it is.
- A CA without ARI, or one that does not answer, leaves renewal to the lifetime, and is asked again
  after six hours; a failure is logged, and nothing else changes.
- Requests to the CA's ARI run on the ACME workers, with their timeouts and a 64 KiB bound on
  answers.
- `internal/pebbletest` gains `SetRenewalInfo`, which has Pebble suggest a window for a
  certificate.
- Tests: `TestCertID` (RFC 9773's example, a serial without its top bit, no key identifier),
  `TestRetryAfter`; against a CA of the test's: a stored certificate far from due, whose window has
  begun, is renewed at once while it is still served, and the CA is asked by its identifier; a
  window to come renews nothing, is picked within it, and the CA is asked again as Retry-After
  says. Against Pebble: the window it suggests for a certificate it issued is used, within the
  certificate's lifetime, and once Pebble moves the window into the past the certificate is
  renewed. The `tlsmgr` tests pass three times in a row under the race detector, with Pebble.
  `go vet ./...`, `GOOS=windows go vet ./...` and `go test -race ./...` pass.
- No benchmark: nothing on the data plane changed.

Differences from the plan:

- A new order does not name the certificate it replaces (ARI's `replaces`): x/crypto's
  `AuthorizeOrder` has no way to send it. A CA uses it to exempt renewals from rate limits, so a
  renewal ARI asks for is ordered as any other. Looked at again on 2026-10-20, after M2: neither
  x/crypto v0.58.0, the latest release, nor its master of 2026-10-16 (`0ae6809`) can send it.
  `AuthorizeOrder` takes `OrderOption`s, but the interface is sealed and offers only `notBefore`,
  `notAfter` and `profile`, and the client has no exported way to post a signed request of one's
  own. Sending it would mean signing ACME's requests in fit, or another ACME library (acmez and
  lego send it), against decision 007; left until x/crypto has it. Pebble v2.10.1 takes
  `replaces`, so the test is ready to write then.

### I35 M2 exit tests — done

- M2's three exit criteria (roadmap) are three tests in `test/e2e`, which run the fit binary:
  `TestChurn` (I26: 2,000 config swaps through the Admin API under traffic, no request failed),
  `TestAutoHTTPSFromPebble` (I30b: a config naming only a host name is served over HTTPS with a
  certificate from Pebble, and HTTP redirects to it), and `TestHealthCheckRotation`, new: two
  backends behind an upstream with an HTTP health check (200ms interval, 100ms timeout, two fails,
  two passes); one backend's probe path stops answering while its other requests are served, so
  only the health check can take it out. It leaves rotation within the window (fails × interval +
  timeout + an interval of slack, 700ms; measured 380–540ms), gets no request while out, and is back
  within its window (passes × interval + an interval, 600ms; measured 350–380ms); no request fails.
- `make m2-exit` runs the three, and CI gains an "M2 exit tests" job that runs them with Pebble
  required: `pebbletest` fails rather than skips when `$FIT_PEBBLE_REQUIRED` is set. The job passed
  in CI on `929ec73` (run 15), with the lint and fuzz jobs. The Let's Encrypt staging check is
  `TestLetsEncryptStaging` (I30b), run by hand.
- CI's full `go test -race ./...` jobs, on Linux and macOS, have failed on every push since the M1
  review (`d4b9c5b`), unnoticed. Run with three CPUs, as a runner has, the suite reproduced one
  cause: a data race between fit's proxy reading a request's announced trailer names and fib's
  HTTP/2 server setting the request's `Trailer` as the body ended. It was fib's: asked as
  [A19](fib-api-requests.md#a19-a-streamed-requests-trailer-set-where-its-end-is-handed-over), fixed
  and released in fib v1.5.3, which fit now uses; `TestTrailers` passes five times in a row with
  three CPUs, where v1.5.2 failed one in five. The jobs still failed on v1.5.3. Their logs need a
  GitHub sign-in, so CI now writes each failed package's test output to an annotation as well
  (`internal/testsummary`), which anyone can read. That showed the cause:
  `TestStuckClientWithoutStallTimeout` gave the client 5s to read the rest of its 200MB download
  once it read again, which under the race detector on a runner's CPUs it did not finish (h3 on
  Linux; h2c and h3 on macOS; reproduced locally with `-cpu 2`). The stuck clients now give up only
  when 5s pass with nothing new arriving. The next run showed a second cause, a bug of fit's:
  `TestWatch` on Linux missed its first edit. `fit run` starts its listeners before its watcher,
  and the watcher took its first look at the file's size and modification time when it started, so
  an edit made in between, the same size as before, showed no change from then on and was never
  applied. The watcher now starts as if the file had just changed, and its contents decide
  (`TestEditBeforeWatching`). Later, on 5608205, `TestPassiveHealth/h2c` saw two 502s from the
  address that refuses connections where one takes it out: the proxy answered a failed request
  before telling the pool, so on a slow runner the client's next requests could reach the address
  before it was out. The pool now hears of the failure first.

Differences from the plan:

- The certificate test is against Pebble in CI; Let's Encrypt staging needs a public name and
  ports 80 and 443, and is documented to be run by hand, as I30's done-when allows. It passed by
  hand on 2026-10-21 (I30b), so all three of the roadmap's exit criteria have been met as written.
- The plan's I35 has no benchmarks; the comparison with nginx that R07c's notes left for M2's end
  is the [M2 report](https://github.com/lesismal/fit/blob/3658ae9/bench/results/m2-report-2026-10-20.md), run after I35 at 59083f3. The
  harness's summary now names the servers of a run of idle connections alone, which it left out.

### I35b Certificate files watched — done

- `fit run` applies the running config again when a certificate or key file it loaded changes
  (`-watch-certs`, on by default; `-watch-interval` now sets how often both watchers look). A
  `tlsmgr.FileWatcher` polls each file's size and modification time on a goroutine of its own, as
  the config file's watcher does (decision 006), and once they have stayed as they are for 500ms
  reads the files and compares their SHA-256 with what the running config loaded, which
  `LoadStatic` now keeps (`Static.Files`, gathered in `Graph.CertFiles`). A difference applies the
  running config again through `admin.Configs.Reapply`, the same path as every reload, as a new
  version with the source `certificates`; the listeners are kept and only their certificates
  change. So a pair that does not load, a certificate written before its key, fails as any bad
  reload does, is reported once, and leaves the running certificates; the same files are not tried
  again until they change. A file that cannot be read, as while it is replaced, is reported once.
- A config applied another way (SIGHUP, the Admin API, `-watch`) is compared with its files once
  too, since they may have changed after it read them; two listeners that loaded one file
  differently, changed between them, have it loaded again.
- Certificate and key files are read up to 1 MiB (`MaxCertFileBytes`), where
  `tls.LoadX509KeyPair` read them whole.
- The certificate expiry metric lists the certificates obtained on demand: a listener with
  on-demand TLS serves any certificate ACME has for a name its files do not cover, and lists those.
- Tests: `tlsmgr.TestFileWatcher` (a renewal written in two steps applies once, a touch or a change
  undone none, a failing pair is reported once and tried again when it changes, a file gone is
  reported once), `TestFileWatcherNewConfig`, the file sums in `TestLoadStatic`, files past the
  limit in `TestLoadStaticErrors`, on-demand listing in `TestCerts`, and
  `e2e.TestCertificateFilesWatched`: the fit binary is given a new certificate, which does not
  match the key and is reported while the old one is served, then the key, after which handshakes
  get the new certificate.

Differences from the plan:

- No benchmark: nothing on the data plane changes; the watcher stats a few files a second on a
  goroutine of its own.
- Applying the whole config again rather than swapping a listener's certificates in place: a
  reload keeps the listeners, and the upstreams' pools and health (I26), so it costs a compile, and
  it gives a renewal the checks, the rollback and the metrics every reload has.

### R07e Hooks and files through fib (A16–A18) — done

After the M2 report, the maintainer let fit's session make the fib side of A16–A18 itself, and
push and tag it: fib v1.5.4 (`fe60763`, `b39373d`).

- In fib: a recycled Context keeps its response writer, cleared, rather than going to
  `writerPool` for each request that needs one, as an access log's `OnFinish` does (A17).
  `SetValue` and `Value` take no lock: `Context.cancel` is an atomic pointer, and the value is set
  before the request goes to another goroutine. `Context.Status()` is the status of the response
  sent or begun (A18). `SendFileStall` bounds every wait for the client between pieces (A16).
  `Context` stays at 160 bytes, `closing` and `lingers` sharing a byte to make room for the
  status: a first version took 16 bytes more and fib's own tests caught it, with HTTP/2 and
  HTTP/3 streams past their allocation size classes. fib's full race suite passes;
  `TestClientStreamTimeoutMidBody` failed once under the whole suite's load and passes alone 30
  times in a row, on a client path the change does not touch.
- In fit: `module.Inline`, which `respond` implements, makes a route without middleware
  `Route.Inline`; with metrics on and the access log off, the dispatcher counts such a request,
  and its own 404 and redirects, from `Context.Status()` once the handler returns, without an
  `OnFinish` hook. `Status` is read only there: another handler's response may be written on
  another goroutine meanwhile. The static handler hands `SendFileStall` the whole range, and its
  `transfer`, which sent pieces itself, is gone; `TestStuckClient` passes over every protocol.
- On the benchmark host, fit before (59083f3, fib 8c637e0) and after (fib v1.5.4), alone, two
  rounds each, alternating (requests a second, mean):

| Scenario | Protocol | as configured | access log | metrics |
| --- | --- | --- | --- | --- |
| fixed | h2c | 3,798k → 3,769k (−0.8%) | 2,863k → 2,926k (+2.2%) | 3,294k → 3,575k (+8.5%) |
| static | h2c | 2,439k → 2,435k (−0.2%) | 2,054k → 2,098k (+2.1%) | 2,257k → 2,284k (+1.2%) |
| proxy | h2c | 238k → 234k (−1.4%) | 236k → 235k (−0.6%) | 237k → 235k (−0.9%) |

  Over HTTP/1.1, every figure is within 0.6%. Three more rounds of fixed and proxy over h2c as
  configured gave −0.6% and −1.3%. A bisection, fit at 5608205 on fib 8c637e0, on fe60763 (A16,
  which these paths do not use) and on b39373d, gave +0.7% and −0.5% for A16 alone, which
  measures the noise between builds, and −1.3% and −0.4% from A16 to A17–A18; a fixed response
  over h2c with nothing hooked takes no writer, and pays only the status's store. Left as it is,
  within what a build moves.

Differences from the plan:

- The access log gained 2%, not the sixth of its cost A17 estimated: what is left is the line.
- `Received` is not in fib; see A18.

### I35c On-demand names dropped when idle — done

- `acme.on_demand` gains `idle_timeout` (a week when left out): a name taken on demand that no
  handshake has asked for that long is dropped. It no longer counts against `max_names`, its
  certificate is no longer served nor listed in the certificate expiry metric, and its OCSP
  response is given back; the certificate stays in the storage, so the next handshake that asks
  for the name takes it on again from there, with the ask URL's yes and counted against the rate,
  without a new order while it is good. Like the rest of `acme`, it is read at start.
- A handshake records that it used a name in an `atomic.Int64` of the name's, which it reads
  without a lock (a `sync.Map` that changes only when a name is taken on or dropped), and writes at
  most once a second, so that handshakes for one name do not all write to it. ACME's scheduler,
  which already wakes for the next name due, wakes for the next name to go idle too, and drops it
  under ACME's lock; a name being worked on waits for its run to end. A name a reload lists is no
  longer on demand, and is not dropped.
- Tests: `TestOnDemandIdle`, on a clock of the test's: two names taken on from the storage, with
  a CA that is not there, fill `max_names`; one is asked for again at 40 minutes, with an hour's
  `idle_timeout`; at 59 minutes neither is dropped, at 61 the other is, and it is no longer
  served, listed or counted, and there is room for another; asked for again it is taken on from
  the storage with a second ask; the first is dropped an hour after its last handshake.
  `TestOnDemandIdleTimer`: on the real clock, with 100ms, the scheduler wakes for it by itself.
  `go vet` and staticcheck for linux, darwin and windows, and `go test -race ./...` pass, with
  Pebble.
- No benchmark: a handshake for a name taken on demand gains a `sync.Map` load and, at most once
  a second, a store; nothing on a request's path changes.

Differences from the plan:

- None.

### R07f The proxy's CPU per request, profiled (A20–A22) — done

The M2 report put fit's proxy at 86% of nginx over HTTP/1.1, with 1.5 times its CPU per request.

- Profiled on the benchmark host with `perf`, fit at 2b37a8e (fib v1.5.4) and nginx 1.31.4 each
  on 7 CPUs, proxying to a fit backend on 2, under wrk (7 threads, 256 connections) over HTTP/1.1:
  fit 226.8k requests a second at 29.7 µs of CPU each, nginx 265.3k at 20.7 µs. The kernel takes
  about the same for both (fit 15.2 µs, nginx 17.0 µs; 5.9 and 6.3 system calls a request); the
  difference is user time, 14.5 µs against 3.6. Of fit's, fib's code and what it calls take
  10.3 µs, the GC's background marking 1.3 µs, the rest of the runtime 1.2 µs, and fit's own code
  1.8 µs.
- fib's: its client writes each upstream request with net/http's `Request.Write` (1.49 µs) and
  reads each response head with `net/http.ReadResponse` over a new 4 KiB `bufio.Reader`
  (2.23 µs), together an eighth of the CPU and over half of the allocations; its event loops arm
  and disarm their relay epoll instance each time they park (0.48 `epoll_ctl` a request, which
  nginx does not make); and the client resets a connection's idle timer on every request
  (0.23 µs). Asked as [A20](fib-api-requests.md#a20-client-requests-written-and-response-heads-read-without-nethttp),
  [A21](fib-api-requests.md#a21-one-epoll_ctl-per-park-of-an-event-loop-not-two) and
  [A22](fib-api-requests.md#a22-a-client-connections-idle-clock-without-a-timer-reset-per-request);
  A20 is in fib v1.5.6 (below), and A22 in fib `7f632cb`, which fit uses from `a6d78cd`; A21 is
  closed as not doable, since `EPOLLONESHOT` cannot save the call (see A21 in fib-api-requests).
- fit's: the proxy skips hop-by-hop fields, `Content-Length` and `Expect` as it copies the
  request's header, rather than deleting each name after, and `removeHopByHop` (also used on
  responses) looks at each field once; `validToken` reads a table. `BenchmarkOutgoing`, new, which
  builds the upstream request: 227 → 214 ns for a load generator's request, 684 → 541 ns for a
  browser's with nine fields, allocations unchanged (3 and 6). On the benchmark host, fit alone,
  proxy, three rounds each, alternating: HTTP/1.1 227.7k → 227.0k (−0.3%), h2c 234.8k → 234.9k,
  within the noise, as 0.05 µs of 29.7 is.
- `go vet` and staticcheck for linux, darwin and windows, and the proxy's tests under the race
  detector, pass.

- A20 was then made in fib, by this session with the maintainer's leave, released as v1.5.6
  (`0a9339f`, CI run 214 green), and fit moved to it. On the benchmark host, fit at 3f25679 on
  v1.5.5 against the same fit on A20, three rounds each, alternating: the proxy over HTTP/1.1
  226.6k → 245.8k requests a second (+8.4%, about 93% of nginx's 265k), at 26.0 µs of CPU a
  request instead of 29.7; over h2c 235.5k → 238.1k (+1.1%). fit's race suite, with Pebble, and
  `go vet` and staticcheck for linux, darwin and windows pass on v1.5.6.

Differences from the plan:

- What fit could take out is small: most of its 1.8 µs is building the `net/http` header map and
  `Request` that fib's `Client.Do` takes, which A20 leaves as they are. Of the rest of the proxy's gap
  to nginx, A22 has since been made in fib, and A21 is closed as not doable, since `EPOLLONESHOT`
  cannot save the call.

### I36 Matchers v2 and priorities — done

- A route's `match` takes, besides `host` and `path`: `method` (compared as written), `header`
  (names to values: the request must have each header, with one of the values listed as the value
  of one of its fields, or with any value when none are listed), `query` (keys to values, alike,
  compared once unescaped), `remote_ip` (IP addresses and CIDR prefixes the connection's peer must
  be in; an IPv4 address mapped into IPv6 counts as IPv4, X-Forwarded-For is not read) and
  `path_regexp` (Go regular expressions, unanchored unless written with `^` and `$`). Every field
  set must match; within a field, any one entry may. `config.Validate` checks each: methods and
  header names are tokens, query keys are not empty, addresses and prefixes parse, regular
  expressions compile.
- Package `router`: `Matcher` (`Match(*http.Request) bool`), and `Methods`, `Headers`, `Query`,
  `RemoteIP` and `PathRegexp` to build one. `Route.Matchers` must all match. `Router.Match` now
  takes the request rather than its host and path. `runtime` builds a route's matchers when it
  compiles the config, cheapest first (`Route.Matchers`), and `listener`'s dispatcher passes
  `c.Request`.
- The order of routes, in the package doc and the `priority` field's description: the highest
  priority; then the most specific host; then the most specific path; **then the route with the
  most matchers set** (one per field: `method`, `header`, `query`, `remote_ip`, `path_regexp`);
  then the one written first. So, at equal priority and on the same host and path,
  `GET /api` with a header matcher beats `GET /api`, which beats `/api`; a route whose matchers do
  not match gives way to the next.
- How it is built: hosts and paths are looked up in maps as before, but a node now keeps, for each
  path and prefix, every route under it, best first, cut after the first one with no matchers
  (none after it can be picked). A lookup tries the exact host, then the wildcard, then no host,
  and in each the exact path, then the prefixes longest first; a list's matchers run only when its
  first route would beat what has been found, so a lower priority or less specific route's
  matchers do not run once a better route matched on the same host. Matching does not allocate:
  the query is scanned in place and compared while it is unescaped, the remote address parsed with
  `netip`; `PathRegexp` allocates only when Go's `regexp` does.
- Tests: table tests for each matcher (`TestMatchers`: methods, headers present and with values,
  queries escaped with `%` and `+` and invalid escapes, remote IPv4, IPv6, mapped and zoned
  addresses, regular expressions), their errors, and the order (`TestMatcherOrder`: falling
  through to the next route, more matchers beating fewer, specificity beating matchers, priority
  beating both, an exact path falling back to a prefix and then to a less specific host, ties to
  the route written first, and matchers that must not run); `TestMatchersAllocs` checks that
  `Match` with method, header, query and remote IP matchers allocates nothing over 53 routes;
  `config`'s `TestValidate` has the new errors with their positions; `runtime`'s
  `TestCompileMatchers` compiles a config with every matcher and checks its router.
- Checks: `go vet ./...` and `go test -race` on the packages the change reaches (router, config,
  runtime, listener, schema, admin, cmd/fit, handler/..., module, observe, tlsmgr, upstream,
  test/e2e), run on the benchmark host in the `golang:1.27-bookworm` image, pass; `go vet` and
  staticcheck for linux, darwin and windows pass.
- Benchmarks on the benchmark host, `bench/microbench.sh`, 10 alternating runs, main against this
  change: `router`'s `BenchmarkMatch` (101 routes, no matchers) 92.4 ns → 56.5 ns (−39%: entries
  are now passed by pointer rather than copied, and the exact host is tried first), `listener`'s
  `BenchmarkDispatch/bare` 111.0 ns → 109.5 ns (−1.4%), allocations unchanged (0 and 4).
  `bench/ab.sh`, fit alone, the fixed response, three rounds of 10s alternating: HTTP/1.1 778k →
  778k requests a second (−0.1%), h2c 3,842k → 3,852k (+0.2%), both within the noise.

Differences from the plan:

- The matchers are fields of a route's `match`, built into the router, rather than
  `module.Matcher` modules; `module.Matcher` stays for modules that may want one later.
  `remote_ip` is the connection's peer; a client address taken from trusted proxies' headers can
  come with I39, which needs one for its limits.

### I37 Expression matcher — done

- A route's `match` takes `expr`, an expression that combines matchers with `&&`, `||`, `!` and
  parentheses, `&&` binding tighter than `||` as in Go:
  `Host("a.example", "*.b.example") && (PathPrefix("/api/") || Method("POST"))`. Its matchers are
  `Host` (exact or `*.` wildcard, ignoring case, a port and a trailing dot), `Path`, `PathPrefix`,
  `PathRegexp`, `Method`, `Header(name, value...)`, `Query(key, value...)` and `RemoteIP`, with
  string arguments quoted as in Go (`"..."` or `` `...` ``). It is ANDed with the route's other
  fields and counts as one matcher in the order of routes; its hosts and paths do not make the
  route more specific, so a route that has only an `expr` is tried as a route with no hosts or
  paths, and is placed with `priority`, which the field's description says.
- `router.ParseExpr` parses it into an `Expr`, a `Matcher` that `String` prints back in a canonical
  form, with only the parentheses its structure needs. It is a recursive descent parser over a
  small scanner; errors give the byte they are at (`at byte 22: there is no matcher Nope`), which
  `config.Validate` reports at the `expr` field. An expression is at most 64 KiB and nests at most
  100 deep, so a config cannot make the parser recurse without end; a long chain of `&&` is not
  deep. `runtime` compiles it with the route, after the route's other matchers.
- Tests: `TestParseExpr` (precedence, grouping, canonical printing, quoting), `TestParseExprErrors`
  (27 malformed expressions, each with its message and position, the length and depth limits, and
  a chain of 1,001 matchers), `TestExprMatch` (an expression using every operator and most
  matchers, on ten requests), `TestExprAllocs` (evaluation allocates nothing), and
  `FuzzParseExpr`: the parser never panics, and what it parses prints back to an expression that
  parses to the same. Fuzzed for 2 minutes on the benchmark host, 4.5 million inputs, no failure.
  `config`'s and `runtime`'s tests have an `expr` too. The race tests of router, config, runtime,
  listener, schema, cmd/fit and test/e2e pass on the host.

Differences from the plan:

- None in substance. The expression does not feed the router's host and path maps: an expression's
  hosts and paths are checked by evaluating it, after the maps found the route's candidates by its
  `host` and `path` fields, which may be set beside `expr` for that.

### I38 Rewrites, redirects and headers — done

- `middleware/rewrite`, the first middleware fit ships: `rewrite` changes the request's path before
  the handler, and the upstream a proxy sends it to, sees it: `strip_prefix` (the first of a list
  the path starts with at a segment boundary, so `/api` makes `/api/users` `/users` and `/api`
  `/`, and leaves `/apix` alone), `regexp` and `replacement` (Go's `ReplaceAllString`, `$1` and
  `${name}`), and `add_prefix`, in that order. The route was already picked by the path as sent.
  The new path is escaped afresh (`RawPath` is cleared), so a `%2F` the client sent is a `/` once
  rewritten. `redirect` answers with a redirect to `to`, in which `{scheme}`, `{host}`, `{path}`,
  `{query}` and `{uri}` stand for the request's; with `regexp`, only paths it matches are
  redirected, `$1` in `to` standing for its groups, and the rest go on to the handler. `status`
  is 301, 302 (the default), 303, 307 or 308.
- `middleware/headers`: `headers` with `request` and `response`, each `delete` (names, or a prefix
  ending in `*`), then `set`, then `add`. Request changes are made before the handler; response
  changes in an `OnHeader` hook, a function made once at provision, so they reach every
  response whichever way the handler writes it. Names are canonical at provision; each request
  gets values of its own, since what reads a header may change them.
- Both register in `modules/standard`, and the schema has their params.
- `internal/fibtest`'s `Recorder` now keeps a copy of a response's header and body: fib's
  `Respond` hands a pooled header to the stream and clears it once the stream returns, so the
  recorder saw a response's hook-added fields vanish.
- Tests: table tests per middleware, `TestRewrite` (strip prefix at segment boundaries and with a
  trailing `/`, an escaped slash, regexp with numbered and named groups and every match, a
  replacement dropping the leading `/`, add prefix, all three in order), `TestRedirect` (every
  request, permanent, each placeholder over TLS, a fixed URL, regexp matched and not),
  `TestHeaders` (set, add, delete, deletion by prefix, delete everything, request and response,
  values not shared between requests), `TestResponseOfAnyKind` (`Respond` and `Write`), and the
  params' errors. `go vet ./...` and the race tests of middleware, modules, schema, runtime,
  internal, handler and listener pass on the benchmark host; `go vet` and staticcheck for linux,
  darwin and windows pass.
- No benchmark: a route without these middleware runs as before, and they are new.

Differences from the plan:

- `redirect` is in `middleware/rewrite` with `rewrite`. Header values take no placeholders yet.

### I39 Rate, concurrency and size limits — done

- `middleware/limit` has three middleware. `rate_limit`: a token bucket per key, `rate` tokens a
  second up to `burst` (the rate, rounded up, by default), one per request, and 429 (or `status`)
  with `Retry-After` in whole seconds when there is none. `key` is `ip` (the default), `route` (one
  bucket for the route) or `header:Name` (by ip when the header is missing); with
  `trusted_proxies`, the client is the last address in X-Forwarded-For that is not a trusted
  proxy, or the first when all are, as nginx's `real_ip_recursive` does, and a list that does not
  parse is not believed past there. `concurrency_limit`: at most `max` requests under way at once,
  one past it answered 429 (or `status`) at once rather than kept waiting. `body_limit`: a body
  whose `Content-Length` is past `max_bytes` is answered 413 before the handler sees it.
- The buckets are in 64 shards with a lock each, keyed by `netip.Addr` (no allocation) or the
  header's value, values over 128 bytes by a seeded 64-bit hash, so what a key costs is bounded.
  Tokens are counted in nanotokens, integers, so that refills add up exactly. `max_keys` (100000
  by default) bounds the keys: a new key in a full shard evicts one of up to eight it looks at,
  the first whose bucket is full by now, which forgets nothing, or else the first of them.
- A request under `concurrency_limit` ends one of three ways, and each gives its place back once:
  the handler returns without retaining it; its response is sent (`OnFinish`, set before the
  handler runs, since the proxy may answer before it returns); or its client goes. fib's `OnCancel`
  keeps one callback, so the handler's (the proxy's, on every request) replaced the middleware's,
  which `TestConcurrencyClientGone` caught: the place stayed taken. Asked as
  [A26](fib-api-requests.md#a26-more-than-one-oncancel-callback-on-a-context); until then the
  middleware waits on the request's `Context()` with `context.AfterFunc`, asked for only once
  the handler has returned with the request retained (one asked for of a request already over is
  cancelled already), and stopped when the response is sent.
- `body_limit` cannot see a body of no declared length, which goes to the handler's `OnBody`:
  that is bounded by the listener's limits alone until fib has a per-request limit, asked as
  [A25](fib-api-requests.md#a25-a-per-request-limit-on-a-request-body-streamed-or-not).
- Tests: `TestRateBucket` (burst, refusal with `Retry-After`, refill by half and whole tokens, and
  no further than the burst, on a test clock), `TestRateSlow` (0.01 a second, `Retry-After: 100`,
  another status), `TestRateKeys` (by ip, IPv4 mapped into IPv6 as IPv4, by route, by header with
  long values hashed, behind trusted proxies), `TestClientIP` (11 X-Forwarded-For cases),
  `TestRateMemoryBounded` (100000 keys with `max_keys` 640 keep at most 640),
  `TestEvictPrefersFull`, `TestRateAllocs` (a request counted by ip, also through
  X-Forwarded-For, allocates nothing), `TestConcurrency` (inline requests, two held and answered
  elsewhere, a third refused and one let in once one is answered), `TestConcurrencyClientGone`
  (over the network, three rounds of a request that hangs taking the one place and giving it back
  when its client hangs up), `TestBody`, and the params' errors. `go vet ./...` and the race
  tests of middleware, modules and schema pass on the benchmark host; `go vet` and staticcheck
  for linux, darwin and windows pass.
- Benchmarks on the benchmark host (`bench/microbench.sh`, 16 threads in parallel):
  `BenchmarkRate` 16.0 ns a request over 1000 keys, 31.8 ns over a million keys kept to 100000
  (evicting), 126.6 ns with one key for all threads (`key: route`, one shard's lock), no
  allocations; `BenchmarkConcurrency`, an inline handler through fibtest, 315 ns bare against
  506 ns behind the limit, 5 allocations more (the slot, its hook and fib's hook list).

Differences from the plan:

- fib's `middleware/limiter` counts fixed or sliding windows and keeps every key until a sweep;
  fit's is a token bucket with a bound on its keys, keyed as fit's configs need, so it does not
  use fib's.
- `body_limit` covers declared lengths only, until A25; `concurrency_limit` uses the request's
  context until A26. Both are done since fib `cfb4f09`, below.

**Since fib `cfb4f09` (A25, A26).** fit moved to fib's `main` at `cfb4f09`. `body_limit` calls
`Context.LimitBody(max_bytes)`, which refuses a declared length past it, a body that arrived whole
and is longer, and one that streams and has brought more, with 413, and ends a body of no declared
length that grows past it with `ErrBodyTooLarge`, which the handler's `OnBody` sees; fib answers
413 if the handler lets the request go unanswered. `concurrency_limit` registers its own `OnCancel`
beside `OnFinish` before the handler runs, now that fib keeps every callback, and no longer asks for
the request's context. `TestBodyOverNetwork` sends chunked bodies at and past the limit, small
enough to arrive whole and large enough to stream, and declared ones, over HTTP/1.1; `TestBody`
and `TestConcurrencyClientGone` pass as before. The full race suite passes on the benchmark host.
No benchmark: the two middleware are off the hot path of routes that do not use them, and fib
measured `OnCancel` (A26).

### I40 Basic auth and client mTLS — done

- `middleware/auth`'s `basic_auth` asks for a user and password with HTTP Basic authentication
  (401 with `WWW-Authenticate: Basic realm="…", charset="UTF-8"`) and checks them against bcrypt
  hashes, `users` of `username` and `password_hash`. `realm` is fit by default; `user_header` sets a
  request field to the user's name, replacing any the client sent; the Authorization field is
  removed before the handler unless `forward_authorization` is set. `fit hash-password` prints the
  hash of the password on its standard input (`-cost`, 10 by default); `htpasswd -B` makes the same.
- A bcrypt check takes tens of milliseconds of CPU, which would hold up every connection on a fib
  worker (rule 1), so it runs on a pool of its own: a quarter of the CPUs, at least one worker, and
  a queue of 1024. The request waits retained (`Context.Async`), holding its config; once the
  password passes, the rest of the chain runs on the pool's goroutine, as a handler that answers
  from elsewhere does. A check the queue has no room for, or that waited past 5 seconds, is
  answered 503 with `Retry-After: 1`; one whose client has gone is dropped. A password that passed
  is remembered for its user as a SHA-256 keyed with a secret of the middleware's, so the user's
  next requests are let in on the worker at once; each user keeps one, so the memory is bounded by
  the config. A flood of wrong passwords therefore costs at most the pool's share of the CPU, and
  users already in are let in meanwhile. An unknown user's password is checked against another
  user's hash all the same, so that a refusal takes as long either way.
- A TLS listener's `tls.client_auth` asks clients for certificates and verifies them against
  `ca_files`: `mode` `require` (the default) refuses a client without a valid one in the handshake,
  and `verify_if_given` lets a client send none. fib's TLS does the verification itself (the
  `ClientAuth` and `ClientCAs` of the listener's configuration), over HTTP/1.1, h2 and HTTP/3. The
  CA files are watched as certificate files are; a reload that changes the mode or the authorities
  binds the listener anew (its TLS configuration is fixed once bound), and one that does not keeps
  it. `config.Validate` refuses a listener that requires certificates and gets its own through
  ACME when only TLS-ALPN-01 is allowed, whose CA sends no certificate.
- `client_cert` lets a request through only with a certificate the listener verified
  (`VerifiedChains`), and refuses the rest with 403 unless `optional`; `subject_header` sets a
  request field to the subject (`CN=alice,O=Example`), with control characters replaced, removing
  any the client sent. Who a request was authenticated as, the Basic auth user or else the
  subject, is `observe.Entry.User`, which the access log writes as `user` and `auth.UserOf` returns
  to later middleware.
- Tests: `TestBasic` (no credentials, wrong password, unknown user, another user's password,
  remembered and not, a forged user field), `TestBasicForwardAuthorization`, `TestBasicBusy` (no
  room in the pool, a check that waited too long, a remembered password let in while the pool is
  full), `TestBasicParams`, `TestClientCert` (verified, none, unverified, in the clear, optional,
  with `basic_auth`), `TestHeaderSafe`; over the network, `TestClientAuthRequire` and
  `TestClientAuthVerifyIfGiven` (a client of the CA, none, another CA's, over HTTP/1.1, h2 and
  HTTP/3), `TestClientAuthReload`, and `TestBasicOverNetwork` (1 MB bodies chunked and declared
  through `basic_auth` and a `body_limit` after it, the first waiting for its check with the body
  arriving, and one past the limit); `TestLoadClientAuth`, `config`'s validation cases,
  `TestHashPassword`, and the access log's `user`. The race tests of every package touched and
  their dependents pass on the benchmark host; `go vet` and staticcheck for linux, darwin and
  windows pass.
- No benchmark: routes without the new middleware run as before, and a listener without
  `client_auth` gets the same TLS configuration.

Differences from the plan:

- The subject is available to later middleware through `auth.UserOf` and the request field, not a
  general per-request store; I42's claims may need one. bcrypt is `golang.org/x/crypto`'s, a module
  fit already depends on (decision 007), so there is no new dependency to note.
- Basic auth's users are written in the config; a file of users, as htpasswd keeps, is left for
  later.

### I41 ForwardAuth — done

- `middleware/auth`'s `forward_auth` sends a GET to an auth service, an upstream of the config's
  (`upstream`, with its balancing, health and limits), at `uri` (`/` by default), with the request's
  fields (all but the hop-by-hop ones and the body's, or only `request_headers`) and
  `X-Forwarded-Method`, `-Proto`, `-Host`, `-Uri` and `-For`, which replace any the client sent. A
  2xx answer lets the request through, with the fields `copy_headers` names copied from the answer
  and any the client sent of those names removed; any other answer is the client's, its status,
  fields and a body that arrived whole (up to the pool's 64 KiB threshold; a larger one is given up
  and the answer goes without). A service that cannot be reached is answered 502, one past
  `timeout` (10s by default) 504, and a check the config's drain cuts off 503.
- The subrequest goes through fib's asynchronous client (`Pool.Pick`, `Send`, `Done`); the request
  waits retained, holding its config, and the rest of the chain runs in the client's callback, so a
  waiting request holds no worker (rule 1). `OnCancel` abandons the subrequest when the client goes,
  a timer when the timeout passes, and `Drain` the ones still waiting once the config is drained.
  A timeout counts against the auth service's backend, as the proxy counts one.
- Tests: `TestForwardAuth` (allowed, with the fields the service saw and the copied user; a client's
  forged copy field removed; a redirect to a login page passed on with its fields and body; a
  denial with a 200 KiB body passed on without it; a service past the timeout),
  `TestForwardAuthUnreachable`, `TestForwardAuthClientGone` (the service sees its request
  abandoned), `TestForwardAuthSlowService` (128 requests at once wait half a second each on the
  service, more than the server has workers, while another route answers each of ten requests
  within 250 ms; all 128 are let in, together), and `TestForwardAuthParams`. The race tests of
  `middleware/auth`, `schema` and `cmd/fit` pass on the benchmark host; `go vet` and staticcheck for
  linux, darwin and windows pass.
- No benchmark: routes without the middleware run as before.

Differences from the plan:

- The auth service is an upstream of the config, not an address of the middleware's own, so it
  has the pools' limits, health checks and load balancing. The client's own `X-Forwarded-*` are not
  believed, there being no trusted proxies for this middleware yet.

### I42 JWT and JWKS — done

- `middleware/auth`'s `jwt` lets a request through only with a JSON Web Token (a compact JWS)
  whose signature verifies and whose claims hold: `exp` is required and in the future, `nbf`, if
  there, in the past, both within `leeway`; `iss` is `issuer` and `aud` (a string or a list) names
  one of `audience`, when they are set. RS256/384/512, PS256/384/512, ES256/384/512, EdDSA (Ed25519)
  and HS256/384/512 are verified with the standard library; `algorithms` narrows them. `none` never
  passes, nor a header with `crit`, nor an algorithm that does not fit the key: HMAC needs the
  config's own secret, so a public key cannot be taken for one, and an ECDSA algorithm needs its
  curve. The token is after `Bearer` in Authorization, or (`token_from`) in another field, a cookie
  or the query; a request without one is answered 401 with `WWW-Authenticate: Bearer realm="fit"`,
  one with a bad one adds `error="invalid_token"`.
- Keys: `public_key_files` (PEM public keys or certificates), `hmac_secret_file` (32 bytes at
  least), and `jwks_url`, a JWK set that a goroutine of the control plane fetches (net/http, 10s,
  1 MiB at most) when the config is loaded, every `jwks_refresh` (1h by default), and when a token
  names a `kid` the set lacks, at most every 10 seconds. Such a token's request, and one that comes
  before the first fetch is done, waits retained for the fetch under way or the next (at most 1024
  wait; past that they are refused), and is verified again when it is done. A fetch that fails
  keeps the keys there were. Keys for encryption, HMAC keys, RSA keys under 2048 bits and EC
  points off their curve are left out of a set. `Cleanup` cancels the goroutine without waiting.
- The claims go to later middleware: `auth.ClaimsOf`, kept in `observe.Entry.Claims`; `sub` is who
  the request was authenticated as (`UserOf`, the access log's `user`), unless an earlier middleware
  said so. `claims_headers` sets request fields to claims (strings as they are, the rest as JSON,
  control characters replaced), and removes those fields as the client sent them, let in or not.
- Verifying an RSA, ECDSA or Ed25519 signature takes tens of microseconds of a worker's CPU, and a
  token is sent again and again for its life, so tokens that passed are kept by their SHA-256 in 16
  shards of at most 1024, a full shard forgetting one at random; a kept token holds only for the
  keys it was verified with, so a key a JWK set drops stops verifying at once, and its `exp` and
  `nbf` are checked on every use.
- Tests: `TestJWTAlgorithms` (all 13 algorithms; a P-256 key for ES384, another RSA key, none, the
  RSA public key used as an HMAC secret; `algorithms`), `TestJWTClaims` (exp and nbf within and past
  the leeway, exp missing or a string, a fractional exp, issuer, audiences, a tampered payload,
  malformed tokens, crit, a token past 16 KiB, the clock moving past exp), `TestJWTTokenFrom`,
  `TestJWTClaimsHeaders`, `TestJWTParams`, `TestParseJWKS`, `TestJWTCache`, `TestJWKSRotation` (a
  request before the first fetch waits for it; a new kid has the set fetched and waits; a dropped
  key no longer verifies; 20 unknown kids at once fetch it at most every minRefetch) and
  `TestJWKSRefresh` (fetched every 50ms; a set that fails to parse keeps the keys). The full race
  suite passes on the benchmark host; `go vet` and staticcheck for linux, darwin and windows pass.
- Benchmark on the benchmark host (`BenchmarkJWT`, a request through `jwt` and fibtest, 3 runs):
  verified anew, HS256 3.2 µs, RS256 25.7 µs, EdDSA 39.8 µs, ES256 54.5 µs; kept, 0.47–0.65 µs and 5
  allocations.

Differences from the plan:

- No JWT library: the standard library verifies every algorithm, and the parsing is small, so
  there is no new dependency. Claims are kept in the access log's entry, the one per-request store
  fit has, rather than in a store of their own.

### I43 Retries and per-route timeouts — done

- The proxy's `retry` sends a request again, to the backend the upstream's policy picks next, up to
  `attempts` (2 by default) times after the first: one whose connection could not be made (a dial
  error, or `max_pending` reached at the backend) whatever its method, since it never left; and, for
  GET, HEAD, OPTIONS, TRACE, PUT and DELETE (idempotent, RFC 9110), one that failed before its
  response began, or whose response has a status of `statuses` (5xx only; none by default), whose
  body is then given up. A request whose body streams (past 64 KB, or still arriving, or with
  trailers) is never sent again, since its body is gone; a whole one is sent again from its bytes.
  Retries stop at a budget, as Finagle's and Linkerd's do: each request adds `budget_ratio` (0.2) of
  a retry, it gains `budget_min_per_second` (10) a second, holds at most ten seconds' worth, and
  starts with one second's, so that retries add at most about a fifth to an upstream already
  failing. An attempt that failed counts against its backend as before; one retried for its status
  does not.
- The proxy's `timeout` bounds a request from when the proxy takes it to the end of its response,
  retries included: past it before the response began the request is answered 504, and a response
  under way is cut short as an upstream that broke off would be. It does not count against the
  backend. None by default; the upstream's own timeouts still apply.
- Tests (`handler/proxy/retry_test.go`): `TestRetryConnectFailure` (a refused backend and a live
  one: every GET and POST answered by the live one, once each), `TestRetryNotResentOnceSent` (a
  backend that reads the request then closes the connection: a POST reaches it once, a PUT once
  each of 4 attempts or twice where fib's client sends it again itself), `TestRetryStatuses` (a 503
  backend and a 200 one: GETs all answered 200; POSTs answered 503 not retried), `TestRetryBudget`
  (50 GETs to a backend that answers 503, `budget_ratio` 0.2 and no per-second allowance: at most
  10 retries), `TestRouteTimeout` (504 for a slow upstream; a download cut short) and
  `TestRetryParams`. The full race suite passes on the benchmark host.
- `bench/ab.sh` on the benchmark host, the proxy scenario, 3 rounds of 10s, main against this
  change (no retry or timeout set): HTTP/1.1 246k → 245k requests a second (−0.6%), h2c 240k →
  240k (−0.1%), both within the noise.

Differences from the plan:

- Retries are the proxy's, not a `middleware/retry`: sending again needs the proxy's request, its
  backend and its pool. Statuses are retried for idempotent requests only, and the budget is shared
  by the requests of one proxy (one route), not of the upstream.

### I44 Circuit breakers — done

- `middleware/breaker`'s `circuit_breaker` is a breaker on a route's requests, with three states.
  Closed, it lets them through and counts, over the last `window` (10s, in ten steps), the
  responses that are failures (5xx, or a status of `statuses`) and those slower than
  `slow_threshold` to begin (from the request to its header; none by default); once at least
  `min_requests` (20) were counted and the failures reach `error_ratio` (0.5), or the slow ones
  `slow_ratio` (0.5), it opens. Open, it answers every request 503 at once, with `Retry-After`
  saying when it will try again, for `open_duration` (30s). Then half-open, it lets
  `half_open_requests` (5) through, answering the rest 503: if they all fare well it closes, with
  an empty window, and if one fails or is slow it opens again. A request whose client goes before
  its answer counts for nothing, and a half-open one gives its place back.
- Outcomes are heard in an `OnHeader` hook, whichever way the handler answers, and the client
  leaving in `OnCancel`; the state and the window are behind one lock, taken twice a request.
  `State` reports the state.
- Tests, on a test clock: `TestBreakerTransitions` (closed under the ratio and under
  `min_requests`; open past it, 503 with `Retry-After` 30 and then 10; half-open after
  `open_duration`; a failed probe opens it again; probes that fare well close it, with a fresh
  window), `TestBreakerHalfOpenLimit` (only `half_open_requests` at once; a cancelled probe's place
  given back), `TestBreakerWindow` (failures that left the window do not count),
  `TestBreakerSlowAndStatuses` and `TestBreakerParams`. The full race suite passes on the benchmark
  host; `go vet` and staticcheck for linux, darwin and windows pass.
- Benchmark on the benchmark host (`BenchmarkBreaker`, an inline handler through fibtest, 3 runs):
  75.6 ns bare, 444 ns behind a closed breaker, 9 allocations more (the outcome, its two hooks,
  fib's hook lists).

Differences from the plan:

- The breaker is a route's, as a middleware, not an upstream's: routes that share an upstream each
  have their own. Latency counts towards it as a share of slow responses rather than an average.

### I45 Traffic splitting: weights, mirroring, failover — done

- The proxy takes `upstreams`, in place of `upstream`: upstreams with a `weight` each (1 to 1000),
  over which requests are spread at random by weight, as a canary release does; each upstream
  then picks the backend by its own policy, and a request's retries stay in its upstream.
- `failover` names an upstream that takes the requests when none of the chosen upstream's backends
  is in rotation (taken out for failing, `max_fails`, or by its health checks) and one of its own
  is; `upstream.Pool.Available` tells. Until then the chosen upstream is used as before.
- `mirror` sends a copy of a share (`percent`, all by default) of the requests whose bodies arrived
  whole (64 KB at most) to another upstream, with the same method, path, fields and body, and drops
  its answers. The copy is made before the request goes on, with a header, URL and body of its own,
  since it may outlive the request, and goes through the mirror's pool asynchronously, so the
  request never waits for it. At most 256 copies are under way at once for a proxy; past that,
  requests are not copied, so a slow or dead mirror costs that much and no more.
- Tests: `TestSplitWeights` (800 requests at 3:1 land within five deviations of 600 and 200),
  `TestSplitFailover` (a refused upstream with `max_fails` 1: its first request 502, the rest to the
  failover), `TestMirror` (the copy has the method, path, query, fields and body), `TestMirrorDoesNotSlow`
  (400 requests at once beside a mirror that takes a second to answer are all answered within
  0.9s, and it gets between 1 and 256 copies), `TestMirrorPercent` (25%: about 100 of 400) and
  `TestSplitParams`. The race tests of `handler/proxy`, `upstream`, `config`, `runtime`, `admin`,
  `schema`, `cmd/fit` and `test/e2e` pass on the benchmark host.
- `bench/ab.sh` on the benchmark host, the proxy scenario, 3 rounds of 10s, main against this
  change (one upstream, no mirror or failover): HTTP/1.1 246k → 246k requests a second (−0.0%),
  h2c 240k → 240k (+0.0%).

Differences from the plan:

- The split is the proxy's, which picks the upstream per request, rather than package `upstream`'s;
  `upstream` gained only `Pool.Available`. Streamed request bodies are not mirrored, since copying
  them would mean buffering them.

### I46 Compression — done

- `middleware/compress`'s `compress` compresses responses with zstd, brotli or gzip, the one the
  request's `Accept-Encoding` prefers most (q-values, `*`, `x-gzip`), and among those it prefers as
  much the first of `encodings` (zstd, br, gzip by default). A response is compressed when its
  status carries a body and is not 206, it has no `Content-Encoding` or `Content-Range` of its own,
  its `Cache-Control` does not say `no-transform`, its type is one that compresses (fib's
  `compress.Compressible`: text, JSON, JavaScript, XML, SVG, fonts), and its declared length, if
  any, is at least `min_length` (1024). Such a response gets `Vary: Accept-Encoding`, also when the
  client accepts none of the encodings or the request is HEAD, and a strong `ETag` is made weak.
  `level` is fastest, default (gzip 5, zstd's default, brotli 4) or better (gzip 7, zstd's better,
  brotli 6).
- Compression is a fib body filter ([A27](fib-api-requests.md#a27-a-hook-that-transforms-a-response-body-as-it-is-written),
  `Context.OnBodyFilter`, made by this session in fib and pushed to its `main` as `fc7ec5e`): the
  body is compressed as it is written, so a response that streams, the proxy's past 64 KB or
  server-sent events, stays streamed, and what the handler flushes is flushed out of the encoder
  too; a response sent whole is compressed whole. Nothing is buffered beyond an encoder's window.
- Encoders come from pools per encoding and are reset for each response; zstd runs with one
  goroutine's concurrency (its caller's), and every encoder's window is 256 KB, so one costs a few
  hundred kilobytes. `max_in_flight` (1024) bounds the responses compressed at once: past it they go
  uncompressed. A response gives its place back when its body ends, or, when its client goes first,
  in `OnCancel`, leaving its encoder to the collector rather than the pool, since a write may still
  hold it.
- New dependencies, `github.com/klauspost/compress` v1.20.3 (zstd, gzip) and
  `github.com/andybalholm/brotli` v1.2.6, both pure Go and with no requirements of their own:
  [decision 008](decisions/008-compression-libraries.md).
- Tests: `TestCompress` (over HTTP/1.1 and h2c: each encoding, preferences and q-values, `*`,
  `identity` and q=0, a whole response with an ETag and a large one written in parts, each
  decompressed to the body and at most a tenth of it on the wire; small, image, already encoded,
  206 and no-transform responses left alone; HEAD), `TestCompressStream` (a server-sent event,
  through each encoding, arrives decompressed before the handler writes the next),
  `TestCompressBounded` (`max_in_flight` 1: a second response goes uncompressed, and the place
  comes back once the streaming client goes), `TestNegotiate` and `TestCompressParams`. The full
  race suite passes on the benchmark host; `go vet` and staticcheck for linux, darwin and windows
  pass.
- Benchmark on the benchmark host (`BenchmarkCompress`, a 16 KB JSON body sent whole through
  fibtest, 3 runs): uncompressed 2.3 µs; gzip 8.8 µs fastest and 9.1 µs default; brotli 10.6 µs and
  16.6 µs; zstd 6.2 µs and 6.4 µs; 21 or 22 allocations, about 2 KB.

Differences from the plan:

- It needed fib's body filters (A27), which this session added to fib: fib's only hook that saw a
  body held streamed responses whole. fib's own `middleware/compress` (gzip and deflate, through
  `OnResponse`) is not used, but its `Compressible` is.

### I47 WebSocket proxying — done

- The proxy passes a request that asks to switch protocols (HTTP/1.1 `Connection: Upgrade` with
  `Upgrade`, WebSocket or another) on to its upstream as an HTTP/1.1 upgrade, with its fields and the
  forwarding ones, through the upstream's pool (`upstream.Pool.SendUpgrade`, fib's
  `Client.StartUpgrade`, A29), so the handshake has the pool's dial, TLS, limits and balancing. The
  request waits retained, holding no worker. If the upstream answers 101, the client's request is
  switched with the upstream's answer, its subprotocol and extensions as chosen, from the upstream
  client's callback (`Context.Upgrade` on a retained request, A28), and the two tunnels are joined;
  any other answer is the client's, and no answer is 502, 503 or 504 as for any request.
- The relay passes each side's bytes to the other as they come, untouched, and holds a side's reads
  while the other has more than 64 KB unsent: over HTTP/1 `Connection.NotifyOutput` says when it has
  drained, with nothing waiting; both connections' own write backpressure is off, as fib's relay
  has it, since the relay paces them. A side that ends ends the other once what it sent has gone. A
  tunnel that carries nothing either way for `tunnel_idle_timeout` (10 minutes by default) is
  closed. A drained config closes its tunnels, and a client that goes before the 101 abandons the
  handshake. The backend counts as under way until the tunnel ends.
- Tests (`websocket_test.go`, `websocket_unix_test.go`): `TestWebSocketEcho` (the 101 with the
  key's accept and the chosen subprotocol, the upstream's handshake with its fields, an echo, 16 MB
  each way at once, the upstream's connection ended when the client's is), `TestWebSocketBackpressure`
  (a client that does not read for a second: the upstream had sent less than half of 32 MB, held
  back, and the client then gets all of it), `TestWebSocketRefused` (a 403 with its body and fields;
  no upstream, 502) and `TestWebSocketIdleTimeout`. The full race suite passes on the benchmark host;
  `go vet` and staticcheck for linux, darwin and windows pass.
- Idle WebSocket memory on the benchmark host, with the harness's idle mode (`-idle ws`), 10,000 connections through to a backend
  that holds them (`bench/results/i47-ws-idle-2026-10-21.json`): fit 5,761 bytes a connection,
  nginx 18,176. fit's harness config gained the `/ws` route, and fit the `ws` idle kind.

Differences from the plan:

- It needed three things of fib, which this session added (A28-A30): upgrading a retained request
  after its handler returned, a client request whose 101 hands over its connection, and extended
  CONNECT from the HTTP/3 client. A test of the backpressure first stalled; that was the test
  shrinking its socket's receive buffer after connecting, which leaves Linux probing a zero window,
  not the relay: the buffer is now set before connecting.

### I48 WebSocket over HTTP/2 and HTTP/3 entries — done

- An extended CONNECT for `websocket` (RFC 8441 over h2 and h2c, RFC 9220 over h3) goes upstream as
  the HTTP/1.1 handshake it stands for: GET with `Connection: Upgrade`, `Upgrade: websocket`, a
  `Sec-WebSocket-Key` of fit's making (an HTTP/2 or HTTP/3 WebSocket has none), and the client's
  `Sec-WebSocket-Version`, `-Protocol` and `-Extensions`. The upstream's 101 becomes the 200 that
  opens the client's stream, without `Sec-WebSocket-Accept`. An extended CONNECT for another
  protocol is answered 501. Pseudo-header fields a server kept, as `:protocol`, are not sent
  upstream.
- The client's tunnel is a stream of a multiplexed connection, whose drain fib can only be waited
  for (A31): while its reads are held, one goroutine a held tunnel waits in `WaitSendable`, as fib's
  own CONNECT relay does, until A31 replaces it.
- Tests: `TestWebSocketExtendedConnect` opens a WebSocket over h2 (TLS), h2c and h3 entries, each
  through to an HTTP/1.1 upstream: the 200 with the chosen subprotocol and no accept, the upstream's
  handshake with a key and version, an echo and a megabyte each way. net/http's Transport refuses a
  `:protocol` field, so the h2 and h2c clients are a bare HTTP/2 client written with fib's `hpack`;
  h3 uses fib's HTTP/3 client (A30). They pass on the benchmark host.
- No benchmark beyond I47's: the proxy's other requests take one more check, whether the request
  asks to switch.

Differences from the plan:

- None in substance; the stopgap above waits for A31.

### I49 gRPC proxying — done

- A gRPC call, an HTTP/2 or HTTP/3 POST of `application/grpc` or a subtype of it, is proxied as any
  request is, its messages streaming both ways as they come and the upstream's trailers, with
  `grpc-status` and `grpc-message`, following its response; it needs an h2 or h2c upstream. What
  the proxy adds: `TE: trailers` goes upstream; `grpc-timeout` bounds the call at the proxy as the
  route's `timeout` does, the shorter of the two winning, and the upstream is told the one chosen
  (a route's timeout becomes the call's `grpc-timeout` when it is the shorter or the call set
  none); and a call the proxy cannot complete is answered as gRPC answers, not with an HTTP status
  its client would not read: before the response began, a trailers-only response (status 200 with
  `grpc-status` and `grpc-message`): `UNAVAILABLE` for no upstream, a refused or failed connect, a
  drain or `max_pending`; `DEADLINE_EXCEEDED` for a timeout; `RESOURCE_EXHAUSTED` for a body too
  large; and on a response cut short, those trailers in place of the `Fit-Error` one. Neither
  timeout counts against the backend.
- A gRPC client may wait for the answer to its first small message before it sends the next, so
  the listener hands a gRPC call's body to the proxy from its header rather than once 64 KB of it
  has arrived: [A32](fib-api-requests.md#a32-a-request-body-streamed-from-its-header-picked-per-request),
  `Config.StreamRequestBodyAtHeader` in fib's `http` and `http3`, made by this session and pushed to
  fib's `main` (`0bd2447`), with fib's HTTP/2 client sending `te: trailers`, which it
  used to drop.
- Tests (`handler/proxy/grpc_test.go`), through fit to a gRPC server of fib's over h2c and h2 (TLS),
  with fib's gRPC client and its JSON codec: `TestGRPCUnary` (an echo, 200 KB, the upstream's
  status and trailer passed on, a deadline of about 5s seen upstream), `TestGRPCServerStreaming`
  (2,000 messages back), `TestGRPCClientStreaming` (500 messages of 1 KB up, one answer),
  `TestGRPCBidiStreaming` (50 messages, each answered before the next goes, 300 KB ones among
  them), `TestGRPCNoUpstream` (`UNAVAILABLE`), `TestGRPCUpstreamGone` (a server stream whose
  upstream stops after two messages ends `UNAVAILABLE` after them), `TestGRPCTimeouts` (a call's
  `grpc-timeout` of 300ms and a route's of 2s each answered `DEADLINE_EXCEEDED` by fit while the
  upstream hangs, the route's carried upstream as the call's deadline, and the backend still in
  rotation after) and `TestGRPCHelpers`. Without A32 `TestGRPCBidiStreaming` stalls on its first
  answer. The race tests of `handler/proxy`, `listener` and the packages that depend on them pass
  on the benchmark host, as do fib's `http`, `http3` and `grpc` with A32; `go vet` and staticcheck
  pass for linux, darwin and windows (and freebsd for fib).
- `bench/ab.sh` on the benchmark host, the proxy scenario, 3 rounds of 10s, `fa1886a` against this
  change with fib at `9ab22e8`: HTTP/1.1 245k → 244k requests a second (−0.3%), h2c 238k → 237k
  (−0.5%), both within the noise. fib's micro-benchmarks on the
  benchmark host (`bench/microbench.sh`, 10 runs each), `main` against the branch: the server's
  `ServerKeepAlive` and `ServerKeepAlivePOST` unchanged within the noise, the bare parser's
  `ParserKeepAliveGET` +5.9% and `ParserContentLength` +4.5%, though neither reaches the new code;
  `main` with only the new `Config` field added costs those two +2.5% and +1.8%, and the rest is
  code layout.

Differences from the plan:

- It needed A32 of fib, which this session made. The listener's read body timeout (fib's 5 minutes)
  still bounds a call whose client streams, and the upstream's `response_idle_timeout` (60s) a
  call silent that long: a longer stream needs them raised.

### I50 TCP proxying — done

- A listener whose `protocols` are `["tcp"]` serves raw TCP, and the service that lists it in the
  new top-level `services` proxies its connections to the service's upstream, byte for byte:
  `{"name": "pg", "listeners": ["db"], "upstream": "postgres"}`. A tcp listener serves nothing else
  and takes no `tls`; each is one service's, and routes cannot name it. The upstream is the one HTTP
  uses: its addresses, its `load_balancing` (`ip_hash` by the client's address; the header, cookie
  and URI hashes, which a connection has none of, in turn), `max_fails` and health checks, and its
  `connect_timeout` (`upstream.Pool.ConnectTimeout`); a connect that fails is tried on the next
  address picked, up to three. A service's `idle_timeout` (10 minutes) closes a connection that
  carries nothing either way, and its `max_conns` (10,000) bounds the connections it proxies at once:
  one past it is closed as it is accepted, beside the listeners' own `limits.max_conns`.
- Package `l4` relays without waiting, as I47's relay does: the upstream's connection is dialed
  through fib's engine asynchronously (`Engine.DialWithHandler`), while the client's reads are
  held; each side's data is sent on from the callback that received it; a side's reads are held
  while the other has more than 64 KB unsent, and lifted when `Connection.NotifyOutput` says it has
  drained; both connections' own write backpressure is off. A side that ends ends the other once
  what it sent has gone (no half-close, as nginx's `proxy_half_close` is off by default). A
  connection keeps the graph it started on: a reload that keeps the listener sends new connections
  to the new service, and the drain timeout closes the old ones.
- Tests (`l4/l4_test.go`, `config`): `TestTCPEcho` (an echo, and 50 connections at once),
  `TestTCPLarge` (32 MB each way at once), `TestTCPSlowClient` (a client that reads nothing for a
  second while its upstream sends 64 MB: the upstream had sent less than half, and the client then
  gets all of it), `TestTCPSlowUpstream` (the same the other way), `TestTCPHangUp` (what the upstream
  sent before closing reaches the client, then the end; the upstream hears the client close),
  `TestTCPConnectFailure` (a refused upstream closes the client; a refused address and a live one,
  every connection reaches the live one), `TestTCPIdleTimeout`, `TestTCPMaxConns`, `TestTCPReload`
  and the config's checks. A client that wrote and closed while its upstream was still being dialed,
  its reads held, was never heard to close: fib lost a peer's end seen while reads were held
  ([A33](fib-api-requests.md#a33-a-peers-end-seen-while-reads-are-held-is-not-lost)), which this
  session fixed in fib. The race tests pass on the benchmark host, `l4`'s 28 times over; one earlier
  run of `l4` failed once and did not again, its cause not caught.
- `BenchmarkTCPProxy` on the benchmark host, one connection echoing 256 KB at a time on loopback
  (5 runs): 11.0 GB/s straight to the echo server, 2.18 GB/s through fit. No `bench/ab.sh`: the HTTP
  path takes one more check per listener binding, none per request.

Differences from the plan:

- TCP services are a top-level `services` list rather than a handler on routes, since a connection
  has no request to route by; I52's SNI routing will add a match to them. A service has no access
  log or metrics yet.

### I51 UDP proxying — done

- A listener whose `protocols` are `["udp"]` takes datagrams, and the service that lists it proxies
  them to its upstream: a service's `listeners` may now name tcp and udp listeners alike, so one
  service can serve DNS on both, and a tcp and a udp listener may share an address (the config
  checks addresses per transport, so HTTP and HTTP/3 count too). A session is a client's address:
  fib makes each address that sends the listener a datagram a connection of its own, and its
  session dials a UDP socket of its own to an upstream address picked as for TCP (`ip_hash` by the
  client's address), so the upstream sees one address per client. Each datagram either way is sent
  on as one datagram, whatever its size; those that arrive before the session's socket is open
  wait, up to 64 KB, and the rest are dropped, as is a datagram that cannot be sent: UDP has no
  backpressure. A session ends after `idle_timeout` with nothing either way (1 minute by default
  for UDP, 10 for TCP), on a drain, or when its upstream refuses it (the ICMP refusal closes its
  socket, which counts against the backend); the client's next datagram starts another. `max_conns`
  counts sessions with connections, and a session past it is closed as it opens.
- The engine's own sweep of silent UDP peers is one timeout for the whole engine, which HTTP/3 also
  relies on, so a session opts out of it and keeps its own timer, as a TCP relay does:
  [A34](fib-api-requests.md#a34-a-udp-peers-own-idle-timeout-or-none),
  `Connection.SetUDPIdleTimeout`, made in fib by this session and pushed to fib's `main`
  (`11de9eb`).
- Tests (`l4/udp_test.go`, `config`): `TestUDPEcho` (datagrams of 1, 1,400 and 60,000 bytes (9,000 on macOS and the BSDs, which send none past 9,216 by default) each
  come back whole; 200 clients at once, 10 round trips each, each one session and one address at
  the upstream), `TestUDPSessionsExpire` (an idle session ends after 300 ms while a busy one keeps
  its socket, and the idle client's next datagram comes from a new one), `TestUDPMaxConns`,
  `TestUDPRefused` (a refused upstream ends the session; with a refused and a live address, the
  client is answered by the live one) and `TestUDPReload` (a session keeps its service until the
  drain timeout, and then goes to the new one); and the config's checks. They pass under the race
  detector on the benchmark host, the UDP tests 10 times over; fib's `TestUDPPeerIdleTimeout` too.
- No benchmark: UDP is a new path, and HTTP's takes one more comparison per listener binding.

Differences from the plan:

- It needed A34 of fib, which this session made and pushed to fib's `main`.
  fib's own `TestUDPSendBatch`, `TestUDPSendBatchSegments` and `TestUDPBurstFromManyPeers` fail in
  the benchmark host's Docker container on fib's `main` as on the branch; they are not this
  change's, and fib's CI is where they are judged.

### I52 SNI routing and PROXY protocol — done

- A service's `server_names` (`"a.example.com"`, `"*.example.com"` for one label below) route the
  TLS connections of its tcp listeners by the name their ClientHello asks for, without terminating
  them: the bytes go on untouched, so the backend's own certificate completes the handshake. A tcp
  listener may now be listed by several services, at most one of them without `server_names`,
  which takes what no name matches, connections that are not TLS and hellos that name none; with
  no such service they are closed. The listener reads the hello, across records, up to 64 KB and
  within 10 s (`l4.OpeningTimeout`), before a service takes the connection, which then dials its
  upstream with what was read waiting to be sent first. Package `l4`'s `Entry` is what a tcp or udp
  listener now hands its connections to.
- The PROXY protocol, v1 and v2. A listener's `proxy_protocol: {"trusted": [...]}` takes the header
  at the start of each connection from those addresses (a trusted connection without one is closed;
  one from elsewhere is served as it is), and the client it names becomes the connection's peer:
  for tcp listeners, for `ip_hash` and what is sent on; for listeners of HTTP over TCP, with or
  without TLS (the header comes before TLS), the requests' `RemoteAddr`, which `X-Forwarded-For` and
  the access log then name. That needed [A35](fib-api-requests.md#a35-a-connections-peer-address-replaced-as-a-proxy-header-gives-it),
  `Connection.SetRemoteAddrPort`, made in fib by this session and pushed to fib's `main` (`530d222`).
  A service's `proxy_protocol: "v1"` or `"v2"` sends its upstream a header first, naming the client
  and the address it connected to (the incoming header's, when there was one).
- Tests (`l4/codec_test.go`, `l4/sni_test.go`, `config`): `TestReadSNI` (a crypto/tls hello whole,
  in records of 100 bytes, every prefix of it, one with no name, and HTTP), `TestProxyRoundTrip` and
  `TestParseProxy` (both versions, IPv4 and IPv6, every prefix, UNKNOWN, LOCAL, a Unix family, a
  TLV, and malformed headers), fuzzing of both parsers (30 s each on the host), `TestEntryPick`,
  `TestSNIRouting` (two TLS backends picked by exact and wildcard names, the handshake with the
  backend's certificate, a client in the clear to the fallback, and a name no service takes closed),
  `TestSNIHalfHello`, `TestProxyProtocolToUpstream` (the v1 line and v2 header the upstream gets),
  `TestProxyProtocolFromClient` (v1 and v2 from a trusted client, split across writes, passed on as
  v1; a trusted client without a header closed; an untrusted one served as itself) and
  `TestProxyProtocolHTTP` (the upstream's `X-Forwarded-For` is the header's client, v1 over IPv4 and
  v2 over IPv6, or the untrusted client itself); and the config's checks. They pass under the race
  detector on the benchmark host, `l4` 10 times over, with the full suite; fib's
  `TestSetRemoteAddrPort` too.
- No benchmark: a listener of HTTP without `proxy_protocol` serves as before, and the tcp relay's
  callbacks pick between two attachment types where they asserted one.

Differences from the plan:

- PROXY headers are sent by tcp services, not yet by the HTTP proxy to its upstreams, nor taken on
  UDP or HTTP/3; and it needed A35 of fib.

### I53 One port, many protocols; SOCKS — done

- A listener's `protocols` may now list `tcp` and the new `socks` beside HTTP's: one that serves
  more than one of HTTP, tcp and socks tells them apart on its port by what a client sends first,
  with fib's `mux` (`listener.Server.sniff`). A TLS ClientHello goes to the service whose
  `server_names` hold its name, passed through as in I52; else, on a listener with `tls`, it is
  terminated and served as HTTPS (HTTP/1.1 or h2 by ALPN); else it goes to the listener's service
  without `server_names`. A SOCKS greeting goes to SOCKS; an HTTP/1.1 request or an h2c preface, on a
  listener in the clear, to HTTP; anything else to the service without `server_names`, or it is
  closed. A client silent for 10 s is closed, so a protocol whose server speaks first needs a
  listener of its own. The PROXY header, from `proxy_protocol`'s trusted addresses, is read before
  any of this. `tls` on a listener now means its HTTP is HTTPS; one that serves no HTTP takes none.
- The handoff: `l4.Entry.Open` takes the listener's TLS handler (`term`), and a TLS connection no
  service names is handed to it once its hello is read (or at once when no service has names), with
  what was read, through `Connection.SetHandler`, as fib's mux hands off; the graph is let go there,
  as each request takes its own.
- `socks` alone, or beside the others: SOCKS5, SOCKS4 and SOCKS4a CONNECT through fib's `socks`
  server, without authentication; BIND and UDP ASSOCIATE are refused. The listener's `socks` says
  who may use it and where to (`config.ForwardProxy`): `clients` (required), `allow` and `deny`
  (host names, `*.example.com` for every name below it, addresses and CIDR ranges), `ports`,
  `connect_timeout` (10 s) and `idle_timeout` (5 min). Ranges also hold the address a host name
  resolves to: `l4.Rules.Dial` dials such a name through fib (which resolves it on its own pool),
  checks the address it connected to in the connection's `OnOpen`, and closes one the rules refuse
  before the SOCKS server hears of it (reply 2). A change to the rules binds the listener anew, as a
  change of limits does.
- fib's SOCKS relay had no idle timeout (rule 10): [A36](fib-api-requests.md#a36-an-idle-timeout-for-socks-and-connect-relays),
  `socks.Config.IdleTimeout`, made in fib by this session and pushed to fib's `main` (`4f1c3e9`).
- Tests (`l4/forward_test.go`, `l4/sniff_test.go`, `test/e2e/multiproto_test.go`, `config`):
  `TestRulesDest` (clients, names, wildcards, ranges, ports, deny before allow, names left to their
  address), `TestSOCKS` (by address and by `localhost`, which only its address allows; a port, an
  address and BIND refused; a name whose address deny holds refused once connected; a client not
  allowed), `TestSOCKSIdleTimeout`, `TestSniffClear` (HTTP/1.1, h2c, TLS passed through by its name,
  SOCKS and raw bytes on one port in the clear) and the e2e `TestOnePortManyProtocols` (HTTPS over
  HTTP/1.1 and h2 with fit's certificate, TLS passed through to a backend with its own, SOCKS and raw
  TCP on one port; a listener with tls whose only service has no names, and one of HTTPS and SOCKS
  without tcp). The full race suite passes on the benchmark host, with fib's `http`, `socks`, `mux`
  and `multiproto` packages.
- No benchmark: a listener that serves HTTP alone is bound as before, and one that sniffs pays only
  at its connections' start.

Differences from the plan:

- The CONNECT handler went to I53b. Custom protocols on a sniffing port are the tcp services'; there
  is no matcher of fit's own for them. SOCKS has no user and password yet: fib's `Authenticate` runs
  on the worker, where bcrypt would block it.

### I53b The CONNECT forward proxy — done

- The `forward_proxy` handler serves CONNECT over HTTP/1.1, HTTP/2 and HTTP/3, on a route that
  matches it (`"match": {"method": ["CONNECT"]}`), with the rules SOCKS has as its params: a client
  or destination they refuse is answered 403, a host name whose address they refuse 502, a dial past
  `connect_timeout` 504; a request that is not a CONNECT 405. It is fib's `ConnectProxy`, whose
  `Allow` checks the client (the PROXY header's, when there was one) and the destination, and whose
  `Dial` is `l4.Rules.Dial`.
- fib's `ConnectProxy` waited for its dial on the worker serving the request (rule 1):
  [A37](fib-api-requests.md#a37-connectproxy-answers-from-its-dials-callback) has it retain the
  request and answer from the dial's callback, and A36 gave its tunnels an idle timeout.
- Tests (`handler/forward`): `TestConnect` (by address and by `localhost`; a port, an address and
  a target that is not a host and port refused; a name whose address deny holds, 502; a client not
  allowed), `TestConnectH2C` (a tunnel on an h2c stream, Go's own client) and
  `TestConnectIdleTimeout`; fib's `TestConnectProxyDoesNotWait` and `TestConnectProxyIdleTimeout`.
  They pass under the race detector on the benchmark host.
- No benchmark: a new handler, off every other route's path.

Differences from the plan:

- Its tunnels are not closed when their graph is drained, as a tcp service's are: they end by their
  idle timeout, their peers, or the listener's own drain. Requests for absolute `http://` URLs, a
  plain HTTP forward proxy, are answered 405.

### I54 Upstream TLS — done

- What I18 left of TLS to upstreams: an upstream of protocol `h2` takes `tls_cert_file` and
  `tls_key_file`, a client certificate (and its chain) and its key, which fit presents to servers
  that ask for one (mutual TLS), on the proxy's requests and the health checks' probes alike, beside
  I18's `tls_ca_file` (custom CAs) and `tls_server_name` (the name sent in SNI and checked). The
  config refuses one without the other, and either without `h2`; a file that cannot be loaded, or a
  key that does not match, is an error at `tls_cert_file`.
- The upstream's TLS files, CA, certificate and key, are watched as a listener's certificate files
  are (I35b): a change loads the config again, and their contents are part of what decides whether
  a reload keeps the upstream's pool, so a new certificate gets a new pool, and an unchanged one
  keeps its connections.
- Tests (`upstream/tls_test.go`, `runtime`, `config`): `TestClientCertificate` (a backend that
  requires a certificate from its own CA, with a certificate for example.com reached by address with
  `tls_server_name`: requests and probes reach it over HTTP/2 with the client certificate, and fail
  without one), `TestTLSCertFileErrors`, `TestSharedPools` (the files watched; a changed certificate
  and key, a new pool) and the config's checks. The full race suite passes on the benchmark host.
- No benchmark: the TLS configuration is built once per pool.

Differences from the plan:

- No setting skips the verification of upstreams' certificates, and the cipher suites are Go's for
  TLS 1.2 and up, as I18 noted they might be; tcp services' upstreams still take no TLS of fit's.

### I55 M3 exit tests — done

- M3's three exit criteria (roadmap), as `make m3-exit` and a CI job of their own, "M3 exit tests":
  - **No middleware puts a blocking call on a worker, and each has a benchmark.** The new package
    `test/workers` serves every middleware and handler fit registers, each with something slow
    behind it: the handler it wraps answers 600 ms later from a timer, `forward_auth`'s auth
    service and `proxy`'s upstream take 600 ms, `basic_auth` checks a bcrypt hash on its pool.
    `TestNoModuleBlocks` checks that ServeHTTP returns within 200 ms and that the answer still
    comes only once the slow part has given it (but for those that answer themselves: `redirect`,
    `respond`, and `static` once its file is cached). `TestEveryModuleCovered` fails for a module
    registered without a case, so a new middleware cannot skip the check; `forward_proxy` is left
    to fib's `TestConnectProxyDoesNotWait`, since a CONNECT needs a connection to switch.
    `BenchmarkModule` is each module's benchmark, with nothing slow behind it. Each middleware's
    own package tests are its conformance tests.
  - **WebSocket, gRPC (unary and streaming) and TCP/UDP proxying, with backpressure under slow
    peers**: the WebSocket tests (echo, extended CONNECT from h2, a client that reads nothing while
    the upstream floods), the gRPC tests (unary, server, client and bidirectional streaming, over h2c
    and h2 upstreams), and TCP and UDP's (echo, slow client, slow upstream). gRPC had no slow-peer
    test: `TestGRPCBackpressure`, new, has a client that reads nothing hold back a 32 MB server
    stream (the upstream sent fewer than half its messages in a second) and an upstream that reads
    nothing hold back a 32 MB client stream, then every message arrive.
  - **One port serving HTTP/1.1, HTTP/2, TLS passed through by SNI and SOCKS**: I53's
    `TestOnePortManyProtocols`, with `TestSNIRouting`, `TestSOCKS` and `TestSniffClear`.
- They pass under the race detector on the benchmark host, run from a checkout of their own at
  `1486c28`; CI runs them once fit is pushed.
- The milestone's benchmarks, the full harness with every server (rule 21): the
  [M3 report](https://github.com/lesismal/fit/blob/3658ae9/bench/results/m3-report-2026-10-22.md).

Differences from the plan:

- The check that no middleware blocks is a test of how soon each returns, not a hook in fib: a
  wait on the worker shows as a ServeHTTP that lasts as long as what it waits on.

### I56 Provider framework and effective config — done

- `provider` (new): `Provider`, a source of config that sends what it provides, whole, each time
  it changes, as a part: a JSON object with `listeners`, `routes`, `upstreams` and `services` only.
  `Run` runs providers, each on a goroutine of its own, and applies their parts one at a time on
  its own; only a provider's latest part waits, so what waits is bounded by one part per provider.
  `Parse` reads a part (at most 16 MiB, its elements objects, `name` a string) and `Merge` merges
  parts into the base config by the rules of decision [009](decisions/009-provider-merging.md):
  the base config wins over every provider, a provider over those added after it; a part's
  elements go after the base config's, so the base config's indexes do not move; a named element
  whose name an earlier source took is left out and recorded as shadowed; every other setting
  comes from the base config alone. The merged config is the base config's text with the parts'
  elements spliced in as written, so `Merged.Errors` maps each error back: into the base config,
  with its line and column there, or into a provider's part, with its path and position there and
  `(provided by <name>)`. `Merged.Sources` says where each top-level member and array element came
  from.
- `admin`: `Configs` keeps the base config, the one `GET /config`, its pointers, ETags and
  rollback are about, and each provider's last applied part. `AddProvider` adds a provider (names
  of base config sources are taken); `Provide` applies a provider's part merged into the running
  base config as a new version whose source is the provider, leaving the running config and the
  provider's previous part in place on any failure, and skipping a part the same as its last.
  Every apply (the file, SIGHUP, the Admin API, rollback, certificates) merges the current parts,
  so a load through the API keeps what providers provide and a rollback brings back the base
  config with them. `GET /config/effective` answers the running config as it runs, its version
  and the base config's ETag, the sources in order of precedence, each element's source, and what
  was shadowed.
- No provider is wired into `fit run` yet: there is none but the file, which stays the base
  config's source (I57 adds Docker, and the config's list of providers with it).
- Tests: `provider` `TestMerge` (two providers and a base config: order, sources, shadowing by the
  base config and by an earlier provider, the base config's text kept), `TestMergeIntoEmpty`
  (kinds the base config leaves out or has empty), `TestParseRefused`, `TestErrors` (an error in
  the base config and one in a part, each at its own line and column), `TestPosition`, `TestRun`
  (one apply at a time, waiting parts replaced, errors reported, return on cancel); `admin`
  `TestProviders` (routes from two providers served, a name shadowed, a broken part refused with
  its path in the part, the same part not applied again, a load and a rollback keeping providers'
  parts, a base config breaking a provider's route refused, an empty part taking routes away,
  reload counts) and `TestFileAndProviders` (the file provider and a fake provider run by
  `provider.Run` together, an edit to the file and the provider's parts merged, the effective
  config's sources). They, `provider/file`, `cmd/fit` and `test/e2e` pass under the race detector
  on the benchmark host (`golang:1.27` in Docker there, from HEAD with this change); vet (also for
  Windows) and staticcheck pass.
- No benchmark: control plane only (rule 21).

Differences from the plan:

- Providers' names are not namespaced (Traefik's `name@provider`): references to them sit inside
  module params, which fit does not interpret; a name taken earlier shadows instead (decision 009).

### I57 Docker provider — done

- `config`: `providers`, a list in order of precedence, each of one kind with an optional name
  (its kind by default; lower-case, unique, not a base config source's). `docker` has `endpoint`
  (`unix:///var/run/docker.sock`, or `tcp://host:port` for a socket proxy), `expose_all` (off:
  only containers labelled `<prefix>.enable=true`), `network`, `use_published_ports` (for a fit
  outside Docker that cannot reach containers' addresses), `label_prefix` (`fit`),
  `max_containers` (1000), `refresh_interval` (30s) and `request_timeout` (10s). Read at start; a
  change to them is a restart warning, as `admin` and `acme` are.
- `provider/docker` (new): the Engine API over plain `net/http` with unversioned paths (decision
  [010](decisions/010-docker-api-over-http.md)): `GET /events` filtered to containers, then `GET
  /containers/json` (with a label filter unless `expose_all`), again 200ms after events (exec
  events, which health checks make, are passed over) and every `refresh_interval`, when the part is
  sent even unchanged, so that one refused because of the rest of the config is tried again. A
  lost stream or a failing daemon is reported and tried again 1s to 30s apart, and the last part
  stays meanwhile. Bounded: the list at most 16 MiB within `request_timeout`, an event line 1 MiB,
  `max_containers` containers. Labels: `fit.enable`, `fit.name` (the upstream's name; the Compose
  project and service as `<project>-<service>`, else the container's name), `fit.port` (else the
  only TCP port), `fit.network`, `fit.upstream` (a JSON object of more upstream settings), routes
  as `fit.routes.<r>.{host,path,path_regexp,method,expr,priority,listeners,middleware,handler}`
  (named `<name>.<r>`, proxying to the upstream unless `handler` says otherwise) and layer 4
  services as `fit.services.<s>.{listeners,port,server_names,proxy_protocol}`. Replicas, the
  containers of one name, share an upstream; containers unhealthy or starting their health check
  wait. A container whose labels are wrong is left out and reported once, with why; the part is the
  same bytes for the same containers.
- `cmd/fit`: `fit run` adds the config's providers to `admin.Configs` in their order and runs them
  with `provider.Run`, printing `fit: provider <name> watching Docker at <endpoint>`, each part
  applied as `fit: applied what provider <name> provides (config version N)`, and a refused part
  once, with its errors where the provider wrote them, until one applies. They stop before
  shutdown, with the file watcher. Under `-prefork` each child runs its own.
- Tests: `config` (a provider of no kind, two of one name, a base source's name, a bad name,
  endpoints, a label prefix); `provider/docker` `TestTranslate` (Compose replicas in one upstream,
  a named container with a chosen port, a handler, middleware and a service on its own port,
  containers not enabled, enabled false, unhealthy or starting left out, and nine kinds of wrong
  labels reported; order does not change the part), `TestTranslateOptions` (`expose_all` under
  another prefix, the host's network, two networks, `network`, `use_published_ports`,
  `max_containers`), `TestProvider` against a fake daemon on a Unix socket (start and stop events,
  exec events listing nothing, three events one listing, the stream lost and found again, a
  daemon answering 500 and coming back), `TestProviderRefresh`, `TestProviderNoDaemon`,
  `TestReadEvents`; `test/e2e` `TestDockerProvider` runs `fit run` with the provider against the
  real daemon: one replica served, a second added to its upstream, the first stopped and dropped,
  the route gone with the last, and `/config/effective` showing the source. They, `config`,
  `schema` (regenerated), `provider/...`, `admin`, `cmd/fit` and all of `test/e2e` pass under the
  race detector on the benchmark host, from a worktree of HEAD with this change; vet for linux,
  darwin and windows and staticcheck pass.
- No benchmark: control plane only (rule 21).

Differences from the plan:

- The e2e test uses `docker compose` where it is installed, as on CI's runners, and otherwise
  `docker run` with the labels Compose sets, which is all the provider sees of Compose. The
  benchmark host has no Compose (installing it there was declined), so there it ran with `docker
  run`; CI runs it with Compose. It skips without a Docker daemon, off Linux (containers'
  addresses are not reachable from a macOS host), and with `FIT_E2E_DOCKER=0`.

### I58 Kubernetes Gateway API I: HTTPRoute — done

- `config`: a provider of kind `kubernetes`, with `kubeconfig` and `context` (else the pod's
  service account in a cluster, or `$KUBECONFIG`, or `~/.kube/config`), `controller_name`
  (`lesismal.github.io/fit`), `namespaces` (all), `listen_host` (all addresses), `backends`
  (`endpoints`, or `cluster_ip`), `max_objects` (10000 of each kind) and `request_timeout` (10s).
- `provider/kubernetes` (new), a client of fit's own on `net/http` (decision
  [011](decisions/011-kubernetes-client.md)): credentials from the service account (its token read
  again as it rotates) or a kubeconfig (CA, client certificate and key as files or data, token or
  token file, `insecure-skip-tls-verify`; exec and auth-provider plugins refused with a message),
  read with `go.yaml.in/yaml/v3`, the one new dependency. An informer per kind (Namespaces,
  GatewayClasses, Gateways, HTTPRoutes, ReferenceGrants, Services, EndpointSlices) lists it in
  pages of 500 and watches it from the list's resource version with bookmarks, five minutes a
  watch; it lists again after 410 Gone, tries a failing list again 1s to 30s apart, takes a kind
  not served (404, no CRDs) as empty and looks again every 30s, and keeps at most `max_objects`.
  Nothing is sent until every kind is listed; then a part 200ms after changes and every 30s even
  unchanged, so that a refused one is tried again.
- The translation: each HTTP listener of a Gateway of fit's GatewayClasses is a listener of fit's
  on its port (`<provider>-<port>`, `http/1.1` and `h2c`, shared by Gateways on one port). An
  HTTPRoute attaches by its parentRefs (section and port) to the listeners whose `allowedRoutes`
  take it (`Same`, `All`, `Selector` on the Namespace's labels, kinds) and whose host names meet
  its own; listeners serving it for the same hosts share its routes. Each match of each rule is a
  route of fit's: paths `Exact`, `PathPrefix` (as `/p` and `/p/*`) and `RegularExpression`
  (anchored), exact headers and query parameters, the method; its priority encodes the Gateway
  API's precedence (host, then exact path, longest prefix, method, headers, query parameters) and
  routes are written in creation order for the rest. Filters: `RequestHeaderModifier` and
  `ResponseHeaderModifier` to `headers`, `URLRewrite` to `rewrite` and the proxy's `host_header`,
  `RequestRedirect` to `redirect`, `RequestMirror` to the proxy's `mirror`; another filter answers
  500. Backends: a Service's port is an upstream `<ns>.<service>.<port>` of its ready endpoints
  (or cluster IP), `h2c` for `appProtocol: kubernetes.io/h2c`, an ExternalName's name; weights
  split the proxy; another namespace's needs a ReferenceGrant; a rule with no valid backend answers
  500, one whose backends have no endpoints ready 503. What is left out is reported once, with why.
- `cmd/fit` runs it as it runs the Docker provider, printing `fit: provider <name> watching
  Kubernetes for GatewayClasses of <controller>`.
- Tests: `config` (two kinds in one provider, `listen_host`, `controller_name`, `backends`);
  `provider/kubernetes` `TestTranslateGateway` (a route on two listeners for different hosts,
  another namespace's route by sections, one refused by `Same` and a selector, another
  controller's ignored, weighted backends, header filters, a rewrite with a host, a missing grant,
  a Service with no endpoints ready, no backends, a redirect, an unsupported match and filter; the
  same part for objects in any order; a grant that lets the backend through),
  `TestTranslateOptions` (`cluster_ip`, `listen_host`, `namespaces`, `controller_name`),
  `TestPrecedence`, `TestIntersect`, `TestSelects`, `TestFilters`, `TestMirror`; `TestProvider`
  against a fake API server over TLS with a kubeconfig (nothing before every kind is listed, a kind
  answering 404, two pages, a route and its endpoints from watches, 410 making a new list, an ended
  watch made again, a deletion), `TestProviderUnauthorized`, `TestKubeconfig` (files relative to
  the kubeconfig, a token file, contexts, insecure, a plugin refused, a bad certificate,
  `$KUBECONFIG`, the service account), `TestTokenRotates`. `test/e2e` `TestKubernetesGateway`
  runs `fit run` with the provider against a kind cluster: a Gateway and an HTTPRoute to a
  Deployment served, a second replica joining, a response header filter added, the route deleted;
  CI's new job "Kubernetes e2e (kind)" makes the cluster, installs the Gateway API v1.3.0 CRDs and
  routes the pod network through the node. `config`, `schema` (regenerated), `provider/...`,
  `admin`, `cmd/fit` and `test/e2e` pass under the race detector on the benchmark host, from a
  worktree of HEAD with this change; vet for linux, darwin and windows and staticcheck pass.
- No benchmark: control plane only (rule 21).

Differences from the plan:

- The kind e2e test runs in CI only: the benchmark host has neither kind nor kubectl, and it skips
  there. It has not run yet; the first push runs it.
- HTTPS listeners (certificates from Secrets) are not served yet: fit's TLS takes certificate
  files, so they need either Secrets written to files or certificates given in the config. They
  move to I59, with the other listener protocols.
- Header and query matches by regular expression, filters on a backend, and a redirect's port
  without a host name are reported and left out. A wildcard host matches one label in fit's
  router and any number in the Gateway API; I61's conformance run will show what of this to close.

### I59 Kubernetes Gateway API II: other routes and status — done

- `config`: the Kubernetes provider's `certificates_dir`, where HTTPS listeners' certificates are
  written (a temporary directory of fit's own, removed when fit stops, when left out). Its docs
  name the new route kinds and what fit's account needs: Secrets of type `kubernetes.io/tls` listed
  and watched, and the status of GatewayClasses, Gateways and routes patched.
- `provider/kubernetes`:
  - More kinds watched, twelve informers in all: GRPCRoutes (v1), TLSRoutes (v1, else v1alpha3,
    else v1alpha2: an informer lists the first version the server serves, and watches and writes
    status there), TCPRoutes and UDPRoutes (v1alpha2), ReferenceGrants (v1beta1, else v1), and
    Secrets, only those of type `kubernetes.io/tls` (a field selector).
  - Listeners by family on each port: HTTP (`http/1.1`, `h2c`); HTTPS (`http/1.1`, `h2`, with
    `tls.certificates`) and TLS passed through (`tcp`, services by server name) share one listener
    of fit's, told apart by the ClientHello's name; TCP (`tcp`); UDP (`udp`, `<provider>-<port>-udp`,
    beside a TCP family on the same port). A listener on a port another family holds already, the
    oldest Gateway first, is `Conflicted` (`ProtocolConflict`) and not served; an unknown protocol,
    a port out of range, a TLS listener that would terminate and an HTTPS one passing through are
    refused. A tcp or udp listener is written only once a service takes it, as fit's config requires.
  - HTTPS certificates: each `certificateRef` names a Secret (another namespace's needs a
    ReferenceGrant from Gateways), checked with `tls.X509KeyPair` and written, before the part is
    sent, as one PEM file of chain and key (0600, in a directory of 0700) named
    `<ns>.<name>.<12 hex digits of its SHA-256>.pem`, so a renewed Secret is a new file and a new
    part; files of that form no longer named are removed once the part is sent. A listener none of
    whose refs can be used is not served (`ResolvedRefs`: `InvalidCertificateRef` or
    `RefNotPermitted`).
  - GRPCRoutes: as HTTPRoutes, a route for each match of each rule: methods `Exact` (service and
    method as `/svc/method`, a service as `/svc/*`, a method by a path regexp) and
    `RegularExpression` (an anchored path regexp), exact headers, a priority after the API's
    precedence for GRPCRoutes (host, service, method, headers); backends over h2c, upstream
    `<ns>.<svc>.<port>.h2c` unless the port's `appProtocol` is h2c already; header modifiers and
    mirrors.
  - TLSRoutes, TCPRoutes and UDPRoutes: a service for each route, `<ns>.<name>.<tls|tcp|udp>`, on
    the listeners it is attached to, a TLSRoute's by the server names its host names and the
    listener's meet, to its first rule's backends (more rules are reported and left out): one
    Service port's upstream, or one upstream of several Services' endpoints, each weighted by its
    backend's weight shared among its endpoints; UDP ports' upstreams end in `.udp`. A listener's
    connections, and each server name, go to the oldest route; a later one is refused.
  - One attachment step for all five kinds: the kinds each listener takes (HTTP and HTTPS:
    HTTPRoutes and GRPCRoutes; TLS, TCP and UDP: their own), narrowed by `allowedRoutes.kinds`, a
    kind the protocol does not take being `InvalidRouteKinds`.
  - Status (`status.go`): after each part, the statuses that differ from what the objects hold are
    written to their status subresources as JSON merge patches, each naming the resource version it
    was made from, so that one made from an older object is refused (409) and the watch brings the
    change and another pass. fit's GatewayClasses are `Accepted`; Gateways `Accepted` (with
    `ListenersNotValid` when a listener is not) and `Programmed`; each listener has its
    `supportedKinds`, `attachedRoutes`, `Accepted`, `Programmed`, `ResolvedRefs` and `Conflicted`;
    each route, on each of fit's Gateways it names, `Accepted` or why not (`NoMatchingParent`,
    `NotAllowedByListeners`, `NoMatchingListenerHostname`), `ResolvedRefs` or why not
    (`BackendNotFound`, `RefNotPermitted`, `InvalidKind`), and `PartiallyInvalid`
    (`UnsupportedValue`) when part of it is left out; other controllers' entries are kept, and fit's
    removed from a route no longer on its Gateways. A condition keeps its `lastTransitionTime`
    while its status stays; `observedGeneration` is the object's generation. The writes run on a
    goroutine of their own, always of the last set made; a kind whose writes fail is reported once
    until they succeed.
- Tests: `provider/kubernetes` `TestHTTPS` (certificates from Secrets, a grant across namespaces,
  a key that does not match, a missing Secret, a ref that is not a Secret, HTTPS and TLS
  passthrough on one port, a protocol conflict, nowhere to write), `TestGRPCRoute` (each kind of
  method match, headers, precedence, h2c upstreams, a listener taking HTTPRoutes only),
  `TestL4Routes` (TCP, UDP and TLS on their listeners, TCP and UDP on one port, weights over two
  Services, a second route refused, a UDPRoute to a TCP port), `TestStatus` (the class, the Gateway
  and its listeners, each reason a route is refused or its backend unresolved, other controllers'
  entries kept, a stale entry removed, nothing written again once written, a transition's time),
  `TestObjectPath`, and `TestProviderStatus` against the fake API server, which now applies status
  patches with their resource versions (statuses written once and then left through resyncs, a
  write refused made again, a TLSRoute at v1alpha2, a certificate file written and removed with its
  Secret). Every part the translation tests make is loaded with `config.Load`. `test/e2e`
  `TestKubernetesGateway` also waits for the class, Gateway and route statuses with kubectl, serves
  an HTTPS listener with a Secret's certificate, and, when the CRD is installed, a TCPRoute; CI's
  kind job installs the Gateway API v1.3.0's experimental channel. The 27 packages `make
  test-changed` picks (the config change reaches them) pass under the race detector on the
  benchmark host; vet for linux, darwin and windows and staticcheck pass.
- No benchmark: control plane only (rule 21).

Differences from the plan:

- `Programmed` says that fit was given the listener, not that the config holding it was applied: a
  provider hears nothing back from the merge. A part the running config refuses is reported where
  it is merged, while the statuses still say `Programmed`.
- TLS listeners that terminate TLS (`tls.mode: Terminate`, for TLSRoutes) are refused: fit's tcp
  services pass TLS through. A TCPRoute or TLSRoute serves its first rule only. A Gateway's
  addresses are neither read nor written; I61's conformance run will show what of this it needs.
- HTTPS listeners on one port with the same host name are not reported as a conflict: their
  certificates are served side by side, picked by SNI.
- The e2e additions run in CI only, as I58's: the benchmark host has neither kind nor kubectl.

### I60 Kubernetes Ingress — done

- `config`: the Kubernetes provider's `ingress`, which turns Ingresses on, with `http_port` (80) and
  `https_port` (443), checked to be ports and not the same one. Off when left out, since fit's
  account then needs to list and watch Ingresses and IngressClasses, and a kind it may not list
  would hold up every part.
- `provider/kubernetes` (`ingress.go`):
  - Two more informers, only with `ingress`: Ingresses and IngressClasses (networking.k8s.io/v1).
  - fit's Ingresses: those whose `ingressClassName`, or else `kubernetes.io/ingress.class`
    annotation, names an IngressClass whose `controller` is `controller_name`, and those naming
    none when such a class has `ingressclass.kubernetes.io/is-default-class: "true"`; in the
    namespaces watched, oldest first.
  - Each path of each rule is a route of fit's, `<ns>.<name>.ing.r<i>.p<j>`, on the listener of
    `http_port`, and on that of `https_port` for a host the Ingress's `tls` covers (by name or
    wildcard, or every host for an entry with no hosts), whose Secrets' certificates are written as
    a Gateway's are (I59). Paths `Exact`, and `Prefix` (with `ImplementationSpecific`, which fit
    reads as `Prefix`) element-wise as `/p` and `/p/*`; a priority after Ingress's precedence (an
    exact host, then a wildcard; an exact path, then the longest prefix). The default backend is a
    route matching anything, at priority 0. A backend is a Service's port by number or name, its
    upstream shared with Gateway routes; a missing Service or port, or a resource backend, answers
    503. The listeners are those a Gateway would make, so an Ingress port shared with a Gateway
    listener of the same family is one listener of fit's, and one of another family is reported,
    the rules then not served there.
  - Annotations, under `fit.lesismal.github.io/`: `ssl-redirect` (HTTP requests for the TLS hosts
    redirected with 308 to `https://<host>[:https_port]{uri}`; a wildcard or any host only from 80
    to 443, else reported and served over HTTP), `rewrite-target` (the matched prefix, or the whole
    path for `Exact`, replaced), `backend-protocol` (`HTTP`, `H2C`, `GRPC`, the last two over h2c),
    `limit-rps` (`rate_limit` by client address), `proxy-body-size` (`body_limit`, in bytes or with
    k, m or g), `allow-source-range` (the route matches those addresses, and a route beside it
    answers 403 to others; a range that does not parse makes every route of the Ingress answer
    403, rather than open it), `request-timeout` (the proxy's `timeout`). Others under the prefix,
    and values that do not parse, are reported and left out.
- Tests: `config` (ports out of range, one port for both); `provider/kubernetes` `TestIngress`
  (classes by name, annotation and default, another controller's left out, TLS and the redirect,
  limits and the timeout, a port by name, `Exact` before `Prefix`, a wildcard host with source
  ranges, a rewrite and h2c, a default backend, a missing Secret, bad annotations, backends that
  answer 503, a range that does not parse, no default class, no `ingress`), `TestIngressPorts`
  (other ports in the redirect, a wildcard host not redirected, the HTTPS port held by a Gateway's
  TCP listener), `TestParseSize`, `TestProviderIngress` against the fake API server. `test/e2e`
  `TestKubernetesGateway` also serves an Ingress of its own IngressClass on a port of its own. The
  27 packages `make test-changed` picks pass under the race detector on the benchmark host; vet
  for linux, darwin and windows and staticcheck pass.
- No benchmark: control plane only (rule 21).

Differences from the plan:

- Ingresses get no status: fit does not know the address it is reached at (a Service, a node, a
  load balancer in front), and an Ingress's `status.loadBalancer` is that address.
- The annotations are fit's own, under its prefix, not ingress-nginx's: their meanings and
  defaults differ (ingress-nginx redirects TLS hosts by default), and reading its names would
  promise what fit does not do. A user moving from it renames the few kept.
- The e2e addition runs in CI only, as I58's.

### I61 Gateway API conformance — done

- The suite: `test/conformance`, a Go module of its own (decision
  [012](decisions/012-gateway-conformance.md)) requiring `sigs.k8s.io/gateway-api` v1.3.0, whose
  test runs the Gateway API's conformance suite; `test/conformance/run.sh` makes fit's
  GatewayClass, starts `fit run` with the Kubernetes provider (Gateways on 127.0.0.1, written into
  their status) and runs the suite with `-conformance-profiles GATEWAY-HTTP`, writing its report.
  fit claims the profile's core: its 33 tests, Gateway, HTTPRoute and ReferenceGrant.
- `config`: the Kubernetes provider's `addresses`, IP addresses or host names written into each
  Gateway's `status.addresses` (`IPAddress` or `Hostname`), as clients and the suite read where a
  Gateway is reached; left as they are when none are given.
- `router` and `config`: a route's host may be a `**.` wildcard, `**.example.com`, which stands for
  one label or more, as the Gateway API's `*.example.com` does (fit's `*.` stays one label). The
  router keeps such hosts in a map of their own, by suffix, below `*.` and above no host in
  specificity, and looks the request's suffixes up, the longest first and at most four, only when
  there are any; HTTPS by default obtains no certificate for one.
- `provider/kubernetes`: the hosts of HTTPRoutes and GRPCRoutes are written with `**.` for the
  Gateway API's wildcards (Ingresses keep `*.`, which is one label in theirs). A listener accepted
  but not served, as one whose certificates are missing, still takes routes and counts them in
  `attachedRoutes`, and a route it takes is accepted and has its backends resolved for its status,
  though it makes no route of fit's.
- The first run on the benchmark host passed 30 of the 33 tests: `HTTPRouteHostnameIntersection`
  and `HTTPRouteListenerHostnameMatching` sent `foo.bar.anotherwildcard.io` to `*.` wildcards of one
  label, and `GatewayWithAttachedRoutes` wanted `attachedRoutes` and `ResolvedRefs` on a listener
  whose Secret is missing; the changes above fix them, and the second run passes all 33.
- CI: a job of its own, "Gateway API conformance (kind)": a kind cluster with the experimental
  CRDs, fit built with `CAP_NET_BIND_SERVICE` for ports 80 and 443, `run.sh`, and the report and
  fit's log kept as an artifact.
- Tests: `router` `TestDeepWildcard` (one label and more, case, the bare domain, specificity
  below `*.` and above none, the longest suffix first when its path matches); `config` (`**.`
  route hosts, `**.` alone refused, no certificate by default for `**.`); `provider/kubernetes`
  (the wildcard written as `**.`, a route on a listener whose certificate is missing attached,
  accepted and its missing backend reported). The 28 packages `make test-changed` picks pass under
  the race detector on the benchmark host; vet for linux, darwin and windows and staticcheck pass,
  the conformance module's included. `scripts/test-changed.sh` now leaves nested modules out: the
  conformance module's `go.mod` had made it test everything.
- Benchmarks (rule 21, the router being on the hot path): `bench/microbench.sh` of
  `BenchmarkMatch`, `main` against this change, first found the per-level loop with its new branch
  3.7% slower (56.8 to 58.9 ns); with the levels unrolled, the deep step a loop over none when no
  `**.` host matched, it is 1.0% faster (56.8 to 56.2 ns, p=0.002), without allocating.
  `bench/ab.sh` (fixed, static and proxy, HTTP/1.1 and h2c, 3 rounds of 10s, the kind node stopped
  meanwhile): every pair within noise, from -0.5% to +0.5%.
- The benchmark host now has kind v0.30.0 (built with its Go) and kubectl v1.34.0 (the release
  binary, its checksum checked) in `/root/dev/bin`, and a kind cluster, `fitconf`, whose pod
  network is routed through its node, for conformance runs.

Differences from the plan:

- Only the profile's core is claimed: its extended features (redirects by path and port,
  rewrites, mirrors, method and query matches, and the rest) are left for a later run, which
  claims each with a flag in `run.sh` once its tests pass.
- Every Gateway is reached at the same address: fit merges Gateways that share a port into one
  listener, so a request reaches the routes of every Gateway on it. The core tests do not tell
  them apart; `GatewayHTTPListenerIsolation`, an extended test, would.

### I62 Consul and etcd providers — done

- `config`: three kinds of provider. `consul` (`ConsulProvider`): `endpoint` (http or https, the
  local agent's by default), `datacenter`, `token_file`, `ca_file`, `cert_file` and `key_file`,
  `expose_all`, `tag_prefix` (`fit`), `max_services` (1000), `refresh_interval` (30s) and
  `request_timeout` (10s). `consul_kv` (`ConsulKVProvider`) and `etcd` (`EtcdProvider`, with
  `endpoints`, `username` and `password_file` in place of the token and datacenter) take a `prefix`
  (`fit/`) and `max_keys` (1000). Endpoints are checked to be http or https and a host and port
  alone; a certificate needs its key, a user its password file.
- `provider/labels`: what the Docker provider read from labels, moved out so that Consul's tags are
  read the same way: routes, services and an upstream's settings under a prefix, and the part a set
  of groups makes. `provider/docker` uses it, unchanged in what it provides.
- `provider/internal/watch`: `Follower`, the loop the three share. It keeps a watch going on a
  `Source`, reads the source whole once the watch is in place (so no change between them is
  missed), after changes (debounced, 200ms) and every `refresh_interval`, sends the part when it
  changed or the interval came, reports each problem once while it lasts, and tries a failed source
  again 1s to 30s apart; `Run` returns only once the watch has. `Client`, an HTTP client with a CA
  and a client certificate, made at each watch so that new files are picked up.
- `provider/internal/kvpart`: each key under the prefix holds a part, a JSON object of listeners,
  routes, upstreams and services as `provider.Parse` takes it; the provider's part is theirs
  together, by key. Keys ending in '/' and empty values are passed over; a key whose value is not a
  part, or past `max_keys`, is left out and reported.
- `provider/consul`: `Catalog` polls `/v1/catalog/services` and `/v1/health/state/any` with
  blocking queries and, when either moves, lists the services, takes those tagged
  `<prefix>.enable=true` (every one with `expose_all`), and reads each one's passing instances from
  `/v1/health/service/<name>?passing=1`. Each service is an upstream of its instances' addresses
  (the service's, or else its node's) and ports (`<prefix>.port` or the registered one); tags of the
  form `<prefix>.<key>=<value>` are labels, as a container's are, and `<prefix>.name` names the
  upstream. `KV` polls `/v1/kv/<prefix>?recurse` (a 404 is no keys). Blocking queries follow
  Consul's guidance (an index that goes back starts over, one below 1 is 1, at least 1s between the
  queries of a poll); the token goes in `X-Consul-Token`, the datacenter in `dc`.
- `provider/etcd`: etcd's v3 API through its JSON gateway. A watch on the prefix's range
  (`POST /v3/watch`, one JSON object a line) must say it is created within `request_timeout`; then
  `POST /v3/kv/range` reads the keys, at most `max_keys` (past them, reported). Events read again,
  debounced; progress notifications do not; a canceled watch, as after a compaction, or an ended
  stream starts over, on the next endpoint. With a user, `POST /v3/auth/authenticate` gets a token
  for the watch's requests, from the password file read each time.
- `cmd/fit`: `fit run` starts the three kinds, saying what each watches.
- Decision [013](decisions/013-consul-and-etcd-over-http.md): net/http, no client libraries, and no
  new dependency.
- Tests: `provider/consul` against a fake Consul with blocking queries (`TestTranslate`: tags,
  names, ports, addresses from the node, a service's own upstream, every problem; `TestCatalog`:
  instances registered, failing their checks and deregistered, Consul failing and back, the token
  and datacenter on every request, services not enabled never asked for; `TestCatalogMaxServices`;
  `TestKV`: keys added and deleted, a 404, an index that goes back); `provider/etcd` against a fake
  gateway (keys written and deleted through the watch, progress notifications, a compaction and an
  ended stream, an endpoint that is not there, authentication, `max_keys`, a watch never created, a
  wrong password, `rangeEnd`, `readWatch`); `provider/internal/watch` (`TestFollower`,
  `TestFollowerInterval`); `provider/internal/kvpart`; `config` (each kind's settings).
  `test/e2e` `TestConsulProviders` runs fit against Consul 1.21.5 in Docker, dev mode, with the
  refresh interval left at 30s: a service's instances registered, one failing its TTL check, the
  last deregistered, and a route from a key written and deleted; `TestEtcdProvider` against etcd
  v3.6.5 with authentication on: a key written, changed and deleted. Both, `TestDockerProvider`
  (after the move to `provider/labels`) and the packages touched pass under the race detector on the
  benchmark host; vet for linux, darwin and windows and staticcheck pass.
- The first run found a race the fakes made plain: `Follower` returned without waiting for its
  watch's goroutines, so a poll could outlive `Run`. It now waits; and the tests' `background`
  helper waits for `Run` before a test's cleanup.
- No benchmark: providers are the control plane (rule 21).

Differences from the plan:

- KV values are JSON parts, a key each, not a tree of keys a setting each as Traefik reads them:
  the same shape as the config file and the Admin API, checked by the same parser, and many keys
  let several writers keep parts of their own.
- Consul Enterprise's namespaces and partitions, and etcd's v2 API, are not supported (decision
  013).

### I63 Shared certificate storage — done

- `config`: `acme.shared_storage`, with exactly one of `redis` (`address`, `username`,
  `password_file`, `db`, `tls`, `ca_file`, `cert_file`, `key_file`), `etcd` (`endpoints`,
  `username`, `password_file` and the TLS files) and `consul` (`endpoint`, `datacenter`,
  `token_file` and the TLS files), and `prefix` (`fit/`), `lock_ttl` (30s, at least 10s) and
  `request_timeout` (10s); not with `storage`. A reload that changes it is refused, as any change to
  `acme` is.
- `tlsmgr/sharedstore`: `Store`, a `tlsmgr.Storage` over a backend of each kind, which applies the
  prefix, refuses the keys `FileStorage` refuses and values over 4 MiB, keeps locks under
  `<prefix>locks/` out of `List`, and holds each lock with a goroutine that renews it every third of
  the TTL until `Unlock` (another goroutine of the process waits, as another process does).
  Redis: RESP2 on at most four connections (AUTH with a user, SELECT, TLS), replies bounded; GET,
  SET, DEL, SCAN with the prefix's glob characters escaped; locks by `SET NX PX` with a random
  token, renewed and deleted by scripts that check it. etcd: the JSON gateway, `/v3/kv/range`,
  `put`, `deleterange`, a user's token (taken again once when refused), the next endpoint when one
  cannot be reached; locks by a lease and a transaction on the key's create revision, renewed by
  `/v3/lease/keepalive` and given back by revoking the lease. Consul: `?raw`, `?keys`, PUT and
  DELETE on `/v1/kv`; locks by a session (`TTL`, `Behavior: delete`, no lock delay) acquiring the
  key, renewed and destroyed.
- `internal/apiclient`: the HTTP client with TLS files and the bounded reads I62 wrote for its
  providers, moved out of `provider/internal/watch` so that `tlsmgr/sharedstore` uses them too.
- `tlsmgr`: `newACME` builds the store from `shared_storage` (and closes it in `Close`); a storage
  that says it is `Shared` makes ACME keep each challenge's answer in it while the challenge is under
  way, the HTTP-01 key authorization by token and the TLS-ALPN-01 certificate by name, deleted
  afterwards. `LookupHTTPChallenge` looks a token up there on ACME's goroutines, at most 16 at once,
  each within 5s, answering at once when the token is not base64url or every slot is taken; a
  TLS-ALPN-01 handshake for a listed name that this instance has no challenge for looks the name up
  there, on fib's handshake pool.
- `listener`: a request for a challenge's answer that is not in memory, with a shared storage,
  takes `Retain` and is answered from `LookupHTTPChallenge`'s callback: the answer, or a 404.
- Decision [014](decisions/014-shared-certificate-storage.md): the three stores without client
  libraries, leased locks, shared challenges.
- Tests: `tlsmgr/sharedstore` `TestStores` runs one suite against Redis 8.2.2 (with a password and
  a database), etcd v3.6.5 (its first endpoint not there) and Consul 1.21.5, each in Docker, in
  parallel: keys stored, replaced, loaded by a second store, listed by prefix (locks left out, glob
  characters taken literally), deleted, bad keys, the value limit, a cancelled context; eight holders
  over two stores take a lock in turn, never two at once; a lock held past its TTL is renewed and
  waited for, and taken once given back; a lock whose holder stopped renewing is taken after the TTL
  (10s; 20s on Consul). `TestReadReply` reads RESP replies and refuses bad and oversized ones.
  `tlsmgr` `TestSharedPebble`: two ACMEs on one shared storage against Pebble, which reaches only
  the second; the first's certificate is obtained with the challenge answered from the storage, and
  the second, asked for the same name, takes it rather than obtaining another; the challenge is gone
  from the storage after. `TestLookupHTTPChallenge` (no storage, bad tokens, the bound of 16) and
  `TestCertsSharedALPN`. `test/e2e` `TestACMESharedStorage`: two `fit run` processes on Redis, etcd
  and Consul in turn, Pebble reaching only the first's HTTP-01 port: the second obtains a name only
  it manages, and a name both manage is obtained once and served by both, the same certificate.
  `config` (each kind's settings, `storage` with `shared_storage`). `startFitWant` takes the
  listeners' count for a config whose "address" keys are not all listeners'. The touched packages
  and the ACME e2e tests pass under the race detector on the benchmark host, which now has Pebble
  v2.10.1 in `/root/dev/gopath/bin`, as CI installs it; vet for linux, darwin, windows and freebsd
  and staticcheck pass.
- No benchmark: certificate management is the control plane (rule 21); the dispatcher's change is
  on a path only `/.well-known/acme-challenge/` requests with a shared storage take.

Differences from the plan:

- Challenges are shared as well as certificates: without it, two instances behind one address can
  share issuance only when the CA happens to reach the one that ordered.
- With a shared storage, a challenge path whose token is nowhere gets a 404 rather than the routes
  (decision 014).

### I64 Dashboard — done

- `admin`: `GET /status`, what fit is doing now and the config does not say: the running config's
  version, when and from where it was applied, reloads, the listeners bound and their protocols,
  open and refused connections and pending bytes, each upstream's requests in flight and its
  backends (in rotation or out, and whether a health check took it out; requests in flight,
  succeeded and failed), and each certificate a listener serves (names, issuer, serial, validity),
  sorted by upstream and by listener and expiry. It reads what `WriteMetrics` reads.
- `admin/dashboard`: the dashboard, HTML, CSS and plain JavaScript embedded with `go:embed` and
  served at `/dashboard/` (`/dashboard` redirects), with a Content-Security-Policy of its own files
  and origin, `nosniff`, `DENY` framing and `no-cache`. Six tabs: an overview (version, reloads,
  connections, routes, backends up, certificates expiring within 14 days; listeners; the sources by
  precedence, with what each provides and what was shadowed), routes (listeners, match, middleware,
  handler and upstream, source), layer 4 services, upstreams with their backends' health, filtered by
  name or address, certificates with the days left, and the config versions, each one's config
  readable. It refreshes every 5s, which can be turned off, writes data as text only, never as
  HTML, and asks for the token when the API answers 401. `admin.disable_dashboard` turns it off.
- `admin` guard: the dashboard's files are served without the token, and a GET or HEAD from the
  API's own origin is let through; decision 005 has an update for I64.
- Tests: `admin` `TestStatus` (a live and a dead backend after requests, the certificate),
  `TestDashboard` (the redirect, the security headers, every file the page names, 404 and 405,
  reads from its own origin allowed and from another refused, writes from its own origin refused),
  `TestDashboardToken` (files without the token, endpoints and writes under `/dashboard/` with it),
  `TestDashboardDisabled`, and `TestDashboardReadOnly`, which reads the embedded files and fails on a
  method other than GET, a form that posts, another origin, or `innerHTML`; `test/e2e`
  `TestDashboard` against `fit run`. The pages were checked by eye in a browser against `fit run` on
  the benchmark host, through an ssh tunnel, with a live and a health-checked-down backend, a
  certificate and two versions, in light and dark: every tab rendered, with no console errors, and a
  header row built as table cells was fixed. `admin` and `config` pass under the race detector on the
  benchmark host; vet for linux, darwin, windows and freebsd and staticcheck pass.
- No benchmark: the Admin API is the control plane (rule 21).

Differences from the plan:

- `GET /status` is new: upstream health and certificates were only in the Prometheus metrics,
  which a page should not parse.

### I65 OpenTelemetry — done

- `config`: `telemetry`, read when fit starts: `endpoint` (OTLP/HTTP, `http://127.0.0.1:4318`),
  `headers_file` (`Name: value` lines, as an API key), `ca_file`, `cert_file`, `key_file`,
  `service_name` (`fit`), `timeout` (10s), and at least one of `traces` (`sample_ratio`, 0 by
  default: only what callers sample; `max_queue_bytes`, 4 MiB; `interval`, 5s) and `metrics`
  (`interval`, 30s). `telemetry.metrics` counts requests as `metrics` does, without the Admin API.
  A reload that changes `telemetry` warns that it takes effect on a restart.
- `observe` (decision [015](decisions/015-opentelemetry-without-the-sdk.md), no SDK):
  `Tracer.Sample` reads `traceparent` (W3C, version 00 and later versions' first fields; an
  invalid one is not followed), follows a caller's decision, and samples a request without one at
  the ratio, from `math/rand/v2`; with ratio 0 an unsampled parent is not even parsed.
  `TrackTraced` gives a sampled request its `Entry` and its trace in one allocation; the trace and
  a JWT's claims now sit behind one pointer of the entry's (`Trace()`, `Claims()`, `SetClaims`),
  so that the entry of every other request stays 112 bytes. `Tracer.End` writes the server span
  as OTLP JSON (`<method> <route>`, the HTTP semantic conventions' attributes and fit's, error
  status for 5xx) into one of eight shards, dropping and counting what passes `max_queue_bytes`;
  a goroutine exports every interval, or once 256 KiB wait, to `/v1/traces`, handing the shards'
  buffers back for the next spans, and reports a failure once while it lasts. `MetricWriter` is
  what `TextWriter` and the new `OTLPWriter` are: counters as cumulative monotonic sums, gauges,
  duration histograms as explicit-bucket ones in seconds; `MetricsExporter` posts them to
  `/v1/metrics` every interval, and once more when fit stops.
- `listener`: the dispatcher asks the tracer, when there is one, and a sampled request gets its
  span recorded when its response finishes, beside the access log's line and the metrics; the
  closure without a tracer is as it was. `handler/proxy`: `outgoing` sends a traced request's
  `traceparent`, naming fit's span, to the upstream (WebSocket upgrades too); an untraced one's
  goes on as it came. `admin`: `WriteMetricsTo(MetricWriter)`, and `fit_trace_spans_total`
  (exported, failed, dropped). `cmd/fit`: the tracer closed after the graph, the metrics exporter
  started once the configs are, and `observe.Version` set for the resource.
- Tests: `observe` (`TestParseTraceparent`: valid, unsampled, other flags, later versions, and
  eleven invalid forms; `TestSample`: following callers, ratio 0, 1 and 0.5 over 20000 requests;
  `TestTracerExport`: the OTLP JSON of two spans against a receiver, attributes, escaping, the
  headers file, the resource; `TestTracerBounds`: drops past `max_queue_bytes`, a failing receiver
  reported once and its recovery; `TestMetricsExporter`: a counter, a gauge and a histogram's
  buckets), `listener` `TestDispatchTrace`, `handler/proxy` `TestOutgoingTraceparent`, `config`
  (`TestParseHeaders`, telemetry settings, a ratio past 1). `test/e2e` `TestTelemetry` against an
  OpenTelemetry Collector 0.137.0 in Docker, its file exporter writing what it receives: a request
  a caller sampled reaches the upstream with a `traceparent` in the caller's trace naming fit's
  span, which the collector gets under the caller's span; one without a `traceparent` is neither
  traced nor given one; the collector gets fit's metrics and the resource's `service.name`. The
  packages touched and the e2e tests pass under the race detector on the benchmark host; vet for
  linux, darwin, windows and freebsd and staticcheck pass.
- Benchmarks (rule 21, the dispatcher and the proxy's `outgoing` being on the hot path), on the
  benchmark host with the kind node stopped meanwhile. `bench/ab.sh` (fixed and proxy, HTTP/1.1 and
  h2c, 3 rounds of 10s, no telemetry): every pair within noise, -0.4% to +0.7%.
  `bench/microbench.sh` of `BenchmarkDispatch`, `main` against this change: bare 205.8 to 205.8 ns,
  log 256.7 to 260.9 (not significant), log+metrics 258.4 to 260.2 (not significant), metrics
  219.1 to 222.1 (+1.4%, p=0.017: the tracer's nil check), allocations and bytes unchanged; with a
  tracer, 226.6 ns sampling nothing and 889 ns sampling every request, 6 KiB a request of which
  most is the exporter's in the same process. `BenchmarkSample`: 4.5 ns without a traceparent,
  8.9 ns for an unsampled one at ratio 0, 26 ns to start a trace and 54 ns to follow a caller's,
  none allocating; `BenchmarkEnd`: 314 ns to write an 847-byte span, without allocating. The first
  run had found the dispatcher 1.4% slower bare, an entry 62 bytes larger, and `End` at 1.2 µs
  and 47 ns for an unsampled parent; the changes above (a trace touched only with a tracer, the
  trace beside the claims, buffers reused, the unsampled parent not parsed) brought them here.

Differences from the plan:

- No SDK, and OTLP/HTTP with JSON only (decision 015).
- One server span per request; the upstream call has no client span of its own.

### I66 Packaging and examples — done

- **The container image** ([Dockerfile](../Dockerfile), decision
  [016](decisions/016-container-image.md)): fit built without cgo, with `-trimpath` and
  `-X main.version`, cross-compiled on the builder's platform with BuildKit's caches, on
  `gcr.io/distroless/static-debian12:nonroot`, running as 65532; a placeholder config at
  `/etc/fit/fit.json`. `.dockerignore` sends only what the build needs.
  [release.yml](../.github/workflows/release.yml) publishes it for linux/amd64 and linux/arm64 to
  `ghcr.io/lesismal/fit:<version>` and `:<major>.<minor>` on a tag `v<semver>`; none is tagged yet.
- **The Helm chart** (`deploy/helm/fit`): a Deployment of fit as the image's user, read-only root,
  no capabilities, ports below 1024 by the safe sysctl `net.ipv4.ip_unprivileged_port_start`; a
  ConfigMap of a listener of its own on `healthPort` (9099) answering `/healthz` to the probes and
  the Kubernetes provider (in-cluster, through the service account), rolled on change; a Service of
  the Gateways' ports; a ClusterRole of what the provider lists, watches and patches; the
  GatewayClass `fit`, when the cluster has the CRDs, and the IngressClass `fit`. Values for the
  image, replicas, classes, Ingress ports, more provider settings, a whole config, extra flags and
  the usual pod settings.
- **systemd** (`deploy/systemd`): `fit.service` and `fit@.service` (`/etc/fit/<name>.json`), as the
  sysusers user `fit` with `CAP_NET_BIND_SERVICE` alone and the file system read-only but for
  `/var/lib/fit` and `/run/fit`; `fit validate` before start and on reload, which then sends
  SIGHUP; stop waits 40s against fit's 30s shutdown. `systemd-analyze security`: 3.1, OK.
- **Examples**: `examples/docker-compose`, fit's image with the Docker provider in front of two
  replicas of `traefik/whoami`, routed by their labels; `examples/kubernetes`, the chart in a kind
  cluster with a Gateway and HTTPRoute and an Ingress to the same app. [deploy/README.md](../deploy/README.md)
  and the examples' READMEs (both languages) say how.
- **Tests**: each example's `test.sh` runs its README's steps and checks the answers: the Compose
  example answers for its host from both replicas, gives 404 once they stop and answers again once
  they start (M4's exit criterion); the Kubernetes one installs the chart with the image built
  there, waits for the Gateway to be Programmed and gets answers from both pods through the Gateway
  and through the Ingress. `deploy/systemd/test.sh` installs the units, serves port 80 as `fit`,
  reloads a changed config, refuses an invalid one while still serving, and runs `fit@second`.
  CI runs them in two new jobs, `packaging` and `helm` (with `helm lint`). All three passed on the
  benchmark host: the examples with Docker Compose v2.39.1, buildx v0.26.1 and Helm v3.18.4 kept
  in a scratch directory there, the systemd test in a throwaway systemd container.

Differences from the plan:

- The Dockerfile is at the repository's root, where `docker build .` finds it, not under `deploy`.
- No image is published yet: the release workflow waits for a tag, so the chart and examples build
  the image and load it where they run it.

### I67 Cache I: HTTP semantics and memory tier — done

- **`middleware/cache`'s `cache`**, a shared HTTP cache by RFC 9111's rules for one: it keeps
  responses to GET whose status it understands (200, 203, 300, 301, 308, 404, 405, 410, 414, 501,
  and 302 and 307 with a lifetime of their own), that do not say `no-store`, `private` or
  `no-cache`, vary on less than everything, set no cookie (as nginx does not keep them), carry no
  trailer and are no larger than `max_entry_size`; a response to a request with `Authorization`
  only when it says `public`, `s-maxage` or `must-revalidate`. The freshness lifetime comes from
  `s-maxage`, `max-age` or `Expires` against `Date`, else, for the statuses that allow one, from
  `default_ttl` or a tenth of the time since `Last-Modified`, at most a day; the age from `Date`,
  `Age` and the time the request took (section 4.2.3). A response already older than its lifetime
  is not kept.
- **Answers.** A fresh response answers GET and HEAD with its `Age`, and a 304 for a request whose
  `If-None-Match` (weakly compared, or `*`) or `If-Modified-Since` it meets, with the fields RFC
  9110 lists for one. A request's `no-cache` (and `Pragma: no-cache` without `Cache-Control`),
  `max-age` and `min-fresh` send it on; `max-stale` takes a stale response unless it said
  `must-revalidate`, `proxy-revalidate` or `s-maxage`; `only-if-cached` gets 504 when nothing
  is kept. A request with `Range` goes on, and a 200 to it is kept. A request that may change
  something (anything but GET, HEAD, OPTIONS, TRACE) that succeeds drops what is kept for its URI,
  and for its `Location` and `Content-Location` on the same host. Every answer to GET and HEAD
  says what the cache did in `Cache-Status` (RFC 9211): `fit; hit; ttl=N`, or
  `fit; fwd=uri-miss|vary-miss|stale|request|bypass`, with `; stored` when its response is kept.
- **Variants.** A response's `Vary` names the request fields kept with it, their values compared
  with the space around list members taken out; a key keeps at most 8 variants, the oldest going
  first. The key is the scheme, the host and the request target.
- **Revalidation.** A stale response the client's own validators match is freshened by the 304
  the origin answers them with: its fields updated from the 304 (RFC 9111 section 3.2) and its
  lifetime worked out again. The cache does not yet send its own validators for a client that sent
  none; that needs a request of the cache's own, which I69's locks and background updates bring.
- **The memory tier** (`store`): a zone's responses in 16 shards, each with its lock, a sixteenth
  of `max_size` (128 MiB) and a least recently used order; an entry counts its body, fields, key
  and 256 bytes. `max_entry_size` (1 MiB, at most a sixteenth of `max_size`) bounds one body. A
  zone (`zone`, `default`) is shared by every cache that names it and kept across reloads: the new
  config's caches take it before the old config's give it back, and it goes when none uses it.
- **Kept as it goes.** A response is copied as it passes through a fib body filter that changes
  nothing, so a response is never held back to be stored and streams as it did, and is stored once
  it has all gone: as long as it declared, not cut short with an error or a trailer (the proxy's
  `Fit-Error`). What the copies under way hold together is bounded by a quarter of `max_size`;
  past it a response goes without being kept. A response whose client goes, or whose write fails,
  gives its copy back through fib's new `BodyAborter` (below).
- **fib** (A40, made by this session on fib's branch `a40-filter-abort`, `3998953`): `BodyAborter`,
  `Abort` called in place of `Close` for a filter whose response ends without its body ending;
  and hooks that run after one that returned a filter are given no `Content-Length`. Without the
  first, a client gone in the middle of a response kept its share of the bound for good; compress
  had the same gap, its in-flight place given back in `OnCancel`, which does not run once a failed
  response is written, and now gives back its place and its encoder in `Abort`. Without the
  second, the cache behind compress took the uncompressed length for the compressed body's and
  kept nothing. fib's tests, the race detector (fib's root package fails four UDP and socket
  buffer tests on the host on `main` as on the branch), vet for linux, darwin, windows and freebsd
  and staticcheck for linux, darwin and windows pass; `bench/microbench.sh -C ../fib-a40 -pkg
  ./http -bench BenchmarkStream` finds no change (benchstat ~).
- **Tests** (`middleware/cache`, over HTTP/1.1 and h2c where they serve): `TestHit` (stored, then
  answered with `Age` and its validators, HEAD from GET's response with its length, another query
  apart), `TestStorable` (21 responses, kept or not as the rules say), `TestFreshness` (max-age,
  Expires and the heuristic over a moved clock), `TestDefaultTTL`, `TestVary`, `TestConditional`
  (304s from the cache, and preconditions it does not meet), `TestRevalidate`,
  `TestRequestDirectives`, `TestInvalidate`, `TestBodies` (a body written in parts kept with its
  length; one short of its declared length not kept; one past `max_entry_size` passed whole),
  `TestClientGone` (a slow origin's client leaves: nothing kept, the bound given back, the next
  request kept), `TestCompressed` (each encoding a variant behind compress), `TestEviction`,
  `TestVariantsBounded`, `TestZones` and `TestParams`. `test/workers` has a case for the cache.
  The full race suite passes on the benchmark host; `go vet` passes.
- **Benchmark** on the benchmark host (`BenchmarkHit`, a 4 KB response answered from the cache
  through fibtest, 5 runs): 1.48–1.50 µs, 15 allocations and 5.4 KB a hit, most of the bytes
  fibtest's copy of the body.

Differences from the plan:

- The cache answers a client's validators and freshens a stale response with the origin's 304 to
  them, but does not send validators of its own: a request of the cache's own comes with I69.
- Responses that set cookies are not kept, as nginx's `proxy_cache` does not keep them; 204 and
  206 are not kept, since fib's body filters, which the cache keeps responses through, do not run
  for a status without a body, and the cache answers no ranges.
- It needed fib's A40, made by this session. fit's `go.mod` moves to its commit on fib's branch
  `a40-filter-abort`; fit is not pushed until fib's CI has passed on that commit and it is on
  fib's `main` (the maintainer's word), as rules 15 and 19 have it.

### I68 Cache II: disk tier — done

- **`disk`**, a zone's disk tier (`path`, required; `max_size`, 1 GiB; `max_entries`, 100000):
  every response kept in memory is written to disk too, one file a response (a 12-byte head, the
  metadata as JSON, the body), through a file in `tmp` renamed into place, so that a file is whole
  or not there. An index of the files, in memory, says without touching the disk whether a request's
  response is there and whether it is fresh; one the memory tier no longer holds is read back,
  answered with `Cache-Status: fit; hit; ttl=N; detail=disk` and kept in memory again.
- **Workers never wait on disk.** Reads, writes and removals run on a pool of the cache's own
  (`internal/filepool`, which the static handler's file pool moved to: 4 workers a CPU, at least
  16, a queue of 4096). A request whose response is on disk is retained, with the config, and read
  on the pool; a read that fails, or that a timer (5s) finds not done, as on a disk that hangs,
  leaves the request to go on as a miss from wherever it is. A write finds no room when the queue
  is full or 64 MiB already wait to be written, and the response is kept in memory alone.
- **Restarts.** When a zone's disk is made, one goroutine reads the index back from the directory:
  every file's metadata, the files that are not whole or not the cache's removed, `tmp` cleared.
  Requests do not wait for it; until it is done they find less on disk. A zone keeps its disk across
  reloads, takes a new directory when its config moves it, and drops it when its config does; two
  zones cannot share a directory.
- **Removals.** A request that changes a resource removes its responses from disk too; a write
  queued before the removal is not kept when it lands (a sequence number in the index, kept for a
  removed key while it is in the index), so a restart does not bring the response back. A file
  gone from under its entry drops the entry. The least recently used keys go past `max_size` or
  `max_entries`, with their files.
- **Tests**: `TestDiskRestart` (five responses, a streamed one and two variants among them,
  written, the process's zone given back, a new one reads the index back and answers each from
  disk, then from memory, a 304 too, without the origin), `TestDiskPromotion` (a memory of 1 MiB
  in front of 4 MiB on disk), `TestDiskNeverBlocks` (a disk that hangs: ServeHTTP returns within
  100 ms, the request goes on as a miss after the pool's timeout), `TestDiskBrokenFiles` (a file
  cut short, junk and a write cut short removed when the index is read back; a file gone from
  under its entry), `TestDiskInvalidate` (a DELETE removes the file, and a restart does not bring
  it back; a write queued before a removal is dropped), `TestDiskBounds` and `TestDiskZones`. They
  pass under the race detector on the benchmark host, ten times over, with the packages that
  depend on `handler/static` and `internal/filepool`.

Differences from the plan:

- Bodies on disk are those the memory tier keeps, at most `max_entry_size`: the disk holds more
  responses, not larger ones. A stale response on disk is not freshened by a 304, since freshening
  needs its body in memory; the request is sent on as a miss.
- Only one fit process may use a directory: two processes sharing one would each keep an index of
  its own.

### I69 Cache III: locks, stale, purge — done

- **Cache locks.** A GET that misses while another request fetches the same key waits for that
  fetch, retained with the config, and is answered from what it keeps; the fetch ends once its
  response is kept, or as soon as it is known not to be (its header settled for a response that
  will not be kept, a body past `max_entry_size`, a client gone), when the waiters go on to fetch
  for themselves. A waiter goes on after `lock_timeout` (5s), a key takes at most 4096 waiters,
  and a fetch older than twice `lock_timeout` is taken for one that will not end. `disable_lock`
  turns it off. Waiters are woken on the goroutine that ends the fetch, each answered or sent on
  without blocking.
- **Requests of the cache's own.** fib's `NewStreamContext` runs the route's handler for a request
  the cache makes, on a stream of the cache's (`fetchStream`) that discards what it is sent once
  the cache's hooks have kept it, and ends the fetch when the request ends (`StreamEnder`). With
  it the cache revalidates a stale response with its own validators (`If-None-Match`, else
  `If-Modified-Since`), as I67 could not: the request that found it waits for the fetch, with any
  others, and a 304 freshens the response.
- **stale-while-revalidate** (RFC 5861, or the `stale_while_revalidate` param for a response that
  says nothing): a stale response within the window is answered at once
  (`detail=stale-while-revalidate`) and fetched again in the background, once a key and at most
  256 at a time in a zone; one fetched since is not fetched again. **stale-if-error**
  (`stale_if_error`): when fetching a stale response again fails, with no response or a 500, 502,
  503 or 504, the requests waiting for it are answered with it (`detail=stale-if-error`) within the
  window. Neither applies to a response that says `must-revalidate`, `proxy-revalidate` or
  `s-maxage`. Both work for responses on disk too.
- **Purge** (`cache.Purge`, and the Admin API's `POST /cache/purge`, `{"zone", "urls", "tags",
  "all"}`, answering `{"purged": n}`, 404 for a zone no cache uses): by URL, every variant; by
  tag, the responses' `Cache-Tag` (comma-separated) and `Surrogate-Key` (space-separated) fields,
  at most 32 tags of 128 bytes each, kept in an index per zone that counts the responses in memory
  and on disk; or all. Purges reach the disk, and drop the writes still queued. `GET /cache` shows
  what each zone keeps.
- **Harness** (`bench`): two scenarios, `cache-hot` (one path, over both protocols) and
  `cache-zipf` (100,000 paths asked for by Zipf's law, s = 1, from a script of the HTTP/1.1 load
  generator), through each server's cache to a cache backend of its own that counts what reaches
  it on its Admin API, from which each result's hit ratio comes. nginx with `proxy_cache` and
  `proxy_cache_lock`, fit with `cache`; Caddy and Traefik have no cache of their own. Run only when
  named.
- **Results** on the benchmark host ([m5-cache-2026-10-23-linux.json](https://github.com/lesismal/fit/blob/3658ae9/bench/results/m5-cache-2026-10-23-linux.json),
  1 GiB caches, enough for every path; 30s after a 10s warm-up):

  | Scenario | nginx | fit |
  | --- | ---: | ---: |
  | `cache-hot` HTTP/1.1 | 332,436 req/s | 684,362 req/s |
  | `cache-hot` h2c | 291,559 req/s | 1,573,104 req/s |
  | `cache-zipf` HTTP/1.1 | 318,132 req/s, 99.975% hits | 650,532 req/s, 99.9996% hits |

  Both keep every response they are asked for; fit answers about twice as many requests over
  HTTP/1.1, and five times as many over h2c, on the same 7 CPUs. With 64 MiB caches instead,
  about 15% of the paths, fit kept 78.0% of its answers to itself, near the 84% a cache of the most
  popular paths would ([m5-cache-64m-2026-10-23-linux.json](https://github.com/lesismal/fit/blob/3658ae9/bench/results/m5-cache-64m-2026-10-23-linux.json)),
  at 444,374 req/s; nginx was not held to its size at this rate: its `max_size` is enforced by its
  cache manager, which fell behind and let the cache grow to 100–770 MB on disk (94.6% to 99.4%
  hits, [the run with a faster manager](https://github.com/lesismal/fit/blob/3658ae9/bench/results/m5-cache-64m-nginx-manager-2026-10-23-linux.json)),
  and a key zone small enough to bound it failed 482,684 requests once full. So the scenarios
  compare caches large enough for the working set.
- **Tests**: `TestStampede` (100 clients, over HTTP/1.1 and h2c, one cold path from an origin
  that answers after 200 ms: one fetch, all answered), `TestLockNotKept` (waiters go on as soon as
  the response is known not to be kept), `TestLockTimeout`, `TestDisableLock`,
  `TestStaleWhileRevalidate` (answered stale at once, one background fetch, a 304, then fresh;
  past the window fetched before it is answered), `TestStaleIfError` (the response's window and
  the param's), `TestRevalidate` (the cache's validators and the client's), `TestPurge` (by URL,
  in another case of host, by tag from both fields, all; in memory and on disk) and `TestTagsOf`;
  `admin`'s `TestCachePurge`; fitbench's `TestParseRequestCount`. Ten runs of the cache's tests
  pass under the race detector on the benchmark host, and the packages the change reaches pass.

Differences from the plan:

- The 64 MiB comparison could not hold nginx to its size, so the published comparison is of caches
  that hold the working set; both numbers are recorded.

### I70 `fit build` and compile-time modules — done

- **`fit build`** (`plugin/build`, and the `build` command): generates a main package that imports
  fit's command line, `modules/standard` and `provider/standard` unless `-without` leaves one out,
  and each `-with` module's root package, which registers its modules in `init`; makes a module
  for it (`go mod init`, fit and each module required at a version, `latest` by default, or
  replaced by a directory of its source, `go mod tidy`) and builds it with the go command on
  `PATH`, `-trimpath`, its version stamped `<fit's>+build`. `-fit` names the fit to build on, a
  version or a directory of fit's source; left out, the running fit's own, as its build recorded
  it (a version Go made up for a checkout with changes taken without its `+dirty`; a fit built by
  `fit build` gives the one it was built on). `-o` (`fit`), `-keep dir` keeps the generated module.
  `GOOS` and `GOARCH` build for another system; the go command's own `GOFLAGS` and workspace are set
  aside for the build's module.
- **The command line is a package**, `cli` (`Main`, `Version`), which `cmd/fit` and a build's main
  package call; `cmd/fit` is now its main and the imports of every module and provider, and still
  takes `-X main.version`.
- **Providers register themselves by kind** (`provider.RegisterKind`, `LookupKind`,
  `RegisteredKinds`), each package in `init`, and `provider/standard` imports the four fit ships
  with, so a build can leave them out; a config that names a kind the binary lacks is refused
  with what it has.
- **The module template**: [docs/modules.md](modules.md) (and its Chinese twin) says what a module
  is, its params and their tags, `Provision`, `Cleanup` and `Drain`, the rules it follows, how to
  test it and how to build it in; [examples/module/hello](https://github.com/lesismal/fit-examples/tree/main/module/hello), a module of
  its own (`go.mod` and all), is a middleware that adds `X-Hello` to its routes' responses.
- **Tests**: `TestBuildWithModule` builds fit from the checkout with the example module and
  without the providers, checks `fit version` says `test+build`, validates and serves a config of
  the module (`X-Hello` on the answer), and has a config of the Docker provider refused; it skips
  without the go command or with `-short`. `TestParseModule` and `TestImports`. The CLI's tests
  moved with it. The packages the change reaches pass under the race detector on the benchmark
  host; vet and staticcheck pass for linux, darwin and windows.
- **Without the providers** (the [M4 report's h2c slowdown](https://github.com/lesismal/fit/blob/3658ae9/bench/results/m4-report-2026-10-23.md#the-h2c-slowdown)):
  `fit build` without `provider/standard` makes a binary of 19.9 MB against 21.1 MB with it.
  `bench/ab.sh` (which now takes `bin:PATH` for a binary built elsewhere) of the two, both from
  `fit build`, over h2c: fixed 3.81M → 3.85M requests a second (+1.0%, 3 of 3 rounds), the proxy
  238k → 236k (within noise). So the code the providers add costs fixed over h2c about 1% and the
  proxy nothing measurable: part of M4's 1.8% for fixed, none of its 1.5% for the proxy.

Differences from the plan:

- The plan named `plugin/build` and `cmd/fit`; the command line also moved to package `cli`, which
  a generated main must import, and providers became registered, to be left out.

### I71 WASM I: runtime and plugin pool — done

- **`plugin/wasm`'s `wasm`** middleware runs a WebAssembly plugin for each request of its routes,
  in wazero ([decision 017](decisions/017-wasm-runtime.md); `github.com/tetratelabs/wazero`
  v1.12.0, pure Go, its only dependency `golang.org/x/sys`). A plugin is a WASI reactor that exports
  `fit_on_request`; it reads the request's method, path, query, host, client address and its
  `config`, reads, sets, adds and deletes the request's fields at once and the response's for when
  its header is settled, answers the request itself (`respond`), and logs (at most 100 lines a
  second). The host functions are in the module `fit`, documented in the package.
- **`plugin/wasm/guest`** is the Go side of the ABI, for `GOOS=wasip1 GOARCH=wasm go build
  -buildmode=c-shared`: `OnRequest`, `RequestHeader`, `SetRequestHeader`, `SetResponseHeader`,
  `Respond` and the rest.
- **The pool**: `instances` goroutines (the CPUs fit has, at most 16), each with an instance of its
  own (`_initialize` run when it is made); a queue of `queue` (1024), past which a request is
  answered 503 at once; `wait_timeout` (1s) for a request waiting in it; a watchdog that stops a
  call past `timeout` (100ms) by cancelling its instance's context, after which the instance is
  made again; `max_memory` (128 MiB) an instance. A call that fails, traps or is stopped answers
  500, or with `on_error: skip` lets the request go on without the plugin. Nothing runs on a fib
  worker: the request is retained, with the config, and goes on from the pool's goroutine. The code
  is compiled once for every config that loads it (and kept in wazero's cache past its last), and
  instances are made when the config is loaded.
- **Tests** (`plugin/wasm`, with `testdata/headers` built for wasip1 by the go command, skipped
  without one): `TestHeaders` (request fields set and deleted, response fields set and added,
  through each instance), `TestRespond`, `TestFailures` (a call that loops past its timeout, one
  that panics, one that asks for more memory than `max_memory`: 500, or on with `skip`, and the
  next request served by the instance made again), `TestNeverBlocks` (ServeHTTP returns within
  100 ms; one call runs, one waits, the third is answered 503 at once), `TestShared`, `TestParams`.
  `test/workers` lists the middleware as checked by `TestNeverBlocks`. The full race suite passes
  on the benchmark host.
- **Cost** on the benchmark host: `BenchmarkWASM`, a request through the plugin of the tests,
  which reads the path, the method and its config and makes four changes to fields, against one
  without it, through fibtest: 5.1 µs against 0.31 µs, 34 allocations against 7.
  `BenchmarkGuestCall`, the call alone: 3.55 µs; so the hand-off to the pool and back costs about
  1.2 µs. Each part is bounded, by the instances and the queue, the timeouts, and the memory.
  Linking wazero into fit (3.9 MB more, 21.1 to 24.9 MB) moves nothing on the harness:
  `bench/ab.sh` of I70's build against this one, fixed and proxy over HTTP/1.1 and h2c, within
  the noise in all four.

Differences from the plan:

- The ABI is request-time only: a plugin changes the response's fields from the request, and is not
  called again once the response begins, which would need its call to hold the response's header
  back on the pool; Proxy-Wasm's response callbacks are I72's question.

### I72 WASM II: Proxy-Wasm subset — done

- **Proxy-Wasm's request phase** ([decision 018](decisions/018-proxy-wasm-subset.md)): the `wasm`
  middleware takes a plugin that exports `proxy_abi_version_0_2_0` or `_0_2_1` for a Proxy-Wasm
  filter. Each instance starts its root context with `vm_config` and `config` when it is made (and
  fails to load if the filter refuses them); each request is a context of its own, created, given
  its request headers (`proxy_on_request_headers`, end of stream for a request without a body),
  then done, logged and deleted. Host functions in the module `env`: logging (info and above, at
  most 100 lines a second), header maps (the request's, with `:method`, `:path`, `:authority` and
  `:scheme`, and the response's, whose changes apply when its header is settled; value, pairs, add,
  replace, remove, set pairs), local responses (status, body, headers, gRPC status), the VM and
  plugin configuration buffers, properties (the request's path, URL path, query, host, method,
  scheme, protocol, the client's address, `plugin_name`, and those `properties` sets), per-instance
  metrics, and `Unimplemented` for calls out, shared data and queues, ticks, foreign functions,
  setting properties and buffers, and continuing or closing streams. A filter that pauses a request
  without answering it fails as `on_error` says.
- **The response's callbacks are not made**: calling a plugin as the response's header is settled
  would hold the header back while the call runs on the pool, which fib cannot do; asked for as
  [A41](fib-api-requests.md#a41-a-responses-header-held-back-while-an-asynchronous-hook-runs).
- **Existing filters, unchanged**: `testdata/proxywasm` holds the Proxy-Wasm Go SDK's
  `http_routing` and `properties` examples as they are (Apache 2.0, a module of their own so that fit
  does not depend on the SDK), built for wasip1 by the tests. `TestProxyWasmRouting` (the filter
  reads its plugin configuration and changes `:authority`, which the handler after it sees as the
  request's host), `TestProxyWasmProperties` (401 for a request without the field a route property
  names, through `send_local_response`; on with it, and without the property), `TestPairs`. The
  tests of plugin/wasm pass under the race detector on the benchmark host.
- **Cost** on the benchmark host: `BenchmarkProxyWasm`, a request through `http_routing`, which
  rolls its die, reads its configuration and logs a line at info: 8.4 µs, 28 allocations, against
  fit's own ABI's 5.1 µs (I71).

Differences from the plan:

- The plan asked for the calls common filters need, or a note of why not: the request phase is
  done and runs existing filters unchanged; the response phase waits for A41, as the note says.
- **Later (2026-10-25): the response's callbacks came in I81**, with A41 and A46
  ([decision 023](decisions/023-proxy-wasm-response.md)).

### I73 Friendly config format — done

- **YAML** ([decision 019](decisions/019-yaml-config.md)): `adapter/yaml` turns a YAML config into
  native JSON key for key (YAML 1.2's types; anchors, aliases and merge keys resolved, bounded at a
  million nodes, 256 levels and 64 MiB of JSON; one document; keys strings and unique), recording
  where each config path's value was written; and native JSON into YAML (`FromJSON`), which adapts
  back to the same JSON. go.yaml.in/yaml/v3 was fit's dependency already (decision 011).
- **`adapter`**: adapters register by name and file extension; `Adapt`, `ReadFile`, `ForPath`, and
  `Result.MapErrors`, which moves the positions of a config's errors to the source's, from the
  nearest value on their path the adapter knows.
- **The CLI**: `fit run` (at start, on SIGHUP and with `-watch`) and `fit validate` take a `.yaml` or
  `.yml` file as they take JSON, with their errors at the YAML's lines and columns; `fit adapt`
  (`-c`, `-from`, `-to json|yaml`, `-o`) converts, checks the result as `fit run` would load it, and
  lists what an adapter could not carry over. `examples/hello.yaml` and `examples/proxy.yaml` are
  the JSON examples' twins.
- **Tests**: `TestRoundTrip` (every example and bench config, JSON to YAML to JSON the same, and
  YAML to JSON to YAML stable), `TestExamples` (the YAML twins say what the JSON does), `TestYAML`
  (types, YAML 1.2's strings, block scalars, anchors and merges), `TestErrors` (two documents, keys
  that are not strings or twice, infinities, unknown tags, an alias bomb), `TestPositions`;
  `adapter`'s `TestParent` and `TestForPath`; the CLI's `TestYAMLConfig` (an error at 4:27 of a
  YAML file) and `TestAdapt`. The packages the change reaches pass under the race detector on the
  benchmark host.

Differences from the plan:

- The Admin API still takes JSON alone; a YAML file's config is kept, and shown, as the native JSON
  it became.

### I74 nginx config converter — done

- **`adapter/nginx`**, registered for `.conf` and `.nginx` files: a parser of nginx's syntax
  (blocks, quotes and escapes, comments, `${var}`; bounded at 100,000 directives and 64 levels),
  and a converter of a common subset to native JSON, every directive it does not convert reported
  at its line, in line order:
  - **servers**: `listen` (addresses, `[::]` and `*` as fit's `:port`, `ssl`, `http2` and the
    `http2` directive as h2 or h2c, `quic` as h3), shared by the servers on one address, their
    certificates (`ssl_certificate`, `ssl_certificate_key`) together; `server_name` (exact,
    `*.` and `.` as fit's `**.`, `_` as no host); a server's `return` as one route for all of it;
    what no location takes, from the server's root or 404.
  - **locations**: `=`, prefix, `^~`, `~` and `~*` (Go's regular expressions; one Go does not
    take is skipped), nested, with nginx's order as priorities (exact, then `^~`, then regular
    expressions in order, then the longest prefix), and inheritance from `http` to `server` to
    `location` as nginx's (`add_header`, `limit_req` and `proxy_set_header` only when the inner
    context sets none).
  - **content**: `proxy_pass` to an `upstream` or an address (an upstream made for it; `https` as
    h2; its URI as a rewrite of the location's prefix; `proxy_set_header` with literal values,
    `Host`, and the forwarded fields fit sets itself; `proxy_connect_timeout` and
    `proxy_read_timeout` on the upstream); `root`, `alias`, `index`, `try_files` as the static
    handler (`$uri` as `{path}`); `return` as respond, or a redirect with `$scheme`, `$host`,
    `$request_uri`, `$uri` and `$args` as fit's placeholders; 404 for a location with none.
  - **upstreams**: servers with `weight`, `max_fails`, `fail_timeout`, `max_conns`, `down` left
    out, `backup` as an upstream of their own that the proxy fails over to; `least_conn`,
    `ip_hash`, `random`, `hash` on the URI, the client's address, a header or a cookie;
    `keepalive` as `max_idle_conns`.
  - **middleware**: `rewrite` as the rewrite middleware (made to replace the whole path, as nginx
    does) or, with `redirect`, `permanent` or a URL, a redirect; `limit_req` with its
    `limit_req_zone` as `rate_limit` (by address, a header or the route; without `nodelay` a
    warning, since fit refuses rather than delays); `client_max_body_size` as `body_limit`;
    `add_header` as `headers`; `gzip on` as `compress`.
  - **skipped, with the reason**: process settings (`user`, `worker_processes`, `events`, `pid`,
    `error_log`), `stream`, `include`, `map`, `if`, named locations, `error_page`, logs, TLS
    tuning (`ssl_protocols` and the rest, which fit chooses itself), buffers and timeouts fit's
    defaults stand for, values with variables fit cannot expand, and anything else.
- **The CLI**: `fit adapt -c nginx.conf` converts and lists the skipped directives; `fit run` and
  `fit validate` take a `.conf` file too, with errors at the nginx line that made the value.
- **Tests**: `TestSamples` (three sample configs, a TLS reverse proxy with a redirect and a
  balanced upstream with a backup, a single-page site, and a full `nginx.conf` with what fit does
  not convert: each loads, and the warnings are at exactly the lines left out), `TestProxySample`,
  `TestParse`, `TestDirectives` (names, listens, returns, rewrites, proxy_pass, upstream hashes,
  limit_req), `TestUnits`, and `TestServe`, which serves a converted config: a file through
  `try_files`, a `return`, a redirect, and `proxy_pass` with a URI to a backend; the CLI's
  `TestAdaptNginx`. They pass under the race detector on the benchmark host.

Differences from the plan:

- Beyond the plan's list, `client_max_body_size`, `add_header`, `gzip`, `try_files`, `alias`,
  `index` and the proxy's headers and timeouts convert, since nearly every config has them.

### I75 FastCGI — done

- **`handler/fastcgi`'s `fastcgi`** answers requests from a FastCGI application, as PHP-FPM is,
  at `address` (a host and port, or `unix/` and a socket's path). The client is fit's own: FastCGI
  records over fib connections dialed through fib's engine (which `module.Env` and
  `module.Context.Engine` now give modules), one request at a time on each connection with
  `KEEP_CONN`, idle ones kept for the next (`max_idle_conns`, 16, `idle_timeout`, 60s), at most
  `max_conns` (64), and a request finding none free waiting in a queue of `max_pending` (1024),
  past which it is answered 503. Nothing waits on a worker.
- **Streamed both ways, bounded**: the request's body goes up as it arrives (`OnBody`), held with
  `HoldBody` while the connection has more than 64 KB unsent; the response comes back as the
  application writes it (`WriteNoWait`, flushed), the connection's reads held while the client has
  more than 64 KB queued, each wait bounded by the write stall timeout. The CGI header is read up
  to `max_header_bytes` (64 KiB), its `Status` and `Location` (302 without a status) taken as
  HTTP's. A connection is kept only when the request ended as FastCGI says, its body all sent.
- **Timeouts**: `connect_timeout` (5s, 504), `response_header_timeout` (60s, 504),
  `response_idle_timeout` (60s), `timeout` for the whole (10m). A response cut short after it
  began is cut short for the client as the proxy does (the HTTP/1 connection closed, an HTTP/2
  stream ended with a `Fit-Error` trailer); a client that goes closes the application's connection.
  The application's standard error goes to fit's log, at most 100 lines a second.
- **Stale idle connections** (added after 1.0's iterations): a connection taken from the idle set,
  or handed on from the last request, may be one the application is closing, as PHP-FPM does when
  a worker reaches `pm.max_requests`, before fit hears. When it closes with nothing sent for the
  request, a request with no body and a method safe to repeat (GET, HEAD, OPTIONS, TRACE) is sent
  again, once, on a newly dialed connection, rather than answered 502, as net/http's client does;
  any other is answered 502, as the application may have run it. `TestStaleIdle` checks both.
- **PHP's parameters**: CGI/1.1's and PHP's (`SCRIPT_FILENAME`, `SCRIPT_NAME`, `PATH_INFO`,
  `PATH_TRANSLATED`, `DOCUMENT_ROOT`, `DOCUMENT_URI`, `REQUEST_URI`, `REQUEST_SCHEME`, `HTTPS`,
  `REDIRECT_STATUS` and the rest), the request's fields as `HTTP_*` but `Proxy` (httpoxy), and
  `env`'s. `split_path` (`.php`) splits a path into the script and its path info, and `index`
  (`index.php`) is a directory's script. `try_files`, as nginx's, picks the script from paths
  looked up under `root` on a pool of the handler's own, so that a front controller takes what is
  no script of its own.
- **Tests**: with Go's `net/http/fcgi` as the application, over HTTP/1.1 and h2c: `TestParams`,
  `TestKeepAlive` (20 requests on one connection; a script's status and Location),
  `TestLargeBodies` (a 10.5 MiB upload, chunked too, and a 16 MiB download read slowly at first),
  `TestTimeoutsAndLimits` (504 for a slow script; one busy, one waiting and the third 503),
  `TestCutConnections` (an application that cuts its connection mid-response; a client that goes,
  which closes the application's connection), `TestTryFilesAndUnix` (a Unix socket, a front
  controller), `TestSplit`, `TestRecords`. **Against PHP-FPM** (`php:8.3-fpm-alpine` in Docker,
  `test/e2e`'s `TestPHPFPM`, over HTTP/1.1 and h2c): `$_SERVER`'s script, file name, path info,
  query, method, URI, fields, host and address, httpoxy left out; a POST form and a 3 MB upload;
  a status, a field and a cookie; a redirect; a 4 MiB response; a front controller through
  `try_files`. `test/workers` has a case for the handler. They pass under the race detector on
  the benchmark host.

### I76 Continuous fuzzing — done

- **Eleven fuzz targets**, the four there were (`FuzzLoad` of config loading, `FuzzParseExpr` of
  the route expression parser, `FuzzReadSNI` and `FuzzParseProxy` of the layer 4 sniffers and the
  PROXY protocol) and seven new: `FuzzHopByHop` (the proxy leaves no hop-by-hop field, nor one
  Connection names), `FuzzForwarded` (a Forwarded element made from any address and host is one
  field value, with no control characters), `FuzzGRPCTimeout` (grpc-timeout's parser, and that
  what the proxy writes reads back no shorter), `FuzzReader` (FastCGI's record reader, fed
  anything in any pieces, keeps no more than a record), `FuzzConvert` (the nginx converter never
  panics, and gives JSON and warnings with lines), `FuzzAdapt` (YAML adapts to JSON, and that JSON
  written back as YAML adapts to the same) and `FuzzDirectives` (the cache's Cache-Control, Vary and
  Cache-Tag parsers give bounded values).
- **In CI**: the `fuzz` job of every push runs each target for 15s from its committed corpus;
  `.github/workflows/fuzz.yml` runs each for 10 minutes every day (and on demand, for as long as
  asked), the corpus it grows carried from run to run in the Actions cache, and a failing input
  kept as an artifact, to be added to `testdata/fuzz` with its fix.
- **Corpora**: each target ran for two minutes on the benchmark host, and what it found
  interesting is committed under its package's `testdata/fuzz` (about 2,700 inputs, 260 KB), so
  that `go test` runs them as seeds.
- **Found**: two round-trip bugs in the YAML adapter, both fixed with their inputs kept: `-0.`
  became JSON's `-0`, which came back from YAML an integer, 0 (negative zero is now written 0); and
  the string `<<`, written back to YAML unquoted, read as a merge key (now quoted). The other
  targets found nothing.

### I77 Config and Admin API v1 freeze — done

- **[`docs/compatibility.md`](compatibility.md)** (and its Chinese twin) is the policy from 1.0:
  what is covered (the native config, schema version 1, with the standard modules' params; the
  Admin API v1; the command line; metrics; the access log's fields; the `module` and
  `plugin/build` packages), what is not (messages, the dashboard, WASM's host functions and the
  Proxy-Wasm subset, experimental in 1.0), what a 1.x release may do (add; tighten a default only
  for security or a resource left unbounded; rename, below), and how a new version comes (a list of
  changes, `fit adapt` from the version before, which the new fit still reads with warnings for a
  release; a v2 Admin API under `/v2/` beside v1).
- **The schema's version**: a config may say `"schema_version": 1`; it is optional, and a fit
  that reads 1 refuses another rather than read it wrongly. The schema's title says "schema
  version 1" (`config.SchemaVersion`).
- **Renamed fields keep their old names**: a `renamed:"old"` tag lets the decoder take the old
  name, with a warning at its path, line and column, kept by the document (`Config.Warnings`, which
  includes what compiling decodes of the modules' params); giving both names is an error.
  `fit validate` and `fit run` print the warnings, at their place in the file, YAML too, at start
  and on each reload, and the Admin API returns them in a load's `warnings`. The schema lists the
  old name as `deprecated`.
- **The review** of every field's name found six that broke the rules the others follow (sizes in
  bytes end in `_bytes`, periods in `_interval`), renamed: `cache`'s `max_size`, `max_entry_size`
  and `disk.max_size` to `max_bytes`, `max_entry_bytes` and `disk.max_bytes`; `compress`'s
  `min_length` to `min_bytes`; `jwt`'s `jwks_refresh` to `jwks_refresh_interval`; `wasm`'s
  `max_memory` to `max_memory_bytes`. Their Go fields follow. The Admin API's paths and shapes
  were kept as they are.
- **Tests**: `TestRenamed` and `TestSchemaVersion` (config), a renamed field in `TestValidate`
  (cli) and in `TestWarnings` (admin), through `Apply` and `POST /load`. They pass under the race
  detector on the benchmark host.

### I78 Reference docs and migration guides — done

- **[Getting started](getting-started.md)**: installing; a first config, checked and run; files; an
  upstream with a health check; HTTPS by default, and certificates from files; middleware; and
  changing the config while it runs, by SIGHUP, `-watch` and the Admin API. Every config in it
  passes `fit validate`, and its Admin API commands were run against fit on the benchmark host,
  with the answers the guide describes.
- **[The config reference](reference/config.md)**, generated by `schema.Reference` from the same
  types and tags as the decoder and the schema: a table for the config and for each object in it,
  headed by its path (`listeners[].tls`), and one for each standard module's params, with each
  field's type (bounds and enums included), default and description, renamed fields' old names
  marked deprecated, and links between them. `module.Info` gained `Doc`, a module's description,
  which each standard module now has. `go test ./schema -update` writes it, and the schema's test
  fails when it is out of date or a field or module has no description, so that every field has
  one. `fit schema -reference` prints it for the modules a binary has, those `fit build` added
  included. The reference is in English, from the code; its Chinese page says how to read it.
- **Migration guides** from [nginx](migrating-from-nginx.md) (`fit adapt` first, then a table
  from directives to fields, and what works differently: route order, no variables, HTTPS by
  default, limits on by default), [Caddy](migrating-from-caddy.md) (a site translated whole, a
  table from directives, how the Admin APIs differ) and [Traefik](migrating-from-traefik.md)
  (entryPoints, routers and services; rules, which `match.expr` takes in Traefik's syntax,
  backticks included; middleware; providers and Docker labels). Each in English and Chinese.
- **The docs build**: `docs`'s `TestLinks` checks every relative link in the repository's
  Markdown, 86 files, that the file it names exists and the heading or anchor after its `#` too,
  as GitHub makes them. None was broken.
- The READMEs say what fit does now, and link the guides.
- **Tests**: `TestReferenceUpToDate`, `TestReferenceNeedsDescriptions` (schema), `TestSchema`
  with `-reference` (cli), `TestLinks` (docs). They pass under the race detector on the benchmark
  host, with the packages whose modules gained descriptions.

### I79 Caddyfile converter, JSON with comments — done

- **`adapter/caddyfile`**, registered for files named `Caddyfile` and ending in `.caddyfile`
  (`adapter.Register` now takes whole file names beside extensions, and `ForPath` matches them):
  a lexer and parser of Caddy's syntax (bare, quoted, backquoted and heredoc tokens, `#` comments,
  lines continued with `\`, `{$ENV}` and `{$ENV:default}`, snippets and their `import` with
  `{args[N]}`, a Caddyfile of one site without braces; bounded in tokens, nesting and imports),
  and a converter of a common subset ([decision 020](decisions/020-caddyfile-and-jsonc.md)):
  global options (`email`, `acme_ca`, `acme_ca_root`, `storage file_system`, `admin`,
  `auto_https`, `http_port`, `https_port`, `on_demand_tls`, `servers { protocols }`); site
  addresses with schemes, ports, paths and `bind`; named matchers (`path` with `*` anywhere,
  `path_regexp`, `host`, `method`, `header`, `query`, `remote_ip`, `client_ip`, `not` as
  `match.expr`); `handle`, `handle_path`, `route`, `reverse_proxy` (upstreams with port ranges and
  schemes, `lb_policy`, health checks, `header_up`, `header_down`, `transport http`, retries),
  `file_server`, `root`, `try_files`, `respond`, `redir`, `rewrite`, `uri`, `header`,
  `request_header`, `encode`, `basic_auth` (old base64 hashes too), `forward_auth`,
  `request_body`, `php_fastcgi`, `tls` (files, on demand, client auth) and `log`. Sites served only
  on the HTTPS port write no listeners, for HTTPS by default; otherwise listeners are written, and
  a route on the HTTP port redirects the HTTPS names. Each handler is a route; each middleware
  directive goes on the routes it covers, in Caddy's directive order, splitting a route it covers
  in part, root, try_files, rewrite, uri and redir taking the most specific of a block, as Caddy
  does. Everything skipped is a warning with its line. `fit adapt`, `fit run` and `fit validate`
  take Caddyfiles; the migration guide from Caddy says what converts and what works differently.
- **JSON with comments and trailing commas**: `config.Standardize` blanks out `//` and `/* */`
  comments and a comma before `]` or `}` (not in strings, and `[,]` stays an error), byte for
  byte, so positions stay the source's. fit's JSON parser runs it, so `config.Load` and
  `ParseValue` take such JSON; `adapter.Adapt` runs it for JSON files, and the Admin API's
  `Configs.update` before keeping a config, so that what is merged with providers, edited by
  pointer and read back is standard JSON.
- **Known gaps**, listed by the converter: `rewrite` and `uri` act after the route is picked;
  `php_fastcgi` beside `file_server` sends no path that is neither a file nor a script to
  `index.php`, since fit's FastCGI handler cannot fall back from files to the front controller;
  Caddy's internal CA, CEL expressions, `templates`, `map`, `handle_errors` and file imports are
  not converted.
- **Tests**: `TestSamples`, `TestSiteSample`, `TestFullSample`, `TestLex` (comments, strings,
  heredocs, environment, continued lines, errors), `TestParse` (snippets, keys across lines, a
  site without braces), `TestDirectives` (addresses, matchers, splits, handle, route, PHP, proxy
  options, global options), `TestIntersect`, `TestUnits`, `TestServe` (a converted Caddyfile
  served end to end) and `FuzzConvert`, in CI's fuzz jobs, with its corpus (adapter/caddyfile);
  `TestStandardize`, `TestLoadJSONC` (config); `TestLoadWithComments` (admin);
  `TestAdaptCaddyfile`, `TestJSONWithComments` (cli). They pass under the race detector on the
  benchmark host; `FuzzConvert` ran 30s there, about a million inputs, without a failure.
  Control-plane only: no benchmark (rule 21).
- **The config docs, after I79**: [Configuration](configuration.md), the entry to the three
  formats in the order Caddyfile, YAML, JSON, with the same site in each, how a config is run,
  checked, converted and changed; a page for each format ([Caddyfile](reference/caddyfile.md),
  directive by directive, with the global options, addresses, matchers and how directives become
  routes; [YAML](reference/yaml.md); [JSON](reference/json.md)); and the
  [config reference](reference/config.md) in chapters, one for each part of the config and for
  handlers and middleware, each saying what the part is for, with an example as a Caddyfile (where
  one says it), in YAML and in JSON, before its tables, and a table of contents. The chapters are
  Markdown in `schema/doc`, embedded and woven into the generated page by `schema.Reference`; the
  YAML is made from the JSON. `TestChapterExamples` (schema) loads each chapter's JSON, decodes
  each module's params with the module and converts each Caddyfile without a warning;
  `TestConfigExamples` (docs) loads every config on the format pages; `TestExampleCaddyfiles`
  (cli) validates `examples/hello.caddyfile` and `proxy.caddyfile`, the JSON examples' twins.
- **Config templates**: [templates](../templates/README.md) holds a complete config in each
  format. `templates/yaml/fit.yaml` and `templates/json/fit.json` are made by `schema.Template`
  (also `fit schema -template yaml|json`) from the same types and tags as the reference: every
  field of the config and of every module's params, each with its description, type, choices and
  default in a comment, a small config written out and the rest commented out with defaults or
  examples; the routes show every handler and every middleware with all their params.
  `templates/caddyfile/Caddyfile` is written by hand: every global option, address form, matcher,
  directive and subdirective the converter takes. `TestTemplatesUpToDate`, `TestTemplatesLoad`
  (each loads, and holds every field) and `TestCaddyfileTemplate` (schema). Two fixes found on
  the way: `fit validate` refused every config with the `fastcgi` handler, which needed an engine
  to compile (it now compiles to be checked, as upstreams do; `TestValidateFastCGI`), and the
  Caddyfile converter wrote a `php_fastcgi` with no root as `"."`, which fit refuses (it is now
  left out, with a warning), and warned of a site-wide `try_files` once per route (now once).

### I80 fit's own .konf format; configs in several files — done

- **`adapter/fitconf`**, registered for `.konf` files ([decision 021](decisions/021-fit-format-and-config-files.md)):
  a lexer and parser of blocks of directives (bare, quoted and backquoted words, `\` escapes, `#`
  comments, `;` between directives, `{}`, `{$ENV}` and `{$ENV:default}`; bounded in words and
  nesting), and a converter that reads each directive as a field of the Go type its block decodes
  into, through `config.Fields`: a scalar takes one argument, typed by its field (a value of the
  wrong type is kept as a string for the config's check to report where it was written), a list
  its arguments, repeated lines appending, an object or a map a block, a list of objects a block
  for each, a module its name and a block decoded by the module's own params type; at the top,
  `listener`, `route`, `upstream`, `service` and `provider` in the singular with the name as the
  argument. A field it does not know is converted as written, for the config's check to name.
  `site <hosts...> { route <paths...> { ... } }` expands to ordinary routes before the conversion,
  with the site's listeners, priority, match and middleware shared. Positions are recorded for
  every path, so that the config's errors point at the `.konf` file. `fitconf.FromJSON` writes
  native JSON as a `.konf` file that adapts back to the same config (routes one by one, not
  gathered into sites).
- **Several files and directories**: `-c` is repeated in `fit run`, `fit validate` and `fit adapt`
  and may name a directory, which stands for its `.konf`, `.yaml`, `.yml` and `.json` files,
  recursively and in lexical order, hidden ones left out (`adapter.Files`, at most 10,000 files and
  64 MiB). `adapter.AdaptSet` adapts each file and merges them: the top-level lists joined, any
  other field from one file only, `schema_version` from several if they agree; `Set.MapErrors`
  maps an error in the merged config to its file, its path there and its place (JSON files get a
  position lookup for this). `config.Error` has a `File`, printed before its position.
  `provider/file`'s watcher watches a set of paths, expanded at each look, so that a file added to
  a directory or taken out is a change, and hands over every file read; SIGHUP and `-watch` read
  them all again.
- **Formats in the docs**: the config docs, getting started, the README and the templates speak of
  `.konf`, YAML and JSON. [The .konf reference](reference/fit.md) is new; the Caddyfile page is now
  [converting a Caddyfile](reference/caddyfile.md), a migration tool beside the nginx converter,
  both of which stay, best converted with `-to fit`; the migration guides show `.konf`. The config
  reference's chapters show a `.konf` example made from their JSON by `FromJSON`, or one of their
  own (the routes chapter's, with a site), which `TestChapterExamples` checks is the same config;
  the chapters' Caddyfile examples and Caddyfile columns are gone. `templates/konf/fit.konf` is
  generated with the others (`fit schema -template fit`) and loads as it is;
  `templates/caddyfile` is gone, and `examples/*.caddyfile` became `examples/*.konf`.
- **Tests**: `TestConvert`, `TestErrors`, `TestPositions`, `TestRoundTrip` (the examples, the test
  configs and the JSON template written as `.konf` and read back), `TestWord`, `TestRegistered` and
  `FuzzAdapt`, in CI's fuzz jobs, with two seeds it found (a module's empty params written back
  without their `{}`; a string that reads as a number, in a list of no known type, written back
  unquoted) (adapter/fitconf); `TestFiles`, `TestMerge`, `TestMergeConflicts`,
  `TestMapErrors` (adapter); `TestDirectory` (provider/file); `TestExampleFitFiles`,
  `TestAdaptFit`, `TestSeveralFiles` (cli); `TestConfigDirectory` (test/e2e: a directory of `.konf`,
  YAML and JSON files run with `-watch`, files added, taken out and broken); the template and
  chapter tests (schema) and `TestConfigExamples` (docs: every `.konf` example on the format pages,
  in getting started and in the README loads). They pass under the race detector on the benchmark
  host, and `FuzzAdapt` ran 3 minutes there, about 1.8 million inputs, without a failure.
  Control-plane only: no benchmark (rule 21).
- **Later (2026-10-24): `.fit` became `.conf`, then `.konf`.** The format's files take `.konf`;
  neither `.fit` nor `.conf` is read, and the nginx and Caddyfile adapters take no extension or
  name: `fit run` and `fit validate` load only fit's own formats, and an nginx config or a
  Caddyfile is converted with `fit adapt -from nginx` or `-from caddyfile`
  ([decision 021](decisions/021-fit-format-and-config-files.md#later-the-extension-konf)).
  The entries above keep the name they had then.
- **Later (2026-10-26): `snippet` became `def`, read by `use`.** A block of directives defined
  once and read by name is `def <name> { ... }`, read by `use <name>`; `include` names files only,
  where it used to read a snippet by name too, so a name that is not a file is an error at its
  place. The docs, [decision 021](decisions/021-fit-format-and-config-files.md#later-def-and-use)
  and the bench config follow; `adapter/fitconf`'s tests cover the two apart. Control-plane only:
  no benchmark (rule 21).
- **Later (2026-10-27): `.konf` became `.conf` again.** The format's files, the templates
  (`templates/conf`), the examples, the bench config, the docs and the tests take `.conf`; `.konf`
  is not read. The nginx and Caddyfile adapters still take no extension, so a `.conf` file is read
  as fit's format
  ([decision 021](decisions/021-fit-format-and-config-files.md#later-the-extension-conf-again)).
  The entries above keep the name they had then. Control-plane only: no benchmark (rule 21).

### I81 WASM III: Proxy-Wasm's response callbacks — done

- **The response's callbacks** ([decision 023](decisions/023-proxy-wasm-response.md)): a Proxy-Wasm
  plugin that exports `proxy_on_response_headers`, `_body` or `_trailers` follows the response of
  each request it lets go on, its context kept open on the instance that made it (at most
  `max_streams`, 1024, an instance; past them 503). The calls are queued for that instance's
  goroutine (`worker.later`), before new requests, never run on the goroutine writing the response.
  `proxy_on_response_headers` runs while fib's `OnHeaderAsync` (A41) holds the header: the plugin
  reads `:status` and the fields, changes the fields, and its request phase's changes are applied
  first. The body goes through `proxy_on_response_body` by fib's `OnBodyFilterAsync` (A46, made for
  this in fib by fit's session): each call gets what was written since the last, read and replaced
  through `proxy_get_buffer_bytes` and `proxy_set_buffer_bytes`; a plugin that pauses holds the body
  and sees all it holds next time, up to `max_body_bytes` (1 MiB); the last call carries the trailer
  (map type 3), given to `proxy_on_response_trailers` when there is one, and the plugin may add one.
  Then, or when the request ends first (`OnCancel`, a filter aborted), the context is done, logged
  and deleted, and the config held for it released. `response: headers` gives the header alone,
  `none` neither. The property `response.code` reads the status.
- **Failures**: a callback that fails, traps or times out, a body held past `max_body_bytes`, and a
  local response sent once the response has begun fail it (the HTTP/1 connection closed, the stream
  reset); with `on_error: skip` the rest of the response goes on without the plugin. An instance made
  again loses its contexts, whose responses go as a failure does.
- **Tests**: the Proxy-Wasm Go SDK's `http_headers` and `http_body` examples, copied unchanged into
  `testdata/proxywasm` (with `gjson`, which `http_headers` needs), and `testdata/proxywasm/response`,
  a filter of fit's own: `TestProxyWasmResponseHeaders`, `TestProxyWasmResponseBody` (append, prepend,
  replace; `response: headers`), `TestProxyWasmResponseTrailers` (a body written in parts, each
  upper-cased, the trailer read, removed and added; a body without a trailer given one; a 204 with
  no body call), `TestProxyWasmResponseFails` (a late local response, a call past its timeout and a
  body held past its bound, with `on_error` fail and skip), `TestProxyWasmMaxStreams`,
  `TestProxyWasmResponseClientGone`, `TestBodyBuffer`; each checks that every context closed and
  every hold on the config was given back. `test/e2e`'s `TestProxyWasmResponse` proxies 5 bytes and
  300 KB, streamed with a trailer, through the filter over HTTP/1.1, h2c, h2, HTTP/1.1 over TLS and
  HTTP/3; `TestProxyWasmResponseSlowPeers` stalls a client on a 64 MB response through the filter
  and streams 512 KB through one that spins 20ms a call, while requests to a route without the
  filter and through it answer at once. They pass under the race detector, with vet and staticcheck.
- **fib**: A46 is on fib's `main` (`a88c75d2`), with its tests, the race detector on the packages it
  touches and their dependents, and vet and staticcheck for linux, darwin, windows and freebsd;
  fit depends on it by its hash (rules 15, 19).

Differences from the plan:

- A new iteration, after the plan's last: it waited for A41, and the body for A46, which fit's
  session added to fib.

### I82 fit's own log: loggers and levels — done

- **`log`** (package `logs`): what fit says about itself goes through loggers, `server`, `admin`,
  `tls`, `provider`, `telemetry`, `access_log`, `plugin`, `fastcgi` and `engine`, each message at
  debug, info, warn or error. `log.level` sets every logger's least level, `log.levels` some
  loggers' own; both change on reload (`admin.Configs` sets them as a config applies), while
  `output` and `format` wait for a restart, with a warning. Without `log` the lines are what they
  were, `fit: ` and the message, debug and info to stdout, warn and error to stderr. What fit's
  callbacks wrote went to stderr whatever it said; their info messages, a certificate obtained, an
  OCSP or OTLP export that works again, a Kubernetes provider's watch and status, are on stdout
  now, which `test/e2e`'s `TestACMESharedStorage` reads (it failed on CI's Linux, where Pebble
  runs, until it did). What
  `fit run` prints whole (config errors at their positions, `fit run: ...`) is written as it is in
  text, and without its prefix as JSON's `msg`. JSON writes `time`, `level`, `logger`, `msg`, and
  the attributes of `log/slog` records.
- **The engine**: `logs.SetDefault` makes the log Go's `slog` default, so fib's messages, and any
  library's `log.Printf`, are the `engine` logger's. fit still turns fib's startup lines off.
- **Levels by call site**: tlsmgr, the providers and the OTLP exporter took a `func(format, ...)`;
  they take a `*logs.Logger` now, and each message has its level (a retry warn, a certificate
  obtained info, one dropped or revoked error). Wasm plugins log at the level they ask:
  fit's ABI's `log(level, ...)` reads it as `log/slog` numbers it, 0 being info as before, and the
  guest package adds `LogAt`; Proxy-Wasm's trace and debug are debug, critical error. FastCGI's
  stderr is warn.
- **`access_log.level`**: a status below 400 is info, 4xx warn, 5xx error; `warn` writes only
  4xx and 5xx. The check is one atomic load and a comparison before anything is formatted, and a
  reload changes it.
- **Converters**: nginx's `error_log` (main, http, server; `notice` is info, `crit`, `alert`,
  `emerg` error, no level error as nginx's) is `log`, and `access_log` with `buffer=` and
  `flush=` is `access_log`; `log_format`, named formats and `if=` are reported. Caddy's global
  `log` and `debug` are `log` (stderr when no output, as Caddy's), a global `log` that includes
  only `http.log.access` is the access log, and a site's `log { level }` is `access_log.level`.
- **Tests**: `logs` (levels, reload, text and JSON, slog attributes and groups, the default log,
  a file), `cli`'s `TestServeLog` (a JSON log in a file, levels by logger, the access log at warn,
  and a reload to other levels), `config`'s `TestLogConfig`, and the converters' `TestLogs` and
  cases. They pass under the race detector, with vet and staticcheck.
- **Benchmark**: the access log's level check is on the request path, so `bench/microbench.sh`
  ran `observe`'s `BenchmarkLog`, HEAD against the change, 10 alternating runs on the benchmark
  host: 21.63 ns against 21.54 ns a line, no significant difference (p=0.53), 0 allocations
  both. The rest is off the request path, and the harness's servers write no logs (rule 21).

### I83 fit's log in fib's formats; loggers of their own; the access log's formats — done

- **Formats**: package `logs` writes through the handlers of fib's package `log` (fib
  `185658f1`), so fit's lines and fib's look alike: `log.format` is `line`, the default (time,
  level, message, then key=value), `text` (logfmt) or `json`; each message carries its logger as
  `logger`. The old `text`, `fit: ` and the message, is gone: before 1.0 the messages are not
  covered by the compatibility policy. `logs.SetDefault` hands the engine logger to
  `fiblog.SetLogger` as well as to `slog.SetDefault`. A line written whole by `fit run` is a
  message without its `fit: ` or `fit run: `, one a line.
- **Loggers of their own**: `log.loggers.<name>` gives a logger a `level`, an `output` and a
  `format` over the log's (`Log.LevelOf`, `OutputOf`, `FormatOf`); a level in both `levels` and
  `loggers` is refused. Loggers writing to one file share it, a message a Write under one lock;
  the default output still splits stdout and stderr by level. A reload warns when any logger's
  output or format changed, and when `access_log.format` did.
- **The access log's formats**: `access_log.format` is `json` (the default, its fields as
  before), `text` (`time=… level=… msg=access` and the fields) or `line` (local time and level,
  `access`, the fields), the same fields in the same order, a request's level its status's;
  values with a space, a quote, `=`, a control character or a byte past ASCII are quoted with
  `strconv.AppendQuote`, without allocating. Caddy's site `log { format console }` converts to
  `line`.
- **fib**: fit moved to fib `33a039f1`, whose CI passed (31 checks, 1 skipped). Its `ServeDuplex`
  had landed logging its panics to `slog` directly; `33a039f1` moved it to fib's `log`.
- **Tests**: `logs` (formats, loggers' own outputs and formats, a shared file and Close, the
  default log with fib's `log`), `observe`'s `TestTextAndLineFormats` and `TestStatusLevel`,
  `config`'s `TestLogConfig`, `cli`'s `TestServeLoggers` (lines in a file, the engine's JSON in
  its own, the access log as lines); `cli`'s and `test/e2e`'s helpers read the new lines as
  `fit: ` and the message. They pass under the race detector, with vet and staticcheck for linux,
  darwin, windows and freebsd.
- **Benchmark**: the JSON line's path changed, so `bench/microbench.sh` ran `observe`'s
  `BenchmarkLog`, main against the change, 10 alternating runs on the benchmark host. The first
  build, with the new fields in the middle of `AccessLog`, was 3–4% slower (21.47 against 22.17 ns,
  p=0.015); with them at its end, 21.75 against 21.50 ns, no significant difference (p=0.481),
  0 allocations both. `bench/ab.sh` with `access_log { output /dev/null }`, fixed, 3 rounds: h2c
  +0.0%, HTTP/1.1 +0.3%, within noise.

The sections from here to I107 (P2P's I84, the VPN's I94, the console's I104, the agent's and the
tunnel's I105–I107) and those from I108 to I111 (WebRTC, with P2P's I87 after I108, which it built
on) come from two lines of work done side by side on 2026-11-01, merged in `8ade7fc`; each line's
sections are in the order its work was done.

### I84 STUN messages and the STUN server — done

The first iteration of the P2P milestone ([decision 025](decisions/025-p2p.md)): ICE, STUN and
TURN built in fit on fib, interoperable with other agents, planned as I84–I93 in the
[development plan](dev-plan.md#p2p-nat-traversal), with two fib requests, A49 (a UDP peer opened
toward an address on a listener's socket, for I85) and A50 (QUIC DATAGRAM frames, for I90).

- **Messages**: package `p2p/stun`. `Message.Parse` takes a message in place: its attributes are
  offsets into the bytes it was given, bounded by `MaxAttrs` (64), and it refuses a bad length, an
  attribute past the end and a FINGERPRINT that is not last; past MESSAGE-INTEGRITY only
  MESSAGE-INTEGRITY-SHA256 and FINGERPRINT count (RFC 8489 section 14.5). `Build` and the `Add`
  methods append into a buffer the message owns, apart from what it parsed. Neither allocates once
  warm (`TestNoAllocs`).
- **Attributes**: the types of STUN, TURN, ICE and RFC 5780; addresses in the clear and XORed
  (IPv4-mapped addresses sent as IPv4), ERROR-CODE with STUN's and TURN's codes,
  UNKNOWN-ATTRIBUTES, 32- and 64-bit values; MESSAGE-INTEGRITY and MESSAGE-INTEGRITY-SHA256 with
  short-term keys and long-term ones (`LongTermKey`, `LongTermKeySHA256`), checked with the header's
  length ending at the attribute as the RFC has it; FINGERPRINT. `IsMessage` is RFC 7983's test,
  and `MessageLen` reads a message's length from its header, for STUN over TCP. Passwords are
  bytes, without OpaqueString (decision 025).
- **Server**: `stun.Server` is a fib handler for UDP and TCP bindings. A Binding request is
  answered with XOR-MAPPED-ADDRESS, SOFTWARE when configured and FINGERPRINT when the request had
  one; an unknown comprehension-required attribute gets a 420 naming it, another method a 400;
  indications, responses, what is not STUN and a bad fingerprint get nothing. Over TCP, messages
  are read out of the stream, copying only an incomplete one's bytes; a stream that is not STUN or
  announces more than `MaxTCPMessage` (4 KiB) is closed, and so is one with no whole message for
  `TCPIdleTimeout` (30 s). UDP peers are kept `UDPIdleTimeout` (10 s). Answers reuse pooled
  messages. It is not in the config yet: that is I92.
- **Tests**: RFC 5769's four vectors (a request with short-term integrity, IPv4 and IPv6
  responses, a request with long-term integrity) parse and check, and the responses are built again
  byte for byte; type encoding, round trips of every attribute kind, malformed messages, the
  server over UDP and TCP (requests split a byte at a time and two in one write, 420, 400, what is
  dropped, what closes a stream). `FuzzParse` is in CI's fuzz lists. They pass under the race
  detector, with vet and staticcheck for linux, darwin, windows and freebsd. The fuzz target was not
  run locally past its seeds; CI runs it.
- **Benchmark**: none: a new package off fit's request path (rule 21).

The next iteration is I85 (A49 in fib), which I86 and I87 wait on; I90 (A50) can run beside it.
Since then, I108 found A49 already built in fib, which closed I85, and decision 029 moved I86, I88
and I89 onto fib's `webrtc/stun`, `webrtc/turn` and `webrtc/ice`; I87 was built as `webrtc.turn`,
recorded after I108 below.

### I94 IP packets and the overlay route table — done

The first iteration of the VPN milestone ([decision 026](decisions/026-vpn.md)): fit nodes joined
into one overlay network with TUN devices, direct through ICE's holes or relayed by the server
node over QUIC, WebSocket or HTTP/2 in a configured order with fallback, or over the transport a
node is set to on the web page; planned as I94–I103 in the
[development plan](dev-plan.md#vpn-a-network-of-fit-nodes), built on the P2P milestone, with one
new fib request, A51 (a packet device's descriptor on the poller, for I95), besides A50 and the
mesh roadmap's F1.

- **Packets**: package `vpn/packet`. `Parse` reads an IPv4 or IPv6 header in place into a
  `Header` (version, protocol or first next header, length by the header, source and destination
  as `netip.Addr`), and refuses an empty or cut-short packet, another version, an IPv4 header
  length under 20 or past the packet, a total length under the header or past the bytes given, an
  IPv6 payload past them, and a jumbogram. Trailing bytes are allowed and `Len` says where the
  packet ends. Checksums are not checked.
- **Route table**: `NewTable` builds an immutable `Table` from `Route`s (a prefix and a node ID):
  each prefix masked, one map per prefix length in use, longest first, for IPv4 and IPv6 apart;
  invalid and IPv4-mapped prefixes, a prefix given twice (also once masked) and more than
  `MaxRoutes` (65536) are refused. `Lookup` returns the node of an address's longest prefix
  (IPv4-mapped addresses as IPv4) with no lock; `Allows(node, src)` is the receive-side check that
  a peer's packet comes from its own prefixes. Neither `Parse` nor a lookup allocates
  (`TestNoAllocs`).
- **Tests**: both versions' good and malformed headers; longest-prefix lookups with host routes,
  subnet routes inside subnet routes and a default route; `Allows` refusing another node's and a
  longer prefix's addresses; refused tables; 50 random tables of overlapping prefixes checked
  against a scan of every route. `FuzzParse` is in CI's fuzz lists and ran 20 s locally. They pass
  under the race detector, with vet and staticcheck for linux, darwin, windows and freebsd.
- **Benchmark**: `BenchmarkRoute` (parse and look up among 2,000 host routes) is there for I101's
  measurements; not run, as a new package off fit's request path with nothing to compare (rule 21).

The next VPN iterations that wait on nothing new are I95 (A51 in fib), I97 (Wintun) and I98 (the
relay over WebSocket).

### I104 The config kept encrypted; the console — done

The console milestone's one iteration ([decision 027](decisions/027-console-and-store.md)): a web
console behind a user name and password from which every part of the config is changed, and the
config kept encrypted in a file of fit's own format, its password in the bootstrap file.

- **Store**: package `admin/store`. `Open` reads a fitstore file (a 59-byte header: `FITSTORE`,
  version 1, Argon2id's time, memory and threads, a 16-byte salt and a 24-byte nonce; then the
  config sealed with XChaCha20-Poly1305, the header its additional data) or, when there is none,
  `Create`s one with a new salt. The key is derived once and kept, so a `Write` takes only a new
  nonce; it writes a 0600 temporary file beside the store, syncs it and renames it over. `Read`
  reads it again, deriving the key again only for another salt (after `fit store import`). A
  header asking past 16 passes, 1 GiB or no threads, and stores past 64 MiB, are refused.
- **Running from the store**: `admin.store` (`path` and one of `password`, `password_file`,
  `password_env`). `fit run` splits the bootstrap file's `admin` off (`admin.SplitAdmin`); an
  existing store is the base config, and the file's other members are left out with a warning;
  otherwise they seed the store, written before the listeners start. `Configs.UseStore` puts the
  bootstrap's `admin` into every config as it loads (`admin.WithAdmin`, at the end, after a line
  break, so errors keep their positions), refuses a base config with `admin` of its own, and saves
  each config applied whose hash differs from the last saved; a failed save is a warning, the
  config staying applied. SIGHUP reads the store again (`SourceStore`); `-watch` is refused.
  `fit store export` and `fit store import` (checked as `fit validate` checks, `admin` refused,
  a new salt, so changing the password is an export and an import).
- **Console**: `admin.console` (`address`, 127.0.0.1:2020; `cert_file` and `key_file`, needed off
  a loopback address; `users` with bcrypt hashes; `session_timeout` 30m, `session_lifetime` 12h,
  `max_sessions` 64, `max_conns` 32). `admin.StartConsole` serves on net/http with the Admin API's
  timeouts and limits: `POST /api/login` (JSON only; a dummy hash for unknown names; two checks at
  once; 5 failures in 15 minutes lock an address out for 15, in a table of 4,096), `/api/logout`,
  `/api/session`, `/api/schema` (`schema.Generate`, so modules built in are there too), and
  `/api/admin/...`, the Admin API's routes (now `Server.routes`) with the source `console`. The
  cookie is HttpOnly, SameSite=Strict and Secure over TLS, its SHA-256 kept; writes need the
  session's `X-Fit-Csrf`; an `Origin` other than the console's own is refused; pages have a CSP
  of their own files. Logins, failures, logouts and each change with its user are logged by the
  admin logger.
- **Pages**: `admin/consoleui`, plain HTML, CSS and JavaScript. A section for each top-level field
  but `admin`; a form made from the schema for objects (fields left out until set), lists (add,
  move, remove), maps, modules (a select, then the module's params' form), enums, numbers,
  booleans and the rest as text with the schema's pattern; each field's doc beside it; a JSON tab;
  versions with view and rollback. Saving sends the whole config with If-Match; errors are listed
  by path, linked to their section, and their fields marked.
- **Tests**: the store's round trip, mode, nonce and salt, a wrong password, every changed byte
  tried, bad headers, another writer's salt; `fit run` seeded, saving API changes, refusing
  `admin`, SIGHUP after an import, a restart from the store, a wrong password, `-watch`;
  `fit store` with refused imports and a password change; the console's login, lockout, sessions'
  idle and absolute ends and their limit, CSRF, origins, edits with ETags and the table of
  failures bounded; validation of both. Checked in a browser: login, an edit applied and served,
  the store rewritten and read after a restart, errors shown at their field. Race detector on
  `admin/...`, `cli`, `config` and `schema`.
- **Benchmark**: none; control plane (rule 21).

### I105 Roles in fit's config — done

The agent-and-tunnel track's foundation ([decision 028](decisions/028-llm-and-tunnel.md)).

- **Config**: `agent` (`hub`, `node`, `client`) and `tunnel` (`server`, `node`, `client`), each role
  a member; `RoleConsole` (admin.console's fields, the address required), checked as the console is;
  a config with roles and no routes needs no listener; loggers `agent` and `tunnel`.
- **`app`**: `Register(Info{ID, Member, New, Check})`; `Manager.Apply` starts, moves and stops the
  roles of each config applied, after the listeners (`Configs.UseApps`), a role's error a warning
  and, at start, fatal; `app.Check` in `runtime.Compile`. `Host` carries the listeners' engine
  (`listener.Server.Engine()`), the upstream engine and the role's logger. `Host.Edit` queues (at
  most 64) a change of the role's member, applied one at a time on the manager's goroutine through
  `Configs.EditMember`: the base config's member replaced by JSON pointer, members on the way made,
  source the role's ID, kept by `admin.store`, a warning without it. `app/standard` imports the
  roles; `cmd/fit` and `fit build` import it. `fit run` stops the roles before the listeners.
- **`login`**: admin.console's users, sessions, CSRF tokens and lockouts, moved to a package of its
  own; the console uses it.
- **`app/console`**: a role's pages as a binding on the listeners' engine: files from memory,
  `/api/login`, `/api/logout`, `/api/session`, the role's routes behind the session and its CSRF
  token, origin checks, the console's security headers, TLS, `max_conns`; bcrypt and a route's
  blocking work on a pool of 4 workers and 64 waiting (`Request.Go`), 503 past it.
- **Tests**: `login`; the console's login, CSRF, origins, files and stop; roles followed across
  configs, a failure as a warning, `EditMember` making a member and refusing one that does not
  load, `Host.Edit` and its refusal after `Stop`. Race detector on `admin`, `app/...`, `login`,
  `config`, `runtime`, `listener`, `cli`, `schema`, `docs`.
- **Benchmark**: none; control plane (rule 21).

### I106 The tunnel — done

fibhub as `tunnel/server`, `tunnel/node` (fibhub's pod) and `tunnel/client`; [the tunnel](tunnel.md).

- On the listeners' engine throughout: links over TCP, WebSocket and QUIC, the last on a UDP binding
  with the connections demultiplexed in fit (`tunnel/internal/transport/quic`, a stopgap for
  [A54](fib-api-requests.md#a54-a-raw-quic-endpoint-on-an-engines-udp-binding)); web proxies and the
  console socket framed over `fib.Connection` to have `HoldReads` (a stopgap for
  [A53](fib-api-requests.md#a53-backpressure-on-a-websocket-connections-sends)).
- `Apply` diffs listeners, uplinks, services, forwards and web proxies, keeping what is unchanged;
  after the first, a listener or forward that cannot bind is a warning and the rest applies.
- Every change on the pages, the server's self-signed QUIC certificate, and a server setting a
  node's services go through `Host.Edit`. fibhub's store, CLI, `-mode`, `webAuth` and password
  prompts are gone; wire names (`/fibhub`, ALPN `fibhub`, `pod` in frames) are kept.
- Rules: goroutines a link (32) and tunnels (64 a link, `max_tunnels`) bounded, frames of an
  unregistered link at most 64 KiB, the replay table capped, idle timeouts, keepalive.
- **Tests**: e2e on fib engines (TCP, WS, QUIC, body encryption, web proxies, wrong passwords,
  write-back and a restart from the store), slow peers, large transfers, cut connections; Docker:
  `test/docker/tunnel`, 208 checks and 17 after a restart. Not tested against fibhub's binaries.
- **Benchmark**: none yet; the tunnel's data path has no harness scenario.
- **Renamed**: the roles are now `tunnel.hub`, `tunnel.node` and `tunnel.proxy` (the server and
  client roles), in the packages `tunnel/hub`, `tunnel/node` and `tunnel/proxy`; the node's and
  proxy's `servers` are `hubs`, a forward's `server` is `hub`, and so on the wire ([the tunnel](tunnel.md)).

### I107 The agent — done

agenthub as `agent/hub`, `agent/node` and `agent/client`, on today's fib; [the agent](llm.md).

- The mesh on fib's WebSocket, its listener a binding of its own (`listen.address`, `/ws`), its
  streams paced by credits; a link that falls behind is dropped by `slow_timeout` (stopgaps for
  [A52](fib-api-requests.md#a52-a-websocket-connections-transport-a-forced-close-and-its-address)
  and A53).
- The node's proxy on fib's asynchronous client: bodies streamed with `OnBody` both ways,
  backpressure linked, `OnCancel` cancelling the upstream, connect, first byte, idle and total
  timeouts, `max_inflight` a model and a node; bodies over 1 MiB stream across the mesh. Jobs,
  GPU queries and hardware probes on bounded pools with timeouts; 8 jobs at once, 200 kept.
- The client's API on a binding of its own (`api.address`, TLS off loopback), forwarding with
  retries; key usage in memory only.
- Every change on the pages, a hub's managed change of a node, and the generated `id` go through
  `Host.Edit`. agenthub's state file, lock, `AGENTHUB_*` variables and console password are gone;
  the hub no longer keeps its node table across restarts.
- **Tests**: e2e on fib engines with a mock model upstream: calls, large and limited bodies, a
  slow agent, a slow upstream, first-byte timeout, cut connections both ways, relays with failover,
  managed nodes, write-back and a restart from the store, pages and SSE; Docker: `test/docker/agent`.
- **Benchmark**: none yet; the agent's proxy has no harness scenario.

### I108 WebRTC on fib's packages — done

The WebRTC track's first iteration ([decision 029](decisions/029-webrtc.md)), which amends the P2P
milestone's plan (decision 025): fib now has `webrtc`, with `webrtc/stun`, `webrtc/ice` and
`webrtc/turn` (a client, a sans-IO server core and `turnserver` on a fib engine), and fit uses them
rather than writing `p2p/turn` and `p2p/ice`.

- **Decision 029**: fit's WebRTC is fib's; I86–I89 use fib's packages; `p2p/stun` stays as the STUN
  server on listeners until I86 moves its messages to `webrtc/stun`; the config's `webrtc`, with
  the TURN server as the role `webrtc.turn` (I87), and `webrtc.media` with the modules `whip` and
  `whep` (I109); media servers proxied and UDP routed by ICE ufrag (I110). The plan's rows: I87
  amended, I108–I110 added; the next free iteration is I111.
- **fib**: `80c7f76` (`v1.6.1-0.20261031191422-80c7f7662ab4`), the first commit on fib's `main`
  with `webrtc`. The full test suite passes on it under the race detector, with vet; no change in
  fit was needed.
- **A49 corrected**: its entry named fib `4febcdf5`, never pushed, with an asynchronous
  `OpenUDPPeer(i, local, addr, handler, done)`. What fib built and pushed is `96a2331`, in
  `80c7f76`: `OpenUDPPeer(binding, addr) (*Connection, error)`, which waits for the socket's loop.
  The entry says so, and I85 is done.
- **fib requests**: A55 (TURN over TCP and TLS), A56 (a TURN server that never waits on a worker:
  `turnserver` calls `AddBinding` and `OpenUDPPeer` from `OnData`), A57 (per-allocation permission
  and channel caps, a peer check with the port, events for metrics), A58 (lifetimes and quotas
  changed in place, and a drain). The next free request is A59.
- **Benchmark**: none; a dependency move with no change on fit's request path (rule 21).

### I87 The TURN server — done

fib's TURN server as the role `webrtc.turn` ([decision 029](decisions/029-webrtc.md),
[WebRTC](webrtc.md)), in place of the `p2p/turn` decision 025 planned.

- **Config**: `webrtc.turn`: `listen` (UDP addresses, one binding), `realm`, `users` (a password,
  or coturn's hex long-term key), `secret` (coturn's `use-auth-secret`), `relay_ip`, `public_ip`,
  `port_min`/`port_max`, `max_allocations` (1024), `max_allocations_per_user` (16),
  `user_bandwidth` (2 MiB a second each way), the lifetimes, `nonce_secret`, `allowed_peers` and
  `denied_peers` over fib's `DeniedByDefault`; checked when loaded. The `webrtc` logger.
- **Role** (`webrtc/turn`): `turnserver.New` and `Start` on a binding of the listeners' engine,
  after a plain bind of each new address (fib binds with SO_REUSEPORT). Users, the secret, the
  peer lists and the bandwidth are read through an atomic pointer from fib's `Auth`, `PeerAllowed`
  and `OnRelay`, so a reload changes them with the allocations kept; another change starts a new
  server, after the old one when they share an address (the old one started again if the new one
  fails), before it otherwise. Bandwidth: a token bucket a username and direction, a second's
  burst, buckets that refilled dropped every 10 s, at most 65,536.
- **Metrics**: `app.Metered`, which the Manager collects from the running roles and the Admin
  API's `/metrics` (and OTLP) writes: allocations, bytes relayed each way, datagrams dropped for
  bandwidth, peers refused, restarts.
- **Stopgaps and gaps**: fib's server binds relayed addresses and opens peers with calls that wait
  on fib's binding lock and loops, from `OnData` on a worker: a breach of rule 1 until
  [A56](fib-api-requests.md#a56-a-turn-server-that-never-waits-on-a-worker), bounded by the
  allocation quotas. Not yet: TCP and TLS to the server (A55), per-allocation permission and channel
  caps, a peer rule with ports, events (A57), lifetimes and quotas changed in place and a drain (A58).
  All of them, the breach included, are gone since I111, on fib `84a67f7`.
- **Tests**: fib's `turn.Client` against the role over loopback (no pion): passwords, a key and
  ephemeral credentials, indications and channels both ways; wrong, unknown, expired and foreign
  credentials refused; 486 and 508; bandwidth dropping; loopback peers refused by default, allowed
  and denied lists; reloads keeping allocations, restarting on a quota on the same address, moving
  to another; a taken address refused with the server kept; a client cut midway freed at its
  lifetime while a peer floods its relayed address and another client relays; the limiter; the
  config's checks; a role's metrics in `/metrics`. Race detector on `webrtc/...`, `config`,
  `admin`, `app/...`, `schema`, `docs`.
- **Benchmark**: none; off the request path (rule 21). A relayed datagram costs fit an atomic load,
  a mutex and a bucket update in `OnRelay`, beside fib's own mutex.

### I109 WHIP and WHEP: an SFU on a shared Mux — done

fit as a small media server ([decision 029](decisions/029-webrtc.md),
[WebRTC](webrtc.md#whip-and-whep)): publishers in with WHIP (RFC 9725), viewers out with WHEP
(draft-ietf-wish-whep), on fib's `webrtc` PeerConnection and `Mux`.

- **Config**: `webrtc.media`, a role beside `webrtc.turn` whose member the Manager hands over
  whole (`*config.WebRTC`), for the TURN secret: `listen` (one UDP port), `public_ips`,
  `ice_servers`, `turn_urls` with `turn_credential_ttl` (1h), `codecs`, `max_peer_connections`
  (1024), `max_streams` (64), `max_viewers` (256), `max_bitrate` (8 Mbit/s), `max_sdp_bytes`
  (32 KiB), `connect_timeout` (30s), `ice_timeout` (30s), `key_frame_interval` (500ms); checked
  when loaded, `turn_urls` only with `webrtc.turn`'s secret.
- **Role** (`webrtc/media`): a `webrtc.Mux` on the listeners' engine, after a plain bind (SO_REUSEPORT),
  one DTLS certificate for its PeerConnections, settings swapped through an atomic pointer; a change
  of `listen` or `public_ips` ends the sessions (DTLS close through the old Mux, then it closes)
  and starts a new one. The roles' modules find it by the upstream engine they are given, which
  is the role's `Host.Upstream`.
- **Handlers** `whip` and `whep`: the stream from `stream` or the path's last segment; the offer
  taken with `OnBody` up to `max_sdp_bytes` and answered in its last callback, on the worker:
  fit is an ICE lite agent with the Mux's host candidates, so `GatheringComplete` is closed once
  the answer is set, and checked so, without a wait. 201 with `Location` and `Link` (RFC 9725 4.6,
  fit's TURN with coturn-style credentials); DELETE 200/404; OPTIONS 204; PATCH 405, as an ICE
  lite answer holds every candidate; 409, 404, 413, 415, 422, 503 with `Retry-After`.
- **Media**: one codec a kind, the publisher's first that `codecs` lists, so that viewers are
  offered what it sends; a `TrackLocal` a publisher section, `SetHeaderExtensions` from its
  `TrackRemote`, written from `OnRTP` on the worker the packet arrived on; a token bucket of
  `max_bitrate` with two seconds' burst and REMB once a second; viewers' PLI and FIR passed on as
  PLI at most once a `key_frame_interval` (CAS), and once when a viewer connects. Ending: DELETE,
  failed or closed PeerConnections (ICE lite silence past `ice_timeout`), `connect_timeout`; a
  publisher's end closes its viewers. PeerConnections are never called under the role's mutex.
- **Metrics**: PeerConnections and streams, sessions answered and ended by reason, refusals by
  reason, packets and bytes received, drops past the bitrate, key frame requests sent and held,
  restarts.
- **Gaps**: simulcast past the first layer, RTX, FEC and transport-wide congestion control are not
  forwarded, and there is no per-viewer bandwidth estimate, which fib's PeerConnection does not
  do; data channels are answered by fib but unused. No fib API request was needed.
- **Tests** (`webrtc/media`, fib's PeerConnections as clients over loopback, under the race
  detector): a publisher's packets reach three viewers intact; a viewer's PLI reaches the
  publisher, and a flood of them passes at most one a `key_frame_interval`; DELETE of a viewer and
  of the publisher, which ends its viewers; the limits (503 and `Retry-After`), 409, 404, 413
  (chunked), 415, 422 (no codec, a viewer without the publisher's codec), 400, 405; a publisher cut
  (its engine closed) ends at `ice_timeout` with its viewers; a viewer cut does not hold the others;
  `connect_timeout`; `Link` headers and a TURN credential checked against the secret; REMB and
  drops past `max_bitrate`; reloads keeping sessions, restarting on `public_ips`, and stopping
  without the member; the config's checks. In Chrome (the browser pane), a canvas
  `captureStream` published with WHIP to fit and played back with WHEP: VP8, 320x240, 65 frames
  decoded, DELETEs 200, 200 and 404.
- **Benchmark**: none; a new data plane off the HTTP hot path (rule 21). Forwarding costs, per
  packet and viewer, fib's SRTP protection and a send.

### I110 WHIP and WHEP proxied; UDP routed by ICE ufrag — done

Media servers behind fit ([decision 029](decisions/029-webrtc.md),
[WebRTC](webrtc.md#proxying-media-servers)): one HTTP endpoint and one UDP port for fit's own
`whip`/`whep` on other hosts or any WHIP or WHEP server.

- **Design**: the handler `webrtc_proxy` and the role `webrtc.proxy`, beside `webrtc.media`, rather
  than an `l4` service mode as the plan said: a reload drains an L4 service's relays with its
  graph, while media sessions must outlast reloads; the routes the handler writes must be found by
  the UDP side, which a process-wide role keyed by the upstream engine gives (as `whip` finds
  `webrtc.media`); and the relays go to addresses answers name, not to an upstream's.
- **Config**: `webrtc.proxy`: `listen`, `public_ips`, `max_sessions` (4096), `max_relays` (4096),
  `session_timeout` (30s), `idle_timeout` (30s); checked when loaded, and refused on
  `webrtc.media`'s port. The handler's params: `upstream`, `media_addrs` (an upstream address to
  an IP, or IP and port), `timeout` (10s), `max_sdp_bytes` (32 KiB).
- **Signaling** (`webrtc/media/proxyhttp.go`): the request taken with `OnBody` up to
  `max_sdp_bytes`, sent from its last callback with `Pool.Pick` and `Send`, the answer gathered
  with `ClientResponse.OnBody` up to the same, the client's `OnCancel` and a `timeout` timer
  cancelling the `Call`. An offer's candidates are removed, so the media server only ever sees
  fit; the answer's ufrag and ice-pwd are kept with the media address (`media_addrs`, or the first
  UDP host candidate, then srflx, IPv4 first), and its candidates and `c=` replaced with the
  role's addresses. `Location` becomes the POST's path and a 128-bit ID of fit's; `Link`s to ICE
  servers pass, those below the session's URL are rewritten, the rest dropped. A session's
  requests go to its media server with `Pool.PickBackend`, new in `upstream`; a trickle-only PATCH
  is answered 204, a restart forwarded and its ufrag added. `max_sessions` is reserved before the
  offer goes upstream; a 201 fit cannot route is DELETEd at the media server.
- **Relay** (`webrtc/media/relay.go`): a UDP binding of the listeners' engine; a new peer
  address's first datagram must be a Binding request whose USERNAME's first part is a known ufrag
  and whose MESSAGE-INTEGRITY its ice-pwd signs (fib's `stun`), else it is dropped and counted and
  its fib connection closed. Each relay dials the media address through the upstream engine,
  async, from a socket of its own: a media server on one port, like fib's Mux, tells peers apart
  by address. Up to 8 datagrams wait for the dial; the idle timer counts what the peer sends;
  8 relays a session, 4 ufrags a session.
- **Metrics**: sessions, relays, sessions answered, refusals, sessions ended by reason, relays
  opened, drops by reason, bytes each way, restarts.
- **fib**: [A59](fib-api-requests.md#a59-a-dial-from-a-chosen-local-address-and-port-range), a
  dial from a chosen local address and port range, for a firewall between fit and the media
  servers; until then the kernel picks them.
- **Tests** (`webrtc/media`, I109's role on three fits as media servers, fib PeerConnections as
  clients, under the race detector): three publishers spread one on each media server, a viewer
  each finding its stream's, media intact through the relay, every answered candidate fit's port,
  DELETEs reaching the right media server and freeing every ufrag and relay, 404 after; a DTLS
  datagram first, an unknown ufrag and a bad MESSAGE-INTEGRITY dropped and unanswered; a media
  server stopped: its peer's relay ends, its session's DELETE is 502 and frees it, and at most one
  offer fails before it is out of rotation; a cut viewer's relay ends at `idle_timeout` and its
  session at `session_timeout`; against a fake media server (`httptest`): candidates, `c=`,
  `Location` and `Link` rewritten, the offer reaching it without candidates, a trickle answered
  204 here and a restart's ufrag routed, 413, 415, a large answer and one without candidates 502
  (the latter DELETEd upstream), a slow media server 504 within `timeout`, a client gone during
  it, every reservation given back; `max_sessions` 503; reloads keeping sessions and media, a
  `public_ips` change keeping them, a `listen` change ending them, and no member answering 503.
  In Chrome (the browser pane), a canvas `captureStream` published through fit to one of two fit
  media servers and played back through fit: VP8, 320x240, both connected, 53 frames decoded in
  4s, DELETEs 200, 200, 404; the viewer's first try reached the other media server (404) and its
  retry the right one.
- **Benchmark**: none; a data plane off the HTTP hot path (rule 21). A relayed datagram costs a
  send on each side.
- **Limits**: candidates only by trickle after the answer, several media addresses, TCP ICE,
  streaming extensions (WHEP's SSE) under a session's URL, and the peer's own address at the
  media server are not supported; see the docs.

### I111 WebRTC on fib's A55–A59 — done

fib's A55–A59, built for fit's WebRTC ([decision 029](decisions/029-webrtc.md)), taken up in
`webrtc.turn` and `webrtc.proxy` ([WebRTC](webrtc.md)).

- **fib**: `84a67f7` (`v1.6.1-0.20261031213322-84a67f76ecfc`), with A56 (`74100b2`), A57
  (`bf669ce`), A58 (`496301d`), A55 (`97201b6`) and A59 (`3655193`). The bump changed
  `PeerAllowed` to take the port (`deps:` commit, the check reading the address alone until this
  iteration's).
- **Rule 1**: fib's TURN server binds relayed addresses and opens peers on goroutines of its own,
  from a bounded queue, and its core takes a lock per allocation: the breach I87 recorded is gone.
  What the queue drops is `fit_turn_dropped_total{reason="unsent"}`.
- **Config** (`webrtc.turn`): `listen_tcp`, `listen_tls` and `tls.certificates` (a type of its
  own, `TURNTLS`, so that a listener's `acme` and `client_auth` are not offered), `max_permissions`
  (64), `max_channels` (64), `max_stream_message` (8 KiB, at least 2048), `stream_idle_timeout`
  (30s); `allowed_peers` and `denied_peers` take an address with a port. (`webrtc.proxy`):
  `relay_ip`, `port_min`, `port_max`, checked when loaded. The certificates are loaded when the
  config compiles, which fails on one that does not load, and watched as a listener's are
  (`runtime`).
- **Peer check**: `relay_ip` and `public_ip` (of every server running, a draining one's too) are
  allowed with port 0 (a permission, by address alone), on a port one of the servers holds a relay
  on (`HoldsRelay`), and on a port `allowed_peers` names with them; never otherwise, whatever the
  prefixes say, so a private `relay_ip` no longer opens fit's other ports to the relay. Other
  peers go by `allowed_peers`, `denied_peers` (a port entry denies that port alone) and fib's
  `DeniedByDefault`.
- **Reload**: one server's member changed in place: `Core().SetLimits` for the quotas, caps and
  lifetimes, `SetAuth` for the users and secret (the AuthFunc now made per config), the peer rules
  and bandwidth through the atomic pointer, the certificates through another, read by each
  handshake. A change of the addresses starts the new server beside the old one, which
  `Shutdown` drains for `fit run -shutdown-timeout` (`app.Host.DrainTimeout`, the Manager's
  `SetDrainTimeout`), its listening bindings removed when it ends; a reload onto a draining
  server's address ends that drain first. A change of the realm, relay or public address, port
  range, nonce secret or stream bounds on the same addresses closes the old server first, as
  before, since the two cannot share an address. A port-0 address never counts as shared.
- **Metrics**: from `OnEvent`, `fit_turn_allocated_total`, `fit_turn_refreshed_total`,
  `fit_turn_freed_total{reason}`, `fit_turn_refused_total{code}`, `fit_turn_auth_failures_total`,
  and `fit_turn_peers_refused_total` (403s, no longer counted in the check itself, which every
  datagram now asks); from `Stats()` over the servers running, `fit_turn_allocations`,
  `fit_turn_pending_allocations`, `fit_turn_permissions`, `fit_turn_channels`,
  `fit_turn_unrelayed_total`; `fit_turn_draining_servers`. Events go to the `webrtc` logger at
  debug: it writes on the goroutine that logs, a fib worker here, so not at info. The proxy's
  `fit_webrtc_proxy_dropped_total{reason="no_port"}`.
- **Proxy**: each relay dials its media server with `Engine.DialWith`, from `relay_ip` and the
  range; `fib.ErrNoLocalPort` drops the peer address and counts it, its next request trying again.
- **Tests** (race detector): TCP and TLS allocations with fib's client (`ClientConfig.Stream`,
  `FrameLen`) relaying both ways, a closed connection freeing its allocation, an idle one closed at
  `stream_idle_timeout`, a new certificate in the same files served on reload without a restart;
  a third permission and a second channel refused with 508, and a cap raised in place; a port of
  `relay_ip` that is not a relay refused though `127.0.0.0/8` is allowed, the same listed with its
  port relayed to, two clients relaying to each other's relayed addresses, and the rules' order
  without a network; 403 counted as a refused peer; events and gauges in `/metrics`; a reload of
  users, quotas and lifetimes keeping the allocation (and 486 at the lowered per-user quota), a
  realm on the same address restarting, an address change draining (the old allocation relaying,
  508 for a new one there, the old address freed once it ends), the drain timeout, a reload back
  onto a draining address; a taken TCP address refused; the proxy's relay leaving from the one
  port of its range, a second peer dropped as `no_port` and let through once the port is free;
  `app`'s drain timeout; the compile-time certificate check; the config's checks.
- **Benchmark**: none; off the HTTP request path (rule 21). A relayed datagram now costs a peer
  check with its port (an atomic load and, on fit's own address, a sharded map lookup per server
  running) beside fib's per-allocation lock.
- **Gaps**: `webrtc.turn.tls` takes certificates from files, not ACME, which roles do not reach;
  a change of the realm or relay addresses on the same addresses still ends the allocations;
  ICE over TCP to media servers is still not relayed by `webrtc.proxy`.

### I112 The agent's model APIs — done

The agent serves every stateless inference endpoint of the mainstream model APIs
([Models and APIs](llm.md#models-and-apis)).

- **Kinds**: a model's `api` is `openai`, `anthropic`, `gemini`, `azure_openai`, `ollama` or
  `cohere`; `azure_openai` models are called as `openai`, so a name is unique per agent API
  (`config.AgentProtocol`, `model.Protocol`). `api_version`, required for `azure_openai` alone.
- **Calls**: OpenAI's chat, responses, completions, embeddings, moderations, images (generations,
  edits, variations), audio (speech, transcriptions, translations) and rerank; Anthropic's messages
  and count_tokens; Gemini's generateContent, streamGenerateContent, countTokens, embedContent and
  batchEmbedContents; Ollama's `/api/chat`, `/api/generate`, `/api/embed`; Cohere's `/v2/chat`,
  `/v2/embed`, `/v2/rerank`. Each a table entry (`model.ProxyPaths`); an Azure model refuses
  moderations, image variations and rerank with 404. No files, batches, stored responses or
  assistants.
- **The model**: read from a JSON body, a `multipart/form-data` body's `model` field
  (`model.FormField`, in place, the files' bytes untouched) or Gemini's path; `upstream` set there by
  the node: the JSON body or the form held whole within `proxy.max_body_bytes`, Gemini's path and
  Azure's deployment URL with the body streamed, Gemini's batch requests' models set too.
- **Node**: each kind's URL (`upstreamURL`), key header (`Authorization: Bearer`, `x-api-key`,
  `x-goog-api-key`, `api-key`) and model-list check (`/models`, `/openai/models?api-version=`,
  `/api/tags`); the body's content type passed upstream, checked to be JSON or a form with a
  boundary; only Gemini's `alt` query passes.
- **Client**: the vendors' SDK paths (`/v1/...`, `/v1beta/models/{model}:{method}` and
  `/v1/models/{model}:{method}`, `/api/...`, `/v2/...`); keys in `Authorization`, `x-api-key`,
  `x-goog-api-key` or Gemini's `key` query; errors in each API's shape (Gemini's RPC status,
  Ollama's and Cohere's); `GET /v1beta/models` and `GET /api/tags` beside `GET /v1/models`, whose
  entries are owned by `fit`; `X-Fit-Node` names the node.
- **Wire**: `ProxyReq` carries `content_type` and `query`; hubs pick nodes by the API agents speak.
- **Docs**: agent.client described by what it does, the path from a machine or LAN through a public
  hub to other machines' nodes, serving agents on its machine or LAN; the history of the programs
  the roles came from removed from the agent's docs, code and tests.
- **Tests** (race detector): the kinds' URLs, headers, rewrites and refusals at the node; multipart
  parsing edge cases; the client's paths, auth, error shapes and lists; e2e with the mock upstream
  per kind: 3 MiB uploads streamed and held to set the model, binary speech, a slow agent reading an
  8 MiB answer to an upload, an upstream cut mid-answer, an upload past the bound (413), Gemini SSE
  events as they come and an agent leaving mid-stream, its JSON-array stream and batch rewrite,
  Azure's deployment URL, Ollama's NDJSON and a cut, Cohere's SSE, and Azure and Gemini through a
  hub.
- **Benchmark**: none; the agent's proxy is off fit's HTTP request path (rule 21).
- **Gaps**: the client holds a body whole (within `api.max_body_bytes`), uploads included, to read
  the model and retry another route; Gemini's tuned models (`tunedModels/...`) and Vertex AI are
  not served.
- **Renamed**: the feature is now `llm`, its roles `llm.hub`, `llm.node` and `llm.proxy` (the
  client role), in the packages `llm/hub`, `llm/node` and `llm/proxy`; "agent" now means only
  the AI agents that call `llm.proxy` ([the LLM feature](llm.md)).

### I113 The LLM roles' node list, links and traffic — done

What the user asked for: nodes register with their hubs, the hubs sync the list of their nodes to
the proxies, which show it; each role's pages query and set its own settings and draw its links and
traffic as the tunnel's pages do. From the proxy, other machines are to see only.

- **Node list** ([The node list](llm.md#the-node-list)): `model.HubNodeList` of `model.NodeSummary`
  (ID, name, description, online and last seen, hardware without disks and addresses and with 16
  GPUs at most, 64 models at most with `models_total`, calls and jobs running, the hub's links with
  the node, its traffic to it); `/hub/node/list`, read by a proxy as it links, and the push
  `/hub/node/list/update`, after 200ms, a second apart, the traffic every 5s while proxies are
  linked; bounded by `max_nodes` and 4 MiB encoded, `total` counting what was left out; a sequence
  per list. The proxy keeps one list per hub, drops it with the hub, and shows it on its nodes page
  (`/proxy/node/list`, with the nodes it links to directly), with no action on other machines.
- **Links**: `wire.Session` counts the bytes of the frames each way and keeps its pings' round
  trips (last, moving average, shortest, after the tunnel's latency meter); a link pings too when
  its last round trip is two intervals old, so that both ends know it; `PeerConn.link`.
- **Routes**: `wire.Stream.Bytes`; `mesh.Routes` counts each role's calls per route (a proxy per
  hub or node and model, a hub per proxy, node and model, a node per caller and model): calls,
  failures, running, request and response bytes, read from the call's stream as it crosses;
  1024 routes, the rest as `other`. `/mesh/status` answers the links and routes with a time in
  milliseconds.
- **Pages**: `webui/static/flow.js` (`LinkMap`), the tunnel's `flow.js` adapted to the llm kit:
  columns, lines by traffic, dashed while idle, rates between two reads, latency good, fair or
  poor, details on a click, a table below; a Traffic page on each role, a Nodes page on the proxy.
  `webui/static/forms.js` (`Settings`): cards for `limits` (`/mesh/limits/get`, `/set`, every
  role), a node's upstream bounds (`/node/upstream/get`, `/set`), a hub's `max_nodes`
  (`/hub/settings/get`, `/set`) and a proxy's API (`/proxy/api/get`, `/set`, an empty address
  taking it out, a non-loopback address needing a certificate whose files load), written back
  without the defaults and checked as the config; `/mesh/status` is not logged as a change.
- **Tests** (race detector): wire's bytes and round trips, on both ends of an idle link; routes
  inbound and outbound, a running call counted as far as it got, a cut link's call failed, the
  bound; the list's summary, size bound, sequence and pace; e2e: a node joining, changing and
  leaving on the proxy, `max_nodes` set on the hub's pages forgetting the offline node, the hub's
  list gone with the hub; the same request and response bytes on proxy, hub and node for an inline
  and a 2 MiB streamed body, every link's latency, a link kicked mid-answer failing the call on all
  three; every new setting set, written back, refused when bad, the API over TLS and taken out.
- **Benchmark**: none; off fit's HTTP request path (rule 21). A mesh frame costs two atomic adds,
  a stream's data frame one more.
- **Gaps**: a hub counts a relayed call that ended well as a success whatever the upstream's
  status, since it does not read the head; the list's traffic and the hub to node latency on a
  proxy are up to 5s old; the browser pane used to check the pages lost a session on concurrent
  requests right after login, as the existing pages did there too, while curl's concurrent
  requests kept theirs, so it was left alone.

### I114 One workbench for every page — done

What the user asked for: every web page of fit's in one workbench, a menu on the left with the
gateway (reverse proxy, forward proxy and the rest), the tunnel and the LLM feature as modules whose
pages open as a tree, in the style of the LLM roles' pages.

- **Server** ([The pages](console.md#the-pages)): `app/console` serves modules, each under
  `/api/m/<module>/` (`Options.Modules`, `console.Module`, routes added on first use, `HandlePrefix`),
  `GET /api/workbench` (the user and the modules served), `MaxBody` and `Logf`; a login's body is 4
  KiB at most. `app.Paged` and `Manager.Modules` list the running roles' modules without waiting for
  an Apply. Every llm and tunnel role has its module; its own console serves it too, beside the old
  `/api/` paths.
- **admin.console** moved from net/http to `app/console` on the listeners' engine (decision 027
  amended): the config's module, `admin`, serves the schema and the Admin API's endpoints in
  process, on the console's pool, with the roles' modules beside it behind one login.
- **Pages**: `app/workbench/web`, one bundle every console serves: `core/` (the llm kit's `ui.js` and
  `ui.css`, now scoped per module with `UI.scope`, and `workbench.js`: the menu tree with filter,
  folding and a narrow mode, the router over modules, `WB.refresh` after a config that starts or
  stops a role); `gateway/` (overview, listeners, reverse proxy routes and upstreams, forward proxy,
  layer 4, TLS, cache, the rest of the config, JSON, versions: the old console's schema editor and
  the dashboard in the new style, one draft for every page); `tunnel/` (hub, node, proxy, rewritten
  from the old pages); `llm/` (hub, node, proxy, moved). The old per-role pages, the tunnel's
  static kit and `admin/consoleui` are gone; the Admin API's read-only `/dashboard/` stays.
- **Tests** (race detector): modules, prefixes and the workbench list on `app/console`; the manager's
  modules; the admin console serving a role's module; the bundle's files all loaded by
  `index.html`, core first; e2e paths moved. Every page of all modules went through in a browser,
  English and Chinese, against one fit running the gateway, three llm roles and three tunnel roles.
- **Benchmark**: none; control plane (rule 21).
- **Gaps**: the schema's field docs stay English on the Chinese pages; a role started by a config
  shows in the menu after `WB.refresh`, which the config pages call on save.

### I115 Media auth policies — done

What the user asked for: auth that can be configured for fit's WebRTC, its streaming media and the
other protocols that take media; I115 is the policies every one of them shares, wired into WebRTC
([decision 030](decisions/030-media-auth-and-pages.md)).

- **Config**: top-level `media_auth`, named policies with `tokens` (`token` or `token_sha256`, at
  least 16 characters), `users` (`password`, or a bcrypt `password_hash`), `secret` (at least 16
  bytes), `public` (`publish`, `play`) and `token_ttl` (12h); each token and user with `publish` and
  `play` stream patterns, `*` matching any run. Checked when loaded: unique names, a policy that
  lets someone in, one of token and token_sha256, one of password and password_hash, patterns.
  `module.Env.MediaAuth` and `module.Context.MediaAuth` give a module its config's policies.
- **Package `mediaauth`**: `Policy.Allow`, `Check`, `Login`, `Password`, `Issue`, `Guard`, `Public`,
  `TTL`, `HasUsers`; `FromHTTP` (Bearer, Basic, `?token=`, cookie `fit_media_token`; a query
  token is redacted in `RequestURI`, which the access log writes), `FromQuery` (`token`, `user` and
  `pass`); `Status` and `WriteError`; `Match`; signed tokens `fm1.<payload>.<mac>`, HMAC-SHA256
  under a key derived from the secret and the policy's name, or a random key of the process
  without a secret. bcrypt runs on the package's pool (a quarter of the CPUs, 256 waiting, 5s),
  never on a worker; passwords found right are remembered as keyed SHA-256s. Policies are found by
  engine and name (`Lookup`, `Provision`), and the role `media_auth` changes them in place with
  each config (`Apply`, `Remove`).
- **Handlers**: `whip`, `whep` (`auth`) and `webrtc_proxy` (`auth`, `action`) check a POST before
  its body is read: 401 with `WWW-Authenticate: Bearer`, 403, 503 when the pool is full or the
  policy gone. Session URLs (DELETE, OPTIONS, a proxied session's requests) need no credentials:
  the session ID is the capability.
- **CLI**: `fit media-token -c <config> -policy <name> -publish <patterns> -play <patterns> -ttl
  <d> -subject <s>` prints a signed token, its expiry on stderr; a policy without a secret is
  refused.
- **Tests** (race detector): patterns, static, hashed and signed tokens, tampering and expiry, a
  token of another policy or secret, public actions, bcrypt on the pool and remembered, a full pool,
  `Guard`'s answers, the registry's in-place reload and removal, `Provision`; config validation;
  `whip`, `whep` and `webrtc_proxy` end to end with 401, 403 and 201, a query token, Basic and a
  bcrypt user, DELETE without credentials, a reload that changes the policy in place and one that
  removes it; `fit media-token`'s token taken by a policy of the same secret, and its errors.
- **Benchmark**: none; the media handlers' signaling is off the hot path, and a route without
  `auth` does what it did (rule 21).
- **Gaps**: no Admin API endpoint issues tokens (the pages, I117, do); a token is revoked only
  by changing the secret.

### I116 The streaming role — done

What the user asked for: streaming media with configurable auth on every protocol that takes it
([decision 030](decisions/030-media-auth-and-pages.md), [streaming](streaming.md)).

- **Config**: top-level `streaming` with the hub's `max_streams` (64), `max_players` (256, a
  stream, over every protocol) and `gop_cache_bytes` (8 MiB); `rtmp` (`listen`, `auth`,
  `max_conns` 1024, `handshake_timeout` 10s, `idle_timeout` 30s, `play_wait`, `max_message_bytes`
  8 MiB); `rtsp` (`listen`, `auth`, `auth_methods` digest and basic, `realm`, `max_conns`,
  `session_timeout` and `idle_timeout` 60s, `play_wait`); `srt` (`listen`, `auth`, `max_conns`,
  `latency` 120ms, `passphrase`, `peer_idle_timeout` 5s, `play_wait`); `webrtc` (`listen`,
  `public_ips`, `ice_servers`, `max_sessions` 256, `connect_timeout` 15s, `play_wait`,
  `max_offer_bytes` 32 KiB, at most 64 KiB). Checked when loaded: a policy of each `auth`, the
  addresses and their clashes within the role and with `webrtc.media` and `webrtc.proxy`, the
  passphrase's length, the realm. `HasApps` counts the role.
- **Package `streaming`**: the role (`New`, `Apply`, `Stop`, `Lookup` by engine, `Hub`, `RTC`,
  `Addr`, `WriteMetrics`) and the handlers `httpflv` (`prefix`, `auth`, `wait`, `cors`), `hls`
  (`prefix`, `auth`, `format` fmp4 or ts, `target_duration`, `part_duration`, `playlist_size`,
  `max_bytes` 32 MiB, `play_wait`, `idle_timeout`, `cors`), `stream_whip` and `stream_whep`
  (`stream`, `prefix`, `auth`). Credentials: RTMP's query of the stream name or app; RTSP's Digest
  (fit's own challenges and nonces, against `Policy.Password`), Basic, or a query token; SRT's `s=`,
  `u=` and `s=`, or the resource's query; HTTP through `mediaauth.Guard`, HLS carrying a query
  token into its playlists' URIs. A `password_hash` user is refused once on RTMP, RTSP and SRT
  while the hash is checked off the worker, and passes at once after. Reloads: `auth` in place;
  another field of a listener restarts it; the hub's limits restart the hub; `hls`'s segmenters
  shared across configs by their params.
- **Metrics**: `fit_streaming_streams{state}`, `fit_streaming_players`,
  `fit_streaming_published_total`, `fit_streaming_auth_refused_total{protocol,reason}`,
  `fit_streaming_restarts_total`.
- **Tests** (race detector): config validation; RTMP publish and play by token, password and a
  bcrypt user (refused once, then let in), refusals counted; HTTP-FLV by query token, Bearer, Basic
  and cookie, 401, 403, 404; HLS's playlists naming the token, a segment with and without it;
  RTSP's challenges, Digest (and a stale or foreign nonce, another algorithm, qop), Basic, a bcrypt
  user's 503 then 200, a query token, 403, fib's client playing by Digest and publishing by token,
  and refused without; SRT's handshake by `s=`, `u=`, the query, 1401, 1403 and 1402 then let in;
  `stream_whip` and `stream_whep` 401, 403, a WHEP viewer of an RTMP stream through the route's
  session URL and its DELETE, a WHIP publisher played over RTMP; limits (connections closed as they
  open, streams, players); cut connections (a publisher cut ends its stream and its HTTP-FLV and
  RTMP players, a player that never reads holds up nothing, a silent client closed after the
  handshake timeout); reloads (auth in place with the publisher kept, a listener restarted on its
  address, the hub restarted, the role removed); 503 without the role or `streaming.webrtc`.
  ffmpeg 7.1 interop in a Linux container (`scripts/streaming-interop.sh`, `TestInterop`): RTMP
  with a token, RTSP with Digest and with a query token, SRT with `s=` publish, each played over
  RTMP, RTSP (TCP, Digest, UDP falling back to TCP), SRT, HTTP-FLV, HLS fMP4 and HLS MPEG-TS with
  credentials (64 cells, 93 to 100 frames of 100 each) and refused without; RTMP without and with a
  viewing token, RTSP without and with a wrong password, SRT without and with a viewing token are
  refused and never publish.
- **Benchmark**: none; a new role off the existing routes' hot path, and its fan-out is fib's
  (rule 21).
- **Gaps**: RTSP over UDP waits on fib's A61 (a SETUP for UDP is answered 461); RTMPS and SRT's
  per-stream passphrases are not offered; pull and relay of remote sources are out of scope; a
  bcrypt user's first attempt over RTMP, RTSP or SRT is refused by design.

### I117 The media pages — done

What the user asked for: basic pages for fit's media, behind auth that can be configured, which
switch the microphone and the camera on and off; for WebRTC the switch works whether or not the
device exists, and the other side reacts to the announced state instead of erring on media that
does not come ([decision 030](decisions/030-media-auth-and-pages.md)).

- **Handler `media_pages`** (package `handler/mediapages`): `prefix` (`/media`), `title`, `auth`,
  `login`, `whip` and `whep` (URL templates with `{stream}`, `/whip/{stream}` and
  `/whep/{stream}`), `hls`, `links` (`{stream}`, `{token}`, `{host}`), `ice_servers`, and the
  bounds `max_rooms` (64), `max_members` (16; twice as many event streams a room),
  `max_message_bytes` (1024), `member_ttl` (30s), `stall_timeout` (30s). Routes: `<prefix>` (308
  to `<prefix>/`), `<prefix>/`, `call`, `publish`, `play` (one page), `app.js`, `app.css` (ETag,
  `no-cache`; the page with a CSP of `'self'` and the endpoints' origins), `api/config`,
  `api/login`, `api/logout`, and the room API `api/rooms/<room>/join`, `members/<name>`,
  `leave` and `events` (SSE).
- **Auth**: the room API checks the policy with `Guard`: `play` on `<room>-*` to join and follow,
  `publish` on `<room>-<name>` to announce publishing (`403` otherwise, and on a later change); a
  member's changes need the key its join returned (128 random bits; `404` for a wrong one, `409`
  for a name another key holds). `login` takes `{user, password}`, `Policy.Login` on the bcrypt
  pool, and answers `Policy.Issue`'s token, also as the cookie `fit_media_token` (`HttpOnly`,
  `SameSite=Strict`, `Secure` over TLS, `Path=/`, `Max-Age` the token's); `logout` clears it.
- **Pages** (`web/index.html`, `app.js`, `app.css`, embedded; no external asset; English and
  Chinese): every publisher offers one `sendonly` audio and one video transceiver from the start;
  the switches (microphone, camera, screen) only `replaceTrack`: the device, a synthetic track
  without one (silent WebAudio; a 320x180 canvas with the name at 2 fps), none when off, the
  device stopped. The state is announced to the room API at each switch and every `member_ttl`/3;
  a member is played only once announced publishing (after its WHIP 201); a failed WHEP POST is
  retried at most 6 times, 1 s doubling to 15 s; the publisher retries with the same backoff,
  honours `Retry-After`, and stops on 401, 403 and 422. A session's DELETE goes before its
  PeerConnection closes (or after 2 s), so that its DTLS close does not end it first and answer the
  DELETE 404; a viewer of a member that left sends none, the server having ended it.
  `window.fitMedia` exposes `join`, `leave`, `mic`, `cam` and `status` for tests.
- **Rooms**: per engine and prefix, shared across reloads by refcount (`acquire`, `release`); one
  lock for the map work; a timer a room sweeps silent members and pings watchers every
  `member_ttl`/3, and forgets an empty room. Events are the whole member list, one shared
  snapshot a change, written with `WriteNoWait`; above the limit a watcher keeps only the latest
  snapshot and sends it on `OnWritable`, under a write deadline of `stall_timeout`. Each event
  stream retains the request and the config; `Drain` ends a swapped-out config's streams, and
  `EventSource` reconnects to the new one.
- **SFU**: `webrtc.media` checked, unchanged: a publisher sending nothing past `ice_timeout` and
  `connect_timeout` stays (consent keeps it), with its viewer; an audio-only publisher is played
  by a viewer of both kinds, video answered inactive, and by one of audio alone; a viewer of video
  alone gets `422`, not `500`. Tests `TestOneKindPublishers`, `TestSilentPublisher`
  (`webrtc/media/kinds_test.go`); `newPeerWith` in the harness for a client's consent interval.
- **Tests** (race detector): pages, assets, ETag, CSP, redirect, `api/config`; login with a
  plaintext and a bcrypt user, the cookie's attributes, a wrong password, a large body, logout,
  login off; params; the room API with 401 and 403, play-only users, a key's capability, `409`,
  `max_members`, `max_rooms`, the watcher bound, bodies past the limit, `member_ttl`, a room
  forgotten when empty; a watcher cut midway forgotten, a watcher that reads nothing cut after
  `stall_timeout` while another gets the last state of 100 members' 2000 changes; a reload keeping
  the rooms and draining the old config's streams, and the hub closed with the last config.
  Browser, `test/docker/mediapages/e2e.sh` (headless Chromium 156 in a Linux container, one browser with fake devices and one with
  none at all, driven over DevTools): login, three members playing each other, a member without
  devices sending synthetic tracks and shown "no microphone · no camera", microphone and camera
  off and on reaching the others, a silent member staying connected, a member joining with both
  off, one WHIP and one WHEP POST a session throughout (no renegotiation, no retries), leave, the
  publish and play pages, a stream not published, and no console errors.
- **Example**: `media-pages` in fit-examples (`fit.conf`): `media_auth`,
  `webrtc.media`, `whip`, `whep` and `media_pages` with a login.
- **Benchmark**: none; the pages and their API are off the hot path (rule 21).
- **Gaps**: no Admin API endpoint issues tokens (the pages' login does); the HLS mode plays only
  where the browser plays HLS itself (Safari), with no player script embedded; screen sharing is
  not covered by the browser test (headless Chromium has no screen to pick).

### I118 The media pages' room chat — done

What the user asked for: a chat in the room call of fit's media pages, text messages and small
files; a way to serve none of the pages, for a deployment whose page is its own; and the
microphone and camera off until a member switches them on (decision 030).

- **Params** (`Pages` in `handler/mediapages`): `pages` (`*bool`, true when left out): serve the
  pages; false builds no page and answers 404 for every path under the prefix that is not the API,
  the redirect from the prefix included, the login and the room API staying; `chat` (`*bool`, true
  when left out): the chat window and its API, `false` answering it 404; `mic_default` and
  `cam_default` (both false): the microphone and camera a member joins with, the URL's `mic=0|1`
  and `cam=0|1` overriding them; `max_file_bytes` (5 MiB, 1024 to 32 MiB): the largest file a room
  takes. `api/config` answers `call.mic`, `call.cam`, `chat.enabled` and `chat.maxFileBytes`.
- **The chat API** (below `api/rooms/<room>/`): `POST chat` takes `{"text"}`; `POST
  files?file=<name>` the raw body with its `Content-Type`, the name sanitized, 1 to 200 bytes;
  `GET files/<id>` the stored file; and `events`, with the chat on, starts with a `history` event
  and carries an `event: chat` for each new message, never before the stream's headers. A request
  names its member with `X-Fit-Member: <name>` and `X-Fit-Room-Key: <key>`, the key its join
  returned, so the key never goes in a URL; a wrong key, a name that is not a member's, or an
  unknown or evicted file is 404. Messages are immutable: `{"id", "from", "at", "text"}`, or
  `{"id", "from", "at", "file": {"name", "size", "type"}}`, the id 128 random bits in hex, also
  the file's id, and `at` Unix milliseconds. Auth is the room API's `Guard` (`play` on
  `<room>-*`, 401 or 403 as the rest); blank or invalid UTF-8 text is 400, a request's body past
  `max_message_bytes` 413, and a file past `max_file_bytes` 413 by its `Content-Length` or as
  it arrives. A download writes the stored bytes with `Content-Length`, the stored `Content-Type`
  or `application/octet-stream`, `Content-Disposition: attachment`,
  `X-Content-Type-Options: nosniff` and `Cache-Control: no-store`.
- **Bounds**: a room keeps at most 64 messages, text and files together, the oldest dropped;
  `chatFileBytes`, a package `var` at 64 MiB, bounds the files across the handler's rooms, adding
  a file past it dropping the oldest file messages wherever they are, whose download then 404s; a
  room forgotten when empty, or the hub closed, drops its bytes. A watcher that cannot take an
  event at once queues at most 256 events or 1 MiB, and is cut off past that; EventSource
  reconnects and the history refills it, the room snapshot still coalescing as before. Nothing
  blocks a worker: an upload is gathered with `readBody` under its limit, a download writes the
  stored slice, and the hub's work stays under short locks; the chat lives in memory only.
- **Pages** (`web/`): the call page's chat window, shown only when `chat.enabled`: name, local
  time and text of each message, a file message with its name, size and a download control, a
  text input whose Enter or Send posts, and a file picker whose file past
  `chat.maxFileBytes` is refused before sending with a hint; a message shows at once from the
  POST's answer and is deduplicated by id against the echo, the `history` event fills the window
  on join and leaving clears it, the panel has a bounded height and its own scroll, and a download
  is fetched with the page's credentials and saved as a blob. User text goes in with
  `textContent` only, there is no external asset and the CSP is unchanged, the texts are English
  and Chinese, and `window.fitMedia.chat`, `file`, `messages` and `chatEnabled` serve the tests.
  The panel sits beside the tiles, about 320px, on a wide window and below them on a narrow one,
  the spotlight still working. Joining starts the microphone and camera at `call.mic` and
  `call.cam`, unless the URL's `mic=0|1` or `cam=0|1` says otherwise, and the switches, the state
  announced to the room and the publisher's tracks agree after the member changes them.
- **Tests** (race detector): the params and their defaults; `pages: false` answering 404 with the
  login and the room API still served; chat off 404; a text message delivered over SSE to two
  members and in a late watcher's history; a wrong key; empty and oversized bodies; a file's round
  trip (bytes, name, type and the download's headers); 413 for an oversized file by
  `Content-Length` and chunked; the history cap; file eviction with `chatFileBytes` lowered; a slow
  watcher cut by chat traffic while a reading one keeps up; auth 401/403; a room forgotten dropping
  its bytes. Browser, `test/docker/mediapages/e2e.sh`, checks that the join starts with the
  microphone and camera off, that they are switched on, that a text message and a small file reach
  the other page and the file downloads, that an oversized file is refused with a hint, and that
  no page logs an error.
- **Benchmark**: none; the pages and their chat are off the hot path (rule 21).
- **Gaps**: the chat is in memory only, so a restart forgets it, while reloads keep the rooms;
  messages cannot be edited or deleted and a file's sender cannot remove it; a room keeps only its
  latest 64 messages, and a file the global 64 MiB bound evicts can no longer be downloaded.

### I119 Media tokens from a route — done

What the user asked for: a route a business backend calls to sign many time-limited `media_auth`
tokens at once, for many users, groups and concurrent meetings, and to revoke them when done with
them — without the backend holding the Admin API's token; then, because one revoke refused every
token of the policy, a keyring of signing keys, so that one shard or one meeting can be revoked
without a big outage.

- **Config** (`config/mediaauth.go`, `mediaauth`): `media_auth` gains `secret_file`, a file holding
  the secret tokens are signed with, read each time the config is applied, so that rotating the file
  and applying the config revokes every token signed before; it must hold at least 16 bytes, its
  line breaks at the end aside, and a missing or unreadable file fails the config. `secret` and
  `secret_file` are one of the two. `keys` is a keyring, in place of both: each `MediaKey` a `key`
  name of 1 to 64 letters, digits, '_', '.', '~' and '-' with its own `secret` or `secret_file`, at
  most 256 keys, unique names, and two keys sharing a secret refused at compile (`keys: "k1" and
  "k2" share a secret`), so a token verifies under one key only and rotating one cannot leave a
  twin good. `mediaauth.Policy` gains `SecretFile()`, the file the single key comes from, `Keys()`,
  `DefaultKey()`, `KeyFile()`, `IssueKey()`, and `RotateKey()`, which replaces one key's material
  so that only its tokens are refused, while `Rotate()` keeps rotating the single key and refuses a
  ring; both refuse a secret under 16 bytes and a removed policy, and all sit under the registry's
  lock, so a config apply cannot interleave. `fit media-token` takes a policy with any of the
  three, and its `-key` signs with a named key of a ring.
- **The handler** (`handler/mediatokens`): `prefix` (/media-tokens), the path the route serves
  under, a request outside it answered 404; `auth`, the policy that signs, without which
  provisioning fails (`media_tokens needs a media_auth policy with secret_file, or a keyring of
  secret_files: ...`), and a keyring must give every key a `secret_file` (`media_tokens needs a
  secret_file for every key of media_auth "live"'s keyring: ...`); `prefixes`, the stream-name
  prefixes every publish and play pattern must start with; `max_ttl` (24h), the longest ttl a
  request may ask for; `max_tokens` (100, 1 to 1000); `max_body_bytes` (64 KiB, 256 bytes to
  1 MiB); and `revoke_timeout` (10s). `POST <prefix>` takes `{"key": "<name>", "tokens":
  [{"subject", "publish", "play", "ttl"}]}` and answers `{"tokens": [{"token", "subject",
  "expires", "key"}]}` in the request's order: `key` signs the whole batch with that key of the
  ring, the ring's first when left out, and a policy with one secret takes none, giving one a 400;
  each answer token names the key that signed it, left out for a single key. `subject` 1 to 128
  bytes with no control character, each token at least one pattern, `ttl` a duration defaulting to
  the smaller of the policy's `token_ttl` and `max_ttl`. A batch is all or nothing: 400 for a bad
  body, subject, pattern, ttl, key, prefix or more than `max_tokens`, 405 for other methods and 413
  past `max_body_bytes`. Issuing is pure computation on the policy's state, so many requests run at
  once and none holds a lock or waits on a disk.
- **The revoke** (`POST <prefix>/revoke`): its body is optional; with a keyring `{"key": "<name>"}`
  rotates one key and `{"all": true}` every key, a body naming neither or a key the policy lacks is
  400, and with a single secret the empty body, `{}` or `{"all": true}` rotates the one key while a
  `key` is 400. One pool job then writes 32 random bytes from `crypto/rand` to each rotated key's
  `secret_file` atomically (a temp file in the same directory, mode 0600, written, closed, renamed)
  and replaces that key, so the tokens of a rotated key are refused at once by every handler sharing
  the policy (whip, whep, the media pages, the streaming role and `media_tokens`) while the tokens
  of the keys left alone stay good until their expiry. It answers `{"revoked": true, "since": <unix
  seconds>, "keys": ["k2"]}`, the keys rotated and left out for a single key, written from the pool
  through `c.Async`; a busy pool is 503 with `Retry-After: 1`, a write past `revoke_timeout` 504,
  and a failed write 500, which stops the revoke there, so on `all` the keys already written stay
  rotated. The files keep the new secrets, so a restart, which reads them again, keeps the
  revocation, as a config apply does. The route's middleware (`jwt`, `basic_auth`, `forward_auth`,
  `rate_limit`) is what authenticates the business backend: the handler checks no caller
  credentials and the Admin API is not involved.
- **Tests** (race detector): config refusing `keys` with `secret`/`secret_file`, and `secret` with
  `secret_file`, counting a keyring as letting someone in, and refusing a bad or duplicate key
  name, more than 256 keys, a key with neither or both secrets, and a short inline secret;
  `mediaauth` compiling a ring with an inline key and a file key, `Keys`, `DefaultKey`, `KeyFile`,
  `Issue` on the default and `IssueKey` on a named key, both tokens verifying, an unknown key not,
  `RotateKey` refusing one key's old token while the other's still verifies and a new token of the
  rotated key passing, a policy rebuilt from the files agreeing, `Rotate` on a ring erroring, and
  duplicate secrets refused; the handler's batch of two with different subjects, prefixes and ttls
  answered in order and accepted for the right streams and refused for others, a batch signed with
  a named key, an unknown key 400, a `key` on a single-key policy 400, the policy's ttl when
  omitted, 400 for a ttl above `max_ttl`, `max_tokens`, bad JSON, subject or pattern, and a prefix
  violation, 413 past `max_body_bytes`, GET 405, a path outside the prefix 404, all or nothing, a
  revoke of one key's 200 with its file holding a new secret, its old token refused and a new one
  accepted while the other key's token still joins, `{"all": true}` rotating every file, a revoke
  with neither on a ring 400, an inline key in the ring a provision error, no `secret_file` a
  provision error, the revoke pool's 503, 504 and 500, and concurrent issue and revoke. The CLI signs with a named key of a ring and
  refuses `-key` on a single-key policy. `TestChapterExamples` loads the chapters, the docs' link test and the workbench's schema test
  cover the new prose and Chinese descriptions.
- **Benchmark**: none; the handler is off the hot path (rule 21), a batch being some HMACs and one
  JSON encode, a token check now up to 256 HMACs at the end of a request's path, and a revoke
  running on its pool.
- **Gaps**: per-key revocation now exists, so one shard's, one meeting's or one group's tokens can
  be refused alone while the rest of the policy goes on. Revocation is still per key, not per
  subject: the tokens a key signed cannot be listed or counted, and one subject's tokens cannot be
  revoked alone, so a subject that needs its own revocation needs a key (and its own `secret_file`)
  of its own. The ring comes from the config: keys cannot be created, rotated or removed through
  the API, so a new shard or a removed one is a config apply. There is no audit beyond the answer's
  `since` and `keys`, and a policy that signs with `secret` cannot revoke at all: it has no file.
  The Admin API exposes no rotation, deliberately, so that a backend never holds its token.

### The examples moved to fit-examples — done

What the user asked for: the config examples in [lesismal/fit-examples](https://github.com/lesismal/fit-examples),
a directory per feature with its config in .conf, one file, first; YAML and JSON later, in that
repository's plan.

- **fit-examples**: every directory of `examples/` with its `fit.conf` and the files it names
  (`static/www`, the CA files, the WASM plugin), the Docker Compose example's config now in .conf,
  the Compose and Kubernetes examples building fit from a checkout in `FIT_DIR`, the template module
  as `github.com/lesismal/fit-examples/module/hello`. Its test runs `fit validate` on every
  `fit.conf`; its CI against the fit it pins and fit's `main`, and the Compose and kind examples.
  `fit.yaml` and `fit.json` are its plan's next items; the old ones are in this repository's
  history, before this change.
- **fit**: `examples/` is gone. The configs the tests used are `cli/testdata` (hello, proxy); the
  module `TestBuildWithModule` builds is `plugin/build/testdata/hello`. The tests that held the
  examples' three formats together go with them, to come back in fit-examples with its YAML and
  JSON. CI's new `examples` job validates fit-examples against this checkout and builds its module
  in; the packaging and Helm jobs run its Compose and Kubernetes examples with this checkout's image.
  The docs link to fit-examples.
- **Benchmark**: none; no code path changed.

## Notes for later iterations

- **The 2026-10-26 pass over the 2026-10-24 campaign.** Four measurements closed that campaign's
  open items. W4's large transfers: fit reads 64 KiB at a time now rather than 16, and fib's A47
  pools the body buffers instead of growing a copy at a time (`bench/ab.sh`, 10 s runs, 3
  alternating rounds on the benchmark host): proxy-download over HTTP/1.1 +80.5%, proxy-upload over
  h2 +41.9%, proxy-upload over HTTP/1.1 +21.9%, the rest within noise. HTTP/3: three fib commits,
  a stream's end arriving with its last data, a round's datagrams in one GSO send, and the leading
  1-RTT packet built in place, gave fixed h3 +10.9% (4 of 5 rounds) and site h3 +17.7% (5 of 5), with
  HTTP/1.1 unchanged; site's CPU a request rose, 76.8 to 92.5 µs, while its throughput rose, which
  is batching trading CPU for requests. W6's p99: `event_loops` 2 rather than 1 cuts h1's p99 by
  23–27% under the paced load that W6 is, and buys nothing in throughput either way (every paired
  delta within ±2%), but at saturation it makes p99 worse (api h1 +40%), so the default stays 1 and
  the note stays that one event loop is part of W6's 1.6 ms periodic stall, not all of it. h2c: the
  code-size suspicion is **rejected** — a build stripped of almost every module (6.47 MB against
  8.63) was 0.9% slower than the full one, one without the providers only 0.6% faster, and the
  instructions a request were the same in all of them while IPC fell (1.516 for M3's build, 1.504
  for main's, 1.492 for the stripped one). A profile-guided build was the only one that beat both,
  1.8% faster than M3's and 3.2% faster than main's; [decision 024](decisions/024-pgo.md) commits
  the profile at `cmd/fit/default.pgo`. W1: the profile puts the gap in user space, HPACK's Huffman
  decoding 6.96% of fit's CPU on h2 with a bearer token h2load never indexes, Go's maps 2.88% and GC
  2.82%; fib's table-driven Huffman decoding gave h2 +15.1% and the client's pooled request framing
  h1 +3.6% and h2 +4.1%. The client change is not merged: 120 lines of the hot path with two
  atomics and a buffer lifetime that crosses goroutines, for a few percent.
- **h2c 1.5–1.8% slower since M3 (the [M4 report](https://github.com/lesismal/fit/blob/3658ae9/bench/results/m4-report-2026-10-23.md#the-h2c-slowdown)).** `bench/ab.sh` of M3's build (`1486c28`) against M4's (`0dd5956`): fixed h2c −1.8%, proxy h2c −1.5%, the rest within noise. Bisected, it builds up over I59–I64 in steps each within noise (−0.8% for I59–I61, −0.8% for I62–I64); neither fib's move to `86071b8` nor I65's tracer check is it, and `router.Match`'s micro-benchmark did not move with I61's `**.` hosts. Those iterations grew fit's text from 6.14 MB to 6.42 MB with the providers, their clients, shared storage and the dashboard, which suggests code layout without proving it. Recorded, not fixed (rule 22), since nothing on the request path changed; I70 measured fit built without the providers: fixed over h2c 1.0% faster, the proxy within noise; so the code size accounts for part of fixed's 1.8% and none of the proxy's 1.5%, which stays unexplained.
- **fib version.** fit uses fib v1.6.0 (`55e4140`), and Go 1.27.3 with it. v1.6.0 is `fcb1718` with `d639b52`, a security fix: Go 1.27.3 (CVE-2026-111943) distrusts a set of intermediate CAs, which fib's full handshakes get from the toolchain, so fib needs Go 1.27.3; and a resumed TLS session's peer is verified again, as a full handshake would, on the server from a ticket's client chains and on the QUIC client from a session kept by server name alone. The tag is by rule 19's exception, the fib version wanted having one. Before it, fit used fib's `main` at `fcb1718` (`v1.5.7-0.20261023040847-fcb1718d079a`), which is `b5d406a` with fixes of two intermittent tests that failed fib's CI after it. `http`'s `TestBodyWriterHoldsNoGoroutine` counted every goroutine in the process, and over h2c each upload's body goes to the engine's shared handler pool, which grows a worker for a task that finds none idle and retires idle ones every second, so on CI's ubuntu runner the measured batch of 40 uploads added 47; it now leaves parked pool workers out, as `TestRetainStress` does. `benchmark/rpc`'s client read its counts as soon as its run ended, before the calls in flight finished, so `TestClientCountsFailures`' 50ms runs under the race detector on a slow macOS runner saw no gRPC call finish (calls=0 fails=0) and failed fib's CI on `b5d406a`; each caller now makes at least one call and the counts are taken once all have finished. `b5d406a` (`v1.5.7-0.20261023021744-b5d406a1d28c`), since I77, is `3998953` with a fix: a whole response a handler sends while the request's body is still arriving, more of it left than the server will read and throw away (a chunked body, one past 256 KiB, or one waiting for 100 Continue), says Connection: close, since the connection closes once the request is done. Without it, fit's `TestBasicOverNetwork` failed 17 runs in 30 on the benchmark host: the 401 for a wrong password went out keep-alive, and Go's client sent the next request on the connection fib then closed ("use of closed network connection"). With it, 30 in 30 pass; fib's `http` tests pass under the race detector, vet and staticcheck for every OS, and `BenchmarkServerKeepAlive`, `...POST` and `...Hooked` are unchanged, with the same allocations: of 10 alternating runs on the host, benchstat found `ServerKeepAlive` 15% slower (p=0.023, spreads of ±20%), which 20 runs did not bear out (746 ns against 753, p=0.92), and the change adds one type assertion to the path. `3998953` (`v1.5.7-0.20261022203111-39989533638b`), since I67, is `86071b8` with A40 (`BodyAborter`, and no `Content-Length` for the hooks after a filter). Both went on fib's `main` on 2026-10-23, with the maintainer's word. Before it, fit used fib's `main` at `86071b8` (`v1.5.7-0.20261022124907-86071b8bb871`), which is `4f1c3e9` with `5fc8250` and `aa0ba7e` (prefork, and connections balanced over a reuseport group by eBPF, which fit's `main` does not use yet) and A38 (`86071b8`, an HTTP/1 response to a request whose body failed says the connection ends; fit's `TestBodyOverNetwork` met a reset without it). `4f1c3e9` (`v1.5.7-0.20261021175136-4f1c3e9c13c9`) is `c3e27ed` with A36 (an idle timeout for SOCKS and CONNECT relays) and A37 (`ConnectProxy` answers from its dial's callback). `c3e27ed` (`v1.5.7-0.20261021172359-c3e27ed1667e`) is `9ab22e8` with A34 (`11de9eb`, a UDP peer's own idle timeout), A35 (`530d222`, a connection's peer address replaced) and `c3e27ed`, a race in http3's raw test client that lost stream data arriving before its reader and failed fib's macOS CI. `9ab22e8` is `929924e` with A32 (`0bd2447`, a request body streamed from its header, picked per request; `te: trailers` from the HTTP/2 client) and A33 (`9ab22e8`, a peer's end seen while reads are held is not lost). `929924e` is `fc7ec5e` with A28-A30 (a retained request upgraded after its handler returned, `Client.StartUpgrade`, extended CONNECT from the HTTP/3 client). `fc7ec5e` is `cfb4f09` with A27 (`Context.OnBodyFilter`). `cfb4f09` is `8e04da9` with A25 (`Context.LimitBody`) and A26 (every `OnCancel` callback kept), and a QUIC fix. `8e04da9` is `06992a5` with two test fixes (the retained HTTP/2 body test releases only once the handler has returned; the SOCKS UDP test opens the dial only once every datagram is in). `06992a5` is `b101811` with a UDP port left to the kernel bound without SO_REUSEADDR and SO_REUSEPORT, set after the bind instead, since Linux could hand a port already held to a second server (in fib's tests, a client's QUIC handshake reached another test's server, whose certificate it rightly refused). `b101811` is `a6d78cd` with the fixes of fib's intermittent tests and of a taskpool bug they found (one shard could take the whole shared budget, so MaxWorkers 16 ran 17 blocking tasks at once). `a6d78cd`, after v1.5.6, brought A22 (`7f632cb`, a client connection's idle timer set only when none is pending) and A24 (`a6d78cd`, a new taskpool ring writes none of its cells, which brings `handler/proxy`'s peak under the race detector from 1.42 GB to 540–720 MB). By commit rather than a v1.5.7 tag, as rule 19 has it. v1.5.6 brings A20, the HTTP/1 client writing request heads
  and reading response heads itself (R07f's notes below), with v1.5.5's fixes for fib's CI (a
  queued file's descriptor closed when its connection closes mid-send; the HTTP/1 idle deadline
  lifted before HTTP/2 takes a connection over) and `OnClose` run for the connections `Close`
  cuts (A23: without it an idle HTTP/2 connection's idle timer kept each closed engine in memory,
  `handler/proxy` peaked at 5.3GB under the race detector, and the swapping stalled an h2c upload
  past its timeout on macOS). v1.5.4 brought A16's stall bound (`SendFileStall`), A17 and the first part of A18 (`Context.Status`), made by fit's session in fib with the maintainer's leave (R07e); before it, fib's `main` at `8c637e0`, after v1.5.3, added connection debugging state and tests. v1.4.0 brought the APIs of R03 (A1–A7, A9, A11, A13, and
  parts of A8 and A12), v1.5.0 the rest of A12 (`Client.Start`), v1.5.1 A14 and A15, v1.5.2 the
  first part of A16, and v1.5.3 A19, a streamed request's `Trailer` set where its end is handed
  over, which CI's race detector had caught in the proxy's `TestTrailers/h2c`; see [fib-api-requests](fib-api-requests.md). v1.3.0, tagged for I16 and I22 on fib's `main` of
  2026-10-19, brought `fib.New(config, bindings...)` with `AddBinding`, `RemoveBinding` and
  `ShutdownBinding`, `Connection.NotifyOutput`, `Context.HoldBody` and `BodyWriter`; v1.3.1 adds
  `ClientConfig.StreamUnknownLengthAtHeader` (A10) and nothing else. If an iteration needs something
  newer, move to a fib commit by its hash (`go get github.com/lesismal/fib@<hash>`, rule 19),
  never to a `replace` directive or a `go.work`.
- **SO_REUSEPORT.** fib v1.3.0 listens with `ReusePort` on by default, which would let a second
  process bind a listener's address too. `listener` checks each address with a plain bind first and
  fails if it is taken.
- **Graph lifetime and async handlers.** `listener`'s dispatcher releases the graph when the
  handler returns. A handler that answers later holds the graph itself with
  `module.Context.Graph()` until it is done, and implements `module.Drainer` to end what it has when
  the graph is drained, as the proxy does (decision 004).
- **Proxied bodies of no length.** Until fib v1.3.1, fib's client handed the proxy a response body
  of no declared length only once 64 KB of it had arrived (`StreamResponseBodyThreshold`), so a
  slow trickle, as server-sent events are, reached the client late: in `handler/proxy/sse_test.go`
  the first event came after about 610 ms, when the stream ended, over HTTP/1.1, h2c and h2
  upstreams. fib v1.3.1 adds `StreamUnknownLengthAtHeader`
  ([A10](fib-api-requests.md#a10-stream-a-client-response-of-unknown-length-from-its-header)),
  which `upstream.New` turns on: such a body is handed over from its header, and bodies with a
  length are kept whole up to 64 KB as before. The test runs and passes on every upstream protocol.
  No benchmark: the harness's proxy backend answers with a `Content-Length`, which takes the same
  path as before.
- **Test hosts.** Only ordinary tests run locally: unit tests, the race detector, vet,
  staticcheck. Every load test runs on the benchmark host (rules 16-18), reached with
  `ssh -S ~/.ssh/fitbench.sock -p 10013 root@127.0.0.1`: the `fitbench` harness, wrk, h2load,
  `go test` micro-benchmarks, perf, and stress or flake reproduction (high `-count`, `-cpu`).
  The host has no Go: cross-build with `GOOS=linux GOARCH=amd64` (`go build`, `go test -c`) and
  run the binaries there. Micro-benchmarks recorded above as run on the development Mac predate
  this. The race detector cannot be cross-built from a Mac; since I36 the race tests have run on
  the host in Docker instead: a copy of the tree with `go mod vendor` is synced to the host and
  `go test -race` runs in the `golang:1.27-bookworm` image with `GOFLAGS=-mod=vendor`, keeping
  the build cache in a volume.
- **Benchmarks.** A new scenario or module that fit can serve goes into
  `bench/cmd/fitbench/servers.go` and `bench/configs/fit/fit.json`. nginx preallocates its
  connection tables (about 29 MB per worker with `worker_connections 65536`), which shows in its
  memory before any connection opens, not per connection.
- **staticcheck.** A staticcheck built with an older Go cannot read Go 1.27 export data. Build it
  as CI does (against the latest `golang.org/x/tools`) and pass it to make:
  `make check STATICCHECK=/path/to/staticcheck`.
- **Testing handlers.** `internal/fibtest.Serve` and `ServeRequest` run a fib handler through
  `fibhttp.NewStreamContext` with a recording stream, with no network. `Context.Respond` with a
  content type ignores `c.Header()`; a response with other fields goes through `WriteResponse`.
- **Error positions.** Error tests compare whole messages with line:column. Work out expected
  columns from the document rather than counting by hand.

### I120 The workbench's config pages and console passwords — done

What the user asked for: every admin console account can change its password (a logged-in user may also
reset another's), kept in the store file; the config menu with no Gateway/System tree, its global
modules and its protocol modules as peers in the sidebar; and every config page edited either as a form
(a tree with a checkbox for each optional member and inputs) or as raw `.conf`, `.yaml` or `.json` text
of the members that page is about, checked as `fit validate` checks a config before anything is
applied.

- **Store** (`admin/store`, `admin/bootstrap.go`, `admin/configs.go`, `cli`): fitstore's payload is
  version 2 — the sealed JSON is `{"config": <base config>, "users": [{"username", "password_hash"}]}`
  — the header's version byte is 2 on writes and 1 is still read, so a version 1 store is written as
  version 2 on the next write (the seed, a config apply or a password change). `Store.Users` and
  `Store.SetUsers`, with `Open`, `Read` and `Write` keeping their API and remembering the users
  (`Write` keeps them, `SetUsers` re-seals the config last read or written); the payload's config is
  embedded as it was written and one that is not JSON is refused. `admin.WithConsoleUsers` replaces
  `admin.console.users[].password_hash` by user name in the bootstrap file's admin, member order kept;
  `Configs.UseConsoleUsers`, `ConsoleUsers`, `SetConsoleUsers` and `SetConsolePassword` keep and write
  the overrides under `cs.mu` (the password read-modify-write is one step, so two changes at once do not
  lose one). `cli/run.go` merges the store's users into the bootstrap admin before the config loads and
  wires the overrides; `fit store import` keeps the users the old store held, and `export` still prints
  the config only.
- **Passwords** (`admin/console.go`, `login`): exact console routes `GET /api/users`
  (`{"user", "store", "users"}`), `POST /api/account/password` (`{"current_password", "password"}`) and
  `POST /api/users/password` (`{"username", "password"}`), under the admin module as
  `/api/m/admin/...`; bcrypt's verify, the new hash and the store write run in one `r.Go` on the
  console's pool; a wrong current password counts as a failed login for the address (403, then 429 with
  `Retry-After` once it is locked out), 400 for an empty or over-72-byte password and an unknown user,
  409 without a store, and 500 when the write fails, the old password still good. Success sets the
  login's hash, ends the user's other sessions (the caller's own stays) and logs
  `console: %q changed the password of %q` or `console: %q set the password of %q`. `login` gains
  `Users`, `HasUser`, `SetPassword` and `EndUserSessions`, with a users `RWMutex`.
- **Menu** (`app/workbench/web/core/workbench.js`): no group tree. The sidebar's first level is the
  modules, in a `RANK` order — overview, listeners, global settings (`sys.config`, named "Global" /
  "全局配置"), JSON, versions and account, then HTTP (reverse proxy, and the cache), HTTP CONNECT (the
  forward proxy), TCP/UDP (layer 4) and TLS — with the roles after them by group and order. A module
  with one page is a link; one with several opens its pages below, a small protocol tag under its label.
  The filter, fold/mini mode, per-module open state and the router are kept, and the breadcrumb is the
  module, then the page.
- **Config pages** (`app/workbench/web/gateway/config.js`, `modules.js`, `config.css`, `admin/check.go`,
  `admin/server.go`): `FitConfig.page(el, sh, render, opts)` with `opts.members` draws the `UI`,
  `.conf`, `.yaml` and `.json` tabs above the notice. The text is rendered by `POST /config/render`,
  parsed (400 ms after typing) by `POST /config/parse` into the shared draft, and saved by
  `PUT /config/text?format=&members=`, which adapts the text and merges those members into the base
  config inside `configs.Update`, so it is loaded, compiled, bound and saved to the store like every
  apply, with the ETag rules; its errors are mapped to the text's line and column and nothing is
  applied on a failure. When the draft has unsaved changes outside the page's members the page saves
  the whole draft with `PUT /config` instead, so no edit is dropped; text that does not parse blocks a
  save. The form gets an include checkbox for each optional non-boolean member (checking sets a default,
  unchecking deletes it). The pages' members are listeners, routes (both route pages, with a note that
  the text holds every route), upstreams, services, auto_https, acme, each system member, and the
  "other" list.
- **Account pages** (`app/workbench/web/account/account.js`): `#/account/password` (the current
  password, the new one and its confirmation) and `#/account/users` (the users, with a reset-password
  modal, the caller marked "you"); a callout and disabled forms when the console has no store.
- **Tests** (race detector): `admin`'s accounts (`GET /api/users`, a self change, a reset, the
  401/400/403/409/429/500 answers, sessions ended, a store round trip, a console restarted from the
  merged admin), `store`'s payload versions, users and writes, `login`'s users and session ends, `cli`'s
  openStore merge and import keeping the users, and the text endpoints (render/parse round trips in the
  three formats, parse errors with line and column, part saves replacing and removing members, foreign
  members and `admin` refused, an invalid config refused with line and column and the running config
  kept, 412, an unknown format). The workbench bundle's TestPages passes with the new files, and its
  schema words are still whole.
- **Benchmark**: none; the control plane (rule 21).
- **Gaps**: a role's console users (the config's `llm.<role>.console.users`) change with the config, not
  from the Account pages, which only `admin.console` serves; `fit store export` prints the config only,
  not the password overrides, so an export/import loses them when the old store does not open; the
  whole-draft save fallback shows the draft's paths, not a raw text's line and column, for its errors;
  builds in this session used the sibling fib checkout through a local `go.work`, since the repo's
  `go.mod` still names the fib hash before the proxy-splice commit (no fib change was needed for I120).

### I121 Consistency between the running service and its config — done

What the user asked for: check that every feature the running fit service has agrees with the config
it runs from; if not, list the differences and let the user confirm, one by one, how to fix each (add,
delete, change).

- **The checker** (`admin/drift.go`): `Configs.Check()` compares the runtime with the config it runs
  from and answers `GET /config/check` with the running version, its ETag, the differences and their
  counts. Rules: the restart-only members (`access_log`, `log`, `telemetry`, `engine`, `acme`,
  `providers`) that changed since fit started, against the values in force (`admin` is info: it comes
  from the bootstrap file); the engine limits `max_conns`, `max_pending_bytes` and
  `max_write_queue_bytes` that the config sets, against the running engine's `Settings()`; the roles
  configured against `app.Roles()` and the manager's `Running()`; the elements `Version.sources` says a
  provider added to the effective config, with `add` fixes that append them to the base config
  (`shadowed` elements are info); the listeners bound (`listener.Server.Addrs()`) against the graph's,
  with port `0` as info; certificates served by each listener against its effective `tls.acme` names
  (written names drift with a `delete` fix, names HTTPS by default adds and certificates obtained on
  demand are info); upstream pools against the effective config, with an all-down upstream as info; and
  cache zones in use against `cache.Zones()`, a zone kept from an earlier config being info. Each
  difference has a stable ID, a category, a kind (`drift` or `info`), the config and runtime values and
  its fixes: a patch (`add`/`delete`/`replace`) into the base config, or `reapply`. `Configs` now keeps
  the loaded `*config.Config` of the running version (`currentCfg`, no public JSON change) so the check
  does not reload the config, and the check reads the runtime under no `cs.mu` (the retained graph's
  certificate managers stand in for `Status()`), so `POST /config/reconcile` can recheck inside
  `configs.Update`'s edit callback, validate the selected IDs and actions, apply the patches (creating
  missing arrays for `/x/-` appends, deleting high indexes first) and answer as `PUT /config` does. A
  missing `If-Match` is 428, a stale one 412, and an unknown ID, unoffered action, empty fix list or
  vanished patch path 400, applying nothing.
- **CLI** (`cli/check.go`, `fit check`): loads the bootstrap config as `fit validate` does, reads only
  `admin.address` and `admin.token_file` (the rest of `admin` has fit's own JSON, so it is not decoded
  with encoding/json), reaches the Admin API over TCP or the `unix/` socket, prints the version, the
  drift/info counts and each difference with its values and fixes, and exits 0 (no drift), 1 (drift
  remains) or 2 (usage/config/connection error). `-json` prints the raw answer; `-fix` prompts for one
  fix per drift item (numbered, `s`/empty skips, re-prompting on bad input, EOF-safe), sends the chosen
  `{id, action}` list with `If-Match`, prints the applied version and warnings, rechecks and prints what
  remains. `fit help` lists it.
- **The pages** (`app/workbench/web/gateway/check.js`, `check.css`): a `sys.check` module after
  Versions in the flat menu, at `#/sys.check/check`, showing each difference with its category heading,
  kind badge, config and runtime values, a fix select per drift row (localized by action, patch path
  shown) and an unchecked apply tick, and a disabled-at-zero apply bar; the confirmation modal lists the
  chosen items before `POST /api/m/admin/config/reconcile` with the check's ETag; success toasts the
  applied version, clears the selection and rechecks, 412 shows the changed-elsewhere notice with
  Check again, 400 shows the server error and rechecks. Info rows are shown muted without controls; a
  green callout says when nothing drifted. English and Chinese words for every label.
- **Tests** (race detector): `admin`'s 18 drift tests (clean check, restart-only both directions and
  `admin` info, engine limits, written/auto/wildcard/served certificates, listener missing/extra/port 0,
  roles configured/running and their delete patch, provider pinning with the effective config unchanged
  and shadowed info, upstream extra/missing/down, cache missing/kept, endpoint ordering, If-Match 412/
  428, unknown ID/action, empty and duplicate fixes, missing-array creation, invalid-result rejection,
  reapply); `cli`'s 19 `fit check` tests with a TCP and a Unix fake Admin API (listing, `-json`,
  consistent/info-only exits, fix selection and skip, re-prompt, POST body and If-Match, 412, 400/500/
  503, tokens, no `admin`, a bootstrap whose console holds fit's own duration JSON, bad config/args,
  unreachable port, timeout, help); `app/workbench`'s bundle tests; and a real-endpoint browser run of
  the page (drift of an engine limit and a restart-only member, selecting both, the confirmation modal,
  apply, recheck clean, Chinese), plus `fit check` against a real demo (listing, `-json`, `-fix`
  applying and rechecking clean).
- **Benchmark**: none; the control plane (rule 21).
- **Gaps**: a role's `Apply` failing for a role already running is not observable, so the checker cannot
  see it; some role listeners bind asynchronously (an `llm.hub` whose listen address cannot be assigned
  counts as running while its mesh listener failed), so those are not drift either; HTTPS-by-default
  generated listeners are not listed as differences; routes and services appear only through provider
  additions because they are compiled from the effective config by construction; a cache zone kept from
  an earlier config is transient by nature; and a `network` field is not compared for listeners
  (address/protocol are).

### I122 The workbench's feature tabs and listener ports — done

What the user asked for: the left menu should have separate entries for each protocol/feature family —
forward proxy, reverse proxy, streaming media and the like — and the Listeners page should list, for
every listener port, all the features that port serves, each linking to the page where it is edited.

- **Feature pages** (`app/workbench/web/gateway/features.js`, `features.css`): `gw.streaming`
  (streaming), `gw.mediaauth` (`media_auth`), `gw.webrtc` (`webrtc`), `gw.tunnel` (`tunnel`) and
  `gw.llm` (`llm`) are modules of their own in the flat menu, after Cache and before the running roles'
  modules, each with a small tag (RTMP / RTSP / SRT, media tokens, WebRTC, Tunnel, LLM) and one page
  editing its member through the shared draft, so the UI/`.conf`/`.yaml`/`.json` tabs, the include
  checkboxes and the save bar work as on every config page. Their pages register `C.place` at load, so a
  save's problems link to the page that edits them from any page. Tunnel and LLM pages show the role
  note (the role starts when the config applies); the streaming page points at the routes where its
  handlers (HLS, HTTP-FLV, WHIP/WHEP) are configured. Global and the "other" page no longer list these
  members (`SYS` keeps log, access log, telemetry, metrics, limits, engine, providers), so no config
  page is duplicated.
- **Port features** (`modules.js` Listeners page): after the schema form, a "What each port serves"
  section with one card per configured listener, plus the ports HTTPS by default makes when the config
  writes none. Each card shows the name, the written address, the address the kernel actually bound
  when different (from one `GET /api/status` per draw, with a muted fallback note when it cannot be
  read), protocol badges, and a row per feature with its count and a link to the page that edits it:
  reverse-proxy routes (`#/gw.reverse/routes?handler=proxy`), forward-proxy routes
  (`#/gw.forward/routes`), the streaming handlers grouped with links to the streaming page and one
  filtered route link per handler, WebRTC handlers likewise, static/respond/FastCGI/media tokens/media
  pages with localized labels and filtered route links, cache middleware zones (`#/gw.cache/zones`) and
  the routes page, the other middleware grouped and named, layer 4 services (`#/gw.l4/services`), SOCKS
  (edited on that page), TLS certificates (`#/gw.tls/certs`), ACME (`#/gw.tls/acme`), TLS by default
  (`#/gw.tls/auto_https`) and the generated HTTP redirect (`#/gw.tls/auto_https`), with PROXY protocol,
  HTTP/3, on-demand and client-certificate tags; a port with nothing else says so. Routes and
  middleware are attributed only to listeners that serve HTTP. The reverse-proxy routes page reads
  `?handler=<module>` from the URL on entry and preselects that handler's filter tab, without overriding
  a tab clicked afterwards.
- **Tests**: the bundle's `TestPages` (every file loaded by index.html, core first) and `TestSchemaZh`
  pass; a headless-browser mock run of the admin endpoints checked 45 things — menu order/tags, card
  counts and every link target, the proxy deep link and the tab behaviour, a draft change redrawing the
  cards and re-reading `/api/status` once, the five feature pages, the "other" page, zh-CN words,
  HTTPS-by-default generated ports, `/api/status` failure degradation, TLS/ACME rows, and auto-HTTPS
  off; a real-backend browser run against a demo with five listeners (HTTP, TLS, SOCKS, TCP, UDP),
  proxy/static/respond/forward-proxy/HLS/media-token routes, cache middleware, layer 4 services, a
  streaming RTMP role and a media auth policy checked 16 things — the sidebar order and tags, the five
  port cards and their features, the `?handler=proxy` deep link filtering the routes page, the five
  feature pages with their four tabs, Global no longer listing the moved members, and the Chinese
  labels.
- **Benchmark**: none; the control plane (rule 21).
- **Gaps**: a SOCKS, TCP or UDP listener shows no route rows even though routes with no `listeners` are
  served by every HTTP-capable listener (they do not serve HTTP); the grouped streaming/WebRTC rows
  need one small link per handler because the route filter selects one module at a time; unknown handler
  modules are labelled by their id; and the port cards describe the config with the running
  `auto_https`-generated ports and bound addresses, not a live count of connections per port.

### I123 The workbench's Engine menu — done

What the user asked for: a new first-level left-menu entry **Engine / 引擎**, each listener port a submenu
of the features that port serves, in this order — handlers (static / respond / FastCGI / media tokens /
media pages → the routes page filtered to each), PROXY protocol / HTTP/3 / on-demand certificates /
client certificates as small tags, reverse-proxy and forward-proxy routes, cache (zones page and routes
page) and the other middleware, streaming handlers (streaming page and each handler's routes), WebRTC
handlers (WebRTC page and routes), layer 4 services, SOCKS, TLS certificates, ACME, HTTPS by default and
the HTTP redirect — and the per-port features out of the Listeners page.

- **Workbench menu** (`core/workbench.js`): a module's `nav` may be a function, called whenever the menu
  is built or rebuilt; a nav item with `kids` is a collapsible second-level node whose children are
  third-level links to other modules' pages (or muted rows); a nav item with `href` is an external link,
  and a module may consist only of links. A module may have a `boot` called once after the shell is
  built. `WB.refreshMenu()` recomputes the dynamic navs and redraws only the sidebar, keeping the open
  state, the page shown and the filter, then re-marks the active page and highlights the link whose
  `href` is the current hash. The filter is recursive, so a module or port stays visible when a
  descendant link matches (and opens while filtering). Other modules' static `nav` arrays are unchanged.
- **The Engine module** (`gateway/engine.js`): a first-level entry after Listeners, with no page of its
  own, whose nav is built from the shared config draft plus `GET /api/status`: one collapsible node per
  configured listener in order (or, when the config writes none and HTTPS by default is on, the generated
  `https`/`http` ports; or a link to the listener form when there is no port). A port's head shows its
  name, address and bound address (in the title), and a one-line line of its protocols and feature tags:
  PROXY protocol, HTTP/3, on-demand certificates, client certificates, HTTPS by default. Its children
  are, in the asked order: `static`, `respond`, `fastcgi`, `media_tokens`, `media_pages` and any other
  handler (each to the routes page filtered to it), reverse-proxy routes (`?handler=proxy`) and
  forward-proxy routes, cache zones and cached routes plus the other middleware grouped (naming the
  modules), the streaming page and each present streaming handler's filtered routes, the WebRTC page and
  its handlers' routes, layer 4 services, SOCKS (edited on the Listeners page), TLS certificates, ACME,
  automatic HTTPS, the HTTP redirect, and last the listener form. Each entry carries its count, a
  localized label and an icon; clicking it navigates to the page that edits it. The module loads once
  from `boot`, re-reads the draft when `config.js` dispatches `fit:config` after a config fetch, and
  rebuilds the menu through `WB.refreshMenu`.
- **The Listeners page** (`modules.js`): the port-feature cards of I122 are gone; the page says the
  features are in the Engine menu and keeps editing the listeners themselves. The reverse-proxy routes
  page still reads `?handler=` and preselects the handler's filter. `config.js` dispatches `fit:config`
  after a successful config fetch; `index.html` loads the new script and stylesheet.
- **Tests**: the bundle's `TestPages` (every file loaded by index.html, core first) and `TestSchemaZh`
  pass; `node --check` on the changed JS; a headless-browser mock of the admin endpoints (46 checks: menu structure/order/hrefs/counts/tags, clicks, filter, auto_https and no-listener cases, fit:config refresh, active highlight, zh, no uncaught exceptions); and a real-backend browser run against a demo with five
  listeners (HTTP with PROXY protocol, TLS with h2/h3, on-demand and client certificates, SOCKS, TCP,
  UDP), routes (proxy/static/respond/forward proxy/HLS/media tokens), cache middleware, layer 4
  services, a streaming RTMP role and a media auth policy: 13 checks passed — Engine after Listeners
  with its Ports tag, five visible port submenus, the port tag lines (PROXY protocol v1/v2, HTTP/3,
  on-demand and client certificates), the web port's entries in the exact asked order with their counts,
  the reverse-proxy entry landing on the filtered routes page with the link highlighted, the cache/
  streaming/port-config entries landing on their pages, the Listeners page with no cards and a pointer
  to Engine, the filter keeping Engine and the port for an "hls" match, the menu following a config
  change (a second proxy route made the count 2), and the Chinese labels/order.
- **Benchmark**: none; the control plane (rule 21).
- **Gaps**: the third level is links only (no Engine page of its own; the port head toggles rather than
  opening a page); a SOCKS/TCP/UDP port shows no route entries (it serves no HTTP); a feature with two
  pages is two entries (cache zones / cached routes, streaming config / each handler's routes); the
  port's bound address and the status-failure note are in the row's title, not visible; the menu keeps
  as many ports as the config has, which is long for a config with dozens of listeners.

### Multipath TCP as a listener's option — done

What the user asked for: a listener may opt in to Multipath TCP, off by default, on the config's
`listeners`, the Admin API and its console, a role's console, streaming's RTMP and RTSP listeners and
TURN's TCP and TLS listeners; a peer without Multipath TCP falls back to plain TCP.

- **fib** ([A63](fib-api-requests.md#a63-multipath-tcp-as-a-listeners-option)):
  `fib.Binding.MultipathTCP` between `Addrs` and `Handler`. A TCP binding with it listens as
  `IPPROTO_MPTCP` (262) on Linux 5.6+ where `net.mptcp.enabled` is set, on the claim socket a spread
  listener uses for its port, on every per-poller socket and on prefork children's copies, and falls
  back to plain TCP per socket on any error; a peer without it falls back in the handshake. UDP and
  Unix bindings are unaffected, other platforms listen plain TCP, and bind, listen and `SO_REUSEPORT`
  errors are still returned. fit moved to the commit by hash ([rule 19](rules.md#9-the-fib-version)).
- **Config** (`config/config.go`, `config/apps.go`, `config/streaming.go`, `config/webrtc.go`):
  `mptcp` on `listeners[]`, `admin`, `admin.console`, `config.RoleConsole` (whose `Console()` carries
  it to its console's binding, as `admin.console`'s does), `streaming.rtmp`, `streaming.rtsp` and
  `webrtc.turn`, each with its `doc` tag and off by default; `config/validate.go` refuses it on a
  listener that takes UDP alone and on a TURN server with neither `listen_tcp` nor `listen_tls`,
  which have no TCP connection for it to carry.
- **Runtime and listeners** (`runtime/graph.go`, `listener/listener.go`): `runtime.Listener.MPTCP`;
  a listener binding's identity includes it, so a reload that changes it makes a new binding, a
  listening socket's transport being fixed once it is made, and both `fib.Binding` literals pass it;
  `udp` listeners and HTTP/3 pass none.
- **Admin API and consoles** (`admin/server.go`, `admin/console.go`, `app/console/console.go`,
  `internal/nettcp`): the Admin API listens through `internal/nettcp`, renamed from `internal/plaintcp`
  and now `Listen(network, address, multipathTCP bool)`, so that a plain-TCP listener never takes
  Multipath TCP from Go 1.24's `net.Listen`, which opens one wherever `GODEBUG=multipathtcp` allows;
  the console and role consoles bind on the engine through `app/console`, which passes the console's
  `mptcp`.
- **Streaming and TURN** (`streaming/listeners.go`, `webrtc/turn/turn.go`): RTMP and RTSP pass it to
  their `bindTCP` and include it in their listener keys, and TURN's static key includes it, so a change
  restarts that listener or starts a new TURN server and drains the old one; SRT and TURN's `listen`
  are UDP and take none.
- **Workbench** (`app/workbench/web/gateway/engine.js`, `schema-zh.js`): the Engine menu tags a port
  that has `mptcp` (MPTCP in both languages), and the seven schema descriptions have their Chinese.
- **Docs**: a Multipath TCP bullet in the Listeners chapter and a sentence where the Admin API,
  streaming and WebRTC chapters describe their listeners; the Chinese reference's Listeners, Admin,
  Streaming and WebRTC rows and the configuration page's listeners row; A63 in both API-request
  files; the schema and the reference, with their Chinese twin, regenerated.
- **Tests**: fib's `go test -race -count=1 ./...` passed on macOS, with gofmt, `go vet` for
  linux/darwin/windows/freebsd and staticcheck for linux/darwin/windows clean; fib's Linux-only tests
  ran on the benchmark host (Linux 7.0.0-38-generic, 16 CPUs, `net.mptcp.enabled`=1, no Go there:
  cross-compiled test binaries over ssh): `TestMultipathTCPListener` (single loop, pollers,
  pollers+reuseport), `TestMultipathTCPDefaultIsPlainTCP`, `TestMultipathTCPAddBinding`,
  `TestMultipathTCPClaimedPort` and `TestBindingMultipathTCPServesAPlainClient`. fit's `go test -race`
  on the touched packages passed locally on macOS, the kernel-dependent tests skipping where MPTCP is
  unavailable, and the Linux cross-compiled binaries of the touched packages passed on the same host:
  `listener` (`TestListenerMPTCP`, `TestListenerReloadMPTCP`), `admin` (`TestAdminMPTCP`,
  `TestAdminConsoleMPTCP`), `app/console` (`TestConsoleMPTCP`), `streaming` (`TestStreamingMPTCP` and
  its `...Off` and `...Restart`), `webrtc/turn` (`TestTURNMPTCP` and its `...Off` and `...Restart`),
  `internal/nettcp` and `internal/mptcptest`; the workbench's `TestSchemaZh` passes.
- **Benchmark**: none; the change is off the hot path (a listener's socket protocol is chosen when it
  is opened, and no per-connection or per-request work is added).
- **Gaps**: the tunnel's and LLM feature's own link listeners (their hubs', nodes' and proxies', and
  the mesh) stay plain TCP, out of this change's scope; Multipath TCP is Linux-only, a peer or client
  without it silently falls back, and there is no config to require it; the workbench only gained an
  Engine-menu tag, with no browser run for it; the Admin API's own listener goes through
  `net.ListenConfig` rather than a fib binding, so its socket handling stays fit's; and the plain-TCP
  default is the CVE-2026-98432 hardening of `24dba02`: an MPTCP listener is what the published
  exploit of that kernel IPv4 TCP use-after-free needs on its target, so an operator who sets `mptcp`
  on an unpatched kernel takes that exposure.

## Logging: files, rotation and retention — done (I155–I157)

### I155 The file sink, the log's time zone, rotation and retention — done

- **Decision 034**: [the decision](decisions/034-log-files.md) records an in-house file sink
  (standard library only: `compress/gzip`, `time/tzdata`), the shape of `rotate`, naming in UTC,
  and `time_zone` local by default, UTC or an IANA zone. It builds on I82–I83 and on decision 027's
  workbench.
- **Package `logs`**: `Files`, a set of sinks by path, opens one `File` per path with a reference
  count, and `File` writes on a goroutine of its own: a queue of 1 MiB in pooled buffers, `Write`
  copying and enqueuing, refusing with `ErrQueueFull` and counting past it, so no logger waits for
  the disk. Rotation before a write that would pass `max_bytes`, or at the first write in a later
  period, whichever comes first; a period is a whole number of intervals from local midnight in the
  log's zone, so `24h` turns at midnight and `1h` on the hour. A rotated file is named
  `<path>.<yyyy-mm-ddThh-mm-ss.mmm>`, the rotation in UTC, `-N` when the name is taken and `.gz`
  when compressed; compression is gzip to a temporary file and a rename on a lazily made
  `internal/filepool` pool. Retention, at open, after each rotation and every ten minutes, removes
  the oldest rotated files past `max_age`, `max_files` and `max_total_bytes`: only names the sink
  makes from that path, never the open file. `SetRotate` takes new settings on a reload; `Info`,
  `List`, `Rotate` and `Cleanup` are the workbench's view and actions.
- **Config**: `config.Rotate` with pointer fields (`max_bytes` 1 GiB, `max_age` 720h,
  `max_total_bytes` 1 GiB and `compress` on; `interval` and `max_files` off, and 0 turning a limit
  off); `log.time_zone`, `log.rotate` and `loggers.<name>.rotate`; `LoadTimeZone`, with
  `time/tzdata` embedded so that an IANA name works in a container without zoneinfo; validation of
  an unknown zone, an interval under 1s or over 24h and not whole days, and one rotation policy per
  file across the log's loggers; `FileOutputs`, `RotateOf` and `TimeZoneName`.
- **`logs.Root`**: every format of every logger writes its lines in the log's zone, fib's handlers
  wrapped so that the record's time is converted; a file output is the shared sink, refused when
  opened with other settings; `SetConfig` changes levels, `time_zone`, rotation and compression in
  place on a reload, while a file's path or a logger's format still takes a restart; `Close`
  releases the sinks and reports a file that does not close; `Files()` hands the set to the access
  log and the workbench. `fit run` warns at startup for a file output with no `rotate` that it grows
  without bound.
- **Tests**: `logs/file_test.go` (rotation by size and by a fake clock's interval; retention by
  age, count and total size, deleting only the sink's names and never the open file; compression;
  a shared sink and a second policy refused; `periodStart` in local and UTC zones); `logs/zone_test.go`
  (a line's zone for `text`, and after `SetConfig`; an unknown zone; `SetConfig` changing rotation);
  `config/rotate_test.go` (the defaults and `Equal`, `local`/`utc`/IANA, the validation errors and
  the shared-file rules); `test/e2e/logfile_test.go` (fit run rotates after SIGHUPs, every file ends
  on a whole line and no line is missing). `go test -race ./logs/ ./config/` passes.
- **Differences from the plan**: the compression pool is made when the first file is compressed, so
  a log without `compress` starts no goroutines; `logs.New` keeps its signature and makes its own
  set, and `observe.NewAccessLogFiles` joins an existing one (I156); the zone is the zone of the
  lines too, not only of the rotation; `time/tzdata` adds about 450 KiB to the binary.
- **Benchmark**: the access log's formatter was compared on the benchmark host (AMD Ryzen 7
  7800X3D, 16 CPUs), a baseline test binary built from `cdc84b6` and this tree's, 10 alternating
  runs of `BenchmarkLog` at 500ms: JSON +5.5% (21.2 to 22.4 ns a line), text +1.9%, the `line`
  format unchanged. Part of the delta may be code and binary layout, not work: a control with the
  old formatter in this tree and one with the baseline's code in this tree moved by the same order,
  and the host was shared with another session's work while they ran (its MPTCP test binaries and
  a `llama-server`, load average 3–12). What a line adds is the zone's offset, a few bytes and no
  allocation; the per-second stamp cache stays and the zone conversion is once a second, and the
  enqueue is on the writer's goroutine. To re-run with `bench/microbench.sh` on an idle host and,
  if the JSON formatter's cost is confirmed there, to reduce it (rules 16–18 and 21).

### I156 The access log's file through the same sink — done

- **Config**: `access_log.time_zone` and `access_log.rotate`, the same `config.Rotate` and the same
  validation; `Config.Validate` refuses a path shared with the log's files that has other rotation
  settings or another `time_zone`.
- **`observe.AccessLog`**: `NewAccessLogFiles(c, files)` opens a file output through fit's set (a
  nil set makes its own), so the two logs share one sink a path; `Close` writes out its own buffers
  and then releases the sink; a batch the sink refuses is counted as the lines it holds (its
  newlines) in the log's dropped count; `write` still never waits. The per-second time stamp cache
  now holds the zone's wall time and offset: `json` and `text` write RFC 3339 with the zone's
  offset, `line` its wall clock, one conversion a second. `SetConfig` changes level, `time_zone` and
  rotation on a reload; `NewAccessLogTo` and `NewAccessLogFormatTo` keep UTC for tests and
  embedders.
- **`fit run`** hands `root.Files()` to the access log and warns for a file output with no
  `rotate`; `admin`'s apply calls `SetConfig`, not only `SetLevel`; `sameAccessLog` still compares
  only the restart-only fields, so the consistency check does not report the live ones.
- **Tests**: `observe/accesslog_sink_test.go` (lines in a configured zone and after a reload; a
  file rotated with flushed batches, every line whole and its count kept; the two logs' one sink);
  the existing `observe` tests unchanged; `fit run`'s e2e access log test passes. `go test -race
  ./observe/ ./logs/ ./config/ ./admin/` passes.
- **Differences from the plan**: the access log's rotation unit is the batch its writer hands over,
  not the line: a batch larger than `max_bytes` gets a file of its own (as a line larger than it
  does for fit's own log), and the file can pass `max_bytes` by at most one batch. `json` and
  `text` now write local time by default where they wrote UTC, as asked; decision 034 records it.
- **Benchmark**: as I155's. A batch a flush writes to the sink is copied once into a pooled buffer
  on the log's own goroutine; the formatter's per-line cost is the one measured there.

### I157 The workbench's Logs page and its Admin API — done

- **Admin API** (`admin/logs.go`): `GET /logs` answers a file output of fit's own log and of the
  access log: its path, loggers, format, effective `time_zone`, `rotate` with its defaults filled
  in, the current and rotated files with sizes and times, the total, the lines dropped and the last
  write error. `POST /logs/rotate` with `{"path"}` turns one output now; `POST /logs/cleanup` with
  `{"path", "dry_run"}` removes what retention says or reports it. Only a path of the running
  config's file outputs is accepted. `Configs.CurrentConfig` returns the config the running version
  loaded.
- **Workbench** (`gateway/logs.js`, `gateway/logs.css`): a first-level **Logs** module after
  Consistency, one card a file output with its loggers, format and zone badges, its rotation summary
  (or a warning that it grows without bound), the files with sizes and times, and **Rotate now** and
  **Clean up now**, the second previewing the files and asking for a confirmation that names them.
  Below, the forms of `log` and `access_log` are the shared draft's own sections (`config.js`'s
  `page`/`section`), so `time_zone`, `rotate` and the rest are edited with the schema's form, the
  `.conf`/`.yaml`/`.json` tabs and the same **Save and apply** bar as any page; nothing is applied
  without it. `workbench.js` ranks it and `index.html` loads it.
- **Tests**: `admin/logs_test.go` (the two outputs with their fields, a rotation now, a dry run that
  removes nothing and a cleanup that removes only the old rotated file, a path that is not a log
  file refused); `app/workbench`'s `TestPages` (every bundle file is loaded) and `TestSchemaZh`
  (the new descriptions' Chinese) pass; `node --check` on `logs.js`. There is no browser harness in
  the repository, so the page has no automated English and Chinese run: a gap to close.
- **Docs**: the schema, the reference and the templates are regenerated; the Log and Access log
  chapters describe `time_zone` and `rotate`, with their examples; the Chinese reference's Log and
  Access log rows, the console docs' workbench menu and pages, and decision 034 in both languages.

## Review follow-ups after v1.0.0

### I161 Unauthenticated connections and file names, after review — done

- **Admin API** (`admin/server.go`): a connection that has not sent the token is closed by a timer
  at accept + 5s, whatever it is doing; its write deadline is capped there too, not only its read
  deadline; dashboard files answered on it carry `Connection: close`. The review's client, 70
  pipelined dashboard requests read slowly, gets one answer and is closed.
- **Slots** (`login/slots.go`, used by the Admin API, the consoles and the pages): a quarter of
  `max_conns` is kept for authenticated connections; when the slots are full, a new connection
  closes the oldest unauthenticated connection of the client holding the most of them (loopback and
  Unix-socket clients count as one), and is refused only when every connection has authenticated.
  An authenticated connection is never closed for another.
- **fastcgi**: scripts are checked with `dotfile.Scan`; a backslash, NUL or `..` answers 400 on
  every OS, in the plain path and in `try_files`, and a `try_files` entry with one is refused when
  the config loads.
- **static**: whether a root folds case is decided once, when the handler is set up
  (`dotfile.FoldsCase`); on such a root a path not spelled as on disk (`dotfile.OnDisk`, which also
  catches Windows' 8.3 short names) or with a fastcgi `split_path` extension in any case
  (`dotfile.AddScripts`, `dotfile.Script`) answers 404. Both checks run on cache misses only, on the
  file pool, so the per-request path is unchanged.
- **Docs and converter**: the nginx migration docs (English and Chinese) map `location ~ \.php$`
  to `(?i)\.php$`, and the nginx converter writes such regexps with `(?i)` and a warning; the
  schema, reference, templates and `schema-zh.js` are regenerated.
- **Breaking**: a full Admin API or console closes an unauthenticated connection rather than refuse
  the new one; an unauthenticated dashboard connection is closed after each answer; fastcgi answers
  400 to a backslash on Linux too; on Windows and macOS `static` refuses case variants and script
  extensions.
- **Tests**: `make test-changed` under the race detector (77 packages); vet for linux, darwin and
  windows. Windows' name lookup is compiled and vetted, not run: CI has no Windows test of it.
- **Benchmark**: `BenchmarkServeCached` on the benchmark host, 10 alternating runs: 687.5 ns to
  692.2 ns (p=0.53), allocations unchanged. `bench/ab.sh` of fit `61b5a6c4`
  against `f294f290` (I161 with I158's fib and I159; 5 rounds, the harness's h2c port moved to 8091
  for the run, since the user's `qwen27b.service` holds 8081): `static` h2c +6.7% and `fixed` h2c
  +5.2%, faster 5 of 5 (I158's fib); `static`, `fixed` and `proxy` over HTTP/1.1 and `proxy` over h2c
  within noise.
- **Gaps**: a loopback client reconnecting at full speed can still race a brand-new loopback admin
  connection before it authenticates (an already authenticated one is never affected); a per-client
  accept rate would close it. The decisions made while building it are in the plan's I161 section,
  pending review.


### I164 Built-in transforms — done

- **Transforms** (`sync/transform*.go`, modules of `sync.transforms`): `filter` (namespace globs
  to include and exclude, operations, and fields' values by `equals`, `in` or `present`, `action`
  `keep` or `drop`); `route` (an exact `map`, then glob `rules` with `{namespace}`, `{first}`,
  `{rest}` and `{last}` templates, `unmatched` `keep` or `drop`); `fields` (`unnest`, `nest`,
  `rename`, `drop` or `keep`, `set` in the canonical JSON form, on before and after, renames on the
  key too); `key` (`map`, `value` or `join` of fields; a delete lacking them keeps its key); `cast`
  (to string, int, uint, float, decimal, bool, bytes, time, date or json, by a defined table, on the
  rows and the key); `mask` (HMAC-SHA256 with the key from `key_env`, SHA-256, or null); `envelope`
  (`flat` with metadata fields, `pair`, `debezium`; deletes kept, dropped or rewritten to updates).
- **One copy per chain**: `Record.Own` and a flag `Clone` sets and `Emit` clears; the dispatcher's
  `emit` closure is made once instead of per record and transform. A `key` after a transform that
  removes one of its fields is refused when the config is checked (`checkChain` in `load`).
- **Docs**: the schema, the reference's part *Sync transforms* (a section per module, and the text
  in `schema/doc/sync_transforms.md`), the templates, and the reference's Chinese index.
- **Tests**: table tests over every value kind and every operation for each transform (text forms,
  13 kinds by 10 cast targets, hashes against HMAC and SHA-256 computed apart, the three envelope
  shapes), params refused, the chain check, a chain of all seven copying a record of each
  operation exactly once and leaving the source's record unchanged, `AllocsPerRun` of a chain of
  four record-level transforms at exactly one allocation, and a pipeline from `generate` through
  five transforms to `memory`. `sync` at 94% under the race detector. `BenchmarkChain`: one update
  through all seven in about 2.9 µs and 20 allocations on the local machine (not the benchmark
  host; a reference only).
- **Benchmark**: none on the host, by rule 21: sync is off the request path.
- **Differs from the plan**: the transforms are files of package `fitsync`, not a package
  `sync/transform`; about 1,170 lines of code and 730 of tests, past the plan's size, and not split.
- **Gaps**: the decisions in the plan's I164 section, pending review. `go test ./docs/` fails on
  `origin/main` as it is, on links of `docs/tutorial/README*.md` to chapters not yet committed,
  unrelated to this change.

### I163 Decision 036; records, pipelines and checkpoints — done

- **Decision 036** (`docs/decisions/036-sync.md` and its twin): the track's principles, the record
  model, its canonical JSON, at least once with idempotent sinks, protocols written in fit, the
  watermark, the queue's bound, the checkpoint file, failures, the role; the ten planning decisions
  and those made while building it, pending review.
- **Package `sync`** (name `fitsync`): `Record` (source, namespace, op, key, before, after, commit
  time, `Tx` with ID, first and last, an opaque JSON `Position`) and `Value` (null, bool, int, uint,
  float, decimal as text, string, bytes, time with its offset, date, JSON, array, ordered map), with
  `AppendJSON`/`UnmarshalJSON` in a canonical form that says each kind JSON alone would not, read
  token by token so a map keeps its order and a number its digits. `Source` (`Run` with `From`,
  `Once` and a blocking `Emit`), `Confirmer`, `Sink` (`Write` of a batch, `BatchError` naming the
  record that failed), `Transform` (`Apply(r, emit)`).
- **Runtime**: per pipeline a supervisor, the source, a dispatcher running the transforms, one
  goroutine per sink, a checkpoint ticker and one waiting for the sinks; none on fib's workers. A
  record counts against `queue.max_records`/`max_bytes` from `Emit` until every sink has
  acknowledged all it became and the watermark passes it, so a stalled sink stops the source. Each
  sink batches by `max_records`, `max_bytes` and `max_wait`, at once when the source has ended.
  Acknowledgements move the watermark over contiguous done records to the last that ends a
  transaction. The checkpoint, `<state_dir>/<pipeline>.json` (`version` 1), is written to a temporary
  file, synced, renamed, and the directory synced, at most every `checkpoint_interval` and at stop;
  a `Confirmer` source is told after the file is written. Writes are retried with a doubling backoff
  from the failed record, then `on_error` (`stop`, `skip`, `dead_letter` to a sink of its own); a
  source's error restarts the pipeline from its checkpoint. `Apply` diffs pipelines by their settings
  and `state_dir`, loading new modules first so a bad config changes nothing; `Close` stops all,
  each writing what it holds within its deadline. `Options.Crash` kills a pipeline at six named
  points as `kill -9` would.
- **Config**: `config.Sync` (`state_dir` required, `pipelines` with `source`, `transforms`, `sinks`
  of `name`/`sink`/`batch`, `mode`, `on_error`, `dead_letter`, `checkpoint_interval`, `queue`,
  `retry`), checked for names, references and backoffs; modules in `sync.sources`, `sync.sinks`,
  `sync.transforms`, loaded by the role's `Check` when the config compiles, so `fit validate` checks
  their params. `sync` is an app role (`app/standard`), with a logger of its own, `sync`; a config
  of `sync` alone needs no listener. The schema, the reference (a chapter `Sync` and parts for the
  three namespaces), the templates and `docs/configuration.md` describe it; the template writer
  takes module references of any namespace.
- **Test connectors**: `generate` (records of every value type, the same each time, transactions of
  `tx_size`, an end after `records`, `interval`), `memory` (named stores, bounded, a hook for tests)
  and `stdout` (canonical JSON lines).
- **Tests**: `sync` at 93% under the race detector: every kind and a record round trip and refused
  forms; two sinks with one held back, the checkpoint at most what the slower acknowledged and on a
  transaction's end; a sink failing mid-batch and whole, every record once; 24 random kills across
  the six points with random transaction, queue and batch sizes, then a run to the end: nothing
  lost, nothing before the saved checkpoint written twice, every checkpoint on a transaction's end,
  the second run starting at it; a stalled sink holding the source at `max_records` and at
  `max_bytes`, and `Close` within its deadline; `once` and a restart after it; `skip`,
  `dead_letter`, a dead letter sink failing, `stop`; transforms dropping, splitting and failing;
  a source failing and restarting; reloads restarting only the changed pipeline, removing, refusing
  a bad module; checkpoint files of other versions or broken, and temporary files left; the role
  through the app manager. `config` and `cli` (`fit validate`) tests of the section. `go vet` for
  linux and windows.
- **Benchmark**: none, by rule 21: sync is off the request path, and nothing on it changed.
- **Differs from the plan**: the sink's module is a reference of its own (`"sink": {"module",
  "params"}`), byte sizes are integers, `dead_letter` is a sink of its own; package `module` needed
  no change. No metrics or Admin API yet (I188, I189). Larger than the plan's size: about 2,400
  lines of code and docs comments besides 950 of tests, since the runtime's parts are only tested
  together; nothing was left over to split into an I163b.
- **Gaps**: the decisions in decision 036 and the plan's I163 section, pending review.

### I162 One address, one server — done

- **Validation** (`config/bindings.go`): `checkBinds` collects every address the config has fit
  listen on, with its transport (listeners, those HTTPS by default makes, `admin`, `admin.console`,
  the LLM roles' consoles, links listeners and proxy API, the tunnel roles' consoles, the hub's
  listeners (QUIC on UDP) and console socket, the proxy's forwards and web proxies, streaming's RTMP,
  RTSP, SRT and WebRTC ports, TURN's `listen`, `listen_tcp` and `listen_tls`, `webrtc.media`,
  `webrtc.proxy`), normalizes the hosts (an empty host, `0.0.0.0`, `::` as every address;
  `localhost` as `127.0.0.1`; IPs canonical, names in lower case) and refuses any two on one
  transport and port whose hosts are the same or one is every address, at the later, naming the
  earlier: `listener "web" listens on TCP :8080 already, the same address as 0.0.0.0:8080; two
  servers on one address would share its connections, so give one of them another port`. Port 0
  never clashes, nor TCP with UDP. The per-role duplicate checks it replaces are gone.
- **At bind time** (`internal/bindcheck`): `Free(network, address)` binds without SO_REUSEPORT and
  closes, and fails with `TCP <addr> is taken: another process, or another part of fit, listens
  there`. `listener`, `streaming`, `webrtc/turn` and `webrtc/media` had their own copies, now this
  one; the consoles (`app/console`: admin and every role's), the LLM proxy's API and the mesh's links
  listener, the tunnel hub's listeners (TCP, TLS, WS, WSS through `transport.ListenFib`; QUIC) and
  console socket, and the tunnel proxy's forwards and web proxies check now too.
- **fib A70** (`856ad5b`): `turnserver.bindRelay`, with `port_min`/`port_max`, could bind a port
  another relay held, two allocations then sharing a relayed address; a port is now reserved in the
  server's set and claimed with a plain bind first. fit moved to `856ad5b`.
- **Tests**: `config/bindings_test.go` crosses every kind with every other (both orders, one error
  naming the earlier, none across TCP and UDP), the normalized forms and their non-clashes, the
  defaults of `admin` and `admin.console`, and `Load` with HTTPS by default's listeners; a test per
  role (`app/console`, `llm/proxy`, `llm/internal/mesh`, `tunnel/proxy`, `tunnel/hub`) binds an
  address another fib engine holds with SO_REUSEPORT (`internal/bindtest`) and is refused, each
  failing without the check; `llm/internal/role`'s test of a kept certificate stopped the role it
  restarted, which it had left listening on the address. fib: `TestRelayPortsNotShared` and
  `TestRelayPortsConcurrent`, under the race detector; vet for linux, darwin, windows and freebsd
  and staticcheck for linux, darwin and windows on `webrtc/turn`.
- **Benchmark**: none, by rule 21: the config check is the control plane, and the bind-time check
  runs once a binding, off the request path; the engines' config is unchanged.
- **Breaking**: configs with two parts on one address, or a wildcard and a specific address on one
  port, are refused at load, where before some listened silently beside each other; the duplicate
  messages changed.
- **Gaps**: the decisions in the plan's I162 section, pending review; a socket that takes an
  address between the check and the bind, or one of the same user that sets SO_REUSEPORT itself
  after, is not caught, as with fib's own claim.


### I158 The cost of fib v1.7.1's HTTP/2 request checks — done

- **Profiles** (`BenchmarkH2ServerGET` and `BenchmarkH2ServerStatic/1KB` on the benchmark host):
  `dabe569`'s pseudo-header checks were about 0.3% of CPU; the larger cost a field, older than
  v1.7.1, was two map lookups for each regular request field (hop-by-hop and canonical key), and
  `textproto.CanonicalMIMEHeaderKey` on each response key. `46af102`'s body count costs nothing on a GET.
- **fib `cb9bbbe`** (on `e02d9a9`, `089a5b0`, `7bac3a4`): each pseudo-header is checked as
  `newRequest` takes it, one byte-class table (`h2ByteClass`) for every byte, `http` and `https`
  without a scan; a path the fast parser accepts is not scanned again; a regular field's canonical
  key, validity and hop-by-hop flag come from one lookup in a table of known names; a request that
  ends with its HEADERS frame counts no body; a known response key is checked with the same lookup;
  a connection checks its `:authority` again only when it changes. Every check is kept, and new
  tests hold the tables to them.
- **Micro-benchmarks** (15 alternating rounds, benchstat, against `5c0e5b9`): GET +0.46%
  (p=0.010; main was +1.60%), Static/1KB −0.18% (main +0.99%), 16KB and 64KB within noise; geomean
  +0.13% (main +0.87%); allocations unchanged. GET's small rest is on the response side, where no
  source changed; code layout is the guess, not proven.
- **fit A/B** (`bench/ab.sh -scenarios fixed,static -protocols h2c`, 5 rounds): fib `5c0e5b9` to
  `cb9bbbe`: `fixed` +2.8%, `static` +5.1%, faster 5 of 5; `5c0e5b9` to fib's old main −1.3% both,
  slower 5 of 5, so the cost was reproduced and is gone.
- **Tests**: `go test -race ./http` at `cb9bbbe`, `TestH2NewRequestRejectsMalformedPseudoHeaders`
  and the buffered-body tests included; vet on linux, windows and freebsd. fit's `go.mod` names
  `v1.7.3-0.20261105195654-cb9bbbe30e24`.
- **Notes**: `bench/ab.sh` takes no lock of its own; a session running it beside another that holds
  `/root/bench.lock` for its whole run deadlocked once. The decisions made are in the plan's I158
  section, pending review.

### I159 Issued tokens for the tunnel and the LLM mesh — done

- **`internal/linktoken`** (decision 035): a token is `fitt1.<16 hex id>.<32-byte secret,
  base64url>`; the listener keeps only a hash that cannot link; both sides add a nonce, each proof
  binds both nonces and its direction, and the listener proves the token back; an expired token's
  links close at its expiry (checked at least once a minute), a revoked one's at once; a member is
  bound to the first token it links with, in the token's `members`; issuing, listing, changing an
  expiry and revoking are config edits logged to `admin`.
- **Config**: `tokens` on `tunnel.hub`, `llm.hub`, `llm.node` and `llm.proxy`; `token` on a tunnel
  hub entry and an LLM peer, refused beside `password`; an LLM listener no longer needs a password.
- **Tunnel**: a `challenge` call before registration carries the token's ID and the node's nonce,
  and the hub's answer proves the token; with `body_password` the hello is version 4, names the
  token's ID, and its keys come from the token by HKDF, not PBKDF2. The shared password still
  registers, and the hub's status lists who uses it (`password_peers`).
- **LLM mesh**: the dialer answers the challenge with the token's ID, its nonce and a proof, and
  the listener's ack carries its own; any role links with a token, and the peer's ID is bound at
  hello; each inbound link shows `token_id` or `shared_password`.
- **Management**: Admin API `GET /tokens`, `GET`/`POST /tokens/{role}`, `PATCH`/`DELETE
  /tokens/{role}/{id}` (also under `/api/m/admin/` on the console); `fit token list|create|expiry|revoke`;
  the tunnel hub's Tokens page (issued once with a copy button, list, expiry, revoke, a warning
  while a member uses the password, a setting that removes it), the LLM links page's tokens card,
  credential column and "remove every password", and a token field where a hub or peer is added.
- **Upgrade**: hubs and listeners first, as a member with a token cannot link to an older one (a
  tunnel node with a token and `body_password` gets only "link closed" from an old hub). The shared
  password is accepted in this release and is to be refused in the next.
- **Tests**: `make test-changed` under the race detector (75 packages); the tunnel end to end over
  tls, wss, quic and sealed tcp with tokens, a token of any role, a member not taken over, unknown,
  forged and replayed proofs refused, expiry and revocation closing links; the LLM mesh over TLS
  with tokens issued, listed, re-expired and revoked from the pages; the Admin API and the CLI. By
  hand in a browser in English and Chinese; no token reached the log.
- **Gaps**: no automated browser test; `test/docker/tunnel` and fit-examples still use passwords,
  which work this release; refusing the password is the next release's. The decisions made are in
  the plan's I159 section, pending review.


### I160 The tunnel's and the LLM mesh's TLS, after review — done

- **Certificate options apart from the address**: `transport.Dial` takes `ClientOptions` (pin, CAs,
  insecure) beside the address and reads none from it; addresses are parsed with `net/url`, and one
  with a user, a query or a fragment is refused by every dial, by `config.CheckTunnelEndpoint` and
  by the pages' API (400). `uplink.Dials` passes addresses unchanged and `uplink.CheckBy` gives each
  its options; `ClientQuery`, `ParseClientQuery` and `quic.DialURL` are gone. The review's `#?pin=`,
  `#?insecure=1`, `#?ca=` and `#?x=` on tls, wss and quic fail validation and the pages' API, and
  given to Dial directly are refused before a connection, the configured pin still refusing the
  other certificate.
- **`quic_insecure`** with a quic endpoint off loopback is refused unless `body_password` (or a pin
  or `ca_file`) is set, with an error that names the endpoint.
- **Self-signed certificates kept without a store**: a hub, and an LLM `self_signed` listener, keep
  theirs in a 0600 file beside the config (`fit.tunnel.hub.pem` beside `fit.json`;
  `certpin.Keep`, `app.Manager.KeepBeside`, `app.Host.KeptFile`), so peers' pins survive a restart.
- **Rotated certificate files**: an LLM listener re-reads `cert_file` and `key_file` and swaps the
  certificate in place; roles list the files they read (`app.Info.CertFiles`) and `runtime.Compile`
  adds them to `-watch-certs`, so the tunnel hub's and the LLM listeners' files are watched.
- **Breaking**: an endpoint address with `?`, `#` or `@` no longer loads, and a tcp endpoint's port
  must be a number; `quic_insecure` off loopback without `body_password`, a pin or `ca_file` no longer
  loads; the first start of this release on a hub without a store makes one last new certificate.
- **Tests**: the race detector on the 79 packages it reaches; vet natively and for Windows; the
  attacks above, a hub restarted with the same fingerprint, an LLM listener taking a rotated
  certificate with its open links kept.
- **Gaps**: the llm proxy's `api.cert_file` and the roles' console `cert_file` are not watched. The
  decisions made are in the plan's I160 section, pending review.


## AI assistant in the workbench

### I124 The assistant's settings and model client — done

- **Decision 031** (English and Chinese). **Config**: `admin.assistant` (`config/assistant.go`):
  providers of eight kinds, `default_model`, `params`, `limits` (every limit of the plan's table),
  `data`, `users` with their levels and `default_level`; it needs `admin.console`.
- **Store**: named sections beside the config (`Store.Section`, `Store.SetSection`); the settings and
  keys are kept in section `assistant`; a store is written as version 3 only while it holds a
  section, versions 1 and 2 are still read, and sections are never exported.
- **`assistant/model`**: one client for anthropic, openai, openai_responses, gemini, azure_openai,
  ollama, openai_compatible and llm_proxy, on fib's asynchronous HTTP client: each kind's request
  from the system prompt, messages and tools, its streamed answer (SSE, or NDJSON for Ollama) parsed
  into text, thinking, tool calls and usage; a line 1 MiB, an answer 8 MiB by default, 64 tool calls,
  8 KiB of an error body; connect, first-byte, silence and whole-call timeouts; retries only before
  an answer; a cancel closes the call's connection. Keys print as `***`, are replaced in every error,
  and go only over https or to loopback.
- **Console module `ai`** (served only with `admin.assistant`): `GET`/`PUT /api/m/ai/settings` (409
  without a store, 412 to a stale page), `POST /api/m/ai/providers/test` (4 at once, cancelled when
  the browser leaves). Disk and certificate work runs on the console's pool; model calls answer
  later, so no fib worker waits.
- **Workbench** (`ai/settings.js`): the Settings page in English and Chinese: providers with their
  key's state, edited in a dialog with a write-only key; a test (model list, then a short call, each
  timed); "Read the provider's list"; the default model, limits, data and each user's level.
- **Tests**: `make test-changed` under the race detector (77 packages); `assistant` 100%,
  `assistant/model` 99.4%, `config/assistant.go` 100% covered; every kind round-trips a tool call
  against a recorded fake (`assistant/internal/fakeapi`); slow, cut, oversized and too-many-tool-call
  streams fail within their bounds; keys stay out of answers and logs. `test/docker/assistant`
  against Ollama 0.12.6 with `qwen2.5:1.5b`, run on the benchmark host (Docker Hub did not answer
  from the dev machine): a tool call round-trips as kind `ollama` and as `openai_compatible`, the
  model lists and a cancelled call pass. The page was checked in a browser against `mockllm`, in
  English and Chinese, the key never in the page or the log.
- **Breaking**: a store that holds the assistant's settings is version 3, which older fits cannot
  open; stores without sections are unchanged.
- **Gaps**: reasoning is not carried between turns for OpenAI Responses' encrypted reasoning items or
  Anthropic's redacted thinking; no way back to the config file's settings once the store holds
  them. The decisions made are in the plan's I124 section, pending review.


### I125 The chat panel and the read tools — done

- **The loop**: `POST /api/m/ai/chat` streams a chat as SSE (start, text, thinking, tool call, tool
  result, ask, usage, summary, done): fit calls the model, runs the tools it asks for and calls it
  again, until an answer, an `ask_user` question or a limit. Every `limits` setting is enforced (tool
  calls, turns, concurrent chats, tokens a day, tool result and answer bytes, conversations). Each
  call carries the rules, the language, the open page and its selection, the skill catalog (empty
  until I127) and the history; past the model's window, early tool results are cut, then early turns
  summarized by the model and the summary kept. The user's words go as typed.
- **Read tools** (`assistant.Registry`): `config_get`, `schema_describe`, `config_validate`,
  `versions_diff`, `status_get`, `metrics_query`, `upstream_health`, `consistency_check`,
  `certs_list`, `cache_zones`, `log_tail`, `access_log_sample`, `adapt_convert` and `ask_user`, on
  the console's pool against the Admin API in process, 30s each; every result redacted, cut to its
  bound with a note, and fenced as data. fit's own log keeps its last 1,000 messages for `log_tail`
  (`logs.Root.Recent`).
- **Conversations**: per user in the encrypted store, a section each (`assistant.chats.<hash>`),
  written by one saver; `GET /api/m/ai/conversations`, `GET`/`DELETE /api/m/ai/conversations/{id}`,
  `POST /api/m/ai/chat/stop`, `GET /api/m/ai/tools`.
- **Workbench**: a chat button in every page's top bar (hidden for level `off`); the panel renders
  Markdown as DOM nodes as it streams, folds tool calls, shows `ask_user` as buttons, and has Stop,
  Retry and pick, new and delete conversation; the Functions page; English and Chinese.
- **Tests**: `make test-changed` (83 packages) under the race detector; `assistant` at 98.0%; a
  scripted fake model and the real Admin API and log ring; in a browser: questions answered from
  tools in English and Chinese, the `ask_user` choice reaching the model, Stop, Retry, conversations
  across a restart, and no conversation text in the store file in the clear.
- **Gaps**: `llm_nodes` and `tunnel_nodes` are not built, as those roles have no Go API for their
  node lists; `metrics_query` reads current counters only, as fit keeps no metric history. The
  decisions made are in the plan's I125 section, pending review.


### I126 Proposals, levels and the audit log — done

- **Propose tools** (level `propose`): `config_propose` (whole top-level members, or add, replace and
  remove at JSON pointers), `reconcile_propose`, `rollback_propose` and `cache_purge_propose`; each
  call adds a step to the conversation's one pending proposal, kept with the conversation: the
  steps, the base ETag and version, a revision, and the whole checked again on the running config,
  with a redacted diff of the changed members in `.conf`, `.yaml` and `.json`. A step wrong in itself
  is refused; one that leaves the whole invalid is kept, and the proposal cannot be applied until
  another mends it.
- **Apply** (level `apply` only; the panel's Apply, then a dialog with the whole diff): as the user,
  through the Admin API in process, with `If-Match` and the revision the person saw: patches as one
  `PUT /config/text?members=`, fixes as one `POST /config/reconcile`, or `POST /rollback/{n}`; saved,
  listed in `/versions` as `console`; purges after. All or nothing for the config; a stale proposal
  is refused (412) and offered again on the new version; a refused change leaves the proposal with
  its reason, which the model is told.
- **Levels** are checked on every tool call against the settings in use, and on every apply; `read`
  and `edit` users get no propose tools.
- **Audit log** (store section `assistant.audit`, bounded by `limits.max_audit_entries`): answers'
  tool calls (redacted, cut), applies, refusals, offers again and discards, with user, time, source,
  conversation, skill, owner, steps, outcome and version; `GET /api/m/ai/audit`; `GET
  /api/m/ai/proposals`, `POST .../{conv}/apply` and `.../{conv}/discard`.
- **Workbench**: the panel's proposal card and confirm dialog, the Proposals and Audit log pages,
  English and Chinese.
- **Tests**: `make test-changed` under the race detector; `assistant` at 98.6%, the new files 100%;
  an admin test applies a two-step proposal as one version that `/versions` lists, saved; in a
  browser against a scripted model, English and Chinese: cancel leaves the config, confirm applies,
  a stale proposal is offered again, a `propose` user cannot apply.
- **Gaps**: no purge by prefix (the Admin API has none); config steps of different kinds are refused
  in one proposal; server-side error details are English only. The decisions made are in the plan's
  I126 section, pending review.


### I127 Skills — done

- **The format** (`assistant/skills.go`): Markdown with YAML front matter (name, title, title_zh,
  description, description_zh, inputs, tools, model, examples), then the prompt; at most 32 KiB, 32
  tools, 8 inputs and 8 examples; bad front matter is refused with its line.
- **Built-in skills**: the plan's 14 (`explain` to `tunnel-setup`), embedded from
  `assistant/skills/*.md` with Chinese titles, descriptions and examples; a test holds them to the
  registry's tools.
- **User skills** (`assistant/skillstore.go`): at most 64, kept in the store's `assistant.skills`
  with the built-ins disabled, read without a lock; `GET`/`POST /api/m/ai/skills`,
  `GET`/`PUT`/`DELETE /api/m/ai/skills/{name}`, `.../enable` and `.../disable`; changed by level
  `apply`.
- **In the chat**: `/name`, the panel's picker, the chat body's `skill` and `inputs`, or the model's
  `skill_load` make a skill the conversation's: its prompt replaces the catalog in the system prompt,
  the tools narrow to the skill's within the user's level plus `ask_user`, and its model is used; the
  `start` and `skill` events and the audit log name it. Without one, the prompt carries the catalog
  only.
- **Workbench**: the Skills page (list, view, add, edit, enable, disable, import, export, Try), the
  panel's skill chip and picker, and `FitAI.useSkill(name, {message, inputs})` for I128.
- **Tests**: `make test-changed` under the race detector; `assistant` at 98.6%; each built-in runs on
  a fixed config against the recorded model; the catalog, `skill_load` and the narrowed tools; a
  `read` user's `/route-builder` cannot propose; in a browser, English and Chinese: bad front matter
  refused with its line, a skill added on the page surviving a restart.
- **Breaking** (inside the package): `Options.Catalog` is gone, the catalog comes from the skills.
- **Gaps**: a user skill that no longer parses after an upgrade is left out and logged; no "export
  all". The decisions made are in the plan's I127 section, pending review.


### I154 Edits on the page, submitted by a person — done

- **Page tools** (`assistant/pagetools.go`, a new tool kind `page`): `page_get` (level `read`),
  `page_edit` and `page_revert` (level `edit`). A call goes to the panel as the stream event
  `page_call` and comes back with `POST /api/m/ai/chat/page` as the call's result, redacted, bounded
  and audited; the wait is at most 30s and ends when the browser leaves. They are offered only while
  the page shown declares an editor (`editable`), and never over MCP.
- **Checks**: fit checks a draft on the console's pool before it reaches the page (the page's members
  only, no `admin`, no secret written, the text parses, at most 256 KiB); a refused draft is told to
  the model, which proposes it (`config_propose`) if the user's level allows, or describes it.
- **Workbench**: config editor pages register through `WB.editor`; a draft is written into the
  shared draft as a person's edit would be, its fields marked, a note with Revert and an "Assistant
  draft" badge beside "Unsaved changes", `[redacted]` values put back from the draft. No config API
  call, version, ETag or audit change happens until the person's **Save and apply**. An untouched raw
  tab now follows the form's changes.
- **Skills**: route-builder, reverse-proxy, rate-limit-auth, tls-acme and cache-tuning name the page
  tools, explain names `page_get`. Docs: "Drafts on the page" in the console docs and decision 031,
  English and Chinese.
- **Tests**: `make test-changed` under the race detector; `assistant` at 98.7%; in a browser against
  a scripted model: a draft asked in Chinese lands in the Routes form, marked, with the ETag and
  version unchanged and no config request sent; the password hash never reached the model; a revert;
  a YAML text draft saved by the person as version 2; refusals of a member not on the page and of a
  secret.
- **Gaps**: two defensive branches are not covered; elements of an array without names are matched
  by position when `[redacted]` values are put back (I126's guard does the same); a map entry's field
  marks its container. The decisions made are in the plan's I154 section, pending review.


### I129 Migration and review — done

- **`migrate`** (`assistant/skills/migrate.md`): nginx and Caddyfile text go through `adapt_convert`
  first and the model converts what the converter left; Traefik YAML and Docker labels are converted
  by the model by a mapping in the prompt. The result is merged into the running config (listeners,
  routes and upstreams kept, a listener on the same address reused, clashing names renamed, no
  `admin` and no secret written) as one proposal; `config_propose` takes `unconverted`, shown with the
  diff in the panel and on the Proposals page.
- **`security-review`**: findings one a line, severity, member and fix, the most severe first, over a
  checklist (the console reachable from everywhere, plain HTTP without a redirect, certificates near
  expiry, optional client certificates, routes to internal or metadata addresses, admin routes
  without auth, missing body and rate limits, `trusted_proxies` trusting everyone, listings and
  dotfiles, long timeouts, inline secrets); it proposes the fixes it may.
- **The evaluation** (`assistant/testdata/eval`): five migrations (two nginx, a Caddyfile, Traefik YAML
  and labels), each checked by routing its requests through the compiled config after its proposal
  is applied, and the items it must name as unconverted; two configs with 12 planted problems. CI
  runs them against recorded answers (5/5, 2/2); `make assistant-eval` against a model set by
  `FIT_EVAL_*`, never in CI; `FIT_EVAL_RECORD=1` records a live run.
- **Fixed on the way**: `config_get` of an unset member failed the call (now `null`); rate_limit's
  `key: ip` was taken for a secret; the nginx converter failed on OpenResty Lua blocks (now skipped and
  reported) and served `auth_basic` locations to anyone (now 401 until a `basic_auth` replaces it).
- **Tests**: `make test-changed` under the race detector; `assistant` at 98.8%, `adapter/nginx` at
  99.8%; the evaluator's own tests fail what they should.
- **Gaps**: no run against a real model yet (the recorded answers are written in a model's shape; a
  live run with `FIT_EVAL_RECORD=1` replaces them); no CLI command. The decisions made are in the
  plan's I129 section, pending review.

### I130 An MCP server on the tool registry — done

- **`admin.assistant.mcp`** (off by default) serves `/mcp` on the console's address over Streamable
  HTTP, for the protocol's revision 2026-07-28 (stateless, headers checked against the body,
  `server/discover`) and the revisions 2025-03-26 to 2025-11-25 (sessions opened with `initialize`);
  `tools/call` answers as SSE and a cut stream cancels the call (package `assistant/mcp`,
  `assistant/external.go`, `console.Options.Endpoints`).
- **What it serves**: the registry's tools that run in fit (not `ask_user` nor the page tools), the
  enabled skills as prompts, and the config, status, versions, schema and config reference as
  resources, redacted and bounded as for the model.
- **Tokens**: made on the new Assistant → MCP page, shown once, kept as SHA-256 hashes in the store's
  `assistant.mcp`, revoked there; a token's level is its own (`read` or `propose`) within its user's,
  checked on every request. A propose call adds to the token's one pending proposal, in its user's
  conversation `MCP: <token name>`, applied only in the workbench. Each token is bounded (`allow`,
  loopback by default, and its own addresses; sessions; request bytes; requests at once and a
  minute; 30s a call); every call is in the audit log with source `mcp` and the token's name.
- **Tests**: `make test-changed` under the race detector (83 packages); `test/mcpsdk`, a test-only
  module, drives fit with the official Go MCP SDK v1.8.0 in both eras, in a CI job of its own; the
  page tools are proven absent; the MCP page checked in a browser in English and Chinese.
- **Gaps**: Claude Code was not tried by hand (`claude mcp add --transport http fit
  http://127.0.0.1:2020/mcp --header "Authorization: Bearer <token>"`); the reference resources are
  English only; no change notifications. The decisions made are in the plan's I130 section, pending
  review.


### I128 Ask AI where the work is — done

- **Ask AI** (`app/workbench/web/ai/ask.js`): `FitAI.askButton(spec)` and `FitAI.ask(spec)` open the
  panel on a new conversation with a skill, its inputs, the thing asked about as the page's
  selection, and a question in the user's language, sent at once (`useSkill`'s new `send`); a
  disabled skill is left out and said so; no button without the module `ai` or for level `off`.
- **Entries**: `explain` on a save's problems, a failed save and a raw tab that does not parse (input
  `error`, `path` when one member; the numbered lines around each problem, secrets redacted, or the
  draft's values at the paths) and on every form field (input `path`, its schema path);
  `drift-explain` on each Consistency difference (input `difference`); `diagnose` on each Engine port
  and route entry, each route of Routes, an upstream with backends down (Overview, Upstreams), an
  Overview error-counter spike between refreshes, and the LLM and tunnel link maps. Menu nodes and
  entries take `ask` (`core/workbench.js`).
- **Docs**: "Ask AI on the pages" in the console docs, English and Chinese.
- **Tests**: `make test-changed` under the race detector; `TestAskSkills` holds every entry to a
  built-in skill and its inputs; in a browser against a scripted model, English and Chinese: each
  entry's first answer used its skill, inputs and selection; no buttons for a user at `off` or
  without `admin.assistant`.
- **Gaps**: no drift row was clicked in the browser (the same path as info rows); `schema_describe`
  cannot resolve module params' paths; the link maps rely on the snapshot sent, as `llm_nodes` and
  `tunnel_nodes` do not exist; spikes are error counters only. The decisions made are in the plan's
  I128 section, pending review.


## Media library: video on demand and channels

### I131 Decision 032; an MP4 reader in fib — done

- **Decision 032** (English and Chinese) records the media library track: its principles, what
  I131–I139 decide and what was rejected.
- **fib A64**: the new package `media/mp4` (fib `8998d58`, with `ba47477` exporting
  `fmp4.ParseSampleEntry`) reads MP4 files that are not fragmented through an `io.ReaderAt`: `Open`
  finds the `moov` at the front or the back and reads only the tables' places and counts;
  `Track.TableBytes` gives a table's memory before `ReadTable` reads it in one pass through a 16 KiB
  buffer, as a compact table of about 8.4 bytes a sample with B-frames; samples are found by index,
  time and keyframe, and edit lists applied as ffmpeg applies them. fit is on that commit
  (`v1.7.3-0.20261106054245-8998d58120f6`).
- **Tests**: every sample of seven files made by ffmpeg 7.1 (H.264, H.265 and AV1 with AAC and Opus;
  `moov` at the front and the back; B-frames, negative composition offsets, audio starting late)
  matches ffprobe with and without edit lists, and each track's codec config too; synthetic files of
  every table form; an hour at 60 fps (216,000 samples) read within its bound; `FuzzOpen` in fib CI's
  fuzz job; `TestMP4` in `media/interop` on longer files; `media/mp4` at 95.5%.
- **Gaps**: only the first sample description is used; `cmov` and encrypted files are not read;
  edits past the first playing one are kept, not applied. The decisions made are in the plan's I131
  section, pending review.


### I132 Media libraries — done

- **Config**: `media_library` (`config/medialibrary.go`): named libraries of `roots`, with `files` and
  `exclude` patterns, `follow_symlinks`, `scan_interval` and every bound with a default; run by the
  role `media_library` (package `medialib`), per engine, changed in place on a reload.
- **Scans** run on the library's own goroutine (at start, on the interval, on a reload's change and on
  `POST /media/library/<name>/rescan`), read directories 256 entries at a time within `max_files` and
  32 levels, and probe media files with fib's `media/mp4` on the library's bounded file pool
  (`file_workers`, `file_queue`, `file_timeout`), `max_probes` at once, reusing unchanged files; the
  new catalog replaces the old one only when whole.
- **Items** group renditions, audio, subtitles (`.vtt`, and `.srt` served as WebVTT) and posters by name
  or by a strict sidecar `<name>.json`; a file that fails to probe is listed with its error and not
  served; sidecar paths and symbolic links out of the roots are refused unless `follow_symlinks`; the
  catalog is bounded by `max_items` and `max_catalog_bytes`.
- **For I133 and I134**: `Open`, `OpenFile` and `Submit` on the pool (`ErrChanged`, `ErrOutside`),
  `Table` with a sample-table cache bounded by `max_table_bytes`, and `Subtitle`.
- **Admin API**: `GET /media/library`, `/media/library/{name}` (paged and filtered),
  `/media/library/{name}/items/{id}` and `POST .../rescan`; seven `fit_media_library_*` metrics;
  `docs/media-library.md` in English and Chinese.
- **Tests**: `make test-changed` under the race detector; `medialib` at 99.2%; a small ffmpeg-made
  library (80 KB) checked against ffprobe; rescans after files are added, changed and removed; broken
  files; symbolic links in, out of, looping and dangling; every bound; reloads racing reads and scans.
- **Gaps**: `test/workers` gets its case with I133's `vod`; a file swapped for a FIFO after a scan could
  hold a pool goroutine (never a fib worker). The decisions made are in the plan's I132 section,
  pending review.


### I133 Video on demand — done

- **`vod`** (`handler/vod`): a library's items over HLS, cut from the files as they are stored: a
  multivariant playlist (renditions as variants with BANDWIDTH, CODECS and RESOLUTION; audio files and
  subtitles as groups), media playlists of fMP4 or MPEG-TS segments (`container`, or
  `index.ts.m3u8`), cut at the keyframe nearest `segment_duration` and bounded by
  `max_segment_bytes`; WebVTT subtitles with `X-TIMESTAMP-MAP`; the files with `Range` through
  SendFile, and posters. Plans, playlists and segments are built on the library's file pool, a
  request waiting retained within `timeout`, and kept in a cache bounded by `max_cache_bytes`, shared
  across reloads; a file changed or removed answers 404 and is dropped. ETag and Last-Modified from
  the file, `cache_control`, and `media_auth`'s `play` on `vod/<library>/<id>`.
- **Tests**: every frame of every fixture in both containers matches the file's tables, segments start
  on keyframes, audio within a frame of video; a held pool, timeouts, a busy pool, cut and slow
  clients, the cache bound, reloads and changed files under the race detector; `vod` at 98.0%.
  `TestInterop` (ffmpeg 6.1, 7.1 and 9.0) and hls.js 1.5.20 in Chromium 156 play and seek every item,
  in a Linux container on the benchmark host (`test/docker/vod/e2e.sh`).
- **Benchmark** (`bench/vod.sh`, benchmark host, 4 server CPUs, segments of 1.26 MB, medians of 3):
  nginx serving ffmpeg's segments cut beforehand 24,960 segments a second, p99 3.6 ms, RSS 135 MiB;
  fit with every segment cached 19,210 (77%), p99 8.4 ms, 615 MiB (holding 435 MB of segments); fit
  with a 64 MiB cache, every segment read and muxed, 5,496, p99 44 ms, 206 MiB.
- **Gaps**: Safari's own player not checked (steps in `test/docker/vod/e2e.sh`), nor H.265 in a
  browser; cached segments are written from memory, not by sendfile (the 23% behind nginx); `v<i>`
  follows the catalog's order; a file's first video and audio tracks only, no MP3; BANDWIDTH is the
  average; no metrics of the cache. The decisions made are in the plan's I133 section, pending review.


### I134 Channels: files played as live streams, and pushed out — done

- **Channels** (`streaming.channels`, package `streaming/channels`): a `media_library`'s items played
  into the hub as a live stream, in order, shuffled, looped, or on a schedule of start times
  (`HH:MM[:SS]`, weekdays, or a date) in a time zone, paced by the samples' times on fit's timers,
  samples read on the library's file pool. Times run on from item to item; an item whose tracks differ
  sets new tracks, a discontinuity in HLS. Every egress of the streaming role serves a channel under
  its `auth` (RTMP, RTSP, SRT, HTTP-FLV, HLS, WHEP). A reload changes channels in place from the next
  item.
- **Push** (`streaming.push`, and a channel's `push`): any stream of the hub relayed to RTMP, RTMPS or
  SRT servers by fib's clients, reconnecting with backoff, URLs redacted wherever shown or logged.
- **Admin API**: `GET /streaming/channels`, `/streaming/channels/{name}`, `POST` with `start`, `stop` or
  `skip`; `GET /streaming/push`, `POST /streaming/push/{name}` with `start`, `stop` or `restart`;
  metrics a channel and a target; `docs/channels.md` in English and Chinese.
- **Tests**: `make test-changed` under the race detector; with a fake clock, pacing, items back to back,
  schedules on time, cut-ins and joins, reloads, skips and push backoff; a channel through every egress
  in process, without a gap over a frame for items encoded alike; pushes to fit and to another fit,
  restarted. On the benchmark host (`test/docker/channels/e2e.sh`): ffprobe on six egresses with
  continuous times, hls.js in Chromium through discontinuities, MediaMTX restarted and found again over
  RTMP and SRT.
- **fib A68** (`826c2c3`): fib's egresses shifted an item whose tracks differ by 120–135 ms, taking
  new tracks for a restarted clock; each now compares a packet with the last of its own kind, and
  HLS keeps the decode times across the discontinuity. fit is on that commit, and the channel tests
  hold such items to a frame's gap.
- **Gaps**: a codec change between items makes RTSP players drop and is not renegotiated over WHEP. The
  decisions made are in the plan's I134 section, pending review.


### I135 Playback URLs that expire — done

- **`media_auth.url_signing`**: URLs signed for one viewer and one resource (`/vod/<library>/<id>/`, a
  stream's `/hls/…/`, `/live/cam/` for `.flv`), carrying `exp`, `kid`, `ip` (the address, or its /24 or
  /64), `sid`, `sub` and `sig`, a truncated HMAC-SHA256; `vod`, `hls` and `httpflv` (channels included)
  check them on the worker without a callback and without allocating.
- **Grants**: a playlist hands what it lists a grant of its own, living `play_grace` past the later of
  the URL's expiry and the item's end, in the query or as the cookie `fit_media_grant`, never renewed.
  `max_sessions` a subject with `on_limit` (`refuse` or `end_oldest`), bounded by
  `max_tracked_sessions`; `referers`, `origins` and `trusted_proxies`; rotating a key refuses what it
  signed.
- **`media_tokens`**: `POST <prefix>/urls` names a viewer (subject, address, session), the paths and a
  ttl, and answers the URLs whole (`url_base`, `url_prefixes`). Tokens and signatures are `REDACTED` in
  the access log however a request is answered; refusals carry the route's CORS.
- **Tests**: expiry with an injected clock, a resource changed in any byte, another item, address and
  subnet, `max_sessions: 2` refusing or ending the oldest, key rotation, cookies, referers and origins;
  `TestSignedURLAccessLog`; `make test-changed` under the race detector; `mediaauth` 98.9%. hls.js
  1.5.20 in Chromium on the benchmark host plays every item through URLs from `/urls`: 401 without one,
  403 for one expired, and playing on through the grant after a seek. A cached segment on the
  benchmark host: open and token requests unchanged (6.13 to 6.03 µs, 6.77 to 6.92 µs, not
  significant), signed 7.63 µs.
- **Gaps**: sessions are counted by each fit alone; RTMP, RTSP, SRT and WHEP take no signed URLs;
  Safari's player was not tried again by hand. The decisions made are in the plan's I135 section,
  pending review.


### I136 Encrypted segments — done

- **`encrypt`** on `vod` and on a channel's HLS (with `key_rotation`, 10 by default): AES-128
  (`METHOD=AES-128`) for fMP4 and MPEG-TS, keys derived from the library's new `secret` or
  `secret_file` (`medialib.Keys`), one an item, or a channel's session and period; segments encrypted on
  the file pool before they are cached, within the cache's bound; a key served only to a grant or
  token for its resource, `private, no-store`; init sections, subtitles and posters clear; an
  encrypting `vod` serves no clear file.
- **Channels**: encrypted by fib's HLS Muxer (fib request A69, done at fib `70a9acc` with `9852eee` and
  `b4a9036`), which replaced fit's stopgap (segments copied, encrypted on the library's pool, cached
  within `max_bytes`, playlists rewritten, no parts): the `hls` handler gives fib's handler an
  `Encrypt` (keys as before, `Run` on the library's file pool), serves the keys under its `auth` for
  `epoch <= KeyEpoch()` of the session's Muxer, and restarts a channel's segmenting when a reload
  changes its encryption; Low-Latency HLS parts are encrypted too, and hls.js plays them
  (`TestChannelInterop`, live/secll).
- **Tests**: `make test-changed` under the race detector; on the benchmark host, ffmpeg plays every
  encrypted item frame for frame and hls.js plays them through signed URLs (`TestInteropEncrypted`,
  `test/docker/vod`), ffprobe and hls.js play encrypted channels across key rotations and
  discontinuities (`TestChannelInterop`); segments alone do not decode; a key is refused without a
  grant (401) and with another resource's (403).
- **Cost** (benchmark host, one core): AES-128-CBC at 1.24 GiB/s, 0.79 ms a 1 MiB segment; building a
  test library's segment +24 µs with fMP4 and +48 µs with MPEG-TS; a cached segment nothing more.
- **Gaps**: subtitles are clear; Safari's player not tried by hand. hls.js failed to append
  Low-Latency parts after a discontinuity where the tracks change, clear or encrypted alike: fib's
  LL-HLS, fixed since by fib A71 (below). The decisions made are in the plan's I136 section,
  pending review.

### Low-Latency HLS across new tracks (fib A71) — done

- **fib** `925f292` and `0ddaba3` (request A71), found with hls.js 1.5.20 on the benchmark host: the
  preload hint of a segment's first part now follows that segment's `EXT-X-DISCONTINUITY` and
  `EXT-X-MAP`, as soon as the tracks change (hls.js made the segment to come of the tags before the
  hint and kept the old init section for it, then appended the new item's parts after it: the media
  element failed to decode them, `bufferAppendError`); and a segment ended by new tracks lasts until
  the earliest of its tracks ends (its `EXTINF` had counted the AAC past the last video frame, and
  hls.js, placing the next item by it, stalled two seconds at every item). fit is on `856ad5b`, which has it.
- **Tests**: `TestChannelInterop` plays `live/secll` (encrypted) and a new `live/mixll` (clear) by
  their parts across items whose tracks differ, with hls.js, 30s at 25 frames a second through five
  discontinuities without a stall or a dropped frame, and ffprobe reads `live/mixll`'s Low-Latency
  playlist; on a fatal error the page reports what hls.js loaded and appended and the last playlist.
- **Gaps**: hls.js playing whole segments (`live/mix`, `live/sec`) shows about 0.8s of video frozen at
  each change of tracks, the audio and the clock running on (it did before A71; the test passes); not
  looked into. The decisions made are in the plan's I136 section, pending review.

## Static files follow the disk

### fib filewatch; check_interval 5s, 0 for every request — done

- **The gap** (found 2026-11-06): `handler/static` told a file by size and modification time only, so a
  file replaced by one of the same size and time (`rsync -t`, `cp -p`, `tar x`, reproducible builds)
  was served from memory as it was, for good; a change showed only after `check_interval` (1s), which
  could not be set to 0.
- **fib** (`3ac534d`): a public `filewatch` package. `filewatch.Watcher` is the inotify watch
  `http.FileCache` had to itself, now watching every directory on the way from the root (a renamed
  directory is a `Dir` event, its old watches dropped), the directories of symbolic links on the way to
  the root (a deploy's `current` pointed elsewhere is `All`), and a swapped link to a directory under
  the root. `filewatch.Stamp` adds the status change time and device to size, mtime and inode.
  `FileCacheConfig.CheckInterval` (5s by default, negative for every request) bounds what a watch
  misses, and replaces the stat on every request off Linux.
- **fit**: fib bumped to that commit. `check_interval` is 5s when left out, and `0` looks at the disk for
  every request with no watch, so that the next response after a replacement is the new file. On
  Linux the handler watches the root with `filewatch`; an event drops the lookups that depend on what
  it names, through an index (the path, its compressed twins, the entries of a directory whose index
  file or listing was found, and everything under a directory that went), and a lookup resolved while
  an event came is not kept. Contents are served while the file's stamp is the one they were read
  with. `Cleanup` closes the watch.
- **Tests**: on Linux under `-race`, a same-stamp replacement, a file created and removed, an index file
  that comes before another, a directory swapped, a root link swapped, each seen at once with
  `check_interval` 1h; with no watch, the same-stamp replacement past the interval and at once with 0;
  what each kind of event drops.
- **Decisions pending review**:
  1. The ETag stays mtime and size, as nginx makes it: a file replaced with the same size and time
     keeps its ETag, so a client revalidating its old copy is answered 304. Adding the inode or change
     time would make ETags differ between machines serving the same files.
  2. ~~Watched lookups still expire at `check_interval`.~~ Ruled by the user the same day: under a
     watch nothing is looked at again until it reports a change (fib `08e6f0a`, and here). The
     interval is for other systems and for a file that is a symbolic link; a network file system's
     writes from other machines need `check_interval: 0`.
  3. No `watch: false` parameter: `check_interval: 0` turns the watch off, and the interval bounds the
     rest.
  4. fib's `FileCache` off Linux now trusts a file for 5s rather than stat it on every request; a
     negative `CheckInterval` keeps the old behaviour.
  5. Windows has no inode or change time; the stamp takes the creation time, which NTFS tunneling may
     carry over to a file renamed into another's place, so a same-size, same-time replacement there is
     not told apart at all: size and times are all it has, whatever `check_interval` is.
  6. On a root that folds case, an event names the file as written on disk, and a lookup by another
     case waits for the interval. inotify roots that fold case are rare.


### I137 A video site, end to end — done

- **fit-examples `video-site`**: a media library played by `vod` and as a channel, `media_tokens` on the
  loopback behind `basic_auth`, a small Go API that logs viewers in and asks `/media-tokens/urls` for
  URLs bound to each viewer's opaque ID and address, and a page playing them with hls.js.
- **`media_pages`** gains `libraries`, `vod` and `channels`: a library page (`<prefix>/library`) of what
  the logged-in user may play, and a player page (`<prefix>/watch`) that plays it with hls.js through a
  URL the pages sign (`api/play`), stopping and offering to play again once refused; hls.js 1.5.20
  (Apache-2.0) vendored with its license at `<prefix>/vendor/hls.min.js`. `mediaauth` gains `Whois`
  and `ViewerAddr`.
- **Docs**: "A video site" in `docs/media-library.md`, English and Chinese; decision 032's I137 entry.
- **Tests**: listing, filtering by permission, paging and search, a bcrypt login, signed and expired
  URLs, channels; `test/docker/videosite` on the benchmark host's Docker: headless Chromium logs in to
  the example's API, plays a film and the channel, seeks, and is refused once a URL and its grant
  expire, on the site's page and on the library and player pages: 16 checks of 16.
- **Gaps**: Safari's own player not tested; `/media/play` still plays HLS natively only. The decisions
  made are in the plan's I137 section, pending review.


### I138 The workbench's media library pages — done

- **Media library** module (tagged VOD and channels) beside Streaming and Media auth: Libraries
  (search, codec, resolution and problem filters, paging, Rescan with progress); an item's page
  (renditions and tracks, audio, subtitles, poster, sidecar, probe errors with Ask AI, and an hls.js
  preview on a two-minute URL `POST /media/preview` signs for the user, stopping at the first refusal);
  Channels (the playlist and schedule as a timeline with drag and drop, loop, shuffle, time zone and
  rendition into the shared draft; now and next, start, stop, skip, play now and a preview; push
  targets with state, error, bytes and bit rate); Playback URLs (a test URL, never kept; keys by name
  and age, rotated after a confirm; refusals by reason; live sessions paged and ended); Metrics
  (`GET /media/stats` every 2s); and config pages for `media_library`, `vod` routes,
  `streaming.channels` and `push`, and each policy's `url_signing`, with the UI, `.conf`, `.yaml` and
  `.json` tabs and save errors linked back. The Engine menu lists each port's `vod` routes and
  channels under streaming and tags the port `VOD`.
- **Admin API**: `/media/preview`, `/media/auth` with `sessions`, `sessions/end`, `rotate` and `urls`,
  `/media/stats`, `min_height` and `max_height` on a library's page; `vod`, sample-table and URL refusal
  metrics. The console's CSP admits the `vod` and `hls` listeners; hls.js 1.5.20 is vendored once in the
  workbench, and `media_pages` serves it from there.
- **Tests**: checked in a browser against a running fit with three libraries and two channels, English
  and Chinese, and against mocked endpoints; `test-changed` under the race detector. The counters on
  `vod`'s path cost nothing measured: `BenchmarkServeSegment` on the benchmark host, 20 rounds,
  open 6.08 to 6.09 µs, token 7.00 to 6.92 µs, signed 7.59 to 7.61 µs, none significant
  (`bench/microbench.sh`, which now runs each build beside its package's `testdata`).
- **Gaps**: cookie-mode `url_signing` does not reach the preview (the cookie is not sent to another
  origin); each preview is a new session, so a low `max_sessions` with `refuse` may refuse repeated
  previews; secrets written in the config still show in the config editor's raw tabs. The decisions
  made are in the plan's I138 section, pending review.


### I139 Media files managed from the workbench — done

- **Uploads**: `media_library` gains `upload_dir`, `max_upload_bytes` (32 GiB), `max_uploads` (4),
  `min_free_bytes` (1 GiB) and `upload_expiry` (24h). Resumable as tus makes them:
  `POST /media/library/<name>/uploads`, `PATCH` at `Upload-Offset`, `HEAD` to resume, `DELETE` to give
  up. The console streams a PATCH's body to the library's file pool as it arrives (`StreamBody` in
  `app/console`, on fib's `StreamRequestBodyAtHeader`, `OnBody`, `HoldBody` and `LimitBody`), 512 KiB at a
  time, into `.fit-upload-<id>.part`, kept across restarts by its `.json`, synced and renamed into place
  once whole, then probed by the scan; 413 before anything is written, 429, 507; a body past the upload
  gives it up and removes its file.
- **Rename, move, delete and sidecars**: on an item and all its files, on the pool, one change at a
  time, never writing out of a root; a rename keeps an ID its sidecar names and rewrites the sidecar's
  references; the item leaves the catalog, its sample tables and the `vod` cache at once.
  `GET .../usage` says what plays an item (channels naming or playing it, playback sessions); a change
  to one in use is refused 409 unless `force`. Every change is in the assistant's audit log (kind
  `media`, source `workbench`).
- **Workbench**: an Upload card on the library page (8 MiB pieces, retries, pause and resume, an upload
  under way resumed by picking its file again); Manage files (rename, move with a folder list, delete,
  each confirmed with what uses the item) and a sidecar form on the item page; English and Chinese;
  checked in a browser.
- **Tests**: resuming after a cut through `medialib`, the Admin API and the console; 413 leaving no file;
  paths out of the roots; a rename keeping a sidecar's ID; a delete dropping the catalog's item, its
  tables and the `vod` cache; usage with a live channel and session; restarts and expiry; under the race
  detector. On the benchmark host, a 4 GiB upload cut at 2 GiB, resumed, checksummed and probed
  (`TestUploadGB`, run with `FIT_UPLOAD_GB`).
- **Gaps**: a delete that fails part way cannot roll back; a PATCH straight to the Admin API is bounded
  by its one-minute read timeout; sessions are known only under `url_signing`; changes through the
  token-only Admin API are not in the audit log. The decisions made are in the plan's I139 section,
  pending review.


### I152 The assistant's media library tools and skills — done

- **Tools**: `media_library_search`, `media_item_get` (with what uses the item), `channels_status` and
  `playback_sessions` read through the Admin API in process; `media_scan_propose`,
  `media_session_end_propose`, `media_key_rotate_propose` and `media_file_propose` propose. Media steps
  are actions: they join a config change of any kind, as purges do, run after it in order through the
  media endpoints as the person who applies (audited once, as that person's `media` entry), and only
  on Apply and the confirm. A file step shows what uses the item (channels naming or playing it,
  viewers) in the tool's result, the card and the dialog; the apply sends back `media_tag`, and a
  mismatch offers the proposal again (412); `force` goes only for what was shown in use.
- **Redaction**: signed playback URLs (and `sig=` and `fit_media_grant=` values) never reach the model;
  a push target's `url`, which holds the stream key, is a secret everywhere, where `config_get` passed
  it before; subjects go hashed unless `admin.assistant.data.send_subjects` allows them.
- **Skills**: `vod-setup`, `channel-scheduler`, `library-health` and `playback-troubleshoot`; the media
  pages' Ask AI runs the last three (an item's probe error, a push target in error, the 403s by reason).
  The media tools are served over MCP within a token's level.
- **Tests**: each skill on a fixed library against a scripted model; no key, signature, playback URL or
  plain subject in a request; a delete proposal naming the playing channel; an end-to-end apply on a
  real library writing `media.delete` as the user; under the race detector; checked in a browser.
- **Gaps**: subjects are hashed in tool data only, not in log lines or a page's selection; the
  `media.*` audit entries of an assistant's apply say source `workbench`. The decisions made are in the
  plan's I152 section, pending review.


## Cluster management

### I140 Decision 033; what a member needs to be managed — done

- **Decision 033** (English and Chinese): the cluster track's choices, and its open questions as
  proposed, pending review.
- **`GET /node`** (the Admin API; `/api/node` and `/api/m/admin/node` through the console): the node ID
  (`admin.node_id`, or made once and kept in the store's section `node`, or in `fit.node-id` beside the
  config), the host, fit's, fib's and Go's versions, the start time, the running config's version,
  ETag and source, the roles, a digest of the schema and the capabilities (`admin.Cap*`).
- **Dry runs**: `?dry_run=1` on `PUT`, `PATCH` and `DELETE /config`, `POST /load` and
  `PUT /config/text` runs the whole apply path (`If-Match`, merge, load, compile, new listener
  addresses tried with `listener.Server.Plan`) and answers as an apply does, with `dry_run`, `new_etag`
  and `unchanged`, changing nothing.
- **Logins**: `{"user","csrf"}` for programs; 429 with `retry_after`; a user slowed after 10 failures in
  a row (2s doubling to 5 minutes); address lockouts doubling to an hour.
- **Workbench**: Account, This node; **Check** beside Save on the config pages, its errors where Save's
  go. Checked in a browser in English and Chinese.
- **Tests**: the same ID across restarts with and without a store, and its move into a store; dry runs
  refusing what an apply refuses and changing nothing; a program's login, read and apply; the backoff
  with an injected clock; under the race detector.
- **Gaps**: a dry run cannot see a role failing at its apply (the agent's, the tunnel's), nor an address
  taken between the check and the apply. The decisions made are in the plan's I140 section, pending
  review.


### I141 The manager: members, their credentials and their state — done

- **`admin.cluster`** (`poll_interval`, `timeout`, `max_nodes`, `max_concurrency`, `max_response_bytes`;
  needs `admin.store` and `admin.console`) makes a fit a manager. Package `cluster` keeps the members in
  the store's section `cluster`, a versioned document beside the groups, templates, rollouts and audit
  log that later iterations fill.
- **Members** are reached through their console (user and password, logged in again on 401, refused
  logins backed off, a password change reported and never made) or their Admin API (a bearer token);
  certificates are checked by the system's CAs, a `ca` PEM or a pinned `fingerprint`, and one refused is
  still shown.
- **The poller** reads `/node`, `/status` and `/config/check` on fib's asynchronous client, at most
  `max_concurrency` members at once, every call bounded by `timeout` and `max_response_bytes`, each
  failure given its own kind.
- **The console module `cluster`**: `/api/m/cluster/nodes[/<name>]`, `.../nodes/<name>/test`, `/test`,
  `/state` and `/audit`; no credential or session cookie in an answer or a log.
- **Tests**: `cluster` at 99.8% under the race detector; a real manager and member in `cli`; on the
  benchmark host's Docker (`test/docker/cluster`), a manager and three members: password, token, a wrong
  password, a certificate not trusted, a wrong pin, a member down, a member's restart and the manager's.
- **Gaps**: no pages yet (I142); entries the manager keeps for one member share that member's address
  lockout; any console user can make the manager test a URL. The decisions made are in the plan's I141
  section and decision 033, pending review.


### I142 The Cluster menu: overview, nodes and a node's page — done

- **Cluster** in the workbench's left menu, shown with `admin.cluster`: **Overview** (members as cards
  or a table, colored by state, with health, fit and config versions, drift, nearest certificate
  expiry, connections and latency; filtered by text, label selector and state in the URL, sorted,
  refreshed as the manager polls; Ask AI on a member in trouble), **Nodes** (add, test, trust a
  certificate by its fingerprint, edit, remove with a confirm; credentials written once and never
  shown; recent audit) and a member's page (its state, and its `/node`, `/status`, `/versions` and
  `/config/check` read through the manager by `GET /api/m/cluster/nodes/<name>/read/<what>`, a bounded
  read-only pass-through that I143 makes general); English and Chinese.
- **`docs/cluster.md`** in both languages (a manager-only `fit run -admin` config, the pages, failure
  kinds, the API, and members behind NAT through the tunnel until I149), moved from the console's docs.
  `fit run`, not the config, now checks a manager's store, so `fit run -admin` with `admin.cluster`
  alone runs one.
- **Tests**: `cluster` at 99.7% under the race detector; I141's Docker harness with the new reads; in a
  browser against local fits: each failure kind shown and recovered from, add, test, trust, edit and
  remove, both languages, no credential in the page, an answer or the log.
- **Gaps**: Ask AI was not clicked (no assistant on the test manager); a read and a poll that both find
  no session may both log in. The decisions made are in the plan's I142 section and decision 033,
  pending review.


### I149 Members behind NAT, linking out — done

- **The manager**: `admin.cluster.link_address` (and `link_url`) serves a WebSocket-over-TLS link
  listener whose self-signed certificate is kept in the store; Nodes, **Add by enrollment**, makes a
  `link` member and a one-use token (`fite1.`), and shows the `admin.cluster_member` line to paste on
  the member (the manager's URL, its certificate's fingerprint, the token).
- **The member** dials out on fib's WebSocket client, pins the certificate, proves the token by
  `internal/linktoken`'s handshake for a credential of its own (`fitc1.`, a hash on the manager, kept in
  its store or `fit.cluster-member`), and links again with backoff, pinging every 15s and closing after
  45s of silence. A member removed on the manager is revoked and forgets its credential.
- **Over the link** the manager reaches the member's Admin API in process (`admin.LinkAPI`): the poller,
  tests, the member's reads and the new `Manager.Call` (I143's pass-through) go through one transport
  for HTTP and links. The state shows `link` and the failure kind `not_linked`; rows show Linked or
  Waiting, with New token.
- **Tests**: enrollment; polls and calls over the link; a manager's restart; a network cut and a silent
  drop; used, expired and replaced tokens; pins; bounds; two real fits in `cli`; under the race
  detector; the pages checked in a browser in both languages.
- **Gaps**: stopgaps for fib's WebSocket A52 and A53 (already requested); a member that cannot keep its
  credential after its token was spent needs a new token. The decisions made are in the plan's I149
  section and decision 033, pending review.


### I144 Resource groups and operations on many — done

- **Groups** in the manager's document: a label selector (`k=v`, `k!=v`, `k`, `!k`), members by name, or
  both; a description; variables for I145; membership computed from the labels as they are and listed
  in every member's state; `/api/m/cluster/groups[/<name>]`, `POST select`.
- **Operations** on a group or a selection (`POST /api/m/cluster/ops`, 202, followed at `ops/<id>`):
  reload (the Admin API's new `POST /reload`, `config.reload`), rollback to the latest version of
  another ETag (with `If-Match`), check, cache purge by key, prefix, tag or all (`prefixes` new in
  `POST /cache/purge`, `cache.prefix`) and test; each on a goroutine of its own, `max_concurrency`
  members at once through `Manager.Call`; 4 operations at once, one a member; each member's outcome
  reported and audited.
- **Workbench**: Cluster, Groups (the list; a group's form with a live preview and its variables; a
  group's page with its members' states, check boxes and an operations bar; dialogs naming the members;
  each member's result as it comes; recent operations; `#/cluster/ops` for any selection); the overview
  filters by group. Checked in a browser in both languages against three members and one down.
- **Tests**: selectors; a label change moving members in and out; five members with one down, four done
  and one failed with its error; a purge reaching every member's zone; a real manager and member in
  `cli`; prefix purges in memory and on disk; under the race detector.
- **Gaps**: an operation cannot be cancelled; no rollback to a chosen version; no `in` or `notin`; older
  members fail reload and prefix purge as `unsupported`. The decisions made are in the plan's I144
  section and decision 033, pending review.


### I143 A member's own pages, opened from the manager — done

- **The node switcher** in the workbench's header opens the manager's own pages (config with its text
  tabs, Engine, versions, Consistency, logs, media, the tunnel's roles) against a member, the URL
  carrying `?node=<member>`, the member's name marked in its own color before the title, in the tab's
  title and in a band above the page; **Open** on every member's card, row and page.
- **The pass-through** `/api/n/<member>/...` goes on with the session, token or link the manager keeps;
  headers by fixed lists both ways, so no cookie or header of the member reaches the manager's page and
  no credential reaches the browser; 3 calls a member and 16 waiting, 64 in all, bodies within
  `max_response_bytes`; every write in the manager's audit log (`node.api`) with its user; a member of
  another config schema opens read-only, refused by the page and the manager.
- **Also**: `console.Module.Prefixes`; the Admin API's `GET /schema` (`schema`); queries on calls to `http`
  members; a data race on a refused login's problem fixed.
- **Tests**: console, token and linked members; headers both ways; `If-Match` and 412; dry runs;
  read-only; refusals; bounds; a real manager and member in `cli`; under the race detector. In a browser
  in both languages against a manager, a console member running a tunnel hub, a token member, a linked
  member and an older one: saves applied as a new version, a stale ETag refused, the older member
  read-only, the way back.
- **Gaps**: the LLM roles' pages are not opened through the switcher; a member added a moment ago may
  open read-only until its first poll. The decisions made are in the plan's I143 section and decision
  033, pending review.


### I145 Templates: config shared by a group — done

- **Templates**: fragments of fit's config the manager owns on the members of its groups: top-level
  members whole, and listeners, routes, services and upstreams by element name; variables per member
  (`${node.name}`, `${node.id}`, `${node.hostname}`, `${node.labels.<key>}`, group variables); an `order`
  for overlaps; secrets kept in the store and masked; warnings for file paths;
  `/api/m/cluster/templates[/<name>]`.
- **Sync state**: each member's desired config (its own, the owned parts taken from its templates) and
  each part in sync, out of sync or a conflict with its reason, by comparing the member's value, the
  rendered one and the hash last seen in sync, which the member keeps through its new
  `PUT /node/managed` (`node.managed`); the member's config pages mark owned parts and ask before a local
  change. `Manager.Desired` is ready for I146's rollouts; nothing but that record is written to members.
- **Pages**: Cluster, Templates (the list; an editor with a form and `.conf`, `.yaml` and `.json` tabs;
  the members' sync with diffs), a member's rendered result, Compare, Import from a node; English and
  Chinese; checked in a browser, a route changed on a member showing as a conflict on the manager.
- **Tests**: I145's done-when end to end against two real fits (one with a store, one without) under the
  race detector; the API's edges with fake members.
- **Gaps**: the template form edits handlers and maps as JSON; a member down when told hears at its next
  read; a member without a store has `admin` in its desired config, for I146 to check. The decisions
  made are in the plan's I145 section and decision 033, pending review.


### I146 Rollouts — done

- **Plans**: a template's change, a sync, or parts copied from a member: each member's desired config,
  diff and own dry run (`If-Match` on the ETag read); members not ready said why.
- **Strategies**: all at once, rolling (by size or percentage, with a pause), or a canary waiting for a
  go; each member applied by its `PUT /config` with `If-Match` (changed since the plan, skipped and never
  overwritten); a health gate after each batch (reachable, running the config applied, no new drift,
  and a 5xx share over a window from the new `GET /status` `requests`, `status.requests`); a failed gate
  stops and rolls back the batch, or all, by `/rollback/{n}` with `If-Match`.
- **Control**: pause, resume, go, abort, roll back, discard; one rollout or operation a member; kept in
  the store (the last 50) with each member's outcome and versions; a restart shows a running rollout
  paused where it stopped, an apply in flight settled by ETag on resume.
- **Pages**: Cluster, Rollouts (history, a new rollout, its plan with diffs and dry-run errors, the
  strategy form, a confirm dialog, progress batch by batch with gates and controls), **Roll out** on
  templates, **Sync this node** on rendered results; English and Chinese.
- **Tests**: Docker with five members on the benchmark host (`test/docker/cluster` `TestRollouts`): a
  rolling rollout in batches, a member failing its 5xx gate rolled back with its batch, a canary
  waiting for its go, a member edited after the plan skipped, a manager restarted between batches and
  resumed; `cluster` at 99.0%; the race suite.
- **Gaps**: with no traffic the 5xx gate passes with a note; server reasons are English on the Chinese
  pages; a rollout's history keeps the paths changed, not the values. The decisions made are in the
  plan's I146 section and decision 033, pending review.


### I147 Drift and alerts — done

- **Drift on every poll**: the consistency check with each poll, and a member a template is attached to
  read against its templates once its config's ETag changed, without telling it anything and never
  blocking the poller; `GET /api/m/cluster/drift` lists each member's drift with its diff, and Fix plans
  a sync rollout of it (`POST drift/fix`, `FixDrift(user, nodes)`), started by a person.
- **Alert rules** (member down, drift, rollout failed, certificate expiring, upstream backends down,
  reloads failing) fire once their condition has lasted their time, are sent to webhooks (fit's signed
  JSON, Slack, Discord, Feishu, DingTalk, WeCom, custom) one at a time each on fib's asynchronous client
  from a bounded queue, repeat unless silenced, and resolve after; rules, webhooks (URLs and secrets),
  silences and alerts are kept in the store's section `cluster.alerts`.
- **Pages**: Cluster, Drift and alerts (drift, alerts, rules, webhooks, silences) and a badge on the
  Cluster menu while alerts fire; English and Chinese; checked in a browser against two members, one
  down and a webhook sink.
- **Tests**: each rule firing once its time has passed and resolving after, with an injected clock; a
  silence holding through repeats; a webhook that never answers not delaying the poller; every format's
  body and signature; secrets redacted; alerts across a restart; `cluster` at 99.2% under the race
  detector.
- **Gaps**: a failed send is not retried; no Teams or Telegram format; alert summaries are written in
  English. The decisions made are in the plan's I147 section and decision 033, pending review.


### I148 Users, roles and the audit log — done

- **Roles**: the manager's console users are `viewer`, `operator` or `admin`, cluster-wide and per group,
  kept in the store's cluster document (version 3 once set) and changed on Cluster, Users and audit, by a
  cluster-wide admin; `admin.cluster.admins` names admins whatever the roles say; until roles are set
  every console user is a cluster-wide admin.
- **One check**, `Manager.Allow`, guards every module endpoint (through a router that fails closed), the
  node switcher, I147's drift and alerts, the console's new gate on the manager's own pages, and the
  assistant's levels.
- **The audit log**: entries gain members, outcome, a diff (16 KiB) with its digest, and versions before
  and after (the document's `rN`, alerts' `aN`, members' config versions); filtered by user, member,
  operation and time, exported as JSON lines, bounded by entries and bytes on every write, kept across
  restarts; tests of members are audited.
- **Pages** show only what each role allows; checked in a browser as a viewer, an operator of one group,
  that group's admin and a cluster-wide admin, in both languages.
- **Tests**: each role refused across the endpoints, the switcher, the gate and I147's endpoints; the open
  state before roles; roles and the log across a restart; `cluster` at 99.1% under the race detector.
- **Gaps**: lists are not filtered by group; with roles set the manager's own LLM pages, which read by
  POST, need a cluster-wide admin; a rollout's plan entry has no diff. The decisions made are in the
  plan's I148 section and decision 033, pending review.


### I150 Metrics, logs and certificates across the cluster — done

- **Metrics**: series kept by the manager from each poll, 360 a member, per member and summed per
  group (requests, 4xx and 5xx a second, connections, bytes queued for clients, reloads),
  `GET /api/m/cluster/metrics`.
- **Certificates**: the inventory from each member's `/status`, now with issuer, serial and dates,
  soonest expiry first, `GET certificates`.
- **Logs**: tailed from up to 16 members at once through a new member endpoint `GET /logs/recent`
  (capability `logs.recent`, the log ring numbered), merged by time and followed with a cursor;
  a member down is reported on its own while the others are read. `GET logs`.
- **CLI**: the manager serves `/cluster/...` on its own Admin API, acting as the cluster-wide admin
  `Admin API`; `fit cluster nodes | state | groups | diff | certs | metrics | logs | rollout
  list/start/status/pause/resume/go/abort/rollback`.
- **Pages**: Cluster → Metrics (a line per member and per group, a table), Logs (members, filters,
  follow), Certificates; sparklines on the Overview; English and Chinese.
- **Tests**: series against a member's own counters, a merged tail of three real members, the
  expiring certificate first, the CLI driving a rollout end to end and aborting a canary; race tests
  on the changed packages and their dependents; the pages checked in a browser against a manager and
  three members.
- **Gaps**: "bytes" are bytes queued, not throughput; series start afresh at a manager's restart; logs
  through a member reached over its console were not run end to end. The decisions made are in the
  plan's I150 section and decision 033 (items 134–145), pending review.


### I153 The assistant across a cluster — done

- **Tools on a manager**: `cluster_members`, `cluster_templates`, `cluster_rollouts`, `cluster_metrics`
  (counters read now plus I150's series), `cluster_certs` (I150's inventory) and `cluster_log_tail`
  (I150's merged tail, behind `data.send_logs`); a `member` argument on the 12 read tools that read the
  Admin API, through the manager's session, reads only.
- **Proposals**: `rollout_propose`, `cluster_op_propose` and `drift_fix_propose` make a rollout plan
  (each member's diff and dry run, a suggested strategy) or a checked operation, linked to its page and
  started only by a person with that page's confirm; never a step of the conversation's proposal.
- **`cluster`** gains patch rollouts and template drafts (saved when the rollout starts, by a
  cluster-wide admin), `source` and `suggested` on plans, and `Manager.Assistant()`.
- **Checks**: the manager's roles and the assistant's level, the narrower winning; no member credential
  reaches a model; reads across members within `max_concurrency` and 20s.
- **Skills and pages**: `fleet-health`, `rollout-planner`, `cluster-drift`, on a manager only; Ask AI on
  the Overview, a failed batch and a drifting member; the New rollout form's patch and draft; MCP serves
  the tools with the workbench's link.
- **Tests**: a patch and a draft planned with diffs and dry runs and applied only at the start; an
  operator refused a template change; no credential in a recorded request; bounded reads with a slow and
  a down member; race tests on the changed packages; a browser run with a manager, two members and a
  scripted model.
- **Gaps**: the failed-batch Ask AI and the Chinese pages not looked at in a browser; a draft changes an
  existing template only. The decisions made are in the plan's I153 section and decisions 031 and 033
  (items 146–156), pending review.
