# fit Web Console Tutorial (Outline)

[English](README.md) | [简体中文](README.zh-CN.md)

This tutorial follows the use of fit's **Web console** (the workbench): it deploys a fit with a
console, logs in, and then walks through the **function navigation bar** on the left, module by
module. Each module is a chapter: what it does, the pages and operations it offers, its **key
parameters**, and how to write the corresponding **.conf configuration**.

Readers should already know [what fit is](../../README.md), the
[.conf format](../reference/fit.md), and [how the config loads](../configuration.md). The
tutorial does not repeat full field tables; it links to the
[config reference](../reference/config.zh-CN.md) (generated from code, in sync with the binary).

## Structure

The first three chapters are preparation: deployment, login, and the navigation bar. The
following 22 chapters correspond one-to-one with the modules of the navigation bar, in menu order.

| Ch. | File | Menu module | What it covers |
| --- | --- | --- | --- |
| 01 | [Deployment](01-deployment.md) | — | Installing fit; the bootstrap file, the store and `admin.console`; first start with `fit run -admin`; creating users; reaching the console; Docker, Helm and systemd. |
| 02 | [Login](02-login.md) | — | The login page; users and bcrypt password hashes; the default password and the forced change; sessions; lockout and slowdown; CSRF and CSP; changing passwords. |
| 03 | [The function navigation bar](03-navigation.md) | — | The workbench layout and URL scheme; the full list of modules and pages; filtering, collapsing, themes and languages; the roles' dynamic modules. |
| 04 | [Overview](04-overview.md) | Overview | The running config at a glance: version, connections, routes, upstream health, certificates, spikes. |
| 05 | [Listeners](05-listeners.md) | Listeners | The `listener` parameters: address, protocols, TLS, client certificates, SOCKS, the PROXY protocol, mptcp. |
| 06 | [Engine](06-engine.md) | Engine | The running features per port: handlers, routes, cache, streaming, WebRTC, layer 4, SOCKS, TLS; the actual bound addresses and tags. |
| 07 | [Global settings](07-global-settings.md) | Global | `log`, `access_log`, `telemetry`, `metrics`, `pprof`, `limits`, `engine`, `providers`, the remaining members, and importing and exporting the whole config. |
| 08 | [JSON](08-json.md) | JSON | The whole config as JSON: the draft and the effective config, the sources and the precedence. |
| 09 | [Versions](09-versions.md) | Versions | The applied config versions: viewing and rolling back. |
| 10 | [Consistency check](10-consistency-check.md) | Consistency check | The differences between the running fit and the effective config: drift and info, per-item fixes, `fit check`. |
| 11 | [Logs](11-logs.md) | Logs | The files of fit's own log and the access log, rotation, retention, rotate now and clean now. |
| 12 | [Cluster](12-cluster.md) | Cluster | The `admin.cluster` manager: members, groups, templates, rollouts, alerts, users, cross-member metrics/logs/certificates, members behind NAT. |
| 13 | [Account](13-account.md) | Account | Changing your own password, setting another user's password, the node's information. |
| 14 | [Assistant](14-assistant.md) | Assistant | `admin.assistant`: providers and keys, levels, the chat panel, proposals, skills, the MCP server, the audit log. |
| 15 | [Reverse proxy](15-reverse-proxy.md) | Reverse proxy | Routes: matching, handlers, middleware; upstreams: load balancing and health checks; the handler and middleware modules. |
| 16 | [Forward proxy](16-forward-proxy.md) | Forward proxy | The `forward_proxy` routes (HTTP CONNECT) and SOCKS5. |
| 17 | [L4 proxy](17-l4-proxy.md) | L4 proxy | `services`: TCP and UDP forwarding, SNI pass-through, the PROXY protocol. |
| 18 | [TLS](18-tls.md) | TLS | The certificates page (the certificates being served), automatic HTTPS, ACME. |
| 19 | [Cache](19-cache.md) | Cache | The cache zones, purging, the `cache` middleware. |
| 20 | [Streaming](20-streaming.md) | Streaming | `streaming`: the RTMP, RTSP, SRT and WebRTC listeners, the channels and the push targets; the streaming handlers on routes. |
| 21 | [Media auth](21-media-auth.md) | Media auth | The `media_auth` policies: tokens, users, the key ring, public actions, URL signing. |
| 22 | [Media library](22-media-library.md) | Media library | The library items, the channels, the playback URLs, the metrics, the config pages and the `vod` routes. |
| 23 | [WebRTC](23-webrtc.md) | WebRTC | `webrtc`: the TURN server, the WHIP/WHEP media server, the media proxy. |
| 24 | [Tunnel roles](24-tunnel.md) | Tunnel roles, tunnel.hub, tunnel.node, tunnel.proxy | The `tunnel` config; the pages of the hub, node and proxy roles. |
| 25 | [LLM roles](25-llm.md) | LLM roles, llm.hub, llm.node, llm.proxy | The `llm` config; the pages of the hub, node and proxy roles. |

## Chapter details

The pages, the config members each chapter edits, and the main sources:

### 01 Deployment

- Installing: `go install`, the prebuilt binaries, the container image `ghcr.io/lesismal/fit`, the Helm chart, the systemd unit.
- The two files: the bootstrap file (plaintext, holding `admin`) and the store (sealed, holding the rest of the config); the fields of `admin.store` (`path`, `password`, `password_file`, `password_env`).
- First start: `fit run -admin` (default user `admin`/`admin`, the password must be changed within `-timeout`); the default store location and the key file.
- Creating users: `fit hash-password`; `admin.console.users`.
- Reaching the console: loopback (SSH tunnel); a non-loopback address requires `cert_file` and `key_file`.
- Packaging: Docker (ports, volumes, the store password), Helm (the Gateway API and Ingress controller), systemd (`fit.service`, `fit@.service`, reload).
- Sources: [deployment](../../deploy/README.md), [getting started](../getting-started.md), [the console and the store](../console.md#two-files), the README.

### 02 Login

- The login page and `POST /api/login`; the session cookie (`fit_console_<port>`), `session_timeout`, `session_lifetime`, `max_sessions`, `max_conns`.
- The default-password flow: it can only open the "change the default password" page; it expires.
- Security: address lockout (5 failures in 15 minutes), user slowdown, the dummy hash, CSRF (`X-Fit-Csrf`), the CSP and the response headers, the `admin` log.
- Sources: [the console and the store](../console.md#logins-and-sessions), [the console and the store](../console.md#users-and-fit-hash-password), [the console and the store](../console.md#limits-and-timeouts).

### 03 The function navigation bar

- The layout: the menu on the left (first level the modules, second level the pages, third level the features of a port), the page on the right; the top bar (title, breadcrumb, tools, theme and language).
- The URL scheme: `#/<module>/<page>`; a module is shown only when the console serves its server module.
- The full list of the modules and their pages (in menu order), with the small tags (HTTP, HTTP CONNECT, TCP/UDP, TLS, RTMP / RTSP / SRT, media tokens, WebRTC, Tunnel, LLM, VOD / channels).
- Filtering the menu, collapsing it to icons, expanding and folding; the themes (light, dark, and three more) and the languages (English, Chinese).
- The roles' dynamic modules: `tunnel.*` and `llm.*` appear once the role runs.
- Sources: `app/workbench/web/core/workbench.js` (the menu order RANK and the module registrations), [the console and the store](../console.md#the-pages).

### 04 Overview

- The running config at a glance: the config version and its source, the reloads, the connections, the routes and the layer 4 services, the up backends, the certificates (expiring within 14 days), the element counts per source, the shadowed elements.
- The spikes: the error counts that grew between two refreshes (upstream failures, refused connections, failed reloads).
- Sources: `app/workbench/web/gateway/overview.js`, [the config reference: The config](../reference/config.md#the-config).

### 05 Listeners

- The page edits `listeners`; each row shows the address, the protocols and the TLS/SOCKS badges; the features of each port are in the Engine menu.
- The parameters: `address`, `protocols` (`http/1.1`, `h2c`, `h2`, `h3`, `tcp`, `udp`, `socks`), `tls` (file certificates, ACME, on demand), `client_auth`, `socks`, `proxy_protocol`, `mptcp`.
- The constraint: one address, one server; two listeners cannot listen on the same address and transport.
- Sources: `templates/conf/conf.d/10-listeners.conf`, [the config reference: Listeners](../reference/config.md#listeners), [the config](../configuration.md#one-address-one-server).

### 06 Engine

- The running listeners per port: each port expands to the features it serves — the handlers, the reverse and forward proxy routes, the cache, the streaming and WebRTC handlers, the layer 4 services, SOCKS, TLS/ACME/HTTPS by default — each linking to the page that edits it.
- Beside the written address, the address the kernel actually bound; the tags of the port row (the PROXY protocol, HTTP/3, on-demand certificates, client certificates, VOD).
- Sources: `app/workbench/web/gateway/engine.js`, [the console and the store](../console.md#the-pages).

### 07 Global settings

- Each page edits one top-level member: `log` (per-logger level, output, format, `time_zone`, `rotate`), `access_log`, `telemetry` (OTLP), `metrics`, `pprof` (toggled at once, served by the Admin API at `/debug/pprof/`), `limits` (connections, headers, request bodies, the write queue), `engine` (event loops, workers, queues, buffers, backlog), `providers` (Docker, Consul, etcd, Kubernetes).
- The "Other" page: the members that have no page of their own yet.
- The "Import / Export" page: the whole config, in `.conf`, `.yaml`, `.json` and `.store`.
- Sources: `templates/conf/conf.d/00-runtime.conf`, `30-observability.conf`, `50-providers.conf`, the chapters of [the config reference](../reference/config.zh-CN.md), [the console and the store](../console.md#importing-and-exporting-the-config).

### 08 JSON

- The draft tab: the whole config as JSON (without `admin` when fit keeps its config in the store), the same draft the forms edit; formatting, opening a file, downloading.
- The effective tab: the base config with what the providers provide, read only; the precedence and the sources.
- Sources: `app/workbench/web/gateway/modules.js`, [the config](../configuration.md).

### 09 Versions

- The applied config versions (`admin.history` of them): version, applied time, source, size, ETag; viewing the text; rolling back (applied again as a new version).
- Sources: `app/workbench/web/gateway/modules.js`, [the config reference: Admin API](../reference/config.md#admin-api).

### 10 Consistency check

- The differences between the running fit and the effective config: drift (fixable) and info (a running fact); the kinds of differences (start-time-only settings, the roles, the providers' parts, the listeners, TLS, the upstreams, the cache).
- Choosing the fixes item by item and applying them together; `fit check` does the same from the terminal.
- Sources: [the console and the store](../console.md#checking-the-running-config), `app/workbench/web/gateway/check.js`.

### 11 Logs

- The files of fit's own log and the access log, their rotation and retention, the files on disk with their sizes and times; **rotate now** and **clean now** (previewing the files to delete first).
- The forms of `log` and `access_log` (including `time_zone` and `rotate`).
- Sources: `app/workbench/web/gateway/logs.js`, [the config reference: Log](../reference/config.md#log), [the config reference: Access log](../reference/config.md#access-log).

### 12 Cluster

- `admin.cluster` makes this fit the manager of the others: the members' addresses and credentials in the store; the overview, the nodes, the groups, the templates, the rollouts, the alerts, the users, the cross-member metrics/logs/certificates.
- Adding, testing and trusting members; a member behind NAT connects to the manager with `admin.cluster_member`.
- Sources: [cluster management](../cluster.md), `app/workbench/web/cluster/`, [the config reference: Admin API](../reference/config.md#admin-api).

### 13 Account

- Changing your own password (the current one required), setting another user's password (every console user is an administrator); the passwords are kept in the store and apply to the next login.
- The node: the node ID, the versions, the start time, the running config's version and ETag, the roles, the capabilities.
- Sources: [the console and the store](../console.md#users-and-fit-hash-password), `app/workbench/web/account/account.js`.

### 14 Assistant

- `admin.assistant`: the providers (Anthropic, OpenAI, Gemini, Azure OpenAI, Ollama, OpenAI-compatible, `llm.proxy`), the keys (write-only), the default model, the limits.
- The user levels: `off`, `read`, `edit`, `propose`, `apply`; the chat panel (the current page as context); the proposals (review and apply); the skills (18 built-in plus custom); the MCP server (`/mcp` and its tokens); the audit log.
- Sources: [the console and the store](../console.md#the-assistant), `app/workbench/web/ai/`.

### 15 Reverse proxy

- The routes page: tabs by handler module (all, `proxy`, `static`, `respond`, `fastcgi`, …); each route's `match` (host, path, method, header, query, remote_ip, expr), `handler` and `middleware`.
- The upstreams page: `addresses`, `load_balancing` (nine strategies), `sticky_cookie`, the connection and queue limits, the timeouts, the active and passive health checks, `deny_internal`; each row shows the up backends.
- The list of the handler and middleware modules (`proxy`, `static`, `respond`, `fastcgi`, `media_pages`, `media_tokens`, the streaming and WebRTC handlers; `basic_auth`, `body_limit`, `cache`, `circuit_breaker`, `client_cert`, `compress`, `concurrency_limit`, `forward_auth`, `headers`, `jwt`, `rate_limit`, `redirect`, `rewrite`, `wasm`), each linked to the reference.
- Sources: `templates/conf/conf.d/60-apps.conf`, [the config reference: Routes](../reference/config.md#routes), [the config reference: Upstreams](../reference/config.md#upstreams), [the config reference: Handlers](../reference/config.md#handlers), [the config reference: Middleware](../reference/config.md#middleware), the per-module docs in [schema/doc](../../schema/doc/).

### 16 Forward proxy

- The `forward_proxy` routes: HTTP CONNECT and plain HTTP proxying for the listed `clients`, optionally limited to `ports`; loopback, link-local, unspecified, multicast and the machine's own addresses are refused by default.
- The `socks` protocol of a listener serves SOCKS5.
- Sources: [the config reference: Handlers](../reference/config.md#http-handlers-forward_proxy), [the config reference: Listeners](../reference/config.md#listeners).

### 17 L4 proxy

- `services`: TCP connections and UDP datagrams to an upstream; TLS passed through by SNI; `proxy_protocol`; `server_names`.
- Sources: `templates/conf/conf.d/40-services.conf`, [the config reference: Services](../reference/config.md#services).

### 18 TLS

- The certificates page: the certificates the listeners serve now (file and ACME), refreshed every 30 seconds; a listener's `tls` is set on the Listeners page.
- HTTPS by default: without a listener, 443 and the redirecting 80 are generated; the fields of `auto_https`.
- ACME: the CA, the account email, the storage (directory, Redis, etcd, Consul), the validation, the on-demand issuance; the fields of `acme`.
- Sources: `templates/conf/conf.d/20-tls.conf`, [the config reference: HTTPS by default](../reference/config.md#https-by-default), [the config reference: ACME](../reference/config.md#acme), [schema/doc/acme.md](../../schema/doc/acme.md), [schema/doc/auto_https.md](../../schema/doc/auto_https.md).

### 19 Cache

- The zones page: the keys, the memory, the disk entries and bytes and the disk path of each zone; **purge** (by URL, by tag, or all).
- The `cache` middleware: caching on a route, the `zone` and the key/TTL parameters.
- Sources: [the config reference: Middleware](../reference/config.md#http-middleware-cache), [schema/doc/modules/cache.md](../../schema/doc/modules/cache.md).

### 20 Streaming

- `streaming`: the fib media hub (the streams, the players, the GOP cache limits); the RTMP, RTSP (Digest/Basic/token) and SRT (stream ID, encryption) listeners and their limits and timeouts; the WebRTC ports; each listener references a media_auth policy with `auth`.
- The channels and the push: `streaming.channels` (the playlists, the schedules, the loop, the shuffle) and `streaming.push` (the RTMP and SRT targets).
- The handlers on routes: `hls`, `httpflv`, `whip`, `whep`, `stream_whip`, `stream_whep`, `vod`.
- Sources: [streaming](../streaming.md), [the config reference: Streaming](../reference/config.md#streaming), [the config reference: Handlers](../reference/config.md#handlers).

### 21 Media auth

- The `media_auth` policies: the static `tokens` (plaintext or SHA-256), the `users` (plaintext or bcrypt), the `secret`/`secret_file`/`keys` ring that signs time-limited tokens (`fit media-token`), the `public` actions that need no credential, the stream-name patterns (`*` wildcard).
- `url_signing`: the expiring playback URLs for the audience.
- Sources: [the config reference: Media auth](../reference/config.md#media-auth), [the media library](../media-library.md#playback-urls-that-expire), [WebRTC](../webrtc.md#auth).

### 22 Media library

- The pages: **Library** (the items, the search, the filters, the paging, the scan state, rescan), **Channels** (the playlists, the schedules, the loop and the shuffle, the push targets), **Playback URLs** (the test URLs of `url_signing`, the key rotation, the sessions), **Metrics**, **Library config** (`media_library`), **VOD routes** (the `vod` routes), **Channel config** (`streaming.channels` and `streaming.push`), **URL signing** (the `url_signing` of each policy).
- The parameters of `media_library`: `roots`, `files`, `exclude`, `follow_symlinks`, the sidecars, the limits.
- Sources: [the media library](../media-library.md), [the channels](../channels.md), [the config reference: Media library](../reference/config.md#media-library), `app/workbench/web/gateway/media.js`.

### 23 WebRTC

- `webrtc.turn`: the UDP, TCP and TLS addresses, the static users and the coturn time-limited credentials, the relay port range, the quotas and the bandwidth, the allowed and refused peers.
- `webrtc.media`: the WHIP/WHEP media server (SFU): the UDP port, the public addresses, the ICE servers, the limits and the timeouts.
- `webrtc.proxy`: relaying the peers to the media server by ICE ufrag.
- Sources: [WebRTC](../webrtc.md), [the config reference: WebRTC](../reference/config.md#webrtc), [schema/doc](../../schema/doc/).

### 24 Tunnel roles

- The `tunnel` config: the hub, the node (exposing the services beside it), the proxy (the local ports and the web proxy through the hub); the TLS, WSS and QUIC links; the connection tokens (`tokens` keep only the hashes, `fit token`).
- The roles' pages: tunnel.hub (overview, nodes, proxies, tunnels, listeners, tokens, settings), tunnel.node (overview, hubs, services, tunnels), tunnel.proxy (overview, hubs, nodes, forwards, tunnels, info).
- Sources: [tunnel](../tunnel.md), [the config reference: Tunnel](../reference/config.md#tunnel), `app/workbench/web/tunnel/`.

### 25 LLM roles

- The `llm` config: the hub, the node (serving AI models from local or remote upstreams), the proxy (the OpenAI, Anthropic, Gemini, Ollama and Cohere compatible interfaces); the WebSocket mesh, the models, the proxy timeouts, the API keys; the connection tokens.
- The roles' pages: llm.hub (overview, nodes, models, proxies, traffic, peers, settings), llm.node (overview, models, GPUs, actions, jobs, traffic, peers, settings), llm.proxy (overview, models, nodes, traffic, keys, peers, settings).
- Sources: [llm](../llm.md), [the config reference: LLM](../reference/config.md#llm), `app/workbench/web/llm/`.

## Authoring conventions (maintainers and chapter tasks)

1. **Files**: two files per chapter, `NN-<slug>.zh-CN.md` (Chinese) and `NN-<slug>.md` (English),
   `NN` the two-digit chapter number. The two files are complete translations of each other, not
   summaries; the head is the title (no chapter number in it — the number is in the file name), a
   blank line, and the language switch
   `[English](NN-<slug>.md) | [简体中文](NN-<slug>.zh-CN.md)`, as in every existing doc.
2. **Chapter skeleton** (sections may be added or dropped to fit the content, but not reordered):
   - `## What it does` / `## 功能`: what the module does, 1–3 paragraphs.
   - `## Pages` / `## 页面与操作`: one `###` subsection per page of the module in the menu; what the page shows and what can be done on it.
   - `## Key parameters` / `## 参数`: only the key parameters, in a table `| field | default | description |`; the full field tables are linked to the matching chapter of [the config reference](../reference/config.md).
   - `## In .conf` / `## .conf 配置`: the .conf form of the module, one minimal but complete example (code fence language `fit`).
   - `## See also` / `## 相关文档`: links to the in-depth docs and the reference.
   Pages that edit no config (Overview, Engine, Versions, the consistency check, the Cluster, …)
   omit "Key parameters" and "In .conf", or cover only the members involved.
3. **Every .conf example must pass validation**: save the example (with its comment lines) to a
   temporary file and run `./bin/fit validate -c <temp file>`; only a passing example goes into the
   chapter. The examples are self-contained complete configs: an `upstream` without a listener is
   rejected, so add a minimal `listener` when needed. `admin` may appear in examples only in
   chapter 01 (the bootstrap file) and 02 (the users); the console pages never edit `admin`.
4. **Links**: relative paths only. Links between chapters use the file names in this directory;
   links to the other docs use `../<file>`. Heading anchors follow GitHub's rules (lower case,
   spaces to `-`, punctuation dropped, CJK kept). Every relative link is checked by
   `go test ./docs`; the target file and heading must exist.
5. **Terminology**: the names of the menu and the pages follow the i18n words of the modules in
   `app/workbench/web` (the Chinese files use the Chinese words, the English files the English
   ones); the config field names are always the English originals from the code.
6. **Length**: 150–400 lines per chapter in Chinese is the norm (the reverse proxy, the media
   library, the assistant, the cluster, the tunnel and the llm may be longer); the English file is
   the same length as the Chinese one.
7. **Do not** run load tests or benchmarks (a rule of this repository); `./bin/fit validate` and
   `go test ./docs` are fine.
8. The files listed as sources are **authoritative**: the pages' behavior follows the code in
   `app/workbench/web`, the parameters follow `schema/doc` and `docs/reference`, the .conf forms
   follow `templates/conf`.

## Sources index

| Source | Path |
| --- | --- |
| The console and the store (pages, login, the assistant, MCP) | [../console.md](../console.md) |
| The config (formats, loading, changing) | [../configuration.md](../configuration.md) |
| The config reference (every field, generated) | [../reference/config.md](../reference/config.md) |
| The .conf format | [../reference/fit.md](../reference/fit.md) |
| The complete config templates (every field, commented) | [../../templates/README.md](../../templates/README.md) |
| Deployment (image, Helm, systemd) | [../../deploy/README.md](../../deploy/README.md) |
| Getting started | [../getting-started.md](../getting-started.md) |
| Cluster management | [../cluster.md](../cluster.md) |
| Streaming | [../streaming.md](../streaming.md) |
| The media library | [../media-library.md](../media-library.md) |
| The channels | [../channels.md](../channels.md) |
| WebRTC | [../webrtc.md](../webrtc.md) |
| tunnel | [../tunnel.md](../tunnel.md) |
| llm | [../llm.md](../llm.md) |
| The module docs (handlers, middleware, ACME, …) | [../../schema/doc/](../../schema/doc/) |
| The workbench front end (the menu and the pages) | [../../app/workbench/web/](../../app/workbench/web/) |
