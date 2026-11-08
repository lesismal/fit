# The function navigation bar

[English](03-navigation.md) | [简体中文](03-navigation.zh-CN.md)

## What it does

After login the whole page is the **workbench**: the function navigation bar on the left, the
chosen page on the right. The navigation bar's first level is the flat list of the modules the
console serves, the second level is each module's pages, the third level is the features of a
port in the Engine menu. A module with one page (or one external link) is itself a link.

The bar shows only the modules the console actually serves: `admin.console` serves the config's
own modules and every role's modules running in the same process; a role's own console serves
only that role's module. Every page has its own URL, `#/<module>/<page>`, so a refresh or a
shared link goes straight to it.

## Layout

- **The navigation bar on the left**: the brand (fit and "Workbench"), the filter, the module
  tree, and the footer (the current user and logout).
- **The top bar**: the menu button (narrow screens), the title and breadcrumb, the page's tools,
  and the theme and language choices.
- **The content on the right**: the page shown.

The bar can be filtered (matching is recursive: a module or port stays visible when any of its
descendants matches), collapsed to icons, and its modules and ports can be expanded and folded;
the open state is kept in the browser. The themes are light, dark and three more; the languages
are English and Chinese.

## The modules and their pages

In menu order (the RANK of `core/workbench.js`). The small tag a module shows under its label is
in parentheses.

| Module | Pages |
| --- | --- |
| [Overview](04-overview.md) | Overview |
| [Listeners](05-listeners.md) | Listeners |
| [Engine](06-engine.md) (Ports) | One submenu per listener port; under each port its features |
| [Global settings](07-global-settings.md) | Log, Access log, Telemetry, Metrics, Profiling, Limits, Engine, Providers, Other, Import / Export |
| [JSON](08-json.md) | Config as JSON (the Draft and Effective tabs) |
| [Versions](09-versions.md) | Config versions |
| [Consistency check](10-consistency-check.md) | Consistency check |
| [Logs](11-logs.md) | Logs |
| [Cluster](12-cluster.md) | Overview, Nodes, Groups, Templates, Rollouts, Drift and alerts, Users and audit, Metrics, Logs, Certificates |
| [Account](13-account.md) | My password, Users, This node |
| [Assistant](14-assistant.md) | Functions, Skills, Proposals, Audit log, MCP, Settings |
| [Reverse proxy](15-reverse-proxy.md) (HTTP) | Routes, Upstreams |
| [Forward proxy](16-forward-proxy.md) (HTTP CONNECT) | Forward proxy |
| [L4 proxy](17-l4-proxy.md) (TCP/UDP) | Layer 4 services |
| [TLS](18-tls.md) (TLS) | Certificates, Automatic HTTPS, ACME |
| [Cache](19-cache.md) (HTTP) | Cache zones |
| [Streaming](20-streaming.md) (RTMP / RTSP / SRT) | Streaming |
| [Media auth](21-media-auth.md) (media tokens) | Media auth |
| [Media library](22-media-library.md) (VOD / channels) | Libraries, Channels, Playback URLs, Metrics, Library config, VOD routes, Channels config, URL signing |
| [WebRTC](23-webrtc.md) (WebRTC) | WebRTC |
| [Tunnel roles](24-tunnel.md) (Tunnel) | Tunnel roles |
| [LLM roles](25-llm.md) (LLM) | LLM roles |

Once a role runs, its modules appear at the end of the list: the tunnel's **Hub**, **Node** and
**proxy** (tunnel.hub, tunnel.node, tunnel.proxy), and the LLM's **Hub**, **Node** and **proxy**
(llm.hub, llm.node, llm.proxy), each with that role's pages. See [Tunnel roles](24-tunnel.md)
and [LLM roles](25-llm.md).

## The Engine menu

The Engine menu puts the same view into the bar: one submenu per listener port, in the order the
config and HTTPS by default give them. A port expands to the features it serves — the handlers
(static files, respond, FastCGI, media tokens, media pages), the reverse and forward proxy
routes, the cache (the zones and the cached routes), the other middleware, the streaming and
WebRTC handlers, the layer 4 services, SOCKS, the TLS certificates, ACME, automatic HTTPS and
its redirect — and the listener form last. The protocols, the PROXY protocol, HTTP/3, the
on-demand certificates, the client certificates, MPTCP and VOD are small tags under the port's
row. See [Engine](06-engine.md).

## Ask AI

When the assistant is available (`admin.assistant`, and the user's level is not `off`), the
ports and the entries that list routes in the bar, each route on a page, an upstream with
backends down, and so on, have an **Ask AI** button: clicking it asks the matching skill (for
example `diagnose`) and sends the object asked about as the page's selection. See
[Assistant](14-assistant.md).

## See also

- [The console and the store](../console.md#the-pages) — the full description of the workbench and its pages
- [Overview](04-overview.md) · [Engine](06-engine.md) — the two read-only views
