# The function navigation bar

[English](03-navigation.md) | [简体中文](03-navigation.zh-CN.md)

## What it does

After login the whole page is the **workbench**: the function navigation bar on the left, the
chosen page on the right. The navigation bar's first level is the flat list of the modules the
console serves, the second level is each module's pages, the third level is the features of a
port in the Engine menu. A module with one page (or one external link) is itself a link.

The bar shows only the modules the console actually serves (`GET /api/workbench` lists the server
modules): `admin.console` serves the config's own module, `admin`, which every module in the table
below calls but Cluster and Assistant; the assistant's, `ai`, with `admin.assistant`; the manager's, `cluster`,
with `admin.cluster`; and the module of every role running in the same process. A role's own
console serves only that role's module. Every page has its own URL, `#/<module>/<page>`, so a
refresh or a shared link goes straight to it; a URL that names no page goes to the module's first
page, and one that names no module to the Overview.

## Layout

- **The navigation bar on the left**: the brand (fit and "Workbench") with the button that
  collapses the bar to icons, **Filter the menu**, the module tree, and the footer (the current
  user and **Log out**).
- **The top bar**: the menu button (narrow screens), the title and breadcrumb (the module, then
  the page for a module of several), the page's tools (the Overview's **Auto refresh** and
  **Refresh**, say), the assistant's chat button when the console serves `ai`, and the theme and
  language pickers.
- **The content on the right**: the page shown.

The filter matches labels recursively: a module or port stays visible when any of its descendants
matches, and a match on a module shows all its pages. Modules are open until folded; ports are
folded until opened. The open state and the collapsed bar are kept in the browser (`localStorage`),
as are the theme (Follow system, Light, Dark, Ocean, Sunset, Graphite) and the language (English or
中文, the browser's language at first); changing the language reloads the page.

## The modules and their pages

In menu order (the RANK of `core/workbench.js`), by the label the menu shows. The small tag a
module shows under its label is in parentheses. A module of one page is a single link, so its
page has no row of its own.

| Module | Pages |
| --- | --- |
| [Overview](04-overview.md) | (one page) |
| [Listeners](05-listeners.md) | (one page) |
| [Engine](06-engine.md) (Ports) | One submenu per listener port; under each port its features |
| [Global](07-global-settings.md) | Log, Access log, Telemetry, Metrics, Profiling, Limits, Engine, Providers, Other, Import / Export |
| [JSON](08-json.md) | (one page, with the Draft and Effective tabs) |
| [Versions](09-versions.md) | (one page) |
| [Consistency](10-consistency-check.md) | (one page) |
| [Logs](11-logs.md) | (one page) |
| [Cluster](12-cluster.md), with `admin.cluster` only | Overview, Nodes, Groups, Templates, Rollouts, Drift and alerts, Users and audit, Metrics, Logs, Certificates, BitTorrent |
| [Account](13-account.md) | My password, Users, This node |
| [Assistant](14-assistant.md), with `admin.assistant` only | Functions, Skills, Proposals, Audit log, MCP, Settings |
| [Reverse proxy](15-reverse-proxy.md) (HTTP) | Routes, Upstreams |
| [Forward proxy](16-forward-proxy.md) (HTTP CONNECT) | (one page) |
| [L4 proxy](17-l4-proxy.md) (TCP/UDP) | (one page: Layer 4 services) |
| [TLS](18-tls.md) (TLS) | Certificates, Automatic HTTPS, ACME |
| [Cache](19-cache.md) (HTTP) | (one page: Cache zones) |
| [Streaming](20-streaming.md) (RTMP / RTSP / SRT) | (one page) |
| [Media auth](21-media-auth.md) (media tokens) | (one page) |
| [Media library](22-media-library.md) (VOD / channels) | Libraries, Channels, Playback URLs, Metrics, Library config, VOD routes, Channels config, URL signing |
| [WebRTC](23-webrtc.md) (WebRTC) | (one page) |
| [Tunnel roles](24-tunnel.md) (Tunnel) | (one page) |
| [LLM roles](25-llm.md) (LLM) | (one page) |
| [Sync](26-sync.md) (pipelines) | Pipelines, Sync config |
| [BitTorrent](27-bittorrent.md) (client / tracker) | Torrents, Tracker, RSS, Publishing, BitTorrent config |

Every module but Cluster and Assistant calls the config's own module, so all of them are listed
whenever `admin.console` runs, even before the part of the config they edit is set: Streaming,
Media library, Sync and BitTorrent then say what to set, and the pipelines, the client or the
tracker start when the config is applied. See [Sync](26-sync.md) and
[BitTorrent](27-bittorrent.md).

Once a role runs, its own modules appear at the end of the list too: the tunnel's **Hub**, **Node**
and **Proxy** (tunnel.hub, tunnel.node, tunnel.proxy), then the LLM's **Hub**, **Node** and
**Proxy** (llm.hub, llm.node, llm.proxy), each with that role's pages; the menu is rebuilt when a save
from the console starts or stops one (after a change made elsewhere, reload the page). See [Tunnel roles](24-tunnel.md) and [LLM roles](25-llm.md).

## The Engine menu

The Engine menu puts the same view into the bar: one submenu per listener port, in the order the
config and HTTPS by default give them, each labelled with its name and port (`web :8080`). A port
expands to the features it serves, each with its count where it has one — the handlers (Static files,
Respond, FastCGI, Media tokens, Media pages, then any other handler of its routes by its module id,
such as `sync_ingest`, `bt_stream`, `bt_publish` or `bt_tracker`), the reverse and forward proxy
routes, the cache (the zones and the cached routes), the other middleware, the streaming config with
its HLS, HTTP-FLV, WHIP and WHEP routes, its VOD routes and the channels it plays, the WebRTC config
with its routes, the layer 4 services, SOCKS, the TLS certificates, ACME, automatic HTTPS and its
redirect — and **Port config**, the listener form, last. The protocols, Auto HTTPS, the PROXY
protocol, HTTP/3, the on-demand certificates, the client certificates, MPTCP and VOD are small tags
under the port's row. See [Engine](06-engine.md).

## Ask AI

When the assistant is available (`admin.assistant`, and the user's level is not `off`), the
ports and the entries that list routes in the bar, each route on a page, an upstream with
backends down, and so on, have an **Ask AI** button (in the bar and on the Overview, an icon whose
tooltip names the skill): clicking it opens the chat panel on a new conversation with the matching
skill (for example `diagnose`), sends the object asked about as the page's selection, and asks at
once. Without the assistant there are no such buttons. See [Assistant](14-assistant.md).

## See also

- [The console and the store](../console.md#the-pages) — the full description of the workbench and its pages
- [Overview](04-overview.md) · [Engine](06-engine.md) — the two read-only views
